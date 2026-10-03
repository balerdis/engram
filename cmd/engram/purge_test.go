package main

import (
	"database/sql"
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

func TestCmdPurgeEnrolledRefusal(t *testing.T) {
	cfg := testConfig(t)
	seedPurgeCLI(t, cfg)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject("lab"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	exited := 0
	old := exitFunc
	exitFunc = func(code int) { exited = code }
	t.Cleanup(func() { exitFunc = old })
	withArgs(t, "engram", "purge", "--project", "lab", "--yes")
	_, stderr := captureOutput(t, func() { cmdPurge(cfg) })
	if exited != 1 || !strings.Contains(stderr, "enrolled") || !strings.Contains(stderr, "nothing was deleted") {
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
	for _, want := range []string{"Relations touching a purged observation are removed even if the other end is", "engram-purge-<timestamp>.db"} {
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
