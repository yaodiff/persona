package hub

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Browser sign-in (#6862, #10291; docs/protocol/protocol.md "Browser QR sign-in"): a browser on the sign-in page asks
// for a code, a signed-in full device scans it and allows it, the device then shows a two-digit number, and the
// browser must type that number before it gets its own web credential. A wrong number voids the request: someone who
// sends a code to the owner would need the owner to read the number back to them.
//
// Requests are short-lived handshakes kept in memory, like passwordLoginState. Durable device credentials are issued
// only after the number matches, and are stored in Postgres.
type browserLoginFlow struct {
	Source                                             string
	Masked                                             string // the source as auth logs write it (maskedSource)
	Network                                            string // browserNetwork: what the approving device is told
	CreatedAt                                          time.Time
	Issued                                             bool
	Secret, Name, State, ApproverHash, Token, DeviceID string
	Number                                             string // shown on the approving device, typed in the browser
	Replaces, Replaced                                 string // this browser's previous web credential, as in POST /auth/tokens
	Until                                              time.Time
}

// States: pending → scanned (a device looked) → confirm (a device allowed; waiting for the browser's number) →
// approved (the number matched). denied (the device said no) and voided (a wrong number) end a request.
const (
	browserPending  = "pending"
	browserScanned  = "scanned"
	browserConfirm  = "confirm"
	browserApproved = "approved"
	browserDenied   = "denied"
	browserVoided   = "voided"
)

type browserLoginState struct {
	mu    sync.Mutex
	flows map[string]*browserLoginFlow
	// Its own limits (#10291): opening the sign-in page, refreshing its code or polling it never counts against
	// password sign-in (passwordLimited), and a block here leaves password sign-in open.
	starts, global, misses authLimiter
}

// Protocol values, not measured capacity: a person refreshes a code a few times a minute at most, and a browser
// that polls its own code never misses.
var (
	browserStartLimits = SecurityConfig{AuthFailures: 20, AuthWindowSeconds: 60, AuthBlockSeconds: 60}  // starts per source
	browserStartGlobal = SecurityConfig{AuthFailures: 200, AuthWindowSeconds: 60, AuthBlockSeconds: 60} // starts, all sources
	browserPollMisses  = SecurityConfig{AuthFailures: 20, AuthWindowSeconds: 60, AuthBlockSeconds: 60}  // unknown id or wrong secret, per source
)

// browserLimited answers 429 for a source (or, for starts, everyone) over browser sign-in's own limits. A start is
// counted here; a poll or a confirm counts only when it misses.
func (h *Hub) browserLimited(w http.ResponseWriter, r *http.Request, start bool) bool {
	now, key, s := time.Now(), authSourceKey(r), &h.browserLogin
	type check struct {
		limiter *authLimiter
		key     string
		cfg     SecurityConfig
	}
	checks := []check{{&s.misses, key, browserPollMisses}}
	if start {
		checks = []check{{&s.starts, key, browserStartLimits}, {&s.global, "all", browserStartGlobal}}
	}
	for _, c := range checks {
		if d := c.limiter.retry(c.key, now, c.cfg); d > 0 {
			retryAfter(w, d)
			writeErrCode(w, http.StatusTooManyRequests, "rate_limited", i18n.Err("hub-password_login.rate_limited"))
			return true
		}
	}
	if start {
		s.starts.fail(key, now, browserStartLimits)
		s.global.fail("all", now, browserStartGlobal)
	}
	return false
}

// browserNetwork says in one word where a request came from, for the device asked to allow it: "internet", "lan", or
// "unknown" when the connection's address says nothing about the browser: a container's gateway, or a proxy on this
// machine or the LAN (any forwarding header on a request that isn't from the internet). Forwarding headers are never
// believed for the answer, only taken as a sign that something relayed the request.
func (h *Hub) browserNetwork(r *http.Request) string {
	if ip := remoteIP(r); ip.IsValid() && h.adminGateway(ip) {
		return "unknown"
	}
	if h.sourceOf(r) == sourcePublic {
		return "internet"
	}
	for _, k := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Real-Ip", "Via"} {
		if r.Header.Get(k) != "" {
			return "unknown"
		}
	}
	return "lan"
}

// requestToken is the credential a request authenticated with, as auth reads it.
func requestToken(r *http.Request) string {
	if tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); tok != "" {
		return tok
	}
	return r.URL.Query().Get("token")
}

// browserNumber is a fresh two-digit number, 10–99.
func browserNumber() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(90))
	if err != nil {
		return "", err
	}
	return fmt.Sprint(10 + n.Int64()), nil
}

// browserFlow finds the browser's own request (its id and secret). Anything else counts as a miss and answers 410.
func (h *Hub) browserFlow(w http.ResponseWriter, r *http.Request, id, secret string) *browserLoginFlow {
	f := h.browserLogin.flows[id]
	if f == nil || !time.Now().Before(f.Until) || subtle.ConstantTimeCompare([]byte(f.Secret), []byte(hashToken(secret))) != 1 {
		h.browserLogin.misses.fail(authSourceKey(r), time.Now(), browserPollMisses)
		writeErrCode(w, http.StatusGone, "expired", i18n.Err("hub-browser_login.expired"))
		return nil
	}
	return f
}

// browserConflict answers 409 with the request's state, so the browser can show where it ended.
func browserConflict(w http.ResponseWriter, code string, err error, state string) {
	body := apierr.LocalizedBody(http.StatusConflict, code, err, i18n.OfWriter(w))
	body["state"] = state
	writeErrorBody(w, http.StatusConflict, body)
}

// browserIssue answers an approved request with its credential, creating it the first time. The same browser can
// retry a lost response without creating another device. Called with h.browserLogin.mu held.
func (h *Hub) browserIssue(w http.ResponseWriter, r *http.Request, f *browserLoginFlow) {
	approver, err := h.store.DeviceTokenByHash(r.Context(), f.ApproverHash)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	if approver == nil || approver.Role != roleFull {
		writeErrCode(w, http.StatusForbidden, "approver_revoked", i18n.Err("hub-browser_login.approver_revoked"))
		return
	}
	if f.Token == "" {
		f.Token, f.DeviceID = "jd_"+randomHex(24), "dev_"+randomHex(6)
	}
	if !f.Issued {
		// Reconcile a commit whose acknowledgement was lost using the same identity.
		existing, err := h.store.DeviceTokenByHash(r.Context(), hashToken(f.Token))
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		if existing == nil {
			if err := h.store.CreateDeviceToken(r.Context(), db.DeviceToken{ID: f.DeviceID, Name: f.Name, Kind: "web", Role: roleFull}, hashToken(f.Token)); err != nil {
				writeErr(w, http.StatusServiceUnavailable, err)
				return
			}
		}
		f.Issued = true
		h.logf("device token %s for %q (web, full) by browser sign-in, approved by device %s %q", f.DeviceID, f.Name, approver.ID, approver.Name)
		// Only a web credential, as in POST /auth/tokens: an id a browser kept cannot take a phone, a watch or a link off.
		if replaced := h.replaceDeviceToken(r.Context(), f.DeviceID, "web", f.Replaces); replaced != "" {
			f.Replaced = replaced
		}
		h.notifyPairing(f.Name, f.DeviceID)
	}
	resp := map[string]any{"state": browserApproved, "token": f.Token, "id": f.DeviceID}
	if f.Replaced != "" {
		resp["replaced"] = f.Replaced
	}
	writeJSON(w, resp)
}

func (h *Hub) browserLoginRoutes(mux *http.ServeMux) {
	routes := http.NewServeMux()
	routes.HandleFunc("POST /api/v1/auth/browser-login/start", func(w http.ResponseWriter, r *http.Request) {
		if h.browserLimited(w, r, true) {
			return
		}
		var in struct {
			Name     string `json:"name"`
			Replaces string `json:"replaces,omitempty"`
		}
		if !passwordJSON(w, r, &in) {
			return
		}
		in.Name, in.Replaces = strings.TrimSpace(in.Name), strings.TrimSpace(in.Replaces)
		if in.Name == "" || len([]rune(in.Name)) > 80 || strings.IndexFunc(in.Name, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			writeErrCode(w, http.StatusBadRequest, "bad_request", i18n.Err("hub-browser_login.invalid_name"))
			return
		}
		if len(in.Replaces) > 64 {
			writeErrCode(w, http.StatusBadRequest, "bad_request", i18n.Err("hub-browser_login.invalid_replaces"))
			return
		}
		s := &h.browserLogin
		s.mu.Lock()
		defer s.mu.Unlock()
		for id, f := range s.flows {
			if !time.Now().Before(f.Until) {
				delete(s.flows, id)
			}
		}
		if len(s.flows) >= passwordFlowLimit {
			writeErrCode(w, http.StatusTooManyRequests, "busy", i18n.Err("hub-browser_login.busy"))
			return
		}
		if s.flows == nil {
			s.flows = map[string]*browserLoginFlow{}
		}
		id, secret := randomToken(), randomToken()
		f := &browserLoginFlow{Secret: hashToken(secret), Name: in.Name, Source: remoteIP(r).String(), Masked: maskedSource(r), Network: h.browserNetwork(r),
			Replaces: in.Replaces, CreatedAt: time.Now(), State: browserPending, Until: time.Now().Add(passwordFlowLifetime)}
		link := "persona://browser-login?" + url.Values{"hub": {h.hubID()}, "request": {id}}.Encode()
		qr, err := newQRImage(link)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.flows[id] = f
		writeJSON(w, map[string]any{"id": id, "secret": secret, "expires_at": f.Until, "qr": qr, "url": link})
	})
	routes.HandleFunc("POST /api/v1/auth/browser-login/poll", func(w http.ResponseWriter, r *http.Request) {
		if h.browserLimited(w, r, false) {
			return
		}
		var in struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
		}
		if !passwordJSON(w, r, &in) {
			return
		}
		s := &h.browserLogin
		s.mu.Lock()
		defer s.mu.Unlock()
		f := h.browserFlow(w, r, in.ID, in.Secret)
		if f == nil {
			return
		}
		if f.State != browserApproved {
			writeJSON(w, map[string]any{"state": f.State})
			return
		}
		h.browserIssue(w, r, f)
	})
	// The browser types the number the approving device shows. One try: a wrong number voids the request.
	routes.HandleFunc("POST /api/v1/auth/browser-login/confirm", func(w http.ResponseWriter, r *http.Request) {
		if h.browserLimited(w, r, false) {
			return
		}
		var in struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
			Number string `json:"number"`
		}
		if !passwordJSON(w, r, &in) {
			return
		}
		s := &h.browserLogin
		s.mu.Lock()
		defer s.mu.Unlock()
		f := h.browserFlow(w, r, in.ID, in.Secret)
		if f == nil {
			return
		}
		if f.State != browserConfirm && f.State != browserApproved {
			browserConflict(w, "not_waiting", i18n.Err("hub-browser_login.not_waiting"), f.State)
			return
		}
		if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(in.Number)), []byte(f.Number)) != 1 {
			if f.State == browserConfirm {
				f.State = browserVoided
				h.logf("browser sign-in for %q (requested from %s) voided: the number typed did not match", f.Name, f.Masked)
			}
			browserConflict(w, "number_mismatch", i18n.Err("hub-browser_login.number_mismatch"), f.State)
			return
		}
		f.State = browserApproved
		h.browserIssue(w, r, f)
	})
	approval := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := principalOf(r.Context())
		if !mayTrust(who) || who.ID == "" {
			writeErrCode(w, http.StatusForbidden, "forbidden", i18n.Err("hub-browser_login.device_required"))
			return
		}
		var in struct {
			ID    string `json:"id"`
			Hub   string `json:"hub"`
			Allow *bool  `json:"allow,omitempty"`
			// The device will show the number (browser_qr_match). An app from before it would close and leave the
			// browser waiting for a number nobody saw, so its Allow is refused with update_required.
			ShowNumber bool `json:"show_number,omitempty"`
		}
		if !passwordJSON(w, r, &in) {
			return
		}
		if in.Hub != h.hubID() {
			writeErrCode(w, http.StatusBadRequest, "other_hub", i18n.Err("hub-browser_login.other_hub"))
			return
		}
		s := &h.browserLogin
		s.mu.Lock()
		defer s.mu.Unlock()
		f := s.flows[in.ID]
		if f == nil || !time.Now().Before(f.Until) {
			writeErrCode(w, http.StatusGone, "expired", i18n.Err("hub-browser_login.expired"))
			return
		}
		caller := hashToken(requestToken(r))
		switch {
		case in.Allow == nil:
			if f.State == browserPending {
				f.State = browserScanned
			}
		case *in.Allow && !in.ShowNumber:
			writeErrCode(w, http.StatusBadRequest, "update_required", i18n.Err("hub-browser_login.update_required"))
			return
		case *in.Allow:
			if f.State != browserPending && f.State != browserScanned {
				writeErrCode(w, http.StatusConflict, "decided", i18n.Err("hub-browser_login.decided"))
				return
			}
			number, err := browserNumber()
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			f.State, f.Number, f.ApproverHash = browserConfirm, number, caller
			h.logf("browser sign-in for %q (requested from %s, %s) allowed by device %s %q; waiting for the browser's number", f.Name, f.Masked, f.Network, who.ID, who.Name)
		default:
			// The device that allowed it can still cancel while the browser hasn't typed the number.
			if f.State != browserPending && f.State != browserScanned && f.State != browserConfirm {
				writeErrCode(w, http.StatusConflict, "decided", i18n.Err("hub-browser_login.decided"))
				return
			}
			f.State = browserDenied
			h.logf("browser sign-in for %q (requested from %s, %s) denied by device %s %q", f.Name, f.Masked, f.Network, who.ID, who.Name)
		}
		resp := map[string]any{"name": f.Name, "state": f.State, "expires_at": f.Until, "source": f.Source, "network": f.Network,
			"age_seconds": int(time.Since(f.CreatedAt).Seconds())}
		// Only the device that allowed it sees the number, again if it asks again (its app was closed meanwhile).
		if f.State == browserConfirm && caller == f.ApproverHash {
			resp["number"] = f.Number
		}
		writeJSON(w, resp)
	})
	routes.Handle("POST /api/v1/auth/browser-login/approve", h.auth(approval))
	// Transport as link sign-in has it (gate): plaintext is refused only from the internet, so a hub on plain LAN HTTP
	// and one behind an HTTPS proxy both work. The credential this hands out is the same kind POST /auth/tokens does.
	mux.Handle("/api/v1/auth/browser-login/", h.gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if h.memberRefuses(w) {
			return
		}
		if code, err := adminWriteCheck(r); err != nil {
			writeErrCode(w, http.StatusForbidden, code, err)
			return
		}
		// Approving a sign-in writes a device token in the hub's database: a
		// snapshot must not see it, and the sign-in works again after the update.
		if isWriteRequest(r) {
			if !h.quiesce.begin() {
				writeErr(w, http.StatusServiceUnavailable, i18n.Err("hub-selfupdate.err_quiesced"))
				return
			}
			defer h.quiesce.end()
		}
		if h.store == nil {
			writeErrCode(w, http.StatusServiceUnavailable, "unavailable", i18n.Err("hub-browser_login.unavailable"))
			return
		}
		routes.ServeHTTP(w, r)
	})))
}
