// Package handoff records escalations for a human agent. A ticket carries
// the reason code and pre-computed facts so the human does not have to
// re-derive them, and only redacted customer text.
package handoff

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/falahlaz/support-agent/internal/guard"
)

type Facts struct {
	Status            string `json:"status,omitempty"`
	DaysSinceDelivery *int   `json:"days_since_delivery,omitempty"`
	HasFinalSaleItems bool   `json:"has_final_sale_items,omitempty"`
}

type Ticket struct {
	ID               string    `json:"id"`
	CreatedAt        time.Time `json:"created_at"`
	TraceID          string    `json:"trace_id"`
	SessionID        string    `json:"session_id"`
	Reason           string    `json:"reason"`
	Intent           string    `json:"intent,omitempty"`
	CustomerVerified bool      `json:"customer_verified"`
	OrderID          string    `json:"order_id,omitempty"` // only when verified
	Facts            *Facts    `json:"facts,omitempty"`
	Summary          string    `json:"summary"` // last customer message, PII-redacted
}

// Sink stores a ticket and returns its ID. Implementations assign the ID
// and redact Summary, so no caller can forget to.
type Sink interface {
	Create(ctx context.Context, t Ticket) (id string, err error)
}

// prepare enforces the rules every sink shares.
func prepare(t Ticket, id string) Ticket {
	t.ID = id
	t.Summary = guard.RedactPII(t.Summary)
	if !t.CustomerVerified {
		t.OrderID = ""
		t.Facts = nil
	}
	return t
}

// JSONLSink appends one JSON object per line to a file.
type JSONLSink struct {
	mu   sync.Mutex
	path string
	seq  int
}

func NewJSONLSink(path string) (*JSONLSink, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("handoff: %w", err)
	}
	s := &JSONLSink{path: path}
	// Continue numbering after existing tickets so IDs stay unique across
	// restarts.
	if b, err := os.ReadFile(path); err == nil {
		for _, c := range b {
			if c == '\n' {
				s.seq++
			}
		}
	}
	return s, nil
}

func (s *JSONLSink) Create(ctx context.Context, t Ticket) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t = prepare(t, fmt.Sprintf("T-%04d", s.seq+1))
	line, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("handoff: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("handoff: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return "", fmt.Errorf("handoff: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("handoff: %w", err)
	}
	s.seq++
	return t.ID, nil
}

// MemSink keeps tickets in memory, for tests. Set Err to simulate failure.
type MemSink struct {
	mu      sync.Mutex
	Err     error
	tickets []Ticket
}

func (m *MemSink) Create(ctx context.Context, t Ticket) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return "", m.Err
	}
	t = prepare(t, fmt.Sprintf("T-%04d", len(m.tickets)+1))
	m.tickets = append(m.tickets, t)
	return t.ID, nil
}

func (m *MemSink) Tickets() []Ticket {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Ticket(nil), m.tickets...)
}
