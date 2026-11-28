package devices

import (
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
)

// PR-1 (issue #17251): a node's single rpc read loop used to call PutNodeSession synchronously for every state.put.
// A Store that does not answer therefore held every reply and event of that connection behind up to one
// nodeStoreWait per event, adding up to a minute of head-of-line blocking over consecutive events. A Machine now
// enqueues its node states here and one goroutine per Machine does the Store writes.
//
// The queue is bounded by the number of sessions with a state still unwritten (the machine's active sessions), not
// by the number of events: a newer complete state of the same session replaces the queued one. Different sessions
// keep strict FIFO order. Store I/O never runs under m.mu or eventRetire.

const (
	// stateWriteBackoffMin/Max bound the wait before retrying a failed node-state write.
	stateWriteBackoffMin = 100 * time.Millisecond
	stateWriteBackoffMax = 2 * time.Second
	// stateWriterDrainWait bounds how long a retired/stopped Machine's writer keeps flushing pending states.
	stateWriterDrainWait = 3 * time.Second
)

// nodeStateWrite is the Store call the writer makes (nodeStateStore.PutNodeSession).
type nodeStateWrite func(ctx context.Context, machine, session string, state db.NodeSession) error

type nodeStatePending struct {
	state   db.NodeSession
	version uint64 // advanced on every coalesced update; a successful call only removes the head at its own version
}

type nodeStateWriter struct {
	machineID string
	put       nodeStateWrite
	logf      func(string, ...any)
	// lifecycle is the Machine run context: when it ends the writer stops normal retries and drains within drainWait.
	lifecycle context.Context

	storeWait  time.Duration // per Store call budget (Machine.storeWait / nodeStoreWait)
	backoffMin time.Duration
	backoffMax time.Duration
	drainWait  time.Duration
	// pause, when non-nil, replaces the real bounded backoff sleep (tests gate retries deterministically).
	pause func(ctx context.Context, attempt int, delay time.Duration)

	wake chan struct{}
	done chan struct{}

	mu      sync.Mutex
	order   []string
	pending map[string]*nodeStatePending
	// exited is the terminal seal, set under mu exactly when the writer has stopped (or is committed to stop
	// after a bounded drain give-up). enqueue reads it under the same mu as its insert, making the
	// insert-versus-exit handshake atomic: an enqueue that sees false is owned by the writer and drained; one
	// that sees true is rejected so the caller runs its bounded fallback instead of entering a dead map.
	exited bool
}

func newNodeStateWriter(m *Machine, put nodeStateWrite) *nodeStateWriter {
	wait := m.storeWait
	if wait <= 0 {
		wait = nodeStoreWait
	}
	lifecycle := m.ctx
	if lifecycle == nil {
		lifecycle = context.Background()
	}
	return &nodeStateWriter{
		machineID:  m.cfg.ID,
		put:        put,
		logf:       m.d.logf,
		lifecycle:  lifecycle,
		storeWait:  wait,
		backoffMin: stateWriteBackoffMin,
		backoffMax: stateWriteBackoffMax,
		drainWait:  stateWriterDrainWait,
		wake:       make(chan struct{}, 1),
		done:       make(chan struct{}),
		pending:    map[string]*nodeStatePending{},
	}
}

// enqueue stores the newest complete state of a session, coalescing an older pending one. It never does Store I/O
// and never blocks on it. A creation receipt the older state carried and the new complete state omits is carried
// forward, so coalescing cannot lose the creation/incarnation pair.
//
// It reports whether the live writer owns the state. false means the writer has already been sealed/exited: the
// state was NOT inserted, and the caller must write it itself through the bounded storeCtx fallback. This closes
// the shutdown/removal window in which an admitted last put used to enter a map no goroutine reads.
func (w *nodeStateWriter) enqueue(session string, state db.NodeSession) bool {
	w.mu.Lock()
	if w.exited {
		w.mu.Unlock()
		return false
	}
	if p := w.pending[session]; p != nil {
		mergeCreationReceipt(&state, p.state)
		p.state = state
		p.version++
	} else {
		w.pending[session] = &nodeStatePending{state: state, version: 1}
		w.order = append(w.order, session)
	}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default: // a wake is already pending
	}
	return true
}

// sealIfEmpty is the writer's atomic terminal handshake. It seals the writer only when no state remains queued.
// The emptiness check and exited=true happen under one mu critical section, so they are atomic against enqueue:
// an enqueue that inserts first leaves order non-empty (this returns false and the writer flushes it); a seal
// first makes a later enqueue observe exited and reject into the caller's fallback.
func (w *nodeStateWriter) sealIfEmpty() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.order) != 0 {
		return false
	}
	w.exited = true
	return true
}

// seal marks the writer exited even if states remain, used only when the bounded drain budget is spent (the same
// best-effort give-up class as the old per-event storeCtx timeout). Later admits then take the bounded fallback
// instead of entering an unwatched map.
func (w *nodeStateWriter) seal() {
	w.mu.Lock()
	w.exited = true
	w.mu.Unlock()
}

// mergeCreationReceipt copies a creation receipt older held that newer omits. Each half is preserved independently.
func mergeCreationReceipt(newer *db.NodeSession, older db.NodeSession) {
	if newer.CreationID == "" && older.CreationID != "" {
		newer.CreationID = older.CreationID
	}
	if newer.Incarnation == "" && older.Incarnation != "" {
		newer.Incarnation = older.Incarnation
	}
}

func creationCommittedResult(state db.NodeSession) string {
	if state.CreationID != "" && state.Incarnation != "" {
		return "committed_with_receipt"
	}
	return "committed_without_receipt"
}

// run is the single writer goroutine. It exits after a bounded drain once lifecycle ends; close(done) is its
// completion signal. The deferred seal is a safety net: every normal exit already sealed atomically in drain, so
// this only guarantees a late enqueue can never mistake a gone writer for a live one.
func (w *nodeStateWriter) run() {
	defer func() {
		w.seal()
		close(w.done)
	}()
	failures := 0
	for {
		w.mu.Lock()
		empty := len(w.order) == 0
		w.mu.Unlock()
		if empty {
			select {
			case <-w.wake:
				continue
			case <-w.lifecycle.Done():
				w.drain()
				return
			}
		}

		if w.writeHead(&failures) {
			w.drain()
			return
		}
		// Stop admitting fresh normal attempts once the Machine ended: the call just finished was already in
		// flight, and everything still queued goes through the time-bounded drain rather than an unbounded tail.
		if w.lifecycle.Err() != nil {
			w.drain()
			return
		}
	}
}

// writeHead writes the current FIFO head once. failures is the consecutive failure count for backoff. It returns
// true when lifecycle ended during the attempt/backoff and the writer must drain.
func (w *nodeStateWriter) writeHead(failures *int) (stop bool) {
	session, version, state := w.head()
	ctx, cancel := w.callContext(context.Background())
	err := w.put(ctx, w.machineID, session, state)
	cancel()
	if err == nil {
		*failures = 0
		w.committed(session, version)
		devicewire.ShareCreationLog(w.logf, session, "manager_persist", creationCommittedResult(state))
		return false
	}
	devicewire.ShareCreationLog(w.logf, session, "manager_persist", "store_error")
	// The pending head is retained with the newest coalesced state: a recovered Store writes it on retry.
	delay := w.backoffDelay(*failures)
	*failures++
	if w.pause != nil {
		w.pause(w.lifecycle, *failures, delay)
	} else {
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-w.lifecycle.Done():
			timer.Stop()
			return true
		}
		timer.Stop()
	}
	return w.lifecycle.Err() != nil
}

func (w *nodeStateWriter) head() (string, uint64, db.NodeSession) {
	w.mu.Lock()
	defer w.mu.Unlock()
	session := w.order[0]
	p := w.pending[session]
	return session, p.version, p.state
}

// committed removes the head only when no newer state was coalesced while its call was in flight; otherwise the
// newer version stays at the head and the loop writes it next.
func (w *nodeStateWriter) committed(session string, writtenVersion uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.pending[session]
	if p == nil || p.version != writtenVersion {
		return
	}
	delete(w.pending, session)
	if len(w.order) > 0 && w.order[0] == session {
		w.order = w.order[1:]
		return
	}
	for i, id := range w.order {
		if id == session {
			w.order = append(w.order[:i], w.order[i+1:]...)
			return
		}
	}
}

func (w *nodeStateWriter) backoffDelay(failures int) time.Duration {
	min, max := w.backoffMin, w.backoffMax
	if min <= 0 {
		min = stateWriteBackoffMin
	}
	if max <= 0 {
		max = stateWriteBackoffMax
	}
	d := min
	for i := 0; i < failures && d < max; i++ {
		if d > max/2 {
			return max
		}
		d *= 2
	}
	if d > max {
		return max
	}
	return d
}

func (w *nodeStateWriter) callContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	wait := w.storeWait
	if wait <= 0 {
		wait = nodeStoreWait
	}
	return context.WithTimeout(parent, wait)
}

// drain keeps flushing pending states after the Machine was retired/stopped, but no longer than drainWait overall.
// It is best effort: a Store that stays down leaves the newest states unwritten, the same bounded give-up class as
// the old per-event storeCtx timeout; the node resends a session's state when it next changes.
//
// The empty-queue exit goes through sealIfEmpty, the atomic terminal handshake against a concurrent enqueue. If
// the drain budget expires with the Store down, seal marks the writer finished anyway so later admits take the
// bounded fallback rather than silently entering a dead queue.
func (w *nodeStateWriter) drain() {
	wait := w.drainWait
	if wait <= 0 {
		wait = stateWriterDrainWait
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	for {
		// Atomic empty-check + terminal seal. false means a state is present (possibly enqueued while lifecycle
		// was ending), so it must be flushed before exit.
		if w.sealIfEmpty() {
			return
		}
		session, version, state := w.head()
		callCtx, callCancel := w.callContext(ctx)
		err := w.put(callCtx, w.machineID, session, state)
		callCancel()
		if err == nil {
			w.committed(session, version)
			devicewire.ShareCreationLog(w.logf, session, "manager_persist", creationCommittedResult(state))
			continue
		}
		devicewire.ShareCreationLog(w.logf, session, "manager_persist", "store_error")
		select {
		case <-ctx.Done():
			w.seal() // bounded give-up: release late admits to their own storeCtx fallback
			return
		case <-time.After(w.backoffMin):
		}
	}
}

// waitDone is for tests: it waits for the writer goroutine to leave.
func (w *nodeStateWriter) waitDone(timeout time.Duration) bool {
	select {
	case <-w.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// persistNodeState handles one validated state.put. A running Machine (m.ctx installed by startMachines / the path
// group before its connection serves) enqueues onto its single ordered writer, so the rpc read loop never waits on
// the Store. A bare Machine with no run lifecycle (focused unit tests that call onEvent directly) keeps the old
// bounded inline write and its synchronous outcome semantics; no serving production Machine takes that branch.
//
// One shutdown-window exception keeps the documented "last put of a machine being let go" guarantee: if the writer
// has already sealed/exited when this admission wins its lock, enqueue rejects and the put runs the same bounded
// storeCtx write inline. That branch is reachable only after m.ctx ended and the writer is gone, never during a
// live Machine's normal lifetime, so it does not re-block the live read loop. It takes neither m.mu nor
// eventRetire itself; in production the event wrapper may already hold eventRetire, exactly as the pre-PR inline
// state.put and the bare-Machine branch did, and the call is bounded by storeCtx.
func (m *Machine) persistNodeState(session string, state db.NodeSession) {
	s, ok := m.d.opts.Store.(nodeStateStore)
	if !ok {
		devicewire.ShareCreationLog(m.d.logf, session, "manager_persist", "store_unavailable")
		return
	}
	if m.ctx == nil {
		m.putNodeStateNow(s, session, state)
		return
	}
	m.stateWriterOnce.Do(func() {
		w := newNodeStateWriter(m, s.PutNodeSession)
		m.stateWriter = w
		m.d.workers.Add(1)
		go func() {
			defer m.d.workers.Done()
			w.run()
		}()
	})
	if m.stateWriter.enqueue(session, state) {
		return
	}
	// The writer sealed/exited in the Machine-stop window. Keep this admitted last put instead of dropping it
	// into an unwatched queue: the old bounded inline write, independent of the ended Machine context.
	m.putNodeStateNow(s, session, state)
}

// putNodeStateNow performs one bounded synchronous PutNodeSession with the legacy outcome logging. It is the bare
// Machine path and the post-exit shutdown fallback; Store I/O here runs under no m.mu lock.
func (m *Machine) putNodeStateNow(s nodeStateStore, session string, state db.NodeSession) {
	ctx, cancel := m.storeCtx()
	defer cancel()
	if err := s.PutNodeSession(ctx, m.cfg.ID, session, state); err != nil {
		devicewire.ShareCreationLog(m.d.logf, session, "manager_persist", "store_error")
		return
	}
	devicewire.ShareCreationLog(m.d.logf, session, "manager_persist", creationCommittedResult(state))
}
