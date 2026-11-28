package netproxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:8080", " http://user:pass@proxy.example:3128 ", "http://[::1]:8080/", "http://proxy.example"} {
		if u, err := Parse(ok); err != nil || u == nil {
			t.Errorf("%q: %v %v", ok, u, err)
		}
	}
	if u, err := Parse("  "); u != nil || err != nil {
		t.Errorf("blank: %v %v", u, err)
	}
	// A left-out port is 80 in the URL itself, not only in net/http: mail dials u.Host for its CONNECT tunnels.
	for raw, host := range map[string]string{"http://proxy.example": "proxy.example:80", "http://user:pass@proxy.example/": "proxy.example:80",
		"http://[::1]": "[::1]:80", "http://proxy.example:3128": "proxy.example:3128"} {
		if u, err := Parse(raw); err != nil || u.Host != host || (u.User != nil) != strings.Contains(raw, "@") {
			t.Errorf("%q: %v %v, want host %q", raw, u, err, host)
		}
	}
	for _, bad := range []string{"socks5://127.0.0.1:1080", "127.0.0.1:8080", "http://", "http://h:1/x", "http://h:1?a=b", "https://h:1",
		"http://:8080", "http://proxy.example:", "http://proxy.example:0", "http://proxy.example:99999", "http://[::1]:", "http://h:1#x"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// No part of a password shows in the error, also when a '/', '?', '#' or bad escape in it makes url.Parse read part of it as
	// the port, path, query or fragment; the error still names the proxy.
	for _, c := range []struct{ raw, secret string }{
		{"socks5://user:s3cret@proxy.example:1", "s3cret"},
		{"http://user:s3cret@proxy.example:1/x", "s3cret"},
		{"http://user:s3cret@proxy.example:%zz", "s3cret"},
		{"user:s3cret@proxy.example:8080", "s3cret"},
		{"http://user:4242/ss@proxy.example:8080", "4242"},
		{"http://user:4242?ss@proxy.example:8080", "4242"},
		{"http://user:4242#ss@proxy.example:8080", "4242"},
		{"http://user:s3cr?t@proxy.example:8080", "s3cr"},
		{"http://user:s3cr#t@proxy.example:8080", "s3cr"},
		{"http://user:s3cr/t@proxy.example:8080", "s3cr"},
		{"http://user:ab%zz@proxy.example:8080", "%zz"},
	} {
		if _, err := Parse(c.raw); err == nil || strings.Contains(err.Error(), c.secret) || !strings.Contains(err.Error(), "proxy.example") {
			t.Errorf("%q: %v", c.raw, err)
		}
	}
}

// With a proxy every request (http and https alike) goes to the proxy; without one it goes straight to the server,
// even when the environment names a proxy.
func TestHTTPClient(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "" { // not a proxy request: a node's port probe (GET /, node/ports.go) asks this server directly
			http.NotFound(w, r)
			return
		}
		proxied.Add(1)
		if r.Method == http.MethodConnect {
			w.WriteHeader(http.StatusForbidden) // enough to know the client came here; no tunnel needed
			return
		}
		_, _ = io.WriteString(w, "via proxy "+r.URL.String())
	}))
	t.Cleanup(proxy.Close)
	var direct atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/origin" { // only this test's request counts: a node's port probe (node/ports.go) asks GET /
			http.NotFound(w, r)
			return
		}
		direct.Add(1)
		_, _ = io.WriteString(w, "direct")
	}))
	t.Cleanup(origin.Close)

	u, err := Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := HTTPClient(u, 5*time.Second)
	resp, err := c.Get("http://github.example/login/device/code")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "via proxy http://github.example/login/device/code" {
		t.Fatalf("http through the proxy: %q", body)
	}
	if _, err := c.Get("https://api.github.example/user"); err == nil {
		t.Fatal("https got through a proxy that refuses CONNECT")
	}
	if proxied.Load() != 2 || direct.Load() != 0 {
		t.Fatalf("proxied %d, direct %d", proxied.Load(), direct.Load())
	}

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	c = HTTPClient(nil, 5*time.Second)
	resp, err = c.Get(origin.URL + "/origin")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "direct" || proxied.Load() != 2 || direct.Load() != 1 {
		t.Fatalf("no proxy: %q, proxied %d, direct %d", body, proxied.Load(), direct.Load())
	}
	if c.Timeout != 5*time.Second {
		t.Fatalf("timeout %v", c.Timeout)
	}
}

// clearProxyEnv leaves the test with no proxy in its environment, whatever the machine running it has set.
func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"} {
		t.Setenv(k, "")
	}
}

// proxyAndOrigin are a proxy that counts what it is asked for and an origin that counts what reaches it directly.
// through makes a client's direct connections land on the origin whatever host they name, so a test can use host
// names only the proxy would know and still tell the two routes apart.
func proxyAndOrigin(t *testing.T) (proxy *httptest.Server, proxied, direct *atomic.Int32, through func(*http.Client) *http.Client) {
	t.Helper()
	proxied, direct = new(atomic.Int32), new(atomic.Int32)
	proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "" { // not a proxy request: a node's port probe (GET /, node/ports.go) asks this server directly
			http.NotFound(w, r)
			return
		}
		proxied.Add(1)
		if r.Method == http.MethodConnect {
			w.WriteHeader(http.StatusForbidden) // enough to know the client came here; no tunnel needed
			return
		}
		_, _ = io.WriteString(w, "via proxy "+r.URL.String())
	}))
	t.Cleanup(proxy.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/origin" { // only this test's requests count: a node's port probe (node/ports.go) asks GET /
			http.NotFound(w, r)
			return
		}
		direct.Add(1)
		_, _ = io.WriteString(w, "direct")
	}))
	t.Cleanup(origin.Close)
	proxyAddr, originAddr := proxy.Listener.Addr().String(), origin.Listener.Addr().String()
	through = func(c *http.Client) *http.Client {
		c.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != proxyAddr {
				addr = originAddr
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
		return c
	}
	return proxy, proxied, direct, through
}

// get is the body of a GET, "" when the request failed.
func get(c *http.Client, url string) string {
	resp, err := c.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

// EnvHTTPClient follows HTTP_PROXY, HTTPS_PROXY and NO_PROXY as they are when the client is made: direct with none
// set, through the proxy for http and https alike once they name one, and direct again for a host NO_PROXY lists.
func TestEnvHTTPClient(t *testing.T) {
	proxy, proxied, direct, through := proxyAndOrigin(t)
	clearProxyEnv(t)
	before := through(EnvHTTPClient(5 * time.Second))
	if got := get(before, "http://service.example/origin"); got != "direct" || proxied.Load() != 0 || direct.Load() != 1 {
		t.Fatalf("no proxy in the environment: %q, proxied %d, direct %d", got, proxied.Load(), direct.Load())
	}
	if before.Timeout != 5*time.Second {
		t.Fatalf("timeout %v", before.Timeout)
	}

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	c := through(EnvHTTPClient(5 * time.Second))
	if got := get(c, "http://service.example/origin"); got != "via proxy http://service.example/origin" {
		t.Fatalf("http through the environment's proxy: %q", got)
	}
	if _, err := c.Get("https://service.example/origin"); err == nil {
		t.Fatal("https got through a proxy that refuses CONNECT")
	}
	if proxied.Load() != 2 || direct.Load() != 1 {
		t.Fatalf("proxied %d, direct %d", proxied.Load(), direct.Load())
	}
	// The client made before the variables were set keeps what it read then.
	if got := get(before, "http://service.example/origin"); got != "direct" || proxied.Load() != 2 {
		t.Fatalf("a client made earlier: %q, proxied %d", got, proxied.Load())
	}

	t.Setenv("NO_PROXY", "service.example")
	c = through(EnvHTTPClient(5 * time.Second))
	if got := get(c, "http://service.example/origin"); got != "direct" {
		t.Fatalf("a host NO_PROXY lists: %q", got)
	}
	if got := get(c, "http://other.example/origin"); got != "via proxy http://other.example/origin" {
		t.Fatalf("a host NO_PROXY does not list: %q", got)
	}
	if proxied.Load() != 3 || direct.Load() != 3 {
		t.Fatalf("proxied %d, direct %d", proxied.Load(), direct.Load())
	}

	// Both spellings set: upper case wins, as it does for http.ProxyFromEnvironment in the same process.
	t.Setenv("NO_PROXY", "")
	t.Setenv("http_proxy", "http://lower-case.example:3128")
	if got := get(through(EnvHTTPClient(5*time.Second)), "http://service.example/origin"); got != "via proxy http://service.example/origin" {
		t.Fatalf("HTTP_PROXY over http_proxy: %q", got)
	}
	// A value Go cannot read as a proxy is ignored, as in the rest of the process: the request goes direct.
	t.Setenv("http_proxy", "")
	t.Setenv("HTTP_PROXY", "http://user:s3%zzcret@proxy.example:3128")
	if got := get(through(EnvHTTPClient(5*time.Second)), "http://service.example/origin"); got != "direct" {
		t.Fatalf("an unreadable HTTP_PROXY: %q", got)
	}
}

// An Egress with no proxy of its own follows the environment; one with a proxy uses it whatever the environment
// says; a malformed one gives no client at all, with or without a proxy in the environment.
func TestEgressHTTPClient(t *testing.T) {
	proxy, proxied, direct, through := proxyAndOrigin(t)
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "" { // a node's port probe, as above
			http.NotFound(w, r)
			return
		}
		t.Errorf("went through the environment's proxy: %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(trap.Close)

	clearProxyEnv(t)
	c, err := Egress{}.HTTPClient(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := get(through(c), "http://service.example/origin"); got != "direct" || proxied.Load() != 0 || direct.Load() != 1 {
		t.Fatalf("no proxy anywhere: %q, proxied %d, direct %d", got, proxied.Load(), direct.Load())
	}

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	if c, err = (Egress{}).HTTPClient(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if got := get(through(c), "http://service.example/origin"); got != "via proxy http://service.example/origin" || proxied.Load() != 1 {
		t.Fatalf("the environment's proxy: %q, proxied %d", got, proxied.Load())
	}

	t.Setenv("HTTP_PROXY", trap.URL)
	t.Setenv("HTTPS_PROXY", trap.URL)
	if c, err = (Egress{Proxy: proxy.URL}).HTTPClient(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if got := get(c, "http://service.example/origin"); got != "via proxy http://service.example/origin" || proxied.Load() != 2 {
		t.Fatalf("its own proxy over the environment's: %q, proxied %d", got, proxied.Load())
	}
	if c, err = (Egress{Proxy: "socks5://user:s3cret@proxy.example:1080"}).HTTPClient(5 * time.Second); c != nil || err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("a malformed proxy: %v %v", c, err)
	}
	if direct.Load() != 1 {
		t.Fatalf("direct %d", direct.Load())
	}
}
