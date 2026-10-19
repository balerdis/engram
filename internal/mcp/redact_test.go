package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

const (
	fakeKey  = "sk-F4K3F4K3F4K3F4K3F4K3F4K3"
	fakeKeyN = "F4K3F4K3F4K3"
	fakePass = "FakePass123"
)

func callTool(t *testing.T, h func(context.Context, mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error), args map[string]any) string {
	t.Helper()
	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", callResultText(t, res))
	}
	return callResultText(t, res)
}

func assertNoSecret(t *testing.T, s *store.Store) {
	t.Helper()
	data, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(data)
	if strings.Contains(string(raw), fakeKeyN) || strings.Contains(string(raw), fakePass) {
		t.Fatalf("credential reached the database: %s", raw)
	}
}

func TestMCPWriteToolsNeverStoreCredentials(t *testing.T) {
	s := newMCPTestStore(t)
	act := NewSessionActivity(10 * time.Minute)
	cfg := MCPConfig{}

	callTool(t, handleSave(s, cfg, act), map[string]any{
		"title": "rotate " + fakeKey, "content": "password=" + fakePass, "type": "note", "project": "engram",
		"topic_key": "keys/" + fakeKey,
	})
	obs, err := s.RecentObservations("engram", "project", 5)
	if err != nil || len(obs) != 1 {
		t.Fatalf("expected 1 observation: %v %v", obs, err)
	}
	callTool(t, handleUpdate(s), map[string]any{"id": float64(obs[0].ID), "content": "token: " + fakeKey})
	callTool(t, handleSavePrompt(s, cfg, act), map[string]any{"content": "use " + fakeKey, "project": "engram", "session_id": "s-mcp"})
	callTool(t, handleSessionSummary(s, cfg, act), map[string]any{"content": "## Goal\npassword=" + fakePass, "project": "engram", "session_id": "s-mcp"})
	callTool(t, handleCapturePassive(s, cfg, act), map[string]any{
		"content": "## Key Learnings:\n- the script reads password=" + fakePass + " from the env file\n", "project": "engram", "session_id": "s-mcp",
	})
	_ = s.CreateSession("s-end", "engram", "/x")
	callTool(t, handleSessionEnd(s, cfg, act), map[string]any{"id": "s-end", "summary": "done " + fakeKey})
	assertNoSecret(t, s)
}
