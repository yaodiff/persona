package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// noticesTo are the texts sent into assistant conversation id.
func (f *fakeHub) noticesTo(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.sends {
		if a["session_id"] == id {
			text, _ := a["text"].(string)
			out = append(out, text)
		}
	}
	return out
}

// newLeaderService plans work from direction lead1, which owns its task from dispatch through review.
func newLeaderService(t *testing.T) (*Service, *fakeHub, Todo) {
	t.Helper()
	return newLeaderServiceOn(t, nil)
}

func newLeaderServiceOn(t *testing.T, st Store) (*Service, *fakeHub, Todo) {
	t.Helper()
	return newLeaderServiceFromAgentNumberOn(t, st, 0)
}

// Public agent tools require at least eight characters of the session id. A seeded fake counter lets those tests
// exercise the real tool without relaxing its lookup contract; older fixtures keep their usual s1, s2 ids.
func newLeaderServiceFromAgentNumberOn(t *testing.T, st Store, agentNumber int) (*Service, *fakeHub, Todo) {
	t.Helper()
	return newConversationPlanServiceOn(t, st, "lead1", agentNumber)
}

func newConversationPlanServiceOn(t *testing.T, st Store, conversation string, agentNumber int) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietServiceOn(t, st)
	f.agents = agentNumber
	f.judge = `{"verdict":"done","reason":"PR 已合并"}`
	f.known["lead1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": "修登录崩溃", "prompt": "iPad 横屏时登录页崩溃，修好并补测试", "machine": "box", "cwd": "/code/app", "readonly": true,
		"why": "这个仓库最近在 box 上", "done_when": []string{"PR 合并到 main"}, "expect_minutes": 30,
	}}})
	res, err := s.toolTaskPlan(context.Background(), conversation, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "You supervise them") {
		t.Fatalf("the leader conversation is not told who supervises: %s", res)
	}
	waitFor(t, "the link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	if v := todo.Supervision; v == nil || v.Supervisor != conversation || len(v.DoneWhen) != 1 {
		t.Fatalf("supervision = %+v", todo.Supervision)
	}
	return s, f, todo
}

// The planning lead receives ordinary supervision, reworks its Agent and accepts delivery without involving main.
func TestLeaderConversationTaskIsSupervisedByItsLead(t *testing.T) {
	s, f, todo := newLeaderService(t)
	ctx := t.Context()
	if !strings.Contains(s.Context(ctx, ContextParams{SessionID: "lead1"}).Text, "这是 leader 对话") {
		t.Fatal("the leader's context does not identify its role")
	}
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了，PR https://github.com/o/r/pull/7 已合并", 0)
	waitFor(t, "lead's supervision", func() bool { return len(f.noticesTo("lead1")) == 1 })
	if n := f.noticesTo("lead1")[0]; !strings.Contains(n, "pull/7") || !strings.Contains(n, "待逐项复核") {
		t.Fatalf("the lead lost its delivery evidence: %s", n)
	}
	if len(f.noticesTo("conv1")) != 0 {
		t.Fatal("main received the lead's ordinary supervision")
	}
	if _, err := runJSON(t, s, "conv2", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "rework", "note": "x"}); err == nil {
		t.Fatal("an unrelated side conversation reviewed the task")
	}
	out, err := runJSON(t, s, "lead1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "rework", "note": "补测试"})
	if err != nil || !strings.Contains(out, "Rework round 1") {
		t.Fatalf("lead rework: %s %v", out, err)
	}
	if sent := sentToAgent(f); len(sent) != 1 || !strings.Contains(sent[0], "补测试") {
		t.Fatalf("rework did not reach the same Agent: %q", sent)
	}
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "补了测试", 0)
	waitFor(t, "second review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	out, err = runJSON(t, s, "lead1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "PR merged", "checks": completeReviewChecks(todoNow(s, todo.ID))})
	if err != nil || !strings.Contains(out, "accepted and closed") {
		t.Fatalf("lead acceptance: %s %v", out, err)
	}
	done := todoNow(s, todo.ID)
	if done.DoneAt == nil || done.Origin != "lead1" || done.Supervision.Supervisor != "lead1" || len(f.noticesTo("conv1")) != 0 {
		t.Fatalf("lead closeout changed ownership or woke main: %+v", done)
	}
}

// Main cannot review a task supervised by a direction lead (#13657): only the lead can review it, and ownership stays
// with the lead.
func TestMainReviewKeepsSupervisorWithLead(t *testing.T) {
	s, f, todo := newLeaderService(t)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了", 0)
	waitFor(t, "the review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "rework", "note": "补截图"}); err == nil {
		t.Fatal("main chat was allowed to review a lead-supervised task")
	}
	if got := todoNow(s, todo.ID).Supervision.Supervisor; got != "lead1" {
		t.Fatalf("main's refused review changed supervisor: %s", got)
	}
	if out, err := runJSON(t, s, "lead1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "rework", "note": "补截图"}); err != nil || !strings.Contains(out, "Rework round 1") {
		t.Fatalf("lead review: %s %v", out, err)
	}
	if got := todoNow(s, todo.ID).Supervision.Supervisor; got != "lead1" {
		t.Fatalf("lead review changed supervisor: %s", got)
	}
}

// A legacy unmarked side-supervised task still goes to main, while a new task_plan from a live side conversation is
// supervised by that conversation (#13657).
func TestDigestGoesToMainWithoutAnExplicitLeader(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	f.known["lead1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	todo, err := s.createTodo(ctx, "甲", "做甲", "lead1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "lead1", []string{"PR 合并"}, 0)
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "1", Detail: "做完了"})
	waitFor(t, "the main message", func() bool { return len(f.noticesTo("conv1")) == 1 })
	if n := f.noticesTo("conv1")[0]; !strings.Contains(n, "📋 督办") {
		t.Fatalf("main's supervision message:\n%s", n)
	}
	s.supervise.dedupMu.Lock()
	s.supervise.dedupMu.Unlock() // the digest send has finished before checking the non-recipient
	if got := f.noticesTo("lead1"); len(got) != 0 {
		t.Fatalf("the origin got a second supervision stream: %q", got)
	}
	if strings.Contains(s.Context(ctx, ContextParams{SessionID: "lead1"}).Text, "leader") {
		t.Fatal("a side conversation's context says it is a leader")
	}

	// task_plan from a live side conversation: the side conversation supervises it (#13657).
	res, err := s.toolTaskPlan(ctx, "lead1", planArgs(true))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "You supervise them") {
		t.Fatalf("a live side conversation is not told it supervises: %s", res)
	}
	var planned Todo
	waitFor(t, "the task", func() bool {
		for _, t := range s.todosOf(ctx, "*") {
			if t.ID != todo.ID {
				planned = t
				return true
			}
		}
		return false
	})
	if planned.Supervision == nil || planned.Supervision.Supervisor != "lead1" {
		t.Fatalf("a live side conversation's task is not side-supervised: %+v", planned.Supervision)
	}
}

// Where each signal goes.
func TestDigestTo(t *testing.T) {
	s, _ := newQuietService(t)
	ctx := context.Background()
	mk := func(supervisor, state string) string {
		todo, err := s.createTodo(ctx, "x", "x", supervisor, "")
		if err != nil {
			t.Fatal(err)
		}
		s.superviseTodo(ctx, todo.ID, supervisor, nil, 0)
		if state != "" {
			if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = state; return true }); err != nil {
				t.Fatal(err)
			}
		}
		return todo.ID
	}
	leaders := Settings{Main: "conv1", Leaders: []string{"lead1"}}
	none := Settings{Main: "conv1"}
	mains, leads, waits := mk("conv1", ""), mk("lead1", ""), mk("lead1", reviewNeedsUser)
	for _, c := range []struct {
		name string
		cfg  Settings
		g    superviseSignal
		want string
	}{
		{"main's task", leaders, superviseSignal{Todo: mains, Kind: sigReview}, "conv1"},
		{"leader's task", leaders, superviseSignal{Todo: leads, Kind: sigReview}, "lead1"},
		{"leader's report", leaders, superviseSignal{Todo: leads, Kind: sigReport, Report: upReportQuestion}, "lead1"},
		{"no leaders: main", none, superviseSignal{Todo: leads, Kind: sigReview}, "conv1"},
		{"leader's task waits for the user", leaders, superviseSignal{Todo: waits, Kind: sigIdle}, "conv1"},
		{"leader's escalation", leaders, superviseSignal{Todo: waits, Kind: sigEscalated}, "conv1"},
		{"leader's failed start", leaders, superviseSignal{Todo: leads, Kind: sigFailed}, "lead1"},
		{"failed start while needs_user", leaders, superviseSignal{Todo: waits, Kind: sigFailed}, "conv1"},
		{"main's failed start", leaders, superviseSignal{Todo: mains, Kind: sigFailed}, "conv1"},
		{"a removed leader's failed start", none, superviseSignal{Todo: leads, Kind: sigFailed}, "conv1"},
		{"gone", leaders, superviseSignal{Todo: "nope", Kind: sigReview}, "conv1"},
	} {
		if got := s.digestTo(ctx, c.cfg, c.g); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// An agent handed out as a task's agent hands out no Persona tasks (task_plan, agent_start), even when it calls them
// as an assistant conversation; the main conversation and leaders are not limited.
func TestWorkerCannotHandOutTasks(t *testing.T) {
	s, _, todo := newSupervisedService(t)
	ctx := context.Background()
	for _, name := range []string{"task_plan", "agent_start"} {
		args := planArgs(true)
		if name == "agent_start" {
			args, _ = json.Marshal(map[string]any{"machine": "box", "cwd": "/code/app", "prompt": "再开一个"})
		}
		res := s.Tool(ctx, ToolCall{Name: name, MachineID: "box", SessionID: "s1", Arguments: args})
		text := ""
		for _, c := range res.Content {
			text += c.Text
		}
		if !res.IsError || !strings.Contains(text, "does not hand out Persona tasks") || !strings.Contains(text, todo.ID) {
			t.Fatalf("%s from the task's agent: %v %s", name, res.IsError, text)
		}
		// As an agent (not an assistant conversation) it has no such tool at all.
		res = s.mcpCall(ctx, "box", "s1", name, args, false)
		if !res.IsError {
			t.Fatalf("%s through the agent's tools went through", name)
		}
	}
	if got := len(s.todosOf(ctx, "*")); got != 1 {
		t.Fatalf("%d todos: the worker handed one out", got)
	}
	for _, self := range []string{"conv1", "lead1"} {
		setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
		if err := s.workerRefusal(ctx, "server", self); err != nil {
			t.Fatalf("%s refused: %v", self, err)
		}
	}
	if err := s.workerRefusal(ctx, "box", "s9"); err != nil {
		t.Fatalf("an agent of no task refused: %v", err)
	}
}

// A cancelled task stays readable and writable; closing it never interrupts its blocked Agent.
func TestCancelDefersStoppingAndArchivingAgents(t *testing.T) {
	ft, row := closeTestService(t, func(s *proto.Session) {
		s.Status, s.State, s.PendingInteractions = proto.StatusBlocked, proto.StateNeedsYou, 1
	})
	ft.f.setSessions("mac", row)
	for _, method := range []string{deviceSessionStop, "session.close"} {
		ft.f.answerNode(method, func(_ string, raw json.RawMessage) (any, error) {
			t.Errorf("cancellation requested an unguarded stop (%s): %s", method, raw)
			return map[string]any{"stopped": true}, nil
		})
	}
	s, ctx := ft.s, context.Background()
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	todo, err := s.createTodo(ctx, "Disposable task", "Check the result", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "mac", row.SessionID, row.Title, "/code", "claude"); err != nil {
		t.Fatal(err)
	}
	res, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "cancelled", "note": "No longer needed"})
	if err != nil || !strings.Contains(res, "cancelled and closed") {
		t.Fatalf("cancel: %s %v", res, err)
	}
	s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{})
	if len(retiredOf(ft)) != 0 || archivedIDs(t, s)[row.SessionID] {
		t.Fatal("closing a task stopped or archived a live conversation")
	}
	if todoNow(s, todo.ID).DoneAt == nil {
		t.Fatal("the task was not closed")
	}
	if err := s.sendToSession(ctx, "mac", row.SessionID, "One more check"); err != nil {
		t.Fatalf("closed task conversation is not writable: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].DoneAt = &old
	s.todos.mu.Unlock()
	s.taskActivity.mu.Lock()
	s.taskActivity.times[runtimeListID("mac", row.SessionID)] = old
	s.taskActivity.mu.Unlock()
	s.fleet.mu.Lock()
	s.fleet.seen[[2]string{"mac", row.SessionID}] = old
	s.fleet.mu.Unlock()
	row.Status, row.State, row.PendingInteractions = proto.StatusIdle, proto.StateIdle, 0
	ft.d.setSessions("mac", []proto.Session{row})
	s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{{"mac", row.SessionID}: {done: old, running: old}})
	if got := retiredOf(ft); len(got) != 1 || got[0] != row.SessionID || !archivedIDs(t, s)[row.SessionID] {
		t.Fatalf("idle, inactive cancelled Agent was not stopped and archived: %v", got)
	}
}

func TestSetLeaders(t *testing.T) {
	s, f := newQuietServiceOn(t, newFleetMem())
	ctx := context.Background()
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Agent: "claude", Status: proto.StatusIdle})
	if got, _ := s.getLeaders(ctx); !strings.HasPrefix(got, "[]") {
		t.Fatalf("default: %s", got)
	}
	for _, bad := range []string{`["conv1"]`, `["nope"]`, `"lead1"`} {
		if _, err := s.setLeaders(ctx, json.RawMessage(bad)); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	if c, err := s.setLeaders(ctx, json.RawMessage(`[]`)); err != nil || c.apply != nil {
		t.Fatalf("no change: %+v %v", c, err)
	}
	c, err := s.setLeaders(ctx, json.RawMessage(`["lead1", "lead1"]`))
	if err != nil || c.apply == nil {
		t.Fatalf("set: %+v %v", c, err)
	}
	if _, err := c.apply(ctx); err != nil {
		t.Fatal(err)
	}
	if !s.isLeader(ctx, "lead1") || s.isLeader(ctx, "conv1") || !s.supervises(ctx, "conv1") || s.supervises(ctx, "conv2") {
		t.Fatal("leaders after set")
	}
	if got, _ := s.getLeaders(ctx); got != `["lead1"]` {
		t.Fatalf("get: %s", got)
	}
}

// A temporarily unavailable assigned lead keeps its queue. Main must not silently become its worker driver.
func TestAssignedLeadUnavailabilityKeepsItsQueue(t *testing.T) {
	s, f, todo := newLeaderService(t)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了", 0)
	waitFor(t, "lead queue", func() bool { return s.queuedFor("lead1") == 1 })
	if len(f.noticesTo("conv1")) != 0 || todoNow(s, todo.ID).Supervision.Supervisor != "lead1" {
		t.Fatal("an unavailable lead silently reassigned its task to main")
	}
}

// An agent that works for another open task by now is not closed or archived when the old task is cancelled.
func TestCancelLeavesAnotherTasksAgent(t *testing.T) {
	ft, row := closeTestService(t, nil)
	ft.f.setSessions("mac", row)
	var closed []string
	for _, m := range []string{"session.close", deviceSessionStop, nodeproto.StopIfIdle} {
		ft.f.answerNode(m, func(_ string, raw json.RawMessage) (any, error) {
			closed = append(closed, string(raw))
			return map[string]any{"ok": true}, nil
		})
	}
	s, ctx := ft.s, context.Background()
	todo, err := s.createTodo(ctx, "新任务", "做", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "mac", row.SessionID, "open", "/code", "claude"); err != nil {
		t.Fatal(err)
	}
	s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{})
	ft.f.mu.Lock()
	n := len(closed)
	ft.f.mu.Unlock()
	if n != 0 || archivedIDs(t, s)[row.SessionID] {
		t.Fatalf("another task's agent was closed (%d) or archived", n)
	}
}

// A refusal from the node never falls back to closing the terminal or archiving the Agent.
func TestClosedTaskRetirementRefusalKeepsAgent(t *testing.T) {
	for _, code := range []string{"no_terminal", "not_stopped", "not_retirable", "unknown_method"} {
		t.Run(code, func(t *testing.T) {
			ft, row := closeTestService(t, nil)
			ft.f.setSessions("mac", row)
			ft.f.answerNode(nodeproto.StopIfIdle, func(_ string, _ json.RawMessage) (any, error) {
				return nil, &rpc.Error{Code: code, Message: "refused"}
			})
			ft.f.answerNode(deviceSessionStop, func(_ string, _ json.RawMessage) (any, error) {
				t.Error("fell back to an unconditional stop")
				return nil, nil
			})
			ft.f.answerNode("session.close", func(_ string, _ json.RawMessage) (any, error) {
				t.Error("automatically closed an Agent's terminal")
				return nil, nil
			})
			s, ctx := ft.s, context.Background()
			todo, err := s.createTodo(ctx, "Disposable task", "Check", "conv1", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.linkTodoAgent(ctx, todo.ID, "mac", row.SessionID, "Agent", "/code", "claude"); err != nil {
				t.Fatal(err)
			}
			if ok, err := s.reviewDoneTodo(ctx, todo.ID, "Cancelled", false); err != nil || !ok {
				t.Fatalf("close: %v %v", ok, err)
			}
			s.todos.mu.Lock()
			at := time.Now().Add(-time.Hour)
			s.todos.todos[todo.ID].DoneAt = &at
			s.todos.mu.Unlock()
			s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{{"mac", row.SessionID}: {done: at, running: at}})
			if archivedIDs(t, s)[row.SessionID] {
				t.Fatal("a refused retirement was archived")
			}
		})
	}
}
