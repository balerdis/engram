package store

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// backupNow is the clock of backup file names; tests pin it to force a collision.
var backupNow = time.Now

// backupInto writes a verified-by-the-caller copy of the database to
// <data dir>/backups/<prefix><YYYYMMDDTHHMMSSZ>.db using VACUUM INTO, which is
// safe while other engram processes use the WAL database (copying the file is
// not). A name already taken gets a -2, -3 ... suffix. The directory is
// created 0700 only when missing (an existing one is left alone) and the file
// is 0600: a backup holds memories in clear text and the data dir may live on a
// multi-user machine. On failure the half-written file is removed.
func (s *Store) backupInto(prefix string) (string, error) {
	backupDir := filepath.Join(s.cfg.DataDir, "backups")
	if _, err := os.Stat(backupDir); os.IsNotExist(err) {
		if err := os.MkdirAll(backupDir, 0o700); err != nil {
			return "", fmt.Errorf("create backup dir: %w", err)
		}
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
