package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// memBlockerStore embeds memStore and implements todoBlockerStore in memory so unit tests
// can exercise persistence and Service restarts cleanly without external dependencies.
type memBlockerStore struct {
	memStore
	bmu      sync.Mutex
	blockers map[string]db.TodoBlockerRow
}

func newMemBlockerStore() *memBlockerStore {
	return &memBlockerStore{
		blockers: make(map[string]db.TodoBlockerRow),
	}
}

func (m *memBlockerStore) UpsertTodoBlocker(_ context.Context, r db.TodoBlockerRow) (*db.TodoBlockerRow, error) {
	m.bmu.Lock()
	defer m.bmu.Unlock()
	return m.putBlockerLocked(r), nil
}

func (m *memBlockerStore) EnsureTodoBlocker(_ context.Context, r db.TodoBlockerRow) (*db.TodoBlockerRow, error) {
	m.bmu.Lock()
	defer m.bmu.Unlock()
	if _, found := m.blockers[r.ID]; found {
		return nil, nil
	}
	return m.putBlockerLocked(r), nil
}

func (m *memBlockerStore) putBlockerLocked(r db.TodoBlockerRow) *db.TodoBlockerRow {
	if r.ConditionJSON == "" {
		r.ConditionJSON = "{}"
	}
	if r.State == "" {
		r.State = db.TodoBlockerStateActive
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = r.CreatedAt
	}
	m.blockers[r.ID] = r
	return &r
}

func (m *memBlockerStore) listFiltered(keep func(db.TodoBlockerRow) bool) []db.TodoBlockerRow {
	m.bmu.Lock()
	defer m.bmu.Unlock()
	var out []db.TodoBlockerRow
	for _, r := range m.blockers {
		if keep(r) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b db.TodoBlockerRow) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}

func (m *memBlockerStore) ListActiveTodoBlockers(_ context.Context) ([]db.TodoBlockerRow, error) {
	return m.listFiltered(func(r db.TodoBlockerRow) bool { return r.State == db.TodoBlockerStateActive }), nil
}

func (m *memBlockerStore) ListTodoReadinessBlockers(_ context.Context, todoIDs []string) ([]db.TodoBlockerRow, error) {
	return m.listFiltered(func(r db.TodoBlockerRow) bool {
		return slices.Contains(todoIDs, r.TodoID) && (r.State == db.TodoBlockerStateActive || r.Kind == todoBlockerKindTodoStage || r.Kind == todoBlockerKindExternalPR)
	}), nil
}

func (m *memBlockerStore) ListMergeTrainPRBlockers(_ context.Context, prURL, repoKey string) ([]db.TodoBlockerRow, error) {
	return m.listFiltered(func(r db.TodoBlockerRow) bool {
		return r.Kind == todoBlockerKindMergeTrain && (strings.EqualFold(r.TargetRef, prURL) || strings.EqualFold(r.TargetRef, repoKey))
	}), nil
}

func (m *memBlockerStore) UpdateActiveTodoBlockerCondition(_ context.Context, id, condition string, at time.Time) (*db.TodoBlockerRow, error) {
	m.bmu.Lock()
	defer m.bmu.Unlock()
	row, found := m.blockers[id]
	if !found || row.State != db.TodoBlockerStateActive {
		return nil, nil
	}
	row.ConditionJSON, row.UpdatedAt = condition, at
	m.blockers[id] = row
	return &row, nil
}

func (m *memBlockerStore) ListTodoBlockersByTodo(_ context.Context, todoID string) ([]db.TodoBlockerRow, error) {
	return m.listFiltered(func(r db.TodoBlockerRow) bool { return r.TodoID == todoID }), nil
}

func (m *memBlockerStore) ListActiveTodoBlockersByTarget(_ context.Context, targetRef string) ([]db.TodoBlockerRow, error) {
	want := strings.TrimSpace(targetRef)
	return m.listFiltered(func(r db.TodoBlockerRow) bool {
		return r.State == db.TodoBlockerStateActive && strings.EqualFold(r.TargetRef, want)
	}), nil
}

func (m *memBlockerStore) ResolveTodoBlocker(_ context.Context, id string, at time.Time) (*db.TodoBlockerRow, error) {
	m.bmu.Lock()
	defer m.bmu.Unlock()
	r, ok := m.blockers[id]
	if !ok || r.State != db.TodoBlockerStateActive {
		return nil, nil
	}
	r.State = db.TodoBlockerStateResolved
	r.UpdatedAt = at
	if r.ResolvedAt == nil {
		t := at
		r.ResolvedAt = &t
	}
	m.blockers[id] = r
	cp := r
	return &cp, nil
}

func (m *memBlockerStore) CancelTodoBlocker(_ context.Context, id string, at time.Time) (*db.TodoBlockerRow, error) {
	m.bmu.Lock()
	defer m.bmu.Unlock()
	r, ok := m.blockers[id]
	if !ok || r.State != db.TodoBlockerStateActive {
		return nil, nil
	}
	r.State = db.TodoBlockerStateCancelled
	r.UpdatedAt = at
	if r.ResolvedAt == nil {
		t := at
		r.ResolvedAt = &t
	}
	m.blockers[id] = r
	cp := r
	return &cp, nil
}

func (m *memBlockerStore) IncrementTodoBlockerAttempts(_ context.Context, id string, at time.Time) (*db.TodoBlockerRow, error) {
	m.bmu.Lock()
	defer m.bmu.Unlock()
	r, ok := m.blockers[id]
	if !ok || r.State != db.TodoBlockerStateActive {
		return nil, nil
	}
	r.Attempts++
	r.UpdatedAt = at
	m.blockers[id] = r
	cp := r
	return &cp, nil
}

func setBlockersTodosV2Flag(t *testing.T, s *Service, want, expected bool) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"todos_v2": want, "expected_todos_v2": expected})
	if err != nil {
		t.Fatal(err)
	}
	w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", s.managerIdentity(), "full", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT todos_v2=%v: code=%d body=%s", want, w.Code, w.Body.String())
	}
}

func TestTodosV2BlockersBypassWhenFlagOff(t *testing.T) {
	st := newMemBlockerStore()
	s, _ := newQuietServiceOn(t, st)
	ctx := context.Background()

	if s.settings(ctx).todosV2On() {
		t.Fatal("expected todos_v2 default to be false")
	}

	task, err := s.createTodo(ctx, "Wait on external PR", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}

	// addTodoBlocker must bypass with zero DB writes when todos_v2=false.
	row, err := s.addTodoBlocker(ctx, db.TodoBlockerRow{
		ID:        "blk_off_1",
		TodoID:    task.ID,
		Kind:      todoBlockerKindExternalPR,
		TargetRef: "https://github.com/example/repo/pull/50",
	})
	if err != nil || row != nil {
		t.Fatalf("todos_v2=false addTodoBlocker = (%+v, %v), want (nil, nil)", row, err)
	}
	if active, _ := st.ListActiveTodoBlockers(ctx); len(active) != 0 {
		t.Fatalf("todos_v2=false wrote blockers to store: %+v", active)
	}

	// Even if a blocker already exists in storage, reconcilePersistedTodoBlockers and sharedActiveTodoBlockerGroups must bypass.
	_, _ = st.UpsertTodoBlocker(ctx, db.TodoBlockerRow{
		ID:        "blk_preexisting",
		TodoID:    task.ID,
		Kind:      todoBlockerKindExternalPR,
		TargetRef: "https://github.com/example/repo/pull/50",
	})
	woke := false
	s.setTodoBlockerUnblockedHook(func(_ context.Context, _ string) { woke = true })

	summary, err := s.reconcilePersistedTodoBlockers(ctx, "")
	if err != nil {
		t.Fatalf("reconcilePersistedTodoBlockers off: %v", err)
	}
	if summary.Evaluated != 0 || len(summary.ResolvedIDs) != 0 || len(summary.UnblockedTodoIDs) != 0 || woke {
		t.Fatalf("todos_v2=false reconciled unexpectedly: summary=%+v woke=%v", summary, woke)
	}
	shared, err := s.sharedActiveTodoBlockerGroups(ctx)
	if err != nil || len(shared) != 0 {
		t.Fatalf("todos_v2=false sharedActiveTodoBlockerGroups = (%+v, %v), want empty", shared, err)
	}
}

func TestTodosV2BlockersExternalPRAutoWakeAndSharedAggregation(t *testing.T) {
	st := newMemBlockerStore()
	s, _ := newQuietServiceOn(t, st)
	setBlockersTodosV2Flag(t, s, true, false)
	ctx := context.Background()

	prereq, err := s.createTodo(ctx, "Upstream fix", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	todoA, err := s.createTodo(ctx, "Resume after PR merge", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	todoB, err := s.createTodo(ctx, "Resume after PR merge and prereq", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}

	prURL := "https://github.com/example/repo/pull/777"
	if _, err := s.addTodoBlocker(ctx, db.TodoBlockerRow{
		ID:        "blk_a_pr",
		TodoID:    todoA.ID,
		Kind:      todoBlockerKindExternalPR,
		TargetRef: prURL,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.addTodoBlocker(ctx, db.TodoBlockerRow{
		ID:        "blk_b_pr",
		TodoID:    todoB.ID,
		Kind:      todoBlockerKindExternalPR,
		TargetRef: prURL,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.addTodoBlocker(ctx, db.TodoBlockerRow{
		ID:        "blk_b_dep",
		TodoID:    todoB.ID,
		Kind:      todoBlockerKindTodoDone,
		TargetRef: prereq.ID,
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var wokeIDs []string
	s.setTodoBlockerUnblockedHook(func(_ context.Context, todoID string) {
		mu.Lock()
		wokeIDs = append(wokeIDs, todoID)
		mu.Unlock()
	})

	// While PR is open, nothing resolves and both todos aggregate under prURL.
	sum1, err := s.reconcilePersistedTodoBlockers(ctx, "")
	if err != nil {
		t.Fatalf("reconcile open: %v", err)
	}
	if len(sum1.ResolvedIDs) != 0 || len(sum1.UnblockedTodoIDs) != 0 {
		t.Fatalf("expected nothing resolved while PR open, got %+v", sum1)
	}
	if len(sum1.SharedGroups) != 1 || sum1.SharedGroups[0].TargetRef != prURL || sum1.SharedGroups[0].Count != 2 {
		t.Fatalf("expected shared blocker group on %s with count 2, got %+v", prURL, sum1.SharedGroups)
	}

	// A targeted observation resolves both PR edges and wakes only the ready to-do.
	sum2, err := s.reconcileTodoBlockersForPR(ctx, TodoPRStatus{URL: prURL, State: "merged"})
	if err != nil {
		t.Fatalf("reconcile merged PR: %v", err)
	}
	if !reflect.DeepEqual(sum2.ResolvedIDs, []string{"blk_a_pr", "blk_b_pr"}) {
		t.Fatalf("ResolvedIDs = %v, want [blk_a_pr blk_b_pr]", sum2.ResolvedIDs)
	}
	if !reflect.DeepEqual(sum2.UnblockedTodoIDs, []string{todoA.ID}) {
		t.Fatalf("UnblockedTodoIDs = %v, want [%s]", sum2.UnblockedTodoIDs, todoA.ID)
	}
	mu.Lock()
	gotWoke := append([]string(nil), wokeIDs...)
	mu.Unlock()
	if !reflect.DeepEqual(gotWoke, []string{todoA.ID}) {
		t.Fatalf("wokeIDs = %v, want [%s]", gotWoke, todoA.ID)
	}

	// Once prereq finishes, full sweep resolves blk_b_dep and wakes todoB.
	doneNow := time.Now().UTC()
	s.todos.mu.Lock()
	s.todos.todos[prereq.ID].DoneAt = &doneNow
	s.todos.mu.Unlock()
	sum3, err := s.reconcilePersistedTodoBlockers(ctx, "")
	if err != nil {
		t.Fatalf("reconcile prereq done: %v", err)
	}
	if !reflect.DeepEqual(sum3.ResolvedIDs, []string{"blk_b_dep"}) {
		t.Fatalf("ResolvedIDs = %v, want [blk_b_dep]", sum3.ResolvedIDs)
	}
	if !reflect.DeepEqual(sum3.UnblockedTodoIDs, []string{todoB.ID}) {
		t.Fatalf("UnblockedTodoIDs = %v, want [%s]", sum3.UnblockedTodoIDs, todoB.ID)
	}
}

func TestTodosV2BlockersSurviveServiceRestartWithAttempts(t *testing.T) {
	st := newMemBlockerStore()
	s1, _ := newQuietServiceOn(t, st)
	setBlockersTodosV2Flag(t, s1, true, false)
	ctx := context.Background()

	headTask, err := s1.createTodo(ctx, "Merge queue head", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	waitTask, err := s1.createTodo(ctx, "Queued behind head", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s1.addTodoBlocker(ctx, db.TodoBlockerRow{
		ID:            "blk_mt_1",
		TodoID:        waitTask.ID,
		Kind:          todoBlockerKindMergeTrain,
		TargetRef:     headTask.ID,
		ConditionJSON: `{"max_attempts":2}`,
	}); err != nil {
		t.Fatal(err)
	}
	inc1, err := s1.incrementTodoBlockerAttempts(ctx, "blk_mt_1")
	if err != nil || inc1 == nil || inc1.Attempts != 1 {
		t.Fatalf("first increment = (%+v, %v), want attempts=1", inc1, err)
	}

	// Simulate full server restart: new Service instance backed by the same persistent store.
	s2, _ := newQuietServiceOn(t, st)
	if !s2.settings(ctx).todosV2On() {
		setBlockersTodosV2Flag(t, s2, true, false)
	}

	// Before reaching threshold, blocker remains active and unescalated.
	sumBefore, err := s2.reconcilePersistedTodoBlockers(ctx, "")
	if err != nil {
		t.Fatalf("reconcile after restart (attempt 1): %v", err)
	}
	if sumBefore.Evaluated != 1 || len(sumBefore.ResolvedIDs) != 0 || len(sumBefore.EscalatedIDs) != 0 {
		t.Fatalf("unexpected reconcile at attempt 1 after restart: %+v", sumBefore)
	}

	// Second attempt trips the circuit breaker (attempts == 2) without losing prior count.
	inc2, err := s2.incrementTodoBlockerAttempts(ctx, "blk_mt_1")
	if err != nil || inc2 == nil || inc2.Attempts != 2 {
		t.Fatalf("second increment after restart = (%+v, %v), want attempts=2", inc2, err)
	}
	sumEsc, err := s2.reconcilePersistedTodoBlockers(ctx, "")
	if err != nil {
		t.Fatalf("reconcile after restart (attempt 2): %v", err)
	}
	if !reflect.DeepEqual(sumEsc.EscalatedIDs, []string{"blk_mt_1"}) {
		t.Fatalf("expected blk_mt_1 escalated after 2 persistent attempts, got %+v", sumEsc)
	}
}
