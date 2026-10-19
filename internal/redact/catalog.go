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
	name    string
	pattern *regexp.Regexp
}

type catalogT struct {
	minLen       int
	maxDetect    int
	keyMinLen    int
	placeholders []*regexp.Regexp
	fillers      map[string]bool
	fillerPats   []*regexp.Regexp
	skipUnquoted []*regexp.Regexp
	keys         [][]string // components of each catalog key
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

// pySpace is Python's `str` whitespace (what `\s` matches there). RE2's `\s`
// is ASCII-only, so every catalog pattern is translated before compiling.
const pySpace = `\t\n\v\f\r\x{1c}-\x{20}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

func isPySpace(r rune) bool {
	switch {
	case r >= 9 && r <= 13, r >= 0x1c && r <= 0x20, r == 0x85, r == 0xa0, r == 0x1680,
		r >= 0x2000 && r <= 0x200a, r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000:
		return true
	}
	return false
}

// pyRE rewrites `\s` (inside and outside character classes) to the Unicode
// whitespace set and compiles with RE2.
func pyRE(pattern string, ci bool) *regexp.Regexp {
	var b strings.Builder
	if ci {
		b.WriteString("(?i)")
	}
	inClass := false
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '\\' && i+1 < len(pattern):
			if pattern[i+1] == 's' {
				if inClass {
					b.WriteString(pySpace)
				} else {
					b.WriteString("[" + pySpace + "]")
				}
			} else {
				b.WriteByte(c)
				b.WriteByte(pattern[i+1])
			}
			i++
		case c == '[' && !inClass:
			inClass = true
			b.WriteByte(c)
		case c == ']' && inClass:
			inClass = false
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return regexp.MustCompile(b.String())
}

const (
	expectedUnquoted = `[^\s"'{}\[\],;:&?]{4,}`
	urlLookbehind    = `(?<![A-Za-z0-9+.-])`
)

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
			Name    string `json:"name"`
			Pattern string `json:"pattern"`
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
	if raw.KeyContext.Unquoted != expectedUnquoted {
		return nil, fmt.Errorf("unquoted_value_pattern changed; the manual value scan must be updated")
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
		minLen:    raw.MinLen,
		maxDetect: raw.MaxDetect,
		keyMinLen: raw.KeyContext.MinLen,
		fillers:   map[string]bool{},
		urlName:   raw.URL.Name,
		bareName:  raw.Explicit.Bare.Name,
	}
	if c.keyMinLen == 0 {
		c.keyMinLen = c.minLen
	}
	for _, p := range raw.Placeholders {
		c.placeholders = append(c.placeholders, pyRE(p, false))
	}
	// Idempotence: engram's own token is a placeholder too.
	c.placeholders = append(c.placeholders, regexp.MustCompile(`^\[REDACTED:[A-Z0-9_]+\]$`))
	for _, f := range raw.KeyContext.Fillers {
		c.fillers[strings.ToLower(f)] = true
	}
	for _, p := range raw.KeyContext.FillPats {
		c.fillerPats = append(c.fillerPats, pyRE(p, false))
	}
	for _, r := range raw.KeyContext.Skip {
		c.skipUnquoted = append(c.skipUnquoted, pyRE(r.Pattern, false))
	}
	for _, k := range raw.KeyContext.Keys {
		c.keys = append(c.keys, components(k))
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
	c.urlPattern = pyRE(strings.TrimPrefix(raw.URL.Pattern, urlLookbehind), false)
	for _, k := range raw.Known {
		c.known = append(c.known, knownFormat{k.Name, pyRE(k.Pattern, false)})
	}
	// The two catalog patterns are applied ANCHORED to a whole flat span.
	c.namedPrivate = pyRE(`^(?:`+raw.Explicit.Named.Pattern+`)$`, false)
	c.barePrivate = pyRE(`^(?:`+raw.Explicit.Bare.Pattern+`)$`, false)
	// Quoted values: quote + body{min,} + quote, where a body unit is a
	// character that is not the quote or a backslash, or a backslash and the
	// character it escapes (so an escaped quote never closes the value early).
	min := "{" + itoa(c.keyMinLen) + ",}"
	c.jsonPair = pyRE(`"([A-Za-z_$][A-Za-z0-9_$-]*)"\s*:\s*"(`+raw.KeyContext.DQBody+min+`)"`, false)
	c.quoted = pyRE(`([A-Za-z_$][A-Za-z0-9_$-]*)\s*[:=]\s*(?:"(`+raw.KeyContext.DQBody+min+`)"|'(`+raw.KeyContext.SQBody+min+`)')`, false)
	return c, nil
}

// components splits an identifier into lowercase words on `_`, `-` and
// camelCase boundaries (a lowercase letter or digit followed by an uppercase
// one, or an uppercase one followed by an uppercase and a lowercase one):
// `dbPassword`, `DB_PASSWORD` and `db-password` are all [db, password].
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
	return out
}

func isUpper(c byte) bool        { return c >= 'A' && c <= 'Z' }
func isLower(c byte) bool        { return c >= 'a' && c <= 'z' }
func isLowerOrDigit(c byte) bool { return isLower(c) || c >= '0' && c <= '9' }
