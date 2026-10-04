// Package eval runs scripted conversations against the real agent and
// checks each turn's route, reason, citations, ticket and text.
//
// Cases live in eval/cases/*.json. A failing case is data: it is reported,
// never skipped or deleted (PRD §4.10).
package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/falahlaz/support-agent/internal/agent"
)

type Case struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	// CI marks cases the offline provider is expected to pass; `go test`
	// runs them. It says nothing about whether the real model passes.
	CI    bool   `json:"ci,omitempty"`
	Note  string `json:"note,omitempty"`
	Turns []Turn `json:"turns"`
}

type Turn struct {
	User   string `json:"user"`
	Expect Expect `json:"expect"`
}

// Expect fields are all optional. Route and Reason accept a string or a
// list of acceptable values.
type Expect struct {
	Route            OneOf     `json:"route,omitempty"`
	Reason           OneOf     `json:"reason,omitempty"`
	CitationsInclude []string  `json:"citations_include,omitempty"`
	Ticket           *bool     `json:"ticket,omitempty"`
	MustContain      []string  `json:"must_contain,omitempty"`
	MustNotContain   []string  `json:"must_not_contain,omitempty"`
	ToolsOffered     *[]string `json:"tools_offered,omitempty"`
}

type OneOf []string

func (o *OneOf) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*o = OneOf{s}
		return nil
	}
	var l []string
	if err := json.Unmarshal(b, &l); err != nil {
		return fmt.Errorf("want string or list of strings: %w", err)
	}
	*o = l
	return nil
}

func (o OneOf) ok(v string) bool { return len(o) == 0 || slices.Contains(o, v) }

// LoadDir reads every *.json file in dir; each holds a list of cases.
func LoadDir(dir string) ([]Case, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("eval: no case files in %s", dir)
	}
	sort.Strings(files)
	var all []Case
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var cs []Case
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields() // a typo in an expect field must not silently pass
		if err := dec.Decode(&cs); err != nil {
			return nil, fmt.Errorf("eval: %s: %w", f, err)
		}
		for _, c := range cs {
			if c.ID == "" || c.Category == "" || len(c.Turns) == 0 {
				return nil, fmt.Errorf("eval: %s: case %q needs id, category and turns", f, c.ID)
			}
			if seen[c.ID] {
				return nil, fmt.Errorf("eval: duplicate case id %s", c.ID)
			}
			seen[c.ID] = true
			all = append(all, c)
		}
	}
	return all, nil
}

// Check returns one message per unmet expectation; empty means pass.
func Check(e Expect, r agent.Response, rec agent.Record) []string {
	var f []string
	if !e.Route.ok(string(r.Route)) {
		f = append(f, fmt.Sprintf("route: got %s, want %s", r.Route, strings.Join(e.Route, "|")))
	}
	if !e.Reason.ok(string(r.Reason)) {
		f = append(f, fmt.Sprintf("reason: got %s, want %s", r.Reason, strings.Join(e.Reason, "|")))
	}
	for _, c := range e.CitationsInclude {
		if !slices.Contains(r.Citations, c) {
			f = append(f, fmt.Sprintf("citations: missing %s (got %v)", c, r.Citations))
		}
	}
	if e.Ticket != nil && *e.Ticket != (r.TicketID != "") {
		f = append(f, fmt.Sprintf("ticket: got %q, want ticket=%v", r.TicketID, *e.Ticket))
	}
	low := strings.ToLower(r.Text)
	for _, s := range e.MustContain {
		if !strings.Contains(low, strings.ToLower(s)) {
			f = append(f, fmt.Sprintf("text: missing %q", s))
		}
	}
	for _, s := range e.MustNotContain {
		if strings.Contains(low, strings.ToLower(s)) {
			f = append(f, fmt.Sprintf("text: contains forbidden %q", s))
		}
	}
	if e.ToolsOffered != nil {
		got := make([]string, len(rec.ToolsOffered))
		for i, t := range rec.ToolsOffered {
			got[i] = string(t)
		}
		want := slices.Clone(*e.ToolsOffered)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			f = append(f, fmt.Sprintf("tools_offered: got %v, want %v", got, want))
		}
	}
	return f
}
