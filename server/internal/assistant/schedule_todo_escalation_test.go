package assistant

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// Fail only the final scheduler write, after the needs_user review and ask were saved.
type scheduleEscalationWriteStore struct {
	*db.DB
	mu       sync.Mutex
	fail     bool
	failures int
}

func (st *scheduleEscalationWriteStore) PutSetting(ctx context.Context, key string, value any) error {
	if tasks, ok := value.([]Task); ok && key == tasksKey &&
		slices.ContainsFunc(tasks, func(t Task) bool { return t.TodoProgress != nil && t.TodoProgress.Escalated }) {
		st.mu.Lock()
		fail := st.fail
		if fail {
			st.failures++
		}
		st.mu.Unlock()
		if fail {
			return errors.New("injected escalation marker write failure")
		}
	}
	return st.DB.PutSetting(ctx, key, value)
}

func TestScheduledTodoTaskLeaderEscalation(t *testing.T) {
	for _, supervisor := range []string{"conv1", "lead1"} {
		t.Run(supervisor, func(t *testing.T) {
			s, _, worker := scheduledTodoFixture(t)
			ctx := t.Context()
			leader := Todo{ID: "task-leader", Origin: supervisor,
				Agents:      []TodoAgent{{MachineID: "box", SessionID: "leader", Source: sourceLeader}},
				Supervision: &Supervision{Supervisor: supervisor}}
			putTodo(s, leader)
			worker.Origin = leaderRef("box", "leader")
			worker.Supervision.Supervisor = worker.Origin
			putTodo(s, worker)
			if !s.agentSupervised(worker) || !s.mayReviewForLeader(ctx, supervisor, worker) {
				t.Fatal("the fixture is not a worker beneath the supervising conversation")
			}
			task := todoProgressTask(worker.ID)
			setTasks(s, task)
			assessTodoBaseline(t, s, task.ID)
			_, first := assessTodoRun(t, s, task.ID, nil)
			if first.State != "no_progress" || first.NoProgress != 1 {
				t.Fatalf("first worker assessment: %+v", first)
			}
			_, second := assessTodoRun(t, s, task.ID, nil)
			current, _ := taskByID(s, task.ID)
			cur, _ := s.todoNow(worker.ID)
			if second.State != "no_progress" || second.NoProgress != 2 || !current.TodoProgress.Escalated ||
				cur.Supervision.State != reviewNeedsUser || len(cur.Supervision.Reviews) != 1 {
				t.Fatalf("the worker did not escalate: %+v %+v", current.TodoProgress, cur.Supervision)
			}
			if cur.Supervision.Supervisor != worker.Origin || cur.DoneAt != nil {
				t.Fatalf("escalation took over or closed the worker: %+v", cur)
			}
			reviews := 0
			for _, event := range testEvents(s) {
				if event.Kind == taskReviewedEvent && event.Detail["todo"] == worker.ID {
					reviews++
					if event.Detail["conversation"] != supervisor {
						t.Fatalf("the decision used an Agent reference instead of the conversation above it: %+v", event.Detail)
					}
				}
			}
			if reviews != 1 {
				t.Fatalf("expected one review by the supervising conversation, got %d", reviews)
			}
			ask := askOf(s, worker.ID)
			if ask == nil || !decideAsk(ask) || len(ask.Options) != 2 {
				t.Fatalf("the worker lacks the existing actionable owner decision: %+v", ask)
			}
			assessTodoRun(t, s, task.ID, nil)
			cur, _ = s.todoNow(worker.ID)
			if len(cur.Supervision.Reviews) != 1 {
				t.Fatal("a later no-progress run repeated the worker's owner decision")
			}
		})
	}
}

func TestScheduledTodoEscalationMarkerRecovery(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		replay, restart, legacy, other bool
	}{
		{name: "replay", replay: true},
		{name: "next assessment"},
		{name: "restart replay", replay: true, restart: true},
		{name: "restart next assessment", restart: true},
		{name: "legacy restart replay", replay: true, restart: true, legacy: true},
		{name: "legacy restart next assessment", restart: true, legacy: true},
		{name: "newer owner question", restart: true, other: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &scheduleEscalationWriteStore{DB: todoTestDB(t), fail: true}
			ctx := t.Context()
			seedTodos(t, store.DB, db.TodoRow{ID: "marker-recovery", Title: "Backfill", Origin: "conv1"})
			if err := store.PutTodoSupervision(ctx, db.TodoSupervisionRow{
				TodoID: "marker-recovery", Supervisor: "conv1", UpdatedAt: time.Now(),
			}); err != nil {
				t.Fatal(err)
			}
			s, _ := newQuietServiceOn(t, store)
			s.superviseTimes.gather = time.Hour
			s.todoAskAfter = time.Nanosecond
			task := todoProgressTask("marker-recovery")
			setTasks(s, task)
			assessTodoBaseline(t, s, task.ID)
			assessTodoRun(t, s, task.ID, nil)
			run, _ := assessTodoRun(t, s, task.ID, nil)
			original, _ := s.todoNow(task.TodoID)
			if original.Supervision.State != reviewNeedsUser || len(original.Supervision.Reviews) != 1 {
				t.Fatalf("the review was not created before the failed marker write: %+v", original.Supervision)
			}
			review := original.Supervision.Reviews[0]
			s.accept.mu.Lock()
			s.loadAcceptLocked(ctx)
			s.accept.mu.Unlock()
			ask := askOf(s, task.TodoID)
			if ask == nil || len(ask.Options) != 2 {
				t.Fatal("the original decision was not saved")
			}
			store.mu.Lock()
			failures := store.failures
			store.mu.Unlock()
			current, _ := taskByID(s, task.ID)
			if failures != 1 || current.TodoProgress.Escalated {
				t.Fatalf("fixture did not reject only the final marker write: failures=%d progress=%+v", failures, current.TodoProgress)
			}

			if tc.legacy {
				var stored []map[string]any
				if _, err := store.Setting(ctx, tasksKey, &stored); err != nil {
					t.Fatal(err)
				}
				for _, task := range stored {
					delete(task["todo_progress"].(map[string]any), "escalation_run")
				}
				if err := store.PutSetting(ctx, tasksKey, stored); err != nil {
					t.Fatal(err)
				}
			}
			if tc.other {
				if _, _, err := s.updateSupervision(ctx, task.TodoID, func(v *Supervision) bool {
					v.Reviews = append(v.Reviews, TaskReview{
						At: time.Now().UTC(), Verdict: reviewNeedsUser, Note: "An unrelated owner question",
					})
					return true
				}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.restart {
				s, _ = newQuietServiceTimed(t, store, heldSuperviseTimings())
				s.todoAskAfter = time.Nanosecond
			}
			retry := func() {
				t.Helper()
				if tc.replay {
					current, _ := taskByID(s, task.ID)
					s.finishScheduledTodo(ctx, current, run)
				} else {
					run, _ = assessTodoRun(t, s, task.ID, nil)
				}
			}
			// A second failed marker write must remain retryable without another review or card.
			retry()
			store.mu.Lock()
			failures = store.failures
			store.fail = false
			store.mu.Unlock()
			wantFailures := 2
			if tc.other {
				wantFailures = 1
			}
			if failures != wantFailures {
				t.Fatalf("incorrect decision recovery: writes rejected=%d, want %d", failures, wantFailures)
			}
			retry()
			current, _ = taskByID(s, task.ID)
			if current.TodoProgress.Escalated == tc.other {
				t.Fatalf("incorrect repaired escalation marker: %+v", current.TodoProgress)
			}
			cur, _ := s.todoNow(task.TodoID)
			wantReviews := 1
			if tc.other {
				wantReviews = 2
			}
			if len(cur.Supervision.Reviews) != wantReviews || !cur.Supervision.Reviews[0].At.Equal(review.At) ||
				tc.other && cur.Supervision.Reviews[1].Note != "An unrelated owner question" {
				t.Fatal("marker recovery duplicated or replaced its existing owner review")
			}
			s.accept.mu.Lock()
			s.loadAcceptLocked(ctx)
			s.accept.mu.Unlock()
			recoveredAsk := askOf(s, task.TodoID)
			if recoveredAsk == nil || !recoveredAsk.AskedAt.Equal(ask.AskedAt) {
				t.Fatal("marker recovery replaced its existing owner decision")
			}
			var stored []Task
			if _, err := store.Setting(ctx, tasksKey, &stored); err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(stored, func(t Task) bool { return t.ID == task.ID })
			if i < 0 || stored[i].TodoProgress == nil || stored[i].TodoProgress.Escalated == tc.other {
				t.Fatal("the repaired marker was not durable")
			}
		})
	}
}

func TestScheduledTodoEscalationIdentityResetsWithProgress(t *testing.T) {
	s, _, todo := scheduledTodoFixture(t)
	task := todoProgressTask(todo.ID)
	setTasks(s, task)
	assessTodoBaseline(t, s, task.ID)
	assessTodoRun(t, s, task.ID, nil)
	first, _ := assessTodoRun(t, s, task.ID, nil)
	assessTodoRun(t, s, task.ID, func() {
		cur, _ := s.todoNow(todo.ID)
		cur.Supervision.State = ""
		cur.Supervision.Criteria[0].State = "met"
		putTodo(s, cur)
	})
	current, _ := taskByID(s, task.ID)
	if scheduleJSON(t, current.TodoProgress)["escalation_run"] != nil || current.TodoProgress.Escalated {
		t.Fatal("observable progress retained the prior escalation's identity")
	}
	assessTodoRun(t, s, task.ID, nil)
	second, _ := assessTodoRun(t, s, task.ID, nil)
	cur, _ := s.todoNow(todo.ID)
	if len(cur.Supervision.Reviews) != 2 || first.ID == second.ID {
		t.Fatal("a new no-progress streak did not create its own decision")
	}
	current, _ = taskByID(s, task.ID)
	if scheduleJSON(t, current.TodoProgress)["escalation_run"] != second.ID {
		t.Fatal("a new streak reused the previous escalation's identity")
	}
}
