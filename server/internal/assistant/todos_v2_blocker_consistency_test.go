package assistant

import (
	"context"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/mergetrain"
	"github.com/nwverse/persona/server/internal/db"
)

type countingBlockerQueueStore struct {
	*memBlockerQueueStore
	sweeps atomic.Int32
}

func newCountingBlockerQueueStore() *countingBlockerQueueStore {
	return &countingBlockerQueueStore{
		memBlockerQueueStore: newMemBlockerQueueStore(),
	}
}

func (c *countingBlockerQueueStore) TaskPlansQueued(ctx context.Context, now time.Time) ([]db.TaskPlanRow, error) {
	c.sweeps.Add(1)
	return c.memBlockerQueueStore.TaskPlansQueued(ctx, now)
}

func TestTodosV2BlockerCancelMergeTrainSyncsCoordinator(t *testing.T) {
	st := newMemBlockerStore()
	s, _ := newQuietServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	setBlockersTodosV2Flag(t, s, true, false)

	ctx := context.Background()
	now := time.Now().UTC()
	prURL1 := "https://github.com/example/repo/pull/101"
	prURL2 := "https://github.com/example/repo/pull/102"
	repoKey := "github.com/example/repo"

	task1 := newMergeTrainTask(t, s, "Conflicting PR 1", prURL1)
	task2 := newMergeTrainTask(t, s, "Queued PR 2", prURL2)

	s.onPRStatusMergeTrain(ctx, task1, TodoPRStatus{
		URL: prURL1, State: "open", HeadOID: "sha-1", Conflicts: true, CheckedAt: now,
	})
	s.onPRStatusMergeTrain(ctx, task2, TodoPRStatus{
		URL: prURL2, State: "open", HeadOID: "sha-t2", Checks: "pending", CheckedAt: now,
	})
	blockerID := mergeTrainBlockerID(prURL1)

	// Cancel the merge_train blocker via the HTTP DELETE endpoint.
	w := doBlockerHTTP(s, http.MethodDelete, "/api/v1/todos/"+task1.ID+"/blockers/"+blockerID, "full", "")
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE merge_train blocker: status=%d body=%s", w.Code, w.Body.String())
	}

	// Verify DB row is cancelled.
	rows, err := st.ListTodoBlockersByTodo(ctx, task1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].State != db.TodoBlockerStateCancelled {
		t.Fatalf("expected cancelled merge_train blocker row, got %+v", rows)
	}

	repo, _ := findRepoSnapshot(s.mergeTrainSnapshot(ctx), repoKey)
	if repo.Head == nil || repo.Head.PRURL != prURL2 || len(repo.Queue) != 0 {
		t.Fatalf("cancel did not remove exactly the affected PR and promote the follower: %+v", repo)
	}

	// Cancel task2 via merged observation so repo head is free, then verify a subsequent clean
	// observation for task1 is NOT stuck in StateEscalated and evaluates to ActionRebaseAndMerge.
	_ = s.onPRStatusMergeTrain(ctx, task2, TodoPRStatus{
		URL:       prURL2,
		State:     "merged",
		HeadOID:   "sha-t2",
		CheckedAt: now.Add(4 * time.Second),
	})
	cleanOut := s.onPRStatusMergeTrain(ctx, task1, TodoPRStatus{
		URL:       prURL1,
		State:     "open",
		HeadOID:   "sha-3",
		Review:    "approved",
		Checks:    "success",
		Conflicts: false,
		CheckedAt: now.Add(5 * time.Second),
	})
	if cleanOut.Decision.State == mergetrain.StateEscalated {
		t.Fatalf("expected clean observation after cancel not to stay escalated, got %+v", cleanOut)
	}
	if cleanOut.Decision.Action != mergetrain.ActionRebaseAndMerge {
		t.Fatalf("expected ActionRebaseAndMerge on clean observation after cancel, got %+v", cleanOut)
	}
}

func TestTodosV2BlockerExternalPRCaseInsensitiveFastPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"HTTPS://GitHub.COM/Owner/Repo/pull/42/", "https://github.com/Owner/Repo/pull/42"},
		{"  http://GitLab.Example.COM:8443/Group/Proj/-/merge_requests/7/ ", "http://gitlab.example.com:8443/Group/Proj/-/merge_requests/7"},
		{"https://github.com/example/repo/pull/101", "https://github.com/example/repo/pull/101"},
		{"todo_abc", "todo_abc"},
		{"github.com/example/repo", "github.com/example/repo"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeTodoBlockerPRRef(tc.in); got != tc.want {
			t.Errorf("normalizeTodoBlockerPRRef(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Mixed-case external_pr URLs produce the same deterministic blocker ID and deduplicate on AddTodoBlocker.
	idMixed := todoBlockerID("todo_1", todoBlockerKindExternalPR, "https://github.com/Owner/Repo/pull/42")
	idLower := todoBlockerID("todo_1", todoBlockerKindExternalPR, "https://github.com/owner/repo/pull/42")
	idUpperHost := todoBlockerID("todo_1", todoBlockerKindExternalPR, "HTTPS://GitHub.COM/Owner/Repo/pull/42/")
	if idMixed != idLower || idMixed != idUpperHost {
		t.Fatalf("expected deterministic case-insensitive external_pr IDs, got mixed=%s lower=%s upperHost=%s", idMixed, idLower, idUpperHost)
	}

	st := newMemBlockerStore()
	s, _ := newQuietServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	setBlockersTodosV2Flag(t, s, true, false)

	ctx := context.Background()
	task, err := s.createTodo(ctx, "Downstream task", "Wait for mixed-case external PR", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a legacy/historical mixed-case external_pr blocker row persisted in storage.
	legacyURL := "https://github.com/Owner/Repo/pull/42"
	if _, err := st.UpsertTodoBlocker(ctx, db.TodoBlockerRow{
		ID:        "blk_legacy_mixed",
		TodoID:    task.ID,
		Kind:      todoBlockerKindExternalPR,
		TargetRef: legacyURL,
		State:     db.TodoBlockerStateActive,
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var unblocked []string
	s.setTodoBlockerUnblockedHook(func(_ context.Context, todoID string) {
		mu.Lock()
		unblocked = append(unblocked, todoID)
		mu.Unlock()
	})

	// Event-driven fast path arrives with lowercase PR URL.
	sum, err := s.reconcileTodoBlockersForPR(ctx, TodoPRStatus{
		URL:   "https://github.com/owner/repo/pull/42",
		State: "merged",
	})
	if err != nil {
		t.Fatalf("reconcileTodoBlockersForPR: %v", err)
	}
	if !reflect.DeepEqual(sum.ResolvedIDs, []string{"blk_legacy_mixed"}) {
		t.Fatalf("expected legacy mixed-case blocker to resolve, got %+v", sum)
	}
	if !reflect.DeepEqual(sum.UnblockedTodoIDs, []string{task.ID}) {
		t.Fatalf("expected task to unblock on fast path, got %+v", sum)
	}
	mu.Lock()
	gotHook := slices.Clone(unblocked)
	mu.Unlock()
	if !reflect.DeepEqual(gotHook, []string{task.ID}) {
		t.Fatalf("expected unblocked hook for %s, got %v", task.ID, gotHook)
	}
}

func TestTodosV2BlockerBatchUnblockDeduplicatesSweep(t *testing.T) {
	st := newCountingBlockerQueueStore()
	s, _ := newQuietServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	s.briefsOptional = true
	setBlockersTodosV2Flag(t, s, true, false)

	ctx := context.Background()
	sharedPR := "https://github.com/example/repo/pull/77"

	// Measure the exact number of TaskPlansQueued calls performed by one sweepAdmissionQueue pass.
	st.sweeps.Store(0)
	s.sweepAdmissionQueue(ctx)
	perSweep := st.sweeps.Load()
	if perSweep <= 0 {
		t.Fatalf("expected one sweepAdmissionQueue pass to query TaskPlansQueued, got %d", perSweep)
	}

	// Avoid shelling out to gh CLI during the open-PR observation below.
	s.setTodoBlockerPRFetcher(func(_ context.Context, _ string) (todoBlockerPRFact, bool) {
		return todoBlockerPRFact{}, false
	})

	var todoIDs []string
	for _, title := range []string{"Batch A", "Batch B", "Batch C"} {
		td, err := s.createTodo(ctx, title, "Blocked on shared PR", "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		todoIDs = append(todoIDs, td.ID)
		if _, err := s.AddTodoBlocker(ctx, td.ID, todoBlockerKindExternalPR, sharedPR, ""); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(todoIDs)

	var mu sync.Mutex
	var hooked []string
	s.setTodoBlockerUnblockedHook(func(_ context.Context, todoID string) {
		mu.Lock()
		hooked = append(hooked, todoID)
		mu.Unlock()
	})

	// Open PR observation resolves nothing -> zero sweeps.
	st.sweeps.Store(0)
	openSum, err := s.reconcileTodoBlockersForPR(ctx, TodoPRStatus{URL: sharedPR, State: "open"})
	if err != nil {
		t.Fatalf("reconcile open PR: %v", err)
	}
	if len(openSum.UnblockedTodoIDs) != 0 || st.sweeps.Load() != 0 {
		t.Fatalf("expected 0 unblocked and 0 sweeps for open PR, got sum=%+v sweeps=%d", openSum, st.sweeps.Load())
	}

	// Merged PR observation unblocks all 3 to-dos in one pass -> 3 hooks fired, exactly 1 sweep.
	st.sweeps.Store(0)
	mergedSum, err := s.reconcileTodoBlockersForPR(ctx, TodoPRStatus{URL: sharedPR, State: "merged"})
	if err != nil {
		t.Fatalf("reconcile merged PR: %v", err)
	}
	if !reflect.DeepEqual(mergedSum.UnblockedTodoIDs, todoIDs) {
		t.Fatalf("UnblockedTodoIDs = %v, want %v", mergedSum.UnblockedTodoIDs, todoIDs)
	}
	mu.Lock()
	gotHooked := slices.Clone(hooked)
	mu.Unlock()
	slices.Sort(gotHooked)
	if !reflect.DeepEqual(gotHooked, todoIDs) {
		t.Fatalf("unblocked hooks = %v, want %v", gotHooked, todoIDs)
	}
	if gotSweeps := st.sweeps.Load(); gotSweeps != perSweep {
		t.Fatalf("expected exactly 1 admission queue sweep (%d queries) for batch unblock of %d todos, got %d",
			perSweep, len(todoIDs), gotSweeps)
	}

	// Single-item DELETE cancellation still triggers an immediate admission queue sweep once upon unblock.
	singleTodo, err := s.createTodo(ctx, "Single delete unblock", "Blocked on PR", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.AddTodoBlocker(ctx, singleTodo.ID, todoBlockerKindExternalPR, "https://github.com/example/repo/pull/88", "")
	if err != nil || row == nil {
		t.Fatalf("AddTodoBlocker: row=%+v err=%v", row, err)
	}
	st.sweeps.Store(0)
	w := doBlockerHTTP(s, http.MethodDelete, "/api/v1/todos/"+singleTodo.ID+"/blockers/"+row.ID, "full", "")
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE single blocker: status=%d body=%s", w.Code, w.Body.String())
	}
	if gotSweeps := st.sweeps.Load(); gotSweeps != perSweep {
		t.Fatalf("expected 1 immediate sweep (%d queries) on single DELETE unblock, got %d", perSweep, gotSweeps)
	}
}

func TestTodosV2BlockerConsistencyFlagOffNoSideEffects(t *testing.T) {
	st := newCountingBlockerQueueStore()
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	s.briefsOptional = true

	ctx := context.Background()
	if s.settings(ctx).todosV2On() {
		t.Fatal("expected todos_v2 default false")
	}
	td, err := s.createTodo(ctx, "Flag off task", "No side effects", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}

	w := doBlockerHTTP(s, http.MethodDelete, "/api/v1/todos/"+td.ID+"/blockers/blk_any", "full", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 when todos_v2=false, got %d body=%s", w.Code, w.Body.String())
	}
	sum, err := s.reconcileTodoBlockersForPR(ctx, TodoPRStatus{
		URL:   "https://github.com/example/repo/pull/42",
		State: "merged",
	})
	if err != nil || sum.Evaluated != 0 || len(sum.UnblockedTodoIDs) != 0 {
		t.Fatalf("expected zero-op reconcile when todos_v2=false, got sum=%+v err=%v", sum, err)
	}
	if got := st.sweeps.Load(); got != 0 {
		t.Fatalf("expected 0 sweeps when todos_v2=false, got %d", got)
	}
}
