package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// task_plan's todo_id (#5279): a task runs on a todo the conversation opened (a Lark topic's) instead of making one.

// reuseArgs is a plan of tasks, each on the todo named (empty: a todo of its own). readonly tasks start at once; the
// rest wait for the user's "Start".
func reuseArgs(readonly bool, todoIDs ...string) json.RawMessage {
	var tasks []map[string]any
	for _, id := range todoIDs {
		task := map[string]any{"title": "发版", "prompt": "把 1.3 版发出去", "machine": "box", "cwd": "/code/app",
			"readonly": readonly, "permission": "bypassPermissions", "why": "这个仓库在 box 上", "done_when": []string{"PR 合并到 master"}}
		if id != "" {
			task["todo_id"] = id
		}
		tasks = append(tasks, task)
	}
	raw, _ := json.Marshal(map[string]any{"tasks": tasks})
	return raw
}

// openedTodo is the todo a topic's first message opens (the main conversation m1 its origin).
func (e *threadEnv) openedTodo() Todo {
	e.t.Helper()
	e.deliver(rootEvent("om_open", "发天气 App 1.3 版"))
	todo := onlyTodo(e.s)
	if todo.ID == "" {
		e.t.Fatalf("%d todos, want the one a topic opened", todoCount(e.s))
	}
	return todo
}

// planOf is the one plan in conversation's feed.
func planOf(t *testing.T, s *Service, conversation string) *TaskPlan {
	t.Helper()
	_, plans, err := s.feed(context.Background(), conversation, time.Time{})
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans %v %v", plans, err)
	}
	for _, p := range plans {
		return p
	}
	return nil
}

// A task with todo_id runs on that todo: no todo is made, the agent is linked to it, the task card is the one the topic
// already put in the feed (no second), the plan's task names the todo, and the task's criteria go to the supervision.
func TestTaskPlanTodoIDRunsOnTheTopicsTodo(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newThreadEnv(t, nil)
	todo := e.openedTodo()
	res, err := e.s.toolTaskPlan(context.Background(), "m1", reuseArgs(true, todo.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "todo "+todo.ID+" on box") {
		t.Fatalf("answer %q", res)
	}
	waitFor(t, "the agent to link to the todo", func() bool { return len(todoNow(e.s, todo.ID).Agents) == 1 })
	if todoCount(e.s) != 1 {
		t.Fatalf("%d todos, want only the topic's", todoCount(e.s))
	}
	got := todoNow(e.s, todo.ID)
	if got.Title != todo.Title || got.Notes != todo.Notes || got.Thread == nil || got.PlanID != "" {
		t.Fatalf("the todo changed: %+v", got)
	}
	if got.Supervision == nil || got.Supervision.Supervisor != "m1" || len(got.Supervision.DoneWhen) != 1 || got.Supervision.DoneWhen[0] != "PR 合并到 master" {
		t.Fatalf("supervision %+v", got.Supervision)
	}
	if kinds := feedKinds(e.s, "m1"); strings.Count(kinds, "task") != 1 || !strings.Contains(kinds, "plan") {
		t.Fatalf("feed %q: one task card (the topic's) and the plan", kinds)
	}
	if p := planOf(t, e.s, "m1"); p.Tasks[0].TodoID != todo.ID || p.Tasks[0].Error != "" {
		t.Fatalf("plan task %+v", p.Tasks[0])
	}
	if len(e.f.starts) != 1 {
		t.Fatalf("%d agents started", len(e.f.starts))
	}
	// Once the agent is linked the todo is no longer held for the plan (the agent on it is what keeps another plan off).
	waitFor(t, "the todo to be let go", func() bool {
		e.s.tasks.mu.Lock()
		defer e.s.tasks.mu.Unlock()
		return len(e.s.tasks.reusing) == 0
	})
}

// A todo_id the conversation may not take is refused with why, and nothing is made: the todo is not there, was opened
// from another conversation, is done, has an agent working on it, or a plan names it twice.
func TestTaskPlanTodoIDRefusals(t *testing.T) {
	released := time.Now()
	for _, c := range []struct {
		name string
		todo Todo // put in memory, when it has an id
		ids  []string
		want string
	}{
		{"no such todo", Todo{}, []string{"todo_nope"}, "there is no such todo"},
		{"another conversation's", Todo{ID: "todo_x", Origin: "c1x1x1"}, []string{"todo_x"}, "not opened from this conversation"},
		{"opened by nobody", Todo{ID: "todo_x"}, []string{"todo_x"}, "not opened from this conversation"},
		{"done", Todo{ID: "todo_x", Origin: "m1", DoneAt: &released}, []string{"todo_x"}, "already done"},
		// A failed start is taken up again (TestTaskPlanTodoIDTakesAFailedStart); a cancelled one is closed for good.
		{"a cancelled failed start", Todo{ID: "todo_x", Origin: "m1", DoneAt: &released, StartError: "没开起来", CancelledAt: &released},
			[]string{"todo_x"}, "already done"},
		{"an agent working on it", Todo{ID: "todo_x", Origin: "m1", Agents: []TodoAgent{{MachineID: "box", SessionID: "agent01"}}}, []string{"todo_x"}, "an agent is already working on it"},
		{"twice in one plan", Todo{ID: "todo_x", Origin: "m1"}, []string{"todo_x", "todo_x"}, "already the todo of another task"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newThreadEnv(t, nil)
			if c.todo.ID != "" {
				c.todo.Title = "发版"
				putTodo(e.s, c.todo)
			}
			before := todoCount(e.s)
			_, err := e.s.toolTaskPlan(context.Background(), "m1", reuseArgs(true, c.ids...))
			if err == nil || !strings.Contains(err.Error(), "todo_id") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v, want one about todo_id and %q", err, c.want)
			}
			if todoCount(e.s) != before || len(e.f.starts) != 0 || feedKinds(e.s, "m1") != "" {
				t.Fatalf("something was made: %d todos (was %d), %d starts, feed %q", todoCount(e.s), before, len(e.f.starts), feedKinds(e.s, "m1"))
			}
		})
	}
}

// An agent that was let go (released) or dismissed is not working on the todo any more: it can take a new task.
func TestTaskPlanTodoIDAllowsATodoWhoseAgentWasLetGo(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name  string
		agent TodoAgent
	}{
		{"released", TodoAgent{MachineID: "box", SessionID: "agent01", ReleasedAt: &now}},
		{"dismissed", TodoAgent{MachineID: "box", SessionID: "agent01", DismissedAt: &now}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newThreadEnv(t, nil)
			putTodo(e.s, Todo{ID: "todo_x", Title: "发版", Origin: "m1", Agents: []TodoAgent{c.agent}})
			if _, err := e.s.toolTaskPlan(context.Background(), "m1", reuseArgs(true, "todo_x")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the new agent to link", func() bool {
				for _, a := range todoNow(e.s, "todo_x").Agents {
					if a.ReleasedAt == nil && a.DismissedAt == nil && a.SessionID != "agent01" {
						return true
					}
				}
				return false
			})
			if todoCount(e.s) != 1 {
				t.Fatalf("%d todos", todoCount(e.s))
			}
		})
	}
}

// A plan that waits for the user's "Start" keeps the todo it names (the apps' card edits cannot change it) and runs on it
// when started.
func TestTaskPlanTodoIDWaitsForStart(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newThreadEnv(t, nil)
	ctx := context.Background()
	todo := e.openedTodo()
	res, err := e.s.toolTaskPlan(ctx, "m1", reuseArgs(false, todo.ID))
	if err != nil || !strings.Contains(res, "nothing has started") {
		t.Fatalf("%q %v", res, err)
	}
	p := planOf(t, e.s, "m1")
	if p.Tasks[0].ReuseTodo != todo.ID || p.Tasks[0].TodoID != "" {
		t.Fatalf("plan task %+v", p.Tasks[0])
	}
	if got := todoNow(e.s, todo.ID); len(got.Agents) != 0 || todoCount(e.s) != 1 {
		t.Fatalf("started early: %+v, %d todos", got, todoCount(e.s))
	}
	// An edit from the card names no todo: the plan's is kept, and a todo_id in an edit is not taken.
	if _, err := e.s.startPlan(ctx, p.ID, []PlanTask{{Title: "发安卓版", ReuseTodo: "todo_other", TodoID: "todo_other"}}, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the agent to link to the todo", func() bool { return len(todoNow(e.s, todo.ID).Agents) == 1 })
	if todoCount(e.s) != 1 {
		t.Fatalf("%d todos, want only the topic's", todoCount(e.s))
	}
	if got := planOf(t, e.s, "m1"); got.Tasks[0].TodoID != todo.ID || got.Tasks[0].Error != "" {
		t.Fatalf("plan task %+v", got.Tasks[0])
	}
	if strings.Count(feedKinds(e.s, "m1"), "task") != 1 {
		t.Fatalf("feed %q: a second task card", feedKinds(e.s, "m1"))
	}
}

// The todo is looked at again when the plan starts: closed, or given an agent, since the plan was made, the task fails
// with why and nothing starts (no todo of its own is made in its place).
func TestTaskPlanTodoIDIsCheckedAgainOnStart(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	for _, c := range []struct {
		name string
		do   func(e *threadEnv, todo Todo)
		want string
	}{
		{"closed meanwhile", func(e *threadEnv, todo Todo) { e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`) }, "already done"},
		{"given an agent meanwhile", func(e *threadEnv, todo Todo) { e.must("PUT", "/api/v1/todos/"+todo.ID+"/agents/box/agent01", "") }, "an agent is already working on it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newThreadEnv(t, nil)
			ctx := context.Background()
			todo := e.openedTodo()
			if _, err := e.s.toolTaskPlan(ctx, "m1", reuseArgs(false, todo.ID)); err != nil {
				t.Fatal(err)
			}
			c.do(e, todo)
			p, err := e.s.startPlan(ctx, planOf(t, e.s, "m1").ID, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(p.Tasks[0].Error, c.want) || p.Tasks[0].TodoID != "" {
				t.Fatalf("plan task %+v", p.Tasks[0])
			}
			time.Sleep(100 * time.Millisecond) // a start that went on would have asked a machine by now
			if todoCount(e.s) != 1 || len(e.f.starts) != 0 {
				t.Fatalf("%d todos, %d starts", todoCount(e.s), len(e.f.starts))
			}
		})
	}
}

// Two plans cannot take one todo in the gap before the first one's agent is linked: the second is refused until the
// first has let go (its agent linked or its start failed).
func TestTodoTakenByAStartedPlanIsNotTakenTwice(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newThreadEnv(t, nil)
	todo := e.openedTodo()
	if _, err := e.s.takeTodoForTask(context.Background(), "new-plan", "m1", PlanTask{ReuseTodo: todo.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.takeTodoForTask(context.Background(), "new-plan", "m1", PlanTask{ReuseTodo: todo.ID}); err == nil || !strings.Contains(err.Error(), "another plan is starting an agent on it") {
		t.Fatalf("a second take: %v", err)
	}
	if _, err := e.s.toolTaskPlan(context.Background(), "m1", reuseArgs(true, todo.ID)); err == nil || !strings.Contains(err.Error(), "another plan is starting") {
		t.Fatalf("a plan made in the gap: %v", err)
	}
	e.s.releaseTakenTodo(todo.ID)
	if _, err := e.s.takeTodoForTask(context.Background(), "new-plan", "m1", PlanTask{ReuseTodo: todo.ID}); err != nil {
		t.Fatalf("after it was let go: %v", err)
	}
}

// The todo a task runs on is that task, not a copy of it: the duplicate hint (two names of one issue) does not list it
// against the task, and still lists a different todo that names the same issue.
func TestTaskPlanTodoIDIsNotItsOwnDuplicate(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newThreadEnv(t, nil)
	e.deliver(rootEvent("om_dup", "修 #1234 登录崩溃"))
	todo := onlyTodo(e.s)
	putTodo(e.s, Todo{ID: "todo_else", Title: "也是 #1234", Origin: "c1x1x1"})
	task := PlanTask{Title: "修 #1234", Prompt: "修好登录崩溃", Machine: "box", Cwd: "/code/app"}
	got := e.s.planDuplicates(context.Background(), []PlanTask{task}, nil)
	if !strings.Contains(got, todo.ID) || !strings.Contains(got, "todo_else") {
		t.Fatalf("without todo_id both todos are hinted: %q", got)
	}
	task.ReuseTodo = todo.ID
	got = e.s.planDuplicates(context.Background(), []PlanTask{task}, nil)
	if strings.Contains(got, todo.ID) || !strings.Contains(got, "todo_else") {
		t.Fatalf("with todo_id only the other todo is hinted: %q", got)
	}
}

// A to-do whose agent did not start is stored done with its start error, but nothing was done and it still waits on the
// owner (#10365): task_plan's todo_id takes it up again, reopening it (as a Retry does), with no second to-do or task
// card. Before, it was refused as "already done", and the assistant fell back on continues, which made a new plan.
func TestTaskPlanTodoIDTakesAFailedStart(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newThreadEnv(t, nil)
	ctx := context.Background()
	todo := e.openedTodo()
	_, open := e.s.snapshotOpenTodos(ctx, "") // the snapshot's open count does not flip with the start (#10365)
	e.f.mu.Lock()
	e.f.failStart = &rpc.Error{Code: proto.CodeStartLineRejected, Message: "the tab did not take the start line",
		Data: json.RawMessage(`{"delivered":"none","line":"zsh: parse error"}`)}
	e.f.mu.Unlock()
	if _, err := e.s.toolTaskPlan(ctx, "m1", reuseArgs(true, todo.ID)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start to fail", func() bool { return todoNow(e.s, todo.ID).StartError != "" && e.s.taskStarting.Load() == 0 })
	if failed := todoNow(e.s, todo.ID); !failed.failedStart() || failed.closedForGood() {
		t.Fatalf("not a failed start: %+v", failed)
	}
	if _, n := e.s.snapshotOpenTodos(ctx, ""); n != open {
		t.Fatalf("open count %d after the failed start, was %d", n, open)
	}
	e.f.mu.Lock()
	e.f.failStart = nil
	e.f.mu.Unlock()
	if _, err := e.s.toolTaskPlan(ctx, "m1", reuseArgs(true, todo.ID)); err != nil {
		t.Fatalf("a failed start refused: %v", err)
	}
	waitFor(t, "the agent to link to the to-do", func() bool { return len(todoNow(e.s, todo.ID).Agents) == 1 })
	got := todoNow(e.s, todo.ID)
	if got.DoneAt != nil || got.StartError != "" || got.ReopenedAt == nil || todoCount(e.s) != 1 {
		t.Fatalf("the to-do after its start was taken up: %+v; %d to-dos", got, todoCount(e.s))
	}
	if strings.Count(feedKinds(e.s, "m1"), "task") != 1 {
		t.Fatalf("feed %q: a second task card", feedKinds(e.s, "m1"))
	}
	if _, n := e.s.snapshotOpenTodos(ctx, ""); n != open {
		t.Fatalf("open count %d once taken up, was %d", n, open)
	}
}

// A start being remedied in the tab its computer keeps is task_retry's: a plan taking the to-do through todo_id would
// open another tab beside the one waiting on its question.
func TestTaskPlanTodoIDFailedStartWhileRemedying(t *testing.T) {
	e := newThreadEnv(t, nil)
	piece, _ := failedDirectionTodoStart(t, e, keptRefusal(proto.CodeShellWaiting))
	if piece.DoneAt != nil {
		t.Fatalf("failed at once, not remedied: %+v", piece)
	}
	_, err := e.s.toolTaskPlan(context.Background(), "c1x1x1", reuseArgs(true, piece.ID))
	if err == nil || !strings.Contains(err.Error(), "being remedied") || !strings.Contains(err.Error(), "task_retry") {
		t.Fatalf("error %v, want the remedy's", err)
	}
	if n := len(e.f.started()); n != 1 {
		t.Fatalf("%d starts", n)
	}
}
