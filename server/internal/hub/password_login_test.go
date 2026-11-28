package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/passwordpake"
)

type passwordMemStore struct {
	*adminMemStore
	password *db.ClientPassword
	tokens   map[string]db.DeviceToken
}

func (s *passwordMemStore) ClientPassword(context.Context) (*db.ClientPassword, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.password == nil {
		return nil, nil
	}
	p := *s.password
	return &p, nil
}
func (s *passwordMemStore) SaveClientPassword(_ context.Context, p db.ClientPassword, previous string, revoke bool) (bool, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if passwordRevision(s.password) != previous {
		return false, nil, nil
	}
	s.password = &p
	var ids []string
	if revoke {
		for hash, device := range s.tokens {
			ids = append(ids, device.ID)
			delete(s.tokens, hash)
		}
	}
	return true, ids, nil
}
func (s *passwordMemStore) IssuePasswordDevice(_ context.Context, revision string, d db.DeviceToken, hash string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.password == nil || !s.password.Enabled || s.password.Revision != revision {
		return false, nil
	}
	s.tokens[hash] = d
	return true, nil
}
func (s *passwordMemStore) DeviceTokenByHash(_ context.Context, hash string) (*db.DeviceToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tokens[hash]
	if !ok || d.RevokedAt != nil {
		return nil, nil
	}
	return &d, nil
}
func (s *passwordMemStore) DeviceTokens(context.Context) ([]db.DeviceToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.DeviceToken, 0, len(s.tokens))
	for _, d := range s.tokens {
		out = append(out, d)
	}
	return out, nil
}
func (s *passwordMemStore) RevokeDeviceToken(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for hash, d := range s.tokens {
		if d.ID == id && d.RevokedAt == nil {
			now := time.Now()
			d.RevokedAt = &now
			s.tokens[hash] = d
			return true, nil
		}
	}
	return false, nil
}
func (s *passwordMemStore) TouchDeviceToken(context.Context, string) error { return nil }

func passwordTestHub(t *testing.T) (*Hub, *passwordMemStore, *adminClient, *adminClient) {
	t.Helper()
	h, parent, _ := adminTestHub(t)
	s := &passwordMemStore{adminMemStore: parent, tokens: map[string]db.DeviceToken{}}
	h.store = s
	setAdmin(h, AdminConfig{Access: accessLoopback})
	cookie := "admin-test-cookie"
	s.sessions[sha256Hex(cookie)] = db.AdminSession{IDHash: sha256Hex(cookie), CSRF: "csrf-test", ExpiresAt: time.Now().Add(time.Hour)}
	// The existing admin guard also requires a configured administrator password.
	s.hash = "configured-admin-password"
	admin := &adminClient{t: t, h: h.routes(), remote: "127.0.0.1:1234", tls: true, csrf: "csrf-test", cookie: &http.Cookie{Name: compat.AdminCookieNames(true)[1], Value: cookie}}
	client := &adminClient{t: t, h: h.routes(), remote: "192.0.2.9:1234", tls: true}
	return h, s, admin, client
}

func passwordCall(t *testing.T, c *adminClient, method, path string, in any, want int) map[string]any {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	status, out, _ := c.do(method, path, string(b))
	if status != want {
		t.Fatalf("%s %s: got %d, want %d: %+v", method, path, status, want, out)
	}
	return out
}

func registerClientPassword(t *testing.T, admin *adminClient, password, previous string, revoke bool) {
	t.Helper()
	c, _ := passwordpake.Configuration().Client()
	request, err := c.RegistrationInit([]byte(password))
	if err != nil {
		t.Fatal(err)
	}
	start := passwordCall(t, admin, "POST", "/api/v1/admin/password-login/register/start", map[string]any{"message": request.Serialize(), "revision": previous}, 200)
	m, _ := base64.StdEncoding.DecodeString(start["message"].(string))
	response, err := c.Deserialize.RegistrationResponse(m)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := c.RegistrationFinalize(response, passwordpake.ClientIdentity, []byte(start["identity"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	passwordCall(t, admin, "POST", "/api/v1/admin/password-login/register/finish", map[string]any{"session": start["session"], "message": record.Serialize(), "revoke_devices": revoke}, 200)
}

func passwordProof(t *testing.T, client *adminClient, password, challenge string) (string, []byte, error) {
	t.Helper()
	c, _ := passwordpake.Configuration().Client()
	ke1, err := c.GenerateKE1([]byte(password))
	if err != nil {
		t.Fatal(err)
	}
	start := passwordCall(t, client, "POST", "/api/v1/auth/password/start", map[string]any{"message": ke1.Serialize(), "name": "测试设备", "kind": "phone", "challenge": challenge}, 200)
	m, _ := base64.StdEncoding.DecodeString(start["message"].(string))
	ke2, err := c.Deserialize.KE2(m)
	if err != nil {
		t.Fatal(err)
	}
	ke3, _, _, err := c.GenerateKE3(ke2, passwordpake.ClientIdentity, []byte(start["identity"].(string)))
	if err != nil {
		return start["session"].(string), nil, err
	}
	return start["session"].(string), ke3.Serialize(), nil
}

func TestPasswordLoginAdminCookies(t *testing.T) {
	_, _, legacy, _ := passwordTestHub(t)
	names := compat.AdminCookieNames(true)
	if len(names) != 2 || names[0] != "persona_admin_tls" || names[0] == names[1] {
		t.Fatalf("unexpected secure admin cookie names: %v", names)
	}
	passwordCall(t, legacy, "GET", "/api/v1/admin/password-login", nil, 200)
	current := *legacy
	current.cookie = &http.Cookie{Name: names[0], Value: legacy.cookie.Value}
	passwordCall(t, &current, "GET", "/api/v1/admin/password-login", nil, 200)
	for _, invalid := range []string{"", "invalid"} {
		mixed := *legacy
		mixed.header = http.Header{"Cookie": {(&http.Cookie{Name: names[0], Value: invalid}).String()}}
		passwordCall(t, &mixed, "GET", "/api/v1/admin/password-login", nil, 401)
	}
	current.header = http.Header{"Cookie": {(&http.Cookie{Name: names[1], Value: "invalid"}).String()}}
	passwordCall(t, &current, "GET", "/api/v1/admin/password-login", nil, 200)
}

func TestPasswordLoginRegistrationAndDevice(t *testing.T) {
	h, store, admin, client := passwordTestHub(t)
	password := "只在客户端出现的密码 2026"
	registerClientPassword(t, admin, password, "", false)
	p, _ := store.ClientPassword(context.Background())
	server, err := passwordpake.Server(p.ServerKeys)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Deserialize.RegistrationRecord(p.Record); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(p.Record, []byte(password)) || bytes.Contains(p.ServerKeys, []byte(password)) {
		t.Fatal("password persisted")
	}
	state := passwordCall(t, client, "GET", "/api/v1/auth/password/state", nil, 200)
	if state["enabled"] != true || state["suite"] != passwordpake.Suite || state["replaces"] != true {
		t.Fatal(state)
	}
	session, proof, err := passwordProof(t, client, password, "")
	if err != nil {
		t.Fatal(err)
	}
	result := passwordCall(t, client, "POST", "/api/v1/auth/password/finish", map[string]any{"session": session, "message": proof}, 200)
	token := result["token"].(string)
	if !strings.HasPrefix(token, "jd_") {
		t.Fatal("not a device token")
	}
	principal, ok := h.authenticate(context.Background(), token)
	if !ok || principal.Role != roleFull || principal.ID != result["id"] {
		t.Fatal("issued token cannot authenticate")
	}
	passwordCall(t, client, "POST", "/api/v1/auth/password/finish", map[string]any{"session": session, "message": proof}, 401)
	if len(store.tokens) != 1 {
		t.Fatal("replay issued another device")
	}
	// An incorrect password fails the server proof at the client, without sending the password or a fixed hash.
	wrongSession, _, err := passwordProof(t, client, "wrong-password", "")
	if err == nil {
		t.Fatal("wrong password authenticated server")
	}
	passwordCall(t, client, "POST", "/api/v1/auth/password/finish", map[string]any{"session": wrongSession, "message": []byte("forged-proof")}, 401)
	// JSON APIs explicitly refuse plaintext password fields.
	passwordCall(t, client, "POST", "/api/v1/auth/password/start", map[string]any{"password": password}, 400)
}

func TestPasswordLoginPKCEAndChanges(t *testing.T) {
	_, store, admin, client := passwordTestHub(t)
	registerClientPassword(t, admin, "client password", "", false)
	verifier := strings.Repeat("V", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	grant := func() string {
		session, proof, err := passwordProof(t, client, "client password", challenge)
		if err != nil {
			t.Fatal(err)
		}
		out := passwordCall(t, client, "POST", "/api/v1/auth/password/finish", map[string]any{"session": session, "message": proof}, 200)
		if _, ok := out["token"]; ok {
			t.Fatal("device token leaked into browser callback")
		}
		return out["code"].(string)
	}
	code := grant()
	passwordCall(t, client, "POST", "/api/v1/auth/password/exchange", map[string]any{"code": code, "verifier": strings.Repeat("X", 43)}, 401)
	passwordCall(t, client, "POST", "/api/v1/auth/password/exchange", map[string]any{"code": code, "verifier": verifier}, 401)
	code = grant()
	passwordCall(t, client, "POST", "/api/v1/auth/password/exchange", map[string]any{"code": code, "verifier": verifier}, 200)
	passwordCall(t, client, "POST", "/api/v1/auth/password/exchange", map[string]any{"code": code, "verifier": verifier}, 401)
	code = grant()
	p, _ := store.ClientPassword(context.Background())
	passwordCall(t, admin, "PUT", "/api/v1/admin/password-login", map[string]any{"revision": p.Revision, "enabled": false}, 200)
	passwordCall(t, client, "POST", "/api/v1/auth/password/exchange", map[string]any{"code": code, "verifier": verifier}, 401)
	if len(store.tokens) != 1 {
		t.Fatal("disabling should preserve already signed-in devices")
	}
	p, _ = store.ClientPassword(context.Background())
	registerClientPassword(t, admin, "a different password", p.Revision, true)
	if len(store.tokens) != 0 {
		t.Fatal("revoke all did not revoke existing devices")
	}
}

func TestPasswordLoginGatesExpiryAndConcurrency(t *testing.T) {
	h, _, admin, client := passwordTestHub(t)
	registerClientPassword(t, admin, "client password", "", false)
	plain := *client
	plain.tls = false
	passwordCall(t, &plain, "GET", "/api/v1/auth/password/state", nil, 403)
	passwordCall(t, client, "POST", "/api/v1/admin/password-login/register/start", map[string]any{}, 401)
	cross := *client
	cross.header = http.Header{"Origin": []string{"https://attacker.example"}}
	passwordCall(t, &cross, "POST", "/api/v1/auth/password/start", map[string]any{}, 403)
	session, proof, err := passwordProof(t, client, "client password", "")
	if err != nil {
		t.Fatal(err)
	}
	h.passwordLogin.mu.Lock()
	f := h.passwordLogin.flows[session]
	f.until = time.Now().Add(-time.Second)
	h.passwordLogin.flows[session] = f
	h.passwordLogin.mu.Unlock()
	passwordCall(t, client, "POST", "/api/v1/auth/password/finish", map[string]any{"session": session, "message": proof}, 401)
	session, proof, err = passwordProof(t, client, "client password", "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	body, _ := json.Marshal(map[string]any{"session": session, "message": proof})
	for range 2 {
		wg.Go(func() {
			status, _, _ := client.do("POST", "/api/v1/auth/password/finish", string(body))
			statuses <- status
		})
	}
	wg.Wait()
	close(statuses)
	var success, refused int
	for status := range statuses {
		if status == 200 {
			success++
		}
		if status == 401 {
			refused++
		}
	}
	if success != 1 || refused != 1 {
		t.Fatal("concurrent proof replay accepted")
	}
}

func TestPasswordLoginProxyTLS(t *testing.T) {
	h, _, admin, client := passwordTestHub(t)
	registerClientPassword(t, admin, "client password", "", false)
	h.admin.gateways = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("fd00::1")} }
	for _, tc := range []struct {
		name, remote, proto, forwarded string
		tls                            bool
		want                           int
	}{
		{"native TLS", "192.0.2.9:1234", "", "", true, 200},
		{"loopback", "127.0.0.1:1234", "", "", false, 200},
		{"LAN HTTPS proxy", "[fd00::20]:1234", "https", "", false, 200},
		{"LAN Forwarded proxy", "[fd00::20]:1234", "", "proto=https", false, 200},
		{"LAN plaintext", "[fd00::20]:1234", "", "", false, 403},
		{"LAN HTTP proxy", "[fd00::20]:1234", "http", "", false, 403},
		{"public plaintext", "192.0.2.9:1234", "", "", false, 403},
		{"public spoofed HTTPS", "192.0.2.9:1234", "https", "", false, 403},
		{"public spoofed Forwarded", "192.0.2.9:1234", "", "proto=https", false, 403},
		{"untrusted container gateway", "[fd00::1]:1234", "https", "", false, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := *client
			c.t, c.remote, c.tls = t, tc.remote, tc.tls
			c.header = http.Header{"X-Forwarded-Proto": {tc.proto}, "Forwarded": {tc.forwarded}}
			state := passwordCall(t, &c, "GET", "/api/v1/auth/password/state", nil, tc.want)
			if tc.want == 200 && state["enabled"] != true {
				t.Fatal(state)
			}
			if tc.want == 403 && state["code"] != "tls_required" {
				t.Fatal(state)
			}
		})
	}
	// Complete the password exchange through the proxy, not just discovery.
	client.remote, client.tls = "[fd00::20]:1234", false
	client.header = http.Header{"X-Forwarded-Proto": {"https"}}
	session, proof, err := passwordProof(t, client, "client password", "")
	if err != nil {
		t.Fatal(err)
	}
	result := passwordCall(t, client, "POST", "/api/v1/auth/password/finish", map[string]any{"session": session, "message": proof}, 200)
	principal, ok := h.authenticate(context.Background(), result["token"].(string))
	if !ok || principal.Role != roleFull {
		t.Fatal("proxy login did not issue a usable device token")
	}
	client.header.Set("Origin", "https://attacker.example")
	passwordCall(t, client, "POST", "/api/v1/auth/password/start", map[string]any{}, 403)
}

func TestPasswordLoginReplacesSameKindToken(t *testing.T) {
	h, _, admin, client := passwordTestHub(t)
	password := "replace-password-2026"
	registerClientPassword(t, admin, password, "", false)

	signIn := func(kind, name, replaces string) map[string]any {
		t.Helper()
		c, _ := passwordpake.Configuration().Client()
		ke1, err := c.GenerateKE1([]byte(password))
		if err != nil {
			t.Fatal(err)
		}
		req := map[string]any{"message": ke1.Serialize(), "name": name, "kind": kind}
		if replaces != "" {
			req["replaces"] = replaces
		}
		start := passwordCall(t, client, "POST", "/api/v1/auth/password/start", req, 200)
		m, _ := base64.StdEncoding.DecodeString(start["message"].(string))
		ke2, err := c.Deserialize.KE2(m)
		if err != nil {
			t.Fatal(err)
		}
		ke3, _, _, err := c.GenerateKE3(ke2, passwordpake.ClientIdentity, []byte(start["identity"].(string)))
		if err != nil {
			t.Fatal(err)
		}
		return passwordCall(t, client, "POST", "/api/v1/auth/password/finish", map[string]any{"session": start["session"], "message": ke3.Serialize()}, 200)
	}

	firstWeb := signIn("web", "Browser 1", "")
	firstPhone := signIn("phone", "Phone 1", "")

	secondWeb := signIn("web", "Browser 2", firstWeb["id"].(string))
	if secondWeb["replaced"] != firstWeb["id"] {
		t.Fatalf("expected replaced=%v, got %v", firstWeb["id"], secondWeb)
	}
	if _, ok := h.authenticate(context.Background(), firstWeb["token"].(string)); ok {
		t.Fatal("replaced web token still authenticates")
	}
	if _, ok := h.authenticate(context.Background(), secondWeb["token"].(string)); !ok {
		t.Fatal("new web token should authenticate")
	}

	// Cross-kind replacement (web trying to replace phone) must not revoke the phone token.
	thirdWeb := signIn("web", "Browser 3", firstPhone["id"].(string))
	if thirdWeb["replaced"] != nil {
		t.Fatalf("cross-kind replace should be ignored: %v", thirdWeb)
	}
	if _, ok := h.authenticate(context.Background(), firstPhone["token"].(string)); !ok {
		t.Fatal("phone token should remain valid")
	}

	// Long replaces values (> 64 bytes) do not fail /auth/password/start with 400.
	longWeb := signIn("web", "Browser 4", "dev_"+strings.Repeat("x", 65))
	if longWeb["token"] == "" {
		t.Fatalf("long replaces should still issue a token: %v", longWeb)
	}
}
