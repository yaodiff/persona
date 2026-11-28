package egress

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

func (p *Proxy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(ctx, network, addr)
	}
	d := net.Dialer{Timeout: 30 * time.Second}
	return d.DialContext(ctx, network, addr)
}

// errInternal: a name of the open internet that leads to this machine or the cloud's metadata.
var errInternal = errors.New("resolves to a loopback or link-local address")

// dialOut reaches host:port: through the hub's upstream proxy when it has one for it, else straight — never to a
// loopback or link-local address the extension did not name as it is.
func (p *Proxy) dialOut(ctx context.Context, host, port string, rule Rule) (net.Conn, error) {
	upstream := p.Upstream
	if upstream == nil {
		upstream = fromEnvironment
	}
	target := net.JoinHostPort(host, port)
	ip := net.ParseIP(host)
	if ip != nil {
		if isSelfIP(host) && p.ownPort(port) {
			return nil, fmt.Errorf("%s:%s %w", host, port, errInternal)
		}
		up, err := upstream(&url.URL{Scheme: "https", Host: target})
		if err != nil {
			return nil, err
		}
		if up != nil {
			return p.connectVia(ctx, up, target)
		}
		return p.dial(ctx, "tcp", target) // named as it is in its permissions (Matches)
	}
	resolve := p.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context, host string) ([]net.IP, error) {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			ips := make([]net.IP, 0, len(addrs))
			for _, a := range addrs {
				ips = append(ips, a.IP)
			}
			return ips, err
		}
	}
	ips, err := resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var last error = fmt.Errorf("%s: no address", host)
	var ok []net.IP
	for _, ip := range ips {
		if Unreachable(ip) {
			last = fmt.Errorf("%s %w", host, errInternal)
			continue
		}
		ok = append(ok, ip)
	}
	if len(ok) == 0 {
		return nil, last
	}
	up, err := upstream(&url.URL{Scheme: "https", Host: target})
	if err != nil {
		return nil, err
	}
	if up != nil {
		// Only a checked answer reaches the upstream, and only as a numeric address: the proxy never sees
		// the name and cannot resolve it again to somewhere forbidden (#4353).
		return dialRace(ctx, ok, port, func(ctx context.Context, target string) (net.Conn, error) {
			return p.connectVia(ctx, up, target)
		})
	}
	return p.dialResolved(ctx, host, port, ok)
}

// fromEnvironment is the hub's own proxy for u, from its environment.
func fromEnvironment(u *url.URL) (*url.URL, error) {
	return http.ProxyFromEnvironment(&http.Request{URL: u})
}

// connectVia opens a tunnel to target through the upstream proxy up.
func (p *Proxy) connectVia(ctx context.Context, up *url.URL, target string) (net.Conn, error) {
	addr := up.Host
	if up.Port() == "" {
		addr = net.JoinHostPort(up.Hostname(), "80")
	}
	c, err := p.dial(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	// Bind cancellation immediately, not only once the tunnel handshake finishes.
	// ReadResponse and Body.Close do not otherwise observe request contexts.
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: http.Header{}}
	if up.User != nil {
		pass, _ := up.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(up.User.Username()+":"+pass)))
	}
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	if err := req.Write(c); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		c.Close()
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		c.Close()
		return nil, fmt.Errorf("the hub's proxy refused %s: %s", target, resp.Status)
	}
	_ = c.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: c, r: br}, nil
	}
	return c, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// CloseWrite half-closes the connection underneath, so a tunnel through the hub's proxy keeps a half-close too.
func (b *bufferedConn) CloseWrite() error { return closeWrite(b.Conn) }
