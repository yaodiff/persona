package assistant

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func TestIntegrationsV2SettingsLifecycle(t *testing.T) {
	s, _, _ := briefService(t, "Verify unified integrations toggle")
	if out, bad := tool(t, s, "todo_create", map[string]any{"title": "Existing workflow item"}); bad {
		t.Fatal(out)
	}
	item, ok := todoByTitle(s, "Existing workflow item")
	if !ok {
		t.Fatal("missing workflow item")
	}

	put := func(manager, role, body string, status int) string {
		t.Helper()
		w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", manager, role, body)
		if w.Code != status {
			t.Fatalf("PUT %s (manager=%q role=%q): got %d, want %d: %s", body, manager, role, w.Code, status, w.Body.String())
		}
		return w.Body.String()
	}

	assertIntegrationsV2Flag(t, s, true)

	// Cross-manager and read-only role are rejected with 403; missing manager target returns 400.
	put("another", "full", `{"integrations_v2":false}`, http.StatusForbidden)
	put("", "full", `{"integrations_v2":false}`, http.StatusBadRequest)
	put(s.managerIdentity(), "read", `{"integrations_v2":false}`, http.StatusForbidden)

	// An older client expecting off gets a conflict against the unset preference.
	conflictBody := put(s.managerIdentity(), "full", `{"integrations_v2":false,"expected_integrations_v2":false}`, http.StatusConflict)
	var errResp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(conflictBody), &errResp); err != nil || errResp.Code != "configuration_conflict" {
		t.Fatalf("unexpected conflict response: %s", conflictBody)
	}
	if settings, ok := s.savedSettings(t.Context()); !ok || settings.IntegrationsV2 != nil {
		t.Fatal("conflict changed the unset preference")
	}
	assertIntegrationsV2Flag(t, s, true)

	setIntegrationsV2ViaHTTP(t, s, false, true)
	assertIntegrationsV2Flag(t, s, false)

	// Toggling performs zero destructive migration on existing state.
	after, ok := todoByTitle(s, "Existing workflow item")
	if !ok || !reflect.DeepEqual(after, item) {
		t.Fatalf("setting change mutated saved item: got %+v, want %+v", after, item)
	}
}

func TestIntegrationsV2PersistsForSelectedManager(t *testing.T) {
	st := &memStore{}
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)
	s.id = "example-manager"

	setIntegrationsV2ViaHTTP(t, s, false, true)

	reloaded := newServiceOn(t, f, st)
	reloaded.id = s.id
	w, view := permissionSettingsRequest(t, reloaded, "GET", "/api/v1/assistant", s.id, "full", "")
	if w.Code != http.StatusOK || view.Manager != s.id || view.IntegrationsV2 {
		t.Fatalf("lost opt-out on reload: code=%d manager=%q integrations_v2=%t", w.Code, view.Manager, view.IntegrationsV2)
	}

	setIntegrationsV2ViaHTTP(t, reloaded, true, false)

	reloadedOn := newServiceOn(t, f, st)
	reloadedOn.id = s.id
	w, view = permissionSettingsRequest(t, reloadedOn, "GET", "/api/v1/assistant", s.id, "full", "")
	if w.Code != http.StatusOK || view.Manager != s.id || !view.IntegrationsV2 {
		t.Fatalf("lost opt-in on reload: code=%d manager=%q integrations_v2=%t", w.Code, view.Manager, view.IntegrationsV2)
	}
}
