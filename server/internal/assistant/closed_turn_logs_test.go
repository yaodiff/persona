package assistant

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestClosedTaskTurnLogsOncePerAgentAndClosure(t *testing.T) {
	ctx := context.Background()
	var logs logBuffer
	s := New(Options{Hub: newFakeHub(), Logf: logs.logf})
	done := time.Now().Add(-time.Hour)
	const id = "todo_report"
	agents := []TodoAgent{
		{MachineID: "box", SessionID: "shared01-a"},
		{MachineID: "box", SessionID: "shared01-b"},
		{MachineID: "laptop", SessionID: "shared01-a"},
	}
	putTodo(s, Todo{ID: id, Title: "Review the report", Origin: "conv1", DoneAt: &done, DoneBy: db.DoneByReview, Agents: agents})

	start := func(agent TodoAgent) {
		s.taskWorking(ctx, agent.MachineID, agent.SessionID, startRow{})
	}
	end := func(agent TodoAgent) {
		t.Helper()
		if result := s.settleTurn(ctx, agent.MachineID, agent.SessionID, ""); result != settleRefused {
			t.Fatalf("late turn end on %s/%s returned %v, want settleRefused", agent.MachineID, agent.SessionID, result)
		}
	}
	check := func(wantStarts, wantEnds int) {
		t.Helper()
		lines := logs.String()
		starts := strings.Count(lines, "left closed")
		ends := strings.Count(lines, "ended a turn: dropped")
		if starts != wantStarts || ends != wantEnds {
			t.Errorf("logged %d ignored starts and %d dropped ends, want %d and %d", starts, ends, wantStarts, wantEnds)
		}
	}

	// A turn running when the task closed ends before we see another start.
	end(agents[0])
	end(agents[0])
	check(0, 1)
	for range 2 {
		for _, agent := range agents {
			start(agent)
			end(agent)
		}
	}
	check(3, 3)
	if got := todoNow(s, id); got.DoneAt == nil || !got.DoneAt.Equal(done) {
		t.Fatalf("unrelated turns changed the task's closure: %+v", got)
	}

	if err := s.reopenTodo(ctx, id); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.reviewDoneTodo(ctx, id, "Reviewed again", true); !ok || err != nil {
		t.Fatalf("accepting the reopened task: ok=%t, err=%v", ok, err)
	}
	for range 2 {
		start(agents[0])
		end(agents[0])
	}
	check(4, 4)
}

func TestClosedTurnLogKeepsRecentEntriesWithinBound(t *testing.T) {
	var tasks tasksState
	done := time.Now()
	todo := Todo{ID: "todo_recent", DoneAt: &done}
	first := func(session string) bool {
		return tasks.firstClosedTurnLog(&todo, "box", session, ignoredClosedStart)
	}
	for i := range maxClosedTurnLogs {
		if !first(strconv.Itoa(i)) {
			t.Fatalf("new session %d was suppressed", i)
		}
	}
	if first("0") {
		t.Fatal("logged a repeated start")
	}
	if !first("new") || first("0") || !first("1") {
		t.Fatal("adding a new entry should evict the least recently seen diagnostic")
	}
	if n := len(tasks.closedTurnLogs); n != maxClosedTurnLogs {
		t.Fatalf("retained %d diagnostics, want %d", n, maxClosedTurnLogs)
	}
	todo.ID = "todo_other"
	if !first("0") {
		t.Fatal("suppressed a different task closed at the same instant")
	}
}

func TestClosedTurnLogConcurrentDuplicates(t *testing.T) {
	var tasks tasksState
	done := time.Now()
	todo := Todo{ID: "todo_shared", DoneAt: &done}
	const workers = 32
	start := make(chan struct{})
	results := make(chan bool, workers)
	for range workers {
		go func() {
			<-start
			results <- tasks.firstClosedTurnLog(&todo, "box", "session", droppedClosedEnd)
		}()
	}
	close(start)
	var logged int
	for range workers {
		if <-results {
			logged++
		}
	}
	if logged != 1 {
		t.Fatalf("logged %d concurrent ends, want 1", logged)
	}
}
