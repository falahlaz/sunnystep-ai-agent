package classify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/falahlaz/support-agent/internal/llm"
	"github.com/falahlaz/support-agent/internal/policy"
	"github.com/falahlaz/support-agent/internal/session"
)

func run(t *testing.T, texts ...string) (Result, Usage, error, *llm.Fake) {
	t.Helper()
	f := &llm.Fake{Fn: llm.Script(texts...)}
	r, u, err := Classify(context.Background(), f, "msg", nil)
	return r, u, err, f
}

func TestParseValid(t *testing.T) {
	cases := []struct {
		out  string
		want Result
	}{
		{`{"intent":"faq","order_id":null,"email":null}`, Result{Intent: policy.IntentFAQ}},
		{`{"intent":"order_status","order_id":"ss10003","email":" Lina.Wong@example.com "}`,
			Result{Intent: policy.IntentOrderStatus, OrderID: "SS-10003", Email: "Lina.Wong@example.com"}},
		{`{"intent":"order_status","order_id":"SS-123","email":null}`,
			Result{Intent: policy.IntentOrderStatus, OrderIDInvalid: true}},
		{`{"intent":"order_action"}`, Result{Intent: policy.IntentOrderAction}}, // missing optional keys
		{"```json\n{\"intent\":\"medical\",\"order_id\":null,\"email\":null}\n```", Result{Intent: policy.IntentMedical}},
	}
	for _, c := range cases {
		r, u, err, _ := run(t, c.out)
		if err != nil || r != c.want || u.Calls != 1 {
			t.Errorf("%s:\n got %+v calls=%d err=%v\nwant %+v", c.out, r, u.Calls, err, c.want)
		}
	}
}

func TestMalformedRetriedOnceThenFails(t *testing.T) {
	bad := []string{
		`not json`,
		`{"intent":"issue_refund"}`, // unknown label
		`{"intent":42}`,
		`{"intent":"faq","order_id":12345}`, // wrong type
		`[]`,
		``,
	}
	for _, b := range bad {
		_, u, err, _ := run(t, b, b)
		if !errors.Is(err, ErrClassifierFailure) || u.Calls != 2 {
			t.Errorf("%q: err=%v calls=%d, want ErrClassifierFailure after 2 calls", b, err, u.Calls)
		}
	}
}

func TestRetryRecovers(t *testing.T) {
	r, u, err, _ := run(t, `oops`, `{"intent":"faq","order_id":null,"email":null}`)
	if err != nil || r.Intent != policy.IntentFAQ || u.Calls != 2 {
		t.Fatalf("r=%+v calls=%d err=%v", r, u.Calls, err)
	}
}

func TestLLMErrorPassesThrough(t *testing.T) {
	f := &llm.Fake{Fn: func(llm.Request) (llm.Response, error) {
		return llm.Response{}, fmt.Errorf("%w: 503", llm.ErrUnavailable)
	}}
	_, _, err := Classify(context.Background(), f, "msg", nil)
	if !errors.Is(err, llm.ErrUnavailable) || errors.Is(err, ErrClassifierFailure) {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.Requests()); n != 1 {
		t.Errorf("calls = %d; the llm client owns transport retries", n)
	}
}

func TestRequestHistoryCapped(t *testing.T) {
	var h []session.Turn
	for i := 0; i < 10; i++ {
		h = append(h, session.Turn{User: fmt.Sprintf("u%d", i), Assistant: fmt.Sprintf("a%d", i)})
	}
	req := Request("now", h)
	if len(req.Messages) != 2*MaxHistory+1 {
		t.Fatalf("messages = %d", len(req.Messages))
	}
	if req.Messages[0].Text != "u4" || req.Messages[len(req.Messages)-1].Text != "now" {
		t.Errorf("wrong window: first=%q last=%q", req.Messages[0].Text, req.Messages[len(req.Messages)-1].Text)
	}
	if req.Schema == nil || req.Purpose != Purpose {
		t.Error("request must carry schema and purpose")
	}
}

// The PRD's boundary cases must be in the prompt as examples.
func TestPromptHasBoundaryExamples(t *testing.T) {
	for _, s := range []string{"can I return worn shoes?", "please return my order SS-10003",
		"will these help my bunions?", "is my refund done?", "more restrictive"} {
		if !strings.Contains(systemPrompt, s) {
			t.Errorf("prompt missing %q", s)
		}
	}
	for _, name := range intentNames() {
		if !strings.Contains(systemPrompt, "- "+name.(string)+":") {
			t.Errorf("prompt does not define label %s", name)
		}
	}
}
