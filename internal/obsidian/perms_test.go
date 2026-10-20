//go:build !windows

package obsidian

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func permsStore() *mockStore {
	return &mockStore{exportData: &store.ExportData{
		Sessions: []store.Session{{ID: "s1", Project: "eng"}},
		Observations: []store.Observation{{
			ID: 1, SessionID: "s1", Type: "bugfix", Title: "Fixed auth", Content: "body", Scope: "project",
			CreatedAt: "2026-01-01T10:00:00Z", UpdatedAt: "2026-01-01T10:00:00Z", Project: strPtr("eng"),
		}},
		Prompts: []store.Prompt{},
	}}
}

// What the exporter creates is private; what already existed keeps its mode.
func TestExportCreatesPrivateFilesAndDirs(t *testing.T) {

	vault := filepath.Join(t.TempDir(), "newvault") // does not exist: the exporter creates it
	if _, err := NewExporter(permsStore(), ExportConfig{VaultPath: vault, GraphConfig: GraphConfigForce}).Export(); err != nil {
		t.Fatal(err)
	}
	n := 0
	_ = filepath.WalkDir(vault, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		fi, _ := d.Info()
		want := fs.FileMode(0o600)
		if d.IsDir() {
			want = 0o700
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v", p, fi.Mode().Perm(), want)
		}
		n++
		return nil
	})
	if n < 5 {
		t.Fatalf("expected a populated vault, walked %d entries", n)
	}
}

func TestExportLeavesExistingVaultModesAlone(t *testing.T) {

	vault := t.TempDir()
	if err := os.Chmod(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	eng := filepath.Join(vault, "engram")
	if err := os.MkdirAll(eng, 0o755); err != nil || os.Chmod(eng, 0o755) != nil {
		t.Fatal(err)
	}
	if _, err := NewExporter(permsStore(), ExportConfig{VaultPath: vault}).Export(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{vault, eng} {
		if fi, _ := os.Stat(d); fi.Mode().Perm() != 0o755 {
			t.Errorf("%s: existing dir changed to %v", d, fi.Mode().Perm())
		}
	}
	// A rewrite keeps the mode of a note that already exists.
	var note string
	_ = filepath.WalkDir(eng, func(p string, d fs.DirEntry, _ error) error {
		if !d.IsDir() && filepath.Ext(p) == ".md" && note == "" {
			note = p
		}
		return nil
	})
	if note == "" {
		t.Fatal("no note written")
	}
	if err := os.Chmod(note, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExporter(permsStore(), ExportConfig{VaultPath: vault, Force: true}).Export(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(note); fi.Mode().Perm() != 0o644 {
		t.Errorf("existing note changed to %v", fi.Mode().Perm())
	}
}
