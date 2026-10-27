package store

import (
	"fmt"
	"strings"
)

// RenderPurge formats a purge plan (result == nil, the dry-run) or an executed
// purge as the text `engram purge` prints. The MCP preview tool uses the same
// function, so both show the same plan.
func RenderPurge(plan *PurgePlan, result *PurgeResult) string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	if plan.Empty() {
		p("Nothing matched the selection; nothing to purge.\n")
		for _, w := range plan.Warnings {
			p("WARNING: %s\n", w)
		}
		return b.String()
	}
	counts := plan.Counts
	verb := "Purge plan (dry-run, nothing deleted; add --yes to execute)"
	if result != nil {
		verb = "Purged"
		counts = result.Deleted
		if result.BackupPath != "" {
			p("Backup written: %s\n", result.BackupPath)
		}
	}
	p("%s\n", verb)
	if len(plan.Projects) > 0 {
		p("  projects: %s\n", strings.Join(plan.Projects, ", "))
	}
	row := func(label string, n int, note string) {
		p("  %-26s %6d%s\n", label, n, note)
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
		p("Samples:\n")
		for _, sm := range plan.Samples {
			p("  %s #%d [%s] %q (%s)\n", sm.Kind, sm.ID, sm.Project, sm.Label, sm.CreatedAt)
		}
	}
	warnings := plan.Warnings
	if result != nil {
		warnings = result.Warnings
	}
	for _, w := range warnings {
		p("WARNING: %s\n", w)
	}
	if result != nil && result.BackupPath != "" {
		p("Backup kept at %s\nIt is not deleted automatically and holds the purged data in clear text; remove it yourself once you no longer need it.\n", result.BackupPath)
	}
	return b.String()
}
