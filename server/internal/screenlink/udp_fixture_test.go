package screenlink

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

// The video over UDP's shared cases (spec/fixtures/screen-udp, #7873): sealing with test keys, every packet kind, the
// layout of messages in Reed-Solomon blocks and the parity itself. The iPhone's client reads the same file, so both
// ends seal, number, cut and rebuild alike. PERSONA_WRITE_UDP_FIXTURES=1 writes the file from the cases below; the
// test then checks the file against the code either way.

const udpFixturePath = "../../../spec/fixtures/screen-udp/cases.json"

type udpFixture struct {
	Version  int `json:"version"`
	Exporter struct {
		Label       string            `json:"label"`
		MaterialHex string            `json:"material_hex"`
		NodeToPhone map[string]string `json:"node_to_phone"`
		PhoneToNode map[string]string `json:"phone_to_node"`
	} `json:"exporter"`
	Packets []udpPacketCase `json:"packets"`
	Layout  []udpLayoutCase `json:"layout"`
	RS      []udpRSCase     `json:"reed_solomon"`
	Link    []udpLinkCase   `json:"link"`
}

type udpPacketCase struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Direction   string         `json:"direction"` // node_to_phone | phone_to_node
	FlowHex     string         `json:"flow_hex"`
	Number      uint64         `json:"packet_number"`
	BodyHex     string         `json:"body_hex"`
	SealedHex   string         `json:"sealed_hex"`
	Expected    map[string]any `json:"expected"`
}

type udpLayoutCase struct {
	Name   string           `json:"name"`
	Length int              `json:"message_length"`
	Blocks []map[string]int `json:"blocks"`
	// Parity: per ratio (ten-thousandths, as text), each block's parity shards.
	Parity map[string][]int `json:"parity"`
}

type udpRSCase struct {
	Name      string   `json:"name"`
	DataHex   []string `json:"data_hex"`
	ParityHex []string `json:"parity_hex"`
	Lost      []int    `json:"lost"` // shard indexes (data then parity) any decoder must rebuild the data without
}

type udpLinkCase struct {
	Name     string         `json:"name"`
	FrameHex string         `json:"frame_hex"`
	Expected map[string]any `json:"expected"`
}

// udpBodyFor is a packet body from the fixture's fields; udpFieldsOf reads one back. Numbers are float64, as JSON.
func udpBodyFor(t *testing.T, e map[string]any) []byte {
	t.Helper()
	n := func(k string) uint64 { return uint64(e[k].(float64)) }
	u16 := func(b []byte, v uint64) []byte { return binary.BigEndian.AppendUint16(b, uint16(v)) }
	u32 := func(b []byte, v uint64) []byte { return binary.BigEndian.AppendUint32(b, uint32(v)) }
	u64 := func(b []byte, v uint64) []byte { return binary.BigEndian.AppendUint64(b, v) }
	switch e["kind"] {
	case "hello_phone":
		b := u64([]byte{udpHello}, n("echo"))
		return append(b, make([]byte, udpHelloMin-8)...)
	case "hello_node":
		b := u64([]byte{udpHello}, n("challenge"))
		return append(b, byte(n("validated")))
	case "data":
		frag, _ := hex.DecodeString(e["fragment_hex"].(string))
		h := udpFragmentHead{Channel: byte(n("channel")), Offset: n("offset"), Length: uint32(n("length")), Block: uint16(n("block")),
			Index: uint16(n("index")), Data: uint8(n("data")), Parity: uint8(n("parity")), Flags: byte(n("flags"))}
		return h.encode(frag)
	case "ack", "skip", "reset":
		kind := map[any]byte{"ack": udpAck, "skip": udpSkip, "reset": udpReset}[e["kind"]]
		return chanOffsetBody(kind, byte(n("channel")), n("offset"))
	case "nack":
		b := []byte{udpNack, byte(n("channel"))}
		for _, x := range e["entries"].([]any) {
			v := x.([]any)
			b = u64(b, uint64(v[0].(float64)))
			b = u16(b, uint64(v[1].(float64)))
			b = u16(b, uint64(v[2].(float64)))
		}
		return b
	case "want":
		return u64(u64([]byte{udpWant, byte(n("channel"))}, n("from")), n("to"))
	case "use", "resumed":
		b := []byte{map[any]byte{"use": udpUse, "resumed": udpResumed}[e["kind"]]}
		for _, x := range e["entries"].([]any) {
			v := x.([]any)
			b = append(b, chanOffset(byte(v[0].(float64)), uint64(v[1].(float64)))...)
		}
		return b
	case "ping":
		return u32(u16(u64([]byte{udpPing}, n("phone_us")), n("parity")), n("pace_kbps"))
	case "pong":
		return u64(u64(u64(u64([]byte{udpPong}, n("echo")), n("data_bytes")), n("parity_bytes")), n("resent_bytes"))
	}
	t.Fatalf("kind %v", e["kind"])
	return nil
}

func udpFieldsOf(t *testing.T, direction string, b []byte) map[string]any {
	t.Helper()
	f := func(v uint64) float64 { return float64(v) }
	u64 := func(i int) float64 { return f(binary.BigEndian.Uint64(b[i:])) }
	entries := func(from, size int, read func([]byte) []any) []any {
		out := []any{}
		for e := b[from:]; len(e) >= size; e = e[size:] {
			out = append(out, read(e))
		}
		return out
	}
	switch b[0] {
	case udpHello:
		if direction == "phone_to_node" {
			return map[string]any{"kind": "hello_phone", "echo": u64(1)}
		}
		return map[string]any{"kind": "hello_node", "challenge": u64(1), "validated": f(uint64(b[9]))}
	case udpData:
		h, frag, err := decodeFragment(b)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{"kind": "data", "channel": f(uint64(h.Channel)), "offset": f(h.Offset), "length": f(uint64(h.Length)),
			"block": f(uint64(h.Block)), "index": f(uint64(h.Index)), "data": f(uint64(h.Data)), "parity": f(uint64(h.Parity)),
			"flags": f(uint64(h.Flags)), "fragment_hex": hex.EncodeToString(frag)}
	case udpAck, udpSkip, udpReset:
		name := map[byte]string{udpAck: "ack", udpSkip: "skip", udpReset: "reset"}[b[0]]
		return map[string]any{"kind": name, "channel": f(uint64(b[1])), "offset": u64(2)}
	case udpNack:
		return map[string]any{"kind": "nack", "channel": f(uint64(b[1])), "entries": entries(2, 12, func(e []byte) []any {
			return []any{f(binary.BigEndian.Uint64(e)), f(uint64(binary.BigEndian.Uint16(e[8:]))), f(uint64(binary.BigEndian.Uint16(e[10:])))}
		})}
	case udpWant:
		return map[string]any{"kind": "want", "channel": f(uint64(b[1])), "from": u64(2), "to": u64(10)}
	case udpUse, udpResumed:
		name := map[byte]string{udpUse: "use", udpResumed: "resumed"}[b[0]]
		return map[string]any{"kind": name, "entries": entries(1, 9, func(e []byte) []any {
			return []any{f(uint64(e[0])), f(binary.BigEndian.Uint64(e[1:]))}
		})}
	case udpPing:
		return map[string]any{"kind": "ping", "phone_us": u64(1), "parity": f(uint64(binary.BigEndian.Uint16(b[9:]))),
			"pace_kbps": f(uint64(binary.BigEndian.Uint32(b[11:])))}
	case udpPong:
		return map[string]any{"kind": "pong", "echo": u64(1), "data_bytes": u64(9), "parity_bytes": u64(17), "resent_bytes": u64(25)}
	}
	t.Fatalf("kind %d", b[0])
	return nil
}

// udpFixtureCases are the cases the file is written from.
func udpFixtureCases(t *testing.T) udpFixture {
	var doc udpFixture
	doc.Version = 1
	material := make([]byte, udpKeyMaterial)
	for i := range material {
		material[i] = byte(i*7 + 3)
	}
	doc.Exporter.Label = UDPExporterLabel
	doc.Exporter.MaterialHex = hex.EncodeToString(material)
	doc.Exporter.NodeToPhone = map[string]string{"key_hex": hex.EncodeToString(material[:32]), "iv_hex": hex.EncodeToString(material[32:44])}
	doc.Exporter.PhoneToNode = map[string]string{"key_hex": hex.EncodeToString(material[44:76]), "iv_hex": hex.EncodeToString(material[76:88])}
	flow := "0a1b2c3d4e5f6071"
	pk := func(name, desc, dir string, pn uint64, e map[string]any) udpPacketCase {
		return udpPacketCase{Name: name, Description: desc, Direction: dir, FlowHex: flow, Number: pn, Expected: e}
	}
	doc.Packets = []udpPacketCase{
		pk("hello_first", "The phone's first hello: no challenge to answer yet, padded to 32 bytes.", "phone_to_node", 0,
			map[string]any{"kind": "hello_phone", "echo": 0.0}),
		pk("hello_challenge", "The node's answer to a hello from an address not proved yet: a challenge.", "node_to_phone", 0,
			map[string]any{"kind": "hello_node", "challenge": float64(0x0123456789abcdef), "validated": 0.0}),
		pk("hello_proof", "The phone's hello answering the challenge.", "phone_to_node", 1,
			map[string]any{"kind": "hello_phone", "echo": float64(0x0123456789abcdef)}),
		pk("hello_confirmed", "The node confirms the address.", "node_to_phone", 1,
			map[string]any{"kind": "hello_node", "challenge": 0.0, "validated": 1.0}),
		pk("use_video", "The phone moves video channel 2 onto the flow from offset 0.", "phone_to_node", 2,
			map[string]any{"kind": "use", "entries": []any{[]any{2.0, 0.0}}}),
		pk("resumed_video", "The node's answer: it has 120 of the phone's bytes on channel 2.", "node_to_phone", 2,
			map[string]any{"kind": "resumed", "entries": []any{[]any{2.0, 120.0}}}),
		pk("data_frame", "A fragment of a frame (droppable): data shard 1 of 3 in block 0, with 2 parity shards.", "node_to_phone", 3,
			map[string]any{"kind": "data", "channel": 2.0, "offset": 4096.0, "length": 3000.0, "block": 0.0, "index": 1.0,
				"data": 3.0, "parity": 2.0, "flags": 1.0, "fragment_hex": "000102030405060708090a0b0c0d0e0f"}),
		pk("data_parity", "A parity shard (index 3 = the first after 3 data shards) of a message that must arrive.", "node_to_phone", 4,
			map[string]any{"kind": "data", "channel": 2.0, "offset": 4096.0, "length": 3000.0, "block": 0.0, "index": 3.0,
				"data": 3.0, "parity": 2.0, "flags": 0.0, "fragment_hex": "f0e1d2c3"}),
		pk("ack", "The phone has every message through offset 7096.", "phone_to_node", 3,
			map[string]any{"kind": "ack", "channel": 2.0, "offset": 7096.0}),
		pk("nack", "The phone asks for two data shards again.", "phone_to_node", 4,
			map[string]any{"kind": "nack", "channel": 2.0, "entries": []any{[]any{4096.0, 0.0, 0.0}, []any{9000.0, 1.0, 127.0}}}),
		pk("want", "The phone asks for the messages starting from 7096 up to 9000 (lost whole).", "phone_to_node", 5,
			map[string]any{"kind": "want", "channel": 2.0, "from": 7096.0, "to": 9000.0}),
		pk("skip", "The phone gives the message at 7096 up.", "phone_to_node", 6,
			map[string]any{"kind": "skip", "channel": 2.0, "offset": 7096.0}),
		pk("reset", "The node: the stream goes on at 12000.", "node_to_phone", 5,
			map[string]any{"kind": "reset", "channel": 2.0, "offset": 12000.0}),
		pk("ping", "The phone's ping: its clock, 15 % parity asked for, pacing at 150 Mbit/s.", "phone_to_node", 7,
			map[string]any{"kind": "ping", "phone_us": 1000000.0, "parity": 1500.0, "pace_kbps": 150000.0}),
		pk("pong", "The node's pong: the ping's value, then bytes sent so far as data, parity and again.", "node_to_phone", 6,
			map[string]any{"kind": "pong", "echo": 1000000.0, "data_bytes": 5000000.0, "parity_bytes": 750000.0, "resent_bytes": 1200.0}),
	}
	keys, _ := newUDPKeys(material)
	var id [8]byte
	raw, _ := hex.DecodeString(flow)
	copy(id[:], raw)
	for i := range doc.Packets {
		c := &doc.Packets[i]
		body := udpBodyFor(t, c.Expected)
		c.BodyHex = hex.EncodeToString(body)
		aead, iv := keys.toPhone, keys.ivPhone
		if c.Direction == "phone_to_node" {
			aead, iv = keys.toNode, keys.ivNode
		}
		c.SealedHex = hex.EncodeToString(sealUDP(aead, iv, id, c.Number, body))
	}

	for _, n := range []int{5, 300, udpFragment, udpFragment + 1, 3000, udpBlockShards * udpFragment, udpBlockShards*udpFragment + 1, 400000} {
		c := udpLayoutCase{Name: fmt.Sprintf("length_%d", n), Length: n, Parity: map[string][]int{}}
		for _, b := range udpBlocks(n) {
			c.Blocks = append(c.Blocks, map[string]int{"first": b.First, "data": b.Data, "shard_length": b.ShardLen})
		}
		for _, ratio := range []int{0, 500, 1500, 4000, 9000} {
			var ms []int
			for _, b := range udpBlocks(n) {
				ms = append(ms, udpParityCount(b.Data, ratio))
			}
			c.Parity[fmt.Sprint(ratio)] = ms
		}
		doc.Layout = append(doc.Layout, c)
	}

	for _, km := range [][3]int{{1, 1, 9}, {3, 2, 16}, {5, 3, 24}, {10, 4, 8}} {
		k, m, n := km[0], km[1], km[2]
		data := make([][]byte, k)
		for j := range data {
			data[j] = make([]byte, n)
			for b := range data[j] {
				data[j][b] = byte(j*31 + b*7 + 1)
			}
		}
		parity, err := rsEncode(data, m)
		if err != nil {
			t.Fatal(err)
		}
		c := udpRSCase{Name: fmt.Sprintf("k%d_m%d", k, m)}
		for _, d := range data {
			c.DataHex = append(c.DataHex, hex.EncodeToString(d))
		}
		for _, p := range parity {
			c.ParityHex = append(c.ParityHex, hex.EncodeToString(p))
		}
		// Lose the first m data shards (or all but parity when k < m).
		for i := 0; i < m && i < k; i++ {
			c.Lost = append(c.Lost, i)
		}
		doc.RS = append(doc.RS, c)
	}

	flowText := base64.RawURLEncoding.EncodeToString(id[:])
	ready, _ := json.Marshal(UDPReady{Port: 50123, Flow: flowText})
	unavailable, _ := json.Marshal(UDPReady{Unavailable: "relay"})
	doc.Link = []udpLinkCase{
		{Name: "udp_open", FrameHex: hex.EncodeToString(frame(TypeUDPOpen)), Expected: map[string]any{"type": "udp_open"}},
		{Name: "udp_ready", FrameHex: hex.EncodeToString(frame(TypeUDPReady, ready)),
			Expected: map[string]any{"type": "udp_ready", "port": 50123.0, "flow": flowText}},
		{Name: "udp_ready_unavailable", FrameHex: hex.EncodeToString(frame(TypeUDPReady, unavailable)),
			Expected: map[string]any{"type": "udp_ready", "unavailable": "relay"}},
	}
	return doc
}

func TestTheUDPWireMatchesTheSharedCases(t *testing.T) {
	if os.Getenv("PERSONA_WRITE_UDP_FIXTURES") == "1" {
		b, err := json.MarshalIndent(udpFixtureCases(t), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(udpFixturePath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(udpFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var doc udpFixture
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Version != 1 {
		t.Fatalf("fixture: %v", err)
	}
	if doc.Exporter.Label != UDPExporterLabel {
		t.Fatalf("label %q", doc.Exporter.Label)
	}
	material, _ := hex.DecodeString(doc.Exporter.MaterialHex)
	keys, err := newUDPKeys(material)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Packets {
		t.Run(c.Name, func(t *testing.T) {
			body, _ := hex.DecodeString(c.BodyHex)
			if enc := udpBodyFor(t, c.Expected); !bytes.Equal(enc, body) {
				t.Fatalf("encoded %x, want %x", enc, body)
			}
			if got := udpFieldsOf(t, c.Direction, body); !reflect.DeepEqual(got, c.Expected) {
				t.Fatalf("decoded %v, want %v", got, c.Expected)
			}
			var id [8]byte
			raw, _ := hex.DecodeString(c.FlowHex)
			copy(id[:], raw)
			aead, iv := keys.toPhone, keys.ivPhone
			if c.Direction == "phone_to_node" {
				aead, iv = keys.toNode, keys.ivNode
			}
			sealed, _ := hex.DecodeString(c.SealedHex)
			if got := sealUDP(aead, iv, id, c.Number, body); !bytes.Equal(got, sealed) {
				t.Fatalf("sealed %x, want %x", got, sealed)
			}
			gotID, pn, ok := udpHeader(sealed)
			if !ok || gotID != id || pn != c.Number {
				t.Fatalf("header %x %d %v", gotID, pn, ok)
			}
			opened, err := openUDP(aead, iv, sealed, pn)
			if err != nil || !bytes.Equal(opened, body) {
				t.Fatalf("opened %x %v", opened, err)
			}
		})
	}
	for _, c := range doc.Layout {
		t.Run(c.Name, func(t *testing.T) {
			blocks := udpBlocks(c.Length)
			if len(blocks) != len(c.Blocks) {
				t.Fatalf("%d blocks, want %d", len(blocks), len(c.Blocks))
			}
			for i, b := range blocks {
				want := c.Blocks[i]
				if b.First != want["first"] || b.Data != want["data"] || b.ShardLen != want["shard_length"] {
					t.Fatalf("block %d %+v, want %v", i, b, want)
				}
			}
			for ratio, want := range c.Parity {
				var r int
				fmt.Sscan(ratio, &r)
				for i, b := range blocks {
					if got := udpParityCount(b.Data, r); got != want[i] {
						t.Fatalf("ratio %d block %d: %d parity, want %d", r, i, got, want[i])
					}
				}
			}
		})
	}
	for _, c := range doc.RS {
		t.Run(c.Name, func(t *testing.T) {
			var data, parity [][]byte
			for _, h := range c.DataHex {
				b, _ := hex.DecodeString(h)
				data = append(data, b)
			}
			for _, h := range c.ParityHex {
				b, _ := hex.DecodeString(h)
				parity = append(parity, b)
			}
			got, err := rsEncode(data, len(parity))
			if err != nil || !reflect.DeepEqual(got, parity) {
				t.Fatalf("parity %x, want %x (%v)", got, parity, err)
			}
			shards := append(append([][]byte{}, data...), parity...)
			for _, i := range c.Lost {
				shards[i] = nil
			}
			if err := rsReconstruct(shards, len(data)); err != nil {
				t.Fatal(err)
			}
			for j := range data {
				if !bytes.Equal(shards[j], data[j]) {
					t.Fatalf("data shard %d rebuilt wrong", j)
				}
			}
		})
	}
	for _, c := range doc.Link {
		t.Run(c.Name, func(t *testing.T) {
			b, _ := hex.DecodeString(c.FrameHex)
			typ, body, err := newReader(bytes.NewReader(b)).next()
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]any{}
			switch typ {
			case TypeUDPOpen:
				got["type"] = "udp_open"
			case TypeUDPReady:
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				got["type"] = "udp_ready"
			}
			if !reflect.DeepEqual(got, c.Expected) {
				t.Fatalf("decoded %v, want %v", got, c.Expected)
			}
		})
	}
}
