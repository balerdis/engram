package redact

import (
	"regexp"
	"regexp/syntax"
	"strings"
)

// Prefilters: cheap necessary conditions checked before each regex pass.
//
// Every pass of detectAndReplace scans the whole text with RE2, and over a
// large text RE2 runs its slow NFA. Most observations hold no credential, so
// most passes find nothing and cost the same as a pass that does. A prefilter
// is a literal-substring test that is TRUE whenever the pass could match: when
// it is false the pass is skipped, and the output is the same by construction.
// They are derived from the catalog patterns at load time (never typed by
// hand), so a re-vendored catalog needs no change here; a pattern the analysis
// cannot bound simply gets no prefilter and always runs.
//
// All needles and all searched text are ASCII-folded (see foldASCII), which
// over-approximates both case-sensitive and (?i) patterns.

// disableGates turns every prefilter off. Only tests set it, to prove that the
// gated output is identical to the plain one.
var disableGates bool

// foldASCII lower-cases ASCII letters and maps the two non-ASCII runes whose
// simple case folding lands on an ASCII letter (U+212A KELVIN SIGN -> k and
// U+017F LONG S -> s), the only ones RE2's (?i) can match with an ASCII
// pattern letter. The result may be shorter than s; it is only for searching.
func foldASCII(s string) string {
	i := 0
	for ; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' || c >= 0x80 {
			break
		}
	}
	if i == len(s) {
		return s
	}
	b := make([]byte, 0, len(s))
	b = append(b, s[:i]...)
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b = append(b, c+('a'-'A'))
		case c == 0xE2 && i+2 < len(s) && s[i+1] == 0x84 && s[i+2] == 0xAA:
			b = append(b, 'k')
			i += 2
		case c == 0xC5 && i+1 < len(s) && s[i+1] == 0xBF:
			b = append(b, 's')
			i++
		default:
			b = append(b, c)
		}
	}
	return string(b)
}

// view caches the folded forms of the text a pass is about to scan. The text
// changes only when a pass replaced something, so the forms are rebuilt then.
type view struct {
	s        string
	folded   string
	stripped string // folded without '_' and '-'
	hasF     bool
	hasS     bool
	init     bool
}

func (v *view) of(s string) *view {
	if !v.init || v.s != s {
		*v = view{s: s, init: true}
	}
	return v
}

func (v *view) fold() string {
	if !v.hasF {
		v.folded, v.hasF = foldASCII(v.s), true
	}
	return v.folded
}

func (v *view) strip() string {
	if !v.hasS {
		f := v.fold()
		if strings.ContainsAny(f, "_-") {
			b := make([]byte, 0, len(f))
			for i := 0; i < len(f); i++ {
				if c := f[i]; c != '_' && c != '-' {
					b = append(b, c)
				}
			}
			v.stripped = string(b)
		} else {
			v.stripped = f
		}
		v.hasS = true
	}
	return v.stripped
}

// gate is a prefilter: ok reports whether the pass may match the text.
type gate struct {
	needles []string // folded; any one present lets the pass run
}

func (g *gate) ok(v *view) bool {
	if g == nil || disableGates {
		return true
	}
	f := v.fold()
	for _, n := range g.needles {
		if strings.Contains(f, n) {
			return true
		}
	}
	return false
}

// literalGate derives a gate from a compiled pattern: a set of literals such
// that every match contains at least one of them. nil when none can be proven.
func literalGate(re *regexp.Regexp) *gate {
	tree, err := syntax.Parse(re.String(), syntax.Perl)
	if err != nil {
		return nil
	}
	set, ok := mustLiterals(tree)
	if !ok || len(set) == 0 {
		return nil
	}
	return &gate{needles: set}
}

func shortest(set []string) int {
	m := -1
	for _, s := range set {
		if m < 0 || len(s) < m {
			m = len(s)
		}
	}
	return m
}

// mustLiterals returns literals of which every match of n contains at least
// one, folded; ok is false when n has no such bound.
func mustLiterals(n *syntax.Regexp) ([]string, bool) {
	switch n.Op {
	case syntax.OpLiteral:
		lit := string(n.Rune)
		for i := 0; i < len(lit); i++ {
			if lit[i] >= 0x80 {
				return nil, false
			}
		}
		return []string{foldASCII(lit)}, lit != ""
	case syntax.OpCapture, syntax.OpPlus:
		return mustLiterals(n.Sub[0])
	case syntax.OpRepeat:
		if n.Min >= 1 {
			return mustLiterals(n.Sub[0])
		}
		return nil, false
	case syntax.OpConcat:
		var best []string
		found := false
		for _, sub := range n.Sub {
			set, ok := mustLiterals(sub)
			if !ok {
				continue
			}
			if !found || shortest(set) > shortest(best) {
				best, found = set, true
			}
		}
		return best, found
	case syntax.OpAlternate:
		var out []string
		for _, sub := range n.Sub {
			set, ok := mustLiterals(sub)
			if !ok {
				return nil, false
			}
			out = append(out, set...)
		}
		return out, len(out) > 0
	}
	return nil, false
}

// keyGate is the prefilter of the key-context passes (JSON pair, quoted and
// unquoted key=value). Those only replace when matchesCatalogKey accepts the
// key, and that needs the key, lower-cased and without '_' and '-', to end with
// the joined components of a catalog key; the regex needs the key to be
// followed by `"`/spaces and then ':' or '='. So some folded, separator-free
// catalog key must be followed (past quotes and any whitespace, ASCII or not)
// by ':' or '='.
type keyGate struct{ needles []string }

func newKeyGate(keys [][]string) *keyGate {
	g := &keyGate{}
	for _, kp := range keys {
		n := strings.Join(kp, "")
		if n == "" {
			return nil
		}
		for i := 0; i < len(n); i++ {
			if n[i] >= 0x80 {
				return nil
			}
		}
		g.needles = append(g.needles, foldASCII(n))
	}
	if len(g.needles) == 0 {
		return nil
	}
	return g
}

func gapByte(c byte) bool {
	return c == '"' || c == '\'' || c == '\\' || c >= 0x80 || c >= 9 && c <= 13 || c >= 0x1c && c <= 0x20
}

func (g *keyGate) ok(v *view) bool {
	if g == nil || disableGates {
		return true
	}
	t := v.strip()
	for _, n := range g.needles {
		from := 0
		for {
			i := strings.Index(t[from:], n)
			if i < 0 {
				break
			}
			j := from + i + len(n)
			// A trailing version or index (`token2`, `PASSWORD_V2`) is not part of
			// the key's words: `v?<digits>` may follow the key.
			if j+1 < len(t) && t[j] == 'v' && t[j+1] >= '0' && t[j+1] <= '9' {
				j++
			}
			for j < len(t) && t[j] >= '0' && t[j] <= '9' {
				j++
			}
			for j < len(t) && gapByte(t[j]) {
				j++
			}
			if j < len(t) && (t[j] == ':' || t[j] == '=') {
				return true
			}
			from += i + 1
		}
	}
	return false
}

// gates holds the prefilter of each pass, built once from the loaded catalog.
var gates = buildGates()

type passGates struct {
	known     []*gate
	headers   []*gate
	url       *gate
	jsonEsc   *gate // pass 6b
	quotedEsc *gate // pass 7b
	privOpen  *gate
	keys      *keyGate
}

func buildGates() *passGates {
	g := &passGates{
		url: literalGate(cat.urlPattern),
		// Every depth's delimiter (2^k-1 backslashes and a quote) contains the
		// depth-1 one, so its literal gates all of them.
		jsonEsc:   literalGate(cat.jsonPairEsc[0]),
		quotedEsc: literalGate(cat.quotedEsc[0]),
		privOpen:  literalGate(rePrivTag),
		keys:      newKeyGate(cat.keys),
	}
	for _, k := range cat.known {
		g.known = append(g.known, literalGate(k.pattern))
	}
	for _, re := range hdrRes {
		g.headers = append(g.headers, literalGate(re))
	}
	return g
}
