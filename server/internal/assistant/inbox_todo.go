package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Turning Inbox rows into to-dos by hand (#6480, docs/assistant/inbox.md "Add to to-dos"). The machine cannot read a
// colleague's deadline out of a message, so the user picks the due time (the client works it out in its own time zone and
// sends due_at, or due_date for a whole day, or neither); a to-do is made at once, titled like the row and pointing back
// at it (todos.inbox_item), and the row counts as handled: done_by=todo, "Added to your to-dos" on its chain, linked to
// the to-do. Undo deletes the to-do and brings the row back.

// doneByTodo is done_by for a row turned into a to-do.
const doneByTodo = "todo"

const inboxTodoTitleMax = 200 // characters of a to-do's title taken from a row

// fileTodo files an item as turned into todoID and records it on the chain. Under b.mu.
func (b *inboxBook) fileTodo(it *InboxItem, at time.Time, todoID string) {
	it.done(at, doneByTodo)
	e := InboxChainEntry{Kind: ChainDone, At: at.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain.done_todo"), By: doneByTodo}
	if todoID != "" {
		e.Link = &InboxChainLink{Type: LinkTodo, ID: todoID}
	}
	b.chainAdd(it, e)
}

type inboxTodoTarget struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
	// Title is what the user made of the row's title (the detail page's capsule lets them edit it); empty takes the row's.
	Title string `json:"title,omitempty"`
}

// inboxTodoMade is one to-do made from a row.
type inboxTodoMade struct {
	Item string `json:"item"`
	Todo Todo   `json:"todo"`
}

type inboxTodoReply struct {
	inboxBulkResult
	Todos []inboxTodoMade `json:"todos"`
	// Undo is the request body of POST /api/v1/inbox/todo/undo.
	Undo *inboxTodoUndo `json:"undo,omitempty"`
}

type inboxTodoUndo struct {
	Items []inboxTodoUndoItem `json:"items"`
}

type inboxTodoUndoItem struct {
	ID   string `json:"id"`
	Todo string `json:"todo_id"`
}

// todoFromItem is the title and notes a row gives its to-do.
func todoFromItem(it *InboxItem, title string) (string, string) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = strings.TrimSpace(it.Title)
	}
	notes := strings.TrimSpace(it.Summary)
	if notes == "" {
		notes = strings.TrimSpace(it.Preview)
	}
	return clipRunesTo(title, inboxTodoTitleMax), notes
}

// addInboxTodos makes a to-do of each open target (one each, sharing plan) and files the rows. A row that cannot be filed
// (gone, filed already, new messages since the list) gets no to-do: the to-do made for it is deleted again.
func (s *Service) addInboxTodos(ctx context.Context, targets []inboxTodoTarget, plan db.TodoEdit, now time.Time) (inboxTodoReply, error) {
	made := map[string]Todo{}
	var bulk []inboxBulkTarget
	todoOf := map[string]string{}
	b := &s.inbox
	for _, t := range targets {
		b.mu.Lock()
		it, ok := b.items[t.ID]
		var title, notes string
		if ok && it.open() {
			title, notes = todoFromItem(it, t.Title)
		}
		b.mu.Unlock()
		if title == "" {
			bulk = append(bulk, inboxBulkTarget{ID: t.ID, Version: t.Version}) // reported as missing / unchanged by the filing
			continue
		}
		td, err := s.createTodoFrom(ctx, title, notes, t.ID, plan)
		if err != nil {
			s.rollbackTodos(ctx, made)
			return inboxTodoReply{}, err
		}
		made[t.ID] = td
		todoOf[t.ID] = td.ID
		bulk = append(bulk, inboxBulkTarget{ID: t.ID, Version: t.Version})
	}
	res := s.setInboxBulkTodos(ctx, bulkTodo, bulk, nil, time.Time{}, now, todoOf)
	out := inboxTodoReply{inboxBulkResult: res, Todos: []inboxTodoMade{}}
	kept := map[string]bool{}
	for _, it := range res.Changed {
		kept[it.ID] = true
	}
	drop := map[string]Todo{}
	for id, td := range made {
		if kept[id] {
			out.Todos = append(out.Todos, inboxTodoMade{Item: id, Todo: td})
			out.Undo = &inboxTodoUndo{Items: append(undoItems(out.Undo), inboxTodoUndoItem{ID: id, Todo: td.ID})}
		} else {
			drop[id] = td
		}
	}
	s.rollbackTodos(ctx, drop)
	return out, nil
}

func undoItems(u *inboxTodoUndo) []inboxTodoUndoItem {
	if u == nil {
		return nil
	}
	return u.Items
}

func (s *Service) rollbackTodos(ctx context.Context, made map[string]Todo) {
	for _, td := range made {
		if err := s.deleteTodo(context.WithoutCancel(ctx), td.ID); err != nil {
			s.logf("inbox: to-do %s made for a row not kept, not deleted: %v", td.ID, err)
		}
	}
}

// undoInboxTodos deletes the to-dos made from rows and brings the rows back. A to-do that is gone already is not an
// error; one that belongs to another row is left alone.
func (s *Service) undoInboxTodos(ctx context.Context, items []inboxTodoUndoItem, now time.Time) inboxBulkResult {
	var back []inboxBulkTarget
	for _, u := range items {
		if td, ok := s.todoNow(u.Todo); ok && td.InboxItem == u.ID {
			if err := s.deleteTodo(ctx, u.Todo); err != nil {
				s.logf("inbox: to-do %s not deleted by Undo: %v", u.Todo, err)
			}
		}
		back = append(back, inboxBulkTarget{ID: u.ID})
	}
	return s.setInboxBulk(ctx, bulkReopen, back, nil, time.Time{}, now)
}

func (s *Service) inboxTodoRoutes() {
	// {"items": [{"id", "version"?, "title"?}], "due_at"? | "due_date"?}: one to-do per row, the same due time.
	s.handle("POST /api/v1/inbox/todo", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, i18n.ErrEnglish("inbox-errors.todo_read_only"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		var in struct {
			Items []inboxTodoTarget `json:"items"`
			todoPlanIn
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		switch {
		case len(in.Items) == 0:
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.todo_targets"))
			return
		case len(in.Items) > inboxBulkMax:
			apierr.Write(w, http.StatusRequestEntityTooLarge, errBulkTooMany)
			return
		}
		var plan db.TodoEdit
		in.Priority = nil
		if err := in.todoPlanIn.edit(&plan); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		out, err := s.addInboxTodos(r.Context(), in.Items, plan, time.Now())
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, out)
	})
	s.handle("POST /api/v1/inbox/todo/undo", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, i18n.ErrEnglish("inbox-errors.todo_read_only"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		var in inboxTodoUndo
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil || len(in.Items) == 0 {
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.todo_targets"))
			return
		}
		if len(in.Items) > inboxBulkMax {
			apierr.Write(w, http.StatusRequestEntityTooLarge, errBulkTooMany)
			return
		}
		apierr.WriteJSON(w, s.undoInboxTodos(r.Context(), in.Items, time.Now()))
	})
}

// checkSuggestedDue holds a to-do suggestion's due time to its two forms: a day (2006-01-02) or a moment (RFC 3339).
func checkSuggestedDue(due string) error {
	if due == "" {
		return nil
	}
	if _, err := time.Parse(dayLayout, due); err == nil {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, due); err == nil {
		return nil
	}
	return errSuggestion{"due is a day (2006-01-02, all day) or a moment (RFC 3339 with its offset), or leave it out"}
}

// planOfDue is the due part of a to-do from a suggestion's due time.
func planOfDue(due string) db.TodoEdit {
	var e db.TodoEdit
	if due == "" {
		return e
	}
	e.SetDue = true
	if d, err := time.Parse(dayLayout, due); err == nil {
		e.DueDate = &d
	} else if at, err := time.Parse(time.RFC3339, due); err == nil {
		u := at.UTC()
		e.DueAt = &u
	}
	return e
}

// confirmTodo adds the confirmed to-do suggestion (title text, due as the user left it) and files the row as turned into
// it, as the manual way does: one tap, nothing more to ask.
func (s *Service) confirmTodo(ctx context.Context, it *InboxItem, text, due string) (InboxItem, error) {
	title := clipRunesTo(strings.TrimSpace(text), inboxTodoTitleMax)
	if err := checkSuggestedDue(due); err != nil || title == "" {
		return InboxItem{}, errTodoSuggestion
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return InboxItem{}, errTodosUnavailable
	}
	b := &s.inbox
	b.mu.Lock()
	_, notes := todoFromItem(it, "")
	b.mu.Unlock()
	td, err := s.createTodoFrom(ctx, title, notes, it.ID, planOfDue(due))
	if err != nil {
		return InboxItem{}, err
	}
	res := s.setInboxBulkTodos(ctx, bulkTodo, []inboxBulkTarget{{ID: it.ID}}, nil, time.Time{}, time.Now(), map[string]string{it.ID: td.ID})
	if len(res.Changed) == 0 {
		s.rollbackTodos(ctx, map[string]Todo{it.ID: td})
		return InboxItem{}, errInboxNotPersisted
	}
	return res.Changed[0], nil
}

var errTodoSuggestion = i18n.ErrEnglish("inbox-errors.todo_suggestion")
