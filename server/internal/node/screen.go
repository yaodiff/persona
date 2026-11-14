package node

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"

	"github.com/nwverse/persona/server/internal/rpc"
)

// Live-screen hardware video channel (#3954), node side.
//
// This file wires `screen.dial` and the login binding; starting the capture/encode helper (Persona Screen.app,
// ScreenCaptureKit + VideoToolbox) and bridging its Unix socket is `screenStart` (screenhelper.go on a Mac). Without
// the helper, `screen.dial` returns `video_unavailable`, so an old node or a node without it keeps RFB only.

// maxScreenStreams caps concurrent video streams per node (the design's `busy`).
const maxScreenStreams = 2

// screenLogins records, per `pair`, whether the paired RFB stream (port.dial of 5900) signed in to Screen Sharing.
// Video for a pair is allowed only while that RFB stream is open AND signed in.
type screenLogins struct {
	mu    sync.Mutex
	byID  map[string]*screenPair
	count int // open video streams, for the busy check
}

type screenPair struct {
	result rfbLoginResult
	open   bool        // the RFB (5900) stream is still open
	video  []io.Closer // the video streams bound to this pair: they end when its RFB stream does
}

func newScreenLogins() *screenLogins { return &screenLogins{byID: map[string]*screenPair{}} }

// observe starts following the handshake on a 5900 relay connection for `pair`. It returns a wrapped conn that tees
// both directions into the login observer; the caller uses the wrapped conn in place of the original. closing it ends
// the pair (video for that pair stops being allowed).
func (s *screenLogins) observe(pair string, c net.Conn) net.Conn {
	if pair == "" {
		return c
	}
	s.mu.Lock()
	s.byID[pair] = &screenPair{result: rfbLoginUnknown, open: true}
	s.mu.Unlock()

	server, client := newSyncBuf(1<<16), newSyncBuf(1<<16)
	go func() {
		res := followRFBLogin(server, client)
		s.mu.Lock()
		if st, ok := s.byID[pair]; ok {
			st.result = res
		}
		s.mu.Unlock()
		// Keep draining so the tee never blocks once the handshake is decided.
		go io.Copy(io.Discard, server)
		go io.Copy(io.Discard, client)
	}()
	return &rfbTeeConn{Conn: c, server: server, client: client, onClose: func() {
		server.close()
		client.close()
		s.mu.Lock()
		var video []io.Closer
		if st, ok := s.byID[pair]; ok {
			st.open = false
			video = st.video
		}
		delete(s.byID, pair)
		s.mu.Unlock()
		for _, v := range video { // video needs the signed-in screen-sharing stream: it ends with it
			_ = v.Close()
		}
	}}
}

// attach binds a video stream to pair, so it closes when the pair's screen-sharing stream does. It reports false, and
// binds nothing, when that stream is no longer open and signed in.
func (s *screenLogins) attach(pair string, video io.Closer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.byID[pair]
	if !ok || !st.open || st.result != rfbLoginSignedIn {
		return false
	}
	st.video = append(st.video, video)
	return true
}

// signedIn reports whether the pair's RFB stream is open and Screen Sharing accepted its login.
func (s *screenLogins) signedIn(pair string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.byID[pair]
	return ok && st.open && st.result == rfbLoginSignedIn
}

func (s *screenLogins) tryEnter() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count >= maxScreenStreams {
		return false
	}
	s.count++
	return true
}
func (s *screenLogins) leave() {
	s.mu.Lock()
	if s.count > 0 {
		s.count--
	}
	s.mu.Unlock()
}

// rfbTeeConn tees a relayed 5900 connection's two directions into the login observer without disturbing the relay:
// Read (Screen Sharing → phone) feeds the observer's server input, Write (phone → Screen Sharing) its client input.
type rfbTeeConn struct {
	net.Conn
	server, client *syncBuf
	onClose        func()
	closeOnce      sync.Once
}

func (t *rfbTeeConn) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	if n > 0 {
		_, _ = t.server.Write(p[:n])
	}
	return n, err
}

func (t *rfbTeeConn) Write(p []byte) (int, error) {
	if len(p) > 0 {
		_, _ = t.client.Write(p)
	}
	return t.Conn.Write(p)
}

func (t *rfbTeeConn) Close() error {
	t.closeOnce.Do(t.onClose)
	return t.Conn.Close()
}

// syncBuf is an unbounded-until-cap byte buffer a producer never blocks on and a consumer blocks reading from until
// data arrives or it is closed. Past its cap it closes itself (the RFB handshake is far under it; anything past the
// handshake the observer does not need).
type syncBuf struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
	cap    int
}

func newSyncBuf(cap int) *syncBuf {
	b := &syncBuf{cap: cap}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return len(p), nil
	}
	if len(b.buf)+len(p) > b.cap {
		b.closed = true
		b.cond.Broadcast()
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	b.cond.Broadcast()
	return len(p), nil
}

func (b *syncBuf) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.buf) == 0 && !b.closed {
		b.cond.Wait()
	}
	if len(b.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.buf)
	b.buf = b.buf[n:]
	return n, nil
}

func (b *syncBuf) close() {
	b.mu.Lock()
	b.closed = true
	b.cond.Broadcast()
	b.mu.Unlock()
}

// screenDial is screen.dial: start the live-screen video helper for a signed-in pair and join its socket to the
// stream `id`. Errors: video_unavailable (no helper or no Screen Recording grant), not_signed_in (the paired RFB
// stream has not passed Screen Sharing's SecurityResult), busy (two video streams already open).
func (n *Node) screenDial(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		ID        string `json:"id"`
		Pair      string `json:"pair"`
		KeepAwake bool   `json:"keep_awake"`
		// Link: the video joins the pair's screen session as a channel (#6398); Attach: a relay rejoining the
		// session's live video channel, as for port.dial.
		Link   bool `json:"link"`
		Attach bool `json:"attach"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ID == "" || p.Pair == "" {
		return nil, &rpc.Error{Code: "video_unavailable", Message: "screen.dial: need id and pair"}
	}
	link := p.Link && n.links != nil && n.links.Get(p.Pair) != nil
	if link && p.Attach {
		return n.reattachScreen(p.ID, p.Pair, true)
	}
	if n.screenStart == nil {
		return nil, &rpc.Error{Code: "video_unavailable", Message: "no live-screen video helper on this node"}
	}
	if !n.screens.signedIn(p.Pair) {
		return nil, &rpc.Error{Code: "not_signed_in", Message: "the paired screen-sharing stream has not signed in"}
	}
	if !n.screens.tryEnter() {
		return nil, &rpc.Error{Code: "busy", Message: "two live-screen video streams are already open"}
	}
	// screenStart owns the stream until it closes, then calls the done callback so the busy counter is released.
	linked, err := n.screenStart(ctx, p.ID, p.Pair, p.KeepAwake, link, n.screens.leave)
	if err != nil {
		n.screens.leave()
		return nil, err
	}
	if linked {
		return map[string]any{"link": 1}, nil
	}
	return map[string]any{}, nil
}
