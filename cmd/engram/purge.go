package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func printPurgeUsage() {
	fmt.Fprint(os.Stderr, `usage: engram purge [--project NAME] [--session ID] [--since DATE] [--until DATE]
                    [--yes] [--include-pinned] [--json]
       engram purge --sync-leftovers [--yes] [--vacuum] [--json]

Remove the memories that test runs leave in the real database. Hard delete,
CLI only. Selectors are combined with AND; at least one is required.

  --project NAME    Project (normalised like everywhere else)
  --session ID      Session id
  --since DATE      Rows created at or after DATE (inclusive)
  --until DATE      Rows created at or before DATE (inclusive; a date-only
                    value covers that whole UTC day)
                    DATE is YYYY-MM-DD or RFC3339; stored timestamps in either
                    format are normalised to UTC before comparing.
  --yes             Execute. Without it nothing is written (dry-run preview).
  --include-pinned  Allow pinned observations to be deleted (refused otherwise)
  --json            Machine-readable output; refusals and errors come out as
                    {"error": "...", "refused": true|false} on stdout (exit 1)

--sync-leftovers    Clean what the removed cloud sync left in the database
                    instead of purging memories (no other selector applies):
                    every row of sync_mutations, sync_apply_deferred,
                    sync_enrolled_projects and cloud_upgrade_state, and the
                    sync_state rows other than 'local' (used by chunk
                    import) and 'cloud' (recreated on every open). Dry-run by default: it shows rows and
                    approximate bytes per table. With --yes it makes the same
                    verified backup (0600, kept) and deletes in one
                    transaction. sync_chunks and those two sync_state rows are
                    never touched, so engram sync keeps working.
  --vacuum          With --sync-leftovers --yes, also rewrite the database file
                    to give the space back. It needs the database to itself:
                    stop engram serve and engram mcp first. If another process
                    holds it, purge refuses before deleting anything.

A flag value may not start with "-" (--project --yes is rejected); write
--project=-x if you really mean it. --since later than --until is refused.

With --yes a backup is written first (path printed); if it fails nothing is
deleted. The backup is engram-purge-YYYYMMDDTHHMMSSZ.db (a -2, -3 ... suffix
if that name is taken) under the data dir's backups/ folder (mode 0600, it
holds the purged data in clear text); it is verified (integrity check and row
counts) before anything is deleted, and it is kept: remove it yourself later.
Relations touching a purged observation are removed even if the other end is
kept: the plan reports how many of them point at observations that survive.
Rows whose date cannot be read are never selected by --since/--until; the
plan warns about how many were skipped. sync_chunks is
never touched. When only --project is given, the project's sync state rows
are removed too.
`)
}

// purgeFail reports an error and exits 1. With --json it prints
// {"error": ..., "refused": ...} on stdout (the stream a script reads) instead
// of text on stderr. refused is true when purge declined on purpose (pinned
// rows, an inverted date range) rather than failing. showUsage adds the usage
// text to the human output of a command-line mistake.
func purgeFail(jsonOut bool, err error, refused, showUsage bool) {
	if jsonOut {
		out, merr := jsonMarshalIndent(map[string]any{"error": err.Error(), "refused": refused}, "", "  ")
		if merr != nil {
			out = []byte(`{"error":"internal error","refused":false}`)
		}
		fmt.Println(string(out))
		exitFunc(1)
		return
	}
	switch {
	case refused:
		fmt.Fprintf(os.Stderr, "engram: %s\nnothing was deleted\n", err)
	case showUsage:
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		printPurgeUsage()
	default:
		fmt.Fprintf(os.Stderr, "engram: %s\n", err)
	}
	exitFunc(1)
}

func cmdPurge(cfg store.Config) {
	var sel store.PurgeSelector
	var opts store.PurgeOptions
	execute, jsonOut := false, false
	leftovers, vacuum := false, false

	args := os.Args[2:]
	// --json decides how every later error is printed, wherever it appears.
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, val, hasVal := strings.Cut(arg, "=")
		failed := false
		value := func() string {
			if hasVal {
				return val
			}
			if i+1 >= len(args) {
				purgeFail(jsonOut, fmt.Errorf("%s requires a value", name), false, true)
				failed = true
				return ""
			}
			i++
			// A flag swallowed as a value (--project --yes) would purge the
			// wrong thing or none; --name=value is the way to pass a dash.
			if strings.HasPrefix(args[i], "-") {
				purgeFail(jsonOut, fmt.Errorf("%s requires a value, got %q which looks like a flag (use %s=VALUE if that is really the value)", name, args[i], name), false, true)
				failed = true
				return ""
			}
			return args[i]
		}
		switch name {
		case "--yes", "--include-pinned", "--json", "--sync-leftovers", "--vacuum":
			// Boolean flags take no value: "--yes=false" must not run as "--yes".
			if hasVal {
				purgeFail(jsonOut, fmt.Errorf("%s takes no value, got %q", name, arg), false, true)
				return
			}
		}
		switch name {
		case "--project":
			sel.Project = value()
		case "--session":
			sel.SessionID = value()
		case "--since":
			sel.Since = value()
		case "--until":
			sel.Until = value()
		case "--yes":
			execute = true
		case "--include-pinned":
			opts.IncludePinned = true
		case "--json":
			jsonOut = true
		case "--sync-leftovers":
			leftovers = true
		case "--vacuum":
			vacuum = true
		case "--help", "-h", "help":
			printPurgeUsage()
			return
		default:
			purgeFail(jsonOut, fmt.Errorf("unknown purge argument %q", arg), false, true)
			return
		}
		if failed {
			return
		}
	}
	if vacuum && !leftovers {
		purgeFail(jsonOut, errors.New("--vacuum only applies to --sync-leftovers"), false, true)
		return
	}
	if leftovers {
		if sel != (store.PurgeSelector{}) || opts.IncludePinned {
			purgeFail(jsonOut, errors.New("--sync-leftovers takes no selector (--project, --session, --since, --until) and no --include-pinned"), false, true)
			return
		}
		cmdPurgeSyncLeftovers(cfg, execute, vacuum, jsonOut)
		return
	}
	if strings.TrimSpace(sel.Project) == "" && strings.TrimSpace(sel.SessionID) == "" &&
		strings.TrimSpace(sel.Since) == "" && strings.TrimSpace(sel.Until) == "" {
		purgeFail(jsonOut, errors.New("purge needs at least one selector (--project, --session, --since, --until)"), false, true)
		return
	}
	// Bad dates are a usage error: say so before opening (and migrating) the store.
	if err := store.ValidatePurgeBounds(sel.Since, sel.Until); err != nil {
		purgeFail(jsonOut, err, errors.Is(err, store.ErrPurgeRefused), !errors.Is(err, store.ErrPurgeRefused))
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		purgeFail(jsonOut, err, false, false)
		return
	}
	defer s.Close()

	var plan *store.PurgePlan
	var result *store.PurgeResult
	if execute {
		result, err = s.Purge(sel, opts)
		if result != nil {
			plan = result.Plan
		}
	} else {
		plan, err = s.PurgePlan(sel, opts)
	}
	if err != nil {
		purgeFail(jsonOut, err, errors.Is(err, store.ErrPurgeRefused), false)
		return
	}

	if jsonOut {
		var payload any = map[string]any{"dry_run": true, "plan": plan}
		if execute {
			payload = map[string]any{"dry_run": false, "result": result}
		}
		out, merr := jsonMarshalIndent(payload, "", "  ")
		if merr != nil {
			purgeFail(jsonOut, merr, false, false)
			return
		}
		fmt.Println(string(out))
		return
	}
	printPurge(plan, result)
}

func printPurge(plan *store.PurgePlan, result *store.PurgeResult) {
	if plan.Empty() {
		fmt.Println("Nothing matched the selection; nothing to purge.")
		for _, w := range plan.Warnings {
			fmt.Printf("WARNING: %s\n", w)
		}
		return
	}
	counts := plan.Counts
	verb := "Purge plan (dry-run, nothing deleted; add --yes to execute)"
	if result != nil {
		verb = "Purged"
		counts = result.Deleted
		if result.BackupPath != "" {
			fmt.Printf("Backup written: %s\n", result.BackupPath)
		}
	}
	fmt.Println(verb)
	if len(plan.Projects) > 0 {
		fmt.Printf("  projects: %s\n", strings.Join(plan.Projects, ", "))
	}
	row := func(label string, n int, note string) {
		fmt.Printf("  %-26s %6d%s\n", label, n, note)
	}
	row("sessions", counts.Sessions, "")
	obsNote := ""
	if plan.Counts.SoftDeleted > 0 || plan.Counts.Pinned > 0 {
		obsNote = fmt.Sprintf("  (%d soft-deleted, %d pinned)", plan.Counts.SoftDeleted, plan.Counts.Pinned)
	}
	row("observations", counts.Observations, obsNote)
	row("user_prompts", counts.Prompts, "")
	row("memory_relations", counts.Relations, "")
	row("prompt_tombstones", counts.PromptTombstones, "")
	row("sync_mutations", counts.SyncMutations, "")
	row("sync_apply_deferred", counts.SyncApplyDeferred, "")
	if plan.WholeProject {
		row("sync_state (cloud:<proj>)", counts.SyncState, "")
		row("sync_enrolled_projects", counts.SyncEnrolled, "")
		row("cloud_upgrade_state", counts.CloudUpgradeState, "")
	}
	if len(plan.Samples) > 0 {
		fmt.Println("Samples:")
		for _, sm := range plan.Samples {
			fmt.Printf("  %s #%d [%s] %q (%s)\n", sm.Kind, sm.ID, sm.Project, sm.Label, sm.CreatedAt)
		}
	}
	warnings := plan.Warnings
	if result != nil {
		warnings = result.Warnings
	}
	for _, w := range warnings {
		fmt.Printf("WARNING: %s\n", w)
	}
	if result != nil && result.BackupPath != "" {
		fmt.Printf("Backup kept at %s\nIt is not deleted automatically and holds the purged data in clear text; remove it yourself once you no longer need it.\n", result.BackupPath)
	}
}

func cmdPurgeSyncLeftovers(cfg store.Config, execute, vacuum, jsonOut bool) {
	s, err := storeNew(cfg)
	if err != nil {
		purgeFail(jsonOut, err, false, false)
		return
	}
	defer s.Close()

	var plan *store.SyncLeftoversPlan
	var result *store.SyncLeftoversResult
	if execute {
		result, err = s.PurgeSyncLeftovers(store.SyncLeftoversOptions{Vacuum: vacuum})
		if result != nil {
			plan = result.Plan
		}
	} else {
		plan, err = s.SyncLeftoversPlan()
	}
	if err != nil {
		purgeFail(jsonOut, err, errors.Is(err, store.ErrPurgeRefused), false)
		return
	}
	if jsonOut {
		payload := map[string]any{"sync_leftovers": true, "dry_run": true, "vacuum_requested": vacuum, "plan": plan}
		if execute {
			payload = map[string]any{"sync_leftovers": true, "dry_run": false, "result": result}
		}
		out, merr := jsonMarshalIndent(payload, "", "  ")
		if merr != nil {
			purgeFail(jsonOut, merr, false, false)
			return
		}
		fmt.Println(string(out))
		return
	}
	printSyncLeftovers(plan, result, vacuum)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func printSyncLeftovers(plan *store.SyncLeftoversPlan, result *store.SyncLeftoversResult, vacuum bool) {
	rows := plan.Tables
	verb := "Sync leftovers (dry-run, nothing deleted; add --yes to delete"
	if vacuum {
		verb += ", with --vacuum to also shrink the file"
	}
	verb += ")"
	if result != nil {
		verb = "Sync leftovers deleted"
		if result.BackupPath != "" {
			fmt.Printf("Backup written: %s\n", result.BackupPath)
		}
		if len(result.Deleted) > 0 {
			rows = result.Deleted
		}
	}
	if plan.Empty() {
		fmt.Println("No sync leftovers found; nothing to clean.")
	} else {
		fmt.Println(verb)
		for i, tb := range rows {
			note := ""
			if result == nil || len(result.Deleted) == 0 {
				note = "  ~" + humanBytes(tb.ApproxBytes)
			} else {
				note = "  ~" + humanBytes(plan.Tables[i].ApproxBytes)
			}
			if tb.Table == "sync_state" {
				note += "  (rows other than local and cloud)"
			}
			fmt.Printf("  %-26s %7d%s\n", tb.Table, tb.Rows, note)
		}
		fmt.Printf("  %-26s %7d  ~%s\n", "total", plan.TotalRows, humanBytes(plan.ApproxBytes))
		fmt.Println("  sync_chunks and the local and cloud sync_state rows are never touched.")
	}
	if result == nil {
		return
	}
	if result.Vacuumed {
		fmt.Printf("Database file: %s -> %s (vacuumed)\n", humanBytes(result.DBBytesBefore), humanBytes(result.DBBytesAfter))
	} else if !plan.Empty() {
		fmt.Printf("Database file: %s -> %s (not vacuumed: the space is reused by new rows, run again with --vacuum to give it back; stop engram serve and engram mcp first)\n", humanBytes(result.DBBytesBefore), humanBytes(result.DBBytesAfter))
	}
	for _, w := range result.Warnings {
		fmt.Printf("WARNING: %s\n", w)
	}
	if result.BackupPath != "" {
		fmt.Printf("Backup kept at %s\nIt is not deleted automatically and holds the deleted data in clear text; remove it yourself once you no longer need it.\n", result.BackupPath)
	}
}
