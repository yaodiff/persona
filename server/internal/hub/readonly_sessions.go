package hub

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Controlled read-only verification sessions: temporary, opt-in credentials (roleReadOnly / tokenKindReadOnly)
// gated by security.allow_readonly_sessions (PERSONA_ALLOW_READONLY_SESSIONS, default false).
//
// Read-only sessions are authorized by default-deny exact Go 1.22 ServeMux route patterns inside Hub.auth
// (before extensionAPIs), never carry AdminHeader/PersonHeader/ownerExtension, suppress read-path side
// effects, and terminate active WebSockets and relayed streams immediately on expiry, revocation, or switch-off.

const (
	readOnlyDefaultTTL  = 30 * time.Minute
	readOnlyMaxTTL      = 4 * time.Hour
	readOnlyMaxActive   = 8
	readOnlyDefaultName = "Read-only verification"

	readOnlyScopeHealth = "health"
	readOnlyScopeUsage  = "usage"

	wsCloseReadOnlyExpired = websocket.StatusCode(4001)
)

type readOnlyAdminAuthKey struct{}

// readOnlyAllowedRoutes is the default-deny exact Go 1.22 ServeMux pattern allowlist for roleReadOnly
// (GET / HEAD only, with GET /api/v1/ws restricted to GET).
var readOnlyAllowedRoutes = map[string]bool{
	// A. Shell bootstrap
	"GET /api/v1/machines":   true,
	"GET /api/v1/managers":   true,
	"GET /api/v1/endpoints":  true,
	"GET /api/v1/navigation": true,
	"GET /api/v1/mutes":      true,
	"GET /api/v1/pins":       true,
	"GET /api/v1/fleet":      true,
	"GET /api/v1/ws":         true,

	// B. Core views — assistant
	"GET /api/v1/assistant":                 true,
	"GET /api/v1/assistant/capabilities":    true,
	"GET /api/v1/assistant/availability":    true,
	"GET /api/v1/assistant/attention":       true,
	"GET /api/v1/assistant/directions/tree": true,
	"GET /api/v1/assistant/directions/{id}": true,
	"GET /api/v1/assistant/leaders":         true,
	"GET /api/v1/assistant/north-star":      true,
	"GET /api/v1/assistant/tracks":          true,
	"GET /api/v1/assistant/track-kinds":     true,

	// B. Core views — todos
	"GET /api/v1/todos":                true,
	"GET /api/v1/todos/{id}":           true,
	"GET /api/v1/todos/revision":       true,
	"GET /api/v1/todos/{id}/decisions": true,
	"GET /api/v1/todos/{id}/timeline":  true,

	// B. Core views — inbox
	"GET /api/v1/inbox":         true,
	"GET /api/v1/inbox/folders": true,
	"GET /api/v1/inbox/{id}":    true,

	// B. Core views — sessions
	"GET /api/v1/sessions":                                       true,
	"GET /api/v1/sessions/{mid}/{sid}/info":                      true,
	"GET /api/v1/sessions/{mid}/{sid}/items":                     true,
	"GET /api/v1/sessions/{mid}/{sid}/items/{iid}/full":          true,
	"GET /api/v1/sessions/{mid}/{sid}/items/{iid}/image":         true,
	"GET /api/v1/sessions/{mid}/{sid}/items/{iid}/visualization": true,

	// B. Core views — agents
	"GET /api/v1/agents":                        true,
	"GET /api/v1/agents/display-settings":       true,
	"GET /api/v1/agents/sessions":               true,
	"GET /api/v1/agents/sessions/{id}":          true,
	"GET /api/v1/agents/sessions/{id}/avatar":   true,
	"GET /api/v1/agents/sessions/{id}/messages": true,
	"GET /api/v1/agents/triage":                 true,

	// B. Core views — interactions
	"GET /api/v1/interactions": true,
}

// readOnlyScopedRoutes maps sensitive opt-in read-only routes to the explicit scope required at issuance.
var readOnlyScopedRoutes = map[string]string{
	// C. Opt-in health scope
	"GET /api/v1/assistant/health/intake":         readOnlyScopeHealth,
	"GET /api/v1/assistant/health/intake/changes": readOnlyScopeHealth,
	"GET /api/v1/assistant/health/items":          readOnlyScopeHealth,

	// C. Opt-in usage scope
	"GET /api/v1/usage":                  readOnlyScopeUsage,
	"GET /api/v1/usage/days":             readOnlyScopeUsage,
	"GET /api/v1/usage/hours":            readOnlyScopeUsage,
	"GET /api/v1/usage/windows":          readOnlyScopeUsage,
	"GET /api/v1/launchers/{name}/quota": readOnlyScopeUsage,
}

type readOnlySessionCreateRequest struct {
	Label      string   `json:"label"`
	TTL        string   `json:"ttl"`
	TTLSeconds *int     `json:"ttl_seconds"`
	Scopes     []string `json:"scopes"`
}

type readOnlySessionView struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	Role      string     `json:"role"`
	Kind      string     `json:"kind"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	Active    bool       `json:"active"`
}

type readOnlySessionCreated struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	Label     string    `json:"label"`
	Role      string    `json:"role"`
	Kind      string    `json:"kind"`
	Scopes    []string  `json:"scopes"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type readOnlySessionListResponse struct {
	Sessions []readOnlySessionView `json:"sessions"`
	Active   int                   `json:"active"`
	Limit    int                   `json:"limit"`
}

type readOnlySessionRevokeResponse struct {
	OK      bool   `json:"ok"`
	ID      string `json:"id,omitempty"`
	Revoked int    `json:"revoked"`
}

type readOnlyErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	Scope string `json:"scope,omitempty"`
}

func writeReadOnlyError(w http.ResponseWriter, status int, code, message, scope string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(readOnlyErrorResponse{Error: message, Code: code, Scope: scope})
}

func (h *Hub) readOnlySessionsAllowed() bool {
	return h.cfg != nil && h.cfg.Security.AllowReadOnlySessions
}

func isReadOnlySessionsRoute(path string) bool {
	return path == "/api/v1/auth/readonly-sessions" || strings.HasPrefix(path, "/api/v1/auth/readonly-sessions/")
}

func isReadOnlyAdminAuth(ctx context.Context) bool {
	ok, _ := ctx.Value(readOnlyAdminAuthKey{}).(bool)
	return ok
}

// tryAdminReadOnlyAuth authenticates a signed-in admin session on /api/v1/auth/readonly-sessions*
// when no bearer token is provided. Out-of-scope callers or missing cookies return handled=false
// so Hub.auth falls through to its standard 401 path.
func (h *Hub) tryAdminReadOnlyAuth(w http.ResponseWriter, r *http.Request) (*http.Request, bool, bool) {
	if !h.adminInScope(r) {
		return r, false, false
	}
	s, err := h.currentAdminSession(r)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return r, true, false
	}
	if s == nil {
		return r, false, false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if code, err := adminWriteCheck(r); err != nil {
			writeErrCode(w, http.StatusForbidden, code, err)
			return r, true, false
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Persona-CSRF")), []byte(s.CSRF)) != 1 {
			writeErrCode(w, http.StatusForbidden, "csrf", errors.New("missing or stale X-Persona-CSRF"))
			return r, true, false
		}
	}
	return r.WithContext(context.WithValue(r.Context(), readOnlyAdminAuthKey{}, true)), true, true
}

// readOnlyCallerAllowed enforces the issuance/management gate: only the master token over loopback
// or a verified signed-in admin session may manage read-only verification sessions.
func (h *Hub) readOnlyCallerAllowed(w http.ResponseWriter, r *http.Request) bool {
	if isReadOnlyAdminAuth(r.Context()) {
		return true
	}
	p := principalOf(r.Context())
	if p.Role == roleFull && p.ID == "" && p.PairHash == "" && h.sourceOf(r) == sourceLoopback {
		return true
	}
	writeReadOnlyError(w, http.StatusForbidden, "readonly_admin_required",
		"read-only sessions require the master token over loopback or a signed-in admin session", "")
	return false
}

func validReadOnlyScopes(scopes []string) bool {
	for i, s := range scopes {
		if (s != readOnlyScopeHealth && s != readOnlyScopeUsage) || slices.Contains(scopes[:i], s) {
			return false
		}
	}
	return true
}

func parseReadOnlyTTL(req readOnlySessionCreateRequest) (time.Duration, error) {
	ttlStr := strings.TrimSpace(req.TTL)
	if ttlStr != "" && req.TTLSeconds != nil {
		return 0, errors.New("specify either ttl or ttl_seconds, not both")
	}
	ttl := readOnlyDefaultTTL
	switch {
	case ttlStr != "":
		d, err := time.ParseDuration(ttlStr)
		if err != nil {
			return 0, errors.New("ttl must be a valid Go duration (e.g. 30m)")
		}
		ttl = d
	case req.TTLSeconds != nil:
		ttl = time.Duration(*req.TTLSeconds) * time.Second
	}
	if ttl <= 0 || ttl > readOnlyMaxTTL {
		return 0, errors.New("ttl must be positive and at most 4h")
	}
	return ttl, nil
}

func (h *Hub) readOnlySessionRoutes(api *http.ServeMux) {
	api.HandleFunc("POST /api/v1/auth/readonly-sessions", h.createReadOnlySession)
	api.HandleFunc("GET /api/v1/auth/readonly-sessions", h.listReadOnlySessions)
	api.HandleFunc("DELETE /api/v1/auth/readonly-sessions", h.revokeAllReadOnlySessions)
	api.HandleFunc("DELETE /api/v1/auth/readonly-sessions/{id}", h.revokeReadOnlySession)
}

func (h *Hub) createReadOnlySession(w http.ResponseWriter, r *http.Request) {
	if !h.readOnlyCallerAllowed(w, r) {
		return
	}
	if !h.readOnlySessionsAllowed() {
		writeReadOnlyError(w, http.StatusForbidden, "readonly_sessions_disabled", "read-only sessions are disabled", "")
		return
	}
	if h.store == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("no database"))
		return
	}
	b, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if readErr != nil {
		writeBodyErr(w, readErr, readErr)
		return
	}
	var req readOnlySessionCreateRequest
	if len(bytes.TrimSpace(b)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeReadOnlyError(w, http.StatusBadRequest, "bad_request", "invalid request body: "+err.Error(), "")
			return
		}
		var trailing json.RawMessage
		if err := dec.Decode(&trailing); err != io.EOF {
			writeReadOnlyError(w, http.StatusBadRequest, "bad_request", "invalid trailing request body", "")
			return
		}
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = readOnlyDefaultName
	}
	if utf8.RuneCountInString(label) > 80 || strings.ContainsAny(label, "\n\r\x00") {
		writeReadOnlyError(w, http.StatusBadRequest, "invalid_label", "label must be at most 80 characters without control line breaks", "")
		return
	}
	ttl, err := parseReadOnlyTTL(req)
	if err != nil {
		writeReadOnlyError(w, http.StatusBadRequest, "invalid_ttl", err.Error(), "")
		return
	}
	scopes := slices.Clone(req.Scopes)
	if scopes == nil {
		scopes = []string{}
	}
	if !validReadOnlyScopes(scopes) {
		writeReadOnlyError(w, http.StatusBadRequest, "invalid_readonly_scopes", "scopes may contain only distinct values from [health, usage]", "")
		return
	}

	h.readonlyMu.Lock()
	defer h.readonlyMu.Unlock()

	list, err := h.store.DeviceTokens(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UTC()
	active := 0
	for _, t := range list {
		if t.Role == roleReadOnly && t.RevokedAt == nil && t.ExpiresAt != nil && now.Before(*t.ExpiresAt) {
			active++
		}
	}
	if active >= readOnlyMaxActive {
		writeReadOnlyError(w, http.StatusTooManyRequests, "readonly_session_limit", "maximum active read-only sessions reached", "")
		return
	}

	tok := "jd_" + randomHex(24)
	expiresAt := now.Add(ttl)
	dt := db.DeviceToken{
		ID:        "dev_" + randomHex(6),
		Name:      label,
		Kind:      tokenKindReadOnly,
		Role:      roleReadOnly,
		Scopes:    scopes,
		CreatedAt: now,
		ExpiresAt: &expiresAt,
	}
	if err := h.store.CreateDeviceToken(r.Context(), dt, hashToken(tok)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	h.logf("read-only session %s created for %q (expires %s, scopes=%v)", dt.ID, label, expiresAt.Format(time.RFC3339), scopes)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, readOnlySessionCreated{
		ID:        dt.ID,
		Token:     tok,
		Label:     label,
		Role:      roleReadOnly,
		Kind:      tokenKindReadOnly,
		Scopes:    scopes,
		CreatedAt: now,
		ExpiresAt: expiresAt,
	})
}

func (h *Hub) listReadOnlySessions(w http.ResponseWriter, r *http.Request) {
	if !h.readOnlyCallerAllowed(w, r) {
		return
	}
	resp := readOnlySessionListResponse{
		Sessions: []readOnlySessionView{},
		Limit:    readOnlyMaxActive,
	}
	if h.store != nil {
		list, err := h.store.DeviceTokens(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		now := time.Now().UTC()
		for _, t := range list {
			if t.Role != roleReadOnly {
				continue
			}
			scopes := slices.Clone(t.Scopes)
			if scopes == nil {
				scopes = []string{}
			}
			active := t.RevokedAt == nil && t.ExpiresAt != nil && now.Before(*t.ExpiresAt)
			if active {
				resp.Active++
			}
			resp.Sessions = append(resp.Sessions, readOnlySessionView{
				ID:        t.ID,
				Label:     t.Name,
				Role:      t.Role,
				Kind:      t.Kind,
				Scopes:    scopes,
				CreatedAt: t.CreatedAt,
				ExpiresAt: t.ExpiresAt,
				LastSeen:  t.LastSeen,
				RevokedAt: t.RevokedAt,
				Active:    active,
			})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, resp)
}

func (h *Hub) revokeAllReadOnlySessions(w http.ResponseWriter, r *http.Request) {
	if !h.readOnlyCallerAllowed(w, r) {
		return
	}
	if h.store == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("no database"))
		return
	}
	h.readonlyMu.Lock()
	defer h.readonlyMu.Unlock()

	list, err := h.store.DeviceTokens(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	revoked := 0
	for _, t := range list {
		if t.Role != roleReadOnly || t.RevokedAt != nil {
			continue
		}
		ok, err := h.store.RevokeDeviceToken(r.Context(), t.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if ok {
			h.revoked(t.ID)
			revoked++
		}
	}
	h.disconnectReadOnlyClients()
	if revoked > 0 {
		h.sendRevocations(newlyReachable(nil, h.meshView()))
	}
	h.logf("revoked %d read-only sessions", revoked)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, readOnlySessionRevokeResponse{OK: true, Revoked: revoked})
}

func (h *Hub) revokeReadOnlySession(w http.ResponseWriter, r *http.Request) {
	if !h.readOnlyCallerAllowed(w, r) {
		return
	}
	if h.store == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("no database"))
		return
	}
	id := r.PathValue("id")
	list, err := h.store.DeviceTokens(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	idx := slices.IndexFunc(list, func(t db.DeviceToken) bool { return t.ID == id })
	if idx < 0 || list[idx].Role != roleReadOnly {
		writeReadOnlyError(w, http.StatusNotFound, "not_found", "no such read-only session", "")
		return
	}
	ok, err := h.store.RevokeDeviceToken(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeReadOnlyError(w, http.StatusNotFound, "not_found", "no such read-only session", "")
		return
	}
	h.revoked(id)
	h.sendRevocations(newlyReachable(nil, h.meshView()))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, readOnlySessionRevokeResponse{OK: true, ID: id, Revoked: 1})
}

// disconnectReadOnlyClients evicts cached read-only tokens, closes all live read-only WebSockets
// with close code 4001, and drops their relayed calls.
func (h *Hub) disconnectReadOnlyClients() {
	t := &h.devtok
	t.mu.Lock()
	for hash, entry := range t.cache {
		if entry.p != nil && entry.p.Role == roleReadOnly {
			delete(t.cache, hash)
		}
	}
	t.mu.Unlock()

	h.mu.Lock()
	seen := map[*client]struct{}{}
	var gone []*client
	var ids []string
	seenID := map[string]struct{}{}
	collect := func(c *client) {
		if c.role != roleReadOnly {
			return
		}
		if _, dup := seen[c]; !dup {
			seen[c] = struct{}{}
			gone = append(gone, c)
		}
		if c.device != "" {
			if _, dup := seenID[c.device]; !dup {
				seenID[c.device] = struct{}{}
				ids = append(ids, c.device)
			}
		}
	}
	for c := range h.clients {
		collect(c)
	}
	for c := range h.wsConnections {
		collect(c)
	}
	h.mu.Unlock()

	for _, c := range gone {
		go func(c *client) {
			if c.conn != nil {
				_ = c.conn.Close(wsCloseReadOnlyExpired, "read-only session ended")
			}
			if c.cancel != nil {
				c.cancel()
			}
		}(c)
	}
	for _, id := range ids {
		h.dropRelayed(id)
	}
}

// authorizeReadOnly enforces default-deny GET/HEAD + exact ServeMux pattern matching for roleReadOnly.
func (h *Hub) authorizeReadOnly(w http.ResponseWriter, r *http.Request, p principal) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeReadOnlyError(w, http.StatusForbidden, "readonly_forbidden", "this read-only session allows GET and HEAD only", "")
		return false
	}
	if h.apiMux == nil {
		writeReadOnlyError(w, http.StatusForbidden, "readonly_forbidden", "this read-only session cannot access this route", "")
		return false
	}
	_, pattern := h.apiMux.Handler(r)
	if pattern == "GET /api/v1/ws" && r.Method != http.MethodGet {
		writeReadOnlyError(w, http.StatusForbidden, "readonly_forbidden", "this read-only session cannot access this route", "")
		return false
	}
	if readOnlyAllowedRoutes[pattern] {
		return true
	}
	if scope, ok := readOnlyScopedRoutes[pattern]; ok {
		if slices.Contains(p.Scopes, scope) {
			return true
		}
		writeReadOnlyError(w, http.StatusForbidden, "readonly_scope_required", "this read-only session lacks the required scope", scope)
		return false
	}
	writeReadOnlyError(w, http.StatusForbidden, "readonly_forbidden", "this read-only session cannot access this route", "")
	return false
}

// readOnlySocketCommand is the strict uplink WS message allowlist for roleReadOnly.
func readOnlySocketCommand(command string) bool {
	switch command {
	case "client_hello", "ping", "subscribe", "unsubscribe":
		return true
	default:
		return false
	}
}

// hiddenFromReadOnly returns true when an interaction card must not be disclosed to a read-only session
// (Private turns, SecretHandoffTool cards, or cards belonging to hidden/space conversations).
func (h *Hub) hiddenFromReadOnly(ix proto.Interaction) bool {
	if ix.Private || ix.ToolName == proto.SecretHandoffTool {
		return true
	}
	if h == nil {
		return false
	}
	return h.hiddenFromExtensions(ix)
}

// readOnlyAllowedSocketEvents is the default-deny allowlist of core WebSocket event types
// forwarded unchanged to roleReadOnly clients. Event types that require scope, space-session,
// or private-card filtering are handled explicitly in readOnlySocketFrame; every unlisted
// event type (including custom third-party extension events and non-core manager broadcasts)
// is dropped by default.
var readOnlyAllowedSocketEvents = map[string]bool{
	// Transport / protocol
	"hello":            true,
	"client_hello_ack": true,
	"pong":             true,
	"error":            true,
	"hub_restarting":   true,

	// Shell bootstrap (Group A)
	"machines":   true,
	"managers":   true,
	"endpoints":  true,
	"navigation": true,
	"fleet":      true,
	"desktops":   true,

	// Core views (Group B)
	"directions":           true,
	"todos":                true,
	"inbox":                true,
	"agents":               true,
	"agents.prefs":         true,
	"agents.triage":        true,
	"mutes":                true,
	"pins":                 true,
	"interaction_resolved": true,
}

// readOnlySocketFrame filters outbound WS frames for roleReadOnly clients using a strict
// default-deny allowlist before writing to the socket. Returning nil drops the frame completely.
func (c *client) readOnlySocketFrame(data []byte) []byte {
	var frame map[string]json.RawMessage
	if json.Unmarshal(data, &frame) != nil {
		return nil
	}
	var typ string
	if json.Unmarshal(frame["type"], &typ) != nil || typ == "" {
		return nil
	}
	if readOnlyAllowedSocketEvents[typ] {
		return data
	}
	switch typ {
	case "sessions_list":
		_, hasChanged := frame["changed"]
		_, hasSince := frame["since"]
		if !hasChanged && !hasSince {
			return data
		}
		delete(frame, "changed")
		delete(frame, "since")
		out, err := json.Marshal(frame)
		if err != nil {
			return nil
		}
		return out
	case "health_intake", "health_items", "health_strength":
		if !slices.Contains(c.scopes, readOnlyScopeHealth) {
			return nil
		}
		return data
	case "interaction":
		var ix proto.Interaction
		if json.Unmarshal(frame["interaction"], &ix) != nil {
			return nil
		}
		if c.hub.hiddenFromReadOnly(ix) || ix.Kind == proto.IxElicitation {
			return nil
		}
		return data
	case "sessions":
		var rows []proto.Session
		if json.Unmarshal(frame["sessions"], &rows) != nil {
			return nil
		}
		filtered := filterVisibleSessions(rows)
		if len(filtered) == len(rows) {
			return data
		}
		raw, err := json.Marshal(filtered)
		if err != nil {
			return nil
		}
		frame["sessions"] = raw
		out, err := json.Marshal(frame)
		if err != nil {
			return nil
		}
		return out
	case "sessions_delta":
		if rawUpsert, ok := frame["upsert"]; ok {
			var rows []proto.Session
			if json.Unmarshal(rawUpsert, &rows) != nil {
				return nil
			}
			filtered := filterVisibleSessions(rows)
			if len(filtered) != len(rows) {
				raw, err := json.Marshal(filtered)
				if err != nil {
					return nil
				}
				frame["upsert"] = raw
				out, err := json.Marshal(frame)
				if err != nil {
					return nil
				}
				return out
			}
		}
		return data
	case "revs":
		if c.hub != nil {
			var mid string
			var revs map[string]json.RawMessage
			if json.Unmarshal(frame["machine_id"], &mid) != nil || json.Unmarshal(frame["revs"], &revs) != nil || mid == "" {
				return nil
			}
			if len(revs) > 0 {
				changed := false
				for sid := range revs {
					if !c.hub.scopedPublicSession(mid, sid) {
						delete(revs, sid)
						changed = true
					}
				}
				if changed {
					if len(revs) == 0 {
						return nil
					}
					raw, err := json.Marshal(revs)
					if err != nil {
						return nil
					}
					frame["revs"] = raw
					out, err := json.Marshal(frame)
					if err != nil {
						return nil
					}
					return out
				}
			}
		}
		return data
	case "items", "delta", "live", "screen", "hook_event":
		if c.hub != nil {
			var mid, sid string
			_ = json.Unmarshal(frame["machine_id"], &mid)
			_ = json.Unmarshal(frame["session_id"], &sid)
			if mid == "" || sid == "" || !c.hub.scopedPublicSession(mid, sid) {
				return nil
			}
		}
		return data
	default:
		return nil
	}
}

func filterVisibleSessions(rows []proto.Session) []proto.Session {
	for i := range rows {
		if !spaceSession(&rows[i]) {
			continue
		}
		out := make([]proto.Session, i, len(rows)-1)
		copy(out, rows[:i])
		for j := i + 1; j < len(rows); j++ {
			if !spaceSession(&rows[j]) {
				out = append(out, rows[j])
			}
		}
		return out
	}
	return rows
}
