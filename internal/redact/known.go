package redact

import (
	"regexp"
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
	// maxRepeated: at most this many values (in registration order) get the pass;
	// each costs a walk over the text.
	maxRepeated = 64
)

var reEscapeBefore = regexp.MustCompile(`\\[ntrbfv0"'/\\]\z|\\u[0-9a-fA-F]{4}\z|%[0-9a-fA-F]{2}\z`)

// tokenRune is a letter or digit of any script, `_`, `$` or `-`.
func tokenRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '$' || r == '-'
}

// escapeBefore reports whether the text right before index ends in a backslash
// escape (`\n`, `\t`, `\"`, `\uXXXX`…) or a percent escape (`%0A`): what follows
// starts a fresh token. Every character of the patterns is ASCII, so looking at
// the last 6 bytes is the same as the last 6 characters.
func escapeBefore(text string, index int) bool {
	from := index - 6
	if from < 0 {
		from = 0
	}
	return reEscapeBefore.MatchString(text[from:index])
}

// occurrences lists every actual occurrence of value in text as [start, end). A
// value of longValue characters or more counts wherever its text stands. A
// shorter one counts only at a token boundary: neither neighbour continues a
// word (unless an escape precedes it), and a `.` beside it does not join it to
// another word.
func occurrences(text, value string) [][2]int {
	var found [][2]int
	long := runeLen(value) >= longValue
	first, _ := utf8.DecodeRuneInString(value)
	last, _ := utf8.DecodeLastRuneInString(value)
	firstTok, lastTok := tokenRune(first), tokenRune(last)
	pos := 0
	for pos <= len(text) {
		i := strings.Index(text[pos:], value)
		if i < 0 {
			break
		}
		start := pos + i
		end := start + len(value)
		ok := true
		if !long {
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
				ok = false
			case lastTok && hasAfter && tokenRune(after):
				ok = false
			case firstTok && hasBefore && before == '.' && start > 1:
				if r, _ := utf8.DecodeLastRuneInString(text[:start-1]); tokenRune(r) {
					ok = false
				}
			case lastTok && hasAfter && after == '.' && end+1 < len(text):
				if r, _ := utf8.DecodeRuneInString(text[end+1:]); tokenRune(r) {
					ok = false
				}
			}
		}
		if ok {
			found = append(found, [2]int{start, end})
			pos = end
		} else {
			pos = start + 1
		}
	}
	return found
}

// replaceKnown is pass 9: text with every other occurrence of each value
// registered in this text replaced by its token, in registration order. A value
// that is only whitespace, or shorter than the key-context floor, is not looked
// for.
func replaceKnown(text string, reg *registry, token func(string) string) string {
	n := len(reg.order)
	if n > maxRepeated {
		n = maxRepeated
	}
	for _, value := range append([]string(nil), reg.order[:n]...) {
		if cat.wsSet.only(value) || runeLen(value) < cat.keyMinLen || !strings.Contains(text, value) {
			continue
		}
		occ := occurrences(text, value)
		if len(occ) == 0 {
			continue
		}
		tok := token(reg.valueToName[value])
		var b strings.Builder
		cursor := 0
		for _, o := range occ {
			b.WriteString(text[cursor:o[0]])
			b.WriteString(tok)
			cursor = o[1]
		}
		b.WriteString(text[cursor:])
		text = b.String()
	}
	return text
}
