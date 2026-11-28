package github

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// With github.proxy set, the device flow (code and polling) and the REST API all go to the proxy, addressed to
// GitHub's hosts; nothing goes to GitHub directly or through the proxy the environment names.
func TestProxyCarriesEverything(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var seen []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "" { // not a proxy request: a node's port probe (GET /, node/ports.go) asks this server directly
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Host+r.URL.Path)
		mu.Unlock()
		switch r.URL.Host + r.URL.Path {
		case "github.example/login/device/code":
			if r.Form.Get("client_id") != ghCLIClientID {
				t.Errorf("client_id %q", r.Form.Get("client_id"))
			}
			_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"PRXY-0001","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`))
		case "github.example/login/oauth/access_token":
			_, _ = w.Write([]byte(`{"access_token":"gho_viaproxy"}`))
		case "api.github.example/user":
			w.Header().Set("X-OAuth-Scopes", "repo")
			_, _ = w.Write([]byte(`{"login":"example-user"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(proxy.Close)
	// github.proxy wins over the environment's proxy: nothing may reach this one.
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
	t.Setenv("HTTP_PROXY", trap.URL)
	t.Setenv("HTTPS_PROXY", trap.URL)

	var cfg Config
	if err := json.Unmarshal([]byte(`{"proxy":"`+proxy.URL+`"}`), &cfg); err != nil || cfg.Proxy != proxy.URL {
		t.Fatalf("github.proxy from hub.json: %+v %v", cfg, err)
	}
	c := New(cfg, "", nil)
	c.web, c.api = "http://github.example", "http://api.github.example" // hosts only the proxy knows
	c.UseSecrets(&memSecrets{})
	p, err := c.LoginStep(ctx, "", nil)
	if err != nil || p.UserCode != "PRXY-0001" {
		t.Fatalf("code through the proxy: %+v %v", p, err)
	}
	if p, err = c.LoginStep(ctx, "device", nil); err != nil || !p.Done || p.Account != "example-user" {
		t.Fatalf("signed in through the proxy: %+v %v", p, err)
	}
	mu.Lock()
	got := strings.Join(seen, ", ")
	mu.Unlock()
	want := "POST github.example/login/device/code, POST github.example/login/oauth/access_token, GET api.github.example/user"
	if got != want {
		t.Fatalf("through the proxy:\n got %s\nwant %s", got, want)
	}
}

// Without github.proxy the connector follows the process's proxy environment: the device flow goes to the proxy
// HTTP_PROXY names, addressed to GitHub's host.
func TestNoProxyFollowsEnvironment(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "" { // not a proxy request: a node's port probe (GET /, node/ports.go) asks this server directly
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Host+r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"ENVP-0003","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`))
	}))
	t.Cleanup(proxy.Close)
	clearProxyEnv(t)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	c := New(Config{}, "", nil)
	c.web, c.api = "http://github.example", "http://api.github.example" // hosts only the proxy knows
	c.UseSecrets(&memSecrets{})
	if p, err := c.LoginStep(context.Background(), "", nil); err != nil || p.UserCode != "ENVP-0003" {
		t.Fatalf("code through the environment's proxy: %+v %v", p, err)
	}
	mu.Lock()
	got := strings.Join(seen, ", ")
	mu.Unlock()
	if want := "POST github.example/login/device/code"; got != want {
		t.Fatalf("through the environment's proxy:\n got %s\nwant %s", got, want)
	}
}

// With neither github.proxy nor a proxy in the environment, and for a host NO_PROXY lists, the connector talks to
// GitHub directly. The host is one no proxy rule exempts by itself (a loopback address always goes direct), reached
// through a dialer that lands every connection on the fake GitHub.
func TestNoProxyAnywhereIsDirect(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"DRCT-0002","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`))
	}))
	t.Cleanup(gh.Close)
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "" { // a node's port probe, as above
			http.NotFound(w, r)
			return
		}
		t.Errorf("went through the environment's proxy: %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(trap.Close)
	direct := func(t *testing.T) {
		t.Helper()
		c := New(Config{}, "", nil)
		c.web, c.api = "http://github.example", "http://api.github.example"
		trapAddr := trap.Listener.Addr().String()
		c.client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != trapAddr {
				addr = gh.Listener.Addr().String()
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
		c.UseSecrets(&memSecrets{})
		if p, err := c.LoginStep(context.Background(), "", nil); err != nil || p.UserCode != "DRCT-0002" {
			t.Fatalf("direct: %+v %v", p, err)
		}
	}
	t.Run("no proxy in the environment", func(t *testing.T) {
		clearProxyEnv(t)
		direct(t)
	})
	t.Run("a host NO_PROXY lists", func(t *testing.T) {
		clearProxyEnv(t)
		t.Setenv("HTTP_PROXY", trap.URL)
		t.Setenv("HTTPS_PROXY", trap.URL)
		t.Setenv("NO_PROXY", "github.example")
		direct(t)
	})
}

// clearProxyEnv leaves the test with no proxy in its environment, whatever the machine running it has set.
func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"} {
		t.Setenv(k, "")
	}
}

// A malformed github.proxy fails every request and says so; it never falls back to direct, and never shows the
// proxy's password.
func TestBadProxy(t *testing.T) {
	ctx := context.Background()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		t.Errorf("went direct: %s", r.URL)
	}))
	t.Cleanup(gh.Close)
	c := New(Config{Proxy: "socks5://user:s3cret@127.0.0.1:1080"}, "", nil)
	c.web, c.api = gh.URL, gh.URL
	c.UseSecrets(&memSecrets{data: []byte(`{"token":"gho_example"}`)})
	if _, err := c.LoginStep(ctx, "", nil); err == nil || !strings.Contains(err.Error(), "github.proxy") || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("sign-in: %v", err)
	}
	c.check(ctx)
	if st := c.Status(ctx); st.State != connectorwire.StateNotConfigured || !strings.Contains(st.Error, "github.proxy") {
		t.Fatalf("status: %+v", st)
	}
}
