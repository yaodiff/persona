package hub

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extwire"
)

func TestSavedAgentSearchReadScopeAndOwnerRelay(t *testing.T) {
	const path = "/api/v1/saved-agent-searches"
	if extwire.RouteScopes["GET "+path] != extwire.ScopeAgentsRead || extwire.RouteScopes["POST "+path] != "" {
		t.Fatal("saved searches must expose read scope only")
	}
	for _, role := range []string{roleFull, roleAnswer} {
		if !allowed(principal{Role: role}, "GET", path) {
			t.Fatalf("%s cannot read", role)
		}
	}
	if allowed(principal{Role: roleAnswer}, "POST", path) || allowed(principal{Role: roleLink}, "GET", path) {
		t.Fatal("saved searches widened unrelated permissions")
	}
	_, base, _, _ := reachHub(t)
	code, body := extDo(t, base, "GET", path, "", nil)
	if code != 200 || !strings.Contains(body, `"who":"b"`) {
		t.Fatalf("extension read was not owner-routed: %d %s", code, body)
	}
	code, body = extDo(t, base, "GET", path, "", map[string]string{ManagerHeader: "assistant"})
	if code != http.StatusForbidden {
		t.Fatalf("extension selected another manager: %d %s", code, body)
	}
}

func TestSavedAgentSearchExtensionCannotWrite(t *testing.T) {
	const path = "/api/v1/saved-agent-searches"
	h := newTestHub(t)
	mux := h.routes()
	f := extTokensOf(t, h)
	// reachHub deliberately authorizes every route to test ownership. Exercise
	// the normal scope matcher here so a write is rejected before manager relay.
	for _, scopes := range [][]string{{extwire.ScopeAgentsRead}, {extwire.ScopeAgentsControl}, {extwire.ScopeAgentsRead, extwire.ScopeAgentsControl}} {
		token := f.issue("saved-search-check", "Saved search check", scopes)
		code, body := call(t, mux, "POST", path, token, `{"revision":"0","action":"add","name":"Work","query":"state:working"}`)
		if code != http.StatusForbidden {
			t.Fatalf("extension with %v wrote saved searches: %d %v", scopes, code, body)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.served) != 0 {
		t.Fatalf("denied writes reached the manager: %v", f.served)
	}
}

func TestSavedAgentSearchBroadcastIsManagerStamped(t *testing.T) {
	h, a, b := pathsHub(t, quiet, quiet)
	app := wsApp(t, h)
	got := broadcastsTo(t, app, b, `{"type":"saved_agent_searches","revision":"9007199254740993"}`)
	got = append(got, broadcastsTo(t, app, a, `{"type":"saved_agent_searches","revision":"7"}`)...)
	owners := map[string]string{}
	for _, message := range got {
		var typ, revision, manager string
		_ = json.Unmarshal(message["type"], &typ)
		if typ != "saved_agent_searches" {
			continue
		}
		_ = json.Unmarshal(message["revision"], &revision)
		_ = json.Unmarshal(message["manager"], &manager)
		owners[revision] = manager
	}
	if len(owners) != 2 || owners["9007199254740993"] != "m-0b0b0b" || owners["7"] != "" {
		t.Fatalf("wrong event owners or integer precision: %v", owners)
	}
}
