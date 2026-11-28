package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

// memBlockerQueueStore combines profileTaskStore (task plans + TaskPlansQueued) with
// memBlockerStore (todoBlockerStore) so sweepAdmissionQueue and blocker persistence work in memory.
type memBlockerQueueStore struct {
	*profileTaskStore
	b *memBlockerStore
}

func newMemBlockerQueueStore() *memBlockerQueueStore {
	return &memBlockerQueueStore{
		profileTaskStore: &profileTaskStore{
			memStore: &memStore{},
			plans:    make(map[string]db.TaskPlanRow),
		},
		b: newMemBlockerStore(),
	}
}

func (m *memBlockerQueueStore) TaskPlansQueued(_ context.Context, _ time.Time) ([]db.TaskPlanRow, error) {
	m.planMu.Lock()
	defer m.planMu.Unlock()
	out := make([]db.TaskPlanRow, 0, len(m.plans))
	for _, row := range m.plans {
		out = append(out, row)
	}
	return out, nil
}

func (m *memBlockerQueueStore) TaskPlansQueuedForTodo(_ context.Context, todoID string) ([]db.TaskPlanRow, error) {
	m.planMu.Lock()
	defer m.planMu.Unlock()
	var rows []db.TaskPlanRow
	for _, row := range m.plans {
		if row.State != planStarted {
			continue
		}
		for _, task := range planFromRow(row).Tasks {
			if task.TodoID == todoID && task.Queue != nil {
				rows = append(rows, row)
				break
			}
		}
	}
	return rows, nil
}

func (m *memBlockerQueueStore) UpsertTodoBlocker(ctx context.Context, r db.TodoBlockerRow) (*db.TodoBlockerRow, error) {
	return m.b.UpsertTodoBlocker(ctx, r)
}
func (m *memBlockerQueueStore) EnsureTodoBlocker(ctx context.Context, r db.TodoBlockerRow) (*db.TodoBlockerRow, error) {
	return m.b.EnsureTodoBlocker(ctx, r)
}
func (m *memBlockerQueueStore) ListActiveTodoBlockers(ctx context.Context) ([]db.TodoBlockerRow, error) {
	return m.b.ListActiveTodoBlockers(ctx)
}
func (m *memBlockerQueueStore) ListTodoReadinessBlockers(ctx context.Context, todoIDs []string) ([]db.TodoBlockerRow, error) {
	return m.b.ListTodoReadinessBlockers(ctx, todoIDs)
}
func (m *memBlockerQueueStore) ListMergeTrainPRBlockers(ctx context.Context, prURL, repoKey string) ([]db.TodoBlockerRow, error) {
	return m.b.ListMergeTrainPRBlockers(ctx, prURL, repoKey)
}
func (m *memBlockerQueueStore) UpdateActiveTodoBlockerCondition(ctx context.Context, id, condition string, at time.Time) (*db.TodoBlockerRow, error) {
	return m.b.UpdateActiveTodoBlockerCondition(ctx, id, condition, at)
}
func (m *memBlockerQueueStore) ListTodoBlockersByTodo(ctx context.Context, todoID string) ([]db.TodoBlockerRow, error) {
	return m.b.ListTodoBlockersByTodo(ctx, todoID)
}
func (m *memBlockerQueueStore) ListActiveTodoBlockersByTarget(ctx context.Context, targetRef string) ([]db.TodoBlockerRow, error) {
	return m.b.ListActiveTodoBlockersByTarget(ctx, targetRef)
}
func (m *memBlockerQueueStore) ResolveTodoBlocker(ctx context.Context, id string, at time.Time) (*db.TodoBlockerRow, error) {
	return m.b.ResolveTodoBlocker(ctx, id, at)
}
func (m *memBlockerQueueStore) CancelTodoBlocker(ctx context.Context, id string, at time.Time) (*db.TodoBlockerRow, error) {
	return m.b.CancelTodoBlocker(ctx, id, at)
}
func (m *memBlockerQueueStore) IncrementTodoBlockerAttempts(ctx context.Context, id string, at time.Time) (*db.TodoBlockerRow, error) {
	return m.b.IncrementTodoBlockerAttempts(ctx, id, at)
}

func newBlockerQueueTestEnv(t *testing.T, todosV2 bool) (*Service, *fakeHub, *memBlockerQueueStore) {
	t.Helper()
	st := newMemBlockerQueueStore()
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	s.briefsOptional = true
	if todosV2 {
		setBlockersTodosV2Flag(t, s, true, false)
	}
	return s, f, st
}

func createAndStartBlockedPlan(t *testing.T, s *Service, st *memBlockerQueueStore, prURL string) *TaskPlan {
	t.Helper()
	ctx := context.Background()
	planArgs, err := json.Marshal(map[string]any{
		"tasks": []map[string]any{
			{
				"title":         "Follow up after external PR",
				"prompt":        "Verify downstream changes after upstream PR merges",
				"role":          "worker",
				"readonly":      true,
				"machine":       "server",
				"cwd":           "/work/app",
				"blocked_by_pr": []string{prURL},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.toolTaskPlanFrom(ctx, "server", "conv1", planArgs); err != nil {
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
		t.Fatalf("loadPlan(%s): %v", planID, err)
	}
	if plan.State != planStarted {
		plan, err = s.startPlan(ctx, planID, nil, false)
		if err != nil {
			t.Fatalf("startPlan: %v", err)
		}
	}
	return plan
}

func waitForCondition(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	if cond() {
		return
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", desc)
		case <-ticker.C:
			if cond() {
				return
			}
		}
	}
}

func doBlockerHTTP(s *Service, method, path, role, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(assistantwire.RoleHeader, role)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	return w
}

func TestTodosV2BlockerQueueDefersStartAndAutoStartsOnPRMerge(t *testing.T) {
	s, f, st := newBlockerQueueTestEnv(t, true)
	ctx := context.Background()
	const prURL = "https://github.com/example/repo/pull/101"

	started := createAndStartBlockedPlan(t, s, st, prURL)
	if len(started.Tasks) != 1 || started.Tasks[0].TodoID == "" {
		t.Fatalf("expected 1 started plan task with todo_id, got %+v", started.Tasks)
	}
	task := started.Tasks[0]
	if task.Queue == nil || task.Queue.Key != blockerWaitQueueKey {
		t.Fatalf("expected task queued with key %q, got %+v", blockerWaitQueueKey, task.Queue)
	}
	if len(task.Queue.Blockers) != 1 || task.Queue.Blockers[0] != prURL {
		t.Fatalf("expected queue blockers [%s], got %+v", prURL, task.Queue.Blockers)
	}

	// While the external PR is still open, sweepAdmissionQueue must NOT launch the agent.
	s.sweepAdmissionQueue(ctx)
	if got := len(f.started()); got != 0 {
		t.Fatalf("agent started prematurely while blocker active: started=%d", got)
	}

	// Verify active blocker is persisted.
	rows, err := st.ListTodoBlockersByTodo(ctx, task.TodoID)
	if err != nil || len(rows) != 1 || rows[0].State != db.TodoBlockerStateActive {
		t.Fatalf("expected 1 active blocker, got rows=%+v err=%v", rows, err)
	}

	// Once the external PR merges, ReconcileTodoBlockers resolves the blocker and triggers sweepAdmissionQueue
	// which automatically launches the deferred agent.
	summary, err := s.reconcileTodoBlockersForPR(ctx, TodoPRStatus{URL: prURL, State: "merged"})
	if err != nil {
		t.Fatalf("ReconcileTodoBlockers: %v", err)
	}
	if len(summary.UnblockedTodoIDs) != 1 || summary.UnblockedTodoIDs[0] != task.TodoID {
		t.Fatalf("expected unblocked todo %s, got %+v", task.TodoID, summary.UnblockedTodoIDs)
	}

	waitForCondition(t, 3*time.Second, "deferred agent to start after PR merge", func() bool {
		return len(f.started()) == 1 && s.taskStarting.Load() == 0
	})

	// Queue marker is cleared once the agent starts.
	reloaded, err := s.loadPlan(ctx, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Tasks[0].Queue != nil {
		t.Fatalf("expected queue cleared after auto-start, got %+v", reloaded.Tasks[0].Queue)
	}
}

func TestTodosV2BlockerQueueAutoStartsOnManualBlockerCancel(t *testing.T) {
	s, f, st := newBlockerQueueTestEnv(t, true)
	ctx := context.Background()
	const prURL = "https://github.com/example/repo/pull/202"

	started := createAndStartBlockedPlan(t, s, st, prURL)
	todoID := started.Tasks[0].TodoID
	if started.Tasks[0].Queue == nil || started.Tasks[0].Queue.Key != blockerWaitQueueKey {
		t.Fatalf("expected task queued on blockerWaitQueueKey, got %+v", started.Tasks[0].Queue)
	}
	if len(f.started()) != 0 {
		t.Fatalf("expected 0 agent starts before cancel, got %d", len(f.started()))
	}

	rows, err := st.ListTodoBlockersByTodo(ctx, todoID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected 1 blocker row, got %+v err=%v", rows, err)
	}
	blockerID := rows[0].ID

	// Cancel blocker via DELETE /api/v1/todos/{id}/blockers/{blockerId}.
	w := doBlockerHTTP(s, http.MethodDelete, "/api/v1/todos/"+todoID+"/blockers/"+blockerID, "full", "")
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE blocker: code=%d body=%s", w.Code, w.Body.String())
	}

	waitForCondition(t, 3*time.Second, "deferred agent to start after manual blocker cancel", func() bool {
		return len(f.started()) == 1 && s.taskStarting.Load() == 0
	})
}

func TestTodosV2BlockerQueueBypassWhenFlagOff(t *testing.T) {
	s, f, st := newBlockerQueueTestEnv(t, false)
	ctx := context.Background()
	const prURL = "https://github.com/example/repo/pull/303"

	started := createAndStartBlockedPlan(t, s, st, prURL)
	if len(started.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %+v", started.Tasks)
	}
	if started.Tasks[0].Queue != nil {
		t.Fatalf("todos_v2=false must not queue blocked_by_pr task, got %+v", started.Tasks[0].Queue)
	}
	waitForCondition(t, 3*time.Second, "agent to start immediately when todos_v2=false", func() bool {
		return len(f.started()) == 1 && s.taskStarting.Load() == 0
	})
	rows, err := st.ListActiveTodoBlockers(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatalf("todos_v2=false must persist 0 blockers, got %+v err=%v", rows, err)
	}
}

func TestTodosV2AttentionPrefsExplicitSettingsTargetGuard(t *testing.T) {
	st := &memStore{}
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)

	payloads := []struct {
		name string
		body string
	}{
		{"auto_seen", `{"todos_v2_auto_seen":"subtasks"}`},
		{"pending_review_sla", `{"todos_v2_pending_review_sla_hours":12}`},
		{"can_wait_sla", `{"todos_v2_can_wait_sla_hours":24}`},
		{"fold_children", `{"todos_v2_fold_children":false}`},
	}

	for _, tc := range payloads {
		t.Run(tc.name, func(t *testing.T) {
			// Missing explicit manager header -> 400.
			wMissing := doTodoHTTP(s, http.MethodPut, "/api/v1/assistant", tc.body)
			if wMissing.Code != http.StatusBadRequest {
				t.Fatalf("%s without target header: got %d want %d body=%s", tc.name, wMissing.Code, http.StatusBadRequest, wMissing.Body.String())
			}
			// Wrong manager header -> 403.
			wOther, _ := permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", "other-assistant", "full", tc.body)
			if wOther.Code != http.StatusForbidden {
				t.Fatalf("%s with wrong target header: got %d want %d body=%s", tc.name, wOther.Code, http.StatusForbidden, wOther.Body.String())
			}
			// Matching manager header -> 200.
			wOK, _ := permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", s.managerIdentity(), "full", tc.body)
			if wOK.Code != http.StatusOK {
				t.Fatalf("%s with matching target header: got %d want %d body=%s", tc.name, wOK.Code, http.StatusOK, wOK.Body.String())
			}
		})
	}
}

func TestTodosV2BlockersHTTPRoutes(t *testing.T) {
	st := newMemBlockerQueueStore()
	s, _ := newQuietServiceOn(t, st)
	ctx := context.Background()

	openTodo, err := s.createTodo(ctx, "Open task", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	otherTodo, err := s.createTodo(ctx, "Other open task", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	doneTodo, err := s.createTodo(ctx, "Done task", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.todos.mu.Lock()
	doneAt := time.Now().UTC()
	s.todos.todos[doneTodo.ID].DoneAt = &doneAt
	s.todos.mu.Unlock()

	// 1. Flag off -> 409 Conflict for both POST and DELETE; read-only -> 403 Forbidden.
	if w := doBlockerHTTP(s, http.MethodPost, "/api/v1/todos/"+openTodo.ID+"/blockers", "answer", `{"kind":"external_pr","target_ref":"https://github.com/example/repo/pull/1"}`); w.Code != http.StatusForbidden {
		t.Fatalf("POST read-only: got %d want 403 body=%s", w.Code, w.Body.String())
	}
	if w := doBlockerHTTP(s, http.MethodDelete, "/api/v1/todos/"+openTodo.ID+"/blockers/blk_1", "answer", ""); w.Code != http.StatusForbidden {
		t.Fatalf("DELETE read-only: got %d want 403 body=%s", w.Code, w.Body.String())
	}
	if w := doBlockerHTTP(s, http.MethodPost, "/api/v1/todos/"+openTodo.ID+"/blockers", "full", `{"kind":"external_pr","target_ref":"https://github.com/example/repo/pull/1"}`); w.Code != http.StatusConflict {
		t.Fatalf("POST flag off: got %d want 409 body=%s", w.Code, w.Body.String())
	}
	if w := doBlockerHTTP(s, http.MethodDelete, "/api/v1/todos/"+openTodo.ID+"/blockers/blk_1", "full", ""); w.Code != http.StatusConflict {
		t.Fatalf("DELETE flag off: got %d want 409 body=%s", w.Code, w.Body.String())
	}

	// Enable todos_v2.
	setBlockersTodosV2Flag(t, s, true, false)

	// 2. POST validation and error codes: 404 (missing todo), 409 (closed todo), 400 (bad JSON / kind / URL).
	if w := doBlockerHTTP(s, http.MethodPost, "/api/v1/todos/no-such-todo/blockers", "full", `{"kind":"external_pr","target_ref":"https://github.com/example/repo/pull/1"}`); w.Code != http.StatusNotFound {
		t.Fatalf("POST missing todo: got %d want 404 body=%s", w.Code, w.Body.String())
	}
	if w := doBlockerHTTP(s, http.MethodPost, "/api/v1/todos/"+doneTodo.ID+"/blockers", "full", `{"kind":"external_pr","target_ref":"https://github.com/example/repo/pull/1"}`); w.Code != http.StatusConflict {
		t.Fatalf("POST closed todo: got %d want 409 body=%s", w.Code, w.Body.String())
	}
	for _, bad := range []string{
		`{not-json`,
		`{"kind":"todo","target_ref":"https://github.com/example/repo/pull/1"}`,
		`{"kind":"external_pr","target_ref":"http://github.com/example/repo/pull/1"}`,
		`{"kind":"external_pr","target_ref":""}`,
	} {
		if w := doBlockerHTTP(s, http.MethodPost, "/api/v1/todos/"+openTodo.ID+"/blockers", "full", bad); w.Code != http.StatusBadRequest {
			t.Fatalf("POST invalid body %s: got %d want 400 body=%s", bad, w.Code, w.Body.String())
		}
	}

	// 3. POST valid external_pr -> 200 with normalized target_ref and deterministic id.
	wAdd := doBlockerHTTP(s, http.MethodPost, "/api/v1/todos/"+openTodo.ID+"/blockers", "full", `{"kind":"external_pr","target_ref":"https://github.com/example/repo/pull/404/"}`)
	if wAdd.Code != http.StatusOK {
		t.Fatalf("POST valid blocker: got %d want 200 body=%s", wAdd.Code, wAdd.Body.String())
	}
	var addResp struct {
		Blocker struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			TargetRef string `json:"target_ref"`
			State     string `json:"state"`
			Attempts  int    `json:"attempts"`
		} `json:"blocker"`
	}
	if err := json.Unmarshal(wAdd.Body.Bytes(), &addResp); err != nil {
		t.Fatalf("decode POST response: %v body=%s", err, wAdd.Body.String())
	}
	if addResp.Blocker.ID == "" || addResp.Blocker.Kind != todoBlockerKindExternalPR ||
		addResp.Blocker.TargetRef != "https://github.com/example/repo/pull/404" ||
		addResp.Blocker.State != db.TodoBlockerStateActive || addResp.Blocker.Attempts != 0 {
		t.Fatalf("unexpected POST blocker response: %+v", addResp.Blocker)
	}

	// 4. DELETE ownership & 404 checks.
	if w := doBlockerHTTP(s, http.MethodDelete, "/api/v1/todos/no-such-todo/blockers/"+addResp.Blocker.ID, "full", ""); w.Code != http.StatusNotFound {
		t.Fatalf("DELETE missing todo: got %d want 404 body=%s", w.Code, w.Body.String())
	}
	if w := doBlockerHTTP(s, http.MethodDelete, "/api/v1/todos/"+otherTodo.ID+"/blockers/"+addResp.Blocker.ID, "full", ""); w.Code != http.StatusNotFound {
		t.Fatalf("DELETE blocker belonging to another todo: got %d want 404 body=%s", w.Code, w.Body.String())
	}
	if w := doBlockerHTTP(s, http.MethodDelete, "/api/v1/todos/"+openTodo.ID+"/blockers/blk_nonexistent", "full", ""); w.Code != http.StatusNotFound {
		t.Fatalf("DELETE missing blocker: got %d want 404 body=%s", w.Code, w.Body.String())
	}

	// 5. DELETE valid blocker -> 200 {"ok":true} and persisted state becomes cancelled.
	wDel := doBlockerHTTP(s, http.MethodDelete, "/api/v1/todos/"+openTodo.ID+"/blockers/"+addResp.Blocker.ID, "full", "")
	if wDel.Code != http.StatusOK {
		t.Fatalf("DELETE valid blocker: got %d want 200 body=%s", wDel.Code, wDel.Body.String())
	}
	var delResp map[string]bool
	if err := json.Unmarshal(wDel.Body.Bytes(), &delResp); err != nil || !delResp["ok"] {
		t.Fatalf("unexpected DELETE response: %s (err=%v)", wDel.Body.String(), err)
	}
	rows, err := st.ListTodoBlockersByTodo(ctx, openTodo.ID)
	if err != nil || len(rows) != 1 || rows[0].State != db.TodoBlockerStateCancelled {
		t.Fatalf("expected blocker state cancelled, got rows=%+v err=%v", rows, err)
	}
}
