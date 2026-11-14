package screenlink

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// The node's end of the video over UDP (#7873): the UDP sockets beside the direct routes' TCP listener, and each flow
// a phone opens on a direct TLS connection. Nothing here answers a packet that doesn't open under a live flow's keys,
// and nothing goes to an address the phone hasn't proved it holds.

const (
	// udpPaceDefault and udpPaceMax bound the pacing in kbit/s: the phone asks for a rate in its pings (#7873's
	// design: from the bursts it measures); before it does, and never above the ceiling, these apply.
	udpPaceDefault = 40_000
	udpPaceMax     = 150_000
	// udpPaceMin: the pacing never goes below this, nor below 4 × the video's own rate, so a frame leaves well inside
	// its interval.
	udpPaceMin = 8_000
	// udpChallenges: the most addresses with an outstanding challenge per flow.
	udpChallenges = 4
	// udpResendQueue bounds the shards and messages queued to go again.
	udpResendQueue = 4096
	// udpFlowsPerSession: one per direct connection, and no more than this per session.
	udpFlowsPerSession = maxDirect
	// udpProbeAfter: video left unacknowledged this long with nothing newer to send has its newest message's last
	// shard sent again, so the phone learns of a message lost whole at the end of a burst; the wait doubles each time,
	// up to udpProbeMax.
	udpProbeAfter = 50 * time.Millisecond
	udpProbeMax   = 800 * time.Millisecond
)

// udpFlow is one phone's UDP flow, opened on (and ending with) a direct TLS transport of a session.
type udpFlow struct {
	id     [8]byte
	d      *Direct
	s      *Session
	parent *transport
	keys   *udpKeys
	out    *udpSealer
	heard  atomic.Int64 // when the last authenticated packet came (unix ns)

	mu         sync.Mutex
	recv       replayWindow
	conn       *net.UDPConn // the socket the phone's packets come in on
	peer       *net.UDPAddr // the address the phone proved it holds; nil until then
	challenges map[string]uint64
	t          *transport // the session's transport for this flow, from the first proof on
	ratio      int        // parity per data shard, ten-thousandths (the phone's ping)
	paceKbps   int        // the phone's pacing request
	closed     bool

	// Pacing, used by the session's sending goroutine only.
	paceNext  time.Time
	rateStart time.Time
	rateBytes int
	videoKbps int

	sentData, sentParity, sentResent atomic.Uint64
}

// flowConn stands in for a UDP flow's transport connection: closing it closes the flow.
type flowConn struct{ f *udpFlow }

func (c flowConn) Read([]byte) (int, error)  { return 0, net.ErrClosed }
func (c flowConn) Write([]byte) (int, error) { return 0, net.ErrClosed }
func (c flowConn) Close() error              { c.f.close(); return nil }
func (f *udpFlow) heardSince() time.Duration { return time.Since(time.Unix(0, f.heard.Load())) }
func (f *udpFlow) idText() string            { return base64.RawURLEncoding.EncodeToString(f.id[:]) }
func sameAddr(a, b *net.UDPAddr) bool {
	return a != nil && b != nil && a.IP.Equal(b.IP) && a.Port == b.Port
}
func randomU64() uint64 { var b [8]byte; _, _ = rand.Read(b[:]); return binary.BigEndian.Uint64(b[:]) }

// MARK: sockets (Direct)

// bindUDPLocked opens a UDP socket on each candidate address: on the port it had before (the node's addresses moved
// under live flows), else the TCP listener's port number when it is free there (one port for both), else one
// ephemeral port shared by all; a candidate where none works gets no UDP.
func (d *Direct) bindUDPLocked() {
	d.closeUDPLocked()
	port := d.udpLast
	if port == 0 {
		port = d.port
	}
	for _, c := range d.cands {
		ip := net.ParseIP(c.Addr)
		if ip == nil {
			continue
		}
		u, err := net.ListenUDP("udp", &net.UDPAddr{IP: ip, Port: port})
		if err != nil && len(d.udpConns) == 0 && port != 0 {
			port = 0 // the number is taken here: one ephemeral port for every candidate instead
			u, err = net.ListenUDP("udp", &net.UDPAddr{IP: ip})
		}
		if err != nil {
			continue
		}
		if port == 0 {
			port = u.LocalAddr().(*net.UDPAddr).Port
		}
		_ = u.SetReadBuffer(4 << 20)
		_ = u.SetWriteBuffer(4 << 20)
		d.udpConns = append(d.udpConns, u)
		go d.readUDP(u)
	}
	d.udpPort = 0
	if len(d.udpConns) > 0 {
		d.udpPort = port
	}
}

func (d *Direct) closeUDPLocked() {
	for _, u := range d.udpConns {
		_ = u.Close()
	}
	if d.udpPort != 0 {
		d.udpLast = d.udpPort
	}
	d.udpConns, d.udpPort = nil, 0
}

// readUDP hands each packet to its flow; a packet that names no live flow is dropped without an answer.
func (d *Direct) readUDP(u *net.UDPConn) {
	buf := make([]byte, 2048)
	for {
		n, from, err := u.ReadFromUDP(buf)
		if err != nil {
			return
		}
		id, pn, ok := udpHeader(buf[:n])
		if !ok || n > udpMaxPacket {
			continue
		}
		d.mu.Lock()
		f := d.flows[id]
		d.mu.Unlock()
		if f != nil {
			f.receive(append([]byte(nil), buf[:n]...), pn, from, u)
		}
	}
}

// openFlow answers udp_open on parent, a direct TLS transport of s: a new flow whose keys come from parent's TLS
// exporter. An earlier flow of the same connection closes.
func (d *Direct) openFlow(s *Session, parent *transport) UDPReady {
	if parent.tls == nil {
		return UDPReady{Unavailable: "relay"}
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return UDPReady{Unavailable: "udp"}
	}
	cs := parent.tls.ConnectionState()
	material, err := cs.ExportKeyingMaterial(UDPExporterLabel, id[:], udpKeyMaterial)
	if err != nil {
		return UDPReady{Unavailable: "udp"}
	}
	keys, err := newUDPKeys(material)
	if err != nil {
		return UDPReady{Unavailable: "udp"}
	}
	f := &udpFlow{id: id, d: d, s: s, parent: parent, keys: keys, challenges: map[string]uint64{},
		out: &udpSealer{aead: keys.toPhone, iv: keys.ivPhone, flow: id}}
	f.heard.Store(time.Now().UnixNano())
	d.mu.Lock()
	if d.udpPort == 0 {
		d.mu.Unlock()
		return UDPReady{Unavailable: "udp"}
	}
	var stale []*udpFlow
	n := 0
	for _, g := range d.flows {
		if g.s != s {
			continue
		}
		if g.parent == parent {
			stale = append(stale, g)
		} else {
			n++
		}
	}
	if n >= udpFlowsPerSession {
		d.mu.Unlock()
		return UDPReady{Unavailable: "busy"}
	}
	d.flows[id] = f
	port := d.udpPort
	d.mu.Unlock()
	for _, g := range stale {
		g.closeTransport()
	}
	d.logf("screenlink: a udp flow opened")
	return UDPReady{Port: port, Flow: f.idText()}
}

// MARK: a flow

// close forgets the flow: its packets are dropped from now on.
func (f *udpFlow) close() {
	f.d.mu.Lock()
	if f.d.flows[f.id] == f {
		delete(f.d.flows, f.id)
	}
	f.d.mu.Unlock()
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}

// closeTransport ends the flow and, once it has one, its session transport (the video moves off it).
func (f *udpFlow) closeTransport() {
	f.mu.Lock()
	t := f.t
	f.mu.Unlock()
	if t != nil {
		f.s.lose(t)
	}
	f.close()
}

// receive is one packet for this flow: dropped unless it opens under the phone's key with a packet number not seen
// before. Only a hello is answered before the phone has proved its address, and the answer is never larger.
func (f *udpFlow) receive(p []byte, pn uint64, from *net.UDPAddr, conn *net.UDPConn) {
	body, err := openUDP(f.keys.toNode, f.keys.ivNode, p, pn)
	if err != nil {
		return
	}
	f.mu.Lock()
	if f.closed || !f.recv.accept(pn) {
		f.mu.Unlock()
		return
	}
	f.heard.Store(time.Now().UnixNano())
	kind := body[0]
	if kind == udpHello {
		reply, validated := f.helloLocked(body, from, conn)
		t := f.t
		f.mu.Unlock()
		if reply != nil {
			f.sendTo(reply, from, conn)
		}
		if validated && t == nil {
			f.s.addFlow(f)
		}
		return
	}
	t := f.t
	if t == nil || !sameAddr(f.peer, from) {
		f.mu.Unlock()
		return // nothing but a hello counts until the phone proves this address
	}
	f.conn = conn // the socket may have been opened again (the node's addresses moved)
	if kind == udpPing && len(body) >= 15 {
		f.ratio = min(int(binary.BigEndian.Uint16(body[9:])), udpMaxParity)
		f.paceKbps = int(binary.BigEndian.Uint32(body[11:]))
	}
	f.mu.Unlock()
	f.s.udpMessage(t, f, kind, body[1:])
}

// helloLocked answers a hello: a challenge for an address the phone hasn't proved, or the proof's confirmation. The
// phone's hello is at least udpHelloMin, so the answer (smaller) can't amplify a forged source address.
func (f *udpFlow) helloLocked(body []byte, from *net.UDPAddr, conn *net.UDPConn) ([]byte, bool) {
	if len(body) < 1+udpHelloMin {
		return nil, false
	}
	echo := binary.BigEndian.Uint64(body[1:9])
	key := from.String()
	answer := func(challenge uint64, ok bool) []byte {
		b := make([]byte, 10)
		b[0] = udpHello
		binary.BigEndian.PutUint64(b[1:], challenge)
		if ok {
			b[9] = 1
		}
		return b
	}
	if sameAddr(f.peer, from) {
		f.conn = conn
		return answer(0, true), true
	}
	if c, ok := f.challenges[key]; ok && echo != 0 && echo == c {
		f.peer, f.conn = from, conn
		f.challenges = map[string]uint64{}
		return answer(0, true), true
	}
	if len(f.challenges) >= udpChallenges {
		f.challenges = map[string]uint64{}
	}
	c := randomU64() | 1
	f.challenges[key] = c
	return answer(c, false), false
}

// sendTo seals body and sends it to addr on conn.
func (f *udpFlow) sendTo(body []byte, addr *net.UDPAddr, conn *net.UDPConn) int {
	p, ok := f.out.seal(body)
	if !ok {
		go f.closeTransport() // the packet numbers ran out: never reuse a nonce
		return 0
	}
	_, _ = conn.WriteToUDP(p, addr)
	return len(p)
}

// send seals body and sends it to the proved address; nothing without one.
func (f *udpFlow) send(body []byte) int {
	f.mu.Lock()
	peer, conn, closed := f.peer, f.conn, f.closed
	f.mu.Unlock()
	if peer == nil || conn == nil || closed {
		return 0
	}
	return f.sendTo(body, peer, conn)
}

// pace waits until n more bytes may leave: the phone's rate, kept between 4 × the video's own rate (and udpPaceMin)
// and udpPaceMax, with up to 2 ms of credit.
func (f *udpFlow) pace(n int) {
	f.mu.Lock()
	want := f.paceKbps
	f.mu.Unlock()
	if want <= 0 {
		want = udpPaceDefault
	}
	kbps := min(max(want, 4*f.videoKbps, udpPaceMin), udpPaceMax)
	now := time.Now()
	if f.paceNext.Before(now.Add(-2 * time.Millisecond)) {
		f.paceNext = now.Add(-2 * time.Millisecond)
	}
	f.paceNext = f.paceNext.Add(time.Duration(float64(n) * 8 / float64(kbps) * float64(time.Millisecond)))
	if wait := f.paceNext.Sub(now); wait > time.Millisecond {
		time.Sleep(wait)
	}
}

// counted keeps the video's own rate (new messages' bytes a second) for the pacing floor.
func (f *udpFlow) counted(n int) {
	now := time.Now()
	if f.rateStart.IsZero() {
		f.rateStart = now
	}
	f.rateBytes += n
	if d := now.Sub(f.rateStart); d >= time.Second {
		f.videoKbps = int(float64(f.rateBytes) * 8 / d.Seconds() / 1000)
		f.rateStart, f.rateBytes = now, 0
	}
}

// MARK: the session's side

// udpOpen answers udp_open on t, which must be a direct TLS transport.
func (s *Session) udpOpen(t *transport) {
	ready := UDPReady{Unavailable: "relay"}
	if s.direct != nil && !t.relay && t.tls != nil {
		ready = s.direct.openFlow(s, t)
	}
	body, _ := json.Marshal(ready)
	s.queue(t, frame(TypeUDPReady, body))
}

// addFlow gives a flow whose phone proved its address a transport in the session. It carries video only, and only
// once the phone moves the video onto it (use).
func (s *Session) addFlow(f *udpFlow) {
	s.mu.Lock()
	if s.ended || f.parent.dead {
		s.mu.Unlock()
		f.close()
		return
	}
	t := &transport{flow: f, parent: f.parent, conn: flowConn{f}, acks: map[byte]uint64{}}
	s.ts[t] = true
	s.mu.Unlock()
	f.mu.Lock()
	if f.closed || f.t != nil {
		f.mu.Unlock()
		s.mu.Lock()
		t.dead = true
		delete(s.ts, t)
		s.mu.Unlock()
		return
	}
	f.t = t
	f.mu.Unlock()
	go s.udpLoop(t)
}

// udpMessage acts on one message of a flow whose phone proved its address.
func (s *Session) udpMessage(t *transport, f *udpFlow, kind byte, b []byte) {
	switch kind {
	case udpPing:
		if len(b) < 8 {
			return
		}
		pong := make([]byte, 33)
		pong[0] = udpPong
		copy(pong[1:9], b[:8])
		binary.BigEndian.PutUint64(pong[9:], f.sentData.Load())
		binary.BigEndian.PutUint64(pong[17:], f.sentParity.Load())
		binary.BigEndian.PutUint64(pong[25:], f.sentResent.Load())
		f.send(pong)
	case udpUse:
		want, err := offsets(b)
		if err != nil {
			return
		}
		var resumed []byte
		s.mu.Lock()
		for id, off := range want {
			ch := s.chans[id]
			if ch == nil || ch.finished || !ch.video() || t.dead {
				continue // RFB never rides UDP
			}
			if !s.moveLocked(ch, t, off) {
				continue
			}
			if len(ch.buf) > 0 && len(ch.marks) > 0 && ch.marks[0].off != ch.base {
				s.dropBacklogLocked(ch) // not at a message boundary: go on at a keyframe
			}
			resumed = append(resumed, chanOffset(id, ch.recv.Load())...)
		}
		s.cond.Broadcast()
		s.mu.Unlock()
		f.send(append([]byte{udpResumed}, resumed...))
	case udpAck, udpSkip:
		if len(b) < 9 {
			return
		}
		id, off := b[0], binary.BigEndian.Uint64(b[1:9])
		s.mu.Lock()
		defer s.mu.Unlock()
		ch := s.chans[id]
		if ch == nil || ch.finished || !ch.video() {
			return
		}
		if kind == udpAck {
			s.ackedLocked(ch, off)
			return
		}
		if ch.active != t {
			return
		}
		if off < ch.base {
			ch.reset = true // gave up something already dropped: the reset was lost, say it again
		} else if s.ackedLocked(ch, off) {
			s.skipBacklogLocked(ch)
		}
		s.cond.Broadcast()
	case udpNack:
		if len(b) < 1 || (len(b)-1)%12 != 0 {
			return
		}
		s.mu.Lock()
		if ch := s.chans[b[0]]; ch != nil && ch.active == t && ch.video() {
			for e := b[1:]; len(e) >= 12 && len(t.resend) < udpResendQueue; e = e[12:] {
				t.resend = append(t.resend, udpResend{off: binary.BigEndian.Uint64(e), block: binary.BigEndian.Uint16(e[8:]),
					index: binary.BigEndian.Uint16(e[10:])})
			}
			s.cond.Broadcast()
		}
		s.mu.Unlock()
	case udpWant:
		if len(b) < 17 {
			return
		}
		from, to := binary.BigEndian.Uint64(b[1:]), binary.BigEndian.Uint64(b[9:])
		s.mu.Lock()
		if ch := s.chans[b[0]]; ch != nil && ch.active == t && ch.video() {
			if from < ch.base {
				ch.reset = true // the phone missed where the stream went on
			}
			for _, mk := range ch.marks {
				if mk.off >= max(from, ch.base) && mk.off < to && len(t.resend) < udpResendQueue {
					t.resend = append(t.resend, udpResend{off: mk.off, whole: true})
				}
			}
			s.cond.Broadcast()
		}
		s.mu.Unlock()
	}
}

// skipBacklogLocked is the phone giving the message at ch.base up: the backlog's frames are dropped, the stream goes
// on after a reset at a keyframe the helper is asked for, and the backlog's other messages (format, encoding, …) still
// go out after the reset, so that keyframe decodes. Like dropBacklogLocked, which a move with an old backlog uses.
func (s *Session) skipBacklogLocked(ch *channel) {
	var keep [][]byte
	end := ch.base + uint64(len(ch.buf))
	for i, mk := range ch.marks {
		next := end
		if i+1 < len(ch.marks) {
			next = ch.marks[i+1].off
		}
		if mk.off < ch.base || next-mk.off < 5 {
			continue // a message the phone has part of
		}
		msg := ch.buf[mk.off-ch.base : next-ch.base]
		if msg[4] != videoFrame {
			keep = append(keep, msg)
		}
	}
	dropped := len(ch.buf)
	ch.base = end
	ch.sent = end
	ch.buf, ch.marks = nil, nil
	ch.reset, ch.keyWait = true, true
	now := s.now()
	for _, m := range keep {
		ch.marks = append(ch.marks, mark{off: ch.base + uint64(len(ch.buf)), at: now})
		ch.buf = append(ch.buf, m...)
	}
	s.logf("screenlink: the phone gave a frame up: %d bytes dropped for a keyframe", dropped)
	go ch.upstream.inject(ch, keyframeRequest)
}

// udpResend is a data shard (or a whole message) the phone asked for again.
type udpResend struct {
	off          uint64
	block, index uint16
	whole        bool
}

// udpJob is what the flow's goroutine sends next, outside the session's lock.
type udpJob struct {
	ctl    []byte // a control body
	ch     byte
	off    uint64
	msg    []byte // the message (a slice of the channel's buffer, never changed in place)
	shards []udpResend
	resent bool
}

// udpLoop sends what a flow's transport has to send until it or the session ends.
func (s *Session) udpLoop(t *transport) {
	f := t.flow
	for {
		s.mu.Lock()
		var job udpJob
		var ok bool
		for {
			if t.dead || s.ended {
				s.mu.Unlock()
				return
			}
			if job, ok = s.nextUDPLocked(t); ok {
				break
			}
			s.armProbeLocked(t)
			s.cond.Wait()
		}
		s.mu.Unlock()
		f.sendJob(job)
	}
}

// probeDue is when t's next tail probe goes, if one is owed: video sent and not all acknowledged.
func (s *Session) probeDue(t *transport, ch *channel) (time.Time, bool) {
	if ch == nil || ch.active != t || ch.finished || ch.sent <= ch.base || t.sentAt.IsZero() {
		return time.Time{}, false
	}
	return t.sentAt.Add(min(udpProbeAfter<<min(t.probes, 8), udpProbeMax)), true
}

// armProbeLocked wakes t's sender when its next tail probe is due.
func (s *Session) armProbeLocked(t *transport) {
	due, ok := s.probeDue(t, s.chans[s.video])
	if !ok || t.armed {
		return
	}
	t.armed = true
	time.AfterFunc(max(time.Until(due), time.Millisecond), func() {
		s.mu.Lock()
		t.armed = false
		s.cond.Broadcast()
		s.mu.Unlock()
	})
}

// messageLocked is the message of ch starting at off, from the buffer; nil when it is no longer there.
func messageLocked(ch *channel, off uint64) []byte {
	end := ch.base + uint64(len(ch.buf))
	for i, mk := range ch.marks {
		if mk.off != off || off < ch.base {
			continue
		}
		next := end
		if i+1 < len(ch.marks) {
			next = ch.marks[i+1].off
		}
		return ch.buf[off-ch.base : next-ch.base]
	}
	return nil
}

func (s *Session) nextUDPLocked(t *transport) (udpJob, bool) {
	ch := s.chans[s.video]
	if ch == nil || ch.active != t || ch.finished {
		t.resend = nil
		return udpJob{}, false
	}
	if ch.reset {
		ch.reset = false
		return udpJob{ctl: chanOffsetBody(udpReset, ch.id, ch.base)}, true
	}
	for len(t.resend) > 0 {
		r := t.resend[0]
		t.resend = t.resend[1:]
		msg := messageLocked(ch, r.off)
		if msg == nil {
			continue // acknowledged or dropped meanwhile
		}
		t.sentAt = time.Now()
		job := udpJob{ch: ch.id, off: r.off, msg: msg, resent: true}
		if !r.whole {
			job.shards = append(job.shards, r)
			// The rest asked for the same message go along.
			for len(t.resend) > 0 && !t.resend[0].whole && t.resend[0].off == r.off {
				job.shards = append(job.shards, t.resend[0])
				t.resend = t.resend[1:]
			}
		}
		return job, true
	}
	end := ch.base + uint64(len(ch.buf))
	if ch.sent < end {
		msg := messageLocked(ch, ch.sent)
		if msg == nil {
			// Not at a message boundary (it can't be after a use at one): go on at a keyframe, the reset first.
			s.dropBacklogLocked(ch)
			return s.nextUDPLocked(t)
		}
		if ch.sent == ch.base {
			ch.progress = s.now() // the first byte the phone hasn't acknowledged goes out: the stall clock starts
		}
		off := ch.sent
		ch.sent += uint64(len(msg))
		t.sentAt, t.probes = time.Now(), 0
		return udpJob{ch: ch.id, off: off, msg: msg}, true
	}
	if due, ok := s.probeDue(t, ch); ok && !time.Now().Before(due) {
		// Tail probe: the newest message sent, its last data shard again.
		var last uint64
		for _, mk := range ch.marks {
			if mk.off < ch.sent && mk.off >= ch.base {
				last = mk.off
			}
		}
		if msg := messageLocked(ch, last); msg != nil {
			t.sentAt = time.Now()
			t.probes++
			blocks := udpBlocks(len(msg))
			b := blocks[len(blocks)-1]
			return udpJob{ch: ch.id, off: last, msg: msg, resent: true,
				shards: []udpResend{{off: last, block: uint16(len(blocks) - 1), index: uint16(b.Data - 1)}}}, true
		}
	}
	if ch.eof && !ch.sentClose {
		// The helper's stream ended: the close goes on the flow's TLS connection, in order after what it sent.
		if p := t.parent; p != nil && !p.dead {
			ch.sentClose = true
			p.ctl = append(p.ctl, frame(TypeClose, chanOffset(ch.id, end)))
			s.cond.Broadcast()
		}
	}
	return udpJob{}, false
}

// sendJob builds a job's packets (fragments and parity) and sends them, paced.
func (f *udpFlow) sendJob(job udpJob) {
	if job.ctl != nil {
		f.send(job.ctl)
		return
	}
	var flags byte
	if len(job.msg) > 4 && job.msg[4] == videoFrame {
		flags = udpDroppable
	}
	blocks := udpBlocks(len(job.msg))
	f.mu.Lock()
	ratio := f.ratio
	f.mu.Unlock()
	if !job.resent {
		f.counted(len(job.msg))
	}
	emit := func(body []byte, counter *atomic.Uint64) {
		f.pace(len(body) + udpHeaderLen + udpTagLen)
		counter.Add(uint64(f.send(body)))
	}
	if len(job.shards) > 0 {
		// Data shards asked for again; their parity count is left out (the phone keeps the block's first).
		for _, r := range job.shards {
			if int(r.block) >= len(blocks) || int(r.index) >= blocks[r.block].Data {
				continue
			}
			b := blocks[r.block]
			h := udpFragmentHead{Channel: job.ch, Offset: job.off, Length: uint32(len(job.msg)), Block: r.block, Index: r.index,
				Data: uint8(b.Data), Flags: flags}
			emit(h.encode(udpShard(job.msg, b.First+int(r.index))), &f.sentResent)
		}
		return
	}
	data, parity := &f.sentData, &f.sentParity
	if job.resent {
		data, parity = &f.sentResent, &f.sentResent
	}
	for bi, b := range blocks {
		m := udpParityCount(b.Data, ratio)
		h := udpFragmentHead{Channel: job.ch, Offset: job.off, Length: uint32(len(job.msg)), Block: uint16(bi),
			Data: uint8(b.Data), Parity: uint8(m), Flags: flags}
		for j := 0; j < b.Data; j++ {
			h.Index = uint16(j)
			emit(h.encode(udpShard(job.msg, b.First+j)), data)
		}
		ps, err := udpParity(job.msg, b, m)
		if err != nil {
			continue
		}
		for i, p := range ps {
			h.Index = uint16(b.Data + i)
			emit(h.encode(p), parity)
		}
	}
}
