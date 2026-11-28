package egress

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// A name in ordinary network permissions is resolved and checked before an upstream sees it. A forbidden answer
// never reaches the proxy; an allowed one is CONNECTed by address, not by a name the proxy can resolve again (#4353).
func TestDialOutValidatesBeforeUpstream(t *testing.T) {
	var mu sync.Mutex
	var dials, requests []string
	p := &Proxy{
		Resolve: func(_ context.Context, host string) ([]net.IP, error) {
			if host == "forbidden.example.com" {
				return []net.IP{net.IPv4(169, 254, 169, 254)}, nil
			}
			return []net.IP{net.ParseIP("192.0.2.1")}, nil
		},
		Upstream: func(*url.URL) (*url.URL, error) { return url.Parse("http://proxy.example.com:3128") },
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			mu.Lock()
			dials = append(dials, addr)
			mu.Unlock()
			near, far := net.Pipe()
			go func() {
				defer far.Close()
				r, err := http.ReadRequest(bufio.NewReader(far))
				if err != nil {
					return
				}
				mu.Lock()
				requests = append(requests, r.Method+" "+r.Host)
				mu.Unlock()
				_, _ = io.WriteString(far, "HTTP/1.1 200 Connection established\r\n\r\n")
				_, _ = io.Copy(io.Discard, far)
			}()
			return near, nil
		},
	}
	if _, err := p.dialOut(context.Background(), "forbidden.example.com", "443", Rule{}); !errors.Is(err, errInternal) {
		t.Fatalf("a link-local answer reached the upstream: %v", err)
	}
	c, err := p.dialOut(context.Background(), "allowed.example.com", "443", Rule{})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(dials, []string{"proxy.example.com:3128"}) {
		t.Fatalf("forbidden answer was dialed: %v", dials)
	}
	if !slices.Equal(requests, []string{"CONNECT 192.0.2.1:443"}) {
		t.Fatalf("upstream requests %v, want only the checked address", requests)
	}
}

// The direct path also refuses hostnames that resolve to cloud metadata.
func TestDialOutStillRefusesMetadataDirectly(t *testing.T) {
	for _, raw := range metadataIPs {
		p := &Proxy{
			Resolve:  func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP(raw)}, nil },
			Upstream: func(*url.URL) (*url.URL, error) { return nil, nil },
			Dial: func(context.Context, string, string) (net.Conn, error) {
				t.Errorf("metadata address %s was dialed", raw)
				return nil, errors.New("metadata address dialed")
			},
		}
		if _, err := p.dialOut(context.Background(), "metadata.example.com", "80", Rule{}); !errors.Is(err, errInternal) {
			t.Fatalf("%s: %v, want errInternal", raw, err)
		}
	}
}

type connectDeadlineRecorder struct {
	net.Conn
	deadline time.Time
}

func (c *connectDeadlineRecorder) SetDeadline(deadline time.Time) error {
	c.deadline = deadline
	return c.Conn.SetDeadline(deadline)
}

func TestConnectViaCapsHandshakeDeadline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		caller  time.Duration
		timeout time.Duration
	}{
		{"caller deadline", 200 * time.Millisecond, 200 * time.Millisecond},
		{"handshake limit", time.Minute, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				near, peer := net.Pipe()
				defer near.Close()
				defer peer.Close()
				go func() { _, _ = io.Copy(io.Discard, peer) }()
				conn := &connectDeadlineRecorder{Conn: near}
				p := &Proxy{Dial: func(context.Context, string, string) (net.Conn, error) { return conn, nil }}
				up := &url.URL{Scheme: "http", Host: "proxy.example.com"}
				start := time.Now()
				ctx, cancel := context.WithDeadline(context.Background(), start.Add(tc.caller))
				defer cancel()

				if c, err := p.connectVia(ctx, up, "192.0.2.1:443"); err == nil {
					_ = c.Close()
					t.Fatal("CONNECT succeeded without an upstream response")
				}
				if got, want := conn.deadline, start.Add(tc.timeout); !got.Equal(want) {
					t.Fatalf("socket deadline %s, want %s", got.Sub(start), tc.timeout)
				}
				if elapsed := time.Since(start); elapsed != tc.timeout {
					t.Fatalf("CONNECT took %s, want %s", elapsed, tc.timeout)
				}
			})
		})
	}
}
