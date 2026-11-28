package assistant

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// The completion check on a task (#3180, docs/assistant/todos.md "Automatic completion"): every automatic judgment of a
// task's work (autoFinishTask, done.go) is kept on the todo as its check, the verdict with its reason in the user's
// language, so an open task whose agents stopped can say why it is not done ("Stopped, not done yet · waiting for CI").
// It is kept with the task's stage and delivery (reportStagesKey: no migration), and only while the judgment is still
// current. A new turn of one of its agents (taskWorking) or a reopen (setTodoRow) clears it: it judged earlier work.

// TodoCheck is the hub's latest automatic completion judgment of a task (Todo.Check): Verdict one of done, not_done,
// waiting, needs_user, undelivered, unsure; Reason the judge's one line ("" when it gave none); At when it was judged;
// WorkSince the start of the stretch of work it judged.
type TodoCheck struct {
	Verdict   string    `json:"verdict"`
	Reason    string    `json:"reason,omitempty"`
	At        time.Time `json:"at"`
	WorkSince time.Time `json:"work_since"`
}

// Reasons the hub gives itself when it decides without the model (taskDoneWithoutModel), by i18n key: what is kept
// on the check is in the user's language, while the judgment's own Reason stays as the supervisor reads it.
const (
	checkReasonAgyUnlinked = "assistant-tasks.check_agy_unlinked"
	checkReasonNoReply     = "assistant-tasks.check_no_reply"
	checkReasonSideTurn    = "assistant-tasks.check_side_turn"
	checkReasonInterrupted = "assistant-tasks.interrupted"
	doneReasonFallback     = "assistant-tasks.done" // a done verdict with no reason
)

// checkOf is judgment c of the work since work, as the todo keeps it, in l.
func checkOf(l i18n.Lang, c taskDoneCheck, work, at time.Time) TodoCheck {
	reason := c.Reason
	if c.reasonKey != "" {
		reason = i18n.T(l, c.reasonKey)
	}
	if reason == "" && c.Verdict == verdictDone {
		reason = i18n.T(l, doneReasonFallback)
	}
	return TodoCheck{Verdict: c.Verdict, Reason: reason, At: at, WorkSince: work}
}

// keepTaskCheck keeps judgment c of t's work since work on the todo, unless a new turn started meanwhile (a turn that
// starts after this takes it off again, taskWorking).
func (s *Service) keepTaskCheck(ctx context.Context, t Todo, work time.Time, c taskDoneCheck) {
	s.tasks.completionMu.Lock()
	defer s.tasks.completionMu.Unlock()
	if !s.taskWorkCurrent(t.ID, work) {
		return
	}
	check := checkOf(s.lang(ctx), c, work, time.Now().UTC())
	if s.storeTaskCheck(ctx, t.ID, &check) {
		s.todosChanged(ctx)
	}
}

// clearTaskCheck takes task id's check off, and tells the apps: a new turn of one of its agents starts work the check
// did not judge.
func (s *Service) clearTaskCheck(ctx context.Context, id string) {
	if s.storeTaskCheck(ctx, id, nil) {
		s.todosChanged(ctx)
	}
}

// storeTaskCheck keeps check as task id's check, with its stages (the same setting), and on the todo; nil takes it
// off. changed: there was something to write (the apps are not told here).
func (s *Service) storeTaskCheck(ctx context.Context, id string, check *TodoCheck) (changed bool) {
	b := &s.reports
	b.mu.Lock()
	if !s.loadStagesLocked(ctx) {
		b.mu.Unlock()
		s.logf("task %s: check not kept: the stages could not be read", id)
		return false
	}
	cur := b.stages[id]
	if check == nil && cur.Check == nil {
		b.mu.Unlock()
		return false
	}
	cur.Check = check
	if cur.empty() {
		delete(b.stages, id)
	} else {
		b.stages[id] = cur
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, reportStagesKey, b.stages); err != nil {
			s.logf("task %s: check: %v", id, err)
		}
	}
	b.mu.Unlock()
	s.todos.mu.Lock()
	if t := s.todos.todos[id]; t != nil {
		t.Check = check
	}
	s.todos.mu.Unlock()
	return true
}

// dropReopenedCheck takes the check off a todo that was reopened (setTodoRow, which tells the apps itself): the work
// it judged is over.
func (s *Service) dropReopenedCheck(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.storeTaskCheck(ctx, id, nil)
}
