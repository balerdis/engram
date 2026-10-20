package redact

import (
	"encoding/json"
	"math/rand"
	"os"
	"strings"
	"testing"
	"unicode"
)

// redactPlain is Redact with every prefilter off.
func redactPlain(s string) string {
	disableGates = true
	defer func() { disableGates = false }()
	return Redact(s)
}

// TestFoldOnlyKAndSLeaveASCII pins the assumption behind foldASCII: of the
// ASCII letters, only k and s have a non-ASCII member in their simple-fold
// orbit, and those members are U+212A and U+017F.
func TestFoldOnlyKAndSLeaveASCII(t *testing.T) {
	for c := 'a'; c <= 'z'; c++ {
		for r := unicode.SimpleFold(c); r != c; r = unicode.SimpleFold(r) {
			if r < 0x80 {
				continue
			}
			if !(c == 'k' && r == 0x212A || c == 's' && r == 0x17F) {
				t.Fatalf("%q folds to unexpected %U", c, r)
			}
		}
	}
	if foldASCII("AKey ſ") != "akey s" {
		t.Fatalf("got %q", foldASCII("AKey ſ"))
	}
}

// TestPrefiltersKeepOutput proves the prefilters change no output: the golden
// inputs, the benchmark corpora and a seeded random mix of catalog fragments
// must redact identically with and without them.
func TestPrefiltersKeepOutput(t *testing.T) {
	data, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct{ Name, Input, Expected string } `json:"cases"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var inputs []string
	for _, c := range doc.Cases {
		inputs = append(inputs, c.Input, c.Expected)
	}
	inputs = append(inputs, benchProse(20<<10), benchMix(20<<10, false),
		"Kelvin: Key=Key123456 ſecret=abcdefgh1234 X-ſignature: abcdefgh1234",
		"x-tokKn: abcdefghijkl", "TOKEN = abcdefgh", "\"api_key\" : \"abcdefgh12\"")
	// Random mixes of fragments that exercise every gate, in any order and
	// case, glued to identifiers, whitespace and separators.
	frag := []string{"token", "Token", "PASSWORD", "api-key", "api_key", "apiKey", "to_ken", "secret", "pwd", "signature",
		"=", ":", " = ", ": ", "\"", "'", "\n", "  ", " ", "_", "-", "://", "user:", "pw12345678", "@h/", "sk-", "ghp_",
		"AKIA", "xoxb-", "eyJ", ".", "-----BEGIN PRIVATE KEY-----", "-----END PRIVATE KEY-----", "abcdefghijklmnopqrstu",
		"Authorization", "Bearer ", "Basic ", "x-api-key", "X-Auth-Token", "-token", "-signature-256", "<private>",
		"</PRIVATE>", "<private>A=", "[REDACTED:TOKEN]", "[REDACTED]", "dbPassword", "myAPIKey", "token2", "PASSWORD_V2", "DB_PASSWORD_2", "password1", "\\n", "\\t", "%0A", "%20", "\u0041", "\ufeff", "\u0085", "\x1c", "\u2028", "\u00a0", "abcdefgh1234x", "Lq7x9mK2pR4vB8nT1wZ5cF", "os.environ.get(", "lexer.get_token()", "Passw0rd(2024", "<private>\n", "<private>   ", "-----BEGIN PRIVATE KEY-----\n", "-----END PRIVATE KEY-----", "hunter2hunter2", "secret_key", "AWS_SECRET_ACCESS_KEY", "sk-ant-", "sk-proj-", "glpat-", "AIza", "npm_", "\\\"", "\\'", "`<private>`", "-----BEGIN PGP PRIVATE KEY BLOCK-----", "MIIEvQIBADANBgkqhkiG9w0BAQEFAAS", "\r\n", "\n\n", "1234", "null", "${X}", "<x>", "foo(", ")", "é", "K", "ſ"}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 3000; i++ {
		var b strings.Builder
		for n := rng.Intn(14) + 1; n > 0; n-- {
			b.WriteString(frag[rng.Intn(len(frag))])
		}
		inputs = append(inputs, b.String())
	}
	for _, in := range inputs {
		if got, want := Redact(in), redactPlain(in); got != want {
			t.Fatalf("prefilter changed the output\ninput: %q\nplain: %q\ngated: %q", in, want, got)
		}
	}
}

// TestGatesAreDerived checks that the catalog yields a prefilter for every
// pass that can have one (a re-vendored catalog that loses them is slower, not
// wrong, but the budget in the docs would no longer hold).
func TestGatesAreDerived(t *testing.T) {
	if gates.keys == nil || gates.url == nil || gates.privOpen == nil {
		t.Fatalf("missing gate: %+v", gates)
	}
	for i, g := range gates.known {
		if g == nil {
			t.Errorf("known format %q has no prefilter", cat.known[i].name)
		}
	}
	for i, g := range gates.headers {
		if g == nil {
			t.Errorf("header rule %d has no prefilter", i)
		}
	}
}
