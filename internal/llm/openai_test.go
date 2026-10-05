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

const oaOK = `{"choices":[{"message":{"content":"{\"intent\":\"faq\"}","refusal":null},"finish_reason":"stop"}],
"usage":{"prompt_tokens":12,"completion_tokens":7}}`

func oaServer(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int32, *[]byte) {
	t.Helper()
	var calls atomic.Int32
	var last []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		last, _ = io.ReadAll(r.Body)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test" {
			w.WriteHeader(404)
			return
		}
		if n <= len(statuses) && statuses[n-1] != 200 {
			w.WriteHeader(statuses[n-1])
			w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		w.Write([]byte(oaOK))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &last
}

func oaClient(t *testing.T, url string, timeout time.Duration) *OpenAI {
	t.Helper()
	c, err := NewOpenAI(OpenAIConfig{APIKey: "test", Model: "test-model", BaseURL: url,
		Timeout: timeout, Backoff: func() time.Duration { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestOpenAIRequiresModelAndKey(t *testing.T) {
	if _, err := NewOpenAI(OpenAIConfig{APIKey: "k"}); err == nil {
		t.Error("missing model should fail")
	}
	if _, err := NewOpenAI(OpenAIConfig{Model: "m"}); err == nil {
		t.Error("missing key should fail")
	}
}

func TestOpenAISuccessAndRequestShape(t *testing.T) {
	srv, calls, last := oaServer(t)
	req := schemaReq
	req.System = "sys"
	resp, err := oaClient(t, srv.URL, time.Second).Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || resp.JSON["intent"] != "faq" || resp.InputTokens != 12 || resp.OutputTokens != 7 {
		t.Fatalf("calls=%d resp=%+v", calls.Load(), resp)
	}
	var got map[string]any
	json.Unmarshal(*last, &got)
	if _, ok := got["temperature"]; ok {
		t.Error("temperature must not be sent")
	}
	msgs := got["messages"].([]any)
	if first := msgs[0].(map[string]any); first["role"] != "system" || first["content"] != "sys" {
		t.Errorf("first message = %v", first)
	}
	rf := got["response_format"].(map[string]any)
	js := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["strict"] != true || js["name"] != "classify" || js["schema"] == nil {
		t.Errorf("response_format = %v", rf)
	}
}

func TestOpenAIRetries(t *testing.T) {
	cases := []struct {
		name      string
		statuses  []int
		wantCalls int32
		wantErr   error
	}{
		{"500 then ok", []int{500}, 2, nil},
		{"429 then ok", []int{429}, 2, nil},
		{"500 twice", []int{500, 502}, 2, ErrUnavailable},
		{"400 not retried", []int{400}, 1, ErrRejected},
		{"401 not retried", []int{401}, 1, ErrRejected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, calls, _ := oaServer(t, c.statuses...)
			_, err := oaClient(t, srv.URL, time.Second).Complete(context.Background(), schemaReq)
			if !errors.Is(err, c.wantErr) {
				t.Errorf("err = %v, want %v", err, c.wantErr)
			}
			if calls.Load() != c.wantCalls {
				t.Errorf("calls = %d, want %d", calls.Load(), c.wantCalls)
			}
		})
	}
}

func TestOpenAITimeoutRetriedOnce(t *testing.T) {
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
	_, err := oaClient(t, srv.URL, 50*time.Millisecond).Complete(context.Background(), schemaReq)
	if !errors.Is(err, ErrUnavailable) || calls.Load() != 2 {
		t.Fatalf("err=%v calls=%d, want ErrUnavailable after 2 calls", err, calls.Load())
	}
}

func TestOpenAIRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":null,"refusal":"no"}}],"usage":{}}`))
	}))
	defer srv.Close()
	if _, err := oaClient(t, srv.URL, time.Second).Complete(context.Background(), schemaReq); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
}
