package assistant

import (
	"cmp"
	"context"
	"errors"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/larkhost"
)

// ---- the hub's transport ----

// botPost sends a card: as a reply to replyTo, else into the owner's chat; one Feishu refuses goes as rich text
// (fallback).
func (s *Service) botPost(ctx context.Context, replyTo string, card *botcard.Card, fallback string) (string, error) {
	return s.botPostTo(ctx, feishuTarget{}, replyTo, card, fallback, "")
}

// feishuTarget says where a feishu.post / feishu.text goes beyond the default (larkhost.Target): the chat to send into
// (ChatID; "" is the owner's private chat, the only one the hub accepts for now), whether a reply goes into the
// parent's thread, whether a reply whose parent is gone fails (the hub's no_detour) instead of landing in the chat as
// a message of its own, and whether it answers the owner's "/read" in a group (the lark module's read.go). The zero value is what
// the calls always did.
type feishuTarget = larkhost.Target

// targetParams adds t's fields to p; the zero target adds none, so the call is the same as before.
func targetParams(t feishuTarget, p map[string]any) map[string]any {
	if t.ChatID != "" {
		p["chat_id"] = t.ChatID
	}
	if t.InThread {
		p["in_thread"] = true
	}
	if t.NoDetour {
		p["no_detour"] = true
	}
	return p
}

// botPostTo is botPostKeyed to a target. Every card the bot sends goes through here, and each one that lands is logged
// with what it was (feishuSendAs): how many went out, of which kind and for which conversation, can be counted from
// the log (#8270).
func (s *Service) botPostTo(ctx context.Context, to feishuTarget, replyTo string, card *botcard.Card, fallback, key string) (string, error) {
	if to.Read {
		return s.botPostContent(ctx, to, replyTo, card.JSON(), fallback, key)
	}
	return s.botPostContent(ctx, to, replyTo, s.workbenchStamp(ctx, card).JSON(), fallback, key)
}

// botPostContent is botPostTo with the message's content as it goes: a card's JSON, or a card entity's
// (botcard.CardEntityContent).
func (s *Service) botPostContent(ctx context.Context, to feishuTarget, replyTo, content, fallback, key string) (string, error) {
	var res struct {
		MessageID string `json:"message_id"`
	}
	p := map[string]any{"reply_to": replyTo, "card": content, "fallback": fallback}
	if key != "" {
		p["uuid"] = key
	}
	pairing, reminder := assistantwire.LarkReminderPairingOf(ctx)
	if reminder {
		p["todo_reminder_pairing"] = map[string]any{"owner": pairing.Owner, "paired_at": pairing.PairedAt}
	}
	targetParams(to, p)
	err := s.appCall(ctx, AppFeishu, FeishuPost, p, &res)
	if reminder && err == nil && res.MessageID == "" {
		err = errors.New("todo reminder: delivery receipt unavailable")
	}
	if err == nil {
		l, _ := ctx.Value(feishuSendKey{}).(feishuSend)
		s.logf("bots: card sent: kind=%s machine=%s session=%s message=%s", cmp.Or(l.kind, "other"), cmp.Or(l.machine, "-"),
			cmp.Or(l.session, "-"), res.MessageID)
	}
	return res.MessageID, err
}

// feishuSend is what a card is, for the line botPostTo logs when it lands: its kind (agent_done, push, sync, task,
// answer, progress, or an agent's card: question, permission, plan, screen) and the computer and session it is about.
type feishuSend struct{ kind, machine, session string }

type feishuSendKey struct{}

// feishuSendAs labels the cards posted within ctx; one posted without a label is logged as kind "other".
func feishuSendAs(ctx context.Context, kind, machine, session string) context.Context {
	return context.WithValue(ctx, feishuSendKey{}, feishuSend{kind: kind, machine: machine, session: session})
}

// botNote replies to a message with a line of text.
func (s *Service) botNote(ctx context.Context, messageID, text string) {
	s.botNoteTo(ctx, feishuTarget{}, messageID, text)
}

// botNoteTo is botNote to a target.
func (s *Service) botNoteTo(ctx context.Context, to feishuTarget, messageID, text string) {
	if err := s.appCall(ctx, AppFeishu, FeishuText, targetParams(to, map[string]any{"reply_to": messageID, "text": text}), nil); err != nil {
		s.logf("bots: note: %v", err)
	}
}

func (s *Service) botUpdateCard(ctx context.Context, messageID string, card *botcard.Card) error {
	return s.appCall(ctx, AppFeishu, FeishuUpdateCard, map[string]any{"message_id": messageID, "card": s.workbenchStamp(ctx, card).JSON()}, nil)
}
