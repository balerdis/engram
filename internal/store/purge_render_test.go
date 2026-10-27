package store

import "testing"

// These strings are the output of the pre-extraction printPurge in
// cmd/engram/purge.go, byte for byte.
func TestRenderPurgeGolden(t *testing.T) {
	plan := &PurgePlan{
		Projects:     []string{"lab"},
		WholeProject: true,
		Counts: PurgeCounts{Sessions: 2, Observations: 3, SoftDeleted: 1, Pinned: 1, Prompts: 4, Relations: 5,
			SyncMutations: 6, SyncState: 7},
		Samples:  []PurgeSample{{ID: 9, Kind: "observation", Project: "lab", Label: "lab note", CreatedAt: "2026-01-02 03:04:05"}},
		Warnings: []string{"w1"},
	}
	wantDry := "Purge plan (dry-run, nothing deleted; add --yes to execute)\n" +
		"  projects: lab\n" +
		"  sessions                        2\n" +
		"  observations                    3  (1 soft-deleted, 1 pinned)\n" +
		"  user_prompts                    4\n" +
		"  memory_relations                5\n" +
		"  prompt_tombstones               0\n" +
		"  sync_mutations                  6\n" +
		"  sync_apply_deferred             0\n" +
		"  sync_state (cloud:<proj>)       7\n" +
		"  sync_enrolled_projects          0\n" +
		"  cloud_upgrade_state             0\n" +
		"Samples:\n" +
		"  observation #9 [lab] \"lab note\" (2026-01-02 03:04:05)\n" +
		"WARNING: w1\n"
	if got := RenderPurge(plan, nil); got != wantDry {
		t.Fatalf("pinned dry-run text changed:\n%q\nwant\n%q", got, wantDry)
	}

	res := &PurgeResult{Plan: plan, BackupPath: "/d/backups/engram-purge-x.db", Warnings: []string{"w2"},
		Deleted: PurgeCounts{Sessions: 2, Observations: 3, Prompts: 4, Relations: 5, SyncMutations: 6, SyncState: 7}}
	wantExec := "Backup written: /d/backups/engram-purge-x.db\n" +
		"Purged\n" +
		"  projects: lab\n" +
		"  sessions                        2\n" +
		"  observations                    3  (1 soft-deleted, 1 pinned)\n" +
		"  user_prompts                    4\n" +
		"  memory_relations                5\n" +
		"  prompt_tombstones               0\n" +
		"  sync_mutations                  6\n" +
		"  sync_apply_deferred             0\n" +
		"  sync_state (cloud:<proj>)       7\n" +
		"  sync_enrolled_projects          0\n" +
		"  cloud_upgrade_state             0\n" +
		"Samples:\n" +
		"  observation #9 [lab] \"lab note\" (2026-01-02 03:04:05)\n" +
		"WARNING: w2\n" +
		"Backup kept at /d/backups/engram-purge-x.db\n" +
		"It is not deleted automatically and holds the purged data in clear text; remove it yourself once you no longer need it.\n"
	if got := RenderPurge(plan, res); got != wantExec {
		t.Fatalf("execute text changed:\n%q\nwant\n%q", got, wantExec)
	}

	empty := &PurgePlan{Warnings: []string{"w3"}}
	wantEmpty := "Nothing matched the selection; nothing to purge.\nWARNING: w3\n"
	if got := RenderPurge(empty, nil); got != wantEmpty {
		t.Fatalf("empty text changed: %q", got)
	}
}
