package sync

import (
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// Local export -> import must carry sessions, observations, prompts and
// relations, and must not leave rows in the legacy sync_mutations table on
// either side.
func TestLocalExportImportRoundTripLeavesNoSyncMutations(t *testing.T) {
	src := newTestStore(t)
	sourceSyncID, targetSyncID := seedRelationForProject(t, src, "proj-rt", "sess-rt", "rel-rt")
	if _, err := src.AddPrompt(store.AddPromptParams{SessionID: "sess-rt", Content: "round trip prompt", Project: "proj-rt"}); err != nil {
		t.Fatalf("add prompt: %v", err)
	}
	if err := src.EndSession("sess-rt", "round trip summary"); err != nil {
		t.Fatalf("end session: %v", err)
	}

	srcRows, err := src.CountSyncMutations()
	if err != nil {
		t.Fatalf("count source mutations: %v", err)
	}
	if srcRows != 0 {
		t.Fatalf("source store has %d sync_mutations rows after local writes, want 0", srcRows)
	}

	syncDir := filepath.Join(t.TempDir(), ".engram")
	exported, err := New(src, syncDir).Export("tester", "proj-rt")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if exported.IsEmpty || exported.SessionsExported != 1 || exported.ObservationsExported != 2 || exported.PromptsExported != 1 || exported.MutationsExported != 1 {
		t.Fatalf("unexpected export result: %+v", exported)
	}

	dst := newTestStore(t)
	imported, err := New(dst, syncDir).Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported.ChunksImported != 1 || imported.SessionsImported != 1 || imported.ObservationsImported != 2 || imported.PromptsImported != 1 {
		t.Fatalf("unexpected import result: %+v", imported)
	}

	sess, err := dst.GetSession("sess-rt")
	if err != nil {
		t.Fatalf("get imported session: %v", err)
	}
	if sess.Project != "proj-rt" || sess.Summary == nil || *sess.Summary != "round trip summary" {
		t.Fatalf("unexpected imported session: %+v", sess)
	}
	obs, err := dst.RecentObservations("proj-rt", "project", 10)
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("expected 2 imported observations, got %d", len(obs))
	}
	prompts, err := dst.RecentPrompts("proj-rt", 10)
	if err != nil {
		t.Fatalf("recent prompts: %v", err)
	}
	if len(prompts) != 1 || prompts[0].Content != "round trip prompt" {
		t.Fatalf("unexpected imported prompts: %+v", prompts)
	}
	rel, err := dst.GetRelation("rel-rt")
	if err != nil {
		t.Fatalf("get imported relation: %v", err)
	}
	if rel.SourceID != sourceSyncID || rel.TargetID != targetSyncID || rel.Relation != store.RelationCompatible {
		t.Fatalf("unexpected imported relation: %+v", rel)
	}

	dstRows, err := dst.CountSyncMutations()
	if err != nil {
		t.Fatalf("count destination mutations: %v", err)
	}
	if dstRows != 0 {
		t.Fatalf("destination store has %d sync_mutations rows after import, want 0", dstRows)
	}
}
