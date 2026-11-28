package devices

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/tunnel"
)

// Reaching a machine (docs/server/server.md "tailcat hole punching"): ssh when the hub can, tailcat when it cannot. A machine with
// both (ssh_host and tailcat_addr) tries ssh first; when ssh fails, tailcat right after. While it runs over tailcat
// the hub checks ssh every few minutes and goes back to it once it works: ssh is the plain, well-known way, and
// tailcat may go through Tailscale's shared relays.

const (
	// directRetry: after ssh failed, reconnects go to tailcat first for this long instead of waiting on ssh again.
	directRetry = 5 * time.Minute
	// directProbeEvery: how often a machine on tailcat checks whether ssh works again; doubled for every time going
	// back to ssh failed right away (ssh logs in but the node does not answer over it), up to 32 times.
	directProbeEvery = 5 * time.Minute
	maxFlaps         = 5
	// tailcatCheckEvery: how often a connection over tailcat is pinged (its userspace TCP has no keepalive: a
	// machine gone to sleep or off the network would stay "online" until a write gave up, minutes later) and its
	// path (direct / relay) asked for, which also nudges NAT traversal. Two missed pings end the connection.
	tailcatCheckEvery = time.Minute
)

// tailcatTimings are a machine's (tests shorten them).
type tailcatTimings struct {
	directRetry, directProbeEvery, checkEvery time.Duration
	// now is the clock a connection's activity is told by (nil: time.Now); a test sets its own so how long a
	// connection was idle does not depend on how fast the machine running it is.
	now func() time.Time
}

var defaultTailcatTimings = tailcatTimings{directRetry: directRetry, directProbeEvery: directProbeEvery, checkEvery: tailcatCheckEvery}

// probeEvery is how long to wait before checking ssh again, longer after each failed switch back.
func (m *Machine) probeEvery() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.timings.directProbeEvery << min(m.flaps, maxFlaps)
}

// noteDirectFailed: ssh did not get to the node. Right after going back to it from tailcat, that is a flap.
func (m *Machine) noteDirectFailed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.directFailed = time.Now()
	if !m.switchedBack.IsZero() && time.Since(m.switchedBack) < 2*time.Minute {
		m.flaps = min(m.flaps+1, maxFlaps)
	}
	m.switchedBack = time.Time{}
}

// transports are the ways to try, in order. A link machine is only reached through its persona-link (link.go; tests
// give it a node command instead, run like ssh's).
func (m *Machine) transports() []string {
	switch {
	case m.cfg.Kind == "local":
		return []string{"local"}
	case m.cfg.Kind == kindLink && m.nodeCmd == nil:
		return []string{"link"}
	case m.cfg.Kind == kindService && m.nodeCmd == nil:
		return []string{kindService}
	case m.cfg.TailcatAddr == "":
		return []string{"ssh"}
	case m.cfg.Kind == "tailcat":
		return []string{"tailcat"}
	}
	m.mu.Lock()
	recent := !m.directFailed.IsZero() && time.Since(m.directFailed) < m.timings.directRetry
	m.mu.Unlock()
	if recent {
		return []string{"tailcat", "ssh"}
	}
	return []string{"ssh", "tailcat"}
}

// tailcatDialer is the manager's tailcat client, made the first time a machine needs it: its key file
// (Options.TailcatKey) is created then (`personad hub tailcat-key` prints the built-in manager's for the nodes). It
// is made again when the hub's DERP map changed since (a start from the manager's own list used the last hand-over's:
// ownlist.go); the tunnels through the old one end, and their machines connect again.
type tailcatDialer struct {
	mu     sync.Mutex
	d      *tunnel.Dialer
	url    string
	err    error
	closed bool
}

// errTailcatClosed: the manager is stopping.
var errTailcatClosed = errors.New("tailcat: the manager is stopping")

// errTailcatPrivate: the hub is in office network mode, where tailcat, which relays over the internet, is never dialed.
var errTailcatPrivate = errors.New("tailcat: the hub is in office network mode: connect this computer with Persona Link")

func (d *Devices) tailcatClient() (*tunnel.Dialer, error) {
	cfg := d.config()
	if cfg.PrivateNetwork {
		return nil, errTailcatPrivate
	}
	url := cfg.TailcatDERPMapURL
	t := &d.tailcat
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.closed:
		return nil, errTailcatClosed
	case t.err != nil:
		return nil, t.err
	case t.d != nil && t.url == url:
		return t.d, nil
	}
	path := d.opts.TailcatKey
	if path == "" {
		path = tunnel.HubKeyPath()
	}
	k, err := tunnel.LoadOrCreateHubKey(path)
	if err != nil {
		t.err = err
		return nil, err
	}
	if old := t.d; old != nil {
		d.logf("tailcat: the hub's DERP map changed: its tunnels start over")
		go old.Close()
	}
	t.d, t.url = &tunnel.Dialer{Key: k.Key, DERPMapURL: url, Logf: d.logf}, url
	return t.d, nil
}

func (m *Machine) tailcatDial(ctx context.Context) (net.Conn, error) {
	if m.dialTailcat != nil {
		return m.dialTailcat(ctx)
	}
	d, err := m.d.tailcatClient()
	if err != nil {
		return nil, err
	}
	c, err := d.Dial(ctx, m.cfg.TailcatAddr, tunnel.NodePort)
	if errors.Is(err, context.DeadlineExceeded) {
		// What a node not running, not set up for this hub's key, or not listening yet looks like: no answer at all.
		return nil, errors.New("tailcat 连不上：那台电脑的常驻 node 没在跑，或没有加这个 hub 的 key（personad node tailcat init）")
	}
	return c, err
}

// tailcatReset starts a new tunnel next time: the node may have moved networks or restarted.
func (m *Machine) tailcatReset() {
	if m.dialTailcat != nil {
		return
	}
	if d, _ := m.d.tailcatClient(); d != nil {
		d.Reset(m.cfg.TailcatAddr)
	}
}

// watchTailcat runs while a connection over tailcat lasts: it pings the node (ending a dead connection), keeps the
// path up to date, and goes back to ssh when that works again (only between interactions: a reconnect would cancel
// a question waiting on the phone).
//
// A connection that moved bytes lately is alive: a ping is only sent when it was idle for a whole check, and the
// watcher never waits on it longer than a check (the ping queues behind any big write: on a slow relay that takes a
// while, on a dead connection forever). Two idle checks in a row without an answer end the connection; closing it
// also fails the stuck write.
func (m *Machine) watchTailcat(ctx context.Context, c *nodeConn) {
	peer := c.peer
	check := time.NewTicker(m.timings.checkEvery)
	defer check.Stop()
	probeTick := time.NewTicker(m.probeEvery())
	defer probeTick.Stop()
	p, via, rev, _ := c.routeState()
	m.updatePath(ctx, p, rev)
	misses := 0
	for {
		p, via, rev, _ = c.routeState()
		if via != "tailcat" || p.src != m { // a resume moved the live connection off this watcher
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-check.C:
			activity := c.pipe().activity // a resume brings another (resume.go)
			if peer.Lost() || activity == nil || activity.idle() < m.timings.checkEvery || m.ping(ctx, peer) || activity.idle() < m.timings.checkEvery {
				misses = 0
				go m.updatePath(ctx, p, rev)
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if misses++; misses >= 2 {
				if peer.Lose(errors.New("tailcat: no answer to two pings")) { // the node keeps the connection (resume.go)
					m.d.logf("[%s] tailcat: no answer to two pings, resuming on a new transport", m.cfg.ID)
					misses = 0
					continue
				}
				m.d.logf("[%s] tailcat: no answer to two pings, reconnecting", m.cfg.ID)
				m.tailcatReset()
				peer.Close(errors.New("tailcat 连接没有回应（那台电脑睡眠或断网了？）"))
				if activity != nil {
					_ = activity.Close() // what fails a write stuck on the dead connection (connectVia may be in one)
				}
				return
			}
		case <-probeTick.C:
			if m.cfg.Kind == "tailcat" || m.machinePending() {
				continue
			}
			pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := m.directWorks(pctx)
			cancel()
			if err != nil {
				continue
			}
			if !c.claimRouteExit(rev) {
				return // the connection resumed onto another route while ssh was being probed
			}
			m.mu.Lock()
			m.directFailed, m.switchedBack = time.Time{}, time.Now()
			m.mu.Unlock()
			m.d.logf("[%s] ssh works again: switching back from tailcat", m.cfg.ID)
			peer.Close(errors.New("ssh works again: switching back from tailcat"))
			return
		}
	}
}

// ping asks the node for an answer within a check, without waiting on a ping stuck behind a write.
func (m *Machine) ping(ctx context.Context, peer *rpc.Peer) bool {
	pctx, cancel := context.WithTimeout(ctx, min(20*time.Second, m.timings.checkEvery))
	defer cancel()
	answer := make(chan error, 1)
	go func() { answer <- peer.Call(pctx, "ping", map[string]any{}, nil) }()
	select {
	case err := <-answer:
		var answered *rpc.Error // an old node without ping says "unknown method": it answered
		return err == nil || errors.As(err, &answered)
	case <-pctx.Done():
		return false
	}
}

// activityConn is a connection that notes when bytes last moved: a read returning data, or a piece of a write going
// out (writes are split so a big one on a slow link shows progress while it lasts).
type activityConn struct {
	net.Conn
	now  func() time.Time // nil: time.Now
	last atomic.Int64     // unix nanoseconds
}

const activityChunk = 32 << 10

func (c *activityConn) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *activityConn) touch() { c.last.Store(c.clock().UnixNano()) }

func (c *activityConn) idle() time.Duration { return c.clock().Sub(time.Unix(0, c.last.Load())) }

func (c *activityConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func (c *activityConn) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n, err := c.Conn.Write(p[:min(len(p), activityChunk)])
		written += n
		if n > 0 {
			c.touch()
		}
		if err != nil {
			return written, err
		}
		p = p[n:]
	}
	return written, nil
}

func (m *Machine) updatePath(ctx context.Context, p *nodePath, rev uint64) {
	if m.dialTailcat != nil {
		return
	}
	d, err := m.d.tailcatClient()
	if err != nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	path, err := d.Path(pctx, m.cfg.TailcatAddr)
	cancel()
	if err != nil {
		return
	}
	m.mu.Lock()
	changed := m.routeRev == rev && m.active == p && m.transport == "tailcat" && m.path != path
	if changed {
		m.path = path
	}
	m.mu.Unlock()
	if changed {
		m.d.logf("[%s] tailcat path: %s", m.cfg.ID, path)
		m.d.changed()
	}
}

// directWorks checks the hub can ssh to the machine, without starting a node.
func (m *Machine) directWorks(ctx context.Context) error {
	if m.probeDirect != nil {
		return m.probeDirect(ctx)
	}
	host := m.cfg.SSHHost
	if host == "" {
		host = m.cfg.ID
	}
	if err := validateSSHHost(host); err != nil {
		return err
	}
	return exec.CommandContext(ctx, "ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--", host, "true").Run()
}

// deployTailcat sends the hub's node build over tailcat: the node installs it over itself and exits, and its
// supervisor starts the new one.
func (m *Machine) deployTailcat(ctx context.Context, bin string) error {
	d, err := m.d.tailcatClient()
	if err != nil {
		return err
	}
	c, err := d.Dial(ctx, m.cfg.TailcatAddr, tunnel.UploadPort)
	if err != nil {
		return err
	}
	defer c.Close()
	return tunnel.Upload(ctx, c, bin)
}

// closeTailcat ends every tunnel when the hub stops.
func (d *Devices) closeTailcat() {
	t := &d.tailcat
	t.mu.Lock()
	t.closed = true // none made after this
	c := t.d
	t.mu.Unlock()
	if c != nil {
		c.Close()
	}
}
