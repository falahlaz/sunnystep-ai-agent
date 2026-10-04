// Package respond holds the fixed customer-facing messages for every
// non-answer route (PRD I8). These are never model-generated: a jailbroken
// or confused model cannot change what a refusal or handoff says.
package respond

import (
	"fmt"

	"github.com/falahlaz/support-agent/internal/policy"
)

const handoffWithRef = "I've passed this to our customer care team (ref %s). They'll reply by email within 1 business day."

// handoffNoRef is used when the ticket could not be created. It must not
// claim the request was logged.
const handoffNoRef = "I'm not able to help with this here, and I couldn't pass it on to our team automatically just now. Please try again in a few minutes or contact our customer care team directly."

var clarify = map[policy.Reason]string{
	policy.ReasonNeedVerification: "I can help with that. To look up your order, please send your order number (format SS-12345) and the email address you used at checkout.",
	policy.ReasonBadOrderIDFormat: "That order number doesn't look right. Order numbers look like SS-12345 (\"SS-\" followed by five digits). Could you check and send it again, with the email used at checkout?",
	policy.ReasonUnclear:          "Sorry, I'm not sure what you need yet. Could you tell me a bit more? For example: a question about sizing, shipping or returns, or the status of an order.",
	policy.ReasonMessageTooLong:   "That message is too long for me to process. Could you send a shorter version (under 2,000 characters) with the main question?",
}

var refuse = map[policy.Reason]string{
	policy.ReasonMedicalAdvice: "I'm not able to give advice about medical or foot conditions. A podiatrist or doctor is the right person for that. If it helps, I can describe the comfort features of our shoes, such as cushioning, arch support and width options.",
	policy.ReasonOutOfScope:    "Sorry, I can only help with questions about Stridewell shoes, orders, shipping, sizing, care and returns.",
	policy.ReasonPaymentData:   "Please don't share card details in chat. For your security, I haven't processed or stored that message. If you have a payment question, our customer care team can help.",
}

// escalateIntro gives a short reason-specific lead-in before the handoff
// sentence. Reasons without an entry use the handoff sentence alone.
var escalateIntro = map[policy.Reason]string{
	policy.ReasonActionNeedsHuman:   "Changes to an order, such as refunds, returns, cancellations, exchanges or address changes, are handled by our team.",
	policy.ReasonCustomerAskedHuman: "Of course.",
	policy.ReasonComplaint:          "I'm sorry about your experience.",
	policy.ReasonNoGrounding:        "I don't have information on that.",
	// Deliberately says nothing about whether the order exists (D6).
	policy.ReasonVerificationLimit: "I wasn't able to verify those order details.",
	policy.ReasonUnknownIntent:     "",
	policy.ReasonClassifierFailure: "",
	policy.ReasonLLMUnavailable:    "",
	policy.ReasonUngroundedAnswer:  "",
	policy.ReasonActionClaim:       "",
	policy.ReasonInternalError:     "",
}

// Text returns the message for a non-answer route. ticketID may be empty
// for escalations whose ticket failed to save. ok is false when no template
// exists (answer routes, or a reason missing from the tables).
func Text(route policy.Route, reason policy.Reason, ticketID string) (string, bool) {
	switch route {
	case policy.RouteClarify:
		s, ok := clarify[reason]
		return s, ok
	case policy.RouteRefuse:
		s, ok := refuse[reason]
		return s, ok
	case policy.RouteEscalate:
		intro, ok := escalateIntro[reason]
		if !ok {
			return "", false
		}
		handoff := handoffNoRef
		if ticketID != "" {
			handoff = fmt.Sprintf(handoffWithRef, ticketID)
		}
		if intro == "" {
			return handoff, true
		}
		return intro + " " + handoff, true
	}
	return "", false
}

// Fallback is used if Text has no template, which the coverage test should
// make impossible. It is a safe handoff message with no ticket claim.
const Fallback = handoffNoRef
