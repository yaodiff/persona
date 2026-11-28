package hub

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytemare/opaque"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/passwordpake"
)

const passwordFlowLifetime = 5 * time.Minute // Protocol lifetime, not a latency claim.
const passwordFlowLimit = 128                // Bound unauthenticated state, not measured concurrency.

type passwordStore interface {
	ClientPassword(context.Context) (*db.ClientPassword, error)
	SaveClientPassword(context.Context, db.ClientPassword, string, bool) (bool, []string, error)
	IssuePasswordDevice(context.Context, string, db.DeviceToken, string) (bool, error)
}

type passwordFlow struct {
	until                                     time.Time
	registration                              *db.ClientPassword
	admin, previous                           string
	mac                                       []byte
	revision, name, kind, challenge, replaces string
}

type passwordLoginState struct {
	mu             sync.Mutex
	flows          map[string]passwordFlow
	limits, global authLimiter
}

func (s *passwordLoginState) put(f passwordFlow) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.flows {
		if !time.Now().Before(p.until) {
			delete(s.flows, id)
		}
	}
	if len(s.flows) >= passwordFlowLimit {
		return "", false
	}
	if s.flows == nil {
		s.flows = make(map[string]passwordFlow)
	}
	id := randomToken()
	f.until = time.Now().Add(passwordFlowLifetime)
	s.flows[id] = f
	return id, true
}

// take always consumes the state, including failed proofs and wrong PKCE verifiers.
func (s *passwordLoginState) take(id string) (passwordFlow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flows[id]
	delete(s.flows, id)
	return f, ok && time.Now().Before(f.until)
}

func (h *Hub) passwordDB(w http.ResponseWriter) (passwordStore, bool) {
	s, ok := h.store.(passwordStore)
	if !ok {
		writeErrCode(w, http.StatusServiceUnavailable, "unavailable", i18n.Err("hub-password_login.unavailable"))
	}
	return s, ok
}

func passwordJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	d.DisallowUnknownFields() // In particular, never accept a plaintext password field.
	if err := d.Decode(v); err != nil {
		writeErrCode(w, 400, "bad_request", i18n.Err("hub-password_login.invalid_request"))
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		writeErrCode(w, 400, "bad_request", i18n.Err("hub-password_login.invalid_request"))
		return false
	}
	return true
}

func passwordInvalid(w http.ResponseWriter) {
	writeErrCode(w, http.StatusUnauthorized, "bad_password", i18n.Err("hub-password_login.invalid_proof"))
}

func (h *Hub) passwordLimited(w http.ResponseWriter, r *http.Request, count bool) bool {
	now := time.Now()
	s := &h.passwordLogin
	for _, entry := range []struct {
		limiter *authLimiter
		key     string
		cfg     SecurityConfig
	}{
		{&s.limits, authSourceKey(r), h.cfg.Security}, {&s.global, "all", h.publicAdminLimits()},
	} {
		if d := entry.limiter.retry(entry.key, now, entry.cfg); d > 0 {
			retryAfter(w, d)
			writeErrCode(w, 429, "rate_limited", i18n.Err("hub-password_login.rate_limited"))
			return true
		}
	}
	// Charge starts, since a wrong password is normally detected by the client before KE3 is sent.
	if count {
		s.limits.fail(authSourceKey(r), now, h.cfg.Security)
		s.global.fail("all", now, h.publicAdminLimits())
	}
	return false
}

var pkceChallenge = lazyre.New(`^[A-Za-z0-9_-]{43}$`)
var pkceVerifier = lazyre.New(`^[A-Za-z0-9._~-]{43,128}$`)

type passwordStateResponse struct {
	Enabled  bool   `json:"enabled"`
	Suite    string `json:"suite"`
	HubID    string `json:"hub_id"`
	HubName  string `json:"hub_name"`
	Replaces bool   `json:"replaces"`
}

func (h *Hub) passwordLoginRoutes(mux *http.ServeMux) {
	open := http.NewServeMux()
	open.HandleFunc("GET /api/v1/auth/password/state", func(w http.ResponseWriter, r *http.Request) {
		s, ok := h.passwordDB(w)
		if !ok {
			return
		}
		p, err := s.ClientPassword(r.Context())
		if err != nil {
			writeErr(w, 503, err)
			return
		}
		writeJSON(w, passwordStateResponse{
			Enabled:  p != nil && p.Enabled,
			Suite:    passwordpake.Suite,
			HubID:    h.hubID(),
			HubName:  h.hubName(),
			Replaces: true,
		})
	})
	open.HandleFunc("POST /api/v1/auth/password/start", func(w http.ResponseWriter, r *http.Request) {
		if h.passwordLimited(w, r, true) {
			return
		}
		var in struct {
			Message   []byte `json:"message"`
			Name      string `json:"name"`
			Kind      string `json:"kind"`
			Challenge string `json:"challenge"`
			Replaces  string `json:"replaces,omitempty"`
		}
		if !passwordJSON(w, r, &in) {
			return
		}
		in.Replaces = strings.TrimSpace(in.Replaces)
		if in.Challenge != "" && !pkceChallenge.MatchString(in.Challenge) {
			writeErrCode(w, 400, "bad_request", i18n.Err("hub-password_login.invalid_authorization"))
			return
		}
		if in.Kind != "web" && in.Kind != "phone" && in.Kind != "desktop" {
			writeErrCode(w, 400, "bad_request", i18n.Err("hub-password_login.invalid_kind"))
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		if len([]rune(in.Name)) == 0 || len([]rune(in.Name)) > 80 {
			writeErrCode(w, 400, "bad_request", i18n.Err("hub-password_login.invalid_name"))
			return
		}
		s, ok := h.passwordDB(w)
		if !ok {
			return
		}
		p, err := s.ClientPassword(r.Context())
		if err != nil {
			writeErr(w, 503, err)
			return
		}
		if p == nil || !p.Enabled {
			writeErrCode(w, 403, "password_login_disabled", i18n.Err("hub-password_login.disabled"))
			return
		}
		server, err := passwordpake.Server(p.ServerKeys)
		if err != nil {
			writeErr(w, 503, i18n.Err("hub-password_login.invalid_configuration"))
			return
		}
		ke1, err := server.Deserialize.KE1(in.Message)
		if err != nil {
			passwordInvalid(w)
			return
		}
		record, err := server.Deserialize.RegistrationRecord(p.Record)
		if err != nil {
			writeErr(w, 503, i18n.Err("hub-password_login.invalid_configuration"))
			return
		}
		ke2, output, err := server.GenerateKE2(ke1, &opaque.ClientRecord{CredentialIdentifier: p.CredentialID,
			ClientIdentity: passwordpake.ClientIdentity, RegistrationRecord: record})
		if err != nil {
			passwordInvalid(w)
			return
		}
		id, ok := h.passwordLogin.put(passwordFlow{mac: output.ClientMAC, revision: p.Revision, name: in.Name, kind: in.Kind, challenge: in.Challenge, replaces: in.Replaces})
		if !ok {
			writeErrCode(w, 503, "busy", i18n.Err("hub-password_login.busy"))
			return
		}
		writeJSON(w, map[string]any{"session": id, "message": ke2.Serialize(), "identity": passwordpake.ServerIdentity(h.hubID()), "suite": passwordpake.Suite})
	})
	open.HandleFunc("POST /api/v1/auth/password/finish", func(w http.ResponseWriter, r *http.Request) {
		if h.passwordLimited(w, r, false) {
			return
		}
		var in struct {
			Session string `json:"session"`
			Message []byte `json:"message"`
		}
		if !passwordJSON(w, r, &in) {
			return
		}
		f, ok := h.passwordLogin.take(in.Session)
		if !ok || f.mac == nil {
			passwordInvalid(w)
			return
		}
		server, _ := passwordpake.Configuration().Server()
		ke3, err := server.Deserialize.KE3(in.Message)
		if err != nil || server.LoginFinish(ke3, f.mac) != nil {
			passwordInvalid(w)
			return
		}
		if f.challenge != "" {
			f.mac = nil
			code, ok := h.passwordLogin.put(f)
			if !ok {
				writeErrCode(w, 503, "busy", i18n.Err("hub-password_login.retry"))
				return
			}
			writeJSON(w, map[string]string{"code": code})
			return
		}
		h.issuePasswordDevice(w, r, f)
	})
	open.HandleFunc("POST /api/v1/auth/password/exchange", func(w http.ResponseWriter, r *http.Request) {
		if h.passwordLimited(w, r, false) {
			return
		}
		var in struct {
			Code     string `json:"code"`
			Verifier string `json:"verifier"`
		}
		if !passwordJSON(w, r, &in) {
			return
		}
		f, ok := h.passwordLogin.take(in.Code)
		sum := sha256.Sum256([]byte(in.Verifier))
		challenge := base64.RawURLEncoding.EncodeToString(sum[:])
		if !ok || f.mac != nil || f.challenge == "" || !pkceVerifier.MatchString(in.Verifier) || subtle.ConstantTimeCompare([]byte(f.challenge), []byte(challenge)) != 1 {
			passwordInvalid(w)
			return
		}
		h.issuePasswordDevice(w, r, f)
	})
	mux.Handle("/api/v1/auth/password/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if h.memberRefuses(w) {
			return
		}
		// Use the same proxy HTTPS policy as Persona Link; public forwarding headers are not trusted.
		if r.TLS == nil && !h.plainLinkAllowed(r) {
			writeErrCode(w, 403, "tls_required", i18n.Err("hub-password_login.tls_required"))
			return
		}
		if r.Method != http.MethodGet {
			if code, err := adminWriteCheck(r); err != nil {
				writeErrCode(w, 403, code, err)
				return
			}
			// Signing in writes a device token in the hub's database: a snapshot
			// must not see it, and the sign-in succeeds again after the update.
			if !h.quiesce.begin() {
				writeErr(w, http.StatusServiceUnavailable, i18n.Err("hub-selfupdate.err_quiesced"))
				return
			}
			defer h.quiesce.end()
		}
		open.ServeHTTP(w, r)
	}))
}

func (h *Hub) issuePasswordDevice(w http.ResponseWriter, r *http.Request, f passwordFlow) {
	s, ok := h.passwordDB(w)
	if !ok {
		return
	}
	kind := f.kind
	if kind == "desktop" {
		kind = "other"
	}
	d := db.DeviceToken{ID: "dev_" + randomHex(6), Name: f.name, Kind: kind, Role: roleFull}
	token := "jd_" + randomHex(24)
	issued, err := s.IssuePasswordDevice(r.Context(), f.revision, d, hashToken(token))
	if err != nil {
		writeErr(w, 503, err)
		return
	}
	if !issued {
		passwordInvalid(w)
		return
	}
	replaced := h.replaceDeviceToken(r.Context(), d.ID, kind, f.replaces)
	h.notifyPairing(d.Name, d.ID)
	resp := map[string]any{"token": token, "id": d.ID, "hub_id": h.hubID(), "name": d.Name}
	if replaced != "" {
		resp["replaced"] = replaced
	}
	writeJSON(w, resp)
}

func (h *Hub) adminPasswordLoginRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/password-login", func(w http.ResponseWriter, r *http.Request) {
		if h.memberRefuses(w) {
			return
		}
		s, ok := h.passwordDB(w)
		if !ok {
			return
		}
		p, err := s.ClientPassword(r.Context())
		if err != nil {
			writeErr(w, 503, err)
			return
		}
		writeJSON(w, map[string]any{"enabled": p != nil && p.Enabled, "configured": p != nil, "revision": passwordRevision(p)})
	})
	mux.HandleFunc("POST /api/v1/admin/password-login/register/start", func(w http.ResponseWriter, r *http.Request) {
		if h.memberRefuses(w) {
			return
		}
		var in struct {
			Message  []byte `json:"message"`
			Revision string `json:"revision"`
		}
		if !passwordJSON(w, r, &in) {
			return
		}
		server, keys, err := passwordpake.NewServer(h.hubID())
		if err != nil {
			writeErr(w, 503, err)
			return
		}
		request, err := server.Deserialize.RegistrationRequest(in.Message)
		if err != nil {
			writeErrCode(w, 400, "bad_request", i18n.Err("hub-password_login.invalid_registration_message"))
			return
		}
		credential := opaque.RandomBytes(64)
		response, err := server.RegistrationResponse(request, credential, nil)
		if err != nil {
			writeErr(w, 503, err)
			return
		}
		session, _ := h.currentAdminSession(r)
		if session == nil {
			writeErrCode(w, 401, "login_required", i18n.Err("hub-password_login.login_required"))
			return
		}
		id, ok := h.passwordLogin.put(passwordFlow{admin: session.IDHash, previous: in.Revision,
			registration: &db.ClientPassword{Enabled: true, Revision: randomToken(), ServerKeys: keys, CredentialID: credential}})
		if !ok {
			writeErrCode(w, 503, "busy", i18n.Err("hub-password_login.settings_busy"))
			return
		}
		writeJSON(w, map[string]any{"session": id, "message": response.Serialize(), "identity": passwordpake.ServerIdentity(h.hubID()), "suite": passwordpake.Suite})
	})
	mux.HandleFunc("POST /api/v1/admin/password-login/register/finish", func(w http.ResponseWriter, r *http.Request) {
		if h.memberRefuses(w) {
			return
		}
		var in struct {
			Session string `json:"session"`
			Message []byte `json:"message"`
			Revoke  bool   `json:"revoke_devices"`
		}
		if !passwordJSON(w, r, &in) {
			return
		}
		f, ok := h.passwordLogin.take(in.Session)
		session, _ := h.currentAdminSession(r)
		if !ok || f.registration == nil || session == nil || f.admin != session.IDHash {
			writeErrCode(w, 400, "invalid_registration", i18n.Err("hub-password_login.expired_settings"))
			return
		}
		server, _ := passwordpake.Configuration().Server()
		if _, err := server.Deserialize.RegistrationRecord(in.Message); err != nil {
			writeErrCode(w, 400, "bad_request", i18n.Err("hub-password_login.invalid_record"))
			return
		}
		f.registration.Record = in.Message
		h.savePasswordLogin(w, r, *f.registration, f.previous, in.Revoke)
	})
	mux.HandleFunc("PUT /api/v1/admin/password-login", func(w http.ResponseWriter, r *http.Request) {
		if h.memberRefuses(w) {
			return
		}
		var in struct {
			Enabled  bool   `json:"enabled"`
			Revision string `json:"revision"`
			Revoke   bool   `json:"revoke_devices"`
		}
		if !passwordJSON(w, r, &in) {
			return
		}
		s, ok := h.passwordDB(w)
		if !ok {
			return
		}
		p, err := s.ClientPassword(r.Context())
		if err != nil {
			writeErr(w, 503, err)
			return
		}
		if p == nil {
			writeErrCode(w, 409, "not_set", i18n.Err("hub-password_login.not_set"))
			return
		}
		p.Enabled, p.Revision = in.Enabled, randomToken()
		h.savePasswordLogin(w, r, *p, in.Revision, in.Revoke)
	})
}

func passwordRevision(p *db.ClientPassword) string {
	if p == nil {
		return ""
	}
	return p.Revision
}

func (h *Hub) savePasswordLogin(w http.ResponseWriter, r *http.Request, p db.ClientPassword, previous string, revoke bool) {
	s, ok := h.passwordDB(w)
	if !ok {
		return
	}
	saved, ids, err := s.SaveClientPassword(r.Context(), p, previous, revoke)
	if err != nil {
		writeErr(w, 503, err)
		return
	}
	if !saved {
		writeErrCode(w, 409, "stale_settings", i18n.Err("hub-password_login.stale_settings"))
		return
	}
	for _, id := range ids {
		h.revoked(id)
	}
	writeJSON(w, map[string]any{"enabled": p.Enabled, "configured": true, "revision": p.Revision})
}
