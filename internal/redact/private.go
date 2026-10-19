package redact

import (
	"regexp"
	"sort"
	"strings"
)

// <private> handling: a scanner, not the two lazy catalog patterns.
//
// The catalog's `explicit.nesting` describes the rules; this is a port of the
// Pegasus hook (engram-hook.py `_private_spans`), which is the oracle:
//
//   - A close tag closes the innermost open tag; spans that nest or overlap are
//     merged into one flat span, and the tags inside it are dropped.
//   - An open tag nobody closes ends at the end of its line, or, when only
//     whitespace follows the tag on its line, at the end of its paragraph (the
//     next blank line, or the end of the text). Never the rest of the text.
//   - A tag written between backticks is a mention, not a tag.
//   - A close tag with nothing open belongs to no span and stays.
//
// The two catalog patterns are applied ANCHORED to `<private>` + inner +
// `</private>` of each flat span. Everything is linear in the text.

var (
	rePrivTag   = regexp.MustCompile(`(?i)<(/?)private>`)
	reBlankLine = regexp.MustCompile(`\n[ \t\r]*\n`)
)

type privTag struct {
	start, end int
	close      bool
}

// privateTags lists every real tag of text, in order.
func privateTags(text string) []privTag {
	var tags []privTag
	for _, m := range rePrivTag.FindAllStringSubmatchIndex(text, -1) {
		if m[0] > 0 && text[m[0]-1] == '`' && m[1] < len(text) && text[m[1]] == '`' {
			continue
		}
		tags = append(tags, privTag{m[0], m[1], m[3] > m[2]})
	}
	return tags
}

// blankBreaks finds the blank-line breaks once and is scanned with a cursor as
// the open tags move forward, and remembers the next newline, so many unclosed
// tags stay linear: the open tags come in ascending order, so a newline found
// for one is still the first one at or after the next.
type blankBreaks struct {
	text   string
	pos    []int
	cursor int
	done   bool
	nl     int // first newline at or after the last start asked (len(text) if none), -1 before the first ask
}

// nextNewline is the index of the first '\n' at or after start, or len(text).
func (b *blankBreaks) nextNewline(start int) int {
	if b.nl >= start {
		return b.nl
	}
	if i := strings.IndexByte(b.text[start:], '\n'); i >= 0 {
		b.nl = start + i
	} else {
		b.nl = len(b.text)
	}
	return b.nl
}

func (b *blankBreaks) find() {
	if b.done {
		return
	}
	b.done = true
	for _, m := range reBlankLine.FindAllStringIndex(b.text, -1) {
		b.pos = append(b.pos, m[0])
	}
}

// unclosedEnd is where an open tag with no close (whose tag ends at start) ends.
func unclosedEnd(text string, start int, blanks *blankBreaks) int {
	eol := blanks.nextNewline(start)
	if strings.Trim(text[start:eol], " \t\r") != "" {
		if eol > 0 && text[eol-1] == '\r' {
			return eol - 1
		}
		return eol
	}
	blanks.find()
	for blanks.cursor < len(blanks.pos) && blanks.pos[blanks.cursor] < eol {
		blanks.cursor++
	}
	if blanks.cursor < len(blanks.pos) {
		return blanks.pos[blanks.cursor]
	}
	return len(text)
}

type privSpan struct {
	start, end int
	inner      string // the span without any tag
}

// privateSpans returns every flat <private> span of text, in order.
func privateSpans(text string) []privSpan {
	if !gates.privOpen.ok((&view{}).of(text)) {
		return nil
	}
	tags := privateTags(text)
	if len(tags) == 0 {
		return nil
	}
	var spans [][2]int
	var opened []int
	for i, t := range tags {
		if !t.close {
			opened = append(opened, i)
		} else if len(opened) > 0 {
			o := opened[len(opened)-1]
			opened = opened[:len(opened)-1]
			spans = append(spans, [2]int{tags[o].start, t.end})
		}
	}
	if len(opened) > 0 {
		blanks := &blankBreaks{text: text, nl: -1}
		for _, i := range opened {
			spans = append(spans, [2]int{tags[i].start, unclosedEnd(text, tags[i].end, blanks)})
		}
	}
	sort.SliceStable(spans, func(a, b int) bool {
		if spans[a][0] != spans[b][0] {
			return spans[a][0] < spans[b][0]
		}
		return spans[a][1] > spans[b][1]
	})
	var merged [][2]int
	for _, sp := range spans {
		if n := len(merged); n > 0 && sp[0] < merged[n-1][1] {
			if sp[1] > merged[n-1][1] {
				merged[n-1][1] = sp[1]
			}
		} else {
			merged = append(merged, sp)
		}
	}
	result := make([]privSpan, 0, len(merged))
	t := 0
	for _, sp := range merged {
		start, stop := sp[0], sp[1]
		var inner strings.Builder
		at := start
		for t < len(tags) && tags[t].start < start {
			t++
		}
		for t < len(tags) && tags[t].end <= stop {
			inner.WriteString(text[at:tags[t].start])
			at = tags[t].end
			t++
		}
		inner.WriteString(text[at:stop])
		result = append(result, privSpan{start, stop, inner.String()})
	}
	return result
}

// mapPrivateSpans returns text with each span replaced by replace(span), or
// kept as it was when replace reports false.
func mapPrivateSpans(text string, spans []privSpan, replace func(privSpan) (string, bool)) string {
	var out strings.Builder
	cursor := 0
	changed := false
	for _, sp := range spans {
		r, ok := replace(sp)
		if !ok {
			continue
		}
		changed = true
		out.WriteString(text[cursor:sp.start])
		out.WriteString(r)
		cursor = sp.end
	}
	if !changed {
		return text
	}
	out.WriteString(text[cursor:])
	return out.String()
}

// stripPrivate turns every flat <private> span into [REDACTED].
func stripPrivate(text string) string {
	return mapPrivateSpans(text, privateSpans(text), func(privSpan) (string, bool) { return "[REDACTED]", true })
}

// StripPrivate is stripPrivate for callers outside the package.
func StripPrivate(text string) string { return stripPrivate(text) }

// replacePrivateSpans is passes 1 and 2: a span that holds NAME=value (the
// catalog's named pattern, anchored over the whole flat span) takes NAME as its
// variable name, every other non-empty span is the bare kind. Named spans
// register first, as the two catalog passes always did.
func replacePrivateSpans(text string, reg *registry, token func(string) string) string {
	spans := privateSpans(text)
	if len(spans) == 0 {
		return text
	}
	repl := map[int]string{}
	for _, sp := range spans {
		if m := cat.namedPrivate.FindStringSubmatch("<private>" + sp.inner + "</private>"); m != nil && m[2] != "" {
			repl[sp.start] = token(reg.register(m[2], m[1]))
		}
	}
	for _, sp := range spans {
		if _, done := repl[sp.start]; done {
			continue
		}
		if m := cat.barePrivate.FindStringSubmatch("<private>" + sp.inner + "</private>"); m != nil && m[1] != "" {
			repl[sp.start] = token(reg.register(m[1], cat.bareName))
		}
	}
	if len(repl) == 0 {
		return text
	}
	return mapPrivateSpans(text, spans, func(sp privSpan) (string, bool) {
		r, ok := repl[sp.start]
		return r, ok
	})
}
