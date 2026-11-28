package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

// The owner's Pass on a task the supervisor accepted with user_check is the acceptance that task_review deferred: the
// terminal cleanup the owner opted into at Start follows it, under the checks of a direct acceptance, and a terminal
// that is busy or watched at that moment stays open for the done-task pass (#16948).
func TestOwnerPassFinishesTaskTerminal(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		optIn       bool
		closeOnEnd  bool
		change      func(*proto.Session)
		shouldClose bool
	}{
		{name: "opted in", optIn: true, shouldClose: true},
		{name: "opted in with close on end", optIn: true, closeOnEnd: true, shouldClose: true},
		{name: "not opted in", optIn: false},
		{name: "not opted in with close on end", optIn: false, closeOnEnd: true},
		{name: "working", optIn: true, closeOnEnd: true, change: func(s *proto.Session) { s.Status = proto.StatusWorking }},
		{name: "queued", optIn: true, closeOnEnd: true, change: func(s *proto.Session) { s.Queued = []string{"More work"} }},
		{name: "owner viewing", optIn: true, closeOnEnd: true, change: func(s *proto.Session) { s.SeenAt = time.Now() }},
		{name: "attached", optIn: true, closeOnEnd: true, change: func(s *proto.Session) { s.Attached = true }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			s, f, task := dispatchTerminalTask(t, scenario.optIn, true)
			closeOnEnd(t, s, scenario.closeOnEnd)
			var closes int
			f.mu.Lock()
			f.nodes[devicewire.MethodCloseTaskTerminal] = func(machine string, raw json.RawMessage) (any, error) {
				closes++
				cur := todoNow(s, task.ID)
				if cur.DoneAt == nil || cur.DoneBy != db.DoneByReview {
					t.Error("close requested before the owner's Pass marked the task done")
				}
				var req devicewire.TaskTerminalCloseRequest
				if err := json.Unmarshal(raw, &req); err != nil {
					return nil, err
				}
				f.mu.Unlock()
				authorized := s.authorizeTaskTerminal(ctx, machine, req)
				f.mu.Lock()
				if !authorized {
					t.Error("the node was not authorized to close the passed task's terminal")
				}
				f.sessions["box"] = nil
				return map[string]string{"status": devicewire.TaskTerminalClosed}, nil
			}
			f.mu.Unlock()

			res, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": task.ID, "verdict": "accepted", "user_check": true})
			if err != nil || !strings.Contains(res, "Terminal cleanup: skipped") {
				t.Fatalf("user_check review = %q, %v", res, err)
			}
			s.tasks.terminalRuns.Wait()
			if closes != 0 {
				t.Fatal("the terminal was closed while the owner's verification waited")
			}
			if scenario.change != nil {
				f.mu.Lock()
				scenario.change(&f.sessions["box"][0])
				f.mu.Unlock()
			}

			if result, err := s.acceptTry(ctx, task.ID, ""); err != nil || !result.taken {
				t.Fatalf("owner Pass = %+v, %v", result, err)
			}
			s.tasks.terminalRuns.Wait()
			got := todoNow(s, task.ID)
			if got.DoneAt == nil || got.Supervision.state(got) != reviewAccepted {
				t.Fatalf("Pass did not close the task as accepted: %+v", got)
			}
			if (closes == 1) != scenario.shouldClose || closes > 1 {
				t.Fatalf("close requests after Pass = %d, want close %t", closes, scenario.shouldClose)
			}
			pt, _, err := s.taskTerminalPlan(ctx, got)
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			switch {
			case scenario.shouldClose:
				want = devicewire.TaskTerminalClosed
			case scenario.optIn:
				want = devicewire.TaskTerminalSkipped
			}
			if pt.TerminalCleanup != want {
				t.Fatalf("terminal cleanup = %q, want %q", pt.TerminalCleanup, want)
			}
			if calls := retireCalls(f); len(calls) != 0 {
				t.Fatalf("Pass stopped or closed the agent another way: %v", calls)
			}

			// A second Pass, or a repeated round, never asks the node again.
			_, _ = s.acceptTry(ctx, task.ID, "")
			s.tasks.terminalRuns.Wait()
			if closes > 1 {
				t.Fatalf("close requests after a repeated Pass = %d", closes)
			}
			if scenario.shouldClose {
				return
			}

			// What Pass left open goes the way of any accepted task's agent: once it is idle and still, the done-task
			// pass stops it, closing its terminal only with close_ended_terminals on.
			f.mu.Lock()
			row := &f.sessions["box"][0]
			row.Status, row.Queued, row.SeenAt, row.Attached = proto.StatusIdle, nil, time.Time{}, false
			f.mu.Unlock()
			old := taskCloseIdle
			taskCloseIdle = 0
			t.Cleanup(func() { taskCloseIdle = old })
			s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{})
			sweep := nodeproto.StopIfIdle + " retire=true,stop_cause=task_completed"
			if scenario.closeOnEnd {
				sweep = nodeproto.StopIfIdle + " retire=true,close_terminal=true,stop_cause=task_completed"
			}
			if calls := retireCalls(f); len(calls) != 1 || calls[0] != sweep {
				t.Fatalf("done-task pass after Pass = %v, want %q", calls, sweep)
			}
			if closes != 0 {
				t.Fatalf("the busy terminal was closed by Pass: %d", closes)
			}
		})
	}
}
