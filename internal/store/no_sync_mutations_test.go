package store

import "testing"

// Nothing writes to sync_mutations any more: the table stays in the schema so
// older binaries can still open the database, but no write path may add rows.
func TestWritePathsLeaveSyncMutationsEmpty(t *testing.T) {
	s := newTestStore(t)

	check := func(step string) {
		t.Helper()
		n, err := s.CountSyncMutations()
		if err != nil {
			t.Fatalf("%s: count: %v", step, err)
		}
		if n != 0 {
			t.Fatalf("%s: sync_mutations has %d row(s), want 0", step, n)
		}
	}

	if err := s.CreateSession("nm-s1", "nm-proj", "/tmp/nm"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.CreateSession("nm-s2", "nm-proj", "/tmp/nm"); err != nil {
		t.Fatalf("CreateSession 2: %v", err)
	}
	check("CreateSession")

	id1, err := s.AddObservation(AddObservationParams{SessionID: "nm-s1", Type: "decision", Title: "first", Content: "first content", Project: "nm-proj", Scope: "project"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	check("AddObservation insert")
	if _, err := s.AddObservation(AddObservationParams{SessionID: "nm-s1", Type: "decision", Title: "first", Content: "first content", Project: "nm-proj", Scope: "project"}); err != nil {
		t.Fatalf("AddObservation dedupe: %v", err)
	}
	check("AddObservation dedupe")
	if _, err := s.AddObservation(AddObservationParams{SessionID: "nm-s1", Type: "architecture", Title: "topic", Content: "v1", Project: "nm-proj", Scope: "project", TopicKey: "arch/nm"}); err != nil {
		t.Fatalf("AddObservation topic: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "nm-s1", Type: "architecture", Title: "topic", Content: "v2", Project: "nm-proj", Scope: "project", TopicKey: "arch/nm"}); err != nil {
		t.Fatalf("AddObservation topic revision: %v", err)
	}
	check("AddObservation topic upsert")
	id2, err := s.AddObservation(AddObservationParams{SessionID: "nm-s1", Type: "note", Title: "second", Content: "second content", Project: "nm-proj", Scope: "project"})
	if err != nil {
		t.Fatalf("AddObservation 2: %v", err)
	}
	id3, err := s.AddObservation(AddObservationParams{SessionID: "nm-s2", Type: "note", Title: "third", Content: "third content", Project: "nm-proj", Scope: "project"})
	if err != nil {
		t.Fatalf("AddObservation 3: %v", err)
	}

	newTitle := "first, edited"
	if _, err := s.UpdateObservation(id1, UpdateObservationParams{Title: &newTitle}); err != nil {
		t.Fatalf("UpdateObservation: %v", err)
	}
	check("UpdateObservation")
	if err := s.PinObservation(id1); err != nil {
		t.Fatalf("PinObservation: %v", err)
	}
	if err := s.UnpinObservation(id1); err != nil {
		t.Fatalf("UnpinObservation: %v", err)
	}
	if err := s.MarkReviewed(id1); err != nil {
		t.Fatalf("MarkReviewed: %v", err)
	}
	check("pin/unpin/mark reviewed")

	obs1, err := s.GetObservation(id1)
	if err != nil {
		t.Fatal(err)
	}
	obs2, err := s.GetObservation(id2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveRelation(SaveRelationParams{SyncID: "rel-nm-1", SourceID: obs1.SyncID, TargetID: obs2.SyncID}); err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}
	if _, err := s.JudgeRelation(JudgeRelationParams{JudgmentID: "rel-nm-1", Relation: RelationRelated, MarkedByActor: "agent:test", MarkedByKind: "agent"}); err != nil {
		t.Fatalf("JudgeRelation: %v", err)
	}
	if _, err := s.JudgeBySemantic(JudgeBySemanticParams{SourceID: obs1.SyncID, TargetID: obs2.SyncID, Relation: RelationCompatible, Confidence: 0.9, Reasoning: "test", Model: "test-model"}); err != nil {
		t.Fatalf("JudgeBySemantic: %v", err)
	}
	check("relations")

	promptID, err := s.AddPrompt(AddPromptParams{SessionID: "nm-s1", Content: "a prompt", Project: "nm-proj"})
	if err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}
	if _, _, err := s.AddPromptIfMissing(AddPromptParams{SessionID: "nm-s1", Content: "another prompt", Project: "nm-proj"}); err != nil {
		t.Fatalf("AddPromptIfMissing insert: %v", err)
	}
	if _, _, err := s.AddPromptIfMissing(AddPromptParams{SessionID: "nm-s1", Content: "another prompt", Project: "nm-proj"}); err != nil {
		t.Fatalf("AddPromptIfMissing existing: %v", err)
	}
	check("AddPrompt / AddPromptIfMissing")
	if err := s.DeletePrompt(promptID); err != nil {
		t.Fatalf("DeletePrompt: %v", err)
	}
	check("DeletePrompt")

	if err := s.DeleteObservation(id2, false); err != nil {
		t.Fatalf("DeleteObservation soft: %v", err)
	}
	if err := s.DeleteObservation(id3, true); err != nil {
		t.Fatalf("DeleteObservation hard: %v", err)
	}
	check("DeleteObservation")

	if err := s.EndSession("nm-s1", "summary"); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	check("EndSession")
	if err := s.DeleteSession("nm-s2"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	check("DeleteSession")

	if _, err := s.MigrateProject("nm-proj", "nm-proj-renamed"); err != nil {
		t.Fatalf("MigrateProject: %v", err)
	}
	check("MigrateProject")
	if _, err := s.MergeProjects([]string{"nm-proj-renamed"}, "nm-proj-canonical"); err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}
	check("MergeProjects")
	if _, err := s.DeleteProject("nm-proj-canonical", true); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	check("DeleteProject")
}
