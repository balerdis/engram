package store

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/redact"
)

func countPrompts(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM user_prompts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The other client sends the prompt already redacted and with <private> stripped.
func preProcessed(text string) string {
	return strings.TrimSpace(redact.StripPrivate(redact.Redact(text)))
}

func TestAddPromptCollapsesSamePromptFromTwoClients(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession("s1", "p", "/x")
	raw := "deploy with " + fakeKey + " <private>my note</private> now"

	first, err := s.AddPrompt(AddPromptParams{SessionID: "s1", Project: "p", Content: raw})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AddPrompt(AddPromptParams{SessionID: "s1", Project: "p", Content: preProcessed(raw)})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || countPrompts(t, s) != 1 {
		t.Fatalf("want one row, ids %d/%d rows %d", first, second, countPrompts(t, s))
	}
}

func TestAddPromptKeepsSamePromptAfterTheWindow(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession("s1", "p", "/x")
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "s1", Project: "p", Content: "run the tests"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE user_prompts SET created_at = datetime('now', '-31 seconds')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "s1", Project: "p", Content: "run the tests"}); err != nil {
		t.Fatal(err)
	}
	if n := countPrompts(t, s); n != 2 {
		t.Fatalf("want 2 rows, got %d", n)
	}
}

func TestAddPromptKeepsSamePromptOfAnotherSession(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession("s1", "p", "/x")
	_ = s.CreateSession("s2", "p", "/x")
	for _, sid := range []string{"s1", "s2"} {
		if _, err := s.AddPrompt(AddPromptParams{SessionID: sid, Project: "p", Content: "run the tests"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := countPrompts(t, s); n != 2 {
		t.Fatalf("want 2 rows, got %d", n)
	}
}

func TestPassiveCaptureFromTwoClientsIsOneObservation(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession("s1", "p", "/x")
	raw := "## Key Learnings:\n\n- the deploy script reads " + fakeKey + " from the environment file <private>x</private>\n"
	for _, text := range []string{raw, preProcessed(raw)} {
		if _, err := s.PassiveCapture(PassiveCaptureParams{SessionID: "s1", Project: "p", Content: text}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM observations WHERE type = 'passive'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 observation, got %d", n)
	}
}

func TestStartSessionRefreshesStartedAtOfResumedSession(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"old", "new"} {
		if err := s.CreateSession(id, "p", "/x"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE sessions SET started_at = '2025-01-01 00:00:00' WHERE id = 'old'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET started_at = '2025-06-01 00:00:00' WHERE id = 'new'`); err != nil {
		t.Fatal(err)
	}
	if err := s.StartSession("old", "p", "/x"); err != nil {
		t.Fatal(err)
	}
	id, ok, err := s.MostRecentActiveSession("p")
	if err != nil || !ok || id != "old" {
		t.Fatalf("want old, got %q ok=%v err=%v", id, ok, err)
	}
}
