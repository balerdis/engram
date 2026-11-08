package mcp

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

var memToolName = regexp.MustCompile(`mem_[a-z_]+`)

// Every mem_* tool named in the instructions must be registered for the profile.
func TestServerInstructionsOnlyNameRegisteredTools(t *testing.T) {
	s := newMCPTestStore(t)
	for _, profile := range []string{"agent", "reader", "admin", "all", "agent,admin"} {
		allow := ResolveTools(profile)
		srv := newServerWithActivity(s, MCPConfig{}, allow, NewSessionActivity(time.Minute))
		registered := srv.ListTools()
		text := buildServerInstructions(allow)
		for _, name := range memToolName.FindAllString(text, -1) {
			if _, ok := registered[name]; !ok {
				t.Errorf("profile %q: instructions name %q but it is not registered", profile, name)
			}
		}
		// Every registered tool must be listed too.
		for name := range registered {
			if !strings.Contains(text, name) {
				t.Errorf("profile %q: registered tool %q missing from instructions", profile, name)
			}
		}
	}
}

func TestServerInstructionsReaderIsReadOnly(t *testing.T) {
	text := buildServerInstructions(ResolveTools("reader"))
	if !strings.Contains(text, "READ-ONLY") {
		t.Errorf("reader instructions must say the profile is read-only:\n%s", text)
	}
	for _, bad := range []string{"PROACTIVE SAVE RULE", "WHO SAVES", "CONFLICT SURFACING"} {
		if strings.Contains(text, bad) {
			t.Errorf("reader instructions must omit %q", bad)
		}
	}
}

func TestServerInstructionsAgentOmitsAdminTools(t *testing.T) {
	text := buildServerInstructions(ResolveTools("agent"))
	for _, bad := range []string{"mem_delete", "mem_stats", "mem_timeline", "mem_merge_projects"} {
		if strings.Contains(text, bad) {
			t.Errorf("agent instructions must not name %q", bad)
		}
	}
	if !strings.Contains(text, "PROACTIVE SAVE RULE") {
		t.Error("agent instructions must keep the save rule")
	}
}

// Multi-byte content under 300 runes (but over 300 bytes) is not truncated.
func TestSearchPreviewMarkCountsRunesNotBytes(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/e"); err != nil {
		t.Fatal(err)
	}
	accented := strings.Repeat("é", 200) + " zanahoria" // 210 runes, 410 bytes
	long := strings.Repeat("é", 400) + " zanahoria"     // 410 runes
	for _, c := range []struct{ title, content string }{{"short accented", accented}, {"long accented", long}} {
		if _, err := s.AddObservation(store.AddObservationParams{SessionID: "s1", Type: "manual", Title: c.title, Content: c.content, Project: "engram", Scope: "project"}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := handleSearch(s, MCPConfig{}, NewSessionActivity(time.Minute))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"query": "zanahoria", "project": "engram"}}})
	if err != nil || res.IsError {
		t.Fatalf("search: %v %v", err, res)
	}
	text, _ := callResultJSON(t, res)["result"].(string)
	var shortBlock, longBlock string
	for _, blk := range strings.Split(text, "\n\n") {
		if strings.Contains(blk, "short accented") {
			shortBlock = blk
		}
		if strings.Contains(blk, "long accented") {
			longBlock = blk
		}
	}
	if shortBlock == "" || longBlock == "" {
		t.Fatalf("results not found:\n%s", text)
	}
	if strings.Contains(shortBlock, "[preview]") {
		t.Errorf("210-rune content flagged as preview:\n%s", shortBlock)
	}
	if !strings.Contains(longBlock, "[preview]") {
		t.Errorf("410-rune content must be flagged:\n%s", longBlock)
	}
}
