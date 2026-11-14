package assistant

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The task lock (s.tasks.mu) is the package's hot lock: starting tasks, routing messages, cards, stages, the
// workspace. The turns' ledger is read and written in the database with its own mutex held (task_turns.go), so
// nothing may wait for that mutex while it holds the task lock: one ledger write that does not come back would park
// every one of those behind it, for as long as the write's context allows (#5718, the second review).

// ledgerStalls is a database whose write of the turns' ledger does not come back while stalled is set: a pool with
// no connection left, a Postgres that restarts. waits gets a value when such a write arrives; answer, once closed,
// lets them through.
type ledgerStalls struct {
	*memStore
	stalled atomic.Bool
	waits   chan struct{}
	answer  chan struct{}
}

func (m *ledgerStalls) PutSetting(ctx context.Context, key string, v any) error {
	if key == taskTurnsKey && m.stalled.Load() {
		select {
		case m.waits <- struct{}{}:
		default:
		}
		select {
		case <-m.answer:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.memStore.PutSetting(ctx, key, v)
}

// comesBack runs do and says whether it returned within d. One that did not is still running: the test lets go of
// what it waits for before it ends.
func comesBack(d time.Duration, do func()) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		do()
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// With a ledger write stuck in the database, what reads or sets the "an end is being handled" mark under the task
// lock still comes back at once, and so does a caller that needs the task lock alone.
func TestTaskLockDoesNotWaitForALedgerWrite(t *testing.T) {
	st := &ledgerStalls{memStore: &memStore{}, waits: make(chan struct{}, 1), answer: make(chan struct{})}
	s, _ := newTaskServiceOn(t, st)
	ctx := context.Background()
	s.turnOf(ctx, "") // the ledger is read, and kept for the first time, while the database still answers
	var once sync.Once
	answers := func() { once.Do(func() { close(st.answer) }) }
	defer answers()

	st.stalled.Store(true)
	wrote := make(chan struct{})
	go func() { // a turn is taken up, and its ledger write does not come back
		defer close(wrote)
		s.takeTurn(ctx, Todo{ID: "todo_a"}, turnRow(proto.StatusIdle, 3, "修好了"), nil, turnClose{Key: "turn-1"}, true, time.Now(), turnDone)
	}()
	select {
	case <-st.waits:
	case <-time.After(3 * time.Second):
		t.Fatal("the ledger write did not reach the database")
	}
	for _, c := range []struct {
		what string
		do   func()
	}{
		{"a look asking whether a start was heard, under the task lock (turnStartHeard)", func() { s.turnStartHeard("todo_b") }},
		{"an end's mark, set with its reservation under the task lock (taskJudging)", func() {
			s.taskJudging("todo_b", 1)
			s.taskJudging("todo_b", -1)
		}},
		{"a caller that needs the task lock alone (taskWorkCurrent)", func() { s.taskWorkCurrent("todo_b", time.Now()) }},
	} {
		if !comesBack(time.Second, c.do) {
			t.Errorf("%s waited for the ledger's database write", c.what)
		}
	}
	select {
	case <-wrote:
		t.Fatal("the ledger write came back while the database did not answer: nothing was held")
	default:
	}

	answers()
	select {
	case <-wrote:
	case <-time.After(3 * time.Second):
		t.Fatal("the ledger write did not come back once the database answered")
	}
	if e, ok := s.turnOf(ctx, "todo_a"); !ok || e.Key != "turn-1" || e.State != turnDone {
		t.Fatalf("the turn on the ledger after the write = %+v, %v", e, ok)
	}
}

// The same on the path that takes an end up. A turn that ended unheard is found by the sweep; while its end's own
// look goes from its transcript read to its receipt's reservation, the ledger's mutex is taken and kept, as a write
// that does not come back keeps it. The reservation is still made, with its mark, the task lock stays free for
// everybody else, and the turn gets its receipt, its judgment and its message once the ledger answers.
func TestTurnEndReservesWhileTheLedgerIsHeld(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	sweeps(t, s, todo.ID, 1)
	f.setItems(turnPage("turn-1", "修好了", true)...)
	nodeListed(s, f, turnRow(proto.StatusIdle, 3, "修好了"))

	b := &s.tasks.turns
	held := make(chan struct{})
	var hold, free sync.Once
	f.hookReads(receiptHooks{after: func(read int) {
		if read == 2 { // the end's own read (settleTurn): nothing asks the ledger from here to the reservation
			hold.Do(func() {
				b.mu.Lock()
				close(held)
			})
		}
	}})
	s.superviseWatch(context.Background(), time.Now())
	select {
	case <-held:
	case <-time.After(3 * time.Second):
		t.Fatal("the sweep did not take the turn up")
	}
	answers := func() { free.Do(b.mu.Unlock) }
	defer answers()

	// The end reaches its reservation: a look at the same task, which reads the reservation and the mark under the
	// task lock, sees it being handled. No such look waits for the ledger on the way there. What goes wrong is said
	// after the ledger answers and the end was handled, so that nothing of this test is left running.
	stuck := ""
	deadline := time.Now().Add(3 * time.Second)
	pace := time.NewTicker(time.Millisecond)
	defer pace.Stop()
	for settling := false; !settling && stuck == ""; {
		if !comesBack(time.Second, func() { _, settling = s.turnStartHeard(todo.ID) }) {
			stuck = "the task lock waits for the ledger: a look at the task did not come back while the ledger was held"
		} else if !settling && time.Now().After(deadline) {
			stuck = "the end did not reach its reservation while the ledger was held"
		}
		if !settling && stuck == "" {
			<-pace.C
		}
	}
	if stuck == "" && !comesBack(time.Second, func() { s.taskWorkCurrent(todo.ID, time.Now()) }) {
		stuck = "the task lock is kept by an end that waits for the ledger"
	}
	var during turnCounts
	if stuck == "" {
		during = countTurns(t, s, todo.ID, f)
	}

	answers()
	wantTurns(t, "once the ledger answers", oneEach, s, todo.ID, f)
	turnsQuiet(t, s, todo.ID)
	if stuck != "" {
		t.Fatal(stuck)
	}
	if during != (turnCounts{}) {
		t.Fatalf("handled while the ledger was held: %+v", during)
	}
	sweeps(t, s, todo.ID, 3)
	stillTurns(t, "later ticks", oneEach, s, todo.ID, f)
}
