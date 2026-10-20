package redact

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Repeated occurrences: after the passes, every OTHER occurrence of a value
// detected in the same text is replaced too (`api_key=VALUE … VALUE` hides both
// copies; before, the second copy of a detected secret was stored in clear).
// A port of the Pegasus hook's `_replace_known` / `_occurrences`, which is the
// oracle; the constants are the hook's.

const (
	// longValue: a value this long is replaced wherever its text stands.
	longValue = 20
)

var reEscapeBefore = regexp.MustCompile(`\\[ntrbfv0ae"'/\\]\z|\\u[0-9a-fA-F]{4}\z|\\x[0-9a-fA-F]{2}\z|\\[0-7]{1,3}\z|%[0-9a-fA-F]{2}\z`)

// tokenRune is a letter or digit of the Basic Multilingual Plane, `_`, `$` or
// `-`. A code point from U+10000 up is never one: the engines of the other
// copies of the redactor disagree on which recent astral characters are letters,
// and a rule that depends on the Unicode version cannot give the same positions
// in all of them.
func tokenRune(r rune) bool {
	if r >= 0x10000 {
		return false
	}
	return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '$' || r == '-'
}

// escapeBefore reports whether the text right before index ends in a backslash
// escape (`\n`, `\t`, `\a`, `\e`, `\"`, `\uXXXX`, `\xNN`, octal `\NNN`...) or a
// percent escape (`%0A`): what follows starts a fresh token. Every character of
// the patterns is ASCII, so looking at the last 6 bytes is the same as the last
// 6 characters.
func escapeBefore(text string, index int) bool {
	from := index - 6
	if from < 0 {
		from = 0
	}
	return reEscapeBefore.MatchString(text[from:index])
}

// isOccurrence reports whether value, standing at text[start:end], is an actual
// occurrence. A value of longValue characters or more counts wherever its text
// stands. A shorter one counts only at a token boundary: neither neighbour
// continues a word (unless an escape precedes it), and a `.` beside it does not
// join it to another word.
func isOccurrence(text string, start, end int, value string, long bool) bool {
	if long {
		return true
	}
	first, _ := utf8.DecodeRuneInString(value)
	last, _ := utf8.DecodeLastRuneInString(value)
	firstTok, lastTok := tokenRune(first), tokenRune(last)
	var before, after rune
	hasBefore, hasAfter := start > 0, end < len(text)
	if hasBefore {
		before, _ = utf8.DecodeLastRuneInString(text[:start])
	}
	if hasAfter {
		after, _ = utf8.DecodeRuneInString(text[end:])
	}
	switch {
	case firstTok && hasBefore && tokenRune(before) && !escapeBefore(text, start):
		return false
	case lastTok && hasAfter && tokenRune(after):
		return false
	case firstTok && hasBefore && before == '.' && start > 1:
		if r, _ := utf8.DecodeLastRuneInString(text[:start-1]); tokenRune(r) {
			return false
		}
	case lastTok && hasAfter && after == '.' && end+1 < len(text):
		if r, _ := utf8.DecodeRuneInString(text[end+1:]); tokenRune(r) {
			return false
		}
	}
	return true
}

type knownValue struct {
	name string
	long bool // longValue runes or more
}

// replaceKnown is pass 9: text with every other occurrence of each value
// registered in this text replaced by its token, in ONE scan, with no cap on how
// many values. The values are grouped by length, each group in a hash table, and
// a position is looked at only when the text there starts like a value (the set
// of every value's first keyMinLen runes). At a position the longest occurrence
// that isOccurrence accepts wins, and the scan resumes after it: leftmost, then
// longest, no overlaps. A value that is only whitespace, or shorter than the
// key-context floor (in runes), is not looked for.
//
// Groups are by length in bytes, not runes: two values that both match at a
// position are prefixes of the same text, so the longer in bytes is the longer
// in runes, and the order of attempts is the same.
func replaceKnown(text string, reg *registry, token func(string) string) string {
	floor := cat.keyMinLen
	if floor < 1 {
		floor = 1
	}
	byLen := map[int]map[string]knownValue{}
	starts := map[string]struct{}{}
	var firstBytes [256]bool
	for _, value := range reg.order {
		if runeLen(value) < floor || cat.wsSet.only(value) {
			continue
		}
		m := byLen[len(value)]
		if m == nil {
			m = map[string]knownValue{}
			byLen[len(value)] = m
		}
		m[value] = knownValue{name: reg.valueToName[value], long: runeLen(value) >= longValue}
		starts[prefixRunes(value, floor)] = struct{}{}
		firstBytes[value[0]] = true
	}
	if len(byLen) == 0 {
		return text
	}
	lengths := make([]int, 0, len(byLen))
	for l := range byLen {
		lengths = append(lengths, l)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(lengths)))

	var b strings.Builder
	cursor, i := 0, 0
	for i < len(text) {
		if !firstBytes[text[i]] {
			i++
			for i < len(text) && !utf8.RuneStart(text[i]) {
				i++
			}
			continue
		}
		_, w := utf8.DecodeRuneInString(text[i:])
		if _, ok := starts[prefixRunes(text[i:], floor)]; !ok {
			i += w
			continue
		}
		matched := false
		for _, l := range lengths {
			end := i + l
			if end > len(text) {
				continue
			}
			cand := text[i:end]
			if kv, ok := byLen[l][cand]; ok && isOccurrence(text, i, end, cand, kv.long) {
				b.WriteString(text[cursor:i])
				b.WriteString(token(kv.name))
				cursor, i = end, end
				matched = true
				break
			}
		}
		if !matched {
			i += w
		}
	}
	if cursor == 0 {
		return text
	}
	b.WriteString(text[cursor:])
	return b.String()
}

// prefixRunes is the first n runes of s (all of s when it is shorter).
func prefixRunes(s string, n int) string {
	i := 0
	for ; n > 0 && i < len(s); n-- {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return s[:i]
}
