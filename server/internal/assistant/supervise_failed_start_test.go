package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// reviewTestTask plans one supervised task from conv1 and waits for either its linked Agent or its failed start.
func reviewTestTask(t *testing.T, s *Service, f *fakeHub, over map[string]any, startErr error) Todo {
	t.Helper()
	known := map[string]bool{}
	for _, todo := range s.todosOf(context.Background(), "conv1") {
		known[todo.ID] = true
	}
	f.mu.Lock()
	f.failStart = startErr
	f.mu.Unlock()
	raw, err := json.Marshal(briefTask(over))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.toolTaskPlan(context.Background(), "conv1", raw); err != nil {
		t.Fatal(err)
	}
	var found Todo
	waitFor(t, "the planned task's start result", func() bool {
		for _, todo := range s.todosOf(context.Background(), "conv1") {
			if known[todo.ID] {
				continue
			}
			if startErr != nil && todo.StartError != "" && todo.DoneAt != nil || startErr == nil && len(todo.Agents) == 1 {
				found = todo
				return true
			}
		}
		return false
	})
	f.mu.Lock()
	f.failStart = nil
	f.mu.Unlock()
	return found
}

func reviewTestAsk(s *Service, id string) *todoAsk {
	ask, ok := s.todoAsks(context.Background())[id]
	if !ok {
		return nil
	}
	return &ask
}

func failedStartReviewService(t *testing.T, withStore bool) (*Service, *fakeHub, Store) {

	t.Helper()
	var st Store
	if withStore {
		st = todoTestDB(t)
	}
	s, f := newQuietServiceOn(t, st)
	s.reports.mu.Lock()
	s.reports.delay = time.Hour
	s.reports.mu.Unlock()
	t.Cleanup(func() {
		s.reports.mu.Lock()
		defer s.reports.mu.Unlock()
		if s.reports.timer != nil {
			s.reports.timer.Stop()
		}
	})
	return s, f, st
}

// Accepted and cancelled reviews both settle the failed-start fields in memory and in the store, without inventing
// completion effects for a task whose Agent never ran. The unreviewed fixture is the positive failed-start control.
func TestTaskReviewClosesAFailedStart(t *testing.T) {
	for _, withStore := range []bool{false, true} {
		for _, tc := range []struct {
			verdict string
			doneBy  string
		}{{verdict: reviewCancelled, doneBy: db.DoneByCancelled}, {verdict: reviewAccepted, doneBy: db.DoneByReview}} {
			name := tc.verdict + "/memory"
			if withStore {
				name = tc.verdict + "/postgres"
			}
			t.Run(name, func(t *testing.T) {
				s, f, st := failedStartReviewService(t, withStore)
				todo := reviewTestTask(t, s, f, nil, errors.New("synthetic start failure"))
				before := todoNow(s, todo.ID)
				if before.StartError == "" || before.DoneAt == nil || before.DoneBy != "" {
					t.Fatalf("failed-start fixture = %+v", before)
				}
				p, i, err := s.currentPlanAssignment(t.Context(), before)
				if err != nil || s.planWithSpace(t.Context(), p).Tasks[i].Error == "" {
					t.Fatalf("open failure projection: plan=%+v error=%v", p, err)
				}
				tr := triageTodo(listedTodo{Todo: before}, noSessions, time.Now(), time.Time{}, time.UTC, false)
				if tr.Section != triageWaiting || tr.Rule != ruleFailedStart {
					t.Fatalf("unreviewed failed start is not actionable: %+v", tr)
				}
				waitFor(t, "the original failed-start report", func() bool {
					s.reports.mu.Lock()
					defer s.reports.mu.Unlock()
					item := s.reports.pending[todo.ID]
					return item != nil && item.state == reportFailed
				})

				s.reports.mu.Lock()
				pending := s.reports.pending[todo.ID]
				var reportBefore reportItem
				hadReport := pending != nil
				if hadReport {
					reportBefore = *pending
				}
				s.reports.mu.Unlock()
				postsBefore := len(f.appParams(AppFeishu, FeishuPost))
				textsBefore := len(f.appParams(AppFeishu, FeishuText))
				pushesBefore := len(f.pushed())
				planEvents := strings.Count(f.eventTypes(), "task_plan")

				args := map[string]any{"verdict": tc.verdict, "note": "review the failed start"}
				if tc.verdict == reviewAccepted {
					args["checks"] = completeReviewChecks(todoNow(s, todo.ID))
				}
				if _, err := reviewErr(s, todo.ID, args); err != nil {
					t.Fatal(err)
				}
				s.tasks.outcomeRuns.Wait()
				got := todoNow(s, todo.ID)
				if got.StartError != "" || got.DoneBy != tc.doneBy || got.DoneAt == nil {
					t.Fatalf("reviewed failed start = %+v, want start_error empty and done_by %q", got, tc.doneBy)
				}
				if wire := s.planWithSpace(t.Context(), p).Tasks[i]; wire.Error != "" || wire.ErrorCode != "" || wire.WorkspaceError != nil || wire.RetryWith != nil {
					t.Fatalf("closed task still advertises the previous failure: %+v", wire)
				}
				if strings.Count(f.eventTypes(), "task_plan") <= planEvents {
					t.Fatal("connected clients were not told to replace the stale plan")
				}
				if p.Tasks[i].Error == "" {
					t.Fatal("client projection erased the historical failed attempt")
				}
				if database, ok := st.(*db.DB); ok {
					stored, err := database.GetTodo(context.Background(), todo.ID)
					if err != nil || stored.StartError != got.StartError || stored.DoneBy != got.DoneBy || stored.DoneAt == nil || !stored.DoneAt.Equal(*got.DoneAt) {
						t.Fatalf("stored = %+v, memory = %+v, err=%v", stored, got, err)
					}
				}
				tr = triageTodo(listedTodo{Todo: got}, noSessions, time.Now(), time.Time{}, time.UTC, false)
				if tr.Section == triageWaiting || tr.Rule == ruleFailedStart {
					t.Fatalf("the reviewed failed start still asks for action: %+v", tr)
				}

				s.reports.mu.Lock()
				afterReport := s.reports.pending[todo.ID]
				s.reports.mu.Unlock()
				if hadReport != (afterReport != nil) || hadReport && (afterReport.state != reportBefore.state || afterReport.text != reportBefore.text) {
					t.Fatalf("a close report was added: before=%+v, after=%+v", reportBefore, afterReport)
				}
				if askOf(s, todo.ID) != nil || len(f.appParams(AppFeishu, FeishuPost)) != postsBefore ||
					len(f.appParams(AppFeishu, FeishuText)) != textsBefore || len(f.pushed()) != pushesBefore {
					t.Fatalf("a conclusion or notification was added: ask=%+v posts=%d texts=%d pushes=%d",
						askOf(s, todo.ID), len(f.appParams(AppFeishu, FeishuPost)), len(f.appParams(AppFeishu, FeishuText)), len(f.pushed()))
				}
				s.tasks.mu.Lock()
				outcome := false
				for key := range s.tasks.outcomes {
					outcome = outcome || strings.HasPrefix(key, "outcome:"+todo.ID+":")
				}
				s.tasks.mu.Unlock()
				if outcome {
					t.Fatal("an outcome was recorded for a task that never started")
				}
			})
		}
	}
}

// The predecessor failed to start and was cancelled; its continuation is parked. Only the predecessor's stale
// failed-start reminder disappears. The continuation and its lineage remain open, parked, and need no owner decision.
func TestCancelledFailedStartWithParkedContinuation(t *testing.T) {
	for _, withStore := range []bool{false, true} {
		name := "memory"
		if withStore {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			s, f, _ := failedStartReviewService(t, withStore)
			title := "cancelled failed start with a parked continuation"
			first := reviewTestTask(t, s, f, map[string]any{"title": title}, errors.New("synthetic start failure"))
			// Reconstruct a historical successor chain. New continues calls now reopen first in place.
			second, err := s.createTaskTodo(context.Background(), title, "Continue the original fix", "conv1", "", "")
			if err != nil {
				t.Fatal(err)
			}
			brief, err := s.ensureTaskBrief(context.Background(), first)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.linkTaskBrief(context.Background(), brief.ID, second.ID, first.ID, "the first Agent did not start"); err != nil {
				t.Fatal(err)
			}
			if err := s.linkTodoAgent(context.Background(), second.ID, "box", "legacy-agent", title, "/code/app", "claude"); err != nil {
				t.Fatal(err)
			}
			s.superviseTodo(context.Background(), second.ID, "conv1", brief.Criteria, brief.ExpectNow)

			raw, err := s.todoView(context.Background(), second.ID)
			if err != nil {
				t.Fatal(err)
			}
			var view struct {
				Lineage []LineageStep `json:"lineage"`
			}
			if err := json.Unmarshal(raw, &view); err != nil || len(view.Lineage) != 2 ||
				view.Lineage[0].TodoID != first.ID || view.Lineage[1].TodoID != second.ID || view.Lineage[1].Continues != first.ID {
				t.Fatalf("continuation lineage = %+v, raw=%s, err=%v", view.Lineage, raw, err)
			}
			if _, err := reviewErr(s, first.ID, map[string]any{"verdict": reviewCancelled, "note": "the replacement has the work"}); err != nil {
				t.Fatal(err)
			}
			if _, err := reviewErr(s, second.ID, waitingPark()); err != nil {
				t.Fatal(err)
			}

			firstNow, secondNow := todoNow(s, first.ID), todoNow(s, second.ID)
			firstTriage := triageTodo(listedTodo{Todo: firstNow}, noSessions, time.Now(), time.Time{}, time.UTC, false)
			if firstNow.StartError != "" || firstNow.DoneBy != db.DoneByCancelled || firstTriage.Section == triageWaiting || firstTriage.Rule == ruleFailedStart {
				t.Fatalf("cancelled predecessor still has a failed-start reminder: todo=%+v triage=%+v", firstNow, firstTriage)
			}
			if secondNow.DoneAt != nil || secondNow.Supervision.state(secondNow) != reviewParked || askOf(s, second.ID) != nil {
				t.Fatalf("continuation is no longer parked without a decision: %+v, ask=%+v", secondNow, askOf(s, second.ID))
			}
			secondTriage := triageTodo(listedTodo{Todo: secondNow, Ask: reviewTestAsk(s, second.ID)}, noSessions, time.Now(), time.Time{}, time.UTC, false)
			if secondTriage.Section == triageWaiting || secondTriage.Rule == ruleOwnerDecides {
				t.Fatalf("parked continuation asks for action: %+v", secondTriage)
			}
			staleAgent := proto.Session{MachineID: "box", SessionID: secondNow.Agents[0].SessionID, State: proto.StateWorking,
				Status: proto.StatusWorking, UpdatedAt: time.Now().Add(-6 * time.Hour)}
			if signals := watchSignals(secondNow, []proto.Session{staleAgent}, time.Now().Add(24*time.Hour)); len(signals) != 0 {
				t.Fatalf("parked continuation produced needs-user signals: %+v", signals)
			}
		})
	}
}

// A late receipt is not a new turn, and the late Agent's own turn is no word that the task wants more either: an
// accepted failed start with a still-linked Agent stays closed until its conversation sends that Agent more (#7539);
// the turn that starts then reopens it, and its completed end waits for review again. A cancelled failed start never
// reopens this way.
func TestReviewedFailedStartAndLateAgentWork(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		s, f, todo := newSupervisedService(t)
		if err := s.failTodo(context.Background(), todo.ID, "synthetic start failure"); err != nil {
			t.Fatal(err)
		}
		if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": reviewAccepted, "note": "accept the recorded outcome",
			"checks": completeReviewChecks(todoNow(s, todo.ID))}); err != nil {
			t.Fatal(err)
		}
		got := todoNow(s, todo.ID)
		if got.StartError != "" || got.DoneBy != db.DoneByReview || got.DoneAt == nil {
			t.Fatalf("accepted failed start = %+v", got)
		}
		agentTurn(s, f, proto.StatusDone, "a receipt from the old turn", 0)
		if got = todoNow(s, todo.ID); got.DoneAt == nil || got.ReopenedAt != nil {
			t.Fatalf("an old receipt reopened the task: %+v", got)
		}

		agentTurn(s, f, proto.StatusWorking, "", 0)
		if got = todoNow(s, todo.ID); got.DoneAt == nil || got.ReopenedAt != nil || got.DoneBy != db.DoneByReview {
			t.Fatalf("the late Agent's own turn reopened the accepted task: %+v", got)
		}
		agentTurn(s, f, proto.StatusDone, "the late turn is over", 0)
		if _, err := s.routeToTask(context.Background(), "conv1", todo.ID, "carry on with the task"); err != nil {
			t.Fatalf("the owner's message on its card: %v", err)
		}
		agentTurn(s, f, proto.StatusWorking, "", 0)
		got = todoNow(s, todo.ID)
		if got.DoneAt != nil || got.ReopenedAt == nil || got.StartError != "" || got.DoneBy != "" {
			t.Fatalf("a turn its conversation sent did not reopen the accepted task: %+v", got)
		}
		f.judge = `{"verdict":"done","reason":"the new work is complete"}`
		agentTurn(s, f, proto.StatusDone, "the new turn is complete", 0)
		waitFor(t, "review of the new turn", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
		got = todoNow(s, todo.ID)
		if got.DoneAt != nil || got.Supervision.State != reviewPending {
			t.Fatalf("the new turn did not wait for review: %+v", got)
		}
		if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": reviewAccepted, "note": "accept the new turn",
			"checks": completeReviewChecks(todoNow(s, todo.ID))}); err != nil {
			t.Fatal(err)
		}
		if got = todoNow(s, todo.ID); got.DoneAt == nil || got.DoneBy != db.DoneByReview || got.StartError != "" {
			t.Fatalf("the second acceptance did not close the task: %+v", got)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		s, f, todo := newSupervisedService(t)
		if err := s.failTodo(context.Background(), todo.ID, "synthetic start failure"); err != nil {
			t.Fatal(err)
		}
		if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": reviewCancelled, "note": "no more work"}); err != nil {
			t.Fatal(err)
		}
		got := todoNow(s, todo.ID)
		if got.StartError != "" || got.DoneBy != db.DoneByCancelled || got.DoneAt == nil {
			t.Fatalf("cancelled failed start = %+v", got)
		}
		agentTurn(s, f, proto.StatusWorking, "", 0)
		if got = todoNow(s, todo.ID); got.DoneAt == nil || got.DoneBy != db.DoneByCancelled || got.ReopenedAt != nil {
			t.Fatalf("a late turn reopened the cancelled task: %+v", got)
		}
	})
}

// Genuine pending review and needs-user states remain actionable; clearing a reviewed failed start must not hide them.
func TestPendingReviewAndNeedsUserRemainActionable(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "the requested work is ready for review")
	got := todoNow(s, todo.ID)
	if got.DoneAt != nil || got.Supervision.State != reviewPending {
		t.Fatalf("pending-review fixture = %+v", got)
	}
	tr := triageTodo(listedTodo{Todo: got, Ask: reviewTestAsk(s, todo.ID)}, noSessions, time.Now(), time.Time{}, time.UTC, false)
	if tr.Section != triageJustDone || tr.Rule != ruleFinishedUnseen {
		t.Fatalf("pending review is no longer visible: %+v", tr)
	}
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	got = todoNow(s, todo.ID)
	if got.DoneAt != nil || got.Supervision.State != reviewNeedsUser || askOf(s, todo.ID) == nil {
		t.Fatalf("needs-user fixture = %+v, ask=%+v", got, askOf(s, todo.ID))
	}
	tr = triageTodo(listedTodo{Todo: got, Ask: reviewTestAsk(s, todo.ID)}, noSessions, time.Now(), time.Time{}, time.UTC, false)
	if tr.Section != triageWaiting || tr.Rule != ruleOwnerDecides {
		t.Fatalf("needs user is no longer actionable: %+v", tr)
	}
}

func TestLeaderCancelsAFailedWorkerStart(t *testing.T) {
	s, f := newQuietService(t)
	startTaskLeader(t, s, f)
	f.mu.Lock()
	f.failStart = errors.New("synthetic worker start failure")
	f.mu.Unlock()
	res := s.mcpCall(context.Background(), "box", "s1", "task_plan", workerArgs(nil), false)
	if res.IsError {
		t.Fatalf("plan the failed worker: %+v", res)
	}
	gotMessage(t, f, "its agent did not start")
	workers := todosOpenedBy(s, "box/s1")
	if len(workers) != 1 || workers[0].StartError == "" || workers[0].DoneAt == nil {
		t.Fatalf("failed worker fixture = %+v", workers)
	}
	worker := workers[0]
	raw, _ := json.Marshal(map[string]any{"todo_id": worker.ID, "verdict": reviewCancelled, "note": "stop the failed worker"})
	res = s.mcpCall(context.Background(), "box", "s1", "task_review", raw, false)
	if res.IsError {
		t.Fatalf("leader cancellation: %+v", res)
	}
	got := todoNow(s, worker.ID)
	if got.StartError != "" || got.DoneBy != db.DoneByCancelled || got.DoneAt == nil {
		t.Fatalf("leader-cancelled worker = %+v", got)
	}
	tr := triageTodo(listedTodo{Todo: got}, noSessions, time.Now(), time.Time{}, time.UTC, false)
	if tr.Section == triageWaiting || tr.Rule == ruleFailedStart {
		t.Fatalf("leader-cancelled worker still asks for action: %+v", tr)
	}
}
