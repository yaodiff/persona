package assistant

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func (h larkHost) FindInterruptibleRun(match func(*channel.Run) bool) func(proto.Session, func(string) string) {
	c := &h.s.chans
	c.mu.Lock()
	index := slices.IndexFunc(c.st.Runs, match)
	if index < 0 {
		c.mu.Unlock()
		return nil
	}
	run := c.st.Runs[index]
	prog := c.live[run.ID]
	c.mu.Unlock()
	if prog == nil {
		return nil
	}
	return func(session proto.Session, interrupt func(string) string) {
		// Target resolution can yield to a finish, recall or replacement. Recheck the same objects
		// under the lifecycle's lock order: progress, then channel state.
		prog.mu.Lock()
		defer prog.mu.Unlock()
		c.mu.Lock()
		current := slices.Contains(c.st.Runs, run) && c.live[run.ID] == prog && match(run) &&
			!run.Recalled && !c.answering[run.ID] && !prog.finished && prog.failing.IsZero() &&
			(!turnOver(session) || !run.Seen && !run.Held)
		c.mu.Unlock()
		if current {
			prog.stoppedCard = interrupt(prog.stoppedCard)
		}
	}
}

// stopOutcome is what interrupting a conversation's current turn came to; the caller renders it as a command note
// (/stop) or a card toast (the progress card's Stop button).
type stopOutcome struct {
	kind    string // toast kind: success | info | error
	message string
}

// turnOver says whether the conversation's last turn ended (its result unseen or already seen) and nothing is being
// opened: no turn to stop. An interrupt there would only press Escape into an idle agent's terminal (the node's
// session.interrupt sends that key, node/node.go) and the owner would be told a turn was stopped that had already
// finished. A state that cannot be told is not over: the owner asked to stop, so it is tried.
func turnOver(s proto.Session) bool {
	return !s.Starting && (s.State == proto.StateUnread || s.State == proto.StateIdle)
}

// turnPending says whether this chat sent a message into the conversation that has not been seen working, or seen to
// end a turn, since: the conversation may still be read as quiet (channel.Run.Seen), so its state cannot say the turn
// is over. A /stop right after the message is exactly the case it is sent for.
func (s *Service) turnPending(machine, session string) bool {
	if session == "" {
		return false
	}
	for _, r := range s.channelRuns(channel.Feishu) {
		if r.MachineID == machine && r.SessionID == session && !r.Seen {
			return true
		}
	}
	return false
}

func (s *Service) interruptBotConversation(ctx context.Context, rt resolvedTarget) stopOutcome {
	l := s.botLang(ctx)
	if err := s.call(ctx, rt.machine, "session.interrupt", map[string]any{"session_id": rt.session.SessionID}, nil); err != nil {
		return stopOutcome{kind: "error", message: i18n.T(l, "assistant-feishu_cards.not_stopped", i18n.Args{"error": clip(oneLine(err.Error()), 200)})}
	}
	return stopOutcome{kind: "success", message: i18n.T(l, "assistant-feishu_cards.stopped", i18n.Args{"label": rt.label})}
}
