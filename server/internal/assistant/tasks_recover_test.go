package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func taskRecoveryDatabase(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(context.Background(), newDatabase(t), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	return database
}

// pendingRecoveryTask saves the gap between starting a plan and linking its agent. No start goroutine is running.
func pendingRecoveryTask(t *testing.T, s *Service, plan string) PlanTask {
	t.Helper()
	todo, err := s.createTaskTodo(context.Background(), "Verify the example change", "Run the example checks.", "conv1", plan, "")
	if err != nil {
		t.Fatal(err)
	}
	return PlanTask{Title: todo.Title, Prompt: todo.Notes, Machine: "box", Cwd: "/code/app", TodoID: todo.ID}
}

func recoveryReceipts(t *testing.T, s *Service, id string) []receiptBody {
	t.Helper()
	items, err := s.feedItems(context.Background(), "conv1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var receipts []receiptBody
	for _, item := range items {
		if item.Kind == feedReceipt && item.Ref == id {
			var body receiptBody
			if err := json.Unmarshal(item.Body, &body); err != nil {
				t.Fatal(err)
			}
			receipts = append(receipts, body)
		}
	}
	return receipts
}

// The old hub cannot know whether its request reached the node. Even a connected node can still be completing
// it, so recovery records the uncertainty instead of launching a second agent. Read-only work has the same gap.
func TestRecoverTaskStartsAfterRestart(t *testing.T) {
	for _, offline := range []bool{true, false} {
		name := "node_reconnected"
		if offline {
			name = "node_not_reconnected"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			database := taskRecoveryDatabase(t)
			old, _ := newTaskServiceOn(t, database)
			p := &TaskPlan{ID: "plan_interrupted", Conversation: "conv1", State: planStarted,
				CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
			for range 3 {
				p.Tasks = append(p.Tasks, pendingRecoveryTask(t, old, p.ID))
			}
			p.Tasks[1].Readonly = true
			p.Tasks[2].Permission = "plan"
			// A topic's existing to-do keeps its empty PlanID when a started plan takes it over.
			reused := pendingRecoveryTask(t, old, "")
			reused.ReuseTodo = reused.TodoID
			p.Tasks = append(p.Tasks, reused)
			if err := old.savePlan(ctx, p); err != nil {
				t.Fatal(err)
			}
			before := time.Now()
			restarted, hub := newTaskServiceOn(t, database)
			hub.mu.Lock()
			hub.hello.Locale = string(i18n.En)
			hub.offline["box"] = offline
			hub.mu.Unlock()
			if !offline {
				// A title and directory are not proof that this session belongs to the interrupted start.
				hub.setSessions("box", proto.Session{SessionID: "possibly-started", MachineID: "box", Cwd: "/code/app",
					Title: p.Tasks[0].Title, Agent: "claude", Status: proto.StatusWorking})
			}
			if err := restarted.recoverTaskStarts(ctx, before); err != nil {
				t.Fatal(err)
			}
			if err := restarted.recoverTaskStarts(ctx, before); err != nil {
				t.Fatal(err)
			}
			again, secondHub := newTaskServiceOn(t, database)
			if err := again.recoverTaskStarts(ctx, time.Now()); err != nil {
				t.Fatal(err)
			}
			saved, err := again.loadPlan(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			reason := i18n.T(i18n.En, "assistant-tasks.start_interrupted")
			if reason == "assistant-tasks.start_interrupted" {
				t.Fatal("the interruption reason has no translation")
			}
			for i, task := range saved.Tasks {
				todo := todoNow(again, task.TodoID)
				if todo.DoneAt == nil || todo.StartError != reason || len(todo.Agents) != 0 || task.Error == "" {
					t.Errorf("task %d was not durably failed: task=%+v todo=%+v", i, task, todo)
				}
				receipts := recoveryReceipts(t, again, task.TodoID)
				if len(receipts) != 1 || receipts[0].State != "failed" || !strings.Contains(receipts[0].Summary, reason) {
					t.Errorf("task %d receipts after repeated recovery and restart: %+v", i, receipts)
				}
			}
			for _, f := range []*fakeHub{hub, secondHub} {
				f.mu.Lock()
				starts := len(f.starts)
				f.mu.Unlock()
				if starts != 0 {
					t.Errorf("recovery launched %d agents without proving the old start stopped", starts)
				}
			}
			pushes := hub.pushed()
			if len(pushes) != len(p.Tasks) || len(secondHub.pushed()) != 0 {
				t.Fatalf("failure pushes: first restart=%d, second=%d", len(pushes), len(secondHub.pushed()))
			}
		})
	}
}

// stopOnRecoveryPush ends Run after the durable failure and receipt have reached the ordinary notification path,
// before the other service loops have work to do.
type stopOnRecoveryPush struct {
	*fakeHub
	cancel context.CancelFunc
}

func (h stopOnRecoveryPush) NotifyRequest(ctx context.Context, p NotifyRequest) (NotifyResult, error) {
	res, err := h.fakeHub.NotifyRequest(ctx, p)
	h.cancel()
	return res, err
}

func TestRunRecoversTaskStarts(t *testing.T) {
	database := taskRecoveryDatabase(t)
	old, _ := newTaskServiceOn(t, database)
	pt := pendingRecoveryTask(t, old, "plan_run_recovery")
	p := &TaskPlan{ID: "plan_run_recovery", Conversation: "conv1", State: planStarted, Tasks: []PlanTask{pt},
		CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
	if err := old.savePlan(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	restarted, hub := newTaskServiceOn(t, database)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	setHub(restarted, stopOnRecoveryPush{fakeHub: hub, cancel: cancel})
	restarted.Run(ctx)
	if ctx.Err() != context.Canceled {
		t.Fatalf("Run did not report the interrupted start before its deadline: %v", ctx.Err())
	}
	if todo := todoNow(restarted, pt.TodoID); todo.DoneAt == nil || todo.StartError == "" {
		t.Fatalf("Run did not recover the persisted interrupted start: %+v", todo)
	}
	if receipts := recoveryReceipts(t, restarted, pt.TodoID); len(receipts) != 1 || receipts[0].State != "failed" {
		t.Fatalf("Run did not persist the failed receipt: %+v", receipts)
	}
}

func TestRecoverTaskStartsLeavesOtherTasksAlone(t *testing.T) {
	ctx := context.Background()
	database := taskRecoveryDatabase(t)
	old, _ := newTaskServiceOn(t, database)
	before := time.Now().Add(time.Second)
	type unchanged struct {
		plan *TaskPlan
		todo Todo
	}
	var controls []unchanged
	for _, name := range []string{"linked", "completed", "cancelled", "remedying", "remedied", "plan_error", "todo_error", "old", "new",
		"open_plan", "dismissed_plan", "no_task_link", "different_conversation"} {
		p := &TaskPlan{ID: "plan_" + name, Conversation: "conv1", State: planStarted,
			CreatedAt: before.Add(-time.Minute), UpdatedAt: before.Add(-time.Minute)}
		pt := pendingRecoveryTask(t, old, p.ID)
		switch name {
		case "linked":
			if err := old.linkTodoAgent(ctx, pt.TodoID, "box", "existing-agent", pt.Title, pt.Cwd, "claude"); err != nil {
				t.Fatal(err)
			}
		case "completed":
			done := true
			if _, err := old.editTodo(ctx, pt.TodoID, db.TodoEdit{Done: &done}); err != nil {
				t.Fatal(err)
			}
		case "cancelled":
			if closed, err := old.reviewDoneTodo(ctx, pt.TodoID, "No longer needed.", false); err != nil || !closed {
				t.Fatalf("cancel task: closed=%v, error=%v", closed, err)
			}
		case "remedying":
			pt.Start = &StartRemedy{State: remedying, Machine: "box", Pane: "w1:p2", Until: before.Add(10 * time.Minute), At: before}
		case "remedied":
			pt.Start = &StartRemedy{State: remedyStarted, At: before}
		case "plan_error":
			pt.Error = "An earlier start failed."
		case "todo_error":
			if err := old.failTodo(ctx, pt.TodoID, "An earlier start failed."); err != nil {
				t.Fatal(err)
			}
		case "old":
			p.CreatedAt, p.UpdatedAt = before.Add(-24*time.Hour), before.Add(-24*time.Hour)
		case "new":
			p.CreatedAt, p.UpdatedAt = before.Add(time.Minute), before.Add(time.Minute)
		case "open_plan":
			p.State = planOpen
		case "dismissed_plan":
			p.State = planDismissed
		case "different_conversation":
			p.Conversation = "conv-other"
		}
		original := todoNow(old, pt.TodoID)
		if name == "no_task_link" {
			pt.TodoID = ""
		}
		p.Tasks = []PlanTask{pt}
		if err := old.savePlan(ctx, p); err != nil {
			t.Fatal(err)
		}
		controls = append(controls, unchanged{p, original})
	}
	// Read the baseline from the database too, so precision and JSON normalization are identical on both sides.
	baseline, _ := newTaskServiceOn(t, database)
	if err := baseline.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	for i := range controls {
		p, err := baseline.loadPlan(ctx, controls[i].plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		controls[i].plan, controls[i].todo = p, todoNow(baseline, controls[i].todo.ID)
	}
	restarted, hub := newTaskServiceOn(t, database)
	if err := restarted.recoverTaskStarts(ctx, before); err != nil {
		t.Fatal(err)
	}
	for _, c := range controls {
		got, err := restarted.loadPlan(ctx, c.plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, c.plan) || !reflect.DeepEqual(todoNow(restarted, c.todo.ID), c.todo) {
			t.Errorf("recovery changed %s: plan=%+v, todo=%+v", c.plan.ID, got, todoNow(restarted, c.todo.ID))
		}
		if receipts := recoveryReceipts(t, restarted, c.todo.ID); len(receipts) != 0 {
			t.Errorf("recovery issued a receipt for %s: %+v", c.plan.ID, receipts)
		}
	}
	hub.mu.Lock()
	starts := len(hub.starts)
	hub.mu.Unlock()
	if starts != 0 || len(hub.pushed()) != 0 {
		t.Fatalf("recovery acted on unrelated tasks: starts=%d, pushes=%d", starts, len(hub.pushed()))
	}
}

// pausingRecoveryCompletionStore holds the user's completed row before it reaches the service's in-memory copy.
// Recovery can read that old copy while discovering plans, but must recheck after it acquires the writer lock.
type pausingRecoveryCompletionStore struct {
	*db.DB
	stored, release, planRead chan struct{}
}

func (st *pausingRecoveryCompletionStore) EditTodo(ctx context.Context, id string, edit db.TodoEdit, at time.Time) (*db.TodoRow, error) {
	row, err := st.DB.EditTodo(ctx, id, edit, at)
	if err == nil && edit.Done != nil && *edit.Done {
		close(st.stored)
		<-st.release
	}
	return row, err
}

func (st *pausingRecoveryCompletionStore) TaskPlan(ctx context.Context, id string) (*db.TaskPlanRow, error) {
	row, err := st.DB.TaskPlan(ctx, id)
	select {
	case st.planRead <- struct{}{}:
	default:
	}
	return row, err
}

func TestRecoverTaskStartsKeepsConcurrentCompletion(t *testing.T) {
	ctx := context.Background()
	database := taskRecoveryDatabase(t)
	old, _ := newTaskServiceOn(t, database)
	pt := pendingRecoveryTask(t, old, "plan_completed_during_recovery")
	p := &TaskPlan{ID: "plan_completed_during_recovery", Conversation: "conv1", State: planStarted, Tasks: []PlanTask{pt},
		CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
	if err := old.savePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	st := &pausingRecoveryCompletionStore{DB: database, stored: make(chan struct{}), release: make(chan struct{}), planRead: make(chan struct{}, 1)}
	restarted, hub := newTaskServiceOn(t, st)
	if err := restarted.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	// Bound the concurrent operations, not the database fixture's migrations on a shared test server.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	completed := make(chan error, 1)
	go func() {
		done := true
		_, err := restarted.editTodo(ctx, pt.TodoID, db.TodoEdit{Done: &done})
		completed <- err
	}()
	select {
	case <-st.stored:
	case <-ctx.Done():
		t.Fatal("the user's completion did not reach storage")
	}
	recovered := make(chan error, 1)
	go func() { recovered <- restarted.recoverTaskStarts(ctx, time.Now()) }()
	select {
	case <-st.planRead:
	case <-ctx.Done():
		close(st.release)
		t.Fatal("recovery did not inspect the old in-memory task")
	}
	close(st.release)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	if err := <-recovered; err != nil {
		t.Fatal(err)
	}
	if todo := todoNow(restarted, pt.TodoID); todo.DoneAt == nil || todo.StartError != "" {
		t.Fatalf("recovery turned a user completion into a failed start: %+v", todo)
	}
	rows, err := database.ListTodos(ctx)
	if err != nil || len(rows) != 1 || rows[0].DoneAt == nil || rows[0].StartError != "" {
		t.Fatalf("stored completion changed: %+v, %v", rows, err)
	}
	if receipts := recoveryReceipts(t, restarted, pt.TodoID); len(receipts) != 0 || len(hub.pushed()) != 0 {
		t.Fatalf("completed task reported as failed: receipts=%+v, pushes=%+v", receipts, hub.pushed())
	}
}

func TestRecoverTaskStartsTellsSupervisor(t *testing.T) {
	ctx := context.Background()
	database := taskRecoveryDatabase(t)
	old, _ := newQuietServiceOn(t, database)
	pt := pendingRecoveryTask(t, old, "plan_supervised")
	old.superviseTodo(ctx, pt.TodoID, "conv1", []string{"The checks pass."}, 30)
	p := &TaskPlan{ID: "plan_supervised", Conversation: "conv1", State: planStarted, Tasks: []PlanTask{pt},
		CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
	if err := old.savePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	restarted, hub := newQuietServiceOn(t, database)
	hub.mu.Lock()
	hub.hello.Locale = string(i18n.En)
	hub.offline["box"] = true
	hub.mu.Unlock()
	if err := restarted.recoverTaskStarts(ctx, before); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the interrupted start's supervision message", func() bool { return len(hub.notices()) == 1 })
	reason := i18n.T(i18n.En, "assistant-tasks.start_interrupted")
	if notice := hub.notices()[0]; !strings.Contains(notice, reason) || !strings.Contains(notice, pt.Title) {
		t.Fatalf("supervisor did not receive the task and reason: %q", notice)
	}
	if todo := todoNow(restarted, pt.TodoID); todo.DoneAt == nil || todo.StartError != reason || todo.Supervision == nil {
		t.Fatalf("interrupted task not failed for its supervisor: %+v", todo)
	}
	if receipts := recoveryReceipts(t, restarted, pt.TodoID); len(receipts) != 1 || receipts[0].State != "failed" {
		t.Fatalf("interrupted task has no visible failed receipt: %+v", receipts)
	}
	hub.mu.Lock()
	starts := len(hub.starts)
	hub.mu.Unlock()
	if starts != 0 {
		t.Fatalf("recovery started %d agents", starts)
	}
}
