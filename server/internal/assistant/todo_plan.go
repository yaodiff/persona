package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// A todo's plan (#1683, docs/assistant/todos.md "Due date, reminder, priority"): when it is due (a moment, or a day in the
// user's time zone), when the phone is reminded of it, and its priority. The apps set them through POST / PATCH
// /api/v1/todos, the assistant through its todo_* tools (todo_tools.go). remindLoop pushes each reminder once, when it is
// due: claimed in the store first, so neither a restart nor a second manager pushes it again.

// dayLayout is how a due day is written (the API's due_date, the tools' due).
const dayLayout = "2006-01-02"

// setPlan takes the plan of row (a stored todo, or one made in memory), the project it is in (todo_project.go), when
// the owner last saw it (todo_seen.go) and their restore and Someday (todo_marks.go).
func (t *Todo) setPlan(row db.TodoRow) {
	t.DueAt, t.DueDate = row.DueAt, ""
	if row.DueDate != nil {
		t.DueDate = row.DueDate.UTC().Format(dayLayout)
	}
	t.RemindAt, t.RemindedAt, t.Priority = row.RemindAt, row.RemindedAt, row.Priority
	t.Project = row.Project
	t.Kind = row.Kind
	t.SeenAt = row.SeenAt
	t.AutoSeenUndone = row.AutoSeenUndone
	t.RestoredAt, t.SomedayUntil = row.RestoredAt, row.SomedayUntil
	t.InboxItem = row.InboxItem
	t.PartOf, t.After = row.PartOf, slices.Clone(row.After)
	t.RequireAcceptedAfter = row.RequireAcceptedAfter
	t.Responsible = cloneResponsible(row.Responsible)
	t.Audience = row.Audience
	t.DirectionID = row.DirectionID
	t.ItemType = row.ItemType
}

// keepPlan writes t's plan, project and the owner's marks (seen, restored, Someday) into row: a row made in memory for a
// change that is not to them keeps them.
func (t *Todo) keepPlan(row *db.TodoRow) {
	row.DueAt, row.DueDate = t.DueAt, nil
	if d, err := time.Parse(dayLayout, t.DueDate); err == nil {
		row.DueDate = &d
	}
	row.RemindAt, row.RemindedAt, row.Priority = t.RemindAt, t.RemindedAt, t.Priority
	row.Project = t.Project
	row.Kind = t.Kind
	row.SeenAt = t.SeenAt
	row.AutoSeenUndone = t.AutoSeenUndone
	row.RestoredAt, row.SomedayUntil = t.RestoredAt, t.SomedayUntil
	row.InboxItem = t.InboxItem
	row.PartOf, row.After = t.PartOf, slices.Clone(t.After)
	row.RequireAcceptedAfter = t.RequireAcceptedAfter
	row.Responsible = cloneResponsible(t.Responsible)
	row.Audience = t.Audience
	row.DirectionID = t.DirectionID
	row.ItemType = t.ItemType
}

// todoRowOf is todo t as a row (what memory has of it; the store-less PATCH changes it).
func todoRowOf(t Todo) db.TodoRow {
	row := db.TodoRow{ID: t.ID, Title: t.Title, Notes: t.Notes, DoneAt: t.DoneAt, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		Origin: t.Origin, PlanID: t.PlanID, StartError: t.StartError, DoneBy: t.DoneBy, DoneReason: t.DoneReason,
		ReopenedAt: t.ReopenedAt, Quiet: t.Quiet}
	t.keepPlan(&row)
	return row
}

// dueBy is the moment todo t is due by in loc: its due_at, or the end of its due day there; false without either.
func (t Todo) dueBy(loc *time.Location) (time.Time, bool) {
	if t.DueAt != nil {
		return *t.DueAt, true
	}
	if d, err := time.ParseInLocation(dayLayout, t.DueDate, loc); err == nil {
		return d.AddDate(0, 0, 1).Add(-time.Second), true
	}
	return time.Time{}, false
}

// overdue: t is open and its due moment has passed.
func (t Todo) overdue(now time.Time, loc *time.Location) bool {
	by, ok := t.dueBy(loc)
	return ok && t.DoneAt == nil && now.After(by)
}

// dueText is t's due date as the user reads it ("" without one): "11-12 18:00" or the day "11-12".
func (t Todo) dueText(loc *time.Location) string {
	if t.DueAt != nil {
		return t.DueAt.In(loc).Format("01-02 15:04")
	}
	if d, err := time.Parse(dayLayout, t.DueDate); err == nil {
		return d.Format("01-02")
	}
	return ""
}

// todoPlanIn is the plan in a POST or PATCH body: due_at (RFC 3339) or due_date (YYYY-MM-DD), remind_at (RFC 3339),
// priority. A field left out is kept, null (or "") clears it.
type todoPlanIn struct {
	DueAt       json.RawMessage `json:"due_at"`
	DueDate     json.RawMessage `json:"due_date"`
	RemindAt    json.RawMessage `json:"remind_at"`
	Priority    *string         `json:"priority"`
	Audience    *string         `json:"audience"`
	DirectionID *string         `json:"direction_id"`
	ItemType    *string         `json:"item_type"`
}

// edit puts the plan into e; an error says which field could not be read.
func (p todoPlanIn) edit(e *db.TodoEdit) error {
	if p.DueAt != nil || p.DueDate != nil {
		at, err := optionalField(p.DueAt, "due_at", func(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) })
		if err != nil {
			return err
		}
		day, err := optionalField(p.DueDate, "due_date", func(s string) (time.Time, error) { return time.Parse(dayLayout, s) })
		if err != nil {
			return err
		}
		if at != nil && day != nil {
			return errors.New("give due_at or due_date, not both")
		}
		e.SetDue, e.DueAt, e.DueDate = true, utcPtr(at), day
	}
	if p.RemindAt != nil {
		at, err := optionalField(p.RemindAt, "remind_at", func(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) })
		if err != nil {
			return err
		}
		e.SetRemind, e.RemindAt = true, utcPtr(at)
	}
	if p.Priority != nil {
		pr, err := todoPriority(*p.Priority)
		if err != nil {
			return err
		}
		e.Priority = &pr
	}
	if p.Audience != nil {
		aud, err := todoAudience(*p.Audience)
		if err != nil {
			return err
		}
		e.Audience = &aud
	}
	if p.DirectionID != nil {
		dir := strings.TrimSpace(*p.DirectionID)
		e.DirectionID = &dir
	}
	return nil
}

// optionalField reads a JSON string field with parse: nil for null or "".
func optionalField(raw json.RawMessage, name string, parse func(string) (time.Time, error)) (*time.Time, error) {
	if raw == nil || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%s must be a string or null", name)
	}
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	t, err := parse(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("can't read %s %q", name, s)
	}
	return &t, nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// todoPriority is priority as it is stored: "" for normal (or none given), high, low.
func todoPriority(p string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", "normal":
		return "", nil
	case db.PriorityHigh:
		return db.PriorityHigh, nil
	case db.PriorityLow:
		return db.PriorityLow, nil
	}
	return "", fmt.Errorf("priority must be high, normal or low, not %q", p)
}

// todoAudience validates an optional to-do audience: "", "human", or "autonomous" (#14619).
func todoAudience(a string) (string, error) {
	trimmed := strings.TrimSpace(a)
	if err := db.ValidateTodoAudience(trimmed); err != nil {
		return "", err
	}
	return trimmed, nil
}

// createPlainTodo makes a todo of the user's (no conversation, no plan of tasks) with plan's due date, reminder and
// priority, and tells the apps.
func (s *Service) createPlainTodo(ctx context.Context, title, notes string, plan db.TodoEdit, bindings ...*repository.CreateBindings) (Todo, error) {
	return s.createPlainTodoWithImages(ctx, title, notes, plan, nil, bindings...)
}

func (s *Service) createPlainTodoWithImages(ctx context.Context, title, notes string, plan db.TodoEdit, images []db.TodoImage, bindings ...*repository.CreateBindings) (Todo, error) {
	return s.createTodoFromWithImages(ctx, title, notes, "", plan, images, bindings...)
}

// createTodoFrom is createPlainTodo for a to-do made from an Inbox row (#6480): inboxItem is that row's id, "" for none.
func (s *Service) createTodoFrom(ctx context.Context, title, notes, inboxItem string, plan db.TodoEdit, bindings ...*repository.CreateBindings) (Todo, error) {
	return s.createTodoFromWithImages(ctx, title, notes, inboxItem, plan, nil, bindings...)
}

func (s *Service) createTodoFromWithImages(ctx context.Context, title, notes, inboxItem string, plan db.TodoEdit, images []db.TodoImage, bindings ...*repository.CreateBindings) (Todo, error) {
	now := time.Now().UTC()
	row := db.TodoRow{ID: "todo_" + randomHex(8), Title: title, Notes: notes, CreatedAt: now, UpdatedAt: now, InboxItem: inboxItem, Images: images}
	plan.Title, plan.Notes, plan.Done = nil, nil, nil
	plan.Apply(&row, now)
	return s.insertTodo(ctx, row, bindings...)
}

// createDirectionTodo makes a piece of leader's direction (#5846): a todo with plan's due date, reminder, priority and
// project, opened by the leader and supervised by it, with no agent and no done_when, as a Lark topic opens one
// (the lark module's thread_open.go). So the direction tree lists it under that direction (as not started until an agent works on
// it, direction_tree.go), and task_plan's todo_id can run an agent on it later instead of opening a second todo.
func (s *Service) createDirectionTodo(ctx context.Context, leader, title, notes string, plan db.TodoEdit, bindings ...*repository.CreateBindings) (Todo, error) {
	now := time.Now().UTC()
	row := db.TodoRow{ID: "todo_" + randomHex(8), Title: title, Notes: notes, CreatedAt: now, UpdatedAt: now,
		Origin: leader, Quiet: s.quietTask(ctx, leader)}
	plan.Title, plan.Notes, plan.Done = nil, nil, nil
	plan.Apply(&row, now)
	t, err := s.insertTodo(ctx, row, bindings...)
	if err != nil {
		return Todo{}, err
	}
	if err := s.superviseTodo(ctx, t.ID, leader, nil, 0); err != nil {
		if rerr := s.removeTodo(context.WithoutCancel(ctx), t.ID); rerr != nil {
			s.logf("todo %s: take back after supervision failure: %v", t.ID, rerr)
		}
		return Todo{}, err
	}
	if cur, ok := s.todoNow(t.ID); ok {
		t = cur
	}
	return t, nil
}

// createPieceTodo makes a piece of a request that conversation will hand out itself (todo_pieces.go): a to-do with
// plan's links, opened by that conversation, with no agent, so that its task_plan's todo_id can run an Agent on it when
// its turn comes (reusableTodo) instead of opening a second to-do. Unlike a leader's piece (createDirectionTodo) it is
// not supervised until an Agent works on it: the plan that starts it sets that up.
func (s *Service) createPieceTodo(ctx context.Context, conversation, title, notes string, plan db.TodoEdit, bindings ...*repository.CreateBindings) (Todo, error) {
	now := time.Now().UTC()
	row := db.TodoRow{ID: "todo_" + randomHex(8), Title: title, Notes: notes, CreatedAt: now, UpdatedAt: now,
		Origin: conversation, Quiet: s.quietTask(ctx, conversation)}
	plan.Title, plan.Notes, plan.Done = nil, nil, nil
	plan.Apply(&row, now)
	return s.insertTodo(ctx, row, bindings...)
}

// editTodo makes edit e to todo id as the user's PATCH does: stored, put into memory, the apps and the hub told; a
// todo it closes has its outcome recorded and a question's to-do answered as dropped. db.ErrTodoNotFound when it is not
// there.
func (s *Service) editTodo(ctx context.Context, id string, e db.TodoEdit) (Todo, error) {
	return s.editTodoWithCloseActivity(ctx, id, e, false)
}

func (s *Service) editTodoWithCloseActivity(ctx context.Context, id string, e db.TodoEdit, checkActivity bool) (Todo, error) {
	if e.Audience != nil {
		if err := db.ValidateTodoAudience(*e.Audience); err != nil {
			return Todo{}, err
		}
	}
	if e.ItemType != nil {
		if err := db.ValidateTodoItemType(strings.TrimSpace(*e.ItemType)); err != nil {
			return Todo{}, err
		}
	}
	cur, ok := s.todoNow(id)
	if !ok {
		return Todo{}, db.ErrTodoNotFound
	}
	if e.Done != nil && *e.Done && cur.DoneAt == nil && s.outsideBackingPending(id) {
		return Todo{}, errOutsideDecisionPending
	}
	if cur.isConcern() && cur.DoneAt == nil && e.Done != nil && *e.Done {
		return Todo{}, db.ErrConcernUnreported // only its report closes it (concerns.go); the store checks it again
	}
	links := e.PartOf != nil || e.After != nil || e.RequireAcceptedAfter != nil
	if f := s.pieceWriteHook; links && f != nil {
		f(id)
	}
	if f := s.editWriteHook; f != nil {
		f(id)
	}
	reopening := e.Done != nil && !*e.Done
	var fencedSessions [][2]string
	if reopening {
		fencedSessions = todoProtectedSessions(cur, e.Responsible)
	} else if e.SetResponsible && e.Responsible != nil && e.Responsible.Kind == db.ResponsibleSession && e.Responsible.SessionID != "" {
		fencedSessions = [][2]string{{e.Responsible.MachineID, e.Responsible.SessionID}}
	}
	// The idle-close fence is checked with the to-do writer held. Waiting releases it before taking the ledger
	// writer, so terminal review and guarded park can still enter in to-do → ledger order.
	if err := s.lockTodoWritingForSessions(ctx, fencedSessions); err != nil {
		return Todo{}, err
	}
	if reopening {
		s.supervise.writeMu.Lock()
	}
	unlock := func() {
		if reopening {
			s.supervise.writeMu.Unlock()
		}
		s.todos.writing.Unlock()
	}
	if e.Done != nil && *e.Done {
		if current, ok := s.todoNow(id); ok && current.DoneAt == nil {
			if err := s.investigationCloseError(ctx, current); err != nil {
				unlock()
				return Todo{}, err
			}
		}
	}
	if links {
		// Checked again with the graph held still: a write since the caller's check may have changed the request
		// (todo_pieces.go). A *pieceError when it no longer holds.
		l, err := s.recheckPieces(id, e)
		if err != nil {
			unlock()
			return Todo{}, err
		}
		e.PartOf, e.After = l.PartOf, l.After
	}
	now := time.Now().UTC()
	var closeGuard func(Todo) error
	if e.Done != nil && *e.Done {
		closeGuard = func(live Todo) error {
			if checkActivity {
				return s.taskCloseSnapshotError(live, time.Now())
			}
			if work := s.unconfirmedTaskClose(ctx, live); work != nil {
				return &taskCloseHoldError{todo: id, work: work}
			}
			return nil
		}
		e.CloseGuard = func() error {
			if live, ok := s.todoNow(id); ok {
				return closeGuard(live)
			}
			return db.ErrTodoNotFound
		}
		if err := e.CloseGuard(); err != nil {
			unlock()
			return Todo{}, err
		}
	}
	var row *db.TodoRow
	if st := s.todoDB(); st != nil {
		var err error
		if row, err = st.EditTodo(ctx, id, e, now); err != nil {
			unlock()
			return Todo{}, err
		}
	} else {
		// as the writes before this one left it, not as it was before the lock; deleted since: not there (never made
		// again from the copy read before the lock)
		base, ok := s.todoNow(id)
		if !ok {
			unlock()
			return Todo{}, db.ErrTodoNotFound
		}
		r := todoRowOf(base)
		e.Apply(&r, now)
		row = &r
	}
	var beforeWrite func(*Todo) bool
	var hold error
	if closeGuard != nil && s.todoDB() == nil {
		beforeWrite = func(live *Todo) bool {
			hold = closeGuard(*live)
			return hold == nil
		}
	}
	out, doneKeys, applied := s.setTodoRow(id, *row, beforeWrite)
	unlock()
	if hold != nil {
		return Todo{}, hold
	}
	if applied {
		s.todoRowSet(ctx, doneKeys)
	}
	if cur.DoneAt == nil && row.DoneAt != nil {
		// Done by the user: what the task came to goes in its decisions and its projects' timelines (#2820). cur
		// still has the agents that were working on it.
		s.goRecordTaskOutcome(cur, *row.DoneAt)
		s.todoPushOver(id)
		s.askTodoClosed(context.WithoutCancel(ctx), id, row.Title) // a question's to-do closed unanswered (ask_todo.go)
		maySatisfyAfter := row.StartError == "" && row.DoneBy != db.DoneByCancelled
		s.onTodoWorkspaceReleased(ctx, cur, maySatisfyAfter)
		if maySatisfyAfter {
			s.evaluateTodoAfterDependents(ctx, id)
			s.evaluateParentCloseoutForChild(ctx, id)
		}
	}
	if reopening && row.DoneAt == nil && (cur.DoneAt != nil || e.RestartWork ||
		row.ReopenedAt != nil && (cur.ReopenedAt == nil || !row.ReopenedAt.Equal(*cur.ReopenedAt))) {
		s.resetTodoReadyWake(ctx, id)
		s.resetParentCloseoutWake(ctx, id)
	}
	return out, nil
}

// deleteTodo deletes todo id as the user's DELETE does; db.ErrTodoNotFound when it is not there.
func (s *Service) deleteTodo(ctx context.Context, id string) error {
	cur, ok := s.todoNow(id)
	if !ok {
		return db.ErrTodoNotFound
	}
	if s.outsideBackingPending(id) {
		return errOutsideDecisionPending
	}
	if err := s.removeTodo(ctx, id); err != nil {
		return err
	}
	s.todoPushOver(id)
	s.askTodoClosed(context.WithoutCancel(ctx), id, cur.Title) // a question's to-do deleted unanswered (ask_todo.go)
	s.onTodoWorkspaceReleased(ctx, cur, false)
	return nil
}

// ---- reminders ----

var (
	// remindIdle is how long remindLoop sleeps at most: it wakes early when the todos change.
	remindIdle = time.Minute
	// remindLate: a reminder pushed this much after its time (the manager was down) says when it was for.
	remindLate = 2 * time.Minute
)

// remindWakeup is the channel that wakes remindLoop (the todos changed).
func (b *todoBook) remindWakeup() chan struct{} {
	b.remindOnce.Do(func() { b.remindWake = make(chan struct{}, 1) })
	return b.remindWake
}

// remindSoon has remindLoop look again now.
func (s *Service) remindSoon() {
	select {
	case s.todos.remindWakeup() <- struct{}{}:
	default:
	}
}

// remindLoop pushes the todos' reminders when they are due, sleeping until the next one.
func (s *Service) remindLoop(ctx context.Context) {
	wake := s.todos.remindWakeup()
	for {
		wait := remindIdle
		if s.todoDB() != nil && s.ensureTodosLoaded(ctx) == nil || s.todosLoaded() {
			if next := s.remindDue(ctx, time.Now()); !next.IsZero() {
				wait = min(wait, max(time.Until(next), 50*time.Millisecond))
			}
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// remindDue pushes the reminders due by now and returns when the next one is (zero for none). A mesh member's
// wait while the center pushes the group's (docs/server/mesh.md), as its scheduled tasks do.
func (s *Service) remindDue(ctx context.Context, now time.Time) time.Time {
	type due struct {
		id string
		at time.Time
	}
	var dues []due
	var next time.Time
	s.todos.mu.RLock()
	for id, t := range s.todos.todos {
		if t.RemindAt == nil || t.RemindedAt != nil || t.DoneAt != nil {
			continue
		}
		if !t.RemindAt.After(now) {
			dues = append(dues, due{id, *t.RemindAt})
		} else if next.IsZero() || t.RemindAt.Before(next) {
			next = *t.RemindAt
		}
	}
	s.todos.mu.RUnlock()
	if len(dues) == 0 || s.hello(ctx).Assistant.Standby {
		return next
	}
	slices.SortFunc(dues, func(a, b due) int { return a.at.Compare(b.at) })
	for _, d := range dues {
		s.remind(ctx, d.id, d.at, now)
	}
	return next
}

// remind pushes todo id's reminder for at, once: claimed in the store first (another manager, or this one before a
// restart, may have pushed it), marked in memory either way so it is not looked at again.
func (s *Service) remind(ctx context.Context, id string, at, now time.Time) {
	claimed := true
	if st := s.todoDB(); st != nil {
		ok, err := st.ClaimTodoReminder(ctx, id, at, now.UTC())
		if err != nil {
			s.logf("todo %s: reminder: %v", id, err) // looked at again on the next round
			return
		}
		claimed = ok
	}
	s.todos.mu.Lock()
	t, ok := s.todos.todos[id]
	if !ok || t.RemindAt == nil || !t.RemindAt.Equal(at) || t.RemindedAt != nil {
		s.todos.mu.Unlock()
		return
	}
	stamp := now.UTC()
	t.RemindedAt = &stamp
	todo := copyTodoLocked(t)
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
	if !claimed {
		return
	}
	title, body := s.reminderText(ctx, todo, at, now)
	larkTitle, larkBody := s.reminderLarkText(ctx, todo, at, now)
	set := s.settings(ctx)
	// Decided once, on the fullest copy's words, so the phone, the channels and Feishu agree.
	private, known, space := s.todoReminderPrivate(ctx, todo, larkTitle+"\n"+larkBody)
	channelTitle, channelBody := s.reminderCopyText(ctx, private, space, title, body)
	// A reminder the owner set for themself: theirs to act on, so P0 even in the hub's low-distraction mode, keyed by
	// to-do and due time (each due time rings once).
	s.push(withCard(ctx, cardTodoRemind), Notification{Subtitle: i18n.T(s.lang(ctx), "assistant-todos.remind_subtitle"), DeviceID: set.MachineID,
		SessionID: set.Main, Title: channelTitle, Body: channelBody, Level: assistantwire.LevelP0,
		RelatedKey: "todo-remind:" + id + "@" + at.UTC().Format(time.RFC3339)})
	// Only the Feishu copy gets the full notes, to-do deep link and reply context. Ordinary phone and other-channel
	// copies keep their existing short text; the content of a task from a space that redacts stays behind the generic
	// notice on every copy.
	// Feishu's copy, which carries the notes and a link, also stays behind the notice when the origin cannot be told,
	// with the notice it always had then.
	to := assistantwire.SessionRef{MachineID: set.MachineID, SessionID: set.Main}
	reminder := s.larkReminder(ctx, to, todo, larkTitle, larkBody, private, known, space)
	go s.larkModule().TodoReminder(to, reminder)
	go s.channelPush(set.MachineID, set.Main, channelTitle, channelBody)
	s.logf("todo %s: reminded (due %s)", id, at.Format(time.RFC3339))
}

// todoReminderPrivate is whether a reminder of t saying text leaves as only a notice (exitPolicy: the text's own
// categories and its origin track's), whether its origin could be told, and its track.
func (s *Service) todoReminderPrivate(ctx context.Context, t Todo, text string) (private, known bool, space string) {
	space, known = s.selfSpace(ctx, t.Origin)
	return s.exitPolicy(ctx, text, t.Origin, space).Redact, known, space
}

// reminderChannelText applies the outbound rule to the phone and channel copies of t's reminder saying title and
// body; ordinary text is unchanged.
func (s *Service) reminderChannelText(ctx context.Context, t Todo, title, body string) (string, string) {
	private, _, space := s.todoReminderPrivate(ctx, t, title+"\n"+body)
	return s.reminderCopyText(ctx, private, space, title, body)
}

// reminderCopyText is a reminder's phone or channel copy: only a notice when it is private (from track space).
func (s *Service) reminderCopyText(ctx context.Context, private bool, space, title, body string) (string, string) {
	if private {
		return s.redactedNotice(ctx, s.lang(ctx), space)
	}
	return title, body
}

// reminderText is the push of t's reminder for at, pushed at now: its title, and its due date, the first line of its
// notes and how late it is in the body.
func (s *Service) reminderText(ctx context.Context, t Todo, at, now time.Time) (title, body string) {
	return s.reminderTextWithNotes(ctx, t, at, now, clip(oneLine(t.Notes), 120), " · ")
}

// reminderLarkText keeps the notes exactly as saved, including line breaks. answerCards paginates them for Feishu;
// the to-do link remains the way to read anything beyond the existing card budget.
func (s *Service) reminderLarkText(ctx context.Context, t Todo, at, now time.Time) (title, body string) {
	return s.reminderTextWithNotes(ctx, t, at, now, t.Notes, "\n\n")
}

func (s *Service) reminderTextWithNotes(ctx context.Context, t Todo, at, now time.Time, notes, sep string) (title, body string) {
	l, loc := s.lang(ctx), s.location(ctx)
	var parts []string
	if due := t.dueText(loc); due != "" {
		key := "assistant-todos.remind_due"
		if t.overdue(now, loc) {
			key = "assistant-todos.remind_overdue"
		}
		parts = append(parts, i18n.T(l, key, i18n.Args{"when": due}))
	}
	if strings.TrimSpace(notes) != "" {
		parts = append(parts, notes)
	}
	if now.Sub(at) > remindLate {
		parts = append(parts, i18n.T(l, "assistant-todos.remind_late", i18n.Args{"when": at.In(loc).Format("01-02 15:04")}))
	}
	if len(parts) == 0 {
		parts = append(parts, i18n.T(l, "assistant-todos.remind_body"))
	}
	return clip(t.Title, 120), strings.Join(parts, sep)
}
