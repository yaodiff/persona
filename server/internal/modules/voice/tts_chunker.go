package voice

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Streaming sentence chunker for the TTS bridge (issue #17843).
//
// Accumulates streamed text deltas from a realtime model and emits plain-text pieces for
// speech synthesis. The first piece of a turn cuts early at a weak pause once it holds
// about 6 CJK characters or a few English words (minimizing time-to-first-audio). Later
// pieces cut at sentence ends and newlines, with an upper length bound that splits cleanly
// on a weak pause, word boundary, or character boundary. Decimals, numeric punctuation,
// common abbreviations, and URLs are not split, and light Markdown cleanup keeps syntax
// characters from being read aloud.

// ttsChunkerConfig tunes streaming text-to-sentence boundaries.
// Speech units count 1 per CJK character and 2 per non-CJK word so Chinese and English
// thresholds scale consistently.
type ttsChunkerConfig struct {
	// firstWeakMinUnits is the minimum speech weight before a weak pause (，,；;：:、—)
	// flushes the first piece of a turn (default 6: ~6 CJK chars or 3 English words).
	firstWeakMinUnits int
	// firstMaxUnits is the upper speech-weight bound for the first piece without punctuation.
	firstMaxUnits int
	// firstMaxRunes is the hard rune cap for the first piece (handles unspaced non-CJK runs).
	firstMaxRunes int
	// maxUnits is the upper speech-weight bound for subsequent pieces without a sentence end.
	maxUnits int
	// maxRunes is the hard rune cap for subsequent pieces.
	maxRunes int
}

const ttsChunkerHardBytes = 2048 // safety cap for malformed/unclosed Markdown links or huge tokens

func defaultTTSChunkerConfig() ttsChunkerConfig {
	return ttsChunkerConfig{
		firstWeakMinUnits: 6,
		firstMaxUnits:     24,
		firstMaxRunes:     120,
		maxUnits:          60,
		maxRunes:          240,
	}
}

func (c ttsChunkerConfig) normalized() ttsChunkerConfig {
	def := defaultTTSChunkerConfig()
	if c.firstWeakMinUnits <= 0 {
		c.firstWeakMinUnits = def.firstWeakMinUnits
	}
	if c.firstMaxUnits <= 0 {
		c.firstMaxUnits = def.firstMaxUnits
	}
	if c.firstMaxRunes <= 0 {
		c.firstMaxRunes = def.firstMaxRunes
	}
	if c.maxUnits <= 0 {
		c.maxUnits = def.maxUnits
	}
	if c.maxRunes <= 0 {
		c.maxRunes = def.maxRunes
	}
	return c
}

// ttsChunker accumulates streamed text deltas and emits speakable sentences.
// Not safe for concurrent use; callers (ttsBridge) serialize calls under a lock.
type ttsChunker struct {
	cfg       ttsChunkerConfig
	buf       strings.Builder
	emitted   int
	lineStart bool // whether the start of buf begins a new line
}

func newTTSChunker(cfg ttsChunkerConfig) *ttsChunker {
	return &ttsChunker{cfg: cfg.normalized(), lineStart: true}
}

// Push appends delta and returns zero or more speakable pieces ready for synthesis.
func (c *ttsChunker) Push(delta string) []string {
	if delta == "" {
		return nil
	}
	delta = strings.ReplaceAll(delta, "\r\n", "\n")
	delta = strings.ReplaceAll(delta, "\r", "\n")
	c.buf.WriteString(delta)
	return c.drain(false)
}

// Flush emits any remaining buffered speakable text at the end of a turn.
func (c *ttsChunker) Flush() []string {
	return c.drain(true)
}

// Reset discards buffered text and restarts first-clause detection for a new turn.
func (c *ttsChunker) Reset() {
	c.buf.Reset()
	c.emitted = 0
	c.lineStart = true
}

func (c *ttsChunker) drain(eof bool) []string {
	var out []string
	for {
		s := c.buf.String()
		if s == "" {
			break
		}
		if strings.TrimSpace(s) == "" {
			if eof {
				c.buf.Reset()
				c.lineStart = true
			}
			break
		}
		cut := c.findCut(s, eof)
		if cut <= 0 {
			break
		}
		rawPiece := s[:cut]
		rest := s[cut:]
		c.lineStart = strings.HasSuffix(rawPiece, "\n")
		piece := cleanTTSMarkdown(rawPiece)
		c.buf.Reset()
		c.buf.WriteString(rest)
		if piece != "" {
			out = append(out, piece)
			c.emitted++
		}
	}
	return out
}

func (c *ttsChunker) findCut(s string, eof bool) int {
	if eof {
		return len(s)
	}
	first := c.emitted == 0
	units := 0
	runes := 0
	inWord := false
	inLinkTarget := false
	urlEnd := -1
	lastSpace := -1
	lastWeak := -1
	atLineStart := c.lineStart
	lineStartIdx := 0

	for i := 0; i < len(s); {
		r, sz := utf8.DecodeRuneInString(s[i:])
		next := i + sz
		runes++

		if r == '\n' {
			inWord = false
			inLinkTarget = false
			urlEnd = -1
			atLineStart = true
			lineStartIdx = next
			return next
		}

		// Track Markdown link targets "](...)" so URLs/paths inside them never cause splits
		// or inflate speech weight.
		if inLinkTarget {
			if r == ')' {
				inLinkTarget = false
			}
			if next >= ttsChunkerHardBytes {
				return next
			}
			i = next
			continue
		}
		if r == ']' && next < len(s) && s[next] == '(' {
			inWord = false
			inLinkTarget = true
			i = next + 1
			runes++
			continue
		}

		// Track bare URLs (http://, https://, ftp://, www.) so colons, dots, and query marks
		// inside a URL never split the sentence.
		if i >= urlEnd && isURLStart(s, i) {
			urlEnd = scanURLEnd(s, i)
			// Count a bare URL as one word for weight tracking.
			units += 2
			inWord = false
			atLineStart = false
		}
		if i < urlEnd {
			// If a URL reaches the very end of the current buffer, wait for whitespace or EOF
			// unless the safety byte cap is exceeded.
			if urlEnd == len(s) && next >= ttsChunkerHardBytes {
				return next
			}
			i = next
			continue
		}

		// Skip leading Markdown ordered/unordered list markers ("- ", "1. ", "12) ") for speech
		// weight and sentence-end detection.
		if atLineStart && !unicode.IsSpace(r) {
			if end := matchListMarkerPrefix(s, lineStartIdx); end > i {
				for p := i; p < end; {
					_, psz := utf8.DecodeRuneInString(s[p:])
					p += psz
					if p > next {
						runes++
					}
				}
				atLineStart = false
				i = end
				continue
			}
			atLineStart = false
		}

		// Update speech weight: 1 per CJK rune, 2 per non-CJK alphanumeric word.
		switch {
		case isCJK(r):
			units++
			inWord = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if !inWord {
				units += 2
				inWord = true
			}
		case (r == '\'' || r == '-' || r == '\u2019' || r == '.' || r == ',' || r == ':') && inWord && next < len(s):
			nr, _ := utf8.DecodeRuneInString(s[next:])
			if !unicode.IsLetter(nr) && !unicode.IsDigit(nr) {
				inWord = false
			}
		default:
			inWord = false
		}

		if unicode.IsSpace(r) {
			if (!first && units >= 10) || (first && units >= c.cfg.firstWeakMinUnits) {
				lastSpace = next
			}
		}

		if isStrongPunct(r) {
			if shouldSkipDot(s, i, next, lineStartIdx, c.lineStart, i == urlEnd, r) {
				i = next
				continue
			}
			end := consumeClosers(s, next)
			// Do not split if this punctuation is inside a Markdown link label right before "](".
			if end < len(s) && s[end] == ']' && end+1 < len(s) && s[end+1] == '(' {
				i = next
				continue
			}
			return end
		}

		if isWeakPunct(r) {
			if shouldSkipWeakPunct(s, i, next, r) {
				i = next
				continue
			}
			end := consumeClosers(s, next)
			if end < len(s) && s[end] == ']' && end+1 < len(s) && s[end+1] == '(' {
				i = next
				continue
			}
			if first && units >= c.cfg.firstWeakMinUnits {
				return end
			}
			if (first && units >= 4) || (!first && units >= 12) {
				lastWeak = end
			}
		}

		if first && (units > c.cfg.firstMaxUnits || runes >= c.cfg.firstMaxRunes) {
			if lastWeak > 0 {
				return lastWeak
			}
			if lastSpace > 0 {
				return lastSpace
			}
			return next
		}
		if !first && (units > c.cfg.maxUnits || runes >= c.cfg.maxRunes) {
			if lastWeak > 0 {
				return lastWeak
			}
			if lastSpace > 0 {
				return lastSpace
			}
			return next
		}

		i = next
	}
	return 0
}

// shouldSkipDot reports whether '.' at index i (ending at next) should not end a sentence:
// decimals (3.14), domains/versions (v1.0, example.com), streamed digit-dot ("3."),
// line-start list markers ("1."), or common abbreviations (Dr., Mr., e.g., U.S.).
func shouldSkipDot(s string, i, next, lineStartIdx int, bufAtLineStart, afterURL bool, r rune) bool {
	if r != '.' {
		return false
	}
	// Followed by digit or letter: decimal (3.14), version (v1.0), domain (a.com), acronym (e.g.).
	if next < len(s) {
		nr, _ := utf8.DecodeRuneInString(s[next:])
		if unicode.IsDigit(nr) || unicode.IsLetter(nr) {
			return true
		}
	}
	// Preceded by a digit at the end of the current buffer: could be a streamed decimal ("3." + "14")
	// or a streamed ordered list marker ("1." + " item").
	if i > 0 {
		pr, _ := utf8.DecodeLastRuneInString(s[:i])
		if unicode.IsDigit(pr) && next == len(s) {
			return true
		}
	}
	// Ordered list marker at start of a real line ("1. ", "12. ").
	if (lineStartIdx > 0 || bufAtLineStart) && isLineStartListDot(s, lineStartIdx, i) {
		return true
	}
	// Common abbreviations and single-letter initials (Dr., Mr., e.g., U.S., J.),
	// except when the dot directly closes a URL token.
	if !afterURL && isAbbreviationBeforeDot(s, i) {
		return true
	}
	return false
}

// shouldSkipWeakPunct skips ',' or ':' inside numbers/times (1,000, 14:30) and ':' before "//".
func shouldSkipWeakPunct(s string, i, next int, r rune) bool {
	if r == ':' && strings.HasPrefix(s[next:], "//") {
		return true
	}
	if r != ',' && r != ':' {
		return false
	}
	if i == 0 {
		return false
	}
	pr, _ := utf8.DecodeLastRuneInString(s[:i])
	if !unicode.IsDigit(pr) {
		return false
	}
	if next == len(s) {
		// Wait one delta so streamed "1," + "000" or "14:" + "30" is not split.
		return true
	}
	nr, _ := utf8.DecodeRuneInString(s[next:])
	return unicode.IsDigit(nr)
}

func isLineStartListDot(s string, lineStart, dotIdx int) bool {
	p := lineStart
	for p < dotIdx && (s[p] == ' ' || s[p] == '\t') {
		p++
	}
	digits := dotIdx - p
	if digits < 1 || digits > 3 {
		return false
	}
	for k := p; k < dotIdx; k++ {
		if s[k] < '0' || s[k] > '9' {
			return false
		}
	}
	return true
}

func matchListMarkerPrefix(s string, lineStart int) int {
	p := lineStart
	for p < len(s) && (s[p] == ' ' || s[p] == '\t') {
		p++
	}
	if p >= len(s) {
		return 0
	}
	// Unordered list marker: "- ", "* ", "+ "
	if (s[p] == '-' || s[p] == '*' || s[p] == '+') && p+1 < len(s) && (s[p+1] == ' ' || s[p+1] == '\t') {
		return p + 2
	}
	// Ordered list marker: 1-3 digits followed by '.' or ')' and space.
	d := p
	for d < len(s) && d-p < 3 && s[d] >= '0' && s[d] <= '9' {
		d++
	}
	if d > p && d+1 < len(s) && (s[d] == '.' || s[d] == ')') && (s[d+1] == ' ' || s[d+1] == '\t') {
		return d + 2
	}
	return 0
}

func isURLStart(s string, i int) bool {
	if i > 0 {
		pr, _ := utf8.DecodeLastRuneInString(s[:i])
		if unicode.IsLetter(pr) || unicode.IsDigit(pr) {
			return false
		}
	}
	rest := s[i:]
	return strings.HasPrefix(rest, "https://") ||
		strings.HasPrefix(rest, "http://") ||
		strings.HasPrefix(rest, "ftp://") ||
		strings.HasPrefix(rest, "www.")
}

// scanURLEnd returns the exclusive byte end of the URL starting at start.
// Sentence-ending punctuation immediately before whitespace is excluded so a URL at the end
// of a sentence still lets the trailing period split the sentence.
func scanURLEnd(s string, start int) int {
	end := start
	for end < len(s) {
		r, sz := utf8.DecodeRuneInString(s[end:])
		if unicode.IsSpace(r) || r > 0x7F || strings.ContainsRune(`<>()"'[]{}`, r) {
			break
		}
		end += sz
	}
	// If the URL ended before the buffer end (followed by whitespace or closing bracket),
	// strip trailing sentence punctuation that belongs to the surrounding sentence.
	if end < len(s) {
		for end > start && strings.ContainsRune(".,;:!?", rune(s[end-1])) {
			end--
		}
	}
	return end
}

var ttsAbbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true,
	"sr": true, "jr": true, "st": true, "ave": true, "rd": true,
	"blvd": true, "mt": true, "ft": true, "vs": true, "etc": true,
	"e.g": true, "i.e": true, "eg": true, "ie": true, "fig": true,
	"no": true, "vol": true, "ch": true, "sec": true, "approx": true,
	"est": true, "dept": true, "inc": true, "ltd": true, "corp": true,
	"co": true, "gov": true, "gen": true, "col": true, "lt": true,
	"sgt": true, "capt": true, "rev": true, "hon": true, "pres": true,
	"sen": true, "rep": true, "jan": true, "feb": true, "mar": true,
	"apr": true, "jun": true, "jul": true, "aug": true, "sep": true,
	"sept": true, "oct": true, "nov": true, "dec": true,
	"a.m": true, "p.m": true, "u.s": true, "u.k": true, "u.s.a": true,
	"ph.d": true, "m.d": true, "b.a": true, "m.a": true, "al": true,
}

func isAbbreviationBeforeDot(s string, dotIdx int) bool {
	// Walk backwards over ASCII letters and internal dots (e.g. "e.g", "U.S", "Dr").
	start := dotIdx
	for start > 0 {
		b := s[start-1]
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || b == '.' {
			start--
			continue
		}
		break
	}
	word := strings.TrimLeft(s[start:dotIdx], ".")
	if word == "" {
		return false
	}
	// If preceded immediately by a non-space, non-opening-punct word char (e.g. CJK or digit),
	// don't treat as an English abbreviation.
	if start > 0 {
		pr, _ := utf8.DecodeLastRuneInString(s[:start])
		if unicode.IsLetter(pr) || unicode.IsDigit(pr) {
			return false
		}
	}
	// Single uppercase letter initial like "J." in "J. K. Rowling" or "U." in "U.S."
	if len(word) == 1 && word[0] >= 'A' && word[0] <= 'Z' {
		return true
	}
	// Multi-letter dotted acronym like "U.N." or "D.C."
	if strings.Contains(word, ".") {
		parts := strings.Split(word, ".")
		allShort := true
		for _, p := range parts {
			if len(p) != 1 {
				allShort = false
				break
			}
		}
		if allShort {
			return true
		}
	}
	return ttsAbbreviations[strings.ToLower(word)]
}

func consumeClosers(s string, pos int) int {
	for pos < len(s) {
		r, sz := utf8.DecodeRuneInString(s[pos:])
		if strings.ContainsRune(`”’"'）】》」』.!！?？…`, r) { // i18n-ignore: CJK and ASCII closing quotes/brackets/punctuation for sentence chunking
			pos += sz
			continue
		}
		break
	}
	return pos
}

func isStrongPunct(r rune) bool {
	return strings.ContainsRune("。！？.!?\n…", r) // i18n-ignore: CJK and ASCII sentence-ending punctuation for chunking
}

func isWeakPunct(r rune) bool {
	return strings.ContainsRune("，,；;：:、—", r) // i18n-ignore: CJK and ASCII weak-pause punctuation for early first-clause chunking
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

var (
	ttsFenceRe      = lazyre.New("^\\s{0,3}(?:`{3,}|~{3,}).*$")
	ttsRuleRe       = lazyre.New(`^\s{0,3}(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)
	ttsHeadingRe    = lazyre.New(`^\s{0,3}#{1,6}\s+`)
	ttsQuoteRe      = lazyre.New(`^\s{0,3}(?:>\s?)+`)
	ttsListRe       = lazyre.New(`^\s*(?:[-*+]|\d{1,3}[.)])\s+(?:\[[ xX]\]\s+)?`)
	ttsImageRe      = lazyre.New(`!\[([^\]\n]*)\]\([^)\n]*\)`)
	ttsLinkRe       = lazyre.New(`\[([^\]\n]+)\]\([^)\s]+(?:\s+"[^"\n]*")?\)`)
	ttsUnderBoldRe  = lazyre.New(`(^|[^\w])__([^\n_]+?)__($|[^\w])`)
	ttsUnderEmRe    = lazyre.New(`(^|[^\w])_([^_\s][^_\n]*?)_($|[^\w])`)
	ttsMultiSpaceRe = lazyre.New(`\s+`)
)

// cleanTTSMarkdown removes light Markdown syntax (bold, italic, backticks, headings,
// list markers, blockquotes, links) so TTS never reads formatting symbols aloud.
// Returns "" if no speakable letter or digit remains.
func cleanTTSMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if ttsFenceRe.MatchString(line) || ttsRuleRe.MatchString(line) {
			continue
		}
		line = ttsHeadingRe.ReplaceAllString(line, "")
		line = ttsQuoteRe.ReplaceAllString(line, "")
		line = ttsListRe.ReplaceAllString(line, "")
		line = ttsImageRe.ReplaceAllString(line, "$1")
		line = ttsLinkRe.ReplaceAllString(line, "$1")
		line = strings.NewReplacer("**", "", "~~", "", "`", "").Replace(line)
		line = ttsUnderBoldRe.ReplaceAllString(line, "$1$2$3")
		line = ttsUnderEmRe.ReplaceAllString(line, "$1$2$3")
		line = stripEmphasisStars(line)
		if t := strings.TrimSpace(line); t != "" {
			kept = append(kept, t)
		}
	}
	out := strings.TrimSpace(ttsMultiSpaceRe.ReplaceAllString(strings.Join(kept, " "), " "))
	if !hasTTSSpeakableRune(out) {
		return ""
	}
	return out
}

// stripEmphasisStars removes '*' Markdown emphasis markers (including unpaired '*' left
// when a clause splits inside italic text) while preserving spaced arithmetic like "2 * 3".
func stripEmphasisStars(s string) string {
	if !strings.ContainsRune(s, '*') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '*' {
			prevSpace := i > 0 && s[i-1] == ' '
			nextSpace := i+1 < len(s) && s[i+1] == ' '
			if prevSpace && nextSpace {
				b.WriteByte('*')
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func hasTTSSpeakableRune(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
