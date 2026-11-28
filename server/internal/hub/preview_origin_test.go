package hub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/streams"
)

// generateTestCertFiles creates a fresh ECDSA P-256 self-signed cert/key pair covering dnsNames.
func generateTestCertFiles(t *testing.T, dir, prefix string, dnsNames []string) (certFile, keyFile string, serial string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sn, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: dnsNames[0]},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile = filepath.Join(dir, prefix+".crt")
	keyFile = filepath.Join(dir, prefix+".key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile, sn.String()
}

type previewTokenStore struct {
	*tokenStore
}

func (s *previewTokenStore) StopDeviceSessions(context.Context, time.Time) error  { return nil }
func (s *previewTokenStore) PruneDeviceSessions(context.Context, time.Time) error { return nil }
func (s *previewTokenStore) PruneLockTests(context.Context, time.Time) error      { return nil }
func (s *previewTokenStore) RecoverLockTests(context.Context, time.Time) error    { return nil }
func (s *previewTokenStore) NextLockTestDue(context.Context) (*time.Time, error)  { return nil, nil }

func newPreviewOriginTestHub(t *testing.T, dir string, store *previewTokenStore) *Hub {
	t.Helper()
	h := newTestHub(t)
	h.cfg.dir = dir
	h.cfg.Token = "master-secret-token"
	h.cfg.PublicURL = "https://hub.example.test"
	h.cfg.Preview = PreviewConfig{Domain: "preview.example.test"}
	h.store = store
	return h
}

func newPreviewTokenStore() *previewTokenStore {
	return &previewTokenStore{
		tokenStore: &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}},
	}
}

func addTestDeviceToken(t *testing.T, store *previewTokenStore, id, kind, role string) string {
	t.Helper()
	tok := "jd_" + randomHex(24)
	dt := db.DeviceToken{ID: id, Name: id, Kind: kind, Role: role}
	if role == roleReadOnly {
		exp := time.Now().Add(time.Hour)
		dt.ExpiresAt = &exp
	}
	if err := store.CreateDeviceToken(context.Background(), dt, hashToken(tok)); err != nil {
		t.Fatal(err)
	}
	return tok
}

type previewGrantReply struct {
	URL       string `json:"url"`
	Origin    string `json:"origin"`
	ExpiresAt string `json:"expires_at"`
}

func requestPreviewGrant(t *testing.T, client *http.Client, hubBase, token, mid string, port int, p, originHdr string) (*http.Response, previewGrantReply) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"port": port, "path": p})
	req, err := http.NewRequest(http.MethodPost, hubBase+"/api/v1/machines/"+mid+"/preview/grant", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if originHdr != "" {
		req.Header.Set("Origin", originHdr)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out previewGrantReply
	_ = json.Unmarshal(raw, &out)
	return res, out
}

func TestPreviewOriginDisabledByDefault(t *testing.T) {
	h, srv := portHub(t)
	_, port := devServer(t)
	portNode(t, h, []proto.Session{{SessionID: "s1", Ports: []proto.Port{{Port: port}}}})

	if slices.Contains(h.features(), featurePreviewOrigin) {
		t.Fatalf("preview_origin must not be advertised when preview.domain is unset: %v", h.features())
	}
	res, _ := requestPreviewGrant(t, http.DefaultClient, srv.URL, "test-token", "m1", port, "/", "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("grant when disabled: got %d, want 404", res.StatusCode)
	}
	// Requests carrying an arbitrary preview-like Host behave normally when disabled.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/health", nil)
	req.Host = "p-abc.preview.example.test"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disabled hub should not intercept unknown hosts: got %d, want 200", resp.StatusCode)
	}
}

func TestPreviewOriginConfigValidation(t *testing.T) {
	validBase := func() *Config {
		return &Config{
			Listen:    ":18660",
			PublicURL: "https://hub.example.com",
			Public:    PublicConfig{TLSListen: ":18662", Advertise: []string{"https://hub.example.com:18662"}},
		}
	}
	if err := validBase().checkPreview(); err != nil {
		t.Fatalf("empty preview config failed: %v", err)
	}
	okReverseProxy := validBase()
	okReverseProxy.Public.TLSListen = ""
	okReverseProxy.Preview = PreviewConfig{Domain: "preview.example.com", Port: 8443, AllowedAncestors: []string{"https://app.example.com"}}
	if err := okReverseProxy.checkPreview(); err != nil {
		t.Fatalf("valid reverse-proxy preview config failed: %v", err)
	}
	okHubTLS := validBase()
	okHubTLS.Preview = PreviewConfig{Domain: "preview.example.com", CertFile: "/a.crt", KeyFile: "/a.key"}
	if err := okHubTLS.checkPreview(); err != nil {
		t.Fatalf("valid hub-TLS preview config failed: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"wildcard prefix", func(c *Config) { c.Preview.Domain = "*.preview.example.com" }},
		{"scheme in domain", func(c *Config) { c.Preview.Domain = "https://preview.example.com" }},
		{"port in domain", func(c *Config) { c.Preview.Domain = "preview.example.com:443" }},
		{"ip literal", func(c *Config) { c.Preview.Domain = "127.0.0.1" }},
		{"single label", func(c *Config) { c.Preview.Domain = "localhost" }},
		{"other fields without domain", func(c *Config) { c.Preview.Port = 8443 }},
		{"cert without key", func(c *Config) { c.Preview = PreviewConfig{Domain: "preview.example.com", CertFile: "/a.crt"} }},
		{"cert without tls_listen", func(c *Config) {
			c.Public.TLSListen = ""
			c.Preview = PreviewConfig{Domain: "preview.example.com", CertFile: "/a.crt", KeyFile: "/a.key"}
		}},
		{"bad port", func(c *Config) { c.Preview = PreviewConfig{Domain: "preview.example.com", Port: 70000} }},
		{"overlaps public_url", func(c *Config) { c.Preview.Domain = "example.com" }},
		{"equals public_url host", func(c *Config) { c.Preview.Domain = "hub.example.com" }},
		{"overlaps advertise", func(c *Config) {
			c.Public.Advertise = []string{"https://sub.preview.example.com:18662"}
			c.Preview.Domain = "preview.example.com"
		}},
		{"overlaps sharing", func(c *Config) {
			c.Sharing = &SharingConfig{PublicOrigin: "https://share.preview.example.com"}
			c.Preview.Domain = "preview.example.com"
		}},
		{"invalid ancestor", func(c *Config) {
			c.Preview = PreviewConfig{Domain: "preview.example.com", AllowedAncestors: []string{"https://hub.example.com/path"}}
		}},
		{"ancestor under preview domain", func(c *Config) {
			c.Preview = PreviewConfig{Domain: "preview.example.com", AllowedAncestors: []string{"https://x.preview.example.com"}}
		}},
	}
	for _, tc := range cases {
		c := validBase()
		tc.mutate(c)
		if err := c.checkPreview(); err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}
}

func TestPreviewOriginLabelDeterminismAndIsolation(t *testing.T) {
	dir := t.TempDir()
	h1 := newPreviewOriginTestHub(t, dir, newPreviewTokenStore())
	l1, err := h1.previewLabel("m1", 5173)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^p-[a-z2-7]{24}$`).MatchString(l1) {
		t.Fatalf("unexpected label format %q", l1)
	}
	if strings.Contains(l1, "5173") || strings.Contains(l1, "m1") {
		t.Fatalf("label must be opaque, got %q", l1)
	}
	l2, err := h1.previewLabel("m1", 5174)
	if err != nil || l2 == l1 {
		t.Fatalf("different ports must get distinct labels: %q vs %q (%v)", l1, l2, err)
	}
	l3, err := h1.previewLabel("m2", 5173)
	if err != nil || l3 == l1 {
		t.Fatalf("different machines must get distinct labels: %q vs %q (%v)", l1, l3, err)
	}
	// Simulated hub restart sharing the same secret.key dir produces the identical label.
	h2 := newPreviewOriginTestHub(t, dir, newPreviewTokenStore())
	l1After, err := h2.previewLabel("m1", 5173)
	if err != nil || l1After != l1 {
		t.Fatalf("label drifted across restart: before=%q after=%q (%v)", l1, l1After, err)
	}
}

// inspectDevServer captures request headers seen by the upstream dev server and supports HTTP,
// dev-server cookies, 302 redirects, CSP/X-Frame-Options override checks, and Vite-like HMR WS.
type inspectedUpstream struct {
	mu      sync.Mutex
	httpReq []http.Header
	hosts   []string
	wsReq   []http.Header
	wsHosts []string
}

func startInspectDevServer(t *testing.T) (*httptest.Server, int, *inspectedUpstream) {
	t.Helper()
	rec := &inspectedUpstream{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.httpReq = append(rec.httpReq, r.Header.Clone())
		rec.hosts = append(rec.hosts, r.Host)
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'self'")
		http.SetCookie(w, &http.Cookie{Name: previewCookieName, Value: "attacker-overwrite", Path: "/", Secure: true})
		http.SetCookie(w, &http.Cookie{Name: "dev_flag", Value: "ok", Path: "/"})
		fmt.Fprintf(w, "path=%s query=%s host=%s", r.URL.Path, r.URL.RawQuery, r.Host)
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "s3cr3t", Path: "/"})
		http.Redirect(w, r, "/me?ok=1", http.StatusFound)
	})
	mux.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.httpReq = append(rec.httpReq, r.Header.Clone())
		rec.hosts = append(rec.hosts, r.Host)
		rec.mu.Unlock()
		c, err := r.Cookie("sid")
		if err != nil {
			http.Error(w, "no sid", http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, "me=%s query=%s", c.Value, r.URL.RawQuery)
	})
	mux.HandleFunc("/hmr", func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.wsReq = append(rec.wsReq, r.Header.Clone())
		rec.wsHosts = append(rec.wsHosts, r.Host)
		rec.mu.Unlock()
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
			Subprotocols:       []string{"vite-hmr"},
		})
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(1 << 20)
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), typ, append([]byte("echo:"), b...)); err != nil {
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, srv.Listener.Addr().(*net.TCPAddr).Port, rec
}

// hostMappedClient dials every request to targetAddr regardless of the URL hostname, letting
// Go tests exercise arbitrary *.preview.example.test and hub.example.test hosts over real TLS/HTTP.
func hostMappedClient(targetAddr string, tlsCfg *tls.Config, jar http.CookieJar, followRedirects bool) *http.Client {
	tr := &http.Transport{
		Proxy:           nil,
		TLSClientConfig: tlsCfg,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, targetAddr)
		},
	}
	c := &http.Client{Transport: tr, Jar: jar, Timeout: 10 * time.Second}
	if !followRedirects {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c
}

func TestPreviewOriginProxyHTTPAndWebSocket(t *testing.T) {
	dir := t.TempDir()
	store := newPreviewTokenStore()
	devTok := addTestDeviceToken(t, store, "dev_web1", "web", roleFull)

	certDir := t.TempDir()
	wildCert, wildKey, _ := generateTestCertFiles(t, certDir, "wild", []string{"*.preview.example.test"})
	hubCert, hubKey, _ := generateTestCertFiles(t, certDir, "hub", []string{"hub.example.test"})

	h := newPreviewOriginTestHub(t, dir, store)
	h.cfg.Listen = "127.0.0.1:0"
	h.cfg.Public = PublicConfig{TLSListen: "127.0.0.1:0", CertFile: hubCert, KeyFile: hubKey}
	h.cfg.Preview = PreviewConfig{
		Domain:   "preview.example.test",
		CertFile: wildCert,
		KeyFile:  wildKey,
	}
	_, port, rec := startInspectDevServer(t)
	portNode(t, h, []proto.Session{{SessionID: "s1", Ports: []proto.Port{{Port: port, Title: "Dev"}}}})
	runPublicTestHub(t, h)

	tlsPort, _ := h.tlsPin()
	tlsAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(tlsPort))
	hubOrigin := "https://hub.example.test:" + strconv.Itoa(tlsPort)

	// Feature flag announced on hello and trusted health.
	if !slices.Contains(h.features(), featurePreviewOrigin) {
		t.Fatalf("expected %q in features: %v", featurePreviewOrigin, h.features())
	}
	if feats := h.hello().Features; !slices.Contains(feats, featurePreviewOrigin) {
		t.Fatalf("expected %q in hello.features: %v", featurePreviewOrigin, feats)
	}

	insecureTLS := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
	noRedir := hostMappedClient(tlsAddr, insecureTLS, nil, false)
	defer noRedir.CloseIdleConnections()

	// 1. Grant preview URL with query & fragment.
	res, grant := requestPreviewGrant(t, noRedir, hubOrigin, devTok, "m1", port, "/app/page?x=1&y=2#sec", hubOrigin)
	if res.StatusCode != http.StatusOK || grant.URL == "" || grant.Origin == "" || grant.ExpiresAt == "" {
		t.Fatalf("grant failed: status=%d grant=%+v", res.StatusCode, grant)
	}
	if !strings.HasPrefix(grant.URL, grant.Origin+previewAuthPath+"?code=") {
		t.Fatalf("unexpected grant URL: %q (origin=%q)", grant.URL, grant.Origin)
	}
	if strings.Contains(grant.URL, devTok) || strings.Contains(grant.URL, h.cfg.Token) {
		t.Fatalf("grant URL leaked hub token: %q", grant.URL)
	}

	// 2. Redeem one-time code -> 302 with strict host-only HttpOnly Secure SameSite=None Partitioned cookie.
	redeemRes, err := noRedir.Get(grant.URL)
	if err != nil {
		t.Fatal(err)
	}
	redeemRes.Body.Close()
	if redeemRes.StatusCode != http.StatusFound {
		t.Fatalf("redeem status=%d, want 302", redeemRes.StatusCode)
	}
	if loc := redeemRes.Header.Get("Location"); loc != "/app/page?x=1&y=2#sec" {
		t.Fatalf("redeem Location=%q, want /app/page?x=1&y=2#sec", loc)
	}
	if strings.Contains(redeemRes.Header.Get("Location"), "code=") {
		t.Fatalf("code leaked into Location: %q", redeemRes.Header.Get("Location"))
	}
	if rp := redeemRes.Header.Get("Referrer-Policy"); rp != "no-referrer" {
		t.Fatalf("redeem Referrer-Policy=%q, want no-referrer", rp)
	}
	if csp := redeemRes.Header.Get("Content-Security-Policy"); !strings.HasPrefix(csp, "frame-ancestors ") || !slices.Contains(strings.Fields(csp), hubOrigin) {
		t.Fatalf("redeem CSP=%q, want frame-ancestors containing %s", csp, hubOrigin)
	}
	var previewCookie *http.Cookie
	for _, c := range redeemRes.Cookies() {
		if c.Name == previewCookieName {
			previewCookie = c
		}
	}
	if previewCookie == nil {
		t.Fatalf("missing %s cookie in %v", previewCookieName, redeemRes.Header.Values("Set-Cookie"))
	}
	if !previewCookie.HttpOnly || !previewCookie.Secure || previewCookie.SameSite != http.SameSiteNoneMode ||
		!previewCookie.Partitioned || previewCookie.Domain != "" || previewCookie.Path != "/" {
		t.Fatalf("cookie attributes wrong: %+v", previewCookie)
	}

	// 3. Proxy HTTP GET with extra Authorization + admin cookies -> all stripped before dev server!
	jar, _ := cookiejar.New(nil)
	uOrigin, _ := url.Parse(grant.Origin)
	jar.SetCookies(uOrigin, []*http.Cookie{previewCookie})
	browser := hostMappedClient(tlsAddr, insecureTLS, jar, true)
	defer browser.CloseIdleConnections()

	req, _ := http.NewRequest(http.MethodGet, grant.Origin+"/app/page?x=1&y=2", nil)
	req.Header.Set("Authorization", "Bearer "+devTok)
	req.Header.Add("Cookie", "persona_admin_tls=secret-admin; dev_in=keepme")
	req.Header.Set("X-Persona-CSRF", "csrf-secret")
	resp, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	wantHost := "localhost:" + strconv.Itoa(port)
	if resp.StatusCode != http.StatusOK || string(body) != "path=/app/page query=x=1&y=2 host="+wantHost {
		t.Fatalf("proxied GET: status=%d body=%q", resp.StatusCode, string(body))
	}
	// Verify response framing headers: X-Frame-Options removed, upstream frame-ancestors replaced, other CSP preserved.
	if resp.Header.Get("X-Frame-Options") != "" {
		t.Fatalf("upstream X-Frame-Options was not stripped: %q", resp.Header.Get("X-Frame-Options"))
	}
	allCSP := strings.Join(resp.Header.Values("Content-Security-Policy"), " | ")
	if !strings.Contains(allCSP, "default-src 'self'") || strings.Contains(allCSP, "frame-ancestors 'self'") || !strings.Contains(allCSP, "frame-ancestors ") || !strings.Contains(allCSP, hubOrigin) {
		t.Fatalf("unexpected CSP headers: %v", resp.Header.Values("Content-Security-Policy"))
	}
	// Verify dev server could NOT overwrite __Host-persona_preview, while its own dev_flag cookie passed through.
	for _, sc := range resp.Cookies() {
		if sc.Name == previewCookieName {
			t.Fatalf("dev server Set-Cookie overwrote %s", previewCookieName)
		}
	}
	rec.mu.Lock()
	if len(rec.httpReq) != 1 || rec.hosts[0] != wantHost {
		t.Fatalf("upstream host=%v", rec.hosts)
	}
	upHdr := rec.httpReq[0]
	rec.mu.Unlock()
	if upHdr.Get("Authorization") != "" || upHdr.Get("X-Persona-Csrf") != "" {
		t.Fatalf("leaked auth headers to dev server: %v", upHdr)
	}
	if ck := upHdr.Get("Cookie"); strings.Contains(ck, previewCookieName) || strings.Contains(ck, "persona_admin_tls") || strings.Contains(ck, devTok) || !strings.Contains(ck, "dev_in=keepme") {
		t.Fatalf("unexpected upstream Cookie header: %q", ck)
	}

	// 4. Dev server 302 redirect + dev server Cookie round-trip (/login -> /me?ok=1).
	resp, err = browser.Get(grant.Origin + "/login")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/me" || string(body) != "me=s3cr3t query=ok=1" {
		t.Fatalf("dev login redirect failed: status=%d url=%s body=%q", resp.StatusCode, resp.Request.URL, string(body))
	}

	// 5. HMR WebSocket proxy: Origin rewritten to http://localhost:<port>, credentials stripped, data echoed.
	wsURL := "wss" + strings.TrimPrefix(grant.Origin, "https") + "/hmr"
	wsClient := hostMappedClient(tlsAddr, insecureTLS, nil, false)
	defer wsClient.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsHeaders := http.Header{
		"Origin":        {grant.Origin},
		"Cookie":        {previewCookieName + "=" + previewCookie.Value + "; sid=s3cr3t"},
		"Authorization": {"Bearer " + devTok},
	}
	c, wsResp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient:   wsClient,
		HTTPHeader:   wsHeaders,
		Subprotocols: []string{"vite-hmr"},
	})
	if err != nil {
		t.Fatalf("HMR websocket dial failed: %v", err)
	}
	defer c.CloseNow()
	if wsResp.Header.Get("Sec-WebSocket-Protocol") != "vite-hmr" {
		t.Fatalf("subprotocol lost: %q", wsResp.Header.Get("Sec-WebSocket-Protocol"))
	}
	c.SetReadLimit(1 << 20)
	for _, msg := range []string{"hmr-update", strings.Repeat("z", 128<<10)} {
		if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
			t.Fatal(err)
		}
		_, got, err := c.Read(ctx)
		if err != nil || string(got) != "echo:"+msg {
			t.Fatalf("ws echo mismatch: err=%v got=%.20q", err, got)
		}
	}
	rec.mu.Lock()
	if len(rec.wsReq) != 1 || rec.wsHosts[0] != wantHost {
		t.Fatalf("upstream ws host=%v", rec.wsHosts)
	}
	upWS := rec.wsReq[0]
	rec.mu.Unlock()
	if got := upWS.Get("Origin"); got != "http://"+wantHost {
		t.Fatalf("upstream ws Origin=%q, want http://%s", got, wantHost)
	}
	if upWS.Get("Authorization") != "" {
		t.Fatalf("leaked Authorization on ws upgrade: %q", upWS.Get("Authorization"))
	}
	if ck := upWS.Get("Cookie"); strings.Contains(ck, previewCookieName) || !strings.Contains(ck, "sid=s3cr3t") {
		t.Fatalf("unexpected upstream ws Cookie: %q", ck)
	}

	// 6. Cross-origin WebSocket from a foreign Origin must be rejected (CSWSH prevention).
	badWSHeaders := http.Header{
		"Origin": {"https://evil.example"},
		"Cookie": {previewCookieName + "=" + previewCookie.Value},
	}
	if badC, badResp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: wsClient, HTTPHeader: badWSHeaders}); err == nil {
		badC.CloseNow()
		t.Fatal("cross-origin WebSocket was accepted")
	} else if badResp == nil || badResp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin WebSocket: err=%v resp=%v, want 403", err, badResp)
	}
}

func TestPreviewOriginHostIsolationAndNoCORS(t *testing.T) {
	dir := t.TempDir()
	store := newPreviewTokenStore()
	devTok := addTestDeviceToken(t, store, "dev_web2", "web", roleFull)

	h := newPreviewOriginTestHub(t, dir, store)
	_, port, _ := startInspectDevServer(t)
	portNode(t, h, []proto.Session{{SessionID: "s1", Ports: []proto.Port{{Port: port}}}})

	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)

	noRedir := hostMappedClient(srv.Listener.Addr().String(), nil, nil, false)
	defer noRedir.CloseIdleConnections()

	// Grant and redeem a real session cookie (simulating reverse-proxy loopback mode).
	res, grant := requestPreviewGrant(t, noRedir, "http://hub.example.test", devTok, "m1", port, "/", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("grant status=%d", res.StatusCode)
	}
	redeemHTTP := strings.Replace(grant.URL, "https://", "http://", 1)
	redeemRes, err := noRedir.Get(redeemHTTP)
	if err != nil {
		t.Fatal(err)
	}
	redeemRes.Body.Close()
	var previewCookie *http.Cookie
	for _, c := range redeemRes.Cookies() {
		if c.Name == previewCookieName {
			previewCookie = c
		}
	}
	if previewCookie == nil {
		t.Fatal("expected preview cookie")
	}
	previewHTTP := strings.Replace(grant.Origin, "https://", "http://", 1)

	// /api and /api/* on the preview host MUST always return 404 (both with and without cookie).
	for _, p := range []string{"/api", "/api/", "/api/v1/health", "/api/v1/machines", "/api/v1/auth/tokens", "/api/public/agent-shares/abc", "/foo/../api/v1/health"} {
		for _, withCookie := range []bool{false, true} {
			req, _ := http.NewRequest(http.MethodGet, previewHTTP+p, nil)
			if withCookie {
				req.AddCookie(previewCookie)
			}
			resp, err := noRedir.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("preview host %s (cookie=%v): got %d, want 404", p, withCookie, resp.StatusCode)
			}
		}
	}

	// Bare preview.domain and nested subdomains must also return 404 (never reach hub routes).
	for _, host := range []string{"preview.example.test", "a.b.preview.example.test"} {
		req, _ := http.NewRequest(http.MethodGet, "http://"+host+"/api/v1/health", nil)
		resp, err := noRedir.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("host %s /api/v1/health: got %d, want 404", host, resp.StatusCode)
		}
	}

	// Hub main API must NEVER accept cookies as ambient credentials and must NEVER emit CORS headers for preview origin.
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		req, _ := http.NewRequest(method, "http://hub.example.test/api/v1/machines", nil)
		req.Header.Set("Origin", grant.Origin)
		req.Header.Set("Sec-Fetch-Site", "same-site")
		req.AddCookie(previewCookie)
		resp, err := noRedir.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("hub API %s with preview cookie only: got %d, want 401", method, resp.StatusCode)
		}
		if acao := resp.Header.Get("Access-Control-Allow-Origin"); acao != "" {
			t.Errorf("hub API emitted Access-Control-Allow-Origin=%q for preview origin", acao)
		}
	}
	// Even /api/v1/health must never emit Access-Control-Allow-Origin for preview origin.
	req, _ := http.NewRequest(http.MethodGet, "http://hub.example.test/api/v1/health", nil)
	req.Header.Set("Origin", grant.Origin)
	resp, err := noRedir.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if acao := resp.Header.Get("Access-Control-Allow-Origin"); acao != "" {
		t.Errorf("hub health emitted Access-Control-Allow-Origin=%q", acao)
	}
	// Admin writes from preview same-site origin are rejected with 403 csrf.
	adminReq, _ := http.NewRequest(http.MethodPost, "http://hub.example.test/api/v1/admin/login", strings.NewReader(`{"password":"x"}`))
	adminReq.Header.Set("Content-Type", "application/json")
	adminReq.Header.Set("Origin", grant.Origin)
	adminReq.Header.Set("Sec-Fetch-Site", "same-site")
	adminResp, err := noRedir.Do(adminReq)
	if err != nil {
		t.Fatal(err)
	}
	adminResp.Body.Close()
	if adminResp.StatusCode != http.StatusForbidden {
		t.Errorf("admin POST from preview origin: got %d, want 403", adminResp.StatusCode)
	}
}

func TestPreviewOriginAuthRevocationAndRestart(t *testing.T) {
	dir := t.TempDir()
	store := newPreviewTokenStore()
	devTok := addTestDeviceToken(t, store, "dev_web3", "web", roleFull)
	watchTok := addTestDeviceToken(t, store, "dev_watch", "watch", roleAnswer)
	roTok := addTestDeviceToken(t, store, "dev_ro", "other", roleReadOnly)

	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	var nowMu sync.Mutex
	clock := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		nowMu.Lock()
		now = now.Add(d)
		nowMu.Unlock()
	}

	h := newPreviewOriginTestHub(t, dir, store)
	h.previewOrigin.now = clock
	_, allowedPort, _ := startInspectDevServer(t)
	_, deniedPort, _ := startInspectDevServer(t)
	portNode(t, h, []proto.Session{{SessionID: "s1", Ports: []proto.Port{{Port: allowedPort}}}})

	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	client := hostMappedClient(srv.Listener.Addr().String(), nil, nil, false)
	defer client.CloseIdleConnections()

	// Unauthorized port -> 403.
	if res, _ := requestPreviewGrant(t, client, "http://hub.example.test", devTok, "m1", deniedPort, "/", ""); res.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthorized port: got %d, want 403", res.StatusCode)
	}
	// Explicitly opening a port via POST .../ports/{port}/open allows preview/grant on it (same rule as .../ports/{port}/connect).
	_, openedPort, _ := startInspectDevServer(t)
	if res, _ := requestPreviewGrant(t, client, "http://hub.example.test", devTok, "m1", openedPort, "/", ""); res.StatusCode != http.StatusForbidden {
		t.Fatalf("unopened non-agent port: got %d, want 403", res.StatusCode)
	}
	openReq, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://hub.example.test/api/v1/machines/m1/ports/%d/open", openedPort), nil)
	openReq.Header.Set("Authorization", "Bearer "+devTok)
	openRes, err := client.Do(openReq)
	if err != nil {
		t.Fatal(err)
	}
	openRes.Body.Close()
	if openRes.StatusCode != http.StatusOK {
		t.Fatalf("POST ports/open: got %d, want 200", openRes.StatusCode)
	}
	if res, _ := requestPreviewGrant(t, client, "http://hub.example.test", devTok, "m1", openedPort, "/", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("opened non-agent port grant: got %d, want 200", res.StatusCode)
	}
	// Read-only (watch / readonly) devices -> 403.
	for name, tok := range map[string]string{"watch": watchTok, "readonly": roTok} {
		if res, _ := requestPreviewGrant(t, client, "http://hub.example.test", tok, "m1", allowedPort, "/", ""); res.StatusCode != http.StatusForbidden {
			t.Fatalf("%s device grant: got %d, want 403", name, res.StatusCode)
		}
	}
	// Invalid redirect paths -> 400.
	for _, badPath := range []string{"//evil.example/x", "https://evil.example/", "/__persona_preview_auth", "/api/v1/health", "/a\nb"} {
		if res, _ := requestPreviewGrant(t, client, "http://hub.example.test", devTok, "m1", allowedPort, badPath, ""); res.StatusCode != http.StatusBadRequest {
			t.Errorf("bad path %q: got %d, want 400", badPath, res.StatusCode)
		}
	}

	// Code expiry (>30s) -> rejected.
	res, expGrant := requestPreviewGrant(t, client, "http://hub.example.test", devTok, "m1", allowedPort, "/", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("grant for expiry test: %d", res.StatusCode)
	}
	advance(previewCodeTTL + time.Second)
	expRes, err := client.Get(strings.Replace(expGrant.URL, "https://", "http://", 1))
	if err != nil {
		t.Fatal(err)
	}
	expRes.Body.Close()
	if expRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired code: got %d, want 401", expRes.StatusCode)
	}

	// Code replay -> second redemption rejected.
	res, grant := requestPreviewGrant(t, client, "http://hub.example.test", devTok, "m1", allowedPort, "/", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("grant: %d", res.StatusCode)
	}
	redeemHTTP := strings.Replace(grant.URL, "https://", "http://", 1)
	first, err := client.Get(redeemHTTP)
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	if first.StatusCode != http.StatusFound {
		t.Fatalf("first redeem: %d", first.StatusCode)
	}
	var sessCookie *http.Cookie
	for _, c := range first.Cookies() {
		if c.Name == previewCookieName {
			sessCookie = c
		}
	}
	second, err := client.Get(redeemHTTP)
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if second.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed code: got %d, want 401", second.StatusCode)
	}

	// Code minted for allowedPort cannot be redeemed on another port's label.
	res, wrongLabelGrant := requestPreviewGrant(t, client, "http://hub.example.test", devTok, "m1", allowedPort, "/", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("wrongLabelGrant: %d", res.StatusCode)
	}
	otherLabel, _ := h.previewLabel("m1", deniedPort)
	uWrong, _ := url.Parse(strings.Replace(wrongLabelGrant.URL, "https://", "http://", 1))
	uWrong.Host = otherLabel + ".preview.example.test"
	wrongRes, err := client.Get(uWrong.String())
	if err != nil {
		t.Fatal(err)
	}
	wrongRes.Body.Close()
	if wrongRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("code on wrong label: got %d, want 401", wrongRes.StatusCode)
	}

	// Cookie from allowedPort cannot be replayed on another port's label.
	reqOther, _ := http.NewRequest(http.MethodGet, "http://"+otherLabel+".preview.example.test/", nil)
	reqOther.AddCookie(sessCookie)
	otherRes, err := client.Do(reqOther)
	if err != nil {
		t.Fatal(err)
	}
	otherRes.Body.Close()
	if otherRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie replayed on another label: got %d, want 401", otherRes.StatusCode)
	}

	// Hub restart survival: brand-new Hub instance with same cfg.dir (secret.key) and store, zero memory state.
	h2 := newPreviewOriginTestHub(t, dir, store)
	h2.previewOrigin.now = clock
	portNode(t, h2, []proto.Session{{SessionID: "s1", Ports: []proto.Port{{Port: allowedPort}}}})
	srv2 := httptest.NewServer(h2.routes())
	t.Cleanup(srv2.Close)
	client2 := hostMappedClient(srv2.Listener.Addr().String(), nil, nil, false)
	defer client2.CloseIdleConnections()

	previewHTTP := strings.Replace(grant.Origin, "https://", "http://", 1)
	reqAfterRestart, _ := http.NewRequest(http.MethodGet, previewHTTP+"/", nil)
	reqAfterRestart.AddCookie(sessCookie)
	restartRes, err := client2.Do(reqAfterRestart)
	if err != nil {
		t.Fatal(err)
	}
	restartBody, _ := io.ReadAll(restartRes.Body)
	restartRes.Body.Close()
	if restartRes.StatusCode != http.StatusOK || !strings.Contains(string(restartBody), "host=localhost:"+strconv.Itoa(allowedPort)) {
		t.Fatalf("session did not survive hub restart: status=%d body=%q", restartRes.StatusCode, string(restartBody))
	}

	// Open an active HMR WebSocket on h2, then revoke dev_web3 -> WS immediately closes AND next HTTP request fails!
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer wsCancel()
	wsConn, _, err := websocket.Dial(wsCtx, "ws"+strings.TrimPrefix(previewHTTP, "http")+"/hmr", &websocket.DialOptions{
		HTTPClient: client2,
		HTTPHeader: http.Header{
			"Origin": {grant.Origin},
			"Cookie": {previewCookieName + "=" + sessCookie.Value},
		},
		Subprotocols: []string{"vite-hmr"},
	})
	if err != nil {
		t.Fatalf("ws on restarted hub failed: %v", err)
	}
	defer wsConn.CloseNow()
	if err := wsConn.Write(wsCtx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, got, err := wsConn.Read(wsCtx); err != nil || string(got) != "echo:ping" {
		t.Fatalf("ws read before revoke: %q %v", got, err)
	}

	wsClosed := make(chan error, 1)
	go func() {
		_, _, rerr := wsConn.Read(wsCtx)
		wsClosed <- rerr
	}()

	// Revoke device via hub API.
	revReq, _ := http.NewRequest(http.MethodDelete, "http://hub.example.test/api/v1/auth/tokens/dev_web3", nil)
	revReq.Header.Set("Authorization", "Bearer "+h2.cfg.Token)
	revRes, err := client2.Do(revReq)
	if err != nil {
		t.Fatal(err)
	}
	revRes.Body.Close()
	if revRes.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d", revRes.StatusCode)
	}
	select {
	case err := <-wsClosed:
		if err == nil {
			t.Fatal("expected active WS to close upon device revocation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("active preview WebSocket was not closed after device revocation")
	}

	// Immediate subsequent HTTP request with the previously-valid cookie must return 401.
	reqAfterRevoke, _ := http.NewRequest(http.MethodGet, previewHTTP+"/", nil)
	reqAfterRevoke.AddCookie(sessCookie)
	afterRevRes, err := client2.Do(reqAfterRevoke)
	if err != nil {
		t.Fatal(err)
	}
	afterRevRes.Body.Close()
	if afterRevRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie still accepted after device revocation: got %d, want 401", afterRevRes.StatusCode)
	}

	// And after yet another hub restart (cold cache), revoked device stays rejected.
	h3 := newPreviewOriginTestHub(t, dir, store)
	h3.previewOrigin.now = clock
	portNode(t, h3, []proto.Session{{SessionID: "s1", Ports: []proto.Port{{Port: allowedPort}}}})
	srv3 := httptest.NewServer(h3.routes())
	t.Cleanup(srv3.Close)
	client3 := hostMappedClient(srv3.Listener.Addr().String(), nil, nil, false)
	defer client3.CloseIdleConnections()
	reqColdRevoke, _ := http.NewRequest(http.MethodGet, previewHTTP+"/", nil)
	reqColdRevoke.AddCookie(sessCookie)
	coldRes, err := client3.Do(reqColdRevoke)
	if err != nil {
		t.Fatal(err)
	}
	coldRes.Body.Close()
	if coldRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked device accepted after hub restart: got %d, want 401", coldRes.StatusCode)
	}
}

func TestPreviewOriginTLSWildcardSNIAndReload(t *testing.T) {
	certDir := t.TempDir()
	wildCert, wildKey, serial1 := generateTestCertFiles(t, certDir, "wild", []string{"*.preview.example.test"})

	h := newPublicTestHub(t)
	h.cfg.PublicURL = "https://hub.example.test"
	h.cfg.Public.TLSListen = "127.0.0.1:0"
	h.cfg.Preview = PreviewConfig{
		Domain:   "preview.example.test",
		CertFile: wildCert,
		KeyFile:  wildKey,
	}
	runPublicTestHub(t, h)

	port, hubPin := h.tlsPin()
	if port == 0 || len(hubPin) != 43 {
		t.Fatalf("hub TLS pin invalid: port=%d pin=%q", port, hubPin)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	peerLeaf := func(serverName string) *x509.Certificate {
		t.Helper()
		conn, err := tls.Dial("tcp", addr, &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		})
		if err != nil {
			t.Fatalf("tls dial SNI=%q: %v", serverName, err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0]
	}

	// Non-preview SNI (empty / hub hostname / bare preview domain) MUST get the hub's pinned identity cert.
	for _, sn := range []string{"", "hub.example.test", "preview.example.test", "a.b.preview.example.test"} {
		leaf := peerLeaf(sn)
		if leaf.DNSNames[0] != h.hubID()+".persona.invalid" {
			t.Errorf("SNI=%q got cert %v, want hub identity cert", sn, leaf.DNSNames)
		}
	}
	// Pinned client (Link / phone) still verifies against hubPin unchanged.
	pinned := pinnedTestClient(hubPin)
	defer pinned.CloseIdleConnections()
	resp, err := pinned.Get("https://" + addr + "/api/v1/health")
	if err != nil {
		t.Fatalf("pinned client failed: %v", err)
	}
	resp.Body.Close()

	// Preview wildcard SNI gets the wildcard cert v1.
	leaf1 := peerLeaf("p-abc123.preview.example.test")
	if leaf1.SerialNumber.String() != serial1 || !slices.Contains(leaf1.DNSNames, "*.preview.example.test") {
		t.Fatalf("preview SNI got serial=%s names=%v, want serial=%s", leaf1.SerialNumber, leaf1.DNSNames, serial1)
	}

	// Replace preview wildcard cert+key on disk (new private key!) and reload -> picked up without touching hub pin!
	wildCert2, wildKey2, serial2 := generateTestCertFiles(t, t.TempDir(), "wild2", []string{"*.preview.example.test"})
	certBytes, _ := os.ReadFile(wildCert2)
	keyBytes, _ := os.ReadFile(wildKey2)
	if err := os.WriteFile(wildCert, certBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wildKey, keyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.refreshTLS(time.Now()); err != nil {
		t.Fatalf("refreshTLS after preview cert rotation failed: %v", err)
	}
	leaf2 := peerLeaf("p-abc123.preview.example.test")
	if leaf2.SerialNumber.String() != serial2 || serial2 == serial1 {
		t.Fatalf("preview cert was not reloaded: got serial=%s want=%s", leaf2.SerialNumber, serial2)
	}
	if _, afterPin := h.tlsPin(); afterPin != hubPin {
		t.Fatalf("hub pin changed after preview cert reload: before=%s after=%s", hubPin, afterPin)
	}
	// Hub SNI still serves the original hub identity cert.
	if leaf := peerLeaf("hub.example.test"); leaf.DNSNames[0] != h.hubID()+".persona.invalid" {
		t.Fatalf("hub SNI cert changed: %v", leaf.DNSNames)
	}
}

func TestPreviewOriginReverseProxyAndSourceClassification(t *testing.T) {
	dir := t.TempDir()
	store := newPreviewTokenStore()
	devTok := addTestDeviceToken(t, store, "dev_rp", "web", roleFull)

	h := newPreviewOriginTestHub(t, dir, store)
	h.cfg.Public = PublicConfig{} // no hub TLS listener: reverse proxy terminates TLS
	h.cfg.Preview = PreviewConfig{Domain: "preview.example.test"}
	_, port, _ := startInspectDevServer(t)
	portNode(t, h, []proto.Session{{SessionID: "s1", Ports: []proto.Port{{Port: port}}}})

	handler := h.routes()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := hostMappedClient(srv.Listener.Addr().String(), nil, nil, false)
	defer client.CloseIdleConnections()

	// Loopback reverse proxy forwards plaintext HTTP with preserved Host -> succeeds, cookie still Secure!
	res, grant := requestPreviewGrant(t, client, "http://hub.example.test", devTok, "m1", port, "/", "")
	if res.StatusCode != http.StatusOK || strings.Contains(grant.Origin, ":443") {
		t.Fatalf("reverse-proxy grant: status=%d grant=%+v", res.StatusCode, grant)
	}
	redeemRes, err := client.Get(strings.Replace(grant.URL, "https://", "http://", 1))
	if err != nil {
		t.Fatal(err)
	}
	redeemRes.Body.Close()
	if redeemRes.StatusCode != http.StatusFound {
		t.Fatalf("reverse-proxy redeem: status=%d", redeemRes.StatusCode)
	}
	var ck *http.Cookie
	for _, c := range redeemRes.Cookies() {
		if c.Name == previewCookieName {
			ck = c
		}
	}
	if ck == nil || !ck.Secure || !ck.HttpOnly || ck.SameSite != http.SameSiteNoneMode || !ck.Partitioned {
		t.Fatalf("reverse-proxy cookie must stay Secure+HttpOnly+SameSite=None+Partitioned: %+v", ck)
	}

	// Direct plaintext request from a PUBLIC peer IP must be refused even with spoofed X-Forwarded-* headers.
	pubReq := httptest.NewRequest(http.MethodGet, strings.Replace(grant.Origin, "https://", "http://", 1)+"/", nil)
	pubReq.TLS = nil
	pubReq.RemoteAddr = "203.0.113.10:54321"
	pubReq.Header.Set("X-Forwarded-For", "127.0.0.1")
	pubReq.Header.Set("X-Forwarded-Proto", "https")
	pubReq.AddCookie(ck)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, pubReq)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "tls_required") {
		t.Fatalf("public plaintext preview must be rejected regardless of X-Forwarded-*: %d %s", rec.Code, rec.Body.String())
	}

	// Master-token preview grant works on loopback, but its session cookie is rejected from a public peer.
	mRes, mGrant := requestPreviewGrant(t, client, "http://hub.example.test", h.cfg.Token, "m1", port, "/", "")
	if mRes.StatusCode != http.StatusOK {
		t.Fatalf("master-token loopback grant: %d", mRes.StatusCode)
	}
	mRedeem, err := client.Get(strings.Replace(mGrant.URL, "https://", "http://", 1))
	if err != nil {
		t.Fatal(err)
	}
	mRedeem.Body.Close()
	var mCk *http.Cookie
	for _, c := range mRedeem.Cookies() {
		if c.Name == previewCookieName {
			mCk = c
		}
	}
	if mCk == nil {
		t.Fatal("expected master preview cookie")
	}
	pubMaster := httptest.NewRequest(http.MethodGet, mGrant.Origin+"/", nil)
	pubMaster.RemoteAddr = "203.0.113.10:54321"
	pubMaster.TLS = &tls.ConnectionState{}
	pubMaster.AddCookie(mCk)
	pubMasterRec := httptest.NewRecorder()
	handler.ServeHTTP(pubMasterRec, pubMaster)
	if pubMasterRec.Code != http.StatusForbidden {
		t.Fatalf("master-token preview cookie from public source: got %d, want 403", pubMasterRec.Code)
	}

	// security.private_network_only rejects public preview traffic even over TLS.
	hPriv := newPreviewOriginTestHub(t, dir, store)
	hPriv.cfg.Security.PrivateNetworkOnly = true
	privReq := httptest.NewRequest(http.MethodGet, grant.Origin+"/", nil)
	privReq.RemoteAddr = "203.0.113.10:54321"
	privReq.TLS = &tls.ConnectionState{}
	privReq.AddCookie(ck)
	privRec := httptest.NewRecorder()
	hPriv.routes().ServeHTTP(privRec, privReq)
	if privRec.Code != http.StatusForbidden || !strings.Contains(privRec.Body.String(), codePrivateNetworkOnly) {
		t.Fatalf("private_network_only public preview: got %d %s", privRec.Code, privRec.Body.String())
	}
}

func TestPreviewOriginUnreachableFallbackSignals(t *testing.T) {
	dir := t.TempDir()
	store := newPreviewTokenStore()
	devTok := addTestDeviceToken(t, store, "dev_fb", "web", roleFull)

	h := newPreviewOriginTestHub(t, dir, store)
	h.previewOrigin.dialTimeout = 120 * time.Millisecond

	// Create a dev server and a closed port, plus a stalling port to trigger timeout.
	_, livePort, _ := startInspectDevServer(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	stallPort := 54321
	var stall atomic.Bool

	// Build a custom node that handles livePort, closedPort (port_gone), and stallPort (hangs).
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide, nodeSide := rpc.New("h", h2nW), rpc.New("n", n2hW)
	hubMux, nodeMux := streams.New(hubSide.Notify), streams.New(nodeSide.Notify)
	hubSide.OnEvent = func(e string, p json.RawMessage) { hubMux.Event(e, p) }
	nodeSide.OnEvent = func(e string, p json.RawMessage) { nodeMux.Event(e, p) }
	nodeSide.OnRequest = func(rctx context.Context, method string, params json.RawMessage) (any, error) {
		var p struct {
			ID   string `json:"id"`
			Port int    `json:"port"`
		}
		_ = json.Unmarshal(params, &p)
		if method != "port.dial" {
			return nil, fmt.Errorf("unexpected %s", method)
		}
		if p.Port == stallPort && stall.Load() {
			<-rctx.Done()
			return nil, rctx.Err()
		}
		c, derr := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(p.Port), time.Second)
		if derr != nil {
			return nil, &rpc.Error{Code: "port_gone", Message: "nothing is listening"}
		}
		if err := nodeMux.Open(p.ID); err != nil {
			c.Close()
			return nil, err
		}
		nodeMux.Start(p.ID, c)
		return map[string]any{}, nil
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = hubSide.Serve(ctx, n2hR) }()
	go func() { defer wg.Done(); _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); h2nW.Close(); n2hW.Close(); wg.Wait() })

	m := newMachine(h, MachineConfig{ID: "m1", Name: "dev box"})
	m.peer, m.online, m.streams = hubSide, true, hubMux
	m.sessions = []proto.Session{{SessionID: "s1", Ports: []proto.Port{{Port: livePort}, {Port: closedPort}, {Port: stallPort}}}}
	h.byID["m1"] = m

	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	client := hostMappedClient(srv.Listener.Addr().String(), nil, nil, false)
	defer client.CloseIdleConnections()

	redeemFor := func(port int) (string, *http.Cookie) {
		res, grant := requestPreviewGrant(t, client, "http://hub.example.test", devTok, "m1", port, "/", "")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("grant port %d: %d", port, res.StatusCode)
		}
		r, err := client.Get(strings.Replace(grant.URL, "https://", "http://", 1))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		for _, c := range r.Cookies() {
			if c.Name == previewCookieName {
				return strings.Replace(grant.Origin, "https://", "http://", 1), c
			}
		}
		t.Fatal("missing cookie")
		return "", nil
	}

	assertUnreachableHTML := func(name, origin string, ck *http.Cookie, wantStatus int, wantCode string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, origin+"/", nil)
		req.Header.Set("Accept", "text/html")
		req.AddCookie(ck)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		s := string(body)
		if resp.StatusCode != wantStatus {
			t.Errorf("%s: status=%d, want %d (body=%s)", name, resp.StatusCode, wantStatus, s)
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			t.Errorf("%s: Content-Type=%q, want text/html", name, resp.Header.Get("Content-Type"))
		}
		if !strings.Contains(s, `persona_preview:"unreachable"`) || !strings.Contains(s, `code:"`+wantCode+`"`) {
			t.Errorf("%s: missing postMessage payload for %q in %s", name, wantCode, s)
		}
		if strings.Contains(s, devTok) || strings.Contains(s, h.cfg.Token) || strings.Contains(s, ck.Value) {
			t.Errorf("%s: leaked secret in error page", name)
		}
		if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors https://hub.example.test") {
			t.Errorf("%s: missing frame-ancestors in CSP: %q", name, resp.Header.Get("Content-Security-Policy"))
		}
	}

	// 1. port_gone
	closedOrigin, closedCookie := redeemFor(closedPort)
	assertUnreachableHTML("port_gone", closedOrigin, closedCookie, http.StatusBadGateway, "port_gone")

	// Non-HTML subresource request gets JSON error with same code.
	jsonReq, _ := http.NewRequest(http.MethodGet, closedOrigin+"/app.js", nil)
	jsonReq.Header.Set("Accept", "application/javascript")
	jsonReq.AddCookie(closedCookie)
	jsonRes, err := client.Do(jsonReq)
	if err != nil {
		t.Fatal(err)
	}
	jsonBody, _ := io.ReadAll(jsonRes.Body)
	jsonRes.Body.Close()
	if jsonRes.StatusCode != http.StatusBadGateway || !strings.Contains(string(jsonBody), `"code":"port_gone"`) {
		t.Errorf("non-HTML port_gone: status=%d body=%s", jsonRes.StatusCode, string(jsonBody))
	}

	// 2. timeout
	stall.Store(true)
	stallOrigin, stallCookie := redeemFor(stallPort)
	assertUnreachableHTML("timeout", stallOrigin, stallCookie, http.StatusGatewayTimeout, "timeout")

	// 3. too_many_connections when maxPortStreams is reached.
	liveOrigin, liveCookie := redeemFor(livePort)
	var heldIDs []string
	for i := 0; i < maxPortStreams; i++ {
		id := fmt.Sprintf("fill-%d", i)
		if err := hubMux.Open(id); err != nil {
			t.Fatal(err)
		}
		heldIDs = append(heldIDs, id)
	}
	assertUnreachableHTML("too_many_connections", liveOrigin, liveCookie, http.StatusTooManyRequests, "too_many_connections")
	for _, id := range heldIDs {
		hubMux.Abort(id, nil)
	}

	// 4. offline
	m.mu.Lock()
	m.online = false
	m.mu.Unlock()
	assertUnreachableHTML("offline", liveOrigin, liveCookie, http.StatusServiceUnavailable, "offline")
}

// TestPreviewOriginE2EHarness runs a real throwaway Hub + SNI wildcard TLS + portNode + dev server
// for scripts/test/preview_origin_isolation_e2e.mjs. Skipped in normal `go test` runs.
func TestPreviewOriginE2EHarness(t *testing.T) {
	if os.Getenv("PERSONA_PREVIEW_ORIGIN_HARNESS") != "1" {
		t.Skip("set PERSONA_PREVIEW_ORIGIN_HARNESS=1 (used by scripts/test/preview_origin_isolation_e2e.mjs)")
	}
	dir := t.TempDir()
	store := newPreviewTokenStore()
	devTok := addTestDeviceToken(t, store, "dev_e2e_web", "web", roleFull)

	certDir := t.TempDir()
	hubCert, hubKey, _ := generateTestCertFiles(t, certDir, "hub", []string{"hub.example.test", "evil.example.test"})
	wildCert, wildKey, _ := generateTestCertFiles(t, certDir, "wild", []string{"*.preview.example.test"})

	// Write a minimal hub parent web page into <dir>/web/index.html.
	webDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(webDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const hubPage = `<!doctype html><html><head><meta charset="utf-8"><title>Persona E2E Hub</title></head><body><h1>Hub</h1><div id="mount"></div></body></html>`
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte(hubPage), 0o600); err != nil {
		t.Fatal(err)
	}

	// Start a real dev server that runs both normal app features (ES module, native <select>,
	// dev cookie + 302, HMR WebSocket) and active cross-origin exfiltration probes inside the iframe.
	var (
		seenMu        sync.Mutex
		seenAuth      bool
		seenHubToken  bool
		seenPreviewCk bool
		seenHosts     []string
		seenWSOrigins []string
		seenWSHosts   []string
		hubOriginVal  atomic.Value
	)
	hubOriginVal.Store("")
	devMux := http.NewServeMux()
	devMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		seenMu.Lock()
		seenHosts = append(seenHosts, r.Host)
		if r.Header.Get("Authorization") != "" {
			seenAuth = true
		}
		if strings.Contains(r.Header.Get("Cookie"), previewCookieName) {
			seenPreviewCk = true
		}
		if strings.Contains(r.Header.Get("Cookie"), devTok) || strings.Contains(r.Header.Get("Authorization"), devTok) {
			seenHubToken = true
		}
		seenMu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "dev_visible", Value: "from_dev_server", Path: "/"})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Frame-Options", "DENY") // Hub must strip this and enforce frame-ancestors.
		fmt.Fprint(w, `<!doctype html>
<html>
<head><meta charset="utf-8"><title>Isolated Dev App</title></head>
<body>
  <h2 id="title">Dev Server Loaded</h2>
  <select id="fruit"><option value="apple">Apple</option><option value="banana">Banana</option></select>
  <div id="hmr-status">connecting</div>
  <pre id="probe-json"></pre>
  <script type="module" src="/app.js"></script>
</body>
</html>`)
	})
	devMux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		hubOrigin, _ := hubOriginVal.Load().(string)
		fmt.Fprintf(w, `
const hubOrigin = %q;
async function runProbes() {
  const result = {
    origin: location.origin,
    href: location.href,
    referrer: document.referrer,
    documentCookie: document.cookie,
    parentLocalStorageBlocked: false,
    parentDocumentBlocked: false,
    crossOriginHubAPIFailed: false,
    crossOriginHubHealthFailed: false,
    sameOriginAPIHealthStatus: 0,
    sameOriginAPIMachinesStatus: 0,
    redirectCookieBody: "",
    hmrReply: "",
    moduleLoaded: true,
  };
  try {
    const t = window.parent.localStorage.getItem("persona.token");
    result.parentLocalStorageLeaked = t;
  } catch (e) {
    result.parentLocalStorageBlocked = true;
  }
  try {
    const html = window.parent.document.documentElement.innerHTML;
    result.parentDocumentLeaked = html.slice(0, 40);
  } catch (e) {
    result.parentDocumentBlocked = true;
  }
  try {
    const r = await fetch(hubOrigin + "/api/v1/machines", { credentials: "include" });
    result.crossOriginHubAPIStatus = r.status;
    result.crossOriginHubAPIBody = await r.text();
  } catch (e) {
    result.crossOriginHubAPIFailed = true;
  }
  try {
    const r = await fetch(hubOrigin + "/api/v1/health", { credentials: "include" });
    result.crossOriginHubHealthStatus = r.status;
    result.crossOriginHubHealthBody = await r.text();
  } catch (e) {
    result.crossOriginHubHealthFailed = true;
  }
  const r1 = await fetch("/api/v1/health");
  result.sameOriginAPIHealthStatus = r1.status;
  const r2 = await fetch("/api/v1/machines");
  result.sameOriginAPIMachinesStatus = r2.status;

  const loginRes = await fetch("/login");
  result.redirectCookieBody = await loginRes.text();

  result.hmrReply = await new Promise((resolve, reject) => {
    const ws = new WebSocket((location.protocol === "https:" ? "wss://" : "ws://") + location.host + "/hmr", ["vite-hmr"]);
    const timer = setTimeout(() => reject(new Error("ws timeout")), 5000);
    ws.onopen = () => ws.send("hmr-ping");
    ws.onmessage = (ev) => { clearTimeout(timer); ws.close(); resolve(String(ev.data)); };
    ws.onerror = () => { clearTimeout(timer); reject(new Error("ws error")); };
  });
  document.getElementById("hmr-status").textContent = result.hmrReply;
  document.getElementById("probe-json").textContent = JSON.stringify(result);
  window.parent.postMessage({ e2e_probe_done: true, result }, hubOrigin);
}
runProbes().catch((err) => {
  document.getElementById("probe-json").textContent = JSON.stringify({ error: String(err) });
  window.parent.postMessage({ e2e_probe_done: true, error: String(err) }, hubOrigin);
});
`, hubOrigin)
	})
	devMux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "dev_sid", Value: "dev42", Path: "/"})
		http.Redirect(w, r, "/me", http.StatusFound)
	})
	devMux.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("dev_sid")
		if err != nil {
			http.Error(w, "missing dev_sid", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, "dev-me:"+c.Value)
	})
	devMux.HandleFunc("/hmr", func(w http.ResponseWriter, r *http.Request) {
		seenMu.Lock()
		seenWSHosts = append(seenWSHosts, r.Host)
		seenWSOrigins = append(seenWSOrigins, r.Header.Get("Origin"))
		if r.Header.Get("Authorization") != "" {
			seenAuth = true
		}
		if strings.Contains(r.Header.Get("Cookie"), previewCookieName) {
			seenPreviewCk = true
		}
		if strings.Contains(r.Header.Get("Cookie"), devTok) {
			seenHubToken = true
		}
		seenMu.Unlock()
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{"vite-hmr"}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), typ, append([]byte("echo:"), b...)); err != nil {
				return
			}
		}
	})
	devSrv := httptest.NewServer(devMux)
	defer devSrv.Close()
	devPort := devSrv.Listener.Addr().(*net.TCPAddr).Port

	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stoppedPort := closedLn.Addr().(*net.TCPAddr).Port
	closedLn.Close()

	// Separate HTTPS server for evil.example.test (to test CSP frame-ancestors blocking in Chrome).
	evilCertPair, err := tls.LoadX509KeyPair(hubCert, hubKey)
	if err != nil {
		t.Fatal(err)
	}
	evilSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><body><h1>Evil Host</h1><div id="mount"></div></body></html>`)
	}))
	evilSrv.TLS = &tls.Config{Certificates: []tls.Certificate{evilCertPair}}
	evilSrv.StartTLS()
	defer evilSrv.Close()
	evilPort := evilSrv.Listener.Addr().(*net.TCPAddr).Port

	h := &Hub{
		cfg: &Config{
			Listen:    "127.0.0.1:0",
			PublicURL: "https://hub.example.test",
			Token:     "harness-master-token",
			Public:    PublicConfig{TLSListen: "127.0.0.1:0", CertFile: hubCert, KeyFile: hubKey},
			Preview: PreviewConfig{
				Domain:   "preview.example.test",
				CertFile: wildCert,
				KeyFile:  wildKey,
			},
			dir: dir,
		},
		store:   store,
		logger:  log.New(io.Discard, "", 0),
		version: "e2e",
		byID:    map[string]*Machine{},
		clients: map[*client]struct{}{},
		calls:   map[string]chan proto.ToolResult{},
	}
	portNode(t, h, []proto.Session{{SessionID: "s1", Ports: []proto.Port{{Port: devPort, Title: "Dev"}, {Port: stoppedPort, Title: "Stopped"}}}})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := h.startPublic(ctx); err != nil {
		t.Fatal(err)
	}
	tlsPort, _ := h.tlsPin()
	hubOrigin := "https://hub.example.test:" + strconv.Itoa(tlsPort)
	hubOriginVal.Store(hubOrigin)

	done := make(chan error, 1)
	go func() { done <- h.serveHTTP(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	// Wait until the TLS server answers /api/v1/health before signaling ready.
	probeClient := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		},
	}
	defer probeClient.CloseIdleConnections()
	readyDeadline := time.Now().Add(5 * time.Second)
	readyTick := time.NewTicker(20 * time.Millisecond)
	defer readyTick.Stop()
	for {
		resp, perr := probeClient.Get("https://127.0.0.1:" + strconv.Itoa(tlsPort) + "/api/v1/health")
		if perr == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(readyDeadline) {
			t.Fatalf("TLS server did not become ready: %v", perr)
		}
		select {
		case serr := <-done:
			t.Fatalf("serveHTTP exited early: %v", serr)
		case <-readyTick.C:
		}
	}

	// Small local control server so the Playwright script can inspect upstream observations and signal stop.
	stopCh := make(chan struct{})
	var stopOnce sync.Once
	ctrlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stop" {
			stopOnce.Do(func() { close(stopCh) })
			w.WriteHeader(http.StatusNoContent)
			return
		}
		seenMu.Lock()
		defer seenMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"seen_auth":       seenAuth,
			"seen_hub_token":  seenHubToken,
			"seen_preview_ck": seenPreviewCk,
			"seen_hosts":      seenHosts,
			"seen_ws_hosts":   seenWSHosts,
			"seen_ws_origins": seenWSOrigins,
		})
	}))
	defer ctrlSrv.Close()

	info, _ := json.Marshal(map[string]any{
		"hub_origin":   hubOrigin,
		"evil_origin":  "https://evil.example.test:" + strconv.Itoa(evilPort),
		"ctrl_url":     ctrlSrv.URL,
		"token":        devTok,
		"machine_id":   "m1",
		"port":         devPort,
		"stopped_port": stoppedPort,
		"tls_port":     tlsPort,
	})
	fmt.Printf("HARNESS_READY %s\n", string(info))

	select {
	case <-stopCh:
	case <-ctx.Done():
	}
}
