package handoff

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func readTickets(t *testing.T, path string) []Ticket {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Ticket
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var tk Ticket
		if err := json.Unmarshal(sc.Bytes(), &tk); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		out = append(out, tk)
	}
	return out
}

func TestJSONLSinkWritesRedactedTickets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "var", "tickets.jsonl")
	s, err := NewJSONLSink(path)
	if err != nil {
		t.Fatal(err)
	}
	days := 12
	id, err := s.Create(context.Background(), Ticket{
		TraceID: "tr1", SessionID: "s1", Reason: "action_requires_human",
		CustomerVerified: true, OrderID: "SS-10003",
		Facts:   &Facts{Status: "delivered", DaysSinceDelivery: &days},
		Summary: "refund SS-10003 please, lina.wong@example.com, card 4111 1111 1111 1111",
	})
	if err != nil || id != "T-0001" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	got := readTickets(t, path)
	if len(got) != 1 {
		t.Fatalf("got %d tickets", len(got))
	}
	tk := got[0]
	if strings.Contains(tk.Summary, "lina.wong") || strings.Contains(tk.Summary, "4111") {
		t.Errorf("summary not redacted: %q", tk.Summary)
	}
	if !strings.Contains(tk.Summary, "SS-10003") {
		t.Errorf("order id should be kept for operators: %q", tk.Summary)
	}
	if tk.Facts == nil || *tk.Facts.DaysSinceDelivery != 12 {
		t.Errorf("facts lost: %+v", tk.Facts)
	}

	// A new sink on the same file continues numbering.
	s2, _ := NewJSONLSink(path)
	if id, _ := s2.Create(context.Background(), Ticket{Reason: "complaint"}); id != "T-0002" {
		t.Errorf("id after restart = %q, want T-0002", id)
	}
}

func TestUnverifiedTicketDropsOrderData(t *testing.T) {
	var m MemSink
	m.Create(context.Background(), Ticket{CustomerVerified: false, OrderID: "SS-10003",
		Facts: &Facts{Status: "delivered"}})
	tk := m.Tickets()[0]
	if tk.OrderID != "" || tk.Facts != nil {
		t.Errorf("unverified ticket kept order data: %+v", tk)
	}
}

func TestJSONLSinkFailure(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewJSONLSink(filepath.Join(dir, "tickets.jsonl"))
	// Make the target path a directory so the open fails.
	os.Mkdir(filepath.Join(dir, "tickets.jsonl"), 0o755)
	if _, err := s.Create(context.Background(), Ticket{}); err == nil {
		t.Fatal("expected error when file cannot be written")
	}
}

func TestMemSinkErr(t *testing.T) {
	m := MemSink{Err: errors.New("disk full")}
	if _, err := m.Create(context.Background(), Ticket{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestJSONLSinkConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	s, _ := NewJSONLSink(path)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Create(context.Background(), Ticket{Reason: "x"}) }()
	}
	wg.Wait()
	got := readTickets(t, path)
	seen := map[string]bool{}
	for _, tk := range got {
		if seen[tk.ID] {
			t.Errorf("duplicate id %s", tk.ID)
		}
		seen[tk.ID] = true
	}
	if len(got) != 50 {
		t.Errorf("got %d tickets, want 50", len(got))
	}
}
