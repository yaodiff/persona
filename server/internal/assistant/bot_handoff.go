package assistant

import (
	"context"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larkhost"
)

// Proxy replies (docs/extensions/bots.md "Proxy replies handed off from a group"). The run itself is the lark module's
// (modules/lark/handoff.go): this file is only what that module asks of the core. A handoff never enters the owner's
// conversations, so it never reaches handToAssistant, StartRun, feishuMessage or openFromElsewhere.
//
// Native message_cot is not part of this: a handoff has no channel run, its chat is deliberately not one the bot may
// send into, and the default tier asks the owner before anything is posted.

// StartHandoffGuest starts the guest that answers handoff, registered before the node starts it (the module's
// HandoffTools is the one tool it lists). Without the lark module (built out or switched off), no guest starts.
func (s *Service) StartHandoffGuest(ctx context.Context, handoff, text, launcher string) (string, string, error) {
	names := make([]string, 0, 1)
	for _, tool := range s.larkModule().HandoffTools() {
		if tool.Name != "" {
			names = append(names, tool.Name)
		}
	}
	if len(names) == 0 {
		return "", "", larkhost.ErrNoChannel
	}
	id := newGuestSessionID()
	machine := s.settings(ctx).MachineID
	if err := s.guestSetHandoff(ctx, machine, id, handoff, names); err != nil {
		return "", "", err
	}
	sess, err := s.Start(ctx, StartParams{Text: text, Launcher: launcher, GuestTools: names, GuestHandoff: handoff,
		NewSessionID: id, Origin: OriginChannel, OriginRef: channel.Feishu})
	if err != nil {
		_ = s.guestDrop(context.WithoutCancel(ctx), id) // a start that never ran leaves no guest behind
		return "", "", err
	}
	return sess.MachineID, sess.SessionID, nil
}

// botHandoffCaller raises a proxy reply's confirmation. Its name is the owner's language, filled in at the call.
func botHandoffCaller(name string) *outsideCaller {
	return &outsideCaller{key: "bot:feishu", kind: "app", name: name}
}

// RaiseHandoffDecision asks the owner to approve one draft. The caller is the bot, so an extension's decision never
// reaches a handoff and a handoff's never reaches an extension.
func (s *Service) RaiseHandoffDecision(ctx context.Context, handoff string, req assistantwire.OutsideDecisionRequest) (string, error) {
	name := i18n.T(s.lang(ctx), "assistant-feishu.handoff_decision_source")
	decision, _, err := s.raiseOutsideDecision(ctx, botHandoffCaller(name), req)
	if err != nil {
		return "", err
	}
	return decision.ID, nil
}

// WithdrawHandoffDecision takes back a confirmation nobody answered (the handoff expired).
func (s *Service) WithdrawHandoffDecision(ctx context.Context, id string) {
	if id == "" {
		return
	}
	name := i18n.T(s.lang(ctx), "assistant-feishu.handoff_decision_source")
	if _, err := s.withdrawOutsideDecision(ctx, botHandoffCaller(name), id); err != nil {
		s.logf("assistant: withdraw handoff decision %s: %v", id, err)
	}
}

// handoffDecided delivers the owner's answer to its handoff, for the bot's own decisions only. An empty option (a typed
// answer, a fixed response) declines. caller is the decision's own caller key.
func (s *Service) handoffDecided(ctx context.Context, caller, handoff, option string) {
	if caller != botHandoffCaller("").key {
		return
	}
	id := strings.TrimPrefix(handoff, "handoff:")
	if id == "" || id == handoff {
		return
	}
	s.larkModule().HandoffDecided(ctx, id, option)
}
