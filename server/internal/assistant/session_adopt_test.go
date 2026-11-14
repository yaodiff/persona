package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func adoptSession(id string, fn func(*proto.Session)) proto.Session {
	sess := proto.Session{SessionID: id, MachineID: "box", Agent: "claude", Launcher: "claude-code",
		Cwd: "/code/app", StartedByPersona: true, UpdatedAt: time.Now().UTC()}
	fn(&sess)
	return sess
}

// The hub's own start names a random session id: the reconciliation matches that exact id and never the fresh
// same-directory session the owner started by hand, however identical the two rows look.
func TestAdoptExactSessionIDIgnoresTheOwnerManualSession(t *testing.T) {
	s, f := newTaskService(t)
	dev := Device{ID: "box"}
	known := map[string]bool{"old": true}
	manual := adoptSession("manual-1", func(*proto.Session) {})
	want := adoptSession("want-1", func(*proto.Session) {})
	f.setSessions("box", adoptSession("old", func(*proto.Session) {}), manual, want)

	got, ok := s.adoptStartedSession(context.Background(), dev, "claude-code", "/code/app", "want-1", known)
	if !ok || got.SessionID != "want-1" {
		t.Fatalf("got %+v ok=%v, want the exact-id session", got, ok)
	}

	// The expected id never shows up (a node too old to honor session_id): the heuristic still must not take the
	// owner's session once another open todo has linked it.
	todo, err := s.createTodo(context.Background(), "主人自己的活", "手动会话", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(context.Background(), todo.ID, "box", "manual-1", "手动", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	f.setSessions("box", adoptSession("old", func(*proto.Session) {}), manual)
	if _, ok := s.adoptStartedSession(context.Background(), dev, "claude-code", "/code/app", "want-1", known); ok {
		t.Fatal("the owner's session on an open todo was adopted")
	}
}

// Exact match is unconditional: the row the start created can be starting (no fresh sign of life yet) and still wins.
func TestAdoptExactSessionIDMatchesWithoutHeuristicConditions(t *testing.T) {
	s, f := newTaskService(t)
	dev := Device{ID: "box"}
	known := map[string]bool{}
	// Same-cwd heuristic candidate, and the expected row that the heuristic would reject (stale, another launcher).
	heuristic := adoptSession("heuristic", func(*proto.Session) {})
	exact := adoptSession("exact-1", func(s *proto.Session) {
		s.UpdatedAt = time.Now().Add(-agentStartAdoptWindow - time.Hour)
		s.Launcher = "codex"
	})
	f.setSessions("box", heuristic, exact)

	got, ok := s.adoptStartedSession(context.Background(), dev, "claude-code", "/code/app", "exact-1", known)
	if !ok || got.SessionID != "exact-1" {
		t.Fatalf("got %+v ok=%v, want the exact-id session regardless of its shape", got, ok)
	}
}

// A nil snapshot means the pre-start list failed, not that exact-id reconciliation is impossible. It must disable
// only the heuristic: the assigned id still matches, while a different fresh session is never guessed (#7772).
func TestAdoptExactSessionIDWithoutPreStartSnapshot(t *testing.T) {
	s, f := newTaskService(t)
	shortAgentStartTimeout(t)
	dev := Device{ID: "box"}
	exact := adoptSession("exact-1", func(*proto.Session) {})
	other := adoptSession("other", func(*proto.Session) {})
	f.setSessions("box", other, exact)

	got, ok := s.adoptStartedSession(context.Background(), dev, "claude-code", "/code/app", "exact-1", nil)
	if !ok || got.SessionID != "exact-1" {
		t.Fatalf("got %+v ok=%v, want exact-id adoption without a pre-start snapshot", got, ok)
	}
	if _, ok := s.adoptStartedSession(context.Background(), dev, "claude-code", "/code/app", "missing", nil); ok {
		t.Fatal("a missing pre-start snapshot enabled heuristic adoption")
	}
}

// Two workspace-none tasks run in the same directory and have no workspace claim to separate them: with an old node
// (no session_id honored), task A's fallback must never take the session task B's open todo already links. Once the
// node lists A's own id, the exact match adopts it even though B's session sits right beside it.
func TestAdoptTwoWorkspaceNoneTasksDoNotCrossAdopt(t *testing.T) {
	s, f := newTaskService(t)
	dev := Device{ID: "box"}
	known := map[string]bool{}
	ctx := context.Background()

	todoB, err := s.createTodo(ctx, "任务 B", "同目录另一个任务", "conv1", "plan-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todoB.ID, "box", "sess-b", "B", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	sessionB := adoptSession("sess-b", func(*proto.Session) {})

	// Old node: only B's linked session is fresh in the directory. A adopts nothing instead of stealing B.
	f.setSessions("box", sessionB)
	if _, ok := s.adoptStartedSession(ctx, dev, "claude-code", "/code/app", "sess-a", known); ok {
		t.Fatal("task A adopted the session task B's open todo links")
	}

	// New node: A's own session appears; the exact id picks it and leaves B's alone.
	sessionA := adoptSession("sess-a", func(*proto.Session) {})
	f.setSessions("box", sessionB, sessionA)
	got, ok := s.adoptStartedSession(ctx, dev, "claude-code", "/code/app", "sess-a", known)
	if !ok || got.SessionID != "sess-a" {
		t.Fatalf("got %+v ok=%v, want task A's own session", got, ok)
	}
}

// The fallback keeps adopting an unlinked fresh session: the open-todo exclusion only removes sessions another todo
// already owns, not the session this start created.
func TestAdoptFallbackStillTakesAnUnlinkedSession(t *testing.T) {
	s, f := newTaskService(t)
	dev := Device{ID: "box"}
	known := map[string]bool{}
	ctx := context.Background()

	todoB, err := s.createTodo(ctx, "任务 B", "同目录另一个任务", "conv1", "plan-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todoB.ID, "box", "sess-b", "B", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	f.setSessions("box",
		adoptSession("sess-b", func(*proto.Session) {}),
		adoptSession("sess-new", func(*proto.Session) {}),
	)
	got, ok := s.adoptStartedSession(ctx, dev, "claude-code", "/code/app", "", known)
	if !ok || got.SessionID != "sess-new" {
		t.Fatalf("got %+v ok=%v, want the unlinked fresh session", got, ok)
	}
}

// A Claude start carries the hub's session id, so its reconciliation can match exactly; the id is a valid Claude
// session id shape (hex and dashes).
func TestAgentStartSendsASessionIDForClaude(t *testing.T) {
	s, f := newTaskService(t)
	if _, err := s.runTool(context.Background(), "server", "conv1", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`)); err != nil {
		t.Fatal(err)
	}
	starts := f.started()
	if len(starts) != 1 {
		t.Fatalf("starts %v", starts)
	}
	id, _ := starts[0]["session_id"].(string)
	if !validSessionIDShape(id) {
		t.Fatalf("the start carried no valid session id: %q", id)
	}
}

func validSessionIDShape(id string) bool {
	if len(id) < 8 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c == '-':
		default:
			return false
		}
	}
	return true
}

// failTodoLoadStore is a store whose todo book cannot be read: every other method delegates to the in-memory task
// store, so only ensureTodosLoaded fails.
type failTodoLoadStore struct {
	*profileTaskStore
	todoStore
	block <-chan struct{}
}

func (st *failTodoLoadStore) ListTodos(ctx context.Context) ([]db.TodoRow, error) {
	if st.block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-st.block:
		}
	}
	return nil, errors.New("the todo book is unavailable")
}

// When the open-todo book cannot be read the heuristic adopts nothing rather than guess: the exclusion that keeps a
// task from stealing another task's agent cannot be evaluated. Both the upfront read (no exact id) and the lazy read
// after a missing exact id fail closed.
func TestAdoptFailsClosedWhenTheTodoBookCannotLoad(t *testing.T) {
	st := &failTodoLoadStore{profileTaskStore: &profileTaskStore{
		memStore: &memStore{vals: map[string][]byte{}}, plans: map[string]db.TaskPlanRow{},
	}}
	f := newFakeHub()
	f.sessions["box"] = nil
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	s := newServiceOn(t, f, st)
	dev := Device{ID: "box"}
	known := map[string]bool{}
	fresh := adoptSession("sess-new", func(*proto.Session) {})
	f.setSessions("box", fresh)

	if _, ok := s.adoptStartedSession(context.Background(), dev, "claude-code", "/code/app", "", known); ok {
		t.Fatal("the heuristic adopted with an unreadable todo book")
	}
	if _, ok := s.adoptStartedSession(context.Background(), dev, "claude-code", "/code/app", "missing-id", known); ok {
		t.Fatal("the fallback after a missing exact id adopted with an unreadable todo book")
	}
}

// The post-budget reconciliation also reads the todo book for heuristic adoption. Its whole-call deadline must bound
// that database read, not only the session lists; otherwise a stuck store can block the task forever (#7772).
func TestTaskStartFinalAdoptionBoundsTheTodoLoad(t *testing.T) {
	release := make(chan struct{})
	st := &failTodoLoadStore{profileTaskStore: &profileTaskStore{
		memStore: &memStore{vals: map[string][]byte{}}, plans: map[string]db.TaskPlanRow{},
	}}
	f := newFakeHub()
	f.sessions["box"] = nil
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	s := newServiceOn(t, f, st)
	// Block only the final reconciliation's read, not service initialization.
	st.block = release
	oldWait, oldPoll, oldFinal := taskStartAdoptWait, taskStartAdoptPoll, taskStartFinalAdoptTimeout
	taskStartAdoptWait, taskStartAdoptPoll, taskStartFinalAdoptTimeout = 20*time.Millisecond, 5*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() {
		taskStartAdoptWait, taskStartAdoptPoll, taskStartFinalAdoptTimeout = oldWait, oldPoll, oldFinal
	})

	watchdog := time.AfterFunc(time.Second, func() { close(release) })
	_, ok := s.adoptTaskStartSessionUntil(context.Background(), Device{ID: "box"}, "claude-code", "/code/app", "", map[string]bool{}, time.Now().Add(20*time.Millisecond))
	bounded := watchdog.Stop()
	if ok {
		t.Fatal("a start with an unreadable todo book was adopted")
	}
	if !bounded {
		t.Fatal("the final reconciliation needed the test watchdog: its todo load had no whole-call deadline")
	}
}

// The documented fallback limit on an old node (one that ignored session_id): a session the owner started by hand is
// hub-started-shaped and, while it is on no open todo, indistinguishable from the session the lost start created. It
// can still be adopted there. This pins the boundary: linking that manual session to an open todo removes it, and an
// up-to-date node's exact-id path never reaches this guess (see the tests above).
func TestAdoptOldNodeFallbackCanTakeAnUnlinkedManualSession(t *testing.T) {
	s, f := newTaskService(t)
	dev := Device{ID: "box"}
	known := map[string]bool{}
	manual := adoptSession("manual-1", func(*proto.Session) {})
	f.setSessions("box", manual)

	got, ok := s.adoptStartedSession(context.Background(), dev, "claude-code", "/code/app", "id-the-old-node-ignored", known)
	if !ok || got.SessionID != "manual-1" {
		t.Fatalf("got %+v ok=%v: the old-node fallback keeps adopting an unlinked same-shaped session", got, ok)
	}
}
