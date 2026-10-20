package plugin_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEngramBody is the default fake `engram` binary: `port` prints the fake
// server's port, `serve` only records the ENGRAM_PORT it was given.
const fakeEngramBody = `#!/bin/sh
case "$1" in
  port) echo "$FAKE_ENGRAM_PORT" ;;
  serve) echo "serve ENGRAM_PORT=$ENGRAM_PORT" >> "$FAKE_ENGRAM_LOG" ;;
  protocol-mode) echo full ;;
  sync) ;;
  *) echo "unknown command: $1" >&2; exit 1 ;;
esac
`

// fakeEngramOld is a binary that predates `engram port`.
const fakeEngramOld = `#!/bin/sh
case "$1" in
  serve) echo "serve ENGRAM_PORT=$ENGRAM_PORT" >> "$FAKE_ENGRAM_LOG" ;;
  protocol-mode) echo full ;;
  *) echo "unknown command: $1" >&2; exit 1 ;;
esac
`

// fakeEngramDisabled makes `engram port` answer that memory is disabled.
const fakeEngramDisabled = `#!/bin/sh
case "$1" in
  port) echo "engram: port 7437 on 127.0.0.1 is held by a process of another OS user (uid 1234) and so is the port derived from this user's uid: engram memory is disabled until ENGRAM_PORT is set to a free port" >&2; exit 1 ;;
  serve) echo "serve ENGRAM_PORT=$ENGRAM_PORT" >> "$FAKE_ENGRAM_LOG" ;;
  protocol-mode) echo full ;;
  *) echo "unknown command: $1" >&2; exit 1 ;;
esac
`

// fakeEngramOldNoisy is a 1.21.x binary: it prints an update-check line on
// stderr before rejecting `port` as an unknown command.
const fakeEngramOldNoisy = `#!/bin/sh
case "$1" in
  serve) echo "serve ENGRAM_PORT=$ENGRAM_PORT" >> "$FAKE_ENGRAM_LOG" ;;
  protocol-mode) echo full ;;
  *) echo "Update available: 1.22.0 (current 1.21.0)" >&2; echo "unknown command: $1" >&2; exit 1 ;;
esac
`

// fakeEngramGarbage answers `engram port` with exit 0 and a non-numeric stdout.
const fakeEngramGarbage = `#!/bin/sh
case "$1" in
  port) echo "not-a-port" ;;
  *) exit 1 ;;
esac
`

type hookOpts struct {
	plugin      string // plugin directory under plugin/; empty = claude-code
	cwd         string
	engram      string                 // fake engram script; empty = fakeEngramBody
	tcp4        *string                // fake /proc/net/tcp contents; nil = file absent
	tcp4Builder func(port int) *string // like tcp4, built once the fake server port is known
	env         []string               // extra environment
	projectResp string                 // body of GET /project/current; empty = {}
	healthDown  bool                   // /health answers 500
	healthBody  string                 // body of GET /health; empty = {}
}

type hookResult struct {
	paths, bodies []string
	queries       []string
	stdout        string
	stderr        string
	serveLog      string
	port          string
}

// tcpTable builds a fake /proc/net/tcp with one LISTEN row per (port, uid).
func tcpTable(rows ...[2]int) *string {
	var b strings.Builder
	b.WriteString("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n")
	for i, r := range rows {
		fmt.Fprintf(&b, "   %d: 0100007F:%04X 00000000:0000 0A 00000000:00000000 00:00000000 00000000  %d        0 %d 1\n", i, r[0], r[1], 1000+i)
	}
	s := b.String()
	return &s
}

// runHook runs a Claude Code plugin script against a fake engram server on a
// free port, with a fake `engram` binary first on PATH, and returns what the
// server received. Nothing here depends on the real /proc or a real engram.
func runHook(t *testing.T, script, stdin string, o hookOpts) hookResult {
	t.Helper()
	for _, bin := range []string{"bash", "jq", "curl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}

	var mu sync.Mutex
	var res hookResult
	projectResp := o.projectResp
	if projectResp == "" {
		projectResp = `{}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/health" && o.healthDown {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		mu.Lock()
		res.paths = append(res.paths, r.URL.Path)
		res.bodies = append(res.bodies, string(b))
		res.queries = append(res.queries, r.URL.RawQuery)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/project/current" {
			_, _ = w.Write([]byte(projectResp))
			return
		}
		if r.URL.Path == "/health" && o.healthBody != "" {
			_, _ = w.Write([]byte(o.healthBody))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	res.port = port

	bin := t.TempDir()
	body := o.engram
	if body == "" {
		body = fakeEngramBody
	}
	if err := os.WriteFile(filepath.Join(bin, "engram"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(t.TempDir(), "serve.log")

	env := append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"ENGRAM_PORT="+port,
		"FAKE_ENGRAM_PORT="+port,
		"FAKE_ENGRAM_LOG="+logFile,
		"ENGRAM_PORT_PROC_TCP6="+filepath.Join(t.TempDir(), "absent-tcp6"),
		"ENGRAM_NO_PROJECT_MIGRATE=",
		// Hooks keep per-user state under $XDG_RUNTIME_DIR or $HOME/.engram: a
		// test never touches the real ones.
		"HOME="+t.TempDir(),
		"XDG_RUNTIME_DIR=",
	)
	if o.tcp4Builder != nil {
		p, _ := strconv.Atoi(port)
		o.tcp4 = o.tcp4Builder(p)
	}
	tcp4 := filepath.Join(t.TempDir(), "tcp")
	if o.tcp4 != nil {
		if err := os.WriteFile(tcp4, []byte(*o.tcp4), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env = append(env, "ENGRAM_PORT_PROC_TCP4="+tcp4)
	env = append(env, o.env...)

	path := filepath.Join(repoRoot(t), "plugin", pluginDir(o), "scripts", script)
	cmd := exec.Command("bash", path)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = env
	var stdout strings.Builder
	cmd.Stdout = &stdout
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s failed: %v\n%s", script, err, stdout.String())
	}
	res.stdout = stdout.String()
	res.stderr = stderr.String()
	if script == "user-prompt-submit.sh" && pluginDir(o) == "claude-code" && stdout.Len() != 0 {
		t.Fatalf("%s must print nothing, got %q", script, stdout.String())
	}
	// A backgrounded fake `serve` may land slightly after the script returns.
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(logFile); err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if b, err := os.ReadFile(logFile); err == nil {
		res.serveLog = string(b)
	}

	mu.Lock()
	defer mu.Unlock()
	res.paths = append([]string(nil), res.paths...)
	res.bodies = append([]string(nil), res.bodies...)
	res.queries = append([]string(nil), res.queries...)
	return res
}

func pluginDir(o hookOpts) string {
	if o.plugin == "" {
		return "claude-code"
	}
	return o.plugin
}

// runHookScript keeps the original simple shape for the basic tests.
func runHookScript(t *testing.T, script, stdin string) (paths []string, bodies []string) {
	t.Helper()
	r := runHook(t, script, stdin, hookOpts{})
	return r.paths, r.bodies
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
	// The project lookup (GET /project/current) precedes the capture.
	if len(paths) == 2 && paths[0] == "/project/current" {
		paths, bodies = paths[1:], bodies[1:]
	}
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

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "engram"), []byte(fakeEngramBody), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", "user-prompt-submit.sh"))
	cmd.Stdin = strings.NewReader(`{"session_id":"s","prompt":"hi"}`)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"ENGRAM_PORT="+port, "FAKE_ENGRAM_PORT="+port,
		"ENGRAM_PORT_PROC_TCP4="+filepath.Join(t.TempDir(), "absent"))
	var stdout strings.Builder
	cmd.Stdout = &stdout
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("hook must exit 0 with the server down: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("hook must print nothing, got %q", stdout.String())
	}
}
