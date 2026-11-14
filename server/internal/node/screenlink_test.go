package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/screenlink"
	"github.com/nwverse/persona/server/internal/streams"
)

// linkRig is a node with a stand-in for Screen Sharing and a hub-side stream Mux wired to its own, so a test holds
// the phone's end of each stream as the hub relays it.
type linkRig struct {
	t      *testing.T
	n      *Node
	hub    *streams.Mux
	vnc    net.Listener
	served chan net.Conn // Screen Sharing's end of each connection the node dialed
}

func newLinkRig(t *testing.T) *linkRig {
	t.Helper()
	vnc, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vnc.Close() })
	r := &linkRig{t: t, vnc: vnc, served: make(chan net.Conn, 8)}
	go func() {
		for {
			c, err := vnc.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
			r.served <- c
		}
	}()
	swap(t, &portDialer, func(ctx context.Context, port int, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", vnc.Addr().String())
	})
	var nodeMux *streams.Mux
	r.hub = streams.New(func(e string, p any) error {
		b, _ := json.Marshal(p)
		nodeMux.Event(e, b)
		return nil
	})
	nodeMux = streams.New(func(e string, p any) error {
		b, _ := json.Marshal(p)
		r.hub.Event(e, b)
		return nil
	})
	r.n = &Node{screens: newScreenLogins(), streams: nodeMux,
		links: screenlink.NewRegistry(screenlink.DefaultLimits, t.Logf)}
	return r
}

// dial is port.dial as the hub sends it; the phone's end of the stream comes back with the node's answer.
func (r *linkRig) dial(id string, params map[string]any) (net.Conn, map[string]any, error) {
	r.t.Helper()
	params["id"] = id
	if err := r.hub.Open(id); err != nil {
		r.t.Fatal(err)
	}
	body, _ := json.Marshal(params)
	res, err := r.n.portDial(context.Background(), body)
	if err != nil {
		r.hub.Abort(id, err)
		return nil, nil, err
	}
	phone, hubEnd := net.Pipe()
	r.t.Cleanup(func() { phone.Close() })
	r.hub.Start(id, hubEnd)
	out, _ := res.(map[string]any)
	return phone, out, nil
}

func (r *linkRig) screenSharing() net.Conn {
	r.t.Helper()
	select {
	case c := <-r.served:
		return c
	case <-time.After(2 * time.Second):
		r.t.Fatal("the node never dialed Screen Sharing")
	}
	return nil
}

func readExactly(t *testing.T, c net.Conn, n int) []byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	b := make([]byte, n)
	if _, err := io.ReadFull(c, b); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return b
}

// linkMessage reads one screen-session message off the phone's end.
func linkMessage(t *testing.T, c net.Conn) (byte, []byte) {
	t.Helper()
	head := readExactly(t, c, 4)
	b := readExactly(t, c, int(binary.BigEndian.Uint32(head)))
	return b[0], b[1:]
}

func linkFrame(typ byte, body []byte) []byte {
	b := make([]byte, 5, 5+len(body))
	binary.BigEndian.PutUint32(b, uint32(1+len(body)))
	b[4] = typ
	return append(b, body...)
}

// What every client without a screen session gets (the web's noVNC, today's apps, a hub that never asks): Screen
// Sharing's bytes on the stream exactly as they come, both ways, and an answer without link. The same for a link
// asked for on another port or without a pair.
func TestPortDialWithoutASessionIsTheRawStream(t *testing.T) {
	for _, tc := range []struct {
		why    string
		params map[string]any
	}{
		{"no link asked (old hub, web, old app)", map[string]any{"port": screenPort, "pair": "K"}},
		{"no pair", map[string]any{"port": screenPort, "link": true}},
		{"another port", map[string]any{"port": 8080, "pair": "K", "link": true}},
	} {
		t.Run(tc.why, func(t *testing.T) {
			r := newLinkRig(t)
			phone, res, err := r.dial("p1", tc.params)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := res["link"]; ok || r.n.links.Len() != 0 {
				t.Fatalf("answer %v: no session for this stream", res)
			}
			ss := r.screenSharing()
			down := bytes.Repeat([]byte("RFB 003.889\n"), 4000)
			go ss.Write(down)
			if got := readExactly(t, phone, len(down)); !bytes.Equal(got, down) {
				t.Fatal("the phone got other bytes than Screen Sharing sent")
			}
			up := bytes.Repeat([]byte{0x03, 0x01, 0, 0}, 3000)
			go phone.Write(up)
			if got := readExactly(t, ss, len(up)); !bytes.Equal(got, up) {
				t.Fatal("Screen Sharing got other bytes than the phone sent")
			}
		})
	}
}

// A phone that asked for a session: the answer says link, the stream starts with the session's paths, and Screen
// Sharing's bytes come as the RFB channel's data. A relay rejoining needs the live session and its token.
func TestPortDialWithLinkCarriesTheSession(t *testing.T) {
	r := newLinkRig(t)
	phone, res, err := r.dial("p1", map[string]any{"port": screenPort, "pair": "K", "link": true})
	if err != nil {
		t.Fatal(err)
	}
	if res["link"] != 1 {
		t.Fatalf("answer %v", res)
	}
	typ, body := linkMessage(t, phone)
	var paths screenlink.Paths
	if typ != screenlink.TypePaths || json.Unmarshal(body, &paths) != nil || paths.Token == "" {
		t.Fatalf("first message %d %q", typ, body)
	}
	ss := r.screenSharing()
	go ss.Write([]byte("RFB 003.889\n"))
	typ, body = linkMessage(t, phone)
	if typ != screenlink.TypeData || body[0] != screenlink.RFB || binary.BigEndian.Uint64(body[1:9]) != 0 || string(body[9:]) != "RFB 003.889\n" {
		t.Fatalf("data %d %q", typ, body)
	}
	// The login observer still follows Screen Sharing's own bytes: video binds to the pair as before.
	if _, ok := r.n.screens.byID["K"]; !ok {
		t.Fatal("the pair's login is no longer observed")
	}

	if _, _, err := r.dial("p2", map[string]any{"port": screenPort, "pair": "other", "link": true, "attach": true}); dialErrCode(t, err) != "session_gone" {
		t.Fatal("a relay rejoined a session that isn't there")
	}
	again, res, err := r.dial("p3", map[string]any{"port": screenPort, "pair": "K", "link": true, "attach": true})
	if err != nil || res["link"] != 1 {
		t.Fatalf("rejoining: %v %v", res, err)
	}
	tok, _ := json.Marshal(map[string]string{"token": paths.Token})
	go again.Write(linkFrame(screenlink.TypeAttach, tok))
	if typ, _ := linkMessage(t, again); typ != screenlink.TypeAttached {
		t.Fatalf("after the token: %d", typ)
	}
	select {
	case c := <-r.served:
		c.Close()
		t.Fatal("rejoining dialed Screen Sharing again")
	default:
	}
}

// screen.dial joins the pair's session only when the phone asked for it and the session is there; a relay rejoining
// video needs a live video channel.
func TestScreenDialJoinsTheSession(t *testing.T) {
	r := newLinkRig(t)
	if _, _, err := r.dial("p1", map[string]any{"port": screenPort, "pair": "K", "link": true}); err != nil {
		t.Fatal(err)
	}
	r.n.screens.mu.Lock()
	r.n.screens.byID["K"].result = rfbLoginSignedIn
	r.n.screens.mu.Unlock()
	var gotLink []bool
	r.n.screenStart = func(_ context.Context, _, _ string, _, link bool, _ func()) (bool, error) {
		gotLink = append(gotLink, link)
		return link, nil
	}
	for _, tc := range []struct {
		pair string
		link bool
		want bool
	}{{"K", true, true}, {"K", false, false}, {"absent", true, false}} {
		r.n.screens.mu.Lock()
		if r.n.screens.byID[tc.pair] == nil {
			r.n.screens.byID[tc.pair] = &screenPair{result: rfbLoginSignedIn, open: true}
		}
		r.n.screens.count = 0
		r.n.screens.mu.Unlock()
		body, _ := json.Marshal(map[string]any{"id": "v", "pair": tc.pair, "link": tc.link})
		res, err := r.n.screenDial(context.Background(), body)
		if err != nil {
			t.Fatal(err)
		}
		if got := res.(map[string]any)["link"] == 1; got != tc.want {
			t.Fatalf("pair %s link %v: answer %v", tc.pair, tc.link, res)
		}
	}
	if len(gotLink) != 3 || !gotLink[0] || gotLink[1] || gotLink[2] {
		t.Fatalf("screenStart was asked for link %v", gotLink)
	}
	body, _ := json.Marshal(map[string]any{"id": "v2", "pair": "K", "link": true, "attach": true})
	if _, err := r.n.screenDial(context.Background(), body); dialErrCode(t, err) != "session_gone" {
		t.Fatal("a video relay rejoined a session with no live video")
	}
}
