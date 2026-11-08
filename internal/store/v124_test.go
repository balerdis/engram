package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func v124Obs(t *testing.T, s *Store, title, content, topic string) int64 {
	t.Helper()
	id, err := s.AddObservation(AddObservationParams{SessionID: "s1", Type: "decision", Title: title, Content: content, Project: "p", Scope: "project", TopicKey: topic})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	return id
}

func v124Store(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	if err := s.CreateSession("s1", "p", "/x"); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAddPromptSkipsSyntheticPrompts(t *testing.T) {
	s := v124Store(t)
	for _, text := range []string{
		"<task-notification>done</task-notification>",
		"  \n\t<agent-message from=\"x\">report</agent-message>",
	} {
		id, err := s.AddPrompt(AddPromptParams{SessionID: "s1", Project: "p", Content: text})
		if err != nil || id != 0 {
			t.Fatalf("%q: want (0, nil), got (%d, %v)", text, id, err)
		}
	}
	if n := countPrompts(t, s); n != 0 {
		t.Fatalf("synthetic prompts were stored: %d", n)
	}
	// A prompt that only mentions the marker is not synthetic.
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "s1", Project: "p", Content: "why does <task-notification> appear?"}); err != nil {
		t.Fatal(err)
	}
	if n := countPrompts(t, s); n != 1 {
		t.Fatalf("want 1 prompt, got %d", n)
	}
}

func TestImportKeepsSyntheticPrompts(t *testing.T) {
	s := v124Store(t)
	res, err := s.Import(&ExportData{
		Sessions: []Session{{ID: "s1", Project: "p"}},
		Prompts:  []Prompt{{SyncID: "prompt-a", SessionID: "s1", Content: "<task-notification>x</task-notification>", Project: "p", CreatedAt: "2026-01-01 00:00:00"}},
	})
	if err != nil || res.PromptsImported != 1 {
		t.Fatalf("import: %+v %v", res, err)
	}
}

func TestImportPromptsIsIdempotent(t *testing.T) {
	s := v124Store(t)
	data := &ExportData{
		Sessions: []Session{{ID: "s1", Project: "p"}},
		Prompts: []Prompt{
			{SyncID: "prompt-with-id", SessionID: "s1", Content: "one", Project: "p", CreatedAt: "2026-01-01 00:00:00"},
			{SessionID: "s1", Content: "no sync id", Project: "p", CreatedAt: "2026-01-02 00:00:00"},
		},
	}
	first, err := s.Import(data)
	if err != nil || first.PromptsImported != 2 {
		t.Fatalf("first import: %+v %v", first, err)
	}
	second, err := s.Import(data)
	if err != nil || second.PromptsImported != 0 {
		t.Fatalf("second import: %+v %v", second, err)
	}
	if n := countPrompts(t, s); n != 2 {
		t.Fatalf("want 2 prompts after two imports, got %d", n)
	}
}

func TestPruneProjectKeepsSessionsOfSoftDeletedObservations(t *testing.T) {
	s := v124Store(t)
	_ = s.CreateSession("s-empty", "p", "/x")
	id := v124Obs(t, s, "t", "c", "")
	if err := s.DeleteObservation(id, false); err != nil {
		t.Fatal(err)
	}
	res, err := s.PruneProject("p")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if res.SessionsDeleted != 1 {
		t.Fatalf("want only the empty session deleted, got %d", res.SessionsDeleted)
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = 's1'`).Scan(&n)
	if n != 1 {
		t.Fatal("session of a soft-deleted observation was removed")
	}
}

func versionContents(t *testing.T, s *Store, id int64) []string {
	t.Helper()
	vs, total, err := s.ObservationVersions(id, 0, 0)
	if err != nil || total != len(vs) {
		t.Fatalf("versions: %v total=%d len=%d", err, total, len(vs))
	}
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Content
	}
	return out
}

func TestObservationVersionsOnUpsertAndUpdate(t *testing.T) {
	s := v124Store(t)
	id := v124Obs(t, s, "title", "v1", "topic/x")
	v124Obs(t, s, "title", "v2", "topic/x") // upsert replaces the content
	v124Obs(t, s, "title", "v2", "topic/x") // same title and content: no version
	c3 := "v3"
	if _, err := s.UpdateObservation(id, UpdateObservationParams{Content: &c3}); err != nil {
		t.Fatal(err)
	}
	same := "v3"
	if _, err := s.UpdateObservation(id, UpdateObservationParams{Content: &same}); err != nil {
		t.Fatal(err)
	}
	got := versionContents(t, s, id)
	if strings.Join(got, ",") != "v2,v1" {
		t.Fatalf("want newest first v2,v1 got %v", got)
	}
	vs, total, _ := s.ObservationVersions(id, 1, 1)
	if total != 2 || len(vs) != 1 || vs[0].Content != "v1" || vs[0].Type != "decision" || vs[0].TopicKey == nil || *vs[0].TopicKey != "topic/x" || vs[0].ReplacedAt == "" {
		t.Fatalf("paging: total=%d %+v", total, vs)
	}
}

func TestObservationVersionsAreBounded(t *testing.T) {
	s := v124Store(t)
	id := v124Obs(t, s, "title", "c0", "topic/x")
	for i := 1; i <= 25; i++ {
		v124Obs(t, s, "title", fmt.Sprintf("c%d", i), "topic/x")
	}
	got := versionContents(t, s, id)
	if len(got) != maxObservationVersions || got[0] != "c24" || got[len(got)-1] != "c5" {
		t.Fatalf("want 20 versions c24..c5, got %d: %v", len(got), got)
	}
}

func TestObservationVersionsGoWithTheirObservation(t *testing.T) {
	s := v124Store(t)
	id := v124Obs(t, s, "title", "a", "topic/x")
	v124Obs(t, s, "title", "b", "topic/x")
	if err := s.DeleteObservation(id, true); err != nil {
		t.Fatal(err)
	}
	if n := purgeCount(t, s, `SELECT COUNT(*) FROM observation_versions`); n != 0 {
		t.Fatalf("hard delete left %d versions", n)
	}
}

func TestPurgeRemovesObservationVersions(t *testing.T) {
	s := v124Store(t)
	v124Obs(t, s, "title", "a", "topic/x")
	v124Obs(t, s, "title", "b", "topic/x")
	if n := purgeCount(t, s, `SELECT COUNT(*) FROM observation_versions`); n != 1 {
		t.Fatalf("setup: %d versions", n)
	}
	if _, err := s.Purge(PurgeSelector{Project: "p"}, PurgeOptions{}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n := purgeCount(t, s, `SELECT COUNT(*) FROM observation_versions`); n != 0 {
		t.Fatalf("purge left %d versions", n)
	}
}

func TestRedactScanRetapsVersionsAndSavesNoneItself(t *testing.T) {
	s := v124Store(t)
	id := v124Obs(t, s, "title", "clean", "topic/x")
	v124Obs(t, s, "title", "second", "topic/x")
	// A version written before the redactor covered it, and a legacy observation
	// that still holds a credential.
	if _, err := s.db.Exec(`UPDATE observation_versions SET content = ?, title = ? WHERE observation_id = ?`, "token "+fakeKey, "key "+fakeKey, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE observations SET content = ? WHERE id = ?`, "legacy "+fakeKey, id); err != nil {
		t.Fatal(err)
	}
	before := purgeCount(t, s, `SELECT COUNT(*) FROM observation_versions`)

	res, err := s.RedactScan(true)
	if err != nil || !res.Applied {
		t.Fatalf("scan: %+v %v", res, err)
	}
	var leaked int
	_ = s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM observation_versions WHERE content LIKE '%'||?||'%' OR title LIKE '%'||?||'%')
		+ (SELECT COUNT(*) FROM observations WHERE content LIKE '%'||?||'%')`, fakeKey, fakeKey, fakeKey).Scan(&leaked)
	if leaked != 0 {
		t.Fatalf("%d rows still hold the credential", leaked)
	}
	if after := purgeCount(t, s, `SELECT COUNT(*) FROM observation_versions`); after != before {
		t.Fatalf("the redaction rewrite saved a version: %d -> %d", before, after)
	}
}

func TestSyncImportKeepsObservationVersion(t *testing.T) {
	s := v124Store(t)
	id := v124Obs(t, s, "title", "local content", "topic/x")
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	proj := "p"
	push := func(title, content string) {
		raw, err := json.Marshal(syncObservationPayload{SyncID: obs.SyncID, SessionID: "s1", Type: "decision", Title: title, Content: content, Project: &proj, Scope: "project"})
		if err != nil {
			t.Fatal(err)
		}
		m := SyncMutation{Entity: SyncEntityObservation, EntityKey: obs.SyncID, Op: SyncOpUpsert, Payload: string(raw), Source: SyncSourceRemote, Project: proj}
		if err := s.withTx(func(tx *sql.Tx) error { return s.applyPulledMutationTx(tx, m) }); err != nil {
			t.Fatal(err)
		}
	}
	push("title", "imported content")
	push("title", "imported content") // unchanged: no version
	if got := versionContents(t, s, id); strings.Join(got, ",") != "local content" {
		t.Fatalf("want the overwritten content kept as a version, got %v", got)
	}
}
