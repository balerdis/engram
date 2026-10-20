package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// backupNow is the clock of backup file names; tests pin it to force a collision.
var backupNow = time.Now

// openBackupRoot opens the data directory as an os.Root and makes sure
// <dataDir>/backups exists as a real directory (never a symlink) with mode
// 0700: it is created with its final mode in one step (Mkdir: no window with
// looser permissions), and an existing one that an older version or a manual
// mkdir left group/other-accessible is tightened. A symlinked backups is
// refused: the backup would go wherever it points. Everything, including the
// backup file itself, then goes through the returned Root, which cannot be
// steered out of the data directory by a swapped path or symlink.
func openBackupRoot(dataDir string) (*os.Root, error) {
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, fmt.Errorf("create backup dir: %w", err)
	}
	err = root.Mkdir("backups", 0o700)
	if err == nil {
		return root, nil
	}
	if !os.IsExist(err) {
		root.Close()
		return nil, fmt.Errorf("create backup dir: %w", err)
	}
	st, err := root.Lstat("backups")
	if err != nil || !st.IsDir() {
		root.Close()
		return nil, fmt.Errorf("create backup dir: %s/backups is a symlink or not a directory; refusing to write a backup through it", dataDir)
	}
	if st.Mode().Perm()&0o077 != 0 {
		// Best effort: a foreign owner cannot chmod.
		_ = root.Chmod("backups", 0o700)
	}
	return root, nil
}

// backupInto writes a verified-by-the-caller copy of the database to
// <data dir>/backups/<prefix><YYYYMMDDTHHMMSSZ>.db using VACUUM INTO, which is
// safe while other engram processes use the WAL database (copying the file is
// not). A name already taken gets a -2, -3 ... suffix. The directory is
// created 0700 when missing and an existing one with group/other permissions
// is tightened to 0700; the file is 0600: a backup holds memories in clear text and the data dir may live on a
// multi-user machine. On failure the half-written file is removed.
func (s *Store) backupInto(prefix string) (string, error) {
	root, err := openBackupRoot(s.cfg.DataDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	// From here on every name is relative to the backups directory as it was
	// when it was opened: a swapped path or symlink above it cannot redirect it.
	bk, err := root.OpenRoot("backups")
	if err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	defer bk.Close()

	stamp := backupNow().UTC().Format("20060102T150405Z")
	var name string
	for n := 1; ; n++ {
		name = prefix + stamp
		if n > 1 {
			name = fmt.Sprintf("%s-%d", name, n)
		}
		name += ".db"
		// The final name is reserved (O_EXCL, 0600) so two backups never share
		// it; the content arrives by rename once it is verified.
		f, err := bk.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			f.Close()
			break
		}
		if !os.IsExist(err) || n >= 1000 {
			return "", fmt.Errorf("create backup file: %w", err)
		}
	}
	final := filepath.Join(s.cfg.DataDir, "backups", name)
	fail := func(tmp string, err error) (string, error) {
		if tmp != "" {
			_ = bk.Remove(tmp)
		}
		_ = bk.Remove(name)
		return "", err
	}

	// VACUUM INTO accepts an existing empty file, so the copy is written to a
	// temporary name created (O_EXCL, 0600) through the Root.
	tmp := "." + name + ".tmp"
	f, err := bk.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail("", fmt.Errorf("create backup file: %w", err))
	}
	pre, _ := f.Stat()
	f.Close()

	// SQLite takes a path, not a Root. On Linux the path goes through the
	// open descriptor of the backups directory (/proc/self/fd), which names
	// that directory itself, whatever happens to its name afterwards. Elsewhere
	// the plain path is used and the result is verified below.
	target := filepath.Join(s.cfg.DataDir, "backups", tmp)
	if dir, err := bk.Open("."); err == nil {
		defer dir.Close()
		if p := fdPath(dir, tmp); p != "" {
			target = p
		}
	}
	if _, err := s.execHook(s.db, `VACUUM INTO ?`, target); err != nil {
		return fail(tmp, fmt.Errorf("backup sqlite database: %w", err))
	}
	if err := verifyBackupTemp(bk, tmp, pre); err != nil {
		return fail(tmp, fmt.Errorf("backup sqlite database: %w", err))
	}
	if err := bk.Rename(tmp, name); err != nil {
		return fail(tmp, fmt.Errorf("backup sqlite database: %w", err))
	}
	return final, nil
}

// fdPath returns a path to name inside the directory held by dir, or "" when
// the platform has no such thing.
func fdPath(dir *os.File, name string) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	p := fmt.Sprintf("/proc/self/fd/%d", dir.Fd())
	if st, err := os.Stat(p); err != nil || !st.IsDir() {
		return ""
	}
	return p + "/" + name
}

// verifyBackupTemp checks, through the Root, that the file VACUUM INTO wrote is
// the one that was created (not a replacement), is a regular file and holds an
// SQLite database. A write that went somewhere else leaves it empty.
func verifyBackupTemp(bk *os.Root, tmp string, pre os.FileInfo) error {
	st, err := bk.Lstat(tmp)
	if err != nil {
		return fmt.Errorf("backup file vanished: %w", err)
	}
	if !st.Mode().IsRegular() || (pre != nil && !os.SameFile(pre, st)) {
		return fmt.Errorf("backup file was replaced while it was written")
	}
	f, err := bk.Open(tmp)
	if err != nil {
		return fmt.Errorf("open backup file: %w", err)
	}
	defer f.Close()
	head := make([]byte, 16)
	if _, err := io.ReadFull(f, head); err != nil || string(head) != "SQLite format 3\x00" {
		return fmt.Errorf("backup file is empty or not a database (the write did not land in the backups directory)")
	}
	return nil
}
