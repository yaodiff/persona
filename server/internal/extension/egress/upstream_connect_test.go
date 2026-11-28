package egress

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	connectTestHost   = "api.example.com"
	connectTestTarget = connectTestHost + ":443"
	connectTestIPv4   = "192.0.2.1:443"
	connectTestIPv6   = "[2001:db8::1]:443"
)

// testConnectUpstream records the wire authority and relays to the selected pipe.
// The origins can only receive bytes through the upstream CONNECT.
func testConnectUpstream(t *testing.T, ipv4, ipv6 func(net.Conn)) (*url.URL, func() []string) {
	t.Helper()
	peers := map[string]func(net.Conn){connectTestIPv4: ipv4, connectTestIPv6: ipv6}
	var mu sync.Mutex
	var targets []string
	relay := &Proxy{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		targets = append(targets, r.RequestURI)
		mu.Unlock()
		peer, allowed := peers[r.RequestURI]
		if r.Host != r.RequestURI || !allowed {
			t.Errorf("unexpected upstream CONNECT %q with Host %q", r.RequestURI, r.Host)
			http.Error(w, "unexpected CONNECT target", http.StatusBadRequest)
			return
		}
		relay.tunnel(w, r, func() (net.Conn, error) {
			c, origin := net.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer origin.Close()
				peer(origin)
			}()
			t.Cleanup(func() {
				_ = c.Close()
				<-done
			})
			return c, nil
		})
	}))
	t.Cleanup(server.Close)
	up, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return up, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(targets)
	}
}

func connectProxyWithUpstream(t *testing.T, ipv4, ipv6 func(net.Conn)) (*Proxy, func() []string) {
	t.Helper()
	up, targets := testConnectUpstream(t, ipv4, ipv6)
	p := &Proxy{
		Upstream: func(*url.URL) (*url.URL, error) { return up, nil },
		Resolve: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("2001:db8::1")}, nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != up.Host {
				return nil, fmt.Errorf("dial to %s bypassed the upstream proxy", addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	startConnectProxy(t, p)
	return p, targets
}

func silentConnectPeer(c net.Conn) { _, _ = io.Copy(io.Discard, c) }

func testConnectTLSPeer(t *testing.T) (func(net.Conn), string) {
	t.Helper()
	server := httptest.NewTLSServer(nil)
	t.Cleanup(server.Close)
	return func(c net.Conn) {
		_, _ = io.Copy(io.Discard, tls.Server(c, server.TLS))
	}, server.Listener.Addr().String()
}

func handshakeThroughProxy(t *testing.T, proxy string) {
	t.Helper()
	c := tls.Client(connectTunnel(t, proxy, connectTestTarget), &tls.Config{InsecureSkipVerify: true})
	defer c.Close()
	if err := c.Handshake(); err != nil {
		t.Fatal(err)
	}
}

func TestConnectUpstreamSkipsSilentTLSAndRemembersAddress(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule Rule
	}{
		{"manifest", Rule{Hosts: []string{connectTestHost}}},
		{"configured", Rule{Targets: []Target{{Host: connectTestHost, Port: "443"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			healthy, _ := testConnectTLSPeer(t)
			p, targets := connectProxyWithUpstream(t, silentConnectPeer, healthy)
			proxy := p.Register("notes", tc.rule)

			start := time.Now()
			handshakeThroughProxy(t, proxy)
			if elapsed := time.Since(start); elapsed > 5*p.connectBudget {
				t.Fatalf("TLS failover took %s with a %s per-address budget", elapsed, p.connectBudget)
			}
			if got, want := targets(), []string{connectTestIPv4, connectTestIPv6}; !slices.Equal(got, want) {
				t.Fatalf("first request sent CONNECT to %v, want %v", got, want)
			}

			handshakeThroughProxy(t, proxy)
			if got, want := targets(), []string{connectTestIPv4, connectTestIPv6, connectTestIPv6}; !slices.Equal(got, want) {
				t.Fatalf("follow-up request sent CONNECT to %v, want %v", got, want)
			}
		})
	}
}

func TestConnectUpstreamExhaustsSilentTargets(t *testing.T) {
	p, targets := connectProxyWithUpstream(t, silentConnectPeer, silentConnectPeer)
	proxy := p.Register("notes", Rule{Hosts: []string{connectTestHost}})
	c := tls.Client(connectTunnel(t, proxy, connectTestTarget), &tls.Config{InsecureSkipVerify: true})
	defer c.Close()

	start := time.Now()
	if err := c.Handshake(); err == nil {
		t.Fatal("TLS succeeded without any server bytes")
	}
	if elapsed := time.Since(start); elapsed > 5*p.connectBudget {
		t.Fatalf("silent targets took %s with a %s per-address budget", elapsed, p.connectBudget)
	}
	if got, want := targets(), []string{connectTestIPv4, connectTestIPv6}; !slices.Equal(got, want) {
		t.Fatalf("CONNECT targets %v, want %v", got, want)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.lastGood) != 0 {
		t.Fatal("a silent target was cached")
	}
}

func TestConnectUpstreamScopesLastGoodByRoute(t *testing.T) {
	healthy, direct := testConnectTLSPeer(t)
	first, firstTargets := testConnectUpstream(t, silentConnectPeer, healthy)
	second, secondTargets := testConnectUpstream(t, healthy, healthy)
	p, directTargets := connectProxy(t, direct, direct)
	var route atomic.Pointer[url.URL]
	p.Upstream = func(*url.URL) (*url.URL, error) { return route.Load(), nil }
	directDial := p.Dial
	p.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == first.Host || addr == second.Host {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
		return directDial(ctx, network, addr)
	}
	proxy := p.Register("notes", Rule{Hosts: []string{connectTestHost}})
	firstWithAuth := *first
	firstWithAuth.User = url.UserPassword("example", "example-password")
	firstWithAuth.Host = net.JoinHostPort(first.Hostname(), "0"+first.Port())

	for _, step := range []struct {
		name    string
		route   *url.URL
		targets func() []string
		want    []string
	}{
		{"first upstream", first, firstTargets, []string{connectTestIPv4, connectTestIPv6}},
		{"direct", nil, directTargets, []string{connectTestIPv4}},
		{"second upstream", second, secondTargets, []string{connectTestIPv4}},
		{"same endpoint with credentials", &firstWithAuth, firstTargets, []string{connectTestIPv4, connectTestIPv6, connectTestIPv6}},
		{"direct again", nil, directTargets, []string{connectTestIPv4, connectTestIPv4}},
	} {
		t.Run(step.name, func(t *testing.T) {
			route.Store(step.route)
			handshakeThroughProxy(t, proxy)
			if got := step.targets(); !slices.Equal(got, step.want) {
				t.Fatalf("dialed %v, want %v", got, step.want)
			}
		})
	}
}

func TestConnectUpstreamPinsValidatedAnswersThroughRetry(t *testing.T) {
	healthy, _ := testConnectTLSPeer(t)
	p, targets := connectProxyWithUpstream(t, silentConnectPeer, healthy)
	forbidden := []net.IP{nil, net.IPv4(127, 0, 0, 1), net.IPv4(169, 254, 169, 254)}
	for _, raw := range metadataIPs {
		forbidden = append(forbidden, net.ParseIP(raw))
	}
	mixed := append([]net.IP{net.ParseIP("192.0.2.1")}, forbidden...)
	mixed = append(mixed, net.ParseIP("2001:db8::1"))
	var lookups atomic.Int32
	p.Resolve = func(_ context.Context, host string) ([]net.IP, error) {
		if host != connectTestHost {
			return nil, fmt.Errorf("unexpected DNS lookup for %q", host)
		}
		if lookups.Add(1) <= 2 {
			return mixed, nil
		}
		return forbidden, nil
	}
	configured := p.Register("configured", Rule{Targets: []Target{{Host: connectTestHost, Port: "443"}}})
	proxy := p.Register("notes", Rule{Hosts: []string{connectTestHost}})

	// Settings addresses reject a mixed answer before opening even the first tunnel.
	if code := connectStatus(t, configured, connectTestTarget); code != http.StatusBadGateway {
		t.Fatalf("configured mixed answer: status %d", code)
	}
	if got := targets(); len(got) != 0 {
		t.Fatalf("configured mixed answer reached the upstream: %v", got)
	}

	// A manifest host filters the mixed answer. A second lookup during retry
	// would see only forbidden addresses.
	handshakeThroughProxy(t, proxy)
	if got := lookups.Load(); got != 2 {
		t.Fatalf("retry resolved DNS again: got %d lookups across two requests", got)
	}
	if code := connectStatus(t, proxy, connectTestTarget); code != http.StatusBadGateway {
		t.Fatalf("rebound answer with a cached address: status %d", code)
	}
	if got, want := targets(), []string{connectTestIPv4, connectTestIPv6}; !slices.Equal(got, want) {
		t.Fatalf("upstream CONNECT targets %v, want exactly the two validated addresses %v", got, want)
	}
}

func TestConnectUpstreamPassesNonTLS(t *testing.T) {
	for _, tc := range []struct {
		name   string
		banner string
	}{
		{"client first", ""},
		{"server first", "SSH-2.0-example\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := func(c net.Conn) {
				if tc.banner != "" {
					_, _ = io.WriteString(c, tc.banner)
				}
				_, _ = io.Copy(c, c)
			}
			p, targets := connectProxyWithUpstream(t, peer, peer)
			proxy := p.Register("notes", Rule{Hosts: []string{connectTestHost}})
			c := connectTunnel(t, proxy, connectTestTarget)
			if tc.banner != "" {
				// Require the banner before the client sniff window has elapsed.
				_ = c.SetReadDeadline(time.Now().Add(p.connectWait / 2))
				banner := make([]byte, len(tc.banner))
				if _, err := io.ReadFull(c, banner); err != nil || string(banner) != tc.banner {
					t.Fatalf("server banner %q: %v", banner, err)
				}
				_ = c.SetReadDeadline(time.Now().Add(time.Second))
			}
			message := "ordinary tunnel data"
			if _, err := io.WriteString(c, message); err != nil {
				t.Fatal(err)
			}
			echo := make([]byte, len(message))
			if _, err := io.ReadFull(c, echo); err != nil || string(echo) != message {
				t.Fatalf("echo %q: %v", echo, err)
			}
			if got := targets(); !slices.Equal(got, []string{connectTestIPv4}) {
				t.Fatalf("non-TLS traffic opened more than one tunnel: %v", got)
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if len(p.lastGood) != 0 {
				t.Fatal("non-TLS traffic populated the TLS response cache")
			}
		})
	}
}

func TestConnectUpstreamReplaysBufferedHelloWithoutEarlyData(t *testing.T) {
	hello := helloRecord([]byte{1, 0, 0, 1, 0})
	early := []byte{0x17, 3, 3, 0, 1, 42}
	silentFlight := make(chan []byte, 1)
	silent := func(c net.Conn) {
		got, _ := io.ReadAll(c)
		select {
		case silentFlight <- got:
		default:
		}
	}
	healthy := func(c net.Conn) {
		got := make([]byte, len(hello))
		if _, err := io.ReadFull(c, got); err != nil || !slices.Equal(got, hello) {
			t.Errorf("replayed ClientHello %x: %v", got, err)
			return
		}
		_, _ = io.WriteString(c, "response")
		_, _ = io.Copy(c, c)
	}
	p, targets := connectProxyWithUpstream(t, silent, healthy)
	proxy := p.Register("notes", Rule{Hosts: []string{connectTestHost}})
	c := connectTunnelWithData(t, proxy, connectTestTarget, append(slices.Clone(hello), early...))
	want := append([]byte("response"), early...)
	got := make([]byte, len(want))
	if _, err := io.ReadFull(c, got); err != nil || !slices.Equal(got, want) {
		t.Fatalf("server response and echoed application data %x: %v", got, err)
	}
	select {
	case got := <-silentFlight:
		if !slices.Equal(got, hello) {
			t.Fatalf("abandoned address received %x, want only ClientHello %x", got, hello)
		}
	case <-time.After(time.Second):
		t.Fatal("abandoned tunnel remained open")
	}
	if got, want := targets(), []string{connectTestIPv4, connectTestIPv6}; !slices.Equal(got, want) {
		t.Fatalf("upstream CONNECT targets %v, want %v", got, want)
	}
}

func TestUpstreamEndpointNormalization(t *testing.T) {
	for _, tc := range []struct{ proxy, want string }{
		{"http://proxy.example.com", "proxy.example.com:80"},
		{"http://example:example-password@PROXY.EXAMPLE.COM:080/path?query=value", "proxy.example.com:80"},
		{"http://[2001:DB8:0:0::1]:3128", "[2001:db8::1]:3128"},
		{"http://PROXY.EXAMPLE.COM.:3128", "proxy.example.com.:3128"},
	} {
		up, err := url.Parse(tc.proxy)
		if err != nil {
			t.Fatal(err)
		}
		if got := upstreamEndpoint(up); got != tc.want {
			t.Errorf("proxy endpoint %q, want %q", got, tc.want)
		}
	}
}
