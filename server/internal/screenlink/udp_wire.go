package screenlink

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"math"
	"sync"
)

// The video over UDP (#7873, docs/extensions/computer-use.md "Video over UDP (#7873)"): on a direct route the phone may
// open one UDP flow beside its TLS connection, and the video channel's node → phone bytes then go as packets, each
// video message cut into fragments with Reed-Solomon parity, so a lost packet never holds back the frames after it.
// Everything else (RFB, the phone's video messages, every link message) stays on the TLS connection.
//
// A packet is a clear header (u8 version, 8-byte flow id, u64 packet number), then the sealed body (u8 kind and its
// fields) and a 16-byte tag: AES-256-GCM with the header as additional data and the direction's IV XOR the packet
// number as the nonce. The keys come from the TLS 1.3 exporter of the direct connection the flow belongs to, which
// already proved the session's token and pinned the node's certificate, so the flow is exactly as authenticated as
// that connection and needs no handshake of its own.

const (
	udpVersion   = 1
	udpHeaderLen = 17 // version, flow id, packet number
	udpTagLen    = 16
	// udpMaxPacket keeps a packet inside Tailscale's 1,280-byte MTU with IPv6 and UDP headers.
	udpMaxPacket = 1200
	// udpDataHead is a data packet's fields before its fragment: kind, channel, message offset, message length,
	// block, index, data shards, parity shards, flags.
	udpDataHead = 1 + 1 + 8 + 4 + 2 + 2 + 1 + 1 + 1
	// udpFragment is the most of a message one packet carries.
	udpFragment = udpMaxPacket - udpHeaderLen - udpTagLen - udpDataHead
	// udpBlockShards is the most data shards in one Reed-Solomon block; a longer message is several blocks.
	udpBlockShards = 128
	// udpMaxParity is the most parity per data shard the node sends, in ten-thousandths (40 %).
	udpMaxParity = 4000
	// udpHelloMin is the least a phone's hello body carries, so the node's answer is never larger than what reached it.
	udpHelloMin = 32
	// udpPacketLimit: a flow that has sent this many packets is closed rather than ever reusing a nonce.
	udpPacketLimit = 1 << 62

	// UDPExporterLabel names the flow's keys in the TLS exporter; the flow id is its context.
	UDPExporterLabel = "EXPORTER-persona-screen-udp-1"
	udpKeyMaterial   = 2 * (32 + 12)
)

// Packet kinds, the first byte of a sealed body.
const (
	udpHello   byte = 1  // phone: u64 the node's challenge answered (0: none), padding to udpHelloMin; node: u64 challenge, u8 validated
	udpData    byte = 2  // node: a fragment (udpDataHead)
	udpAck     byte = 3  // phone: u8 channel, u64 received through
	udpNack    byte = 4  // phone: u8 channel, n × (u64 message offset, u16 block, u16 index): send these data shards again
	udpSkip    byte = 5  // phone: u8 channel, u64 offset of the message given up
	udpUse     byte = 6  // phone: n × (u8 channel, u64 offset), as the link's use
	udpResumed byte = 7  // node: as the link's resumed
	udpReset   byte = 8  // node: u8 channel, u64 offset: the stream goes on there
	udpPing    byte = 9  // phone: u64 phone µs, u16 parity per data shard (ten-thousandths), u32 pacing kbit/s (0: the node's)
	udpPong    byte = 10 // node: u64 the ping's, then u64 data, u64 parity and u64 resent bytes sent so far (whole packets)
	udpWant    byte = 11 // phone: u8 channel, u64 from, u64 to: send the messages starting in [from, to) again
)

// Data flags.
const udpDroppable byte = 1 // a frame: the phone may give it up for a keyframe; any other message is sent until it arrives

var errUDP = errors.New("screenlink: bad udp packet")

// udpKeys are one flow's keys: the node seals with node→phone and opens with phone→node; the phone the other way.
type udpKeys struct {
	toPhone, toNode cipher.AEAD
	ivPhone, ivNode [12]byte
}

// newUDPKeys splits the exporter's udpKeyMaterial bytes: node→phone key and IV, then phone→node key and IV.
func newUDPKeys(material []byte) (*udpKeys, error) {
	if len(material) != udpKeyMaterial {
		return nil, errUDP
	}
	k := &udpKeys{}
	var err error
	if k.toPhone, err = newGCM(material[:32]); err != nil {
		return nil, err
	}
	copy(k.ivPhone[:], material[32:44])
	if k.toNode, err = newGCM(material[44:76]); err != nil {
		return nil, err
	}
	copy(k.ivNode[:], material[76:88])
	return k, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func udpNonce(iv [12]byte, pn uint64) []byte {
	n := iv
	for i := 0; i < 8; i++ {
		n[4+i] ^= byte(pn >> (56 - 8*i))
	}
	return n[:]
}

// sealUDP is one packet: the header in the clear, body sealed under aead with the nonce for pn.
func sealUDP(aead cipher.AEAD, iv [12]byte, flow [8]byte, pn uint64, body []byte) []byte {
	var head [udpHeaderLen]byte // the additional data may not overlap the output
	head[0] = udpVersion
	copy(head[1:9], flow[:])
	binary.BigEndian.PutUint64(head[9:], pn)
	out := make([]byte, udpHeaderLen, udpHeaderLen+len(body)+udpTagLen)
	copy(out, head[:])
	return aead.Seal(out, udpNonce(iv, pn), body, head[:])
}

// udpHeader reads a packet's clear header; it says nothing until the body opens.
func udpHeader(p []byte) (flow [8]byte, pn uint64, ok bool) {
	if len(p) < udpHeaderLen+udpTagLen+1 || p[0] != udpVersion {
		return flow, 0, false
	}
	copy(flow[:], p[1:9])
	return flow, binary.BigEndian.Uint64(p[9:17]), true
}

// openUDP authenticates and opens a packet whose header was read; a packet that doesn't open was not sealed with
// this flow's key, or was changed on the way.
func openUDP(aead cipher.AEAD, iv [12]byte, p []byte, pn uint64) ([]byte, error) {
	body, err := aead.Open(nil, udpNonce(iv, pn), p[udpHeaderLen:], p[:udpHeaderLen])
	if err != nil || len(body) == 0 {
		return nil, errUDP
	}
	return body, nil
}

// replayWindow accepts each packet number once: anything older than its 4,096 newest, or seen already, is a replay.
type replayWindow struct {
	top  uint64 // the highest number accepted, +1 (0: none yet)
	bits [64]uint64
}

const replaySpan = 64 * 64

// accept marks pn seen and says whether it was new.
func (w *replayWindow) accept(pn uint64) bool {
	if pn == math.MaxUint64 {
		return false
	}
	n := pn + 1
	switch {
	case n > w.top:
		shift := n - w.top
		if shift >= replaySpan {
			w.bits = [64]uint64{}
		} else {
			for i := w.top; i < n; i++ {
				w.clear(i)
			}
		}
		w.top = n
	case w.top-n >= replaySpan:
		return false
	case w.has(pn):
		return false
	}
	w.set(pn)
	return true
}

func (w *replayWindow) has(pn uint64) bool { return w.bits[(pn/64)%64]&(1<<(pn%64)) != 0 }
func (w *replayWindow) set(pn uint64)      { w.bits[(pn/64)%64] |= 1 << (pn % 64) }
func (w *replayWindow) clear(pn uint64)    { w.bits[(pn/64)%64] &^= 1 << (pn % 64) }

// udpSealer numbers and seals one direction's packets; numbers start at 0 and never repeat.
type udpSealer struct {
	mu   sync.Mutex
	aead cipher.AEAD
	iv   [12]byte
	flow [8]byte
	next uint64
}

func (s *udpSealer) seal(body []byte) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next >= udpPacketLimit {
		return nil, false
	}
	pn := s.next
	s.next++
	return sealUDP(s.aead, s.iv, s.flow, pn, body), true
}

// MARK: bodies

// udpFragmentHead is a data packet's fields.
type udpFragmentHead struct {
	Channel byte
	Offset  uint64 // the message's first byte in the channel
	Length  uint32 // the message's length
	Block   uint16
	Index   uint16 // < Data: a data shard; from Data on: parity
	Data    uint8  // data shards in this block
	Parity  uint8  // parity shards in this block
	Flags   byte
}

func (h udpFragmentHead) encode(fragment []byte) []byte {
	b := make([]byte, udpDataHead, udpDataHead+len(fragment))
	b[0] = udpData
	b[1] = h.Channel
	binary.BigEndian.PutUint64(b[2:], h.Offset)
	binary.BigEndian.PutUint32(b[10:], h.Length)
	binary.BigEndian.PutUint16(b[14:], h.Block)
	binary.BigEndian.PutUint16(b[16:], h.Index)
	b[18], b[19], b[20] = h.Data, h.Parity, h.Flags
	return append(b, fragment...)
}

func decodeFragment(body []byte) (udpFragmentHead, []byte, error) {
	if len(body) < udpDataHead || body[0] != udpData {
		return udpFragmentHead{}, nil, errUDP
	}
	h := udpFragmentHead{Channel: body[1], Offset: binary.BigEndian.Uint64(body[2:]), Length: binary.BigEndian.Uint32(body[10:]),
		Block: binary.BigEndian.Uint16(body[14:]), Index: binary.BigEndian.Uint16(body[16:]), Data: body[18], Parity: body[19], Flags: body[20]}
	if h.Data == 0 || int(h.Index) >= int(h.Data)+int(h.Parity) {
		return udpFragmentHead{}, nil, errUDP
	}
	return h, body[udpDataHead:], nil
}

// udpBlock is one Reed-Solomon block of a message: which shards it covers and how long they are.
type udpBlock struct {
	First    int // the message's shard index of its first data shard
	Data     int
	ShardLen int // the parity shards' length; data shards are this long but for the message's last
}

// udpBlocks lays a message of n bytes out in blocks: fragments of udpFragment bytes, at most udpBlockShards to a
// block. A block's shards are udpFragment long, or the message's length when it is one short shard.
func udpBlocks(n int) []udpBlock {
	shards := max(1, (n+udpFragment-1)/udpFragment)
	var out []udpBlock
	for first := 0; first < shards; first += udpBlockShards {
		k := min(udpBlockShards, shards-first)
		out = append(out, udpBlock{First: first, Data: k, ShardLen: min(udpFragment, n-first*udpFragment)})
	}
	return out
}

// udpParityCount is the parity shards for a block of k data shards at ratio (ten-thousandths of a shard per data
// shard): none at 0, else ⌈ratio × k⌉ + 1, within udpMaxParity and 256 shards in all.
func udpParityCount(k, ratio int) int {
	if ratio <= 0 || k <= 0 {
		return 0
	}
	ratio = min(ratio, udpMaxParity)
	m := (ratio*k+9999)/10000 + 1
	return min(m, 256-k)
}

// udpShard is data shard j (the message's) of msg, without padding.
func udpShard(msg []byte, j int) []byte {
	from := j * udpFragment
	return msg[from:min(from+udpFragment, len(msg))]
}

// udpParity is block b's parity for msg: its data shards, the last padded with zeros to the block's shard length.
func udpParity(msg []byte, b udpBlock, m int) ([][]byte, error) {
	if m == 0 {
		return nil, nil
	}
	data := make([][]byte, b.Data)
	for j := range data {
		s := udpShard(msg, b.First+j)
		if len(s) < b.ShardLen {
			s = append(append([]byte{}, s...), make([]byte, b.ShardLen-len(s))...)
		}
		data[j] = s
	}
	return rsEncode(data, m)
}

// chanOffsetBody is a kind byte then a channel and offset (ack, skip, reset).
func chanOffsetBody(kind, ch byte, off uint64) []byte {
	b := make([]byte, 10)
	b[0], b[1] = kind, ch
	binary.BigEndian.PutUint64(b[2:], off)
	return b
}
