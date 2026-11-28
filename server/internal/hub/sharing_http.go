package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/shareproof"
)

const publicSharePrefix = "/api/public/agent-shares/"

// shareManagementSlots bounds concurrent owner-admitted management relays.
var shareManagementSlots = make(chan struct{}, 2)

const shareCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'none'; font-src 'none'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'; worker-src 'none'"

func shareHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, noarchive, nofollow")
	w.Header().Set("Content-Security-Policy", shareCSP)
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
}
func canonicalShareOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(u.Host, "\\ \t\r\n") {
		return ""
	}
	if u.Hostname() == "" || strings.HasSuffix(u.Host, ":") {
		return ""
	}
	if u.Port() != "" {
		port := atoi(u.Port())
		if port < 1 || port > 65535 {
			return ""
		}
	}
	host := strings.ToLower(u.Host)
	if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
		host = u.Hostname()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	return u.Scheme + "://" + host
}
func (h *Hub) publicShareBuildExists() bool {
	if h.cfg == nil {
		return false
	}
	if st, err := os.Stat(filepath.Join(h.cfg.dir, "web", "public-share", "index.html")); err == nil && st.Mode().IsRegular() {
		return true
	}
	st, err := fs.Stat(webDist, "webdist/public-share/index.html")
	return err == nil && st.Mode().IsRegular()
}

func (h *Hub) sharingOrigin() string {
	if h.cfg == nil || h.cfg.Sharing == nil {
		return ""
	}
	origin := canonicalShareOrigin(h.cfg.Sharing.PublicOrigin)
	if origin == "" {
		return ""
	}
	u, err := url.Parse(h.cfg.PublicURL)
	if err != nil || u.Host == "" {
		return ""
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	u.ForceQuery = false
	if canonicalShareOrigin(u.String()) == origin {
		return ""
	}
	return origin
}
func exactPublicShare(r *http.Request) bool {
	rest, ok := strings.CutPrefix(r.URL.Path, publicSharePrefix)
	return ok && r.Method == http.MethodGet && rest != "" && !strings.Contains(rest, "/") && r.URL.RawPath == ""
}
func (h *Hub) sharingRoutes(api *http.ServeMux) {
	for _, pattern := range devicewire.ShareRoutes {
		api.HandleFunc(pattern, h.forwardShareManagement)
	}
}
func (h *Hub) centerSharing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, publicSharePrefix) {
			shareHeaders(w)
			if !exactPublicShare(r) {
				shareNotFound(w)
				return
			}
			h.forwardSharePublic(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func shareNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(404)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": "not_found", "error": i18n.T(i18n.En, "sharing.not_found")})
}
func (h *Hub) shareTransportError(w http.ResponseWriter, r *http.Request, status int, code string) {
	shareHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	key := "sharing.unavailable"
	if code == "owner_enrollment_required" {
		key = "sharing.enrollment_required"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "error": i18n.Localize(i18n.Err(key), h.requestLang(r))})
}
func (h *Hub) shareManagerCapable(id string) bool {
	h.bus.mu.Lock()
	defer h.bus.mu.Unlock()
	c := h.bus.current[id]
	return c != nil && !c.ended && !c.replaced && slices.Contains(c.presence.Capabilities, devicewire.SharingCapability)
}
func (h *Hub) forwardShareManagement(w http.ResponseWriter, r *http.Request) {
	shareHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if _, err := adminWriteCheck(r); err != nil {
			h.shareTransportError(w, r, 400, "invalid_request")
			return
		}
	}
	id := r.Header.Get(ManagerHeader)
	if id == "" || len(r.Header.Values(ManagerHeader)) != 1 || r.URL.Query().Has("manager") {
		h.shareTransportError(w, r, 400, "manager_required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || len(r.Header.Values("Authorization")) != 1 || r.URL.Query().Has("token") {
		h.shareTransportError(w, r, 403, "owner_enrollment_required")
		return
	}
	s := h.ownerAuthStore()
	if s == nil {
		h.shareTransportError(w, r, 503, "authority_unavailable")
		return
	}
	// An admitted relay holds one database connection for its whole duration and
	// takes up to two more briefly. Bounding concurrent relays keeps that well below
	// the hub's pool, so sharing traffic can never starve every other hub query.
	select {
	case shareManagementSlots <- struct{}{}:
		defer func() { <-shareManagementSlots }()
	case <-time.After(3 * time.Second):
		w.Header().Set("Retry-After", "1")
		h.shareTransportError(w, r, 429, "busy")
		return
	case <-ctx.Done():
		h.shareTransportError(w, r, 503, "authority_unavailable")
		return
	}
	binding, release, err := s.AdmitOwner(ctx, h.hubID(), hashToken(strings.TrimPrefix(auth, "Bearer ")))
	if err != nil {
		h.shareTransportError(w, r, 403, "owner_enrollment_required")
		return
	}
	defer release()
	c := devicewire.Caller{Kind: "device", Issuer: h.hubID(), CredentialID: binding.CredentialID, OwnerSubject: *binding.Subject, BindingRevision: binding.Revision, AdmissionID: rand.Text()}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		h.shareTransportError(w, r, 400, "invalid_request")
		return
	}
	machine := r.PathValue("mid")
	if r.URL.Path == "/api/v1/agent-shares" && r.Method == http.MethodPost {
		var p struct {
			Machine string `json:"machine"`
		}
		if json.Unmarshal(body, &p) != nil {
			h.shareTransportError(w, r, 400, "invalid_request")
			return
		}
		machine = p.Machine
	}
	var a *devicewire.ShareAuthority
	if machine != "" {
		if h.machineOwner(machine) != id || h.localMachine(machine) == nil {
			h.shareTransportError(w, r, 409, "authority_unavailable")
			return
		}
		store := h.shareRoutingStore()
		if store == nil {
			h.shareTransportError(w, r, 503, "authority_unavailable")
			return
		}
		route, err := store.ShareRouting(ctx, h.hubID(), machine, id)
		if err != nil {
			h.shareTransportError(w, r, 409, "authority_unavailable")
			return
		}
		a = &route.Authority
	}
	if !h.shareManagerCapable(id) {
		h.shareTransportError(w, r, 409, "manager_unsupported")
		return
	}
	h.shareRelay(w, r, id, c, a, body, false)
}

// shareRelay has a positive response-header allowlist, an explicit caller, a
// bounded deadline/body, and no operation receipt or capability response cache.
func (h *Hub) shareRelay(w http.ResponseWriter, r *http.Request, id string, c devicewire.Caller, a *devicewire.ShareAuthority, body []byte, public bool) {
	query := r.URL.Query()
	query.Del("token")
	query.Del("manager")
	req := assistantwire.HTTPRequest{Method: r.Method, Path: r.URL.EscapedPath(), Query: query.Encode(), Body: body, Caller: &c, ShareAuthority: a, Header: map[string][]string{"Content-Type": {"application/json"}, assistantwire.RoleHeader: {c.Kind}, "Accept-Language": {string(h.requestLang(r))}}}
	if c.Kind == "admin" {
		req.Header[assistantwire.AdminHeader] = []string{"1"}
	}
	res, err := buswire.Call(r.Context(), h.managerCaller(id), assistantwire.HTTPMethod, req)
	if err != nil {
		if public {
			shareNotFound(w)
		} else {
			h.shareTransportError(w, r, 503, "authority_unavailable")
		}
		return
	}
	maxBody := 512 << 10
	if !public {
		maxBody = 2 << 20
	}
	if len(res.Body) > maxBody || res.Status < 200 || res.Status >= 300 && res.Status < 400 || res.Status > 599 || !json.Valid(res.Body) {
		if public {
			shareNotFound(w)
		} else {
			h.shareTransportError(w, r, 502, "authority_unavailable")
		}
		return
	}
	if res.Status < 300 {
		if public && res.ShareAuthority == nil {
			shareNotFound(w)
			return
		}
		if res.ShareAuthority != nil {
			store := h.shareRoutingStore()
			if store == nil || h.machineOwner(res.ShareAuthority.Machine) != id || (a != nil && *a != *res.ShareAuthority) || store.VerifyShareAuthority(r.Context(), id, *res.ShareAuthority) != nil {
				if public {
					shareNotFound(w)
				} else {
					h.shareTransportError(w, r, 409, "authority_unavailable")
				}
				return
			}
		}
	}
	if public && res.Status != 200 && res.Status != 409 && res.Status != 429 {
		shareNotFound(w)
		return
	}
	// Manager error strings are not trusted to avoid echoing a bearer or SQL text.
	if res.Status >= 400 {
		var e struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(res.Body, &e)
		allowed := map[string]bool{"share_changed": true, "revision_conflict": true, "creation_already_committed": true, "idempotency_conflict": true, "owner_enrollment_required": true, "ownership_unbound": true, "snapshot_changed": true, "preview_required": true, "preview_changed": true, "public_origin_unavailable": true, "insecure_transport_ack_required": true, "invalid_request": true, "invalid_selection": true, "invalid_expiry": true, "source_limit": true, "share_limit": true, "not_found": true, "authority_unavailable": true, "inspection_changed": true, "ownership_conflict": true}
		if !allowed[e.Code] {
			e.Code = "authority_unavailable"
		}
		if res.Status == 409 && e.Code == "creation_already_committed" && !public {
			// Only tokenless metadata survives an idempotent creation replay.
			var replay struct {
				Share json.RawMessage `json:"share"`
			}
			_ = json.Unmarshal(res.Body, &replay)
			if replay.Share != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(409)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": e.Code, "share": replay.Share})
				return
			}
		}
		h.shareTransportError(w, r, res.Status, e.Code)
		return
	}
	shareHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

type shareBucket struct {
	credit       float64
	at, timeSeen time.Time
}
type shareIngress struct {
	mu      sync.Mutex
	buckets map[string]shareBucket
	active  int
}

func (s *shareIngress) enter(ip, token string, now time.Time) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buckets == nil {
		s.buckets = map[string]shareBucket{}
	}
	for k, b := range s.buckets {
		if now.Sub(b.timeSeen) > 5*time.Minute {
			delete(s.buckets, k)
		}
	}
	if s.active >= 32 {
		return nil, false
	}
	consume := func(key string, rate, burst float64) bool {
		b, ok := s.buckets[key]
		if !ok {
			// A full table evicts its least recently seen bucket instead of
			// refusing everyone, so a flood of new sources cannot lock readers out.
			if len(s.buckets) >= 4096 {
				oldest, at := "", now
				for k, v := range s.buckets {
					if !v.timeSeen.After(at) {
						oldest, at = k, v.timeSeen
					}
				}
				delete(s.buckets, oldest)
			}
			b = shareBucket{credit: burst, at: now}
		}
		b.credit = min(burst, b.credit+now.Sub(b.at).Seconds()*rate)
		b.at, b.timeSeen = now, now
		if b.credit < 1 {
			s.buckets[key] = b
			return false
		}
		b.credit--
		s.buckets[key] = b
		return true
	}
	if !consume("ip:"+ip, 1, 20) || (token != "" && !consume("token:"+token, 2, 30)) {
		return nil, false
	}
	s.active++
	return func() { s.mu.Lock(); s.active--; s.mu.Unlock() }, true
}

// shareSourceKey groups an IPv6 source by its /64, the unit one subscriber
// usually controls, so rotating addresses inside it buys no extra budget.
func shareSourceKey(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	a = a.Unmap()
	if a.Is6() {
		if p, err := a.Prefix(64); err == nil {
			return p.String()
		}
	}
	return a.String()
}
func (h *Hub) sharingPeer(r *http.Request, now time.Time) (string, bool) {
	if h.cfg != nil && h.cfg.Sharing != nil {
		if ip, ok := shareproof.Verify(shareproof.DecodeKey(h.cfg.Sharing.IngressKey), r, now); ok {
			return net.JoinHostPort(ip.String(), "0"), true
		}
	}
	return r.RemoteAddr, false
}

func (h *Hub) forwardSharePublic(w http.ResponseWriter, r *http.Request) {
	shareHeaders(w)
	token := strings.TrimPrefix(r.URL.Path, publicSharePrefix)
	raw, err := base64.RawURLEncoding.DecodeString(token)
	wellFormed := len(token) == 64 && err == nil && len(raw) == 48 && base64.RawURLEncoding.EncodeToString(raw) == token
	// Only a well-formed capability gets a token bucket; malformed paths spend the
	// source's budget alone and cannot fill the table with throwaway keys.
	tokenKey := ""
	if wellFormed {
		hash := sha256.Sum256([]byte(token))
		tokenKey = hex.EncodeToString(hash[:])
	}
	now := time.Now()
	peer, authenticated := h.sharingPeer(r, now)
	if authenticated {
		w.Header().Set(shareproof.Acknowledgement, shareproof.Accepted)
	}
	release, ok := h.shareIngress.enter(shareSourceKey(peer), tokenKey, now)
	if !ok {
		w.Header().Set("Retry-After", "15")
		h.shareTransportError(w, r, 429, "rate_limited")
		return
	}
	defer release()
	if !wellFormed || h.sharingOrigin() == "" {
		shareNotFound(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	store := h.shareRoutingStore()
	if store == nil {
		shareNotFound(w)
		return
	}
	id, err := store.ShareManagerByHandle(ctx, raw[:16])
	if err != nil || !h.shareManagerCapable(id) {
		shareNotFound(w)
		return
	}
	h.shareRelay(w, r, id, devicewire.Caller{Kind: "anonymous"}, nil, nil, true)
}

// sharingDispatcher is outside the application's ingress gate. An HTTP sharing
// origin is an explicit deployment choice, not permission for plaintext app auth.
func (h *Hub) sharingDispatcher(next http.Handler) http.Handler {
	embedded, _ := fs.Sub(webDist, "webdist/public-share")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := h.sharingOrigin()
		u, _ := url.Parse(origin)
		hostOrigin := ""
		if u != nil {
			hostOrigin = canonicalShareOrigin(u.Scheme + "://" + r.Host)
		}
		if origin != "" && hostOrigin == origin {
			shareHeaders(w)
			if exactPublicShare(r) {
				h.memberForward(h.centerSharing(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { shareNotFound(w) }))).ServeHTTP(w, r)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				shareNotFound(w)
				return
			}
			name := ""
			if rest, ok := strings.CutPrefix(r.URL.Path, "/share/"); ok && rest != "" && !strings.Contains(rest, "/") && r.URL.RawPath == "" {
				name = "index.html"
			} else if strings.HasPrefix(r.URL.Path, "/share-assets/") && r.URL.RawPath == "" && path.Clean(r.URL.Path) == r.URL.Path {
				name = strings.TrimPrefix(r.URL.Path, "/")
			}
			if name == "" || strings.Contains(name, "..") {
				shareNotFound(w)
				return
			}
			var site fs.FS
			dir := filepath.Join(h.cfg.dir, "web", "public-share")
			if st, err := os.Stat(filepath.Join(dir, "index.html")); err == nil && st.Mode().IsRegular() {
				site = os.DirFS(dir)
			} else {
				site = embedded
			}
			if site == nil {
				shareNotFound(w)
				return
			}
			f, err := site.Open(name)
			if err != nil {
				shareNotFound(w)
				return
			}
			defer f.Close()
			st, err := f.Stat()
			if err != nil || !st.Mode().IsRegular() || st.Size() > 2<<20 {
				shareNotFound(w)
				return
			}
			b, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
			if err != nil || len(b) > 2<<20 {
				shareNotFound(w)
				return
			}
			w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
			if r.Method == http.MethodGet {
				_, _ = w.Write(b)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/share/") || strings.HasPrefix(r.URL.Path, "/share-assets/") || strings.HasPrefix(r.URL.Path, "/public-share/") || strings.HasPrefix(r.URL.Path, publicSharePrefix) {
			shareHeaders(w)
			shareNotFound(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (h *Hub) adminShareRoutes(mux *http.ServeMux) {
	for _, pattern := range []string{"GET /api/v1/admin/sessions/{mid}/{sid}/ownership-inspection", "POST /api/v1/admin/sessions/{mid}/{sid}/owner"} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			shareHeaders(w)
			if h.memberRefuses(w) {
				return
			}
			session, ok := h.adminSession(w, r)
			if !ok {
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			r = r.WithContext(ctx)
			machine := r.PathValue("mid")
			id := h.machineOwner(machine)
			if h.localMachine(machine) == nil || !h.shareManagerCapable(id) {
				h.shareTransportError(w, r, 409, "authority_unavailable")
				return
			}
			store := h.shareRoutingStore()
			if store == nil {
				h.shareTransportError(w, r, 503, "authority_unavailable")
				return
			}
			route, err := store.ShareRouting(ctx, h.hubID(), machine, id)
			if err != nil {
				h.shareTransportError(w, r, 409, "authority_unavailable")
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			if err != nil {
				h.shareTransportError(w, r, 400, "invalid_request")
				return
			}
			caller := devicewire.Caller{Kind: "admin", Issuer: h.hubID(), CredentialID: session.IDHash, AdmissionID: rand.Text()}
			if r.Method == http.MethodPost {
				var p struct {
					Subject string `json:"owner_subject"`
				}
				if json.Unmarshal(body, &p) != nil {
					h.shareTransportError(w, r, 400, "invalid_request")
					return
				}
				auth := h.ownerAuthStore()
				if auth == nil {
					h.shareTransportError(w, r, 503, "authority_unavailable")
					return
				}
				subjects, err := auth.OwnerSubjects(ctx, h.hubID())
				found := false
				for _, s := range subjects {
					if s.Subject == p.Subject {
						found = true
					}
				}
				if err != nil || !found {
					h.shareTransportError(w, r, 400, "invalid_request")
					return
				}
				caller.OwnerSubject = p.Subject
			}
			h.shareRelay(w, r, id, caller, &route.Authority, body, false)
		})
	}
}
