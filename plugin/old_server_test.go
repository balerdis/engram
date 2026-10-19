package plugin_test

import (
	"os"
	"strings"
	"testing"
)

// fakeEngramVersion is a binary that reports the given version.
func fakeEngramVersion(version string) string {
	return `#!/bin/sh
case "$1" in
  port) echo "$FAKE_ENGRAM_PORT" ;;
  version) echo "engram ` + version + `" ;;
  serve) echo "serve ENGRAM_PORT=$ENGRAM_PORT" >> "$FAKE_ENGRAM_LOG" ;;
  protocol-mode) echo full ;;
  sync) ;;
  *) echo "unknown command: $1" >&2; exit 1 ;;
esac
`
}

// A 1.21 serve that kept running after the binary became 1.22.0 is announced,
// once, on stdout (the context the model reads) and on stderr, with the fix.
func TestSessionStartWarnsAboutAnOlderRunningServer(t *testing.T) {
	for _, dir := range pluginDirs {
		in := hookInput(t, map[string]string{"session_id": "s1", "cwd": t.TempDir()})
		r := runHook(t, "session-start.sh", in, hookOpts{plugin: dir, engram: fakeEngramVersion("1.22.0"), healthBody: `{"status":"ok","version":"1.21.0"}`})
		for name, out := range map[string]string{"stdout": r.stdout, "stderr": r.stderr} {
			if strings.Count(out, "WARNING (engram)") != 1 {
				t.Fatalf("%s/%s: want exactly one warning, got %q", dir, name, out)
			}
			for _, want := range []string{"1.21.0", "1.22.0", "Stop it", "engram serve"} {
				if !strings.Contains(out, want) {
					t.Fatalf("%s/%s: warning lacks %q: %q", dir, name, want, out)
				}
			}
		}
		if r.serveLog != "" {
			t.Fatalf("%s: a running server must not be restarted: %q", dir, r.serveLog)
		}
	}
}

func TestSessionStartStaysQuietWhenVersionsMatchOrAreUnknown(t *testing.T) {
	cases := []struct{ name, engram, health string }{
		{"same version", fakeEngramVersion("1.22.0"), `{"version":"1.22.0"}`},
		{"dev server", fakeEngramVersion("1.22.0"), `{"version":"dev"}`},
		{"dev binary", fakeEngramVersion("dev"), `{"version":"1.21.0"}`},
		{"server without version", fakeEngramVersion("1.22.0"), `{"status":"ok"}`},
		{"binary without version command", fakeEngramBody, `{"version":"1.21.0"}`},
	}
	for _, dir := range pluginDirs {
		for _, c := range cases {
			in := hookInput(t, map[string]string{"session_id": "s1", "cwd": t.TempDir()})
			r := runHook(t, "session-start.sh", in, hookOpts{plugin: dir, engram: c.engram, healthBody: c.health})
			if strings.Contains(r.stdout, "WARNING (engram)") || strings.Contains(r.stderr, "WARNING (engram)") {
				t.Fatalf("%s/%s: unexpected warning: %q %q", dir, c.name, r.stdout, r.stderr)
			}
		}
	}
}

// A server that this hook starts itself is the new binary: no comparison.
func TestSessionStartDoesNotWarnForAServerItStarted(t *testing.T) {
	for _, dir := range pluginDirs {
		in := hookInput(t, map[string]string{"session_id": "s1", "cwd": t.TempDir()})
		r := runHook(t, "session-start.sh", in, hookOpts{plugin: dir, engram: fakeEngramVersion("1.22.0"), healthDown: true, healthBody: `{"version":"1.21.0"}`})
		if strings.Contains(r.stdout, "WARNING (engram)") {
			t.Fatalf("%s: warned about a server the hook started: %q", dir, r.stdout)
		}
		if r.serveLog == "" {
			t.Fatalf("%s: serve was not started", dir)
		}
	}
}

// The Codex prompt hook asks the server for the session start time: the id must
// be URL-encoded like in the other scripts (an id with ? or a space arrived cut).
func TestCodexPromptHookURLEncodesTheSessionID(t *testing.T) {
	id := "enc?x y-" + strings.ReplaceAll(t.Name(), "/", "_")
	t.Cleanup(func() { _ = os.Remove("/tmp/engram-codex-" + id + "-tools-loaded") })
	in := `{"session_id":"` + id + `","prompt":"hello there","cwd":"` + t.TempDir() + `"}`
	o := hookOpts{plugin: "codex", projectResp: `{"project":"p"}`}
	runHook(t, "user-prompt-submit.sh", in, o) // first message: creates the state file
	r := runHook(t, "user-prompt-submit.sh", in, o)
	want := "/sessions/" + id
	if !contains(r.paths, want) {
		t.Fatalf("paths = %v, want the whole encoded id %q", r.paths, want)
	}
}
