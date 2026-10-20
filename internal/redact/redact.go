// Package redact removes credentials from text before it is stored.
//
// It is a Go port of the Pegasus Harness redactor (same catalog, same passes,
// same order): explicit <private> tags, known token formats, auth headers,
// URL passwords and key/value pairs next to a sensitive key name. Detection is
// by context or by shape, never by "looks random": git hashes, sha256 digests
// and UUIDs survive untouched. A detected value becomes [REDACTED:NAME]; text
// that is already a placeholder is left alone, so Redact is idempotent.
//
// Every pattern runs on RE2 (linear time). Where the reference uses a
// lookbehind, the previous character is checked by hand (see scan).
package redact

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxDetectBytes is the detection cap from the catalog: text beyond it is not
// inspected, so callers drop it rather than store it unredacted.
func MaxDetectBytes() int { return cat.maxDetect }

// Token is engram's replacement token for a detected value named name.
func Token(name string) string { return "[REDACTED:" + name + "]" }

// CutToDetectCap returns the text cut to the detection cap, in bytes, on a
// character boundary, and whether anything was dropped.
func CutToDetectCap(text string) (string, bool) {
	if len(text) <= cat.maxDetect {
		return text, false
	}
	cut := text[:cat.maxDetect]
	for len(cut) > 0 {
		if r, w := utf8.DecodeLastRuneInString(cut); r == utf8.RuneError && w == 1 {
			cut = cut[:len(cut)-1]
			continue
		}
		break
	}
	return cut, true
}

// Redact cuts text to the detection cap (dropping the remainder), replaces
// every detected credential with [REDACTED:NAME] and turns any leftover
// <private>…</private> into [REDACTED].
func Redact(text string) string { return RedactWith(text, Token) }

// RedactWith is Redact with a custom replacement token. Idempotence only holds
// for tokens that match a catalog placeholder pattern.
func RedactWith(text string, token func(name string) string) string {
	if text == "" {
		return text
	}
	text, _ = CutToDetectCap(text)
	return stripPrivate(detectAndReplace(text, token))
}

// --- registry -------------------------------------------------------------

type registry struct {
	valueToName map[string]string
	used        map[string]bool
	order       []string       // values in registration order
	nextSuffix  map[string]int // base name -> the next `_N` to try: every lower one is taken
}

func newRegistry() *registry {
	return &registry{valueToName: map[string]string{}, used: map[string]bool{}, nextSuffix: map[string]int{}}
}

func normalizeName(raw string) string {
	var b strings.Builder
	sep := false
	for _, r := range strings.ToUpper(raw) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			if sep && b.Len() > 0 {
				b.WriteByte('_')
			}
			sep = false
			b.WriteRune(r)
		} else {
			sep = true
		}
	}
	if b.Len() == 0 {
		return "SECRET"
	}
	return b.String()
}

func (g *registry) register(value, base string) string {
	if n, ok := g.valueToName[value]; ok {
		return n
	}
	name := normalizeName(base)
	if g.used[name] {
		n := g.nextSuffix[name]
		if n < 2 {
			n = 2
		}
		for g.used[name+"_"+itoa(n)] {
			n++
		}
		g.nextSuffix[name] = n + 1
		name = name + "_" + itoa(n)
	}
	g.used[name] = true
	g.valueToName[value] = name
	g.order = append(g.order, value)
	return name
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

// --- predicates -----------------------------------------------------------

// maxCheckRunes bounds the anchored placeholder/skip checks. A value longer
// than this is never a placeholder (so it is redacted: the safe side), which
// keeps crafted input from making each candidate rescan a huge value.
const maxCheckRunes = 1024

func tooLong(v string) bool {
	return len(v) > maxCheckRunes*utf8.UTFMax || (len(v) > maxCheckRunes && utf8.RuneCountInString(v) > maxCheckRunes)
}

func isPlaceholder(v string) bool {
	if tooLong(v) {
		return false
	}
	for _, p := range cat.placeholders {
		if p.MatchString(v) {
			return true
		}
	}
	if cat.fillers[strings.ToLower(v)] {
		return true
	}
	for _, p := range cat.fillerPats {
		if p.MatchString(v) {
			return true
		}
	}
	return false
}

func skipsAsUnquotedCode(v string) bool {
	if tooLong(v) {
		return false
	}
	for _, p := range cat.skipUnquoted {
		if p.MatchString(v) {
			return true
		}
	}
	return false
}

func matchesCatalogKey(raw string) bool {
	rp := components(raw)
	if len(rp) == 0 {
		return false
	}
	rawWhole := strings.Join(rp, "")
	for i, kp := range cat.keys {
		if rawWhole == cat.keyWholes[i] {
			return true
		}
		if len(rp) >= len(kp) && strings.Join(rp[len(rp)-len(kp):], "") == cat.keyWholes[i] {
			return true
		}
	}
	return false
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// --- scanning with a manual lookbehind -------------------------------------

func inURLScheme(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '.' || r == '-'
}
func inHeaderName(r rune) bool {
	// U+0130 and U+0131: the reference's case-insensitive [A-Za-z0-9-] holds them.
	return r == 0x130 || r == 0x131 || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-'
}
func inIdent(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '$' || r == '-'
}

// rejectedSkip returns where to resume after a match that starts at start was
// rejected by a `(?<![class])` lookbehind. The match start is itself a class
// character, so every start up to and including the first non-class character
// at or after it has a class character right before it and would be rejected
// too: resuming past that character keeps the whole scan linear.
func rejectedSkip(s string, start int, class func(rune) bool) int {
	q := start
	for q < len(s) {
		r, w := utf8.DecodeRuneInString(s[q:])
		if !class(r) {
			return q + w
		}
		q += w
	}
	return len(s)
}

func prevInClass(s string, start int, class func(rune) bool) bool {
	if class == nil || start == 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:start])
	return class(r)
}

// scan is re.sub with an optional lookbehind: matches whose previous
// character is in lbClass are skipped as if the lookbehind had failed. fn gets
// the absolute submatch indexes and reports whether it replaced the match
// (an unreplaced match is still consumed, as re.sub does).
func scan(re *regexp.Regexp, s string, lbClass func(rune) bool, fn func(loc []int) (string, bool)) string {
	var b strings.Builder
	cursor, pos := 0, 0
	for pos <= len(s) {
		loc := re.FindStringSubmatchIndex(s[pos:])
		if loc == nil {
			break
		}
		for i := range loc {
			if loc[i] >= 0 {
				loc[i] += pos
			}
		}
		if prevInClass(s, loc[0], lbClass) {
			pos = rejectedSkip(s, loc[0], lbClass)
			continue
		}
		if repl, ok := fn(loc); ok {
			b.WriteString(s[cursor:loc[0]])
			b.WriteString(repl)
			cursor = loc[1]
		}
		if loc[1] == loc[0] { // never happens with these patterns; guards the loop
			pos = loc[1] + 1
		} else {
			pos = loc[1]
		}
	}
	if cursor == 0 {
		return s
	}
	b.WriteString(s[cursor:])
	return b.String()
}

// --- detection --------------------------------------------------------------

func headerRegexps() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(cat.headers))
	for i, h := range cat.headers {
		min := "{" + itoa(cat.minLen) + ",}"
		if len(h.names) > 0 {
			names := make([]string, len(h.names))
			for j, n := range h.names {
				names[j] = regexp.QuoteMeta(n)
			}
			scheme := ""
			if len(h.schemes) > 0 {
				scheme = "(?:" + strings.Join(h.schemes, "|") + `)` + cat.ws + `+`
			}
			out[i] = catalogRE(`(?:`+strings.Join(names, "|")+`)`+cat.ws+`*:`+cat.ws+`*`+scheme+`([^`+cat.whitespace+`,"']`+min+`)`, true)
		} else if len(h.suffixes) > 0 {
			sfx := make([]string, len(h.suffixes))
			for j, n := range h.suffixes {
				sfx[j] = regexp.QuoteMeta(n)
			}
			digit := ""
			if h.digitSeg {
				digit = `(?:-[0-9]+)?`
			}
			out[i] = catalogRE(`([A-Za-z][A-Za-z0-9-]*(?:`+strings.Join(sfx, "|")+`)`+digit+`)`+cat.ws+`*:`+cat.ws+`*([^`+cat.whitespace+`,"']`+min+`)`, true)
		}
	}
	return out
}

var hdrRes = headerRegexps()

func detectAndReplace(text string, token func(string) string) string {
	if text == "" {
		return text
	}
	reg := newRegistry()
	out := text
	var v view // folded forms of out, for the prefilters

	// replaceAt swaps s[vs:ve] inside the match s[ms:me] for its token. The
	// reference replaces the FIRST occurrence of the value in the match, which
	// hits the wrong text (and leaves the secret in place) when the value also
	// appears earlier in the match, e.g. `a://pw12345678:pw12345678@h`.
	replaceAt := func(s string, ms, me, vs, ve int, base string) string {
		return s[ms:vs] + token(reg.register(s[vs:ve], base)) + s[ve:me]
	}

	// 1. + 2. <private>NAME=value</private> and <private>value</private>
	// (flat spans, see private.go): the whole span is replaced.
	out = replacePrivateSpans(out, reg, token)

	// 3. Known formats.
	for i, f := range cat.known {
		if !gates.known[i].ok(v.of(out)) {
			continue
		}
		// requires_literal: the pattern runs only up to the end of the line that
		// holds the last such literal, and not at all without one (a lazy block
		// pattern then has nothing to scan). The matches are the same ones.
		cur, rest := out, ""
		if f.requiresLiteral != "" {
			at := strings.LastIndex(out, f.requiresLiteral)
			if at < 0 {
				continue
			}
			if nl := strings.IndexByte(out[at:], '\n'); nl >= 0 {
				cur, rest = out[:at+nl], out[at+nl:]
			}
		}
		out = scan(f.pattern, cur, nil, func(m []int) (string, bool) {
			whole := cur[m[0]:m[1]]
			if runeLen(whole) < cat.minLen || isPlaceholder(whole) {
				return "", false
			}
			return token(reg.register(whole, f.name)), true
		}) + rest
	}

	// 4. Headers.
	for i, h := range cat.headers {
		if !gates.headers[i].ok(v.of(out)) {
			continue
		}
		re := hdrRes[i]
		cur := out
		if len(h.names) > 0 {
			out = scan(re, cur, nil, func(m []int) (string, bool) {
				value := cur[m[2]:m[3]]
				if value == "" || runeLen(value) < cat.minLen || isPlaceholder(value) {
					return "", false
				}
				return replaceAt(cur, m[0], m[1], m[2], m[3], h.name), true
			})
		} else if len(h.suffixes) > 0 {
			out = scan(re, cur, inHeaderName, func(m []int) (string, bool) {
				value := cur[m[4]:m[5]]
				if value == "" || runeLen(value) < cat.minLen || isPlaceholder(value) {
					return "", false
				}
				return replaceAt(cur, m[0], m[1], m[4], m[5], cur[m[2]:m[3]]), true
			})
		}
	}

	// 5. URL credentials.
	if gates.url.ok(v.of(out)) {
		cur := out
		out = scan(cat.urlPattern, cur, inURLScheme, func(m []int) (string, bool) {
			password := cur[m[2]:m[3]]
			if password == "" || runeLen(password) < cat.minLen || isPlaceholder(password) {
				return "", false
			}
			return replaceAt(cur, m[0], m[1], m[2], m[3], cat.urlName), true
		})
	}

	// Passes 6-8 only replace values of catalog keys.
	if gates.keys.ok(v.of(out)) {
		out = keyContextPasses(out, reg, token, replaceAt)
	}

	// 9. Every other occurrence of a value detected in this text.
	return replaceKnown(out, reg, token)
}

// keyContextPasses are passes 6-8: JSON pairs, quoted pairs, unquoted pairs.
func keyContextPasses(out string, reg *registry, token func(string) string, replaceAt func(s string, ms, me, vs, ve int, base string) string) string {
	// 6. JSON "key": "value".
	{
		cur := out
		out = scan(cat.jsonPair, cur, nil, func(m []int) (string, bool) {
			key, value := cur[m[2]:m[3]], cur[m[4]:m[5]]
			if !matchesCatalogKey(key) || isPlaceholder(value) {
				return "", false
			}
			return replaceAt(cur, m[0], m[1], m[4], m[5], key), true
		})
	}

	var v view // folded forms of out, for the prefilters of 6b and 7b

	// 6b. The same inside a JSON string: \"key\":\"value\", the quotes escaped.
	if gates.jsonEsc.ok(v.of(out)) {
		for _, re := range cat.jsonPairEsc {
			cur := out
			out = scan(re, cur, nil, func(m []int) (string, bool) {
				key, value := cur[m[2]:m[3]], cur[m[4]:m[5]]
				if !matchesCatalogKey(key) || isPlaceholder(value) {
					return "", false
				}
				return replaceAt(cur, m[0], m[1], m[4], m[5], key), true
			})
		}
	}

	// 7. key = "value" / key: 'value'.
	{
		cur := out
		out = scan(cat.quoted, cur, inIdent, func(m []int) (string, bool) {
			key := cur[m[2]:m[3]]
			vs, ve := m[4], m[5]
			if vs < 0 {
				vs, ve = m[6], m[7]
			}
			if !matchesCatalogKey(key) || isPlaceholder(cur[vs:ve]) {
				return "", false
			}
			return replaceAt(cur, m[0], m[1], vs, ve, key), true
		})
	}

	// 7b. key=\"value\" / key: \"value\": the quotes are escaped because the text
	// is itself inside a JSON string; the escaped quote delimits the value.
	if gates.quotedEsc.ok(v.of(out)) {
		for _, re := range cat.quotedEsc {
			cur := out
			out = scan(re, cur, inIdent, func(m []int) (string, bool) {
				key, value := cur[m[2]:m[3]], cur[m[4]:m[5]]
				if !matchesCatalogKey(key) || isPlaceholder(value) {
					return "", false
				}
				return replaceAt(cur, m[0], m[1], m[4], m[5], key), true
			})
		}
	}

	// 8. key=value / key: value, unquoted.
	return unquotedPairs(out, reg, token)
}

// unquotedPairs is pass 8. The key and its separator are found by a regex that
// never reads the value; the value is read ONCE per run (a stretch of value
// characters, the catalog's unquoted_value_pattern without its minimum), and
// every candidate inside a run shares its end, so `a=a=a=…` or `token=f(token=f(…`
// cost one walk and not one per candidate. A skipped candidate resumes right
// after its KEY, so a real `key=value` inside a skipped value is still found. A
// value over maxCheckRunes characters is never code-shaped or a placeholder: it
// is taken whole (isPlaceholder and skipsAsUnquotedCode already say so).
func unquotedPairs(s string, reg *registry, token func(string) string) string {
	var b strings.Builder
	cursor, pos := 0, 0
	runStart, runEnd := -1, -1
	for pos < len(s) {
		loc := cat.keySep.FindStringSubmatchIndex(s[pos:])
		if loc == nil {
			break
		}
		for i := range loc {
			if loc[i] >= 0 {
				loc[i] += pos
			}
		}
		start, keyEnd, v := loc[0], loc[3], loc[1]
		if prevInClass(s, start, inIdent) {
			pos = rejectedSkip(s, start, inIdent)
			continue
		}
		if !(v >= runStart && v < runEnd) {
			runStart, runEnd = v, v+cat.valueRun.FindStringIndex(s[v:])[1]
		}
		key := s[start:keyEnd]
		if runeLen(s[v:min(runEnd, v+cat.valueMin*utf8.UTFMax)]) < cat.valueMin || !matchesCatalogKey(key) {
			pos = keyEnd
			continue
		}
		value := s[v:runEnd]
		if isPlaceholder(value) || skipsAsUnquotedCode(value) {
			pos = keyEnd
			continue
		}
		b.WriteString(s[cursor:v])
		b.WriteString(token(reg.register(value, key)))
		cursor, pos = runEnd, runEnd
	}
	if cursor == 0 {
		return s
	}
	b.WriteString(s[cursor:])
	return b.String()
}
