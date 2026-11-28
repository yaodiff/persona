package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type writeRejectRecord struct {
	kind, op    string
	size, limit int
}

// WriteLimit rejects oversized requests, events and replies before writing to the wire while allowing lines
// up to the exact limit (newline included), naming the operation and encoded byte size without leaking contents.
func TestPeerWriteLimitExactBoundary(t *testing.T) {
	const secret = "SECRET_PAYLOAD_MARKER_DO_NOT_LEAK"

	t.Run("request value, appended, and marshaled", func(t *testing.T) {
		raw := []byte(`{"secret":"` + secret + `","pad":"` + strings.Repeat("x", 128) + `"}`)
		line, err := oldCall("h1", "devices.call", json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		exact := len(line)

		for _, tc := range []struct {
			name   string
			budget int
			fit    bool
		}{
			{"fits exactly", exact, true},
			{"one byte over", exact - 1, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				for _, mode := range []string{"value", "appended", "marshaled"} {
					var mu sync.Mutex
					var rejected []writeRejectRecord
					var wire bytes.Buffer
					var p *Peer
					// Close on write so Call returns ErrDropped once a line actually hits the wire.
					w := &lineCatcher{}
					p = New("h", w)
					w.p = p
					p.WriteLimit = tc.budget
					p.OnWriteRejected = func(kind, op string, size, limit int) {
						mu.Lock()
						rejected = append(rejected, writeRejectRecord{kind: kind, op: op, size: size, limit: limit})
						mu.Unlock()
					}

					appendRan := false
					var params any
					switch mode {
					case "value":
						params = json.RawMessage(raw)
					case "marshaled":
						params = Marshaled(raw)
					case "appended":
						a := appended(raw)
						orig := a.Append
						a.Append = func(b []byte) []byte {
							appendRan = true
							return orig(b)
						}
						params = a
					}

					err := p.Call(context.Background(), "devices.call", params, nil)
					if tc.fit {
						if !errors.Is(err, ErrDropped) {
							t.Fatalf("%s: got err=%v, want ErrDropped after wire write", mode, err)
						}
						if !bytes.Equal(w.line, line) {
							t.Fatalf("%s: wire line mismatch (%d vs %d bytes)", mode, len(w.line), len(line))
						}
						if len(rejected) != 0 {
							t.Fatalf("%s: unexpected rejection %+v", mode, rejected)
						}
					} else {
						if !errors.Is(err, ErrMessageTooLarge) {
							t.Fatalf("%s: got err=%v, want ErrMessageTooLarge", mode, err)
						}
						if len(w.line) != 0 || wire.Len() != 0 {
							t.Fatalf("%s: oversized request wrote %d bytes", mode, len(w.line))
						}
						if mode == "appended" && appendRan {
							t.Fatalf("appended: Append callback ran despite exceeding WriteLimit")
						}
						if strings.Contains(err.Error(), secret) {
							t.Fatalf("%s: error leaked payload content: %v", mode, err)
						}
						if !strings.Contains(err.Error(), "devices.call") || !strings.Contains(err.Error(), strconv.Itoa(exact)) {
							t.Fatalf("%s: error missing operation or size: %v", mode, err)
						}
						if len(rejected) != 1 || rejected[0] != (writeRejectRecord{"request", "devices.call", exact, tc.budget}) {
							t.Fatalf("%s: rejected = %+v, want single exact record", mode, rejected)
						}
						select {
						case <-p.Done():
							t.Fatalf("%s: peer closed after rejected outgoing request", mode)
						default:
						}
					}
				}
			})
		}
	})

	t.Run("event marshaled, raw, and struct", func(t *testing.T) {
		raw := []byte(`{"secret":"` + secret + `","pad":"` + strings.Repeat("y", 128) + `"}`)
		wantLine, ok := marshaledLine("fleet.sessions", Marshaled(raw))
		if !ok {
			t.Fatal("marshaledLine failed")
		}
		exact := len(wantLine)

		for _, mode := range []string{"marshaled", "raw", "struct"} {
			var wire bytes.Buffer
			var rejected []writeRejectRecord
			p := New("s", &wire)
			p.WriteLimit = exact - 1
			p.OnWriteRejected = func(kind, op string, size, limit int) {
				rejected = append(rejected, writeRejectRecord{kind: kind, op: op, size: size, limit: limit})
			}

			var params any
			switch mode {
			case "marshaled":
				params = Marshaled(raw)
			case "raw":
				params = json.RawMessage(raw)
			case "struct":
				params = struct {
					Secret string `json:"secret"`
					Pad    string `json:"pad"`
				}{Secret: secret, Pad: strings.Repeat("y", 128)}
			}

			err := p.Notify("fleet.sessions", params)
			if !errors.Is(err, ErrMessageTooLarge) {
				t.Fatalf("%s: Notify over limit err=%v, want ErrMessageTooLarge", mode, err)
			}
			if wire.Len() != 0 {
				t.Fatalf("%s: Notify over limit wrote %d bytes", mode, wire.Len())
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("%s: error leaked payload content: %v", mode, err)
			}
			if len(rejected) != 1 || rejected[0] != (writeRejectRecord{"event", "fleet.sessions", exact, exact - 1}) {
				t.Fatalf("%s: rejected=%+v, want exact event record", mode, rejected)
			}

			// Exact-limit write still succeeds on the same peer.
			p.WriteLimit = exact
			if err := p.Notify("fleet.sessions", params); err != nil {
				t.Fatalf("%s: Notify at exact limit err=%v", mode, err)
			}
			if !bytes.Equal(wire.Bytes(), wantLine) {
				t.Fatalf("%s: wire mismatch at exact limit", mode)
			}
		}
	})

	t.Run("oversized reply returns bounded error and keeps peer alive", func(t *testing.T) {
		c2sR, c2sW := io.Pipe()
		s2cR, s2cW := io.Pipe()
		defer c2sR.Close()
		defer c2sW.Close()
		defer s2cR.Close()
		defer s2cW.Close()

		const limit = 256
		var mu sync.Mutex
		var rejected []writeRejectRecord

		server := New("s", s2cW)
		server.ReadLimit = limit
		server.WriteLimit = limit
		server.OnWriteRejected = func(kind, op string, size, limit int) {
			mu.Lock()
			rejected = append(rejected, writeRejectRecord{kind: kind, op: op, size: size, limit: limit})
			mu.Unlock()
		}
		server.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			switch method {
			case "big.result":
				return map[string]string{"secret": secret, "pad": strings.Repeat("z", limit)}, nil
			case "big.error":
				return nil, fmt.Errorf("%s %s", secret, strings.Repeat("e", limit))
			default:
				return map[string]bool{"ok": true}, nil
			}
		}

		client := New("c", c2sW)
		client.ReadLimit = limit
		client.WriteLimit = limit

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		go func() { _ = server.Serve(ctx, c2sR) }()
		go func() { _ = client.Serve(ctx, s2cR) }()

		for _, method := range []string{"big.result", "big.error"} {
			err := client.Call(ctx, method, nil, nil)
			var re *Error
			if !errors.As(err, &re) {
				t.Fatalf("%s: got %T %v, want *rpc.Error", method, err, err)
			}
			if !strings.Contains(re.Message, method) || !strings.Contains(re.Message, "bytes") || strings.Contains(re.Message, secret) {
				t.Fatalf("%s: unexpected error reply %q", method, re.Message)
			}
		}

		// Subsequent normal call must still succeed on the exact same connection.
		var out struct {
			OK bool `json:"ok"`
		}
		if err := client.Call(ctx, "ping", nil, &out); err != nil || !out.OK {
			t.Fatalf("ping after oversized replies failed: out=%+v err=%v", out, err)
		}

		mu.Lock()
		got := append([]writeRejectRecord(nil), rejected...)
		mu.Unlock()
		if len(got) != 2 || got[0].kind != "reply" || got[0].op != "big.result" || got[0].size <= limit ||
			got[1].kind != "reply" || got[1].op != "big.error" || got[1].size <= limit {
			t.Fatalf("unexpected rejection records: %+v", got)
		}
	})

	t.Run("unrepresentable error reply closes peer", func(t *testing.T) {
		var wire bytes.Buffer
		p := New("h", &wire)
		p.WriteLimit = 16
		p.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
			return strings.Repeat("x", 64), nil
		}
		p.handle(context.Background(), message{ID: "r1", Method: "tiny"})
		select {
		case <-p.Done():
		default:
			t.Fatal("unrepresentable error reply must close peer")
		}
	})

	t.Run("Limits.FrameBytes still takes precedence over WriteLimit", func(t *testing.T) {
		var wire bytes.Buffer
		p := New("h", &wire)
		p.WriteLimit = 1024
		p.Limits.FrameBytes = 128
		p.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
			return strings.Repeat("x", 256), nil
		}
		p.handle(context.Background(), message{ID: "r1", Method: "large"})
		var reply message
		if err := json.NewDecoder(bufio.NewReader(&wire)).Decode(&reply); err != nil {
			t.Fatal(err)
		}
		if reply.Error == nil || reply.Error.Message != errFrameBudget.Error() {
			t.Fatalf("reply error = %+v, want %q", reply.Error, errFrameBudget.Error())
		}
	})
}

// Rejecting an oversized Marshaled event must not allocate the envelope line.
func TestPeerWriteLimitMarshaledEventAvoidsEnvelopeAlloc(t *testing.T) {
	raw := Marshaled(`{"pad":"` + strings.Repeat("a", 256<<10) + `"}`)
	p := New("s", io.Discard)
	p.WriteLimit = 64 << 10
	allocs := testing.AllocsPerRun(20, func() {
		if err := p.Notify("fleet.sessions", raw); !errors.Is(err, ErrMessageTooLarge) {
			t.Fatalf("got %v, want ErrMessageTooLarge", err)
		}
	})
	if allocs > 4 {
		t.Fatalf("rejected oversized Marshaled Notify allocated %.0f times, want <= 4 (no envelope allocation)", allocs)
	}
}
