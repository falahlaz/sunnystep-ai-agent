package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Meta struct {
	Provider  string    `json:"provider"`
	Model     string    `json:"model,omitempty"`
	Repeat    int       `json:"repeat"`
	Started   time.Time `json:"started"`
	Duration  string    `json:"duration"`
	GitCommit string    `json:"git_commit,omitempty"`
}

type CategoryStat struct {
	Category string  `json:"category"`
	Runs     int     `json:"runs"`
	Passed   int     `json:"passed"`
	Rate     float64 `json:"rate"`
}

type CaseStat struct {
	CaseID   string `json:"case_id"`
	Category string `json:"category"`
	Passed   int    `json:"passed"`
	Runs     int    `json:"runs"`
}

type Summary struct {
	Meta         Meta           `json:"meta"`
	Runs         int            `json:"runs"`
	Passed       int            `json:"passed"`
	Categories   []CategoryStat `json:"categories"`
	Cases        []CaseStat     `json:"cases"`
	LLMCalls     int            `json:"llm_calls"`
	InputTokens  int            `json:"input_tokens"`
	OutputTokens int            `json:"output_tokens"`
	TurnP50MS    int64          `json:"turn_p50_ms"`
	TurnP95MS    int64          `json:"turn_p95_ms"`
	Routes       map[string]int `json:"routes"`
}

func Summarize(meta Meta, runs []CaseRun) Summary {
	s := Summary{Meta: meta, Runs: len(runs), Routes: map[string]int{}}
	cats := map[string]*CategoryStat{}
	cases := map[string]*CaseStat{}
	var lat []int64
	for _, r := range runs {
		if r.Pass {
			s.Passed++
		}
		cs := cats[r.Category]
		if cs == nil {
			cs = &CategoryStat{Category: r.Category}
			cats[r.Category] = cs
		}
		cs.Runs++
		k := cases[r.CaseID]
		if k == nil {
			k = &CaseStat{CaseID: r.CaseID, Category: r.Category}
			cases[r.CaseID] = k
		}
		k.Runs++
		if r.Pass {
			cs.Passed++
			k.Passed++
		}
		for _, t := range r.Turns {
			s.LLMCalls += t.Record.LLMCalls
			s.InputTokens += t.Record.InputTokens
			s.OutputTokens += t.Record.OutputTokens
			s.Routes[string(t.Response.Route)]++
			lat = append(lat, t.LatencyMS)
		}
	}
	for _, c := range cats {
		c.Rate = float64(c.Passed) / float64(c.Runs)
		s.Categories = append(s.Categories, *c)
	}
	sort.Slice(s.Categories, func(i, j int) bool { return s.Categories[i].Category < s.Categories[j].Category })
	for _, c := range cases {
		s.Cases = append(s.Cases, *c)
	}
	sort.Slice(s.Cases, func(i, j int) bool {
		a, b := s.Cases[i], s.Cases[j]
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		return a.CaseID < b.CaseID
	})
	s.TurnP50MS, s.TurnP95MS = percentile(lat, 50), percentile(lat, 95)
	return s
}

func percentile(v []int64, p int) int64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]int64(nil), v...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	idx := (p*len(c) + 99) / 100 // nearest-rank
	if idx < 1 {
		idx = 1
	}
	return c[idx-1]
}

// Write creates dir with report.md and results.json.
func Write(dir string, s Summary, runs []CaseRun) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(struct {
		Summary Summary   `json:"summary"`
		Runs    []CaseRun `json:"runs"`
	}{s, runs}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "results.json"), b, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(Markdown(s, runs)), 0o644)
}

func Markdown(s Summary, runs []CaseRun) string {
	var b strings.Builder
	m := s.Meta
	fmt.Fprintf(&b, "# Eval report\n\n")
	fmt.Fprintf(&b, "| | |\n|---|---|\n| Provider | %s |\n", m.Provider)
	if m.Model != "" {
		fmt.Fprintf(&b, "| Model | %s |\n", m.Model)
	}
	if m.GitCommit != "" {
		fmt.Fprintf(&b, "| Commit | %s |\n", m.GitCommit)
	}
	fmt.Fprintf(&b, "| Started | %s |\n| Duration | %s |\n| Repeats | %d |\n", m.Started.Format(time.RFC3339), m.Duration, m.Repeat)
	fmt.Fprintf(&b, "| Case runs passed | **%d / %d (%.0f%%)** |\n", s.Passed, s.Runs, pct(s.Passed, s.Runs))
	fmt.Fprintf(&b, "| LLM calls | %d |\n| Tokens in / out | %d / %d |\n", s.LLMCalls, s.InputTokens, s.OutputTokens)
	fmt.Fprintf(&b, "| Turn latency p50 / p95 | %d ms / %d ms |\n\n", s.TurnP50MS, s.TurnP95MS)

	fmt.Fprintf(&b, "## Pass rate by category\n\n| Category | Passed | Rate |\n|---|---|---|\n")
	for _, c := range s.Categories {
		fmt.Fprintf(&b, "| %s | %d / %d | %.0f%% |\n", c.Category, c.Passed, c.Runs, 100*c.Rate)
	}

	fmt.Fprintf(&b, "\n## Per case (passes out of %d repeats)\n\n| Case | Category | Passed | |\n|---|---|---|---|\n", m.Repeat)
	for _, c := range s.Cases {
		mark := "ok"
		switch {
		case c.Passed == 0:
			mark = "**FAIL**"
		case c.Passed < c.Runs:
			mark = "**FLAKY**"
		}
		fmt.Fprintf(&b, "| %s | %s | %d / %d | %s |\n", c.CaseID, c.Category, c.Passed, c.Runs, mark)
	}

	fmt.Fprintf(&b, "\n## Failures\n\n")
	any := false
	for _, r := range runs {
		if r.Pass {
			continue
		}
		any = true
		fmt.Fprintf(&b, "### %s (repeat %d)\n\n", r.CaseID, r.Repeat)
		if r.Error != "" {
			fmt.Fprintf(&b, "Error: %s\n\n", r.Error)
		}
		for i, t := range r.Turns {
			status := "ok"
			if len(t.Failures) > 0 {
				status = "FAIL"
			}
			fmt.Fprintf(&b, "- Turn %d [%s] user: %q\n", i+1, status, t.User)
			fmt.Fprintf(&b, "  - got: `%s/%s` intent=`%s` kb_top=`%s` (%.2f) cites=%v ticket=%q\n",
				t.Response.Route, t.Response.Reason, t.Record.Intent, t.Record.KBTopChunk, t.Record.KBTopScore,
				t.Response.Citations, t.Response.TicketID)
			if t.Record.ValidationFailure != "" {
				fmt.Fprintf(&b, "  - validation failure: `%s`\n", t.Record.ValidationFailure)
			}
			if t.Record.Error != "" {
				fmt.Fprintf(&b, "  - error: %s\n", t.Record.Error)
			}
			for _, f := range t.Failures {
				fmt.Fprintf(&b, "  - ✗ %s\n", f)
			}
			if len(t.Failures) > 0 {
				fmt.Fprintf(&b, "  - text: %q\n", t.Response.Text)
			}
		}
		b.WriteString("\n")
	}
	if !any {
		b.WriteString("None.\n")
	}
	return b.String()
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}
