package devices

// The owner's messages to an Agent whose computer is away (#10661).
//
// The hub marks the owner's message to an existing Agent (not an assistant conversation) as one that may be held
// (NodeCall.Hold). When the computer's node is not connected, so nothing was written, or older held messages of the
// same session still wait, the manager keeps the call as it came in its Postgres (held_operations) and answers
// CodeHeld: the hub tells the app it goes later, and every client draws it on the session's row
// (proto.Session.Undelivered with Computer set, read through HeldMessages). Once the node is back (configured and
// listed) the held calls go at once, per session in the order they were held, each exactly as the hub sent it: Call
// again with its caller, submission and share authority, under the same client_msg_id. Sent, a message leaves the row.
//
// Only a try known to have written nothing goes again by itself (the node not connected, closed before the write).
// A try whose outcome is unknown (the connection dropped with it out, its time ran out, the node said delivered
// "unknown", the manager stopped during it) never does: it fails as unclear, and the later ones of its session wait
// behind it until the owner retries or deletes it, or its transcript, read once per connection, has a line the node
// tied to its client_msg_id (it did arrive: it leaves the rows). A delivery is judged on the node's call alone: refused when the
// node refused it or it was never asked (its preparation here failed), with why; delivered once the node took it.
// A message still waiting for its computer a day after it was held fails as expired (not one behind a message that may
// have arrived: that one waits for the owner). Retry and Delete are the owner's
// (POST …/held/{msg}/retry, DELETE …/held/{msg}; the assistant's undelivered routes end here for older apps).

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// heldStore keeps the held messages (db.DB); a store without it holds none: a send fails as before.
type heldStore interface {
	HoldOperation(ctx context.Context, op db.HeldOperation) (db.HeldOperation, bool, error)
	HeldOperationOf(ctx context.Context, kind, machine, session, opID string) (db.HeldOperation, bool, error)
	HeldOperations(ctx context.Context, kind string) ([]db.HeldOperation, error)
	RecoverHeldOperations(ctx context.Context, kind string) (int64, error)
	ClaimHeldOperation(ctx context.Context, seq int64, at time.Time) (db.HeldOperation, bool, error)
	SettleHeldOperation(ctx context.Context, seq int64, from, state, failure, reason string) (bool, error)
	SettleHeldTry(ctx context.Context, seq int64, attempts int, state, failure, reason string) (bool, error)
	DeleteHeldTry(ctx context.Context, seq int64, attempts int) (bool, error)
	RetryHeldOperation(ctx context.Context, seq int64, at time.Time) (ok, check bool, err error)
	RenewHeldOperation(ctx context.Context, seq int64, at time.Time) (bool, error)
	DeleteHeldOperation(ctx context.Context, seq int64, states ...string) (bool, error)
	DeleteUnsureHeld(ctx context.Context, kind, machine, session, opID string) (bool, error)
}

// Vars so tests change them.
var (
	heldExpiry      = 24 * time.Hour   // how long a message waits for its computer
	heldTick        = 15 * time.Second // how often the held messages are looked at without anything waking them
	heldSendTimeout = 5 * time.Minute  // one try (an Agent resumed to take it can take minutes)
	heldStoreWait   = 5 * time.Second  // one write to the store
	// heldListWait: a message waits for its session in the node's list this long into a connection (an Agent of a
	// runtime comes back a moment after its node), then goes anyway and the node's answer decides.
	heldListWait = 2 * time.Minute
	// heldLoadBackoff: after the store could not be read, it is not asked again this long (a send goes as before
	// meanwhile, without waiting on it).
	heldLoadBackoff = 5 * time.Second
	// heldBlockedRenew: a message held behind one that may have arrived waits for the owner, not for its computer: its
	// wait starts again this often, so it has nearly a whole day once the owner decides.
	heldBlockedRenew = time.Hour
	heldCheckWait    = 20 * time.Second // reading echo evidence for a message that may have arrived
)

var errEchoStatusUnsupported = errors.New("node does not support held-message echo checks")

// heldCheckTries is how many times in a row a transcript read for a message that may have arrived may fail before the
// message is left to the owner (unconfirmed).
const heldCheckTries = 3

// heldTryOver: a try begun at tried, by any manager, is over by now (a try takes heldSendTimeout at most).
func heldTryOver(tried, now time.Time) bool { return now.Sub(tried) > heldSendTimeout+time.Minute }

// codeHeldNotKept: an eligible message that had to wait behind held ones could not be kept (the store failed);
// nothing was typed.
const codeHeldNotKept = "held_not_kept"

// heldPerSession bounds the messages held for one session (waiting or failed): a new one beyond it is refused
// (codeHeldFull), nothing written. The owner's failed ones are never dropped to make room.
const heldPerSession = 50

// codeHeldFull: a message that would be held was refused, nothing typed: its session holds heldPerSession already.
const codeHeldFull = "held_full"

// codeHeldAhead: a message kept in order (NodeCall.Ordered) without Hold was refused, nothing typed, while older ones
// of its session are held: it would have overtaken them.
const codeHeldAhead = "held_ahead"

// heldMaxBytes bounds one held call (its words and pictures as the app sent them): a bigger one is not held and the
// app gets the failure as before.
const heldMaxBytes = 16 << 20

// heldRow is one held message as the manager has it.
type heldRow struct {
	db.HeldOperation
	text   string
	images int
	// busy: the owner's Retry or Delete, or its expiry, is writing it now; it and the later ones of its session wait
	// until that is done.
	busy bool
	// checked: the connection (its start) on which the session's transcript was read for this one, which may have
	// arrived: one read per connection.
	checked time.Time
	// checking: its transcript is being read (heldCheck): it holds the later ones back and a Retry answers 409; a Delete
	// may go (the read's own writes then find nothing). failedReads: reads that failed in a row (since its last Retry).
	checking    bool
	failedReads int
	// stale: the store still has it as being sent, while what was decided of it here is its state (a write after
	// its try failed, or a claim whose answer was lost). It holds the later ones of its session back until heldRepair
	// writes it. staleOwn: this manager made that try, so nobody else is sending it.
	stale, staleOwn bool
}

// newHeldRow is op as the manager keeps it: what its row shows, not its call (pictures up to heldMaxBytes), which is
// read from the store when it goes.
func newHeldRow(op db.HeldOperation) *heldRow {
	r := &heldRow{HeldOperation: op, text: op.Text, images: op.Images}
	r.Call = nil
	return r
}

// unclear: a try of it may have arrived; the later ones of its session wait behind it.
func (r *heldRow) unclear() bool { return r.State == db.HeldFailed && r.Failure == db.HeldUnclear }

// checkFirst reports whether the owner retried an unclear send. The node's echo evidence decides whether it
// arrived, can be sent again, or still needs the owner's decision.
func (r *heldRow) checkFirst() bool { return r.State == db.HeldWaiting && r.Failure == db.HeldUnclear }

// awaitsOwner: it may have arrived (unclear, unconfirmed): the owner decides, and the later ones of its session wait
// behind it.
func (r *heldRow) awaitsOwner() bool {
	return r.State == db.HeldFailed && (r.Failure == db.HeldUnclear || r.Failure == db.HeldUnconfirmed)
}

// taken: something writes it now (the owner's change, the expiry, the repair) or reads its transcript: the later ones
// of its session wait, and nothing else picks it.
func (r *heldRow) taken() bool { return r.busy || r.checking }

// expired: it has waited a whole day; heldExpire fails it, nothing sends it.
func (r *heldRow) expired(now time.Time) bool { return now.Sub(r.HeldAt) >= heldExpiry }

// heldBook is the held messages, read from the store once and kept in step with it.
type heldBook struct {
	loadMu  sync.Mutex // one load at a time
	mu      sync.Mutex
	loaded  bool
	failed  time.Time             // when the last load failed (heldLoadBackoff)
	ctx     context.Context       // the deliveries' (heldLoop's); nil before it runs
	rows    map[string][]*heldRow // by machine, in the order they were held
	revoked map[string]bool       // credentials this manager was told were signed out
	// sending: the sessions (machine + "/" + session) a delivery runs in: one at a time each.
	sending map[string]bool
	// gone: messages sent whose row the store kept (its delete failed), by seq, with the attempts of that try:
	// heldRepair deletes them.
	gone map[int64]int
	// unsure: holds the store may have kept though neither its answer nor a look-up said so: the app was told they
	// failed, so heldRepair drops them (untried, waiting) unless held here since.
	unsure map[heldKey]bool
	// holding: holds of a key under way (hold), which the repair leaves alone; repairing: unsure keys the repair is
	// deleting now, closed when done, which a hold of that key waits for.
	holding   map[heldKey]int
	repairing map[heldKey]chan struct{}
	waiting   int              // holds waiting for such a delete now
	now       func() time.Time // tests
}

// heldKey names a held message: its machine, session and client_msg_id.
type heldKey struct{ machine, session, id string }

func (h *heldBook) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func (d *Devices) heldStore() heldStore {
	s, _ := d.opts.Store.(heldStore)
	return s
}

// heldStoreCtx bounds one store write, which goes on when ctx ended (what was decided is kept).
func heldStoreCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), heldStoreWait)
}

// heldLoad reads the held messages once, after failing as unclear what a stopped manager left being sent; false
// while they cannot be read (nothing is held then: a send fails as before).
func (d *Devices) heldLoad(ctx context.Context) bool {
	h := &d.held
	// Read, or failed a moment ago (every holdable send asks: they must not wait on a store that is down, one after
	// another).
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
		d.logf("held messages: %v", err)
		h.mu.Lock()
		h.failed = h.clock()
		h.mu.Unlock()
		return false
	}
	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	// Nothing of this manager's is being sent before the load: every row left sending is a stopped manager's.
	if n, err := st.RecoverHeldOperations(sctx, db.HeldKindMessage); err != nil {
		return fail(err)
	} else if n > 0 {
		d.logf("held messages: %d left being sent by a stopped manager, marked unclear", n)
	}
	ops, err := st.HeldOperations(sctx, db.HeldKindMessage)
	if err != nil {
		return fail(err)
	}
	rows := map[string][]*heldRow{}
	for _, op := range ops {
		rows[op.MachineID] = append(rows[op.MachineID], newHeldRow(op))
	}
	h.mu.Lock()
	h.rows, h.sending, h.gone, h.unsure, h.loaded = rows, map[string]bool{}, map[int64]int{}, map[heldKey]bool{}, true
	h.holding, h.repairing = map[heldKey]int{}, map[heldKey]chan struct{}{}
	h.mu.Unlock()
	for machine := range rows { // rows drawn before the load showed none
		d.heldChanged(machine)
	}
	return true
}

// heldLoop loads the held messages, then sends what can go, fails what waited too long, until ctx ends.
func (d *Devices) heldLoop(ctx context.Context) {
	if d.heldStore() == nil {
		return
	}
	h := &d.held
	h.mu.Lock()
	h.ctx = ctx
	h.mu.Unlock()
	d.heldStarts.mu.Lock()
	d.heldStarts.ctx = ctx
	d.heldStarts.mu.Unlock()
	t := time.NewTicker(heldTick)
	defer t.Stop()
	for {
		if d.heldLoad(ctx) {
			d.heldRepair(ctx)
			d.heldFailRevoked(ctx)
			d.heldExpire(ctx)
			h.mu.Lock()
			machines := make([]string, 0, len(h.rows))
			for machine := range h.rows {
				machines = append(machines, machine)
			}
			h.mu.Unlock()
			for _, machine := range machines {
				d.heldWake(machine)
			}
		}
		d.heldStartTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// heldChanged has machine's rows published again with what is held for them now (Options.HeldChanged).
func (d *Devices) heldChanged(machine string) {
	if f := d.opts.HeldChanged; f != nil {
		go f(machine)
	}
}

// ---- holding ----

// heldCall is a session.send the hub marked as one that must not overtake held messages (NodeCall.Ordered), or that
// the manager may hold (NodeCall.Hold), as it would be kept.
type heldCall struct {
	machine, session, id string
	lang                 i18n.Lang
	hold                 bool                // it may be held (Hold); else only kept in order (Ordered)
	call                 devicewire.NodeCall // as it came, but for Hold, TimeoutMS and Request
}

// heldCallOf is p as a call kept in order behind held messages, or held; nil when it is neither: not a session.send
// the hub marked, without a session or its own client_msg_id, or no store to keep it in.
func (d *Devices) heldCallOf(ctx context.Context, m *Machine, p devicewire.NodeCall) *heldCall {
	if p.Method != "session.send" || p.Hold == nil && !p.Ordered || d.heldStore() == nil {
		return nil
	}
	var ids struct {
		SessionID string `json:"session_id"`
		ID        string `json:"client_msg_id"`
	}
	if json.Unmarshal(p.Params, &ids) != nil || ids.SessionID == "" || ids.ID == "" {
		return nil
	}
	call := p
	// Kept without what made it held or ordered: its delivery is the call itself, never held or refused again.
	call.Hold, call.Ordered, call.TimeoutMS, call.Request = nil, false, 0, ""
	c := &heldCall{machine: m.cfg.ID, session: ids.SessionID, id: ids.ID, lang: i18n.From(ctx), call: call}
	if p.Hold != nil {
		c.hold, c.lang = true, i18n.Lang(p.Hold.Lang)
	}
	return c
}

// heldNotWritten: err proves the call wrote nothing to the node. Only such a call may be held, or sent again by
// itself; anything less sure is taken as one that may have arrived. What proves it:
//   - errOffline: callOnce found no connection, or one already closed, before the call;
//   - rpc.ErrNotSent: marked only where nothing was written yet (the call's wait for the connection, for the writer or
//     the reload ending first; the connection closing while the call waited to write);
//   - rpc.ErrSuspended: refused before the call was registered;
//   - a hop further on answering machine_offline (it never went out there).
//
// Everything Call codes machine_offline (the hub's 503 node_unreachable, delivered "none") is one of these: on this
// path rpc.ErrClosed comes only as NotSent(ErrClosed) or as ErrDropped. Not taken: a bare ErrClosed (a wrapper that
// lost ErrDropped's identity would make a possible delivery look unwritten), and rpc.ErrNotWritten alone, which a
// writer's own context error after part of a frame went out (websocket.NetConn) also yields.
func heldNotWritten(err error) bool {
	if errors.Is(err, errOffline) || errors.Is(err, rpc.ErrNotSent) || errors.Is(err, rpc.ErrSuspended) {
		return true
	}
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == devicewire.CodeMachineOffline
}

// heldMayHold: a call that failed with err is held. Not when its caller went away (cancelled): nobody waits for the
// answer, and its retry is held then.
func heldMayHold(err error) bool { return heldNotWritten(err) && !errors.Is(err, context.Canceled) }

// holdBehind holds c without calling the node when it must not overtake what its session holds (a message waiting,
// being sent, changed by the owner, or one that may have arrived), or answers the one c is held as already. A call
// only kept in order (no Hold: an app that does not draw held messages) is refused there instead (codeHeldAhead),
// nothing written. nil: c goes to the node.
func (d *Devices) holdBehind(ctx context.Context, c *heldCall) error {
	if !d.heldLoad(ctx) {
		return nil // cannot tell: as before
	}
	h := &d.held
	h.mu.Lock()
	held, blocked := false, false
	for _, r := range h.rows[c.machine] {
		if r.SessionID != c.session {
			continue
		}
		if r.OpID == c.id {
			held = true
			break
		}
		if r.State != db.HeldFailed || r.awaitsOwner() || r.taken() || r.stale {
			blocked = true
		}
	}
	h.mu.Unlock()
	switch {
	case held:
		return d.heldAnswer(c.machine, c.session, c.id, c.lang)
	case !blocked:
		return nil
	case !c.hold:
		return &rpc.Error{Code: codeHeldAhead, Message: i18n.T(c.lang, "devices-held.ahead"), Data: json.RawMessage(`{"delivered":"none"}`)}
	}
	if err := d.hold(ctx, c); err != nil {
		return err
	}
	// Not kept, and it must not go before the ones waiting: nothing was typed, the app may try again.
	return &rpc.Error{Code: codeHeldNotKept, Message: i18n.T(c.lang, "devices-held.not_kept"),
		Data: json.RawMessage(`{"delivered":"none"}`)}
}

// hold keeps c and answers CodeHeld; nil when it could not be kept (nor the held ones read).
func (d *Devices) hold(ctx context.Context, c *heldCall) error {
	if !d.heldLoad(ctx) {
		return nil
	}
	h := &d.held
	key := heldKey{c.machine, c.session, c.id}
	h.mu.Lock()
	for ch := h.repairing[key]; ch != nil; ch = h.repairing[key] {
		// The repair deletes this key's row the store may have kept unsure: held after that, never before it.
		h.waiting++
		h.mu.Unlock()
		wctx, cancel := heldStoreCtx(ctx)
		select {
		case <-ch:
		case <-wctx.Done():
		}
		cancel()
		h.mu.Lock()
		h.waiting--
		if wctx.Err() != nil && h.repairing[key] == ch {
			h.mu.Unlock()
			return nil
		}
	}
	h.holding[key]++ // the repair leaves this key alone meanwhile
	defer func() {
		h.mu.Lock()
		if h.holding[key]--; h.holding[key] <= 0 {
			delete(h.holding, key)
		}
		h.mu.Unlock()
	}()
	n, known := 0, false
	for _, r := range h.rows[c.machine] {
		if r.SessionID == c.session {
			n, known = n+1, known || r.OpID == c.id
		}
	}
	h.mu.Unlock()
	if !known && n >= heldPerSession {
		d.logf("[%s] %s: a message not held: %d held there already", c.machine, c.session, n)
		return &rpc.Error{Code: codeHeldFull, Message: i18n.T(c.lang, "devices-held.full"), Data: json.RawMessage(`{"delivered":"none"}`)}
	}
	raw, err := json.Marshal(c.call)
	if err != nil || len(raw) > heldMaxBytes {
		d.logf("[%s] %s: a message too large to hold (%d bytes)", c.machine, c.session, len(raw))
		return nil
	}
	now := h.clock().UTC()
	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	st := d.heldStore()
	op, fresh, err := st.HoldOperation(sctx, db.HeldOperation{Kind: db.HeldKindMessage, OpID: c.id, MachineID: c.machine,
		SessionID: c.session, Call: raw, Lang: string(c.lang), SentAt: now, HeldAt: now})
	if err != nil {
		// It may be kept all the same (the answer lost after the commit): one look by its key decides, else a row the
		// app was told failed would go after the next start, maybe after the owner sent it again.
		lctx, lcancel := heldStoreCtx(ctx)
		kept, found, lerr := st.HeldOperationOf(lctx, db.HeldKindMessage, c.machine, c.session, c.id)
		lcancel()
		if lerr != nil { // it may be kept, and the app is told it failed: heldRepair drops it unless held since
			h.mu.Lock()
			h.unsure[key] = true
			h.mu.Unlock()
		}
		if lerr != nil || !found {
			d.logf("[%s] %s: a message could not be held: %v", c.machine, c.session, err)
			return nil
		}
		d.logf("[%s] %s: a message was held, though its store said %v", c.machine, c.session, err)
		op, fresh = kept, true
	}
	h.mu.Lock()
	delete(h.unsure, key) // held here now: not one to drop
	rows := h.rows[op.MachineID]
	i, found := slices.BinarySearchFunc(rows, op.Seq, func(r *heldRow, seq int64) int { return cmp.Compare(r.Seq, seq) })
	if !found {
		h.rows[op.MachineID] = slices.Insert(rows, i, newHeldRow(op))
	}
	n = len(h.rows[op.MachineID])
	revoked := h.revoked[op.CredentialID]
	h.mu.Unlock()
	if revoked {
		d.heldFailRevoked(ctx)
	}
	if fresh {
		d.logf("[%s] %s: a message held until its computer is back (%d held there)", c.machine, c.session, n)
		d.heldChanged(c.machine)
		d.heldWake(c.machine) // it may be back already
	}
	return d.heldAnswer(op.MachineID, op.SessionID, op.OpID, c.lang)
}

// heldAnswer is CodeHeld with held message id as its row shows it.
func (d *Devices) heldAnswer(machine, session, id string, lang i18n.Lang) error {
	h := &d.held
	var row heldRow
	found, behind := false, false
	h.mu.Lock()
	for _, r := range h.rows[machine] {
		if r.SessionID != session {
			continue
		}
		if r.OpID == id {
			row, found = *r, true
			break
		}
		behind = behind || r.awaitsOwner()
	}
	h.mu.Unlock()
	name := d.heldComputer(machine)
	msg := proto.UndeliveredMessage{ID: id, State: proto.UndeliveredWaiting, Computer: name, // gone meanwhile (sent, deleted)
		Error: i18n.T(lang, "devices-held.waiting", i18n.Args{"name": name})}
	if found {
		msg = row.wire(lang, name, behind)
	}
	data, _ := json.Marshal(devicewire.HeldAnswer{Delivered: "later", Undelivered: &msg})
	return &rpc.Error{Code: devicewire.CodeHeld, Message: msg.Error, Data: data}
}

// ---- what the rows show ----

// heldComputer is what machine is called now.
func (d *Devices) heldComputer(machine string) string {
	if m := d.machineByID(machine); m != nil {
		return m.name()
	}
	return machine
}

// HeldMessages is assistant.Devices': the messages held for machine's Agents, by session, oldest first, each with the
// line under it in lang; nil when there are none. It runs on every decoration of the rows: from memory.
func (d *Devices) HeldMessages(machine string, lang i18n.Lang) map[string][]proto.UndeliveredMessage {
	h := &d.held
	h.mu.Lock()
	rows := h.rows[machine]
	if len(rows) == 0 {
		h.mu.Unlock()
		return nil
	}
	snap := make([]heldRow, len(rows))
	for i, r := range rows {
		snap[i] = *r
	}
	h.mu.Unlock()
	name := d.heldComputer(machine)
	out := map[string][]proto.UndeliveredMessage{}
	behind := map[string]bool{} // a message of the session before it may have arrived
	for i := range snap {
		r := &snap[i]
		out[r.SessionID] = append(out[r.SessionID], r.wire(lang, name, behind[r.SessionID]))
		if r.awaitsOwner() {
			behind[r.SessionID] = true
		}
	}
	return out
}

// wire is r as its row shows it: the owner's message with the line under it.
func (r *heldRow) wire(lang i18n.Lang, computer string, behind bool) proto.UndeliveredMessage {
	m := proto.UndeliveredMessage{ID: r.OpID, Text: r.text, Images: r.images, At: r.SentAt, State: proto.UndeliveredWaiting,
		Computer: computer}
	switch {
	case r.State == db.HeldFailed:
		// Every way it stopped is failed on the wire: an older app takes any other state for one sent by itself.
		m.State = proto.UndeliveredFailed
		switch r.Failure {
		case db.HeldRefused:
			reason := localizeHeldReason(lang, r.Error)
			if reason == "" {
				reason = i18n.T(lang, "devices-held.no_reason")
			}
			m.Error = i18n.T(lang, "devices-held.refused", i18n.Args{"reason": reason})
		case db.HeldExpired:
			m.Error = i18n.T(lang, "devices-held.expired", i18n.Args{"name": computer})
		case db.HeldUnconfirmed:
			m.Error = i18n.T(lang, "devices-held.unconfirmed")
		case db.HeldSignedOut:
			m.Error = i18n.T(lang, "undelivered.signed_out")
			m.DeleteOnly = true
		default:
			m.Error = i18n.T(lang, "devices-held.unclear")
		}
	case r.State == db.HeldSending:
		m.Error = i18n.T(lang, "devices-held.sending")
	case behind:
		m.Error = i18n.T(lang, "devices-held.behind")
	case r.checkFirst():
		m.Error = i18n.T(lang, "devices-held.check", i18n.Args{"name": computer})
	default:
		m.Error = i18n.T(lang, "devices-held.waiting", i18n.Args{"name": computer})
	}
	return m
}

// localizeHeldReason translates the node's generic terminal delivery and dialog refusal sentences into the viewer's
// language while preserving any dialog headline and specific diagnostics verbatim.
func localizeHeldReason(lang i18n.Lang, reason string) string {
	switch reason {
	case i18n.T(i18n.Default, "rpc-errors.not_delivered"):
		return i18n.T(lang, "rpc-errors.not_delivered")
	case i18n.T(i18n.Default, "rpc-errors.not_delivered_finished"):
		return i18n.T(lang, "rpc-errors.not_delivered_finished")
	}
	for _, key := range []string{"rpc-errors.not_delivered_botmux_owner_unresolved", "rpc-errors.not_delivered_botmux_owner_mismatch",
		"rpc-errors.not_delivered_botmux_owner_refused"} {
		if reason == i18n.T(i18n.Default, key) {
			return i18n.T(lang, key)
		}
	}
	if lang == i18n.ZhHans {
		return reason
	}
	const (
		dialogBare   = "电脑上的终端正开着一个对话框（确认、选择列表等），这条消息没有发出去：先回答或关掉它再发。" // i18n-ignore: matches node dialog_open refusal
		dialogPrefix = "电脑上的终端正开着一个对话框（「"                              // i18n-ignore: matches node dialog_open refusal
		dialogMid    = "」），这条消息没有发出去："                                 // i18n-ignore: matches node dialog_open refusal
		dialogNotice = "它只是一条通知，按回车就关掉，可以让发送先关掉它再发。"                   // i18n-ignore: matches node dialog_open refusal
	)
	if reason == dialogBare {
		return i18n.T(lang, "rpc-errors.dialog_open")
	}
	if rest, ok := strings.CutPrefix(reason, dialogPrefix); ok {
		if head, tail, ok := strings.Cut(rest, dialogMid); ok && head != "" {
			if tail == dialogNotice {
				return i18n.T(lang, "rpc-errors.dialog_open_notice") + " (" + head + ")"
			}
			return i18n.T(lang, "rpc-errors.dialog_open") + " (" + head + ")"
		}
	}
	return reason
}

// ---- sending ----

// heldHead is what leads a session's held messages (headLocked).
type heldHead int

const (
	heldHeadNone    heldHead = iota // nothing to do now: none waits, or one ahead is being sent or changed
	heldHeadSend                    // the next one to send
	heldHeadUnclear                 // one that may have arrived holds the rest back
)

// headLocked is what leads the session's held messages: the first that goes next or holds the rest back. One being
// sent, or the owner's (Retry, Delete) or the expiry's to change now, holds the later ones back whatever its state
// (heldHeadNone); one that may have arrived too (heldHeadUnclear); a refused or expired one does not. h.mu is held.
func (h *heldBook) headLocked(machine, session string) (*heldRow, heldHead) {
	for _, r := range h.rows[machine] {
		if r.SessionID != session {
			continue
		}
		switch {
		case r.taken(), r.stale, r.State == db.HeldSending:
			return r, heldHeadNone
		case r.unclear():
			return r, heldHeadUnclear
		case r.awaitsOwner(): // unconfirmed: looked for already; the owner decides
			return r, heldHeadNone
		case r.State == db.HeldWaiting:
			return r, heldHeadSend
		}
	}
	return nil, heldHeadNone
}

// heldWake starts on machine what its held messages can do now: per session, sending the next one (heldOn says the
// node takes it), or reading the transcript once on this connection for one that may have arrived (heldCheck); one
// at a time per session, the sessions apart. Called when its node's session list changes (a new connection's first
// list once its node is configured: what was held while it was away goes then), on a Retry, a new held message, and
// every heldTick.
func (d *Devices) heldWake(machine string) {
	h := &d.held
	h.mu.Lock()
	ctx := h.ctx
	if !h.loaded || ctx == nil || ctx.Err() != nil || len(h.rows[machine]) == 0 {
		h.mu.Unlock()
		return
	}
	type due struct {
		session string
		head    heldHead
		row     heldRow
	}
	var sessions []string
	for _, r := range h.rows[machine] {
		if !slices.Contains(sessions, r.SessionID) {
			sessions = append(sessions, r.SessionID)
		}
	}
	now := h.clock()
	var dues []due
	for _, session := range sessions {
		if h.sending[machine+"/"+session] {
			continue
		}
		if r, head := h.headLocked(machine, session); head != heldHeadNone {
			dues = append(dues, due{session, head, *r})
		}
	}
	h.mu.Unlock()
	if len(dues) == 0 { // only failed ones, or every session's delivery runs: nothing to look up
		return
	}
	m := d.machineByID(machine)
	if m == nil {
		return
	}
	dues = slices.DeleteFunc(dues, func(e due) bool { // not under h.mu
		listed, ready := m.heldOn(e.session)
		return heldActOn(&e.row, e.head, listed, ready, now) == heldDoNothing
	})
	var start []string
	h.mu.Lock()
	for _, e := range dues {
		if key := machine + "/" + e.session; !h.sending[key] {
			h.sending[key] = true
			start = append(start, e.session)
		}
	}
	h.mu.Unlock()
	for _, session := range start {
		d.workers.Add(1)
		go func() {
			defer d.workers.Done()
			d.heldSend(ctx, machine, session)
		}()
	}
}

// heldAction is what a session's held messages need now (heldActOn).
type heldAction int

const (
	heldDoNothing heldAction = iota
	heldDoSend               // send its head
	heldDoCheck              // read the transcript for its head first
)

// heldActOn is what the session's head r (headLocked) needs now, given what its node offers (heldOn). One that may
// have arrived has the transcript read: failed (unclear), once per connection that lists its session; retried after
// that (checkFirst), once its node takes a message (listed, or heldListWait into the connection: a session that never
// comes back is not waited for), until a read answers (heldCheckTries at most). An expired one is left to heldExpire.
func heldActOn(r *heldRow, head heldHead, listed time.Time, ready bool, now time.Time) heldAction {
	switch {
	case head == heldHeadUnclear:
		if !listed.IsZero() && !listed.Equal(r.checked) {
			return heldDoCheck
		}
	case head != heldHeadSend:
	case r.checkFirst(): // the owner's decision: it does not expire
		if ready {
			return heldDoCheck
		}
	case r.expired(now):
	case ready:
		return heldDoSend
	}
	return heldDoNothing
}

// heldOn is what m's node offers a held message to session now. listed: the start of the connection whose current
// list has the session, whatever its status (the node resumes a stopped one); zero when not connected or not listed.
// ready: a message may go: listed, or heldListWait into the connection without it, and the node's answer decides.
// Right after the handshake an Agent of a runtime is not back yet: sent then, the message would be refused for a
// session that is there a second later.
func (m *Machine) heldOn(session string) (listed time.Time, ready bool) {
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
	if m.sessionsListed {
		for i := range m.sessions { // by index: a range value copies every (large) session it passes
			if m.sessions[i].SessionID == session {
				return m.connectedAt, true
			}
		}
	}
	return time.Time{}, !m.connectedAt.IsZero() && time.Since(m.connectedAt) >= heldListWait
}

func (h *heldBook) rowLocked(machine string, seq int64) *heldRow {
	for _, r := range h.rows[machine] {
		if r.Seq == seq {
			return r
		}
	}
	return nil
}

func (h *heldBook) dropLocked(machine string, seq int64) {
	h.rows[machine] = slices.DeleteFunc(h.rows[machine], func(r *heldRow) bool { return r.Seq == seq })
	if len(h.rows[machine]) == 0 {
		delete(h.rows, machine)
	}
}

// heldSend does what the session's held messages can do, in order, while ctx lasts: sends each next one while the node
// takes them (heldOn), and reads the transcript once per connection for one that may have arrived (heldCheck). An
// expired one is left to heldExpire.
func (d *Devices) heldSend(ctx context.Context, machine, session string) {
	h := &d.held
	st := d.heldStore()
	key := machine + "/" + session
	var checkedUnseen int64
	for {
		m := d.machineByID(machine)
		var listed time.Time
		ready := false
		if m != nil {
			listed, ready = m.heldOn(session)
		}
		now := h.clock()
		h.mu.Lock()
		r, head := h.headLocked(machine, session)
		act := heldDoNothing
		if ctx.Err() == nil && r != nil {
			act = heldActOn(r, head, listed, ready, now)
			if act == heldDoCheck && r.checkFirst() && r.Seq == checkedUnseen {
				act = heldDoSend
			}
		}
		if act == heldDoNothing {
			delete(h.sending, key) // decided with the rows locked: what comes after this goes with the next wake
			h.mu.Unlock()
			if m != nil && head != heldHeadNone && ctx.Err() == nil {
				if l, ok := m.heldOn(session); ok != ready || !l.Equal(listed) {
					d.heldWake(machine) // its node changed meanwhile: its wake found this delivery still running
				}
			}
			return
		}
		seq, id, lang := r.Seq, r.OpID, r.Lang
		if act == heldDoCheck {
			retried := r.checkFirst()
			r.checking = true // a Retry meanwhile answers 409; a Delete may go
			if !retried {
				r.checked = listed // a failed one: read once on this connection, whatever comes of it
			}
			h.mu.Unlock()
			read, resend := d.heldCheck(ctx, m, machine, session, seq, id, retried)
			if !read { // the next wake reads again
				h.mu.Lock()
				delete(h.sending, key)
				h.mu.Unlock()
				return
			}
			if resend {
				checkedUnseen = seq
			}
			continue // look at the current head again, which the owner could have deleted during the check
		}
		checkedUnseen = 0
		r.State = db.HeldSending // from now on Delete answers that it is being sent
		h.mu.Unlock()
		sctx, cancel := heldStoreCtx(ctx)
		claim, claimed, err := st.ClaimHeldOperation(sctx, seq, now)
		cancel()
		if err != nil || !claimed {
			// Not marked as being sent: it must not go. A store that has it otherwise is followed (heldSync): being
			// sent there (another manager, or this one's claim whose answer was lost) it may arrive.
			h.mu.Lock()
			if r := h.rowLocked(machine, seq); r != nil {
				r.State = db.HeldWaiting
				if err != nil {
					r.TriedAt = now // the claim may have been taken: its try began now at the latest
				}
			}
			delete(h.sending, key)
			h.mu.Unlock()
			d.logf("[%s] %s: a held message not sent: the store did not mark it as being sent (%v)", machine, session, err)
			if err == nil {
				d.heldSync(ctx, machine, seq)
			}
			d.heldChanged(machine)
			return
		}
		h.mu.Lock()
		if r := h.rowLocked(machine, seq); r != nil {
			r.TriedAt, r.Attempts = claim.TriedAt, claim.Attempts // the store's: this try's name for its writes
		}
		h.mu.Unlock()
		d.heldChanged(machine)
		checkUnseen := claim.Failure == db.HeldUnclear
		outcome, reason := d.heldDeliver(ctx, claim.Call, lang, checkUnseen)
		if !d.heldSettle(ctx, machine, session, seq, outcome, reason, checkUnseen) {
			h.mu.Lock()
			delete(h.sending, key)
			h.mu.Unlock()
			return
		}
	}
}

// heldCheck drops a message seen by the node. An explicit retry may send again only after a known-unseen answer.
// Unknown leaves an explicit retry unconfirmed. A failed read is retried up to heldCheckTries times.
func (d *Devices) heldCheck(ctx context.Context, m *Machine, machine, session string, seq int64, id string, retried bool) (read, resend bool) {
	h := &d.held
	defer func() {
		h.mu.Lock()
		if r := h.rowLocked(machine, seq); r != nil {
			r.checking = false
		}
		h.mu.Unlock()
		d.heldChanged(machine)
	}()
	cctx, cancel := context.WithTimeout(ctx, heldCheckWait)
	state, err := heldEchoState(cctx, m, session, id)
	cancel()
	if err != nil {
		d.logf("[%s] %s: a held message that may have arrived: its transcript could not be read: %v", machine, session, err)
		h.mu.Lock()
		r := h.rowLocked(machine, seq)
		tries := 0
		if r != nil {
			r.failedReads++
			tries = r.failedReads
		}
		h.mu.Unlock()
		if tries >= heldCheckTries {
			d.heldUnconfirmed(ctx, machine, session, seq, retried)
		}
		return false, false
	}
	h.mu.Lock()
	if r := h.rowLocked(machine, seq); r != nil {
		r.failedReads = 0 // read: only failures in a row leave it to the owner
	}
	h.mu.Unlock()
	if state == proto.EchoKnownUnseen {
		return true, retried
	}
	if state != proto.EchoSeen {
		if retried {
			d.heldUnconfirmed(ctx, machine, session, seq, true)
		}
		return true, false
	}
	sctx, scancel := heldStoreCtx(ctx)
	defer scancel()
	// Failed, or waiting after the owner's Retry: either way it is there, and never sent again.
	ok, err := d.heldStore().DeleteHeldOperation(sctx, seq, db.HeldFailed, db.HeldWaiting)
	if err != nil {
		d.logf("[%s] %s: a held message that arrived stays: %v", machine, session, err)
		return true, false
	}
	if !ok { // the store has it otherwise (gone: deleted meanwhile): followed
		d.heldSync(ctx, machine, seq)
		return true, false
	}
	h.mu.Lock()
	h.dropLocked(machine, seq)
	h.mu.Unlock()
	d.logf("[%s] %s: a held message that may have arrived is in its transcript: taken as sent", machine, session)
	return true, false
}

// heldEchoState falls back to the latest turns on older nodes and when the new check has no evidence. A page can
// only prove arrival through a matching client_msg_id on a user item.
func heldEchoState(ctx context.Context, m *Machine, session, id string) (proto.SessionEchoState, error) {
	if m == nil {
		return proto.EchoUnknown, errOffline
	}
	var status proto.SessionEchoStatus
	err := m.call(ctx, proto.MethodSessionEchoStatus, proto.SessionEchoParams{SessionID: session, ClientMsgID: id}, &status)
	if err != nil && !errors.Is(err, errEchoStatusUnsupported) {
		return proto.EchoUnknown, err
	}
	if status.State == proto.EchoKnownUnseen || status.State == proto.EchoSeen {
		return status.State, nil
	}
	var page struct {
		Items []proto.Item `json:"items"`
	}
	err = m.call(ctx, "session.items", map[string]any{"session_id": session, "turns": 3, "lazy_images": true}, &page)
	if err == nil && slices.ContainsFunc(page.Items, func(it proto.Item) bool { return it.Role == "user" && it.ClientMsgID == id }) {
		return proto.EchoSeen, nil
	}
	return proto.EchoUnknown, err
}

// heldUnconfirmed leaves message seq, which may have arrived and was not found or not read, to the owner: failed,
// unconfirmed (a Retry then sends it as it is). retried: it waits (checkFirst); else it failed as unclear.
func (d *Devices) heldUnconfirmed(ctx context.Context, machine, session string, seq int64, retried bool) {
	from := db.HeldFailed
	if retried {
		from = db.HeldWaiting
	}
	sctx, cancel := heldStoreCtx(ctx)
	ok, err := d.heldStore().SettleHeldOperation(sctx, seq, from, db.HeldFailed, db.HeldUnconfirmed, "")
	cancel()
	switch {
	case err != nil:
		d.logf("[%s] %s: a held message not left to the owner yet: %v", machine, session, err)
	case !ok: // the store has it otherwise (gone: deleted meanwhile)
		d.heldSync(ctx, machine, seq)
	default:
		h := &d.held
		h.mu.Lock()
		if r := h.rowLocked(machine, seq); r != nil {
			r.State, r.Failure, r.Error = db.HeldFailed, db.HeldUnconfirmed, ""
		}
		h.mu.Unlock()
		d.logf("[%s] %s: a held message that may have arrived is left to the owner", machine, session)
	}
}

// heldTry is a held message's delivery, in its context (heldDeliveryKey): callNode notes whether it called the node
// and what that call answered, so the outcome is judged on the node's call alone.
type heldTry struct {
	called      bool
	checkUnseen bool
	err         error
}

// heldDeliveryKey marks a held message's delivery in its context (a *heldTry): its share receipt is settled only by an
// outcome that is final (callNode).
type heldDeliveryKey struct{}

// heldDeliver sends held call raw as the hub sent it (its caller, submission and share authority with it), and says
// what came of it, judged on the node's call alone: stopped before the node was asked (its preparation here failed),
// nothing was written and it is refused, with why; the node took it, it was delivered whatever came after.
func (d *Devices) heldDeliver(ctx context.Context, raw json.RawMessage, lang string, checkUnseen bool) (heldOutcome, string) {
	l := i18n.Lang(lang)
	var p devicewire.NodeCall
	if err := json.Unmarshal(raw, &p); err != nil {
		return heldRefused, heldReason(l, err)
	}
	p.Hold, p.Ordered = nil, false // never held or refused again by its own delivery (rows kept before this said ordered)
	cctx, cancel := context.WithTimeout(ctx, heldSendTimeout)
	defer cancel()
	if p.Caller != nil && p.Caller.CredentialID != "" {
		id := p.Caller.CredentialID
		d.held.mu.Lock()
		revoked := d.held.revoked[id]
		d.held.mu.Unlock()
		if revoked {
			return heldSignedOut, ""
		}
		if d.opts.Hub == nil {
			return heldNotSent, ""
		}
		revoked, err := bus.CredentialRevoked(cctx, d.opts.Hub.Call, id)
		if err != nil {
			d.logf("check held message's device credential: %v", err)
			return heldNotSent, ""
		}
		if revoked {
			d.heldCredentialRevoked(id)
			return heldSignedOut, ""
		}
	}
	try := &heldTry{checkUnseen: checkUnseen}
	cctx = i18n.With(context.WithValue(cctx, heldDeliveryKey{}, try), l)
	_, err := d.callNode(cctx, p)
	switch {
	case !try.called && ctx.Err() != nil:
		return heldNotSent, "" // the manager stopping before the node was asked: it waits again
	case !try.called:
		return heldRefused, heldReason(l, err)
	case try.err == nil:
		return heldDelivered, "" // the node took it (an answer too large to pass on comes after)
	}
	return heldOutcomeOf(try.err)
}

// heldReason is why a held message was refused, in words: the node's (or the manager's coded) message, else that it
// could not be made ready here.
func heldReason(l i18n.Lang, err error) string {
	var re *rpc.Error
	if errors.As(err, &re) && cmp.Or(re.Message, re.Code) != "" {
		return textfmt.Clip(textfmt.OneLine(cmp.Or(re.Message, re.Code)), 300)
	}
	return i18n.T(l, "devices-held.not_prepared")
}

// heldSettles: a call's share receipt is settled now (finishShareCall), which is always but for a held message's
// delivery that wrote nothing or was refused: it stays held and may go again, its receipt waiting for that.
func heldSettles(ctx context.Context, err error) bool {
	if err == nil || ctx.Value(heldDeliveryKey{}) == nil {
		return true
	}
	outcome, _ := heldOutcomeOf(err)
	return outcome == heldUnclear
}

type heldOutcome int

const (
	heldDelivered   heldOutcome = iota
	heldNotSent                 // nothing written: it waits again
	heldRefused                 // the node refused it, nothing typed
	heldUnclear                 // it may have arrived: never sent again by itself
	heldEchoUnknown             // the guarded retry lacks negative evidence: left to the owner
	heldSignedOut               // its sending credential was revoked
)

// heldOutcomeOf is what a node call that ended with err did, and the node's words for a refusal.
func heldOutcomeOf(err error) (heldOutcome, string) {
	if err == nil {
		return heldDelivered, ""
	}
	if heldNotWritten(err) {
		return heldNotSent, ""
	}
	if errors.Is(err, errEchoStatusUnsupported) {
		return heldEchoUnknown, ""
	}
	var re *rpc.Error
	if !errors.As(err, &re) {
		return heldUnclear, "" // the connection dropped with it out, its time ran out, the manager stopped during it
	}
	var data struct {
		Delivered string `json:"delivered"`
	}
	_ = json.Unmarshal(re.Data, &data)
	switch {
	case re.Code == proto.CodeEchoUnconfirmed:
		return heldEchoUnknown, ""
	case data.Delivered == "unknown", re.Code == devicewire.CodeNodeClosed, re.Code == devicewire.CodeDeadline:
		return heldUnclear, ""
	case re.Code == "" && (re.Message == rpc.ErrClosed.Error() || re.Message == context.DeadlineExceeded.Error() ||
		re.Message == context.Canceled.Error()):
		return heldUnclear, "" // a hop further on relayed its own drop or wait
	}
	return heldRefused, textfmt.Clip(textfmt.OneLine(cmp.Or(re.Message, re.Code)), 300)
}

// heldSettle keeps what delivery seq did; false: the session's delivery stops here (nothing written: its computer
// went; or it may have arrived, and the later ones wait behind it).
func (d *Devices) heldSettle(ctx context.Context, machine, session string, seq int64, outcome heldOutcome, reason string, checkUnseen bool) bool {
	h := &d.held
	st := d.heldStore()
	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	defer d.heldChanged(machine)
	h.mu.Lock()
	attempts := 0 // the try this was (its writes touch only that try: never a newer claim)
	if r := h.rowLocked(machine, seq); r != nil {
		attempts = r.Attempts
	}
	h.mu.Unlock()
	if outcome == heldDelivered {
		ok, err := st.DeleteHeldTry(sctx, seq, attempts)
		switch {
		case err != nil: // still being sent in the store: heldRepair deletes it (a restart shows it unclear)
			h.mu.Lock()
			h.dropLocked(machine, seq)
			h.gone[seq] = attempts
			h.mu.Unlock()
			d.logf("[%s] %s: a held message was sent but stays in the store: %v", machine, session, err)
		case !ok: // the store has it otherwise: followed, not dropped here while it keeps a row
			d.logf("[%s] %s: a held message was sent; the store has it otherwise", machine, session)
			d.heldSync(ctx, machine, seq)
		default:
			h.mu.Lock()
			h.dropLocked(machine, seq)
			h.mu.Unlock()
		}
		d.logf("[%s] %s: a held message was sent", machine, session)
		return true
	}
	state, failure := db.HeldFailed, ""
	switch outcome {
	case heldNotSent:
		state, reason = db.HeldWaiting, ""
		if checkUnseen {
			failure = db.HeldUnclear // the next connection must check the original send again
		}
	case heldRefused:
		failure = db.HeldRefused
	case heldEchoUnknown:
		failure, reason = db.HeldUnconfirmed, ""
	case heldSignedOut:
		failure = db.HeldSignedOut
	default:
		failure, reason = db.HeldUnclear, ""
	}
	ok, err := st.SettleHeldTry(sctx, seq, attempts, state, failure, reason)
	h.mu.Lock()
	if r := h.rowLocked(machine, seq); r != nil {
		r.State, r.Failure, r.Error = state, failure, reason
		// Not written: the store still has it as being sent. What was decided stands here, written by heldRepair;
		// it holds the later ones back until then.
		r.stale, r.staleOwn = err != nil, err != nil
	}
	h.mu.Unlock()
	if err != nil {
		d.logf("[%s] %s: a held message's outcome not kept yet: %v", machine, session, err)
		return false
	}
	if !ok { // the store has it otherwise: followed
		d.logf("[%s] %s: a held message's outcome not kept: the store has it otherwise", machine, session)
		d.heldSync(ctx, machine, seq)
		return false
	}
	switch {
	case state == db.HeldWaiting:
		d.logf("[%s] %s: a held message waits again", machine, session)
		return false
	case failure == db.HeldRefused:
		d.logf("[%s] %s: a held message was refused: %s", machine, session, reason)
		return true // nothing typed: the next one goes
	case failure == db.HeldSignedOut:
		return true
	}
	d.logf("[%s] %s: a held message may have arrived: left to the owner", machine, session)
	return false
}

// heldExpire, the one place a message expires, fails each that has waited heldExpiry for its computer. One held
// behind a message that may have arrived waits for the owner, not for its computer: it never expires there, and its
// wait starts again every heldBlockedRenew, so it has nearly a day once the owner decides.
func (d *Devices) heldExpire(ctx context.Context) {
	h := &d.held
	now := h.clock()
	type due struct {
		machine string
		seq     int64
		renew   bool
	}
	var dues []due
	h.mu.Lock()
	for machine, rows := range h.rows {
		behind := map[string]bool{}
		for _, r := range rows {
			switch {
			case r.awaitsOwner(), r.checkFirst():
				// A retried one to be looked for is the owner's decision already: it does not expire (a Delete
				// still works, and the expiry would drop its mark), and the later ones wait behind it.
				behind[r.SessionID] = true
			case r.State != db.HeldWaiting || r.taken() || r.stale:
			case behind[r.SessionID] && now.Sub(r.HeldAt) >= heldBlockedRenew:
				r.busy = true // neither sent nor changed by the owner meanwhile
				dues = append(dues, due{machine, r.Seq, true})
			case !behind[r.SessionID] && r.expired(now):
				r.busy = true
				dues = append(dues, due{machine, r.Seq, false})
			}
		}
	}
	h.mu.Unlock()
	changed, touched := map[string]bool{}, map[string]bool{}
	for _, e := range dues {
		touched[e.machine] = true // its busy mark held the later ones back meanwhile
		sctx, cancel := heldStoreCtx(ctx)
		var ok bool
		var err error
		if e.renew {
			ok, err = d.heldStore().RenewHeldOperation(sctx, e.seq, now)
		} else {
			ok, err = d.heldStore().SettleHeldOperation(sctx, e.seq, db.HeldWaiting, db.HeldFailed, db.HeldExpired, "")
		}
		cancel()
		h.mu.Lock()
		if r := h.rowLocked(e.machine, e.seq); r != nil {
			r.busy = false
			switch {
			case err != nil: // the next look tries again
			case ok && e.renew:
				r.HeldAt = now
			case ok:
				r.State, r.Failure, r.Error = db.HeldFailed, db.HeldExpired, ""
				changed[e.machine] = true
			case !e.renew: // the store has it otherwise: left to the owner
				r.State, r.Failure, r.Error = db.HeldFailed, db.HeldUnclear, ""
				changed[e.machine] = true
			}
		}
		h.mu.Unlock()
		switch {
		case err != nil:
			d.logf("[%s] a held message's wait not kept: %v", e.machine, err)
		case ok && !e.renew:
			d.logf("[%s] a held message expired: its computer was away for %s", e.machine, heldExpiry)
		}
	}
	for machine := range changed {
		d.heldChanged(machine)
	}
	for machine := range touched {
		d.heldWake(machine) // the ones an expired (or busy) message held back go now
	}
}

// ---- the owner's Retry and Delete ----

var (
	// errHeldSendingNow: the message is being sent this moment: Delete could not take it back.
	errHeldSendingNow = i18n.Err("devices-held.sending_now")
	// errHeldStore: the store did not take the change.
	errHeldStore     = i18n.Err("devices-held.store_failed")
	errHeldSignedOut = i18n.Err("undelivered.signed_out")
)

// heldFind finds held message msg of session sid of machine ("": any machine) and marks it busy for the owner's
// change; nil when there is none. err: it is being sent now (or, but for a Delete, its transcript is being read).
func (h *heldBook) heldFind(machine, sid, msg string, deleting bool) (*heldRow, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for m, rows := range h.rows {
		if machine != "" && m != machine {
			continue
		}
		for _, r := range rows {
			if r.SessionID != sid || r.OpID != msg {
				continue
			}
			if r.State == db.HeldSending || r.busy || r.checking && !deleting {
				return nil, errHeldSendingNow
			}
			r.busy = true
			c := *r
			return &c, nil
		}
	}
	return nil, nil
}

// heldRetry is the owner's Retry: a message that failed waits again, a new day from now, and goes at once when its
// computer is there (one that may have arrived too: the owner checked). found false: no such held message.
func (d *Devices) heldRetry(ctx context.Context, machine, sid, msg string) (bool, error) {
	if !d.heldLoad(ctx) {
		return false, nil
	}
	h := &d.held
	r, err := h.heldFind(machine, sid, msg, false)
	if r == nil {
		return err != nil, err
	}
	h.mu.Lock()
	signedOut := r.Failure == db.HeldSignedOut || h.revoked[r.CredentialID]
	if row := h.rowLocked(r.MachineID, r.Seq); signedOut && row != nil {
		row.busy = false
	}
	h.mu.Unlock()
	if signedOut {
		return true, errHeldSignedOut
	}
	now := h.clock()
	held, wrote, check := true, false, false
	if r.State != db.HeldWaiting { // a waiting one is only woken: nothing to write
		held, wrote, check, err = d.heldRetryStore(ctx, r, now)
	}
	h.mu.Lock()
	if row := h.rowLocked(r.MachineID, r.Seq); row != nil {
		row.busy = false
		row.checked, row.failedReads = time.Time{}, 0 // one that may have arrived is looked for again
		if wrote {
			row.State, row.Failure, row.Error, row.HeldAt, row.stale = db.HeldWaiting, "", "", now, false
			if check {
				row.Failure = db.HeldUnclear
			}
		}
	}
	h.mu.Unlock()
	d.heldChanged(r.MachineID)
	d.heldWake(r.MachineID) // its busy mark held the later ones back; retried, it goes now
	switch {
	case err != nil:
		if !errors.Is(err, errHeldSendingNow) {
			d.logf("[%s] %s: a held message's retry not kept: %v", r.MachineID, sid, err)
			err = errHeldStore
		}
		return true, err
	case !held:
		return false, nil
	}
	d.logf("[%s] %s: a held message retried by the owner", r.MachineID, sid)
	return true, nil
}

// heldRetryStore writes the owner's Retry of failed r. held false: the store no longer has it; wrote: it waits again
// there, check: to be looked for in the transcript before it goes (it may have arrived). A store that has it otherwise
// is followed (heldMirror); one it has as being sent by a try that is over gets that try written as unclear first,
// then the Retry, never over a newer try; else errHeldSendingNow.
func (d *Devices) heldRetryStore(ctx context.Context, r *heldRow, now time.Time) (held, wrote, check bool, err error) {
	st := d.heldStore()
	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	ok, check, err := st.RetryHeldOperation(sctx, r.Seq, now)
	if err != nil || ok {
		return true, ok, check, err
	}
	held, stale, attempts, err := d.heldMirror(ctx, r) // only a failed one is retried
	if err != nil || !stale {
		return held, false, false, err
	}
	// That try is over: what it was decided here when it was this manager's own (nothing typed: refused, expired, or
	// waiting), else it may have arrived (unclear: looked for before it goes).
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
	// The store changed meanwhile (a newer try, or another change): answered from what it has now.
	if held, _, _, err = d.heldMirror(ctx, r); err == nil && held {
		err = errHeldSendingNow
	}
	return held, false, false, err
}

// heldDelete is the owner's Delete on a message that waits or failed; errHeldSendingNow while it is being sent.
func (d *Devices) heldDelete(ctx context.Context, machine, sid, msg string) (bool, error) {
	if !d.heldLoad(ctx) {
		return false, nil
	}
	h := &d.held
	r, err := h.heldFind(machine, sid, msg, true)
	if r == nil {
		return err != nil, err
	}
	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	receipt := d.heldReceipt(sctx, r) // read while the row is there: its call is not kept here
	gone, err := d.heldDeleteStore(ctx, sctx, r)
	h.mu.Lock()
	if err == nil && !gone {
		err = errHeldSendingNow
	}
	if err != nil {
		if row := h.rowLocked(r.MachineID, r.Seq); row != nil {
			row.busy = false
		}
		h.mu.Unlock()
		if !errors.Is(err, errHeldSendingNow) {
			d.logf("[%s] %s: a held message's delete not kept: %v", r.MachineID, sid, err)
			err = errHeldStore
		}
		d.heldChanged(r.MachineID) // the store's row may have been taken in meanwhile (heldMirror)
		d.heldWake(r.MachineID)    // its busy mark held the later ones back
		return true, err
	}
	h.dropLocked(r.MachineID, r.Seq)
	h.mu.Unlock()
	d.heldUnshare(sctx, r, receipt)
	d.logf("[%s] %s: a held message deleted by the owner", r.MachineID, sid)
	d.heldChanged(r.MachineID)
	d.heldWake(r.MachineID) // the ones it held back go now
	return true, nil
}

// heldDeleteStore writes the owner's Delete of r: gone, the store no longer has it. A store that has it otherwise is
// followed (heldMirror); one it has as being sent by a try that is over gets that try deleted, never a newer one; a
// delete that did not apply is answered from what the store has then, never counted as done.
func (d *Devices) heldDeleteStore(ctx, sctx context.Context, r *heldRow) (gone bool, err error) {
	st := d.heldStore()
	ok, err := st.DeleteHeldOperation(sctx, r.Seq) // never one being sent
	if err != nil || ok {
		return ok, err
	}
	held, stale, attempts, err := d.heldMirror(ctx, r)
	switch {
	case err != nil:
		return false, err
	case !held:
		return true, nil // gone already
	case !stale:
		return false, errHeldSendingNow // the store has it, and not waiting or failed: being sent
	}
	if ok, err = st.DeleteHeldTry(sctx, r.Seq, attempts); err != nil || ok {
		return ok, err
	}
	// The store changed meanwhile (a newer try): answered from what it has now.
	if held, _, _, err = d.heldMirror(ctx, r); err != nil {
		return false, err
	}
	return !held, nil
}

// heldMirror takes into memory what the store has of r after it did not take the owner's change: gone, it is
// dropped (held false); waiting or failed, so it is here too. Being sent: stale when no delivery of its session runs
// here and the try is over (this manager's own that it could not write, or one begun longer ago than any try takes),
// so the owner's change may be written over it; else errHeldSendingNow (another manager on the same database, in a
// switch-over, may be sending it), left as it is here.
func (d *Devices) heldMirror(ctx context.Context, r *heldRow) (held, stale bool, attempts int, err error) {
	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	op, found, err := d.heldStore().HeldOperationOf(sctx, r.Kind, r.MachineID, r.SessionID, r.OpID)
	if err != nil {
		return true, false, 0, err
	}
	h := &d.held
	h.mu.Lock()
	defer h.mu.Unlock()
	row := h.rowLocked(r.MachineID, r.Seq)
	switch {
	case !found:
		h.dropLocked(r.MachineID, r.Seq)
		return false, false, 0, nil
	case op.State == db.HeldSending:
		own := row != nil && row.stale && row.staleOwn && row.Attempts == op.Attempts
		if !h.sending[r.MachineID+"/"+r.SessionID] && (own || heldTryOver(op.TriedAt, h.clock())) {
			return true, true, op.Attempts, nil
		}
		return true, false, 0, errHeldSendingNow
	}
	if row != nil {
		row.State, row.Failure, row.Error, row.HeldAt, row.stale = op.State, op.Failure, op.Error, op.HeldAt, false
	}
	return true, false, 0, nil
}

// heldSync takes into memory what the store has of message seq of machine after it did not take a delivery's write:
// gone, it is dropped; being sent (another manager, or this one's claim whose answer was lost), it may arrive: unclear
// here, stale until heldRepair writes that once the try is over; else as the store has it.
func (d *Devices) heldSync(ctx context.Context, machine string, seq int64) {
	h := &d.held
	h.mu.Lock()
	r := h.rowLocked(machine, seq)
	var key heldRow
	if r != nil {
		key = *r
	}
	h.mu.Unlock()
	if r == nil {
		return
	}
	sctx, cancel := heldStoreCtx(ctx)
	op, found, err := d.heldStore().HeldOperationOf(sctx, key.Kind, key.MachineID, key.SessionID, key.OpID)
	cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	r = h.rowLocked(machine, seq)
	switch {
	case r == nil:
	case err != nil: // not read: it may have arrived
		r.State, r.Failure, r.Error = db.HeldFailed, db.HeldUnclear, ""
	case !found:
		h.dropLocked(machine, seq)
	case op.State == db.HeldSending:
		// The try it holds is the one judged, by its attempts: a repair never writes over a newer one.
		r.State, r.Failure, r.Error, r.stale, r.staleOwn = db.HeldFailed, db.HeldUnclear, "", true, false
		r.Attempts = op.Attempts
		if !op.TriedAt.IsZero() {
			r.TriedAt = op.TriedAt
		}
	default:
		r.State, r.Failure, r.Error, r.HeldAt, r.stale = op.State, op.Failure, op.Error, op.HeldAt, false
	}
}

// heldRepair writes what was decided of the messages the store still has as being sent (stale), deletes the rows of
// ones sent whose delete failed, and drops holds the store may have kept though the app was told they failed (unsure): once no delivery of the session runs here and the try is over (at once for
// this manager's own). Each write touches only the try it was judged on, never a newer claim; a row the owner is
// changing is left for later, and one being written is busy meanwhile (the owner's change answers 409). It never sends
// anything.
func (d *Devices) heldRepair(ctx context.Context) {
	h := &d.held
	now := h.clock()
	var dues []heldRow
	gone := map[int64]int{}
	h.mu.Lock()
	for machine, rows := range h.rows {
		for _, r := range rows {
			if r.stale && !r.taken() && !h.sending[machine+"/"+r.SessionID] && (r.staleOwn || heldTryOver(r.TriedAt, now)) {
				r.busy = true
				dues = append(dues, *r)
			}
		}
	}
	maps.Copy(gone, h.gone)
	type unsureKey struct {
		key  heldKey
		done chan struct{}
	}
	var unsure []unsureKey
	for k := range h.unsure {
		switch {
		case slices.ContainsFunc(h.rows[k.machine], func(r *heldRow) bool { return r.SessionID == k.session && r.OpID == k.id }):
			delete(h.unsure, k) // held here since
		case h.holding[k] > 0: // a hold of it runs (the app sent it again): looked at next time
		default:
			done := make(chan struct{}) // a hold of it meanwhile waits until this delete is over
			h.repairing[k] = done
			unsure = append(unsure, unsureKey{k, done})
		}
	}
	h.mu.Unlock()
	st := d.heldStore()
	for _, u := range unsure {
		sctx, cancel := heldStoreCtx(ctx)
		ok, err := st.DeleteUnsureHeld(sctx, db.HeldKindMessage, u.key.machine, u.key.session, u.key.id)
		cancel()
		h.mu.Lock()
		delete(h.repairing, u.key)
		close(u.done)
		if err == nil { // dropped, or not there (never kept, or held since and tried)
			delete(h.unsure, u.key)
		}
		h.mu.Unlock()
		if err == nil && ok {
			d.logf("[%s] %s: a message the app was told failed, kept all the same, dropped", u.key.machine, u.key.session)
		}
	}
	for seq, attempts := range gone {
		sctx, cancel := heldStoreCtx(ctx)
		_, err := st.DeleteHeldTry(sctx, seq, attempts)
		cancel()
		if err == nil { // deleted, or that try is not there any more
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
		switch {
		case err != nil: // the next look tries again
			d.logf("[%s] %s: a held message's outcome not kept yet: %v", r.MachineID, r.SessionID, err)
		case ok:
			d.logf("[%s] %s: a held message's outcome kept at last", r.MachineID, r.SessionID)
		default: // the store has it otherwise (a newer try, or another change)
			d.heldSync(ctx, r.MachineID, r.Seq)
		}
	}
	for machine := range touched {
		d.heldChanged(machine)
		d.heldWake(machine) // what it held back, its busy mark too, goes now
	}
}

// heldReceipt is the share receipt r's call names (its submission, the machine it went to), read from the store; nil
// when there is none or no share store.
func (d *Devices) heldReceipt(ctx context.Context, r *heldRow) *devicewire.NodeCall {
	if d.shareDB() == nil {
		return nil
	}
	op, found, err := d.heldStore().HeldOperationOf(ctx, r.Kind, r.MachineID, r.SessionID, r.OpID)
	var p devicewire.NodeCall
	if err != nil || !found || json.Unmarshal(op.Call, &p) != nil || p.Submission == nil || p.Submission.ID == "" {
		return nil
	}
	return &p
}

// heldUnshare settles a deleted message's share receipt, which its hold left open, as never delivered.
func (d *Devices) heldUnshare(ctx context.Context, r *heldRow, receipt *devicewire.NodeCall) {
	store := d.shareDB()
	if store == nil || receipt == nil {
		return
	}
	_ = store.SettleShareSubmission(ctx, receipt.Submission.ID, receipt.MachineID, r.SessionID, "failed")
}

// heldRPCError is a Retry's or Delete's error for the assistant's routes: coded, in ctx's language.
func heldRPCError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	code := "internal"
	if errors.Is(err, errHeldSendingNow) || errors.Is(err, errHeldSignedOut) {
		code = "conflict"
	}
	return &rpc.Error{Code: code, Message: i18n.Localize(err, i18n.From(ctx).Or(i18n.Default))}
}

// HeldRetry is assistant.Devices'.
func (d *Devices) HeldRetry(ctx context.Context, sid, msg string) (bool, error) {
	found, err := d.heldRetry(ctx, "", sid, msg)
	return found, heldRPCError(ctx, err)
}

// HeldDelete is assistant.Devices'.
func (d *Devices) HeldDelete(ctx context.Context, sid, msg string) (bool, error) {
	found, err := d.heldDelete(ctx, "", sid, msg)
	return found, heldRPCError(ctx, err)
}

// postHeldRetry: POST /api/v1/sessions/{mid}/{sid}/held/{msg}/retry → {"ok":true}.
func (d *Devices) postHeldRetry(w http.ResponseWriter, r *http.Request, m *Machine) {
	d.heldRoute(w, r, m, d.heldRetry)
}

// deleteHeld: DELETE /api/v1/sessions/{mid}/{sid}/held/{msg} → {"ok":true}.
func (d *Devices) deleteHeld(w http.ResponseWriter, r *http.Request, m *Machine) {
	d.heldRoute(w, r, m, d.heldDelete)
}

// heldRoute serves the owner's change of a held message from a person's full device: 404 when there is no such message,
// 409 while it is being sent.
func (d *Devices) heldRoute(w http.ResponseWriter, r *http.Request, m *Machine,
	act func(ctx context.Context, machine, sid, msg string) (bool, error)) {
	// A Retry sends it again with its sender's provenance: only a person's own full device may (not the hub's token, not
	// a pairing code), as the hub says.
	if r.Header.Get(assistantwire.RoleHeader) != "full" || r.Header.Get(assistantwire.PersonHeader) != "1" {
		writeErrCode(w, http.StatusForbidden, proto.CodeReadOnly, i18n.Err("devices-held.read_only"))
		return
	}
	found, err := act(r.Context(), m.cfg.ID, r.PathValue("sid"), r.PathValue("msg"))
	switch {
	case errors.Is(err, errHeldSendingNow), errors.Is(err, errHeldSignedOut):
		writeErrCode(w, http.StatusConflict, "conflict", err)
	case err != nil:
		writeErr(w, http.StatusServiceUnavailable, err)
	case !found:
		writeErr(w, http.StatusNotFound, i18n.Err("devices-held.not_found"))
	default:
		writeJSON(w, map[string]any{"ok": true})
	}
}
