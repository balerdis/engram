package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

const scanFakeKey = "sk-F4K3F4K3F4K3F4K3F4K3F4K3"

func seedLegacyCLI(t *testing.T, cfg store.Config) {
	t.Helper()
	s, err := storeNew(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`INSERT INTO sessions (id, project, directory) VALUES ('s1', 'p', '/work/` + scanFakeKey + `')`,
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash)
		 VALUES ('obs-1', 's1', 'note', 't', 'password=FakePass123 and ` + scanFakeKey + `', 'p', 'project', 'h')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func runRedactScanCLI(t *testing.T, cfg store.Config, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	old := exitFunc
	exitFunc = func(c int) { code = c }
	t.Cleanup(func() { exitFunc = old })
	stdout, stderr = captureOutput(t, func() { cmdRedactScan(cfg, args) })
	return
}

func TestCmdRedactScanDryRunThenYes(t *testing.T) {
	cfg := testConfig(t)
	seedLegacyCLI(t, cfg)

	stdout, stderr, code := runRedactScanCLI(t, cfg, "--scan")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "dry-run") || !strings.Contains(stdout, "content") {
		t.Fatalf("dry-run: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, leak := range []string{"F4K3", "FakePass", scanFakeKey} {
		if strings.Contains(stdout+stderr, leak) {
			t.Fatalf("output leaks a value (%s): %q", leak, stdout)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(cfg.DataDir, "backups", "*")); len(m) != 0 {
		t.Fatalf("dry-run wrote a backup: %v", m)
	}

	stdout, stderr, code = runRedactScanCLI(t, cfg, "--scan", "--yes")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "Backup written:") || strings.Contains(stdout, "F4K3") {
		t.Fatalf("--yes: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	matches, _ := filepath.Glob(filepath.Join(cfg.DataDir, "backups", "engram-redact-*.db"))
	if len(matches) != 1 {
		t.Fatalf("backups: %v", matches)
	}

	stdout, _, _ = runRedactScanCLI(t, cfg, "--scan", "--yes")
	if !strings.Contains(stdout, "Nothing to redact") {
		t.Fatalf("second run: %q", stdout)
	}
	if m, _ := filepath.Glob(filepath.Join(cfg.DataDir, "backups", "engram-redact-*.db")); len(m) != 1 {
		t.Fatalf("idempotent run wrote another backup: %v", m)
	}
}

func TestCmdRedactScanBooleanFlagsRejectValues(t *testing.T) {
	cfg := testConfig(t)
	seedLegacyCLI(t, cfg)
	for _, args := range [][]string{{"--scan", "--yes=false"}, {"--scan=true"}, {"--scan", "--json=0"}, {"--scan", "--nope"}} {
		_, stderr, code := runRedactScanCLI(t, cfg, args...)
		if code != 1 || stderr == "" {
			t.Fatalf("%v: code=%d stderr=%q", args, code, stderr)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(cfg.DataDir, "backups", "*")); len(m) != 0 {
		t.Fatalf("a rejected invocation wrote a backup: %v", m)
	}
	// The data is untouched.
	db, _ := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	defer db.Close()
	var n int
	db.QueryRow(`SELECT count(*) FROM observations WHERE instr(content, 'FakePass123')`).Scan(&n)
	if n != 1 {
		t.Fatal("a rejected invocation changed the data")
	}
}

func TestRedactScanRouting(t *testing.T) {
	if !isRedactScan([]string{"--scan", "--yes"}) || !isRedactScan([]string{"--scan=1"}) || isRedactScan([]string{"--catalog-sha256"}) || isRedactScan(nil) {
		t.Fatal("isRedactScan")
	}
	// --yes on the stdin filter is a usage error, not a silent apply.
	if err := runRedact([]string{"--yes"}, strings.NewReader(""), &strings.Builder{}); err == nil {
		t.Fatal("expected usage error")
	}
}
