package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestReporterCloseLateActivityRollsBackAndRetriesJudgment(t *testing.T) {
	for _, activity := range []string{"message", "view"} {
		t.Run(activity, func(t *testing.T) {
			ctx := context.Background()
			st := &reviewActivityStore{DB: todoTestDB(t)}
			s, f := newTaskServiceOn(t, st)
			s.markReporter(ctx, "server", "conv1")
			todo := startedTask(t, s, f)
			items := turnPage("automatic-close", "The result is checked.", true)
			row := turnRow(proto.StatusDone, 3, "The result is checked.")
			nodeListed(s, f, row)
			end, _ := turnEndOf(items)
			work := time.Now().UTC()
			s.takeTurn(ctx, todo, row, items, end, true, work, turnDone)
			agent := todo.Agents[0]
			reachedCommit := false
			st.beforeCommit = func() {
				// The UPDATE has run, but its transaction has not committed.
				reachedCommit = true
				if activity == "view" {
					setTaskViewer(t, s, agent.MachineID, agent.SessionID, true)
				} else {
					s.rememberSessionActivity(ctx, agent.MachineID, agent.SessionID)
				}
			}
			check := taskDoneCheck{Verdict: verdictDone, WouldClose: true, Reason: "Checked"}
			s.applyTaskDone(ctx, todo, work, true, check, nil)
			if !reachedCommit {
				t.Fatal("automatic close did not reach its transaction's final guard")
			}
			current := todoNow(s, todo.ID)
			stored, err := st.GetTodo(ctx, todo.ID)
			if err != nil || stored.DoneAt != nil || current.DoneAt != nil || current.DoneBy != "" {
				t.Fatalf("late activity did not roll back closure: memory=%+v stored=%+v err=%v", current, stored, err)
			}
			if turn, ok := s.turnOf(ctx, todo.ID); !ok || turn.Judged != nil {
				t.Fatalf("refused automatic closure left a settled judgment: %+v", turn)
			}
			st.beforeCommit = nil
			setTaskViewer(t, s, agent.MachineID, agent.SessionID, false)
			old := time.Now().Add(-time.Hour)
			s.taskActivity.mu.Lock()
			s.taskActivity.times[runtimeListID(agent.MachineID, agent.SessionID)] = old
			s.taskActivity.mu.Unlock()
			s.fleet.mu.Lock()
			s.fleet.seen[[2]string{agent.MachineID, agent.SessionID}] = old
			s.fleet.mu.Unlock()
			s.applyTaskDone(ctx, current, work, true, check, nil)
			stored, err = st.GetTodo(ctx, todo.ID)
			if current = todoNow(s, todo.ID); err != nil || stored.DoneAt == nil ||
				current.DoneAt == nil || current.DoneBy != db.DoneByAuto {
				t.Fatalf("automatic closure could not retry after inactivity: memory=%+v stored=%+v err=%v", current, stored, err)
			}
		})
	}
}
