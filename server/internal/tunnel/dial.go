package tunnel

import (
	"context"
	"net"
	"sync"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

type tunnelClient interface {
	DialTCPPort(ctx context.Context, port uint16) (net.Conn, error)
	path(ctx context.Context) (string, error)
	Close() error
}

type tailcatAdapter struct {
	*tailcat.Client
}

func (a tailcatAdapter) path(ctx context.Context) (string, error) {
	r, err := a.DiscoPing(ctx)
	if err != nil {
		return "", err
	}
	if r.Endpoint != "" {
		return "direct", nil
	}
	return "relay", nil
}

type dialEntry struct {
	c      tunnelClient
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	active int
}

// Dialer is the hub's side: one tailcat client per node address, all with the hub's key. A client brings its tunnel
// up on the first dial (about as long as an ssh login over the internet) and keeps it for the next ones.
type Dialer struct {
	Key        key.NodePrivate
	DERPMapURL string
	Logf       logger.Logf

	newClient func(addr tailcat.Addr) tunnelClient // tests only; nil constructs a tailcat.Client

	mu      sync.Mutex
	closed  bool
	clients map[tailcat.Addr]*dialEntry
}

func (d *Dialer) acquire(addr tailcat.Addr) (*dialEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, net.ErrClosed
	}
	e := d.clients[addr]
	if e == nil {
		var c tunnelClient
		if d.newClient != nil {
			c = d.newClient(addr)
		} else {
			c = tailcatAdapter{&tailcat.Client{Server: addr, Key: d.Key, DERPMapURL: d.DERPMapURL, Logf: Logf(d.Logf)}}
		}
		ctx, cancel := context.WithCancel(context.Background())
		e = &dialEntry{c: c, ctx: ctx, cancel: cancel}
		if d.clients == nil {
			d.clients = map[tailcat.Addr]*dialEntry{}
		}
		d.clients[addr] = e
	}
	e.active++
	return e, nil
}

// release decrements e's active usage count and reports whether e was reset or closed while in flight.
// If e closed while active, the last returning caller closes e.c exactly once after all callers exit,
// ensuring tailcat.Client.Close never races with lazy startup or consumes locoBackend.closeOnce early.
func (d *Dialer) release(e *dialEntry) bool {
	d.mu.Lock()
	e.active--
	closed := e.closed
	shouldClose := closed && e.active == 0
	d.mu.Unlock()
	if shouldClose {
		_ = e.c.Close()
	}
	return closed
}

func mergeCancel(parent, extra context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(extra, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// Dial opens a TCP connection to port on the node at addr.
func (d *Dialer) Dial(ctx context.Context, addr string, port uint16) (conn net.Conn, err error) {
	e, err := d.acquire(tailcat.Addr(addr))
	if err != nil {
		return nil, err
	}
	callCtx, cancel := mergeCancel(ctx, e.ctx)
	defer func() {
		cancel()
		if d.release(e) {
			if conn != nil {
				_ = conn.Close()
				conn = nil
			}
			if err == nil || ctx.Err() == nil {
				err = net.ErrClosed
			}
		}
	}()
	if e.ctx.Err() != nil {
		return nil, net.ErrClosed
	}
	return e.c.DialTCPPort(callCtx, port)
}

// Path reports how packets reach the node now: "direct" (NAT traversal worked) or "relay" (through DERP). Asking
// also nudges the client to look for a direct path again.
func (d *Dialer) Path(ctx context.Context, addr string) (path string, err error) {
	e, err := d.acquire(tailcat.Addr(addr))
	if err != nil {
		return "", err
	}
	callCtx, cancel := mergeCancel(ctx, e.ctx)
	defer func() {
		cancel()
		if d.release(e) {
			path = ""
			if err == nil || ctx.Err() == nil {
				err = net.ErrClosed
			}
		}
	}()
	if e.ctx.Err() != nil {
		return "", net.ErrClosed
	}
	return e.c.path(callCtx)
}

// Reset drops the client for addr: the next dial starts a new tunnel (after the node moved networks, or restarted
// and the old one went stale).
func (d *Dialer) Reset(addr string) {
	d.mu.Lock()
	e := d.clients[tailcat.Addr(addr)]
	closeNow := false
	if e != nil {
		e.closed = true
		closeNow = e.active == 0
		delete(d.clients, tailcat.Addr(addr))
	}
	d.mu.Unlock()
	if e != nil {
		e.cancel()
		if closeNow {
			_ = e.c.Close()
		}
	}
}

// Close ends every tunnel and prevents future Dial/Path calls from opening new ones.
func (d *Dialer) Close() {
	d.mu.Lock()
	d.closed = true
	es := d.clients
	d.clients = nil
	var toCancel []*dialEntry
	var toClose []*dialEntry
	for _, e := range es {
		e.closed = true
		toCancel = append(toCancel, e)
		if e.active == 0 {
			toClose = append(toClose, e)
		}
	}
	d.mu.Unlock()
	for _, e := range toCancel {
		e.cancel()
	}
	for _, e := range toClose {
		_ = e.c.Close()
	}
}
