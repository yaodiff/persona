package hub

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/streams"
)

// Isolated wildcard-subdomain preview origin proxy (docs/web/preview.md "Isolated preview origin"):
//
// When hub.json sets preview.domain (e.g. "preview.example.com"), each (machine, port) gets a
// deterministic, unguessable origin https://<label>.<preview.domain>[:port] that is strictly
// separate from the hub's web origin (protecting localStorage["persona.token"] and hub APIs).
//
// Flow:
//  1. The hub web app calls POST /api/v1/machines/{mid}/preview/grant {port, path?} with a full
//     device Bearer token. The hub checks portAllowed and mints a 30s one-time code bound to
//     (device, machine, port, label, path) and returns the redemption URL + preview origin.
//  2. The iframe loads https://<label>.<domain>[:port]/__persona_preview_auth?code=...
//     The hub consumes the code once, seals a self-contained session token with a key derived
//     from secret.key (so sessions survive hub restarts), sets the host-only cookie
//     __Host-persona_preview (HttpOnly; Secure; SameSite=None; Partitioned; Path=/), and 302
//     redirects to the target path with Referrer-Policy: no-referrer.
//  3. Subsequent HTTP and WebSocket (Vite/Next HMR) requests to <label>.<domain> validate the
//     cookie (including immediate device revocation checks and portAllowed), strip Authorization
//     and hub/preview cookies, rewrite Host to localhost:<port> (and WS Origin to
//     http://localhost:<port> after verifying incoming WS Origin matches the preview origin),
//     and relay bytes through the existing port.dial + streams.Mux tunnel without rewriting
//     paths, query strings, redirects, dev-server cookies, or response bodies.
//  4. Requests to <preview.domain> or *.<preview.domain> never reach hub API, admin, sharing, or
//     web routes; /api and /api/* on preview hosts always return 404.
//  5. When the target port cannot be dialed or times out, the hub returns a small HTML error
//     page that posts {persona_preview: "unreachable", code} (with no secrets) to the allowed
//     hub parent origin(s) via postMessage so the web client can fall back to the browser stream.

const (
	// featurePreviewOrigin is advertised in hello.features and trusted /api/v1/health
	// only when preview.domain is configured.
	featurePreviewOrigin = "preview_origin"

	previewAuthPath       = "/__persona_preview_auth"
	previewCookieName     = "__Host-persona_preview"
	previewCodeTTL        = 30 * time.Second
	previewSessionTTL     = 12 * time.Hour
	previewCodeLimit      = 512
	previewDialTimeout    = 10 * time.Second
	previewDeviceCacheTTL = 30 * time.Second
	previewSessionVersion = 1
)

// previewOriginState holds the hub's runtime preview-origin state.
// Only short-lived one-time redemption codes and a short revocation-evicted device lookup cache
// live in memory; active sessions are self-contained and survive hub restarts.
type previewOriginState struct {
	mu      sync.Mutex
	codes   map[string]previewCode
	devices map[string]previewDeviceEntry

	keysOnce   sync.Once
	labelKey   []byte
	sessionKey []byte
	keysErr    error

	// Test seams (zero values use production defaults).
	now         func() time.Time
	dialTimeout time.Duration
}

type previewCode struct {
	machineID  string
	port       int
	label      string
	deviceID   string
	masterHash string // set only when granted with master token (deviceID == "")
	path       string
	ancestor   string // verified same-host hub web origin of the grant request
	expiresAt  time.Time
}

type previewDeviceEntry struct {
	active    bool
	checkedAt time.Time
}

type previewSession struct {
	V          int    `json:"v"`
	DeviceID   string `json:"d,omitempty"`
	MasterHash string `json:"k,omitempty"`
	MachineID  string `json:"m"`
	Port       int    `json:"p"`
	Label      string `json:"l"`
	Ancestor   string `json:"a,omitempty"`
	IssuedAt   int64  `json:"i"`
	ExpiresAt  int64  `json:"e"`
}

type previewDialError struct {
	code string
	err  error
}

func (e *previewDialError) Error() string { return e.err.Error() }
func (e *previewDialError) Unwrap() error { return e.err }

// previewOriginFeatures returns featurePreviewOrigin when preview.domain is validly configured.
// Read statically by scripts/test/test_doc_wire.py: returns must stay []string{…} or nil.
func (h *Hub) previewOriginFeatures() []string {
	if !h.previewOriginEnabled() {
		return nil
	}
	return []string{featurePreviewOrigin}
}

func (h *Hub) previewNow() time.Time {
	if h.previewOrigin.now != nil {
		return h.previewOrigin.now()
	}
	return time.Now()
}

func (h *Hub) previewDialBudget() time.Duration {
	if h.previewOrigin.dialTimeout > 0 {
		return h.previewOrigin.dialTimeout
	}
	return previewDialTimeout
}

func (h *Hub) previewDomain() string {
	if h.cfg == nil || strings.TrimSpace(h.cfg.Preview.Domain) == "" {
		return ""
	}
	if h.cfg.checkPreview() != nil {
		return ""
	}
	d, err := previewDomainOf(h.cfg.Preview.Domain)
	if err != nil {
		return ""
	}
	return d
}

func (h *Hub) previewOriginEnabled() bool { return h.previewDomain() != "" }

// previewSNIMatch reports whether SNI is a single-level subdomain of preview.domain.
func (h *Hub) previewSNIMatch(serverName string) bool {
	domain := h.previewDomain()
	if domain == "" {
		return false
	}
	label, matched := previewHostLabel(serverName, domain)
	return matched && label != ""
}

// previewKeys derives separate 32-byte keys for labels and session sealing from the hub's
// persistent secret.key via HKDF-SHA256, so labels and sessions survive hub restarts.
func (h *Hub) previewKeys() (labelKey, sessionKey []byte, err error) {
	h.previewOrigin.keysOnce.Do(func() {
		if h.cfg != nil && h.cfg.dir != "" {
			box := h.secretBox()
			h.previewOrigin.labelKey, h.previewOrigin.keysErr = box.Derive("persona preview label v1", 32)
			if h.previewOrigin.keysErr != nil {
				return
			}
			h.previewOrigin.sessionKey, h.previewOrigin.keysErr = box.Derive("persona preview session v1", 32)
			return
		}
		// Synthetic test hubs without a data directory: derive deterministically from cfg.Token
		// (or an ephemeral random seed) without writing secret.key into the working directory.
		var seed []byte
		if h.cfg != nil && h.cfg.Token != "" {
			seed = []byte(h.cfg.Token)
		} else {
			seed = make([]byte, 32)
			if _, rerr := io.ReadFull(rand.Reader, seed); rerr != nil {
				h.previewOrigin.keysErr = rerr
				return
			}
		}
		h.previewOrigin.labelKey, h.previewOrigin.keysErr = hkdf.Key(sha256.New, seed, nil, "persona preview label v1", 32)
		if h.previewOrigin.keysErr != nil {
			return
		}
		h.previewOrigin.sessionKey, h.previewOrigin.keysErr = hkdf.Key(sha256.New, seed, nil, "persona preview session v1", 32)
	})
	return h.previewOrigin.labelKey, h.previewOrigin.sessionKey, h.previewOrigin.keysErr
}

// previewLabel returns the stable, unguessable DNS label for (machineID, port).
func (h *Hub) previewLabel(machineID string, port int) (string, error) {
	labelKey, _, err := h.previewKeys()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, labelKey)
	fmt.Fprintf(mac, "%d:%s:%d", len(machineID), machineID, port)
	sum := mac.Sum(nil)
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:15]))
	return "p-" + encoded, nil
}

func previewMasterHash(token string) string {
	sum := sha256.Sum256([]byte("persona-preview-master:" + token))
	return hex.EncodeToString(sum[:16])
}

func (h *Hub) previewEffectivePort() int {
	if h.cfg == nil {
		return 443
	}
	if h.cfg.Preview.Port > 0 {
		return h.cfg.Preview.Port
	}
	if h.cfg.Preview.CertFile != "" {
		if port, _ := h.tlsPin(); port > 0 {
			return port
		}
		if p := portOf(h.cfg.Public.TLSListen); p != "" {
			if n, err := strconv.Atoi(p); err == nil && n > 0 && n <= 65535 {
				return n
			}
		}
	}
	return 443
}

func (h *Hub) previewOriginForLabel(label string) string {
	domain := h.previewDomain()
	if domain == "" || label == "" {
		return ""
	}
	host := label + "." + domain
	if port := h.previewEffectivePort(); port != 443 {
		host = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return "https://" + host
}

// previewFrameAncestors returns the canonical hub web origins allowed to frame preview content.
func (h *Hub) previewFrameAncestors(extraOrigin string) []string {
	domain := h.previewDomain()
	var out []string
	add := func(raw string, allowPath bool) {
		o := canonicalPreviewOrigin(raw, allowPath)
		if o == "" {
			return
		}
		u, err := url.Parse(o)
		if err != nil || hostMatchesPreviewDomain(u.Hostname(), domain) {
			return
		}
		if !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	if h.cfg != nil {
		add(h.cfg.PublicURL, true)
		for _, adv := range h.cfg.Public.Advertise {
			if strings.Contains(adv, "://") {
				add(adv, true)
			} else if strings.TrimSpace(adv) != "" {
				add("https://"+strings.TrimSpace(adv), true)
			}
		}
		for _, anc := range h.cfg.Preview.AllowedAncestors {
			add(anc, false)
		}
	}
	if extraOrigin != "" {
		add(extraOrigin, false)
	}
	return out
}

func previewCSPFrameAncestors(ancestors []string) string {
	if len(ancestors) == 0 {
		return "frame-ancestors 'none'"
	}
	return "frame-ancestors " + strings.Join(ancestors, " ")
}

// cleanPreviewPath validates a requested target path for preview/grant.
// It preserves query strings and fragments while preventing open redirects, loops, and /api/ targets.
func cleanPreviewPath(raw string) (string, error) {
	if raw == "" {
		return "/", nil
	}
	if strings.ContainsAny(raw, "\r\n\x00\\") {
		return "", errors.New("path contains invalid characters")
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "", errors.New("path must start with a single '/'")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "", errors.New("path must be a relative path on the preview origin")
	}
	clean := path.Clean("/" + u.Path)
	if clean == previewAuthPath || strings.HasPrefix(clean, previewAuthPath+"/") {
		return "", errors.New("path cannot target the preview auth endpoint")
	}
	if clean == "/api" || strings.HasPrefix(clean, "/api/") {
		return "", errors.New("path cannot target /api/")
	}
	return raw, nil
}

// grantAncestorOrigin returns the canonical Origin header of a preview/grant request only when it
// matches the Host the hub received the request on (and is not itself a preview domain).
func (h *Hub) grantAncestorOrigin(r *http.Request) string {
	orig := canonicalPreviewOrigin(r.Header.Get("Origin"), false)
	if orig == "" {
		return ""
	}
	u, err := url.Parse(orig)
	if err != nil || hostMatchesPreviewDomain(u.Hostname(), h.previewDomain()) {
		return ""
	}
	if r.TLS != nil && u.Scheme != "https" {
		return ""
	}
	reqOrig := canonicalPreviewOrigin(u.Scheme+"://"+r.Host, false)
	if reqOrig == "" || reqOrig != orig {
		return ""
	}
	return orig
}

func (h *Hub) putPreviewCode(entry previewCode) (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	now := h.previewNow()
	s := &h.previewOrigin
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.codes == nil {
		s.codes = map[string]previewCode{}
	}
	for k, v := range s.codes {
		if !now.Before(v.expiresAt) {
			delete(s.codes, k)
		}
	}
	if len(s.codes) >= previewCodeLimit {
		var oldestKey string
		var oldestExp time.Time
		for k, v := range s.codes {
			if oldestKey == "" || v.expiresAt.Before(oldestExp) {
				oldestKey, oldestExp = k, v.expiresAt
			}
		}
		if oldestKey != "" {
			delete(s.codes, oldestKey)
		}
	}
	s.codes[code] = entry
	return code, nil
}

// takePreviewCode atomically consumes a one-time code, validating non-expiry and label match.
func (h *Hub) takePreviewCode(code, reqLabel string) (previewCode, bool) {
	if code == "" || len(code) > 128 || reqLabel == "" {
		return previewCode{}, false
	}
	now := h.previewNow()
	s := &h.previewOrigin
	s.mu.Lock()
	entry, ok := s.codes[code]
	delete(s.codes, code)
	s.mu.Unlock()
	if !ok || !now.Before(entry.expiresAt) || entry.label != reqLabel {
		return previewCode{}, false
	}
	return entry, true
}

// sealPreviewSession encodes and authenticates sess using AES-256-GCM bound to label via AAD.
func (h *Hub) sealPreviewSession(sess previewSession) (string, error) {
	_, sessionKey, err := h.previewKeys()
	if err != nil {
		return "", err
	}
	plain, err := json.Marshal(sess)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(sessionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	aad := []byte("persona-preview-v1:" + sess.Label)
	sealed := gcm.Seal(nonce, nonce, plain, aad)
	return "v1." + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// openPreviewSession verifies, decrypts, and validates a sealed session cookie for reqLabel.
func (h *Hub) openPreviewSession(cookieVal, reqLabel string) (previewSession, bool) {
	encoded, ok := strings.CutPrefix(cookieVal, "v1.")
	if !ok || encoded == "" || len(encoded) > 4096 || reqLabel == "" {
		return previewSession{}, false
	}
	_, sessionKey, err := h.previewKeys()
	if err != nil {
		return previewSession{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return previewSession{}, false
	}
	block, err := aes.NewCipher(sessionKey)
	if err != nil {
		return previewSession{}, false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return previewSession{}, false
	}
	if len(raw) < gcm.NonceSize()+gcm.Overhead() {
		return previewSession{}, false
	}
	aad := []byte("persona-preview-v1:" + reqLabel)
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], aad)
	if err != nil {
		return previewSession{}, false
	}
	var sess previewSession
	if err := json.Unmarshal(plain, &sess); err != nil {
		return previewSession{}, false
	}
	if sess.V != previewSessionVersion || sess.Label != reqLabel || sess.MachineID == "" || sess.Port < 1 || sess.Port > 65535 {
		return previewSession{}, false
	}
	if h.previewNow().Unix() >= sess.ExpiresAt {
		return previewSession{}, false
	}
	wantLabel, err := h.previewLabel(sess.MachineID, sess.Port)
	if err != nil || subtle.ConstantTimeCompare([]byte(wantLabel), []byte(reqLabel)) != 1 {
		return previewSession{}, false
	}
	return sess, true
}

// previewDeviceRevoked immediately evicts id from the preview device status cache.
func (h *Hub) previewDeviceRevoked(id string) {
	if id == "" {
		return
	}
	h.previewOrigin.mu.Lock()
	delete(h.previewOrigin.devices, id)
	h.previewOrigin.mu.Unlock()
}

// previewDeviceValid checks that the credential owning a preview session is still active.
// Revocations take effect immediately via h.isDeviceRevoked and cache eviction; across restarts,
// the first request queries h.store.DeviceTokens.
func (h *Hub) previewDeviceValid(ctx context.Context, deviceID, masterHash string) (bool, error) {
	if deviceID == "" {
		if masterHash == "" || h.cfg == nil || h.cfg.Token == "" {
			return false, nil
		}
		want := previewMasterHash(h.cfg.Token)
		return subtle.ConstantTimeCompare([]byte(masterHash), []byte(want)) == 1, nil
	}
	if h.isDeviceRevoked(deviceID) {
		return false, nil
	}
	if h.store == nil {
		return false, nil
	}
	now := h.previewNow()
	s := &h.previewOrigin
	s.mu.Lock()
	entry, hit := s.devices[deviceID]
	s.mu.Unlock()
	if hit && now.Sub(entry.checkedAt) < previewDeviceCacheTTL {
		if h.isDeviceRevoked(deviceID) {
			return false, nil
		}
		return entry.active, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	tokens, err := h.store.DeviceTokens(lookupCtx)
	cancel()
	if err != nil {
		return false, err
	}
	active := false
	for _, dt := range tokens {
		if dt.ID == deviceID {
			active = dt.RevokedAt == nil && dt.Role == roleFull && (dt.ExpiresAt == nil || now.Before(*dt.ExpiresAt))
			break
		}
	}
	// Fence against a revocation that raced with the database read.
	if active && h.isDeviceRevoked(deviceID) {
		active = false
	}
	s.mu.Lock()
	if s.devices == nil {
		s.devices = map[string]previewDeviceEntry{}
	}
	if len(s.devices) >= 2048 {
		for k, v := range s.devices {
			if now.Sub(v.checkedAt) >= previewDeviceCacheTTL {
				delete(s.devices, k)
			}
		}
	}
	s.devices[deviceID] = previewDeviceEntry{active: active, checkedAt: now}
	s.mu.Unlock()
	return active, nil
}

// previewGrant is POST /api/v1/machines/{mid}/preview/grant {port, path?}:
// issues a short-lived one-time redemption URL on the port's isolated preview origin.
func (h *Hub) previewGrant(w http.ResponseWriter, r *http.Request, m *Machine) {
	if !h.previewOriginEnabled() {
		writeErrCode(w, http.StatusNotFound, "preview_origin_disabled", errors.New("preview origin is not configured on this hub"))
		return
	}
	p := principalOf(r.Context())
	if p.Role != roleFull || p.PairHash != "" {
		writeErr(w, http.StatusForbidden, errors.New("this device cannot open previews"))
		return
	}
	var body struct {
		Port int    `json:"port"`
		Path string `json:"path"`
	}
	if err := decodeJSONBody(w, r, &body, 16<<10); err != nil {
		writeBodyErr(w, err, errors.New("port is required"))
		return
	}
	if body.Port <= 0 || body.Port > 65535 {
		writeErr(w, http.StatusBadRequest, errors.New("bad port"))
		return
	}
	targetPath, err := cleanPreviewPath(body.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !h.portAllowed(m, body.Port) {
		writeErrCode(w, http.StatusForbidden, "port_not_allowed", fmt.Errorf("port %d is not an agent's and was not opened", body.Port))
		return
	}
	if m.portStreams() == nil {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("%s: %w", m.name(), errOffline))
		return
	}
	label, err := h.previewLabel(m.cfg.ID, body.Port)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	origin := h.previewOriginForLabel(label)
	masterHash := ""
	if p.ID == "" {
		masterHash = previewMasterHash(h.cfg.Token)
	}
	expiresAt := h.previewNow().Add(previewCodeTTL)
	code, err := h.putPreviewCode(previewCode{
		machineID:  m.cfg.ID,
		port:       body.Port,
		label:      label,
		deviceID:   p.ID,
		masterHash: masterHash,
		path:       targetPath,
		ancestor:   h.grantAncestorOrigin(r),
		expiresAt:  expiresAt,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	redeemURL := origin + previewAuthPath + "?" + url.Values{"code": {code}}.Encode()
	writeJSON(w, previewGrantResponse{
		URL:       redeemURL,
		Origin:    origin,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	})
}

type previewGrantResponse struct {
	URL       string `json:"url"`
	Origin    string `json:"origin"`
	ExpiresAt string `json:"expires_at"`
}

// previewDispatcher intercepts all requests whose Host matches <preview.domain> or
// *.<preview.domain> before they can reach hub API, admin, sharing, or web routes.
// When preview.domain is not configured, it returns next untouched.
func (h *Hub) previewDispatcher(next http.Handler) http.Handler {
	domain := h.previewDomain()
	if domain == "" {
		return next
	}
	previewHandler := recoverPanics(h, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.servePreviewHost(w, r, domain)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, matched := previewHostLabel(r.Host, domain); !matched {
			next.ServeHTTP(w, r)
			return
		}
		previewHandler.ServeHTTP(w, r)
	})
}

func (h *Hub) servePreviewHost(w http.ResponseWriter, r *http.Request, domain string) {
	// Enforce the hub's transport rules by socket peer address only (never trusting X-Forwarded-*).
	if h.cfg.Security.PrivateNetworkOnly && h.sourceOf(r) == sourcePublic {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", previewCSPFrameAncestors(h.previewFrameAncestors("")))
		writeErrCode(w, http.StatusForbidden, codePrivateNetworkOnly, errors.New("private network only"))
		return
	}
	if r.TLS == nil {
		if h.cfg.Security.PlaintextLoopbackOnly && h.sourceOf(r) != sourceLoopback {
			h.writePreviewPlainRefusal(w, r)
			return
		}
		if !h.cfg.Security.AllowPublicPlaintext && h.sourceOf(r) == sourcePublic {
			h.writePreviewPlainRefusal(w, r)
			return
		}
	}

	label, _ := previewHostLabel(r.Host, domain)
	if label == "" {
		h.writePreviewNotFound(w, "")
		return
	}

	// Hard-block /api and /api/* on preview hosts regardless of authentication or upstream routes.
	clean := path.Clean("/" + r.URL.Path)
	if clean == "/api" || strings.HasPrefix(clean, "/api/") || strings.HasPrefix(r.URL.Path, "/api/") {
		h.writePreviewNotFound(w, "")
		return
	}

	if r.URL.Path == previewAuthPath {
		h.servePreviewAuth(w, r, label)
		return
	}

	h.servePreviewProxy(w, r, label)
}

func (h *Hub) writePreviewPlainRefusal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", previewCSPFrameAncestors(h.previewFrameAncestors("")))
	writeErrCode(w, http.StatusForbidden, "tls_required", errors.New("TLS is required"))
}

func (h *Hub) writePreviewNotFound(w http.ResponseWriter, extraAncestor string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", previewCSPFrameAncestors(h.previewFrameAncestors(extraAncestor)))
	writeErrCode(w, http.StatusNotFound, "not_found", errors.New("not found"))
}

func setPreviewSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:        previewCookieName,
		Value:       value,
		Path:        "/",
		MaxAge:      maxAge,
		HttpOnly:    true,
		Secure:      true,
		SameSite:    http.SameSiteNoneMode,
		Partitioned: true,
	})
}

func (h *Hub) servePreviewAuth(w http.ResponseWriter, r *http.Request, label string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		w.Header().Set("Content-Security-Policy", previewCSPFrameAncestors(h.previewFrameAncestors("")))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	code := r.URL.Query().Get("code")
	entry, ok := h.takePreviewCode(code, label)
	if !ok {
		h.writePreviewUnreachable(w, r, http.StatusUnauthorized, "auth_failed", "")
		return
	}
	if entry.deviceID == "" && h.sourceOf(r) == sourcePublic {
		h.writePreviewUnreachable(w, r, http.StatusForbidden, "auth_failed", entry.ancestor)
		return
	}
	valid, storeErr := h.previewDeviceValid(r.Context(), entry.deviceID, entry.masterHash)
	if storeErr != nil {
		h.writePreviewUnreachable(w, r, http.StatusServiceUnavailable, "offline", entry.ancestor)
		return
	}
	if !valid {
		h.writePreviewUnreachable(w, r, http.StatusUnauthorized, "auth_failed", entry.ancestor)
		return
	}
	m := h.localMachine(entry.machineID)
	if m == nil {
		h.writePreviewUnreachable(w, r, http.StatusServiceUnavailable, "offline", entry.ancestor)
		return
	}
	if !h.portAllowed(m, entry.port) {
		h.writePreviewUnreachable(w, r, http.StatusForbidden, "port_not_allowed", entry.ancestor)
		return
	}
	now := h.previewNow()
	sealed, err := h.sealPreviewSession(previewSession{
		V:          previewSessionVersion,
		DeviceID:   entry.deviceID,
		MasterHash: entry.masterHash,
		MachineID:  entry.machineID,
		Port:       entry.port,
		Label:      entry.label,
		Ancestor:   entry.ancestor,
		IssuedAt:   now.Unix(),
		ExpiresAt:  now.Add(previewSessionTTL).Unix(),
	})
	if err != nil {
		h.writePreviewUnreachable(w, r, http.StatusInternalServerError, "bad_gateway", entry.ancestor)
		return
	}
	w.Header().Set("Content-Security-Policy", previewCSPFrameAncestors(h.previewFrameAncestors(entry.ancestor)))
	setPreviewSessionCookie(w, sealed, int(previewSessionTTL.Seconds()))
	w.Header().Set("Location", entry.path)
	w.WriteHeader(http.StatusFound)
}

func (h *Hub) servePreviewProxy(w http.ResponseWriter, r *http.Request, label string) {
	cookies := r.CookiesNamed(previewCookieName)
	if len(cookies) != 1 || cookies[0].Value == "" {
		h.writePreviewUnreachable(w, r, http.StatusUnauthorized, "auth_failed", "")
		return
	}
	sess, ok := h.openPreviewSession(cookies[0].Value, label)
	if !ok {
		setPreviewSessionCookie(w, "", -1)
		h.writePreviewUnreachable(w, r, http.StatusUnauthorized, "auth_failed", "")
		return
	}
	if sess.DeviceID == "" && h.sourceOf(r) == sourcePublic {
		setPreviewSessionCookie(w, "", -1)
		h.writePreviewUnreachable(w, r, http.StatusForbidden, "auth_failed", sess.Ancestor)
		return
	}
	valid, storeErr := h.previewDeviceValid(r.Context(), sess.DeviceID, sess.MasterHash)
	if storeErr != nil {
		h.writePreviewUnreachable(w, r, http.StatusServiceUnavailable, "offline", sess.Ancestor)
		return
	}
	if !valid {
		setPreviewSessionCookie(w, "", -1)
		h.writePreviewUnreachable(w, r, http.StatusUnauthorized, "auth_failed", sess.Ancestor)
		return
	}
	m := h.localMachine(sess.MachineID)
	if m == nil {
		h.writePreviewUnreachable(w, r, http.StatusServiceUnavailable, "offline", sess.Ancestor)
		return
	}
	if !h.portAllowed(m, sess.Port) {
		h.writePreviewUnreachable(w, r, http.StatusForbidden, "port_not_allowed", sess.Ancestor)
		return
	}

	wsUpgrade := isWebSocketUpgrade(r)
	if wsUpgrade {
		if orig := r.Header.Get("Origin"); orig != "" && !h.isSamePreviewOrigin(orig, label, r.Host) {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "cross-origin WebSocket rejected", http.StatusForbidden)
			return
		}
	}

	mux := m.portStreams()
	if mux == nil {
		h.writePreviewUnreachable(w, r, http.StatusServiceUnavailable, "offline", sess.Ancestor)
		return
	}
	if mux.Len() >= maxPortStreams {
		h.writePreviewUnreachable(w, r, http.StatusTooManyRequests, "too_many_connections", sess.Ancestor)
		return
	}

	// Track under the owning device credential so revoking the device immediately cancels
	// active HTTP requests and open HMR WebSockets.
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{ID: sess.DeviceID, Role: roleFull}))
	r, untrack := h.trackRelayed(r)
	defer untrack()
	if r.Context().Err() != nil {
		h.writePreviewUnreachable(w, r, http.StatusUnauthorized, "auth_failed", sess.Ancestor)
		return
	}

	target := "localhost:" + strconv.Itoa(sess.Port)
	ancestors := h.previewFrameAncestors(sess.Ancestor)
	frameAncestorsHeader := previewCSPFrameAncestors(ancestors)

	tr := &http.Transport{
		Proxy:              nil,
		DisableCompression: true,
		DisableKeepAlives:  !wsUpgrade,
		ForceAttemptHTTP2:  false,
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			return h.dialPreviewStream(dialCtx, r.Context(), m, sess.Port)
		},
	}
	defer tr.CloseIdleConnections()

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = target
			pr.Out.Host = target
			stripPreviewOutboundCredentials(pr.Out.Header)
			if wsUpgrade {
				pr.Out.Header.Set("Origin", "http://"+target)
			}
		},
		Transport:     tr,
		FlushInterval: -1,
		ModifyResponse: func(res *http.Response) error {
			applyPreviewResponseHeaders(res.Header, frameAncestorsHeader)
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			var de *previewDialError
			if errors.As(err, &de) {
				status := http.StatusBadGateway
				switch de.code {
				case "offline":
					status = http.StatusServiceUnavailable
				case "timeout":
					status = http.StatusGatewayTimeout
				case "too_many_connections":
					status = http.StatusTooManyRequests
				case "port_gone":
					status = http.StatusBadGateway
				}
				h.writePreviewUnreachable(rw, req, status, de.code, sess.Ancestor)
				return
			}
			if errors.Is(err, context.DeadlineExceeded) {
				h.writePreviewUnreachable(rw, req, http.StatusGatewayTimeout, "timeout", sess.Ancestor)
				return
			}
			if errors.Is(err, context.Canceled) {
				if sess.DeviceID != "" && h.isDeviceRevoked(sess.DeviceID) {
					setPreviewSessionCookie(rw, "", -1)
					h.writePreviewUnreachable(rw, req, http.StatusUnauthorized, "auth_failed", sess.Ancestor)
				}
				return
			}
			h.writePreviewUnreachable(rw, req, http.StatusBadGateway, "bad_gateway", sess.Ancestor)
		},
	}
	proxy.ServeHTTP(w, r)
}

// dialPreviewStream opens one stream on m's streams.Mux via port.dial and binds it to a net.Conn.
// When reqCtx ends (client disconnect or device revocation), the stream is closed and aborted immediately.
func (h *Hub) dialPreviewStream(dialCtx, reqCtx context.Context, m *Machine, port int) (net.Conn, error) {
	mux := m.portStreams()
	if mux == nil {
		return nil, &previewDialError{code: "offline", err: fmt.Errorf("%s: %w", m.name(), errOffline)}
	}
	if mux.Len() >= maxPortStreams {
		return nil, &previewDialError{code: "too_many_connections", err: errors.New("too many connections to this machine's ports")}
	}
	id := hubStreamIDs.next("p")
	if err := mux.Open(id); err != nil {
		return nil, &previewDialError{code: "offline", err: err}
	}
	dctx, cancel := context.WithTimeout(dialCtx, h.previewDialBudget())
	_, err := m.dialPortStream(dctx, mux, devicewire.PortDial{ID: id, Port: port}, nil)
	cancel()
	if err != nil {
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == "port_gone" {
			return nil, &previewDialError{code: "port_gone", err: err}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &previewDialError{code: "timeout", err: err}
		}
		if errors.Is(err, errOffline) || errors.Is(err, errManagerOffline) {
			return nil, &previewDialError{code: "offline", err: err}
		}
		return nil, &previewDialError{code: "bad_gateway", err: err}
	}
	mine, theirs := net.Pipe()
	mux.Start(id, theirs)
	sc := &previewStreamConn{Conn: mine, mux: mux, id: id}
	sc.stop = context.AfterFunc(reqCtx, func() { _ = sc.Close() })
	return sc, nil
}

type previewStreamConn struct {
	net.Conn
	mux  *streams.Mux
	id   string
	stop func() bool
	once sync.Once
}

func (c *previewStreamConn) Close() error {
	var err error
	c.once.Do(func() {
		if c.stop != nil {
			c.stop()
		}
		err = c.Conn.Close()
		c.mux.Abort(c.id, err)
	})
	return err
}

func isWebSocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	for _, v := range r.Header.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
}

func (h *Hub) isSamePreviewOrigin(orig, label, reqHost string) bool {
	got := canonicalPreviewOrigin(orig, false)
	if got == "" || !strings.HasPrefix(got, "https://") {
		return false
	}
	if want := h.previewOriginForLabel(label); want != "" && got == want {
		return true
	}
	if reqOrig := canonicalPreviewOrigin("https://"+reqHost, false); reqOrig != "" && got == reqOrig {
		return true
	}
	return false
}

// previewStrippedCookies are Persona cookies that must never reach a proxied dev server.
var previewStrippedCookies = map[string]bool{
	previewCookieName:   true,
	"persona_admin":     true,
	"persona_admin_tls": true,
	"jarvis_admin":      true,
	"jarvis_admin_tls":  true,
}

// stripPreviewOutboundCredentials removes Authorization, Proxy-Authorization, internal Persona
// headers, and hub/preview cookies before forwarding to the dev server, while keeping all
// dev-server cookies verbatim.
func stripPreviewOutboundCredentials(hdr http.Header) {
	hdr.Del("Authorization")
	hdr.Del("Proxy-Authorization")
	for k := range hdr {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-persona-") || strings.HasPrefix(lk, "x-jarvis-") {
			hdr.Del(k)
		}
	}
	cookieVals := hdr.Values("Cookie")
	if len(cookieVals) == 0 {
		return
	}
	var kept []string
	for _, line := range cookieVals {
		for _, part := range strings.Split(line, ";") {
			trimmed := strings.TrimSpace(part)
			if trimmed == "" {
				continue
			}
			name, _, _ := strings.Cut(trimmed, "=")
			if previewStrippedCookies[strings.TrimSpace(name)] {
				continue
			}
			kept = append(kept, trimmed)
		}
	}
	if len(kept) == 0 {
		hdr.Del("Cookie")
	} else {
		hdr.Set("Cookie", strings.Join(kept, "; "))
	}
}

// applyPreviewResponseHeaders enforces hub framing policy and prevents the dev server from
// overwriting the hub's preview session cookie, while keeping all other headers untouched.
func applyPreviewResponseHeaders(hdr http.Header, frameAncestors string) {
	hdr.Del("X-Frame-Options")
	if csps := hdr.Values("Content-Security-Policy"); len(csps) > 0 {
		hdr.Del("Content-Security-Policy")
		for _, v := range csps {
			if stripped := stripCSPFrameAncestors(v); stripped != "" {
				hdr.Add("Content-Security-Policy", stripped)
			}
		}
	}
	hdr.Add("Content-Security-Policy", frameAncestors)

	if setCookies := hdr.Values("Set-Cookie"); len(setCookies) > 0 {
		hdr.Del("Set-Cookie")
		for _, sc := range setCookies {
			name, _, _ := strings.Cut(sc, "=")
			if strings.EqualFold(strings.TrimSpace(name), previewCookieName) {
				continue
			}
			hdr.Add("Set-Cookie", sc)
		}
	}
}

func stripCSPFrameAncestors(policy string) string {
	var kept []string
	for _, directive := range strings.Split(policy, ";") {
		trimmed := strings.TrimSpace(directive)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) > 0 && strings.EqualFold(fields[0], "frame-ancestors") {
			continue
		}
		kept = append(kept, trimmed)
	}
	return strings.Join(kept, "; ")
}

func wantsPreviewHTML(r *http.Request) bool {
	if r.Method != http.MethodGet || isWebSocketUpgrade(r) {
		return false
	}
	dest := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Dest")))
	if dest == "iframe" || dest == "document" {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "text/html") || strings.Contains(accept, "application/xhtml+xml")
}

// writePreviewUnreachable renders either a small HTML fallback page (posting
// {persona_preview: "unreachable", code} to the allowed hub parent origins) for iframe/document
// navigations, or a minimal JSON error for subresources and non-GET requests.
func (h *Hub) writePreviewUnreachable(w http.ResponseWriter, r *http.Request, status int, code, extraAncestor string) {
	ancestors := h.previewFrameAncestors(extraAncestor)
	frameAncestors := previewCSPFrameAncestors(ancestors)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !wantsPreviewHTML(r) {
		w.Header().Set("Content-Security-Policy", frameAncestors)
		writeErrCode(w, status, code, errors.New("preview unreachable: "+code))
		return
	}
	nonce := randomHex(16)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", fmt.Sprintf("default-src 'none'; script-src 'nonce-%s'; style-src 'nonce-%s'; %s", nonce, nonce, frameAncestors))
	targetsJSON, _ := json.Marshal(ancestors)
	codeJSON, _ := json.Marshal(code)
	escapedCode := html.EscapeString(code)
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="referrer" content="no-referrer"><title>Preview unavailable</title>`+
		`<style nonce="%s">body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;font-family:system-ui,-apple-system,sans-serif;background:#111;color:#eee}main{max-width:26rem;padding:1.5rem;text-align:center}p{opacity:.75;font-size:.9rem}</style>`+
		`</head><body><main><h1>Preview unavailable</h1><p>Error: <code>%s</code></p></main>`+
		`<script nonce="%s">(function(){if(window.parent&&window.parent!==window){var msg={persona_preview:"unreachable",code:%s};var targets=%s;for(var i=0;i<targets.length;i++){try{window.parent.postMessage(msg,targets[i]);}catch(e){}}}})();</script>`+
		`</body></html>`,
		nonce, escapedCode, nonce, string(codeJSON), string(targetsJSON))
}
