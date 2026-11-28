package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestPassingJudgeCheckCannotCloseAnActiveTask(t *testing.T) {
	ctx := context.Background()
	st := &reviewActivityStore{DB: todoTestDB(t)}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	sampleAt(t, s, 0)
	agent := todo.Agents[0]
	items := turnPage("sampled-review", "The result is checked.", true)
	row := turnRow(proto.StatusDone, 3, "The result is checked.")
	row.MachineID, row.SessionID = agent.MachineID, agent.SessionID
	f.setSessions(agent.MachineID, row)
	s.setSessions(agent.MachineID, []proto.Session{row})
	end, _ := turnEndOf(items)
	work := time.Now().UTC()
	s.takeTurn(ctx, todo, row, items, end, true, work, turnDone)
	var checks []autoCheckItem
	for _, criterion := range todo.Supervision.DoneWhen {
		checks = append(checks, autoCheckItem{
			ReviewCheck: ReviewCheck{Criterion: criterion, Met: true, Evidence: "Checked"},
			Checkable:   true,
		})
	}
	check := taskDoneCheck{Verdict: verdictDone, Reason: "Checked",
		AutoCheck: &taskAutoCheck{Delivery: "live", Checks: checks}}
	s.applyTaskDone(ctx, todo, work, true, check, nil)
	after := todoNow(s, todo.ID)
	if after.DoneAt != nil || after.Supervision.State != reviewPending || len(after.Supervision.Reviews) != 0 {
		t.Fatalf("passing checks must leave the task open for its supervisor: %+v", after)
	}
	setTaskViewer(t, s, agent.MachineID, agent.SessionID, true)
	if _, err := reviewErr(s, todo.ID, map[string]any{
		"verdict": "accepted", "checks": completeReviewChecks(todo),
	}); err == nil {
		t.Fatal("the supervisor accepted while the owner was viewing the Agent")
	}
	if current := todoNow(s, todo.ID); current.DoneAt != nil || len(current.Supervision.Reviews) != 0 {
		t.Fatalf("refused review changed the task: %+v", current)
	}
}
