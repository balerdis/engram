package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		{[]string{"--json", "--project", "lab", "--yes=false"}, false, "takes no value"},
		{[]string{"--json", "--project", "lab", "--yes=0"}, false, "takes no value"},
		{[]string{"--json", "--json=false", "--project", "lab"}, false, "takes no value"},
		{[]string{"--json", "--project", "lab", "--include-pinned=false"}, false, "takes no value"},
		{[]string{"--json", "--project", "lab", "--vacuum=no"}, false, "takes no value"},
		{[]string{"--json", "--project", "lab", "--sync-leftovers=false"}, false, "takes no value"},
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

// seedSyncLeftoversCLI writes the rows an old cloud-sync database carries.
func seedSyncLeftoversCLI(t *testing.T, cfg store.Config) {
	t.Helper()
	st, err := store.New(cfg) // creates and migrates the database
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`INSERT OR IGNORE INTO sync_state (target_key, lifecycle) VALUES ('cloud', 'idle')`,
		`INSERT OR IGNORE INTO sync_state (target_key, lifecycle) VALUES ('local', 'healthy')`,
		`INSERT INTO sync_state (target_key, lifecycle) VALUES ('cloud:lab', 'idle')`,
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, project) VALUES
			('cloud','observation','a','upsert','{"x":1}','lab'),('cloud','observation','b','upsert','{"x":2}','lab')`,
		`INSERT INTO sync_apply_deferred (sync_id, entity, payload) VALUES ('d1','relation','{}')`,
		`INSERT INTO sync_enrolled_projects (project) VALUES ('lab')`,
		`INSERT INTO cloud_upgrade_state (project) VALUES ('lab')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
}

func leftoverRows(t *testing.T, cfg store.Config) map[string]int {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string]int{}
	for _, tb := range []string{"sync_mutations", "sync_apply_deferred", "sync_enrolled_projects", "cloud_upgrade_state", "sync_state", "sync_chunks", "observations"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + tb).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[tb] = n
	}
	return out
}

func TestCmdPurgeSyncLeftoversDryRunThenYes(t *testing.T) {
	cfg := testConfig(t)
	seedPurgeCLI(t, cfg)
	seedSyncLeftoversCLI(t, cfg)
	before := leftoverRows(t, cfg)

	stdout, stderr, code := runPurgeExpectExit(t, cfg, "--sync-leftovers")
	if code != 0 || stderr != "" {
		t.Fatalf("dry run: code=%d stderr=%q", code, stderr)
	}
	for _, want := range []string{"dry-run", "sync_mutations", "sync_apply_deferred", "sync_enrolled_projects", "cloud_upgrade_state", "sync_state", "never touched"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry run output missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, "~") { // approximate bytes
		t.Fatalf("dry run shows no sizes:\n%s", stdout)
	}
	if got := leftoverRows(t, cfg); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Fatalf("dry run changed the database: %v -> %v", before, got)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "backups")); err == nil {
		t.Fatal("dry run created a backup dir")
	}

	stdout, stderr, code = runPurgeExpectExit(t, cfg, "--sync-leftovers", "--yes")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "Backup written:") || !strings.Contains(stdout, "Backup kept at") {
		t.Fatalf("--yes: code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	after := leftoverRows(t, cfg)
	for _, tb := range []string{"sync_mutations", "sync_apply_deferred", "sync_enrolled_projects", "cloud_upgrade_state"} {
		if after[tb] != 0 {
			t.Errorf("%s: %d rows left", tb, after[tb])
		}
	}
	if after["sync_state"] != 2 || after["observations"] != before["observations"] || after["sync_chunks"] != before["sync_chunks"] {
		t.Errorf("unexpected survivors: %v (before %v)", after, before)
	}
	matches, _ := filepath.Glob(filepath.Join(cfg.DataDir, "backups", "engram-purge-*.db"))
	if len(matches) != 1 {
		t.Fatalf("backups: %v", matches)
	}
	if fi, _ := os.Stat(matches[0]); fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %o", fi.Mode().Perm())
	}

	stdout, _, _ = runPurgeExpectExit(t, cfg, "--sync-leftovers", "--yes")
	if !strings.Contains(stdout, "No sync leftovers found") {
		t.Fatalf("second run: %q", stdout)
	}
}

func TestCmdPurgeSyncLeftoversJSON(t *testing.T) {
	cfg := testConfig(t)
	seedSyncLeftoversCLI(t, cfg)

	stdout, _, _ := runPurgeExpectExit(t, cfg, "--sync-leftovers", "--json")
	var dry struct {
		SyncLeftovers bool `json:"sync_leftovers"`
		DryRun        bool `json:"dry_run"`
		Plan          struct {
			Tables []struct {
				Table       string `json:"table"`
				Rows        int    `json:"rows"`
				ApproxBytes int64  `json:"approx_bytes"`
			} `json:"tables"`
			TotalRows int `json:"total_rows"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(stdout), &dry); err != nil {
		t.Fatalf("not JSON: %q", stdout)
	}
	if !dry.SyncLeftovers || !dry.DryRun || dry.Plan.TotalRows != 6 {
		t.Fatalf("dry json: %+v", dry)
	}

	stdout, _, _ = runPurgeExpectExit(t, cfg, "--sync-leftovers", "--yes", "--json")
	var done struct {
		DryRun bool `json:"dry_run"`
		Result struct {
			BackupPath string `json:"backup_path"`
			Deleted    []struct {
				Table string `json:"table"`
				Rows  int    `json:"rows"`
			} `json:"deleted"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &done); err != nil {
		t.Fatalf("not JSON: %q", stdout)
	}
	if done.DryRun || done.Result.BackupPath == "" || len(done.Result.Deleted) != 5 {
		t.Fatalf("exec json: %+v", done)
	}
}

func TestCmdPurgeSyncLeftoversArgumentErrors(t *testing.T) {
	cfg := testConfig(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--sync-leftovers", "--project", "lab"}, "takes no selector"},
		{[]string{"--sync-leftovers", "--include-pinned"}, "takes no selector"},
		{[]string{"--vacuum", "--project", "lab"}, "--vacuum only applies to --sync-leftovers"},
	} {
		_, stderr, code := runPurgeExpectExit(t, cfg, c.args...)
		if code != 1 || !strings.Contains(stderr, c.want) {
			t.Fatalf("args %v: code=%d stderr=%q", c.args, code, stderr)
		}
	}
	stdout, _, code := runPurgeExpectExit(t, cfg, "--sync-leftovers", "--vacuum", "--json", "--project", "lab")
	if code != 1 || !strings.Contains(stdout, `"error"`) {
		t.Fatalf("json arg error: code=%d %q", code, stdout)
	}
}

func TestCmdPurgeSyncLeftoversVacuum(t *testing.T) {
	cfg := testConfig(t)
	seedSyncLeftoversCLI(t, cfg)
	stdout, stderr, code := runPurgeExpectExit(t, cfg, "--sync-leftovers", "--yes", "--vacuum")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "(vacuumed)") {
		t.Fatalf("code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
}

func TestPurgeHelpMentionsSyncLeftovers(t *testing.T) {
	_, stderr := captureOutput(t, func() { printPurgeUsage() })
	for _, want := range []string{"--sync-leftovers", "--vacuum", "stop engram serve and engram mcp"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("usage missing %q", want)
		}
	}
}

// engram sync (local .engram/ chunks) must keep working after the cleanup:
// the local sync_state row and sync_chunks survive, so a chunk exported before
// is not imported twice and a new one still is.
func TestSyncStillWorksAfterSyncLeftoversCleanup(t *testing.T) {
	withCwd(t, t.TempDir())
	exportCfg := testConfig(t)
	importCfg := testConfig(t)

	mustSeedObservation(t, exportCfg, "k-1", "keep", "note", "one", "content-one", "project")
	withArgs(t, "engram", "sync", "--all")
	if _, stderr, rec := captureOutputAndRecover(t, func() { cmdSync(exportCfg) }); rec != nil || stderr != "" {
		t.Fatalf("export: %v %q", rec, stderr)
	}
	withArgs(t, "engram", "sync", "--import")
	stdout, stderr, rec := captureOutputAndRecover(t, func() { cmdSync(importCfg) })
	if rec != nil || stderr != "" || !strings.Contains(stdout, "Imported 1 new chunk") {
		t.Fatalf("first import: %v %q %q", rec, stderr, stdout)
	}

	seedSyncLeftoversCLI(t, importCfg)
	if _, stderr, code := runPurgeExpectExit(t, importCfg, "--sync-leftovers", "--yes"); code != 0 || stderr != "" {
		t.Fatalf("cleanup: code=%d %q", code, stderr)
	}

	// Re-import: nothing new, the known chunk is still known.
	withArgs(t, "engram", "sync", "--import")
	stdout, _, rec = captureOutputAndRecover(t, func() { cmdSync(importCfg) })
	if rec != nil || !strings.Contains(stdout, "No new chunks to import") || !strings.Contains(stdout, "1 chunks already imported") {
		t.Fatalf("re-import after cleanup: %v %q", rec, stdout)
	}

	time.Sleep(1100 * time.Millisecond)
	mustSeedObservation(t, exportCfg, "k-2", "keep", "note", "two", "content-two", "project")
	withArgs(t, "engram", "sync", "--all")
	if _, _, rec := captureOutputAndRecover(t, func() { cmdSync(exportCfg) }); rec != nil {
		t.Fatalf("second export: %v", rec)
	}
	withArgs(t, "engram", "sync", "--import")
	stdout, stderr, rec = captureOutputAndRecover(t, func() { cmdSync(importCfg) })
	if rec != nil || stderr != "" || !strings.Contains(stdout, "Imported 1 new chunk") || !strings.Contains(stdout, "Observations: 1") {
		t.Fatalf("import of a new chunk after cleanup: %v %q %q", rec, stderr, stdout)
	}
}
