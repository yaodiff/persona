package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A finished Agent whose only background work is background commands the node found stalled is no longer Working
// (#16418): its row keeps the commands and background_stalled_since for a look, and is not background_working. A
// subagent, a workflow or a Monitor beside them, or a turn that goes on, keeps the earlier rules.
func TestStalledBackgroundShellsDoNotKeepAFinishedAgentWorking(t *testing.T) {
	s, _ := newTaskService(t)
	stalled := proto.UnixTime32Of(time.Now().Add(-5 * time.Minute))
	shell := proto.BackgroundTask{ID: "b1", Kind: "shell", Command: "cmp only.json | head -2", MovedFromForeground: true}
	for _, tc := range []struct {
		name        string
		row         proto.Session
		state       string
		background  bool // background_working
		stalledKept bool
	}{
		{"stalled shell after the turn", proto.Session{Status: proto.StatusDone, Background: []proto.BackgroundTask{shell}, BackgroundStalledSince: stalled},
			proto.StateUnread, false, true},
		{"quiet shell not stalled yet", proto.Session{Status: proto.StatusDone, Background: []proto.BackgroundTask{shell}},
			proto.StateWorking, true, false},
		{"stalled shell beside a Monitor", proto.Session{Status: proto.StatusDone, BackgroundStalledSince: stalled,
			Background: []proto.BackgroundTask{shell, {ID: "m1", Kind: "monitor"}}}, proto.StateWorking, true, true},
		{"stalled shell beside a subagent", proto.Session{Status: proto.StatusDone, BackgroundStalledSince: stalled,
			Background: []proto.BackgroundTask{shell, {ID: "a1", Kind: "agent"}}}, proto.StateWorking, true, false},
		{"turn still going", proto.Session{Status: proto.StatusWorking, Background: []proto.BackgroundTask{shell}, BackgroundStalledSince: stalled},
			proto.StateWorking, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.row.SessionID, tc.row.MachineID, tc.row.Agent, tc.row.UpdatedAt = "s1", "box", "claude", time.Now().Add(-time.Minute)
			out := s.decorateInventory("box", []proto.Session{tc.row}, true)
			if len(out) != 1 {
				t.Fatalf("rows %+v", out)
			}
			got := out[0]
			if got.State != tc.state || got.BackgroundWorking != tc.background || (got.BackgroundStalledSince != 0) != tc.stalledKept ||
				len(got.Background) != len(tc.row.Background) {
				t.Fatalf("state %s background_working %v stalled %v background %d, want %s %v %v %d", got.State, got.BackgroundWorking,
					got.BackgroundStalledSince, len(got.Background), tc.state, tc.background, tc.stalledKept, len(tc.row.Background))
			}
		})
	}
}

// What settles a task's Agent with background commands left (#16418): stalled ones alone always; after a done
// report in the round under way, ones that only wait, were moved from the foreground or stalled. Never a command
// still at work, a subagent, a workflow, a Monitor, a question, or the Agent's own turn going on.
func TestTaskAgentSettledWithLeftOverShells(t *testing.T) {
	stalled := proto.UnixTime32Of(time.Now().Add(-5 * time.Minute))
	moved := proto.BackgroundTask{ID: "b1", Kind: "shell", MovedFromForeground: true}
	started := proto.BackgroundTask{ID: "b2", Kind: "shell"}
	bg := func(state string, tasks ...proto.BackgroundTask) proto.Session {
		return proto.Session{Status: proto.StatusDone, State: state, BackgroundWorking: state == proto.StateWorking, Background: tasks}
	}
	with := func(c proto.Session, f func(*proto.Session)) proto.Session { f(&c); return c }
	for _, tc := range []struct {
		name           string
		c              proto.Session
		plain, onEarly bool // settled without a done report, and with one
	}{
		{"nothing left", bg(proto.StateUnread), true, true},
		{"stalled shell, downgraded", with(bg(proto.StateUnread, started), func(c *proto.Session) { c.BackgroundStalledSince = stalled }), true, true},
		{"stalled shell, raw row", with(bg("", started), func(c *proto.Session) { c.BackgroundStalledSince = stalled }), true, true},
		{"a running shell", bg(proto.StateWorking, started), false, false},
		{"a moved shell", bg(proto.StateWorking, moved), false, true},
		{"a waiting shell", with(bg(proto.StateWorking, started), func(c *proto.Session) { c.BackgroundWaiting = true }), false, true},
		{"a moved shell beside a running one", bg(proto.StateWorking, moved, started), false, false},
		{"a moved shell beside a Monitor", bg(proto.StateWorking, moved, proto.BackgroundTask{ID: "m1", Kind: "monitor"}), false, false},
		{"a stalled shell beside a subagent", with(bg(proto.StateWorking, moved, proto.BackgroundTask{ID: "a1", Kind: "agent"}),
			func(c *proto.Session) { c.BackgroundStalledSince = stalled }), false, false},
		{"a moved shell and a running workflow", with(bg(proto.StateWorking, moved), func(c *proto.Session) {
			c.Workflows = []proto.Workflow{{TaskID: "w1", Running: true, Status: proto.WorkflowRunning}}
		}), false, false},
		{"its own turn working", with(bg(proto.StateWorking, moved), func(c *proto.Session) {
			c.BackgroundWorking, c.BackgroundStalledSince = false, stalled
		}), false, false},
		{"a question waits", with(bg(proto.StateUnread, started), func(c *proto.Session) {
			c.BackgroundStalledSince, c.PendingInteractions = stalled, 1
		}), false, false},
		{"needs you", with(bg(proto.StateNeedsYou, moved), func(c *proto.Session) { c.BackgroundStalledSince = stalled }), false, false},
		{"turn going on", with(bg(proto.StateWorking, moved), func(c *proto.Session) { c.Status = proto.StatusWorking }), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskAgentSettled(tc.c); got != tc.plain {
				t.Errorf("without a done report: settled %v, want %v", got, tc.plain)
			}
			if got := taskAgentSettledReported(tc.c, true, time.Time{}, false); got != tc.onEarly {
				t.Errorf("after a done report: settled %v, want %v", got, tc.onEarly)
			}
		})
	}
}

// A done report counts only for the Agent and turn that produced it, and only for shells started before the report.
func TestTaskReportedDoneIsTheRoundsOwn(t *testing.T) {
	at := time.Now().UTC()
	agentA := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateWorking, BackgroundWorking: true,
		Background: []proto.BackgroundTask{{ID: "b1", Kind: "shell", MovedFromForeground: true, StartedAt: at.Add(-time.Minute)}}}
	agentB := proto.Session{MachineID: "box", SessionID: "s2", Status: proto.StatusDone, State: proto.StateWorking, BackgroundWorking: true,
		Background: []proto.BackgroundTask{{ID: "b2", Kind: "shell", MovedFromForeground: true, StartedAt: at.Add(-time.Minute)}}}
	report := func(kind, machine, session string, reportAt, updated time.Time) Todo {
		return Todo{Supervision: &Supervision{UpdatedAt: updated,
			Progress: &TaskProgress{Kind: kind, Text: "done", At: reportAt, machine: machine, session: session}}}
	}
	if _, ok := taskReportedDone(report(upReportDone, "box", "s1", at, at.Add(-time.Minute)), agentA); !ok {
		t.Fatal("a done report from the same Agent after the supervision last changed is this round's")
	}
	if _, ok := taskReportedDone(report(upReportDone, "box", "s1", at, at.Add(time.Minute)), agentA); ok {
		t.Fatal("a review or rework after the report starts another round")
	}
	if _, ok := taskReportedDone(report(upReportProgress, "box", "s1", at, at.Add(-time.Minute)), agentA); ok {
		t.Fatal("a progress report is no done report")
	}
	if _, ok := taskReportedDone(report(upReportDone, "box", "s1", at, at.Add(-time.Minute)), agentB); ok {
		t.Fatal("another Agent must not inherit Agent A's done report")
	}
	if _, ok := taskReportedDone(report(upReportDone, "", "", at, at.Add(-time.Minute)), agentA); ok {
		t.Fatal("an unattributed done report must not settle any Agent")
	}
	reopened := report(upReportDone, "box", "s1", at, at.Add(-time.Minute))
	later := at.Add(time.Second)
	reopened.ReopenedAt = &later
	if _, ok := taskReportedDone(reopened, agentA); ok {
		t.Fatal("the task opened again after the report")
	}
	if _, ok := taskReportedDone(Todo{}, agentA); ok {
		t.Fatal("a task without supervision has no done report")
	}
	done := report(upReportDone, "box", "s1", at, at.Add(-time.Minute))
	if !taskAgentSettledIn(done, []proto.Session{agentA}, agentA) {
		t.Fatal("Agent A's done report settles its own pre-report moved shell")
	}
	if taskAgentSettledIn(done, []proto.Session{agentA, agentB}, agentB) {
		t.Fatal("Agent A's done report must not settle Agent B's moved shell")
	}
	afterReport := agentA
	afterReport.Background = []proto.BackgroundTask{{ID: "b3", Kind: "shell", MovedFromForeground: true, StartedAt: at.Add(time.Second)}}
	if taskAgentSettledIn(done, []proto.Session{afterReport}, afterReport) {
		t.Fatal("a shell started after the done report must not be settled by the earlier report")
	}
}

// Regression (#16418 review): Agent A's done report must not settle a second Agent linked later without a supervision
// update, nor a later turn of Agent A itself.
func TestDoneReportDoesNotSettleAnotherAgentOrNewTurn(t *testing.T) {
	t.Run("relinked second Agent", func(t *testing.T) {
		s, f, todo := newSupervisedService(t)
		ctx := context.Background()
		listedDecorated(t, s, f, turnRow(proto.StatusWorking, 1, ""))
		args, _ := json.Marshal(map[string]any{"kind": "done", "text": "Agent A finished"})
		if res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", args); res.IsError {
			t.Fatalf("report: %+v", res)
		}
		agentA := turnRow(proto.StatusDone, 2, "done")
		waitFor(t, "Agent A done report", func() bool {
			_, ok := taskReportedDone(todoNow(s, todo.ID), agentA)
			return ok
		})
		// Link Agent B to the same to-do without updating Supervision.UpdatedAt.
		if err := s.linkTodoAgent(ctx, todo.ID, "box", "s2", "agent B", "", "claude"); err != nil {
			t.Fatalf("link Agent B: %v", err)
		}
		cur := todoNow(s, todo.ID)
		if cur.Supervision.Progress != nil {
			t.Fatalf("linking another Agent should clear the prior done report, got %+v", cur.Supervision.Progress)
		}
		agentB := proto.Session{MachineID: "box", SessionID: "s2", Status: proto.StatusDone, State: proto.StateWorking, BackgroundWorking: true,
			Background: []proto.BackgroundTask{{ID: "b2", Kind: "shell", MovedFromForeground: true}}}
		if taskAgentSettledIn(cur, []proto.Session{agentA, agentB}, agentB) {
			t.Fatal("Agent B must not settle using Agent A's cleared done report")
		}
	})

	t.Run("new turn on same Agent", func(t *testing.T) {
		s, f, todo := newSupervisedService(t)
		ctx := context.Background()
		listedDecorated(t, s, f, turnRow(proto.StatusWorking, 1, ""))
		args, _ := json.Marshal(map[string]any{"kind": "done", "text": "Turn 1 finished"})
		if res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", args); res.IsError {
			t.Fatalf("report: %+v", res)
		}
		agentA := turnRow(proto.StatusDone, 2, "done")
		waitFor(t, "Turn 1 done report", func() bool {
			_, ok := taskReportedDone(todoNow(s, todo.ID), agentA)
			return ok
		})
		// Turn 2 starts on the same Agent without a supervision update.
		s.turnStartedObserved(ctx, SessionRef{MachineID: "box", SessionID: "s1"}, true, startRow{Rev: 3})
		if got := todoNow(s, todo.ID).Supervision.Progress; got != nil {
			t.Fatalf("new turn should clear prior done report, got %+v", got)
		}
		turn2Ended := turnRow(proto.StatusDone, 4, "waiting on shell")
		turn2Ended.State, turn2Ended.BackgroundWorking = proto.StateWorking, true
		turn2Ended.Background = []proto.BackgroundTask{{ID: "b2", Kind: "shell", MovedFromForeground: true}}
		if taskAgentSettledIn(todoNow(s, todo.ID), []proto.Session{turn2Ended}, turn2Ended) {
			t.Fatal("Turn 2 must not settle using Turn 1's done report")
		}
	})
}

// decoratedRow is what the manager's session list says of row (the manager decorates the node's rows; the fake
// answers sessions.list with them, as the hub's own list does).
func decoratedRow(t *testing.T, s *Service, row proto.Session) proto.Session {
	t.Helper()
	out := s.decorateInventory(row.MachineID, []proto.Session{row}, true)
	if len(out) != 1 {
		t.Fatalf("decorated %+v", out)
	}
	return out[0]
}

// listedDecorated has the manager hear the node's row and the fake list it as the manager decorates it. It returns once
// the manager has handled what the row shows: a turn that starts or ends runs on the fleet's queue after setSessions
// returns, and a start handled after the test's next step would take back a done report made in that turn (#16579).
func listedDecorated(t *testing.T, s *Service, f *fakeHub, row proto.Session) proto.Session {
	t.Helper()
	s.setSessions("box", []proto.Session{row})
	waitFleetOrder(t, s)
	d := decoratedRow(t, s, row)
	f.setSessions("box", d)
	return d
}

// The hang of #16418 end to end: a task Agent's foreground cmp hit the timeout and moved to the background, the Agent
// finished its work and its turn closed, and the command sat there. While the node has not judged it, the Agent is
// Working and the task waits, as before. Once the node marks it stalled, the row is no longer Working and the
// supervision sweep settles the closed turn: one receipt, one judgment, one supervision message, pending review.
func TestStalledBackgroundShellLetsTheSweepSettleTheTurn(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	sweeps(t, s, todo.ID, 1) // the manager's sweep has run: the turns' ledger is kept from here on
	shell := proto.BackgroundTask{ID: "b1", Kind: "shell", Description: "cmp only.json | head -2", MovedFromForeground: true,
		StartedAt: time.Now().Add(-10 * time.Minute)}
	working := turnRow(proto.StatusWorking, 1, "")
	listedDecorated(t, s, f, working)
	f.setItems(turnPage("turn-1", "", false)...)
	reply := "修好了，PR https://github.com/o/r/pull/901 已合并"
	f.setItems(turnPage("turn-1", reply, true)...)
	ended := turnRow(proto.StatusDone, 2, reply)
	ended.State, ended.Background = "", []proto.BackgroundTask{shell}
	if d := listedDecorated(t, s, f, ended); d.State != proto.StateWorking || !d.BackgroundWorking {
		t.Fatalf("a shell not judged yet keeps the finished Agent Working: %s %v", d.State, d.BackgroundWorking)
	}
	sweeps(t, s, todo.ID, 2)
	stillTurns(t, "while the shell is not judged", turnCounts{}, s, todo.ID, f)
	if now := todoNow(s, todo.ID); now.Supervision.State == reviewPending {
		t.Fatal("background work not judged stalled must not complete the task")
	}
	stalled := ended
	stalled.BackgroundStalledSince = proto.UnixTime32Of(time.Now().Add(-4 * time.Minute))
	d := listedDecorated(t, s, f, stalled)
	if d.State != proto.StateUnread || d.BackgroundWorking || d.BackgroundStalledSince == 0 || len(d.Background) != 1 {
		t.Fatalf("stalled shell row: state %s background_working %v stalled %v background %+v", d.State, d.BackgroundWorking,
			d.BackgroundStalledSince, d.Background)
	}
	sweeps(t, s, todo.ID, 2)
	wantTurns(t, "the sweep settles the closed turn", oneEach, s, todo.ID, f)
	if now := todoNow(s, todo.ID); now.DoneAt != nil || now.Supervision.State != reviewPending {
		t.Fatalf("not waiting for review: %+v %+v", now, now.Supervision)
	}
	sweeps(t, s, todo.ID, 2)
	stillTurns(t, "after later sweeps", oneEach, s, todo.ID, f)
}

// A task Agent that reported the task done (report_to_supervisor) and ended its turn with only a moved-from-foreground
// command left settles at once (#16418), without waiting for the node's grace: its row is still Working (background),
// but the report says the work is over. A command it started itself and that still runs keeps it waiting.
func TestDoneReportSettlesWithMovedShellLeft(t *testing.T) {
	for _, tc := range []struct {
		name    string
		shell   proto.BackgroundTask
		settles bool
	}{
		{"moved from the foreground", proto.BackgroundTask{ID: "b1", Kind: "shell", MovedFromForeground: true}, true},
		{"started in the background, still running", proto.BackgroundTask{ID: "b2", Kind: "shell", Command: "go test ./..."}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			ctx := context.Background()
			sweeps(t, s, todo.ID, 1) // the turns' ledger is kept from here on
			listedDecorated(t, s, f, turnRow(proto.StatusWorking, 1, ""))
			args, _ := json.Marshal(map[string]any{"kind": "done", "text": "Login crash fixed; PR merged",
				"evidence": []string{"PR 合并到 master: https://github.com/o/r/pull/901"}})
			if res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", args); res.IsError {
				t.Fatalf("report: %+v", res)
			}
			waitFor(t, "the done report", func() bool {
				_, ok := taskReportedDone(todoNow(s, todo.ID), turnRow(proto.StatusDone, 2, ""))
				return ok
			})
			reply := "修好了，PR https://github.com/o/r/pull/901 已合并"
			f.setItems(turnPage("turn-1", reply, true)...)
			ended := turnRow(proto.StatusDone, 2, reply)
			ended.State, ended.Background = "", []proto.BackgroundTask{tc.shell}
			if d := listedDecorated(t, s, f, ended); d.State != proto.StateWorking || !d.BackgroundWorking {
				t.Fatalf("a shell left keeps the row Working (background): %s %v", d.State, d.BackgroundWorking)
			}
			sweeps(t, s, todo.ID, 2)
			if !tc.settles {
				// Only the report's own message to the supervisor: no receipt, no judgment.
				stillTurns(t, "a running shell after the report", turnCounts{0, 0, 1}, s, todo.ID, f)
				return
			}
			wantTurns(t, "the done report settles the turn", oneEach, s, todo.ID, f)
			if now := todoNow(s, todo.ID); now.Supervision.State != reviewPending {
				t.Fatalf("not waiting for review: %+v", now.Supervision)
			}
		})
	}
}
