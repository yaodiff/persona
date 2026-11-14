package screen

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/nwverse/persona/server/internal/dialogwire"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Defined in the contract package dialogwire (docs/server/architecture.md "Contract packages"); the old names stay.
type (
	Dialog = dialogwire.Dialog
	Option = dialogwire.Option
	Step   = dialogwire.Step
)

const (
	CursorSubmit      = dialogwire.CursorSubmit
	KindChat          = dialogwire.KindChat
	KindTypeSomething = dialogwire.KindTypeSomething
	RiskDelete        = dialogwire.RiskDelete
	DeadlineDeny      = dialogwire.DeadlineDeny
	DeadlineAllow     = dialogwire.DeadlineAllow
)

// DialogLines is how much of the screen a dialog is read from, the same when the card is made (hub) and when its
// answer is checked (node): the revision covers what the window shows.
const DialogLines = 40

var numberedRE = lazyre.New(`^(\s*)(?:([❯›>])\s*)?(\d)\.\s+(\S.*)$`)

// cursorRE is the selected row of an unnumbered list: `❯ No, exit`.
var cursorRE = lazyre.New(`^(\s*)❯\s+(\S.*)$`)

// boxRE is a multi-select option's check box before its label: `[ ] Phone`, `[✔] Computer`.
var boxRE = lazyre.New(`^\[([ ✔✓xX])\]\s*(.*)$`)

// previewRE is the box beside the options of a question with previews (AskUserQuestion's preview field), from the
// gap before it to the end of the row: `1. Yes         ┌──────────┐`, `2. Change      │ Line one │`.
var previewRE = lazyre.New(`\s{2,}[┌│└].*$`)

// previewNote is the hint under that box.
const previewNote = "Notes: press n to add notes"

// submitRE is the row under a multi-select question's options that sends them, maybe under the cursor.
var submitRE = lazyre.New(`^\s*(❯)?\s*(Submit|Next)$`)

// ParseDialog reads the dialog at the bottom of a plain screen, or nil when there is none.
func ParseDialog(plain string) *Dialog {
	lines := strings.Split(Plain(plain), "\n") // a colored read is the same dialog (#5538)
	for i, l := range lines {
		lines[i] = strings.TrimRight(strings.ReplaceAll(l, " ", " "), " ")
	}
	// A fresh pane taller than what claude has drawn reads as the dialog then blank rows down to the bottom (Claude
	// Code's Bypass Permissions confirmation on a start: 36 of them in a 50-row pane). The readers below look back
	// from the last row a fixed number of rows (dialogTop, the cursor's search), so those rows pushed the dialog out
	// and no card could offer its choices.
	lines = withoutBlankTail(lines)
	if d := numbered(lines); d != nil {
		return d
	}
	if d := rewindList(lines); d != nil {
		return d
	}
	return unnumbered(lines)
}

// DialogWhole says whether the dialog at the bottom of plain is shown from the rule that opens it: what a read of only
// the screen's bottom rows needs before it is taken for that dialog (#6318). A dialog taller than the read starts at
// the read's top (describe finds no rule), so its card would show part of it, a permission's command cut, and its
// revision would cover only those rows: another command with the same last rows would pass for it. The row it is read
// from is its question's first, else its first option's.
func DialogWhole(plain string) bool {
	d := ParseDialog(plain)
	if d == nil {
		return false
	}
	anchor := d.Question
	if anchor == "" && len(d.Options) > 0 {
		anchor = d.Options[0].Label
	}
	if r := []rune(strings.TrimSpace(anchor)); len(r) > 12 {
		anchor = string(r[:12])
	}
	if strings.TrimSpace(anchor) == "" {
		return false
	}
	lines := strings.Split(Plain(plain), "\n")
	for q := len(lines) - 1; q >= 0; q-- {
		if !strings.Contains(strings.ReplaceAll(lines[q], "\u00a0", " "), strings.TrimSpace(anchor)) {
			continue
		}
		for i := q - 1; i >= 0; i-- {
			if isRule(lines[i]) {
				return true
			}
		}
		return false
	}
	return false
}

// rewindList reads Claude Code's /rewind list as a dialog: one option per prompt row, the row under it ("notes.txt
// +1 -1", "No code changes") its detail (#4306). unnumbered took that row for an option of its own, so the card had
// an extra button and every button below it moved the cursor one prompt too far. Enter on a prompt only opens the
// confirmation screen, which numbered reads. The (current) row is no prompt to go back to and is left out; with the
// cursor on it nothing is read, as before.
func rewindList(lines []string) *Dialog {
	if !Choosing(strings.Join(lines, "\n")) {
		return nil
	}
	r := ParseRewind(strings.Join(lines[dialogTop(lines):], "\n")) // a panel in the conversation above picks nothing
	if r == nil || r.Kind != RewindList {
		return nil
	}
	cur := slices.IndexFunc(r.Rows, func(row RewindRow) bool { return row.Selected })
	if cur < 0 || r.Rows[cur].Current {
		return nil
	}
	// The prompts often repeat ("continue" with no code changes): scrolled one row, the window looks the same.
	d := &Dialog{Question: rewindListQuestion}
	window := fmt.Sprintf("%d/%d", r.Above, r.Below)
	for i, row := range r.Rows {
		if row.Current {
			continue
		}
		var keys []string
		for k := cur; k < i; k++ {
			keys = append(keys, "down")
		}
		for k := i; k < cur; k++ {
			keys = append(keys, "up")
		}
		key := strconv.Itoa(len(d.Options) + 1)
		if i == cur {
			d.Cursor = key
		}
		d.Options = append(d.Options, Option{Key: key, Label: row.Text, Detail: row.Changes, Keys: append(keys, "enter")})
	}
	hash(d, d.Question, window)
	return d
}

type row struct {
	i, n   int
	label  string
	indent int  // column where the label starts
	cursor bool // the ❯ is on it
}

// numbered reads the dialog's numbered options. Only rows from dialogTop down count: a numbered list in the agent's
// answer above the dialog ("I'll do three things: 1. … 2. …") is the conversation, and its numbers pick nothing.
// Nothing below dialogTop is the conversation — dialogTop stops under the last conversation row it finds, so a
// finished turn under the list ("✻ Cooked for 12s") is already outside the window read here.
func numbered(lines []string) *Dialog {
	var rows []row
	for i := dialogTop(lines); i < len(lines); i++ {
		l := lines[i]
		m := numberedRE.FindStringSubmatchIndex(l)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(l[m[6]:m[7]])
		if n < 1 {
			continue
		}
		rows = append(rows, row{i: i, n: n, label: strings.TrimSpace(l[m[8]:m[9]]), indent: len([]rune(l[:m[8]])), cursor: m[4] >= 0})
	}
	// The last run 1, 2, 3… is the live dialog.
	start := -1
	for k := len(rows) - 1; k >= 0; k-- {
		if rows[k].n == 1 {
			start = k
			break
		}
	}
	if start < 0 {
		return nil
	}
	block := []row{rows[start]}
	for _, r := range rows[start+1:] {
		if r.n == block[len(block)-1].n+1 {
			block = append(block, r)
		}
	}
	if len(block) < 2 {
		return nil
	}
	// Options with previews: the box beside them shows the focused one's preview. It is no option's label or detail
	// (it changes as the cursor moves), and a number only moves the cursor there: enter picks (2026-11-04: the
	// number alone left the card answered and the question waiting).
	preview := false
	for k, r := range block {
		if l := previewRE.ReplaceAllString(r.label, ""); l != r.label {
			block[k].label, preview = l, true
		}
	}
	d := &Dialog{}
	for k, r := range block {
		end := min(len(lines), r.i+6)
		if k+1 < len(block) {
			end = block[k+1].i
		}
		o := Option{Key: strconv.Itoa(r.n), Label: r.label, Keys: []string{strconv.Itoa(r.n)}}
		if preview {
			o.Keys = append(o.Keys, "enter")
		}
		if m := boxRE.FindStringSubmatch(r.label); m != nil {
			d.Multi = true
			o.Label, o.Checked = strings.TrimSpace(m[2]), m[1] != " "
		} else if l, ok := strings.CutSuffix(r.label, " ✔"); ok { // the pick of an answered question, shown again
			o.Label, o.Checked = strings.TrimSpace(l), true
		}
		if r.cursor {
			d.Cursor = o.Key
		}
		var detail []string
		for _, l := range lines[r.i+1 : end] {
			if m := submitRE.FindStringSubmatch(l); m != nil && d.Multi {
				d.Submit = m[2]
				if m[1] != "" {
					d.Cursor = CursorSubmit
				}
				break
			}
			lead := len(l) - len(strings.TrimLeft(l, " "))
			if preview {
				l = previewRE.ReplaceAllString(l, "")
				if t := strings.TrimSpace(l); t != "" && (strings.ContainsRune("┌│└", []rune(t)[0]) || t == previewNote) {
					continue
				}
			}
			t := strings.TrimSpace(l)
			if t == "" || isRule(t) || lead < r.indent-1 {
				if len(detail) > 0 {
					break
				}
				continue
			}
			detail = append(detail, t)
		}
		o.Detail = joinWrapped(detail)
		switch lower := strings.ToLower(o.Label); {
		case strings.HasPrefix(lower, "type something"):
			o.Kind = KindTypeSomething
		case strings.HasPrefix(lower, "chat about this"):
			o.Kind = KindChat
		}
		d.Options = append(d.Options, o)
	}
	typedOption(d)
	describe(d, lines, block[0].i)
	return d
}

// typedOption: text typed into AskUserQuestion's free-text field shows in place of its "Type something" label (and
// checks it, in a multi-select question). The field is the option above "Chat about this": it keeps its label and
// kind, the text goes in Text, so the dialog's revision does not change with what is typed.
func typedOption(d *Dialog) {
	for k, o := range d.Options {
		if o.Kind != KindChat || k == 0 {
			continue
		}
		if f := &d.Options[k-1]; f.Kind == "" {
			// As Claude labels it when empty: "Type something." under a single choice.
			label := "Type something"
			if !d.Multi {
				label += "."
			}
			f.Text, f.Label, f.Kind = f.Label, label, KindTypeSomething
		}
	}
}

// unnumbered reads a list without numbers whose cursor row is `❯ label` and whose other rows line up with it, under
// a footer of key hints (Claude's trust prompt): options are picked by arrow moves and enter. Like numbered, only rows
// from dialogTop down count (#4159): the user's own prompt above a panel without options (/help, a notice) is a
// `❯ label` too, and the row it wraps onto (or a command's `⎿` output) lines up with it as a second "option". So a list
// whose cursor sits at the left edge is taken for that prompt and not read; Claude Code 2.1.318 draws its lists' cursor
// at column 1 or 3 (trust prompt, /resume, /theme, /memory, rewind).
func unnumbered(lines []string) *Dialog {
	if !Choosing(strings.Join(lines, "\n")) {
		return nil
	}
	top := dialogTop(lines)
	cur := -1
	indent := 0
	// The cursor is near the bottom; the options above it may run higher (a long list).
	for i := len(lines) - 1; i >= max(top, len(lines)-30); i-- {
		if m := cursorRE.FindStringSubmatch(lines[i]); m != nil {
			cur, indent = i, len([]rune(m[1]))+2
			break
		}
	}
	if cur < 0 {
		return nil
	}
	sibling := func(l string) bool {
		r := []rune(l)
		return len(r) > indent && strings.TrimSpace(string(r[:indent])) == "" && r[indent] != ' ' && !footerRE.MatchString(l)
	}
	first, last := cur, cur
	for first-1 >= top && sibling(lines[first-1]) {
		first--
	}
	for last+1 < len(lines) && sibling(lines[last+1]) {
		last++
	}
	if first == last {
		return nil
	}
	d := &Dialog{}
	for i := first; i <= last; i++ {
		label := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "❯"))
		var keys []string
		for k := cur; k < i; k++ {
			keys = append(keys, "down")
		}
		for k := i; k < cur; k++ {
			keys = append(keys, "up")
		}
		d.Options = append(d.Options, Option{Key: strconv.Itoa(i - first + 1), Label: label, Keys: append(keys, "enter")})
	}
	describe(d, lines, first)
	return d
}

// tabsRE is AskUserQuestion's tab bar: "←  ☒ Env  ☐ Platform  ✔ Submit  →" (a multi-select or multi-question dialog).
var tabsRE = lazyre.New(`^←\s+(.*?)\s+→$`)

// gapRE parts the tab bar's tabs (two spaces or more; a header may have one).
var gapRE = lazyre.New(`\s{2,}`)

// The review tab's lines (Claude Code 2.1.316).
const (
	reviewTitle      = "Review your answers"
	reviewIncomplete = "You have not answered all questions"
)

// steps reads the tab bar: each question's header and whether it is answered (☒), the Submit tab left out.
func steps(bar string) []Step {
	var out []Step
	for _, f := range gapRE.Split(bar, -1) {
		r := []rune(strings.TrimSpace(f))
		if len(r) < 2 || !strings.ContainsRune("☐☒✔", r[0]) {
			continue
		}
		label := strings.TrimSpace(string(r[1:]))
		if r[0] == '✔' && label == "Submit" {
			continue
		}
		out = append(out, Step{Label: label, Done: r[0] == '☒'})
	}
	return out
}

// describe fills header, question and detail from the lines above the options, up to the rule that opens the dialog.
func describe(d *Dialog, lines []string, first int) {
	top := 0
	for i := first - 1; i >= 0; i-- {
		if isRule(lines[i]) {
			top = i + 1
			break
		}
	}
	var quoted, plain []string
	var box command // the rows between the dashed rules around a command (╌╌╌)
	above := -1     // how many plain rows were above that box (-1: no box)
	rows := lines[top:first]
	for i := 0; i < len(rows); i++ {
		l := rows[i]
		t := strings.TrimSpace(l)
		if dashedRE.MatchString(t) {
			if !box.open && above < 0 {
				above = len(plain)
			}
			box.open = !box.open
			box.width = max(box.width, displayWidth(l))
			continue
		}
		if box.open {
			// The command, whatever its rows start with: 2.1.322 draws a │ before each, as before the warning under it,
			// which left the command out of the card and out of Rev. Its rows are what is asked, as in 2.1.316.
			row := strings.TrimSpace(strings.TrimPrefix(t, "│"))
			box.rows = append(box.rows, row)
			if row != "" {
				plain = append(plain, row)
			}
			continue
		}
		if text, n := deadlineRows(rows[i:]); n > 0 {
			// The countdown, wrapped onto two or three rows on a narrow pane: read as a question row, its seconds
			// moved Rev every second, and every answer was refused (#5265).
			d.Deadline, d.DeadlineAction, d.DeadlineIn = text, deadlineAction(text), deadlineSeconds(text)
			i += n - 1
			continue
		}
		m := tabsRE.FindStringSubmatch(t)
		switch {
		case t == "":
		case m != nil:
			// One question: its tab is the header. Several: which one is shown is only told by color, so no header;
			// the steps say which are answered.
			if st := steps(m[1]); len(st) == 1 {
				d.Header = st[0].Label
			} else {
				d.Steps = st
			}
		case t == reviewTitle:
			d.Review = true
		case strings.HasSuffix(t, reviewIncomplete):
			d.Incomplete = true
		case strings.ContainsRune("☐□☒✔◻■", []rune(t)[0]):
			d.Header = strings.TrimSpace(string([]rune(t)[1:]))
		case strings.HasPrefix(t, "│") || strings.HasPrefix(t, "|"):
			quoted = append(quoted, strings.TrimSpace(strings.TrimLeft(t, "│|")))
		default:
			plain = append(plain, t)
		}
	}
	asks := len(plain) > 0 && strings.ContainsAny(plain[len(plain)-1], "?？")
	// What is asked, in the order it is on screen, for Rev: which wrapped row holds the question mark moves with the
	// pane's width, so Question and Detail are cut differently at another width (#5538).
	asked := strings.Join(append(slices.Clone(quoted), plain...), "\n")
	switch {
	case len(quoted) > 0 && asks:
		// A boxed note above a question that is asked in plain words (a permission prompt's warning): the note is
		// the warning, the question is the question.
		d.Warning = joinWrapped(quoted)
		asked = strings.Join(plain, "\n") // the warning is not in Rev
		d.Question = plain[len(plain)-1]
		d.Detail = strings.Join(plain[:len(plain)-1], "\n")
	case len(quoted) > 0:
		d.Question = joinWrapped(quoted)
		d.Detail = strings.Join(plain, "\n")
	case len(plain) > 0:
		// The last line that asks something (a trust prompt ends in a "Security guide" link), else the last line.
		q := len(plain) - 1
		for i := len(plain) - 1; i >= 0; i-- {
			if strings.ContainsAny(plain[i], "?？") {
				q = i
				break
			}
		}
		d.Question = plain[q]
		d.Detail = strings.Join(append(slices.Clone(plain[:q]), plain[q+1:]...), "\n")
	default:
		d.Question, d.Header = d.Header, ""
		asked = d.Question
	}
	// A shell command's permission prompt: "Bash command", the agent's words for it, the command in its box.
	if above > 0 && commandPromptRE.MatchString(plain[0]) {
		d.Command = box.text()
		d.Description = joinWrapped(plain[1:above])
	}
	if strings.HasPrefix(d.Warning, "Dangerous rm operation") {
		d.Risk = RiskDelete
	}
	hash(d, asked, "")
}

// commandPromptRE is the first row of a permission prompt for a shell command.
var commandPromptRE = lazyre.New(`^(Bash|PowerShell) command\b`)

// command is the box a permission prompt draws a command in: its rows, and the width of the rules around it (the
// pane's).
type command struct {
	rows  []string
	width int
	open  bool
}

// text is the command with the rows the terminal wrapped joined back: a row was wrapped when the next row's first
// word would not have fitted after it (Claude Code 2.1.322 fills a box row up to the rule's width less 2, its " │ "
// included, and breaks between words), otherwise the command has a line break there. A word too long for a row is
// broken inside it, and gets a space where it was broken. Only for showing it: Rev leaves out all white space.
func (c command) text() string {
	var b strings.Builder
	for i, r := range c.rows {
		if i > 0 {
			prev := c.rows[i-1]
			next, _, _ := strings.Cut(r, " ")
			if prev != "" && r != "" && displayWidth(" │ "+prev)+1+displayWidth(next) > c.width-2 {
				b.WriteByte(' ')
			} else {
				b.WriteByte('\n')
			}
		}
		b.WriteString(r)
	}
	return strings.Trim(b.String(), "\n")
}

// displayWidth is how many columns s takes in a terminal: two for wide (CJK) characters.
func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		if cjk(r) {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// hash sets Rev from what the dialog asks (asked: its question and detail rows, in screen order) and offers, and
// window: where a scrolled list's rows sit in it (the /rewind list's "↑ 1 more"), so the same rows one step further
// down are another dialog. window is shown nowhere. Rev leaves out all white space: the same dialog redrawn at another
// width (the pane resized) wraps its question and its options' details at other places, and an option's long label
// onto its detail row. Before #5538 that was another revision, and an answer to a card made before the resize was
// refused.
func hash(d *Dialog, asked, window string) {
	h := sha256.New()
	h.Write([]byte(squash(d.Header) + "\x00" + squash(asked)))
	if window != "" {
		h.Write([]byte("\x00" + window))
	}
	for _, o := range d.Options {
		label := o.Label
		if o.Kind == KindTypeSomething { // its label is what is typed into it until it is read back as Text
			label = o.Kind
		}
		h.Write([]byte("\x00" + o.Key + "\x00" + squash(label+o.Detail)))
	}
	d.Rev = hex.EncodeToString(h.Sum(nil))[:12]
	legacyHash(d, window)
}

// legacyHash sets LegacyRev: Rev as hubs and nodes before #5538 compute it, wrapped rows and all.
func legacyHash(d *Dialog, window string) {
	h := sha256.New()
	h.Write([]byte(d.Header + "\x00" + d.Question + "\x00" + d.Detail))
	if window != "" {
		h.Write([]byte("\x00" + window))
	}
	for _, o := range d.Options {
		label := o.Label
		if o.Kind == KindTypeSomething { // its label is what is typed into it until it is read back as Text
			label = o.Kind
		}
		h.Write([]byte("\x00" + o.Key + "\x00" + label + "\x00" + o.Detail))
	}
	d.LegacyRev = hex.EncodeToString(h.Sum(nil))[:12]
}

// The countdown Claude Code adds when it will answer for the user (auto mode: "⚠ Claude Code will automatically
// block this request in 1:59, to avoid holding up an unattended session"; earlier builds say deny), and the dashed
// rules it draws around the command asked about.
var (
	deadlineRE = lazyre.New(`(?i)^⚠?\s*.*\bwill automatically (block|deny|allow|approve|accept|reject)\b.*\bin (\d+):(\d{2})\b`)
	// deadlineEndRE ends its sentence.
	deadlineEndRE = lazyre.New(`(?i)(\bunattended session|\.)$`)
	dashedRE      = lazyre.New(`^[╌┄┈╍┅┉-]{8,}$`)
)

// deadlineRows reads the countdown from the first of rows, the rows of it a narrow pane wrapped it onto with it: its
// text, and how many rows it takes (0: rows[0] does not start one). It ends where its sentence does (a wider pane may
// leave only "session" on its last row, which is still the countdown), else at a blank row, a box or the question.
func deadlineRows(rows []string) (string, int) {
	first := strings.TrimSpace(rows[0])
	if !strings.HasPrefix(first, "⚠") && !strings.Contains(strings.ToLower(first), "will automatically") {
		return "", 0
	}
	var parts []string
	for n, l := range rows[:min(len(rows), 4)] {
		t := strings.TrimSpace(l)
		if n > 0 && (t == "" || isRule(t) || dashedRE.MatchString(t) || strings.HasPrefix(t, "⚠") ||
			strings.HasPrefix(t, "│") || strings.HasPrefix(t, "|") || strings.ContainsAny(t, "?？")) {
			break
		}
		parts = append(parts, t)
		if j := joinWrapped(parts); deadlineRE.MatchString(j) && deadlineEndRE.MatchString(j) {
			return strings.TrimSpace(strings.TrimPrefix(j, "⚠")), len(parts)
		}
	}
	if j := joinWrapped(parts); deadlineRE.MatchString(j) {
		return strings.TrimSpace(strings.TrimPrefix(j, "⚠")), len(parts)
	}
	return "", 0
}

// deadlineAction is what the countdown in text does when it runs out (DeadlineDeny, DeadlineAllow).
func deadlineAction(text string) string {
	m := deadlineRE.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	switch strings.ToLower(m[1]) {
	case "allow", "approve", "accept":
		return DeadlineAllow
	}
	return DeadlineDeny
}

// deadlineSeconds is how long the countdown in text has left.
func deadlineSeconds(text string) int {
	m := deadlineRE.FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	min, _ := strconv.Atoi(m[2])
	sec, _ := strconv.Atoi(m[3])
	return min*60 + sec
}

// joinWrapped puts rows the terminal wrapped back into one text: no space where a row ends inside CJK text.
func joinWrapped(rows []string) string {
	var b strings.Builder
	var last rune
	for _, r := range rows {
		if r == "" {
			continue
		}
		next := []rune(r)[0]
		if b.Len() > 0 && !(cjk(last) || cjk(next)) {
			b.WriteByte(' ')
		}
		b.WriteString(r)
		rs := []rune(r)
		last = rs[len(rs)-1]
	}
	return b.String()
}

func cjk(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) ||
		(r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef)
}
