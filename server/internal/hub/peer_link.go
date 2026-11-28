package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/streams"
)

// A link between two paired hubs (docs/server/mesh.md "Mesh links"): a WebSocket on the TLS port of the hub that was dialed,
// pinned to its key, with the credential it issued. Either hub may dial; of two links between the same hubs the one
// dialed by the smaller hub_id stays. On it runs the node protocol's RPC (internal/rpc): records one way (rec), calls
// both ways (hello, relay, stream.open, unpair), and streams (internal/streams) carrying HTTP between the hubs: the
// apps' requests a member passes on to the center, the node streams the center opens through a member (peer_http.go,
// peer_node.go). Heartbeats and reconnecting follow persona-link (link.PingEvery, link.PingTimeout).

const (
	peerPath     = "/api/v1/peer"
	peerPairPath = "/api/v1/peer/pair"
)

// Close codes on a peer link besides the standard ones (1001: the hub is stopping).
const (
	peerCloseDuplicate websocket.StatusCode = 4011 // the two hubs have another link: this one is not kept
	peerCloseUnpaired  websocket.StatusCode = 4012 // the other hub was unpaired (its credential revoked)
	peerCloseSlow      websocket.StatusCode = 4013 // the link could not keep up
)

// The links registerLink refuses: the mesh is stopping, the other hub is no longer paired (or its credential was
// revoked or rotated during hello), or the two hubs already have a link that stays.
var (
	errLinkStopping  = errors.New("hub stopping")
	errLinkUnpaired  = errors.New("peer unpaired or credential revoked")
	errDuplicateLink = errors.New("the two hubs already have a link")
)

type peerLink struct {
	h       *Hub
	hubID   string // the other hub
	dialer  string // the hub that dialed this link
	address string // where it was dialed to, or (accepted) the masked source
	tokenID string // accepted links: the credential the other hub came with
	conn    *websocket.Conn
	rpc     *rpc.Peer
	mux     *streams.Mux
	out     chan peerOut
	ctx     context.Context
	cancel  context.CancelCauseFunc
	since   time.Time
	rtt     atomic.Int64 // the last ping round trip, ns
	every   time.Duration
	timeout time.Duration
	info    peerHello // what the other hub said about itself

	streamN   atomic.Int64
	transport *http.Transport
	proxy     *httputil.ReverseProxy
	closeOnce sync.Once
}

type peerOut struct {
	event  string
	params any
}

// peerHello is the first call each way on a link.
type peerHello struct {
	HubID   string `json:"hub_id"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Epoch   int64  `json:"epoch"`
}

func (h *Hub) peerHello() peerHello {
	ms := &h.mesh
	ms.mu.Lock()
	epoch := ms.epoch
	ms.mu.Unlock()
	return peerHello{HubID: h.hubID(), Name: h.hubName(), Version: h.version, Epoch: epoch}
}

func (l *peerLink) rttMS() int {
	if d := l.rtt.Load(); d > 0 {
		return max(1, int(time.Duration(d).Milliseconds()))
	}
	return 0
}

// send queues a one-way message; a link that falls this far behind is dropped (the other hub resyncs on reconnect).
func (l *peerLink) send(event string, params any) {
	select {
	case l.out <- peerOut{event, params}:
	default:
		l.close(peerCloseSlow, "peer link too slow")
	}
}

func (l *peerLink) writeLoop() {
	for {
		select {
		case <-l.ctx.Done():
			return
		case o := <-l.out:
			if err := l.rpc.Notify(o.event, o.params); err != nil {
				l.cancel(fmt.Errorf("write: %w", err))
				_ = l.conn.CloseNow()
				return
			}
		}
	}
}

// close ends the link with a close frame saying why.
func (l *peerLink) close(code websocket.StatusCode, reason string) {
	l.closeOnce.Do(func() {
		l.cancel(errors.New(reason))
		go func() { _ = l.conn.Close(code, reason) }()
	})
}

func (l *peerLink) keepAlive() {
	t := time.NewTicker(l.every)
	defer t.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), l.timeout) // not l.ctx: see link.go send
			start := time.Now()
			err := l.conn.Ping(ctx)
			cancel()
			if err != nil {
				if l.ctx.Err() == nil {
					l.cancel(fmt.Errorf("no pong within %s", l.timeout))
					_ = l.conn.CloseNow()
				}
				return
			}
			l.rtt.Store(int64(time.Since(start)))
		}
	}
}

// runPeerLink serves one link until it ends, and says why.
func (h *Hub) runPeerLink(parent context.Context, conn *websocket.Conn, hubID, dialer, address, tokenID string) error {
	conn.SetReadLimit(64 << 20)
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	// The byte stream is not bound to ctx: a link closed with a reason must still get its close frame out.
	nctx, ncancel := context.WithCancel(context.Background())
	defer ncancel()
	l := &peerLink{h: h, hubID: hubID, dialer: dialer, address: address, tokenID: tokenID, conn: conn,
		out: make(chan peerOut, 8192), ctx: ctx, cancel: cancel, since: time.Now().UTC(),
		every: link.PingEvery, timeout: link.PingTimeout}
	ms := &h.mesh
	ms.mu.Lock()
	if ms.ping[0] > 0 {
		l.every, l.timeout = ms.ping[0], ms.ping[1]
	}
	ms.mu.Unlock()
	nc := websocket.NetConn(nctx, conn, websocket.MessageBinary)
	l.rpc = rpc.New("p", nc)
	l.mux = streams.New(l.rpc.Notify)
	defer l.mux.CloseAll()
	l.rpc.OnEvent = func(event string, params json.RawMessage) {
		if !l.mux.Event(event, params) {
			h.peerEvent(l, event, params)
		}
	}
	l.rpc.OnRequest = func(rctx context.Context, method string, params json.RawMessage) (any, error) {
		return h.peerRequest(rctx, l, method, params)
	}
	served := make(chan error, 1)
	go func() { served <- l.rpc.Serve(ctx, nc) }()

	hctx, hcancel := context.WithTimeout(ctx, 10*time.Second)
	err := l.rpc.Call(hctx, "hello", h.peerHello(), &l.info)
	hcancel()
	if err == nil && l.info.HubID != hubID {
		err = fmt.Errorf("the hub there is %q, not %q", l.info.HubID, hubID)
	}
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "hello failed")
		return fmt.Errorf("hello: %w", err)
	}
	h.setupPeerHTTP(l) // before anyone can find the link
	if err := h.registerLink(l); err != nil {
		if l.transport != nil {
			l.transport.CloseIdleConnections()
		}
		switch {
		case errors.Is(err, errLinkStopping):
			_ = conn.Close(websocket.StatusGoingAway, "hub stopping")
		case errors.Is(err, errLinkUnpaired):
			_ = conn.Close(peerCloseUnpaired, "credential revoked")
		default:
			_ = conn.Close(peerCloseDuplicate, "the two hubs already have a link")
		}
		return err
	}
	defer h.unregisterLink(l)
	go l.writeLoop()
	go l.keepAlive()
	h.logf("mesh: link up with %q (%s), dialed by %s, via %s", l.info.Name, hubID, shortID(dialer), address)
	h.linkUp(l)
	select {
	case err = <-served:
	case <-l.ctx.Done():
		err = context.Cause(l.ctx)
		if parent.Err() != nil {
			l.close(websocket.StatusGoingAway, "hub stopping")
		}
		select { // the other side's close frame, or the end of the stream
		case <-served:
		case <-time.After(5 * time.Second):
		}
	}
	_ = conn.CloseNow()
	if errors.Is(err, context.Canceled) {
		err = errors.New("link closed")
	}
	h.logf("mesh: link down with %q (%s): %v", l.info.Name, hubID, err)
	return err
}

// registerLink makes l the link to its hub, unless the mesh is stopping (errLinkStopping), the peer was unpaired or its
// credential rotated/revoked during hello (errLinkUnpaired), or the two hubs already have another link that stays
// (errDuplicateLink): the one dialed by the smaller hub_id stays; a newer link dialed by the same hub replaces the
// older (it went away unnoticed).
func (h *Hub) registerLink(l *peerLink) error {
	self := h.hubID()
	preferred := min(self, l.hubID)
	ms := &h.mesh
	ms.mu.Lock()
	if !ms.running {
		ms.mu.Unlock()
		return errLinkStopping
	}
	peer, paired := ms.peers[l.hubID]
	if !paired || (peer.CredentialID != "" && h.isDeviceRevoked(peer.CredentialID)) || (l.tokenID != "" && ((peer.CredentialID != "" && peer.CredentialID != l.tokenID) || h.isDeviceRevoked(l.tokenID))) {
		ms.mu.Unlock()
		return errLinkUnpaired
	}
	old := ms.links[l.hubID]
	if old != nil && old.ctx.Err() == nil && old.dialer != l.dialer && l.dialer != preferred {
		ms.mu.Unlock()
		return errDuplicateLink
	}
	if ms.links == nil {
		ms.links = map[string]*peerLink{}
	}
	ms.links[l.hubID] = l
	ms.mu.Unlock()
	if old != nil {
		old.close(peerCloseDuplicate, "replaced by another link between the two hubs")
	}
	return nil
}

func (h *Hub) unregisterLink(l *peerLink) {
	ms := &h.mesh
	ms.mu.Lock()
	gone := ms.links[l.hubID] == l
	if gone {
		delete(ms.links, l.hubID)
	}
	ms.mu.Unlock()
	l.cancel(errors.New("link closed"))
	if l.transport != nil {
		l.transport.CloseIdleConnections()
	}
	if gone {
		h.meshTouch()
		h.meshRebuild()
	}
}

// linkUp sends a new link every record this hub holds, and has the picture worked out again.
func (h *Hub) linkUp(l *peerLink) {
	ms := &h.mesh
	ms.mu.Lock()
	recs := make([]*hubRecord, 0, len(ms.records))
	for id, r := range ms.records {
		if time.Since(ms.seenAt[id]) < meshRecordKeep { // a record gone stale here must not come back to life there
			recs = append(recs, r)
		}
	}
	ms.mu.Unlock()
	h.meshPublish(true) // the new neighbor, at once
	for _, r := range recs {
		if r.HubID != h.hubID() {
			l.send("rec", r)
		}
	}
	if ps := h.peerStore(); ps != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = ps.UpdatePeerSeen(ctx, l.hubID, l.info.Name, h.peerEndpoints(l.hubID))
		}()
	}
}

// peerLinkTo is the live link to a direct peer.
func (h *Hub) peerLinkTo(hubID string) *peerLink {
	ms := &h.mesh
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if l := ms.links[hubID]; l != nil && l.ctx.Err() == nil {
		return l
	}
	return nil
}

// nextLink is the link to send something for hubID through.
func (h *Hub) nextLink(hubID string) *peerLink {
	next := h.meshView().next(hubID)
	if next == "" {
		return nil
	}
	return h.peerLinkTo(next)
}

func (h *Hub) peerRequest(ctx context.Context, l *peerLink, method string, params json.RawMessage) (any, error) {
	switch method {
	case "hello":
		return h.peerHello(), nil
	case "stream.open":
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
			return nil, errors.New("stream.open needs an id")
		}
		return map[string]any{}, h.acceptPeerStream(l, p.ID)
	case "relay":
		var req meshRelayReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return h.meshRelay(ctx, l, req)
	case "unpair":
		go h.peerUnpaired(l.hubID)
		return map[string]any{}, nil
	}
	return nil, fmt.Errorf("unknown method %s", method)
}

func (h *Hub) peerEvent(l *peerLink, event string, params json.RawMessage) {
	switch event {
	case "rec":
		var rec hubRecord
		if json.Unmarshal(params, &rec) == nil {
			h.meshRecord(l, &rec)
		}
	}
}

// stopPeerLinks is part of the shutdown: every link gets a 1001, the dialers stop.
func (h *Hub) stopPeerLinks() {
	h.meshPersist(true) // the records keep the members' computers known across the restart
	ms := &h.mesh
	ms.mu.Lock()
	links := ms.linkList()
	dialers := ms.dialers
	ms.dialers = map[string]*peerDialer{}
	ms.running = false
	ms.mu.Unlock()
	for _, d := range dialers {
		d.cancel()
	}
	for _, l := range links {
		l.close(websocket.StatusGoingAway, "hub stopping")
	}
}

// closePeerLinksOf closes the links that came with a revoked credential (an unpaired hub's).
func (h *Hub) closePeerLinksOf(tokenID string) int {
	ms := &h.mesh
	ms.mu.Lock()
	var gone []*peerLink
	for _, l := range ms.links {
		if l.tokenID != "" && l.tokenID == tokenID {
			gone = append(gone, l)
		}
	}
	ms.mu.Unlock()
	for _, l := range gone {
		l.close(peerCloseUnpaired, "credential revoked")
	}
	return len(gone)
}
