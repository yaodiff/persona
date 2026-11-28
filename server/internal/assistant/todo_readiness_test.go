package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

func TestTodoReadinessPostgresProtectsApprovedPRWaitAfterLabsRollback(t *testing.T) {
	database := todoTestDB(t)
	s, f := workspaceQueueService(t, database)
	ctx := t.Context()
	setBlockersTodosV2Flag(t, s, true, false)
	const prURL = "https://github.com/example/upstream/pull/62"
	existing, err := s.createTodo(ctx, "Follow up once the upstream PR closes", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.AddTodoBlocker(ctx, existing.ID, todoBlockerKindExternalPR, prURL, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CancelTodoBlocker(ctx, old.ID, s.todoViewNow()); err != nil {
		t.Fatal(err)
	}
	_, plan := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
		"title": "Follow up once the upstream PR closes", "prompt": "Run post-merge checks",
		"readonly": true, "todo_id": existing.ID, "blocked_by_pr": []string{prURL},
	})
	if plan.State == planOpen {
		var err error
		plan, err = s.startPlan(ctx, plan.ID, nil, false)
		if err != nil {
			t.Fatal(err)
		}
	}
	task := plan.Tasks[0]
	if task.Queue == nil || task.Queue.Key != blockerWaitQueueKey {
		t.Fatalf("approved task did not wait on the PR: %+v", task.Queue)
	}
	rows, err := database.ListTodoBlockersByTodo(ctx, task.TodoID)
	if err != nil || len(rows) != 1 || rows[0].ID != old.ID || rows[0].State != db.TodoBlockerStateActive {
		t.Fatalf("persisted blockers = %+v, %v", rows, err)
	}

	setBlockersTodosV2Flag(t, s, false, true)
	s.sweepAdmissionQueue(ctx)
	s.startPlanTask(plan.ID, plan.Conversation, 0, task, false)
	waitFor(t, "rollback queue sweep to settle", func() bool { return s.taskStarting.Load() == 0 })
	reloaded, err := s.loadPlan(ctx, plan.ID)
	if err != nil || reloaded.Tasks[0].Queue == nil || len(f.started()) != 0 {
		t.Fatalf("rollback released an active PR blocker: plan=%+v starts=%d err=%v", reloaded, len(f.started()), err)
	}
	row, ok, err := s.viewedTodo(ctx, task.TodoID)
	if err != nil || !ok || row.Wait == nil || row.Wait.URL != prURL || len(row.ActiveBlockers) != 0 {
		t.Fatalf("rollback hid the approved wait or exposed Labs fields: wait=%+v blockers=%+v, %v", row.Wait, row.ActiveBlockers, err)
	}
	if err := s.applyPRStatus(ctx, Todo{}, TodoPRStatus{URL: prURL, State: "merged", CheckedAt: s.todoViewNow()}); err != nil {
		t.Fatal(err)
	}
	waitWorkspaceStarts(t, s, f, 1)
	rows, err = database.ListTodoBlockersByTodo(ctx, task.TodoID)
	if err != nil || len(rows) != 1 || rows[0].State != db.TodoBlockerStateResolved {
		t.Fatalf("PR observation did not resolve the durable blocker: rows=%+v err=%v", rows, err)
	}
}

type unreadableTodoBlockers struct{ *db.DB }

func (unreadableTodoBlockers) ListTodoBlockersByTodo(context.Context, string) ([]db.TodoBlockerRow, error) {
	return nil, errors.New("blocker store read failed")
}

func (unreadableTodoBlockers) ListTodoReadinessBlockers(context.Context, []string) ([]db.TodoBlockerRow, error) {
	return nil, errors.New("blocker store read failed")
}

func TestTodoReadinessPostgresKeepsWorkspaceQueuedOnBlockerReadError(t *testing.T) {
	database := todoTestDB(t)
	s, f := workspaceQueueService(t, database)
	ctx := t.Context()
	setBlockersTodosV2Flag(t, s, true, false)
	owner, err := s.createTodo(ctx, "Existing workspace owner", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, owner.ID, "box", "existing-session", owner.Title, "/code/app", ""); err != nil {
		t.Fatal(err)
	}
	_, plan := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
		"title": "Queued workspace task", "prompt": "Run once the workspace is free",
		"workspace": map[string]any{"existing": "/code/app", "queue": true},
	})
	waitWorkspaceStarts(t, s, f, 0)
	if q := plan.Tasks[0].Queue; q == nil || q.Key != workspaceQueueKey {
		t.Fatalf("expected workspace queue, got %+v", q)
	}

	s.setStore(func() Store { return unreadableTodoBlockers{database} })
	row, ok, err := s.viewedTodo(ctx, plan.Tasks[0].TodoID)
	if err != nil || !ok || row.View.Phase != assistantwire.TodoPhaseWaiting {
		t.Fatalf("unreadable prerequisites looked ready in projection: view=%+v ok=%v err=%v", row.View, ok, err)
	}
	s.startPlanTask(plan.ID, plan.Conversation, 0, plan.Tasks[0], false)
	done := true
	if _, err := s.editTodo(ctx, owner.ID, db.TodoEdit{Done: &done}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "workspace drain to settle", func() bool { return s.taskStarting.Load() == 0 })
	loaded, err := s.loadPlan(ctx, plan.ID)
	if err != nil || loaded.Tasks[0].Queue == nil || loaded.Tasks[0].Queue.Claimed || len(f.started()) != 0 {
		t.Fatalf("workspace launched with unreadable blockers: plan=%+v starts=%d err=%v", loaded, len(f.started()), err)
	}

	s.setStore(func() Store { return database })
	s.sweepWorkspaceQueues(ctx)
	waitWorkspaceStarts(t, s, f, 1)
}
