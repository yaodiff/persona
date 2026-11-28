package bus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/rpc"
)

// Sending an oversized event (such as a >64 MiB fleet.sessions frame) must fail locally without disconnecting
// the bus WebSocket with StatusMessageTooBig (1009), so subsequent calls continue on the same connection.
func TestClientOversizedEventDoesNotDisconnectBus(t *testing.T) {
	var dials atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != Path {
			http.NotFound(w, r)
			return
		}
		dials.Add(1)
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn := NewConn(r.Context(), ws)
		p := rpc.New("h", conn)
		p.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			if method == "manager.hello" {
				return map[string]any{"name": "assistant"}, nil
			}
			return map[string]any{"ok": true, "method": method}, nil
		}
		_ = p.Serve(r.Context(), conn)
	}))
	defer srv.Close()

	var log logLines
	c := &Client{
		Hub:          srv.URL,
		Token:        "t",
		HelloMethod:  "manager.hello",
		HelloParams:  func() any { return map[string]any{"id": "assistant"} },
		Logf:         log.logf,
		Uncompressed: true,
		Backoff:      50 * time.Millisecond,
		MaxBackoff:   100 * time.Millisecond,
	}
	ctx, _, _ := runClient(t, c)
	if err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	firstPeer := c.Peer()
	if firstPeer == nil {
		t.Fatal("expected connected peer")
	}

	// Build a pre-encoded fleet.sessions payload exceeding MaxMessage (64 MiB), containing a marker
	// that must not appear in diagnostics.
	const secretMarker = "PRIVATE_PAYLOAD_CONTENT_DO_NOT_LOG"
	payload := make([]byte, 0, MaxMessage+64)
	payload = append(payload, `{"machine_id":"box","pad":"`...)
	payload = append(payload, secretMarker...)
	payload = append(payload, bytes.Repeat([]byte("a"), MaxMessage)...)
	payload = append(payload, `"}`...)

	err := c.Notify("fleet.sessions", rpc.Marshaled(payload))
	if !errors.Is(err, rpc.ErrMessageTooLarge) {
		t.Fatalf("oversized fleet.sessions event err = %v, want rpc.ErrMessageTooLarge", err)
	}

	// The exact same connection must stay alive and keep answering RPCs.
	var out struct {
		OK     bool   `json:"ok"`
		Method string `json:"method"`
	}
	if err := c.Call(ctx, "ping", nil, &out); err != nil || !out.OK || out.Method != "ping" {
		t.Fatalf("ping after oversized event failed: out=%+v err=%v logs=%q", out, err, log.all())
	}
	if got := c.Peer(); got != firstPeer {
		t.Fatalf("bus peer changed after oversized event (%p -> %p, dials=%d, logs=%q)", firstPeer, got, dials.Load(), log.all())
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials = %d, want 1 (connection must not disconnect)", got)
	}
	joined := strings.Join(log.all(), "\n")
	if !strings.Contains(joined, "fleet.sessions") || strings.Contains(joined, secretMarker) {
		t.Fatalf("expected diagnostic with operation name and without payload contents; logs=%q", joined)
	}
}

// Verify exact limit acceptance, local rejection at limit+1 across events, requests, and handler replies,
// and continued bidirectional RPC traffic on the same WebSocket connection.
func TestClientWriteLimitExactBoundaryAndRecovery(t *testing.T) {
	const limit = 2048
	const secretMarker = "SECRET_BUS_CONTENT_MUST_NOT_APPEAR"

	var dials atomic.Int32
	var srvMu sync.Mutex
	var srvPeer *rpc.Peer
	gotEvents := make(chan int, 4)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != Path {
			http.NotFound(w, r)
			return
		}
		dials.Add(1)
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn := newConn(r.Context(), ws, limit)
		p := rpc.New("h", conn)
		p.ReadLimit = limit
		p.WriteLimit = limit
		p.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			if method == "manager.hello" {
				return map[string]any{"name": "assistant"}, nil
			}
			return map[string]any{"ok": true, "method": method}, nil
		}
		p.OnEvent = func(event string, params json.RawMessage) {
			if event == "fleet.sessions" {
				gotEvents <- len(params)
			}
		}
		srvMu.Lock()
		srvPeer = p
		srvMu.Unlock()
		_ = p.Serve(r.Context(), conn)
	}))
	defer srv.Close()

	var log logLines
	c := &Client{
		Hub:          srv.URL,
		Token:        "t",
		HelloMethod:  "manager.hello",
		HelloParams:  func() any { return map[string]any{"id": "assistant"} },
		Logf:         log.logf,
		Uncompressed: true,
		maxMessage:   limit,
		OnRequest: func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			if method == "devices.big" {
				return map[string]string{"secret": secretMarker, "pad": strings.Repeat("r", limit)}, nil
			}
			return map[string]any{"ok": true, "method": method}, nil
		},
	}
	ctx, _, _ := runClient(t, c)
	if err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	firstPeer := c.Peer()

	// Exact limit line (including trailing '\n') passes end-to-end through bus.Conn and rpc.Peer.Serve.
	const eventPrefix = `{"event":"fleet.sessions","params":`
	const eventSuffix = "}\n"
	const bodyHead = `{"p":"`
	const bodyTail = `"}`
	exactPad := limit - len(eventPrefix) - len(eventSuffix) - len(bodyHead) - len(bodyTail)
	exactParams := rpc.Marshaled(bodyHead + strings.Repeat("x", exactPad) + bodyTail)
	if err := c.Notify("fleet.sessions", exactParams); err != nil {
		t.Fatalf("exact-limit event failed: %v", err)
	}
	select {
	case n := <-gotEvents:
		if n != len(exactParams) {
			t.Fatalf("received event params length = %d, want %d", n, len(exactParams))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exact-limit event did not arrive")
	}

	// One byte over the limit is rejected locally without touching the socket.
	overParams := rpc.Marshaled(bodyHead + secretMarker + strings.Repeat("x", exactPad-len(secretMarker)+1) + bodyTail)
	if err := c.Notify("fleet.sessions", overParams); !errors.Is(err, rpc.ErrMessageTooLarge) {
		t.Fatalf("over-limit event err = %v, want ErrMessageTooLarge", err)
	}

	// Oversized outgoing Call is rejected locally.
	if err := c.Call(ctx, "devices.push", map[string]string{"secret": secretMarker, "pad": strings.Repeat("q", limit)}, nil); !errors.Is(err, rpc.ErrMessageTooLarge) {
		t.Fatalf("over-limit Call err = %v, want ErrMessageTooLarge", err)
	}

	// Oversized handler reply from client -> server returns a bounded RPC error without dropping the bus.
	srvMu.Lock()
	sp := srvPeer
	srvMu.Unlock()
	err := sp.Call(ctx, "devices.big", nil, nil)
	var re *rpc.Error
	if !errors.As(err, &re) {
		t.Fatalf("over-limit reply err = %T %v, want *rpc.Error", err, err)
	}
	if !strings.Contains(re.Message, "devices.big") || strings.Contains(re.Message, secretMarker) {
		t.Fatalf("unexpected over-limit reply error: %q", re.Message)
	}

	// Normal calls in both directions continue on the exact same connection.
	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.Call(ctx, "ping", nil, &out); err != nil || !out.OK {
		t.Fatalf("client->server ping failed: out=%+v err=%v", out, err)
	}
	out.OK = false
	if err := sp.Call(ctx, "devices.ping", nil, &out); err != nil || !out.OK {
		t.Fatalf("server->client ping failed: out=%+v err=%v", out, err)
	}
	if c.Peer() != firstPeer || dials.Load() != 1 {
		t.Fatalf("connection did not stay intact: samePeer=%v dials=%d", c.Peer() == firstPeer, dials.Load())
	}
	joined := strings.Join(log.all(), "\n")
	for _, want := range []string{"fleet.sessions", "devices.push", "devices.big", strconv.Itoa(limit + 1)} {
		if !strings.Contains(joined, want) {
			t.Errorf("logs missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, secretMarker) {
		t.Fatalf("logs leaked secret payload content:\n%s", joined)
	}
}

// Conn.Write rejects a line larger than its limit before calling ws.Write, leaving the WebSocket usable.
func TestConnSendRejectsOverLimitWithoutClosing(t *testing.T) {
	ctx := context.Background()
	const limit = 512
	cws, sws := wsPair(t, false)
	sws.SetReadLimit(limit)
	c := newConn(ctx, cws, limit)

	exact := bytes.Repeat([]byte("a"), limit)
	if _, err := c.Write(append(bytes.Clone(exact), '\n')); err != nil {
		t.Fatalf("Write at exact limit failed: %v", err)
	}
	if _, got, err := sws.Read(ctx); err != nil || !bytes.Equal(got, exact) {
		t.Fatalf("read exact message: len=%d err=%v", len(got), err)
	}

	over := append(bytes.Repeat([]byte("b"), limit+1), '\n')
	if _, err := c.Write(over); !errors.Is(err, rpc.ErrMessageTooLarge) {
		t.Fatalf("Write over limit err = %v, want ErrMessageTooLarge", err)
	}

	// Connection stays open for subsequent messages.
	if _, err := c.Write([]byte("after\n")); err != nil {
		t.Fatalf("Write after rejection failed: %v", err)
	}
	if _, got, err := sws.Read(ctx); err != nil || string(got) != "after" {
		t.Fatalf("read after rejection = %q, %v", got, err)
	}
}
