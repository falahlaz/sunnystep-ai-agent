// Command agent runs the support agent as a terminal chat or an HTTP server.
//
//	go run ./cmd/agent chat
//	go run ./cmd/agent serve --addr :8080
//
// LLM_PROVIDER=anthropic (default) uses the Claude API (ANTHROPIC_API_KEY,
// LLM_MODEL); LLM_PROVIDER=fake uses offline keyword rules.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/falahlaz/support-agent/internal/agent"
	"github.com/falahlaz/support-agent/internal/handoff"
	"github.com/falahlaz/support-agent/internal/kb"
	"github.com/falahlaz/support-agent/internal/llm"
	"github.com/falahlaz/support-agent/internal/obs"
	"github.com/falahlaz/support-agent/internal/offline"
	"github.com/falahlaz/support-agent/internal/orders"
	"github.com/falahlaz/support-agent/internal/session"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

const usage = "usage: agent chat|serve [flags]  (agent <cmd> -h for flags)"

func run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	kbDir := fs.String("kb", "data/kb", "knowledge base directory")
	ordersPath := fs.String("orders", "data/orders.json", "orders file")
	tickets := fs.String("tickets", "var/tickets.jsonl", "handoff ticket file")
	addr := fs.String("addr", ":8080", "listen address (serve)")
	logPath := fs.String("log", "var/chat.log", "turn log file (chat); serve logs to stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	switch cmd {
	case "chat":
		f, err := openLog(*logPath)
		if err != nil {
			return err
		}
		defer f.Close()
		a, err := build(*kbDir, *ordersPath, *tickets, f, nil)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "turn logs → %s\n", *logPath)
		return chat(a, os.Stdin, os.Stdout)
	case "serve":
		m := obs.NewMetrics()
		m.Publish()
		a, err := build(*kbDir, *ordersPath, *tickets, os.Stdout, m)
		if err != nil {
			return err
		}
		return serve(*addr, newMux(a))
	default:
		return errors.New(usage)
	}
}

// build loads and validates all data before anything else runs, so a bad
// KB or orders file fails startup instead of the first customer turn.
func build(kbDir, ordersPath, ticketsPath string, logOut io.Writer, m *obs.Metrics) (*agent.Agent, error) {
	idx, err := kb.LoadDir(kbDir)
	if err != nil {
		return nil, fmt.Errorf("load kb: %w", err)
	}
	store, err := orders.LoadFile(ordersPath)
	if err != nil {
		return nil, fmt.Errorf("load orders: %w", err)
	}
	sink, err := handoff.NewJSONLSink(ticketsPath)
	if err != nil {
		return nil, err
	}
	client, err := newLLM()
	if err != nil {
		return nil, err
	}
	if m == nil {
		m = obs.NewMetrics()
	}
	return agent.New(agent.DefaultConfig(), agent.Deps{
		LLM:      client,
		KB:       idx,
		Orders:   store,
		Sessions: session.NewMemStore(session.DefaultTTL, time.Now),
		Sink:     sink,
		Observer: &obs.Observer{Log: slog.New(slog.NewJSONHandler(logOut, nil)), Metrics: m},
		Now:      time.Now, // the only real clock; injected everywhere below
	})
}

func newLLM() (llm.Client, error) {
	switch p := os.Getenv("LLM_PROVIDER"); p {
	case "", "anthropic":
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			fmt.Fprintln(os.Stderr, "warning: ANTHROPIC_API_KEY is not set; the SDK will try other credential sources")
		}
		return llm.NewAnthropic(llm.AnthropicConfig{Model: os.Getenv("LLM_MODEL")}), nil
	case "fake":
		fmt.Fprintln(os.Stderr, "LLM_PROVIDER=fake: offline keyword rules, not a real model")
		return offline.Client{}, nil
	default:
		return nil, fmt.Errorf("unknown LLM_PROVIDER %q (want anthropic or fake)", p)
	}
}

func openLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}

func chat(a *agent.Agent, in io.Reader, out io.Writer) error {
	sid := "chat-" + randomHex()
	fmt.Fprintln(out, "Stridewell support (type 'exit' to quit)")
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for {
		fmt.Fprint(out, "\nyou> ")
		if !sc.Scan() {
			fmt.Fprintln(out)
			return sc.Err()
		}
		msg := sc.Text()
		if strings.TrimSpace(msg) == "exit" {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		r := a.Turn(ctx, sid, msg)
		cancel()
		fmt.Fprintf(out, "agent> %s\n", r.Text)
		fmt.Fprintf(out, "\x1b[2m[%s/%s cites=%s ticket=%s trace=%s]\x1b[0m\n",
			r.Route, r.Reason, strings.Join(r.Citations, ","), dash(r.TicketID), r.TraceID)
	}
}

func serve(addr string, h http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      90 * time.Second, // two model calls with one retry each
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	fmt.Fprintf(os.Stderr, "listening on %s\n", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func randomHex() string {
	var b [6]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
