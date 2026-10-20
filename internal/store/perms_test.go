package store

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func skipNoPerms(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestNewCreatesPrivateDataDirAndDB(t *testing.T) {
	skipNoPerms(t)
	dir := filepath.Join(t.TempDir(), "data")
	s, err := New(FallbackConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateSession("s1", "p", "/tmp"); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, dir); m != 0o700 {
		t.Fatalf("data dir %o, want 700", m)
	}
	for _, name := range []string{"engram.db", "engram.db-wal", "engram.db-shm"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			continue // not every file exists on every platform build
		}
		if m := mode(t, filepath.Join(dir, name)); m != 0o600 {
			t.Fatalf("%s %o, want 600", name, m)
		}
	}
}

func TestNewTightensExistingDBFilesButNotACustomDir(t *testing.T) {
	skipNoPerms(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := New(FallbackConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db := filepath.Join(dir, "engram.db")
	if err := os.Chmod(db, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err = New(FallbackConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if m := mode(t, db); m != 0o600 {
		t.Fatalf("existing db %o, want 600", m)
	}
	if m := mode(t, dir); m != 0o755 {
		t.Fatalf("a custom data dir the user chose was changed to %o", m)
	}
}

func TestDefaultDataDirIsTightened(t *testing.T) {
	skipNoPerms(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".engram")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := New(FallbackConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if m := mode(t, dir); m != 0o700 {
		t.Fatalf("default data dir %o, want 700", m)
	}
}

func TestChmodFailureWarnsOnceAndDoesNotFail(t *testing.T) {
	var buf bytes.Buffer
	old := permWarnOut
	permWarnOut = &buf
	permWarned.Store(false)
	t.Cleanup(func() { permWarnOut = old; permWarned.Store(false) })

	warnPerm("first %s", "a")
	warnPerm("second %s", "b")
	if got := buf.String(); strings.Count(got, "[engram] warning:") != 1 || !strings.Contains(got, "first a") {
		t.Fatalf("warning not emitted exactly once: %q", got)
	}

	// A missing file is skipped silently, a chmod that fails only warns.
	buf.Reset()
	permWarned.Store(false)
	tightenDBFiles(filepath.Join(t.TempDir(), "missing.db"))
	if buf.Len() != 0 {
		t.Fatalf("unexpected warning: %q", buf.String())
	}
}

func TestBackupDirIsCreatedPrivateInOneStep(t *testing.T) {
	skipNoPerms(t)
	dir := t.TempDir()
	root, err := openBackupRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	if m := mode(t, filepath.Join(dir, "backups")); m != 0o700 {
		t.Fatalf("backups dir %o, want 700", m)
	}
}

// A symlinked backups is refused: the backup must never go through it.
func TestSymlinkedBackupsDirIsRefused(t *testing.T) {
	skipNoPerms(t)
	other := t.TempDir()
	if err := os.Chmod(other, 0o755); err != nil {
		t.Fatal(err)
	}
	s := newTestStore(t)
	if err := os.Symlink(other, filepath.Join(s.cfg.DataDir, "backups")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BackupSQLite(); err == nil {
		t.Fatal("a backup through a symlinked backups dir must be refused")
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Fatalf("something was written through the symlink: %v", entries)
	}
	if m := mode(t, other); m != 0o755 {
		t.Fatalf("symlink target was chmodded to %o", m)
	}
}

func TestTightenDBFilesNeverAddsOwnerBits(t *testing.T) {
	skipNoPerms(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "engram.db")
	for name, c := range map[string]struct{ from, want os.FileMode }{
		"engram.db":     {0o400, 0o400},
		"engram.db-wal": {0o644, 0o600},
		"engram.db-shm": {0o640, 0o600},
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, c.from); err != nil {
			t.Fatal(err)
		}
	}
	tightenDBFiles(db)
	if m := mode(t, db); m != 0o400 {
		t.Fatalf("0400 became %o", m)
	}
	if m := mode(t, db+"-wal"); m != 0o600 {
		t.Fatalf("0644 became %o", m)
	}
	if m := mode(t, db+"-shm"); m != 0o600 {
		t.Fatalf("0640 became %o", m)
	}
}

func TestSymlinkedDBIsWarnedAboutNotChanged(t *testing.T) {
	skipNoPerms(t)
	var buf bytes.Buffer
	old := permWarnOut
	permWarnOut = &buf
	permWarned.Store(false)
	t.Cleanup(func() { permWarnOut = old; permWarned.Store(false) })

	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "real.db")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "engram.db")); err != nil {
		t.Fatal(err)
	}
	tightenDBFiles(filepath.Join(dir, "engram.db"))
	if m := mode(t, target); m != 0o644 {
		t.Fatalf("the symlink target was changed to %o", m)
	}
	if !strings.Contains(buf.String(), "symlink") || !strings.Contains(buf.String(), "644") {
		t.Fatalf("no warning about the symlinked database: %q", buf.String())
	}
}

// A backups directory swapped for a symlink between the check and VACUUM INTO
// must not receive the backup: on Linux the write goes through the descriptor
// of the directory that was opened, elsewhere the result is verified and the
// backup is refused. Either way nothing lands in the symlink's target.
func TestBackupIntoSurvivesABackupsDirSwappedForASymlink(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pinning the directory needs /proc/self/fd (Linux)")
	}
	s := newTestStore(t)
	dataDir := s.cfg.DataDir
	elsewhere := t.TempDir()
	orig := s.hooks.exec
	swapped := false
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "VACUUM INTO") && !swapped {
			swapped = true
			if err := os.Rename(filepath.Join(dataDir, "backups"), filepath.Join(dataDir, "backups.real")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, filepath.Join(dataDir, "backups")); err != nil {
				t.Fatal(err)
			}
		}
		return orig(db, query, args...)
	}
	path, err := s.backupInto("engram-test-")
	if es, _ := os.ReadDir(elsewhere); len(es) != 0 {
		t.Fatalf("the backup was steered into the symlink target: %v (err=%v)", es, err)
	}
	if err != nil {
		t.Fatalf("the backup lands in the real directory: %v", err)
	}
	real := filepath.Join(dataDir, "backups.real", filepath.Base(path))
	fi, serr := os.Stat(real)
	if serr != nil || fi.Mode().Perm() != 0o600 || fi.Size() == 0 {
		t.Fatalf("backup in the real directory: %v %v", fi, serr)
	}
	if es, _ := filepath.Glob(filepath.Join(dataDir, "backups.real", ".*.tmp")); len(es) != 0 {
		t.Fatalf("temp file left behind: %v", es)
	}
}
