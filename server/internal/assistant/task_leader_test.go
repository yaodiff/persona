package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
)

var planIDRe = regexp.MustCompile(`plan_[0-9a-f]+`)

// leaderPlanArgs is a main-conversation task_plan for one writable task leader.
func leaderPlanArgs(role string) json.RawMessage {
	task := map[string]any{"title": "拆开做登录改造", "prompt": "把登录改造拆成几块交给 worker 做", "machine": "box", "cwd": "/code/app",
		"readonly": false, "permission": "bypassPermissions", "why": "这个仓库在 box 上", "done_when": []string{"所有 worker 的改动合进一个 PR"}}
	if role != "" {
		task["role"] = role
	}
	raw, _ := json.Marshal(map[string]any{"tasks": []any{task}})
	return raw
}

// workerArgs is a task leader's task_plan of one worker.
func workerArgs(extra map[string]any) json.RawMessage {
	task := map[string]any{"title": "改登录页", "prompt": "只改登录页的布局，补测试", "role": "worker", "readonly": false, "permission": "bypassPermissions",
		"done_when": []string{"登录页布局改好", "测试通过"}}
	for k, v := range extra {
		task[k] = v
	}
	raw, _ := json.Marshal(map[string]any{"tasks": []any{task}})
	return raw
}

// startTaskLeader makes a task leader of the main conversation's task (its agent s1 on box, listed and idle) and waits for
// the link.
func startTaskLeader(t *testing.T, s *Service, f *fakeHub) Todo {
	t.Helper()
	ctx := context.Background()
	res, err := s.toolTaskPlan(ctx, "conv1", leaderPlanArgs("leader"))
	if err != nil {
		t.Fatal(err)
	}
	// A writable task waits for the user's Start on its card.
	if _, err := s.startPlan(ctx, planIDRe.FindString(res), nil, false); err != nil {
		t.Fatalf("%s: %v", res, err)
	}
	waitFor(t, "the leader's link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Title: "leader",
		Status: proto.StatusIdle, UpdatedAt: time.Now()})
	return onlyTodo(s)
}

func todosOpenedBy(s *Service, origin string) []Todo {
	return s.todosOf(context.Background(), origin)
}

// planWorker has the leader (s1 on box) plan one worker, waits for its agent, and lists it as session sN on box.
func planWorker(t *testing.T, s *Service, f *fakeHub, extra map[string]any) Todo {
	t.Helper()
	known := map[string]bool{}
	for _, o := range todosOpenedBy(s, "box/s1") {
		known[o.ID] = true
	}
	res := s.mcpCall(context.Background(), "box", "s1", "task_plan", workerArgs(extra), false)
	if res.IsError {
		t.Fatalf("the leader's task_plan: %s", res.Content[0].Text)
	}
	var w Todo
	waitFor(t, "the worker's link", func() bool {
		for _, o := range todosOpenedBy(s, "box/s1") {
			if !known[o.ID] && len(o.Agents) == 1 {
				w = o
				return true
			}
		}
		return false
	})
	list := todosOpenedBy(s, "box/s1")
	sessions := []proto.Session{{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle, UpdatedAt: time.Now()}}
	for _, o := range list {
		for _, a := range o.Agents {
			sessions = append(sessions, proto.Session{SessionID: a.SessionID, MachineID: "box", Cwd: "/code/app", Agent: "claude",
				Status: proto.StatusWorking, UpdatedAt: time.Now()})
		}
	}
	f.setSessions("box", sessions...)
	return w
}

// sendsTo are the texts session.send put into machine's session.
func sendsTo(f *fakeHub, machine, session string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c.Device != machine || c.Method != DeviceSessionSend {
			continue
		}
		var p struct {
			SessionID string `json:"session_id"`
			Text      string `json:"text"`
		}
		raw, _ := json.Marshal(c.Params)
		if json.Unmarshal(raw, &p) == nil && p.SessionID == session {
			out = append(out, p.Text)
		}
	}
	return out
}

// deliveredTo are the texts session.send put into session and the fake hub took (sendsTo also has those it refused
// with f.failSend).
func deliveredTo(f *fakeHub, session string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.agentSent {
		if a["session_id"] == session {
			text, _ := a["text"].(string)
			out = append(out, text)
		}
	}
	return out
}

func leaderToolNames(tools []proto.Tool) []string {
	var out []string
	for _, tl := range tools {
		out = append(out, tl.Name)
	}
	return out
}

// The main conversation's task_plan marks a task's agent as a task leader (role leader): the agent's todo_agents row
// says so, and only that agent's tool list has the restricted task_plan; any other agent, a worker included, has none.
func TestTaskLeaderGetsRestrictedTaskPlanAndWorkersDoNot(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo := startTaskLeader(t, s, f)
	if a := todo.Agents[0]; a.Source != sourceLeader || a.SessionID != "s1" || a.MachineID != "box" {
		t.Fatalf("the leader's link: %+v", a)
	}
	if v := todo.Supervision; v == nil || v.Supervisor != "conv1" {
		t.Fatalf("the leader's own task is supervised by the conversation that handed it out: %+v", v)
	}
	if prompt, _ := f.starts[0]["prompt"].(string); !strings.Contains(prompt, "you are the leader of this task") {
		t.Fatalf("the leader's first message does not say it leads: %s", prompt)
	}

	var restricted []proto.Tool
	for _, tl := range s.sessionMCPTools(ctx, "box", "s1", false, false) {
		if tl.Name == "task_plan" {
			restricted = append(restricted, tl)
		}
	}
	if len(restricted) != 1 || !strings.Contains(restricted[0].Description, "Hand part of your own task to workers") {
		t.Fatalf("the leader's tools: %+v", restricted)
	}
	var schema map[string]any
	if err := json.Unmarshal(restricted[0].InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	taskSchema := schema["properties"].(map[string]any)["tasks"].(map[string]any)
	item := taskSchema["items"].(map[string]any)
	fields := item["properties"].(map[string]any)
	// A nested distinct_from.todo_id explains separation; it cannot select a worker's to-do.
	if fields["todo_id"] != nil || item["additionalProperties"] != false {
		t.Fatalf("the leader must not be able to select an existing worker to-do: %+v", item)
	}
	role, ok := fields["role"].(map[string]any)
	if !ok || !reflect.DeepEqual(role["enum"], []any{"worker"}) ||
		!slices.Contains(item["required"].([]any), any("role")) {
		t.Fatalf("the leader's task must require role=worker: %+v", item)
	}
	override, _ := fields["distinct_from"].(map[string]any)
	distinct, _ := override["items"].(map[string]any)
	if !reflect.DeepEqual(distinct["required"], []any{"todo_id", "reason"}) {
		t.Fatalf("the duplicate override needs a to-do ID and reason: %+v", distinct)
	}
	if got := leaderToolNames(s.sessionMCPTools(ctx, "box", "someone-else", false, false)); slices.Contains(got, "task_plan") {
		t.Fatalf("an agent of no task got task_plan: %v", got)
	}

	for _, session := range []string{"", "s1"} {
		if got := leaderToolNames(s.runtimeLeaderTools(ctx, "box", session, false)); !slices.Equal(got, []string{"task_plan", "task_review"}) {
			t.Fatalf("runtime leader tools for %q: %v", session, got)
		}
	}
	if got := s.runtimeLeaderTools(ctx, "box", "s1", true); len(got) != 0 {
		t.Fatalf("guest received runtime leader tools: %v", got)
	}

	w := planWorker(t, s, f, nil)
	if got := s.runtimeLeaderTools(ctx, "box", w.Agents[0].SessionID, false); len(got) != 0 {
		t.Fatalf("worker received runtime leader tools: %v", got)
	}
	if got := leaderToolNames(s.sessionMCPTools(ctx, "box", w.Agents[0].SessionID, false, false)); slices.Contains(got, "task_plan") {
		t.Fatalf("a worker got task_plan: %v", got)
	}
	if got := leaderToolNames(s.sessionMCPTools(ctx, "box", "s1", false, false)); !slices.Contains(got, "report_to_supervisor") {
		t.Fatalf("the leader lost the agents' own tools: %v", got)
	}
}

// The tool list is only the listing: a call must pass the same check, for the leader's own task only.
func TestTaskLeaderTaskPlanCallEnforcement(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo := startTaskLeader(t, s, f)
	other, err := s.createTaskTodo(ctx, "别的任务", "", "conv1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	call := func(session string, args json.RawMessage) proto.ToolResult {
		return s.mcpCall(ctx, "box", session, "task_plan", args, false)
	}
	refused := func(what string, res proto.ToolResult, want ...string) {
		t.Helper()
		if !res.IsError {
			t.Fatalf("%s was accepted: %+v", what, res)
		}
		for _, w := range want {
			if !strings.Contains(res.Content[0].Text, w) {
				t.Fatalf("%s: %q lacks %q", what, res.Content[0].Text, w)
			}
		}
	}

	refused("an agent of no task", call("someone-else", workerArgs(nil)), "unknown tool task_plan")
	refused("a call with no session", call("", workerArgs(nil)), "unknown tool task_plan")
	refused("a leader taking another todo", call("s1", workerArgs(map[string]any{"todo_id": other.ID})), "todo_id")
	refused("a leader making a leader", call("s1", workerArgs(map[string]any{"role": "leader"})), "role")
	refused("a leader attaching files", call("s1", workerArgs(map[string]any{"attachments": []string{"/tmp/a.png"}})), "attachments")
	if got := len(todosOpenedBy(s, "box/s1")); got != 0 {
		t.Fatalf("refused calls opened %d todos", got)
	}

	// Left-out machine and cwd are the leader's own place; the worker starts at once, with no plan card or push.
	feedBefore, pushesBefore := feedKinds(s, "conv1"), len(f.pushed())
	res := call("s1", workerArgs(nil))
	if res.IsError || !strings.Contains(res.Content[0].Text, "beneath your task "+todo.ID) {
		t.Fatalf("the leader's worker: %+v", res)
	}
	var starts []map[string]any
	waitFor(t, "the worker's start", func() bool { starts = f.started(); return len(starts) == 2 })
	if cwd, _ := starts[1]["cwd"].(string); cwd != "/code/app" {
		t.Fatalf("the worker's directory: %v", starts[1])
	}
	if mode, _ := starts[1]["permission_mode"].(string); mode != "bypassPermissions" {
		t.Fatalf("the worker's permission: %v", starts[1])
	}
	if prompt, _ := starts[1]["prompt"].(string); !strings.Contains(prompt, "handed to you by the leader") {
		t.Fatalf("the worker is not told who handed it the task: %s", prompt)
	}
	waitFor(t, "the worker's link", func() bool {
		l := todosOpenedBy(s, "box/s1")
		return len(l) == 1 && len(l[0].Agents) == 1
	})
	w := todosOpenedBy(s, "box/s1")[0]
	a := w.Agents[0]
	if a.Source != sourceWorker || a.Via != todo.ID || w.Quiet {
		t.Fatalf("the worker's link: %+v, todo %+v", a, w)
	}
	// Its supervisor is the leader's agent session, kept in the ledger with the task's done_when, and the API shows it.
	if v := w.Supervision; v == nil || v.Supervisor != "box/s1" || !slices.Equal(v.DoneWhen, []string{"登录页布局改好", "测试通过"}) {
		t.Fatalf("the worker's ledger: %+v", v)
	}
	if body := request(s, "GET", "/api/v1/todos?all=1", "").Body.String(); !strings.Contains(body, `"supervisor":"box/s1"`) {
		t.Fatalf("GET /api/v1/todos does not show the worker's supervisor: %s", body)
	}
	if lead, supervised, ok := s.leaderSupervisor(w); !supervised || !ok || lead.SessionID != "s1" || lead.MachineID != "box" {
		t.Fatalf("the worker's supervisor: %+v supervised %v ok %v", lead, supervised, ok)
	}
	if got := feedKinds(s, "conv1"); got != feedBefore {
		t.Fatalf("the worker's plan came to the main conversation's feed: %s -> %s", feedBefore, got)
	}
	if n := len(f.pushed()); n != pushesBefore {
		t.Fatalf("the user was pushed %d notifications for the leader's workers", n-pushesBefore)
	}

	// A worker has no dispatch right, and is told why; neither has anyone else's agent.
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle},
		proto.Session{SessionID: a.SessionID, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking})
	refused("a worker's task_plan", call(a.SessionID, workerArgs(nil)), "does not hand out Persona tasks")
	refused("a worker's task_plan, with the leader's role", call(a.SessionID, leaderPlanArgs("leader")), "does not hand out Persona tasks")
	if got := len(todosOpenedBy(s, a.SessionID)); got != 0 {
		t.Fatalf("a worker opened %d todos", got)
	}

	// The leader's task closes: its tool goes, and it cannot plan more.
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "done", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("closing the leader's task: %v %v", ok, err)
	}
	refused("a closed task's leader", call("s1", workerArgs(nil)))
}

func TestLeaderTaskPlanStartHost(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f := newQuietServiceOn(t, st)
	f.launcherList = companionLaunchers()
	startTaskLeader(t, s, f)

	args := workerArgs(map[string]any{"launcher": "agy", "host": "tmux"})
	res := s.mcpCall(context.Background(), "box", "s1", "task_plan", args, false)
	if !res.IsError || !strings.Contains(res.Content[0].Text, `host "tmux" is not where an Agent can start`) {
		t.Fatalf("invalid host: %+v", res)
	}
	if got := startHosts(f); !slices.Equal(got, []string{""}) {
		t.Fatalf("session.start hosts after refusal = %q, want only the leader's start", got)
	}

	planWorker(t, s, f, map[string]any{"launcher": "agy", "host": " herdr "})
	putTierProfiles(t, s, "economy", map[string]ModelTier{"agy-work": {Launcher: "agy", Host: new(proto.EngineHerdr)}}, nil)
	planWorker(t, s, f, map[string]any{"tier": "agy-work"})
	planWorker(t, s, f, map[string]any{"tier": "agy-work", "host": "agy"})
	if got := startHosts(f); !slices.Equal(got, []string{"", "herdr", "herdr", "agy"}) {
		t.Fatalf("session.start hosts = %q, want leader followed by explicit, inherited and overridden worker hosts", got)
	}
}

// A worker whose own supervision ledger cannot be stored never starts. The leader is linked directly so its setup
// does not depend on the deliberately failing worker ledger.
func TestTaskLeaderWorkerStopsWhenSupervisionCannotBeStored(t *testing.T) {
	st := failingSupervisionStore{DB: todoTestDB(t)}
	s, f := newQuietServiceOn(t, st)
	ctx := context.Background()
	leader, err := s.createTaskTodo(ctx, "leader", "", "conv1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgentWithSource(ctx, leader.ID, "box", "s1", "leader", "/code/app", "", "", sourceLeader, ""); err != nil {
		t.Fatal(err)
	}
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle})
	res := s.mcpCall(ctx, "box", "s1", "task_plan", workerArgs(nil), false)
	waitFor(t, "the worker start reservation to settle", func() bool { return s.taskStarting.Load() == 0 })
	if len(f.starts) != 0 {
		t.Fatalf("session.start ran for a worker without its supervision ledger: %+v", f.starts)
	}
	if res.IsError || !strings.Contains(res.Content[0].Text, "injected supervision write failure") {
		t.Fatalf("the worker's ledger failure was not reported: %+v", res)
	}
	workers := todosOpenedBy(s, "box/s1")
	if len(workers) != 1 || workers[0].DoneAt == nil || !strings.Contains(workers[0].StartError, "injected supervision write failure") ||
		workers[0].Supervision != nil || len(workers[0].Agents) != 0 {
		t.Fatalf("failed worker preparation left an open or unsupervised card: %+v", workers)
	}
}

// The room is bounded: a leader cannot keep more than leaderWorkersMost workers open. Workers count by the task they
// belong to, so a leader started again (a new session) does not get a new allowance.
func TestTaskLeaderWorkersAreBounded(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	leader := startTaskLeader(t, s, f)
	for i := 0; i < leaderWorkersMost; i++ {
		w, err := s.createTaskTodo(ctx, "worker", "", "box/s1", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 { // ledger names this leader
			s.superviseTodo(ctx, w.ID, "box/s1", nil, 0)
		} else { // an older leader session, linked to the same leader task
			s.superviseTodo(ctx, w.ID, "box/old", nil, 0)
			if err := s.linkTodoAgentWithSource(ctx, w.ID, "box", "old-worker-"+w.ID, "worker", "/code/app", "", "", sourceWorker, leader.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	res := s.mcpCall(ctx, "box", "s1", "task_plan", workerArgs(nil), false)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "at most 8 workers") {
		t.Fatalf("a ninth open worker: %+v", res)
	}
}

// Unknown identities have no leader rights; discovery can be refreshed later.
func TestLeaderToolListWithNoSessionDoesNotWait(t *testing.T) {
	s, _ := newQuietService(t)
	if got := s.leaderTools(context.Background(), "box", ""); got != nil {
		t.Fatalf("no session: %v", got)
	}
}

// The same session id on another computer is another agent: it is not the leader, is not reached as its workers'
// supervisor and does not review for it.
func TestLeaderIdentityIsMachineAndSession(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	if _, _, ok := s.leaderTask(ctx, "other", "s1"); ok {
		t.Fatal("the same session id on another computer is a leader")
	}
	if s.isLeaderSession("other", "s1") || !s.isLeaderSession("box", "s1") {
		t.Fatal("isLeaderSession ignores the machine")
	}
	review, _ := json.Marshal(map[string]any{"todo_id": w.ID, "verdict": "accepted"})
	if res := s.mcpCall(ctx, "other", "s1", "task_review", review, false); !res.IsError {
		t.Fatalf("an agent with the leader's session id on another computer reviewed: %+v", res)
	}
	if lead, _, ok := s.leaderSupervisor(todoNow(s, w.ID)); !ok || lead.MachineID != "box" {
		t.Fatalf("the worker's leader: %+v", lead)
	}
}

// A writable task needs no explicit permission: without one it takes the usual default, and any mode is accepted. A
// read-only task is unaffected.
func TestWritableTaskPlanNeedsNoExplicitPermission(t *testing.T) {
	s, f := newQuietService(t)
	f.known["lead1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	ctx := context.Background()
	plan := func(self string, readonly bool, permission string) (string, error) {
		task := map[string]any{"title": "修登录崩溃", "prompt": "修好并补测试", "role": "worker", "machine": "box", "cwd": "/code/app", "readonly": readonly,
			"why": "这个仓库在 box 上"}
		if permission != "" {
			task["permission"] = permission
		}
		raw, _ := json.Marshal(map[string]any{"tasks": []any{task}})
		return s.toolTaskPlan(ctx, self, raw)
	}
	for _, self := range []string{"conv1", "lead1"} {
		for _, permission := range []string{"", "default", "acceptEdits", "auto", "plan", "bypassPermissions"} {
			if _, err := plan(self, false, permission); err != nil {
				t.Fatalf("%s, permission %q: %v", self, permission, err)
			}
		}
		if _, err := plan(self, true, ""); err != nil {
			t.Fatalf("%s, read-only: %v", self, err)
		}
	}
	if got := len(s.tasks.plans); got != 14 {
		t.Fatalf("plans saved: %d", got)
	}
}

// Any conversation may give role leader; a live side conversation supervises the leader task it dispatched (#13657)
// while its workers stay with the task leader Agent.
func TestTaskPlanRoleFromAnyConversation(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	ctx := context.Background()
	res, err := s.toolTaskPlan(ctx, "conv2", leaderPlanArgs("leader"))
	if err != nil {
		t.Fatalf("a side conversation's leader: %v", err)
	}
	if _, err := s.startPlan(ctx, planIDRe.FindString(res), nil, false); err != nil {
		t.Fatalf("%s: %v", res, err)
	}
	waitFor(t, "the leader's link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	if v := todo.Supervision; v == nil || v.Supervisor != "conv2" || todo.Origin != "conv2" {
		t.Fatalf("live side conversation does not supervise its own leader: %+v (origin %q)", todo.Supervision, todo.Origin)
	}
	if a := todo.Agents[0]; a.Source != sourceLeader || a.SessionID != "s1" || a.MachineID != "box" {
		t.Fatalf("the leader's link: %+v", a)
	}
	if _, err := s.toolTaskPlan(ctx, "conv1", leaderPlanArgs("boss")); err == nil || !strings.Contains(err.Error(), `"boss" is not one`) {
		t.Fatalf("a made-up role: %v", err)
	}
	if _, err := s.toolTaskPlan(ctx, "conv1", leaderPlanArgs("leader")); err != nil {
		t.Fatalf("the main conversation's leader: %v", err)
	}
	// A client's edit on the card can change what the user may, never the role.
	p := &TaskPlan{}
	for _, pl := range s.tasks.plans {
		p = pl
	}
	task := p.Tasks[0]
	applyPlanEdit(&task, PlanTask{Role: "", Parent: "todo_x", Machine: "box"})
	if task.Role != roleLeader || task.Parent != "" {
		t.Fatalf("an edit changed the role or the parent: %+v", task)
	}
}

// gotMessage waits for a supervision message into the leader's session s1 that has all of words, and gives it.
func gotMessage(t *testing.T, f *fakeHub, words ...string) string {
	t.Helper()
	var found string
	waitFor(t, "a supervision message to the leader with "+strings.Join(words, ", "), func() bool {
		for _, m := range sendsTo(f, "box", "s1") {
			ok := true
			for _, w := range words {
				ok = ok && strings.Contains(m, w)
			}
			if ok {
				found = m
				return true
			}
		}
		return false
	})
	return found
}

// A worker's reports, urgent or not, are supervision signals to its leader's agent session on the leader's machine; the
// leader's own report goes to the conversation that handed it its task. Nothing of the worker's reaches the main chat.
func TestWorkerReportGoesToLeaderOnly(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	worker := w.Agents[0].SessionID

	res := s.agentTool(ctx, "box", worker, "report_to_supervisor", json.RawMessage(`{"kind":"blocked","text":"登录页卡在设计评审","evidence":["见 PR 评论"]}`))
	if res.IsError {
		t.Fatalf("the worker's report: %+v", res)
	}
	m := gotMessage(t, f, "Persona · supervision", "改登录页", "登录页卡在设计评审", "见 PR 评论", "登录页布局改好")
	if !strings.Contains(m, "blocked") {
		t.Errorf("the leader's message does not say it is blocked:\n%s", m)
	}
	// A question that says it can wait is no briefing item either: it goes to the leader as any report does.
	res = s.agentTool(ctx, "box", worker, "report_to_supervisor", json.RawMessage(`{"kind":"question","text":"用哪个分支？","urgent":false}`))
	if res.IsError || strings.Contains(res.Content[0].Text, "Queued") {
		t.Fatalf("the worker's urgent:false question: %+v", res)
	}
	gotMessage(t, f, "用哪个分支？")
	if n := sendsTo(f, "box", worker); len(n) != 0 {
		t.Fatalf("the worker was sent %q", n)
	}

	// The leader's own report is the conversation's, and carries nothing of the worker's.
	res = s.agentTool(ctx, "box", "s1", "report_to_supervisor", json.RawMessage(`{"kind":"blocked","text":"leader 自己卡在评审"}`))
	if res.IsError {
		t.Fatalf("the leader's report: %+v", res)
	}
	waitFor(t, "the main conversation's message", func() bool { return len(f.notices()) == 1 })
	n := f.notices()[0]
	if !strings.Contains(n, "leader 自己卡在评审") || strings.Contains(n, "登录页卡在设计评审") || strings.Contains(n, "改登录页") || strings.Contains(n, "用哪个分支") {
		t.Fatalf("the main conversation's message:\n%s", n)
	}
	items, _, _ := s.feed(ctx, "conv1", time.Time{})
	for _, it := range items {
		if it.Kind == feedReport && it.Ref != onlyLeaderTask(s).ID {
			t.Fatalf("a worker's report was put in the main conversation's feed: %+v", it)
		}
	}
}

func onlyLeaderTask(s *Service) Todo {
	for _, t := range s.todosOf(context.Background(), "conv1") {
		return t
	}
	return Todo{}
}

// A worker whose leader's task is closed: its active report is not lost. The worker is rehomed to the conversation above
// the leader first (supervisor and origin), and the report is told to that conversation; with nobody above, the worker is
// cancelled and told so readably.
func TestWorkerReportAfterLeaderClosedIsRehomedNotLost(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo := startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	worker := w.Agents[0].SessionID
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "done", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("closing the leader's task: %v %v", ok, err)
	}
	res := s.agentTool(ctx, "box", worker, "report_to_supervisor", json.RawMessage(`{"kind":"blocked","text":"leader 走了之后的汇报"}`))
	if res.IsError || strings.Contains(res.Content[0].Text, "Nobody receives") {
		t.Fatalf("a worker of a closed leader task: %+v", res)
	}
	cur := todoNow(s, w.ID)
	if cur.Supervision.Supervisor != "conv1" || cur.Origin != "conv1" {
		t.Fatalf("the worker was not rehomed (supervisor, origin): %+v origin %q", cur.Supervision, cur.Origin)
	}
	waitFor(t, "the report to reach the conversation above", func() bool { return len(f.notices()) >= 1 })
	if n := f.notices()[0]; !strings.Contains(n, "leader 走了之后的汇报") {
		t.Fatalf("the conversation above did not get the report:\n%s", n)
	}
	// Idempotent: another pass changes nothing and tells nothing twice.
	updated := cur.Supervision.UpdatedAt
	s.rehomeLeaderlessWorkers(ctx)
	s.rehomeLeaderlessWorkers(ctx)
	if got := todoNow(s, w.ID).Supervision.UpdatedAt; !got.Equal(updated) {
		t.Fatalf("rehoming again changed the ledger: %v -> %v", updated, got)
	}
	if got := sendsTo(f, "box", "s1"); len(got) != 0 {
		t.Fatalf("the closed leader was messaged: %q", got)
	}
	// The to-do is the conversation's task now: its notices and listings follow the origin.
	found := false
	for _, o := range s.todosOf(ctx, "conv1") {
		found = found || o.ID == w.ID
	}
	if !found {
		t.Fatal("the rehomed worker is not among the conversation's tasks")
	}
}

// With nobody above the closed leader, a worker's report cancels it with a readable result.
func TestWorkerReportWithNobodyAboveCancelsReadably(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo := startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	setSettings(t, s, func(v *Settings) { v.Main = "" })
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.Supervisor = ""; return true }); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "done", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("closing the leader's task: %v %v", ok, err)
	}
	res := s.agentTool(ctx, "box", w.Agents[0].SessionID, "report_to_supervisor", json.RawMessage(`{"kind":"progress","text":"x"}`))
	if res.IsError || !strings.Contains(res.Content[0].Text, "was cancelled") {
		t.Fatalf("the report of a worker with nobody above: %+v", res)
	}
	if cur := todoNow(s, w.ID); cur.DoneAt == nil || !cur.Cancelled() {
		t.Fatalf("the worker: %+v", cur)
	}
}

// A worker that does not start is told to its leader as a supervision signal, not to a conversation, and no start remedy
// (a main-chat tool) is begun for it.
func TestFailedWorkerStartIsTheLeadersToHear(t *testing.T) {
	// An unanswered start now reconciles with the machine before it fails (#6540); keep that window short here, this
	// test is about who hears the failure, not how long reconciliation is allowed to take. The fake fails the start at
	// once, so the start keeps its budget (#14158).
	shortTaskStartAdopt(t)
	s, f := newQuietService(t)
	startTaskLeader(t, s, f)
	f.mu.Lock()
	f.failStart = context.DeadlineExceeded
	f.mu.Unlock()
	res := s.mcpCall(context.Background(), "box", "s1", "task_plan", workerArgs(nil), false)
	if res.IsError {
		t.Fatalf("%+v", res)
	}
	gotMessage(t, f, "its agent did not start")
	if got := f.notices(); len(got) != 0 {
		t.Fatalf("the main conversation was told: %q", got)
	}
}

// The leader, its workers and where a report goes are kept in the database's rows: a restarted manager reads them back.
func TestTaskLeaderSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	database := taskRecoveryDatabase(t)
	s, f := newQuietServiceOn(t, database)
	todo := startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	worker := w.Agents[0].SessionID

	restarted, hub := newQuietServiceOn(t, database)
	hub.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle},
		proto.Session{SessionID: worker, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking})
	lead, agent, ok := restarted.leaderTask(ctx, "box", "s1")
	if !ok || lead.ID != todo.ID || agent.Source != sourceLeader {
		t.Fatalf("the leader after a restart: %+v %+v %v", lead, agent, ok)
	}
	if got := leaderToolNames(restarted.sessionMCPTools(ctx, "box", "s1", false, false)); !slices.Contains(got, "task_plan") {
		t.Fatalf("the leader's tools after a restart: %v", got)
	}
	if got := leaderToolNames(restarted.sessionMCPTools(ctx, "box", worker, false, false)); slices.Contains(got, "task_plan") {
		t.Fatalf("a worker's tools after a restart: %v", got)
	}
	wr := todoNow(restarted, w.ID)
	if v := wr.Supervision; v == nil || v.Supervisor != "box/s1" || len(v.DoneWhen) != 2 {
		t.Fatalf("the worker's ledger after a restart: %+v", v)
	}
	if lead, supervised, ok := restarted.leaderSupervisor(wr); !supervised || !ok || lead.SessionID != "s1" {
		t.Fatalf("the worker's supervisor after a restart: %+v %v %v", lead, supervised, ok)
	}
	res := restarted.agentTool(ctx, "box", worker, "report_to_supervisor", json.RawMessage(`{"kind":"blocked","text":"重启后还在跑"}`))
	if res.IsError {
		t.Fatalf("the report after a restart: %+v", res)
	}
	gotMessage(t, hub, "重启后还在跑")
	if got := hub.notices(); len(got) != 0 {
		t.Fatalf("the main conversation was told: %q", got)
	}
	// The saved plans keep the role and the parent, so starting a task again (a retry) links the same way.
	if p, err := restarted.loadPlan(ctx, todo.PlanID); err != nil || p.Tasks[0].Role != roleLeader || p.Tasks[0].Parent != "" {
		t.Fatalf("the leader's plan after a restart: %+v %v", p, err)
	}
	if p, err := restarted.loadPlan(ctx, w.PlanID); err != nil || p.Conversation != "box/s1" || p.Tasks[0].Role != "worker" || p.Tasks[0].Parent != todo.ID {
		t.Fatalf("the worker's plan after a restart: %+v %v", p, err)
	}
	// And in the rows the link is read from:
	rows, err := database.ListAllTodoAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, r := range rows {
		kinds[r.SessionID] = r.Source + "/" + r.Via
	}
	if kinds["s1"] != "leader/" || kinds[worker] != "worker/"+todo.ID {
		t.Fatalf("todo_agents: %v", kinds)
	}
}

// A legacy one-shot list waits for the leader being linked, bounded; a revision-aware caller returns immediately and
// recovers through its cursor instead. The wait alone grants rights to nobody.
func TestLegacyTaskLeaderListWaitsForItsLink(t *testing.T) {
	s, _ := newQuietService(t)
	old := leaderListWait
	leaderListWait = 400 * time.Millisecond
	t.Cleanup(func() { leaderListWait = old })
	ctx := context.Background()

	if got := s.leaderTools(ctx, "box", "sx"); got != nil {
		t.Fatalf("no leader is starting: %v", got)
	}
	s.leaderStarts.Add(1)
	start := time.Now()
	if got := s.leaderTools(ctx, "box", "sx"); got != nil || time.Since(start) < 300*time.Millisecond {
		t.Fatalf("an unlinked agent while a leader starts: %v after %s", got, time.Since(start))
	}
	todo, err := s.createTaskTodo(ctx, "leader", "", "conv1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(100*time.Millisecond, func() {
		_ = s.linkTodoAgentWithSource(ctx, todo.ID, "box", "sx", "leader", "/code/app", "", "", sourceLeader, "")
		s.leaderStarts.Add(-1)
	})
	if got := leaderToolNames(s.leaderTools(ctx, "box", "sx")); !slices.Equal(got, []string{"task_plan", "task_review"}) {
		t.Fatalf("the leader's list after its link: %v", got)
	}

	s.leaderStarts.Add(1)
	defer s.leaderStarts.Add(-1)
	start = time.Now()
	watched := s.sessionMCPToolsSince(ctx, "box", "other", false, false, "a-revision")
	if elapsed := time.Since(start); elapsed >= 200*time.Millisecond {
		t.Fatalf("a revision-aware list waited for the legacy startup window: %s", elapsed)
	}
	if got := leaderToolNames(watched["tools"].([]proto.Tool)); slices.Contains(got, "task_plan") || slices.Contains(got, "task_review") {
		t.Fatalf("an unrelated agent gained leader rights: %v", got)
	}
}

// Registration after the initial list invalidates its cursor. No startup counter
// or finite wait window is involved, and late watch subscription cannot lose it.
func TestTaskLeaderListRefreshesAfterItsLink(t *testing.T) {
	s, _ := newQuietService(t)
	ctx := context.Background()
	todo, err := s.createTaskTodo(ctx, "leader", "", "conv1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	before := s.sessionMCPToolsSince(ctx, "box", "sx", false, false, "")
	if got := leaderToolNames(before["tools"].([]proto.Tool)); slices.Contains(got, "task_plan") || slices.Contains(got, "task_review") {
		t.Fatalf("unlinked agent has leader tools: %v", got)
	}
	cursor := before["revision"].(string)
	_, changed := s.mcpChanges.snapshot()
	if err := s.linkPlanAgent(ctx, PlanTask{TodoID: todo.ID, Role: roleLeader}, "box", "sx", false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("registration did not wake tool discovery")
	}
	after := s.sessionMCPToolsSince(ctx, "box", "sx", false, false, cursor)
	names := leaderToolNames(after["tools"].([]proto.Tool))
	if after["revision"] == cursor || !slices.Contains(names, "task_plan") || !slices.Contains(names, "task_review") {
		t.Fatalf("late leader list: %+v", after)
	}
	if got := s.leaderTools(ctx, "box", "other"); got != nil {
		t.Fatalf("another Agent gained rights: %v", got)
	}

	// Completing the task invalidates discovery too, and the tools disappear.
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "done", time.Now()); err != nil || !ok {
		t.Fatalf("close: %v %v", ok, err)
	}
	closed := s.sessionMCPToolsSince(ctx, "box", "sx", false, false, after["revision"].(string))
	if got := leaderToolNames(closed["tools"].([]proto.Tool)); slices.Contains(got, "task_plan") || slices.Contains(got, "task_review") {
		t.Fatalf("closed task kept leader tools: %v", got)
	}
}

// The messages the leader and its workers are given.
func TestTaskLeaderMessages(t *testing.T) {
	for _, want := range []string{"you are the leader of this task", "task_plan", "default permission mode", "Persona · supervision", "task_review", "stop and ask the user"} {
		if !strings.Contains(prompts.RenderTaskLeader(prompts.TaskLeader{}), want) {
			t.Errorf("the leader's brief lacks %q", want)
		}
	}
	for _, want := range []string{"handed to you by the leader", "report_to_supervisor", "cannot hand it on", "stop and ask the user"} {
		if !strings.Contains(prompts.RenderTaskWorker(), want) {
			t.Errorf("the worker's brief lacks %q", want)
		}
	}
	entries := []prompts.SuperviseEntry{{ID: "todo_1", Title: "改登录页", Kind: "report", Report: "question", Said: "用哪个分支？", Step: 2, Steps: 5,
		DoneWhen: []string{"测试通过"}, Round: 1}}
	got := prompts.RenderWorkerSupervise(entries)
	for _, want := range []string{"Persona · supervision: 1 of the worker tasks", `1. "改登录页" (todo_1): the worker reports a question for you (step 2 of 5)`,
		"sent back 1 time(s)", "Done when: 测试通过", "It said:\n   用哪个分支？", "task_review", "You can review your own workers only"} {
		if !strings.Contains(got, want) {
			t.Errorf("the worker supervision message lacks %q:\n%s", want, got)
		}
	}
}

// The hub's own signals about a worker (its turn ending, a judge's verdict, a question waiting on the user) go to its
// leader's session, never to the main conversation; and the done judge does not close a supervised worker: it waits for
// the leader's review.
func TestWorkerSignalsGoToTheLeaderAndTheJudgeDoesNotCloseIt(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	f.judge = `{"verdict":"done","reason":"PR 已合并"}`
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	worker := w.Agents[0].SessionID
	turn := func(status, text string, pending int) {
		state := proto.StateUnread
		if pending > 0 {
			state = proto.StateNeedsYou
		}
		if status == proto.StatusWorking {
			state = proto.StateWorking
		}
		f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle},
			proto.Session{SessionID: worker, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: status, State: state, LastText: text,
				PendingInteractions: pending, UpdatedAt: time.Now()})
		if status == proto.StatusWorking {
			s.taskWorking(ctx, "box", worker, startRow{})
		} else {
			s.taskTurnEnded(ctx, "box", worker)
		}
	}
	turn(proto.StatusWorking, "", 0)
	turn(proto.StatusDone, "做完了，PR https://github.com/o/r/pull/7 已合并", 0)
	m := gotMessage(t, f, "waits for your review", "改登录页", "pull/7")
	if !strings.Contains(m, "登录页布局改好") {
		t.Errorf("the worker's done_when is not in the message:\n%s", m)
	}
	if cur := todoNow(s, w.ID); cur.DoneAt != nil || cur.Supervision.State != reviewPending {
		t.Fatalf("the judge closed a supervised worker, or it does not wait for review: %+v %+v", cur, cur.Supervision)
	}
	if got := f.notices(); len(got) != 0 {
		t.Fatalf("the main conversation was told about a worker: %q", got)
	}
	items, _, _ := s.feed(ctx, "conv1", time.Time{})
	for _, it := range items {
		if it.Ref == w.ID {
			t.Fatalf("the worker's to-do is in the main conversation's feed: %+v", it)
		}
	}
}

// accepted, rework and cancelled by the leader, and what a leader may and may not review. The leader's task_review is
// listed for the leader only and reaches its direct workers only; no card goes to the user.
func TestTaskLeaderReviewsItsWorkers(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	leader := startTaskLeader(t, s, f)
	w1 := planWorker(t, s, f, nil)
	w2 := planWorker(t, s, f, map[string]any{"title": "改注册页"})
	w3 := planWorker(t, s, f, map[string]any{"title": "改设置页"})
	review := func(session string, args map[string]any) proto.ToolResult {
		raw, _ := json.Marshal(args)
		return s.mcpCall(ctx, "box", session, "task_review", raw, false)
	}
	refused := func(what string, res proto.ToolResult, want string) {
		t.Helper()
		if !res.IsError || !strings.Contains(res.Content[0].Text, want) {
			t.Fatalf("%s: %+v", what, res)
		}
	}
	if got := leaderToolNames(s.sessionMCPTools(ctx, "box", "s1", false, false)); !slices.Contains(got, "task_review") || !slices.Contains(got, "task_plan") {
		t.Fatalf("the leader's tools: %v", got)
	}
	if got := leaderToolNames(s.sessionMCPTools(ctx, "box", w1.Agents[0].SessionID, false, false)); slices.Contains(got, "task_review") {
		t.Fatalf("a worker got task_review: %v", got)
	}
	// Not the leader's to review: its own task, another task, a to-do of the main chat; and nobody else may review a worker.
	other, err := s.createTaskTodo(ctx, "别的任务", "", "conv1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	refused("the leader's own task", review("s1", map[string]any{"todo_id": leader.ID, "verdict": "accepted"}), "not one of your workers")
	refused("another task", review("s1", map[string]any{"todo_id": other.ID, "verdict": "accepted"}), "not one of your workers")
	refused("an agent of no task", review("someone-else", map[string]any{"todo_id": w1.ID, "verdict": "accepted"}), "unknown tool task_review")
	refused("a worker's review of a sibling", review(w1.Agents[0].SessionID, map[string]any{"todo_id": w2.ID, "verdict": "accepted"}), "unknown tool task_review")
	refused("a field outside the schema", review("s1", map[string]any{"todo_id": w1.ID, "verdict": "accepted", "outcome": "cannot"}), "only the fields")
	refused("rework with nothing said", review("s1", map[string]any{"todo_id": w1.ID, "verdict": "rework"}), "say what is missing")
	if cur := todoNow(s, w1.ID); cur.DoneAt != nil || cur.Supervision.State != "" {
		t.Fatalf("a refused review changed the worker: %+v", cur.Supervision)
	}

	// rework: the gaps go to the worker; the third goes up instead.
	pushes := len(f.pushed())
	res := review("s1", map[string]any{"todo_id": w1.ID, "verdict": "rework", "note": "补一个深色模式截图"})
	if res.IsError || !strings.Contains(res.Content[0].Text, "Rework round 1 of 2") {
		t.Fatalf("rework: %+v", res)
	}
	if got := sendsTo(f, "box", w1.Agents[0].SessionID); len(got) != 1 || !strings.Contains(got[0], "补一个深色模式截图") {
		t.Fatalf("what the worker was sent: %q", got)
	}
	if v := todoNow(s, w1.ID).Supervision; v.State != reviewRework || v.Round != 1 || v.Supervisor != "box/s1" {
		t.Fatalf("the ledger after a rework: %+v", v)
	}
	// accepted: closed by the leader's review, with no card to the user.
	res = review("s1", map[string]any{"todo_id": w2.ID, "verdict": "accepted", "note": "测试通过", "checks": completeReviewChecks(todoNow(s, w2.ID))})
	if res.IsError || !strings.Contains(res.Content[0].Text, "accepted and closed") {
		t.Fatalf("accepted: %+v", res)
	}
	if cur := todoNow(s, w2.ID); cur.DoneAt == nil || cur.DoneBy != "review" || cur.Supervision.State != reviewAccepted || cur.Supervision.Supervisor != "box/s1" {
		t.Fatalf("an accepted worker: %+v %+v", cur, cur.Supervision)
	}
	// cancelled: closed, its agent is stopped.
	res = review("s1", map[string]any{"todo_id": w3.ID, "verdict": "cancelled", "note": "不用做了"})
	if res.IsError || !strings.Contains(res.Content[0].Text, "cancelled and closed") {
		t.Fatalf("cancelled: %+v", res)
	}
	if cur := todoNow(s, w3.ID); cur.DoneAt == nil || !cur.Cancelled() {
		t.Fatalf("a cancelled worker: %+v", cur)
	}
	if n := len(f.pushed()); n != pushes {
		t.Fatalf("the leader's reviews pushed the user %d times", n-pushes)
	}
	// A closed worker takes no more reviews that need it open.
	refused("rework of a closed worker", review("s1", map[string]any{"todo_id": w2.ID, "verdict": "rework", "note": "x"}), "is done already")
	if got := f.notices(); len(got) != 0 {
		t.Fatalf("the main conversation was told: %q", got)
	}
}

// A task leader follows the same complete-evidence rule as a conversation supervisor, with no write on a refusal.
func TestTaskLeaderAcceptedRequiresCompleteCurrentCriteria(t *testing.T) {
	for _, stored := range []bool{false, true} {
		name := "memory"
		if stored {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			var st Store
			if stored {
				st = todoTestDB(t)
			}
			s, f := newQuietServiceOn(t, st)
			ctx := context.Background()
			startTaskLeader(t, s, f)
			w := planWorker(t, s, f, nil)
			before := todoNow(s, w.ID)
			valid := completeReviewChecks(before)
			for _, tc := range []struct {
				name   string
				checks []ReviewCheck
				want   string
			}{
				{name: "missing", checks: valid[:1], want: "has no check"},
				{name: "duplicate", checks: []ReviewCheck{valid[0], valid[0], valid[1]}, want: "duplicate"},
				{name: "unknown", checks: append(append([]ReviewCheck{}, valid...), ReviewCheck{Criterion: "old criterion", Met: true, Evidence: "old"}), want: "unknown current done_when"},
				{name: "unmet", checks: []ReviewCheck{valid[0], {Criterion: before.Supervision.DoneWhen[1], Evidence: "still missing"}}, want: "not met; use rework for what is missing"},
				{name: "blank evidence", checks: []ReviewCheck{valid[0], {Criterion: before.Supervision.DoneWhen[1], Met: true, Evidence: "  "}}, want: "non-empty evidence"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					res := s.mcpCall(ctx, "box", "s1", "task_review", mustJSON(map[string]any{"todo_id": w.ID, "verdict": "accepted", "checks": tc.checks}), false)
					if !res.IsError || !strings.Contains(res.Content[0].Text, tc.want) || !strings.Contains(res.Content[0].Text, "nothing was written") ||
						strings.Contains(res.Content[0].Text, "outcome cannot") {
						t.Fatalf("accepted invalid checks: %+v", res)
					}
					after := todoNow(s, w.ID)
					if after.DoneAt != nil || !reflect.DeepEqual(after.Supervision, before.Supervision) {
						t.Fatalf("a refused review wrote the worker or ledger: before %+v, after %+v", before.Supervision, after.Supervision)
					}
				})
			}
			res := s.mcpCall(ctx, "box", "s1", "task_review", mustJSON(map[string]any{"todo_id": w.ID, "verdict": "accepted", "checks": valid}), false)
			if res.IsError || !strings.Contains(res.Content[0].Text, "accepted and closed") || todoNow(s, w.ID).DoneAt == nil {
				t.Fatalf("complete acceptance: %+v", res)
			}
		})
	}
}

// needs_user goes up to the conversation that handed the leader its task (the main one here) as a decision, with the
// leader's reason and recommendation, and no card goes to the user; a third rework goes up the same way. The main
// conversation reviewing the worker afterwards does not take the worker over.
func TestTaskLeaderNeedsUserGoesToItsOwnSupervisor(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	review := func(self string, args map[string]any) proto.ToolResult {
		raw, _ := json.Marshal(args)
		return s.mcpCall(ctx, "box", self, "task_review", raw, false)
	}
	pushes := len(f.pushed())
	res := review("s1", map[string]any{"todo_id": w.ID, "verdict": "needs_user", "reason": "要不要顺手改注册页", "recommend": "先只改登录页"})
	if res.IsError || !strings.Contains(res.Content[0].Text, "no card went to the user") {
		t.Fatalf("needs_user: %+v", res)
	}
	waitFor(t, "the decision to reach the main conversation", func() bool { return len(f.notices()) == 1 })
	n := f.notices()[0]
	for _, want := range []string{"改登录页", "要不要顺手改注册页", "leader 推荐：先只改登录页", "leader（一个编码 Agent"} {
		if !strings.Contains(n, want) {
			t.Errorf("the main conversation's message lacks %q:\n%s", want, n)
		}
	}
	if len(f.pushed()) != pushes {
		t.Fatalf("a card or push went to the user: %+v", f.pushed()[pushes:])
	}
	if cur := todoNow(s, w.ID); cur.Supervision.State != reviewNeedsUser || cur.Supervision.Supervisor != "box/s1" {
		t.Fatalf("the ledger: %+v", cur.Supervision)
	}
	// The main conversation decides with its own task_review; the worker stays the leader's.
	if res, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": w.ID, "verdict": "rework", "note": "只改登录页"}); err != nil {
		t.Fatalf("main's rework: %s %v", res, err)
	}
	if cur := todoNow(s, w.ID); cur.Supervision.Supervisor != "box/s1" {
		t.Fatalf("the main conversation took the worker over: %+v", cur.Supervision)
	}

	// Two reworks, then the third goes up.
	w2 := planWorker(t, s, f, map[string]any{"title": "改注册页"})
	for i := 1; i <= maxReworkRounds; i++ {
		if res := review("s1", map[string]any{"todo_id": w2.ID, "verdict": "rework", "note": "还不够"}); res.IsError {
			t.Fatalf("rework %d: %+v", i, res)
		}
	}
	res = review("s1", map[string]any{"todo_id": w2.ID, "verdict": "rework", "note": "还是不够"})
	if res.IsError || !strings.Contains(res.Content[0].Text, "sent back 2 times already") {
		t.Fatalf("the third rework: %+v", res)
	}
	if v := todoNow(s, w2.ID).Supervision; v.State != reviewNeedsUser {
		t.Fatalf("the ledger after the third rework: %+v", v)
	}
}

func leaderNeedsUserReview(t *testing.T, s *Service, todoID, recommend string) {
	t.Helper()
	res := s.mcpCall(context.Background(), "box", "s1", "task_review", mustJSON(map[string]any{
		"todo_id": todoID, "verdict": "needs_user", "reason": "The owner must choose the release action", "recommend": recommend,
	}), false)
	if res.IsError {
		t.Fatalf("leader needs_user review was rejected: %+v", res)
	}
}

func flushLeaderDecisionDigest(s *Service) {
	b := &s.supervise
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	for id, g := range b.pending {
		g.At = time.Now().Add(-time.Hour)
		b.pending[id] = g
	}
	b.last = time.Now().Add(-time.Hour)
	b.mu.Unlock()
	s.superviseTimes = shortSuperviseTimings
	s.sendDigest(context.Background())
}

func leaderDecisionFixture(t *testing.T) (*Service, *fakeHub, string) {
	t.Helper()
	s, f := newQuietService(t)
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	return s, f, w.ID
}

func TestTaskLeaderNeedsUserRecommendationRevisionIsNotDeduplicated(t *testing.T) {
	s, f, id := leaderDecisionFixture(t)
	leaderNeedsUserReview(t, s, id, "approve the release")
	flushLeaderDecisionDigest(s)
	if got := f.notices(); len(got) != 1 || !strings.Contains(got[0], "approve the release") {
		t.Fatalf("first decision did not reach the supervisor: %q", got)
	}

	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	leaderNeedsUserReview(t, s, id, "do not release; roll back first")
	if task := todoNow(s, id); task.DoneAt != nil || task.Supervision.State != reviewNeedsUser || len(task.Supervision.Reviews) != 2 {
		t.Fatalf("second review was not recorded on the same open needs-user task: %+v", task.Supervision)
	}
	flushLeaderDecisionDigest(s)
	got := f.notices()
	if len(got) != 2 || !strings.Contains(got[len(got)-1], "do not release; roll back first") {
		t.Fatalf("revised recommendation was suppressed; notices (%d): %q", len(got), got)
	}
}

func TestTaskLeaderNeedsUserIdenticalRecommendationIsDeduplicated(t *testing.T) {
	s, f, id := leaderDecisionFixture(t)
	leaderNeedsUserReview(t, s, id, "approve the release")
	flushLeaderDecisionDigest(s)
	if got := len(f.notices()); got != 1 {
		t.Fatalf("first decision notices = %d", got)
	}

	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	leaderNeedsUserReview(t, s, id, "approve the release")
	if task := todoNow(s, id); len(task.Supervision.Reviews) != 2 {
		t.Fatalf("repeated review was not recorded: %+v", task.Supervision)
	}
	flushLeaderDecisionDigest(s)
	if got := f.notices(); len(got) != 1 {
		t.Fatalf("an unchanged recommendation was notified again: %q", got)
	}
}

// A worker's own card stays with the user (askRoute is a main-chat ladder this task does not extend), and no start
// remedy is begun for a worker's failed start.
func TestWorkerCardsStayWithTheUser(t *testing.T) {
	s, f := newQuietService(t)
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	if !s.agentSupervisedSession("box", w.Agents[0].SessionID) {
		t.Fatal("a worker is not agent-supervised")
	}
	if s.agentSupervisedSession("box", "s1") {
		t.Fatal("the leader's own task counts as agent-supervised")
	}
	_ = f
}

// A task leader explicitly planned under a direction keeps that direction; its workers report to the leader Agent.
func TestTaskLeaderUnderALeaderConversation(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	f.known["lead1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	var in map[string]any
	_ = json.Unmarshal(leaderPlanArgs("leader"), &in)
	in["supervisor"] = "lead1"
	raw, _ := json.Marshal(in)
	res, err := s.toolTaskPlan(ctx, "conv1", raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.startPlan(ctx, planIDRe.FindString(res), nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the leader's link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	if v := todo.Supervision; v == nil || v.Supervisor != "lead1" || todo.Agents[0].Source != sourceLeader {
		t.Fatalf("the task leader's task: %+v %+v", todo.Supervision, todo.Agents)
	}
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle})
	w := planWorker(t, s, f, nil)

	if res := s.agentTool(ctx, "box", w.Agents[0].SessionID, "report_to_supervisor", json.RawMessage(`{"kind":"blocked","text":"worker 的进度"}`)); res.IsError {
		t.Fatalf("%+v", res)
	}
	if res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", json.RawMessage(`{"kind":"blocked","text":"leader 的阻塞"}`)); res.IsError {
		t.Fatalf("%+v", res)
	}
	waitFor(t, "direction's message", func() bool { return len(f.noticesTo("lead1")) == 1 })
	if n := f.noticesTo("lead1")[0]; !strings.Contains(n, "leader 的阻塞") || strings.Contains(n, "worker 的进度") {
		t.Fatalf("direction's message:\n%s", n)
	}
	if got := f.noticesTo("conv1"); len(got) != 0 {
		t.Fatalf("main got the direction’s ordinary supervision: %q", got)
	}
	if m := gotMessage(t, f, "worker 的进度"); strings.Contains(m, "leader 的阻塞") {
		t.Fatalf("the task leader's message carries its own report:\n%s", m)
	}
}

// Even where the judge closes a main-chat task that passed its own check (spot checks off), it does not close a
// leader's worker: the worker waits for its leader's review.
func TestJudgeDoesNotCloseALeadersWorkerThatPassedItsCheck(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	worker := w.Agents[0].SessionID
	sampleAt(t, s, 0)
	f.mu.Lock()
	f.judge = `{"verdict":"done","reason":"已合并并上线","delivery":"live","checks":[` +
		`{"criterion":"登录页布局改好","checkable":true,"met":true,"evidence":"PR 里有截图"},` +
		`{"criterion":"测试通过","checkable":true,"met":true,"evidence":"CI 绿"}]}`
	f.mu.Unlock()
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Agent: "claude", Status: proto.StatusIdle},
		proto.Session{SessionID: worker, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
	s.taskWorking(ctx, "box", worker, startRow{})
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Agent: "claude", Status: proto.StatusIdle},
		proto.Session{SessionID: worker, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone, State: proto.StateUnread,
			LastText: "做完了，证据见 PR\n交付：已上线并实测；卡点：无；谁来解：无", UpdatedAt: time.Now()})
	s.taskTurnEnded(ctx, "box", worker)
	gotMessage(t, f, "waits for your review")
	if cur := todoNow(s, w.ID); cur.DoneAt != nil || cur.Supervision.State != reviewPending {
		t.Fatalf("the judge closed a leader's worker: %+v %+v", cur, cur.Supervision)
	}
}

// Automatic signals about a worker (idle, stalled, orphaned, interrupted) go to the leader's session like a report.
func TestWorkerAutomaticSignalsGoToLeader(t *testing.T) {
	for _, kind := range []string{sigIdle, sigStalled, sigOrphaned, sigInterrupted} {
		t.Run(kind, func(t *testing.T) {
			s, f := newQuietService(t)
			ctx := context.Background()
			startTaskLeader(t, s, f)
			var extra map[string]any
			if kind == sigStalled {
				extra = map[string]any{"expect_minutes": 10} // an explicit short estimate also exercises estimate_due
			}
			w := planWorker(t, s, f, extra)
			switch kind {
			case sigIdle, sigInterrupted:
				s.signal(superviseSignal{Todo: w.ID, Kind: kind, Key: "k1", Machine: "box", Session: w.Agents[0].SessionID, Detail: "停下了"})
			case sigStalled:
				old := w.Agents[0]
				_ = old
				// watchSignals: an agent working without progress past the stall bound.
				stall := proto.Session{SessionID: w.Agents[0].SessionID, MachineID: "box", Cwd: "/code/app", Agent: "claude",
					Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now().Add(-superviseStallMin - time.Minute)}
				f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Agent: "claude", Status: proto.StatusIdle}, stall)
				f.setLocale("en")
				s.superviseWatch(ctx, time.Now().Add(11*time.Minute))
				due := gotMessage(t, f, "Persona · supervision", "改登录页", "automatic estimate reached")
				if !strings.Contains(due, "not an overdue judgment") {
					t.Fatalf("leader's estimate_due lacks its fact: %s", due)
				}
				waitFor(t, "leader delivery to persist estimate_due", func() bool {
					b, ok := s.briefOfTodo(w.ID)
					return ok && b.EstimateDueToldAt != nil
				})
			case sigOrphaned:
				// A session its computer no longer lists: the orphan pass marks it closed, and the worker's signal goes to the leader.
				f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Agent: "claude", Status: proto.StatusIdle})
				s.orphanWatch(ctx, time.Now())
				if g, ok := s.orphanSignal(todoNow(s, w.ID)); ok {
					s.signal(g)
				}
			}
			want := map[string]string{sigIdle: "stopped, but the work is not finished", sigStalled: "no progress for",
				sigOrphaned: "nobody works on it any more", sigInterrupted: "cut its turn off"}[kind]
			m := gotMessage(t, f, "Persona · supervision", "改登录页", want)
			if !strings.Contains(m, want) {
				t.Errorf("the leader's message for %s lacks %q:\n%s", kind, want, m)
			}
			if got := f.notices(); len(got) != 0 {
				t.Fatalf("the main conversation was told: %q", got)
			}
		})
	}
}

// A leader that cannot be reached: the signals are not dropped. While its task is open they become a decision for the
// conversation above it (who to tell, with the reason), so the worker is not left unreviewed; with nobody above it they
// stay pending and are tried again. A closed agent whose resume fails is handled the same way.
func TestUnreachableLeaderEscalatesInsteadOfDropping(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo := startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	// The leader's agent row is released (it was replaced): there is no leader agent to reach any more.
	leadRow, _, _ := s.leaderSupervisor(todoNow(s, w.ID))
	if leadRow.SessionID != "s1" {
		t.Fatalf("setup: %v", leadRow)
	}
	s.todos.mu.Lock()
	for i := range s.todos.todos[todo.ID].Agents {
		a := &s.todos.todos[todo.ID].Agents[i]
		if a.SessionID == "s1" {
			now := time.Now().UTC()
			a.ReleasedAt = &now
		}
	}
	s.todos.mu.Unlock()
	if lead, _, ok := s.leaderSupervisor(todoNow(s, w.ID)); ok {
		t.Fatalf("setup: the leader is still reachable: %+v", lead)
	}
	if up := s.supervisorAbove(ctx, leadRow); up != "conv1" {
		t.Fatalf("setup: the conversation above the released leader is %q", up)
	}
	f.setSessions("box", proto.Session{SessionID: w.Agents[0].SessionID, MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusDone, State: proto.StateUnread, LastText: "做完了", UpdatedAt: time.Now()})
	s.signal(superviseSignal{Todo: w.ID, Kind: sigReview, Key: "review@1", Detail: "做完了，等你验收"})
	waitFor(t, "the decision to go up to the main conversation", func() bool {
		return len(f.notices()) == 1
	})
	n := f.notices()[0]
	if !strings.Contains(n, "leader（一个编码 Agent") || !strings.Contains(n, "cannot be reached") {
		t.Fatalf("the main conversation's escalation:\n%s", n)
	}
	// The worker's own task stays open; the leader's task is named in the decision so the conversation can act.
	if cur := todoNow(s, w.ID); cur.DoneAt != nil {
		t.Fatalf("the worker was closed by an escalation: %+v", cur)
	}
}

// A leader whose own task closed: its open workers are rehomed to the conversation above it (which becomes their
// supervisor and can review them), not left pending for ever.
func TestClosedLeaderRehomesItsWorkers(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo := startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	if _, _, err := s.updateSupervision(ctx, w.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "done", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("closing the leader's task: %v %v", ok, err)
	}
	s.rehomeLeaderlessWorkers(ctx)
	cur := todoNow(s, w.ID)
	if cur.Supervision.Supervisor != "conv1" || cur.Supervision.State != reviewPending || cur.DoneAt != nil {
		t.Fatalf("the rehomed worker: %+v %+v", cur, cur.Supervision)
	}
	// The conversation can review it, and does not take over a worker whose leader is alive.
	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": w.ID, "verdict": "accepted", "note": "测试通过",
		"checks": completeReviewChecks(todoNow(s, w.ID))}); err != nil {
		t.Fatalf("the main conversation reviewing a rehomed worker: %v", err)
	}
	if got := todoNow(s, w.ID); got.DoneAt == nil || got.DoneBy != "review" {
		t.Fatalf("the accepted rehomed worker: %+v", got)
	}
}

// With no conversation above it at all the worker is cancelled with a reason, not left with nobody to review it.
func TestLeaderlessWorkerWithNobodyAboveIsCancelled(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo := startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	setSettings(t, s, func(v *Settings) { v.Main = "" })
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.Supervisor = ""; return true }); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "done", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("closing the leader's task: %v %v", ok, err)
	}
	s.rehomeLeaderlessWorkers(ctx)
	if cur := todoNow(s, w.ID); cur.DoneAt == nil || !cur.Cancelled() {
		t.Fatalf("the worker with nobody above it: %+v", cur)
	}
}

// A closed leader agent whose resume fails: the worker's signal goes up as a decision that names the worker and says the
// leader could not be reached, and (here with nobody above) the message is tried again on the leader's own context,
// not a cancelled one, until the leader takes it.
func TestLeaderResumeFailureEscalatesAndRetries(t *testing.T) {
	s, f := newQuietService(t)
	s.leaderResume = 40 * time.Millisecond
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	lead, _, _ := s.leaderSupervisor(todoNow(s, w.ID))
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusOffline, UpdatedAt: time.Now()},
		proto.Session{SessionID: w.Agents[0].SessionID, MachineID: "box", Agent: "claude", Status: proto.StatusWorking})
	f.mu.Lock()
	f.failSend = context.DeadlineExceeded
	f.mu.Unlock()
	kept := []superviseSignal{{Todo: w.ID, Kind: sigReview, Key: "review@1", Detail: "做完了"}}
	text := prompts.RenderWorkerSupervise([]prompts.SuperviseEntry{{ID: w.ID, Title: w.Title, Kind: "review"}})
	s.resumeLeaderForSupervision(lead, text, kept, kept, kept, func(context.Context, error) {}, nil)
	waitFor(t, "the failed resume to go up", func() bool { return len(f.notices()) >= 1 })
	n := f.notices()[0]
	if !strings.Contains(n, "改登录页") || !strings.Contains(n, w.ID) || !strings.Contains(n, "联系不上") || strings.Contains(n, "判断要用户决定") {
		t.Fatalf("the escalation must name the worker and say the leader is unreachable:\n%s", n)
	}
	// The leader comes back: the retry (own context, several attempts) delivers it.
	f.mu.Lock()
	f.failSend = nil
	f.mu.Unlock()
	waitFor(t, "the retry to reach the leader", func() bool { return slices.Contains(deliveredTo(f, "s1"), text) })
}

// With nobody above the leader nothing is lost either: no conversation to tell, so the leader itself is retried.
func TestUndeliveredMessageWithNobodyAboveIsRetried(t *testing.T) {
	s, f := newQuietService(t)
	s.leaderResume = 30 * time.Millisecond
	ctx := context.Background()
	todo := startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	setSettings(t, s, func(v *Settings) { v.Main = "" })
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.Supervisor = ""; return true }); err != nil {
		t.Fatal(err)
	}
	lead, _, _ := s.leaderSupervisor(todoNow(s, w.ID))
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Agent: "claude", Status: proto.StatusOffline})
	f.mu.Lock()
	f.failSend = context.DeadlineExceeded
	f.mu.Unlock()
	kept := []superviseSignal{{Todo: w.ID, Kind: sigReview, Key: "review@1", Detail: "做完了"}}
	const text = "Persona · supervision: undelivered"
	s.resumeLeaderForSupervision(lead, text, kept, kept, kept, func(context.Context, error) {}, nil)
	time.AfterFunc(150*time.Millisecond, func() { f.mu.Lock(); f.failSend = nil; f.mu.Unlock() })
	waitFor(t, "the undelivered message to reach the leader after a few tries", func() bool {
		return slices.Contains(deliveredTo(f, "s1"), text)
	})
	if got := f.notices(); len(got) != 0 {
		t.Fatalf("with nobody above, something was told to the main conversation: %q", got)
	}
}

// stopLeader lists task leader s1 on box as an agent that has stopped (read-only: closed, or cut off by a restart, node
// stopped.go) beside its working worker, and has the node refuse every send to it as it refuses a stopped agent's:
// agent_stopped, nothing typed.
func stopLeader(f *fakeHub, worker string) {
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusInterrupted, Capabilities: &proto.SessionCapabilities{Read: true, Terminal: true}, UpdatedAt: time.Now()},
		proto.Session{SessionID: worker, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking,
			UpdatedAt: time.Now()})
	f.mu.Lock()
	f.failSend = &rpc.Error{Code: proto.CodeAgentStopped, Message: "这个 Agent 已经停了，这次没有发送",
		Data: json.RawMessage(`{"delivered":"none"}`)}
	f.mu.Unlock()
}

// leaderEscalations counts the decisions the main conversation got saying a worker's task leader cannot be reached.
func leaderEscalations(f *fakeHub) int {
	n := 0
	for _, m := range f.notices() {
		n += strings.Count(m, "联系不上")
	}
	return n
}

// blockedReport has worker report that it is blocked, which goes to its leader.
func blockedReport(t *testing.T, s *Service, worker string) {
	t.Helper()
	res := s.agentTool(context.Background(), "box", worker, "report_to_supervisor",
		json.RawMessage(`{"kind":"blocked","text":"登录页卡在设计评审"}`))
	if res.IsError {
		t.Fatalf("the worker's report: %+v", res)
	}
}

// A listed task leader whose agent has stopped (#8518): the node refuses the message. That goes as a failed resume
// does: the conversation above the leader hears of it once, and the leader is not sent the message again at every
// digest (its next try waits leaderResume, here longer than the test).
func TestStoppedLeaderIsEscalatedOnceNotRetriedEveryDigest(t *testing.T) {
	s, f := newQuietService(t)
	s.leaderResume = time.Hour
	ctx := context.Background()
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	stopLeader(f, w.Agents[0].SessionID)
	blockedReport(t, s, w.Agents[0].SessionID)
	waitFor(t, "the send the stopped leader's node refuses", func() bool { return len(sendsTo(f, "box", "s1")) > 0 })
	for range 5 {
		s.sendDigest(ctx) // the digests that follow, every 30 seconds on a running hub
	}
	if got := sendsTo(f, "box", "s1"); len(got) != 1 {
		t.Fatalf("the stopped leader was sent the message %d times: at every digest", len(got))
	}
	waitFor(t, "the conversation above the leader to hear of it", func() bool { return leaderEscalations(f) > 0 })
	for range 5 {
		s.sendDigest(ctx)
	}
	if n := leaderEscalations(f); n != 1 {
		t.Fatalf("the conversation above the leader heard of it %d times:\n%s", n, strings.Join(f.notices(), "\n---\n"))
	}
	if n := f.notices()[0]; !strings.Contains(n, w.ID) || !strings.Contains(n, "改登录页") || !strings.Contains(n, "登录页卡在设计评审") {
		t.Fatalf("the escalation must name the worker and say what it reported:\n%s", n)
	}
	if got := sendsTo(f, "box", "s1"); len(got) != 1 {
		t.Fatalf("the stopped leader was sent the message %d times", len(got))
	}
	if cur := todoNow(s, w.ID); cur.DoneAt != nil {
		t.Fatalf("the worker was closed by the escalation: %+v", cur)
	}
}

// The stopped leader is still tried again, after a wait, though no digest sends it the message any more: once it runs
// again (the user's Resume) the message reaches it, and the conversation above was told only the once.
func TestStoppedLeaderGetsTheMessageOnceItRunsAgain(t *testing.T) {
	s, f := newQuietService(t)
	s.leaderResume = 50 * time.Millisecond
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	stopLeader(f, w.Agents[0].SessionID)
	blockedReport(t, s, w.Agents[0].SessionID)
	waitFor(t, "the conversation above the leader to hear of it", func() bool { return leaderEscalations(f) > 0 })
	waitFor(t, "the leader to be tried again after its wait", func() bool { return len(sendsTo(f, "box", "s1")) > 1 })
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusIdle, UpdatedAt: time.Now()},
		proto.Session{SessionID: w.Agents[0].SessionID, MachineID: "box", Cwd: "/code/app", Agent: "claude",
			Status: proto.StatusWorking, UpdatedAt: time.Now()})
	f.mu.Lock()
	f.failSend = nil
	f.mu.Unlock()
	waitFor(t, "the message to reach the leader that runs again", func() bool {
		for _, m := range deliveredTo(f, "s1") {
			if strings.Contains(m, "登录页卡在设计评审") {
				return true
			}
		}
		return false
	})
	if n := leaderEscalations(f); n != 1 {
		t.Fatalf("the conversation above the leader heard of it %d times:\n%s", n, strings.Join(f.notices(), "\n---\n"))
	}
}

// A send to a running leader that fails for any other reason (here a dialog in its box) is still put back for the
// next digest, and nothing goes up: the leader is there, only not now.
func TestLeaderSendFailingOtherwiseIsRetriedByTheDigest(t *testing.T) {
	s, f := newQuietService(t)
	s.leaderResume = time.Hour
	ctx := context.Background()
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	f.mu.Lock()
	f.failSend = &rpc.Error{Code: screen.DialogOpen, Message: "a dialog has the keyboard", Data: json.RawMessage(`{"delivered":"none"}`)}
	f.mu.Unlock()
	blockedReport(t, s, w.Agents[0].SessionID)
	waitFor(t, "the refused send", func() bool { return len(sendsTo(f, "box", "s1")) > 0 })
	s.sendDigest(ctx)
	waitFor(t, "the next digest to try the leader again", func() bool { return len(sendsTo(f, "box", "s1")) > 1 })
	f.mu.Lock()
	f.failSend = nil
	f.mu.Unlock()
	waitFor(t, "the message to reach the leader", func() bool {
		for _, m := range deliveredTo(f, "s1") {
			if strings.Contains(m, "登录页卡在设计评审") {
				return true
			}
		}
		return false
	})
	if n := leaderEscalations(f); n != 0 {
		t.Fatalf("a leader that is there was escalated:\n%s", strings.Join(f.notices(), "\n---\n"))
	}
}

// A send the node says may already be in the leader's session but was not seen taking (its Claude's inbox read it; held
// for approval at the computer, dropped, typed part way: delivered unknown) is neither resent by the next digests, which
// would only pile copies there, nor counted as told (#9607): the conversation above hears of it once, the turn stays
// untold and the deduplication is released, so a later digest or a restarted manager can still deliver it.
func TestLeaderSendUnconfirmedIsEscalatedNotResent(t *testing.T) {
	for _, code := range []string{"peer_held", "peer_unconfirmed", "input_uncertain"} {
		t.Run(code, func(t *testing.T) {
			s, f := newQuietService(t)
			s.leaderResume = time.Hour
			ctx := context.Background()
			startTaskLeader(t, s, f)
			w := planWorker(t, s, f, nil)
			f.mu.Lock()
			f.failSend = &rpc.Error{Code: code, Message: "not taken there", Data: json.RawMessage(`{"delivered":"unknown"}`)}
			f.mu.Unlock()
			blockedReport(t, s, w.Agents[0].SessionID)
			waitFor(t, "the unconfirmed send", func() bool { return len(sendsTo(f, "box", "s1")) > 0 })
			for range 3 {
				s.sendDigest(ctx)
			}
			waitFor(t, "the conversation above the leader to hear of it", func() bool { return leaderEscalations(f) > 0 })
			if n := len(sendsTo(f, "box", "s1")); n != 1 {
				t.Errorf("the leader was sent the unconfirmed message %d times", n)
			}
			if n := leaderEscalations(f); n != 1 {
				t.Errorf("the conversation above heard of it %d times:\n%s", n, strings.Join(f.notices(), "\n---\n"))
			}
			if !strings.Contains(strings.Join(f.notices(), "\n"), "may not have received") {
				t.Errorf("the escalation does not say the leader may not have it:\n%s", strings.Join(f.notices(), "\n---\n"))
			}
		})
	}
}

// An unconfirmed supervision message does not mark the turn it tells as told: the ledger a restarted manager recovers
// from still has it to tell (#9607).
func TestLeaderSendUnconfirmedLeavesTheTurnUntold(t *testing.T) {
	s, f := newQuietService(t)
	s.leaderResume = time.Hour
	ctx := context.Background()
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	if _, _, err := s.updateSupervision(ctx, w.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	s.supervise.mu.Lock()
	s.supervise.init()
	s.supervise.mu.Unlock()
	g := superviseSignal{Todo: w.ID, Kind: sigReview, Key: reviewKey(todoNow(s, w.ID).Supervision), Machine: "box",
		Session: w.Agents[0].SessionID, Detail: "done", At: time.Now()}
	b := &s.tasks.turns
	b.mu.Lock()
	if !s.turnsLocked(ctx) {
		b.mu.Unlock()
		t.Fatal("cannot load the turn ledger")
	}
	b.doc.Turns[w.ID] = taskTurn{Machine: "box", Session: g.Session, Key: g.Key, State: turnInterrupted,
		Signal: &turnSignal{Kind: g.Kind, Key: g.Key, Detail: g.Detail}}
	b.mu.Unlock()
	f.mu.Lock()
	f.failSend = &rpc.Error{Code: "peer_unconfirmed", Message: "dropped there", Data: json.RawMessage(`{"delivered":"unknown"}`)}
	f.mu.Unlock()
	lead, _, ok := s.leaderSupervisor(todoNow(s, w.ID))
	if !ok {
		t.Fatal("no leader")
	}
	s.supervise.dedupMu.Lock()
	delivered := s.deliverToLeaderAgent(ctx, lead, []superviseSignal{g}, "", time.Now(), time.Time{})
	s.supervise.dedupMu.Unlock()
	b.mu.Lock()
	told := b.doc.Turns[w.ID].Signal.Told != nil
	b.mu.Unlock()
	if delivered || told {
		t.Errorf("an unconfirmed send: delivered %v, turn told %v", delivered, told)
	}
}

// A supervision message states no permission-mode class (#9607): the assistant cannot bind a worker's report to the
// mode the worker wrote it in (its session row is a cached list, which may still say bypass after the worker switched),
// so nothing it sends a leader claims one, whatever the worker's or the leader's listed mode.
func TestLeaderSupervisionStatesNoClass(t *testing.T) {
	for _, worker := range []string{"default", "bypassPermissions", "plan"} {
		t.Run(worker, func(t *testing.T) {
			s, f := newQuietService(t)
			ctx := context.Background()
			startTaskLeader(t, s, f)
			w := planWorker(t, s, f, nil)
			sid := w.Agents[0].SessionID
			f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
				Status: proto.StatusIdle, PermissionMode: proto.ModeBypass, UpdatedAt: time.Now()},
				proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "claude",
					Status: proto.StatusWorking, PermissionMode: worker, UpdatedAt: time.Now()})
			const marker = "the login page is stuck in design review"
			raw, _ := json.Marshal(map[string]any{"kind": "blocked", "text": marker})
			if res := s.agentTool(ctx, "box", sid, "report_to_supervisor", raw); res.IsError {
				t.Fatalf("report refused: %+v", res)
			}
			waitFor(t, "the report to reach the leader", func() bool { return len(sendsTo(f, "box", "s1")) > 0 })
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, call := range f.calls {
				if call.Method != DeviceSessionSend {
					continue
				}
				b, _ := json.Marshal(call.Params)
				var p map[string]any
				_ = json.Unmarshal(b, &p)
				if text, _ := p["text"].(string); p["session_id"] == "s1" && strings.Contains(text, marker) {
					for k := range p {
						if strings.EqualFold(k, "from_mode") || strings.EqualFold(k, "owner_input") {
							t.Errorf("a supervision message claims %s: %v", k, p[k])
						}
					}
					return
				}
			}
			t.Fatal("no supervision message to the leader")
		})
	}
}

// A retry that gets an unconfirmed supervision message to the leader records it as a digest would: its turn is told
// then, so a manager restarted afterwards does not deliver it again (#9607).
func TestLeaderRetryDeliveredMarksTheTurnToldAndDeduplicatesSignals(t *testing.T) {
	for _, status := range []string{proto.StatusIdle, proto.StatusOffline} {
		t.Run(status, func(t *testing.T) { checkLeaderRetryDelivery(t, status) })
	}
}

func checkLeaderRetryDelivery(t *testing.T, status string) {
	t.Helper()
	s, f := newQuietService(t)
	s.leaderResume = 50 * time.Millisecond
	ctx := context.Background()
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	blocked := planWorker(t, s, f, nil)
	if _, _, err := s.updateSupervision(ctx, w.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	s.supervise.mu.Lock()
	s.supervise.init()
	s.supervise.mu.Unlock()
	g := superviseSignal{Todo: w.ID, Kind: sigReview, Key: reviewKey(todoNow(s, w.ID).Supervision), Machine: "box",
		Session: w.Agents[0].SessionID, Detail: "done", At: time.Now()}
	// The blocked report renders before the review, which is first in the input.
	signals := []superviseSignal{g, {Todo: blocked.ID, Kind: sigReport, Report: upReportBlocked, Machine: "box",
		Session: blocked.Agents[0].SessionID, Detail: "waiting for access", At: g.At}}
	b := &s.tasks.turns
	b.mu.Lock()
	if !s.turnsLocked(ctx) {
		b.mu.Unlock()
		t.Fatal("cannot load the turn ledger")
	}
	b.doc.Turns[w.ID] = taskTurn{Machine: "box", Session: g.Session, Key: g.Key, State: turnInterrupted,
		Signal: &turnSignal{Kind: g.Kind, Key: g.Key, Detail: g.Detail}}
	b.mu.Unlock()
	lead, _, ok := s.leaderSupervisor(todoNow(s, w.ID))
	if !ok {
		t.Fatal("no leader")
	}
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: status, UpdatedAt: time.Now()})
	f.mu.Lock()
	f.failSend = &rpc.Error{Code: "peer_unconfirmed", Message: "not taken", Data: json.RawMessage(`{"delivered":"unknown"}`)}
	f.mu.Unlock()
	s.supervise.dedupMu.Lock()
	first := s.deliverToLeaderAgent(ctx, lead, signals, "", time.Now(), time.Time{})
	s.supervise.dedupMu.Unlock()
	b.mu.Lock()
	toldBefore := b.doc.Turns[w.ID].Signal.Told != nil
	b.mu.Unlock()
	if first || toldBefore {
		t.Fatalf("the unconfirmed send: delivered %v, told %v", first, toldBefore)
	}
	waitFor(t, "the first attempt to escalate", func() bool { return leaderEscalations(f) > 0 })
	f.mu.Lock()
	f.failSend = nil
	f.mu.Unlock()
	waitFor(t, "the retry to reach the leader", func() bool { return len(deliveredTo(f, "s1")) > 0 })
	waitFor(t, "the retried turn to be told", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.doc.Turns[w.ID].Signal.Told != nil
	})

	var entries []prompts.SuperviseEntry
	for _, signal := range signals {
		entry, ok := s.digestEntry(ctx, signal, "")
		if !ok {
			t.Fatalf("signal %s has no digest entry", signal.Kind)
		}
		entries = append(entries, entry)
	}
	s.supervise.dedupMu.Lock()
	defer s.supervise.dedupMu.Unlock()
	remaining, _, rollback, err := s.reserveSupervision(ctx, entries, signals, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer rollback()
	if len(remaining) != 0 {
		t.Errorf("the retry left %d of %d delivered signals eligible for redelivery", len(remaining), len(signals))
	}
}

// A flexible task leader started again on another computer: the worker's fallback by the leader task's link reaches its
// current agent there, not only one on the old computer.
func TestLeaderRestartedOnAnotherComputerIsStillReached(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo := startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	// The old agent is released; the task leader's new agent runs on another computer.
	s.todos.mu.Lock()
	for i := range s.todos.todos[todo.ID].Agents {
		a := &s.todos.todos[todo.ID].Agents[i]
		if a.SessionID == "s1" {
			now := time.Now().UTC()
			a.ReleasedAt = &now
		}
	}
	s.todos.mu.Unlock()
	if err := s.linkTodoAgentWithSource(ctx, todo.ID, "box2", "s9", "leader", "/code/app", "", "", sourceLeader, ""); err != nil {
		t.Fatal(err)
	}
	lead, supervised, ok := s.leaderSupervisor(todoNow(s, w.ID))
	if !supervised || !ok || lead.MachineID != "box2" || lead.SessionID != "s9" {
		t.Fatalf("the worker's leader after the leader moved: %+v supervised %v ok %v", lead, supervised, ok)
	}
}

// A leader of a writable task on a work-trust computer is refused at planning and at start, readably, because that
// computer's agents get no task_plan / task_review.
func TestTaskLeaderRefusedOnWorkTrustMachine(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	f.setHello(func(h *Hello) { h.Assistant.WorkMachines = []string{"box"} })
	args := leaderPlanArgs("leader")
	if _, err := s.toolTaskPlan(ctx, "conv1", args); err == nil || !strings.Contains(err.Error(), "work-trust computer") {
		t.Fatalf("planning a leader on a work-trust computer: %v", err)
	}
	// A saved plan that names one is held at Start too.
	f.setHello(func(h *Hello) { h.Assistant.WorkMachines = nil })
	res, err := s.toolTaskPlan(ctx, "conv1", args)
	if err != nil {
		t.Fatal(err)
	}
	f.setHello(func(h *Hello) { h.Assistant.WorkMachines = []string{"box"} })
	_, err = s.startPlan(ctx, planIDRe.FindString(res), nil, false)
	var invalid planInvalid
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "work-trust computer") {
		t.Fatalf("starting a saved leader plan on a work-trust computer: %v", err)
	}
	if len(f.starts) != 0 {
		t.Fatalf("it started: %v", f.starts)
	}
}

// A task leader never lands on a work-trust computer by any other road either: a retry of a saved plan, a recovered or
// remedy start (startTaskAgent), nor a flexible task moved by the health check.
func TestTaskLeaderNeverLandsOnWorkTrustMachine(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	f.setHello(func(h *Hello) { h.Assistant.WorkMachines = []string{"box"} })
	_, _, err := s.startTaskAgent(ctx, &PlanTask{Title: "L", Prompt: "p", Machine: "box", Cwd: "/code/app", Role: roleLeader, Permission: "bypassPermissions"}, true)
	if err == nil || !strings.Contains(err.Error(), "work-trust computer") {
		t.Fatalf("starting a task leader on a work-trust computer: %v", err)
	}
	f.mu.Lock()
	starts := len(f.starts)
	f.mu.Unlock()
	if starts != 0 {
		t.Fatalf("a start went out: %v", f.starts)
	}
	// Retry of a saved leader plan on a computer that is work-trust now.
	f.setHello(func(h *Hello) { h.Assistant.WorkMachines = nil })
	res, err := s.toolTaskPlan(ctx, "conv1", leaderPlanArgs("leader"))
	if err != nil {
		t.Fatal(err)
	}
	id := planIDRe.FindString(res)
	started, err := s.startPlan(ctx, id, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	todo := started.Tasks[0].TodoID
	waitFor(t, "the first start", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.starts) == 1 })
	waitFor(t, "its link", func() bool { return len(todoNow(s, todo).Agents) == 1 })
	if err := s.failTodo(ctx, todo, "did not start"); err != nil {
		t.Fatal(err)
	}
	f.setHello(func(h *Hello) { h.Assistant.WorkMachines = []string{"box"} })
	if _, err := s.retryTask(ctx, todo); err == nil || !strings.Contains(err.Error(), "work-trust computer") {
		t.Fatalf("retrying a leader plan on a work-trust computer: %v", err)
	}
}

// healthyPlace never moves a flexible task leader to a work-trust computer, while an ordinary task may go there.
func TestHealthDetourSkipsWorkTrustForTaskLeaders(t *testing.T) {
	s, f := rerouteHub(t)
	f.setHello(func(h *Hello) { h.Assistant.WorkMachines = []string{"box"} })
	blockWorkLauncher(s, "l4")
	failWorkLauncherCheck(s, "server") // box is the only healthy alternative
	ctx := context.Background()
	leader := PlanTask{Title: "L", Machine: "l4", Cwd: "~/code/app", Launcher: "work", MachinePolicy: machineFlexible, Role: roleLeader}
	if s.healthyPlace(ctx, &leader) || leader.Machine != "l4" {
		t.Fatalf("a task leader was moved to a work-trust computer: %+v", leader)
	}
	ordinary := PlanTask{Title: "O", Machine: "l4", Cwd: "~/code/app", Launcher: "work", MachinePolicy: machineFlexible}
	if !s.healthyPlace(ctx, &ordinary) || ordinary.Machine != "box" {
		t.Fatalf("control: an ordinary task is moved as before: %+v", ordinary)
	}
}

// A plan saved without a permission retries as it is: no permission is required of a writable task.
func TestRetryNeedsNoPermission(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	res, err := s.toolTaskPlan(ctx, "conv1", json.RawMessage(`{"tasks":[{"title":"Fix","permission":"bypassPermissions","prompt":"Fix it","machine":"box","cwd":"/code/app"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planIDRe.FindString(res)
	started, err := s.startPlan(ctx, id, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	todo := started.Tasks[0].TodoID
	waitFor(t, "the first start", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.starts) == 1 })
	waitFor(t, "its link", func() bool { return len(todoNow(s, todo).Agents) == 1 })
	if err := s.failTodo(ctx, todo, "did not start"); err != nil {
		t.Fatal(err)
	}
	// The plan as an older one saved it: a writable task with no permission.
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	p.Tasks[0].Permission = ""
	if err := s.savePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.retryTask(ctx, todo); err != nil {
		t.Fatalf("retrying a saved plan with no permission: %v", err)
	}
	waitFor(t, "the retry's start", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.starts) == 2 })
	if cur := todoNow(s, todo); cur.DoneAt != nil {
		t.Fatalf("the failed todo was not reopened: %+v", cur)
	}
}

// main → task leader → worker: the task leader's needs_user reaches main, which can review the worker without taking
// it over. When the task leader's own task closes, an open worker is rehomed to main (#5732).
func TestNeedsUserFromTaskLeaderIsReviewedByMain(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	f.known["lead1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	var in map[string]any
	_ = json.Unmarshal(leaderPlanArgs("leader"), &in)
	raw, _ := json.Marshal(in)
	res, err := s.toolTaskPlan(ctx, "conv1", raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.startPlan(ctx, planIDRe.FindString(res), nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the leader's link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	leaderTodo := onlyTodo(s)
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle})
	w := planWorker(t, s, f, nil)
	review := func(self string, args map[string]any) (string, error) {
		return runJSON(t, s, self, "task_review", args)
	}

	if r := s.mcpCall(ctx, "box", "s1", "task_review", mustJSON(map[string]any{"todo_id": w.ID, "verdict": "needs_user", "reason": "要不要顺手改注册页", "recommend": "先只改登录页"}), false); r.IsError {
		t.Fatalf("needs_user: %+v", r)
	}
	waitFor(t, "the decision to reach main", func() bool { return len(f.notices()) == 1 })
	if n := f.notices()[0]; !strings.Contains(n, "要不要顺手改注册页") {
		t.Fatalf("main's message:\n%s", n)
	}
	if got := f.noticesTo("lead1"); len(got) != 0 {
		t.Fatalf("the leader conversation got a second supervision stream: %q", got)
	}
	// Not its own to review: another conversation, and a task that no leader of lead1 leads.
	if _, err := review("conv2", map[string]any{"todo_id": w.ID, "verdict": "accepted"}); err == nil {
		t.Fatal("a conversation that is not above the task leader reviewed its worker")
	}
	if res, err := review("conv1", map[string]any{"todo_id": w.ID, "verdict": "rework", "note": "只改登录页"}); err != nil || !strings.Contains(res, "Rework round 1") {
		t.Fatalf("main's rework: %s %v", res, err)
	}
	if cur := todoNow(s, w.ID); cur.Supervision.Supervisor != "box/s1" || cur.Supervision.State != reviewRework {
		t.Fatalf("main took the worker over, or its state: %+v", cur.Supervision)
	}
	if res, err := review("conv1", map[string]any{"todo_id": w.ID, "verdict": "accepted", "note": "好了",
		"checks": completeReviewChecks(todoNow(s, w.ID))}); err != nil || !strings.Contains(res, "accepted and closed") {
		t.Fatalf("main's accepted: %s %v", res, err)
	}
	if cur := todoNow(s, w.ID); cur.DoneAt == nil || cur.Supervision.State != reviewAccepted || cur.Supervision.Supervisor != "box/s1" {
		t.Fatalf("the accepted worker: %+v %+v", cur, cur.Supervision)
	}

	// H1: another open worker when the task leader's own task closes is rehomed to main, which can close it.
	w2 := planWorker(t, s, f, map[string]any{"title": "改注册页"})
	if res, err := review("conv1", map[string]any{"todo_id": leaderTodo.ID, "verdict": "accepted", "note": "done",
		"checks": completeReviewChecks(todoNow(s, leaderTodo.ID))}); err != nil ||
		!strings.Contains(res, "accepted and closed") {
		t.Fatalf("closing the leader's task: %s %v", res, err)
	}
	s.rehomeLeaderlessWorkers(ctx)
	if cur := todoNow(s, w2.ID); cur.Supervision.Supervisor != "conv1" || cur.DoneAt != nil {
		t.Fatalf("the rehomed worker: %+v", cur.Supervision)
	}
	if res, err := review("conv1", map[string]any{"todo_id": w2.ID, "verdict": "cancelled", "note": "不用做了"}); err != nil || !strings.Contains(res, "cancelled and closed") {
		t.Fatalf("main's cancel of the rehomed worker: %s %v", res, err)
	}
}

func mustJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

// Every start of a plan task (runPlanTaskStart) needs no permission: without one the task takes the default mode.
func TestPlanTaskStartNeedsNoPermission(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	res, err := s.toolTaskPlan(ctx, "conv1", json.RawMessage(`{"tasks":[{"title":"Fix","permission":"bypassPermissions","prompt":"Fix it","machine":"box","cwd":"/code/app"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planIDRe.FindString(res)
	started, err := s.startPlan(ctx, id, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	todo := started.Tasks[0].TodoID
	waitFor(t, "the first start", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.starts) == 1 })
	waitFor(t, "its link", func() bool { return len(todoNow(s, todo).Agents) == 1 })
	for _, perm := range []string{"", "default", "acceptEdits"} {
		task := started.Tasks[0]
		task.Permission = perm
		want := len(f.starts) + 1
		s.startPlanTask(id, "conv1", 0, task, true)
		waitFor(t, "the start with permission "+perm, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.starts) == want })
		if cur := todoNow(s, todo); cur.StartError != "" {
			t.Fatalf("permission %q: the to-do after the start: %+v", perm, cur)
		}
	}
}

// A rehomed worker's supervisor and origin are stored: a restarted manager reads both back.
func TestRehomedWorkerSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	database := taskRecoveryDatabase(t)
	s, f := newQuietServiceOn(t, database)
	todo := startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "done", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("closing the leader's task: %v %v", ok, err)
	}
	s.rehomeLeaderlessWorkers(ctx)
	restarted, _ := newQuietServiceOn(t, database)
	cur := todoNow(restarted, w.ID)
	if cur.Origin != "conv1" || cur.Supervision == nil || cur.Supervision.Supervisor != "conv1" || cur.DoneAt != nil {
		t.Fatalf("the rehomed worker after a restart: origin %q %+v", cur.Origin, cur.Supervision)
	}
}

// A leader's workers follow the brief rule (goal for read-only, done_when for writable), while the hub supplies an
// omitted estimate automatically, as for any task_plan task.
func TestLeaderWorkersNeedABrief(t *testing.T) {
	s, f := newQuietService(t)
	startTaskLeader(t, s, f)
	s.briefsOptional = false
	call := func(task map[string]any) proto.ToolResult {
		task["role"] = "worker"
		raw, _ := json.Marshal(map[string]any{"tasks": []any{task}})
		return s.mcpCall(context.Background(), "box", "s1", "task_plan", raw, false)
	}
	res := call(map[string]any{"title": "改登录页", "prompt": "改", "readonly": false, "permission": "bypassPermissions", "done_when": []string{"测试通过"}})
	if res.IsError {
		t.Fatalf("a writable worker with criteria and an automatic estimate: %+v", res)
	}
	res = call(map[string]any{"title": "查一下", "prompt": "查", "readonly": true, "expect_minutes": 10})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "goal") {
		t.Fatalf("a read-only worker without a goal: %+v", res)
	}
	if res = call(map[string]any{"title": "查一下", "prompt": "查", "readonly": true, "goal": "知道登录页为什么崩", "expect_minutes": 10}); res.IsError {
		t.Fatalf("a read-only worker with its brief: %+v", res)
	}
}

// A worker's turn whose supervision message went in only on a retry (the first send unconfirmed) is told then, in the
// stored ledger: a manager restarted afterwards does not deliver the same review to the leader again (#9607).
func TestLeaderRetryDeliveredIsNotRedeliveredAfterRestart(t *testing.T) {
	ctx := context.Background()
	database := &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}
	s, f := newQuietServiceOn(t, database)
	s.leaderResume = 50 * time.Millisecond
	f.judge = `{"verdict":"done","reason":"PR 已合并"}`
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	worker := w.Agents[0].SessionID
	s.superviseWatch(ctx, time.Now())
	f.setSessions("box",
		proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle},
		proto.Session{SessionID: worker, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
	s.taskWorking(ctx, "box", worker, startRow{})
	f.mu.Lock()
	f.failSend = &rpc.Error{Code: "peer_unconfirmed", Message: "not taken", Data: json.RawMessage(`{"delivered":"unknown"}`)}
	f.mu.Unlock()
	f.setSessions("box",
		proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle, UpdatedAt: time.Now()},
		proto.Session{SessionID: worker, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone, State: proto.StateUnread, LastText: "做完了，PR https://github.com/o/r/pull/7 已合并", UpdatedAt: time.Now()})
	s.taskTurnEnded(ctx, "box", worker)
	turnsQuiet(t, s, w.ID)
	waitFor(t, "the unconfirmed send", func() bool { return len(sendsTo(f, "box", "s1")) > 0 })
	s.supervise.dedupMu.Lock()
	s.supervise.dedupMu.Unlock()
	f.mu.Lock()
	f.failSend = nil
	f.mu.Unlock()
	waitFor(t, "the retry to reach the leader", func() bool { return len(deliveredTo(f, "s1")) > 0 })
	waitFor(t, "the retried turn to be told", func() bool {
		turn, ok := s.turnOf(ctx, w.ID)
		return ok && turn.Signal != nil && turn.Signal.Told != nil
	})

	s.supervise.mu.Lock()
	if s.supervise.timer != nil {
		s.supervise.timer.Stop()
		s.supervise.timer = nil
	}
	s.supervise.mu.Unlock()
	restarted, hub := restartedOn(t, database)
	// The fixture's store keeps no todo rows: the restarted manager gets the same ones, under its lock (its own
	// goroutines already read them).
	s.todos.mu.RLock()
	rows := make(map[string]*Todo, len(s.todos.todos))
	for id, row := range s.todos.todos {
		cloned := copyTodoLocked(row)
		rows[id] = &cloned
	}
	s.todos.mu.RUnlock()
	restarted.todos.mu.Lock()
	restarted.todos.todos, restarted.todos.loaded = rows, true
	restarted.todos.mu.Unlock()
	hub.setSessions("box",
		proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle, UpdatedAt: time.Now()},
		proto.Session{SessionID: worker, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone, State: proto.StateUnread, LastText: "做完了，PR https://github.com/o/r/pull/7 已合并", UpdatedAt: time.Now()})
	restarted.superviseWatch(ctx, time.Now())
	turnsQuiet(t, restarted, w.ID)
	for range 3 {
		restarted.sendDigest(ctx)
	}
	for _, m := range deliveredTo(hub, "s1") {
		if strings.Contains(m, w.ID) {
			t.Fatalf("the review the leader took on a retry was delivered again after a restart:\n%s", m)
		}
	}
}
