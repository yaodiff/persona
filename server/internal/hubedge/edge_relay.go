//go:build unix

package hubedge

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/acceptloop"
	"github.com/nwverse/persona/server/internal/proxyproto"
)

func (e *edge) accept(ln net.Listener) {
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	acceptloop.Run(e.ctx, ln, e.acceptLogf, func(c net.Conn) { go e.relay(c, port) })
}

// relay waits for a slot that serves (a switch holds connections back meanwhile), dials its socket for port, says
// whom it relays (PROXY v2) and copies both ways.
func (e *edge) relay(c net.Conn, port string) {
	if e.waiting.Add(1) > maxWaiting {
		e.waiting.Add(-1)
		_ = c.Close()
		return
	}
	header := proxyproto.AppendV2(nil, addrPort(c.RemoteAddr()), addrPort(c.LocalAddr()))
	p, upstream := e.dialActive(port, header)
	e.waiting.Add(-1)
	if upstream == nil {
		_ = c.Close()
		return
	}
	r := &relay{client: c, up: upstream}
	go r.fromClient()
	r.toClient(func() net.Conn {
		// The slot answered nothing and let go: a connection still in its socket's queue when the edge released it
		// (a switch, the edge stopping). Once another slot serves, it gets what the client sent so far, as if it came
		// first. Not after a crash: the slot may have acted on a request it had not answered yet.
		if !p.released.Load() {
			return nil
		}
		np, nc := e.dialActive(port, header)
		p = np
		return nc
	})
}

// replayLimit is how much of what a client sends first is kept for another slot (relay.toClient): a request line,
// headers and a small body; past it, the connection is not replayed.
const replayLimit = 64 << 10

// halfCloseLinger is how long a client may go on sending once the slot half-closed the connection. net/http does that
// after an early answer to a request whose body it did not read (413, a 401 on an upload), then reads on for a moment
// so that the client's last bytes do not reset the connection and throw the answer away (its rstAvoidanceDelay). Past
// it the edge closes both ways: a client cannot hold the relay open.
const halfCloseLinger = 5 * time.Second

// relay is one relayed connection. Until the slot answers its first byte, what the client sends is kept, so that a
// connection the slot never took (it stopped with it still queued) goes to the next slot instead of failing.
type relay struct {
	client net.Conn
	linger time.Duration // after the slot's half-close (zero: halfCloseLinger)

	mu             sync.Mutex
	up             net.Conn
	kept           []byte
	stoppedKeeping bool // true once the slot answers its first byte or client bytes exceed replayLimit
	overflow       bool // more than replayLimit was sent before it: no replay
	closed         bool // true once closeAll has closed client and up
	clientEOF      bool
	once           sync.Once
	ended          atomic.Int32 // directions done: the second one closes both
}

func (r *relay) fromClient() {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.client.Read(buf)
		if n > 0 {
			r.mu.Lock()
			if !r.stoppedKeeping {
				if len(r.kept)+n > replayLimit {
					r.overflow, r.stoppedKeeping, r.kept = true, true, nil
				} else {
					r.kept = append(r.kept, buf[:n]...)
				}
			}
			up, keeping := r.up, !r.stoppedKeeping
			r.mu.Unlock()
			if _, werr := up.Write(buf[:n]); werr != nil && !keeping {
				r.closeAll()
				return
			}
		}
		if err != nil {
			r.mu.Lock()
			r.clientEOF = true
			up := r.up
			r.mu.Unlock()
			halfClose(up)
			r.end()
			return
		}
	}
}

// toClient copies the slot's bytes to the client. When the slot ends before its first byte, next is asked for
// another upstream (nil: none) and gets what the client sent so far. When it half-closes after that, the client's side
// is half-closed too, and what the client still sends goes on to the slot for up to halfCloseLinger.
func (r *relay) toClient(next func() net.Conn) {
	buf := make([]byte, 32<<10)
	first := true
	for {
		r.mu.Lock()
		up := r.up
		r.mu.Unlock()
		n, err := up.Read(buf)
		if n > 0 {
			if first {
				first = false
				r.mu.Lock()
				r.stoppedKeeping, r.kept = true, nil
				r.mu.Unlock()
			}
			if _, werr := r.client.Write(buf[:n]); werr != nil {
				r.closeAll()
				return
			}
		}
		if err == nil {
			continue
		}
		if !first && errors.Is(err, io.EOF) {
			linger := r.linger
			if linger <= 0 {
				linger = halfCloseLinger
			}
			halfClose(r.client)
			_ = r.client.SetReadDeadline(time.Now().Add(linger)) // fromClient ends then
			_ = up.SetWriteDeadline(time.Now().Add(linger))      // also when the slot stopped reading
			r.end()
			return
		}
		if first {
			r.mu.Lock()
			replay := !r.closed && !r.overflow && replayable(r.kept)
			r.mu.Unlock()
			if replay {
				if nc := next(); nc != nil {
					r.mu.Lock()
					if r.closed || r.overflow || !replayable(r.kept) {
						r.mu.Unlock()
						_ = nc.Close()
						r.closeAll()
						return
					}
					_, werr := nc.Write(r.kept)
					if werr != nil {
						r.mu.Unlock()
						_ = nc.Close()
						r.closeAll()
						return
					}
					_ = r.up.Close()
					r.up = nc
					eof := r.clientEOF
					r.mu.Unlock()
					if eof {
						halfClose(nc)
					}
					continue
				}
			}
		}
		r.closeAll()
		return
	}
}

// replayable: what the client sent can go to another slot without anything happening twice. A released slot may
// have run a request it never answered (its stop cut the handler off), so only what cannot have done anything there
// is replayed: a TLS handshake the slot never answered (no handshake, no request), and a request that changes nothing
// (GET, HEAD, OPTIONS; a WebSocket upgrade is a GET). Nothing sent yet: nothing to repeat either.
func replayable(sent []byte) bool {
	if len(sent) == 0 || sent[0] == 0x16 { // a TLS handshake record
		return true
	}
	for _, m := range []string{"GET ", "HEAD ", "OPTIONS "} {
		if bytes.HasPrefix(sent, []byte(m)) {
			return true
		}
	}
	return false
}

// end is one direction done: once both are, the connection closes both ways.
func (r *relay) end() {
	if r.ended.Add(1) == 2 {
		r.closeAll()
	}
}

func (r *relay) closeAll() {
	r.once.Do(func() {
		_ = r.client.Close()
		r.mu.Lock()
		r.closed = true
		_ = r.up.Close()
		r.mu.Unlock()
	})
}

func halfClose(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	} else {
		_ = c.Close()
	}
}

// dialActive waits for a slot that serves (a switch holds connections back meanwhile), dials its socket for port
// and writes header; nil once HoldMax is over or the edge stops.
func (e *edge) dialActive(port string, header []byte) (*proc, net.Conn) {
	deadline := time.NewTimer(e.o.HoldMax)
	defer deadline.Stop()
	for {
		e.mu.Lock()
		held, gate, p := e.held, e.gate, e.active
		e.mu.Unlock()
		if held || p == nil {
			if e.onGateWait != nil {
				e.onGateWait()
			}
			select {
			case <-gate:
				continue
			case <-deadline.C:
				return nil, nil
			case <-e.ctx.Done():
				return nil, nil
			}
		}
		if e.ctx.Err() != nil {
			return nil, nil
		}
		path, ok := p.sockets[port]
		if !ok {
			return nil, nil
		}
		uc, err := net.DialTimeout("unix", path, 5*time.Second)
		if err == nil {
			if _, err = uc.Write(header); err == nil {
				return p, uc
			}
			_ = uc.Close()
		}
		// The slot is going away (a switch has not held connections back yet) or died: wait for the next one.
		retry := time.NewTimer(50 * time.Millisecond)
		select {
		case <-retry.C:
		case <-deadline.C:
			retry.Stop()
			return nil, nil
		case <-e.ctx.Done():
			retry.Stop()
			return nil, nil
		}
	}
}

func addrPort(a net.Addr) netip.AddrPort {
	if t, ok := a.(*net.TCPAddr); ok {
		return t.AddrPort()
	}
	return netip.AddrPort{}
}

func (e *edge) holdLocked() {
	if !e.held {
		e.gate, e.held = make(chan struct{}), true
	}
}

func (e *edge) unholdLocked() {
	if e.held && e.active != nil {
		close(e.gate)
		e.held = false
	}
}

// hold makes new connections wait (a switch, a slot that died).
func (e *edge) hold() {
	e.mu.Lock()
	e.holdLocked()
	e.mu.Unlock()
}

func (e *edge) setActive(p *proc) {
	e.mu.Lock()
	e.active = p
	if p == nil {
		e.holdLocked()
	} else {
		e.served = p.name
		e.unholdLocked()
	}
	e.mu.Unlock()
}

func (e *edge) current() *proc {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active
}
