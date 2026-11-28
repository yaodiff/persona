package node

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
	"github.com/nwverse/persona/server/internal/transcript"
)

// The handoff note itself (engine_handoff.go reads the items): a pure function of the conversation's items, so the
// same transcripts always give the same note. Ported from cindy's agentHandoff.ts (THIRD_PARTY_NOTICES.md) with its
// structure and bounds: a work ledger read off the tool calls, a one-line index of the earlier turns, the latest turns
// word for word, and the end marker. Its words are prompts/agents/engine_handoff.md; here is only what goes in.
//
// Lengths are counted in runes and every cut lands on a rune boundary. The note stays within handoffHardCap: the
// verbatim turns shrink from 4 to 1 first, then everything is clipped harder (handoffLevels). cindy's last resort cut
// the text itself, keeping its end marker; here the data is squeezed instead, so every section and line stays whole
// and the marker is always last.

// handoffLimits are the bounds one rendering of the note is built with.
type handoffLimits struct {
	recent      int // turns word for word
	text        int // runes of an input or a reply among them
	input       int // runes of a tool call's input
	result      int // runes of a tool's result
	edge        int // a turn's lines kept at each end when it has more than twice as many
	detail      int // runes of a turn's lines past which each is compacted to compact
	compact     int
	digest      int // runes of the earlier turns' index; the oldest lines go first
	files       int // the ledger's files, commands (and failed attempts): the latest
	commands    int
	transcripts int // transcript files named: the latest
}

const (
	handoffHardCap       = 16000 // runes: the whole note
	handoffDigestLineCap = 120   // runes of one index line
	handoffCommandCap    = 120   // runes of one ledger command
	handoffPathCap       = 200   // runes of one ledger file
	handoffHintCap       = 80    // runes of one failing test's path
	handoffNameCap       = 80    // runes of a tool's name, a call's id, a sending session's id
	handoffFilesCap      = 20
	handoffCommandsCap   = 10
	handoffHintsPerCall  = 3
	// What each index line and turn line costs beyond its text, about (the template's labels), against the budgets.
	handoffIndexOverhead = 10
	handoffLineOverhead  = 40
)

// handoffLevels: cindy's bounds with 4, 3, 2 and 1 verbatim turns, then harder clips with one. The last level fits
// whatever the transcripts hold: its every part has a fixed bound, and one transcript path (at most PATH_MAX) leaves
// room under the cap.
var handoffLevels = func() []handoffLimits {
	base := handoffLimits{text: 2000, input: 160, result: 1000, edge: 20, detail: 9000, compact: 240, digest: 3000,
		files: handoffFilesCap, commands: handoffCommandsCap, transcripts: 10}
	var out []handoffLimits
	for recent := 4; recent >= 1; recent-- {
		l := base
		l.recent = recent
		out = append(out, l)
	}
	return append(out,
		handoffLimits{recent: 1, text: 1000, input: 120, result: 500, edge: 10, detail: 4000, compact: 160, digest: 1500,
			files: handoffFilesCap, commands: handoffCommandsCap, transcripts: 10},
		handoffLimits{recent: 1, text: 500, input: 80, result: 250, edge: 5, detail: 2000, compact: 120, digest: 500,
			files: 10, commands: 5, transcripts: 5},
		handoffLimits{recent: 1, text: 250, input: 60, result: 120, edge: 3, detail: 1000, compact: 80, digest: 0,
			files: 5, commands: 3, transcripts: 1},
	)
}()

// handoffNote is what the note is built from: the conversation's items oldest first (only the stretches it covers),
// the engines ("claude" / "codex"), whether to is resumed (delta), and the transcript files to name.
type handoffNote struct {
	Items       []proto.Item
	From, To    string
	Delta       bool
	Transcripts []prompts.EngineHandoffTranscript
}

// buildHandoff is the handoff text for n.
func buildHandoff(n handoffNote) string {
	turns := splitHandoffTurns(n.Items)
	ledger := handoffLedger(n.Items)
	var text string
	for _, l := range handoffLevels {
		if text = prompts.RenderEngineHandoff(handoffData(n, turns, ledger, l)); utf8.RuneCountInString(text) <= handoffHardCap {
			break
		}
	}
	return text
}

// engineName is an engine as the model reads it.
func engineName(agent string) string {
	switch agent {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case agyAgent:
		return "Antigravity"
	}
	return agent
}

// handoffTurn is one turn of the conversation: an input (the user's, or Persona's or another session's: who, from)
// and what followed it until the next input, unclipped.
type handoffTurn struct {
	who, from string
	text      string
	images    int
	lines     []handoffLine
	reply     string // the turn's last reply, for the index
}

type handoffLine struct {
	kind     string // prompts.EngineHandoffSaid, Tool or Result
	tool, id string
	text     string
	failed   bool
}

// splitHandoffTurns splits the items into turns. A turn starts at each input: a user text (without the blocks Persona
// or the runtime put around the words, an earlier handoff's among them, so handoffs never nest) or pictures sent
// alone. Thinking, notices and the pictures a tool's result carried are left out. What comes before the first input
// is a turn without one.
func splitHandoffTurns(items []proto.Item) []handoffTurn {
	images, texts := map[int]int{}, map[int]bool{}
	for _, it := range items {
		switch {
		case it.Role == "user" && it.Kind == proto.KindImage && it.ToolUseID == "":
			images[it.Seq]++
		case it.Role == "user" && it.Kind == proto.KindText:
			texts[it.Seq] = true
		}
	}
	var turns []handoffTurn
	counted := map[int]bool{} // a line's pictures go with its first input
	start := func(t handoffTurn, seq int) {
		if !counted[seq] {
			t.images, counted[seq] = images[seq], true
		}
		turns = append(turns, t)
	}
	current := func() *handoffTurn {
		if len(turns) == 0 {
			turns = append(turns, handoffTurn{})
		}
		return &turns[len(turns)-1]
	}
	for _, it := range items {
		switch {
		case it.Role == "user" && it.Kind == proto.KindText:
			t := handoffTurn{text: transcript.StripInjected(handoffItemText(it))}
			switch {
			case it.From != "":
				t.who, t.from = prompts.EngineHandoffAgent, textfmt.Clip(it.From, handoffNameCap)
			case prompts.ParseInjected(it.Text) != nil:
				t.who = prompts.EngineHandoffPersona
			}
			start(t, it.Seq)
		case it.Role == "user" && it.Kind == proto.KindImage && it.ToolUseID == "":
			if !texts[it.Seq] && !counted[it.Seq] {
				start(handoffTurn{}, it.Seq)
			}
		case it.Role == "assistant" && it.Kind == proto.KindText:
			if text := strings.TrimSpace(handoffItemText(it)); text != "" {
				t := current()
				t.lines = append(t.lines, handoffLine{kind: prompts.EngineHandoffSaid, text: text})
				t.reply = text
			}
		case it.Role == "assistant" && it.Kind == proto.KindToolUse:
			t := current()
			t.lines = append(t.lines, handoffLine{kind: prompts.EngineHandoffTool, tool: it.ToolName, id: it.ToolUseID,
				text: handoffInput(it.ToolInput)})
		case it.Kind == proto.KindToolResult:
			t := current()
			t.lines = append(t.lines, handoffLine{kind: prompts.EngineHandoffResult, id: it.ToolUseID,
				text: handoffResultText(handoffItemText(it)), failed: it.IsError || it.Outcome != nil && it.Outcome.Status == proto.OutcomeFailed})
		}
	}
	out := turns[:0]
	for _, t := range turns {
		if strings.TrimSpace(t.text) != "" || t.images > 0 || len(t.lines) > 0 {
			out = append(out, t)
		}
	}
	return out
}

// handoffItemText is an item's text without the line a page's cut ends it in (landedItem); a cut result gets its
// output's last lines back (Outcome.Tail), where a build or a test run says what failed.
func handoffItemText(it proto.Item) string {
	if it.FullSize == 0 {
		return it.Text
	}
	t := it.Text
	if i := strings.LastIndex(t, "\n…"); i >= 0 {
		t = t[:i]
	}
	if it.Outcome != nil && it.Outcome.Tail != "" {
		t += "\n[…]\n" + it.Outcome.Tail
	}
	return t
}

// handoffInput is a call's input on one line, as written ("" for none).
func handoffInput(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}

var (
	handoffReminderRe = lazyre.New(`(?s)<system-reminder\b[^>]*>.*?</system-reminder>`)
	handoffDataURIRe  = lazyre.New(`data:image/[\w.+-]+;base64,[A-Za-z0-9+/=]+`)
)

// handoffResultText is a tool's result without the reminders Claude Code appends to it and without image data.
func handoffResultText(s string) string {
	if strings.Contains(s, "<system-reminder") {
		s = handoffReminderRe.ReplaceAllString(s, "")
	}
	if strings.Contains(s, ";base64,") {
		s = handoffDataURIRe.ReplaceAllString(s, "[image]")
	}
	return strings.TrimSpace(s)
}

// handoffWork is the work ledger, unclipped by level: the files the calls changed (first seen order) and the commands
// they ran (in order) with what each came to, and the failed attempts.
type handoffWork struct {
	files    []string
	commands []prompts.EngineHandoffCommand
	failed   []prompts.EngineHandoffCommand
}

// The tools that write files (Claude Code's, and Codex's as its own names; Persona's transcripts give a Codex file
// change as Edit with file_path) and those that run commands (a Codex command is Bash there).
var (
	handoffFileTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true,
		"apply_patch": true, "str_replace_editor": true}
	handoffCommandTools = map[string]bool{"Bash": true, "shell": true, "exec_command": true, "local_shell": true}
	// handoffPatchFileRe is a file an apply_patch names ("*** Update File: path", "*** Move to: path").
	handoffPatchFileRe = lazyre.New(`(?m)^\*\*\* (?:Add File|Update File|Delete File|Move to): (.+)$`)
	handoffExitRe      = lazyre.New(`(?i)\bexit(?:ed)?(?:\s+code)?[:\s]+(-?\d+)`)
	handoffTestsRe     = lazyre.New(`(?i)\b(tests? failed|failing tests|assertionerror)\b`)
	handoffFailLineRe  = lazyre.New(`\bFAIL\s+(\S+)`)
	handoffTestFileRe  = lazyre.New(`(\S+\.(?:test|spec)\.\w+)`)
)

// handoffLedger reads the work ledger off the calls (cindy's extractWorkState): a command's outcome from its result,
// and for a failed one the failing tests its output names, else the command itself.
func handoffLedger(items []proto.Item) handoffWork {
	var w handoffWork
	seen := map[string]bool{}
	commandOf := map[string]int{}
	for _, it := range items {
		switch {
		case it.Role == "assistant" && it.Kind == proto.KindToolUse && (handoffFileTools[it.ToolName] || handoffCommandTools[it.ToolName]):
			var input map[string]any // decoded for these calls only: most of a conversation's are reads and searches
			_ = json.Unmarshal(it.ToolInput, &input)
			switch {
			case handoffFileTools[it.ToolName]:
				var paths []string
				for _, key := range []string{"file_path", "path", "notebook_path"} {
					if p, _ := input[key].(string); p != "" {
						paths = append(paths, p)
					}
				}
				if it.ToolName == "apply_patch" { // the patch, under whatever key: in key order, the same every time
					for _, key := range slices.Sorted(maps.Keys(input)) {
						if s, ok := input[key].(string); ok {
							for _, m := range handoffPatchFileRe.FindAllStringSubmatch(s, -1) {
								paths = append(paths, strings.TrimSpace(m[1]))
							}
						}
					}
				}
				for _, p := range paths {
					if !seen[p] {
						seen[p] = true
						w.files = append(w.files, p)
					}
				}
			case handoffCommandTools[it.ToolName]:
				cmd := handoffCommand(input)
				if cmd == "" {
					continue
				}
				if it.ToolUseID != "" {
					commandOf[it.ToolUseID] = len(w.commands)
				}
				w.commands = append(w.commands, prompts.EngineHandoffCommand{Command: textfmt.Clip(textfmt.OneLine(cmd), handoffCommandCap)})
			}
		case it.Kind == proto.KindToolResult && it.ToolUseID != "":
			i, ok := commandOf[it.ToolUseID]
			if !ok {
				continue
			}
			status, exit := handoffOutcome(it)
			if status == "" {
				continue
			}
			c := &w.commands[i]
			c.Status, c.Exit = status, exit
			if !handoffFailed(status, exit) {
				continue
			}
			if hints := handoffFailHints(handoffItemText(it)); len(hints) > 0 {
				for _, h := range hints {
					w.failed = append(w.failed, prompts.EngineHandoffCommand{Hint: h})
				}
			} else {
				w.failed = append(w.failed, *c)
			}
		}
	}
	return w
}

// handoffCommand is the command line a call's input runs: a string, or an argv (Codex's shell runs
// ["bash", "-lc", "<command>"]).
func handoffCommand(input map[string]any) string {
	for _, key := range []string{"command", "cmd"} {
		switch v := input[key].(type) {
		case string:
			return v
		case []any:
			var argv []string
			for _, a := range v {
				if s, ok := a.(string); ok {
					argv = append(argv, s)
				}
			}
			if len(argv) == 3 && (argv[1] == "-lc" || argv[1] == "-c") {
				return argv[2]
			}
			return strings.Join(argv, " ")
		}
	}
	return ""
}

// handoffOutcome is what a command came to, as its result says: an exit code (status "exit"), the outcome Persona
// recorded, a failed test run, or "" when nothing says.
func handoffOutcome(it proto.Item) (status string, exit int) {
	o := it.Outcome
	switch {
	case o != nil && o.ExitCode != nil:
		return "exit", *o.ExitCode
	case o != nil && (o.Status == proto.OutcomeDenied || o.Status == proto.OutcomeBlocked || o.Status == proto.OutcomeInterrupted ||
		o.Status == proto.OutcomeTimedOut || o.Status == proto.OutcomeBackground):
		return o.Status, 0
	case it.IsError || o != nil && o.Status == proto.OutcomeFailed:
		return proto.OutcomeFailed, 0
	}
	text := handoffItemText(it)
	if m := handoffExitRe.FindStringSubmatch(text); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return "exit", n
		}
	}
	if handoffTestsRe.MatchString(text) {
		return "tests_failed", 0
	}
	if o != nil && o.Status == proto.OutcomeOK {
		return proto.OutcomeOK, 0
	}
	return "", 0
}

func handoffFailed(status string, exit int) bool {
	switch status {
	case "", proto.OutcomeOK, proto.OutcomeBackground, proto.OutcomeNoMatch:
		return false
	case "exit":
		return exit != 0
	}
	return true
}

// handoffFailHints are the failing tests' paths a result names (a "FAIL path" line, a *.test.* / *.spec.* file).
func handoffFailHints(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		m := handoffFailLineRe.FindStringSubmatch(line)
		if m == nil {
			m = handoffTestFileRe.FindStringSubmatch(line)
		}
		if m == nil {
			continue
		}
		if out = append(out, textfmt.Clip(m[1], handoffHintCap)); len(out) == handoffHintsPerCall {
			break
		}
	}
	return out
}

// handoffData is the note's data within the bounds l.
func handoffData(n handoffNote, turns []handoffTurn, w handoffWork, l handoffLimits) prompts.EngineHandoff {
	h := prompts.EngineHandoff{From: engineName(n.From), To: engineName(n.To), Delta: n.Delta}
	for _, f := range handoffLatest(w.files, l.files) {
		h.Ledger.Files = append(h.Ledger.Files, handoffExcerpt(f, handoffPathCap))
	}
	h.Ledger.Commands = handoffLatest(w.commands, l.commands)
	h.Ledger.Failed = handoffLatest(w.failed, l.commands)
	h.Transcripts = handoffLatest(n.Transcripts, l.transcripts)

	recent := min(l.recent, len(turns))
	earlier := turns[:len(turns)-recent]
	h.Index, h.Omitted = handoffIndex(earlier, l.digest)
	for _, t := range turns[len(turns)-recent:] {
		h.Recent = append(h.Recent, handoffVerbatim(t, l))
	}
	return h
}

func handoffLatest[T any](s []T, n int) []T {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// handoffIndex is the earlier turns' index within budget runes: a line for each turn's input and one for its last
// reply, the newest kept; omitted says older lines were left out.
func handoffIndex(turns []handoffTurn, budget int) (lines []prompts.EngineHandoffIndex, omitted bool) {
	for _, t := range turns {
		if text := strings.TrimSpace(t.text); text != "" {
			lines = append(lines, prompts.EngineHandoffIndex{Who: t.who, From: t.from, Text: handoffExcerpt(textfmt.OneLine(text), handoffDigestLineCap)})
		}
		if t.reply != "" {
			lines = append(lines, prompts.EngineHandoffIndex{Reply: true, Text: handoffExcerpt(textfmt.OneLine(t.reply), handoffDigestLineCap)})
		}
	}
	cost := func(l prompts.EngineHandoffIndex) int { return utf8.RuneCountInString(l.Text) + handoffIndexOverhead }
	total := 0
	for _, l := range lines {
		total += cost(l)
	}
	if total <= budget {
		return lines, false
	}
	used, keep := 0, len(lines)
	for keep > 0 && used+cost(lines[keep-1]) <= budget {
		keep--
		used += cost(lines[keep])
	}
	return lines[keep:], true
}

// handoffVerbatim is one turn word for word within l: the input and each line clipped; a turn whose lines run past
// l.detail has each compacted further, and one with more than twice l.edge keeps l.edge at each end.
func handoffVerbatim(t handoffTurn, l handoffLimits) prompts.EngineHandoffTurn {
	out := prompts.EngineHandoffTurn{Who: t.who, From: t.from, Text: handoffExcerpt(strings.TrimSpace(t.text), l.text), Images: t.images}
	lines := make([]prompts.EngineHandoffLine, len(t.lines))
	total := 0
	for i, x := range t.lines {
		line := prompts.EngineHandoffLine{Kind: x.kind, Tool: textfmt.Clip(x.tool, handoffNameCap),
			ID: textfmt.Clip(x.id, handoffNameCap), Failed: x.failed}
		switch x.kind {
		case prompts.EngineHandoffSaid:
			line.Text = handoffExcerpt(x.text, l.text)
		case prompts.EngineHandoffTool:
			line.Text = handoffExcerpt(x.text, l.input)
		default:
			line.Text = handoffExcerpt(x.text, l.result)
		}
		lines[i] = line
		total += utf8.RuneCountInString(line.Text) + handoffLineOverhead
	}
	if total > l.detail {
		for i := range lines {
			lines[i].Text = handoffExcerpt(lines[i].Text, l.compact)
		}
	}
	if len(lines) > 2*l.edge {
		kept := append([]prompts.EngineHandoffLine(nil), lines[:l.edge]...)
		kept = append(kept, prompts.EngineHandoffLine{Kind: prompts.EngineHandoffSkipped, Omitted: len(lines) - 2*l.edge})
		lines = append(kept, lines[len(lines)-l.edge:]...)
	}
	out.Lines = lines
	return out
}

// handoffExcerpt is s within n runes: its start and its end around a "[…]" where the middle was left out, so both a
// request's setup and its final correction stay (cindy's excerpt).
func handoffExcerpt(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	marker := " […] "
	if strings.Contains(s, "\n") {
		marker = "\n[…]\n"
	}
	m := utf8.RuneCountInString(marker)
	if n <= m {
		return textfmt.Cut(s, n)
	}
	r := []rune(s)
	head := (n - m + 1) / 2
	tail := n - m - head
	return string(r[:head]) + marker + string(r[len(r)-tail:])
}
