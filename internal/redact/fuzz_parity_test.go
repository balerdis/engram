package redact

import (
	"encoding/json"
	"os"
	"testing"
)

// TestFuzzParity compares Redact with outputs the Pegasus hook produced for
// random mixes: set REDACT_FUZZ to a file made by a generator (a JSON array of
// {input, expected}). It is skipped without it.
func TestFuzzParity(t *testing.T) {
	path := os.Getenv("REDACT_FUZZ")
	if path == "" {
		t.Skip("REDACT_FUZZ not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Input, Expected string }
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	diffs := 0
	for _, c := range cases {
		if got := Redact(c.Input); got != c.Expected {
			diffs++
			if diffs <= 10 {
				t.Errorf("\ninput:  %q\nhook:   %q\ngo:     %q", c.Input, c.Expected, got)
			}
		}
		if got := redactPlain(c.Input); got != c.Expected {
			t.Errorf("without prefilters differs: %q", c.Input)
			break
		}
	}
	t.Logf("%d cases, %d differences", len(cases), diffs)
}

// The reference treats U+0130 and U+0131 as `i` in case-insensitive patterns.
func TestDottedIMatchesLikeTheReference(t *testing.T) {
	for in, want := range map[string]string{
		"<prıvate>abcd1234</prıvate>":        "[REDACTED:PRIVATE]",
		"<PRİVATE>abcd1234</private>":        "[REDACTED:PRIVATE]",
		"Authorızatıon: Bearer abcdefgh1234": "Authorızatıon: Bearer [REDACTED:AUTHORIZATION]",
		"x-api-keyı: abcdefgh1234":           "x-api-keyı: abcdefgh1234",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}
