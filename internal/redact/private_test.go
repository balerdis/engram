package redact

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// The Pegasus hook's own unit cases for the <private> scanner (its oracle).
func TestStripPrivateSpans(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"<private><private>x</private></private>", "[REDACTED]"},
		{"a <private>b <private>c</private> d</private> e", "a [REDACTED] e"},
		{"<private>a<private>b</private>c</private>", "[REDACTED]"}, // the lazy regex left "c</private>"
		{"<private>unclosed and </private> then <private>again", "[REDACTED] then [REDACTED]"},
		{"keep <private>secret to the end of the line\nnext line", "keep [REDACTED]\nnext line"},
		{"keep <private>crlf line\r\nnext line", "keep [REDACTED]\r\nnext line"},
		{"<private>\nparagraph one\nstill one\n\nparagraph two", "[REDACTED]\n\nparagraph two"},
		{"a stray </private> close and `<private>` mentioned in prose", "a stray </private> close and `<private>` mentioned in prose"},
		{"</private><private>", "</private>[REDACTED]"},
		{"a <private>x</private> b <PRIVATE>y</Private> c", "a [REDACTED] b [REDACTED] c"},
		{"no tags", "no tags"},
	} {
		if got := StripPrivate(c.in); got != c.want {
			t.Errorf("StripPrivate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNestedPrivateNeverLeavesATail(t *testing.T) {
	in := "x <private>a<private>b</private>SECRETTAIL</private> y"
	if got := Redact(in); got != "x [REDACTED:PRIVATE] y" {
		t.Fatalf("got %q", got)
	}
}

func TestUnclosedPrivateRedactsToEndOfLineOrParagraph(t *testing.T) {
	if got := Redact("note <private>password is hunter2\nnext line stays"); got != "note [REDACTED:PRIVATE]\nnext line stays" {
		t.Fatalf("line: %q", got)
	}
	if got := Redact("<private>\nline one\nline two\n\nvisible"); got != "[REDACTED:PRIVATE]\n\nvisible" {
		t.Fatalf("paragraph: %q", got)
	}
	if got := Redact("a stray </private> stays, `<private>` too"); got != "a stray </private> stays, `<private>` too" {
		t.Fatalf("stray/mention: %q", got)
	}
}

func TestComponentsSplitCamelCase(t *testing.T) {
	for in, want := range map[string][]string{
		"dbPassword":            {"db", "password"},
		"DB_PASSWORD":           {"db", "password"},
		"db-password":           {"db", "password"},
		"DBPassword":            {"db", "password"},
		"myAPIKey":              {"my", "api", "key"},
		"AWS_SECRET_ACCESS_KEY": {"aws", "secret", "access", "key"},
		"id2Token":              {"id2", "token"},
		"token":                 {"token"},
		"__":                    nil,
	} {
		if got := components(in); !reflect.DeepEqual(got, want) {
			t.Errorf("components(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestManyUnclosedPrivateTagsStayLinear(t *testing.T) {
	start := time.Now()
	_ = Redact(strings.Repeat("<private>", 28000))
	_ = Redact(strings.Repeat("<private>\n", 28000))
	_ = Redact(strings.Repeat("<private>\n\n", 20000))
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}
