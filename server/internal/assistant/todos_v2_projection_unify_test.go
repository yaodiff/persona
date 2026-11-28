package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func intPtr(v int) *int              { return &v }
func timePtr(t time.Time) *time.Time { return &t }

func setProjectionUnifySettings(t *testing.T, s *Service, todosV2 bool, pendingSLA, canWaitSLA *int) {
	t.Helper()
	setSettings(t, s, func(cfg *Settings) {
		cfg.TodosV2 = boolPtr(todosV2)
		cfg.TodosV2PendingReviewSLAHours = pendingSLA
		cfg.TodosV2CanWaitSLAHours = canWaitSLA
	})
}

func directionBlockerEnv(t *testing.T, st Store) *todoEventEnv {
	t.Helper()
	e := newTodoEventEnv(t, st)
	if err := e.s.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestTodosV2ProjectionUnifyDirectionTree(t *testing.T) {
	st := newMemBlockerStore()
	e := directionBlockerEnv(t, st)
	e.leaders("conv_lead_a")
	ctx := context.Background()
	now := time.Now().UTC()

	// Configure todos_v2=true with custom 12h pending_review SLA.
	setProjectionUnifySettings(t, e.s, true, intPtr(12), intPtr(24))

	// 1. Task in pending_review for 15h (> 12h custom SLA): must surface as needs_you.
	agedReviewID := e.leaderTask("Aged pending review", "conv_lead_a")
	e.worked(agedReviewID)
	agedSince := now.Add(-15 * time.Hour)
	e.s.todos.mu.Lock()
	e.s.todos.todos[agedReviewID].CreatedAt = agedSince.Add(-time.Hour)
	e.s.todos.todos[agedReviewID].UpdatedAt = agedSince
	e.s.todos.todos[agedReviewID].Supervision.State = reviewPending
	e.s.todos.todos[agedReviewID].Supervision.PendingSince = &agedSince
	e.s.todos.todos[agedReviewID].Supervision.UpdatedAt = agedSince
	e.s.todos.mu.Unlock()

	// 2. Task in pending_review for 4h (< 12h custom SLA): stays working, NOT needs_you or review.
	freshReviewID := e.leaderTask("Fresh pending review", "conv_lead_a")
	e.worked(freshReviewID)
	freshSince := now.Add(-4 * time.Hour)
	e.s.todos.mu.Lock()
	e.s.todos.todos[freshReviewID].CreatedAt = freshSince.Add(-time.Hour)
	e.s.todos.todos[freshReviewID].UpdatedAt = freshSince
	e.s.todos.todos[freshReviewID].Supervision.State = reviewPending
	e.s.todos.todos[freshReviewID].Supervision.PendingSince = &freshSince
	e.s.todos.todos[freshReviewID].Supervision.UpdatedAt = freshSince
	e.s.todos.mu.Unlock()

	// 3. Idle task (> 5h) with an active persistent blocker: projected into waiting, NOT stuck.
	blockedID := e.leaderTask("Blocked by upstream PR", "conv_lead_a")
	e.worked(blockedID)
	idleSince := now.Add(-5 * time.Hour)
	e.s.todos.mu.Lock()
	e.s.todos.todos[blockedID].CreatedAt = idleSince.Add(-time.Hour)
	e.s.todos.todos[blockedID].UpdatedAt = idleSince
	e.s.todos.todos[blockedID].Supervision.UpdatedAt = idleSince
	e.s.todos.mu.Unlock()
	if _, err := e.s.AddTodoBlocker(ctx, blockedID, todoBlockerKindExternalPR, "https://github.com/example/upstream/pull/42", ""); err != nil {
		t.Fatalf("AddTodoBlocker: %v", err)
	}

	// 4. Someday task: excluded from open counts in both direction tree and board projection.
	somedayID := e.leaderTask("Deferred someday task", "conv_lead_a")
	somedayUntil := now.Add(7 * 24 * time.Hour)
	e.s.todos.mu.Lock()
	e.s.todos.todos[somedayID].SomedayUntil = &somedayUntil
	e.s.todos.mu.Unlock()

	tree := e.s.directionTree(ctx, now)
	var leadDir *treeDirection
	for i := range tree.Directions {
		if tree.Directions[i].ID == "conv_lead_a" {
			leadDir = &tree.Directions[i]
			break
		}
	}
	if leadDir == nil {
		t.Fatalf("direction conv_lead_a missing from tree: %+v", tree.Directions)
	}
	stateByID := map[string]string{}
	for _, task := range leadDir.Tasks {
		stateByID[task.ID] = task.State
	}
	if stateByID[agedReviewID] != treeNeedsYou {
		t.Fatalf("aged pending_review (>12h SLA): want %q, got %q (all=%v)", treeNeedsYou, stateByID[agedReviewID], stateByID)
	}
	if stateByID[freshReviewID] != treeWorking {
		t.Fatalf("fresh pending_review (<12h SLA): want %q, got %q (all=%v)", treeWorking, stateByID[freshReviewID], stateByID)
	}
	if stateByID[blockedID] != treeWorking {
		t.Fatalf("active-blocked idle task must not be marked stuck: want %q, got %q (all=%v)", treeWorking, stateByID[blockedID], stateByID)
	}
	if _, exists := stateByID[somedayID]; exists {
		t.Fatalf("someday task must not be open in direction tree under todos_v2=true: %v", stateByID)
	}
	if leadDir.Counts.Stuck != 0 {
		t.Fatalf("expected 0 stuck tasks in conv_lead_a, got %+v", leadDir.Counts)
	}

	// Verify open count matches board projection OpenTotal.
	listed, err := e.s.listedTodos(ctx, false)
	if err != nil {
		t.Fatalf("listedTodos: %v", err)
	}
	p := e.s.viewed(ctx, listed, now)
	directions := e.s.todoDirectionLabels(ctx, e.s.settings(ctx))
	e.s.populateTodoEffectiveDirections(listed, directions)
	board := buildTodoBoardProjectionWithOptions(listed, directions, true, p.counts())
	treeOpen := leadDir.Counts.NeedsYou + leadDir.Counts.Working + leadDir.Counts.Review + leadDir.Counts.Stuck + leadDir.Counts.Planned
	if treeOpen != board.Counts.OpenTotal {
		t.Fatalf("direction tree open count (%d) != board projection OpenTotal (%d)", treeOpen, board.Counts.OpenTotal)
	}
	if leadDir.Counts.NeedsYou != p.counts().NeedsYou {
		t.Fatalf("direction tree needs_you (%d) != projection counts.NeedsYou (%d)", leadDir.Counts.NeedsYou, p.counts().NeedsYou)
	}

	// Flip todos_v2=false: legacy todo_triage path must be preserved (aged pending_review is review, not needs_you;
	// active persistent blocker is ignored so idle task becomes stuck).
	setProjectionUnifySettings(t, e.s, false, intPtr(12), intPtr(24))
	legacyTree := e.s.directionTree(ctx, now)
	var legacyDir *treeDirection
	for i := range legacyTree.Directions {
		if legacyTree.Directions[i].ID == "conv_lead_a" {
			legacyDir = &legacyTree.Directions[i]
			break
		}
	}
	if legacyDir == nil {
		t.Fatalf("legacy direction conv_lead_a missing")
	}
	legacyStates := map[string]string{}
	for _, task := range legacyDir.Tasks {
		legacyStates[task.ID] = task.State
	}
	if legacyStates[agedReviewID] != treeReview {
		t.Fatalf("todos_v2=false: aged pending_review should use legacy review state, got %q", legacyStates[agedReviewID])
	}
	if legacyStates[blockedID] != treeStuck {
		t.Fatalf("todos_v2=false: idle task with ignored persistent blocker should be legacy stuck, got %q", legacyStates[blockedID])
	}
}

func TestTodosV2ProjectionUnifyLeaderReports(t *testing.T) {
	st := newMemBlockerStore()
	e := directionBlockerEnv(t, st)
	e.leaders("conv_lead_a")
	ctx := context.Background()
	now := time.Now().UTC()

	// Initialize leader report Since watermark.
	e.s.leaderReportsSync(ctx, now.Add(-24*time.Hour), false)
	setProjectionUnifySettings(t, e.s, true, intPtr(12), intPtr(24))

	// 1. pending_review aged 15h (> 12h SLA): escalated to NeedsYou -> stuck report fires.
	agedReviewID := e.leaderTask("Review SLA breached", "conv_lead_a")
	e.worked(agedReviewID)
	agedAt := now.Add(-15 * time.Hour)
	e.s.todos.mu.Lock()
	e.s.todos.todos[agedReviewID].CreatedAt = agedAt.Add(-time.Hour)
	e.s.todos.todos[agedReviewID].UpdatedAt = agedAt
	e.s.todos.todos[agedReviewID].Supervision.State = reviewPending
	e.s.todos.todos[agedReviewID].Supervision.PendingSince = &agedAt
	e.s.todos.todos[agedReviewID].Supervision.UpdatedAt = agedAt
	e.s.todos.mu.Unlock()

	// 2. pending_review aged 4h (< 12h SLA): still Working -> no stuck report.
	freshReviewID := e.leaderTask("Review within SLA", "conv_lead_a")
	e.worked(freshReviewID)
	freshAt := now.Add(-4 * time.Hour)
	e.s.todos.mu.Lock()
	e.s.todos.todos[freshReviewID].CreatedAt = freshAt.Add(-time.Hour)
	e.s.todos.todos[freshReviewID].UpdatedAt = freshAt
	e.s.todos.todos[freshReviewID].Supervision.State = reviewPending
	e.s.todos.todos[freshReviewID].Supervision.PendingSince = &freshAt
	e.s.todos.todos[freshReviewID].Supervision.UpdatedAt = freshAt
	e.s.todos.mu.Unlock()

	// 3. Idle task with active persistent blocker: projected into Waiting -> no false stuck report.
	blockedID := e.leaderTask("Waiting on external PR", "conv_lead_a")
	e.worked(blockedID)
	idleAt := now.Add(-6 * time.Hour)
	e.s.todos.mu.Lock()
	e.s.todos.todos[blockedID].CreatedAt = idleAt.Add(-time.Hour)
	e.s.todos.todos[blockedID].UpdatedAt = idleAt
	e.s.todos.todos[blockedID].Supervision.UpdatedAt = idleAt
	e.s.todos.mu.Unlock()
	if _, err := e.s.AddTodoBlocker(ctx, blockedID, todoBlockerKindExternalPR, "https://github.com/example/repo/pull/77", ""); err != nil {
		t.Fatalf("AddTodoBlocker: %v", err)
	}

	got := e.reports(now, true)
	stuckTodos := map[string]assistantwire.LeaderReportEvent{}
	for _, r := range got {
		if r.Kind == assistantwire.LeaderStuck {
			stuckTodos[r.Todo] = r
		}
	}
	if _, ok := stuckTodos[agedReviewID]; !ok {
		t.Fatalf("expected stuck report for aged pending_review (>12h SLA), got %+v", got)
	}
	if _, ok := stuckTodos[freshReviewID]; ok {
		t.Fatalf("unexpected stuck report for fresh pending_review (<12h SLA): %+v", stuckTodos[freshReviewID])
	}
	if _, ok := stuckTodos[blockedID]; ok {
		t.Fatalf("unexpected stuck report for active-blocked task: %+v", stuckTodos[blockedID])
	}

	// Verify todos_v2=false preserves legacy leader_reports behavior (active persistent blocker ignored -> fires stuck).
	stLegacy := newMemBlockerStore()
	eLegacy := directionBlockerEnv(t, stLegacy)
	eLegacy.leaders("conv_lead_a")
	eLegacy.s.leaderReportsSync(ctx, now.Add(-24*time.Hour), false)
	setProjectionUnifySettings(t, eLegacy.s, false, intPtr(12), intPtr(24))

	legacyBlockedID := eLegacy.leaderTask("Legacy idle task", "conv_lead_a")
	eLegacy.worked(legacyBlockedID)
	eLegacy.s.todos.mu.Lock()
	eLegacy.s.todos.todos[legacyBlockedID].CreatedAt = idleAt.Add(-time.Hour)
	eLegacy.s.todos.todos[legacyBlockedID].UpdatedAt = idleAt
	eLegacy.s.todos.todos[legacyBlockedID].Supervision.UpdatedAt = idleAt
	eLegacy.s.todos.mu.Unlock()
	if _, err := eLegacy.s.AddTodoBlocker(ctx, legacyBlockedID, todoBlockerKindExternalPR, "https://github.com/example/repo/pull/77", ""); err != nil {
		t.Fatalf("AddTodoBlocker legacy: %v", err)
	}
	legacyGot := eLegacy.reports(now, true)
	foundLegacyStuck := false
	for _, r := range legacyGot {
		if r.Kind == assistantwire.LeaderStuck && r.Todo == legacyBlockedID {
			foundLegacyStuck = true
		}
	}
	if !foundLegacyStuck {
		t.Fatalf("todos_v2=false: expected legacy stuck report for idle task, got %+v", legacyGot)
	}
}

func TestTodosV2ProjectionUnifyTasksSnapshot(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	cfgV2 := Settings{
		TodosV2:                      boolPtr(true),
		TodosV2PendingReviewSLAHours: intPtr(12),
		TodosV2CanWaitSLAHours:       intPtr(24),
	}
	cfgLegacy := Settings{
		TodosV2:                      boolPtr(false),
		TodosV2PendingReviewSLAHours: intPtr(12),
		TodosV2CanWaitSLAHours:       intPtr(24),
	}

	// 1. snapshotFailedStartsWithConfig: supervised failed start counts open under todos_v2=true,
	// superseded failed start is closed, someday failed start (>48h) is not open.
	failedAt := now.Add(-2 * time.Hour)
	oldFailedAt := now.Add(-72 * time.Hour)
	somedayUntil := now.Add(48 * time.Hour)
	doneAt := now.Add(-1 * time.Hour)

	supFailed := Todo{
		ID:          "todo_sup_fail",
		Title:       "Deploy service alpha",
		CreatedAt:   failedAt.Add(-time.Minute),
		UpdatedAt:   failedAt,
		DoneAt:      &failedAt,
		StartError:  "dial timeout",
		Supervision: &Supervision{Supervisor: "conv_lead_a"},
	}
	supersededFailed := Todo{
		ID:         "todo_superseded_fail",
		Title:      "Fix widget #99123",
		CreatedAt:  failedAt.Add(-2 * time.Minute),
		UpdatedAt:  failedAt,
		DoneAt:     &failedAt,
		StartError: "temporary failure",
	}
	finishedReplacement := Todo{
		ID:        "todo_finished_ok",
		Title:     "Fix widget #99123",
		CreatedAt: doneAt.Add(-10 * time.Minute),
		UpdatedAt: doneAt,
		DoneAt:    &doneAt,
		DoneBy:    db.DoneByReview,
	}
	somedayFailed := Todo{
		ID:           "todo_someday_fail",
		Title:        "Someday failed task",
		CreatedAt:    oldFailedAt.Add(-time.Minute),
		UpdatedAt:    oldFailedAt,
		DoneAt:       &oldFailedAt,
		StartError:   "offline",
		SomedayUntil: &somedayUntil,
	}
	failedList := []Todo{supFailed, supersededFailed, somedayFailed}
	allListed := []listedTodo{
		{Todo: supFailed},
		{Todo: supersededFailed},
		{Todo: finishedReplacement},
		{Todo: somedayFailed},
	}

	openV2 := snapshotFailedStartsWithConfig(failedList, allListed, now, time.UTC, cfgV2)
	if !openV2[supFailed.ID] {
		t.Fatalf("todos_v2=true: supervised failed start should count as open: %+v", openV2)
	}
	if openV2[supersededFailed.ID] {
		t.Fatalf("todos_v2=true: superseded failed start must not count as open: %+v", openV2)
	}
	if openV2[somedayFailed.ID] {
		t.Fatalf("todos_v2=true: someday failed start must not count as open: %+v", openV2)
	}

	// 2. snapshotDoneRuleWithConfig: honors custom PendingReviewSLAHours and CanWaitSLAHours under todos_v2=true,
	// while todos_v2=false preserves legacy todo_triage behavior.
	agedPendingSince := now.Add(-15 * time.Hour)
	agedPending := Todo{
		ID:        "todo_aged_pending",
		Title:     "Aged pending review",
		CreatedAt: agedPendingSince.Add(-time.Hour),
		UpdatedAt: agedPendingSince,
		Supervision: &Supervision{
			Supervisor:   "conv_lead_a",
			State:        reviewPending,
			PendingSince: &agedPendingSince,
			UpdatedAt:    agedPendingSince,
		},
	}
	if rule := snapshotDoneRuleWithConfig(agedPending, nil, nil, nil, nil, now, time.UTC, cfgV2); rule != ruleOwnerDecides {
		t.Fatalf("todos_v2=true aged pending_review (>12h SLA): want %q, got %q", ruleOwnerDecides, rule)
	}
	if rule := snapshotDoneRuleWithConfig(agedPending, nil, nil, nil, nil, now, time.UTC, cfgLegacy); rule != ruleFinishedUnseen {
		t.Fatalf("todos_v2=false aged pending_review: want legacy %q, got %q", ruleFinishedUnseen, rule)
	}

	freshPendingSince := now.Add(-3 * time.Hour)
	freshPending := Todo{
		ID:        "todo_fresh_pending",
		Title:     "Fresh pending review",
		CreatedAt: freshPendingSince.Add(-time.Hour),
		UpdatedAt: freshPendingSince,
		Supervision: &Supervision{
			Supervisor:   "conv_lead_a",
			State:        reviewPending,
			PendingSince: &freshPendingSince,
			UpdatedAt:    freshPendingSince,
		},
	}
	if rule := snapshotDoneRuleWithConfig(freshPending, nil, nil, nil, nil, now, time.UTC, cfgV2); rule != ruleAgentRunning {
		t.Fatalf("todos_v2=true fresh pending_review (<12h SLA): want %q, got %q", ruleAgentRunning, rule)
	}

	// Can-wait decision: within 24h SLA -> not owner_decides; past 24h SLA -> owner_decides.
	canWaitFresh := &todoAsk{Outcome: outcomeDecide, Verdict: "Choose option", AskedAt: now.Add(-6 * time.Hour), ForBriefing: true}
	canWaitAged := &todoAsk{Outcome: outcomeDecide, Verdict: "Choose option", AskedAt: now.Add(-30 * time.Hour), ForBriefing: true}
	plainTodo := Todo{ID: "todo_cw", Title: "Can wait task", CreatedAt: now.Add(-40 * time.Hour), UpdatedAt: now.Add(-6 * time.Hour)}
	if rule := snapshotDoneRuleWithConfig(plainTodo, nil, canWaitFresh, nil, nil, now, time.UTC, cfgV2); rule == ruleOwnerDecides {
		t.Fatalf("todos_v2=true can_wait within 24h SLA must not escalate to %q", ruleOwnerDecides)
	}
	if rule := snapshotDoneRuleWithConfig(plainTodo, nil, canWaitAged, nil, nil, now, time.UTC, cfgV2); rule != ruleOwnerDecides {
		t.Fatalf("todos_v2=true can_wait past 24h SLA: want %q, got %q", ruleOwnerDecides, rule)
	}
	// Legacy ignores ForBriefing and immediately treats outcomeDecide as ruleOwnerDecides.
	if rule := snapshotDoneRuleWithConfig(plainTodo, nil, canWaitFresh, nil, nil, now, time.UTC, cfgLegacy); rule != ruleOwnerDecides {
		t.Fatalf("todos_v2=false can_wait should preserve legacy %q, got %q", ruleOwnerDecides, rule)
	}
}

func TestTodosV2ChildSummaryExcludesSomedayAndDeferredAttention(t *testing.T) {
	items := []listedTodo{
		{Todo: Todo{ID: "parent"}},
		{Todo: Todo{ID: "immediate", PartOf: "parent"}},
		{Todo: Todo{ID: "quiet", PartOf: "parent", Quiet: true}},
		{Todo: Todo{ID: "deferred", PartOf: "parent"}},
		{Todo: Todo{ID: "someday", PartOf: "parent"}},
		{Todo: Todo{ID: "closed", PartOf: "parent"}},
	}
	views := []assistantwire.TodoSummary{
		{Phase: assistantwire.TodoPhaseNeedsYou, Section: assistantwire.TodoListSectionNeedsYou},
		{Phase: assistantwire.TodoPhaseNeedsYou, Section: assistantwire.TodoListSectionNeedsYou},
		{Phase: assistantwire.TodoPhaseNeedsYou, Section: assistantwire.TodoListSectionNeedsYou},
		{Phase: assistantwire.TodoPhaseNeedsYou, Section: assistantwire.TodoListSectionCanWait, CanWait: true},
		{Phase: assistantwire.TodoPhaseSomeday, Section: assistantwire.TodoListSectionSomeday},
		{Phase: assistantwire.TodoPhaseDone, Section: assistantwire.TodoListSectionJustFinished},
	}
	populateTodoChildSummaries(items, views)
	want := todoChildSummary{Total: 5, Open: 3, NeedsYou: 1, Done: 1}
	if got := items[0].ChildSummary; got == nil || *got != want {
		t.Fatalf("child_summary = %+v, want %+v", got, want)
	}
}

func TestTodosV2ProjectionUnifyParentChildSummary(t *testing.T) {
	st := newMemBlockerStore()
	s, f := newQuietServiceOn(t, st)
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

	setProjectionUnifySettings(t, s, true, intPtr(24), intPtr(48))

	parent, err := s.createTodo(ctx, "Parent goal", "", "conv_main", "")
	if err != nil {
		t.Fatal(err)
	}
	childNeedsYou, err := s.createTodo(ctx, "Child waiting on user", "", "conv_main", "")
	if err != nil {
		t.Fatal(err)
	}
	childWorking, err := s.createTodo(ctx, "Child running", "", "conv_main", "")
	if err != nil {
		t.Fatal(err)
	}
	childOpen, err := s.createTodo(ctx, "Child open plain", "", "conv_main", "")
	if err != nil {
		t.Fatal(err)
	}
	childDone, err := s.createTodo(ctx, "Child completed", "", "conv_main", "")
	if err != nil {
		t.Fatal(err)
	}
	childCancelled, err := s.createTodo(ctx, "Child cancelled", "", "conv_main", "")
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := s.createTodo(ctx, "Grandchild under open child", "", "conv_main", "")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := s.createTodo(ctx, "Standalone leaf", "", "conv_main", "")
	if err != nil {
		t.Fatal(err)
	}

	// Wire parent-child relationships and distinct child states.
	doneAt := now.Add(-1 * time.Hour)
	s.todos.mu.Lock()
	for _, id := range []string{childNeedsYou.ID, childWorking.ID, childOpen.ID, childDone.ID, childCancelled.ID} {
		s.todos.todos[id].PartOf = parent.ID
	}
	s.todos.todos[grandchild.ID].PartOf = childOpen.ID

	// childNeedsYou: supervision needs_user
	s.todos.todos[childNeedsYou.ID].Supervision = &Supervision{
		Supervisor: "conv_main",
		State:      reviewNeedsUser,
		UpdatedAt:  now.Add(-30 * time.Minute),
		Reviews:    []TaskReview{{At: now.Add(-30 * time.Minute), Verdict: reviewNeedsUser, Reason: "Approve design"}},
	}
	// childWorking: active working agent
	s.todos.todos[childWorking.ID].Agents = []TodoAgent{{MachineID: "mac1", SessionID: "sess_work", LinkedAt: now.Add(-20 * time.Minute)}}
	// childDone: completed
	s.todos.todos[childDone.ID].DoneAt = timePtr(doneAt)
	s.todos.todos[childDone.ID].DoneBy = db.DoneByReview
	// childCancelled: cancelled
	s.todos.todos[childCancelled.ID].DoneAt = timePtr(doneAt)
	s.todos.todos[childCancelled.ID].DoneBy = db.DoneByCancelled
	s.todos.todos[childCancelled.ID].CancelledAt = timePtr(doneAt)
	s.todos.mu.Unlock()

	f.sessions["mac1"] = []proto.Session{{
		MachineID: "mac1",
		SessionID: "sess_work",
		State:     proto.StateWorking,
		UpdatedAt: now.Add(-5 * time.Minute),
	}}

	list, err := s.listedTodos(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.viewed(ctx, list, now)

	byID := map[string]listedTodo{}
	for _, item := range list {
		byID[item.ID] = item
	}

	gotParent := byID[parent.ID]
	if gotParent.DoneAt != nil {
		t.Fatalf("parent goal must remain open (never auto-closed), got DoneAt=%v", gotParent.DoneAt)
	}
	if gotParent.ChildSummary == nil {
		t.Fatalf("expected non-nil child_summary on parent goal under todos_v2=true")
	}
	wantParent := todoChildSummary{Total: 5, Open: 3, NeedsYou: 1, Working: 1, Done: 2}
	if *gotParent.ChildSummary != wantParent {
		t.Fatalf("parent child_summary: want %+v, got %+v", wantParent, *gotParent.ChildSummary)
	}

	// childOpen has 1 direct grandchild -> gets its own child_summary (and grandchild is NOT double-counted on parent).
	gotChildOpen := byID[childOpen.ID]
	if gotChildOpen.ChildSummary == nil || *gotChildOpen.ChildSummary != (todoChildSummary{Total: 1, Open: 1}) {
		t.Fatalf("childOpen child_summary: want {Total:1 Open:1}, got %+v", gotChildOpen.ChildSummary)
	}

	// Leaf to-do omits child_summary in Go struct and JSON.
	gotLeaf := byID[leaf.ID]
	if gotLeaf.ChildSummary != nil {
		t.Fatalf("leaf todo must not have child_summary, got %+v", gotLeaf.ChildSummary)
	}
	rawLeaf, err := json.Marshal(gotLeaf)
	if err != nil {
		t.Fatal(err)
	}
	var leafMap map[string]any
	if err := json.Unmarshal(rawLeaf, &leafMap); err != nil {
		t.Fatal(err)
	}
	if _, has := leafMap["child_summary"]; has {
		t.Fatalf("leaf JSON must omit child_summary: %s", string(rawLeaf))
	}

	// Even if ALL direct children of a parent are done, parent is NEVER auto-closed.
	allDoneParent, err := s.createTodo(ctx, "All-done children parent", "", "conv_main", "")
	if err != nil {
		t.Fatal(err)
	}
	onlyChild, err := s.createTodo(ctx, "Only done child", "", "conv_main", "")
	if err != nil {
		t.Fatal(err)
	}
	s.todos.mu.Lock()
	s.todos.todos[onlyChild.ID].PartOf = allDoneParent.ID
	s.todos.todos[onlyChild.ID].DoneAt = timePtr(doneAt)
	s.todos.todos[onlyChild.ID].DoneBy = db.DoneByReview
	s.todos.mu.Unlock()

	list2, err := s.listedTodos(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.viewed(ctx, list2, now)
	for _, item := range list2 {
		if item.ID == allDoneParent.ID {
			if item.DoneAt != nil {
				t.Fatalf("parent with all children done must NOT be auto-closed")
			}
			if item.ChildSummary == nil || *item.ChildSummary != (todoChildSummary{Total: 1, Open: 0, Done: 1}) {
				t.Fatalf("allDoneParent child_summary: want {Total:1 Done:1}, got %+v", item.ChildSummary)
			}
		}
	}

	// todos_v2=false: child_summary must remain nil and omitted from JSON for parents too.
	setProjectionUnifySettings(t, s, false, intPtr(24), intPtr(48))
	listOff, err := s.listedTodos(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.viewed(ctx, listOff, now)
	for _, item := range listOff {
		if item.ChildSummary != nil {
			t.Fatalf("todos_v2=false: item %s must have nil child_summary, got %+v", item.ID, item.ChildSummary)
		}
		raw, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if _, has := m["child_summary"]; has {
			t.Fatalf("todos_v2=false: item %s JSON must omit child_summary", item.ID)
		}
	}
}
