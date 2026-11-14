package kb

import (
	"slices"
	"sync"
	"unicode"
	"unicode/utf8"
)

// The search index's keys (docs/assistant/knowledge.md "Search index"). A search finds a file when every word of the
// query is a substring of the file's folded text (hasAll over fold), so the index has to answer "which files can
// hold this substring", never "which files hold this word": a query of "uild" finds "build", and "报 dr" is two
// substrings. Each rune of the folded text that is not white space gives one key, made of the rune and what follows
// it, so a key depends on nothing outside itself and every place a substring occurs gives the keys the substring
// itself gives:
//
//   - a wide rune (CJK, kana, Hangul, full-width forms, emoji: U+2E80 and up), or a narrow one followed by a wide
//     one: the pair of the two (a character bigram);
//   - a narrow rune followed by a narrow one: the three (a trigram; the third may be wide, or the end of the token);
//   - the last rune before white space or the end of the text: the pair of it and the token's end.
//
// White space is unicode.IsSpace, what strings.Fields splits a query at, so no word of a query holds any and no key
// spans it. Case and normalization are fold's: the keys are taken from fold's output on both sides. Punctuation is a
// rune like any other, as it is to the scan.
const (
	gramEnd  = 0x110000 // after a token's last rune: white space or the text's end
	gramNone = 0x110001 // no third symbol: a pair
	gramWide = 0x2E80   // the first wide rune (CJK Radicals Supplement)
)

func gramKey(a, b, c uint32) uint64 { return uint64(a)<<42 | uint64(b)<<21 | uint64(c) }

// appendGrams appends the keys of folded (fold's output), one per rune that is not white space, in the text's order
// and with repeats.
func appendGrams(dst []uint64, folded string) []uint64 {
	// a and b are the two symbols before the one being read (a rune or gramEnd); the key that starts at a is known
	// once the symbol after b is.
	a, b := uint32(gramEnd), uint32(gramEnd)
	step := func(c uint32) {
		if a != gramEnd {
			if a >= gramWide || b == gramEnd || b >= gramWide {
				dst = append(dst, gramKey(a, b, gramNone))
			} else {
				dst = append(dst, gramKey(a, b, c))
			}
		}
		a, b = b, c
	}
	for _, r := range folded {
		if unicode.IsSpace(r) {
			step(gramEnd)
		} else {
			step(uint32(r))
		}
	}
	step(gramEnd)
	step(gramEnd)
	return dst
}

// gramScratch holds the buffers fileGrams works in: a file's keys with repeats are many times its distinct ones.
var gramScratch = sync.Pool{New: func() any { return new([]uint64) }}

// fileGrams is the sorted set of a file's keys: those of its folded text.
func fileGrams(folded string) []uint64 {
	buf := gramScratch.Get().(*[]uint64)
	keys := appendGrams((*buf)[:0], folded)
	slices.Sort(keys)
	out := slices.Clone(slices.Compact(keys))
	if cap(keys) <= 1<<18 { // a large file's buffer is not kept
		*buf = keys
		gramScratch.Put(buf)
	}
	return out
}

// gramClause is what the index can say of one word of a query: a file that holds the word has every key of all;
// when the word is too short to give one (a single rune, or two narrow ones), it has at least one key in [lo, hi).
type gramClause struct {
	all    []uint64
	lo, hi uint64
}

// wordClause is the clause of w, one word of a folded query (no white space, not empty).
func wordClause(w string) gramClause {
	runes := make([]uint32, 0, len(w))
	for _, r := range w {
		runes = append(runes, uint32(r))
	}
	var c gramClause
	for i := 0; i+1 < len(runes); i++ {
		a, b := runes[i], runes[i+1]
		switch {
		case a >= gramWide || b >= gramWide:
			c.all = append(c.all, gramKey(a, b, gramNone))
		case i+2 < len(runes):
			c.all = append(c.all, gramKey(a, b, runes[i+2]))
		}
	}
	if len(c.all) > 0 {
		slices.Sort(c.all)
		c.all = slices.Compact(c.all)
		return c
	}
	// Whatever follows the word in a file, the key that starts at its first rune starts with the word.
	switch len(runes) {
	case 1:
		c.lo, c.hi = gramKey(runes[0], 0, 0), gramKey(runes[0]+1, 0, 0)
	case 2:
		c.lo, c.hi = gramKey(runes[0], runes[1], 0), gramKey(runes[0], runes[1]+1, 0)
	}
	return c
}

// queryClauses are the clauses of a query's words (words: folded, split at white space). ok is false when a word
// gives none (not valid UTF-8 after folding cannot happen, an empty word neither; the index is then not asked).
func queryClauses(ws []string) (clauses []gramClause, ok bool) {
	for _, w := range ws {
		if w == "" || !utf8.ValidString(w) {
			return nil, false
		}
		c := wordClause(w)
		if len(c.all) == 0 && c.hi == 0 {
			return nil, false
		}
		clauses = append(clauses, c)
	}
	return clauses, len(clauses) > 0
}
