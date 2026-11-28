package egress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestProxyRefusesOwnPortViaLiteralLoopbackUnspecifiedAndTrailingDot(t *testing.T) {
	var mu sync.Mutex
	var dialed []string
	_, otherPort := localServer(t)

	hubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hub %s", r.Host)
	}))
	t.Cleanup(hubSrv.Close)

	p := &Proxy{
		HubAddr:  strings.TrimPrefix(hubSrv.URL, "http://"),
		Upstream: func(*url.URL) (*url.URL, error) { return nil, nil },
		Resolve: func(_ context.Context, host string) ([]net.IP, error) {
			if host == "rebind.example.com" {
				return []net.IP{net.ParseIP("127.0.0.1")}, nil
			}
			return nil, fmt.Errorf("unexpected DNS lookup for %s", host)
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, addr)
			mu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)

	_, ownPort, err := net.SplitHostPort(p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	zeroNetIP := net.IPv4(0, 1, 2, 3).String()
	purl := p.Register("ext", Rule{
		Hosts: []string{
			"127.0.0.1", "::1", "::ffff:127.0.0.1",
			"0.0.0.0", "::", "::ffff:0.0.0.0", zeroNetIP,
			"rebind.example.com", LocalHost,
		},
		Hub: false,
	})
	u, err := url.Parse(purl)
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	auth := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + pw))

	rawStatus := func(reqLine, hostHeader string) (int, string) {
		t.Helper()
		c, err := net.Dial("tcp", u.Host)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		fmt.Fprintf(c, "%s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\nConnection: close\r\n\r\n", reqLine, hostHeader, auth)
		method, _, _ := strings.Cut(reqLine, " ")
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: method})
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(body)
	}

	cases := []struct {
		name    string
		reqLine string
		host    string
		want    int
	}{
		{"http literal 127.0.0.1 own port", "GET http://127.0.0.1:" + ownPort + "/", "127.0.0.1:" + ownPort, http.StatusForbidden},
		{"connect literal 127.0.0.1 own port", "CONNECT 127.0.0.1:" + ownPort, "127.0.0.1:" + ownPort, http.StatusForbidden},
		{"connect literal ::1 own port", "CONNECT [::1]:" + ownPort, "[::1]:" + ownPort, http.StatusForbidden},
		{"connect literal IPv4-mapped loopback own port", "CONNECT [::ffff:127.0.0.1]:" + ownPort, "[::ffff:127.0.0.1]:" + ownPort, http.StatusForbidden},
		{"connect literal 0.0.0.0 own port", "CONNECT 0.0.0.0:" + ownPort, "0.0.0.0:" + ownPort, http.StatusForbidden},
		{"connect literal :: own port", "CONNECT [::]:" + ownPort, "[::]:" + ownPort, http.StatusForbidden},
		{"connect literal IPv4-mapped unspecified own port", "CONNECT [::ffff:0.0.0.0]:" + ownPort, "[::ffff:0.0.0.0]:" + ownPort, http.StatusForbidden},
		{"connect literal 0.0.0.0/8 own port", "CONNECT " + zeroNetIP + ":" + ownPort, zeroNetIP + ":" + ownPort, http.StatusForbidden},
		{"http trailing-dot LocalHost own port", "GET http://" + LocalHost + ".:" + ownPort + "/", LocalHost + ".:" + ownPort, http.StatusForbidden},
		{"connect trailing-dot LocalHost own port", "CONNECT " + LocalHost + ".:" + ownPort, LocalHost + ".:" + ownPort, http.StatusForbidden},
		{"uppercase trailing-dot LocalHost own port", "CONNECT " + strings.ToUpper(LocalHost) + ".:" + ownPort, strings.ToUpper(LocalHost) + ".:" + ownPort, http.StatusForbidden},
		{"http trailing-dot HubHost without Hub permission", "GET http://" + HubHost + "./api", HubHost + ".", http.StatusForbidden},
		{"uppercase trailing-dot HubHost without Hub permission", "GET http://" + strings.ToUpper(HubHost) + "./api", strings.ToUpper(HubHost) + ".", http.StatusForbidden},
		{"leading-zero port on LocalHost", "GET http://" + LocalHost + ":0" + ownPort + "/", LocalHost + ":0" + ownPort, http.StatusBadRequest},
		{"leading-zero port on 127.0.0.1", "CONNECT 127.0.0.1:0" + ownPort, "127.0.0.1:0" + ownPort, http.StatusBadRequest},
		{"plus-signed port on CONNECT", "CONNECT /", "127.0.0.1:+" + ownPort, http.StatusBadRequest},
		{"empty port on CONNECT", "CONNECT /", "127.0.0.1:", http.StatusBadRequest},
		{"service-name port on CONNECT", "CONNECT /", "127.0.0.1:http", http.StatusBadRequest},
		{"double trailing dot on HubHost", "GET http://" + HubHost + "../api", HubHost + "..", http.StatusBadRequest},
		{"double trailing dot on LocalHost", "CONNECT " + LocalHost + "..:" + ownPort, LocalHost + "..:" + ownPort, http.StatusBadRequest},
		{"double trailing dot inside brackets", "CONNECT [::ffff:127.0.0.1..]:" + ownPort, "[::ffff:127.0.0.1..]:" + ownPort, http.StatusBadRequest},
		{"nested brackets", "CONNECT [[::1]]:" + ownPort, "[[::1]]:" + ownPort, http.StatusBadRequest},
		{"DNS resolving to loopback on ownPort", "GET http://rebind.example.com:" + ownPort + "/", "rebind.example.com:" + ownPort, http.StatusBadGateway},
	}
	for _, tc := range cases {
		if got, body := rawStatus(tc.reqLine, tc.host); got != tc.want {
			t.Errorf("%s: status = %d (%q), want %d", tc.name, got, body, tc.want)
		}
	}

	// runtimeFetch broker must also refuse self-proxy targets and non-canonical ports.
	for _, tc := range []struct {
		target string
		want   int
	}{
		{"http://127.0.0.1:" + ownPort + "/", http.StatusForbidden},
		{"http://0.0.0.0:" + ownPort + "/", http.StatusForbidden},
		{"http://" + LocalHost + ".:" + ownPort + "/", http.StatusForbidden},
		{"http://127.0.0.1:0" + ownPort + "/", http.StatusBadRequest},
		{"http://" + LocalHost + "..:" + ownPort + "/", http.StatusBadRequest},
	} {
		raw, _ := json.Marshal(map[string]any{"url": tc.target, "method": "GET", "headers": map[string]string{}, "body": ""})
		r := httptest.NewRequest(http.MethodPost, RuntimeFetchPath, bytes.NewReader(raw))
		w := httptest.NewRecorder()
		p.handler("ext").ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("runtimeFetch(%s): status = %d (%q), want %d", tc.target, w.Code, w.Body.String(), tc.want)
		}
	}

	// Direct defense-in-depth checks on dialOut and runtimeDial for literal loopback/unspecified ownPort.
	for _, host := range []string{"127.0.0.1", "::1", "0.0.0.0", "::", zeroNetIP} {
		if c, err := p.dialOut(context.Background(), host, ownPort, Rule{}); !errors.Is(err, errInternal) {
			if c != nil {
				c.Close()
			}
			t.Errorf("dialOut(%s, ownPort): err = %v, want errInternal", host, err)
		}
		if c, err := p.runtimeDial(context.Background(), host, ownPort, false); !errors.Is(err, errInternal) {
			if c != nil {
				c.Close()
			}
			t.Errorf("runtimeDial(%s, ownPort): err = %v, want errInternal", host, err)
		}
	}

	mu.Lock()
	gotDialed := append([]string(nil), dialed...)
	mu.Unlock()
	if len(gotDialed) != 0 {
		t.Fatalf("proxy own port or forbidden host was dialed: %v", gotDialed)
	}

	// Non-proxy loopback ports remain reachable when loopback is allowed.
	if got, body := rawStatus("GET http://127.0.0.1:"+otherPort+"/ok", "127.0.0.1:"+otherPort); got != http.StatusOK {
		t.Fatalf("legitimate loopback port: status = %d (%q), want 200", got, body)
	}
	// Trailing-dot HubHost routes to HubHost and rewrites Host when Hub is granted.
	purlHub := p.Register("ext-hub", Rule{Hub: true})
	u, _ = url.Parse(purlHub)
	pw, _ = u.User.Password()
	auth = base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + pw))
	if got, body := rawStatus("GET http://"+HubHost+"./ok", HubHost+"."); got != http.StatusOK || body != "hub "+p.HubAddr {
		t.Fatalf("trailing-dot HubHost with Hub=true: status=%d body=%q, want 200 %q", got, body, "hub "+p.HubAddr)
	}
}

func TestUpstreamProxyErrorFailsClosed(t *testing.T) {
	wantErr := errors.New("upstream proxy configuration invalid")
	var mu sync.Mutex
	var dialed []string
	p := &Proxy{
		Resolve:  func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("192.0.2.1")}, nil },
		Upstream: func(*url.URL) (*url.URL, error) { return nil, wantErr },
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, addr)
			mu.Unlock()
			a, b := net.Pipe()
			b.Close()
			return a, nil
		},
	}

	ctx := context.Background()
	if c, err := p.dialOut(ctx, "192.0.2.1", "443", Rule{}); !errors.Is(err, wantErr) {
		if c != nil {
			c.Close()
		}
		t.Errorf("dialOut(ip): err = %v, want %v", err, wantErr)
	}
	if c, err := p.dialOut(ctx, "api.example.com", "443", Rule{}); !errors.Is(err, wantErr) {
		if c != nil {
			c.Close()
		}
		t.Errorf("dialOut(host): err = %v, want %v", err, wantErr)
	}
	if c, err := p.dialConfigured(ctx, "gateway.example.com", "443"); !errors.Is(err, wantErr) {
		if c != nil {
			c.Close()
		}
		t.Errorf("dialConfigured: err = %v, want %v", err, wantErr)
	}
	if c, err := p.runtimeDial(ctx, "api.example.com", "443", false); !errors.Is(err, wantErr) {
		if c != nil {
			c.Close()
		}
		t.Errorf("runtimeDial: err = %v, want %v", err, wantErr)
	}

	mu.Lock()
	if len(dialed) != 0 {
		mu.Unlock()
		t.Fatalf("direct dial ran after upstream error: %v", dialed)
	}
	mu.Unlock()

	// Control: when Upstream returns (nil, nil), direct dial succeeds.
	p.Upstream = func(*url.URL) (*url.URL, error) { return nil, nil }
	for _, name := range []string{"dialOut", "dialConfigured", "runtimeDial"} {
		var c net.Conn
		var err error
		switch name {
		case "dialOut":
			c, err = p.dialOut(ctx, "api.example.com", "443", Rule{})
		case "dialConfigured":
			c, err = p.dialConfigured(ctx, "gateway.example.com", "443")
		case "runtimeDial":
			c, err = p.runtimeDial(ctx, "api.example.com", "443", false)
		}
		if err != nil {
			t.Fatalf("%s with nil upstream: %v", name, err)
		}
		_ = c.Close()
	}
}

func TestForSettingsAndUnreachableEdgeCases(t *testing.T) {
	p := &Proxy{
		// HubAddr intentionally unset: hubPort defaults to 18660.
		LocalAddrs: func() []net.IP { return []net.IP{net.ParseIP("198.51.100.5")} },
		Upstream:   func(*url.URL) (*url.URL, error) { return nil, nil },
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			t.Errorf("forbidden address dialed: %s", addr)
			return nil, errors.New("forbidden dial")
		},
	}
	for _, port := range []string{"18660", "018660", "+18660", "", "http"} {
		if p.forSettings(net.ParseIP("198.51.100.5"), port) {
			t.Errorf("forSettings allowed machine IP on port %q when HubAddr is empty", port)
		}
	}

	for _, badHub := range []string{"not-a-hostport", "127.0.0.1:", "127.0.0.1:018660", "127.0.0.1:http"} {
		pBadHub := &Proxy{
			HubAddr:    badHub,
			LocalAddrs: func() []net.IP { return []net.IP{net.ParseIP("198.51.100.5")} },
		}
		if pBadHub.forSettings(net.ParseIP("198.51.100.5"), "8443") {
			t.Errorf("forSettings failed open on malformed HubAddr %q", badHub)
		}
	}

	for _, ip := range []net.IP{net.IPv4(224, 0, 1, 1), net.ParseIP("ff05::1"), net.IPv4(0, 1, 2, 3), net.IPv4bcast} {
		if !Unreachable(ip) {
			t.Errorf("Unreachable(%s) = false, want true", ip)
		}
		p.Resolve = func(context.Context, string) ([]net.IP, error) { return []net.IP{ip}, nil }
		if c, err := p.dialOut(context.Background(), "bad.example.com", "443", Rule{}); !errors.Is(err, errInternal) {
			if c != nil {
				c.Close()
			}
			t.Errorf("dialOut %s: err = %v, want errInternal", ip, err)
		}
	}
}

func TestConcurrentSamePathUnixReplacementPreservesLiveSocket(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)

	p := newProxy(t, strings.TrimPrefix(srv.URL, "http://"), nil)
	p.Register("ext", Rule{Hosts: []string{"api.example.com"}})

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "proxy.sock")

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 4 {
				if err := p.ListenUnix("ext", sock); err != nil {
					t.Errorf("ListenUnix: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	// The surviving socket path must remain bound on disk and serve requests.
	tr := &http.Transport{
		Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "sandbox"}),
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
	resp, err := (&http.Client{Transport: tr}).Get("http://api.example.com/")
	if err != nil {
		t.Fatalf("dial after concurrent ListenUnix replacement failed (socket unlinked?): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Same check for concurrent RelayUnix replacement on the same path.
	targetSock := filepath.Join(dir, "target.sock")
	tln, err := net.Listen("unix", targetSock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tln.Close() })
	go func() {
		for {
			c, err := tln.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(c, "pong")
			_ = c.Close()
		}
	}()

	relaySock := filepath.Join(dir, "relay.sock")
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 4 {
				if err := p.RelayUnix("ext", relaySock, targetSock); err != nil {
					t.Errorf("RelayUnix: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	rc, err := net.Dial("unix", relaySock)
	if err != nil {
		t.Fatalf("dial after concurrent RelayUnix replacement failed (socket unlinked?): %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil || string(got) != "pong" {
		t.Fatalf("relay read = %q, err = %v; want pong", got, err)
	}
}
