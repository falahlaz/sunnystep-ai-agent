package offline

import (
	"context"
	"testing"

	"github.com/falahlaz/support-agent/internal/llm"
)

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"will these help my bunions?":           "medical",
		"please return my order SS-10003":       "order_action",
		"where is my order and I want a refund": "order_status", // keyword rules are crude; real model → order_action
		"can I return worn shoes?":              "faq",
		"hi":                                    "unclear",
		"get me a human":                        "human_request",
	}
	for msg, want := range cases {
		got := classify([]llm.Message{{Role: llm.RoleUser, Text: msg}})
		if got["intent"] != want {
			t.Errorf("%q → %v, want %s", msg, got["intent"], want)
		}
	}
}

func TestClassifyUsesHistoryForIDAndEmail(t *testing.T) {
	got := classify([]llm.Message{
		{Role: llm.RoleUser, Text: "where is ss10003"},
		{Role: llm.RoleAssistant, Text: "please send your email"},
		{Role: llm.RoleUser, Text: "lina.wong@example.com"},
	})
	if got["intent"] != "order_status" || got["order_id"] != "ss10003" || got["email"] != "lina.wong@example.com" {
		t.Errorf("got %v", got)
	}
}

func TestDraftCitesFirstContextEntry(t *testing.T) {
	sys := "rules\n\nCONTEXT:\n[returns#return-window] Return window\nYou can return unworn shoes within 30 days. More text.\n\n[x#y] Other\nz\n"
	resp, _ := Client{}.Complete(context.Background(), llm.Request{Purpose: "draft", System: sys})
	if resp.JSON["answer"] != "You can return unworn shoes within 30 days." {
		t.Errorf("answer = %v", resp.JSON["answer"])
	}
	if c := resp.JSON["citations"].([]any); len(c) != 1 || c[0] != "returns#return-window" {
		t.Errorf("citations = %v", c)
	}
}
