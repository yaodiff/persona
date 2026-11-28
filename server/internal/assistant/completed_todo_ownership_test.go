package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestCompletedTodoOwnershipDoesNotBecomeActiveWork(t *testing.T) {
	e := newTodoEnv(t, nil)
	now, older := time.Now(), time.Now().Add(-8*24*time.Hour)
	putTodo(e.s, Todo{ID: "old", Title: "Earlier task", DoneAt: &older,
		Agents: []TodoAgent{{MachineID: "box", SessionID: "history"}}})
	putTodo(e.s, Todo{ID: "done", Title: "Completed task", DoneAt: &now, Origin: "conv1", Quiet: true,
		Agents: []TodoAgent{
			{MachineID: "box", SessionID: "history", ReleasedAt: &now},
			{MachineID: "box", SessionID: "dismissed", DismissedAt: &now},
			{MachineID: "box", SessionID: "reused"},
		}})
	putTodo(e.s, Todo{ID: "open", Title: "Current task",
		Agents: []TodoAgent{{MachineID: "box", SessionID: "reused"}}})
	putTodo(e.s, Todo{ID: "cancelled", Title: "Cancelled task", DoneAt: &now, DoneBy: db.DoneByCancelled,
		Agents: []TodoAgent{{MachineID: "box", SessionID: "cancelled-history"}}})
	report := e.s.todoReport(context.Background())
	if len(report.Links) != 1 || report.Links[0].TodoID != "open" {
		t.Fatalf("completed work entered active links: %+v", report.Links)
	}
	if len(report.Completed) != 2 ||
		report.Completed[0].TodoID != "cancelled" || report.Completed[0].SessionID != "cancelled-history" ||
		!report.Completed[0].Cancelled || report.Completed[0].Task ||
		report.Completed[1].TodoID != "done" || report.Completed[1].SessionID != "history" ||
		report.Completed[1].Cancelled || report.Completed[1].Task {
		t.Fatalf("completed ownership: %+v", report.Completed)
	}
	if _, ok := e.s.openTodoOfAgent("box", "history"); ok {
		t.Fatal("display-only ownership became supervised work")
	}
	if len(report.Quiet) != 1 || report.Quiet[0].SessionID != "history" {
		t.Fatalf("the existing quiet grace period changed: %+v", report.Quiet)
	}
}

func TestCompletedTodoOwnershipDecoratesAndClearsWithoutTaskSignals(t *testing.T) {
	ft := newFleetTestNoStore(t)
	ft.d.setSessions("mac", []proto.Session{{SessionID: "history"}, {SessionID: "active"}})
	report := &TodoReport{
		Links: []TodoLink{{MachineID: "mac", SessionID: "active", TodoID: "current", Title: "Current task", Task: true}},
		Completed: []TodoLink{
			{MachineID: "mac", SessionID: "history", TodoID: "done", Title: "Completed task"},
			{MachineID: "mac", SessionID: "active", TodoID: "old", Title: "Older task"},
		},
	}
	before := len(ft.toldOf(assistantwire.EventFleetSessions))
	ft.s.fleetTakeSnapshot(Snapshot{Todos: report})
	closed, active := listed(t, ft.s, "mac", "history"), listed(t, ft.s, "mac", "active")
	if closed.Todo != nil || closed.CompletedTodo() == nil || closed.CompletedTodo().ID != "done" || closed.Quiet {
		t.Fatalf("completed ownership changed active/quiet state: %+v", closed)
	}
	if active.Todo == nil || active.Todo.ID != "current" || active.CompletedTodo() != nil {
		t.Fatalf("history displaced current work: %+v", active)
	}
	if got := ft.s.withTask(proto.Interaction{MachineID: "mac", SessionID: "history"}).TaskTitle; got != "" {
		t.Fatalf("completed ownership named a supervised task: %q", got)
	}
	ft.s.fleetTakeSnapshot(Snapshot{Todos: &TodoReport{}})
	if got := listed(t, ft.s, "mac", "history"); got.CompletedTodo() != nil {
		t.Fatalf("a withdrawn association remained on the session: %+v", got.CompletedTodo())
	}
	if len(ft.toldOf(assistantwire.EventFleetSessions)) < before+2 {
		t.Fatal("changing completed ownership did not refresh the session list")
	}
}
