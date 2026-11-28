package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func externalRuntimeAgent() proto.Session {
	c := boxAgent("runtime-external")
	c.SourceKind = proto.SourceRuntime
	c.Capabilities = &proto.SessionCapabilities{Read: true, Send: true, Interrupt: true}
	c.Status, c.State = proto.StatusWorking, proto.StateWorking
	return c
}

func TestExternalRuntimeSessionControl(t *testing.T) {
	for _, assignment := range []struct {
		name, origin, supervisor string
		persona, closed, allowed bool
	}{
		{name: "unassigned"},
		{name: "other conversation", origin: "conv2", supervisor: "conv2"},
		{name: "closed", origin: "conv1", supervisor: "conv1", closed: true},
		{name: "Persona started", persona: true, allowed: true},
		{name: "origin", origin: "conv1", allowed: true},
		{name: "supervisor", origin: "conv2", supervisor: "conv1", allowed: true},
	} {
		t.Run(assignment.name, func(t *testing.T) {
			for _, tool := range []string{"agent_send", "agent_interrupt", "agent_supervise"} {
				t.Run(tool, func(t *testing.T) {
					s, f := newQuietService(t)
					c := externalRuntimeAgent()
					c.StartedByPersona = assignment.persona
					f.setSessions("box", c)
					if assignment.origin != "" {
						task := onBoxTask(t, s, c.SessionID, assignment.origin, assignment.closed)
						if assignment.supervisor != "" && !assignment.closed {
							if err := s.superviseTodo(t.Context(), task.ID, assignment.supervisor, []string{"Verified"}, 30); err != nil {
								t.Fatal(err)
							}
						}
					}
					before := taskLedger(t, s, "conv1", "conv2")
					result, err := runJSON(t, s, "conv1", tool, map[string]any{
						"machine": "box", "session_id": c.SessionID, "text": "Continue", "then": "Continue",
					})
					if assignment.allowed {
						if err != nil {
							t.Fatalf("%s refused an authorized session: %v", tool, err)
						}
						switch tool {
						case "agent_send":
							if len(sentToAgent(f)) != 1 {
								t.Fatalf("message was not delivered: %s", result)
							}
						case "agent_interrupt":
							if len(f.interrupted()) != 1 || len(sentToAgent(f)) != 1 {
								t.Fatalf("interruption or redirect was not delivered: %s", result)
							}
						case "agent_supervise":
							task, ok := s.openTodoOfAgent("box", c.SessionID)
							if !ok || supervisorOf(task) != "conv1" {
								t.Fatalf("task was not supervised: %s, %+v", result, task)
							}
						}
						return
					}
					if err == nil {
						t.Fatalf("%s accepted an unassigned external session: %s", tool, result)
					}
					for _, part := range []string{agentLabel(c), "box", c.SourceKind, "not started by Persona", "open task", "agent_read"} {
						if !strings.Contains(err.Error(), part) {
							t.Errorf("refusal omitted %q: %v", part, err)
						}
					}
					if len(sentToAgent(f)) != 0 || len(f.interrupted()) != 0 || len(startCalls(f)) != 0 ||
						len(s.pendingQuestions()) != 0 || before != taskLedger(t, s, "conv1", "conv2") {
						t.Fatal("refused control had side effects")
					}
				})
			}
		})
	}
}

func TestExternalRuntimeSessionControlFailsClosedOnTodoReadError(t *testing.T) {
	st := &failTodoLoadStore{profileTaskStore: &profileTaskStore{
		memStore: &memStore{vals: map[string][]byte{}}, plans: map[string]db.TaskPlanRow{},
	}}
	s, f := newQuietServiceOn(t, st)
	c := externalRuntimeAgent()
	f.setSessions("box", c)
	for _, tool := range []string{"agent_send", "agent_interrupt", "agent_supervise"} {
		t.Run(tool, func(t *testing.T) {
			if _, err := runJSON(t, s, "conv1", tool, map[string]any{
				"machine": "box", "session_id": c.SessionID, "text": "Continue",
			}); err == nil || !strings.Contains(err.Error(), "the todo book is unavailable") {
				t.Fatalf("unavailable task inventory: %v", err)
			}
		})
	}
	if len(sentToAgent(f)) != 0 || len(f.interrupted()) != 0 || taskCount(s) != 0 {
		t.Fatal("control went through without access to task assignments")
	}
}

func TestExternalRuntimeSessionControlRechecksAssignmentAfterApproval(t *testing.T) {
	for _, tool := range []string{"agent_send", "agent_interrupt"} {
		t.Run(tool, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			const self = "sleep-000-0000"
			f := healthHub()
			c := externalRuntimeAgent()
			c.MachineID, c.Cwd, c.Space = "server", "/home/me/.persona/health/work", SpaceHealth
			f.sessions["server"] = append(f.sessions["server"], c)
			s := newTestService(t, f)
			setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
			task, err := s.createTodo(ctx, "Analyze a sleep report", "", self, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.linkTodoAgent(ctx, task.ID, c.MachineID, c.SessionID, c.Title, c.Cwd, c.Agent); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]string{"machine": c.MachineID, "session_id": c.SessionID, "text": "Continue", "then": "Continue"})
			finished := make(chan error, 1)
			go func() {
				_, err := s.runTool(ctx, c.MachineID, self, tool, raw)
				finished <- err
			}()
			waitFor(t, "the outbound approval", func() bool { return len(s.pendingQuestions()) == 1 })
			card := s.pendingQuestions()[0]
			if err := s.failTodo(ctx, task.ID, "closed while approval was pending"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: card.ID, Answer: json.RawMessage(`{"allow":true}`)}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-finished:
				if err == nil || !strings.Contains(err.Error(), "not assigned to an open task") {
					t.Fatalf("control after the assignment ended: %v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if len(sentToAgent(f)) != 0 || len(f.interrupted()) != 0 {
				t.Fatal("approval bypassed the current task assignment")
			}
		})
	}
}

func TestExternalRuntimeSessionInterruptRechecksAssignmentBeforeRedirect(t *testing.T) {
	s, f := newQuietService(t)
	c := externalRuntimeAgent()
	f.setSessions("box", c)
	task := onBoxTask(t, s, c.SessionID, "conv1", false)
	requested := make(chan struct{})
	f.answerNode("session.interrupt", func(string, json.RawMessage) (any, error) {
		close(requested)
		return map[string]bool{"settled": false}, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]string{"machine": "box", "session_id": c.SessionID, "then": "Continue"})
	finished := make(chan error, 1)
	go func() {
		_, err := s.runTool(ctx, "server", "conv1", "agent_interrupt", raw)
		finished <- err
	}()
	select {
	case <-requested:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := s.failTodo(ctx, task.ID, "closed while the interruption was settling"); err != nil {
		t.Fatal(err)
	}
	c.Status, c.State = proto.StatusIdle, proto.StateIdle
	f.setSessions("box", c)
	select {
	case err := <-finished:
		if err == nil || !strings.Contains(err.Error(), "stop was requested") || !strings.Contains(err.Error(), "not assigned to an open task") {
			t.Fatalf("redirect after the assignment ended: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if len(sentToAgent(f)) != 0 {
		t.Fatal("redirect reached an external session after its assignment ended")
	}
}

func TestExternalRuntimeSessionControlWithLegacyBackgroundAssignment(t *testing.T) {
	s, f := newQuietService(t)
	s.noBackground = false
	c := externalRuntimeAgent()
	f.setSessions("box", c)
	task := onBoxTask(t, s, c.SessionID, "conv1", false, "Verified")
	const bg = "bg1"
	f.mu.Lock()
	f.known[bg] = true
	f.mu.Unlock()
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: bg, MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = bg, "server" })
	args := map[string]any{"machine": "box", "session_id": c.SessionID, "text": "Continue"}
	if _, err := runJSON(t, s, bg, "agent_send", args); err == nil || !strings.Contains(err.Error(), "not assigned to an open task") {
		t.Fatalf("expected refusal without a task assignment: %v", err)
	}
	tellLegacyBackground(t, s, f, task.ID, bg)
	if result, err := runJSON(t, s, bg, "agent_send", args); err != nil || len(sentToAgent(f)) != 1 {
		t.Fatalf("legacy supervisor could not send: %s, %v", result, err)
	}
}

func TestExternalRuntimeSessionRemainsReadable(t *testing.T) {
	s, f := newQuietService(t)
	c := externalRuntimeAgent()
	f.setSessions("box", c)
	f.answerNode(DeviceSessionItems, func(string, json.RawMessage) (any, error) {
		return map[string]any{"items": []proto.Item{{Seq: 1, Role: "assistant", Kind: proto.KindText, Text: "Visible transcript"}}}, nil
	})
	for _, call := range []struct{ tool, want string }{{"agent_read", "Visible transcript"}, {"agents_list", c.SessionID}} {
		result, err := runJSON(t, s, "conv1", call.tool, map[string]any{"machine": "box", "session_id": c.SessionID})
		if err != nil || !strings.Contains(result, call.want) {
			t.Errorf("%s: %s (%v)", call.tool, result, err)
		}
	}
}
