package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// getReadiness is GET /api/v1/assistant/readiness (query: its query string) as the hub relays it, in lang.
func getReadiness(t *testing.T, s *Service, query, lang string) Readiness {
	t.Helper()
	header := map[string][]string{}
	if lang != "" {
		header["Accept-Language"] = []string{lang}
	}
	res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{Method: "GET", Path: "/api/v1/assistant/readiness",
		Query: query, Header: header}, s.lang)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 {
		t.Fatalf("readiness: %d %s", res.Status, res.Body)
	}
	var out Readiness
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("readiness: %s", res.Body)
	}
	return out
}

func readinessStep(t *testing.T, r Readiness, id string) ReadinessStep {
	t.Helper()
	for _, st := range r.Steps {
		if st.ID == id {
			return st
		}
	}
	t.Fatalf("no step %s in %+v", id, r.Steps)
	return ReadinessStep{}
}

func nextOf(r Readiness) string {
	if r.Next == nil {
		return ""
	}
	return *r.Next
}

// readyMachine is a hub with the assistant's machine "server" on Claude Code 2.1.308, its main conversation m1.
func readyMachine(t *testing.T, main ...proto.Session) (*fakeHub, *Service, *memStore) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.doctor = map[string]string{"server": "2.1.308，支持 --plugin-dir"}
	f.settings = &Settings{Main: "m1"}
	if len(main) > 0 {
		f.setSessions("server", main...)
	}
	st := &memStore{}
	return f, newServiceOn(t, f, st), st
}

// With no computer the first missing step is the computer, said in plain words with what to do; what comes after it
// cannot be told, and the suggestions are the local ones only (docs/assistant/onboarding.md 5.1).
func TestReadinessWithoutComputer(t *testing.T) {
	s := newStorelessService(t, newFakeHub())
	r := getReadiness(t, s, "", "")
	c := readinessStep(t, r, StepComputer)
	if r.Version != 1 || r.Mode != ReadinessSetup || r.Ready || nextOf(r) != StepComputer || r.Machine != nil || r.Launcher != "claude" ||
		c.State != StepTodo || c.Code != proto.CodeNoMachine || c.Action == nil || c.Action.ID != "add_computer" ||
		c.Title != "添加你的电脑" || c.Detail != "Persona 要在你的一台电脑上干活，现在还没接上电脑。" {
		t.Fatalf("readiness = %+v", r)
	}
	if len(r.Steps) != 4 || r.Steps[1].ID != StepClaude || r.Steps[2].ID != StepModel || r.Steps[3].ID != StepFirstMessage {
		t.Fatalf("steps = %+v", r.Steps)
	}
	for _, st := range r.Steps[1:] {
		if st.State != StepUnknown {
			t.Fatalf("after the computer: %+v", st)
		}
	}
	for _, sg := range r.Suggestions {
		if !sg.Local {
			t.Fatalf("setup suggests %+v", sg)
		}
	}
	if len(r.Suggestions) != 2 || r.Probe != nil || r.Dismissed || r.CheckedAt.IsZero() {
		t.Fatalf("readiness = %+v", r)
	}
	// In English when the request says so.
	if c := readinessStep(t, getReadiness(t, s, "", "en"), StepComputer); c.Title != "Add your computer" {
		t.Fatalf("en: %+v", c)
	}
	// warm carries the same.
	status, body := serveJSON(t, s, "POST", "/api/v1/assistant/warm", "", `{}`)
	rd, _ := body["readiness"].(map[string]any)
	if status != 200 || body["ok"] != true || rd == nil || rd["mode"] != ReadinessSetup || rd["next"] != StepComputer {
		t.Fatalf("warm: %d %v", status, body)
	}
}

// A computer that is not online is named, and said to be asleep rather than the hub unreachable.
func TestReadinessComputerOffline(t *testing.T) {
	f := newFakeHub().withMachine()
	f.devices = []Device{{ID: "server", Name: "小林的 MacBook"}}
	r := getReadiness(t, newStorelessService(t, f), "", "")
	c := readinessStep(t, r, StepComputer)
	if r.Mode != ReadinessSetup || c.State != StepProblem || c.Code != "node_unreachable" || c.Action.ID != "wake_computer" ||
		c.Title != "小林的 MacBook 不在线" || r.Machine == nil || r.Machine.Name != "小林的 MacBook" || c.Machine != "server" {
		t.Fatalf("readiness = %+v", r)
	}
}

// A computer with Claude Code and a model never tried: the main conversation can go (full), the model is unknown
// rather than a problem, and nothing spent tokens to find out.
func TestReadinessFullButUnverified(t *testing.T) {
	f, s, _ := readyMachine(t)
	r := getReadiness(t, s, "", "")
	cl, m, first := readinessStep(t, r, StepClaude), readinessStep(t, r, StepModel), readinessStep(t, r, StepFirstMessage)
	if r.Mode != ReadinessFull || !r.Ready || nextOf(r) != StepModel || r.Machine == nil || r.Machine.ID != "server" ||
		cl.State != StepOK || cl.Version != "2.1.308" ||
		m.State != StepUnknown || m.Code != "model_unverified" || m.Action != nil || // the try is the first message's (#6317)
		first.State != StepTodo || first.Code != "never_tried" || first.Action == nil || first.Action.ID != "test_assistant" {
		t.Fatalf("readiness = %+v", r)
	}
	if len(f.checked) != 0 {
		t.Fatalf("checked without a probe: %v", f.checked)
	}
	// The self-check is asked for Claude Code's version only: without the path-access reads (#5944).
	f.mu.Lock()
	asked := 0
	for _, c := range f.calls {
		if c.Method == deviceNodeDoctor {
			asked++
			if string(c.Params) != `{"preflight":false}` {
				t.Errorf("node.doctor params %s", c.Params)
			}
		}
	}
	f.mu.Unlock()
	if asked == 0 {
		t.Error("node.doctor not asked")
	}
	if len(r.Suggestions) < 3 || r.Suggestions[2].ID != "today" || len(r.Suggestions[2].Needs) != 1 || r.Suggestions[2].Prompt == "" {
		t.Fatalf("suggestions = %+v", r.Suggestions)
	}
}

// The node's self-check found no claude for the plain launcher: Claude Code is missing.
func TestReadinessClaudeMissing(t *testing.T) {
	f, s, _ := readyMachine(t)
	f.doctor["server"] = "读不到 claude 的版本"
	r := getReadiness(t, s, "", "")
	cl := readinessStep(t, r, StepClaude)
	if r.Mode != ReadinessSetup || nextOf(r) != StepClaude || cl.State != StepProblem || cl.Code != proto.CodeClaudeMissing ||
		cl.Action.ID != "install_claude" || len(cl.AltActions) != 1 || cl.AltActions[0].ID != "connect_model" ||
		cl.Detail != "server 上还没装 Claude Code。装好它，或者改用 API Key。" {
		t.Fatalf("claude = %+v (%s)", cl, r.Mode)
	}
}

// A self-check whose claude --version did not answer this time (a busy machine's 10 s ran out) while the shell found
// claude is unknown, never claude_missing: Claude Code may well be there.
func TestReadinessClaudeUnanswered(t *testing.T) {
	f, s, _ := readyMachine(t)
	f.doctor["server"] = "这次没读到 claude 的版本（超时或出错），claude 本身找得到"
	f.doctorState = map[string]string{"server": "unanswered"}
	r := getReadiness(t, s, "", "")
	if cl := readinessStep(t, r, StepClaude); cl.State != StepUnknown || cl.Code == proto.CodeClaudeMissing || r.Mode != ReadinessFull {
		t.Fatalf("claude = %+v (%s)", cl, r.Mode)
	}
}

// The node found Claude Code when it connected, and a later self-check finds none: Claude Code updating itself through
// npm removes and relinks its package, and a check caught then must not block the main conversation. A start refused
// because claude is missing still says so.
func TestReadinessClaudeMissingWhileUpdating(t *testing.T) {
	f, s, _ := readyMachine(t)
	f.devices = []Device{{ID: "server", Name: "server", Online: true, Reachable: true, ClaudeVersion: "2.1.308"}}
	f.doctor["server"] = "读不到 claude 的版本"
	r := getReadiness(t, s, "", "")
	if cl := readinessStep(t, r, StepClaude); cl.State != StepUnknown || cl.Code == proto.CodeClaudeMissing || r.Mode != ReadinessFull {
		t.Fatalf("claude = %+v (%s)", cl, r.Mode)
	}
	data, _ := json.Marshal(map[string]string{"delivered": "none", "launcher": "claude"})
	f.mu.Lock()
	f.failAssistant = &rpc.Error{Code: proto.CodeClaudeMissing, Message: "zsh:1: command not found: claude", Data: data}
	f.mu.Unlock()
	if status, _ := serveJSON(t, s, "POST", "/api/v1/assistant/conversations", "", `{"prompt":"你好","main":true}`); status != 409 {
		t.Fatalf("start: %d", status)
	}
	if cl := readinessStep(t, getReadiness(t, s, "", ""), StepClaude); cl.Code != proto.CodeClaudeMissing || cl.Action.ID != "install_claude" {
		t.Fatalf("after a refused start: %+v", cl)
	}
}

// A launcher health check caught in the same self-update records "command not found: claude" once: the node's
// self-check decides, so Send stays on. A second miss in a row stands.
func TestReadinessClaudeHealthMissWhileUpdating(t *testing.T) {
	f, s, _ := readyMachine(t)
	f.devices = []Device{{ID: "server", Name: "server", Online: true, Reachable: true, ClaudeVersion: "2.1.308"}}
	record := LauncherHealth{Machine: "server", Launcher: "claude", State: healthFailing, Kind: classifyLauncherError("zsh:1: command not found: claude"),
		Error: "zsh:1: command not found: claude", CheckedAt: time.Now(), Source: healthSourceCheck, Fails: 1, MissingFails: 1}
	s.health.loaded = true
	s.health.recs = map[string]*LauncherHealth{healthKey("server", "claude"): &record}
	r := getReadiness(t, s, "", "")
	if cl := readinessStep(t, r, StepClaude); cl.State != StepOK || r.Mode != ReadinessFull {
		t.Fatalf("one miss: claude = %+v (%s)", cl, r.Mode)
	}
	record.Fails, record.MissingFails = 2, 2
	if cl := readinessStep(t, getReadiness(t, s, "", ""), StepClaude); cl.Code != proto.CodeClaudeMissing || cl.Action.ID != "install_claude" {
		t.Fatalf("two misses in a row: %+v", cl)
	}
}

// An earlier failure of another kind is not a miss (#7670 review): after an auth failure, the first "command not found:
// claude" of a self-update still defers to the self-check; a second miss in a row stands, and anything else in between
// starts the count again.
func TestReadinessClaudeHealthMissAfterAnotherFailure(t *testing.T) {
	f, s, _ := readyMachine(t)
	f.devices = []Device{{ID: "server", Name: "server", Online: true, Reachable: true, ClaudeVersion: "2.1.308"}}
	ctx := context.Background()
	fail := func(err string) LauncherHealth {
		return s.recordHealth(ctx, "server", "claude", healthResult{agent: "claude", err: err, source: healthSourceCheck})
	}
	claude := func() ReadinessStep { return readinessStep(t, getReadiness(t, s, "", ""), StepClaude) }
	fail("401 unauthorized")
	if h := fail("zsh:1: command not found: claude"); h.Fails != 2 || h.MissingFails != 1 {
		t.Fatalf("record = %+v", h)
	}
	if cl := claude(); cl.Code == proto.CodeClaudeMissing {
		t.Fatalf("auth failure, then one miss: %+v", cl)
	}
	fail("timed out waiting for the model")
	fail("zsh:1: command not found: claude")
	if cl := claude(); cl.Code == proto.CodeClaudeMissing {
		t.Fatalf("a timeout between two misses: %+v", cl)
	}
	fail("zsh:1: command not found: claude")
	if cl := claude(); cl.Code != proto.CodeClaudeMissing || cl.Action.ID != "install_claude" {
		t.Fatalf("two misses in a row: %+v", cl)
	}
}

// A node with no self-check (an old one) leaves Claude Code unknown, which does not stop the main conversation.
func TestReadinessOldNodeUnknown(t *testing.T) {
	f, s, _ := readyMachine(t)
	f.doctor = nil
	r := getReadiness(t, s, "", "")
	if cl := readinessStep(t, r, StepClaude); cl.State != StepUnknown || r.Mode != ReadinessFull {
		t.Fatalf("readiness = %+v", r)
	}
}

// A start refused because Claude Code is not there is the claude step's, until a start goes or a self-check made
// after it reads a version (installed since: in setup mode no start comes to clear it).
func TestReadinessStartRefusal(t *testing.T) {
	f, s, st := readyMachine(t)
	f.doctor["server"] = "读不到 claude 的版本"
	refuse := func() {
		t.Helper()
		data, _ := json.Marshal(map[string]string{"delivered": "none", "launcher": "claude"})
		f.mu.Lock()
		f.failAssistant = &rpc.Error{Code: proto.CodeClaudeMissing, Message: "zsh:1: command not found: claude", Data: data}
		f.mu.Unlock()
		if status, _ := serveJSON(t, s, "POST", "/api/v1/assistant/conversations", "", `{"prompt":"你好","main":true}`); status != 409 {
			t.Fatalf("start: %d", status)
		}
		f.mu.Lock()
		f.failAssistant = nil
		f.mu.Unlock()
	}
	refuse()
	if cl := readinessStep(t, getReadiness(t, s, "", ""), StepClaude); cl.Code != proto.CodeClaudeMissing || cl.Action.ID != "install_claude" {
		t.Fatalf("claude = %+v", cl)
	}
	// Claude Code installed: the next self-check reads a version and the refusal is forgotten.
	old := readinessClaudeRetry
	readinessClaudeRetry = 0
	t.Cleanup(func() { readinessClaudeRetry = old })
	f.mu.Lock()
	f.doctor["server"] = "2.1.308，支持 --plugin-dir"
	f.mu.Unlock()
	if cl := readinessStep(t, getReadiness(t, s, "", ""), StepClaude); cl.State != StepOK || cl.Version != "2.1.308" {
		t.Fatalf("after installing: %+v", cl)
	}
	var rec readinessRecord
	if _, err := st.Setting(context.Background(), readinessKey, &rec); err != nil || rec.Start != nil {
		t.Fatalf("record = %+v (%v)", rec, err)
	}
	// Refused again, and the self-check after it reads no version (the version read before does not count): the
	// refusal holds until a start goes.
	readinessClaudeRetry = time.Hour
	f.mu.Lock()
	f.doctor["server"] = "读不到 claude 的版本"
	f.mu.Unlock()
	refuse()
	if cl := readinessStep(t, getReadiness(t, s, "", ""), StepClaude); cl.Code != proto.CodeClaudeMissing {
		t.Fatalf("refused again: %+v", cl)
	}
	if status, body := serveJSON(t, s, "POST", "/api/v1/assistant/conversations", "", `{"prompt":"你好","main":true}`); status != 200 {
		t.Fatalf("start: %d %v", status, body)
	}
	if _, err := st.Setting(context.Background(), readinessKey, &rec); err != nil || rec.Start != nil {
		t.Fatalf("after a start: %+v (%v)", rec, err)
	}
}

// The main conversation's last turn was Claude Code's "Not logged in": the model step says sign in (or use a key),
// and so does the first message.
func TestReadinessNotLoggedIn(t *testing.T) {
	_, s, _ := readyMachine(t, proto.Session{SessionID: "m1", State: proto.StateIdle, LastText: "Not logged in · Please run /login",
		UpdatedAt: time.Now()})
	r := getReadiness(t, s, "", "")
	m, first := readinessStep(t, r, StepModel), readinessStep(t, r, StepFirstMessage)
	if r.Mode != ReadinessSetup || nextOf(r) != StepModel || m.State != StepProblem || m.Code != proto.TurnErrorNotLoggedIn ||
		m.Action.ID != "connect_model" || len(m.AltActions) != 1 || m.AltActions[0].ID != "login_claude" ||
		m.Title != "模型没接好：claude 没登录" || m.Detail != notLoggedInZh || m.Launcher != "claude" ||
		first.State != StepProblem || first.Code != proto.TurnErrorNotLoggedIn || first.Detail != notLoggedInZh {
		t.Fatalf("readiness = %+v", r)
	}
}

const (
	notLoggedInZh       = "主对话用的是 Claude Code 命令行，Claude 桌面版登录了也不算。在 server 的终端里运行 claude auth login，或者改用 API Key。"
	notLoggedInSwitchZh = "主对话用的是 Claude Code 命令行，Claude 桌面版登录了也不算。在 server 的终端里运行 claude auth login，改用 API Key，或在设置 › 模型中选择此电脑上运行主对话的其他应用或配置。"
	desktopAgentZh      = "想用桌面版的登录，新建 Agent 时选 claude-desktop，会话会在 Claude 桌面版里运行。"
)

// Claude Code's CLI signed out on a Mac whose Claude desktop app keeps sessions (#9366): the app's sign-in is not the
// CLI's, and its launcher runs no main chat, so it is no main chat to switch to; the words say an Agent started with
// it runs in the app. The Codex App's launcher runs none either. A launcher that does run the main chat is still the
// switch (#4355), with the desktop app's word after it.
func TestReadinessNotLoggedInDesktopLauncher(t *testing.T) {
	f, s, _ := readyMachine(t, proto.Session{SessionID: "m1", State: proto.StateIdle, LastText: "Not logged in · Please run /login",
		UpdatedAt: time.Now()})
	desktop := proto.Launcher{Name: "claude-desktop", Source: proto.SourceClaudeDesktop, Agent: "claude"}
	codexApp := proto.Launcher{Name: "codex-app", Source: "codex_app", Agent: "codex"}
	f.mu.Lock()
	f.zshrc = map[string][]proto.Launcher{"server": {desktop, codexApp}}
	f.mu.Unlock()
	m := readinessStep(t, getReadiness(t, s, "", ""), StepModel)
	if m.Code != proto.TurnErrorNotLoggedIn || len(m.AltActions) != 1 || m.AltActions[0].ID != "login_claude" ||
		m.Title != "模型没接好：claude 没登录" || m.Detail != notLoggedInZh+desktopAgentZh {
		t.Fatalf("desktop app only: %+v", m)
	}
	en := readinessStep(t, getReadiness(t, s, "", "en"), StepModel)
	if en.Title != "Model not connected: claude isn't signed in" || !strings.Contains(en.Detail, "the Claude desktop app's sign-in doesn't count") ||
		!strings.Contains(en.Detail, "Run claude auth login in a terminal on server") || !strings.Contains(en.Detail, "start an agent with claude-desktop") {
		t.Fatalf("en: %+v", en)
	}
	f.mu.Lock()
	f.zshrc = map[string][]proto.Launcher{"server": {desktop, codexApp, {Name: "cx", Agent: "codex"}}}
	f.mu.Unlock()
	s.invalidateLauncherListCache("server")
	m = readinessStep(t, getReadiness(t, s, "", ""), StepModel)
	if len(m.AltActions) != 2 || m.AltActions[0].ID != "login_claude" || m.AltActions[1].ID != "main_chat_model" ||
		m.Detail != notLoggedInSwitchZh+desktopAgentZh {
		t.Fatalf("with a Codex launcher too: %+v", m)
	}
	f.mu.Lock()
	f.zshrc = map[string][]proto.Launcher{"server": {{Name: "cx", Agent: "codex"}}}
	f.mu.Unlock()
	s.invalidateLauncherListCache("server")
	if m = readinessStep(t, getReadiness(t, s, "", ""), StepModel); len(m.AltActions) != 2 || m.Detail != notLoggedInSwitchZh {
		t.Fatalf("no desktop app: %+v", m)
	}
}

// The model step names the launcher that needs repair; first_message keeps its launcher-free wording because that
// step deliberately carries no launcher identity.
func TestReadinessModelErrorNamesLauncher(t *testing.T) {
	for _, tc := range []struct {
		code, text, title, firstTitle string
	}{
		{proto.TurnErrorNotLoggedIn, "Not logged in · Please run /login", "Model not connected: claude isn't signed in", "Claude Code CLI isn't signed in"},
		{proto.TurnErrorAuthFailed, "Invalid API key · Please run /login", "Model not connected: the key for claude is wrong", "The model's key doesn't work"},
		{proto.TurnErrorModelRoute, "There's an issue with the selected model (example-model). It may not exist or you may not have access to it.", "Model not connected: this model isn't available on claude", "This model isn't available"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			_, s, _ := readyMachine(t, proto.Session{SessionID: "m1", State: proto.StateIdle, LastText: tc.text, UpdatedAt: time.Now()})
			r := getReadiness(t, s, "", "en")
			m, first := readinessStep(t, r, StepModel), readinessStep(t, r, StepFirstMessage)
			if m.Code != tc.code || m.Launcher != "claude" || m.Title != tc.title || first.Title != tc.firstTitle || first.Launcher != "" {
				t.Fatalf("model = %+v; first message = %+v", m, first)
			}
		})
	}
}

// An answer in the main conversation: all ready, and the first message stays done after a later failure.
func TestReadinessAnswered(t *testing.T) {
	f, s, _ := readyMachine(t, proto.Session{SessionID: "m1", State: proto.StateIdle, LastText: "你好，我是 Persona。",
		UpdatedAt: time.Now()})
	r := getReadiness(t, s, "", "")
	if r.Mode != ReadinessFull || r.Next != nil || readinessStep(t, r, StepModel).State != StepOK || readinessStep(t, r, StepFirstMessage).State != StepOK {
		t.Fatalf("readiness = %+v", r)
	}
	f.setSessions("server", proto.Session{SessionID: "m1", State: proto.StateIdle, LastText: "API Error: 429 rate limit",
		UpdatedAt: time.Now().Add(time.Second)})
	r = getReadiness(t, s, "", "")
	m := readinessStep(t, r, StepModel)
	if m.State != StepProblem || m.Code != proto.TurnErrorRateLimited || r.Mode != ReadinessFull || // a rate limit passes by itself
		m.Action == nil || m.Action.ID != "connect_model" || len(m.AltActions) != 0 || readinessStep(t, r, StepFirstMessage).State != StepOK {
		t.Fatalf("after a rate limit: %+v", r)
	}
}

// Once first_message is complete, a later key or model failure still needs a way to verify a repair in this chat.
func TestReadinessRetryAfterAnswered(t *testing.T) {
	for _, tc := range []struct {
		code, text, action string
	}{
		{proto.TurnErrorAuthFailed, "Invalid API key · Please run /login", "edit_provider"},
		{proto.TurnErrorModelRoute, "There's an issue with the selected model (example-model). It may not exist or you may not have access to it.", "connect_model"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			f, s, st := readyMachine(t, proto.Session{SessionID: "m1", State: proto.StateIdle, LastText: "Hello.", UpdatedAt: time.Now().Add(-time.Minute)})
			if r := getReadiness(t, s, "", "en"); !r.Ready || readinessStep(t, r, StepFirstMessage).State != StepOK {
				t.Fatalf("initial answer: %+v", r)
			}
			f.setSessions("server", proto.Session{SessionID: "m1", State: proto.StateIdle, LastText: tc.text, UpdatedAt: time.Now().Add(-time.Second)})
			// Reload the persisted first answer, as when the assistant restarts before the repair.
			s = newServiceOn(t, f, st)
			assertBlocked := func(r Readiness) {
				t.Helper()
				m, first := readinessStep(t, r, StepModel), readinessStep(t, r, StepFirstMessage)
				if r.Ready || r.Mode != ReadinessSetup || nextOf(r) != StepModel || m.State != StepProblem || m.Code != tc.code ||
					m.Action == nil || m.Action.ID != tc.action || len(m.AltActions) != 1 || m.AltActions[0].ID != "test_assistant" ||
					first.State != StepOK || first.Action != nil {
					t.Fatalf("model repair must remain testable after the first answer: %+v", r)
				}
			}
			assertBlocked(getReadiness(t, s, "", "en"))
			f.mu.Lock()
			if len(f.checked) != 0 {
				t.Error("readiness spent tokens without an explicit probe")
			}
			f.checks = map[string]proto.LauncherCheck{"server/claude": {Error: tc.text}}
			f.mu.Unlock()
			failed := getReadiness(t, s, "probe=1", "en")
			assertBlocked(failed)
			if failed.Probe == nil || failed.Probe.OK || failed.Probe.Error != tc.text {
				t.Fatalf("failed probe: %+v", failed.Probe)
			}
			// Let the previous probe expire without a wall-clock sleep.
			if err := s.readinessUpdate(context.Background(), func(r *readinessRecord) { r.Probe.At = time.Now().Add(-time.Minute) }); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.checks = nil
			f.mu.Unlock()
			recovered := getReadiness(t, s, "probe=1", "en")
			if recovered.Probe == nil || !recovered.Probe.OK || !recovered.Ready || recovered.Mode != ReadinessFull || recovered.Next != nil ||
				readinessStep(t, recovered, StepModel).State != StepOK || readinessStep(t, recovered, StepFirstMessage).State != StepOK {
				t.Fatalf("successful retry: %+v", recovered)
			}
			if r := getReadiness(t, newServiceOn(t, f, st), "", "en"); !r.Ready || readinessStep(t, r, StepFirstMessage).State != StepOK {
				t.Fatalf("recovered record after restart: %+v", r)
			}
			if v := s.settings(context.Background()); v.Main != "m1" || len(v.Former) != 0 {
				t.Fatalf("retry replaced or archived the main conversation: %+v", v)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.checked) != 2 || f.checked[0] != "server/claude" || f.checked[1] != "server/claude" || f.made != 0 || len(f.sends) != 0 || len(f.retired) != 0 ||
				len(f.sessions["server"]) != 1 || f.sessions["server"][0].SessionID != "m1" || f.sessions["server"][0].LastText != tc.text {
				t.Fatalf("retry must only check the current launcher: checks=%v made=%d sends=%v retired=%v sessions=%+v", f.checked, f.made, f.sends, f.retired, f.sessions)
			}
		})
	}
}

// A successful check on a different machine or launcher cannot clear this main chat's failure.
func TestReadinessRetryProbeIdentity(t *testing.T) {
	for _, identity := range []struct{ machine, launcher string }{{"other", "claude"}, {"server", "other"}} {
		t.Run(identity.machine+"/"+identity.launcher, func(t *testing.T) {
			_, s, _ := readyMachine(t, proto.Session{SessionID: "m1", State: proto.StateIdle, LastText: "Invalid API key · Please run /login", UpdatedAt: time.Now().Add(-time.Minute)})
			if err := s.readinessUpdate(context.Background(), func(r *readinessRecord) {
				now := time.Now()
				r.AnsweredAt = &now
				r.Probe = &readinessProbe{Machine: identity.machine, Launcher: identity.launcher, At: now, Check: proto.LauncherCheck{OK: true}}
			}); err != nil {
				t.Fatal(err)
			}
			r := getReadiness(t, s, "", "en")
			if r.Ready || readinessStep(t, r, StepModel).Code != proto.TurnErrorAuthFailed || readinessStep(t, r, StepFirstMessage).State != StepOK {
				t.Fatalf("unrelated probe cleared the current failure: %+v", r)
			}
		})
	}
}

// ?probe=1 runs the one-word check once; asked again within 30 seconds it answers the same result. A key that does
// not work is the model step's problem, with editing the provider to do.
func TestReadinessProbe(t *testing.T) {
	f, s, _ := readyMachine(t)
	f.checks = map[string]proto.LauncherCheck{"server/claude": {Error: "Invalid API key · Please run /login", Millis: 900}}
	r := getReadiness(t, s, "probe=1", "")
	m := readinessStep(t, r, StepModel)
	if r.Probe == nil || r.Probe.OK || r.Probe.Error == "" || m.Code != proto.TurnErrorAuthFailed || m.Action.ID != "edit_provider" ||
		r.Mode != ReadinessSetup {
		t.Fatalf("readiness = %+v probe %+v", r, r.Probe)
	}
	if r2 := getReadiness(t, s, "probe=1", ""); r2.Probe == nil || r2.Probe.Millis != 900 || len(f.checked) != 1 {
		t.Fatalf("again: %+v, checked %v", r2.Probe, f.checked)
	}
	if r3 := getReadiness(t, s, "", ""); r3.Probe != nil || readinessStep(t, r3, StepModel).Code != proto.TurnErrorAuthFailed {
		t.Fatalf("without probe: %+v", r3)
	}
	// Past the reuse window it runs again; this time it answers.
	old := readinessProbeReuse
	readinessProbeReuse = 0
	t.Cleanup(func() { readinessProbeReuse = old })
	f.mu.Lock()
	f.checks = nil
	f.mu.Unlock()
	r4 := getReadiness(t, s, "probe=1", "")
	if r4.Probe == nil || !r4.Probe.OK || readinessStep(t, r4, StepModel).State != StepOK ||
		len(f.checked) != 2 || r4.Mode != ReadinessFull {
		t.Fatalf("again later: %+v, checked %v", r4, f.checked)
	}
	if first := readinessStep(t, r4, StepFirstMessage); first.State != StepOK || r4.Next != nil {
		t.Fatalf("probe ok should complete first_message: first = %+v, next = %v", first, r4.Next)
	}
	// Stays ok on later reads without probe and persists AnsweredAt.
	r5 := getReadiness(t, s, "", "")
	if first := readinessStep(t, r5, StepFirstMessage); first.State != StepOK || r5.Next != nil {
		t.Fatalf("persisted first_message: %+v", first)
	}
}

// A client that stops waiting for its probe (its own time ran out, the page closed) does not cut the check off: the
// result is kept, and the next try within the reuse window answers it instead of starting the check again (#1892).
func TestReadinessProbeOutlivesTheClient(t *testing.T) {
	f, s, _ := readyMachine(t)
	started, release := make(chan struct{}, 1), make(chan struct{})
	var asked atomic.Int32 // the checks the node was asked for, the one cut off too
	f.mu.Lock()
	f.checkWait = func(ctx context.Context) error {
		asked.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return ctx.Err() // a check cut off with the client fails, however the select woke
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = serveHTTP(ctx, s, assistantwire.HTTPRequest{Method: "GET", Path: "/api/v1/assistant/readiness", Query: "probe=1"}, s.lang)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the check never started")
	}
	cancel() // the client hangs up while the check runs
	close(release)
	<-done
	r := getReadiness(t, s, "probe=1", "")
	if r.Probe == nil || !r.Probe.OK || asked.Load() != 1 {
		t.Fatalf("the try after: %+v, checks asked for %d (want the first one's result, 1)", r.Probe, asked.Load())
	}
}

// "Later" is kept in the settings (assistant.readiness), with the last result.
func TestReadinessDismiss(t *testing.T) {
	f, s, st := readyMachine(t)
	if status, body := serveJSON(t, s, "POST", "/api/v1/assistant/readiness/dismiss", "", `{"dismissed":true}`); status != 200 || body["ok"] != true {
		t.Fatalf("dismiss: %d %v", status, body)
	}
	if r := getReadiness(t, s, "", ""); !r.Dismissed {
		t.Fatalf("readiness = %+v", r)
	}
	var rec readinessRecord
	if ok, err := st.Setting(context.Background(), readinessKey, &rec); !ok || err != nil || !rec.Dismissed || rec.Last == nil ||
		rec.Last.Mode != ReadinessFull {
		t.Fatalf("record = %+v (%v %v)", rec, ok, err)
	}
	// A new assistant on the same database reads it back.
	if r := getReadiness(t, newServiceOn(t, f, st), "", ""); !r.Dismissed {
		t.Fatalf("after a restart: %+v", r)
	}
	// The same result again is not written again.
	puts := st.putCount()
	getReadiness(t, s, "", "")
	if after := st.putCount(); after != puts {
		t.Fatalf("written again: %d → %d", puts, after)
	}
	if status, _ := serveJSON(t, s, "POST", "/api/v1/assistant/readiness/dismiss", "", `{"dismissed":false}`); status != 200 {
		t.Fatal(status)
	}
	if r := getReadiness(t, s, "", ""); r.Dismissed {
		t.Fatalf("undone: %+v", r)
	}
}

// A launcher of the user's own that the machine no longer has: launcher_missing; one that runs Codex: skipped.
func TestReadinessLauncher(t *testing.T) {
	f, s, _ := readyMachine(t)
	f.hello.Assistant.Launcher = "cc_gone"
	f.zshrc = map[string][]proto.Launcher{"server": {{Name: "cx", Agent: "codex"}}}
	r := getReadiness(t, s, "", "")
	if cl := readinessStep(t, r, StepClaude); r.Launcher != "cc_gone" || cl.Code != proto.CodeLauncherMissing || cl.Action.ID != "connect_model" ||
		cl.Detail != "在 server 上找不到「cc_gone」这个模型入口，可能被删了。" || r.Mode != ReadinessSetup {
		t.Fatalf("readiness = %+v", r)
	}
	f.mu.Lock()
	f.hello.Assistant.Launcher = "cx"
	f.mu.Unlock()
	if cl := readinessStep(t, getReadiness(t, s, "", ""), StepClaude); cl.State != StepSkipped {
		t.Fatalf("codex: %+v", cl)
	}
}

// A direct API provider added in Persona that speaks Messages starts Claude Code on its gateway (#2455): its launcher
// is there, not launcher_missing, right after the key is added.
func TestReadinessManagedAPILauncher(t *testing.T) {
	f, s, _ := readyMachine(t)
	key, err := s.sealKey("sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.putSetting(context.Background(), launchersManagedKey, []storedLauncher{{ManagedLauncher: proto.ManagedLauncher{Name: "direct",
		Kind: "api", API: "messages", BaseURL: "https://api.example.com", Model: "m"}, KeySealed: key}}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.hello.Assistant.Launcher = "direct"
	f.mu.Unlock()
	r := getReadiness(t, s, "", "")
	if cl := readinessStep(t, r, StepClaude); r.Launcher != "direct" || cl.State == StepProblem || r.Mode != ReadinessFull {
		t.Fatalf("readiness = %+v", r)
	}
}

// A record that cannot be read is not written over: what was kept ("Later") survives a database hiccup.
func TestReadinessUnreadRecordKept(t *testing.T) {
	_, s, st := readyMachine(t)
	if status, _ := serveJSON(t, s, "POST", "/api/v1/assistant/readiness/dismiss", "", `{}`); status != 200 {
		t.Fatal(status)
	}
	s.ready.mu.Lock()
	s.ready.loaded, s.ready.rec = false, readinessRecord{}
	s.ready.mu.Unlock()
	st.mu.Lock()
	st.failSetting = errors.New("database hiccup")
	st.mu.Unlock()
	getReadiness(t, s, "", "")
	if status, _ := serveJSON(t, s, "POST", "/api/v1/assistant/readiness/dismiss", "", `{"dismissed":false}`); status != 500 {
		t.Fatalf("dismiss over an unread record: %d", status)
	}
	st.mu.Lock()
	st.failSetting = nil
	st.mu.Unlock()
	var rec readinessRecord
	if _, err := st.Setting(context.Background(), readinessKey, &rec); err != nil || !rec.Dismissed {
		t.Fatalf("record = %+v (%v)", rec, err)
	}
}

// Claude Code signed out as the launcher runs it (claude.auth_status, free): the model step says sign in before any
// message is sent. The answer is kept; once the user signs in, the next question after the short retry says so.
func TestReadinessSignedOutStatus(t *testing.T) {
	f, s, _ := readyMachine(t)
	f.auth = map[string]proto.ClaudeAuthStatus{"server/claude": {State: proto.ClaudeNotLoggedIn, AuthMethod: "none"}}
	r := getReadiness(t, s, "", "")
	m := readinessStep(t, r, StepModel)
	if r.Mode != ReadinessSetup || nextOf(r) != StepModel || m.State != StepProblem || m.Code != proto.TurnErrorNotLoggedIn ||
		m.Action.ID != "connect_model" || len(m.AltActions) != 1 || m.AltActions[0].ID != "login_claude" || m.Title != "模型没接好：claude 没登录" {
		t.Fatalf("readiness = %+v", r)
	}
	getReadiness(t, s, "", "")
	f.mu.Lock()
	asked := append([]string(nil), f.authAsked...)
	f.mu.Unlock()
	if len(asked) != 1 || asked[0] != "server/claude" {
		t.Fatalf("asked %v", asked)
	}
	// Signed in to claude.ai since: that is the model's evidence, and the main conversation can go.
	old := readinessClaudeRetry
	readinessClaudeRetry = 0
	t.Cleanup(func() { readinessClaudeRetry = old })
	f.mu.Lock()
	f.auth["server/claude"] = proto.ClaudeAuthStatus{State: proto.ClaudeLoggedIn, AuthMethod: "claude.ai"}
	f.mu.Unlock()
	r = getReadiness(t, s, "", "")
	if m := readinessStep(t, r, StepModel); r.Mode != ReadinessFull || m.State != StepOK {
		t.Fatalf("after signing in: %+v", r)
	}
	if len(f.checked) != 0 {
		t.Fatalf("spent tokens: %v", f.checked)
	}
}

// Signed in with a key (or a gateway's token) says nothing of whether the key works: the model stays unverified, and
// says what it runs on.
func TestReadinessKeyStatusUnverified(t *testing.T) {
	f, s, _ := readyMachine(t)
	f.auth = map[string]proto.ClaudeAuthStatus{"server/claude": {State: proto.ClaudeLoggedIn, AuthMethod: "api_key", KeySource: "ANTHROPIC_API_KEY"}}
	r := getReadiness(t, s, "", "")
	m := readinessStep(t, r, StepModel)
	if r.Mode != ReadinessFull || m.State != StepUnknown || m.Code != "model_unverified" ||
		m.Detail != "server 上的 Claude Code 用的是配好的账号或 Key，还没试过。发一句试试它能不能用。" {
		t.Fatalf("readiness = %+v", r)
	}
	// One button for the try, on the first message's step, where clients show how it went (#6317).
	if first := readinessStep(t, r, StepFirstMessage); m.Action != nil || first.State != StepTodo || first.Action == nil || first.Action.ID != "test_assistant" {
		t.Fatalf("the try's button: model %+v, first message %+v", m.Action, first)
	}
	// A main conversation replaced since counts as answered: the model step keeps the try then.
	f3 := newFakeHub().withMachine()
	f3.doctor = map[string]string{"server": "2.1.308，支持 --plugin-dir"}
	f3.settings = &Settings{Main: "m1", Former: []FormerMain{{MachineID: "server", SessionID: "m0"}}}
	f3.auth = f.auth
	r3 := getReadiness(t, newServiceOn(t, f3, &memStore{}), "", "")
	if m3, first3 := readinessStep(t, r3, StepModel), readinessStep(t, r3, StepFirstMessage); first3.State != StepOK || m3.Action == nil || m3.Action.ID != "test_assistant" {
		t.Fatalf("first message ok: model %+v, first message %+v", m3, first3)
	}
	// An unknown status (an old Claude Code, a launcher that drops the arguments) is no evidence either way.
	f2, s2, _ := readyMachine(t)
	f2.auth = map[string]proto.ClaudeAuthStatus{"server/claude": {State: proto.ClaudeAuthUnknown, Reason: proto.ClaudeAuthOld}}
	if m := readinessStep(t, getReadiness(t, s2, "", ""), StepModel); m.State != StepUnknown || m.Detail != "发一句试试，确认模型能回答。" {
		t.Fatalf("unknown status: %+v", m)
	}
}

// A problem's occurrence identifies this failure across reads, and advances for a later failure with the same words.
func TestReadinessFailureOccurrence(t *testing.T) {
	firstAt := time.Now().Add(-time.Hour).UTC()
	f, s, _ := readyMachine(t, proto.Session{SessionID: "m1", State: proto.StateIdle, TurnError: "API Error: 429 Too Many Requests", UpdatedAt: firstAt})
	occurrence := func(r Readiness) string {
		t.Helper()
		raw, err := json.Marshal(readinessStep(t, r, StepModel))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		got, _ := fields["occurrence"].(string)
		return got
	}
	first := getReadiness(t, s, "", "")
	if got, want := occurrence(first), firstAt.Format(time.RFC3339Nano); got != want {
		t.Fatalf("first occurrence = %q, want %q", got, want)
	}
	if got := occurrence(getReadiness(t, s, "", "")); got != firstAt.Format(time.RFC3339Nano) {
		t.Fatalf("an ordinary read changed the failure occurrence: %q", got)
	}
	f.setSessions("server", proto.Session{SessionID: "m1", State: proto.StateIdle, TurnError: "API Error: 429 Too Many Requests", UpdatedAt: firstAt.Add(time.Second)})
	if got, want := occurrence(getReadiness(t, s, "", "")), firstAt.Add(time.Second).Format(time.RFC3339Nano); got != want {
		t.Fatalf("a later failure kept the old occurrence: %q, want %q", got, want)
	}

	// Rechecking one subscription window advances checked_at, not the occurrence. A new reset window is a new failure.
	reset := time.Now().Add(time.Hour).UTC()
	_, healthService, _ := readyMachine(t)
	record := LauncherHealth{Machine: "server", Launcher: "claude", State: healthFailing, Kind: errKindUsageLimit,
		Error: "API Error: 429", CheckedAt: time.Now().Add(-time.Minute), ResetsAt: &reset}
	healthService.health.loaded = true
	healthService.health.recs = map[string]*LauncherHealth{healthKey("server", "claude"): &record}
	if got, want := occurrence(getReadiness(t, healthService, "", "")), reset.Format(time.RFC3339Nano); got != want {
		t.Fatalf("health occurrence = %q, want reset window %q", got, want)
	}
	record.CheckedAt = time.Now()
	if got := occurrence(getReadiness(t, healthService, "", "")); got != reset.Format(time.RFC3339Nano) {
		t.Fatalf("a repeated health check revived the same reset window: %q", got)
	}
	laterReset := reset.Add(time.Hour)
	record.ResetsAt = &laterReset
	if got := occurrence(getReadiness(t, healthService, "", "")); got != laterReset.Format(time.RFC3339Nano) {
		t.Fatalf("a new reset window kept the old occurrence: %q", got)
	}

	// The settled health record comes from the same main turn. Its reset must not give that turn a second occurrence.
	turnReset := time.Now().Add(2 * time.Hour).UTC()
	turn := proto.Session{SessionID: "m1", State: proto.StateIdle, TurnError: limitText, TurnErrorKind: proto.TurnErrorUsageLimit,
		TurnErrorResetsAt: &turnReset, UpdatedAt: firstAt}
	turnHub, turnService, _ := readyMachine(t, turn)
	beforeHealth := occurrence(getReadiness(t, turnService, "", ""))
	settled := LauncherHealth{Machine: "server", Launcher: "claude", State: healthFailing, Kind: errKindUsageLimit,
		Error: limitText, CheckedAt: firstAt.Add(time.Minute), ResetsAt: &turnReset}
	turnService.health.loaded = true
	turnService.health.recs = map[string]*LauncherHealth{healthKey("server", "claude"): &settled}
	if got := occurrence(getReadiness(t, turnService, "", "")); got != beforeHealth {
		t.Fatalf("settling the same turn changed its occurrence from %q to %q", beforeHealth, got)
	}
	if beforeHealth != firstAt.Format(time.RFC3339Nano) {
		t.Fatalf("main turn occurrence = %q, want turn time %q", beforeHealth, firstAt.Format(time.RFC3339Nano))
	}
	laterTurnReset := turnReset.Add(time.Hour)
	turn.TurnErrorResetsAt, turn.UpdatedAt = &laterTurnReset, firstAt.Add(time.Minute)
	turnHub.setSessions("server", turn)
	turnService.health.recs = nil
	if got, want := occurrence(getReadiness(t, turnService, "", "")), firstAt.Add(time.Minute).Format(time.RFC3339Nano); got != want {
		t.Fatalf("a later turn occurrence = %q, want %q", got, want)
	}
	previousTurn := occurrence(getReadiness(t, turnService, "", ""))
	turn.UpdatedAt = firstAt.Add(2 * time.Minute) // a genuine second turn within the same reset window
	turnHub.setSessions("server", turn)
	if got := occurrence(getReadiness(t, turnService, "", "")); got == previousTurn {
		t.Fatalf("a later turn in the same reset window kept the dismissed occurrence %q", got)
	}
}

// The newest evidence wins: an answer in the main conversation after a signed-out status (signed in since, within
// the cache) is the model working.
func TestReadinessAnswerAfterSignedOutStatus(t *testing.T) {
	f, s, _ := readyMachine(t)
	f.auth = map[string]proto.ClaudeAuthStatus{"server/claude": {State: proto.ClaudeNotLoggedIn}}
	if m := readinessStep(t, getReadiness(t, s, "", ""), StepModel); m.Code != proto.TurnErrorNotLoggedIn {
		t.Fatalf("model = %+v", m)
	}
	f.setSessions("server", proto.Session{SessionID: "m1", State: proto.StateIdle, LastText: "你好。", UpdatedAt: time.Now().Add(time.Second)})
	if r := getReadiness(t, s, "", ""); readinessStep(t, r, StepModel).State != StepOK || r.Mode != ReadinessFull {
		t.Fatalf("after an answer: %+v", r)
	}
}

// Signed in to claude.ai only disproves signed out: a failure a turn met (a model the plan lacks, a rate limit) stays,
// even when the sign-in status was asked after it.
func TestReadinessSignedInKeepsTurnFailure(t *testing.T) {
	for _, c := range []struct{ text, code string }{
		{"There's an issue with the selected model (claude-x). It may not exist or you may not have access to it.", proto.TurnErrorModelRoute},
		{"API Error: 429 rate limit", proto.TurnErrorRateLimited},
	} {
		f, s, _ := readyMachine(t, proto.Session{SessionID: "m1", State: proto.StateIdle, TurnError: c.text,
			UpdatedAt: time.Now().Add(-time.Minute)})
		f.auth = map[string]proto.ClaudeAuthStatus{"server/claude": {State: proto.ClaudeLoggedIn, AuthMethod: "claude.ai"}}
		r := getReadiness(t, s, "", "")
		if m := readinessStep(t, r, StepModel); m.State != StepProblem || m.Code != c.code {
			t.Fatalf("%s: model = %+v", c.code, m)
		}
	}
	// With nothing else known it is the model's evidence.
	f, s, _ := readyMachine(t)
	f.auth = map[string]proto.ClaudeAuthStatus{"server/claude": {State: proto.ClaudeLoggedIn, AuthMethod: "claude.ai"}}
	if m := readinessStep(t, getReadiness(t, s, "", ""), StepModel); m.State != StepOK {
		t.Fatalf("nothing else: %+v", m)
	}
}

// A status the node could not read in time is asked again after the short retry; an old node's unknown method waits
// the long one.
func TestReadinessAuthStatusRetry(t *testing.T) {
	old := readinessClaudeRetry
	readinessClaudeRetry = 0
	t.Cleanup(func() { readinessClaudeRetry = old })
	f, s, _ := readyMachine(t)
	f.auth = map[string]proto.ClaudeAuthStatus{"server/claude": {State: proto.ClaudeAuthUnknown, Reason: proto.ClaudeAuthTimeout}}
	getReadiness(t, s, "", "")
	getReadiness(t, s, "", "")
	authAsked := func(f *fakeHub) int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.authAsked)
	}
	// The second ask is not waited for (TestReadinessUnknownStatusNotWaitedFor): it lands a moment later. Prefetch and
	// the two readiness reads can legitimately start a third first, so require the retry rather than an exact total.
	waitFor(t, "the timeout asked again", func() bool { return authAsked(f) >= 2 })
	f2, s2, _ := readyMachine(t) // f2.auth nil: unknown method
	getReadiness(t, s2, "", "")
	getReadiness(t, s2, "", "")
	asked := authAsked(f2)
	if asked != 1 {
		t.Fatalf("old node asked %d times", asked)
	}
}

// An expired status that said nothing (a launcher whose `claude auth status` prints no status, a timeout) is asked
// again without the readiness waiting for it: such a launcher says nothing on every ask, and each ask runs the login
// shell and Claude Code for seconds. What the new ask says is there for the next readiness.
func TestReadinessUnknownStatusNotWaitedFor(t *testing.T) {
	old := readinessClaudeRetry
	readinessClaudeRetry = 0
	t.Cleanup(func() { readinessClaudeRetry = old })
	f, s, _ := readyMachine(t)
	f.auth = map[string]proto.ClaudeAuthStatus{"server/claude": {State: proto.ClaudeAuthUnknown, Reason: proto.ClaudeAuthUnreadable}}
	if m := readinessStep(t, getReadiness(t, s, "", ""), StepModel); m.State != StepUnknown {
		t.Fatalf("first: model = %+v", m)
	}
	hold := make(chan struct{})
	// With no retry delay, the first readiness's prefetch and its waited read can each finish an ask. Let both finish
	// before holding the next one, so the assertion counts this readiness's new ask rather than assuming a global total.
	waitFor(t, "the first status asks to finish", func() bool {
		s.ready.mu.Lock()
		defer s.ready.mu.Unlock()
		return len(s.ready.authIn) == 0
	})
	f.mu.Lock()
	asksBefore := len(f.authAsked)
	f.authHold = hold
	f.auth["server/claude"] = proto.ClaudeAuthStatus{State: proto.ClaudeLoggedIn, AuthMethod: "claude.ai"}
	f.mu.Unlock()
	released := false
	release := func() {
		if !released {
			released = true
			close(hold)
		}
	}
	t.Cleanup(release)
	start := time.Now()
	r := getReadiness(t, s, "", "")
	if took := time.Since(start); took > time.Second {
		t.Fatalf("readiness waited %v for the node's sign-in status", took)
	}
	if m := readinessStep(t, r, StepModel); m.State != StepUnknown {
		t.Fatalf("while asked again: model = %+v", m)
	}
	waitFor(t, "the second ask on its way", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.authAsked) == asksBefore+1
	})
	release()
	readinessClaudeRetry = time.Hour // the new answer is the one kept
	waitFor(t, "the new status", func() bool {
		return readinessStep(t, getReadiness(t, s, "", ""), StepModel).State == StepOK
	})
}

func authCalls(f *fakeHub) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.authParams...)
}

func lastAuthCall(t *testing.T, f *fakeHub) map[string]any {
	t.Helper()
	calls := authCalls(f)
	if len(calls) == 0 {
		t.Fatal("no claude.auth_status call")
	}
	return calls[len(calls)-1]
}

// profileAuth simulates a node whose private assistant profile and legacy/machine-default profile have independent
// sign-in states: plain calls (without purpose "assistant") and explicit legacy calls inspect legacySignedIn, while
// default assistant calls inspect privateSignedIn.
func profileAuth(privateSignedIn, legacySignedIn bool) func(string, map[string]any) proto.ClaudeAuthStatus {
	return func(_ string, params map[string]any) proto.ClaudeAuthStatus {
		signedIn := legacySignedIn
		if params["purpose"] == "assistant" && params["config_dir"] != assistantConfigDirLegacy {
			signedIn = privateSignedIn
		}
		if signedIn {
			return proto.ClaudeAuthStatus{State: proto.ClaudeLoggedIn, AuthMethod: "claude.ai"}
		}
		return proto.ClaudeAuthStatus{State: proto.ClaudeNotLoggedIn, AuthMethod: "none"}
	}
}

// Readiness passes assistant profile context to claude.auth_status using the same wire fields launchers.check sends:
// purpose "assistant", optional config_dir (omitted for Persona's default private profile), and optional conversation.
func TestReadinessAuthStatusPassesAssistantContext(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mainID    string
		configDir string
	}{
		{name: "default-private-with-main", mainID: "m1"},
		{name: "default-private-without-main"},
		{name: "legacy-with-main", mainID: "m1", configDir: assistantConfigDirLegacy},
		{name: "legacy-without-main", configDir: assistantConfigDirLegacy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, _ := readyMachine(t)
			setSettings(t, s, func(v *Settings) { v.Main = tc.mainID })
			if tc.configDir != "" {
				setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", ConfigDir: tc.configDir})
			}
			f.mu.Lock()
			f.authBy = func(_ string, _ map[string]any) proto.ClaudeAuthStatus {
				return proto.ClaudeAuthStatus{State: proto.ClaudeLoggedIn, AuthMethod: "claude.ai"}
			}
			f.mu.Unlock()

			getReadiness(t, s, "", "en")
			p := lastAuthCall(t, f)
			if p["name"] != "claude" || p["purpose"] != "assistant" {
				t.Fatalf("claude.auth_status params = %v, want name=claude purpose=assistant", p)
			}
			if tc.mainID == "" {
				if v, ok := p["conversation"]; ok {
					t.Fatalf("claude.auth_status sent unexpected conversation=%v (%v)", v, p)
				}
			} else if p["conversation"] != tc.mainID {
				t.Fatalf("claude.auth_status conversation = %v, want %q (%v)", p["conversation"], tc.mainID, p)
			}
			if tc.configDir == "" {
				if v, ok := p["config_dir"]; ok {
					t.Fatalf("claude.auth_status sent unexpected config_dir=%v for default private profile (%v)", v, p)
				}
			} else if p["config_dir"] != tc.configDir {
				t.Fatalf("claude.auth_status config_dir = %v, want %q (%v)", p["config_dir"], tc.configDir, p)
			}
		})
	}
}

// Readiness reflects the sign-in state of the profile the main chat actually runs in: a signed-out machine-default
// ~/.claude must not force setup mode when Persona's private profile is signed in, and a signed-in ~/.claude must not
// mask a signed-out private profile.
func TestReadinessAuthStatusUsesAssistantProfile(t *testing.T) {
	for _, tc := range []struct {
		name            string
		configDir       string
		privateSignedIn bool
		legacySignedIn  bool
		wantMode        string
		wantModelState  string
		wantModelCode   string
	}{
		{
			name:            "private-signed-in-default-signed-out",
			privateSignedIn: true,
			legacySignedIn:  false,
			wantMode:        ReadinessFull,
			wantModelState:  StepOK,
		},
		{
			name:            "private-signed-out-default-signed-in",
			privateSignedIn: false,
			legacySignedIn:  true,
			wantMode:        ReadinessSetup,
			wantModelState:  StepProblem,
			wantModelCode:   proto.TurnErrorNotLoggedIn,
		},
		{
			name:            "legacy-signed-in-private-signed-out",
			configDir:       assistantConfigDirLegacy,
			privateSignedIn: false,
			legacySignedIn:  true,
			wantMode:        ReadinessFull,
			wantModelState:  StepOK,
		},
		{
			name:            "legacy-signed-out-private-signed-in",
			configDir:       assistantConfigDirLegacy,
			privateSignedIn: true,
			legacySignedIn:  false,
			wantMode:        ReadinessSetup,
			wantModelState:  StepProblem,
			wantModelCode:   proto.TurnErrorNotLoggedIn,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, _ := readyMachine(t)
			if tc.configDir != "" {
				setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", ConfigDir: tc.configDir})
			}
			f.mu.Lock()
			f.authBy = profileAuth(tc.privateSignedIn, tc.legacySignedIn)
			f.mu.Unlock()

			r := getReadiness(t, s, "", "en")
			m := readinessStep(t, r, StepModel)
			if r.Mode != tc.wantMode || r.Ready != (tc.wantMode == ReadinessFull) ||
				m.State != tc.wantModelState || m.Code != tc.wantModelCode {
				t.Fatalf("readiness mode=%s ready=%v model=%+v, want mode=%s state=%s code=%q (auth params=%v)",
					r.Mode, r.Ready, m, tc.wantMode, tc.wantModelState, tc.wantModelCode, authCalls(f))
			}
		})
	}
}

// Switching Main chat's config_dir between private and legacy, or changing the main conversation, must not serve a
// stale cached claude.auth_status from the previous profile within the 10-minute cache TTL.
func TestReadinessAuthCacheSeparatesProfilesAndConversations(t *testing.T) {
	t.Run("switch-private-to-legacy", func(t *testing.T) {
		f, s, _ := readyMachine(t)
		f.mu.Lock()
		f.authBy = profileAuth(true, false)
		f.mu.Unlock()

		first := getReadiness(t, s, "", "en")
		if first.Mode != ReadinessFull || readinessStep(t, first, StepModel).State != StepOK {
			t.Fatalf("initial private readiness = %+v (auth params=%v)", first, authCalls(f))
		}
		callsAfterFirst := len(authCalls(f))

		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", ConfigDir: assistantConfigDirLegacy})
		second := getReadiness(t, s, "", "en")
		m := readinessStep(t, second, StepModel)
		if second.Mode != ReadinessSetup || m.State != StepProblem || m.Code != proto.TurnErrorNotLoggedIn {
			t.Fatalf("after switching to legacy: mode=%s model=%+v, want setup/not_logged_in (auth params=%v)",
				second.Mode, m, authCalls(f))
		}
		if len(authCalls(f)) <= callsAfterFirst {
			t.Fatalf("switching config_dir reused stale auth cache: calls=%v", authCalls(f))
		}
		if p := lastAuthCall(t, f); p["config_dir"] != assistantConfigDirLegacy {
			t.Fatalf("after switching to legacy: last auth params = %v", p)
		}
	})

	t.Run("switch-legacy-to-private", func(t *testing.T) {
		f, s, _ := readyMachine(t)
		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", ConfigDir: assistantConfigDirLegacy})
		f.mu.Lock()
		f.authBy = profileAuth(false, true)
		f.mu.Unlock()

		first := getReadiness(t, s, "", "en")
		if first.Mode != ReadinessFull || readinessStep(t, first, StepModel).State != StepOK {
			t.Fatalf("initial legacy readiness = %+v (auth params=%v)", first, authCalls(f))
		}
		callsAfterFirst := len(authCalls(f))

		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude"})
		second := getReadiness(t, s, "", "en")
		m := readinessStep(t, second, StepModel)
		if second.Mode != ReadinessSetup || m.State != StepProblem || m.Code != proto.TurnErrorNotLoggedIn {
			t.Fatalf("after switching to private: mode=%s model=%+v, want setup/not_logged_in (auth params=%v)",
				second.Mode, m, authCalls(f))
		}
		if len(authCalls(f)) <= callsAfterFirst {
			t.Fatalf("switching config_dir reused stale auth cache: calls=%v", authCalls(f))
		}
		if p := lastAuthCall(t, f); p["config_dir"] != nil {
			t.Fatalf("after switching to private: last auth params = %v", p)
		}
	})

	t.Run("main-conversation-change", func(t *testing.T) {
		f, s, _ := readyMachine(t)
		f.mu.Lock()
		f.authBy = func(_ string, params map[string]any) proto.ClaudeAuthStatus {
			if params["purpose"] == "assistant" && params["conversation"] == "m1" {
				return proto.ClaudeAuthStatus{State: proto.ClaudeLoggedIn, AuthMethod: "claude.ai"}
			}
			return proto.ClaudeAuthStatus{State: proto.ClaudeNotLoggedIn, AuthMethod: "none"}
		}
		f.mu.Unlock()

		first := getReadiness(t, s, "", "en")
		if first.Mode != ReadinessFull || readinessStep(t, first, StepModel).State != StepOK {
			t.Fatalf("initial m1 readiness = %+v (auth params=%v)", first, authCalls(f))
		}
		callsAfterFirst := len(authCalls(f))

		setSettings(t, s, func(v *Settings) { v.Main = "m2" })
		second := getReadiness(t, s, "", "en")
		m := readinessStep(t, second, StepModel)
		if second.Mode != ReadinessSetup || m.State != StepProblem || m.Code != proto.TurnErrorNotLoggedIn {
			t.Fatalf("after changing main conversation: mode=%s model=%+v, want setup/not_logged_in (auth params=%v)",
				second.Mode, m, authCalls(f))
		}
		if len(authCalls(f)) <= callsAfterFirst {
			t.Fatalf("changing main conversation reused stale auth cache: calls=%v", authCalls(f))
		}
		if p := lastAuthCall(t, f); p["conversation"] != "m2" {
			t.Fatalf("after changing main conversation: last auth params = %v", p)
		}
	})
}

// Older nodes that ignore extra claude.auth_status fields or lack claude.auth_status altogether remain compatible.
func TestReadinessAuthStatusOlderNodeCompat(t *testing.T) {
	t.Run("ignores-extra-params", func(t *testing.T) {
		f, s, _ := readyMachine(t)
		f.auth = map[string]proto.ClaudeAuthStatus{"server/claude": {State: proto.ClaudeLoggedIn, AuthMethod: "claude.ai"}}
		r := getReadiness(t, s, "", "en")
		if r.Mode != ReadinessFull || readinessStep(t, r, StepModel).State != StepOK {
			t.Fatalf("older node logged_in: %+v", r)
		}

		f2, s2, _ := readyMachine(t)
		f2.auth = map[string]proto.ClaudeAuthStatus{"server/claude": {State: proto.ClaudeNotLoggedIn, AuthMethod: "none"}}
		r2 := getReadiness(t, s2, "", "en")
		if m := readinessStep(t, r2, StepModel); r2.Mode != ReadinessSetup || m.Code != proto.TurnErrorNotLoggedIn {
			t.Fatalf("older node not_logged_in: %+v", r2)
		}
	})

	t.Run("unknown-method", func(t *testing.T) {
		f, s, _ := readyMachine(t)
		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", ConfigDir: assistantConfigDirLegacy})
		r := getReadiness(t, s, "", "en")
		if m := readinessStep(t, r, StepModel); r.Mode != ReadinessFull || m.State != StepUnknown {
			t.Fatalf("older node without claude.auth_status: %+v", r)
		}
		getReadiness(t, s, "", "en")
		if calls := authCalls(f); len(calls) != 1 {
			t.Fatalf("older node without claude.auth_status asked %d times, want 1", len(calls))
		}
	})
}
