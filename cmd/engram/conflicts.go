package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// cmdConflicts is the top-level dispatcher for `engram conflicts <subcommand>`.
// Switches on os.Args[2] and delegates to the sub-command function.
func cmdConflicts(cfg store.Config) {
	if len(os.Args) < 3 {
		printConflictsUsage()
		exitFunc(1)
		return
	}
	switch os.Args[2] {
	case "list":
		cmdConflictsList(cfg)
	case "show":
		cmdConflictsShow(cfg)
	case "stats":
		cmdConflictsStats(cfg)
	case "scan":
		cmdConflictsScan(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown conflicts subcommand: %s\n", os.Args[2])
		printConflictsUsage()
		exitFunc(1)
	}
}

func printConflictsUsage() {
	fmt.Fprintln(os.Stderr, "usage: engram conflicts <subcommand> [options]")
	fmt.Fprintln(os.Stderr, "subcommands: list, show, stats, scan")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  list       [--project P]  [--status S]  [--since RFC3339]  [--limit N]")
	fmt.Fprintln(os.Stderr, "  show       <relation_id>")
	fmt.Fprintln(os.Stderr, "  stats      [--project P]")
	fmt.Fprintln(os.Stderr, "  scan       [--project P]  [--since RFC3339]  [--dry-run]  [--apply]  [--max-insert N]")
}

// resolveConflictsProject returns the explicit project if non-empty, otherwise falls
// back to detecting the project from the current working directory.
// On detection failure, writes an error to stderr and calls exitFunc(1).
func resolveConflictsProject(explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot resolve cwd: %v\n", err)
		fmt.Fprintln(os.Stderr, "hint: use --project to specify the project explicitly")
		exitFunc(1)
	}
	detected := detectProject(cwd)
	if detected == "" {
		fmt.Fprintln(os.Stderr, "error: could not detect project from cwd")
		fmt.Fprintln(os.Stderr, "hint: use --project to specify the project explicitly")
		exitFunc(1)
	}
	return detected
}

// ─── list ─────────────────────────────────────────────────────────────────────

func cmdConflictsList(cfg store.Config) {
	args := os.Args[3:]

	var projectFlag, statusFlag, sinceFlag string
	limit := 50

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			if i+1 < len(args) {
				projectFlag = args[i+1]
				i++
			}
		case "--status":
			if i+1 < len(args) {
				statusFlag = args[i+1]
				i++
			}
		case "--since":
			if i+1 < len(args) {
				sinceFlag = args[i+1]
				i++
			}
		case "--limit":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					limit = n
				}
				i++
			}
		}
	}

	proj := resolveConflictsProject(projectFlag)

	var sinceTime time.Time
	if sinceFlag != "" {
		t, err := time.Parse(time.RFC3339, sinceFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: --since must be RFC3339 format: %v\n", err)
			exitFunc(1)
			return
		}
		sinceTime = t
	}

	if limit > 500 {
		fmt.Fprintln(os.Stderr, "error: --limit cannot exceed 500")
		exitFunc(1)
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	opts := store.ListRelationsOptions{
		Project:   proj,
		Status:    statusFlag,
		SinceTime: sinceTime,
		Limit:     limit,
	}

	items, err := s.ListRelations(opts)
	if err != nil {
		fatal(err)
		return
	}

	total, err := s.CountRelations(opts)
	if err != nil {
		fatal(err)
		return
	}

	fmt.Printf("Conflicts List (project: %s)\n", proj)
	fmt.Printf("  Total:  %d\n", total)
	fmt.Printf("  Showing: %d\n", len(items))
	if len(items) == 0 {
		fmt.Println("  No relations found.")
		return
	}
	fmt.Println()
	for _, item := range items {
		fmt.Printf("  id:             %d\n", item.ID)
		fmt.Printf("  sync_id:        %s\n", item.SyncID)
		fmt.Printf("  relation:       %s\n", item.Relation)
		fmt.Printf("  judgment_status: %s\n", item.JudgmentStatus)
		fmt.Printf("  source:         %s — %s\n", item.SourceID, truncate(item.SourceTitle, 60))
		fmt.Printf("  target:         %s — %s\n", item.TargetID, truncate(item.TargetTitle, 60))
		fmt.Printf("  created_at:     %s\n", item.CreatedAt)
		fmt.Println()
	}
}

// ─── show ─────────────────────────────────────────────────────────────────────

func cmdConflictsShow(cfg store.Config) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: engram conflicts show <relation_id>")
		exitFunc(1)
		return
	}

	idStr := strings.TrimSpace(os.Args[3])
	relID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: relation_id must be an integer: %v\n", err)
		exitFunc(1)
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	// Scan via ListRelations (no filter, no limit) and find by integer ID.
	// Phase 4 hook: add GetRelationByID to store for O(log N) lookup.
	items, err := s.ListRelations(store.ListRelationsOptions{Limit: 0})
	if err != nil {
		fatal(err)
		return
	}

	var found *store.RelationListItem
	for i := range items {
		if items[i].ID == relID {
			found = &items[i]
			break
		}
	}

	if found == nil {
		fmt.Fprintf(os.Stderr, "error: relation %d not found\n", relID)
		exitFunc(1)
		return
	}

	fmt.Printf("Conflict Detail\n")
	fmt.Printf("  relation_id:     %d\n", found.ID)
	fmt.Printf("  sync_id:         %s\n", found.SyncID)
	fmt.Printf("  relation:        %s\n", found.Relation)
	fmt.Printf("  judgment_status: %s\n", found.JudgmentStatus)
	fmt.Printf("  created_at:      %s\n", found.CreatedAt)
	fmt.Printf("  updated_at:      %s\n", found.UpdatedAt)
	fmt.Println()
	fmt.Printf("  source_id:       %s\n", found.SourceID)
	fmt.Printf("  source_title:    %s\n", found.SourceTitle)
	fmt.Println()
	fmt.Printf("  target_id:       %s\n", found.TargetID)
	fmt.Printf("  target_title:    %s\n", found.TargetTitle)
}

// ─── stats ────────────────────────────────────────────────────────────────────

func cmdConflictsStats(cfg store.Config) {
	args := os.Args[3:]

	var projectFlag string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			if i+1 < len(args) {
				projectFlag = args[i+1]
				i++
			}
		}
	}

	proj := resolveConflictsProject(projectFlag)

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	stats, err := s.GetRelationStats(proj)
	if err != nil {
		fatal(err)
		return
	}

	fmt.Printf("Conflicts Stats (project: %s)\n", proj)
	fmt.Println()

	if len(stats.ByJudgmentStatus) == 0 {
		fmt.Println("  No relations found.")
	} else {
		fmt.Println("  By judgment_status:")
		// Print in a stable order: pending, accepted, rejected, then others.
		for _, status := range []string{"pending", "accepted", "rejected"} {
			if n, ok := stats.ByJudgmentStatus[status]; ok {
				fmt.Printf("    %-12s %d\n", status+":", n)
			}
		}
		for status, n := range stats.ByJudgmentStatus {
			if status != "pending" && status != "accepted" && status != "rejected" {
				fmt.Printf("    %-12s %d\n", status+":", n)
			}
		}
	}

	if len(stats.ByRelation) > 0 {
		fmt.Println()
		fmt.Println("  By relation type:")
		for rel, n := range stats.ByRelation {
			fmt.Printf("    %-20s %d\n", rel+":", n)
		}
	}

	fmt.Println()
	fmt.Printf("  Deferred:    %d\n", stats.DeferredCount)
	fmt.Printf("  Dead:        %d\n", stats.DeadCount)
}

// ─── scan ─────────────────────────────────────────────────────────────────────

func cmdConflictsScan(cfg store.Config) {
	args := os.Args[3:]

	var projectFlag, sinceFlag string
	dryRun := true // default
	apply := false
	maxInsert := 100

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			if i+1 < len(args) {
				projectFlag = args[i+1]
				i++
			}
		case "--since":
			if i+1 < len(args) {
				sinceFlag = args[i+1]
				i++
			}
		case "--dry-run":
			dryRun = true
			apply = false
		case "--apply":
			apply = true
			dryRun = false
		case "--max-insert":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					maxInsert = n
				}
				i++
			}
		case "--semantic", "--concurrency", "--timeout-per-call", "--max-semantic":
			fmt.Fprintf(os.Stderr, "error: %s was removed in 1.24.0: the semantic scan no longer launches a model; judge the candidates with mem_judge\n", args[i])
			exitFunc(1)
			return
		}
	}

	// Explicit mutex enforcement
	if dryRun && apply {
		fmt.Fprintln(os.Stderr, "error: --dry-run and --apply are mutually exclusive")
		exitFunc(1)
		return
	}

	proj := resolveConflictsProject(projectFlag)

	var sinceTime time.Time
	if sinceFlag != "" {
		t, err := time.Parse(time.RFC3339, sinceFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: --since must be RFC3339 format: %v\n", err)
			exitFunc(1)
			return
		}
		sinceTime = t
	}

	opts := store.ScanOptions{
		Project:   proj,
		Since:     sinceTime,
		Apply:     apply,
		MaxInsert: maxInsert,
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()

	result, err := s.ScanProject(opts)
	if err != nil {
		fatal(err)
		return
	}

	fmt.Printf("Conflicts Scan (project: %s)\n", proj)
	fmt.Printf("  inspected:        %d\n", result.Inspected)
	fmt.Printf("  candidates_found: %d\n", result.CandidatesFound)
	fmt.Printf("  already_related:  %d\n", result.AlreadyRelated)
	fmt.Printf("  inserted:         %d\n", result.RelationsInserted)
	fmt.Printf("  dry_run:          %v\n", result.DryRun)

	if result.Capped {
		fmt.Printf("WARNING: max-insert cap of %d reached — stopped early. Re-run to continue.\n", maxInsert)
	}
}
