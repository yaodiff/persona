package lark

import (
	"context"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// turnOver says whether the conversation's last turn ended (its result unseen or already seen) and nothing is being
// opened: no turn to stop. An interrupt there would only press Escape into an idle agent's terminal (the node's
// session.interrupt sends that key, node/node.go) and the owner would be told a turn was stopped that had already
// finished. A state that cannot be told is not over: the owner asked to stop, so it is tried.
func turnOver(s proto.Session) bool {
	return !s.Starting && (s.State == proto.StateUnread || s.State == proto.StateIdle)
}

// stopConversation is typed /stop: it targets the conversation's current turn, independently of any progress card.
// The core's TurnPending says
// whether this chat sent a message that has not been seen working, or seen to end a turn, since: the conversation may
// still be read as quiet, so its state cannot say the turn is over. A /stop right after the message is exactly the
// case it is sent for.
func (s core) stopConversation(ctx context.Context, f FeishuFeatures, t botTarget) (kind, message string) {
	l := s.botLang(ctx)
	if err := s.botAllowed(l, f, t); err != nil {
		return "error", err.Error()
	}
	rt, err := s.h.ResolveTarget(ctx, t)
	if err != nil || rt.IsNew || (turnOver(rt.Session) && !s.h.TurnPending(rt.Machine, rt.Session.SessionID)) {
		return "info", i18n.T(l, "assistant-feishu_cards.nothing_running")
	}
	return s.h.InterruptConversation(ctx, rt)
}

// stopProgressCard accepts the existing button payload only while the callback's
// actual message still belongs to a current, watched Feishu run. History and thread
// bindings cannot grant this ownership. Owner/chat authorization remains in extmgr.
func (s core) stopProgressCard(ctx context.Context, f FeishuFeatures, t botTarget, mid string) (kind, message string) {
	l := s.botLang(ctx)
	if err := s.botAllowed(l, f, t); err != nil {
		return "error", err.Error()
	}
	expired := i18n.T(l, "assistant-feishu_cards.card_expired")
	if mid == "" || t.MachineID == "" || t.SessionID == "" {
		return "info", expired
	}
	withRun := s.h.FindInterruptibleRun(func(r *channel.Run) bool {
		return r.Channel == channel.Feishu && botTargetOf(r) == t && r.Ext["card"] == mid
	})
	if withRun == nil {
		return "info", expired
	}
	rt, err := s.h.ResolveTarget(ctx, t)
	if err != nil || rt.IsNew || rt.Machine != t.MachineID || rt.Session.SessionID != t.SessionID {
		return "info", expired
	}
	kind, message = "info", expired
	// The host holds the progress lock through delivery. Concurrent presses act once, and a failed
	// RPC stays retryable. The node's conversation-level RPC has no atomic turn-generation precondition.
	withRun(rt.Session, func(stoppedCard string) string {
		if stoppedCard != mid {
			kind, message = s.h.InterruptConversation(ctx, rt)
			if kind == "success" {
				return mid
			}
		}
		return stoppedCard
	})
	return kind, message
}
