package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

func TestTodosV2RepoGateHTTPGatingAndRollback(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	st := &fakeTodoRepoStore{
		planMemStore: &planMemStore{memStore: &memStore{}},
		repos:        map[string][]repository.TaskRepository{},
	}
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)

	makeRepo := func(repoID, path string, links []repository.TaskLink) repository.TaskRepository {
		return repository.TaskRepository{
			RepoID: repoID,
			SourceSnapshot: repository.SourceSnapshot{
				Identity: repository.Identity{
					Provider:     "github",
					InstanceURL:  "https://github.com",
					Path:         path,
					CanonicalURL: "https://github.com/" + path,
				},
			},
			Links: links,
		}
	}

	// 1. Multi-repo to-do: 2 target repos (1 merged, 1 CI failed) + 1 failing reference repo.
	blockedID := "todo_gate_blocked"
	// 2. Multi-repo to-do: 2 ready target repos + 1 failing reference repo (must still be ready).
	readyID := "todo_gate_ready"
	// 3. Reference-only to-do: only reference repo bound (must be reference_only).
	refOnlyID := "todo_gate_ref_only"
	// 4. Unbound to-do: no repositories bound (must omit repo_gate even under todos_v2=true).
	unboundID := "todo_gate_unbound"

	s.todos.mu.Lock()
	s.todos.loaded = true
	s.todos.todos = map[string]*Todo{
		blockedID: {
			ID:        blockedID,
			Title:     "Cross-repo feature with CI failure",
			CreatedAt: now.Add(-4 * time.Minute),
			UpdatedAt: now.Add(-4 * time.Minute),
			PRURL:     "https://github.com/org/backend/pull/101",
			PRStatus: &TodoPRStatus{
				URL:       "https://github.com/org/backend/pull/101",
				State:     "open",
				Review:    "approved",
				Checks:    "failure",
				CheckedAt: now.Add(-time.Minute),
			},
			OtherPRs: []TodoPRStatus{
				{
					URL:       "https://github.com/org/frontend/pull/202",
					State:     "merged",
					CheckedAt: now,
				},
				{
					URL:       "https://github.com/org/docs-ref/pull/999",
					State:     "open",
					Checks:    "failure",
					Conflicts: true,
					CheckedAt: now,
				},
			},
		},
		readyID: {
			ID:        readyID,
			Title:     "Cross-repo feature all targets ready",
			CreatedAt: now.Add(-3 * time.Minute),
			UpdatedAt: now.Add(-3 * time.Minute),
			PRURL:     "https://github.com/org/api/pull/11",
			PRStatus: &TodoPRStatus{
				URL:       "https://github.com/org/api/pull/11",
				State:     "merged",
				CheckedAt: now,
			},
			OtherPRs: []TodoPRStatus{
				{
					URL:       "https://github.com/org/web/pull/22",
					State:     "open",
					Review:    "approved",
					Checks:    "success",
					CheckedAt: now,
				},
				{
					URL:       "https://github.com/org/upstream-ref/pull/88",
					State:     "open",
					Checks:    "failure",
					Conflicts: true,
					CheckedAt: now,
				},
			},
		},
		refOnlyID: {
			ID:        refOnlyID,
			Title:     "Research task with reference repo only",
			CreatedAt: now.Add(-2 * time.Minute),
			UpdatedAt: now.Add(-2 * time.Minute),
			PRURL:     "https://github.com/org/upstream-ref/pull/77",
			PRStatus: &TodoPRStatus{
				URL:       "https://github.com/org/upstream-ref/pull/77",
				State:     "open",
				Checks:    "failure",
				CheckedAt: now,
			},
		},
		unboundID: {
			ID:        unboundID,
			Title:     "Plain to-do without bound repos",
			CreatedAt: now.Add(-1 * time.Minute),
			UpdatedAt: now.Add(-1 * time.Minute),
		},
	}
	s.todos.mu.Unlock()

	st.repos[blockedID] = []repository.TaskRepository{
		makeRepo("repo_backend", "org/backend", []repository.TaskLink{
			{Kind: "pull_request", Number: "101", CanonicalURL: "https://github.com/org/backend/pull/101", Role: repository.LinkRoleDelivery},
		}),
		makeRepo("repo_frontend", "org/frontend", []repository.TaskLink{
			// Empty role defaults to delivery (Target Repo).
			{Kind: "pull_request", Number: "202", CanonicalURL: "https://github.com/org/frontend/pull/202", Role: ""},
		}),
		makeRepo("repo_docs_ref", "org/docs-ref", []repository.TaskLink{
			{Kind: "pull_request", Number: "999", CanonicalURL: "https://github.com/org/docs-ref/pull/999", Role: repository.LinkRoleReference},
		}),
	}
	st.repos[readyID] = []repository.TaskRepository{
		makeRepo("repo_api", "org/api", []repository.TaskLink{
			{Kind: "pull_request", Number: "11", CanonicalURL: "https://github.com/org/api/pull/11", Role: repository.LinkRoleDelivery},
		}),
		makeRepo("repo_web", "org/web", []repository.TaskLink{
			{Kind: "pull_request", Number: "22", CanonicalURL: "https://github.com/org/web/pull/22", Role: repository.LinkRoleDelivery},
		}),
		makeRepo("repo_upstream_ref", "org/upstream-ref", []repository.TaskLink{
			{Kind: "pull_request", Number: "88", CanonicalURL: "https://github.com/org/upstream-ref/pull/88", Role: repository.LinkRoleReference},
		}),
	}
	st.repos[refOnlyID] = []repository.TaskRepository{
		makeRepo("repo_upstream_ref", "org/upstream-ref", []repository.TaskLink{
			{Kind: "pull_request", Number: "77", CanonicalURL: "https://github.com/org/upstream-ref/pull/77", Role: repository.LinkRoleReference},
		}),
	}

	getTodosRaw := func() map[string]map[string]any {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1/todos", nil)
		r.Header.Set(RoleHeader, "full")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/todos: code=%d body=%s", w.Code, w.Body.String())
		}
		var payload struct {
			Todos []map[string]any `json:"todos"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode GET /api/v1/todos: %v", err)
		}
		byID := make(map[string]map[string]any, len(payload.Todos))
		for _, row := range payload.Todos {
			id, _ := row["id"].(string)
			byID[id] = row
		}
		return byID
	}

	getTodoItemRaw := func(id string) map[string]any {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1/todos/"+id, nil)
		r.Header.Set(RoleHeader, "full")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/todos/%s: code=%d body=%s", id, w.Code, w.Body.String())
		}
		var payload struct {
			Todo map[string]any `json:"todo"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode GET /api/v1/todos/%s: %v", id, err)
		}
		return payload.Todo
	}

	setTodosV2 := func(want, expected bool) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"todos_v2": want, "expected_todos_v2": expected})
		w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", s.managerIdentity(), "full", string(body))
		if w.Code != http.StatusOK {
			t.Fatalf("PUT todos_v2=%t: code=%d body=%s", want, w.Code, w.Body.String())
		}
	}

	decodeGate := func(row map[string]any) *repoGate {
		t.Helper()
		raw, ok := row["repo_gate"]
		if !ok {
			return nil
		}
		b, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		var g repoGate
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatal(err)
		}
		return &g
	}

	// Phase 1: todos_v2=false (default) -> repo_gate must be completely omitted on all rows.
	before := getTodosRaw()
	for _, id := range []string{blockedID, readyID, refOnlyID, unboundID} {
		if _, has := before[id]["repo_gate"]; has {
			t.Fatalf("todos_v2=false: todo %s unexpectedly emitted repo_gate: %v", id, before[id]["repo_gate"])
		}
	}
	if _, has := getTodoItemRaw(blockedID)["repo_gate"]; has {
		t.Fatalf("todos_v2=false: GET /api/v1/todos/%s unexpectedly emitted repo_gate", blockedID)
	}

	// Phase 2: enable todos_v2=true -> repo_gate is computed strictly for bound todos.
	setTodosV2(true, false)
	enabled := getTodosRaw()

	wantBlocked := &repoGate{
		State:          repoGateBlockedCI,
		TargetCount:    2,
		ReadyCount:     1,
		ReferenceCount: 1,
		BlockingRepo:   "org/backend",
		BlockingPR:     "https://github.com/org/backend/pull/101",
		Reason:         "ci_failed",
	}
	if got := decodeGate(enabled[blockedID]); !reflect.DeepEqual(got, wantBlocked) {
		t.Fatalf("blocked todo repo_gate = %+v, want %+v", got, wantBlocked)
	}
	if got := decodeGate(getTodoItemRaw(blockedID)); !reflect.DeepEqual(got, wantBlocked) {
		t.Fatalf("blocked todo item repo_gate = %+v, want %+v", got, wantBlocked)
	}

	wantReady := &repoGate{
		State:          repoGateReady,
		TargetCount:    2,
		ReadyCount:     2,
		ReferenceCount: 1,
		Reason:         "all_targets_ready",
	}
	if got := decodeGate(enabled[readyID]); !reflect.DeepEqual(got, wantReady) {
		t.Fatalf("ready todo repo_gate = %+v, want %+v", got, wantReady)
	}

	wantRefOnly := &repoGate{
		State:          repoGateReferenceOnly,
		TargetCount:    0,
		ReadyCount:     0,
		ReferenceCount: 1,
		Reason:         "reference_only",
	}
	if got := decodeGate(enabled[refOnlyID]); !reflect.DeepEqual(got, wantRefOnly) {
		t.Fatalf("reference-only todo repo_gate = %+v, want %+v", got, wantRefOnly)
	}

	if _, has := enabled[unboundID]["repo_gate"]; has {
		t.Fatalf("unbound todo should omit repo_gate even when todos_v2=true, got: %v", enabled[unboundID]["repo_gate"])
	}

	// After the ordinary PR freshness window, the bound gate still holds both a list read and a view recheck.
	later := now.Add(2 * time.Hour)
	list, err := s.listedTodos(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	full := s.todoProjectionOf(t.Context(), list, later)
	recheck, err := s.todoViewsOf(t.Context(), map[string]bool{blockedID: true}, later)
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range list {
		if row.ID != blockedID {
			continue
		}
		wait := full.waits[i]
		if wait == nil || wait.URL != wantBlocked.BlockingPR ||
			!reflect.DeepEqual(wait.PrBlockers, []assistantwire.TodoWaitPRBlocker{assistantwire.TodoWaitPRBlockerChecksFailing}) {
			t.Fatalf("bound gate did not keep its structured waiting evidence: %+v", wait)
		}
		if len(recheck.waits) != 1 || !reflect.DeepEqual(recheck.waits[0], wait) ||
			todoClassOf(recheck.views[0]) != todoClassOf(full.views[i]) {
			t.Fatalf("recheck diverged from the bound gate in the list: %+v", recheck)
		}
	}

	assertPRHistory := func(row map[string]any) {
		t.Helper()
		for _, key := range []string{"pr_url", "pr_status", "other_prs"} {
			if want := before[blockedID][key]; want == nil || !reflect.DeepEqual(row[key], want) {
				t.Errorf("%s = %v, want original PR history %v", key, row[key], want)
			}
		}
	}

	// Accept the task while the last CI observation still says the delivery PR failed.
	s.todos.mu.Lock()
	s.todos.todos[blockedID].DoneAt = &now
	s.todos.todos[blockedID].DoneBy = db.DoneByReview
	s.todos.mu.Unlock()
	for _, result := range []struct {
		route string
		row   map[string]any
	}{
		{"list", getTodosRaw()[blockedID]},
		{"detail", getTodoItemRaw(blockedID)},
	} {
		if got := decodeGate(result.row); got != nil {
			t.Errorf("%s closed todo repo_gate = %+v, want omitted", result.route, got)
		}
		view, _ := result.row["view"].(map[string]any)
		if result.row["done_at"] == nil || view["phase"] != string(assistantwire.TodoPhaseDone) || result.row["wait"] != nil {
			t.Errorf("%s closed todo has done_at=%v, phase=%v, wait=%v, want done without a wait",
				result.route, result.row["done_at"], view["phase"], result.row["wait"])
		}
		assertPRHistory(result.row)
	}

	// Phase 3: lossless rollback to todos_v2=false -> repo_gate immediately disappears again.
	setTodosV2(false, true)
	after := getTodosRaw()
	for _, id := range []string{blockedID, readyID, refOnlyID, unboundID} {
		if _, has := after[id]["repo_gate"]; has {
			t.Fatalf("after rollback todos_v2=false: todo %s unexpectedly kept repo_gate: %v", id, after[id]["repo_gate"])
		}
	}

	// Verify saved todos were never mutated by toggling todos_v2.
	if gotTitle, _ := after[blockedID]["title"].(string); !strings.Contains(gotTitle, "Cross-repo feature") {
		t.Fatalf("unexpected title after rollback: %q", gotTitle)
	}
	assertPRHistory(after[blockedID])
}

func TestTodosV2RepoGateUnlinkedTargetNeedsExplicitPR(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	st := &fakeTodoRepoStore{
		planMemStore: &planMemStore{memStore: &memStore{}},
		repos:        map[string][]repository.TaskRepository{},
	}
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)

	todoID := "todo_gate_unlinked_target"
	s.todos.mu.Lock()
	s.todos.loaded = true
	s.todos.todos = map[string]*Todo{
		todoID: {
			ID:        todoID,
			Title:     "Target repo bound without explicit links",
			CreatedAt: now,
			UpdatedAt: now,
			PRURL:     "https://github.com/org/core/pull/5",
			PRStatus: &TodoPRStatus{
				URL:       "https://github.com/org/core/pull/5",
				State:     "open",
				Checks:    "failure",
				CheckedAt: now.Add(-time.Hour),
			},
		},
	}
	s.todos.mu.Unlock()

	st.repos[todoID] = []repository.TaskRepository{
		{
			RepoID: "repo_core",
			SourceSnapshot: repository.SourceSnapshot{
				Identity: repository.Identity{
					Provider:     "github",
					InstanceURL:  "https://github.com",
					Path:         "org/core",
					CanonicalURL: "https://github.com/org/core",
				},
			},
		},
		{
			RepoID: "repo_worker",
			SourceSnapshot: repository.SourceSnapshot{
				Identity: repository.Identity{
					Provider:     "github",
					InstanceURL:  "https://github.com",
					Path:         "org/worker",
					CanonicalURL: "https://github.com/org/worker",
				},
			},
		},
	}

	assertSnapshotState := func(want string) {
		t.Helper()
		if got := snapshotStatesForTest(s, context.Background())[todoID].State; got != want {
			t.Fatalf("task snapshot state = %q, want %q", got, want)
		}
	}
	assertSnapshotState("PR 检查失败") // i18n-ignore: legacy task snapshot label

	w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", s.managerIdentity(), "full", `{"todos_v2":true,"expected_todos_v2":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT todos_v2=true: %d %s", w.Code, w.Body.String())
	}
	assertSnapshotState("还没开始") // i18n-ignore: task snapshot label

	r := httptest.NewRequest("GET", "/api/v1/todos", nil)
	r.Header.Set(RoleHeader, "full")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Todos []struct {
			ID       string    `json:"id"`
			RepoGate *repoGate `json:"repo_gate"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Todos) != 1 {
		t.Fatalf("got %d todos, want 1", len(payload.Todos))
	}
	want := &repoGate{
		State:          repoGateMissingPR,
		TargetCount:    2,
		ReadyCount:     0,
		ReferenceCount: 0,
		BlockingRepo:   "org/core",
		Reason:         "missing_pr",
	}
	if !reflect.DeepEqual(payload.Todos[0].RepoGate, want) {
		t.Fatalf("repo_gate = %+v, want %+v", payload.Todos[0].RepoGate, want)
	}
}
