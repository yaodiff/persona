package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// todoWakeSetup links one todo with an agent on box whose turn has a due wake and a still-open work card.
func todoWakeSetup(t *testing.T, s *Service) (todoID, cardID string) {
	t.Helper()
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "发 1.3 版", "", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1", "修登录", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(-time.Minute)
	cardID = "work_s1"
	s.putWork(ctx, db.SessionWork{MachineID: "box", SessionID: "s1", TurnAt: turnEnd, WakeAt: &due, InteractionID: cardID,
		Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, WakeAt: &due, InteractionID: cardID}})
	s.work.mu.Lock()
	s.work.cards[cardID] = proto.Interaction{ID: cardID, MachineID: "box", SessionID: "s1"}
	s.work.mu.Unlock()
	return todo.ID, cardID
}

func assertRetired(t *testing.T, s *Service, cardID string) {
	t.Helper()
	e := entryOf(t, s, "s1")
	if e.WakeAt != nil || e.Work.WakeAt != nil || e.InteractionID != "" || e.Work.InteractionID != "" {
		t.Fatalf("follow-up not retired: %+v", e)
	}
	s.work.mu.Lock()
	_, ok := s.work.cards[cardID]
	s.work.mu.Unlock()
	if ok {
		t.Fatal("the work card stays up")
	}
}

// While the todo is still open the due wake fires as always.
func TestOpenTodoWakeStillFires(t *testing.T) {
	s, f := workService(t)
	f.setSessions("box", stopped("s1", "我两分钟后再查一下"))
	todoWakeSetup(t, s)
	s.workTick(context.Background(), time.Now())
	if len(f.sentToAgents()) != 1 {
		t.Fatalf("open todo: sent %v", f.sentToAgents())
	}
}

// Marking the todo done withdraws the agent's due wake and work card, and a later tick sends nothing (#4489).
func TestTodoDoneRetiresTheWakeAndCard(t *testing.T) {
	s, f := workService(t)
	f.setSessions("box", stopped("s1", "我两分钟后再查一下"))
	id, cardID := todoWakeSetup(t, s)
	done := time.Now()
	if _, ok := s.applyTodoRow(context.Background(), id, db.TodoRow{ID: id, Title: "发 1.3 版", DoneAt: &done, UpdatedAt: done}, nil); !ok {
		t.Fatal("applyTodoRow did not apply")
	}
	assertRetired(t, s, cardID)
	s.workTick(context.Background(), time.Now().Add(time.Hour))
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("a finished todo woke its agent: %v", got)
	}
}

// A wake postponed while the machine was offline must not fire after the todo is completed and the machine returns.
func TestTodoDoneAfterAnOfflinePostponementDoesNotWake(t *testing.T) {
	s, f := workService(t)
	f.setSessions("box", stopped("s1", "我两分钟后再查一下"))
	id, cardID := todoWakeSetup(t, s)
	now := time.Now()
	f.devices = []Device{{ID: "box", Name: "box", Online: false, Reachable: false}}
	s.workTick(context.Background(), now) // offline: the due wake is postponed, not sent
	if e := entryOf(t, s, "s1"); e.WakeAt == nil || !e.WakeAt.After(now) || len(f.sentToAgents()) != 0 {
		t.Fatalf("postponement: %+v sent %v", e, f.sentToAgents())
	}
	done := now
	s.applyTodoRow(context.Background(), id, db.TodoRow{ID: id, Title: "发 1.3 版", DoneAt: &done, UpdatedAt: done}, nil)
	assertRetired(t, s, cardID)
	f.devices = []Device{{ID: "box", Name: "box", Online: true, Reachable: true}}
	s.workTick(context.Background(), now.Add(time.Hour)) // the machine is back: still nothing
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("the finished todo's postponed wake fired: %v", got)
	}
}

// A todo whose agent failed to start retires the same follow-ups (#4489).
func TestFailTodoRetiresTheWake(t *testing.T) {
	s, f := workService(t)
	f.setSessions("box", stopped("s1", "我两分钟后再查一下"))
	id, cardID := todoWakeSetup(t, s)
	if err := s.failTodo(context.Background(), id, "启动失败"); err != nil {
		t.Fatal(err)
	}
	assertRetired(t, s, cardID)
	s.workTick(context.Background(), time.Now().Add(time.Hour))
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("failTodo woke the agent: %v", got)
	}
}

// Reopening the todo leaves a newly scheduled follow-up alone: only the open to done transition retires (#4489).
func TestReopenDoesNotRetireANewWake(t *testing.T) {
	s, f := workService(t)
	f.setSessions("box", stopped("s1", "我两分钟后再查一下"))
	id, cardID := todoWakeSetup(t, s)
	done := time.Now()
	s.applyTodoRow(context.Background(), id, db.TodoRow{ID: id, Title: "发 1.3 版", DoneAt: &done, UpdatedAt: done}, nil)
	assertRetired(t, s, cardID)
	next := time.Now().Add(time.Minute)
	s.putWork(context.Background(), db.SessionWork{MachineID: "box", SessionID: "s1", TurnAt: turnEnd.Add(time.Minute),
		WakeAt: &next, Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, WakeAt: &next}})
	reopened := time.Now()
	s.applyTodoRow(context.Background(), id, db.TodoRow{ID: id, Title: "发 1.3 版", ReopenedAt: &reopened, UpdatedAt: reopened}, nil)
	if e := entryOf(t, s, "s1"); e.WakeAt == nil {
		t.Fatalf("the new wake was retired on reopen: %+v", e)
	}
}
