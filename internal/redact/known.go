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
	// Independent checks, as in the reference (not a switch: a `.` case that
	// does not reject must not hide the one after it).
	if firstTok && hasBefore && tokenRune(before) && !escapeBefore(text, start) {
		return false
	}
	if lastTok && hasAfter && tokenRune(after) {
		return false
	}
	if firstTok && hasBefore && before == '.' && start > 1 {
		if r, _ := utf8.DecodeLastRuneInString(text[:start-1]); tokenRune(r) {
			return false
		}
	}
	if lastTok && hasAfter && after == '.' && end+1 < len(text) {
		if r, _ := utf8.DecodeRuneInString(text[end+1:]); tokenRune(r) {
			return false
		}
	}
	return true
}

// knownPerEnd bounds the candidates looked at per END position of the text (the
// longest ones; they are suffixes of one another): no text, however it is made,
// costs more than this per character.
const knownPerEnd = 8

// maxKnownValues is how many registered values (the most recent) the index keeps.
const maxKnownValues = 50000

// automaton is an Aho-Corasick automaton over runes. Node 0 is the root; goto is
// keyed node<<21|rune; fail is the longest proper suffix that is also a trie
// prefix; term[n] is the index of the value that ends at n (-1 for none); dlink[n]
// the nearest node on its failure chain that ends a value (0 for none), so the
// values that end at one place are a chain from the longest to the shortest.
type automaton struct {
	next   map[uint64]int32
	fail   []int32
	depth  []int32
	term   []int32
	dlink  []int32
	values []string
	names  []string
	long   []bool
}

func buildAutomaton(values, names []string) *automaton {
	a := &automaton{next: map[uint64]int32{}, fail: []int32{0}, depth: []int32{0}, term: []int32{-1}, dlink: []int32{0}}
	parent := []int32{0}
	char := []rune{0}
	for i, value := range values {
		node := int32(0)
		for _, r := range value {
			key := uint64(node)<<21 | uint64(r)
			nxt, ok := a.next[key]
			if !ok {
				nxt = int32(len(a.depth))
				a.next[key] = nxt
				a.depth = append(a.depth, a.depth[node]+1)
				a.term = append(a.term, -1)
				a.fail = append(a.fail, 0)
				a.dlink = append(a.dlink, 0)
				parent = append(parent, node)
				char = append(char, r)
			}
			node = nxt
		}
		if a.term[node] < 0 {
			a.term[node] = int32(len(a.values))
			a.values = append(a.values, value)
			a.names = append(a.names, names[i])
			a.long = append(a.long, runeLen(value) >= longValue)
		}
	}
	// Breadth-first by depth.
	byDepth := map[int32][]int32{}
	var maxDepth int32
	for n := int32(1); n < int32(len(a.depth)); n++ {
		byDepth[a.depth[n]] = append(byDepth[a.depth[n]], n)
		if a.depth[n] > maxDepth {
			maxDepth = a.depth[n]
		}
	}
	for level := int32(1); level <= maxDepth; level++ {
		for _, n := range byDepth[level] {
			if level > 1 {
				f := a.fail[parent[n]]
				c := char[n]
				for f != 0 {
					if _, ok := a.next[uint64(f)<<21|uint64(c)]; ok {
						break
					}
					f = a.fail[f]
				}
				a.fail[n] = a.next[uint64(f)<<21|uint64(c)]
			}
			f := a.fail[n]
			if a.term[f] >= 0 {
				a.dlink[n] = f
			} else {
				a.dlink[n] = a.dlink[f]
			}
		}
	}
	return a
}

type candidate struct {
	end   int // in runes
	index int32
}

// replaceKnown is pass 9: text with every other occurrence of each value
// registered in this text replaced by its token, in ONE scan whose cost is linear
// in the text: an Aho-Corasick automaton over the values is run over the text once
// and the candidate occurrences it reports (at most knownPerEnd per end position,
// the longest first) are resolved leftmost-first: once no occurrence that starts
// earlier can still appear (the leftmost start the automaton still has alive has
// moved past it), the earliest start with a candidate that isOccurrence accepts
// wins, the longest such candidate at that start, and the scan goes on after it
// (the automaton state is cut back to what starts there, by failure links).
// A value that is only whitespace, or shorter than the key-context floor (in
// runes), is not looked for; the index keeps the maxKnownValues most recent.
func replaceKnown(text string, reg *registry, token func(string) string) string {
	floor := cat.keyMinLen
	if floor < 1 {
		floor = 1
	}
	var values, names []string
	order := reg.order
	if len(order) > maxKnownValues {
		order = order[len(order)-maxKnownValues:]
	}
	for _, value := range order {
		if runeLen(value) < floor || cat.wsSet.only(value) {
			continue
		}
		values = append(values, value)
		names = append(names, reg.valueToName[value])
	}
	if len(values) == 0 {
		return text
	}
	if len(values) <= 8 {
		found := false
		for _, v := range values {
			if strings.Contains(text, v) {
				found = true
				break
			}
		}
		if !found {
			return text
		}
	}
	a := buildAutomaton(values, names)

	// The text as runes, with the byte offset of each (and of the end).
	runes := make([]rune, 0, len(text))
	offs := make([]int, 0, len(text)+1)
	for i := 0; i < len(text); {
		r, w := utf8.DecodeRuneInString(text[i:])
		runes = append(runes, r)
		offs = append(offs, i)
		i += w
	}
	offs = append(offs, len(text))
	size := len(runes)

	var b strings.Builder
	cursor := 0 // rune index
	scanned := 0
	groups := map[int][]candidate{}
	state := int32(0)
	emit := func(from int, name string) {
		b.WriteString(text[offs[cursor]:offs[from]])
		b.WriteString(token(name))
	}
	valid := func(start int, c candidate) bool {
		v := a.values[c.index]
		return isOccurrence(text, offs[start], offs[c.end], v, a.long[c.index])
	}
	for j := 0; j < size; j++ {
		c := uint64(runes[j])
		for {
			if nxt, ok := a.next[uint64(state)<<21|c]; ok {
				state = nxt
				break
			}
			if state == 0 {
				break
			}
			state = a.fail[state]
		}
		node := state
		if a.term[node] < 0 {
			node = a.dlink[node]
		}
		for seen := 0; node != 0 && seen < knownPerEnd; seen++ {
			s := j + 1 - int(a.depth[node])
			groups[s] = append(groups[s], candidate{j + 1, a.term[node]})
			node = a.dlink[node]
		}
		limit := j + 1 - int(a.depth[state])
		for scanned < limit {
			if len(groups) == 0 {
				scanned = limit
				break
			}
			found := groups[scanned]
			delete(groups, scanned)
			if len(found) > 0 {
				hit := -1
				for k := len(found) - 1; k >= 0; k-- {
					if valid(scanned, found[k]) {
						hit = k
						break
					}
				}
				if hit >= 0 {
					c := found[hit]
					emit(scanned, a.names[c.index])
					for d := scanned + 1; d < c.end; d++ {
						delete(groups, d)
					}
					cursor, scanned = c.end, c.end
					for int(a.depth[state]) > j+1-c.end {
						state = a.fail[state]
					}
					limit = j + 1 - int(a.depth[state])
					continue
				}
			}
			scanned++
		}
	}
	// Nothing can start later than the end of the text: resolve what is left.
	for len(groups) > 0 && scanned <= size {
		found := groups[scanned]
		delete(groups, scanned)
		for k := len(found) - 1; k >= 0; k-- {
			if valid(scanned, found[k]) {
				c := found[k]
				emit(scanned, a.names[c.index])
				for d := scanned + 1; d < c.end; d++ {
					delete(groups, d)
				}
				cursor = c.end
				scanned = c.end - 1
				break
			}
		}
		scanned++
	}
	if cursor == 0 {
		return text
	}
	b.WriteString(text[offs[cursor]:])
	return b.String()
}
