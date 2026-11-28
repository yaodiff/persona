package assistant

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Seen to-dos (#5707, docs/assistant/todo-status-sop.md "Seen"): the owner looked at a to-do in Just finished, by Got it
// on its row or by opening it, so it is filed under Done (todo_triage.go) instead of waiting out the 24 hours. The mark is
// the to-do's seen_at, kept here so every device agrees. Marking it is not activity: updated_at stays, and a to-do
// that moves after it comes back to Just finished.

// seeTodo marks todo id seen now, or with seen false takes the mark off (Undo); db.ErrTodoNotFound when it is not there.
func (s *Service) seeTodo(ctx context.Context, id string, seen bool) (Todo, error) {
	s.todos.writing.Lock()
	cur, ok := s.todoNow(id)
	if !ok {
		s.todos.writing.Unlock()
		return Todo{}, db.ErrTodoNotFound
	}
	var at *time.Time
	if seen {
		now := time.Now().UTC()
		at = &now
	}
	var row *db.TodoRow
	if st := s.todoDB(); st != nil {
		var err error
		if row, err = st.SeeTodo(ctx, id, at); err != nil {
			s.todos.writing.Unlock()
			return Todo{}, err
		}
	} else {
		r := todoRowOf(cur)
		r.SeenAt = at
		if at == nil {
			r.AutoSeenUndone = true
		}
		row = &r
	}
	out, _, applied := s.setTodoRow(id, *row, nil)
	s.todos.writing.Unlock()
	if !applied {
		return Todo{}, db.ErrTodoNotFound // deleted meanwhile
	}
	s.todosChanged(ctx) // the other devices read the list again: it moves there too
	return out, nil
}

// todoCompletion is one completion of a to-do as the writer that closed it saw it: the to-do, when it was closed and
// when it was last reopened before that. Auto-seen settles that completion only, never a later one.
type todoCompletion struct {
	id                 string
	doneAt, reopenedAt *time.Time
}

// completionOf is t's completion as t holds it.
func completionOf(t Todo) todoCompletion {
	return todoCompletion{id: t.ID, doneAt: t.DoneAt, reopenedAt: t.ReopenedAt}
}

// committedCompletion is the completion row, the committed close of to-do id, records.
func committedCompletion(id string, row db.TodoRow) todoCompletion {
	return todoCompletion{id: id, doneAt: row.DoneAt, reopenedAt: row.ReopenedAt}
}

// autoSeeTodo writes only the completion the caller observed. Keep the check, commit and cache update
// in the same writer order as reopen and manual Undo.
func (s *Service) autoSeeTodo(ctx context.Context, completed todoCompletion) (bool, error) {
	now := time.Now().UTC()
	s.todos.writing.Lock()
	cur, ok := s.todoNow(completed.id)
	if !ok || !sameTodoCompletion(cur, completed) || cur.AutoSeenUndone {
		s.todos.writing.Unlock()
		return false, nil
	}
	var row *db.TodoRow
	if st := s.todoDB(); st != nil {
		r, updated, err := st.AutoSeeTodo(ctx, cur.ID, *completed.doneAt, completed.reopenedAt, now)
		if err != nil || !updated {
			s.todos.writing.Unlock()
			return false, err
		}
		row = r
	} else {
		r := todoRowOf(cur)
		r.SeenAt = &now
		row = &r
	}
	_, _, applied := s.setTodoRow(cur.ID, *row, nil)
	s.todos.writing.Unlock()
	if applied {
		s.todosChanged(ctx)
	}
	return applied, nil
}

func sameTodoCompletion(current Todo, completed todoCompletion) bool {
	return current.DoneAt != nil && sameTime(current.DoneAt, completed.doneAt) &&
		sameTime(current.ReopenedAt, completed.reopenedAt)
}

// autoSeeAcceptedTodo settles an accepted autonomous to-do when todos_v2 is enabled (#14639, #16098):
// it sets seen_at on the to-do and marks its finished linked worker sessions as read according to
// todos_v2_auto_seen ("off", "subtasks", or "all_autonomous").
// Never applies when todos_v2 is off, mode is "off", mode is "subtasks" on a top-level to-do,
// audience != autonomous, user_check is true, outcome != done,
// or the owner called Undo in this completion round (DELETE /api/v1/todos/{id}/seen -> AutoSeenUndone == true).
func (s *Service) autoSeeAcceptedTodo(ctx context.Context, completed todoCompletion, outcome string, userCheck bool) {
	cfg := s.settings(ctx)
	if userCheck || outcome != outcomeDone || !cfg.todosV2On() {
		return
	}
	mode := cfg.todosV2AutoSeenMode()
	if mode == todosV2AutoSeenOff {
		return
	}
	id := completed.id
	cur, ok := s.todoNow(id)
	if !ok || cur.Audience != db.AudienceAutonomous || cur.AutoSeenUndone || !sameTodoCompletion(cur, completed) {
		return
	}
	if mode == todosV2AutoSeenSubtasks && strings.TrimSpace(cur.PartOf) == "" {
		return
	}
	applied, err := s.autoSeeTodo(ctx, completed)
	if err != nil {
		s.logf("task %s: auto-seen: %v", id, err)
		return
	}
	if !applied {
		return
	}
	lists := machineSessions{}
	for _, a := range cur.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil || a.MachineID == "" || a.SessionID == "" {
			continue
		}
		l, ok := lists[a.MachineID]
		if !ok {
			l.list, l.err = s.sessions(ctx, a.MachineID)
			lists[a.MachineID] = l
		}
		if l.err != nil {
			continue
		}
		for _, c := range l.list {
			if c.SessionID != a.SessionID {
				continue
			}
			if workerSessionFinishedForAutoSeen(c) {
				// Session lookup can outlast a reopen or an Undo.
				if live, ok := s.todoNow(id); !ok || !sameTodoCompletion(live, completed) || live.AutoSeenUndone {
					return
				}
				s.markSessionRead(ctx, a.MachineID, a.SessionID)
			}
			break
		}
	}
}

func workerSessionFinishedForAutoSeen(c proto.Session) bool {
	if c.Assistant {
		return false
	}
	if c.Status == proto.StatusWorking || c.Status == proto.StatusBlocked || c.Status == proto.StatusOffline {
		return false
	}
	if c.State == proto.StateWorking || c.State == proto.StateNeedsYou {
		return false
	}
	if c.BackgroundWorking {
		return false
	}
	return true
}

func (s *Service) todoSeenRoutes() {
	register := func(method string, seen bool) {
		// PUT /api/v1/todos/{id}/seen: the owner saw it (Got it, or its detail opened); DELETE: Undo.
		s.handle(method+" /api/v1/todos/{id}/seen", func(w http.ResponseWriter, r *http.Request) {
			if !fullRole(r) {
				apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
				return
			}
			if err := s.ensureTodosLoaded(r.Context()); err != nil {
				apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
				return
			}
			t, err := s.seeTodo(r.Context(), r.PathValue("id"), seen)
			if errors.Is(err, db.ErrTodoNotFound) {
				apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
				return
			}
			if err != nil {
				apierr.Write(w, http.StatusInternalServerError, err)
				return
			}
			apierr.WriteJSON(w, map[string]any{"todo": t})
		})
	}
	register("PUT", true)
	register("DELETE", false)
}
