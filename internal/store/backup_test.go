package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestBackupSQLiteIsPrivate(t *testing.T) {
	s := newTestStore(t)
	path, err := s.BackupSQLite()
	if err != nil {
		t.Fatalf("BackupSQLite: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(path), "engram-repair-") {
		t.Fatalf("unexpected backup name %s", path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup file mode = %o, want 600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("backup dir mode = %o, want 700", di.Mode().Perm())
	}
}

func TestBackupSQLiteLeavesExistingDirAlone(t *testing.T) {
	s := newTestStore(t)
	dir := filepath.Join(s.cfg.DataDir, "backups")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BackupSQLite(); err != nil {
		t.Fatalf("BackupSQLite: %v", err)
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o755 {
		t.Fatalf("existing dir mode changed to %o", di.Mode().Perm())
	}
}

func TestBackupNameAndCollisionSuffix(t *testing.T) {
	s := newTestStore(t)
	backupNow = func() time.Time { return time.Date(2026, 10, 19, 15, 4, 5, 123456789, time.UTC) }
	t.Cleanup(func() { backupNow = time.Now })

	var names []string
	for i := 0; i < 3; i++ {
		p, err := s.purgeBackup()
		if err != nil {
			t.Fatalf("purgeBackup #%d: %v", i, err)
		}
		names = append(names, filepath.Base(p))
	}
	want := []string{
		"engram-purge-20261019T150405Z.db",
		"engram-purge-20261019T150405Z-2.db",
		"engram-purge-20261019T150405Z-3.db",
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}

func TestBackupNameRealClockMatchesPattern(t *testing.T) {
	s := newTestStore(t)
	p, err := s.purgeBackup()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^engram-purge-\d{8}T\d{6}Z(-\d+)?\.db$`).MatchString(filepath.Base(p)) {
		t.Fatalf("unexpected name %s", p)
	}
}

func TestValidatePurgeBounds(t *testing.T) {
	cases := []struct {
		since, until string
		wantErr      string
		refused      bool
	}{
		{"", "", "", false},
		{"2026-01-01", "", "", false},
		{"2026-01-02", "2026-01-02", "", false}, // same day: date-only until covers the day
		{"2026-01-02T12:00:00Z", "2026-01-02", "", false},
		{"2026-01-03", "2026-01-02", "purge refused: --since is after --until", true},
		{"2026-01-02T12:00:01Z", "2026-01-02T12:00:00Z", "purge refused: --since is after --until", true},
		{"nope", "", "--since: invalid date", false},
		{"", "2026-13-40", "--until: invalid date", false},
	}
	for _, c := range cases {
		err := ValidatePurgeBounds(c.since, c.until)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("(%q,%q): unexpected error %v", c.since, c.until, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("(%q,%q): error %v, want %q", c.since, c.until, err, c.wantErr)
			continue
		}
		if c.refused && !strings.HasPrefix(err.Error(), "purge refused: --since is after --until") {
			t.Errorf("(%q,%q): refusal text %q", c.since, c.until, err)
		}
	}
}

func TestPurgeRefusesSinceAfterUntil(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	_, err := s.PurgePlan(PurgeSelector{Project: "lab", Since: "2030-01-02", Until: "2030-01-01"}, PurgeOptions{})
	if err == nil || err.Error() != "purge refused: --since is after --until" {
		t.Fatalf("got %v", err)
	}
}

func TestPurgeWarnsWhenDeletedDiffersFromPlan(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	orig := s.hooks.exec
	added := false
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		res, err := orig(db, query, args...)
		if err == nil && !added && strings.HasPrefix(query, "VACUUM INTO") {
			added = true
			// A write that lands after the plan and the backup, before the delete.
			s.hooks.exec = orig
			if _, aerr := s.AddObservation(AddObservationParams{SessionID: "s-lab", Type: "decision", Title: "late", Content: "late arrival", Project: "lab", Scope: "project"}); aerr != nil {
				t.Errorf("late add: %v", aerr)
			}
		}
		return res, err
	}
	res, err := s.Purge(PurgeSelector{Project: "lab"}, PurgeOptions{})
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "differ from the plan shown") && strings.Contains(w, "observations: planned 3, deleted 4") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing count-difference warning: %v", res.Warnings)
	}
}

func TestPurgeNoWarningWhenCountsMatch(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	res, err := s.Purge(PurgeSelector{Project: "lab"}, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "differ from the plan shown") {
			t.Fatalf("unexpected warning %q", w)
		}
	}
}
