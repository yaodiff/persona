package devices

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func newTestWriter(t *testing.T, put nodeStateWrite) (*nodeStateWriter, context.CancelFunc) {
	t.Helper()
	lifecycle, cancel := context.WithCancel(context.Background())
	w := &nodeStateWriter{
		machineID:  "box",
		put:        put,
		lifecycle:  lifecycle,
		storeWait:  time.Second,
		backoffMin: time.Millisecond,
		backoffMax: 5 * time.Millisecond,
		drainWait:  200 * time.Millisecond,
		wake:       make(chan struct{}, 1),
		done:       make(chan struct{}),
		pending:    map[string]*nodeStatePending{},
	}
	return w, cancel
}

type writtenCall struct {
	session string
	state   db.NodeSession
}

// TestStateWriterLatestSessionWinsWithoutLosingCreationReceipt: while the first write is in flight, repeated newer
// complete states of the same session coalesce; after release the final write carries the newest values and the
// creation receipt must survive even when later states omit it.
func TestStateWriterLatestSessionWinsWithoutLosingCreationReceipt(t *testing.T) {
	var mu sync.Mutex
	var calls []writtenCall
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})

	w, cancel := newTestWriter(t, func(_ context.Context, _, session string, st db.NodeSession) error {
		mu.Lock()
		n := len(calls) + 1
		calls = append(calls, writtenCall{session, st})
		mu.Unlock()
		if n == 1 {
			close(firstEntered)
			<-releaseFirst
		}
		return nil
	})
	defer cancel()
	go w.run()

	w.enqueue("s1", db.NodeSession{Cwd: "/one", CreationID: "creation-1", Incarnation: "incarnation-1"})
	select {
	case <-firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first write never started")
	}
	// Newer complete states omit the receipt, as a node's periodic state.put can.
	w.enqueue("s1", db.NodeSession{Cwd: "/two"})
	w.enqueue("s1", db.NodeSession{Cwd: "/three"})
	close(releaseFirst)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(calls)
		mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(time.Millisecond) // sleep-ok: yield inside the deadline-bounded poll for the two coalesced writes
	}
	cancel()
	if !w.waitDone(2 * time.Second) {
		t.Fatal("writer did not stop")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("writes = %d, want 2 (first plus one coalesced latest), calls=%+v", len(calls), calls)
	}
	first, last := calls[0].state, calls[1].state
	if first.Cwd != "/one" {
		t.Fatalf("first write cwd = %q, want /one", first.Cwd)
	}
	if last.Cwd != "/three" {
		t.Fatalf("final write cwd = %q, want latest /three", last.Cwd)
	}
	if last.CreationID != "creation-1" || last.Incarnation != "incarnation-1" {
		t.Fatalf("creation receipt lost by coalescing: creation_id=%q incarnation=%q", last.CreationID, last.Incarnation)
	}
}

// Different sessions keep strict enqueue FIFO order even when the head write blocks.
func TestStateWriterDoesNotReorderDifferentSessions(t *testing.T) {
	var mu sync.Mutex
	var entered []string
	aEntered := make(chan struct{})
	releaseA := make(chan struct{})

	w, cancel := newTestWriter(t, func(_ context.Context, _, session string, _ db.NodeSession) error {
		mu.Lock()
		entered = append(entered, session)
		mu.Unlock()
		if session == "a" {
			close(aEntered)
			<-releaseA
		}
		return nil
	})
	defer cancel()
	go w.run()

	w.enqueue("a", db.NodeSession{Cwd: "/a"})
	select {
	case <-aEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("session a write never started")
	}
	w.enqueue("b", db.NodeSession{Cwd: "/b"})
	w.enqueue("c", db.NodeSession{Cwd: "/c"})

	// A correct single writer cannot have entered b while a is blocked. Give a broken fan-out implementation a
	// moment to reveal itself.
	select {
	case <-time.After(50 * time.Millisecond):
	case <-func() chan struct{} {
		ch := make(chan struct{})
		go func() {
			for {
				mu.Lock()
				n := len(entered)
				mu.Unlock()
				if n >= 2 {
					close(ch)
					return
				}
				time.Sleep(time.Millisecond) // sleep-ok: yield inside the poll that fails fast if a blocked head fans writes out concurrently
			}
		}()
		return ch
	}():
		mu.Lock()
		got := append([]string(nil), entered...)
		mu.Unlock()
		t.Fatalf("a later session was written while head a was still in flight (reordering/concurrency): order=%v", got)
	}

	close(releaseA)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(entered)
		mu.Unlock()
		if n == 3 {
			break
		}
		time.Sleep(time.Millisecond) // sleep-ok: yield inside the deadline-bounded poll for the three ordered writes
	}
	cancel()
	if !w.waitDone(2 * time.Second) {
		t.Fatal("writer did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"a", "b", "c"}
	if fmt.Sprint(entered) != fmt.Sprint(want) {
		t.Fatalf("write order = %v, want %v", entered, want)
	}
}

type pauseCall struct {
	attempt int
	delay   time.Duration
	resume  chan struct{}
}

// A persistently failing Store must not make the writer spin: retries wait on bounded backoff, one attempt at a
// time, and enqueue never blocks on the failing writer.
func TestStateWriterStoreFailureDoesNotSpinOrBlockReads(t *testing.T) {
	attempts := make(chan int, 64)
	pauses := make(chan pauseCall, 64)
	var attemptMu sync.Mutex
	n := 0

	w, cancel := newTestWriter(t, func(_ context.Context, _, _ string, _ db.NodeSession) error {
		attemptMu.Lock()
		n++
		k := n
		attemptMu.Unlock()
		select {
		case attempts <- k:
		default: // drain retries can outrun the buffer; the no-spin assertion reads the first attempts
		}
		return errors.New("store unavailable")
	})
	w.backoffMin = 10 * time.Millisecond
	w.backoffMax = 80 * time.Millisecond
	w.pause = func(ctx context.Context, attempt int, delay time.Duration) {
		pc := pauseCall{attempt: attempt, delay: delay, resume: make(chan struct{})}
		select {
		case pauses <- pc:
		case <-ctx.Done():
			return
		}
		select {
		case <-pc.resume:
		case <-ctx.Done():
		}
	}
	go w.run()

	w.enqueue("s1", db.NodeSession{Cwd: "/one"})
	select {
	case k := <-attempts:
		if k != 1 {
			t.Fatalf("first attempt = %d, want 1", k)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first write attempt never happened")
	}

	var pc1 pauseCall
	select {
	case pc1 = <-pauses:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not enter backoff after failure")
	}
	if pc1.delay <= 0 || pc1.delay > 20*time.Millisecond {
		t.Fatalf("first backoff = %v, want about 10ms", pc1.delay)
	}
	// No busy retry before the backoff is released.
	select {
	case k := <-attempts:
		t.Fatalf("writer spun: unexpected attempt %d during backoff", k)
	default:
	}

	// Enqueue must return immediately while the writer is parked in backoff.
	enqueued := make(chan struct{})
	go func() { w.enqueue("s1", db.NodeSession{Cwd: "/two"}); close(enqueued) }()
	select {
	case <-enqueued:
	case <-time.After(time.Second):
		t.Fatal("enqueue blocks while the writer is in failure backoff")
	}

	close(pc1.resume)
	select {
	case <-attempts:
	case <-time.After(2 * time.Second):
		t.Fatal("retry did not happen after backoff released")
	}
	var pc2 pauseCall
	select {
	case pc2 = <-pauses:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not back off after the second failure")
	}
	if pc2.delay < pc1.delay {
		t.Fatalf("backoff did not grow: %v then %v", pc1.delay, pc2.delay)
	}
	select {
	case k := <-attempts:
		t.Fatalf("writer spun: unexpected attempt %d during second backoff", k)
	default:
	}

	// Stopping must unblock the gated pause and end the writer through the bounded drain.
	cancel()
	select {
	case <-pc2.resume:
		// test owns this channel; closing is harmless if cancel already unblocked the pauser
	default:
	}
	close(pc2.resume)
	if !w.waitDone(2 * time.Second) {
		t.Fatal("writer kept running after lifecycle ended")
	}
}

// After a failed write the pending state is retained and retried without any new event. Removing pending retention
// after a failure fails this test.
func TestStateWriterFailedWriteRetriesRetainedPending(t *testing.T) {
	var mu sync.Mutex
	var okCalls []writtenCall
	failures := 0
	firstFailed := make(chan struct{})
	failedOnce := sync.Once{}

	w, cancel := newTestWriter(t, func(_ context.Context, _, session string, st db.NodeSession) error {
		mu.Lock()
		failures++
		k := failures
		mu.Unlock()
		if k == 1 {
			failedOnce.Do(func() { close(firstFailed) })
			return errors.New("temporary store error")
		}
		mu.Lock()
		okCalls = append(okCalls, writtenCall{session, st})
		mu.Unlock()
		return nil
	})
	w.backoffMin = time.Millisecond
	w.backoffMax = 2 * time.Millisecond
	defer cancel()
	go w.run()

	w.enqueue("s1", db.NodeSession{Cwd: "/original", CreationID: "c1", Incarnation: "i1"})
	select {
	case <-firstFailed:
	case <-time.After(2 * time.Second):
		t.Fatal("first write did not fail")
	}
	// No new enqueue: retention alone must produce the retry.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(okCalls)
		mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(time.Millisecond) // sleep-ok: yield inside the deadline-bounded poll for the retained pending retry
	}
	cancel()
	if !w.waitDone(2 * time.Second) {
		t.Fatal("writer did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(okCalls) != 1 {
		t.Fatalf("retries of retained pending = %d, want 1 (the failure dropped the pending state)", len(okCalls))
	}
	got := okCalls[0].state
	if got.Cwd != "/original" || got.CreationID != "c1" || got.Incarnation != "i1" {
		t.Fatalf("retry wrote %+v, want the retained state with its receipt", got)
	}
}

// While a retry is held in backoff, a coalesced newer value is what the retry writes.
func TestStateWriterFailedWriteRetryWritesLatest(t *testing.T) {
	var mu sync.Mutex
	var okCalls []writtenCall
	failures := 0
	firstFailed := make(chan struct{})
	failedOnce := sync.Once{}
	gatePause := make(chan struct{})

	w, cancel := newTestWriter(t, func(_ context.Context, _, session string, st db.NodeSession) error {
		mu.Lock()
		failures++
		k := failures
		mu.Unlock()
		if k == 1 {
			failedOnce.Do(func() { close(firstFailed) })
			return errors.New("temporary store error")
		}
		mu.Lock()
		okCalls = append(okCalls, writtenCall{session, st})
		mu.Unlock()
		return nil
	})
	w.backoffMin = time.Millisecond
	w.pause = func(ctx context.Context, _ int, _ time.Duration) {
		select {
		case <-gatePause:
		case <-ctx.Done():
		}
	}
	defer cancel()
	go w.run()

	w.enqueue("s1", db.NodeSession{Cwd: "/original", CreationID: "c1", Incarnation: "i1"})
	select {
	case <-firstFailed:
	case <-time.After(2 * time.Second):
		t.Fatal("first write did not fail")
	}
	w.enqueue("s1", db.NodeSession{Cwd: "/latest"})
	close(gatePause)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(okCalls)
		mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(time.Millisecond) // sleep-ok: yield inside the deadline-bounded poll for the successful retry
	}
	cancel()
	if !w.waitDone(2 * time.Second) {
		t.Fatal("writer did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(okCalls) != 1 {
		t.Fatalf("successful retries = %d, want exactly 1", len(okCalls))
	}
	got := okCalls[0].state
	if got.Cwd != "/latest" {
		t.Fatalf("retry wrote cwd %q, want newest pending /latest", got.Cwd)
	}
	if got.CreationID != "c1" || got.Incarnation != "i1" {
		t.Fatalf("retry lost creation receipt: %+v", got)
	}
}

// Stopping a Machine drains states queued behind a failed head within the bounded drain budget.
func TestStateWriterStopDrainsPending(t *testing.T) {
	var mu sync.Mutex
	var okOrder []string
	calls := 0
	aFailed := make(chan struct{})

	w, cancel := newTestWriter(t, func(ctx context.Context, _, session string, _ db.NodeSession) error {
		mu.Lock()
		calls++
		k := calls
		mu.Unlock()
		if k == 1 {
			close(aFailed)
			return errors.New("temporary")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		mu.Lock()
		okOrder = append(okOrder, session)
		mu.Unlock()
		return nil
	})
	w.backoffMin = time.Millisecond
	w.pause = func(ctx context.Context, _ int, _ time.Duration) { <-ctx.Done() }
	go w.run()

	w.enqueue("a", db.NodeSession{Cwd: "/a"})
	w.enqueue("b", db.NodeSession{Cwd: "/b"})
	w.enqueue("c", db.NodeSession{Cwd: "/c"})
	select {
	case <-aFailed:
	case <-time.After(2 * time.Second):
		t.Fatal("head write never failed")
	}
	// Lifecycle ends while the writer is parked in its retry pause; pause unblocks and drain takes over.
	cancel()

	if !w.waitDone(2 * time.Second) {
		t.Fatal("bounded drain did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"a", "b", "c"}
	if fmt.Sprint(okOrder) != fmt.Sprint(want) {
		t.Fatalf("drain order = %v, want %v (a retried then b,c)", okOrder, want)
	}
}

// A Store that keeps failing makes drain exit within its bound instead of hanging Machine shutdown.
func TestStateWriterDrainIsBounded(t *testing.T) {
	w, _ := newTestWriter(t, func(ctx context.Context, _, _ string, _ db.NodeSession) error {
		<-ctx.Done()
		return ctx.Err()
	})
	w.drainWait = 60 * time.Millisecond
	w.backoffMin = time.Millisecond
	w.enqueue("s1", db.NodeSession{Cwd: "/one"})

	start := time.Now()
	w.drain() // synchronous: every drain call ctx is capped by drainWait
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("drain took %s, want it bounded near 60ms", elapsed)
	}
}
