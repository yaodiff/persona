package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// heldDigest holds the first digest of s between taking its batch out of the gather and marking it sent
// (digestTakenHook) and returns once it is held there. let lets it go on; the test's end lets it go too, before the
// fixture drains its timers. Later digests pass.
func heldDigest(t *testing.T, s *Service, raise func()) (let func()) {
	t.Helper()
	taken, release := make(chan struct{}), make(chan struct{})
	var held, freed sync.Once
	s.digestTakenHook = func() {
		held.Do(func() {
			close(taken)
			<-release
		})
	}
	let = func() { freed.Do(func() { close(release) }) }
	t.Cleanup(let)
	raise()
	select {
	case <-taken:
	case <-time.After(5 * time.Second):
		t.Fatal("no digest took the signal")
	}
	return let
}

// settledDigests waits until nothing waits in s's gather and no digest is running, and checks that no digest left a
// key marked taken behind it.
func settledDigests(t *testing.T, s *Service) {
	t.Helper()
	b := &s.supervise
	waitFor(t, "the gather to empty", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.pending) == 0
	})
	// The digest that took the last signal holds digestMu until it is done.
	b.digestMu.Lock()
	defer b.digestMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.taken) != 0 {
		t.Fatalf("still taken after every digest finished: %v", b.taken)
	}
}

// withMainConversation gives s the quiet fixture's main conversation (conv1 on machine server, idle): without one,
// a digest leaves everything waiting for it and takes nothing.
func withMainConversation(t *testing.T, s *Service, f *fakeHub) {
	t.Helper()
	f.mu.Lock()
	f.hello.Assistant.MachineID = "server"
	f.known["conv1"] = true
	f.mu.Unlock()
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
}

// failingStartTodo is a to-do the main conversation (conv1) supervises, with its failed start signal.
func failingStartTodo(t *testing.T, s *Service) (Todo, superviseSignal) {
	t.Helper()
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "甲", "做甲", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, todo.ID, "conv1", []string{"PR 合并"}, 0); err != nil {
		t.Fatal(err)
	}
	return todo, superviseSignal{Todo: todo.ID, Kind: sigFailed, Key: "x", Detail: "box 离线"}
}

// reviewWaitingTodo is a to-do the main conversation (conv1) supervises that waits for its review, with its review
// signal. Reviews have no handling of their own between gathering and delivery (failed starts do: start_remedy.go).
func reviewWaitingTodo(t *testing.T, s *Service) (Todo, superviseSignal) {
	t.Helper()
	todo, _ := failingStartTodo(t, s)
	if _, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	return todo, superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "r1", Detail: "做完了"}
}

// A signal its digest has taken is told once, even when the same signal is raised again before that digest marked it
// sent (#17838): what a later digest would find again must not reach the conversation a second time.
func TestDigestTellsATakenSignalOnce(t *testing.T) {
	retold := func(g superviseSignal) superviseSignal { g.Detail = "做完了，PR 也更新了"; return g }
	for _, c := range []struct {
		name   string
		again  func(g superviseSignal) superviseSignal
		inside bool // raised again while the digest is held between taking and marking it sent
	}{
		{"same signal while taken", func(g superviseSignal) superviseSignal { return g }, true},
		{"same key, new text while taken", retold, true},
		{"same key, new text once sent", retold, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := newQuietServiceOn(t, &memStore{})
			_, g := reviewWaitingTodo(t, s)
			let := heldDigest(t, s, func() { s.signal(g) })
			b := &s.supervise
			b.mu.Lock()
			_, waiting := b.pending[g.id()]
			key := b.taken[g.id()]
			b.mu.Unlock()
			if waiting || key != g.Key {
				t.Fatalf("while held: pending %v, taken %q; want it taken under %q", waiting, key, g.Key)
			}
			if c.inside {
				s.signal(c.again(g))
			}
			let()
			waitFor(t, "the message", func() bool { return len(f.notices()) >= 1 })
			if !c.inside {
				s.signal(c.again(g))
			}
			settledDigests(t, s)
			if n := f.notices(); len(n) != 1 {
				t.Fatalf("told %d times:\n%q", len(n), n)
			}
		})
	}
}

// The taken mark never outlives the digest that set it (#17838). A manager that stops while a digest holds the signal
// leaves nothing behind: the next one restores the signal from the outbox and tells the key. A digest that puts the
// signal back (the deduplication history cannot be read, or the idle waits for its Agent's own wake) lets the same key
// be raised and told again, and a handover can tell the held key to its new supervisor.
func TestDigestTakenKeyIsToldAfterARestartOrAPutBack(t *testing.T) {
	t.Run("restart while taken", func(t *testing.T) {
		st := &memStore{}
		first, _ := newQuietServiceOn(t, st)
		todo, g := reviewWaitingTodo(t, first)
		heldDigest(t, first, func() { first.signal(g) })
		// The first manager never gets past the hold; a new one comes up over the same database. Its digest is held
		// until it has the task, which this fixture keeps in memory only (a real one loads it from the database).
		next, f := newQuietServiceTimed(t, st, heldSuperviseTimings())
		putTodo(next, todoNow(first, todo.ID))
		g.Detail = "做完了，PR 也更新了"
		next.signal(g)
		nb := &next.supervise
		nb.mu.Lock()
		cur, waiting := nb.pending[g.id()]
		taken := len(nb.taken)
		nb.mu.Unlock()
		if !waiting || cur.Detail != g.Detail || taken != 0 {
			t.Fatalf("after the restart the key was not taken up again: %+v (waiting %v, %d taken)", cur, waiting, taken)
		}
		flushSuperviseDigest(context.Background(), next)
		settledDigests(t, next)
		if n := f.notices(); len(n) != 1 || !strings.Contains(n[0], "PR 也更新了") {
			t.Fatalf("after the restart told %d times:\n%q", len(n), n)
		}
	})
	t.Run("put back while taken", func(t *testing.T) {
		st := &memStore{}
		s, f := newQuietServiceOn(t, st)
		_, g := reviewWaitingTodo(t, s)
		let := heldDigest(t, s, func() { s.signal(g) })
		st.failKey(superviseDedupKey, errors.New("database away"))
		let()
		b := &s.supervise
		waitFor(t, "the signal put back", func() bool {
			b.mu.Lock()
			defer b.mu.Unlock()
			_, back := b.pending[g.id()]
			return back && b.taken[g.id()] == "" && b.sent[g.id()] == ""
		})
		// The same key, raised again once it was put back, replaces what waits. digestMu is held so that no retrying
		// digest has it taken just then.
		b.digestMu.Lock()
		g.Detail = "做完了，PR 也更新了"
		s.signal(g)
		st.failKey(superviseDedupKey, nil)
		b.digestMu.Unlock()
		waitFor(t, "the message", func() bool { return len(f.notices()) >= 1 })
		settledDigests(t, s)
		if n := f.notices(); len(n) != 1 || !strings.Contains(n[0], "PR 也更新了") {
			t.Fatalf("after the put-back told %d times:\n%q", len(n), n)
		}
	})
	t.Run("put back by the digest that holds it", func(t *testing.T) {
		// A digest that puts a signal back before it is done (screened, or its group not known yet) uses restoreDigest:
		// the signal is untold again at once, so the same key raised meanwhile replaces it in pending.
		s, _ := newQuietServiceOn(t, &memStore{})
		setSuperviseTimingsForTest(s, heldSuperviseTimings())
		_, g := failingStartTodo(t, s)
		b := &s.supervise
		b.mu.Lock()
		b.init()
		b.taken[g.id()] = g.Key
		b.mu.Unlock()
		s.restoreDigest([]superviseSignal{g}, time.Now(), time.Time{})
		g.Detail = "box 还是离线"
		s.signal(g)
		b.mu.Lock()
		cur, waiting := b.pending[g.id()]
		taken := b.taken[g.id()]
		b.mu.Unlock()
		if !waiting || cur.Detail != g.Detail || taken != "" {
			t.Fatalf("put back but still taken: %+v (waiting %v, taken %q)", cur, waiting, taken)
		}
	})
	t.Run("handover while taken", func(t *testing.T) {
		s, _ := newQuietServiceOn(t, &memStore{})
		todo, review := reviewWaitingTodo(t, s)
		heldDigest(t, s, func() { s.signal(review) })
		// A handover tells the same review key to its new supervisor on purpose (signalHandedOverReview).
		s.signalHandedOverReview(todo.ID, "conv2", review.Key, "主对话换了")
		b := &s.supervise
		b.mu.Lock()
		cur, waiting := b.pending[review.id()]
		b.mu.Unlock()
		if !waiting || cur.Recipient != "conv2" {
			t.Fatalf("the handed-over review was refused while its old copy was taken: %+v (waiting %v)", cur, waiting)
		}
	})
	t.Run("withheld for its wake while taken", func(t *testing.T) {
		// A digest that puts its only signal back (an idle whose Agent wakes itself later) returns before it marks
		// anything sent: nothing stays taken, and the same key still replaces what waits.
		s, f, todo, c, _ := mechanicalFixture(t)
		setSuperviseTimingsForTest(s, heldSuperviseTimings())
		withMainConversation(t, s, f)
		c.Status, c.State = proto.StatusWorking, proto.StateWorking
		c.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: time.Now().Add(time.Hour)}
		f.setSessions("box", c)
		idle := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "turn-1", Machine: c.MachineID, Session: c.SessionID}
		took := 0
		s.digestTakenHook = func() { took++ } // the flush below runs the digest on this goroutine
		s.signal(idle)
		flushSuperviseDigest(context.Background(), s)
		if took != 1 {
			t.Fatalf("the digest took its batch %d times, want once", took)
		}
		b := &s.supervise
		b.mu.Lock()
		_, waiting := b.pending[idle.id()]
		taken, sent := len(b.taken), b.sent[idle.id()]
		b.mu.Unlock()
		if !waiting || taken != 0 || sent != "" {
			t.Fatalf("after the digest withheld it: waiting %v, %d taken, sent %q", waiting, taken, sent)
		}
		idle.Detail = "还在等它自己醒"
		s.signal(idle)
		b.mu.Lock()
		cur := b.pending[idle.id()]
		b.mu.Unlock()
		if cur.Detail != idle.Detail {
			t.Fatalf("the same key raised again was refused: %+v", cur)
		}
		if n := f.notices(); len(n) != 0 {
			t.Fatalf("a withheld idle was told: %q", n)
		}
	})
	t.Run("handled without a model while taken", func(t *testing.T) {
		// A digest whose only signal is settled without a model (an evidenced closeout parks the task) tells nothing and
		// puts nothing back: it returns before marking anything sent, and its own release leaves nothing taken.
		s, f, task, c, _ := automaticFixture(t, automaticCloseout("deployment rollout-42", "build https://example.org/deployments/rollout-42", nil))
		setSuperviseTimingsForTest(s, heldSuperviseTimings())
		withMainConversation(t, s, f)
		s.supervise.mu.Lock()
		s.supervise.last = time.Time{} // automaticFixture's holdDigest put the last message an hour ahead
		s.supervise.mu.Unlock()
		idle := superviseSignal{Todo: task.ID, Kind: sigIdle, Key: "turn-1", Machine: c.MachineID, Session: c.SessionID}
		took := 0
		s.digestTakenHook = func() { took++ } // the flush below runs the digest on this goroutine
		s.signal(idle)
		flushSuperviseDigest(context.Background(), s)
		if took != 1 {
			t.Fatalf("the digest took its batch %d times, want once", took)
		}
		b := &s.supervise
		b.mu.Lock()
		_, waiting := b.pending[idle.id()]
		taken, sent := len(b.taken), b.sent[idle.id()]
		b.mu.Unlock()
		if waiting || taken != 0 || sent != "" {
			t.Fatalf("after the digest settled it: waiting %v, %d taken, sent %q", waiting, taken, sent)
		}
		if a := automaticMetadata(t, todoNow(s, task.ID).Supervision); a["count"] != float64(1) {
			t.Fatalf("the fixture did not park the task without a model: %+v", a)
		}
		if n := f.notices(); len(n) != 0 {
			t.Fatalf("a settled idle was told: %q", n)
		}
	})
}
