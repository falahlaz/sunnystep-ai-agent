package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

type OpenAIConfig struct {
	APIKey  string
	Model   string        // required; no default so the model is always an explicit choice
	BaseURL string        // default https://api.openai.com; tests point this at httptest
	Timeout time.Duration // per attempt; default 15s
	Backoff func() time.Duration
}

// OpenAI calls the Chat Completions API with plain net/http (no SDK, so no
// new dependency). Same retry policy as Anthropic: one retry on
// 429/5xx/timeout/network, none on other 4xx. See DECISIONS.md D25.
type OpenAI struct {
	http    *http.Client
	key     string
	model   string
	baseURL string
	timeout time.Duration
	backoff func() time.Duration
	now     func() time.Time
}

func NewOpenAI(cfg OpenAIConfig) (*OpenAI, error) {
	if cfg.Model == "" {
		return nil, errors.New("llm: openai needs LLM_MODEL")
	}
	if cfg.APIKey == "" {
		return nil, errors.New("llm: openai needs OPENAI_API_KEY")
	}
	o := &OpenAI{
		http:    &http.Client{},
		key:     cfg.APIKey,
		model:   cfg.Model,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		timeout: cfg.Timeout,
		backoff: cfg.Backoff,
		now:     time.Now,
	}
	if o.baseURL == "" {
		o.baseURL = "https://api.openai.com"
	}
	if o.timeout == 0 {
		o.timeout = 15 * time.Second
	}
	if o.backoff == nil {
		o.backoff = func() time.Duration {
			return 300*time.Millisecond + time.Duration(rand.Int64N(int64(500*time.Millisecond)))
		}
	}
	return o, nil
}

type oaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type oaRequest struct {
	Model               string         `json:"model"`
	Messages            []oaMessage    `json:"messages"`
	MaxCompletionTokens int            `json:"max_completion_tokens"`
	ResponseFormat      map[string]any `json:"response_format,omitempty"`
}

type oaResponse struct {
	Choices []struct {
		Message struct {
			Content string  `json:"content"`
			Refusal *string `json:"refusal"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// statusError carries the HTTP status so the retry rule can see it.
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string { return fmt.Sprintf("status %d: %s", e.code, e.body) }

func (o *OpenAI) Complete(ctx context.Context, req Request) (Response, error) {
	body, err := json.Marshal(o.body(req))
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	start := o.now()
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Response{}, fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
			case <-time.After(o.backoff()):
			}
		}
		out, err := o.once(ctx, body)
		if err == nil {
			return o.toResponse(req, out, o.now().Sub(start))
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		var se *statusError
		if errors.As(err, &se) && se.code != 429 && se.code < 500 {
			break
		}
	}
	var se *statusError
	if errors.As(lastErr, &se) && se.code != 429 && se.code < 500 {
		return Response{}, fmt.Errorf("%w: %v", ErrRejected, lastErr)
	}
	return Response{}, fmt.Errorf("%w: %v", ErrUnavailable, lastErr)
}

// body builds the request. Temperature is not sent, matching the Anthropic
// client (D10); several current OpenAI models also reject it.
func (o *OpenAI) body(req Request) oaRequest {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	var msgs []oaMessage
	if req.System != "" {
		msgs = append(msgs, oaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		role := "user"
		if m.Role == RoleAssistant {
			role = "assistant"
		}
		msgs = append(msgs, oaMessage{Role: role, Content: m.Text})
	}
	b := oaRequest{Model: o.model, Messages: msgs, MaxCompletionTokens: maxTokens}
	if req.Schema != nil {
		name := req.Purpose
		if name == "" {
			name = "output"
		}
		b.ResponseFormat = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   name,
				"schema": req.Schema,
				"strict": true,
			},
		}
	}
	return b
}

func (o *OpenAI) once(ctx context.Context, body []byte) (*oaResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+o.key)
	resp, err := o.http.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg := string(raw)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, &statusError{code: resp.StatusCode, body: msg}
	}
	var out oaResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

func (o *OpenAI) toResponse(req Request, out *oaResponse, latency time.Duration) (Response, error) {
	if len(out.Choices) == 0 {
		return Response{}, fmt.Errorf("%w: no choices in response", ErrUnavailable)
	}
	msg := out.Choices[0].Message
	if msg.Refusal != nil && *msg.Refusal != "" {
		return Response{}, fmt.Errorf("%w: model refused", ErrRejected)
	}
	resp := Response{
		Text:         msg.Content,
		InputTokens:  out.Usage.PromptTokens,
		OutputTokens: out.Usage.CompletionTokens,
		Latency:      latency,
	}
	if req.Schema != nil {
		resp.JSON, _ = DecodeJSON(resp.Text)
	}
	return resp, nil
}
