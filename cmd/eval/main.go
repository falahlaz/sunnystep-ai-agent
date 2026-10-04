// Command eval runs the eval cases and writes a report.
//
//	go run ./cmd/eval --provider fake
//	go run ./cmd/eval --provider anthropic --repeat 3
//
// Failing cases are reported, not hidden; the exit code is 0 unless the
// run itself could not happen.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/falahlaz/support-agent/internal/eval"
	"github.com/falahlaz/support-agent/internal/kb"
	"github.com/falahlaz/support-agent/internal/llm"
	"github.com/falahlaz/support-agent/internal/offline"
	"github.com/falahlaz/support-agent/internal/orders"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	provider := flag.String("provider", "fake", "fake (offline rules) or anthropic")
	repeat := flag.Int("repeat", 1, "runs per case (flakiness)")
	parallel := flag.Int("parallel", 4, "cases run concurrently")
	casesDir := flag.String("cases", "eval/cases", "case directory")
	outDir := flag.String("out", "eval/results", "results root; a timestamped dir is created")
	filter := flag.String("filter", "", "only case IDs containing this")
	kbDir := flag.String("kb", "data/kb", "knowledge base directory")
	ordersPath := flag.String("orders", "data/orders.json", "orders file")
	flag.Parse()

	cases, err := eval.LoadDir(*casesDir)
	if err != nil {
		return err
	}
	if *filter != "" {
		var keep []eval.Case
		for _, c := range cases {
			if strings.Contains(c.ID, *filter) {
				keep = append(keep, c)
			}
		}
		cases = keep
	}
	idx, err := kb.LoadDir(*kbDir)
	if err != nil {
		return err
	}
	store, err := orders.LoadFile(*ordersPath)
	if err != nil {
		return err
	}

	meta := eval.Meta{Provider: *provider, Repeat: *repeat, Started: time.Now(), GitCommit: gitCommit()}
	var client llm.Client
	switch *provider {
	case "fake":
		client = offline.Client{}
	case "anthropic":
		meta.Model = os.Getenv("LLM_MODEL")
		if meta.Model == "" {
			meta.Model = llm.DefaultModel
		}
		client = llm.NewAnthropic(llm.AnthropicConfig{Model: meta.Model})
	default:
		return fmt.Errorf("unknown provider %q", *provider)
	}

	fmt.Fprintf(os.Stderr, "running %d cases × %d with %s\n", len(cases), *repeat, *provider)
	runs := eval.Run(context.Background(), cases, eval.Options{
		KB: idx, Orders: store, Client: client, Repeat: *repeat, Parallel: *parallel,
		Progress: func(r eval.CaseRun) {
			mark := "ok  "
			if !r.Pass {
				mark = "FAIL"
			}
			fmt.Fprintf(os.Stderr, "%s %s #%d\n", mark, r.CaseID, r.Repeat)
		},
	})
	meta.Duration = time.Since(meta.Started).Round(time.Second).String()
	s := eval.Summarize(meta, runs)

	dir := filepath.Join(*outDir, meta.Started.Format("20060102-150405")+"-"+*provider)
	if err := eval.Write(dir, s, runs); err != nil {
		return err
	}
	fmt.Printf("passed %d/%d case runs; report: %s\n", s.Passed, s.Runs, filepath.Join(dir, "report.md"))
	return nil
}

func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
