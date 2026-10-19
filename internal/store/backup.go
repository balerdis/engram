package store

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// backupNow is the clock of backup file names; tests pin it to force a collision.
var backupNow = time.Now

// ensureBackupDir creates <dataDir>/backups with its final 0700 mode in one
// step (Mkdir: there is no window in which it exists with looser permissions),
// and tightens an existing one that an older version or a manual mkdir left
// group/other-accessible. Both go through os.Root, so neither can be steered
// out of the data directory by a swapped path or symlink.
func ensureBackupDir(dataDir string) error {
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}
	defer root.Close()
	err = root.Mkdir("backups", 0o700)
	if err == nil {
		return nil
	}
	if !os.IsExist(err) {
		return fmt.Errorf("create backup dir: %w", err)
	}
	if st, err := root.Lstat("backups"); err == nil && st.IsDir() && st.Mode().Perm()&0o077 != 0 {
		// Best effort: a foreign owner cannot chmod.
		_ = root.Chmod("backups", 0o700)
	}
	return nil
}

// backupInto writes a verified-by-the-caller copy of the database to
// <data dir>/backups/<prefix><YYYYMMDDTHHMMSSZ>.db using VACUUM INTO, which is
// safe while other engram processes use the WAL database (copying the file is
// not). A name already taken gets a -2, -3 ... suffix. The directory is
// created 0700 when missing and an existing one with group/other permissions
// is tightened to 0700; the file is 0600: a backup holds memories in clear text and the data dir may live on a
// multi-user machine. On failure the half-written file is removed.
func (s *Store) backupInto(prefix string) (string, error) {
	backupDir := filepath.Join(s.cfg.DataDir, "backups")
	if err := ensureBackupDir(s.cfg.DataDir); err != nil {
		return "", err
	}
	stamp := backupNow().UTC().Format("20060102T150405Z")
	var path string
	for n := 1; ; n++ {
		name := prefix + stamp
		if n > 1 {
			name = fmt.Sprintf("%s-%d", name, n)
		}
		path = filepath.Join(backupDir, name+".db")
		// VACUUM INTO accepts an existing empty file, so pre-create it with 0600.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			f.Close()
			break
		}
		if !os.IsExist(err) || n >= 1000 {
			return "", fmt.Errorf("create backup file: %w", err)
		}
	}
	if _, err := s.execHook(s.db, `VACUUM INTO ?`, path); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("backup sqlite database: %w", err)
	}
	return path, nil
}
