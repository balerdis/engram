package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

const (
	redFakeKey  = "sk-F4K3F4K3F4K3F4K3F4K3F4K3"
	redFakeKeyN = "F4K3F4K3F4K3"
	redFakePass = "FakePass123"
)

func redactPostJSON(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// assertNoSecretInStore checks everything the store can export.
func assertNoSecretInStore(t *testing.T, s *store.Store) {
	t.Helper()
	data, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(data)
	if strings.Contains(string(raw), redFakeKeyN) || strings.Contains(string(raw), redFakePass) {
		t.Fatalf("credential reached the database: %s", raw)
	}
}

func TestHTTPWritePathsNeverStoreCredentials(t *testing.T) {
	st := newServerTestStore(t)
	h := New(st, 0).Handler()

	if rec := redactPostJSON(t, h, "/sessions", map[string]any{"id": "s1", "project": "p"}); rec.Code != http.StatusCreated {
		t.Fatalf("session: %d", rec.Code)
	}
	if rec := redactPostJSON(t, h, "/observations", map[string]any{
		"session_id": "s1", "type": "note", "project": "p",
		"title": "t " + redFakeKey, "content": "password=" + redFakePass,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("observation: %d", rec.Code)
	}
	if rec := redactPostJSON(t, h, "/prompts", map[string]any{"session_id": "s1", "project": "p", "content": "use " + redFakeKey}); rec.Code != http.StatusCreated {
		t.Fatalf("prompt: %d", rec.Code)
	}
	if rec := redactPostJSON(t, h, "/observations/passive", map[string]any{
		"session_id": "s1", "project": "p",
		"content": "## Key Learnings:\n- the script reads password=" + redFakePass + " from the env file\n",
	}); rec.Code != http.StatusOK {
		t.Fatalf("passive: %d %s", rec.Code, rec.Body.String())
	}
	if rec := redactPostJSON(t, h, "/sessions/s1/end", map[string]any{"summary": "done " + redFakeKey}); rec.Code != http.StatusOK {
		t.Fatalf("end: %d", rec.Code)
	}
	assertNoSecretInStore(t, st)

	sess, err := st.GetSession("s1")
	if err != nil || sess.Summary == nil || !strings.Contains(*sess.Summary, "[REDACTED:OPENAI_KEY]") {
		t.Fatalf("summary not redacted: %v %v", sess, err)
	}
}

func TestHTTPUpdateAndImportNeverStoreCredentials(t *testing.T) {
	st := newServerTestStore(t)
	h := New(st, 0).Handler()
	redactPostJSON(t, h, "/sessions", map[string]any{"id": "s1", "project": "p"})
	id, err := st.AddObservation(store.AddObservationParams{SessionID: "s1", Type: "note", Project: "p", Title: "t", Content: "clean"})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/observations/"+redactItoa(id),
		strings.NewReader(`{"title":"t `+redFakeKey+`","content":"password=`+redFakePass+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}

	imp := store.ExportData{
		Sessions:     []store.Session{{ID: "s2", Project: "p", StartedAt: "2026-01-01 00:00:00"}},
		Observations: []store.Observation{{SyncID: "obs-import1", SessionID: "s2", Type: "note", Title: "x", Content: "token: " + redFakeKey, Scope: "project", CreatedAt: "2026-01-01 00:00:00", UpdatedAt: "2026-01-01 00:00:00"}},
		Prompts:      []store.Prompt{{SyncID: "prompt-import1", SessionID: "s2", Content: "password=" + redFakePass, CreatedAt: "2026-01-01 00:00:00"}},
	}
	if rec := redactPostJSON(t, h, "/import", imp); rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	assertNoSecretInStore(t, st)
}

func redactItoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestPostPromptReportsSkippedSyntheticPrompt(t *testing.T) {
	st := newServerTestStore(t)
	h := New(st, 0).Handler()
	redactPostJSON(t, h, "/sessions", map[string]any{"id": "s1", "project": "p"})

	rec := redactPostJSON(t, h, "/prompts", map[string]any{"session_id": "s1", "project": "p", "content": "<task-notification>done</task-notification>"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"skipped"`) {
		t.Fatalf("synthetic prompt: want 200 skipped, got %d %s", rec.Code, rec.Body.String())
	}
	// A duplicate inside the dedupe window still reports the stored id as saved.
	body := map[string]any{"session_id": "s1", "project": "p", "content": "real prompt"}
	first := redactPostJSON(t, h, "/prompts", body)
	second := redactPostJSON(t, h, "/prompts", body)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || first.Body.String() != second.Body.String() || strings.Contains(second.Body.String(), `"id":0`) {
		t.Fatalf("duplicate: %d %s / %d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
}
