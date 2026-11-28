package assistant

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A transport receipt is not an acknowledgement. Keep the source and one retry in
// the existing turn ledger until the exact dispatched prompt gets visible output.
type taskInboxEvent struct {
	ID      string                 `json:"id"`
	Key     string                 `json:"key"`
	Todo    string                 `json:"todo"`
	Machine string                 `json:"machine"`
	Session string                 `json:"session"`
	Prompt  string                 `json:"prompt"`
	Inbox   prompts.SuperviseInbox `json:"inbox"`
	Retried bool                   `json:"retried,omitempty"`
	Turn    string                 `json:"turn,omitempty"`
	Failure string                 `json:"failure,omitempty"`
	RetryAt time.Time              `json:"retry_at,omitzero"`
}

const inboxRetryUnconfirmed = "External event unhandled: the no-output turn's automatic redelivery has no confirmed outcome."

func (s *Service) updateTaskInboxEvents(ctx context.Context, change func(map[string]taskInboxEvent) bool) error {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.turnsLocked(ctx) {
		return errors.New("external-event ledger could not be read")
	}
	before := b.doc.InboxEvents
	b.doc.InboxEvents = maps.Clone(before)
	if b.doc.InboxEvents == nil {
		b.doc.InboxEvents = map[string]taskInboxEvent{}
	}
	if !change(b.doc.InboxEvents) {
		b.doc.InboxEvents = before
		return nil
	}
	if err := s.saveTurnsLocked(ctx); err != nil {
		b.doc.InboxEvents = before
		return err
	}
	return nil
}

func (s *Service) recordTaskInboxEvent(ctx context.Context, e taskInboxEvent) (duplicate bool, err error) {
	err = s.updateTaskInboxEvents(ctx, func(events map[string]taskInboxEvent) bool {
		if _, ok := events[e.ID]; ok && !e.Retried {
			duplicate = true
			return false
		}
		events[e.ID] = e
		return true
	})
	return duplicate, err
}

func (s *Service) taskInboxRecipient(ctx context.Context, todo, machine, session string) bool {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.turnsLocked(ctx) {
		return false
	}
	for _, e := range b.doc.InboxEvents {
		if e.Todo == todo && e.Machine == machine && e.Session == session {
			return true
		}
	}
	return false
}

func inboxUnhandledSignal(e taskInboxEvent) superviseSignal {
	in := e.Inbox
	in.ToAgent = "unhandled"
	return superviseSignal{Todo: e.Todo, Kind: sigInbox, Key: e.ID, Machine: e.Machine, Session: e.Session,
		Detail: e.Failure + " Source event: " + e.Key, Inbox: &in}
}

func (s *Service) taskInboxUnhandled(ctx context.Context, id string) bool {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.turnsLocked(ctx) {
		return false
	}
	_, pending := b.doc.InboxEvents[id]
	return pending
}

// The normal sweep also restores an unconfirmed retry's source-bearing notice
// after a restart. It never blindly repeats a send whose outcome is unknown.
func (s *Service) recoverTaskInboxEvents(ctx context.Context) {
	b := &s.tasks.turns
	b.mu.Lock()
	var pending []taskInboxEvent
	if s.turnsLocked(ctx) {
		for _, e := range b.doc.InboxEvents {
			if e.Failure != "" && (e.Failure != inboxRetryUnconfirmed || time.Since(e.RetryAt) >= 5*time.Minute) {
				pending = append(pending, e)
			}
		}
	}
	b.mu.Unlock()
	for _, e := range pending {
		s.signal(inboxUnhandledSignal(e))
	}
}

// taskInboxTurnEnded handles only server-recorded deliveries to this recipient,
// not a task's unrelated owner input, an old answer, or a user's explicit stop.
func (s *Service) taskInboxTurnEnded(ctx context.Context, t Todo, last proto.Session, items []proto.Item, silent bool) bool {
	_, items = splitLastTurn(items)
	for _, it := range items {
		if it.Kind == proto.KindNotice && it.Text == userStoppedNotice {
			return false // an explicit stop neither acknowledges nor retries its event
		}
	}
	b := &s.tasks.turns
	b.mu.Lock()
	pending := s.turnsLocked(ctx) && len(b.doc.InboxEvents) > 0
	b.mu.Unlock()
	if !pending {
		return false
	}
	if silent {
		// Recovery also sees turns stopped by their conversation, before their
		// receipt or after its Stop was consumed and its verdict restored.
		_, stopped := s.stopFor(last.MachineID, last.SessionID, s.taskStretch(t))
		if quietTurnKind(items, stopped) == proto.TurnErrorStopped {
			return false
		}
		if m, ok := s.quietMarks(last.MachineID)[last.SessionID]; ok && m.kind == proto.TurnErrorStopped && m.holds(last) {
			return false
		}
	}
	ask, _ := turnExchange(items)
	if ask == "" {
		return false
	}
	end, _ := turnEndOf(items)
	turn := end.Key
	if turn == "" {
		turn = turnOpen(items)
	}
	if turn == "" {
		turn = fmt.Sprintf("rev:%d@%s", last.Rev, last.UpdatedAt.UTC().Format(time.RFC3339Nano))
	}
	var retry, failed, sources []taskInboxEvent
	matched := false
	err := s.updateTaskInboxEvents(ctx, func(events map[string]taskInboxEvent) bool {
		changed := false
		for id, e := range events {
			if e.Todo != t.ID || e.Machine != last.MachineID || e.Session != last.SessionID ||
				ask != strings.TrimSpace(e.Prompt) {
				continue
			}
			matched = true
			sources = append(sources, e)
			if !silent {
				delete(events, id)
				changed = true
				continue
			}
			if e.Turn == turn {
				continue // a duplicate completion cannot spend another attempt
			}
			e.Turn = turn
			if e.Retried {
				e.Failure = "External event unhandled: the original turn and its one automatic redelivery produced no output."
				failed = append(failed, e)
			} else {
				e.Retried = true
				e.RetryAt = time.Now().UTC()
				e.Failure = inboxRetryUnconfirmed
				retry = append(retry, e)
			}
			events[id] = e
			changed = true
		}
		return changed
	})
	if err != nil {
		s.logf("task %s: external-event completion: %v", t.ID, err)
		if silent {
			for _, e := range sources {
				e.Failure = "External event unhandled: its retry state could not be saved: " + clip(oneLine(err.Error()), 200)
				s.signal(inboxUnhandledSignal(e))
			}
		}
		return matched
	}
	for _, e := range retry {
		current, ok := s.todoNow(t.ID)
		var sendErr error
		switch {
		case !ok || !taskOpen(current):
			sendErr = errors.New("the task is no longer open")
		case current.Supervision != nil && current.Supervision.state(current) == reviewNeedsUser:
			sendErr = errors.New("the task now waits for the owner")
		case !slices.ContainsFunc(current.Agents, func(a TodoAgent) bool {
			return a.MachineID == e.Machine && a.SessionID == e.Session && a.ReleasedAt == nil && a.DismissedAt == nil
		}):
			sendErr = errors.New("the event's agent is no longer assigned to the task")
		default:
			_, sendErr = s.sendTaskInput(ctx, current, e.Prompt, true, &e, false, nil)
		}
		if err := s.updateTaskInboxEvents(ctx, func(events map[string]taskInboxEvent) bool {
			cur, exists := events[e.ID]
			if !exists || cur.Turn != e.Turn {
				return false // its retry already ended while the send was in flight
			}
			cur.Failure = ""
			if sendErr != nil {
				cur.Failure = "External event unhandled: automatic redelivery failed: " + clip(oneLine(sendErr.Error()), 200)
				failed = append(failed, cur)
			}
			events[e.ID] = cur
			return true
		}); err != nil {
			s.logf("task %s: external-event redelivery outcome: %v", t.ID, err)
		}
	}
	for _, e := range failed {
		s.signal(inboxUnhandledSignal(e))
	}
	return matched
}
