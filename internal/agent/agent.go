// Package agent runs one customer turn end to end (PRD §3).
//
// The model is called at most twice per turn: once to classify, once to
// draft an answer. Everything between those calls is deterministic Go:
// verification, retrieval, the policy decision, the context the drafting
// model may see, validation, templates and handoff.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/falahlaz/support-agent/internal/classify"
	"github.com/falahlaz/support-agent/internal/guard"
	"github.com/falahlaz/support-agent/internal/handoff"
	"github.com/falahlaz/support-agent/internal/kb"
	"github.com/falahlaz/support-agent/internal/llm"
	"github.com/falahlaz/support-agent/internal/orders"
	"github.com/falahlaz/support-agent/internal/policy"
	"github.com/falahlaz/support-agent/internal/respond"
	"github.com/falahlaz/support-agent/internal/session"
)

type Config struct {
	Policy        policy.Config
	MaxMessageLen int // runes; longer messages are not sent to the model
	KBTopK        int
	MaxAnswerLen  int // runes
}

func DefaultConfig() Config {
	return Config{Policy: policy.DefaultConfig(), MaxMessageLen: 2000, KBTopK: 4, MaxAnswerLen: 1200}
}

type Deps struct {
	LLM      llm.Client
	KB       *kb.Index
	Orders   *orders.Store
	Sessions session.Store
	Sink     handoff.Sink
	Observer Observer         // optional
	Now      func() time.Time // required: business logic never calls time.Now
	NewID    func() string    // optional trace ID generator
}

type Agent struct {
	cfg      Config
	llm      llm.Client
	kb       *kb.Index
	orders   *orders.Store
	sessions session.Store
	sink     handoff.Sink
	obs      Observer
	now      func() time.Time
	newID    func() string
	// locks serialises turns per session so concurrent requests cannot lose
	// a VerifyFailures increment (which would weaken the attempt limit).
	locks [64]sync.Mutex
}

func New(cfg Config, d Deps) (*Agent, error) {
	if d.LLM == nil || d.KB == nil || d.Orders == nil || d.Sessions == nil || d.Sink == nil || d.Now == nil {
		return nil, errors.New("agent: missing dependency")
	}
	a := &Agent{cfg: cfg, llm: d.LLM, kb: d.KB, orders: d.Orders, sessions: d.Sessions,
		sink: d.Sink, obs: d.Observer, now: d.Now, newID: d.NewID}
	if a.obs == nil {
		a.obs = nopObserver{}
	}
	if a.newID == nil {
		a.newID = randomID
	}
	return a, nil
}

type Response struct {
	Text      string        `json:"text"`
	Route     policy.Route  `json:"route"`
	Reason    policy.Reason `json:"reason"`
	Citations []string      `json:"citations,omitempty"`
	TicketID  string        `json:"ticket_id,omitempty"`
	TraceID   string        `json:"trace_id"`
}

// turn carries the state of one Turn between steps.
type turn struct {
	rec     Record
	sess    *session.Session
	msg     string
	intent  policy.Intent
	view    *orders.View
	facts   *orderFacts
	focusID string // the order this turn is about, verified or not
	// historyText replaces the customer's message in session history when
	// the message must never reach the model later (payment data, too long).
	historyText string
}

// Turn handles one customer message. It never panics and always returns a
// Response with a route and text.
func (a *Agent) Turn(ctx context.Context, sessionID, message string) (resp Response) {
	start := a.now()
	t := &turn{msg: message, rec: Record{TraceID: a.newID(), SessionID: sessionID}}

	mu := &a.locks[stripe(sessionID)]
	mu.Lock()
	defer mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			t.rec.Error = fmt.Sprintf("panic: %v", r)
			resp = a.escalate(ctx, t, policy.ReasonInternalError)
		}
		resp.TraceID = t.rec.TraceID
		t.rec.Route, t.rec.Reason = resp.Route, resp.Reason
		t.rec.Citations, t.rec.TicketID = resp.Citations, resp.TicketID
		if t.sess != nil {
			t.rec.VerifyFailures = t.sess.VerifyFailures
			a.saveSession(t, resp)
		}
		t.rec.TotalLatencyMS = a.now().Sub(start).Milliseconds()
		a.obs.ObserveTurn(t.rec)
	}()

	sess, ok := a.sessions.Get(sessionID)
	if !ok {
		sess = &session.Session{ID: sessionID}
	}
	t.sess = sess
	return a.run(ctx, t)
}

func (a *Agent) run(ctx context.Context, t *turn) Response {
	msg := t.msg

	// Pre-checks: these messages are never sent to the model.
	if utf8.RuneCountInString(msg) > a.cfg.MaxMessageLen {
		t.historyText = "[message too long, not processed]"
		t.rec.Message = guard.RedactPII(truncate(msg, 200))
		return a.template(t, policy.RouteClarify, policy.ReasonMessageTooLong)
	}
	if guard.LooksLikePaymentData(msg) {
		t.historyText = "[message withheld: contained payment details]"
		return a.template(t, policy.RouteRefuse, policy.ReasonPaymentData)
	}
	t.rec.Message = guard.RedactPII(msg)
	if strings.TrimSpace(msg) == "" {
		return a.template(t, policy.RouteClarify, policy.ReasonUnclear)
	}

	// 3. Classify.
	cls, usage, err := classify.Classify(ctx, a.llm, msg, t.sess.History)
	a.addUsage(t, usage.Calls, usage.InputTokens, usage.OutputTokens, usage.Latency)
	if err != nil {
		t.rec.Error = err.Error()
		if errors.Is(err, classify.ErrClassifierFailure) {
			return a.escalate(ctx, t, policy.ReasonClassifierFailure)
		}
		t.rec.LLMError = true
		return a.escalate(ctx, t, policy.ReasonLLMUnavailable)
	}
	t.intent = cls.Intent
	t.rec.Intent = cls.Intent

	// 4a. Verification. Hints from the model mean nothing until Lookup
	// accepts the (order ID, email) pair.
	if cls.OrderIDInvalid && cls.Intent == policy.IntentOrderStatus {
		return a.template(t, policy.RouteClarify, policy.ReasonBadOrderIDFormat)
	}
	a.verify(t, cls)

	// 4b. Retrieval.
	hits := a.kb.Search(msg, a.cfg.KBTopK)
	if len(hits) > 0 {
		t.rec.KBTopScore, t.rec.KBTopChunk = hits[0].Score, hits[0].ChunkID
	}

	// 5. Policy.
	verified := t.view != nil
	t.rec.Verified = verified
	d := policy.Decide(a.cfg.Policy, policy.Input{
		Intent:         cls.Intent,
		KBTopScore:     t.rec.KBTopScore,
		OrderVerified:  verified,
		VerifyFailures: t.sess.VerifyFailures,
	})
	t.rec.ToolsOffered = d.AllowedTools

	// 6. Route.
	switch d.Route {
	case policy.RouteAnswer:
		return a.answer(ctx, t, d, hits)
	case policy.RouteEscalate:
		return a.escalate(ctx, t, d.Reason)
	default:
		return a.template(t, d.Route, d.Reason)
	}
}

// verify updates session verification state from this turn's hints and
// sets t.view only if the order in focus is verified for this session.
func (a *Agent) verify(t *turn, cls classify.Result) {
	s := t.sess
	id := cls.OrderID
	if id == "" && cls.Email != "" {
		id = s.PendingOrderID // ID on an earlier turn, email now
	}

	// Once the attempt limit is reached no further lookups run in this
	// session, so a correct guess after the limit cannot verify (D6).
	locked := s.VerifyFailures >= a.cfg.Policy.MaxVerifyFailures
	if id != "" && cls.Email != "" && !s.Verified(id) && !locked {
		view, err := a.orders.Lookup(id, cls.Email)
		switch {
		case err == nil:
			s.VerifiedOrderID, s.VerifiedEmail = view.ID, cls.Email
			s.VerifyFailures, s.PendingOrderID = 0, ""
		case errors.Is(err, orders.ErrNotVerified):
			s.VerifyFailures++
			s.PendingOrderID = id
		}
	} else if id != "" && !s.Verified(id) {
		s.PendingOrderID = id
	}

	t.focusID = id
	if t.focusID == "" {
		t.focusID = s.VerifiedOrderID
	}
	if t.focusID == "" || !s.Verified(t.focusID) {
		return
	}
	view, err := a.orders.Lookup(t.focusID, s.VerifiedEmail)
	if err != nil {
		return // order data changed under us; treat as unverified
	}
	f := computeFacts(view, a.now())
	t.view, t.facts = &view, &f
}

func (a *Agent) answer(ctx context.Context, t *turn, d policy.Decision, hits []kb.Hit) Response {
	in := buildDraftInput(d.AllowedTools, hits, t.view, t.facts)
	res, err := a.draft(ctx, in, t.msg, t.sess.History)
	a.addUsage(t, 1, res.resp.InputTokens, res.resp.OutputTokens, res.resp.Latency)
	if err != nil {
		t.rec.Error = err.Error()
		if errors.Is(err, llm.ErrUnavailable) || errors.Is(err, llm.ErrRejected) {
			t.rec.LLMError = true
			return a.escalate(ctx, t, policy.ReasonLLMUnavailable)
		}
		return a.escalate(ctx, t, policy.ReasonInternalError)
	}
	if res.failure != "" {
		t.rec.ValidationFailure = res.failure
		reason := policy.ReasonUngroundedAnswer
		if res.failure == "action_claim" {
			reason = policy.ReasonActionClaim
		}
		return a.escalate(ctx, t, reason)
	}
	return Response{Text: res.answer, Route: policy.RouteAnswer, Reason: d.Reason, Citations: res.citations}
}

func (a *Agent) template(t *turn, route policy.Route, reason policy.Reason) Response {
	text, ok := respond.Text(route, reason, "")
	if !ok {
		t.rec.Error = fmt.Sprintf("no template for %s/%s", route, reason)
		text = respond.Fallback
	}
	return Response{Text: text, Route: route, Reason: reason}
}

func (a *Agent) escalate(ctx context.Context, t *turn, reason policy.Reason) Response {
	tk := handoff.Ticket{
		CreatedAt: a.now(),
		TraceID:   t.rec.TraceID,
		SessionID: t.rec.SessionID,
		Reason:    string(reason),
		Intent:    string(t.intent),
		Summary:   t.rec.Message, // already redacted; the sink redacts again
	}
	if t.view != nil {
		tk.CustomerVerified = true
		tk.OrderID = t.view.ID
		tk.Facts = &handoff.Facts{Status: string(t.view.Status), HasFinalSaleItems: t.facts.HasFinalSaleItems,
			DaysSinceDelivery: t.facts.DaysSinceDelivery}
	}
	id, err := a.createTicket(ctx, tk)
	if err != nil {
		t.rec.HandoffFailed = true
		t.rec.Error = strings.TrimPrefix(t.rec.Error+"; ", "; ") + "handoff: " + err.Error()
		id = ""
	}
	text, ok := respond.Text(policy.RouteEscalate, reason, id)
	if !ok {
		text = respond.Fallback
	}
	return Response{Text: text, Route: policy.RouteEscalate, Reason: reason, TicketID: id}
}

// createTicket isolates the sink: escalate also runs from Turn's recover,
// so a panicking sink must surface as an error, not a second panic.
func (a *Agent) createTicket(ctx context.Context, tk handoff.Ticket) (id string, err error) {
	defer func() {
		if r := recover(); r != nil {
			id, err = "", fmt.Errorf("sink panic: %v", r)
		}
	}()
	return a.sink.Create(ctx, tk)
}

func (a *Agent) saveSession(t *turn, resp Response) {
	user := t.msg
	if t.historyText != "" {
		user = t.historyText
	}
	t.sess.AppendTurn(session.Turn{User: user, Assistant: resp.Text})
	a.sessions.Put(t.sess)
}

func (a *Agent) addUsage(t *turn, calls, in, out int, lat time.Duration) {
	t.rec.LLMCalls += calls
	t.rec.InputTokens += in
	t.rec.OutputTokens += out
	t.rec.LLMLatencyMS += lat.Milliseconds()
}

func stripe(id string) int {
	h := fnv.New32a()
	h.Write([]byte(id))
	return int(h.Sum32() % 64)
}

func randomID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
