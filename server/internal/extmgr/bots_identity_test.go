package extmgr

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
)

// The identity is a dedicated type: its JSON carries only the public fields, and a planted secret, pairing code,
// registration and owner id never appear in it.
func TestBotIdentityCarriesNoSecret(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.h.bots.mu.Lock()
	w.h.bots.set.Pairing = &botPairing{Code: "135790"}
	w.h.bots.set.Registration = &botRegistration{URL: "https://example.invalid/device-code-xyz"}
	w.h.bots.set.RegistrationDeviceEnc = "sealed-device"
	w.h.bots.set.RegistrationSecretEnc = "sealed-secret"
	w.h.bots.set.SecretEnc = "sealed-app-secret"
	w.h.bots.set.LarkCLIProfile = "private-profile"
	owner := w.h.bots.set.Owner.OpenID
	w.h.bots.mu.Unlock()

	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/bots/feishu/identity", nil))
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("identity: %d %s", rec.Code, body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"enabled": true, "paired": true, "ready": true, "app_id": true, "bot_open_id": true, "bot_name": true, "domain": true, "capabilities": true}
	for k := range got {
		if !allowed[k] {
			t.Errorf("unexpected key %q", k)
		}
	}
	for _, secret := range []string{"135790", "device-code-xyz", "sealed-device", "sealed-secret", "sealed-app-secret", "s3cret", "private-profile", owner, "secret_enc", "pairing"} {
		if strings.Contains(body, secret) {
			t.Errorf("the identity carries %q: %s", secret, body)
		}
	}
	if got["enabled"] != true || got["paired"] != true || got["app_id"] != "cli_test" || got["bot_name"] != "Persona" {
		t.Fatalf("identity: %v", got)
	}
	caps, _ := got["capabilities"].([]any)
	if len(caps) != 1 || caps[0] != "proxy_reply" {
		t.Fatalf("capabilities: %v", got["capabilities"])
	}
}

// An extension token with the scope reads the identity; without it the scope layer names the scope it needs.
func TestBotIdentityScope(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	granted := w.h.extTokens.issue("reader", "Reader", []string{extension.ScopeBotsFeishuIdentity})
	denied := w.h.extTokens.issue("other", "Other", []string{extension.ScopeKBRead})
	for _, c := range []struct {
		token string
		ok    bool
	}{{granted, true}, {denied, false}} {
		auth := w.h.authorize(extwire.Authorize{Hash: hashToken(c.token), Pattern: "GET /api/v1/bots/feishu/identity", Method: "GET", Path: "/api/v1/bots/feishu/identity"})
		if auth.OK != c.ok || auth.Scope != extension.ScopeBotsFeishuIdentity {
			t.Errorf("token ok %v: %+v", c.ok, auth)
		}
	}
}

// The identity says what is true: off, unpaired, and with no credentials each leave their fields out.
func TestBotIdentityShapes(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"enabled":false}`); code != 200 {
		t.Fatal(code)
	}
	code, out := w.do(t, "GET", "/api/v1/bots/feishu/identity", "")
	if code != 200 || out["enabled"] != false || out["paired"] != false || out["ready"] != false || out["app_id"] != "cli_test" {
		t.Fatalf("off: %d %v", code, out)
	}
}
