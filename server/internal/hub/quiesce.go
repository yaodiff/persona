package hub

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// The hub's built-in writer quiesce for a Web development snapshot
// (docs/operations/hub-update.md "Web development updates"): the hub is the only
// writer of its own database (with the built-in child manager), so it can pause
// itself instead of requiring operator hooks. Requests that write are refused
// for the duration, writes already under way are waited for, the built-in
// manager is stopped, and the background writers (app registrations, extension
// reports, device-session rows and pruning, the computer-trust sweep) leave
// their work pending until resume. The database is then checked with the same
// Quiesced rules the backup command uses, counting only the hub's own pool
// connections as its own.

// writeQuiescer counts request and background writes and stops them.
type writeQuiescer struct {
	mu       sync.Mutex
	paused   bool
	inflight int
	idle     chan struct{} // closed and cleared when inflight reaches 0
	// testOnPauseDefer, when non-nil in tests, runs inside beginOrDefer after
	// observing paused==true and before onPaused while holding mu.
	testOnPauseDefer func()
}

type quiesceHeldKey struct{}

// withQuiesceHeld marks ctx as already holding a writeQuiescer turn (for
// example, an HTTP handler admitted through quiesceGate) so inner helpers do
// not re-enter begin() and fail if a pause started while the request was in
// flight.
func withQuiesceHeld(ctx context.Context) context.Context {
	return context.WithValue(ctx, quiesceHeldKey{}, true)
}

func quiesceHeld(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	held, _ := ctx.Value(quiesceHeldKey{}).(bool)
	return held
}

// begin admits one write. False means writes are paused: leave the work
// pending for the resume.
func (q *writeQuiescer) begin() bool {
	return q.beginOrDefer(nil)
}

// beginOrDefer admits one write, or invokes onPaused while holding q.mu when
// writes are paused. Because resume takes q.mu before waking deferred workers,
// pending state published in onPaused is guaranteed to be visible to the wake
// pass that follows resume.
func (q *writeQuiescer) beginOrDefer(onPaused func()) bool {
	if q == nil {
		return true
	}
	q.mu.Lock()
	if q.paused {
		if q.testOnPauseDefer != nil {
			q.testOnPauseDefer()
		}
		if onPaused != nil {
			onPaused()
		}
		q.mu.Unlock()
		return false
	}
	q.inflight++
	q.mu.Unlock()
	return true
}

func (q *writeQuiescer) end() {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.inflight--
	if q.inflight == 0 && q.idle != nil {
		close(q.idle)
		q.idle = nil
	}
	q.mu.Unlock()
}

// pause stops admitting writes and waits for the ones under way. A write that
// began before keeps its turn; nothing new starts.
func (q *writeQuiescer) pause(ctx context.Context) error {
	q.mu.Lock()
	q.paused = true
	if q.inflight == 0 {
		q.mu.Unlock()
		return nil
	}
	idle := make(chan struct{})
	q.idle = idle
	q.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *writeQuiescer) resume() {
	q.mu.Lock()
	q.paused = false
	q.mu.Unlock()
}

// isWriteRequest is what the gate refuses while paused: anything but a read.
// The update's own status reads stay available.
func isWriteRequest(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// quiesceGate refuses writes while the hub pauses them for a snapshot, after
// authentication (a stranger is not told about the hub's state).
func (h *Hub) quiesceGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cancellation only registers intent in memory. The installer owns all writes.
		if !isWriteRequest(r) || (r.Method == http.MethodPost && r.URL.Path == "/api/v1/hub/update/cancel") {
			next.ServeHTTP(w, r)
			return
		}
		if !h.quiesce.begin() {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("hub-selfupdate.err_quiesced"))
			return
		}
		defer h.quiesce.end()
		next.ServeHTTP(w, r.WithContext(withQuiesceHeld(r.Context())))
	})
}

// assistantGate lets the supervisor be stopped and started again for a
// snapshot without ending supervision.
type assistantGate struct {
	mu      sync.Mutex
	paused  bool
	running bool
	resume  chan struct{} // closed on resume, replaced when paused
	exit    chan struct{} // closed when the current child returned
	cancel  context.CancelFunc
	started bool
}

func (g *assistantGate) waitResumed(ctx context.Context) bool {
	for {
		g.mu.Lock()
		if !g.paused {
			g.mu.Unlock()
			return true
		}
		wait := g.resume
		g.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return false
		}
	}
}

// register makes a starting child visible to pause. False means pause won the
// race: the caller must not start it.
func (g *assistantGate) register(exit chan struct{}, cancel context.CancelFunc) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused {
		return false
	}
	g.running, g.exit, g.cancel = true, exit, cancel
	return true
}

func (g *assistantGate) unregister() (paused bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.exit != nil {
		close(g.exit)
	}
	g.running, g.exit, g.cancel = false, nil, nil
	return g.paused
}

// pause stops the running child and keeps it stopped until resume.
func (g *assistantGate) pause(ctx context.Context) error {
	g.mu.Lock()
	g.paused = true
	if g.resume == nil || !g.started {
		g.resume = make(chan struct{})
	}
	g.started = true
	if !g.running {
		g.mu.Unlock()
		return nil
	}
	exit, cancel := g.exit, g.cancel
	g.mu.Unlock()
	cancel()
	select {
	case <-exit:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(20 * time.Second):
		return i18n.Err("hub-selfupdate.err_quiesce_timeout")
	}
}

func (g *assistantGate) start() {
	g.mu.Lock()
	if g.paused {
		close(g.resume)
		g.resume = make(chan struct{})
		g.paused = false
	}
	g.mu.Unlock()
}

// developmentQuiesceWait bounds waiting for an in-flight write or the
// manager's shutdown; a snapshot never waits forever on a stuck handler.
const developmentQuiesceWait = 2 * time.Minute

// pauseDevelopmentWriters stops the hub's own writers for a snapshot: request
// writes first (so no new work is accepted), then the built-in manager and the
// background writers' in-flight writes.
func (h *Hub) pauseDevelopmentWriters(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, developmentQuiesceWait)
	defer cancel()
	if err := h.quiesce.pause(ctx); err != nil {
		h.resumeDevelopmentWriters()
		return err
	}
	if err := h.assistantProc.pause(ctx); err != nil {
		// The child may already be stopping: let the supervisor start it
		// again rather than leave the gate closed, and wake every worker that
		// deferred its write while the pause held.
		h.resumeDevelopmentWriters()
		return err
	}
	return nil
}

// resumeDevelopmentWriters lets the writers go again. Writes are admitted
// before the pending workers are woken: each of them refuses its work while
// the pause holds, and a wake it consumed then is not repeated, so the work
// would stay pending until some unrelated later report.
func (h *Hub) resumeDevelopmentWriters() {
	h.quiesce.resume()
	h.resumeExtReportSave()
	h.wakeHelloWriters()
	h.wakePhoneWrites()
	h.scheduleDeviceSessionFinishes()
	h.lockTests.wake()
	h.resumeMachineEntries()
	h.live.resumeWrites()
	h.wakeClientOps()
	h.push.resumeWrites()
	h.resumePushGradingLayerReconcile()
	h.resumePersonaTiers()
	h.resumeMeshPersist()
	h.resumeAssistantProfile()
	h.resumeCodexInventory()
	h.resumeLooksGone()
	h.resumeFSWatchEpoch()
	h.resumeBusSeen()
	h.assistantProc.start()
}
