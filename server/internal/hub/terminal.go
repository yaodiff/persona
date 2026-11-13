package hub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// terminals relays the terminal tab's live streams (docs/integrations/terminal.md). The first app to subscribe to a session's
// terminal makes its node stream it (terminal.watch); every frame fans out to the subscribers, and the frames since the
// latest whole one stay here, so an app that subscribes — or comes back after a dropped connection — starts from a
// whole frame, or continues after the last frame it has when its epoch is still the stream's. The last one to leave
// stops the stream. Any failure (node offline, stream ended, a node or herdr that cannot stream) drops the relay and
// tells its apps, which subscribe again later or read the screen instead (unsupported).
type terminals struct {
	mu     sync.Mutex
	relays map[[2]string]*termRelay
	// What the node is asked about a session's terminal (watch, unwatch) goes one call at a time: each looks at what
	// is wanted when its turn comes, so the last one leaves the node as the hub wants.
	calls map[[2]string]*sync.Mutex
}

func (t *terminals) callLock(key [2]string) *sync.Mutex {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.calls == nil {
		t.calls = map[[2]string]*sync.Mutex{}
	}
	if t.calls[key] == nil {
		t.calls[key] = &sync.Mutex{}
	}
	return t.calls[key]
}

type termRelay struct {
	// Each subscriber with the machine ids it asked by: the computer's own id, or one it absorbed. Everything an app
	// gets carries the id it asked with, which is what its page matches a push by (web/src/views/session/terminal/feed.ts).
	subs   map[*client]map[string]bool
	epoch  string
	frames []termFrame // from the latest whole frame on

	// The app driving the terminal at its size (nil: the stream is of the pane at its own size) and that size. What
	// the node runs follows these: every change starts a sync (terminalSync).
	controller   *client
	controllerID string // the machine id the controller asked by
	ctl          termCtl
	takeover     bool
	synced       *termCtl // what the node runs: controlled at that size, or (nil) observing if observing
	observing    bool

	// Done when the machine's connection ends with the relay on it (terminalsEnded): a watch still on its way to that
	// connection stops waiting, so it does not hold the session's call lock, and the next subscribe's watch goes to the
	// node's new connection at once instead of after the old call's deadline (#6327). Nil in tests that make a relay
	// by hand.
	life context.Context
	end  context.CancelFunc
}

// termCtl is a controlled terminal's grid.
type termCtl struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

type termFrame struct {
	Seq  int    `json:"seq"`
	Full bool   `json:"full"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
	Data string `json:"data"`
}

// termPush is a terminal frame as the apps get it, typed so a frame costs no map and no key sort. Fields are in sorted
// key order, the bytes the map[string]any it replaced encoded to.
type termPush struct {
	Cols      int    `json:"cols"`
	Data      string `json:"data"`
	Epoch     string `json:"epoch"`
	Full      bool   `json:"full"`
	MachineID string `json:"machine_id"`
	Rows      int    `json:"rows"`
	Seq       int    `json:"seq"`
	SessionID string `json:"session_id"`
	Type      string `json:"type"` // "terminal"
}

func termMessage(machineID, sessionID, epoch string, f termFrame) termPush {
	return termPush{Type: "terminal", MachineID: machineID, SessionID: sessionID, Epoch: epoch,
		Seq: f.Seq, Full: f.Full, Cols: f.Cols, Rows: f.Rows, Data: f.Data}
}

// replay is what a subscriber holding (epoch, after) is missing: the frames after its last when it is on this stream
// and the kept frames still reach back to it, else everything from the whole frame.
func (r *termRelay) replay(epoch string, after int) []termFrame {
	if len(r.frames) == 0 {
		return nil
	}
	if epoch == r.epoch && after >= r.frames[0].Seq {
		for i, f := range r.frames {
			if f.Seq > after {
				return r.frames[i:]
			}
		}
		return nil
	}
	return r.frames
}

// terminalSubscribe adds an app to a session's terminal. ctl: the app wants to drive it at that grid (the phone's
// size); takeover: even if another program holds the terminal's attach slot (after the app asked its user).
func (h *Hub) terminalSubscribe(ctx context.Context, c *client, machineID, sessionID, epoch string, after int, ctl *termCtl, takeover bool) {
	m := h.localMachine(machineID)
	if m == nil {
		c.sendJSON(map[string]any{"type": "terminal", "machine_id": machineID, "session_id": sessionID, "error": "unknown machine"})
		return
	}
	// Kept under the computer's own id, which is what its frames and ends carry (a page may have asked by an absorbed
	// one); what goes back to this app carries the id it asked by.
	asked := machineID
	machineID = m.cfg.ID
	key := [2]string{machineID, sessionID}
	t := &h.terms
	t.mu.Lock()
	if t.relays == nil {
		t.relays = map[[2]string]*termRelay{}
	}
	r := t.relays[key]
	first := r == nil
	if first {
		r = &termRelay{subs: map[*client]map[string]bool{}}
		r.life, r.end = context.WithCancel(context.Background())
		t.relays[key] = r
	}
	if r.subs[c] == nil {
		r.subs[c] = map[string]bool{}
	}
	r.subs[c][asked] = true
	sync := first
	switch {
	case ctl != nil && (r.controller != c || r.controllerID != asked):
		// The newest device drives; the one before watches. So does a page of the same app that drove it by the
		// computer's other id: each page knows only the id it asked by.
		if old := r.controller; old != nil {
			old.sendTerm(termControlMessage(r.controllerID, sessionID, "taken", nil, i18n.T(old.lang(), "hub-terminal.taken_by_device")))
		}
		r.controller, r.controllerID, r.ctl, r.takeover, sync = c, asked, *ctl, takeover, true
	case ctl != nil: // the same app again (it lost frames, or reconnected): continue, at its size now
		if takeover && !r.takeover {
			r.takeover, sync = true, true
		}
		if *ctl != r.ctl {
			r.ctl = *ctl
			if r.synced != nil {
				h.terminalNotify(m, "terminal.resize", map[string]any{"session_id": sessionID, "cols": ctl.Cols, "rows": ctl.Rows})
			}
		}
	case r.controller == c && r.controllerID == asked: // it only watches now
		r.controller, r.controllerID, sync = nil, "", true
	}
	// Under the lock: the replay goes out before any frame that arrives meanwhile. A sync starts the stream over.
	if !sync {
		for _, f := range r.replay(epoch, after) {
			c.sendTerm(termMessage(asked, sessionID, r.epoch, f))
		}
	}
	t.mu.Unlock()
	if sync {
		go h.terminalSync(context.WithoutCancel(ctx), m, key, r)
	}
}

func termControlMessage(machineID, sessionID, state string, ctl *termCtl, why string) map[string]any {
	msg := map[string]any{"type": "terminal_control", "machine_id": machineID, "session_id": sessionID, "state": state}
	if ctl != nil {
		msg["cols"], msg["rows"] = ctl.Cols, ctl.Rows
	}
	if why != "" {
		msg["error"] = why
	}
	return msg
}

// terminalSync makes the node stream the relay as it now wants: driven at the controller's size, or watched at the
// pane's. The controller hears how that went (terminal_control): active, busy (another program holds the terminal:
// the app asks its user before it takes over; meanwhile it watches), unsupported (an old node or herdr: it watches).
func (h *Hub) terminalSync(ctx context.Context, m *Machine, key [2]string, r *termRelay) {
	mu := h.terms.callLock(key)
	mu.Lock()
	defer mu.Unlock()
	for h.terminalSyncOnce(ctx, m, key, r) {
	}
}

// terminalSyncOnce is one watch call; true when what is wanted changed meanwhile (sync again). A relay gone by then
// has nothing to sync.
func (h *Hub) terminalSyncOnce(ctx context.Context, m *Machine, key [2]string, r *termRelay) bool {
	t := &h.terms
	t.mu.Lock()
	if t.relays[key] != r {
		t.mu.Unlock()
		return false
	}
	controller, controllerID, ctl, takeover := r.controller, r.controllerID, r.ctl, r.takeover
	if controller == nil && r.observing ||
		controller != nil && r.synced != nil && *r.synced == ctl && !takeover {
		if controller != nil { // a device that took it over at the size the node drives it at: it drives it now
			controller.sendTerm(termControlMessage(controllerID, key[1], "active", &ctl, ""))
		}
		t.mu.Unlock() // the node already streams it so
		return false
	}
	t.mu.Unlock()

	params := map[string]any{"session_id": key[1]}
	if controller != nil {
		params["control"] = map[string]any{"cols": ctl.Cols, "rows": ctl.Rows, "takeover": takeover}
	}
	var res struct {
		Control *bool `json:"control"`
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	stop := func() bool { return false }
	if r.life != nil { // the machine's connection went meanwhile: this watch went with it
		stop = context.AfterFunc(r.life, cancel)
	}
	err := m.call(cctx, "terminal.watch", params, &res)
	stop()
	cancel()

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.relays[key] != r {
		return false
	}
	var rerr *rpc.Error
	switch {
	case controller != nil && errors.As(err, &rerr) && rerr.Code == "attach_busy":
		if r.controller == controller {
			r.controller, r.takeover = nil, false
		}
		r.synced, r.observing = nil, false // the node stopped watching to try: watch again
		controller.sendTerm(termControlMessage(controllerID, key[1], "busy", nil, i18n.T(controller.lang(), "hub-terminal.busy")))
		return true
	case err != nil:
		go h.terminalEnded(key[0], key[1], err.Error(), r)
		return false
	case controller != nil && (res.Control == nil || !*res.Control):
		if r.controller == controller {
			r.controller, r.takeover = nil, false
		}
		r.synced, r.observing = nil, true // it ignored control and watches
		controller.sendTerm(termControlMessage(controllerID, key[1], "unsupported", nil, i18n.T(controller.lang(), "hub-terminal.unsupported")))
	case controller != nil:
		r.synced, r.observing = &termCtl{ctl.Cols, ctl.Rows}, false
		if r.controller == controller {
			r.takeover = false
		}
		controller.sendTerm(termControlMessage(controllerID, key[1], "active", &ctl, ""))
		if r.controller == controller && r.ctl != ctl { // it resized while the node was starting
			r.synced = &termCtl{r.ctl.Cols, r.ctl.Rows}
			h.terminalNotify(m, "terminal.resize", map[string]any{"session_id": key[1], "cols": r.ctl.Cols, "rows": r.ctl.Rows})
		}
	default:
		r.synced, r.observing = nil, true
	}
	return r.controller != controller || r.controllerID != controllerID || r.controller != nil && r.takeover
}

// terminalNotify sends the node an event about a terminal it streams (keys, sizes, the lease); events keep their order.
func (h *Hub) terminalNotify(m *Machine, event string, params map[string]any) {
	go func() { _ = m.notify(event, params) }()
}

// terminalInput forwards what the driving app typed: bytes for the terminal, as they are; and the wheel (scroll lines,
// > 0 back in history) over a cell of its screen, at ([column, row]; older apps do not say, the node takes the middle).
// From an app that does not drive it, nothing.
func (h *Hub) terminalInput(c *client, machineID, sessionID, text string, ctl *termCtl, scroll int, at []int) {
	m := h.localMachine(machineID)
	if m == nil {
		return
	}
	t := &h.terms
	t.mu.Lock()
	r := t.relays[[2]string{m.cfg.ID, sessionID}]
	if r == nil || r.controller != c || r.controllerID != machineID { // only the page that drives it
		t.mu.Unlock()
		return
	}
	resize := false
	if ctl != nil && *ctl != r.ctl && ctl.Cols > 0 && ctl.Rows > 0 {
		r.ctl = *ctl
		if r.synced != nil {
			r.synced = &termCtl{ctl.Cols, ctl.Rows}
			resize = true
		}
	}
	live := r.synced != nil
	t.mu.Unlock()
	// Written here, not from a goroutine: the app's messages come in order and so must the keys.
	if resize {
		_ = m.notify("terminal.resize", map[string]any{"session_id": sessionID, "cols": ctl.Cols, "rows": ctl.Rows})
	}
	if text != "" && live {
		_ = m.notify("terminal.input", map[string]any{"session_id": sessionID, "text": text})
	}
	if scroll != 0 && live {
		lines := scroll
		if lines < 0 {
			lines = -lines
		}
		params := map[string]any{"session_id": sessionID, "lines": lines, "up": scroll > 0}
		if len(at) == 2 && at[0] >= 0 && at[1] >= 0 {
			params["column"], params["row"] = at[0], at[1]
		}
		_ = m.notify("terminal.scroll", params)
	}
}

// terminalClick is a tap on a terminal the app drives, to its node in order with the keys (the driver's only).
func (h *Hub) terminalClick(c *client, machineID, sessionID string, column, row int) {
	m := h.localMachine(machineID)
	if m == nil || column < 0 || row < 0 {
		return
	}
	t := &h.terms
	t.mu.Lock()
	r := t.relays[[2]string{m.cfg.ID, sessionID}]
	live := r != nil && r.controller == c && r.controllerID == machineID && r.synced != nil
	t.mu.Unlock()
	if live {
		_ = m.notify("terminal.click", map[string]any{"session_id": sessionID, "column": column, "row": row})
	}
}

// termMouse is terminal_input.mouse: one event of the pointer on a terminal the app drives. Action is down | up | drag
// (moving with the button held) | move (no button; herdr hands it to a program that asked for any-event tracking only),
// Button left | middle | right, At the cell of its screen ([column, row], 0-based) and Mods the modifier bits
// (shift 1, ctrl 2, alt 4).
type termMouse struct {
	Action string `json:"action"`
	Button string `json:"button"`
	At     []int  `json:"at"`
	Mods   int    `json:"mods"`
}

// termMouseMax bounds a cell: a screen is never this big, and a number past it is not a cell.
const termMouseMax = 4096

// valid is whether this is an event herdr takes; anything else is dropped here rather than relayed.
func (e termMouse) valid() bool {
	switch e.Action {
	case "down", "up", "drag", "move":
	default:
		return false
	}
	switch e.Button {
	case "left", "middle", "right":
	default:
		return false
	}
	return len(e.At) == 2 && e.At[0] >= 0 && e.At[1] >= 0 && e.At[0] < termMouseMax && e.At[1] < termMouseMax
}

// terminalMouse relays a mouse event to the node in order with the keys (the driver's only: from any other connection it
// is dropped, as are keys). The hub does not know what the program wants of the mouse; herdr decides.
func (h *Hub) terminalMouse(c *client, machineID, sessionID string, e termMouse) {
	m := h.localMachine(machineID)
	if m == nil || !e.valid() {
		return
	}
	t := &h.terms
	t.mu.Lock()
	r := t.relays[[2]string{m.cfg.ID, sessionID}]
	live := r != nil && r.controller == c && r.controllerID == machineID && r.synced != nil
	t.mu.Unlock()
	if live {
		_ = m.notify("terminal.mouse", map[string]any{"session_id": sessionID, "action": e.Action, "button": e.Button,
			"column": e.At[0], "row": e.At[1], "modifiers": e.Mods & 7})
	}
}

// terminalKeepalive renews the node's lease on every terminal an app drives; a node that stops hearing it lets the
// terminal go (docs/integrations/terminal.md).
func (h *Hub) terminalKeepalive(ctx context.Context) {
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		byMachine := map[string][]string{}
		t := &h.terms
		t.mu.Lock()
		for k, r := range t.relays {
			if r.controller != nil && r.synced != nil {
				byMachine[k[0]] = append(byMachine[k[0]], k[1])
			}
		}
		t.mu.Unlock()
		for mid, ids := range byMachine {
			if m := h.localMachine(mid); m != nil {
				_ = m.notify("terminal.keepalive", map[string]any{"session_ids": ids})
			}
		}
	}
}

// terminalUnsubscribe takes the id the app asked by off its subscription; the app leaves the relay with its last one.
func (h *Hub) terminalUnsubscribe(c *client, machineID, sessionID string) {
	asked := machineID
	m := h.localMachine(machineID)
	if m != nil { // the relay is under the computer's own id (terminalSubscribe)
		machineID = m.cfg.ID
	}
	h.terminalLeave(c, m, [2]string{machineID, sessionID}, asked)
}

// terminalLeave takes asked off the app's subscription to the relay under key (the computer's own id); m is that
// computer (nil: it is gone, the relay only goes).
func (h *Hub) terminalLeave(c *client, m *Machine, key [2]string, asked string) {
	t := &h.terms
	t.mu.Lock()
	r := t.relays[key]
	if r == nil || !r.subs[c][asked] {
		t.mu.Unlock()
		return
	}
	delete(r.subs[c], asked)
	if len(r.subs[c]) == 0 {
		delete(r.subs, c)
	}
	last := len(r.subs) == 0
	if last {
		delete(t.relays, key)
	}
	// The page that drove it is the one that asked by this id; one of the same app under the other id only watched.
	drove := r.controller == c && (r.controllerID == asked || r.subs[c] == nil)
	if drove {
		r.controller, r.controllerID = nil, "" // the pane goes back to its size; whoever still watches sees it there
	}
	t.mu.Unlock()
	if m == nil {
		return
	}
	if last {
		go h.terminalUnwatch(m, key)
	} else if drove {
		go h.terminalSync(context.Background(), m, key, r)
	}
}

// terminalUnwatch stops the node's stream of a relay that went, after any watch on its way; one subscribed again
// meanwhile keeps it (its own watch replaces the stream).
func (h *Hub) terminalUnwatch(m *Machine, key [2]string) {
	t := &h.terms
	mu := t.callLock(key)
	mu.Lock()
	defer mu.Unlock()
	t.mu.Lock()
	again := t.relays[key] != nil
	t.mu.Unlock()
	if again {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = m.call(ctx, "terminal.unwatch", map[string]any{"session_id": key[1]}, nil)
}

// terminalDrop unsubscribes a client that went away from every terminal it watched.
func (h *Hub) terminalDrop(c *client) {
	t := &h.terms
	t.mu.Lock()
	type sub struct {
		key   [2]string
		asked string
	}
	var subs []sub
	for k, r := range t.relays {
		for asked := range r.subs[c] {
			subs = append(subs, sub{k, asked})
		}
	}
	t.mu.Unlock()
	// By the relay's own key: an id the app asked by may no longer reach the computer.
	for _, s := range subs {
		h.terminalLeave(c, h.localMachine(s.key[0]), s.key, s.asked)
	}
}

// onTerminalFrame keeps a node's frame and sends it to the session's subscribers. A new epoch starts with a whole
// frame; a diff with nothing to apply to (the whole frame was never seen) is dropped.
func (h *Hub) onTerminalFrame(machineID string, params json.RawMessage) {
	var p struct {
		SessionID string `json:"session_id"`
		Epoch     string `json:"epoch"`
		termFrame
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	t := &h.terms
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.relays[[2]string{machineID, p.SessionID}]
	if r == nil {
		return
	}
	if p.Epoch != r.epoch {
		if !p.Full {
			return
		}
		r.epoch = p.Epoch
	}
	if p.Full {
		r.frames = []termFrame{p.termFrame}
	} else if len(r.frames) > 0 {
		r.frames = append(r.frames, p.termFrame)
	} else {
		return
	}
	// Encoded once for every watcher: a frame's data can be a large diff, and it arrives many times a second.
	b, err := json.Marshal(termMessage(machineID, p.SessionID, r.epoch, p.termFrame))
	if err != nil {
		return
	}
	var aliased map[string][]byte // the frame under an absorbed id a page asked by, encoded once each
	for c, ids := range r.subs {
		for id := range ids {
			if id == machineID {
				c.queue(b) // the same non-blocking queue sendTerm uses
				continue
			}
			ab, ok := aliased[id]
			if !ok {
				if ab, err = json.Marshal(termMessage(id, p.SessionID, r.epoch, p.termFrame)); err != nil {
					continue
				}
				if aliased == nil {
					aliased = map[string][]byte{}
				}
				aliased[id] = ab
			}
			c.queue(ab)
		}
	}
}

// terminalControlEnded: the node stopped driving a terminal on its own — someone took the attach slot over (taken),
// or its lease ran out. The app that drove it hears why and watches it from now on.
func (h *Hub) terminalControlEnded(machineID, sessionID, why string, taken bool) {
	key := [2]string{machineID, sessionID}
	t := &h.terms
	t.mu.Lock()
	r := t.relays[key]
	if r == nil || r.synced == nil {
		t.mu.Unlock()
		return
	}
	state, text := "lost", "hub-terminal.lost"
	if taken {
		state, text = "taken", "hub-terminal.taken_by_program"
	}
	if c := r.controller; c != nil {
		c.sendTerm(termControlMessage(r.controllerID, sessionID, state, nil, i18n.T(c.lang(), text)))
	}
	r.controller, r.controllerID, r.synced, r.observing = nil, "", nil, false
	t.mu.Unlock()
	h.logf("[%s] terminal %s: control ended: %s", machineID, sessionID, why)
	if m := h.localMachine(machineID); m != nil {
		go h.terminalSync(context.Background(), m, key, r)
	}
}

// terminalEnded tells a relay's apps its stream stopped, and drops it. only: the relay the failure is about (nil:
// whichever is there), so a late failure of an old stream leaves a new one alone.
func (h *Hub) terminalEnded(machineID, sessionID, why string, only *termRelay) {
	key := [2]string{machineID, sessionID}
	t := &h.terms
	t.mu.Lock()
	r := t.relays[key]
	if r == nil || (only != nil && r != only) {
		t.mu.Unlock()
		return
	}
	delete(t.relays, key)
	t.mu.Unlock()
	if m := h.localMachine(machineID); m != nil && r.synced != nil {
		go h.terminalUnwatch(m, key) // the node may still drive it (a watch that timed out here but started there)
	}
	// an old node ("unknown method") or herdr: read the screen instead of trying again
	unsupported := strings.Contains(why, "unknown method") || strings.HasPrefix(why, "unsupported")
	for c, ids := range r.subs {
		for id := range ids {
			message := map[string]any{"type": "terminal", "machine_id": id, "session_id": sessionID, "error": apierr.RPCText(c.lang(), why), "unsupported": unsupported}
			if message["error"] != why {
				message["detail"] = why
			}
			c.sendTerm(message)
		}
	}
}

// sendTerm queues a terminal message without ever blocking (it runs under the relays' lock): a client whose queue is
// full is dropped — a missing diff would leave its screen wrong — and continues from its last frame when it reconnects.
func (c *client) sendTerm(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.queue(b)
}

// terminalsEnded ends every terminal stream of the machine, saying why: its connection is gone (connectionEnded). A
// watch still on its way went with that connection; it stops waiting for it, so the next subscribe's watch goes to the
// node's new connection now, not after the old call's deadline (#6327).
func (h *Hub) terminalsEnded(machineID, why string) {
	t := &h.terms
	t.mu.Lock()
	var ids []string
	var relays []*termRelay
	for k, r := range t.relays {
		if k[0] == machineID {
			ids = append(ids, k[1])
			relays = append(relays, r)
		}
	}
	t.mu.Unlock()
	for i, id := range ids {
		h.terminalEnded(machineID, id, why, relays[i]) // one subscribed since, on the new connection, stays
		if relays[i].end != nil {
			relays[i].end()
		}
	}
}
