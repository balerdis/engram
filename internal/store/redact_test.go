package store

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Gentleman-Programming/engram/internal/redact"
)

// Obviously fake values taken from the redact golden cases.
const (
	fakeKey  = "sk-F4K3F4K3F4K3F4K3F4K3F4K3"
	fakeKeyN = "F4K3F4K3F4K3"
	fakePass = "FakePass123"
)

// dbHolds reports whether needle appears in any persisted free-text column.
func dbHolds(t *testing.T, s *Store, needle string) bool {
	t.Helper()
	queries := []string{
		`SELECT count(*) FROM observations WHERE instr(title, ?1) OR instr(content, ?1) OR instr(ifnull(topic_key,''), ?1) OR instr(ifnull(tool_name,''), ?1)`,
		`SELECT count(*) FROM user_prompts WHERE instr(content, ?1)`,
		`SELECT count(*) FROM sessions WHERE instr(ifnull(summary,''), ?1)`,
		`SELECT count(*) FROM memory_relations WHERE instr(ifnull(reason,''), ?1) OR instr(ifnull(evidence,''), ?1)`,
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

func TestRedactAddObservationTitleContentTopicTool(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "p", "/x"); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "note", Project: "p",
		Title:    "rotate " + fakeKey,
		Content:  "config password=" + fakePass + " and " + fakeKey,
		TopicKey: "topic/" + fakeKey,
		ToolName: "tool-" + fakeKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dbHolds(t, s, fakeKeyN) || dbHolds(t, s, fakePass) {
		t.Fatal("credential reached the database")
	}
	obs, _ := s.GetObservation(id)
	if !strings.Contains(obs.Content, "password=[REDACTED:PASSWORD]") || !strings.Contains(obs.Title, "[REDACTED:OPENAI_KEY]") {
		t.Fatalf("unexpected stored text: %q / %q", obs.Title, obs.Content)
	}
}

func TestRedactUpdateObservation(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession("s1", "p", "/x")
	id, _ := s.AddObservation(AddObservationParams{SessionID: "s1", Type: "note", Project: "p", Title: "t", Content: "clean"})
	title, content, topic := "t "+fakeKey, "password="+fakePass, "k/"+fakeKey
	if _, err := s.UpdateObservation(id, UpdateObservationParams{Title: &title, Content: &content, TopicKey: &topic}); err != nil {
		t.Fatal(err)
	}
	if dbHolds(t, s, fakeKeyN) || dbHolds(t, s, fakePass) {
		t.Fatal("credential reached the database through update")
	}
}

func TestRedactPrompts(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession("s1", "p", "/x")
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "s1", Project: "p", Content: "use " + fakeKey}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddPromptIfMissing(AddPromptParams{SessionID: "s1", Project: "p", Content: "password=" + fakePass}); err != nil {
		t.Fatal(err)
	}
	// Idempotent re-send of the same prompt dedups against the redacted text.
	_, inserted, err := s.AddPromptIfMissing(AddPromptParams{SessionID: "s1", Project: "p", Content: "password=" + fakePass})
	if err != nil || inserted {
		t.Fatalf("expected dedup on redacted prompt, inserted=%v err=%v", inserted, err)
	}
	if dbHolds(t, s, fakeKeyN) || dbHolds(t, s, fakePass) {
		t.Fatal("credential reached user_prompts")
	}
}

func TestRedactEndSessionSummary(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession("s1", "p", "/x")
	if err := s.EndSession("s1", "done, token: "+fakeKey+" password="+fakePass); err != nil {
		t.Fatal(err)
	}
	if dbHolds(t, s, fakeKeyN) || dbHolds(t, s, fakePass) {
		t.Fatal("credential reached sessions.summary")
	}
	sess, _ := s.GetSession("s1")
	if sess.Summary == nil || !strings.Contains(*sess.Summary, "[REDACTED:") {
		t.Fatalf("summary not redacted: %v", sess.Summary)
	}
	// Ending without a summary keeps the stored one.
	if err := s.EndSession("s1", ""); err != nil {
		t.Fatal(err)
	}
	sess, _ = s.GetSession("s1")
	if sess.Summary == nil || !strings.Contains(*sess.Summary, "[REDACTED:") {
		t.Fatal("empty summary overwrote the stored one")
	}
}

func TestRedactPassiveCapture(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession("s1", "p", "/x")
	text := "## Key Learnings:\n- the deploy script reads password=" + fakePass + " from the environment file\n"
	if _, err := s.PassiveCapture(PassiveCaptureParams{SessionID: "s1", Project: "p", Content: text}); err != nil {
		t.Fatal(err)
	}
	if dbHolds(t, s, fakePass) {
		t.Fatal("credential reached the database through passive capture")
	}
}

func TestRedactImport(t *testing.T) {
	s := newTestStore(t)
	sum := "sum " + fakeKey
	tk := "k/" + fakeKey
	data := &ExportData{
		Sessions:     []Session{{ID: "s1", Project: "p", Directory: "/x", StartedAt: "2026-01-01 00:00:00", Summary: &sum}},
		Observations: []Observation{{SyncID: "obs-aaaa", SessionID: "s1", Type: "note", Title: "t " + fakeKey, Content: "password=" + fakePass, Scope: "project", TopicKey: &tk, CreatedAt: "2026-01-01 00:00:00", UpdatedAt: "2026-01-01 00:00:00"}},
		Prompts:      []Prompt{{SyncID: "prompt-aaaa", SessionID: "s1", Content: "use " + fakeKey, CreatedAt: "2026-01-01 00:00:00"}},
	}
	if _, err := s.Import(data); err != nil {
		t.Fatal(err)
	}
	if dbHolds(t, s, fakeKeyN) || dbHolds(t, s, fakePass) {
		t.Fatal("credential reached the database through Import")
	}
}

func TestRedactPulledChunk(t *testing.T) {
	s := newTestStore(t)
	mk := func(entity, key string, payload any) SyncMutation {
		raw, _ := json.Marshal(payload)
		return SyncMutation{Entity: entity, EntityKey: key, Op: SyncOpUpsert, Payload: string(raw), Source: SyncSourceRemote}
	}
	sum := "sum " + fakeKey
	proj := "p"
	muts := []SyncMutation{
		mk(SyncEntitySession, "s1", syncSessionPayload{ID: "s1", Project: "p", Directory: "/x", Summary: &sum}),
		mk(SyncEntityObservation, "obs-bbbb", syncObservationPayload{SyncID: "obs-bbbb", SessionID: "s1", Type: "note", Title: "t " + fakeKey, Content: "password=" + fakePass, Project: &proj, Scope: "project"}),
		mk(SyncEntityPrompt, "prompt-bbbb", syncPromptPayload{SyncID: "prompt-bbbb", SessionID: "s1", Content: "use " + fakeKey, Project: &proj}),
	}
	if err := s.ApplyPulledChunk("local", "chunk-1", muts); err != nil {
		t.Fatal(err)
	}
	if dbHolds(t, s, fakeKeyN) || dbHolds(t, s, fakePass) {
		t.Fatal("credential reached the database through ApplyPulledChunk")
	}
}

func TestRedactJudgeRelationReasonEvidence(t *testing.T) {
	s, a, b := setupSyncApplyStore(t)
	_ = a
	_ = b
	rel, err := s.SaveRelation(SaveRelationParams{SyncID: "rel-test1", SourceID: a, TargetID: b})
	if err != nil {
		t.Fatal(err)
	}
	reason, ev := "because "+fakeKey, "password="+fakePass
	if _, err := s.JudgeRelation(JudgeRelationParams{JudgmentID: rel.SyncID, Relation: "related", Reason: &reason, Evidence: &ev, MarkedByActor: "user", MarkedByKind: "human"}); err != nil {
		t.Fatal(err)
	}
	if dbHolds(t, s, fakeKeyN) || dbHolds(t, s, fakePass) {
		t.Fatal("credential reached memory_relations")
	}
}

func TestRedactIdempotentAndPlaceholders(t *testing.T) {
	for _, in := range []string{
		"password=[REDACTED:PASSWORD] and [REDACTED:OPENAI_KEY] and [REDACTED]",
		"password=$PEGASUS_SECRET_DB_PASSWORD token=${PEGASUS_SECRET_API_TOKEN}",
	} {
		once := sanitizeStored(in, 50000)
		if once != in {
			t.Fatalf("placeholder text changed: %q -> %q", in, once)
		}
		if twice := sanitizeStored(once, 50000); twice != once {
			t.Fatalf("not idempotent: %q -> %q", once, twice)
		}
	}
	first := sanitizeStored("a "+fakeKey+" b password="+fakePass, 50000)
	if second := sanitizeStored(first, 50000); second != first {
		t.Fatalf("not idempotent after redaction: %q -> %q", first, second)
	}
}

func TestRedactPrivateTags(t *testing.T) {
	got := sanitizeStored("keep <private>API_KEY=FakeSecret1234</private> end <private>just a note</private>", 50000)
	// A named value inside <private> is detected before the tag is stripped,
	// so it keeps its name; an unnamed one becomes a generic placeholder.
	if got != "keep [REDACTED:API_KEY] end [REDACTED:PRIVATE]" {
		t.Fatalf("unexpected private handling: %q", got)
	}
	if strings.Contains(got, "FakeSecret1234") {
		t.Fatal("secret survived")
	}
}

func TestRedactOverDetectCap(t *testing.T) {
	big := strings.Repeat("a", redact.MaxDetectBytes()+1000) + " password=" + fakePass
	got := sanitizeStored(big, 0)
	if strings.Contains(got, fakePass) {
		t.Fatal("secret beyond the cap survived")
	}
	if len(got) > redact.MaxDetectBytes()+len(truncatedSuffix) || !strings.HasSuffix(got, truncatedSuffix) {
		t.Fatalf("expected cut + marker, got len=%d", len(got))
	}
	// Secret inside the cap with a huge tail is still redacted.
	in := "password=" + fakePass + " " + strings.Repeat("b", redact.MaxDetectBytes())
	if got := sanitizeStored(in, 0); strings.Contains(got, fakePass) {
		t.Fatal("secret inside cap survived")
	}
}

func TestTruncateRuneSafe(t *testing.T) {
	const max = 50000
	// "ñ" is 2 bytes; max is even, so shift by one byte to land mid-character.
	in := "x" + strings.Repeat("ñ", max)
	got := sanitizeStored(in, max)
	if !utf8.ValidString(got) {
		t.Fatal("truncation produced invalid UTF-8")
	}
	if !strings.HasSuffix(got, truncatedSuffix) || len(got) > max+len(truncatedSuffix) {
		t.Fatalf("unexpected truncation: len=%d", len(got))
	}
	// 4-byte runes at every offset.
	for off := 0; off < 4; off++ {
		s := strings.Repeat("a", off) + strings.Repeat("😀", 20)
		if g := truncateRuneSafe(s, 10); !utf8.ValidString(g) {
			t.Fatalf("offset %d: invalid UTF-8", off)
		}
	}
	// Store path.
	s := newTestStore(t)
	_ = s.CreateSession("s1", "p", "/x")
	id, err := s.AddObservation(AddObservationParams{SessionID: "s1", Type: "note", Project: "p", Title: "t", Content: in})
	if err != nil {
		t.Fatal(err)
	}
	obs, _ := s.GetObservation(id)
	if !utf8.ValidString(obs.Content) {
		t.Fatal("stored content is invalid UTF-8")
	}
}

func TestRedactDedupDiffersOnlyInSecret(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession("s1", "p", "/x")
	p := AddObservationParams{SessionID: "s1", Type: "note", Project: "p", Title: "same", Scope: "project"}
	p.Content = "deploy with token=fake-token-1111 today"
	id1, err := s.AddObservation(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Content = "deploy with token=fake-token-2222 today"
	id2, err := s.AddObservation(p)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("observations differing only in the secret must dedup: %d vs %d", id1, id2)
	}
}

func BenchmarkSanitizeStored50KB(b *testing.B) {
	chunk := "The deploy uses the config at /etc/app and a note about tokens, see commit a1b2c3d4e5a1b2c3d4e5a1b2c3d4e5a1b2c3d4e5. password=FakePass123 next.\n"
	in := strings.Repeat(chunk, 50000/len(chunk)+1)[:50000]
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		_ = sanitizeStored(in, 50000)
	}
}

func BenchmarkSanitizeStored50KBPlain(b *testing.B) {
	chunk := "The deploy uses the config at /etc/app and a note about tokens, see commit a1b2c3d4e5a1b2c3d4e5a1b2c3d4e5a1b2c3d4e5. Nothing secret here.\n"
	in := strings.Repeat(chunk, 50000/len(chunk)+1)[:50000]
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		_ = sanitizeStored(in, 50000)
	}
}
