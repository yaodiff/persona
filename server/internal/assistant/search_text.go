package assistant

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
	"golang.org/x/text/unicode/norm"
)

// What the chat search index keeps of a message, and how a search matches it (#6282, the shared rule of
// spec/fixtures/chat-search, its "index": docs/design/proposals/assistant-search.md "One rule for what is searchable").
// The user's own words and the assistant's replies, as the chat shows them; never a hidden tag, a quoted context, a
// thought, a tool or a message Persona wrote for the model. The web reads the same rule in lib/chat-search-match.ts.

var (
	// A quoted context with its attributes (web lib/compatibility.ts CONTEXT_RE); transcript.ContextRe is the snapshot's bare tag.
	searchContextRe  = lazyre.New(`(?s)<` + compat.ContextPattern() + `\b[^>]*>.*?</` + compat.ContextPattern() + `>\s*`)
	searchFileRe     = lazyre.New(`<` + compat.FilePattern() + `\b[^>]*?/?>(?:\s*</` + compat.FilePattern() + `>)?`)
	searchPastedRe   = lazyre.New(`(?s)<pasted_content\b[^>]*>(.*?)</pasted_content>`)
	searchImagesRe   = lazyre.New(`(?s)<attached_images>.*?</attached_images>`)
	searchImageNumRe = lazyre.New(`\[Image #\d+\]\s?`)
	// The hub's tag a card's answer ends with (view_actions.go): hidden in the chat, so never indexed.
	searchActionRe = lazyre.New(`\n<persona-action view="[^"<>]*" action="[^"<>]*">\{[^<>]*\}</persona-action>$`)
)

// searchIndexText is the text the index keeps of it, and false for an item it keeps nothing of.
func searchIndexText(it proto.Item) (string, bool) {
	if it.Kind != proto.KindText {
		return "", false
	}
	var text string
	switch it.Role {
	case "assistant":
		// a card as its fallback words, never its JSON; the rest as the chat shows it, without Markdown's markup (#7072)
		text = searchPlain(wire.AssistantCardText(it.Text))
	case "user":
		if it.From != "" || searchInjected(it) {
			return "", false // another session's message, or Persona's to the model: not the user's words
		}
		text = searchUserText(it.Text)
	default:
		return "", false
	}
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	return text, true
}

// searchInjected: a message Persona wrote for the model (the hub's withInjected reads it the same way).
func searchInjected(it proto.Item) bool {
	if it.Injected != nil {
		return true
	}
	if it.ClientMsgID != "" || prompts.UserAuthoredContext(it.Text) {
		return false
	}
	return prompts.ParseInjected(it.Text) != nil
}

// searchUserText is the user's words as the bubble shows them: without the view errors, quoted contexts, files,
// photos and the card answer's tag they travel with; pasted text as itself.
func searchUserText(s string) string {
	s = transcript.ViewErrorsRe.Regexp().ReplaceAllString(s, "")
	s = searchContextRe.Regexp().ReplaceAllString(s, "")
	s = searchFileRe.Regexp().ReplaceAllString(s, "")
	s = searchPastedRe.Regexp().ReplaceAllString(s, "$1")
	s = searchImagesRe.Regexp().ReplaceAllString(s, "")
	s = searchImageNumRe.Regexp().ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	return strings.TrimSpace(searchActionRe.Regexp().ReplaceAllString(s, ""))
}

// A reply's Markdown as the chat shows it (#7072): the patterns searchPlain takes the markup away with. The mock hub
// (scripts/mock/mock_hub.py search_plain) keeps the same rule; spec/fixtures/chat-search checks both.
var (
	searchFenceRe    = lazyre.New("^[ \t]{0,3}(`{3,}|~{3,})")
	searchRuleRe     = lazyre.New(`^[ \t]{0,3}(?:(?:-[ \t]*){3,}|(?:\*[ \t]*){3,}|(?:_[ \t]*){3,})$`)
	searchTableSepRe = lazyre.New(`^[ \t]*\|?[ \t]*:?-{3,}:?[ \t]*(?:\|[ \t]*:?-{3,}:?[ \t]*)*\|?[ \t]*$`)
	searchHeadingRe  = lazyre.New(`^[ \t]{0,3}#{1,6}[ \t]+`)
	searchQuoteRe    = lazyre.New(`^[ \t]{0,3}>[ \t]?`)
	searchListRe     = lazyre.New(`^[ \t]*(?:[-*+]|[0-9]{1,9}[.)])[ \t]+(?:\[[ xX]\][ \t]+)?`)
	searchEscapeRe   = lazyre.New("\\\\([\\\\`*_{}\\[\\]()#+\\-.!|>~<])")
	// images and links keep their text, autolinks their address
	searchLinkRes = []searchSub{
		{lazyre.New(`!\[([^\]\n]*)\]\([^)\n]*\)`), "$1"},
		{lazyre.New(`\[([^\]\n]+)\]\([^)\n]*\)`), "$1"},
		{lazyre.New(`\[([^\]\n]+)\]\[[^\]\n]*\]`), "$1"},
		{lazyre.New(`<((?:https?|mailto):[^> \t\n]+)>`), "$1"},
	}
	// after `**` (searchStrong), the rest of emphasis keeps its words
	searchEmphasisRes = []searchSub{
		{lazyre.New(`(^|[^0-9A-Za-z_])__([^ \t\n_]|[^ \t\n_][^\n]*?[^ \t\n_])__($|[^0-9A-Za-z_])`), "$1$2$3"},
		{lazyre.New(`~~([^~\n]+?)~~`), "$1"},
		{lazyre.New(`\*([^ \t\n*]|[^ \t\n*][^*\n]*?[^ \t\n*])\*`), "$1"},
		{lazyre.New(`(^|[^0-9A-Za-z_])_([^ \t\n_]|[^ \t\n_][^_\n]*?[^ \t\n_])_($|[^0-9A-Za-z_])`), "$1$2$3"},
	}
)

// searchSub is a pattern of markup and what it leaves.
type searchSub struct {
	re   *lazyre.Regexp
	with string
}

// searchPlain is a reply's Markdown as the chat shows it: code fence lines, rules and table separators dropped (the
// code kept), heading, quote, list and task-box markers taken off each line, a table row's cells joined with spaces,
// then searchInline on each line. Snippets are cut from what it returns, so no marker shows in one.
func searchPlain(md string) string {
	if !strings.ContainsAny(md, "*_`#>[~|-+!<\\0123456789") {
		return md
	}
	lines := strings.Split(md, "\n")
	out := lines[:0]
	fence := "" // the open code fence's marker, "" outside one
	for _, line := range lines {
		if fence != "" {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				fence = ""
			} else {
				out = append(out, line)
			}
			continue
		}
		if m := searchFenceRe.Regexp().FindStringSubmatch(line); m != nil {
			fence = m[1]
			continue
		}
		if searchRuleRe.Regexp().MatchString(line) || searchTableSepRe.Regexp().MatchString(line) {
			continue
		}
		line = searchHeadingRe.Regexp().ReplaceAllString(line, "")
		for searchQuoteRe.Regexp().MatchString(line) {
			line = searchQuoteRe.Regexp().ReplaceAllString(line, "")
		}
		line = searchListRe.Regexp().ReplaceAllString(line, "")
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "|") {
			cells := strings.Split(strings.Trim(t, "|"), "|")
			for i, c := range cells {
				cells[i] = strings.TrimSpace(c)
			}
			line = strings.Join(cells, " ")
		}
		out = append(out, searchInline(line))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// searchEscaped stands for a backslash-escaped character while the markup is taken off, so `\*` stays a star.
const searchEscaped = 0xE000

// searchCode stands for a line's i-th code span (searchCode+i) while the markup around it is taken off (#7195): bold or
// a link that holds code pairs across it, and nothing inside the code reads as markup.
const searchCode = 0xF0000

// searchInline is a line without its inline markup: a code span keeps what is inside it, as it is; the text around
// it loses link, image and emphasis syntax; an escaped character is itself.
func searchInline(line string) string {
	line = searchEscapeRe.Regexp().ReplaceAllStringFunc(line, func(m string) string { return string(rune(searchEscaped + int(m[1]))) })
	var code []string
	var b strings.Builder
	for line != "" {
		at, n, end, ok := searchCodeSpan(line)
		if !ok {
			b.WriteString(line)
			break
		}
		b.WriteString(line[:at])
		c := line[at+n : end]
		if len(c) >= 2 && c[0] == ' ' && c[len(c)-1] == ' ' && strings.Trim(c, " ") != "" {
			c = c[1 : len(c)-1]
		}
		b.WriteRune(rune(searchCode + len(code)))
		code = append(code, c)
		line = line[end+n:]
	}
	unescape := func(r rune) rune {
		if r >= searchEscaped && r < searchEscaped+128 {
			return r - searchEscaped
		}
		return r
	}
	var out strings.Builder
	for _, r := range searchMarks(b.String()) {
		if i := int(r - searchCode); r >= searchCode && i < len(code) {
			out.WriteString(strings.Map(unescape, code[i]))
			continue
		}
		out.WriteRune(unescape(r))
	}
	return out.String()
}

// searchCodeSpan finds the first code span in s: where its opening backticks start, how many there are, and where its
// closing run (as many backticks, no more) starts; false when there is none.
func searchCodeSpan(s string) (at, n, end int, ok bool) {
	from := 0
	for {
		i := strings.IndexByte(s[from:], '`')
		if i < 0 {
			return 0, 0, 0, false
		}
		at = from + i
		n = 0
		for at+n < len(s) && s[at+n] == '`' {
			n++
		}
		for j := at + n; j < len(s); {
			k := strings.IndexByte(s[j:], '`')
			if k < 0 {
				break
			}
			k += j
			m := 0
			for k+m < len(s) && s[k+m] == '`' {
				m++
			}
			if m == n {
				return at, n, k, true
			}
			j = k + m
		}
		from = at + n // backticks with no closing run are themselves
	}
}

// searchMarks is a line, its code spans standing in for themselves, without link, image and emphasis syntax.
func searchMarks(s string) string {
	for _, r := range searchLinkRes {
		s = r.re.Regexp().ReplaceAllString(s, r.with)
	}
	s = searchStrong(s)
	for _, r := range searchEmphasisRes {
		s = r.re.Regexp().ReplaceAllString(s, r.with)
		if strings.HasSuffix(r.with, "$3") {
			s = r.re.Regexp().ReplaceAllString(s, r.with) // a match takes the character between two: again for the next
		}
	}
	return s
}

// searchStrong is a line without `**` (#7195). A run of two or more stars not after a space closes the nearest open
// run before it, and one not before a space opens; a pair loses two stars on its inner side each (`***a***` leaves
// `*a*` for the emphasis after it). A run left without a partner goes too (bold cut off, or across lines: each line
// is read alone), unless it reads as an expression: `2 ** 3`, a space on each side, or `a**b`, a letter or digit
// (two of those on one line pair, as the chat pairs them).
func searchStrong(s string) string {
	if !strings.Contains(s, "**") {
		return s
	}
	type run struct{ at, end, cutFrom, cutTo int }
	var runs []run
	for i := 0; i < len(s); {
		if s[i] != '*' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '*' {
			j++
		}
		if j-i >= 2 {
			runs = append(runs, run{at: i, end: j, cutFrom: i, cutTo: j}) // all of it, unless it pairs or stays
		}
		i = j
	}
	var open []int // runs that may open, the nearest last
	paired := make([]bool, len(runs))
	for k, r := range runs {
		before := r.at > 0 && !searchSpace(s[r.at-1])
		after := r.end < len(s) && !searchSpace(s[r.end])
		if before && len(open) > 0 {
			o := open[len(open)-1]
			open = open[:len(open)-1]
			paired[o], paired[k] = true, true
			runs[o].cutFrom, runs[o].cutTo = runs[o].end-2, runs[o].end
			runs[k].cutFrom, runs[k].cutTo = r.at, r.at+2
			continue
		}
		if after {
			open = append(open, k)
		}
	}
	var b strings.Builder
	from := 0
	for k, r := range runs {
		if !paired[k] && r.at > 0 && r.end < len(s) &&
			(searchSpace(s[r.at-1]) && searchSpace(s[r.end]) || searchOperand(s[r.at-1]) && searchOperand(s[r.end])) {
			continue // an expression: it stays
		}
		b.WriteString(s[from:r.cutFrom])
		from = r.cutTo
	}
	b.WriteString(s[from:])
	return b.String()
}

func searchSpace(c byte) bool { return c == ' ' || c == '\t' }

func searchOperand(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// searchFold is what a search matches: NFKC (full and half width alike) and lower case.
func searchFold(s string) string { return strings.ToLower(norm.NFKC.String(s)) }

// searchMaxRunes is the longest query read; searchMaxWords the most words of it that must all match.
const (
	searchMaxRunes = 200
	searchMaxWords = 8
)

// searchWords is a query's words, folded, each once, in order: every one must be in a message (or the title).
func searchWords(q string) []string {
	r := []rune(strings.TrimSpace(q))
	if len(r) > searchMaxRunes {
		r = r[:searchMaxRunes]
	}
	var out []string
	for _, w := range strings.Fields(searchFold(string(r))) {
		if len(out) == searchMaxWords {
			break
		}
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// searchHolds: folded holds every word.
func searchHolds(folded string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(folded, w) {
			return false
		}
	}
	return len(words) > 0
}

// How much of the text a snippet shows before and after the first match, in characters.
const (
	snippetBefore = 40
	snippetAfter  = 80
)

// searchSnippet is the text around the first match of words in body, on one line, with "…" where it was cut, and the
// [start, end) of every match in it as UTF-16 offsets (as the apps count them); the start of body when no word is
// found in it rune by rune (a match only the whole text's normalization makes).
func searchSnippet(body string, words []string) (string, [][2]int) {
	runes := []rune(body)
	folded, from := searchFoldRunes(runes)
	start, end := 0, min(len(runes), snippetBefore+snippetAfter)
	for _, w := range words {
		if at := searchIndexRunes(folded, []rune(w), 0); at >= 0 {
			first, last := from[at], from[at+len([]rune(w))-1]
			start, end = max(0, first-snippetBefore), min(len(runes), last+1+snippetAfter)
			break
		}
	}
	var b strings.Builder
	if start > 0 {
		b.WriteString("…")
	}
	b.WriteString(searchOneLine(string(runes[start:end])))
	if end < len(runes) {
		b.WriteString("…")
	}
	snippet := b.String()
	return snippet, searchMatchRanges(snippet, words)
}

// searchOneLine joins a text's lines with a space, as the apps' snippets do.
func searchOneLine(s string) string {
	lines := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	for i := range lines {
		lines[i] = strings.TrimFunc(lines[i], unicode.IsSpace)
	}
	return strings.Join(slices.DeleteFunc(lines, func(l string) bool { return l == "" }), " ")
}

// searchFoldRunes folds a text rune by rune, and for each folded rune the index of the rune it came from.
func searchFoldRunes(runes []rune) ([]rune, []int) {
	folded := make([]rune, 0, len(runes))
	from := make([]int, 0, len(runes))
	for i, r := range runes {
		for _, f := range searchFold(string(r)) {
			folded = append(folded, f)
			from = append(from, i)
		}
	}
	return folded, from
}

func searchIndexRunes(s, sub []rune, at int) int {
	if len(sub) == 0 {
		return -1
	}
	for i := at; i+len(sub) <= len(s); i++ {
		match := true
		for j := range sub {
			if s[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// searchMatchRanges is every match of words in text as UTF-16 [start, end), in order, overlaps joined.
func searchMatchRanges(text string, words []string) [][2]int {
	runes := []rune(text)
	folded, from := searchFoldRunes(runes)
	// the UTF-16 offset each rune starts at, and the end
	at := make([]int, len(runes)+1)
	for i, r := range runes {
		at[i+1] = at[i] + len(utf16.Encode([]rune{r}))
	}
	marked := make([]bool, len(runes))
	for _, w := range words {
		sub := []rune(w)
		for i := searchIndexRunes(folded, sub, 0); i >= 0; i = searchIndexRunes(folded, sub, i+len(sub)) {
			for j := i; j < i+len(sub); j++ {
				marked[from[j]] = true
			}
		}
	}
	var out [][2]int
	for i := 0; i < len(runes); {
		if !marked[i] {
			i++
			continue
		}
		j := i
		for j < len(runes) && marked[j] {
			j++
		}
		out = append(out, [2]int{at[i], at[j]})
		i = j
	}
	return out
}
