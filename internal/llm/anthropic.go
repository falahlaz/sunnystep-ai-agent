package llm

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const DefaultModel = "claude-sonnet-5-5"

type AnthropicConfig struct {
	APIKey  string
	Model   string        // default DefaultModel
	BaseURL string        // tests point this at an httptest server
	Timeout time.Duration // per attempt; default 15s
	// Backoff before the single retry; default 300-800ms with jitter.
	// Tests set it to return 0.
	Backoff func() time.Duration
}

// Anthropic calls the Claude Messages API. Retry policy is ours, not the
// SDK's: exactly one retry on 429/5xx/timeout/network, none on other 4xx.
type Anthropic struct {
	client  anthropic.Client
	model   string
	timeout time.Duration
	backoff func() time.Duration
	now     func() time.Time
}

func NewAnthropic(cfg AnthropicConfig) *Anthropic {
	opts := []option.RequestOption{option.WithMaxRetries(0)}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	a := &Anthropic{
		client:  anthropic.NewClient(opts...),
		model:   cfg.Model,
		timeout: cfg.Timeout,
		backoff: cfg.Backoff,
		now:     time.Now,
	}
	if a.model == "" {
		a.model = DefaultModel
	}
	if a.timeout == 0 {
		a.timeout = 15 * time.Second
	}
	if a.backoff == nil {
		a.backoff = func() time.Duration {
			return 300*time.Millisecond + time.Duration(rand.Int64N(int64(500*time.Millisecond)))
		}
	}
	return a
}

func (a *Anthropic) Complete(ctx context.Context, req Request) (Response, error) {
	params := a.params(req)
	start := a.now()
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Response{}, fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
			case <-time.After(a.backoff()):
			}
		}
		msg, err := a.once(ctx, params)
		if err == nil {
			return a.toResponse(req, msg, a.now().Sub(start))
		}
		lastErr = err
		if !retryable(ctx, err) {
			break
		}
	}
	var apierr *anthropic.Error
	if errors.As(lastErr, &apierr) && apierr.StatusCode < 500 && apierr.StatusCode != 429 {
		return Response{}, fmt.Errorf("%w: status %d: %v", ErrRejected, apierr.StatusCode, lastErr)
	}
	return Response{}, fmt.Errorf("%w: %v", ErrUnavailable, lastErr)
}

func (a *Anthropic) once(ctx context.Context, p anthropic.MessageNewParams) (*anthropic.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	return a.client.Messages.New(ctx, p)
}

// params builds the request. Temperature is not sent: the default model
// (Sonnet 5.5) rejects non-default sampling values. Effort is set to low;
// classification and short grounded drafts do not benefit from more.
// See docs/DECISIONS.md D10.
func (a *Anthropic) params(req Request) anthropic.MessageNewParams {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	msgs := make([]anthropic.MessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		block := anthropic.NewTextBlock(m.Text)
		if m.Role == RoleAssistant {
			msgs = append(msgs, anthropic.NewAssistantMessage(block))
		} else {
			msgs = append(msgs, anthropic.NewUserMessage(block))
		}
	}
	p := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: int64(maxTokens),
		Messages:  msgs,
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortLow,
		},
	}
	if req.System != "" {
		p.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	if req.Schema != nil {
		p.OutputConfig.Format = anthropic.JSONOutputFormatParam{Schema: req.Schema}
	}
	return p
}

func (a *Anthropic) toResponse(req Request, msg *anthropic.Message, latency time.Duration) (Response, error) {
	if msg.StopReason == anthropic.StopReasonRefusal {
		return Response{}, fmt.Errorf("%w: model refused (%s)", ErrRejected, msg.StopDetails.Category)
	}
	var sb strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			sb.WriteString(t.Text)
		}
	}
	resp := Response{
		Text:         sb.String(),
		InputTokens:  int(msg.Usage.InputTokens),
		OutputTokens: int(msg.Usage.OutputTokens),
		Latency:      latency,
	}
	if req.Schema != nil {
		// A decode failure is not an error here: the caller sees JSON == nil
		// and applies its own retry/fail-closed rule.
		resp.JSON, _ = DecodeJSON(resp.Text)
	}
	return resp, nil
}

func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false // caller gave up; do not spend more time
	}
	var apierr *anthropic.Error
	if errors.As(err, &apierr) {
		return apierr.StatusCode == 429 || apierr.StatusCode >= 500
	}
	return true // per-attempt timeout or transport error
}
