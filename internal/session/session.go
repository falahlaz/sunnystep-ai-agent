// Package session keeps per-conversation state server-side: verification
// status, the failure counter and recent history. Nothing here is ever sent
// to the model except History text, which the orchestrator controls.
package session

import (
	"sync"
	"time"
)

const (
	DefaultTTL = 30 * time.Minute
	MaxHistory = 12
)

type Turn struct {
	User      string
	Assistant string
}

type Session struct {
	ID string
	// VerifiedOrderID binds the session to one order. Asking about another
	// order requires verifying that order too.
	VerifiedOrderID string
	VerifiedEmail   string // server-side only; never logged or sent to the model
	VerifyFailures  int
	// PendingOrderID is an order ID the customer gave without (or with a
	// wrong) email, kept so the email can arrive on a later turn without
	// relying on the model to re-extract the ID from history.
	PendingOrderID string
	History        []Turn
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// AppendTurn adds a turn and keeps only the last MaxHistory.
func (s *Session) AppendTurn(t Turn) {
	s.History = append(s.History, t)
	if n := len(s.History); n > MaxHistory {
		s.History = append([]Turn(nil), s.History[n-MaxHistory:]...)
	}
}

// Verified reports whether the session is verified for orderID.
func (s *Session) Verified(orderID string) bool {
	return s.VerifiedOrderID != "" && s.VerifiedOrderID == orderID
}

type Store interface {
	Get(id string) (*Session, bool)
	Put(*Session)
}

// MemStore is an in-memory Store with idle expiry. Get returns a copy, so a
// caller's changes take effect only on Put.
type MemStore struct {
	mu  sync.Mutex
	m   map[string]*Session
	ttl time.Duration
	now func() time.Time
}

func NewMemStore(ttl time.Duration, now func() time.Time) *MemStore {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &MemStore{m: make(map[string]*Session), ttl: ttl, now: now}
}

func (s *MemStore) Get(id string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[id]
	if !ok {
		return nil, false
	}
	if s.now().Sub(sess.UpdatedAt) > s.ttl {
		delete(s.m, id)
		return nil, false
	}
	return clone(sess), true
}

func (s *MemStore) Put(sess *Session) {
	c := clone(sess)
	c.UpdatedAt = s.now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = c.UpdatedAt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[c.ID] = c
	// Opportunistic sweep keeps memory bounded without a background goroutine.
	for id, other := range s.m {
		if c.UpdatedAt.Sub(other.UpdatedAt) > s.ttl {
			delete(s.m, id)
		}
	}
}

func clone(s *Session) *Session {
	c := *s
	c.History = append([]Turn(nil), s.History...)
	return &c
}
