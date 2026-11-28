package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
)

func reorganizeRequest(s *Service, role, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/todos/reorganize", strings.NewReader(body))
	if role != "" {
		r.Header.Set(RoleHeader, role)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestTodoReorganizeAllStatesAndValidation(t *testing.T) {
	ctx := context.Background()
	s, f := transferEnv(t)
	setBlockersTodosV2Flag(t, s, true, false)

	// Active leader directions: lead0001, lead0002 (from transferEnv).
	// Create to-dos across all states:
	// 1. Goal parent (plain open to-do)
	goal, err := s.createTodo(ctx, "Parent goal", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// 2. Personal reminder (Origin == "", no agents, no supervision)
	personal, err := s.createTodo(ctx, "Personal reminder", "buy coffee", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// 3. Unstarted supervised task (Origin != "", no active agent, supervised by lead0001)
	unstarted, err := s.createTodo(ctx, "Unstarted task", "", "lead0001", "plan_u1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, unstarted.ID, "lead0001", []string{"ready"}, 0); err != nil {
		t.Fatal(err)
	}
	// 4. Running supervised task with an active Agent
	running, err := s.createTodo(ctx, "Running supervised task", "", "lead0001", "plan_r1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, running.ID, "box", "leadw001", "Lead Worker", "/repo", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, running.ID, "lead0001", []string{"tests pass"}, 0); err != nil {
		t.Fatal(err)
	}
	// 5. Completed supervised task (DoneAt != nil)
	completed, err := s.createTodo(ctx, "Completed task", "", "lead0001", "plan_c1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, completed.ID, "lead0001", []string{"shipped"}, 0); err != nil {
		t.Fatal(err)
	}
	done := true
	if _, err := s.editTodo(ctx, completed.ID, todoDoneEdit(done)); err != nil {
		t.Fatal(err)
	}

	// Read-only / non-full roles must be rejected with 403.
	for _, role := range []string{"read", "watch", "answer"} {
		w := reorganizeRequest(s, role, fmt.Sprintf(`{"ids":[%q],"direction_id":"dir_a"}`, personal.ID))
		if w.Code != http.StatusForbidden {
			t.Fatalf("role %q: got %d, want 403: %s", role, w.Code, w.Body.String())
		}
	}

	// Validation errors: empty ids, >100 ids, missing operation, missing part_of target -> 400.
	if w := reorganizeRequest(s, "full", `{"ids":[],"direction_id":"dir_a"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty ids: got %d, want 400", w.Code)
	}
	if w := reorganizeRequest(s, "full", `{"ids":["  ",""],"direction_id":"dir_a"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("blank ids: got %d, want 400", w.Code)
	}
	tooMany := make([]string, maxReorganizeIDs+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("todo_%03d", i)
	}
	rawTooMany, _ := json.Marshal(map[string]any{"ids": tooMany, "direction_id": "dir_a"})
	if w := reorganizeRequest(s, "full", string(rawTooMany)); w.Code != http.StatusBadRequest {
		t.Fatalf("too many ids: got %d, want 400", w.Code)
	}
	if w := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q]}`, personal.ID)); w.Code != http.StatusBadRequest {
		t.Fatalf("no fields: got %d, want 400", w.Code)
	}
	if w := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"part_of":"todo_nonexistent"}`, personal.ID)); w.Code != http.StatusBadRequest {
		t.Fatalf("missing part_of target: got %d, want 400: %s", w.Code, w.Body.String())
	}

	// Non-existent ID in ids -> 404 without mutating valid IDs in the same request.
	w := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q,"todo_missing"],"direction_id":"dir_should_not_apply"}`, personal.ID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing id: got %d, want 404: %s", w.Code, w.Body.String())
	}
	if got, _ := s.todoNow(personal.ID); got.DirectionID != "" {
		t.Fatalf("partial write leaked on 404: direction_id=%q", got.DirectionID)
	}

	// Self-loop -> 400.
	if w := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"part_of":%q}`, goal.ID, goal.ID)); w.Code != http.StatusBadRequest {
		t.Fatalf("self loop: got %d, want 400: %s", w.Code, w.Body.String())
	}

	// Batch attach all 4 states under goal + assign direction_id (with duplicates in ids deduped).
	// Default transfer_supervision=false must NOT change supervision.supervisor.
	sentBefore := f.sent()
	body, _ := json.Marshal(map[string]any{
		"ids":          []string{personal.ID, unstarted.ID, running.ID, completed.ID, running.ID},
		"part_of":      goal.ID,
		"direction_id": "lead0002",
	})
	w = reorganizeRequest(s, "full", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("batch reorganize: got %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp todoReorganizeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Updated) != 4 {
		t.Fatalf("updated len = %d, want 4", len(resp.Updated))
	}
	if len(resp.TransferredIDs) != 0 {
		t.Fatalf("default transfer_supervision should not transfer, got %v", resp.TransferredIDs)
	}
	for _, id := range []string{personal.ID, unstarted.ID, running.ID, completed.ID} {
		got, ok := s.todoNow(id)
		if !ok {
			t.Fatalf("todo %s missing", id)
		}
		if got.PartOf != goal.ID || got.DirectionID != "lead0002" {
			t.Fatalf("todo %s: part_of=%q direction_id=%q, want %q / %q", id, got.PartOf, got.DirectionID, goal.ID, "lead0002")
		}
	}

	// Assert running task's active agent, activeBySession, origin, and supervision.supervisor are untouched,
	// and no messages were sent to the worker agent.
	gotRunning, _ := s.todoNow(running.ID)
	if gotRunning.Origin != "lead0001" {
		t.Fatalf("running origin mutated: %q", gotRunning.Origin)
	}
	if gotRunning.Supervision == nil || gotRunning.Supervision.Supervisor != "lead0001" {
		t.Fatalf("default reorganize mutated supervision: %+v", gotRunning.Supervision)
	}
	if len(gotRunning.Agents) != 1 || gotRunning.Agents[0].SessionID != "leadw001" || gotRunning.Agents[0].ReleasedAt != nil {
		t.Fatalf("running agent mutated: %+v", gotRunning.Agents)
	}
	s.todos.mu.RLock()
	activeTodo := s.todos.activeBySession[[2]string{"box", "leadw001"}]
	s.todos.mu.RUnlock()
	if activeTodo != running.ID {
		t.Fatalf("activeBySession mutated: got %q, want %q", activeTodo, running.ID)
	}
	if f.sent() != sentBefore {
		t.Fatalf("reorganize sent unexpected agent messages: before=%d after=%d", sentBefore, f.sent())
	}

	// Completed task remains completed and supervision untouched.
	gotCompleted, _ := s.todoNow(completed.ID)
	if gotCompleted.DoneAt == nil {
		t.Fatal("completed task was unexpectedly reopened")
	}
	if gotCompleted.Supervision == nil || gotCompleted.Supervision.Supervisor != "lead0001" {
		t.Fatalf("completed supervision mutated: %+v", gotCompleted.Supervision)
	}

	// Multi-hop cycle detection: goal <- personal <- leaf; trying to attach goal under personal (2-hop) or leaf (3-hop) -> 400.
	leaf, err := s.createTodo(ctx, "Leaf item", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if w := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"part_of":%q}`, leaf.ID, personal.ID)); w.Code != http.StatusOK {
		t.Fatalf("attach leaf under personal: got %d: %s", w.Code, w.Body.String())
	}
	if w := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"part_of":%q}`, goal.ID, personal.ID)); w.Code != http.StatusBadRequest {
		t.Fatalf("2-hop cycle: got %d, want 400: %s", w.Code, w.Body.String())
	}
	if w := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"part_of":%q}`, goal.ID, leaf.ID)); w.Code != http.StatusBadRequest {
		t.Fatalf("3-hop cycle: got %d, want 400: %s", w.Code, w.Body.String())
	}

	// Detach personal from goal (part_of: "") and clear direction_id ("").
	if w := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"part_of":"","direction_id":""}`, personal.ID)); w.Code != http.StatusOK {
		t.Fatalf("detach personal: got %d: %s", w.Code, w.Body.String())
	}
	if got, _ := s.todoNow(personal.ID); got.PartOf != "" || got.DirectionID != "" {
		t.Fatalf("detached personal: part_of=%q direction_id=%q, want empty", got.PartOf, got.DirectionID)
	}
}

func TestTodoReorganizeIncludeChildrenSubtreeAndTransferSupervision(t *testing.T) {
	ctx := context.Background()
	s, f := transferEnv(t)
	setBlockersTodosV2Flag(t, s, true, false)

	// Build a 3-level subtree: root -> childRunning (active supervised) -> grandPersonal (personal)
	// plus root -> childCompleted (completed supervised)
	root, err := s.createTodo(ctx, "Initiative root", "", "lead0001", "plan_root")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, root.ID, "lead0001", []string{"root done"}, 0); err != nil {
		t.Fatal(err)
	}

	childRunning, err := s.createTodo(ctx, "Child running task", "", "lead0001", "plan_child")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, childRunning.ID, "box", "leadw001", "Lead Worker", "/repo", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, childRunning.ID, "lead0001", []string{"child done"}, 0); err != nil {
		t.Fatal(err)
	}

	grandPersonal, err := s.createTodo(ctx, "Grandchild personal", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	childCompleted, err := s.createTodo(ctx, "Child completed task", "", "lead0001", "plan_done")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, childCompleted.ID, "lead0001", []string{"shipped"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.editTodo(ctx, childCompleted.ID, todoDoneEdit(true)); err != nil {
		t.Fatal(err)
	}

	// Link parent -> child -> grandchild via reorganize.
	if w := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q,%q],"part_of":%q}`, childRunning.ID, childCompleted.ID, root.ID)); w.Code != http.StatusOK {
		t.Fatalf("attach children: %d %s", w.Code, w.Body.String())
	}
	if w := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"part_of":%q}`, grandPersonal.ID, childRunning.ID)); w.Code != http.StatusOK {
		t.Fatalf("attach grandchild: %d %s", w.Code, w.Body.String())
	}

	// 1. Without include_children, only root's direction_id changes.
	if w := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"direction_id":"dir_only_root"}`, root.ID)); w.Code != http.StatusOK {
		t.Fatalf("root only: %d %s", w.Code, w.Body.String())
	}
	if got, _ := s.todoNow(root.ID); got.DirectionID != "dir_only_root" {
		t.Fatalf("root direction_id=%q", got.DirectionID)
	}
	if got, _ := s.todoNow(childRunning.ID); got.DirectionID != "" {
		t.Fatalf("child should not change when include_children=false, got %q", got.DirectionID)
	}

	// 2. With include_children=true and transfer_supervision=true targeting a non-leader direction_id:
	// all 4 nodes get direction_id, zero supervision transfers occur, no error.
	w := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"direction_id":"dir_non_leader","include_children":true,"transfer_supervision":true}`, root.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("non-leader subtree: %d %s", w.Code, w.Body.String())
	}
	var nonLeaderResp todoReorganizeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &nonLeaderResp); err != nil {
		t.Fatal(err)
	}
	if len(nonLeaderResp.Updated) != 4 || len(nonLeaderResp.TransferredIDs) != 0 {
		t.Fatalf("non-leader response: updated=%d transferred=%v", len(nonLeaderResp.Updated), nonLeaderResp.TransferredIDs)
	}
	for _, id := range []string{root.ID, childRunning.ID, grandPersonal.ID, childCompleted.ID} {
		if got, _ := s.todoNow(id); got.DirectionID != "dir_non_leader" {
			t.Fatalf("todo %s direction_id=%q, want dir_non_leader", id, got.DirectionID)
		}
	}

	// 3. With include_children=true and transfer_supervision=true targeting active leader lead0002:
	// all 4 nodes get direction_id="lead0002", ONLY open supervised tasks (root, childRunning) are transferred;
	// personal (grandPersonal) and completed (childCompleted) are silently skipped; running Agent is untouched.
	sentBefore := f.sent()
	w = reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"direction_id":"lead0002","include_children":true,"transfer_supervision":true}`, root.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("active leader subtree transfer: %d %s", w.Code, w.Body.String())
	}
	var transferResp todoReorganizeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &transferResp); err != nil {
		t.Fatal(err)
	}
	if len(transferResp.Updated) != 4 {
		t.Fatalf("updated len = %d, want 4", len(transferResp.Updated))
	}
	wantTransferred := []string{root.ID, childRunning.ID}
	slices.Sort(wantTransferred)
	gotTransferred := slices.Clone(transferResp.TransferredIDs)
	slices.Sort(gotTransferred)
	if !slices.Equal(gotTransferred, wantTransferred) {
		t.Fatalf("transferred_ids = %v, want %v", gotTransferred, wantTransferred)
	}

	for _, id := range []string{root.ID, childRunning.ID} {
		got, _ := s.todoNow(id)
		if got.DirectionID != "lead0002" {
			t.Fatalf("todo %s direction_id=%q, want lead0002", id, got.DirectionID)
		}
		if got.Supervision == nil || got.Supervision.Supervisor != "lead0002" || got.Supervision.SupervisorSource != "explicit" {
			t.Fatalf("todo %s supervision not transferred: %+v", id, got.Supervision)
		}
	}
	// Personal to-do has direction_id updated and no supervision created.
	if got, _ := s.todoNow(grandPersonal.ID); got.DirectionID != "lead0002" || got.Supervision != nil {
		t.Fatalf("personal grandchild unexpected state: dir=%q sup=%+v", got.DirectionID, got.Supervision)
	}
	// Completed task has direction_id updated and keeps original supervisor lead0001.
	if got, _ := s.todoNow(childCompleted.ID); got.DirectionID != "lead0002" || got.DoneAt == nil || got.Supervision == nil || got.Supervision.Supervisor != "lead0001" {
		t.Fatalf("completed child unexpected state: dir=%q done=%v sup=%+v", got.DirectionID, got.DoneAt, got.Supervision)
	}
	// Running Agent was neither interrupted nor messaged, and origin/agents stay intact.
	gotChild, _ := s.todoNow(childRunning.ID)
	if gotChild.Origin != "lead0001" || len(gotChild.Agents) != 1 || gotChild.Agents[0].SessionID != "leadw001" || gotChild.Agents[0].ReleasedAt != nil {
		t.Fatalf("childRunning origin/agents mutated: origin=%q agents=%+v", gotChild.Origin, gotChild.Agents)
	}
	if f.sent() != sentBefore {
		t.Fatalf("reorganize must never send messages to running worker agent: before=%d after=%d", sentBefore, f.sent())
	}

	// 4. Clearing direction_id with transfer_supervision=true clears direction_id across subtree without error or transfer.
	w = reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"direction_id":"","include_children":true,"transfer_supervision":true}`, root.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("clear direction_id subtree: %d %s", w.Code, w.Body.String())
	}
	var clearResp todoReorganizeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &clearResp); err != nil {
		t.Fatal(err)
	}
	if len(clearResp.TransferredIDs) != 0 {
		t.Fatalf("clearing direction_id must not transfer supervision: %v", clearResp.TransferredIDs)
	}
	for _, id := range []string{root.ID, childRunning.ID, grandPersonal.ID, childCompleted.ID} {
		if got, _ := s.todoNow(id); got.DirectionID != "" {
			t.Fatalf("todo %s direction_id=%q, want empty", id, got.DirectionID)
		}
	}
}

func TestTodoReorganizeTodosV2LosslessRollback(t *testing.T) {
	s, _, _ := briefService(t, "Reorganize todos under todos_v2 and roll back losslessly")

	req := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set(RoleHeader, "full")
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	setTodosV2 := func(want, expected bool) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"todos_v2": want, "expected_todos_v2": expected})
		w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", s.managerIdentity(), "full", string(body))
		if w.Code != http.StatusOK {
			t.Fatalf("PUT todos_v2=%t: code=%d body=%s", want, w.Code, w.Body.String())
		}
	}
	getTodos := func() (map[string]map[string]any, map[string]any) {
		t.Helper()
		w := req("GET", "/api/v1/todos", "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/todos: code=%d body=%s", w.Code, w.Body.String())
		}
		var payload struct {
			Todos  []map[string]any `json:"todos"`
			Counts map[string]any   `json:"counts"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode GET /api/v1/todos: %v", err)
		}
		byID := make(map[string]map[string]any, len(payload.Todos))
		for _, item := range payload.Todos {
			id, _ := item["id"].(string)
			byID[id] = item
		}
		return byID, payload.Counts
	}
	stable := func(v any) any {
		m := maps.Clone(v.(map[string]any))
		delete(m, "next_change_at")
		delete(m, "rank")
		return m
	}

	// Enable todos_v2, create parent + child, and reorganize via POST /api/v1/todos/reorganize.
	setTodosV2(true, false)

	ctx := context.Background()
	parent, err := s.createTodo(ctx, "Parent initiative", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.createTodo(ctx, "Child task", "", "conv1", "plan_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, child.ID, "box", "sess_live", "Worker", "/tmp", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, child.ID, "conv1", []string{"verified"}, 0); err != nil {
		t.Fatal(err)
	}

	w := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"part_of":%q,"direction_id":"dir_alpha"}`, child.ID, parent.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("reorganize: %d %s", w.Code, w.Body.String())
	}

	onRows, onCounts := getTodos()

	// Roll back todos_v2 -> false.
	setTodosV2(false, true)
	if off := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"direction_id":"dir_other"}`, child.ID)); off.Code != http.StatusConflict {
		t.Fatalf("reorganize with todos_v2 off: %d %s", off.Code, off.Body.String())
	}
	offRows, offCounts := getTodos()
	if len(offRows) != len(onRows) {
		t.Fatalf("rollback changed count: off=%d on=%d", len(offRows), len(onRows))
	}
	if !reflect.DeepEqual(offCounts, onCounts) {
		t.Fatalf("rollback changed counts: off=%v on=%v", offCounts, onCounts)
	}
	for id, onRow := range onRows {
		offRow, ok := offRows[id]
		if !ok {
			t.Fatalf("todo %s missing after rollback", id)
		}
		if !reflect.DeepEqual(stable(offRow["view"]), stable(onRow["view"])) {
			t.Fatalf("todo %s view changed on rollback: off=%v on=%v", id, offRow["view"], onRow["view"])
		}
	}

	// Re-enable todos_v2 -> true and verify part_of, direction_id, origin, active agent, and supervision survived intact.
	setTodosV2(true, false)
	gotChild, ok := s.todoNow(child.ID)
	if !ok {
		t.Fatal("child missing after re-enable")
	}
	if gotChild.PartOf != parent.ID || gotChild.DirectionID != "dir_alpha" || gotChild.Origin != "conv1" {
		t.Fatalf("child fields lost after re-enable: %+v", gotChild)
	}
	if gotChild.Supervision == nil || gotChild.Supervision.Supervisor != "conv1" {
		t.Fatalf("child supervision mutated: %+v", gotChild.Supervision)
	}
	if len(gotChild.Agents) != 1 || gotChild.Agents[0].SessionID != "sess_live" || gotChild.Agents[0].ReleasedAt != nil {
		t.Fatalf("child active agent mutated: %+v", gotChild.Agents)
	}
}

func todoDoneEdit(done bool) db.TodoEdit {
	return db.TodoEdit{Done: &done}
}
