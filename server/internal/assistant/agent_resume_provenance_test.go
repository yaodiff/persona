package assistant

import (
	"context"
	"maps"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func codexHistorySession(sid string, cause proto.StopCause, status string) proto.Session {
	return proto.Session{
		SessionID: sid, MachineID: "box", Agent: "codex", SourceKind: "codex_history",
		Status:         status,
		Capabilities:   &proto.SessionCapabilities{Read: true, Terminal: true, StopCause: cause},
		ReadOnlyReason: "stopped",
	}
}

func TestAssistantResumeProvenanceMatrix(t *testing.T) {
	// Every stop but the user's: an unknown one too, an Agent that quit by itself or that its machine's restart cut off
	// (#11528).
	causes := []proto.StopCause{proto.StopIdle, proto.StopTaskCompleted, proto.StopRestart, proto.StopUsageLimit, proto.StopTransientError,
		proto.StopResumeFailed, proto.StopCompactFailed, proto.StopTimeout, proto.StopUnknown}
	for _, source := range []string{"ordinary", "codex_history"} {
		statuses := []string{proto.StatusOffline, proto.StatusInterrupted}
		if source == "codex_history" {
			statuses = append(statuses, proto.StatusDone)
		}
		for _, cause := range causes {
			for _, status := range statuses {
				name := source + "/" + cause.String() + "/" + string(status)
				t.Run(name, func(t *testing.T) {
					s, f := newQuietService(t)
					c := stoppedBoxAgent("resume01")
					c.Capabilities.StopCause, c.Status = cause, status
					if source == "codex_history" {
						c = codexHistorySession("resume01", cause, status)
					}
					f.setSessions("box", c)
					resumingNode(f)
					original := onBoxTask(t, s, c.SessionID, "conv1", false, "The original acceptance criteria")
					_, err := runJSON(t, s, "conv1", "agent_send", map[string]any{
						"machine": "box", "session_id": c.SessionID, "text": "Continue the same task",
						"resume": true, "resume_why": "The next step needs this session's context",
					})
					if err != nil {
						t.Fatal(err)
					}
					want := map[string]any{"resume_session_id": c.SessionID, "prompt": "Continue the same task", "resume_automatic": true}
					if got := startCalls(f); len(got) != 1 || !maps.Equal(got[0], want) {
						t.Fatalf("session.start = %v, want exactly %v", got, want)
					}
					if sent := sentToAgent(f); len(sent) != 0 {
						t.Fatalf("session.send = %q", sent)
					}
					todo := onlyTodo(s)
					if todo.ID != original.ID || len(todo.Agents) != 1 || todo.Agents[0].SessionID != c.SessionID ||
						todo.Supervision == nil || todo.Supervision.Supervisor != "conv1" {
						t.Fatalf("resume changed the original supervised task: %+v", todo)
					}
				})
			}
		}
	}
}

func TestAssistantResumeRejectsIneligibleProvenance(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session proto.Session
	}{
		{"user stop", stoppedWithCause("userstop", proto.StopUser)},
		{"exit", stoppedWithCause("exitstop", proto.StopExit)},
		{"future stop cause", stoppedWithCause("future01", proto.StopCause("future"))},
		{"legacy node without capabilities", func() proto.Session {
			c := stoppedWithCause("legacy01", proto.StopUnknown)
			c.Capabilities = nil
			return c
		}()},
		{"external history source", func() proto.Session {
			c := codexHistorySession("external", proto.StopIdle, proto.StatusOffline)
			c.SourceKind = "external_history"
			return c
		}()},
		{"nonterminal codex history", func() proto.Session {
			c := codexHistorySession("noterm001", proto.StopIdle, proto.StatusOffline)
			c.Capabilities.Terminal = false
			return c
		}()},
		{"unreadable codex history", func() proto.Session {
			c := codexHistorySession("noread01", proto.StopIdle, proto.StatusOffline)
			c.Capabilities.Read = false
			return c
		}()},
		{"codex subagent", func() proto.Session {
			c := codexHistorySession("subagent1", proto.StopIdle, proto.StatusOffline)
			c.IsSubagent = true
			return c
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newQuietService(t)
			f.setSessions("box", tc.session)
			resumingNode(f)
			before := taskCount(s)
			_, err := runJSON(t, s, "conv1", "agent_send", map[string]any{
				"machine": "box", "session_id": tc.session.SessionID, "text": "Continue",
				"resume": true, "resume_why": "Continue with the existing context",
				"task": map[string]any{"title": "Must not be created"},
			})
			if err == nil {
				t.Fatal("ineligible session resumed")
			}
			assertNoResumeSideEffects(t, f)
			if got := taskCount(s); got != before {
				t.Fatalf("task count changed from %d to %d", before, got)
			}
		})
	}
}

func TestCodexHistoryResumeCannotTakeAnotherConversationsTask(t *testing.T) {
	s, f := newQuietService(t)
	c := codexHistorySession("peerhist1", proto.StopIdle, proto.StatusInterrupted)
	f.setSessions("box", c)
	todo := onBoxTask(t, s, c.SessionID, "conv2", false, "Peer acceptance")
	_, err := runJSON(t, s, "conv1", "agent_send", map[string]any{
		"machine": "box", "session_id": c.SessionID, "text": "Take over peer work",
		"resume": true, "resume_why": "Need its context",
	})
	if err == nil {
		t.Fatal("resumed a task owned by another conversation")
	}
	assertNoResumeSideEffects(t, f)
	if got := todoNow(s, todo.ID); got.Supervision == nil || got.Supervision.Supervisor != "conv2" || got.Agents[0].SessionID != c.SessionID {
		t.Fatalf("peer task changed: %+v", got)
	}
}

func TestCodexHistoryPlainSendAndAutomaticRelayStayRefused(t *testing.T) {
	s, f := newQuietService(t)
	c := codexHistorySession("history01", proto.StopRestart, proto.StatusOffline)
	f.setSessions("box", c)
	todo := onBoxTask(t, s, c.SessionID, "conv1", false, "Keep the existing task")
	before := todoNow(s, todo.ID)
	if _, err := s.runTool(t.Context(), "server", "conv1", "agent_send", sendArgs(c.SessionID, "Plain message", "")); err == nil {
		t.Fatal("plain send woke eligible codex history")
	}
	if _, err := s.sendToTask(context.Background(), todo, "Automatic follow-up"); err == nil {
		t.Fatal("automatic relay reached eligible codex history")
	}
	assertNoResumeSideEffects(t, f)
	if got := todoNow(s, todo.ID); got.Title != before.Title || got.Notes != before.Notes || got.Supervision.Supervisor != before.Supervision.Supervisor ||
		len(got.Agents) != len(before.Agents) || got.Agents[0].SessionID != before.Agents[0].SessionID {
		t.Fatalf("task changed after refused send/relay: before=%+v after=%+v", before, got)
	}
}

func stoppedWithCause(sid string, cause proto.StopCause) proto.Session {
	c := stoppedBoxAgent(sid)
	c.Capabilities.StopCause = cause
	return c
}

func assertNoResumeSideEffects(t *testing.T, f *fakeHub) {
	t.Helper()
	if starts := startCalls(f); len(starts) != 0 {
		t.Fatalf("session.start calls = %v", starts)
	}
	if sent := sentToAgent(f); len(sent) != 0 {
		t.Fatalf("session.send calls = %q", sent)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.starts) != 0 {
		t.Fatalf("replacement agent starts = %v", f.starts)
	}
}

func taskCount(s *Service) int {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	return len(s.todos.todos)
}

func TestAssistantResumeIdleUnownedCodexAppHistoryAndLinkedTodo(t *testing.T) {
	t.Run("idle unowned App history resumes on existing todo and supports compaction", func(t *testing.T) {
		for _, fullContext := range []bool{false, true} {
			name := "normal_context"
			if fullContext {
				name = "nearly_full_context"
			}
			t.Run(name, func(t *testing.T) {
				s, f := newQuietService(t)
				c := codexHistorySession("app00001", proto.StopTaskCompleted, proto.StatusOffline)
				c.SpaceKind = codexAppSpace
				c.AgentName = "Codex App"
				c.Capabilities.Takeover = &proto.CodexTakeover{Running: false, CanResume: true, AppOwned: true}
				if fullContext {
					c.ContextTokens, c.ContextWindow = 190_000, 200_000
				}
				f.setSessions("box", c)
				resumingNode(f)
				original := onBoxTask(t, s, c.SessionID, "conv1", false, "Un-draft the MR and verify")

				// Plain agent_send without resume must stay refused.
				if _, err := s.runTool(t.Context(), "server", "conv1", "agent_send", sendArgs(c.SessionID, "Un-draft the MR", "")); err == nil {
					t.Fatal("plain agent_send woke idle Codex App history")
				}
				assertNoResumeSideEffects(t, f)

				// Explicit agent_send with resume: true resumes the same thread and preserves the linked to-do.
				if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{
					"machine": "box", "session_id": c.SessionID, "text": "Un-draft the MR",
					"resume": true, "resume_why": "Needs the finished Codex App thread context",
				}); err != nil {
					t.Fatalf("agent_send resume on idle App history failed: %v", err)
				}
				starts := startCalls(f)
				if len(starts) != 1 || starts[0]["resume_session_id"] != c.SessionID || starts[0]["resume_automatic"] != true {
					t.Fatalf("session.start = %v", starts)
				}
				if fullContext {
					if _, hasPrompt := starts[0]["prompt"]; hasPrompt {
						t.Fatalf("nearly full context resume should compact before sending prompt: %v", starts[0])
					}
					if sent := sentToAgent(f); len(sent) != 1 {
						t.Fatalf("expected compacted follow-up send, got %q", sent)
					}
				}
				todo := onlyTodo(s)
				if todo.ID != original.ID || len(todo.Agents) != 1 || todo.Agents[0].SessionID != c.SessionID {
					t.Fatalf("linked todo changed: got %+v, want %s", todo, original.ID)
				}
			})
		}
	})

	t.Run("view-only App history while App is running refuses agent_send resume", func(t *testing.T) {
		s, f := newQuietService(t)
		c := proto.Session{
			SessionID: "app00002", MachineID: "box", Agent: "codex", AgentName: "Codex App",
			SourceKind: "codex_history", SpaceKind: codexAppSpace, Status: proto.StatusIdle,
			Capabilities: &proto.SessionCapabilities{
				Read:     true,
				Terminal: false,
				Takeover: &proto.CodexTakeover{Running: false, CanResume: false, Reason: "app_running", AppOwned: true},
			},
			ReadOnlyReason: "View-only in the Codex App: resume is unavailable here.",
		}
		f.setSessions("box", c)
		resumingNode(f)
		onBoxTask(t, s, c.SessionID, "conv1", false, "Original task")
		if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{
			"machine": "box", "session_id": c.SessionID, "text": "Un-draft the MR",
			"resume": true, "resume_why": "Needs context",
		}); err == nil {
			t.Fatal("agent_send resumed a view-only Codex App chat while App is running")
		}
		assertNoResumeSideEffects(t, f)
	})
}
