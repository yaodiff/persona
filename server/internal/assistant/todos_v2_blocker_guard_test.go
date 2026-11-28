package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// memBlockerGuardStore extends memBlockerQueueStore with gatewayQueueStore support so
// gateway_quota_wait and queueQuotaStart CAS transitions can be tested in memory.
type memBlockerGuardStore struct {
	*memBlockerQueueStore
	svc *Service
}

func newMemBlockerGuardStore() *memBlockerGuardStore {
	return &memBlockerGuardStore{memBlockerQueueStore: newMemBlockerQueueStore()}
}

func (m *memBlockerGuardStore) UnstartedQueuedTodo(_ context.Context, todoID, planID string, since time.Time) (bool, error) {
	if m.svc == nil {
		return false, nil
	}
	todo, ok := m.svc.todoNow(todoID)
	return ok && unstartedTodo(todo) && todo.PlanID == planID &&
		(todo.ReopenedAt == nil || !todo.ReopenedAt.After(since)), nil
}

func (m *memBlockerGuardStore) ClaimUnstartedTaskQueue(
	ctx context.Context,
	planID string,
	index int,
	todoID string,
	since time.Time,
	expected, replacement, decision json.RawMessage,
	updatedAt time.Time,
) (bool, error) {
	ready, err := m.UnstartedQueuedTodo(ctx, todoID, planID, since)
	if err != nil || !ready {
		return false, err
	}
	m.planMu.Lock()
	defer m.planMu.Unlock()
	row, ok := m.plans[planID]
	if !ok || row.State != planStarted {
		return false, nil
	}
	var tasks []PlanTask
	if err := json.Unmarshal(row.Tasks, &tasks); err != nil || index < 0 || index >= len(tasks) {
		return false, err
	}
	if tasks[index].TodoID != todoID {
		return false, nil
	}
	actual, err := json.Marshal(tasks[index].Queue)
	if err != nil {
		return false, err
	}
	if string(actual) != string(expected) {
		return false, nil
	}
	if string(replacement) == "null" {
		tasks[index].Queue = nil
	} else {
		var q TaskQueue
		if err := json.Unmarshal(replacement, &q); err != nil {
			return false, err
		}
		tasks[index].Queue = &q
	}
	if len(decision) > 0 && string(decision) != "null" {
		var pl placement
		if err := json.Unmarshal(decision, &pl); err != nil {
			return false, err
		}
		tasks[index].Placement = &pl
	}
	raw, err := json.Marshal(tasks)
	if err != nil {
		return false, err
	}
	row.Tasks = raw
	row.UpdatedAt = updatedAt
	m.plans[planID] = row
	return true, nil
}

func newBlockerGuardTestEnv(t *testing.T, todosV2 bool) (*Service, *fakeHub, *memBlockerGuardStore) {
	t.Helper()
	st := newMemBlockerGuardStore()
	f := newFakeHub().withMachine()
	f.devices = []Device{
		{ID: "server", Name: "Server", Kind: "local", Home: "/home/server", Online: true, Reachable: true},
		{ID: "work-1", Name: "Work 1", Kind: "remote", Home: "/home/work", Online: true, Reachable: true},
	}
	f.devicesFull = true
	f.sessions["work-1"] = nil
	s := newServiceOn(t, f, st)
	st.svc = s
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	s.briefsOptional = true
	if todosV2 {
		setBlockersTodosV2Flag(t, s, true, false)
	}
	return s, f, st
}

func upsertActiveExternalPRBlocker(t *testing.T, st *memBlockerGuardStore, todoID, prURL string) string {
	t.Helper()
	now := time.Now().UTC()
	row, err := st.UpsertTodoBlocker(context.Background(), db.TodoBlockerRow{
		ID:        "blk_" + todoID + "_ext",
		TodoID:    todoID,
		Kind:      "external_pr",
		TargetRef: prURL,
		State:     db.TodoBlockerStateActive,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("UpsertTodoBlocker: %v", err)
	}
	return row.ID
}

func TestTodosV2BlockerGuardPendingPlacementDefersUntilBlockerClears(t *testing.T) {
	s, f, st := newBlockerGuardTestEnv(t, true)
	ctx := context.Background()
	const prURL = "https://github.com/example/repo/pull/401"
	s.touchPlacementLease("test-scheduler", s.placementNow())

	// Create a plan with Machine == "" (auto-placement) AND blocked_by_pr.
	planArgs, err := json.Marshal(map[string]any{
		"tasks": []map[string]any{
			{
				"title":         "Auto-placed blocked task",
				"prompt":        "Run after external PR merges",
				"role":          "worker",
				"readonly":      true,
				"cwd":           "/work/app",
				"blocked_by_pr": []string{prURL},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.toolTaskPlanFrom(ctx, "", "conv1", planArgs); err != nil {
		t.Fatalf("toolTaskPlanFrom: %v", err)
	}
	st.planMu.Lock()
	var planID string
	for id := range st.plans {
		planID = id
	}
	st.planMu.Unlock()
	if planID == "" {
		t.Fatal("expected a persisted task plan")
	}
	plan, err := s.loadPlan(ctx, planID)
	if err != nil {
		t.Fatalf("loadPlan: %v", err)
	}
	if plan.State != planStarted {
		plan, err = s.startPlan(ctx, planID, nil, false)
		if err != nil {
			t.Fatalf("startPlan: %v", err)
		}
	}
	if len(plan.Tasks) != 1 || plan.Tasks[0].TodoID == "" {
		t.Fatalf("expected 1 started plan task, got %+v", plan.Tasks)
	}
	todoID := plan.Tasks[0].TodoID
	if plan.Tasks[0].Queue == nil || plan.Tasks[0].Queue.Key != pendingPlacementQueueKey {
		t.Fatalf("expected task initially in %q, got %+v", pendingPlacementQueueKey, plan.Tasks[0].Queue)
	}
	if len(f.started()) != 0 {
		t.Fatalf("expected 0 started agents during pending placement, got %d", len(f.started()))
	}

	// Assign remote work machine via placement API while the external_pr blocker is still active.
	// Read the live placement version (the fallback loop may have bumped it while waiting).
	var assignVersion = 1
	if live, err := s.loadPlan(ctx, planID); err == nil && len(live.Tasks) > 0 && live.Tasks[0].Queue != nil && live.Tasks[0].Queue.Placement != nil && live.Tasks[0].Queue.Placement.Version > 0 {
		assignVersion = live.Tasks[0].Queue.Placement.Version
	}
	assignBody, _ := json.Marshal(map[string]any{
		"machine": "work-1",
		"reason":  "placed on work-1",
		"version": assignVersion,
	})
	w := doBlockerHTTP(s, http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/assign", "full", string(assignBody))
	if w.Code == http.StatusConflict {
		var conflict struct {
			CurrentVersion int `json:"current_version"`
		}
		if json.Unmarshal(w.Body.Bytes(), &conflict) == nil && conflict.CurrentVersion > 0 {
			assignBody, _ = json.Marshal(map[string]any{
				"machine": "work-1",
				"reason":  "placed on work-1",
				"version": conflict.CurrentVersion,
			})
			w = doBlockerHTTP(s, http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/assign", "full", string(assignBody))
		}
	}
	if w.Code != http.StatusOK {
		t.Fatalf("POST placement assign: code=%d body=%s", w.Code, w.Body.String())
	}

	// Agent must NOT start while blocker is active; task must transition to blockerWaitQueueKey
	// with the assigned machine and cwd preserved.
	if got := len(f.started()); got != 0 {
		t.Fatalf("agent started prematurely after placement assignment while blocker active: started=%d", got)
	}
	reloaded, err := s.loadPlan(ctx, planID)
	if err != nil {
		t.Fatalf("loadPlan after assign: %v", err)
	}
	task := reloaded.Tasks[0]
	if task.Machine != "work-1" {
		t.Fatalf("expected assigned machine %q preserved, got %q", "work-1", task.Machine)
	}
	if task.Cwd != "/work/app" {
		t.Fatalf("expected cwd %q preserved, got %q", "/work/app", task.Cwd)
	}
	if task.Queue == nil || task.Queue.Key != blockerWaitQueueKey {
		t.Fatalf("expected task transitioned to %q, got %+v", blockerWaitQueueKey, task.Queue)
	}
	if task.Queue.Claimed {
		t.Fatalf("expected Claimed=false in blockerWaitQueue, got true")
	}
	if len(task.Queue.Blockers) != 1 || task.Queue.Blockers[0] != prURL {
		t.Fatalf("expected queue blockers [%s], got %+v", prURL, task.Queue.Blockers)
	}
	if td, ok := s.todoNow(todoID); !ok || td.StartError != "" || td.DoneAt != nil {
		t.Fatalf("expected todo open and not failed, got ok=%v todo=%+v", ok, td)
	}

	// Sweeping while blocker is active must still not launch the agent.
	s.sweepAdmissionQueue(ctx)
	if got := len(f.started()); got != 0 {
		t.Fatalf("agent started during sweep while blocker active: started=%d", got)
	}

	// Resolve blocker and sweep: agent should start on the already-assigned machine.
	rows, err := st.ListTodoBlockersByTodo(ctx, todoID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected 1 active blocker row, got rows=%+v err=%v", rows, err)
	}
	if _, err := st.ResolveTodoBlocker(ctx, rows[0].ID, time.Now().UTC()); err != nil {
		t.Fatalf("ResolveTodoBlocker: %v", err)
	}
	s.sweepAdmissionQueue(ctx)

	waitForCondition(t, 3*time.Second, "agent to start after blocker resolves", func() bool {
		return len(f.started()) == 1 && s.taskStarting.Load() == 0
	})
	afterStart, err := s.loadPlan(ctx, planID)
	if err != nil {
		t.Fatalf("loadPlan after start: %v", err)
	}
	if afterStart.Tasks[0].Queue != nil {
		t.Fatalf("expected queue cleared after agent start, got %+v", afterStart.Tasks[0].Queue)
	}
}

func TestTodosV2BlockerGuardWorkspaceQueueUnstallsNextWaiter(t *testing.T) {
	s, f, st := newBlockerGuardTestEnv(t, true)
	ctx := context.Background()
	const prURL = "https://github.com/example/repo/pull/402"

	todoA, err := s.createTaskTodoAs(ctx, "todo_ws_a", "Blocked workspace waiter", "Task A", "conv1", "plan_ws", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	todoB, err := s.createTaskTodoAs(ctx, "todo_ws_b", "Unblocked workspace waiter", "Task B", "conv1", "plan_ws", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	qA := s.workspaceQueueEntry(ctx, "server", "/work/app", "owner_old", "Previous owner", false, nil)
	qB := s.workspaceQueueEntry(ctx, "server", "/work/app", "owner_old", "Previous owner", false, nil)
	plan := &TaskPlan{
		ID:           "plan_ws",
		Conversation: "conv1",
		State:        planStarted,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
		Tasks: []PlanTask{
			{
				TodoID:   todoA.ID,
				Title:    todoA.Title,
				Prompt:   "Task A",
				Role:     "worker",
				Readonly: true,
				Machine:  "server",
				Cwd:      "/work/app",
				Queue:    &qA,
			},
			{
				TodoID:   todoB.ID,
				Title:    todoB.Title,
				Prompt:   "Task B",
				Role:     "worker",
				Readonly: true,
				Machine:  "server",
				Cwd:      "/work/app",
				Queue:    &qB,
			},
		},
	}
	if err := s.savePlan(ctx, plan); err != nil {
		t.Fatalf("savePlan: %v", err)
	}

	// Add an active external_pr blocker to Task A while it is queued on the workspace.
	upsertActiveExternalPRBlocker(t, st, todoA.ID, prURL)

	// Drain the free workspace: Task A must transition to blockerWaitQueueKey without stalling Task B.
	s.drainWorkspaceQueue(ctx, "server", "/work/app")

	waitForCondition(t, 3*time.Second, "unblocked waiter Task B to start", func() bool {
		return len(f.started()) == 1 && s.taskStarting.Load() == 0
	})

	reloaded, err := s.loadPlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("loadPlan: %v", err)
	}
	taskA := reloaded.Tasks[0]
	if taskA.Queue == nil || taskA.Queue.Key != blockerWaitQueueKey {
		t.Fatalf("expected Task A moved to %q, got %+v", blockerWaitQueueKey, taskA.Queue)
	}
	if taskA.Machine != "server" || taskA.Cwd != "/work/app" {
		t.Fatalf("expected Task A machine/cwd preserved, got machine=%q cwd=%q", taskA.Machine, taskA.Cwd)
	}
	if taskA.Queue.Claimed {
		t.Fatalf("expected Task A Claimed=false, got true")
	}
	if tdA, ok := s.todoNow(todoA.ID); !ok || tdA.StartError != "" || len(tdA.Agents) != 0 {
		t.Fatalf("expected Task A unstarted and not failed, got ok=%v todo=%+v", ok, tdA)
	}
	if tdB, ok := s.todoNow(todoB.ID); !ok || len(tdB.Agents) != 1 {
		t.Fatalf("expected Task B started with 1 agent, got ok=%v agents=%d", ok, len(tdB.Agents))
	}
}

func TestTodosV2BlockerGuardRunPlanTaskStartFromWorkspaceAndGatewayClaims(t *testing.T) {
	t.Run("claimed workspace start re-parks and releases claims", func(t *testing.T) {
		s, f, st := newBlockerGuardTestEnv(t, true)
		ctx := context.Background()
		const prURL = "https://github.com/example/repo/pull/403"

		todo, err := s.createTaskTodoAs(ctx, "todo_ws_claim", "Claimed workspace task", "Do work", "conv1", "plan_ws_claim", "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		q := s.workspaceQueueEntry(ctx, "server", "/work/app", "", "", false, nil)
		q.Claimed = true
		plan := &TaskPlan{
			ID:           "plan_ws_claim",
			Conversation: "conv1",
			State:        planStarted,
			CreatedAt:    time.Now().UTC(),
			UpdatedAt:    time.Now().UTC(),
			Tasks: []PlanTask{{
				TodoID:   todo.ID,
				Title:    todo.Title,
				Prompt:   "Do work",
				Role:     "worker",
				Readonly: true,
				Machine:  "server",
				Cwd:      "/work/app",
				Queue:    &q,
			}},
		}
		if err := s.savePlan(ctx, plan); err != nil {
			t.Fatal(err)
		}
		upsertActiveExternalPRBlocker(t, st, todo.ID, prURL)

		if !s.preclaimWorkspace(ctx, "server", "/work/app", todo.ID) {
			t.Fatal("expected preclaimWorkspace to succeed")
		}
		admissionReleased := false
		task := plan.Tasks[0]
		task.admissionRelease = func() { admissionReleased = true }

		s.runPlanTaskStart(plan.ID, plan.Conversation, 0, task, false)

		if len(f.started()) != 0 {
			t.Fatalf("expected 0 started agents, got %d", len(f.started()))
		}
		if !admissionReleased {
			t.Fatal("expected admissionRelease to be called on blocker defer")
		}
		if owner := s.taskWorkspaceOwner(ctx, "server", "/work/app", "", false); owner != nil {
			t.Fatalf("expected workspace claim released, still owned by %+v", owner)
		}
		reloaded, err := s.loadPlan(ctx, plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		gotQ := reloaded.Tasks[0].Queue
		if gotQ == nil || gotQ.Key != blockerWaitQueueKey || gotQ.Claimed || gotQ.Workspace != nil {
			t.Fatalf("expected re-parked blockerWaitQueue without Claimed/Workspace, got %+v", gotQ)
		}
		if td, ok := s.todoNow(todo.ID); !ok || td.StartError != "" || td.DoneAt != nil {
			t.Fatalf("expected todo unfailed, got ok=%v todo=%+v", ok, td)
		}
	})

	t.Run("gateway quota wait and claimed gateway start re-park cleanly", func(t *testing.T) {
		s, f, st := newBlockerGuardTestEnv(t, true)
		ctx := context.Background()
		const prURL = "https://github.com/example/repo/pull/404"

		todo, err := s.createTaskTodoAs(ctx, "todo_gw_claim", "Gateway queued task", "Do work", "conv1", "plan_gw", "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		wait := &taskGatewayWait{
			Pool:     GatewayQuotaPool{Source: "gw1", Provider: "claude"},
			Launcher: "claude",
			Since:    now.Add(-2 * time.Minute),
			Deadline: now.Add(20 * time.Minute),
		}
		q := TaskQueue{
			Key:      gatewayQuotaWaitKey,
			Reason:   "waiting for gateway quota",
			Attempt:  1,
			QueuedAt: now.Add(-2 * time.Minute),
			NextAt:   now.Add(-time.Minute),
			Claimed:  true,
			Gateway:  wait,
		}
		plan := &TaskPlan{
			ID:           "plan_gw",
			Conversation: "conv1",
			State:        planStarted,
			CreatedAt:    now.Add(-2 * time.Minute),
			UpdatedAt:    now,
			Tasks: []PlanTask{{
				TodoID:   todo.ID,
				Title:    todo.Title,
				Prompt:   "Do work",
				Role:     "worker",
				Readonly: true,
				Machine:  "server",
				Cwd:      "/work/app",
				Queue:    &q,
			}},
		}
		if err := s.savePlan(ctx, plan); err != nil {
			t.Fatal(err)
		}
		blockerID := upsertActiveExternalPRBlocker(t, st, todo.ID, prURL)

		admissionReleased := false
		task := plan.Tasks[0]
		task.admissionRelease = func() { admissionReleased = true }

		s.runPlanTaskStart(plan.ID, plan.Conversation, 0, task, false)

		if len(f.started()) != 0 {
			t.Fatalf("expected 0 started agents, got %d", len(f.started()))
		}
		if !admissionReleased {
			t.Fatal("expected admissionRelease to be called")
		}
		reloaded, err := s.loadPlan(ctx, plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		gotQ := reloaded.Tasks[0].Queue
		if gotQ == nil || gotQ.Key != blockerWaitQueueKey || gotQ.Claimed || gotQ.Gateway != nil {
			t.Fatalf("expected re-parked blockerWaitQueue without Claimed/Gateway, got %+v", gotQ)
		}
		if reloaded.Tasks[0].Machine != "server" || reloaded.Tasks[0].Cwd != "/work/app" {
			t.Fatalf("expected machine/cwd preserved, got %q %q", reloaded.Tasks[0].Machine, reloaded.Tasks[0].Cwd)
		}

		// Resolving the blocker and sweeping should now start the agent cleanly.
		if _, err := st.ResolveTodoBlocker(ctx, blockerID, time.Now().UTC()); err != nil {
			t.Fatalf("ResolveTodoBlocker: %v", err)
		}
		s.sweepAdmissionQueue(ctx)
		waitForCondition(t, 3*time.Second, "gateway-deferred task to start after blocker resolves", func() bool {
			return len(f.started()) == 1 && s.taskStarting.Load() == 0
		})
	})

	t.Run("retryGatewayWait defers to blockerWaitQueue before claiming", func(t *testing.T) {
		s, f, st := newBlockerGuardTestEnv(t, true)
		ctx := context.Background()
		const prURL = "https://github.com/example/repo/pull/405"

		todo, err := s.createTaskTodoAs(ctx, "todo_gw_sweep", "Gateway sweep task", "Do work", "conv1", "plan_gw_sweep", "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		q := TaskQueue{
			Key:      gatewayQuotaWaitKey,
			Reason:   "waiting for gateway quota",
			Attempt:  1,
			QueuedAt: now.Add(-2 * time.Minute),
			NextAt:   now.Add(-time.Minute),
			Gateway: &taskGatewayWait{
				Pool:     GatewayQuotaPool{Source: "gw1", Provider: "claude"},
				Launcher: "claude",
				Since:    now.Add(-2 * time.Minute),
				Deadline: now.Add(20 * time.Minute),
			},
		}
		plan := &TaskPlan{
			ID:           "plan_gw_sweep",
			Conversation: "conv1",
			State:        planStarted,
			CreatedAt:    now.Add(-2 * time.Minute),
			UpdatedAt:    now,
			Tasks: []PlanTask{{
				TodoID:   todo.ID,
				Title:    todo.Title,
				Prompt:   "Do work",
				Role:     "worker",
				Readonly: true,
				Machine:  "server",
				Cwd:      "/work/app",
				Queue:    &q,
			}},
		}
		if err := s.savePlan(ctx, plan); err != nil {
			t.Fatal(err)
		}
		upsertActiveExternalPRBlocker(t, st, todo.ID, prURL)

		s.sweepAdmissionQueue(ctx)

		if len(f.started()) != 0 {
			t.Fatalf("expected 0 started agents during gateway sweep while blocked, got %d", len(f.started()))
		}
		reloaded, err := s.loadPlan(ctx, plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		gotQ := reloaded.Tasks[0].Queue
		if gotQ == nil || gotQ.Key != blockerWaitQueueKey || gotQ.Claimed || gotQ.Gateway != nil {
			t.Fatalf("expected retryGatewayWait to re-park into blockerWaitQueue, got %+v", gotQ)
		}
	})
}

func TestTodosV2BlockerGuardBypassWhenFlagOff(t *testing.T) {
	s, f, st := newBlockerGuardTestEnv(t, false)
	ctx := context.Background()
	const prURL = "https://github.com/example/repo/pull/406"

	todo, err := s.createTaskTodoAs(ctx, "todo_flag_off", "Unblocked when flag off", "Do work", "conv1", "plan_off", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	plan := &TaskPlan{
		ID:           "plan_off",
		Conversation: "conv1",
		State:        planStarted,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
		Tasks: []PlanTask{{
			TodoID:   todo.ID,
			Title:    todo.Title,
			Prompt:   "Do work",
			Role:     "worker",
			Readonly: true,
			Machine:  "server",
			Cwd:      "/work/app",
		}},
	}
	if err := s.savePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	// Persist an active blocker row directly; with todos_v2=false, runPlanTaskStart must bypass it.
	upsertActiveExternalPRBlocker(t, st, todo.ID, prURL)

	s.runPlanTaskStart(plan.ID, plan.Conversation, 0, plan.Tasks[0], false)

	if got := len(f.started()); got != 1 {
		t.Fatalf("todos_v2=false must not block task start even with active blocker rows; started=%d", got)
	}
	reloaded, err := s.loadPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Tasks[0].Queue != nil {
		t.Fatalf("todos_v2=false must not set blocker queue, got %+v", reloaded.Tasks[0].Queue)
	}
}
