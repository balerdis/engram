package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// DiagnosticSessionEvidence is the read-only session projection used by
// operational diagnostics. It intentionally avoids observation/prompt payloads.
type DiagnosticSessionEvidence struct {
	ID        string `json:"id"`
	Project   string `json:"project"`
	Directory string `json:"directory"`
	Name      string `json:"name"`
}

// SQLiteLockSnapshot captures conservative SQLite lock/contention indicators.
// wal_checkpoint(PASSIVE) is an observational probe for this diagnostic surface;
// callers must not interpret it as a repair action.
type SQLiteLockSnapshot struct {
	JournalMode        string `json:"journal_mode"`
	BusyTimeoutMS      int    `json:"busy_timeout_ms"`
	CheckpointBusy     int    `json:"checkpoint_busy"`
	CheckpointLog      int    `json:"checkpoint_log"`
	CheckpointedFrames int    `json:"checkpointed_frames"`
}

type SessionProjectReclassification struct {
	SessionID   string
	FromProject string
	ToProject   string
}

type SessionProjectReclassificationCounts struct {
	Sessions     int64
	Observations int64
	Prompts      int64
}

type SessionProjectReclassificationResult struct {
	Counts     SessionProjectReclassificationCounts
	BackupPath string
}

// ListDiagnosticSessions returns session evidence scoped by project when
// provided. The query is read-only and ordered for deterministic diagnostics.
func (s *Store) ListDiagnosticSessions(project string) ([]DiagnosticSessionEvidence, error) {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	query := `SELECT id, project, ifnull(directory, ''), id FROM sessions`
	args := []any{}
	if project != "" {
		query += ` WHERE project = ?`
		args = append(args, project)
	}
	query += ` ORDER BY started_at DESC, id ASC`

	rows, err := s.queryItHook(s.db, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := make([]DiagnosticSessionEvidence, 0)
	for rows.Next() {
		var ev DiagnosticSessionEvidence
		if err := rows.Scan(&ev.ID, &ev.Project, &ev.Directory, &ev.Name); err != nil {
			return nil, err
		}
		sessions = append(sessions, ev)
	}
	return sessions, rows.Err()
}

// ReadSQLiteLockSnapshot returns SQLite lock-related PRAGMA values without
// starting an application write transaction.
func (s *Store) ReadSQLiteLockSnapshot(ctx context.Context) (SQLiteLockSnapshot, error) {
	var snapshot SQLiteLockSnapshot
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&snapshot.JournalMode); err != nil {
		return snapshot, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&snapshot.BusyTimeoutMS); err != nil {
		return snapshot, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&snapshot.CheckpointBusy, &snapshot.CheckpointLog, &snapshot.CheckpointedFrames); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func (s *Store) EstimateSessionProjectReclassification(actions []SessionProjectReclassification) (SessionProjectReclassificationCounts, error) {
	var counts SessionProjectReclassificationCounts
	for _, action := range normalizeSessionProjectReclassificationActions(actions) {
		var n int64
		if err := s.db.QueryRow(`SELECT count(*) FROM sessions WHERE id = ? AND project = ?`, action.SessionID, action.FromProject).Scan(&n); err != nil {
			return counts, fmt.Errorf("estimate sessions: %w", err)
		}
		counts.Sessions += n
		if err := s.db.QueryRow(`SELECT count(*) FROM observations WHERE session_id = ? AND project = ? AND deleted_at IS NULL`, action.SessionID, action.FromProject).Scan(&n); err != nil {
			return counts, fmt.Errorf("estimate observations: %w", err)
		}
		counts.Observations += n
		if err := s.db.QueryRow(`SELECT count(*) FROM user_prompts WHERE session_id = ? AND project = ?`, action.SessionID, action.FromProject).Scan(&n); err != nil {
			return counts, fmt.Errorf("estimate prompts: %w", err)
		}
		counts.Prompts += n
	}
	return counts, nil
}

func (s *Store) ApplySessionProjectReclassification(actions []SessionProjectReclassification) (SessionProjectReclassificationResult, error) {
	normalized := normalizeSessionProjectReclassificationActions(actions)
	backupPath, err := s.BackupSQLite()
	if err != nil {
		return SessionProjectReclassificationResult{}, err
	}
	var result SessionProjectReclassificationResult
	result.BackupPath = backupPath
	err = s.withTx(func(tx *sql.Tx) error {
		for _, action := range normalized {
			res, err := s.execHook(tx, `UPDATE sessions SET project = ? WHERE id = ? AND project = ?`, action.ToProject, action.SessionID, action.FromProject)
			if err != nil {
				return fmt.Errorf("reclassify session %q: %w", action.SessionID, err)
			}
			n, _ := res.RowsAffected()
			result.Counts.Sessions += n

			res, err = s.execHook(tx, `UPDATE observations SET project = ? WHERE session_id = ? AND project = ?`, action.ToProject, action.SessionID, action.FromProject)
			if err != nil {
				return fmt.Errorf("reclassify observations for session %q: %w", action.SessionID, err)
			}
			n, _ = res.RowsAffected()
			result.Counts.Observations += n

			res, err = s.execHook(tx, `UPDATE user_prompts SET project = ? WHERE session_id = ? AND project = ?`, action.ToProject, action.SessionID, action.FromProject)
			if err != nil {
				return fmt.Errorf("reclassify prompts for session %q: %w", action.SessionID, err)
			}
			n, _ = res.RowsAffected()
			result.Counts.Prompts += n
		}
		return nil
	})
	if err != nil {
		return SessionProjectReclassificationResult{}, err
	}
	return result, nil
}

// BackupSQLite writes engram-repair-<timestamp>.db under <data dir>/backups
// (dir 0700 when created, file 0600, like purge's backup).
func (s *Store) BackupSQLite() (string, error) {
	return s.backupInto("engram-repair-")
}

func normalizeSessionProjectReclassificationActions(actions []SessionProjectReclassification) []SessionProjectReclassification {
	seen := make(map[string]struct{})
	out := make([]SessionProjectReclassification, 0, len(actions))
	for _, action := range actions {
		action.SessionID = strings.TrimSpace(action.SessionID)
		action.FromProject, _ = NormalizeProject(action.FromProject)
		action.ToProject, _ = NormalizeProject(action.ToProject)
		if action.SessionID == "" || action.FromProject == "" || action.ToProject == "" || action.FromProject == action.ToProject {
			continue
		}
		key := action.SessionID + "\x00" + action.FromProject + "\x00" + action.ToProject
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, action)
	}
	return out
}
