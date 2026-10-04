package session

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)} }

func TestTTLExpiry(t *testing.T) {
	c := newClock()
	s := NewMemStore(30*time.Minute, c.now)
	s.Put(&Session{ID: "a", VerifiedOrderID: "SS-10003"})

	c.add(29 * time.Minute)
	if _, ok := s.Get("a"); !ok {
		t.Fatal("session expired too early")
	}
	// Activity refreshes the idle timer.
	sess, _ := s.Get("a")
	s.Put(sess)
	c.add(29 * time.Minute)
	if _, ok := s.Get("a"); !ok {
		t.Fatal("Put should refresh the TTL")
	}
	c.add(31 * time.Minute)
	if _, ok := s.Get("a"); ok {
		t.Fatal("session should have expired")
	}
}

func TestGetReturnsCopy(t *testing.T) {
	s := NewMemStore(0, newClock().now)
	s.Put(&Session{ID: "a"})
	got, _ := s.Get("a")
	got.VerifiedOrderID = "SS-10003"
	got.AppendTurn(Turn{User: "x"})
	again, _ := s.Get("a")
	if again.VerifiedOrderID != "" || len(again.History) != 0 {
		t.Fatal("mutating a Get result leaked into the store without Put")
	}
}

func TestHistoryCap(t *testing.T) {
	var s Session
	for i := 0; i < 20; i++ {
		s.AppendTurn(Turn{User: fmt.Sprint(i)})
	}
	if len(s.History) != MaxHistory || s.History[0].User != "8" {
		t.Fatalf("history len=%d first=%q", len(s.History), s.History[0].User)
	}
}

func TestVerifiedBoundToOneOrder(t *testing.T) {
	s := Session{VerifiedOrderID: "SS-10003"}
	if !s.Verified("SS-10003") || s.Verified("SS-10004") || (&Session{}).Verified("") {
		t.Fatal("verification must be bound to exactly one order")
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := newClock()
	s := NewMemStore(0, c.now)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			id := fmt.Sprintf("s%d", g%4)
			for i := 0; i < 200; i++ {
				sess, ok := s.Get(id)
				if !ok {
					sess = &Session{ID: id}
				}
				sess.VerifyFailures++
				sess.AppendTurn(Turn{User: "hi"})
				s.Put(sess)
				c.add(time.Millisecond)
			}
		}(g)
	}
	wg.Wait()
	for i := 0; i < 4; i++ {
		if _, ok := s.Get(fmt.Sprintf("s%d", i)); !ok {
			t.Errorf("session s%d missing", i)
		}
	}
}
