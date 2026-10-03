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
  --json            Machine-readable output

With --yes a backup is written first (path printed); if it fails nothing is
deleted. The backup is engram-purge-<timestamp>.db under the data dir's
backups/ folder (mode 0600, it holds the purged data in clear text); it is
verified (integrity check and row counts) before anything is deleted.
Relations touching a purged observation are removed even if the other end is
kept: the plan reports how many of them point at observations that survive.
Rows whose date cannot be read are never selected by --since/--until; the
plan warns about how many were skipped. Projects enrolled in cloud sync are always refused. sync_chunks is
never touched. When only --project is given, the project's sync state rows
are removed too.
`)
}

func cmdPurge(cfg store.Config) {
	var sel store.PurgeSelector
	var opts store.PurgeOptions
	execute, jsonOut := false, false

	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, val, hasVal := strings.Cut(arg, "=")
		value := func() (string, bool) {
			if hasVal {
				return val, true
			}
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "error: %s requires a value\n", name)
				exitFunc(1)
				return "", false
			}
			i++
			return args[i], true
		}
		var ok bool
		switch name {
		case "--project":
			if sel.Project, ok = value(); !ok {
				return
			}
		case "--session":
			if sel.SessionID, ok = value(); !ok {
				return
			}
		case "--since":
			if sel.Since, ok = value(); !ok {
				return
			}
		case "--until":
			if sel.Until, ok = value(); !ok {
				return
			}
		case "--yes":
			execute = true
		case "--include-pinned":
			opts.IncludePinned = true
		case "--json":
			jsonOut = true
		case "--help", "-h", "help":
			printPurgeUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "error: unknown purge argument %q\n", arg)
			printPurgeUsage()
			exitFunc(1)
			return
		}
	}
	if strings.TrimSpace(sel.Project) == "" && strings.TrimSpace(sel.SessionID) == "" &&
		strings.TrimSpace(sel.Since) == "" && strings.TrimSpace(sel.Until) == "" {
		fmt.Fprintln(os.Stderr, "error: purge needs at least one selector (--project, --session, --since, --until)")
		printPurgeUsage()
		exitFunc(1)
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
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
		if errors.Is(err, store.ErrPurgeRefused) {
			fmt.Fprintf(os.Stderr, "engram: %s\nnothing was deleted\n", err)
			exitFunc(1)
			return
		}
		fatal(err)
		return
	}

	if jsonOut {
		var payload any = map[string]any{"dry_run": true, "plan": plan}
		if execute {
			payload = map[string]any{"dry_run": false, "result": result}
		}
		out, merr := jsonMarshalIndent(payload, "", "  ")
		if merr != nil {
			fatal(merr)
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
}
