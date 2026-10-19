package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedSyncLeftovers writes the rows an old cloud-sync database carries, with
// plain SQL because no code path writes them any more.
func seedSyncLeftovers(t *testing.T, s *Store, mutations int) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT OR IGNORE INTO sync_state (target_key, lifecycle, last_pulled_seq) VALUES ('local', 'healthy', 7)`)
	exec(`UPDATE sync_state SET last_pulled_seq = 7 WHERE target_key = 'local'`)
	exec(`INSERT INTO sync_state (target_key, lifecycle) VALUES ('cloud:lab', 'degraded'), ('cloud:other', 'idle')`)
	for i := 0; i < mutations; i++ {
		exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, project) VALUES ('cloud', 'observation', ?, 'upsert', ?, 'lab')`,
			"obs-"+strings.Repeat("x", i%5), strings.Repeat("p", 100))
	}
	exec(`INSERT INTO sync_apply_deferred (sync_id, entity, payload) VALUES ('d1', 'relation', '{}'), ('d2', 'relation', '{}')`)
	exec(`INSERT INTO sync_enrolled_projects (project) VALUES ('lab')`)
	exec(`INSERT INTO cloud_upgrade_state (project) VALUES ('lab')`)
	exec(`INSERT INTO sync_chunks (target_key, chunk_id) VALUES ('local', 'abc123')`)
}

func rowsOf(plan *SyncLeftoversPlan, table string) int {
	for _, t := range plan.Tables {
		if t.Table == table {
			return t.Rows
		}
	}
	return -1
}

func TestSyncLeftoversPlanCountsAndWritesNothing(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	seedSyncLeftovers(t, s, 10)
	before := purgeTables(t, s)

	plan, err := s.SyncLeftoversPlan()
	if err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{
		"sync_mutations": 10, "sync_apply_deferred": 2, "sync_enrolled_projects": 1,
		"cloud_upgrade_state": 1, "sync_state": 2, // 'local' and 'cloud' are kept
	} {
		if got := rowsOf(plan, table); got != want {
			t.Errorf("%s: %d rows, want %d", table, got, want)
		}
	}
	if plan.TotalRows != 16 || plan.ApproxBytes < 1000 {
		t.Errorf("total=%d bytes=%d", plan.TotalRows, plan.ApproxBytes)
	}
	for _, tb := range plan.Tables {
		if tb.Rows > 0 && tb.ApproxBytes <= 0 {
			t.Errorf("%s has rows but no bytes", tb.Table)
		}
	}
	after := purgeTables(t, s)
	for k, v := range before {
		if after[k] != v {
			t.Errorf("dry run changed %s: %d -> %d", k, v, after[k])
		}
	}
	if _, err := os.Stat(filepath.Join(s.cfg.DataDir, "backups")); err == nil {
		t.Error("a dry run created a backup dir")
	}
}

func TestPurgeSyncLeftoversDeletesOnlyLeftovers(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	seedSyncLeftovers(t, s, 10)
	before := purgeTables(t, s)

	res, err := s.PurgeSyncLeftovers(SyncLeftoversOptions{})
	if err != nil {
		t.Fatalf("PurgeSyncLeftovers: %v", err)
	}
	after := purgeTables(t, s)
	for _, table := range []string{"sync_mutations", "sync_apply_deferred", "sync_enrolled_projects", "cloud_upgrade_state"} {
		if after[table] != 0 {
			t.Errorf("%s still has %d rows", table, after[table])
		}
	}
	if after["sync_state"] != 2 || purgeCount(t, s, `SELECT last_pulled_seq FROM sync_state WHERE target_key='local'`) != 7 {
		t.Errorf("the local and cloud sync_state rows did not survive intact: %d rows", after["sync_state"])
	}
	for _, table := range []string{"sync_chunks", "sessions", "observations", "user_prompts", "memory_relations"} {
		if after[table] != before[table] {
			t.Errorf("%s changed: %d -> %d", table, before[table], after[table])
		}
	}
	if res.BackupPath == "" || len(res.Warnings) != 0 {
		t.Errorf("backup=%q warnings=%v", res.BackupPath, res.Warnings)
	}
	fi, err := os.Stat(res.BackupPath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("backup file: %v %v", fi, err)
	}
	if got := res.Deleted[0]; got.Table != "sync_mutations" || got.Rows != 10 {
		t.Errorf("deleted[0] = %+v", got)
	}
	// The backup still holds the data.
	if err := verifyPurgeBackup(res.BackupPath, res.Plan.counts()); err != nil {
		t.Errorf("backup does not hold the deleted rows: %v", err)
	}
}

func TestPurgeSyncLeftoversBackupFailureDeletesNothing(t *testing.T) {
	s := newTestStore(t)
	seedSyncLeftovers(t, s, 3)
	if err := os.WriteFile(filepath.Join(s.cfg.DataDir, "backups"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeSyncLeftovers(SyncLeftoversOptions{}); err == nil || !strings.Contains(err.Error(), "nothing deleted") {
		t.Fatalf("expected an abort, got %v", err)
	}
	if n := purgeCount(t, s, `SELECT COUNT(*) FROM sync_mutations`); n != 3 {
		t.Fatalf("sync_mutations changed: %d", n)
	}
}

func TestPurgeSyncLeftoversEmptyIsNoop(t *testing.T) {
	s := newTestStore(t)
	res, err := s.PurgeSyncLeftovers(SyncLeftoversOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Plan.Empty() || res.BackupPath != "" {
		t.Fatalf("empty cleanup made a backup: %+v", res)
	}
}

func TestPurgeSyncLeftoversVacuumShrinksFile(t *testing.T) {
	s := newTestStore(t)
	seedSyncLeftovers(t, s, 3000)
	if _, err := s.db.Exec(`UPDATE sync_mutations SET payload = ?`, strings.Repeat("z", 4000)); err != nil {
		t.Fatal(err)
	}
	res, err := s.PurgeSyncLeftovers(SyncLeftoversOptions{Vacuum: true})
	if err != nil {
		t.Fatalf("PurgeSyncLeftovers: %v", err)
	}
	if !res.Vacuumed || res.DBBytesAfter >= res.DBBytesBefore/2 {
		t.Fatalf("vacuumed=%v before=%d after=%d warnings=%v", res.Vacuumed, res.DBBytesBefore, res.DBBytesAfter, res.Warnings)
	}
	if purgeCount(t, s, `SELECT COUNT(*) FROM sync_state WHERE target_key='local'`) != 1 {
		t.Fatal("local sync_state row lost")
	}
}

func TestPurgeSyncLeftoversVacuumRefusedWhenAnotherProcessHoldsTheDB(t *testing.T) {
	s := newTestStore(t)
	seedSyncLeftovers(t, s, 5)
	// A second engram process: another connection with an open read transaction.
	other, err := New(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := other.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	// Add WAL content so the TRUNCATE checkpoint cannot complete past the reader.
	if _, err := s.db.Exec(`INSERT INTO sync_enrolled_projects (project) VALUES ('later')`); err != nil {
		t.Fatal(err)
	}

	_, err = s.PurgeSyncLeftovers(SyncLeftoversOptions{Vacuum: true})
	if err == nil || !strings.Contains(err.Error(), "--vacuum") || !strings.Contains(err.Error(), "stop the other engram processes") {
		t.Fatalf("expected a clear refusal, got %v", err)
	}
	if got := purgeCount(t, s, `SELECT COUNT(*) FROM sync_mutations`); got != 5 {
		t.Fatalf("a refused --vacuum deleted rows: %d left", got)
	}
	if entries, _ := filepath.Glob(filepath.Join(s.cfg.DataDir, "backups", "*")); len(entries) != 0 {
		t.Fatalf("a refused --vacuum left a backup: %v", entries)
	}
}
