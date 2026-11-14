// Package screenlink carries one live screen over several routes at once: the node's end of a screen session (#6398,
// docs/extensions/computer-use.md "Live screen routes (#6398)"). The node's connections to Screen Sharing (5900) and
// to Persona Screen (the video helper) become channels of one session, keyed by the RFB stream's pair. Each channel is
// a resumable byte stream: bytes carry their offset, the receiver acknowledges them, and the sender keeps what is not
// acknowledged yet, so any of the session's transports (the hub's relays, later a direct connection) can carry a
// channel on from where the other side is. The node never parses RFB; it reads the video channel's message lengths
// only, to drop a stale backlog whole and start again at a keyframe.
//
// A transport only ever carries a session the hub opened: the hub dials Screen Sharing and starts the helper as
// before, and a transport that joins later proves with the session's token that it belongs to it.
package screenlink

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Subprotocol is the WebSocket subprotocol a phone offers on …/ports/5900/connect and …/screen/video/connect to ask
// for a screen session. The hub selects it only when the node answered port.dial / screen.dial with {link: 1}; every
// other client, hub or node keeps the raw stream.
const Subprotocol = "persona-screen-link.1"

// Message types. Every transport carries the same framing as the video wire: u32 big-endian length (type + body),
// u8 type, body.
const (
	TypeData     byte = 1  // u8 channel, u64 offset, bytes
	TypeAck      byte = 2  // u8 channel, u64 received through
	TypePaths    byte = 3  // JSON Paths
	TypeAttach   byte = 4  // JSON {"token"}: the first message of a transport joining a live session
	TypeAttached byte = 5  // empty: the token was right
	TypePing     byte = 6  // u64, the phone's clock
	TypePong     byte = 7  // the ping's u64, u32 TCP packets retransmitted and u32 sent since the last pong (0, 0: unknown)
	TypeUse      byte = 8  // n × (u8 channel, u64 received through): send these channels on this transport from there
	TypeResumed  byte = 9  // the same from the node: the phone resends its bytes from there
	TypeClose    byte = 10 // u8 channel, u64 its end: no byte after it
	TypeReset    byte = 11 // u8 channel, u64 offset: the video channel goes on at this message boundary; drop a partial message
)

// Channels of a session: RFB is Screen Sharing's stream, resumed byte for byte and never dropped. Each video stream
// (Persona Screen's) is a channel of its own from FirstVideo up; a stale backlog of it is dropped for a keyframe on a
// move.
const (
	RFB        byte = 1
	FirstVideo byte = 2
)

// maxFrame bounds one link message: a data message carries at most dataChunk bytes, and nothing else is large.
const maxFrame = 1 << 20

// dataChunk is the most one data message carries, so a small RFB message never waits behind a long run of video.
const dataChunk = 16 << 10

// Paths is what the node tells the phone about the session's routes (TypePaths): the token a transport attaches
// with, and, from the direct routes on (#6398 stage 3), the candidates and the certificate to pin.
type Paths struct {
	Version     int         `json:"version"`
	Token       string      `json:"token"`
	Cert        string      `json:"cert,omitempty"`
	Port        int         `json:"port,omitempty"`
	Candidates  []Candidate `json:"candidates"`
	Unavailable string      `json:"unavailable,omitempty"`
}

// Candidate is one address a direct route may reach the node at.
type Candidate struct {
	Kind   string `json:"kind"` // lan | tailscale
	Addr   string `json:"addr"`
	Prefix int    `json:"prefix,omitempty"`
}

// errFrame is a malformed or oversized message: the transport carrying it is closed.
var errFrame = errors.New("screenlink: bad message")

// frame is one message ready to write: the length prefix, the type and the body, in one buffer so a single Write
// carries it.
func frame(typ byte, parts ...[]byte) []byte {
	n := 1
	for _, p := range parts {
		n += len(p)
	}
	b := make([]byte, 4, 4+n)
	binary.BigEndian.PutUint32(b, uint32(n))
	b = append(b, typ)
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

// chanOffset is the 9-byte head of data, ack, close and reset bodies, and each entry of use and resumed.
func chanOffset(ch byte, off uint64) []byte {
	b := make([]byte, 9)
	b[0] = ch
	binary.BigEndian.PutUint64(b[1:], off)
	return b
}

func readChanOffset(body []byte) (byte, uint64, []byte, error) {
	if len(body) < 9 {
		return 0, 0, nil, errFrame
	}
	return body[0], binary.BigEndian.Uint64(body[1:9]), body[9:], nil
}

// offsets reads the entries of use and resumed.
func offsets(body []byte) (map[byte]uint64, error) {
	if len(body)%9 != 0 {
		return nil, errFrame
	}
	out := map[byte]uint64{}
	for len(body) > 0 {
		out[body[0]] = binary.BigEndian.Uint64(body[1:9])
		body = body[9:]
	}
	return out, nil
}

// reader reads link messages off a transport.
type reader struct {
	r   *bufio.Reader
	buf []byte
}

func newReader(r io.Reader) *reader { return &reader{r: bufio.NewReaderSize(r, 32<<10)} }

// next is the next message; its body is valid until the following call.
func (r *reader) next() (byte, []byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(r.r, head[:4]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[:4])
	if n < 1 || n > maxFrame {
		return 0, nil, fmt.Errorf("%w: length %d", errFrame, n)
	}
	if cap(r.buf) < int(n) {
		r.buf = make([]byte, n)
	}
	b := r.buf[:n]
	if _, err := io.ReadFull(r.r, b); err != nil {
		return 0, nil, err
	}
	return b[0], b[1:], nil
}
