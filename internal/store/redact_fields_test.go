package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// fieldsHold reports whether needle (any case) is in a type, project or
// directory column of any table.
func fieldsHold(t *testing.T, s *Store, needle string) bool {
	t.Helper()
	queries := []string{
		`SELECT count(*) FROM observations WHERE instr(lower(type), lower(?1)) OR instr(lower(ifnull(project,'')), lower(?1))`,
		`SELECT count(*) FROM sessions WHERE instr(lower(project), lower(?1)) OR instr(lower(directory), lower(?1))`,
		`SELECT count(*) FROM user_prompts WHERE instr(lower(ifnull(project,'')), lower(?1))`,
	}
	for _, q := range queries {
		var n int
		if err := s.db.QueryRow(q, needle).Scan(&n); err != nil {
			t.Fatalf("scan db: %v", err)
		}
		if n > 0 {
			return true
		}
	}
	return false
}

func TestRedactTypeDirectoryProjectOnEveryWritePath(t *testing.T) {
	s := newTestStore(t)
	secretProj := "app-" + fakeKey // the project name is also a lookup key
	secretDir := "/work/" + fakeKey
	secretType := "note-" + fakeKey

	// Session start (implicit and explicit), observation save and update, prompt.
	if err := s.CreateSession("s1", secretProj, secretDir); err != nil {
		t.Fatal(err)
	}
	if err := s.StartSession("s2", secretProj, secretDir); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "s1", Type: secretType, Project: secretProj, Title: "t", Content: "c"})
	if err != nil {
		t.Fatal(err)
	}
	newType := "other-" + fakeKey
	newProj := "moved-" + fakeKey
	if _, err := s.UpdateObservation(id, UpdateObservationParams{Type: &newType, Project: &newProj}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "s1", Project: secretProj, Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddPromptIfMissing(AddPromptParams{SessionID: "s1", Project: secretProj, Content: "hi again"}); err != nil {
		t.Fatal(err)
	}

	// Import.
	data := &ExportData{
		Sessions:     []Session{{ID: "s3", Project: secretProj, Directory: secretDir, StartedAt: "2026-01-01 00:00:00"}},
		Observations: []Observation{{SyncID: "obs-aaaa", SessionID: "s3", Type: secretType, Title: "t", Content: "c", Scope: "project", Project: &secretProj, CreatedAt: "2026-01-01 00:00:00", UpdatedAt: "2026-01-01 00:00:00"}},
		Prompts:      []Prompt{{SyncID: "prompt-aaaa", SessionID: "s3", Project: secretProj, Content: "x", CreatedAt: "2026-01-01 00:00:00"}},
	}
	if _, err := s.Import(data); err != nil {
		t.Fatal(err)
	}

	// Pulled chunk.
	mk := func(entity, key string, payload any) SyncMutation {
		raw, _ := json.Marshal(payload)
		return SyncMutation{Entity: entity, EntityKey: key, Op: SyncOpUpsert, Payload: string(raw), Source: SyncSourceRemote}
	}
	muts := []SyncMutation{
		mk(SyncEntitySession, "s4", syncSessionPayload{ID: "s4", Project: secretProj, Directory: secretDir}),
		mk(SyncEntityObservation, "obs-bbbb", syncObservationPayload{SyncID: "obs-bbbb", SessionID: "s4", Type: secretType, Title: "t", Content: "c", Project: &secretProj, Scope: "project"}),
		mk(SyncEntityPrompt, "prompt-bbbb", syncPromptPayload{SyncID: "prompt-bbbb", SessionID: "s4", Content: "y", Project: &secretProj}),
	}
	if err := s.ApplyPulledChunk("local", "chunk-f", muts); err != nil {
		t.Fatal(err)
	}

	if fieldsHold(t, s, fakeKeyN) {
		t.Fatal("a credential reached a type, project or directory column")
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM observations WHERE project LIKE '%redacted%'`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("expected redacted project names, n=%d err=%v", n, err)
	}
}

// The project name is a lookup key: the same secret-bearing name must map to
// the same stored identity on write and on read, and the canonical form must be
// a fixed point.
func TestRedactedProjectIdentityIsStable(t *testing.T) {
	s := newTestStore(t)
	name := "app-" + fakeKey
	a, _ := NormalizeProject(name)
	b, _ := NormalizeProject(name)
	c, _ := NormalizeProject(a)
	if a != b || a != c {
		t.Fatalf("not deterministic/idempotent: %q %q %q", a, b, c)
	}
	if strings.Contains(strings.ToLower(a), "f4k3") {
		t.Fatalf("secret in canonical name: %q", a)
	}
	if err := s.CreateSession("s1", name, "/x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "s1", Type: "note", Project: name, Title: "t", Content: "findme"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Search("findme", SearchOptions{Project: name})
	if err != nil || len(res) != 1 {
		t.Fatalf("lookup by the same name failed: %v %d", err, len(res))
	}
	if ok, _ := s.ProjectExists(a); !ok {
		t.Fatal("ProjectExists by the canonical name failed")
	}
}
