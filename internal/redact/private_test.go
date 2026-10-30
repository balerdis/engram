package redact

import (
	"reflect"
	"runtime"
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
	if got := Redact("<private>\nline one\nline two\n\nvisible"); got != "\n[REDACTED:PRIVATE]\n\nvisible" {
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

// bestOf is the fastest of runs executions of f. The minimum filters out the
// scheduler and GC noise of a loaded machine; it can only be too high, never
// too low, so a ratio built from two minimums is stable.
func bestOf(runs int, f func()) time.Duration {
	best := time.Duration(1<<63 - 1)
	for i := 0; i < runs; i++ {
		start := time.Now()
		f()
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

// requireLinear runs f on an input of size small and on one factor times
// larger. A linear cost grows about factor times and a quadratic one about
// factor squared, so a limit between the two tells them apart without any
// absolute wall-clock limit. A loaded machine can still spoil one measurement
// (the small run lucky, the large one preempted), so a measurement over the
// limit is repeated: a real quadratic is over it every time, noise is not.
func requireLinear(t *testing.T, name string, small, factor int, limit float64, f func(n int)) {
	t.Helper()
	const runs, attempts = 3, 4
	f(small) // warm up caches and the regexp machinery
	var ratio float64
	for i := 1; i <= attempts; i++ {
		runtime.GC()
		ts := bestOf(runs, func() { f(small) })
		tl := bestOf(runs, func() { f(factor * small) })
		ratio = float64(tl) / float64(ts)
		t.Logf("%s: attempt %d: n=%d %v, n=%d %v, ratio %.1f (limit %.0f)", name, i, small, ts, factor*small, tl, ratio, limit)
		if ratio < limit {
			return
		}
	}
	t.Errorf("%s: %dx the input took %.1fx the time in all %d attempts; linear is about %dx, quadratic about %dx",
		name, factor, ratio, attempts, factor, factor*factor)
}

func TestManyUnclosedPrivateTagsStayLinear(t *testing.T) {
	// StripPrivate has no size cap (MCP mem_suggest_topic_key reaches it): the
	// cost of unclosed tags must be linear. Quadratic took 5 s at 1 MB, 19 s at 2 MB.
	// Its shapes grow 8x: linear is 8x, quadratic 64x, the limit sits at 24x.
	for _, c := range []struct {
		name  string
		small int
		f     func(n int)
	}{
		{"StripPrivate tag", 8000, func(n int) { _ = StripPrivate(strings.Repeat("<private>", n)) }},
		{"StripPrivate tag+spaces", 7000, func(n int) { _ = StripPrivate(strings.Repeat("<private>  ", n)) }},
		{"StripPrivate tag+text", 6000, func(n int) { _ = StripPrivate(strings.Repeat("<private> x\n", n)) }},
	} {
		requireLinear(t, c.name, c.small, 8, 24, c.f)
	}
	// Redact caps its input at the detection cap, so its sizes stay under it.
	// Its detection pass is a little superlinear on its own (allocation and GC:
	// about 12x for 8x the input), so its limit is looser (32x) and the sharp
	// probe for a quadratic scanner is StripPrivate above.
	for _, c := range []struct {
		name  string
		small int
		f     func(n int)
	}{
		{"Redact tag", 3500, func(n int) { _ = Redact(strings.Repeat("<private>", n)) }},
		{"Redact tag+nl", 3500, func(n int) { _ = Redact(strings.Repeat("<private>\n", n)) }},
		{"Redact tag+blank", 2500, func(n int) { _ = Redact(strings.Repeat("<private>\n\n", n)) }},
	} {
		requireLinear(t, c.name, c.small, 8, 32, c.f)
	}
}

// Linear even when no prefilter helps (they are only an optimisation): the
// shapes that were quadratic, with the prefilters off. Each grows 4x: linear is
// about 4x, quadratic about 16x, the limit sits at 10x.
func TestQuadraticShapesStayLinearWithoutPrefilters(t *testing.T) {
	disableGates = true
	defer func() { disableGates = false }()
	for _, c := range []struct {
		name  string
		small int
		f     func(n int)
	}{
		{"a=", 31000, func(n int) { _ = Redact(strings.Repeat("a=", n)) }},
		{"token=f(", 7700, func(n int) { _ = Redact(strings.Repeat("token=f(", n)) }},
		{"token=getX(password=", 1200, func(n int) { _ = Redact(strings.Repeat("token=getX(password=hunter2hunter2 ", n)) }},
		{"begin key", 2200, func(n int) { _ = Redact(strings.Repeat("-----BEGIN PRIVATE KEY-----", n)) }},
		{"long spaces", 50000, func(n int) { _ = Redact("a" + strings.Repeat(" ", n) + "=x") }},
	} {
		requireLinear(t, c.name, c.small, 4, 10, c.f)
	}
}
