package store

import (
	"os"
	"strings"
	"testing"
)

// seedLegacyRows writes rows the way a version before the redactor did: raw
// inserts, secrets everywhere, the FTS triggers firing as they always did.
func seedLegacyRows(t *testing.T, s *Store) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO sessions (id, project, directory, summary) VALUES ('s1', 'App-Legacy', ?, ?)`, "/work/"+fakeKey, "done "+fakeKey)
	exec(`INSERT INTO sessions (id, project, directory) VALUES ('s2', 'MixedCase', '/clean')`)
	exec(`INSERT INTO observations (sync_id, session_id, type, title, content, tool_name, project, scope, topic_key, normalized_hash)
	      VALUES ('obs-1', 's1', ?, ?, ?, ?, ?, 'project', ?, 'oldhash')`,
		"note-"+fakeKey, "rotate "+fakeKey, "password="+fakePass+" uniqueword", "tool-"+fakeKey, "app-"+fakeKey, "topic/"+fakeKey)
	exec(`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash)
	      VALUES ('obs-2', 's2', 'note', 'clean title', 'clean content otherword', 'MixedCase', 'project', 'h2')`)
	exec(`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES ('prompt-1', 's1', ?, ?)`, "use "+fakeKey, "app-"+fakeKey)
	exec(`INSERT INTO memory_relations (sync_id, source_id, target_id, reason, evidence) VALUES ('rel-1', 'obs-1', 'obs-2', ?, ?)`, "because "+fakeKey, "password="+fakePass)
}

func scanChanged(res *RedactScanResult, table string) int {
	for _, t := range res.Tables {
		if t.Table == table {
			return t.RowsChanged
		}
	}
	return -1
}

func TestRedactScanDryRunApplyIdempotent(t *testing.T) {
	s := newTestStore(t)
	seedLegacyRows(t, s)

	// Dry-run: counts only, nothing changes, no backup.
	res, err := s.RedactScan(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || res.BackupPath != "" || scanChanged(res, "sessions") != 1 || scanChanged(res, "observations") != 1 ||
		scanChanged(res, "user_prompts") != 1 || scanChanged(res, "memory_relations") != 1 || res.RowsChanged != 4 {
		t.Fatalf("unexpected dry-run: %+v", res)
	}
	var holds int
	if err := s.db.QueryRow(`SELECT count(*) FROM observations WHERE instr(content, ?)`, fakePass).Scan(&holds); err != nil || holds != 1 {
		t.Fatalf("dry-run changed the data (holds=%d err=%v)", holds, err)
	}
	if _, err := os.Stat(s.cfg.DataDir + "/backups"); err == nil {
		t.Fatal("dry-run created the backups dir")
	}
	// The report never carries a value.
	if b := strings.ToLower(res.dump()); strings.Contains(b, "f4k3") || strings.Contains(b, "fakepass") {
		t.Fatalf("report leaks a value: %s", b)
	}

	// Apply.
	res, err = s.RedactScan(true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || res.BackupPath == "" || res.RowsChanged != 4 {
		t.Fatalf("unexpected apply result: %+v", res)
	}
	if fi, err := os.Stat(res.BackupPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup: %v %v", fi, err)
	}
	if dbHolds(t, s, fakeKeyN) || dbHolds(t, s, fakePass) || fieldsHold(t, s, fakeKeyN) {
		t.Fatal("a credential survived the scan")
	}
	// Project identity: the secret-bearing name moved to the same canonical
	// form a new write gets; a clean mixed-case name was left alone.
	want, _ := NormalizeProject("app-" + fakeKey)
	var proj, mixed string
	s.db.QueryRow(`SELECT project FROM observations WHERE sync_id='obs-1'`).Scan(&proj)
	s.db.QueryRow(`SELECT project FROM observations WHERE sync_id='obs-2'`).Scan(&mixed)
	if proj != want || mixed != "MixedCase" {
		t.Fatalf("project %q (want %q), clean %q", proj, want, mixed)
	}
	var sp, pp string
	s.db.QueryRow(`SELECT project FROM sessions WHERE id='s1'`).Scan(&sp)
	s.db.QueryRow(`SELECT project FROM user_prompts WHERE sync_id='prompt-1'`).Scan(&pp)
	if sp != "App-Legacy" || pp != want {
		t.Fatalf("session project %q prompt project %q", sp, pp)
	}
	// normalized_hash follows the new content.
	var hash, content string
	s.db.QueryRow(`SELECT normalized_hash, content FROM observations WHERE sync_id='obs-1'`).Scan(&hash, &content)
	if hash != hashNormalized(content) || hash == "oldhash" {
		t.Fatalf("hash not recomputed: %q", hash)
	}

	// FTS in sync: the old secret is not searchable, the rest still is.
	if r, err := s.Search(fakePass, SearchOptions{}); err != nil || len(r) != 0 {
		t.Fatalf("FTS still finds the secret: %v %d", err, len(r))
	}
	if r, err := s.Search("uniqueword", SearchOptions{}); err != nil || len(r) != 1 {
		t.Fatalf("FTS lost the row: %v %d", err, len(r))
	}
	if r, err := s.Search("otherword", SearchOptions{}); err != nil || len(r) != 1 {
		t.Fatalf("FTS lost the untouched row: %v %d", err, len(r))
	}
	var ftsHits int
	s.db.QueryRow(`SELECT count(*) FROM observations_fts WHERE observations_fts MATCH '"F4K3F4K3F4K3"'`).Scan(&ftsHits)
	if ftsHits != 0 {
		t.Fatal("FTS index still holds the secret token")
	}
	// The index agrees with the observations table (external-content check).
	if _, err := s.db.Exec(`INSERT INTO observations_fts(observations_fts, rank) VALUES('integrity-check', 1)`); err != nil {
		t.Fatalf("FTS integrity-check: %v", err)
	}

	// Idempotent: a second run finds nothing and writes no second backup.
	res2, err := s.RedactScan(true)
	if err != nil {
		t.Fatal(err)
	}
	if res2.RowsChanged != 0 || res2.BackupPath != "" || res2.Applied {
		t.Fatalf("second run not a no-op: %+v", res2)
	}
}

func (r *RedactScanResult) dump() string {
	var b strings.Builder
	for _, t := range r.Tables {
		b.WriteString(t.Table)
		for _, f := range t.FieldNames() {
			b.WriteString(" " + f)
		}
	}
	return b.String()
}
