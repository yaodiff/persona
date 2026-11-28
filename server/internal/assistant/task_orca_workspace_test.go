package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestPlannedOrcaTaskGroupsWorkersAndClosesAfterAcceptance(t *testing.T) {
	s, f := taskHerdrHub(t, nil)
	f.mu.Lock()
	f.devices = []Device{
		{ID: "server", Name: "server", Home: "/home/me", Online: true, Reachable: true},
		{ID: "box", Name: "box", Home: "/home/me", Online: true, Reachable: true, Engine: proto.EngineOrca},
	}
	f.mu.Unlock()
	leader := startTaskLeader(t, s, f)
	if space, ok := s.taskSpaceOf(t.Context(), leader.ID); !ok || space.Host != proto.EngineOrca || space.ID != taskHerdrWorkspaceID {
		t.Fatalf("leader's recorded Orca placement: %+v", space)
	}
	writer := planWorker(t, s, f, nil)
	reader := planWorker(t, s, f, map[string]any{"title": "Review the changes", "readonly": true, "permission": "plan"})
	starts := f.started()
	create := taskWorktreeCreate(f)
	if len(starts) != 3 || create["open_workspace"] != false || starts[1]["cwd"] != create["path"] {
		t.Fatalf("task checkout: starts=%v create=%v", starts, create)
	}
	for _, p := range starts[1:] {
		if p["task_id"] != leader.ID || p["workspace_id"] != taskHerdrWorkspaceID {
			t.Fatalf("worker did not join the leader: %v", p)
		}
	}
	ctx := t.Context()
	for _, child := range []Todo{writer, reader} {
		accept, _ := json.Marshal(map[string]any{"todo_id": child.ID, "verdict": "accepted",
			"note": "Verified", "checks": completeReviewChecks(todoNow(s, child.ID))})
		if r := s.mcpCall(ctx, "box", "s1", "task_review", accept, false); r.IsError {
			t.Fatal(r)
		}
	}
	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": leader.ID,
		"verdict": "accepted", "note": "Verified", "checks": completeReviewChecks(todoNow(s, leader.ID))}); err != nil {
		t.Fatal(err)
	}
	f.setSessions("box", taskCloseSession(writer.Agents[0].SessionID, true, false))
	s.closeDoneTaskSpaces(ctx, time.Now(), doneSeen{})
	if len(taskWorkspaceCloses(f)) != 0 {
		t.Fatal("closed while the task still had a live Agent")
	}
	user := taskCloseSession("unrelated", true, false)
	user.WorkspaceID = taskHerdrWorkspaceID
	f.setSessions("box", user)
	f.answerNode(deviceWorkspaceClose, func(string, json.RawMessage) (any, error) {
		return map[string]any{"closed": true}, nil
	})
	s.closeDoneTaskSpaces(ctx, time.Now(), doneSeen{})
	closes := taskWorkspaceCloses(f)
	if len(closes) != 1 || closes[0]["host"] != proto.EngineOrca || closes[0]["task_id"] != leader.ID || closes[0]["workspace_id"] != taskHerdrWorkspaceID {
		t.Fatalf("Orca task cleanup: %v", closes)
	}
}

func TestOrcaLeaderOnHerdrDefaultRecoversPlacementBeforeFirstWorker(t *testing.T) {
	s, f := taskHerdrHub(t, nil)
	ctx := t.Context()
	// The ordinary plan starts on a herdr-default machine but explicitly selects Orca.
	var plan struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(leaderPlanArgs(roleLeader), &plan); err != nil {
		t.Fatal(err)
	}
	plan.Tasks[0]["host"], plan.Tasks[0]["workspace"] = proto.EngineOrca, "new"
	args, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	f.onStart = func(f *fakeHub, params map[string]any) {
		f.failStart = nil
		if len(f.starts) == 1 {
			// The node creates the requested session, but the leader's start reply is lost.
			sid, _ := params["session_id"].(string)
			if sid == "" {
				sid = "leader-session"
			}
			f.sessions["box"] = []proto.Session{{SessionID: sid, MachineID: "box", Cwd: params["cwd"].(string),
				Launcher: "claude", Agent: "claude", Status: proto.StatusWorking, UpdatedAt: time.Now(), StartedByPersona: true}}
			f.failStart = context.DeadlineExceeded
		}
	}
	result, err := s.toolTaskPlan(ctx, "conv1", args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.startPlan(ctx, planIDRe.FindString(result), nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "leader link without a placement reply", func() bool { return len(onlyTodo(s).Agents) == 1 })
	leader := onlyTodo(s)
	space, ok := s.taskSpaceOf(ctx, leader.ID)
	if !ok || space.Host != proto.EngineOrca || space.ID != "" {
		t.Fatalf("pending group: %+v", space)
	}
	create := taskWorktreeCreate(f)
	if create["kind"] != proto.EngineOrca || create["open_workspace"] == false || f.started()[0]["host"] != proto.EngineOrca {
		t.Fatalf("leader checkout and terminal: %v, %v", create, f.started())
	}
	f.mu.Lock()
	for _, call := range f.worktrees {
		if call["kind"] != proto.EngineOrca {
			t.Errorf("worktree call reached the default engine: %v", call)
		}
	}
	f.mu.Unlock()
	sid := leader.Agents[0].SessionID
	f.answerNode("session.info", func(string, json.RawMessage) (any, error) {
		return map[string]string{"task_id": "another-task", "task_workspace_id": "another-worktree"}, nil
	})
	response := s.mcpCall(ctx, "box", sid, "task_plan", workerArgs(map[string]any{
		"title": "Read the changes", "readonly": true, "permission": "plan",
	}), false)
	if response.IsError {
		t.Fatal(response)
	}
	waitFor(t, "worker refused without the leader's receipt", func() bool {
		for _, child := range todosOpenedBy(s, leaderRef("box", sid)) {
			if child.StartError != "" {
				return true
			}
		}
		return false
	})
	if len(f.started()) != 1 {
		t.Fatal("worker started in an unconfirmed workspace")
	}
	f.answerNode("session.info", func(_ string, raw json.RawMessage) (any, error) {
		var in struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		if in.SessionID != sid {
			return map[string]string{}, nil
		}
		return map[string]string{"task_id": leader.ID, "task_workspace_id": taskHerdrWorkspaceID}, nil
	})
	response = s.mcpCall(ctx, "box", sid, "task_plan", workerArgs(nil), false)
	if response.IsError {
		t.Fatal(response)
	}
	waitFor(t, "worker linked after checking the leader's placement", func() bool {
		for _, child := range todosOpenedBy(s, leaderRef("box", sid)) {
			if len(child.Agents) == 1 {
				return true
			}
		}
		return false
	})
	starts := f.started()
	workerCheckout := taskWorktreeCreate(f)
	if len(starts) != 2 || starts[1]["host"] != proto.EngineOrca || starts[1]["workspace_id"] != taskHerdrWorkspaceID ||
		starts[1]["task_id"] != leader.ID || workerCheckout["kind"] != proto.EngineOrca || workerCheckout["open_workspace"] != false {
		t.Fatalf("worker placement: starts=%v checkout=%v", starts, workerCheckout)
	}
}
