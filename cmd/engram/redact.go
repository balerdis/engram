package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/redact"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// runRedact implements `engram redact`. With --catalog-sha256 it prints the
// hash of the embedded credential catalog (so a release can check that its
// copy matches). Without flags it filters stdin to stdout with the same
// redactor the server applies to everything it stores.
func runRedact(args []string, in io.Reader, out io.Writer) error {
	switch {
	case len(args) == 0:
		raw, err := io.ReadAll(in)
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, redact.Redact(string(raw)))
		return err
	case len(args) == 1 && args[0] == "--catalog-sha256":
		_, err := fmt.Fprintln(out, redact.CatalogSHA256())
		return err
	default:
		return errors.New("usage: engram redact [--catalog-sha256]  (filters stdin to stdout)\n       engram redact --scan [--yes] [--json]  (re-redact the stored data)")
	}
}

func printRedactScanUsage() {
	fmt.Fprint(os.Stderr, `usage: engram redact --scan [--yes] [--json]

Re-redact what was stored before the redactor covered it: credentials in the
free text saved before 1.22.0 and in type, directory and project (1.22.1).
Dry-run by default: it prints how many rows and fields would change, per table,
and never a value.

  --scan    Scan the stored data (sessions, observations, user_prompts,
            memory_relations).
  --yes     Apply the changes. A verified backup (0600, kept) is written to the
            data dir's backups/ folder first; if it fails nothing is changed.
            The search index is kept in sync. A second run changes nothing.
  --json    Machine-readable output.

Flags take no value (--yes=false is rejected).
`)
}

// parseRedactScanArgs reads the flags that follow --scan. Boolean flags reject
// "=value": "--yes=false" must never run as "--yes".
func parseRedactScanArgs(args []string) (yes, jsonOut bool, err error) {
	seenScan := false
	for _, a := range args {
		name, _, hasVal := strings.Cut(a, "=")
		switch name {
		case "--scan", "--yes", "--json":
			if hasVal {
				return false, false, fmt.Errorf("%s takes no value, got %q", name, a)
			}
		}
		switch name {
		case "--scan":
			seenScan = true
		case "--yes":
			yes = true
		case "--json":
			jsonOut = true
		default:
			return false, false, fmt.Errorf("unknown redact argument %q", a)
		}
	}
	if !seenScan {
		return false, false, errors.New("--yes and --json only apply to --scan")
	}
	return yes, jsonOut, nil
}

// isRedactScan reports whether the redact arguments ask for the stored-data
// scan (which needs the data dir, unlike the stdin filter).
func isRedactScan(args []string) bool {
	for _, a := range args {
		if name, _, _ := strings.Cut(a, "="); name == "--scan" {
			return true
		}
	}
	return false
}

// cmdRedactScan implements `engram redact --scan`.
func cmdRedactScan(cfg store.Config, args []string) {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			printRedactScanUsage()
			return
		}
	}
	yes, jsonOut, err := parseRedactScanArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		printRedactScanUsage()
		exitFunc(1)
		return
	}
	s, err := storeNew(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram: %s\n", err)
		exitFunc(1)
		return
	}
	defer s.Close()
	res, err := s.RedactScan(yes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram: %s\n", err)
		exitFunc(1)
		return
	}
	if jsonOut {
		out, merr := jsonMarshalIndent(map[string]any{"dry_run": !yes, "result": res}, "", "  ")
		if merr != nil {
			fmt.Fprintf(os.Stderr, "engram: %s\n", merr)
			exitFunc(1)
			return
		}
		fmt.Println(string(out))
		return
	}
	printRedactScan(res, yes)
}

func printRedactScan(res *store.RedactScanResult, yes bool) {
	switch {
	case res.RowsChanged == 0:
		fmt.Println("Nothing to redact: no stored row changes.")
	case yes:
		fmt.Println("Redacted the stored data")
		if res.BackupPath != "" {
			fmt.Printf("Backup written: %s\n", res.BackupPath)
		}
	default:
		fmt.Println("Redaction scan (dry-run, nothing changed; add --yes to apply)")
	}
	for _, t := range res.Tables {
		label := "rows to change"
		if yes {
			label = "rows changed"
		}
		fmt.Printf("  %-18s scanned %6d  %s %6d\n", t.Table, t.Scanned, label, t.RowsChanged)
		for _, f := range t.FieldNames() {
			fmt.Printf("      %-16s %6d\n", f, t.Fields[f])
		}
	}
	if c := res.Collisions; c.Any() {
		verb := "would collide"
		if yes {
			verb = "now collide"
		}
		fmt.Printf("NOTE: after the rewrite rows %s (nothing is merged): %d observation(s) share a topic_key, %d are identical, %d project name(s) land on an existing one\n", verb, c.TopicKey, c.Identical, c.ProjectRenames)
	}
	if len(res.LegacySyncRows) > 0 {
		names := make([]string, 0, len(res.LegacySyncRows))
		for n := range res.LegacySyncRows {
			names = append(names, n)
		}
		sort.Strings(names)
		var parts []string
		for _, n := range names {
			parts = append(parts, fmt.Sprintf("%s: %d", n, res.LegacySyncRows[n]))
		}
		fmt.Printf("NOTE: the legacy sync tables still hold rows (%s); the scan does not rewrite them, run `engram purge --sync-leftovers` to delete them\n", strings.Join(parts, ", "))
	}
	for _, w := range res.Warnings {
		fmt.Printf("WARNING: %s\n", w)
	}
	if yes && res.BackupPath != "" {
		fmt.Printf("Backup kept at %s: it still holds the OLD values in clear text; it is not deleted automatically, remove it yourself once you no longer need it.\n", res.BackupPath)
	}
}
