package node

import (
	"encoding/binary"
	"math"
	"net"
	"sync"
)

// Pixel scrolling for the live screen (#7775, docs/extensions/computer-use.md "Pixel scrolling"). A phone that can
// scroll by the point sends its scrolls inside the RFB stream as 16-byte records, client message type 200 (unassigned in
// IANA's RFB client-to-server registry). The node takes them out, so macOS's Screen Sharing never sees them, and hands
// them to Persona Input, which posts them as continuous scroll events; one the helper can't take becomes RFB wheel
// notches here, a notch a Mac point (what Screen Sharing scrolls a notch).
//
// To find them, rfbInputConn follows the phone's side of the stream: the handshake (as followRFBLogin does, but as the
// bytes go by: security types 1, 2 and 30, after which macOS's Screen Sharing speaks plain RFB; with any other type
// nothing is read), then RFB's six client messages, each passed on at once, byte for byte. A message type it doesn't
// know ends the reading for good: everything after it passes as it came. The phone sends records only to a node that
// says it takes them (`screen_input` in its status).

const (
	inputRecordType    = 200
	inputRecordVersion = 1
	inputRecordSize    = 16

	inputKindHello  = 0
	inputKindScroll = 1
)

// inputRecord is one scroll record: kind, scroll phase and momentum phase as CGScrollPhase / CGMomentumScrollPhase,
// dy and dx in Mac points (> 0 shows what is above / to the left).
type inputRecord struct {
	kind, phase, momentum uint8
	dy, dx                float32
}

// errInputRecord names the check a record failed: length, type, version, kind, phase or momentum.
type errInputRecord string

func (e errInputRecord) Error() string { return "screen input record: bad " + string(e) }

func decodeInputRecord(b []byte) (inputRecord, error) {
	switch {
	case len(b) != inputRecordSize:
		return inputRecord{}, errInputRecord("length")
	case b[0] != inputRecordType:
		return inputRecord{}, errInputRecord("type")
	case b[1] != inputRecordVersion:
		return inputRecord{}, errInputRecord("version")
	case b[2] != inputKindHello && b[2] != inputKindScroll:
		return inputRecord{}, errInputRecord("kind")
	}
	switch b[3] {
	case 0, 1, 2, 4, 8:
	default:
		return inputRecord{}, errInputRecord("phase")
	}
	if b[4] > 3 {
		return inputRecord{}, errInputRecord("momentum")
	}
	return inputRecord{kind: b[2], phase: b[3], momentum: b[4],
		dy: math.Float32frombits(binary.BigEndian.Uint32(b[8:12])), dx: math.Float32frombits(binary.BigEndian.Uint32(b[12:16]))}, nil
}

// inputSink is where a stream's records go: the pair's Persona Input session.
type inputSink interface {
	// hello: the phone can scroll by pixels (start the helper, or ask for its permission once).
	hello()
	// scroll hands a record to the helper as it came; false when it can't take it (the stream falls back to notches).
	scroll(raw []byte) bool
	close()
}

// Where rfbInputConn is on Screen Sharing's side of the handshake.
const (
	srvVersion = iota // ProtocolVersion, 12 bytes
	srvCount          // how many security types, 1 byte
	srvTypes          // the types
	srvChoice         // waiting for the phone's choice
	srvDH             // type 30: generator and key length, 4 bytes
	srvDone
)

// And on the phone's side.
const (
	inVersion  = iota // ProtocolVersion, 12 bytes
	inChoice          // the chosen security type, 1 byte
	inAuth            // the type's own bytes
	inInit            // ClientInit, 1 byte
	inMessages        // client messages
	inOff             // not reading: everything passes as it came
)

// inputFallbackMost is the most notches one record may turn into (a frame's points; more is not a scroll).
const inputFallbackMost = 200

// rfbInputConn is a paired 5900 connection read on the phone's side (Write) for scroll records, and on Screen Sharing's
// side (Read) only as far as the handshake needs (type 30's key length). Write passes on what remains of p (the
// records taken out, notches in place of any the helper couldn't take) and reports all of p written.
type rfbInputConn struct {
	net.Conn
	sink inputSink

	mu sync.Mutex
	// Screen Sharing's side.
	srv     int
	srvNeed int
	srvHead []byte
	keyLen  int
	// The phone's side.
	state   int
	need    int    // bytes still to pass in this step or message (-1: type 30's, not known yet)
	chosen  byte   // the security type chosen
	head    []byte // a message's first bytes, until its length is known (passed on all the same)
	record  []byte // a record coming in, never passed on
	x, y    uint16 // where the phone last put the pointer, and its buttons
	buttons byte
	rest    float64 // fallback: a fraction of a notch carried to the next record
	closed  sync.Once
}

func newRFBInputConn(c net.Conn, sink inputSink) *rfbInputConn {
	return &rfbInputConn{Conn: c, sink: sink, srv: srvVersion, srvNeed: 12, state: inVersion, need: 12}
}

func (c *rfbInputConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.mu.Lock()
		c.server(p[:n])
		c.mu.Unlock()
	}
	return n, err
}

// server follows Screen Sharing's handshake until type 30's key length is known, or isn't needed.
func (c *rfbInputConn) server(p []byte) {
	for len(p) > 0 && c.srv != srvDone && c.srv != srvChoice {
		take := min(len(p), c.srvNeed)
		c.srvHead = append(c.srvHead, p[:take]...)
		c.srvNeed -= take
		p = p[take:]
		if c.srvNeed > 0 {
			return
		}
		switch c.srv {
		case srvVersion:
			c.srv, c.srvNeed = srvCount, 1
		case srvCount:
			if count := int(c.srvHead[len(c.srvHead)-1]); count > 0 {
				c.srv, c.srvNeed = srvTypes, count
			} else {
				c.srv = srvDone // refused before any authentication
			}
		case srvTypes:
			c.srv = srvChoice
			c.srvChose() // the phone may have chosen already (not with Screen Sharing, which waits for nothing)
		case srvDH:
			c.keyLen = int(binary.BigEndian.Uint16(c.srvHead[len(c.srvHead)-2:]))
			c.srv = srvDone
		}
		c.srvHead = c.srvHead[:0]
	}
}

// srvChose moves Screen Sharing's side on once both its types are in and the phone has chosen.
func (c *rfbInputConn) srvChose() {
	if c.srv != srvChoice || c.state <= inChoice {
		return
	}
	if c.chosen == rfbSecAppleDH {
		c.srv, c.srvNeed = srvDH, 4
	} else {
		c.srv = srvDone
	}
}

func (c *rfbInputConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	out := c.client(p)
	c.mu.Unlock()
	if len(out) > 0 {
		if _, err := c.Conn.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// client is what of p goes on to Screen Sharing: p as it came, less the records, with notches for any the helper
// couldn't take.
func (c *rfbInputConn) client(p []byte) []byte {
	if c.state == inOff {
		return p
	}
	out := make([]byte, 0, len(p))
	for len(p) > 0 {
		if c.state == inOff {
			return append(out, p...)
		}
		if c.state == inMessages {
			var forward []byte
			forward, p = c.message(p)
			out = append(out, forward...)
			continue
		}
		if c.need < 0 { // type 30's own bytes, at the first of them: Screen Sharing's key length came before
			if c.keyLen == 0 {
				c.state = inOff
				continue
			}
			c.need = 128 + c.keyLen
		}
		take := min(len(p), c.need)
		if c.state == inChoice {
			c.chosen = p[0]
		}
		out = append(out, p[:take]...)
		c.need -= take
		p = p[take:]
		if c.need == 0 {
			c.nextHandshakeStep()
		}
	}
	return out
}

func (c *rfbInputConn) nextHandshakeStep() {
	switch c.state {
	case inVersion:
		c.state, c.need = inChoice, 1
	case inChoice:
		switch c.chosen {
		case rfbSecNone:
			c.state, c.need = inInit, 1
		case rfbSecVNCAuth:
			c.state, c.need = inAuth, 16
		case rfbSecAppleDH:
			c.state, c.need = inAuth, -1 // 128 bytes and the key: its length comes in Screen Sharing's next bytes
		default:
			c.state = inOff // a type whose session may not be plain RFB: nothing is read
		}
		c.srvChose()
	case inAuth:
		c.state, c.need = inInit, 1
	case inInit:
		c.state, c.need = inMessages, 0
	}
}

// headLen is how many first bytes of a client message tell its length; 0 for a type this doesn't read.
func headLen(typ byte) int {
	switch typ {
	case 0: // SetPixelFormat
		return 20
	case 2: // SetEncodings: then 4 bytes an encoding
		return 4
	case 3: // FramebufferUpdateRequest
		return 10
	case 4: // KeyEvent
		return 8
	case 5: // PointerEvent
		return 6
	case 6: // ClientCutText: then its text
		return 8
	}
	return 0
}

// message takes what it can of one client message from p: the bytes to pass on, and what is left of p.
func (c *rfbInputConn) message(p []byte) ([]byte, []byte) {
	if c.record != nil { // a record coming in
		take := min(len(p), inputRecordSize-len(c.record))
		c.record = append(c.record, p[:take]...)
		p = p[take:]
		if len(c.record) < inputRecordSize {
			return nil, p
		}
		raw := c.record
		c.record = nil
		return c.deliver(raw), p
	}
	if c.need > 0 { // the rest of a message whose length is known
		take := min(len(p), c.need)
		c.need -= take
		return p[:take], p[take:]
	}
	if len(c.head) == 0 && p[0] == inputRecordType {
		c.record = make([]byte, 0, inputRecordSize)
		return nil, p
	}
	typ := p[0]
	if len(c.head) > 0 {
		typ = c.head[0]
	}
	want := headLen(typ)
	if want == 0 {
		c.state = inOff // a message this doesn't know: its length isn't known either, so nothing more is read
		return p, nil
	}
	take := min(len(p), want-len(c.head))
	c.head = append(c.head, p[:take]...)
	forward, rest := p[:take], p[take:]
	if len(c.head) < want {
		return forward, rest
	}
	switch typ {
	case 2:
		c.need = 4 * int(binary.BigEndian.Uint16(c.head[2:4]))
	case 5: // where the pointer is, for fallback notches
		c.buttons = c.head[1]
		c.x, c.y = binary.BigEndian.Uint16(c.head[2:4]), binary.BigEndian.Uint16(c.head[4:6])
	case 6:
		c.need = int(binary.BigEndian.Uint32(c.head[4:8]))
	}
	c.head = c.head[:0]
	return forward, rest
}

// deliver hands a complete record on: a hello or a scroll to the session, or, for a scroll it can't take, what Screen
// Sharing gets instead: wheel notches. A record that doesn't decode is dropped, never passed to Screen Sharing.
func (c *rfbInputConn) deliver(raw []byte) []byte {
	r, err := decodeInputRecord(raw)
	if err != nil {
		return nil
	}
	if r.kind == inputKindHello {
		c.sink.hello()
		return nil
	}
	if r.phase == 1 || r.momentum == 1 {
		c.rest = 0 // a new gesture or coast starts clean
	}
	if c.sink.scroll(raw) {
		return nil
	}
	return c.notches(float64(r.dy))
}

// notches is dy Mac points as RFB wheel notches at the pointer (button 4 up, 5 down, each pressed and let go, with the
// phone's own buttons as they were), the fraction carried to the next record.
func (c *rfbInputConn) notches(dy float64) []byte {
	whole := math.Trunc(dy + c.rest)
	c.rest = dy + c.rest - whole
	n := int(math.Min(math.Abs(whole), inputFallbackMost))
	bit := byte(8)
	if whole < 0 {
		bit = 16
	}
	out := make([]byte, 0, n*12)
	for range n {
		for _, b := range []byte{c.buttons | bit, c.buttons &^ bit} {
			out = append(out, 5, b, byte(c.x>>8), byte(c.x), byte(c.y>>8), byte(c.y))
		}
	}
	return out
}

func (c *rfbInputConn) Close() error {
	c.closed.Do(c.sink.close)
	return c.Conn.Close()
}
