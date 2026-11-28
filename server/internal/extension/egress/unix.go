package egress

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/nwverse/persona/server/internal/acceptloop"
	"github.com/nwverse/persona/server/internal/osproc"
)

// ListenUnix serves ext on a unix socket at path (made anew; only this user may connect): its sandbox's way out.
func (p *Proxy) ListenUnix(ext, path string) error { return p.listenUnix(ext, path, false) }

// Socketed says whether ext reaches the proxy through a socket of its own (ListenUnix: its sandbox's), not the loopback
// listener.
func (p *Proxy) Socketed(ext string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.unix[ext] != nil
}

// ListenRuntimeUnix additionally bounds pre-header sockets from source runtimes.
func (p *Proxy) ListenRuntimeUnix(ext, path string) error { return p.listenUnix(ext, path, true) }

func (p *Proxy) listenUnix(ext, path string, source bool) error {
	p.unixMu.Lock()
	defer p.unixMu.Unlock()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return net.ErrClosed
	}
	old := p.unix[ext]
	delete(p.unix, ext)
	p.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	_ = os.Remove(path)
	ln, err := osproc.Listen("unix", path)
	if err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = ln.Close()
		return net.ErrClosed
	}
	if source {
		ln = p.boundLocked(ln)
	}
	if p.unix == nil {
		p.unix = map[string]net.Listener{}
	}
	p.unix[ext] = ln
	p.mu.Unlock()
	go p.serve(ln, ext, source)
	return nil
}

// RelayUnix serves ext on a unix socket at path (made anew; only this user may connect) whose every connection is
// joined to the unix socket at target, dialed then (so a target made anew, a restarted node's, is still reached):
// a sandboxed agent runtime's way to this machine's node (#5413), and to nothing else. Unregister closes it.
// Each connection holds two descriptors and three goroutines here, so the socket is bounded (runtimeListener).
func (p *Proxy) RelayUnix(ext, path, target string) error {
	p.unixMu.Lock()
	defer p.unixMu.Unlock()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return net.ErrClosed
	}
	old := p.relay[ext]
	delete(p.relay, ext)
	p.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	_ = os.Remove(path)
	ln, err := osproc.Listen("unix", path)
	if err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = ln.Close()
		return net.ErrClosed
	}
	if p.relay == nil {
		p.relay = map[string]net.Listener{}
	}
	ln = p.boundLocked(ln)
	p.relay[ext] = ln
	p.mu.Unlock()
	go p.serveRelay(ln, ext, target)
	return nil
}

// boundLocked is ln bounded (runtimeListener), out of the proxy's shared runtimeSlots. p.mu held.
func (p *Proxy) boundLocked(ln net.Listener) *runtimeListener {
	if p.runtimeSlots == nil {
		p.runtimeSlots = make(chan struct{}, runtimeSlotsPerProxy)
	}
	return &runtimeListener{Listener: ln, slots: p.runtimeSlots, done: make(chan struct{}),
		connections: map[*runtimeConn]struct{}{}}
}

// serveRelay serves ln until it is closed (RelayUnix replacing it, Unregister, Close).
func (p *Proxy) serveRelay(ln net.Listener, ext, target string) {
	logf := func(format string, args ...any) { p.logf("egress: %s: %s", ext, fmt.Sprintf(format, args...)) }
	acceptloop.Run(context.Background(), ln, logf, func(c net.Conn) { go p.relayConn(c, ext, target) })
}

func (p *Proxy) relayConn(c net.Conn, ext, target string) {
	defer c.Close()
	var d net.Dialer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	up, err := d.DialContext(ctx, "unix", target)
	cancel()
	if err != nil {
		p.logf("egress: %s: %s: %v", ext, filepath.Base(target), err)
		return
	}
	defer up.Close()
	// Either end gone ends both: the protocol on it has no half-closed state.
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go pipe(up, c)
	go pipe(c, up)
	<-done
}
