package project

import (
	"fmt"
	"strings"
)

// Canonical is the single function that turns a project name into the form
// that is stored and compared everywhere: trimmed, lowercase, with runs of "-"
// and "_" collapsed. store.NormalizeProject and the detector delegate to it.
//
// It returns the canonical name and a warning when the input changed (empty
// otherwise). An empty input returns ("", "").
func Canonical(name string) (canonical string, warning string) {
	if name == "" {
		return "", ""
	}
	n := strings.TrimSpace(strings.ToLower(name))
	for strings.Contains(n, "--") {
		n = strings.ReplaceAll(n, "--", "-")
	}
	for strings.Contains(n, "__") {
		n = strings.ReplaceAll(n, "__", "_")
	}
	if n == name {
		return n, ""
	}
	return n, fmt.Sprintf("⚠️ Project name normalized: %q → %q", name, n)
}
