package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

type countingBlockerStore struct {
	*memBlockerStore
	listActiveCalls atomic.Int64
}

func newCountingBlockerStore() *countingBlockerStore {
	return &countingBlockerStore{memBlockerStore: newMemBlockerStore()}
}

func (c *countingBlockerStore) ListTodoReadinessBlockers(ctx context.Context, todoIDs []string) ([]db.TodoBlockerRow, error) {
	c.listActiveCalls.Add(1)
	return c.memBlockerStore.ListTodoReadinessBlockers(ctx, todoIDs)
}

func TestTodosV2BlockersDefaultExternalPRFetcherAutoWake(t *testing.T) {
	st := newMemBlockerStore()
	s, _ := newQuietServiceOn(t, st)
	setBlockersTodosV2Flag(t, s, true, false)
	ctx := context.Background()

	var mu sync.Mutex
	prMerged := false
	failNext := true
	prRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/repos/example/upstream/pulls/99":
			prRequests++
			if failNext {
				http.Error(w, "temporary upstream error", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if prMerged {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"title":     "Upstream API change",
					"state":     "closed",
					"merged":    true,
					"merged_at": time.Now().UTC().Format(time.RFC3339),
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"title": "Upstream API change",
				"state": "open",
				"head":  map[string]any{"sha": "abc123"},
			})
		case "/repos/example/upstream/pulls/99/reviews":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case "/repos/example/upstream/commits/abc123/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"state":"success","total_count":0,"statuses":[]}`))
		case "/repos/example/upstream/commits/abc123/check-runs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"total_count":0,"check_runs":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Use the default production forge fetcher path (no custom setTodoBlockerPRFetcher hook).
	s.deploy.forge = githubForge{base: srv.URL, client: srv.Client()}

	todoA, err := s.createTodo(ctx, "Downstream consumer A", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	todoB, err := s.createTodo(ctx, "Downstream consumer B", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}

	// Both todos depend on a pure external PR not tracked by any local todo, with slight casing variation.
	if _, err := s.AddTodoBlocker(ctx, todoA.ID, todoBlockerKindExternalPR, "https://github.com/example/upstream/pull/99", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTodoBlocker(ctx, todoB.ID, todoBlockerKindExternalPR, "https://github.com/Example/Upstream/pull/99/", ""); err != nil {
		t.Fatal(err)
	}

	var woke []string
	s.setTodoBlockerUnblockedHook(func(_ context.Context, todoID string) {
		mu.Lock()
		woke = append(woke, todoID)
		mu.Unlock()
	})

	now := time.Now()
	if err := s.refreshTodoBlockers(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshTodoBlockers(ctx, now.Add(externalPRRetryMin/2)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if prRequests != 1 {
		t.Fatalf("shared PR received %d failed requests before backoff expired", prRequests)
	}
	failNext = false
	mu.Unlock()

	// The next successful read sees the PR open. A later tick within the TTL reuses it.
	next := now.Add(2 * externalPRRetryMin)
	if err := s.refreshTodoBlockers(ctx, next); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	prMerged = true
	mu.Unlock()
	if err := s.refreshTodoBlockers(ctx, next.Add(externalPRObservationTTL/2)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if prRequests != 2 || len(woke) != 0 {
		t.Fatalf("open PR was refetched or cleared before TTL: requests=%d wakes=%v", prRequests, woke)
	}
	mu.Unlock()

	if err := s.refreshTodoBlockers(ctx, next.Add(2*externalPRObservationTTL)); err != nil {
		t.Fatal(err)
	}
	if active, err := st.ListActiveTodoBlockers(ctx); err != nil || len(active) != 0 {
		t.Fatalf("terminal observation left blockers active: %+v, %v", active, err)
	}
	wantWoke := []string{todoA.ID, todoB.ID}
	if todoB.ID < todoA.ID {
		wantWoke = []string{todoB.ID, todoA.ID}
	}
	mu.Lock()
	if prRequests != 3 {
		t.Fatalf("expected one fresh request on merge, got %d requests total", prRequests)
	}
	gotWoke := append([]string(nil), woke...)
	mu.Unlock()
	if !reflect.DeepEqual(gotWoke, wantWoke) {
		t.Fatalf("woke hook IDs = %v, want %v", gotWoke, wantWoke)
	}
}

func TestTodosV2BlockersProjectionWaitingAndBoard(t *testing.T) {
	st := newCountingBlockerStore()
	s, _ := newQuietServiceOn(t, st)
	setBlockersTodosV2Flag(t, s, true, false)
	ctx := context.Background()

	plainBlocked, err := s.createTodo(ctx, "Blocked plain task", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	needsYouBlocked, err := s.createTodo(ctx, "Failed start blocked task", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.failTodo(ctx, needsYouBlocked.ID, "agent failed to launch"); err != nil {
		t.Fatal(err)
	}
	unblockedPlain, err := s.createTodo(ctx, "Unblocked plain task", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}

	prURL := "https://github.com/example/upstream/pull/101"
	if _, err := s.AddTodoBlocker(ctx, plainBlocked.ID, todoBlockerKindExternalPR, prURL, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTodoBlocker(ctx, needsYouBlocked.ID, todoBlockerKindExternalPR, prURL, ""); err != nil {
		t.Fatal(err)
	}

	st.listActiveCalls.Store(0)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/todos", nil)
	req.Header.Set(RoleHeader, "full")
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos: code=%d body=%s", w.Code, w.Body.String())
	}

	// Verify O(1) DB query (zero N+1).
	if calls := st.listActiveCalls.Load(); calls != 1 {
		t.Fatalf("ListActiveTodoBlockers calls = %d, want 1", calls)
	}

	var resp todosList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode todosList: %v", err)
	}
	byID := make(map[string]listedTodo, len(resp.Todos))
	for _, item := range resp.Todos {
		byID[item.ID] = item
	}

	gotBlocked := byID[plainBlocked.ID]
	if len(gotBlocked.ActiveBlockers) != 1 {
		t.Fatalf("plainBlocked.ActiveBlockers = %+v, want 1 blocker", gotBlocked.ActiveBlockers)
	}
	if gotBlocked.ActiveBlockers[0].Kind != todoBlockerKindExternalPR || gotBlocked.ActiveBlockers[0].TargetRef != prURL {
		t.Fatalf("unexpected active_blocker: %+v", gotBlocked.ActiveBlockers[0])
	}
	if gotBlocked.View == nil || gotBlocked.View.Section != assistantwire.TodoListSectionWaiting || gotBlocked.View.Phase != assistantwire.TodoPhaseWaiting {
		t.Fatalf("plainBlocked.View = %+v, want waiting section/phase", gotBlocked.View)
	}
	if gotBlocked.View.Reason != assistantwire.TodoReasonParked || gotBlocked.View.Line == nil || gotBlocked.View.Line.Key != assistantwire.TodoLineKeyParked {
		t.Fatalf("plainBlocked.View reason/line = %+v, want parked with line", gotBlocked.View)
	}
	if target := gotBlocked.View.Line.Args[assistantwire.TodoLineArgReason]; target != "example/upstream#101" {
		t.Fatalf("plainBlocked wait target = %q, want example/upstream#101", target)
	}

	// Needs-you task must stay in needs_you even when it also has an active blocker.
	gotNeedsYou := byID[needsYouBlocked.ID]
	if len(gotNeedsYou.ActiveBlockers) != 1 {
		t.Fatalf("needsYouBlocked.ActiveBlockers = %+v, want 1 blocker", gotNeedsYou.ActiveBlockers)
	}
	if gotNeedsYou.View == nil || gotNeedsYou.View.Section != assistantwire.TodoListSectionNeedsYou {
		t.Fatalf("needsYouBlocked.View = %+v, want needs_you", gotNeedsYou.View)
	}

	// Unblocked plain task must remain in todo with no active_blockers.
	gotUnblocked := byID[unblockedPlain.ID]
	if len(gotUnblocked.ActiveBlockers) != 0 {
		t.Fatalf("unblockedPlain.ActiveBlockers = %+v, want empty", gotUnblocked.ActiveBlockers)
	}
	if gotUnblocked.View == nil || gotUnblocked.View.Section != assistantwire.TodoListSectionTodo {
		t.Fatalf("unblockedPlain.View = %+v, want todo", gotUnblocked.View)
	}

	// Board projection must consume the updated waiting view.
	if resp.Board == nil {
		t.Fatal("expected board projection under todos_v2=true")
	}
	foundInWaiting := false
	for _, g := range resp.Board.StatusGroups {
		if g.Key == boardSectionWaiting {
			for _, e := range g.Entries {
				if e.TodoID == plainBlocked.ID {
					foundInWaiting = true
				}
			}
		}
	}
	if !foundInWaiting {
		t.Fatalf("expected plainBlocked %s in board waiting group, got %+v", plainBlocked.ID, resp.Board.StatusGroups)
	}
}

func TestTodosV2BlockersProjectionRanksAndRechecks(t *testing.T) {
	st := newMemBlockerStore()
	s, f := newQuietServiceOn(t, st)
	setBlockersTodosV2Flag(t, s, true, false)
	e := &todoEnv{t: t, s: s, f: f}
	first := e.create("Wait for upstream work").ID
	second := e.create("Wait for another change").ID
	plain := e.create("Personal to-do").ID
	closed := e.create("Finished task").ID
	for _, id := range []string{first, second, closed} {
		if _, err := s.AddTodoBlocker(t.Context(), id, todoBlockerKindExternalPR, "https://github.com/example/upstream/pull/101", ""); err != nil {
			t.Fatal(err)
		}
	}
	e.must("PATCH", "/api/v1/todos/"+closed, `{"done":true}`)

	list, full, err := s.todosViewed(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	byID := full.byID()
	ranks := []int{byID[first].Rank, byID[second].Rank}
	if byID[first].Section != assistantwire.TodoListSectionWaiting || byID[second].Section != assistantwire.TodoListSectionWaiting ||
		ranks[0]+ranks[1] != 1 || ranks[0] == ranks[1] {
		t.Errorf("waiting section ranks = %v for views %+v and %+v; want 0 and 1", ranks, byID[first], byID[second])
	}
	if got := byID[plain]; got.Section != assistantwire.TodoListSectionTodo || got.Rank != 0 {
		t.Errorf("only personal to-do in its section = %+v; want rank 0", got)
	}
	for _, row := range list {
		if row.ID == closed && (row.View.Phase != assistantwire.TodoPhaseDone || row.Wait != nil || len(row.ActiveBlockers) != 0) {
			t.Errorf("closed task still has active blocker evidence: view=%+v wait=%+v blockers=%+v", row.View, row.Wait, row.ActiveBlockers)
		}
	}

	gen := s.todoViewGen()
	ids := map[string]bool{first: true}
	recheck, err := s.todoViewsOf(t.Context(), ids, s.todoViewNow())
	if err != nil {
		t.Fatal(err)
	}
	if len(recheck.waits) != 1 || recheck.waits[0] == nil || recheck.waits[0].Kind != assistantwire.TodoWaitKindPrMerge {
		t.Errorf("recheck lost the blocker wait: %+v", recheck.waits)
	}
	if s.todoViewsSeen(gen, recheck, ids) {
		t.Errorf("rechecking an unchanged blocker changed the published class: %+v", recheck.views)
	}
}

func TestTodosV2BlockersMergedIntoSharedBlockersAndDedup(t *testing.T) {
	st := newMemBlockerStore()
	s, _ := newQuietServiceOn(t, st)
	setBlockersTodosV2Flag(t, s, true, false)
	ctx := context.Background()

	prURL := "https://github.com/example/shared/pull/42"
	prURLVariant := "https://github.com/Example/Shared/pull/42/"

	todo1, err := s.createTodo(ctx, "Blocker edge task 1", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	todo2, err := s.createTodo(ctx, "Blocker edge task 2", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	todo3, err := s.createTodo(ctx, "Parked task 3", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	todo4, err := s.createTodo(ctx, "Parked task 4", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	closedTodo, err := s.createTodo(ctx, "Closed task with stale blocker", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}

	// todo1 and todo2 have persistent external_pr blockers on prURL (different casing).
	if _, err := s.AddTodoBlocker(ctx, todo1.ID, todoBlockerKindExternalPR, prURL, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTodoBlocker(ctx, todo2.ID, todoBlockerKindExternalPR, prURLVariant, ""); err != nil {
		t.Fatal(err)
	}
	// closedTodo also has a persisted active blocker row, but is done and must be excluded.
	if _, err := s.AddTodoBlocker(ctx, closedTodo.ID, todoBlockerKindExternalPR, prURL, ""); err != nil {
		t.Fatal(err)
	}
	doneNow := time.Now().UTC()
	s.todos.mu.Lock()
	s.todos.todos[closedTodo.ID].DoneAt = &doneNow
	// todo3 and todo4 are parked via Supervision on the same PR so projection.SharedBlockers emits it too.
	parkedAt := doneNow.Add(-time.Hour)
	until := doneNow.Add(4 * time.Hour)
	for _, id := range []string{todo3.ID, todo4.ID} {
		s.todos.todos[id].Supervision = &Supervision{
			State:     reviewParked,
			UpdatedAt: parkedAt,
			Reviews: []TaskReview{{
				At:        parkedAt,
				Verdict:   reviewParked,
				Reason:    "waiting on shared PR",
				Until:     until,
				WaitingOn: parkExternalEvent,
				Evidence: &ParkEvidence{
					Kind: parkExternalEvent,
					URL:  prURLVariant,
				},
			}},
		}
	}
	s.todos.mu.Unlock()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/todos", nil)
	req.Header.Set(RoleHeader, "full")
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos: code=%d body=%s", w.Code, w.Body.String())
	}

	var resp todosList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode todosList: %v", err)
	}
	if len(resp.SharedBlockers) != 1 {
		t.Fatalf("SharedBlockers = %+v, want exactly 1 deduplicated group", resp.SharedBlockers)
	}
	sb := resp.SharedBlockers[0]
	wantKey := projection.SharedBlockerKindPullRequest + ":" + prURL
	if sb.Key != wantKey || sb.Kind != projection.SharedBlockerKindPullRequest || sb.Label != "example/shared#42" || sb.URL != prURL {
		t.Fatalf("unexpected shared blocker metadata: %+v", sb)
	}
	if sb.Count != 4 || len(sb.TodoIDs) != 4 {
		t.Fatalf("expected union of 4 open todos (excluding closed), got count=%d ids=%v", sb.Count, sb.TodoIDs)
	}
	for _, id := range []string{todo1.ID, todo2.ID, todo3.ID, todo4.ID} {
		found := false
		for _, gotID := range sb.TodoIDs {
			if gotID == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing todo %s in shared blocker TodoIDs %v", id, sb.TodoIDs)
		}
	}
}

func TestTodosV2BlockersProjectionFlagOffRollback(t *testing.T) {
	st := newCountingBlockerStore()
	s, _ := newQuietServiceOn(t, st)
	ctx := context.Background()

	todoA, err := s.createTodo(ctx, "Task A", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	todoB, err := s.createTodo(ctx, "Task B", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}

	// Pre-populate persistent blockers directly in DB (e.g. written while todos_v2 was previously on).
	prURL := "https://github.com/example/rollback/pull/7"
	_, _ = st.UpsertTodoBlocker(ctx, db.TodoBlockerRow{
		ID:        "blk_rb_a",
		TodoID:    todoA.ID,
		Kind:      todoBlockerKindExternalPR,
		TargetRef: prURL,
	})
	_, _ = st.UpsertTodoBlocker(ctx, db.TodoBlockerRow{
		ID:        "blk_rb_b",
		TodoID:    todoB.ID,
		Kind:      todoBlockerKindExternalPR,
		TargetRef: prURL,
	})

	getRaw := func() []byte {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/todos", nil)
		req.Header.Set(RoleHeader, "full")
		s.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/todos: code=%d body=%s", w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}

	// 1. Flag OFF (default): zero blocker DB queries, no active_blockers, no shared_blockers, plain todo stays in todo.
	st.listActiveCalls.Store(0)
	offBefore := getRaw()
	if calls := st.listActiveCalls.Load(); calls != 0 {
		t.Fatalf("todos_v2=false called ListActiveTodoBlockers %d times, want 0", calls)
	}
	if strings.Contains(string(offBefore), `"active_blockers"`) || strings.Contains(string(offBefore), `"shared_blockers"`) {
		t.Fatalf("todos_v2=false leaked blocker fields on wire: %s", string(offBefore))
	}

	// 2. Toggle ON: active_blockers, waiting projection, and shared_blockers appear.
	setBlockersTodosV2Flag(t, s, true, false)
	onBody := getRaw()
	var onResp todosList
	if err := json.Unmarshal(onBody, &onResp); err != nil {
		t.Fatalf("decode todos_v2=true response: %v", err)
	}
	if len(onResp.SharedBlockers) != 1 || onResp.SharedBlockers[0].Count != 2 {
		t.Fatalf("todos_v2=true SharedBlockers = %+v, want 1 group of 2", onResp.SharedBlockers)
	}
	for _, item := range onResp.Todos {
		if len(item.ActiveBlockers) != 1 || item.View == nil || item.View.Section != assistantwire.TodoListSectionWaiting {
			t.Fatalf("todos_v2=true todo %s: active_blockers=%+v view=%+v", item.ID, item.ActiveBlockers, item.View)
		}
	}

	// 3. Toggle OFF: lossless rollback to byte-identical legacy payload with zero blocker DB queries.
	setBlockersTodosV2Flag(t, s, false, true)
	st.listActiveCalls.Store(0)
	offAfter := getRaw()
	if calls := st.listActiveCalls.Load(); calls != 0 {
		t.Fatalf("todos_v2=false after rollback called ListActiveTodoBlockers %d times, want 0", calls)
	}
	if string(offBefore) != string(offAfter) {
		t.Fatalf("todos_v2 rollback not byte-identical:\nbefore: %s\nafter:  %s", string(offBefore), string(offAfter))
	}
}
