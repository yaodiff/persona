package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// memRuntimeLoopStore extends memBlockerStore with in-memory task_plans and feed support
// so task_plan + startPlan + todo_blockers can be exercised end-to-end without Postgres.
type memRuntimeLoopStore struct {
	*memBlockerStore
	pmu   sync.Mutex
	plans map[string]db.TaskPlanRow
}

func newMemRuntimeLoopStore() *memRuntimeLoopStore {
	return &memRuntimeLoopStore{
		memBlockerStore: newMemBlockerStore(),
		plans:           make(map[string]db.TaskPlanRow),
	}
}

func (m *memRuntimeLoopStore) CreateTaskPlan(_ context.Context, r db.TaskPlanRow) error {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	m.plans[r.ID] = r
	return nil
}

func (m *memRuntimeLoopStore) TaskPlan(_ context.Context, id string) (*db.TaskPlanRow, error) {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	r, ok := m.plans[id]
	if !ok {
		return nil, nil
	}
	cp := r
	return &cp, nil
}

func (m *memRuntimeLoopStore) TaskPlans(_ context.Context, conv string) ([]db.TaskPlanRow, error) {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	var out []db.TaskPlanRow
	for _, r := range m.plans {
		if conv == "" || r.Conversation == conv {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memRuntimeLoopStore) TaskPlansByID(_ context.Context, ids []string) ([]db.TaskPlanRow, error) {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	var out []db.TaskPlanRow
	for _, id := range ids {
		if r, ok := m.plans[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memRuntimeLoopStore) UpdateTaskPlan(_ context.Context, id, from, to string, tasks json.RawMessage, at time.Time) (bool, error) {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	r, ok := m.plans[id]
	if !ok || (from != "" && r.State != from) {
		return false, nil
	}
	r.State = to
	r.Tasks = append([]byte(nil), tasks...)
	r.UpdatedAt = at
	m.plans[id] = r
	return true, nil
}

func (m *memRuntimeLoopStore) AppendFeed(context.Context, db.FeedRow) error { return nil }

func TestTodosV2RuntimeLoopsBootEpochStartupSweepOnceAndCap(t *testing.T) {
	// Zero-value Service{} must lazily initialize a stable boot_epoch without races.
	var zeroSvc Service
	var wg sync.WaitGroup
	epochs := make([]string, 8)
	for i := range epochs {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			epochs[idx] = zeroSvc.runtimeBootEpoch()
		}(i)
	}
	wg.Wait()
	if epochs[0] == "" {
		t.Fatal("expected non-empty lazy boot_epoch on zero-value Service")
	}
	for i := 1; i < len(epochs); i++ {
		if epochs[i] != epochs[0] {
			t.Fatalf("lazy boot_epoch diverged: %q != %q", epochs[i], epochs[0])
		}
	}

	s, f := newQuietServiceOn(t, nil)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	setRestartRecoveryTodosV2(t, s, true, false)

	if got := s.runtimeBootEpoch(); got == "" {
		t.Fatal("expected New() to initialize boot_epoch")
	}

	task := setupSupervisedRecoveryTask(t, s, f, "Restart interrupted task", "s-boot-1", "assistant", []string{"All checks pass."})
	if err := s.RecordSupervisedTaskActive(ctx, task.ID, "mac", "s-boot-1", "boot-prev"); err != nil {
		t.Fatal(err)
	}

	// Simulate boot-2: first superviseWatch runs startup sweep once; second superviseWatch is a no-op.
	s.setBootEpoch("boot-2")
	s.superviseWatch(ctx, time.Now())
	if !s.todosV2RecoverySwept.Load() {
		t.Fatal("expected startup restart recovery sweep flag to be set after first ready superviseWatch")
	}
	if sent := sentToAgent(f); len(sent) != 1 || !strings.Contains(sent[0], "attempt 1/2") {
		t.Fatalf("expected 1 resume prompt for attempt 1/2, got %+v", sent)
	}

	// Repeating superviseWatch in the same process boot must not re-sweep or re-send.
	s.superviseWatch(ctx, time.Now())
	if sent := sentToAgent(f); len(sent) != 1 {
		t.Fatalf("expected second superviseWatch in same boot to be a no-op, got %+v", sent)
	}

	// Simulate boot-3: second recovery attempt (2/2).
	s.setBootEpoch("boot-3")
	s.todosV2RecoverySwept.Store(false)
	s.superviseWatch(ctx, time.Now())
	if sent := sentToAgent(f); len(sent) != 2 || !strings.Contains(sent[1], "attempt 2/2") {
		t.Fatalf("expected 2nd resume prompt for attempt 2/2, got %+v", sent)
	}

	// Simulate boot-4: cap reached -> escalate cleanly without sending a 3rd prompt.
	s.setBootEpoch("boot-4")
	s.todosV2RecoverySwept.Store(false)
	s.superviseWatch(ctx, time.Now())
	if sent := sentToAgent(f); len(sent) != 2 {
		t.Fatalf("expected no prompt after cap reached, got %+v", sent)
	}
	s.supervise.mu.Lock()
	sig, hasEscalation := s.supervise.pending[task.ID+"|"+sigRecovered]
	s.supervise.mu.Unlock()
	if !hasEscalation || !strings.Contains(sig.Key, "restart_recovery_cap:") {
		t.Fatalf("expected cap escalation signal, got has=%t sig=%+v", hasEscalation, sig)
	}
}

func TestTodosV2RuntimeLoopsActiveCheckpointAndResetOnCompletion(t *testing.T) {
	st := newMemBlockerStore()
	s, f := newQuietServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	setRestartRecoveryTodosV2(t, s, true, false)

	// 1. Working supervised agent gets active checkpoint recorded during superviseWatch.
	task := setupSupervisedRecoveryTask(t, s, f, "Active supervised task", "s-work-loop", "assistant", []string{"Verified."})
	f.mu.Lock()
	for i := range f.sessions["mac"] {
		if f.sessions["mac"][i].SessionID == "s-work-loop" {
			f.sessions["mac"][i].Status = proto.StatusWorking
			f.sessions["mac"][i].State = proto.StateWorking
		}
	}
	f.mu.Unlock()

	s.setBootEpoch("boot-active-1")
	s.superviseWatch(ctx, time.Now())

	rec, found, err := s.todoRecoveryStore().GetTodoRecoveryState(ctx, task.ID)
	if err != nil || !found {
		t.Fatalf("expected active checkpoint recorded by superviseWatch: found=%t err=%v", found, err)
	}
	if rec.LastActiveBootEpoch != "boot-active-1" || rec.SessionID != "s-work-loop" {
		t.Fatalf("unexpected recovery checkpoint: %+v", rec)
	}

	// Simulate an earlier recovery attempt so we can verify completion resets attempts to 0.
	if _, err := s.todoRecoveryStore().IncrementTodoRecoveryAttempts(ctx, task.ID, "boot-active-2", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	// Add a dependent todo blocked on task.ID (todo_done) to verify completion triggers targeted blocker reconciliation.
	dep, err := s.createTodo(ctx, "Dependent on active task", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTodoBlocker(ctx, dep.ID, todoBlockerKindTodoDone, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	var unblocked []string
	s.setTodoBlockerUnblockedHook(func(_ context.Context, todoID string) {
		unblocked = append(unblocked, todoID)
	})

	// Complete via reviewDoneTodo (accepted=true): resets recovery attempts AND unblocks dependent.
	closed, err := s.reviewDoneTodo(ctx, task.ID, "accepted in review", true)
	if err != nil || !closed {
		t.Fatalf("reviewDoneTodo: closed=%t err=%v", closed, err)
	}
	recAfter, found, err := s.todoRecoveryStore().GetTodoRecoveryState(ctx, task.ID)
	if err != nil || !found {
		t.Fatalf("expected recovery row after reset: found=%t err=%v", found, err)
	}
	if recAfter.RecoveryAttempts != 0 {
		t.Fatalf("expected recovery attempts reset to 0 on reviewDoneTodo, got %d", recAfter.RecoveryAttempts)
	}
	if len(unblocked) != 1 || unblocked[0] != dep.ID {
		t.Fatalf("expected reviewDoneTodo to unblock %s immediately, got %+v", dep.ID, unblocked)
	}

	// 2. Completing a plain todo via todo_complete also triggers targeted ReconcileTodoBlockers.
	prereq2, err := s.createTodo(ctx, "Plain prerequisite", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	dep2, err := s.createTodo(ctx, "Waits on plain prerequisite", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTodoBlocker(ctx, dep2.ID, todoBlockerKindTodoDone, prereq2.ID, ""); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"id": prereq2.ID})
	if _, err := s.toolTodo(ctx, "mac", "conv1", "todo_complete", args); err != nil {
		t.Fatalf("todo_complete: %v", err)
	}
	if len(unblocked) != 2 || unblocked[1] != dep2.ID {
		t.Fatalf("expected todo_complete to unblock %s immediately, got %+v", dep2.ID, unblocked)
	}
}

func TestTodosV2RuntimeLoopsPRStatusEventAndBlockedByPREndToEnd(t *testing.T) {
	st := newMemRuntimeLoopStore()
	s, f := newQuietServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	s.briefsOptional = true
	ctx := context.Background()
	setRestartRecoveryTodosV2(t, s, true, false)

	f.mu.Lock()
	f.devices = []Device{{ID: "mac", Name: "Mac", Online: true}}
	f.mu.Unlock()

	const prURL1 = "https://github.com/nwverse/persona/pull/77"
	const prURL2 = "https://github.com/nwverse/persona/pull/88"

	// 1. todo_create with blocked_by_pr persists normalized external_pr blocker under todos_v2=true.
	createArgs, _ := json.Marshal(map[string]any{
		"title":         "Follow-up after PR 77",
		"blocked_by_pr": []string{prURL1 + "/", prURL1}, // duplicate with trailing slash -> deduped to 1
	})
	out, err := s.toolTodo(ctx, "mac", "conv1", "todo_create", createArgs)
	if err != nil {
		t.Fatalf("todo_create with blocked_by_pr: %v", err)
	}
	if !strings.Contains(out, "Created:") {
		t.Fatalf("unexpected todo_create output: %s", out)
	}

	active, err := st.ListActiveTodoBlockers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].Kind != todoBlockerKindExternalPR || active[0].TargetRef != prURL1 {
		t.Fatalf("expected 1 normalized external_pr blocker for %s, got %+v", prURL1, active)
	}
	waiterTodoID := active[0].TodoID

	var unblocked []string
	s.setTodoBlockerUnblockedHook(func(_ context.Context, todoID string) {
		unblocked = append(unblocked, todoID)
	})

	// Create an upstream task tracking prURL1 and deliver a merged status via applyPRStatus.
	upstream, err := s.createTodo(ctx, "Upstream PR 77", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.setTaskStageOn(ctx, upstream.ID, "review", "", "", false, prURL1)

	now := time.Now().UTC()
	if err := s.applyPRStatus(ctx, todoNow(s, upstream.ID), TodoPRStatus{
		URL:       prURL1,
		State:     "merged",
		CheckedAt: now,
	}); err != nil {
		t.Fatalf("applyPRStatus: %v", err)
	}

	// Targeted fast-path must have resolved the blocker and fired the wake immediately.
	if len(unblocked) != 1 || unblocked[0] != waiterTodoID {
		t.Fatalf("expected applyPRStatus(merged) to unblock %s immediately, got %+v", waiterTodoID, unblocked)
	}
	rem, err := st.ListActiveTodoBlockersByTarget(ctx, prURL1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rem) != 0 {
		t.Fatalf("expected blocker for %s to be resolved, still active: %+v", prURL1, rem)
	}
	s.supervise.mu.Lock()
	sig, hasWake := s.supervise.pending[waiterTodoID+"|"+sigBlockersUnblocked]
	s.supervise.mu.Unlock()
	if !hasWake || sig.Key == "" {
		t.Fatalf("expected blockers_unblocked wake for %s, got has=%t sig=%+v", waiterTodoID, hasWake, sig)
	}

	// 2. task_plan with blocked_by_pr persists external_pr blocker and wakes on periodic superviseWatch fallback sweep.
	planArgs, _ := json.Marshal(map[string]any{
		"tasks": []map[string]any{
			{
				"title":         "Deploy after external PR 88",
				"prompt":        "Run post-merge deployment checks",
				"role":          "worker",
				"readonly":      true,
				"machine":       "mac",
				"cwd":           "/work/app",
				"blocked_by_pr": []string{prURL2},
			},
		},
	})
	planOut, err := s.toolTaskPlanFrom(ctx, "mac", "conv1", planArgs)
	if err != nil {
		t.Fatalf("task_plan with blocked_by_pr: %v", err)
	}
	// Wait for any auto-started plan task to finish linking, or start it if it remained open.
	plans, err := st.TaskPlans(ctx, "conv1")
	if err != nil || len(plans) == 0 {
		t.Fatalf("expected saved plan for conv1, tool output=%s err=%v", planOut, err)
	}
	var planTodoID string
	if plans[0].State == planOpen {
		startedPlan, err := s.startPlan(ctx, plans[0].ID, nil, false)
		if err != nil {
			t.Fatalf("startPlan: %v", err)
		}
		planTodoID = startedPlan.Tasks[0].TodoID
	} else {
		var savedTasks []PlanTask
		if err := json.Unmarshal(plans[0].Tasks, &savedTasks); err != nil {
			t.Fatal(err)
		}
		if len(savedTasks) > 0 {
			planTodoID = savedTasks[0].TodoID
		}
	}
	waitFor(t, "plan task dispatch to settle", func() bool { return s.taskStarting.Load() == 0 })
	if planTodoID == "" {
		t.Fatalf("expected started plan task to have TodoID: plan=%+v", plans[0])
	}

	activePR2, err := st.ListActiveTodoBlockersByTarget(ctx, prURL2)
	if err != nil {
		t.Fatal(err)
	}
	if len(activePR2) != 1 || activePR2[0].TodoID != planTodoID {
		t.Fatalf("expected active external_pr blocker on %s for %s, got %+v", planTodoID, prURL2, activePR2)
	}

	// Install PR fetcher reporting prURL2 merged, then run periodic superviseWatch fallback sweep.
	s.setTodoBlockerPRFetcher(func(_ context.Context, targetRef string) (todoBlockerPRFact, bool) {
		if targetRef == prURL2 {
			return todoBlockerPRFact{URL: prURL2, State: "merged", Merged: true}, true
		}
		return todoBlockerPRFact{}, false
	})
	s.superviseWatch(ctx, time.Now())

	waitForCondition(t, 3*time.Second, "blocker observer to reconcile PR", func() bool {
		return !s.blockerReconcile.sweep.Load()
	})
	if len(unblocked) != 2 || unblocked[1] != planTodoID {
		t.Fatalf("expected periodic superviseWatch fallback sweep to unblock %s, got %+v", planTodoID, unblocked)
	}
	activeAfter, err := st.ListActiveTodoBlockersByTarget(ctx, prURL2)
	if err != nil {
		t.Fatal(err)
	}
	if len(activeAfter) != 0 {
		t.Fatalf("expected blocker for %s resolved after superviseWatch, got %+v", prURL2, activeAfter)
	}
}

func TestTodosV2RuntimeLoopsBlockedByPRValidation(t *testing.T) {
	st := newMemBlockerStore()
	s, f := newQuietServiceOn(t, st)
	s.briefsOptional = true
	ctx := context.Background()
	setRestartRecoveryTodosV2(t, s, true, false)

	f.mu.Lock()
	f.devices = []Device{{ID: "mac", Name: "Mac", Online: true}}
	f.mu.Unlock()

	// Non-https URL must be rejected on todo_create.
	badArgs, _ := json.Marshal(map[string]any{
		"title":         "Bad PR URL",
		"blocked_by_pr": []string{"http://github.com/org/repo/pull/1"},
	})
	if _, err := s.toolTodo(ctx, "mac", "conv1", "todo_create", badArgs); err == nil {
		t.Fatal("expected non-https blocked_by_pr URL to be rejected on todo_create")
	}

	// More than 20 URLs must be rejected on task_plan.
	tooMany := make([]string, 21)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("https://github.com/org/repo/pull/%d", i+1)
	}
	planBadArgs, _ := json.Marshal(map[string]any{
		"tasks": []map[string]any{
			{
				"title":         "Too many PR blockers",
				"prompt":        "Do work",
				"role":          "worker",
				"machine":       "mac",
				"cwd":           "/work/app",
				"blocked_by_pr": tooMany,
			},
		},
	})
	if _, err := s.toolTaskPlanFrom(ctx, "mac", "conv1", planBadArgs); err == nil {
		t.Fatal("expected >20 blocked_by_pr URLs to be rejected on task_plan")
	}
}

func TestTodosV2RuntimeLoopsBypassWhenFlagOff(t *testing.T) {
	st := newMemRuntimeLoopStore()
	s, f := newQuietServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	s.briefsOptional = true
	ctx := context.Background()

	if s.settings(ctx).todosV2On() {
		t.Fatal("expected todos_v2 default to be false")
	}

	f.mu.Lock()
	f.devices = []Device{{ID: "mac", Name: "Mac", Online: true}}
	f.mu.Unlock()

	const prURL = "https://github.com/nwverse/persona/pull/99"

	// 1. todo_create ignores blocked_by_pr when todos_v2=false (no error, no DB writes).
	createArgs, _ := json.Marshal(map[string]any{
		"title":         "Legacy todo with blocked_by_pr ignored",
		"blocked_by_pr": []string{prURL},
	})
	if _, err := s.toolTodo(ctx, "mac", "conv1", "todo_create", createArgs); err != nil {
		t.Fatalf("todo_create with todos_v2=false should ignore blocked_by_pr, got err=%v", err)
	}
	if active, err := st.ListActiveTodoBlockers(ctx); err != nil || len(active) != 0 {
		t.Fatalf("expected 0 blockers written when todos_v2=false, got len=%d err=%v", len(active), err)
	}

	// 2. task_plan ignores blocked_by_pr when todos_v2=false.
	planArgs, _ := json.Marshal(map[string]any{
		"tasks": []map[string]any{
			{
				"title":         "Legacy plan task",
				"prompt":        "Run checks",
				"role":          "worker",
				"readonly":      true,
				"machine":       "mac",
				"cwd":           "/work/app",
				"blocked_by_pr": []string{prURL},
			},
		},
	})
	if _, err := s.toolTaskPlanFrom(ctx, "mac", "conv1", planArgs); err != nil {
		t.Fatalf("task_plan with todos_v2=false should ignore blocked_by_pr, got err=%v", err)
	}
	plans, err := st.TaskPlans(ctx, "conv1")
	if err != nil || len(plans) == 0 {
		t.Fatalf("expected saved plan for conv1, err=%v", err)
	}
	var savedTasks []PlanTask
	if err := json.Unmarshal(plans[0].Tasks, &savedTasks); err != nil {
		t.Fatal(err)
	}
	if len(savedTasks) == 0 || len(savedTasks[0].BlockedByPR) != 0 {
		t.Fatalf("expected BlockedByPR cleared when todos_v2=false, got tasks=%+v", savedTasks)
	}

	// 3. Even if pre-existing recovery checkpoint + active blocker exist in DB, superviseWatch + applyPRStatus bypass 100%.
	task := setupSupervisedRecoveryTask(t, s, f, "Flag-off task", "s-off-loop", "assistant", []string{"Done."})
	if err := s.todoRecoveryStore().RecordTodoActiveCheckpoint(ctx, db.TodoRecoveryState{
		TodoID:              task.ID,
		MachineID:           "mac",
		SessionID:           "s-off-loop",
		LastActiveBootEpoch: "boot-prev",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertTodoBlocker(ctx, db.TodoBlockerRow{
		ID:        "blk_off_1",
		TodoID:    task.ID,
		Kind:      todoBlockerKindExternalPR,
		TargetRef: prURL,
		State:     db.TodoBlockerStateActive,
	}); err != nil {
		t.Fatal(err)
	}
	unblocked := 0
	s.setTodoBlockerUnblockedHook(func(_ context.Context, _ string) {
		unblocked++
	})
	s.setTodoBlockerPRFetcher(func(_ context.Context, _ string) (todoBlockerPRFact, bool) {
		return todoBlockerPRFact{URL: prURL, State: "merged", Merged: true}, true
	})

	s.setBootEpoch("boot-now")
	s.superviseWatch(ctx, time.Now())

	s.setTaskStageOn(ctx, task.ID, "review", "", "", false, prURL)
	if err := s.applyPRStatus(ctx, todoNow(s, task.ID), TodoPRStatus{
		URL:       prURL,
		State:     "merged",
		CheckedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("applyPRStatus: %v", err)
	}

	if s.todosV2RecoverySwept.Load() {
		t.Fatal("expected todosV2RecoverySwept to remain false when todos_v2=false")
	}
	if sent := sentToAgent(f); len(sent) != 0 {
		t.Fatalf("expected 0 recovery prompts when todos_v2=false, got %+v", sent)
	}
	if unblocked != 0 {
		t.Fatalf("expected 0 blocker unblocks when todos_v2=false, got %d", unblocked)
	}
	if active, err := st.ListActiveTodoBlockers(ctx); err != nil || len(active) != 1 {
		t.Fatalf("expected blocker untouched when todos_v2=false, got len=%d err=%v", len(active), err)
	}
}
