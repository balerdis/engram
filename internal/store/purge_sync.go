package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Leftovers of the removed cloud sync. The tables stay in the schema (the
// schema rule is "no DROP"), but nothing writes to them any more, so on a
// database that lived through the cloud sync they only weigh. sync_state keeps
// the 'local' row that ApplyPulledChunk advances (LocalChunkTargetKey), and
// Store.migrate() recreates the 'cloud' row (DefaultSyncTargetKey) on every
// open, so deleting it would be undone at once and the cleanup would never read
// as empty: both stay. Every other row (cloud:<project> and the like) is a
// leftover. sync_chunks is never touched: it is what keeps
// already-imported chunks from being imported twice.

type syncLeftoverSpec struct {
	table string
	cond  string // "" = every row
	args  []any
	set   func(*PurgeCounts, int)
}

func syncLeftoverSpecs() []syncLeftoverSpec {
	return []syncLeftoverSpec{
		{"sync_mutations", "", nil, func(c *PurgeCounts, n int) { c.SyncMutations = n }},
		{"sync_apply_deferred", "", nil, func(c *PurgeCounts, n int) { c.SyncApplyDeferred = n }},
		{"sync_enrolled_projects", "", nil, func(c *PurgeCounts, n int) { c.SyncEnrolled = n }},
		{"cloud_upgrade_state", "", nil, func(c *PurgeCounts, n int) { c.CloudUpgradeState = n }},
		{"sync_state", "target_key NOT IN (?, ?)", []any{LocalChunkTargetKey, DefaultSyncTargetKey}, func(c *PurgeCounts, n int) { c.SyncState = n }},
	}
}

// SyncLeftoverTable is one table's share of the leftovers.
type SyncLeftoverTable struct {
	Table string `json:"table"`
	Rows  int    `json:"rows"`
	// ApproxBytes is the summed length of the row values: an estimate of the
	// payload, not of the file space (indexes and page slack are not counted).
	ApproxBytes int64 `json:"approx_bytes"`
}

// SyncLeftoversPlan describes what the cleanup would delete. Building it
// writes nothing.
type SyncLeftoversPlan struct {
	Tables      []SyncLeftoverTable `json:"tables"`
	TotalRows   int                 `json:"total_rows"`
	ApproxBytes int64               `json:"approx_bytes"`
	// KeptSyncStateRows are the sync_state rows the cleanup never deletes.
	KeptSyncStateRows []string `json:"kept_sync_state_rows"`
}

// Empty reports whether there is nothing to delete.
func (p *SyncLeftoversPlan) Empty() bool { return p.TotalRows == 0 }

func (p *SyncLeftoversPlan) counts() PurgeCounts {
	var c PurgeCounts
	specs := syncLeftoverSpecs()
	for i, t := range p.Tables {
		specs[i].set(&c, t.Rows)
	}
	return c
}

// SyncLeftoversOptions tunes PurgeSyncLeftovers.
type SyncLeftoversOptions struct {
	// Vacuum rewrites the database file afterwards to give the space back. It
	// needs the database to itself; PurgeSyncLeftovers refuses (before deleting
	// anything) when another process has it open.
	Vacuum bool
}

// SyncLeftoversResult is the outcome of an executed cleanup.
type SyncLeftoversResult struct {
	Plan       *SyncLeftoversPlan  `json:"plan"`
	BackupPath string              `json:"backup_path,omitempty"`
	Deleted    []SyncLeftoverTable `json:"deleted"`
	Vacuumed   bool                `json:"vacuumed"`
	// DBBytesBefore/After are the sizes of engram.db plus its -wal file.
	DBBytesBefore int64    `json:"db_bytes_before"`
	DBBytesAfter  int64    `json:"db_bytes_after"`
	Warnings      []string `json:"warnings"`
}

func (s *Store) syncLeftoverTableSize(q rowQueryer, spec syncLeftoverSpec) (int, int64, error) {
	// Column list of the table, to sum the length of every value.
	rows, err := q.Query(`SELECT name FROM pragma_table_info(?)`, spec.table)
	if err != nil {
		return 0, 0, err
	}
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return 0, 0, err
		}
		cols = append(cols, `COALESCE(length("`+strings.ReplaceAll(c, `"`, `""`)+`"),0)`)
	}
	rows.Close()
	if len(cols) == 0 {
		return 0, 0, fmt.Errorf("table %s has no columns", spec.table)
	}
	where := ""
	if spec.cond != "" {
		where = " WHERE " + spec.cond
	}
	var n int
	var bytes sql.NullInt64
	err = q.QueryRow(`SELECT COUNT(*), SUM(`+strings.Join(cols, "+")+`) FROM `+spec.table+where, spec.args...).Scan(&n, &bytes)
	return n, bytes.Int64, err
}

type rowQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func (s *Store) syncLeftoversPlanTx(tx *sql.Tx) (*SyncLeftoversPlan, error) {
	plan := &SyncLeftoversPlan{Tables: []SyncLeftoverTable{}, KeptSyncStateRows: []string{LocalChunkTargetKey, DefaultSyncTargetKey}}
	for _, spec := range syncLeftoverSpecs() {
		n, b, err := s.syncLeftoverTableSize(tx, spec)
		if err != nil {
			return nil, fmt.Errorf("sync leftovers: count %s: %w", spec.table, err)
		}
		plan.Tables = append(plan.Tables, SyncLeftoverTable{Table: spec.table, Rows: n, ApproxBytes: b})
		plan.TotalRows += n
		plan.ApproxBytes += b
	}
	return plan, nil
}

// SyncLeftoversPlan computes what PurgeSyncLeftovers would delete. It writes
// nothing.
func (s *Store) SyncLeftoversPlan() (*SyncLeftoversPlan, error) {
	tx, err := s.beginTxHook()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return s.syncLeftoversPlanTx(tx)
}

// dbFileBytes is the size of engram.db plus its WAL.
func (s *Store) dbFileBytes() int64 {
	var total int64
	for _, suffix := range []string{"", "-wal"} {
		if fi, err := os.Stat(filepath.Join(s.cfg.DataDir, "engram.db"+suffix)); err == nil {
			total += fi.Size()
		}
	}
	return total
}

// requireExclusiveAccess checks that no other process holds the database, which
// VACUUM needs. A TRUNCATE checkpoint reports busy while another connection
// (engram serve, engram mcp, a reader) is open on the WAL.
func (s *Store) requireExclusiveAccess() error {
	var busy, logFrames, checkpointed int
	err := s.db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed)
	if err != nil {
		return fmt.Errorf("%w: cannot get exclusive access to the database for --vacuum: %v; stop the other engram processes (serve, mcp) and retry", ErrPurgeRefused, err)
	}
	if busy != 0 {
		return fmt.Errorf("%w: another process is using the database, so --vacuum cannot run; stop the other engram processes (serve, mcp) and retry", ErrPurgeRefused)
	}
	return nil
}

// PurgeSyncLeftovers deletes the sync leftovers. Order: plan, exclusive-access
// check when vacuuming (nothing is deleted if it fails), verified backup (abort
// on failure), one transaction of DELETE statements, then the optional VACUUM.
func (s *Store) PurgeSyncLeftovers(opts SyncLeftoversOptions) (*SyncLeftoversResult, error) {
	plan, err := s.SyncLeftoversPlan()
	if err != nil {
		return nil, err
	}
	res := &SyncLeftoversResult{Plan: plan, Deleted: []SyncLeftoverTable{}, Warnings: []string{}, DBBytesBefore: s.dbFileBytes()}
	if opts.Vacuum {
		if err := s.requireExclusiveAccess(); err != nil {
			return nil, err
		}
	}
	if !plan.Empty() {
		backup, err := s.purgeBackup()
		if err != nil {
			return nil, fmt.Errorf("cleanup aborted, nothing deleted: backup failed: %w", err)
		}
		res.BackupPath = backup
		if err := verifyPurgeBackup(backup, plan.counts()); err != nil {
			return nil, fmt.Errorf("cleanup aborted, nothing deleted: backup verification failed (the backup file was left at %s): %w", backup, err)
		}
		err = s.withTx(func(tx *sql.Tx) error {
			res.Deleted = res.Deleted[:0]
			for _, spec := range syncLeftoverSpecs() {
				q := `DELETE FROM ` + spec.table
				if spec.cond != "" {
					q += ` WHERE ` + spec.cond
				}
				r, err := s.execHook(tx, q, spec.args...)
				if err != nil {
					return fmt.Errorf("delete %s: %w", spec.table, err)
				}
				n, _ := r.RowsAffected()
				res.Deleted = append(res.Deleted, SyncLeftoverTable{Table: spec.table, Rows: int(n)})
			}
			return nil
		})
		if err != nil {
			res.Deleted = []SyncLeftoverTable{}
			return nil, fmt.Errorf("cleanup failed, nothing was deleted; the backup at %s holds the untouched data: %w", backup, err)
		}
		var deleted PurgeCounts
		for i, spec := range syncLeftoverSpecs() {
			spec.set(&deleted, res.Deleted[i].Rows)
		}
		if diff := purgeCountDiffs(plan.counts(), deleted); len(diff) > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"the rows deleted differ from the plan shown (%s): the data changed between the preview and the delete",
				strings.Join(diff, "; ")))
		}
	}
	if opts.Vacuum {
		if err := s.requireExclusiveAccess(); err != nil {
			res.Warnings = append(res.Warnings, "VACUUM not run: "+err.Error())
		} else if _, err := s.execHook(s.db, `VACUUM`); err != nil {
			res.Warnings = append(res.Warnings, "VACUUM failed (the data is deleted; the file keeps its size): "+err.Error())
		} else {
			res.Vacuumed = true
			if _, err := s.execHook(s.db, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
				res.Warnings = append(res.Warnings, "WAL checkpoint failed: "+err.Error())
			}
		}
	} else if _, err := s.execHook(s.db, `PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		res.Warnings = append(res.Warnings, "WAL checkpoint failed: "+err.Error())
	}
	res.DBBytesAfter = s.dbFileBytes()
	return res, nil
}
