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

// The owner's "close an Agent's terminal when its session ends" (#10355, agent_terminal_close.go).

func closeOnEnd(t *testing.T, s *Service, on bool) {
	t.Helper()
	setSettings(t, s, func(v *Settings) { v.CloseEndedTerminals = &on })
}

// retireCalls are the node calls that stop or close an agent, each with what it asked.
func retireCalls(f *fakeHub) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c.Method != deviceSessionStop && c.Method != nodeproto.StopIfIdle && c.Method != deviceSessionRetire && c.Method != "session.close" {
			continue
		}
		var a map[string]any
		_ = json.Unmarshal(c.Params, &a)
		out = append(out, c.Method+" "+closeArgs(a))
	}
	return out
}

func closeArgs(a map[string]any) string {
	var parts []string
	for _, k := range []string{"retire", "close_terminal", "after_exit", "stop_cause"} {
		if v, ok := a[k]; ok {
			parts = append(parts, k+"="+strings.Trim(strings.ReplaceAll(closeArgString(v), `"`, ""), " "))
		}
	}
	return strings.Join(parts, ",")
}

func closeArgString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestCloseEndedTerminalsIsOffByDefault(t *testing.T) {
	s, _ := newTaskService(t)
	if s.closeEndedTerminals(context.Background()) {
		t.Fatal("close-on-end is on by default")
	}
	if v := s.view(context.Background(), false); v.CloseEndedTerminals {
		t.Fatal("the view says on by default")
	}
}

// A done task's idle agent always uses the cancellable activity guard, including when terminal cleanup is on.
func TestDoneTaskAgentTerminalFollowsTheSetting(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[on], func(t *testing.T) {
			ctx := context.Background()
			s, f := newTaskService(t)
			closeOnEnd(t, s, on)
			todo := startedTask(t, s, f)
			f.setSessions("box", idleAgent(time.Now().Add(-time.Hour)))
			if ok, err := s.autoDoneTodo(ctx, todo.ID, "做完了", time.Time{}); err != nil || !ok {
				t.Fatalf("auto done: %v %v", ok, err)
			}
			old := taskCloseIdle
			taskCloseIdle = 0
			t.Cleanup(func() { taskCloseIdle = old })
			s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{})
			want := nodeproto.StopIfIdle + " retire=true,stop_cause=task_completed"
			if on {
				want = nodeproto.StopIfIdle + " retire=true,close_terminal=true,stop_cause=task_completed"
			}
			if got := retireCalls(f); len(got) != 1 || got[0] != want {
				t.Fatalf("calls %v, want %q", got, want)
			}
		})
	}
}

// A busy agent is never closed, whatever the setting: the done-task pass leaves it alone.
func TestDoneTaskBusyAgentIsNeverClosed(t *testing.T) {
	busy := map[string]func(*proto.Session){
		"working":  func(c *proto.Session) { c.Status = proto.StatusWorking },
		"waiting":  func(c *proto.Session) { c.PendingInteractions = 1; c.State = proto.StateNeedsYou },
		"queued":   func(c *proto.Session) { c.Queued = []string{"next"} },
		"workflow": func(c *proto.Session) { c.Workflows = []proto.Workflow{{TaskID: "w", Running: true}} },
		"subagent": func(c *proto.Session) { c.Subagents = []proto.Subagent{{Running: true}} },
		"loop":     func(c *proto.Session) { c.Loop = &proto.LoopInfo{} },
		"port":     func(c *proto.Session) { c.Ports = []proto.Port{{}} },
		"bg shell": func(c *proto.Session) { c.Background = []proto.BackgroundTask{{}} },
	}
	for name, edit := range busy {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, f := newTaskService(t)
			closeOnEnd(t, s, true)
			todo := startedTask(t, s, f)
			c := idleAgent(time.Now().Add(-time.Hour))
			edit(&c)
			f.setSessions("box", c)
			if ok, err := s.autoDoneTodo(ctx, todo.ID, "做完了", time.Time{}); err != nil || !ok {
				t.Fatalf("auto done: %v %v", ok, err)
			}
			old := taskCloseIdle
			taskCloseIdle = 0
			t.Cleanup(func() { taskCloseIdle = old })
			s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{})
			if got := retireCalls(f); len(got) != 0 {
				t.Fatalf("a busy agent was asked: %v", got)
			}
		})
	}
}

// An agent Persona started, stopped for being idle, ends like a done task's: with the setting on its terminal is
// closed too; off, it is only stopped and keeps it.
func TestIdleStopClosesTheTerminalWithTheSetting(t *testing.T) {
	for _, on := range []bool{false, true} {
		ctx := context.Background()
		s, f := newTaskService(t)
		closeOnEnd(t, s, on)
		c := idleAgent(time.Now().Add(-48 * time.Hour))
		f.setSessions("box", c)
		s.retireAgent(ctx, taskCloseRefused{}, "idle", "box", c, time.Now())
		got := retireCalls(f)
		want := nodeproto.StopIfIdle + " retire=true,stop_cause=idle"
		if on {
			want = nodeproto.StopIfIdle + " retire=true,close_terminal=true,stop_cause=idle"
		}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("on=%v: idle stop asked %v, want %q", on, got, want)
		}
	}
}

// Cancellation never stops an Agent immediately. After the inactivity grace, the same guarded pass honors the
// terminal setting; working Agents and background work are still left alone. A loop running when the task is
// cancelled refuses the cancellation itself (taskCloseWorkSnapshot, #15416), so the loop here starts after it, as
// the owner's follow-up in the conversation the cancelled task leaves usable would.
func TestCancelledTaskAgentTerminal(t *testing.T) {
	for _, tc := range []struct {
		name        string
		on          bool
		edit, after func(*proto.Session)
		want        string
	}{
		{"off", false, nil, nil, nodeproto.StopIfIdle + " retire=true,stop_cause=task_completed"},
		{"on idle", true, nil, nil, nodeproto.StopIfIdle + " retire=true,close_terminal=true,stop_cause=task_completed"},
		{"on working", true, func(c *proto.Session) { c.Status, c.State = proto.StatusWorking, proto.StateWorking }, nil, ""},
		{"on loop", true, nil, func(c *proto.Session) { c.Loop = &proto.LoopInfo{} }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft, row := closeTestService(t, tc.edit)
			closeOnEnd(t, ft.s, tc.on)
			s, ctx := ft.s, context.Background()
			todo, err := s.createTodo(ctx, "做", "做", "conv1", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.linkTodoAgent(ctx, todo.ID, "mac", row.SessionID, "x", "/code", "claude"); err != nil {
				t.Fatal(err)
			}
			if ok, err := s.reviewDoneTodo(ctx, todo.ID, "Cancelled", false); err != nil || !ok {
				t.Fatalf("cancel: %v %v", ok, err)
			}
			if tc.after != nil {
				tc.after(&row)
				ft.d.setSessions("mac", []proto.Session{row})
			}
			if got := retireCalls(ft.f); len(got) != 0 {
				t.Fatalf("cancellation stopped an Agent immediately: %v", got)
			}
			s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{})
			if got := retireCalls(ft.f); len(got) != 0 {
				t.Fatalf("cancellation skipped the inactivity grace: %v", got)
			}
			old := time.Now().Add(-time.Hour)
			s.todos.mu.Lock()
			s.todos.todos[todo.ID].DoneAt = &old
			s.todos.mu.Unlock()
			s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{{"mac", row.SessionID}: {done: old, running: old}})
			got := retireCalls(ft.f)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("busy cancelled Agent was stopped: %v", got)
				}
			} else if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("calls %v, want %q", got, tc.want)
			}
		})
	}
}

// An agent that quit by itself (cause unknown) or was told to (/exit) has the shell it left closed once the manager
// saw it running and then stopped; one the user stopped, one stopped for being idle, one never seen running, one
// Persona did not start and everything with the setting off keep their terminals.
func TestEndedAgentTerminalIsClosed(t *testing.T) {
	stopped := func(c proto.Session, cause proto.StopCause) proto.Session {
		c.Status, c.State, c.PaneID = proto.StatusOffline, proto.StateOffline, ""
		c.Capabilities = &proto.SessionCapabilities{Read: true, Terminal: true, StopCause: cause}
		return c
	}
	for _, tc := range []struct {
		name     string
		on       bool
		seen     bool
		cause    proto.StopCause
		edit     func(*proto.Session)
		closes   bool
		listFail bool
	}{
		{name: "exited by itself", on: true, seen: true, cause: proto.StopUnknown, closes: true},
		{name: "/exit", on: true, seen: true, cause: proto.StopExit, closes: true},
		{name: "off", on: false, seen: true, cause: proto.StopExit},
		{name: "user stop", on: true, seen: true, cause: proto.StopUser},
		{name: "idle stop", on: true, seen: true, cause: proto.StopIdle},
		{name: "restart", on: true, seen: true, cause: proto.StopRestart},
		{name: "never seen running", on: true, seen: false, cause: proto.StopExit},
		{name: "not Persona's", on: true, seen: true, cause: proto.StopUnknown, edit: func(c *proto.Session) { c.StartedByPersona = false }},
		{name: "interrupted", on: true, seen: true, cause: proto.StopUnknown, edit: func(c *proto.Session) { c.Status = proto.StatusInterrupted }},
		{name: "waiting card", on: true, seen: true, cause: proto.StopUnknown, edit: func(c *proto.Session) { c.PendingInteractions = 1 }},
		{name: "queued", on: true, seen: true, cause: proto.StopUnknown, edit: func(c *proto.Session) { c.Queued = []string{"next"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, f := newTaskService(t)
			closeOnEnd(t, s, tc.on)
			seen := endedSeen{}
			running := idleAgent(time.Now())
			if tc.seen {
				f.setSessions("box", running)
				s.closeEndedAgentTerminals(ctx, seen)
			}
			if got := retireCalls(f); len(got) != 0 {
				t.Fatalf("a running agent was asked: %v", got)
			}
			c := stopped(running, tc.cause)
			if tc.edit != nil {
				tc.edit(&c)
			}
			f.setSessions("box", c)
			s.closeEndedAgentTerminals(ctx, seen)
			got := retireCalls(f)
			if !tc.closes {
				if len(got) != 0 {
					t.Fatalf("closed: %v", got)
				}
				return
			}
			if len(got) != 1 || got[0] != "session.retire close_terminal=true,after_exit=true" {
				t.Fatalf("calls %v", got)
			}
			s.closeEndedAgentTerminals(ctx, seen) // once per end
			if got := retireCalls(f); len(got) != 1 {
				t.Fatalf("asked again: %v", got)
			}
		})
	}
}

// A node refusing the after-exit close (something else runs in the shell) is logged and not asked again for that end.
func TestEndedAgentRefusalIsNotRepeated(t *testing.T) {
	ctx := context.Background()
	s, f := newTaskService(t)
	closeOnEnd(t, s, true)
	f.answerNode(deviceSessionRetire, func(_ string, _ json.RawMessage) (any, error) {
		return nil, &rpc.Error{Code: "agent_busy", Message: "the stopped terminal changed or is in use"}
	})
	seen := endedSeen{}
	running := idleAgent(time.Now())
	f.setSessions("box", running)
	s.closeEndedAgentTerminals(ctx, seen)
	c := running
	c.Status, c.PaneID = proto.StatusOffline, ""
	c.Capabilities = &proto.SessionCapabilities{Read: true, Terminal: true}
	f.setSessions("box", c)
	s.closeEndedAgentTerminals(ctx, seen)
	s.closeEndedAgentTerminals(ctx, seen)
	if got := retireCalls(f); len(got) != 1 {
		t.Fatalf("calls %v", got)
	}
}

// The apps switch it with PUT /api/v1/assistant and read it back from the view (GET).
func TestCloseEndedTerminalsHTTP(t *testing.T) {
	s, _ := newTaskService(t)
	for _, on := range []bool{true, false} {
		w, view := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", "", "full", `{"close_ended_terminals":`+closeArgString(on)+`}`)
		if w.Code != 200 || view.CloseEndedTerminals != on || s.closeEndedTerminals(context.Background()) != on {
			t.Fatalf("PUT %v: %d %v", on, w.Code, view.CloseEndedTerminals)
		}
	}
}

func TestOpenTaskKeepsTerminalAfterUnexplainedStop(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	closeOnEnd(t, s, true)
	a := todo.Agents[0]
	run := idleAgent(time.Now())
	run.SessionID = a.SessionID
	seen := endedSeen{[2]string{a.MachineID, a.SessionID}: true}
	run.PaneID, run.Status, run.State = "", proto.StatusOffline, proto.StateUnknown
	run.StartedByPersona = true
	run.Capabilities = &proto.SessionCapabilities{Read: true, Terminal: true}
	f.setSessions(a.MachineID, run)
	s.closeEndedAgentTerminals(t.Context(), seen)
	if got := retireCalls(f); len(got) != 0 {
		t.Fatalf("closed the open task's terminal: %v", got)
	}
}

func TestTerminalResumeOptionTargetsOnlyItsBoundAgent(t *testing.T) {
	task := Todo{Agents: []TodoAgent{{MachineID: "box", SessionID: "00000001-example"}}}
	asked := time.Now()
	label := "Resume 00000001 on its terminal page"
	if target := decisionTerminal(task, label, asked); target == nil || target.MachineID != "box" {
		t.Fatal(target)
	}
	if decisionTerminal(task, "Resume 00000002 on its terminal page", asked) != nil {
		t.Fatal("unrelated terminal")
	}
	if terminalResumeLabel("Start a replacement", nil) {
		t.Fatal("ordinary decision classified as a terminal action")
	}
	now := time.Now()
	task.Agents[0].ReleasedAt = &now
	if decisionTerminal(task, label, asked) != nil {
		t.Fatal("released Agent")
	}
	task.Agents = append(task.Agents, TodoAgent{MachineID: "box", SessionID: "00000001-other", LinkedAt: asked.Add(time.Minute)})
	if decisionTerminal(task, label, asked) != nil {
		t.Fatal("matching prefix from a later Agent binding")
	}
}

func TestNeedsUserRejectsTerminalControlAsDecision(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "The owner can choose the next step.")
	args := rolloutDecision()
	const manual = "Resume the Agent on its terminal page"
	args["recommend"] = manual
	args["options"] = []DecisionOption{{Label: manual, LeadsTo: "Open the terminal"}, {Label: "Replace", LeadsTo: "Start a new Agent"}}
	got, err := reviewErr(s, todo.ID, args)
	if err == nil || !strings.Contains(err.Error(), "terminal page") {
		t.Fatalf("unexecutable decision accepted: %s, %v", got, err)
	}
}
