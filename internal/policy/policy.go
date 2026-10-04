// Package policy decides what the agent is allowed to do on a turn.
//
// The LLM classifies intent; this package (plain Go, no model) maps the
// classification plus verified facts to a route and a tool allowlist. The
// model never decides to perform an action and never sees tools it is not
// allowed to call this turn. Unknown inputs fail closed (escalate).
package policy

import "fmt"

type Intent string

const (
	IntentFAQ          Intent = "faq"           // general question answerable from the KB
	IntentOrderStatus  Intent = "order_status"  // read-only question about a specific order
	IntentOrderAction  Intent = "order_action"  // refund, return, cancel, exchange, address change...
	IntentMedical      Intent = "medical"       // whether a shoe treats/relieves a condition
	IntentComplaint    Intent = "complaint"     // angry customer, legal threat, damage claim
	IntentHumanRequest Intent = "human_request" // customer explicitly asks for a person
	IntentUnclear      Intent = "unclear"       // cannot tell what the customer wants
	IntentOutOfScope   Intent = "out_of_scope"  // unrelated to the store
)

type Route string

const (
	RouteAnswer   Route = "answer"
	RouteClarify  Route = "clarify"
	RouteEscalate Route = "escalate"
	RouteRefuse   Route = "refuse"
)

type Tool string

const (
	ToolSearchKB Tool = "search_kb"
	ToolGetOrder Tool = "get_order"
)

// Reason codes are stable strings: they go into logs, metrics and handoff
// tickets, so dashboards can count them.
type Reason string

const (
	ReasonGroundedFAQ        Reason = "grounded_faq"
	ReasonVerifiedOrder      Reason = "verified_order"
	ReasonNeedVerification   Reason = "need_order_verification"
	ReasonVerificationLimit  Reason = "verification_attempts_exceeded"
	ReasonActionNeedsHuman   Reason = "action_requires_human"
	ReasonNoGrounding        Reason = "no_kb_grounding"
	ReasonMedicalAdvice      Reason = "medical_advice"
	ReasonComplaint          Reason = "complaint"
	ReasonCustomerAskedHuman Reason = "customer_requested_human"
	ReasonUnclear            Reason = "unclear_request"
	ReasonOutOfScope         Reason = "out_of_scope"
	ReasonUnknownIntent      Reason = "unknown_intent"
)

type Config struct {
	// MinKBScore is the BM25 score below which a FAQ answer is not attempted.
	// Calibrated on the sample KB: off-topic queries scored 2.2-2.7,
	// on-topic 4.2-5.9 (see docs/DECISIONS.md). A coarse filter only; the
	// citation check is the second line of defence.
	MinKBScore float64
	// MaxVerifyFailures before handing over to a human (anti-enumeration).
	MaxVerifyFailures int
}

func DefaultConfig() Config {
	return Config{MinKBScore: 3.0, MaxVerifyFailures: 3}
}

// Input holds only facts produced by deterministic code, plus the intent
// label (which has already been validated against the Intent enum).
type Input struct {
	Intent         Intent
	KBTopScore     float64
	OrderVerified  bool
	VerifyFailures int
}

type Decision struct {
	Route        Route
	Reason       Reason
	AllowedTools []Tool
}

func (d Decision) String() string {
	return fmt.Sprintf("%s/%s tools=%v", d.Route, d.Reason, d.AllowedTools)
}

func Decide(cfg Config, in Input) Decision {
	switch in.Intent {
	case IntentHumanRequest:
		return Decision{Route: RouteEscalate, Reason: ReasonCustomerAskedHuman}

	case IntentMedical:
		return Decision{Route: RouteRefuse, Reason: ReasonMedicalAdvice}

	case IntentComplaint:
		return Decision{Route: RouteEscalate, Reason: ReasonComplaint}

	case IntentOrderAction:
		// Never executed by the agent, verified or not. The handoff ticket
		// records whether the customer was verified.
		return Decision{Route: RouteEscalate, Reason: ReasonActionNeedsHuman}

	case IntentOrderStatus:
		if in.OrderVerified {
			return Decision{Route: RouteAnswer, Reason: ReasonVerifiedOrder,
				AllowedTools: []Tool{ToolGetOrder, ToolSearchKB}}
		}
		if in.VerifyFailures >= cfg.MaxVerifyFailures {
			return Decision{Route: RouteEscalate, Reason: ReasonVerificationLimit}
		}
		return Decision{Route: RouteClarify, Reason: ReasonNeedVerification}

	case IntentFAQ:
		if in.KBTopScore < cfg.MinKBScore {
			return Decision{Route: RouteEscalate, Reason: ReasonNoGrounding}
		}
		return Decision{Route: RouteAnswer, Reason: ReasonGroundedFAQ,
			AllowedTools: []Tool{ToolSearchKB}}

	case IntentUnclear:
		return Decision{Route: RouteClarify, Reason: ReasonUnclear}

	case IntentOutOfScope:
		return Decision{Route: RouteRefuse, Reason: ReasonOutOfScope}
	}
	return Decision{Route: RouteEscalate, Reason: ReasonUnknownIntent}
}

// ParseIntent validates a label coming from the model. Anything outside the
// enum is rejected so the caller can fail closed.
func ParseIntent(s string) (Intent, error) {
	switch i := Intent(s); i {
	case IntentFAQ, IntentOrderStatus, IntentOrderAction, IntentMedical,
		IntentComplaint, IntentHumanRequest, IntentUnclear, IntentOutOfScope:
		return i, nil
	}
	return "", fmt.Errorf("policy: unknown intent %q", s)
}
