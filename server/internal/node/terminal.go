package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The terminal tab's live stream (docs/integrations/terminal.md): while the hub watches a session's terminal, one
// `herdr terminal session observe` runs on its pane at the pane's own size and every frame goes to the hub as a
// terminal.frame event. The stream starts over — a new epoch, beginning with a whole frame — when the pane changes
// size, when herdr ends it (the pane moved, a live handoff) and when the diffs after the last whole frame pile up
// (the hub keeps them to replay to apps that reconnect).
//
// A phone that drives the terminal takes it instead (`herdr terminal session control` at the phone's grid): the
// pane's programs see the phone's size and lay themselves out for it, the frames are the same kind, and the phone's
// keys go straight in (terminal.input). Letting go — the hub says so, the lease runs out — first resizes the pane back
// to the size it had when the phone took it: herdr lays a released pane out at its client's size only while a herdr
// client is attached, and with none would leave it at the phone's (#5339). A control handed straight to the next one
// keeps that first size and skips the resize in between.

const (
	// termReplayLimit: base64 bytes of diffs after a whole frame before a fresh observe (a new whole frame).
	termReplayLimit = 256 << 10
	// termSizeEvery: how often the pane's size is checked while streaming.
	termSizeEvery = 2 * time.Second
	// termGiveUp: failed starts in a row before the stream ends (the app falls back to reading the screen).
	termGiveUp = 4
)

// termMinGap: frames closer together than this go out as one. herdr emits a frame per change up to every ~16 ms
// (60 a second: a build log, vim scrolling); the phone takes ~12 ms a frame, so it gets at most ~33. Claude Code
// redraws about every 110 ms while it streams, which passes untouched; the first frame after a quiet spell goes out
// at once (typing echoes are not held back).
var termMinGap = 30 * time.Millisecond

// frameGate merges frames that come within termMinGap of the last one sent, or while one is still being written:
// the diffs' bytes run on (they are VT output, applied in order), a whole frame drops the diffs before it. What is
// held goes out when the gap is up and the link has taken the frame before it.
//
// Sending runs off the caller (the host's reader): a slow link (a computer the hub reaches through a proxy, ~160 ms
// and a few hundred KB/s, 2026-11-01) used to block the reader on every write, so frames queued behind each other and
// the phone kept drawing the past while the user scrolled. Now they merge while the link is busy, and when what waits
// outgrows a whole screen, refresh (control streams of a host that can redraw) asks for a whole frame to replace it:
// a slow link carries the latest screen, not every step on the way there.
type frameGate struct {
	mu       sync.Mutex
	idle     *sync.Cond
	send     func(host.Frame)
	refresh  func() // optional: ask the host for a whole frame of the screen as it is now
	minGap   time.Duration
	last     time.Time
	busy     bool // a send is being written
	closed   bool // stream ended: reject further puts, arm no timers
	pending  *host.Frame
	timer    *time.Timer
	timerGen uint64
	whole    int  // bytes of the last whole frame: what a screen costs on the wire
	asked    bool // refresh asked, no whole frame since
}

func (g *frameGate) gap() time.Duration {
	if g.minGap > 0 {
		return g.minGap
	}
	return termMinGap
}

// gateBudget: waiting diffs past this (or past a whole screen, when that is larger) ask for a whole frame instead.
const gateBudget = 16 << 10

func (g *frameGate) put(f host.Frame) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	if f.Full {
		g.whole, g.asked = len(f.Data), false
	}
	if g.pending == nil {
		if g.busy {
			g.pending = &f
			return
		}
		if wait := g.gap() - time.Since(g.last); wait > 0 {
			g.pending = &f
			g.armLocked(wait)
			return
		}
		g.start(f)
		return
	}
	if f.Full {
		*g.pending = f
		return
	}
	g.pending.Data = joinFrameData(g.pending.Data, f.Data)
	g.pending.Width, g.pending.Height = f.Width, f.Height
	if g.busy && g.refresh != nil && !g.asked && !g.pending.Full && len(g.pending.Data) > max(g.whole, gateBudget) {
		g.asked = true
		go g.refresh()
	}
}

// armLocked schedules one rate-limit timer if none is armed and the gate is still open. Called with g.mu held.
func (g *frameGate) armLocked(wait time.Duration) {
	if g.timer != nil || g.closed {
		return
	}
	g.timerGen++
	gen := g.timerGen
	g.timer = time.AfterFunc(wait, func() { g.onTimer(gen) })
}

// stopTimerLocked cancels any armed timer and invalidates callbacks from earlier generations. Called with g.mu held.
func (g *frameGate) stopTimerLocked() {
	g.timerGen++
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
}

func (g *frameGate) onTimer(gen uint64) {
	g.mu.Lock()
	if g.closed || g.timerGen != gen {
		g.mu.Unlock()
		return
	}
	g.timer = nil
	if g.busy || g.pending == nil {
		g.mu.Unlock()
		return
	}
	f := *g.pending
	g.pending, g.busy, g.last = nil, true, time.Now()
	g.mu.Unlock()
	g.deliver(f, false)
}

// start sends f on a goroutine of its own and keeps sending what waited meanwhile. Called with g.mu held.
func (g *frameGate) start(f host.Frame) {
	g.busy, g.last = true, time.Now()
	go g.deliver(f, false)
}

// deliver sends f and drains queued frames. When drain is true (or the gate is closed), it sends all buffered
// frames immediately without arming a rate-limit timer.
func (g *frameGate) deliver(f host.Frame, drain bool) {
	for {
		g.send(f)
		g.mu.Lock()
		if g.pending == nil {
			g.done()
			return
		}
		if !drain && !g.closed {
			if wait := g.gap() - time.Since(g.last); wait > 0 {
				g.armLocked(wait)
				g.done()
				return
			}
		}
		f, g.pending, g.last = *g.pending, nil, time.Now()
		g.mu.Unlock()
	}
}

// done ends a run of sends. Called with g.mu held; releases it.
func (g *frameGate) done() {
	g.busy = false
	if g.idle != nil {
		g.idle.Broadcast()
	}
	g.mu.Unlock()
}

// flush sends what is held now, waiting for any send under way without leaving a timer armed.
func (g *frameGate) flush() {
	g.drain(false, true)
}

// close ends the gate: further puts are ignored, any timer is stopped, any send under way completes, and
// already-buffered frames are sent before close returns.
func (g *frameGate) close() {
	g.drain(true, true)
}

// discard ends the gate without sending buffered frames, waiting for any send under way to finish.
func (g *frameGate) discard() {
	g.drain(true, false)
}

func (g *frameGate) drain(closeGate, sendPending bool) {
	g.mu.Lock()
	if closeGate {
		g.closed = true
		if !sendPending {
			g.pending = nil
		}
	}
	g.stopTimerLocked()
	if g.idle == nil {
		g.idle = sync.NewCond(&g.mu)
	}
	for g.busy {
		g.idle.Wait()
	}
	g.stopTimerLocked()
	if !sendPending || g.pending == nil {
		g.mu.Unlock()
		return
	}
	f := *g.pending
	g.pending, g.busy, g.last = nil, true, time.Now()
	g.mu.Unlock()
	g.deliver(f, true)
}

func joinFrameData(a, b string) string {
	x, err1 := base64.StdEncoding.DecodeString(a)
	y, err2 := base64.StdEncoding.DecodeString(b)
	if err1 != nil || err2 != nil {
		return a + b // not ours to fix; keep both
	}
	return base64.StdEncoding.EncodeToString(append(x, y...))
}

type termStream struct {
	cancel context.CancelFunc

	// The phone driving the terminal at its own size (nil: observing). Anything the hub sends about it renews the
	// lease; one that runs out lets the terminal go, in case the hub lost track of it.
	mu    sync.Mutex
	ctl   host.Control
	lease time.Time
	// leaseFor is termLease when terminalControl made the stream (0 for an observing one, which has no lease):
	// controlLoop outlives the call that started it, so neither it nor leaseOver reads the package variable again.
	leaseFor time.Duration
	// home is the size the pane's programs saw before the first phone took it: what letGo gives back.
	home termSize
	// handover: the next control takes the pane straight over, so letting go gives no size back.
	handover bool
	// leaving: letting go has begun; frames after it (the resize back) are not the phone's to see.
	leaving bool
}

type termSize struct{ cols, rows int }

// letGo ends the control: at home (host.Releaser) unless it is handed over or herdr already ended it.
func (s *termStream) letGo(c host.Control) {
	s.mu.Lock()
	s.leaving = true
	home, handover := s.home, s.handover
	s.mu.Unlock()
	select {
	case <-c.Done(): // taken over, the pane gone: whoever took it owns its size
		c.Close()
		return
	default:
	}
	if r, ok := c.(host.Releaser); ok && !handover {
		r.CloseAt(home.cols, home.rows)
		return
	}
	c.Close()
}

func (s *termStream) isLeaving() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaving
}

// termLease: a controlled terminal goes back to its own size when the hub said nothing about it for this long (it
// sends terminal.keepalive every 20 s while the phone holds it).
var termLease = 75 * time.Second

func (s *termStream) control() host.Control {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lease = time.Now()
	return s.ctl
}

// driver is the control without renewing the lease: the frame path's, which is the pane's output, not the hub
// (output that kept coming used to hold a control the hub had forgotten for good, #5450).
func (s *termStream) driver() host.Control {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctl
}

func (s *termStream) leaseOver() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lease) > s.leaseFor
}

// canObserve: whether any host can stream a terminal (herdr: one with `terminal session observe`).
func (n *Node) canObserve() bool {
	for _, h := range n.hosts.All() {
		if st, ok := h.(host.Streamer); ok && st.CanObserve() {
			return true
		}
	}
	return false
}

// errNoTermStream: the hub falls back to the screen reads (old herdr, Windows).
var errNoTermStream = errors.New("unsupported: this herdr cannot stream a terminal (needs herdr terminal session observe)")

// termControl is what the phone asks for to drive the terminal: its grid, and whether to take the attach slot from
// whoever holds it.
type termControl struct {
	Cols     int  `json:"cols"`
	Rows     int  `json:"rows"`
	Takeover bool `json:"takeover"`
}

// terminalWatch starts streaming a session's terminal, or starts it over when it already streams (the hub asks again
// after it lost what it kept, and needs a whole frame). With ctl the phone takes the terminal at its size
// (terminal session control) instead of watching it at the pane's; an attach slot held by another client is
// attach_busy, unless ctl.Takeover. Returns the size streamed and whether it is controlled (an old node leaves that
// out: the hub then knows the phone only watches).
func (n *Node) terminalWatch(ctx context.Context, id string, ctl *termControl) (map[string]any, error) {
	if n.isAssistantSession(id) {
		return nil, fmt.Errorf("session %s has no terminal", id)
	}
	if !n.canObserve() {
		return nil, errNoTermStream
	}
	pane, err := n.terminalPane(ctx, id)
	if err != nil {
		return nil, err
	}
	st, ref, err := paneCap[host.Streamer](n, pane)
	if err != nil || !st.CanObserve() {
		return nil, errNoTermStream
	}
	if ctl != nil && ctl.Cols > 0 && ctl.Rows > 0 {
		if c, cref, err := paneCap[host.Controller](n, pane); err == nil && c.CanControl() {
			return n.terminalControl(ctx, id, c, cref, *ctl)
		}
	}
	cols, rows, err := st.PaneSize(ctx, ref)
	if err != nil {
		return nil, err
	}
	// The stream outlives this request but not the connection: a resident node makes a new Node per hub
	// connection, and one left running on the old one would observe (or hold the attach slot) for good (#5331).
	sctx, cancel := context.WithCancel(rpc.ConnContext(ctx))
	s := &termStream{cancel: cancel}
	n.setTermStream(id, s)
	go n.terminalLoop(sctx, id, s)
	return map[string]any{"cols": cols, "rows": rows, "control": false}, nil
}

// setTermStream makes s the session's stream, stopping the one before it.
func (n *Node) setTermStream(id string, s *termStream) {
	n.mu.Lock()
	if n.terms == nil {
		n.terms = map[string]*termStream{}
	}
	old := n.terms[id]
	if s == nil {
		delete(n.terms, id)
	} else {
		n.terms[id] = s
	}
	n.mu.Unlock()
	if old != nil && old != s {
		old.cancel()
	}
}

func (n *Node) terminalUnwatch(id string) { n.setTermStream(id, nil) }

func (n *Node) termStreamOf(id string) *termStream {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.terms[id]
}

// terminalControl takes the session's terminal at the phone's size. The stream before it (an observe) stops first,
// so its frames are not mixed into the new epoch; when the slot is busy the hub starts an observe again.
func (n *Node) terminalControl(ctx context.Context, id string, ctlr host.Controller, pane host.PaneRef, p termControl) (map[string]any, error) {
	var home termSize
	handedOver := false
	if old := n.termStreamOf(id); old != nil {
		old.mu.Lock()
		prev := old.ctl
		if prev != nil { // the pane is at the last phone's size: keep the size from before, give nothing back in between
			old.handover, home, handedOver = true, old.home, true
		}
		old.mu.Unlock()
		n.setTermStream(id, nil)
		if prev != nil { // ours holds the slot until it is gone: taking it again too soon finds it busy
			select {
			case <-prev.Done():
			case <-time.After(3 * time.Second):
			}
		}
	}
	if home.cols <= 0 {
		home = paneHome(ctx, ctlr, pane)
	}
	sctx, cancel := context.WithCancel(rpc.ConnContext(ctx)) // ends with the connection, as terminalWatch's (#5331)
	s := &termStream{cancel: cancel, lease: time.Now(), leaseFor: termLease, home: home}
	epoch := strconv.FormatInt(time.Now().UnixNano(), 36)
	frames, replay := 0, 0
	gate := &frameGate{minGap: termMinGap, send: func(f host.Frame) {
		if s.isLeaving() {
			return
		}
		frames++
		if f.Full {
			replay = 0
		} else {
			replay += len(f.Data)
		}
		if n.peer != nil {
			_ = n.peer.Notify("terminal.frame", map[string]any{"session_id": id, "epoch": epoch, "seq": frames,
				"full": f.Full, "cols": f.Width, "rows": f.Height, "data": n.scrubB64(f.Data)}) // base64: hidden here (secretenv.go)
		}
		if replay > termReplayLimit {
			// A resize to the same size: herdr sends a whole frame, the programs see no change (no SIGWINCH).
			replay = 0
			if c := s.driver(); c != nil {
				go func() { _ = c.Resize(f.Width, f.Height) }()
			}
		}
	}, refresh: func() {
		if r, ok := s.driver().(host.Redrawer); ok {
			_ = r.Redraw()
		}
	}}
	// Not sctx: a control ends with its context, which would beat controlLoop's letGo to it and leave the pane at the
	// phone's size. controlLoop ends it whenever sctx ends (the connection included, #5331); starting is bounded by
	// the host (herdr: controlWait).
	c, err := ctlr.Control(context.WithoutCancel(sctx), pane, p.Cols, p.Rows, p.Takeover, gate.put)
	if err != nil {
		gate.discard()
		cancel()
		if handedOver { // the control before let go at the phone's size for this one, which did not come
			go giveBack(ctlr, pane, home)
		}
		if errors.Is(err, host.ErrAttachBusy) {
			return nil, &rpc.Error{Code: "attach_busy", Message: err.Error()}
		}
		return nil, err
	}
	s.mu.Lock()
	s.ctl = c
	s.mu.Unlock()
	n.setTermStream(id, s)
	go n.controlLoop(sctx, id, s, gate)
	return map[string]any{"cols": p.Cols, "rows": p.Rows, "control": true}, nil
}

// paneHome is the size the pane's programs see before a phone takes it (host.Streamer's PaneSize: herdr's is the
// pane's terminal's, not its layout's). Zero when it does not say; letting go is then a plain release.
func paneHome(ctx context.Context, h host.Controller, pane host.PaneRef) termSize {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if st, ok := h.(host.Streamer); ok {
		if c, r, err := st.PaneSize(ctx, pane); err == nil && c > 0 && r > 0 {
			return termSize{c, r}
		}
	}
	return termSize{}
}

// giveBack puts a pane released at a phone's size back to home: a control at home, let go at once (with no herdr
// client attached herdr keeps a released pane's last size; with one it lays the pane out at that client's). A slot
// someone else holds now is theirs, size and all: nothing is taken over.
func giveBack(ctlr host.Controller, pane host.PaneRef, home termSize) {
	if home.cols <= 0 || home.rows <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := ctlr.Control(ctx, pane, home.cols, home.rows, false, func(host.Frame) {})
	if err != nil {
		return
	}
	c.Close()
	select {
	case <-c.Done():
	case <-ctx.Done():
	}
}

// controlLoop holds the terminal until the hub lets it go (unwatch, or a watch that replaces it), the lease runs out
// or herdr ends it (someone took the slot over, the pane went away): then terminal.end tells the hub, which goes
// back to watching.
func (n *Node) controlLoop(ctx context.Context, id string, s *termStream, gate *frameGate) {
	c := s.control()
	t := time.NewTicker(s.leaseFor / 5)
	defer t.Stop()
	var why error
	for why == nil {
		select {
		case <-ctx.Done():
			gate.close()
			s.letGo(c)
			return
		case <-c.Done():
			why = c.Err()
		case <-t.C:
			if s.leaseOver() {
				why = errors.New("the hub stopped renewing the terminal control")
			}
		}
	}
	gate.close()
	s.letGo(c)
	n.mu.Lock()
	mine := n.terms[id] == s
	if mine {
		delete(n.terms, id)
	}
	n.mu.Unlock()
	s.cancel()
	if mine && n.peer != nil {
		_ = n.peer.Notify("terminal.end", map[string]any{"session_id": id, "error": why.Error(), "control": true,
			"taken": errors.Is(why, host.ErrTakenOver)})
	}
}

// onTerminalEvent handles what the hub forwards from the phone driving a terminal; events come in order, so keys do.
func (n *Node) onTerminalEvent(event string, params json.RawMessage) {
	var p struct {
		SessionID  string   `json:"session_id"`
		SessionIDs []string `json:"session_ids"`
		Text       string   `json:"text"`
		Cols       int      `json:"cols"`
		Rows       int      `json:"rows"`
		Lines      int      `json:"lines"`
		Up         bool     `json:"up"`
		Column     *int     `json:"column"`
		Row        *int     `json:"row"`
		// terminal.mouse (docs/integrations/terminal.md "Mouse")
		Action    string `json:"action"`
		Button    string `json:"button"`
		Modifiers int    `json:"modifiers"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	switch event {
	case "terminal.input":
		if s := n.termStreamOf(p.SessionID); s != nil {
			if c := s.control(); c != nil && p.Text != "" {
				_ = c.Input(p.Text)
			}
		}
	case "terminal.resize":
		if s := n.termStreamOf(p.SessionID); s != nil {
			if c := s.control(); c != nil && p.Cols > 0 && p.Rows > 0 {
				_ = c.Resize(p.Cols, p.Rows)
			}
		}
	case "terminal.scroll":
		if s := n.termStreamOf(p.SessionID); s != nil {
			if c := s.control(); c != nil && p.Lines > 0 {
				column, row := -1, -1 // an older hub: the middle of the screen
				if p.Column != nil && p.Row != nil {
					column, row = *p.Column, *p.Row
				}
				_ = c.Scroll(p.Lines, p.Up, column, row)
			}
		}
	case "terminal.click":
		if s := n.termStreamOf(p.SessionID); s != nil {
			if c := s.control(); c != nil && p.Column != nil && p.Row != nil && *p.Column >= 0 && *p.Row >= 0 {
				_ = c.Click(*p.Column, *p.Row)
			}
		}
	case "terminal.mouse":
		if s := n.termStreamOf(p.SessionID); s != nil && p.Column != nil && p.Row != nil {
			if m, ok := s.control().(host.Mouser); ok { // control() is nil with no terminal driven: the assertion fails
				_ = m.Mouse(host.MouseEvent{Action: p.Action, Button: p.Button, Column: *p.Column, Row: *p.Row, Modifiers: p.Modifiers})
			}
		}
	case "terminal.keepalive":
		for _, id := range p.SessionIDs {
			if s := n.termStreamOf(id); s != nil {
				s.control()
			}
		}
	}
}

// terminalLoop runs observes one after another until the hub unwatches, or the session's terminal cannot be streamed
// any more (it stopped running): then a terminal.end tells the hub why.
func (n *Node) terminalLoop(ctx context.Context, id string, s *termStream) {
	defer s.cancel()
	fails := 0
	var why error
	for ctx.Err() == nil {
		frames, err := n.observeOnce(ctx, id)
		if ctx.Err() != nil {
			return
		}
		if frames > 0 {
			fails = 0
		} else {
			fails++
			why = err
		}
		if fails >= termGiveUp {
			break
		}
		select { // not a hot loop when herdr refuses at once
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(fails*fails)*250*time.Millisecond + 100*time.Millisecond):
		}
	}
	n.mu.Lock()
	mine := n.terms[id] == s
	if mine {
		delete(n.terms, id)
	}
	n.mu.Unlock()
	if mine && n.peer != nil {
		msg := "terminal stream ended"
		if why != nil {
			msg = why.Error()
		}
		_ = n.peer.Notify("terminal.end", map[string]any{"session_id": id, "error": msg})
	}
}

// observeOnce streams one epoch: from a whole frame until the pane changes size, herdr ends it or the replay is full.
// Returns how many frames went out.
func (n *Node) observeOnce(ctx context.Context, id string) (int, error) {
	pane, err := n.terminalPane(ctx, id)
	if err != nil {
		return 0, err
	}
	st, ref, err := paneCap[host.Streamer](n, pane)
	if err != nil {
		return 0, err
	}
	cols, rows, err := st.PaneSize(ctx, ref)
	if err != nil {
		return 0, err
	}
	octx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		t := time.NewTicker(termSizeEvery)
		defer t.Stop()
		for {
			select {
			case <-octx.Done():
				return
			case <-t.C:
				if c, r, err := st.PaneSize(octx, ref); err == nil && (c != cols || r != rows) {
					cancel()
					return
				}
			}
		}
	}()
	epoch := strconv.FormatInt(time.Now().UnixNano(), 36)
	frames, replay := 0, 0
	gate := &frameGate{minGap: termMinGap, send: func(f host.Frame) {
		frames++
		if f.Full {
			replay = 0
		} else {
			replay += len(f.Data)
		}
		if n.peer != nil {
			_ = n.peer.Notify("terminal.frame", map[string]any{"session_id": id, "epoch": epoch, "seq": frames,
				"full": f.Full, "cols": f.Width, "rows": f.Height, "data": n.scrubB64(f.Data)}) // base64: hidden here (secretenv.go)
		}
		if replay > termReplayLimit {
			cancel()
		}
	}}
	err = st.Observe(octx, ref, cols, rows, gate.put)
	gate.close() // stops future puts and timers, waits for any send under way, and drains what is held
	gate.mu.Lock()
	sent := frames
	gate.mu.Unlock()
	return sent, err
}
