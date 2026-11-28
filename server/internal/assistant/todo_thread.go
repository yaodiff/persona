package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
)

// The chat thread a todo is bound to (#5279, docs/assistant/todos.md "Chat thread"): at most one thread per todo and one todo
// per thread. Binding itself posts nothing; topic_followup.go observes human activity through the configured reader.

// TodoThread is the binding as the apps and the store have it. Channel is a channel id (internal/channel: "feishu"
// for the Feishu bot's chats); for Feishu ChatID is the chat's id (oc_…) and ThreadID the thread_id of its events
// (omt_…), not the root message's message_id. sameThread compares two bindings, and todos in memory carry theirs
// (Todo.Thread), so a lookup by thread needs no store.
type TodoThread = db.TodoThread

const (
	threadFieldMax = 256 // channel, chat_id and thread_id, in characters
	threadTitleMax = 200
)

// cleanThread is th as it is kept: the three fields that name the thread trimmed and present (the channel also in
// lower case, as channel ids are: "feishu", channel.Feishu), the title trimmed and clipped. The error says which field is wrong.
func cleanThread(th TodoThread) (TodoThread, error) {
	th.Channel, th.ChatID, th.ThreadID = strings.ToLower(strings.TrimSpace(th.Channel)), strings.TrimSpace(th.ChatID), strings.TrimSpace(th.ThreadID)
	for _, f := range []struct{ name, v string }{{"channel", th.Channel}, {"chat_id", th.ChatID}, {"thread_id", th.ThreadID}} {
		if f.v == "" {
			return TodoThread{}, fmt.Errorf("%s cannot be empty", f.name)
		}
		if utf8.RuneCountInString(f.v) > threadFieldMax {
			return TodoThread{}, fmt.Errorf("%s is longer than %d characters", f.name, threadFieldMax)
		}
	}
	th.Title = clip(strings.TrimSpace(th.Title), threadTitleMax)
	return th, nil
}

// postable: whether decision d may be posted into the chat thread its todo is bound to. What the user said in the thread
// is already there: posting it again would only echo it (and the bot's own post must not come back as a reply). Nor
// are the words the user wrote in the private chat as the answer to a "Please verify" card (a manual decision whose
// source message is a Lark message): what is said in the private chat stays in it.
func postable(d db.TodoDecisionRow) bool {
	if d.Source == db.DecisionManual && strings.HasPrefix(d.SourceMessage, "feishu-") {
		return false // the user's own words in the private chat, on the "Please verify" card: they stay there
	}
	return d.Source != db.DecisionThread
}

// sameThread: a and b name the same thread (the title is only a label).
func sameThread(a, b TodoThread) bool {
	return a.Channel == b.Channel && a.ChatID == b.ChatID && a.ThreadID == b.ThreadID
}

// bindThread binds todo id to th (already clean) in place of the thread it had, and tells the apps unless it had
// exactly that one (nothing is written then: an agent calling again does not stir the apps up). guard, when given,
// sees the todo as it is under the lock that orders the writes and can refuse (its error is returned as it is).
// db.ErrTodoNotFound for an unknown todo, db.ErrThreadTaken when another todo holds the thread.
func (s *Service) bindThread(ctx context.Context, id string, th TodoThread, guard func(Todo) error) (Todo, error) {
	out, changed, err := s.writeThread(ctx, id, &th, guard)
	if err != nil {
		return Todo{}, err
	}
	if changed {
		s.todosChanged(ctx)
	}
	return out, nil
}

// unbindThread removes todo id's thread; the apps are told only when it had one.
func (s *Service) unbindThread(ctx context.Context, id string) (Todo, error) {
	out, changed, err := s.writeThread(ctx, id, nil, nil)
	if err != nil {
		return Todo{}, err
	}
	if changed {
		s.todosChanged(ctx)
	}
	return out, nil
}

// writeThread is the write behind bindThread (th given) and unbindThread (nil): store first, then memory, both under
// the lock that makes memory take the rows in the order the store committed them (todoBook.writing).
func (s *Service) writeThread(ctx context.Context, id string, th *TodoThread, guard func(Todo) error) (Todo, bool, error) {
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()

	s.todos.mu.RLock()
	cur, ok := s.todos.todos[id]
	if !ok {
		s.todos.mu.RUnlock()
		return Todo{}, false, db.ErrTodoNotFound
	}
	snapshot := copyTodoLocked(cur)
	if th != nil {
		for oid, o := range s.todos.todos {
			if oid != id && o.Thread != nil && sameThread(*o.Thread, *th) {
				s.todos.mu.RUnlock()
				return Todo{}, false, db.ErrThreadTaken
			}
		}
	}
	s.todos.mu.RUnlock()
	if guard != nil {
		if err := guard(snapshot); err != nil {
			return Todo{}, false, err
		}
	}

	if th != nil && snapshot.Thread != nil && *snapshot.Thread == *th {
		return formatTodo(snapshot), false, nil
	}
	at := decisionNow()
	changed := th != nil || snapshot.Thread != nil
	if st := s.todoDB(); st != nil {
		var err error
		if th != nil {
			err = st.SetTodoThread(ctx, id, *th, at)
		} else {
			changed, err = st.ClearTodoThread(ctx, id, at)
		}
		if err != nil {
			return Todo{}, false, err
		}
	}

	s.todos.mu.Lock()
	defer s.todos.mu.Unlock()
	t, ok := s.todos.todos[id]
	if !ok {
		return Todo{}, false, db.ErrTodoNotFound // deleted meanwhile (DELETE does not wait for this lock)
	}
	if changed {
		t.Thread = th
		if th != nil {
			c := *th
			t.Thread = &c
		}
		if at.After(t.UpdatedAt) {
			t.UpdatedAt = at
		}
	}
	return formatTodo(copyTodoLocked(t)), changed, nil
}

func (s *Service) todoThreadRoutes() {
	// PUT /api/v1/todos/{id}/thread: bind the todo to a chat thread {channel, chat_id, thread_id, title?}, replacing
	// the one it had.
	s.handle("PUT /api/v1/todos/{id}/thread", func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.threadTodo(w, r)
		if !ok {
			return
		}
		var in TodoThread
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		th, err := cleanThread(in)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		t, err := s.bindThread(r.Context(), id, th, nil)
		if err != nil {
			threadErr(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"todo": t})
	})

	// DELETE /api/v1/todos/{id}/thread: unbind it (a todo with no thread stays as it is).
	s.handle("DELETE /api/v1/todos/{id}/thread", func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.threadTodo(w, r)
		if !ok {
			return
		}
		t, err := s.unbindThread(r.Context(), id)
		if err != nil {
			threadErr(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"todo": t})
	})
}

// threadTodo is the request's todo id once the caller may write todos, the todos are loaded and it is one of them;
// otherwise it answers.
func (s *Service) threadTodo(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
		return "", false
	}
	if err := s.ensureTodosLoaded(r.Context()); err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
		return "", false
	}
	id := r.PathValue("id")
	if _, ok := s.todoNow(id); !ok {
		apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
		return "", false
	}
	return id, true
}

func threadErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrTodoNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.Is(err, db.ErrThreadTaken):
		apierr.Write(w, http.StatusConflict, err)
	default:
		apierr.Write(w, http.StatusInternalServerError, err)
	}
}

// agentThreadTool runs todo_bind_thread for agent machine/session (#5279): it binds the open todo it works on to a
// chat thread. As with the decision tools the todo is the agent's own (an agent cannot name another's), and a todo
// that already has another thread keeps it: changing it is the user's call.
func (s *Service) agentThreadTool(ctx context.Context, machine, session string, args json.RawMessage) (string, error) {
	if session == "" { // an older node names no session for these tools
		return "", errors.New("Persona could not tell which agent session is calling (the node on this machine may be older than the hub)")
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", errTodosUnavailable
	}
	var a struct {
		TodoID string `json:"todo_id"`
		TodoThread
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	t, ok := s.openTodoOfAgent(machine, session)
	if !ok {
		return "You are not working on an open todo: there is nothing to bind a thread to.", nil
	}
	if id := strings.TrimSpace(a.TodoID); id != "" && id != t.ID {
		return "", fmt.Errorf("you can only bind the todo you work on (%s), not %s", t.ID, id)
	}
	th, err := cleanThread(a.TodoThread)
	if err != nil {
		return "", err
	}
	_, err = s.bindThread(ctx, t.ID, th, func(cur Todo) error {
		if cur.Thread != nil && !sameThread(*cur.Thread, th) {
			return errors.New("this todo is already bound to another thread: changing it is the user's call, ask them")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	s.logf("todo %s: %s bound a thread", t.ID, short(session))
	return fmt.Sprintf("Bound todo %s to that thread (one thread per todo; a thread can belong to one todo only).", t.ID), nil
}
