package plugin_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// for the whole import.
func TestSessionStartDoesNotHoldStderrDuringTheImport(t *testing.T) {
	slowImport := strings.Replace(fakeEngramBody, "  sync) ;;", "  sync) sleep 3 ;;", 1)
	for _, dir := range pluginDirs {
		in := hookInput(t, map[string]string{"session_id": "s1", "cwd": importCwd(t)})
		start := time.Now()
		runHook(t, "session-start.sh", in, hookOpts{plugin: dir, engram: slowImport, env: []string{"XDG_RUNTIME_DIR=" + t.TempDir()}})
		if took := time.Since(start); took >= 2500*time.Millisecond {
			t.Fatalf("%s: the hook held its stdio for %v, until the import ended", dir, took)
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
