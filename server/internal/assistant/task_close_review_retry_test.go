package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskReviewActivityRefusalRetriesJudgedTurn(t *testing.T) {
	testReviewActivityRetry(t, false)
}

func TestLeaderReviewActivityRefusalRetriesJudgedTurn(t *testing.T) {
	testReviewActivityRetry(t, true)
}

func testReviewActivityRetry(t *testing.T, worker bool) {
	t.Helper()
	for _, verdict := range []string{reviewAccepted, reviewCancelled} {
		for _, activity := range []string{"message", "view"} {
			for _, boundary := range []string{"guard", "ledger", "closure"} {
				t.Run(verdict+"/"+activity+"/"+boundary, func(t *testing.T) {
					shortTurnRetries(t, time.Nanosecond)
					ctx := context.Background()
					var st Store
					if boundary == "closure" {
						st = todoTestDB(t)
					}
					s, f := newQuietServiceOn(t, st)
					// Inspect queued review effects, without a digest timer racing the assertions.
					s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
					f.mu.Lock()
					f.judge = `{"verdict":"done","reason":"The result is checked."}`
					f.mu.Unlock()
					var todo Todo
					var lead TodoAgent
					if worker {
						leader := startTaskLeader(t, s, f)
						lead = leader.Agents[0]
						todo = planWorker(t, s, f, nil)
					} else {
						raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
							"title": "Check the result", "prompt": "Check the result and include evidence.",
							"machine": "box", "cwd": "/code/app", "readonly": true, "why": "The files are here.",
							"done_when": []string{"The result is checked"},
						}}})
						if _, err := s.toolTaskPlan(ctx, "conv1", raw); err != nil {
							t.Fatal(err)
						}
						waitFor(t, "the task's Agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
						todo = onlyTodo(s)
					}
					agent := todo.Agents[0]
					if !worker {
						row := turnRow(proto.StatusIdle, 0, "")
						row.MachineID, row.SessionID = agent.MachineID, agent.SessionID
						nodeListed(s, f, row)
					}
					review := func(v, note string) error {
						args := map[string]any{"todo_id": todo.ID, "verdict": v, "note": note}
						var checks []ReviewCheck
						for _, criterion := range todo.Supervision.DoneWhen {
							checks = append(checks, ReviewCheck{Criterion: criterion, Met: true, Evidence: "Checked"})
						}
						args["checks"] = checks
						raw, _ := json.Marshal(args)
						var err error
						if worker {
							_, err = s.leaderReview(ctx, lead, raw)
						} else {
							_, err = s.toolTaskReview(ctx, "conv1", raw)
						}
						return err
					}
					if verdict == reviewCancelled {
						if err := review(reviewRework, "Check the revised result."); err != nil {
							t.Fatal(err)
						}
						todo = todoNow(s, todo.ID)
					}
					items := turnPage("waiting-for-review", "The result is ready.", true)
					f.setItems(items...)
					row := turnRow(proto.StatusDone, 3, "The result is ready.")
					row.MachineID, row.SessionID = agent.MachineID, agent.SessionID
					rows := []proto.Session{row}
					if worker {
						// The leader is still reviewing: its own turn must not read the worker's fixture transcript.
						rows = append(rows, proto.Session{MachineID: lead.MachineID, SessionID: lead.SessionID,
							Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
					}
					f.setSessions(agent.MachineID, rows...)
					s.setSessions(agent.MachineID, rows)
					end, _ := turnEndOf(items)
					work := time.Now()
					s.takeTurn(ctx, todo, row, items, end, true, work, turnDone)
					s.applyTaskDone(ctx, todo, work, true, taskDoneCheck{Verdict: verdictDone, WouldClose: true, Reason: "Checked"}, nil)
					before := todoNow(s, todo.ID)
					if e, _ := s.turnOf(ctx, todo.ID); e.Judged == nil || before.Supervision.State != reviewPending {
						t.Fatalf("the fixture did not reach a judged review: %+v", e)
					}
					if _, ok := reviewSignal(s, todo.ID); !ok {
						t.Fatal("the fixture did not queue its review signal")
					}
					feed := feedKinds(s, todo.Origin)
					markActive := func() {
						if activity == "view" {
							setTaskViewer(t, s, agent.MachineID, agent.SessionID, true)
						} else {
							s.rememberSessionActivity(ctx, agent.MachineID, agent.SessionID)
						}
					}
					if boundary == "guard" {
						markActive()
					}
					var lock *sync.Mutex
					method := "updateSupervisionGuardedWithClose"
					if boundary == "ledger" {
						lock = &s.supervise.writeMu
					} else if boundary == "closure" {
						lock = &s.todos.writing
					}
					if lock != nil {
						lock.Lock()
					}
					unlock := sync.OnceFunc(func() {
						if lock != nil {
							lock.Unlock()
						}
					})
					finished := make(chan error, 1)
					go func() {
						finished <- review(verdict, "Review the checked result.")
						close(finished)
					}()
					defer func() {
						unlock()
						select {
						case <-finished:
						case <-time.After(5 * time.Second):
							t.Error("the review did not finish after releasing its write boundary")
						}
						turnsQuiet(t, s, todo.ID)
					}()
					if lock != nil {
						waitFor(t, "the review's write boundary", func() bool {
							for _, stack := range goroutineStacks() {
								if strings.Contains(stack, "[sync.Mutex.Lock]") &&
									strings.Contains(stack, "(*Service)."+method) &&
									strings.Contains(stack, "testReviewActivityRetry") {
									return true
								}
							}
							return false
						})
						markActive()
					}
					unlock()
					select {
					case err := <-finished:
						if err == nil || !strings.Contains(err.Error(), "cannot be closed") {
							t.Fatalf("activity did not refuse the review: %v", err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("the refused review did not return")
					}
					if e, _ := s.turnOf(ctx, todo.ID); e.Judged != nil || e.Signal != nil {
						t.Fatalf("the activity refusal settled the completion instead of deferring it: %+v", e)
					}
					sweeps(t, s, todo.ID, turnJudgeTries+1)
					held := todoNow(s, todo.ID)
					if e, _ := s.turnOf(ctx, todo.ID); e.Judged != nil || held.DoneAt != nil || held.Supervision.State == reviewPending ||
						held.Supervision.PendingSince != nil || !reflect.DeepEqual(held.Supervision.Reviews, before.Supervision.Reviews) ||
						feedKinds(s, todo.Origin) != feed {
						t.Fatalf("the held completion published review effects: turn=%+v supervision=%+v", e, held.Supervision)
					}
					if _, ok := reviewSignal(s, todo.ID); ok {
						t.Fatal("the held completion kept or replayed its review signal")
					}
					if st != nil {
						var saved taskTurnsDoc
						if found, err := st.Setting(ctx, taskTurnsKey, &saved); err != nil || !found {
							t.Fatalf("read the deferred turn: %v %v", found, err)
						}
						if e := saved.Turns[todo.ID]; e.Judged != nil || e.Signal != nil {
							t.Fatalf("the activity hold was not durable: %+v", e)
						}
						rows, err := s.supervisionDB().ListTodoSupervision(ctx)
						if err != nil {
							t.Fatal(err)
						}
						for _, r := range rows {
							if r.TodoID == todo.ID && (r.State == reviewPending || r.PendingSince != nil) {
								t.Fatalf("the saved review still waits for acceptance: %+v", r)
							}
						}
					}
					s.tasks.turns.mu.Lock()
					tries := s.tasks.turns.tries[todo.ID]
					s.tasks.turns.mu.Unlock()
					if tries != 0 {
						t.Fatalf("owner activity consumed %d judgment attempts", tries)
					}
					if activity == "view" {
						setTaskViewer(t, s, agent.MachineID, agent.SessionID, false)
					}
					s.taskActivity.mu.Lock()
					s.taskActivity.times[runtimeListID(agent.MachineID, agent.SessionID)] = time.Now().Add(-taskUserIdle - time.Second)
					s.taskActivity.mu.Unlock()
					sweeps(t, s, todo.ID, 2)
					resumed := todoNow(s, todo.ID)
					if e, _ := s.turnOf(ctx, todo.ID); e.Judged == nil || e.Key != end.Key || !e.Work.Equal(work) ||
						resumed.DoneAt != nil || resumed.Supervision.State != reviewPending || resumed.Supervision.PendingSince == nil ||
						resumed.Supervision.Round != before.Supervision.Round ||
						!reflect.DeepEqual(resumed.Supervision.Reviews, before.Supervision.Reviews) {
						t.Fatalf("inactivity alone did not restore the same turn's review: turn=%+v supervision=%+v", e, resumed.Supervision)
					}
					if _, ok := reviewSignal(s, todo.ID); !ok {
						t.Fatal("inactivity did not restore the review signal")
					}
					if err := review(verdict, "Review the checked result."); err != nil {
						t.Fatal(err)
					}
					if got := todoNow(s, todo.ID); got.DoneAt == nil || got.Supervision.State != verdict || len(f.retires()) != 0 {
						t.Fatalf("the deferred review did not close without stopping its Agent: %+v", got)
					}
				})
			}
		}
	}
}
