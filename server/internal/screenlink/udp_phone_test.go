package screenlink

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

// udpPhone is the phone's end of a UDP flow as the app keeps it (#7873): it proves its address, moves the video channel
// onto the flow, rebuilds each message from its fragments and parity, hands messages on in order, acknowledges them,
// asks again for what is missing, and can give a frame up.
type udpPhone struct {
	t    *testing.T
	conn *net.UDPConn
	keys *udpKeys
	out  *udpSealer
	flow [8]byte

	mu        sync.Mutex
	recv      replayWindow
	ch        byte
	expected  uint64
	msgs      map[uint64]*rxMessage
	got       []byte
	drop      func(udpFragmentHead) bool // simulated loss of fragments, by what they are
	nacks     int
	rebuilt   int
	ctl       chan []byte
	ratio     int
	autoAsk   bool
	lastHeard time.Time
	stop      chan struct{}
}

type rxMessage struct {
	length int
	blocks map[uint16]*rxBlock
	lastAt time.Time
	asked  map[[2]uint16]time.Time
}

type rxBlock struct {
	data, parity int
	shards       map[int][]byte
	done         [][]byte // the data shards, once whole
}

// udpLink is a phone's flow set up end to end: a session with a video channel, a direct route attached, a UDP flow
// opened on it and the phone's address proved.
type udpLink struct {
	s      *Session
	p      *phone // the phone's relays and direct route
	direct *carrier
	dc     *tls.Conn
	helper net.Conn
	ss     net.Conn
	id     byte
	paths  Paths
	ready  UDPReady
	up     *udpPhone
	nodeAt *net.UDPAddr
}

func openUDPLink(t *testing.T, limits Limits) *udpLink {
	t.Helper()
	r := newDirectRig(t)
	r.reg = NewRegistry(limits, t.Logf)
	r.reg.EnableDirect(r.d)
	local, ss := screenSharing(t)
	s, relay := r.reg.Open("pair1", local)
	t.Cleanup(s.End)
	p := newPhone(t, true)
	p.carry(relay)
	paths := readPaths(t, p.next(TypePaths))
	if paths.UDP != 1 {
		t.Fatalf("paths offer no udp: %+v", paths)
	}
	hl, helper := screenSharing(t)
	vrelay, ok := s.Video(hl)
	if !ok {
		t.Fatal("video")
	}
	p.carry(vrelay)
	first, _ := offsets(p.next(TypeResumed).body)
	var id byte
	for k := range first {
		id = k
	}
	dc, err := dialDirect(t, paths, []string{ALPN})
	if err != nil {
		t.Fatal(err)
	}
	direct := p.carry(dc)
	direct.send(TypeAttach, attachBody(paths.Token))
	p.next(TypeAttached)
	direct.send(TypeUDPOpen)
	var ready UDPReady
	if err := json.Unmarshal(p.next(TypeUDPReady).body, &ready); err != nil || ready.Port == 0 || ready.Flow == "" {
		t.Fatalf("udp_ready %+v %v", ready, err)
	}
	nodeAt, _ := net.ResolveUDPAddr("udp", net.JoinHostPort(paths.Candidates[0].Addr, strconv.Itoa(ready.Port)))
	l := &udpLink{s: s, p: p, direct: direct, dc: dc, helper: helper, ss: ss, id: id, paths: paths, ready: ready, nodeAt: nodeAt}
	l.up = newUDPPhone(t, dc, ready, nodeAt)
	l.up.ch = id
	return l
}

func newUDPPhone(t *testing.T, dc *tls.Conn, ready UDPReady, to *net.UDPAddr) *udpPhone {
	t.Helper()
	var flow [8]byte
	raw, err := base64.RawURLEncoding.DecodeString(ready.Flow)
	if err != nil || len(raw) != 8 {
		t.Fatalf("flow id %q", ready.Flow)
	}
	copy(flow[:], raw)
	cs := dc.ConnectionState()
	material, err := cs.ExportKeyingMaterial(UDPExporterLabel, flow[:], udpKeyMaterial)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := newUDPKeys(material)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialUDP("udp", nil, to)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadBuffer(4 << 20)
	u := &udpPhone{t: t, conn: conn, keys: keys, flow: flow, out: &udpSealer{aead: keys.toNode, iv: keys.ivNode, flow: flow},
		msgs: map[uint64]*rxMessage{}, ctl: make(chan []byte, 256), autoAsk: true, stop: make(chan struct{})}
	t.Cleanup(func() { close(u.stop); conn.Close() })
	go u.read()
	go u.tick()
	return u
}

// send seals body and sends it to the node.
func (u *udpPhone) send(body []byte) []byte {
	p, _ := u.out.seal(body)
	_, _ = u.conn.Write(p)
	return p
}

// next is the next control body of kind (anything else of that time is skipped), failing after two seconds.
func (u *udpPhone) next(kind byte) []byte {
	u.t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case b := <-u.ctl:
			if b[0] == kind {
				return b
			}
		case <-deadline:
			u.t.Fatalf("no udp message %d", kind)
			return nil
		}
	}
}

// prove has the phone prove its address: hello, the node's challenge, hello with it, confirmed.
func (u *udpPhone) prove() {
	u.t.Helper()
	hello := func(echo uint64) {
		b := make([]byte, 1+udpHelloMin)
		b[0] = udpHello
		binary.BigEndian.PutUint64(b[1:], echo)
		u.send(b)
	}
	hello(0)
	c := u.next(udpHello)
	if c[9] != 0 || binary.BigEndian.Uint64(c[1:]) == 0 {
		u.t.Fatalf("the first hello was not challenged: %v", c)
	}
	hello(binary.BigEndian.Uint64(c[1:]))
	if ok := u.next(udpHello); ok[9] != 1 {
		u.t.Fatalf("the proof was not confirmed: %v", ok)
	}
}

// use moves the channel onto the flow from off (repeated until the node answers, as the app does).
func (u *udpPhone) use(ch byte, off uint64) []byte {
	u.t.Helper()
	u.mu.Lock()
	if ch == u.ch {
		u.expected = off
	}
	u.mu.Unlock()
	for i := 0; i < 20; i++ {
		u.send(append([]byte{udpUse}, chanOffset(ch, off)...))
		select {
		case b := <-u.ctl:
			if b[0] == udpResumed {
				return b
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	u.t.Fatal("no resumed")
	return nil
}

// ping asks for parity at ratio (ten-thousandths) and returns the pong's counters: data, parity, resent bytes.
func (u *udpPhone) ping(ratio int) (uint64, uint64, uint64) {
	u.t.Helper()
	b := make([]byte, 15)
	b[0] = udpPing
	binary.BigEndian.PutUint64(b[1:], 7)
	binary.BigEndian.PutUint16(b[9:], uint16(ratio))
	u.send(b)
	p := u.next(udpPong)
	if len(p) != 33 || binary.BigEndian.Uint64(p[1:]) != 7 {
		u.t.Fatalf("pong %v", p)
	}
	return binary.BigEndian.Uint64(p[9:]), binary.BigEndian.Uint64(p[17:]), binary.BigEndian.Uint64(p[25:])
}

func (u *udpPhone) delivered() []byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]byte(nil), u.got...)
}

func (u *udpPhone) read() {
	buf := make([]byte, 2048)
	for {
		n, err := u.conn.Read(buf)
		if err != nil {
			return
		}
		_, pn, ok := udpHeader(buf[:n])
		if !ok {
			continue
		}
		body, err := openUDP(u.keys.toPhone, u.keys.ivPhone, buf[:n], pn)
		if err != nil {
			u.t.Errorf("a packet from the node didn't open")
			continue
		}
		u.mu.Lock()
		fresh := u.recv.accept(pn)
		u.lastHeard = time.Now()
		u.mu.Unlock()
		if !fresh {
			u.t.Errorf("the node sent packet %d twice", pn)
			continue
		}
		switch body[0] {
		case udpData:
			h, frag, err := decodeFragment(body)
			if err != nil {
				u.t.Errorf("a bad fragment")
				continue
			}
			u.fragment(h, append([]byte(nil), frag...))
		case udpReset:
			off := binary.BigEndian.Uint64(body[2:])
			u.mu.Lock()
			if off >= u.expected {
				u.expected = off
				for o := range u.msgs {
					if o < off {
						delete(u.msgs, o)
					}
				}
			}
			u.mu.Unlock()
			u.ctl <- body
			u.deliver()
		default:
			u.ctl <- body
		}
	}
}

func (u *udpPhone) fragment(h udpFragmentHead, frag []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.drop != nil && u.drop(h) {
		return
	}
	if h.Offset < u.expected {
		// Had already (a probe, or a copy asked for twice): say so again, the earlier ack may have been lost.
		ack := chanOffsetBody(udpAck, u.ch, u.expected)
		go u.send(ack)
		return
	}
	m := u.msgs[h.Offset]
	if m == nil {
		m = &rxMessage{length: int(h.Length), blocks: map[uint16]*rxBlock{}, asked: map[[2]uint16]time.Time{}}
		u.msgs[h.Offset] = m
	}
	m.lastAt = time.Now()
	b := m.blocks[h.Block]
	if b == nil {
		b = &rxBlock{data: int(h.Data), shards: map[int][]byte{}}
		m.blocks[h.Block] = b
	}
	b.parity = max(b.parity, int(h.Parity))
	if b.done != nil {
		return
	}
	b.shards[int(h.Index)] = frag
	if len(b.shards) < b.data {
		return
	}
	// Whole: rebuild what is missing from parity.
	layout := udpBlocks(m.length)[h.Block]
	shards := make([][]byte, b.data+b.parity)
	missing := 0
	for i := range shards {
		s, ok := b.shards[i]
		if !ok {
			if i < b.data {
				missing++
			}
			continue
		}
		if len(s) < layout.ShardLen {
			s = append(append([]byte{}, s...), make([]byte, layout.ShardLen-len(s))...)
		}
		shards[i] = s
	}
	if err := rsReconstruct(shards, b.data); err != nil {
		u.t.Errorf("block %d of the message at %d didn't rebuild: %v", h.Block, h.Offset, err)
		return
	}
	u.rebuilt += missing
	b.done = shards[:b.data]
	u.deliverLocked()
}

func (u *udpPhone) deliver() {
	u.mu.Lock()
	u.deliverLocked()
	u.mu.Unlock()
}

// deliverLocked hands on every whole message that is next in line, then acknowledges.
func (u *udpPhone) deliverLocked() {
	moved := false
	for {
		m := u.msgs[u.expected]
		if m == nil {
			break
		}
		layout := udpBlocks(m.length)
		var msg []byte
		for bi := range layout {
			b := m.blocks[uint16(bi)]
			if b == nil || b.done == nil {
				return
			}
			for _, s := range b.done {
				msg = append(msg, s...)
			}
		}
		msg = msg[:m.length]
		u.got = append(u.got, msg...)
		delete(u.msgs, u.expected)
		u.expected += uint64(m.length)
		moved = true
	}
	if moved {
		ack := chanOffsetBody(udpAck, u.ch, u.expected)
		go u.send(ack)
	}
}

// skip gives the message the decoder waits for up.
func (u *udpPhone) skip() uint64 {
	u.mu.Lock()
	off := u.expected
	u.mu.Unlock()
	u.send(chanOffsetBody(udpSkip, u.ch, off))
	return off
}

// tick asks again for what is missing: data shards a block still lacks once its message went quiet, and whole
// messages a later one shows were sent.
func (u *udpPhone) tick() {
	t := time.NewTicker(5 * time.Millisecond)
	defer t.Stop()
	const quiet, again = 10 * time.Millisecond, 40 * time.Millisecond
	var wantAt time.Time
	for {
		select {
		case <-u.stop:
			return
		case <-t.C:
		}
		u.mu.Lock()
		if !u.autoAsk {
			u.mu.Unlock()
			continue
		}
		now := time.Now()
		var nack []byte
		var want []byte
		var offs []uint64
		for off := range u.msgs {
			offs = append(offs, off)
		}
		sort.Slice(offs, func(i, j int) bool { return offs[i] < offs[j] })
		if len(offs) > 0 && offs[0] > u.expected && now.Sub(wantAt) > again {
			wantAt = now
			want = make([]byte, 18)
			want[0], want[1] = udpWant, u.ch
			binary.BigEndian.PutUint64(want[2:], u.expected)
			binary.BigEndian.PutUint64(want[10:], offs[0])
		}
		for i, off := range offs {
			m := u.msgs[off]
			if i == len(offs)-1 && now.Sub(m.lastAt) < quiet {
				continue // the newest message may still be on its way
			}
			for bi, l := range udpBlocks(m.length) {
				b := m.blocks[uint16(bi)]
				if b != nil && b.done != nil {
					continue
				}
				have, need := 0, l.Data
				if b != nil {
					have = len(b.shards)
				}
				for j := 0; j < l.Data && have < need; j++ {
					if b != nil {
						if _, ok := b.shards[j]; ok {
							continue
						}
					}
					key := [2]uint16{uint16(bi), uint16(j)}
					if at, ok := m.asked[key]; !ok || now.Sub(at) > again {
						m.asked[key] = now
						if nack == nil {
							nack = []byte{udpNack, u.ch}
						}
						nack = binary.BigEndian.AppendUint64(nack, off)
						nack = binary.BigEndian.AppendUint16(nack, uint16(bi))
						nack = binary.BigEndian.AppendUint16(nack, uint16(j))
						u.nacks++
					}
					have++
				}
			}
		}
		u.mu.Unlock()
		if want != nil {
			u.send(want)
		}
		for len(nack) > 2 {
			n := min(len(nack), 2+12*80)
			u.send(nack[:n])
			nack = append([]byte{udpNack, u.ch}, nack[n:]...)
		}
	}
}

// lossyRelay forwards UDP between a phone and addr, dropping the node's packets that drop picks (by their order).
func lossyRelay(t *testing.T, addr *net.UDPAddr, drop func(n int) bool) *net.UDPAddr {
	t.Helper()
	front, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	back, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = front.SetReadBuffer(4 << 20)
	_ = back.SetReadBuffer(4 << 20)
	t.Cleanup(func() { front.Close(); back.Close() })
	var mu sync.Mutex
	var phone *net.UDPAddr
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := front.ReadFromUDP(buf)
			if err != nil {
				return
			}
			mu.Lock()
			phone = from
			mu.Unlock()
			_, _ = back.Write(buf[:n])
		}
	}()
	go func() {
		buf := make([]byte, 2048)
		for i := 0; ; i++ {
			n, err := back.Read(buf)
			if err != nil {
				return
			}
			mu.Lock()
			to := phone
			mu.Unlock()
			if to != nil && !drop(i) {
				_, _ = front.WriteToUDP(buf[:n], to)
			}
		}
	}()
	return front.LocalAddr().(*net.UDPAddr)
}

// videoStream is n video messages: a keyframe, then frames of varied sizes, a format message every 10.
func videoStream(n int) [][]byte {
	var out [][]byte
	for i := 0; i < n; i++ {
		if i%10 == 5 {
			out = append(out, videoMessage(videoFormat, pattern(40+i)))
			continue
		}
		size := []int{100, 1500, 4000, 12000, 30000, 600}[i%6]
		out = append(out, videoFrameMsg(uint32(i), i == 0, size))
	}
	return out
}

func join(msgs [][]byte) []byte {
	var b []byte
	for _, m := range msgs {
		b = append(b, m...)
	}
	return b
}

// writeAll writes the messages to the helper's end in the background.
func writeAll(helper net.Conn, msgs [][]byte) {
	go func() {
		for _, m := range msgs {
			if _, err := helper.Write(m); err != nil {
				return
			}
		}
	}()
}
