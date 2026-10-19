package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The embedded OpenCode plugin is a copy of plugin/opencode/engram.ts made by
// `go generate ./internal/setup/`. A stale copy installs an old plugin, so the
// two must be byte-identical.
func TestEmbeddedOpenCodePluginMatchesSource(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "plugin", "opencode", "engram.ts"))
	if err != nil {
		t.Fatalf("read plugin source: %v", err)
	}
	embedded, err := openCodeFS.ReadFile("plugins/opencode/engram.ts")
	if err != nil {
		t.Fatalf("read embedded copy: %v", err)
	}
	if !bytes.Equal(src, embedded) {
		t.Fatal("internal/setup/plugins/opencode/engram.ts differs from plugin/opencode/engram.ts: run `go generate ./internal/setup/` and commit the result")
	}
}

// Everything under plugins/ must have a source of truth in plugin/ and match it.
func TestEveryEmbeddedPluginAssetHasAMatchingSource(t *testing.T) {
	root := "plugins"
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		src, err := os.ReadFile(filepath.Join("..", "..", "plugin", rel))
		if err != nil {
			t.Errorf("%s has no source in plugin/%s: %v", p, rel, err)
			return nil
		}
		emb, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.Equal(src, emb) {
			t.Errorf("%s differs from plugin/%s: run `go generate ./internal/setup/`", p, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
