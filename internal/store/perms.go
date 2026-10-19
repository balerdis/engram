package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
)

// The data directory and the database files hold memories in clear text, and
// ~/.engram may live on a multi-user machine: the directory is 0700 and
// engram.db (plus -wal and -shm) is 0600.
//
//   - A directory engram creates is 0700.
//   - An existing DEFAULT data directory (~/.engram) is tightened to 0700.
//   - An existing directory that is not the default is left alone, even when
//     it is group- or world-accessible: with ENGRAM_DATA_DIR the user picked
//     it (it may be a shared directory on purpose). The 0600 on the database
//     files still applies, and that is what protects the data.
//   - Existing database files are tightened to 0600 on every open.
//   - A failing chmod never fails startup: one warning on stderr per process.

var (
	// permWarnOut is where the one permission warning goes (stderr: stdout is
	// the MCP protocol channel). Tests replace it.
	permWarnOut io.Writer = os.Stderr
	permWarned  atomic.Bool
)

func warnPerm(format string, args ...any) {
	if !permWarned.CompareAndSwap(false, true) {
		return
	}
	fmt.Fprintf(permWarnOut, "[engram] warning: "+format+" (further permission warnings are suppressed)\n", args...)
}

// isDefaultDataDir reports whether dir is ~/.engram of the current user.
func isDefaultDataDir(dir string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return filepath.Clean(dir) == filepath.Join(home, ".engram")
}

// prepareDataDir creates dir with 0700 when missing and tightens an existing
// default one. Only a failure to create the directory is an error.
func prepareDataDir(dir string) error {
	existed := false
	if _, err := os.Stat(dir); err == nil {
		existed = true
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if !existed || !isDefaultDataDir(dir) {
		return nil
	}
	if st, err := os.Stat(dir); err == nil && st.IsDir() && st.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			warnPerm("cannot restrict %s to 0700: %v", dir, err)
		}
	}
	return nil
}

// precreateDB creates an empty engram.db with 0600 when it does not exist, so
// SQLite never creates it with the umask default (-wal and -shm copy the
// mode of the main file). An empty file is a valid new SQLite database.
func precreateDB(path string) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		f.Close()
		return
	}
	if !errors.Is(err, os.ErrExist) {
		warnPerm("cannot create %s with mode 0600: %v", path, err)
	}
}

// tightenDBFiles clears the group and other bits of engram.db, engram.db-wal
// and engram.db-shm (the ones that exist). It never adds owner bits: a 0400 file
// stays 0400. A symlinked file is never chmodded (that would change its
// target's mode): when the target is group- or other-accessible, one warning
// says so.
func tightenDBFiles(dbPath string) {
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		st, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if st.Mode()&os.ModeSymlink != 0 {
			if ts, err := os.Stat(p); err == nil && ts.Mode().Perm()&0o077 != 0 {
				warnPerm("%s is a symlink to a file that is group- or other-accessible (mode %o): engram does not change it, restrict it yourself", p, ts.Mode().Perm())
			}
			continue
		}
		if !st.Mode().IsRegular() || st.Mode().Perm()&0o077 == 0 {
			continue
		}
		if err := os.Chmod(p, st.Mode().Perm()&^0o077); err != nil {
			warnPerm("cannot restrict %s to owner-only: %v", p, err)
		}
	}
}
