package project

import (
	"fmt"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/redact"
)

// Canonical is the single function that turns a project name into the form
// that is stored and compared everywhere: trimmed, lowercase, with runs of "-"
// and "_" collapsed. store.NormalizeProject and the detector delegate to it.
//
// A credential inside the name is replaced by its [REDACTED:NAME] placeholder
// BEFORE lowercasing (the known token formats are case sensitive), so a name
// never carries a secret into the database or a lookup. The name is also a
// lookup key, so redaction must be deterministic: it is a pure function of the
// text (no state, no randomness) and Canonical is idempotent, which keeps the
// identity of a project stable across writes and reads.
//
// It returns the canonical name and a warning when the input changed (empty
// otherwise); the warning shows the redacted input, never the credential. An
// empty input returns ("", "").
func Canonical(name string) (canonical string, warning string) {
	if name == "" {
		return "", ""
	}
	shown := redact.Redact(name)
	n := strings.TrimSpace(strings.ToLower(shown))
	for strings.Contains(n, "--") {
		n = strings.ReplaceAll(n, "--", "-")
	}
	for strings.Contains(n, "__") {
		n = strings.ReplaceAll(n, "__", "_")
	}
	if n == name {
		return n, ""
	}
	return n, fmt.Sprintf("⚠️ Project name normalized: %q → %q", shown, n)
}
