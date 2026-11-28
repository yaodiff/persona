package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskReworkWaitsForOwnerBeforeIndependentReviewRound(t *testing.T) {
	shortTurnRetries(t, time.Nanosecond)
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "Choose how the change should roll out.")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	oldDecision := askOf(s, todo.ID)
	s.UserSpoke(ctx, SessionRef{MachineID: "box", SessionID: "s1"})
	if got := askOf(s, todo.ID); got.AgentReply == nil {
		t.Fatal("the first round's decision was not paused")
	}
	if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": reviewRework, "note": "Revise the rollout plan."}); err != nil {
		t.Fatal(err)
	}
	current := todoNow(s, todo.ID)
	items := turnPage("after-supervised-rework", "The revised change is ready.", true)
	f.setItems(items...)
	row := turnRow(proto.StatusDone, 4, "The revised change is ready.")
	nodeListed(s, f, row)
	end, _ := turnEndOf(items)
	work := time.Now()
	s.takeTurn(ctx, current, row, items, end, true, work, turnDone)
	feed := feedKinds(s, todo.Origin)
	s.applyTaskDone(ctx, current, work, true, taskDoneCheck{Verdict: verdictDone, WouldClose: true, Reason: "Rework checked"}, nil)
	sweeps(t, s, todo.ID, turnJudgeTries+2)
	if e, _ := s.turnOf(ctx, todo.ID); e.Judged != nil {
		t.Fatalf("recent owner activity settled the rework's judgment: %+v", e.Judged)
	}
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.Supervision.State != reviewRework ||
		got.Supervision.Round != 1 || feedKinds(s, todo.Origin) != feed {
		t.Fatalf("recent activity advanced the rework's review round: %+v", got.Supervision)
	}
	s.taskActivity.mu.Lock()
	s.taskActivity.times[runtimeListID("box", "s1")] = time.Now().Add(-taskUserIdle - time.Second)
	s.taskActivity.mu.Unlock()
	sweeps(t, s, todo.ID, 2)
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.Supervision.State != reviewPending || got.Supervision.Round != 1 {
		t.Fatalf("inactivity did not release the rework's independent review: %+v", got.Supervision)
	}
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	if got := askOf(s, todo.ID); got == nil || got.AgentReply != nil || !got.DueAt.IsZero() ||
		got.AskedAt.Equal(oldDecision.AskedAt) {
		t.Fatalf("the earlier decision pause leaked into the new round: %+v", got)
	}
}

func TestClosedTaskJudgmentIsDroppedBeforeOwnerActivityHold(t *testing.T) {
	for _, verdict := range []string{reviewAccepted, reviewCancelled} {
		for _, activity := range []string{"message", "view"} {
			t.Run(verdict+"/"+activity, func(t *testing.T) {
				s, f, todo := newSupervisedService(t)
				ctx := context.Background()
				items := turnPage("finished-before-close", "The change is finished.", true)
				f.setItems(items...)
				row := turnRow(proto.StatusDone, 3, "The change is finished.")
				nodeListed(s, f, row)
				end, _ := turnEndOf(items)
				work := todo.CreatedAt.Add(time.Millisecond)
				s.takeTurn(ctx, todo, row, items, end, true, work, turnDone)
				if ok, err := s.reviewDoneTodo(ctx, todo.ID, "Closed before the judgment returned", verdict == reviewAccepted); err != nil || !ok {
					t.Fatalf("close: %v %v", ok, err)
				}
				closed := todoNow(s, todo.ID)
				if activity == "view" {
					setTaskViewer(t, s, row.MachineID, row.SessionID, true)
				} else {
					s.rememberSessionActivity(ctx, row.MachineID, row.SessionID)
				}

				// The judge still holds its open copy; that old work must be dropped, not deferred for a new retry.
				s.applyTaskDone(ctx, todo, work, true, taskDoneCheck{Verdict: verdictDone, WouldClose: true, Reason: "Late result"}, nil)
				if e, ok := s.turnOf(ctx, todo.ID); !ok || e.Judged == nil || e.Judged.Skip != "done" {
					t.Fatalf("owner activity kept a closed task's old judgment retryable: %+v", e)
				}
				now := todoNow(s, todo.ID)
				if now.DoneAt == nil || !now.DoneAt.Equal(*closed.DoneAt) || now.DoneBy != closed.DoneBy ||
					now.Check != nil || now.Supervision.State != closed.Supervision.State {
					t.Fatalf("a late judgment changed the closed task: before=%+v after=%+v", closed, now)
				}
			})
		}
	}
}

// Activity holds completion judgments, not the independent lifecycle of a start that never linked an Agent.
func TestTaskJudgmentActivityHoldDoesNotBlockStartRemedy(t *testing.T) {
	for _, activity := range []string{"message", "view"} {
		for _, finish := range []string{"supervisor turn", "tab expiry"} {
			t.Run(activity+"/"+finish, func(t *testing.T) {
				s, f, todo := newSupervisedService(t)
				ctx := context.Background()
				items := turnPage("finished-turn", "The change is finished.", true)
				f.setItems(items...)
				row := turnRow(proto.StatusDone, 3, "The change is finished.")
				nodeListed(s, f, row)
				end, _ := turnEndOf(items)
				s.takeTurn(ctx, todo, row, items, end, true, todo.CreatedAt.Add(time.Millisecond), turnDone)
				if activity == "view" {
					setTaskViewer(t, s, row.MachineID, row.SessionID, true)
				} else {
					s.rememberSessionActivity(ctx, row.MachineID, row.SessionID)
				}
				if s.recoverTurn(ctx, todo, row) {
					t.Fatal("owner activity did not hold the completion judgment")
				}

				failed, plan := remedyingTask(t, s, f, keptRefusal(proto.CodeShellWaiting))
				waitFor(t, "the failed-start supervision message", func() bool { return len(f.notices()) == 1 })
				if len(failed.Agents) != 0 || s.taskCloseActivityError(ctx, failed, time.Now()) != nil {
					t.Fatal("a start without a linked Agent inherited another task's activity")
				}
				s.watchTurn(ctx, failed, nil)
				if e, ok := s.turnOf(ctx, failed.ID); ok {
					t.Fatalf("a start without an Agent entered completion recovery: %+v", e)
				}
				if _, _, _, r, err := s.remedyTool(ctx, "conv1", failed.ID); err != nil || r.State != remedying {
					t.Fatalf("the pending start lost its remedy controls: start=%+v err=%v", r, err)
				}
				by := "assistant"
				if finish == "supervisor turn" {
					s.remedyTurnEnded(ctx, "conv1")
				} else {
					if err := s.patchPlanTask(ctx, plan.ID, 0, func(pt *PlanTask) {
						pt.Start.Until = time.Now().Add(-time.Second)
					}); err != nil {
						t.Fatal(err)
					}
					s.remedyWatch(ctx, todoNow(s, failed.ID), time.Now(), s.planHistoryOnce())
					by = "hub"
				}
				if now := todoNow(s, failed.ID); now.DoneAt == nil || now.StartError == "" {
					t.Fatalf("the completion hold blocked failed-start finalization: %+v", now)
				}
				if r := remedyState(t, s, plan.ID); r == nil || r.State != remedyFailed || r.By != by {
					t.Fatalf("failed-start finalization: %+v", r)
				}
				history := remedyHistory(t, s, failed.ID)
				if !slices.Contains(history, feedReceipt) || !slices.Contains(history, feedStartRemedy+"/gave_up/"+by) {
					t.Fatalf("failed-start finalization lost its receipt: %v", history)
				}
				if s.recoverTurn(ctx, todo, row) {
					t.Fatal("failed-start finalization released the owner's completion hold")
				}
				if now := todoNow(s, todo.ID); now.DoneAt != nil || now.Check != nil || now.Supervision.State == reviewPending {
					t.Fatalf("failed-start finalization changed the active task: %+v", now)
				}
				if e, _ := s.turnOf(ctx, todo.ID); e.Judged != nil {
					t.Fatalf("the active task's pending judgment was consumed: %+v", e.Judged)
				}
				f.mu.Lock()
				judged := f.judged
				f.mu.Unlock()
				if judged != 0 {
					t.Fatalf("owner activity called the completion judge %d times", judged)
				}
			})
		}
	}
}

func TestTaskJudgmentDefersAndRetriesOwnerActivity(t *testing.T) {
	for _, supervised := range []bool{false, true} {
		for _, replay := range []bool{false, true} {
			for _, activity := range []string{"message", "view"} {
				t.Run(fmt.Sprintf("supervised=%t/replay=%t/%s", supervised, replay, activity), func(t *testing.T) {
					shortTurnRetries(t, time.Nanosecond)
					s, f, todo := newSupervisedService(t)
					ctx := context.Background()
					if !supervised {
						s.todos.mu.Lock()
						s.todos.todos[todo.ID].Supervision = nil
						s.todos.mu.Unlock()
						todo = todoNow(s, todo.ID)
					}
					items := turnPage("finished-turn", "The change is finished and the PR is merged.", true)
					f.setItems(items...)
					row := turnRow(proto.StatusDone, 3, "The change is finished and the PR is merged.")
					nodeListed(s, f, row)
					end, _ := turnEndOf(items)
					work := todo.CreatedAt.Add(time.Millisecond)
					s.takeTurn(ctx, todo, row, items, end, true, work, turnDone)
					check := taskDoneCheck{Verdict: verdictDone, WouldClose: true, Reason: "Finished"}
					if replay {
						s.turnJudged(ctx, todo.ID, check, nil, "")
					}
					if activity == "view" {
						setTaskViewer(t, s, "box", "s1", true)
					} else {
						s.rememberSessionActivity(ctx, "box", "s1")
					}
					if replay {
						saved, _ := s.turnOf(ctx, todo.ID)
						work = saved.Judged.At.Add(time.Millisecond)
						s.turnAgain(ctx, todo, saved, row, row.LastText, work, true)
					} else {
						s.applyTaskDone(ctx, todo, work, true, check, nil)
					}
					// More ticks than the failed-judgment limit must still leave this retryable.
					sweeps(t, s, todo.ID, turnJudgeTries+2)
					if e, _ := s.turnOf(ctx, todo.ID); e.Judged != nil {
						t.Fatalf("owner activity settled the pending turn: %+v", e.Judged)
					}
					if now := todoNow(s, todo.ID); now.DoneAt != nil || now.Check != nil ||
						now.Supervision != nil && now.Supervision.State == reviewPending {
						t.Fatalf("owner activity applied the judgment: %+v", now)
					}
					f.mu.Lock()
					judgments := f.judged
					f.mu.Unlock()
					if judgments != 0 {
						t.Fatalf("activity-only retries called the model %d times", judgments)
					}

					if activity == "view" {
						setTaskViewer(t, s, "box", "s1", false)
					}
					s.taskActivity.mu.Lock()
					s.taskActivity.times[runtimeListID("box", "s1")] = time.Now().Add(-taskUserIdle - time.Second)
					s.taskActivity.mu.Unlock()
					// No row change or new turn: the existing sweep must retry the deferred judgment.
					sweeps(t, s, todo.ID, 2)
					now := todoNow(s, todo.ID)
					if e, _ := s.turnOf(ctx, todo.ID); e.Judged == nil {
						t.Fatal("inactivity left the turn pending forever")
					}
					if now.DoneAt != nil || now.Supervision == nil || now.Supervision.State != reviewPending {
						t.Fatalf("inactivity did not restore supervised review: %+v", now)
					}
				})
			}
		}
	}
}

type taskRetirementBarrier struct {
	Hub
	method  string
	entered chan struct{}
	release chan struct{}
}

func (h *taskRetirementBarrier) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	method := h.method
	if method == "" {
		method = nodeproto.StopIfIdle
	}
	if p.Method == method {
		close(h.entered)
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return h.Hub.DeviceCall(ctx, p)
}

func TestClosedTaskRetirementRechecksAfterStop(t *testing.T) {
	for _, closeTerminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "terminal kept", true: "terminal cleanup"}[closeTerminal], func(t *testing.T) {
			testClosedTaskRetirementRechecksAfterStop(t, closeTerminal)
		})
	}
}

func testClosedTaskRetirementRechecksAfterStop(t *testing.T, closeTerminal bool) {
	for _, method := range []string{nodeproto.StopIfIdle, "session.info"} {
		for _, change := range []string{"new task", "reopen", "reclose", "view", "message"} {
			t.Run(method+"/"+change, func(t *testing.T) {
				ft, row := closeTestService(t, nil)
				ft.f.setSessions("mac", row)
				s, ctx := ft.s, context.Background()
				closeOnEnd(t, s, closeTerminal)
				gate := &taskRetirementBarrier{Hub: s.hub, method: method, entered: make(chan struct{}), release: make(chan struct{})}
				s.hub = gate
				todo, err := s.createTodo(ctx, "Disposable task", "Check the result", "conv1", "")
				if err != nil {
					t.Fatal(err)
				}
				if err := s.linkTodoAgent(ctx, todo.ID, "mac", row.SessionID, row.Title, "/code", "claude"); err != nil {
					t.Fatal(err)
				}
				if ok, err := s.reviewDoneTodo(ctx, todo.ID, "Closed", true); err != nil || !ok {
					t.Fatalf("close: %v %v", ok, err)
				}
				s.todos.mu.Lock()
				old := time.Now().Add(-time.Hour)
				s.todos.todos[todo.ID].DoneAt = &old
				s.todos.mu.Unlock()
				finished := make(chan struct{})
				go func() {
					defer close(finished)
					s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{{"mac", row.SessionID}: {done: old, running: old}})
				}()
				defer func() {
					close(gate.release)
					select {
					case <-finished:
					case <-time.After(5 * time.Second):
						t.Error("retirement did not finish")
					}
				}()
				select {
				case <-gate.entered:
				case <-time.After(5 * time.Second):
					t.Fatalf("%s was not reached", method)
				}
				switch change {
				case "new task":
					next, err := s.createTodo(ctx, "Next task", "Keep talking", "conv1", "")
					if err != nil {
						t.Fatal(err)
					}
					if err := s.linkTodoAgent(ctx, next.ID, "mac", row.SessionID, row.Title, "/code", "claude"); err != nil {
						t.Fatal(err)
					}
				case "reopen", "reclose":
					if err := s.reopenTodo(ctx, todo.ID); err != nil {
						t.Fatal(err)
					}
					if change == "reclose" {
						if ok, err := s.reviewDoneTodo(ctx, todo.ID, "Closed again", true); err != nil || !ok {
							t.Fatalf("reclose: %v %v", ok, err)
						}
					}
				case "view":
					setTaskViewer(t, s, "mac", row.SessionID, true)
				case "message":
					s.rememberSessionActivity(ctx, "mac", row.SessionID)
				}
				// Let the blocked RPC succeed after the concurrent change, before checking archive state.
				cancelled := method == nodeproto.StopIfIdle && (change == "message" || change == "view")
				select {
				case gate.release <- struct{}{}:
				case <-finished:
					if !cancelled {
						t.Fatalf("%s ended before the fixture released it", method)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("%s did not wait for the fixture", method)
				}
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Fatalf("%s did not return", method)
				}
				wantStops := 1
				if cancelled {
					wantStops = 0
				}
				if len(retiredOf(ft)) != wantStops {
					t.Fatal("fixture did not complete its stop RPC")
				}
				if archivedIDs(t, s)[row.SessionID] {
					t.Fatalf("archived an Agent after ownership or activity changed during %s", method)
				}
			})
		}
	}
}
