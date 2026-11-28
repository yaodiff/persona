package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A handover carries on in the to-do it hands on (#6427): a new agent after the first one stopped works on the same
// to-do, not a new one; a to-do another open to-do already carries on is not continued a second time.

// continuesArgs is a read-only plan (it starts at once) of one task that continues todo from.
func continuesArgs(from string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{"title": "接着发版", "prompt": "上一个 Agent 停了，接着把 1.3 版发出去",
		"machine": "box", "cwd": "/code/app", "readonly": true, "permission": "bypassPermissions", "why": "这个仓库在 box 上",
		"continues": from, "continues_why": "原 Agent 停了"}}})
	return raw
}

// stoppedAgent01 lists box's agent01 as stopped: its agent quit and its terminal stayed (tasks_close.go).
func (e *threadEnv) stoppedAgent01() {
	e.sessions("box", proto.Session{SessionID: "agent01", Title: "修发版", Status: proto.StatusOffline})
}

// newAgentOn waits for an agent other than agent01 to be linked to todo id and answers agent01's link.
func newAgentOn(t *testing.T, e *threadEnv, id string) TodoAgent {
	t.Helper()
	var old TodoAgent
	waitFor(t, "a new agent on "+id, func() bool {
		fresh := false
		for _, a := range todoNow(e.s, id).Agents {
			if a.SessionID == "agent01" {
				old = a
			} else if a.ReleasedAt == nil && a.DismissedAt == nil {
				fresh = true
			}
		}
		return fresh
	})
	return old
}

// The conversation supervising a to-do another conversation opened may run a new agent on it once its agent stopped:
// the stopped agent is let go (kept in the to-do's history, not dismissed) and no to-do is made.
func TestTaskPlanTodoIDSupervisorTakesOverAStoppedAgent(t *testing.T) {
	e := newThreadEnv(t, nil)
	e.stoppedAgent01()
	putTodo(e.s, Todo{ID: "todo_x", Title: "发版", Origin: "c1x1x1", Supervision: &Supervision{Supervisor: "m1"},
		Agents: []TodoAgent{{MachineID: "box", SessionID: "agent01"}}})
	if _, err := e.s.toolTaskPlan(context.Background(), "m1", reuseArgs(true, "todo_x")); err != nil {
		t.Fatal(err)
	}
	old := newAgentOn(t, e, "todo_x")
	if old.ReleasedAt == nil || old.DismissedAt != nil {
		t.Fatalf("the stopped agent: %+v, want let go and kept", old)
	}
	if todoCount(e.s) != 1 {
		t.Fatalf("%d todos, want only todo_x", todoCount(e.s))
	}
}

// Plain todo_id still refuses a live Agent; continues explicitly hands it over on the same to-do.
func TestTakeOverOfAWorkingAgent(t *testing.T) {
	e := newThreadEnv(t, nil) // agent01 is listed as running
	putTodo(e.s, Todo{ID: "todo_x", Title: "发版", Origin: "m1", Supervision: &Supervision{Supervisor: "m1"},
		Agents: []TodoAgent{{MachineID: "box", SessionID: "agent01"}}})
	if _, err := e.s.toolTaskPlan(context.Background(), "m1", reuseArgs(true, "todo_x")); err == nil || !strings.Contains(err.Error(), "already working on it") {
		t.Fatalf("todo_id: %v", err)
	}
	if _, err := e.s.toolTaskPlan(context.Background(), "m1", continuesArgs("todo_x")); err != nil {
		t.Fatal(err)
	}
	if old := newAgentOn(t, e, "todo_x"); old.ReleasedAt == nil {
		t.Fatalf("the previous Agent was not released: %+v", old)
	}
	if todoCount(e.s) != 1 || len(e.f.stopped) != 1 || e.f.stopped[0] != "agent01" {
		t.Fatalf("continuation split the task or did not stop the previous Agent: todos=%d, stopped=%v", todoCount(e.s), e.f.stopped)
	}
}

// continues of a to-do still open whose agent stopped runs on that to-do: no second to-do for the same work.
func TestTaskPlanContinuesAnOpenTodoRunsOnIt(t *testing.T) {
	e := newThreadEnv(t, nil)
	e.stoppedAgent01()
	putTodo(e.s, Todo{ID: "todo_x", Title: "发版", Origin: "m1", Supervision: &Supervision{Supervisor: "m1"},
		Agents: []TodoAgent{{MachineID: "box", SessionID: "agent01"}}})
	res, err := e.s.toolTaskPlan(context.Background(), "m1", continuesArgs("todo_x"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "todo todo_x on box") {
		t.Fatalf("answer %q", res)
	}
	newAgentOn(t, e, "todo_x")
	if todoCount(e.s) != 1 {
		t.Fatalf("%d todos, want the continued one only", todoCount(e.s))
	}
}

// A to-do another open to-do already carries on is not continued again: refused when planned, and a card planned
// before that started is refused on Start; nothing is made either way.
func TestContinuationOfAContinuedTodoIsRefused(t *testing.T) {
	e := newThreadEnv(t, nil)
	ctx := context.Background()
	done := time.Now()
	putTodo(e.s, Todo{ID: "todo_a", Title: "发版", Origin: "m1", DoneAt: &done})
	// A card that waits for Start, planned while todo_a had no successor yet.
	waiting := continuesArgs("todo_a")
	waiting = json.RawMessage(strings.Replace(string(waiting), `"readonly":true`, `"readonly":false,"done_when":["PR 合并"]`, 1))
	if res, err := e.s.toolTaskPlan(ctx, "m1", waiting); err != nil || !strings.Contains(res, "nothing has started") {
		t.Fatalf("%q %v", res, err)
	}
	card := planOf(t, e.s, "m1")
	// Meanwhile another conversation carried todo_a on in todo_b.
	brief, err := e.s.ensureTaskBrief(ctx, todoNow(e.s, "todo_a"))
	if err != nil {
		t.Fatal(err)
	}
	putTodo(e.s, Todo{ID: "todo_b", Title: "接着发版", Origin: "c1x1x1"})
	if err := e.s.linkTaskBrief(ctx, brief.ID, "todo_b", "todo_a", "原 Agent 停了"); err != nil {
		t.Fatal(err)
	}
	before := todoCount(e.s)
	if _, err := e.s.toolTaskPlan(ctx, "m1", continuesArgs("todo_a")); err == nil || !strings.Contains(err.Error(), "todo_id todo_b") {
		t.Fatalf("a second continuation: %v", err)
	}
	if _, err := e.s.startPlan(ctx, card.ID, nil, false); err == nil || !strings.Contains(err.Error(), "todo_b") {
		t.Fatalf("the stale card's Start: %v", err)
	}
	if todoCount(e.s) != before || len(e.f.starts) != 0 {
		t.Fatalf("%d todos (was %d), %d starts", todoCount(e.s), before, len(e.f.starts))
	}
}

// The duplicate hint looks at a continuation too: the to-do it continues is not its copy, any other like it is.
func TestPlanDuplicatesLooksAtContinuations(t *testing.T) {
	e := newThreadEnv(t, nil)
	putTodo(e.s, Todo{ID: "todo_a", Title: "合并发布 #7240 并补历史", Origin: "m1"})
	putTodo(e.s, Todo{ID: "todo_c", Title: "合并发布 #7240 补历史数据", Origin: "c1x1x1"})
	task := PlanTask{Title: "合并发布 #7240 并补历史", Prompt: "接着做", Machine: "box", Cwd: "/code/app", Continues: "todo_a"}
	got := e.s.planDuplicates(context.Background(), []PlanTask{task}, nil)
	if !strings.Contains(got, "todo_c") || strings.Contains(got, "todo_a") {
		t.Fatalf("hint %q: want todo_c, not the continued todo_a", got)
	}
}
