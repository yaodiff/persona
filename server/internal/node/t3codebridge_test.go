package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fakeT3Code is a T3 Code server's MCP endpoint and its t3 command line, as the bridge uses them (synthetic data).
type fakeT3Code struct {
	t     *testing.T
	token string
	srv   *httptest.Server

	mu          sync.Mutex
	initialized bool
	pinged      bool
	interrupts  []string
	archived    []string
	sends       []string // "<thread> <message>"
	starts      []string // "<project> <message>"
	revoked     []string

	threads   []map[string]any            // what t3_thread_list answers (t3codeFakeThreads when nil)
	questions map[string][]string         // thread -> the user-input requests it waits on
	countless map[string]bool             // threads whose read leaves pendingRequestCount out (an older server)
	asked     map[string][]t3codeQuestion // request -> its questions
	responds  []string                    // "<thread> <request> <answers as JSON>"
	listCalls int                         // t3_thread_list calls: one a refresh
}

// t3codeFakeThreads are the threads a fake T3 Code lists unless a test gives its own.
func t3codeFakeThreads() []map[string]any {
	return []map[string]any{
		{"threadId": "thr-1", "projectId": "proj-1", "title": "Tidy the build", "status": "running",
			"providerInstanceId": "claudeAgent", "model": "claude-sonnet", "runtimeMode": "full-access",
			"interactionMode": "default", "itemCount": 5, "updatedAt": "2026-11-20T10:00:00.000Z"},
		{"threadId": "thr-2", "projectId": "proj-2", "title": "", "status": "waiting",
			"providerInstanceId": "codex", "model": "gpt-5", "runtimeMode": "approval-required",
			"interactionMode": "plan", "itemCount": 0, "updatedAt": "2026-11-20T09:00:00.000Z"},
	}
}

func newFakeT3Code(t *testing.T) *fakeT3Code {
	f := &fakeT3Code{t: t, token: "synthetic-t3-token", questions: map[string][]string{},
		countless: map[string]bool{"thr-2": true}, asked: map[string][]t3codeQuestion{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeT3Code) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mcp" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Header.Get("Origin") != "" {
		w.WriteHeader(http.StatusForbidden) // T3 Code refuses browser origins
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusOK)
		return
	}
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
		Result json.RawMessage `json:"result"`
	}
	_ = json.NewDecoder(r.Body).Decode(&msg)
	switch {
	case msg.Method == "" && len(msg.Result) > 0: // the client's answer to the ping
		f.mu.Lock()
		f.pinged = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		return
	case msg.Method == "initialize":
		w.Header().Set("Mcp-Session-Id", "mcp-session-1")
		writeT3Result(w, msg.ID, map[string]any{"protocolVersion": t3codeMCPProtocol, "capabilities": map[string]any{}})
		return
	case msg.Method == "notifications/initialized":
		f.mu.Lock()
		f.initialized = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		return
	case r.Header.Get("Mcp-Session-Id") != "mcp-session-1":
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	args := msg.Params.Arguments
	switch msg.Params.Name {
	case "t3_project_list":
		writeT3Tool(w, msg.ID, map[string]any{"projects": []map[string]any{
			{"id": "proj-1", "title": "Demo App", "workspaceRoot": "/code/demo"},
			{"id": "proj-2", "title": "Demo App Docs", "workspaceRoot": "/code/demo/docs"},
		}, "nextCursor": nil})
	case "t3_thread_list":
		f.mu.Lock()
		threads := f.threads
		f.listCalls++
		f.mu.Unlock()
		if threads == nil {
			threads = t3codeFakeThreads()
		}
		writeT3Tool(w, msg.ID, map[string]any{"threads": threads, "nextCursor": nil, "total": len(threads)})
	case "t3_thread_read":
		f.read(w, msg.ID, args)
	case "t3_pending_request":
		f.pendingRequest(w, msg.ID, args)
	case "t3_thread_interrupt":
		tid, _ := args["threadId"].(string)
		if tid == "thr-1" { // full access, above a CLI-issued session's ceiling
			writeT3Failure(w, msg.ID, "runtime_mode_escalation_denied", "This session's access is approval-required.")
			return
		}
		f.mu.Lock()
		f.interrupts = append(f.interrupts, tid)
		f.mu.Unlock()
		writeT3Tool(w, msg.ID, map[string]any{"threadId": tid, "runId": "run-2", "status": "interrupt_requested"})
	case "t3_thread_organize":
		f.mu.Lock()
		f.archived = append(f.archived, fmt.Sprint(args["threadId"], " ", args["action"]))
		f.mu.Unlock()
		writeT3Tool(w, msg.ID, map[string]any{"sequence": 1})
	default:
		writeT3Failure(w, msg.ID, "invalid_request", "unknown tool")
	}
}

// read answers t3_thread_read as an event stream, with a ping before the answer.
func (f *fakeT3Code) read(w http.ResponseWriter, id json.RawMessage, args map[string]any) {
	text := func(s string) *string { return &s }
	all := []map[string]any{
		{"position": 0, "itemId": "i0", "type": "user_message", "status": "completed", "text": text("Tidy the build please"), "updatedAt": "2026-11-20T09:59:00.000Z"},
		{"position": 1, "itemId": "i1", "type": "reasoning", "status": "completed", "text": text("Looking at the Makefile"), "updatedAt": "2026-11-20T09:59:01.000Z"},
		{"position": 2, "itemId": "i2", "type": "command_execution", "status": "completed", "title": text("make lint"), "text": text("ok"), "updatedAt": "2026-11-20T09:59:02.000Z"},
		{"position": 3, "itemId": "i3", "type": "checkpoint", "status": "completed", "updatedAt": "2026-11-20T09:59:03.000Z"},
		{"position": 4, "itemId": "i4", "type": "assistant_message", "status": "completed", "text": text("The build is tidy."), "updatedAt": "2026-11-20T09:59:04.000Z"},
	}
	after := -1
	if v, ok := args["afterPosition"].(float64); ok {
		after = int(v)
	}
	limit := 100
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
	}
	var page []map[string]any
	for _, it := range all {
		if it["position"].(int) > after && len(page) < limit {
			page = append(page, it)
		}
	}
	var next any
	if len(page) > 0 {
		next = page[len(page)-1]["position"]
	}
	tid, _ := args["threadId"].(string)
	thread := map[string]any{"threadId": tid, "itemCount": len(all)}
	f.mu.Lock()
	if !f.countless[tid] {
		thread["pendingRequestCount"] = len(f.questions[tid])
	}
	f.mu.Unlock()
	res := map[string]any{"thread": thread,
		"recentRuns": []any{}, "items": page, "nextPosition": next,
		"hasMore": len(page) > 0 && page[len(page)-1]["position"].(int) < len(all)-1}
	raw, _ := json.Marshal(res)
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"srv-1\",\"method\":\"ping\"}\n\n")
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(raw)}}, "structuredContent": json.RawMessage(raw)}})
	_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", body)
}

// pendingRequest answers t3_pending_request as T3 Code does for a session held to approval-required: it lists and reads
// a thread's questions, and takes an answer only in a thread at that mode.
func (f *fakeT3Code) pendingRequest(w http.ResponseWriter, id json.RawMessage, args map[string]any) {
	tid, _ := args["threadId"].(string)
	req, _ := args["requestId"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	waiting := slices.Contains(f.questions[tid], req)
	switch args["action"] {
	case "list":
		writeT3Tool(w, id, map[string]any{"requestIds": append([]string{}, f.questions[tid]...)})
	case "read":
		if !waiting {
			writeT3Failure(w, id, "invalid_request", "The pending user-input request was not found.")
			return
		}
		writeT3Tool(w, id, map[string]any{"requestId": req, "questions": f.asked[req]})
	case "respond":
		if tid == "thr-1" { // full access, above the session's ceiling
			writeT3Failure(w, id, "runtime_mode_escalation_denied", "This session's access is approval-required.")
			return
		}
		if !waiting {
			writeT3Failure(w, id, "invalid_request", "The user-input request was already answered.")
			return
		}
		raw, _ := json.Marshal(args["answers"])
		f.responds = append(f.responds, tid+" "+req+" "+string(raw))
		f.questions[tid] = slices.DeleteFunc(f.questions[tid], func(s string) bool { return s == req })
		writeT3Tool(w, id, map[string]any{"sequence": 7})
	default:
		writeT3Failure(w, id, "invalid_request", "unknown action")
	}
}

func writeT3Result(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeT3Tool(w http.ResponseWriter, id json.RawMessage, structured any) {
	raw, _ := json.Marshal(structured)
	writeT3Result(w, id, map[string]any{"content": []map[string]any{{"type": "text", "text": string(raw)}}, "structuredContent": structured})
}

func writeT3Failure(w http.ResponseWriter, id json.RawMessage, code, message string) {
	raw, _ := json.Marshal(map[string]any{"_tag": "OrchestratorMcpFailure", "code": code, "message": message})
	writeT3Result(w, id, map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": string(raw)}}})
}

// cli stands in for the t3 command line.
func (f *fakeT3Code) cli(_ context.Context, stdin string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	joined := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(joined, "auth session issue "):
		if !strings.Contains(joined, "--subject mcp-client") || !strings.Contains(joined, "--json") {
			return nil, errors.New("t3 auth: unexpected flags " + joined)
		}
		return json.Marshal(map[string]any{"sessionId": "sess-1", "token": f.token, "expiresAt": "2026-11-28T00:00:00.000Z"})
	case strings.HasPrefix(joined, "auth session revoke "):
		f.revoked = append(f.revoked, args[len(args)-1])
		return []byte("{}"), nil
	case strings.HasPrefix(joined, "thread send --json "):
		f.sends = append(f.sends, args[3]+" "+stdin)
		return json.Marshal(map[string]any{"threadId": args[3], "delivery": "queued"})
	case strings.HasPrefix(joined, "thread start --json "):
		f.starts = append(f.starts, args[3]+" "+stdin)
		return json.Marshal(map[string]any{"threadId": "thr-new", "projectId": args[3], "runId": "run-9"})
	}
	return nil, errors.New("t3: unexpected command " + joined)
}

// useFakeT3Code points the bridge's discovery and command line at f: a T3 home whose state names f's server.
func useFakeT3Code(t *testing.T, f *fakeT3Code) {
	home := t.TempDir()
	t.Setenv("T3CODE_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "userdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, _ := json.Marshal(map[string]any{"version": 1, "pid": os.Getpid(), "port": 1, "origin": f.srv.URL, "serverVersion": "0.1.11"})
	if err := os.WriteFile(filepath.Join(home, "userdata", "server-runtime.json"), st, 0o600); err != nil {
		t.Fatal(err)
	}
	oldBin, oldRun, oldAlive := t3codeBin, t3codeRun, t3codeAlive
	oldPoll, oldRestart := t3codeBridgePoll, t3codeBridgeRestart
	t3codeBin = func() string { return "/fake/bin/t3" }
	t3codeRun = f.cli
	t3codeAlive = func(int) bool { return true }
	t3codeBridgePoll, t3codeBridgeRestart = 30*time.Millisecond, 30*time.Millisecond
	t.Cleanup(func() {
		t3codeBin, t3codeRun, t3codeAlive = oldBin, oldRun, oldAlive
		t3codeBridgePoll, t3codeBridgeRestart = oldPoll, oldRestart
	})
}

func TestT3CodeEngineDetected(t *testing.T) {
	n, _ := runtimeNode(t)
	t.Setenv("T3CODE_HOME", t.TempDir())
	oldBin := t3codeBin
	t.Cleanup(func() { t3codeBin = oldBin })
	t3codeBin = func() string { return "" }
	if e := n.t3codeEngine(context.Background()); e.ID != proto.EngineT3Code || e.Name != "T3 Code" || e.Available {
		t.Fatalf("without T3 Code: %+v", e)
	}
	t3codeBin = func() string { return "/fake/bin/t3" }
	if e := n.t3codeEngine(context.Background()); !e.Available || e.Version != "" {
		t.Fatalf("t3 installed, server not running: %+v", e)
	}

	f := newFakeT3Code(t)
	useFakeT3Code(t, f)
	t3codeBin = func() string { return "" }
	if e := n.t3codeEngine(context.Background()); !e.Available || e.Version != "0.1.11" || !strings.Contains(e.Note, "t3") {
		t.Fatalf("server running without the t3 command line: %+v", e)
	}

	var found bool
	for _, x := range n.engines(context.Background(), "") {
		found = found || x.ID == proto.EngineT3Code
	}
	if !found {
		t.Fatal("node.info's engines leave T3 Code out")
	}
}

func TestT3CodeServerStateNeedsLoopbackAndALiveProcess(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "userdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(origin string) {
		st, _ := json.Marshal(map[string]any{"version": 1, "pid": os.Getpid(), "port": 3773, "origin": origin, "serverVersion": "0.1.11"})
		if err := os.WriteFile(filepath.Join(home, "userdata", "server-runtime.json"), st, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldAlive := t3codeAlive
	t.Cleanup(func() { t3codeAlive = oldAlive })
	t3codeAlive = func(int) bool { return true }
	for origin, want := range map[string]bool{
		"http://127.0.0.1:3773": true,
		"http://localhost:3773": true,
		"http://[::1]:3773":     true,
		"http://192.0.2.7:3773": false, // the bridge sends its session only to this computer
		"https://127.0.0.1:1":   false,
		"":                      false,
		// User info that names this computer while the request goes elsewhere (the session token would leave it).
		"http://localhost:80@evil.example":      false,
		"http://127.0.0.1:1@evil.example":       false,
		"http://localhost:3773@evil.example:80": false,
		"http://[::1]:3773@evil.example":        false,
	} {
		write(origin)
		if _, ok := t3codeServerOf(home); ok != want {
			t.Errorf("origin %q: found %v, want %v", origin, ok, want)
		}
	}
	write("http://127.0.0.1:3773")
	t3codeAlive = func(int) bool { return false }
	if _, ok := t3codeServerOf(home); ok {
		t.Error("a server whose process is gone is still found")
	}
}

// The MCP endpoint is built from the parsed origin, and only for a plain http origin on this computer: nothing in the
// origin may send the request, and with it the bridge's session token, anywhere else.
func TestT3CodeMCPEndpointOnlyForAPlainLoopbackOrigin(t *testing.T) {
	for origin, want := range map[string]string{
		"http://127.0.0.1:3773":  "http://127.0.0.1:3773/mcp",
		"http://127.0.0.1:3773/": "http://127.0.0.1:3773/mcp",
		"http://localhost:3773":  "http://localhost:3773/mcp",
		"http://[::1]:3773":      "http://[::1]:3773/mcp",
		"http://127.0.0.1":       "http://127.0.0.1/mcp",
		"http://127.8.9.10:3773": "http://127.8.9.10:3773/mcp",
		// User info: the host the request goes to is the one after the @.
		"http://localhost:80@evil.example":      "",
		"http://127.0.0.1:1@evil.example":       "",
		"http://localhost:3773@evil.example:80": "",
		"http://[::1]:3773@evil.example":        "",
		"http://user@127.0.0.1:3773":            "", // loopback, but no user info is ever expected
		"http://user:pass@localhost:3773":       "",
		// Not this computer, or not plain http.
		"http://evil.example:3773":           "",
		"http://192.0.2.7:3773":              "",
		"http://localhost.evil.example:3773": "",
		"http://127.0.0.1.evil.example":      "",
		"https://127.0.0.1:3773":             "",
		"HTTP://127.0.0.1:3773":              "http://127.0.0.1:3773/mcp", // the scheme is case-insensitive and parsed lower
		"ws://127.0.0.1:3773":                "",
		"http:127.0.0.1:3773":                "",
		"//127.0.0.1:3773":                   "",
		"127.0.0.1:3773":                     "",
		"":                                   "",
		// Anything after the host that the endpoint would have been joined onto.
		"http://127.0.0.1:3773/evil":           "",
		"http://127.0.0.1:3773?x=1":            "",
		"http://127.0.0.1:3773?":               "",
		"http://127.0.0.1:3773#frag":           "",
		"http://127.0.0.1:3773/%2e%2e":         "",
		"http://localhost:x@evil.example":      "",
		"http://127.0.0.1:3773\\@evil.example": "",
		"http://[::1%25eth0]:3773":             "",
	} {
		got, ok := t3codeMCPEndpoint(origin)
		if ok != (want != "") || got != want {
			t.Errorf("origin %q: endpoint %q (ok %v), want %q", origin, got, ok, want)
		}
	}
}

func TestT3CodeMappings(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"approval-required", "default"}: "default",
		{"auto-accept-edits", "default"}: "acceptEdits",
		{"auto", "default"}:              "auto",
		{"full-access", "default"}:       proto.ModeBypass,
		{"full-access", "plan"}:          "plan",
		{"", ""}:                         "",
	} {
		if got := t3codeMode(in[0], in[1]); got != want {
			t.Errorf("t3codeMode(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
	for in, want := range map[string]string{"claudeAgent": "claude", "codex": "codex", "opencode": "opencode", "pi": "pi", "": "codex"} {
		if got := t3codeAgent(in); got != want {
			t.Errorf("t3codeAgent(%q) = %q, want %q", in, got, want)
		}
	}
	none, one := 0, 1
	for _, c := range []struct {
		status  string
		items   int
		pending *int
		want    string
	}{
		{"idle", 0, nil, proto.StatusIdle},
		{"idle", 3, nil, proto.StatusDone},
		{"starting", 0, nil, proto.StatusWorking},
		{"running", 4, &none, proto.StatusWorking},
		// A run that asks keeps running in T3 Code: only the count says it waits on the user.
		{"running", 4, &one, proto.StatusBlocked},
		{"queued", 4, &one, proto.StatusBlocked},
		// "waiting" is background work the run still holds, not the user's turn.
		{"waiting", 4, &none, proto.StatusWorking},
		{"waiting", 4, &one, proto.StatusBlocked},
		// A server that does not count: "waiting" is the only sign left.
		{"waiting", 3, nil, proto.StatusBlocked},
		{"completed", 3, nil, proto.StatusDone},
	} {
		if got := t3codeStatus(c.status, c.items, c.pending); got != c.want {
			t.Errorf("t3codeStatus(%q, %d, %v) = %q, want %q", c.status, c.items, c.pending, got, c.want)
		}
	}
	if t3codeSessionID("") != "" || t3codeSessionID("bad id") != "" || t3codeSessionID("thr_1") != "t3code-thr_1" {
		t.Error("t3codeSessionID")
	}
}

func TestT3CodeBridgeSessionsAndOperations(t *testing.T) {
	f := newFakeT3Code(t)
	useFakeT3Code(t, f)
	n, _ := runtimeNode(t)
	if _, err := nodeCall(t, n, "engine.use", map[string]any{"engine": proto.EngineT3Code}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.runtimeHost.setT3CodeBridge(false) })

	waitFor(t, "T3 Code threads listed", func() bool {
		m := listed(t, n)
		return m["t3code-thr-1"].SessionID != "" && m["t3code-thr-2"].SessionID != ""
	})
	got := listed(t, n)
	s1, s2 := got["t3code-thr-1"], got["t3code-thr-2"]
	if s1.Host != proto.EngineT3Code || s1.Title != "Tidy the build" || s1.Cwd != "/code/demo" || s1.SpaceName != "Demo App" ||
		s1.Agent != "claude" || s1.Status != proto.StatusWorking || s1.PermissionMode != proto.ModeBypass || s1.Model != "claude-sonnet" {
		t.Fatalf("thr-1 = %+v", s1)
	}
	if s2.Title != "Demo App Docs" || s2.Status != proto.StatusBlocked || s2.PermissionMode != "plan" || s2.Agent != "codex" {
		t.Fatalf("thr-2 = %+v", s2)
	}

	raw, err := nodeCall(t, n, "session.items", map[string]any{"session_id": "t3code-thr-1", "limit": 50})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items   []proto.Item `json:"items"`
		ToSeq   int          `json:"to_seq"`
		HasMore bool         `json:"has_more"`
	}
	_ = json.Unmarshal(raw, &page)
	if page.ToSeq != 5 || page.HasMore || len(page.Items) != 4 {
		t.Fatalf("page = %+v", page)
	}
	if it := page.Items[0]; it.Role != "user" || it.Kind != proto.KindText || it.Text != "Tidy the build please" || it.Seq != 1 {
		t.Fatalf("first item = %+v", it)
	}
	if it := page.Items[1]; it.Kind != proto.KindThinking {
		t.Fatalf("reasoning = %+v", it)
	}
	if it := page.Items[2]; it.Kind != proto.KindToolUse || it.ToolName != "make lint" {
		t.Fatalf("command = %+v", it)
	}
	if it := page.Items[3]; it.Role != "assistant" || it.Text != "The build is tidy." || it.Seq != 5 {
		t.Fatalf("last item = %+v", it)
	}

	raw, err = nodeCall(t, n, "session.items", map[string]any{"session_id": "t3code-thr-1", "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	page.Items = nil
	_ = json.Unmarshal(raw, &page)
	if !page.HasMore || len(page.Items) != 1 || page.Items[0].Seq != 5 {
		t.Fatalf("last two positions = %+v", page)
	}

	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": "t3code-thr-1", "text": "also the tests", "client_msg_id": "msg-1"}); err != nil {
		t.Fatal(err)
	}
	_, err = nodeCall(t, n, "session.interrupt", map[string]any{"session_id": "t3code-thr-1"})
	if err == nil || !strings.Contains(err.Error(), "T3 Code") {
		t.Fatalf("interrupting a full-access thread: %v, want the refusal that points to T3 Code", err)
	}
	if _, err := nodeCall(t, n, "session.interrupt", map[string]any{"session_id": "t3code-thr-2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "session.close", map[string]any{"session_id": "t3code-thr-2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/code/demo/docs/guide", "prompt": "Write the guide"}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/elsewhere", "prompt": "Nothing"}); err == nil {
		t.Fatal("a start outside every T3 project was taken")
	}

	f.mu.Lock()
	if !f.initialized || !f.pinged {
		t.Errorf("initialized=%v pinged=%v", f.initialized, f.pinged)
	}
	if len(f.sends) != 1 || f.sends[0] != "thr-1 also the tests" {
		t.Errorf("sends = %q", f.sends)
	}
	if len(f.interrupts) != 1 || f.interrupts[0] != "thr-2" || len(f.archived) != 1 || f.archived[0] != "thr-2 archive" {
		t.Errorf("interrupts = %q archived = %q", f.interrupts, f.archived)
	}
	if len(f.starts) != 1 || f.starts[0] != "proj-2 Write the guide" { // the deepest project holding the folder
		t.Errorf("starts = %q", f.starts)
	}
	f.mu.Unlock()

	n.runtimeHost.setT3CodeBridge(false)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.revoked) == 0 || f.revoked[0] != "sess-1" {
		t.Errorf("the bridge's session was not revoked when it stopped: %q", f.revoked)
	}
}

// T3 Code takes no pictures through the bridge: a send or start with them is refused, never answered ok after dropping
// them (#11084).
func TestT3CodeImageInputIsExplicitlyUnsupported(t *testing.T) {
	b := newT3CodeBridge(nil, nil, t.Logf)
	for _, method := range []string{"session.send", "session.start"} {
		params, _ := json.Marshal(map[string]any{"session_id": "t3code-thr-1", "text": "read this", "prompt": "read this",
			"images": []map[string]any{{"mime": "image/png", "data": "aW1hZ2U="}}})
		_, err := b.request(context.Background(), method, params)
		var rpcErr *rpc.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != "image_input_unsupported" {
			t.Fatalf("%s: %v, want image_input_unsupported", method, err)
		}
	}
}

// A dynamic_tool item shows under its own tool's name with its input, and a user-input request as its questions and
// options (or its answers), never as the JSON T3 Code reads them out as.
func TestT3CodeItemsShowToolsAndQuestionsReadably(t *testing.T) {
	item := func(typ, text string) proto.Item {
		t.Helper()
		it, ok := t3codeItem(t3codeTimelineItem{Position: 3, ItemID: "i3", Type: typ, Status: "running", Text: &text,
			UpdatedAt: "2026-11-20T09:59:03.000Z"})
		if !ok {
			t.Fatalf("%s item dropped", typ)
		}
		return it
	}

	ask := item("dynamic_tool", `{"toolName": "AskUserQuestion", "input": {"questions": [{"question": "Which folder should it scan?",
		"header": "Folder", "options": [{"label": "A new one", "description": "Only what you save there"}], "multiSelect": false}]}}`)
	if ask.Kind != proto.KindToolUse || ask.ToolName != "AskUserQuestion" || ask.Text != "" ||
		!strings.Contains(string(ask.ToolInput), `"Which folder should it scan?"`) {
		t.Fatalf("AskUserQuestion call = %+v", ask)
	}
	if it := item("dynamic_tool", `{"toolName": "ToolSearch", "input": {"query": "select:x"}, "output": "found x"}`); it.ToolName != "ToolSearch" ||
		it.Text != "found x" || string(it.ToolInput) != `{"query": "select:x"}` {
		t.Fatalf("call with a text output = %+v", it)
	}
	if it := item("dynamic_tool", `{"toolName": "mcp__docs__search", "input": {}, "output": {"hits": 2}}`); it.ToolName != "mcp__docs__search" ||
		it.Text != "{\n  \"hits\": 2\n}" {
		t.Fatalf("call with a structured output = %+v", it)
	}
	cut := `{"toolName": "Bash", "input": {"command": "ls`
	if it := item("dynamic_tool", cut); it.ToolName != "Tool" || it.Text != cut || it.ToolInput != nil {
		t.Fatalf("a page cut short = %+v", it)
	}

	questions := `[{"id": "q1", "header": "Folder", "question": "Which folder should it scan?", "options": [
		{"label": "A new one (Recommended)", "description": "Only what you save there"}, {"label": "Default", "description": ""}],
		"multiSelect": false}, {"id": "q2", "header": "Kinds", "question": "Kinds", "options": [{"label": "Video", "description": "Clips"}],
		"multiSelect": true}]`
	pending := item("user_input_request", questions)
	want := "Folder\nWhich folder should it scan?\n· A new one (Recommended) — Only what you save there\n· Default\n\nKinds\n· Video — Clips"
	if pending.Kind != proto.KindNotice || pending.Text != want {
		t.Fatalf("waiting question = %q, want %q", pending.Text, want)
	}
	answered := item("user_input_request", `{"answers": {"q1": "A new one (Recommended)", "q2": ["Video", "Audio"]}, "questions": `+questions+`}`)
	want = "Folder\nWhich folder should it scan?\n→ A new one (Recommended)\n\nKinds\n→ Video, Audio"
	if answered.Text != want {
		t.Fatalf("answered question = %q, want %q", answered.Text, want)
	}
	if it := item("user_input_request", `[{"id": "q1", "quest`); it.Text != `[{"id": "q1", "quest` {
		t.Fatalf("a page cut short = %q", it.Text)
	}
}

// A card's answers go back to T3 Code keyed by question id, as option values where an option has one, a multi-select
// as a list, and typed text only where the question takes it.
func TestT3CodeAnswersFromACard(t *testing.T) {
	var questions []t3codeQuestion
	if err := json.Unmarshal([]byte(`[
		{"id": "q1", "question": "Which folder?", "options": [{"label": "A new one", "description": "", "value": "new"}, {"label": "Default", "description": ""}]},
		{"id": "q2", "question": "Which kinds?", "multiSelect": true, "options": [{"label": "Video", "description": ""}, {"label": "Audio", "description": ""}]},
		{"id": "q3", "question": "Keep the originals?", "allowCustomAnswer": false, "options": [{"label": "Yes", "description": ""}]},
		{"id": "q4", "question": "Anything else?", "options": []},
		{"id": "q5", "header": "Size", "question": "", "multiSelect": true, "options": [{"label": "Small", "description": ""}]},
		{"id": "q6", "question": "Unanswered", "options": []}
	]`), &questions); err != nil {
		t.Fatal(err)
	}
	got := t3codeAnswers(questions, map[string]string{
		"Which folder?":       "A new one",
		"Which kinds?":        "Video, Audio",
		"Keep the originals?": "Only the best ones",
		"Anything else?":      "  Name it Saved  ",
		"Size":                "Whatever fits",
	})
	raw, _ := json.Marshal(got)
	if want := `{"q1":"new","q2":["Video","Audio"],"q4":"Name it Saved","q5":"Whatever fits"}`; string(raw) != want {
		t.Fatalf("answers = %s, want %s", raw, want)
	}
	ix := t3codeInteraction("t3code-thr-3", questions)
	if ix == nil || ix.Kind != proto.IxQuestion || len(ix.Questions) != 6 || ix.Questions[4].Question != "Size" ||
		ix.Questions[4].Header != "" || !ix.Questions[1].MultiSelect || ix.Questions[0].Options[0].Label != "A new one" {
		t.Fatalf("card = %+v", ix)
	}
	if t3codeInteraction("t3code-thr-3", nil) != nil {
		t.Fatal("a card without questions")
	}
}

// t3codeQuestionThreads: a full-access thread and an approval-required one, both running.
func t3codeQuestionThreads() []map[string]any {
	return []map[string]any{
		{"threadId": "thr-1", "projectId": "proj-1", "title": "Tidy the build", "status": "running",
			"providerInstanceId": "claudeAgent", "model": "claude-sonnet", "runtimeMode": "full-access",
			"interactionMode": "default", "itemCount": 5, "updatedAt": "2026-11-20T10:00:00.000Z"},
		{"threadId": "thr-3", "projectId": "proj-1", "title": "Save the clips", "status": "running",
			"providerInstanceId": "claudeAgent", "model": "claude-sonnet", "runtimeMode": "approval-required",
			"interactionMode": "default", "itemCount": 5, "updatedAt": "2026-11-20T10:00:00.000Z"},
	}
}

func t3codeFakeQuestions() []t3codeQuestion {
	var qs []t3codeQuestion
	_ = json.Unmarshal([]byte(`[
		{"id": "q1", "header": "Folder", "question": "Which folder should it scan?", "options": [
			{"label": "A new one", "description": "Only what you save there", "value": "new"},
			{"label": "Default", "description": "Everything you save"}]},
		{"id": "q2", "header": "Kinds", "question": "Which kinds?", "multiSelect": true, "options": [
			{"label": "Video", "description": ""}, {"label": "Audio", "description": ""}]}]`), &qs)
	return qs
}

func startT3CodeQuestions(t *testing.T, f *fakeT3Code, resolve func(proto.Interaction) proto.Resolution) (*Node, *runtimeHub) {
	t.Helper()
	useFakeT3Code(t, f)
	n, hub := runtimeNode(t)
	hub.mu.Lock()
	hub.resolve = resolve
	hub.mu.Unlock()
	if _, err := nodeCall(t, n, "engine.use", map[string]any{"engine": proto.EngineT3Code}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.runtimeHost.setT3CodeBridge(false) })
	return n, hub
}

func (h *runtimeHub) askedCards() []proto.Interaction {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]proto.Interaction{}, h.asked...)
}

// A thread that asks shows as Needs you while it waits, though its run keeps running in T3 Code. In a thread at
// approval-required the question becomes a card, and its answer goes back to T3 Code; a full-access thread's question
// gets no card, since T3 Code would refuse the bridge's answer.
func TestT3CodeQuestionsAreNeedsYouAndAnsweredFromACard(t *testing.T) {
	f := newFakeT3Code(t)
	f.threads = t3codeQuestionThreads()
	f.questions["thr-1"], f.questions["thr-3"] = []string{"req-full"}, []string{"req-1"}
	f.asked["req-full"], f.asked["req-1"] = t3codeFakeQuestions(), t3codeFakeQuestions()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	n, hub := startT3CodeQuestions(t, f, func(ix proto.Interaction) proto.Resolution {
		<-release
		return proto.Resolution{Answers: map[string]string{"Which folder should it scan?": "A new one", "Which kinds?": "Video, Audio"}}
	})

	waitFor(t, "both threads as Needs you", func() bool {
		m := listed(t, n)
		return m["t3code-thr-1"].Status == proto.StatusBlocked && m["t3code-thr-3"].Status == proto.StatusBlocked
	})
	waitFor(t, "the approval-required thread's card", func() bool { return len(hub.askedCards()) > 0 })
	release <- struct{}{}
	waitFor(t, "the answer in T3 Code", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.responds) > 0
	})
	waitFor(t, "the answered thread back at work", func() bool { return listed(t, n)["t3code-thr-3"].Status == proto.StatusWorking })

	f.mu.Lock()
	if want := `thr-3 req-1 {"q1":"new","q2":["Video","Audio"]}`; len(f.responds) != 1 || f.responds[0] != want {
		t.Errorf("responds = %q, want %q", f.responds, want)
	}
	f.mu.Unlock()
	cards := hub.askedCards()
	if len(cards) != 1 {
		t.Fatalf("cards = %+v, want only the approval-required thread's", cards)
	}
	if c := cards[0]; c.ID != t3codeAskID("thr-3", "req-1") || c.SessionID != "t3code-thr-3" || c.Kind != proto.IxQuestion ||
		len(c.Questions) != 2 || c.Questions[0].Header != "Folder" || c.Questions[0].Options[1].Description != "Everything you save" {
		t.Fatalf("card = %+v", c)
	}
	if s := listed(t, n)["t3code-thr-1"]; s.Status != proto.StatusBlocked {
		t.Fatalf("the full-access thread still waits on T3 Code: %+v", s)
	}
}

// A question answered in T3 Code takes its card back from the phone; one handed back to the computer is left to T3
// Code and not asked again.
func TestT3CodeQuestionCardWithdrawnOrHandedBack(t *testing.T) {
	f := newFakeT3Code(t)
	f.threads = t3codeQuestionThreads()[1:]
	f.questions["thr-3"] = []string{"req-1"}
	f.asked["req-1"] = t3codeFakeQuestions()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	n, hub := startT3CodeQuestions(t, f, func(ix proto.Interaction) proto.Resolution {
		if ix.ID == t3codeAskID("thr-3", "req-1") {
			<-release // the card stays up
		}
		return proto.Resolution{Dismiss: true}
	})
	waitFor(t, "the card", func() bool { return len(hub.askedCards()) == 1 })

	f.mu.Lock()
	f.questions["thr-3"] = []string{"req-2"} // req-1 answered in T3 Code; the run asks again
	f.asked["req-2"] = t3codeFakeQuestions()
	f.mu.Unlock()
	id := t3codeAskID("thr-3", "req-1")
	waitFor(t, "the answered card taken back", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return slices.Contains(hub.cancels, id)
	})
	waitFor(t, "the next question's card", func() bool { return len(hub.askedCards()) == 2 })
	f.mu.Lock()
	seen := f.listCalls
	f.mu.Unlock()
	waitFor(t, "a few more refreshes", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.listCalls >= seen+5
	})
	if cards := hub.askedCards(); len(cards) != 2 || cards[1].ID != t3codeAskID("thr-3", "req-2") {
		t.Fatalf("a card handed back was asked again: %+v", cards)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.responds) != 0 {
		t.Fatalf("answered in T3 Code for a card nobody answered: %q", f.responds)
	}
	if s := listed(t, n)["t3code-thr-3"]; s.Status != proto.StatusBlocked {
		t.Fatalf("thr-3 = %+v, want Needs you while req-2 waits", s)
	}
}
