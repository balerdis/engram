package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func purgeSeed(t *testing.T, s *Store) {
	t.Helper()
	for _, c := range []struct{ id, project string }{{"s-lab", "lab"}, {"s-lab2", "lab"}, {"s-keep", "keep"}} {
		if err := s.CreateSession(c.id, c.project, "/tmp/"+c.project); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}
	add := func(session, project, title, content string) int64 {
		id, err := s.AddObservation(AddObservationParams{SessionID: session, Type: "decision", Title: title, Content: content, Project: project, Scope: "project"})
		if err != nil {
			t.Fatalf("add obs: %v", err)
		}
		return id
	}
	add("s-lab", "lab", "lab alpha", "zebrafish content alpha")
	add("s-lab", "lab", "lab beta", "zebrafish content beta")
	add("s-lab2", "lab", "lab gamma", "zebrafish content gamma")
	add("s-keep", "keep", "keep one", "zebrafish keepable content")
	for _, p := range []struct{ session, project, content string }{
		{"s-lab", "lab", "quokka prompt lab"}, {"s-keep", "keep", "quokka prompt keep"},
	} {
		if _, err := s.AddPrompt(AddPromptParams{SessionID: p.session, Project: p.project, Content: p.content}); err != nil {
			t.Fatalf("add prompt: %v", err)
		}
	}
}

func purgeCount(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

func purgeTables(t *testing.T, s *Store) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, tb := range []string{"sessions", "observations", "user_prompts", "memory_relations", "prompt_tombstones", "sync_mutations", "sync_apply_deferred", "sync_state", "sync_enrolled_projects", "cloud_upgrade_state", "sync_chunks"} {
		out[tb] = purgeCount(t, s, "SELECT COUNT(*) FROM "+tb)
	}
	return out
}

func TestPurgeRequiresSelector(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.PurgePlan(PurgeSelector{}, PurgeOptions{}); !errors.Is(err, ErrPurgeRefused) {
		t.Fatalf("expected refusal, got %v", err)
	}
}

func TestPurgeSelectors(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)

	cases := []struct {
		name         string
		sel          PurgeSelector
		obs, prompts int
		sessions     int
	}{
		{"project", PurgeSelector{Project: "LAB"}, 3, 1, 2},
		{"session", PurgeSelector{SessionID: "s-lab"}, 2, 1, 1},
		{"project+session", PurgeSelector{Project: "lab", SessionID: "s-lab2"}, 1, 0, 1},
		{"project+other session", PurgeSelector{Project: "keep", SessionID: "s-lab"}, 0, 0, 0},
		{"since future", PurgeSelector{Since: "2999-01-01"}, 0, 0, 0},
		{"until past", PurgeSelector{Until: "2000-01-01"}, 0, 0, 0},
		{"until today covers the day", PurgeSelector{Project: "lab", Until: "2999-01-01"}, 3, 1, 2},
		{"since past", PurgeSelector{Project: "lab", Since: "2000-01-01T00:00:00Z"}, 3, 1, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := s.PurgePlan(c.sel, PurgeOptions{})
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if p.Counts.Observations != c.obs || p.Counts.Prompts != c.prompts || p.Counts.Sessions != c.sessions {
				t.Fatalf("counts = %+v, want obs=%d prompts=%d sessions=%d", p.Counts, c.obs, c.prompts, c.sessions)
			}
		})
	}
}

func TestPurgeInvalidDate(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.PurgePlan(PurgeSelector{Since: "yesterday"}, PurgeOptions{}); err == nil || !strings.Contains(err.Error(), "invalid date") {
		t.Fatalf("expected invalid date error, got %v", err)
	}
}

func TestPurgeDryRunWritesNothing(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	before := purgeTables(t, s)
	p, err := s.PurgePlan(PurgeSelector{Project: "lab"}, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Counts.Observations != 3 || len(p.Samples) == 0 {
		t.Fatalf("unexpected plan %+v", p)
	}
	after := purgeTables(t, s)
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("table %s changed on dry-run: %d -> %d", k, v, after[k])
		}
	}
	if _, err := os.Stat(s.cfg.DataDir + "/backups"); err == nil {
		t.Fatal("dry-run must not create a backup")
	}
}

func TestPurgeExecuteDeletesSelectionOnly(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)

	// Related rows across the tables.
	labObs := purgeCount(t, s, "SELECT MIN(id) FROM observations WHERE project='lab'")
	var labSync, keepSync string
	if err := s.db.QueryRow("SELECT sync_id FROM observations WHERE id=?", labObs).Scan(&labSync); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT sync_id FROM observations WHERE project='keep'").Scan(&keepSync); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, a ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO memory_relations (sync_id, source_id, target_id) VALUES ('rel-lab', ?, ?)`, labSync, keepSync)
	mustExec(`INSERT INTO memory_relations (sync_id, source_id, target_id) VALUES ('rel-keep', ?, ?)`, keepSync, keepSync)
	mustExec(`INSERT OR IGNORE INTO sync_state (target_key) VALUES ('cloud')`)
	mustExec(`DELETE FROM sync_mutations`)
	mustExec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, project) VALUES ('cloud','observation',?, 'upsert','{}','lab')`, labSync)
	mustExec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, project) VALUES ('cloud','observation',?, 'upsert','{}','keep')`, keepSync)
	mustExec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, project) VALUES ('cloud','relation','rel-lab','upsert','{}','')`)
	mustExec(`INSERT INTO sync_apply_deferred (sync_id, entity, payload) VALUES (?, 'observation', '{}')`, labSync)
	mustExec(`INSERT INTO sync_apply_deferred (sync_id, entity, payload) VALUES (?, 'observation', '{}')`, keepSync)
	mustExec(`INSERT INTO prompt_tombstones (sync_id, session_id, project) VALUES ('prompt-gone','s-lab','lab')`)
	mustExec(`INSERT INTO sync_chunks (target_key, chunk_id) VALUES ('local','abc123')`)
	chunksBefore := purgeCount(t, s, "SELECT COUNT(*) FROM sync_chunks")

	res, err := s.Purge(PurgeSelector{Project: "lab", SessionID: "s-lab"}, PurgeOptions{})
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if res.BackupPath == "" {
		t.Fatal("expected backup path")
	}
	if _, err := os.Stat(res.BackupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	d := res.Deleted
	if d.Observations != 2 || d.Prompts != 1 || d.Sessions != 1 || d.Relations != 1 || d.PromptTombstones != 1 || d.SyncMutations != 2 || d.SyncApplyDeferred != 1 {
		t.Fatalf("deleted = %+v", d)
	}
	if got := purgeCount(t, s, "SELECT COUNT(*) FROM observations"); got != 2 {
		t.Fatalf("observations left = %d", got)
	}
	if purgeCount(t, s, "SELECT COUNT(*) FROM sessions WHERE id='s-lab2'") != 1 || purgeCount(t, s, "SELECT COUNT(*) FROM sessions WHERE id='s-keep'") != 1 {
		t.Fatal("other sessions must survive")
	}
	if purgeCount(t, s, "SELECT COUNT(*) FROM memory_relations WHERE sync_id='rel-keep'") != 1 {
		t.Fatal("unrelated relation deleted")
	}
	if purgeCount(t, s, "SELECT COUNT(*) FROM sync_chunks") != chunksBefore {
		t.Fatal("sync_chunks must not be touched")
	}
	if purgeCount(t, s, "SELECT COUNT(*) FROM sync_mutations WHERE project='keep'") != 1 {
		t.Fatal("keep mutation deleted")
	}

	// FTS consistency: purged gone, others still found.
	hits, err := s.Search("zebrafish", SearchOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.SessionID == "s-lab" {
			t.Fatalf("purged observation still searchable: %+v", h)
		}
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 remaining hits, got %d", len(hits))
	}
	if purgeCount(t, s, "SELECT COUNT(*) FROM observations_fts WHERE observations_fts MATCH 'alpha'") != 0 {
		t.Fatal("fts row remains for purged observation")
	}
	if purgeCount(t, s, "SELECT COUNT(*) FROM prompts_fts WHERE prompts_fts MATCH 'quokka'") != 1 {
		t.Fatal("prompts_fts should keep only the other project's prompt")
	}
	if _, err := s.db.Exec(`INSERT INTO observations_fts(observations_fts) VALUES('integrity-check')`); err != nil {
		t.Fatalf("fts integrity: %v", err)
	}
}

func TestPurgeWholeProjectCleansProjectRows(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	for _, q := range []string{
		`INSERT OR IGNORE INTO sync_state (target_key) VALUES ('cloud:lab')`,
		`INSERT OR IGNORE INTO sync_state (target_key) VALUES ('cloud:keep')`,
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, project) VALUES ('cloud:lab','observation','obs-zzz','upsert','{}','lab')`,
		`INSERT OR IGNORE INTO cloud_upgrade_state (project) VALUES ('lab')`,
		`INSERT OR IGNORE INTO cloud_upgrade_state (project) VALUES ('keep')`,
		`INSERT INTO sync_apply_deferred (sync_id, entity, payload) VALUES ('d1','observation','{"project":"lab"}')`,
		`INSERT INTO sync_apply_deferred (sync_id, entity, payload) VALUES ('d2','observation','{"project":"keep"}')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	res, err := s.Purge(PurgeSelector{Project: "Lab"}, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Plan.WholeProject || res.Deleted.SyncState != 1 || res.Deleted.CloudUpgradeState != 1 || res.Deleted.SyncApplyDeferred != 1 {
		t.Fatalf("result = %+v", res.Deleted)
	}
	if purgeCount(t, s, "SELECT COUNT(*) FROM sync_state WHERE target_key='cloud:keep'") != 1 ||
		purgeCount(t, s, "SELECT COUNT(*) FROM cloud_upgrade_state WHERE project='keep'") != 1 ||
		purgeCount(t, s, "SELECT COUNT(*) FROM sync_apply_deferred WHERE sync_id='d2'") != 1 {
		t.Fatal("other project's rows must stay")
	}
	if purgeCount(t, s, "SELECT COUNT(*) FROM sessions WHERE project='lab'") != 0 {
		t.Fatal("lab sessions remain")
	}
}

func TestPurgeNonWholeKeepsProjectRows(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO sync_state (target_key) VALUES ('cloud:lab')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Purge(PurgeSelector{Project: "lab", SessionID: "s-lab"}, PurgeOptions{}); err != nil {
		t.Fatal(err)
	}
	if purgeCount(t, s, "SELECT COUNT(*) FROM sync_state WHERE target_key='cloud:lab'") != 1 {
		t.Fatal("project-level rows must stay for a partial selection")
	}
}

func TestPurgeRefusesPinnedUnlessIncluded(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	id := purgeCount(t, s, "SELECT MIN(id) FROM observations WHERE project='lab'")
	if err := s.PinObservation(int64(id)); err != nil {
		t.Fatal(err)
	}
	before := purgeTables(t, s)
	if _, err := s.Purge(PurgeSelector{Project: "lab"}, PurgeOptions{}); !errors.Is(err, ErrPurgeRefused) || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("expected pinned refusal, got %v", err)
	}
	if after := purgeTables(t, s); after["observations"] != before["observations"] {
		t.Fatal("refusal must delete nothing")
	}
	if _, err := os.Stat(s.cfg.DataDir + "/backups"); err == nil {
		t.Fatal("refusal must not create a backup")
	}
	res, err := s.Purge(PurgeSelector{Project: "lab"}, PurgeOptions{IncludePinned: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted.Observations != 3 {
		t.Fatalf("deleted = %+v", res.Deleted)
	}
}

func TestPurgeRefusesEnrolledProject(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	if err := s.EnrollProject("lab"); err != nil {
		t.Fatal(err)
	}
	for _, sel := range []PurgeSelector{{Project: "lab"}, {SessionID: "s-lab"}, {Since: "2000-01-01"}} {
		if _, err := s.Purge(sel, PurgeOptions{IncludePinned: true}); !errors.Is(err, ErrPurgeRefused) || !strings.Contains(err.Error(), "enrolled") {
			t.Fatalf("selector %+v: expected enrolled refusal, got %v", sel, err)
		}
	}
	if purgeCount(t, s, "SELECT COUNT(*) FROM observations WHERE project='lab'") != 3 {
		t.Fatal("nothing may be deleted")
	}
}

func TestPurgeBackupFailureAborts(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	orig := s.hooks.exec
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "VACUUM INTO") {
			return nil, errors.New("injected backup failure")
		}
		return orig(db, query, args...)
	}
	before := purgeTables(t, s)
	_, err := s.Purge(PurgeSelector{Project: "lab"}, PurgeOptions{})
	if err == nil || !strings.Contains(err.Error(), "backup failed") {
		t.Fatalf("expected backup failure, got %v", err)
	}
	after := purgeTables(t, s)
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("table %s changed despite backup failure", k)
		}
	}
}

func TestPurgeBackupCreatedBeforeDelete(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	res, err := s.Purge(PurgeSelector{Project: "lab"}, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Open the backup file directly and check it still has the purged rows.
	db, err := sql.Open("sqlite", res.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM observations WHERE project='lab'").Scan(&n); err != nil || n != 3 {
		t.Fatalf("backup should hold the 3 pre-purge lab rows, got %d (%v)", n, err)
	}
}

func TestPurgeNothingMatched(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	res, err := s.Purge(PurgeSelector{Project: "ghost"}, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Plan.Empty() || res.BackupPath != "" {
		t.Fatalf("expected empty plan without backup, got %+v", res)
	}
}

func TestPurgeDateNormalisation(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	set := func(id int64, ts string) {
		if _, err := s.db.Exec(`UPDATE observations SET created_at=? WHERE id=?`, ts, id); err != nil {
			t.Fatal(err)
		}
	}
	var ids []int64
	rows, err := s.db.Query(`SELECT id FROM observations WHERE project='lab' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	set(ids[0], "2026-09-10 23:59:59")       // local format, last second of the 10th
	set(ids[1], "2026-09-11T00:00:00Z")      // RFC3339, first second of the 11th
	set(ids[2], "2026-09-10T22:00:00-03:00") // RFC3339 with offset = 2026-09-11 01:00 UTC

	count := func(sel PurgeSelector) int {
		p, err := s.PurgePlan(sel, PurgeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return p.Counts.Observations
	}
	if n := count(PurgeSelector{Project: "lab", Until: "2026-09-10"}); n != 1 {
		t.Fatalf("until 2026-09-10 (inclusive day) = %d, want 1", n)
	}
	if n := count(PurgeSelector{Project: "lab", Since: "2026-09-11"}); n != 2 {
		t.Fatalf("since 2026-09-11 = %d, want 2", n)
	}
	if n := count(PurgeSelector{Project: "lab", Since: "2026-09-11T00:00:00Z", Until: "2026-09-11T00:00:00Z"}); n != 1 {
		t.Fatalf("RFC3339 inclusive point = %d, want 1", n)
	}
	if n := count(PurgeSelector{Project: "lab", Since: "2026-09-10T21:00:00-03:00"}); n != 2 {
		t.Fatalf("offset since = %d, want 2", n)
	}
}

func TestPurgeDateRangeKeepsSessionWithOutsideRows(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	id := purgeCount(t, s, "SELECT MIN(id) FROM observations WHERE session_id='s-lab'")
	if _, err := s.db.Exec(`UPDATE observations SET created_at='2020-01-01 00:00:00' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	// Selecting only the old row must not delete s-lab (it still holds other rows) and must not trip the FK.
	res, err := s.Purge(PurgeSelector{Project: "lab", Until: "2020-12-31"}, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted.Observations != 1 || res.Deleted.Sessions != 0 {
		t.Fatalf("deleted = %+v", res.Deleted)
	}
}

func TestPurgeExportedWarning(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	p, err := s.PurgePlan(PurgeSelector{Project: "lab"}, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) != 0 {
		t.Fatalf("no chunks recorded, no warning expected: %v", p.Warnings)
	}
	if _, err := s.db.Exec(`INSERT INTO sync_chunks (target_key, chunk_id, imported_at) VALUES ('local','c1','2999-01-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	p, err = s.PurgePlan(PurgeSelector{Project: "lab"}, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], ".engram/chunks") || p.ExportedRows == 0 {
		t.Fatalf("expected chunk warning, got %+v", p)
	}
}

func TestPurgeFKOrderWithForeignKeysOn(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d (%v)", fk, err)
	}
	if _, err := s.Purge(PurgeSelector{SessionID: "s-lab"}, PurgeOptions{}); err != nil {
		t.Fatalf("purge under FK enforcement: %v", err)
	}
	if purgeCount(t, s, "SELECT COUNT(*) FROM sessions WHERE id='s-lab'") != 0 {
		t.Fatal("session should be gone")
	}
}

func TestDeleteProjectNormalizesName(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	res, err := s.DeleteProject("  LAB ", true)
	if err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if res.Project != "lab" || res.ObservationsDeleted != 3 {
		t.Fatalf("result = %+v", res)
	}
}

func purgeLabObsIDs(t *testing.T, s *Store) []int64 {
	t.Helper()
	rows, err := s.db.Query(`SELECT id FROM observations WHERE project='lab' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

func TestPurgeUnreadableDatesNeverMatchDateBounds(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	ids := purgeLabObsIDs(t, s)
	for i, ts := range []string{"garbage", "", "2020-01-01 00:00:00"} {
		if _, err := s.db.Exec(`UPDATE observations SET created_at=? WHERE id=?`, ts, ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := s.PurgePlan(PurgeSelector{Project: "lab", Since: "2026-01-01"}, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Counts.Observations != 0 {
		t.Fatalf("since: unreadable dates must not match, got %d observations", plan.Counts.Observations)
	}
	if plan.UnreadableDateRows != 2 {
		t.Fatalf("UnreadableDateRows = %d, want 2", plan.UnreadableDateRows)
	}
	found := false
	for _, w := range plan.Warnings {
		if strings.Contains(w, "2 rows with an unreadable date were not selected by the date filter") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing unreadable-date warning: %v", plan.Warnings)
	}
	plan, err = s.PurgePlan(PurgeSelector{Project: "lab", Until: "2026-12-31"}, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Counts.Observations != 1 {
		t.Fatalf("until: only the readable 2020 row may match, got %d", plan.Counts.Observations)
	}
	// Without a date bound nothing is skipped and no warning is raised.
	plan, err = s.PurgePlan(PurgeSelector{Project: "lab"}, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.UnreadableDateRows != 0 || plan.Counts.Observations != 3 {
		t.Fatalf("no date bound: unreadable=%d obs=%d", plan.UnreadableDateRows, plan.Counts.Observations)
	}
}

func TestPurgeReportsRelationsToUnselectedObservations(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	syncID := func(project string) string {
		var id string
		if err := s.db.QueryRow(`SELECT sync_id FROM observations WHERE project=? ORDER BY id LIMIT 1`, project).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	lab, keep := syncID("lab"), syncID("keep")
	if _, err := s.db.Exec(`INSERT INTO memory_relations (sync_id, source_id, target_id) VALUES ('rel-x', ?, ?)`, lab, keep); err != nil {
		t.Fatal(err)
	}
	plan, err := s.PurgePlan(PurgeSelector{Project: "lab"}, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Counts.Relations != 1 || plan.RelationsToKept != 1 {
		t.Fatalf("relations=%d toKept=%d, want 1/1", plan.Counts.Relations, plan.RelationsToKept)
	}
	res, err := s.Purge(PurgeSelector{Project: "lab"}, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan.RelationsToKept != 1 {
		t.Fatalf("result plan RelationsToKept = %d", res.Plan.RelationsToKept)
	}
	if purgeCount(t, s, `SELECT COUNT(*) FROM memory_relations`) != 0 {
		t.Fatal("relation should be deleted")
	}
	if purgeCount(t, s, `SELECT COUNT(*) FROM observations WHERE project='keep'`) != 1 {
		t.Fatal("kept observation must survive")
	}
}

func TestDeleteProjectCoversLegacyNameVariants(t *testing.T) {
	for _, hard := range []bool{true, false} {
		s := newTestStore(t)
		purgeSeed(t, s)
		// Legacy rows stored un-normalised.
		if _, err := s.db.Exec(`UPDATE sessions SET project='Lab' WHERE id='s-lab2'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE observations SET project='Lab' WHERE session_id='s-lab2'`); err != nil {
			t.Fatal(err)
		}
		res, err := s.DeleteProject("lab", hard)
		if err != nil {
			t.Fatalf("hard=%v: %v", hard, err)
		}
		if res.ObservationsDeleted != 3 {
			t.Fatalf("hard=%v: observations deleted = %d, want 3 (%+v)", hard, res.ObservationsDeleted, res)
		}
		if hard {
			if res.SessionsDeleted != 2 || purgeCount(t, s, `SELECT COUNT(*) FROM observations WHERE LOWER(project)='lab'`) != 0 {
				t.Fatalf("hard delete incomplete: %+v", res)
			}
		} else if purgeCount(t, s, `SELECT COUNT(*) FROM observations WHERE LOWER(project)='lab' AND deleted_at IS NULL`) != 0 {
			t.Fatal("soft delete left live rows")
		}
		if purgeCount(t, s, `SELECT COUNT(*) FROM observations WHERE project='keep'`) != 1 {
			t.Fatal("other project touched")
		}
	}
}

func TestPurgeBackupNameAndPermissions(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	res, err := s.Purge(PurgeSelector{Project: "lab"}, PurgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if b := filepath.Base(res.BackupPath); !strings.HasPrefix(b, "engram-purge-") || !strings.HasSuffix(b, ".db") {
		t.Fatalf("backup name = %q", b)
	}
	if fi, err := os.Stat(res.BackupPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup perms = %v (%v)", fi, err)
	}
	if fi, err := os.Stat(filepath.Dir(res.BackupPath)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("new backup dir perms = %v (%v)", fi, err)
	}
}

func TestPurgeBackupKeepsExistingDirMode(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	dir := filepath.Join(s.cfg.DataDir, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Purge(PurgeSelector{Project: "lab"}, PurgeOptions{}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o755 {
		t.Fatalf("existing dir mode changed to %v", fi.Mode().Perm())
	}
}

func TestPurgeAbortsWhenBackupVerificationFails(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	orig := s.hooks.exec
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		r, err := orig(db, query, args...)
		if err == nil && strings.Contains(query, "VACUUM INTO") {
			// Corrupt the freshly written backup.
			if werr := os.WriteFile(args[0].(string), []byte(strings.Repeat("not a database", 500)), 0o600); werr != nil {
				t.Fatal(werr)
			}
		}
		return r, err
	}
	before := purgeTables(t, s)
	_, err := s.Purge(PurgeSelector{Project: "lab"}, PurgeOptions{})
	if err == nil || !strings.Contains(err.Error(), "backup verification failed") || !strings.Contains(err.Error(), "engram-purge-") {
		t.Fatalf("expected verification failure naming the backup, got %v", err)
	}
	for k, v := range before {
		if got := purgeCount(t, s, "SELECT COUNT(*) FROM "+k); got != v {
			t.Fatalf("table %s changed despite failed verification", k)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(s.cfg.DataDir, "backups", "engram-purge-*.db"))
	if len(matches) != 1 {
		t.Fatalf("backup should be left in place, found %v", matches)
	}
}

func TestVerifyPurgeBackupChecksRowCounts(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	path, err := s.purgeBackup()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPurgeBackup(path, PurgeCounts{Observations: 4, Sessions: 3}); err != nil {
		t.Fatalf("matching counts should verify: %v", err)
	}
	if err := verifyPurgeBackup(path, PurgeCounts{Observations: 5}); err == nil || !strings.Contains(err.Error(), "observations") {
		t.Fatalf("too-high planned count must fail, got %v", err)
	}
}

func TestPurgeDeleteFailureNamesBackup(t *testing.T) {
	s := newTestStore(t)
	purgeSeed(t, s)
	orig := s.hooks.exec
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		if strings.HasPrefix(query, "DELETE FROM observations") {
			return nil, errors.New("injected delete failure")
		}
		return orig(db, query, args...)
	}
	before := purgeTables(t, s)
	_, err := s.Purge(PurgeSelector{Project: "lab"}, PurgeOptions{})
	if err == nil || !strings.Contains(err.Error(), "the backup at ") || !strings.Contains(err.Error(), "holds the untouched data") {
		t.Fatalf("expected backup-naming error, got %v", err)
	}
	for k, v := range before {
		if got := purgeCount(t, s, "SELECT COUNT(*) FROM "+k); got != v {
			t.Fatalf("table %s changed after failed delete", k)
		}
	}
}
