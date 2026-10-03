package plugin_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// runHookScript runs a Claude Code plugin script with the given stdin against
// a fake engram server on a free port and returns the requests it received.
func runHookScript(t *testing.T, script, stdin string) (paths []string, bodies []string) {
	t.Helper()
	for _, bin := range []string{"bash", "jq", "curl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}

	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}

	path := filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", script)
	cmd := exec.Command("bash", path)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(), "ENGRAM_PORT="+port)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s failed: %v\n%s", script, err, stdout.String())
	}
	if script == "user-prompt-submit.sh" && stdout.Len() != 0 {
		t.Fatalf("%s must print nothing, got %q", script, stdout.String())
	}

	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), paths...), append([]string(nil), bodies...)
}

func TestSubagentStopHookSendsLastAssistantMessage(t *testing.T) {
	cwd := t.TempDir()
	input, _ := json.Marshal(map[string]string{
		"session_id":             "sess-1",
		"cwd":                    cwd,
		"last_assistant_message": "## Key Learnings:\n1. subagent finding",
		"stdout":                 "legacy field must be ignored",
	})
	paths, bodies := runHookScript(t, "subagent-stop.sh", string(input))
	if len(paths) != 1 || paths[0] != "/observations/passive" {
		t.Fatalf("requests = %v, want one POST /observations/passive", paths)
	}
	var got struct {
		SessionID string `json:"session_id"`
		Content   string `json:"content"`
		Source    string `json:"source"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &got); err != nil {
		t.Fatalf("parse body %q: %v", bodies[0], err)
	}
	if got.Content != "## Key Learnings:\n1. subagent finding" || got.SessionID != "sess-1" || got.Source != "subagent-stop" {
		t.Fatalf("unexpected body: %#v", got)
	}
}

func TestSubagentStopHookSkipsWithoutLastAssistantMessage(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"session_id": "sess-1", "cwd": t.TempDir(), "stdout": "legacy"})
	paths, _ := runHookScript(t, "subagent-stop.sh", string(input))
	if len(paths) != 0 {
		t.Fatalf("expected no request, got %v", paths)
	}
}

func TestSessionEndHookEndsSessionWithEmptyBody(t *testing.T) {
	paths, bodies := runHookScript(t, "session-end.sh", `{"session_id":"sess-9","reason":"other"}`)
	if len(paths) != 1 || paths[0] != "/sessions/sess-9/end" {
		t.Fatalf("requests = %v, want one POST /sessions/sess-9/end", paths)
	}
	if bodies[0] != "{}" {
		t.Fatalf("body = %q, want {}", bodies[0])
	}
}

func TestUserPromptSubmitHookPostsPromptSilently(t *testing.T) {
	input, _ := json.Marshal(map[string]string{
		"session_id": "sess-p",
		"prompt":     "fix the login bug",
		"cwd":        t.TempDir(),
	})
	paths, bodies := runHookScript(t, "user-prompt-submit.sh", string(input))
	if len(paths) != 1 || paths[0] != "/prompts" {
		t.Fatalf("expected exactly one POST /prompts, got %v", paths)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(bodies[0]), &got); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, bodies[0])
	}
	if got["session_id"] != "sess-p" || got["content"] != "fix the login bug" {
		t.Fatalf("unexpected payload %v", got)
	}
}

func TestUserPromptSubmitHookSendsNothingWithoutPromptOrSession(t *testing.T) {
	for name, in := range map[string]map[string]string{
		"no prompt":  {"session_id": "sess-p"},
		"no session": {"prompt": "hello"},
	} {
		input, _ := json.Marshal(in)
		if paths, _ := runHookScript(t, "user-prompt-submit.sh", string(input)); len(paths) != 0 {
			t.Fatalf("%s: expected no requests, got %v", name, paths)
		}
	}
}

func TestUserPromptSubmitHookIsSilentAndExitsZeroWhenServerDown(t *testing.T) {
	for _, bin := range []string{"bash", "jq", "curl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	_ = l.Close()

	cmd := exec.Command("bash", filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", "user-prompt-submit.sh"))
	cmd.Stdin = strings.NewReader(`{"session_id":"s","prompt":"hi"}`)
	cmd.Env = append(os.Environ(), "ENGRAM_PORT="+port)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("hook must exit 0 with the server down: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("hook must print nothing, got %q", stdout.String())
	}
}
