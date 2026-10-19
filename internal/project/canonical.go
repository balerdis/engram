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
	n := name
	// One round is redact + lowercase + trim + collapse. Lowercasing can turn
	// the text into something the redactor detects on the next round (a known
	// token format is case sensitive: "GHP_Authorizationtokenuser" lowercases
	// to a `ghp_` token), so the rounds repeat until the name stops changing:
	// only then is Canonical idempotent, which the stored identity needs.
	for round := 0; ; round++ {
		next := canonicalRound(n)
		if next == n {
			break
		}
		n = next
		if round >= maxCanonicalRounds {
			// Does not settle (never seen): a fixed constant is safer than a name
			// that would change on the next write.
			n = "[redacted]"
			break
		}
	}
	if n == name {
		return n, ""
	}
	return n, fmt.Sprintf("⚠️ Project name normalized: %q → %q", shown, n)
}

// maxCanonicalRounds bounds the fixed-point iteration; two rounds settle every
// case found, the rest is margin.
const maxCanonicalRounds = 4

func canonicalRound(name string) string {
	n := strings.TrimSpace(strings.ToLower(redact.Redact(name)))
	for strings.Contains(n, "--") {
		n = strings.ReplaceAll(n, "--", "-")
	}
	for strings.Contains(n, "__") {
		n = strings.ReplaceAll(n, "__", "_")
	}
	return n
}
