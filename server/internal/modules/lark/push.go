package lark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	pushTimeout   = 30 * time.Second
	mirrorRetry   = 30 * time.Second
	mirrorRetries = 3
)

// PushKeyed sends an ordinary push. A key keeps each piece idempotent. Only a failure worth retrying
// is returned to the caller.
func (l *Layer) PushKeyed(ctx context.Context, to assistantwire.SessionRef, title, body, key string, receipt assistantwire.LarkPushCopy) error {
	return l.chat().postPush(ctx, to, title, body, key, receipt, nil)
}

type todoReminderSend struct {
	todo         string
	private      bool
	pairing      assistantwire.LarkReminderPairing
	botAttempted bool
}

// TodoReminder retries just the Feishu copy of a claimed occurrence within the mirror's retry bounds.
// A delayed callback must not replay an ambiguous send after the provider's one-hour UUID retention.
func (l *Layer) TodoReminder(to assistantwire.SessionRef, reminder assistantwire.LarkReminder) {
	s := l.chat()
	expires := time.Now().Add(time.Hour).Round(0)
	send := &todoReminderSend{todo: reminder.Todo}
	at, scheduled := reminder.Schedule.Occurrence()
	key := ""
	if scheduled {
		// Identity is the occurrence and card index. Later reminders and other to-dos get independent keys.
		sum := sha256.Sum256([]byte(reminder.Todo + "\x00" + at.UTC().Format(time.RFC3339Nano)))
		key = "t" + hex.EncodeToString(sum[:16]) + ":"
	}
	for attempt := 0; time.Now().Before(expires); attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
		title, body, private := reminder.Copy(ctx)
		send.private = private
		err := s.postPush(ctx, to, title, body, key, assistantwire.LarkPushCopy{Delivered: reminder.Delivered}, send)
		cancel()
		if err == nil || botcard.Refused(err) {
			return
		}
		if attempt >= mirrorRetries || !scheduled {
			s.logf("bots: todo reminder: given up after %d tries: %v", attempt+1, err)
			return
		}
		<-time.After(s.timing("mirror_retry"))
		if !reminder.Current() {
			return // completed, deleted or re-armed while this occurrence waited
		}
	}
}

// pushStatus distinguishes an unknown status from an off bot. A temporary failure is never permission to post.
func (s core) pushStatus(ctx context.Context) (FeishuStatus, error) {
	st, err := s.feishuStatus(ctx)
	s.workbenchOn(st.Features, err == nil && st.Ready)
	if err != nil && s.h.Answered(err) {
		err = nil
	}
	return st, err
}

func (s core) postPush(ctx context.Context, to assistantwire.SessionRef, title, body, key string, receipt assistantwire.LarkPushCopy, reminder *todoReminderSend) error {
	st, err := s.pushStatus(ctx)
	if err != nil {
		return err
	}
	if !st.Ready || !st.Features.Pushes {
		return nil
	}
	if reminder != nil {
		if st.Owner == "" || st.PairedAt.IsZero() {
			return nil // no complete pairing identity authorizes a reminder
		}
		if reminder.pairing.Owner == "" {
			reminder.pairing = assistantwire.LarkReminderPairing{Owner: st.Owner, PairedAt: st.PairedAt}
		} else if reminder.pairing.Owner != st.Owner || !reminder.pairing.PairedAt.Equal(st.PairedAt) {
			return nil
		}
		ctx = assistantwire.WithLarkReminderPairing(ctx, reminder.pairing)
	}
	if receipt.Started != nil {
		receipt.Started()
	}
	// The body is laid out like the bot's answers. The push's title heads the first card, the open button goes under the last.
	lang := s.botLang(ctx)
	ctx = s.sendAs(ctx, "push", to.MachineID, to.SessionID)
	cards := answerCards(lang, s.linkNamer(ctx), body, false, nil, "")
	head := botcard.NewCard("", "", title)
	for i, ac := range cards {
		if i == 0 {
			// The push's title heads the card, blue (only to know), its kind as a tag. The body's heading and status remain.
			ac.card.Retitle("", title, botcard.Blue)
			ac.card.Config = head.Config
			kind := "assistant-feishu_cards.tag_notice"
			if reminder != nil {
				kind = "assistant-feishu_cards.tag_todo"
			}
			ac.card.Tag(i18n.T(lang, kind), botcard.Neutral)
		}
		// A generic space notice must not identify the private to-do through its navigation metadata.
		if i == len(cards)-1 && to.SessionID != "" {
			switch {
			case reminder == nil:
				ac.card.AppendToLinks(s.openButton(ctx, to.MachineID, to.SessionID))
			case !reminder.private:
				web := s.h.TodoLink(strings.TrimRight(st.PublicURL, "/"), reminder.todo)
				ac.card.AppendToLinks(s.h.OpenLinkButton(ctx, web))
			}
		}
	}
	if (reminder == nil || !reminder.botAttempted) && s.divertAnswer(ctx, assistantwire.NoticePush(), cards) {
		if receipt.Delivered != nil {
			receipt.Delivered()
		}
		return nil
	}
	var missing []int // the pieces that did not get out (1-based): said at the end
	var again error   // the first that may get out when sent again
	for i, ac := range cards {
		k := ""
		if key != "" {
			k = key + strconv.Itoa(i)
		}
		if reminder != nil {
			reminder.botAttempted = true
		}
		mid, err := s.botPostTo(ctx, feishuTarget{}, "", ac.card, ac.fallback, k)
		if err != nil {
			s.logf("bots: push: %v", err)
			var refused *rpc.Error
			if reminder != nil && errors.As(err, &refused) && refused.Code == assistantwire.CodeChatDenied {
				return nil // the actual route definitively rejected the original pairing
			}
			if again == nil && !botcard.Refused(err) {
				again = err
			}
			if i == 0 {
				return again // nothing went out, so the turn-end mirror can still deliver the turn
			}
			missing = append(missing, i+1)
			continue
		}
		if i == 0 && receipt.Delivered != nil {
			receipt.Delivered()
		}
		if to.SessionID != "" {
			target := botTarget{Kind: targetPersona, MachineID: to.MachineID, SessionID: to.SessionID}
			if reminder != nil && !reminder.private {
				target.TodoID = reminder.todo
			}
			s.rememberBotThread(mid, target, title)
		}
	}
	if reminder == nil && len(missing) > 0 && (key == "" || again == nil) {
		s.botSay(ctx, missingPiecesNote(lang, missing))
	}
	return again
}

func (s core) divertAnswer(ctx context.Context, kind assistantwire.NoticeKind, cards []botAnswerCard) bool {
	cs, fs := answerCardParts(cards)
	return s.divertNotices(ctx, kind, cs, fs)
}

// Mirror lays out and sends a turn that ended without being asked for in Feishu.
func (l *Layer) Mirror(ctx context.Context, to assistantwire.LarkMirror, lang i18n.Lang, turn assistantwire.LarkMirrorTurn) {
	s := l.chat()
	foot := to.Label
	if turn.Asked != "" && to.Main {
		foot = i18n.T(lang, "assistant-feishu_cards.asked_elsewhere", i18n.Args{"asked": turn.Question}) + "\n" + foot
	}
	cards := answerCards(lang, s.linkNamer(ctx), turn.Answer, false, nil, foot)
	if len(cards) > 0 {
		cards[len(cards)-1].card.AppendToLinks(s.openButton(ctx, to.MachineID, to.SessionID))
	}
	// Two turns that asked and answered alike still need independent keys.
	sum := sha256.Sum256([]byte(to.MachineID + "\x00" + to.SessionID + "\x00" + turn.Turn + "\x00" + turn.Asked + "\x00" + turn.Answer))
	s.postMirror(ctx, mirrorSend{to: to, lang: lang, cards: cards, key: "m" + hex.EncodeToString(sum[:8])}, 0)
}

// MirrorPost sends briefing cards already laid out by Core, using the same send and retry policy as a turn mirror.
func (l *Layer) MirrorPost(ctx context.Context, to assistantwire.LarkMirror, lang i18n.Lang, cards []*botcard.Card, fallbacks []string, key string, sent func()) {
	out := make([]botAnswerCard, len(cards))
	for i, card := range cards {
		out[i] = botAnswerCard{card: card, fallback: fallbacks[i]}
	}
	l.chat().postMirror(ctx, mirrorSend{to: to, lang: lang, cards: out, key: key, sent: sent}, 0)
}

// mirrorSend keeps the rendered cards and keys across retry attempts.
type mirrorSend struct {
	to    assistantwire.LarkMirror
	lang  i18n.Lang
	cards []botAnswerCard
	key   string
	sent  func()
}

// postMirror sends each card with a stable key. If the first fails temporarily, the same cards go again
// within the retry budget. Later pieces that fail are named at the end.
func (s core) postMirror(ctx context.Context, m mirrorSend, attempt int) {
	kind := "task"
	if m.to.Main {
		kind = "sync"
	}
	ctx = s.sendAs(ctx, kind, m.to.MachineID, m.to.SessionID)
	threadChat := string(m.to.ThreadChatID)
	threadID := string(m.to.ThreadID)
	threadDelivery := threadChat != "" && threadID != ""
	postTo := feishuTarget{}
	replyTo := ""
	if threadDelivery {
		st, err := s.feishuStatusKept(ctx)
		if err != nil || !st.Ready {
			s.logf("bots: mirror to thread %s/%s: bot is not ready (%v)", threadChat, threadID, err)
			return
		}
		if !slices.Contains(st.Chats, threadChat) {
			s.logf("bots: mirror to thread %s/%s: chat is not opted in", threadChat, threadID)
			return
		}
		replyTo = s.topicMessage(ctx, threadChat, threadID)
		if replyTo == "" && strings.HasPrefix(threadID, "om_") {
			replyTo = threadID
		}
		if replyTo == "" {
			s.logf("bots: mirror to thread %s/%s: no anchor message known to reply to", threadChat, threadID)
			return
		}
		postTo = feishuTarget{ChatID: threadChat, InThread: true, NoDetour: true}
	} else if !m.to.Main && s.divertAnswer(ctx, assistantwire.NoticeTask(), m.cards) {
		// A scheduled task's result can go to a notifications companion. Main turns stay in the chat where a reply continues them.
		if m.sent != nil {
			m.sent()
		}
		return
	}
	target := botTarget{Kind: targetPersona, MachineID: m.to.MachineID, SessionID: m.to.SessionID}
	var missing []int
	for i, ac := range m.cards {
		mid, err := s.botPostTo(ctx, postTo, replyTo, ac.card, ac.fallback, m.key+strconv.Itoa(i))
		if err != nil {
			s.logf("bots: mirror: %v", err)
			if threadDelivery && botcard.MessageGone(err) {
				s.forgetTopicMessage(ctx, threadChat, threadID, replyTo)
				return
			}
			if i == 0 {
				// The rest would read as an answer without its beginning.
				if botcard.Refused(err) || attempt >= mirrorRetries {
					s.logf("bots: mirror: given up after %d tries", attempt+1)
					return
				}
				s.retryMirror(m, attempt+1)
				return
			}
			missing = append(missing, i+1)
			continue
		}
		s.rememberBotThread(mid, target, m.to.Label) // a reply to any of them goes to this conversation
		if threadDelivery {
			s.rememberTopicMessage(ctx, threadChat, threadID, mid)
			if i == 0 {
				s.rememberTopicSession(ctx, threadChat, threadID, target, m.to.Label)
			}
		}
		if i == 0 && m.sent != nil {
			m.sent()
		}
	}
	if len(missing) > 0 {
		if threadDelivery {
			s.botNoteTo(ctx, postTo, replyTo, missingPiecesNote(m.lang, missing))
		} else {
			s.botSay(ctx, missingPiecesNote(m.lang, missing))
		}
	}
}

// retryMirror consumes the same bounded retry slots for status transport failures as for failed posts.
func (s core) retryMirror(m mirrorSend, attempt int) {
	time.AfterFunc(s.timing("mirror_retry"), func() {
		ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
		defer cancel()
		st, err := s.pushStatus(ctx)
		if err != nil {
			s.logf("bots: mirror retry status: %v", err)
			if attempt < mirrorRetries {
				s.retryMirror(m, attempt+1)
			}
			return
		}
		f := st.Features
		threadDelivery := m.to.ThreadChatID != "" && m.to.ThreadID != ""
		if !st.Ready || m.to.Main && !f.Sync || !m.to.Main && !f.Pushes && !threadDelivery {
			return
		}
		if m.to.StillDue(ctx, f.Pushes || threadDelivery) {
			s.postMirror(ctx, m, attempt)
		}
	})
}

// AgentDone posts the one Feishu card for a background Agent's finished turn. It asks for the content
// only with pushes on, so Core can reuse its notification and privacy decisions.
func (l *Layer) AgentDone(to assistantwire.SessionRef, view func(context.Context) (assistantwire.LarkAgentDone, bool)) {
	s := l.chat()
	ctx, cancel := context.WithTimeout(s.sendAs(context.Background(), "agent_done", to.MachineID, to.SessionID), pushTimeout)
	defer cancel()
	f, ready := s.feishuFeatures(ctx)
	if !ready || !f.Pushes {
		return
	}
	v, ok := view(ctx)
	if !ok {
		return
	}
	card, fallback := agentDoneCard(s.botLang(ctx), v.Head, v.Text, v.Place, v.Secondary, v.AwaitsReply, s.openButton(ctx, to.MachineID, to.SessionID))
	mid, err := s.botPost(ctx, "", card, fallback)
	if err != nil {
		s.logf("bots: agent done: %v", err)
		return
	}
	// A reply to the card talks to that agent, as with one picked with /agents.
	s.rememberBotThread(mid, botTarget{Kind: targetAgent, MachineID: to.MachineID, SessionID: to.SessionID}, v.ThreadTitle)
}

// agentDoneClip bounds the result text in a background agent's finished card; the rest is read in the app, so the
// notice is always one card. agentDoneLead is about how much of it shows open, the rest folded under it.
const (
	agentDoneClip = 1000
	agentDoneLead = 300
)

// agentDoneState is a finished turn's card as its push decided it (doneAlert): its title without the push's mark, the
// header's colour and the tag that says the state — failed or cut off red, blocked or asking the owner orange, done
// green.
func agentDoneState(head string, awaitsReply bool) (title, color, tag string) {
	switch {
	case strings.HasPrefix(head, "⚠️"):
		return strings.TrimSpace(strings.TrimPrefix(head, "⚠️")), botcard.Red, "assistant-feishu_cards.agent_tag_failed"
	case strings.HasPrefix(head, "⏳"):
		return strings.TrimSpace(strings.TrimPrefix(head, "⏳")), botcard.Orange, "assistant-feishu_cards.agent_tag_blocked"
	case awaitsReply:
		return head, botcard.Orange, "assistant-feishu_cards.agent_tag_asks"
	}
	return head, botcard.Green, "assistant-feishu_cards.agent_tag_done"
}

// leadAndRest cuts Markdown at the first paragraph break past n runes outside code: what shows open and what is folded
// ("" when there is no such break).
func leadAndRest(md string, n int) (lead, rest string) {
	lines := strings.Split(md, "\n")
	runes, fence := 0, ""
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(t, fence) {
				fence = ""
			}
		} else if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = t[:3]
		} else if t == "" && runes >= n && i < len(lines)-1 {
			if r := strings.TrimSpace(strings.Join(lines[i+1:], "\n")); r != "" {
				return strings.TrimSpace(strings.Join(lines[:i], "\n")), r
			}
		}
		runes += len([]rune(line))
	}
	return md, ""
}

// agentDoneCard is a background agent's finished card and its plain-text fallback: the push's head and text, the
// place it ran, the session's other name (secondary, "" for none), and open (the conversation's
// button, nil without a web address) as its one button.
func agentDoneCard(l i18n.Lang, head, text, place, secondary string, awaitsReply bool, open botcard.Button) (*botcard.Card, string) {
	title, color, tag := agentDoneState(head, awaitsReply)
	note := func(clipped bool) string {
		n := i18n.T(l, "assistant-feishu_cards.agent_done_note", i18n.Args{"place": place})
		if secondary != "" {
			n = secondary + "\n" + n
		}
		if clipped {
			n = i18n.T(l, "assistant-feishu_cards.only_beginning") + "\n" + n
		}
		return n
	}
	limit := agentDoneClip
	hint := i18n.T(l, "assistant-feishu_cards.agent_done_reply_hint")
	if open != nil {
		open["type"] = botcard.Primary // the one thing to do with it, besides replying
	}
	var (
		card    *botcard.Card
		body    string
		clipped bool
	)
	for { // a chart-laden answer can render wide: halve the body until the card fits Feishu's limit
		body = clip(text, limit)
		clipped = len([]rune(text)) > limit
		lead, rest := leadAndRest(body, agentDoneLead)
		card = botcard.NewCard(title, color, title).Subtitle(dotJoin(i18n.T(l, "assistant-feishu_cards.agent_done_note", i18n.Args{"place": place}), secondary)).
			Tag(i18n.T(l, tag), color).Markdown(botcard.CardMarkdown(lead)).
			Panel(i18n.T(l, "assistant-feishu_cards.agent_done_more"), false, botcard.CardMarkdown(rest))
		meta := []string{botcard.EscapeMarkdown(hint)}
		if clipped {
			meta = append([]string{botcard.EscapeMarkdown(i18n.T(l, "assistant-feishu_cards.only_beginning"))}, meta...)
		}
		card.Meta(meta...).Buttons(open)
		if len(card.JSON()) <= assistantwire.AnswerCardMaxBytes || limit <= agentDoneClip/8 {
			break
		}
		limit /= 2
	}
	fallback := fallbackText.Replace(head) + "\n\n" + fallbackText.Replace(body) + "\n" + fallbackText.Replace(note(clipped)) +
		"\n" + fallbackText.Replace(hint)
	return card, strings.TrimSpace(fallback)
}
