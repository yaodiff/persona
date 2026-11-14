package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/streams"
)

// echoVNC stands in for Screen Sharing: it greets, then sends back whatever it reads, so a test sees both
// directions of a stream through the hub.
func echoVNC(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = c.Write([]byte("RFB 003.889\n"))
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

// screenNode is a Mac's node for the live screen's two routes: port.dial and screen.dial both reach vnc. newNode
// answers a screen session asked for with {link: 1}, as a node with #6398 does; otherwise it answers {} as an older
// node. gone answers session_gone. seen gets each request's params.
func screenNode(t *testing.T, h *Hub, vnc string, newNode, gone bool, seen func(method string, params map[string]any)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide, nodeSide := rpc.New("h", h2nW), rpc.New("n", n2hW)
	hubMux, nodeMux := streams.New(hubSide.Notify), streams.New(nodeSide.Notify)
	hubSide.OnEvent = func(e string, p json.RawMessage) { hubMux.Event(e, p) }
	nodeSide.OnEvent = func(e string, p json.RawMessage) { nodeMux.Event(e, p) }
	nodeSide.OnRequest = func(_ context.Context, method string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		seen(method, p)
		if gone {
			return nil, &rpc.Error{Code: "session_gone", Message: "no session"}
		}
		id, _ := p["id"].(string)
		c, err := net.DialTimeout("tcp", vnc, 2*time.Second)
		if err != nil {
			return nil, err
		}
		if err := nodeMux.Open(id); err != nil {
			c.Close()
			return nil, err
		}
		nodeMux.Start(id, c)
		if newNode && p["link"] == true {
			return map[string]any{"link": 1}, nil
		}
		return map[string]any{}, nil
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = hubSide.Serve(ctx, n2hR) }()
	go func() { defer wg.Done(); _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); h2nW.Close(); n2hW.Close(); wg.Wait() })

	m := newMachine(h, MachineConfig{ID: "m1", Name: "mac"})
	m.peer, m.online, m.streams = hubSide, true, hubMux
	m.info = proto.NodeInfo{OS: "darwin"}
	h.byID["m1"] = m
	h.machines = append(h.machines, m)
	h.ext.report.Store(&extwire.Report{Machines: map[string]extwire.MachineExtras{"m1": {ScreenPort: true}}})
}

// The screen session (#6398) reaches only a phone that offers its subprotocol, through a node that answers for it.
// Everyone else gets today's stream byte for byte: the web's noVNC ("binary"), today's iPhone app (no subprotocol),
// a new app through an older node, and a request to rejoin a session from a client that didn't offer it. Both live
// screen routes, 5900 and video, work the same.
func TestOnlyAPhoneOfferingTheScreenLinkGetsIt(t *testing.T) {
	link := screenLinkSubprotocol
	for _, tc := range []struct {
		why       string
		route     string // "port" or "video"
		offer     []string
		attach    bool
		newNode   bool
		wantLink  bool // the dial asked the node for a session
		wantProto string
		off       bool // hub.json's screen_link_off
	}{
		{"the web's noVNC", "port", []string{"binary"}, false, true, false, "binary", false},
		{"today's iPhone app", "port", nil, false, true, false, "", false},
		{"a rejoin without the offer", "port", []string{"binary"}, true, true, false, "binary", false},
		{"a new app, an older node", "port", []string{link, "binary"}, false, false, true, "binary", false},
		{"a new app, a new node", "port", []string{link, "binary"}, false, true, true, link, false},
		{"a new app rejoining", "port", []string{link, "binary"}, true, true, true, link, false},
		{"video: today's app", "video", nil, false, true, false, "", false},
		{"video: a new app, an older node", "video", []string{link, "binary"}, false, false, true, "binary", false},
		{"video: a new app, a new node", "video", []string{link, "binary"}, false, true, true, link, false},
		{"the hub's switch off: a new app, a new node", "port", []string{link, "binary"}, false, true, false, "binary", true},
		{"the hub's switch off: video", "video", []string{link, "binary"}, false, true, false, "binary", true},
		{"the hub's switch off: a rejoin", "port", []string{link, "binary"}, true, true, false, "binary", true},
	} {
		t.Run(tc.why, func(t *testing.T) {
			h := newTestHub(t)
			h.cfg.ScreenLinkOff = tc.off
			h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
			tok := "jd_" + randomHex(24)
			_ = h.store.CreateDeviceToken(context.Background(), db.DeviceToken{ID: "dev", Name: "iPhone", Kind: "phone", Role: roleFull}, hashToken(tok))
			var mu sync.Mutex
			var asked map[string]any
			screenNode(t, h, echoVNC(t), tc.newNode, false, func(method string, p map[string]any) {
				mu.Lock()
				asked = p
				mu.Unlock()
			})
			srv := httptest.NewServer(h.routes())
			t.Cleanup(srv.Close)

			path := "/api/v1/machines/m1/ports/5900/connect"
			if tc.route == "video" {
				path = "/api/v1/machines/m1/screen/video/connect"
			}
			q := url.Values{"token": {tok}, "pair": {"K"}}
			if tc.attach {
				q.Set("attach", "1")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+path+"?"+q.Encode(),
				&websocket.DialOptions{Subprotocols: tc.offer})
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			if c.Subprotocol() != tc.wantProto {
				t.Fatalf("selected %q, want %q", c.Subprotocol(), tc.wantProto)
			}
			mu.Lock()
			_, gotLink := asked["link"]
			_, gotAttach := asked["attach"]
			mu.Unlock()
			if gotLink != tc.wantLink || gotAttach != (tc.wantLink && tc.attach) {
				t.Fatalf("the node was asked %v", asked)
			}
			if tc.wantProto == link {
				return // the node's session framing from here: internal/node and internal/screenlink test it
			}
			// The raw stream, both ways: Screen Sharing's greeting, then the phone's bytes echoed back unchanged.
			nc := websocket.NetConn(ctx, c, websocket.MessageBinary)
			up := bytes.Repeat([]byte{0x05, 0x00, 0x01, 0x02}, 5000)
			go nc.Write(up)
			want := append([]byte("RFB 003.889\n"), up...)
			got := make([]byte, len(want))
			if _, err := io.ReadFull(nc, got); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("the raw stream changed: %v", err)
			}
		})
	}
}

// A phone rejoining a session the node no longer has is told it's gone (410), on either route, and starts over.
func TestRejoiningAGoneSessionIs410(t *testing.T) {
	for _, path := range []string{"ports/5900/connect", "screen/video/connect"} {
		t.Run(path, func(t *testing.T) {
			h := newTestHub(t)
			h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
			tok := "jd_" + randomHex(24)
			_ = h.store.CreateDeviceToken(context.Background(), db.DeviceToken{ID: "dev", Name: "iPhone", Kind: "phone", Role: roleFull}, hashToken(tok))
			screenNode(t, h, echoVNC(t), true, true, func(string, map[string]any) {})
			srv := httptest.NewServer(h.routes())
			t.Cleanup(srv.Close)
			u := fmt.Sprintf("ws%s/api/v1/machines/m1/%s?token=%s&pair=K&attach=1", strings.TrimPrefix(srv.URL, "http"), path, url.QueryEscape(tok))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, res, err := websocket.Dial(ctx, u, &websocket.DialOptions{Subprotocols: []string{screenLinkSubprotocol}})
			if err == nil || res == nil || res.StatusCode != http.StatusGone {
				t.Fatalf("rejoining a gone session: %v %v", res, err)
			}
		})
	}
}
