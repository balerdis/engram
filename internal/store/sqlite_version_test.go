package store

import (
	"strconv"
	"strings"
	"testing"
)

// The WAL-reset corruption bug is fixed in SQLite 3.51.3. Several engram
// processes share one database in WAL mode, so the bundled driver must not
// regress below it.
func TestSQLiteVersionHasWALResetFix(t *testing.T) {
	s := newTestStore(t)
	var v string
	if err := s.db.QueryRow(`SELECT sqlite_version()`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(v, ".")
	if len(parts) < 3 {
		t.Fatalf("unexpected sqlite_version %q", v)
	}
	var n [3]int
	for i := range n {
		x, err := strconv.Atoi(parts[i])
		if err != nil {
			t.Fatalf("unexpected sqlite_version %q", v)
		}
		n[i] = x
	}
	if n[0] < 3 || (n[0] == 3 && (n[1] < 51 || (n[1] == 51 && n[2] < 3))) {
		t.Fatalf("SQLite %s is older than 3.51.3 (WAL-reset corruption fix)", v)
	}
}
