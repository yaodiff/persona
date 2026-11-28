package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The workbench (#1961, docs/extensions/bots.md "The workbench"): with the bot's `workbench` switch on, the chat gets a
// home card that gathers what Persona has going on and opens every other card (/home), the day at a glance (/today) and
// the Inbox with its actions (/inbox). The home card is the standing board (feishu_board.go) drawn as a home page,
// pinned and kept the only one (feishu_workbench_pin.go). These cards only read the assistant's state and call what
// the app's pages call; nothing here decides anything a model would. With the switch off none of it exists: the
// commands go on as text, no card carries a workbench button and /start has no permission field.

const (
	// wbInboxMost is the most Inbox rows one /inbox card lists: each has a row of buttons.
	wbInboxMost = 6
	// wbHomeRows is how many waiting things, agents at work and Inbox items the home card shows of each.
	wbHomeRows = 3
	// wbFilled is Card 2.0's filled primary button: the one action a card is about.
	wbFilled = "primary_filled"
)

// workbenchCommand answers /home, /today and /inbox with the switch on; false when cmd is none of them (or the switch
// is off), so the message goes on as it always did.
func (s core) workbenchCommand(ctx context.Context, f assistantwire.FeishuFeatures, cmd string) bool {
	if !f.Workbench {
		return false
	}
	// The cards stand on their own: quoting the one-word command above them only adds a line.
	reply := func(c *botcard.Card, fallback string) {
		if _, err := s.botPostTo(ctx, feishuTarget{}, "", c, fallback, ""); err != nil {
			s.logf("bots: workbench: %v", err)
		}
	}
	switch cmd {
	case "/home", "/工作台", "/主页": // i18n-ignore: command aliases typed by the user; the catalogs describe them in /help
		s.summonWorkbench(ctx)
	case "/today", "/今日", "/今天": // i18n-ignore: command aliases typed by the user
		reply(s.todayCard(ctx))
	case "/inbox", "/收件箱": // i18n-ignore: command aliases typed by the user
		reply(s.inboxCard(ctx))
	default:
		return false
	}
	return true
}

// workbenchNav answers a workbench button ({a: nav, to}): a switch of where messages go is a toast; a card is posted
// from a goroutine (Feishu gives the callback 3 seconds).
func (s core) workbenchNav(ctx context.Context, a *botcard.CardAction) *botcard.CardResponse {
	l := s.botLang(ctx)
	f, _ := s.feishuFeatures(ctx)
	if !f.Workbench {
		return toast("info", i18n.T(l, "assistant-feishu_cards.card_expired"))
	}
	to := a.Str("to")
	switch to {
	case "new", "main":
		if !f.Main {
			return toast("error", i18n.T(l, "assistant-feishu_cards.main_off"))
		}
		if to == "new" {
			s.setBotCurrent(botTarget{Kind: targetPersona})
			return toast("success", i18n.T(l, "assistant-feishu_cards.next_is_new"))
		}
		s.setBotCurrent(botTarget{Kind: targetMain})
		return toast("success", i18n.T(l, "assistant-feishu_cards.now_to_main"))
	case "agents", "start":
		if !f.Agents {
			return toast("error", i18n.T(l, "assistant-feishu_cards.agents_off_short"))
		}
	case "refresh":
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.workbenchRefresh(cctx)
		}()
		return toast("success", wbT(l, "refreshed"))
	case "ix":
		id := a.Str("id")
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.workbenchBringCard(cctx, id)
		}()
		return toast("info", wbT(l, "opening"))
	case "today", "inbox", "tasks", "home", "list":
	default:
		return toast("info", i18n.T(l, "assistant-feishu_cards.card_expired"))
	}
	go func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		post := func(c *botcard.Card, fallback string) {
			if _, err := s.botPostTo(cctx, feishuTarget{}, "", c, fallback, ""); err != nil {
				s.logf("bots: workbench %s: %v", to, err)
			}
		}
		switch to {
		case "agents":
			post(s.agentsCard(cctx, ""))
		case "start":
			post(s.startAgentCard(cctx, ""), "")
		case "list":
			post(s.conversationsCard(cctx))
		case "tasks":
			cards, fallbacks := s.h.TasksOverview(cctx)
			for i, c := range cards {
				post(c, fallbacks[i])
			}
		case "today":
			post(s.todayCard(cctx))
		case "inbox":
			post(s.inboxCard(cctx))
		case "home":
			s.summonWorkbench(cctx)
		}
	}()
	return toast("info", wbT(l, "opening"))
}

func wbNavButton(label, style, to string) botcard.Button {
	return botcard.Btn(label, style, map[string]any{"a": "nav", "to": to})
}

// homeCard is the workbench: where messages go (its subtitle), what waits for the owner (each a row that brings its card
// down), the agents at work (each a row that switches the chat to it), the Inbox and the day, the week's tokens, and one
// row of buttons with a "more" menu for every other card and companion bot. said is the card without its "updated at"
// note, to tell a redraw that would change nothing (boardPut).
func (s core) homeCard(ctx context.Context, f assistantwire.FeishuFeatures) (card *botcard.Card, fallback, said string) {
	l, web := s.botLang(ctx), s.h.WebBase(ctx)
	loc := s.h.Location(ctx)
	now := s.t.board.clock()
	c, busy := s.workbenchCount(ctx, now)
	rows := s.h.WorkbenchBoard(ctx, l, web, now)
	title := wbT(l, "home_title")
	target := s.h.TargetLabel(ctx, s.botTargetFor("", ""))
	card = botcard.NewCard(title, botcard.Blue, wbT(l, "home_summary")).Subtitle(wbT(l, "home_target", i18n.Args{"target": clip(target, 40)}))
	if len(rows) > 0 {
		card.Tag(wbT(l, "home_tag_waiting", i18n.Args{"n": len(rows)}), botcard.Orange)
	}
	if c.Working > 0 {
		card.Tag(wbT(l, "home_tag_busy", i18n.Args{"n": c.Working}), botcard.Blue)
	}
	var fb strings.Builder
	fb.WriteString(title + "\n")

	// What waits for the owner, as the board lists it: a row that holds something up brings its card down here.
	if len(rows) == 0 {
		card.Markdown("<font color='grey'>" + wbT(l, "home_nothing_waits") + "</font>")
	} else {
		card.Markdown("**" + wbT(l, "home_waits_head", i18n.Args{"count": len(rows)}) + "**")
		for i, r := range rows {
			if i == wbHomeRows {
				card.Note(wbT(l, "home_waits_more", i18n.Args{"count": len(rows) - wbHomeRows}))
				break
			}
			line := "**" + botcard.EscapeMarkdown(r.From) + "** · " + botcard.EscapeMarkdown(r.Summary) + "\n<font color='grey'>" +
				botcard.EscapeMarkdown(i18n.T(l, "assistant-feishu_cards.board_waited", i18n.Args{"wait": r.Waited})) + "</font>"
			switch {
			case r.Blocking:
				card.Row(map[string]any{"a": "nav", "to": "ix", "id": r.ID}, line)
			case r.Link != "":
				card.LinkRow(r.Link, line)
			default:
				card.Markdown(line)
			}
			fmt.Fprintf(&fb, "- %s · %s\n", r.From, r.Summary)
		}
	}

	// The agents at work, each a row that switches the chat to it.
	card.Markdown("**" + wbAgentsLine(l, c) + "**")
	for i, r := range busy {
		if i == wbHomeRows {
			break
		}
		primary, secondary := s.h.SessionNamesAs(r.Session, s.h.UseHerdrTabName(ctx))
		label := clip(oneLine(primary), 24)
		card.Row(map[string]any{"a": "switch", "k": targetAgent, "m": r.Machine, "s": r.Session.SessionID, "t": label + " · " + r.MachineName},
			agentRowMarkdown(l, label, secondary, r.MachineName, r.Session, 70))
	}

	// The Inbox and the day.
	var day []string
	inbox := wbInboxLine(l, c)
	for i, it := range c.inboxTop {
		if i == 2 {
			break
		}
		inbox += "\n· " + wbT(l, "pair", i18n.Args{"a": botcard.EscapeMarkdown(clip(wbSender(it), 10)), "b": botcard.EscapeMarkdown(clip(wbGist(it), 40))})
	}
	day = append(day, inbox)
	if e := s.h.NextMeeting(ctx, now); e != nil {
		day = append(day, wbT(l, "home_next_meeting", i18n.Args{"time": e.Start.In(loc).Format("15:04"), "title": botcard.EscapeMarkdown(clip(e.Title, 30))}))
	}
	day = append(day, wbT(l, "home_machines", i18n.Args{"online": c.Online, "count": c.Machines}))
	card.Markdown(strings.Join(day, "\n"))
	fb.WriteString(strings.Join(day, "\n"))

	// One row: the Inbox (filled when something there needs the owner), the tasks, and a "more" menu with the rest.
	companions := s.workbenchCompanions(ctx)
	var bs []botcard.Button
	inboxBot := slices.IndexFunc(companions, func(b assistantwire.WorkbenchCompanion) bool { return b.Inbox })
	if inboxBot < 0 {
		label, style := wbT(l, "btn_inbox"), botcard.Default
		if c.inboxNeed > 0 {
			label, style = wbT(l, "btn_inbox_count", i18n.Args{"count": c.inboxNeed}), wbFilled
		}
		bs = append(bs, wbNavButton(label, style, "inbox"))
	} else {
		b := companions[inboxBot]
		label := strings.TrimSpace(b.Name)
		if c.inboxNeed > 0 {
			label += " · " + fmt.Sprint(c.inboxNeed)
		}
		btn := botcard.LinkBtn(label, b.Link)
		if c.inboxNeed > 0 {
			btn["type"] = wbFilled
		}
		bs = append(bs, btn)
	}
	bs = append(bs, wbNavButton(wbT(l, "btn_tasks"), botcard.Default, "tasks"))
	nav := func(key, to string) botcard.MenuItem {
		return botcard.MenuItem{Label: wbT(l, key), Value: map[string]any{"to": to}}
	}
	menu := []botcard.MenuItem{nav("btn_today", "today")}
	if f.Agents {
		menu = append(menu, nav("btn_agents", "agents"), nav("btn_start", "start"))
	}
	if f.Main {
		menu = append(menu, nav("btn_main", "main"), nav("btn_new", "new"))
	}
	menu = append(menu, nav("btn_refresh", "refresh"))
	for i, b := range companions {
		if i != inboxBot {
			menu = append(menu, botcard.MenuItem{Label: strings.TrimSpace(b.Name), URL: b.Link})
		}
	}
	card.Buttons(append(bs, botcard.Overflow(map[string]any{"a": "nav"}, menu...))...)
	said = card.JSON()
	card.Note(wbT(l, "home_note_pinned", i18n.Args{"time": now.In(loc).Format("15:04")}))
	return card, s.h.ClipFallback(strings.TrimSpace(fb.String())), said
}

func wbWho(it assistantwire.WorkbenchItem) string {
	switch {
	case it.Sender != "" && it.Title != "" && it.Sender != it.Title:
		return it.Sender + " · " + it.Title
	case it.Sender != "":
		return it.Sender
	}
	return it.Title
}

func wbSender(it assistantwire.WorkbenchItem) string {
	if it.Sender != "" {
		return it.Sender
	}
	return it.Title
}

func wbGist(it assistantwire.WorkbenchItem) string {
	for _, t := range []string{it.Recap, it.Summary, it.Preview} {
		if t = strings.TrimSpace(t); t != "" {
			return t
		}
	}
	return ""
}

// wbSource is where an item came from, in words: a connector's label for it, else its source's name (a subscription's
// one row has no label, its title being the subscription's name, #5671), never an id when there is a name.
func wbSource(l i18n.Lang, it assistantwire.WorkbenchItem) string {
	if it.Connector && it.ConnectorLabel != "" {
		return it.ConnectorLabel
	}
	if it.Connector && strings.TrimSpace(it.AccountName) != "" {
		return it.AccountName
	}
	if it.Mail || it.Source == "mail" {
		return wbT(l, "source_mail")
	}
	return it.Source
}

// todayCard is the day at a glance: the main conversation's "Today" (the one the app shows), plus what waits.
func (s core) todayCard(ctx context.Context) (*botcard.Card, string) {
	l, loc := s.botLang(ctx), s.h.Location(ctx)
	now := s.t.board.clock().In(loc)
	title := wbT(l, "today_title", i18n.Args{"date": now.Format(wbT(l, "date_layout")), "weekday": wbT(l, "weekday_"+now.Weekday().String())})
	card := botcard.NewCard(title, botcard.Blue, title)
	var body todayBody
	if raw := s.h.TodayBody(ctx); len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	c, _ := s.workbenchCount(ctx, now)
	var b strings.Builder
	section := func(head string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "**%s**\n", head)
		for _, ln := range lines {
			fmt.Fprintf(&b, "- %s\n", ln)
		}
		b.WriteString("\n")
	}
	var wait []string
	if c.Pending > 0 {
		wait = append(wait, wbT(l, "today_pending", i18n.Args{"count": c.Pending}))
	}
	for _, t := range body.Waiting {
		wait = append(wait, botcard.EscapeMarkdown(clip(t.Title, 60)))
	}
	for i, it := range s.workbenchInbox() {
		if i == wbHomeRows || !wbNeedsYou(it) {
			break
		}
		wait = append(wait, wbT(l, "pair", i18n.Args{"a": botcard.EscapeMarkdown(clip(wbWho(it), 16)), "b": botcard.EscapeMarkdown(clip(wbGist(it), 50))}))
	}
	section(wbT(l, "today_waiting"), wait)
	var meet []string
	for _, m := range body.Meetings {
		meet = append(meet, m.Start.In(loc).Format("15:04")+"–"+m.End.In(loc).Format("15:04")+" "+botcard.EscapeMarkdown(clip(m.Title, 40)))
	}
	section(wbT(l, "today_meetings"), meet)
	var done []string
	for _, t := range body.Done {
		done = append(done, botcard.EscapeMarkdown(clip(t.Title, 60)))
	}
	section(wbT(l, "today_done"), done)
	var fail []string
	for _, x := range body.Failed {
		line := botcard.EscapeMarkdown(clip(x.Title, 40))
		if x.Why != "" {
			line = wbT(l, "pair", i18n.Args{"a": line, "b": botcard.EscapeMarkdown(clip(x.Why, 60))})
		}
		fail = append(fail, line)
	}
	section(wbT(l, "today_failed"), fail)
	var at []string
	for _, ch := range body.AtMe {
		at = append(at, botcard.EscapeMarkdown(clip(ch.Name, 20))+" · "+botcard.EscapeMarkdown(clip(ch.Preview, 40)))
	}
	section(wbT(l, "today_at_me"), at)
	quiet := strings.TrimSpace(b.String()) == ""
	fmt.Fprintf(&b, "**%s**\n", wbT(l, "today_now"))
	fmt.Fprintf(&b, "%s\n", wbAgentsLine(l, c))
	fmt.Fprintf(&b, "%s\n", wbInboxLine(l, c))
	if body.Working > 0 {
		fmt.Fprintf(&b, "%s\n", wbT(l, "today_working", i18n.Args{"count": body.Working}))
	}
	if quiet {
		b.WriteString("\n" + wbT(l, "today_quiet"))
	}
	if c.Pending > 0 {
		card.Tag(wbT(l, "home_tag_waiting", i18n.Args{"n": c.Pending}), botcard.Orange)
	}
	card.Markdown(strings.TrimSpace(b.String()))
	inbox := wbNavButton(wbT(l, "btn_inbox"), botcard.Default, "inbox")
	if c.inboxNeed > 0 {
		inbox = wbNavButton(wbT(l, "btn_inbox_count", i18n.Args{"count": c.inboxNeed}), wbFilled, "inbox")
	}
	card.Buttons(inbox, wbNavButton(wbT(l, "btn_tasks"), botcard.Default, "tasks"),
		botcard.TextBtn(wbT(l, "btn_home"), map[string]any{"a": "nav", "to": "home"}))
	return card, title + "\n\n" + strings.TrimSpace(b.String())
}

// inboxCard lists the open Inbox with a row of actions per item.
func (s core) inboxCard(ctx context.Context) (*botcard.Card, string) {
	l := s.botLang(ctx)
	open := s.workbenchInbox()
	need := 0
	for _, it := range open {
		if wbNeedsYou(it) {
			need++
		}
	}
	title := wbT(l, "inbox_title", i18n.Args{"count": need})
	color := botcard.Blue
	if need > 0 {
		color = botcard.Orange
	}
	card := botcard.NewCard(title, color, title)
	var fb strings.Builder
	fb.WriteString(title + "\n")
	if len(open) == 0 {
		card.Markdown(wbT(l, "inbox_empty"))
		return card.Buttons(botcard.TextBtn(wbT(l, "btn_home"), map[string]any{"a": "nav", "to": "home"})), title
	}
	for i, it := range open {
		if i == wbInboxMost {
			card.Note(wbT(l, "inbox_more", i18n.Args{"count": len(open) - wbInboxMost}))
			break
		}
		state, color := wbT(l, "inbox_tag_need"), botcard.Orange
		if it.Urgent {
			state, color = wbT(l, "inbox_tag_urgent"), botcard.Red
		} else if !wbNeedsYou(it) {
			state, color = wbT(l, "inbox_tag_fyi"), botcard.Neutral
		}
		head := botcard.InlineTag(state, color) + " **" + botcard.EscapeMarkdown(clip(wbSender(it), 16)) + "**"
		if it.Sender != "" && it.Title != "" && it.Title != it.Sender {
			head += " · " + botcard.EscapeMarkdown(clip(it.Title, 16))
		}
		line := head
		if g := wbGist(it); g != "" {
			line += "\n" + botcard.EscapeMarkdown(clip(g, 120))
		}
		line += "\n<font color='grey'>" + botcard.EscapeMarkdown(wbSource(l, it)) + " · " + wbClock(s.h.Location(ctx), it.UpdatedAt, s.t.board.clock()) + "</font>"
		if i > 0 {
			card.HR()
		}
		card.Markdown(line)
		// One row: drafting a reply first where the item can be answered, then done, muting the sender in "more".
		var btns []botcard.Button
		if inboxCanReply(it) {
			btns = append(btns, botcard.Btn(wbT(l, "btn_draft"), botcard.Primary, map[string]any{"a": "ib", "op": "draft", "id": it.ID}))
		}
		btns = append(btns, botcard.Btn(wbT(l, "btn_done"), botcard.Default, map[string]any{"a": "ib", "op": "done", "id": it.ID}))
		if len(it.Mutes) > 0 {
			btns = append(btns, botcard.Overflow(map[string]any{"a": "ib", "id": it.ID},
				botcard.MenuItem{Label: wbT(l, "btn_mute"), Value: map[string]any{"op": "mute"}}))
		}
		card.Buttons(btns...)
		fmt.Fprintf(&fb, "- %s\n", wbT(l, "pair", i18n.Args{"a": wbWho(it), "b": clip(wbGist(it), 60)}))
	}
	card.Note(wbT(l, "inbox_note"))
	return card, strings.TrimSpace(fb.String())
}

// inboxCanReply: a reply can be sent to the item's source as the owner.
func inboxCanReply(it assistantwire.WorkbenchItem) bool {
	if it.Connector {
		return it.ConnectorCanReply
	}
	return it.Lark || it.Mail
}

// workbenchInboxAction answers an Inbox button ({a: ib, op, id}): done and mute change the list card in place;
// drafting posts a draft card (the model takes longer than the callback may), and sending it updates that card. The
// hub checked the owner pressed it; sending is the same reply the app's Send makes, once per draft card.
func (s core) workbenchInboxAction(ctx context.Context, a *botcard.CardAction) *botcard.CardResponse {
	l := s.botLang(ctx)
	if f, _ := s.feishuFeatures(ctx); !f.Workbench {
		return toast("info", i18n.T(l, "assistant-feishu_cards.card_expired"))
	}
	id, op, mid := a.Str("id"), a.Str("op"), a.Context.MessageID
	it, ok := s.h.WorkbenchItem(id)
	if !ok {
		return toast("info", wbT(l, "inbox_gone"))
	}
	refresh := func(msg string) *botcard.CardResponse {
		card, _ := s.inboxCard(ctx)
		return &botcard.CardResponse{Toast: &botcard.Toast{Type: "success", Content: msg}, Card: card.Raw()}
	}
	switch op {
	case "done":
		if err := s.h.InboxDone(ctx, id, false); err != nil {
			return toast("error", clip(oneLine(err.Error()), 120))
		}
		return refresh(wbT(l, "done_ok"))
	case "mute":
		if len(it.Mutes) == 0 {
			return toast("info", wbT(l, "mute_none"))
		}
		m := it.Mutes[0]
		for _, x := range it.Mutes {
			if x.Tag == "sender" {
				m = x
			}
		}
		if err := s.h.InboxMute(ctx, it.Source, m.Tag, m.Value, m.Name); err != nil {
			return toast("error", clip(oneLine(err.Error()), 120))
		}
		_ = s.h.InboxDone(ctx, id, true)
		return refresh(wbT(l, "mute_ok", i18n.Args{"name": clip(m.Name, 20)}))
	case "draft", "redraft":
		instruction := strings.TrimSpace(a.Form("instruction"))
		current := strings.TrimSpace(a.Form("text"))
		if op == "redraft" && current != "" {
			instruction = strings.TrimSpace(wbT(l, "redraft_prompt", i18n.Args{"draft": current}) + "\n" + instruction)
		}
		list := a.Str("list")
		if op == "draft" {
			list = mid // pressed on the Inbox card: it is redrawn once the reply goes out
		}
		expectedVersion, _ := strconv.Atoi(a.Str("version"))
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cardItem, text, err := s.h.DraftReply(cctx, id, instruction, expectedVersion, op == "redraft")
			if cardItem.ID == "" {
				cardItem = it
				if err == nil {
					// A transient result cannot borrow a version from the item captured before generation.
					cardItem.ReplyVersion = 0
				}
			}
			card := s.draftCard(cctx, cardItem, text, err, list)
			if op == "redraft" {
				if err := s.botUpdateCard(cctx, mid, card); err == nil {
					return
				}
			}
			if _, err := s.botPostTo(cctx, feishuTarget{}, mid, card, "", ""); err != nil {
				s.logf("bots: draft card: %v", err)
			}
		}()
		return toast("info", wbT(l, "drafting"))
	case "send":
		text := strings.TrimSpace(a.Form("text"))
		if text == "" {
			return toast("warning", wbT(l, "send_empty"))
		}
		if it.Done {
			return toast("info", wbT(l, "inbox_gone"))
		}
		// Missing, malformed and explicit zero all denote an old unversioned card. The reply-policy route decides
		// whether that legacy card is still allowed; inside policy scope it fails closed.
		expectedVersion, _ := strconv.Atoi(a.Str("version"))
		list := a.Str("list")
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			// One reply per draft card however many presses: the card's message is the send's id, and a press that
			// comes after the reply filed the item changes nothing (the first one drew the card).
			filed, err := s.h.ReplyInbox(cctx, id, text, "feishu-workbench-"+mid, expectedVersion)
			if filed {
				return
			}
			// Sent, the draft is over: grey, what went out.
			card := botcard.NewCard(wbT(l, "sent_title", i18n.Args{"who": clip(wbWho(it), 20)}), botcard.Grey, wbT(l, "sent_title", i18n.Args{"who": wbWho(it)})).
				Markdown(botcard.EscapeMarkdown(text))
			if err != nil {
				card = botcard.NewCard(wbT(l, "send_failed_title"), botcard.Red, wbT(l, "send_failed_title")).Markdown(botcard.EscapeMarkdown(clip(oneLine(err.Error()), 200)))
			}
			if err := s.botUpdateCard(cctx, mid, card); err != nil {
				s.logf("bots: sent card: %v", err)
			}
			if list != "" && err == nil {
				listCard, _ := s.inboxCard(cctx)
				_ = s.botUpdateCard(cctx, list, listCard)
			}
		}()
		return toast("info", wbT(l, "sending"))
	}
	return toast("info", i18n.T(l, "assistant-feishu_cards.card_expired"))
}

// draftCard is a draft to edit and send as the owner; list is the Inbox card to redraw once it is sent.
func (s core) draftCard(ctx context.Context, it assistantwire.WorkbenchItem, text string, err error, list string) *botcard.Card {
	l := s.botLang(ctx)
	title := wbT(l, "draft_title", i18n.Args{"who": clip(wbWho(it), 20)})
	if err != nil || strings.TrimSpace(text) == "" {
		why := wbT(l, "draft_failed")
		if err != nil {
			why = wbT(l, "pair", i18n.Args{"a": why, "b": clip(oneLine(err.Error()), 200)})
		}
		return botcard.NewCard(title, botcard.Red, title).Markdown(botcard.EscapeMarkdown(why)).
			Buttons(botcard.Btn(wbT(l, "btn_draft"), botcard.Primary, map[string]any{"a": "ib", "op": "draft", "id": it.ID, "list": list}))
	}
	card := botcard.NewCard(title, botcard.Orange, title)
	if p := strings.TrimSpace(it.Preview); p != "" {
		card.Note(wbT(l, "draft_original", i18n.Args{"text": botcard.EscapeMarkdown(clip(p, 160))}))
	}
	box := botcard.Input("text", wbT(l, "draft_text"), "", true)
	box["default_value"] = text
	instr := botcard.Input("instruction", wbT(l, "draft_instruction"), wbT(l, "draft_instruction_placeholder"), false)
	version := it.ReplyVersion
	// Sending goes out under the owner's name: Feishu's dialog asks first. Both actions bind the exact AI card they
	// rendered; an action from a replaced suggestion must never confirm its old text against the new source position.
	card.Form("draft", []any{box, instr},
		botcard.Confirm(botcard.Submit(wbT(l, "btn_send"), wbFilled, "send_submit", map[string]any{"a": "ib", "op": "send", "id": it.ID, "list": list, "version": version}),
			wbT(l, "send_confirm_title"), wbT(l, "send_confirm_text", i18n.Args{"who": clip(wbWho(it), 20)})),
		botcard.Submit(wbT(l, "btn_redraft"), botcard.Default, "redraft_submit", map[string]any{"a": "ib", "op": "redraft", "id": it.ID, "list": list, "version": version}))
	reply := wbT(l, "draft_as_you")
	if it.Connector && it.ConnectorReplyTo != "" {
		reply = it.ConnectorReplyTo
	}
	card.Note(botcard.EscapeMarkdown(reply))
	return card
}

// wbClock is when, as a time that stays true on a card nobody redraws: the clock today, the date before.
func wbClock(loc *time.Location, t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	t, now = t.In(loc), now.In(loc)
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}

// wbAgentsLine says what the agents do, leaving out the states nobody is in.
func wbAgentsLine(l i18n.Lang, c wbCounts) string {
	var parts []string
	for _, p := range []struct {
		n   int
		key string
	}{{c.Working, "agents_working"}, {c.Unread, "agents_unread"}, {c.Idle, "agents_idle"}} {
		if p.n > 0 {
			parts = append(parts, wbT(l, p.key, i18n.Args{"count": p.n}))
		}
	}
	if len(parts) == 0 {
		return wbT(l, "agents_none")
	}
	return wbT(l, "agents_head") + strings.Join(parts, " · ")
}

func wbInboxLine(l i18n.Lang, c wbCounts) string {
	switch {
	case c.inboxNeed == 0 && c.inboxFYI == 0:
		return wbT(l, "inbox_clear")
	case c.inboxFYI == 0:
		return wbT(l, "inbox_need", i18n.Args{"count": c.inboxNeed})
	case c.inboxNeed == 0:
		return wbT(l, "inbox_fyi", i18n.Args{"count": c.inboxFYI})
	}
	return wbT(l, "inbox_need", i18n.Args{"count": c.inboxNeed}) + " · " + wbT(l, "inbox_fyi_short", i18n.Args{"count": c.inboxFYI})
}

// wbCounts is what the home card sums up: the agents and computers (the host's) and the open Inbox.
type wbCounts struct {
	assistantwire.WorkbenchCounts
	inboxNeed, inboxFYI int
	inboxTop            []assistantwire.WorkbenchItem
}

// workbenchCount is what the home card sums up now, and the agents at work (what needs the owner first).
func (s core) workbenchCount(ctx context.Context, now time.Time) (wbCounts, []assistantwire.WorkbenchAgent) {
	hc, busy := s.h.WorkbenchAgents(ctx, now)
	c := wbCounts{WorkbenchCounts: hc}
	for _, it := range s.workbenchInbox() {
		if wbNeedsYou(it) {
			c.inboxNeed++
			if len(c.inboxTop) < wbHomeRows {
				c.inboxTop = append(c.inboxTop, it)
			}
		} else {
			c.inboxFYI++
		}
	}
	return c, busy
}

// workbenchInbox is the open Inbox without its noise, what needs the owner first, newest first within.
func (s core) workbenchInbox() []assistantwire.WorkbenchItem { return s.h.WorkbenchInbox() }

// wbNeedsYou: the item is one the owner is to act on ("Needs you"), or urgent.
func wbNeedsYou(it assistantwire.WorkbenchItem) bool { return it.Tier == tierAction || it.Urgent }

// tierAction is the Inbox's "Needs you" tier.
const tierAction = "action"

// todayBody is what the Today card reads of the main conversation's "Today" (the core's today.go, as JSON).
type todayBody struct {
	Waiting  []struct{ Title string } `json:"waiting"`
	Working  int                      `json:"working"`
	Meetings []struct {
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
		Title string    `json:"title"`
	} `json:"meetings"`
	Done   []struct{ Title string } `json:"done"`
	Failed []struct {
		Title string `json:"title"`
		Why   string `json:"why"`
	} `json:"failed"`
	AtMe []struct {
		Name    string `json:"name"`
		Preview string `json:"preview"`
	} `json:"at_me"`
}

// toast is a card action's answer that says something.
func toast(kind, text string) *botcard.CardResponse {
	return &botcard.CardResponse{Toast: &botcard.Toast{Type: kind, Content: text}}
}

// wbT is a workbench string in l.
func wbT(l i18n.Lang, key string, args ...i18n.Args) string {
	return i18n.T(l, "assistant-feishu_workbench."+key, args...)
}
