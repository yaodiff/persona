package node

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

// hookSock serves n's hook socket and returns a call that sends one request and decodes the node's one-line answer.
func hookSock(t *testing.T, n *Node) func(req map[string]any) map[string]any {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mcpg") // unix socket paths are short on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go n.serveSock(ctx, c)
		}
	}()
	return func(req map[string]any) map[string]any {
		t.Helper()
		c, err := net.Dial("unix", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		b, _ := json.Marshal(req)
		_, _ = c.Write(append(b, '\n'))
		line, err := bufio.NewReader(c).ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(line, &out); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return out
	}
}

func toolNames(res map[string]any) []string {
	list, _ := res["tools"].([]any)
	var out []string
	for _, x := range list {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m["name"].(string))
		}
	}
	return out
}

func callText(res map[string]any) string {
	var out []string
	content, _ := res["content"].([]any)
	for _, x := range content {
		if m, ok := x.(map[string]any); ok {
			s, _ := m["text"].(string)
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n")
}

// An Antigravity assistant chat's MCP server is `personad mcp` on the hook socket, not the runtime socket: its tool
// list has the command gates' tools, and a call of one goes through the gate and runs the skill's script
// here, as on the runtime socket (TestGatedToolGoesThroughTheGate). Claude Code's assistant (Bash and its hook), a
// guest, a narrower tool set and an agent get neither.
func TestMCPGateToolsOnTheHookSocketForARuntimesAssistant(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here")
	}
	// This test is the runtime's process and its MCP server's (TestMCPAssistantFromARuntime).
	lineageWas := nodeLineage
	t.Cleanup(func() { nodeLineage = lineageWas })
	nodeLineage = func() []int { return nil }
	n, hub := runtimeNode(t)
	calls := assistantHub(hub)
	skills := filepath.Join(n.home, ".persona", "skills")
	t.Setenv("PERSONA_SKILLS_DIR", skills)
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	n.dir = filepath.Join(n.home, ".persona")
	script := filepath.Join(skills, "example", "scripts", "ex.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "example", "SKILL.md"), []byte("---\nname: example\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(n.home, "ran")
	if err := os.WriteFile(script, []byte("#!/bin/bash\necho \"$*\" >> "+shellQuote(ran)+"\necho done \"$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	prepareAssistantForTest(t, n, ".persona/assistant", nil, nil)
	const gated = "chat_api"
	g := exampleGate("")
	g.Tool = &proto.GateTool{Name: gated, Description: "Run ex."}
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = proto.AssistantLark{Gates: []proto.CommandGate{g}}, time.Now().Add(time.Hour)
	n.mu.Unlock()
	host := dialRuntime(t, n, assistantRuntimeHello())
	sid := startedAssistant(t, n, host)
	ask := hookSock(t, n)
	hubCalls := func() int {
		calls.mu.Lock()
		defer calls.mu.Unlock()
		return len(calls.calls)
	}
	ranLines := func() string {
		b, _ := os.ReadFile(ran)
		return strings.TrimSpace(string(b))
	}
	tools := func(extra map[string]any) map[string]any {
		req := map[string]any{"kind": "mcp.tools", "assistant": true, "session_id": sid}
		for k, v := range extra {
			req[k] = v
		}
		return ask(req)
	}
	call := func(extra map[string]any, args ...string) map[string]any {
		req := map[string]any{"kind": "mcp.call", "name": gated, "args": map[string]any{"args": args}, "assistant": true, "session_id": sid}
		for k, v := range extra {
			req[k] = v
		}
		return ask(req)
	}

	// The runtime's assistant chat: the hub's tools, the gate's after them, and the session as before.
	res := tools(nil)
	if got := strings.Join(toolNames(res), ","); got != "phone_contacts,ask_user,"+gated {
		t.Fatalf("tools %q", got)
	}
	if res["session"] != sid {
		t.Fatalf("session %v, want %q", res["session"], sid)
	}
	var shown proto.Tool
	b, _ := json.Marshal(res["tools"].([]any)[2])
	if err := json.Unmarshal(b, &shown); err != nil || !strings.Contains(string(shown.InputSchema), `"required":["args"]`) || shown.Description != "Run ex." {
		t.Fatalf("the gate's tool %s (%v)", b, err)
	}
	// A chat the runtime does not list yet lists the tools as it starts (runtimeMayName), but runs nothing.
	if got := toolNames(ask(map[string]any{"kind": "mcp.tools", "assistant": true, "session_id": "session-new"})); !strings.Contains(strings.Join(got, ","), gated) {
		t.Fatalf("a starting chat's tools %v", got)
	}

	// A read goes through the gate (an allow rule) and runs the script; the hub is not asked to run it.
	before := hubCalls()
	if r := call(nil, "GET", "/me"); r["is_error"] == true || !strings.Contains(callText(r), "done GET /me") {
		t.Fatalf("a read: %v", r)
	}
	if hubCalls() != before {
		t.Fatal("the gated tool went to the hub")
	}
	// A write the configured rules leave to the phone is refused there: the script does not run.
	hub.mu.Lock()
	hub.resolve = func(ix proto.Interaction) proto.Resolution {
		no := false
		return proto.Resolution{Allow: &no, Message: "not now"}
	}
	hub.mu.Unlock()
	if r := call(nil, "POST", "/conversations/oc_test/messages", `{"text":"hi"}`); r["is_error"] != true || !strings.Contains(callText(r), "not now") {
		t.Fatalf("a refused write: %v", r)
	}
	hub.mu.Lock()
	asked := len(hub.asked)
	var card proto.Interaction
	if asked > 0 {
		card = hub.asked[0]
	}
	hub.mu.Unlock()
	if asked != 1 || card.SessionID != sid || card.ToolName == "" || !card.OutwardWrite {
		t.Fatalf("%d cards, first %+v", asked, card)
	}
	// Missing args: the tool says how to call it, nothing runs.
	if r := ask(map[string]any{"kind": "mcp.call", "name": gated, "args": map[string]any{}, "assistant": true, "session_id": sid}); r["is_error"] != true {
		t.Fatalf("no args: %v", r)
	}

	// None of these get the gate's tool or run it: a guest, a narrower tool set, an agent, a chat no runtime lists.
	for _, c := range []struct {
		name  string
		extra map[string]any
	}{
		{"guest", map[string]any{"guest": true}},
		{"tool set", map[string]any{"tool_set": "supervise"}},
		{"agent", map[string]any{"assistant": false}},
		{"unlisted", map[string]any{"session_id": "session-new"}},
	} {
		if c.name != "unlisted" {
			if got := toolNames(tools(c.extra)); strings.Contains(strings.Join(got, ","), gated) {
				t.Errorf("%s: tools %v", c.name, got)
			}
		}
		before := hubCalls()
		call(c.extra, "GET", "/"+strings.ReplaceAll(c.name, " ", "-"))
		if hubCalls() != before+1 {
			t.Errorf("%s: the call did not go to the hub as any other tool's", c.name)
		}
	}
	if got := ranLines(); got != "GET /me" {
		t.Fatalf("the script ran %q", got)
	}

	// Once the runtime reports the worker that runs the conversation (runtime.assistant_pids), only that worker's
	// descendants run its gated tool: this test's process, the runtime's own, is not one of them.
	worker := exec.CommandContext(t.Context(), "sleep", "60")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Process.Kill(); _ = worker.Wait() })
	reportWorker := func(pids map[string]int) {
		t.Helper()
		if err := host.peer.Notify("runtime.assistant_pids", map[string]any{"pids": pids}); err != nil {
			t.Fatal(err)
		}
		want := len(pids)
		waitFor(t, "the worker report", func() bool { return len(n.runtimes.assistantWorkers()) == want })
	}
	reportWorker(map[string]int{sid: worker.Process.Pid})
	before = hubCalls()
	call(nil, "GET", "/not-the-worker")
	if hubCalls() != before+1 || ranLines() != "GET /me" {
		t.Fatalf("a process outside the conversation's worker ran its gated tool: %q", ranLines())
	}
	reportWorker(map[string]int{})
	if r := call(nil, "GET", "/again"); r["is_error"] == true || !strings.Contains(ranLines(), "GET /again") {
		t.Fatalf("without a worker report: %v", r)
	}

	// Claude Code's assistant, a process this node runs: it runs the gated command with Bash, not as a tool.
	n.mu.Lock()
	n.procs["s1"] = &assistantProc{id: "s1", cmd: &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}}
	n.mu.Unlock()
	res = tools(map[string]any{"session_id": "s1"})
	if res["session"] != "s1" || strings.Contains(strings.Join(toolNames(res), ","), gated) {
		t.Fatalf("Claude Code's assistant: %v", res)
	}
	before = hubCalls()
	call(map[string]any{"session_id": "s1"}, "GET", "/claude")
	if hubCalls() != before+1 || strings.Contains(ranLines(), "/claude") {
		t.Fatalf("Claude Code's assistant ran the gated tool here: %q", ranLines())
	}
}

type mcpGateProxyRig struct {
	t        *testing.T
	n        *Node
	hub      *runtimeHub
	host     *fakeRuntime
	api      *fakeAPI
	sid      string
	cwd      string
	ranPath  string
	toolName string
	ask      func(req map[string]any) map[string]any
	sentMu   sync.Mutex
	sent     []proto.LarkSent
}

func newMCPGateProxyRig(t *testing.T, pathPrefix string, rules ...proto.LarkRule) *mcpGateProxyRig {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here")
	}
	oldCtx := larkContextFromCLI
	larkContextFromCLI = func(context.Context, *Node, proto.AssistantLark, larkCall) larkCardContext {
		return larkCardContext{}
	}
	t.Cleanup(func() { larkContextFromCLI = oldCtx })

	lineageWas := nodeLineage
	t.Cleanup(func() { nodeLineage = lineageWas })
	nodeLineage = func() []int { return nil }

	api := newFakeAPI(t)
	n, hub := runtimeNode(t)
	t.Cleanup(func() { n.gates().close() })
	assistantHub(hub)

	rig := &mcpGateProxyRig{
		t:        t,
		n:        n,
		hub:      hub,
		api:      api,
		toolName: "chat_api",
	}

	hub.mu.Lock()
	baseAnswer := hub.answer
	hub.answer = func(method string, params json.RawMessage) (any, error, bool) {
		if method == proto.MethodLarkSent {
			var s proto.LarkSent
			if err := json.Unmarshal(params, &s); err != nil {
				return nil, err, true
			}
			rig.sentMu.Lock()
			rig.sent = append(rig.sent, s)
			rig.sentMu.Unlock()
			return struct{}{}, nil, true
		}
		return baseAnswer(method, params)
	}
	hub.mu.Unlock()

	skills := filepath.Join(n.home, ".persona", "skills")
	t.Setenv("PERSONA_SKILLS_DIR", skills)
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	n.dir = filepath.Join(n.home, ".persona")
	script := filepath.Join(skills, "example", "scripts", "ex.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "example", "SKILL.md"), []byte("---\nname: example\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ranPath := filepath.Join(n.home, "mcp-gate-ran.log")
	rig.ranPath = ranPath
	if pathPrefix == "" {
		pathPrefix = "/api/v1"
	}
	scriptBody := "#!/bin/bash\nset -euo pipefail\n" +
		"printf 'RAN\\n' >> " + shellQuote(ranPath) + "\n" +
		"method=\"$1\"; path=\"$2\"; shift 2\n" +
		"extra=()\n" +
		"has_data=0\n" +
		"if [ \"$#\" -gt 0 ]; then\n" +
		"  case \"$1\" in\n" +
		"    '{'*|'['*)\n" +
		"      extra+=(-H 'content-type: application/json' --data-raw \"$1\")\n" +
		"      has_data=1\n" +
		"      shift\n" +
		"      ;;\n" +
		"    '@'*)\n" +
		"      extra+=(-H 'content-type: application/json' --data-binary \"$1\")\n" +
		"      has_data=1\n" +
		"      shift\n" +
		"      ;;\n" +
		"  esac\n" +
		"fi\n" +
		"if [ \"$has_data\" -eq 0 ] && [ \"$#\" -gt 0 ]; then\n" +
		"  for arg in \"$@\"; do\n" +
		"    case \"$arg\" in\n" +
		"      -d|--data|--data-raw) extra+=(-H 'content-type: application/json'); break ;;\n" +
		"    esac\n" +
		"  done\n" +
		"fi\n" +
		"if [ -n \"${EXAMPLE_ACCOUNT:-}\" ]; then\n" +
		"  has_acct=0\n" +
		"  for arg in \"$@\"; do\n" +
		"    case \"$arg\" in\n" +
		"      x-example-account:*|X-Example-Account:*) has_acct=1 ;;\n" +
		"    esac\n" +
		"  done\n" +
		"  if [ \"$has_acct\" -eq 0 ]; then\n" +
		"    extra+=(-H \"X-Example-Account: ${EXAMPLE_ACCOUNT}\")\n" +
		"  fi\n" +
		"fi\n" +
		"exec curl -sS --fail-with-body -X \"$method\" ${extra[@]+\"${extra[@]}\"} \"$@\" \"${EXAMPLE_BASE}" + pathPrefix + "${path}\"\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}
	prepareAssistantForTest(t, n, ".persona/assistant", nil, nil)

	g := proxiedGate(api.URL, rules...)
	g.PathPrefix = pathPrefix
	g.Tool = &proto.GateTool{Name: rig.toolName, Description: "Run ex."}
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = proto.AssistantLark{Gates: []proto.CommandGate{g}}, time.Now().Add(time.Hour)
	n.mu.Unlock()

	host := dialRuntime(t, n, assistantRuntimeHello())
	sid := startedAssistant(t, n, host)
	st, _ := n.state.get(sid)
	rig.host = host
	rig.sid = sid
	rig.cwd = st.Cwd
	rig.ask = hookSock(t, n)
	return rig
}

func (r *mcpGateProxyRig) callMCP(args ...string) map[string]any {
	r.t.Helper()
	return r.ask(map[string]any{
		"kind":       "mcp.call",
		"name":       r.toolName,
		"args":       map[string]any{"args": args},
		"assistant":  true,
		"session_id": r.sid,
	})
}

func (r *mcpGateProxyRig) callRuntimeMCP(args ...string) proto.ToolResult {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res proto.ToolResult
	if err := r.host.peer.Call(ctx, "mcp.call", map[string]any{
		"name":       r.toolName,
		"args":       map[string]any{"args": args},
		"session_id": r.sid,
		"assistant":  true,
	}, &res); err != nil {
		r.t.Fatal(err)
	}
	return res
}

func (r *mcpGateProxyRig) setResolve(fn func(proto.Interaction) proto.Resolution) {
	r.hub.mu.Lock()
	r.hub.asked = nil
	r.hub.resolve = fn
	r.hub.mu.Unlock()
}

func (r *mcpGateProxyRig) askedCards() []proto.Interaction {
	r.hub.mu.Lock()
	defer r.hub.mu.Unlock()
	return append([]proto.Interaction(nil), r.hub.asked...)
}

func (r *mcpGateProxyRig) ranCount() int {
	b, err := os.ReadFile(r.ranPath)
	if err != nil {
		return 0
	}
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func (r *mcpGateProxyRig) sentCount() int {
	r.sentMu.Lock()
	defer r.sentMu.Unlock()
	return len(r.sent)
}

func (r *mcpGateProxyRig) lastSent() proto.LarkSent {
	r.sentMu.Lock()
	defer r.sentMu.Unlock()
	return r.sent[len(r.sent)-1]
}

func (r *mcpGateProxyRig) grantsCount() int {
	gp := r.n.gates()
	gp.mu.Lock()
	defer gp.mu.Unlock()
	return len(gp.grants)
}

func TestMCPGateToolEditableApprovalRewritesViaGateProxy(t *testing.T) {
	t.Run("send with apostrophe, newline, quotes, backslash, Chinese, mentions, and large int edits cleanly", func(t *testing.T) {
		r := newMCPGateProxyRig(t, "/api/v1")
		const original = "it's \"draft\"\n第二行 C:\\tmp $5 😀"
		const edited = "Edited 'quote' \"double\" $HOME $(printf nope)\n改写后的第二行 😀"
		const body = `{"text":"it's \"draft\"\n第二行 C:\\tmp $5 😀","mentions":[{"key":"@_user_1","id":"ou_example"}],"large":9007199254740993}`
		const query = "receive_id_type=open_id&tag=a%20b"

		r.setResolve(func(ix proto.Interaction) proto.Resolution {
			if ix.EditableText == "" {
				return proto.Resolution{Allow: boolp(true)}
			}
			return proto.Resolution{Allow: boolp(true), EditedText: edited}
		})

		res := r.callMCP("POST", "/conversations/c1/messages?"+query, body)
		if res["is_error"] == true || !strings.Contains(callText(res), `{"ok":true}`) {
			t.Fatalf("edited MCP send = %v, want success", res)
		}
		cards := r.askedCards()
		if len(cards) != 1 {
			t.Fatalf("expected 1 card, got %d", len(cards))
		}
		card := cards[0]
		if card.EditableText != original {
			t.Fatalf("card.EditableText = %q, want %q", card.EditableText, original)
		}
		if card.ID == "" || !card.OutwardWrite || !card.Detachable || card.ToolName != "示例" {
			t.Fatalf("unexpected card metadata: %+v", card)
		}
		if r.grantsCount() != 0 {
			t.Fatalf("expected one-use rewrite grant to be consumed, got %d leftover", r.grantsCount())
		}

		reqs := r.api.requests()
		if len(reqs) != 1 || reqs[0].Method != http.MethodPost || reqs[0].Path != "/api/v1/conversations/c1/messages" ||
			reqs[0].Query != query || reqs[0].Auth != "Bearer test-token" {
			t.Fatalf("upstream requests = %+v", reqs)
		}
		oldFields, _, ok := editableTextFields(body)
		if !ok {
			t.Fatal("original test body must be editable")
		}
		gotFields, gotText, ok := editableTextFields(reqs[0].Body)
		if !ok || gotText != edited {
			t.Fatalf("upstream body text = %q (ok=%v), want %q; raw=%s", gotText, ok, edited, reqs[0].Body)
		}
		for _, k := range []string{"mentions", "large"} {
			if string(gotFields[k]) != string(oldFields[k]) {
				t.Fatalf("upstream field %s changed: %s -> %s", k, oldFields[k], gotFields[k])
			}
		}

		waitFor(t, "edited LarkSent ledger report", func() bool { return r.sentCount() == 1 })
		sent := r.lastSent()
		if sent.Text != edited || sent.LetThrough.EditedFrom != original ||
			sent.LetThrough.InteractionID != card.ID || sent.LetThrough.Kind != proto.LarkThroughCard {
			t.Fatalf("LarkSent = %+v, want edited text + EditedFrom=%q + InteractionID=%q", sent, original, card.ID)
		}
	})

	t.Run("reply route, -d flag, unchanged allow, detached retry, and runtime socket mcp.call", func(t *testing.T) {
		r := newMCPGateProxyRig(t, "/api/v1")

		// 1. Reply route via hook socket with edited text.
		r.setResolve(func(ix proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: boolp(true), EditedText: "edited reply"}
		})
		res := r.callMCP("POST", "/messages/om_1/replies", `{"text":"reply draft","replyMode":"thread"}`)
		if res["is_error"] == true {
			t.Fatalf("edited reply = %v", res)
		}
		if cards := r.askedCards(); len(cards) != 1 || cards[0].EditableText != "reply draft" {
			t.Fatalf("reply card = %+v", cards)
		}
		reqs := r.api.requests()
		fields, gotText, ok := editableTextFields(reqs[len(reqs)-1].Body)
		if !ok || gotText != "edited reply" || string(fields["replyMode"]) != `"thread"` {
			t.Fatalf("reply upstream body = %s", reqs[len(reqs)-1].Body)
		}

		// 2. -d flag with multiline JSON.
		r.setResolve(func(ix proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: boolp(true), EditedText: "edited via -d"}
		})
		res = r.callMCP("POST", "/conversations/c1/messages", "-d", `{"text":"via -d\nsecond line"}`)
		if res["is_error"] == true {
			t.Fatalf("-d edited send = %v", res)
		}
		if cards := r.askedCards(); len(cards) != 1 || cards[0].EditableText != "via -d\nsecond line" {
			t.Fatalf("-d card = %+v", cards)
		}
		reqs = r.api.requests()
		if _, gotText, ok := editableTextFields(reqs[len(reqs)-1].Body); !ok || gotText != "edited via -d" {
			t.Fatalf("-d upstream body = %s", reqs[len(reqs)-1].Body)
		}

		// 3. Unchanged Allow keeps original body and records empty EditedFrom.
		waitFor(t, "prior sent reports", func() bool { return r.sentCount() == 2 })
		r.setResolve(func(ix proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: boolp(true)}
		})
		res = r.callMCP("POST", "/conversations/c1/messages", `{"text":"keep original"}`)
		if res["is_error"] == true {
			t.Fatalf("unchanged allow = %v", res)
		}
		if cards := r.askedCards(); len(cards) != 1 || cards[0].EditableText != "keep original" {
			t.Fatalf("unchanged allow card = %+v", cards)
		}
		waitFor(t, "unchanged sent report", func() bool { return r.sentCount() == 3 })
		if sent := r.lastSent(); sent.Text != "keep original" || sent.LetThrough.EditedFrom != "" {
			t.Fatalf("unchanged LarkSent = %+v, want empty EditedFrom", sent)
		}

		// 4. Detached continuation retry preserves original ApprovalID and EditedFrom.
		const origApprovalID = "ix_detached_mcp_orig"
		r.setResolve(func(ix proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: boolp(true), ApprovalID: origApprovalID, EditedText: "detached edited"}
		})
		res = r.callMCP("POST", "/conversations/c1/messages", `{"text":"detached draft"}`)
		if res["is_error"] == true {
			t.Fatalf("detached retry edit = %v", res)
		}
		waitFor(t, "detached sent report", func() bool { return r.sentCount() == 4 })
		if sent := r.lastSent(); sent.Text != "detached edited" ||
			sent.LetThrough.EditedFrom != "detached draft" || sent.LetThrough.InteractionID != origApprovalID {
			t.Fatalf("detached LarkSent = %+v, want ApprovalID=%q EditedFrom=detached draft", sent, origApprovalID)
		}

		// 5. Runtime socket mcp.call also supports editable approval and proxy rewrite.
		r.setResolve(func(ix proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: boolp(true), EditedText: "runtime socket edited"}
		})
		rtRes := r.callRuntimeMCP("POST", "/conversations/c1/messages", `{"text":"runtime socket draft"}`)
		if rtRes.IsError {
			t.Fatalf("runtime socket edited send = %+v", rtRes)
		}
		if cards := r.askedCards(); len(cards) != 1 || cards[0].EditableText != "runtime socket draft" {
			t.Fatalf("runtime socket card = %+v", cards)
		}
		reqs = r.api.requests()
		if _, gotText, ok := editableTextFields(reqs[len(reqs)-1].Body); !ok || gotText != "runtime socket edited" {
			t.Fatalf("runtime socket upstream body = %s", reqs[len(reqs)-1].Body)
		}
	})

	t.Run("multi-account query and header bind rewrite grant to routed account", func(t *testing.T) {
		r := newMCPGateProxyRig(t, "/agent/v1")
		r.setResolve(func(ix proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: boolp(true), EditedText: "account 2 edited"}
		})
		res := r.callMCP("POST", "/conversations/c1/messages?ol_account=2", `{"text":"account 2 draft"}`)
		if res["is_error"] == true {
			t.Fatalf("ol_account query edit = %v", res)
		}
		if cards := r.askedCards(); len(cards) != 1 || cards[0].EditableText != "account 2 draft" {
			t.Fatalf("ol_account card = %+v", cards)
		}
		reqs := r.api.requests()
		if len(reqs) != 1 || reqs[0].Query != "ol_account=2" {
			t.Fatalf("ol_account upstream = %+v", reqs)
		}
		if _, gotText, ok := editableTextFields(reqs[0].Body); !ok || gotText != "account 2 edited" {
			t.Fatalf("ol_account upstream body = %s", reqs[0].Body)
		}

		r.setResolve(func(ix proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: boolp(true), EditedText: "header account 3 edited"}
		})
		res = r.callMCP("POST", "/conversations/c1/messages", `{"text":"header account 3 draft"}`, "-H", "x-example-account: 3")
		if res["is_error"] == true {
			t.Fatalf("header account edit = %v", res)
		}
		reqs = r.api.requests()
		if len(reqs) != 2 || reqs[1].Account != "3" {
			t.Fatalf("header account upstream = %+v", reqs)
		}
		if _, gotText, ok := editableTextFields(reqs[1].Body); !ok || gotText != "header account 3 edited" {
			t.Fatalf("header account upstream body = %s", reqs[1].Body)
		}

		// Configured default account via EXAMPLE_ACCOUNT env var.
		r.n.mu.Lock()
		r.n.larkCfg.Gates[0].Env["EXAMPLE_ACCOUNT"] = "4"
		r.n.mu.Unlock()
		r.setResolve(func(ix proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: boolp(true), EditedText: "env account 4 edited"}
		})
		res = r.callMCP("POST", "/conversations/c1/messages", `{"text":"env account 4 draft"}`)
		if res["is_error"] == true {
			t.Fatalf("configured default account edit = %v", res)
		}
		reqs = r.api.requests()
		if len(reqs) != 3 || reqs[2].Account != "4" {
			t.Fatalf("configured default account upstream = %+v", reqs)
		}
		if _, gotText, ok := editableTextFields(reqs[2].Body); !ok || gotText != "env account 4 edited" {
			t.Fatalf("configured default account upstream body = %s", reqs[2].Body)
		}
	})

	t.Run("non-Auth gate does not set EditableText", func(t *testing.T) {
		r := newMCPGateProxyRig(t, "/api/v1")
		r.n.mu.Lock()
		r.n.larkCfg.Gates[0].Auth = nil
		r.n.mu.Unlock()
		r.setResolve(func(proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: boolp(false), Message: "checked"}
		})
		res := r.callMCP("POST", "/conversations/c1/messages", `{"text":"direct gate"}`)
		if res["is_error"] != true {
			t.Fatalf("non-Auth gate = %v", res)
		}
		if cards := r.askedCards(); len(cards) != 1 || cards[0].EditableText != "" {
			t.Fatalf("non-Auth gate card = %+v, want empty EditableText", cards)
		}
	})

	t.Run("read-only, non-message, file-backed, and unknown-flag calls do not set EditableText", func(t *testing.T) {
		r := newMCPGateProxyRig(t, "/api/v1")
		if err := os.WriteFile(filepath.Join(r.cwd, "msg.json"), []byte(`{"text":"from file"}`), 0o600); err != nil {
			t.Fatal(err)
		}

		// Read-only call runs without raising any card.
		r.setResolve(func(proto.Interaction) proto.Resolution {
			t.Error("read-only call unexpectedly raised a card")
			return proto.Resolution{Allow: boolp(true)}
		})
		if res := r.callMCP("GET", "/me"); res["is_error"] == true {
			t.Fatalf("GET /me = %v", res)
		}
		if len(r.askedCards()) != 0 {
			t.Fatalf("GET /me raised cards: %+v", r.askedCards())
		}

		// Non-message, @file, -d @file, and unknown flags must keep EditableText empty (and unknown-flag writes
		// on a proxy-authenticated gate are refused directly without raising an editable card).
		for _, tc := range []struct {
			name      string
			args      []string
			wantCards int
		}{
			{name: "reaction", args: []string{"POST", "/messages/om_1/reactions", `{"text":"ok"}`}, wantCards: 1},
			{name: "positional @file", args: []string{"POST", "/conversations/c1/messages", "@msg.json"}, wantCards: 1},
			{name: "-d @file", args: []string{"POST", "/conversations/c1/messages", "-d", "@msg.json"}, wantCards: 1},
			{name: "unknown flags -a/--body", args: []string{"POST", "/conversations/c1/messages", "-a", "2", "--body", `{"text":"hi"}`}, wantCards: 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ranBefore := r.ranCount()
				r.setResolve(func(ix proto.Interaction) proto.Resolution {
					return proto.Resolution{Allow: boolp(false), Message: "checked"}
				})
				res := r.callMCP(tc.args...)
				if res["is_error"] != true {
					t.Fatalf("%s expected deny error result, got %v", tc.name, res)
				}
				if r.ranCount() != ranBefore {
					t.Fatalf("%s unexpectedly executed script", tc.name)
				}
				cards := r.askedCards()
				if len(cards) != tc.wantCards {
					t.Fatalf("%s raised %d cards, want %d", tc.name, len(cards), tc.wantCards)
				}
				for _, card := range cards {
					if card.EditableText != "" {
						t.Fatalf("%s unexpectedly set EditableText=%q", tc.name, card.EditableText)
					}
				}
			})
		}
	})
}

func TestMCPGateToolFailClosedEditDenyNotesAndDetachedStates(t *testing.T) {
	denyRule := proto.LarkRule{
		Method: http.MethodPost,
		Path:   "/conversations/*/messages",
		Body:   map[string]any{"text": "blocked"},
		Action: "deny",
	}
	forbidDelete := proto.LarkRule{
		Method: http.MethodDelete,
		Path:   "/forbidden",
		Action: "deny",
	}
	r := newMCPGateProxyRig(t, "/api/v1", denyRule, forbidDelete)

	t.Run("applyLarkProxyEdit failure fails closed without running script or leaking grants", func(t *testing.T) {
		for _, tc := range []struct {
			name, badEdit string
		}{
			{name: "edited text hits deny rule", badEdit: "blocked"},
			{name: "edited text is blank", badEdit: "   "},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ranBefore := r.ranCount()
				reqBefore := len(r.api.requests())
				r.setResolve(func(ix proto.Interaction) proto.Resolution {
					return proto.Resolution{Allow: boolp(true), EditedText: tc.badEdit}
				})
				res := r.callMCP("POST", "/conversations/c1/messages", `{"text":"original draft"}`)
				if res["is_error"] != true || callText(res) == "" {
					t.Fatalf("%s = %v, want error result", tc.name, res)
				}
				if r.ranCount() != ranBefore {
					t.Fatalf("%s executed script despite applyLarkProxyEdit failure", tc.name)
				}
				if len(r.api.requests()) != reqBefore {
					t.Fatalf("%s reached upstream API", tc.name)
				}
				if r.grantsCount() != 0 {
					t.Fatalf("%s leaked %d grant(s)", tc.name, r.grantsCount())
				}
			})
		}

		t.Run("hot-reloaded deny rule during card wait fails closed", func(t *testing.T) {
			ranBefore := r.ranCount()
			reqBefore := len(r.api.requests())
			r.n.mu.Lock()
			savedRules := append([]proto.LarkRule(nil), r.n.larkCfg.Gates[0].Rules...)
			r.n.mu.Unlock()
			t.Cleanup(func() {
				r.n.mu.Lock()
				r.n.larkCfg.Gates[0].Rules = savedRules
				r.n.mu.Unlock()
			})
			r.setResolve(func(ix proto.Interaction) proto.Resolution {
				r.n.mu.Lock()
				r.n.larkCfg.Gates[0].Rules = append([]proto.LarkRule{
					{Method: http.MethodPost, Path: "/conversations/*/messages", Body: map[string]any{"text": "hot-blocked"}, Action: "deny"},
				}, r.n.larkCfg.Gates[0].Rules...)
				r.n.mu.Unlock()
				return proto.Resolution{Allow: boolp(true), EditedText: "hot-blocked"}
			})
			res := r.callMCP("POST", "/conversations/c1/messages", `{"text":"price $5"}`)
			if res["is_error"] != true || callText(res) == "" {
				t.Fatalf("hot-reloaded deny edit = %v, want error result", res)
			}
			if r.ranCount() != ranBefore || len(r.api.requests()) != reqBefore || r.grantsCount() != 0 {
				t.Fatalf("hot-reloaded deny edit did not fail closed: ran=%d reqs=%d grants=%d",
					r.ranCount()-ranBefore, len(r.api.requests())-reqBefore, r.grantsCount())
			}
		})
	})

	t.Run("user deny with note returns trimmed note without running script", func(t *testing.T) {
		ranBefore := r.ranCount()
		r.setResolve(func(proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: boolp(false), Message: "  Keep it under two sentences  "}
		})
		res := r.callMCP("POST", "/conversations/c1/messages", `{"text":"too long"}`)
		text := callText(res)
		if res["is_error"] != true || !strings.Contains(text, "Keep it under two sentences") {
			t.Fatalf("user deny with note = %v (text=%q)", res, text)
		}
		if r.ranCount() != ranBefore {
			t.Fatal("user deny with note executed script")
		}
	})

	t.Run("user deny without note or whitespace-only note returns clear error text", func(t *testing.T) {
		for _, rawMsg := range []string{"", "   "} {
			ranBefore := r.ranCount()
			r.setResolve(func(proto.Interaction) proto.Resolution {
				return proto.Resolution{Allow: boolp(false), Message: rawMsg}
			})
			res := r.callMCP("POST", "/conversations/c1/messages", `{"text":"plain deny"}`)
			text := strings.TrimSpace(callText(res))
			if res["is_error"] != true || text == "" || strings.HasSuffix(text, "：") || !strings.Contains(text, "用户没有同意") {
				t.Fatalf("plain deny (msg=%q) = %v, want clear non-empty refusal text", rawMsg, res)
			}
			if r.ranCount() != ranBefore {
				t.Fatalf("plain deny (msg=%q) executed script", rawMsg)
			}
		}
	})

	t.Run("configured rule deny and assistant-attributed deny return clear error text", func(t *testing.T) {
		ranBefore := r.ranCount()
		r.setResolve(func(proto.Interaction) proto.Resolution {
			t.Error("configured deny rule must not raise a card")
			return proto.Resolution{Allow: boolp(true)}
		})
		res := r.callMCP("DELETE", "/forbidden")
		if res["is_error"] != true || !strings.Contains(callText(res), "DELETE /forbidden") {
			t.Fatalf("rule deny = %v", res)
		}
		if len(r.askedCards()) != 0 || r.ranCount() != ranBefore {
			t.Fatalf("rule deny raised cards=%d or ran script", len(r.askedCards()))
		}

		r.setResolve(func(proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: boolp(false), AnsweredBy: proto.AnsweredByAssistant, Message: "supervisor refused"}
		})
		res = r.callMCP("POST", "/conversations/c1/messages", `{"text":"asst denied"}`)
		if res["is_error"] != true || !strings.Contains(callText(res), "supervisor refused") {
			t.Fatalf("assistant-attributed deny = %v", res)
		}
		if r.ranCount() != ranBefore {
			t.Fatal("assistant-attributed deny executed script")
		}
	})

	t.Run("detached pending returns non-error waiting text without running script", func(t *testing.T) {
		for _, tc := range []struct {
			name, msg, wantPrefix string
		}{
			{name: "hub WaitingText", msg: approvalops.WaitingText + " The command was not executed.", wantPrefix: approvalops.WaitingText},
			{name: "hub ClarifyingText", msg: approvalops.ClarifyingText, wantPrefix: approvalops.ClarifyingText},
			{name: "empty message fallback", msg: "", wantPrefix: approvalops.WaitingText},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ranBefore := r.ranCount()
				r.setResolve(func(ix proto.Interaction) proto.Resolution {
					return proto.Resolution{Dismiss: true, Pending: true, ApprovalID: ix.ID, Message: tc.msg}
				})
				res := r.callMCP("POST", "/conversations/c1/messages", `{"text":"pending msg"}`)
				text := callText(res)
				if res["is_error"] == true {
					t.Fatalf("%s returned is_error=true (%q), want non-error pending status", tc.name, text)
				}
				if !strings.HasPrefix(text, tc.wantPrefix) {
					t.Fatalf("%s text = %q, want prefix %q", tc.name, text, tc.wantPrefix)
				}
				if r.ranCount() != ranBefore {
					t.Fatalf("%s executed script while pending", tc.name)
				}
			})
		}
	})

	t.Run("remembered always allow does not trigger applyLarkProxyEdit", func(t *testing.T) {
		r2 := newMCPGateProxyRig(t, "/api/v1")
		r2.setResolve(func(ix proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: boolp(true), Remembered: true, EditedText: "must-be-ignored"}
		})
		res := r2.callMCP("POST", "/conversations/c1/messages", `{"text":"original remembered"}`)
		if res["is_error"] == true || !strings.Contains(callText(res), `{"ok":true}`) {
			t.Fatalf("remembered allow = %v, want success", res)
		}
		reqs := r2.api.requests()
		if len(reqs) != 1 {
			t.Fatalf("remembered allow upstream count = %d, want 1", len(reqs))
		}
		if _, gotText, ok := editableTextFields(reqs[0].Body); !ok || gotText != "original remembered" {
			t.Fatalf("remembered allow upstream body = %s, want original unedited text", reqs[0].Body)
		}
		waitFor(t, "remembered LarkSent report", func() bool { return r2.sentCount() == 1 })
		if sent := r2.lastSent(); sent.LetThrough.Kind != proto.LarkThroughAlways || sent.LetThrough.EditedFrom != "" {
			t.Fatalf("remembered LarkSent = %+v, want Kind=always and empty EditedFrom", sent)
		}
	})

	t.Run("hot-removed gate yielding empty decision fails closed without running script", func(t *testing.T) {
		r2 := newMCPGateProxyRig(t, "/api/v1")
		r2.n.mu.Lock()
		removedGate := r2.n.larkCfg.Gates[0]
		r2.n.larkCfg.Gates = nil
		r2.n.mu.Unlock()
		ranBefore := r2.ranCount()
		res := r2.n.assistantGateTool(context.Background(), &removedGate, r2.sid,
			json.RawMessage(`{"args":["POST","/conversations/c1/messages","{\"text\":\"removed\"}"]}`))
		if !res.IsError || len(res.Content) == 0 || strings.TrimSpace(res.Content[0].Text) == "" {
			t.Fatalf("hot-removed gate = %+v, want fail-closed error", res)
		}
		if r2.ranCount() != ranBefore || len(r2.api.requests()) != 0 {
			t.Fatalf("hot-removed gate ran script (%d) or reached upstream (%d)", r2.ranCount()-ranBefore, len(r2.api.requests()))
		}
	})

	t.Run("dismiss, cancel, and empty resolution return non-execution error without misleading denial or running script", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			res     func(proto.Interaction) proto.Resolution
			wantSub string
		}{
			{
				name: "hub dismiss message",
				res: func(ix proto.Interaction) proto.Resolution {
					return proto.Resolution{Dismiss: true, ApprovalID: ix.ID, Message: "This approval did not complete with an approved outcome."}
				},
				wantSub: "did not complete",
			},
			{
				name:    "empty dismiss fallback",
				res:     func(ix proto.Interaction) proto.Resolution { return proto.Resolution{Dismiss: true, ApprovalID: ix.ID} },
				wantSub: "请勿自动重试",
			},
			{
				name:    "cancel fallback",
				res:     func(ix proto.Interaction) proto.Resolution { return proto.Resolution{Cancel: true} },
				wantSub: "invalid question cancellation",
			},
			{
				name:    "nil allow fallback",
				res:     func(ix proto.Interaction) proto.Resolution { return proto.Resolution{} },
				wantSub: "请勿自动重试",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ranBefore := r.ranCount()
				r.setResolve(tc.res)
				res := r.callMCP("POST", "/conversations/c1/messages", `{"text":"dismissed msg"}`)
				text := callText(res)
				if res["is_error"] != true || !strings.Contains(text, tc.wantSub) {
					t.Fatalf("%s = %v (text=%q), want error containing %q", tc.name, res, text, tc.wantSub)
				}
				if r.ranCount() != ranBefore {
					t.Fatalf("%s executed script after dismiss", tc.name)
				}
			})
		}
	})
}
