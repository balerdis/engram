package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/redact"
)

// RedactScanTable is the per-table part of a scan report: counts only.
type RedactScanTable struct {
	Table       string         `json:"table"`
	Scanned     int            `json:"scanned"`
	RowsChanged int            `json:"rows_changed"`
	Fields      map[string]int `json:"fields_changed"`
}

type RedactScanResult struct {
	Applied     bool              `json:"applied"`
	Tables      []RedactScanTable `json:"tables"`
	RowsChanged int               `json:"rows_changed"`
	BackupPath  string            `json:"backup_path,omitempty"`
	Warnings    []string          `json:"warnings,omitempty"`
}

type scanColumn struct {
	name string
	fn   func(string) string
}

type scanTable struct {
	name string
	cols []scanColumn
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
	{"sessions", []scanColumn{{"project", scanProject}, {"directory", scanText}, {"summary", scanText}}},
	{"observations", []scanColumn{{"type", scanText}, {"title", scanText}, {"content", scanText}, {"tool_name", scanText}, {"project", scanProject}, {"topic_key", scanTopicKey}}},
	{"user_prompts", []scanColumn{{"content", scanText}, {"project", scanProject}}},
	{"memory_relations", []scanColumn{{"reason", scanText}, {"evidence", scanText}}},
}

type scanQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

type scanUpdate struct {
	table string
	rowid int64
	cols  []string
	args  []any
}

// redactScanCollect reads every row of the scanned tables and returns the
// updates the redactor would make, with the counts.
func redactScanCollect(q scanQuerier) ([]RedactScanTable, []scanUpdate, error) {
	var report []RedactScanTable
	var updates []scanUpdate
	for _, t := range redactScanTables {
		names := make([]string, len(t.cols))
		for i, c := range t.cols {
			names[i] = c.name
		}
		rows, err := q.Query(`SELECT rowid, ` + strings.Join(names, ", ") + ` FROM ` + t.name)
		if err != nil {
			return nil, nil, fmt.Errorf("scan %s: %w", t.name, err)
		}
		rep := RedactScanTable{Table: t.name, Fields: map[string]int{}}
		for rows.Next() {
			vals := make([]sql.NullString, len(t.cols))
			dest := make([]any, 0, len(t.cols)+1)
			var rowid int64
			dest = append(dest, &rowid)
			for i := range vals {
				dest = append(dest, &vals[i])
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return nil, nil, fmt.Errorf("scan %s: %w", t.name, err)
			}
			rep.Scanned++
			up := scanUpdate{table: t.name, rowid: rowid}
			for i, c := range t.cols {
				if !vals[i].Valid || vals[i].String == "" {
					continue
				}
				if nv := c.fn(vals[i].String); nv != vals[i].String {
					up.cols = append(up.cols, c.name)
					up.args = append(up.args, nv)
					rep.Fields[c.name]++
					if t.name == "observations" && c.name == "content" {
						up.cols = append(up.cols, "normalized_hash")
						up.args = append(up.args, hashNormalized(nv))
					}
				}
			}
			if len(up.cols) > 0 {
				rep.RowsChanged++
				updates = append(updates, up)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("scan %s: %w", t.name, err)
		}
		rows.Close()
		report = append(report, rep)
	}
	return report, updates, nil
}

func (r *RedactScanResult) fill(report []RedactScanTable) {
	r.Tables = report
	r.RowsChanged = 0
	for _, t := range report {
		r.RowsChanged += t.RowsChanged
	}
}

// RedactScan re-redacts what was stored before the redactor existed (before
// 1.22.0) or before it covered a field (type, directory and project before
// 1.22.1). It applies to every stored row exactly the transformation the write
// paths apply now, so a second run finds nothing (idempotent).
//
// It reports counts only: rows scanned, rows changed and changed fields per
// table, never a value. Without apply nothing is written. With apply it takes
// a verified 0600 backup first (like purge), then rewrites the changed rows in
// one transaction and keeps the FTS index in sync (the observations triggers
// maintain it row by row; the index is rebuilt at the end as well).
//
// Per field:
//   - free text (title, content, summary, prompt content, reason, evidence,
//     tool_name, type, directory): sanitizeStored with no length limit, so no
//     row is truncated by the scan (text beyond the detection cap is dropped,
//     as on every write).
//   - topic_key: sanitized, and normalized only when the sanitizer changed it.
//   - project: a name the redactor changes is canonicalised like a new write
//     (lowercase, placeholder lowercased); one it leaves alone is untouched, so
//     a legacy mixed-case name keeps its identity.
//   - observations.normalized_hash is recomputed for a changed content.
func (s *Store) RedactScan(apply bool) (*RedactScanResult, error) {
	res := &RedactScanResult{}
	report, updates, err := redactScanCollect(s.db)
	if err != nil {
		return nil, err
	}
	res.fill(report)
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
		}
	}
	if err := verifyPurgeBackup(backup, counts); err != nil {
		return nil, fmt.Errorf("redact scan aborted, nothing changed: backup verification failed (the backup file was left at %s): %w", backup, err)
	}

	obsChanged := false
	err = s.withTx(func(tx *sql.Tx) error {
		// Recompute inside the write transaction so the rewrite is current.
		fresh, ups, err := redactScanCollect(tx)
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
			if u.table == "observations" {
				obsChanged = true
			}
		}
		res.fill(fresh)
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
	if _, err := s.execHook(s.db, `PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		res.Warnings = append(res.Warnings, "WAL checkpoint failed: "+err.Error())
	}
	return res, nil
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
