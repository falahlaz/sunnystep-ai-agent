package main

import (
	"encoding/json"
	"expvar"
	"net/http"
	"strings"

	"github.com/falahlaz/support-agent/internal/agent"
)

const maxBody = 16 << 10 // well above the 2,000-char message limit

type chatRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

func newMux(a *agent.Agent) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat", func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		sid := strings.TrimSpace(req.SessionID)
		if sid == "" || len(sid) > 128 {
			httpError(w, http.StatusBadRequest, "session_id is required (max 128 chars)")
			return
		}
		resp := a.Turn(r.Context(), sid, req.Message)
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /debug/vars", expvar.Handler())
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
