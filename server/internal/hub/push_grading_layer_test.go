package hub

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

func gradingLayerHTTP(t *testing.T, h *Hub, p *principal, method, target, body string) (int, []byte) {
	t.Helper()
	mux := http.NewServeMux()
	h.pushGradingRoutes(mux)
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if p != nil {
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, *p))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func extGradingPrincipal(manager, ext string) *principal {
	return &principal{
		ID:        "extension:" + ext,
		Name:      ext,
		Role:      roleExtension,
		Extension: ext,
		Manager:   manager,
		Scopes:    []string{extension.ScopePushGrading},
	}
}

// Scope enforcement through the full hub routes and authorization middleware:
// push.grading is required for /api/v1/push/grading/layer and /api/v1/push/grading/preview,
// is excluded from notify and legacy every-scope / all-scope tokens, and never opens PUT /api/v1/push/grading.
func TestPushGradingLayerScopeAuthorization(t *testing.T) {
	h := newTestHub(t)
	h.push.store = newGradingStore()
	mux := h.routes()
	tokens := extTokensOf(t, h)

	routes := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/push/grading/layer", ""},
		{http.MethodPut, "/api/v1/push/grading/layer", `{"version":1,"actions":{"P1":"hold"}}`},
		{http.MethodDelete, "/api/v1/push/grading/layer", ""},
		{http.MethodPost, "/api/v1/push/grading/preview", `{"notification":{"kind":"assistant","level":"P1"}}`},
	}

	for name, tok := range map[string]string{
		"none":         tokens.issue("ext-none", "None", nil),
		"notify-only":  tokens.issue("ext-notify", "Notify", []string{extension.ScopeNotify}),
		"legacy-every": tokens.issue("ext-every", "Every", extension.EveryScope()),
	} {
		t.Run("denied_"+name, func(t *testing.T) {
			for _, rt := range routes {
				code, body := call(t, mux, rt.method, rt.path, tok, rt.body)
				if code != http.StatusForbidden || body["code"] != "permission_denied" || body["scope"] != extension.ScopePushGrading {
					t.Fatalf("%s %s (%s): got %d %v, want 403 permission_denied scope=%s", rt.method, rt.path, name, code, body, extension.ScopePushGrading)
				}
			}
		})
	}

	granted := tokens.issue("ext-grader", "Grader", []string{extension.ScopePushGrading})
	for _, rt := range routes {
		code, body := call(t, mux, rt.method, rt.path, granted, rt.body)
		if code != http.StatusOK {
			t.Fatalf("%s %s with push.grading: got %d %v, want 200", rt.method, rt.path, code, body)
		}
	}

	// push.grading never opens the owner's switch writes.
	for _, switchPath := range []string{"/api/v1/push/grading", "/api/v1/push/ln-mode"} {
		code, body := call(t, mux, http.MethodPut, switchPath, granted, `{"enabled":true}`)
		if code != http.StatusForbidden || body["code"] != "permission_denied" {
			t.Fatalf("PUT %s with push.grading: got %d %v, want 403 permission_denied", switchPath, code, body)
		}
	}

	// Answer-only and scoped credentials are refused on all four routes.
	for _, role := range []string{roleAnswer, roleScoped} {
		p := &principal{ID: "dev-" + role, Role: role}
		for _, rt := range routes {
			code, body := gradingLayerHTTP(t, h, p, rt.method, rt.path, rt.body)
			if code != http.StatusForbidden {
				t.Fatalf("%s %s as %s: got %d %s, want 403", rt.method, rt.path, role, code, body)
			}
		}
	}

	// Full credentials can GET, DELETE, and preview, but cannot PUT an extension-owned layer.
	master := h.cfg.Token
	if code, body := call(t, mux, http.MethodPut, "/api/v1/push/grading/layer", master, `{"version":1,"actions":{"P1":"hold"}}`); code != http.StatusForbidden {
		t.Fatalf("full credential PUT layer: got %d %v, want 403", code, body)
	}
	if code, body := call(t, mux, http.MethodGet, "/api/v1/push/grading/layer", master, ""); code != http.StatusOK {
		t.Fatalf("full credential GET layer: got %d %v, want 200", code, body)
	}
	if code, body := call(t, mux, http.MethodPost, "/api/v1/push/grading/preview", master, `{"notification":{"kind":"assistant","level":"P1"}}`); code != http.StatusOK {
		t.Fatalf("full credential POST preview: got %d %v, want 200", code, body)
	}
	if code, body := call(t, mux, http.MethodDelete, "/api/v1/push/grading/layer", master, ""); code != http.StatusOK {
		t.Fatalf("full credential DELETE layer: got %d %v, want 200", code, body)
	}
}

// Strict layer ownership and manager isolation: an extension can read, write, delete and preview only its own layer
// on its own manager; naming another extension or manager is rejected with 403, and competing singleton writes return 409 layer_taken.
func TestPushGradingLayerOwnershipIsolation(t *testing.T) {
	h, _ := gradingHub(t)
	extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")
	extB := extGradingPrincipal(assistantwire.AssistantID, "ext-b")
	extOtherMgr := extGradingPrincipal("manager-b", "ext-a")

	// Ext A stores its versioned singleton layer.
	code, body := gradingLayerHTTP(t, h, extA, http.MethodPut, "/api/v1/push/grading/layer",
		`{"version":1,"actions":{"P1":"hold","P2":"push"},"dedup_window":"6h"}`)
	if code != http.StatusOK {
		t.Fatalf("extA PUT: %d %s", code, body)
	}

	// Ext B sees only its own (absent) layer, never Ext A's.
	code, body = gradingLayerHTTP(t, h, extB, http.MethodGet, "/api/v1/push/grading/layer", "")
	if code != http.StatusOK {
		t.Fatalf("extB GET: %d %s", code, body)
	}
	var gotB pushGradingLayerResponse
	if err := json.Unmarshal(body, &gotB); err != nil || gotB.Layer != nil {
		t.Fatalf("extB saw another extension's layer: %s (%v)", body, err)
	}

	// Same extension ID on manager-b also sees null (never crosses managers).
	code, body = gradingLayerHTTP(t, h, extOtherMgr, http.MethodGet, "/api/v1/push/grading/layer", "")
	if code != http.StatusOK {
		t.Fatalf("extOtherMgr GET: %d %s", code, body)
	}
	var gotOtherMgr pushGradingLayerResponse
	if err := json.Unmarshal(body, &gotOtherMgr); err != nil || gotOtherMgr.Layer != nil {
		t.Fatalf("extOtherMgr saw assistant's layer: %s (%v)", body, err)
	}

	// Ext B cannot read, overwrite, delete, or preview Ext A's layer or another manager.
	denied := []struct {
		name, method, target, body, wantCode string
	}{
		{"foreign GET query ext", http.MethodGet, "/api/v1/push/grading/layer?extension=ext-a", "", codeLayerOwnershipDenied},
		{"foreign GET query mgr", http.MethodGet, "/api/v1/push/grading/layer?manager=manager-b", "", codeOtherManager},
		{"foreign PUT query ext", http.MethodPut, "/api/v1/push/grading/layer?extension=ext-a", `{"version":1}`, codeLayerOwnershipDenied},
		{"foreign PUT query mgr", http.MethodPut, "/api/v1/push/grading/layer?manager=manager-b", `{"version":1}`, codeOtherManager},
		{"foreign DELETE query ext", http.MethodDelete, "/api/v1/push/grading/layer?extension=ext-a", "", codeLayerOwnershipDenied},
		{"foreign DELETE query mgr", http.MethodDelete, "/api/v1/push/grading/layer?manager=manager-b", "", codeOtherManager},
		{"foreign preview query ext", http.MethodPost, "/api/v1/push/grading/preview?extension=ext-a", `{"notification":{"level":"P1"}}`, codeLayerOwnershipDenied},
		{"foreign preview query mgr", http.MethodPost, "/api/v1/push/grading/preview?manager=manager-b", `{"notification":{"level":"P1"}}`, codeOtherManager},
		{"foreign preview notif mgr", http.MethodPost, "/api/v1/push/grading/preview", `{"notification":{"level":"P1","manager":"manager-b"}}`, codeOtherManager},
	}
	for _, tc := range denied {
		code, raw := gradingLayerHTTP(t, h, extB, tc.method, tc.target, tc.body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		if code != http.StatusForbidden || out["code"] != tc.wantCode {
			t.Fatalf("%s: got %d %s, want 403 code=%q", tc.name, code, raw, tc.wantCode)
		}
	}

	// Competing PUT from Ext B or from ext-a on manager-b returns 409 layer_taken and leaves Ext A's layer intact.
	for name, p := range map[string]*principal{"other ext": extB, "other manager": extOtherMgr} {
		code, raw := gradingLayerHTTP(t, h, p, http.MethodPut, "/api/v1/push/grading/layer", `{"version":1,"actions":{"P1":"silence"}}`)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		if code != http.StatusConflict || out["code"] != codePushGradingLayerTaken {
			t.Fatalf("%s competing PUT: got %d %s, want 409 code=%q", name, code, raw, codePushGradingLayerTaken)
		}
	}

	// Extension principal without an extension ID is refused.
	noExt := &principal{Role: roleExtension, Manager: assistantwire.AssistantID}
	if code, raw := gradingLayerHTTP(t, h, noExt, http.MethodPut, "/api/v1/push/grading/layer", `{"version":1}`); code != http.StatusForbidden {
		t.Fatalf("empty extension identity: got %d %s, want 403", code, raw)
	}

	// Ext B deleting its own (non-existent) layer leaves Ext A's layer untouched.
	code, body = gradingLayerHTTP(t, h, extB, http.MethodDelete, "/api/v1/push/grading/layer", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"removed":false`) {
		t.Fatalf("extB DELETE own empty: %d %s", code, body)
	}
	snap, err := readPushGradingSnapshot(context.Background(), h.push.store)
	if err != nil || snap.Layer == nil || snap.Layer.Source != "extension:ext-a" || snap.Layer.Manager != assistantwire.AssistantID {
		t.Fatalf("extB DELETE removed extA's layer: %+v err=%v", snap, err)
	}

	// Full credential inspects the singleton layer and respects manager/extension filters.
	code, body = gradingLayerHTTP(t, h, nil, http.MethodGet, "/api/v1/push/grading/layer", "")
	if code != http.StatusOK {
		t.Fatalf("owner GET: %d %s", code, body)
	}
	var ownerView pushGradingLayerResponse
	if err := json.Unmarshal(body, &ownerView); err != nil || ownerView.Layer == nil || ownerView.Layer.Source != "extension:ext-a" || ownerView.Layer.Revision != 1 {
		t.Fatalf("owner GET = %s (%v)", body, err)
	}
	code, body = gradingLayerHTTP(t, h, nil, http.MethodGet, "/api/v1/push/grading/layer?manager=manager-b", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"layer":null`) {
		t.Fatalf("owner GET other manager = %d %s, want layer:null", code, body)
	}
}

// Layer updates persist durably in hub.push_grading_layer across reloads, bump revision on renewal,
// never touch hub.ln_mode, and leave stored state unchanged on store failure.
func TestPushGradingLayerPersistenceAcrossReload(t *testing.T) {
	h, _ := gradingHub(t)
	store := h.push.store.(*gradingStore)
	flaky := &flakyGradingLayerStore{gradingStore: store}
	h.push.store = flaky
	extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")

	code, body := gradingLayerHTTP(t, h, extA, http.MethodPut, "/api/v1/push/grading/layer",
		`{"version":1,"actions":{"P1":"hold","P2":"push"},"dedup_window":"30m"}`)
	if code != http.StatusOK {
		t.Fatalf("PUT v1: %d %s", code, body)
	}
	var res1 pushGradingLayerResponse
	if err := json.Unmarshal(body, &res1); err != nil || res1.Layer == nil || res1.Layer.Revision != 1 || res1.Layer.Source != "extension:ext-a" {
		t.Fatalf("PUT v1 response = %s (%v)", body, err)
	}
	store.mu.Lock()
	_, legacyExists := store.vals["hub.push_grading.layers"]
	store.mu.Unlock()
	if legacyExists {
		t.Fatal("PUT wrote to legacy hub.push_grading.layers key")
	}

	// Read from a fresh hub sharing the same store.
	restarted := &Hub{push: newPusher(t.TempDir(), store, t.Logf)}
	code, body = gradingLayerHTTP(t, restarted, extA, http.MethodGet, "/api/v1/push/grading/layer", "")
	if code != http.StatusOK {
		t.Fatalf("restarted GET: %d %s", code, body)
	}
	var got pushGradingLayerResponse
	if err := json.Unmarshal(body, &got); err != nil || got.Layer == nil || got.Layer.Revision != 1 ||
		got.Layer.Config.Actions.value.P2.value != "push" || got.Layer.Config.DedupWindow.value != 30*time.Minute {
		t.Fatalf("reloaded layer = %s (%v)", body, err)
	}

	// Same-owner renewal increments revision and preserves explicit null resets.
	code, body = gradingLayerHTTP(t, h, extA, http.MethodPut, "/api/v1/push/grading/layer",
		`{"version":1,"actions":{"P1":null,"P2":"silence"},"dedup_window":null}`)
	if code != http.StatusOK {
		t.Fatalf("PUT v2: %d %s", code, body)
	}
	var res2 pushGradingLayerResponse
	if err := json.Unmarshal(body, &res2); err != nil || res2.Layer == nil || res2.Layer.Revision != 2 ||
		!res2.Layer.Config.Actions.value.P1.null || !res2.Layer.Config.DedupWindow.null {
		t.Fatalf("PUT v2 response = %s (%v)", body, err)
	}

	// A store write failure returns 503 and does NOT mutate stored state.
	flaky.mu.Lock()
	flaky.failPut = true
	flaky.mu.Unlock()
	code, body = gradingLayerHTTP(t, h, extA, http.MethodPut, "/api/v1/push/grading/layer", `{"version":1,"actions":{"P1":"push"}}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("PUT with failing store: %d %s, want 503", code, body)
	}
	flaky.mu.Lock()
	flaky.failPut = false
	flaky.mu.Unlock()
	snap, err := readPushGradingSnapshot(context.Background(), store)
	if err != nil || snap.Layer == nil || snap.Layer.Revision != 2 || snap.Layer.Config.Actions.value.P2.value != "silence" {
		t.Fatalf("stored layer changed after failed store write: %+v err=%v", snap, err)
	}

	// Delete persists across reload too.
	if code, body = gradingLayerHTTP(t, h, extA, http.MethodDelete, "/api/v1/push/grading/layer", ""); code != http.StatusOK || !strings.Contains(string(body), `"removed":true`) {
		t.Fatalf("DELETE: %d %s", code, body)
	}
	restarted3 := &Hub{push: newPusher(t.TempDir(), store, t.Logf)}
	code, body = gradingLayerHTTP(t, restarted3, extA, http.MethodGet, "/api/v1/push/grading/layer", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"layer":null`) {
		t.Fatalf("deleted layer returned after reload: %d %s", code, body)
	}
}

// Removing a layer (by the extension or by the owner) OR disabling / uninstalling / revoking push.grading
// (via extwire.EventReport) removes hub.push_grading_layer durably without ever mutating hub.ln_mode.
func TestPushGradingLayerRemovalAndExtensionTeardownRestoresOwnerSettings(t *testing.T) {
	t.Run("owner switch off preserved across claim and delete", func(t *testing.T) {
		h, _ := gradingHub(t)
		extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")

		if code, body := gradingLayerHTTP(t, h, extA, http.MethodPut, "/api/v1/push/grading/layer", `{"version":1,"actions":{"P1":"hold"}}`); code != http.StatusOK {
			t.Fatalf("PUT: %d %s", code, body)
		}
		if h.push.pushGradingOn() {
			t.Fatal("extension layer flipped owner's hub.ln_mode switch")
		}
		if code, body := gradingLayerHTTP(t, h, nil, http.MethodGet, "/api/v1/push/grading", ""); code != http.StatusOK || string(body) != "{\"enabled\":false}\n" {
			t.Fatalf("GET /api/v1/push/grading = %d %s, want enabled:false", code, body)
		}
		if code, body := gradingLayerHTTP(t, h, extA, http.MethodDelete, "/api/v1/push/grading/layer", ""); code != http.StatusOK || !strings.Contains(string(body), `"removed":true`) {
			t.Fatalf("DELETE: %d %s", code, body)
		}
		if snap, err := readPushGradingSnapshot(context.Background(), h.push.store); err != nil || snap.Layer != nil {
			t.Fatalf("layer remained after delete: %+v err=%v", snap, err)
		}
	})

	t.Run("owner switch on preserved across claim and owner delete", func(t *testing.T) {
		h, _ := gradingHub(t)
		if err := h.push.setPushGrading(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")

		if code, body := gradingLayerHTTP(t, h, extA, http.MethodPut, "/api/v1/push/grading/layer", `{"version":1,"actions":{"P1":"push"}}`); code != http.StatusOK {
			t.Fatalf("PUT: %d %s", code, body)
		}
		if !h.push.pushGradingOn() {
			t.Fatal("extension layer disabled owner's hub.ln_mode switch")
		}
		// Owner removes the extension's layer with full credentials -> switch stays on, layer is gone.
		if code, body := gradingLayerHTTP(t, h, nil, http.MethodDelete, "/api/v1/push/grading/layer?extension=ext-a", ""); code != http.StatusOK || !strings.Contains(string(body), `"removed":true`) {
			t.Fatalf("owner DELETE: %d %s", code, body)
		}
		if !h.push.pushGradingOn() {
			t.Fatal("owner DELETE flipped owner's hub.ln_mode switch")
		}
		if snap, err := readPushGradingSnapshot(context.Background(), h.push.store); err != nil || snap.Layer != nil {
			t.Fatalf("layer remained after owner delete: %+v err=%v", snap, err)
		}
	})

	t.Run("extension disable or uninstall via EventReport prunes singleton layer durably", func(t *testing.T) {
		h, _ := gradingHub(t)
		store := h.push.store.(*gradingStore)
		if err := h.push.setPushGrading(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")
		extB := extGradingPrincipal(assistantwire.AssistantID, "ext-b")

		if code, body := gradingLayerHTTP(t, h, extA, http.MethodPut, "/api/v1/push/grading/layer", `{"version":1,"actions":{"P2":"push"}}`); code != http.StatusOK {
			t.Fatalf("extA PUT: %d %s", code, body)
		}

		// An older manager report without PushGradingDeclared leaves the layer intact.
		rawOld, _ := json.Marshal(extwire.Report{EventsDeclared: true})
		busEvents[extwire.EventReport](h, nil, rawOld)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !h.waitPushGradingLayerReconciled(ctx) {
			t.Fatal("reconciler did not settle after legacy report")
		}
		if snap, err := readPushGradingSnapshot(ctx, store); err != nil || snap.Layer == nil || snap.Layer.Source != "extension:ext-a" {
			t.Fatalf("legacy report without PushGradingDeclared pruned layer: %+v err=%v", snap, err)
		}

		// Report that still lists ext-a keeps the layer intact.
		rawKeepA, _ := json.Marshal(extwire.Report{PushGradingDeclared: true, PushGradingExtensions: []string{"ext-a", "ext-b"}})
		busEvents[extwire.EventReport](h, nil, rawKeepA)
		if !h.waitPushGradingLayerReconciled(ctx) {
			t.Fatal("reconciler did not settle after keep report")
		}
		if snap, err := readPushGradingSnapshot(ctx, store); err != nil || snap.Layer == nil || snap.Layer.Source != "extension:ext-a" {
			t.Fatalf("active ext-a layer was pruned: %+v err=%v", snap, err)
		}

		// Report with ext-a disabled/uninstalled/scope-removed (only ext-b active) prunes ext-a durably.
		rawDropA, _ := json.Marshal(extwire.Report{PushGradingDeclared: true, PushGradingExtensions: []string{"ext-b"}})
		busEvents[extwire.EventReport](h, nil, rawDropA)
		if !h.waitPushGradingLayerReconciled(ctx) {
			t.Fatal("reconciled layer prune did not finish")
		}
		if snap, err := readPushGradingSnapshot(ctx, store); err != nil || snap.Layer != nil {
			t.Fatalf("ext-a layer still present after EventReport dropped ext-a: %+v err=%v", snap, err)
		}
		if !h.push.pushGradingOn() {
			t.Fatal("lifecycle prune mutated owner hub.ln_mode")
		}

		// Now ext-b can claim the singleton layer with revision 1, and dropping all extensions prunes it too.
		code, body := gradingLayerHTTP(t, h, extB, http.MethodPut, "/api/v1/push/grading/layer", `{"version":1,"actions":{"P1":"silence"}}`)
		if code != http.StatusOK || !strings.Contains(string(body), `"revision":1`) {
			t.Fatalf("extB claim after prune: %d %s", code, body)
		}
		rawDropAll, _ := json.Marshal(extwire.Report{PushGradingDeclared: true})
		busEvents[extwire.EventReport](h, nil, rawDropAll)
		if !h.waitPushGradingLayerReconciled(ctx) {
			t.Fatal("final layer prune did not finish")
		}
		if snap, err := readPushGradingSnapshot(ctx, store); err != nil || snap.Layer != nil {
			t.Fatalf("ext-b layer still present after empty report: %+v err=%v", snap, err)
		}
	})
}

func putVersionedLayer(t *testing.T, h *Hub, p *principal, body string) {
	t.Helper()
	code, raw := gradingLayerHTTP(t, h, p, http.MethodPut, "/api/v1/push/grading/layer", body)
	if code != http.StatusOK {
		t.Fatalf("PUT layer %s: %d %s", body, code, raw)
	}
}

func previewGradingHTTP(t *testing.T, h *Hub, p *principal, body string) pushGradingPreviewResponse {
	t.Helper()
	code, raw := gradingLayerHTTP(t, h, p, http.MethodPost, "/api/v1/push/grading/preview", body)
	if code != http.StatusOK {
		t.Fatalf("POST preview %s: %d %s", body, code, raw)
	}
	var out pushGradingPreviewResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode preview %s: %v", raw, err)
	}
	return out
}

// Combination safety: system alerts always pass, active owner attention modes are never weakened by extension layers,
// per-manager isolation holds, custom dedup windows work without suppressing P0, merge_held is respected, owner explicit
// enabled:false overrides extension layers, and cache invalidation makes layer/switch/prune writes take effect immediately.
func TestPushGradingLayerCombinesSafelyWithOwnerControls(t *testing.T) {
	t.Run("system alerts bypass owner base P0 hold and extension layers", func(t *testing.T) {
		h, f := gradingHub(t)
		attempts := gradingAttempts(h)
		baseCfg := parseGradingTestConfig(t, `{"version":1,"enabled":true,"actions":{"P0":"hold","P1":"silence","P2":"silence"}}`, false)
		if err := writePushGradingBase(context.Background(), h.push.store, baseCfg); err != nil {
			t.Fatal(err)
		}
		h.push.invalidatePushGradingCache()
		h.push.sendLocalized(func(i18n.Lang) map[string]any {
			return map[string]any{"aps": map[string]any{"alert": map[string]any{"title": "Computer joined"}, "sound": "default"}}
		}, "", nil)
		if *attempts != 1 {
			t.Fatalf("system alert attempts = %d, want 1", *attempts)
		}
		f.calls(t, 1)
	})

	t.Run("owner attention mode is never weakened by extension actions or notify_user promotion", func(t *testing.T) {
		h, f := gradingHub(t)
		attempts := gradingAttempts(h)
		extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")
		putVersionedLayer(t, h, extA, `{"version":1,"actions":{"P1":"push","P2":"push"},"notify_user":{"others":"P0"}}`)

		// Immersive focus on qa-plain: out-of-focus P1 and in-focus level-less non-main notify_user are STILL held by immersive mode.
		setAttention(h, assistantwire.AttentionImmersive, time.Hour, &assistantwire.AttentionFocus{Track: "qa-plain"})
		nOut := trackNotification(assistantwire.LevelP1, "qa-other", "", "out of focus")
		out, err := h.deliverPushFrom(assistantwire.AssistantID, nOut)
		if err != nil || heldOf(out) != assistantwire.AttentionReasonImmersiveHeld || *attempts != 0 {
			t.Fatalf("immersive out-of-focus: out=%+v err=%v attempts=%d", out, err, *attempts)
		}
		nInFocusNonMain := trackNotification("", "qa-plain", "", "in focus non-main")
		nInFocusNonMain.DeviceID, nInFocusNonMain.SessionID = "mac", "routine-conv"
		nInFocusNonMain.Asked = true
		out, err = h.deliverPushFrom(assistantwire.AssistantID, nInFocusNonMain)
		if err != nil || heldOf(out) != assistantwire.AttentionReasonImmersiveHeld || *attempts != 0 {
			t.Fatalf("immersive in-focus non-main promoted by ext: out=%+v err=%v attempts=%d", out, err, *attempts)
		}

		// Quiet mode: even an allowed P0 is STILL hushed (no sound, passive), and P1 stays quiet-held despite layer actions.P1=push.
		setAttention(h, assistantwire.AttentionQuiet, time.Hour, nil)
		out, err = h.deliverPushFrom(assistantwire.AssistantID, gradingNotification(assistantwire.LevelP1, "q:p1", "quiet p1"))
		if err != nil || heldOf(out) != assistantwire.AttentionReasonQuietHeld {
			t.Fatalf("quiet P1 weakened by layer: out=%+v err=%v", out, err)
		}
		if err := h.deliverPush(gradingNotification(assistantwire.LevelP0, "q:p0", "quiet p0")); err != nil {
			t.Fatal(err)
		}
		calls := f.calls(t, 1)
		if aps := apsOf(t, calls[0]); aps["sound"] != nil || aps["interruption-level"] != "passive" {
			t.Fatalf("quiet mode P0 was not hushed: %+v", aps)
		}
	})

	t.Run("per-manager isolation, owner explicit off, custom dedup window, and immediate cache invalidation", func(t *testing.T) {
		h, f := gradingHub(t)
		attempts := gradingAttempts(h)
		extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")

		// Claim layer on assistant with 20h dedup, P1=push, P2=hold (hub.ln_mode stays off).
		putVersionedLayer(t, h, extA, `{"version":1,"actions":{"P1":"push","P2":"hold"},"dedup_window":"20h"}`)
		if h.push.pushGradingOn() {
			t.Fatal("extension layer mutated hub.ln_mode")
		}

		// Other manager (manager-b) is unaffected: P2 still pushes.
		outB, err := h.deliverPushFrom("manager-b", gradingNotification(assistantwire.LevelP2, "mb:1", "manager-b p2"))
		if err != nil || heldOf(outB) != "" || *attempts != 1 {
			t.Fatalf("manager-b affected by assistant layer: out=%+v err=%v attempts=%d", outB, err, *attempts)
		}

		// Assistant applies the layer immediately without waiting for TTL: P1 pushes, P2 is held (ln_p1_held),
		// and a 15h repeat after P0 is deduped under the 20h window while P0 still interrupts.
		base := h.push.now()
		outP0, err := h.deliverPushFrom(assistantwire.AssistantID, gradingNotification(assistantwire.LevelP0, "pr:dedup", "first p0"))
		if err != nil || heldOf(outP0) != "" {
			t.Fatalf("first P0: out=%+v err=%v", outP0, err)
		}
		moveGradingClock(h, base.Add(15*time.Hour))
		outP2Repeat, err := h.deliverPushFrom(assistantwire.AssistantID, gradingNotification(assistantwire.LevelP2, "pr:dedup", "repeat p2"))
		if err != nil || heldOf(outP2Repeat) != interceptReasonDedup {
			t.Fatalf("15h repeat under 20h layer: out=%+v err=%v, want %s", outP2Repeat, err, interceptReasonDedup)
		}
		outP2Fresh, err := h.deliverPushFrom(assistantwire.AssistantID, gradingNotification(assistantwire.LevelP2, "pr:fresh", "fresh p2"))
		if err != nil || heldOf(outP2Fresh) != interceptReasonP1 {
			t.Fatalf("fresh P2 under actions.P2=hold: out=%+v err=%v, want %s", outP2Fresh, err, interceptReasonP1)
		}
		outP0Repeat, err := h.deliverPushFrom(assistantwire.AssistantID, gradingNotification(assistantwire.LevelP0, "pr:dedup", "second p0"))
		if err != nil || heldOf(outP0Repeat) != "" {
			t.Fatalf("P0 under 20h dedup: out=%+v err=%v, want delivered", outP0Repeat, err)
		}

		// Deleting the layer invalidates the cache immediately (same timestamp) and restores default delivery.
		if code, raw := gradingLayerHTTP(t, h, extA, http.MethodDelete, "/api/v1/push/grading/layer", ""); code != http.StatusOK {
			t.Fatalf("DELETE layer: %d %s", code, raw)
		}
		outAfterDel, err := h.deliverPushFrom(assistantwire.AssistantID, gradingNotification(assistantwire.LevelP2, "pr:after-del", "after delete"))
		if err != nil || heldOf(outAfterDel) != "" {
			t.Fatalf("after DELETE layer: out=%+v err=%v, want immediate restore", outAfterDel, err)
		}

		// Owner explicit hub.push_grading enabled=false overrides an active extension layer.
		putVersionedLayer(t, h, extA, `{"version":1,"actions":{"P1":"silence","P2":"silence"}}`)
		baseOff := parseGradingTestConfig(t, `{"version":1,"enabled":false}`, false)
		if err := writePushGradingBase(context.Background(), h.push.store, baseOff); err != nil {
			t.Fatal(err)
		}
		h.push.invalidatePushGradingCache()
		outBaseOff, err := h.deliverPushFrom(assistantwire.AssistantID, gradingNotification(assistantwire.LevelP2, "pr:base-off", "base off"))
		if err != nil || heldOf(outBaseOff) != "" {
			t.Fatalf("owner base enabled=false did not override layer: out=%+v err=%v", outBaseOff, err)
		}
		f.calls(t, 5)
	})

	t.Run("merge_held false keeps same-minute P1 intercepts separate", func(t *testing.T) {
		h, _ := gradingHub(t)
		extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")
		putVersionedLayer(t, h, extA, `{"version":1,"merge_held":false}`)
		for _, title := range []string{"one", "two"} {
			if err := h.deliverPush(gradingNotification(assistantwire.LevelP1, "k:"+title, title)); err != nil {
				t.Fatal(err)
			}
		}
		if view := h.push.heldPushesView("", 0); len(view) != 2 {
			t.Fatalf("merge_held=false rows = %d, want 2: %+v", len(view), view)
		}
	})

	t.Run("HTTP notify obeys per-manager layer and returns live reason", func(t *testing.T) {
		h, _ := gradingHub(t)
		extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")
		putVersionedLayer(t, h, extA, `{"version":1,"actions":{"P1":"silence"}}`)
		res, err := h.deliverNotify(notifyRequest{Title: "Ping", Body: "hello", Level: levelActive, PushLevel: assistantwire.LevelP1}, "", "noisy-bot", assistantwire.AssistantID)
		if err != nil || res["sent"] != false || res["held"] != true || res["reason"] != interceptReasonP2 {
			t.Fatalf("deliverNotify = %+v, err=%v; want held %s", res, err, interceptReasonP2)
		}
	})
}

// POST /api/v1/push/grading/preview returns neutral evaluator outcomes ("held", "silenced", "deduped"),
// explicit live_reason adapter output, and config attribution for persisted, draft, and layer:null previews
// with ZERO side effects on any notification, dedup, intercept, delivered-push, layer, WebSocket, or store state.
func TestPushGradingPreviewMatchesDeliveryAndWritesZeroState(t *testing.T) {
	h, f := gradingHub(t)
	h.clients = map[*client]struct{}{}
	mainGradingProfile(h)
	store := h.push.store.(*gradingStore)
	wsClient := &client{device: "web-preview", out: make(chan []byte, 8), meta: clientMeta{features: map[string]bool{"ws_notify": true}}}
	addTestClient(h, wsClient)
	extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")

	// Seed one real P0 push (with owner switch on) so dedup state & sent counts are non-empty before previewing.
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := h.deliverPush(gradingNotification(assistantwire.LevelP0, "pr:existing", "real p0")); err != nil {
		t.Fatal(err)
	}
	f.calls(t, 1)
	<-wsClient.out
	if !h.push.waitInterceptsSaved(2 * time.Second) {
		t.Fatal("initial push state did not save")
	}
	putVersionedLayer(t, h, extA, `{"version":1,"actions":{"P1":"hold","P2":"push"},"dedup_window":"6h"}`)

	snapshotState := func() (intercepts []heldPushRecord, stats []pushDayStats, lastAlert map[string]map[string]time.Time, storeVals map[string][]byte, sentRows int) {
		intercepts = h.push.heldPushesView("", 0)
		stats = h.push.pushDailyStats(h.push.now().Add(-24*time.Hour), "")
		h.push.gradingMu.Lock()
		lastAlert = make(map[string]map[string]time.Time, len(h.push.gradingLastAlert))
		for m, byKey := range h.push.gradingLastAlert {
			lastAlert[m] = maps.Clone(byKey)
		}
		h.push.gradingMu.Unlock()
		store.mu.Lock()
		storeVals = make(map[string][]byte, len(store.vals))
		for k, v := range store.vals {
			storeVals[k] = append([]byte(nil), v...)
		}
		store.mu.Unlock()
		sentRows = len(store.sentTable())
		return
	}
	beforeIntercepts, beforeStats, beforeLastAlert, beforeStore, beforeSentRows := snapshotState()
	attempts := gradingAttempts(h)

	// 1. Persisted layer sets actions.P2=push -> allowed with layer attribution.
	d1 := previewGradingHTTP(t, h, extA, `{"notification":{"kind":"assistant","level":"P2","related_key":"bg:1"}}`)
	if !d1.Allow || d1.Held || d1.Action != "push" || d1.Reason != "" || d1.LiveReason != "" ||
		d1.EffectiveLevel != assistantwire.LevelP2 || d1.Source != "extension:ext-a" ||
		d1.LayerSource != "extension:ext-a" || d1.DedupWindowSeconds != int((6*time.Hour).Seconds()) {
		t.Fatalf("preview 1 (persisted P2=push) = %+v", d1)
	}

	// 2. Fresh P1 -> neutral reason "held", live_reason "ln_p1_held".
	d2 := previewGradingHTTP(t, h, extA, `{"notification":{"kind":"assistant","level":"P1","related_key":"regular:1"}}`)
	if d2.Allow || !d2.Held || d2.Action != "hold" || d2.Reason != pushGradingOutcomeHeld ||
		d2.LiveReason != interceptReasonP1 || d2.Source != "extension:ext-a" {
		t.Fatalf("preview 2 (fresh P1) = %+v", d2)
	}

	// 3. Existing P0 key -> neutral reason "deduped", live_reason "ln_dedup_12h", DedupHit=true.
	d3 := previewGradingHTTP(t, h, extA, `{"notification":{"kind":"assistant","level":"P1","related_key":"pr:existing"}}`)
	if d3.Allow || !d3.Held || !d3.DedupHit || d3.Reason != pushGradingOutcomeDeduped || d3.LiveReason != interceptReasonDedup {
		t.Fatalf("preview 3 (dedup hit) = %+v", d3)
	}

	// 4. Previewing a fresh P0 key returns allow=true AND does NOT record it into gradingLastAlert.
	d4 := previewGradingHTTP(t, h, extA, `{"notification":{"kind":"assistant","level":"P0","related_key":"pr:never-sent"}}`)
	if !d4.Allow || d4.EffectiveLevel != assistantwire.LevelP0 {
		t.Fatalf("preview 4 (fresh P0) = %+v", d4)
	}

	// 5. Main conversation level-less notify_user promotes to P0 in preview just like live delivery.
	d5 := previewGradingHTTP(t, h, extA, `{"notification":{"kind":"assistant","machine_id":"mac","session_id":"main-conv"}}`)
	if !d5.Allow || d5.EffectiveLevel != assistantwire.LevelP0 {
		t.Fatalf("preview 5 (main conversation) = %+v", d5)
	}

	// 6. Explicit "layer": null previews removing the persisted layer (owner switch on -> P2 silenced, 12h dedup).
	d6 := previewGradingHTTP(t, h, extA, `{"notification":{"kind":"assistant","level":"P2","related_key":"bg:1"},"layer":null}`)
	if d6.Allow || !d6.Held || d6.Action != "silence" || d6.Reason != pushGradingOutcomeSilenced ||
		d6.LiveReason != interceptReasonP2 || d6.LayerSource != "" || d6.DedupWindowSeconds != int((12*time.Hour).Seconds()) {
		t.Fatalf("preview 6 (layer:null) = %+v", d6)
	}

	// 7. Draft versioned layer overrides persisted layer for this preview only (without saving it).
	d7 := previewGradingHTTP(t, h, extA, `{"notification":{"kind":"assistant","level":"P1","related_key":"draft:1"},"layer":{"version":1,"actions":{"P1":"silence"}}}`)
	if d7.Allow || d7.Action != "silence" || d7.Reason != pushGradingOutcomeSilenced || d7.LiveReason != interceptReasonP2 {
		t.Fatalf("preview 7 (draft P1=silence) = %+v", d7)
	}

	// 8. Owner preview on another manager (manager-b) ignores assistant's singleton layer.
	d8 := previewGradingHTTP(t, h, nil, `{"notification":{"kind":"assistant","level":"P2","manager":"manager-b","related_key":"bg:1"}}`)
	if d8.Allow || d8.Reason != pushGradingOutcomeSilenced || d8.LayerSource != "" {
		t.Fatalf("preview 8 (manager-b isolation) = %+v", d8)
	}

	// Verify ZERO state mutation after all 8 preview calls.
	afterIntercepts, afterStats, afterLastAlert, afterStore, afterSentRows := snapshotState()
	if *attempts != 0 {
		t.Fatalf("preview triggered %d APNs attempts, want 0", *attempts)
	}
	f.calls(t, 0)
	select {
	case msg := <-wsClient.out:
		t.Fatalf("preview emitted WebSocket frame: %s", msg)
	default:
	}
	if !reflect.DeepEqual(beforeIntercepts, afterIntercepts) {
		t.Fatalf("preview mutated intercepts: before=%+v after=%+v", beforeIntercepts, afterIntercepts)
	}
	if !reflect.DeepEqual(beforeStats, afterStats) {
		t.Fatalf("preview mutated daily stats: before=%+v after=%+v", beforeStats, afterStats)
	}
	if !reflect.DeepEqual(beforeLastAlert, afterLastAlert) {
		t.Fatalf("preview mutated gradingLastAlert: before=%+v after=%+v", beforeLastAlert, afterLastAlert)
	}
	if !reflect.DeepEqual(beforeStore, afterStore) || beforeSentRows != afterSentRows {
		t.Fatalf("preview wrote to store: sentRows %d→%d, store changed=%v", beforeSentRows, afterSentRows, !reflect.DeepEqual(beforeStore, afterStore))
	}
}

// Same-input semantic parity: for identical notifications across owner switch, versioned base,
// manager-scoped extension layers, dedup windows, active attention modes, and main/non-main
// notify_user derivation, POST /api/v1/push/grading/preview and live deliverPushFrom agree 1:1.
func TestPushGradingPreviewLiveSemanticParity(t *testing.T) {
	type parityStep struct {
		name         string
		manager      string
		notification assistantwire.Notification
		wantAllow    bool
		wantNeutral  string
		wantLive     string
	}
	runParity := func(t *testing.T, setup func(*testing.T, *Hub), steps []parityStep) {
		t.Helper()
		h, _ := gradingHub(t)
		mainGradingProfile(h)
		setup(t, h)
		for _, st := range steps {
			mgr := cmp.Or(st.manager, assistantwire.AssistantID)
			asked := st.notification.Asked
			payload, err := json.Marshal(map[string]any{
				"notification": pushGradingPreviewNotification{
					Kind:       st.notification.Kind,
					Level:      st.notification.Level,
					RelatedKey: st.notification.RelatedKey,
					MachineID:  st.notification.DeviceID,
					SessionID:  st.notification.SessionID,
					Track:      st.notification.Track,
					Depth:      st.notification.Depth,
					Manager:    mgr,
					Asked:      &asked,
					Title:      st.notification.Title,
					Body:       st.notification.Body,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			prev := previewGradingHTTP(t, h, nil, string(payload))
			liveOut, err := h.deliverPushFrom(mgr, st.notification)
			if err != nil {
				t.Fatalf("%s: live deliverPushFrom error: %v", st.name, err)
			}
			liveHeld := heldOf(liveOut)
			liveAllow := liveHeld == ""
			if prev.Allow != liveAllow || prev.LiveReason != liveHeld {
				t.Fatalf("%s: preview/live mismatch: preview(allow=%v reason=%q live=%q) vs live(allow=%v held=%q)",
					st.name, prev.Allow, prev.Reason, prev.LiveReason, liveAllow, liveHeld)
			}
			if prev.Allow != st.wantAllow || prev.Reason != st.wantNeutral || prev.LiveReason != st.wantLive {
				t.Fatalf("%s: got allow=%v neutral=%q live=%q, want allow=%v neutral=%q live=%q",
					st.name, prev.Allow, prev.Reason, prev.LiveReason, st.wantAllow, st.wantNeutral, st.wantLive)
			}
		}
	}

	t.Run("default off allows every level", func(t *testing.T) {
		runParity(t, func(*testing.T, *Hub) {}, []parityStep{
			{name: "P0", notification: gradingNotification(assistantwire.LevelP0, "k0", "p0"), wantAllow: true},
			{name: "P1", notification: gradingNotification(assistantwire.LevelP1, "k1", "p1"), wantAllow: true},
			{name: "P2", notification: gradingNotification(assistantwire.LevelP2, "k2", "p2"), wantAllow: true},
		})
	})

	t.Run("owner switch on with P0 dedup and main promotion", func(t *testing.T) {
		runParity(t, func(t *testing.T, h *Hub) {
			if err := h.push.setPushGrading(context.Background(), true); err != nil {
				t.Fatal(err)
			}
		}, []parityStep{
			{name: "P0 pushes and records dedup", notification: gradingNotification(assistantwire.LevelP0, "pr:1", "p0"), wantAllow: true},
			{name: "P1 repeat deduped", notification: gradingNotification(assistantwire.LevelP1, "pr:1", "p1 rep"), wantAllow: false, wantNeutral: pushGradingOutcomeDeduped, wantLive: interceptReasonDedup},
			{name: "P1 fresh held", notification: gradingNotification(assistantwire.LevelP1, "pr:2", "p1 fresh"), wantAllow: false, wantNeutral: pushGradingOutcomeHeld, wantLive: interceptReasonP1},
			{name: "P2 fresh silenced", notification: gradingNotification(assistantwire.LevelP2, "pr:3", "p2 fresh"), wantAllow: false, wantNeutral: pushGradingOutcomeSilenced, wantLive: interceptReasonP2},
			{name: "main conv level-less pushes", notification: gradingNotifyUserPush("main-conv", ""), wantAllow: true},
			{name: "non-main level-less held", notification: gradingNotifyUserPush("routine-conv", ""), wantAllow: false, wantNeutral: pushGradingOutcomeHeld, wantLive: interceptReasonP1},
		})
	})

	t.Run("extension layer on assistant isolated from manager-b without touching hub.ln_mode", func(t *testing.T) {
		runParity(t, func(t *testing.T, h *Hub) {
			extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")
			putVersionedLayer(t, h, extA, `{"version":1,"actions":{"P1":"push","P2":"hold"},"notify_user":{"others_max":"P1"}}`)
		}, []parityStep{
			{name: "assistant P1 pushes", manager: assistantwire.AssistantID, notification: gradingNotification(assistantwire.LevelP1, "a:1", "a p1"), wantAllow: true},
			{name: "assistant P2 held", manager: assistantwire.AssistantID, notification: gradingNotification(assistantwire.LevelP2, "a:2", "a p2"), wantAllow: false, wantNeutral: pushGradingOutcomeHeld, wantLive: interceptReasonP1},
			{name: "assistant non-main explicit P0 capped to P1 and pushed", manager: assistantwire.AssistantID, notification: gradingNotifyUserPush("routine-conv", assistantwire.LevelP0), wantAllow: true},
			{name: "manager-b P2 unaffected", manager: "manager-b", notification: gradingNotification(assistantwire.LevelP2, "b:1", "b p2"), wantAllow: true},
		})
	})

	t.Run("owner base shallow merge with extension layer", func(t *testing.T) {
		runParity(t, func(t *testing.T, h *Hub) {
			base := parseGradingTestConfig(t, `{"version":1,"enabled":true,"actions":{"P1":"push"}}`, false)
			if err := writePushGradingBase(context.Background(), h.push.store, base); err != nil {
				t.Fatal(err)
			}
			extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")
			putVersionedLayer(t, h, extA, `{"version":1,"actions":{"P2":"push"}}`)
		}, []parityStep{
			{name: "assistant P1 from base", manager: assistantwire.AssistantID, notification: gradingNotification(assistantwire.LevelP1, "m:1", "p1"), wantAllow: true},
			{name: "assistant P2 from layer", manager: assistantwire.AssistantID, notification: gradingNotification(assistantwire.LevelP2, "m:2", "p2"), wantAllow: true},
			{name: "manager-b P1 from base", manager: "manager-b", notification: gradingNotification(assistantwire.LevelP1, "m:3", "p1"), wantAllow: true},
			{name: "manager-b P2 default silence", manager: "manager-b", notification: gradingNotification(assistantwire.LevelP2, "m:4", "p2"), wantAllow: false, wantNeutral: pushGradingOutcomeSilenced, wantLive: interceptReasonP2},
		})
	})

	t.Run("active owner attention mode dominates extension layer", func(t *testing.T) {
		runParity(t, func(t *testing.T, h *Hub) {
			extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")
			putVersionedLayer(t, h, extA, `{"version":1,"actions":{"P1":"push","P2":"push"},"notify_user":{"others":"P0"}}`)
			setAttention(h, assistantwire.AttentionImmersive, time.Hour, &assistantwire.AttentionFocus{Track: "qa-plain"})
		}, []parityStep{
			{name: "immersive in-focus P0 allowed", notification: trackNotification(assistantwire.LevelP0, "qa-plain", "", "in focus"), wantAllow: true},
			{name: "immersive out-of-focus P0 held", notification: trackNotification(assistantwire.LevelP0, "qa-other", "", "out"), wantAllow: false, wantNeutral: assistantwire.AttentionReasonImmersiveHeld, wantLive: assistantwire.AttentionReasonImmersiveHeld},
			{name: "immersive P1 held despite layer", notification: trackNotification(assistantwire.LevelP1, "qa-plain", "", "p1"), wantAllow: false, wantNeutral: assistantwire.AttentionReasonImmersiveHeld, wantLive: assistantwire.AttentionReasonImmersiveHeld},
			{name: "immersive P2 silent despite layer", notification: trackNotification(assistantwire.LevelP2, "qa-plain", "", "p2"), wantAllow: false, wantNeutral: assistantwire.AttentionReasonSilent, wantLive: assistantwire.AttentionReasonSilent},
		})
	})

	t.Run("P1-5 per-manager dedup isolation and 48h retention under mixed windows", func(t *testing.T) {
		h, _ := gradingHub(t)
		if err := h.push.setPushGrading(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")
		putVersionedLayer(t, h, extA, `{"version":1,"actions":{"P1":"push"},"dedup_window":"36h"}`)

		base := h.push.now()
		// Assistant pushes P1 (allowed by layer) with key "shared:1" -> records dedup ONLY for assistant.
		if allow, _ := h.push.applyPushGrading(pushPolicy{kind: assistantwire.NotifyPush, manager: assistantwire.AssistantID, level: assistantwire.LevelP1, relatedKey: "shared:1"}); !allow {
			t.Fatal("assistant P1 should push")
		}
		// Manager-b must NOT see assistant's dedup entry (neither in preview nor live).
		prevB := previewGradingHTTP(t, h, nil, `{"notification":{"kind":"assistant","level":"P2","manager":"manager-b","related_key":"shared:1"}}`)
		if prevB.DedupHit || prevB.Reason != pushGradingOutcomeSilenced {
			t.Fatalf("manager-b saw assistant's dedup entry: %+v", prevB)
		}
		// 13h later (>12h manager-b window, <36h assistant window), manager-b pushes P0 "other:1".
		// That must NOT evict assistant's "shared:1" entry!
		moveGradingClock(h, base.Add(13*time.Hour))
		if allow, _ := h.push.applyPushGrading(pushPolicy{kind: assistantwire.NotifyPush, manager: "manager-b", level: assistantwire.LevelP0, relatedKey: "other:1"}); !allow {
			t.Fatal("manager-b P0 should push")
		}
		prevA := previewGradingHTTP(t, h, extA, `{"notification":{"kind":"assistant","level":"P2","related_key":"shared:1"}}`)
		if !prevA.DedupHit || prevA.Reason != pushGradingOutcomeDeduped {
			t.Fatalf("assistant 36h dedup entry was prematurely evicted by manager-b push: %+v", prevA)
		}
	})

	t.Run("P1-6 preview enforces machine/session ownership and scopes mainConv to target manager", func(t *testing.T) {
		h, _ := gradingHub(t)
		mainGradingProfile(h)
		if err := h.push.setPushGrading(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		// Extension on manager-b probing assistant's machine/session gets 403 other_manager.
		extOtherMgr := extGradingPrincipal("manager-b", "ext-b")
		code, raw := gradingLayerHTTP(t, h, extOtherMgr, http.MethodPost, "/api/v1/push/grading/preview",
			`{"notification":{"kind":"assistant","machine_id":"mac","session_id":"main-conv"}}`)
		var errBody map[string]any
		_ = json.Unmarshal(raw, &errBody)
		if code != http.StatusForbidden || errBody["code"] != codeOtherManager {
			t.Fatalf("foreign machine/session preview: got %d %s, want 403 %s", code, raw, codeOtherManager)
		}
		// Owner preview targeting manager-b with assistant's main-conv does NOT promote to P0.
		prevOther := previewGradingHTTP(t, h, nil,
			`{"notification":{"kind":"assistant","manager":"manager-b","machine_id":"mac","session_id":"main-conv"}}`)
		if prevOther.Allow || prevOther.EffectiveLevel != assistantwire.LevelP1 {
			t.Fatalf("manager-b preview promoted assistant's main conversation: %+v", prevOther)
		}
	})
}

// Strict input validation for PUT /api/v1/push/grading/layer and POST /api/v1/push/grading/preview.
func TestPushGradingLayerValidation(t *testing.T) {
	h, _ := gradingHub(t)
	extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")

	badLayers := map[string]string{
		"empty object":          `{}`,
		"not an object":         `[]`,
		"unsupported version":   `{"version":2}`,
		"unknown field":         `{"version":1,"extra":1}`,
		"legacy field":          `{"grading_enabled":true}`,
		"owner-only enabled":    `{"version":1,"enabled":true}`,
		"owner-only null":       `{"version":1,"enabled":null}`,
		"extension holds P0":    `{"version":1,"actions":{"P0":"hold"}}`,
		"extension silences P0": `{"version":1,"actions":{"P0":"silence"}}`,
		"duplicate key":         `{"version":1,"version":1}`,
		"case colliding key":    `{"version":1,"Version":1}`,
		"case colliding action": `{"version":1,"actions":{"P1":"hold","p1":"push"}}`,
		"trailing json":         `{"version":1}{"version":1}`,
		"bad action":            `{"version":1,"actions":{"P1":"drop"}}`,
		"dedup negative":        `{"version":1,"dedup_window":"-1s"}`,
		"dedup too large":       `{"version":1,"dedup_window":"48h1s"}`,
		"dedup non-duration":    `{"version":1,"dedup_window":"12"}`,
		"bad notify level":      `{"version":1,"notify_user":{"main":"P9"}}`,
		"duplicate exemption":   `{"version":1,"dedup_exempt_levels":["P0","P0"]}`,
		"bad escalate reason":   `{"version":1,"cards":{"escalate_reasons":["other"]}}`,
		"bad report level":      `{"version":1,"cards":{"report_level":"urgent"}}`,
	}
	for name, body := range badLayers {
		code, raw := gradingLayerHTTP(t, h, extA, http.MethodPut, "/api/v1/push/grading/layer", body)
		if code != http.StatusBadRequest {
			t.Errorf("PUT %s: got %d %s, want 400", name, code, raw)
		}
	}

	// Oversized request body (>16KiB) returns 413.
	huge := `{"version":1,"dedup_window":"` + strings.Repeat("1", pushGradingLayerBodyLimit+128) + `"}`
	if code, raw := gradingLayerHTTP(t, h, extA, http.MethodPut, "/api/v1/push/grading/layer", huge); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("PUT oversized: got %d %s, want 413", code, raw)
	}

	// Bad preview inputs.
	badPreviews := map[string]string{
		"missing notification": `{}`,
		"null notification":    `{"notification":null}`,
		"unknown field":        `{"notification":{"level":"P1"},"unknown":true}`,
		"unknown notif field":  `{"notification":{"level":"P1","extra":1}}`,
		"bad kind":             `{"notification":{"kind":"bogus"}}`,
		"bad level":            `{"notification":{"level":"P9"}}`,
		"bad depth":            `{"notification":{"depth":"huge"}}`,
		"legacy draft layer":   `{"notification":{"level":"P1"},"layer":{"dedup_window_seconds":1}}`,
		"draft bad version":    `{"notification":{"level":"P1"},"layer":{"version":2}}`,
		"draft owner enabled":  `{"notification":{"level":"P1"},"layer":{"version":1,"enabled":true}}`,
		"draft holds P0":       `{"notification":{"level":"P1"},"layer":{"version":1,"actions":{"P0":"hold"}}}`,
		"draft foreign field":  `{"notification":{"level":"P1"},"layer":{"version":1,"extension":"ext-a"}}`,
		"control char key":     `{"notification":{"related_key":"a\u0001b"}}`,
		"duplicate preview":    `{"notification":{"level":"P1"},"notification":{"level":"P2"}}`,
		"case colliding field": `{"notification":{"level":"P1"},"Notification":{"level":"P2"}}`,
	}
	for name, body := range badPreviews {
		code, raw := gradingLayerHTTP(t, h, extA, http.MethodPost, "/api/v1/push/grading/preview", body)
		if code != http.StatusBadRequest {
			t.Errorf("preview %s: got %d %s, want 400", name, code, raw)
		}
	}

	// Bad DELETE revision query parameter returns 400.
	for _, badRev := range []string{"0", "-1", "abc"} {
		if code, raw := gradingLayerHTTP(t, h, extA, http.MethodDelete, "/api/v1/push/grading/layer?revision="+badRev, ""); code != http.StatusBadRequest {
			t.Fatalf("DELETE revision=%s: got %d %s, want 400", badRev, code, raw)
		}
	}
}

// Concurrent PUT/GET/DELETE/preview/applyPushGrading/reconcilePushGradingLayer must be race-free.
func TestPushGradingLayerConcurrentAccess(t *testing.T) {
	h, _ := gradingHub(t)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := range 6 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			extName := fmt.Sprintf("ext-%d", id%3)
			p := extGradingPrincipal(assistantwire.AssistantID, extName)
			for j := range 25 {
				switch j % 5 {
				case 0:
					code, raw := gradingLayerHTTP(t, h, p, http.MethodPut, "/api/v1/push/grading/layer",
						`{"version":1,"actions":{"P1":"hold"}}`)
					if code != http.StatusOK && code != http.StatusConflict {
						errs <- fmt.Errorf("PUT %s: %d %s", extName, code, bytes.TrimSpace(raw))
						return
					}
				case 1:
					code, raw := gradingLayerHTTP(t, h, p, http.MethodGet, "/api/v1/push/grading/layer", "")
					if code != http.StatusOK {
						errs <- fmt.Errorf("GET %s: %d %s", extName, code, bytes.TrimSpace(raw))
						return
					}
				case 2:
					code, raw := gradingLayerHTTP(t, h, p, http.MethodPost, "/api/v1/push/grading/preview",
						`{"notification":{"kind":"assistant","level":"P1","related_key":"k:1"}}`)
					if code != http.StatusOK {
						errs <- fmt.Errorf("preview %s: %d %s", extName, code, bytes.TrimSpace(raw))
						return
					}
				case 3:
					h.push.applyPushGrading(pushPolicy{
						kind: assistantwire.NotifyPush, manager: assistantwire.AssistantID, level: assistantwire.LevelP1, relatedKey: "k:1",
					})
				case 4:
					if id%2 == 0 {
						code, raw := gradingLayerHTTP(t, h, p, http.MethodDelete, "/api/v1/push/grading/layer", "")
						if code != http.StatusOK {
							errs <- fmt.Errorf("DELETE %s: %d %s", extName, code, bytes.TrimSpace(raw))
							return
						}
					} else {
						h.reconcilePushGradingLayer(assistantwire.AssistantID, []string{"ext-0", "ext-2"})
					}
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !h.waitPushGradingLayerReconciled(ctx) {
		t.Fatal("background layer reconciliation did not settle")
	}
}

// Per-manager Hello.QuietPush and Hello.PushGradingCards reflect effectiveGradingFor (covering both hub.ln_mode and
// extension layers with manager isolation), and writing, deleting, or pruning a layer refreshes connected managers via
// manager.updated without mutating hub.ln_mode.
func TestHelloQuietPushAndCardsReflectExtensionLayer(t *testing.T) {
	h := newTestHub(t)
	h.push.store = newGradingStore()
	a := connectAssistant(t, h)
	ctx := context.Background()
	extA := extGradingPrincipal(assistantwire.AssistantID, "ext-a")

	// Initially off: neither assistant nor manager-b has QuietPush or PushGradingCards.
	initial := h.assistantHelloFor(ctx, assistantwire.AssistantID)
	if initial.QuietPush || initial.PushGradingCards != nil {
		t.Fatalf("initial assistant hello = %+v, want QuietPush=false and PushGradingCards=nil", initial)
	}
	if other := h.assistantHelloFor(ctx, "manager-b"); other.QuietPush || other.PushGradingCards != nil {
		t.Fatalf("initial manager-b hello = %+v, want QuietPush=false and PushGradingCards=nil", other)
	}

	// Extension writes a layer with custom cards.* while owner hub.ln_mode stays off.
	putVersionedLayer(t, h, extA, `{"version":1,"cards":{"supervisor_hold":false,"escalate_reasons":["irreversible"],"hold_conclusions":false,"hold_acceptance":true,"decision_level":"P1","report_level":"P2"}}`)
	if h.push.pushGradingOn() {
		t.Fatal("extension layer mutated owner hub.ln_mode")
	}

	wantCards := &assistantwire.PushGradingCards{
		SupervisorHold:  false,
		EscalateReasons: []string{"irreversible"},
		HoldConclusions: false,
		HoldAcceptance:  true,
		DecisionLevel:   assistantwire.LevelP1,
		ReportLevel:     assistantwire.LevelP2,
	}
	// Connected assistant receives the layer update over manager.updated.
	eventually(t, 5*time.Second, "assistant receives layer QuietPush and PushGradingCards", func() bool {
		hello, _ := a.bh.Hello(ctx)
		return hello.QuietPush && reflect.DeepEqual(hello.PushGradingCards, wantCards)
	})
	// Fresh handshake hello for assistant matches, while manager-b does NOT inherit assistant's extension layer.
	gotAssistant := h.assistantHelloFor(ctx, assistantwire.AssistantID)
	if !gotAssistant.QuietPush || !reflect.DeepEqual(gotAssistant.PushGradingCards, wantCards) {
		t.Fatalf("assistant hello after layer PUT: QuietPush=%v cards=%+v, want true %+v", gotAssistant.QuietPush, gotAssistant.PushGradingCards, wantCards)
	}
	gotOther := h.assistantHelloFor(ctx, "manager-b")
	if gotOther.QuietPush || gotOther.PushGradingCards != nil {
		t.Fatalf("manager-b hello inherited assistant layer: QuietPush=%v cards=%+v", gotOther.QuietPush, gotOther.PushGradingCards)
	}

	// Deleting the layer clears QuietPush and PushGradingCards and refreshes the connected manager.
	if code, raw := gradingLayerHTTP(t, h, extA, http.MethodDelete, "/api/v1/push/grading/layer", ""); code != http.StatusOK || !strings.Contains(string(raw), `"removed":true`) {
		t.Fatalf("DELETE layer: %d %s", code, raw)
	}
	eventually(t, 5*time.Second, "assistant receives cleared QuietPush after DELETE", func() bool {
		hello, _ := a.bh.Hello(ctx)
		return !hello.QuietPush && hello.PushGradingCards == nil
	})
	afterDelete := h.assistantHelloFor(ctx, assistantwire.AssistantID)
	if afterDelete.QuietPush || afterDelete.PushGradingCards != nil {
		t.Fatalf("assistant hello after DELETE: QuietPush=%v cards=%+v, want false nil", afterDelete.QuietPush, afterDelete.PushGradingCards)
	}

	// Re-claim layer, then uninstall/revoke via EventReport prune: connected manager refreshes back to off.
	putVersionedLayer(t, h, extA, `{"version":1,"cards":{"report_level":"P2"}}`)
	eventually(t, 5*time.Second, "assistant receives re-claimed layer", func() bool {
		hello, _ := a.bh.Hello(ctx)
		return hello.QuietPush && hello.PushGradingCards != nil && hello.PushGradingCards.ReportLevel == assistantwire.LevelP2
	})
	rawDrop, _ := json.Marshal(extwire.Report{PushGradingDeclared: true})
	busEvents[extwire.EventReport](h, nil, rawDrop)
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !h.waitPushGradingLayerReconciled(waitCtx) {
		t.Fatal("reconcile prune did not settle")
	}
	eventually(t, 5*time.Second, "assistant receives cleared QuietPush after lifecycle prune", func() bool {
		hello, _ := a.bh.Hello(ctx)
		return !hello.QuietPush && hello.PushGradingCards == nil
	})
}
