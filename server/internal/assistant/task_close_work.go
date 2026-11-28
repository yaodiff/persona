package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/fleetview"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
)

type taskCloseWork struct {
	session, title, items, quote string
}

// taskCloseWorkSnapshot reads only live node inventory and the manager's work snapshot.
// It is also called at the close transaction's commit boundary.
func (s *Service) taskCloseWorkSnapshot(t Todo, now time.Time) *taskCloseWork {
	if t.DoneAt != nil || s.devs == nil {
		return nil
	}
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil || a.SessionID == "" {
			continue
		}
		m, known := s.devs.Summary(a.MachineID)
		if !known || !m.Online {
			continue
		}
		c, ok := s.nodeSession(a.MachineID, a.SessionID)
		if !ok || c.Status == proto.StatusOffline || c.State == proto.StateOffline || c.Archived {
			continue
		}
		r := &s.fleet.rep
		r.mu.RLock()
		reported := r.work[[2]string{a.MachineID, a.SessionID}]
		r.mu.RUnlock()
		var items []string
		if c.Loop != nil && (c.Loop.Kind != "wakeup" || c.Loop.NextAt.After(now)) {
			items = append(items, "loop ("+c.Loop.Kind+")")
		}
		background, monitor := false, false
		for _, bg := range c.Background {
			if bg.EndedAt != nil || bg.ExpiresAt != nil && !bg.ExpiresAt.After(now) {
				continue
			}
			if bg.Kind == "monitor" {
				monitor = true
			} else {
				background = true
			}
		}
		for _, sub := range c.Subagents {
			background = background || sub.Running
		}
		if background {
			items = append(items, "background work")
		}
		if monitor {
			items = append(items, "monitor")
		}
		if c.Work != nil && c.Work.WakeAt != nil && c.Work.WakeAt.After(now) ||
			reported.WakeAt != nil && reported.WakeAt.After(now) && sameTurn(c, reported.TurnAt) {
			items = append(items, "scheduled wake")
		}
		if len(items) != 0 {
			b := &s.taskActivity
			b.mu.Lock()
			quote := b.ownerExcerpt[runtimeListID(a.MachineID, a.SessionID)]
			b.mu.Unlock()
			return &taskCloseWork{session: runtimeListID(a.MachineID, a.SessionID), title: fleetview.AgentTitle(sessionTitle(&c), 80), items: strings.Join(items, ", "), quote: quote}
		}
	}
	return nil
}

// Only owner-message events supply excerpts. The transcript's user role can also
// represent a dispatch, a supervisor message or a scheduled injection.
func (s *Service) rememberTaskCloseExcerpt(ref SessionRef, text string) {
	if text = strings.TrimSpace(text); text == "" || s.activeTodoOf(ref.MachineID, ref.SessionID) == "" {
		return
	}
	b := &s.taskActivity
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ownerExcerpt == nil || len(b.ownerExcerpt) >= 512 {
		b.ownerExcerpt = make(map[string]string)
	}
	b.ownerExcerpt[runtimeListID(ref.MachineID, ref.SessionID)] = textfmt.Cut(text, 200)
}

type taskCloseToolRefusal struct {
	cause   error
	message string
}

func (e taskCloseToolRefusal) Error() string { return e.message }
func (e taskCloseToolRefusal) Unwrap() error { return e.cause }

func (s *Service) taskCloseToolError(ctx context.Context, err error) error {
	var hold *taskCloseHoldError
	if !errors.As(err, &hold) || hold.work == nil {
		return err
	}
	l := s.lang(ctx)
	message := i18n.T(l, "assistant-todos.close_active_refused", i18n.Args{
		"id": hold.todo, "agent": hold.work.title, "activity": hold.work.items,
	})
	if hold.work.quote != "" {
		message += "\n" + i18n.T(l, "assistant-todos.latest_owner_message", i18n.Args{"message": hold.work.quote})
	}
	message += "\n" + i18n.T(l, "assistant-todos.close_active_choices")
	return taskCloseToolRefusal{cause: err, message: message}
}

var errCloseNotConfirmed = i18n.Err("assistant-todos.close_not_confirmed")

type ownerCloseConfirmation struct {
	todo       string
	reopened   *time.Time
	work       taskCloseWork
	recovering bool
}

type ownerCloseKey struct{}

func (s *Service) unconfirmedTaskClose(ctx context.Context, t Todo) *taskCloseWork {
	work := s.taskCloseWorkSnapshot(t, time.Now())
	if work == nil {
		return nil
	}
	c, ok := ctx.Value(ownerCloseKey{}).(ownerCloseConfirmation)
	if ok && c.todo == t.ID && (c.work == *work || c.recovering) &&
		(c.reopened == nil && t.ReopenedAt == nil ||
			c.reopened != nil && t.ReopenedAt != nil && c.reopened.Equal(*t.ReopenedAt)) {
		return nil
	}
	return work
}

// acceptTryFromApp asks before taking an owner Pass that would close a live Agent's work.
// The answer path revalidates the round after the user responds to the card.
func (s *Service) acceptTryFromApp(ctx context.Context, id, askedAt string) (acceptResult, error) {
	t, ok := s.todoNow(id)
	if ok && t.DoneAt == nil && userCheckAsked(t) {
		a, found := s.askCopy(id)
		at, err := time.Parse(timeKeyLayout, askedAt)
		if err == nil && found && a.AskedAt.Equal(at) && a.AcceptedAt.IsZero() && !acceptAskStale(&a, t) {
			if work := s.taskCloseWorkSnapshot(t, time.Now()); work != nil {
				ctx, err = s.confirmOwnerTaskClose(ctx, t, work, true)
				if err != nil {
					return acceptResult{}, err
				}
			}
		}
	}
	return s.acceptReviewRound(ctx, id, askedAt)
}

// The app's first press requests a one-time owner approval using the existing app
// permission card. The context proof is private to this request and references one task round.
func (s *Service) confirmOwnerTaskClose(ctx context.Context, t Todo, work *taskCloseWork, accept bool) (context.Context, error) {
	key := "assistant-todos.close_active_confirm"
	if accept {
		key = "assistant-todos.accept_active_confirm"
	}
	v := s.settings(ctx)
	args := i18n.Args{"title": clip(t.Title, 80), "agent": clip(work.title, 80)}
	summary := i18n.T(s.requestCtxLang(ctx), key, args)
	if work.quote != "" {
		summary += "\n" + i18n.T(s.requestCtxLang(ctx), "assistant-todos.latest_agent_message", i18n.Args{"message": work.quote})
	}
	payload, _ := json.Marshal(map[string]string{"id": t.ID})
	now := time.Now().UTC()
	wait, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	r, err := s.ask(wait, proto.Interaction{
		ID: "ix_" + randomHex(6), MachineID: cmp.Or(v.MainMachineID, v.MachineID), SessionID: cmp.Or(v.Main, t.Origin),
		Kind: proto.IxPermission, ToolName: "todo_complete", Summary: summary, ToolInput: payload,
		SessionTitle: "Persona", CreatedAt: now, ExpiresAt: now.Add(45 * time.Second),
	})
	if err != nil || !r.UserAllowed() || r.Remembered {
		return ctx, errCloseNotConfirmed
	}
	return context.WithValue(ctx, ownerCloseKey{}, ownerCloseConfirmation{todo: t.ID, reopened: t.ReopenedAt, work: *work}), nil
}

// Close declines live alongside the turn ledger so a restart cannot erase them
// while the same Agent round still awaits review.
func (s *Service) recordTodoCloseDecline(ctx context.Context, id string, at time.Time) error {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if at.IsZero() && !b.loaded && s.store() == nil {
		return nil
	}
	if !s.turnsReadLocked(ctx) {
		return errors.New("task turn ledger unavailable")
	}
	prev := b.doc.CloseDeclines[id]
	if at.IsZero() {
		if prev.IsZero() {
			return nil
		}
		delete(b.doc.CloseDeclines, id)
	} else {
		if b.doc.CloseDeclines == nil {
			b.doc.CloseDeclines = make(map[string]time.Time)
		}
		b.doc.CloseDeclines[id] = at
	}
	if err := s.saveTurnsLocked(ctx); err != nil {
		// A failed write is never permission to accept a recently declined close.
		if at.IsZero() {
			b.doc.CloseDeclines[id] = prev
		}
		return err
	}
	return nil
}

func (s *Service) todoCloseDeclineError(ctx context.Context, t Todo) error {
	if t.DoneAt != nil {
		return nil
	}
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.turnsReadLocked(ctx) {
		return errors.New("the task turn ledger could not be read; retry before accepting the task")
	}
	if !b.doc.CloseDeclines[t.ID].IsZero() {
		return errors.New("the user declined closing this task. Keep it open until its Agent takes another turn, or use needs_user to ask the owner")
	}
	return nil
}
