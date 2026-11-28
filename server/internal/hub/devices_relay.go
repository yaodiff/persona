package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/link"
)

// The relay to the manager (devicewire.RelayPath): connections the hub holds for it — a Persona Link and the streams
// it opens (GET /api/v1/link, /api/v1/link/stream: the computer dials the hub's address, as before the move), the
// center's stream to one of this hub's computers (peer_node.go) — and the node streams the manager opens through
// another hub of the mesh. For each, the manager dials a WebSocket with its bus token and the hub copies messages both
// ways as they are (text: Persona Link's messages; binary: a byte stream); a close code and reason go through. The
// hub checks the credential, keeps the heartbeat with Persona Link (its side of the connection) and closes a link
// whose credential is revoked; what the link says is the manager's.

const kindLink = "link"

// relayWait is how long the hub waits for the manager to take a connection it holds for it.
const relayWait = 10 * time.Second

// linkManagerWait: how long a Persona Link (or a center's node stream) waits for the manager while the hub starts,
// short of Persona Link's handshake timeout (8 s a candidate).
const linkManagerWait = 5 * time.Second

// relayState is the connections waiting for the manager, the Persona Links relayed now, and every relay a manager
// has taken (cutRelays ends them when the machine moves or the manager goes).
type relayState struct {
	mu       sync.Mutex
	stopping bool // set by stopLinks under mu; prevents new links registering after shutdown snapshot
	pending  map[string]chan *relayTaken
	owner    map[string]relayFor // pending id → what it is for (only its manager may take it)
	links    map[*relayLink]struct{}
	taken    map[*relayTaken]struct{}
}

// relayFor is who a relay was handed to, and the machine it is for ("": an extension's request).
type relayFor struct{ manager, machine string }

// relayTaken is the manager's side of a relay, once it dialed; done closes when the hub is through with it. outside
// is the connection it is spliced to when that is Persona Link's (rs.mu): it is told to try again when the relay is
// cut, and lands at the machine's manager then.
type relayTaken struct {
	id      string // devices.accept's
	conn    *websocket.Conn
	done    chan struct{}
	handed  relayFor
	outside *websocket.Conn
}

// relayLink is a Persona Link connection (or one of its streams) the hub relays.
type relayLink struct {
	conn    *websocket.Conn // Persona Link's side
	tokenID string
	stream  bool
}

func (h *Hub) linkRoutes(api *http.ServeMux) {
	api.HandleFunc("GET "+link.Path, h.serveLink)
	api.HandleFunc("GET "+link.StreamPath, h.serveLinkStream)
}

// relayRoutes mounts the manager's relay channel beside the bus (its token, not a device's).
func (h *Hub) relayRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+devicewire.RelayPath, h.serveRelay)
}

// linkCaller checks a link request: a Persona Link credential, for a machine of kind link, over TLS (the hub's own, or
// the reverse proxy's in front of it: plainLinkAllowed).
func (h *Hub) linkCaller(w http.ResponseWriter, r *http.Request) (principal, bool) {
	p := principalOf(r.Context())
	if p.Role != roleLink {
		writeErr(w, http.StatusForbidden, errors.New("only a Persona Link credential opens a machine link"))
		return p, false
	}
	if r.TLS == nil && !h.plainLinkAllowed(r) {
		h.securityError(w, r, http.StatusForbidden, "tls_required", 0)
		return p, false
	}
	if mc := h.knownEntry(p.Machine); mc == nil || mc.Kind != kindLink {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no machine %q of kind link", p.Machine))
		return p, false
	}
	return p, true
}

// plainLinkAllowed: a link request that reached the hub without TLS is let in from this machine (a reverse proxy beside
// the hub), or from the LAN when it came through a reverse proxy that took it over HTTPS (X-Forwarded-Proto or
// Forwarded proto=https; docs/operations/link.md "Hub behind a reverse proxy"). The header is never trusted from the internet: that is
// refused before (gate) and here. A client lying in it only gets to send its own link credential in plaintext over
// the LAN; Persona Link itself only speaks HTTPS.
func (h *Hub) plainLinkAllowed(r *http.Request) bool {
	switch h.sourceOf(r) {
	case sourceLoopback:
		return true
	case sourceLAN:
		return forwardedHTTPS(r)
	}
	return false
}

// forwardedHTTPS: the proxy that passed r on says its client spoke HTTPS to it.
func forwardedHTTPS(r *http.Request) bool {
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		first, _, _ := strings.Cut(v, ",")
		return strings.EqualFold(strings.TrimSpace(first), "https")
	}
	for _, v := range r.Header.Values("Forwarded") {
		element, _, _ := strings.Cut(v, ",")
		for _, pair := range strings.Split(element, ";") {
			k, val, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "proto") {
				return strings.EqualFold(strings.Trim(strings.TrimSpace(val), `"`), "https")
			}
		}
	}
	return false
}

// linkPairable: a Persona Link can reach this hub, on its TLS port (pinned) or through the HTTPS reverse proxy
// public_url names (the hub has no TLS of its own; the link checks the proxy's certificate with the system's CAs).
func (h *Hub) linkPairable() bool {
	if port, _ := h.tlsPin(); port > 0 {
		return true
	}
	u, err := url.Parse(strings.TrimSpace(h.cfg.PublicURL))
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

// enterWS counts a WebSocket handler for the shutdown (stopWebSockets), unless the hub is already stopping.
func (h *Hub) enterWS() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.wsStopping {
		return false
	}
	h.wsHandlers.Add(1)
	return true
}

func (h *Hub) serveLink(w http.ResponseWriter, r *http.Request) {
	p, ok := h.linkCaller(w, r)
	if !ok {
		return
	}
	h.relayLinkConn(w, r, devicewire.Accept{Kind: devicewire.AcceptLink, MachineID: p.Machine, TokenID: p.ID, Source: maskedSource(r)},
		&websocket.AcceptOptions{InsecureSkipVerify: true}, 1<<20)
}

func (h *Hub) serveLinkStream(w http.ResponseWriter, r *http.Request) {
	p, ok := h.linkCaller(w, r)
	if !ok {
		return
	}
	// Compressed like the ssh connections (Compression=yes): node traffic is JSON. Each message on its own
	// (link.StreamCompression), also for a Persona Link that still asks for context takeover.
	h.relayLinkConn(w, r, devicewire.Accept{Kind: devicewire.AcceptLinkStream, MachineID: p.Machine, TokenID: p.ID,
		StreamID: r.URL.Query().Get("id")}, &websocket.AcceptOptions{InsecureSkipVerify: true,
		CompressionMode: link.StreamCompression}, 64<<20)
}

// relayLinkConn takes Persona Link's connection and hands it to the manager: 503 assistant_offline while there is none
// (Persona Link tries again).
func (h *Hub) relayLinkConn(w http.ResponseWriter, r *http.Request, a devicewire.Accept, opts *websocket.AcceptOptions, limit int64) {
	// Right after the hub started (its listeners come up before the managers do) an absent manager's link waits for
	// that manager a little, within what Persona Link gives a handshake. A connected non-carrier manager takes it as
	// before; the carrier keeps its existing wait for its first report.
	owner := h.machineOwner(a.MachineID)
	if (owner == devicesOwner || h.busConnOf(owner) == nil) && !h.awaitManagerFor(r.Context(), owner, linkManagerWait) {
		writeErrCode(w, http.StatusServiceUnavailable, "assistant_offline", errAssistantOffline)
		return
	}
	if !h.enterWS() {
		http.Error(w, "hub stopping", http.StatusServiceUnavailable)
		return
	}
	defer h.wsHandlers.Done()
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	conn.SetReadLimit(limit)
	l := &relayLink{conn: conn, tokenID: a.TokenID, stream: a.Kind == devicewire.AcceptLinkStream}
	rs := &h.relay
	rs.mu.Lock()
	if rs.stopping {
		rs.mu.Unlock()
		_ = conn.Close(websocket.StatusGoingAway, "hub stopping")
		return
	}
	if h.isDeviceRevoked(a.TokenID) {
		rs.mu.Unlock()
		_ = conn.Close(link.CloseRevoked, "credential revoked")
		return
	}
	if rs.links == nil {
		rs.links = map[*relayLink]struct{}{}
	}
	rs.links[l] = struct{}{}
	rs.mu.Unlock()
	defer func() {
		rs.mu.Lock()
		delete(rs.links, l)
		rs.mu.Unlock()
	}()
	taken, err := h.handToManager(r.Context(), a)
	if err != nil {
		_ = conn.Close(websocket.StatusTryAgainLater, clip(errAssistantOffline.Error(), 100))
		return
	}
	defer close(taken.done)
	rs.mu.Lock()
	taken.outside = conn
	rs.mu.Unlock()
	// Moved meanwhile (the move's cut ran before outside was set): to the new manager, from the start.
	if h.machineOwner(a.MachineID) != taken.handed.manager {
		_ = conn.Close(websocket.StatusTryAgainLater, relayMoved)
		_ = taken.conn.Close(websocket.StatusTryAgainLater, relayMoved)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	every, timeout, _ := h.linkPingTimes()
	var why atomic.Value // why the hub ended it, for the manager
	go func() {          // a link whose network went away quietly (a laptop gone to sleep): no pong
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, timeout)
				start := time.Now()
				err := conn.Ping(pctx)
				pcancel()
				if err != nil && ctx.Err() == nil {
					// With the error: a pong that never came, or a ping coder/websocket could not even send within its
					// own 5 s for control frames (behind a large message still being written). Clipped to a close
					// reason's 123 bytes.
					why.Store(clip(fmt.Sprintf("no pong within %s: %v", timeout, err), 120))
					_ = conn.CloseNow()
					return
				}
				if err == nil && !l.stream { // the link's round trip, for the manager's choice of paths
					_ = h.notifyBus(h.machineOwner(a.MachineID), devicewire.EventLinkRTT, devicewire.LinkRTT{ID: taken.id,
						RTTMS: max(1, int(time.Since(start).Milliseconds()))})
				}
			}
		}
	}()
	spliceWS(conn, taken.conn, func() string { s, _ := why.Load().(string); return s })
}

// handToManager has the manager take a connection the hub holds: it dials the relay with the id and the hub waits
// for it.
func (h *Hub) handToManager(ctx context.Context, a devicewire.Accept) (*relayTaken, error) {
	to := h.machineOwner(a.MachineID)
	return h.handToManagerVia(ctx, relayFor{manager: to, machine: a.MachineID}, func(ctx context.Context, id string) error {
		a.ID = id
		_, err := buswire.Call(ctx, h.managerCaller(to), devicewire.AcceptMethod, a)
		return err
	})
}

// handToManagerVia offers a relay and waits for the manager to take it after accept asks.
func (h *Hub) handToManagerVia(ctx context.Context, f relayFor, accept func(context.Context, string) error) (*relayTaken, error) {
	id, ch, drop := h.offerRelay(f)
	cctx, cancel := context.WithTimeout(ctx, relayWait)
	defer cancel()
	err := accept(cctx, id)
	if err == nil {
		select {
		case t := <-ch:
			return t, nil
		case <-cctx.Done():
			err = errors.New("the manager did not take the connection in time")
		}
	}
	drop()
	return nil, err
}

// offerRelay registers a relay for f.manager to take (serveRelay ?id=): its id, and where the connection comes once
// the manager dialed it (at most one). drop unregisters it; one the manager dialed just now, that nobody took from the
// channel, goes away (serveRelay delivers under rs.mu, and the close handshake is not waited for under it). After the
// connection was taken from the channel, drop changes nothing.
func (h *Hub) offerRelay(f relayFor) (id string, taken <-chan *relayTaken, drop func()) {
	rs := &h.relay
	id = randomHex(12)
	ch := make(chan *relayTaken, 1)
	rs.mu.Lock()
	if rs.pending == nil {
		rs.pending = map[string]chan *relayTaken{}
	}
	rs.pending[id] = ch
	if rs.owner == nil {
		rs.owner = map[string]relayFor{}
	}
	rs.owner[id] = f
	rs.mu.Unlock()
	return id, ch, func() {
		rs.mu.Lock()
		delete(rs.pending, id)
		delete(rs.owner, id)
		var late *relayTaken
		select {
		case late = <-ch:
		default:
		}
		rs.mu.Unlock()
		if late != nil {
			go func() {
				_ = late.conn.Close(websocket.StatusGoingAway, "not wanted any more")
				close(late.done)
			}()
		}
	}
}

// serveRelay is the manager's side of a relay: one the hub holds for it (?id=), or a node stream through another hub
// of the mesh (?hub=&machine=).
func (h *Hub) serveRelay(w http.ResponseWriter, r *http.Request) {
	ident, ok := h.busCaller(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	rs := &h.relay
	if ident.ID != h.relayOwner(q) {
		writeErr(w, http.StatusForbidden, errors.New("only the manager it is for relays it"))
		return
	}
	if !h.enterWS() {
		http.Error(w, "hub stopping", http.StatusServiceUnavailable)
		return
	}
	defer h.wsHandlers.Done()
	if hub := q.Get("hub"); hub != "" {
		h.relayThroughHub(w, r, hub, q.Get("machine"))
		return
	}
	id := q.Get("id")
	rs.mu.Lock()
	ch, f := rs.pending[id], rs.owner[id]
	rs.mu.Unlock()
	if ch == nil {
		writeErr(w, http.StatusNotFound, errors.New("no such relay"))
		return
	}
	if f.machine != "" && h.machineOwner(f.machine) != ident.ID {
		writeErr(w, http.StatusForbidden, errors.New("the machine is another manager's now"))
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(64 << 20)
	t := &relayTaken{id: id, conn: conn, done: make(chan struct{}), handed: f}
	rs.mu.Lock()
	// Still the machine's manager's, checked with the relay registered (cutRelays takes rs.mu after a move changed the
	// owner): a move either finds it here or is seen by this check.
	delivered := rs.pending[id] == ch && (f.machine == "" || h.machineOwner(f.machine) == f.manager)
	if delivered {
		delete(rs.pending, id)
		delete(rs.owner, id)
		if rs.taken == nil {
			rs.taken = map[*relayTaken]struct{}{}
		}
		rs.taken[t] = struct{}{}
		ch <- t // buffered: one delivery per id
	}
	rs.mu.Unlock()
	if !delivered {
		_ = conn.Close(websocket.StatusGoingAway, "not wanted any more")
		return
	}
	<-t.done
	h.letRelayGo(t)
}

// relayOwner is the manager a relay request (serveRelay's query) is for: the machine's for a node stream through
// another hub, the one it was handed to for a connection the hub holds (the carrier of hub.json's machines for an
// id it does not know: it answers 404).
func (h *Hub) relayOwner(q url.Values) string {
	if q.Get("hub") != "" {
		return h.machineOwner(q.Get("machine"))
	}
	rs := &h.relay
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if o, ok := rs.owner[q.Get("id")]; ok {
		return o.manager
	}
	return devicesOwner
}

// relayThroughHub opens a node stream through hub of the mesh for the manager and copies it to the relay.
func (h *Hub) relayThroughHub(w http.ResponseWriter, r *http.Request, hub, mid string) {
	octx, ocancel := context.WithCancel(context.Background())
	defer ocancel()
	pipe, err := h.openThroughHub(octx, hub, mid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	defer pipe.close()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(64 << 20)
	t := &relayTaken{conn: conn, handed: relayFor{manager: h.machineOwner(mid), machine: mid}}
	if !h.holdRelay(t) {
		_ = conn.Close(websocket.StatusTryAgainLater, relayMoved)
		return
	}
	defer h.letRelayGo(t)
	nc := websocket.NetConn(octx, conn, websocket.MessageBinary)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(pipe.w, nc); done <- struct{}{} }()
	go func() { _, _ = io.Copy(nc, pipe.r); done <- struct{}{} }()
	<-done
	pipe.close()
	_ = nc.Close()
	<-done
}

// spliceWS copies messages between a (the outside's) and b (the manager's) until either ends; the close code and
// reason of one go to the other. why is the hub's own reason when a went away without one (its heartbeat).
func spliceWS(a, b *websocket.Conn, why func() string) {
	done := make(chan struct{}, 2)
	pass := func(from, to *websocket.Conn) {
		defer func() { done <- struct{}{} }()
		for {
			typ, data, err := from.Read(context.Background())
			if err != nil {
				code, reason := websocket.CloseStatus(err), ""
				var ce websocket.CloseError
				if errors.As(err, &ce) {
					reason = ce.Reason
				}
				switch code {
				case -1, websocket.StatusNoStatusRcvd, websocket.StatusAbnormalClosure, websocket.StatusTLSHandshake:
					code, reason = websocket.StatusGoingAway, "the other side went away"
					if w := why(); w != "" && from == a {
						reason = w
					}
				}
				_ = to.Close(code, reason)
				return
			}
			wctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			err = to.Write(wctx, typ, data)
			cancel()
			if err != nil {
				_ = from.Close(websocket.StatusGoingAway, "the other side went away")
				return
			}
		}
	}
	go pass(a, b)
	go pass(b, a)
	<-done
	<-done
}

// relayMoved is why a relay is cut: its machine is another manager's now, or its manager is gone.
const relayMoved = "the machine's manager changed"

// holdRelay keeps t among the relays taken (cutRelays) if its machine is still its manager's; false: it is not, and
// t is not kept. Checked with t registered: a move's cut, which comes after the owner changed, finds it, or the check
// sees the new owner.
func (h *Hub) holdRelay(t *relayTaken) bool {
	rs := &h.relay
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if t.handed.machine != "" && h.machineOwner(t.handed.machine) != t.handed.manager {
		return false
	}
	if rs.taken == nil {
		rs.taken = map[*relayTaken]struct{}{}
	}
	rs.taken[t] = struct{}{}
	return true
}

// letRelayGo: the hub is through with t.
func (h *Hub) letRelayGo(t *relayTaken) {
	rs := &h.relay
	rs.mu.Lock()
	delete(rs.taken, t)
	rs.mu.Unlock()
}

// cutRelays ends the relays a manager may not keep: every one of manager gone's (gone != ""), and those for machine
// mid handed to another manager than its owner now (mid != ""). Persona Link is told to try again (1013) and lands
// at the machine's manager; the manager's side is closed the same way.
func (h *Hub) cutRelays(gone, mid string) {
	owner := ""
	if mid != "" {
		owner = h.machineOwner(mid)
	}
	rs := &h.relay
	rs.mu.Lock()
	var cut []*websocket.Conn
	for t := range rs.taken {
		if gone != "" && t.handed.manager == gone || mid != "" && t.handed.machine == mid && t.handed.manager != owner {
			if t.outside != nil {
				cut = append(cut, t.outside)
			}
			cut = append(cut, t.conn)
		}
	}
	rs.mu.Unlock()
	for _, c := range cut {
		go func() { _ = c.Close(websocket.StatusTryAgainLater, relayMoved) }()
	}
}

// closeLinks closes the links that came with a revoked credential.
func (h *Hub) closeLinks(tokenID string) int {
	rs := &h.relay
	rs.mu.Lock()
	var gone []*relayLink
	for l := range rs.links {
		if l.tokenID == tokenID {
			gone = append(gone, l)
		}
	}
	rs.mu.Unlock()
	n := 0
	for _, l := range gone {
		if !l.stream {
			n++
		}
		go func() { _ = l.conn.Close(link.CloseRevoked, "credential revoked") }()
	}
	return n
}

// stopLinks refuses new links and returns the open ones, for the shutdown to close (stopWebSockets):
// every link gets a 1001 so Persona Link comes back to the next hub quickly.
func (h *Hub) stopLinks() []*relayLink {
	rs := &h.relay
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.stopping = true
	out := make([]*relayLink, 0, len(rs.links))
	for l := range rs.links {
		out = append(out, l)
	}
	return out
}

// linkEndpoints is where the hub can be found, and what it is called and how many hubs its mesh has (the Mac menu
// bar shows both, docs/operations/link.md); the manager tells Persona Link.
func (h *Hub) linkEndpoints() link.Message {
	msg := link.Message{Type: "endpoints", HubName: h.hubName(), MeshHubs: h.meshHubCount()}
	for _, e := range h.linkEndpointList() {
		if e.Scheme == "https" {
			msg.Endpoints = append(msg.Endpoints, link.Endpoint{URL: e.URL, Kind: e.Kind})
		}
	}
	return msg
}

// sendLinkEndpoints: the hub's addresses or its mesh changed; the manager tells the links.
func (h *Hub) sendLinkEndpoints() { h.devicesConfigChanged() }
