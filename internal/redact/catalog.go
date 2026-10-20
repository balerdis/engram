package redact

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"regexp/syntax"
	"sort"
	"strconv"
	"strings"
)

// catalogJSON is a byte-for-byte vendored copy of the Pegasus Harness
// credential-transport catalog. Re-vendor it only with `make sync-catalog`
// and update the pinned hash in catalog_test.go in the same change.
//
//go:embed catalog.json
var catalogJSON []byte

// CatalogSHA256 returns the hex SHA-256 of the embedded catalog.
func CatalogSHA256() string {
	sum := sha256.Sum256(catalogJSON)
	return hex.EncodeToString(sum[:])
}

type headerRule struct {
	names      []string
	schemes    []string
	suffixes   []string
	name       string
	deriveName bool
	digitSeg   bool
}

// skipRule is a skip_unquoted_if rule: a value that matches pattern is code,
// unless the rule has unless_callee and the callee of the value (what stands
// before its first "(") matches callee entirely and every pattern of all.
type skipRule struct {
	pattern *regexp.Regexp
	callee  *regexp.Regexp   // nil: no unless_callee
	all     []*regexp.Regexp // each must find something in the callee
}

type knownFormat struct {
	name            string
	pattern         *regexp.Regexp
	requiresLiteral string // the pattern runs only up to the end of the line of the last such literal
}

type catalogT struct {
	minLen       int
	maxDetect    int
	keyMinLen    int
	placeholders []*regexp.Regexp
	fillers      map[string]bool
	fillerPats   []*regexp.Regexp
	skipUnquoted []skipRule
	whitespace   string // body of a character class
	ws           string // [whitespace]
	wsSet        wsSet
	keys         [][]string     // components of each catalog key
	keyWholes    []string       // the components of each key, joined
	valueMin     int            // minimum run length of an unquoted value
	valueRun     *regexp.Regexp // ^<unquoted value class>*
	keySep       *regexp.Regexp // the key and its separator, never the value
	headers      []headerRule
	urlPattern   *regexp.Regexp
	urlName      string
	known        []knownFormat
	jsonPair     *regexp.Regexp
	jsonPairEsc  *regexp.Regexp // pass 6b: \"key\":\"value\"
	quoted       *regexp.Regexp
	quotedEsc    *regexp.Regexp // pass 7b: key=\"value\"
	namedPrivate *regexp.Regexp
	barePrivate  *regexp.Regexp
	bareName     string
}

// catalogRE compiles a catalog pattern with RE2, case-insensitively when ci.
// No catalog pattern uses `\s` for whitespace (engines disagree about it): the
// patterns carry the characters of the catalog's `whitespace` field literally,
// and `[\s\S]` (any character) is the one use left, which is the same in all.
func catalogRE(pattern string, ci bool) *regexp.Regexp {
	if ci {
		pattern = "(?i)" + pattern
		if tree, err := syntax.Parse(pattern, syntax.Perl); err == nil {
			return regexp.MustCompile(dottedI(tree).String())
		}
	}
	return regexp.MustCompile(pattern)
}

// classHas reports whether the class (pairs of range bounds) holds r.
func classHas(rs []rune, r rune) bool {
	for i := 0; i+1 < len(rs); i += 2 {
		if r >= rs[i] && r <= rs[i+1] {
			return true
		}
	}
	return false
}

// dottedI rewrites every case-insensitive `i` literal (and class holding `i`) of
// the tree so it also matches U+0130 and U+0131 (the dotted capital I and the dotless small i): the
// reference implementation's case-insensitive matching treats both as `i`, and
// RE2's simple folding does not.
func dottedI(n *syntax.Regexp) *syntax.Regexp {
	for i, sub := range n.Sub {
		n.Sub[i] = dottedI(sub)
	}
	if n.Op == syntax.OpCharClass && classHas(n.Rune, 'i') && !classHas(n.Rune, 0x130) {
		// A class that holds `i` under (?i) holds both dotted forms in the reference.
		n.Rune = append(n.Rune, 0x130, 0x131)
		return n
	}
	if n.Op != syntax.OpLiteral || n.Flags&syntax.FoldCase == 0 {
		return n
	}
	hasI := false
	for _, r := range n.Rune {
		if r == 'i' || r == 'I' {
			hasI = true
		}
	}
	if !hasI {
		return n
	}
	concat := &syntax.Regexp{Op: syntax.OpConcat}
	for _, r := range n.Rune {
		if r == 'i' || r == 'I' {
			concat.Sub = append(concat.Sub, &syntax.Regexp{Op: syntax.OpCharClass, Rune: []rune{'I', 'I', 'i', 'i', 0x130, 0x131}})
		} else {
			concat.Sub = append(concat.Sub, &syntax.Regexp{Op: syntax.OpLiteral, Rune: []rune{r}, Flags: n.Flags})
		}
	}
	return concat
}

// wsSet is the catalog's whitespace as a predicate: the body of a character
// class, with ranges written `a-b`.
type wsSet struct{ ranges [][2]rune }

func parseWhitespace(body string) wsSet {
	rs := []rune(body)
	var set wsSet
	for i := 0; i < len(rs); i++ {
		if i+2 < len(rs) && rs[i+1] == '-' {
			set.ranges = append(set.ranges, [2]rune{rs[i], rs[i+2]})
			i += 2
			continue
		}
		set.ranges = append(set.ranges, [2]rune{rs[i], rs[i]})
	}
	return set
}

func (w wsSet) has(r rune) bool {
	for _, rg := range w.ranges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// only reports whether s is made of whitespace (an empty s is).
func (w wsSet) only(s string) bool {
	for _, r := range s {
		if !w.has(r) {
			return false
		}
	}
	return true
}

const urlLookbehind = `(?<![A-Za-z0-9+.-])`

var cat = mustLoadCatalog()

func mustLoadCatalog() *catalogT {
	c, err := loadCatalog(catalogJSON)
	if err != nil {
		panic("redact: invalid catalog: " + err.Error())
	}
	return c
}

func loadCatalog(data []byte) (*catalogT, error) {
	var raw struct {
		Whitespace   string   `json:"whitespace"`
		MinLen       int      `json:"min_value_length"`
		MaxDetect    int      `json:"max_detect_bytes"`
		Placeholders []string `json:"placeholder_patterns"`
		KeyContext   struct {
			Keys     []string `json:"keys"`
			MinLen   int      `json:"min_value_length"`
			Unquoted string   `json:"unquoted_value_pattern"`
			DQBody   string   `json:"quoted_value_double_body"`
			EscBody  string   `json:"quoted_value_escaped_body"`
			SQBody   string   `json:"quoted_value_single_body"`
			Fillers  []string `json:"filler_skip_values"`
			FillPats []string `json:"filler_skip_patterns"`
			Skip     []struct {
				Pattern      string `json:"pattern"`
				UnlessCallee *struct {
					Callee string   `json:"callee"`
					All    []string `json:"all"`
				} `json:"unless_callee"`
			} `json:"skip_unquoted_if"`
		} `json:"key_context"`
		Headers      json.RawMessage `json:"headers"`
		HeadersOrder []string        `json:"headers_order"`
		URL          struct {
			Pattern string `json:"pattern"`
			Name    string `json:"name"`
		} `json:"url_credentials"`
		Known []struct {
			Name            string `json:"name"`
			Pattern         string `json:"pattern"`
			RequiresLiteral string `json:"requires_literal"`
		} `json:"known_formats"`
		Explicit struct {
			Nesting struct {
				Open  string `json:"open"`
				Close string `json:"close"`
			} `json:"nesting"`
			Named struct {
				Pattern string `json:"pattern"`
			} `json:"named"`
			Bare struct {
				Pattern string `json:"pattern"`
				Name    string `json:"name"`
			} `json:"bare"`
		} `json:"explicit"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw.Whitespace == "" {
		return nil, fmt.Errorf("catalog lacks the whitespace field")
	}
	// The unquoted value is a class (everything but whitespace and the listed
	// punctuation) repeated at least N times: it is read once per run, by hand.
	const unquotedTail = `"'{}\[\],;:&?`
	wantUnquoted := "[^" + raw.Whitespace + unquotedTail + "]"
	m := regexp.MustCompile(`(?s)^(.*)\{(\d+),\}$`).FindStringSubmatch(raw.KeyContext.Unquoted)
	if m == nil || m[1] != wantUnquoted {
		return nil, fmt.Errorf("unquoted_value_pattern changed; the value scan must be updated")
	}
	if !strings.HasPrefix(raw.URL.Pattern, urlLookbehind) {
		return nil, fmt.Errorf("url_credentials.pattern lost its lookbehind; the previous-character check must be updated")
	}
	if raw.KeyContext.DQBody == "" || raw.KeyContext.SQBody == "" || raw.KeyContext.EscBody == "" {
		return nil, fmt.Errorf("catalog lacks quoted_value_double_body/quoted_value_single_body/quoted_value_escaped_body")
	}
	if raw.Explicit.Nesting.Open != "<private>" || raw.Explicit.Nesting.Close != "</private>" {
		return nil, fmt.Errorf("explicit.nesting changed; the <private> scanner must be updated")
	}
	c := &catalogT{
		whitespace: raw.Whitespace,
		ws:         "[" + raw.Whitespace + "]",
		wsSet:      parseWhitespace(raw.Whitespace),
		minLen:     raw.MinLen,
		maxDetect:  raw.MaxDetect,
		keyMinLen:  raw.KeyContext.MinLen,
		fillers:    map[string]bool{},
		urlName:    raw.URL.Name,
		bareName:   raw.Explicit.Bare.Name,
	}
	if c.keyMinLen == 0 {
		c.keyMinLen = c.minLen
	}
	c.valueMin, _ = strconv.Atoi(m[2])
	c.valueRun = regexp.MustCompile(`^` + wantUnquoted + `*`)
	c.keySep = regexp.MustCompile(`([A-Za-z_$][A-Za-z0-9_$-]*)` + c.ws + `*[:=]` + c.ws + `*`)
	for _, p := range raw.Placeholders {
		c.placeholders = append(c.placeholders, catalogRE(p, false))
	}
	// Idempotence: engram's own token is a placeholder too.
	c.placeholders = append(c.placeholders, regexp.MustCompile(`^\[REDACTED:[A-Z0-9_]+\]$`))
	for _, f := range raw.KeyContext.Fillers {
		c.fillers[strings.ToLower(f)] = true
	}
	for _, p := range raw.KeyContext.FillPats {
		c.fillerPats = append(c.fillerPats, catalogRE(p, false))
	}
	for _, r := range raw.KeyContext.Skip {
		rule := skipRule{pattern: catalogRE(r.Pattern, false)}
		if u := r.UnlessCallee; u != nil {
			rule.callee = catalogRE(u.Callee, false)
			for _, a := range u.All {
				rule.all = append(rule.all, catalogRE(a, false))
			}
		}
		c.skipUnquoted = append(c.skipUnquoted, rule)
	}
	for _, k := range raw.KeyContext.Keys {
		kp := components(k)
		c.keys = append(c.keys, kp)
		c.keyWholes = append(c.keyWholes, strings.Join(kp, ""))
	}
	// headers is an object whose order matters: decode it in document order.
	dec := json.NewDecoder(bytes.NewReader(raw.Headers))
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	byName := map[string]headerRule{}
	var docOrder []string
	for dec.More() {
		keyTok, err := dec.Token() // rule key
		if err != nil {
			return nil, err
		}
		ruleKey, _ := keyTok.(string)
		var h struct {
			Names      []string `json:"header_names"`
			Schemes    []string `json:"schemes"`
			Suffixes   []string `json:"header_name_suffixes"`
			Name       string   `json:"name"`
			DeriveName bool     `json:"derive_name_from_header"`
			DigitSeg   bool     `json:"allow_trailing_digit_segment"`
		}
		if err := dec.Decode(&h); err != nil && err != io.EOF {
			return nil, err
		}
		byName[ruleKey] = headerRule{h.Names, h.Schemes, h.Suffixes, h.Name, h.DeriveName, h.DigitSeg}
		docOrder = append(docOrder, ruleKey)
	}
	// The rules run in the catalog's headers_order, not in the order of the JSON
	// object (no port can rely on an object's order); by name when it is missing.
	order := raw.HeadersOrder
	if len(order) == 0 {
		order = append([]string(nil), docOrder...)
		sort.Strings(order)
	}
	if len(order) != len(byName) {
		return nil, fmt.Errorf("headers_order does not list every header rule")
	}
	for _, name := range order {
		h, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("headers_order names an unknown rule %q", name)
		}
		c.headers = append(c.headers, h)
	}
	c.urlPattern = catalogRE(strings.TrimPrefix(raw.URL.Pattern, urlLookbehind), false)
	for _, k := range raw.Known {
		c.known = append(c.known, knownFormat{k.Name, catalogRE(k.Pattern, false), k.RequiresLiteral})
	}
	// The two catalog patterns are applied ANCHORED to a whole flat span.
	c.namedPrivate = catalogRE(`^(?:`+raw.Explicit.Named.Pattern+`)$`, false)
	c.barePrivate = catalogRE(`^(?:`+raw.Explicit.Bare.Pattern+`)$`, false)
	// Quoted values: quote + body{min,} + quote, where a body unit is a
	// character that is not the quote or a backslash, or a backslash and the
	// character it escapes (so an escaped quote never closes the value early).
	min := "{" + itoa(c.keyMinLen) + ",}"
	c.jsonPair = catalogRE(`"([A-Za-z_$][A-Za-z0-9_$-]*)"`+c.ws+`*:`+c.ws+`*"(`+raw.KeyContext.DQBody+min+`)"`, false)
	esc := "{" + itoa(c.keyMinLen) + ",}"
	c.jsonPairEsc = catalogRE(`\\"([A-Za-z_$][A-Za-z0-9_$-]*)\\"`+c.ws+`*:`+c.ws+`*\\"(`+raw.KeyContext.EscBody+esc+`)\\"`, false)
	c.quotedEsc = catalogRE(`([A-Za-z_$][A-Za-z0-9_$-]*)`+c.ws+`*[:=]`+c.ws+`*\\"(`+raw.KeyContext.EscBody+esc+`)\\"`, false)
	c.quoted = catalogRE(`([A-Za-z_$][A-Za-z0-9_$-]*)`+c.ws+`*[:=]`+c.ws+`*(?:"(`+raw.KeyContext.DQBody+min+`)"|'(`+raw.KeyContext.SQBody+min+`)')`, false)
	return c, nil
}

// components splits an identifier into lowercase words on `_`, `-` and
// camelCase boundaries (a lowercase letter or digit followed by an uppercase
// one, or an uppercase one followed by an uppercase and a lowercase one):
// `dbPassword`, `DB_PASSWORD` and `db-password` are all [db, password].
//
// A version or index suffix is not a word: on the LAST component only, one that
// is just `v?<digits>` is dropped (when there is more than one component), and
// otherwise the digits glued to its end are stripped, so `token2`, `password1`,
// `PASSWORD_V2` and `DB_PASSWORD_2` match.
func components(raw string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == '_' || r == '-' }) {
		start := 0
		for i := 1; i < len(part); i++ {
			prev, cur := part[i-1], part[i]
			boundary := (isLowerOrDigit(prev) && isUpper(cur)) ||
				(isUpper(prev) && isUpper(cur) && i+1 < len(part) && isLower(part[i+1]))
			if boundary {
				out = append(out, strings.ToLower(part[start:i]))
				start = i
			}
		}
		out = append(out, strings.ToLower(part[start:]))
	}
	if n := len(out); n > 0 {
		last := out[n-1]
		if n > 1 && isVersionWord(last) {
			out = out[:n-1]
		} else if t := strings.TrimRight(last, "0123456789"); t != "" {
			out[n-1] = t
		}
	}
	return out
}

// isVersionWord reports whether w is `v?<digits>` (digits required).
func isVersionWord(w string) bool {
	w = strings.TrimPrefix(w, "v")
	if w == "" {
		return false
	}
	for i := 0; i < len(w); i++ {
		if w[i] < '0' || w[i] > '9' {
			return false
		}
	}
	return true
}

func isUpper(c byte) bool        { return c >= 'A' && c <= 'Z' }
func isLower(c byte) bool        { return c >= 'a' && c <= 'z' }
func isLowerOrDigit(c byte) bool { return isLower(c) || c >= '0' && c <= '9' }
