package egress

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
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
	ips, err := p.resolveAll(ctx, host)
	if err != nil {
		return nil, err
	}
	var last error = fmt.Errorf("%s: no address", host)
	var checked []net.IP
	for _, ip := range ips {
		if Unreachable(ip) {
			last = fmt.Errorf("%s %w", host, errInternal)
			continue
		}
		checked = append(checked, ip)
	}
	if len(checked) == 0 {
		return nil, last
	}
	up, err := upstream(&url.URL{Scheme: "https", Host: target})
	if err != nil {
		return nil, err
	}
	// Each attempt uses a checked numeric address, including through the upstream proxy (#4353).
	key := connectKey{host: host, port: port, upstream: upstreamEndpoint(up)}
	return p.dialResolved(ctx, key, checked, p.dialAttemptFor(up))
}

// fromEnvironment is the hub's own proxy for u, from its environment.
func fromEnvironment(u *url.URL) (*url.URL, error) {
	return http.ProxyFromEnvironment(&http.Request{URL: u})
}

func (p *Proxy) dialAttemptFor(up *url.URL) dialAttempt {
	if up != nil {
		return func(ctx context.Context, target string) (net.Conn, error) {
			return p.connectVia(ctx, up, target)
		}
	}
	return func(ctx context.Context, target string) (net.Conn, error) {
		return p.dial(ctx, "tcp", target)
	}
}

// upstreamEndpoint identifies the proxy by its TCP address. Preserve DNS root
// dots and IPv6 zones because they can affect which endpoint the dial reaches.
func upstreamEndpoint(up *url.URL) string {
	if up == nil {
		return ""
	}
	host, port := up.Hostname(), up.Port()
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.String()
	} else {
		host = strings.ToLower(host)
	}
	if port == "" {
		port = "80"
	} else if n, err := strconv.ParseUint(port, 10, 16); err == nil {
		port = strconv.FormatUint(n, 10)
	}
	return net.JoinHostPort(host, port)
}

// connectVia opens a tunnel to target through the upstream proxy up.
func (p *Proxy) connectVia(ctx context.Context, up *url.URL, target string) (net.Conn, error) {
	c, err := p.dial(ctx, "tcp", upstreamEndpoint(up))
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
	deadline := time.Now().Add(30 * time.Second)
	if caller, ok := ctx.Deadline(); ok && caller.Before(deadline) {
		deadline = caller
	}
	_ = c.SetDeadline(deadline)
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
