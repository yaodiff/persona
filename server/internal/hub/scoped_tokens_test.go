package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestScopedDeviceToken(t *testing.T) {
	h := newTestHub(t)
	h.store = testDB(t)
	machine := fakeNode(t, 3)
	machine.sessions = []proto.Session{
		{SessionID: "s1", Title: "Visible chat", Status: proto.StatusIdle, Cwd: "/private-dir"},
		{SessionID: "offline", Status: proto.StatusOffline},
		{SessionID: "health", Space: "health"},
	}
	machine.hub = h
	h.machines = append(h.machines, machine)
	h.byID[machine.cfg.ID] = machine
	mux := h.routes()
	master := h.cfg.Token

	for _, body := range []string{
		`{"scopes":null}`, `{"scopes":[]}`, `{"scopes":["files.read"]}`,
		`{"scopes":["agents.read","agents.read"]}`, `{"kind":"phone","scopes":["agents.read"]}`,
		`{"scopes":["agents.read"],"machines":["m1"]}`,
	} {
		if code, out := call(t, mux, "POST", "/api/v1/auth/tokens", master, body); code != http.StatusBadRequest {
			t.Fatalf("invalid issuance %s: %d %v", body, code, out)
		}
	}
	mint := func(scope string) (string, string) {
		t.Helper()
		code, out := call(t, mux, "POST", "/api/v1/auth/tokens", master, `{"name":"External","kind":"other","scopes":["`+scope+`"]}`)
		if code != http.StatusOK || out["role"] != roleScoped || !reflect.DeepEqual(out["scopes"], []any{scope}) {
			t.Fatalf("issuance: %d %v", code, out)
		}
		return out["id"].(string), out["token"].(string)
	}
	code, paired := call(t, mux, "POST", "/api/v1/auth/tokens", master, `{"kind":"phone"}`)
	if code != http.StatusOK {
		t.Fatalf("phone issuance: %d %v", code, paired)
	}
	if code, out := call(t, mux, "POST", "/api/v1/auth/tokens", paired["token"].(string), `{"scopes":["agents.read"]}`); code != http.StatusForbidden || out["code"] != "permission_denied" {
		t.Fatalf("a paired device minted Agent credentials: %d %v", code, out)
	}
	readID, readToken := mint("agents.read")
	t.Cleanup(func() { _, _ = h.store.RevokeDeviceToken(context.Background(), readID) })
	writeID, writeToken := mint("agents.control")
	t.Cleanup(func() { _, _ = h.store.RevokeDeviceToken(context.Background(), writeID) })
	if code, out := call(t, mux, "POST", "/api/v1/sessions", writeToken, `{"machine_id":"m1","permission_mode":"bypassPermissions"}`); code != http.StatusForbidden || out["scope"] != "agents.permission_mode" {
		t.Fatalf("control-only start with explicit mode: %d %v", code, out)
	}
	if code, out := call(t, mux, "POST", "/api/v1/sessions", writeToken, `{"machine_id":"m1","resume_session_id":"s1"}`); code != http.StatusForbidden || out["scope"] != "agents.permission_mode" {
		t.Fatalf("control-only resume: %d %v", code, out)
	}
	for _, test := range []struct{ path, body string }{
		{"/api/v1/sessions/m1/offline/messages", `{"text":"Check the result"}`},
		{"/api/v1/sessions/m1/s1/messages", `{"text":"Check the result","Permission_Mode":"default"}`},
	} {
		if code, out := call(t, mux, "POST", test.path, writeToken, test.body); code != http.StatusForbidden || out["scope"] != "agents.permission_mode" {
			t.Fatalf("scoped message requires a permission grant: %d %v", code, out)
		}
	}
	h.reported.mu.Lock()
	h.reported.interactions = map[string]proto.Interaction{
		"permission": {ID: "permission", MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission},
		"question":   {ID: "question", MachineID: "m1", SessionID: "s1", Kind: proto.IxQuestion},
	}
	h.reported.mu.Unlock()
	for _, test := range []struct{ action, card, body string }{
		{"choose", "permission", `{"option":0,"rev":"example"}`},
		{"resolve", "permission", `{"allow":true}`},
		{"resolve", "question", `{"allow":true}`},
		{"reanswer", "permission", `{"text":"example"}`},
	} {
		path := "/api/v1/interactions/" + test.card + "/" + test.action
		if code, out := call(t, mux, "POST", path, writeToken, test.body); code != http.StatusForbidden || out["code"] != "permission_denied" {
			t.Fatalf("%s: %d %v", path, code, out)
		}
	}
	if code, out := call(t, mux, "GET", "/api/v1/sessions/m1/health/items", readToken, ""); code != http.StatusNotFound {
		t.Fatalf("health transcript: %d %v", code, out)
	}
	if code, out := call(t, mux, "POST", "/api/v1/sessions/m1/s1/messages", writeToken, `{"text":"\u001b"}`); code != http.StatusForbidden || out["code"] != codeTerminalControls {
		t.Fatalf("terminal escape: %d %v", code, out)
	}
	for _, check := range []struct{ method, path, scope string }{
		{"GET", "/api/v1/machines/m1/files", ""},
		{"POST", "/api/v1/auth/tokens", ""},
		{"POST", "/api/v1/sessions", "agents.control"},
		{"POST", "/api/v1/sessions/m1/s1/modes", "agents.permission_mode"},
	} {
		code, body := call(t, mux, check.method, check.path, readToken, "{}")
		scope, _ := body["scope"].(string)
		if code != http.StatusForbidden || body["code"] != "permission_denied" || scope != check.scope {
			t.Fatalf("%s %s: %d %v", check.method, check.path, code, body)
		}
	}
	for _, path := range []string{"/api/v1/sessions", "/api/v1/machines/m1/launchers", "/api/v1/machines/m1/launchers/default/models"} {
		if code, out := call(t, mux, "GET", path, readToken, ""); code == http.StatusForbidden {
			t.Fatalf("blocked read %s: %d %v", path, code, out)
		}
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "203.0.113.10:12345"
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dial := func(token string) (*websocket.Conn, *http.Response, error) {
		return websocket.Dial(ctx, "wss"+strings.TrimPrefix(srv.URL, "https")+"/api/v1/ws?features=sessions_delta,session_live",
			&websocket.DialOptions{HTTPClient: srv.Client(), HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	}
	if conn, res, err := dial(writeToken); err == nil || res == nil || res.StatusCode != http.StatusForbidden {
		if conn != nil {
			_ = conn.CloseNow()
		}
		t.Fatalf("write-only WS: %v %v", res, err)
	}
	conn, _, err := dial(readToken)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	h.broadcast(map[string]any{"type": "inbox", "body": "private"})
	h.broadcast(map[string]any{"type": "items", "machine_id": "m1", "session_id": "s1", "body": "owner push"})
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","machine_id":"m1","session_id":"health","since_seq":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","machine_id":"m1","session_id":"s1","since_seq":0}`)); err != nil {
		t.Fatal(err)
	}
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event struct {
			Type    string `json:"type"`
			Backlog bool   `json:"backlog"`
			HasMore *bool  `json:"has_more"`
			Items   []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if json.Unmarshal(raw, &event) != nil || !scopedSocketEvent(event.Type) || strings.Contains(string(raw), "owner push") ||
			strings.Contains(string(raw), `"health"`) || strings.Contains(string(raw), "/private-dir") {
			t.Fatalf("unexpected event: %s", raw)
		}
		if event.Type == "items" {
			if !event.Backlog || event.HasMore == nil || !*event.HasMore || len(event.Items) != 2 || event.Items[0].ID != "i2" || event.Items[1].ID != "i3" {
				t.Fatalf("initial subscription dropped older-page availability: %s", raw)
			}
			break
		}
	}
	h.reported.mu.Lock()
	viewing := append([]assistantwire.SessionRef(nil), h.reported.clients.Viewing...)
	h.reported.mu.Unlock()
	if len(viewing) != 0 {
		t.Fatalf("a scoped subscription was reported as owner viewing: %+v", viewing)
	}
	h.broadcast(map[string]any{"type": "sessions", "machine_id": "m1", "sessions": []proto.Session{
		{SessionID: "s1", Title: "Changed title", Cwd: "/private-dir", Status: proto.StatusWorking},
		{SessionID: "health", Space: "health"},
	}})
	h.toSubscribers("m1", "s1", map[string]any{"type": "items", "machine_id": "m1", "session_id": "s1",
		"items": []proto.Item{
			{ID: "chat", Kind: "text", Role: "assistant", Text: "Visible reply", From: "private sender"},
			{ID: "tool", Kind: "tool_result", Role: "tool", Text: "private result", ToolInput: json.RawMessage(`{"data":"private data"}`)},
		}})
	gotSessions, gotItems := false, false
	for !gotSessions || !gotItems {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event struct {
			Type     string                 `json:"type"`
			Sessions []scopedSessionSummary `json:"sessions"`
			Items    []scopedChatItem       `json:"items"`
		}
		if err := json.Unmarshal(raw, &event); err != nil || strings.Contains(string(raw), "private") {
			t.Fatalf("bad scoped event: %s", raw)
		}
		switch event.Type {
		case "sessions":
			gotSessions = len(event.Sessions) == 1 && event.Sessions[0].Title == "Changed title"
		case "items":
			gotItems = len(event.Items) == 2 && event.Items[0].Text == "Visible reply" && event.Items[1].ID == "tool" && event.Items[1].Text == ""
		}
	}
	if code, out := call(t, mux, "DELETE", "/api/v1/auth/tokens/"+readID, master, ""); code != http.StatusOK {
		t.Fatalf("revoke: %d %v", code, out)
	}
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("revocation did not close the socket")
			}
			break
		}
	}
	if code, out := call(t, mux, "GET", "/api/v1/sessions", readToken, ""); code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d %v", code, out)
	}
}

func TestScopedDeviceAgentRouteScopes(t *testing.T) {
	h := &Hub{apiMux: http.NewServeMux()}
	h.extensionRoutesRelayed(h.apiMux)
	checked := 0
	for pattern, want := range extwire.RouteScopes {
		method, path, _ := strings.Cut(pattern, " ")
		if path != "/api/v1/agents" && !strings.HasPrefix(path, "/api/v1/agents/") {
			continue
		}
		path = strings.ReplaceAll(path, "{id}", "src%3Ak1")
		r := httptest.NewRequest(method, path, nil)
		if got := h.scopedDeviceRoute(r); got != want {
			t.Errorf("%s: got scope %q, want %q", pattern, got, want)
		}
		checked++
	}
	if checked != 7 {
		t.Fatalf("checked %d Agent routes, want all 7", checked)
	}
}

func TestScopedDeviceAgentRelay(t *testing.T) {
	h, _, _, srv := relayTestHub(t)
	mux := srv.Config.Handler
	mint := func(scope string) string {
		t.Helper()
		code, out := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"scopes":["`+scope+`"]}`)
		if code != http.StatusOK {
			t.Fatalf("issuance: %d %v", code, out)
		}
		return out["token"].(string)
	}
	read, write := mint("agents.read"), mint("agents.control")
	const list = "/api/v1/agents/sessions"
	const message = "/api/v1/agents/sessions/src%3Ak1/messages"
	for _, tc := range []struct {
		method, path, token, body, code string
		status                          int
	}{
		{"GET", list, read, "", "", http.StatusOK},
		{"GET", list, write, "", "permission_denied", http.StatusForbidden},
		{"POST", message, write, `{"text":"Check the result"}`, "", http.StatusOK},
		{"POST", message, read, `{"text":"Check the result"}`, "permission_denied", http.StatusForbidden},
		{"POST", message, write, `{"text":"\u001b"}`, codeTerminalControls, http.StatusForbidden},
	} {
		if status, out := call(t, mux, tc.method, tc.path, tc.token, tc.body); status != tc.status || tc.code != "" && out["code"] != tc.code {
			t.Fatalf("%s %s: %d %v", tc.method, tc.path, status, out)
		}
	}
}
