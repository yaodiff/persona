package screenlink

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Limits bound what a session keeps and how long it waits, so the node's memory never grows with how long a route
// has been down: a channel's unacknowledged bytes are capped by size (Window*) and by age (StallAfter), and a session
// waits for a lost transport only Grace. Starting values; the bench tunes them.
type Limits struct {
	// WindowRFB and WindowVideo: the most node → phone bytes a channel keeps unacknowledged. With that much out the
	// node stops reading Screen Sharing (it waits, as over TCP) or Persona Screen (it drops frames, as it does today
	// when the stream is full).
	WindowRFB, WindowVideo int
	// StallAfter: the oldest unacknowledged byte this old means the transport carrying it is stuck; it is closed and
	// the channel goes on elsewhere.
	StallAfter time.Duration
	// Grace: a channel with no transport this long is closed (the RFB channel ends the session), and so is a session
	// with no hub relay this long, so the hub keeps the last word over a session.
	Grace time.Duration
	// VideoBacklog: when the video channel moves to another transport with a backlog older than this, the backlog is
	// dropped and the stream goes on from a new keyframe: a late picture is better than replaying old frames.
	VideoBacklog time.Duration
	// AttachWait: a transport joining a live session sends its token within this.
	AttachWait time.Duration
	// Tick: how often the limits are checked.
	Tick time.Duration
}

// DefaultLimits are the limits a node runs with.
var DefaultLimits = Limits{
	WindowRFB:    4 << 20,
	WindowVideo:  512 << 10,
	StallAfter:   10 * time.Second,
	Grace:        10 * time.Second,
	VideoBacklog: 200 * time.Millisecond,
	AttachWait:   3 * time.Second,
	Tick:         250 * time.Millisecond,
}

// maxVideoMessage is the video wire's own bound on one message (ScreenVideoWire.maxMessageBytes).
const maxVideoMessage = 4 << 20

// Video wire message types the node looks at (design-3954.protocol.md): a frame's flags say whether it is a keyframe;
// format carries the parameter sets that come before one; keyframe asks the helper for one.
const (
	videoFormat   byte = 2
	videoFrame    byte = 3
	videoKeyframe byte = 18
	videoKeyFlag  byte = 1
)

// keyframeRequest is the video wire's `keyframe` message (phone → helper), which the node writes to the helper after
// dropping a backlog.
var keyframeRequest = []byte{0, 0, 0, 1, videoKeyframe}

// Session is one live screen at the node: its channels (RFB, then video) and the transports carrying them.
type Session struct {
	pair   string
	token  string
	limits Limits
	logf   func(string, ...any)
	now    func() time.Time
	onEnd  func()

	mu           sync.Mutex
	cond         *sync.Cond
	chans        map[byte]*channel
	video        byte // the current video channel's id, 0 before the first
	ts           map[*transport]bool
	noRelaySince time.Time
	ended        bool
	done         chan struct{}
}

type channel struct {
	id     byte
	local  io.ReadWriteCloser
	window int

	// Node → phone. buf holds bytes [base, base+len(buf)): base is what the phone acknowledged, sent where the
	// active transport's writer is. Bytes in buf are never changed in place, so a writer may hold a slice of it.
	buf       []byte
	base      uint64
	sent      uint64
	marks     []mark // where each read (RFB) or message (video) starts in buf, and when it was read
	eof       bool   // local ended: a close follows the last byte
	sentClose bool
	reset     bool // a reset at base goes out before more data
	keyWait   bool // video: frames are dropped until a keyframe
	active    *transport
	home      *transport // the relay the hub opened for it
	lostAt    time.Time  // since when no transport carries it; zero while one does
	progress  time.Time  // when the phone last acknowledged something, or the channel last moved or started sending
	finished  bool

	// Phone → node, written to local under umu in order; recv is the next offset expected (written under umu, read
	// anywhere).
	umu      sync.Mutex
	recv     atomic.Uint64
	closeAt  *uint64 // the phone closed the channel at this offset
	upstream videoUpstream
}

// video reports whether ch carries a video stream (its messages' lengths are followed; a stale backlog is dropped).
func (ch *channel) video() bool { return ch.id >= FirstVideo }

type mark struct {
	off uint64
	at  time.Time
}

type transport struct {
	conn    io.ReadWriteCloser
	relay   bool
	homeFor byte // a relay: the channel the hub opened it for
	pending bool // must attach with the token first
	dead    bool
	ctl     [][]byte                      // control messages waiting to go out
	acks    map[byte]uint64               // acknowledgements waiting to go out, the newest per channel
	counter func() (retrans, sent uint32) // TCP counters for pongs; nil on a relay
}

// Registry is the node's sessions by pair.
type Registry struct {
	limits Limits
	logf   func(string, ...any)
	direct *Direct // the direct routes' listener; nil: relays only

	mu     sync.Mutex
	byPair map[string]*Session
}

// NewRegistry makes a registry whose sessions run with limits; logf gets kinds, counts and timings only.
func NewRegistry(limits Limits, logf func(string, ...any)) *Registry {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Registry{limits: limits, logf: logf, byPair: map[string]*Session{}}
}

// Get is the live session for pair, nil without one.
func (r *Registry) Get(pair string) *Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byPair[pair]
}

// EnableDirect has the registry's sessions offer direct routes through d.
func (r *Registry) EnableDirect(d *Direct) {
	r.mu.Lock()
	r.direct = d
	r.mu.Unlock()
}

// Len is how many sessions are live (tests, logs).
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byPair)
}

// Open starts the session for pair around rfb, the node's connection to Screen Sharing, and returns the endpoint of
// its first relay for streams.Mux.Start. A session already there for pair (a phone that started over) ends first.
func (r *Registry) Open(pair string, rfb io.ReadWriteCloser) (*Session, io.ReadWriteCloser) {
	if old := r.Get(pair); old != nil {
		old.End()
	}
	s := &Session{pair: pair, token: newToken(), limits: r.limits, logf: r.logf, now: time.Now,
		chans: map[byte]*channel{}, ts: map[*transport]bool{}, done: make(chan struct{})}
	s.cond = sync.NewCond(&s.mu)
	r.mu.Lock()
	r.byPair[pair] = s
	direct := r.direct
	r.mu.Unlock()
	s.onEnd = func() {
		r.mu.Lock()
		if r.byPair[pair] == s {
			delete(r.byPair, pair)
		}
		r.mu.Unlock()
		if direct != nil {
			direct.leave(s)
		}
	}
	paths := Paths{Version: 1, Token: s.token, Candidates: []Candidate{}}
	if direct != nil {
		paths = direct.join(s)
	}
	body, _ := json.Marshal(paths)
	s.mu.Lock()
	ch := s.addChannelLocked(RFB, rfb, s.limits.WindowRFB)
	relay := s.addTransportLocked(true, RFB, false)
	ch.active, ch.home = relay, relay
	relay.ctl = append(relay.ctl, frame(TypePaths, body))
	s.mu.Unlock()
	go s.watch()
	return s, s.startTransport(relay)
}

// Video starts a video channel around the helper's connection and returns the endpoint of its relay. Each video
// stream of a session gets its own channel id (2 and up), so bytes of an earlier stream still on their way are never
// taken for the new one's; an earlier stream still open ends, with its relays. The relay's first message names the
// id (resumed, at 0). It reports false when the session has ended.
func (s *Session) Video(helper io.ReadWriteCloser) (io.ReadWriteCloser, bool) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return nil, false
	}
	var stale []io.Closer
	if old := s.chans[s.video]; old != nil {
		delete(s.chans, s.video)
		if !old.finished {
			old.finished = true
			stale = append(stale, old.local)
		}
		for t := range s.ts {
			if t.relay && t.homeFor == s.video {
				t.dead = true
				delete(s.ts, t)
				if t.conn != nil {
					stale = append(stale, t.conn)
				}
			}
		}
	}
	id := FirstVideo
	if s.video >= FirstVideo && s.video < 255 {
		id = s.video + 1
	}
	s.video = id
	ch := s.addChannelLocked(id, helper, s.limits.WindowVideo)
	relay := s.addTransportLocked(true, id, false)
	ch.active, ch.home = relay, relay
	relay.ctl = append(relay.ctl, frame(TypeResumed, chanOffset(id, 0)))
	s.cond.Broadcast()
	s.mu.Unlock()
	for _, c := range stale {
		_ = c.Close()
	}
	return s.startTransport(relay), true
}

// HasVideo reports whether a live video channel is there for a relay to reattach to.
func (s *Session) HasVideo() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.chans[s.video]
	return ch != nil && !ch.finished && !s.ended
}

// Reattach is a hub relay rejoining the session for its RFB channel (video: the current video channel) after the
// phone's relay dropped while the session went on: it must send the token first, and then becomes the channel's relay.
func (s *Session) Reattach(video bool) (io.ReadWriteCloser, bool) {
	s.mu.Lock()
	ch := RFB
	if video {
		ch = s.video
	}
	c := s.chans[ch]
	if s.ended || c == nil || c.finished {
		s.mu.Unlock()
		return nil, false
	}
	t := s.addTransportLocked(true, ch, true)
	s.mu.Unlock()
	return s.startTransport(t), true
}

// setPaths tells the phone the direct routes changed (the node's addresses moved, or the firewall changed), on the
// transport carrying RFB.
func (s *Session) setPaths(p Paths) {
	body, err := json.Marshal(p)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	t := s.chans[RFB].active
	if t == nil || t.dead || t.pending {
		for c := range s.ts {
			if !c.dead && !c.pending {
				t = c
				break
			}
		}
	}
	if t == nil {
		return
	}
	t.ctl = append(t.ctl, frame(TypePaths, body))
	s.cond.Broadcast()
}

// addDirect is a direct connection that attached with this session's token (Direct.handshake): one more transport,
// reading on with r. False when the session ended or has maxDirect already.
func (s *Session) addDirect(conn net.Conn, r *reader, counter func() (uint32, uint32)) bool {
	s.mu.Lock()
	n := 0
	for t := range s.ts {
		if !t.relay && !t.dead {
			n++
		}
	}
	if s.ended || n >= maxDirect {
		s.mu.Unlock()
		return false
	}
	t := &transport{conn: conn, acks: map[byte]uint64{}, counter: counter}
	t.ctl = append(t.ctl, frame(TypeAttached))
	s.ts[t] = true
	s.cond.Broadcast()
	s.mu.Unlock()
	go s.writeLoop(t)
	go s.readFrom(t, r)
	return true
}

// End ends the session: every transport and channel closes. Safe to call more than once.
func (s *Session) End() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	var closers []io.Closer
	for t := range s.ts {
		t.dead = true
		if t.conn != nil {
			closers = append(closers, t.conn)
		}
	}
	for _, ch := range s.chans {
		if !ch.finished {
			ch.finished = true
			closers = append(closers, ch.local)
		}
	}
	s.cond.Broadcast()
	s.mu.Unlock()
	for _, c := range closers {
		_ = c.Close()
	}
	s.onEnd()
	close(s.done)
}

// Done is closed when the session has ended.
func (s *Session) Done() <-chan struct{} { return s.done }

func newToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func (s *Session) addChannelLocked(id byte, local io.ReadWriteCloser, window int) *channel {
	ch := &channel{id: id, local: local, window: window}
	s.chans[id] = ch
	go s.readLocal(ch)
	return ch
}

func (s *Session) addTransportLocked(relay bool, homeFor byte, pending bool) *transport {
	t := &transport{relay: relay, homeFor: homeFor, pending: pending, acks: map[byte]uint64{}}
	s.ts[t] = true
	if relay && !pending {
		s.noRelaySince = time.Time{}
	}
	return t
}

// startTransport wires t to a pipe and returns the other end for the stream it rides on.
func (s *Session) startTransport(t *transport) io.ReadWriteCloser {
	mine, theirs := net.Pipe()
	s.mu.Lock()
	t.conn = mine
	s.mu.Unlock()
	go s.writeLoop(t)
	go s.readFrom(t, newReader(mine))
	if t.pending {
		time.AfterFunc(s.limits.AttachWait, func() {
			s.mu.Lock()
			late := t.pending && !t.dead
			s.mu.Unlock()
			if late {
				s.logf("screenlink: a transport did not attach in time")
				s.lose(t)
			}
		})
	}
	return theirs
}

// MARK: node → phone

// readLocal reads the channel's local end into its buffer while the window has room: RFB as it comes, video a whole
// message at a time so a backlog can be dropped whole.
func (s *Session) readLocal(ch *channel) {
	chunk := make([]byte, 32<<10)
	for {
		s.mu.Lock()
		for !ch.finished && len(ch.buf) >= ch.window {
			s.cond.Wait()
		}
		if ch.finished {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		var b []byte
		var err error
		if ch.video() {
			b, err = readVideoMessage(ch.local)
		} else {
			var n int
			n, err = ch.local.Read(chunk)
			b = append([]byte(nil), chunk[:n]...)
		}
		s.mu.Lock()
		if len(b) > 0 && !ch.finished && s.keepLocked(ch, b) {
			ch.marks = append(ch.marks, mark{off: ch.base + uint64(len(ch.buf)), at: s.now()})
			ch.buf = append(ch.buf, b...)
			s.cond.Broadcast()
		}
		if err != nil {
			ch.eof = true
			s.cond.Broadcast()
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}

// keepLocked says whether a message read from the helper goes out: after a dropped backlog, frames wait for the
// keyframe the helper was asked for.
func (s *Session) keepLocked(ch *channel, b []byte) bool {
	if !ch.video() || !ch.keyWait || len(b) < 5 {
		return true
	}
	switch b[4] {
	case videoFrame:
		if len(b) > 17 && b[17]&videoKeyFlag != 0 {
			ch.keyWait = false
			return true
		}
		return false
	}
	return true
}

func readVideoMessage(r io.Reader) ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(head[:])
	if n < 1 || n > maxVideoMessage {
		return nil, errFrame
	}
	b := make([]byte, 4+n)
	copy(b, head[:])
	if _, err := io.ReadFull(r, b[4:]); err != nil {
		return nil, err
	}
	return b, nil
}

// writeLoop sends what t has to send: control messages and acknowledgements first, then the channels it carries,
// RFB before video so a small RFB message never waits behind a run of video.
func (s *Session) writeLoop(t *transport) {
	for {
		s.mu.Lock()
		var out []byte
		for {
			if t.dead || s.ended {
				s.mu.Unlock()
				return
			}
			if out = s.nextLocked(t); out != nil {
				break
			}
			s.cond.Wait()
		}
		conn := t.conn
		s.mu.Unlock()
		if _, err := conn.Write(out); err != nil {
			s.lose(t)
			return
		}
	}
}

func (s *Session) nextLocked(t *transport) []byte {
	if len(t.ctl) > 0 {
		out := t.ctl[0]
		t.ctl = t.ctl[1:]
		return out
	}
	for id, off := range t.acks {
		delete(t.acks, id)
		return frame(TypeAck, chanOffset(id, off))
	}
	if t.pending {
		return nil
	}
	for _, id := range []byte{RFB, s.video} {
		ch := s.chans[id]
		if ch == nil || ch.active != t || ch.finished {
			continue
		}
		if ch.reset {
			ch.reset = false
			return frame(TypeReset, chanOffset(id, ch.base))
		}
		end := ch.base + uint64(len(ch.buf))
		if ch.sent < end {
			if ch.sent == ch.base {
				ch.progress = s.now() // the first byte the phone hasn't acknowledged goes out: the stall clock starts
			}
			from := ch.sent - ch.base
			to := min(from+dataChunk, uint64(len(ch.buf)))
			out := frame(TypeData, chanOffset(id, ch.sent), ch.buf[from:to])
			ch.sent = ch.base + to
			return out
		}
		if ch.eof && !ch.sentClose {
			ch.sentClose = true
			return frame(TypeClose, chanOffset(id, end))
		}
	}
	return nil
}

// ackedLocked drops what the phone has through off.
func (s *Session) ackedLocked(ch *channel, off uint64) bool {
	end := ch.base + uint64(len(ch.buf))
	if off > end {
		return false
	}
	if off <= ch.base {
		return true
	}
	ch.buf = ch.buf[off-ch.base:]
	ch.base = off
	ch.progress = s.now()
	if ch.sent < off {
		ch.sent = off
	}
	// Keep the mark the first byte still left belongs to.
	i := 0
	for i+1 < len(ch.marks) && ch.marks[i+1].off <= off {
		i++
	}
	ch.marks = ch.marks[i:]
	if len(ch.buf) == 0 {
		ch.marks = nil
	}
	s.cond.Broadcast()
	return true
}

// ageLocked is how long the oldest byte the phone hasn't acknowledged has been waiting; 0 with none.
func (s *Session) ageLocked(ch *channel) time.Duration {
	if len(ch.buf) == 0 || len(ch.marks) == 0 {
		return 0
	}
	return s.now().Sub(ch.marks[0].at)
}

// moveLocked has t carry ch from off on (what the phone has). A video backlog older than VideoBacklog is dropped and
// the stream goes on at a new keyframe; RFB is always sent on exactly from off.
func (s *Session) moveLocked(ch *channel, t *transport, off uint64) bool {
	if !s.ackedLocked(ch, off) {
		return false
	}
	moved := ch.active != t
	ch.active, ch.lostAt = t, time.Time{}
	if !moved {
		return true // the same route: what it has in flight still arrives
	}
	ch.sent, ch.progress = ch.base, s.now()
	if ch.video() && len(ch.buf) > 0 && s.ageLocked(ch) > s.limits.VideoBacklog {
		s.dropBacklogLocked(ch)
	}
	s.cond.Broadcast()
	return true
}

// dropBacklogLocked drops the video channel's unacknowledged messages, tells the phone where the stream goes on, and
// asks the helper for a keyframe; frames before it are dropped.
func (s *Session) dropBacklogLocked(ch *channel) {
	dropped := len(ch.buf)
	ch.base += uint64(len(ch.buf))
	ch.sent = ch.base
	ch.buf, ch.marks = nil, nil
	ch.reset, ch.keyWait = true, true
	s.logf("screenlink: video moved with a %d-byte backlog: dropped for a keyframe", dropped)
	go ch.upstream.inject(ch, keyframeRequest)
}

// MARK: phone → node

func (s *Session) readFrom(t *transport, r *reader) {
	for {
		typ, body, err := r.next()
		if err != nil {
			s.lose(t)
			return
		}
		if !s.handle(t, typ, body) {
			s.lose(t)
			return
		}
	}
}

// handle acts on one message from the phone; false closes the transport.
func (s *Session) handle(t *transport, typ byte, body []byte) bool {
	s.mu.Lock()
	pending := t.pending
	s.mu.Unlock()
	if pending {
		return s.attach(t, typ, body)
	}
	switch typ {
	case TypeData:
		id, off, data, err := readChanOffset(body)
		if err != nil {
			return false
		}
		return s.receive(t, id, off, data)
	case TypeAck:
		id, off, _, err := readChanOffset(body)
		if err != nil {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if ch := s.chans[id]; ch != nil && !ch.finished {
			return s.ackedLocked(ch, off)
		}
		return true
	case TypePing:
		if len(body) < 8 {
			return false
		}
		var retrans, sent uint32
		if t.counter != nil {
			retrans, sent = t.counter()
		}
		var tail [8]byte
		binary.BigEndian.PutUint32(tail[:4], retrans)
		binary.BigEndian.PutUint32(tail[4:], sent)
		s.queue(t, frame(TypePong, body[:8], tail[:]))
		return true
	case TypeUse:
		want, err := offsets(body)
		if err != nil {
			return false
		}
		var resumed []byte
		s.mu.Lock()
		for id, off := range want {
			ch := s.chans[id]
			if ch == nil || ch.finished {
				continue
			}
			if !s.moveLocked(ch, t, off) {
				s.mu.Unlock()
				return false
			}
			resumed = append(resumed, chanOffset(id, ch.recv.Load())...)
		}
		t.ctl = append(t.ctl, frame(TypeResumed, resumed))
		s.cond.Broadcast()
		s.mu.Unlock()
		return true
	case TypeClose:
		id, off, _, err := readChanOffset(body)
		if err != nil {
			return false
		}
		s.peerClose(id, off)
		return true
	}
	return true // a type this node doesn't know: skipped, as on the video wire
}

// attach checks a joining transport's first message: the session's token, or it is closed.
func (s *Session) attach(t *transport, typ byte, body []byte) bool {
	var a struct {
		Token string `json:"token"`
	}
	if typ != TypeAttach || json.Unmarshal(body, &a) != nil || subtle.ConstantTimeCompare([]byte(a.Token), []byte(s.token)) != 1 {
		s.logf("screenlink: a transport sent no valid token")
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.dead || s.ended {
		return false
	}
	t.pending = false
	t.ctl = append(t.ctl, frame(TypeAttached))
	if t.relay {
		s.noRelaySince = time.Time{}
		if ch := s.chans[t.homeFor]; ch != nil && (ch.home == nil || ch.home.dead) {
			ch.home = t
		}
	}
	s.cond.Broadcast()
	return true
}

// receive writes the phone's bytes for channel id to its local end once each, in order: a copy that came twice (over
// the old route and the new) is trimmed. A gap means the phone skipped bytes: the transport is closed.
func (s *Session) receive(t *transport, id byte, off uint64, data []byte) bool {
	s.mu.Lock()
	ch := s.chans[id]
	s.mu.Unlock()
	if ch == nil {
		return true
	}
	ch.umu.Lock()
	recv := ch.recv.Load()
	if off > recv {
		ch.umu.Unlock()
		s.logf("screenlink: a gap in the phone's bytes on channel %d", id)
		return false
	}
	if skip := recv - off; skip < uint64(len(data)) {
		fresh := data[skip:]
		var err error
		if ch.video() {
			err = ch.upstream.write(ch.local, fresh)
		} else {
			_, err = ch.local.Write(fresh)
		}
		if err != nil {
			ch.umu.Unlock()
			s.finish(ch)
			return true
		}
		recv += uint64(len(fresh))
		ch.recv.Store(recv)
	}
	closeAt := ch.closeAt
	ch.umu.Unlock()
	s.mu.Lock()
	t.acks[id] = recv
	s.cond.Broadcast()
	s.mu.Unlock()
	if closeAt != nil && recv >= *closeAt {
		s.finish(ch)
	}
	return true
}

// peerClose is the phone closing channel id once it has every byte through off.
func (s *Session) peerClose(id byte, off uint64) {
	s.mu.Lock()
	ch := s.chans[id]
	s.mu.Unlock()
	if ch == nil {
		return
	}
	ch.umu.Lock()
	ch.closeAt = &off
	done := ch.recv.Load() >= off
	ch.umu.Unlock()
	if done {
		s.finish(ch)
	}
}

func (s *Session) queue(t *transport, msg []byte) {
	s.mu.Lock()
	t.ctl = append(t.ctl, msg)
	s.cond.Broadcast()
	s.mu.Unlock()
}

// MARK: losing and ending

// lose is t gone (its stream or connection ended, or it broke the protocol): each channel it carried goes on over its
// own relay, or any other transport, from what the phone acknowledged; one with none waits up to Grace.
func (s *Session) lose(t *transport) {
	s.mu.Lock()
	if t.dead {
		s.mu.Unlock()
		return
	}
	t.dead = true
	delete(s.ts, t)
	now := s.now()
	for _, ch := range s.chans {
		if ch.home == t {
			ch.home = nil
		}
		if ch.active != t || ch.finished {
			continue
		}
		if alt := s.fallbackLocked(ch); alt != nil {
			s.moveLocked(ch, alt, ch.base)
		} else {
			ch.active, ch.lostAt = nil, now
		}
	}
	if s.noRelaySince.IsZero() && !s.hasRelayLocked() {
		s.noRelaySince = now
	}
	conn := t.conn
	s.cond.Broadcast()
	s.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (s *Session) fallbackLocked(ch *channel) *transport {
	if ch.home != nil && !ch.home.dead && !ch.home.pending {
		return ch.home
	}
	for t := range s.ts {
		if !t.dead && !t.pending {
			return t
		}
	}
	return nil
}

func (s *Session) hasRelayLocked() bool {
	for t := range s.ts {
		if t.relay && !t.dead && !t.pending {
			return true
		}
	}
	return false
}

// finish closes one channel's local end; the RFB channel's ends the session (video needs it).
func (s *Session) finish(ch *channel) {
	s.mu.Lock()
	if ch.finished {
		s.mu.Unlock()
		return
	}
	ch.finished = true
	s.cond.Broadcast()
	s.mu.Unlock()
	_ = ch.local.Close()
	if ch.id == RFB {
		s.End()
	}
}

// watch checks the limits every Tick until the session ends.
func (s *Session) watch() {
	tick := time.NewTicker(s.limits.Tick)
	defer tick.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-tick.C:
		}
		var stuck []*transport
		var expired []*channel
		end := false
		s.mu.Lock()
		now := s.now()
		for _, ch := range s.chans {
			if ch.finished {
				continue
			}
			if ch.active == nil && !ch.lostAt.IsZero() && now.Sub(ch.lostAt) >= s.limits.Grace {
				expired = append(expired, ch)
			}
			// The phone has had bytes this long without acknowledging them: the route carrying them is stuck.
			if ch.active != nil && ch.sent > ch.base && now.Sub(ch.progress) >= s.limits.StallAfter {
				stuck = append(stuck, ch.active)
			}
			// The local end finished and the phone has everything: the channel is done.
			if ch.eof && ch.sentClose && len(ch.buf) == 0 {
				expired = append(expired, ch)
			}
		}
		if !s.noRelaySince.IsZero() && now.Sub(s.noRelaySince) >= s.limits.Grace {
			end = true
		}
		s.mu.Unlock()
		for _, t := range stuck {
			s.logf("screenlink: a transport left bytes unacknowledged too long")
			s.lose(t)
		}
		for _, ch := range expired {
			s.finish(ch)
		}
		if end {
			s.logf("screenlink: no hub relay for %s: the session ends", s.limits.Grace)
			s.End()
			return
		}
	}
}

// MARK: video upstream

// videoUpstream follows the phone's video messages to the helper by their lengths, so the node can put its own
// keyframe request between two of them, never inside one.
type videoUpstream struct {
	head    int // bytes of the current message's length prefix seen (0–4)
	lenBuf  [4]byte
	left    uint32 // bytes of the current message's body still to come
	pending []byte // a message to write at the next boundary
}

func (u *videoUpstream) atBoundary() bool { return u.head == 0 && u.left == 0 }

// write passes the phone's bytes to the helper, then a waiting message once a boundary is reached. umu is held.
func (u *videoUpstream) write(w io.Writer, b []byte) error {
	if _, err := w.Write(b); err != nil {
		return err
	}
	for len(b) > 0 {
		if u.left > 0 {
			n := min(uint32(len(b)), u.left)
			u.left -= n
			b = b[n:]
			if u.left == 0 {
				u.head = 0
			}
			continue
		}
		n := copy(u.lenBuf[u.head:], b)
		u.head += n
		b = b[n:]
		if u.head == 4 {
			u.left = binary.BigEndian.Uint32(u.lenBuf[:])
			if u.left == 0 {
				return errFrame
			}
		}
	}
	if u.atBoundary() && u.pending != nil {
		msg := u.pending
		u.pending = nil
		_, err := w.Write(msg)
		return err
	}
	return nil
}

// inject writes msg to the helper now if no phone message is half written, else right after the one that is.
func (u *videoUpstream) inject(ch *channel, msg []byte) {
	ch.umu.Lock()
	defer ch.umu.Unlock()
	if !u.atBoundary() {
		u.pending = msg
		return
	}
	_, _ = ch.local.Write(msg)
}
