package respond

import (
	"strings"
	"testing"

	"github.com/falahlaz/support-agent/internal/policy"
)

func TestEveryOutcomeHasTemplate(t *testing.T) {
	for _, o := range policy.AllOutcomes() {
		if o.Route == policy.RouteAnswer {
			continue // model-drafted, validated by guard
		}
		for _, ticket := range []string{"T-0001", ""} {
			s, ok := Text(o.Route, o.Reason, ticket)
			if !ok || strings.TrimSpace(s) == "" {
				t.Errorf("no template for %s/%s (ticket=%q)", o.Route, o.Reason, ticket)
			}
		}
	}
}

func TestAnswerRouteHasNoTemplate(t *testing.T) {
	if _, ok := Text(policy.RouteAnswer, policy.ReasonGroundedFAQ, ""); ok {
		t.Error("answer route must not use a template")
	}
	if _, ok := Text(policy.RouteEscalate, "made_up_reason", ""); ok {
		t.Error("unknown reason must report missing template")
	}
}

func TestEscalateTicketRef(t *testing.T) {
	s, _ := Text(policy.RouteEscalate, policy.ReasonActionNeedsHuman, "T-0042")
	if !strings.Contains(s, "T-0042") {
		t.Errorf("missing ticket ref: %q", s)
	}
	s, _ = Text(policy.RouteEscalate, policy.ReasonActionNeedsHuman, "")
	if strings.Contains(s, "ref ") || strings.Contains(s, "passed this") {
		t.Errorf("no-ticket message must not claim the request was logged: %q", s)
	}
}

// The verification-limit message must not reveal whether the order exists.
func TestVerificationLimitDoesNotLeak(t *testing.T) {
	s, _ := Text(policy.RouteEscalate, policy.ReasonVerificationLimit, "T-1")
	low := strings.ToLower(s)
	for _, bad := range []string{"not found", "doesn't exist", "does not exist", "no order", "wrong email", "email doesn't match", "incorrect email"} {
		if strings.Contains(low, bad) {
			t.Errorf("message leaks verification detail %q: %s", bad, s)
		}
	}
}
