package node

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// reloadDrainWindow bounds how long Prepare keeps headless admission closed after the handoff grace
// while waiting for an active turn or compaction once RPCs have drained. reloadBlockLogEvery
// rate-limits repeated reminder logs while waiting on long-running assistants. Vars so tests change them.
var (
	reloadDrainWindow   = 30 * time.Second
	reloadBlockLogEvery = 5 * time.Minute
)

// ResidentReload drains work while Attach is still serving. Only the final exec closes existing connections;
// idle conversations that need the SIGKILL fallback are reaped before that transport gap begins.
type ResidentReload struct {
	mu                    sync.Mutex
	active                int
	paused                bool
	headlessPaused        bool // controlled by manager sessions.list update_handoff (SetHeadlessPaused)
	prepareHeadlessPaused bool // controlled by Prepare during bounded handoff drain
	preparing             bool // true while Prepare is actively waiting to reload
	grace                 time.Duration
	graceSet              bool
	idle                  chan struct{}
	peers                 map[*rpc.Peer]func()
	logger                *log.Logger
	draining              *proto.NodeUpdateDraining
}

func (r *ResidentReload) enter() (func(), error) {
	if r == nil {
		return func() {}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused {
		return nil, &rpc.Error{Code: nodeproto.CodeRestarting, Message: nodeproto.CodeRestarting}
	}
	if r.active == 0 {
		r.idle = make(chan struct{})
	}
	r.active++
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.active--
		if r.active == 0 {
			close(r.idle)
		}
	}, nil
}

func (r *ResidentReload) enterHeadless() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused || r.headlessPaused || r.prepareHeadlessPaused {
		return &rpc.Error{Code: nodeproto.CodeRestarting, Message: nodeproto.CodeRestarting}
	}
	return nil
}

// SetLogger routes reload wait diagnostics to logger instead of the process default logger.
func (r *ResidentReload) SetLogger(logger *log.Logger) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.logger = logger
	r.mu.Unlock()
}

func (r *ResidentReload) logf(format string, args ...any) {
	r.mu.Lock()
	logger := r.logger
	r.mu.Unlock()
	if logger != nil {
		logger.Printf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

// Preparing reports whether Prepare is currently waiting to drain and restart the resident node.
func (r *ResidentReload) Preparing() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.preparing
}

// SetHeadlessPaused controls only new external assistant turns. Other RPCs and recovery of an admitted turn continue.
func (r *ResidentReload) SetHeadlessPaused(paused bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.headlessPaused = paused
	r.mu.Unlock()
}

// SetGrace sets the next Prepare's admission grace. Zero preserves the indefinite natural-idle wait.
func (r *ResidentReload) SetGrace(grace time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.grace, r.graceSet = grace, true
	r.mu.Unlock()
}

func (r *ResidentReload) peer(p *rpc.Peer, disconnect func()) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.peers == nil {
		r.peers = map[*rpc.Peer]func(){}
	}
	if disconnect != nil {
		r.peers[p] = disconnect
	} else {
		delete(r.peers, p)
	}
}

func (n *Node) SetResidentReload(r *ResidentReload) { n.reload = r }

func (r *ResidentReload) Resume() {
	r.mu.Lock()
	r.paused = false
	r.prepareHeadlessPaused = false
	r.mu.Unlock()
}

// Prepare never stops an answering conversation or an admitted RPC. Natural idle pauses admission immediately;
// after the grace it drains admitted RPCs and holds only headless admission for a bounded window so short
// continuous turns finish without locking out read-only RPCs, session.stop/session.interrupt, or unrelated
// conversations when a long-running background task or turn blocks reload. A pre-transfer headless pause
// stays in force until the manager clears it.
func (r *ResidentReload) Prepare(ctx context.Context) error {
	r.mu.Lock()
	grace, graceSet := r.grace, r.graceSet
	r.preparing = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.preparing = false
		r.mu.Unlock()
	}()
	if !graceSet {
		grace = time.Duration(proto.DefaultNodeUpdateHandoffGraceSeconds) * time.Second
	}
	var handoffDeadline time.Time
	if grace > 0 {
		handoffDeadline = time.Now().Add(grace)
	}
	var (
		drainDeadline time.Time
		deferredIDs   map[string]struct{}
		lastAction    string
		lastLogAt     time.Time
	)
	logBlocking := func(action string, busy []busyAssistant) {
		now := time.Now()
		if action == lastAction && !lastLogAt.IsZero() && now.Sub(lastLogAt) < reloadBlockLogEvery {
			return
		}
		lastAction, lastLogAt = action, now
		r.logf("resident reload: %s; waiting for busy assistant(s): %s", action, formatBusyAssistants(busy))
	}
	for {
		if err := ctx.Err(); err != nil {
			r.Resume()
			return err
		}
		busy := busyAssistants()
		if len(busy) == 0 {
			// Any previously deferred long-running work has now cleared; reset deferred state
			// so a subsequent turn starting during active-RPC draining cannot inherit it.
			deferredIDs = nil
			drainDeadline = time.Time{}
			now := time.Now()
			expired := !handoffDeadline.IsZero() && !now.Before(handoffDeadline)
			r.mu.Lock()
			idle, ready := r.idle, r.active == 0
			if ready || expired {
				r.paused = true
			}
			if expired {
				r.prepareHeadlessPaused = true
			}
			r.mu.Unlock()
			if ready {
				if len(busyAssistants()) == 0 {
					break
				}
				r.Resume()
				continue
			}
			delay := 200 * time.Millisecond
			if !expired && !handoffDeadline.IsZero() {
				delay = min(delay, max(time.Until(handoffDeadline), 0))
			}
			timer := time.NewTimer(delay)
			select {
			case <-idle:
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				r.Resume()
				return ctx.Err()
			}
			timer.Stop()
			continue
		}

		now := time.Now()
		hasTasks := hasBusyBackgroundTasks(busy)
		// Once every conversation that caused us to defer has cleared, re-arm the handoff grace
		// so a new stream of continuous turns cannot starve the pending reload.
		if len(deferredIDs) > 0 && !anyIDBusy(busy, deferredIDs) {
			deferredIDs = nil
			drainDeadline = time.Time{}
			if grace > 0 {
				handoffDeadline = now.Add(grace)
			}
		}
		expired := !handoffDeadline.IsZero() && !now.Before(handoffDeadline)

		var (
			idle  chan struct{}
			ready bool
		)
		if !expired {
			r.mu.Lock()
			r.paused = false
			r.prepareHeadlessPaused = false
			r.mu.Unlock()
		} else if hasTasks {
			// Background tasks can run for tens of minutes or wait on this node's own RPCs; never wedge
			// RPC or headless admission while waiting for them to exit.
			r.mu.Lock()
			r.paused = false
			r.prepareHeadlessPaused = false
			r.mu.Unlock()
			drainDeadline = time.Time{}
			logBlocking("keeping RPCs open while background task(s) finish", busy)
		} else if len(deferredIDs) > 0 {
			r.mu.Lock()
			r.paused = false
			r.prepareHeadlessPaused = false
			r.mu.Unlock()
			logBlocking("bounded drain window elapsed; resumed headless admission", busy)
		} else {
			r.mu.Lock()
			idle, ready = r.idle, r.active == 0
			if !ready && drainDeadline.IsZero() {
				r.paused = true
				r.prepareHeadlessPaused = true
			} else {
				r.paused = false
				if drainDeadline.IsZero() {
					drainDeadline = now.Add(reloadDrainWindow)
				}
				if !now.Before(drainDeadline) {
					r.prepareHeadlessPaused = false
					deferredIDs = busyIDSet(busy)
				} else {
					r.prepareHeadlessPaused = true
				}
			}
			r.mu.Unlock()
			if len(deferredIDs) > 0 {
				logBlocking("bounded drain window elapsed; resumed headless admission", busy)
			} else {
				logBlocking("draining busy assistant work before restart", busy)
			}
		}

		delay := 200 * time.Millisecond
		if !expired && !handoffDeadline.IsZero() {
			delay = min(delay, max(time.Until(handoffDeadline), 0))
		} else if !drainDeadline.IsZero() && len(deferredIDs) == 0 {
			delay = min(delay, max(time.Until(drainDeadline), 0))
		}
		timer := time.NewTimer(delay)
		if expired && !hasTasks && len(deferredIDs) == 0 && !ready && drainDeadline.IsZero() && idle != nil {
			select {
			case <-idle:
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				r.Resume()
				return ctx.Err()
			}
		} else {
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				r.Resume()
				return ctx.Err()
			}
		}
		timer.Stop()
	}
	var procs []*assistantProc
	liveAssistants.Range(func(key, _ any) bool {
		procs = append(procs, key.(*assistantProc))
		return true
	})
	stopAndWait(procs)
	if err := ctx.Err(); err != nil {
		r.Resume()
		return err
	}
	return nil
}

// SetUpdateDraining records the committed update transaction currently waiting for idle handover,
// or clears it when id is empty.
func (r *ResidentReload) SetUpdateDraining(id, target string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if id == "" {
		r.draining = nil
	} else {
		r.draining = &proto.NodeUpdateDraining{ID: proto.NodeUpdateID(id), Target: target}
	}
	r.mu.Unlock()
}

// UpdateDraining returns a copy of the active committed update waiting for idle handover, or nil.
func (r *ResidentReload) UpdateDraining() *proto.NodeUpdateDraining {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.draining == nil {
		return nil
	}
	cp := *r.draining
	return &cp
}

// ReportUpdateDraining records draining state and notifies connected peers without stalling the reload worker.
func (r *ResidentReload) ReportUpdateDraining(ctx context.Context, id, target string) {
	if r == nil || id == "" {
		return
	}
	draining := proto.NodeUpdateDraining{ID: proto.NodeUpdateID(id), Target: target}
	r.mu.Lock()
	r.draining = &draining
	peers := make([]*rpc.Peer, 0, len(r.peers))
	for peer := range r.peers {
		peers = append(peers, peer)
	}
	r.mu.Unlock()
	for _, peer := range peers {
		go func(p *rpc.Peer) {
			send, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			_ = p.NotifyContext(send, nodeproto.EventUpdateDraining, draining)
		}(peer)
	}
}

func (r *ResidentReload) Announce(ctx context.Context) error {
	r.mu.Lock()
	var peers []*rpc.Peer
	for peer := range r.peers {
		peers = append(peers, peer)
	}
	var params map[string]any
	if r.draining != nil && r.draining.ID != "" {
		params = map[string]any{"id": string(r.draining.ID)}
		if r.draining.Target != "" {
			params["target"] = r.draining.Target
		}
	} else {
		params = map[string]any{}
	}
	r.mu.Unlock()
	for _, peer := range peers {
		err := r.exchange(ctx, peer, func() error {
			return peer.NotifyContext(ctx, nodeproto.EventRestarting, params)
		})
		if errors.Is(err, rpc.ErrClosed) {
			continue
		}
		if err != nil {
			return err
		}
		// The ordered request fences the announcement and drains manager calls before the old node closes.
		if err := r.exchange(ctx, peer, func() error {
			return peer.Call(ctx, "state.load", map[string]bool{"restart_barrier": true}, nil)
		}); err != nil && !errors.Is(err, rpc.ErrClosed) {
			return err
		}
	}
	return nil
}

// ReportUpdatePreflightFailure lets the manager stop waiting for a replacement while the current node keeps serving.
func (r *ResidentReload) ReportUpdatePreflightFailure(ctx context.Context, id string, err error) {
	failure := proto.NodeUpdatePreflightFailure{ID: id, Error: err.Error()}
	r.mu.Lock()
	defer r.mu.Unlock()
	for peer := range r.peers {
		// The notification must never stall the reload worker or interrupt the surviving connection.
		go func() {
			send, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			_ = peer.NotifyContext(send, nodeproto.EventUpdatePreflightFailed, failure)
		}()
	}
}

// exchange also bounds an already-started write. Aborting an unconfirmed announcement must close the physical
// connection: a queued or partially written control message must not arrive later and suspend a surviving manager.
func (r *ResidentReload) exchange(ctx context.Context, peer *rpc.Peer, send func() error) error {
	done := make(chan error, 1)
	go func() { done <- send() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		r.mu.Lock()
		disconnect := r.peers[peer]
		r.mu.Unlock()
		peer.Close(err)
		if disconnect != nil {
			disconnect()
		}
	}
	return err
}

func (r *ResidentReload) CancelAnnouncement() {
	r.mu.Lock()
	var peers []*rpc.Peer
	for peer := range r.peers {
		peers = append(peers, peer)
	}
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, peer := range peers {
		_ = r.exchange(ctx, peer, func() error {
			return peer.Call(ctx, "state.load", map[string]bool{"restart_barrier": false}, nil)
		})
	}
}
