package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func readOnlyFakeNode(t *testing.T, baseTS time.Time) *Machine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide := rpc.New("h", h2nW)
	nodeSide := rpc.New("n", n2hW)
	nodeSide.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case "session.items":
			return map[string]any{
				"items": []proto.Item{
					{ID: "u1#0", Seq: 1, TS: baseTS, Role: "user", Kind: proto.KindText, Text: "hello"},
					{ID: "i1", Seq: 2, TS: baseTS.Add(250 * time.Millisecond), Role: "assistant", Kind: proto.KindText, Text: "reply"},
				},
				"has_more": false,
				"history":  "h1",
				"from_seq": 0,
				"to_seq":   2,
			}, nil
		case "session.item":
			return map[string]any{
				"id":   "i1",
				"seq":  2,
				"role": "assistant",
				"kind": proto.KindText,
				"text": "full item body",
			}, nil
		case "session.info":
			return map[string]any{"session_id": "s1"}, nil
		case "session.image":
			return proto.Image{Mime: "image/png", Data: "iVBORw0KGgo="}, nil
		case "session.visualization":
			return map[string]any{"html": "<div/>"}, nil
		default:
			return map[string]any{}, nil
		}
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m := newMachine(&Hub{}, MachineConfig{ID: "m1", Name: "m1"})
	m.peer = hubSide
	return m
}

// readOnlyTestStore combines in-memory device tokens, admin sessions, and device session spy counters.
type readOnlyTestStore struct {
	*adminMemStore
	mu              sync.Mutex
	tokensByHash    map[string]db.DeviceToken
	revokedIDs      map[string]bool
	insertSessionN  atomic.Int64
	touchTokenCalls atomic.Int64
}

func newReadOnlyTestStore() *readOnlyTestStore {
	return &readOnlyTestStore{
		adminMemStore: newAdminMemStore(),
		tokensByHash:  map[string]db.DeviceToken{},
		revokedIDs:    map[string]bool{},
	}
}

func (s *readOnlyTestStore) CreateDeviceToken(_ context.Context, t db.DeviceToken, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	s.tokensByHash[hash] = t
	return nil
}

func (s *readOnlyTestStore) DeviceTokenByHash(_ context.Context, hash string) (*db.DeviceToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokensByHash[hash]
	if !ok || s.revokedIDs[t.ID] {
		return nil, nil
	}
	if t.ExpiresAt != nil && !time.Now().Before(*t.ExpiresAt) {
		return nil, nil
	}
	cp := t
	return &cp, nil
}

func (s *readOnlyTestStore) DeviceTokens(context.Context) ([]db.DeviceToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.DeviceToken, 0, len(s.tokensByHash))
	for _, t := range s.tokensByHash {
		out = append(out, t)
	}
	return out, nil
}

func (s *readOnlyTestStore) RevokeDeviceToken(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for hash, t := range s.tokensByHash {
		if t.ID == id && !s.revokedIDs[id] {
			s.revokedIDs[id] = true
			now := time.Now().UTC()
			t.RevokedAt = &now
			s.tokensByHash[hash] = t
			return true, nil
		}
	}
	return false, nil
}

func (s *readOnlyTestStore) TouchDeviceToken(context.Context, string) error {
	s.touchTokenCalls.Add(1)
	return nil
}

func (s *readOnlyTestStore) InsertDeviceSession(context.Context, db.DeviceSession) (int64, error) {
	return s.insertSessionN.Add(1), nil
}

func (s *readOnlyTestStore) FinishDeviceSession(context.Context, int64, time.Time, string, int) error {
	return nil
}

func (s *readOnlyTestStore) RecordDeviceSessionHeartbeat(context.Context, int64, int, int) error {
	return nil
}

func (s *readOnlyTestStore) insertRawToken(tok string, dt db.DeviceToken) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if dt.CreatedAt.IsZero() {
		dt.CreatedAt = time.Now().UTC()
	}
	s.tokensByHash[hashToken(tok)] = dt
}

func doReadOnlyReq(t *testing.T, handler http.Handler, method, path, tok, body, remoteAddr string, headers map[string]string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var rBody io.Reader
	if body != "" {
		rBody = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rBody)
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	} else {
		req.RemoteAddr = "127.0.0.1:43210"
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func mintReadOnlySession(t *testing.T, handler http.Handler, masterTok, body string) readOnlySessionCreated {
	t.Helper()
	rec := doReadOnlyReq(t, handler, http.MethodPost, "/api/v1/auth/readonly-sessions", masterTok, body, "127.0.0.1:43210", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/auth/readonly-sessions (%s): %d %s", body, rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("POST /api/v1/auth/readonly-sessions Cache-Control = %q, want no-store", cc)
	}
	var out readOnlySessionCreated
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode created session: %v (%s)", err, rec.Body.String())
	}
	return out
}

func TestReadOnlyConfigDefaultAndEnv(t *testing.T) {
	var def Config
	if def.Security.AllowReadOnlySessions {
		t.Fatal("security.allow_readonly_sessions must default to false")
	}
	raw, err := json.Marshal(def.Security)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "allow_readonly_sessions") {
		t.Fatalf("default false should be omitted from JSON: %s", raw)
	}

	for _, tc := range []struct {
		env  string
		want bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"0", false},
		{"false", false},
		{"not-a-bool", false},
	} {
		t.Run("env_"+tc.env, func(t *testing.T) {
			t.Setenv("PERSONA_ALLOW_READONLY_SESSIONS", tc.env)
			c := &Config{}
			c.applyEnv()
			if c.Security.AllowReadOnlySessions != tc.want {
				t.Fatalf("PERSONA_ALLOW_READONLY_SESSIONS=%q: got %v, want %v", tc.env, c.Security.AllowReadOnlySessions, tc.want)
			}
		})
	}
}

func TestReadOnlyIssuanceGateLimitsAndSecrets(t *testing.T) {
	var logBuf bytes.Buffer
	h := newTestHub(t)
	h.logger = log.New(&logBuf, "", 0)
	store := newReadOnlyTestStore()
	h.store = store
	mux := h.routes()
	master := h.cfg.Token

	// 1. Default switch OFF refuses issuance even with loopback master token.
	rec := doReadOnlyReq(t, mux, http.MethodPost, "/api/v1/auth/readonly-sessions", master, `{"label":"QA"}`, "127.0.0.1:40001", nil, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "readonly_sessions_disabled") {
		t.Fatalf("issuance while disabled: %d %s", rec.Code, rec.Body.String())
	}

	// Enable switch.
	h.cfg.Security.AllowReadOnlySessions = true

	// 2. Non-loopback master token is rejected.
	lanAddr := ip4(10, 0, 0, 5) + ":40001"
	rec = doReadOnlyReq(t, mux, http.MethodPost, "/api/v1/auth/readonly-sessions", master, `{}`, lanAddr, nil, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "readonly_admin_required") {
		t.Fatalf("LAN master token issuance: %d %s", rec.Code, rec.Body.String())
	}
	rec = doReadOnlyReq(t, mux, http.MethodPost, "/api/v1/auth/readonly-sessions", master, `{}`, "203.0.113.10:40001", nil, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("public master token issuance: %d %s", rec.Code, rec.Body.String())
	}

	// 3. Paired full / answer / scoped tokens are rejected even over loopback.
	store.insertRawToken("jd_full_dev", db.DeviceToken{ID: "dev_full", Name: "Phone", Kind: "phone", Role: roleFull})
	store.insertRawToken("jd_answer_dev", db.DeviceToken{ID: "dev_watch", Name: "Watch", Kind: "watch", Role: roleAnswer})
	store.insertRawToken("jd_scoped_dev", db.DeviceToken{ID: "dev_scoped", Name: "Bot", Kind: "other", Role: roleScoped, Scopes: []string{"agents.read"}})
	for _, tok := range []string{"jd_full_dev", "jd_answer_dev", "jd_scoped_dev"} {
		for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
			rec = doReadOnlyReq(t, mux, method, "/api/v1/auth/readonly-sessions", tok, `{}`, "127.0.0.1:40001", nil, nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s with %s: %d %s", method, tok, rec.Code, rec.Body.String())
			}
		}
	}

	// 4. Signed-in Admin session (cookie + CSRF) can issue, list, and revoke.
	adminRaw := "adm_test_session_cookie"
	adminCSRF := "csrf_test_token_123"
	if err := store.CreateAdminSession(context.Background(), db.AdminSession{
		IDHash:    hashToken(adminRaw),
		CSRF:      adminCSRF,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	adminCookies := []*http.Cookie{{Name: "persona_admin", Value: adminRaw}}
	// Missing CSRF on POST -> 403.
	rec = doReadOnlyReq(t, mux, http.MethodPost, "/api/v1/auth/readonly-sessions", "", `{"label":"AdminCheck"}`, "127.0.0.1:40001",
		map[string]string{"Sec-Fetch-Site": "same-origin"}, adminCookies)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"csrf"`) {
		t.Fatalf("admin POST without CSRF: %d %s", rec.Code, rec.Body.String())
	}
	// Valid admin session + CSRF -> 200.
	rec = doReadOnlyReq(t, mux, http.MethodPost, "/api/v1/auth/readonly-sessions", "", `{"label":"AdminCheck","ttl":"10m","scopes":["usage"]}`, "127.0.0.1:40001",
		map[string]string{"Sec-Fetch-Site": "same-origin", "X-Persona-CSRF": adminCSRF}, adminCookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin POST with CSRF: %d %s", rec.Code, rec.Body.String())
	}
	var adminIssued readOnlySessionCreated
	if err := json.Unmarshal(rec.Body.Bytes(), &adminIssued); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(adminIssued.Token, "jd_") || adminIssued.Role != roleReadOnly || adminIssued.Kind != tokenKindReadOnly || !slices.Equal(adminIssued.Scopes, []string{"usage"}) {
		t.Fatalf("unexpected admin issued session: %+v", adminIssued)
	}
	// Admin GET list succeeds without CSRF and never exposes plaintext token.
	rec = doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/auth/readonly-sessions", "", "", "127.0.0.1:40001", nil, adminCookies)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), adminIssued.Token) || !strings.Contains(rec.Body.String(), adminIssued.ID) {
		t.Fatalf("admin GET list: %d %s", rec.Code, rec.Body.String())
	}
	// Logs never contain plaintext token either.
	if strings.Contains(logBuf.String(), adminIssued.Token) {
		t.Fatalf("hub log leaked plaintext read-only token: %s", logBuf.String())
	}
	// Admin DELETE revokes it cleanly.
	rec = doReadOnlyReq(t, mux, http.MethodDelete, "/api/v1/auth/readonly-sessions/"+adminIssued.ID, "", "", "127.0.0.1:40001",
		map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-origin", "X-Persona-CSRF": adminCSRF}, adminCookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin DELETE: %d %s", rec.Code, rec.Body.String())
	}

	// 5. Input validation (TTL, scopes, label, unknown fields).
	for _, bad := range []struct {
		body string
		code string
	}{
		{`{"ttl":"0s"}`, "invalid_ttl"},
		{`{"ttl":"-5m"}`, "invalid_ttl"},
		{`{"ttl":"4h1s"}`, "invalid_ttl"},
		{`{"ttl":"bogus"}`, "invalid_ttl"},
		{`{"ttl_seconds":0}`, "invalid_ttl"},
		{`{"ttl_seconds":14401}`, "invalid_ttl"},
		{`{"ttl":"5m","ttl_seconds":300}`, "invalid_ttl"},
		{`{"scopes":["admin"]}`, "invalid_readonly_scopes"},
		{`{"scopes":["health","health"]}`, "invalid_readonly_scopes"},
		{`{"scopes":[""]}`, "invalid_readonly_scopes"},
		{`{"label":"bad\nline"}`, "invalid_label"},
		{fmt.Sprintf(`{"label":%q}`, strings.Repeat("a", 81)), "invalid_label"},
		{`{"unknown_field":true}`, "bad_request"},
	} {
		rec = doReadOnlyReq(t, mux, http.MethodPost, "/api/v1/auth/readonly-sessions", master, bad.body, "127.0.0.1:40001", nil, nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), bad.code) {
			t.Fatalf("bad body %s: got %d %s, want 400 %s", bad.body, rec.Code, rec.Body.String(), bad.code)
		}
	}

	// 6. Default TTL is 30m; active cap is 8; expired/revoked sessions do not consume cap.
	var issued []readOnlySessionCreated
	for i := range readOnlyMaxActive {
		body := fmt.Sprintf(`{"label":"verify-%d"}`, i)
		if i == 1 {
			body = `{"label":"verify-1","ttl_seconds":120}`
		}
		created := mintReadOnlySession(t, mux, master, body)
		if i == 0 {
			d := created.ExpiresAt.Sub(created.CreatedAt)
			if d != readOnlyDefaultTTL {
				t.Fatalf("default ttl = %v, want %v", d, readOnlyDefaultTTL)
			}
		}
		issued = append(issued, created)
	}
	// 9th active session is rejected with 429.
	rec = doReadOnlyReq(t, mux, http.MethodPost, "/api/v1/auth/readonly-sessions", master, `{"label":"overflow"}`, "127.0.0.1:40001", nil, nil)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "readonly_session_limit") {
		t.Fatalf("9th active session: %d %s", rec.Code, rec.Body.String())
	}
	// Revoking one frees a slot.
	rec = doReadOnlyReq(t, mux, http.MethodDelete, "/api/v1/auth/readonly-sessions/"+issued[0].ID, master, "", "127.0.0.1:40001", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke slot: %d %s", rec.Code, rec.Body.String())
	}
	replacement := mintReadOnlySession(t, mux, master, `{"label":"replacement"}`)
	if replacement.ID == "" || !strings.HasPrefix(replacement.Token, "jd_") {
		t.Fatalf("replacement issuance failed: %+v", replacement)
	}

	// 7. roleReadOnly token is forbidden on ALL /api/v1/auth/* routes.
	roTok := replacement.Token
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/auth/tokens"},
		{http.MethodPost, "/api/v1/auth/tokens"},
		{http.MethodDelete, "/api/v1/auth/tokens/dev_full"},
		{http.MethodPost, "/api/v1/auth/pair-codes"},
		{http.MethodGet, "/api/v1/auth/readonly-sessions"},
		{http.MethodPost, "/api/v1/auth/readonly-sessions"},
		{http.MethodDelete, "/api/v1/auth/readonly-sessions"},
		{http.MethodDelete, "/api/v1/auth/readonly-sessions/" + replacement.ID},
		{http.MethodPost, "/api/v1/auth/browser-login/approve"},
	} {
		rec = doReadOnlyReq(t, mux, tc.method, tc.path, roTok, `{}`, "127.0.0.1:40001", nil, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("roleReadOnly %s %s: got %d %s, want 403", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestReadOnlyRouteMatrixScopesAndRelayHeaders(t *testing.T) {
	h := newTestHub(t)
	h.cfg.Security.AllowReadOnlySessions = true
	h.agentWork.everUp = true
	store := newReadOnlyTestStore()
	h.store = store
	h.bus.idents = &memIdents{}
	m := readOnlyFakeNode(t, time.Now().UTC())
	m.hub = h
	m.sessions = []proto.Session{{MachineID: "m1", SessionID: "s1", Title: "Public", Status: proto.StatusIdle, Cwd: "/work"}}
	h.machines = []*Machine{m}
	h.byID["m1"] = m
	// Advertise a third-party extension prefix and a migratable prefix to verify h.auth blocks them before extensionAPIs.
	h.keepExtReport("extmgr", &extwire.Report{
		APIs: []extwire.APIPrefix{
			{Prefix: "acme-ext"},
			{Prefix: "lark"},
		},
	})
	mux := h.routes()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Connect a fake assistant manager on the bus that records relayed HTTP headers.
	var relayMu sync.Mutex
	var lastRelayed map[string][]string
	connectGuardManager(t, h, srv.URL, "assistant", func(method string, raw json.RawMessage) (any, error) {
		if method == assistantwire.MethodHTTP {
			var req assistantwire.HTTPRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, err
			}
			relayMu.Lock()
			lastRelayed = req.Header
			relayMu.Unlock()
			return assistantwire.HTTPResponse{Status: http.StatusOK, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"ok":true}`)}, nil
		}
		return nil, fmt.Errorf("test manager does not open stream for %s", method)
	})
	connectGuardManager(t, h, srv.URL, "devices", func(method string, raw json.RawMessage) (any, error) {
		if method == assistantwire.MethodHTTP {
			return assistantwire.HTTPResponse{Status: http.StatusOK, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"ok":true}`)}, nil
		}
		return nil, fmt.Errorf("test manager does not open stream for %s", method)
	})

	// 1. Every pattern in readOnlyAllowedRoutes and readOnlyScopedRoutes must match a real route registered on h.apiMux.
	samplePathForPattern := func(pattern string) string {
		path := strings.TrimPrefix(pattern, "GET ")
		r := strings.NewReplacer(
			"{mid}", "m1",
			"{sid}", "s1",
			"{iid}", "i1",
			"{id}", "id1",
			"{name}", "claude",
		)
		return r.Replace(path)
	}
	for pattern := range readOnlyAllowedRoutes {
		req := httptest.NewRequest(http.MethodGet, samplePathForPattern(pattern), nil)
		_, matched := h.apiMux.Handler(req)
		if matched != pattern {
			t.Fatalf("allowlist pattern %q matched %q on h.apiMux", pattern, matched)
		}
	}
	for pattern := range readOnlyScopedRoutes {
		req := httptest.NewRequest(http.MethodGet, samplePathForPattern(pattern), nil)
		_, matched := h.apiMux.Handler(req)
		if matched != pattern {
			t.Fatalf("scoped allowlist pattern %q matched %q on h.apiMux", pattern, matched)
		}
	}

	baseRO := mintReadOnlySession(t, mux, h.cfg.Token, `{"label":"base-ro"}`)
	healthRO := mintReadOnlySession(t, mux, h.cfg.Token, `{"label":"health-ro","scopes":["health"]}`)
	usageRO := mintReadOnlySession(t, mux, h.cfg.Token, `{"label":"usage-ro","scopes":["usage"]}`)

	// 2. All Group A & Group B routes pass h.auth on GET (and HEAD except /api/v1/ws).
	for pattern := range readOnlyAllowedRoutes {
		path := samplePathForPattern(pattern)
		if pattern == "GET /api/v1/ws" {
			// HEAD on /api/v1/ws must be rejected with 403.
			rec := doReadOnlyReq(t, mux, http.MethodHead, path, baseRO.Token, "", "127.0.0.1:41000", nil, nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("HEAD /api/v1/ws: got %d %s, want 403", rec.Code, rec.Body.String())
			}
			continue
		}
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := doReadOnlyReq(t, mux, method, path, baseRO.Token, "", "127.0.0.1:41000", nil, nil)
			if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
				t.Fatalf("allowed %s %s (pattern %s) blocked: %d %s", method, path, pattern, rec.Code, rec.Body.String())
			}
		}
	}

	// 3. Non-GET/HEAD requests on allowed prefixes are strictly rejected with 403.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/sessions"},
		{http.MethodDelete, "/api/v1/sessions/m1/s1"},
		{http.MethodPut, "/api/v1/mutes"},
		{http.MethodPut, "/api/v1/pins"},
		{http.MethodPost, "/api/v1/todos"},
		{http.MethodPatch, "/api/v1/todos/t1"},
		{http.MethodPost, "/api/v1/interactions/ix1/resolve"},
		{http.MethodPost, "/api/v1/assistant/messages"},
		{http.MethodPost, "/api/v1/notify"},
		{http.MethodPost, "/api/v1/mail/messages/acc1/1/move"},
	} {
		rec := doReadOnlyReq(t, mux, tc.method, tc.path, baseRO.Token, `{}`, "127.0.0.1:41000", nil, nil)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "readonly_forbidden") {
			t.Fatalf("write %s %s: got %d %s, want 403 readonly_forbidden", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}

	// 4. All high-risk GET routes + third-party extension prefixes are denied.
	highRiskPaths := []string{
		"/api/v1/machines/m1/files/list",
		"/api/v1/machines/m1/files/download",
		"/api/v1/machines/m1/files/preview",
		"/api/v1/machines/m1/files/roots",
		"/api/v1/machines/m1/files/tail",
		"/api/v1/machines/m1/file",
		"/api/v1/machines/m1/file/download",
		"/api/v1/machines/m1/fs",
		"/api/v1/machines/m1/fs/find",
		"/api/v1/machines/m1/git/diff",
		"/api/v1/machines/m1/git/status",
		"/api/v1/machines/m1/git/blob",
		"/api/v1/machines/m1/browser/cookies",
		"/api/v1/machines/m1/clipboard",
		"/api/v1/machines/m1/computer/apps",
		"/api/v1/imessage/chats",
		"/api/v1/mail/accounts",
		"/api/v1/recordings/r1/audio",
		"/api/v1/machines/m1/ports/8080/connect",
		"/api/v1/machines/m1/preview/connect",
		"/api/v1/machines/m1/screen/video/connect",
		"/api/v1/auth/tokens",
		"/api/v1/connectors/github/auth",
		"/api/v1/extensions",
		"/api/v1/extensions/acme/-/status",
		"/api/v1/hub/recovery",
		"/api/v1/model-config",
		"/api/v1/model-config/models",
		"/api/v1/acme-ext/data",
		"/api/v1/lark",
		"/api/v1/assistant/directions/offers",
		"/api/v1/inbox/done",
		"/api/v1/todos/places",
	}
	for _, path := range highRiskPaths {
		rec := doReadOnlyReq(t, mux, http.MethodGet, path, baseRO.Token, "", "127.0.0.1:41000", nil, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("high-risk GET %s: got %d %s, want 403", path, rec.Code, rec.Body.String())
		}
	}
	// Protected admin routes never open to a read-only bearer token; public /admin/state reports logged_in=false.
	for _, path := range []string{"/api/v1/admin/overview", "/api/v1/admin/devices", "/api/v1/admin/network"} {
		rec := doReadOnlyReq(t, mux, http.MethodGet, path, baseRO.Token, "", "127.0.0.1:41000", nil, nil)
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Fatalf("admin GET %s with readonly token: got %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/admin/state", baseRO.Token, "", "127.0.0.1:41000", nil, nil); !strings.Contains(rec.Body.String(), `"logged_in":false`) {
		t.Fatalf("GET /api/v1/admin/state with readonly token: %d %s", rec.Code, rec.Body.String())
	}

	// 5. Opt-in health and usage scopes are denied by default and granted strictly per scope.
	healthPaths := []string{
		"/api/v1/assistant/health/intake",
		"/api/v1/assistant/health/intake/changes",
		"/api/v1/assistant/health/items",
	}
	usagePaths := []string{
		"/api/v1/usage",
		"/api/v1/usage/days",
		"/api/v1/usage/hours",
		"/api/v1/usage/windows",
		"/api/v1/launchers/claude/quota",
	}
	for _, path := range healthPaths {
		if rec := doReadOnlyReq(t, mux, http.MethodGet, path, baseRO.Token, "", "127.0.0.1:41000", nil, nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"scope":"health"`) {
			t.Fatalf("baseRO %s: got %d %s, want 403 health scope", path, rec.Code, rec.Body.String())
		}
		if rec := doReadOnlyReq(t, mux, http.MethodGet, path, usageRO.Token, "", "127.0.0.1:41000", nil, nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"scope":"health"`) {
			t.Fatalf("usageRO %s: got %d %s, want 403 health scope", path, rec.Code, rec.Body.String())
		}
		if rec := doReadOnlyReq(t, mux, http.MethodGet, path, healthRO.Token, "", "127.0.0.1:41000", nil, nil); rec.Code != http.StatusOK {
			t.Fatalf("healthRO %s: got %d %s, want 200", path, rec.Code, rec.Body.String())
		}
	}
	for _, path := range usagePaths {
		if rec := doReadOnlyReq(t, mux, http.MethodGet, path, baseRO.Token, "", "127.0.0.1:41000", nil, nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"scope":"usage"`) {
			t.Fatalf("baseRO %s: got %d %s, want 403 usage scope", path, rec.Code, rec.Body.String())
		}
		if rec := doReadOnlyReq(t, mux, http.MethodGet, path, healthRO.Token, "", "127.0.0.1:41000", nil, nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"scope":"usage"`) {
			t.Fatalf("healthRO %s: got %d %s, want 403 usage scope", path, rec.Code, rec.Body.String())
		}
		if rec := doReadOnlyReq(t, mux, http.MethodGet, path, usageRO.Token, "", "127.0.0.1:41000", nil, nil); rec.Code != http.StatusOK {
			t.Fatalf("usageRO %s: got %d %s, want 200", path, rec.Code, rec.Body.String())
		}
	}

	// 6. Relayed headers to manager carry X-Persona-Role: readonly and never AdminHeader / PersonHeader / ExtensionHeader / ownerExtension.
	rec := doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/todos", baseRO.Token, "", "127.0.0.1:41000", map[string]string{
		assistantwire.AdminHeader:     "1",
		assistantwire.PersonHeader:    "1",
		assistantwire.ExtensionHeader: "spoofed",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("relayed GET /api/v1/todos: %d %s", rec.Code, rec.Body.String())
	}
	relayMu.Lock()
	gotHeaders := lastRelayed
	relayMu.Unlock()
	if got := gotHeaders[assistantwire.RoleHeader]; !slices.Equal(got, []string{roleReadOnly}) {
		t.Fatalf("relayed RoleHeader = %q, want [%q]", got, roleReadOnly)
	}
	for _, forbidden := range []string{assistantwire.AdminHeader, assistantwire.PersonHeader, assistantwire.ExtensionHeader} {
		if v := gotHeaders[forbidden]; len(v) > 0 {
			t.Fatalf("relayed request carried forbidden header %s=%q", forbidden, v)
		}
	}
	roCtxReq := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	roCtxReq = roCtxReq.WithContext(context.WithValue(roCtxReq.Context(), principalKey{}, principal{ID: baseRO.ID, Role: roleReadOnly, ExpiresAt: baseRO.ExpiresAt}))
	if h.ownerExtension(roCtxReq) {
		t.Fatal("roleReadOnly must never satisfy ownerExtension")
	}
	if !h.spacesHidden(roCtxReq) {
		t.Fatal("roleReadOnly must satisfy spacesHidden")
	}
}

func TestReadOnlySuppressesFiveSideEffectsAndFiltersData(t *testing.T) {
	h := newTestHub(t)
	h.cfg.Security.AllowReadOnlySessions = true
	store := newReadOnlyTestStore()
	h.store = store
	h.bus.idents = &memIdents{}
	h.userLocale.Store(i18n.En)

	t0 := time.Now().Add(-500 * time.Millisecond)
	m := readOnlyFakeNode(t, t0)
	m.hub = h
	m.sessions = []proto.Session{
		{MachineID: "m1", SessionID: "s1", Title: "Public Chat", Status: proto.StatusIdle, Cwd: "/work/project"},
		{MachineID: "m1", SessionID: "sh", Title: "Health Private Chat", Status: proto.StatusIdle, Cwd: "/home/me/.persona/health", Space: "health"},
	}
	h.machines = []*Machine{m}
	h.byID["m1"] = m

	h.reported.mu.Lock()
	h.reported.interactions = map[string]proto.Interaction{
		"ix_ok":      {ID: "ix_ok", MachineID: "m1", SessionID: "s1", Cwd: "/work/project", Kind: proto.IxQuestion, Summary: "OK-CARD"},
		"ix_private": {ID: "ix_private", MachineID: "m1", SessionID: "s1", Cwd: "/work/project", Kind: proto.IxQuestion, Private: true, Summary: "PRIVATE-CARD"},
		"ix_secret":  {ID: "ix_secret", MachineID: "m1", SessionID: "s1", Cwd: "/work/project", Kind: proto.IxPermission, ToolName: proto.SecretHandoffTool, Summary: "SECRET-CARD"},
		"ix_space":   {ID: "ix_space", MachineID: "m1", SessionID: "sh", Cwd: "/home/me/.persona/health", Kind: proto.IxQuestion, Summary: "SPACE-CARD"},
		"ix_elicit":  {ID: "ix_elicit", MachineID: "m1", SessionID: "s1", Cwd: "/work/project", Kind: proto.IxElicitation, Summary: "ELICIT-CARD"},
	}
	h.reported.mu.Unlock()

	mux := h.routes()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var sessionItemEvents atomic.Int64
	var fleetClientEvents atomic.Int64
	busBarrier := make(chan struct{}, 4)
	mgr := connectGuardManager(t, h, srv.URL, "assistant", func(string, json.RawMessage) (any, error) {
		return map[string]any{}, nil
	})
	mgr.mu.Lock()
	mgr.onEvent = func(event string, _ json.RawMessage) {
		switch event {
		case assistantwire.EventSessionItems:
			sessionItemEvents.Add(1)
		case assistantwire.EventClients:
			fleetClientEvents.Add(1)
		case "test.barrier":
			busBarrier <- struct{}{}
		}
	}
	mgr.mu.Unlock()
	flushAssistantBus := func() {
		t.Helper()
		if err := h.notifyBus("assistant", "test.barrier", nil); err != nil {
			t.Fatalf("notifyBus barrier: %v", err)
		}
		select {
		case <-busBarrier:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for assistant bus barrier")
		}
	}

	ro := mintReadOnlySession(t, mux, h.cfg.Token, `{"label":"side-effect-check"}`)
	store.insertRawToken("jd_full_compare", db.DeviceToken{ID: "dev_full_compare", Name: "Full", Kind: "web", Role: roleFull})

	// Side effect ①: items/{iid}/full does NOT trigger EventSessionItems for roleReadOnly, but DOES for roleFull.
	rec := doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/sessions/m1/s1/items/i1/full", ro.Token, "", "127.0.0.1:42000", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("readonly items/full: %d %s", rec.Code, rec.Body.String())
	}
	flushAssistantBus()
	if got := sessionItemEvents.Load(); got != 0 {
		t.Fatalf("readonly items/full triggered %d EventSessionItems notifications, want 0", got)
	}
	rec = doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/sessions/m1/s1/items/i1/full", "jd_full_compare", "", "127.0.0.1:42000", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("full items/full: %d %s", rec.Code, rec.Body.String())
	}
	eventually(t, 2*time.Second, "full items/full notifies EventSessionItems", func() bool {
		return sessionItemEvents.Load() == 1
	})

	// Side effect ②: GET /items forces allowBind=false for roleReadOnly so pending TTFT stays unbound until a real client reads it.
	h.ttft.arm("m1", "s1", t0)
	if _, ok := h.ttft.firstToken("m1", "s1", t0.Add(220*time.Millisecond)); !ok {
		t.Fatal("failed to arm pending ttft")
	}
	rec = doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/sessions/m1/s1/items", ro.Token, "", "127.0.0.1:42000", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("readonly items: %d %s", rec.Code, rec.Body.String())
	}
	var roItems struct {
		Items []proto.Item `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &roItems); err != nil {
		t.Fatal(err)
	}
	for _, it := range roItems.Items {
		if it.TTFTMS != nil {
			t.Fatalf("readonly items unexpectedly bound pending TTFT: %+v", it)
		}
	}
	// A full-token read immediately afterwards can still bind the pending TTFT.
	rec = doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/sessions/m1/s1/items", "jd_full_compare", "", "127.0.0.1:42000", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("full items: %d %s", rec.Code, rec.Body.String())
	}
	var fullItems struct {
		Items []proto.Item `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &fullItems); err != nil {
		t.Fatal(err)
	}
	bound := false
	for _, it := range fullItems.Items {
		if it.TTFTMS != nil && *it.TTFTMS == 220 {
			bound = true
		}
	}
	if !bound {
		t.Fatalf("pending TTFT was consumed by readonly read; full items = %+v", fullItems.Items)
	}

	// HTTP space hiding & interaction filtering (same rules as WS).
	rec = doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/sessions", ro.Token, "", "127.0.0.1:42000", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Public Chat") || strings.Contains(rec.Body.String(), "Health Private Chat") {
		t.Fatalf("readonly GET /api/v1/sessions leaked space session: %d %s", rec.Code, rec.Body.String())
	}
	rec = doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/sessions/m1/sh/items", ro.Token, "", "127.0.0.1:42000", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("readonly GET space session items: got %d %s, want 404", rec.Code, rec.Body.String())
	}
	rec = doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/interactions", ro.Token, "", "127.0.0.1:42000", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "OK-CARD") ||
		strings.Contains(rec.Body.String(), "PRIVATE-CARD") ||
		strings.Contains(rec.Body.String(), "SECRET-CARD") ||
		strings.Contains(rec.Body.String(), "SPACE-CARD") {
		t.Fatalf("readonly GET /api/v1/interactions filtering mismatch: %d %s", rec.Code, rec.Body.String())
	}

	// Side effect ③ & ④ & ⑤ over real WebSocket + direct frame checks.
	// Confirm client_hello with ws_notify neither creates nor replays a notify ring entry for roleReadOnly.
	fleetBefore := fleetClientEvents.Load()

	wsCtx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws?token=" + ro.Token
	conn, _, err := websocket.Dial(wsCtx, wsURL, nil)
	if err != nil {
		t.Fatalf("readonly ws dial: %v", err)
	}
	defer conn.CloseNow()

	// First frame must be strongly-typed hello with read_only=true and expires_at.
	_, helloRaw, err := conn.Read(wsCtx)
	if err != nil {
		t.Fatalf("read ws hello: %v", err)
	}
	var helloMsg struct {
		Type      string `json:"type"`
		ReadOnly  bool   `json:"read_only"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(helloRaw, &helloMsg); err != nil || helloMsg.Type != "hello" || !helloMsg.ReadOnly || helloMsg.ExpiresAt == "" {
		t.Fatalf("readonly ws hello = %s (err=%v)", helloRaw, err)
	}
	if _, err := time.Parse(time.RFC3339, helloMsg.ExpiresAt); err != nil {
		t.Fatalf("readonly ws hello expires_at not RFC3339: %q (%v)", helloMsg.ExpiresAt, err)
	}

	// Contrast: full WS hello omits read_only and expires_at.
	fullConn, _, err := websocket.Dial(wsCtx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/ws?token=jd_full_compare", nil)
	if err != nil {
		t.Fatalf("full ws dial: %v", err)
	}
	_, fullHelloRaw, err := fullConn.Read(wsCtx)
	fullConn.CloseNow()
	if err != nil {
		t.Fatalf("read full ws hello: %v", err)
	}
	if strings.Contains(string(fullHelloRaw), `"read_only"`) || strings.Contains(string(fullHelloRaw), `"expires_at"`) {
		t.Fatalf("full ws hello unexpectedly carried read_only/expires_at: %s", fullHelloRaw)
	}
	// Side effect ④: full WS recorded 1 InsertDeviceSession; readonly WS recorded 0 (total stays 1).
	eventually(t, time.Second, "full ws recorded exactly one device session", func() bool {
		return store.insertSessionN.Load() == 1
	})
	// Wait for fullConn's async dropClient/fleetClientsChanged teardown to settle before checking readonly client_hello.
	eventually(t, time.Second, "full ws dropped from h.clients", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.clients) == 1
	})
	eventually(t, time.Second, "full ws fleetClientsChanged settled", func() bool {
		return fleetClientEvents.Load() >= fleetBefore+2
	})
	fleetBeforeHello := fleetClientEvents.Load()

	// Send client_hello on readonly WS with locale/app/ws_notify/elicitation features; verify side effect ③ is skipped.
	if err := conn.Write(wsCtx, websocket.MessageText, []byte(`{"type":"client_hello","locale":"ja","platform":"web","version":"9.9.9","features":["ws_notify","elicitation"]}`)); err != nil {
		t.Fatal(err)
	}
	// Send forbidden uplink commands; none may mutate state or trigger replies.
	for _, forbidden := range []string{
		`{"type":"seen","machine_id":"m1","session_id":"s1"}`,
		`{"type":"interaction_seen","interaction_id":"ix_ok"}`,
		`{"type":"terminal_subscribe","machine_id":"m1","session_id":"s1"}`,
		`{"type":"herdr_subscribe","machine_id":"m1"}`,
		`{"type":"fs_watch","machine_id":"m1","path":"/tmp"}`,
		`{"type":"register_phone","device_id":"evil-phone","tools":[{"name":"phone_x"}]}`,
		`{"type":"register_desktop","device_id":"evil-desktop"}`,
		`{"type":"tool_result","call_id":"c1"}`,
		`{"type":"extension_ui_stream","request_id":"r1"}`,
		`{"type":"subscribe","machine_id":"m1","session_id":"sh"}`, // hidden space session
	} {
		if err := conn.Write(wsCtx, websocket.MessageText, []byte(forbidden)); err != nil {
			t.Fatal(err)
		}
	}
	// Allowed uplink: subscribe public session + ping -> pong acts as synchronization barrier.
	if err := conn.Write(wsCtx, websocket.MessageText, []byte(`{"type":"subscribe","machine_id":"m1","session_id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(wsCtx, websocket.MessageText, []byte(`{"type":"ping","id":"sync-1"}`)); err != nil {
		t.Fatal(err)
	}

	sawAck, sawPong, sawOKCard := false, false, false
	for !sawPong || !sawAck {
		_, raw, err := conn.Read(wsCtx)
		if err != nil {
			t.Fatalf("read ws frame before pong: %v", err)
		}
		var env struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if env.Type == "notify" || env.Type == "tool_call" || env.Type == "extension_ui_stream" {
			t.Fatalf("readonly ws received forbidden frame: %s", raw)
		}
		if strings.Contains(string(raw), "Health Private Chat") || strings.Contains(string(raw), "PRIVATE-CARD") ||
			strings.Contains(string(raw), "SECRET-CARD") || strings.Contains(string(raw), "SPACE-CARD") ||
			strings.Contains(string(raw), "ELICIT-CARD") {
			t.Fatalf("readonly ws leaked hidden session or card: %s", raw)
		}
		if env.Type == "interaction" && strings.Contains(string(raw), "OK-CARD") {
			sawOKCard = true
		}
		if env.Type == "client_hello_ack" {
			sawAck = true
		}
		if env.Type == "pong" && env.ID == "sync-1" {
			sawPong = true
		}
	}
	if !sawAck || !sawPong || !sawOKCard {
		t.Fatalf("ws barrier incomplete: ack=%v pong=%v okCard=%v", sawAck, sawPong, sawOKCard)
	}

	// Verify side effect ③ invariants after client_hello + forbidden uplinks.
	if got := h.userLang(); got != i18n.En {
		t.Fatalf("readonly client_hello mutated hub userLocale to %q", got)
	}
	h.apps.mu.Lock()
	_, recordedApp := h.apps.byCred[ro.ID]
	h.apps.mu.Unlock()
	if recordedApp {
		t.Fatal("readonly client_hello recorded client app metadata")
	}
	h.mu.Lock()
	_, hasRing := h.rings.devices[ro.ID]
	phoneToolsN := len(h.phoneTools)
	var roClient *client
	for c := range h.clients {
		if c.device == ro.ID {
			roClient = c
		}
	}
	h.mu.Unlock()
	if hasRing {
		t.Fatal("readonly client_hello touched notify ring state")
	}
	if phoneToolsN != 0 {
		t.Fatalf("readonly register_phone registered %d phone tools", phoneToolsN)
	}
	if h.hasElicitationClient() {
		t.Fatal("readonly client with elicitation feature must not count as an elicitation client")
	}
	if roClient == nil {
		t.Fatal("readonly client missing from h.clients")
	}
	roClient.mu.Lock()
	subPublic := roClient.subs[[2]string{"m1", "s1"}]
	subSpace := roClient.subs[[2]string{"m1", "sh"}]
	roClient.mu.Unlock()
	if !subPublic || subSpace {
		t.Fatalf("readonly subs: public=%v (want true), space=%v (want false)", subPublic, subSpace)
	}
	flushAssistantBus()
	if fleetClientEvents.Load() != fleetBeforeHello {
		t.Fatalf("readonly client_hello fired fleetClientsChanged: before=%d after=%d", fleetBeforeHello, fleetClientEvents.Load())
	}

	// Verify unit-level downlink filtering on readOnlySocketFrame (all blocked types, scopes, sessions, revs).
	noScopeClient := &client{hub: h, role: roleReadOnly}
	healthScopeClient := &client{hub: h, role: roleReadOnly, scopes: []string{readOnlyScopeHealth}}
	for _, dropped := range []string{
		`{"type":"tool_call","call_id":"1"}`,
		`{"type":"notify","title":"x"}`,
		`{"type":"extension_ui_stream","request_id":"1"}`,
		`{"type":"terminal","data":"abc"}`,
		`{"type":"herdr_state","machine_id":"m1"}`,
		`{"type":"fs_changed","path":"/tmp"}`,
		`{"type":"lock_test","id":"1"}`,
		`{"type":"file_transfer","id":"1"}`,
		`{"type":"held_start","id":"1"}`,
		`{"type":"health_intake","date":"2026-11-26"}`,
		`{"type":"health_items","items":[]}`,
		`{"type":"health_strength","data":{}}`,
		`{"type":"interaction","interaction":{"id":"p","machine_id":"m1","session_id":"s1","private":true}}`,
		`{"type":"interaction","interaction":{"id":"s","machine_id":"m1","session_id":"s1","tool_name":"` + proto.SecretHandoffTool + `"}}`,
		`{"type":"interaction","interaction":{"id":"e","machine_id":"m1","session_id":"s1","kind":"elicitation"}}`,
		`{"type":"interaction","interaction":{"id":"h","machine_id":"m1","session_id":"sh"}}`,
		`{"type":"items","machine_id":"m1","session_id":"sh","items":[]}`,
		`{"type":"delta","machine_id":"m1","session_id":"sh","text":"secret"}`,
		`{"type":"acme_custom_event","payload":"secret"}`,
		`{"type":"mail_accounts","accounts":[]}`,
		`{"type":"messaging","text":"dm"}`,
		`{"type":"lark","feed_ids":["f1"]}`,
		`{"type":"extension_auth","id":"ext1"}`,
		`{"type":"model_profile","profile":"x"}`,
		`{"type":"interaction_reanswered","id":"ix_ok","resolution":{"text":"secret"}}`,
		`{"type":"items","items":[]}`,
	} {
		if out := noScopeClient.readOnlySocketFrame([]byte(dropped)); out != nil {
			t.Fatalf("readOnlySocketFrame did not drop %s -> %s", dropped, out)
		}
	}
	// Allowlisted core view events pass unchanged, and sessions_list strips any changed/since keys.
	for _, allowed := range []string{
		`{"type":"todos"}`,
		`{"type":"directions"}`,
		`{"type":"inbox"}`,
		`{"type":"machines","machines":[]}`,
		`{"type":"navigation","pages":[]}`,
		`{"type":"interaction_resolved","id":"ix_ok"}`,
	} {
		if out := noScopeClient.readOnlySocketFrame([]byte(allowed)); out == nil {
			t.Fatalf("readOnlySocketFrame unexpectedly dropped core event %s", allowed)
		}
	}
	if out := noScopeClient.readOnlySocketFrame([]byte(`{"type":"sessions_list","revision":2,"since":1,"changed":["m1/sh"]}`)); out == nil || strings.Contains(string(out), "changed") || strings.Contains(string(out), "since") {
		t.Fatalf("sessions_list defense-in-depth strip = %s", out)
	}
	// Health events pass ONLY when health scope is granted.
	if out := healthScopeClient.readOnlySocketFrame([]byte(`{"type":"health_intake","date":"2026-11-26"}`)); out == nil {
		t.Fatal("health-scoped readonly client dropped health_intake")
	}
	// revs filters out space session sh and preserves s1.
	revsOut := noScopeClient.readOnlySocketFrame([]byte(`{"type":"revs","machine_id":"m1","revs":{"s1":5,"sh":9}}`))
	if revsOut == nil || !strings.Contains(string(revsOut), `"s1":5`) || strings.Contains(string(revsOut), `"sh"`) {
		t.Fatalf("revs filter = %s", revsOut)
	}
	// sessions_list hint sends plain (never detailed) to roleReadOnly even with sessions_list_changes.
	listClient := &client{hub: h, role: roleReadOnly, out: make(chan []byte, 2), urlFeatures: map[string]bool{featureSessionsListChanges: true}}
	listHint{plain: []byte(`{"type":"sessions_list","revision":2}`), detailed: []byte(`{"type":"sessions_list","revision":2,"changed":["m1/sh"]}`)}.send(listClient)
	if got := string(<-listClient.out); strings.Contains(got, "changed") {
		t.Fatalf("sessions_list detailed hint leaked to roleReadOnly: %s", got)
	}
}

func TestReadOnlyShortTTLCacheDoubleCheckAndWSExpiry(t *testing.T) {
	h := newTestHub(t)
	h.cfg.Security.AllowReadOnlySessions = true
	store := newReadOnlyTestStore()
	h.store = store
	mux := h.routes()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Fail-closed: a malformed roleReadOnly DB row with nil expires_at is always rejected.
	store.insertRawToken("jd_ro_nil_exp", db.DeviceToken{ID: "dev_ro_nil", Name: "Broken", Kind: tokenKindReadOnly, Role: roleReadOnly, ExpiresAt: nil})
	if rec := doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/machines", "jd_ro_nil_exp", "", "127.0.0.1:43000", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("nil expires_at readonly token: got %d %s, want 401", rec.Code, rec.Body.String())
	}

	// Issue a very short-lived (220ms) read-only session well inside the 30s token cache window.
	ro := mintReadOnlySession(t, mux, h.cfg.Token, `{"label":"short-ttl","ttl":"220ms"}`)

	// Warm the 30s h.devtok.cache via HTTP.
	if rec := doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/machines", ro.Token, "", "127.0.0.1:43000", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("initial HTTP before expiry: %d %s", rec.Code, rec.Body.String())
	}
	h.devtok.mu.Lock()
	_, cached := h.devtok.cache[hashToken(ro.Token)]
	h.devtok.mu.Unlock()
	if !cached {
		t.Fatal("expected readonly token to populate 30s token cache")
	}

	// Connect a live WebSocket before expiry.
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()
	conn, _, err := websocket.Dial(wsCtx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/ws?token="+ro.Token, nil)
	if err != nil {
		t.Fatalf("short-ttl ws dial: %v", err)
	}
	defer conn.CloseNow()
	if _, _, err := conn.Read(wsCtx); err != nil {
		t.Fatalf("read hello: %v", err)
	}

	// Also start a relayed context via trackRelayed to verify relayed streams cancel at ExpiresAt.
	relayReq := httptest.NewRequest(http.MethodGet, "/api/v1/todos", nil)
	relayReq = relayReq.WithContext(context.WithValue(relayReq.Context(), principalKey{}, principal{
		ID: ro.ID, Role: roleReadOnly, ExpiresAt: ro.ExpiresAt,
	}))
	relayedReq, relayDone := h.trackRelayed(relayReq)
	defer relayDone()

	// Wait for WS close on expiry; must arrive with close code 4001 well within 2s (not waiting 30s).
	closeDeadline := time.After(2 * time.Second)
	errCh := make(chan error, 1)
	go func() {
		for {
			_, _, err := conn.Read(wsCtx)
			if err != nil {
				errCh <- err
				return
			}
		}
	}()
	select {
	case err := <-errCh:
		if code := websocket.CloseStatus(err); code != wsCloseReadOnlyExpired {
			t.Fatalf("expired WS close status = %d (%v), want %d", code, err, wsCloseReadOnlyExpired)
		}
	case <-closeDeadline:
		t.Fatal("expired read-only WebSocket did not close promptly")
	}

	// Relayed context must also be cancelled on expiry.
	select {
	case <-relayedReq.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("trackRelayed context did not cancel at ExpiresAt")
	}

	// Immediately after expiry (still well within 30s cache TTL), HTTP must return 401 and evict the cache entry.
	if rec := doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/machines", ro.Token, "", "127.0.0.1:43000", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("HTTP right after expiry within 30s cache window: got %d %s, want 401", rec.Code, rec.Body.String())
	}
	h.devtok.mu.Lock()
	_, stillCached := h.devtok.cache[hashToken(ro.Token)]
	h.devtok.mu.Unlock()
	if stillCached {
		t.Fatal("expired readonly token remained in h.devtok.cache")
	}
}

func TestReadOnlyRevokeSingleAllAndSwitchCircuitBreaker(t *testing.T) {
	h := newTestHub(t)
	h.cfg.Security.AllowReadOnlySessions = true
	store := newReadOnlyTestStore()
	h.store = store
	h.bus.idents = &memIdents{}
	store.insertRawToken("jd_full_keep", db.DeviceToken{ID: "dev_full_keep", Name: "Keep", Kind: "phone", Role: roleFull})

	mux := h.routes()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	revokedCh := make(chan string, 8)
	mgr := connectGuardManager(t, h, srv.URL, "assistant", func(string, json.RawMessage) (any, error) { return map[string]any{}, nil })
	mgr.mu.Lock()
	mgr.onEvent = func(event string, raw json.RawMessage) {
		if event == bus.EventCredentialRevoked {
			var p bus.DeviceCredential
			if json.Unmarshal(raw, &p) == nil {
				revokedCh <- p.ID
			}
		}
	}
	mgr.mu.Unlock()

	dialRO := func(tok string) *websocket.Conn {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/ws?token="+tok, nil)
		if err != nil {
			t.Fatalf("dial %s: %v", tok, err)
		}
		if _, _, err := conn.Read(ctx); err != nil {
			conn.CloseNow()
			t.Fatalf("read hello: %v", err)
		}
		t.Cleanup(func() { _ = conn.CloseNow() })
		return conn
	}
	waitWSClosed := func(conn *websocket.Conn, wantCode websocket.StatusCode) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		for {
			_, _, err := conn.Read(ctx)
			if err != nil {
				if wantCode != 0 && websocket.CloseStatus(err) != wantCode {
					t.Fatalf("ws close code = %d (%v), want %d", websocket.CloseStatus(err), err, wantCode)
				}
				return
			}
		}
	}

	// 1. Single revoke refuses non-readonly token ID with 404.
	if rec := doReadOnlyReq(t, mux, http.MethodDelete, "/api/v1/auth/readonly-sessions/dev_full_keep", h.cfg.Token, "", "127.0.0.1:44000", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("single revoke on full token: got %d %s, want 404", rec.Code, rec.Body.String())
	}

	ro1 := mintReadOnlySession(t, mux, h.cfg.Token, `{"label":"ro1"}`)
	ro2 := mintReadOnlySession(t, mux, h.cfg.Token, `{"label":"ro2"}`)
	c1 := dialRO(ro1.Token)
	c2 := dialRO(ro2.Token)

	// Single revoke closes ro1's WS, notifies manager bus, rejects ro1 HTTP, keeps ro2 alive.
	rec := doReadOnlyReq(t, mux, http.MethodDelete, "/api/v1/auth/readonly-sessions/"+ro1.ID, h.cfg.Token, "", "127.0.0.1:44000", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("single revoke ro1: %d %s", rec.Code, rec.Body.String())
	}
	waitWSClosed(c1, 0)
	select {
	case id := <-revokedCh:
		if id != ro1.ID {
			t.Fatalf("revoked bus event id = %q, want %q", id, ro1.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not receive EventCredentialRevoked for single revoke")
	}
	if rec := doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/machines", ro1.Token, "", "127.0.0.1:44000", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("ro1 after single revoke: got %d, want 401", rec.Code)
	}
	if rec := doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/machines", ro2.Token, "", "127.0.0.1:44000", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("ro2 should stay alive: got %d", rec.Code)
	}

	// 2. Revoke all closes remaining readonly sessions and leaves full device token intact.
	ro3 := mintReadOnlySession(t, mux, h.cfg.Token, `{"label":"ro3"}`)
	c3 := dialRO(ro3.Token)
	rec = doReadOnlyReq(t, mux, http.MethodDelete, "/api/v1/auth/readonly-sessions", h.cfg.Token, "", "127.0.0.1:44000", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke all: %d %s", rec.Code, rec.Body.String())
	}
	var revResp readOnlySessionRevokeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &revResp); err != nil || !revResp.OK || revResp.Revoked != 2 {
		t.Fatalf("revoke all response: %s (err=%v)", rec.Body.String(), err)
	}
	waitWSClosed(c2, 0)
	waitWSClosed(c3, 0)
	if rec := doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/machines", "jd_full_keep", "", "127.0.0.1:44000", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("full device token must survive revoke-all: got %d", rec.Code)
	}

	// 3. Switch-off circuit breaker: turning off allow_readonly_sessions rejects HTTP with 403 and disconnects live WS with 4001.
	ro4 := mintReadOnlySession(t, mux, h.cfg.Token, `{"label":"ro4"}`)
	c4 := dialRO(ro4.Token)
	// Warm cache so middleware hits the cached roleReadOnly path.
	if rec := doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/machines", ro4.Token, "", "127.0.0.1:44000", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("ro4 before switch-off: %d", rec.Code)
	}
	h.cfg.Security.AllowReadOnlySessions = false
	rec = doReadOnlyReq(t, mux, http.MethodGet, "/api/v1/machines", ro4.Token, "", "127.0.0.1:44000", nil, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "readonly_sessions_disabled") {
		t.Fatalf("ro4 HTTP after switch-off: got %d %s, want 403 readonly_sessions_disabled", rec.Code, rec.Body.String())
	}
	waitWSClosed(c4, wsCloseReadOnlyExpired)

	// 4. Switch-off also disconnects a live idle readonly WS when an outbound broadcast arrives, even without HTTP traffic.
	h.cfg.Security.AllowReadOnlySessions = true
	ro5 := mintReadOnlySession(t, mux, h.cfg.Token, `{"label":"ro5"}`)
	c5 := dialRO(ro5.Token)
	h.cfg.Security.AllowReadOnlySessions = false
	h.broadcastMachines()
	waitWSClosed(c5, wsCloseReadOnlyExpired)
}

func TestReadOnlyWebSocketDefaultDenyCustomExtensionEventsLiveAndReplay(t *testing.T) {
	h := newTestHub(t)
	h.cfg.Security.AllowReadOnlySessions = true
	store := newReadOnlyTestStore()
	h.store = store
	store.insertRawToken("jd_full_ext_events", db.DeviceToken{ID: "dev_full_ext_events", Name: "Full", Kind: "web", Role: roleFull})

	// Declare a third-party extension API contribution with a custom broadcast event.
	h.keepExtReport("assistant", &extwire.Report{
		EventsDeclared: true,
		APIs: []extwire.APIPrefix{
			{Prefix: "acme-ext", Events: []string{"acme_custom_event"}},
		},
	})

	mux := h.routes()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ro := mintReadOnlySession(t, mux, h.cfg.Token, `{"label":"ws-default-deny"}`)

	type helloFrame struct {
		Type        string `json:"type"`
		EventStream string `json:"event_stream"`
		EventSeq    int64  `json:"event_seq"`
		Resumed     bool   `json:"resumed"`
	}
	dialAndReadHello := func(tok, query string) (*websocket.Conn, helloFrame) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws?token=" + tok
		if query != "" {
			url += "&" + query
		}
		conn, _, err := websocket.Dial(ctx, url, nil)
		if err != nil {
			t.Fatalf("dial %s: %v", url, err)
		}
		t.Cleanup(func() { _ = conn.CloseNow() })
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read hello: %v", err)
		}
		var hf helloFrame
		if err := json.Unmarshal(raw, &hf); err != nil || hf.Type != "hello" {
			t.Fatalf("hello frame = %s (err=%v)", raw, err)
		}
		return conn, hf
	}
	readUntilType := func(conn *websocket.Conn, stopType string) []string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var seen []string
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				t.Fatalf("read until %s (seen=%v): %v", stopType, seen, err)
			}
			var env struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("decode ws frame %s: %v", raw, err)
			}
			seen = append(seen, env.Type)
			if env.Type == stopType {
				return seen
			}
		}
	}

	// 1. Live WebSocket delivery: both clients connect before the broadcasts.
	roLive, roHello := dialAndReadHello(ro.Token, "")
	fullLive, fullHello := dialAndReadHello("jd_full_ext_events", "")
	if roHello.EventStream == "" || roHello.EventStream != fullHello.EventStream {
		t.Fatalf("event streams differ: ro=%+v full=%+v", roHello, fullHello)
	}
	resumeStream := fullHello.EventStream
	resumeAfter := fullHello.EventSeq

	// Relay a custom third-party extension event, non-allowlisted manager events, and finally an allowlisted core event ("todos") as barrier.
	for _, msg := range []string{
		`{"type":"acme_custom_event","secret":"ext-only-payload"}`,
		`{"type":"mail_accounts","accounts":["private"]}`,
		`{"type":"messaging","text":"private-dm"}`,
		`{"type":"todos"}`,
	} {
		if err := h.relayBroadcastFrom("assistant", json.RawMessage(msg)); err != nil {
			t.Fatalf("relayBroadcastFrom(%s): %v", msg, err)
		}
	}

	fullLiveTypes := readUntilType(fullLive, "todos")
	for _, want := range []string{"acme_custom_event", "mail_accounts", "messaging", "todos"} {
		if !slices.Contains(fullLiveTypes, want) {
			t.Fatalf("full live WS missed %q: got %v", want, fullLiveTypes)
		}
	}
	roLiveTypes := readUntilType(roLive, "todos")
	for _, forbidden := range []string{"acme_custom_event", "mail_accounts", "messaging"} {
		if slices.Contains(roLiveTypes, forbidden) {
			t.Fatalf("readonly live WS received forbidden event %q: got %v", forbidden, roLiveTypes)
		}
	}

	// 2. Eventlog replay (reconnect/resume): both clients resume from before the broadcasts.
	resumeQuery := fmt.Sprintf("event_stream=%s&event_seq=%d", resumeStream, resumeAfter)
	fullResume, fullResHello := dialAndReadHello("jd_full_ext_events", resumeQuery)
	if !fullResHello.Resumed {
		t.Fatalf("full resume hello was not resumed: %+v", fullResHello)
	}
	roResume, roResHello := dialAndReadHello(ro.Token, resumeQuery)
	if !roResHello.Resumed {
		t.Fatalf("readonly resume hello was not resumed: %+v", roResHello)
	}

	fullReplayTypes := readUntilType(fullResume, "todos")
	for _, want := range []string{"acme_custom_event", "mail_accounts", "messaging", "todos"} {
		if !slices.Contains(fullReplayTypes, want) {
			t.Fatalf("full replay WS missed %q: got %v", want, fullReplayTypes)
		}
	}
	roReplayTypes := readUntilType(roResume, "todos")
	for _, forbidden := range []string{"acme_custom_event", "mail_accounts", "messaging"} {
		if slices.Contains(roReplayTypes, forbidden) {
			t.Fatalf("readonly replay WS received forbidden event %q: got %v", forbidden, roReplayTypes)
		}
	}
}
