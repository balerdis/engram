package plugin_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func hookInput(t *testing.T, m map[string]string) string {
	t.Helper()
	b, _ := json.Marshal(m)
	return string(b)
}

func TestHooksSendWhenPortIsOwnedByOurUID(t *testing.T) {
	me := os.Getuid()
	o := hookOpts{tcp4Builder: func(port int) *string { return tcpTable([2]int{port, me}) }}
	r := runHook(t, "session-end.sh", `{"session_id":"s1"}`, o)
	if len(r.paths) != 1 || r.paths[0] != "/sessions/s1/end" {
		t.Fatalf("requests = %v, want the session end", r.paths)
	}
}

func TestHooksSendNothingWhenOnlyAnotherUIDHoldsTheOldBinaryPort(t *testing.T) {
	foreign := os.Getuid() + 1
	o := hookOpts{
		engram:      fakeEngramOld,
		tcp4Builder: func(port int) *string { return tcpTable([2]int{port, foreign}) },
	}
	for _, c := range []struct{ script, in string }{
		{"session-end.sh", `{"session_id":"s1"}`},
		{"user-prompt-submit.sh", `{"session_id":"s1","prompt":"hello there"}`},
		{"subagent-stop.sh", hookInput(t, map[string]string{"session_id": "s1", "cwd": t.TempDir(), "last_assistant_message": "x"})},
	} {
		if r := runHook(t, c.script, c.in, o); len(r.paths) != 0 {
			t.Fatalf("%s: expected no request, got %v", c.script, r.paths)
		}
	}
	o.healthDown = true
	r := runHook(t, "session-start.sh", hookInput(t, map[string]string{"session_id": "s1", "cwd": t.TempDir()}), o)
	if len(r.paths) != 0 || r.serveLog != "" || r.stdout != "" {
		t.Fatalf("session-start must be silent, send nothing and start nothing: %v %q %q", r.paths, r.serveLog, r.stdout)
	}
}

func TestHooksOldBinaryWithOwnedPortStillWorks(t *testing.T) {
	me := os.Getuid()
	o := hookOpts{
		engram:      fakeEngramOld,
		tcp4Builder: func(port int) *string { return tcpTable([2]int{port, me}) },
	}
	r := runHook(t, "session-end.sh", `{"session_id":"s1"}`, o)
	if len(r.paths) != 1 {
		t.Fatalf("requests = %v, want one", r.paths)
	}
}

func TestHooksSendNothingWhenEngramPortSaysDisabled(t *testing.T) {
	o := hookOpts{engram: fakeEngramDisabled, healthDown: true}
	for _, c := range []struct{ script, in string }{
		{"session-end.sh", `{"session_id":"s1"}`},
		{"user-prompt-submit.sh", `{"session_id":"s1","prompt":"hello there"}`},
		{"subagent-stop.sh", hookInput(t, map[string]string{"session_id": "s1", "cwd": t.TempDir(), "last_assistant_message": "x"})},
		{"post-compaction.sh", hookInput(t, map[string]string{"session_id": "s1", "cwd": t.TempDir()})},
		{"session-start.sh", hookInput(t, map[string]string{"session_id": "s1", "cwd": t.TempDir()})},
	} {
		r := runHook(t, c.script, c.in, o)
		if len(r.paths) != 0 || r.serveLog != "" || r.stdout != "" {
			t.Fatalf("%s: expected silence, got paths=%v serve=%q stdout=%q", c.script, r.paths, r.serveLog, r.stdout)
		}
	}
}

func TestSessionStartStartsServeWithExplicitResolvedPort(t *testing.T) {
	o := hookOpts{healthDown: true, env: []string{"ENGRAM_PORT="}}
	r := runHook(t, "session-start.sh", hookInput(t, map[string]string{"session_id": "s1", "cwd": t.TempDir()}), o)
	want := "serve ENGRAM_PORT=" + r.port
	if !strings.Contains(r.serveLog, want) {
		t.Fatalf("serve log = %q, want %q", r.serveLog, want)
	}
}

func TestSubagentStopUsesProjectFromServer(t *testing.T) {
	cwd := t.TempDir()
	o := hookOpts{projectResp: `{"project":"canonical-name","project_source":"git_remote","project_path":"/x"}`}
	r := runHook(t, "subagent-stop.sh", hookInput(t, map[string]string{"session_id": "s1", "cwd": cwd, "last_assistant_message": "learned"}), o)
	var sawQuery, sawPassive bool
	for i, p := range r.paths {
		if p == "/project/current" {
			sawQuery = strings.Contains(r.queries[i], "cwd=")
		}
		if p == "/observations/passive" {
			sawPassive = true
			var got map[string]string
			_ = json.Unmarshal([]byte(r.bodies[i]), &got)
			if got["project"] != "canonical-name" {
				t.Fatalf("project = %q, want canonical-name", got["project"])
			}
		}
	}
	if !sawQuery || !sawPassive {
		t.Fatalf("paths = %v (query=%v passive=%v)", r.paths, sawQuery, sawPassive)
	}
}

func TestSubagentStopFallbackProjectIsCanonical(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "My__Proj--X")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	r := runHook(t, "subagent-stop.sh", hookInput(t, map[string]string{"session_id": "s1", "cwd": cwd, "last_assistant_message": "learned"}), hookOpts{})
	for i, p := range r.paths {
		if p == "/observations/passive" {
			var got map[string]string
			_ = json.Unmarshal([]byte(r.bodies[i]), &got)
			if got["project"] != "my_proj-x" {
				t.Fatalf("project = %q, want my_proj-x", got["project"])
			}
			return
		}
	}
	t.Fatalf("no passive capture, paths = %v", r.paths)
}

func TestAmbiguousProjectSkipsProjectScopedWork(t *testing.T) {
	o := hookOpts{projectResp: `{"project":"","error_hint":"ambiguous","available_projects":["a","b"]}`}
	cwd := t.TempDir()
	r := runHook(t, "subagent-stop.sh", hookInput(t, map[string]string{"session_id": "s1", "cwd": cwd, "last_assistant_message": "x"}), o)
	for _, p := range r.paths {
		if p == "/observations/passive" {
			t.Fatalf("ambiguous project must not send a capture: %v", r.paths)
		}
	}
	r = runHook(t, "session-start.sh", hookInput(t, map[string]string{"session_id": "s1", "cwd": cwd}), o)
	for _, p := range r.paths {
		if p == "/projects/migrate" || p == "/sessions" || p == "/context" {
			t.Fatalf("ambiguous project must skip %s: %v", p, r.paths)
		}
	}
}

func gitRepo(t *testing.T, name string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	return dir
}

func migrates(r hookResult) bool {
	for _, p := range r.paths {
		if p == "/projects/migrate" {
			return true
		}
	}
	return false
}

func TestSessionStartMigratesOnlyFromGitRoot(t *testing.T) {
	root := gitRepo(t, "OldName")
	o := hookOpts{projectResp: `{"project":"newname"}`}
	r := runHook(t, "session-start.sh", hookInput(t, map[string]string{"session_id": "s1", "cwd": root}), o)
	if !migrates(r) {
		t.Fatalf("expected migrate from the git root, paths = %v", r.paths)
	}
	for i, p := range r.paths {
		if p == "/projects/migrate" {
			var got map[string]string
			_ = json.Unmarshal([]byte(r.bodies[i]), &got)
			if got["old_project"] != "oldname" || got["new_project"] != "newname" {
				t.Fatalf("migrate body = %v", got)
			}
		}
	}
	r = runHook(t, "session-start.sh", hookInput(t, map[string]string{"session_id": "s1", "cwd": filepath.Join(root, "sub")}), o)
	if migrates(r) {
		t.Fatalf("must not migrate from a subdirectory: %v", r.paths)
	}
}

func TestSessionStartDoesNotMigrateWhenDisabledOrSameName(t *testing.T) {
	root := gitRepo(t, "OldName")
	in := hookInput(t, map[string]string{"session_id": "s1", "cwd": root})
	r := runHook(t, "session-start.sh", in, hookOpts{projectResp: `{"project":"newname"}`, env: []string{"ENGRAM_NO_PROJECT_MIGRATE=1"}})
	if migrates(r) {
		t.Fatalf("ENGRAM_NO_PROJECT_MIGRATE=1 must disable migrate: %v", r.paths)
	}
	r = runHook(t, "session-start.sh", in, hookOpts{projectResp: `{"project":"oldname"}`})
	if migrates(r) {
		t.Fatalf("same canonical name must not migrate: %v", r.paths)
	}
}

func TestProtocolBlocksSayWhoSaves(t *testing.T) {
	for _, script := range []string{"session-start.sh", "post-compaction.sh"} {
		r := runHook(t, script, hookInput(t, map[string]string{"session_id": "s1", "cwd": t.TempDir()}), hookOpts{})
		for _, want := range []string{
			"WHO SAVES: only the agent talking with the person saves memories.",
			"whoever launched you decides what to save.",
			"PROACTIVE SAVE (only the agent talking with the person)",
			"SESSION CLOSE (only the agent talking with the person)",
		} {
			if !strings.Contains(r.stdout, want) {
				t.Fatalf("%s: output lacks %q:\n%s", script, want, r.stdout)
			}
		}
	}
}
