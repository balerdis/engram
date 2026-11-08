package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// Saving twice by topic_key reuses the observation id; a candidate that was
// already judged must not be proposed again.
func TestHandleSave_TopicKeyResaveDoesNotReproposeJudgedPair(t *testing.T) {
	s := newMCPTestStore(t)
	save := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	judge := handleJudge(s, NewSessionActivity(10*time.Minute))

	call := func(args map[string]any) map[string]any {
		t.Helper()
		res, err := save(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
		if err != nil || res.IsError {
			t.Fatalf("save: err=%v res=%v", err, res)
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(callResultText(t, res)), &env); err != nil {
			t.Fatalf("envelope: %v", err)
		}
		return env
	}

	call(map[string]any{"title": "Auth architecture sessions design", "content": "Session-based auth design", "type": "architecture"})
	first := call(map[string]any{"title": "Auth architecture sessions design updated", "content": "Updated design", "type": "architecture", "topic_key": "architecture/auth-sessions"})
	cands, _ := first["candidates"].([]any)
	if len(cands) == 0 {
		t.Fatalf("first save must propose the older observation, got %v", first)
	}
	jid, _ := cands[0].(map[string]any)["judgment_id"].(string)
	res, err := judge(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"judgment_id": jid, "relation": "not_conflict"}}})
	if err != nil || res.IsError {
		t.Fatalf("judge: err=%v res=%v", err, res)
	}

	again := call(map[string]any{"title": "Auth architecture sessions design updated", "content": "Updated design again", "type": "architecture", "topic_key": "architecture/auth-sessions"})
	if c, _ := again["candidates"].([]any); len(c) != 0 {
		t.Fatalf("judged pair proposed again: %v", c)
	}
	if jr, _ := again["judgment_required"].(bool); jr {
		t.Fatalf("judgment_required must be false when no new candidate")
	}
}
