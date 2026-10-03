package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrPurgeRefused is wrapped by every refusal returned from PurgePlan/Purge
// (pinned observations, cloud-enrolled projects, empty selector). Nothing has
// been deleted when it is returned.
var ErrPurgeRefused = errors.New("purge refused")

// PurgeSelector picks the rows to purge. All non-empty fields are combined
// with AND; at least one is required.
//
// Since is inclusive. Until is inclusive too: a date-only value (YYYY-MM-DD)
// covers the whole UTC day, an RFC3339 value is compared to the second.
type PurgeSelector struct {
	Project   string `json:"project,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Since     string `json:"since,omitempty"`
	Until     string `json:"until,omitempty"`
}

// PurgeOptions tunes what a purge may touch.
type PurgeOptions struct {
	// IncludePinned allows pinned observations to be part of the selection.
	IncludePinned bool
}

// PurgeCounts holds per-table row counts.
type PurgeCounts struct {
	Sessions          int `json:"sessions"`
	Observations      int `json:"observations"`
	SoftDeleted       int `json:"observations_soft_deleted"`
	Pinned            int `json:"observations_pinned"`
	Prompts           int `json:"prompts"`
	Relations         int `json:"memory_relations"`
	PromptTombstones  int `json:"prompt_tombstones"`
	SyncMutations     int `json:"sync_mutations"`
	SyncApplyDeferred int `json:"sync_apply_deferred"`
	SyncState         int `json:"sync_state"`
	SyncEnrolled      int `json:"sync_enrolled_projects"`
	CloudUpgradeState int `json:"cloud_upgrade_state"`
}

// PurgeSample is one sample row shown in the preview.
type PurgeSample struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"` // "observation" or "prompt"
	Project   string `json:"project,omitempty"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
}

// PurgePlan describes what a purge would delete. Building it writes nothing.
type PurgePlan struct {
	Selector     PurgeSelector `json:"selector"`
	WholeProject bool          `json:"whole_project"`
	Projects     []string      `json:"projects"`
	Counts       PurgeCounts   `json:"counts"`
	Samples      []PurgeSample `json:"samples"`
	// ExportedRows counts selected sessions/observations/prompts that predate
	// the newest recorded sync chunk and so may live in .engram/chunks files.
	ExportedRows int      `json:"possibly_exported_rows"`
	Warnings     []string `json:"warnings"`
}

// Empty reports whether the selection matched nothing at all.
func (p *PurgePlan) Empty() bool {
	c := p.Counts
	return c.Sessions == 0 && c.Observations == 0 && c.Prompts == 0 && c.Relations == 0 &&
		c.PromptTombstones == 0 && c.SyncMutations == 0 && c.SyncApplyDeferred == 0 &&
		c.SyncState == 0 && c.SyncEnrolled == 0 && c.CloudUpgradeState == 0
}

// PurgeResult is the outcome of an executed purge.
type PurgeResult struct {
	Plan       *PurgePlan  `json:"plan"`
	BackupPath string      `json:"backup_path,omitempty"`
	Deleted    PurgeCounts `json:"deleted"`
	Warnings   []string    `json:"warnings"`
}

const purgeTSLayout = "2006-01-02 15:04:05"

// ParsePurgeBound parses a --since/--until value (YYYY-MM-DD or RFC3339).
// It returns the normalised UTC timestamp and whether the value was date-only.
func ParsePurgeBound(value string) (ts string, dateOnly bool, err error) {
	v := strings.TrimSpace(value)
	if t, perr := time.Parse("2006-01-02", v); perr == nil {
		return t.UTC().Format(purgeTSLayout), true, nil
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, purgeTSLayout} {
		if t, perr := time.Parse(layout, v); perr == nil {
			return t.UTC().Format(purgeTSLayout), false, nil
		}
	}
	return "", false, fmt.Errorf("invalid date %q: use YYYY-MM-DD or RFC3339", value)
}

type purgeWhere struct {
	sinceTS  string
	untilTS  string // exclusive when untilExcl
	untilEx  bool
	variants []string
}

func (s *Store) purgeResolve(tx *sql.Tx, sel *PurgeSelector) (*purgeWhere, error) {
	sel.Project = strings.TrimSpace(sel.Project)
	sel.SessionID = strings.TrimSpace(sel.SessionID)
	sel.Since = strings.TrimSpace(sel.Since)
	sel.Until = strings.TrimSpace(sel.Until)
	if sel.Project == "" && sel.SessionID == "" && sel.Since == "" && sel.Until == "" {
		return nil, fmt.Errorf("%w: at least one selector (project, session, since, until) is required", ErrPurgeRefused)
	}
	w := &purgeWhere{}
	if sel.Since != "" {
		ts, _, err := ParsePurgeBound(sel.Since)
		if err != nil {
			return nil, err
		}
		w.sinceTS = ts
	}
	if sel.Until != "" {
		ts, dateOnly, err := ParsePurgeBound(sel.Until)
		if err != nil {
			return nil, err
		}
		w.untilTS = ts
		if dateOnly {
			t, _ := time.Parse(purgeTSLayout, ts)
			w.untilTS = t.Add(24 * time.Hour).Format(purgeTSLayout)
			w.untilEx = true
		}
	}
	if sel.Project != "" {
		norm, _ := NormalizeProject(sel.Project)
		sel.Project = norm
		seen := map[string]bool{norm: true}
		w.variants = []string{norm}
		rows, err := tx.Query(`SELECT project FROM sessions UNION SELECT project FROM observations UNION SELECT project FROM user_prompts`)
		if err != nil {
			return nil, fmt.Errorf("purge: list projects: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var p sql.NullString
			if err := rows.Scan(&p); err != nil {
				return nil, err
			}
			if !p.Valid || p.String == "" {
				continue
			}
			if n, _ := NormalizeProject(p.String); n == norm && !seen[p.String] {
				seen[p.String] = true
				w.variants = append(w.variants, p.String)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return w, nil
}

func purgePlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func purgeArgs(vals []string) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		out[i] = v
	}
	return out
}

// clauses builds the AND-ed predicate for one row kind.
func (w *purgeWhere) clauses(sel PurgeSelector, eff, session, ts string) (string, []any) {
	var parts []string
	var args []any
	if len(w.variants) > 0 {
		parts = append(parts, eff+" IN ("+purgePlaceholders(len(w.variants))+")")
		args = append(args, purgeArgs(w.variants)...)
	}
	if sel.SessionID != "" {
		parts = append(parts, session+" = ?")
		args = append(args, sel.SessionID)
	}
	norm := "COALESCE(strftime('%Y-%m-%d %H:%M:%S', " + ts + "), " + ts + ")"
	if w.sinceTS != "" {
		parts = append(parts, norm+" >= ?")
		args = append(args, w.sinceTS)
	}
	if w.untilTS != "" {
		op := "<="
		if w.untilEx {
			op = "<"
		}
		parts = append(parts, norm+" "+op+" ?")
		args = append(args, w.untilTS)
	}
	return strings.Join(parts, " AND "), args
}

type purgeStep struct {
	table string
	cond  string
	args  []any
	set   func(*PurgeCounts, int)
}

// purgeBuild fills the temp id tables and returns the plan plus the ordered
// delete steps. It must run inside tx; it writes only to TEMP tables.
func (s *Store) purgeBuild(tx *sql.Tx, sel PurgeSelector, opts PurgeOptions) (*PurgePlan, []purgeStep, error) {
	w, err := s.purgeResolve(tx, &sel)
	if err != nil {
		return nil, nil, err
	}
	whole := sel.Project != "" && sel.SessionID == "" && sel.Since == "" && sel.Until == ""
	plan := &PurgePlan{Selector: sel, WholeProject: whole, Projects: []string{}, Samples: []PurgeSample{}, Warnings: []string{}}

	for _, t := range []string{"purge_obs", "purge_prompts", "purge_sessions", "purge_rel"} {
		if _, err := tx.Exec(`DROP TABLE IF EXISTS temp.` + t); err != nil {
			return nil, nil, err
		}
	}
	for _, ddl := range []string{
		`CREATE TEMP TABLE purge_obs (id INTEGER PRIMARY KEY, sync_id TEXT)`,
		`CREATE TEMP TABLE purge_prompts (id INTEGER PRIMARY KEY, sync_id TEXT)`,
		`CREATE TEMP TABLE purge_sessions (id TEXT PRIMARY KEY)`,
		`CREATE TEMP TABLE purge_rel (sync_id TEXT PRIMARY KEY)`,
	} {
		if _, err := tx.Exec(ddl); err != nil {
			return nil, nil, fmt.Errorf("purge: temp tables: %w", err)
		}
	}

	obsEff := `COALESCE(NULLIF(o.project,''), (SELECT x.project FROM sessions x WHERE x.id = o.session_id), '')`
	cond, args := w.clauses(sel, obsEff, "o.session_id", "o.created_at")
	if _, err := tx.Exec(`INSERT INTO purge_obs SELECT o.id, o.sync_id FROM observations o WHERE `+cond, args...); err != nil {
		return nil, nil, fmt.Errorf("purge: select observations: %w", err)
	}
	prEff := `COALESCE(NULLIF(p.project,''), (SELECT x.project FROM sessions x WHERE x.id = p.session_id), '')`
	cond, args = w.clauses(sel, prEff, "p.session_id", "p.created_at")
	if _, err := tx.Exec(`INSERT INTO purge_prompts SELECT p.id, p.sync_id FROM user_prompts p WHERE `+cond, args...); err != nil {
		return nil, nil, fmt.Errorf("purge: select prompts: %w", err)
	}
	// A session goes only when it matches and nothing outside the selection
	// still references it (keeps the FK intact for date-range selections).
	cond, args = w.clauses(sel, "ses.project", "ses.id", "ses.started_at")
	if _, err := tx.Exec(`INSERT INTO purge_sessions SELECT ses.id FROM sessions ses WHERE `+cond+`
		AND NOT EXISTS (SELECT 1 FROM observations o WHERE o.session_id = ses.id AND o.id NOT IN (SELECT id FROM purge_obs))
		AND NOT EXISTS (SELECT 1 FROM user_prompts p WHERE p.session_id = ses.id AND p.id NOT IN (SELECT id FROM purge_prompts))`, args...); err != nil {
		return nil, nil, fmt.Errorf("purge: select sessions: %w", err)
	}

	relCond := `source_id IN (SELECT sync_id FROM purge_obs) OR target_id IN (SELECT sync_id FROM purge_obs) OR session_id IN (SELECT id FROM purge_sessions)`
	if _, err := tx.Exec(`INSERT OR IGNORE INTO purge_rel SELECT sync_id FROM memory_relations WHERE ` + relCond); err != nil {
		return nil, nil, fmt.Errorf("purge: select relations: %w", err)
	}

	// Matched projects, for display and the enrolment refusal.
	projSet := map[string]bool{}
	projRows, err := tx.Query(`SELECT DISTINCT ` + obsEff + ` FROM observations o WHERE o.id IN (SELECT id FROM purge_obs)
		UNION SELECT DISTINCT ` + prEff + ` FROM user_prompts p WHERE p.id IN (SELECT id FROM purge_prompts)
		UNION SELECT project FROM sessions WHERE id IN (SELECT id FROM purge_sessions)`)
	if err != nil {
		return nil, nil, fmt.Errorf("purge: matched projects: %w", err)
	}
	for projRows.Next() {
		var p sql.NullString
		if err := projRows.Scan(&p); err != nil {
			projRows.Close()
			return nil, nil, err
		}
		if p.Valid && p.String != "" {
			n, _ := NormalizeProject(p.String)
			projSet[n] = true
		}
	}
	projRows.Close()
	for p := range projSet {
		plan.Projects = append(plan.Projects, p)
	}
	sortStrings(plan.Projects)

	// Refusals.
	if err := s.purgeCount(tx, `SELECT COUNT(*) FROM observations WHERE pinned = 1 AND id IN (SELECT id FROM purge_obs)`, nil, &plan.Counts.Pinned); err != nil {
		return nil, nil, err
	}
	if plan.Counts.Pinned > 0 && !opts.IncludePinned {
		return nil, nil, fmt.Errorf("%w: the selection includes %d pinned observation(s); pass --include-pinned to delete them", ErrPurgeRefused, plan.Counts.Pinned)
	}
	check := append([]string{}, plan.Projects...)
	check = append(check, w.variants...)
	if len(check) > 0 {
		var enrolled []string
		rows, err := tx.Query(`SELECT project FROM sync_enrolled_projects WHERE project IN (`+purgePlaceholders(len(check))+`)`, purgeArgs(check)...)
		if err != nil {
			return nil, nil, fmt.Errorf("purge: check enrolment: %w", err)
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, nil, err
			}
			enrolled = append(enrolled, p)
		}
		rows.Close()
		if len(enrolled) > 0 {
			return nil, nil, fmt.Errorf("%w: project %q is enrolled in cloud sync; purge never touches enrolled projects", ErrPurgeRefused, enrolled[0])
		}
	}

	// Delete steps in FK-safe order.
	steps := []purgeStep{
		{"memory_relations", relCond, nil, func(c *PurgeCounts, n int) { c.Relations = n }},
		{"user_prompts", `id IN (SELECT id FROM purge_prompts)`, nil, func(c *PurgeCounts, n int) { c.Prompts = n }},
	}
	tomb := `sync_id IN (SELECT sync_id FROM purge_prompts) OR session_id IN (SELECT id FROM purge_sessions)`
	mut := `(entity = 'observation' AND entity_key IN (SELECT sync_id FROM purge_obs))
		OR (entity = 'prompt' AND entity_key IN (SELECT sync_id FROM purge_prompts))
		OR (entity = 'session' AND entity_key IN (SELECT id FROM purge_sessions))
		OR (entity = 'relation' AND entity_key IN (SELECT sync_id FROM purge_rel))`
	def := `sync_id IN (SELECT sync_id FROM purge_obs) OR sync_id IN (SELECT sync_id FROM purge_prompts)
		OR sync_id IN (SELECT id FROM purge_sessions) OR sync_id IN (SELECT sync_id FROM purge_rel)`
	var wholeArgs []any
	if whole {
		in := purgePlaceholders(len(w.variants))
		tomb += ` OR project IN (` + in + `)`
		mut += ` OR project IN (` + in + `)`
		def += ` OR (json_valid(payload) AND json_extract(payload, '$.project') IN (` + in + `))`
		wholeArgs = purgeArgs(w.variants)
		cloudKeys := make([]string, len(w.variants))
		for i, v := range w.variants {
			cloudKeys[i] = "cloud:" + v
		}
		mut += ` OR target_key IN (` + in + `)`
		steps = append(steps,
			purgeStep{"prompt_tombstones", tomb, wholeArgs, func(c *PurgeCounts, n int) { c.PromptTombstones = n }},
			purgeStep{"sync_mutations", mut, append(append([]any{}, wholeArgs...), purgeArgs(cloudKeys)...), func(c *PurgeCounts, n int) { c.SyncMutations = n }},
			purgeStep{"sync_apply_deferred", def, wholeArgs, func(c *PurgeCounts, n int) { c.SyncApplyDeferred = n }},
		)
	} else {
		steps = append(steps,
			purgeStep{"prompt_tombstones", tomb, nil, func(c *PurgeCounts, n int) { c.PromptTombstones = n }},
			purgeStep{"sync_mutations", mut, nil, func(c *PurgeCounts, n int) { c.SyncMutations = n }},
			purgeStep{"sync_apply_deferred", def, nil, func(c *PurgeCounts, n int) { c.SyncApplyDeferred = n }},
		)
	}
	steps = append(steps,
		purgeStep{"observations", `id IN (SELECT id FROM purge_obs)`, nil, func(c *PurgeCounts, n int) { c.Observations = n }},
		purgeStep{"sessions", `id IN (SELECT id FROM purge_sessions)`, nil, func(c *PurgeCounts, n int) { c.Sessions = n }},
	)
	if whole {
		in := purgePlaceholders(len(w.variants))
		cloudKeys := make([]string, len(w.variants))
		for i, v := range w.variants {
			cloudKeys[i] = "cloud:" + v
		}
		steps = append(steps,
			purgeStep{"sync_enrolled_projects", `project IN (` + in + `)`, purgeArgs(w.variants), func(c *PurgeCounts, n int) { c.SyncEnrolled = n }},
			purgeStep{"cloud_upgrade_state", `project IN (` + in + `)`, purgeArgs(w.variants), func(c *PurgeCounts, n int) { c.CloudUpgradeState = n }},
			purgeStep{"sync_state", `target_key IN (` + in + `)`, purgeArgs(cloudKeys), func(c *PurgeCounts, n int) { c.SyncState = n }},
		)
	}

	// Counts (the plan side of the steps).
	for _, st := range steps {
		var n int
		if err := s.purgeCount(tx, `SELECT COUNT(*) FROM `+st.table+` WHERE `+st.cond, st.args, &n); err != nil {
			return nil, nil, fmt.Errorf("purge: count %s: %w", st.table, err)
		}
		st.set(&plan.Counts, n)
	}
	if err := s.purgeCount(tx, `SELECT COUNT(*) FROM observations WHERE deleted_at IS NOT NULL AND id IN (SELECT id FROM purge_obs)`, nil, &plan.Counts.SoftDeleted); err != nil {
		return nil, nil, err
	}

	// Samples.
	rows, err := tx.Query(`SELECT id, COALESCE(title,''), COALESCE(project,''), created_at FROM observations WHERE id IN (SELECT id FROM purge_obs) ORDER BY id LIMIT 5`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var sm PurgeSample
		sm.Kind = "observation"
		if err := rows.Scan(&sm.ID, &sm.Label, &sm.Project, &sm.CreatedAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		sm.Label = purgeTruncate(sm.Label, 70)
		plan.Samples = append(plan.Samples, sm)
	}
	rows.Close()
	rows, err = tx.Query(`SELECT id, COALESCE(content,''), COALESCE(project,''), created_at FROM user_prompts WHERE id IN (SELECT id FROM purge_prompts) ORDER BY id LIMIT 3`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var sm PurgeSample
		sm.Kind = "prompt"
		if err := rows.Scan(&sm.ID, &sm.Label, &sm.Project, &sm.CreatedAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		sm.Label = purgeTruncate(sm.Label, 70)
		plan.Samples = append(plan.Samples, sm)
	}
	rows.Close()

	// Exported-chunk warning. The store records chunks, not per-row exports,
	// so a row that predates the newest recorded local chunk may be inside one.
	var lastChunk sql.NullString
	if err := tx.QueryRow(`SELECT MAX(imported_at) FROM sync_chunks WHERE target_key = 'local'`).Scan(&lastChunk); err != nil {
		return nil, nil, err
	}
	if lastChunk.Valid && lastChunk.String != "" {
		ts := func(col string) string { return `COALESCE(strftime('%Y-%m-%d %H:%M:%S', ` + col + `), ` + col + `)` }
		var a, b, c int
		for _, q := range []struct {
			sql string
			dst *int
		}{
			{`SELECT COUNT(*) FROM observations WHERE id IN (SELECT id FROM purge_obs) AND ` + ts("created_at") + ` <= ?`, &a},
			{`SELECT COUNT(*) FROM user_prompts WHERE id IN (SELECT id FROM purge_prompts) AND ` + ts("created_at") + ` <= ?`, &b},
			{`SELECT COUNT(*) FROM sessions WHERE id IN (SELECT id FROM purge_sessions) AND ` + ts("started_at") + ` <= ?`, &c},
		} {
			if err := tx.QueryRow(q.sql, lastChunk.String).Scan(q.dst); err != nil {
				return nil, nil, err
			}
		}
		plan.ExportedRows = a + b + c
		if plan.ExportedRows > 0 {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"%d selected row(s) predate the last recorded sync chunk and may have been exported: copies can remain in .engram/chunks files in repositories, and importing those chunks on another machine or after a restore could bring them back",
				plan.ExportedRows))
		}
	}
	return plan, steps, nil
}

func (s *Store) purgeCount(tx *sql.Tx, q string, args []any, dst *int) error {
	return tx.QueryRow(q, args...).Scan(dst)
}

func purgeTruncate(v string, n int) string {
	v = strings.Join(strings.Fields(v), " ")
	r := []rune(v)
	if len(r) <= n {
		return v
	}
	return string(r[:n]) + "..."
}

func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func purgeDropTemp(tx *sql.Tx) {
	for _, t := range []string{"purge_obs", "purge_prompts", "purge_sessions", "purge_rel"} {
		_, _ = tx.Exec(`DROP TABLE IF EXISTS temp.` + t)
	}
}

// PurgePlan computes what Purge would delete. It never writes to the database
// file (only to connection-local temp tables, inside a rolled-back transaction).
func (s *Store) PurgePlan(sel PurgeSelector, opts PurgeOptions) (*PurgePlan, error) {
	tx, err := s.beginTxHook()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	plan, _, err := s.purgeBuild(tx, sel, opts)
	return plan, err
}

// Purge hard-deletes the selection. Order: refusals, backup (abort on
// failure), one transaction of DELETE statements only (FTS triggers keep
// search consistent), then a passive WAL checkpoint. sync_chunks is never
// touched, so already-seen chunks are not re-imported.
func (s *Store) Purge(sel PurgeSelector, opts PurgeOptions) (*PurgeResult, error) {
	plan, err := s.PurgePlan(sel, opts)
	if err != nil {
		return nil, err
	}
	res := &PurgeResult{Plan: plan, Warnings: append([]string{}, plan.Warnings...)}
	if plan.Empty() {
		return res, nil
	}
	backup, err := s.BackupSQLite()
	if err != nil {
		return nil, fmt.Errorf("purge aborted, nothing deleted: backup failed: %w", err)
	}
	res.BackupPath = backup

	err = s.withTx(func(tx *sql.Tx) error {
		res.Deleted = PurgeCounts{}
		// Recompute inside the write transaction so the deleted set is current.
		fresh, steps, err := s.purgeBuild(tx, sel, opts)
		if err != nil {
			return err
		}
		res.Deleted.SoftDeleted = fresh.Counts.SoftDeleted
		res.Deleted.Pinned = fresh.Counts.Pinned
		for _, st := range steps {
			r, err := s.execHook(tx, `DELETE FROM `+st.table+` WHERE `+st.cond, st.args...)
			if err != nil {
				return fmt.Errorf("purge: delete %s: %w", st.table, err)
			}
			n, _ := r.RowsAffected()
			st.set(&res.Deleted, int(n))
		}
		purgeDropTemp(tx)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.execHook(s.db, `PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		res.Warnings = append(res.Warnings, "WAL checkpoint failed: "+err.Error())
	}
	return res, nil
}
