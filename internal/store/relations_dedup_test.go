package store

import "testing"

var floorAll = -100.0

func TestSanitizeFTSCandidatesEscapesInnerQuotes(t *testing.T) {
	got := sanitizeFTSCandidates(`say "hi" he said"x`)
	want := `"say" OR "hi" OR "he" OR "said""x"`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// A title with an inner quote must not make the candidate query fail.
func TestFindCandidatesTitleWithInnerQuote(t *testing.T) {
	s := setupRelationsStore(t)
	addTestObs(t, s, `Parser says foo"bar broke`, "bugfix", "testproject", "project")
	id, _ := addTestObs(t, s, `Parser says foo"bar again`, "bugfix", "testproject", "project")
	if _, err := s.FindCandidates(id, CandidateOptions{Project: "testproject", Scope: "project", Limit: 5, BM25Floor: &floorAll}); err != nil {
		t.Fatalf("FindCandidates with inner quote: %v", err)
	}
}

// A pair that already has a relation (any status, either direction) is not
// proposed again; an orphaned relation does not block.
func TestFindCandidatesSkipsPairsWithLiveRelation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		verdict    string // "" = leave pending
		wantFirst  int
		wantSecond int
	}{
		{"pending", "", 1, 0},
		{"not_conflict", RelationNotConflict, 1, 0},
		{"related", RelationRelated, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := setupRelationsStore(t)
			addTestObs(t, s, "Choose database engine for sessions", "decision", "testproject", "project")
			id, _ := addTestObs(t, s, "Choose database engine for sessions storage", "decision", "testproject", "project")
			opts := CandidateOptions{Project: "testproject", Scope: "project", Limit: 5, BM25Floor: &floorAll}

			first, err := s.FindCandidates(id, opts)
			if err != nil || len(first) != tc.wantFirst {
				t.Fatalf("first: n=%d err=%v", len(first), err)
			}
			if tc.verdict != "" {
				if _, err := s.JudgeRelation(JudgeRelationParams{JudgmentID: first[0].JudgmentID, Relation: tc.verdict}); err != nil {
					t.Fatalf("judge: %v", err)
				}
			}
			// Re-save of the same observation (topic_key upsert keeps the id).
			second, err := s.FindCandidates(id, opts)
			if err != nil || len(second) != tc.wantSecond {
				t.Fatalf("second: n=%d err=%v (want %d)", len(second), err, tc.wantSecond)
			}
			var n int
			if err := s.db.QueryRow(`SELECT count(*) FROM memory_relations`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("rows=%d err=%v, want exactly 1", n, err)
			}
		})
	}
}

// The reverse direction (the old candidate being the saved one) also counts.
func TestFindCandidatesSkipsReverseDirection(t *testing.T) {
	s := setupRelationsStore(t)
	idA, _ := addTestObs(t, s, "Choose database engine for sessions", "decision", "testproject", "project")
	idB, _ := addTestObs(t, s, "Choose database engine for sessions storage", "decision", "testproject", "project")
	opts := CandidateOptions{Project: "testproject", Scope: "project", Limit: 5, BM25Floor: &floorAll}
	if c, err := s.FindCandidates(idB, opts); err != nil || len(c) != 1 {
		t.Fatalf("B->A: n=%d err=%v", len(c), err)
	}
	if c, err := s.FindCandidates(idA, opts); err != nil || len(c) != 0 {
		t.Fatalf("A->B must be skipped: n=%d err=%v", len(c), err)
	}
}

func TestFindCandidatesOrphanedRelationDoesNotBlock(t *testing.T) {
	s := setupRelationsStore(t)
	addTestObs(t, s, "Choose database engine for sessions", "decision", "testproject", "project")
	id, _ := addTestObs(t, s, "Choose database engine for sessions storage", "decision", "testproject", "project")
	opts := CandidateOptions{Project: "testproject", Scope: "project", Limit: 5, BM25Floor: &floorAll}
	if c, err := s.FindCandidates(id, opts); err != nil || len(c) != 1 {
		t.Fatalf("first: n=%d err=%v", len(c), err)
	}
	if _, err := s.db.Exec(`UPDATE memory_relations SET judgment_status = 'orphaned'`); err != nil {
		t.Fatal(err)
	}
	if c, err := s.FindCandidates(id, opts); err != nil || len(c) != 1 {
		t.Fatalf("orphaned must not block: n=%d err=%v", len(c), err)
	}
}
