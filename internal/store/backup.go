package store

import (
	"fmt"
	"os"
	"path/filepath"
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
	stamp := backupNow().UTC().Format("20060102T150405Z")
	var path string
	for n := 1; ; n++ {
		name := prefix + stamp
		if n > 1 {
			name = fmt.Sprintf("%s-%d", name, n)
		}
		name += ".db"
		path = filepath.Join(s.cfg.DataDir, "backups", name)
		// VACUUM INTO accepts an existing empty file, so pre-create it with 0600
		// (through the Root, never through a plain path).
		f, err := root.OpenFile("backups/"+name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			f.Close()
			break
		}
		if !os.IsExist(err) || n >= 1000 {
			return "", fmt.Errorf("create backup file: %w", err)
		}
	}
	if _, err := s.execHook(s.db, `VACUUM INTO ?`, path); err != nil {
		_ = root.Remove("backups/" + filepath.Base(path))
		return "", fmt.Errorf("backup sqlite database: %w", err)
	}
	return path, nil
}
