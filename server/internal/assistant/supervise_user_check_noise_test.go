package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Once a supervised task reaches reviewUserCheck its supervisor has accepted the result and handed the final check to
// the owner's own eyes. The wait is then the owner's, so clocks that nag the supervisor about execution must not fire:
// leader_stuck (taskStuck), overdue (briefClock) and estimate_due (estimateDueSignal). The owner-side verification
// card, Waiting on you / Needs you placement and the owner Pass/Reject loop stay as they are and are covered by the
// user_check, acceptance and triage tests. These tests pin the generation-site exemptions; the delivery-layer routine
// filter (supervise_routine.go) is a second net, not a substitute, because leader reports carry their own event.
//
// A to-do carries no role field: "worker" / "leader" is PlanTask metadata (Role, task_leader.go) and none of the three
// clocks reads it. The exemptions are therefore role-independent; the role=leader cases below mark the linked plan task
// accordingly and pin that the state exemption covers a leader task exactly like an ordinary one.

var userCheckRoleCases = []string{"ordinary", "worker", roleLeader}

// markPlanTaskLeader stamps the plan task behind todoID as a role=leader task, the same fact production records when a
// leader task is started.
func markPlanTaskLeader(t *testing.T, s *Service, todoID string) {
	t.Helper()
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	for _, p := range s.tasks.plans {
		for i := range p.Tasks {
			if p.Tasks[i].TodoID == todoID {
				p.Tasks[i].Role = roleLeader
				return
			}
		}
	}
	t.Fatalf("no plan task for to-do %s", todoID)
}

// TestTaskStuckSkipsOwnerUserCheck pins the taskStuck exemption for ordinary and role=leader to-dos. Aged waiting
// tasks in this state sit with the owner, not stuck on execution.
func TestTaskStuckSkipsOwnerUserCheck(t *testing.T) {
	now := time.Now().UTC()
	for _, roleCase := range userCheckRoleCases {
		t.Run(roleCase, func(t *testing.T) {
			task := Todo{Supervision: &Supervision{State: reviewUserCheck, ExpectMinutes: 30},
				Agents: []TodoAgent{{SessionID: "worked"}}}
			agedWaiting := todoTriage{Section: triageWaiting, Last: now.Add(-48 * time.Hour)}
			if taskStuck(task, agedWaiting, true, now) {
				t.Fatal("a task in the owner's user_check is not stuck for its supervisor")
			}
			// Control: the exact same aged waiting task under another owner-facing review state is still stuck, so the
			// assertion above is the user_check exemption doing work, not a vacuous fixture.
			task.Supervision.State = reviewNeedsUser
			if !taskStuck(task, agedWaiting, true, now) {
				t.Fatal("control: an aged review-needs-user wait still counts as stuck")
			}
		})
	}
}

// TestLeaderReportsUserCheckIsNotStuckForAnyRole drives the real leader report sync: neither an ordinary to-do nor a
// role=leader to-do in the owner's check may produce a stuck leader report or its sigLeaderStuck branch.
func TestLeaderReportsUserCheckIsNotStuckForAnyRole(t *testing.T) {
	ctx := context.Background()
	e := directionEnv(t, &memStore{})
	e.leaders("conv_lead_a")
	e.s.leaderReportsSync(ctx, time.Now(), false) // start the report ledger, as the existing stuck tests do

	ordinary := e.leaderTask("Fix the report and wait for the owner's check", "conv_lead_a")
	leaderRole := e.leaderTask("Leader task waiting for the owner's verification", "conv_lead_a")
	for _, id := range []string{ordinary, leaderRole} {
		if _, _, err := e.s.updateSupervision(ctx, id, func(v *Supervision) bool {
			// The real transition appends an accepted review carrying UserCheck and moves the state to user_check.
			v.State = reviewUserCheck
			v.Reviews = append(v.Reviews, TaskReview{At: time.Now().UTC(), Verdict: reviewAccepted, UserCheck: true})
			return true
		}); err != nil {
			t.Fatal(err)
		}
	}

	at := time.Now().Add(25 * time.Hour) // past the daily stuck repeat, mirroring TestLeaderReportsStuckWaitingAndEndedLeader
	for _, r := range e.reports(at, true) {
		if r.Kind == assistantwire.LeaderStuck {
			t.Fatalf("user_check to-do %s produced a leader stuck report: %+v", r.Todo, r)
		}
	}
	for _, id := range []string{ordinary, leaderRole} {
		e.s.lreports.mu.Lock()
		_, told := e.s.lreports.told.Stuck[id]
		e.s.lreports.mu.Unlock()
		if told {
			t.Fatalf("user_check to-do %s recorded a stuck tell, which also raises sigLeaderStuck", id)
		}
	}
}

func pendingSuperviseSignal(s *Service, todoID, kind string) (superviseSignal, bool) {
	s.supervise.mu.Lock()
	defer s.supervise.mu.Unlock()
	g, ok := s.supervise.pending[todoID+"|"+kind]
	return g, ok
}

func putTodoInOwnerUserCheck(t *testing.T, s *Service, id string) {
	t.Helper()
	if _, _, err := s.updateSupervision(t.Context(), id, func(v *Supervision) bool {
		v.State = reviewUserCheck
		v.Reviews = append(v.Reviews, TaskReview{At: time.Now().UTC(), Verdict: reviewAccepted, UserCheck: true})
		return true
	}); err != nil {
		t.Fatal(err)
	}
}

// TestEstimateDueSignalSkipsOwnerUserCheck pins the estimate_due generation exemption for every task role.
func TestEstimateDueSignalSkipsOwnerUserCheck(t *testing.T) {
	for _, roleCase := range userCheckRoleCases {
		t.Run(roleCase, func(t *testing.T) {
			s, _, todo, brief := startEstimateTask(t, nil, map[string]any{"expect_minutes": 30})
			if roleCase == roleLeader {
				markPlanTaskLeader(t, s, todo.ID)
			}
			putTodoInOwnerUserCheck(t, s, todo.ID)

			cur, ok := s.todoNow(todo.ID)
			if !ok {
				t.Fatal("todo vanished")
			}
			s.estimateDueSignal(t.Context(), cur, brief.EstimateDueAt.Add(time.Minute))
			if g, ok := pendingSuperviseSignal(s, todo.ID, sigEstimateDue); ok {
				t.Fatalf("estimate_due raised while the owner's check stands: %+v", g)
			}
		})
	}
}

// TestBriefClockSkipsOverdueInOwnerUserCheck pins the overdue generation exemption (signal and durable mark) for every
// task role. estimate_due has its own exemption and test; here the clock runs fully past the brief due time.
func TestBriefClockSkipsOverdueInOwnerUserCheck(t *testing.T) {
	for _, roleCase := range userCheckRoleCases {
		t.Run(roleCase, func(t *testing.T) {
			s, _, todo, brief := startEstimateTask(t, nil, map[string]any{"expect_minutes": 30})
			if roleCase == roleLeader {
				markPlanTaskLeader(t, s, todo.ID)
			}
			putTodoInOwnerUserCheck(t, s, todo.ID)

			cur, ok := s.todoNow(todo.ID)
			if !ok {
				t.Fatal("todo vanished")
			}
			s.briefClock(t.Context(), cur, brief.DueAt.Add(time.Minute))
			if g, ok := pendingSuperviseSignal(s, todo.ID, sigOverdue); ok {
				t.Fatalf("overdue raised while the owner's check stands: %+v", g)
			}
			if b, ok := s.briefOfTodo(todo.ID); ok && b.Overdue != nil {
				t.Fatalf("a durable overdue mark was written during the owner's check: %+v", b.Overdue)
			}
		})
	}
}
