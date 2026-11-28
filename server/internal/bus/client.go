package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/rpc"
)

// ErrOffline: the client has no connection to the hub that said its handshake right now.
var ErrOffline = errors.New("not connected to the hub")

// stableAfter: a connection that stayed up this long starts the backoff over when it drops.
const stableAfter = time.Minute

// A wait longer than probeOver asks the hub's health every probeEvery, each probe given up after probeEvery
// (Client.pause). Settings, not measurements: a laptop that wakes reconnects within about probeEvery of its hub
// answering again.
const (
	probeOver  = 3 * time.Second
	probeEvery = 2 * time.Second
)

// Client keeps a service's connection to the hub's bus up until Run's context ends: dial with the token, say the
// handshake, serve the hub's calls and events until the connection drops, dial again. Between tries it waits
// Backoff, doubling up to MaxBackoff; a rejected token (ErrRejected) waits MaxBackoff every time. A long wait ends
// early when the hub, out of reach as it began, answers again (pause): a laptop waking from sleep, its backoff long
// grown by the failed tries while it slept, is back within seconds of its network.
type Client struct {
	Hub        string       // the hub's base URL, http(s)://host:port
	Token      string       // the service's bus token
	HTTPClient *http.Client // optional: a TLS client pinned to the hub's key for its public port

	// HelloMethod is the handshake (manager.hello for a manager's token) and HelloParams what it says, asked afresh
	// on every connection. OnHello gets the hub's answer before the connection counts as up; an error from it drops
	// the connection.
	HelloMethod string
	HelloParams func() any
	OnHello     func(result json.RawMessage) error

	// OnRequest answers the hub's calls, OnEvent takes its one-way events (nil: calls fail, events are dropped).
	OnRequest rpc.Handler
	OnEvent   rpc.EventHandler
	// Cancels are the methods whose calls tell the hub when their caller stops waiting (rpc.Peer.Cancels).
	Cancels map[string]bool

	Logf func(format string, args ...any)
	// Zero values are the protocol's: 1 s doubling to a minute; a ping every 30 s, 20 s for its pong.
	Backoff, MaxBackoff    time.Duration
	PingEvery, PingTimeout time.Duration
	// StopWhenRevoked: Run returns ErrRevoked once the hub refuses the token itself, instead of trying it again every
	// MaxBackoff (a manager whose token only a new install code replaces).
	StopWhenRevoked bool
	// StopWhenRejected: with StopWhenRevoked, any refusal of the token (ErrRejected too: a hub from before revoked
	// tokens were told apart answers a plain 401) is taken for a revoke (the built-in manager's own service, which
	// must not run next to the child such a hub starts).
	StopWhenRejected bool
	// Uncompressed: offer the hub no compression (permessage-deflate) on the bus. For a service on the hub's own
	// machine, whose bytes never leave it: compressing them only costs CPU, and each end keeps a flate state with its
	// context (about 0.85 MB) and its largest compressed message. A hub only compresses when it is offered.
	Uncompressed bool

	// probeOver, probeEvery, maxMessage replace the constants of the same names (tests).
	probeOver, probeEvery time.Duration
	maxMessage            int

	mu   sync.Mutex
	peer *rpc.Peer     // the connection that said its handshake; nil between connections
	up   chan struct{} // closed while peer is set
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (c *Client) maxMessageBytes() int {
	if c.maxMessage > 0 {
		return c.maxMessage
	}
	return MaxMessage
}

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// Run keeps the connection up until ctx ends, and returns ctx's error (or ErrRevoked, with StopWhenRevoked).
func (c *Client) Run(ctx context.Context) error {
	if c.HelloMethod == "" {
		return errors.New("bus: the client has no HelloMethod")
	}
	first, most := orDefault(c.Backoff, time.Second), orDefault(c.MaxBackoff, time.Minute)
	delay := first
	reach := c.prober()
	for {
		began := time.Now()
		helloed, err := c.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if helloed && time.Since(began) >= stableAfter {
			delay = first
		}
		if c.StopWhenRevoked && errors.Is(err, ErrRevoked) {
			c.logf("bus: the hub refused this token: %v; not trying again", err)
			return err
		}
		if c.StopWhenRevoked && c.StopWhenRejected && errors.Is(err, ErrRejected) {
			c.logf("bus: the hub refused this token: %v; not trying again", err)
			return fmt.Errorf("%w (%v)", ErrRevoked, err)
		}
		wait, probe := delay, reach
		if errors.Is(err, ErrRejected) {
			wait, probe = most, nil // the hub answered, and said no: only someone fixing the token helps
		} else {
			delay = min(delay*2, most)
		}
		c.logf("bus: the connection to the hub ended: %v; again in %s", err, wait)
		if err := c.pause(ctx, wait, probe); err != nil {
			return err
		}
	}
}

// pause waits d before the next try, or ctx's end (its error). A wait longer than probeOver whose hub was out of
// reach as it began (reach: its health did not answer) ends as soon as the hub answers again, looking every
// probeEvery: the network, a tunnel to the hub or the hub itself came back, and the next try goes at once. A hub that
// answered as the wait began says the try failed past its health (a reset during the handshake, a bus that refuses
// for now): that wait runs to its end, so such a hub is still tried only as the backoff says, and sees one probe a
// wait. A nil reach waits d whole.
func (c *Client) pause(ctx context.Context, d time.Duration, reach func(context.Context, time.Duration) bool) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	var look <-chan time.Time
	every := orDefault(c.probeEvery, probeEvery)
	if reach != nil && d > orDefault(c.probeOver, probeOver) && !reach(ctx, every) {
		t := time.NewTicker(every)
		defer t.Stop()
		look = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case <-look:
			if reach(ctx, every) {
				c.logf("bus: the hub answers again; trying now")
				return nil
			}
		}
	}
}

// healthPath is the hub's one route without authentication (hub/hello.go): {"ok": true, …} while it runs.
const healthPath = "/api/v1/health"

// prober is how pause looks whether the hub answers: GET healthPath with the client's own HTTP client (the default
// one when it has none, as websocket.Dial), so the probe goes the bus's own way to the hub: pinned to its key,
// through a proxy, over an SSH forward. Something that answers for a hub it cannot reach (a forward whose tunnel
// stalled, a proxy whose upstream is gone) does not pass for it. Given up after within. nil for a hub URL that does
// not parse (each try says so).
func (c *Client) prober() func(ctx context.Context, within time.Duration) bool {
	u, err := healthURL(c.Hub)
	if err != nil {
		return nil
	}
	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return func(ctx context.Context, within time.Duration) bool {
		ctx, cancel := context.WithTimeout(ctx, within)
		defer cancel()
		return healthy(ctx, hc, u)
	}
}

// healthy says the hub answers its health at u: a 2xx whose body says "ok": true, as `personad hub doctor` reads it.
func healthy(ctx context.Context, hc *http.Client, u string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct {
		OK bool `json:"ok"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body)
	return resp.StatusCode/100 == 2 && err == nil && body.OK
}

// healthURL is healthPath under the hub's base URL (a reverse proxy's path kept), over http(s) for ws(s).
func healthURL(hub string) (string, error) {
	return HTTPURL(hub, healthPath)
}

// HTTPURL resolves a Hub path through the same reverse proxy as the bus.
func HTTPURL(hub, path string) (string, error) {
	if _, err := Endpoint(hub); err != nil {
		return "", err
	}
	base, err := url.Parse(strings.TrimRight(hub, "/") + "/")
	if err != nil {
		return "", err
	}
	switch base.Scheme {
	case "ws":
		base.Scheme = "http"
	case "wss":
		base.Scheme = "https"
	}
	return base.JoinPath(strings.TrimPrefix(path, "/")).String(), nil
}

// session is one connection: dial, handshake, serve until it drops. helloed: the handshake went through.
func (c *Client) session(ctx context.Context) (helloed bool, err error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	mode := websocket.CompressionContextTakeover
	if c.Uncompressed {
		mode = websocket.CompressionDisabled
	}
	ws, err := dial(sctx, c.Hub, c.Token, c.HTTPClient, mode)
	if err != nil {
		return false, err
	}
	defer ws.CloseNow()
	limit := c.maxMessageBytes()
	conn := newConn(sctx, ws, int64(limit))
	go func() {
		err := Keepalive(sctx, ws, orDefault(c.PingEvery, PingEvery), orDefault(c.PingTimeout, PingTimeout))
		if sctx.Err() == nil {
			c.logf("bus: %v: hanging up", err)
			_ = ws.CloseNow()
		}
	}()
	p := rpc.New("s", conn)
	p.ReadLimit = limit
	p.WriteLimit = limit
	p.OnWriteRejected = func(kind, op string, size, max int) {
		c.logf("bus: rejected outgoing %s %s: %d bytes (limit %d)", kind, op, size, max)
	}
	p.OnRequest, p.OnEvent, p.Cancels = c.OnRequest, c.OnEvent, c.Cancels
	served := make(chan error, 1)
	go func() { served <- p.Serve(sctx, conn) }()

	var params any = map[string]any{}
	if c.HelloParams != nil {
		params = c.HelloParams()
	}
	var res json.RawMessage
	hctx, hcancel := context.WithTimeout(sctx, HelloTimeout)
	err = p.Call(hctx, c.HelloMethod, params, &res)
	if err != nil {
		err = helloError(err, p, served, sctx, hctx)
		hcancel()
		return false, fmt.Errorf("%s: %w", c.HelloMethod, err)
	}
	hcancel()
	if c.OnHello != nil {
		if err := c.OnHello(res); err != nil {
			return false, err
		}
	}
	c.setPeer(p)
	defer c.setPeer(nil)
	select {
	case err := <-served:
		return true, closeReason(err)
	case <-p.Done():
		// Closed (rpc.Peer.Close): by its own read loop as it ended, or by the service to have everything read
		// again on a new connection. The socket goes with it (ws.CloseNow above): a closed peer left on an open
		// socket answers nothing, and nothing would dial again until the hub hung up. Close takes a nil error too
		// (an orderly close): it still ends the connection, and Run hears why rather than nil.
		err := p.Err()
		if err == nil {
			err = rpc.ErrClosed
		}
		return true, closeReason(err)
	}
}

// helloError translates a handshake failure into ErrRejected / ErrRevoked when the hub refused the
// identity or closed the WebSocket with CloseRevoked / CloseRefused during HelloMethod.
func helloError(err error, p *rpc.Peer, served <-chan error, sctx, hctx context.Context) error {
	var coded *rpc.Error
	if errors.As(err, &coded) {
		if coded.Code == CodeWrongIdentity {
			return fmt.Errorf("%w: %s", ErrRejected, coded.Message)
		}
		return err
	}
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return closeReason(err)
	}
	if perr := p.Err(); perr != nil {
		return closeReason(perr)
	}
	if sctx.Err() != nil || hctx.Err() != nil {
		return err
	}
	// If ws.Write failed with a raw transport error right as the hub sent a Close frame, give Serve
	// a brief window to surface the peer's CloseError before falling back to the write error.
	t := time.NewTimer(50 * time.Millisecond)
	defer t.Stop()
	select {
	case serr := <-served:
		if serr != nil {
			return closeReason(serr)
		}
	case <-t.C:
	case <-hctx.Done():
	case <-sctx.Done():
	}
	return err
}

// closeReason says why the hub closed the connection: a revoked token or a refused handshake is ErrRejected.
func closeReason(err error) error {
	var ce websocket.CloseError
	if !errors.As(err, &ce) {
		return err
	}
	switch ce.Code {
	case CloseRevoked:
		return fmt.Errorf("%w: %s", ErrRevoked, ce.Reason)
	case CloseRefused:
		return fmt.Errorf("%w: %s", ErrRejected, ce.Reason)
	case CloseReplaced:
		return fmt.Errorf("replaced by a newer connection: %s", ce.Reason)
	}
	return err
}

func (c *Client) setPeer(p *rpc.Peer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.up == nil {
		c.up = make(chan struct{})
	}
	switch {
	case p != nil && c.peer == nil:
		close(c.up)
	case p == nil && c.peer != nil:
		c.up = make(chan struct{})
	}
	c.peer = p
}

// Peer is the connection that said its handshake, nil while there is none.
func (c *Client) Peer() *rpc.Peer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peer
}

// Wait returns once the client is connected and its handshake went through, or when ctx ends.
func (c *Client) Wait(ctx context.Context) error {
	c.mu.Lock()
	if c.up == nil {
		c.up = make(chan struct{})
	}
	up := c.up
	c.mu.Unlock()
	select {
	case <-up:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Call calls a hub method on the current connection (ErrOffline while there is none).
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	p := c.Peer()
	if p == nil {
		return ErrOffline
	}
	return p.Call(ctx, method, params, out)
}

// Notify sends the hub a one-way event on the current connection (ErrOffline while there is none).
func (c *Client) Notify(event string, params any) error {
	p := c.Peer()
	if p == nil {
		return ErrOffline
	}
	return p.Notify(event, params)
}
