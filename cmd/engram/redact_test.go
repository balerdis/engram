package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/redact"
)

func TestRunRedactFiltersStdin(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("key sk-F4K3F4K3F4K3F4K3F4K3F4K3 end\n")
	if err := runRedact(nil, in, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "key [REDACTED:OPENAI_KEY] end\n" {
		t.Fatalf("unexpected output %q", out.String())
	}
}

func TestRunRedactCatalogSHA256(t *testing.T) {
	var out bytes.Buffer
	if err := runRedact([]string{"--catalog-sha256"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != redact.CatalogSHA256() || len(got) != 64 {
		t.Fatalf("unexpected hash %q", got)
	}
}

func TestRunRedactRejectsUnknownArgs(t *testing.T) {
	if err := runRedact([]string{"--nope"}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("expected usage error")
	}
}
