package plugin_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A prompt or a subagent message bigger than one argv string (128 KiB on
// Linux) must reach the server complete: the body travels through a pipe.
func TestLargeTextReachesTheServerComplete(t *testing.T) {
	big := strings.Repeat("0123456789abcdef", 300*1024/16) // ~300 KB
	for _, dir := range pluginDirs {
		cases := []struct{ script, field, path string }{
			{"user-prompt-submit.sh", "prompt", "/prompts"},
			{"subagent-stop.sh", "last_assistant_message", "/observations/passive"},
		}
		for _, c := range cases {
			in := hookInput(t, map[string]string{"session_id": "s1", "cwd": t.TempDir(), c.field: big})
			r := runHook(t, c.script, in, hookOpts{plugin: dir})
			var body string
			for i, p := range r.paths {
				if p == c.path {
					body = r.bodies[i]
				}
			}
			var got struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(body), &got); err != nil || got.Content != big {
				t.Fatalf("%s/%s: body not complete (len %d, err %v), paths %v", dir, c.script, len(got.Content), err, r.paths)
			}
		}
	}
}

func importCwd(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".engram"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".engram", "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return cwd
}

// The import is skipped when its lock directory cannot be trusted; one line on
// stderr says so and why, and nothing reaches stdout (the model context).
func TestSessionStartSaysWhyTheImportWasSkipped(t *testing.T) {
	for _, dir := range pluginDirs {
		cwd := importCwd(t)
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		in := hookInput(t, map[string]string{"session_id": "s1", "cwd": cwd})

		r := runHook(t, "session-start.sh", in, hookOpts{plugin: dir, env: []string{"XDG_RUNTIME_DIR=" + filepath.Join(blocker, "x")}})
		if !strings.Contains(r.stderr, "chunk import skipped") || !strings.Contains(r.stderr, "is missing") {
			t.Fatalf("%s: stderr = %q, want a missing-directory line", dir, r.stderr)
		}
		if strings.Contains(r.stdout, "chunk import skipped") {
			t.Fatalf("%s: the skip line must not reach stdout", dir)
		}

		if os.Getuid() != 0 {
			r = runHook(t, "session-start.sh", in, hookOpts{plugin: dir, env: []string{"XDG_RUNTIME_DIR=/"}})
			if !strings.Contains(r.stderr, "not owned by this user") {
				t.Fatalf("%s: stderr = %q, want a not-owned line", dir, r.stderr)
			}
		}

		r = runHook(t, "session-start.sh", in, hookOpts{plugin: dir, env: []string{"XDG_RUNTIME_DIR=" + t.TempDir()}})
		if strings.Contains(r.stderr, "chunk import skipped") {
			t.Fatalf("%s: a usable directory must not report a skip: %q", dir, r.stderr)
		}
	}
}

// The backgrounded import must not hold the hook's stderr pipe: a caller that
// waits for its stdio to close (as exec.Cmd.Run does here) would otherwise wait
// for the whole import. No time threshold: the fake `sync` blocks on a gate file
// that only this test creates, so a hook that returns while the import is still
// running (its recorded pid alive) cannot have waited for it. If the hook did
// hold the pipe it would return only when the script's own import timeout kills
// the import, and the pid would be dead.
func TestSessionStartDoesNotHoldStderrDuringTheImport(t *testing.T) {
	for _, dir := range pluginDirs {
		state := t.TempDir()
		gate := filepath.Join(state, "gate")
		pidFile := filepath.Join(state, "pid")
		release := func() {
			if f, err := os.Create(gate); err == nil {
				_ = f.Close()
			}
		}
		t.Cleanup(release)
		blockedImport := strings.Replace(fakeEngramBody, "  sync) ;;",
			"  sync) echo $$ > '"+pidFile+".tmp' && mv '"+pidFile+".tmp' '"+pidFile+"'; while [ ! -e '"+gate+"' ]; do sleep 0.02; done ;;", 1)
		in := hookInput(t, map[string]string{"session_id": "s1", "cwd": importCwd(t)})
		runHook(t, "session-start.sh", in, hookOpts{plugin: dir, engram: blockedImport, env: []string{"XDG_RUNTIME_DIR=" + t.TempDir()}})

		// The hook has returned. The import starts in the background, maybe a
		// moment later: wait for its pid, then it must still be blocked on the gate.
		var pid int
		for i := 0; i < 1500 && pid == 0; i++ {
			if b, err := os.ReadFile(pidFile); err == nil {
				pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			}
			if pid == 0 {
				time.Sleep(20 * time.Millisecond)
			}
		}
		if pid == 0 {
			t.Fatalf("%s: the import never started", dir)
		}
		proc, err := os.FindProcess(pid)
		if err != nil || proc.Signal(syscall.Signal(0)) != nil {
			t.Fatalf("%s: the hook returned only after the import ended (pid %d gone): it held its stdio", dir, pid)
		}
		release()
		for i := 0; i < 1500 && proc.Signal(syscall.Signal(0)) == nil; i++ {
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// Nothing to migrate when the old name already is the canonical one.
func TestSessionStartSkipsMigrateWhenTheNameIsUnchanged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := gitRepo(t, "Same_Name")
	for _, dir := range pluginDirs {
		in := hookInput(t, map[string]string{"session_id": "s1", "cwd": root})
		if r := runHook(t, "session-start.sh", in, hookOpts{plugin: dir, projectResp: `{"project":"same_name"}`}); migrates(r) {
			t.Fatalf("%s: same canonical name must not call migrate: %v", dir, r.paths)
		}
		if r := runHook(t, "session-start.sh", in, hookOpts{plugin: dir, projectResp: `{"project":"other"}`}); !migrates(r) {
			t.Fatalf("%s: a changed name must call migrate: %v", dir, r.paths)
		}
	}
}
