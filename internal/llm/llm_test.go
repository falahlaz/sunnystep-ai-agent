package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const okBody = `{
  "id": "msg_1", "type": "message", "role": "assistant", "model": "test",
  "content": [{"type": "text", "text": "{\"intent\":\"faq\"}"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 12, "output_tokens": 7}
}`

// server answers with the given status codes in order, then 200.
func server(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		w.Header().Set("Content-Type", "application/json")
		if n <= len(statuses) && statuses[n-1] != 200 {
			w.WriteHeader(statuses[n-1])
			w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"boom"}}`))
			return
		}
		w.Write([]byte(okBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func client(url string, timeout time.Duration) *Anthropic {
	return NewAnthropic(AnthropicConfig{
		APIKey: "test", BaseURL: url, Timeout: timeout,
		Backoff: func() time.Duration { return 0 },
	})
}

var schemaReq = Request{Purpose: "classify", Messages: []Message{{Role: RoleUser, Text: "hi"}},
	Schema: map[string]any{"type": "object"}}

func TestAnthropicSuccess(t *testing.T) {
	srv, calls := server(t)
	resp, err := client(srv.URL, time.Second).Complete(context.Background(), schemaReq)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
	if resp.JSON["intent"] != "faq" || resp.InputTokens != 12 || resp.OutputTokens != 7 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestAnthropicRetries(t *testing.T) {
	cases := []struct {
		name      string
		statuses  []int
		wantCalls int32
		wantErr   error
	}{
		{"500 then ok", []int{500}, 2, nil},
		{"429 then ok", []int{429}, 2, nil},
		{"500 twice", []int{500, 503}, 2, ErrUnavailable},
		{"400 not retried", []int{400}, 1, ErrRejected},
		{"401 not retried", []int{401}, 1, ErrRejected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, calls := server(t, c.statuses...)
			_, err := client(srv.URL, time.Second).Complete(context.Background(), schemaReq)
			if !errors.Is(err, c.wantErr) {
				t.Errorf("err = %v, want %v", err, c.wantErr)
			}
			if calls.Load() != c.wantCalls {
				t.Errorf("calls = %d, want %d", calls.Load(), c.wantCalls)
			}
		})
	}
}

func TestAnthropicTimeoutRetriedOnce(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	_, err := client(srv.URL, 50*time.Millisecond).Complete(context.Background(), schemaReq)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (one retry)", calls.Load())
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("took %v; per-attempt timeout not applied", el)
	}
}

func TestAnthropicCallerCancelNotRetried(t *testing.T) {
	srv, calls := server(t, 500, 500)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client(srv.URL, time.Second).Complete(ctx, schemaReq)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() > 1 {
		t.Errorf("calls = %d; cancelled context must not be retried", calls.Load())
	}
}

func TestDecodeJSON(t *testing.T) {
	for _, s := range []string{`{"a":1}`, "```json\n{\"a\":1}\n```", "  {\"a\":1}  "} {
		if m, err := DecodeJSON(s); err != nil || m["a"] != float64(1) {
			t.Errorf("DecodeJSON(%q) = %v, %v", s, m, err)
		}
	}
	for _, s := range []string{``, `null`, `[1]`, `{"a":`, `Sure! {"a":1}`} {
		if _, err := DecodeJSON(s); err == nil {
			t.Errorf("DecodeJSON(%q) should fail", s)
		}
	}
}

func TestFakeScriptAndRecording(t *testing.T) {
	f := &Fake{Fn: Script(`{"intent":"faq"}`)}
	resp, err := f.Complete(context.Background(), schemaReq)
	if err != nil || resp.JSON["intent"] != "faq" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if _, err := f.Complete(context.Background(), schemaReq); !errors.Is(err, ErrUnavailable) {
		t.Errorf("exhausted script should fail, got %v", err)
	}
	if n := len(f.Requests()); n != 2 {
		t.Errorf("recorded %d requests, want 2", n)
	}
}

func TestAnthropicRequestShape(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(okBody))
	}))
	defer srv.Close()
	req := schemaReq
	req.System = "sys"
	if _, err := client(srv.URL, time.Second).Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["temperature"]; ok {
		t.Error("temperature must not be sent (rejected by Sonnet 5.5, D10)")
	}
	oc, _ := got["output_config"].(map[string]any)
	if oc["effort"] != "low" || oc["format"] == nil {
		t.Errorf("output_config = %v", got["output_config"])
	}
	if got["model"] != DefaultModel {
		t.Errorf("model = %v", got["model"])
	}
}
