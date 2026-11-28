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

func TestDecisionTerminalDoesNotInterpretTheReason(t *testing.T) {
	for _, tc := range []struct{ name, reason string }{
		{"English negation", "Do not resume on the terminal; continue in this conversation."},
		{"English mention", "Resume the meeting tomorrow. The terminal output is attached."},
		{"Chinese negation", "不要在终端恢复，请在当前对话继续。"},
		{"Chinese mention", "终端输出已保存。何时恢复会议？"},
		{"Japanese negation", "ターミナルで再開しないで、この会話を続けてください。"},
		{"Japanese mention", "ターミナルの出力は保存済みです。会議をいつ再開しますか？"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			waitsForReview(t, s, f, todo.ID, "The owner can choose the next step.")
			args := rolloutDecision()
			args["reason"] = tc.reason
			if _, err := reviewErr(s, todo.ID, args); err != nil {
				t.Fatal(err)
			}
			row := listedAsk(t, s, todo.ID)
			if row["question"] != tc.reason || row["resume_terminal"] != nil {
				t.Fatalf("decision %v", row)
			}
		})
	}
}

func TestDecisionTerminalAfterResumeRefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps *proto.SessionCapabilities
		want bool
	}{
		{"user stop", &proto.SessionCapabilities{StopCause: proto.StopUser}, true},
		{"legacy node", nil, true},
		{"resumable stop", &proto.SessionCapabilities{StopCause: proto.StopUnknown}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			waitsForReview(t, s, f, todo.ID, "The owner can choose the next step.")
			ag := todo.Agents[0]
			if err := s.linkTodoAgent(t.Context(), todo.ID, ag.MachineID, "another-agent", "Second Agent", "", ""); err != nil {
				t.Fatal(err)
			}
			stopped := proto.Session{MachineID: ag.MachineID, SessionID: ag.SessionID, Capabilities: tc.caps}
			err := s.resumeAllowed(t.Context(), todo.Origin, Device{ID: ag.MachineID}, stopped, "needs its context", nil, beginResumeReview(todo.Origin))
			if tc.want && err == nil {
				t.Fatal("expected resume refusal")
			}
			if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
				t.Fatal(err)
			}
			row := listedAsk(t, s, todo.ID)
			terminal, linked := row["resume_terminal"].(map[string]any)
			if row == nil || linked != tc.want || linked && (terminal["machine_id"] != ag.MachineID || terminal["session_id"] != ag.SessionID) || row["terminal_option"] != nil {
				t.Fatalf("decision %v, expected terminal link %t for %s/%s", row, tc.want, ag.MachineID, ag.SessionID)
			}
			if tc.name == "legacy node" {
				if err := s.releaseStoppedAgents(t.Context(), todo.ID, []TodoAgent{ag}); err != nil {
					t.Fatal(err)
				}
				if released := listedAsk(t, s, todo.ID); released == nil || released["resume_terminal"] != nil {
					t.Fatalf("decision kept a released Agent: %v", released)
				}
				if err := s.linkTodoAgent(t.Context(), todo.ID, ag.MachineID, ag.SessionID, "", "", ""); err != nil {
					t.Fatal(err)
				}
				if relinked := listedAsk(t, s, todo.ID); relinked == nil || relinked["resume_terminal"] != nil {
					t.Fatalf("decision linked a later binding: %v", relinked)
				}
			}
			// A later decision on the same task needs its own refusal.
			nextReview := rolloutDecision()
			nextReview["reason"] = "What next?"
			if _, err := reviewErr(s, todo.ID, nextReview); err != nil {
				t.Fatal(err)
			}
			if next := listedAsk(t, s, todo.ID); next["question"] != "What next?" || next["resume_terminal"] != nil {
				t.Fatalf("next decision %v", next)
			}
		})
	}
}

func TestDecisionTerminalRequiresCurrentRefusal(t *testing.T) {
	for _, scenario := range []string{"other supervisor", "resumable retry", "owner resumed", "legacy done", "legacy blocked", "two refusals", "relinked Agent"} {
		t.Run(scenario, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			waitsForReview(t, s, f, todo.ID, "Choose how to proceed.")
			ag := todo.Agents[0]
			self := todo.Origin
			if scenario == "other supervisor" {
				self = "other-conversation"
			}
			device := Device{ID: ag.MachineID}
			stopped := proto.Session{MachineID: ag.MachineID, SessionID: ag.SessionID, Capabilities: &proto.SessionCapabilities{StopCause: proto.StopUser}}
			if err := s.resumeAllowed(t.Context(), self, device, stopped, "needs its context", nil, beginResumeReview(self)); err == nil {
				t.Fatal("expected the manual-resume refusal")
			}
			switch scenario {
			case "owner resumed", "legacy done", "legacy blocked":
				running := boxAgent(ag.SessionID)
				if scenario == "legacy done" {
					running.Status, running.Capabilities = proto.StatusDone, nil
				}
				if scenario == "legacy blocked" {
					running.Status, running.Capabilities = proto.StatusBlocked, nil
				}
				s.SessionsChanged(t.Context(), DeviceSessions{MachineID: ag.MachineID, Sessions: []proto.Session{running}})
			case "other supervisor":
				s.todos.mu.RLock()
				records := len(s.todos.refusedResumes)
				s.todos.mu.RUnlock()
				if records != 0 {
					t.Fatalf("stored %d attempts by an unrelated reviewer", records)
				}
			case "resumable retry":
				stopped.Capabilities.StopCause = proto.StopUnknown
				_ = s.resumeAllowed(t.Context(), self, device, stopped, "needs its context", nil, beginResumeReview(self))
			case "two refusals":
				stopped.SessionID = "other-agent"
				if err := s.linkTodoAgent(t.Context(), todo.ID, device.ID, stopped.SessionID, "", "", ""); err != nil {
					t.Fatal(err)
				}
				if err := s.resumeAllowed(t.Context(), self, device, stopped, "needs its context", nil, beginResumeReview(self)); err == nil {
					t.Fatal("expected the second manual-resume refusal")
				}
			case "relinked Agent":
				if err := s.linkTodoAgent(t.Context(), todo.ID, device.ID, stopped.SessionID, "", "", ""); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
				t.Fatal(err)
			}
			if row := listedAsk(t, s, todo.ID); row == nil || row["resume_terminal"] != nil {
				t.Fatalf("decision %v", row)
			}
		})
	}
}

func TestDecisionTerminalAfterDelegatedRework(t *testing.T) {
	s, f, leader := stoppedTaskLeader(t)
	s.todos.mu.Lock()
	s.todos.todos[leader.ID].Origin = "original-conversation"
	// With no assigned supervisor, the current main reviews this task on behalf of its original conversation.
	s.todos.todos[leader.ID].Supervision.Supervisor = ""
	s.todos.mu.Unlock()
	stopped := stoppedBoxAgent(resumedTaskLeaderSession)
	stopped.Capabilities.StopCause = proto.StopUser
	f.setSessions("box", stopped)
	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{
		"todo_id": leader.ID, "verdict": "rework", "note": "finish the missing checks",
	}); err == nil || !strings.Contains(err.Error(), "user's Resume") {
		t.Fatalf("expected a manual-resume refusal for this rework, got %v", err)
	}
	if _, err := reviewErr(s, leader.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	row := listedAsk(t, s, leader.ID)
	if target, _ := row["resume_terminal"].(map[string]any); target["session_id"] != stopped.SessionID {
		t.Fatalf("rework refusal did not reach the reviewer's decision: %v", row)
	}
}

func refuseResumeForDecision(t *testing.T, s *Service, f *fakeHub, reviewer string, stopped proto.Session) {
	t.Helper()
	// Supervision fixtures use the short session id "s1"; agent_send resolves its full alias.
	stopped.SetAliases([]string{"bound-worker"})
	f.setSessions("box", stopped)
	_, err := runJSON(t, s, reviewer, "agent_send", map[string]any{
		"machine": "box", "session_id": "bound-worker", "text": "Continue the investigation",
		"resume": true, "resume_why": "Use this session's findings",
	})
	if err == nil || !strings.Contains(err.Error(), "user's Resume") {
		t.Fatalf("expected a manual terminal refusal for %s, got %v", stopped.SessionID, err)
	}
}

func TestDecisionTerminalUserCheckReopensForThisDecision(t *testing.T) {
	s, f, todo := acceptedForUserCheck(t)
	ag := todo.Agents[0]
	refuseResumeForDecision(t, s, f, todo.Origin, stoppedWithCause(ag.SessionID, proto.StopUser))
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	if cur := todoNow(s, todo.ID); cur.ReopenedAt == nil || cur.Supervision.State != reviewNeedsUser {
		t.Fatalf("user check did not reopen for this decision: %+v", cur)
	}
	row := listedAsk(t, s, todo.ID)
	if target, _ := row["resume_terminal"].(map[string]any); target["session_id"] != ag.SessionID {
		t.Fatalf("terminal lost on user-check escalation: %v", row)
	}
	// The projection is returned to callers; changing it must not change the persisted ask in memory.
	s.todoAsks(t.Context())[todo.ID].Terminal.SessionID = "another-session"
	if a := askOf(s, todo.ID); a.ResumeTerminal.SessionID != ag.SessionID {
		t.Fatal("projection changed the stored terminal")
	}
}

func TestDecisionTerminalCloseThenReopenDiscardsRefusal(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "Choose how to proceed.")
	refuseResumeForDecision(t, s, f, todo.Origin, stoppedWithCause(todo.Agents[0].SessionID, proto.StopUser))
	for _, done := range []bool{true, false} {
		if _, err := s.editTodo(t.Context(), todo.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	if row := listedAsk(t, s, todo.ID); row == nil || row["resume_terminal"] != nil {
		t.Fatalf("reopened task kept the closed task's refusal: %v", row)
	}
}

func TestDecisionTerminalScheduleLeavesRefusalForSupervisor(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "Choose how to proceed.")
	ag := todo.Agents[0]
	refuseResumeForDecision(t, s, f, todo.Origin, stoppedWithCause(ag.SessionID, proto.StopUser))
	now := time.Now().UTC()
	task := Task{ID: "schedule-check", TodoID: todo.ID, TodoProgress: &scheduleTodoProgress{Since: now.Add(-2 * time.Hour)}}
	s.escalateScheduledTodo(t.Context(), task, ScheduleRun{ID: "check-run", At: now}, ScheduleTodoAssessment{NoProgress: 2, Reason: "No new result"})
	if a := askOf(s, todo.ID); a == nil || a.Check != task.ID || a.ResumeTerminal != nil {
		t.Fatalf("schedule's decision: %+v", a)
	}
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	if row := listedAsk(t, s, todo.ID); row["resume_terminal"] == nil {
		t.Fatalf("supervisor lost the refusal: %v", row)
	}
}

func TestDecisionTerminalEventForwardingCannotSupplyRefusal(t *testing.T) {
	for _, route := range []string{"inbox", "inbox retry", "PR"} {
		t.Run(route, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			waitsForReview(t, s, f, todo.ID, "Choose how to proceed.")
			ag := todo.Agents[0]
			c := archiveRow(agentArchive{Session: archiveSnapshot(stoppedWithCause(ag.SessionID, proto.StopIdle)), Archived: true})
			f.setSessions(ag.MachineID, c)
			storeReads := 0
			f.answerNode("session.info", func(string, json.RawMessage) (any, error) {
				storeReads++
				return map[string]any{"stop_cause": proto.StopUser}, nil
			})
			switch route {
			case "inbox", "inbox retry":
				var retry *inboxTodoForwardRecord
				if route == "inbox retry" {
					retry = &inboxTodoForwardRecord{MachineID: ag.MachineID, SessionID: ag.SessionID, WasResume: true}
				}
				result := s.deliverInboxForwardToTodo(t.Context(), &InboxItem{ID: "inbox-1"}, todo, "https://example.com/change", "Feedback", retry)
				if !strings.Contains(result.reason, "user's Resume") {
					t.Fatalf("wrong forwarding result: %+v", result)
				}
			case "PR":
				_, err := s.deliverPREventToAgent(t.Context(), todo, prRoutedEvent{Kind: prEventKindReview, ReviewState: "CHANGES_REQUESTED"}, "pr-event")
				if err == nil || !strings.Contains(err.Error(), "user's Resume") {
					t.Fatalf("wrong PR refusal: %v", err)
				}
			}
			if storeReads != 1 {
				t.Fatalf("archived stop-cause checks: %d", storeReads)
			}
			if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
				t.Fatal(err)
			}
			if row := listedAsk(t, s, todo.ID); row == nil || row["resume_terminal"] != nil {
				t.Fatalf("forwarding supplied the terminal: %v", row)
			}
		})
	}
}

func TestDecisionTerminalRecoveryDuringArchiveLookup(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "Choose how to proceed.")
	ag := todo.Agents[0]
	c := archiveRow(agentArchive{Session: archiveSnapshot(stoppedWithCause(ag.SessionID, proto.StopIdle)), Archived: true})
	f.answerNode("session.info", func(string, json.RawMessage) (any, error) {
		// The node published the resumed session while its earlier archive lookup was still in flight.
		s.todos.observeResumedSessions(DeviceSessions{MachineID: ag.MachineID, Sessions: []proto.Session{{SessionID: ag.SessionID, Status: proto.StatusDone}}})
		return map[string]any{"stop_cause": proto.StopUser}, nil
	})
	refuseResumeForDecision(t, s, f, todo.Origin, c)
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	if row := listedAsk(t, s, todo.ID); row == nil || row["resume_terminal"] != nil {
		t.Fatalf("late archive lookup restored the target: %v", row)
	}
}

func TestDecisionTerminalCleansExpiredRefusalsOnReview(t *testing.T) {
	now := time.Now().UTC()
	key := resumeAttempt{todo: "task", reviewer: "supervisor", agent: SessionRef{MachineID: "machine", SessionID: "worker"}}
	b := todoBook{
		todos:           map[string]*Todo{"task": {ID: "task", Agents: []TodoAgent{{MachineID: "machine", SessionID: "worker"}}}},
		activeBySession: map[[2]string]string{{"machine", "worker"}: "task"},
		refusedResumes:  map[resumeAttempt]time.Time{key: now.Add(-2 * decisionRecheckAfter)},
	}
	if target := b.takeResumeTerminal("task", "supervisor", now); target != nil || len(b.refusedResumes) != 0 {
		t.Fatalf("expired refusal, target=%v, remaining=%d", target, len(b.refusedResumes))
	}
}

func TestDecisionTerminalRecoveryAfterStoppedSnapshot(t *testing.T) {
	for _, via := range []string{"agent_send", "rework"} {
		t.Run(via, func(t *testing.T) {
			s, f, todo := stoppedTaskLeader(t)
			ag := todo.Agents[0]
			stopped := stoppedWithCause(ag.SessionID, proto.StopUser)
			f.setSessions(ag.MachineID, stopped)
			observed := false
			f.mu.Lock()
			f.afterList = map[string]func(){ag.MachineID: func() {
				observed = true
				live := boxAgent(ag.SessionID)
				f.setSessions(ag.MachineID, live)
				s.SessionsChanged(t.Context(), DeviceSessions{MachineID: ag.MachineID, Sessions: []proto.Session{live}})
			}}
			f.mu.Unlock()
			ctx := listHooked(t.Context())
			var err error
			if via == "agent_send" {
				args, _ := json.Marshal(map[string]any{"machine": ag.MachineID, "session_id": ag.SessionID, "text": "Finish the investigation", "resume": true, "resume_why": "The findings are in this session"})
				_, err = s.runTool(ctx, "server", "conv1", "agent_send", args)
			} else {
				// Explicit rework has already authorized this reviewer; the selected task Agent is looked up here.
				_, err = s.sendToTask(context.WithValue(ctx, reworkReviewerKey{}, beginResumeReview("conv1")), todo, "Finish the investigation")
			}
			if !observed || err == nil || !strings.Contains(err.Error(), "user's Resume") {
				t.Fatalf("stale snapshot lookup: observed=%v, err=%v", observed, err)
			}
			if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
				t.Fatal(err)
			}
			if row := listedAsk(t, s, todo.ID); row == nil || row["resume_terminal"] != nil {
				t.Fatalf("stale stopped snapshot supplied a terminal: %v", row)
			}
		})
	}
}

func TestDecisionTerminalArchiveRetryTimeoutKeepsConfirmedRefusal(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "Choose how to proceed.")
	ag := todo.Agents[0]
	c := archiveRow(agentArchive{Session: archiveSnapshot(stoppedWithCause(ag.SessionID, proto.StopIdle)), Archived: true})
	f.answerNode("session.info", func(string, json.RawMessage) (any, error) { return map[string]any{"stop_cause": proto.StopUser}, nil })
	refuseResumeForDecision(t, s, f, todo.Origin, c)
	f.answerNode("session.info", func(string, json.RawMessage) (any, error) { return nil, context.DeadlineExceeded })
	if _, err := runJSON(t, s, todo.Origin, "agent_send", map[string]any{"machine": ag.MachineID, "session_id": "bound-worker", "text": "Continue", "resume": true, "resume_why": "Keep the findings"}); err == nil || !strings.Contains(err.Error(), "could not verify") {
		t.Fatalf("retry should still report the failed archive lookup: %v", err)
	}
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	row := listedAsk(t, s, todo.ID)
	if ref, _ := row["resume_terminal"].(map[string]any); ref["session_id"] != ag.SessionID || ref["machine_id"] != ag.MachineID {
		t.Fatalf("archive timeout discarded the confirmed refusal: %v", row)
	}
}
