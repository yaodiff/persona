package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The voice program as a marketplace package, on an extension's token (#17851, #16264): the scopes the package runs
// on, the hub routes it calls and the scope each needs, and the routes that stay closed to every extension. The
// package asks for voiceScopes in permissions.hub instead of the hub's full token; what follows from that is pinned
// below.
//
// This is the package's list, not the built-in's. The built-in in this tree (server/internal/modules/voice) runs on
// the hub's own token and calls four more kinds of route, which no scope opens and the package does without:
//   - GET /api/v1/ws (hublink.go), its wake-up: the package is told of changes by an extension event instead
//     (agent.changed, the other part of #17851);
//   - GET /api/v1/recordings/{id}/live (hub.go meetingRoom): the package tells a meeting's room by its session row,
//     origin "meeting" and origin_ref the recording;
//   - GET /api/v1/channels (hub.go channels): the package names the built-in channels itself;
//   - POST /api/v1/machines/{mid}/files/mkdir and …/files/write (archive.go), a call's archive: the first package
//     version writes none, and a hub with an archive computer set keeps the built-in (#16264).
//
// They stay closed on purpose (closedToExtensions, closedFamilies; the channel list has no scope either), and
// TestVoiceProgramOnAnExtensionsToken checks that a token of voiceScopes is refused on each.

// voiceScopes are the scopes the voice package runs on, each one covered by the old full grant too
// (extension.InEveryScope), so a built-in handing over to a package asks for nothing more.
var voiceScopes = []string{extwire.ScopeKBRead, extwire.ScopeAgentsRead, extwire.ScopeAgentsControl,
	extwire.ScopeMachinesRead, extwire.ScopeAssistantAsk}

// voiceRoutes are the hub routes the voice package calls: those of the built-in's (server/internal/modules/voice/hub.go)
// that a scope opens, the four kinds above left out. A request of each as the program sends it, and the scope the
// route needs.
var voiceRoutes = []struct {
	pattern, scope string
	method, path   string
	body           string
}{
	{"GET /api/v1/kb-search", extwire.ScopeKBRead, "GET", "/api/v1/kb-search?q=release", ""},
	{"GET /api/v1/sessions", extwire.ScopeAgentsRead, "GET", "/api/v1/sessions?machine_id=mb", ""},
	{"GET /api/v1/sessions/{mid}/{sid}/items", extwire.ScopeAgentsRead, "GET", "/api/v1/sessions/mb/b1/items?turns=1&lazy_images=1", ""},
	{"GET /api/v1/sessions/{mid}/{sid}/items/{iid}/full", extwire.ScopeAgentsRead, "GET", "/api/v1/sessions/mb/b1/items/i1/full", ""},
	{"GET /api/v1/interactions", extwire.ScopeAgentsRead, "GET", "/api/v1/interactions", ""},
	{"GET /api/v1/agents/triage", extwire.ScopeAgentsRead, "GET", "/api/v1/agents/triage", ""},
	{"POST /api/v1/interactions/{id}/resolve", extwire.ScopeAgentsControl, "POST", "/api/v1/interactions/ix_ok/resolve", `{"answers":{}}`},
	{"POST /api/v1/sessions/{mid}/{sid}/interrupt", extwire.ScopeAgentsControl, "POST", "/api/v1/sessions/mb/b1/interrupt", `{}`},
	{"POST /api/v1/sessions/{mid}/{sid}/messages", extwire.ScopeAgentsControl, "POST", "/api/v1/sessions/mb/b1/messages", `{"text":"hello","client_msg_id":"voice-0011223344556677"}`},
	{"GET /api/v1/machines", extwire.ScopeMachinesRead, "GET", "/api/v1/machines", ""},
	{"GET /api/v1/assistant", extwire.ScopeAssistantAsk, "GET", "/api/v1/assistant", ""},
	{"POST /api/v1/assistant/conversations", extwire.ScopeAssistantAsk, "POST", "/api/v1/assistant/conversations", `{"prompt":"hello","main":true}`},
}

// closedToExtensions are routes no scope may ever open, and why: giving one a scope hands an extension more than the
// route's name says.
var closedToExtensions = []struct{ pattern, why string }{
	{"GET /api/v1/ws", "the apps' socket: a connection that is not a scoped or read-only device's is served as the owner's (the " +
		"event log, every manager's sessions, terminals); extensions are told of changes through contributes.events"},
	{"GET /api/v1/recordings/{id}/live", "a meeting's live lines, questions, photos and highlights; its room is on the session row (origin, origin_ref)"},
	{"POST /api/v1/recordings/{id}/live", "the phone's live sentences of a meeting"},
	{"PUT /api/v1/agents/triage", "changing the triage's switches; reading them is agents.read's"},
}

// closedFamilies are whole families of routes no scope may ever open: every route at prefix or under it, the ones
// registered now and any added later, whatever its method. A new route there is closed without anyone listing it, so
// opening one means changing this list, with the reason against it in front of the reader. must are routes of the
// family that are there today: the family read as empty, or without them, is a scan that no longer sees it.
var closedFamilies = []struct {
	prefix, why string
	must        []string
}{
	{"/api/v1/machines/{mid}/files", "the Files browser: listing, reading and changing anything in the home, a shell's startup file among it",
		[]string{"POST /api/v1/machines/{mid}/files", "POST /api/v1/machines/{mid}/files/write", "POST /api/v1/machines/{mid}/files/mkdir",
			"POST /api/v1/machines/{mid}/files/upload", "POST /api/v1/machines/{mid}/files/create", "PUT /api/v1/machines/{mid}/files/content",
			"PATCH /api/v1/machines/{mid}/files/entry", "DELETE /api/v1/machines/{mid}/files/entry", "GET /api/v1/machines/{mid}/files/download",
			"GET /api/v1/machines/{mid}/files/list"}},
	{"/api/v1/machines/{mid}/file", "reading any file the phone may read, its bytes and its previews",
		[]string{"GET /api/v1/machines/{mid}/file", "GET /api/v1/machines/{mid}/file/download"}},
	{"/api/v1/machines/{mid}/fs", "listing and finding every file under a folder",
		[]string{"GET /api/v1/machines/{mid}/fs", "GET /api/v1/machines/{mid}/fs/find"}},
	{"/api/v1/machines/{mid}/dirs", "listing a computer's folders", []string{"GET /api/v1/machines/{mid}/dirs"}},
	{"/api/v1/machines/{mid}/file-transfer", "the owner's switch for files sent to a computer",
		[]string{"PUT /api/v1/machines/{mid}/file-transfer/settings"}},
}

// closedFamily is the family of closedFamilies route is in (by its path, whatever its method), and whether it is in one.
func closedFamily(route string) (prefix, why string, ok bool) {
	path := route
	if _, p, cut := strings.Cut(route, " "); cut {
		path = p
	}
	for _, f := range closedFamilies {
		if path == f.prefix || strings.HasPrefix(path, f.prefix+"/") {
			return f.prefix, f.why, true
		}
	}
	return "", "", false
}

// closedRoutes are the routes closed to extensions among patterns: closedToExtensions' and every one of a closed
// family, sorted.
func closedRoutes(patterns []string) []string {
	var out []string
	for _, c := range closedToExtensions {
		out = append(out, c.pattern)
	}
	for _, p := range patterns {
		if _, _, ok := closedFamily(p); ok {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Each route the voice package calls is registered and needs one of its five scopes, nothing else; the five are all
// it needs, and each is in the old full grant.
func TestVoiceRoutesNeedOnlyTheVoiceScopes(t *testing.T) {
	patterns := routePatterns(t)
	used := map[string]bool{}
	for _, r := range voiceRoutes {
		if !slices.Contains(patterns, r.pattern) {
			t.Errorf("%s is not a route of the hub any more: the voice program calls it", r.pattern)
		}
		if got := extwire.RouteScopes[r.pattern]; got != r.scope {
			t.Errorf("%s needs %q, want %s", r.pattern, got, r.scope)
		}
		if !slices.Contains(voiceScopes, r.scope) {
			t.Errorf("%s needs %s, which is not one of the voice program's scopes %v", r.pattern, r.scope, voiceScopes)
		}
		used[r.scope] = true
	}
	for _, s := range voiceScopes {
		if !used[s] {
			t.Errorf("%s opens no route the voice program calls: it should not ask for it", s)
		}
		if !extension.InEveryScope(s) {
			t.Errorf("%s is granted by name only: a built-in handing over to a package would have to ask for it", s)
		}
	}
}

// No scope opens the apps' socket, a meeting's live route, the triage's PUT or any route of a closed family (a
// computer's files and folders): the routes registered now, whichever the scope table names, and any added later.
func TestRoutesClosedToExtensionTokens(t *testing.T) {
	patterns := routePatterns(t)
	for _, c := range closedToExtensions {
		if !slices.Contains(patterns, c.pattern) {
			t.Errorf("%s is not a route of the hub any more: take it off closedToExtensions", c.pattern)
		}
		if scope, ok := extwire.RouteScopes[c.pattern]; ok {
			t.Errorf("%s is opened by %s; it stays for full credentials only: %s", c.pattern, scope, c.why)
		}
	}
	// What is registered: every route of a family, found by its path.
	found := map[string][]string{}
	for _, p := range patterns {
		prefix, why, ok := closedFamily(p)
		if !ok {
			continue
		}
		found[prefix] = append(found[prefix], p)
		if scope, opened := extwire.RouteScopes[p]; opened {
			t.Errorf("%s is opened by %s; every route under %s stays for full credentials only: %s", p, scope, prefix, why)
		}
	}
	for _, f := range closedFamilies {
		if len(found[f.prefix]) == 0 {
			t.Errorf("no route is registered under %s: the scan no longer sees the family (or take it off closedFamilies)", f.prefix)
		}
		for _, m := range f.must {
			if !slices.Contains(found[f.prefix], m) {
				t.Errorf("%s is not found under %s: the scan no longer sees it (or take it off closedFamilies)", m, f.prefix)
			}
		}
	}
	// What the scope table names, registered or not: a scope given to a route of a family before the route is there.
	for route, scope := range extwire.RouteScopes {
		if prefix, why, ok := closedFamily(route); ok {
			t.Errorf("%s is opened by %s; every route under %s stays for full credentials only: %s", route, scope, prefix, why)
		}
	}
	// The Files browser's family as it is today: a route added to it is closed by the scan, not by this number.
	if n := len(found["/api/v1/machines/{mid}/files"]); n < 21 {
		t.Errorf("%d routes found under /api/v1/machines/{mid}/files, 21 when this was written: the scan lost some: %v", n, found["/api/v1/machines/{mid}/files"])
	}
}

// voiceHub is a hub whose manager B owns mb and knows the token of an extension granted voiceScopes: it answers
// extensions.authorize as the manager does (extmgr scopeOpens), by the route's scope in extwire.RouteScopes.
func voiceHub(t *testing.T) (*Hub, string, *guardManager, *guardManager) {
	t.Helper()
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "mb", Name: "mb", Kind: "local"}}
	answer := func(knows bool) func(string, json.RawMessage) (any, error) {
		return func(method string, params json.RawMessage) (any, error) {
			switch method {
			case extwire.MethodAuthorize:
				if !knows {
					return extwire.Authorized{}, nil
				}
				var a extwire.Authorize
				_ = json.Unmarshal(params, &a)
				scope := extwire.RouteScopes[a.Pattern]
				return extwire.Authorized{OK: scope != "" && slices.Contains(voiceScopes, scope), Scope: scope,
					Extension: "voice", Name: "Voice", Scopes: voiceScopes}, nil
			case assistantwire.MethodHTTP:
				return map[string]any{"status": 200, "body": []byte(`{"ok":true}`)}, nil
			case devicewire.MethodCall:
				return map[string]any{"ok": true}, nil
			}
			return map[string]any{"ok": true}, nil
		}
	}
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, answer(false))
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", answer(true))
	if _, err := h.moveMachine(context.Background(), "mb", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	b.report(t, devicewire.State{Machine: proto.Machine{ID: "mb", Name: "mb"}})
	eventually(t, 5*time.Second, "mb", func() bool { return h.localMachine("mb") != nil })
	return h, srv.URL, a, b
}

// The voice package's requests with a token of voiceScopes: every route it calls lets it in, the routes closed to
// extensions, those the built-in still calls among them, refuse it with permission_denied, and what it sees and does is an
// extension's, not the owner's: no conversation of a space, no private card, a meeting's room by its session row, and
// a message that is not the owner's own input.
func TestVoiceProgramOnAnExtensionsToken(t *testing.T) {
	h, url, a, b := voiceHub(t)
	const room = "0a1b2c3d-0000-4000-8000-000000000001"
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "mb", Sessions: []proto.Session{
		{MachineID: "mb", SessionID: "b1", Title: "B-SESSION", Cwd: "/work/project"},
		{MachineID: "mb", SessionID: "bh", Title: "SPACE-SESSION", Cwd: "/home/me/.persona/health", Assistant: true, Space: "health"},
		{MachineID: "mb", SessionID: "br", Title: "ROOM-SESSION", Cwd: "/home/me/.persona/guest-meeting/" + room, Origin: "meeting", OriginRef: room},
	}})
	eventually(t, 5*time.Second, "mb's sessions", func() bool { _, ok := h.localMachine("mb").session("br"); return ok })
	for _, ix := range []proto.Interaction{
		{ID: "ix_ok", MachineID: "mb", SessionID: "b1", Cwd: "/work/project", Kind: proto.IxQuestion, Summary: "OK-CARD"},
		{ID: "ix_private", MachineID: "mb", SessionID: "b1", Cwd: "/work/project", Kind: proto.IxQuestion, Private: true, Summary: "PRIVATE-CARD"},
		{ID: "ix_space", MachineID: "mb", SessionID: "bh", Kind: proto.IxQuestion, Summary: "SPACE-CARD"},
	} {
		if err := b.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: ix}); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 5*time.Second, "the cards", func() bool { return h.interactionOwner("ix_space") != "" })

	denied := func(code int, body string) bool {
		return code == http.StatusForbidden && strings.Contains(body, "permission_denied")
	}
	t.Run("the routes it calls let it in", func(t *testing.T) {
		for _, r := range voiceRoutes {
			if code, body := extDoAs(t, url, "voice-token", r.method, r.path, r.body); code != http.StatusOK {
				t.Errorf("%s %s: %d %s", r.method, r.path, code, clip(body, 200))
			}
		}
	})
	t.Run("the closed routes refuse it", func(t *testing.T) {
		// Every closed route as the hub registers it (the families' whole, closedRoutes), a request of each, and the
		// channel list, which no scope opens either (the program names the built-in channels itself).
		type request struct{ method, path, body string }
		reqs := []request{{"GET", "/api/v1/channels", ""}}
		closed := closedRoutes(routePatterns(t))
		for _, p := range closed {
			method, path, ok := strings.Cut(p, " ")
			if !ok {
				t.Fatalf("%s: a closed route without a method", p)
			}
			path = strings.NewReplacer("{mid}", "mb", "{oid}", "op1", "{id}", room).Replace(path)
			if strings.Contains(path, "{") {
				t.Fatalf("%s: a path value this test does not fill in: %s", p, path)
			}
			body := ""
			if method != http.MethodGet && method != http.MethodDelete {
				body = `{"path":"~/PersonaCalls/call.md","dir":"~","name":"PersonaCalls","text":"x","create":true,"enabled":false}`
			}
			reqs = append(reqs, request{method, path, body})
		}
		if len(closed) < 34 {
			t.Errorf("%d closed routes asked, 34 when this was written: %v", len(closed), closed)
		}
		for _, c := range reqs {
			before := len(reached(b))
			if code, body := extDoAs(t, url, "voice-token", c.method, c.path, c.body); !denied(code, body) {
				t.Errorf("%s %s: %d %s, want 403 permission_denied", c.method, c.path, code, clip(body, 200))
			}
			if got := reached(b); len(got) != before {
				t.Errorf("%s %s reached the manager: %v", c.method, c.path, got[before:])
			}
		}
	})
	t.Run("the triage's switches are its own manager's", func(t *testing.T) {
		beforeA, beforeB := len(reached(a)), len(reached(b))
		if code, body := extDoAs(t, url, "voice-token", "GET", "/api/v1/agents/triage", ""); code != http.StatusOK {
			t.Fatalf("triage: %d %s", code, clip(body, 200))
		}
		if got := reached(b)[beforeB:]; len(got) != 1 || !strings.Contains(got[0], "/api/v1/agents/triage") {
			t.Errorf("B got %v, want the one request", got)
		}
		// Naming the other manager is refused, and nothing of it reaches that manager.
		for _, path := range []string{"/api/v1/agents/triage?manager=" + assistantwire.AssistantID} {
			if code, body := extDoAs(t, url, "voice-token", "GET", path, ""); code != http.StatusForbidden || !strings.Contains(body, codeOtherManager) {
				t.Errorf("%s: %d %s, want 403 %s", path, code, clip(body, 200), codeOtherManager)
			}
		}
		if got := reached(a)[beforeA:]; len(got) != 0 {
			t.Errorf("A was reached: %v", got)
		}
	})
	t.Run("a space's conversation and a private card are not its to see or act on", func(t *testing.T) {
		code, body := extDoAs(t, url, "voice-token", "GET", "/api/v1/sessions?machine_id=mb", "")
		if code != http.StatusOK || !strings.Contains(body, "B-SESSION") || strings.Contains(body, "SPACE-SESSION") {
			t.Errorf("sessions: %d %s", code, clip(body, 300))
		}
		for _, c := range []struct{ method, path, body string }{
			{"GET", "/api/v1/sessions/mb/bh/items?turns=1", ""},
			{"POST", "/api/v1/sessions/mb/bh/messages", `{"text":"hello","client_msg_id":"voice-0011223344556688"}`},
			{"POST", "/api/v1/sessions/mb/bh/interrupt", `{}`},
			{"POST", "/api/v1/interactions/ix_private/resolve", `{"answers":{}}`},
			{"POST", "/api/v1/interactions/ix_space/resolve", `{"answers":{}}`},
		} {
			if code, body := extDoAs(t, url, "voice-token", c.method, c.path, c.body); code != http.StatusNotFound {
				t.Errorf("%s %s: %d %s, want 404", c.method, c.path, code, clip(body, 200))
			}
		}
		code, body = extDoAs(t, url, "voice-token", "GET", "/api/v1/interactions", "")
		if code != http.StatusOK || !strings.Contains(body, "OK-CARD") || strings.Contains(body, "PRIVATE-CARD") || strings.Contains(body, "SPACE-CARD") {
			t.Errorf("cards: %d %s", code, clip(body, 300))
		}
	})
	t.Run("a meeting's room is told by its session row", func(t *testing.T) {
		code, body := extDoAs(t, url, "voice-token", "GET", "/api/v1/sessions?machine_id=mb", "")
		var out struct {
			Sessions []proto.Session `json:"sessions"`
		}
		if err := json.Unmarshal([]byte(body), &out); code != http.StatusOK || err != nil {
			t.Fatalf("sessions: %d %v %s", code, err, clip(body, 300))
		}
		i := slices.IndexFunc(out.Sessions, func(s proto.Session) bool { return s.SessionID == "br" })
		if i < 0 || out.Sessions[i].Origin != "meeting" || out.Sessions[i].OriginRef != room {
			t.Fatalf("the room's row: %+v", out.Sessions)
		}
		if code, body := extDoAs(t, url, "voice-token", "GET", "/api/v1/sessions/mb/br/items?turns=1", ""); code != http.StatusOK {
			t.Errorf("the room's items: %d %s", code, clip(body, 200))
		}
	})
	t.Run("what it sends is not the owner's own input", func(t *testing.T) {
		start := func(token string) string {
			before := len(reached(b))
			if code, body := extDoAs(t, url, token, "POST", "/api/v1/assistant/conversations", `{"prompt":"hello","main":true}`); code != http.StatusOK {
				t.Fatalf("start: %d %s", code, clip(body, 200))
			}
			return strings.Join(reached(b)[before:], "\n")
		}
		if got := start("voice-token"); !strings.Contains(got, assistantwire.MethodHTTP) || strings.Contains(got, `"reply_input":true`) || strings.Contains(got, `"human":true`) {
			t.Errorf("an extension's start was relayed as the owner's own input:\n%s", clip(got, 400))
		}
	})
}

// extDoAs is a request with the extension token name of voiceHub's manager B.
func extDoAs(t *testing.T, url, name, method, path, body string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, method, url+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+extTokenPrefix+name)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	var out json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, string(out)
}
