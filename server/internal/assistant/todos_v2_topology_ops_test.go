package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
)

// memTopologyStore extends memBlockerQueueStore with in-memory saved repository links
// so split/merge link copying and deduplicated transfer can be verified without Postgres.
type memTopologyStore struct {
	*memBlockerQueueStore
	repoMu sync.Mutex
	links  map[string][]repository.TaskLink
}

func newMemTopologyStore() *memTopologyStore {
	return &memTopologyStore{
		memBlockerQueueStore: newMemBlockerQueueStore(),
		links:                make(map[string][]repository.TaskLink),
	}
}

func (m *memTopologyStore) seedRepoLink(todoID string, link repository.TaskLink) {
	m.repoMu.Lock()
	defer m.repoMu.Unlock()
	m.links[todoID] = append(m.links[todoID], link)
}

func (m *memTopologyStore) TodoRepositories(_ context.Context, todoID string) (repository.TaskSet, error) {
	m.repoMu.Lock()
	defer m.repoMu.Unlock()
	ls := slices.Clone(m.links[todoID])
	if len(ls) == 0 {
		return repository.TaskSet{TodoID: todoID, Revision: 1}, nil
	}
	return repository.TaskSet{
		TodoID:   todoID,
		Revision: 1,
		Repositories: []repository.TaskRepository{
			{
				RepoID: "repo_test",
				Links:  ls,
			},
		},
	}, nil
}

func (m *memTopologyStore) CreateTodoWithRepositories(_ context.Context, _ db.TodoRow, _ repository.CreateBindings) error {
	return nil
}

func (m *memTopologyStore) SetTodoRepositories(_ context.Context, todoID string, _ repository.SetInput) (repository.TaskSet, error) {
	return repository.TaskSet{TodoID: todoID, Revision: 1}, nil
}

func (m *memTopologyStore) SetTodoRepositoryLinks(_ context.Context, todoID, _ string, _ repository.LinksInput) (repository.TaskSet, error) {
	return repository.TaskSet{TodoID: todoID, Revision: 1}, nil
}

func (m *memTopologyStore) ImportTodoRepositoryLinkDetails(_ context.Context, todoID string, in []repository.LinkInput, _ string) (repository.TaskSet, bool, error) {
	m.repoMu.Lock()
	defer m.repoMu.Unlock()
	existing := make(map[string]bool)
	for _, l := range m.links[todoID] {
		existing[normalizePRKey(l.CanonicalURL)] = true
	}
	changed := false
	for _, item := range in {
		key := normalizePRKey(item.URL)
		if key == "" || existing[key] {
			continue
		}
		existing[key] = true
		role := item.Role
		if role == "" {
			role = repository.LinkRoleDelivery
		}
		kind := item.Kind
		if kind == "" {
			kind = "pull_request"
		}
		m.links[todoID] = append(m.links[todoID], repository.TaskLink{
			Kind:         kind,
			Number:       item.Number,
			Title:        item.Title,
			CanonicalURL: item.URL,
			Role:         role,
		})
		changed = true
	}
	return repository.TaskSet{TodoID: todoID, Revision: 2}, changed, nil
}

func newTopologyTestEnv(t *testing.T, todosV2 bool) (*Service, *memTopologyStore) {
	t.Helper()
	st := newMemTopologyStore()
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	s.briefsOptional = true
	setBlockersTodosV2Flag(t, s, todosV2, false)
	return s, st
}

func doTopologyHTTP(s *Service, role, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if role != "" {
		req.Header.Set(RoleHeader, role)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func TestTodosV2TopologySplit(t *testing.T) {
	ctx := context.Background()

	t.Run("split_inherits_metadata_wires_after_moves_blockers_and_copies_links", func(t *testing.T) {
		s, st := newTopologyTestEnv(t, true)
		dirID := "dir_alpha"
		proj := "persona"
		itemType := db.ItemTypeTask
		src, err := s.createPlainTodo(ctx, "Parent epic", "Initial context", db.TodoEdit{
			DirectionID: &dirID,
			Project:     &proj,
			ItemType:    &itemType,
		}, nil)
		if err != nil {
			t.Fatalf("create source: %v", err)
		}

		// Seed a delivery PR link and an active external_pr blocker on source.
		prURL := "https://github.com/example/persona/pull/101"
		st.seedRepoLink(src.ID, repository.TaskLink{
			Kind:         "pull_request",
			Number:       "101",
			Title:        "Parent PR",
			CanonicalURL: prURL,
			Role:         repository.LinkRoleDelivery,
		})
		blk, err := s.AddTodoBlocker(ctx, src.ID, todoBlockerKindExternalPR, "https://github.com/example/persona/pull/99", "")
		if err != nil || blk == nil {
			t.Fatalf("AddTodoBlocker: %v, row=%v", err, blk)
		}

		res, err := s.splitTodo(ctx, src.ID, todoSplitRequest{
			Children: []todoSplitChildInput{
				{
					Title:          "Step 1: Schema",
					Notes:          "Define schema",
					Priority:       "high",
					MoveBlockerIDs: []string{blk.ID},
					CopyRepoLinks:  true,
				},
				{
					Title:        "Step 2: Handler",
					AfterIndices: []int{0},
				},
			},
		})
		if err != nil {
			t.Fatalf("splitTodo: %v", err)
		}
		if res.SourceID != src.ID || len(res.Children) != 2 || res.MovedBlockers != 1 {
			t.Fatalf("unexpected split response: %+v", res)
		}

		c0, ok := s.todoNow(res.Children[0].ID)
		if !ok {
			t.Fatalf("child 0 missing")
		}
		if c0.PartOf != src.ID || c0.DirectionID != dirID || c0.Project != proj || c0.ItemType != itemType || c0.Priority != "high" {
			t.Fatalf("child 0 metadata mismatch: %+v", c0)
		}

		c1, ok := s.todoNow(res.Children[1].ID)
		if !ok {
			t.Fatalf("child 1 missing")
		}
		if c1.PartOf != src.ID || !slices.Equal(c1.After, []string{c0.ID}) {
			t.Fatalf("child 1 after mismatch: part_of=%q after=%v", c1.PartOf, c1.After)
		}

		// Mirrored todo_done blocker should exist on child 1.
		c1Blockers, err := st.ListTodoBlockersByTodo(ctx, c1.ID)
		if err != nil {
			t.Fatalf("ListTodoBlockersByTodo(c1): %v", err)
		}
		if len(c1Blockers) != 1 || c1Blockers[0].Kind != todoBlockerKindTodoDone || c1Blockers[0].TargetRef != c0.ID || c1Blockers[0].State != db.TodoBlockerStateActive {
			t.Fatalf("expected mirrored todo_done blocker on c1, got %+v", c1Blockers)
		}

		// Moved blocker should be active on child 0 and cancelled on source.
		c0Blockers, _ := st.ListTodoBlockersByTodo(ctx, c0.ID)
		if len(c0Blockers) != 1 || c0Blockers[0].Kind != todoBlockerKindExternalPR || c0Blockers[0].State != db.TodoBlockerStateActive {
			t.Fatalf("expected moved external_pr blocker on c0, got %+v", c0Blockers)
		}
		srcBlockers, _ := st.ListTodoBlockersByTodo(ctx, src.ID)
		if len(srcBlockers) != 1 || srcBlockers[0].State != db.TodoBlockerStateCancelled {
			t.Fatalf("expected source blocker cancelled, got %+v", srcBlockers)
		}

		// Copied repo link on child 0 must have reference role, preserving source delivery role.
		c0Set, _ := st.TodoRepositories(ctx, c0.ID)
		if len(c0Set.Repositories) != 1 || len(c0Set.Repositories[0].Links) != 1 || c0Set.Repositories[0].Links[0].Role != repository.LinkRoleReference {
			t.Fatalf("expected reference repo link on c0, got %+v", c0Set)
		}
		srcSet, _ := st.TodoRepositories(ctx, src.ID)
		if srcSet.Repositories[0].Links[0].Role != repository.LinkRoleDelivery {
			t.Fatalf("source delivery link role mutated: %+v", srcSet)
		}

		// Source notes should include provenance snippet listing created child IDs.
		srcAfter, _ := s.todoNow(src.ID)
		if !strings.Contains(srcAfter.Notes, c0.ID) || !strings.Contains(srcAfter.Notes, c1.ID) {
			t.Fatalf("expected audit note with child IDs on source, got %q", srcAfter.Notes)
		}
	})

	t.Run("split_refuses_when_todos_v2_false_with_zero_side_effects", func(t *testing.T) {
		s, _ := newTopologyTestEnv(t, false)
		src, err := s.createPlainTodo(ctx, "Legacy parent", "Keep untouched", db.TodoEdit{}, nil)
		if err != nil {
			t.Fatalf("create source: %v", err)
		}
		w := doTopologyHTTP(s, "", http.MethodPost, "/api/v1/todos/"+src.ID+"/split", `{"children":[{"title":"Child A"}]}`)
		if w.Code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict when todos_v2=false, got %d: %s", w.Code, w.Body.String())
		}
		s.todos.mu.RLock()
		count := len(s.todos.todos)
		s.todos.mu.RUnlock()
		if count != 1 {
			t.Fatalf("expected zero children created when todos_v2=false, got %d todos", count)
		}
	})

	t.Run("split_rejects_cyclic_after_indices", func(t *testing.T) {
		s, _ := newTopologyTestEnv(t, true)
		src, _ := s.createPlainTodo(ctx, "Cyclic split", "", db.TodoEdit{}, nil)
		if _, err := s.splitTodo(ctx, src.ID, todoSplitRequest{
			Children: []todoSplitChildInput{
				{Title: "A", AfterIndices: []int{1}},
				{Title: "B", AfterIndices: []int{0}},
			},
		}); err == nil {
			t.Fatal("expected cycle error in after_indices")
		}
	})
}

func TestTodosV2TopologyMerge(t *testing.T) {
	ctx := context.Background()

	t.Run("merge_transfers_links_blockers_reparents_children_rewrites_after_and_closes_source", func(t *testing.T) {
		s, st := newTopologyTestEnv(t, true)
		target, err := s.createPlainTodo(ctx, "Primary goal", "Target notes", db.TodoEdit{}, nil)
		if err != nil {
			t.Fatalf("create target: %v", err)
		}
		source, err := s.createPlainTodo(ctx, "Duplicate goal", "Source details", db.TodoEdit{}, nil)
		if err != nil {
			t.Fatalf("create source: %v", err)
		}
		child, err := s.createPlainTodo(ctx, "Source child", "", db.TodoEdit{PartOf: &source.ID}, nil)
		if err != nil {
			t.Fatalf("create child: %v", err)
		}
		// Another sibling waiting on source.
		root, err := s.createPlainTodo(ctx, "Common root", "", db.TodoEdit{}, nil)
		if err != nil {
			t.Fatalf("create root: %v", err)
		}
		// Attach downstream todo waiting on source directly in book (simulating open todo with After=[source.ID]).
		downstream, err := s.createPlainTodo(ctx, "Downstream work", "", db.TodoEdit{PartOf: &root.ID}, nil)
		if err != nil {
			t.Fatalf("create downstream: %v", err)
		}
		s.todos.mu.Lock()
		s.todos.todos[downstream.ID].After = []string{source.ID}
		s.todos.mu.Unlock()
		s.syncAfterBlockers(ctx, downstream.ID, []string{source.ID}, "", nil)

		// Repo links: shared link (should deduplicate) + unique source link (should transfer).
		sharedPR := "https://github.com/example/persona/pull/200"
		sourcePR := "https://github.com/example/persona/pull/201"
		st.seedRepoLink(target.ID, repository.TaskLink{Kind: "pull_request", Number: "200", CanonicalURL: sharedPR, Role: repository.LinkRoleDelivery})
		st.seedRepoLink(source.ID, repository.TaskLink{Kind: "pull_request", Number: "200", CanonicalURL: sharedPR, Role: repository.LinkRoleDelivery})
		st.seedRepoLink(source.ID, repository.TaskLink{Kind: "pull_request", Number: "201", CanonicalURL: sourcePR, Role: repository.LinkRoleDelivery})

		// Blockers on source: one external PR (transfer), one self-blocker on target (skip), one duplicate of target's blocker (dedup).
		if _, err := s.AddTodoBlocker(ctx, target.ID, todoBlockerKindExternalPR, "https://github.com/example/persona/pull/300", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddTodoBlocker(ctx, source.ID, todoBlockerKindExternalPR, "https://github.com/example/persona/pull/300", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddTodoBlocker(ctx, source.ID, todoBlockerKindExternalPR, "https://github.com/example/persona/pull/301", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddTodoBlocker(ctx, source.ID, todoBlockerKindTodoDone, target.ID, "{}"); err != nil {
			t.Fatal(err)
		}

		res, err := s.mergeTodos(ctx, target.ID, todoMergeRequest{SourceID: source.ID})
		if err != nil {
			t.Fatalf("mergeTodos: %v", err)
		}
		if res.TargetID != target.ID || res.SourceID != source.ID {
			t.Fatalf("unexpected ids in merge response: %+v", res)
		}
		if !slices.Equal(res.ReparentedChildren, []string{child.ID}) {
			t.Fatalf("expected reparented [%s], got %v", child.ID, res.ReparentedChildren)
		}
		if res.TransferredLinks != 1 {
			t.Fatalf("expected 1 new transferred link (deduped), got %d", res.TransferredLinks)
		}
		if res.TransferredBlockers != 1 {
			t.Fatalf("expected 1 new transferred blocker (deduped + self skipped), got %d", res.TransferredBlockers)
		}

		// Verify child reparented.
		childAfter, _ := s.todoNow(child.ID)
		if childAfter.PartOf != target.ID {
			t.Fatalf("expected child PartOf=%q, got %q", target.ID, childAfter.PartOf)
		}

		// Verify downstream After rewritten from source -> target and mirrored blocker updated.
		downAfter, _ := s.todoNow(downstream.ID)
		if !slices.Equal(downAfter.After, []string{target.ID}) {
			t.Fatalf("expected downstream After=[%s], got %v", target.ID, downAfter.After)
		}
		downBlockers, _ := st.ListTodoBlockersByTodo(ctx, downstream.ID)
		hasTargetBlocker := false
		for _, b := range downBlockers {
			if b.State == db.TodoBlockerStateActive && b.Kind == todoBlockerKindTodoDone && b.TargetRef == target.ID {
				hasTargetBlocker = true
			}
			if b.State == db.TodoBlockerStateActive && b.TargetRef == source.ID {
				t.Fatalf("stale active blocker on source remained: %+v", b)
			}
		}
		if !hasTargetBlocker {
			t.Fatalf("expected active mirrored blocker targeting %s, got %+v", target.ID, downBlockers)
		}

		// Verify target has provenance notes and 2 repo links.
		targetAfter, _ := s.todoNow(target.ID)
		if !strings.Contains(targetAfter.Notes, source.ID) || !strings.Contains(targetAfter.Notes, "Source details") {
			t.Fatalf("expected provenance note on target, got %q", targetAfter.Notes)
		}
		targetSet, _ := st.TodoRepositories(ctx, target.ID)
		if len(targetSet.Repositories) != 1 || len(targetSet.Repositories[0].Links) != 2 {
			t.Fatalf("expected 2 deduplicated repo links on target, got %+v", targetSet)
		}

		// Verify source is cancelled with merge reason and has no active blockers.
		sourceAfter, _ := s.todoNow(source.ID)
		if sourceAfter.DoneAt == nil || !sourceAfter.Cancelled() || sourceAfter.DoneReason != "merged into "+target.ID {
			t.Fatalf("expected source cancelled with reason 'merged into %s', got done_at=%v done_by=%q reason=%q",
				target.ID, sourceAfter.DoneAt, sourceAfter.DoneBy, sourceAfter.DoneReason)
		}
		sourceBlockers, _ := st.ListTodoBlockersByTodo(ctx, source.ID)
		for _, b := range sourceBlockers {
			if b.State == db.TodoBlockerStateActive {
				t.Fatalf("expected all source blockers cancelled, found active: %+v", b)
			}
		}
	})

	t.Run("merge_refuses_active_agent_without_force_and_allows_with_force", func(t *testing.T) {
		s, _ := newTopologyTestEnv(t, true)
		target, _ := s.createPlainTodo(ctx, "Target", "", db.TodoEdit{}, nil)
		source, _ := s.createPlainTodo(ctx, "Source with active agent", "", db.TodoEdit{}, nil)
		s.todos.mu.Lock()
		s.todos.todos[source.ID].Agents = []TodoAgent{{
			MachineID: "m1",
			SessionID: "sess_live",
			LinkedAt:  time.Now().UTC(),
		}}
		s.todos.mu.Unlock()

		w := doTopologyHTTP(s, "", http.MethodPost, "/api/v1/todos/"+target.ID+"/merge", `{"source_id":"`+source.ID+`"}`)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "active agent") {
			t.Fatalf("expected 409 active agent refusal, got %d: %s", w.Code, w.Body.String())
		}
		srcCheck, _ := s.todoNow(source.ID)
		if srcCheck.DoneAt != nil {
			t.Fatal("source should remain open after refused merge")
		}

		// With force_with_active_agent=true it succeeds.
		w = doTopologyHTTP(s, "", http.MethodPost, "/api/v1/todos/"+target.ID+"/merge", `{"source_id":"`+source.ID+`","force_with_active_agent":true}`)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 with force_with_active_agent=true, got %d: %s", w.Code, w.Body.String())
		}
		srcAfter, _ := s.todoNow(source.ID)
		if srcAfter.DoneAt == nil || !srcAfter.Cancelled() {
			t.Fatalf("expected source closed after forced merge, got %+v", srcAfter)
		}
	})

	t.Run("merge_rejects_part_of_cycle", func(t *testing.T) {
		s, _ := newTopologyTestEnv(t, true)
		parent, _ := s.createPlainTodo(ctx, "Parent", "", db.TodoEdit{}, nil)
		child, _ := s.createPlainTodo(ctx, "Child", "", db.TodoEdit{PartOf: &parent.ID}, nil)

		// Merging parent (source) into child (target) would create a cycle.
		w := doTopologyHTTP(s, "", http.MethodPost, "/api/v1/todos/"+child.ID+"/merge", `{"source_id":"`+parent.ID+`"}`)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "cycle") {
			t.Fatalf("expected 409 cycle refusal, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("merge_refuses_when_todos_v2_false", func(t *testing.T) {
		s, _ := newTopologyTestEnv(t, false)
		target, _ := s.createPlainTodo(ctx, "Target", "", db.TodoEdit{}, nil)
		source, _ := s.createPlainTodo(ctx, "Source", "", db.TodoEdit{}, nil)

		w := doTopologyHTTP(s, "", http.MethodPost, "/api/v1/todos/"+target.ID+"/merge", `{"source_id":"`+source.ID+`"}`)
		if w.Code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict when todos_v2=false, got %d: %s", w.Code, w.Body.String())
		}
		srcCheck, _ := s.todoNow(source.ID)
		if srcCheck.DoneAt != nil {
			t.Fatal("source must remain open when todos_v2=false")
		}
	})
}

func TestTodosV2TopologyHTTPAndToolDispatch(t *testing.T) {
	ctx := context.Background()

	t.Run("readonly_role_gets_403_on_split_and_merge", func(t *testing.T) {
		s, _ := newTopologyTestEnv(t, true)
		a, _ := s.createPlainTodo(ctx, "Todo A", "", db.TodoEdit{}, nil)
		b, _ := s.createPlainTodo(ctx, "Todo B", "", db.TodoEdit{}, nil)

		wSplit := doTopologyHTTP(s, "read", http.MethodPost, "/api/v1/todos/"+a.ID+"/split", `{"children":[{"title":"C1"}]}`)
		if wSplit.Code != http.StatusForbidden {
			t.Fatalf("expected 403 on readonly split, got %d: %s", wSplit.Code, wSplit.Body.String())
		}
		wMerge := doTopologyHTTP(s, "read", http.MethodPost, "/api/v1/todos/"+a.ID+"/merge", `{"source_id":"`+b.ID+`"}`)
		if wMerge.Code != http.StatusForbidden {
			t.Fatalf("expected 403 on readonly merge, got %d: %s", wMerge.Code, wMerge.Body.String())
		}
	})

	t.Run("mcp_tools_gated_and_dispatched", func(t *testing.T) {
		s, _ := newTopologyTestEnv(t, false)
		hasTool := func(name string) bool {
			for _, tl := range s.Tools(ctx) {
				if tl.Name == name {
					return true
				}
			}
			return false
		}
		if hasTool("todo_split") || hasTool("todo_merge") {
			t.Fatal("todo_split and todo_merge must not be advertised when todos_v2=false")
		}

		src, _ := s.createPlainTodo(ctx, "Source", "", db.TodoEdit{}, nil)
		tgt, _ := s.createPlainTodo(ctx, "Target", "", db.TodoEdit{}, nil)

		splitArgs, _ := json.Marshal(map[string]any{
			"id":       src.ID,
			"children": []map[string]any{{"title": "Sub 1"}},
		})
		if _, err := s.runTool(ctx, "m1", "conv1", "todo_split", splitArgs); err == nil || !strings.Contains(err.Error(), "todos_v2") {
			t.Fatalf("expected todos_v2 error on todo_split when off, got: %v", err)
		}
		mergeArgs, _ := json.Marshal(map[string]any{
			"target_id": tgt.ID,
			"source_id": src.ID,
		})
		if _, err := s.runTool(ctx, "m1", "conv1", "todo_merge", mergeArgs); err == nil || !strings.Contains(err.Error(), "todos_v2") {
			t.Fatalf("expected todos_v2 error on todo_merge when off, got: %v", err)
		}

		// Enable todos_v2 and verify tools are advertised and succeed.
		setBlockersTodosV2Flag(t, s, true, false)
		if !hasTool("todo_split") || !hasTool("todo_merge") {
			t.Fatal("todo_split and todo_merge must be advertised when todos_v2=true")
		}

		outSplit, err := s.runTool(ctx, "m1", "conv1", "todo_split", splitArgs)
		if err != nil {
			t.Fatalf("todo_split tool: %v", err)
		}
		var splitRes todoSplitResponse
		if err := json.Unmarshal([]byte(outSplit), &splitRes); err != nil || len(splitRes.Children) != 1 {
			t.Fatalf("unexpected todo_split output: %s (err=%v)", outSplit, err)
		}

		outMerge, err := s.runTool(ctx, "m1", "conv1", "todo_merge", mergeArgs)
		if err != nil {
			t.Fatalf("todo_merge tool: %v", err)
		}
		var mergeRes todoMergeResponse
		if err := json.Unmarshal([]byte(outMerge), &mergeRes); err != nil || len(mergeRes.ReparentedChildren) != 1 {
			t.Fatalf("unexpected todo_merge output: %s (err=%v)", outMerge, err)
		}
	})
}
