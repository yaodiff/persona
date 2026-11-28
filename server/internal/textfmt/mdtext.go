package textfmt

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
)

// A receipt's summary is the agent's last reply cut for a client to render.

// ClipMarkdown is md cut to at most about n characters for a client to render: on a line break when one falls in the
// second half, and never inside a code span, emphasis, link or code block (an unpaired ** or ` would show as it is).
func ClipMarkdown(md string, n int) string {
	md = wire.AssistantCardText(md)
	if utf8.RuneCountInString(md) <= n {
		return md
	}
	cut := string([]rune(md)[:n])
	midLine := true
	if i := strings.LastIndexByte(cut, '\n'); i >= 0 && utf8.RuneCountInString(cut[:i]) >= n/2 {
		cut, midLine = strings.TrimRight(cut[:i], " \t\n"), false
	}
	lines := strings.Split(cut, "\n")
	if fence := openFence(lines); fence != "" {
		return cut + "\n" + fence + "\n…"
	}
	if midLine {
		lines[len(lines)-1] = strings.TrimRight(closeInline(lines[len(lines)-1]), " \t")
	}
	return strings.Join(lines, "\n") + "…"
}

// closeInline is a line cut in the middle without the code span, emphasis or link it was cut inside. Backtick
// spans pair by the length of their opener (a two-backtick opener closes on two backticks, not one), emphasis marks
// only pair outside code (a bold mark inside a code span is literal), and the cut moves back before the opener it
// still sits inside. It is a truncation heuristic, not a markdown parser: backslash escapes are treated literally.
func closeInline(line string) string {
	for {
		i := unclosedInline(line)
		if i < 0 || i >= len(line) {
			break
		}
		line = strings.TrimRight(line[:i], " \t")
	}
	return line
}

// unclosedInline is the start of the code span, emphasis or link the line ends inside, or -1.
func unclosedInline(line string) int {
	best := -1
	note := func(i int) {
		if i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	inCode, codeLen, codeAt := false, 0, -1
	boldStar, boldUnder, strike, emStar, emUnder := -1, -1, -1, -1, -1
	bracket, inLinkURL := -1, false
	for i := 0; i < len(line); {
		switch {
		case inCode:
			if line[i] == '`' {
				k := markRun(line, i, '`')
				if k == codeLen {
					inCode = false
				}
				i += k
			} else {
				i++
			}
		case line[i] == '`':
			inCode, codeLen, codeAt = true, markRun(line, i, '`'), i
			i += codeLen
		case line[i] == '*': // ** is bold, a lone * is italic; a star flanked by whitespace ("* item", "2 * 3") is literal
			at, k := &emStar, 1
			if strings.HasPrefix(line[i:], "**") {
				at, k = &boldStar, 2
			}
			if opens, closes := emphasisFlanks(line, i, k); opens || closes {
				switch {
				case *at >= 0:
					*at = -1 // an earlier opener is settled
				case opens:
					*at = i // a closer with no opener (as in "x* y") is literal
				}
			}
			i += k
		case line[i] == '_': // as the star, and an underscore flanked by word characters (foo_bar) is literal too
			k := 1
			if strings.HasPrefix(line[i:], "__") {
				k = 2
			}
			opens, closes := emphasisFlanks(line, i, k)
			if (opens || closes) && !intraWordUnderscore(line, i, k) {
				at := &emUnder
				if k == 2 {
					at = &boldUnder
				}
				if *at >= 0 {
					*at = -1 // an earlier opener is settled
				} else if opens {
					*at = i // a closer with no opener stays literal
				}
			}
			i += k
		case strings.HasPrefix(line[i:], "~~"):
			if strike < 0 {
				strike = i
			} else {
				strike = -1
			}
			i += 2
		case line[i] == ']' && bracket >= 0 && !inLinkURL:
			if strings.HasPrefix(line[i+1:], "(") { // [text](url): only the ')' closes it
				inLinkURL = true
			} else {
				bracket = -1 // a bare ']' settles the last '['
			}
			i++
		case line[i] == ')' && inLinkURL:
			bracket, inLinkURL = -1, false
			i++
		case line[i] == '[':
			if !inLinkURL { // a '[' inside the url of an open link stays part of the url
				bracket, inLinkURL = i, false
			}
			i++
		default:
			i++
		}
	}
	if inCode {
		note(codeAt)
	}
	for _, at := range []int{boldStar, boldUnder, strike, emStar, emUnder} {
		note(at)
	}
	if bracket >= 0 { // the '[' has no ']', or its ]( has no ')' yet
		note(bracket)
	}
	return best
}

// markRun is the length of the run of c starting at i.
func markRun(line string, i int, c byte) int {
	j := i
	for j < len(line) && line[j] == c {
		j++
	}
	return j - i
}

// emphasisFlanks reports whether the delimiter run of k marks at i can open or close emphasis. A mark followed by
// whitespace cannot open ("* item"); one ending exactly at the cut might be the opener the text was cut inside, so
// it can. A mark can only close when a non-whitespace character precedes it.
func emphasisFlanks(line string, i, k int) (opens, closes bool) {
	if i+k >= len(line) {
		opens = true // the cut may sit right after the opener
	} else if r, _ := utf8.DecodeRuneInString(line[i+k:]); !unicode.IsSpace(r) {
		opens = true
	}
	if i > 0 {
		r, _ := utf8.DecodeLastRuneInString(line[:i])
		closes = !unicode.IsSpace(r)
	}
	return opens, closes
}

// intraWordUnderscore reports whether the underscore mark at i (length k) has a word character on both sides,
// as in foo_bar: such underscores are literal, not emphasis.
func intraWordUnderscore(line string, i, k int) bool {
	if i == 0 || i+k >= len(line) {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(line[:i])
	q, _ := utf8.DecodeRuneInString(line[i+k:])
	return isWordRune(r) && isWordRune(q)
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// openFence is the ``` or ~~~ of a code block the lines leave open, if any.
func openFence(lines []string) string {
	open := ""
	for _, l := range lines {
		f := leadingFence(strings.TrimSpace(strings.TrimLeft(l, " \t>")))
		switch {
		case f == "":
		case open == "":
			open = f
		case f[0] == open[0] && len(f) >= len(open) && strings.TrimSpace(strings.TrimLeft(l, " \t>")) == f:
			open = ""
		}
	}
	return open
}

// leadingFence is the ``` or ~~~ run a line starts with, if any.
func leadingFence(line string) string {
	for _, c := range []byte{'`', '~'} {
		i := 0
		for i < len(line) && line[i] == c {
			i++
		}
		if i >= 3 {
			return line[:i]
		}
	}
	return ""
}
