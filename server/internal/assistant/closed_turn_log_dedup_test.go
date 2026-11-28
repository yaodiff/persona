package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A reused long-lived session linked to an accepted task must log "left closed" and "ended a turn: dropped" once per
// close cycle rather than spamming identical lines on every subsequent unrelated turn (#16421). Reopening and later
// re-accepting the task produces a new DoneAt and logs fresh once.
func TestClosedTaskTurnLogsDeduplicatedPerCloseCycle(t *testing.T) {
	s, f, todo := doneAndReviewed(t)
	_ = reviewAndWait(t, s, todo.ID, reviewAccepted)
	var logs logBuffer
	s.logf = logs.logf

	ctx := context.Background()
	for i := 0; i < 4; i++ {
		f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
			Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
		s.taskWorking(ctx, "box", "s1", startRow{})
		f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
			Status: proto.StatusDone, State: proto.StateUnread, LastText: fmt.Sprintf("unrelated %d", i), UpdatedAt: time.Now()})
		s.taskTurnEnded(ctx, "box", "s1")
	}

	leftClosedLine := fmt.Sprintf("task %s: accepted; its agent works again, but nothing was sent for the task: left closed", todo.ID)
	droppedLine := fmt.Sprintf("task %s: closed; its agent box/s1 ended a turn: dropped", todo.ID)
	if got := strings.Count(logs.String(), leftClosedLine); got != 1 {
		t.Fatalf("left closed logged %d times, want 1:\n%s", got, logs.String())
	}
	if got := strings.Count(logs.String(), droppedLine); got != 1 {
		t.Fatalf("ended a turn: dropped logged %d times, want 1:\n%s", got, logs.String())
	}
	if got := todoNow(s, todo.ID); got.DoneAt == nil {
		t.Fatalf("unrelated turns reopened accepted task: %+v", got)
	}

	// Reopening and re-accepting creates a new DoneAt, so the first ignored turn of the new close cycle logs once again.
	if err := s.reopenTodo(ctx, todo.ID); err != nil {
		t.Fatalf("reopenTodo: %v", err)
	}
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "Second pass done: PR https://github.com/o/r/pull/99 is merged", 0)
	waitFor(t, "pending review after reopen", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	reaccepted := reviewAndWait(t, s, todo.ID, reviewAccepted)

	for i := 0; i < 3; i++ {
		f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
			Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
		s.taskWorking(ctx, "box", "s1", startRow{})
		f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
			Status: proto.StatusDone, State: proto.StateUnread, LastText: fmt.Sprintf("post-reaccept %d", i), UpdatedAt: time.Now()})
		s.taskTurnEnded(ctx, "box", "s1")
	}

	if got := strings.Count(logs.String(), leftClosedLine); got != 2 {
		t.Fatalf("left closed after re-accept logged %d times, want 2:\n%s", got, logs.String())
	}
	if got := strings.Count(logs.String(), droppedLine); got != 2 {
		t.Fatalf("ended a turn: dropped after re-accept logged %d times, want 2:\n%s", got, logs.String())
	}

	// Explicit follow-up send still reopens the accepted task even while its closed-turn dedup key is present (#6726/#7539).
	if reaccepted.DoneAt == nil {
		t.Fatalf("expected reaccepted DoneAt to be set")
	}
	note, err := s.noteAgentSend(ctx, todo.Origin, "box", "s1", "", "Please follow up on this task")
	if err != nil {
		t.Fatalf("noteAgentSend: %v", err)
	}
	note.sent(ctx, nil)
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
	s.taskWorking(ctx, "box", "s1", startRow{})
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.ReopenedAt == nil {
		t.Fatalf("explicit agent_send did not reopen task after closed-turn dedup: %+v", got)
	}
}

func TestClosedTurnLoggedCapBoundsMemory(t *testing.T) {
	s := &Service{}
	now := time.Now().UTC()
	for i := 0; i < closedTurnLoggedCap+10; i++ {
		td := Todo{ID: fmt.Sprintf("todo_%04d", i), DoneAt: &now}
		if !s.shouldLogClosedTurn(td, "box", "s1", true) {
			t.Fatalf("first call for %s unexpectedly suppressed", td.ID)
		}
		if s.shouldLogClosedTurn(td, "box", "s1", true) {
			t.Fatalf("repeat call for %s was not suppressed", td.ID)
		}
	}
	s.tasks.mu.Lock()
	n := len(s.tasks.closedTurnLogged)
	s.tasks.mu.Unlock()
	if n > closedTurnLoggedCap {
		t.Fatalf("closedTurnLogged grew to %d > cap %d", n, closedTurnLoggedCap)
	}
}
