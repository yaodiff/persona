package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// releaseBoxLink marks sid's link on todo released and drops it from the active index, the in-memory effect of
// releaseStoppedAgents after the agent stopped.
func releaseBoxLink(t *testing.T, s *Service, todoID, sid string) {
	t.Helper()
	at := time.Now().UTC()
	s.todos.mu.Lock()
	defer s.todos.mu.Unlock()
	todo, ok := s.todos.todos[todoID]
	if !ok {
		t.Fatalf("no todo %s", todoID)
	}
	found := false
	for i := range todo.Agents {
		if todo.Agents[i].MachineID == "box" && todo.Agents[i].SessionID == sid {
			todo.Agents[i].ReleasedAt = &at
			found = true
		}
	}
	if !found {
		t.Fatalf("todo %s has no agent box/%s", todoID, sid)
	}
	delete(s.todos.activeBySession, [2]string{"box", sid})
}

// A stopped agent the conversation still has on an open, now unassigned to-do is resumed with todo_id back onto that
// same to-do: the historical link is reactivated, no new to-do is made, and the node's resume call goes out (#13192).
func TestResumeStoppedAgentOntoItsOpenTodo(t *testing.T) {
	s, f := newQuietService(t)
	row := stoppedBoxAgent("back0001")
	f.setSessions("box", row)
	resumingNode(f)
	todo := onBoxTask(t, s, "back0001", "conv1", false, "the document gains the new section")
	releaseBoxLink(t, s, todo.ID, "back0001")
	if _, ok := s.openTodoOfAgent("box", "back0001"); ok {
		t.Fatal("the released agent must read as on no open task before the resume")
	}
	res, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "back0001",
		"text": "Add the missing section to the same document", "resume": true,
		"resume_why": "it holds the document token and structure", "todo_id": todo.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(startCalls(f)) != 1 || !strings.Contains(res, "Resumed back0001 on box") || !strings.Contains(res, todo.ID) {
		t.Fatalf("resume receipt = %q, starts = %d", res, len(startCalls(f)))
	}
	got, ok := s.openTodoOfAgent("box", "back0001")
	if !ok || got.ID != todo.ID {
		t.Fatalf("agent not back on its to-do: %+v ok=%v", got, ok)
	}
	now, _ := s.todoNow(todo.ID)
	if now.DoneAt != nil || len(now.Agents) != 1 || now.Agents[0].ReleasedAt != nil || now.Agents[0].Via != "resume" {
		t.Fatalf("the to-do/link was not reactivated in place: %+v", now)
	}
}

// The resume-onto-todo guard refuses every case that must not re-link, before any node call (#13192).
func TestResumeOntoTodoRefusesOutsideTheBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, s *Service, f *fakeHub) (todoID string, row proto.Session)
		caller string
		want   string
	}{
		{
			name: "another conversation's to-do",
			setup: func(t *testing.T, s *Service, f *fakeHub) (string, proto.Session) {
				row := stoppedBoxAgent("else0001")
				f.setSessions("box", row)
				todo := onBoxTask(t, s, "else0001", "conv2", false, "done")
				releaseBoxLink(t, s, todo.ID, "else0001")
				return todo.ID, row
			},
			caller: "conv1", want: "not opened from this conversation",
		},
		{
			name: "to-do with an active agent",
			setup: func(t *testing.T, s *Service, f *fakeHub) (string, proto.Session) {
				row := stoppedBoxAgent("busy0002")
				f.setSessions("box", row, boxAgent("busy0001"))
				todo := onBoxTask(t, s, "busy0002", "conv1", false, "done")
				releaseBoxLink(t, s, todo.ID, "busy0002")
				if err := s.linkTodoAgent(t.Context(), todo.ID, "box", "busy0001", "active", "/code/busy0002", "claude"); err != nil {
					t.Fatal(err)
				}
				return todo.ID, row
			},
			caller: "conv1", want: "an agent is already working on it",
		},
		{
			name: "session is not the to-do's latest released agent",
			setup: func(t *testing.T, s *Service, f *fakeHub) (string, proto.Session) {
				row := stoppedBoxAgent("stray001")
				owner := stoppedBoxAgent("owner001")
				f.setSessions("box", row, owner)
				todo := onBoxTask(t, s, "owner001", "conv1", false, "done")
				releaseBoxLink(t, s, todo.ID, "owner001")
				return todo.ID, row
			},
			caller: "conv1", want: "not the to-do's latest released Agent",
		},
		{
			name: "done to-do",
			setup: func(t *testing.T, s *Service, f *fakeHub) (string, proto.Session) {
				row := stoppedBoxAgent("done0002")
				f.setSessions("box", row)
				todo := onBoxTask(t, s, "done0002", "conv1", false, "done")
				if err := s.failTodo(t.Context(), todo.ID, "finished"); err != nil {
					t.Fatal(err)
				}
				releaseBoxLink(t, s, todo.ID, "done0002")
				return todo.ID, row
			},
			caller: "conv1", want: "already done",
		},
		{
			name: "agent waiting for the user",
			setup: func(t *testing.T, s *Service, f *fakeHub) (string, proto.Session) {
				row := stoppedBoxAgent("wait0001")
				row.State, row.PendingInteractions = proto.StateNeedsYou, 1
				f.setSessions("box", row)
				todo := onBoxTask(t, s, "wait0001", "conv1", false, "done")
				releaseBoxLink(t, s, todo.ID, "wait0001")
				return todo.ID, row
			},
			caller: "conv1", want: "waiting for the user",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newQuietService(t)
			todoID, row := tc.setup(t, s, f)
			_, err := runJSON(t, s, tc.caller, "agent_send", map[string]any{"machine": "box", "session_id": row.SessionID,
				"text": "continue", "resume": true, "resume_why": "context", "todo_id": todoID})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want contains %q", err, tc.want)
			}
			if starts := startCalls(f); len(starts) != 0 {
				t.Fatalf("nothing may start on a refusal, got %d", len(starts))
			}
			if _, ok := s.openTodoOfAgent("box", row.SessionID); ok {
				t.Fatal("a refused resume must leave the agent unlinked")
			}
		})
	}
}

// todo_id without resume, on a running agent, or together with task is an arguments error, not a resume.
func TestResumeTodoIDArgumentGuards(t *testing.T) {
	s, f := newQuietService(t)
	stopped := stoppedBoxAgent("arg0001")
	running := boxAgent("arg0002")
	f.setSessions("box", stopped, running)
	todo := onBoxTask(t, s, "arg0001", "conv1", false, "done")
	releaseBoxLink(t, s, todo.ID, "arg0001")
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"todo_id without resume", map[string]any{"machine": "box", "session_id": "arg0001", "text": "go", "todo_id": todo.ID}, "only works with resume"},
		{"todo_id on a running agent", map[string]any{"machine": "box", "session_id": "arg0002", "text": "go", "resume": true, "resume_why": "x", "todo_id": todo.ID}, "for a stopped agent"},
		{"todo_id together with task", map[string]any{"machine": "box", "session_id": "arg0001", "text": "go", "resume": true, "resume_why": "x", "todo_id": todo.ID, "task": map[string]any{"title": "Other"}}, "alternatives"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runJSON(t, s, "conv1", "agent_send", tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// A re-link that passes resumeTodoForAgent but fails the next resume guard (a user stop only the owner may resume) is
// rolled back: the to-do is unassigned again, no node start goes out (#13192).
func TestResumeOntoTodoRollsBackWhenTheResumeIsRefused(t *testing.T) {
	s, f := newQuietService(t)
	row := stoppedBoxAgent("userstop1")
	row.Capabilities.StopCause = proto.StopUser
	f.setSessions("box", row)
	todo := onBoxTask(t, s, "userstop1", "conv1", false, "done")
	releaseBoxLink(t, s, todo.ID, "userstop1")
	_, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "userstop1",
		"text": "continue", "resume": true, "resume_why": "context", "todo_id": todo.ID})
	if err == nil || !strings.Contains(err.Error(), "user's Resume") {
		t.Fatalf("err = %v, want the owner-only stop refusal", err)
	}
	if _, ok := s.openTodoOfAgent("box", "userstop1"); ok {
		t.Fatal("the re-link must be rolled back after the resume guard refused")
	}
	now, _ := s.todoNow(todo.ID)
	if len(now.Agents) != 1 || now.Agents[0].ReleasedAt == nil {
		t.Fatalf("the historical link must stay released: %+v", now.Agents)
	}
	if starts := startCalls(f); len(starts) != 0 {
		t.Fatalf("nothing may start on a refusal, got %d", len(starts))
	}
}

// When the stopped session is still the active Agent on another open to-do, resuming it onto an older released to-do
// must refuse before any re-link: the upsert would otherwise release the other to-do's active link, and a failed start
// could not restore it (#13225 QA review).
func TestResumeOntoTodoRefusesWhileAgentStaysOnAnotherOpenTodo(t *testing.T) {
	s, f := newQuietService(t)
	row := stoppedBoxAgent("both0001")
	f.setSessions("box", row)
	resumingNode(f)
	first := onBoxTask(t, s, "both0001", "conv1", false, "first part")
	releaseBoxLink(t, s, first.ID, "both0001")
	second, err := s.createTodo(t.Context(), "Task B", "notes", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(t.Context(), second.ID, "box", "both0001", "B", "/code/both0001", "claude"); err != nil {
		t.Fatal(err)
	}
	_, err = runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "both0001",
		"text": "Fix the first part again", "resume": true, "resume_why": "it knows the code", "todo_id": first.ID})
	if err == nil || !strings.Contains(err.Error(), "still on to-do "+second.ID) {
		t.Fatalf("err = %v, want the still-on-another-to-do refusal", err)
	}
	if starts := startCalls(f); len(starts) != 0 {
		t.Fatalf("nothing may start on a refusal, got %d", len(starts))
	}
	got, ok := s.openTodoOfAgent("box", "both0001")
	if !ok || got.ID != second.ID {
		t.Fatalf("the other to-do must keep its Agent: %+v ok=%v", got, ok)
	}
	older, _ := s.todoNow(first.ID)
	if len(older.Agents) != 1 || older.Agents[0].ReleasedAt == nil || older.Agents[0].Via == "resume" {
		t.Fatalf("the first to-do's historical link must stay released and untouched: %+v", older.Agents)
	}
}

// A lead re-links its own still-open, unassigned to-do the same way the main conversation can.
func TestALeadResumesItsAgentOntoItsOwnTodo(t *testing.T) {
	s, f := leadTakeEnv(t, stoppedBoxAgent("leadback1"))
	resumingNode(f)
	todo := onBoxTask(t, s, "leadback1", "lead0001", false, "the follow-up lands")
	releaseBoxLink(t, s, todo.ID, "leadback1")
	res, err := runJSON(t, s, "lead0001", "agent_send", map[string]any{"machine": "box", "session_id": "leadback1",
		"text": "One more pass on the same artifact", "resume": true, "resume_why": "it made the artifact", "todo_id": todo.ID})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.openTodoOfAgent("box", "leadback1")
	if !ok || got.ID != todo.ID || got.Origin != "lead0001" {
		t.Fatalf("not back on the lead's to-do: %+v ok=%v", got, ok)
	}
	if !strings.Contains(res, "Resumed leadback1 on box") {
		t.Fatalf("receipt = %q", res)
	}
}
