package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falahlaz/support-agent/internal/agent"
	"github.com/falahlaz/support-agent/internal/kb"
	"github.com/falahlaz/support-agent/internal/offline"
	"github.com/falahlaz/support-agent/internal/orders"
	"github.com/falahlaz/support-agent/internal/policy"
)

func loadCases(t *testing.T) []Case {
	t.Helper()
	cs, err := LoadDir("../../eval/cases")
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// The case set must keep the PRD §4.10 minimum per category. This guards
// against cases being deleted to make a report look better.
func TestCaseSetMeetsPRDMinimum(t *testing.T) {
	min := map[string]int{"faq_grounded": 8, "faq_ungrounded": 5, "order_status": 6,
		"verification": 5, "action": 6, "safety": 6, "unclear": 4}
	got := map[string]int{}
	for _, c := range loadCases(t) {
		got[c.Category]++
	}
	for cat, n := range min {
		if got[cat] < n {
			t.Errorf("category %s has %d cases, PRD minimum is %d", cat, got[cat], n)
		}
	}
}

// Every reason a case expects must be a real reason code, so a typo cannot
// make a case unpassable (or a renamed code silently untested).
func TestCaseExpectationsUseKnownCodes(t *testing.T) {
	routes, reasons := map[string]bool{}, map[string]bool{}
	for _, o := range policy.AllOutcomes() {
		routes[string(o.Route)], reasons[string(o.Reason)] = true, true
	}
	for _, c := range loadCases(t) {
		for i, tr := range c.Turns {
			for _, r := range tr.Expect.Route {
				if !routes[r] {
					t.Errorf("%s turn %d: unknown route %q", c.ID, i+1, r)
				}
			}
			for _, r := range tr.Expect.Reason {
				if !reasons[r] {
					t.Errorf("%s turn %d: unknown reason %q", c.ID, i+1, r)
				}
			}
		}
	}
}

// CI subset: cases marked "ci" run through the real agent with the offline
// provider. These exercise the control layer (verification, safety,
// actions); model quality is measured by `cmd/eval --provider anthropic`.
func TestCICasesPassOffline(t *testing.T) {
	idx, err := kb.LoadDir("../../data/kb")
	if err != nil {
		t.Fatal(err)
	}
	store, err := orders.LoadFile("../../data/orders.json")
	if err != nil {
		t.Fatal(err)
	}
	var ci []Case
	for _, c := range loadCases(t) {
		if c.CI {
			ci = append(ci, c)
		}
	}
	if len(ci) < 20 {
		t.Fatalf("only %d ci cases", len(ci))
	}
	runs := Run(context.Background(), ci, Options{KB: idx, Orders: store, Client: offline.Client{}, Parallel: 4})
	for _, r := range runs {
		if !r.Pass {
			t.Errorf("%s failed:\n%s", r.CaseID, Markdown(Summarize(Meta{}, []CaseRun{r}), []CaseRun{r}))
		}
	}
}

func TestOneOf(t *testing.T) {
	var e Expect
	if err := json.Unmarshal([]byte(`{"route":"answer","reason":["a","b"]}`), &e); err != nil {
		t.Fatal(err)
	}
	if len(e.Route) != 1 || len(e.Reason) != 2 || !e.Reason.ok("b") || e.Reason.ok("c") || !(OneOf{}).ok("x") {
		t.Errorf("OneOf = %+v", e)
	}
}

func TestCheck(t *testing.T) {
	yes, no := true, false
	empty := []string{}
	resp := agent.Response{Route: policy.RouteEscalate, Reason: policy.ReasonActionNeedsHuman,
		TicketID: "T-1", Text: "Passed to our team (ref T-1)."}
	rec := agent.Record{}
	if f := Check(Expect{Route: OneOf{"escalate"}, Ticket: &yes, MustContain: []string{"OUR TEAM"},
		MustNotContain: []string{"refund has been"}, ToolsOffered: &empty}, resp, rec); len(f) != 0 {
		t.Errorf("unexpected failures: %v", f)
	}
	f := Check(Expect{Route: OneOf{"answer"}, Ticket: &no, CitationsInclude: []string{"x#y"},
		MustNotContain: []string{"ref"}}, resp, rec)
	if len(f) != 4 {
		t.Errorf("want 4 failures, got %v", f)
	}
}

func TestLoadDirRejectsTyposAndDuplicates(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.json"), []byte(`[{"id":"x","category":"c","turns":[{"user":"u","expect":{"rout":"answer"}}]}]`), 0o644)
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "rout") {
		t.Errorf("typo in expect field should fail, got %v", err)
	}
	os.WriteFile(filepath.Join(dir, "a.json"), []byte(`[{"id":"x","category":"c","turns":[{"user":"u","expect":{}}]}]`), 0o644)
	os.WriteFile(filepath.Join(dir, "b.json"), []byte(`[{"id":"x","category":"c","turns":[{"user":"u","expect":{}}]}]`), 0o644)
	if _, err := LoadDir(dir); err == nil {
		t.Error("duplicate id should fail")
	}
}

func TestSummarizeFlakyAndPercentiles(t *testing.T) {
	runs := []CaseRun{
		{CaseID: "a", Category: "x", Repeat: 1, Pass: true, Turns: []TurnResult{{LatencyMS: 100}}},
		{CaseID: "a", Category: "x", Repeat: 2, Pass: false, Turns: []TurnResult{{LatencyMS: 300}}},
		{CaseID: "b", Category: "x", Repeat: 1, Pass: true, Turns: []TurnResult{{LatencyMS: 200}}},
	}
	s := Summarize(Meta{Repeat: 2}, runs)
	if s.Passed != 2 || s.Categories[0].Passed != 2 || s.TurnP50MS != 200 || s.TurnP95MS != 300 {
		t.Errorf("summary = %+v", s)
	}
	md := Markdown(s, runs)
	if !strings.Contains(md, "FLAKY") || !strings.Contains(md, "### a (repeat 2)") {
		t.Errorf("markdown missing flaky/failure sections:\n%s", md)
	}
}
