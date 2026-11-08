package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// Every physical connection must carry the pragmas, including the replacement
// the pool opens after discarding one.
func TestEveryNewConnectionHasPragmas(t *testing.T) {
	s := newTestStore(t)
	s.db.SetMaxIdleConns(0) // each statement now runs on a brand new connection
	for i := 0; i < 3; i++ {
		var timeout, fk int
		var mode string
		if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if timeout != 5000 || fk != 1 || mode != "wal" {
			t.Fatalf("connection %d: busy_timeout=%d foreign_keys=%d journal_mode=%s", i, timeout, fk, mode)
		}
	}
}

// Begin() takes the write lock up front (BEGIN IMMEDIATE), so a second process
// queues on busy_timeout instead of failing later with SQLITE_BUSY_SNAPSHOT.
func TestBeginTakesWriteLockImmediately(t *testing.T) {
	s := newTestStore(t)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	other, err := sql.Open("sqlite", "file:"+filepath.Join(s.cfg.DataDir, "engram.db")+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	_, err = other.Exec(`INSERT INTO sessions (id, project, directory) VALUES ('x', 'p', '/d')`)
	if err == nil || !isRetryableSQLiteLockError(err) {
		t.Fatalf("expected a lock error while the transaction is open, got %v", err)
	}
}
