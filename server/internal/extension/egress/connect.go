package egress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	connectCacheMost = 1024
	connectHelloMost = 64 << 10
)

type connectDialKey struct{}

type connectKey struct {
	host, port string
	upstream   string // normalized HTTP proxy endpoint, empty for a direct route
}

// connectPlan retains only the addresses already checked for this request. A cache
// hit changes their order, never the permission check or the DNS answer itself.
type connectPlan struct {
	net.Conn
	key       connectKey
	target    string
	remaining []string
	attempt   dialAttempt
}

func (c *connectPlan) CloseWrite() error { return closeWrite(c.Conn) }

// dialResolved races checked addresses and carries the selected route into any TLS CONNECT retries.
func (p *Proxy) dialResolved(ctx context.Context, key connectKey, ips []net.IP, attempt dialAttempt) (net.Conn, error) {
	connect, _ := ctx.Value(connectDialKey{}).(bool)
	port := key.port
	if !connect || len(ips) < 2 {
		return dialRace(ctx, ips, port, attempt)
	}
	p.mu.Lock()
	last := p.lastGood[key]
	p.mu.Unlock()
	ordered := append([]net.IP(nil), ips...)
	for i, ip := range ordered {
		if net.JoinHostPort(ip.String(), port) == last {
			copy(ordered[1:i+1], ordered[:i])
			ordered[0] = ip
			break
		}
	}
	c, err := dialRace(ctx, ordered, port, func(ctx context.Context, target string) (net.Conn, error) {
		c, err := attempt(ctx, target)
		if err != nil {
			return nil, err
		}
		return &connectPlan{Conn: c, key: key, target: target, attempt: attempt}, nil
	})
	if err != nil {
		return nil, err
	}
	plan := c.(*connectPlan)
	seen := map[string]bool{plan.target: true}
	for _, ip := range interleave(ordered) {
		target := net.JoinHostPort(ip.String(), port)
		if !seen[target] {
			plan.remaining = append(plan.remaining, target)
			seen[target] = true
		}
	}
	return plan, nil
}

func (p *Proxy) rememberConnect(key connectKey, target string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastGood == nil {
		p.lastGood = make(map[connectKey]string)
	}
	if _, ok := p.lastGood[key]; !ok && len(p.lastGood) >= connectCacheMost {
		// This is only a hint: any entry can be evicted without changing reachability.
		for old := range p.lastGood {
			delete(p.lastGood, old)
			break
		}
	}
	p.lastGood[key] = target
}

type connectRead struct {
	data []byte
	err  error
	tls  bool
}

// clientHello buffers a complete ClientHello, including TLS record headers. TCP
// reads and TLS records can both split the handshake. Stop at its last record so
// TLS early application data is never part of a replay. Unrecognized, oversized
// or incomplete input is relayed unchanged, without retrying another address.
func clientHello(c net.Conn) (out connectRead) {
	read := func(n int) bool {
		start := len(out.data)
		out.data = append(out.data, make([]byte, n)...)
		got, err := io.ReadFull(c, out.data[start:])
		out.data = out.data[:start+got]
		out.err = err
		return err == nil
	}
	if !read(1) || out.data[0] != 0x16 {
		return out
	}
	var handshake []byte
	for {
		start := len(out.data) - 1
		if !read(4) {
			return out
		}
		header := out.data[start:]
		n := int(binary.BigEndian.Uint16(header[3:5]))
		if header[0] != 0x16 || header[1] != 3 || n == 0 || n > 16384 || len(out.data)+n > connectHelloMost {
			return out
		}
		if !read(n) {
			return out
		}
		handshake = append(handshake, out.data[start+5:]...)
		if len(handshake) >= 4 {
			size := 4 + int(handshake[1])<<16 + int(handshake[2])<<8 + int(handshake[3])
			if handshake[0] != 1 || size > connectHelloMost {
				return out
			}
			if len(handshake) >= size {
				// A record containing anything beyond ClientHello is not safe to replay.
				out.tls = len(handshake) == size
				return out
			}
		}
		if len(out.data)+5 > connectHelloMost || !read(1) {
			return out
		}
	}
}

func readConnectByte(c net.Conn) <-chan connectRead {
	done := make(chan connectRead, 1)
	go func() {
		b := make([]byte, 1)
		n, err := c.Read(b)
		done <- connectRead{data: b[:n], err: err}
	}()
	return done
}

func prependConnect(c net.Conn, b []byte) net.Conn {
	if len(b) == 0 {
		return c
	}
	return &bufferedConn{Conn: c, r: bufio.NewReader(io.MultiReader(bytes.NewReader(b), c))}
}

// prepareConnect watches both directions before deciding whether replay is safe.
// The server may speak first on any port (including 443); never hold its banner
// waiting for the client. An idle/slow client gets one second to finish its hello,
// then ordinary relay resumes with all bytes intact and no replay or new timeout.
// A plan is only needed for CONNECT tunnels with multiple checked addresses.
func (p *Proxy) prepareConnect(ctx context.Context, near net.Conn, plan *connectPlan) (net.Conn, net.Conn, error) {
	wait, budget := p.connectWait, p.connectBudget
	if wait <= 0 {
		wait = time.Second
	}
	if budget <= 0 {
		budget = 4 * time.Second
	}
	far := plan.Conn
	_ = near.SetReadDeadline(time.Now().Add(wait))
	hello := make(chan connectRead, 1)
	go func() { hello <- clientHello(near) }()
	response := readConnectByte(far)
	var first, reply connectRead
	select {
	case reply = <-response:
		_ = near.SetReadDeadline(time.Now())
		first = <-hello
	case first = <-hello:
		if first.tls {
			_ = near.SetReadDeadline(time.Time{})
			return p.tryConnect(ctx, near, plan, first.data, response, budget)
		}
		_ = far.SetReadDeadline(time.Now())
		reply = <-response
	}
	_ = near.SetReadDeadline(time.Time{})
	_ = far.SetReadDeadline(time.Time{})
	return prependConnect(near, first.data), prependConnect(far, reply.data), nil
}

// tryConnect commits on the first server byte, even a TLS alert or a partial
// response followed by EOF. After that point no client data can be replayed.
func (p *Proxy) tryConnect(ctx context.Context, near net.Conn, plan *connectPlan, hello []byte, response <-chan connectRead, budget time.Duration) (net.Conn, net.Conn, error) {
	far, target := plan.Conn, plan.target
	targets := append([]string{target}, plan.remaining...)
	var failed dialErrors
	for i, target := range targets {
		deadline := time.Now().Add(budget)
		if i > 0 {
			actx, cancel := context.WithDeadline(ctx, deadline)
			var err error
			far, err = plan.attempt(actx, target)
			cancel()
			if err != nil {
				failed = append(failed, fmt.Errorf("%s: %w", target, err))
				if ctx.Err() != nil {
					break
				}
				continue
			}
			response = readConnectByte(far)
		}
		c := far
		stop := context.AfterFunc(ctx, func() { _ = c.Close() })
		_ = far.SetDeadline(deadline)
		_, writeErr := io.Copy(far, bytes.NewReader(hello))
		if writeErr != nil {
			// Join the reader before closing; it may already hold a response byte.
			_ = far.SetReadDeadline(time.Now())
		}
		reply := <-response
		stop()
		if len(reply.data) > 0 {
			if writeErr != nil {
				_ = far.Close()
				return near, nil, fmt.Errorf("ClientHello write after server response: %w", writeErr)
			}
			_ = far.SetDeadline(time.Time{})
			p.rememberConnect(plan.key, target)
			return near, prependConnect(far, reply.data), nil
		}
		_ = far.Close()
		if writeErr != nil {
			reply.err = writeErr
		}
		failed = append(failed, fmt.Errorf("%s: no response within %s: %w", target, budget, reply.err))
		if ctx.Err() != nil {
			break
		}
	}
	if ctx.Err() != nil {
		return near, nil, fmt.Errorf("TLS first response failed: %w; %w", ctx.Err(), failed)
	}
	return near, nil, fmt.Errorf("TLS first response failed: %w", failed)
}
