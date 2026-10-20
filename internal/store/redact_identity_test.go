package store

import "testing"

// identityHolds reports whether needle is in a free-text identity column of
// memory_relations (the only table with marked_by_* and a free-text session_id).
func identityHolds(t *testing.T, s *Store, needle string) bool {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM memory_relations WHERE
		instr(ifnull(marked_by_actor,''), ?1) OR instr(ifnull(marked_by_kind,''), ?1) OR
		instr(ifnull(marked_by_model,''), ?1) OR instr(ifnull(session_id,''), ?1)`, needle).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestRedactIdentityFieldsOnEveryWritePath(t *testing.T) {
	s, a, b := setupSyncApplyStore(t)

	// JudgeRelation: actor, kind, model and session.
	rel, err := s.SaveRelation(SaveRelationParams{SyncID: "rel-ident1", SourceID: a, TargetID: b})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.JudgeRelation(JudgeRelationParams{
		JudgmentID: rel.SyncID, Relation: "related",
		MarkedByActor: "actor-" + fakeKey, MarkedByKind: "kind-" + fakeKey, MarkedByModel: "model-" + fakeKey, SessionID: "sess-" + fakeKey,
	}); err != nil {
		t.Fatal(err)
	}
	if identityHolds(t, s, fakeKeyN) {
		t.Fatal("credential reached the identity columns through JudgeRelation")
	}
	got, err := s.GetRelation("rel-ident1")
	if err != nil || got.MarkedByActor == nil || *got.MarkedByActor == "" {
		t.Fatalf("actor lost: %+v %v", got, err)
	}

	// JudgeBySemantic: model (actor and kind are constants).
	s2, a2, b2 := setupSyncApplyStore(t)
	if _, err := s2.JudgeBySemantic(JudgeBySemanticParams{SourceID: a2, TargetID: b2, Relation: "related", Confidence: 0.9, Model: "m-" + fakeKey}); err != nil {
		t.Fatal(err)
	}
	if identityHolds(t, s2, fakeKeyN) {
		t.Fatal("credential reached marked_by_model through JudgeBySemantic")
	}

	// Pulled relation.
	s3, a3, b3 := setupSyncApplyStore(t)
	actor, kind, model, sess := "a-"+fakeKey, "k-"+fakeKey, "m-"+fakeKey, "s-"+fakeKey
	m := buildRelationMutation(t, syncRelationPayload{
		SyncID: "rel-pulled1", SourceID: a3, TargetID: b3, Relation: RelationRelated, JudgmentStatus: JudgmentStatusJudged,
		MarkedByActor: &actor, MarkedByKind: &kind, MarkedByModel: &model, SessionID: &sess, Project: "proj-apply",
	})
	if err := applyRelationMutation(t, s3, m); err != nil {
		t.Fatal(err)
	}
	if identityHolds(t, s3, fakeKeyN) {
		t.Fatal("credential reached the identity columns through a pulled relation")
	}
}

// The identity columns of rows written before this version are covered by the
// scan; the session keys of observations, prompts and sessions are not (they
// are foreign keys that must stay stable).
func TestRedactScanCoversRelationIdentityFields(t *testing.T) {
	s, a, b := setupSyncApplyStore(t)
	if _, err := s.db.Exec(`INSERT INTO memory_relations (sync_id, source_id, target_id, marked_by_actor, marked_by_kind, marked_by_model, session_id)
		VALUES ('rel-old', ?, ?, ?, ?, ?, ?)`, a, b, "a-"+fakeKey, "k-"+fakeKey, "m-"+fakeKey, "s-"+fakeKey); err != nil {
		t.Fatal(err)
	}
	res, err := s.RedactScan(false)
	if err != nil || scanChanged(res, "memory_relations") != 1 {
		t.Fatalf("dry-run: %+v %v", res, err)
	}
	if !identityHolds(t, s, fakeKeyN) {
		t.Fatal("dry-run changed the data")
	}
	if _, err := s.RedactScan(true); err != nil {
		t.Fatal(err)
	}
	if identityHolds(t, s, fakeKeyN) {
		t.Fatal("scan left a credential in the identity columns")
	}
	res, err = s.RedactScan(false)
	if err != nil || res.RowsChanged != 0 {
		t.Fatalf("second scan not idempotent: %+v %v", res, err)
	}
}

// Rows stored by an older catalog that the newer one now catches (a value in
// escaped quotes inside a JSON string, a padded <private> value) are rewritten
// by the scan, and a second scan finds nothing.
func TestRedactScanAppliesTheCurrentCatalog(t *testing.T) {
	s := newTestStore(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO sessions (id, project, directory) VALUES ('s1', 'p', '/x')`)
	exec(`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash)
	      VALUES ('o1', 's1', 'note', 'escaped', ?, 'p', 'project', 'h1')`, `{"log":"password=\"Hunter2Hunter2\" x"}`)
	exec(`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash)
	      VALUES ('o2', 's1', 'note', 'padded', ?, 'p', 'project', 'h2')`, "<private>\nPaddedValue1\n</private> and PaddedValue1 again")
	res, err := s.RedactScan(false)
	if err != nil || scanChanged(res, "observations") != 2 {
		t.Fatalf("dry-run: %+v %v", res, err)
	}
	if _, err := s.RedactScan(true); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM observations WHERE instr(content, 'Hunter2Hunter2') OR instr(content, 'PaddedValue1')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a value survived the scan (n=%d err=%v)", n, err)
	}
	res, err = s.RedactScan(false)
	if err != nil || res.RowsChanged != 0 {
		t.Fatalf("second scan not idempotent: %+v %v", res, err)
	}
}
