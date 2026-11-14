package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func setTaskViewer(t *testing.T, s *Service, machine, session string, viewing bool) {
	t.Helper()
	c := assistantwire.Clients{}
	if viewing {
		c.Viewing = []SessionRef{{MachineID: machine, SessionID: session}}
	}
	raw, _ := json.Marshal(c)
	if !s.FleetEvent(context.Background(), assistantwire.EventClients, raw) {
		t.Fatal("viewing report was not handled")
	}
}

func TestTaskReviewRefusesOwnerActivityWithoutSideEffects(t *testing.T) {
	for _, verdict := range []string{reviewAccepted, reviewCancelled} {
		for _, activity := range []string{"message", "view", "recent read"} {
			t.Run(verdict+"/"+activity, func(t *testing.T) {
				s, f := newTaskService(t)
				todo := startedTask(t, s, f)
				ctx := context.Background()
				switch activity {
				case "message":
					s.UserSpoke(ctx, SessionRef{MachineID: "box", SessionID: "s1"})
				case "view":
					setTaskViewer(t, s, "box", "s1", true)
				case "recent read":
					s.markSeen(ctx, "box", "s1")
				}
				before, feed := todoNow(s, todo.ID), feedKinds(s, "conv1")
				_, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": verdict})
				if err == nil || !strings.Contains(err.Error(), "cannot be closed") ||
					!strings.Contains(err.Error(), "user") || !strings.Contains(err.Error(), "retry") {
					t.Fatalf("missing actionable refusal: %v", err)
				}
				after := todoNow(s, todo.ID)
				if after.DoneAt != nil || after.Supervision.state(after) != before.Supervision.state(before) ||
					feedKinds(s, "conv1") != feed || len(f.retires()) != 0 {
					t.Fatalf("refused review changed the task or its Agent: before %+v, after %+v", before, after)
				}
			})
		}
	}
}

func TestAutomaticTaskCloseDefersOwnerActivity(t *testing.T) {
	for _, activity := range []string{"message", "view"} {
		t.Run(activity, func(t *testing.T) {
			s, f := newTaskService(t)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			if activity == "message" {
				s.UserSpoke(ctx, SessionRef{MachineID: "box", SessionID: "s1"})
			} else {
				setTaskViewer(t, s, "box", "s1", true)
			}
			if ok, err := s.autoDoneTodo(ctx, todo.ID, "Finished", time.Time{}); err != nil || ok {
				t.Fatalf("automatic close: %v %v", ok, err)
			}
			if ok, err := s.reviewDoneTodo(ctx, todo.ID, "Cancelled automatically", false); err == nil || ok {
				t.Fatalf("automatic cancellation: %v %v", ok, err)
			}
			if cur := todoNow(s, todo.ID); cur.DoneAt != nil {
				t.Fatalf("activity did not keep the task open: %+v", cur)
			}
		})
	}
}

func TestTaskCloseActivityExpiresAndIgnoresReleasedAgents(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	ctx, now := context.Background(), time.Now()
	s.rememberSessionActivity(ctx, "box", "s1")
	if err := s.taskCloseActivityError(ctx, todo, now.Add(taskUserIdle-time.Second)); err == nil {
		t.Fatal("recent activity expired early")
	}
	if err := s.taskCloseActivityError(ctx, todo, now.Add(taskUserIdle+time.Second)); err != nil {
		t.Fatalf("old activity still blocks: %v", err)
	}
	setTaskViewer(t, s, "box", "s1", true)
	if err := s.taskCloseActivityError(ctx, todo, now.Add(24*time.Hour)); err == nil {
		t.Fatal("an open quiet page lost its protection")
	}
	todo.Agents[0].ReleasedAt = &now
	if err := s.taskCloseActivityError(ctx, todo, now); err != nil {
		t.Fatalf("released Agent blocked another task: %v", err)
	}
}

func TestTaskActivitySurvivesRestartAndViewExit(t *testing.T) {
	ft := newFleetTest(t)
	ctx := context.Background()
	ft.s.UserSpoke(ctx, SessionRef{MachineID: "mac", SessionID: "s1"})
	restarted := New(Options{Hub: ft.f, Store: func() Store { return ft.st }, Devices: ft.d, Logf: t.Logf})
	if why := restarted.sessionActivityReason(ctx, "mac", "s1", time.Now()); why == "" {
		t.Fatal("a restart lost the recent user message")
	}
	setTaskViewer(t, restarted, "mac", "s2", true)
	setTaskViewer(t, restarted, "mac", "s2", false)
	if why := restarted.sessionActivityReason(ctx, "mac", "s2", time.Now()); why == "" {
		t.Fatal("leaving a quiet page did not start the inactivity window")
	}
	restartedAgain := New(Options{Hub: ft.f, Store: func() Store { return ft.st }, Devices: ft.d, Logf: t.Logf})
	if why := restartedAgain.sessionActivityReason(ctx, "mac", "s2", time.Now()); why == "" {
		t.Fatal("a restart lost the last view's activity")
	}
}

func TestOwnerActivityDoesNotChangeReadReceipts(t *testing.T) {
	ft := newFleetTest(t)
	ctx := context.Background()
	ft.s.loadSeen(ctx)
	before := ft.s.seenAt("mac", "s1")
	ft.s.UserSpoke(ctx, SessionRef{MachineID: "mac", SessionID: "s1"})
	setTaskViewer(t, ft.s, "mac", "s1", true)
	setTaskViewer(t, ft.s, "mac", "s1", false)
	if after := ft.s.seenAt("mac", "s1"); !after.Equal(before) {
		t.Fatalf("owner input/view exit marked an unread answer seen: %v -> %v", before, after)
	}
	if why := ft.s.sessionActivityReason(ctx, "mac", "s1", time.Now()); why == "" {
		t.Fatal("separating read receipts lost the activity protection")
	}
}

func TestIdlePassDoesNotBypassClosedTaskGrace(t *testing.T) {
	ft, row := closeTestService(t, nil)
	row.UpdatedAt = time.Now().Add(-48 * time.Hour)
	ft.d.setSessions("mac", []proto.Session{row})
	ft.f.setSessions("mac", row)
	ctx, s := context.Background(), ft.s
	todo, err := s.createTodo(ctx, "Disposable task", "Check", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "mac", row.SessionID, row.Title, "/code", "claude"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.reviewDoneTodo(ctx, todo.ID, "Closed", true); err != nil || !ok {
		t.Fatalf("close: %v %v", ok, err)
	}
	s.closeIdleAgents(ctx, taskCloseRefused{}, idleSeen{{"mac", row.SessionID}: row.UpdatedAt})
	if len(retiredOf(ft)) != 0 {
		t.Fatal("the general idle pass bypassed the task's post-closure grace")
	}
}

func TestClosedTaskAgentWaitsForOwnerThenArchivesWithReason(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(map[bool]string{true: "completed", false: "cancelled"}[accepted], func(t *testing.T) {
			ft, row := closeTestService(t, nil)
			ft.f.setSessions("mac", row)
			s, ctx := ft.s, context.Background()
			todo, err := s.createTodo(ctx, "Disposable task", "Check the result", "conv1", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.linkTodoAgent(ctx, todo.ID, "mac", row.SessionID, row.Title, "/code", "claude"); err != nil {
				t.Fatal(err)
			}
			if ok, err := s.reviewDoneTodo(ctx, todo.ID, "Closed", accepted); err != nil || !ok {
				t.Fatalf("close: %v %v", ok, err)
			}
			s.closeDoneTaskAgents(ctx, taskCloseRefused{})
			if len(retiredOf(ft)) != 0 || archivedIDs(t, s)[row.SessionID] {
				t.Fatal("task closure immediately stopped or archived its Agent")
			}
			if err := s.sendToSession(ctx, "mac", row.SessionID, "Please check one more thing"); err != nil {
				t.Fatalf("closed task conversation cannot continue: %v", err)
			}
			s.todos.mu.Lock()
			old := time.Now().Add(-time.Hour)
			s.todos.todos[todo.ID].DoneAt = &old
			s.todos.mu.Unlock()

			// Even an old, idle closed task stays usable while the owner is there.
			setTaskViewer(t, s, "mac", row.SessionID, true)
			s.closeDoneTaskAgents(ctx, taskCloseRefused{})
			if len(retiredOf(ft)) != 0 {
				t.Fatal("stopped an Agent whose page is open")
			}
			setTaskViewer(t, s, "mac", row.SessionID, false)
			s.closeDoneTaskAgents(ctx, taskCloseRefused{})
			if len(retiredOf(ft)) != 0 {
				t.Fatal("stopped immediately after leaving the page")
			}
			s.fleet.mu.Lock()
			s.fleet.seen[[2]string{"mac", row.SessionID}] = old
			s.fleet.mu.Unlock()
			s.taskActivity.mu.Lock()
			s.taskActivity.times[runtimeListID("mac", row.SessionID)] = old
			s.taskActivity.mu.Unlock()
			working := row
			working.Status, working.State = proto.StatusWorking, proto.StateWorking
			ft.d.setSessions("mac", []proto.Session{working})
			s.closeDoneTaskAgents(ctx, taskCloseRefused{})
			if len(retiredOf(ft)) != 0 {
				t.Fatal("stopped the Agent before it became idle")
			}
			ft.d.setSessions("mac", []proto.Session{row})
			s.closeDoneTaskAgents(ctx, taskCloseRefused{})
			if len(retiredOf(ft)) != 1 || !archivedIDs(t, s)[row.SessionID] {
				t.Fatalf("idle, inactive Agent was not stopped and archived: %v", retiredOf(ft))
			}
			want := i18n.T(s.lang(ctx), "hub-agent_archives.task_closed")
			list, err := s.sessions(ctx, "mac")
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range list {
				if c.SessionID == row.SessionID && (c.ReadOnlyReason != want || c.Status != proto.StatusOffline) {
					t.Fatalf("stop reason was not overlaid: %+v", c)
				}
			}
			restarted := New(Options{Hub: ft.f, Store: func() Store { return ft.st }, Devices: ft.d, Logf: t.Logf})
			rows, _, err := restarted.archivedSessions(ctx, "mac", nil, 0)
			if err != nil || len(rows) != 1 || rows[0].ReadOnlyReason != want {
				t.Fatalf("archived stop reason did not survive restart: %+v %v", rows, err)
			}
			report := s.todoReport(ctx)
			if len(report.Completed) != 1 || report.Completed[0].Cancelled != !accepted ||
				todoNow(s, todo.ID).DoneBy != map[bool]string{true: db.DoneByReview, false: db.DoneByCancelled}[accepted] {
				t.Fatalf("completed/cancelled ownership lost: %+v", report)
			}
		})
	}
}

// #6819 B: a planned task its supervisor accepted leaves its Agent usable. The owner chatting on the Agent's own page
// (the manager sees only the turn) keeps the to-do completed, so the page keeps its closed-task notice and the
// closed-task cleanup can still run; the task's conversation sending more (agent_send, the card) still reopens it.
func TestAcceptedPlannedTaskStaysClosedWhenOwnerChatsDirectly(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	if todo.PlanID == "" {
		t.Fatal("fixture is not a planned task")
	}
	agentTurn(s, f, proto.StatusIdle, "READY", 0)
	reviewTask(t, s, todo.ID, map[string]any{"verdict": reviewAccepted, "note": "Disposable lifecycle probe"})
	if closed := todoNow(s, todo.ID); closed.DoneAt == nil || closed.DoneBy != db.DoneByReview {
		t.Fatalf("accepted review did not close the task: %+v", closed)
	}
	time.Sleep(5 * time.Millisecond)

	setTaskViewer(t, s, "box", "s1", true) // the owner opens the Agent page and types there
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusIdle, "B_AFTER_CLOSE_6819", 0)
	setTaskViewer(t, s, "box", "s1", false) // leaving the page is recent activity too
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusIdle, "still the owner's exchange", 0)
	if got := todoNow(s, todo.ID); got.DoneAt == nil || got.ReopenedAt != nil || got.DoneBy != db.DoneByReview {
		t.Fatalf("the owner's direct chat reopened the accepted task: %+v", got)
	}
	if rep := s.todoReport(context.Background()); len(rep.Completed) != 1 || rep.Completed[0].TodoID != todo.ID {
		t.Fatalf("the task left Completed after the owner's direct chat: %+v", rep)
	}

	// agent_send from the task's conversation records itself first (tools.go), as the card's routeToTask does.
	s.noteAgentSend("conv1", "box", "s1")
	agentTurn(s, f, proto.StatusWorking, "", 0)
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.ReopenedAt == nil {
		t.Fatalf("the task's conversation sending more did not reopen it: %+v", got)
	}
}
