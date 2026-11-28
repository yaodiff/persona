package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestDeferredUnsupervisedTurnRetriesWithoutInventory(t *testing.T) {
	for _, change := range []string{"same turn", "new turn", "open turn", "released", "reopened", "another agent working"} {
		t.Run(change, func(t *testing.T) {
			shortTurnRetries(t, time.Nanosecond)
			s, f, todo := newSupervisedService(t)
			ctx := context.Background()
			s.todos.mu.Lock()
			s.todos.todos[todo.ID].Supervision = nil
			s.todos.mu.Unlock()
			todo = todoNow(s, todo.ID)
			items := turnPage("held-turn", "The change is finished and verified.", true)
			f.setItems(items...)
			row := turnRow(proto.StatusDone, 3, "The change is finished and verified.")
			nodeListed(s, f, row)
			end, _ := turnEndOf(items)
			work := time.Now()
			s.takeTurn(ctx, todo, row, items, end, true, work, turnDone)
			s.rememberSessionActivity(ctx, "box", "s1")
			s.applyTaskDone(ctx, todo, work, true, taskDoneCheck{Verdict: verdictDone, WouldClose: true, Reason: "Finished"}, nil)
			if e, _ := s.turnOf(ctx, todo.ID); e.Judged != nil {
				t.Fatal("the owner's activity did not defer this judgment")
			}
			f.setSessions("box")
			s.setSessions("box", nil)
			switch change {
			case "new turn":
				f.setItems(turnPage("later-turn", "Another exchange finished.", true)...)
			case "open turn":
				f.setItems(turnPage("later-turn", "More work is running.", false)...)
			case "released":
				s.todos.mu.Lock()
				s.todos.todos[todo.ID].Agents[0].ReleasedAt = &work
				s.todos.mu.Unlock()
			case "reopened":
				s.todos.mu.Lock()
				reopened := work.Add(time.Second)
				s.todos.todos[todo.ID].ReopenedAt = &reopened
				s.todos.mu.Unlock()
			case "another agent working":
				if err := s.linkTodoAgent(ctx, todo.ID, "box", "s2", "Another Agent", "/code", "claude"); err != nil {
					t.Fatal(err)
				}
				other := row
				other.SessionID, other.Status = "s2", proto.StatusWorking
				nodeListed(s, f, other)
			}
			sweeps(t, s, todo.ID, turnJudgeTries+2)
			if got := todoNow(s, todo.ID); got.DoneAt != nil {
				t.Fatal("the missing row bypassed owner activity")
			}
			s.taskActivity.mu.Lock()
			s.taskActivity.times[runtimeListID("box", "s1")] = time.Now().Add(-taskUserIdle - time.Second)
			s.taskActivity.mu.Unlock()
			sweeps(t, s, todo.ID, 2)
			got := todoNow(s, todo.ID)
			if change == "same turn" {
				if got.DoneAt != nil || got.Supervision == nil || got.Supervision.State != reviewPending {
					t.Fatalf("the durable completed turn did not reach review: %+v", got)
				}
				f.mu.Lock()
				judged := f.judged
				f.mu.Unlock()
				if judged != 1 {
					t.Fatalf("the durable turn was judged %d times, want once", judged)
				}
			} else if got.DoneAt != nil {
				t.Fatalf("a stale durable turn closed the task after %s", change)
			}
		})
	}
}

func TestDeferredUnsupervisedTurnRetriesFromDurableLedgerWithoutInventory(t *testing.T) {
	shortTurnRetries(t, time.Nanosecond)
	database := taskRecoveryDatabase(t)
	s, f := newTaskServiceOn(t, database)
	todo := startedTask(t, s, f)
	if todo.Supervision != nil {
		t.Fatal("the fixture must be unsupervised")
	}
	ctx := context.Background()
	items := turnPage("durably-held-turn", "The change is finished and verified.", true)
	f.setItems(items...)
	row := turnRow(proto.StatusDone, 3, "The change is finished and verified.")
	nodeListed(s, f, row)
	end, _ := turnEndOf(items)
	work := time.Now()
	s.takeTurn(ctx, todo, row, items, end, true, work, turnDone)
	s.rememberSessionActivity(ctx, "box", "s1")
	s.applyTaskDone(ctx, todo, work, true, taskDoneCheck{Verdict: verdictDone, WouldClose: true, Reason: "Finished"}, nil)
	again, f2 := restartedOn(t, database)
	setSettings(t, again, func(v *Settings) { v.Main = "conv1" })
	f2.judge = `{"verdict":"done","reason":"Finished and verified"}`
	f2.setItems(items...)
	f2.setSessions("box")
	again.setSessions("box", nil)
	if err := again.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	sweeps(t, again, todo.ID, 2)
	if got := todoNow(again, todo.ID); got.DoneAt != nil {
		t.Fatal("a manager restart lost the durable owner-activity hold")
	}
	if e, ok := again.turnOf(ctx, todo.ID); !ok || e.Key != end.Key || e.Judged != nil {
		t.Fatalf("the pending turn did not survive restart: %+v", e)
	}
	if err := again.loadTaskActivity(ctx); err != nil {
		t.Fatal(err)
	}
	again.taskActivity.mu.Lock()
	again.taskActivity.times[runtimeListID("box", "s1")] = time.Now().Add(-taskUserIdle - time.Second)
	again.taskActivity.mu.Unlock()
	sweeps(t, again, todo.ID, 2)
	if got := todoNow(again, todo.ID); got.DoneAt != nil || got.Supervision == nil || got.Supervision.State != reviewPending {
		t.Fatalf("the restarted manager did not recover the turn for review: %+v", got)
	}
	f2.mu.Lock()
	defer f2.mu.Unlock()
	if f2.judged != 1 {
		t.Fatalf("the durable turn was judged %d times after restart", f2.judged)
	}
}
