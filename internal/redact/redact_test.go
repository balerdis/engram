package redact

import (
	"encoding/json"
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
			if again := Redact(got); again != got {
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

func TestValueAlsoPresentEarlierInMatch(t *testing.T) {
	got := Redact("a://fakepassword1:fakepassword1@h")
	if got != "a://fakepassword1:[REDACTED:URL_PASSWORD]@h" {
		t.Fatal(got)
	}
	if Redact(got) != got {
		t.Fatal("not idempotent")
	}
}
