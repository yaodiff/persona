package hub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/update"
)

func TestWebDevelopmentBuildRespectsConfiguredUpdateSource(t *testing.T) {
	for _, tc := range []struct {
		name, configured, wantURL, wantFrom string
		invalid, privateOnly                bool
	}{
		{name: "custom", configured: "https://mirror.example.com/dl/", wantURL: "https://mirror.example.com/dl", wantFrom: "hub.json"},
		{name: "invalid", configured: "https://mirror.example.com/dl/latest.json", invalid: true},
		{name: "invalid in private network mode", configured: "https://mirror.example.com/dl/latest.json", invalid: true, privateOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			h.version = "web-dev-fixture"
			h.build = update.Make(h.version, update.Release, "202611171200", "")
			h.cfg.Update.URL = tc.configured
			h.cfg.Security.PrivateNetworkOnly = tc.privateOnly
			s := h.updateSource()
			if s.url != tc.wantURL || s.from != tc.wantFrom || (s.urlErr != nil) != tc.invalid {
				t.Fatalf("update source: %+v", s)
			}
			if tc.invalid {
				if key, _ := h.upd.whyNotKey(); key != "bad_source" {
					t.Fatalf("reason = %q, want bad_source", key)
				}
			}
		})
	}
}

// hub.json's update.url replaces the URL the build was made with (#4522: v0.2.1's pointed at a site that stayed on an
// old release): checks read it, and GET /api/v1/hub/update says which source is in effect and where it came from.
func TestUpdateSourceFromHubJSON(t *testing.T) {
	stale := newPublisher(t, "v0.1.17", 202611011200) // what the build points at
	fresh := newPublisher(t, "0.2.1", 202611110749)
	var asked []string
	stale.asked = func(p string) { asked = append(asked, "stale "+p) }
	fresh.asked = func(p string) { asked = append(asked, "fresh "+p) }
	h, _, _, _ := updatingHub(t, stale, update.Release)
	u := h.upd

	if v := u.view(); v.UpdateURL != stale.srv.URL+"/dl" || v.UpdateFrom != "build" || v.UpdateProxy != "" {
		t.Fatalf("the build's source: %+v", v)
	}
	h.cfg.Update.URL = fresh.srv.URL + "/dl/" // the trailing slash goes
	r, err := u.check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Manifest.Build != 202611110749 || len(asked) != 1 || asked[0] != "fresh "+testFeedPath() {
		t.Fatalf("checked %v, got build %d", asked, r.Manifest.Build)
	}
	v := u.view()
	if v.UpdateURL != fresh.srv.URL+"/dl" || v.UpdateFrom != "hub.json" || !v.CanUpdate || v.Latest == nil || !v.Latest.Newer {
		t.Fatalf("hub.json's source: %+v", v)
	}
	b, _ := json.Marshal(v)
	if !strings.Contains(string(b), `"update_url":"`+fresh.srv.URL+`/dl"`) || !strings.Contains(string(b), `"update_url_from":"hub.json"`) {
		t.Fatalf("GET /api/v1/hub/update: %s", b)
	}
	// The install script hands out the same source as the sums it read there, and so does the install line.
	if h.updateURL() != fresh.srv.URL+"/dl" || h.siteURL() != fresh.srv.URL {
		t.Fatalf("install scripts: %q, %q", h.updateURL(), h.siteURL())
	}

	// A release build made without an update URL updates itself once hub.json gives one.
	h.build.UpdateURL = ""
	if !h.selfUpdates() {
		t.Fatal("a source in hub.json alone")
	}
	h.cfg.Update.URL = ""
	if h.selfUpdates() || u.view().UpdateURL != "" || u.view().UpdateFrom != "" {
		t.Fatalf("no source at all: %+v", u.view())
	}
}

// The GitHub Releases base works as it is, and so do a private publisher's file:// copy and a mirror on this machine;
// what would not work, or would fetch unsigned releases over plain http from elsewhere, is refused with why, and a
// password in it is never shown.
func TestUpdateSourceRefusesABadURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://github.com/nwverse/persona/releases/latest/download/": "https://github.com/nwverse/persona/releases/latest/download",
		"file:///srv/persona/dl":          "file:///srv/persona/dl",
		"http://127.0.0.1:8080/dl":        "http://127.0.0.1:8080/dl",
		"http://[::1]:8080/dl":            "http://[::1]:8080/dl",
		"http://localhost/persona/dl":     "http://localhost/persona/dl",
		" https://mirror.example.com/dl ": "https://mirror.example.com/dl",
	} {
		if got, err := parseUpdateURL(strings.TrimSpace(raw)); err != nil || got != want {
			t.Errorf("%q: %q, %v", raw, got, err)
		}
	}
	p := newPublisher(t, "0.2.1", 202611110749)
	for raw, why := range map[string]string{
		"https://user:s3cret@mirror.example.com/dl":  "user or password",
		"http://mirror.example.com/dl":               "only for this machine",
		"https://mirror.example.com/dl/latest.json":  "names latest.json",
		"https://mirror.example.com/dl?token=s3cret": "query",
		"https://mirror.example.com/dl#s3cret":       "fragment",
		"ftp://mirror.example.com/dl":                "only https://",
		"mirror.example.com/dl":                      "only https://",
		"file://host/dl":                             "absolute directory",
		"https:///dl":                                "no host",
		"https://mirror.example.com:s3cret/dl":       "not a URL",
		"http://user:s3cret@127.0.0.1:8080/dl":       "user or password",
	} {
		t.Run(raw, func(t *testing.T) {
			h, _, _, _ := updatingHub(t, p, update.Release)
			var logged bytes.Buffer
			h.logger = log.New(&logged, "", 0)
			asked := 0
			p.asked = func(string) { asked++ }
			h.cfg.Update.URL = raw
			if h.selfUpdates() || h.updateURL() != "" {
				t.Fatalf("still a source: %q", h.updateURL())
			}
			v := h.upd.view()
			if v.CanUpdate || !strings.Contains(v.Why, "update.url") || !strings.Contains(v.Why, why) {
				t.Fatalf("why_not %q, want %q", v.Why, why)
			}
			if _, err := h.upd.check(context.Background()); err == nil || asked != 0 {
				t.Fatalf("checked anyway (%d requests): %v", asked, err)
			}
			err := h.upd.begin(context.Background(), false)
			h.upd.logSource()
			b, _ := json.Marshal(v)
			for _, text := range []string{string(b), logged.String(), i18n.Localize(err, i18n.Lang("en"))} {
				if strings.Contains(text, "s3cret") {
					t.Fatalf("the secret is shown: %s", text)
				}
			}
			if !strings.Contains(logged.String(), "update.url cannot be used") {
				t.Fatalf("not logged: %q", logged.String())
			}
		})
	}
}

// proxyServer is an HTTP proxy that forwards plain-http requests and remembers what it was asked and with which
// credentials.
type proxyServer struct {
	*httptest.Server
	mu    sync.Mutex
	asked []string
	auth  []string
}

func newProxyServer(t *testing.T) *proxyServer {
	p := &proxyServer{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "" {
			http.NotFound(w, r) // not a proxy request
			return
		}
		p.mu.Lock()
		p.asked = append(p.asked, r.URL.String())
		p.auth = append(p.auth, r.Header.Get("Proxy-Authorization"))
		p.mu.Unlock()
		out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *proxyServer) requests() ([]string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.asked...), append([]string(nil), p.auth...)
}

// update.proxy carries the update's checks and downloads alone (#4522: HTTPS_PROXY on the whole service broke
// everything else the hub talks to). Its user and password reach the proxy, never the log or the app.
func TestUpdateProxyOnlyForUpdates(t *testing.T) {
	pub := newPublisher(t, "0.2.1", 202611110749)
	proxy := newProxyServer(t)
	h, _, _, _ := updatingHub(t, pub, update.Release)
	var logged bytes.Buffer
	h.logger = log.New(&logged, "", 0)
	pu, _ := url.Parse(proxy.URL)
	h.cfg.Update.Proxy = "http://user:s3cret@" + pu.Host
	h.upd.client = newUpdater(h).client // the hub's own client, not the test publisher's

	r, err := h.upd.check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := update.Download(context.Background(), h.upd.client, h.updateURL(), r.Manifest.Files[runtime.GOOS+"-"+runtime.GOARCH], t.TempDir()); err != nil {
		t.Fatal(err)
	}
	asked, auth := proxy.requests()
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:s3cret"))
	if len(asked) != 2 || !strings.HasSuffix(asked[0], testFeedPath()) || !strings.HasSuffix(asked[1], ".gz") || auth[0] != wantAuth || auth[1] != wantAuth {
		t.Fatalf("through the proxy: %v %v", asked, auth)
	}
	// Anything else the hub fetches goes as it did.
	resp, err := http.Get(pub.srv.URL + testFeedPath())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if asked, _ := proxy.requests(); len(asked) != 2 {
		t.Fatalf("another client went through the update proxy: %v", asked)
	}
	v := h.upd.view()
	h.upd.logSource()
	b, _ := json.Marshal(v)
	if v.UpdateProxy != "http://"+pu.Host || strings.Contains(string(b), "s3cret") || strings.Contains(string(b), "user") {
		t.Fatalf("the app's view of the proxy: %s", b)
	}
	if !strings.Contains(logged.String(), "through http://"+pu.Host) || strings.Contains(logged.String(), "s3cret") || strings.Contains(logged.String(), "user") {
		t.Fatalf("the log: %q", logged.String())
	}
}

// An update.proxy that does not read fails every check with why, and nothing goes around it directly.
func TestUpdateProxyThatDoesNotReadFailsChecks(t *testing.T) {
	pub := newPublisher(t, "0.2.1", 202611110749)
	asked := 0
	pub.asked = func(string) { asked++ }
	h, _, _, _ := updatingHub(t, pub, update.Release)
	var logged bytes.Buffer
	h.logger = log.New(&logged, "", 0)
	h.cfg.Update.Proxy = "socks5://user:s3cret@proxy.example:1080"
	h.upd.client = newUpdater(h).client
	_, err := h.upd.check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "update.proxy") || strings.Contains(err.Error(), "s3cret") || asked != 0 {
		t.Fatalf("checked around a proxy that does not read (%d requests): %v", asked, err)
	}
	h.upd.logSource()
	b, _ := json.Marshal(h.upd.view())
	if !strings.Contains(string(b), "update.proxy") || strings.Contains(string(b), "s3cret") || strings.Contains(logged.String(), "s3cret") {
		t.Fatalf("view %s, log %q", b, logged.String())
	}
}

// Node packages from the update source go through its proxy too, unless node_packages names its own; a mirror of their
// own in node_packages.url does not.
func TestNodePackagesFollowTheUpdateSource(t *testing.T) {
	h := newTestHub(t)
	h.build.UpdateURL = "https://dl.example.com/dl"
	h.cfg.Update = UpdateConfig{URL: "https://github.com/nwverse/persona/releases/latest/download", Proxy: "http://proxy.example:3128"}
	if c := h.devicesConfig(); c.NodePackagesURL != h.cfg.Update.URL || c.NodePackagesProxy != "http://proxy.example:3128" {
		t.Fatalf("from the update source: %+v", c)
	}
	h.cfg.NodePackages.Proxy = "http://other.example:8080"
	if c := h.devicesConfig(); c.NodePackagesURL != h.cfg.Update.URL || c.NodePackagesProxy != "http://other.example:8080" {
		t.Fatalf("their own proxy: %+v", c)
	}
	h.cfg.NodePackages = NodePackagesConfig{URL: "https://lan.example.com/dl"}
	if c := h.devicesConfig(); c.NodePackagesURL != "https://lan.example.com/dl" || c.NodePackagesProxy != "" {
		t.Fatalf("their own mirror: %+v", c)
	}
}
