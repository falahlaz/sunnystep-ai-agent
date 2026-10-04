package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falahlaz/support-agent/internal/agent"
	"github.com/falahlaz/support-agent/internal/obs"
)

func testAgent(t *testing.T, logOut *bytes.Buffer) *agent.Agent {
	t.Helper()
	t.Setenv("LLM_PROVIDER", "fake")
	a, err := build("../../data/kb", "../../data/orders.json", filepath.Join(t.TempDir(), "t.jsonl"), logOut, obs.NewMetrics())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestStartupFailsFastOnBadData(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "fake")
	dir := t.TempDir()
	bad := filepath.Join(dir, "orders.json")
	os.WriteFile(bad, []byte(`[{"id":"BAD","status":"delivered"}]`), 0o644)
	if _, err := build("../../data/kb", bad, filepath.Join(dir, "t.jsonl"), &bytes.Buffer{}, nil); err == nil {
		t.Error("bad orders file should fail startup")
	}
	if _, err := build(dir, "../../data/orders.json", filepath.Join(dir, "t.jsonl"), &bytes.Buffer{}, nil); err == nil {
		t.Error("empty kb dir should fail startup")
	}
	t.Setenv("LLM_PROVIDER", "openai")
	if _, err := build("../../data/kb", "../../data/orders.json", filepath.Join(dir, "t.jsonl"), &bytes.Buffer{}, nil); err == nil {
		t.Error("unknown provider should fail startup")
	}
}

func TestServeChat(t *testing.T) {
	var logs bytes.Buffer
	srv := httptest.NewServer(newMux(testAgent(t, &logs)))
	defer srv.Close()

	post := func(body string) (*http.Response, map[string]any) {
		resp, err := http.Post(srv.URL+"/chat", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return resp, m
	}

	resp, m := post(`{"session_id":"s1","message":"can I return worn shoes?"}`)
	if resp.StatusCode != 200 || m["route"] == nil || m["trace_id"] == nil {
		t.Fatalf("status=%d body=%v", resp.StatusCode, m)
	}
	for _, bad := range []string{`not json`, `{"message":"hi"}`, `{"session_id":"s","message":"hi","x":1}`} {
		if resp, _ := post(bad); resp.StatusCode != 400 {
			t.Errorf("%s → %d, want 400", bad, resp.StatusCode)
		}
	}

	// One turn → exactly one turn log line.
	if n := strings.Count(logs.String(), `"msg":"turn"`); n != 1 {
		t.Errorf("turn log lines = %d, want 1:\n%s", n, logs.String())
	}

	if r, _ := http.Get(srv.URL + "/healthz"); r.StatusCode != 200 {
		t.Errorf("healthz = %d", r.StatusCode)
	}
	if r, _ := http.Get(srv.URL + "/debug/vars"); r.StatusCode != 200 {
		t.Errorf("debug/vars = %d", r.StatusCode)
	}
}

func TestChatREPL(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("where is ss10003\nlina.wong@example.com\nexit\n")
	if err := chat(testAgent(t, &bytes.Buffer{}), in, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"[clarify/need_order_verification", "[answer/verified_order cites=order:SS-10003"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}
