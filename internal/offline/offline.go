// Package offline is a keyword-rule stand-in for the model, so the binary
// runs without an API key (fresh-clone check, demos, LLM_PROVIDER=fake).
// It is deliberately simple; it exercises the same control layer as the
// real model but says nothing about real-model quality.
package offline

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/falahlaz/support-agent/internal/llm"
)

type Client struct{}

var (
	orderRe = regexp.MustCompile(`(?i)\bss[- ]?\d+\b`)
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	ctxIDRe = regexp.MustCompile(`(?m)^\[([^\]]+)\]`)
)

// rules are checked in order: most restrictive first (D15).
var rules = []struct {
	intent string
	re     *regexp.Regexp
}{
	{"medical", regexp.MustCompile(`(?i)bunion|plantar|fasciitis|arthritis|diabet|podiatr|injur|knee pain|back pain|heal my|cure|flat feet`)},
	{"order_action", regexp.MustCompile(`(?i)refund (me|my)|cancel|change (my |the )?(delivery )?address|exchange|return (my|this|the) order|replace|lost|send .* back|issue .*refund`)},
	{"complaint", regexp.MustCompile(`(?i)worst|terrible|lawyer|legal|sue\b|ridiculous|unacceptable|scam|furious`)},
	{"human_request", regexp.MustCompile(`(?i)human|real person|speak to (someone|a person)|live agent`)},
	{"order_status", regexp.MustCompile(`(?i)where is|status|track|arrive|refund (done|processed)|my order|delivered|notes on my order`)},
	{"out_of_scope", regexp.MustCompile(`(?i)poem|weather|joke|bitcoin|recipe`)},
	{"faq", regexp.MustCompile(`(?i)return|refund|ship|deliver|size|wide|wash|clean|warranty|guarantee|store|hours|gift card|discount|sell|pay|japan|exchange|insole`)},
}

func (Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	var out any
	switch req.Purpose {
	case "classify":
		out = classify(req.Messages)
	case "draft":
		out = draft(req.System)
	default:
		out = map[string]any{}
	}
	b, _ := json.Marshal(out)
	m, _ := llm.DecodeJSON(string(b))
	return llm.Response{Text: string(b), JSON: m}, nil
}

func classify(msgs []llm.Message) map[string]any {
	last := msgs[len(msgs)-1].Text
	intent := "unclear"
	for _, r := range rules {
		if r.re.MatchString(last) {
			intent = r.intent
			break
		}
	}
	// Order ID / email may come from earlier user turns.
	var orderID, email any
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != llm.RoleUser {
			continue
		}
		if orderID == nil {
			if m := orderRe.FindString(msgs[i].Text); m != "" {
				orderID = m
			}
		}
		if email == nil {
			if m := emailRe.FindString(msgs[i].Text); m != "" {
				email = m
			}
		}
	}
	// A bare ID/email continues the earlier order request.
	if intent == "unclear" && (orderID != nil || email != nil) {
		intent = "order_status"
	}
	return map[string]any{"intent": intent, "order_id": orderID, "email": email}
}

// draft answers extractively: the first sentence of the first context
// entry, cited by its ID.
func draft(system string) map[string]any {
	_, ctxText, _ := strings.Cut(system, "CONTEXT:\n")
	ids := ctxIDRe.FindAllStringSubmatchIndex(ctxText, -1)
	if len(ids) == 0 {
		return map[string]any{"answer": "I don't have that information.", "citations": []string{}}
	}
	id := ctxText[ids[0][2]:ids[0][3]]
	body := ctxText[ids[0][1]:]
	if len(ids) > 1 {
		body = ctxText[ids[0][1]:ids[1][0]]
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	text := strings.Join(lines[1:], " ")
	if strings.HasPrefix(id, "order:") && len(lines) > 1 {
		var v struct {
			ID, Status, Carrier string
			Tracking            string `json:"tracking_number"`
		}
		json.Unmarshal([]byte(lines[1]), &v)
		text = "Your order " + v.ID + " is " + v.Status + "."
		if v.Tracking != "" {
			text += " " + v.Carrier + " tracking number: " + v.Tracking + "."
		}
	} else if i := strings.Index(text, ". "); i > 0 {
		text = text[:i+1]
	}
	return map[string]any{"answer": strings.TrimSpace(text), "citations": []string{id}}
}
