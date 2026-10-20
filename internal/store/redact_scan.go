package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/redact"
)

// RedactScanTable is the per-table part of a scan report: counts only.
type RedactScanTable struct {
	Table       string         `json:"table"`
	Scanned     int            `json:"scanned"`
	RowsChanged int            `json:"rows_changed"`
	Fields      map[string]int `json:"fields_changed"`
}

// RedactScanCollisions counts what the redaction would make collide. The scan
// never merges anything: these are reported so the person knows (accepted
// limit, see docs).
type RedactScanCollisions struct {
	// TopicKey: observations that would share (project, scope, topic_key) after
	// the rewrite, where at least one of them was changed by it.
	TopicKey int `json:"topic_key_rows"`
	// Identical: observations that would become equal in project, scope, type,
	// title and content, where at least one of them was changed by it.
	Identical int `json:"identical_rows"`
	// ProjectRenames: project names that would be renamed onto a name that
	// already exists or that another renamed name also maps to.
	ProjectRenames int `json:"project_renames"`
}

func (c RedactScanCollisions) Any() bool {
	return c.TopicKey+c.Identical+c.ProjectRenames > 0
}

type RedactScanResult struct {
	Applied     bool                 `json:"applied"`
	Tables      []RedactScanTable    `json:"tables"`
	RowsChanged int                  `json:"rows_changed"`
	Collisions  RedactScanCollisions `json:"collisions"`
	// LegacySyncRows counts rows of the tables the removed cloud sync left
	// behind. The scan does not rewrite them: `engram purge --sync-leftovers`
	// deletes them.
	LegacySyncRows map[string]int `json:"legacy_sync_rows,omitempty"`
	// OldValuesRemain is true when the WAL could not be truncated (another
	// connection holds a read transaction): engram.db and engram.db-wal may still
	// hold the old values until those readers close.
	OldValuesRemain bool     `json:"old_values_remain,omitempty"`
	BackupPath      string   `json:"backup_path,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
}

type scanColumn struct {
	name string
	fn   func(string) string
}

type scanTable struct {
	name  string
	cols  []scanColumn
	extra []string // read-only columns, for the collision report
}

func scanText(v string) string { return sanitizeStored(v, 0) }

func scanTopicKey(v string) string {
	if r := sanitizeStored(v, 0); r != v {
		return normalizeTopicKey(r)
	}
	return v
}

func scanProject(v string) string {
	if redact.Redact(v) == v {
		return v
	}
	n, _ := NormalizeProject(v)
	return n
}

var redactScanTables = []scanTable{
	{name: "sessions", cols: []scanColumn{{"project", scanProject}, {"directory", scanText}, {"summary", scanText}}},
	{name: "observations", cols: []scanColumn{{"type", scanText}, {"title", scanText}, {"content", scanText}, {"tool_name", scanText}, {"project", scanProject}, {"topic_key", scanTopicKey}}, extra: []string{"scope", "deleted_at"}},
	{name: "user_prompts", cols: []scanColumn{{"content", scanText}, {"project", scanProject}}},
	{name: "prompt_tombstones", cols: []scanColumn{{"project", scanProject}}},
	{name: "memory_relations", cols: []scanColumn{{"reason", scanText}, {"evidence", scanText}, {"marked_by_actor", scanText}, {"marked_by_kind", scanText}, {"marked_by_model", scanText}, {"session_id", scanText}}},
}

// legacySyncTables are what the removed cloud sync left; reported, not rewritten.
var legacySyncTables = []string{"sync_mutations", "sync_apply_deferred", "sync_enrolled_projects"}

type scanQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

type scanUpdate struct {
	table string
	rowid int64
	cols  []string
	args  []any
}

type collisionGroup struct {
	n       int
	changed bool
}

// collisionTracker finds what the rewrite would make collide.
type collisionTracker struct {
	topic    map[string]*collisionGroup
	ident    map[string]*collisionGroup
	projects map[string]bool   // every project name as stored now
	renames  map[string]string // old name -> new name, for the changed ones
}

func newCollisionTracker() *collisionTracker {
	return &collisionTracker{
		topic: map[string]*collisionGroup{}, ident: map[string]*collisionGroup{},
		projects: map[string]bool{}, renames: map[string]string{},
	}
}

func (c *collisionTracker) group(m map[string]*collisionGroup, key string, changed bool) {
	g := m[key]
	if g == nil {
		g = &collisionGroup{}
		m[key] = g
	}
	g.n++
	g.changed = g.changed || changed
}

func (c *collisionTracker) project(old, nv string) {
	if old == "" {
		return
	}
	c.projects[old] = true
	if nv != old {
		c.renames[old] = nv
	}
}

func (c *collisionTracker) result() RedactScanCollisions {
	var r RedactScanCollisions
	for _, g := range c.topic {
		if g.n > 1 && g.changed {
			r.TopicKey += g.n
		}
	}
	for _, g := range c.ident {
		if g.n > 1 && g.changed {
			r.Identical += g.n
		}
	}
	targets := map[string]int{}
	for _, nv := range c.renames {
		targets[nv]++
	}
	for _, nv := range c.renames {
		if c.projects[nv] || targets[nv] > 1 {
			r.ProjectRenames++
		}
	}
	return r
}

// redactScanCollect reads every row of the scanned tables and returns the
// updates the redactor would make, with the counts and the collisions.
func redactScanCollect(q scanQuerier) ([]RedactScanTable, []scanUpdate, RedactScanCollisions, error) {
	var report []RedactScanTable
	var updates []scanUpdate
	track := newCollisionTracker()
	for _, t := range redactScanTables {
		names := make([]string, 0, len(t.cols)+len(t.extra))
		for _, c := range t.cols {
			names = append(names, c.name)
		}
		names = append(names, t.extra...)
		rows, err := q.Query(`SELECT rowid, ` + strings.Join(names, ", ") + ` FROM ` + t.name)
		if err != nil {
			return nil, nil, RedactScanCollisions{}, fmt.Errorf("scan %s: %w", t.name, err)
		}
		rep := RedactScanTable{Table: t.name, Fields: map[string]int{}}
		for rows.Next() {
			vals := make([]sql.NullString, len(names))
			dest := make([]any, 0, len(names)+1)
			var rowid int64
			dest = append(dest, &rowid)
			for i := range vals {
				dest = append(dest, &vals[i])
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return nil, nil, RedactScanCollisions{}, fmt.Errorf("scan %s: %w", t.name, err)
			}
			rep.Scanned++
			up := scanUpdate{table: t.name, rowid: rowid}
			newVals := make(map[string]string, len(t.cols))
			changedCol := map[string]bool{}
			for i, c := range t.cols {
				newVals[c.name] = vals[i].String
				if !vals[i].Valid || vals[i].String == "" {
					continue
				}
				if nv := c.fn(vals[i].String); nv != vals[i].String {
					newVals[c.name] = nv
					changedCol[c.name] = true
					up.cols = append(up.cols, c.name)
					up.args = append(up.args, nv)
					rep.Fields[c.name]++
					if t.name == "observations" && c.name == "content" {
						up.cols = append(up.cols, "normalized_hash")
						up.args = append(up.args, hashNormalized(nv))
					}
				}
			}
			if nv, ok := newVals["project"]; ok {
				for i, c := range t.cols {
					if c.name == "project" {
						track.project(vals[i].String, nv)
					}
				}
			}
			if t.name == "observations" && !vals[len(t.cols)+1].Valid { // not soft-deleted
				scope := vals[len(t.cols)].String
				anyChanged := len(changedCol) > 0
				if tk := newVals["topic_key"]; tk != "" {
					track.group(track.topic, newVals["project"]+"\x00"+scope+"\x00"+tk, changedCol["topic_key"] || changedCol["project"])
				}
				track.group(track.ident, strings.Join([]string{newVals["project"], scope, newVals["type"], newVals["title"], hashNormalized(newVals["content"])}, "\x00"), anyChanged)
			}
			if len(up.cols) > 0 {
				rep.RowsChanged++
				updates = append(updates, up)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, RedactScanCollisions{}, fmt.Errorf("scan %s: %w", t.name, err)
		}
		rows.Close()
		report = append(report, rep)
	}
	return report, updates, track.result(), nil
}

func (r *RedactScanResult) fill(report []RedactScanTable, coll RedactScanCollisions) {
	r.Tables = report
	r.Collisions = coll
	r.RowsChanged = 0
	for _, t := range report {
		r.RowsChanged += t.RowsChanged
	}
}

// legacySyncRows counts the rows of the legacy sync tables that have any.
func legacySyncRows(q scanQuerier) map[string]int {
	out := map[string]int{}
	for _, name := range legacySyncTables {
		rows, err := q.Query(`SELECT COUNT(*) FROM ` + name)
		if err != nil {
			continue
		}
		var n int
		if rows.Next() {
			_ = rows.Scan(&n)
		}
		rows.Close()
		if n > 0 {
			out[name] = n
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RedactScan re-redacts what was stored before the redactor existed (before
// 1.22.0) or before it covered a field (type, directory and project before
// 1.22.1). It applies to every stored row exactly the transformation the write
// paths apply now, so a second run finds nothing (idempotent).
//
// It reports counts only: rows scanned, rows changed and changed fields per
// table, never a value. Without apply nothing is written. With apply it takes
// a verified 0600 backup first (like purge), then rewrites the changed rows in
// one transaction with secure_delete on, rebuilds the FTS indexes, VACUUMs and
// truncates the WAL, so the old values are not left recoverable in free pages,
// FTS shadow tables or the WAL. The backup still holds them: it is kept and its
// path reported, never deleted automatically.
//
// Per field:
//   - free text (title, content, summary, prompt content, reason, evidence,
//     tool_name, type, directory): sanitizeStored with no length limit, so no
//     row is truncated by the scan (text beyond the detection cap is dropped,
//     as on every write).
//   - topic_key: sanitized, and normalized only when the sanitizer changed it.
//   - project (sessions, observations, user_prompts, prompt_tombstones): a name
//     the redactor changes is canonicalised like a new write (lowercase,
//     placeholder lowercased); one it leaves alone is untouched, so a legacy
//     mixed-case name keeps its identity.
//   - observations.normalized_hash is recomputed for a changed content.
//
// The legacy sync tables are only counted (LegacySyncRows). Nothing is merged:
// rows that become identical, share a topic_key or land on an existing project
// name are counted in Collisions.
func (s *Store) RedactScan(apply bool) (*RedactScanResult, error) {
	res := &RedactScanResult{}
	report, updates, coll, err := redactScanCollect(s.db)
	if err != nil {
		return nil, err
	}
	res.fill(report, coll)
	res.LegacySyncRows = legacySyncRows(s.db)
	if !apply || len(updates) == 0 {
		return res, nil
	}

	backup, err := s.backupInto("engram-redact-")
	if err != nil {
		return nil, fmt.Errorf("redact scan aborted, nothing changed: backup failed: %w", err)
	}
	res.BackupPath = backup
	var counts PurgeCounts
	for _, t := range report {
		switch t.Table {
		case "sessions":
			counts.Sessions = t.Scanned
		case "observations":
			counts.Observations = t.Scanned
		case "user_prompts":
			counts.Prompts = t.Scanned
		case "memory_relations":
			counts.Relations = t.Scanned
		case "prompt_tombstones":
			counts.PromptTombstones = t.Scanned
		}
	}
	if err := verifyPurgeBackup(backup, counts); err != nil {
		return nil, fmt.Errorf("redact scan aborted, nothing changed: backup verification failed (the backup file was left at %s): %w", backup, err)
	}

	// Freed pages are zeroed (the old values must not survive in them). The
	// store holds one connection, so the setting covers every statement below.
	if _, err := s.execHook(s.db, `PRAGMA secure_delete = ON`); err != nil {
		res.Warnings = append(res.Warnings, "secure_delete could not be enabled: "+err.Error())
	}
	defer func() { _, _ = s.execHook(s.db, `PRAGMA secure_delete = OFF`) }()

	obsChanged, promptsChanged := false, false
	err = s.withTx(func(tx *sql.Tx) error {
		// Recompute inside the write transaction so the rewrite is current.
		fresh, ups, freshColl, err := redactScanCollect(tx)
		if err != nil {
			return err
		}
		for _, u := range ups {
			set := make([]string, len(u.cols))
			for i, c := range u.cols {
				set[i] = c + " = ?"
			}
			args := append(append([]any{}, u.args...), u.rowid)
			if _, err := s.execHook(tx, `UPDATE `+u.table+` SET `+strings.Join(set, ", ")+` WHERE rowid = ?`, args...); err != nil {
				return fmt.Errorf("redact scan: update %s: %w", u.table, err)
			}
			switch u.table {
			case "observations":
				obsChanged = true
			case "user_prompts":
				promptsChanged = true
			}
		}
		res.fill(fresh, freshColl)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("redact scan failed, nothing was changed; the backup at %s holds the untouched data: %w", backup, err)
	}
	res.Applied = true
	if obsChanged {
		if _, err := s.execHook(s.db, `INSERT INTO observations_fts(observations_fts) VALUES('rebuild')`); err != nil {
			res.Warnings = append(res.Warnings, "FTS rebuild failed: "+err.Error())
		}
	}
	if promptsChanged {
		if _, err := s.execHook(s.db, `INSERT INTO prompts_fts(prompts_fts) VALUES('rebuild')`); err != nil {
			res.Warnings = append(res.Warnings, "prompts FTS rebuild failed: "+err.Error())
		}
	}
	// Rewrite the file so no free page keeps an old value, then empty the WAL.
	if _, err := s.execHook(s.db, `VACUUM`); err != nil {
		res.Warnings = append(res.Warnings, "VACUUM failed, so old values may remain in the free pages of engram.db (stop engram serve and engram mcp and run the scan again, or VACUUM it yourself): "+err.Error())
	}
	if busy, err := s.truncateWAL(); err != nil {
		res.Warnings = append(res.Warnings, "WAL checkpoint failed, so old values may remain in engram.db-wal: "+err.Error())
		res.OldValuesRemain = true
	} else if busy {
		res.OldValuesRemain = true
		res.Warnings = append(res.Warnings, "THE OLD VALUES ARE STILL IN THE DATABASE FILES: the WAL could not be truncated because another process holds the database open for reading (engram serve, engram mcp or another reader). Stop it and run `engram redact --scan --yes` again (or checkpoint the WAL yourself) before treating the data as clean.")
	}
	return res, nil
}

// walRetry is how long truncateWAL keeps retrying a busy checkpoint; tests shorten it.
var walRetry = 5 * time.Second

// truncateWAL runs PRAGMA wal_checkpoint(TRUNCATE) and reads its result row
// (busy, log, checkpointed): busy=1 means a reader kept it from finishing and the
// WAL still holds the pages, with no error. It retries with a short backoff for
// walRetry and reports whether it was still busy.
func (s *Store) truncateWAL() (busy bool, err error) {
	deadline := time.Now().Add(walRetry)
	wait := 25 * time.Millisecond
	for {
		var b, logFrames, done int
		if err := s.db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&b, &logFrames, &done); err != nil {
			return false, err
		}
		if b == 0 {
			return false, nil
		}
		if time.Now().After(deadline) {
			return true, nil
		}
		time.Sleep(wait)
		if wait < 400*time.Millisecond {
			wait *= 2
		}
	}
}

// FieldNames returns the changed field names of a table in a stable order.
func (t RedactScanTable) FieldNames() []string {
	names := make([]string, 0, len(t.Fields))
	for n := range t.Fields {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
