package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func dispatchToolFrom(t testing.TB, s *Service, sessionID, name string, args any) (string, bool) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Tool(context.Background(), ToolCall{Name: name, Arguments: raw, MachineID: "server", SessionID: sessionID})
	if len(r.Content) == 0 {
		t.Fatalf("%s from %s: no answer", name, sessionID)
	}
	return r.Content[0].Text, r.IsError
}

func dispatchNewTodo(t testing.TB, s *Service, sessionID string, args any) Todo {
	t.Helper()
	before := map[string]bool{}
	s.todos.mu.RLock()
	for id := range s.todos.todos {
		before[id] = true
	}
	s.todos.mu.RUnlock()

	out, bad := dispatchToolFrom(t, s, sessionID, "task_plan", args)
	if bad {
		t.Fatalf("task_plan from %s failed: %s", sessionID, out)
	}
	waitFor(t, "the dispatch to finish", func() bool { return s.taskStarting.Load() == 0 })

	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for id, td := range s.todos.todos {
		if !before[id] {
			return copyTodoLocked(td)
		}
	}
	t.Fatalf("task_plan from %s created no new todo; output: %s", sessionID, out)
	return Todo{}
}

func dispatchTaskArgs(title string, over map[string]any) map[string]any {
	task := map[string]any{
		"title":     title,
		"prompt":    "inspect " + title + " and report results",
		"role":      "worker",
		"machine":   "box",
		"cwd":       "/code/app",
		"readonly":  true,
		"goal":      "verify " + title,
		"done_when": []string{"tests pass"},
	}
	for k, v := range over {
		if v == nil {
			delete(task, k)
		} else {
			task[k] = v
		}
	}
	return map[string]any{"tasks": []map[string]any{task}}
}

func TestTodosV2DispatchDefaultsOffBypass(t *testing.T) {
	s, _ := transferEnv(t)

	// 1. Direction leader dispatches without audience/direction_id while todos_v2=false.
	leadTodo := dispatchNewTodo(t, s, "lead0001", dispatchTaskArgs("Off leader task", nil))
	if leadTodo.Audience != "" || leadTodo.DirectionID != "" {
		t.Fatalf("todos_v2=false must bypass defaults: audience=%q direction_id=%q", leadTodo.Audience, leadTodo.DirectionID)
	}
	if leadTodo.Origin != "lead0001" || leadTodo.Supervision == nil || leadTodo.Supervision.Supervisor != "lead0001" {
		t.Fatalf("unexpected origin/supervision: origin=%q sup=%+v", leadTodo.Origin, leadTodo.Supervision)
	}

	// 2. Main dispatches with explicit supervisor=lead0001 while todos_v2=false.
	args := dispatchTaskArgs("Off delegated task", nil)
	args["supervisor"] = "lead0001"
	delegated := dispatchNewTodo(t, s, "conv1", args)
	if delegated.Audience != "" || delegated.DirectionID != "" {
		t.Fatalf("todos_v2=false delegated must bypass defaults: audience=%q direction_id=%q", delegated.Audience, delegated.DirectionID)
	}

	// 3. Main dispatches non-user-requested task while todos_v2=false.
	bgTodo := dispatchNewTodo(t, s, "conv1", dispatchTaskArgs("Off background task", map[string]any{
		"user_requested": false,
	}))
	if bgTodo.Audience != "" || bgTodo.DirectionID != "" {
		t.Fatalf("todos_v2=false background must bypass defaults: audience=%q direction_id=%q", bgTodo.Audience, bgTodo.DirectionID)
	}
}

func TestTodosV2DispatchDefaultsOnLeaderAndSupervised(t *testing.T) {
	ctx := context.Background()
	s, _ := transferEnv(t)
	setTodosV2ForTest(t, s, true, false)

	// 1. Direction leader dispatches without audience/direction_id (even with user_requested=true).
	leadTodo := dispatchNewTodo(t, s, "lead0001", dispatchTaskArgs("On leader task", map[string]any{
		"user_requested": true,
	}))
	if leadTodo.DirectionID != "lead0001" || leadTodo.Audience != db.AudienceAutonomous {
		t.Fatalf("leader defaults mismatch: direction_id=%q audience=%q, want lead0001 / %q",
			leadTodo.DirectionID, leadTodo.Audience, db.AudienceAutonomous)
	}
	if leadTodo.Origin != "lead0001" || leadTodo.Supervision == nil || leadTodo.Supervision.Supervisor != "lead0001" {
		t.Fatalf("leader origin/supervision mutated: origin=%q sup=%+v", leadTodo.Origin, leadTodo.Supervision)
	}

	// 2. Main dispatches with supervisor=lead0002 -> direction_id=lead0002, audience=autonomous, origin stays conv1.
	delegatedArgs := dispatchTaskArgs("On delegated task", nil)
	delegatedArgs["supervisor"] = "lead0002"
	delegated := dispatchNewTodo(t, s, "conv1", delegatedArgs)
	if delegated.DirectionID != "lead0002" || delegated.Audience != db.AudienceAutonomous {
		t.Fatalf("delegated defaults mismatch: direction_id=%q audience=%q, want lead0002 / %q",
			delegated.DirectionID, delegated.Audience, db.AudienceAutonomous)
	}
	if delegated.Origin != "conv1" || delegated.Supervision == nil || delegated.Supervision.Supervisor != "lead0002" {
		t.Fatalf("delegated origin/supervision mutated: origin=%q sup=%+v", delegated.Origin, delegated.Supervision)
	}

	// 3. Main dispatches non-user-requested supervised execution task -> audience=autonomous, direction_id stays empty.
	autoMain := dispatchNewTodo(t, s, "conv1", dispatchTaskArgs("On autonomous main task", map[string]any{
		"user_requested": false,
	}))
	if autoMain.DirectionID != "" || autoMain.Audience != db.AudienceAutonomous {
		t.Fatalf("main non-user-requested defaults mismatch: direction_id=%q audience=%q", autoMain.DirectionID, autoMain.Audience)
	}

	// 4. Main dispatches decomposed subtask (part_of != "") with user_requested=true -> audience=autonomous.
	goal, err := s.createTodo(ctx, "Parent initiative", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	subtask := dispatchNewTodo(t, s, "conv1", dispatchTaskArgs("On decomposed subtask", map[string]any{
		"user_requested": true,
		"part_of":        goal.ID,
	}))
	if subtask.DirectionID != "" || subtask.Audience != db.AudienceAutonomous || subtask.PartOf != goal.ID {
		t.Fatalf("subtask defaults mismatch: direction_id=%q audience=%q part_of=%q", subtask.DirectionID, subtask.Audience, subtask.PartOf)
	}

	// 5. Main dispatches single user-requested personal task (no leader, no part_of) -> audience stays empty.
	singleUser := dispatchNewTodo(t, s, "conv1", dispatchTaskArgs("On single user task", map[string]any{
		"user_requested": true,
	}))
	if singleUser.DirectionID != "" || singleUser.Audience != "" {
		t.Fatalf("single user-requested main task must not default to autonomous: direction_id=%q audience=%q",
			singleUser.DirectionID, singleUser.Audience)
	}
}

func TestTodosV2DispatchDefaultsRespectExplicitAndReusedValues(t *testing.T) {
	ctx := context.Background()
	s, _ := transferEnv(t)
	setTodosV2ForTest(t, s, true, false)

	// 1. Explicit audience="human" and explicit direction_id are never overwritten even under a direction leader.
	explicitTodo := dispatchNewTodo(t, s, "lead0001", dispatchTaskArgs("Explicit override task", map[string]any{
		"audience":     db.AudienceHuman,
		"direction_id": "dir_custom",
	}))
	if explicitTodo.Audience != db.AudienceHuman || explicitTodo.DirectionID != "dir_custom" {
		t.Fatalf("explicit values overwritten: audience=%q direction_id=%q", explicitTodo.Audience, explicitTodo.DirectionID)
	}

	// 2. Reused todo with existing audience="human" and direction_id="dir_existing" is never overwritten.
	reusedPreserved, err := s.createTodo(ctx, "Existing human todo", "", "lead0001", "")
	if err != nil {
		t.Fatal(err)
	}
	humanAud := db.AudienceHuman
	existingDir := "dir_existing"
	if _, err := s.editTodo(ctx, reusedPreserved.ID, db.TodoEdit{Audience: &humanAud, DirectionID: &existingDir}); err != nil {
		t.Fatal(err)
	}
	if out, bad := dispatchToolFrom(t, s, "lead0001", "task_plan", dispatchTaskArgs("Reuse preserved task", map[string]any{
		"todo_id": reusedPreserved.ID,
	})); bad {
		t.Fatalf("reuse preserved task_plan failed: %s", out)
	}
	waitFor(t, "the reuse dispatch to finish", func() bool { return s.taskStarting.Load() == 0 })
	gotPreserved, _ := s.todoNow(reusedPreserved.ID)
	if gotPreserved.Audience != db.AudienceHuman || gotPreserved.DirectionID != "dir_existing" {
		t.Fatalf("reused todo values overwritten: audience=%q direction_id=%q", gotPreserved.Audience, gotPreserved.DirectionID)
	}

	// 3. Reused todo with empty audience and empty direction_id gets defaults filled by leader dispatch.
	reusedBlank, err := s.createTodo(ctx, "Existing blank todo", "", "lead0001", "")
	if err != nil {
		t.Fatal(err)
	}
	if out, bad := dispatchToolFrom(t, s, "lead0001", "task_plan", dispatchTaskArgs("Reuse blank task", map[string]any{
		"todo_id": reusedBlank.ID,
	})); bad {
		t.Fatalf("reuse blank task_plan failed: %s", out)
	}
	waitFor(t, "the blank reuse dispatch to finish", func() bool { return s.taskStarting.Load() == 0 })
	gotBlank, _ := s.todoNow(reusedBlank.ID)
	if gotBlank.Audience != db.AudienceAutonomous || gotBlank.DirectionID != "lead0001" {
		t.Fatalf("reused blank todo did not receive defaults: audience=%q direction_id=%q", gotBlank.Audience, gotBlank.DirectionID)
	}
}

func TestTodosV2DispatchDefaultsAutoSeenEndToEndAndRollback(t *testing.T) {
	ctx := context.Background()
	s, f := transferEnv(t)
	now := time.Now().UTC().Truncate(time.Second)

	f.mu.Lock()
	f.sessions["box"] = append(f.sessions["box"], proto.Session{
		SessionID: "auto_e2e_w1",
		MachineID: "box",
		Cwd:       "/code/app",
		Agent:     "claude",
		Status:    proto.StatusDone,
		State:     proto.StateUnread,
		UpdatedAt: now,
	})
	f.mu.Unlock()

	setTodosV2ForTest(t, s, true, false)

	// 1. Direction leader dispatches task without specifying audience or direction_id.
	td := dispatchNewTodo(t, s, "lead0001", dispatchTaskArgs("Leader auto-seen task", map[string]any{
		"done_when": []string{"tests pass"},
	}))
	if td.DirectionID != "lead0001" || td.Audience != db.AudienceAutonomous {
		t.Fatalf("dispatched defaults mismatch: direction_id=%q audience=%q", td.DirectionID, td.Audience)
	}
	if err := s.linkTodoAgent(ctx, td.ID, "box", "auto_e2e_w1", "E2E Worker", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	beforeAccept, _ := s.todoNow(td.ID)
	origAgents := append([]TodoAgent(nil), beforeAccept.Agents...)

	// 2. Leader accepts review (no user_check) -> auto-seen triggers automatically.
	reviewPayload, _ := json.Marshal(map[string]any{
		"todo_id": td.ID,
		"verdict": reviewAccepted,
		"note":    "Verified",
		"checks":  []map[string]any{{"criterion": "tests pass", "met": true, "evidence": "ok"}},
	})
	if out, err := s.toolTaskReview(ctx, "lead0001", reviewPayload); err != nil {
		t.Fatalf("toolTaskReview: err=%v out=%s", err, out)
	}
	afterAccept, _ := s.todoNow(td.ID)
	if afterAccept.DoneAt == nil || afterAccept.SeenAt == nil || afterAccept.AutoSeenUndone {
		t.Fatalf("expected auto-seen after leader review accept: done=%v seen=%v undone=%t",
			afterAccept.DoneAt, afterAccept.SeenAt, afterAccept.AutoSeenUndone)
	}
	if s.seenAt("box", "auto_e2e_w1").IsZero() {
		t.Fatal("expected finished worker session auto_e2e_w1 to be marked seen")
	}

	// 3. DELETE /api/v1/todos/{id}/seen clears seen_at and persists AutoSeenUndone=true.
	wUndo := doTodoHTTP(s, http.MethodDelete, "/api/v1/todos/"+td.ID+"/seen", "")
	if wUndo.Code != http.StatusOK {
		t.Fatalf("DELETE /seen: code=%d body=%s", wUndo.Code, wUndo.Body.String())
	}
	afterUndo, _ := s.todoNow(td.ID)
	if afterUndo.SeenAt != nil || !afterUndo.AutoSeenUndone {
		t.Fatalf("after DELETE /seen: seen=%v undone=%t", afterUndo.SeenAt, afterUndo.AutoSeenUndone)
	}
	// Re-accepting must respect persisted AutoSeenUndone.
	if out, err := s.toolTaskReview(ctx, "lead0001", reviewPayload); err != nil {
		t.Fatalf("re-accept toolTaskReview: err=%v out=%s", err, out)
	}
	afterReaccept, _ := s.todoNow(td.ID)
	if afterReaccept.SeenAt != nil || !afterReaccept.AutoSeenUndone {
		t.Fatalf("re-accept overwrote AutoSeenUndone: seen=%v undone=%t", afterReaccept.SeenAt, afterReaccept.AutoSeenUndone)
	}

	// 4. Roll back todos_v2=false: new dispatches revert to blank defaults, existing records stay intact.
	// The rollback must change nothing of the agents, in-memory guards included; closing the to-do in step 2 already
	// set resumeInvalidatedAt on them (#17678), so the copy to compare whole is the one from just before.
	beforeRollback, _ := s.todoNow(td.ID)
	rollbackAgents := append([]TodoAgent(nil), beforeRollback.Agents...)
	setTodosV2ForTest(t, s, false, true)

	postRollback := dispatchNewTodo(t, s, "lead0001", dispatchTaskArgs("Post-rollback leader task", nil))
	if postRollback.Audience != "" || postRollback.DirectionID != "" {
		t.Fatalf("after rollback todos_v2=false must leave defaults empty: audience=%q direction_id=%q",
			postRollback.Audience, postRollback.DirectionID)
	}

	existingAfterRollback, ok := s.todoNow(td.ID)
	if !ok {
		t.Fatal("existing todo missing after rollback")
	}
	if existingAfterRollback.Origin != "lead0001" ||
		existingAfterRollback.Supervision == nil ||
		existingAfterRollback.Supervision.Supervisor != "lead0001" ||
		!reflect.DeepEqual(existingAfterRollback.Agents, rollbackAgents) ||
		agentRecords(t, existingAfterRollback.Agents) != agentRecords(t, origAgents) ||
		existingAfterRollback.DirectionID != "lead0001" ||
		existingAfterRollback.Audience != db.AudienceAutonomous ||
		!existingAfterRollback.AutoSeenUndone {
		t.Fatalf("existing todo corrupted after rollback: %+v", existingAfterRollback)
	}
}

// agentRecords is what a to-do lists of its agents, and so what the store keeps of them: their exported fields, as
// JSON. In-memory guards such as resumeInvalidatedAt (#17678) are not part of it.
func agentRecords(t *testing.T, agents []TodoAgent) string {
	t.Helper()
	b, err := json.Marshal(agents)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
