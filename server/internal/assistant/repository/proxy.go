package repository

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/netproxy"
	"golang.org/x/net/proxy"
)

// Match the extension market's priority for the public GitHub API. Enterprise
// and GitLab endpoints use their own environment rules, including NO_PROXY.
func (c Checker) proxyFor(req *http.Request) (*url.URL, error) {
	if req.URL.Hostname() == "api.github.com" && strings.TrimSpace(c.GitHubProxy) != "" {
		return netproxy.Parse(c.GitHubProxy)
	}
	return http.ProxyFromEnvironment(req)
}

type contextDial func(context.Context, string, string) (net.Conn, error)

func (d contextDial) Dial(network, address string) (net.Conn, error) {
	return d(context.Background(), network, address)
}

func (d contextDial) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d(ctx, network, address)
}

// The proxy is operator-controlled and may itself be on loopback. Only the
// destination is subject to repository trust rules, and the proxy receives its
// validated numeric address, never a hostname it could resolve differently.
func dialThroughProxy(ctx context.Context, upstream *url.URL, network, target string, dial contextDial) (net.Conn, error) {
	if upstream == nil {
		return dial(ctx, network, target)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	scheme := upstream.Scheme
	if scheme == "" {
		scheme = "http"
	}
	port := upstream.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		}
	}
	if upstream.Hostname() == "" || port == "" {
		return nil, errors.New("invalid repository proxy")
	}
	address := net.JoinHostPort(upstream.Hostname(), port)
	if scheme == "socks5" || scheme == "socks5h" {
		var auth *proxy.Auth
		if upstream.User != nil {
			password, _ := upstream.User.Password()
			auth = &proxy.Auth{User: upstream.User.Username(), Password: password}
		}
		d, err := proxy.SOCKS5("tcp", address, auth, dial)
		if err != nil {
			return nil, err
		}
		return d.(proxy.ContextDialer).DialContext(ctx, network, target)
	}
	if scheme != "http" && scheme != "https" {
		return nil, errors.New("unsupported repository proxy scheme")
	}
	conn, err := dial(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	connected := false
	defer func() {
		if !connected {
			_ = conn.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	// Keep the cancellation callback on the raw connection while TLS wraps it.
	wire := conn
	if scheme == "https" {
		secure := tls.Client(conn, &tls.Config{ServerName: upstream.Hostname()})
		if err := secure.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		wire = secure
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: http.Header{}}
	if upstream.User != nil {
		password, _ := upstream.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(upstream.User.Username()+":"+password)))
	}
	if err := req.Write(wire); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(io.LimitReader(wire, 16<<10))
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("repository proxy refused CONNECT")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_ = wire.SetDeadline(time.Time{})
	connected = true
	// Any tunnel bytes read with the response stay before the live connection.
	// Do not consume or close CONNECT's body: the caller owns the tunnel now.
	return &proxyConn{Conn: wire, reader: io.MultiReader(reader, wire)}, nil
}

type proxyConn struct {
	net.Conn
	reader io.Reader
}

func (c *proxyConn) Read(b []byte) (int, error) { return c.reader.Read(b) }
