package assistant

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// assistantHome is where the node's sessions are the assistant's conversations.
const assistantHome = "/home/u/.persona/assistant"

// routeEnv is a manager with one task, "修登录崩溃", handed out by leader conversation lead1 (unless leaders is false: then
// the main conversation conv1 supervises it and no leader is configured), its agents s1 and s2 on mac.
type routeEnv struct {
	*fleetTest
	todo   Todo
	agents []proto.Session // mac's sessions
}

func newRouteEnv(t *testing.T, st Store, leaders bool) *routeEnv {
	t.Helper()
	old := askHoldFor
	t.Cleanup(func() { askHoldFor = old }) // after the fleet drains below (Cleanup is LIFO), never beside a late digest
	var ft *fleetTest
	if st == nil {
		ft = newFleetTest(t)
	} else {
		ft = newFleetTestOn(t, st)
	}
	s, f := ft.s, ft.f
	shortSupervise(t, s)
	noDirectionsTimers(s)
	askHoldFor = time.Hour // tests move cards up by hand (askTick)
	ctx := context.Background()
	f.hello.Assistant.MachineID = "server"
	f.known["conv1"], f.known["lead1"] = true, true
	ft.d.add("server", "Server")
	convs := []proto.Session{{SessionID: "conv1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
		{SessionID: "lead1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle}}
	ft.sessions("server", convs...)
	f.setSessions("server", convs...)
	agents := []proto.Session{
		{SessionID: "worker-1", MachineID: "mac", Title: "修登录", Cwd: "/code/app", Status: proto.StatusBlocked, UpdatedAt: time.Now().UTC()},
		{SessionID: "worker-2", MachineID: "mac", Title: "补测试", Cwd: "/code/app", Status: proto.StatusBlocked, UpdatedAt: time.Now().UTC()}}
	ft.sessions("mac", agents...)
	f.setSessions("mac", agents...)
	supervisor := "conv1"
	setSettings(t, s, func(v *Settings) {
		v.Main, v.MachineID = "conv1", "server"
		if leaders {
			v.Leaders, supervisor = []string{"lead1"}, "lead1"
		} else {
			off := false
			v.MainTasksQuiet = &off // its card is pushed, as any non-quiet task's
		}
	})
	todo, err := s.createTodo(ctx, "修登录崩溃", "iPad 横屏时登录页崩溃", supervisor, "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, supervisor, []string{"PR 合并到 main"}, 30)
	for _, a := range agents {
		if err := s.linkTodoAgent(ctx, todo.ID, "mac", a.SessionID, a.Title, a.Cwd, "claude"); err != nil {
			t.Fatal(err)
		}
	}
	return &routeEnv{fleetTest: ft, todo: todoNow(s, todo.ID), agents: agents}
}

func askQuestion(id, session string) proto.Interaction {
	return proto.Interaction{ID: id, MachineID: "mac", SessionID: session, Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "用哪个分支？", Options: []proto.QuestionOption{{Label: "main"}, {Label: "feat/x"}}}},
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
}

func askPermission(id, session string) proto.Interaction {
	return proto.Interaction{ID: id, MachineID: "mac", SessionID: session, Kind: proto.IxPermission, ToolName: "Bash",
		ToolInput: []byte(`{"command":"git push origin feat/x"}`), CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
}

// raise asks ix as a hook would; the channel has its answer once it has one.
func (e *routeEnv) raise(ix proto.Interaction) <-chan proto.Resolution {
	out := make(chan proto.Resolution, 1)
	go func() {
		res, _ := e.s.waitInteraction(context.Background(), ix)
		out <- res
	}()
	e.waitPendingID(ix.ID)
	return out
}

func (e *routeEnv) waitPendingID(id string) {
	e.t.Helper()
	waitFor(e.t, "card "+id, func() bool { _, ok := e.s.pendingIx(id); return ok })
}

// pushesOf counts the pushes of the cards ids.
func (e *routeEnv) pushesOf(ids ...string) int {
	n := 0
	for _, p := range e.f.pushed() {
		for _, id := range ids {
			if p.Interaction != nil && p.Interaction.ID == id {
				n++
			}
		}
	}
	return n
}

// shown is the card id as the apps were told of it last, and whether they were told of it at all: a waitFor check
// asks this one, as a card not shown yet is "not yet" there, not a failure (#4407).
func (e *routeEnv) shown(id string) (proto.Interaction, bool) {
	df := &delegateFleet{fleetTest: e.fleetTest}
	list := df.shownAs(id)
	if len(list) == 0 {
		return proto.Interaction{}, false
	}
	return list[len(list)-1], true
}

// lastShown is the card id as the apps were told of it last, for a card that must have been shown by now.
func (e *routeEnv) lastShown(id string) proto.Interaction {
	e.t.Helper()
	ix, ok := e.shown(id)
	if !ok {
		e.t.Fatalf("card %s was never shown", id)
	}
	return ix
}

// noticeWith waits for a message into conversation to that says want, and answers it.
func (e *routeEnv) noticeWith(to, want string) string {
	e.t.Helper()
	var got string
	waitFor(e.t, to+" told "+want, func() bool {
		for _, n := range e.f.noticesTo(to) {
			if strings.Contains(n, want) {
				got = n
				return true
			}
		}
		return false
	})
	return got
}

// settleService waits until s's supervision messages due have gone out. Taking the digest lock for each observation
// covers restoration as well as delivery and closes the window where a finishing delivery arms its retry.
func settleService(t *testing.T, s *Service) {
	t.Helper()
	b := &s.supervise
	waitFor(t, "the supervision messages due to go out", func() bool {
		b.digestMu.Lock()
		defer b.digestMu.Unlock()
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.pending) == 0 && b.timer == nil
	})
}

// settle is settleService for e's own manager.
func (e *routeEnv) settle() {
	e.t.Helper()
	settleService(e.t, e.s)
}

// noDirectionsTimers keeps s's directions comparisons from running at all: they are timers of their own, and one left armed
// past its test logs through that test's T (a database already closed) after it ended. The route tests do not read
// what they compare.
func noDirectionsTimers(s *Service) {
	s.directions.after = func(time.Duration, func()) {}
	s.dtree.after = func(time.Duration, func()) {}
}

// supervisorState says what the apps see the two assistant conversations in: the main conversation and the leader in
// their statuses (working while on a turn).
func (e *routeEnv) supervisorState(main, lead string) {
	convs := []proto.Session{
		{SessionID: "conv1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: main},
		{SessionID: "lead1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: lead}}
	e.sessions("server", convs...)
	e.f.setSessions("server", convs...)
}

// A worker's question goes to its leader, not the user: no push, the card shown as delegating, the main
// conversation not told.
func TestAskGoesToTheLeaderFirst(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	e.raise(askQuestion("ix1", "worker-1"))
	n := e.noticeWith("lead1", "interaction_id=ix1")
	for _, want := range []string{"signal=ask", "先交给你处理，用户还没收到", "用哪个分支？", "machine=Mac", "session_id=worker-1"} {
		if !strings.Contains(n, want) {
			t.Errorf("the leader's message lacks %q:\n%s", want, n)
		}
	}
	e.settle()
	if got := e.pushesOf("ix1"); got != 0 {
		t.Fatalf("the user was pushed %d times", got)
	}
	if !e.lastShown("ix1").Delegating {
		t.Fatal("the card is not shown as delegating")
	}
	e.s.chans.mu.Lock()
	carded := e.s.chans.announced["ix1"]
	e.s.chans.mu.Unlock()
	if carded {
		t.Fatal("the Feishu bot and the channels were given the card")
	}
	if got := e.f.noticesTo("conv1"); len(got) != 0 {
		t.Fatalf("the main conversation was told: %q", got)
	}
}

// The leader answers the question: the agent gets the answer, nothing reaches the user, and the answer is the task's
// decision (the leader's call, not the user's).
func TestLeaderAnswersAnAsk(t *testing.T) {
	database := todoTestDB(t)
	e := newRouteEnv(t, database, true)
	got := e.raise(askQuestion("ix1", "worker-1"))
	e.noticeWith("lead1", "interaction_id=ix1")
	if _, err := runJSON(t, e.s, "conv2", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-1",
		"interaction_id": "ix1", "answers": map[string]any{"用哪个分支？": "main"}}); err != nil {
		t.Fatalf("another conversation may still answer a question as before: %v", err)
	}
	if res := answerOf(t, got); res.Answers["用哪个分支？"] != "main" || res.AnsweredBy != proto.AnsweredByAssistant {
		t.Fatalf("the agent got %+v", res)
	}
	// conv2 is not its rung: no decision of its call.
	if list, _ := database.ListTodoDecisions(context.Background(), e.todo.ID, false); len(list) != 0 {
		t.Fatalf("an answer off the ladder became a decision: %+v", list)
	}

	got = e.raise(askQuestion("ix2", "worker-1"))
	e.noticeWith("lead1", "interaction_id=ix2")
	res, err := runJSON(t, e.s, "lead1", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-1",
		"interaction_id": "ix2", "answers": map[string]any{"用哪个分支？": "feat/x"}, "reason": "任务就在 feat/x 上"})
	if err != nil || !strings.Contains(res, "feat/x") {
		t.Fatalf("the leader's answer: %s %v", res, err)
	}
	if r := answerOf(t, got); r.Answers["用哪个分支？"] != "feat/x" || r.Reason != "任务就在 feat/x 上" {
		t.Fatalf("the agent got %+v", r)
	}
	var list []db.TodoDecisionRow
	waitFor(t, "the decision", func() bool {
		list, _ = database.ListTodoDecisions(context.Background(), e.todo.ID, false)
		return len(list) == 1
	})
	d := list[0]
	if d.Question != "用哪个分支？" || d.Choice != "feat/x" || d.Source != db.DecisionAgent || d.SourceSession != "lead1" ||
		d.SourceMessage != "ix2" || d.Reason != "任务就在 feat/x 上" {
		t.Fatalf("decision = %+v", d)
	}
	e.settle()
	e.s.askTick(time.Now().Add(24 * time.Hour)) // answered and claimed cards never ring on a later routing pass
	if n := e.pushesOf("ix1", "ix2"); n != 0 {
		t.Fatalf("the user was pushed %d times", n)
	}
}

// The leader cannot allow a tool that acts in the user's name; it escalates: the user gets one push with its reason and
// recommendation, the main conversation an escalated entry, and the user's approval goes to the agent and is kept as
// the task's decision.
func TestLeaderEscalatesAnApproval(t *testing.T) {
	database := todoTestDB(t)
	e := newRouteEnv(t, database, true)
	ix := askPermission("ix1", "worker-1")
	ix.ToolInput = []byte(`{"command":"gh pr comment 12 --body done"}`) // in the user's name: theirs, not the leader's
	got := e.raise(ix)
	n := e.noticeWith("lead1", "interaction_id=ix1")
	if !strings.Contains(n, "，权限，session_id=worker-1") || !strings.Contains(n, "Bash") {
		t.Fatalf("the leader's message:\n%s", n)
	}
	if _, err := runJSON(t, e.s, "lead1", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-1",
		"interaction_id": "ix1", "allow": true}); err == nil || !strings.Contains(err.Error(), "only the user") || !strings.Contains(err.Error(), "ask_escalate") {
		t.Fatalf("the leader allowed a comment in the user's name: %v", err)
	}
	if _, err := runJSON(t, e.s, "conv2", "ask_escalate", map[string]any{"todo_id": e.todo.ID, "reason": "x"}); err == nil {
		t.Fatal("a conversation not supervising the task escalated its cards")
	}
	res, err := runJSON(t, e.s, "lead1", "ask_escalate", map[string]any{"todo_id": e.todo.ID,
		"reason": "要以你的名义在 PR 上评论", "recommend": "允许"})
	if err != nil || !strings.Contains(res, "1 card(s)") {
		t.Fatalf("escalate: %s %v", res, err)
	}
	waitFor(t, "the push", func() bool { return e.pushesOf("ix1") == 1 })
	shown := e.lastShown("ix1")
	if shown.Delegating || shown.Delegate == nil || shown.Delegate.Reason != "要以你的名义在 PR 上评论" || shown.Delegate.Recommend != "允许" {
		t.Fatalf("the user's card: %+v %+v", shown, shown.Delegate)
	}
	m := e.noticeWith("conv1", "负责它的 leader 对话判断要用户决定")
	for _, want := range []string{"它的 Agent 在等的卡片已经发给用户", "以你的名义在 PR 上评论", "leader 推荐：允许", "interaction_id=ix1", "signal=escalated"} {
		if !strings.Contains(m, want) {
			t.Errorf("the main conversation's message lacks %q:\n%s", want, m)
		}
	}
	if _, err := runJSON(t, e.s, "lead1", "ask_escalate", map[string]any{"todo_id": e.todo.ID, "reason": "again"}); err == nil {
		t.Fatal("escalated again what the user was told of")
	}

	yes := true
	if err := e.s.resolve("ix1", proto.Resolution{Allow: &yes}); err != nil {
		t.Fatal(err)
	}
	if r := answerOf(t, got); r.Allow == nil || !*r.Allow {
		t.Fatalf("the agent got %+v", r)
	}
	var list []db.TodoDecisionRow
	waitFor(t, "the decision", func() bool {
		list, _ = database.ListTodoDecisions(context.Background(), e.todo.ID, false)
		return len(list) == 1
	})
	if d := list[0]; d.Source != db.DecisionCard || d.SourceMessage != "ix1" || d.SourceSession != "worker-1" || d.Question == "" || d.Choice == "" {
		t.Fatalf("decision = %+v", d)
	}
	e.settle()
	if n := e.pushesOf("ix1"); n != 1 {
		t.Fatalf("pushed %d times", n)
	}
}

// Two cards of one task, from two of its agents, are one entry for the leader and one push for the user.
func TestAsksOfOneTaskRingAsOne(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	e.raise(askQuestion("ix1", "worker-1"))
	e.raise(askPermission("ix2", "worker-2"))
	n := e.noticeWith("lead1", "interaction_id=ix2")
	if !strings.Contains(n, "interaction_id=ix1") || strings.Count(n, "任务「修登录崩溃」") != 1 {
		t.Fatalf("not one entry with both cards:\n%s", n)
	}
	if _, err := runJSON(t, e.s, "lead1", "ask_escalate", map[string]any{"todo_id": e.todo.ID, "reason": "两件事要你定"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the push", func() bool { return e.pushesOf("ix1", "ix2") == 1 })
	e.settle()
	if n := e.pushesOf("ix1", "ix2"); n != 1 {
		t.Fatalf("%d pushes for one escalation", n)
	}
	for _, id := range []string{"ix1", "ix2"} {
		if ix := e.lastShown(id); ix.Delegating || ix.Delegate == nil {
			t.Fatalf("%s not handed to the user: %+v", id, ix)
		}
	}
}

// A card nobody handles goes up a rung at a time, from the leader to the main conversation, then back to its leader,
// and never to the user on its own.
func TestUnhandledAskGoesUp(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	e.raise(askQuestion("ix1", "worker-1"))
	e.noticeWith("lead1", "interaction_id=ix1")
	e.s.askTick(time.Now().Add(2 * askHoldFor))
	m := e.noticeWith("conv1", "interaction_id=ix1")
	for _, want := range []string{"signal=ask", "interaction_id=ix1", "下一层没有及时处理，上交到你这里"} {
		if !strings.Contains(m, want) {
			t.Errorf("the main conversation's message lacks %q:\n%s", want, m)
		}
	}
	if strings.Contains(m, "你是这些任务的 leader 对话") {
		t.Fatalf("the main conversation is told it is the leader:\n%s", m)
	}
	e.s.askTick(time.Now().Add(4 * askHoldFor))
	if r := e.noticeWith("lead1", "再交给你处理"); !strings.Contains(r, "interaction_id=ix1") {
		t.Fatalf("the leader's reminder:\n%s", r)
	}
	e.s.askTick(time.Now().Add(6 * askHoldFor))
	e.settle()
	if n := e.pushesOf("ix1"); n != 0 {
		t.Fatalf("the user was pushed %d times on a timeout", n)
	}
	if !e.lastShown("ix1").Delegating {
		t.Fatal("the card is no longer shown as its supervisors'")
	}
	if todo, ok := e.s.routedTo("ix1", "lead1", false); !ok || todo != e.todo.ID {
		t.Fatal("the card is not parked with its leader")
	}
}

// A leader (and then the main conversation) on a long turn has not neglected the card: its hold does not count while
// the conversation works, so a card does not climb merely because the turn is long; its time starts when the turn
// ends. A conversation still on a turn askBusyMax after the card came is stuck instead, and the card moves on: past the
// last rung, back to the leader, never to the user.
func TestAskHoldWaitsForABusySupervisor(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	oldHold, oldBusy := askHoldFor, askBusyMax
	askHoldFor, askBusyMax = time.Minute, 10*time.Minute
	t.Cleanup(func() { askHoldFor, askBusyMax = oldHold, oldBusy })
	e.raise(askQuestion("ix1", "worker-1"))
	e.noticeWith("lead1", "interaction_id=ix1")
	start := time.Now()

	e.supervisorState(proto.StatusIdle, proto.StatusWorking) // the leader is on a long turn
	e.s.askTick(start.Add(2 * time.Minute))
	e.s.askTick(start.Add(5 * time.Minute))
	e.settle()
	if got := e.f.noticesTo("conv1"); len(got) != 0 {
		t.Fatalf("the card climbed while its leader worked: %q", got)
	}
	if _, ok := e.s.routedTo("ix1", "lead1", false); !ok {
		t.Fatal("the card left its leader")
	}

	e.supervisorState(proto.StatusIdle, proto.StatusIdle) // the turn ended: the hold starts now
	e.s.askTick(start.Add(6*time.Minute + 30*time.Second))
	e.noticeWith("conv1", "interaction_id=ix1") // its time was up: the main conversation has it

	e.supervisorState(proto.StatusWorking, proto.StatusIdle) // and it is on a long turn too
	e.s.askTick(start.Add(8 * time.Minute))
	e.settle()
	if got := e.pushesOf("ix1"); got != 0 {
		t.Fatalf("the card reached the user while the main conversation worked (%d)", got)
	}
	if _, ok := e.s.routedTo("ix1", "conv1", true); !ok {
		t.Fatal("the card left the main conversation")
	}

	e.s.askTick(start.Add(17 * time.Minute)) // busy for longer than askBusyMax: stuck, not working: back to the leader
	e.noticeWith("lead1", "再交给你处理")
	e.settle()
	if got := e.pushesOf("ix1"); got != 0 {
		t.Fatalf("the card reached the user on a timeout (%d)", got)
	}
}

// Permission and plan cards are their supervisor's to decide: no deadline hands them to the owner. Past the last rung
// they go back to the leader and stay there, and the leader's allow reaches the agent marked as the assistant's answer
// (never the user's), kept as the task's decision made by the leader. A conversation the card was not handed to cannot
// answer it, and an answer must say allow.
func TestSupervisorDecidesAnApprovalWithoutADeadline(t *testing.T) {
	for _, kind := range []string{proto.IxPermission, proto.IxPlan} {
		t.Run(kind, func(t *testing.T) {
			database := todoTestDB(t)
			e := newRouteEnv(t, database, true)
			oldHold, oldBusy := askHoldFor, askBusyMax
			askHoldFor, askBusyMax = 5*time.Minute, time.Hour
			t.Cleanup(func() { askHoldFor, askBusyMax = oldHold, oldBusy })
			ix := askPermission("ix1", "worker-1")
			if kind == proto.IxPlan {
				ix.Kind, ix.ToolName, ix.ToolInput, ix.Plan = proto.IxPlan, "ExitPlanMode", nil, "Merge the branch, then run tests."
			}
			answered := e.raise(ix)
			e.noticeWith("lead1", "interaction_id=ix1")
			e.s.fleet.mu.Lock()
			routedAt := e.s.fleet.interactions[ix.ID].route.routedAt
			e.s.fleet.mu.Unlock()

			e.s.askTick(routedAt.Add(6 * time.Minute)) // the leader's hold: up to the main conversation
			e.noticeWith("conv1", "interaction_id=ix1")
			e.s.askTick(routedAt.Add(12 * time.Minute)) // past the last rung: back to the leader, not the owner
			e.noticeWith("lead1", "再交给你处理")
			for _, at := range []time.Duration{15 * time.Minute, 30 * time.Minute, 2 * time.Hour} {
				e.s.askTick(routedAt.Add(at)) // the old 15-minute deadline, and long after it
			}
			e.settle()
			if got := e.pushesOf(ix.ID); got != 0 {
				t.Fatalf("the owner was pushed %d times on a timeout", got)
			}
			if !e.lastShown(ix.ID).Delegating {
				t.Fatal("the card is no longer shown as its supervisors'")
			}
			if _, ok := e.s.routedTo(ix.ID, "lead1", false); !ok {
				t.Fatal("the card is not parked with its leader")
			}

			if _, err := runJSON(t, e.s, "conv2", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-1",
				"interaction_id": "ix1", "allow": true}); err == nil || !strings.Contains(err.Error(), "only the user") {
				t.Fatalf("a conversation the card was not handed to allowed it: %v", err)
			}
			if _, err := runJSON(t, e.s, "lead1", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-1",
				"interaction_id": "ix1"}); err == nil || !strings.Contains(err.Error(), "allow is required") {
				t.Fatalf("an answer without allow: %v", err)
			}
			res, err := runJSON(t, e.s, "lead1", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-1",
				"interaction_id": "ix1", "allow": true, "reason": "推任务分支在任务范围内"})
			if err != nil || !strings.Contains(res, "not the user's") {
				t.Fatalf("the leader's allow: %s %v", res, err)
			}
			got := answerOf(t, answered)
			if got.AnsweredBy == "" || got.Reason != "推任务分支在任务范围内" || got.UserAllowed() {
				t.Fatalf("the agent got the leader's answer as the user's: %+v", got)
			}
			if kind == proto.IxPermission && (got.Allow == nil || !*got.Allow) {
				t.Fatalf("the agent was not allowed: %+v", got)
			}
			if kind == proto.IxPlan && (got.Approve == nil || !*got.Approve) {
				t.Fatalf("the plan was not approved: %+v", got)
			}
			var list []db.TodoDecisionRow
			waitFor(t, "the decision", func() bool {
				list, _ = database.ListTodoDecisions(context.Background(), e.todo.ID, false)
				return len(list) == 1
			})
			if d := list[0]; d.Source != db.DecisionAgent || d.SourceSession != "lead1" || d.SourceMessage != "ix1" ||
				d.Reason != "推任务分支在任务范围内" || d.Choice == "" {
				t.Fatalf("decision = %+v", d)
			}
			e.settle()
			if n := e.pushesOf(ix.ID); n != 0 {
				t.Fatalf("the answered card rang %d times", n)
			}
		})
	}
}

// What would go out in the user's name, spend money or delete, and a card with a form, stay the user's: the
// supervisor cannot allow them and is told to escalate; the card stays pending for the user.
func TestSupervisorCannotAnswerTheUsersApprovals(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	comment := askPermission("ix1", "worker-1")
	comment.ToolInput = []byte(`{"command":"gh pr comment 12 --body fixed"}`)
	e.raise(comment)
	e.noticeWith("lead1", "interaction_id=ix1")
	if _, err := runJSON(t, e.s, "lead1", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-1",
		"interaction_id": "ix1", "allow": true}); err == nil || !strings.Contains(err.Error(), "only the user") || !strings.Contains(err.Error(), "ask_escalate") {
		t.Fatalf("the leader allowed a comment in the user's name: %v", err)
	}
	if _, err := runJSON(t, e.s, "conv1", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-1",
		"interaction_id": "ix1", "allow": true}); err == nil || !strings.Contains(err.Error(), "only the user") {
		t.Fatalf("the main conversation allowed a comment in the user's name: %v", err)
	}
	form := askPermission("ix2", "worker-2")
	form.Fields = []proto.Field{{Key: "password", Label: "Password", Secret: true}}
	e.raise(form)
	if _, err := runJSON(t, e.s, "lead1", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-2",
		"interaction_id": "ix2", "allow": true}); err == nil || !strings.Contains(err.Error(), "only the user") {
		t.Fatalf("the leader allowed a card with a password form: %v", err)
	}
	for _, id := range []string{"ix1", "ix2"} {
		if _, pending := e.s.pendingIx(id); !pending {
			t.Fatalf("card %s was answered", id)
		}
	}
	e.settle()
}

// Denying an owner-only approval executes nothing, so the supervisor answers it directly. Only an affirmative answer
// remains the owner's; the denial and its reason go to the agent as the assistant's decision, never the user's.
func TestSupervisorDeniesOwnerOnlyApproval(t *testing.T) {
	for _, kind := range []string{proto.IxPermission, proto.IxPlan} {
		t.Run(kind, func(t *testing.T) {
			e := newRouteEnv(t, nil, true)
			ix := askPermission("ix1", "worker-1")
			ix.ToolName = "mcp__mail__send"
			ix.ToolInput = []byte(`{"body":"A plain project update."}`)
			if kind == proto.IxPlan {
				ix.Kind, ix.ToolName, ix.ToolInput, ix.Plan = proto.IxPlan, "ExitPlanMode", nil, "Delete the old user records."
			}
			answered := e.raise(ix)
			e.noticeWith("lead1", "interaction_id=ix1")
			if _, err := runJSON(t, e.s, "lead1", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-1",
				"interaction_id": "ix1", "allow": true}); err == nil || !strings.Contains(err.Error(), "only the user") || !strings.Contains(err.Error(), "ask_escalate") {
				t.Fatalf("the supervisor allowed an owner-only %s: %v", kind, err)
			}

			reason := "Do not perform this action."
			res, err := runJSON(t, e.s, "lead1", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-1",
				"interaction_id": "ix1", "allow": false, "reason": reason})
			verb := "Denied"
			if kind == proto.IxPlan {
				verb = "Rejected the plan"
			}
			if err != nil || !strings.Contains(res, verb) || !strings.Contains(res, "not the user's") {
				t.Fatalf("the supervisor denied an owner-only %s: %s %v", kind, res, err)
			}
			got := answerOf(t, answered)
			if got.AnsweredBy != proto.AnsweredByAssistant || got.Reason != reason {
				t.Fatalf("the agent got the supervisor's denial as %+v", got)
			}
			if kind == proto.IxPermission && (got.Allow == nil || *got.Allow || got.Message != reason) {
				t.Fatalf("the permission denial = %+v", got)
			}
			if kind == proto.IxPlan && (got.Approve == nil || *got.Approve || got.Feedback != reason) {
				t.Fatalf("the plan denial = %+v", got)
			}
			if _, pending := e.s.pendingIx(ix.ID); pending {
				t.Fatal("the denied card is still pending")
			}
			e.settle()
			if n := e.pushesOf(ix.ID); n != 0 {
				t.Fatalf("the denied card rang the owner %d times", n)
			}
		})
	}
}

// userApproval keeps for the user what is theirs by the decision rights: forms and secrets, owner actions, what goes
// out in their name, spending and deleting; pushing, merging and deploying are the supervisor's.
func TestUserApproval(t *testing.T) {
	for _, tc := range []struct {
		ix   proto.Interaction
		user bool
	}{
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Bash", ToolInput: []byte(`{"command":"git push origin feat/x"}`)}, false},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Bash", ToolInput: []byte(`{"command":"gh pr merge 12 --squash"}`)}, false},
		{proto.Interaction{Kind: proto.IxPlan, Plan: "Merge the PR, then deploy to production."}, false},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Bash", ToolInput: []byte(`{"command":"gh issue comment 3 --body hi"}`)}, true},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Bash", ToolInput: []byte(`{"command":"send the release announcement"}`)}, true},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Bash", ToolInput: []byte(`{"command":"delete the old user records"}`)}, true},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Bash", ToolInput: []byte(`{"command":"为订阅付费"}`)}, true}, // i18n-ignore: synthetic payment command
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "mcp__mail__send", Summary: "Send mail to Bob"}, true},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Write", ToolInput: []byte(`{"file_path":"/work/notes.md","content":"删除旧数据，再发表评论"}`)}, false}, // i18n-ignore: synthetic file payload
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Edit", EditableText: "Delete this comment", ToolInput: []byte(`{"file_path":"/work/notes.md","new_string":"Delete this comment"}`)}, false},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "File /work/draft.md", ToolInput: []byte(`{"TargetFile":"/work/draft.md","CodeContent":"删除该字段"}`)}, false}, // i18n-ignore: synthetic Antigravity file payload
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "File /work/review.md", ToolInput: []byte(`{"Path":"/work/review.md","Content":"Replay the comment"}`)}, false},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "File /work/edit.md", ToolInput: []byte(`{"TargetFile":"/work/edit.md","TargetContent":"old","ReplacementContent":"Delete this comment"}`)}, false},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "File /work/run.json", ToolInput: []byte(`{"command":"send mail"}`)}, true},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Skill", ToolInput: []byte(`{"skill":"agent-browser","args":"只读检查、不发送邮件、不改设置"}`)}, false}, // i18n-ignore: synthetic skill arguments
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Skill", ToolInput: []byte(`{"skill":"agent-browser","args":"Do not send mail or delete files"}`)}, false},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Skill", Fields: []proto.Field{{Key: "otp", Label: "OTP"}}}, true},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Skill", Secret: true}, true},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Skill", OwnerProposal: true}, true},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "mcp__mail__reply", ToolInput: []byte(`{"body":"A plain project update."}`)}, true},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "mcp__github__issue_comment", ToolInput: []byte(`{"body":"A plain project update."}`)}, true},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "todo_remove", ToolInput: []byte(`{"id":"item"}`)}, true},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "shop_charge", ToolInput: []byte(`{"item":"standard"}`)}, true},
		{proto.Interaction{Kind: proto.IxPlan, Plan: "Delete the old user records."}, true},
		{proto.Interaction{Kind: proto.IxPlan, Plan: "删除旧数据"}, true}, // i18n-ignore: synthetic card
		{proto.Interaction{Kind: proto.IxPlan, Plan: "给账号充值"}, true}, // i18n-ignore: synthetic card
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Bash", Fields: []proto.Field{{Key: "otp", Label: "OTP"}}}, true},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Bash", Secret: true}, true},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: proto.SecretHandoffTool}, true},
		{proto.Interaction{Kind: proto.IxPermission, ToolName: "Bash", OwnerProposal: true}, true},
	} {
		if got := userApproval(tc.ix) != ""; got != tc.user {
			t.Errorf("%s %s %s: the user's = %v, want %v (%q)", tc.ix.ToolName, tc.ix.ToolInput, tc.ix.Plan, got, tc.user, userApproval(tc.ix))
		}
	}
	for _, w := range userApprovalWords {
		if !slices.Contains(riskyWords, w) {
			t.Errorf("%q is not one of riskyWords", w)
		}
	}
}

// A supervisor that needs the owner itself immediately gives an owner-only card to the next rung; the main
// conversation needing the owner does not hand it to them: neither a timeout nor a release reaches the owner.
func TestOwnerOnlyAskReleasesWhenSupervisorNeedsYou(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	e.raise(askPermission("ix1", "worker-1"))
	plan := askPermission("ix2", "worker-2")
	plan.Kind, plan.ToolName, plan.ToolInput, plan.Plan = proto.IxPlan, "ExitPlanMode", nil, "Merge the branch, then run tests."
	e.raise(plan)
	e.noticeWith("lead1", "interaction_id=ix2")
	e.supervisorState(proto.StatusIdle, proto.StatusBlocked)
	e.s.askTick(time.Now())
	e.noticeWith("conv1", "interaction_id=ix2")
	if got := e.pushesOf("ix1", "ix2"); got != 0 {
		t.Fatalf("the owner was pushed before the main chat needed them too: %d", got)
	}
	for _, id := range []string{"ix1", "ix2"} {
		if _, ok := e.s.routedTo(id, "lead1", false); ok {
			t.Fatalf("the leader kept owner-only card %s while needing the owner", id)
		}
	}

	e.supervisorState(proto.StatusBlocked, proto.StatusIdle)
	e.s.askTick(time.Now().Add(time.Second))
	e.s.askTick(time.Now().Add(2 * time.Second))
	e.settle()
	if got := e.pushesOf("ix1", "ix2"); got != 0 {
		t.Fatalf("the owner was pushed %d times because the main conversation needs them", got)
	}
	for _, id := range []string{"ix1", "ix2"} {
		if _, ok := e.s.routedTo(id, "conv1", false); !ok {
			t.Fatalf("owner-only card %s left the main conversation", id)
		}
	}
}

func TestQuestionKeepsItsHoldWhenSupervisorNeedsYou(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	e.raise(askQuestion("ix1", "worker-1"))
	e.noticeWith("lead1", "interaction_id=ix1")
	e.supervisorState(proto.StatusIdle, proto.StatusBlocked)
	e.s.askTick(time.Now())
	if _, ok := e.s.routedTo("ix1", "lead1", false); !ok {
		t.Fatal("a question left its supervisor before its ordinary hold")
	}
	for _, n := range e.f.noticesTo("conv1") {
		if strings.Contains(n, "interaction_id=ix1") {
			t.Fatalf("the main chat was given the question early:\n%s", n)
		}
	}
	if got := e.pushesOf("ix1"); got != 0 {
		t.Fatalf("the owner was pushed %d times", got)
	}
	e.settle()
}

// A manager that restarts finds the cards it left pending on their way to the task's supervisor again: the leader can
// still answer one and escalate another.
func TestRestoredAskFindsItsSupervisorAgain(t *testing.T) {
	database := todoTestDB(t)
	e := newRouteEnv(t, database, true)
	ask := askQuestion("ix1", "worker-1")
	ask.Resumable, ask.ExpiresAt = true, time.Now().Add(time.Hour).UTC()
	approve := askPermission("ix2", "worker-2")
	approve.Resumable, approve.ExpiresAt = true, time.Now().Add(time.Hour).UTC()
	ctx, gone := context.WithCancel(context.Background())
	waits := make(chan proto.Resolution, 2)
	for _, ix := range []proto.Interaction{ask, approve} {
		go func() { r, _ := e.s.waitInteraction(ctx, ix); waits <- r }()
		e.waitPendingID(ix.ID)
	}
	e.noticeWith("lead1", "interaction_id=ix2")
	gone() // the manager went: its hooks' waits end and their cards stay pending for the next one
	for range 2 {
		<-waits
	}

	e.f.mu.Lock()
	e.f.sends, e.f.told, e.f.pushes = nil, nil, nil // what the first manager told and pushed
	e.f.mu.Unlock()
	s2 := newRestartedService(t, Options{Hub: e.f, Store: func() Store { return database }, Logf: t.Logf, Devices: e.d})
	shortSupervise(t, s2)
	noDirectionsTimers(s2)

	// A node may claim a card before the hub's handshake restores the others: it finds its route then too.
	answer := make(chan proto.Resolution, 1)
	go func() { r, _ := s2.waitInteraction(context.Background(), ask); answer <- r }()
	waitFor(t, "the hook waiting again", func() bool {
		s2.fleet.mu.Lock()
		defer s2.fleet.mu.Unlock()
		p := s2.fleet.interactions[ask.ID]
		return p != nil && p.resolved != nil // await made its channel: the wait is on the card
	})
	if got, ok := s2.routedTo("ix1", "lead1", false); !ok || got != e.todo.ID {
		t.Fatalf("the card its node claimed before the handshake: route to its leader = %q, %v", got, ok)
	}

	s2.FleetUp(context.Background())
	for _, id := range []string{"ix1", "ix2"} {
		if got, ok := s2.routedTo(id, "lead1", false); !ok || got != e.todo.ID {
			t.Fatalf("restored card %s: route to its leader = %q, %v", id, got, ok)
		}
		if ix, ok := s2.pendingIx(id); !ok || !ix.Delegating {
			t.Fatalf("restored card %s: %+v %v, want it shown delegating", id, ix, ok)
		}
	}
	// The hooks reconnect through the next node; the leader answers the question...
	if _, err := runJSON(t, s2, "lead1", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-1",
		"interaction_id": "ix1", "answers": map[string]any{"用哪个分支？": "main"}, "reason": "任务就在 main 上"}); err != nil {
		t.Fatalf("the leader could not answer the restored card: %v", err)
	}
	if r := answerOf(t, answer); r.Answers["用哪个分支？"] != "main" || r.AnsweredBy != proto.AnsweredByAssistant {
		t.Fatalf("the agent got %+v", r)
	}
	var list []db.TodoDecisionRow
	waitFor(t, "the decision", func() bool {
		list, _ = database.ListTodoDecisions(context.Background(), e.todo.ID, false)
		return len(list) == 1
	})
	if d := list[0]; d.Source != db.DecisionAgent || d.SourceSession != "lead1" || d.SourceMessage != "ix1" {
		t.Fatalf("decision = %+v", d)
	}
	// ...and escalates the approval it cannot give.
	if _, err := runJSON(t, s2, "lead1", "ask_escalate", map[string]any{"todo_id": e.todo.ID, "reason": "要推到远端", "recommend": "允许"}); err != nil {
		t.Fatalf("the leader could not escalate the restored card: %v", err)
	}
	waitFor(t, "the push", func() bool { return e.pushesOf("ix2") == 1 })
	if ix := e.lastShown("ix2"); ix.Delegating || ix.Delegate == nil || ix.Delegate.Reason != "要推到远端" {
		t.Fatalf("the user's card: %+v %+v", ix, ix.Delegate)
	}
	if n := e.pushesOf("ix1"); n != 0 {
		t.Fatalf("the answered card was pushed %d times", n)
	}
	settleService(t, s2)
}

// An approval restored after a restart goes to its task's supervisor again and, as any card, never reaches the owner
// on a timeout; the supervisor can still allow it, as its own call.
func TestRestoredApprovalStaysWithItsSupervisor(t *testing.T) {
	database := todoTestDB(t)
	e := newRouteEnv(t, database, true)
	ix := askPermission("ix1", "worker-1")
	ix.Resumable, ix.ExpiresAt = true, time.Now().Add(2*time.Hour).UTC()
	ix.CreatedAt = time.Now().Add(-time.Hour).UTC()
	ctx, gone := context.WithCancel(context.Background())
	wait := make(chan proto.Resolution, 1)
	go func() { r, _ := e.s.waitInteraction(ctx, ix); wait <- r }()
	e.waitPendingID(ix.ID)
	gone()
	<-wait

	s2 := newRestartedService(t, Options{Hub: e.f, Store: func() Store { return database }, Logf: t.Logf, Devices: e.d})
	shortSupervise(t, s2)
	noDirectionsTimers(s2)
	answer := make(chan proto.Resolution, 1)
	go func() { r, _ := s2.waitInteraction(context.Background(), ix); answer <- r }()
	waitFor(t, "the hook waiting again", func() bool {
		s2.fleet.mu.Lock()
		defer s2.fleet.mu.Unlock()
		p := s2.fleet.interactions[ix.ID]
		return p != nil && p.resolved != nil
	})
	s2.FleetUp(context.Background())
	if _, ok := s2.routedTo("ix1", "lead1", false); !ok {
		t.Fatal("the restored card has no route to its leader")
	}
	for i := 2; i <= 8; i += 2 {
		s2.askTick(time.Now().Add(time.Duration(i) * askHoldFor))
	}
	settleService(t, s2)
	if n := e.pushesOf("ix1"); n != 0 {
		t.Fatalf("the restored approval rang %d times on a timeout", n)
	}
	if _, ok := s2.routedTo("ix1", "lead1", false); !ok {
		t.Fatal("the restored approval left its leader")
	}
	if list, _ := database.ListTodoDecisions(context.Background(), e.todo.ID, false); len(list) != 0 {
		t.Fatalf("the timeout became a decision: %+v", list)
	}
	if _, err := runJSON(t, s2, "lead1", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-1",
		"interaction_id": "ix1", "allow": true, "reason": "在任务范围内"}); err != nil {
		t.Fatalf("the leader could not allow the restored approval: %v", err)
	}
	if r := answerOf(t, answer); r.Allow == nil || !*r.Allow || r.AnsweredBy != proto.AnsweredByAssistant {
		t.Fatalf("the agent got %+v", r)
	}
	settleService(t, s2)
}

// A question restored after a restart stays with its supervisors when its time is up: never pushed, still pending.
func TestRestoredQuestionStaysWithItsSupervisor(t *testing.T) {
	database := todoTestDB(t)
	e := newRouteEnv(t, database, true)
	ix := askQuestion("ix1", "worker-1")
	ix.Resumable, ix.ExpiresAt = true, time.Now().Add(time.Hour).UTC()
	ctx, gone := context.WithCancel(context.Background())
	wait := make(chan proto.Resolution, 1)
	go func() { r, _ := e.s.waitInteraction(ctx, ix); wait <- r }()
	e.waitPendingID(ix.ID)
	gone()
	<-wait

	s2 := newRestartedService(t, Options{Hub: e.f, Store: func() Store { return database }, Logf: t.Logf, Devices: e.d})
	shortSupervise(t, s2)
	noDirectionsTimers(s2)
	s2.FleetUp(context.Background())
	if _, ok := s2.routedTo("ix1", "lead1", false); !ok {
		t.Fatal("the restored card has no route to its leader")
	}
	s2.askTick(time.Now().Add(2 * askHoldFor)) // its leader has had its time
	s2.askTick(time.Now().Add(3 * askHoldFor)) // the main conversation too: back to the leader
	settleService(t, s2)
	if n := e.pushesOf("ix1"); n != 0 {
		t.Fatalf("a restored question was pushed %d times", n)
	}
	if got, pending := s2.pendingIx("ix1"); !pending || !got.Delegating {
		t.Fatalf("the restored question left its supervisors: %+v %v", got, pending)
	}
	if _, ok := s2.routedTo("ix1", "lead1", false); !ok {
		t.Fatal("the restored question is not parked with its leader")
	}
}

// Without leaders, non-six-class permission and question cards on an open supervised task still take askLadder ->
// routeAsk -> active sigAsk wake-up and askTick reminders on the main conversation instead of deadlocking in
// handNonOwnerCard's passive keepRelayLater queue (#16291). The main conversation can answer or escalate them, while
// six-class owner cards, supervising conversations' own cards, closed/needs-user tasks, and agent-supervised workers
// retain their existing guards.
func TestAskWithoutLeadersRoutesSupervisedAsksToMain(t *testing.T) {
	for _, withBackground := range []bool{false, true} {
		name := "main_only"
		if withBackground {
			name = "with_background_supervisor"
		}
		t.Run(name, func(t *testing.T) {
			e := newRouteEnv(t, nil, false)
			ctx := context.Background()
			if withBackground {
				e.s.noBackground = false
				e.f.known["bg1"] = true
				convs := []proto.Session{
					{SessionID: "conv1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
					{SessionID: "lead1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
					{SessionID: "bg1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
				}
				e.sessions("server", convs...)
				e.f.setSessions("server", convs...)
				setSettings(t, e.s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
			}

			// Non-six-class IxPermission actively wakes main via sigAsk (never parked in keepRelayLater or routed to bg1),
			// enters askTick reminders, and can be answered by main.
			perm := askPermission("ix_perm_nolead", "worker-1")
			permDone := e.raise(perm)
			pn := e.noticeWith("conv1", "interaction_id="+perm.ID)
			if !strings.Contains(pn, "signal=ask") || !strings.Contains(pn, "先交给你处理") {
				t.Fatalf("main's permission supervision notice lacks sigAsk:\n%s", pn)
			}
			if !e.lastShown(perm.ID).Delegating {
				t.Fatal("routed permission is not shown as delegating")
			}
			if hasRelayFrom(e.s, "conv1", cardAgentPermission, "interaction_id="+perm.ID) {
				t.Fatal("supervised permission fell into handNonOwnerCard keepRelayLater queue")
			}
			if withBackground && len(e.f.noticesTo("bg1")) != 0 {
				t.Fatalf("background supervisor was woken for main-supervised permission: %q", e.f.noticesTo("bg1"))
			}
			e.s.askTick(time.Now().Add(2 * askHoldFor))
			if r := e.noticeWith("conv1", "再交给你处理"); !strings.Contains(r, "interaction_id="+perm.ID) {
				t.Fatalf("main did not receive askTick reminder for permission:\n%s", r)
			}
			if _, err := runJSON(t, e.s, "conv1", "interaction_answer", map[string]any{
				"machine": "mac", "session_id": "worker-1", "interaction_id": perm.ID,
				"allow": true, "reason": "Routine git push within task scope",
			}); err != nil {
				t.Fatalf("main could not answer routed permission: %v", err)
			}
			if r := answerOf(t, permDone); r.Allow == nil || !*r.Allow || r.AnsweredBy != proto.AnsweredByAssistant {
				t.Fatalf("permission resolution = %+v, want assistant allow", r)
			}

			// Non-six-class IxQuestion actively wakes main via sigAsk (never parked in keepRelayLater), enters askTick
			// reminders, and can be escalated to the user via ask_escalate.
			q := askQuestion("ix_q_nolead", "worker-2")
			qDone := e.raise(q)
			qn := e.noticeWith("conv1", "interaction_id="+q.ID)
			if !strings.Contains(qn, "signal=ask") || !strings.Contains(qn, "用哪个分支？") {
				t.Fatalf("main's question supervision notice lacks sigAsk:\n%s", qn)
			}
			if !e.lastShown(q.ID).Delegating {
				t.Fatal("routed question is not shown as delegating")
			}
			if hasRelayFrom(e.s, "conv1", cardAgentQuestion, "interaction_id="+q.ID) {
				t.Fatal("supervised question fell into handNonOwnerCard keepRelayLater queue")
			}
			if withBackground && len(e.f.noticesTo("bg1")) != 0 {
				t.Fatalf("background supervisor was woken for main-supervised question: %q", e.f.noticesTo("bg1"))
			}
			e.s.askTick(time.Now().Add(2 * askHoldFor))
			waitFor(t, "main received askTick reminder for question", func() bool {
				for _, n := range e.f.noticesTo("conv1") {
					if strings.Contains(n, "interaction_id="+q.ID) && strings.Contains(n, "再交给你处理") {
						return true
					}
				}
				return false
			})
			if _, err := runJSON(t, e.s, "conv1", "ask_escalate", map[string]any{
				"todo_id": e.todo.ID, "reason": "Need owner choice on branch", "recommend": "feat/x",
			}); err != nil {
				t.Fatalf("main could not escalate routed question without leaders: %v", err)
			}
			waitFor(t, "escalated question pushed to owner", func() bool { return e.pushesOf(q.ID) == 1 })
			if ix := e.lastShown(q.ID); ix.Delegating || ix.Delegate == nil || ix.Delegate.Reason != "Need owner choice on branch" {
				t.Fatalf("escalated question card = %+v %+v", ix, ix.Delegate)
			}
			if err := e.s.resolve(q.ID, proto.Resolution{Answers: map[string]string{"用哪个分支？": "feat/x"}}); err != nil {
				t.Fatal(err)
			}
			answerOf(t, qDone)

			e.settle()
			if n := e.pushesOf(perm.ID); n != 0 {
				t.Fatalf("answered supervised permission pushed %d times", n)
			}
			if n := e.pushesOf(q.ID); n != 1 {
				t.Fatalf("escalated supervised question pushed %d times, want 1", n)
			}
			if got := e.f.noticesTo("lead1"); len(got) != 0 {
				t.Fatalf("unconfigured leader received notices: %q", got)
			}

			// Six-class owner cards and supervising conversations' own cards still go directly to the user.
			now := time.Now().UTC()
			e.s.fleet.mu.Lock()
			e.s.fleet.clients.Elicitation = true
			e.s.fleet.mu.Unlock()
			confirmOut := proto.Interaction{
				ID: "ix_confirm_outbound", MachineID: "mac", SessionID: "worker-1", Kind: proto.IxPermission,
				ToolName: "agent_send", OwnerProposal: true, Summary: "Hand off sensitive details", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
			}
			e.s.cardAsks.Store(confirmOut.ID, cardConfirmOutbound)
			t.Cleanup(func() { e.s.cardAsks.Delete(confirmOut.ID) })

			allow := true
			ownerCards := []struct {
				name string
				ix   proto.Interaction
				res  proto.Resolution
			}{
				{
					name: "secret_permission",
					ix: proto.Interaction{
						ID: "ix_owner_secret", MachineID: "mac", SessionID: "worker-1", Kind: proto.IxPermission,
						ToolName: "Bash", Summary: "enter password", Secret: true, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
					},
					res: proto.Resolution{Allow: &allow},
				},
				{
					name: "form_permission",
					ix: proto.Interaction{
						ID: "ix_owner_form", MachineID: "mac", SessionID: "worker-1", Kind: proto.IxPermission,
						ToolName: "Bash", Fields: []proto.Field{{Key: "otp", Label: "OTP"}},
						CreatedAt: now, ExpiresAt: now.Add(time.Hour),
					},
					res: proto.Resolution{Allow: &allow, Values: map[string]string{"otp": "123456"}},
				},
				{
					name: "user_approval_delete",
					ix: proto.Interaction{
						ID: "ix_owner_delete", MachineID: "mac", SessionID: "worker-1", Kind: proto.IxPermission,
						ToolName: "Bash", Summary: "delete user records permanently",
						CreatedAt: now, ExpiresAt: now.Add(time.Hour),
					},
					res: proto.Resolution{Allow: &allow},
				},
				{
					name: "outward_write_gate",
					ix: proto.Interaction{
						ID: "ix_owner_outward", MachineID: "mac", SessionID: "worker-1", Kind: proto.IxPermission,
						ToolName: "Feishu", Summary: "Send external message", OutwardWrite: true,
						CreatedAt: now, ExpiresAt: now.Add(time.Hour),
					},
					res: proto.Resolution{Allow: &allow},
				},
				{
					name: "confirm_outbound",
					ix:   confirmOut,
					res:  proto.Resolution{Allow: &allow},
				},
				{
					name: "secret_question",
					ix: proto.Interaction{
						ID: "ix_owner_secret_q", MachineID: "mac", SessionID: "worker-1", Kind: proto.IxQuestion,
						Secret: true, Questions: []proto.Question{{Question: "API token?"}},
						CreatedAt: now, ExpiresAt: now.Add(time.Hour),
					},
					res: proto.Resolution{Answers: map[string]string{"API token?": "ok"}},
				},
				{
					name: "elicitation",
					ix: proto.Interaction{
						ID: "ix_owner_elicit", MachineID: "mac", SessionID: "worker-1", Kind: proto.IxElicitation,
						Summary: "Sign in form", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
					},
					res: proto.Resolution{Action: "decline"},
				},
				{
					name: "main_owner_only_permission",
					ix: proto.Interaction{
						ID: "ix_main_own", MachineID: "server", SessionID: "conv1", Kind: proto.IxPermission, OwnerProposal: true,
						ToolName: "Bash", Summary: "go test ./...", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
					},
					res: proto.Resolution{Allow: &allow},
				},
			}
			if withBackground {
				ownerCards = append(ownerCards, struct {
					name string
					ix   proto.Interaction
					res  proto.Resolution
				}{
					name: "background_owner_only_permission",
					ix: proto.Interaction{
						ID: "ix_bg_own", MachineID: "server", SessionID: "bg1", Kind: proto.IxPermission, OwnerProposal: true,
						ToolName: "Bash", Summary: "go test ./...", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
					},
					res: proto.Resolution{Allow: &allow},
				})
			}
			for _, tc := range ownerCards {
				done := e.raise(tc.ix)
				waitFor(t, tc.name+" pushed to owner", func() bool { return e.pushesOf(tc.ix.ID) == 1 })
				if e.lastShown(tc.ix.ID).Delegating {
					t.Errorf("%s was marked delegating", tc.name)
				}
				if err := e.s.resolve(tc.ix.ID, tc.res); err != nil {
					t.Fatal(err)
				}
				answerOf(t, done)
			}

			// Existing supervision state guards stay intact when leaders is empty.
			if _, _, err := e.s.updateSupervision(ctx, e.todo.ID, func(v *Supervision) bool { v.State = reviewNeedsUser; return true }); err != nil {
				t.Fatal(err)
			}
			if _, _, _, ok := e.s.askLadder(ctx, askPermission("ix_guard_needs_user", "worker-1")); ok {
				t.Error("askLadder routed permission in supervision state needs_user")
			}
			if _, _, _, ok := e.s.askLadder(ctx, askQuestion("ix_guard_q_needs_user", "worker-1")); ok {
				t.Error("askLadder routed question in supervision state needs_user")
			}
			for _, state := range []string{reviewAccepted, reviewCancelled} {
				closedAt := time.Now().UTC()
				e.s.todos.mu.Lock()
				if cur := e.s.todos.todos[e.todo.ID]; cur != nil {
					cur.DoneAt = &closedAt
					cur.Supervision.State = state
				}
				e.s.todos.mu.Unlock()
				if _, _, _, ok := e.s.askLadder(ctx, askPermission("ix_guard_"+state, "worker-1")); ok {
					t.Errorf("askLadder routed permission in supervision state %q", state)
				}
				if _, _, _, ok := e.s.askLadder(ctx, askQuestion("ix_guard_q_"+state, "worker-1")); ok {
					t.Errorf("askLadder routed question in supervision state %q", state)
				}
			}

			// agentSupervised workers remain unrouted for non-screen cards when leaders is empty.
			leaderTodo, err := e.s.createTodo(ctx, "Leader task", "", "conv1", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := e.s.superviseTodo(ctx, leaderTodo.ID, "conv1", nil, 0); err != nil {
				t.Fatal(err)
			}
			if err := e.s.linkTodoAgentWithSource(ctx, leaderTodo.ID, "mac", "task-leader-1", "Leader", "/code/app", "claude", "", sourceLeader, ""); err != nil {
				t.Fatal(err)
			}
			workerTodo, err := e.s.createTodo(ctx, "Worker task", "", leaderRef("mac", "task-leader-1"), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := e.s.superviseTodo(ctx, workerTodo.ID, leaderRef("mac", "task-leader-1"), nil, 0); err != nil {
				t.Fatal(err)
			}
			if err := e.s.linkTodoAgent(ctx, workerTodo.ID, "mac", "sub-worker-1", "Worker", "/code/app", "claude"); err != nil {
				t.Fatal(err)
			}
			if _, _, _, ok := e.s.askLadder(ctx, askPermission("ix_guard_agent_sup_p", "sub-worker-1")); ok {
				t.Error("askLadder routed permission for agent-supervised worker")
			}
			if _, _, _, ok := e.s.askLadder(ctx, askQuestion("ix_guard_agent_sup_q", "sub-worker-1")); ok {
				t.Error("askLadder routed question for agent-supervised worker")
			}
			e.settle()
		})
	}
}

// What a leader reads of the cards handed to it, and what the main conversation reads of the leader's escalation.
func TestGoldenSuperviseAsk(t *testing.T) {
	got := prompts.RenderSupervise(prompts.Supervise{Leader: true, Asked: true, AskMinutes: 5,
		Entries: []prompts.SuperviseEntry{{ID: "todo_1", Title: "修登录崩溃", Kind: sigAsk, DoneWhen: []string{"PR 合并到 main"},
			Machine: "box", Session: "a1b2c3d4", Last: "改好了，要推到远端",
			Asks: []prompts.SuperviseAsk{
				{ID: "ix_1", Kind: proto.IxQuestion, Machine: "box", Session: "a1b2c3d4", Text: "- 用哪个分支？（选项：main / feat/x）"},
				{ID: "ix_2", Kind: proto.IxPermission, Machine: "box", Session: "e5f6a7b8", Text: "允许运行 Bash：git push", Raised: true}},
			Decisions: "- 用哪个分支？→ feat/x（用户在卡片上选的）"}}})
	golden(t, "supervise.ask", got)
	for _, fact := range []string{"Question routing: timeout_minutes=5", "signal=ask", "interaction_id=ix_1", "用哪个分支？", "machine=box", "session_id=a1b2c3d4"} {
		if !strings.Contains(got, fact) {
			t.Errorf("supervision payload lacks fact %q:\n%s", fact, got)
		}
	}
	escalated := prompts.RenderSupervise(prompts.Supervise{Escalated: true,
		Entries: []prompts.SuperviseEntry{{ID: "todo_1", Title: "修登录崩溃", Kind: sigEscalated, Detail: "要推到远端分支，需要你同意",
			Recommend: "允许", Asks: []prompts.SuperviseAsk{{ID: "ix_2", Kind: proto.IxPermission, Machine: "box", Session: "e5f6a7b8",
				Text: "允许运行 Bash：git push"}}}}})
	golden(t, "supervise.ask_escalated", escalated)
	if !strings.Contains(escalated, "signal=escalated") || !strings.Contains(escalated, "要推到远端分支") {
		t.Errorf("escalated payload lacks task facts:\n%s", escalated)
	}
}

// With leaders configured, a quiet task of the main conversation's still has its card go to the main conversation
// first; past its time the card stays there, reminded: a quiet task's card is never pushed unless the main
// conversation escalates it.
func TestQuietTaskAskIsNotPushedOnTimeout(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	ctx := context.Background()
	w := proto.Session{SessionID: "worker-3", MachineID: "mac", Title: "整理材料", Cwd: "/code/doc", Status: proto.StatusBlocked, UpdatedAt: time.Now().UTC()}
	list := append(append([]proto.Session(nil), e.agents...), w)
	e.sessions("mac", list...)
	e.f.setSessions("mac", list...)
	quiet, err := e.s.createTodo(ctx, "整理材料", "", "conv1", "")
	if err != nil || !quiet.Quiet {
		t.Fatalf("not a quiet task: %+v %v", quiet, err)
	}
	e.s.superviseTodo(ctx, quiet.ID, "conv1", nil, 0)
	if err := e.s.linkTodoAgent(ctx, quiet.ID, "mac", w.SessionID, w.Title, w.Cwd, "claude"); err != nil {
		t.Fatal(err)
	}
	e.raise(askQuestion("ix1", "worker-3"))
	n := e.noticeWith("conv1", "interaction_id=ix1")
	if !strings.Contains(n, "signal=ask") {
		t.Fatalf("the main conversation's message:\n%s", n)
	}
	e.s.askTick(time.Now().Add(2 * askHoldFor))
	e.noticeWith("conv1", "再交给你处理")
	e.settle()
	if n := e.pushesOf("ix1"); n != 0 {
		t.Fatalf("a quiet task's card was pushed %d times", n)
	}
	if ix, ok := e.s.pendingIx("ix1"); !ok || !ix.Delegating {
		t.Fatalf("a quiet task's card left its supervisor: %+v %v", ix, ok)
	}
}

// A card the assistant is deciding on for the user (agents.delegate) is not the supervisor's to escalate: the
// delegation tells the user itself, once.
func TestEscalateLeavesADelegatedCard(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	on := true
	e.f.mu.Lock()
	e.f.hello.Agents.Delegate.Enabled = &on
	e.f.mu.Unlock()
	release := make(chan struct{})
	e.s.delegateHook = func(context.Context, DelegateParams) (DelegateResult, error) {
		<-release
		return DelegateResult{Ask: &proto.Delegate{Reason: "要你定"}}, nil
	}
	// Waiting for the user's decision already: its cards are not routed.
	if _, _, err := e.s.updateSupervision(context.Background(), e.todo.ID, func(v *Supervision) bool { v.State = reviewNeedsUser; return true }); err != nil {
		t.Fatal(err)
	}
	e.raise(askQuestion("ix1", "worker-1"))
	waitFor(t, "the delegation", func() bool { ix, ok := e.shown("ix1"); return ok && ix.Delegating })
	if _, err := runJSON(t, e.s, "lead1", "ask_escalate", map[string]any{"todo_id": e.todo.ID, "reason": "x"}); err == nil {
		t.Fatal("escalated a card the assistant is deciding on")
	}
	close(release)
	waitFor(t, "the push", func() bool { return e.pushesOf("ix1") == 1 })
	e.settle()
	if n := e.pushesOf("ix1"); n != 1 {
		t.Fatalf("pushed %d times", n)
	}
}
