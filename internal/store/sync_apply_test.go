package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// buildRelationMutation builds a SyncMutation for entity='relation' from a
// syncRelationPayload.
func buildRelationMutation(t *testing.T, p syncRelationPayload) SyncMutation {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("buildRelationMutation: marshal: %v", err)
	}
	return SyncMutation{
		Entity:    SyncEntityRelation,
		EntityKey: p.SyncID,
		Op:        SyncOpUpsert,
		Payload:   string(raw),
		Source:    SyncSourceRemote,
		Project:   p.Project,
	}
}

// applyRelationMutation calls applyPulledMutationTx inside a transaction.
func applyRelationMutation(t *testing.T, s *Store, m SyncMutation) error {
	t.Helper()
	return s.withTx(func(tx *sql.Tx) error {
		return s.applyPulledMutationTx(tx, m)
	})
}

// countRelationRows returns the count of rows in memory_relations with the
// given sync_id.
func countRelationRows(t *testing.T, s *Store, syncID string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM memory_relations WHERE sync_id = ?`, syncID,
	).Scan(&n); err != nil {
		t.Fatalf("countRelationRows: %v", err)
	}
	return n
}

// countDeferredRows returns the count of rows in sync_apply_deferred for the
// given sync_id.
func countDeferredRows(t *testing.T, s *Store, syncID string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM sync_apply_deferred WHERE sync_id = ?`, syncID,
	).Scan(&n); err != nil {
		t.Fatalf("countDeferredRows: %v", err)
	}
	return n
}

// setupSyncApplyStore creates a fresh store with two sessions and two
// observations suitable for relation apply tests.
func setupSyncApplyStore(t *testing.T) (s *Store, syncObsA, syncObsB string) {
	t.Helper()
	s = newTestStore(t)
	if err := s.CreateSession("ses-apply-test", "proj-apply", "/tmp/apply"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, syncObsA = addTestObsSession(t, s, "ses-apply-test", "Obs A for apply tests", "decision", "proj-apply", "project")
	_, syncObsB = addTestObsSession(t, s, "ses-apply-test", "Obs B for apply tests", "decision", "proj-apply", "project")
	return
}

// ─── Phase C.3 — Pull-side RED tests (REQ-002, REQ-009) ──────────────────────

// C.3a — ApplyPulledRelation_InsertsWhenObsExist: both source and target
// observations exist locally → relation is upserted into memory_relations.
func TestApplyPulledRelation_InsertsWhenObsExist(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	relSyncID := newSyncID("rel")
	m := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       syncB,
		Relation:       RelationConflictsWith,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})

	if err := applyRelationMutation(t, s, m); err != nil {
		t.Fatalf("applyPulledMutationTx: %v", err)
	}

	n := countRelationRows(t, s, relSyncID)
	if n != 1 {
		t.Errorf("expected 1 row in memory_relations for sync_id=%q; got %d", relSyncID, n)
	}

	// Verify the deferred table is empty for this sync_id.
	d := countDeferredRows(t, s, relSyncID)
	if d != 0 {
		t.Errorf("expected 0 deferred rows after successful apply; got %d", d)
	}
}

// C.3b — ApplyPulledRelation_DefersOnFKMiss: target observation absent →
// row written to sync_apply_deferred; no halt; seq is ACK-able.
func TestApplyPulledRelation_DefersOnFKMiss(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)

	// Target does NOT exist locally.
	missingTarget := "obs-ghost-" + newSyncID("x")

	relSyncID := newSyncID("rel")
	m := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       missingTarget,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})

	// applyPulledMutationTx must return ErrRelationFKMissing.
	err := applyRelationMutation(t, s, m)
	if err == nil {
		t.Fatal("expected ErrRelationFKMissing when target is absent; got nil")
	}
	if !errors.Is(err, ErrRelationFKMissing) {
		t.Errorf("expected ErrRelationFKMissing; got %v", err)
	}

	// Caller writes to sync_apply_deferred on ErrRelationFKMissing.
	// The test simulates the caller: write deferred row and verify.
	if _, werr := s.db.Exec(`
		INSERT INTO sync_apply_deferred (sync_id, entity, payload, apply_status, retry_count, first_seen_at)
		VALUES (?, 'relation', ?, 'deferred', 0, datetime('now'))
		ON CONFLICT(sync_id) DO UPDATE SET payload=excluded.payload, last_attempted_at=datetime('now')
	`, relSyncID, m.Payload); werr != nil {
		t.Fatalf("write deferred row: %v", werr)
	}

	d := countDeferredRows(t, s, relSyncID)
	if d != 1 {
		t.Errorf("expected 1 deferred row; got %d", d)
	}

	// Verify apply_status and retry_count.
	var applyStatus string
	var retryCount int
	if err2 := s.db.QueryRow(
		`SELECT apply_status, retry_count FROM sync_apply_deferred WHERE sync_id = ?`, relSyncID,
	).Scan(&applyStatus, &retryCount); err2 != nil {
		t.Fatalf("scan deferred row: %v", err2)
	}
	if applyStatus != "deferred" {
		t.Errorf("apply_status: want %q, got %q", "deferred", applyStatus)
	}
	if retryCount != 0 {
		t.Errorf("retry_count: want 0, got %d", retryCount)
	}

	// memory_relations must NOT have a row for this sync_id.
	r := countRelationRows(t, s, relSyncID)
	if r != 0 {
		t.Errorf("expected 0 rows in memory_relations for deferred relation; got %d", r)
	}
}

// C.3c — ApplyPulledRelation_IdempotentOnSyncID: pulling the same relation
// twice yields exactly one row (REQ-009, INSERT OR REPLACE on sync_id).
func TestApplyPulledRelation_IdempotentOnSyncID(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	relSyncID := newSyncID("rel")
	p := syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       syncB,
		Relation:       RelationCompatible,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	}
	m := buildRelationMutation(t, p)

	// First apply.
	if err := applyRelationMutation(t, s, m); err != nil {
		t.Fatalf("first applyPulledMutationTx: %v", err)
	}

	// Second apply with same sync_id.
	if err := applyRelationMutation(t, s, m); err != nil {
		t.Fatalf("second applyPulledMutationTx: %v", err)
	}

	n := countRelationRows(t, s, relSyncID)
	if n != 1 {
		t.Errorf("expected exactly 1 row after two pulls with same sync_id; got %d", n)
	}
}

// ─── Phase E store-layer helpers (REQ-007) ────────────────────────────────────

// insertDeferredRow inserts a row directly into sync_apply_deferred for test setup.
func insertDeferredRow(t *testing.T, s *Store, syncID, entity, payload string, retryCount int, applyStatus string) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO sync_apply_deferred (sync_id, entity, payload, retry_count, apply_status, first_seen_at)
		VALUES (?, ?, ?, ?, ?, datetime('now'))
	`, syncID, entity, payload, retryCount, applyStatus); err != nil {
		t.Fatalf("insertDeferredRow: %v", err)
	}
}

// TestCountDeferredAndDead: 3 deferred + 1 dead → counts correct.
func TestCountDeferredAndDead(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)
	missingTarget := "obs-missing-count-" + newSyncID("x")
	makePayload := func(id string) string {
		p, _ := json.Marshal(syncRelationPayload{
			SyncID:         id,
			SourceID:       syncA,
			TargetID:       missingTarget,
			Relation:       RelationRelated,
			JudgmentStatus: JudgmentStatusJudged,
			Project:        "proj-apply",
			CreatedAt:      "2026-04-26T10:00:00Z",
			UpdatedAt:      "2026-04-26T10:00:00Z",
		})
		return string(p)
	}

	insertDeferredRow(t, s, newSyncID("r1"), SyncEntityRelation, makePayload("r1"), 0, "deferred")
	insertDeferredRow(t, s, newSyncID("r2"), SyncEntityRelation, makePayload("r2"), 1, "deferred")
	insertDeferredRow(t, s, newSyncID("r3"), SyncEntityRelation, makePayload("r3"), 2, "deferred")
	insertDeferredRow(t, s, newSyncID("r4"), SyncEntityRelation, makePayload("r4"), 5, "dead")

	deferred, dead, err := s.CountDeferredAndDead()
	if err != nil {
		t.Fatalf("CountDeferredAndDead: %v", err)
	}
	if deferred != 3 {
		t.Errorf("deferred: want 3, got %d", deferred)
	}
	if dead != 1 {
		t.Errorf("dead: want 1, got %d", dead)
	}
}

// C.3d — ApplyPulledRelation_MultiActorSamePair: two mutations, same
// (source, target) pair but different sync_id → two distinct rows (REQ-009).
func TestApplyPulledRelation_MultiActorSamePair(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	relSyncID1 := newSyncID("rel")
	relSyncID2 := newSyncID("rel")

	m1 := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID1,
		SourceID:       syncA,
		TargetID:       syncB,
		Relation:       RelationConflictsWith,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})
	m2 := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID2,
		SourceID:       syncA,
		TargetID:       syncB,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:05:00Z",
		UpdatedAt:      "2026-04-26T10:05:00Z",
	})

	if err := applyRelationMutation(t, s, m1); err != nil {
		t.Fatalf("apply actor-1 mutation: %v", err)
	}
	if err := applyRelationMutation(t, s, m2); err != nil {
		t.Fatalf("apply actor-2 mutation: %v", err)
	}

	n1 := countRelationRows(t, s, relSyncID1)
	n2 := countRelationRows(t, s, relSyncID2)
	if n1 != 1 {
		t.Errorf("actor-1 sync_id: expected 1 row, got %d", n1)
	}
	if n2 != 1 {
		t.Errorf("actor-2 sync_id: expected 1 row, got %d", n2)
	}
}

// applyPulled applies one mutation through the same code path that local chunk
// import uses, without recording a chunk.
func applyPulled(s *Store, m SyncMutation) error {
	return s.withTx(func(tx *sql.Tx) error { return s.applyPulledMutationTx(tx, m) })
}

func TestApplyPulledChunkSkipsRelationWithMissingEndpoint(t *testing.T) {
	s := newTestStore(t)
	mk := func(entity, key string, payload any) SyncMutation {
		raw, _ := json.Marshal(payload)
		return SyncMutation{Entity: entity, EntityKey: key, Op: SyncOpUpsert, Payload: string(raw), Source: SyncSourceRemote}
	}
	proj := "p"
	muts := []SyncMutation{
		mk(SyncEntitySession, "s1", syncSessionPayload{ID: "s1", Project: "p", Directory: "/x"}),
		mk(SyncEntityObservation, "obs-skip1", syncObservationPayload{SyncID: "obs-skip1", SessionID: "s1", Type: "note", Title: "t", Content: "c", Project: &proj, Scope: "project"}),
		mk(SyncEntityRelation, "rel-skip1", syncRelationPayload{SyncID: "rel-skip1", SourceID: "obs-skip1", TargetID: "obs-gone", Relation: RelationRelated, JudgmentStatus: JudgmentStatusPending}),
	}
	skipped, err := s.ApplyPulledChunkReport("local", "chunk-skip", muts)
	if err != nil {
		t.Fatalf("chunk must not fail: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	if o, err := s.GetObservationBySyncID("obs-skip1"); err != nil || o == nil {
		t.Fatalf("observation must be imported: %v", err)
	}
	if again, err := s.ApplyPulledChunkReport("local", "chunk-skip", muts); err != nil || again != 0 {
		t.Fatalf("re-apply must be a no-op, got skipped=%d err=%v", again, err)
	}
}
