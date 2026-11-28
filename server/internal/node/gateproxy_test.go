package node

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// proxiedGate is the example gate as a hub hands it over since #1317: its credentials go on the requests at the
// node's proxy (Authorization: Bearer ${EXAMPLE_TOKEN}), not into the assistant's environment.
func proxiedGate(base string, user ...proto.LarkRule) proto.CommandGate {
	g := exampleGate(base, user...)
	g.Auth = &proto.GateAuth{Header: "Authorization", Value: "Bearer ${EXAMPLE_TOKEN}"}
	return g
}

// apiSeen is a request the example API got.
type apiSeen struct {
	Method, Path, Query, Auth, Origin, Host, Account, Cookie, Other, Body string
}

// portProbeAgents are the User-Agents of the port probe of any node a machine may run: today's, and that of a node
// installed before the Persona-only rename (#6079), which keeps sending it until it is replaced.
var portProbeAgents = []string{portProbeAgent, compat.PortProbeAgent}

func isFakeAPICardContextPath(p string) bool {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) >= 2 && (parts[0] == "api" || parts[0] == "agent") && parts[1] == "v1" {
		parts = parts[2:]
	}
	return len(parts) == 2 && (parts[0] == "conversations" || parts[0] == "messages") && parts[1] != ""
}

type fakeAPI struct {
	*httptest.Server
	mu   sync.Mutex
	seen []apiSeen
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	a := &fakeAPI{}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" && slices.Contains(portProbeAgents, r.Header.Get("User-Agent")) {
			// A Persona node on the machine running these tests (the tests run under its agents) finds this server
			// among an agent's ports and probes it (ports.go): that GET / is not a request the gate passed on. Only
			// the probe's whole shape is left out, never a request the gate forwards.
			return
		}
		if r.Method == http.MethodGet && r.Header.Get("User-Agent") == larkCardContextAgent && isFakeAPICardContextPath(r.URL.Path) {
			// The node's read-only approval-card context lookup is not a gated command forwarded by the proxy.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.seen = append(a.seen, apiSeen{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"),
			Origin: r.Header.Get("Origin"), Host: r.Host, Account: r.Header.Get("X-Example-Account"), Cookie: r.Header.Get("Cookie"),
			Other: r.Header.Get("X-Other"), Body: string(b)})
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if i := strings.LastIndex(r.URL.Path, "/status/"); i >= 0 { // the API answers this one with the status it names
			code, _ := strconv.Atoi(r.URL.Path[i+len("/status/"):])
			w.WriteHeader(code)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/boom") { // the API fails this one
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"ok":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(a.Close)
	return a
}

// A node's port probe of the example API is not counted as a request the gate passed on: on a machine where Persona
// runs the agents, the node probes the tests' servers too, and a count of the API's requests saw one more.
func TestFakeAPIIgnoresPortProbes(t *testing.T) {
	api := newFakeAPI(t)
	if got := probePort(context.Background(), portOf(t, api.URL), "127.0.0.1"); !got.http {
		t.Fatalf("the probe did not reach the API: %+v", got)
	}
	// A node installed before the rename probes the same way under its old name.
	for _, agent := range portProbeAgents {
		if code, _ := send(t, "GET", api.URL+"/", "", "User-Agent", agent); code != http.StatusOK {
			t.Fatalf("the probe as %s: %d", agent, code)
		}
	}
	if seen := api.requests(); len(seen) != 0 {
		t.Fatalf("a port probe counted as the gate's request: %+v", seen)
	}
	// Any other GET / still counts.
	if code, _ := send(t, "GET", api.URL+"/", ""); code != http.StatusOK || len(api.requests()) != 1 {
		t.Fatalf("a GET / that is not the probe: %d, %d requests", code, len(api.requests()))
	}
}

func (a *fakeAPI) requests() []apiSeen {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]apiSeen(nil), a.seen...)
}

// gateNode is a node the hub gave the proxied example gate on api, with the example skill in cwd.
func gateNode(t *testing.T, api string, user ...proto.LarkRule) (*Node, string) {
	t.Helper()
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	n := &Node{dir: t.TempDir(), larkCfgAt: time.Now(), logger: log.New(io.Discard, "", 0),
		larkCfg: proto.AssistantLark{Gates: []proto.CommandGate{proxiedGate(api, user...)}}}
	t.Cleanup(func() { n.gates().close() })
	cwd := t.TempDir()
	skill := filepath.Join(cwd, ".claude", "skills", "example", "scripts")
	_ = os.MkdirAll(skill, 0o755)
	_ = os.WriteFile(filepath.Join(skill, "ex.sh"), []byte("#!/bin/bash\n"), 0o755)
	return n, cwd
}

// send is a request to url, as the gated command's script, or anything else the assistant runs, would make it;
// header is name, value, ….
func send(t *testing.T, method, url, body string, header ...string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil { // an answer cut short is not an empty or a shorter one
		t.Errorf("%s %s: the answer broke off after %d bytes: %v", method, url, len(b), err)
	}
	return res.StatusCode, string(b)
}

func envOf(env []string, name string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

// The assistant's process gets its own address at the node's proxy for a gate with Auth, never the credentials;
// the proxy adds them, and the address goes when the process does (#1317).
func TestGateCredentialsStayInTheNode(t *testing.T) {
	api := newFakeAPI(t)
	n, cwd := gateNode(t, api.URL)
	n.larkCfg.Gates[0].Env["EXAMPLE_ACCOUNT"] = "2"
	env, release := n.assistantLarkEnv(cwd, "")
	if all := strings.Join(env, "\n"); strings.Contains(all, "test-token") || strings.Contains(all, "EXAMPLE_TOKEN") {
		t.Fatalf("the credentials reached the assistant's environment:\n%s", all)
	}
	if v, _ := envOf(env, "EXAMPLE_ACCOUNT"); v != "2" {
		t.Fatalf("EXAMPLE_ACCOUNT %q: the gate's other env goes in as before", v)
	}
	base, _ := envOf(env, "EXAMPLE_BASE")
	if !regexp.MustCompile(`^http://127\.0\.0\.1:\d+/[0-9a-f]{32}$`).MatchString(base) {
		t.Fatalf("EXAMPLE_BASE %q: not the process's own address at the proxy", base)
	}
	if code, body := send(t, "GET", base+"/api/v1/me?x=1", ""); code != http.StatusOK || body != `{"ok":true}` {
		t.Fatalf("a read: %d %s", code, body)
	}
	got := api.requests()
	if len(got) != 1 || got[0].Path != "/api/v1/me" || got[0].Query != "x=1" || got[0].Auth != "Bearer test-token" ||
		got[0].Host != strings.TrimPrefix(api.URL, "http://") {
		t.Fatalf("the API got %+v", got)
	}
	env2, release2 := n.assistantLarkEnv(cwd, "")
	defer release2()
	if base2, _ := envOf(env2, "EXAMPLE_BASE"); base2 == base {
		t.Fatal("two processes share an address")
	}
	release()
	if code, _ := send(t, "GET", base+"/api/v1/me", ""); code != http.StatusNotFound {
		t.Fatalf("the address of a process that ended: %d", code)
	}
	if len(api.requests()) != 1 {
		t.Fatal("a request went through an address taken back")
	}
}

// The proxy holds every request to the gate's rules, whoever sends it: reads go, a write goes once after the gate let
// that very call through, a rule's deny stays denied, and the sender's own credentials and cookies stay behind.
func TestGateProxyHoldsRequestsToTheRules(t *testing.T) {
	api := newFakeAPI(t)
	n, _ := gateNode(t, api.URL, proto.LarkRule{Method: "DELETE", Path: "/messages/*", Action: "deny"})
	p := n.gates()
	base, release, err := p.open("ex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	root := base[:strings.LastIndex(base, "/")]
	msg := base + "/api/v1/conversations/F1/messages"

	if code, _ := send(t, "GET", base+"/api/v1/me", "", "Authorization", "Bearer mine", "Cookie", "s=1", "X-Example-Account", "3", "X-Other", "1"); code != http.StatusOK {
		t.Fatalf("a read: %d", code)
	}
	if r := api.requests()[0]; r.Auth != "Bearer test-token" || r.Cookie != "" || r.Other != "" || r.Account != "3" {
		t.Fatalf("the headers the API got: %+v", r)
	}
	if code, body := send(t, "POST", msg, `{"text":"hi"}`); code != http.StatusForbidden || !strings.Contains(body, "手机上确认") {
		t.Fatalf("a write nobody confirmed: %d %s", code, body)
	}
	if code, _ := send(t, "POST", base+"/api/v1/conversations", `{"type":"p2p"}`); code != http.StatusOK {
		t.Fatalf("a write the rules allow: %d", code)
	}
	p.grant("ex", "", larkCall{Method: "POST", Path: "/conversations/F1/messages", Body: `{"text":"hi"}`}, proto.LarkLetThrough{Kind: proto.LarkThroughCard})
	if code, _ := send(t, "POST", msg, `{"text":"bye"}`); code != http.StatusForbidden {
		t.Fatalf("another body than the one confirmed: %d", code)
	}
	if code, _ := send(t, "POST", msg, `{ "text" : "hi" }`, "Origin", root); code != http.StatusOK {
		t.Fatalf("the confirmed write: %d", code)
	}
	if code, _ := send(t, "POST", msg, `{"text":"hi"}`); code != http.StatusForbidden {
		t.Fatalf("the confirmed write, sent again: %d", code)
	}
	p.grant("ex", "", larkCall{Method: "DELETE", Path: "/messages/M1"}, proto.LarkLetThrough{Kind: proto.LarkThroughCard})
	if code, body := send(t, "DELETE", base+"/api/v1/messages/M1", ""); code != http.StatusForbidden || !strings.Contains(body, "不允许") {
		t.Fatalf("a call the rules deny: %d %s", code, body)
	}
	p.grant("ex", "", larkCall{Method: "POST", Path: "/conversations/F2/messages", Body: `{"text":"$MSG"}`}, proto.LarkLetThrough{Kind: proto.LarkThroughCard})
	if code, _ := send(t, "POST", base+"/api/v1/conversations/F2/messages", `{"text":"what the shell filled in"}`); code != http.StatusOK {
		t.Fatalf("a confirmed body the shell fills in: %d", code)
	}
	p.grant("ex", "", larkCall{Method: "POST", Path: "/conversations/F3/messages"}, proto.LarkLetThrough{Kind: proto.LarkThroughCard})
	p.mu.Lock()
	p.grants[len(p.grants)-1].until = time.Now().Add(-time.Second)
	p.mu.Unlock()
	if code, _ := send(t, "POST", base+"/api/v1/conversations/F3/messages", `{}`); code != http.StatusForbidden {
		t.Fatalf("a grant past its time: %d", code)
	}
	if code, _ := send(t, "GET", root+"/"+strings.Repeat("0", 32)+"/api/v1/me", ""); code != http.StatusNotFound {
		t.Fatalf("an address nobody was given: %d", code)
	}
	var writes []apiSeen
	for _, r := range api.requests() {
		if r.Method != "GET" {
			writes = append(writes, r)
		}
	}
	if len(writes) != 3 || writes[1].Path != "/api/v1/conversations/F1/messages" || writes[1].Body != `{ "text" : "hi" }` {
		t.Fatalf("the writes the API got: %+v", writes)
	}
	if writes[1].Origin != api.URL {
		t.Fatalf("Origin %q: the API's own, not the proxy's", writes[1].Origin)
	}
}

// A confirmed body of exactly JSON null binds the grant to null, not to any body: another JSON body or one that is not
// JSON is refused and leaves the grant, which the confirmed null then uses once (#8973).
func TestGateGrantOfAJSONNullBodyTakesOnlyNull(t *testing.T) {
	api := newFakeAPI(t)
	n, _ := gateNode(t, api.URL)
	p := n.gates()
	base, release, err := p.open("ex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	note := base + "/api/v1/notes/N1"
	p.grant("ex", "", larkCall{Method: "POST", Path: "/notes/N1", Body: "null"}, proto.LarkLetThrough{Kind: proto.LarkThroughCard})
	if code, _ := send(t, "POST", note, `{"text":"changed"}`); code != http.StatusForbidden {
		t.Fatalf("another JSON body than the null confirmed: %d", code)
	}
	if code, _ := send(t, "POST", note, "changed", "Content-Type", "text/plain"); code != http.StatusForbidden {
		t.Fatalf("a body that is not JSON for the null confirmed: %d", code)
	}
	if code, _ := send(t, "POST", note, " null "); code != http.StatusOK {
		t.Fatalf("the confirmed null: %d", code)
	}
	if code, _ := send(t, "POST", note, "null"); code != http.StatusForbidden {
		t.Fatalf("the confirmed null, sent again: %d", code)
	}
	var writes []apiSeen
	for _, r := range api.requests() {
		if r.Method != "GET" {
			writes = append(writes, r)
		}
	}
	if len(writes) != 1 || writes[0].Path != "/api/v1/notes/N1" || writes[0].Body != " null " {
		t.Fatalf("the writes the API got: %+v", writes)
	}
}

// What the phone confirms is what the proxy lets through, once; a refusal lets nothing through; "Always allow for this session" lets
// the same route through again without asking.
func TestGateLetsThroughWhatThePhoneConfirmed(t *testing.T) {
	api := newFakeAPI(t)
	var mu sync.Mutex
	answer := proto.Resolution{Allow: boolp(false), Message: "不发"}
	h := newScriptHub(t, func(context.Context, proto.Interaction) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		return answer, nil
	}, notifyOK)
	n := h.n
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = proto.AssistantLark{Gates: []proto.CommandGate{proxiedGate(api.URL)}}, time.Now()
	n.mu.Unlock()
	t.Cleanup(func() { n.gates().close() })
	base, release, err := n.gates().open("ex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	check := func(cmd string) (string, string) {
		t.Helper()
		ix := proto.Interaction{SessionID: "s1", ExpiresAt: time.Now().Add(time.Minute)}
		d, reason, err := n.larkCheck(context.Background(), "s1", t.TempDir(), cmd, ix)
		if err != nil {
			t.Fatal(err)
		}
		return d, reason
	}
	asked := func() int {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.created)
	}
	write := `ex POST /conversations/F1/messages '{"text":"hi"}'`
	if d, reason := check(write); d != "deny" || reason != "不发" {
		t.Fatalf("refused on the phone: %s %q", d, reason)
	}
	if code, _ := send(t, "POST", base+"/api/v1/conversations/F1/messages", `{"text":"hi"}`); code != http.StatusForbidden {
		t.Fatalf("a write the phone refused went through: %d", code)
	}
	mu.Lock()
	answer = proto.Resolution{Allow: boolp(true), Always: true}
	mu.Unlock()
	if d, _ := check(write); d != "allow" {
		t.Fatalf("confirmed on the phone: %s", d)
	}
	if code, _ := send(t, "POST", base+"/api/v1/conversations/F1/messages", `{"text":"hi"}`); code != http.StatusOK {
		t.Fatalf("the write the phone confirmed: %d", code)
	}
	cards := asked()
	if d, _ := check(`ex POST /conversations/F2/messages '{"text":"again"}'`); d != "allow" || asked() != cards {
		t.Fatalf("本会话总是允许: %s, %d cards (was %d)", d, asked(), cards)
	}
	if code, _ := send(t, "POST", base+"/api/v1/conversations/F2/messages", `{"text":"again"}`); code != http.StatusOK {
		t.Fatalf("a write Always allow covers: %d", code)
	}
	if got := api.requests(); len(got) != 2 {
		t.Fatalf("the API got %+v", got)
	}
}

// A write the gate cannot follow (a loop) through a gate whose credentials are at the proxy is refused at once, with
// how to send it instead; through a gate that still puts its credentials in the environment it is put to the phone
// whole, as before.
func TestGateWriteItCannotFollow(t *testing.T) {
	n := &Node{dir: t.TempDir(), larkCfgAt: time.Now(), logger: log.New(io.Discard, "", 0),
		larkCfg: proto.AssistantLark{Gates: []proto.CommandGate{proxiedGate("http://api.example.test")}}}
	loop := `for i in 1 2; do ex POST /conversations/F$i/messages '{"text":"hi"}'; done`
	ix := proto.Interaction{ExpiresAt: time.Now().Add(time.Minute)}
	if d, reason, err := n.larkCheck(context.Background(), "s1", t.TempDir(), loop, ix); err != nil || d != "deny" || !strings.Contains(reason, "单独一条") {
		t.Fatalf("a write in a loop: %s %q %v", d, reason, err)
	}
	n.larkCfg = proto.AssistantLark{Gates: []proto.CommandGate{exampleGate("http://api.example.test")}}
	// No hub to ask here: put to the phone, and not confirmed.
	if d, reason, _ := n.larkCheck(context.Background(), "s1", t.TempDir(), loop, ix); d != "deny" || strings.Contains(reason, "单独一条") {
		t.Fatalf("a write in a loop, credentials in the environment: %s %q", d, reason)
	}
}

// A resident node's next connection lets through what a conversation kept from the last one sends, with the
// credentials it has now: the proxy is the process's, not the connection's.
func TestKeptConversationSendsThroughTheNextConnection(t *testing.T) {
	api := newFakeAPI(t)
	keeper := newAssistantKeeper()
	discard := log.New(io.Discard, "", 0)
	first := &Node{dir: t.TempDir(), keeper: keeper, larkCfgAt: time.Now(), logger: discard,
		larkCfg: proto.AssistantLark{Gates: []proto.CommandGate{proxiedGate(api.URL)}}}
	t.Cleanup(func() { first.gates().close() })
	base, release, err := first.gates().open("ex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	next := &Node{dir: t.TempDir(), keeper: keeper, larkCfgAt: time.Now(), logger: discard,
		larkCfg: proto.AssistantLark{Gates: []proto.CommandGate{proxiedGate(api.URL)}}}
	next.larkCfg.Gates[0].Env["EXAMPLE_TOKEN"] = "rotated"
	next.grantGateCalls(next.larkCfg, "", []larkCall{{Gate: 0, Method: "POST", Path: "/conversations/F1/messages", Body: `{"text":"hi"}`}}, proto.LarkLetThrough{Kind: proto.LarkThroughCard})
	if code, _ := send(t, "POST", base+"/api/v1/conversations/F1/messages", `{"text":"hi"}`); code != http.StatusOK {
		t.Fatalf("a kept conversation's write the next connection let through: %d", code)
	}
	if got := api.requests(); len(got) != 1 || got[0].Auth != "Bearer rotated" {
		t.Fatalf("the API got %+v", got)
	}
}

// End to end, as a runtime's assistant runs the gated command: the skill's script (curl here) reaches the API only
// through the proxy and never has the credentials; the write the phone confirms goes, the one it refuses does not.
func TestGatedToolSendsThroughTheProxy(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}
	api := newFakeAPI(t)
	n, hub := runtimeNode(t)
	assistantHub(hub)
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
	if err := os.WriteFile(script, []byte(`#!/bin/bash
echo "token:${EXAMPLE_TOKEN:-none}"
if [ -n "${3:-}" ]; then
  curl -sS -X "$1" -H 'content-type: application/json' --data-raw "$3" "$EXAMPLE_BASE/api/v1$2"
else
  curl -sS -X "$1" "$EXAMPLE_BASE/api/v1$2"
fi
`), 0o755); err != nil {
		t.Fatal(err)
	}
	prepareAssistantForTest(t, n, ".persona/assistant", nil, nil)
	g := proxiedGate(api.URL)
	g.Tool = &proto.GateTool{Name: "example_api", Description: "Run ex."}
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = proto.AssistantLark{Gates: []proto.CommandGate{g}}, time.Now().Add(time.Hour)
	n.mu.Unlock()
	t.Cleanup(func() { n.gates().close() })
	host := dialRuntime(t, n, assistantRuntimeHello())
	sid := startedAssistant(t, n, host)
	run := func(args ...string) proto.ToolResult {
		t.Helper()
		var res proto.ToolResult
		if err := host.peer.Call(context.Background(), "mcp.call", map[string]any{"name": "example_api", "args": map[string]any{"args": args}, "session_id": sid, "assistant": true}, &res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := run("GET", "/me"); res.IsError || !strings.Contains(res.Content[0].Text, "token:none") || !strings.Contains(res.Content[0].Text, `{"ok":true}`) {
		t.Fatalf("a read: %+v", res)
	}
	resolve := func(allow bool, msg string) {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		hub.resolve = func(proto.Interaction) proto.Resolution { return proto.Resolution{Allow: boolp(allow), Message: msg} }
	}
	resolve(true, "")
	if res := run("POST", "/conversations/oc_test/messages", `{"text":"hi"}`); res.IsError || !strings.Contains(res.Content[0].Text, `{"ok":true}`) {
		t.Fatalf("a confirmed write: %+v", res)
	}
	resolve(false, "not now")
	if res := run("POST", "/conversations/oc_test/messages", `{"text":"again"}`); !res.IsError || !strings.Contains(res.Content[0].Text, "not now") {
		t.Fatalf("a refused write: %+v", res)
	}
	got := api.requests()
	if len(got) != 2 || got[0].Auth != "Bearer test-token" || got[1].Method != "POST" || got[1].Body != `{"text":"hi"}` {
		var b []byte
		b, _ = json.Marshal(got)
		t.Fatalf("the API got %s", b)
	}
}

// ledgerHub is the manager as the gate proxy meets it: it takes lark.sent and keeps what it was told.
type ledgerHub struct {
	mu   sync.Mutex
	got  []proto.LarkSent
	hold chan struct{} // when set, a report waits for it to close before it is answered
	fail bool          // the manager answers with an error
}

// lockedLog is a log the test reads while the node writes it.
type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// ledgerNode is gateNode with a hub behind it that takes the proxy's reports.
func ledgerNode(t *testing.T, api string, user ...proto.LarkRule) (*Node, *ledgerHub, *lockedLog) {
	t.Helper()
	n, _ := gateNode(t, api, user...)
	logs := &lockedLog{}
	n.logger = log.New(logs, "", 0)
	h := &ledgerHub{}
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	hub.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != proto.MethodLarkSent {
			return nil, nil
		}
		h.mu.Lock()
		hold, fail := h.hold, h.fail
		h.mu.Unlock()
		if hold != nil {
			<-hold
		}
		if fail {
			return nil, errors.New("manager down")
		}
		var s proto.LarkSent
		_ = json.Unmarshal(params, &s)
		h.mu.Lock()
		h.got = append(h.got, s)
		h.mu.Unlock()
		return struct{}{}, nil
	}
	peer := n.peer
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = n2hW.Close(); _ = h2nW.Close() })
	return n, h, logs
}

// reportList waits for at least want reports and then a moment more, so one too many shows; they come in the order the
// manager took them, which is not the order of the writes.
func (h *ledgerHub) reportList(t *testing.T, want int) []proto.LarkSent {
	t.Helper()
	waitFor(t, "reports to the manager", func() bool { // catchup_test.go: on every platform, unlike fullWait (#5924)
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.got) >= want
	})
	time.Sleep(100 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.got) != want {
		t.Fatalf("%d reports, want %d: %+v", len(h.got), want, h.got)
	}
	return append([]proto.LarkSent(nil), h.got...)
}

// reportsNow is the reports taken so far, without waiting for more.
func (h *ledgerHub) reportsNow() []proto.LarkSent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]proto.LarkSent(nil), h.got...)
}

// reports is reportList by path (the last report of a path stands for it).
func (h *ledgerHub) reports(t *testing.T, want int) map[string]proto.LarkSent {
	t.Helper()
	out := map[string]proto.LarkSent{}
	for _, s := range h.reportList(t, want) {
		out[s.Path] = s
	}
	return out
}

// A write the proxy forwarded and the API answered with a 2xx is told to the manager, with how the gate let it
// through (a rule, a card, "Always allow") and the conversation it belongs to. A read, a write the proxy refused and
// one the API failed are not.
func TestGateProxyTellsTheManagerOfWritesTheAPITook(t *testing.T) {
	api := newFakeAPI(t)
	n, hub, _ := ledgerNode(t, api.URL, proto.LarkRule{Method: "POST", Path: "/notes/**", Action: "allow"})
	p := n.gates()
	base, release, err := p.open("ex", "s-proc")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// A process the node knew no conversation of takes the grant of any, and is told as the grant's.
	anon, releaseAnon, err := p.open("ex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseAnon()
	card := proto.LarkLetThrough{Kind: proto.LarkThroughCard, InteractionID: "ix_1", AnsweredBy: proto.AnsweredByAssistant}

	if code, _ := send(t, "GET", base+"/api/v1/me", ""); code != http.StatusOK {
		t.Fatalf("a read: %d", code)
	}
	if code, _ := send(t, "POST", base+"/api/v1/conversations/F0/messages", `{"text":"nobody asked"}`); code != http.StatusForbidden {
		t.Fatalf("a write nobody confirmed: %d", code)
	}
	if code, _ := send(t, "POST", base+"/api/v1/notes/n1/boom", `{"text":"the API fails"}`); code != http.StatusInternalServerError {
		t.Fatalf("a rule's write the API failed: %d", code)
	}
	p.grant("ex", "s-card", larkCall{Method: "POST", Path: "/conversations/F9/boom"}, card)
	if code, _ := send(t, "POST", anon+"/api/v1/conversations/F9/boom", `{"text":"fails too"}`); code != http.StatusInternalServerError {
		t.Fatalf("a confirmed write the API failed: %d", code)
	}

	if code, _ := send(t, "POST", base+"/api/v1/notes/n1", `{"text":"from a rule","n":1}`); code != http.StatusOK {
		t.Fatalf("a rule's write: %d", code)
	}
	p.grant("ex", "s-card", larkCall{Method: "POST", Path: "/conversations/F1/messages", Body: `{"text":"hi"}`}, card)
	if code, _ := send(t, "POST", anon+"/api/v1/conversations/F1/messages", `{"text":"hi"}`); code != http.StatusOK {
		t.Fatalf("a confirmed write: %d", code)
	}
	p.grant("ex", "s-always", larkCall{Method: "POST", Path: "/conversations/F2/messages"}, proto.LarkLetThrough{Kind: proto.LarkThroughAlways})
	if code, _ := send(t, "POST", anon+"/api/v1/conversations/F2/messages", `{"text":"again"}`); code != http.StatusOK {
		t.Fatalf("a write Always allow covers: %d", code)
	}
	// A rule's allow that the gate also confirmed (a health conversation confirms every write) was let through by the
	// card, which is used up by it: the next such write is the rule's.
	p.grant("ex", "s-proc", larkCall{Method: "POST", Path: "/notes/n2"}, card)
	for i := 0; i < 2; i++ {
		if code, _ := send(t, "POST", base+"/api/v1/notes/n2", `{"text":"twice"}`); code != http.StatusOK {
			t.Fatalf("a rule's write the card also covers: %d", code)
		}
	}

	got := hub.reports(t, 5)
	if r := got["/notes/n1"]; r.Command != "ex" || r.Method != "POST" || r.Text != "from a rule" || r.SessionID != "s-proc" || r.LetThrough.Kind != proto.LarkThroughRule ||
		r.LetThrough.Rule == nil || r.LetThrough.Rule.Method != "POST" || r.LetThrough.Rule.Path != "/notes/**" || r.LetThrough.Rule.Action != "allow" {
		t.Fatalf("the rule's write: %+v", r)
	}
	if r := got["/conversations/F1/messages"]; r.Method != "POST" || r.Text != "hi" || r.SessionID != "s-card" || r.LetThrough != card {
		t.Fatalf("the confirmed write: %+v", r)
	}
	if r := got["/conversations/F2/messages"]; r.Text != "again" || r.SessionID != "s-always" || r.LetThrough != (proto.LarkLetThrough{Kind: proto.LarkThroughAlways}) {
		t.Fatalf("the write Always allow covers: %+v", r)
	}
	// Both /notes/n2 writes are in got under one key: the later one, the rule's, is what it holds (reports by path).
	hub.mu.Lock()
	var n2 []proto.LarkSent
	for _, s := range hub.got {
		if s.Path == "/notes/n2" {
			n2 = append(n2, s)
		}
	}
	hub.mu.Unlock()
	kinds := map[string]string{}
	for _, s := range n2 {
		kinds[s.LetThrough.Kind] = s.SessionID
	}
	if len(n2) != 2 || kinds[proto.LarkThroughCard] != "s-proc" || kinds[proto.LarkThroughRule] != "s-proc" {
		t.Fatalf("the writes a card and a rule both cover: %+v", n2)
	}
}

// What the report says it wrote is the body's words cut to 4000 characters, and reading the body for it does not
// change what the API gets; a body that is not JSON says none.
func TestGateProxyReportCutsTheText(t *testing.T) {
	api := newFakeAPI(t)
	n, hub, _ := ledgerNode(t, api.URL, proto.LarkRule{Method: "POST", Path: "/notes/**", Action: "allow"})
	base, release, err := n.gates().open("ex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	long := strings.Repeat("好", 5000)
	if code, _ := send(t, "POST", base+"/api/v1/notes/long", `{"title":"t","body":"`+long+`","text":"not this one"}`); code != http.StatusOK {
		t.Fatalf("a long write: %d", code)
	}
	if code, _ := send(t, "POST", base+"/api/v1/notes/plain", "plain words", "Content-Type", "text/plain"); code != http.StatusOK {
		t.Fatalf("a write that is not JSON: %d", code)
	}
	got := hub.reports(t, 2)
	if r := got["/notes/long"]; r.Text != strings.Repeat("好", gateTextMost) || r.SessionID != "" {
		t.Fatalf("the long write told as %d characters, session %q", len([]rune(r.Text)), r.SessionID)
	}
	if r := got["/notes/plain"]; r.Text != "" {
		t.Fatalf("the plain write told as %q", r.Text)
	}
	seen := api.requests()
	if len(seen) != 2 || strings.Count(seen[0].Body, "好") != 5000 || seen[1].Body != "plain words" {
		t.Fatalf("what the API got changed: %d requests", len(seen))
	}
}

// The report never holds the command's response, and a manager that fails it only leaves a line in the log.
func TestGateProxyReportDoesNotHoldTheResponse(t *testing.T) {
	api := newFakeAPI(t)
	n, hub, logs := ledgerNode(t, api.URL, proto.LarkRule{Method: "POST", Path: "/notes/**", Action: "allow"})
	base, release, err := n.gates().open("ex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	hub.mu.Lock()
	hub.hold = make(chan struct{})
	hub.mu.Unlock()
	if code, body := send(t, "POST", base+"/api/v1/notes/slow", `{"text":"hi"}`); code != http.StatusOK || body != `{"ok":true}` {
		t.Fatalf("a write while the manager is slow: %d %s", code, body)
	}
	hub.mu.Lock()
	told := len(hub.got)
	close(hub.hold)
	hub.hold = nil
	hub.mu.Unlock()
	if told != 0 {
		t.Fatalf("%d reports before the manager answered", told)
	}
	hub.reports(t, 1)

	hub.mu.Lock()
	hub.fail = true
	hub.mu.Unlock()
	if code, body := send(t, "POST", base+"/api/v1/notes/failing", `{"text":"secret words"}`); code != http.StatusOK || body != `{"ok":true}` {
		t.Fatalf("a write whose report fails: %d %s", code, body)
	}
	waitFor(t, "the failure in the log", func() bool { return strings.Contains(logs.String(), "telling the manager of POST /notes/failing") })
	if strings.Contains(logs.String(), "secret words") {
		t.Fatalf("the words reached the log: %s", logs)
	}
}

// larkCheck's side of it: the card a write was confirmed on, who answered it and the conversation come to the proxy
// with the grant, and "Always allow for this session" makes the next write the conversation's own, without a card.
func TestGateReportNamesTheCardAndTheConversation(t *testing.T) {
	api := newFakeAPI(t)
	h := newScriptHub(t, func(context.Context, proto.Interaction) (any, error) {
		return proto.Resolution{Allow: boolp(true), Always: true, AnsweredBy: proto.AnsweredByAssistant}, nil
	}, notifyOK)
	n := h.n
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = proto.AssistantLark{Gates: []proto.CommandGate{proxiedGate(api.URL)}}, time.Now()
	n.mu.Unlock()
	t.Cleanup(func() { n.gates().close() })
	base, release, err := n.gates().open("ex", "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, c := range []struct{ path, text string }{{"F1", "first"}, {"F2", "second"}} {
		ix := proto.Interaction{SessionID: "s1", ExpiresAt: time.Now().Add(time.Minute)}
		cmd := `ex POST /conversations/` + c.path + `/messages '{"text":"` + c.text + `"}'`
		if d, _, err := n.larkCheck(context.Background(), "s1", t.TempDir(), cmd, ix); err != nil || d != "allow" {
			t.Fatalf("%s: %s %v", c.path, d, err)
		}
		if code, _ := send(t, "POST", base+"/api/v1/conversations/"+c.path+"/messages", `{"text":"`+c.text+`"}`); code != http.StatusOK {
			t.Fatalf("%s: %d", c.path, code)
		}
	}
	waitFor(t, "the reports", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.larkSent) == 2
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.created) != 1 || h.created[0].ID == "" {
		t.Fatalf("cards %+v: one, with an id of its own", h.created)
	}
	first, second := h.larkSent[0], h.larkSent[1]
	if first.Path == "/conversations/F2/messages" {
		first, second = second, first
	}
	want := proto.LarkLetThrough{Kind: proto.LarkThroughCard, InteractionID: h.created[0].ID, AnsweredBy: proto.AnsweredByAssistant}
	if first.Path != "/conversations/F1/messages" || first.Text != "first" || first.SessionID != "s1" || first.LetThrough != want {
		t.Fatalf("the write the card allowed: %+v, want %+v", first, want)
	}
	if second.Text != "second" || second.SessionID != "s1" || second.LetThrough != (proto.LarkLetThrough{Kind: proto.LarkThroughAlways}) {
		t.Fatalf("the write Always allow covered: %+v", second)
	}
}

// The status the API answered with decides: any 2xx is a write that went out (201 and 204 as much as 200); a
// redirect, 304 and the failures are not.
func TestGateProxyReportsOnly2xx(t *testing.T) {
	api := newFakeAPI(t)
	n, hub, _ := ledgerNode(t, api.URL, proto.LarkRule{Method: "POST", Path: "/notes/**", Action: "allow"})
	base, release, err := n.gates().open("ex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, c := range []struct {
		path string
		code int
		told bool
	}{
		{"/notes/a", 200, true},
		{"/notes/b/status/201", 201, true},
		{"/notes/c/status/204", 204, true},
		{"/notes/d/status/301", 301, false},
		{"/notes/e/status/304", 304, false},
		{"/notes/f/status/404", 404, false},
		{"/notes/g/status/503", 503, false},
	} {
		if code, _ := send(t, "POST", base+"/api/v1"+c.path, `{"text":"x"}`); code != c.code {
			t.Fatalf("%s: the command got %d", c.path, code)
		}
	}
	got := hub.reports(t, 3)
	for _, path := range []string{"/notes/a", "/notes/b/status/201", "/notes/c/status/204"} {
		if _, ok := got[path]; !ok {
			t.Errorf("%s was not told: %+v", path, got)
		}
	}
}

// An answer the proxy could not finish passing on (the API sent its headers, a 2xx, and dropped the body) is a write the
// API took all the same: it is told, though ReverseProxy aborts the response with a panic.
func TestGateProxyReportsAWriteWhoseBodyBroke(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush() // and goes: the connection ends short of what it said
	}))
	t.Cleanup(api.Close)
	n, hub, _ := ledgerNode(t, api.URL, proto.LarkRule{Method: "POST", Path: "/notes/**", Action: "allow"})
	base, release, err := n.gates().open("ex", "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	req, _ := http.NewRequest("POST", base+"/api/v1/notes/cut", strings.NewReader(`{"text":"half an answer"}`))
	req.Header.Set("Content-Type", "application/json")
	if res, err := http.DefaultClient.Do(req); err == nil { // the command may see the headers or only the break
		_, _ = io.ReadAll(res.Body)
		res.Body.Close()
	}
	got := hub.reportList(t, 1)
	if r := got[0]; r.Path != "/notes/cut" || r.Text != "half an answer" || r.SessionID != "s1" || r.LetThrough.Kind != proto.LarkThroughRule {
		t.Fatalf("told %+v", got)
	}
}

// A grant belongs to the conversation larkCheck let the call through for: another conversation's identical call does not
// use it up, and the ledger names the conversation and the card of the one that did. A process the node knew no
// conversation of takes any, and is told as the grant's.
func TestGateGrantsStayWithTheirConversation(t *testing.T) {
	api := newFakeAPI(t)
	n, hub, _ := ledgerNode(t, api.URL)
	p := n.gates()
	open := func(session string) string {
		t.Helper()
		base, release, err := p.open("ex", session)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
		return base
	}
	a, b, anon := open("sA"), open("sB"), open("")
	call := larkCall{Method: "POST", Path: "/conversations/F1/messages"}
	cardA := proto.LarkLetThrough{Kind: proto.LarkThroughCard, InteractionID: "ix_A"}
	cardB := proto.LarkLetThrough{Kind: proto.LarkThroughCard, InteractionID: "ix_B", AnsweredBy: proto.AnsweredByAssistant}
	p.grant("ex", "sA", call, cardA)
	p.grant("ex", "sB", call, cardB)
	write := func(base string) int {
		code, _ := send(t, "POST", base+"/api/v1/conversations/F1/messages", `{"text":"same call"}`)
		return code
	}
	if code := write(b); code != http.StatusOK {
		t.Fatalf("B with its own grant: %d", code)
	}
	if code := write(b); code != http.StatusForbidden {
		t.Fatalf("B with only A's grant left: %d", code)
	}
	if code := write(a); code != http.StatusOK {
		t.Fatalf("A with its own grant: %d", code)
	}
	p.grant("ex", "sA", call, cardA)
	if code := write(anon); code != http.StatusOK {
		t.Fatalf("a process with no conversation known: %d", code)
	}
	bySession := map[string][]proto.LarkLetThrough{}
	for _, r := range hub.reportList(t, 3) {
		bySession[r.SessionID] = append(bySession[r.SessionID], r.LetThrough)
	}
	if len(bySession["sB"]) != 1 || bySession["sB"][0] != cardB || len(bySession["sA"]) != 2 || bySession["sA"][0] != cardA || bySession["sA"][1] != cardA {
		t.Fatalf("the ledger got %+v", bySession)
	}
}

// Reports in flight are limited; one over waits a moment for a place and then is given up on with a line in the log,
// while the command's response is not held back more than that.
func TestGateReportWaitsForAPlaceThenGivesUp(t *testing.T) {
	api := newFakeAPI(t)
	n, hub, logs := ledgerNode(t, api.URL, proto.LarkRule{Method: "POST", Path: "/notes/**", Action: "allow"})
	base, release, err := n.gates().open("ex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	hub.mu.Lock()
	hub.hold = make(chan struct{})
	hub.mu.Unlock()
	for i := 0; i < gateReportsMax+1; i++ {
		start := time.Now()
		if code, _ := send(t, "POST", base+"/api/v1/notes/n"+strconv.Itoa(i), `{"text":"hi"}`); code != http.StatusOK {
			t.Fatalf("write %d: %d", i, code)
		}
		if i < gateReportsMax && time.Since(start) >= gateReportWait {
			t.Fatalf("write %d waited %v with places left", i, time.Since(start))
		}
	}
	// The response is not held for it: the command has its answer while the report still waits for a place.
	waitFor(t, "the line about the one over the limit", func() bool {
		return strings.Contains(logs.String(), "too many reports in flight")
	})
	hub.mu.Lock()
	close(hub.hold)
	hub.hold = nil
	hub.mu.Unlock()
	hub.reportList(t, gateReportsMax)
}

// A write the manager let through from an "Always allow" it kept (it says so: remembered) was not let through by a card,
// whatever the card's id: the ledger says conversation_always.
func TestGateReportRememberedAlwaysIsNotACard(t *testing.T) {
	api := newFakeAPI(t)
	h := newScriptHub(t, func(context.Context, proto.Interaction) (any, error) {
		return proto.Resolution{Allow: boolp(true), Always: true, Remembered: true}, nil
	}, notifyOK)
	n := h.n
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = proto.AssistantLark{Gates: []proto.CommandGate{proxiedGate(api.URL)}}, time.Now()
	n.mu.Unlock()
	t.Cleanup(func() { n.gates().close() })
	base, release, err := n.gates().open("ex", "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ix := proto.Interaction{SessionID: "s1", ExpiresAt: time.Now().Add(time.Minute)}
	if d, _, err := n.larkCheck(context.Background(), "s1", t.TempDir(), `ex POST /conversations/F1/messages '{"text":"hi"}'`, ix); err != nil || d != "allow" {
		t.Fatalf("%s %v", d, err)
	}
	if code, _ := send(t, "POST", base+"/api/v1/conversations/F1/messages", `{"text":"hi"}`); code != http.StatusOK {
		t.Fatalf("the write: %d", code)
	}
	waitFor(t, "the report", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.larkSent) == 1
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.larkSent[0]; r.SessionID != "s1" || r.LetThrough != (proto.LarkLetThrough{Kind: proto.LarkThroughAlways}) {
		t.Fatalf("told %+v", r)
	}
}

// reply is what a fake API answers one path with.
type reply struct {
	status int
	header map[string]string
	body   []byte
}

// replyingAPI answers each path (without the gate's /api/v1 prefix) with its reply: 200 and JSON when it names none.
func replyingAPI(t *testing.T, answers map[string]reply) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		a, ok := answers[strings.TrimPrefix(r.URL.Path, "/api/v1")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		for k, v := range a.header {
			w.Header().Set(k, v)
		}
		if a.status != 0 {
			w.WriteHeader(a.status)
		}
		_, _ = w.Write(a.body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// rawSend is send that keeps the answer as it came: the status, the headers and the body's bytes, nothing decoded.
// The request says it takes gzip, as many clients do, so the API's gzip answer reaches it compressed.
func rawSend(t *testing.T, url, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil { // an answer cut short is not a shorter one
		t.Errorf("%s: the answer broke off after %d bytes: %v", url, len(b), err)
	}
	return res, b
}

func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return b.Bytes()
}

// The id the API's answer gave for the message a write made goes to the manager with the report, in the shapes a
// gateway's receipt and the Open Platform's answer have. An answer that has none, or cannot be read (not JSON, not
// JSON by its Content-Type, cut off, compressed in a way the proxy does not read, over the copy's limit), is told all the
// same, with no id; the command got its answer unchanged in every case.
func TestGateProxyTellsTheMessageIDTheAPIGave(t *testing.T) {
	pad := strings.Repeat("a", gateReplyMax)
	answers := map[string]reply{}
	wants := map[string]string{}
	add := func(name string, a reply, ref string) {
		answers["/conversations/"+name+"/messages"] = a
		wants["/conversations/"+name+"/messages"] = ref
	}
	json200 := func(s string) reply { return reply{body: []byte(s)} }
	add("open-platform", json200(`{"code":0,"msg":"success","data":{"chat_id":"oc_1","message_id":"om_1"}}`), "om_1")
	add("gateway", json200(`{"messageId":"om_2","clientId":"c1"}`), "om_2")
	add("receipt", json200(`{"ok":true,"message_id":"om_3"}`), "om_3")
	add("data-camel", json200(`{"data":{"messageId":"om_4"}}`), "om_4")
	add("both", json200(`{"message_id":"top","data":{"message_id":"inner"}}`), "inner")
	add("opaque", json200(`{"message_id":"x:123/45"}`), "x:123/45")
	add("created", reply{status: http.StatusCreated, body: []byte(`{"messageId":"om_5"}`)}, "om_5")
	add("vendor-json", reply{header: map[string]string{"Content-Type": "application/vnd.api+json"}, body: []byte(`{"message_id":"om_6"}`)}, "om_6")
	add("exactly the most", json200(`{"message_id":"`+strings.Repeat("长", proto.MessageRefMost)+`"}`), strings.Repeat("长", proto.MessageRefMost))
	add("over the most is none, not cut", json200(`{"message_id":"`+strings.Repeat("长", proto.MessageRefMost+1)+`"}`), "")
	add("padded to the most", json200(`{"message_id":" `+strings.Repeat("a", proto.MessageRefMost)+` "}`), strings.Repeat("a", proto.MessageRefMost))
	add("gzip", reply{header: map[string]string{"Content-Encoding": "gzip"}, body: gzipped(t, `{"data":{"message_id":"om_7"}}`)}, "om_7")
	add("none", json200(`{"ok":true}`), "")
	add("empty-id", json200(`{"data":{"message_id":" "},"message_id":""}`), "")
	add("number", json200(`{"message_id":5}`), "")
	add("array", json200(`["om_x"]`), "")
	add("null", json200(`null`), "")
	add("cut-off", json200(`{"message_id":"om_8"`), "")
	add("not-json", reply{header: map[string]string{"Content-Type": "text/plain"}, body: []byte(`{"message_id":"om_9"}`)}, "")
	add("no-content", reply{status: http.StatusNoContent}, "")
	add("brotli", reply{header: map[string]string{"Content-Encoding": "br"}, body: []byte(`{"message_id":"om_10"}`)}, "")
	add("broken-gzip", reply{header: map[string]string{"Content-Encoding": "gzip"}, body: []byte(`{"message_id":"om_11"}`)}, "")
	add("too-long", json200(`{"message_id":"om_12","pad":"`+pad+`"}`), "")
	// Whole JSON in the first gateReplyMax bytes and more after it (blanks): over the limit all the same, not read.
	add("too-long-by-blanks", json200(`{"message_id":"om_14"}`+strings.Repeat(" ", gateReplyMax)), "")
	add("gzip-too-long", reply{header: map[string]string{"Content-Encoding": "gzip"}, body: gzipped(t, `{"message_id":"om_13","pad":"`+pad+`"}`)}, "")
	api := replyingAPI(t, answers)

	n, hub, _ := ledgerNode(t, api.URL, proto.LarkRule{Method: "POST", Path: "/conversations/**", Action: "allow"})
	base, release, err := n.gates().open("ex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for path, a := range answers {
		res, got := rawSend(t, base+"/api/v1"+path, `{"text":"hi"}`)
		wantStatus := a.status
		if wantStatus == 0 {
			wantStatus = http.StatusOK
		}
		if res.StatusCode != wantStatus || !bytes.Equal(got, a.body) {
			t.Errorf("%s: the command got %d with %d bytes, want %d with the API's %d bytes", path, res.StatusCode, len(got), wantStatus, len(a.body))
		}
	}
	reports := hub.reports(t, len(answers))
	for path, ref := range wants {
		if r, ok := reports[path]; !ok || r.MessageRef != ref || r.Text != "hi" {
			t.Errorf("%s: told %+v, want message_ref %q", path, r, ref)
		}
	}
}

// What the proxy keeps of an answer is a copy: the command gets the bytes the API sent, headers and status included, as
// they come: an answer that is still being written reaches the command before it is finished (streams go as they come),
// and the id is read from the copy of all of it, however the API split it. An answer longer than the copy's limit passes
// whole.
func TestGateProxyWatchesTheAnswerWithoutChangingIt(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	defer letGo()
	big := `{"message_id":"om_big","pad":"` + strings.Repeat("b", 3*gateReplyMax) + `"}`
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost { // not the gate's: a Persona node on this machine probes the ports tests listen on (ports.go)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Answer", "as sent")
		switch strings.TrimPrefix(r.URL.Path, "/api/v1") {
		case "/conversations/stream/messages":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"messa`))
			w.(http.Flusher).Flush()
			<-release
			_, _ = w.Write([]byte(`ge_id":"om_s"}}`))
		case "/conversations/big/messages":
			_, _ = w.Write([]byte(big))
		}
	}))
	t.Cleanup(api.Close)
	n, hub, _ := ledgerNode(t, api.URL, proto.LarkRule{Method: "POST", Path: "/conversations/**", Action: "allow"})
	base, rel, err := n.gates().open("ex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer rel()

	// A proxy that held the answer back would leave the command waiting for the API, which waits for the test: the
	// deadline turns that into a failure.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/api/v1/conversations/stream/messages", strings.NewReader(`{"text":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	first := make([]byte, len(`{"data":{"messa`))
	if _, err := io.ReadFull(res.Body, first); err != nil || string(first) != `{"data":{"messa` { // the API is still writing
		t.Fatalf("the first part of the answer: %q %v", first, err)
	}
	if res.StatusCode != http.StatusCreated || res.Header.Get("X-Answer") != "as sent" {
		t.Fatalf("status %d, X-Answer %q", res.StatusCode, res.Header.Get("X-Answer"))
	}
	if told := len(hub.reportsNow()); told != 0 {
		t.Fatalf("%d reports while the answer was still coming", told)
	}
	letGo()
	rest, _ := io.ReadAll(res.Body)
	if string(rest) != `ge_id":"om_s"}}` {
		t.Fatalf("the rest of the answer: %q", rest)
	}

	if _, got := rawSend(t, base+"/api/v1/conversations/big/messages", `{"text":"hi"}`); string(got) != big {
		t.Fatalf("an answer over the copy's limit: %d bytes, want %d", len(got), len(big))
	}
	reports := hub.reports(t, 2)
	if r := reports["/conversations/stream/messages"]; r.MessageRef != "om_s" {
		t.Errorf("the answer written in two parts: told %+v", r)
	}
	if r := reports["/conversations/big/messages"]; r.MessageRef != "" || r.Text != "hi" {
		t.Errorf("the answer over the limit: told %+v", r)
	}
}

// lateBody is a request's body that, once read to its end, holds any further read until the answer has begun to go
// out: what a goroutine that is slow to get to the body does to the proxy's request on its way to the API.
type lateBody struct {
	io.ReadCloser
	answered <-chan struct{}
	ended    atomic.Bool
}

func (b *lateBody) Read(p []byte) (int, error) {
	if b.ended.Load() {
		select {
		case <-b.answered:
		case <-time.After(10 * time.Second):
		}
	}
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.ended.Store(true)
	}
	return n, err
}

// answerSignal closes answered when the first of the answer has been flushed to the command, by when net/http has closed
// the request's body.
type answerSignal struct {
	http.ResponseWriter
	once     sync.Once
	answered chan struct{}
}

func (a *answerSignal) Flush() {
	a.ResponseWriter.(http.Flusher).Flush()
	a.once.Do(func() { close(a.answered) })
}

// Once the proxy has begun to answer, net/http no longer lets a handler read the request's body (a read fails with
// "http: invalid Read on closed Body"), so what the proxy sends on to the API must not read the server's body past the
// end of the request: a transport that reads it after the API's answer has begun, as it does to see that the body had
// no more than its length, closes the connection to the API with the answer half read, and the command gets half an
// answer. The API here answers at once and then holds the rest until the connection is cut or a moment has gone by.
// Each shape of request goes another way through the proxy: a JSON body read whole, one over what is read to match it
// (by one byte, and by more), a body that is not JSON, and each of them sent without a length (chunked); the API must
// get each body as it was sent.
func TestGateProxyDoesNotCutAnAnswerWhenTheRequestBodyIsClosed(t *testing.T) {
	const first, rest = `{"data":{"message_id":"om_late","note":"`, `all of it"}}`
	var mu sync.Mutex
	took := map[string]string{} // the body the API got, by path
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost { // not the gate's: a Persona node on this machine probes the ports tests listen on (ports.go)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		took[r.URL.Path] = string(b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(first))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done(): // the proxy let go of the connection
		case <-time.After(300 * time.Millisecond):
		}
		_, _ = w.Write([]byte(rest))
	}))
	t.Cleanup(api.Close)
	n, hub, _ := ledgerNode(t, api.URL, proto.LarkRule{Method: "POST", Path: "/conversations/**", Action: "allow"})
	gp := n.gates()
	base, release, err := gp.open("ex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// The proxy behind a server of the test's own, to hold what the proxy's request reads of the body.
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answered := make(chan struct{}) // one for each request
		r.Body = &lateBody{ReadCloser: r.Body, answered: answered}
		gp.ServeHTTP(&answerSignal{ResponseWriter: w, answered: answered}, r)
	}))
	t.Cleanup(front.Close)

	// A JSON body of n bytes.
	jsonOf := func(n int) string { return `{"text":"` + strings.Repeat("a", n-len(`{"text":""}`)) + `"}` }
	const (
		jsonType  = "application/json"
		otherType = "application/octet-stream"
	)
	cases := []struct {
		name, contentType, body string
		chunked                 bool
		text                    string // what the ledger line says the write said: none for a body not read whole
	}{
		{"json", jsonType, `{"text":"hi"}`, false, "hi"},
		{"json one over the most", jsonType, jsonOf(gateBodyMax + 1), false, ""},
		{"json over the most", jsonType, jsonOf(gateBodyMax + 100), false, ""},
		{"json chunked", jsonType, `{"text":"hi"}`, true, "hi"},
		{"json chunked over the most", jsonType, jsonOf(gateBodyMax + 100), true, ""},
		{"not json", otherType, "0123456789", false, ""},
		{"not json chunked", otherType, "0123456789", true, ""},
		{"form", "application/x-www-form-urlencoded", "text=hi", false, ""},
	}
	pathOf := func(i int) string { return "/conversations/late" + strconv.Itoa(i) + "/messages" }
	t.Run("requests", func(t *testing.T) {
		for i, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				req, err := http.NewRequest("POST", front.URL+base[strings.LastIndex(base, "/"):]+"/api/v1"+pathOf(i), strings.NewReader(c.body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", c.contentType)
				if c.chunked {
					req.ContentLength = -1
				}
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				got, err := io.ReadAll(res.Body)
				if err != nil || res.StatusCode != http.StatusOK || string(got) != first+rest {
					t.Errorf("the command got %d with %q (%v), want 200 with %q", res.StatusCode, got, err, first+rest)
				}
				mu.Lock()
				body := took["/api/v1"+pathOf(i)]
				mu.Unlock()
				if body != c.body {
					t.Errorf("the API got a body of %d bytes, want the %d bytes sent", len(body), len(c.body))
				}
			})
		}
	})
	if t.Failed() {
		return
	}
	reports := hub.reports(t, len(cases))
	for i, c := range cases {
		if r := reports[pathOf(i)]; r.MessageRef != "om_late" || r.Text != c.text {
			t.Errorf("%s: told %+v, want message_ref om_late and text %q", c.name, r, c.text)
		}
	}
}

// A command that does not ask for gzip gets an answer the proxy's own transport decompressed, and the id is read from
// that all the same.
func TestGateProxyReadsAnAnswerItsTransportDecompressed(t *testing.T) {
	api := replyingAPI(t, map[string]reply{"/conversations/c1/messages": {header: map[string]string{"Content-Encoding": "gzip"},
		body: gzipped(t, `{"data":{"message_id":"om_z"}}`)}})
	n, hub, _ := ledgerNode(t, api.URL, proto.LarkRule{Method: "POST", Path: "/conversations/**", Action: "allow"})
	base, release, err := n.gates().open("ex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	req, _ := http.NewRequest("POST", base+"/api/v1/conversations/c1/messages", strings.NewReader(`{"text":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if got, _ := io.ReadAll(res.Body); string(got) != `{"data":{"message_id":"om_z"}}` || res.Header.Get("Content-Encoding") != "" {
		t.Fatalf("the command got %q (Content-Encoding %q)", got, res.Header.Get("Content-Encoding"))
	}
	if r := hub.reports(t, 1)["/conversations/c1/messages"]; r.MessageRef != "om_z" {
		t.Fatalf("told %+v", r)
	}
}

// gunzipCapped and readCapped read at most most+1 bytes: an answer gzipped from a few kilobytes into many megabytes (a
// bomb) is refused after the cap, not inflated.
func TestGunzipCapped(t *testing.T) {
	const most = 1 << 10
	for _, c := range []struct {
		name string
		in   []byte
		ok   bool
	}{
		{"a small answer", gzipped(t, `{"message_id":"om_1"}`), true},
		{"exactly the most", gzipped(t, strings.Repeat("a", most)), true},
		{"one byte over", gzipped(t, strings.Repeat("a", most+1)), false},
		{"a bomb", gzipped(t, strings.Repeat("\x00", 8<<20)), false},
		{"not gzip", []byte(`{"message_id":"om_2"}`), false},
		{"cut off", gzipped(t, strings.Repeat("a", most))[:20], false},
	} {
		if got, ok := gunzipCapped(c.in, most); ok != c.ok || (ok && len(got) == 0) || len(got) > most {
			t.Errorf("%s: %d bytes, %v, want %v", c.name, len(got), ok, c.ok)
		}
	}
}

// endless is a reader that never ends: it fails once it has served far more than anyone should read, so a read without a
// limit ends in a failure of the test and not a hang.
type endless struct{ served, giveUp int }

func (e *endless) Read(p []byte) (int, error) {
	if e.served >= e.giveUp {
		return 0, errors.New("read far past the limit")
	}
	n := min(len(p), e.giveUp-e.served)
	clear(p[:n])
	e.served += n
	return n, nil
}

func TestReadCappedReadsOneByteOverAtMost(t *testing.T) {
	const most = 1 << 10
	e := &endless{giveUp: 8 << 20}
	if got, ok := readCapped(e, most); ok || got != nil || e.served > most+1 {
		t.Fatalf("an endless reader: %d bytes, %v, after %d bytes served (at most %d may be)", len(got), ok, e.served, most+1)
	}
	if got, ok := readCapped(strings.NewReader(strings.Repeat("a", most)), most); !ok || len(got) != most {
		t.Fatalf("exactly the most: %d bytes, %v", len(got), ok)
	}
}

// hangup is a client that took the first part of an answer and went away: its later writes fail.
type hangup struct {
	*httptest.ResponseRecorder
	took   chan struct{} // closed once the first part was taken
	writes int
}

func (h *hangup) Write(b []byte) (int, error) {
	if h.writes++; h.writes > 1 {
		return 0, errors.New("the client went away")
	}
	n, err := h.ResponseRecorder.Write(b)
	close(h.took)
	return n, err
}

// A command that goes away while the API is still writing its answer ends the proxy's copy of it there: the write is
// told to the manager all the same, with what it said, and with no id (the copy is of what the command got, which was
// half an answer), and nothing panics.
func TestGateProxyReportsAWriteWhoseCommandWentAway(t *testing.T) {
	var apiWrote sync.WaitGroup
	apiWrote.Add(1)
	took := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost { // not the gate's: a Persona node on this machine probes the ports tests listen on (ports.go)
			return
		}
		defer apiWrote.Done()
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"message_id":"om_x"`))
		w.(http.Flusher).Flush()
		select { // the command takes that and goes: the rest is for nobody
		case <-took:
		case <-time.After(5 * time.Second):
		}
		_, _ = w.Write([]byte(`}}`))
	}))
	t.Cleanup(api.Close)
	n, hub, _ := ledgerNode(t, api.URL, proto.LarkRule{Method: "POST", Path: "/conversations/**", Action: "allow"})
	p := n.gates()
	base, release, err := p.open("ex", "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	key := strings.TrimPrefix(base[strings.LastIndex(base, "/"):], "/")

	// The proxy's own handler, driven with a writer that fails: this is the proxy's view of a command that went away,
	// whatever the network says first.
	w := &hangup{ResponseRecorder: httptest.NewRecorder(), took: took}
	req := httptest.NewRequest("POST", "/"+key+"/api/v1/conversations/c1/messages", strings.NewReader(`{"text":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("the proxy panicked: %v", r)
			}
		}()
		p.ServeHTTP(w, req)
	}()
	apiWrote.Wait()
	if w.writes != 2 || w.Body.String() != `{"data":{"message_id":"om_x"` {
		t.Fatalf("the command got %q after %d write(s)", w.Body.String(), w.writes)
	}
	got := hub.reportList(t, 1)
	if r := got[0]; r.Path != "/conversations/c1/messages" || r.Text != "hi" || r.MessageRef != "" {
		t.Fatalf("told %+v: the write, with what it said and no id", got)
	}
}

// The same on a real connection: the command reads the first part of the answer and closes it while the API goes on.
func TestGateProxyReportsAWriteWhoseCommandClosedTheConnection(t *testing.T) {
	woke := make(chan string, 1) // what ended the API's wait: the proxy letting go of its request, or the timeout
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost { // not the gate's: a Persona node on this machine probes the ports tests listen on (ports.go)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"message_id":"om_y"`))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			woke <- "the proxy let go of the request"
		case <-time.After(5 * time.Second):
			woke <- "the timeout"
		}
	}))
	t.Cleanup(api.Close)
	n, hub, _ := ledgerNode(t, api.URL, proto.LarkRule{Method: "POST", Path: "/conversations/**", Action: "allow"})
	base, release, err := n.gates().open("ex", "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/api/v1/conversations/c2/messages", strings.NewReader(`{"text":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, len(`{"data":{"message_id":"om_y"`))
	if _, err := io.ReadFull(res.Body, first); err != nil || string(first) != `{"data":{"message_id":"om_y"` {
		t.Fatalf("the first part of the answer: %q %v", first, err)
	}
	res.Body.Close() // the command goes away with the answer unfinished
	select {
	case why := <-woke: // at once if the proxy cancels what it asked the API (its timer is 5 s)
		if why != "the proxy let go of the request" {
			t.Fatalf("the API's wait ended by %s: the proxy went on with an answer nobody takes", why)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the API's wait never ended")
	}
	got := hub.reportList(t, 1)
	if r := got[0]; r.Path != "/conversations/c2/messages" || r.Text != "hi" || r.SessionID != "s1" || r.MessageRef != "" {
		t.Fatalf("told %+v", got)
	}
}

// The process always learns its own address at the gate proxy, even when the gate names no environment of its own or
// lacks the base variable, and never gets it twice (#13540).
func TestGateAddressAlwaysInEnv(t *testing.T) {
	api := newFakeAPI(t)
	n, cwd := gateNode(t, api.URL)
	envOfName := func(env []string, name string) int {
		n := 0
		for _, kv := range env {
			if k, _, _ := strings.Cut(kv, "="); k == name {
				n++
			}
		}
		return n
	}
	// No environment at all: the address is still there, once.
	n.larkCfg.Gates[0].Env = nil
	env, release := n.assistantLarkEnv(cwd, "")
	release()
	if base, ok := envOf(env, "EXAMPLE_BASE"); !ok || envOfName(env, "EXAMPLE_BASE") != 1 ||
		!regexp.MustCompile(`^http://127\.0\.0\.1:\d+/[0-9a-f]{32}$`).MatchString(base) {
		t.Fatalf("EXAMPLE_BASE %q in %v", base, env)
	}
	// The gate's other variables go in as before, and the address is not repeated.
	n.larkCfg.Gates[0].Env = map[string]string{"EXAMPLE_BASE": "old", "EXAMPLE_ACCOUNT": "2"}
	env, release = n.assistantLarkEnv(cwd, "")
	release()
	if base, _ := envOf(env, "EXAMPLE_BASE"); envOfName(env, "EXAMPLE_BASE") != 1 || base == "old" || func() bool { v, _ := envOf(env, "EXAMPLE_ACCOUNT"); return v != "2" }() {
		t.Fatalf("environment %v", env)
	}
}

// A gate whose API a sandboxed extension names as host.persona.internal is dialed on this machine's loopback (#13540).
func TestGateProxyHostPersonaInternal(t *testing.T) {
	api := newFakeAPI(t)
	n, cwd := gateNode(t, api.URL)
	u, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	n.larkCfg.Gates[0].Env["EXAMPLE_BASE"] = "http://host.persona.internal:" + u.Port()
	env, release := n.assistantLarkEnv(cwd, "")
	defer release()
	base, _ := envOf(env, "EXAMPLE_BASE")
	if code, body := send(t, "GET", base+"/api/v1/me", ""); code != http.StatusOK || body != `{"ok":true}` {
		t.Fatalf("%d %s", code, body)
	}
	got := api.requests()
	if len(got) != 1 || got[0].Host != "127.0.0.1:"+u.Port() {
		t.Fatalf("dialed as %+v", got)
	}
}

// A gate the node does not know yet, or one whose address is not in its environment, is asked for again at once (at
// most every gateRefreshInterval), not after assistantLark's minute (#13540).
func TestAssistantLarkForGateRefreshes(t *testing.T) {
	hub := newScriptHub(t, func(context.Context, proto.Interaction) (any, error) {
		return proto.Resolution{Allow: boolp(true)}, nil
	}, notifyOK)
	n := hub.n
	stale := proto.CommandGate{Command: "ex", BaseEnv: "EX_BASE", Auth: &proto.GateAuth{}, Env: map[string]string{}}
	fresh := stale
	fresh.Env = map[string]string{"EX_BASE": "http://127.0.0.1:1"}
	hub.mu.Lock()
	hub.lark = proto.AssistantLark{Gates: []proto.CommandGate{fresh}}
	hub.mu.Unlock()
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = proto.AssistantLark{Gates: []proto.CommandGate{stale}}, time.Now().Add(-5*time.Second)
	n.mu.Unlock()
	cfg := n.assistantLarkForGate(t.Context(), "ex")
	if len(cfg.Gates) != 1 || cfg.Gates[0].Env["EX_BASE"] != "http://127.0.0.1:1" {
		t.Fatalf("refreshed: %+v", cfg)
	}
}

// An attempt just made throttles the next one even without an answer yet, so a hub outage is not dialled on every gate
// request; once the interval has passed it is tried again (#13540).
func TestAssistantLarkForGateThrottlesAnAttempt(t *testing.T) {
	hub := newScriptHub(t, func(context.Context, proto.Interaction) (any, error) {
		return proto.Resolution{Allow: boolp(true)}, nil
	}, notifyOK)
	n := hub.n
	stale := proto.CommandGate{Command: "ex", BaseEnv: "EX_BASE", Auth: &proto.GateAuth{}, Env: map[string]string{}}
	fresh := stale
	fresh.Env = map[string]string{"EX_BASE": "http://127.0.0.1:1"}
	hub.mu.Lock()
	hub.lark = proto.AssistantLark{Gates: []proto.CommandGate{fresh}}
	hub.mu.Unlock()
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt, n.larkCfgTryAt = proto.AssistantLark{Gates: []proto.CommandGate{stale}}, time.Now().Add(-5*time.Second), time.Now()
	n.mu.Unlock()
	if cfg := n.assistantLarkForGate(t.Context(), "ex"); cfg.Gates[0].Env["EX_BASE"] != "" {
		t.Fatalf("a recent attempt still called the hub: %+v", cfg)
	}
	n.mu.Lock()
	n.larkCfgTryAt = time.Now().Add(-5 * time.Second)
	n.mu.Unlock()
	if cfg := n.assistantLarkForGate(t.Context(), "ex"); cfg.Gates[0].Env["EX_BASE"] != "http://127.0.0.1:1" {
		t.Fatalf("a later request refreshed: %+v", cfg)
	}
}
