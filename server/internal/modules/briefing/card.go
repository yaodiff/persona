package briefing

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/deeplink"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/modapi"
	"github.com/nwverse/persona/server/internal/textfmt"
)

const (
	fallbackMax = 1500 // runes in the phone notification when all required rows fit

	// briefAnswerRunes is how much of the model's answer (its summary, the calendar and the chats) the card quotes at the top.
	briefAnswerRunes = 1500
	// briefTailBytes is kept free for the "and N more" lines and the note under the sections.
	briefTailBytes = 1500
	// briefSpareEls is the elements kept free for the same: an "and N more" line for each of the five sections, the note under
	// them and the button that opens the conversation.
	briefSpareEls = 7
)

// briefBlock is one row of a section: its Markdown, and the button under it (nil: none).
type briefBlock struct {
	md    string
	plain string
	btn   botcard.Button
	todo  string // the task a button accepts
}

// RenderCards renders one logical card, then paginates it after adding the conversation button.
func (r *runner) RenderCards(f *modapi.BriefSnapshot, label, answer, header, openLabel, openURL string) ([]modapi.BriefCardPage, string, []string) {
	card, plain, shown := r.briefCardWithHeader(f, label, answer, header)
	if openURL != "" {
		card.Buttons(botcard.PageBtn(openLabel, openURL))
	}
	title := label
	if header != "" {
		title = header
	}
	return splitBriefAnswerCards(card, plain, title), plain, shown
}

// briefCardWithHeader is briefCard with a fixed first line in place of the usual routine title. The model-failure
// fallback uses it: both the Feishu title and the plain-text body's first line say the model was unavailable.
func (runner *runner) briefCardWithHeader(f *modapi.BriefSnapshot, label, answer, header string) (card *botcard.Card, fallback string, shown []string) {
	l := i18n.Lang(f.Lang)
	color := botcard.Blue
	if len(f.Waiting) > 0 {
		color = botcard.Orange
	}
	title, subtitle := label, runner.CountsIn(f, f.Lang)
	plain := []string{label}
	if header != "" {
		title, subtitle = header, label
		plain = []string{header, label}
	}
	c := botcard.NewCard(title, color, subtitle)
	if answer = strings.TrimSpace(answer); answer != "" {
		// Charts become text first, as the bot's answers do (the lark module's answer.go): textfmt.ClipMarkdown drops a persona-card block
		// it cannot draw down to its title, so a chart on a long answer would lose its numbers.
		c.Markdown(botcard.CardMarkdown(textfmt.ClipMarkdown(botcard.PersonaCards(answer), briefAnswerRunes)))
		plain = append(plain, textfmt.Clip(answer, briefAnswerRunes))
		if len([]rune(answer)) > briefAnswerRunes {
			c.Note(i18n.T(l, "assistant-briefing.answer_clipped"))
		}
	}
	room := briefBudget{bytes: botcard.MaxBytes - briefTailBytes - len(c.JSON()), els: botcard.MaxElements - briefSpareEls - len(c.Body.Elements)}

	// The north star first: the report opens with it (north_star.go).
	var star []briefBlock
	for _, line := range f.NorthStarLines {
		star = append(star, briefBlock{md: botcard.EscapeMarkdown(line), plain: line})
	}
	if len(star) > 0 {
		room.section(c, &plain, l, i18n.T(l, "assistant-north_star.heading"), star, len(star))
	}

	var confirmations []briefBlock
	confSummary := runner.confirmationSummary(f.Lang, f.Confirmations)
	confirmations = append(confirmations, briefBlock{md: botcard.EscapeMarkdown(confSummary), plain: confSummary})
	for _, line := range f.ConfirmationLines {
		confirmations = append(confirmations, briefBlock{md: botcard.EscapeMarkdown(line), plain: line})
	}
	room.section(c, &plain, l, i18n.T(l, "assistant-confirmations.heading"), confirmations, len(confirmations))

	// Waiting for you: the board's rows that hold an Agent up.
	var waiting []briefBlock
	for i, r := range f.Waiting {
		waiting = append(waiting, briefBlock{md: runner.prompts.BoardRowMarkdown(f.Lang, i+1, r), plain: runner.prompts.BoardRowPlain(f.Lang, i+1, r)})
	}
	if f.WaitingUnknown { // the cards waiting were not read yet: it does not say that none waits
		line := botcard.EscapeMarkdown(i18n.T(l, "assistant-briefing.waiting_unknown"))
		c.Markdown(line)
		plain = append(plain, line)
	} else {
		room.section(c, &plain, l, i18n.T(l, "assistant-briefing.head_waiting", i18n.Args{"n": len(f.Waiting)}), waiting, len(f.Waiting))
	}

	// Direction waits and disabled routines are required facts, including in the model-failure card. They do not use
	// briefBudget.section: its ten-row rule would silently cut owner waits. The collector has already folded only the
	// non-owner tail at BriefingWaits.MaxRows; all owner rows are written here. Later optional sections yield room.
	briefFixedDirections(c, &plain, &room, f)
	briefFixedDisabled(c, &plain, &room, f)
	// Everything through the fixed lists is required. If those lists alone exceed the old 1500-rune phone fallback,
	// deliver them in full and omit only the optional sections below; clipping the combined string would cut owner rows.
	requiredPlain := len(plain)

	// The last twelve hours: what did not go through first, so a long list of what finished does not push it out.
	var past []briefBlock
	for _, x := range f.Failed {
		past = append(past, briefBlock{md: briefFailedLine(f, x, true), plain: briefFailedLine(f, x, false)})
	}
	for _, d := range f.Done {
		line := func(esc func(string) string) string {
			return i18n.T(l, "assistant-briefing.done_line", i18n.Args{"title": esc(textfmt.Clip(oneLine(d.Title), 60)), "time": briefClock(f, d.At)})
		}
		past = append(past, briefBlock{md: line(botcard.EscapeMarkdown) + briefLink(l, deeplink.Web(f.Web, deeplink.Todo(d.ID))), plain: line(func(s string) string { return s })})
	}
	pastHead := i18n.T(l, "assistant-briefing.head_past", i18n.Args{"hours": int(Window / time.Hour), "done": f.DoneTotal, "failed": f.FailedTotal})
	room.section(c, &plain, l, pastHead, past, f.DoneTotal+f.FailedTotal)

	// To verify, each row with its Accept button.
	var accept []briefBlock
	for i, r := range f.Accept {
		md := runner.prompts.BoardRowMarkdown(f.Lang, i+1, r)
		btnLabel := i18n.T(l, "assistant-reports.accept_pass")
		a, asked := f.Asks[r.ID]
		if asked && !a.Conclusion { // a conclusion's row says its verdict already (boardRows); verification work says it here (#1226)
			md += "\n" + botcard.EscapeMarkdown(a.Verdict)
		}
		if ch := f.Changed[r.ID]; ch != "" {
			md += "\n" + botcard.EscapeMarkdown(textfmt.Clip(oneLine(ch), 80))
		}
		if rec := a.RecommendText; asked && rec != "" {
			md += "\n" + botcard.EscapeMarkdown(rec)
			btnLabel = i18n.T(l, "assistant-reports.verdict_take")
		} else if asked && a.Decide {
			// Nothing recommended: there is nothing for a button to take, the owner answers its card.
			accept = append(accept, briefBlock{md: md, plain: runner.prompts.BoardRowPlain(f.Lang, i+1, r)})
			continue
		} else if asked && a.Conclusion {
			btnLabel = i18n.T(l, "assistant-reports.verdict_ack")
		}
		btn := botcard.Btn(btnLabel, botcard.Primary,
			map[string]any{"a": "accept", "id": r.ID, "asked_at": r.Asked.UTC().Format(time.RFC3339Nano)})
		accept = append(accept, briefBlock{md: md, plain: runner.prompts.BoardRowPlain(f.Lang, i+1, r), btn: btn, todo: r.ID})
	}
	shown = room.section(c, &plain, l, i18n.T(l, "assistant-briefing.head_accept", i18n.Args{"n": len(f.Accept)}), accept, len(f.Accept))

	// Can wait: the questions Agents left for a briefing, each with a "Handled" button (left out when there are none).
	if f.LaterTotal > 0 {
		var later []briefBlock
		for i, r := range f.Later {
			btn := botcard.Btn(i18n.T(l, "assistant-briefing.later_done"), botcard.Default, map[string]any{"a": "later_done", "id": r.ID})
			later = append(later, briefBlock{md: runner.prompts.BoardRowMarkdown(f.Lang, i+1, r), plain: runner.prompts.BoardRowPlain(f.Lang, i+1, r), btn: btn})
		}
		room.section(c, &plain, l, i18n.T(l, "assistant-briefing.head_later", i18n.Args{"n": f.LaterTotal}), later, f.LaterTotal)
	}

	// In progress: the tasks being worked on with their stage bar (left out when there are none).
	if f.ProgressTotal > 0 {
		var working []briefBlock
		for i, p := range f.Progress {
			stage := ""
			if p.StageKind != "general" {
				stage = runner.prompts.OpenStageBar(f.Lang, p.Stage) // open work: never done
			}
			md := fmt.Sprintf("**%d** %s\n%s", i+1, botcard.EscapeMarkdown(textfmt.Clip(oneLine(p.Title), 60)), stage+briefLink(l, deeplink.Web(f.Web, deeplink.Todo(p.ID))))
			working = append(working, briefBlock{md: md, plain: fmt.Sprintf("%d. %s", i+1, textfmt.Clip(oneLine(p.Title), 60))})
		}
		room.section(c, &plain, l, i18n.T(l, "assistant-briefing.head_progress", i18n.Args{"n": f.ProgressTotal}), working, f.ProgressTotal)
	}

	if len(f.DeferredSupervision) > 0 {
		rows := make([]briefBlock, 0, len(f.DeferredSupervision))
		for _, row := range f.DeferredSupervision {
			line := row.Title + " — " + row.Reason
			rows = append(rows, briefBlock{md: botcard.EscapeMarkdown(line) + briefLink(l, deeplink.Web(f.Web, deeplink.Todo(row.TodoID.String()))), plain: line})
		}
		room.section(c, &plain, l, i18n.T(l, "assistant-briefing.supervision_deferred"), rows, len(rows))
	}

	// Quick capture (capture_brief.go): how many wait to be sorted and what was filed; a sensitive capture only counted.
	if len(f.Captures) > 0 {
		var captures []briefBlock
		for _, line := range f.Captures {
			captures = append(captures, briefBlock{md: botcard.EscapeMarkdown(line), plain: line})
		}
		room.section(c, &plain, l, i18n.T(l, "assistant-captures.heading"), captures, len(captures))
	}

	// The recommended setup, on the week's first briefing when the numbers name something (setup_suggest.go): what each
	// mechanism would have removed, its cost and where it is turned on. Nothing is turned on from here.
	if f.Setup != nil && len(f.Setup.Suggestions) > 0 {
		intro := i18n.T(l, "assistant-setup_suggest.intro")
		setup := []briefBlock{{md: botcard.EscapeMarkdown(intro), plain: intro}}
		for _, sg := range f.Setup.Suggestions {
			setup = append(setup, briefBlock{md: botcard.EscapeMarkdown(sg.Text) + briefLink(l, sg.Link), plain: sg.Text})
		}
		room.section(c, &plain, l, i18n.T(l, "assistant-setup_suggest.heading"), setup, len(setup))
	}

	// Provider text stays in plain-text elements. In particular, URLs and card-style
	// tags in report sections cannot turn into links, mentions, or action buttons.
	for _, contribution := range f.Contributions {
		for _, line := range contribution.PlainLines() {
			c.Note(line)
			plain = append(plain, line)
		}
	}

	c.Note(i18n.T(l, "assistant-briefing.note", i18n.Args{"time": briefClock(f, f.At)}))
	if required := strings.Join(plain[:requiredPlain], "\n"); len([]rune(required)) > fallbackMax {
		return c, required, shown
	}
	return c, textfmt.Clip(strings.Join(plain, "\n"), fallbackMax), shown
}

// splitBriefAnswerCards keeps every fixed wait row inside Feishu's real per-card limits. The original card is returned
// unchanged when it fits. Otherwise Markdown is split only at line boundaries (or inside one exceptionally long line),
// then elements are packed in their original order; no required row is replaced by an overflow count. plain remains the
// complete phone notification, while each rich card gets only its own text as its plain-text substitute.
func splitBriefAnswerCards(card *botcard.Card, plain, title string) []modapi.BriefCardPage {
	fits := func(c *botcard.Card) bool {
		return len(c.JSON()) <= botcard.MaxBytes && len(c.Body.Elements) <= botcard.MaxElements
	}
	if fits(card) {
		return []modapi.BriefCardPage{{Card: (*assistantwire.BotCard)(card), Fallback: plain}}
	}
	empty := func() *botcard.Card {
		c := *card // header and config are immutable here; each continuation gets its own element slice
		c.Body.Elements = nil
		return &c
	}
	markdown := func(s string) map[string]any { return map[string]any{"tag": "markdown", "content": s} }
	fitsMarkdown := func(s string) bool {
		c := empty()
		c.Markdown(s)
		return fits(c)
	}
	var cards []*botcard.Card
	cur := empty()
	flush := func() {
		if len(cur.Body.Elements) == 0 {
			return
		}
		cards = append(cards, cur)
		cur = empty()
	}
	for _, el := range card.Body.Elements {
		parts := []any{el}
		if m, ok := el.(map[string]any); ok && m["tag"] == "markdown" {
			if text, ok := m["content"].(string); ok && !fitsMarkdown(text) {
				parts = nil
				// SplitAfter keeps every newline in exactly one chunk, so concatenating the chunks reproduces the element.
				for _, line := range strings.SplitAfter(text, "\n") {
					if line == "" {
						continue
					}
					for !fitsMarkdown(line) {
						runes := []rune(line)
						lo, hi, best := 1, len(runes), 0
						for lo <= hi {
							mid := (lo + hi) / 2
							if fitsMarkdown(string(runes[:mid])) {
								best, lo = mid, mid+1
							} else {
								hi = mid - 1
							}
						}
						if best == 0 { // the header alone is over budget; still do not discard the row's text
							best = 1
						}
						parts = append(parts, markdown(string(runes[:best])))
						line = string(runes[best:])
					}
					if line != "" {
						parts = append(parts, markdown(line))
					}
				}
			}
		}
		for _, part := range parts {
			cur.Body.Elements = append(cur.Body.Elements, part)
			if fits(cur) {
				continue
			}
			cur.Body.Elements = cur.Body.Elements[:len(cur.Body.Elements)-1]
			flush()
			cur.Body.Elements = append(cur.Body.Elements, part)
		}
	}
	flush()
	out := make([]modapi.BriefCardPage, len(cards))
	for i, c := range cards {
		out[i] = modapi.BriefCardPage{Card: (*assistantwire.BotCard)(c), Fallback: briefCardElementText(c, title)}
	}
	return out
}

// briefCardElementText is used only when a rich continuation itself is refused. Keeping its Markdown markers and links
// is preferable to repeating the whole briefing under every continuation or dropping that continuation's fixed rows.
func briefCardElementText(c *botcard.Card, title string) string {
	lines := []string{title}
	for _, el := range c.Body.Elements {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		switch m["tag"] {
		case "markdown":
			if text, ok := m["content"].(string); ok {
				lines = append(lines, text)
			}
		case "div":
			if text, ok := m["text"].(map[string]any); ok {
				if content, ok := text["content"].(string); ok {
					lines = append(lines, content)
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

// briefFixedDirections writes every collected direction wait. Folding is data, decided by foldBriefDirection, not a
// side effect of the card's remaining byte budget: in particular no owner row can disappear here.
func briefFixedDirections(c *botcard.Card, plain *[]string, room *briefBudget, f *modapi.BriefSnapshot) {
	if len(f.Directions) == 0 {
		return
	}
	l := i18n.Lang(f.Lang)
	head := i18n.T(l, "assistant-briefing.head_directions")
	*plain = append(*plain, head)
	var body strings.Builder
	body.WriteString("**" + botcard.EscapeMarkdown(head) + "**")
	for _, d := range f.Directions {
		line := i18n.T(l, "assistant-briefing.direction_head", i18n.Args{"name": d.Name, "open": d.Open, "active": d.Active, "undispatched": d.Undispatched})
		body.WriteString("\n\n**" + botcard.EscapeMarkdown(line) + "**")
		if d.Link != "" {
			body.WriteString(briefLink(l, d.Link))
		}
		*plain = append(*plain, line)
		for _, w := range d.Waits {
			body.WriteString("\n" + botcard.EscapeMarkdown(w.Line) + briefLink(l, w.Link))
			row := w.Line
			if botcard.MarkdownLinkOK(w.Link) {
				row += " · " + w.Link
			}
			*plain = append(*plain, row)
		}
		if d.More > 0 {
			more := i18n.T(l, "assistant-briefing.direction_more", i18n.Args{"n": d.More})
			body.WriteString("\n" + botcard.EscapeMarkdown(more) + briefLink(l, d.Link))
			*plain = append(*plain, more)
		}
	}
	text := body.String()
	c.Markdown(text)
	room.bytes, room.els = room.bytes-mdCost(text), room.els-1
}

func briefFixedDisabled(c *botcard.Card, plain *[]string, room *briefBudget, f *modapi.BriefSnapshot) {
	if len(f.DisabledRoutines) == 0 {
		return
	}
	l := i18n.Lang(f.Lang)
	head := i18n.T(l, "assistant-briefing.head_disabled")
	*plain = append(*plain, head)
	var body strings.Builder
	body.WriteString("**" + botcard.EscapeMarkdown(head) + "**")
	for _, r := range f.DisabledRoutines {
		body.WriteString("\n\n" + botcard.EscapeMarkdown(r.Line) + briefLink(l, r.Link))
		*plain = append(*plain, r.Line)
	}
	text := body.String()
	c.Markdown(text)
	room.bytes, room.els = room.bytes-mdCost(text), room.els-1
}

// briefBudget is what is left of the card's bytes and elements.
type briefBudget struct{ bytes, els int }

// section adds a heading and as many of its blocks as fit, in one Markdown element, or (when some block has a button)
// one per block; the blocks that do not fit, and those beyond RowsMax, are counted in an "and N more" line. total is
// how many rows the section stands for (its blocks may be fewer: the lists are cut before). It returns the todos of the
// blocks with buttons that were added.
func (b *briefBudget) section(c *botcard.Card, plain *[]string, l i18n.Lang, head string, blocks []briefBlock, total int) (shown []string) {
	head = "**" + botcard.EscapeMarkdown(head) + "**"
	*plain = append(*plain, head)
	if total == 0 {
		line := head + "\n" + botcard.EscapeMarkdown(i18n.T(l, "assistant-briefing.none"))
		if n := mdCost(line); n <= b.bytes && b.els >= 1 {
			c.Markdown(line)
			b.bytes, b.els = b.bytes-n, b.els-1
		}
		return nil
	}
	hasButtons := false
	for _, bl := range blocks {
		hasButtons = hasButtons || bl.btn != nil
	}
	body := head // the one Markdown element of a section without buttons, grown row by row
	added := 0
	if hasButtons {
		if n := mdCost(head); n <= b.bytes && b.els >= 1 {
			c.Markdown(head)
			b.bytes, b.els = b.bytes-n, b.els-1
		}
	}
	for _, bl := range blocks {
		if added >= RowsMax {
			break
		}
		if hasButtons && bl.btn == nil { // a row without one among rows with buttons (a decision with nothing recommended)
			n := mdCost(bl.md)
			if n > b.bytes || b.els < 1 {
				break
			}
			c.Markdown(bl.md)
			b.bytes, b.els = b.bytes-n, b.els-1
		} else if hasButtons {
			n := botcard.ElementBytes(func(x *botcard.Card) { x.Markdown(bl.md).Buttons(bl.btn) })
			if n > b.bytes || b.els < 2 {
				break
			}
			c.Markdown(bl.md).Buttons(bl.btn)
			b.bytes, b.els = b.bytes-n, b.els-2
			shown = append(shown, bl.todo)
		} else {
			n := mdCost("\n\n" + bl.md)
			if n > b.bytes {
				break
			}
			body += "\n\n" + bl.md
			b.bytes -= n
		}
		*plain = append(*plain, bl.plain)
		added++
	}
	if !hasButtons && b.els >= 1 {
		c.Markdown(body)
		b.els--
	}
	if more := total - added; more > 0 {
		line := botcard.EscapeMarkdown(i18n.T(l, "assistant-feishu_cards.board_more", i18n.Args{"count": more}))
		*plain = append(*plain, line)
		c.Markdown(line) // its room is the tail's
	}
	return shown
}

// mdCost is the bytes a Markdown element with this content takes in the card.
func mdCost(s string) int {
	b, _ := json.Marshal(s)
	return len(b) + 40
}

// briefClock is t as the owner's time of day.
func briefClock(f *modapi.BriefSnapshot, t time.Time) string {
	loc := f.Loc
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("15:04")
}

// briefFailedLine is one thing that did not go through, as a line: what it was, what happened, and why; md: for a
// card's Markdown (escaped), else for its plain-text fallback.
func briefFailedLine(f *modapi.BriefSnapshot, x modapi.BriefFailure, md bool) string {
	esc := func(s string) string { return s }
	if md {
		esc = botcard.EscapeMarkdown
	}
	key := "failed_task"
	switch {
	case x.Kind == "routine" && x.Skipped:
		key = "failed_skipped"
	case x.Kind == "routine":
		key = "failed_run"
	}
	line := i18n.T(i18n.Lang(f.Lang), "assistant-briefing."+key, i18n.Args{"title": esc(textfmt.Clip(oneLine(x.Title), 60)), "time": briefClock(f, x.At)})
	if x.Why != "" {
		line += " — " + esc(x.Why)
	}
	if md { // what did not go through is red on the card
		line = "<font color='red'>" + line + "</font>"
	}
	return line
}

// briefLink is " · [Open](link)" for an address a card link can hold, else nothing.
func briefLink(l i18n.Lang, link string) string {
	if !botcard.MarkdownLinkOK(link) {
		return ""
	}
	return " · [" + i18n.T(l, "assistant-feishu_cards.board_open") + "](" + link + ")"
}
