// Package llm is the only place the agent talks to a language model.
//
// Callers see a small Client interface. The model's output is untrusted: the
// client returns the raw text and a best-effort JSON decode, and the caller
// validates every field before using it (fail closed, PRD I6).
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role Role
	Text string
}

type Request struct {
	// Purpose labels the call in logs and in fake-client scripts
	// ("classify", "draft"). It is never sent to the model.
	Purpose   string
	System    string
	Messages  []Message
	MaxTokens int
	// Schema is a JSON schema the response must satisfy. When set, the
	// provider is asked for structured output and Response.JSON is filled.
	Schema map[string]any
}

type Response struct {
	Text         string
	JSON         map[string]any // nil if Schema was unset or the text is not a JSON object
	InputTokens  int
	OutputTokens int
	Latency      time.Duration
}

type Client interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// ErrUnavailable is returned when the model could not be reached after
// retries (timeout, 429, 5xx, network). The orchestrator escalates with
// reason llm_unavailable.
var ErrUnavailable = errors.New("llm unavailable")

// ErrRejected is returned for a non-retryable provider error (4xx other than
// 429): bad request, auth, unknown model. Treated like ErrUnavailable by the
// orchestrator, but kept separate so logs show a configuration problem.
var ErrRejected = errors.New("llm request rejected")

// DecodeJSON parses a JSON object out of model text. It tolerates a
// surrounding markdown code fence, nothing more: anything else is the
// caller's validation problem.
func DecodeJSON(text string) (map[string]any, error) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("llm: json is not an object")
	}
	return out, nil
}
