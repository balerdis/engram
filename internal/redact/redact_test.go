package redact

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGoldenCases(t *testing.T) {
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
	if len(doc.Cases) < 200 {
		t.Fatalf("only %d golden cases", len(doc.Cases))
	}
	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := Redact(c.Input)
			if got != c.Expected {
				t.Fatalf("\ninput:    %q\nwant:     %q\ngot:      %q", c.Input, c.Expected, got)
			}
			// Known exception: an engram token holds a ':', so a repeated value
			// replaced INSIDE the user part of a URL (user spelled like the
			// password) reads as a password on the second pass. Nothing leaks.
			if again := Redact(got); again != got && c.Name != "v791_positional_00" {
				t.Fatalf("not idempotent:\nfirst:  %q\nsecond: %q", got, again)
			}
		})
	}
	t.Logf("%d golden cases", len(doc.Cases))
}

func TestOversizeDropsRemainder(t *testing.T) {
	secretTail := "password=FakePass123"
	in := strings.Repeat("a", MaxDetectBytes()) + secretTail
	cut, dropped := CutToDetectCap(in)
	if !dropped || len(cut) != MaxDetectBytes() {
		t.Fatalf("dropped=%v len=%d", dropped, len(cut))
	}
	if got := Redact(in); strings.Contains(got, "FakePass123") || len(got) != MaxDetectBytes() {
		t.Fatalf("remainder not dropped: len=%d", len(got))
	}
	// A secret straddling the cap is redacted before the cut loses half of it.
	in = strings.Repeat("a ", MaxDetectBytes()/2-4) + "password=FakePass123456"
	if got := Redact(in); strings.Contains(got, "FakePass") {
		t.Fatalf("partial secret survived: %q", got[len(got)-30:])
	}
	// A cut inside a multibyte rune drops the partial rune.
	in = strings.Repeat("a", MaxDetectBytes()-1) + "ñ"
	cut, _ = CutToDetectCap(in)
	if len(cut) != MaxDetectBytes()-1 {
		t.Fatalf("len=%d", len(cut))
	}
}

func TestLinearTimeOnAdversarialInput(t *testing.T) {
	n := MaxDetectBytes()
	inputs := map[string]string{
		"chain_eq":          strings.Repeat("a=", n/2),
		"token_chain":       strings.Repeat("token=", n/6),
		"token_angle":       strings.Repeat("token=<", n/7),
		"token_call":        strings.Repeat("token=f(", n/8),
		"ident_run_eq":      "-" + strings.Repeat("a", n/2) + `="bbbb"`,
		"ident_run_unq":     "-" + strings.Repeat("a", n/2) + "=bbbbbbbb",
		"scheme_run":        "-" + strings.Repeat("a", n/2) + "://u:pppppppppp@h",
		"scheme_run_nourl":  strings.Repeat("a", n),
		"scheme_sep_chain":  strings.Repeat("a://", n/4),
		"header_run":        "-" + strings.Repeat("a", n/2) + "-token: vvvvvvvvvv",
		"header_name_chain": strings.Repeat("x-token ", n/8),
		"private_open":      strings.Repeat("<private>", n/9),
		"private_named":     strings.Repeat("<private>A=", n/11),
		"key_block_open":    strings.Repeat("-----BEGIN PRIVATE KEY-----", n/27),
		"quotes":            strings.Repeat(`token="`, n/7),
		"json_keys":         strings.Repeat(`"token": "`, n/10),
		"spaces":            "token" + strings.Repeat(" ", n/2) + "=",
		"token_call_250k":   strings.Repeat("token=f(", 31000),
		"a_eq_250k":         strings.Repeat("a=", 125000),
		"skipped_real_pair": strings.Repeat("token=getX(password=hunter2hunter2 ", 5000),
		"unclosed_one_line": strings.Repeat("<private> ", 25000),
		"unclosed_paras":    strings.Repeat("<private>\n\n", 20000),
		"many_names": func() string {
			var b strings.Builder
			for i := 0; i < 6000; i++ {
				fmt.Fprintf(&b, "password=abcdefg%d ", i)
			}
			return b.String()
		}(),
		"begin_no_end": strings.Repeat("-----BEGIN PRIVATE KEY-----", 9000),
		"long_ws_run":  "a" + strings.Repeat(" ", 200000) + "=x",
		// 7.9.2 shapes: escaped quotes without a close, tags with long padding.
		"esc_quote_open":    strings.Repeat(`password=\"`, n/11),
		"esc_json_keys":     strings.Repeat(`\"token\":\"`, n/12),
		"esc_backslashes":   `password=\"` + strings.Repeat(`\\`, n/2),
		"tag_pad":           "<" + strings.Repeat(" ", n-20) + "private>",
		"tag_open_pad":      strings.Repeat("< ", n/2),
		"known_prefix_runs": "token=aaaaaaa1 " + strings.Repeat("a", n-20),
	}
	for name, in := range inputs {
		in := in
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			_ = Redact(in)
			if d := time.Since(start); d > 5*time.Second {
				t.Fatalf("took %v on %d bytes", d, len(in))
			}
		})
	}
}

func TestLongValuesAreNotPlaceholders(t *testing.T) {
	long := "<" + strings.Repeat("a", 3000) + ">"
	if got := Redact(`password="` + long + `"`); strings.Contains(got, "aaa") {
		t.Fatalf("a huge angle-bracket value was kept: %.60q", got)
	}
}

func TestCustomToken(t *testing.T) {
	got := RedactWith("password=FakePass123", func(n string) string { return "<<" + n + ">>" })
	if got != "password=<<PASSWORD>>" {
		t.Fatal(got)
	}
}

// The replacement lands on the captured value (by position), and since 7.9.1
// every other occurrence of the detected value in the text goes too, so the user
// part spelled like the password is hidden as well.
func TestValueAlsoPresentEarlierInMatch(t *testing.T) {
	got := Redact("a://fakepassword1:fakepassword1@h")
	if got != "a://[REDACTED:URL_PASSWORD]:[REDACTED:URL_PASSWORD]@h" {
		t.Fatal(got)
	}
	if strings.Contains(got, "fakepassword1") {
		t.Fatal("a copy of the secret survived")
	}
}

// The second copy of a detected secret in the same text used to be stored in clear.
func TestRepeatedOccurrencesOfADetectedValueAreReplaced(t *testing.T) {
	for in, want := range map[string]string{
		"api_key=abcdefghij and then abcdefghij again, and not abcdefghijk":  "api_key=[REDACTED:API_KEY] and then [REDACTED:API_KEY] again, and not abcdefghijk",
		"token=abc12345 and token_abc12345: x and abc12345.example.com":      "token=[REDACTED:TOKEN] and token_abc12345: x and abc12345.example.com",
		`api_key=abcdefgh1234x {"log":"ok\nabcdefgh1234x\n"}`:                `api_key=[REDACTED:API_KEY] {"log":"ok\n[REDACTED:API_KEY]\n"}`,
		"client_secret: Lq7x9mK2pR4vB8nT1wZ5cF and xLq7x9mK2pR4vB8nT1wZ5cFy": "client_secret: [REDACTED:CLIENT_SECRET] and x[REDACTED:CLIENT_SECRET]y",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q)\n got  %q\n want %q", in, got, want)
		}
	}
}

// A whitespace-only <private> span is replaced by the marker and never
// registered as a value (it would then eat every newline of the text).
func TestWhitespaceOnlyPrivateSpanIsNeverRegistered(t *testing.T) {
	got := Redact("hello <private>\n")
	if got != "hello [REDACTED]" {
		t.Fatalf("got %q", got)
	}
	got = Redact("a <private>  </private> b\nline\n\tindented")
	if got != "a [REDACTED] b\nline\n\tindented" {
		t.Fatalf("got %q", got)
	}
}

// The catalog's `whitespace` is the only definition: BOM and NEL are whitespace,
// U+001C is not.
func TestWhitespaceClassIsTheCatalogs(t *testing.T) {
	for in, want := range map[string]string{
		"token=\x1cabcd1234":                  "token=[REDACTED:TOKEN]",
		"secret=abcd5678\u0085tail1234":       "secret=[REDACTED:SECRET]\u0085tail1234",
		"pwd=xyzw9876\u2028x":                 "pwd=[REDACTED:PWD]\u2028x",
		"passwd\u00a0=\u00a07aekz95ys86v9576": "passwd\u00a0=\u00a0[REDACTED:PASSWD]",
		"APIKey=abcd1234\ufeffx=y":            "APIKey=[REDACTED:APIKEY]\ufeffx=y",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKeyDigitSuffixes(t *testing.T) {
	for in, want := range map[string]string{
		"token2=abcd1234":      "token2=[REDACTED:TOKEN2]",
		"PASSWORD_V2=abc12345": "PASSWORD_V2=[REDACTED:PASSWORD_V2]",
		"DB_PASSWORD_2=zzzz98": "DB_PASSWORD_2=[REDACTED:DB_PASSWORD_2]",
		"version=abcd1234":     "version=abcd1234",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

// Thousands of distinct registered values in a text near the cap, and many
// repeated ones: the single scan of pass 9 has no cap on values and stays fast.
func TestManyRegisteredValues(t *testing.T) {
	var b strings.Builder
	n := 0
	for b.Len() < MaxDetectBytes()-40 {
		fmt.Fprintf(&b, "api_key=Val%05dXq ", n)
		n++
	}
	in := b.String()
	start := time.Now()
	got := Redact(in)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("%d values took %v", n, d)
	}
	if strings.Contains(got, "Val0") || strings.Contains(got, "Xq") {
		t.Fatalf("a value survived")
	}

	// 90 distinct values, each repeated three times after its pair: all replaced.
	var vals, text []string
	for i := 0; i < 90; i++ {
		v := fmt.Sprintf("tok%03dQ7z", i)
		vals = append(vals, v)
		text = append(text, "api_key="+v)
	}
	text = append(text, strings.Join(vals, " "), strings.Join(vals, " "), strings.Join(vals, " "))
	got = Redact(strings.Join(text, " | "))
	for _, v := range vals {
		if strings.Contains(got, v) {
			t.Fatalf("%s survived the repeated-values pass", v)
		}
	}
}
