package hub

// An extension's token acts as its manager and only as it (docs/assistant/managers.md "Ownership"): whatever names the target
// (the path, the query, a header, the body), another manager's machines, sessions, cards and conversations, and
// another manager itself, are out of its reach.

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
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// reachHub: A (the assistant) owns ma with session s3, its card ix_a and its conversation conv-a; B owns mb with
// session b1 and knows the extension token bx-token (opening every route: the scope is not what is tested).
func reachHub(t *testing.T) (*Hub, string, *guardManager, *guardManager) {
	t.Helper()
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}, {ID: "mb", Name: "mb", Kind: "local"}}
	answer := func(who string, knows bool) func(string, json.RawMessage) (any, error) {
		return func(method string, _ json.RawMessage) (any, error) {
			switch method {
			case extwire.MethodAuthorize:
				if knows {
					return extwire.Authorized{OK: true, Extension: "bx", Name: "BX"}, nil
				}
				return extwire.Authorized{}, nil
			case assistantwire.MethodHTTP:
				return map[string]any{"status": 200, "body": []byte(`{"who":"` + who + `","items":[{"id":"` + who + `-item"}]}`)}, nil
			case devicewire.MethodCall:
				return map[string]any{"who": who, "session": map[string]any{"session_id": who + "-started"}}, nil
			}
			return map[string]any{"who": who}, nil
		}
	}
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, answer("a", false))
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", answer("b", true))
	if _, err := h.moveMachine(context.Background(), "mb", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	a.report(t, devicewire.State{Machine: proto.Machine{ID: "ma", Name: "ma"}})
	b.report(t, devicewire.State{Machine: proto.Machine{ID: "mb", Name: "mb"}})
	eventually(t, 5*time.Second, "ma, mb", func() bool { return h.localMachine("ma") != nil && h.localMachine("mb") != nil })
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "ma", Sessions: []proto.Session{{MachineID: "ma", SessionID: "s3", Title: "A-SESSION"}}})
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "mb", Sessions: []proto.Session{{MachineID: "mb", SessionID: "b1", Title: "B-SESSION"}}})
	for _, c := range []struct {
		g  *guardManager
		ix proto.Interaction
	}{{a, proto.Interaction{ID: "ix_a", MachineID: "ma", SessionID: "s3", Kind: proto.IxPermission, Summary: "A-CARD"}},
		{b, proto.Interaction{ID: "ix_b", MachineID: "mb", SessionID: "b1", Kind: proto.IxPermission, Summary: "B-CARD"}}} {
		if err := c.g.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: c.ix}); err != nil {
			t.Fatal(err)
		}
	}
	var res assistantwire.SnapshotResult
	if err := a.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: "a", Seq: 1,
		Sessions: []assistantwire.SessionView{{MachineID: "ma", SessionID: "conv-a"}}}, &res); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "both cards", func() bool { return h.interactionOwner("ix_a") != "" && h.interactionOwner("ix_b") != "" })
	return h, srv.URL, a, b
}

// extDo is a request with B's extension's token.
func extDo(t *testing.T, url, method, path, body string, header map[string]string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, method, url+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+extTokenPrefix+"bx-token")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
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

// reached is what manager f got but whose-token-is-this (and what the hub does on its own: fs.watch, the tools).
func reached(f *guardManager) []string {
	var out []string
	for _, l := range strings.Split(f.log(), "\n") {
		if l == "" || strings.HasPrefix(l, extwire.MethodAuthorize+" ") || strings.HasPrefix(l, "assistant.tools") ||
			strings.Contains(l, `"method":"fs.watch"`) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// B's extension cannot reach A by naming it, nor A's machine, session, card or conversation wherever it is named, and
// A's things are left out of the hub's own lists; what is B's goes through.
func TestAnExtensionsTokenActsOnlyAsItsManager(t *testing.T) {
	h, url, a, b := reachHub(t)
	namesA := map[string]string{ManagerHeader: assistantwire.AssistantID}
	refused := []struct {
		name, method, path, body string
		header                   map[string]string
	}{
		{"names A: a merged list", "GET", "/api/v1/inbox", "", namesA},
		{"names A: a paged list", "GET", "/api/v1/inbox/done", "", namesA},
		{"names A by query: a manager route", "GET", "/api/v1/assistant?manager=assistant", "", nil},
		{"names A: a naming preview", "POST", "/api/v1/assistant/workspace-naming/preview", `{"machine_id":"mb","layout":"root"}`, namesA},
		{"names A by query: a naming preview", "POST", "/api/v1/assistant/workspace-naming/preview?manager=assistant", `{"machine_id":"mb","layout":"root"}`, nil},
		{"names A: an inbox reply", "POST", "/api/v1/inbox/item-1/reply", `{"text":"x"}`, namesA},
		{"names A: an inbox retry", "POST", "/api/v1/inbox/item-1/retry", "", namesA},
		{"names A: the knowledge base", "GET", "/api/v1/kb/people", "", namesA},
		{"names A: another extension's HTTP", "GET", "/api/v1/extensions/lark/things", "", namesA},
		{"A's card choice", "POST", "/api/v1/interactions/ix_a/choose", `{"option":0,"rev":"old"}`, nil},
		{"A's conversation's feed", "GET", "/api/v1/assistant/conversations/conv-a/feed", "", nil},
		{"A's conversation's stop", "POST", "/api/v1/assistant/conversations/conv-a/stop", "", nil},
		{"an agent on A's machine (body)", "POST", "/api/v1/sessions", `{"machine_id":"ma","cwd":"/tmp","prompt":"B's prompt"}`, nil},
		{"A's session resumed (body)", "POST", "/api/v1/sessions", `{"machine_id":"ma","resume_session_id":"s3","cwd":"/tmp"}`, nil},
		{"the sessions of ma (query)", "GET", "/api/v1/sessions?machine_id=ma", "", nil},
		{"the paged sessions of ma (query)", "GET", "/api/v1/sessions?limit=10&machine_id=ma", "", nil},
		{"a notification on A's session (body)", "POST", "/api/v1/notify", `{"title":"t","body":"b","machine_id":"ma","session_id":"s3"}`, nil},
		{"names A: push grading layer GET", "GET", "/api/v1/push/grading/layer", "", namesA},
		{"names A by query: push grading layer PUT", "PUT", "/api/v1/push/grading/layer?manager=assistant", `{"grading_enabled":true}`, nil},
		{"names A: push grading layer DELETE", "DELETE", "/api/v1/push/grading/layer", "", namesA},
		{"names A: push grading preview", "POST", "/api/v1/push/grading/preview", `{"notification":{"level":"P1"}}`, namesA},
	}
	for _, c := range refused {
		before := len(reached(a))
		if code, out := extDo(t, url, c.method, c.path, c.body, c.header); code != http.StatusForbidden || !strings.Contains(out, codeOtherManager) {
			t.Errorf("%s: %d %s", c.name, code, clip(out, 200))
		}
		if got := reached(a); len(got) != before {
			t.Errorf("%s: A was reached: %v", c.name, got[before:])
		}
	}
	filtered := []struct{ name, path, mine, theirs string }{
		{"sessions", "/api/v1/sessions", "B-SESSION", "A-SESSION"},
		{"paged sessions", "/api/v1/sessions?limit=10", "B-SESSION", "A-SESSION"},
		{"session counts", "/api/v1/sessions?counts=1", `"mb":1`, `"ma"`},
		{"cards", "/api/v1/interactions", "B-CARD", "A-CARD"},
		{"machines", "/api/v1/machines", `"id":"mb"`, `"id":"ma"`},
	}
	for _, c := range filtered {
		code, out := extDo(t, url, "GET", c.path, "", nil)
		if code != http.StatusOK || !strings.Contains(out, c.mine) || strings.Contains(out, c.theirs) {
			t.Errorf("%s: %d %s", c.name, code, clip(out, 300))
		}
	}
	// Its own: B's manager, by name or not, B's machine, a notification on B's session.
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/assistant?manager=m-0b0b0b", ""},
		{"GET", "/api/v1/assistant", ""},
		{"POST", "/api/v1/assistant/workspace-naming/preview", `{"machine_id":"mb","layout":"root"}`},
		{"POST", "/api/v1/assistant/workspace-naming/preview?manager=m-0b0b0b", `{"machine_id":"mb","layout":"root"}`},
		{"GET", "/api/v1/inbox/done", ""},
		{"POST", "/api/v1/notify", `{"title":"t","body":"b","machine_id":"mb","session_id":"b1"}`},
		{"GET", "/api/v1/push/grading/layer", ""},
		{"POST", "/api/v1/push/grading/preview", `{"notification":{"kind":"assistant","level":"P1"}}`},
	} {
		if code, out := extDo(t, url, c.method, c.path, c.body, nil); code != http.StatusOK {
			t.Errorf("%s %s: %d %s", c.method, c.path, code, clip(out, 200))
		}
	}
	if code, out := extDo(t, url, "POST", "/api/v1/sessions", `{"machine_id":"mb","cwd":"/tmp","prompt":"hi"}`, nil); code == http.StatusForbidden ||
		!slices.ContainsFunc(reached(b), func(l string) bool { return strings.Contains(l, `"method":"session.start"`) }) {
		t.Errorf("an agent on B's machine: %d %s", code, clip(out, 200))
	}
	_ = h
}

func TestTodoRepositoryRoutesStayOnTheirManager(t *testing.T) {
	h, url, a, b := reachHub(t)
	h.noteTodoOwner("todo-a", assistantwire.AssistantID)
	h.noteTodoOwner("todo-b", "m-0b0b0b")
	for _, route := range []struct{ method, suffix, body string }{
		{"GET", "/repositories", ""},
		{"PUT", "/repositories", `{}`},
		{"PUT", "/repositories/repo-b/links", `{}`},
		{"GET", "/workspace-rules", ""},
		{"PUT", "/workspace-rules", `{"expected_revision":0,"workspace_rules":null}`},
		{"POST", "/workspace-naming/preview", `{"machine_id":"mb","layout":"root","selected_repo_ids":["repo-b"]}`},
		{"POST", "/workspace-naming/plans", `{"machine_id":"mb","layout":"root","selected_repo_ids":["repo-b"],"operation_id":"example-operation"}`},
		{"GET", "/workspace-naming/plans/example-operation", ""},
	} {
		beforeA, beforeB := len(reached(a)), len(reached(b))
		ownPath := "/api/v1/todos/todo-b" + route.suffix
		for _, request := range []struct {
			path   string
			header map[string]string
		}{
			{"/api/v1/todos/todo-a" + route.suffix, nil},
			{ownPath, map[string]string{ManagerHeader: assistantwire.AssistantID}},
			{ownPath + "?manager=assistant", nil},
		} {
			if code, out := extDo(t, url, route.method, request.path, route.body, request.header); code != http.StatusForbidden || !strings.Contains(out, codeOtherManager) {
				t.Errorf("%s %s: another manager: %d %s", route.method, request.path, code, clip(out, 200))
			}
		}
		if got := len(reached(a)); got != beforeA {
			t.Errorf("%s %s: A was reached: %v", route.method, route.suffix, reached(a)[beforeA:])
		}
		if got := len(reached(b)); got != beforeB {
			t.Errorf("%s %s: a foreign Todo reached B: %v", route.method, route.suffix, reached(b)[beforeB:])
		}

		if code, out := extDo(t, url, route.method, ownPath, route.body, nil); code != http.StatusOK {
			t.Errorf("%s %s: own Todo: %d %s", route.method, route.suffix, code, clip(out, 200))
		}
		if got := len(reached(a)); got != beforeA {
			t.Errorf("%s %s: own Todo reached A: %v", route.method, route.suffix, reached(a)[beforeA:])
		}
		calls := reached(b)[beforeB:]
		if len(calls) != 1 || !strings.HasPrefix(calls[0], assistantwire.MethodHTTP+" ") || !strings.Contains(calls[0], ownPath) {
			t.Errorf("%s %s: own Todo did not reach only B: %v", route.method, route.suffix, calls)
		}
	}
}

func TestAssistantEntriesStayOnTokenManager(t *testing.T) {
	_, url, a, b := reachHub(t)
	const path = "/api/v1/assistant/entries"
	beforeA, beforeB := len(reached(a)), len(reached(b))
	for _, request := range []struct {
		path   string
		header map[string]string
	}{
		{path, map[string]string{ManagerHeader: assistantwire.AssistantID}},
		{path + "?manager=assistant&by=user", nil},
	} {
		if code, out := extDo(t, url, "GET", request.path, "", request.header); code != http.StatusForbidden || !strings.Contains(out, codeOtherManager) {
			t.Errorf("another manager's ledger: %d %s", code, out)
		}
	}
	if len(reached(a)) != beforeA || len(reached(b)) != beforeB {
		t.Fatal("a refused ledger selector reached a manager")
	}
	if code, out := extDo(t, url, "GET", path+"?by=user", "", nil); code != http.StatusOK || !strings.Contains(out, `"who":"b"`) {
		t.Fatalf("the token manager's ledger: %d %s", code, out)
	}
	if len(reached(a)) != beforeA {
		t.Fatal("the ledger read reached another manager")
	}
	calls := reached(b)[beforeB:]
	if len(calls) != 1 || !strings.HasPrefix(calls[0], assistantwire.MethodHTTP+" ") || !strings.Contains(calls[0], path) {
		t.Fatalf("ledger read did not reach only the token manager: %v", calls)
	}
}

// The To-dos view's reads (todo_view.go): one to-do and its timeline go to its own manager (byTodo), another manager's
// are refused; the places are asked of the extension's own manager alone (mergedAsk), and naming another is refused.
func TestTodoViewRoutesStayOnTheirManager(t *testing.T) {
	h, url, a, b := reachHub(t)
	h.noteTodoOwner("todo-a", assistantwire.AssistantID)
	h.noteTodoOwner("todo-b", "m-0b0b0b")
	beforeA, beforeB := len(reached(a)), len(reached(b))
	for _, path := range []string{"/api/v1/todos/todo-a", "/api/v1/todos/todo-a/timeline"} {
		if code, out := extDo(t, url, "GET", path, "", nil); code != http.StatusForbidden || !strings.Contains(out, codeOtherManager) {
			t.Errorf("another manager's to-do, %s: %d %s", path, code, clip(out, 200))
		}
	}
	if code, out := extDo(t, url, "GET", "/api/v1/todos/places", "", map[string]string{ManagerHeader: assistantwire.AssistantID}); code != http.StatusForbidden ||
		!strings.Contains(out, codeOtherManager) {
		t.Errorf("another manager's places: %d %s", code, clip(out, 200))
	}
	paths := []string{"/api/v1/todos/todo-b", "/api/v1/todos/todo-b/timeline", "/api/v1/todos/places"}
	for _, path := range paths {
		if code, out := extDo(t, url, "GET", path, "", nil); code != http.StatusOK {
			t.Errorf("%s: %d %s", path, code, clip(out, 200))
		}
	}
	if got := reached(a)[beforeA:]; len(got) != 0 {
		t.Errorf("A was reached: %v", got)
	}
	calls := reached(b)[beforeB:]
	if len(calls) != len(paths) {
		t.Fatalf("B got %v", calls)
	}
	for i, path := range paths {
		if !strings.Contains(calls[i], path) {
			t.Errorf("B's call %d is %s, want %s", i, calls[i], path)
		}
	}
}

func TestSpacesRoutesStayOnTheirManager(t *testing.T) {
	h, url, a, b := reachHub(t)
	var snapshot assistantwire.SnapshotResult
	if err := b.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: "b", Seq: 1,
		Sessions: []assistantwire.SessionView{{MachineID: "mb", SessionID: "conv-b"}}}, &snapshot); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "the two conversation owners", func() bool {
		return h.conversationOwner("conv-a") == assistantwire.AssistantID && h.conversationOwner("conv-b") == "m-0b0b0b"
	})
	for _, route := range []struct{ method, path, body string }{
		{"GET", "/api/v1/assistant/tracks", ""},
		{"POST", "/api/v1/assistant/tracks", `{}`},
		{"PUT", "/api/v1/assistant/tracks/example", `{}`},
		{"GET", "/api/v1/assistant/track-kinds", ""},
		{"GET", "/api/v1/assistant/tracks/example/space-md", ""},
		{"GET", "/api/v1/assistant/tracks/example/conversations", ""},
		{"PUT", "/api/v1/assistant/tracks/example/space-md", `{}`},
		{"GET", "/api/v1/assistant/conversations/conv-b/categories", ""},
		{"PUT", "/api/v1/assistant/conversations/conv-b/categories", `{}`},
	} {
		beforeA, beforeB := len(reached(a)), len(reached(b))
		// Track and kind routes go to the token's manager. The category route additionally checks its conversation's
		// owner, so the same request against A's conversation is refused before any manager sees it.
		crossPath := route.path
		if strings.Contains(crossPath, "/conversations/") {
			crossPath = "/api/v1/assistant/conversations/conv-a/categories"
		}
		for _, request := range []struct {
			path   string
			header map[string]string
		}{
			{route.path, map[string]string{ManagerHeader: assistantwire.AssistantID}},
			{route.path + "?manager=assistant", nil},
			{crossPath, nil},
		} {
			if crossPath == route.path && request.header == nil {
				continue // the token manager's own request is the successful case below
			}
			if code, out := extDo(t, url, route.method, request.path, route.body, request.header); code != http.StatusForbidden || !strings.Contains(out, codeOtherManager) {
				t.Errorf("%s %s: another manager: %d %s", route.method, request.path, code, clip(out, 200))
			}
		}
		if got := len(reached(a)); got != beforeA {
			t.Errorf("%s %s: A was reached: %v", route.method, route.path, reached(a)[beforeA:])
		}
		if got := len(reached(b)); got != beforeB {
			t.Errorf("%s %s: a refused request reached B: %v", route.method, route.path, reached(b)[beforeB:])
		}
		if code, out := extDo(t, url, route.method, route.path, route.body, nil); code != http.StatusOK {
			t.Errorf("%s %s: own manager: %d %s", route.method, route.path, code, clip(out, 200))
		}
		calls := reached(b)[beforeB:]
		if len(calls) != 1 || !strings.HasPrefix(calls[0], assistantwire.MethodHTTP+" ") || !strings.Contains(calls[0], route.path) {
			t.Errorf("%s %s: did not reach only the token manager: %v", route.method, route.path, calls)
		}
		if got := len(reached(a)); got != beforeA {
			t.Errorf("%s %s: own request reached A: %v", route.method, route.path, reached(a)[beforeA:])
		}
	}
}

// The literal self-refresh route has no object id to route by. The authenticated extension token alone chooses its
// manager; explicitly naming another manager is not an alternate path to that manager's markets.
func TestExtensionSelfMarketRefreshStaysOnTokenManager(t *testing.T) {
	_, url, a, b := reachHub(t)
	beforeA, beforeB := len(reached(a)), len(reached(b))
	// guardManager answers RPCs but does not take an HTTP relay. Stop after the dispatch this test is about; the real
	// manager's immediate 202 and its background read are covered in extmgr.
	const path = "/api/v1/extensions/-/self/market/refresh"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+path, strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+extTokenPrefix+"bx-token")
	req.Header.Set("Content-Type", "application/json")
	answered := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			err = resp.Body.Close()
		}
		answered <- err
	}()
	eventually(t, 5*time.Second, "the token manager's self refresh", func() bool { return len(reached(b)) > beforeB })
	cancel()
	select {
	case <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled self refresh did not return")
	}
	if got := reached(a)[beforeA:]; len(got) != 0 {
		t.Fatalf("the other manager received the self refresh: %v", got)
	}
	calls := reached(b)[beforeB:]
	if len(calls) != 1 || !strings.HasPrefix(calls[0], extwire.MethodAccept+" ") {
		t.Fatalf("the token owner's manager did not receive exactly one self refresh: %v", calls)
	}
	for _, tc := range []struct {
		path   string
		header map[string]string
	}{
		{"/api/v1/extensions/-/self/market/refresh", map[string]string{ManagerHeader: assistantwire.AssistantID}},
		{"/api/v1/extensions/-/self/market/refresh?manager=" + assistantwire.AssistantID, nil},
	} {
		beforeA, beforeB = len(reached(a)), len(reached(b))
		if code, out := extDo(t, url, http.MethodPost, tc.path, "", tc.header); code != http.StatusForbidden || !strings.Contains(out, codeOtherManager) {
			t.Fatalf("naming the other manager: %d %s", code, out)
		}
		if len(reached(a)) != beforeA || len(reached(b)) != beforeB {
			t.Fatalf("a refused manager selector reached a manager: A %v, B %v", reached(a)[beforeA:], reached(b)[beforeB:])
		}
	}
}

// Every route an extension's token can open (extwire.RouteScopes) is bounded to its manager one way or another; a new
// one has to say how here. "path": its machine, session, card or conversation is in the path (extensionReach);
// "manager": it goes to the extension's own manager (requestManager, byConversation, byTodo, mergeList), which is held to its
// own machines like any manager; "own": the hub's own answer, limited to the manager's things; "body": the route
// checks what its body or query names itself.
func TestEveryExtensionRouteIsBounded(t *testing.T) {
	bounded := map[string]string{
		"GET /api/v1/kb/{kind}": "manager", "GET /api/v1/kb/{kind}/{id}": "manager", "GET /api/v1/kb/chats/{id}/context": "manager",
		"GET /api/v1/kb/chats/{id}/days/{day}": "manager", "GET /api/v1/kb/moments/{id}": "manager", "GET /api/v1/kb-search": "manager",
		"PUT /api/v1/kb/{kind}/{id}": "manager",
		"GET /api/v1/inbox":          "manager", "GET /api/v1/inbox/done": "manager", "GET /api/v1/inbox/done/days": "manager", "GET /api/v1/inbox/{id}": "manager", "GET /api/v1/inbox/mutes": "manager",
		"GET /api/v1/inbox/policies": "manager", "POST /api/v1/inbox/done": "manager", "POST /api/v1/inbox/bulk": "manager",
		"POST /api/v1/inbox/merge": "manager", "POST /api/v1/inbox/merge/undo": "manager", "POST /api/v1/inbox/todo": "manager", "POST /api/v1/inbox/todo/undo": "manager",
		"POST /api/v1/inbox/{id}/done": "manager", "POST /api/v1/inbox/{id}/draft": "manager", "POST /api/v1/inbox/{id}/reply": "manager",
		"POST /api/v1/inbox/{id}/suggestion": "manager", "POST /api/v1/inbox/{id}/handoff": "manager", "POST /api/v1/inbox/{id}/doc/status": "manager",
		"POST /api/v1/inbox/{id}/task-link/recheck": "manager", "POST /api/v1/inbox/{id}/retry": "manager", // relayed like done (inboxRoutesRelayed)
		"PUT /api/v1/inbox/{id}/parent": "manager", "GET /api/v1/inbox/tree/{kind}/{id...}": "manager",
		"PUT /api/v1/inbox/{id}/folder": "manager", "GET /api/v1/inbox/folders": "manager", "PUT /api/v1/inbox/folders": "manager",
		"POST /api/v1/inbox/mutes": "manager", "DELETE /api/v1/inbox/mutes/{id}": "manager", "POST /api/v1/inbox/policies": "manager",
		"PATCH /api/v1/inbox/policies/{id}": "manager", "DELETE /api/v1/inbox/policies/{id}": "manager", "POST /api/v1/inbox/refresh": "manager",
		"GET /api/v1/sessions/archived":                              "own+body",
		"GET /api/v1/sessions":                                       "own+body", // its machines; ?machine_id= another's: 403
		"GET /api/v1/sessions/{mid}/{sid}/info":                      "path",
		"GET /api/v1/sessions/{mid}/{sid}/items":                     "path",
		"GET /api/v1/sessions/{mid}/{sid}/items/{iid}/full":          "path",
		"GET /api/v1/sessions/{mid}/{sid}/items/{iid}/image":         "path",
		"GET /api/v1/sessions/{mid}/{sid}/items/{iid}/visualization": "path",
		"GET /api/v1/sessions/{mid}/{sid}/screen":                    "path",
		"GET /api/v1/interactions":                                   "own",
		"GET /api/v1/interactions/history":                           "manager",
		"GET /api/v1/agents":                                         "manager", "GET /api/v1/agents/sessions": "manager",
		"GET /api/v1/agents/sessions/{id}": "manager", "GET /api/v1/agents/sessions/{id}/messages": "manager",
		"GET /api/v1/agents/sessions/{id}/avatar": "manager",
		"GET /api/v1/saved-agent-searches":        "manager",
		"GET /api/v1/workspaces":                  "manager", "GET /api/v1/workspace-board": "manager",
		"GET /api/v1/workspace-board/activity": "manager", "GET /api/v1/workspace-board/activity/{revision}": "manager",
		"POST /api/v1/sessions":                            "body", // machine_id, resume_session_id: its manager's (startSession)
		"POST /api/v1/sessions/{mid}/{sid}/messages":       "path",
		"POST /api/v1/sessions/{mid}/{sid}/interrupt":      "path",
		"POST /api/v1/sessions/{mid}/{sid}/queue":          "path",
		"POST /api/v1/sessions/{mid}/{sid}/queue/send-now": "path",
		"POST /api/v1/sessions/{mid}/{sid}/seen":           "path",
		"POST /api/v1/sessions/{mid}/{sid}/rename":         "path",
		"POST /api/v1/sessions/{mid}/{sid}/archive-idle":   "path",
		"GET /api/v1/sessions/archive-idle/audit":          "manager",
		"POST /api/v1/sessions/{mid}/{sid}/modes":          "path", // and only of a conversation its extension opened (extensionModeRefused)
		"POST /api/v1/interactions/{id}/choose":            "path",
		"POST /api/v1/interactions/{id}/resolve":           "path",
		"POST /api/v1/interactions/{id}/reanswer":          "path", // pending: its card; no longer pending: its manager
		"POST /api/v1/agents/sessions/{id}/messages":       "manager",
		"POST /api/v1/agents/sessions/{id}/read":           "manager",
		"GET /api/v1/machines":                             "own",
		"GET /api/v1/machines/{id}/metrics":                "path",
		"GET /api/v1/machines/{id}/usage":                  "path",
		"GET /api/v1/machines/{mid}/stat":                  "path",
		"POST /api/v1/notify":                              "body",    // machine_id, session_id: its manager's (ownsRef)
		"GET /api/v1/push/intercepts":                      "manager", // only entries with its manager (pushGradingRoutes, TestPushGradingInterceptsHeldToManager)
		"GET /api/v1/push/ln-mode":                         "own",     // the hub-wide switch state, no manager's data in the answer
		"GET /api/v1/push/grading":                         "own",     // alias of the hub-wide switch state, same answer and authorization
		"GET /api/v1/push/grading/layer":                   "manager", // only the calling extension's own layer on its manager (pushGradingLayerRoutes)
		"PUT /api/v1/push/grading/layer":                   "manager", // writes only the calling extension's own layer on its manager (pushGradingLayerRoutes)
		"DELETE /api/v1/push/grading/layer":                "manager", // deletes only the calling extension's own layer on its manager (pushGradingLayerRoutes)
		"POST /api/v1/push/grading/preview":                "manager", // read-only preview bounded to the calling extension's manager (pushGradingLayerRoutes)
		"GET /api/v1/push/stats/daily":                     "manager", // only its manager's counts (pushGradingRoutes, TestPushDailyStatsHeldToManager)
		"GET /api/v1/assistant":                            "manager",
		"GET /api/v1/assistant/capabilities":               "manager",
		"GET /api/v1/assistant/entries":                    "manager", // TestAssistantEntriesStayOnTokenManager
		"POST /api/v1/assistant/conversations":             "manager",
		"GET /api/v1/assistant/conversations/{id}/feed":    "path",
		"POST /api/v1/assistant/conversations/{id}/stop":   "path",
		// The token manager owns the Spaces routes; category routes additionally follow the conversation in their path.
		"GET /api/v1/assistant/tracks": "manager", "POST /api/v1/assistant/tracks": "manager",
		"PUT /api/v1/assistant/tracks/{slug}": "manager", "GET /api/v1/assistant/track-kinds": "manager",
		"GET /api/v1/assistant/tracks/{slug}/space-md": "manager", "PUT /api/v1/assistant/tracks/{slug}/space-md": "manager",
		"GET /api/v1/assistant/tracks/{slug}/conversations":   "manager",
		"GET /api/v1/assistant/tracks/launchers":              "manager",
		"GET /api/v1/assistant/conversations/{id}/categories": "path",
		"PUT /api/v1/assistant/conversations/{id}/categories": "path",
		"GET /api/v1/assistant/reporters":                     "manager",
		"GET /api/v1/assistant/leaders":                       "manager",
		"POST /api/v1/assistant/directions":                   "manager", // create validates an optional body Agent against this manager
		"POST /api/v1/assistant/directions/{id}/progress":     "manager", // current-direction membership is manager-owned
		"GET /api/v1/assistant/dispatch-policy":               "manager",
		"PUT /api/v1/assistant/dispatch-policy":               "manager",
		"GET /api/v1/assistant/dispatch-guard":                "manager",
		"PUT /api/v1/assistant/dispatch-guard":                "manager",
		"GET /api/v1/assistant/worker-presets":                "manager",
		"GET /api/v1/assistant/start-defaults":                "manager",
		"PUT /api/v1/assistant/start-defaults":                "manager",
		"POST /api/v1/assistant/start-defaults/preview":       "manager",
		"GET /api/v1/assistant/machine-resources":             "manager",
		"GET /api/v1/assistant/placement/pending":             "manager",
		"POST /api/v1/assistant/placement/{todo_id}/assign":   "manager",
		"POST /api/v1/assistant/placement/{todo_id}/defer":    "manager",
		"GET /api/v1/todos":                                   "manager", "GET /api/v1/todos/{id}/decisions": "manager",
		"GET /api/v1/todos/{id}": "manager", "GET /api/v1/todos/places": "manager", // TestTodoViewRoutesStayOnTheirManager
		"GET /api/v1/todos/{id}/timeline":                     "manager", // TestTodoViewRoutesStayOnTheirManager
		"GET /api/v1/todos/{id}/repositories":                 "manager", // associations stay with the todo's manager
		"PUT /api/v1/todos/{id}/repositories":                 "manager",
		"PUT /api/v1/todos/{id}/repositories/{repo_id}/links": "manager",
		// The Todo's owner selects the manager; sample previews use the token's own manager.
		// The manager checks machine_id before resolving a preview or saving a plan.
		"GET /api/v1/todos/{id}/workspace-rules":                       "manager",
		"PUT /api/v1/todos/{id}/workspace-rules":                       "manager",
		"POST /api/v1/todos/{id}/workspace-naming/preview":             "manager",
		"POST /api/v1/todos/{id}/workspace-naming/plans":               "manager",
		"GET /api/v1/todos/{id}/workspace-naming/plans/{operation_id}": "manager",
		"POST /api/v1/assistant/workspace-naming/preview":              "manager",
		"POST /api/v1/todos":                                           "manager", "PATCH /api/v1/todos/{id}": "manager",
		// The paired bot is the owner's; the manager reads its public identity and bounds every handoff (bots_handoff.go).
		"GET /api/v1/bots/feishu/identity":                  "manager",
		"POST /api/v1/bots/feishu/handoffs":                 "manager",
		"POST /api/v1/decisions":                            "manager",
		"GET /api/v1/decisions/{id}":                        "manager",
		"POST /api/v1/decisions/{id}/withdraw":              "manager",
		"POST /api/v1/todos/{id}/decisions":                 "manager",
		"PUT /api/v1/todos/{id}/agents/{mid}/{sid}":         "path",
		"DELETE /api/v1/todos/{id}/agents/{mid}/{sid}":      "path",
		"PUT /api/v1/todos/{id}/spaces/{mid}/{space...}":    "path",
		"DELETE /api/v1/todos/{id}/spaces/{mid}/{space...}": "path",
		"POST /api/v1/extensions/-/self/market/refresh":     "manager",
		"/api/v1/extensions/{id}/":                          "manager",
	}
	for route := range extwire.RouteScopes {
		how, ok := bounded[route]
		if !ok {
			t.Errorf("%s is open to extensions: say here how it is held to the extension's manager (and check it)", route)
			continue
		}
		path := route
		if _, p, ok := strings.Cut(route, " "); ok {
			path = p
		}
		inPath := strings.Contains(path, "{mid}") || strings.HasPrefix(path, "/api/v1/machines/{id}") ||
			strings.HasPrefix(path, "/api/v1/interactions/{id}") || strings.HasPrefix(path, "/api/v1/assistant/conversations/{id}")
		if (how == "path") != inPath {
			t.Errorf("%s: said %q, but extensionReach sees its target in the path: %v", route, how, inPath)
		}
	}
	for route := range bounded {
		if _, ok := extwire.RouteScopes[route]; !ok {
			t.Errorf("%s is not open to extensions any more: take it out of this list", route)
		}
	}
}
