package transcript

import (
	"encoding/json"
	"html"
	"path"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"mvdan.cc/sh/v3/syntax"
)

// BotMux (the Lark topic ↔ coding CLI bridge, docs/integrations/engines.md "botmux") hands the agent each Lark
// message wrapped in blocks for the model: <user_message> around what was written, <sender />, <mentions>,
// <attachments>, <message_location />, <botmux_reminder>, <botmux_formatting> and the like, and the agent answers
// with `botmux send`. BotmuxMessage and BotmuxSend read those back for the apps (proto.Botmux): the words and a few
// facts to show as tags. Anything they cannot read stays as it is (nil), and Item.Text always keeps the original.
// The formats are BotMux 2.50 through 3.59.20 (docs/protocol/protocol.md "BotMux messages"); spec/fixtures/botmux is
// shared with the web app.
//
// Reading the prompt wrapper and the command line back is a compatibility fallback. BotMux's wrapper is text for the
// model, not a published format, and can change in any release. When BotMux offers a versioned, structured record of
// each turn (the inbound message, its sender and attachments, and what was sent back), that record comes first and
// this parsing stays only for sessions and BotMux versions without it.

const (
	botmuxOpen  = "<user_message>"
	botmuxClose = "</user_message>"
	// botmuxTaskOpen starts what an /api/trigger turn asks (botmux's buildExternalEventApplicationContext): the
	// node's own sends to a botmux session arrive that way (node/hostbotmux.go Prompt).
	botmuxTaskOpen  = `<botmux_task trusted="true">`
	botmuxTaskClose = "</botmux_task>"
)

// botmuxOpenTagRe is a block's opening tag at the start of the text, its name in group 1.
var botmuxOpenTagRe = lazyre.New(`^<([a-z][a-z0-9_]*)\b[^<>]*>`)

var (
	botmuxSenderRe   = lazyre.New(`<sender\b([^<>]*)/>`)
	botmuxMentionRe  = lazyre.New(`<mention\b([^<>]*)/>`)
	botmuxAttachRe   = lazyre.New(`(?s)<attachments\b[^<>]*>(.*?)</attachments>`)
	botmuxAttachItem = lazyre.New(`<(?:image|file|video|audio)\s+n="`)
	botmuxLocationRe = lazyre.New(`<message_location\b([^<>]*)/>`)
	botmuxAttrRe     = lazyre.New(`([a-z_]+)="([^"]*)"`)
	// botmuxFormattingTail is the formatting block BotMux puts after the words when it sends them bare (its
	// replyDelivery "transcript" with one person in the chat): no <user_message> then.
	botmuxFormattingTail = lazyre.New(`(?s)\s*<botmux_formatting>.*?</botmux_formatting>\s*$`)
	// botmuxQuoteHint and botmuxTopicHint are the lines BotMux starts the words with when the message quoted another
	// one, and on the first turn of a topic that had messages before (its prompt.quote_hint, prompt.topic_context),
	// in Chinese and English.
	botmuxQuoteHint = lazyre.New(`^\s*\[(?:用户引用了消息 用 botmux quoted \S+ 查看|User quoted a message — run ` + "`botmux quoted [^`]+`" + ` to view it)\]\s*\n?`) // i18n-ignore: matches BotMux's text
	botmuxTopicHint = lazyre.New(`^\s*\[(?:本条是话题内的回复，[^\n]*|This is a reply inside a topic that already had prior messages[^\n]*)\]\s*\n?`)                 // i18n-ignore: matches BotMux's text
	botmuxSourceRe  = lazyre.New(`"sourceName":\s*("(?:[^"\\]|\\.)*")`)
)

// BotmuxMessage reads a user line BotMux wrote: nil when s is not one.
func BotmuxMessage(s string) *proto.Botmux {
	if !strings.Contains(s, "<user_message>") && !strings.Contains(s, "<botmux_") {
		return nil // most lines: no copy, no regexp
	}
	t := strings.TrimSpace(s)
	b := &proto.Botmux{Kind: proto.BotmuxMessage}
	var body string
	switch open := strings.Index(t, botmuxOpen); {
	case open < 0 && botmuxFormattingTail.MatchString(t):
		body = botmuxFormattingTail.ReplaceAllString(t, "")
	case strings.HasPrefix(t, botmuxTaskOpen):
		return botmuxTask(t)
	case open >= 0:
		rest, after := t[open+len(botmuxOpen):], ""
		end := strings.LastIndex(rest, botmuxClose)
		if end < 0 { // clipped inside the words: what there is of them
			body, b.Truncated = rest, true
		} else {
			body, after = rest[:end], rest[end+len(botmuxClose):]
		}
		before, ok1 := botmuxBlocks(t[:open])
		behind, ok2 := botmuxBlocks(after)
		if !ok1 || !ok2 {
			return nil // something else that happens to use the tag
		}
		for name, bs := range behind {
			before[name] = append(before[name], bs...)
		}
		botmuxFacts(b, before)
		if len(before["sender"]) == 0 {
			for _, block := range before["botmux_task"] {
				if task := botmuxTask(block); task != nil {
					return task
				}
			}
		}
	default:
		return nil
	}
	body = strings.TrimSpace(body)
	for {
		if loc := botmuxQuoteHint.FindStringIndex(body); loc != nil {
			b.Quoted, body = true, body[loc[1]:]
		} else if loc := botmuxTopicHint.FindStringIndex(body); loc != nil {
			b.TopicContext, body = true, body[loc[1]:]
		} else {
			break
		}
	}
	b.Text = strings.TrimSpace(body)
	if b.Text == "" && b.Attachments == 0 && !b.Quoted {
		return nil
	}
	return b
}

// botmuxOnlyBlocks: s has nothing but blocks (and blank space) outside them, as BotMux writes around the words.
func botmuxOnlyBlocks(s string) bool {
	_, ok := botmuxBlocks(s)
	return ok
}

// botmuxBlocks splits s into its top-level blocks, <tag …>…</tag> or <tag … />, keyed by tag name; ok is false when
// anything else is outside them. A block ends at the closing tag of its own name, so children such as <identity>'s
// <name> and <open_id> stay inside it.
func botmuxBlocks(s string) (blocks map[string][]string, ok bool) {
	blocks = map[string][]string{}
	for {
		s = strings.TrimSpace(s)
		if s == "" {
			return blocks, true
		}
		m := botmuxOpenTagRe.FindStringSubmatchIndex(s)
		if m == nil {
			return nil, false
		}
		name, head := s[m[2]:m[3]], s[:m[1]]
		if strings.HasSuffix(head, "/>") {
			blocks[name] = append(blocks[name], head)
			s = s[m[1]:]
			continue
		}
		end := strings.Index(s[m[1]:], "</"+name+">")
		if end < 0 {
			return nil, false
		}
		end += m[1] + len("</"+name+">")
		blocks[name] = append(blocks[name], s[:end])
		s = s[end:]
	}
}

// botmuxTask is what a trigger turn asks, from its <botmux_task> block.
func botmuxTask(block string) *proto.Botmux {
	body, ok := strings.CutPrefix(block, botmuxTaskOpen)
	if !ok {
		return nil
	}
	body, _, _ = strings.Cut(body, botmuxTaskClose)
	task := &proto.Botmux{Kind: proto.BotmuxTrigger, Text: strings.TrimSpace(body)}
	if match := botmuxSourceRe.FindStringSubmatch(body); match != nil {
		_ = json.Unmarshal([]byte(match[1]), &task.Source)
	}
	return task
}

// botmuxFacts reads the tags' facts off the top-level blocks around the words, each from its own block only (a
// routing or role block may quote the same tags as examples).
func botmuxFacts(b *proto.Botmux, blocks map[string][]string) {
	first := func(name string) string {
		if bs := blocks[name]; len(bs) > 0 {
			return bs[0]
		}
		return ""
	}
	if m := botmuxSenderRe.FindStringSubmatch(first("sender")); m != nil {
		a := botmuxAttrs(m[1])
		b.Sender, b.SenderType = a["name"], proto.BotmuxSenderType(a["type"])
	}
	for _, m := range botmuxMentionRe.FindAllStringSubmatch(first("mentions"), -1) {
		if name := botmuxAttrs(m[1])["name"]; name != "" {
			b.Mentions = append(b.Mentions, name)
		}
	}
	if m := botmuxAttachRe.FindStringSubmatch(first("attachments")); m != nil {
		b.Attachments = len(botmuxAttachItem.FindAllStringIndex(m[1], -1))
	}
	if m := botmuxLocationRe.FindStringSubmatch(first("message_location")); m != nil {
		b.Thread = botmuxAttrs(m[1])["placement"] == "thread"
	}
}

func botmuxAttrs(s string) map[string]string {
	out := map[string]string{}
	for _, m := range botmuxAttrRe.FindAllStringSubmatch(s, -1) {
		out[m[1]] = html.UnescapeString(m[2])
	}
	return out
}

// botmuxSendFlag is how a `botmux send` flag takes its value.
type botmuxSendFlag int

const (
	sendFlagBool      botmuxSendFlag = iota + 1 // no value
	sendFlagValue                               // the next word, or --flag=value
	sendFlagOptional                            // only --flag=value; the next word is not its value
	sendFlagFileValue                           // value flag whose reply body is opaque to wrapper parsing (--content-file, --card-file, --card-json)
	sendFlagGiveUp                              // nothing is sent (--dry-run, --help)
)

// botmuxSendFlags is `botmux send`'s flag table (BotMux 3.59 src/cli/send-invocation.ts SEND_FLAG_SPECS, the same from
// 3.59.6 to 3.59.20). A flag not in it makes BotMux refuse the send, so BotmuxSend gives up on it too.
var botmuxSendFlags = map[string]botmuxSendFlag{
	"--card": sendFlagBool, "--text": sendFlagBool, "--top-level": sendFlagBool, "--no-quote": sendFlagBool,
	"--mention-back": sendFlagBool, "--no-mention": sendFlagBool, "--voice": sendFlagBool, "--slash": sendFlagBool,
	"--remote-runner-outbound": sendFlagBool, "--schedule-remove-self": sendFlagBool, "--anyway": sendFlagBool,
	"--attention": sendFlagOptional, "--urgent": sendFlagOptional,
	"--diff": sendFlagValue, "--image": sendFlagValue, "--images": sendFlagValue, "--file": sendFlagValue,
	"--files": sendFlagValue, "--video": sendFlagValue, "--videos": sendFlagValue, "--video-cover": sendFlagValue,
	"--video-covers": sendFlagValue, "--mention": sendFlagValue, "--expected-link": sendFlagValue, "--layout": sendFlagValue,
	"--response-kind": sendFlagValue, "--image-mode": sendFlagValue, "--chat-id": sendFlagValue, "--into": sendFlagValue,
	"--quote": sendFlagValue, "--reply-to": sendFlagValue, "--as": sendFlagValue, "--plugin-card-action": sendFlagValue,
	"--completion-proposal-file": sendFlagValue, "--session-id": sendFlagValue,
	// the body comes from a file or a card (real send, opaque to wrapper parsing)
	"--content-file": sendFlagFileValue, "--card-file": sendFlagFileValue, "--card-json": sendFlagFileValue,
	// --dry-run only renders locally, --help prints help
	"--dry-run": sendFlagGiveUp, "--help": sendFlagGiveUp,
}

// botmuxSendMedia are the flags that attach a file to a reply, one per occurrence.
var botmuxSendMedia = map[string]bool{
	"--image": true, "--images": true, "--file": true, "--files": true, "--video": true, "--videos": true,
}

// BotmuxSend reads a shell call that is `botmux send`: the reply it posted, nil when the command is anything else, its
// words cannot be told from the command alone (a file, stdin from another command, a substitution), or BotMux would
// refuse it (a flag it does not know, a flag missing its value). This reads the command line back, a compatibility
// fallback: the transcript has no structured record of what was sent.
func BotmuxSend(command string) *proto.Botmux {
	if !strings.Contains(command, "botmux") || !strings.Contains(command, "send") {
		return nil
	}
	words, heredoc, rest, ok := shellWords(strings.TrimSpace(command))
	words = skipShellAssignments(words)
	for ok && len(words) > 0 && words[0] == "cd" && rest != "" { // `cd <dir> && botmux send --files <relative path>`
		words, heredoc, rest, ok = shellWords(strings.TrimSpace(rest))
		words = skipShellAssignments(words)
	}
	if !ok || len(words) < 2 || (words[0] != "botmux" && path.Base(words[0]) != "botmux") || words[1] != "send" {
		return nil
	}
	// Another real `botmux send` anywhere in the script (not one in a quoted word, a comment or a here-document's
	// body) means this one's words are not the whole reply.
	if calls, ok := botmuxSendCalls(command); !ok || len(calls) != 1 {
		return nil
	}
	b := &proto.Botmux{Kind: proto.BotmuxReply}
	var text []string
	for i := 2; i < len(words); i++ {
		w := words[i]
		if w == "-h" || w == "--" { // help, or the separator BotMux refuses
			return nil
		}
		if !strings.HasPrefix(w, "--") { // `-5 degrees` and a lone `-` (stdin) are words too
			text = append(text, w)
			continue
		}
		name, value, eq := strings.Cut(w, "=")
		switch botmuxSendFlags[name] {
		case sendFlagBool:
			if eq {
				return nil
			}
			b.Voice = b.Voice || name == "--voice"
		case sendFlagOptional:
		case sendFlagValue:
			if !eq {
				if i+1 >= len(words) || words[i+1] == "-h" || strings.HasPrefix(words[i+1], "--") {
					return nil
				}
				i++
				value = words[i]
			}
			if value == "" {
				return nil
			}
			switch {
			case name == "--response-kind" && value == "final":
				b.Final = true
			case botmuxSendMedia[name]:
				b.Attachments++
			}
		default: // sendFlagGiveUp, or a flag BotMux does not know
			return nil
		}
	}
	stdin := len(text) == 1 && text[0] == "-" // a lone `-` reads the words from stdin
	if stdin {
		text = nil
	}
	b.Text = strings.TrimSpace(strings.Join(text, " "))
	if b.Text == "" {
		b.Text = strings.TrimSpace(heredoc)
	}
	if b.Text == "" && (stdin || b.Attachments == 0) { // nothing sent, or words from a stdin this cannot read
		return nil
	}
	return b // a reply with only attachments (`botmux send --image shot.png`) has no text
}

// shellWords splits the first simple command of s the way sh would: quotes, backslashes, $'…', output redirects and one
// here-document (its body in heredoc). What follows it (after ;, &&, ||, | or a new line, the here-document's body
// first) does not change its words. Rest retains the trailing shell text. ok is false when the words depend on running something (a substitution, a subshell,
// an input redirect), on an expansion (a glob, braces, a tilde in an argument) or a quote or here-document is not closed.
func shellWords(s string) (words []string, heredoc, rest string, ok bool) {
	var cur strings.Builder
	inWord, delim, strip, delimQuoted := false, "", false, false
	bracketStart := -1
	flush := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
		bracketStart = -1
	}
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\n' || c == ';' || c == '|' || c == '&' && !strings.HasPrefix(s[i:], "&>"):
			// the first command ends here: its here-document's body starts on the next line
			flush()
			if delim == "" {
				switch {
				case strings.HasPrefix(s[i:], "&&"):
					return words, "", s[i+2:], true
				case c == ';' || c == '\n':
					if c == '\n' && len(words) == 0 {
						i++
						continue
					}
					return words, "", s[i+1:], true
				}
				return words, "", s[i:], true
			}
			nl := strings.IndexByte(s[i:], '\n')
			if nl < 0 {
				return nil, "", "", false
			}
			body, tail, ok := heredocBody(s[i+nl+1:], delim, strip)
			if !ok {
				return nil, "", "", false
			}
			if !delimQuoted && hasUnquotedHeredocExpansion(body) {
				return nil, "", "", false
			}
			// the rest of the command's own line, then what follows the delimiter line on a line of its own
			rest = s[i : i+nl]
			if tail != "" {
				rest += "\n" + tail
			}
			return words, body, rest, true
		case c == ' ' || c == '\t':
			flush()
			i++
		case c == '#' && !inWord:
			nl := strings.IndexByte(s[i:], '\n')
			if nl < 0 {
				i = len(s)
			} else {
				i += nl
			}
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, "", "", false
			}
			cur.WriteString(s[i+1 : i+1+end])
			inWord = true
			i += end + 2
		case c == '$' && i+1 < len(s) && s[i+1] == '\'':
			j := i + 2
			for ; j < len(s) && s[j] != '\''; j++ {
				if s[j] == '\\' && j+1 < len(s) {
					j++
					decoded, ok := ansiEscape(s[j])
					if !ok {
						return nil, "", "", false
					}
					cur.WriteByte(decoded)
					continue
				}
				cur.WriteByte(s[j])
			}
			if j >= len(s) {
				return nil, "", "", false
			}
			inWord = true
			i = j + 1
		case c == '"':
			j := i + 1
			for ; j < len(s) && s[j] != '"'; j++ {
				switch {
				case s[j] == '\\' && j+1 < len(s) && strings.IndexByte("\"\\$`\n", s[j+1]) >= 0:
					j++
					if s[j] != '\n' {
						cur.WriteByte(s[j])
					}
				case s[j] == '$' || s[j] == '`':
					return nil, "", "", false // a substitution: the words depend on running it
				default:
					cur.WriteByte(s[j])
				}
			}
			if j >= len(s) {
				return nil, "", "", false
			}
			inWord = true
			i = j + 1
		case c == '\\' && i+1 < len(s):
			if s[i+1] != '\n' {
				cur.WriteByte(s[i+1])
				inWord = true
			}
			i += 2
		case c == '*' || c == '?' || c == ']' && bracketStart >= 0 && cur.Len() > bracketStart:
			return nil, "", "", false
		case c == '{' && braceExpansion(s[i:]):
			return nil, "", "", false // {a,b} and {1..3} become several words
		case c == '~' && len(words) > 0 && tildePrefix(cur.String(), inWord):
			// the home directory in an argument; in the command's name it only finds the program (~/.botmux/bin/botmux)
			return nil, "", "", false
		case c == '<' && strings.HasPrefix(s[i:], "<<") && delim == "":
			flush()
			i += 2
			if i < len(s) && s[i] == '-' {
				strip = true
				i++
			}
			for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
				i++
			}
			j := i
			for j < len(s) && s[j] != ' ' && s[j] != '\t' && s[j] != '\n' {
				j++
			}
			var delimOK bool
			delim, delimQuoted, delimOK = parseHeredocDelim(s[i:j])
			if !delimOK {
				return nil, "", "", false
			}
			i = j
		case c == '>':
			// an output redirect (2>&1, >/dev/null) changes where the command's output goes, not its words
			if inWord && (cur.String() == "1" || cur.String() == "2") {
				cur.Reset()
				inWord = false
			}
			flush()
			i++
			if i < len(s) && s[i] == '>' {
				i++
			}
			if i < len(s) && s[i] == '&' {
				i++
			}
			for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
				i++
			}
			j := i
			for j < len(s) && strings.IndexByte(" \t\n|;&<>()`$'\"", s[j]) < 0 {
				j++
			}
			if j == i {
				return nil, "", "", false
			}
			i = j
		case strings.IndexByte("<>()`$&", c) >= 0:
			if c == '$' && (i+1 >= len(s) || !shellParameterStart(s[i+1])) {
				cur.WriteByte(c)
				inWord = true
				i++
				continue
			}
			return nil, "", "", false
		default:
			cur.WriteByte(c)
			if c == '[' && bracketStart < 0 {
				bracketStart = cur.Len()
			}
			inWord = true
			i++
		}
	}
	flush()
	if delim != "" {
		return nil, "", "", false // a here-document with no body
	}
	return words, "", "", true
}

// braceExpansion reports whether the unquoted { that s starts with opens a brace expansion: a , or .. of its own
// before its unquoted } in the same word. {}, {a} and quoted or escaped braces stay literal.
func braceExpansion(s string) bool {
	depth, sep := 0, false
	for j := 0; j < len(s); j++ {
		switch c := s[j]; {
		case c == '\\':
			j++
		case c == '\'':
			end := strings.IndexByte(s[j+1:], '\'')
			if end < 0 {
				return false
			}
			j += end + 1
		case c == '"':
			k := j + 1
			for ; k < len(s) && s[k] != '"'; k++ {
				if s[k] == '\\' {
					k++
				}
			}
			if k >= len(s) {
				return false
			}
			j = k
		case strings.IndexByte(" \t\n;|&<>()", c) >= 0:
			return false // the word ends first
		case c == '{':
			depth++
		case c == '}':
			if depth--; depth == 0 {
				return sep
			}
		case depth == 1 && (c == ',' || c == '.' && j+1 < len(s) && s[j+1] == '.'):
			sep = true
		}
	}
	return false
}

// tildePrefix reports whether an unquoted ~ after word (inWord: something, maybe quoted, came before it) starts a
// tilde prefix sh expands: at the start of a word, or right after the first = or a later : of an assignment (a=~/x,
// PATH=a:~/b); --file=~/x and a:~ stay literal.
func tildePrefix(word string, inWord bool) bool {
	if !inWord {
		return true
	}
	eq := strings.IndexByte(word, '=')
	if eq <= 0 || !shellName(word[:eq]) {
		return false
	}
	return eq == len(word)-1 || word[len(word)-1] == ':'
}

func shellName(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isNameByte(s[i]) || i == 0 && s[i] >= '0' && s[i] <= '9' {
			return false
		}
	}
	return s != ""
}

// heredocBody is a here-document's lines up to its delimiter; ok is false when the delimiter never comes (a clipped
// command).
func heredocBody(s, delim string, strip bool) (string, string, bool) {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strip {
			l = strings.TrimLeft(l, "\t")
			lines[i] = l
		}
		if l == delim {
			return strings.Join(lines[:i], "\n"), strings.Join(lines[i+1:], "\n"), true
		}
	}
	return "", "", false
}

func isNameByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func shellParameterStart(next byte) bool {
	return isNameByte(next) || strings.IndexByte("({?*@#-$!", next) >= 0
}

func ansiEscape(c byte) (byte, bool) {
	switch c {
	case 'a':
		return '\a', true
	case 'b':
		return '\b', true
	case 'e', 'E':
		return 0x1b, true
	case 'f':
		return '\f', true
	case 'n':
		return '\n', true
	case 't':
		return '\t', true
	case 'r':
		return '\r', true
	case 'v':
		return '\v', true
	case '\\', '\'', '"', '?':
		return c, true
	}
	return 0, false
}

// botmuxCommand is a Bash call's command line, for BotmuxSend (Codex's commands are Bash calls here too, codex.go).
func botmuxCommand(name string, input json.RawMessage) string {
	if name != "Bash" {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	return in.Command
}

// parseHeredocDelim parses a POSIX shell here-document delimiter word, reporting whether any character
// was quoted (single quote, double quote, or backslash), which suppresses shell expansion in the body.
func parseHeredocDelim(raw string) (delim string, quoted, ok bool) {
	var b strings.Builder
	for i := 0; i < len(raw); {
		switch raw[i] {
		case '\'':
			end := strings.IndexByte(raw[i+1:], '\'')
			if end < 0 {
				return "", false, false
			}
			b.WriteString(raw[i+1 : i+1+end])
			quoted = true
			i += end + 2
		case '"':
			end := strings.IndexByte(raw[i+1:], '"')
			if end < 0 {
				return "", false, false
			}
			b.WriteString(raw[i+1 : i+1+end])
			quoted = true
			i += end + 2
		case '\\':
			if i+1 >= len(raw) {
				return "", false, false
			}
			b.WriteByte(raw[i+1])
			quoted = true
			i += 2
		default:
			b.WriteByte(raw[i])
			i++
		}
	}
	if b.Len() == 0 {
		return "", false, false
	}
	return b.String(), quoted, true
}

// hasUnquotedHeredocExpansion reports whether an unquoted heredoc body contains shell expansions
// ($var, ${...}, $(...), $((...)), backticks, or backslash escapes that the shell evaluates before sending.
func hasUnquotedHeredocExpansion(body string) bool {
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '`':
			return true
		case '\\':
			if i+1 < len(body) && strings.IndexByte("$`\\\n", body[i+1]) >= 0 {
				return true
			}
		case '$':
			if i+1 < len(body) && shellParameterStart(body[i+1]) {
				return true
			}
		}
	}
	return false
}

// botmuxResultReportsFailure reports whether a tool_result's text carries a failed BotMux JSON receipt.
func botmuxResultReportsFailure(text string) bool {
	s := strings.TrimSpace(text)
	if !strings.Contains(s, `"ok"`) && !strings.Contains(s, `"success"`) {
		return false
	}
	check := func(raw string) bool {
		raw = strings.TrimSpace(raw)
		if !strings.HasPrefix(raw, "{") {
			return false
		}
		var body struct {
			OK      *bool `json:"ok"`
			Success *bool `json:"success"`
		}
		return json.Unmarshal([]byte(raw), &body) == nil &&
			(body.OK != nil && !*body.OK || body.Success != nil && !*body.Success)
	}
	if check(s) {
		return true
	}
	for _, line := range strings.Split(s, "\n") {
		if check(line) {
			return true
		}
	}
	return false
}

// botmuxToolResultSucceeded reports whether res represents a completed, successful `botmux send` tool execution.
func botmuxToolResultSucceeded(res *proto.Item) bool {
	if res == nil || res.IsError {
		return false
	}
	if outcome := res.Outcome; outcome != nil {
		if (outcome.Status != "" && outcome.Status != proto.OutcomeOK) ||
			(outcome.ExitCode != nil && *outcome.ExitCode != 0) {
			return false
		}
	}
	return !botmuxResultReportsFailure(res.Text)
}

// skipShellAssignments drops the NAME=value words before a command's name (`LANG=C botmux send hi`): they set its
// environment, not its words.
func skipShellAssignments(words []string) []string {
	for len(words) > 0 {
		name, _, found := strings.Cut(words[0], "=")
		if !found || !shellName(name) {
			break
		}
		words = words[1:]
	}
	return words
}

// botmuxOpaqueWord stands for the part of a word only running the shell could tell (an expansion, a substitution).
const botmuxOpaqueWord = "\x00"

// botmuxSendCalls is every `botmux send` invocation in command, each as its words: the simple commands anywhere in the
// script (and-or chains, pipelines, blocks, if and loop bodies, substitutions) whose program is botmux and whose first
// argument is send. Quoted words, comments and here-document bodies are never commands; redirects and NAME=value
// prefixes are not words. BotmuxSend and countBotmuxSendInvocations both read commands through it, so a command the
// structured overlay counts is one the decoder sees the same way. ok is false when command does not parse.
func botmuxSendCalls(command string) (calls [][]string, ok bool) {
	script, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, false
	}
	syntax.Walk(script, func(node syntax.Node) bool {
		call, isCall := node.(*syntax.CallExpr)
		if !isCall || len(call.Args) < 2 {
			return true
		}
		words := make([]string, len(call.Args))
		for i, w := range call.Args {
			words[i] = botmuxWordText(w)
		}
		if path.Base(words[0]) == "botmux" && words[1] == "send" {
			calls = append(calls, words)
		}
		return true
	})
	return calls, true
}

// botmuxWordText is a parsed word with its quotes removed; parts the shell computes read as botmuxOpaqueWord.
func botmuxWordText(w *syntax.Word) string {
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(shellUnescape(p.Value, ""))
		case *syntax.SglQuoted:
			if p.Dollar {
				b.WriteString(botmuxOpaqueWord)
			} else {
				b.WriteString(p.Value)
			}
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				if lit, isLit := q.(*syntax.Lit); isLit {
					b.WriteString(shellUnescape(lit.Value, "\"\\$`\n"))
				} else {
					b.WriteString(botmuxOpaqueWord)
				}
			}
		default:
			b.WriteString(botmuxOpaqueWord)
		}
	}
	return b.String()
}

// shellUnescape removes the backslashes sh removes: before any character outside quotes (only "" for escapable), or
// before one of escapable inside double quotes. A backslash-newline joins lines.
func shellUnescape(s, escapable string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (escapable == "" || strings.IndexByte(escapable, s[i+1]) >= 0) {
			i++
			if s[i] != '\n' {
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func botmuxSendStatusPreserved(command string) bool {
	script, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	return err == nil && len(script.Stmts) == 1 && botmuxStatusPreservingStmt(script.Stmts[0])
}

func botmuxStatusPreservingStmt(stmt *syntax.Stmt) bool {
	if stmt.Background || stmt.Negated || stmt.Coprocess {
		return false
	}
	switch cmd := stmt.Cmd.(type) {
	case *syntax.CallExpr:
		return true
	case *syntax.BinaryCmd:
		return cmd.Op == syntax.AndStmt && botmuxStatusPreservingStmt(cmd.X) && botmuxStatusPreservingStmt(cmd.Y)
	default:
		return false
	}
}

// DecorateBotmux populates Item.Botmux from BotMux wrappers (user lines) and completed, successful
// `botmux send` tool calls. Only call this for known BotMux sessions; ordinary sessions must not be decorated.
// Running or errored `botmux send` calls are left undecorated so they are not labeled as delivered to Lark.
func DecorateBotmux(items []proto.Item) []proto.Item {
	if len(items) == 0 {
		return items
	}
	results := map[string]*proto.Item{}
	for i := range items {
		if items[i].Kind == proto.KindToolResult && items[i].ToolUseID != "" {
			results[items[i].ToolUseID] = &items[i]
		}
	}
	var out []proto.Item
	clone := func() []proto.Item {
		if out == nil {
			out = append([]proto.Item(nil), items...)
		}
		return out
	}
	for i := range items {
		it := &items[i]
		var bm *proto.Botmux
		switch {
		case it.Role == "user" && it.Kind == proto.KindText && it.From == "":
			bm = BotmuxMessage(it.Text)
		case it.Kind == proto.KindToolUse:
			if cmd := botmuxCommand(it.ToolName, it.ToolInput); cmd != "" {
				if botmuxToolResultSucceeded(results[it.ToolUseID]) {
					bm = BotmuxSend(cmd)
					if bm != nil && !botmuxSendStatusPreserved(cmd) {
						bm = nil
					}
				}
			}
		default:
			continue
		}
		if bm != nil || it.Botmux != nil {
			dst := clone()
			dst[i].Botmux = bm
		}
	}
	if out == nil {
		return items
	}
	return out
}

// markBotmux is an in-place convenience wrapper around DecorateBotmux for tests.
func markBotmux(items []proto.Item) {
	copy(items, DecorateBotmux(items))
}
