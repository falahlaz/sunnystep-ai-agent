// Package obs turns agent.Records into one structured log event per turn
// and expvar counters.
package obs

import (
	"context"
	"expvar"
	"log/slog"

	"github.com/falahlaz/support-agent/internal/agent"
	"github.com/falahlaz/support-agent/internal/guard"
)

// Metrics holds the counters. They are created unpublished so tests can
// make as many as they like; the binary calls Publish once.
type Metrics struct {
	TurnsTotal           *expvar.Int
	RouteTotal           *expvar.Map
	ReasonTotal          *expvar.Map
	LLMErrorsTotal       *expvar.Int
	ValidationFailures   *expvar.Map
	HandoffFailuresTotal *expvar.Int
}

func NewMetrics() *Metrics {
	return &Metrics{
		TurnsTotal:           new(expvar.Int),
		RouteTotal:           new(expvar.Map).Init(),
		ReasonTotal:          new(expvar.Map).Init(),
		LLMErrorsTotal:       new(expvar.Int),
		ValidationFailures:   new(expvar.Map).Init(),
		HandoffFailuresTotal: new(expvar.Int),
	}
}

// Publish registers the counters under their PRD names at /debug/vars.
// It panics if called twice (expvar's rule), so only main calls it.
func (m *Metrics) Publish() {
	expvar.Publish("turns_total", m.TurnsTotal)
	expvar.Publish("route_total", m.RouteTotal)
	expvar.Publish("reason_total", m.ReasonTotal)
	expvar.Publish("llm_errors_total", m.LLMErrorsTotal)
	expvar.Publish("validation_failures_total", m.ValidationFailures)
	expvar.Publish("handoff_failures_total", m.HandoffFailuresTotal)
}

// Observer implements agent.Observer.
type Observer struct {
	Log     *slog.Logger
	Metrics *Metrics
}

func (o *Observer) ObserveTurn(r agent.Record) {
	if m := o.Metrics; m != nil {
		m.TurnsTotal.Add(1)
		m.RouteTotal.Add(string(r.Route), 1)
		m.ReasonTotal.Add(string(r.Reason), 1)
		if r.LLMError {
			m.LLMErrorsTotal.Add(1)
		}
		if r.ValidationFailure != "" {
			m.ValidationFailures.Add(r.ValidationFailure, 1)
		}
		if r.HandoffFailed {
			m.HandoffFailuresTotal.Add(1)
		}
	}
	if o.Log == nil {
		return
	}
	level := slog.LevelInfo
	if r.HandoffFailed || r.Reason == "internal_error" {
		level = slog.LevelError
	} else if r.Error != "" {
		level = slog.LevelWarn
	}
	tools := make([]string, len(r.ToolsOffered))
	for i, t := range r.ToolsOffered {
		tools[i] = string(t)
	}
	o.Log.LogAttrs(context.Background(), level, "turn",
		slog.String("trace_id", r.TraceID),
		slog.String("session_id", r.SessionID),
		// Redacted again here: logging must not depend on every caller
		// having done it (PRD I7).
		slog.String("message", guard.RedactPII(r.Message)),
		slog.String("intent", string(r.Intent)),
		slog.String("route", string(r.Route)),
		slog.String("reason", string(r.Reason)),
		slog.Float64("kb_top_score", r.KBTopScore),
		slog.String("kb_top_chunk", r.KBTopChunk),
		slog.Bool("verified", r.Verified),
		slog.Int("verify_failures", r.VerifyFailures),
		slog.Any("tools_offered", tools),
		slog.Any("citations", nonNil(r.Citations)),
		slog.String("ticket_id", r.TicketID),
		slog.String("validation_failure", r.ValidationFailure),
		slog.Int("llm_calls", r.LLMCalls),
		slog.Int64("llm_latency_ms", r.LLMLatencyMS),
		slog.Int("input_tokens", r.InputTokens),
		slog.Int("output_tokens", r.OutputTokens),
		slog.Int64("total_latency_ms", r.TotalLatencyMS),
		slog.String("error", guard.RedactPII(r.Error)),
	)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
