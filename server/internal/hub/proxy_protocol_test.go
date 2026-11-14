package hub

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestProxyProtocolPolicy(t *testing.T) {
	if p, err := (*ProxyProtocolConfig)(nil).policy(); p != nil || err != nil {
		t.Fatalf("unset: %v, %v", p, err)
	}
	for _, tc := range []struct {
		name      string
		cfg       ProxyProtocolConfig
		err       string
		tls, http bool
		from      string
	}{
		{"default is the TLS port", ProxyProtocolConfig{From: []string{"127.0.0.1/32"}}, "", true, false, "127.0.0.1/32"},
		{"bare address", ProxyProtocolConfig{From: []string{"127.0.0.1", "::1"}}, "", true, false, "127.0.0.1/32,::1/128"},
		{"mapped address", ProxyProtocolConfig{From: []string{"::ffff:127.0.0.1"}}, "", true, false, "127.0.0.1/32"},
		{"HTTP behind a proxy elsewhere", ProxyProtocolConfig{From: []string{"192.0.2.10"}, Listeners: []string{"tls", "HTTP"}}, "", true, true, "192.0.2.10/32"},
		{"HTTP only", ProxyProtocolConfig{From: []string{"192.0.2.10"}, Listeners: []string{"http"}}, "", false, true, "192.0.2.10/32"},
		{"empty", ProxyProtocolConfig{}, "from is empty", false, false, ""},
		{"blank", ProxyProtocolConfig{From: []string{" "}}, "from is empty", false, false, ""},
		{"not an address", ProxyProtocolConfig{From: []string{"proxy.example.com"}}, "not a network", false, false, ""},
		{"everyone, IPv4", ProxyProtocolConfig{From: []string{"0.0.0.0/0"}}, "anyone", false, false, ""},
		{"everyone, IPv6", ProxyProtocolConfig{From: []string{"::/0"}}, "anyone", false, false, ""},
		{"unknown listener", ProxyProtocolConfig{From: []string{"127.0.0.1"}, Listeners: []string{"admin"}}, "not tls or http", false, false, ""},
		{"HTTP with loopback", ProxyProtocolConfig{From: []string{"127.0.0.1/32"}, Listeners: []string{"http"}}, "hub pair", false, false, ""},
		{"HTTP with IPv6 loopback", ProxyProtocolConfig{From: []string{"::1"}, Listeners: []string{"tls", "http"}}, "hub pair", false, false, ""},
		{"HTTP with a range around loopback", ProxyProtocolConfig{From: []string{"127.0.0.0/8"}, Listeners: []string{"http"}}, "hub pair", false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := tc.cfg.policy()
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var from []string
			for _, prefix := range p.from {
				from = append(from, prefix.String())
			}
			if p.tls != tc.tls || p.http != tc.http || strings.Join(from, ",") != tc.from {
				t.Fatalf("policy = %+v", p)
			}
		})
	}
}

// syncBuffer is a log the servers' goroutines write to while the test reads it.
type syncBuffer struct {
	mu      sync.Mutex
	b       bytes.Buffer
	changed chan struct{}
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.b.Write(p)
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
	return n, err
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) WaitFor(text string, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		if strings.Contains(s.b.String(), text) {
			s.mu.Unlock()
			return true
		}
		if s.changed == nil {
			s.changed = make(chan struct{})
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			return false
		}
	}
}

func TestSyncBufferWaitFor(t *testing.T) {
	const target = "refused a connection"
	t.Run("already written", func(t *testing.T) {
		var logs syncBuffer
		_, _ = logs.Write([]byte(target))
		if !logs.WaitFor(target, time.Second) {
			t.Fatal("existing log not found")
		}
	})
	t.Run("wait for the complete delayed log", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var logs syncBuffer
			done := make(chan bool, 2)
			for range 2 {
				go func() { done <- logs.WaitFor(target, 5*time.Second) }()
			}
			synctest.Wait()
			_, _ = logs.Write([]byte("unrelated log\nrefused a "))
			synctest.Wait()
			select {
			case <-done:
				t.Fatal("wait completed before the target was written")
			default:
			}
			_, _ = logs.Write([]byte("connection\n"))
			for range 2 {
				if !<-done {
					t.Fatal("delayed log not found")
				}
			}
		})
	})
	t.Run("missing log times out despite other writes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var logs syncBuffer
			go func() {
				for range 2 {
					time.Sleep(2 * time.Second)
					_, _ = logs.Write([]byte("unrelated log\n"))
				}
			}()
			start := time.Now()
			if logs.WaitFor(target, 5*time.Second) {
				t.Fatal("missing log accepted")
			}
			if elapsed := time.Since(start); elapsed != 5*time.Second {
				t.Fatalf("timeout = %s, want 5s", elapsed)
			}
		})
	})
}

// proxyTestHub is a hub with TLS on loopback, an admin UI open to 192.0.2.0/24 only, and pp as security.proxy_protocol.
// It returns the TLS and plaintext base URLs, the pin and the hub's log.
func proxyTestHub(t *testing.T, pp *ProxyProtocolConfig) (h *Hub, tlsURL, plainURL, pin string, logs *syncBuffer) {
	t.Helper()
	t.Setenv("PERSONA_ADMIN_ACCESS", "")
	h = newPublicTestHub(t)
	logs = &syncBuffer{}
	h.logger = log.New(logs, "", 0)
	h.cfg.Public.TLSListen = "127.0.0.1:0"
	// The plaintext port is not handed back by serveHTTP: take a free one.
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.cfg.Listen = free.Addr().String()
	free.Close()
	h.cfg.Security = SecurityConfig{AuthFailures: 2, ProxyProtocol: pp}
	h.cfg.Admin = AdminConfig{Access: accessCIDRs, CIDRs: []string{"192.0.2.0/24"}}
	h.admin.container = func() bool { return false }
	h.admin.prefixes = func() []ifacePrefix { return []ifacePrefix{} }
	h.devtok.onLink = func() []netip.Prefix { return []netip.Prefix{} }
	web := filepath.Join(h.cfg.dir, "web")
	if err := os.MkdirAll(filepath.Join(web, "admin"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(web, "index.html"), []byte("CLIENT"), 0o644)
	_ = os.WriteFile(filepath.Join(web, "admin", "index.html"), []byte("ADMIN"), 0o644)
	runPublicTestHub(t, h)
	port, pin := h.tlsPin()
	return h, fmt.Sprintf("https://127.0.0.1:%d", port), "http://" + h.cfg.Listen, pin, logs
}

// proxiedClient talks to the hub over TLS pinned to pin, each connection starting with header (none when empty), as
// a proxy in front would send it.
func proxiedClient(t *testing.T, pin string, header []byte) *http.Client {
	t.Helper()
	c := pinnedTestClient(pin)
	tr := c.Transport.(*http.Transport)
	tr.DisableKeepAlives = true
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err == nil && len(header) > 0 {
			_, err = conn.Write(header)
		}
		return conn, err
	}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func v1Header(src string) []byte {
	family := "TCP4"
	if strings.Contains(src, ":") {
		family = "TCP6"
	}
	dst := "192.0.2.1"
	if family == "TCP6" {
		dst = "2001:db8::1"
	}
	return []byte(fmt.Sprintf("PROXY %s %s %s 50000 443\r\n", family, src, dst))
}

func v2Header(src, dst netip.AddrPort) []byte {
	b := []byte("\r\n\r\n\x00\r\nQUIT\n\x21\x11\x00\x0c")
	s, d := src.Addr().As4(), dst.Addr().As4()
	b = append(append(b, s[:]...), d[:]...)
	b = binary.BigEndian.AppendUint16(b, src.Port())
	return binary.BigEndian.AppendUint16(b, dst.Port())
}

type probe struct {
	status int
	code   string
	body   string
	fields int // a JSON object's number of keys
}

func get(t *testing.T, c *http.Client, url, token string) (probe, error) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return probe{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	p := probe{status: resp.StatusCode, body: string(b)}
	var obj map[string]any
	if json.Unmarshal(b, &obj) == nil {
		p.fields = len(obj)
		p.code, _ = obj["code"].(string)
	}
	return p, nil
}

func mustGet(t *testing.T, c *http.Client, url, token string) probe {
	t.Helper()
	p, err := get(t, c, url, token)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return p
}

// The proxy on this machine passes on a client from the internet: the hub judges that client, not loopback.
func TestProxyProtocolEndToEnd(t *testing.T) {
	_, base, plain, pin, logs := proxyTestHub(t, &ProxyProtocolConfig{From: []string{"127.0.0.1/32"}})

	public := proxiedClient(t, pin, v1Header("203.0.113.7"))
	if p := mustGet(t, public, base+"/api/v1/health", ""); p.status != 200 || p.fields != 5 {
		t.Fatalf("public health: %+v (want the 5 public fields)", p)
	}
	if p := mustGet(t, public, base+"/api/v1/machines", "test-token"); p.status != 403 || p.code != "master_token_local_only" {
		t.Fatalf("master token from the internet: %+v", p)
	}
	if p := mustGet(t, public, base+"/admin", ""); p.body != "CLIENT" {
		t.Fatalf("admin UI out of scope: %+v", p)
	}
	// Failed authentication counts against the client: blocked after security.auth_failures (2).
	var last probe
	for range 3 {
		last = mustGet(t, public, base+"/api/v1/machines", "bad-token")
	}
	if last.status != 429 || last.code != "rate_limited" {
		t.Fatalf("failures from the internet not limited: %+v", last)
	}
	// A different client through the same proxy is not blocked with it.
	other := proxiedClient(t, pin, v1Header("2001:db8:77::7"))
	if p := mustGet(t, other, base+"/api/v1/machines", "bad-token"); p.status != 401 {
		t.Fatalf("another client: %+v", p)
	}

	// v2, a client in admin.cidrs: the admin UI by its address, over TLS. Still not local for the master token.
	inScope := proxiedClient(t, pin, v2Header(netip.MustParseAddrPort("192.0.2.7:50000"), netip.MustParseAddrPort("192.0.2.1:443")))
	if p := mustGet(t, inScope, base+"/admin", ""); p.body != "ADMIN" {
		t.Fatalf("admin UI in scope: %+v", p)
	}
	if p := mustGet(t, inScope, base+"/api/v1/machines", "test-token"); p.status != 403 {
		t.Fatalf("master token from 192.0.2.7: %+v", p)
	}

	// Straight to the TLS port from the trusted address, no header: refused, and logged.
	if _, err := get(t, proxiedClient(t, pin, nil), base+"/api/v1/health", ""); err == nil {
		t.Fatal("a connection from the proxy's address without a header was served")
	}
	if !logs.WaitFor("proxy protocol: refused a connection from 127.0.0.1", 5*time.Second) {
		t.Fatalf("refusal not logged:\n%s", logs)
	}

	// The proxy's own client on this machine stays loopback, with a hint in the log once.
	local := proxiedClient(t, pin, v1Header("127.0.0.1"))
	for range 2 {
		if p := mustGet(t, local, base+"/admin", ""); p.body != "ADMIN" {
			t.Fatalf("loopback through the proxy: %+v", p)
		}
	}
	if n := strings.Count(logs.String(), "passed on a connection from loopback"); n != 1 {
		t.Fatalf("loopback hint logged %d times:\n%s", n, logs)
	}

	// The plaintext port is left alone: the hub's own tools reach it over loopback without a header.
	if p := mustGet(t, http.DefaultClient, plain+"/api/v1/health", ""); p.status != 200 || p.fields <= 5 {
		t.Fatalf("plaintext health from loopback: %+v", p)
	}
	if p := mustGet(t, http.DefaultClient, plain+"/api/v1/machines", "test-token"); p.status != 200 {
		t.Fatalf("master token over loopback HTTP: %+v", p)
	}
}

// Without security.proxy_protocol nothing changes: a header is garbage to the TLS server, and loopback is loopback.
// The same holds for a header from an address not in from.
func TestProxyProtocolOffOrUntrusted(t *testing.T) {
	for _, pp := range []*ProxyProtocolConfig{nil, {From: []string{"192.0.2.10/32"}}} {
		_, base, _, pin, _ := proxyTestHub(t, pp)
		if _, err := get(t, proxiedClient(t, pin, v1Header("192.0.2.7")), base+"/api/v1/health", ""); err == nil {
			t.Fatalf("%+v: a header from an untrusted address was read", pp)
		}
		direct := proxiedClient(t, pin, nil)
		if p := mustGet(t, direct, base+"/api/v1/health", ""); p.status != 200 || p.fields <= 5 {
			t.Fatalf("%+v: loopback health: %+v", pp, p)
		}
		if p := mustGet(t, direct, base+"/admin", ""); p.body != "ADMIN" {
			t.Fatalf("%+v: loopback admin UI: %+v", pp, p)
		}
	}
}

func TestProxyProtocolBadConfigStopsTheHub(t *testing.T) {
	h := newPublicTestHub(t)
	h.cfg.Public.TLSListen = "127.0.0.1:0"
	h.cfg.Security.ProxyProtocol = &ProxyProtocolConfig{From: []string{"0.0.0.0/0"}}
	if err := h.startPublic(context.Background()); err == nil || !strings.Contains(err.Error(), "security.proxy_protocol") {
		t.Fatalf("startPublic: %v", err)
	}
	if h.public.listener != nil {
		t.Fatal("a listener was opened with a bad security.proxy_protocol")
	}
}

func TestDoctorProxyProtocol(t *testing.T) {
	for _, tc := range []struct {
		pp   *ProxyProtocolConfig
		tls  string
		want string
	}{
		{nil, ":19443", ""},
		{&ProxyProtocolConfig{From: []string{"127.0.0.1/32"}}, ":19443", "正常｜security.proxy_protocol：读 127.0.0.1/32 发来的 PROXY 头"},
		{&ProxyProtocolConfig{From: []string{"127.0.0.1/32"}}, "", "有问题｜security.proxy_protocol：只作用于 TLS 端口"},
		{&ProxyProtocolConfig{From: []string{"127.0.0.1/32"}, Listeners: []string{"http"}}, ":19443", "有问题｜security.proxy_protocol：配置有误"},
	} {
		dir := t.TempDir()
		cfg := Config{Listen: ":18660", Public: PublicConfig{TLSListen: tc.tls}}
		cfg.Security.ProxyProtocol = tc.pp
		b, _ := json.Marshal(cfg)
		if err := os.WriteFile(filepath.Join(dir, "hub.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		d := &doctorReport{l: i18n.ZhHans} // the wording the report had before it spoke other languages
		d.config(dir)
		out := reportText(d)
		if tc.want == "" {
			if strings.Contains(out, "proxy_protocol") {
				t.Fatalf("unset proxy_protocol reported:\n%s", out)
			}
		} else if !strings.Contains(out, tc.want) {
			t.Fatalf("doctor output lacks %q:\n%s", tc.want, out)
		}
	}
}
