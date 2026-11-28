package rpc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// laneWriter writes a framed connection (lanes.go). The peer's writers queue their lines on it and wait; one goroutine
// (run) cuts them into frames and writes them, each write a batch of up to frameMax bytes: the acknowledgement due
// first, then control and the other lanes, with a turn for waiting bulk during sustained traffic. A line once started is
// written to its end, as a line on the one writer was; one not started yet can still be withdrawn (NotSent).
type laneWriter struct {
	w      io.Writer
	closed <-chan struct{} // the peer's
	fail   func(error)     // closes the peer: a write failed, or the other side broke the protocol
	filter func([]byte) []byte
	wrote  *atomic.Int64 // when a write last went through (Peer.LastWrite); nil: not kept

	mu      sync.Mutex
	wake    chan struct{} // 1: lines queued, credit or an acknowledgement due
	busy    bool          // a write is under way
	err     error         // why the writer stopped; nil while it goes on
	lanes   [laneCount]laneQueue
	seq     uint64 // the last data frame numbered
	ackDue  uint64 // the reader has read every frame with credit through this one, and wants it acknowledged
	ackAt   uint32 // when it read it (clockMS)
	ackSent uint64
	win     creditWindow
	totals  [laneCount]laneTotals

	highFrames     int       // interactive or event frames sent while bulk waits, capped at highFramesBeforeBulk
	lastUncredited uint64    // latest event or interactive answer put on the transport without waiting for credit
	ackAdvanced    time.Time // when the peer last acknowledged new data, on any lane

	// Resuming (resume.go): keep says frames are kept until the other side acknowledges them (kept, keptBytes; none
	// through gapThrough: not kept, so the connection resumes only once those are acknowledged, ackedThrough).
	// allDue / allAt / allSent are the acknowledgement of every frame read, as ackDue is of those with credit. lost:
	// the transport of generation gen failed and the writer waits for the next; resend is the index in kept from which
	// it writes again what the other side did not read (-1: nothing to write again).
	keep         bool
	kept         []keptFrame
	keptBytes    int
	gapThrough   uint64
	ackedThrough uint64
	allDue       uint64
	allAt        uint32
	allSent      uint64
	allWaiting   bool // a write for the acknowledgement of every frame read is due (ackAll)
	gen          uint64
	lost         bool
	resend       int
	resent       int64      // frames written again, every resume together
	held         []*outLine // lines whose last frame went with a write that failed, until written again (run)
	lose         func(err error, gen uint64) bool
}

// keptFrame is a data frame kept for a resume, its header and payload. Frames after a gap are kept on: once the other
// side acknowledges the gap, the connection is resumable again.
type keptFrame struct {
	seq uint64
	b   []byte
	at  time.Time
}

// A pending bulk frame gets one turn after this many interactive or event frames. Control always goes first.
const highFramesBeforeBulk = 8

// laneSlots is how many lines of one lane may be in progress at once (laneQueue.slots); a frame's slot says which.
const laneSlots = 16

var newline = []byte("\n")

type laneQueue struct {
	lines []*outLine // waiting or in progress, in turn order
	next  int        // whose turn it is
	slots uint16     // slots in use, one bit each
}

type laneTotals struct {
	frames  int64
	maxWait time.Duration
}

// outLine is a line queued on a laneWriter.
type outLine struct {
	b       []byte // the line; once it starts, as it goes out: filtered, its newline dropped
	off     int    // written so far
	lane    Lane
	credit  bool // it waits for credit (route)
	slot    uint16
	started bool
	queued  time.Time
	done    chan error // told once: nil when the last frame is written, or why it never will be
}

func newLaneWriter(w io.Writer, closed <-chan struct{}, fail func(error), filter func([]byte) []byte) *laneWriter {
	return &laneWriter{w: w, closed: closed, fail: fail, filter: filter, wake: make(chan struct{}, 1), win: newCreditWindow(), resend: -1}
}

func (lw *laneWriter) poke() {
	select {
	case lw.wake <- struct{}{}:
	default:
	}
}

// start begins writing: from now on everything this side sends is frames.
func (lw *laneWriter) start() { go lw.run() }

// enqueue records any wait for the writer and, for a control line, the uncredited traffic already ahead of it.
func (lw *laneWriter) enqueue(l *outLine) (observation writeObservation, err error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if lw.err != nil {
		return observation, lw.err
	}
	observation.delayed = lw.busy
	for i := range lw.lanes {
		observation.delayed = observation.delayed || len(lw.lanes[i].lines) > 0
	}
	if l.lane == LaneControl && lw.lastUncredited > lw.ackedThrough {
		observation.writer = lw
		observation.acked, observation.ahead = lw.ackedThrough, lw.lastUncredited
	}
	q := &lw.lanes[l.lane]
	q.lines = append(q.lines, l)
	lw.poke()
	return observation, nil
}

// withdraw takes l back if none of it went out yet.
func (lw *laneWriter) withdraw(l *outLine) bool {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if !l.started {
		lw.lanes[l.lane].drop(l) // not found: the writer stopped and let it go
	}
	return !l.started
}

// drop takes l out of the turn order, which goes on with the line after it.
func (q *laneQueue) drop(l *outLine) {
	for i, x := range q.lines {
		if x == l {
			q.lines = append(q.lines[:i], q.lines[i+1:]...)
			if i < q.next {
				q.next--
			}
			if q.next >= len(q.lines) {
				q.next = 0
			}
			return
		}
	}
}

// ack asks for an acknowledgement of every frame read through seq, read at at (clockMS).
func (lw *laneWriter) ack(seq uint64, at uint32) {
	lw.mu.Lock()
	if seq > lw.ackDue {
		lw.ackDue, lw.ackAt = seq, at
		lw.poke()
	}
	lw.mu.Unlock()
}

// ackAll asks for an acknowledgement of every frame read through seq (resuming: what the other side need not keep).
// It goes with the next write, or on its own after allAckDelay: one a read would be a write a read the other way.
func (lw *laneWriter) ackAll(seq uint64, at uint32) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if seq <= lw.allDue {
		return
	}
	lw.allDue, lw.allAt = seq, at
	if !lw.allWaiting {
		lw.allWaiting = true
		time.AfterFunc(allAckDelay, func() {
			lw.mu.Lock()
			lw.allWaiting = false
			lw.mu.Unlock()
			lw.poke()
		})
	}
}

// allAckDelay bounds how long an acknowledgement of every frame read waits for a write to go with. It stays well under
// resumeStale, or the other side takes a transport for silent before an acknowledgement was due: a test that shortens
// resumeStale shortens this too (TestResumeBusyReaderIsNotSilence).
var allAckDelay = 500 * time.Millisecond

// acked takes the other side's acknowledgement of every frame through seq, the last read at at by its clock.
func (lw *laneWriter) acked(seq uint64, at uint32) error {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if seq > lw.seq {
		return fmt.Errorf("%w: acknowledges frame %d, %d sent", ErrFrame, seq, lw.seq)
	}
	now := time.Now()
	lw.dropKeptLocked(seq, now)
	lw.win.acked(seq, at, now)
	if len(lw.lanes[LaneInteractive].lines)+len(lw.lanes[LaneBulk].lines) > 0 { // what may wait for credit
		lw.poke()
	}
	return nil
}

func (lw *laneWriter) run() {
	buf := make([]byte, 0, frameMax)
	var finished []*outLine
	for {
		select {
		case <-lw.closed: // nothing more goes out on a closed peer, however much is queued
			lw.stop(ErrClosed)
			return
		default:
		}
		lw.mu.Lock()
		var batch []byte
		if !lw.lost {
			if lw.resend >= 0 && lw.resend < len(lw.kept) {
				batch, finished = lw.resendLocked(buf[:0]), finished[:0]
			} else {
				lw.resend = -1
				lw.releaseHeldLocked()
				batch, finished = lw.fillLocked(buf[:0], finished[:0], time.Now())
			}
		}
		if len(batch) == 0 {
			lw.mu.Unlock()
			select {
			case <-lw.wake:
			case <-lw.closed:
				lw.stop(ErrClosed)
				return
			}
			continue
		}
		w, gen := lw.w, lw.gen
		lw.busy = true
		lw.mu.Unlock()
		n, err := w.Write(batch)
		if err == nil && n != len(batch) {
			err = io.ErrShortWrite
		}
		if err == nil && lw.wrote != nil {
			lw.wrote.Store(time.Now().UnixNano())
		}
		resuming := err != nil && lw.lose != nil && lw.lose(err, gen)
		lw.mu.Lock()
		lw.busy = false
		switch {
		case resuming:
			// What this write carried is kept and goes again on the next transport: its lines are written once the
			// frames written again are (or told the error if the peer closes first).
			lw.held = append(lw.held, finished...)
			err = nil
		default:
			for _, l := range finished {
				l.done <- err
			}
			if err == nil && lw.resend < 0 {
				lw.releaseHeldLocked()
			}
		}
		lw.mu.Unlock()
		if err != nil {
			lw.stop(err)
			lw.fail(err)
			return
		}
	}
}

// releaseHeldLocked completes lines held across a lost transport once nothing remains to resend.
func (lw *laneWriter) releaseHeldLocked() {
	if len(lw.held) == 0 {
		return
	}
	for _, l := range lw.held {
		l.done <- nil
	}
	lw.held = nil
}

// stop ends the writer: every line still queued is told err.
func (lw *laneWriter) stop(err error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if lw.err == nil {
		lw.err = err
	}
	for i := range lw.lanes {
		q := &lw.lanes[i]
		for _, l := range q.lines {
			if l.started {
				l.done <- lw.err
			} else {
				l.done <- NotSent(lw.err) // none of it went out
			}
		}
		*q = laneQueue{}
	}
	for _, l := range lw.held {
		l.done <- lw.err
	}
	lw.held = nil
}

// fillLocked makes the next write: the acknowledgement due, then frames in priority order while they fit in frameMax
// (the first always does). Once the link's rate is known, credited frames in one write are also bounded by frameSize so
// slow WebSocket transports do not coalesce several small bulk frames back into one 32 KiB message ahead of pings.
// finished is the lines whose last frame it holds.
func (lw *laneWriter) fillLocked(batch []byte, finished []*outLine, now time.Time) ([]byte, []*outLine) {
	batch = lw.acksLocked(batch)
	creditBudget := frameMax
	if lw.win.rate > 0 {
		creditBudget = frameHeader + lw.win.frameSize()
	}
	creditBytes := 0
	for {
		l, n, ok := lw.pickLocked(frameMax-len(batch), creditBudget-creditBytes, len(batch) == 0, now)
		if !ok {
			break
		}
		lw.seq++
		fin := l.off+n == len(l.b)
		var h [frameHeader]byte
		putHead(h[:], frameHead{kind: frameData, lane: l.lane, fin: fin, slot: l.slot, size: n, seq: lw.seq})
		at := len(batch)
		batch = append(append(batch, h[:]...), l.b[l.off:l.off+n]...)
		lw.keepLocked(lw.seq, batch[at:], now)
		l.off += n
		lw.totals[l.lane].frames++
		switch {
		case l.lane == LaneBulk || len(lw.lanes[LaneBulk].lines) == 0:
			lw.highFrames = 0
		case l.lane != LaneControl && lw.highFrames < highFramesBeforeBulk:
			lw.highFrames++
		}
		if !l.credit && (l.lane == LaneEvents || l.lane == LaneInteractive) {
			lw.lastUncredited = lw.seq
		}
		if l.credit {
			lw.win.wrote(lw.seq, n, now, n == lw.most(l))
			creditBytes += frameHeader + n
		}
		if fin {
			q := &lw.lanes[l.lane]
			q.slots &^= 1 << l.slot
			q.drop(l)
			finished = append(finished, l)
		}
	}
	return batch, finished
}

// pickLocked is the next frame: its line, advanced to its turn, and its size; ok false when there is none, or when
// the next one would not fit in room (or creditRoom for credited frames, when the batch is not empty: first). Within a
// lane, lines that fit in one frame take turns with the lane's oldest long line; the other long lines wait until it is
// through, as they would on the one writer: taking turns among them would finish every one of them only at the end,
// each later than its caller can wait (concurrent file chunks, each a minute's worth on a slow link).
func (lw *laneWriter) pickLocked(room, creditRoom int, first bool, now time.Time) (*outLine, int, bool) {
	order := [laneCount]Lane{LaneControl, LaneInteractive, LaneEvents, LaneBulk}
	if lw.highFrames >= highFramesBeforeBulk {
		order = [laneCount]Lane{LaneControl, LaneBulk, LaneInteractive, LaneEvents}
	}
	for _, lane := range order {
		q := &lw.lanes[lane]
		if len(q.lines) == 0 {
			continue
		}
		long := q.long(lw.most, nil)
		var beside *outLine
		if long != nil && long.credit && !lw.win.room(min(long.left(), lw.most(long))) {
			// It waits for credit: the oldest long line that needs none (an answer) goes beside it, which a read loop
			// waiting for it (AfterReply) must never wait for credit through.
			beside = q.long(lw.most, func(l *outLine) bool { return !l.credit })
		}
		for k := 0; k < len(q.lines); k++ {
			i := (q.next + k) % len(q.lines)
			l := q.lines[i]
			most := lw.most(l)
			if l != long && l != beside && l.left() > most {
				continue // a long line behind the one going out
			}
			if !l.started && q.slots == 1<<laneSlots-1 {
				continue // every slot is busy with a line in progress: this one waits for its turn
			}
			n := min(l.left(), most)
			if l.credit && !lw.win.canSend(n) {
				continue // no credit for it; a line of the lane that needs none may go
			}
			if !first && (frameHeader+n > room || l.credit && frameHeader+n > creditRoom) {
				return nil, 0, false // the next frame in priority goes first in the next write
			}
			if !l.started {
				// Filtered as it goes on the wire, as a line on the one writer was: a value handed over (node/secretenv.go)
				// while the line waited is hidden too. Under the lock, the scan holds up the writers and the reader's
				// acknowledgements for as long as it takes, only while values are handed over.
				if lw.filter != nil {
					l.b = lw.filter(l.b)
				}
				l.b = bytes.TrimSuffix(l.b, newline)
				n = min(len(l.b), most)
				l.slot = uint16(bits.TrailingZeros16(^q.slots)) // a free one: not every slot is busy (above)
				q.slots |= 1 << l.slot
				l.started = true
				lw.totals[lane].maxWait = max(lw.totals[lane].maxWait, now.Sub(l.queued))
			}
			q.next = (i + 1) % len(q.lines)
			return l, n, true
		}
	}
	return nil, 0, false
}

// left is what of l is still to go; before it starts, before filter, which may change it a little: a hint is enough.
func (l *outLine) left() int {
	if l.started {
		return len(l.b) - l.off
	}
	return len(bytes.TrimSuffix(l.b, newline))
}

// long is the lane's long line whose frames go now, among those only takes (nil: all): the one in progress, else the
// oldest that needs more than one frame (most); nil when there is none.
func (q *laneQueue) long(most func(*outLine) int, only func(*outLine) bool) *outLine {
	var oldest *outLine
	for _, l := range q.lines {
		if only != nil && !only(l) {
			continue
		}
		if l.started {
			return l // a line started and not through is a long one: a short one starts and ends in one frame
		}
		if oldest == nil && l.left() > most(l) {
			oldest = l
		}
	}
	return oldest
}

// LaneStats is a framed connection's writer as it is now (Peer.LaneStats).
type LaneStats struct {
	Framed   bool // this side's output is framed
	Lanes    [laneCount]LaneCounters
	Window   int           // bytes of the lanes with credit (interactive, bulk) that may be in flight
	InFlight int           // those sent and not acknowledged
	RTT      time.Duration // the latest round trip of such a frame
	Queued   time.Duration // how long the latest such frame spent queued on its way
	// Resuming (resume.go): frames are kept for it, their bytes, and the frames written again, every resume together.
	Resume bool
	Kept   int
	Resent int64
}

// LaneCounters is one lane's: what waits now, the frames sent, and the longest a line waited for its first frame (one
// still waiting included).
type LaneCounters struct {
	QueuedFrames int
	QueuedBytes  int64
	SentFrames   int64
	MaxWait      time.Duration
}

// String is the counters on one line, for a log, each field per lane in priority order (control/interactive/events/
// bulk): "lanes=on queued=0/1/0/3 bytes=0B/1KiB/0B/96KiB maxwait=1ms/40ms/2ms/1.2s sent=12/340/88/51 window=64KiB
// inflight=32KiB rtt=410ms queued=30ms"; "lanes=off" for a connection of lines.
func (s LaneStats) String() string {
	if !s.Framed {
		return "lanes=off"
	}
	var queued, size, wait, sent [laneCount]string
	for i, c := range s.Lanes {
		queued[i] = fmt.Sprint(c.QueuedFrames)
		size[i] = sizeText(c.QueuedBytes)
		wait[i] = c.MaxWait.Round(time.Millisecond).String()
		sent[i] = fmt.Sprint(c.SentFrames)
	}
	line := fmt.Sprintf("lanes=on queued=%s bytes=%s maxwait=%s sent=%s window=%s inflight=%s rtt=%s queued=%s",
		strings.Join(queued[:], "/"), strings.Join(size[:], "/"), strings.Join(wait[:], "/"), strings.Join(sent[:], "/"),
		sizeText(int64(s.Window)), sizeText(int64(s.InFlight)), s.RTT.Round(time.Millisecond), s.Queued.Round(time.Millisecond))
	if s.Resume {
		line += fmt.Sprintf(" resume=on kept=%s resent=%d", sizeText(int64(s.Kept)), s.Resent)
	}
	return line
}

func sizeText(n int64) string {
	switch {
	case n >= 10<<20:
		return fmt.Sprintf("%dMiB", n>>20)
	case n >= 10<<10:
		return fmt.Sprintf("%dKiB", n>>10)
	}
	return fmt.Sprintf("%dB", n)
}

// most is the most a frame of l carries now: a quarter of the window when it waits for credit.
func (lw *laneWriter) most(l *outLine) int {
	if l.credit {
		return lw.win.frameSize()
	}
	return framePayloadMax
}

func (lw *laneWriter) stats() LaneStats {
	now := time.Now()
	lw.mu.Lock()
	defer lw.mu.Unlock()
	s := LaneStats{Framed: true, Window: lw.win.cwnd, InFlight: lw.win.inFlight, RTT: lw.win.rtt, Queued: lw.win.queued,
		Resume: lw.keep, Kept: lw.keptBytes, Resent: lw.resent}
	for i := range lw.lanes {
		c := &s.Lanes[i]
		c.SentFrames, c.MaxWait = lw.totals[i].frames, lw.totals[i].maxWait
		for _, l := range lw.lanes[i].lines {
			left, most := len(l.b)-l.off, lw.most(l)
			c.QueuedBytes += int64(left)
			c.QueuedFrames += max((left+most-1)/most, 1)
			if !l.started {
				c.MaxWait = max(c.MaxWait, now.Sub(l.queued))
			}
		}
	}
	return s
}

// acksLocked appends the acknowledgements due: of the frames with credit (the window's samples), then, resuming, of
// every frame read when that goes further.
func (lw *laneWriter) acksLocked(batch []byte) []byte {
	sent := uint64(0)
	if lw.ackDue > lw.ackSent {
		batch = appendAck(batch, lw.ackDue, lw.ackAt)
		lw.ackSent, sent = lw.ackDue, lw.ackDue
	}
	if lw.allDue > lw.allSent {
		if lw.allDue > sent {
			batch = appendAck(batch, lw.allDue, lw.allAt)
		}
		lw.allSent = lw.allDue
	}
	return batch
}

func appendAck(batch []byte, seq uint64, at uint32) []byte {
	var h [frameHeader + ackPayload]byte
	putHead(h[:], frameHead{kind: frameAck, size: ackPayload, seq: seq})
	binary.BigEndian.PutUint32(h[frameHeader:], at)
	return append(batch, h[:]...)
}

// keepLocked keeps frame seq, b (header and payload), for a resume. Past resumeKeepMax bytes, or with the oldest kept
// for resumeKeepAge (the other side does not acknowledge), nothing is kept through seq: a gap the other side must
// acknowledge before the connection can resume again.
func (lw *laneWriter) keepLocked(seq uint64, b []byte, now time.Time) {
	if !lw.keep {
		return
	}
	if lw.keptBytes+len(b) > resumeKeepMax || len(lw.kept) > 0 && now.Sub(lw.kept[0].at) > resumeKeepAge {
		lw.kept, lw.keptBytes, lw.gapThrough = nil, 0, seq
		return
	}
	lw.kept = append(lw.kept, keptFrame{seq: seq, b: bytes.Clone(b), at: now})
	lw.keptBytes += len(b)
}

// dropKeptLocked lets go what the other side read, every frame through seq.
func (lw *laneWriter) dropKeptLocked(seq uint64, now time.Time) {
	if seq > lw.ackedThrough {
		lw.ackedThrough, lw.ackAdvanced = seq, now
	}
	i := 0
	for i < len(lw.kept) && lw.kept[i].seq <= seq {
		lw.keptBytes -= len(lw.kept[i].b)
		i++
	}
	if i > 0 {
		clear(lw.kept[:i]) // the frames' bytes go; the slice moves on (append makes it anew when it outgrows its end)
		lw.kept = lw.kept[i:]
		if lw.resend >= 0 {
			if len(lw.kept) == 0 {
				lw.resend = -1
			} else {
				lw.resend = max(lw.resend-i, 0)
			}
		}
	}
}

// oldestKept is when the oldest frame waiting to be acknowledged was written; zero with none.
func (lw *laneWriter) oldestKept() time.Time {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if len(lw.kept) == 0 {
		return time.Time{}
	}
	return lw.kept[0].at
}

// resumable: what is kept would resume the connection, every frame after those the other side acknowledged.
func (lw *laneWriter) resumable() bool {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.keptAfterLocked(lw.ackedThrough)
}

// canResume: the frames after peerRead, which a resume writes again, are all kept.
func (lw *laneWriter) canResume(peerRead uint64) bool {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.keptAfterLocked(peerRead)
}

// keptAfterLocked: every frame written after seq is kept. What is kept is every frame after the gap and the
// acknowledged ones, so seq must be at least both.
func (lw *laneWriter) keptAfterLocked(seq uint64) bool {
	return lw.keep && max(lw.gapThrough, lw.ackedThrough) <= seq && seq <= lw.seq
}

// loseGen: the transport of generation gen failed; nothing more is written until resume. w is what it was written on.
func (lw *laneWriter) loseGen(gen uint64) (w io.Writer) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if gen != lw.gen {
		return nil
	}
	lw.lost = true
	return lw.w
}

// resume writes on w, generation gen, from now on: first again what came after peerRead, then on as before. The
// acknowledgements go again too, the last ones may have been lost with the old transport.
func (lw *laneWriter) resume(w io.Writer, gen, peerRead uint64) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	lw.dropKeptLocked(peerRead, time.Now())
	lw.w, lw.gen, lw.lost = w, gen, false
	lw.resend = -1
	if len(lw.kept) > 0 {
		lw.resend = 0
	}
	lw.ackSent, lw.allSent = 0, 0
	lw.win.resumed(peerRead, time.Now())
	lw.poke()
}

// resendLocked is the next write of frames written again (resume), whole frames up to frameMax (the first always).
func (lw *laneWriter) resendLocked(batch []byte) []byte {
	batch = lw.acksLocked(batch)
	for lw.resend >= 0 && lw.resend < len(lw.kept) {
		f := lw.kept[lw.resend]
		if len(batch) > 0 && len(batch)+len(f.b) > frameMax {
			break
		}
		batch = append(batch, f.b...)
		lw.resend++
		lw.resent++
	}
	if lw.resend >= len(lw.kept) {
		lw.resend = -1
	}
	return batch
}
