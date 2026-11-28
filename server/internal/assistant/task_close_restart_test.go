package assistant

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

const restartTaskID = "todo_restart"

func closedTaskForRetirementRestart(t *testing.T, database *db.DB, closedAt time.Time) (*Service, *fakeHub) {
	t.Helper()
	ctx := t.Context()
	createdAt := closedAt.Add(-time.Hour)
	todo := db.TodoRow{ID: restartTaskID, Title: "Verify follow-up", Origin: "conv1", CreatedAt: createdAt, UpdatedAt: createdAt}
	if err := database.CreateTodo(ctx, todo); err != nil {
		t.Fatal(err)
	}
	if _, err := database.LinkTodoAgentWithSource(ctx, todo.ID, "box", "s1", "Follow-up", "/code", "claude", "", "", "", createdAt); err != nil {
		t.Fatal(err)
	}
	done := true
	if _, err := database.UpdateTodo(ctx, todo.ID, nil, nil, &done, closedAt); err != nil {
		t.Fatal(err)
	}
	s, f := newTaskServiceOn(t, database)
	if err := s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	return s, f
}

func restartTaskRetirementManager(t *testing.T, f *fakeHub, database *db.DB) *Service {
	t.Helper()
	s := newRestartedService(t, Options{Hub: f, Store: func() Store { return database }, Logf: testLogf(t)})
	if err := s.ensureTodosLoaded(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestResumedDoneTaskAgentSurvivesManagerRestart(t *testing.T) {
	ctx := t.Context()
	database := todoTestDB(t)
	now := time.Now()
	closedAt := now.Add(-2 * time.Hour).UTC().Truncate(time.Microsecond)
	s, f := closedTaskForRetirementRestart(t, database, closedAt)
	key := [2]string{"box", "s1"}
	f.setSessions("box", idleAgent(closedAt.Add(-time.Hour)))
	before := doneSeen{key: {done: closedAt, running: closedAt}}
	s.closeDoneTaskAgents(ctx, taskCloseRefused{}, before)
	if got := f.retires(); len(got) != 1 {
		t.Fatalf("the original completed task did not stop its idle Agent: %v", got)
	}

	// The same session resumed and answered. Its node stays connected across the manager restart.
	resumedAt := now.Add(-59 * time.Minute)
	f.setSessions("box", idleAgent(resumedAt))
	s.closeDoneTaskAgents(ctx, taskCloseRefused{}, before)
	if got := f.retires(); len(got) != 1 {
		t.Fatalf("stopped a resumed Agent before the manager restart: %v", got)
	}
	// An explicit restore replaces the archive entry. The task's stop evidence must still survive.
	if err := s.noteAgentArchive(ctx, idleAgent(resumedAt), false); err != nil {
		t.Fatal(err)
	}
	restarted := restartTaskRetirementManager(t, f, database)
	seen, idle, refused := doneSeen{}, idleSeen{}, taskCloseRefused{}
	restarted.closeDoneTaskAgents(ctx, refused, seen)
	restarted.closeIdleAgents(ctx, refused, seen, idle)
	// Advance both in-memory observation clocks past the short closure grace without waiting.
	w := seen[key]
	w.running = resumedAt
	seen[key], idle[key] = w, resumedAt
	restarted.closeDoneTaskAgents(ctx, refused, seen)
	restarted.closeIdleAgents(ctx, refused, seen, idle)
	if got := f.retires(); len(got) != 1 {
		t.Fatalf("the completed-task pass stopped the resumed Agent after restart, before the ordinary idle window: %v", got)
	}

	// Simulate the ordinary idle window expiring with no new activity.
	old := now.Add(-13 * time.Hour)
	f.setSessions("box", idleAgent(old))
	w = seen[key]
	w.running = old
	seen[key], idle[key] = w, old
	restarted.closeDoneTaskAgents(ctx, refused, seen)
	restarted.closeIdleAgents(ctx, refused, seen, idle)
	if got := f.retires(); len(got) != 2 {
		t.Fatalf("the resumed Agent did not stop after the ordinary idle window: %v", got)
	}
	f.mu.Lock()
	args := f.retireArgs[1]
	f.mu.Unlock()
	if args["method"] != nodeproto.StopIfIdle || args["stop_cause"] != string(proto.StopIdle) || args["close_terminal"] != nil {
		t.Fatalf("the resumed Agent did not use ordinary idle retirement with its terminal kept: %v", args)
	}
}

func TestManualCloseTimestampSurvivesRetirementRestart(t *testing.T) {
	ctx := t.Context()
	database := todoTestDB(t)
	s, f := newTaskServiceOn(t, database)
	todo, err := s.createTodo(ctx, "Verify follow-up", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1", "Follow-up", "/code", "claude"); err != nil {
		t.Fatal(err)
	}

	closedAt := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Microsecond).Add(277 * time.Nanosecond)
	done := true
	row, err := database.UpdateTodo(ctx, todo.ID, nil, nil, &done, closedAt)
	if err != nil {
		t.Fatal(err)
	}
	if stored, ok := s.applyTodoRow(ctx, todo.ID, *row, nil); !ok || stored.DoneAt == nil {
		t.Fatal("the manager did not apply the owner's completion")
	}
	key := [2]string{"box", "s1"}
	f.setSessions("box", idleAgent(closedAt))
	seen, refused := doneSeen{key: {done: closedAt, running: closedAt}}, taskCloseRefused{}
	s.closeDoneTaskAgents(ctx, refused, seen)
	if got := f.retires(); len(got) != 1 {
		t.Fatalf("the original completion did not stop the Agent: %v", got)
	}

	resumed := idleAgent(time.Now().Add(-time.Hour))
	f.setSessions("box", resumed)
	if err := s.noteAgentArchive(ctx, resumed, false); err != nil {
		t.Fatal(err)
	}
	// Restoring the archive removed its close timestamp. Exercise the stop ledger
	// left by a manager that wrote the original nanosecond value to JSON.
	stopKey := (doneTaskAgent{todo: todo.ID, machine: key[0], session: key[1]}).stopKey()
	var markers map[string]time.Time
	if ok, err := database.Setting(ctx, taskCloseStopsKey, &markers); err != nil || !ok || markers[stopKey].IsZero() {
		t.Fatalf("no durable stop marker was written: %v %v", markers, err)
	}
	firstMarker := markers[stopKey]
	markers[stopKey] = closedAt
	if err := database.PutSetting(ctx, taskCloseStopsKey, markers); err != nil {
		t.Fatal(err)
	}

	restarted := restartTaskRetirementManager(t, f, database)
	pgTime := closedAt.Truncate(time.Microsecond)
	readback := todoNow(restarted, todo.ID).DoneAt
	if readback == nil || !readback.Equal(pgTime) {
		t.Fatalf("Postgres did not truncate the completion to microseconds: %v, want %v", readback, pgTime)
	}
	t.Logf("manager returned %s, Postgres reloaded %s, legacy stop marker %s",
		row.DoneAt.Format(time.RFC3339Nano), readback.Format(time.RFC3339Nano), closedAt.Format(time.RFC3339Nano))
	seen, refused = doneSeen{}, taskCloseRefused{}
	restarted.closeDoneTaskAgents(ctx, refused, seen)
	w := seen[key]
	w.running = resumed.UpdatedAt // observation window has aged past the task-close grace
	seen[key] = w
	restarted.closeDoneTaskAgents(ctx, refused, seen)
	idle := idleSeen{}
	restarted.closeIdleAgents(ctx, refused, seen, idle)
	if got := f.retires(); len(got) != 1 {
		t.Fatalf("new manager repeated the task-completed stop for the same round: stops=%v", got)
	}
	if idle[key].IsZero() {
		t.Fatal("the resumed Agent did not enter the ordinary idle pass")
	}
	if !firstMarker.Equal(pgTime) {
		t.Fatalf("new stop markers must use Postgres precision: %s, want %s", firstMarker, pgTime)
	}
}

func TestCompletedTaskStopEvidenceAcrossManagerRestart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		legacy   bool
		reclosed bool
	}{
		{name: "offline observed before restart"},
		{name: "legacy archive", legacy: true},
		{name: "task closed again", reclosed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			database := todoTestDB(t)
			now := time.Now()
			closedAt := now.Add(-3 * time.Hour).UTC().Truncate(time.Microsecond)
			s, f := closedTaskForRetirementRestart(t, database, closedAt)
			running := idleAgent(now.Add(-time.Hour))
			if tc.legacy {
				// An older manager restored the Agent from a confirmed archive without the new setting.
				record := agentArchive{Session: archiveSnapshot(running), ClosedAt: closedAt.Add(time.Minute), ChangedAt: running.UpdatedAt}
				if err := database.PutSetting(ctx, agentArchivesKey, map[string]agentArchive{runtimeListID("box", "s1"): record}); err != nil {
					t.Fatal(err)
				}
			} else {
				f.setSessions("box", retiredAgent(closedAt))
				s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{})
			}
			if tc.reclosed {
				done := false
				if _, err := database.UpdateTodo(ctx, restartTaskID, nil, nil, &done, closedAt.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				done = true
				if _, err := database.UpdateTodo(ctx, restartTaskID, nil, nil, &done, now.Add(-2*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			restarted := restartTaskRetirementManager(t, f, database)
			f.setSessions("box", running)
			key := [2]string{"box", "s1"}
			seen, refused := doneSeen{}, taskCloseRefused{}
			restarted.closeDoneTaskAgents(ctx, refused, seen)
			w := seen[key]
			w.running = running.UpdatedAt
			seen[key] = w
			restarted.closeDoneTaskAgents(ctx, refused, seen)
			got := f.retires()
			if !tc.reclosed {
				if len(got) != 0 {
					t.Fatalf("lost stop evidence for the current completion: %v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("an earlier completion's stop evidence prevented the new closure: %v", got)
			}
			f.mu.Lock()
			args := f.retireArgs[0]
			f.mu.Unlock()
			if args["stop_cause"] != string(proto.StopTaskCompleted) {
				t.Fatalf("the new completion did not use its normal stop policy: %v", args)
			}
		})
	}
}
