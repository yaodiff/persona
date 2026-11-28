package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

// A supervisor takes back the needs_user it raised (task_review accepted withdraw_needs_user,
// docs decision rights: only six kinds of things go to the user; the rest is the supervisor's): the task is accepted
// and closed, nothing new reaches the user, the hub is told the to-do is over (its next push may ring), and its
// decision card ends neutral, never as the user's choice. The push already on the phone stays: it carries no card id.

// toldResolved reports whether the assistant told the hub that card id is over (EventFleetResolved).
func toldResolved(f *fakeHub, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.told {
		var r assistantwire.FleetResolved
		if e.event == assistantwire.EventFleetResolved && json.Unmarshal(e.params, &r) == nil && r.ID == id {
			return true
		}
	}
	return false
}

// withdrawArgs is a supervisor's withdrawal of its own needs_user.
func withdrawArgs() map[string]any {
	return map[string]any{"verdict": "accepted", "withdraw_needs_user": true,
		"note": "督办自己定了：按先灰度 10% 做完了，不用再问用户"}
}

func TestSupervisorWithdrawsItsNeedsUserQuietly(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	database := todoTestDB(t)
	s, f, todo, posts, updates := setupNeedsUserTaskWithLark(t, database)
	card := assistantwire.TodoAskCardID(todo.ID)
	decided := decidePushes(t, f)
	if len(decided) != 1 || decided[0]["related_key"] != assistantwire.TodoPushKey(todo.ID, assistantwire.TodoPushDecide) || decided[0]["card_id"] != nil {
		t.Fatalf("the needs_user's decision push = %v, want one keyed by the to-do and reason, without a card id", decided)
	}
	pushedBefore := len(f.pushed())
	conclusionsBefore := len(cardsWith(posts(), "结论："))
	if toldResolved(f, card) {
		t.Fatal("the hub was told the to-do is over before the withdrawal")
	}

	res := reviewTask(t, s, todo.ID, withdrawArgs())

	got := todoNow(s, todo.ID)
	if got.DoneAt == nil || got.DoneBy != db.DoneByReview || got.Supervision.State != reviewAccepted {
		t.Fatalf("after the withdrawal: done=%v by=%q state=%q, want accepted and closed", got.DoneAt, got.DoneBy, got.Supervision.State)
	}
	r := triageLatestReview(got.Supervision)
	if r == nil || r.Verdict != reviewAccepted || !r.WithdrewNeedsUser || r.SupersededReason == "" {
		t.Fatalf("latest review %+v, want an accepted withdrawal with its reason", r)
	}
	if n := len(f.pushed()); n != pushedBefore {
		t.Fatalf("the withdrawal pushed %d more: %+v", n-pushedBefore, f.pushed()[pushedBefore:])
	}
	if n := len(cardsWith(posts(), "结论：")); n != conclusionsBefore {
		t.Fatalf("the withdrawal posted %d more conclusion cards", n-conclusionsBefore)
	}
	s.reports.mu.Lock()
	_, reported := s.reports.pending[todo.ID]
	s.reports.mu.Unlock()
	if reported {
		t.Fatal("a closed report is still due for the withdrawn task")
	}
	if !toldResolved(f, card) {
		t.Fatalf("the hub was not told the to-do is over (EventFleetResolved %s)", card)
	}
	// The decision card ends neutral: superseded with the supervisor's reason, never an option taken.
	a := askOf(s, todo.ID)
	if a == nil || !a.Superseded || a.AcceptedAt.IsZero() || a.ByDefault || a.OptionTaken || a.Chosen != "" || a.Response != "" {
		t.Fatalf("decision ask %+v, want superseded without an answer", a)
	}
	if _, open := s.todoAsks(ctx)[todo.ID]; open {
		t.Fatal("the withdrawn decision is still in Needs you")
	}
	redrawn := updates()
	if len(redrawn) == 0 {
		t.Fatal("the decision card was not closed")
	}
	if last := redrawn[len(redrawn)-1]; strings.Contains(last, "按推荐来，已交给") || strings.Contains(last, "Taken: the recommendation") {
		t.Fatalf("the closed card says the user took the recommendation:\n%s", last)
	}
	s.tasks.outcomeRuns.Wait()
	decisions, err := database.ListTodoDecisions(ctx, todo.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range decisions {
		if d.Source != db.DecisionOutcome {
			t.Fatalf("the withdrawal wrote a decision: %+v", d)
		}
	}
	for _, ask := range s.availabilityAsks(ctx, time.Now().Add(-time.Hour)) {
		if ask.kind == "decision" && ask.who == askByOwner {
			t.Fatalf("the withdrawn decision counts as the owner's answer: %+v", ask)
		}
	}
	if !strings.Contains(res, "withdrew its needs_user") || strings.Contains(res, "conclusion card (the verdict") {
		t.Fatalf("answer: %s", res)
	}
}

// Only the task's supervisor, or the main conversation, withdraws its needs_user; another conversation is refused and
// nothing changes.
func TestOnlyTheSupervisorWithdrawsANeedsUser(t *testing.T) {
	t.Run("another conversation", func(t *testing.T) {
		s, f, todo, _, _ := setupNeedsUserTaskWithLark(t, nil)
		args := withdrawArgs()
		args["todo_id"] = todo.ID
		args["checks"] = completeReviewChecks(todo)
		raw, _ := json.Marshal(args)
		f.known["conv2"] = true
		if _, err := s.runTool(context.Background(), "server", "conv2", "task_review", raw); err == nil {
			t.Fatal("another conversation withdrew the needs_user")
		}
		got := todoNow(s, todo.ID)
		if got.DoneAt != nil || got.Supervision.State != reviewNeedsUser {
			t.Fatalf("after a refused withdrawal: done=%v state=%q", got.DoneAt, got.Supervision.State)
		}
		if a := askOf(s, todo.ID); a == nil || a.Superseded || !a.AcceptedAt.IsZero() {
			t.Fatalf("a refused withdrawal ended the decision: %+v", a)
		}
		if toldResolved(f, assistantwire.TodoAskCardID(todo.ID)) {
			t.Fatal("a refused withdrawal told the hub the to-do is over")
		}
	})
	t.Run("the main conversation", func(t *testing.T) {
		s, f, todo, _, _ := setupNeedsUserTaskWithLark(t, nil)
		setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
		pushed := len(f.pushed())
		args := withdrawArgs()
		args["todo_id"] = todo.ID
		args["checks"] = completeReviewChecks(todo)
		raw, _ := json.Marshal(args)
		if _, err := s.runTool(context.Background(), "server", "main-0000-0000", "task_review", raw); err != nil {
			t.Fatal(err)
		}
		if got := todoNow(s, todo.ID); got.DoneAt == nil || got.Supervision.State != reviewAccepted {
			t.Fatalf("main's withdrawal: done=%v state=%q", got.DoneAt, got.Supervision.State)
		}
		if n := len(f.pushed()); n != pushed {
			t.Fatalf("main's withdrawal pushed %d more", n-pushed)
		}
	})
}

// A withdrawal is refused when the task does not wait for the user, and withdraw_needs_user goes with accepted only.
func TestWithdrawNeedsUserNeedsANeedsUser(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	for _, args := range []map[string]any{
		withdrawArgs(),
		{"verdict": "rework", "withdraw_needs_user": true, "note": "再查一下"},
		{"verdict": "accepted", "withdraw_needs_user": true},
	} {
		args["todo_id"] = todo.ID
		raw, _ := json.Marshal(args)
		if _, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw); err == nil {
			t.Fatalf("withdrawal %v accepted", args)
		}
	}
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.Supervision.State != reviewPending {
		t.Fatalf("after refused withdrawals: done=%v state=%q", got.DoneAt, got.Supervision.State)
	}
	// Who may withdraw, and from what.
	waiting := Todo{ID: "todo_w", Supervision: &Supervision{Supervisor: "conv1", State: reviewNeedsUser}}
	for _, tc := range []struct {
		self   string
		isMain bool
		ok     bool
	}{{"conv1", false, true}, {"main", true, true}, {"conv2", false, false}} {
		if err := withdrawNeedsUserError(waiting, tc.self, tc.isMain); (err == nil) != tc.ok {
			t.Errorf("%s (main %v): %v", tc.self, tc.isMain, err)
		}
	}
	waiting.Supervision.State = reviewRework
	if withdrawNeedsUserError(waiting, "conv1", false) == nil {
		t.Error("withdrew a needs_user from a task in rework")
	}
}

// The judge's owner wait (held by the supervisor, never pushed: noteOwnerWait) is withdrawn the same way when the
// supervisor finds it is not the user's: the hub is told the to-do is over and nothing goes out.
func TestSupervisorWithdrawsAnOwnerWait(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	withWebBot(f, testWeb)
	work := time.Now().UTC()
	s.superviseJudged(context.Background(), todo, work, taskDoneCheck{Verdict: verdictNeedsUser, Reason: "需要你本人操作"}, nil)
	if w := todoNow(s, todo.ID).Supervision.Waits; w == nil || len(f.pushed()) != 0 {
		t.Fatalf("owner wait = %+v, pushes %+v", w, f.pushed())
	}
	card := assistantwire.TodoAskCardID(todo.ID)
	reviewTask(t, s, todo.ID, withdrawArgs())
	got := todoNow(s, todo.ID)
	if got.DoneAt == nil || got.Supervision.State != reviewAccepted || got.Supervision.Waits != nil {
		t.Fatalf("after withdrawing the owner wait: done=%v state=%q waits=%+v", got.DoneAt, got.Supervision.State, got.Supervision.Waits)
	}
	if n := len(f.pushed()); n != 0 {
		t.Fatalf("withdrawing the owner wait pushed %d", n)
	}
	if !toldResolved(f, card) {
		t.Fatal("the hub was not told the owner wait is over")
	}
}

func TestSupervisorWithdrawalRespectsOwnerActivity(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	s.superviseJudged(t.Context(), todo, time.Now().UTC(), taskDoneCheck{Verdict: verdictNeedsUser, Reason: "owner action"}, nil)
	before := todoNow(s, todo.ID)
	if before.Supervision.Waits == nil {
		t.Fatal("expected an owner wait")
	}
	agent := before.Agents[0]
	setTaskViewer(t, s, agent.MachineID, agent.SessionID, true)
	args := withdrawArgs()
	args["todo_id"] = todo.ID
	args["checks"] = completeReviewChecks(before)
	if _, err := runJSON(t, s, "conv1", "task_review", args); err == nil || !strings.Contains(err.Error(), "cannot be closed") {
		t.Fatalf("withdrawal with an active viewer: %v", err)
	}
	after := todoNow(s, todo.ID)
	if after.DoneAt != nil || after.Supervision.Waits == nil || len(after.Supervision.Reviews) != len(before.Supervision.Reviews) ||
		toldResolved(f, assistantwire.TodoAskCardID(todo.ID)) {
		t.Fatalf("refused withdrawal changed the open task: %+v", after)
	}
}

// Directly closing or deleting a to-do, or withdrawing an outside decision, tells the hub its to-do ask is over
// (EventFleetResolved todo-ask:<id>) so any delivered phone notification is recalled (#13916).
func TestTodoCloseDeleteAndOutsideWithdrawTellHubResolved(t *testing.T) {
	t.Run("direct close", func(t *testing.T) {
		s, f, todo, _, _ := setupNeedsUserTaskWithLark(t, nil)
		done := true
		if _, err := s.editTodo(context.Background(), todo.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		if !toldResolved(f, assistantwire.TodoAskCardID(todo.ID)) {
			t.Fatalf("direct close did not emit EventFleetResolved %s", assistantwire.TodoAskCardID(todo.ID))
		}
	})
	t.Run("delete", func(t *testing.T) {
		s, f, todo, _, _ := setupNeedsUserTaskWithLark(t, nil)
		if err := s.deleteTodo(context.Background(), todo.ID); err != nil {
			t.Fatal(err)
		}
		if !toldResolved(f, assistantwire.TodoAskCardID(todo.ID)) {
			t.Fatalf("delete did not emit EventFleetResolved %s", assistantwire.TodoAskCardID(todo.ID))
		}
	})
}
