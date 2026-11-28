package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// connectListener runs a local peer and joins its handlers on cleanup. A silent
// peer drains the ClientHello without writing, just like the failed TLS address.
func connectListener(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	var handlers sync.WaitGroup
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				defer c.Close()
				serve(c)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-accepted
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		handlers.Wait()
	})
	return ln.Addr().String()
}

// connectProxy maps checked public addresses to local listeners, preserving the
// attempted address separately from the actual socket's loopback RemoteAddr.
func connectProxy(t *testing.T, targets ...string) (*Proxy, func() []string) {
	t.Helper()
	addresses := []string{"192.0.2.1", "2001:db8::1"}
	var mu sync.Mutex
	var dialed []string
	p := &Proxy{
		Upstream: func(*url.URL) (*url.URL, error) { return nil, nil },
		Resolve: func(context.Context, string) ([]net.IP, error) {
			ips := make([]net.IP, len(targets))
			for i := range targets {
				ips[i] = net.ParseIP(addresses[i])
			}
			return ips, nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, addr)
			mu.Unlock()
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if i := slices.Index(addresses, host); i >= 0 && i < len(targets) {
				addr = targets[i]
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	startConnectProxy(t, p)
	return p, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(dialed)
	}
}

func startConnectProxy(t *testing.T, p *Proxy) {
	t.Helper()
	p.connectWait = time.Second
	p.connectBudget = 200 * time.Millisecond
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
}

func connectTunnel(t *testing.T, proxy, target string) net.Conn {
	t.Helper()
	return connectTunnelWithData(t, proxy, target, nil)
}

func connectTunnelWithData(t *testing.T, proxy, target string, data []byte) net.Conn {
	t.Helper()
	u, err := url.Parse(proxy)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.DialTimeout("tcp", u.Host, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	password, _ := u.User.Password()
	auth := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + password))
	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth)
	if _, err := c.Write(append([]byte(request), data...)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(c)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT: %s", response.Status)
	}
	return &bufferedConn{Conn: c, r: reader}
}

func TestConnectSkipsSilentTLSAndRemembersAddress(t *testing.T) {
	for _, configured := range []bool{false, true} {
		for _, port := range []string{"443", "8443"} {
			t.Run(fmt.Sprintf("configured=%t/port=%s", configured, port), func(t *testing.T) {
				silent := connectListener(t, func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
				srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.WriteString(w, "TLS answered")
				}))
				t.Cleanup(srv.Close)
				p, dialed := connectProxy(t, silent, srv.Listener.Addr().String())
				rule := Rule{Hosts: []string{"api.example.com"}}
				if configured {
					rule = Rule{Targets: []Target{{Host: "api.example.com", Port: port}}}
				}
				proxy, _ := url.Parse(p.Register("notes", rule))
				transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true,
					TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
				t.Cleanup(transport.CloseIdleConnections)
				client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
				for request := range 2 {
					began := time.Now()
					response, err := client.Get("https://api.example.com:" + port + "/")
					if err != nil {
						t.Fatalf("request %d: %v", request, err)
					}
					body, err := io.ReadAll(response.Body)
					_ = response.Body.Close()
					if err != nil || string(body) != "TLS answered" {
						t.Fatalf("request %d: body %q, error %v", request, body, err)
					}
					if took := time.Since(began); took > 2*time.Second {
						t.Fatalf("request %d took %s past its address budget", request, took)
					}
					want := []string{net.JoinHostPort("192.0.2.1", port), net.JoinHostPort("2001:db8::1", port)}
					if request == 1 {
						want = append(want, net.JoinHostPort("2001:db8::1", port))
					}
					if got := dialed(); !slices.Equal(got, want) {
						t.Fatalf("request %d dialed %v, want %v", request, got, want)
					}
				}
			})
		}
	}
}

func TestConnectAllTLSAddressesSilent(t *testing.T) {
	silent := func(c net.Conn) { _, _ = io.Copy(io.Discard, c) }
	p, dialed := connectProxy(t, connectListener(t, silent), connectListener(t, silent))
	proxy := p.Register("notes", Rule{Hosts: []string{"api.example.com"}})
	conn := tls.Client(connectTunnel(t, proxy, "api.example.com:443"), &tls.Config{InsecureSkipVerify: true})
	defer conn.Close()
	began := time.Now()
	if err := conn.Handshake(); err == nil {
		t.Fatal("TLS succeeded without any server bytes")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatalf("TLS waited for the client's deadline instead of exhausting addresses: %v", err)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("all silent addresses took %s", took)
	}
	if got, want := dialed(), []string{"192.0.2.1:443", "[2001:db8::1]:443"}; !slices.Equal(got, want) {
		t.Fatalf("dialed %v, want %v", got, want)
	}
}

func TestConnectNonTLSClientFirst(t *testing.T) {
	echo := connectListener(t, func(c net.Conn) { _, _ = io.Copy(c, c) })
	p, dialed := connectProxy(t, echo, echo)
	proxy := p.Register("notes", Rule{Hosts: []string{"api.example.com"}})
	c := connectTunnel(t, proxy, "api.example.com:8080")
	message := "ordinary tunnel data"
	if _, err := io.WriteString(c, message); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(message))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != message {
		t.Fatalf("echo %q: %v", got, err)
	}
	if got := dialed(); !slices.Equal(got, []string{"192.0.2.1:8080"}) {
		t.Fatalf("non-TLS tunnel redialed: %v", got)
	}
}

func TestConnectServerFirstBanner(t *testing.T) {
	for _, port := range []string{"22", "443"} {
		t.Run(port, func(t *testing.T) {
			banner := "SSH-2.0-example\r\n"
			server := connectListener(t, func(c net.Conn) {
				_, _ = io.WriteString(c, banner)
				_, _ = io.Copy(c, c)
			})
			p, dialed := connectProxy(t, server, server)
			proxy := p.Register("notes", Rule{Hosts: []string{"api.example.com"}})
			c := connectTunnel(t, proxy, "api.example.com:"+port)
			// The server speaks before the client; it must not wait for the
			// one-second client sniff deadline, even on the usual TLS port.
			_ = c.SetDeadline(time.Now().Add(500 * time.Millisecond))
			got := make([]byte, len(banner))
			if _, err := io.ReadFull(c, got); err != nil || string(got) != banner {
				t.Fatalf("banner %q: %v", got, err)
			}
			if _, err := io.WriteString(c, "reply"); err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(c, got[:5]); err != nil || string(got[:5]) != "reply" {
				t.Fatalf("after banner %q: %v", got[:5], err)
			}
			if got := dialed(); !slices.Equal(got, []string{net.JoinHostPort("192.0.2.1", port)}) {
				t.Fatalf("server-first tunnel redialed: %v", got)
			}
		})
	}
}

func TestConnectNeverRetriesAfterServerByte(t *testing.T) {
	server := connectListener(t, func(c net.Conn) {
		var first [1]byte
		if _, err := io.ReadFull(c, first[:]); err == nil {
			_, _ = c.Write([]byte{0x16})
		}
	})
	p, dialed := connectProxy(t, server, server)
	proxy := p.Register("notes", Rule{Hosts: []string{"api.example.com"}})
	c := tls.Client(connectTunnel(t, proxy, "api.example.com:443"), &tls.Config{InsecureSkipVerify: true})
	defer c.Close()
	if err := c.Handshake(); err == nil {
		t.Fatal("a truncated TLS record completed the handshake")
	}
	if got := dialed(); !slices.Equal(got, []string{"192.0.2.1:443"}) {
		t.Fatalf("retried after the server spoke: %v", got)
	}
}

func TestConnectIdleClientResumesWithoutReplay(t *testing.T) {
	for _, prefix := range []string{"", "\x16"} {
		t.Run(fmt.Sprintf("prefix=%x", prefix), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client, near := net.Pipe()
				far, server := net.Pipe()
				defer client.Close()
				defer near.Close()
				defer far.Close()
				defer server.Close()
				resume := make(chan struct{})
				go func() {
					if prefix != "" {
						_, _ = io.WriteString(client, prefix)
					}
					<-resume
					_, _ = io.WriteString(client, "late")
				}()
				p := &Proxy{connectWait: 20 * time.Millisecond}
				began := time.Now()
				near, far, err := p.prepareConnect(context.Background(), near, &connectPlan{
					Conn: far, target: "192.0.2.1:443", remaining: []string{"[2001:db8::1]:443"},
				})
				if err != nil {
					t.Fatal(err)
				}
				if took := time.Since(began); took != p.connectWait {
					t.Fatalf("client wait %s, want %s", took, p.connectWait)
				}
				close(resume)
				got := make([]byte, len(prefix)+len("late"))
				if _, err := io.ReadFull(near, got); err != nil || string(got) != prefix+"late" {
					t.Fatalf("late client input %q: %v", got, err)
				}
				go func() { _, _ = io.WriteString(server, "reply") }()
				got = make([]byte, len("reply"))
				if _, err := io.ReadFull(far, got); err != nil || string(got) != "reply" {
					t.Fatalf("late server reply %q: %v", got, err)
				}
			})
		})
	}
}

type connectReadyConn struct {
	net.Conn
	ready chan struct{}
}

func (c *connectReadyConn) SetReadDeadline(deadline time.Time) error {
	err := c.Conn.SetReadDeadline(deadline)
	if deadline.IsZero() {
		select {
		case c.ready <- struct{}{}:
		default:
		}
	}
	return err
}

func TestConnectIdleClientKeepsHijackedRequestAlive(t *testing.T) {
	echo := connectListener(t, func(c net.Conn) { _, _ = io.Copy(c, c) })
	p, dialed := connectProxy(t, echo, echo)
	p.connectWait = 20 * time.Millisecond
	ready := make(chan struct{}, 1)
	dial := p.Dial
	p.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &connectReadyConn{Conn: c, ready: ready}, nil
	}
	proxy := p.Register("notes", Rule{Hosts: []string{"api.example.com"}})
	c := connectTunnel(t, proxy, "api.example.com:443")
	// Wait for the probe's read deadline to be cleared before sending. This
	// exercises the real HTTP hijack lifetime without sleeping past the timeout.
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("idle-client probe did not finish")
	}
	if _, err := io.WriteString(c, "late"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("late"))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "late" {
		t.Fatalf("late echo %q: %v", got, err)
	}
	if got := dialed(); !slices.Equal(got, []string{"192.0.2.1:443"}) {
		t.Fatalf("idle client redialed: %v", got)
	}
}

func TestConnectSingleAddressAndUpstreamKeepTheirLifetime(t *testing.T) {
	for _, upstream := range []bool{false, true} {
		t.Run(fmt.Sprintf("upstream=%t", upstream), func(t *testing.T) {
			silent := connectListener(t, func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
			p, dialed := connectProxy(t, silent)
			want := []string{"192.0.2.1:443"}
			if upstream {
				inner := newProxy(t, silent, nil)
				up, _ := url.Parse(inner.Register("notes", Rule{Hosts: []string{"api.example.com", "192.0.2.1"}}))
				p.Upstream = func(*url.URL) (*url.URL, error) { return up, nil }
				p.Resolve = func(context.Context, string) ([]net.IP, error) {
					// The hub checks the answer locally, then the upstream CONNECTs to that address (#4353).
					return []net.IP{net.ParseIP("192.0.2.1")}, nil
				}
				want = []string{up.Host}
			}
			p.connectBudget = 10 * time.Millisecond
			proxy := p.Register("notes", Rule{Hosts: []string{"api.example.com"}})
			c := tls.Client(connectTunnel(t, proxy, "api.example.com:443"), &tls.Config{InsecureSkipVerify: true})
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if err := c.HandshakeContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("single destination should keep the client's handshake lifetime: %v", err)
			}
			if got := dialed(); !slices.Equal(got, want) {
				t.Fatalf("dialed %v, want %v", got, want)
			}
		})
	}
}

func TestConnectReplaysBufferedClientHello(t *testing.T) {
	// Only the record/handshake framing matters to the proxy: these fixture
	// bytes must survive both net/http prefetch and replay byte-for-byte.
	hello := []byte{0x16, 3, 3, 0, 8, 1, 0, 0, 4, 1, 2, 3, 4}
	silent := connectListener(t, func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	server := connectListener(t, func(c net.Conn) {
		got := make([]byte, len(hello))
		if _, err := io.ReadFull(c, got); err != nil || !slices.Equal(got, hello) {
			t.Errorf("replayed ClientHello %x: %v", got, err)
			return
		}
		_, _ = io.WriteString(c, "replayed")
	})
	p, dialed := connectProxy(t, silent, server)
	proxy := p.Register("notes", Rule{Hosts: []string{"api.example.com"}})
	c := connectTunnelWithData(t, proxy, "api.example.com:443", hello)
	got := make([]byte, len("replayed"))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "replayed" {
		t.Fatalf("buffered hello reply %q: %v", got, err)
	}
	if got := dialed(); !slices.Equal(got, []string{"192.0.2.1:443", "[2001:db8::1]:443"}) {
		t.Fatalf("buffered hello dialed %v", got)
	}
}

func TestConnectNonTLSKeepsHalfClose(t *testing.T) {
	server := connectListener(t, func(c net.Conn) {
		request, err := io.ReadAll(c)
		if err != nil {
			return
		}
		_, _ = c.Write(append([]byte("reply: "), request...))
	})
	p, dialed := connectProxy(t, server, server)
	proxy := p.Register("notes", Rule{Hosts: []string{"api.example.com"}})
	c := connectTunnel(t, proxy, "api.example.com:8080")
	if _, err := io.WriteString(c, "request"); err != nil {
		t.Fatal(err)
	}
	if err := closeWrite(c); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "reply: request" {
		t.Fatalf("half-close reply %q: %v", got, err)
	}
	if got := dialed(); !slices.Equal(got, []string{"192.0.2.1:8080"}) {
		t.Fatalf("half-close redialed: %v", got)
	}
}
