package plugin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func codexStateOpts(home, xdg string) hookOpts {
	return hookOpts{plugin: "codex", projectResp: `{"project":"p"}`, env: []string{"HOME=" + home, "XDG_RUNTIME_DIR=" + xdg}}
}

func promptInput(id string) string {
	// jq-free JSON: ids used here need escaping only for backslash and quote.
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(id)
	return `{"session_id":"` + esc + `","prompt":"hi","cwd":"/nonexistent"}`
}

// The state lives in a per-user 0700 directory, with a sanitized name, never in
// /tmp, and a hostile session id cannot leave it.
func TestCodexPromptHookStateIsPrivateAndSanitized(t *testing.T) {
	ids := []string{"../../escape", "a/b/c", "/etc/passwd", "..", ".", "-rf", "x\ny", "a b;$(id)", strings.Repeat("z", 500), "ñandú"}
	for _, xdgSet := range []bool{false, true} {
		home := t.TempDir()
		xdg := ""
		base := filepath.Join(home, ".engram")
		if xdgSet {
			xdg = t.TempDir()
			base = xdg
		}
		o := codexStateOpts(home, xdg)
		for _, id := range ids {
			r := runHook(t, "user-prompt-submit.sh", promptInput(id), o)
			if !strings.Contains(r.stdout, "CRITICAL FIRST ACTION") {
				t.Fatalf("id %q: first message must inject, got %q", id, r.stdout)
			}
			r = runHook(t, "user-prompt-submit.sh", promptInput(id), o)
			if strings.Contains(r.stdout, "CRITICAL FIRST ACTION") {
				t.Fatalf("id %q: second message must not inject again", id)
			}
		}
		dir := filepath.Join(base, "codex-state")
		st, err := os.Stat(dir)
		if err != nil || st.Mode().Perm() != 0o700 {
			t.Fatalf("state dir = %v, %v; want 0700", st, err)
		}
		ents, _ := os.ReadDir(dir)
		if len(ents) != len(ids) {
			t.Fatalf("want one state file per id (%d), got %d", len(ids), len(ents))
		}
		for _, e := range ents {
			n := e.Name()
			if len(n) > 120 || !strings.HasPrefix(n, "s-") || strings.Trim(n, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-") != "" {
				t.Fatalf("unsafe state file name %q", n)
			}
			if fi, _ := e.Info(); fi.Mode().Perm() != 0o600 {
				t.Fatalf("%s mode %v, want 0600", n, fi.Mode().Perm())
			}
		}
		// Nothing leaked next to or above the directory.
		for _, d := range []string{filepath.Dir(base), base, filepath.Dir(dir)} {
			es, _ := os.ReadDir(d)
			for _, e := range es {
				if strings.Contains(e.Name(), "escape") || e.Name() == "passwd" {
					t.Fatalf("hostile id escaped into %s: %s", d, e.Name())
				}
			}
		}
	}
	if m, _ := filepath.Glob("/tmp/engram-codex-*"); len(m) > 0 {
		for _, f := range m {
			if strings.Contains(f, "escape") {
				t.Fatalf("state written to /tmp: %s", f)
			}
		}
	}
}

// Ids that sanitize to the same text keep separate state.
func TestCodexPromptHookKeepsCollidingIDsApart(t *testing.T) {
	o := codexStateOpts(t.TempDir(), "")
	runHook(t, "user-prompt-submit.sh", promptInput("a/b"), o)
	r := runHook(t, "user-prompt-submit.sh", promptInput("a?b"), o)
	if !strings.Contains(r.stdout, "CRITICAL FIRST ACTION") {
		t.Fatalf("a?b shared state with a/b: %q", r.stdout)
	}
}

// A symlink planted where the state file goes is neither followed nor written.
func TestCodexPromptHookDoesNotFollowASymlinkStateFile(t *testing.T) {
	home := t.TempDir()
	o := codexStateOpts(home, "")
	runHook(t, "user-prompt-submit.sh", promptInput("victim"), o)
	dir := filepath.Join(home, ".engram", "codex-state")
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("setup: %d files", len(ents))
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Replace the state file of a fresh id by a symlink to the target.
	runHook(t, "user-prompt-submit.sh", promptInput("other"), o)
	ents, _ = os.ReadDir(dir)
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		_ = os.Remove(p)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"victim", "other"} {
		r := runHook(t, "user-prompt-submit.sh", promptInput(id), o)
		if strings.Contains(r.stdout, "CRITICAL FIRST ACTION") {
			t.Fatalf("injected over a symlink state file: %q", r.stdout)
		}
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatalf("symlink target was written: %q", b)
	}
}

// A state directory that is a symlink (or someone else's) is not used.
func TestCodexPromptHookRefusesASymlinkStateDir(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".engram"), 0o700); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(home, ".engram", "codex-state")); err != nil {
		t.Fatal(err)
	}
	r := runHook(t, "user-prompt-submit.sh", promptInput("s1"), codexStateOpts(home, ""))
	if strings.Contains(r.stdout, "CRITICAL FIRST ACTION") {
		t.Fatalf("used a symlinked state dir: %q", r.stdout)
	}
	if es, _ := os.ReadDir(elsewhere); len(es) != 0 {
		t.Fatalf("wrote through the symlinked dir: %v", es)
	}
}
