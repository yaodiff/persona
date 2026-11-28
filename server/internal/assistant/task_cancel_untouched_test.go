package assistant

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestSupervisorCancelsUntouchedViewedDispatch(t *testing.T) {
	for _, tc := range []string{"memory", "postgres"} {
		t.Run(tc, func(t *testing.T) {
			var store Store
			if tc == "postgres" {
				store = todoTestDB(t)
			}
			s, f := newTaskServiceOn(t, store)
			setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
			todo := startedTask(t, s, f)
			f.setItems(untouchedDispatchPrompt(t, s, todo),
				proto.Item{Role: "assistant", Kind: proto.KindToolUse, ToolName: "Read", ToolUseID: "read"},
				proto.Item{Role: "user", Kind: proto.KindToolResult, ToolUseID: "read"})
			setTaskViewer(t, s, "box", "s1", true)
			s.markSeen(t.Context(), "box", "s1")
			got, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "cancelled"})
			if err != nil || !strings.Contains(got, "cancelled and closed") {
				t.Fatalf("cancellation while viewed: %q, %v", got, err)
			}
			after := todoNow(s, todo.ID)
			if after.DoneAt == nil || after.DoneBy != db.DoneByCancelled || len(f.retires()) != 0 {
				t.Fatalf("closure or live Agent state: %+v, retirements: %v", after, f.retires())
			}
			// The separate guard still protects the Agent from automatic retirement.
			if why := s.sessionActivityReason(t.Context(), "box", "s1", after.CreatedAt); !strings.Contains(why, "viewing Agent") {
				t.Fatalf("retirement lost the viewer protection: %q", why)
			}
		})
	}
}

func TestViewedDispatchCancellationPreservesWorkAndOwnerInput(t *testing.T) {
	for _, tc := range []string{"owner event", "owner transcript", "assistant answer", "done report", "delivered file", "unknown history"} {
		t.Run(tc, func(t *testing.T) {
			s, f := newTaskService(t)
			setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
			todo := startedTask(t, s, f)
			items := []proto.Item{untouchedDispatchPrompt(t, s, todo)}
			setTaskViewer(t, s, "box", "s1", true)
			switch tc {
			case "owner event":
				s.UserSpoke(t.Context(), SessionRef{MachineID: "box", SessionID: "s1"})
			case "owner transcript":
				items = append(items, proto.Item{ID: "owner", Role: "user", Kind: proto.KindText, Text: "Keep this", ClientMsgID: "owner-input"})
			case "assistant answer":
				items = append(items, proto.Item{ID: "answer", Role: "assistant", Kind: proto.KindText, Text: "Here is the result."})
			case "done report":
				items = append(items, proto.Item{ID: "report", Role: "assistant", Kind: proto.KindToolUse,
					ToolName: "report_to_supervisor", ToolInput: []byte(`{"kind":"done","text":"Checks passed."}`)})
			case "delivered file":
				s.keepTaskFiles(todo.ID, []taskFile{{Name: "result.txt", Path: "/tmp/result.txt"}})
			case "unknown history":
				items = nil
			}
			f.setItems(items...)
			_, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "cancelled"})
			if err == nil || !strings.Contains(err.Error(), "cannot be closed") || todoNow(s, todo.ID).DoneAt != nil {
				t.Fatalf("viewer protection was lost: %v", err)
			}
		})
	}
}

func TestUntouchedCancellationRechecksOwnerInputAtCommit(t *testing.T) {
	s, f := newQuietService(t)
	todo := startedTask(t, s, f)
	f.setItems(untouchedDispatchPrompt(t, s, todo))
	setTaskViewer(t, s, "box", "s1", true)
	args, err := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": reviewCancelled})
	if err != nil {
		t.Fatal(err)
	}

	s.todos.writing.Lock()
	unlock := sync.OnceFunc(s.todos.writing.Unlock)
	defer unlock()
	done := make(chan error, 1)
	go func() {
		_, err := s.toolTaskReview(t.Context(), "conv1", args)
		done <- err
	}()
	waitFor(t, "the cancellation commit boundary", func() bool {
		for _, stack := range goroutineStacks() {
			if strings.Contains(stack, "[sync.Mutex.Lock]") &&
				strings.Contains(stack, "(*Service).updateSupervisionGuardedWithClose") &&
				strings.Contains(stack, "TestUntouchedCancellationRechecksOwnerInputAtCommit.func") {
				return true
			}
		}
		return false
	})
	s.rememberSessionActivity(t.Context(), "box", "s1")
	unlock()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "cannot be closed") || todoNow(s, todo.ID).DoneAt != nil {
		t.Fatalf("late owner input did not hold the task open: %v", err)
	}
}

func untouchedDispatchPrompt(t *testing.T, s *Service, todo Todo) proto.Item {
	t.Helper()
	plan, err := s.loadPlan(t.Context(), todo.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	task := plan.Tasks[0]
	if task.TodoID != todo.ID {
		t.Fatalf("plan task %q does not match to-do %q", task.TodoID, todo.ID)
	}
	return proto.Item{ID: "dispatch", Role: "user", Kind: proto.KindText, Text: task.Prompt + "\n\nTask instructions."}
}
