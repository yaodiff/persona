package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestTodoViewRoutesKeepCloseoutAndAcceptanceSeparate(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		ctx := context.Background()
		old := time.Now().UTC().Add(-2 * time.Hour)
		for _, status := range []string{proto.CloseoutNotDone, proto.CloseoutDone} {
			id := "closeout-" + status
			if _, err := e.s.insertTodo(ctx, db.TodoRow{ID: id, Title: "Example " + status,
				CreatedAt: old, UpdatedAt: old}); err != nil {
				t.Fatal(err)
			}
			e.must("PUT", "/api/v1/todos/"+id+"/agents/server/"+status, "")
			if _, _, err := e.s.updateSupervision(ctx, id, func(v *Supervision) bool {
				v.State = reviewPending
				return true
			}); err != nil {
				t.Fatal(err)
			}
		}
		e.sessions("server",
			proto.Session{SessionID: proto.CloseoutNotDone, State: proto.StateUnread, Status: proto.StatusIdle,
				StateSince: old, UpdatedAt: old, Work: &proto.WorkState{TurnAt: old,
					Closeout: &proto.Closeout{Status: proto.CloseoutNotDone}}},
			proto.Session{SessionID: proto.CloseoutDone, State: proto.StateUnread, Status: proto.StatusIdle,
				StateSince: old, UpdatedAt: old, Work: &proto.WorkState{TurnAt: old,
					Closeout: &proto.Closeout{Status: proto.CloseoutDone}}})
		read := func() map[string]listedTodo {
			t.Helper()
			var list struct {
				Todos  []listedTodo
				Counts assistantwire.TodoCounts
			}
			if err := json.Unmarshal(e.must("GET", "/api/v1/todos", "").Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
			if list.Counts.Open != 2 || list.Counts.InProgress != 0 {
				t.Fatalf("two open tasks, neither actively executing: %+v", list.Counts)
			}
			rows := map[string]listedTodo{}
			for _, row := range list.Todos {
				if row.View == nil || row.DoneAt != nil || row.Supervision == nil || row.Supervision.State != reviewPending {
					t.Fatalf("a view cannot accept a task: %+v", row)
				}
				var item struct{ Todo listedTodo }
				if err := json.Unmarshal(e.must("GET", "/api/v1/todos/"+row.ID, "").Body.Bytes(), &item); err != nil {
					t.Fatal(err)
				}
				if item.Todo.View == nil || item.Todo.View.Reason != row.View.Reason {
					t.Fatalf("list and item disagree: %+v / %+v", row.View, item.Todo.View)
				}
				rows[row.ID] = row
			}
			return rows
		}
		rows := read()
		if rows["closeout-not_done"].View.Reason != assistantwire.TodoReasonStoppedUnfinished ||
			rows["closeout-done"].View.Section != assistantwire.TodoListSectionJustFinished {
			t.Fatalf("unfinished is Needs you; reported done is uncounted Just finished: %+v", rows)
		}
		now := time.Now().UTC()
		if _, _, err := e.s.updateSupervision(ctx, "closeout-not_done", func(v *Supervision) bool {
			v.FollowedUpAt = &now
			return true
		}); err != nil {
			t.Fatal(err)
		}
		if row := read()["closeout-not_done"]; row.View.Phase != assistantwire.TodoPhaseTodo {
			t.Fatalf("the new route must carry confirmed follow-up into the projection: %+v", row.View)
		}
	})
}
