package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func findTodoByTitleForTest(s *Service, title string) Todo {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for _, td := range s.todos.todos {
		if td.Title == title {
			return copyTodoLocked(td)
		}
	}
	return Todo{}
}

func TestTodosV2AfterBlockerMirrorTodoCreateAndFlagBypass(t *testing.T) {
	ctx := context.Background()

	t.Run("todos_v2_on_mirrors_todo_done_and_validates_stage", func(t *testing.T) {
		st := newMemBlockerQueueStore()
		f := newFakeHub().withMachine()
		s := newServiceOn(t, f, st)
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		setBlockersTodosV2Flag(t, s, true, false)

		root, err := s.createPlainTodo(ctx, "Parent goal", "", db.TodoEdit{}, nil)
		if err != nil {
			t.Fatalf("create root: %v", err)
		}
		dep, err := s.createPlainTodo(ctx, "Upstream step", "", db.TodoEdit{PartOf: &root.ID}, nil)
		if err != nil {
			t.Fatalf("create dep: %v", err)
		}

		// Invalid unlock_stage must be rejected under todos_v2=true.
		badArgs, _ := json.Marshal(map[string]any{
			"title":        "Bad unlock stage",
			"part_of":      root.ID,
			"after":        []string{dep.ID},
			"unlock_stage": "not_a_real_stage",
		})
		if _, err := s.toolTodoCreate(ctx, "m1", "conv1", badArgs); err == nil {
			t.Fatal("expected invalid unlock_stage to be rejected under todos_v2=true")
		}

		// Plain after creates Todo.After and mirrors a persistent todo_done blocker row.
		args, _ := json.Marshal(map[string]any{
			"title":   "Downstream step",
			"part_of": root.ID,
			"after":   []string{dep.ID},
		})
		if _, err := s.toolTodoCreate(ctx, "m1", "conv1", args); err != nil {
			t.Fatalf("toolTodoCreate: %v", err)
		}

		child := findTodoByTitleForTest(s, "Downstream step")
		if child.ID == "" {
			t.Fatal("expected created downstream todo")
		}
		if len(child.After) != 1 || child.After[0] != dep.ID {
			t.Fatalf("expected Todo.After=%v, got %v", []string{dep.ID}, child.After)
		}

		rows, err := st.ListTodoBlockersByTodo(ctx, child.ID)
		if err != nil {
			t.Fatalf("ListTodoBlockersByTodo: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("expected 1 mirrored blocker row, got %+v", rows)
		}
		if rows[0].Kind != todoBlockerKindTodoDone || rows[0].TargetRef != dep.ID || rows[0].State != db.TodoBlockerStateActive {
			t.Fatalf("unexpected mirrored blocker row: %+v", rows[0])
		}
	})

	t.Run("todos_v2_off_bypasses_mirror_and_unlock_inputs", func(t *testing.T) {
		st := newMemBlockerQueueStore()
		f := newFakeHub().withMachine()
		s := newServiceOn(t, f, st)
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		setBlockersTodosV2Flag(t, s, false, false)

		root, err := s.createPlainTodo(ctx, "Legacy parent", "", db.TodoEdit{}, nil)
		if err != nil {
			t.Fatalf("create root: %v", err)
		}
		dep, err := s.createPlainTodo(ctx, "Legacy dep", "", db.TodoEdit{PartOf: &root.ID}, nil)
		if err != nil {
			t.Fatalf("create dep: %v", err)
		}

		args, _ := json.Marshal(map[string]any{
			"title":            "Legacy child",
			"part_of":          root.ID,
			"after":            []string{dep.ID},
			"unlock_stage":     "pr_open",
			"unlock_contracts": []string{"api_v1"},
		})
		if _, err := s.toolTodoCreate(ctx, "m1", "conv1", args); err != nil {
			t.Fatalf("toolTodoCreate with todos_v2=false: %v", err)
		}

		child := findTodoByTitleForTest(s, "Legacy child")
		if child.ID == "" || len(child.After) != 1 || child.After[0] != dep.ID {
			t.Fatalf("expected Todo.After preserved under todos_v2=false, got %+v", child)
		}
		rows, err := st.ListActiveTodoBlockers(ctx)
		if err != nil {
			t.Fatalf("ListActiveTodoBlockers: %v", err)
		}
		if len(rows) != 0 {
			t.Fatalf("expected zero blocker rows when todos_v2=false, got %+v", rows)
		}
	})
}

func TestTodosV2AfterBlockerMirrorStageGateStructuredAndLegacyNotes(t *testing.T) {
	ctx := context.Background()
	st := newMemBlockerQueueStore()
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	setBlockersTodosV2Flag(t, s, true, false)

	root, err := s.createPlainTodo(ctx, "Feature epic", "", db.TodoEdit{}, nil)
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	dep, err := s.createPlainTodo(ctx, "API contract PR", "", db.TodoEdit{PartOf: &root.ID}, nil)
	if err != nil {
		t.Fatalf("create dep: %v", err)
	}

	// 1. Structured unlock_stage + unlock_contracts on todo_create (no legacy notes syntax).
	args, _ := json.Marshal(map[string]any{
		"title":            "Web consumer",
		"part_of":          root.ID,
		"after":            []string{dep.ID},
		"unlock_stage":     "PR_OPEN",
		"unlock_contracts": []string{"API_V1", "api_v1", "schema_v1"},
	})
	if _, err := s.toolTodoCreate(ctx, "m1", "conv1", args); err != nil {
		t.Fatalf("toolTodoCreate structured stage gate: %v", err)
	}
	child := findTodoByTitleForTest(s, "Web consumer")
	if child.ID == "" {
		t.Fatal("expected Web consumer todo")
	}

	rows, err := st.ListTodoBlockersByTodo(ctx, child.ID)
	if err != nil {
		t.Fatalf("ListTodoBlockersByTodo: %v", err)
	}
	if len(rows) != 1 || rows[0].Kind != todoBlockerKindTodoStage || rows[0].TargetRef != dep.ID {
		t.Fatalf("expected single todo_stage blocker, got %+v", rows)
	}
	var cond todoStageBlockerCondition
	if err := json.Unmarshal([]byte(rows[0].ConditionJSON), &cond); err != nil {
		t.Fatalf("unmarshal condition_json %q: %v", rows[0].ConditionJSON, err)
	}
	if cond.RequiredStage != "pr_open" || len(cond.RequiredContracts) != 2 || cond.RequiredContracts[0] != "api_v1" || cond.RequiredContracts[1] != "schema_v1" {
		t.Fatalf("unexpected stage condition_json: %+v", cond)
	}

	// Before dep reaches pr_open + contracts, both stagegate evaluation and blocker reconciliation stay blocked.
	if _, ready := s.todoAfterReadyState(ctx, child); ready {
		t.Fatal("expected child to remain unready before prereq reaches pr_open + contracts")
	}
	sumBefore, err := s.ReconcileTodoBlockers(ctx, dep.ID)
	if err != nil {
		t.Fatalf("ReconcileTodoBlockers before ready: %v", err)
	}
	if len(sumBefore.ResolvedIDs) != 0 || len(sumBefore.UnblockedTodoIDs) != 0 {
		t.Fatalf("expected no resolution before ready, got %+v", sumBefore)
	}

	// 2. Legacy notes syntax remains compatible and also mirrors into todo_stage.
	legacyArgs, _ := json.Marshal(map[string]any{
		"title":   "Legacy notes consumer",
		"part_of": root.ID,
		"after":   []string{dep.ID},
		"notes":   "[unlock_stage: pr_open] [unlock_contracts: api_v1]",
	})
	if _, err := s.toolTodoCreate(ctx, "m1", "conv1", legacyArgs); err != nil {
		t.Fatalf("toolTodoCreate legacy notes: %v", err)
	}
	legacyChild := findTodoByTitleForTest(s, "Legacy notes consumer")
	legacyRows, err := st.ListTodoBlockersByTodo(ctx, legacyChild.ID)
	if err != nil {
		t.Fatalf("ListTodoBlockersByTodo legacy: %v", err)
	}
	if len(legacyRows) != 1 || legacyRows[0].Kind != todoBlockerKindTodoStage {
		t.Fatalf("expected legacy notes todo to mirror todo_stage blocker, got %+v", legacyRows)
	}

	// Advance dep to open PR + required contracts.
	prURL := "https://github.com/example/repo/pull/404"
	notes := "[contracts: api_v1, schema_v1]"
	if _, err := s.editTodo(ctx, dep.ID, db.TodoEdit{Notes: &notes}); err != nil {
		t.Fatalf("edit dep notes: %v", err)
	}
	s.todos.mu.Lock()
	if cur := s.todos.todos[dep.ID]; cur != nil {
		cur.PRURL = prURL
		cur.PRStatus = &TodoPRStatus{URL: prURL, State: "open"}
	}
	s.todos.mu.Unlock()

	// Structured stage gate must now satisfy todoAfterReadyState even without notes on child.
	if _, ready := s.todoAfterReadyState(ctx, child); !ready {
		t.Fatal("expected structured stage gate child to be ready once dep reaches pr_open + contracts")
	}

	// Reconciling blockers resolves both stage gates and emits an unblock with the upstream PR URL.
	sumAfter, err := s.ReconcileTodoBlockers(ctx, dep.ID)
	if err != nil {
		t.Fatalf("ReconcileTodoBlockers after ready: %v", err)
	}
	if len(sumAfter.UnblockedTodoIDs) != 2 {
		t.Fatalf("expected 2 unblocked todos, got %+v", sumAfter)
	}

	s.supervise.mu.Lock()
	sig, ok := s.supervise.pending[child.ID+"|"+sigBlockersUnblocked]
	s.supervise.mu.Unlock()
	if !ok {
		t.Fatalf("expected blockers_unblocked pending for %s", child.ID)
	}
	if !slices.Contains(sig.BlockerPRs, prURL) {
		t.Fatalf("expected blockers_unblocked to carry upstream PR URL %q, got %q", prURL, sig.BlockerPRs)
	}
}

func TestTodosV2AfterBlockerMirrorTaskPlanUnlockStage(t *testing.T) {
	ctx := context.Background()

	t.Run("todos_v2_on_mirrors_task_plan_after_and_unlock_stage", func(t *testing.T) {
		st := newMemBlockerQueueStore()
		f := newFakeHub().withMachine()
		s := newServiceOn(t, f, st)
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		s.briefsOptional = true
		setBlockersTodosV2Flag(t, s, true, false)

		root, err := s.createPlainTodo(ctx, "Plan root", "", db.TodoEdit{}, nil)
		if err != nil {
			t.Fatalf("create root: %v", err)
		}
		dep, err := s.createPlainTodo(ctx, "Plan prereq", "", db.TodoEdit{PartOf: &root.ID}, nil)
		if err != nil {
			t.Fatalf("create dep: %v", err)
		}

		// Invalid unlock_stage on task_plan fails validation under todos_v2=true.
		badPlanArgs, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{
				{
					"title":        "Invalid stage task",
					"prompt":       "do work",
					"machine":      "server",
					"cwd":          "/tmp",
					"role":         "worker",
					"part_of":      root.ID,
					"after":        []string{dep.ID},
					"unlock_stage": "bogus",
				},
			},
		})
		if _, err := s.toolTaskPlanFrom(ctx, "server", "conv1", badPlanArgs); err == nil {
			t.Fatal("expected invalid unlock_stage on task_plan to fail")
		}

		// Valid unlock_stage + unlock_contracts on task_plan -> startPlan mirrors todo_stage blocker.
		planArgs, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{
				{
					"title":            "Staged planned task",
					"prompt":           "build ui",
					"machine":          "server",
					"cwd":              "/tmp",
					"role":             "worker",
					"part_of":          root.ID,
					"after":            []string{dep.ID},
					"unlock_stage":     "contract_ready",
					"unlock_contracts": []string{"dto_v1"},
				},
			},
		})
		if _, err := s.toolTaskPlanFrom(ctx, "server", "conv1", planArgs); err != nil {
			t.Fatalf("toolTaskPlanFrom: %v", err)
		}

		var planID string
		st.planMu.Lock()
		for id := range st.plans {
			planID = id
		}
		st.planMu.Unlock()
		if planID == "" {
			t.Fatal("expected saved plan")
		}

		started, err := s.startPlan(ctx, planID, nil, false)
		if err != nil {
			t.Fatalf("startPlan: %v", err)
		}
		if len(started.Tasks) != 1 || started.Tasks[0].TodoID == "" {
			t.Fatalf("expected started task with TodoID, got %+v", started.Tasks)
		}
		todoID := started.Tasks[0].TodoID

		rows, err := st.ListTodoBlockersByTodo(ctx, todoID)
		if err != nil {
			t.Fatalf("ListTodoBlockersByTodo: %v", err)
		}
		if len(rows) != 1 || rows[0].Kind != todoBlockerKindTodoStage || rows[0].TargetRef != dep.ID {
			t.Fatalf("expected mirrored todo_stage blocker from task_plan, got %+v", rows)
		}
		if !strings.Contains(rows[0].ConditionJSON, `"required_stage":"contract_ready"`) ||
			!strings.Contains(rows[0].ConditionJSON, `"required_contracts":["dto_v1"]`) {
			t.Fatalf("unexpected condition_json: %s", rows[0].ConditionJSON)
		}
	})

	t.Run("saved_plan_keeps_its_unlock_rule_after_service_recreation", func(t *testing.T) {
		st := newMemBlockerQueueStore()
		f := newFakeHub().withMachine()
		first := newServiceOn(t, f, st)
		first.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		setBlockersTodosV2Flag(t, first, true, false)

		root, err := first.createPlainTodo(ctx, "Persisted plan root", "", db.TodoEdit{}, nil)
		if err != nil {
			t.Fatalf("create root: %v", err)
		}
		dep, err := first.createPlainTodo(ctx, "Persisted plan prereq", "", db.TodoEdit{PartOf: &root.ID}, nil)
		if err != nil {
			t.Fatalf("create dep: %v", err)
		}
		planArgs, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"title":            "Persisted staged task",
				"prompt":           "build ui",
				"machine":          "server",
				"cwd":              "/tmp",
				"role":             "worker",
				"part_of":          root.ID,
				"after":            []string{dep.ID},
				"unlock_stage":     "pr_open",
				"unlock_contracts": []string{"dto_v1"},
			}},
		})
		if _, err := first.toolTaskPlanFrom(ctx, "server", "conv1", planArgs); err != nil {
			t.Fatalf("toolTaskPlanFrom: %v", err)
		}
		var planID string
		st.planMu.Lock()
		for id := range st.plans {
			planID = id
		}
		st.planMu.Unlock()
		if planID == "" {
			t.Fatal("expected saved plan")
		}

		// A fresh Service has none of first's in-memory plan unlock hooks.
		second := newServiceOn(t, f, st)
		second.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		if err := second.ensureTodosLoaded(ctx); err != nil {
			t.Fatalf("ensureTodosLoaded: %v", err)
		}
		persisted, err := second.loadPlan(ctx, planID)
		if err != nil {
			t.Fatalf("loadPlan: %v", err)
		}
		if len(persisted.Tasks) != 1 || persisted.Tasks[0].UnlockStage != "pr_open" ||
			len(persisted.Tasks[0].UnlockContracts) != 1 || persisted.Tasks[0].UnlockContracts[0] != "dto_v1" {
			t.Fatalf("saved unlock rule did not survive recreation: %+v", persisted.Tasks)
		}
		started, err := second.startPlan(ctx, planID, nil, false)
		if err != nil {
			t.Fatalf("startPlan after recreation: %v", err)
		}
		if len(started.Tasks) != 1 || started.Tasks[0].TodoID == "" {
			t.Fatalf("expected started task with TodoID, got %+v", started.Tasks)
		}
		rows, err := st.ListTodoBlockersByTodo(ctx, started.Tasks[0].TodoID)
		if err != nil {
			t.Fatalf("ListTodoBlockersByTodo: %v", err)
		}
		if len(rows) != 1 || rows[0].Kind != todoBlockerKindTodoStage {
			t.Fatalf("expected persisted todo_stage blocker, got %+v", rows)
		}
		if !strings.Contains(rows[0].ConditionJSON, `"required_stage":"pr_open"`) ||
			!strings.Contains(rows[0].ConditionJSON, `"required_contracts":["dto_v1"]`) {
			t.Fatalf("unexpected condition_json after recreation: %s", rows[0].ConditionJSON)
		}
	})

	t.Run("todos_v2_off_ignores_task_plan_unlock_stage", func(t *testing.T) {
		st := newMemBlockerQueueStore()
		f := newFakeHub().withMachine()
		s := newServiceOn(t, f, st)
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		s.briefsOptional = true
		setBlockersTodosV2Flag(t, s, false, false)

		root, err := s.createPlainTodo(ctx, "Legacy plan root", "", db.TodoEdit{}, nil)
		if err != nil {
			t.Fatalf("create root: %v", err)
		}
		dep, err := s.createPlainTodo(ctx, "Legacy plan prereq", "", db.TodoEdit{PartOf: &root.ID}, nil)
		if err != nil {
			t.Fatalf("create dep: %v", err)
		}

		planArgs, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{
				{
					"title":            "Legacy planned task",
					"prompt":           "build ui",
					"machine":          "server",
					"cwd":              "/tmp",
					"role":             "worker",
					"part_of":          root.ID,
					"after":            []string{dep.ID},
					"unlock_stage":     "pr_open",
					"unlock_contracts": []string{"dto_v1"},
				},
			},
		})
		if _, err := s.toolTaskPlanFrom(ctx, "server", "conv1", planArgs); err != nil {
			t.Fatalf("toolTaskPlanFrom todos_v2=false: %v", err)
		}
		var planID string
		st.planMu.Lock()
		for id := range st.plans {
			planID = id
		}
		st.planMu.Unlock()
		if _, err := s.startPlan(ctx, planID, nil, false); err != nil {
			t.Fatalf("startPlan todos_v2=false: %v", err)
		}
		rows, err := st.ListActiveTodoBlockers(ctx)
		if err != nil {
			t.Fatalf("ListActiveTodoBlockers: %v", err)
		}
		if len(rows) != 0 {
			t.Fatalf("expected zero blocker rows when todos_v2=false, got %+v", rows)
		}
	})
}

func TestTodosV2AfterBlockerMirrorReorganizePrunesStaleWithoutTouchingExternalPR(t *testing.T) {
	ctx := context.Background()
	st := newMemBlockerQueueStore()
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	setBlockersTodosV2Flag(t, s, true, false)

	root1, err := s.createPlainTodo(ctx, "Root 1", "", db.TodoEdit{}, nil)
	if err != nil {
		t.Fatalf("create root1: %v", err)
	}
	root2, err := s.createPlainTodo(ctx, "Root 2", "", db.TodoEdit{}, nil)
	if err != nil {
		t.Fatalf("create root2: %v", err)
	}
	dep1, err := s.createPlainTodo(ctx, "Dep 1", "", db.TodoEdit{PartOf: &root1.ID}, nil)
	if err != nil {
		t.Fatalf("create dep1: %v", err)
	}
	dep2, err := s.createPlainTodo(ctx, "Dep 2", "", db.TodoEdit{PartOf: &root1.ID}, nil)
	if err != nil {
		t.Fatalf("create dep2: %v", err)
	}

	extPR := "https://github.com/example/upstream/pull/77"
	args, _ := json.Marshal(map[string]any{
		"title":         "Child with mixed blockers",
		"part_of":       root1.ID,
		"after":         []string{dep1.ID, dep2.ID},
		"blocked_by_pr": []string{extPR},
	})
	if _, err := s.toolTodoCreate(ctx, "m1", "conv1", args); err != nil {
		t.Fatalf("toolTodoCreate: %v", err)
	}

	child := findTodoByTitleForTest(s, "Child with mixed blockers")
	if child.ID == "" {
		t.Fatal("expected child todo")
	}

	// Remove dep2 from the todo map so reorganizeTodos prunes it from child.After.
	s.todos.mu.Lock()
	delete(s.todos.todos, dep2.ID)
	s.todos.mu.Unlock()

	if _, err := s.reorganizeTodos(ctx, todoReorganizeRequest{
		IDs:    []string{child.ID},
		PartOf: &root2.ID,
	}); err != nil {
		t.Fatalf("reorganizeTodos: %v", err)
	}

	rows, err := st.ListTodoBlockersByTodo(ctx, child.ID)
	if err != nil {
		t.Fatalf("ListTodoBlockersByTodo: %v", err)
	}
	byKey := make(map[string]db.TodoBlockerRow, len(rows))
	for _, r := range rows {
		byKey[r.Kind+"|"+r.TargetRef] = r
	}
	if r := byKey[todoBlockerKindTodoDone+"|"+dep1.ID]; r.State != db.TodoBlockerStateActive {
		t.Fatalf("expected dep1 mirrored blocker to stay active, got %+v", r)
	}
	if r := byKey[todoBlockerKindTodoDone+"|"+dep2.ID]; r.State != db.TodoBlockerStateCancelled {
		t.Fatalf("expected pruned dep2 mirrored blocker to be cancelled, got %+v", r)
	}
	if r := byKey[todoBlockerKindExternalPR+"|"+extPR]; r.State != db.TodoBlockerStateActive {
		t.Fatalf("expected external_pr blocker to stay active untouched, got %+v", r)
	}
}

func TestTodosV2AfterBlockerMirrorSigTodoReadyIncludesUpstreamPRLinks(t *testing.T) {
	ctx := context.Background()
	st := newMemBlockerQueueStore()
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	setBlockersTodosV2Flag(t, s, true, false)

	root, err := s.createPlainTodo(ctx, "Release train", "", db.TodoEdit{}, nil)
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	dep, err := s.createPlainTodo(ctx, "Upstream PR task", "", db.TodoEdit{PartOf: &root.ID}, nil)
	if err != nil {
		t.Fatalf("create dep: %v", err)
	}
	child, err := s.createPlainTodo(ctx, "Downstream deploy task", "", db.TodoEdit{
		PartOf: &root.ID,
		After:  &[]string{dep.ID},
	}, nil)
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	prURL := "https://github.com/example/service/pull/512"
	done := true
	if _, err := s.editTodo(ctx, dep.ID, db.TodoEdit{Done: &done}); err != nil {
		t.Fatalf("complete dep: %v", err)
	}
	s.todos.mu.Lock()
	if cur := s.todos.todos[dep.ID]; cur != nil {
		cur.PRURL = prURL
		cur.PRStatus = &TodoPRStatus{URL: prURL, State: "merged"}
	}
	s.todos.mu.Unlock()

	s.evaluateTodoAfterDependents(ctx, dep.ID)

	s.supervise.mu.Lock()
	sig, ok := s.supervise.pending[child.ID+"|"+sigTodoReady]
	s.supervise.mu.Unlock()
	if !ok {
		t.Fatalf("expected sigTodoReady pending for %s", child.ID)
	}
	if !strings.Contains(sig.Detail, prURL) {
		t.Fatalf("expected sigTodoReady Detail to contain upstream PR URL %q, got %q", prURL, sig.Detail)
	}
}
