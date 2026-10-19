package store

import (
	"bytes"
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
	if err := ensureBackupDir(dir); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, filepath.Join(dir, "backups")); m != 0o700 {
		t.Fatalf("backups dir %o, want 700", m)
	}
	// A symlink named backups is never followed by the tightening.
	other := t.TempDir()
	if err := os.Chmod(other, 0o755); err != nil {
		t.Fatal(err)
	}
	dir2 := t.TempDir()
	if err := os.Symlink(other, filepath.Join(dir2, "backups")); err != nil {
		t.Fatal(err)
	}
	_ = ensureBackupDir(dir2)
	if m := mode(t, other); m != 0o755 {
		t.Fatalf("symlink target was chmodded to %o", m)
	}
}
