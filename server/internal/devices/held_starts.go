package devices

// The owner's new-Agent starts held while their computer is away (#10674).
//
// When POST /api/v1/sessions targets a paired computer that is offline, the hub validates the request and asks the
// computer's manager to hold it (devicewire.MethodHoldStart). The manager persists the original start payload in
// held_operations (kind = "start", session_id = "") keyed by X-Persona-Op-Id and returns a structured HeldStart row
// (and broadcasts a held_start WebSocket event to full devices declaring agent_start_hold).
//
// Once the computer reconnects (configured and listed, with the bus to the hub up), the manager replays each waiting
// held start once through the hub's real start/validation path (devicewire.MethodReplayHeldStart), passing op_id all
// the way to session.start / session.start.work on the node.
//
// Only an attempt proven not to have reached the node waits again by itself. If the connection drops mid-flight (or
// the manager restarts mid-flight), the row moves to failed/unclear and is never blindly auto-retried: on reconnect,
// reconciliation queries start.status(op_id) on the node first. If the node already started a session for op_id, the
// manager links and removes the held start cleanly (emitting state "started" with the real session_id) without ever
// spawning a duplicate Agent. If the outcome remains unknown (or the node lacks start_op_id_v1), the row waits for the
// owner to Retry or Delete.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// heldStartsPerMachine caps held new-Agent starts per computer (waiting or failed).
const heldStartsPerMachine = 50

// Vars so tests can shorten timeouts deterministically.
var (
	heldStartTimeout     = 5 * time.Minute
	heldStartCheckWait   = 20 * time.Second
	heldStartLinkTimeout = 15 * time.Second
)

var (
	errHeldStartStartingNow = i18n.Err("devices-held-starts.starting_now")
	errHeldStartStore       = i18n.Err("devices-held-starts.store_failed")
	errHeldStartSignedOut   = i18n.Err("devices-held-starts.signed_out")
)

type heldStartKey struct{ machine, opID string }

type heldStartRow struct {
	db.HeldOperation
	meta        devicewire.HeldStartMeta
	busy        bool
	checked     time.Time
	checking    bool
	failedReads int
	stale       bool
	staleOwn    bool
}

func newHeldStartRow(op db.HeldOperation) *heldStartRow {
	r := &heldStartRow{HeldOperation: op}
	if len(op.Meta) > 0 {
		_ = json.Unmarshal(op.Meta, &r.meta)
	}
	r.Call = nil
	r.Meta = nil
	return r
}

func (r *heldStartRow) unclear() bool { return r.State == db.HeldFailed && r.Failure == db.HeldUnclear }
func (r *heldStartRow) checkFirst() bool {
	return r.State == db.HeldWaiting && r.Failure == db.HeldUnclear
}
func (r *heldStartRow) taken() bool { return r.busy || r.checking }
func (r *heldStartRow) expired(now time.Time) bool {
	return now.Sub(r.HeldAt) >= heldExpiry
}

type heldStartBook struct {
	loadMu    sync.Mutex
	mu        sync.Mutex
	loaded    bool
	failed    time.Time
	ctx       context.Context
	rows      map[string][]*heldStartRow
	revoked   map[string]bool
	running   map[string]bool
	gone      map[int64]int
	unsure    map[heldStartKey]bool
	holding   map[heldStartKey]int
	repairing map[heldStartKey]chan struct{}
	now       func() time.Time // tests
}

func (h *heldStartBook) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func (h *heldStartBook) rowLocked(machine string, seq int64) *heldStartRow {
	for _, r := range h.rows[machine] {
		if r.Seq == seq {
			return r
		}
	}
	return nil
}

func (h *heldStartBook) dropLocked(machine string, seq int64) {
	h.rows[machine] = slices.DeleteFunc(h.rows[machine], func(r *heldStartRow) bool { return r.Seq == seq })
	if len(h.rows[machine]) == 0 {
		delete(h.rows, machine)
	}
}

func (r *heldStartRow) wire(lang i18n.Lang, computer string) proto.HeldStart {
	hs := proto.HeldStart{
		OpID:        r.OpID,
		MachineID:   r.MachineID,
		Computer:    computer,
		Cwd:         r.meta.Cwd,
		Launcher:    r.meta.Launcher,
		AgentType:   r.meta.AgentType,
		Model:       r.meta.Model,
		Host:        r.meta.Host,
		TabName:     r.meta.TabName,
		TodoID:      r.meta.TodoID,
		WorkSession: r.meta.WorkSession,
		Prompt:      r.Text,
		Images:      r.Images,
		State:       proto.HeldStartWaiting,
		SentAt:      r.SentAt,
		HeldAt:      r.HeldAt,
	}
	switch {
	case r.State == db.HeldSending || r.checking:
		hs.State = proto.HeldStartStarting
		hs.Error = i18n.T(lang, "devices-held-starts.starting")
	case r.State == db.HeldFailed:
		hs.State = proto.HeldStartFailed
		hs.Failure = r.Failure
		switch r.Failure {
		case db.HeldRefused:
			reason := r.Error
			if reason == "" {
				reason = i18n.T(lang, "devices-held-starts.no_reason")
			}
			hs.Error = i18n.T(lang, "devices-held-starts.refused", i18n.Args{"reason": reason})
		case db.HeldExpired:
			hs.Error = i18n.T(lang, "devices-held-starts.expired", i18n.Args{"name": computer})
		case db.HeldUnconfirmed:
			hs.Error = i18n.T(lang, "devices-held-starts.unconfirmed")
		case db.HeldSignedOut:
			hs.Error = i18n.T(lang, "devices-held-starts.signed_out")
			hs.DeleteOnly = true
		default:
			hs.Error = i18n.T(lang, "devices-held-starts.unclear")
		}
	case r.checkFirst():
		hs.Error = i18n.T(lang, "devices-held-starts.check", i18n.Args{"name": computer})
	default:
		hs.Error = i18n.T(lang, "devices-held-starts.waiting", i18n.Args{"name": computer})
	}
	hs.CanCancel = r.State != db.HeldSending && !r.busy
	hs.CanRetry = r.State == db.HeldFailed && !r.taken() && r.Failure != db.HeldSignedOut
	return hs
}

func (d *Devices) pushHeldStart(hs proto.HeldStart) {
	d.broadcast(map[string]any{
		"type":       "held_start",
		"machine_id": hs.MachineID,
		"held_start": hs,
	})
}

func (d *Devices) pushHeldStartRow(r heldStartRow) {
	d.pushHeldStart(r.wire(i18n.Lang(r.Lang).Or(i18n.Default), d.heldComputer(r.MachineID)))
}

// heldStartLoad reads held starts once, recovering any left sending by a stopped manager as failed/unclear.
func (d *Devices) heldStartLoad(ctx context.Context) bool {
	h := &d.heldStarts
	settled := func() (bool, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.loaded, h.loaded || !h.failed.IsZero() && h.clock().Sub(h.failed) < heldLoadBackoff
	}
	if loaded, done := settled(); done {
		return loaded
	}
	st := d.heldStore()
	if st == nil {
		return false
	}
	h.loadMu.Lock()
	defer h.loadMu.Unlock()
	if loaded, done := settled(); done {
		return loaded
	}
	fail := func(err error) bool {
		d.logf("held starts: %v", err)
		h.mu.Lock()
		h.failed = h.clock()
		h.mu.Unlock()
		return false
	}
	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	if n, err := st.RecoverHeldOperations(sctx, db.HeldKindStart); err != nil {
		return fail(err)
	} else if n > 0 {
		d.logf("held starts: %d left starting by a stopped manager, marked unclear", n)
	}
	ops, err := st.HeldOperations(sctx, db.HeldKindStart)
	if err != nil {
		return fail(err)
	}
	rows := map[string][]*heldStartRow{}
	for _, op := range ops {
		rows[op.MachineID] = append(rows[op.MachineID], newHeldStartRow(op))
	}
	h.mu.Lock()
	h.rows, h.running, h.gone, h.unsure, h.loaded = rows, map[string]bool{}, map[int64]int{}, map[heldStartKey]bool{}, true
	h.holding, h.repairing = map[heldStartKey]int{}, map[heldStartKey]chan struct{}{}
	h.mu.Unlock()
	return true
}

func (d *Devices) heldStartTick(ctx context.Context) {
	if !d.heldStartLoad(ctx) {
		return
	}
	d.heldStartRepair(ctx)
	d.heldStartFailRevoked(ctx)
	d.heldStartExpire(ctx)
	d.heldStartWakeAll()
}

func (d *Devices) heldStartWakeAll() {
	h := &d.heldStarts
	h.mu.Lock()
	machines := make([]string, 0, len(h.rows))
	for machine := range h.rows {
		machines = append(machines, machine)
	}
	h.mu.Unlock()
	for _, machine := range machines {
		d.heldStartWake(machine)
	}
}

// ---- holding ----

func (m *Machine) connectedNow() bool {
	m.mu.Lock()
	p := m.peer
	m.mu.Unlock()
	if p == nil {
		return false
	}
	select {
	case <-p.Done():
		return false
	default:
		return true
	}
}

func (m *Machine) supportsStartOpID() bool {
	m.mu.Lock()
	info := m.info
	m.mu.Unlock()
	return nodeHas(info, nodeproto.StartOpID)
}

// heldStartOn reports when m's current connection began and whether held starts may act on it now.
// Like held messages, held starts wait for the node's initial session list (after node.configure and managed launchers)
// or heldListWait into the connection, and require the bus to the hub to be up for replay.
func (m *Machine) heldStartOn() (connAt time.Time, ready bool) {
	if !m.d.busUp() {
		return time.Time{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.peer == nil {
		return time.Time{}, false
	}
	select {
	case <-m.peer.Done():
		return time.Time{}, false
	default:
	}
	if m.sessionsListed || (!m.connectedAt.IsZero() && time.Since(m.connectedAt) >= heldListWait) {
		return m.connectedAt, true
	}
	return time.Time{}, false
}

// holdStart handles devicewire.MethodHoldStart: persists a new-Agent start while its computer is away.
func (d *Devices) holdStart(ctx context.Context, req devicewire.HoldStartRequest) (devicewire.HoldStartReply, error) {
	c := req.Call
	if c.MachineID == "" || c.OpID == "" || len(c.Params) == 0 {
		return devicewire.HoldStartReply{}, &rpc.Error{Code: "invalid_params", Message: "machine_id, op_id and params are required"}
	}
	m := d.machineByID(c.MachineID)
	if m == nil {
		return devicewire.HoldStartReply{}, &rpc.Error{Code: devicewire.CodeNoDevice, Message: "no such machine"}
	}
	if req.OnlyIfOffline && m.connectedNow() {
		return devicewire.HoldStartReply{}, &rpc.Error{Code: devicewire.CodeMachineOnline, Message: "machine is online"}
	}
	lang := i18n.Lang(c.Lang).Or(i18n.Default)
	if !d.heldStartLoad(ctx) {
		return devicewire.HoldStartReply{}, &rpc.Error{Code: "unavailable", Message: i18n.T(lang, "devices-held-starts.store_failed")}
	}
	h := &d.heldStarts
	key := heldStartKey{c.MachineID, c.OpID}
	h.mu.Lock()
	for ch := h.repairing[key]; ch != nil; ch = h.repairing[key] {
		h.mu.Unlock()
		wctx, cancel := heldStoreCtx(ctx)
		select {
		case <-ch:
		case <-wctx.Done():
		}
		cancel()
		h.mu.Lock()
		if wctx.Err() != nil && h.repairing[key] == ch {
			h.mu.Unlock()
			return devicewire.HoldStartReply{}, &rpc.Error{Code: "unavailable", Message: i18n.T(lang, "devices-held-starts.store_failed")}
		}
	}
	h.holding[key]++
	defer func() {
		h.mu.Lock()
		if h.holding[key]--; h.holding[key] <= 0 {
			delete(h.holding, key)
		}
		h.mu.Unlock()
	}()
	n, known := 0, false
	for _, r := range h.rows[c.MachineID] {
		n++
		if r.OpID == c.OpID {
			known = true
		}
	}
	h.mu.Unlock()
	if !known && n >= heldStartsPerMachine {
		d.logf("[%s] held start refused: %d held already", c.MachineID, n)
		return devicewire.HoldStartReply{}, &rpc.Error{Code: devicewire.CodeHeldStartFull, Message: i18n.T(lang, "devices-held-starts.full")}
	}
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > heldMaxBytes {
		return devicewire.HoldStartReply{}, &rpc.Error{Code: "too_large", Message: i18n.T(lang, "devices-held-starts.not_prepared")}
	}
	now := h.clock().UTC()
	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	st := d.heldStore()
	op, fresh, err := st.HoldOperation(sctx, db.HeldOperation{
		Kind:      db.HeldKindStart,
		OpID:      c.OpID,
		MachineID: c.MachineID,
		SessionID: "",
		Call:      raw,
		Lang:      string(lang),
		SentAt:    now,
		HeldAt:    now,
	})
	if err != nil {
		lctx, lcancel := heldStoreCtx(ctx)
		kept, found, lerr := st.HeldOperationOf(lctx, db.HeldKindStart, c.MachineID, "", c.OpID)
		lcancel()
		if lerr != nil {
			h.mu.Lock()
			h.unsure[key] = true
			h.mu.Unlock()
		}
		if lerr != nil || !found {
			d.logf("[%s] held start could not be saved: %v", c.MachineID, err)
			return devicewire.HoldStartReply{}, &rpc.Error{Code: "unavailable", Message: i18n.T(lang, "devices-held-starts.store_failed")}
		}
		d.logf("[%s] held start was saved, though its store said %v", c.MachineID, err)
		op, fresh = kept, true
	}
	h.mu.Lock()
	delete(h.unsure, key)
	rows := h.rows[op.MachineID]
	i, found := slices.BinarySearchFunc(rows, op.Seq, func(r *heldStartRow, seq int64) int { return cmp.Compare(r.Seq, seq) })
	if !found {
		h.rows[op.MachineID] = slices.Insert(rows, i, newHeldStartRow(op))
	}
	snap := *h.rows[op.MachineID][i]
	revoked := op.CredentialID != "" && h.revoked[op.CredentialID]
	total := len(h.rows[op.MachineID])
	h.mu.Unlock()
	if revoked {
		d.heldStartFailRevoked(ctx)
		h.mu.Lock()
		if row := h.rowLocked(op.MachineID, op.Seq); row != nil {
			snap = *row
		}
		h.mu.Unlock()
	}
	hs := snap.wire(lang, d.heldComputer(op.MachineID))
	if fresh {
		d.logf("[%s] Agent start held until computer is back (%d held)", c.MachineID, total)
		d.pushHeldStart(hs)
		d.heldStartWake(c.MachineID) // in case the computer connected right as we held
	}
	return devicewire.HoldStartReply{Delivered: "later", HeldStart: hs}, nil
}

// ---- replay & reconciliation ----

type heldStartAction int

const (
	heldStartDoNothing heldStartAction = iota
	heldStartDoStart
	heldStartDoCheck
)

// nextLocked finds the next held start on machine that can act now. Unlike messages to one session, independent Agent
// starts on the same computer do not block each other when one fails or awaits the owner.
func (h *heldStartBook) nextLocked(machine string, connAt time.Time, ready bool, now time.Time) (*heldStartRow, heldStartAction) {
	for _, r := range h.rows[machine] {
		if r.taken() || r.stale || r.State == db.HeldSending {
			continue
		}
		switch {
		case r.unclear():
			if ready && !connAt.IsZero() && !connAt.Equal(r.checked) {
				return r, heldStartDoCheck
			}
		case r.State == db.HeldWaiting && r.checkFirst():
			if ready {
				return r, heldStartDoCheck
			}
		case r.State == db.HeldWaiting && !r.expired(now):
			if ready {
				return r, heldStartDoStart
			}
		}
	}
	return nil, heldStartDoNothing
}

func (d *Devices) heldStartWake(machine string) {
	h := &d.heldStarts
	h.mu.Lock()
	ctx := h.ctx
	if !h.loaded || ctx == nil || ctx.Err() != nil || len(h.rows[machine]) == 0 || h.running[machine] {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	m := d.machineByID(machine)
	if m == nil {
		return
	}
	connAt, ready := m.heldStartOn()
	if !ready {
		return
	}
	now := h.clock()
	h.mu.Lock()
	if h.running[machine] {
		h.mu.Unlock()
		return
	}
	if _, act := h.nextLocked(machine, connAt, ready, now); act == heldStartDoNothing {
		h.mu.Unlock()
		return
	}
	h.running[machine] = true
	h.mu.Unlock()
	d.workers.Add(1)
	go func() {
		defer d.workers.Done()
		d.heldStartRun(ctx, machine)
	}()
}

func (d *Devices) heldStartRun(ctx context.Context, machine string) {
	h := &d.heldStarts
	st := d.heldStore()
	for {
		m := d.machineByID(machine)
		var connAt time.Time
		ready := false
		if m != nil {
			connAt, ready = m.heldStartOn()
		}
		now := h.clock()
		h.mu.Lock()
		var r *heldStartRow
		act := heldStartDoNothing
		if ctx.Err() == nil {
			r, act = h.nextLocked(machine, connAt, ready, now)
		}
		if act == heldStartDoNothing || r == nil {
			delete(h.running, machine)
			h.mu.Unlock()
			if m != nil && ctx.Err() == nil {
				if c, ok := m.heldStartOn(); ok != ready || !c.Equal(connAt) {
					d.heldStartWake(machine)
				}
			}
			return
		}
		seq, opID, lang := r.Seq, r.OpID, r.Lang
		if act == heldStartDoCheck {
			retried := r.checkFirst()
			r.checking = true
			if !retried {
				r.checked = connAt
			}
			snap := *r
			h.mu.Unlock()
			d.pushHeldStartRow(snap)
			ok := d.heldStartCheck(ctx, m, machine, seq, opID, retried)
			if !ok {
				h.mu.Lock()
				delete(h.running, machine)
				h.mu.Unlock()
				return
			}
			continue
		}
		r.State = db.HeldSending
		h.mu.Unlock()
		sctx, cancel := heldStoreCtx(ctx)
		claim, claimed, err := st.ClaimHeldOperation(sctx, seq, now)
		cancel()
		if err != nil || !claimed {
			h.mu.Lock()
			if row := h.rowLocked(machine, seq); row != nil {
				row.State = db.HeldWaiting
				if err != nil {
					row.TriedAt = now
				}
			}
			delete(h.running, machine)
			h.mu.Unlock()
			d.logf("[%s] held start %s not claimed: %v", machine, opID, err)
			if err == nil {
				d.heldStartSync(ctx, machine, seq)
			}
			return
		}
		h.mu.Lock()
		var startingSnap heldStartRow
		hasSnap := false
		if row := h.rowLocked(machine, seq); row != nil {
			row.TriedAt, row.Attempts = claim.TriedAt, claim.Attempts
			startingSnap, hasSnap = *row, true
		}
		h.mu.Unlock()
		if hasSnap {
			d.pushHeldStartRow(startingSnap)
		}
		outcome, sessionID, reason := d.heldStartDeliver(ctx, claim.Call, lang)
		if !d.heldStartSettle(ctx, machine, seq, outcome, sessionID, reason) {
			h.mu.Lock()
			delete(h.running, machine)
			h.mu.Unlock()
			return
		}
	}
}

// heldStartCheck queries start.status(op_id) on the node for a start whose previous try had an uncertain outcome.
// If the node already created a session for op_id, the held start reconciles cleanly without spawning a duplicate.
// Returns false to pause the machine loop until the next wake/tick (e.g. start still pending or node disconnected).
func (d *Devices) heldStartCheck(ctx context.Context, m *Machine, machine string, seq int64, opID string, retried bool) bool {
	h := &d.heldStarts
	finishCheck := func(broadcast bool) {
		h.mu.Lock()
		var snap heldStartRow
		has := false
		if r := h.rowLocked(machine, seq); r != nil {
			r.checking = false
			snap, has = *r, true
		}
		h.mu.Unlock()
		if broadcast && has {
			d.pushHeldStartRow(snap)
		}
	}
	if m == nil || !m.supportsStartOpID() {
		if retried {
			d.heldStartUnconfirmed(ctx, machine, seq, true)
			finishCheck(true)
			return true
		}
		finishCheck(true)
		return true
	}
	cctx, cancel := context.WithTimeout(ctx, heldStartCheckWait)
	var status proto.StartStatus
	err := m.call(cctx, proto.MethodStartStatus, proto.StartStatusParams{OpID: opID}, &status)
	cancel()
	if err != nil {
		d.logf("[%s] held start %s status check failed: %v", machine, opID, err)
		h.mu.Lock()
		tries := 0
		if r := h.rowLocked(machine, seq); r != nil {
			if heldNotWritten(err) && !retried {
				r.checked = time.Time{} // retry check once the computer reconnects
			} else {
				r.failedReads++
				tries = r.failedReads
			}
		}
		h.mu.Unlock()
		if tries >= heldCheckTries {
			d.heldStartUnconfirmed(ctx, machine, seq, retried)
		}
		finishCheck(true)
		return false
	}
	h.mu.Lock()
	if r := h.rowLocked(machine, seq); r != nil {
		r.failedReads = 0
	}
	h.mu.Unlock()
	switch status.State {
	case proto.StartStatusStarted:
		if status.SessionID != "" {
			d.heldStartReconcile(ctx, machine, seq, opID, status.SessionID)
			finishCheck(false)
			return true
		}
	case proto.StartStatusPending:
		// Still running on the node: never duplicate and never fail yet; check again on the next tick.
		h.mu.Lock()
		if r := h.rowLocked(machine, seq); r != nil && !retried {
			r.checked = time.Time{}
		}
		h.mu.Unlock()
		finishCheck(true)
		return false
	}
	// StartStatusUnknown (or started without session_id): never blindly auto-retry.
	if retried {
		d.heldStartUnconfirmed(ctx, machine, seq, true)
	}
	finishCheck(true)
	return true
}

func (d *Devices) heldStartReconcile(ctx context.Context, machine string, seq int64, opID, sessionID string) {
	h := &d.heldStarts
	st := d.heldStore()
	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	op, found, err := st.HeldOperationOf(sctx, db.HeldKindStart, machine, "", opID)
	if err == nil && found && len(op.Call) > 0 && d.opts.Hub != nil {
		var call devicewire.HeldStartCall
		if json.Unmarshal(op.Call, &call) == nil {
			lctx, lcancel := context.WithTimeout(ctx, heldStartLinkTimeout)
			if _, herr := buswire.Call(lctx, d.opts.Hub, devicewire.ReplayHeldStartMethod, devicewire.ReplayHeldStartRequest{
				Call:                call,
				ReconciledSessionID: sessionID,
			}); herr != nil {
				d.logf("[%s] held start %s reconciled to %s, post-start link failed: %v", machine, opID, sessionID, herr)
			}
			lcancel()
		}
	}
	ok, err := st.DeleteHeldOperation(sctx, seq, db.HeldFailed, db.HeldWaiting)
	if err != nil {
		d.logf("[%s] held start %s reconciled to %s, store delete failed: %v", machine, opID, sessionID, err)
		return
	}
	if !ok {
		d.heldStartSync(ctx, machine, seq)
		return
	}
	h.mu.Lock()
	var snap heldStartRow
	has := false
	if r := h.rowLocked(machine, seq); r != nil {
		snap, has = *r, true
	}
	h.dropLocked(machine, seq)
	h.mu.Unlock()
	d.logf("[%s] held start %s reconciled to existing session %s", machine, opID, sessionID)
	if has {
		hs := snap.wire(i18n.Lang(snap.Lang).Or(i18n.Default), d.heldComputer(machine))
		hs.State = proto.HeldStartStarted
		hs.SessionID = sessionID
		hs.Failure, hs.Error, hs.CanRetry, hs.CanCancel = "", "", false, false
		d.pushHeldStart(hs)
	}
}

func (d *Devices) heldStartUnconfirmed(ctx context.Context, machine string, seq int64, retried bool) {
	from := db.HeldFailed
	if retried {
		from = db.HeldWaiting
	}
	sctx, cancel := heldStoreCtx(ctx)
	ok, err := d.heldStore().SettleHeldOperation(sctx, seq, from, db.HeldFailed, db.HeldUnconfirmed, "")
	cancel()
	switch {
	case err != nil:
		d.logf("[%s] held start not left to owner yet: %v", machine, err)
	case !ok:
		d.heldStartSync(ctx, machine, seq)
	default:
		h := &d.heldStarts
		h.mu.Lock()
		if r := h.rowLocked(machine, seq); r != nil {
			r.State, r.Failure, r.Error = db.HeldFailed, db.HeldUnconfirmed, ""
		}
		h.mu.Unlock()
		d.logf("[%s] held start outcome unconfirmed: left to owner", machine)
	}
}

func (d *Devices) heldStartDeliver(ctx context.Context, raw json.RawMessage, lang string) (outcome, sessionID, reason string) {
	l := i18n.Lang(lang).Or(i18n.Default)
	var call devicewire.HeldStartCall
	if err := json.Unmarshal(raw, &call); err != nil {
		return devicewire.ReplayOutcomeRefused, "", heldStartReason(l, err)
	}
	cctx, cancel := context.WithTimeout(ctx, heldStartTimeout)
	defer cancel()
	credID := call.Principal.ID
	if call.Caller != nil && call.Caller.CredentialID != "" {
		credID = call.Caller.CredentialID
	}
	if credID != "" {
		d.heldStarts.mu.Lock()
		revoked := d.heldStarts.revoked[credID]
		d.heldStarts.mu.Unlock()
		if revoked {
			return devicewire.ReplayOutcomeSignedOut, "", ""
		}
		if d.opts.Hub == nil {
			return devicewire.ReplayOutcomeNotSent, "", ""
		}
		revoked, err := bus.CredentialRevoked(cctx, d.opts.Hub.Call, credID)
		if err != nil {
			d.logf("check held start's device credential: %v", err)
			return devicewire.ReplayOutcomeNotSent, "", ""
		}
		if revoked {
			d.heldStartCredentialRevoked(credID)
			return devicewire.ReplayOutcomeSignedOut, "", ""
		}
	}
	if d.opts.Hub == nil {
		return devicewire.ReplayOutcomeNotSent, "", ""
	}
	reply, err := buswire.Call(cctx, d.opts.Hub, devicewire.ReplayHeldStartMethod, devicewire.ReplayHeldStartRequest{Call: call})
	if err != nil {
		if errors.Is(err, bus.ErrOffline) || heldNotWritten(err) {
			return devicewire.ReplayOutcomeNotSent, "", ""
		}
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == bus.CodeUnknownMethod {
			return devicewire.ReplayOutcomeNotSent, "", ""
		}
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return devicewire.ReplayOutcomeUnclear, "", ""
		}
		return devicewire.ReplayOutcomeUnclear, "", ""
	}
	if reply.Outcome == "" {
		return devicewire.ReplayOutcomeUnclear, "", ""
	}
	return reply.Outcome, reply.SessionID, reply.Reason
}

func heldStartReason(l i18n.Lang, err error) string {
	var re *rpc.Error
	if errors.As(err, &re) && cmp.Or(re.Message, re.Code) != "" {
		return textfmt.Clip(textfmt.OneLine(cmp.Or(re.Message, re.Code)), 300)
	}
	return i18n.T(l, "devices-held-starts.not_prepared")
}

func (d *Devices) heldStartSettle(ctx context.Context, machine string, seq int64, outcome, sessionID, reason string) bool {
	h := &d.heldStarts
	st := d.heldStore()
	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	h.mu.Lock()
	attempts := 0
	var before heldStartRow
	hasBefore := false
	if r := h.rowLocked(machine, seq); r != nil {
		attempts = r.Attempts
		before, hasBefore = *r, true
	}
	h.mu.Unlock()
	if outcome == devicewire.ReplayOutcomeStarted {
		ok, err := st.DeleteHeldTry(sctx, seq, attempts)
		switch {
		case err != nil:
			h.mu.Lock()
			h.dropLocked(machine, seq)
			h.gone[seq] = attempts
			h.mu.Unlock()
			d.logf("[%s] held start succeeded (%s) but stays in store: %v", machine, sessionID, err)
		case !ok:
			d.logf("[%s] held start succeeded (%s); store has it otherwise", machine, sessionID)
			d.heldStartSync(ctx, machine, seq)
		default:
			h.mu.Lock()
			h.dropLocked(machine, seq)
			h.mu.Unlock()
		}
		d.logf("[%s] held start launched session %s", machine, sessionID)
		if hasBefore {
			hs := before.wire(i18n.Lang(before.Lang).Or(i18n.Default), d.heldComputer(machine))
			hs.State = proto.HeldStartStarted
			hs.SessionID = sessionID
			hs.Failure, hs.Error, hs.CanRetry, hs.CanCancel = "", "", false, false
			d.pushHeldStart(hs)
		}
		return true
	}
	state, failure := db.HeldFailed, ""
	switch outcome {
	case devicewire.ReplayOutcomeNotSent:
		state, reason = db.HeldWaiting, ""
	case devicewire.ReplayOutcomeRefused:
		failure = db.HeldRefused
	case devicewire.ReplayOutcomeSignedOut:
		failure, reason = db.HeldSignedOut, ""
	default:
		failure, reason = db.HeldUnclear, ""
	}
	ok, err := st.SettleHeldTry(sctx, seq, attempts, state, failure, reason)
	var after heldStartRow
	hasAfter := false
	h.mu.Lock()
	if r := h.rowLocked(machine, seq); r != nil {
		r.State, r.Failure, r.Error = state, failure, reason
		r.stale, r.staleOwn = err != nil, err != nil
		after, hasAfter = *r, true
	}
	h.mu.Unlock()
	if err != nil {
		d.logf("[%s] held start outcome not kept yet: %v", machine, err)
		if hasAfter {
			d.pushHeldStartRow(after)
		}
		return false
	}
	if !ok {
		d.logf("[%s] held start outcome not kept: store has it otherwise", machine)
		d.heldStartSync(ctx, machine, seq)
		return false
	}
	if hasAfter {
		d.pushHeldStartRow(after)
	}
	if state == db.HeldWaiting {
		d.logf("[%s] held start waits again", machine)
		return false // computer went offline again
	}
	if failure == db.HeldRefused {
		d.logf("[%s] held start refused: %s", machine, reason)
	} else if failure == db.HeldUnclear {
		d.logf("[%s] held start outcome unclear: left to owner", machine)
	}
	return true // independent held starts on the same computer may still proceed
}

// ---- expiry, revocation, repair ----

func (d *Devices) heldStartExpire(ctx context.Context) {
	h := &d.heldStarts
	now := h.clock()
	type due struct {
		machine string
		seq     int64
	}
	var dues []due
	h.mu.Lock()
	for machine, rows := range h.rows {
		for _, r := range rows {
			if r.State == db.HeldWaiting && !r.checkFirst() && !r.taken() && !r.stale && r.expired(now) {
				r.busy = true
				dues = append(dues, due{machine, r.Seq})
			}
		}
	}
	h.mu.Unlock()
	for _, e := range dues {
		sctx, cancel := heldStoreCtx(ctx)
		ok, err := d.heldStore().SettleHeldOperation(sctx, e.seq, db.HeldWaiting, db.HeldFailed, db.HeldExpired, "")
		cancel()
		var snap heldStartRow
		changed := false
		h.mu.Lock()
		if r := h.rowLocked(e.machine, e.seq); r != nil {
			r.busy = false
			switch {
			case err != nil:
			case ok:
				r.State, r.Failure, r.Error = db.HeldFailed, db.HeldExpired, ""
				snap, changed = *r, true
			default:
				r.State, r.Failure, r.Error = db.HeldFailed, db.HeldUnclear, ""
				snap, changed = *r, true
			}
		}
		h.mu.Unlock()
		if err != nil {
			d.logf("[%s] held start expiry not kept: %v", e.machine, err)
		} else if ok {
			d.logf("[%s] held start expired after %s", e.machine, heldExpiry)
		}
		if changed {
			d.pushHeldStartRow(snap)
		}
		d.heldStartWake(e.machine)
	}
}

func (d *Devices) heldStartCredentialRevoked(id string) {
	h := &d.heldStarts
	h.mu.Lock()
	if h.revoked == nil {
		h.revoked = make(map[string]bool)
	}
	h.revoked[id] = true
	h.mu.Unlock()
	ctx := context.Background()
	if d.heldStartLoad(ctx) {
		d.heldStartFailRevoked(ctx)
	}
}

func (d *Devices) heldStartFailRevoked(ctx context.Context) {
	h := &d.heldStarts
	h.mu.Lock()
	if len(h.revoked) == 0 {
		h.mu.Unlock()
		return
	}
	var rows []db.HeldOperation
	for _, machineRows := range h.rows {
		for _, r := range machineRows {
			if r.CredentialID != "" && h.revoked[r.CredentialID] && !r.busy && !r.stale && r.State != db.HeldSending && r.Failure != db.HeldSignedOut {
				r.busy = true
				rows = append(rows, r.HeldOperation)
			}
		}
	}
	h.mu.Unlock()
	if len(rows) == 0 {
		return
	}
	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	st := d.heldStore()
	for _, row := range rows {
		ok, err := st.SettleHeldOperation(sctx, row.Seq, row.State, db.HeldFailed, db.HeldSignedOut, "")
		if err != nil {
			d.logf("fail held start from signed-out device: %v", err)
		} else if !ok {
			d.heldStartSync(ctx, row.MachineID, row.Seq)
		}
		var snap heldStartRow
		changed := false
		h.mu.Lock()
		if r := h.rowLocked(row.MachineID, row.Seq); r != nil {
			r.busy = false
			if ok {
				r.State, r.Failure, r.Error = db.HeldFailed, db.HeldSignedOut, ""
				snap, changed = *r, true
			}
		}
		h.mu.Unlock()
		if changed {
			d.pushHeldStartRow(snap)
		}
	}
}

func (d *Devices) heldStartSync(ctx context.Context, machine string, seq int64) {
	h := &d.heldStarts
	h.mu.Lock()
	r := h.rowLocked(machine, seq)
	var key heldStartRow
	if r != nil {
		key = *r
	}
	h.mu.Unlock()
	if r == nil {
		return
	}
	sctx, cancel := heldStoreCtx(ctx)
	op, found, err := d.heldStore().HeldOperationOf(sctx, db.HeldKindStart, key.MachineID, "", key.OpID)
	cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	r = h.rowLocked(machine, seq)
	switch {
	case r == nil:
	case err != nil:
		r.State, r.Failure, r.Error = db.HeldFailed, db.HeldUnclear, ""
	case !found:
		h.dropLocked(machine, seq)
	case op.State == db.HeldSending:
		r.State, r.Failure, r.Error, r.stale, r.staleOwn = db.HeldFailed, db.HeldUnclear, "", true, false
		r.Attempts = op.Attempts
		if !op.TriedAt.IsZero() {
			r.TriedAt = op.TriedAt
		}
	default:
		r.State, r.Failure, r.Error, r.HeldAt, r.stale = op.State, op.Failure, op.Error, op.HeldAt, false
	}
}

func (d *Devices) heldStartRepair(ctx context.Context) {
	h := &d.heldStarts
	now := h.clock()
	var dues []heldStartRow
	gone := map[int64]int{}
	h.mu.Lock()
	for machine, rows := range h.rows {
		for _, r := range rows {
			if r.stale && !r.taken() && !h.running[machine] && (r.staleOwn || heldTryOver(r.TriedAt, now)) {
				r.busy = true
				dues = append(dues, *r)
			}
		}
	}
	maps.Copy(gone, h.gone)
	type unsureEntry struct {
		key  heldStartKey
		done chan struct{}
	}
	var unsure []unsureEntry
	for k := range h.unsure {
		switch {
		case slices.ContainsFunc(h.rows[k.machine], func(r *heldStartRow) bool { return r.OpID == k.opID }):
			delete(h.unsure, k)
		case h.holding[k] > 0:
		default:
			done := make(chan struct{})
			h.repairing[k] = done
			unsure = append(unsure, unsureEntry{k, done})
		}
	}
	h.mu.Unlock()
	st := d.heldStore()
	for _, u := range unsure {
		sctx, cancel := heldStoreCtx(ctx)
		ok, err := st.DeleteUnsureHeld(sctx, db.HeldKindStart, u.key.machine, "", u.key.opID)
		cancel()
		h.mu.Lock()
		delete(h.repairing, u.key)
		close(u.done)
		if err == nil {
			delete(h.unsure, u.key)
		}
		h.mu.Unlock()
		if err == nil && ok {
			d.logf("[%s] held start %s the app was told failed, dropped", u.key.machine, u.key.opID)
		}
	}
	for seq, attempts := range gone {
		sctx, cancel := heldStoreCtx(ctx)
		_, err := st.DeleteHeldTry(sctx, seq, attempts)
		cancel()
		if err == nil {
			h.mu.Lock()
			delete(h.gone, seq)
			h.mu.Unlock()
		}
	}
	touched := map[string]bool{}
	for _, r := range dues {
		touched[r.MachineID] = true
		sctx, cancel := heldStoreCtx(ctx)
		ok, err := st.SettleHeldTry(sctx, r.Seq, r.Attempts, r.State, r.Failure, r.Error)
		cancel()
		h.mu.Lock()
		if row := h.rowLocked(r.MachineID, r.Seq); row != nil {
			row.busy = false
			if err == nil && ok {
				row.stale, row.staleOwn = false, false
			}
		}
		h.mu.Unlock()
		if err == nil && !ok {
			d.heldStartSync(ctx, r.MachineID, r.Seq)
		}
	}
	for machine := range touched {
		d.heldStartWake(machine)
	}
}

// ---- owner HTTP routes ----

func (h *heldStartBook) find(machine, opID string, deleting bool) (*heldStartRow, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.rows[machine] {
		if r.OpID != opID {
			continue
		}
		if r.State == db.HeldSending || r.busy || (r.checking && !deleting) {
			return nil, errHeldStartStartingNow
		}
		r.busy = true
		c := *r
		return &c, nil
	}
	return nil, nil
}

func (d *Devices) heldStartMirror(ctx context.Context, r *heldStartRow) (held, stale bool, attempts int, err error) {
	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	op, found, err := d.heldStore().HeldOperationOf(sctx, db.HeldKindStart, r.MachineID, "", r.OpID)
	if err != nil {
		return true, false, 0, err
	}
	h := &d.heldStarts
	h.mu.Lock()
	defer h.mu.Unlock()
	row := h.rowLocked(r.MachineID, r.Seq)
	switch {
	case !found:
		h.dropLocked(r.MachineID, r.Seq)
		return false, false, 0, nil
	case op.State == db.HeldSending:
		own := row != nil && row.stale && row.staleOwn && row.Attempts == op.Attempts
		if !h.running[r.MachineID] && (own || heldTryOver(op.TriedAt, h.clock())) {
			return true, true, op.Attempts, nil
		}
		return true, false, 0, errHeldStartStartingNow
	}
	if row != nil {
		row.State, row.Failure, row.Error, row.HeldAt, row.stale = op.State, op.Failure, op.Error, op.HeldAt, false
	}
	return true, false, 0, nil
}

func (d *Devices) heldStartRetry(ctx context.Context, machine, opID string) (bool, error) {
	if !d.heldStartLoad(ctx) {
		return false, nil
	}
	h := &d.heldStarts
	r, err := h.find(machine, opID, false)
	if r == nil {
		return err != nil, err
	}
	h.mu.Lock()
	signedOut := r.Failure == db.HeldSignedOut || (r.CredentialID != "" && h.revoked[r.CredentialID])
	if row := h.rowLocked(r.MachineID, r.Seq); signedOut && row != nil {
		row.busy = false
	}
	h.mu.Unlock()
	if signedOut {
		return true, errHeldStartSignedOut
	}
	now := h.clock()
	held, wrote, check := true, false, false
	if r.State != db.HeldWaiting {
		held, wrote, check, err = d.heldStartRetryStore(ctx, r, now)
	}
	var snap heldStartRow
	hasSnap := false
	h.mu.Lock()
	if row := h.rowLocked(r.MachineID, r.Seq); row != nil {
		row.busy = false
		row.checked, row.failedReads = time.Time{}, 0
		if wrote {
			row.State, row.Failure, row.Error, row.HeldAt, row.stale = db.HeldWaiting, "", "", now, false
			if check {
				row.Failure = db.HeldUnclear
			}
		}
		snap, hasSnap = *row, true
	}
	h.mu.Unlock()
	if hasSnap {
		d.pushHeldStartRow(snap)
	}
	d.heldStartWake(r.MachineID)
	switch {
	case err != nil:
		if !errors.Is(err, errHeldStartStartingNow) {
			d.logf("[%s] held start %s retry not kept: %v", machine, opID, err)
			err = errHeldStartStore
		}
		return true, err
	case !held:
		return false, nil
	}
	d.logf("[%s] held start %s retried by owner", machine, opID)
	return true, nil
}

func (d *Devices) heldStartRetryStore(ctx context.Context, r *heldStartRow, now time.Time) (held, wrote, check bool, err error) {
	st := d.heldStore()
	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	ok, check, err := st.RetryHeldOperation(sctx, r.Seq, now)
	if err != nil || ok {
		return true, ok, check, err
	}
	held, stale, attempts, err := d.heldStartMirror(ctx, r)
	if err != nil || !stale {
		return held, false, false, err
	}
	failure := db.HeldUnclear
	if r.stale && r.staleOwn && r.Attempts == attempts && r.Failure != db.HeldUnclear {
		failure = r.Failure
	}
	settled, err := st.SettleHeldTry(sctx, r.Seq, attempts, db.HeldFailed, failure, "")
	if err == nil && settled {
		if ok, check, err = st.RetryHeldOperation(sctx, r.Seq, now); err == nil && ok {
			return true, true, check, nil
		}
	}
	if err != nil {
		return true, false, false, err
	}
	if held, _, _, err = d.heldStartMirror(ctx, r); err == nil && held {
		err = errHeldStartStartingNow
	}
	return held, false, false, err
}

func (d *Devices) heldStartDelete(ctx context.Context, machine, opID string) (bool, error) {
	if !d.heldStartLoad(ctx) {
		return false, nil
	}
	h := &d.heldStarts
	r, err := h.find(machine, opID, true)
	if r == nil {
		return err != nil, err
	}
	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	gone, err := d.heldStartDeleteStore(ctx, sctx, r)
	h.mu.Lock()
	if err == nil && !gone {
		err = errHeldStartStartingNow
	}
	if err != nil {
		if row := h.rowLocked(r.MachineID, r.Seq); row != nil {
			row.busy = false
		}
		h.mu.Unlock()
		if !errors.Is(err, errHeldStartStartingNow) {
			d.logf("[%s] held start %s delete not kept: %v", machine, opID, err)
			err = errHeldStartStore
		}
		d.heldStartWake(r.MachineID)
		return true, err
	}
	snap := *r
	h.dropLocked(r.MachineID, r.Seq)
	h.mu.Unlock()
	d.logf("[%s] held start %s deleted by owner", machine, opID)
	hs := snap.wire(i18n.Lang(snap.Lang).Or(i18n.Default), d.heldComputer(machine))
	hs.State = proto.HeldStartDeleted
	hs.Failure, hs.Error, hs.CanRetry, hs.CanCancel = "", "", false, false
	d.pushHeldStart(hs)
	d.heldStartWake(r.MachineID)
	return true, nil
}

func (d *Devices) heldStartDeleteStore(ctx, sctx context.Context, r *heldStartRow) (gone bool, err error) {
	st := d.heldStore()
	ok, err := st.DeleteHeldOperation(sctx, r.Seq)
	if err != nil || ok {
		return ok, err
	}
	held, stale, attempts, err := d.heldStartMirror(ctx, r)
	switch {
	case err != nil:
		return false, err
	case !held:
		return true, nil
	case !stale:
		return false, errHeldStartStartingNow
	}
	if ok, err = st.DeleteHeldTry(sctx, r.Seq, attempts); err != nil || ok {
		return ok, err
	}
	if held, _, _, err = d.heldStartMirror(ctx, r); err != nil {
		return false, err
	}
	return !held, nil
}

func heldStartPersonAllowed(r *http.Request) bool {
	return r.Header.Get(assistantwire.RoleHeader) == "full" && r.Header.Get(assistantwire.PersonHeader) == "1"
}

// listHeldStarts: GET /api/v1/machines/{mid}/held-starts → proto.HeldStartList.
func (d *Devices) listHeldStarts(w http.ResponseWriter, r *http.Request, m *Machine) {
	w.Header().Set("Cache-Control", "no-store")
	if !heldStartPersonAllowed(r) {
		writeErrCode(w, http.StatusForbidden, proto.CodeReadOnly, i18n.Err("devices-held-starts.read_only"))
		return
	}
	if d.heldStore() == nil {
		writeJSON(w, proto.HeldStartList{MachineID: m.cfg.ID, Computer: m.name(), Starts: []proto.HeldStart{}})
		return
	}
	h := &d.heldStarts
	h.mu.Lock()
	if !h.loaded {
		h.mu.Unlock()
		writeErrCode(w, http.StatusServiceUnavailable, "held_starts_unavailable", i18n.Err("devices-held-starts.unavailable"))
		return
	}
	rows := h.rows[m.cfg.ID]
	snap := make([]heldStartRow, len(rows))
	for i, row := range rows {
		snap[i] = *row
	}
	h.mu.Unlock()
	lang := i18n.From(r.Context()).Or(i18n.Default)
	computer := m.name()
	out := proto.HeldStartList{MachineID: m.cfg.ID, Computer: computer, Starts: make([]proto.HeldStart, 0, len(snap))}
	for i := range snap {
		out.Starts = append(out.Starts, snap[i].wire(lang, computer))
	}
	writeJSON(w, out)
}

// postHeldStartRetry: POST /api/v1/machines/{mid}/held-starts/{op}/retry → {"ok":true}.
func (d *Devices) postHeldStartRetry(w http.ResponseWriter, r *http.Request, m *Machine) {
	d.heldStartRoute(w, r, m, d.heldStartRetry)
}

// deleteHeldStart: DELETE /api/v1/machines/{mid}/held-starts/{op} → {"ok":true}.
func (d *Devices) deleteHeldStart(w http.ResponseWriter, r *http.Request, m *Machine) {
	d.heldStartRoute(w, r, m, d.heldStartDelete)
}

func (d *Devices) heldStartRoute(w http.ResponseWriter, r *http.Request, m *Machine,
	act func(ctx context.Context, machine, opID string) (bool, error)) {
	if !heldStartPersonAllowed(r) {
		writeErrCode(w, http.StatusForbidden, proto.CodeReadOnly, i18n.Err("devices-held-starts.read_only"))
		return
	}
	found, err := act(r.Context(), m.cfg.ID, r.PathValue("op"))
	switch {
	case errors.Is(err, errHeldStartStartingNow), errors.Is(err, errHeldStartSignedOut):
		writeErrCode(w, http.StatusConflict, "conflict", err)
	case err != nil:
		writeErr(w, http.StatusServiceUnavailable, err)
	case !found:
		writeErr(w, http.StatusNotFound, i18n.Err("devices-held-starts.not_found"))
	default:
		writeJSON(w, map[string]any{"ok": true})
	}
}
