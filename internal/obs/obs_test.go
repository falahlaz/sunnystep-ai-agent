package obs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/falahlaz/support-agent/internal/agent"
	"github.com/falahlaz/support-agent/internal/policy"
)

var prdFields = []string{"trace_id", "session_id", "intent", "route", "reason", "kb_top_score",
	"kb_top_chunk", "verified", "verify_failures", "tools_offered", "citations", "ticket_id",
	"llm_calls", "llm_latency_ms", "input_tokens", "output_tokens", "total_latency_ms", "error"}

func TestTurnEventHasPRDFieldsAndNoPII(t *testing.T) {
	var buf bytes.Buffer
	o := &Observer{Log: slog.New(slog.NewJSONHandler(&buf, nil)), Metrics: NewMetrics()}
	o.ObserveTurn(agent.Record{
		TraceID: "t1", SessionID: "s1", Route: policy.RouteEscalate, Reason: policy.ReasonActionNeedsHuman,
		// Simulate a caller that forgot to redact.
		Message: "refund SS-10003 lina.wong@example.com +65 9123 4567",
		Error:   "lookup for lina.wong@example.com failed",
	})
	var ev map[string]any
	if err := json.Unmarshal(buf.Bytes(), &ev); err != nil {
		t.Fatalf("not one JSON line: %q", buf.String())
	}
	if ev["msg"] != "turn" {
		t.Errorf("msg = %v", ev["msg"])
	}
	for _, f := range prdFields {
		if _, ok := ev[f]; !ok {
			t.Errorf("missing field %s", f)
		}
	}
	line := buf.String()
	for _, pii := range []string{"lina.wong", "9123 4567"} {
		if strings.Contains(line, pii) {
			t.Errorf("log contains PII %q: %s", pii, line)
		}
	}
	if !strings.Contains(line, "SS-10003") {
		t.Error("order id should stay in logs for operators")
	}
}

func TestMetrics(t *testing.T) {
	m := NewMetrics()
	o := &Observer{Metrics: m}
	o.ObserveTurn(agent.Record{Route: policy.RouteAnswer, Reason: policy.ReasonGroundedFAQ})
	o.ObserveTurn(agent.Record{Route: policy.RouteEscalate, Reason: policy.ReasonUngroundedAnswer, ValidationFailure: "unknown_citation"})
	o.ObserveTurn(agent.Record{Route: policy.RouteEscalate, Reason: policy.ReasonLLMUnavailable, LLMError: true, HandoffFailed: true})
	if m.TurnsTotal.Value() != 3 || m.LLMErrorsTotal.Value() != 1 || m.HandoffFailuresTotal.Value() != 1 {
		t.Errorf("counters: turns=%d llm=%d handoff=%d", m.TurnsTotal.Value(), m.LLMErrorsTotal.Value(), m.HandoffFailuresTotal.Value())
	}
	if m.RouteTotal.Get("escalate").String() != "2" || m.ValidationFailures.Get("unknown_citation").String() != "1" {
		t.Errorf("maps: %s %s", m.RouteTotal.String(), m.ValidationFailures.String())
	}
}
