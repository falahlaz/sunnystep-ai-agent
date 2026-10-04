package eval

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/falahlaz/support-agent/internal/agent"
	"github.com/falahlaz/support-agent/internal/handoff"
	"github.com/falahlaz/support-agent/internal/kb"
	"github.com/falahlaz/support-agent/internal/llm"
	"github.com/falahlaz/support-agent/internal/orders"
	"github.com/falahlaz/support-agent/internal/session"
)

// Clock is the fixed business time for every eval run, so "days since
// delivery" and eligibility facts do not drift with the calendar.
var Clock = time.Date(2026, 10, 4, 12, 0, 0, 0, time.FixedZone("SGT", 8*3600))

type Options struct {
	KB       *kb.Index
	Orders   *orders.Store
	Client   llm.Client
	Repeat   int
	Parallel int
	// Progress, if set, is called after each case run.
	Progress func(CaseRun)
}

type TurnResult struct {
	User      string         `json:"user"`
	Response  agent.Response `json:"response"`
	Record    agent.Record   `json:"record"`
	LatencyMS int64          `json:"latency_ms"` // wall clock around Turn
	Failures  []string       `json:"failures,omitempty"`
}

type CaseRun struct {
	CaseID   string       `json:"case_id"`
	Category string       `json:"category"`
	Repeat   int          `json:"repeat"`
	Pass     bool         `json:"pass"`
	Turns    []TurnResult `json:"turns"`
	Error    string       `json:"error,omitempty"`
}

type capture struct{ recs []agent.Record }

func (c *capture) ObserveTurn(r agent.Record) { c.recs = append(c.recs, r) }

// Run executes every case Repeat times. Each case run gets its own session
// store and ticket sink, so cases cannot affect each other.
func Run(ctx context.Context, cases []Case, o Options) []CaseRun {
	if o.Repeat < 1 {
		o.Repeat = 1
	}
	if o.Parallel < 1 {
		o.Parallel = 1
	}
	type job struct {
		c      Case
		repeat int
		idx    int
	}
	jobs := make(chan job)
	out := make([]CaseRun, len(cases)*o.Repeat)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for w := 0; w < o.Parallel; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				r := runCase(ctx, j.c, j.repeat, o)
				out[j.idx] = r
				if o.Progress != nil {
					mu.Lock()
					o.Progress(r)
					mu.Unlock()
				}
			}
		}()
	}
	i := 0
	for rep := 1; rep <= o.Repeat; rep++ {
		for _, c := range cases {
			jobs <- job{c, rep, i}
			i++
		}
	}
	close(jobs)
	wg.Wait()
	return out
}

func runCase(ctx context.Context, c Case, repeat int, o Options) CaseRun {
	run := CaseRun{CaseID: c.ID, Category: c.Category, Repeat: repeat, Pass: true}
	cap := &capture{}
	now := func() time.Time { return Clock }
	a, err := agent.New(agent.DefaultConfig(), agent.Deps{
		LLM: o.Client, KB: o.KB, Orders: o.Orders,
		Sessions: session.NewMemStore(0, now), Sink: &handoff.MemSink{},
		Observer: cap, Now: now,
	})
	if err != nil {
		run.Pass, run.Error = false, err.Error()
		return run
	}
	sid := fmt.Sprintf("eval-%s-%d", c.ID, repeat)
	for i, t := range c.Turns {
		start := time.Now() // harness timing, not business logic
		resp := a.Turn(ctx, sid, t.User)
		tr := TurnResult{User: t.User, Response: resp, LatencyMS: time.Since(start).Milliseconds()}
		if i < len(cap.recs) {
			tr.Record = cap.recs[i]
		}
		tr.Failures = Check(t.Expect, resp, tr.Record)
		if len(tr.Failures) > 0 {
			run.Pass = false
		}
		run.Turns = append(run.Turns, tr)
	}
	return run
}
