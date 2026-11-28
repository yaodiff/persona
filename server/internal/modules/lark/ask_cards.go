package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// The cards of what Persona and the agents ask (asks.go), laid out from the interaction and what the core says of it
// (assistantwire.AskView): the card asking it, and the small grey card it becomes once it is over.

const (
	maxFormQuestions     = 6     // more questions than this are answered in the app (keeps the form under ~200 components)
	maxFormOptions       = 20    // options shown per question
	maxQuestionRunes     = 600   // runes of one question's text
	maxOptionDescRunes   = 150   // runes of one option's description
	maxOptionDescBudget  = 800   // runes of all option descriptions of one question
	maxDialogRunes       = 2000  // runes of a screen dialog's detail and question
	maxDialogOptions     = 8     // option buttons of a screen dialog
	maxScreenRunes       = 2500  // runes of the terminal screen kept
	maxScreenLineRunes   = 300   // runes per terminal line
	maxPermissionReason  = 1000  // runes of a permission's reason
	maxPlanRunes         = 5000  // runes of a plan shown on its card (clipPlan)
	maxAnsweredQuestions = 3     // questions shown on an answered card
	maxAnsweredQRunes    = 150   // runes per question on an answered card
	maxCardJSONBytes     = 24000 // serialized size above which safeCard falls back (Feishu rejects ~30 KB)
)

// interactionCard is the card asking ix, with the app's choices; v is what the core says of it (Host.AskView).
func interactionCard(l i18n.Lang, ix proto.Interaction, v assistantwire.AskView) *botcard.Card {
	place, secondary := feishuAgentPlace(v)
	place = ixTaskPlace(v, place)
	where := botcard.EscapeMarkdown(place)
	val := func(extra map[string]any) map[string]any {
		v := map[string]any{"a": "ix", "id": ix.ID}
		if !ix.CreatedAt.IsZero() {
			v["created_at"] = ix.CreatedAt.Format(time.RFC3339Nano)
		}
		for k, x := range extra {
			v[k] = x
		}
		return v
	}
	open := openPage(v)
	// The "more" menu every card ends its row of actions with: dismissing it where it can be, and its conversation.
	menu := func(dismiss bool) botcard.Button {
		var items []botcard.MenuItem
		if dismiss {
			items = append(items, botcard.MenuItem{Label: i18n.T(l, "assistant-feishu_cards.dismiss"), Value: map[string]any{"dismiss": "1"}})
		}
		if label, web := v.OpenLabel, v.OpenURL; web != "" {
			items = append(items, botcard.MenuItem{Label: label, URL: web, PC: botcard.SidebarURL(web)})
		}
		return botcard.Overflow(val(nil), items...)
	}
	switch ix.Kind {
	case proto.IxQuestion:
		c := ixCard(l, v, "assistant-feishu_cards.tag_question", i18n.T(l, "assistant-feishu_cards.question_summary", i18n.Args{"question": firstQuestion(ix)}))
		if len(ix.Questions) == 1 && !ix.Questions[0].MultiSelect && len(ix.Questions[0].Options) <= 4 {
			q := ix.Questions[0]
			c.Markdown(questionText(l, q))
			var bs []botcard.Button
			submit := botcard.Primary // the card's one primary: the recommended option, else the written answer
			for _, o := range q.Options {
				style := botcard.Default
				if o.Label == q.Recommend { // what the asker recommends is the one to press
					style, submit = botcard.Primary, botcard.Default
				}
				bs = append(bs, botcard.Btn(clip(o.Label, 30), style, val(map[string]any{"q": "0", "v": o.Label})))
			}
			c.Buttons(append(bs, menu(true))...)
			c.Form("other", []any{botcard.Input("text", "", i18n.T(l, "assistant-feishu_cards.or_write_answer"), false)},
				botcard.Submit(i18n.T(l, "assistant-feishu_cards.answer"), submit, "q_submit", val(map[string]any{"q": "0", "free": "1"})))
			if len(q.Options) > 0 {
				c.Note(i18n.T(l, "assistant-feishu_cards.answer_by_text"))
			}
			return safeCard(l, c, open)
		} else if len(ix.Questions) > maxFormQuestions {
			// The submit handler needs an answer to every question, so a form clipped to the first few could never be
			// sent: that many questions are answered in the app.
			c.Note(i18n.T(l, "assistant-feishu_cards.questions_in_app", i18n.Args{"count": len(ix.Questions)}))
			if open != nil {
				open["type"] = botcard.Primary
			}
			return safeCard(l, c.Buttons(open, menu(true)), open)
		} else {
			var fields []any
			for i, q := range ix.Questions {
				fields = append(fields, botcard.MarkdownBlock(questionText(l, q)))
				var opts []botcard.Option
				for j, o := range q.Options {
					if j >= maxFormOptions {
						break
					}
					opts = append(opts, botcard.Option{Label: clip(o.Label, 60), Value: o.Label})
				}
				if len(opts) > 0 {
					fields = append(fields, botcard.Select("q"+strconv.Itoa(i), map[bool]string{false: i18n.T(l, "assistant-feishu_cards.pick_one"), true: i18n.T(l, "assistant-feishu_cards.pick_some")}[q.MultiSelect], opts, q.MultiSelect))
				}
				fields = append(fields, botcard.Input("t"+strconv.Itoa(i), "", i18n.T(l, "assistant-feishu_cards.or_write"), false))
			}
			c.Form("answers", fields, botcard.Submit(i18n.T(l, "assistant-feishu_cards.submit"), botcard.Primary, "qs_submit", val(map[string]any{"form": "1"})))
			// A form of one question with options also takes a plain reply; a form of several is submitted as one (a
			// reply is not split over its questions), and a free-text question has no options.
			if len(ix.Questions) == 1 && len(ix.Questions[0].Options) > 0 {
				c.Note(i18n.T(l, "assistant-feishu_cards.answer_by_text"))
			}
		}
		return safeCard(l, c.Buttons(menu(true)), open)
	case proto.IxPlan:
		c := ixCard(l, v, "assistant-feishu_cards.tag_plan", i18n.T(l, "assistant-feishu_cards.plan_summary"))
		c.Markdown(botcard.CardMarkdown(clipPlan(ix.Plan, i18n.T(l, "assistant-feishu_cards.plan_clipped"))))
		// Approving to run on its own lets the agent go on without asking: Feishu's dialog asks first.
		c.Buttons(
			botcard.Confirm(botcard.Btn(i18n.T(l, "assistant-feishu_cards.approve_run"), botcard.Primary, val(map[string]any{"approve": "1", "mode": "bypassPermissions"})),
				i18n.T(l, "assistant-feishu_cards.confirm_run_title"), i18n.T(l, "assistant-feishu_cards.confirm_run_text")),
			botcard.Btn(i18n.T(l, "assistant-feishu_cards.approve_edits"), botcard.Default, val(map[string]any{"approve": "1", "mode": "acceptEdits"})),
			menu(false),
		)
		c.Form("reject", []any{botcard.Input("feedback", "", i18n.T(l, "assistant-feishu_cards.feedback_placeholder"), true)},
			botcard.Submit(i18n.T(l, "assistant-feishu_cards.send_back"), botcard.Danger, "plan_reject", val(map[string]any{"approve": "0"})))
		return safeCard(l, c, open)
	case proto.IxPermission:
		c := ixCard(l, v, "assistant-feishu_cards.tag_permission", i18n.T(l, "assistant-feishu_cards.permission_summary", i18n.Args{"tool": v.Tool}))
		var b strings.Builder
		fmt.Fprintf(&b, "**%s**", botcard.EscapeMarkdown(v.Tool))
		if ix.Reason != "" {
			fmt.Fprintf(&b, "\n%s", botcard.EscapeMarkdown(clip(ix.Reason, maxPermissionReason)))
		}
		c.Markdown(b.String())
		if sum := strings.TrimSpace(ix.Summary); sum != "" {
			if !v.ExactProposal {
				sum = clip(sum, 2500)
			}
			c.Markdown("```\n" + strings.ReplaceAll(sum, "```", "'''") + "\n```")
		} else if in := proto.RedactArgs(ix.ToolInput); in != "" { // a credential's value never goes to the chat (#4734)
			c.Markdown("```json\n" + strings.ReplaceAll(clip(in, 2500), "```", "'''") + "\n```")
		}
		if ix.CanClarify && !ix.Private {
			// Question-only form: no Resolution.Values, Allow or Deny travels with
			// it. Field-bearing cards keep their fields App-only but may still ask.
			c.Form("clarify", []any{botcard.Input("question", "", i18n.T(l, "assistant-feishu_cards.question_placeholder"), false)},
				botcard.Submit(i18n.T(l, "assistant-feishu_cards.have_question"), botcard.Default, "permission_clarify", val(map[string]any{"clarify": "1"})))
		}
		if len(ix.Fields) > 0 {
			// A password or a code is typed in the app, never in a chat.
			c.Note(i18n.T(l, "assistant-feishu_cards.fields_in_app"))
			if open != nil {
				open["type"] = botcard.Primary
			}
			return safeCard(l, c.Buttons(open), open)
		}
		bs := []botcard.Button{
			botcard.Btn(i18n.T(l, "assistant-feishu_cards.allow"), botcard.Primary, val(map[string]any{"allow": "1"})),
			botcard.Btn(i18n.T(l, "assistant-feishu_cards.deny"), botcard.Danger, val(map[string]any{"allow": "0"})),
		}
		// As the app's card (InteractionViews.swift's BUIPermissionForm, web's PermissionCard): Always allow for this session for
		// Claude on a conversation, and for another agent only what it says that always allows — a runtime cannot keep
		// Claude's session rule (node/runtimes.go), so the button there would only allow once and ask again.
		label := ix.AlwaysLabel
		if label == "" && (ix.Agent == "" || ix.Agent == "claude") && ix.SessionID != "" {
			label = i18n.T(l, "assistant-feishu_cards.always_allow")
		}
		// The manager's word wins where it gave one (#3590): no button that would only allow once.
		if kept := ix.AlwaysHonored; kept != nil && !*kept {
			label = ""
		} else if kept != nil && label == "" {
			label = i18n.T(l, "assistant-feishu_cards.always_allow")
		}
		if label != "" {
			// A permission that stays for the session asks first.
			bs = append(bs, botcard.Confirm(botcard.Btn(clip(label, 20), botcard.Default, val(map[string]any{"allow": "1", "always": "1"})),
				i18n.T(l, "assistant-feishu_cards.confirm_always_title"), i18n.T(l, "assistant-feishu_cards.confirm_always_text")))
		}
		return safeCard(l, c.Buttons(append(bs, menu(false))...), open)
	case proto.IxScreen:
		summary := i18n.T(l, "assistant-feishu_cards.screen_summary")
		if v.Terminal {
			summary = ix.Dialog.Question + " · " + place
		}
		c := ixCard(l, v, "assistant-feishu_cards.tag_screen", summary)
		if d := ix.Dialog; d != nil {
			q := d.Question
			if d.Detail != "" {
				q = d.Detail + "\n" + q
			}
			if r := []rune(q); len(r) > maxDialogRunes {
				q = string(r[:maxDialogRunes]) + i18n.T(l, "assistant-feishu_cards.clipped")
			}
			c.Markdown(botcard.EscapeMarkdown(q))
			var bs []botcard.Button
			for _, o := range d.Options {
				if o.Kind == "type_something" || o.Kind == "chat" {
					continue
				}
				if len(bs) >= maxDialogOptions {
					break
				}
				bs = append(bs, botcard.Btn(clip(o.Label, 30), botcard.Default, val(map[string]any{"opt": o.Key})))
			}
			c.Buttons(append(bs, menu(false))...)
		} else {
			screen := ix.Screen
			lines := strings.Split(screen, "\n")
			if len(lines) > 20 {
				lines = lines[len(lines)-20:]
			}
			for i, line := range lines {
				if r := []rune(line); len(r) > maxScreenLineRunes {
					lines[i] = string(r[:maxScreenLineRunes]) + "…"
				}
			}
			screen = strings.Join(lines, "\n")
			if r := []rune(screen); len(r) > maxScreenRunes {
				screen = "…\n" + string(r[len(r)-maxScreenRunes:])
			}
			c.Markdown("```\n" + strings.ReplaceAll(screen, "```", "'''") + "\n```")
			c.Buttons(
				botcard.Btn(i18n.T(l, "assistant-feishu_cards.enter"), botcard.Default, val(map[string]any{"keys": "enter"})),
				botcard.Btn("Esc", botcard.Default, val(map[string]any{"keys": "esc"})),
				botcard.Btn("1", botcard.Default, val(map[string]any{"keys": "1"})),
				botcard.Btn("2", botcard.Default, val(map[string]any{"keys": "2"})),
				menu(false),
			)
		}
		if !ix.Secret {
			c.Form("type", []any{botcard.Input("text", "", i18n.T(l, "assistant-feishu_cards.type_placeholder"), false)},
				botcard.Submit(i18n.T(l, "assistant-feishu_cards.send"), botcard.Primary, "screen_text", val(map[string]any{"typed": "1"})))
		} else {
			c.Note(i18n.T(l, "assistant-feishu_cards.secret_in_app"))
		}
		return safeCard(l, c, open)
	case proto.IxElicitation:
		return safeCard(l, elicitationCard(l, ix, v, open, menu(false)), open)
	}
	return safeCard(l, botcard.NewCard(where, botcard.Grey, "").Subtitle(secondary).Buttons(open), open)
}

// clipPlan is a plan longer than maxPlanRunes cut to its first channel.Split piece, then note. The piece ends at a line
// break where it can and closes a code block it cuts into, so the note reads as text, not as the code's last line.
func clipPlan(plan, note string) string {
	if len([]rune(plan)) <= maxPlanRunes {
		return plan
	}
	head := channel.Split(plan, maxPlanRunes)[0]
	if r := []rune(head); len(r) > maxPlanRunes { // Split hands back a plan of only line breaks whole
		head = string(r[:maxPlanRunes])
	}
	return head + note
}

// safeCard returns c, or, when it would still be too big for Feishu after the clipping above, a short card that sends
// the owner to the app (with the open button, if any).
func safeCard(l i18n.Lang, c *botcard.Card, open botcard.Button) *botcard.Card {
	if len(c.JSON()) <= maxCardJSONBytes {
		return c
	}
	title := ""
	if c.Header != nil {
		title = c.Header.Title.Content
	}
	fallback := botcard.NewCard(title, botcard.Orange, "")
	fallback.Note(i18n.T(l, "assistant-feishu_cards.too_long_in_app"))
	if len(open) > 0 {
		fallback.Buttons(open)
	}
	return fallback
}

// elicitationTarget is the link an elicitation may open and its host: an absolute http(s) URL with a host and no
// user or password (like the apps' urlTarget, web/src/lib/elicitation.ts), exactly as given; "" otherwise. A link
// such as https://github.com@evil.example/ reads as one site and opens another.
func elicitationTarget(raw string) (string, string) {
	target := strings.TrimSpace(raw)
	if !elicitationURLRe.MatchString(target) {
		return "", ""
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", ""
	}
	return target, u.Hostname()
}

// elicitationURLRe: http(s):// and then a host at once (not https:///host or https:host, which parsers forgive).
var elicitationURLRe = lazyre.New(`(?i)^https?://[^/\\?#@]`)

// elicitationCard is an MCP server asking to authorize (a link) or to fill in a form: what it asks on the card, its
// link as a button; a form is filled in the app (its values may be secrets), so only its fields' names are shown.
func elicitationCard(l i18n.Lang, ix proto.Interaction, v assistantwire.AskView, open, menu botcard.Button) *botcard.Card {
	var msg, link, host string
	var fields []string
	if e := ix.Elicitation; e != nil {
		msg = strings.TrimSpace(e.Message)
		link, host = elicitationTarget(e.URL)
		var schema struct {
			Properties map[string]struct {
				Title string `json:"title"`
			} `json:"properties"`
		}
		if json.Unmarshal(e.RequestedSchema, &schema) == nil {
			for k, p := range schema.Properties {
				if t := strings.TrimSpace(p.Title); t != "" {
					k = t
				}
				fields = append(fields, k)
			}
			slices.Sort(fields)
		}
	}
	c := ixCard(l, v, "assistant-feishu_cards.tag_elicitation",
		i18n.T(l, "assistant-feishu_cards.elicitation_summary", i18n.Args{"message": clip(oneLine(msg), 60)}))
	if msg != "" {
		c.Markdown(botcard.EscapeMarkdown(clip(msg, 2000)))
	}
	if link != "" {
		// The host on its own line: the link's text could look like another site's (as the apps show it).
		c.Markdown(i18n.T(l, "assistant-feishu_cards.elicitation_host", i18n.Args{"host": botcard.Code(host)}))
		c.Note(i18n.T(l, "assistant-feishu_cards.elicitation_confirm_in_app"))
		authorize := botcard.LinkBtn(i18n.T(l, "assistant-feishu_cards.elicitation_open"), link)
		authorize["type"] = botcard.Primary
		return c.Buttons(authorize, menu)
	}
	if len(fields) > 0 {
		c.Note(i18n.T(l, "assistant-feishu_cards.elicitation_fields", i18n.Args{"fields": clip(strings.Join(fields, "、"), 200)}))
	}
	c.Note(i18n.T(l, "assistant-feishu_cards.elicitation_in_app"))
	if open != nil {
		open["type"] = botcard.Primary
	}
	return c.Buttons(open)
}

func firstQuestion(ix proto.Interaction) string {
	if len(ix.Questions) > 0 {
		return ix.Questions[0].Question
	}
	return ""
}

func questionText(l i18n.Lang, q proto.Question) string {
	s := ""
	if q.Header != "" {
		s = "**" + botcard.EscapeMarkdown(clip(q.Header, 60)) + "**\n"
	}
	s += botcard.EscapeMarkdown(clip(q.Question, maxQuestionRunes))
	descBudget := maxOptionDescBudget
	clippedOptions := 0
	for i, o := range q.Options {
		if i >= maxFormOptions {
			clippedOptions = len(q.Options) - i
			break
		}
		if o.Description != "" && descBudget > 0 {
			desc := clip(o.Description, maxOptionDescRunes)
			if len([]rune(desc)) > descBudget {
				desc = clip(desc, descBudget)
			}
			descBudget -= len([]rune(desc))
			s += "\n- " + botcard.EscapeMarkdown(clip(o.Label, 40)) + "：" + botcard.EscapeMarkdown(desc)
		}
	}
	if clippedOptions > 0 {
		s += i18n.T(l, "assistant-feishu_cards.clipped")
	}
	return s
}

// ixKindTags are the tags that say what an interaction's card is.
var ixKindTags = map[string]string{proto.IxQuestion: "assistant-feishu_cards.tag_question", proto.IxPlan: "assistant-feishu_cards.tag_plan",
	proto.IxPermission: "assistant-feishu_cards.tag_permission", proto.IxScreen: "assistant-feishu_cards.tag_screen",
	proto.IxElicitation: "assistant-feishu_cards.tag_elicitation"}

// ixWho is who asks, for a card's header: the task its agent works on, with the task's direction (AskView.Task), or
// else the agent; and under it the agent, its computer and the session's other name, the parts it has.
func ixWho(v assistantwire.AskView) (title, sub string) {
	agent, machine, secondary := v.Agent, v.Machine, v.Secondary
	task, same := v.Task, v.TaskSame
	switch {
	case task == "":
		return agent, dotJoin(machine, secondary)
	case same: // the agent is named after its task: the direction before it
		return task + agent, dotJoin(machine, secondary)
	}
	return task, dotJoin(agent, machine, secondary)
}

// ixCard starts the card asking ix: who asks in the header, orange (the owner is to act), its kind as a tag.
func ixCard(l i18n.Lang, v assistantwire.AskView, kind, summary string) *botcard.Card {
	title, sub := ixWho(v)
	return botcard.NewCard(title, botcard.Orange, summary).Subtitle(sub).Tag(i18n.T(l, kind), botcard.Orange)
}

// answeredCard is an interaction's card once it is over: grey and small, what it asked in a line and how it ended,
// its conversation's page as a link in small text; no buttons.
func (s core) answeredCard(ctx context.Context, ix proto.Interaction, how string) *botcard.Card {
	return answeredCardOf(s.botLang(ctx), ix, s.h.AskView(ctx, ix), how)
}

// answeredCardOf is answeredCard from what the core says of ix.
func answeredCardOf(l i18n.Lang, ix proto.Interaction, v assistantwire.AskView, how string) *botcard.Card {
	title, sub := ixWho(v)
	c := botcard.NewCard(title, botcard.Grey, how).Subtitle(sub)
	if kind := ixKindTags[ix.Kind]; kind != "" {
		c.Tag(i18n.T(l, kind), botcard.Neutral)
	}
	var asked []string
	switch ix.Kind {
	case proto.IxQuestion:
		qs := ix.Questions
		more := 0
		if len(qs) > maxAnsweredQuestions {
			more = len(qs) - maxAnsweredQuestions
			qs = qs[:maxAnsweredQuestions]
		}
		for _, q := range qs {
			asked = append(asked, botcard.EscapeMarkdown(clip(oneLine(q.Question), maxAnsweredQRunes)))
		}
		if more > 0 {
			asked = append(asked, strings.TrimSpace(i18n.T(l, "assistant-feishu_cards.clipped")))
		}
	case proto.IxPermission:
		asked = append(asked, botcard.EscapeMarkdown(v.Tool+" "+clip(oneLine(ix.Summary), 200)))
	case proto.IxPlan:
		asked = append(asked, botcard.EscapeMarkdown(clip(textfmt.PlanTitle(ix.Plan), 200)))
	}
	if len(asked) > 0 {
		c.Markdown("<font color='grey'>" + strings.Join(asked, "\n") + "</font>")
	}
	c.Markdown("**✓ " + botcard.EscapeMarkdown(how) + "**")
	// The conversation's page rides on the card as a link, and the button on safeCard's short card, which takes its
	// place when this one would exceed Feishu's size budget.
	if label, web := v.OpenLabel, v.OpenURL; web != "" && v.OpenInline {
		c.Meta("[" + botcard.EscapeMarkdown(label) + "](" + web + ")")
	}
	return safeCard(l, c, openPage(v))
}

// openPage is the button that opens the conversation's page; nil (Card.Buttons leaves it out) when the hub has no
// http(s) address for it.
func openPage(v assistantwire.AskView) botcard.Button {
	if v.OpenURL == "" {
		return nil
	}
	return botcard.PageBtn(v.OpenLabel, v.OpenURL)
}

func ixWhere(v assistantwire.AskView) string {
	place, _ := feishuAgentPlace(v)
	return ixTaskPlace(v, place)
}

func ixTaskPlace(v assistantwire.AskView, place string) string {
	task, same := v.Task, v.TaskSame
	switch {
	case task == "":
		return place
	case same:
		return task + place
	}
	return task + " · " + place
}

// feishuAgentPlace identifies the session even when the node supplied no title.
func feishuAgentPlace(v assistantwire.AskView) (place, secondary string) {
	agent, machine, secondary := v.Agent, v.Machine, v.Secondary
	if machine == "" {
		return agent, secondary
	}
	return agent + " · " + machine, secondary
}
