package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// keptRefusal is a node's refusal of a start whose new tab's shell stopped on oh-my-zsh's update question, the tab
// kept for 10 minutes (node/shellsettle.go).
func keptRefusal(code string) error {
	data, _ := json.Marshal(map[string]any{"line": "[oh-my-zsh] Would you like to update? [Y/n]",
		"screen": "Last login: Mon Nov  9 22:01:13 on ttys004\n[oh-my-zsh] Would you like to update? [Y/n]", "pane": "w1:p2",
		"kept_until": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339), "delivered": "none"})
	return &rpc.Error{Code: code, Message: "the new tab's shell is waiting for an answer", Data: data}
}

// remedyingTask is a task of the main conversation conv1, supervised, on box, whose start failed with failure.
func remedyingTask(t *testing.T, s *Service, f *fakeHub, failure error) (Todo, *TaskPlan) {
	t.Helper()
	return remedyingTaskAs(t, s, f, failure, "conv1", "plan_r")
}

func remedyingTaskAs(t *testing.T, s *Service, f *fakeHub, failure error, supervisor, planID string) (Todo, *TaskPlan) {
	t.Helper()
	return remedyingTaskFrom(t, s, f, failure, supervisor, supervisor, planID)
}

// remedyingTaskFrom starts a task from origin's plan under an explicitly assigned supervisor. Keeping those two facts
// separate models a main-created task later put under a manual leader without rewriting its persisted plan (#5732).
func remedyingTaskFrom(t *testing.T, s *Service, f *fakeHub, failure error, origin, supervisor, planID string) (Todo, *TaskPlan) {
	t.Helper()
	ctx := context.Background()
	f.mu.Lock()
	f.failStart = failure
	f.mu.Unlock()
	todo, err := s.createTodo(ctx, "修登录", "修登录", origin, planID)
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, supervisor, []string{"PR 合并"}, 0)
	task := PlanTask{Title: "修登录", Prompt: "修登录", Machine: "box", Cwd: "/code/app", Permission: "bypassPermissions", TodoID: todo.ID}
	p := &TaskPlan{ID: planID, Conversation: origin, State: planStarted, Tasks: []PlanTask{task}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.savePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	s.startPlanTask(p.ID, p.Conversation, 0, task, false)
	return todoNow(s, todo.ID), p
}

// remedyState is the plan task's start remedy as stored.
func remedyState(t *testing.T, s *Service, planID string) *StartRemedy {
	t.Helper()
	p, err := s.loadPlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	return p.Tasks[0].Start
}

// remedyHistory is conv1's feed items for todo, as kind or kind/action.
func remedyHistory(t *testing.T, s *Service, todo string) []string {
	t.Helper()
	items, err := s.feedItems(context.Background(), "conv1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range items {
		if it.Ref != todo && it.Kind != feedNotice {
			continue
		}
		k := it.Kind
		var b struct {
			Action, By string
		}
		_ = json.Unmarshal(it.Body, &b)
		if b.Action != "" {
			k += "/" + b.Action + "/" + b.By
		}
		out = append(out, k)
	}
	return out
}

// A supervised task whose new tab's shell stopped on a question, the tab kept (#2570), is not failed yet: the
// supervisor hears of it with the question, the tab's last lines, until when it is kept and the retries left.
func TestAStartWhoseTabIsKeptIsRemediedFirst(t *testing.T) {
	s, f := newQuietService(t)
	todo, p := remedyingTask(t, s, f, keptRefusal(proto.CodeShellWaiting))
	if todo.DoneAt != nil || todo.StartError != "" {
		t.Fatalf("failed at once: %+v", todo)
	}
	r := remedyState(t, s, p.ID)
	if r == nil || r.State != remedying || r.Pane != "w1:p2" || r.Machine != "box" || r.Code != proto.CodeShellWaiting || r.Attempt != 0 {
		t.Fatalf("start %+v", r)
	}
	waitFor(t, "the supervision message", func() bool { return len(f.notices()) == 1 })
	n := f.notices()[0]
	for _, want := range []string{"Agent 没开起来，它的启动标签在电脑上保留到", "停在一个提问上「[oh-my-zsh] Would you like to update? [Y/n]」",
		"| Last login:", "还能重试 3 次", "signal=failed"} {
		if !strings.Contains(n, want) {
			t.Errorf("message lacks %q:\n%s", want, n)
		}
	}
	if got := remedyHistory(t, s, todo.ID); slices.Contains(got, feedReceipt) {
		t.Errorf("a failed receipt while remedying: %v", got)
	}
}

// The main conversation reads the kept tab, answers its question and starts again in the same tab: the node gets
// the keys for that pane and the start runs in it; the task's history has each step, and the supervisor hears it
// started. Another conversation can do none of it.
func TestRemedyToolsAnswerAndRetryInTheSameTab(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo, p := remedyingTask(t, s, f, keptRefusal(proto.CodeShellWaiting))
	var typed []map[string]any
	f.mu.Lock()
	f.nodes = map[string]nodeAnswer{
		"session.screen": func(_ string, raw json.RawMessage) (any, error) {
			return map[string]any{"text": "[oh-my-zsh] Would you like to update? [Y/n]\n", "status": proto.StatusIdle}, nil
		},
		"start.keys": func(machine string, raw json.RawMessage) (any, error) {
			var a map[string]any
			_ = json.Unmarshal(raw, &a)
			a["machine"] = machine
			typed = append(typed, a)
			return map[string]any{}, nil
		},
	}
	f.mu.Unlock()
	args := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	if _, err := s.runTool(ctx, "server", "conv2", "task_start_keys", args(map[string]any{"todo_id": todo.ID, "keys": []string{"n"}, "reason": "x"})); err == nil {
		t.Fatal("another conversation typed into the tab")
	}
	screen, err := s.runTool(ctx, "server", "conv1", "task_start_screen", args(map[string]any{"todo_id": todo.ID}))
	if err != nil || !strings.Contains(screen, "Would you like to update?") {
		t.Fatalf("screen %q, %v", screen, err)
	}
	if _, err := s.runTool(ctx, "server", "conv1", "task_start_keys", args(map[string]any{"todo_id": todo.ID, "keys": []string{"n"}})); err == nil {
		t.Fatal("keys without a reason")
	}
	if _, err := s.runTool(ctx, "server", "conv1", "task_start_keys", args(map[string]any{"todo_id": todo.ID, "keys": []string{"n"},
		"reason": "不更新 oh-my-zsh，先让任务跑起来"})); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(typed) != 1 || typed[0]["machine"] != "box" || typed[0]["pane"] != "w1:p2" || len(typed[0]["keys"].([]any)) != 1 {
		t.Errorf("typed %v", typed)
	}
	f.failStart = nil
	f.mu.Unlock()
	res, err := s.runTool(ctx, "server", "conv1", "task_retry", args(map[string]any{"todo_id": todo.ID, "reason": "提问已经跳过"}))
	if err != nil || !strings.Contains(res, "the same tab") {
		t.Fatalf("retry %q, %v", res, err)
	}
	waitFor(t, "the start in the kept tab", func() bool { return len(todoNow(s, todo.ID).Agents) == 1 })
	f.mu.Lock()
	last := f.starts[len(f.starts)-1]
	f.mu.Unlock()
	if last["in_pane"] != "w1:p2" {
		t.Errorf("started with %v", last)
	}
	if r := remedyState(t, s, p.ID); r == nil || r.State != remedyStarted || r.Attempt != 1 || r.By != "assistant" {
		t.Errorf("start %+v", r)
	}
	want := []string{feedStartRemedy + "/screen/assistant", feedStartRemedy + "/keys/assistant", feedStartRemedy + "/retry/assistant",
		feedStartRemedy + "/started/assistant"}
	if got := remedyHistory(t, s, todo.ID); !slices.Equal(got, want) {
		t.Errorf("history %v", got)
	}
	waitFor(t, "the retry started message", func() bool {
		return slices.ContainsFunc(f.notices(), func(n string) bool { return strings.Contains(n, "重试后 Agent 开起来了") })
	})
}

// The supervision turn that heard of the failure ends with no retry going: the start fails for good, with its
// receipt. A turn that never told the user gets the hub's notice in the conversation; one that did, none. Neither is
// pushed to the owner: the supervisor gave up on its own task, and only its needs_user brings that to the owner.
func TestRemedyEndsWithTheTurnThatSawIt(t *testing.T) {
	for _, told := range []bool{false, true} {
		s, f := newQuietService(t)
		ctx := context.Background()
		todo, p := remedyingTask(t, s, f, keptRefusal(proto.CodeShellWaiting))
		waitFor(t, "the supervision message", func() bool { return len(f.notices()) == 1 })
		if !s.supervising("conv1") {
			t.Fatal("the supervision turn is not held")
		}
		if told {
			s.promoteSupervision("conv1") // notify_user
		}
		pushes := len(f.pushed())
		s.remedyTurnEnded(ctx, "conv1")
		got := todoNow(s, todo.ID)
		if got.DoneAt == nil || got.StartError == "" {
			t.Fatalf("told=%v: not failed: %+v", told, got)
		}
		if r := remedyState(t, s, p.ID); r == nil || r.State != remedyFailed || r.By != "assistant" {
			t.Errorf("told=%v: start %+v", told, r)
		}
		kinds := remedyHistory(t, s, todo.ID)
		if !slices.Contains(kinds, feedReceipt) || !slices.Contains(kinds, feedStartRemedy+"/gave_up/assistant") {
			t.Errorf("told=%v: history %v", told, kinds)
		}
		if notice := slices.Contains(kinds, feedNotice); notice == told {
			t.Errorf("told=%v: the hub's notice %v", told, kinds)
		}
		if len(f.pushed()) > pushes {
			t.Errorf("told=%v: pushed %v", told, f.pushed())
		}
	}
}

// A retry the assistant started is still running when the turn ends: the start is not given up under it.
func TestRemedyTurnEndLeavesARetryRunning(t *testing.T) {
	s, f := newQuietService(t)
	todo, _ := remedyingTask(t, s, f, keptRefusal(proto.CodeShellWaiting))
	waitFor(t, "the supervision message", func() bool { return len(f.notices()) == 1 })
	s.claimTaskStart(todo.ID)
	s.remedyTurnEnded(context.Background(), "conv1")
	if got := todoNow(s, todo.ID); got.DoneAt != nil {
		t.Fatalf("given up under a retry: %+v", got)
	}
}

// A retry parked in the admission queue is owned by the queue loop, not given up by turn-end or tab expiry.
func TestRemedyLeavesAdmissionQueuedRetryAlive(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo, p := remedyingTask(t, s, f, keptRefusal(proto.CodeShellWaiting))
	waitFor(t, "the supervision message", func() bool { return len(f.notices()) == 1 })
	if err := s.patchPlanTask(ctx, p.ID, 0, func(pt *PlanTask) {
		pt.Start.Until = time.Now().Add(-time.Second)
		pt.Queue = &TaskQueue{
			Reason:   "high load",
			Key:      "admission_load",
			Attempt:  1,
			QueuedAt: time.Now().UTC(),
			NextAt:   time.Now().Add(time.Minute).UTC(),
			Retry:    true,
		}
	}); err != nil {
		t.Fatal(err)
	}
	s.remedyTurnEnded(ctx, "conv1")
	s.remedyWatch(ctx, todoNow(s, todo.ID), time.Now(), s.planHistoryOnce())
	if got := todoNow(s, todo.ID); got.DoneAt != nil {
		t.Fatalf("queued retry was given up: %+v", got)
	}
	if r := remedyState(t, s, p.ID); r == nil || r.State != remedying {
		t.Fatalf("start state changed while queued: %+v", r)
	}
}

// The kept tab expires with nobody remedying it (no supervision turn came, or the hub restarted): the supervisor's
// tick fails the start for good and tells the user.
func TestRemedyExpiresWithItsTab(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo, p := remedyingTask(t, s, f, keptRefusal(proto.CodeAgentStartStalled))
	if err := s.patchPlanTask(ctx, p.ID, 0, func(pt *PlanTask) { pt.Start.Until = time.Now().Add(-time.Second) }); err != nil {
		t.Fatal(err)
	}
	s.remedyWatch(ctx, todoNow(s, todo.ID), time.Now(), s.planHistoryOnce())
	if got := todoNow(s, todo.ID); got.DoneAt == nil {
		t.Fatalf("not failed: %+v", got)
	}
	if kinds := remedyHistory(t, s, todo.ID); !slices.Contains(kinds, feedStartRemedy+"/gave_up/hub") || !slices.Contains(kinds, feedNotice) {
		t.Errorf("history %v", kinds)
	}
}

// Three retries at most: a fourth is refused, and a failure after the third is not remedied again.
func TestRemedyRetriesAreCapped(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo, p := remedyingTask(t, s, f, keptRefusal(proto.CodeShellWaiting))
	if err := s.patchPlanTask(ctx, p.ID, 0, func(pt *PlanTask) { pt.Start.Attempt = startRemedyRetries }); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "reason": "再试一次"})
	if _, err := s.runTool(ctx, "server", "conv1", "task_retry", raw); err == nil || !strings.Contains(err.Error(), "3 times") {
		t.Fatalf("a fourth retry: %v", err)
	}
	loaded, _ := s.loadPlan(ctx, p.ID)
	if s.beginRemedy(ctx, p.ID, 0, loaded.Tasks[0], keptRefusal(proto.CodeShellWaiting), proto.CodeShellWaiting,
		map[string]string{"pane": "w1:p2", "kept_until": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}, "x") {
		t.Fatal("remedied after the last retry")
	}
}

// An assistant's retry that fails so no tab is kept (the shell refused the line twice) fails the start for good at
// once, and the supervisor hears of it as of a first failure.
func TestARetryThatFailsWithoutATabFailsForGood(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo, p := remedyingTask(t, s, f, keptRefusal(proto.CodeShellWaiting))
	waitFor(t, "the supervision message", func() bool { return len(f.notices()) == 1 })
	f.mu.Lock()
	f.failStart = &rpc.Error{Code: proto.CodeStartLineRejected, Message: "the new tab's shell did not take the start line, twice"}
	f.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "reason": "再试一次"})
	if _, err := s.runTool(ctx, "server", "conv1", "task_retry", raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failure", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
	if r := remedyState(t, s, p.ID); r == nil || r.State != remedyFailed || r.Attempt != 1 {
		t.Errorf("start %+v", r)
	}
	waitFor(t, "the history", func() bool {
		kinds := remedyHistory(t, s, todo.ID)
		return slices.Contains(kinds, feedStartRemedy+"/gave_up/hub") && slices.Contains(kinds, feedReceipt)
	})
	waitFor(t, "the supervisor told", func() bool { return len(f.notices()) == 2 })
}

// A retry that ends the way the first start did is still news: task_retry promised the supervisor how it ends (#6027).
// In the kept tab it stops on the same question again; in a new tab, after a start that failed for good, it fails
// with the same error.
func TestARetryThatFailsTheSameWayIsTold(t *testing.T) {
	for name, c := range map[string]struct {
		failure error
		state   string
	}{
		"the same question in the kept tab": {keptRefusal(proto.CodeShellWaiting), remedying},
		"the same error in a new tab":       {&rpc.Error{Code: proto.CodeStartLineRejected, Message: "the new tab's shell did not take the start line, twice"}, remedyFailed},
	} {
		t.Run(name, func(t *testing.T) {
			s, f := newQuietService(t)
			todo, p := remedyingTask(t, s, f, c.failure)
			waitFor(t, "the first failure told", func() bool { return len(f.notices()) == 1 })
			raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "reason": "再试一次"})
			if _, err := s.runTool(context.Background(), "server", "conv1", "task_retry", raw); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the retry to end", func() bool {
				busy := s.taskStartOnWay(todo.ID)
				r := remedyState(t, s, p.ID)
				return !busy && len(f.started()) >= 2 && r != nil && r.State == c.state && r.Attempt == 1
			})
			waitFor(t, "the retry's failure told", func() bool { return len(f.notices()) == 2 })
		})
	}
}

// Another held conversation's turn ends (a leader's, or the background supervisor's, which is held on every
// supervision message, #4927): it never saw the failed start, so the start the main conversation is still remedying
// is not given up under it. The main conversation's own turn end then ends it as ever.
func TestRemedyIsNotEndedByAnotherConversationsTurn(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo, p := remedyingTask(t, s, f, keptRefusal(proto.CodeShellWaiting))
	waitFor(t, "the supervision message", func() bool { return len(f.notices()) == 1 })
	s.holdPushes(ctx, "s900") // a supervision message went into another conversation after the failure was told
	s.remedyTurnEnded(ctx, "s900")
	if got := todoNow(s, todo.ID); got.DoneAt != nil {
		t.Fatalf("given up by a turn that never saw the failure: %+v", got)
	}
	if r := remedyState(t, s, p.ID); r == nil || r.State != remedying {
		t.Fatalf("start %+v", r)
	}
	s.remedyTurnEnded(ctx, "conv1")
	if got := todoNow(s, todo.ID); got.DoneAt == nil || got.StartError == "" {
		t.Fatalf("not ended by the turn that saw it: %+v", got)
	}
}

// A supervised task whose agent stopped on a dialog the computer did not know, and whose start cannot be remedied (no
// tab kept), fails for good: the supervisor's message still shows the agent's screen as its rows, every choice of the
// dialog on its own line, not only the one clipped line of the reason; with no retries offered for a tab that is gone.
func TestAStalledStartThatFailsForGoodShowsItsScreen(t *testing.T) {
	s, f := newQuietService(t)
	shown := " Make auto mode your default permission mode?\n ❯ 1. Yes, make auto mode my default\n   2. Something new\n Enter to confirm · Esc to cancel"
	data, _ := json.Marshal(map[string]any{"screen": shown})
	failure := &rpc.Error{Code: proto.CodeAgentStartStalled, Data: data,
		Message: "an unrecognized dialog blocks the start: \"Make auto mode your default permission mode?\"\n" + shown}
	todo, _ := remedyingTask(t, s, f, failure)
	waitFor(t, "the failure", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
	waitFor(t, "the supervision message", func() bool { return len(f.notices()) == 1 })
	n := f.notices()[0]
	for _, want := range []string{"启动标签最后几行", "   |  ❯ 1. Yes, make auto mode my default\n   |    2. Something new\n", "signal=failed"} {
		if !strings.Contains(n, want) {
			t.Errorf("message lacks %q:\n%s", want, n)
		}
	}
	if strings.Contains(n, "还能重试") {
		t.Errorf("retries offered for a start that failed for good:\n%s", n)
	}
}

func sideRemedyService(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	store := restartHistoryStore{&profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f := newQuietServiceOn(t, store)
	f.mu.Lock()
	f.known["side1"], f.known["side2"] = true, true
	f.mu.Unlock()
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "side1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "side2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	return s, f
}

// startSideTask dispatches through the same tool a side conversation uses.
func startSideTask(t *testing.T, s *Service) Todo {
	t.Helper()
	ctx := t.Context()
	result := s.Tool(ctx, ToolCall{MachineID: "server", SessionID: "side1", Name: "task_plan", Arguments: planArgs(true)})
	if result.IsError {
		t.Fatalf("side conversation could not dispatch: %s", toolText(result))
	}
	waitFor(t, "the side task and its supervisor", func() bool {
		return onlyTodo(s).Supervision != nil
	})
	task := onlyTodo(s)
	if task.Supervision.Supervisor != "side1" || task.Supervision.SupervisorSource != "plan" ||
		s.isMain(ctx, "side1") || s.isLeader(ctx, "side1") {
		t.Fatalf("expected a task owned by an ordinary conversation, got %+v", task.Supervision)
	}
	return task
}

func sideRemedyTool(t *testing.T, s *Service, self, name, id string) proto.ToolResult {
	t.Helper()
	args, err := json.Marshal(map[string]any{"todo_id": id, "keys": []string{"n"}, "reason": "Continue startup"})
	if err != nil {
		t.Fatal(err)
	}
	return s.Tool(t.Context(), ToolCall{MachineID: "server", SessionID: self, Name: name, Arguments: args})
}

func checkRemedyRefused(t *testing.T, s *Service, self, name, id, want string) {
	t.Helper()
	result := sideRemedyTool(t, s, self, name, id)
	if !result.IsError || !strings.Contains(toolText(result), want) {
		t.Fatalf("%s calling %s = %q, want refusal directing it to %s", self, name, toolText(result), want)
	}
}

func checkTaskRetried(t *testing.T, s *Service, self, id, tab string) {
	t.Helper()
	result := sideRemedyTool(t, s, self, "task_retry", id)
	if result.IsError || !strings.Contains(toolText(result), tab) {
		t.Fatalf("%s retry = %q, want a retry in %s", self, toolText(result), tab)
	}
	waitFor(t, "the retried Agent on the same task", func() bool {
		cur := todoNow(s, id)
		return cur.DoneAt == nil && cur.StartError == "" && len(cur.Agents) == 1
	})
}

func TestSideSupervisorRemediesItsOwnStart(t *testing.T) {
	s, f := sideRemedyService(t)
	f.mu.Lock()
	f.failStart = keptRefusal(proto.CodeShellWaiting)
	typed := 0
	f.nodes = map[string]nodeAnswer{
		"session.screen": func(_ string, _ json.RawMessage) (any, error) {
			return map[string]any{"text": "Would you like to update? [Y/n]\n", "status": proto.StatusIdle}, nil
		},
		"start.keys": func(_ string, _ json.RawMessage) (any, error) {
			typed++
			return map[string]any{}, nil
		},
	}
	f.mu.Unlock()

	task := startSideTask(t, s)
	waitFor(t, "the failed start told to its supervising conversation", func() bool {
		return len(supervisionTo(f, "side1", sigFailed)) == 1
	})
	for _, name := range []string{"task_start_screen", "task_start_keys", "task_retry"} {
		checkRemedyRefused(t, s, "side2", name, task.ID, "task's current supervising conversation")
	}
	if result := sideRemedyTool(t, s, "side1", "task_start_screen", task.ID); result.IsError || !strings.Contains(toolText(result), "Would you like to update?") {
		t.Fatalf("supervisor's screen = %q", toolText(result))
	}
	if result := sideRemedyTool(t, s, "side1", "task_start_keys", task.ID); result.IsError || typed != 1 {
		t.Fatalf("supervisor's keys = %q, sent %d times", toolText(result), typed)
	}
	f.mu.Lock()
	f.failStart = nil
	f.mu.Unlock()
	checkTaskRetried(t, s, "side1", task.ID, "the same tab")
}

func TestSideSupervisorRetriesItsFailedStart(t *testing.T) {
	s, f, d := supervisedQueueHub(t)
	f.mu.Lock()
	f.inbox = newFakeInbox()
	f.known["side1"], f.known["side2"] = true, true
	f.mu.Unlock()
	conversations := []proto.Session{
		{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		{SessionID: "side1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		{SessionID: "side2", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
	}
	f.setSessions("server", conversations...)
	d.setSessions("server", conversations)
	busyOverloaded(d)
	plan, task := queuedStartFor(t, s, "busy", "side1", "plan")
	if plan.Tasks[0].Queue == nil || supervisorOf(task) != "side1" {
		t.Fatalf("expected admission to queue a task supervised by side1")
	}
	timeOutQueue(t, s, plan.ID)
	waitFor(t, "the timed-out start told to its supervising conversation", func() bool {
		return len(supervisionTo(f, "side1", sigFailed)) == 1
	})
	if cur := todoNow(s, task.ID); !cur.failedStart() || len(cur.Agents) != 0 {
		t.Fatalf("expected a failed start with no Agent, got %+v", cur)
	}

	checkRemedyRefused(t, s, "side2", "task_retry", task.ID, "task's current supervising conversation")
	busyHealthy(d)
	checkTaskRetried(t, s, "side1", task.ID, "a new tab")
}

func TestMainRetriesAnotherConversationsTask(t *testing.T) {
	for _, archive := range []bool{false, true} {
		name := "override"
		if archive {
			name = "after handover"
		}
		t.Run(name, func(t *testing.T) {
			s, f := sideRemedyService(t)
			f.mu.Lock()
			f.failStart = errors.New("the Agent could not start")
			f.mu.Unlock()
			task := startSideTask(t, s)
			waitFor(t, "a failed start", func() bool { return todoNow(s, task.ID).failedStart() })
			if archive {
				if _, err := s.setArchived(t.Context(), "server", "side1", true, titleByUser); err != nil {
					t.Fatal(err)
				}
				if got := supervisorOf(todoNow(s, task.ID)); got != "conv1" {
					t.Fatalf("failed start handed to %q, want conv1", got)
				}
				checkRemedyRefused(t, s, "side1", "task_retry", task.ID, "Ask the main conversation")
			}
			f.mu.Lock()
			f.failStart = nil
			f.mu.Unlock()
			checkTaskRetried(t, s, "conv1", task.ID, "a new tab")
		})
	}
}

func TestFormerSideSupervisorCannotRemedy(t *testing.T) {
	s, f := sideRemedyService(t)
	f.mu.Lock()
	f.failStart = keptRefusal(proto.CodeShellWaiting)
	f.mu.Unlock()
	task := startSideTask(t, s)
	waitFor(t, "the kept start told to its supervising conversation", func() bool {
		return len(supervisionTo(f, "side1", sigFailed)) == 1
	})
	if _, err := s.setArchived(t.Context(), "server", "side1", true, titleByUser); err != nil {
		t.Fatal(err)
	}
	if got := supervisorOf(todoNow(s, task.ID)); got != "conv1" {
		t.Fatalf("kept start handed to %q, want conv1", got)
	}
	for _, name := range []string{"task_start_screen", "task_start_keys", "task_retry"} {
		checkRemedyRefused(t, s, "side1", name, task.ID, "Ask the main conversation")
	}
}
