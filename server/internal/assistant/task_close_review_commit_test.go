package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestTaskReviewOfAlreadyClosedTaskSurvivesRestart(t *testing.T) {
	for _, verdict := range []string{reviewAccepted, reviewCancelled} {
		t.Run(verdict, func(t *testing.T) {
			database := todoTestDB(t)
			s, f, todo := newSupervisedServiceOn(t, database)
			w := request(s, http.MethodPatch, "/api/v1/todos/"+todo.ID, `{"done":true}`)
			if w.Code != http.StatusOK {
				t.Fatalf("closing from the app: %d %s", w.Code, w.Body)
			}
			before := todoNow(s, todo.ID)
			closed, err := database.GetTodo(t.Context(), todo.ID)
			if err != nil || closed.DoneAt == nil {
				t.Fatalf("the app did not persist the closure: %+v %v", closed, err)
			}
			const note = "Reviewed after the owner closed the task"
			checks := completeReviewChecks(before)
			reviewTask(t, s, todo.ID, map[string]any{"verdict": verdict, "note": note, "checks": checks})

			restarted := New(Options{Hub: f, Store: func() Store { return database }, Logf: t.Logf})
			if err := restarted.ensureTodosLoaded(t.Context()); err != nil {
				t.Fatal(err)
			}
			stored := todoNow(restarted, todo.ID)
			if stored.DoneAt == nil || !stored.DoneAt.Equal(*closed.DoneAt) || stored.DoneBy != closed.DoneBy {
				t.Fatalf("the review changed the existing closure: before %v/%q, after %v/%q",
					closed.DoneAt, closed.DoneBy, stored.DoneAt, stored.DoneBy)
			}
			ledger := stored.Supervision
			if ledger == nil || ledger.State != verdict || len(ledger.Reviews) != len(before.Supervision.Reviews)+1 {
				t.Fatalf("the review ledger did not survive restart: %+v", ledger)
			}
			last := ledger.Reviews[len(ledger.Reviews)-1]
			if last.Verdict != verdict || last.Note != note || !reflect.DeepEqual(last.Checks, checks) {
				t.Fatalf("the stored review did not match its verdict, note and checks: %+v", last)
			}
		})
	}
}

func TestTaskReviewLateOwnerActivityCommitsNothing(t *testing.T) {
	for _, reviewer := range []string{"conversation", "leader", "no ledger"} {
		for _, verdict := range []string{reviewAccepted, reviewCancelled} {
			for _, activity := range []string{"message", "view"} {
				for _, boundary := range []string{"ledger", "closure"} {
					if reviewer == "no ledger" && boundary == "ledger" {
						continue
					}
					t.Run(strings.Join([]string{reviewer, verdict, activity, boundary}, "/"), func(t *testing.T) {
						ctx := context.Background()
						var s *Service
						var todo Todo
						var lead TodoAgent
						switch reviewer {
						case "conversation":
							s, _, todo = newSupervisedService(t)
						case "leader":
							svc, f := newQuietService(t)
							s = svc
							leader := startTaskLeader(t, s, f)
							lead = leader.Agents[0]
							todo = planWorker(t, s, f, nil)
						case "no ledger":
							svc, f := newTaskService(t)
							s = svc
							todo = startedTask(t, s, f)
							if todo.Supervision != nil {
								t.Fatal("the fixture unexpectedly has a supervision ledger")
							}
							setSettings(t, s, func(v *Settings) { v.Main = "conv1" }) // main can review legacy tasks
						}
						if _, err := s.ensureTaskBrief(ctx, todo); err != nil {
							t.Fatal(err)
						}
						s.accept.mu.Lock()
						s.accept.loaded = true
						s.accept.asks = map[string]*acceptAsk{todo.ID: {
							Conclusion: true, Outcome: outcomeDecide, AskedAt: time.Now().UTC(), Recommend: "Wait for approval",
						}}
						s.accept.mu.Unlock()
						agent := todo.Agents[0]
						args := map[string]any{"todo_id": todo.ID, "verdict": verdict}
						if todo.Supervision != nil {
							checks := []ReviewCheck{}
							for _, criterion := range todo.Supervision.DoneWhen {
								checks = append(checks, ReviewCheck{Criterion: criterion, Met: true, Evidence: "Verified"})
							}
							args["checks"] = checks
						}
						raw, _ := json.Marshal(args)
						before := todoNow(s, todo.ID)
						feedBefore := feedKinds(s, todo.Origin)
						decisionBefore := askOf(s, todo.ID)
						briefBefore, _ := s.briefOfTodo(todo.ID)

						lock, method := &s.supervise.writeMu, "updateSupervisionGuardedWithClose"
						if boundary == "closure" {
							lock = &s.todos.writing
							if reviewer == "no ledger" && verdict == reviewCancelled {
								method = "commitReviewedTodo"
							}
						}
						lock.Lock()
						unlock := sync.OnceFunc(lock.Unlock)
						defer unlock()
						done := make(chan struct{})
						var reviewErr error
						go func() {
							if reviewer == "leader" {
								_, reviewErr = s.leaderReview(ctx, lead, raw)
							} else {
								_, reviewErr = s.toolTaskReview(ctx, "conv1", raw)
							}
							close(done)
						}()
						defer func() {
							unlock()
							select {
							case <-done:
							case <-time.After(5 * time.Second):
								t.Error("review did not finish after releasing its write boundary")
							}
						}()
						// Observe the actual mutex wait: the initial activity guard has returned, but the commit cannot run.
						waitFor(t, "the review at its "+boundary+" write boundary", func() bool {
							for _, stack := range goroutineStacks() {
								if strings.Contains(stack, "[sync.Mutex.Lock]") &&
									strings.Contains(stack, "(*Service)."+method) &&
									strings.Contains(stack, "TestTaskReviewLateOwnerActivityCommitsNothing.func") {
									return true
								}
							}
							return false
						})
						if boundary == "closure" && method == "updateSupervisionGuardedWithClose" {
							if !s.supervise.writeMu.TryLock() {
								t.Fatal("terminal review took the ledger lock before the to-do lock")
							}
							s.supervise.writeMu.Unlock()
						}
						if activity == "view" {
							setTaskViewer(t, s, agent.MachineID, agent.SessionID, true)
						} else {
							s.UserSpoke(ctx, SessionRef{MachineID: agent.MachineID, SessionID: agent.SessionID})
							// A message can update decision bookkeeping in its own right; the refused review must leave that alone.
							decisionBefore = askOf(s, todo.ID)
						}
						unlock()
						select {
						case <-done:
						case <-time.After(5 * time.Second):
							t.Fatal("review did not return")
						}
						if reviewErr == nil || !strings.Contains(reviewErr.Error(), "cannot be closed") {
							t.Errorf("late activity did not refuse the review: %v", reviewErr)
						}
						after := todoNow(s, todo.ID)
						if after.DoneAt != nil || !reflect.DeepEqual(after.Supervision, before.Supervision) {
							t.Errorf("refused review changed task state: before=%+v after=%+v", before.Supervision, after.Supervision)
						}
						if got := feedKinds(s, todo.Origin); got != feedBefore {
							t.Errorf("refused review added a feed entry: before=%q after=%q", feedBefore, got)
						}
						briefAfter, _ := s.briefOfTodo(todo.ID)
						if !reflect.DeepEqual(briefAfter.Result, briefBefore.Result) {
							t.Errorf("refused review committed a brief result: %+v", briefAfter.Result)
						}
						if got := askOf(s, todo.ID); !reflect.DeepEqual(got, decisionBefore) {
							t.Errorf("refused review changed the pending decision: before=%+v after=%+v", decisionBefore, got)
						}
					})
				}
			}
		}
	}
}

type reviewActivityStore struct {
	*db.DB
	beforeCommit       func()
	beforeActivitySave func()
}

func (st *reviewActivityStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == taskActivityKey && st.beforeActivitySave != nil {
		st.beforeActivitySave()
	}
	return st.DB.PutSetting(ctx, key, value)
}

func (st *reviewActivityStore) ReviewTodo(ctx context.Context, id, reason string, accepted bool, at time.Time, ledger *db.TodoSupervisionRow, guard func() error) (*db.TodoRow, bool, error) {
	return st.DB.ReviewTodo(ctx, id, reason, accepted, at, ledger, func() error {
		st.beforeCommit()
		return guard()
	})
}

func (st *reviewActivityStore) AutoDoneTodo(ctx context.Context, id, reason string, at, work time.Time, guard func() error) (*db.TodoRow, bool, error) {
	return st.DB.AutoDoneTodo(ctx, id, reason, at, work, func() error {
		if st.beforeCommit != nil {
			st.beforeCommit()
		}
		return guard()
	})
}

func TestTaskReviewActivityDuringDatabaseWriteRollsBack(t *testing.T) {
	for _, verdict := range []string{reviewAccepted, reviewCancelled} {
		t.Run(verdict+"/view", func(t *testing.T) {
			ctx := context.Background()
			st := &reviewActivityStore{DB: todoTestDB(t)}
			s, _, todo := newSupervisedServiceOn(t, st)
			before := todoNow(s, todo.ID)
			feedBefore := feedKinds(s, todo.Origin)
			st.beforeCommit = func() {
				// Messages use the concurrent BusHub test; a view changes only the final guard's cached input.
				setTaskViewer(t, s, "box", "s1", true)
			}
			checks := []ReviewCheck{}
			for _, criterion := range todo.Supervision.DoneWhen {
				checks = append(checks, ReviewCheck{Criterion: criterion, Met: true, Evidence: "Verified"})
			}
			_, err := runJSON(t, s, "conv1", "task_review", map[string]any{
				"todo_id": todo.ID, "verdict": verdict, "checks": checks,
			})
			if err == nil || !strings.Contains(err.Error(), "cannot be closed") {
				t.Fatalf("activity during the transaction did not refuse the review: %v", err)
			}
			after := todoNow(s, todo.ID)
			if after.DoneAt != nil || !reflect.DeepEqual(after.Supervision, before.Supervision) ||
				feedKinds(s, todo.Origin) != feedBefore || s.resultOfTodo(after.ID) != nil {
				t.Fatal("a rolled-back review escaped into memory, the feed, or the brief")
			}
			stored, err := st.GetTodo(ctx, todo.ID)
			if err != nil || stored.DoneAt != nil {
				t.Fatalf("the closure was not rolled back: %+v %v", stored, err)
			}
			rows, err := st.ListTodoSupervision(ctx)
			if err != nil || len(rows) != 1 || rows[0].State != before.Supervision.State ||
				string(rows[0].Reviews) != "[]" {
				t.Fatalf("the review ledger was not rolled back: %+v %v", rows, err)
			}
		})
	}
}
