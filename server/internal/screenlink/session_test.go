package screenlink

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// Short limits so the tests run in well under a second each.
var testLimits = Limits{
	WindowRFB:    64 << 10,
	WindowVideo:  32 << 10,
	StallAfter:   300 * time.Millisecond,
	Grace:        300 * time.Millisecond,
	VideoBacklog: 50 * time.Millisecond,
	AttachWait:   300 * time.Millisecond,
	Tick:         10 * time.Millisecond,
}

// phone is the phone's end of a session as the app keeps it: per channel, the bytes received in order (copies that
// came twice trimmed), and every other message in arrival order.
type phone struct {
	t   *testing.T
	ack bool // acknowledge what arrives

	mu       sync.Mutex
	got      map[byte][]byte
	recv     map[byte]uint64
	msgs     chan msg
	carriers []*carrier
}

type msg struct {
	typ  byte
	body []byte
	via  *carrier
}

// carrier is one transport as the phone holds it.
type carrier struct {
	p    *phone
	conn io.ReadWriteCloser
	wmu  sync.Mutex
	gone chan struct{}
}

func newPhone(t *testing.T, ack bool) *phone {
	return &phone{t: t, ack: ack, got: map[byte][]byte{}, recv: map[byte]uint64{}, msgs: make(chan msg, 1024)}
}

func (p *phone) carry(conn io.ReadWriteCloser) *carrier {
	c := &carrier{p: p, conn: conn, gone: make(chan struct{})}
	p.carriers = append(p.carriers, c)
	p.t.Cleanup(func() { conn.Close() })
	go func() {
		defer close(c.gone)
		r := newReader(conn)
		for {
			typ, body, err := r.next()
			if err != nil {
				return
			}
			body = append([]byte(nil), body...)
			switch typ {
			case TypeData:
				id, off, data, _ := readChanOffset(body)
				p.mu.Lock()
				have := p.recv[id]
				if off <= have && off+uint64(len(data)) > have {
					p.got[id] = append(p.got[id], data[have-off:]...)
					p.recv[id] = off + uint64(len(data))
				}
				recv := p.recv[id]
				p.mu.Unlock()
				if p.ack {
					c.send(TypeAck, chanOffset(id, recv))
				}
			case TypeAck:
				// The node acknowledging the phone's bytes: a real phone frees them; nothing to check here.
			case TypeReset:
				id, off, _, _ := readChanOffset(body)
				p.mu.Lock()
				p.recv[id] = off
				p.mu.Unlock()
				p.msgs <- msg{typ, body, c}
			default:
				p.msgs <- msg{typ, body, c}
			}
		}
	}()
	return c
}

func (c *carrier) send(typ byte, parts ...[]byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, _ = c.conn.Write(frame(typ, parts...))
}

func (p *phone) received(id byte) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.got[id]...)
}

func (p *phone) offset(id byte) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.recv[id]
}

// next is the next non-data message, failing the test after a second.
func (p *phone) next(want byte) msg {
	p.t.Helper()
	select {
	case m := <-p.msgs:
		if m.typ != want {
			p.t.Fatalf("message %d, want %d (%q)", m.typ, want, m.body)
		}
		return m
	case <-time.After(2 * time.Second):
		p.t.Fatalf("no message %d", want)
	}
	return msg{}
}

// waitFor polls cond for up to two seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for !cond() {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting: %s", what)
		case <-tick.C:
		}
	}
}

// fakeClock is a session's clock in a test that needs time to pass without waiting for it.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// useClock has s read c's time from now on (s.now is read under s.mu).
func useClock(s *Session) *fakeClock {
	c := &fakeClock{now: time.Now()}
	s.mu.Lock()
	s.now = c.Now
	s.mu.Unlock()
	return c
}

// screenSharing is the far end of the node's connection to Screen Sharing (or to the helper).
func screenSharing(t *testing.T) (node, far net.Conn) {
	node, far = net.Pipe()
	t.Cleanup(func() { node.Close(); far.Close() })
	return node, far
}

// readAll gathers what the far end reads until it has n bytes.
func readN(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(r, b); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("read %d bytes: timed out", n)
	}
	return b
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

func token(t *testing.T, m msg) string {
	t.Helper()
	var p Paths
	if err := json.Unmarshal(m.body, &p); err != nil || p.Token == "" || p.Version != 1 {
		t.Fatalf("paths: %q %v", m.body, err)
	}
	return p.Token
}

func attachBody(tok string) []byte {
	b, _ := json.Marshal(map[string]string{"token": tok})
	return b
}

// RFB through the session's relay, both ways, the token first and no candidates before the direct routes.
func TestRFBGoesBothWays(t *testing.T) {
	reg := NewRegistry(testLimits, nil)
	local, ss := screenSharing(t)
	s, relay := reg.Open("pair1", local)
	defer s.End()
	p := newPhone(t, true)
	c := p.carry(relay)
	m := p.next(TypePaths)
	var paths Paths
	_ = json.Unmarshal(m.body, &paths)
	if paths.Token == "" || len(paths.Candidates) != 0 {
		t.Fatalf("paths %+v", paths)
	}

	go ss.Write([]byte("RFB 003.889\n"))
	waitFor(t, "the greeting", func() bool { return string(p.received(RFB)) == "RFB 003.889\n" })
	c.send(TypeData, chanOffset(RFB, 0), []byte("RFB 003.008\n"))
	if got := readN(t, ss, 12); string(got) != "RFB 003.008\n" {
		t.Fatalf("Screen Sharing read %q", got)
	}
	if reg.Get("pair1") != s {
		t.Fatal("the registry holds the session by pair")
	}
}

// A dropped relay (the hub restarted, the phone changed networks) and a new one reattaching with the token: every byte
// arrives once and in order, both ways. A wrong token is refused.
func TestARelayReattachesAndResumesExactly(t *testing.T) {
	reg := NewRegistry(testLimits, nil)
	local, ss := screenSharing(t)
	s, relay := reg.Open("pair1", local)
	defer s.End()
	p := newPhone(t, true)
	c := p.carry(relay)
	tok := token(t, p.next(TypePaths))

	down := pattern(300 << 10)
	go func() {
		for i := 0; i < len(down); i += 10 << 10 {
			if _, err := ss.Write(down[i : i+10<<10]); err != nil {
				return
			}
		}
	}()
	waitFor(t, "some of the stream", func() bool { return len(p.received(RFB)) > 40<<10 })
	up := pattern(5000)
	upRead := make(chan []byte, 1)
	go func() { b := make([]byte, 3000); io.ReadFull(ss, b); upRead <- b }()
	c.send(TypeData, chanOffset(RFB, 0), up[:3000])
	upGot := <-upRead
	relay.Close() // the hub's stream ends mid-stream

	// A transport that doesn't know the token is closed.
	bad, ok := s.Reattach(false)
	if !ok {
		t.Fatal("reattach")
	}
	pb := newPhone(t, true)
	cb := pb.carry(bad)
	cb.send(TypeAttach, attachBody("not-the-token"))
	select {
	case <-cb.gone:
	case <-time.After(time.Second):
		t.Fatal("a wrong token keeps the transport")
	}

	again, _ := s.Reattach(false)
	c2 := p.carry(again)
	c2.send(TypeAttach, attachBody(tok))
	p.next(TypeAttached)
	c2.send(TypeUse, chanOffset(RFB, p.offset(RFB)))
	resumed := p.next(TypeResumed)
	at, err := offsets(resumed.body)
	if err != nil || at[RFB] != 3000 {
		t.Fatalf("resumed %v %v", at, err)
	}
	// The phone resends from where the node is: the node writes each byte once.
	go func() { b := make([]byte, 2000); io.ReadFull(ss, b); upRead <- b }()
	c2.send(TypeData, chanOffset(RFB, 2000), up[2000:]) // overlapping what already went
	upGot = append(upGot, <-upRead...)
	if !bytes.Equal(upGot, up) {
		t.Fatal("Screen Sharing got the phone's bytes out of order or twice")
	}
	waitFor(t, "the whole stream", func() bool { return len(p.received(RFB)) == len(down) })
	if !bytes.Equal(p.received(RFB), down) {
		t.Fatal("the phone got Screen Sharing's bytes out of order or twice")
	}
}

// The node reads Screen Sharing only while what the phone hasn't acknowledged fits the window, however long the
// phone is away: memory doesn't grow with an outage.
func TestTheWindowBoundsWhatTheNodeHolds(t *testing.T) {
	reg := NewRegistry(testLimits, nil)
	local, ss := screenSharing(t)
	s, relay := reg.Open("pair1", local)
	defer s.End()
	p := newPhone(t, false) // never acknowledges
	p.carry(relay)
	p.next(TypePaths)

	// Write until a write blocks: the node stopped reading Screen Sharing. Each write has a second to go through, far
	// longer than the node takes while it has room.
	got, chunk := 0, make([]byte, 4<<10)
	for got < 4*testLimits.WindowRFB {
		_ = ss.SetWriteDeadline(time.Now().Add(time.Second))
		n, err := ss.Write(chunk)
		got += n
		if err != nil {
			break
		}
	}
	if got > testLimits.WindowRFB+32<<10 {
		t.Fatalf("the node read %d bytes with nothing acknowledged; window %d", got, testLimits.WindowRFB)
	}
	if got < testLimits.WindowRFB {
		t.Fatalf("the node read only %d bytes; it should fill the window", got)
	}
}

// A transport that leaves bytes unacknowledged for StallAfter is stuck: it is closed, and with no other the session
// ends after Grace.
func TestAStuckTransportIsClosedAndTheSessionEnds(t *testing.T) {
	reg := NewRegistry(testLimits, nil)
	local, ss := screenSharing(t)
	s, relay := reg.Open("pair1", local)
	p := newPhone(t, false)
	c := p.carry(relay)
	p.next(TypePaths)
	go ss.Write([]byte("hello"))
	waitFor(t, "the bytes", func() bool { return string(p.received(RFB)) == "hello" })
	select {
	case <-c.gone:
	case <-time.After(2 * time.Second):
		t.Fatal("a transport that never acknowledges stays open")
	}
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a session with no relay goes on")
	}
	if reg.Get("pair1") != nil {
		t.Fatal("an ended session stays in the registry")
	}
	if _, err := ss.Write([]byte("x")); err == nil {
		t.Fatal("the connection to Screen Sharing stays open after the session ended")
	}
}

// Screen Sharing closing: the phone gets every byte, then close at its end. The phone closing: Screen Sharing's
// connection closes once its bytes are written, and the session ends.
func TestClosesGoBothWays(t *testing.T) {
	reg := NewRegistry(testLimits, nil)
	local, ss := screenSharing(t)
	s, relay := reg.Open("pair1", local)
	p := newPhone(t, true)
	c := p.carry(relay)
	p.next(TypePaths)
	go func() { ss.Write([]byte("bye")); ss.Close() }()
	m := p.next(TypeClose)
	if id, off, _, _ := readChanOffset(m.body); id != RFB || off != 3 || string(p.received(RFB)) != "bye" {
		t.Fatalf("close %d at %d after %q", id, off, p.received(RFB))
	}
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the RFB channel ended and the session goes on")
	}

	local2, ss2 := screenSharing(t)
	s2, relay2 := reg.Open("pair2", local2)
	p2 := newPhone(t, true)
	c2 := p2.carry(relay2)
	p2.next(TypePaths)
	read := make(chan []byte, 1)
	go func() { b := make([]byte, 4); io.ReadFull(ss2, b); read <- b }()
	c2.send(TypeData, chanOffset(RFB, 0), []byte("last"))
	c2.send(TypeClose, chanOffset(RFB, 4))
	if got := <-read; string(got) != "last" {
		t.Fatalf("read %q", got)
	}
	select {
	case <-s2.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the phone closed RFB and the session goes on")
	}
	_ = c
}

// videoMessage is one video wire message.
func videoMessage(typ byte, body []byte) []byte {
	b := make([]byte, 5, 5+len(body))
	binary.BigEndian.PutUint32(b, uint32(1+len(body)))
	b[4] = typ
	return append(b, body...)
}

func videoFrameMsg(number uint32, key bool, size int) []byte {
	body := make([]byte, 13+size)
	binary.BigEndian.PutUint32(body, number)
	if key {
		body[12] = videoKeyFlag
	}
	return videoMessage(videoFrame, body)
}

// frames splits a channel's bytes into video messages: (type, frame number, keyframe).
func frames(t *testing.T, b []byte) [][3]int {
	t.Helper()
	var out [][3]int
	for len(b) >= 5 {
		n := int(binary.BigEndian.Uint32(b))
		if len(b) < 4+n {
			t.Fatalf("a partial message: %d of %d", len(b)-4, n)
		}
		m := b[4 : 4+n]
		e := [3]int{int(m[0]), -1, 0}
		if m[0] == videoFrame {
			e[1] = int(binary.BigEndian.Uint32(m[1:]))
			if m[13]&videoKeyFlag != 0 {
				e[2] = 1
			}
		}
		out = append(out, e)
		b = b[4+n:]
	}
	return out
}

// openVideo is a session with a video channel: the phone holds both relays.
func openVideo(t *testing.T, ack bool, limits ...Limits) (*Session, *phone, *carrier, net.Conn, byte, string) {
	l := testLimits
	if len(limits) > 0 {
		l = limits[0]
	}
	reg := NewRegistry(l, nil)
	local, _ := screenSharing(t)
	s, relay := reg.Open("pair1", local)
	t.Cleanup(s.End)
	p := newPhone(t, ack)
	p.carry(relay)
	tok := token(t, p.next(TypePaths))
	hl, helper := screenSharing(t)
	vrelay, ok := s.Video(hl)
	if !ok {
		t.Fatal("video")
	}
	cv := p.carry(vrelay)
	first, _ := offsets(p.next(TypeResumed).body)
	var id byte
	for k, v := range first {
		if v != 0 {
			t.Fatalf("a new video channel starts at 0, not %d", v)
		}
		id = k
	}
	if id != FirstVideo {
		t.Fatalf("the first video channel is %d", id)
	}
	return s, p, cv, helper, id, tok
}

// Moving video with a backlog older than VideoBacklog drops it: the phone is told where the stream goes on, the
// helper is asked for a keyframe, frames before it are dropped, and the keyframe is the next thing that arrives. The
// node moves on its own when the video relay drops (onto the session's other relay), and the phone can move it (use).
func TestAStaleVideoBacklogIsDroppedForAKeyframe(t *testing.T) {
	for _, how := range []string{"the relay drops", "the phone moves it"} {
		t.Run(how, func(t *testing.T) {
			s, p, cv, helper, id, _ := openVideo(t, false)
			clock := useClock(s)
			rfbRelay := p.carriers[0]
			go helper.Write(append(videoFrameMsg(1, true, 100), videoFrameMsg(2, false, 100)...))
			waitFor(t, "two frames", func() bool { return len(frames(t, p.received(id))) == 2 })
			two := p.offset(id)
			go helper.Write(videoFrameMsg(3, false, 100)) // the backlog: on its way, never acknowledged
			waitFor(t, "frame 3 read", func() bool { return len(frames(t, p.received(id))) == 3 })
			clock.Advance(2 * testLimits.VideoBacklog) // the backlog grows old

			keyReq := make(chan []byte, 1)
			go func() { b := make([]byte, 5); io.ReadFull(helper, b); keyReq <- b }()
			if how == "the relay drops" {
				cv.conn.Close()
			} else {
				// The phone says it has the first two frames only: the third was lost on the old route.
				rfbRelay.send(TypeUse, chanOffset(id, two))
				p.next(TypeResumed)
			}
			reset := p.next(TypeReset)
			rid, at, _, _ := readChanOffset(reset.body)
			if rid != id || at <= two || reset.via != rfbRelay {
				t.Fatalf("reset %d at %d (phone had %d)", rid, at, two)
			}
			select {
			case b := <-keyReq:
				if !bytes.Equal(b, keyframeRequest) {
					t.Fatalf("the helper got %v, want a keyframe request", b)
				}
			case <-time.After(time.Second):
				t.Fatal("the helper was not asked for a keyframe")
			}
			// The helper's next frame left before it saw the request: dropped. The keyframe goes.
			before := len(p.received(id))
			go helper.Write(append(videoFrameMsg(4, false, 100), videoFrameMsg(5, true, 100)...))
			waitFor(t, "the keyframe", func() bool { return len(p.received(id)) > before })
			got := frames(t, p.received(id)[before:])
			if len(got) != 1 || got[0][1] != 5 || got[0][2] != 1 {
				t.Fatalf("after the reset: %v, want the keyframe 5 alone", got)
			}
		})
	}
}

// A young video backlog is sent on exactly, like RFB: no reset, no keyframe.
func TestAFreshVideoBacklogIsResent(t *testing.T) {
	limits := testLimits
	limits.VideoBacklog = time.Hour
	s, p, cv, helper, id, tok := openVideo(t, false, limits)
	p.carriers[0].conn.Close() // only the video relay: the node can't move video by itself
	go helper.Write(append(videoFrameMsg(1, true, 100), videoFrameMsg(2, false, 100)...))
	waitFor(t, "two frames", func() bool { return len(frames(t, p.received(id))) == 2 })
	cv.conn.Close()
	again, _ := s.Reattach(true)
	c2 := p.carry(again)
	c2.send(TypeAttach, attachBody(tok))
	p.next(TypeAttached)
	c2.send(TypeUse, chanOffset(id, uint64(len(videoFrameMsg(1, true, 100)))))
	p.next(TypeResumed)
	go helper.Write(videoFrameMsg(3, false, 100))
	waitFor(t, "frame 3", func() bool { return len(frames(t, p.received(id))) == 3 })
	select {
	case m := <-p.msgs:
		t.Fatalf("unexpected message %d", m.typ)
	default:
	}
}

// A new video stream gets a channel of its own: the earlier stream's relay closes, and bytes still on their way for
// it never reach the new helper.
func TestANewVideoStreamGetsItsOwnChannel(t *testing.T) {
	s, p, cv, _, id, _ := openVideo(t, true)
	hl, helper2 := screenSharing(t)
	vrelay, ok := s.Video(hl)
	if !ok {
		t.Fatal("second video")
	}
	select {
	case <-cv.gone:
	case <-time.After(time.Second):
		t.Fatal("the earlier stream's relay stays open")
	}
	c2 := p.carry(vrelay)
	first, _ := offsets(p.next(TypeResumed).body)
	if _, ok := first[id+1]; !ok || len(first) != 1 {
		t.Fatalf("the second stream's channel: %v", first)
	}
	start := videoMessage(16, []byte(`{}`))
	read := make(chan []byte, 1)
	go func() { b := make([]byte, len(start)); io.ReadFull(helper2, b); read <- b }()
	c2.send(TypeData, chanOffset(id, 0), []byte("stale"))
	c2.send(TypeData, chanOffset(id+1, 0), start)
	select {
	case got := <-read:
		if !bytes.Equal(got, start) {
			t.Fatalf("the new helper read %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the new helper read nothing")
	}
}

// The node's keyframe request goes between two of the phone's messages to the helper, never inside one.
func TestTheKeyframeRequestWaitsForAMessageBoundary(t *testing.T) {
	var out bytes.Buffer
	ch := &channel{id: FirstVideo, local: nopCloser{&out}}
	msg := videoMessage(17, make([]byte, 20))
	if err := ch.upstream.write(&out, msg[:7]); err != nil {
		t.Fatal(err)
	}
	ch.upstream.inject(ch, keyframeRequest)
	if out.Len() != 7 {
		t.Fatal("the request went inside the phone's message")
	}
	if err := ch.upstream.write(&out, msg[7:]); err != nil {
		t.Fatal(err)
	}
	if want := append(append([]byte(nil), msg...), keyframeRequest...); !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("wrote %v", out.Bytes())
	}
	ch.upstream.inject(ch, keyframeRequest)
	if out.Len() != len(msg)+2*len(keyframeRequest) {
		t.Fatal("at a boundary the request goes at once")
	}
}

type nopCloser struct{ io.Writer }

func (nopCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (nopCloser) Close() error             { return nil }

func TestFraming(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(frame(TypeData, chanOffset(RFB, 42), []byte("abc")))
	buf.Write(frame(TypeUse, chanOffset(RFB, 7), chanOffset(FirstVideo, 9)))
	r := newReader(&buf)
	typ, body, err := r.next()
	if err != nil || typ != TypeData {
		t.Fatal(typ, err)
	}
	if id, off, data, _ := readChanOffset(body); id != RFB || off != 42 || string(data) != "abc" {
		t.Fatal(id, off, data)
	}
	typ, body, _ = r.next()
	if at, err := offsets(body); typ != TypeUse || err != nil || at[RFB] != 7 || at[FirstVideo] != 9 {
		t.Fatal(at, err)
	}
	var big bytes.Buffer
	_ = binary.Write(&big, binary.BigEndian, uint32(maxFrame+1))
	if _, _, err := newReader(&big).next(); err == nil {
		t.Fatal("an oversized message is read")
	}
	if _, err := offsets(make([]byte, 10)); err == nil {
		t.Fatal("a ragged use is read")
	}
}
