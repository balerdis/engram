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
	skipUnquoted []*regexp.Regexp
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
	quoted       *regexp.Regexp
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
	}
	return regexp.MustCompile(pattern)
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
			SQBody   string   `json:"quoted_value_single_body"`
			Fillers  []string `json:"filler_skip_values"`
			FillPats []string `json:"filler_skip_patterns"`
			Skip     []struct {
				Pattern string `json:"pattern"`
			} `json:"skip_unquoted_if"`
		} `json:"key_context"`
		Headers json.RawMessage `json:"headers"`
		URL     struct {
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
	if raw.KeyContext.DQBody == "" || raw.KeyContext.SQBody == "" {
		return nil, fmt.Errorf("catalog lacks quoted_value_double_body/quoted_value_single_body")
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
		c.skipUnquoted = append(c.skipUnquoted, catalogRE(r.Pattern, false))
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
	for dec.More() {
		if _, err := dec.Token(); err != nil { // rule key
			return nil, err
		}
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
		c.headers = append(c.headers, headerRule{h.Names, h.Schemes, h.Suffixes, h.Name, h.DeriveName, h.DigitSeg})
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
