package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falahlaz/support-agent/internal/classify"
	"github.com/falahlaz/support-agent/internal/handoff"
	"github.com/falahlaz/support-agent/internal/kb"
	"github.com/falahlaz/support-agent/internal/llm"
	"github.com/falahlaz/support-agent/internal/orders"
	"github.com/falahlaz/support-agent/internal/policy"
	"github.com/falahlaz/support-agent/internal/session"
)

var sgt = time.FixedZone("SGT", 8*3600)

// fixedNow is 2026-10-04 12:00 SGT: SS-10003 was delivered 17 days earlier.
func fixedNow() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, sgt) }

type harness struct {
	agent *Agent
	fake  *llm.Fake
	sink  *handoff.MemSink
	store *session.MemStore
	recs  *recorder
}

type recorder struct {
	mu   sync.Mutex
	recs []Record
}

func (r *recorder) ObserveTurn(rec Record) { r.mu.Lock(); r.recs = append(r.recs, rec); r.mu.Unlock() }
func (r *recorder) all() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Record(nil), r.recs...)
}

var (
	loadOnce sync.Once
	kbIdx    *kb.Index
	store    *orders.Store
	loadErr  error
)

func data(t testing.TB) (*kb.Index, *orders.Store) {
	t.Helper()
	loadOnce.Do(func() {
		kbIdx, loadErr = kb.LoadDir("../../data/kb")
		if loadErr == nil {
			store, loadErr = orders.LoadFile("../../data/orders.json")
		}
	})
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	return kbIdx, store
}

func newHarness(t testing.TB, fn func(llm.Request) (llm.Response, error)) *harness {
	t.Helper()
	k, o := data(t)
	h := &harness{fake: &llm.Fake{Fn: fn}, sink: &handoff.MemSink{},
		store: session.NewMemStore(0, fixedNow), recs: &recorder{}}
	a, err := New(DefaultConfig(), Deps{LLM: h.fake, KB: k, Orders: o, Sessions: h.store,
		Sink: h.sink, Observer: h.recs, Now: fixedNow, NewID: func() string { return "trace" }})
	if err != nil {
		t.Fatal(err)
	}
	h.agent = a
	return h
}

func cls(intent, orderID, email string) string {
	q := func(s string) string {
		if s == "" {
			return "null"
		}
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf(`{"intent":%q,"order_id":%s,"email":%s}`, intent, q(orderID), q(email))
}

func draft(answer string, cites ...string) string {
	q := make([]string, len(cites))
	for i, c := range cites {
		q[i] = fmt.Sprintf("%q", c)
	}
	return fmt.Sprintf(`{"answer":%q,"citations":[%s]}`, answer, strings.Join(q, ","))
}

// script serves classify and draft outputs from separate queues.
func script(classifies []string, drafts ...string) func(llm.Request) (llm.Response, error) {
	return llm.ByPurpose(map[string]func(llm.Request) (llm.Response, error){
		classify.Purpose: llm.Script(classifies...),
		DraftPurpose:     llm.Script(drafts...),
	})
}

func (h *harness) purposes() map[string]int {
	n := map[string]int{}
	for _, r := range h.fake.Requests() {
		n[r.Purpose]++
	}
	return n
}

// allText concatenates everything any model request contained.
func (h *harness) allText() string {
	var b strings.Builder
	for _, r := range h.fake.Requests() {
		b.WriteString(r.System)
		for _, m := range r.Messages {
			b.WriteString("\n" + m.Text)
		}
	}
	return b.String()
}

func expect(t *testing.T, got Response, route policy.Route, reason policy.Reason) {
	t.Helper()
	if got.Route != route || got.Reason != reason {
		t.Fatalf("got %s/%s, want %s/%s (text=%q)", got.Route, got.Reason, route, reason, got.Text)
	}
	if strings.TrimSpace(got.Text) == "" || got.TraceID == "" {
		t.Fatalf("empty text or trace id: %+v", got)
	}
}

var ctx = context.Background()

func TestFAQGrounded(t *testing.T) {
	h := newHarness(t, script([]string{cls("faq", "", "")},
		draft("You have 30 days from delivery to return unworn shoes.", "returns#return-window")))
	r := h.agent.Turn(ctx, "s", "how many days do I have to return my shoes")
	expect(t, r, policy.RouteAnswer, policy.ReasonGroundedFAQ)
	if len(r.Citations) != 1 || r.Citations[0] != "returns#return-window" {
		t.Errorf("citations = %v", r.Citations)
	}
	req := h.fake.Requests()[1]
	if !strings.Contains(req.System, "[returns#return-window]") {
		t.Error("retrieved chunk not in draft context")
	}
	if strings.Contains(req.System, "[order:") {
		t.Error("FAQ draft must not carry order data")
	}
}

func TestFAQUngroundedCitationEscalates(t *testing.T) {
	for name, d := range map[string]string{
		"no citation":      draft("30 days."),
		"not retrieved":    draft("30 days.", "warranty#made-up"),
		"malformed":        `I think 30 days`,
		"too long":         draft(strings.Repeat("a", 1300), "returns#return-window"),
		"citation non str": `{"answer":"x","citations":[1]}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, script([]string{cls("faq", "", "")}, d))
			r := h.agent.Turn(ctx, "s", "how many days do I have to return my shoes")
			expect(t, r, policy.RouteEscalate, policy.ReasonUngroundedAnswer)
			if r.TicketID == "" || len(r.Citations) != 0 {
				t.Errorf("ticket=%q citations=%v", r.TicketID, r.Citations)
			}
			if h.recs.all()[0].ValidationFailure == "" {
				t.Error("validation failure kind not recorded")
			}
		})
	}
}

func TestFAQLowScoreEscalatesWithoutDraft(t *testing.T) {
	h := newHarness(t, script([]string{cls("faq", "", "")}))
	r := h.agent.Turn(ctx, "s", "do you sell bags")
	expect(t, r, policy.RouteEscalate, policy.ReasonNoGrounding)
	if h.purposes()[DraftPurpose] != 0 {
		t.Error("draft called on a non-answer route")
	}
}

func TestOrderStatusNeedsVerification(t *testing.T) {
	h := newHarness(t, script([]string{cls("order_status", "SS-10003", "")}))
	r := h.agent.Turn(ctx, "s", "where is my order SS-10003")
	expect(t, r, policy.RouteClarify, policy.ReasonNeedVerification)
	if h.purposes()[DraftPurpose] != 0 {
		t.Error("draft called before verification")
	}
	if strings.Contains(r.Text, "Lina") || strings.Contains(r.Text, "delivered") {
		t.Errorf("clarify leaks order data: %q", r.Text)
	}
}

func TestOrderStatusVerifiedSameTurn(t *testing.T) {
	h := newHarness(t, script([]string{cls("order_status", "SS-10003", "lina.wong@example.com")},
		draft("Your order was delivered on 17 September.", "order:SS-10003")))
	r := h.agent.Turn(ctx, "s", "status of SS-10003, email lina.wong@example.com")
	expect(t, r, policy.RouteAnswer, policy.ReasonVerifiedOrder)

	sys := h.fake.Requests()[1].System
	for _, want := range []string{"[order:SS-10003]", `"days_since_delivery":17`, `"has_final_sale_items":false`} {
		if !strings.Contains(sys, want) {
			t.Errorf("draft context missing %s", want)
		}
	}
	for _, leak := range []string{"Bishan", "570005", `"email"`} {
		if strings.Contains(sys, leak) {
			t.Errorf("draft context leaks %q", leak)
		}
	}
	rec := h.recs.all()[0]
	if !rec.Verified || len(rec.ToolsOffered) != 2 {
		t.Errorf("record = %+v", rec)
	}
}

func TestVerificationAcrossTwoTurns(t *testing.T) {
	h := newHarness(t, script(
		[]string{cls("order_status", "ss10003", ""), cls("order_status", "", "lina.wong@example.com")},
		draft("Delivered.", "order:SS-10003")))
	expect(t, h.agent.Turn(ctx, "s", "where is ss10003?"), policy.RouteClarify, policy.ReasonNeedVerification)
	expect(t, h.agent.Turn(ctx, "s", "lina.wong@example.com"), policy.RouteAnswer, policy.ReasonVerifiedOrder)
}

func TestWrongEmailThreeTimesEscalates(t *testing.T) {
	bad := cls("order_status", "SS-10003", "someone@else.com")
	good := cls("order_status", "SS-10003", "lina.wong@example.com")
	h := newHarness(t, script([]string{bad, bad, bad, good}))
	var texts []string
	for i := 0; i < 2; i++ {
		r := h.agent.Turn(ctx, "s", "SS-10003 someone@else.com")
		expect(t, r, policy.RouteClarify, policy.ReasonNeedVerification)
		texts = append(texts, r.Text)
	}
	r := h.agent.Turn(ctx, "s", "SS-10003 someone@else.com")
	expect(t, r, policy.RouteEscalate, policy.ReasonVerificationLimit)

	// After the limit, even the correct email must not verify (anti-enumeration).
	r = h.agent.Turn(ctx, "s", "SS-10003 lina.wong@example.com")
	expect(t, r, policy.RouteEscalate, policy.ReasonVerificationLimit)
	if h.purposes()[DraftPurpose] != 0 {
		t.Error("order data drafted after attempt limit")
	}
	for _, tk := range h.sink.Tickets() {
		if tk.CustomerVerified || tk.OrderID != "" {
			t.Errorf("unverified ticket carries order: %+v", tk)
		}
	}
}

// Unknown order and wrong email must look identical to the customer.
func TestUnknownOrderAndWrongEmailIdentical(t *testing.T) {
	h1 := newHarness(t, script([]string{cls("order_status", "SS-99999", "a@b.com")}))
	h2 := newHarness(t, script([]string{cls("order_status", "SS-10003", "a@b.com")}))
	r1 := h1.agent.Turn(ctx, "s", "x")
	r2 := h2.agent.Turn(ctx, "s", "x")
	if r1.Text != r2.Text || r1.Reason != r2.Reason {
		t.Errorf("responses differ:\n%q\n%q", r1.Text, r2.Text)
	}
}

func TestBadOrderIDFormatNotCountedAsFailure(t *testing.T) {
	h := newHarness(t, script([]string{cls("order_status", "SS-123", "lina.wong@example.com")}))
	r := h.agent.Turn(ctx, "s", "order SS-123 lina.wong@example.com")
	expect(t, r, policy.RouteClarify, policy.ReasonBadOrderIDFormat)
	if s, _ := h.store.Get("s"); s.VerifyFailures != 0 {
		t.Errorf("failures = %d, want 0", s.VerifyFailures)
	}
}

func TestVerifiedOrderAThenOrderB(t *testing.T) {
	h := newHarness(t, script(
		[]string{cls("order_status", "SS-10003", "lina.wong@example.com"), cls("order_status", "SS-10002", "")},
		draft("Delivered.", "order:SS-10003")))
	expect(t, h.agent.Turn(ctx, "s", "SS-10003 lina.wong@example.com"), policy.RouteAnswer, policy.ReasonVerifiedOrder)
	r := h.agent.Turn(ctx, "s", "and where is SS-10002?")
	expect(t, r, policy.RouteClarify, policy.ReasonNeedVerification)
	if strings.Contains(h.allText(), "NV2026093012345") {
		t.Error("order B data reached the model without verification")
	}
}

func TestInjectedGiftMessageNeverReachesModel(t *testing.T) {
	h := newHarness(t, script([]string{cls("order_status", "SS-10007", "chloe.ng@example.com")},
		draft("There are no notes I can share.", "order:SS-10007")))
	r := h.agent.Turn(ctx, "s", "any notes on my order? SS-10007 chloe.ng@example.com")
	expect(t, r, policy.RouteAnswer, policy.ReasonVerifiedOrder)
	all := h.allText()
	for _, leak := range []string{"ignore all previous", "pre-approved", "gift_message"} {
		if strings.Contains(all, leak) {
			t.Errorf("model saw %q", leak)
		}
	}
}

func TestOrderActionEscalatesWithFacts(t *testing.T) {
	h := newHarness(t, script([]string{cls("order_action", "SS-10005", "aisha.r@example.com")}))
	r := h.agent.Turn(ctx, "s", "refund my order SS-10005, email aisha.r@example.com")
	expect(t, r, policy.RouteEscalate, policy.ReasonActionNeedsHuman)
	if h.purposes()[DraftPurpose] != 0 {
		t.Error("draft called on action route")
	}
	tk := h.sink.Tickets()[0]
	if !tk.CustomerVerified || tk.OrderID != "SS-10005" || tk.Facts == nil ||
		!tk.Facts.HasFinalSaleItems || *tk.Facts.DaysSinceDelivery != 11 || tk.Facts.Status != "delivered" {
		t.Errorf("ticket = %+v facts=%+v", tk, tk.Facts)
	}
	if strings.Contains(tk.Summary, "aisha.r@example.com") {
		t.Errorf("ticket summary not redacted: %q", tk.Summary)
	}
	if !strings.Contains(r.Text, tk.ID) {
		t.Errorf("response missing ticket ref: %q", r.Text)
	}
}

func TestMedicalRefusedWithoutDraft(t *testing.T) {
	h := newHarness(t, script([]string{cls("medical", "", "")}))
	r := h.agent.Turn(ctx, "s", "will these help my plantar fasciitis?")
	expect(t, r, policy.RouteRefuse, policy.ReasonMedicalAdvice)
	if h.purposes()[DraftPurpose] != 0 || r.TicketID != "" {
		t.Error("refuse must not draft or ticket")
	}
}

func TestPaymentDataNeverSentToModel(t *testing.T) {
	h := newHarness(t, script([]string{cls("faq", "", "")}, draft("Shipping is free over 80.", "shipping#shipping-fees")))
	r := h.agent.Turn(ctx, "s", "my card is 4111 1111 1111 1111 cvv 123, can you charge it?")
	expect(t, r, policy.RouteRefuse, policy.ReasonPaymentData)
	if n := len(h.fake.Requests()); n != 0 {
		t.Fatalf("model called %d times for a payment message", n)
	}
	// The next turn's history must not carry the card number either.
	h.agent.Turn(ctx, "s", "is shipping free")
	if strings.Contains(h.allText(), "4111") {
		t.Error("card number reached the model through history")
	}
	if strings.Contains(h.recs.all()[0].Message, "4111") {
		t.Error("card number in turn record")
	}
}

func TestTooLongNotSentToModel(t *testing.T) {
	h := newHarness(t, script(nil))
	r := h.agent.Turn(ctx, "s", strings.Repeat("é", 2001))
	expect(t, r, policy.RouteClarify, policy.ReasonMessageTooLong)
	if len(h.fake.Requests()) != 0 {
		t.Error("too-long message sent to model")
	}
	r = h.agent.Turn(ctx, "s2", strings.Repeat("é", 2000))
	if r.Reason == policy.ReasonMessageTooLong {
		t.Error("2000 runes should be accepted")
	}
}

func TestClassifierFailures(t *testing.T) {
	cases := []struct {
		name   string
		fn     func(llm.Request) (llm.Response, error)
		reason policy.Reason
		calls  int
	}{
		{"malformed twice", script([]string{"nope", "{}"}), policy.ReasonClassifierFailure, 2},
		{"unknown label", script([]string{cls("refund_now", "", ""), cls("refund_now", "", "")}), policy.ReasonClassifierFailure, 2},
		{"llm down", func(llm.Request) (llm.Response, error) {
			return llm.Response{}, fmt.Errorf("%w: 503", llm.ErrUnavailable)
		}, policy.ReasonLLMUnavailable, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.fn)
			r := h.agent.Turn(ctx, "s", "hello there")
			expect(t, r, policy.RouteEscalate, c.reason)
			if n := len(h.fake.Requests()); n != c.calls {
				t.Errorf("calls = %d, want %d", n, c.calls)
			}
			if rec := h.recs.all()[0]; rec.LLMCalls != c.calls || rec.Error == "" {
				t.Errorf("record = %+v", rec)
			}
		})
	}
}

func TestDraftLLMUnavailable(t *testing.T) {
	h := newHarness(t, llm.ByPurpose(map[string]func(llm.Request) (llm.Response, error){
		classify.Purpose: llm.Script(cls("faq", "", "")),
		DraftPurpose: func(llm.Request) (llm.Response, error) {
			return llm.Response{}, fmt.Errorf("%w: timeout", llm.ErrUnavailable)
		},
	}))
	r := h.agent.Turn(ctx, "s", "is shipping free")
	expect(t, r, policy.RouteEscalate, policy.ReasonLLMUnavailable)
	if rec := h.recs.all()[0]; !rec.LLMError || rec.LLMCalls != 2 {
		t.Errorf("record = %+v", rec)
	}
}

func TestTicketFailureNoRef(t *testing.T) {
	h := newHarness(t, script([]string{cls("human_request", "", "")}))
	h.sink.Err = errors.New("disk full")
	r := h.agent.Turn(ctx, "s", "get me a human")
	expect(t, r, policy.RouteEscalate, policy.ReasonCustomerAskedHuman)
	if r.TicketID != "" || strings.Contains(r.Text, "ref ") || strings.Contains(r.Text, "passed this") {
		t.Errorf("claims a ticket that was not created: %+v", r)
	}
	if !h.recs.all()[0].HandoffFailed {
		t.Error("handoff failure not recorded")
	}
}

func TestPanicRecovered(t *testing.T) {
	h := newHarness(t, func(llm.Request) (llm.Response, error) { panic("boom") })
	r := h.agent.Turn(ctx, "s", "hello")
	expect(t, r, policy.RouteEscalate, policy.ReasonInternalError)
	if rec := h.recs.all(); len(rec) != 1 || !strings.Contains(rec[0].Error, "boom") {
		t.Errorf("records = %+v", rec)
	}
}

type panicSink struct{}

func (panicSink) Create(context.Context, handoff.Ticket) (string, error) { panic("sink boom") }

func TestPanickingSinkDuringRecover(t *testing.T) {
	h := newHarness(t, func(llm.Request) (llm.Response, error) { panic("boom") })
	h.agent.sink = panicSink{}
	r := h.agent.Turn(ctx, "s", "hello")
	expect(t, r, policy.RouteEscalate, policy.ReasonInternalError)
	if r.TicketID != "" {
		t.Error("ticket id without a ticket")
	}
}

func TestOneRecordPerTurn(t *testing.T) {
	h := newHarness(t, script([]string{cls("unclear", "", ""), cls("faq", "", "")}))
	h.agent.Turn(ctx, "s", "hi")                      // template
	h.agent.Turn(ctx, "s", "4111 1111 1111 1111")     // pre-check
	h.agent.Turn(ctx, "s", "do you sell bags")        // escalate
	h.agent.Turn(ctx, "s", strings.Repeat("x", 3000)) // too long
	if n := len(h.recs.all()); n != 4 {
		t.Fatalf("records = %d, want 4", n)
	}
	s, _ := h.store.Get("s")
	if len(s.History) != 4 {
		t.Errorf("history = %d, want 4", len(s.History))
	}
}

// Every non-answer route makes no draft call, and get_order is never
// offered to an unverified customer (I2, I3) across a sweep of intents.
func TestToolInvariantsEndToEnd(t *testing.T) {
	intents := []string{"faq", "order_status", "order_action", "medical", "complaint",
		"human_request", "unclear", "out_of_scope"}
	emails := []string{"", "lina.wong@example.com", "wrong@example.com"}
	for _, in := range intents {
		for _, em := range emails {
			h := newHarness(t, script([]string{cls(in, "SS-10003", em)}, draft("ok", "order:SS-10003")))
			r := h.agent.Turn(ctx, "s", "return window for SS-10003")
			rec := h.recs.all()[0]
			drafted := h.purposes()[DraftPurpose] > 0
			if r.Route != policy.RouteAnswer && drafted && r.Reason != policy.ReasonUngroundedAnswer {
				t.Errorf("%s/%q: drafted on route %s", in, em, r.Route)
			}
			for _, tool := range rec.ToolsOffered {
				if tool == policy.ToolGetOrder && !rec.Verified {
					t.Errorf("%s/%q: get_order offered unverified", in, em)
				}
			}
			if !rec.Verified && strings.Contains(h.allText(), "[order:") {
				t.Errorf("%s/%q: order context sent unverified", in, em)
			}
		}
	}
}

func TestConcurrentWrongEmailsCountedExactly(t *testing.T) {
	bad := cls("order_status", "SS-10003", "x@y.com")
	h := newHarness(t, func(r llm.Request) (llm.Response, error) {
		return llm.Response{Text: bad}, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); h.agent.Turn(ctx, "s", "SS-10003 x@y.com") }()
	}
	wg.Wait()
	s, _ := h.store.Get("s")
	// Lookups stop at the limit, so the counter ends exactly at it.
	if s.VerifyFailures != DefaultConfig().Policy.MaxVerifyFailures {
		t.Errorf("failures = %d, want %d", s.VerifyFailures, DefaultConfig().Policy.MaxVerifyFailures)
	}
}

func FuzzTurn(f *testing.F) {
	for _, s := range []string{"hi", `{"intent":"faq"}`, cls("order_status", "SS-10003", "lina.wong@example.com"),
		draft("x", "order:SS-10003"), "4111 1111 1111 1111", "\x00\xff", strings.Repeat("a", 2500)} {
		f.Add(s, s)
	}
	f.Fuzz(func(t *testing.T, msg, modelOut string) {
		h := newHarness(t, func(llm.Request) (llm.Response, error) { return llm.Response{Text: modelOut}, nil })
		r := h.agent.Turn(ctx, "s", msg)
		switch r.Route {
		case policy.RouteAnswer, policy.RouteClarify, policy.RouteEscalate, policy.RouteRefuse:
		default:
			t.Fatalf("invalid route %q", r.Route)
		}
		if strings.TrimSpace(r.Text) == "" {
			t.Fatal("empty text")
		}
		if r.Route == policy.RouteAnswer && len(r.Citations) == 0 {
			t.Fatal("answer without citation")
		}
	})
}

// A verified session asking a general question gets only search_kb, so
// the order must not be in the draft context (I2).
func TestVerifiedSessionFAQGetsNoOrderContext(t *testing.T) {
	h := newHarness(t, script(
		[]string{cls("order_status", "SS-10003", "lina.wong@example.com"), cls("faq", "", "")},
		draft("Delivered.", "order:SS-10003"),
		draft("Shipping is free over the threshold.", "shipping#shipping-fees")))
	expect(t, h.agent.Turn(ctx, "s", "SS-10003 lina.wong@example.com"), policy.RouteAnswer, policy.ReasonVerifiedOrder)
	r := h.agent.Turn(ctx, "s", "is shipping free")
	expect(t, r, policy.RouteAnswer, policy.ReasonGroundedFAQ)
	if sys := h.fake.Requests()[3].System; strings.Contains(sys, "[order:") {
		t.Error("FAQ draft carried order data although get_order was not allowed")
	}
}
