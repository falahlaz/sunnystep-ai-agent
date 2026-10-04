package policy

import (
	"slices"
	"testing"
)

func TestDecide(t *testing.T) {
	cfg := DefaultConfig()
	cases := []struct {
		name   string
		in     Input
		route  Route
		reason Reason
		tools  []Tool
	}{
		{"grounded faq", Input{Intent: IntentFAQ, KBTopScore: 5.2}, RouteAnswer, ReasonGroundedFAQ, []Tool{ToolSearchKB}},
		{"faq at threshold", Input{Intent: IntentFAQ, KBTopScore: 3.0}, RouteAnswer, ReasonGroundedFAQ, []Tool{ToolSearchKB}},
		{"faq weak match", Input{Intent: IntentFAQ, KBTopScore: 2.7}, RouteEscalate, ReasonNoGrounding, nil},
		{"faq no match", Input{Intent: IntentFAQ}, RouteEscalate, ReasonNoGrounding, nil},

		{"status verified", Input{Intent: IntentOrderStatus, OrderVerified: true}, RouteAnswer, ReasonVerifiedOrder, []Tool{ToolGetOrder, ToolSearchKB}},
		{"status unverified", Input{Intent: IntentOrderStatus}, RouteClarify, ReasonNeedVerification, nil},
		{"status 2 failures", Input{Intent: IntentOrderStatus, VerifyFailures: 2}, RouteClarify, ReasonNeedVerification, nil},
		{"status 3 failures", Input{Intent: IntentOrderStatus, VerifyFailures: 3}, RouteEscalate, ReasonVerificationLimit, nil},

		// Actions escalate even when the customer is verified.
		{"action verified", Input{Intent: IntentOrderAction, OrderVerified: true}, RouteEscalate, ReasonActionNeedsHuman, nil},
		{"action unverified", Input{Intent: IntentOrderAction}, RouteEscalate, ReasonActionNeedsHuman, nil},

		// A strong KB match must not unlock a medical answer.
		{"medical with kb hit", Input{Intent: IntentMedical, KBTopScore: 9}, RouteRefuse, ReasonMedicalAdvice, nil},
		{"complaint", Input{Intent: IntentComplaint}, RouteEscalate, ReasonComplaint, nil},
		{"human", Input{Intent: IntentHumanRequest}, RouteEscalate, ReasonCustomerAskedHuman, nil},
		{"unclear", Input{Intent: IntentUnclear}, RouteClarify, ReasonUnclear, nil},
		{"out of scope", Input{Intent: IntentOutOfScope}, RouteRefuse, ReasonOutOfScope, nil},
		{"unknown fails closed", Input{Intent: "issue_refund"}, RouteEscalate, ReasonUnknownIntent, nil},
		{"empty fails closed", Input{}, RouteEscalate, ReasonUnknownIntent, nil},
	}
	for _, c := range cases {
		d := Decide(cfg, c.in)
		if d.Route != c.route || d.Reason != c.reason || !slices.Equal(d.AllowedTools, c.tools) {
			t.Errorf("%s: got %v, want %s/%s tools=%v", c.name, d, c.route, c.reason, c.tools)
		}
	}
}

// Invariant: only RouteAnswer may carry tools, and get_order only ever
// appears for a verified customer.
func TestToolInvariants(t *testing.T) {
	cfg := DefaultConfig()
	intents := []Intent{IntentFAQ, IntentOrderStatus, IntentOrderAction, IntentMedical,
		IntentComplaint, IntentHumanRequest, IntentUnclear, IntentOutOfScope, "bogus"}
	for _, i := range intents {
		for _, verified := range []bool{false, true} {
			for _, score := range []float64{0, 2.9, 3, 10} {
				for _, fails := range []int{0, 5} {
					in := Input{Intent: i, KBTopScore: score, OrderVerified: verified, VerifyFailures: fails}
					d := Decide(cfg, in)
					if d.Route != RouteAnswer && len(d.AllowedTools) > 0 {
						t.Errorf("%+v: non-answer route has tools %v", in, d.AllowedTools)
					}
					if slices.Contains(d.AllowedTools, ToolGetOrder) && !verified {
						t.Errorf("%+v: get_order allowed without verification", in)
					}
				}
			}
		}
	}
}

func TestParseIntent(t *testing.T) {
	if i, err := ParseIntent("faq"); err != nil || i != IntentFAQ {
		t.Fatalf("faq: %v %v", i, err)
	}
	for _, bad := range []string{"", "FAQ", "refund", "faq "} {
		if _, err := ParseIntent(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

// Every Decision that Decide can return must be listed in AllOutcomes,
// otherwise respond's template coverage test would not see it.
func TestDecideOutcomesListed(t *testing.T) {
	listed := map[Outcome]bool{}
	for _, o := range AllOutcomes() {
		listed[o] = true
	}
	intents := []Intent{IntentFAQ, IntentOrderStatus, IntentOrderAction, IntentMedical,
		IntentComplaint, IntentHumanRequest, IntentUnclear, IntentOutOfScope, "bogus"}
	cfg := DefaultConfig()
	for _, in := range intents {
		for _, score := range []float64{0, 10} {
			for _, v := range []bool{false, true} {
				for _, f := range []int{0, 5} {
					d := Decide(cfg, Input{Intent: in, KBTopScore: score, OrderVerified: v, VerifyFailures: f})
					if !listed[Outcome{d.Route, d.Reason}] {
						t.Errorf("Decide produced %s/%s, not in AllOutcomes", d.Route, d.Reason)
					}
				}
			}
		}
	}
}
