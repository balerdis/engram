package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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
	ExportedRows int `json:"possibly_exported_rows"`
	// UnreadableDateRows counts rows that match the non-date selectors but whose
	// timestamp cannot be parsed, so a --since/--until bound never selects them.
	UnreadableDateRows int `json:"unreadable_date_rows"`
	// RelationsToKept counts the relations being deleted that link to an
	// observation outside the selection (that observation survives).
	RelationsToKept int      `json:"relations_to_unselected_observations"`
	Warnings        []string `json:"warnings"`
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
		variants, err := projectVariants(tx, norm)
		if err != nil {
			return nil, err
		}
		w.variants = variants
	}
	return w, nil
}

// projectVariants returns norm plus every raw project spelling stored in
// sessions, observations or prompts that normalises to norm (legacy rows
// written before names were normalised, e.g. "Lab" for "lab").
func projectVariants(tx *sql.Tx, norm string) ([]string, error) {
	seen := map[string]bool{norm: true}
	variants := []string{norm}
	rows, err := tx.Query(`SELECT project FROM sessions UNION SELECT project FROM observations UNION SELECT project FROM user_prompts`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
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
			variants = append(variants, p.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return variants, nil
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

// purgeNormTS is the UTC-normalised form of a stored timestamp column. It is
// NULL when the text is not a parseable date. The GLOB guard matters because
// strftime also accepts non-dates: a bare number is read as a Julian day and
// the literal "now" as the current time, and either would pass a date bound.
func purgeNormTS(ts string) string {
	return "(CASE WHEN " + ts + " GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]*' THEN strftime('%Y-%m-%d %H:%M:%S', " + ts + ") END)"
}

// baseClauses builds the project/session part of the predicate ("1=1" if none).
func (w *purgeWhere) baseClauses(sel PurgeSelector, eff, session string) (string, []any) {
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
	if len(parts) == 0 {
		return "1=1", nil
	}
	return strings.Join(parts, " AND "), args
}

// clauses builds the AND-ed predicate for one row kind. A row whose date is
// unparseable (NULL after normalisation) never satisfies a date bound.
func (w *purgeWhere) clauses(sel PurgeSelector, eff, session, ts string) (string, []any) {
	base, args := w.baseClauses(sel, eff, session)
	parts := []string{base}
	norm := purgeNormTS(ts)
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

// unreadable builds the predicate for rows that match the non-date selectors
// but whose date cannot be parsed; only meaningful when a date bound is set.
func (w *purgeWhere) unreadable(sel PurgeSelector, eff, session, ts string) (string, []any) {
	base, args := w.baseClauses(sel, eff, session)
	return base + " AND " + purgeNormTS(ts) + " IS NULL", args
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

	// Relations that link to an observation which is NOT part of the selection.
	if err := s.purgeCount(tx, `SELECT COUNT(*) FROM memory_relations WHERE sync_id IN (SELECT sync_id FROM purge_rel)
		AND (source_id IN (SELECT sync_id FROM observations WHERE id NOT IN (SELECT id FROM purge_obs))
		  OR target_id IN (SELECT sync_id FROM observations WHERE id NOT IN (SELECT id FROM purge_obs)))`, nil, &plan.RelationsToKept); err != nil {
		return nil, nil, fmt.Errorf("purge: count relations to kept observations: %w", err)
	}
	if plan.RelationsToKept > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"%d relation(s) to be deleted link to observations outside the selection; those observations are kept and lose the relation",
			plan.RelationsToKept))
	}

	// Rows whose date cannot be read are never selected by a date bound.
	if w.sinceTS != "" || w.untilTS != "" {
		for _, q := range []struct{ table, eff, session, ts string }{
			{"observations o", obsEff, "o.session_id", "o.created_at"},
			{"user_prompts p", prEff, "p.session_id", "p.created_at"},
			{"sessions ses", "ses.project", "ses.id", "ses.started_at"},
		} {
			c, a := w.unreadable(sel, q.eff, q.session, q.ts)
			var n int
			if err := s.purgeCount(tx, `SELECT COUNT(*) FROM `+q.table+` WHERE `+c, a, &n); err != nil {
				return nil, nil, fmt.Errorf("purge: count unreadable dates: %w", err)
			}
			plan.UnreadableDateRows += n
		}
		if plan.UnreadableDateRows > 0 {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"%d rows with an unreadable date were not selected by the date filter", plan.UnreadableDateRows))
		}
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
	backup, err := s.purgeBackup()
	if err != nil {
		return nil, fmt.Errorf("purge aborted, nothing deleted: backup failed: %w", err)
	}
	res.BackupPath = backup
	if err := verifyPurgeBackup(backup, plan.Counts); err != nil {
		return nil, fmt.Errorf("purge aborted, nothing deleted: backup verification failed (the backup file was left at %s): %w", backup, err)
	}

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
		return nil, fmt.Errorf("purge failed, nothing was deleted; the backup at %s holds the untouched data: %w", backup, err)
	}
	if _, err := s.execHook(s.db, `PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		res.Warnings = append(res.Warnings, "WAL checkpoint failed: "+err.Error())
	}
	return res, nil
}

// purgeBackup writes engram-purge-<timestamp>.db under <data dir>/backups. The
// directory is created 0700 only when missing (an existing one is left alone)
// and the file is 0600: it holds the purged data in clear text.
func (s *Store) purgeBackup() (string, error) {
	backupDir := filepath.Join(s.cfg.DataDir, "backups")
	if _, err := os.Stat(backupDir); os.IsNotExist(err) {
		if err := os.MkdirAll(backupDir, 0o700); err != nil {
			return "", fmt.Errorf("create backup dir: %w", err)
		}
	}
	path := filepath.Join(backupDir, "engram-purge-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".db")
	// VACUUM INTO accepts an existing empty file, so pre-create it with 0600.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create backup file: %w", err)
	}
	f.Close()
	if _, err := s.execHook(s.db, `VACUUM INTO ?`, path); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("backup sqlite database: %w", err)
	}
	return path, nil
}

// verifyPurgeBackup opens the backup read-only, runs integrity_check and makes
// sure each purged table holds at least the planned number of rows.
func verifyPurgeBackup(path string, planned PurgeCounts) error {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer db.Close()
	var verdict string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&verdict); err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	if verdict != "ok" {
		return fmt.Errorf("integrity_check reported %q", verdict)
	}
	for _, t := range []struct {
		table string
		want  int
	}{
		{"sessions", planned.Sessions},
		{"observations", planned.Observations},
		{"user_prompts", planned.Prompts},
		{"memory_relations", planned.Relations},
		{"prompt_tombstones", planned.PromptTombstones},
		{"sync_mutations", planned.SyncMutations},
		{"sync_apply_deferred", planned.SyncApplyDeferred},
		{"sync_state", planned.SyncState},
		{"sync_enrolled_projects", planned.SyncEnrolled},
		{"cloud_upgrade_state", planned.CloudUpgradeState},
	} {
		if t.want == 0 {
			continue
		}
		var got int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + t.table).Scan(&got); err != nil {
			return fmt.Errorf("count %s in backup: %w", t.table, err)
		}
		if got < t.want {
			return fmt.Errorf("backup has %d row(s) in %s, expected at least %d", got, t.table, t.want)
		}
	}
	return nil
}
