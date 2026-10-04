package agent

import (
	"github.com/falahlaz/support-agent/internal/policy"
)

// Record is everything observable about one turn. The agent hands exactly
// one Record per Turn to the Observer, on every path including panics.
// It never holds raw customer text: Message is PII-redacted.
type Record struct {
	TraceID        string
	SessionID      string
	Message        string // redacted; empty for payment-data messages
	Intent         policy.Intent
	Route          policy.Route
	Reason         policy.Reason
	KBTopScore     float64
	KBTopChunk     string
	Verified       bool
	VerifyFailures int
	ToolsOffered   []policy.Tool
	Citations      []string
	TicketID       string
	HandoffFailed  bool
	// ValidationFailure is the guard check that rejected a draft
	// ("no_citation", "unknown_citation", "too_long", "malformed",
	// "action_claim"), or empty.
	ValidationFailure string
	LLMCalls          int
	LLMLatencyMS      int64
	InputTokens       int
	OutputTokens      int
	LLMError          bool
	TotalLatencyMS    int64
	Error             string
}

type Observer interface {
	ObserveTurn(Record)
}

type nopObserver struct{}

func (nopObserver) ObserveTurn(Record) {}
