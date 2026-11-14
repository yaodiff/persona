package screenlink

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// The video over UDP's threat model (#7873), one test per threat:
//
//   - Replay: a packet captured on the way and sent again is dropped (TestUDPDropsWhatDoesNotOpen, TestTheReplayWindow).
//   - Tampering: a packet changed on the way, header or body, doesn't open (TestUDPDropsWhatDoesNotOpen).
//   - A wrong key: a packet sealed with any key but the flow's own direction's doesn't open, including the node's own
//     packets reflected back and keys exported from another connection (TestUDPKeysAreBoundToTheirConnectionAndDirection).
//   - Unauthenticated packets get no answer at all (TestUDPDropsWhatDoesNotOpen, TestNothingButAHelloBeforeTheProof).
//   - Amplification: nothing is sent to an address the phone hasn't proved, and the one answer before the proof (the
//     hello's challenge) is smaller than the hello (TestUDPNeverAmplifies).
//   - Nonces: each direction has its own key and IV and numbers its packets from 0 without repeating; a flow that ran
//     out of numbers is closed rather than reuse one (TestUDPNoncesNeverRepeat).

// silence fails if a packet arrives on c within d.
func silence(t *testing.T, c *net.UDPConn, d time.Duration, what string) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(d))
	if n, err := c.Read(make([]byte, 2048)); err == nil {
		t.Fatalf("%s: answered with %d bytes", what, n)
	}
	_ = c.SetReadDeadline(time.Time{})
}

// noPong fails if a pong reaches the phone within d.
func (u *udpPhone) noPong(d time.Duration, what string) {
	u.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case b := <-u.ctl:
			if b[0] == udpPong {
				u.t.Fatalf("%s: answered", what)
			}
		case <-deadline:
			return
		}
	}
}

func pingBody() []byte {
	b := make([]byte, 15)
	b[0] = udpPing
	binary.BigEndian.PutUint64(b[1:], 9)
	return b
}

func attacker(t *testing.T, to *net.UDPAddr) *net.UDPConn {
	t.Helper()
	c, err := net.DialUDP("udp", nil, to)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestTheReplayWindow(t *testing.T) {
	var w replayWindow
	for _, c := range []struct {
		pn   uint64
		want bool
	}{
		{0, true}, {0, false}, {2, true}, {1, true}, {1, false}, {2, false},
		{5000, true}, {5000 - replaySpan, false}, {5000 - replaySpan + 1, true}, {4999, true}, {4999, false},
		{1 << 40, true}, {5000, false}, {(1 << 40) - 1, true}, {1<<64 - 1, false},
	} {
		if got := w.accept(c.pn); got != c.want {
			t.Fatalf("packet %d: accepted %v, want %v", c.pn, got, c.want)
		}
	}
}

func TestUDPNoncesNeverRepeat(t *testing.T) {
	material := make([]byte, udpKeyMaterial)
	_, _ = rand.Read(material)
	keys, err := newUDPKeys(material)
	if err != nil {
		t.Fatal(err)
	}
	if keys.ivPhone == keys.ivNode || bytes.Equal(material[:32], material[44:76]) {
		t.Fatal("the two directions share a key or an IV")
	}
	var flow [8]byte
	toPhone := &udpSealer{aead: keys.toPhone, iv: keys.ivPhone, flow: flow}
	toNode := &udpSealer{aead: keys.toNode, iv: keys.ivNode, flow: flow}
	seen := map[[13]byte]bool{} // direction, nonce
	for i := 0; i < 5000; i++ {
		for d, s := range []*udpSealer{toPhone, toNode} {
			p, ok := s.seal([]byte{udpPing})
			if !ok {
				t.Fatal("ran out of packet numbers")
			}
			_, pn, _ := udpHeader(p)
			if pn != uint64(i) {
				t.Fatalf("packet %d numbered %d", i, pn)
			}
			var n [13]byte
			n[0] = byte(d)
			copy(n[1:], udpNonce(s.iv, pn))
			if seen[n] {
				t.Fatalf("a nonce repeated at packet %d", i)
			}
			seen[n] = true
		}
	}
	// The same body and number seal differently in the two directions.
	a := sealUDP(keys.toPhone, keys.ivPhone, flow, 7, []byte("same"))
	b := sealUDP(keys.toNode, keys.ivNode, flow, 7, []byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("both directions sealed alike")
	}
	// A sealer that reaches the limit stops instead of wrapping around.
	toPhone.next = udpPacketLimit - 1
	if _, ok := toPhone.seal([]byte{udpPing}); !ok {
		t.Fatal("the last packet number was refused")
	}
	if _, ok := toPhone.seal([]byte{udpPing}); ok {
		t.Fatal("sealed past the limit")
	}
}

func TestUDPKeysAreBoundToTheirConnectionAndDirection(t *testing.T) {
	l := openUDPLink(t, udpLimits())
	l.up.prove()

	// The node's own packet, reflected back to it, doesn't open: its direction has another key. (Sealed as the node
	// seals for this flow: the same flow id, the node's key and its next packet number.)
	nodePacket, _ := l.flow().out.seal(pingBody())
	_, _ = l.up.conn.Write(nodePacket)
	l.up.noPong(300*time.Millisecond, "a reflected node packet")

	// Keys exported with the same flow id from another TLS connection of the same session don't open the flow.
	dc2, err := dialDirect(t, l.paths, []string{ALPN})
	if err != nil {
		t.Fatal(err)
	}
	c2 := l.p.carry(dc2)
	c2.send(TypeAttach, attachBody(l.paths.Token))
	l.p.next(TypeAttached)
	cs := dc2.ConnectionState()
	other, err := cs.ExportKeyingMaterial(UDPExporterLabel, l.up.flow[:], udpKeyMaterial)
	if err != nil {
		t.Fatal(err)
	}
	ours := l.dc.ConnectionState()
	mine, _ := ours.ExportKeyingMaterial(UDPExporterLabel, l.up.flow[:], udpKeyMaterial)
	if bytes.Equal(other, mine) {
		t.Fatal("two connections exported the same keys")
	}
	otherKeys, _ := newUDPKeys(other)
	forged := &udpSealer{aead: otherKeys.toNode, iv: otherKeys.ivNode, flow: l.up.flow, next: 1 << 20}
	p, _ := forged.seal(pingBody())
	_, _ = l.up.conn.Write(p)
	l.up.noPong(300*time.Millisecond, "keys from another connection")

	// The flow still works with its own keys.
	l.up.ping(0)
}

// flow is the node's flow for the link's phone.
func (l *udpLink) flow() *udpFlow {
	l.s.direct.mu.Lock()
	defer l.s.direct.mu.Unlock()
	return l.s.direct.flows[l.up.flow]
}

func TestUDPDropsWhatDoesNotOpen(t *testing.T) {
	l := openUDPLink(t, udpLimits())
	l.up.prove()

	// Replay: the same ping twice is answered once.
	p := l.up.send(pingBody())
	l.up.next(udpPong)
	_, _ = l.up.conn.Write(p)
	l.up.noPong(300*time.Millisecond, "a replayed ping")

	// Tampering: a changed header (the packet number, the flow id), body or tag doesn't open.
	for _, at := range []int{0, 3, 12, udpHeaderLen + 2, -1} {
		q, _ := l.up.out.seal(pingBody())
		i := at
		if at < 0 {
			i = len(q) - 1
		}
		q[i] ^= 0x40
		_, _ = l.up.conn.Write(q)
		l.up.noPong(150*time.Millisecond, "a tampered packet")
	}

	// A wrong key under the flow's id.
	material := make([]byte, udpKeyMaterial)
	_, _ = rand.Read(material)
	wrong, _ := newUDPKeys(material)
	w := &udpSealer{aead: wrong.toNode, iv: wrong.ivNode, flow: l.up.flow, next: 1 << 30}
	q, _ := w.seal(pingBody())
	_, _ = l.up.conn.Write(q)
	l.up.noPong(300*time.Millisecond, "a wrong key")

	// Unauthenticated packets from anywhere get nothing back: garbage, another version, too short, an unknown flow.
	a := attacker(t, l.nodeAt)
	junk := make([]byte, 120)
	_, _ = rand.Read(junk)
	unknown := sealUDP(wrong.toNode, wrong.ivNode, [8]byte{1, 2, 3}, 0, pingBody())
	other := append([]byte(nil), unknown...)
	other[0] = 2
	for _, pkt := range [][]byte{junk, unknown, other, {udpVersion, 1, 2}, {}, make([]byte, 1500)} {
		_, _ = a.Write(pkt)
	}
	silence(t, a, 300*time.Millisecond, "unauthenticated packets")

	// None of it hurt the flow.
	l.up.ping(0)
}

// Before the phone proves its address, nothing but a hello is answered: no pong, no resumed, no video.
func TestNothingButAHelloBeforeTheProof(t *testing.T) {
	l := openUDPLink(t, udpLimits())
	l.up.send(pingBody())
	l.up.send(append([]byte{udpUse}, chanOffset(l.id, 0)...))
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case b := <-l.up.ctl:
			t.Fatalf("answered %d before the proof", b[0])
		case <-deadline:
			return
		}
	}
}

// Nothing goes to an address the phone hasn't proved, and the one answer before the proof is smaller than what came.
// A hello captured from the phone and replayed from elsewhere gets nothing; a short hello gets nothing; a fresh hello
// from a new address (the phone after a Wi-Fi change) gets a challenge only, while the video keeps going to the proved
// address; once the new address answers the challenge, the video follows it.
func TestUDPNeverAmplifies(t *testing.T) {
	l := openUDPLink(t, udpLimits())
	l.up.prove()
	l.up.use(l.id, 0)

	hello := func(echo uint64, size int) []byte {
		b := make([]byte, size)
		b[0] = udpHello
		binary.BigEndian.PutUint64(b[1:], echo)
		p, _ := l.up.out.seal(b)
		return p
	}
	b := attacker(t, l.nodeAt)

	// A replayed hello from elsewhere.
	captured := hello(0, 1+udpHelloMin)
	_, _ = l.up.conn.Write(captured)
	l.up.next(udpHello)
	_, _ = b.Write(captured)
	silence(t, b, 300*time.Millisecond, "a replayed hello")

	// A hello too short to answer without amplifying.
	_, _ = b.Write(hello(0, 9))
	silence(t, b, 300*time.Millisecond, "a short hello")

	// A fresh hello from the new address: a challenge, no larger than the hello.
	req := hello(0, 1+udpHelloMin)
	_, _ = b.Write(req)
	_ = b.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 2048)
	n, err := b.Read(buf)
	if err != nil {
		t.Fatal("no challenge")
	}
	if n > len(req) {
		t.Fatalf("answered %d bytes to a %d-byte hello", n, len(req))
	}
	_, pn, _ := udpHeader(buf[:n])
	body, err := openUDP(l.up.keys.toPhone, l.up.keys.ivPhone, buf[:n], pn)
	if err != nil || body[0] != udpHello || body[9] != 0 {
		t.Fatalf("the answer %v", body)
	}
	challenge := binary.BigEndian.Uint64(body[1:])

	// A ping from the new address before it proved itself gets nothing there, and the video stays where it was.
	pp, _ := l.up.out.seal(pingBody())
	_, _ = b.Write(pp)
	writeAll(l.helper, videoStream(6))
	waitFor(t, "video at the proved address", func() bool { return len(l.up.delivered()) == len(join(videoStream(6))) })
	silence(t, b, 300*time.Millisecond, "an address not proved yet")

	// The new address answers the challenge: it is the phone's now.
	_, _ = b.Write(hello(challenge, 1+udpHelloMin))
	_ = b.SetReadDeadline(time.Now().Add(time.Second))
	n, err = b.Read(buf)
	if err != nil {
		t.Fatal("no confirmation")
	}
	_, pn, _ = udpHeader(buf[:n])
	if body, err := openUDP(l.up.keys.toPhone, l.up.keys.ivPhone, buf[:n], pn); err != nil || body[9] != 1 {
		t.Fatalf("the confirmation %v", body)
	}
	writeAll(l.helper, [][]byte{videoFrameMsg(99, false, 500)})
	_ = b.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := b.Read(buf); err != nil {
		t.Fatal("the video didn't follow the proved address")
	}
}
