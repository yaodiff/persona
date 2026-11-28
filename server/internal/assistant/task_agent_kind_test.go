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

// startAgentKindPlanTask runs one ordinary planned start to its link and returns the resulting todo.
func startAgentKindPlanTask(t *testing.T, s *Service, task PlanTask) Todo {
	t.Helper()
	ctx := context.Background()
	todo, err := s.createTodo(ctx, task.Title, task.Prompt, "conv1", "plan_agent_kind")
	if err != nil {
		t.Fatal(err)
	}
	task.TodoID = todo.ID
	s.startPlanTask("plan_agent_kind", "conv1", 0, task, false)
	got := todoNow(s, todo.ID)
	if len(got.Agents) != 1 {
		t.Fatalf("planned task did not finish with one linked Agent: %+v", got)
	}
	return got
}

// The node's answer is authoritative even when the resolved launcher metadata says something else. Recording it must
// not add a node round trip or change the launch the plan asked for.
func TestPlannedTaskAgentKindUsesStartAnswerWithoutChangingStart(t *testing.T) {
	s, f := newTaskService(t)
	f.launcherList = []proto.Launcher{{Name: "codex", Agent: "gemini"}}
	f.startReply = func(_ map[string]any, sess *proto.Session) *proto.StartInstead {
		sess.Agent = "codex"
		return nil
	}
	// The node has reported its empty inventory. A background refresh of its todo marks can use that list
	// while this test counts every node call the planned start and link make.
	s.syncTodoTokens("box", []proto.Session{})
	tokenSendDone(t, s, "box")
	before := f.callCount()
	todo := startAgentKindPlanTask(t, s, PlanTask{
		Title: "Record the started kind", Prompt: "Inspect the regression", Machine: "box", Cwd: "/code/app",
		Readonly: true, Permission: "plan", Launcher: "codex", Model: "model-from-plan", Effort: "high",
	})
	tokenSendDone(t, s, "box")
	after := f.callCount()
	starts := f.started()
	if len(starts) != 1 {
		t.Fatalf("session.start calls = %d, want one", len(starts))
	}
	start := starts[0]
	if start["launcher"] != "codex" || start["model"] != "model-from-plan" || start["effort"] != "high" || start["permission_mode"] != "plan" {
		t.Fatalf("recording the kind changed the planned start: %+v", start)
	}
	if _, added := start["agent"]; added {
		t.Fatalf("recording the kind added a session.start parameter: %+v", start)
	}
	if got, want := after-before, 7; got != want {
		t.Fatalf("the planned start and link made %d node calls, want the existing %d", got, want)
	}
	link := todo.Agents[0]
	if link.Source != "manual" || link.Agent != "codex" {
		t.Fatalf("ordinary planned link = %+v, want the start answer's agent codex", link)
	}
}

// An older node omits session.agent. The launcher kind already resolved for this start is the fallback, not a fresh
// probe and not a guess from the launcher's name.
func TestPlannedTaskAgentKindFallsBackToResolvedLauncher(t *testing.T) {
	s, f := newTaskService(t)
	f.launcherList = []proto.Launcher{{Name: "cx", Agent: "codex"}}
	f.startReply = func(_ map[string]any, sess *proto.Session) *proto.StartInstead {
		sess.Agent = ""
		return nil
	}
	todo := startAgentKindPlanTask(t, s, PlanTask{
		Title: "Use the resolved kind", Prompt: "Inspect the regression", Machine: "box", Cwd: "/code/app",
		Readonly: true, Permission: "plan", Launcher: "cx",
	})
	if got := todo.Agents[0].Agent; got != "codex" {
		t.Fatalf("linked agent = %q, want resolved launcher kind codex when the start answer is empty", got)
	}
}

// Neither the start answer nor the launcher list identifies a kind: the snapshot stays unknown rather than inheriting
// the fake node's usual claude default or inferring one from the launcher name.
func TestPlannedTaskAgentKindStaysEmptyWhenBothSourcesAreUnknown(t *testing.T) {
	s, f := newTaskService(t)
	f.launcherList = []proto.Launcher{{Name: "mystery"}}
	f.startReply = func(_ map[string]any, sess *proto.Session) *proto.StartInstead {
		sess.Agent = ""
		return nil
	}
	todo := startAgentKindPlanTask(t, s, PlanTask{
		Title: "Keep an unknown kind", Prompt: "Inspect the regression", Machine: "box", Cwd: "/code/app",
		Readonly: true, Permission: "plan", Launcher: "mystery",
	})
	if got := todo.Agents[0].Agent; got != "" {
		t.Fatalf("linked agent = %q, want empty when neither source names a kind", got)
	}
}

// The retry used when the owner finishes a todo during its start must carry the same kind as the ordinary link.
func TestPlannedTaskAgentKindOnTaskFinishedDuringStart(t *testing.T) {
	s, f := newTaskService(t)
	f.launcherList = []proto.Launcher{{Name: "codex", Agent: "codex"}}
	f.startReply = func(_ map[string]any, sess *proto.Session) *proto.StartInstead {
		sess.Agent = "codex"
		return nil
	}
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Finished during start", "Inspect the regression", "conv1", "plan_agent_kind")
	if err != nil {
		t.Fatal(err)
	}
	f.onStart = func(_ *fakeHub, _ map[string]any) {
		s.todos.mu.Lock()
		now := time.Now().UTC()
		s.todos.todos[todo.ID].DoneAt = &now
		s.todos.mu.Unlock()
	}
	s.startPlanTask("plan_agent_kind", "conv1", 0, PlanTask{
		TodoID: todo.ID, Title: todo.Title, Prompt: "Inspect the regression", Machine: "box", Cwd: "/code/app",
		Readonly: true, Permission: "plan", Launcher: "codex",
	}, false)
	got := todoNow(s, todo.ID)
	if got.DoneAt == nil || len(got.Agents) != 1 {
		t.Fatalf("the Agent did not link to the todo finished during its start: %+v", got)
	}
	if kind := got.Agents[0].Agent; kind != "codex" {
		t.Fatalf("Agent linked to the finished todo has kind %q, want codex", kind)
	}
}

// Leaders and workers can run different launchers and models. Each row records its own start answer, not the leader's
// kind, and recording it leaves the worker's requested launcher and model alone.
func TestTaskLeaderAndWorkerAgentKindsComeFromTheirOwnStarts(t *testing.T) {
	s, f := newQuietService(t)
	f.launcherList = []proto.Launcher{{Name: "codex-leader", Agent: "codex"}, {Name: "gemini-worker", Agent: "gemini"}}
	f.startReply = func(params map[string]any, sess *proto.Session) *proto.StartInstead {
		switch params["launcher"] {
		case "codex-leader":
			sess.Agent = "codex"
		case "gemini-worker":
			sess.Agent = "gemini"
		}
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal(leaderPlanArgs("leader"), &args); err != nil {
		t.Fatal(err)
	}
	args["tasks"].([]any)[0].(map[string]any)["launcher"] = "codex-leader"
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := s.toolTaskPlan(ctx, "conv1", raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.startPlan(ctx, planIDRe.FindString(res), nil, false); err != nil {
		t.Fatalf("%s: %v", res, err)
	}
	waitFor(t, "the leader's link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	leader := onlyTodo(s)
	if a := leader.Agents[0]; a.Source != sourceLeader || a.Agent != "codex" {
		t.Errorf("leader link = %+v, want source leader and start answer codex", a)
	}

	worker := planWorker(t, s, f, map[string]any{"launcher": "gemini-worker", "model": "worker-model"})
	if a := worker.Agents[0]; a.Source != sourceWorker || a.Agent != "gemini" {
		t.Errorf("worker link = %+v, want source worker and its own start answer gemini", a)
	}
	starts := f.started()
	if len(starts) != 2 || starts[1]["launcher"] != "gemini-worker" || starts[1]["model"] != "worker-model" {
		t.Fatalf("worker launcher/model changed while recording its kind: %+v", starts)
	}
}

// A spawned child has no session row lookup to rely on later: its historical link carries the child's start answer.
func TestTaskSpawnAgentKindComesFromChildStart(t *testing.T) {
	s, f, todo := spawnFixture(t, "codex", "plan")
	f.startReply = func(_ map[string]any, sess *proto.Session) *proto.StartInstead {
		sess.Agent = "codex"
		return nil
	}
	res := s.mcpCall(context.Background(), "box", "s1", "task_spawn", json.RawMessage(spawnArgs), false)
	if res.IsError {
		t.Fatal(res.Content)
	}
	agents := todoNow(s, todo.ID).Agents
	if len(agents) != 2 {
		t.Fatalf("spawned child was not linked: %+v", agents)
	}
	child := agents[1]
	if child.Source != "spawned" || child.Agent != "codex" {
		t.Fatalf("spawned link = %+v, want source spawned and the child start's codex kind", child)
	}
}

// supervisedAgentKindReview starts an independent reviewer whose node answer reports codex.
func supervisedAgentKindReview(t *testing.T, st Store) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f, todo := newSupervisedServiceOn(t, st)
	f.mu.Lock()
	f.hello.Agents.Review = assistantwire.ReviewConfig{Launcher: "codex-review", Model: "review-model"}
	f.startReply = func(params map[string]any, sess *proto.Session) *proto.StartInstead {
		if params["launcher"] == "codex-review" {
			sess.Agent = "codex"
		}
		return nil
	}
	f.mu.Unlock()
	withReviewNode(f, &reviewNode{change: reviewChangeOf()})
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "修好了", 0)
	waitFor(t, "the reviewer's todo link", func() bool {
		run := autoReviewOf(s, todo.ID)
		if run.SessionID == "" {
			return false
		}
		for _, a := range todoNow(s, todo.ID).Agents {
			if a.Source == sourceReview && a.SessionID == run.SessionID {
				return true
			}
		}
		return false
	})
	return s, f, todo
}

// The reviewer is released from the working set but remains in the todo's in-memory history with its own kind.
func TestAutoReviewAgentKindInMemory(t *testing.T) {
	s, _, todo := supervisedAgentKindReview(t, nil)
	run := autoReviewOf(s, todo.ID)
	for _, a := range todoNow(s, todo.ID).Agents {
		if a.SessionID == run.SessionID {
			if a.Source != sourceReview || a.Agent != "codex" {
				t.Fatalf("in-memory reviewer link = %+v, want source review and start answer codex", a)
			}
			return
		}
	}
	t.Fatalf("reviewer %s has no in-memory link", run.SessionID)
}

// The same value reaches the persistent todo_agents row rather than only the manager's transient copy.
func TestAutoReviewAgentKindPersisted(t *testing.T) {
	database, err := db.Open(context.Background(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s, _, todo := supervisedAgentKindReview(t, database)
	run := autoReviewOf(s, todo.ID)
	rows, err := database.ListAllTodoAgents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rows {
		if a.TodoID == todo.ID && a.SessionID == run.SessionID {
			if a.Source != sourceReview || a.Agent != "codex" {
				t.Fatalf("persisted reviewer link = %+v, want source review and start answer codex", a)
			}
			return
		}
	}
	t.Fatalf("reviewer %s has no persisted link", run.SessionID)
}
