package assistant

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// The rows of what waits for the owner and their wait wording, shared by the briefing, the acceptance asks and the standing board (moved out of feishu_board.go, #13404).

const (
	// boardRowsMax is the most rows one board lists (it is one card); the rest are counted.
	boardRowsMax = 30
	// boardSummaryRunes is how much of a card's question a row quotes.
	boardSummaryRunes = 60
	// boardTailBytes is kept free for the "and N more" line and the note under the rows.
	boardTailBytes = 1500
)

// boardRow is one thing waiting for the owner.
type boardRow struct {
	blocking bool // an Agent (or the main conversation) is held up by it; a to-do waiting to be verified is not
	waited   time.Duration
	id       string // breaks a tie
	summary  string
	from     string
	link     string
	nudged   bool // the bot nudged under its card in the chat (the lark module's nudge.go, #6084)
	// machine and session are the chat a card or a question that can wait came from, empty when it came from none; the
	// apps open it from the brief item (modules/briefing/feed.go).
	machine, session string
	// asked is when the "Please verify" ask of a to-do waiting to be verified was made (its card, if it has one, went out then) (zero: the row is not one); id is the to-do.
	asked time.Time

	detail, evidenceLink string // current verification summary and deliverable for the briefing feed
}

// boardRows are the things waiting, in the order the board shows them: what holds up an Agent first, the longest
// waiting first, then the to-dos waiting for the owner to verify them, the longest waiting first.
func (s *Service) boardRows(ctx context.Context, l i18n.Lang, web string, now time.Time) []boardRow {
	var rows []boardRow
	for _, ix := range s.visiblePendingInteractions() {
		if s.interactionEnded(ix.ID) { // over, though its entry is still being taken away
			continue
		}
		link := web
		if ix.SessionID != "" {
			link = sessionLink(web, ix.MachineID, ix.SessionID, s.sessionAssistantLocal(ix.MachineID, ix.SessionID))
		}
		rows = append(rows, boardRow{blocking: true, waited: boardWaited(now, ix.CreatedAt), id: ix.ID,
			summary: boardSummary(l, ix), from: s.boardFrom(l, ix.MachineID, ix.SessionID, ix.SessionTitle), link: link, nudged: s.larkModule().CardNudged(ix.ID),
			machine: ix.MachineID, session: ix.SessionID})
	}
	for _, a := range s.awaitingAcceptance(ctx) {
		summary := i18n.T(l, "assistant-feishu_cards.board_verify", i18n.Args{"title": clip(oneLine(a.title), 40)})
		verdict := verdictText(l, &a.ask, a.delivery)
		if a.ask.Conclusion { // the task's conclusion, as its card says it
			summary = i18n.T(l, "assistant-feishu_cards.board_verdict", i18n.Args{"title": clip(oneLine(a.title), 40), "verdict": verdict})
		}
		detail, evidence := a.verificationDetails(web, verdict)
		rows = append(rows, boardRow{detail: detail, evidenceLink: evidence, waited: boardWaited(now, a.since), id: a.todo, summary: summary,
			from: s.boardFrom(l, a.machine, a.session, ""), link: todoLink(web, a.todo), asked: a.since})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].blocking != rows[j].blocking {
			return rows[i].blocking
		}
		if rows[i].waited != rows[j].waited {
			return rows[i].waited > rows[j].waited
		}
		return rows[i].id < rows[j].id
	})
	return rows
}

func boardWaited(now, since time.Time) time.Duration {
	if since.IsZero() || since.After(now) {
		return 0
	}
	return now.Sub(since)
}

// boardSummary is the line a card gets on the board: what it asks, clipped. A permission says only that it needs the
// owner's approval, never the command or the path (the board sits in a chat that may be seen in a screenshot), and a
// terminal dialog only that it needs them (its text is the screen's).
func boardSummary(l i18n.Lang, ix proto.Interaction) string {
	switch ix.Kind {
	case proto.IxPermission:
		return i18n.T(l, "assistant-feishu_cards.board_needs_approval")
	case proto.IxScreen:
		return answeredT(l, "needs_you")
	}
	return clip(oneLine(interactionQuestion(l, ix)), boardSummaryRunes)
}

// boardFrom says who asked: Persona (a conversation of its own) or the Agent, by its title.
func (s *Service) boardFrom(l i18n.Lang, machine, session, title string) string {
	if session == "" || s.sessionAssistantLocal(machine, session) {
		return i18n.T(l, "assistant-feishu_cards.board_from_persona")
	}
	name := transcript.Title(title)
	if name == "" {
		if sess, ok := s.nodeSession(machine, session); ok {
			name = transcript.Title(sess.Title)
		}
	}
	if name == "" {
		return i18n.T(l, "assistant-feishu_cards.board_from_agent_unnamed")
	}
	return i18n.T(l, "assistant-feishu_cards.board_from_agent", i18n.Args{"name": clip(oneLine(name), 30)})
}

// boardWait is how long something has waited, in l: "2 min", "1 h 20 min", "3 d".
func boardWait(l i18n.Lang, d time.Duration) string {
	switch {
	case d < time.Minute:
		return i18n.T(l, "assistant-feishu_cards.board_wait_now")
	case d < time.Hour:
		return i18n.T(l, "assistant-feishu_cards.board_wait_minutes", i18n.Args{"count": int(d / time.Minute)})
	case d < 24*time.Hour:
		h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
		if m == 0 {
			return i18n.T(l, "assistant-feishu_cards.board_wait_hours", i18n.Args{"count": h})
		}
		return i18n.T(l, "assistant-feishu_cards.board_wait_hours_minutes", i18n.Args{"hours": h, "minutes": m})
	}
	return i18n.T(l, "assistant-feishu_cards.board_wait_days", i18n.Args{"count": int(d / (24 * time.Hour))})
}

// awaitingAccept is a to-do waiting for the owner to verify it.
type awaitingAccept struct {
	todo, title, machine, session string
	since                         time.Time
	ask                           acceptAsk     // its verdict for a conclusion's row (conclusion.go)
	review                        *TaskReview   // active accepted review for owner verification
	delivery                      *TodoDelivery // effective delivery state (currentDelivery)
}

// awaitingAcceptance are the to-dos whose "Please verify" ask is up (with a card or without, NoCard) and not passed: the ones that can still be passed
// (not closed by the owner or a review, not asked before the latest reopen), in the order they were asked. Read only:
// the sweeps that forget the ended ones are the reminders' and the snapshot's. A decision returned to its supervisor is
// kept in the book but is not an owner row until a fresh review re-issues it.
func (s *Service) awaitingAcceptance(ctx context.Context) []awaitingAccept {
	return s.acceptanceRounds(ctx, false)
}

// acceptanceRounds reads open asks. includeReturned is for the recheck sweep itself; every owner-facing projection
// leaves returned rounds out, including legacy undecidable reviews and rechecks persisted by an older release.
func (s *Service) acceptanceRounds(ctx context.Context, includeReturned bool) []awaitingAccept {
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return nil
	}
	asks := map[string]acceptAsk{}
	for id, a := range b.asks {
		if !a.AskedAt.IsZero() && a.AcceptedAt.IsZero() {
			asks[id] = *a
		}
	}
	b.mu.Unlock()
	var out []awaitingAccept
	for id, a := range asks {
		t, ok := s.todoNow(id)
		if !ok || acceptAskEnded(&a, t) || acceptAskStale(&a, t) {
			continue
		}
		if !includeReturned && (!a.RecheckAt.IsZero() || ownerDecisionReturned(t.Supervision)) {
			continue
		}
		var review *TaskReview
		if r := triageLatestReview(t.Supervision); t.Supervision.state(t) == reviewUserCheck && r != nil && r.Verdict == reviewAccepted {
			review = r
		}
		out = append(out, awaitingAccept{review: review, todo: id, title: t.Title, machine: a.MachineID, session: a.SessionID, since: a.AskedAt, ask: a, delivery: currentDelivery(t)})
	}
	slices.SortFunc(out, func(x, y awaitingAccept) int {
		if c := x.since.Compare(y.since); c != 0 {
			return c
		}
		return strings.Compare(x.todo, y.todo)
	})
	return out
}

// boardRowMarkdown is one row: its number and what it is, then how long it has waited, whether it was nudged, who asked and a link.
func boardRowMarkdown(l i18n.Lang, n int, r boardRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%d** %s\n", n, botcard.EscapeMarkdown(r.summary))
	b.WriteString(botcard.EscapeMarkdown(i18n.T(l, "assistant-feishu_cards.board_waited", i18n.Args{"wait": boardWait(l, r.waited)})))
	if r.nudged {
		b.WriteString(" · " + i18n.T(l, "assistant-feishu_cards.board_nudged"))
	}
	b.WriteString(" · " + botcard.EscapeMarkdown(r.from))
	if boardLinkOK(r.link) {
		b.WriteString(" · [" + i18n.T(l, "assistant-feishu_cards.board_open") + "](" + r.link + ")")
	}
	return b.String()
}

var boardLinkOK = botcard.MarkdownLinkOK

// boardCard is the board and the same as plain text (the fallback when Feishu refuses the card): one line a thing,
// with what is waiting and a note of when it was written; when nothing is, it says so. said is the card without that
// note, to tell an edit that would change nothing.
func (s *Service) boardCard(ctx context.Context, now time.Time) (card *botcard.Card, fallback, said string) {
	l, web := s.botLang(ctx), s.webBase(ctx)
	rows := s.boardRows(ctx, l, web, now)
	title := i18n.T(l, "assistant-feishu_cards.board_title")
	summary := i18n.T(l, "assistant-feishu_cards.board_empty")
	color := botcard.Green
	if len(rows) > 0 {
		summary = i18n.T(l, "assistant-feishu_cards.board_summary", i18n.Args{"count": len(rows)})
		color = botcard.Blue
		if rows[0].blocking {
			color = botcard.Orange
		}
	}
	c := botcard.NewCard(title, color, summary)
	var plain []string
	if len(rows) == 0 {
		c.Markdown(i18n.T(l, "assistant-feishu_cards.board_empty"))
		plain = append(plain, i18n.T(l, "assistant-feishu_cards.board_empty"))
	}
	// One card: the rows that fit its byte and element budget, the rest counted.
	budget := reportCardMaxBytes - boardTailBytes - len(c.JSON())
	shown := 0
	for i, r := range rows {
		md := boardRowMarkdown(l, i+1, r)
		n := feishuElBytes(func(c *botcard.Card) { c.Markdown(md) })
		if shown >= boardRowsMax || n > budget {
			break
		}
		budget -= n
		c.Markdown(md)
		plain = append(plain, fmt.Sprintf("%d. %s · %s · %s", i+1, r.summary, i18n.T(l, "assistant-feishu_cards.board_waited", i18n.Args{"wait": boardWait(l, r.waited)}), r.from))
		shown++
	}
	if more := len(rows) - shown; more > 0 {
		line := i18n.T(l, "assistant-feishu_cards.board_more", i18n.Args{"count": more})
		c.Markdown(botcard.EscapeMarkdown(line))
		plain = append(plain, line)
	}
	said = c.JSON()
	c.Note(i18n.T(l, "assistant-feishu_cards.board_updated", i18n.Args{"time": now.In(s.location(ctx)).Format("15:04")}))
	return c, title + "\n" + clip(strings.Join(plain, "\n"), reportFallbackMax), said
}
