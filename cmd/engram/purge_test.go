package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func seedPurgeCLI(t *testing.T, cfg store.Config) {
	t.Helper()
	mustSeedSession(t, cfg, "s-lab", "lab")
	mustSeedObservation(t, cfg, "s-lab", "lab", "decision", "lab note", "disposable content", "project")
	mustSeedSession(t, cfg, "s-keep", "keep")
	mustSeedObservation(t, cfg, "s-keep", "keep", "decision", "keep note", "valuable content", "project")
}

func TestCmdPurgeDryRunThenExecute(t *testing.T) {
	cfg := testConfig(t)
	seedPurgeCLI(t, cfg)

	withArgs(t, "engram", "purge", "--project", "LAB")
	stdout, stderr := captureOutput(t, func() { cmdPurge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "dry-run") || !strings.Contains(stdout, "observations") || !strings.Contains(stdout, "lab note") {
		t.Fatalf("unexpected dry-run output: %q / %q", stdout, stderr)
	}

	withArgs(t, "engram", "purge", "--project", "lab", "--yes")
	stdout, stderr = captureOutput(t, func() { cmdPurge(cfg) })
	if stderr != "" || !strings.Contains(stdout, "Backup written:") || !strings.Contains(stdout, "Purged") {
		t.Fatalf("unexpected execute output: %q / %q", stdout, stderr)
	}

	withArgs(t, "engram", "purge", "--project", "lab", "--yes")
	stdout, _ = captureOutput(t, func() { cmdPurge(cfg) })
	if !strings.Contains(stdout, "Nothing matched") {
		t.Fatalf("expected nothing-matched message, got %q", stdout)
	}
}

func TestCmdPurgeJSON(t *testing.T) {
	cfg := testConfig(t)
	seedPurgeCLI(t, cfg)
	withArgs(t, "engram", "purge", "--project", "lab", "--json")
	stdout, _ := captureOutput(t, func() { cmdPurge(cfg) })
	if !strings.Contains(stdout, `"dry_run": true`) || !strings.Contains(stdout, `"observations": 1`) {
		t.Fatalf("unexpected json: %q", stdout)
	}
}

func TestCmdPurgeNoSelectorExits(t *testing.T) {
	cfg := testConfig(t)
	exited := 0
	old := exitFunc
	exitFunc = func(code int) { exited = code }
	t.Cleanup(func() { exitFunc = old })
	withArgs(t, "engram", "purge", "--yes")
	_, stderr := captureOutput(t, func() { cmdPurge(cfg) })
	if exited != 1 || !strings.Contains(stderr, "at least one selector") {
		t.Fatalf("exit=%d stderr=%q", exited, stderr)
	}
}

func TestCmdPurgeUnreadableDateWarningHumanAndJSON(t *testing.T) {
	cfg := testConfig(t)
	seedPurgeCLI(t, cfg)
	corruptObservationDate(t, cfg, "lab", "garbage")

	withArgs(t, "engram", "purge", "--project", "lab", "--since", "2026-01-01")
	stdout, _ := captureOutput(t, func() { cmdPurge(cfg) })
	if !strings.Contains(stdout, "rows with an unreadable date were not selected by the date filter") {
		t.Fatalf("human output missing warning: %q", stdout)
	}
	withArgs(t, "engram", "purge", "--project", "lab", "--since", "2026-01-01", "--json")
	stdout, _ = captureOutput(t, func() { cmdPurge(cfg) })
	if !strings.Contains(stdout, "rows with an unreadable date were not selected by the date filter") || !strings.Contains(stdout, `"unreadable_date_rows": 1`) {
		t.Fatalf("json output missing warning: %q", stdout)
	}
}

func TestPurgeHelpMentionsRelationsAndBackup(t *testing.T) {
	_, stderr := captureOutput(t, func() { printPurgeUsage() })
	for _, want := range []string{"Relations touching a purged observation are removed even if the other end is", "engram-purge-YYYYMMDDTHHMMSSZ.db"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("usage missing %q: %q", want, stderr)
		}
	}
}

func TestShouldCheckForUpdatesSkipsPurge(t *testing.T) {
	if shouldCheckForUpdates([]string{"purge", "--project", "x"}) {
		t.Fatal("purge must not trigger the update check")
	}
}

func corruptObservationDate(t *testing.T, cfg store.Config, project, ts string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE observations SET created_at=? WHERE project=?`, ts, project); err != nil {
		t.Fatal(err)
	}
}

// runPurgeExpectExit runs cmdPurge with a stubbed exitFunc and returns what it
// printed and the exit code (0 when it never exited).
func runPurgeExpectExit(t *testing.T, cfg store.Config, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	old := exitFunc
	exitFunc = func(c int) { code = c }
	t.Cleanup(func() { exitFunc = old })
	withArgs(t, append([]string{"engram", "purge"}, args...)...)
	stdout, stderr = captureOutput(t, func() { cmdPurge(cfg) })
	return stdout, stderr, code
}

func assertNoStoreOpened(t *testing.T, cfg store.Config) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "engram.db")); err == nil {
		t.Fatal("the store was opened before the arguments were validated")
	}
}

func TestCmdPurgeBadDateIsUsageErrorBeforeOpeningStore(t *testing.T) {
	cfg := testConfig(t)
	for _, args := range [][]string{
		{"--since", "yesterday"},
		{"--project", "lab", "--until", "2026-99-99"},
	} {
		_, stderr, code := runPurgeExpectExit(t, cfg, args...)
		if code != 1 || !strings.Contains(stderr, "invalid date") || !strings.Contains(stderr, "usage: engram purge") {
			t.Fatalf("args %v: code=%d stderr=%q", args, code, stderr)
		}
		assertNoStoreOpened(t, cfg)
	}
}

func TestCmdPurgeSinceAfterUntilRefused(t *testing.T) {
	cfg := testConfig(t)
	_, stderr, code := runPurgeExpectExit(t, cfg, "--since", "2026-02-01", "--until", "2026-01-01")
	if code != 1 || !strings.Contains(stderr, "purge refused: --since is after --until") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	assertNoStoreOpened(t, cfg)
	// Same day is fine.
	seedPurgeCLI(t, cfg)
	_, stderr, code = runPurgeExpectExit(t, cfg, "--since", "2026-02-01", "--until", "2026-02-01")
	if code != 0 || stderr != "" {
		t.Fatalf("same-day range rejected: code=%d stderr=%q", code, stderr)
	}
}

func TestCmdPurgeFlagLikeValueRejected(t *testing.T) {
	cfg := testConfig(t)
	for _, args := range [][]string{
		{"--project", "--yes"},
		{"--session", "-x"},
		{"--since", "--until", "2026-01-01"},
	} {
		_, stderr, code := runPurgeExpectExit(t, cfg, args...)
		if code != 1 || !strings.Contains(stderr, "looks like a flag") {
			t.Fatalf("args %v: code=%d stderr=%q", args, code, stderr)
		}
		assertNoStoreOpened(t, cfg)
	}
	// The explicit = form still carries a dash.
	if _, stderr, code := runPurgeExpectExit(t, cfg, "--session=-x"); code != 0 || strings.Contains(stderr, "looks like a flag") {
		t.Fatalf("--session=-x rejected: code=%d stderr=%q", code, stderr)
	}
}

func TestCmdPurgeJSONErrors(t *testing.T) {
	cfg := testConfig(t)
	seedPurgeCLI(t, cfg)
	pin := func() {
		s, err := store.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		id, err := s.AddObservation(store.AddObservationParams{SessionID: "s-lab", Type: "decision", Title: "pinned", Content: "pinned content", Project: "lab", Scope: "project"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PinObservation(id); err != nil {
			t.Fatal(err)
		}
	}
	pin()
	cases := []struct {
		args    []string
		refused bool
		text    string
	}{
		{[]string{"--json", "--project", "lab"}, true, "pinned"},
		{[]string{"--project", "lab", "--yes", "--json"}, true, "pinned"},
		{[]string{"--json", "--since", "2026-02-01", "--until", "2026-01-01"}, true, "--since is after --until"},
		{[]string{"--json", "--since", "nonsense"}, false, "invalid date"},
		{[]string{"--json", "--project", "--yes"}, false, "looks like a flag"},
		{[]string{"--json", "--bogus"}, false, "unknown purge argument"},
		{[]string{"--json"}, false, "at least one selector"},
	}
	for _, c := range cases {
		stdout, stderr, code := runPurgeExpectExit(t, cfg, c.args...)
		var got struct {
			Error   string `json:"error"`
			Refused bool   `json:"refused"`
		}
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("args %v: stdout is not JSON: %q (%v)", c.args, stdout, err)
		}
		if code != 1 || stderr != "" || got.Refused != c.refused || !strings.Contains(got.Error, c.text) {
			t.Fatalf("args %v: code=%d stderr=%q got=%+v", c.args, code, stderr, got)
		}
	}
}

func TestCmdPurgeExecutePrintsBackupReminder(t *testing.T) {
	cfg := testConfig(t)
	seedPurgeCLI(t, cfg)
	stdout, stderr, code := runPurgeExpectExit(t, cfg, "--project", "lab", "--yes")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	matches, _ := filepath.Glob(filepath.Join(cfg.DataDir, "backups", "engram-purge-*.db"))
	if len(matches) != 1 {
		t.Fatalf("backups: %v", matches)
	}
	if !strings.Contains(stdout, "Backup kept at "+matches[0]) || !strings.Contains(stdout, "not deleted automatically") {
		t.Fatalf("missing backup reminder: %q", stdout)
	}
}
