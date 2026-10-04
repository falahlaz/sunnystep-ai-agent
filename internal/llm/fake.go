package llm

import (
	"context"
	"fmt"
	"sync"
)

// Fake is a deterministic Client for tests. Fn decides the response; every
// request is recorded so tests can assert on what the model was shown (e.g.
// that an order's gift_message never reaches it).
type Fake struct {
	Fn func(Request) (Response, error)

	mu       sync.Mutex
	requests []Request
}

func (f *Fake) Complete(ctx context.Context, req Request) (Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if f.Fn == nil {
		return Response{}, fmt.Errorf("%w: fake has no Fn", ErrUnavailable)
	}
	resp, err := f.Fn(req)
	if err != nil {
		return Response{}, err
	}
	if req.Schema != nil && resp.JSON == nil {
		resp.JSON, _ = DecodeJSON(resp.Text)
	}
	return resp, nil
}

// Requests returns a copy of every request seen so far.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// Script returns an Fn that serves texts in order, one per call. Calls past
// the end fail with ErrUnavailable so a test notices unexpected extra calls.
func Script(texts ...string) func(Request) (Response, error) {
	var mu sync.Mutex
	i := 0
	return func(Request) (Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if i >= len(texts) {
			return Response{}, fmt.Errorf("%w: script exhausted after %d calls", ErrUnavailable, len(texts))
		}
		t := texts[i]
		i++
		return Response{Text: t, InputTokens: 10, OutputTokens: 5}, nil
	}
}

// ByPurpose routes requests to a per-purpose Fn ("classify", "draft").
func ByPurpose(m map[string]func(Request) (Response, error)) func(Request) (Response, error) {
	return func(r Request) (Response, error) {
		fn, ok := m[r.Purpose]
		if !ok {
			return Response{}, fmt.Errorf("%w: fake has no script for purpose %q", ErrUnavailable, r.Purpose)
		}
		return fn(r)
	}
}
