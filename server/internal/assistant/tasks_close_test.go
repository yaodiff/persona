package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func doneTaskStartup(t *testing.T, done time.Time, row proto.Session, cards ...proto.Interaction) (*Service, *fakeHub) {
	t.Helper()
	s, f := newTaskService(t)
	putTodo(s, Todo{ID: "done-task", Origin: "conv1", DoneAt: &done, Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}})
	f.setSessions("box", row)
	for i, ix := range cards {
		ix.MachineID, ix.SessionID = "box", "s1"
		if ix.ID == "" {
			ix.ID = "card-" + ix.Kind
		}
		cards[i] = ix
	}
	s.fleet.mu.Lock()
	for _, ix := range cards {
		s.ixMapLocked()[ix.ID] = &pending{ix: ix}
	}
	s.fleet.mu.Unlock()
	return s, f
}

func startupBlockedAgent() proto.Session {
	row := idleAgent(time.Time{})
	row.Status, row.State, row.LastText = proto.StatusBlocked, proto.StateNeedsYou, ""
	return row
}

func taskCloseCalls(f *fakeHub) []DeviceCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var calls []DeviceCall
	for _, c := range f.calls {
		switch c.Method {
		case deviceSessionStop, deviceSessionRetire, "session.close":
			calls = append(calls, c)
		}
	}
	return calls
}

func TestDoneTaskStartupAgentIsStopped(t *testing.T) {
	ctx := t.Context()
	row := startupBlockedAgent()
	row.PendingInteractions = 1
	s, f := doneTaskStartup(t, time.Now(), row, proto.Interaction{Kind: proto.IxScreen})
	refused, seen := taskCloseRefused{}, doneSeen{}

	s.closeDoneTaskAgents(ctx, refused, seen)
	if calls, cards := taskCloseCalls(f), s.pendingInteractions(); len(calls) != 0 || len(cards) != 1 {
		t.Fatalf("before idle cutoff: calls %v, cards %v", calls, cards)
	}
	old := time.Now().Add(-taskCloseIdle - time.Minute)
	s.todos.mu.Lock()
	s.todos.todos["done-task"].DoneAt = &old
	s.todos.mu.Unlock()

	s.closeDoneTaskAgents(ctx, refused, seen)
	calls := taskCloseCalls(f)
	if len(calls) != 1 || calls[0].Method != deviceSessionStop || calls[0].Device != "box" {
		t.Fatalf("after idle cutoff: calls %v, want one session.stop on box", calls)
	}
	var args struct {
		Session string `json:"session_id"`
		Retire  bool   `json:"retire"`
	}
	if err := json.Unmarshal(calls[0].Params, &args); err != nil || args.Session != "s1" || args.Retire {
		t.Fatalf("stop args = %s, %v", calls[0].Params, err)
	}
	if cards := s.pendingInteractions(); len(cards) != 0 {
		t.Fatalf("startup screen still pending: %v", cards)
	}
	if w := seen[[2]string{"box", "s1"}]; !w.stopped || w.lastStop.IsZero() || !w.running.IsZero() {
		t.Fatalf("stop not recorded: %+v", w)
	}
	s.closeDoneTaskAgents(ctx, refused, seen)
	if len(taskCloseCalls(f)) != 1 {
		t.Fatal("stale blocked row triggered a second stop")
	}
}

func TestDoneTaskStartupCleanupLeavesActiveOrProtectedAgents(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*proto.Session)
		cards []proto.Interaction
		open  bool
	}{
		{name: "has run a turn", edit: func(c *proto.Session) { c.UpdatedAt = time.Now().Add(-time.Hour) }},
		{name: "queued", edit: func(c *proto.Session) { c.Queued = []string{"next"} }},
		{name: "background", edit: func(c *proto.Session) { c.Background = []proto.BackgroundTask{{}} }},
		{name: "loop", edit: func(c *proto.Session) { c.Loop = &proto.LoopInfo{} }},
		{name: "port", edit: func(c *proto.Session) { c.Ports = []proto.Port{{}} }},
		{name: "running subagent", edit: func(c *proto.Session) { c.Subagents = []proto.Subagent{{Running: true}} }},
		{name: "running workflow", edit: func(c *proto.Session) { c.Workflows = []proto.Workflow{{Running: true}} }},
		{name: "starting", edit: func(c *proto.Session) { c.Starting = true }},
		{name: "assistant", edit: func(c *proto.Session) { c.Assistant = true }},
		{name: "external", edit: func(c *proto.Session) { c.SourceKind = "runtime" }},
		{name: "hand started", edit: func(c *proto.Session) { c.StartedByPersona = false }},
		{name: "no pane", edit: func(c *proto.Session) { c.PaneID = "" }},
		{name: "open task", open: true},
		{name: "no card", cards: []proto.Interaction{}},
		{name: "permission", cards: []proto.Interaction{{Kind: proto.IxPermission}}},
		{name: "question", cards: []proto.Interaction{{Kind: proto.IxQuestion}}},
		{name: "plan alongside screen", cards: []proto.Interaction{{Kind: proto.IxScreen}, {Kind: proto.IxPlan}}},
		{name: "secret screen", cards: []proto.Interaction{{Kind: proto.IxScreen, Secret: true}}},
		{name: "private screen", cards: []proto.Interaction{{Kind: proto.IxScreen, Private: true}}},
		{name: "screen form", cards: []proto.Interaction{{Kind: proto.IxScreen, Fields: []proto.Field{{}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, cards := startupBlockedAgent(), tc.cards
			if tc.edit != nil {
				tc.edit(&row)
			}
			if cards == nil {
				cards = []proto.Interaction{{Kind: proto.IxScreen}}
			}
			row.PendingInteractions = len(cards)
			s, f := doneTaskStartup(t, time.Now().Add(-taskCloseIdle-time.Minute), row, cards...)
			if tc.open {
				putTodo(s, Todo{ID: "open-task", Origin: "conv1", Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}})
			}
			s.closeDoneTaskAgents(t.Context(), taskCloseRefused{}, doneSeen{})
			if calls, pending := taskCloseCalls(f), s.pendingInteractions(); len(calls) != 0 || len(pending) != len(cards) {
				t.Fatalf("protected agent: calls %v, pending %v", calls, pending)
			}
		})
	}
}

func TestDoneTaskStartupRefusalRetriesOnlyStop(t *testing.T) {
	s, f := doneTaskStartup(t, time.Now().Add(-taskCloseIdle-time.Minute), startupBlockedAgent(), proto.Interaction{Kind: proto.IxScreen})
	f.answerNode(deviceSessionStop, func(_ string, _ json.RawMessage) (any, error) {
		return nil, &rpc.Error{Code: "no_terminal", Message: "terminal unavailable"}
	})
	refused, seen := taskCloseRefused{}, doneSeen{}
	s.closeDoneTaskAgents(t.Context(), refused, seen)
	key := [2]string{"box", "s1"}
	if len(s.pendingInteractions()) != 1 || refused[key].at.IsZero() || seen[key].stopped {
		t.Fatal("refused stop cleared the card or lost the refusal")
	}
	s.closeDoneTaskAgents(t.Context(), refused, seen)
	methods := func() []string {
		var out []string
		for _, c := range taskCloseCalls(f) {
			out = append(out, c.Method)
		}
		return out
	}
	if got := methods(); !slices.Equal(got, []string{deviceSessionStop}) {
		t.Fatalf("calls during backoff = %v", got)
	}
	refused[key] = taskCloseRefusal{at: time.Now().Add(-taskCloseRetry - time.Minute)}
	f.answerNode(deviceSessionStop, func(_ string, _ json.RawMessage) (any, error) {
		return map[string]bool{"stopped": true}, nil
	})
	s.closeDoneTaskAgents(t.Context(), refused, seen)
	if got := methods(); !slices.Equal(got, []string{deviceSessionStop, deviceSessionStop}) {
		t.Fatalf("calls after backoff = %v", got)
	}
	if len(s.pendingInteractions()) != 0 || len(refused) != 0 || !seen[key].stopped {
		t.Fatal("successful stop did not end the screen or clear the refusal")
	}
}

func TestDoneTaskStartupStopKeepsNewPermissionCard(t *testing.T) {
	s, f := doneTaskStartup(t, time.Now().Add(-taskCloseIdle-time.Minute), startupBlockedAgent(), proto.Interaction{Kind: proto.IxScreen})
	f.answerNode(deviceSessionStop, func(_ string, _ json.RawMessage) (any, error) {
		s.fleet.mu.Lock()
		defer s.fleet.mu.Unlock()
		s.ixMapLocked()["permission"] = &pending{ix: proto.Interaction{ID: "permission", MachineID: "box", SessionID: "s1", Kind: proto.IxPermission}}
		return map[string]bool{"stopped": true}, nil
	})
	s.closeDoneTaskAgents(t.Context(), taskCloseRefused{}, doneSeen{})
	if got := s.pendingInteractions(); len(got) != 1 || got[0].ID != "permission" {
		t.Fatalf("pending after stop = %v, want only the new permission card", got)
	}
}

func TestDoneTaskStartupCleanupRechecksTaskOwnership(t *testing.T) {
	row := startupBlockedAgent()
	s, f := doneTaskStartup(t, time.Now().Add(-taskCloseIdle-time.Minute), row, proto.Interaction{Kind: proto.IxScreen})
	f.answerNode(DeviceSessionsList, func(_ string, _ json.RawMessage) (any, error) {
		putTodo(s, Todo{ID: "done-task", Origin: "conv1", Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}})
		return map[string]any{"sessions": []proto.Session{row}}, nil
	})
	s.closeDoneTaskAgents(t.Context(), taskCloseRefused{}, doneSeen{})
	if calls, cards := taskCloseCalls(f), s.pendingInteractions(); len(calls) != 0 || len(cards) != 1 {
		t.Fatalf("reopened during session lookup: calls %v, pending cards %v", calls, cards)
	}
}

// Closing the agents of finished tasks (tasks_close.go).

// idleAgent is s1 as a finished agent in a pane Persona started, last active at.
func idleAgent(at time.Time) proto.Session {
	return proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle, PaneID: "w1:p1",
		StartedByPersona: true, UpdatedAt: at, LastText: "做完了"}
}

func (f *fakeHub) retires() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.retired...)
}

// A done task's agent closes once it has been still long enough; one working, waiting for the user, running
// something in the background, or not running is left as it is.
func TestRetirable(t *testing.T) {
	now := time.Now()
	done := now.Add(-time.Hour)
	old := now.Add(-time.Hour)
	cases := []struct {
		name string
		edit func(*proto.Session)
		done time.Time
		want bool
	}{
		{"idle long enough", func(*proto.Session) {}, done, true},
		{"done, unread", func(s *proto.Session) { s.State = proto.StateUnread }, done, true},
		{"codex", func(s *proto.Session) { s.Agent = "codex" }, done, true},
		{"agy in a pane", func(s *proto.Session) { s.Agent = "agy" }, done, true},
		{"agy run by its runtime", func(s *proto.Session) { s.Agent, s.SourceKind = "agy", "runtime" }, done, false},
		{"cursor", func(s *proto.Session) { s.Agent = "cursor" }, done, false},
		{"task done just now", func(*proto.Session) {}, now.Add(-time.Second), false},
		{"active just now", func(s *proto.Session) { s.UpdatedAt = now.Add(-time.Second) }, done, false},
		{"working", func(s *proto.Session) { s.Status = proto.StatusWorking }, done, false},
		{"blocked", func(s *proto.Session) { s.Status = proto.StatusBlocked }, done, false},
		{"unknown status", func(s *proto.Session) { s.Status = proto.StatusUnknown }, done, false},
		{"needs you", func(s *proto.Session) { s.State = proto.StateNeedsYou }, done, false},
		{"pending question", func(s *proto.Session) { s.PendingInteractions = 1 }, done, false},
		{"queued", func(s *proto.Session) { s.Queued = []string{"next"} }, done, false},
		{"background shell", func(s *proto.Session) { s.Background = []proto.BackgroundTask{{}} }, done, false},
		{"subagent running", func(s *proto.Session) { s.Subagents = []proto.Subagent{{Running: true}} }, done, false},
		{"subagent finished", func(s *proto.Session) { s.Subagents = []proto.Subagent{{}} }, done, true},
		{"workflow running", func(s *proto.Session) { s.Workflows = []proto.Workflow{{TaskID: "w", Running: true}} }, done, false},
		{"workflow ended", func(s *proto.Session) { s.Workflows = []proto.Workflow{{TaskID: "w", Status: proto.WorkflowCompleted}} }, done, true},
		{"loop", func(s *proto.Session) { s.Loop = &proto.LoopInfo{} }, done, false},
		{"serving a port", func(s *proto.Session) { s.Ports = []proto.Port{{}} }, done, false},
		{"offline", func(s *proto.Session) { s.Status, s.PaneID = proto.StatusOffline, "" }, done, false},
		{"starting", func(s *proto.Session) { s.Starting = true }, done, false},
		{"assistant", func(s *proto.Session) { s.Assistant = true }, done, false},
		{"another tool's", func(s *proto.Session) { s.SourceKind = "codex_app_server" }, done, false},
		{"not started by the hub", func(s *proto.Session) { s.StartedByPersona = false }, done, false},
		{"a runtime's agent", func(s *proto.Session) { s.Agent = "dsh" }, done, false},
	}
	for _, c := range cases {
		s := idleAgent(old)
		c.edit(&s)
		if got := retirable(s, c.done, now); got != c.want {
			t.Errorf("%s: retirable = %v, want %v", c.name, got, c.want)
		}
	}
}

// A task found done: its agent, still for long enough, is stopped on its machine and keeps its terminal; the todo
// keeps it.
func TestDoneTaskAgentIsClosed(t *testing.T) {
	ctx := context.Background()
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	f.setSessions("box", idleAgent(time.Now().Add(-time.Hour)))
	refused, seen := taskCloseRefused{}, doneSeen{}

	s.closeDoneTaskAgents(ctx, refused, seen)
	if got := f.retires(); len(got) != 0 {
		t.Fatalf("closed the agent of an open task: %v", got)
	}
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "做完了", time.Time{}); err != nil || !ok {
		t.Fatalf("auto done: %v %v", ok, err)
	}
	s.closeDoneTaskAgents(ctx, refused, seen) // done just now: not yet
	if got := f.retires(); len(got) != 0 {
		t.Fatalf("closed at once: %v", got)
	}
	old := taskCloseIdle
	taskCloseIdle = 0
	t.Cleanup(func() { taskCloseIdle = old })
	s.closeDoneTaskAgents(ctx, refused, seen)
	if got := f.retires(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("retired %v", got)
	}
	f.mu.Lock()
	args := f.retireArgs[0]
	f.mu.Unlock()
	if args["method"] != nodeproto.StopIfIdle || args["retire"] != true { // its terminal stays (#4754)
		t.Fatalf("asked %v", args)
	}
	if a := todoNow(s, todo.ID).Agents; len(a) != 1 || a[0].SessionID != "s1" || a[0].ReleasedAt != nil {
		t.Fatalf("the todo lost its agent: %+v", a)
	}

	// Closed, it is offline: not asked again.
	c := idleAgent(time.Now().Add(-time.Hour))
	c.Status, c.PaneID = proto.StatusOffline, ""
	f.setSessions("box", c)
	s.closeDoneTaskAgents(ctx, refused, seen)
	if got := f.retires(); len(got) != 1 {
		t.Fatalf("asked again: %v", got)
	}
}

// A node that will not stop an agent (another tool owns it, or the node is too old to know session.stop) is not asked
// again every pass, only once the agent changed or a while later (the node may have been updated since).
func TestRefusedRetireIsNotRepeated(t *testing.T) {
	ctx := context.Background()
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	old := taskCloseIdle
	taskCloseIdle = 0
	t.Cleanup(func() { taskCloseIdle = old })
	f.mu.Lock()
	f.retireErr = &rpc.Error{Code: "not_retirable", Message: "this session belongs to another tool"}
	f.mu.Unlock()
	asked := 0
	count := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, c := range f.calls {
			if c.Method == nodeproto.StopIfIdle {
				n++
			}
		}
		return n
	}
	at := time.Now().Add(-time.Hour)
	f.setSessions("box", idleAgent(at))
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "做完了", time.Time{}); err != nil || !ok {
		t.Fatalf("auto done: %v %v", ok, err)
	}
	refused, seen := taskCloseRefused{}, doneSeen{}
	s.closeDoneTaskAgents(ctx, refused, seen)
	s.closeDoneTaskAgents(ctx, refused, seen)
	if asked = count(); asked != 1 {
		t.Fatalf("asked %d times", asked)
	}
	f.setSessions("box", idleAgent(at.Add(time.Minute))) // it did something since
	s.closeDoneTaskAgents(ctx, refused, seen)
	if asked = count(); asked != 2 {
		t.Fatalf("not asked again after a change: %d", asked)
	}

	// Its node is updated: the refusal it gave before does not stand for ever.
	f.mu.Lock()
	f.retireErr = nil
	f.mu.Unlock()
	s.closeDoneTaskAgents(ctx, refused, seen)
	if asked = count(); asked != 2 {
		t.Fatalf("asked again at once: %d", asked)
	}
	oldRetry := taskCloseRetry
	taskCloseRetry = 0
	t.Cleanup(func() { taskCloseRetry = oldRetry })
	s.closeDoneTaskAgents(ctx, refused, seen)
	if got := f.retires(); count() != 3 || len(got) != 1 {
		t.Fatalf("not asked again after a while: %d (%v closed)", count(), got)
	}
	if _, ok := refused[[2]string{"box", "s1"}]; ok {
		t.Fatal("the refusal outlived the close")
	}
}

// retiredAgent is s1 stopped, its terminal kept: offline, in no pane.
func retiredAgent(at time.Time) proto.Session {
	c := idleAgent(at)
	c.Status, c.PaneID = proto.StatusOffline, ""
	return c
}

// doneTaskAgentClosed is a task done with its agent s1 stopped by the pass, still listed as running (the next list has
// not caught up), and the pass's memory.
func doneTaskAgentClosed(t *testing.T) (*Service, *fakeHub, Todo, taskCloseRefused, doneSeen) {
	t.Helper()
	ctx := context.Background()
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	old := taskCloseIdle
	taskCloseIdle = 0
	t.Cleanup(func() { taskCloseIdle = old })
	f.setSessions("box", idleAgent(time.Now().Add(-time.Hour)))
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "做完了", time.Time{}); err != nil || !ok {
		t.Fatalf("auto done: %v %v", ok, err)
	}
	refused, seen := taskCloseRefused{}, doneSeen{}
	s.closeDoneTaskAgents(ctx, refused, seen)
	if got := f.retires(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("retired %v", got)
	}
	return s, f, todo, refused, seen
}

func TestResumedDoneTaskAgentRetiresWhenIdle(t *testing.T) {
	ctx := t.Context()
	s, f, _, refused, done := doneTaskAgentClosed(t)
	old := time.Now().Add(-24 * time.Hour)
	key := [2]string{"box", "s1"}
	f.setSessions("box", retiredAgent(old))
	s.closeDoneTaskAgents(ctx, refused, done)
	f.setSessions("box", idleAgent(old))
	s.closeDoneTaskAgents(ctx, refused, done)
	if !done[key].resumed {
		t.Fatal("the task pass did not recognize the resume")
	}
	seen := idleSeen{key: old}
	s.closeIdleAgents(ctx, refused, done, seen)
	if got := f.retires(); len(got) != 1 {
		t.Fatalf("stopped on the first idle pass after resuming: %v", got)
	}
	seen[key] = old
	w := done[key]
	w.running = old
	done[key] = w
	setTaskViewer(t, s, "box", "s1", true)
	s.closeIdleAgents(ctx, refused, done, seen)
	if got := f.retires(); len(got) != 1 {
		t.Fatalf("stopped while the owner was viewing: %v", got)
	}
	setTaskViewer(t, s, "box", "s1", false)
	s.closeIdleAgents(ctx, refused, done, seen)
	if got := f.retires(); len(got) != 1 {
		t.Fatalf("stopped immediately after the owner left: %v", got)
	}
	s.taskActivity.mu.Lock()
	s.taskActivity.times[runtimeListID("box", "s1")] = old
	s.taskActivity.mu.Unlock()
	s.closeIdleAgents(ctx, refused, done, seen)
	if got := f.retires(); len(got) != 2 {
		t.Fatalf("the resumed Agent never stopped after the idle window: %v", got)
	}
	f.mu.Lock()
	args := f.retireArgs[1]
	f.mu.Unlock()
	if args["method"] != nodeproto.StopIfIdle || args["stop_cause"] != string(proto.StopIdle) || args["close_terminal"] != nil {
		t.Fatalf("the idle pass did not use guarded stop with the terminal kept: %v", args)
	}
	s.closeDoneTaskAgents(ctx, refused, done)
	s.closeIdleAgents(ctx, refused, done, seen)
	if got := f.retires(); len(got) != 2 {
		t.Fatalf("stopped again before observing an offline row: %v", got)
	}
}

// The owner resumes a done task's stopped agent and types for a while (#10311): the resume adds no step, so its last
// step and the task's completion are both old, yet it is not stopped again. It runs until the idle rule stops it, or
// until its task, reopened, is done again: then it stops as before.
func TestResumedDoneTaskAgentIsLeftRunning(t *testing.T) {
	ctx := context.Background()
	s, f, todo, refused, seen := doneTaskAgentClosed(t)
	old := time.Now().Add(-time.Hour)
	f.setSessions("box", retiredAgent(old))
	s.closeDoneTaskAgents(ctx, refused, seen)

	f.setSessions("box", idleAgent(old)) // Resume: running again, its last step unchanged
	for range 3 {
		s.closeDoneTaskAgents(ctx, refused, seen)
	}
	if got := f.retires(); len(got) != 1 {
		t.Fatalf("stopped the resumed agent again: %v", got)
	}

	if err := s.reopenTodo(ctx, todo.ID); err != nil {
		t.Fatal(err)
	}
	s.closeDoneTaskAgents(ctx, refused, seen)
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "又做完了", time.Now()); err != nil || !ok {
		t.Fatalf("done again: %v %v", ok, err)
	}
	s.closeDoneTaskAgents(ctx, refused, seen)
	if got := f.retires(); len(got) != 2 {
		t.Fatalf("the agent of a task done again was not stopped: %v", got)
	}
}

// Resumed before any list showed it stopped: the pass stopped it itself, so running again is a resume all the same.
func TestResumeBeforeTheNextPassIsAResume(t *testing.T) {
	ctx := context.Background()
	s, f, _, refused, seen := doneTaskAgentClosed(t)
	for range 3 {
		s.closeDoneTaskAgents(ctx, refused, seen)
	}
	if got := f.retires(); len(got) != 1 {
		t.Fatalf("stopped the resumed agent again: %v", got)
	}
}

// A task done while its agent was still launching (#2954): the launch is no stop, so the agent that comes up is the
// task's, not a resumed one, and stops once still as before.
func TestLaunchingAgentOfDoneTaskIsNoResume(t *testing.T) {
	ctx := context.Background()
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	old := taskCloseIdle
	taskCloseIdle = 0
	t.Cleanup(func() { taskCloseIdle = old })
	at := time.Now().Add(-time.Hour)
	launching := idleAgent(at)
	launching.Starting, launching.PaneID = true, ""
	f.setSessions("box", launching)
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "做完了", time.Time{}); err != nil || !ok {
		t.Fatalf("auto done: %v %v", ok, err)
	}
	refused, seen := taskCloseRefused{}, doneSeen{}
	s.closeDoneTaskAgents(ctx, refused, seen)
	f.setSessions("box", idleAgent(at))
	s.closeDoneTaskAgents(ctx, refused, seen)
	if got := f.retires(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("the launched agent of a done task was not stopped: %v", got)
	}
}

// With nothing seen yet (a manager restart: the owner may have resumed the agent meanwhile), the agent's stillness
// counts from when the pass first saw it running, never from its old last step alone.
func TestFirstSightHoldsOffTheStop(t *testing.T) {
	ctx := context.Background()
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	at := time.Now().Add(-time.Hour)
	f.setSessions("box", idleAgent(at))
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "做完了", time.Time{}); err != nil || !ok {
		t.Fatalf("auto done: %v %v", ok, err)
	}
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].DoneAt = &at
	s.todos.mu.Unlock()
	refused, seen := taskCloseRefused{}, doneSeen{}
	s.closeDoneTaskAgents(ctx, refused, seen)
	if got := f.retires(); len(got) != 0 {
		t.Fatalf("stopped on first sight: %v", got)
	}
	old := taskCloseIdle
	taskCloseIdle = 0
	t.Cleanup(func() { taskCloseIdle = old })
	s.closeDoneTaskAgents(ctx, refused, seen)
	if got := f.retires(); len(got) != 1 {
		t.Fatalf("not stopped once still since first sight: %v", got)
	}
}
