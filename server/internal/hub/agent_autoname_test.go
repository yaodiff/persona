package hub

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func autonameCapability(t *testing.T, manager *guardManager, supported bool) {
	t.Helper()
	capabilities := []string{assistantwire.CapabilityConfigOwn, assistantwire.CapabilityAgentSessionArchives}
	if supported {
		capabilities = append(capabilities, assistantwire.CapabilityAgentAutoname)
	}
	if err := manager.call(t, busHello, map[string]any{"id": manager.id, "capabilities": capabilities}, nil); err != nil {
		t.Fatal(err)
	}
}

// A requested name goes to the manager that owns the machine, as the user's own full device asked it: an older or
// offline owner is told apart before anything is relayed, and the feature is listed only when every owner takes it.
func TestAgentAutonameRelaysToACapableOwner(t *testing.T) {
	h, srv, a, b := archiveRelayHub(t)
	if slices.Contains(h.features(), featureAgentAutoname) {
		t.Fatal("advertised before any manager named it")
	}
	autonameCapability(t, a, true)
	autonameCapability(t, b, false)
	if slices.Contains(h.features(), featureAgentAutoname) {
		t.Fatal("mixed manager versions must hide the feature")
	}
	before := len(archiveHTTPCalls(t, b))
	code, body := ownershipGet(t, h, srv, http.MethodPost, "/api/v1/sessions/mb/same/autoname", nil)
	if code != http.StatusNotImplemented || body["code"] != "agent_autoname_unsupported" || body["error"] == "" {
		t.Fatalf("an older owner: %d %+v", code, body)
	}
	if len(archiveHTTPCalls(t, b)) != before {
		t.Fatal("the request reached a manager without the route")
	}
	code, body = ownershipGet(t, h, srv, http.MethodPost, "/api/v1/sessions/ma/same/autoname", nil)
	calls := archiveHTTPCalls(t, a)
	if code != http.StatusOK || body["owner"] != "ma" || len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != "/api/v1/sessions/ma/same/autoname" ||
		!slices.Equal(calls[0].Header[assistantwire.RoleHeader], []string{roleFull}) {
		t.Fatalf("a capable owner: %d %+v %+v", code, body, calls)
	}

	autonameCapability(t, b, true)
	if !slices.Contains(h.features(), featureAgentAutoname) {
		t.Fatalf("every owner takes it: %v", h.features())
	}
	code, body = ownershipGet(t, h, srv, http.MethodPost, "/api/v1/sessions/mb/same/autoname", nil)
	if code != http.StatusOK || body["owner"] != "mb" || len(archiveHTTPCalls(t, b)) != before+1 || len(archiveHTTPCalls(t, a)) != 1 {
		t.Fatalf("the machine's owner: %d %+v", code, body)
	}

	// Only the user's full device: an extension's token has no scope for it, of its own manager or another's.
	for _, path := range []string{"/api/v1/sessions/ma/same/autoname", "/api/v1/sessions/mb/same/autoname"} {
		if code, body := extDo(t, srv.URL, http.MethodPost, path, "{}", nil); code != http.StatusForbidden {
			t.Fatalf("extension %s: %d %s", path, code, body)
		}
	}
	for _, role := range []string{roleAnswer, roleLink} {
		if allowed(principal{Role: role}, http.MethodPost, "/api/v1/sessions/mb/same/autoname") {
			t.Fatalf("%s may rename agents", role)
		}
	}
	if len(archiveHTTPCalls(t, a)) != 1 || len(archiveHTTPCalls(t, b)) != before+1 {
		t.Fatal("a forbidden request reached a manager")
	}

	if err := h.busConnOf(b.id).ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "manager offline", func() bool { return h.busConnOf(b.id) == nil })
	if slices.Contains(h.features(), featureAgentAutoname) {
		t.Fatal("an offline owner must hide the feature")
	}
	code, body = ownershipGet(t, h, srv, http.MethodPost, "/api/v1/sessions/mb/same/autoname", nil)
	if code != http.StatusServiceUnavailable || body["code"] != assistantwire.CodeAssistantOffline {
		t.Fatalf("an offline owner: %d %+v", code, body)
	}
}
