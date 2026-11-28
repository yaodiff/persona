package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fakeCodexAppSeed stands in for the App's codex starting a chat: it records what it was asked and writes the chat's
// rollout (as thread/name/set has the app-server do) under the App's home, with the originator Persona's chats carry.
type fakeCodexAppSeed struct {
	mu    sync.Mutex
	seeds []codexAppSeed
	fail  error
}

type codexAppSeed struct{ cli, home, dir, title, thread string }

func (f *fakeCodexAppSeed) seeded() []codexAppSeed {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]codexAppSeed(nil), f.seeds...)
}

func writeCodexAppRollout(t *testing.T, home, thread, originator, cwd string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "11", "12")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-2026-11-12T09-00-00-"+thread+".jsonl")
	meta := fmt.Sprintf(`{"timestamp":"2026-11-12T09:00:00Z","type":"session_meta","payload":{"id":%q,"originator":%q,"source":"vscode","cwd":%q}}`, thread, originator, cwd)
	if err := os.WriteFile(p, []byte(meta+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// codexAppNode is a node whose Codex home has the App's bus (one window owning nothing yet: the App opens a chat at
// codex://threads/<id> and owns it from then on), with the App's codex and its chat start faked.
func codexAppNode(t *testing.T) (*Node, *fakeCodexBus, *fakeCodexAppSeed, *[]string) {
	t.Helper()
	busHome, bus := startFakeCodexBus(t, "")
	n, _ := testNode(t)
	n.dir, n.home = t.TempDir(), filepath.Dir(busHome)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	shellLaunchers(t)
	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := &fakeCodexAppSeed{}
	opened := &[]string{}
	oldCLI, oldSeed, oldOpen := codexAppCLI, seedCodexAppChat, openCodexThread
	codexAppCLI = func() (string, error) { return cli, nil }
	seedCodexAppChat = func(_ context.Context, cli, home, dir, title string) (string, error) {
		seed.mu.Lock()
		defer seed.mu.Unlock()
		if seed.fail != nil {
			return "", seed.fail
		}
		thread := newUUID()
		seed.seeds = append(seed.seeds, codexAppSeed{cli, home, dir, title, thread})
		writeCodexAppRollout(t, home, thread, codexAppClient, dir)
		bus.later = thread
		return thread, nil
	}
	var mu sync.Mutex
	openCodexThread = func(thread string) error {
		mu.Lock()
		*opened = append(*opened, thread)
		mu.Unlock()
		bus.opened.Store(true)
		return nil
	}
	t.Cleanup(func() { codexAppCLI, seedCodexAppChat, openCodexThread = oldCLI, oldSeed, oldOpen })
	return n, bus, seed, opened
}

func startCodexApp(t *testing.T, n *Node, params map[string]any) (proto.Session, error) {
	t.Helper()
	b, _ := json.Marshal(params)
	res, err := n.handle(context.Background(), "session.start", b)
	if err != nil {
		return proto.Session{}, err
	}
	out, _ := json.Marshal(res)
	var r struct {
		Session proto.Session `json:"session"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	return r.Session, nil
}

// New Agent with the Codex App's launcher starts the chat with the App's codex (no turn), then the App opens it and
// runs its first message: the chat is the App's, in its sidebar, and Persona lists it as one of the App's chats.
func TestNewAgentWithTheCodexAppLauncherStartsTheChatInTheApp(t *testing.T) {
	n, bus, seed, opened := codexAppNode(t)
	res, err := n.handle(context.Background(), "launchers.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); !strings.Contains(string(b), `"name":"codex-app","base_url":"","model":"","source":"codex_app","agent":"codex"`) {
		t.Fatalf("launchers: %s", b)
	}
	dir := t.TempDir()
	s, err := startCodexApp(t, n, map[string]any{"cwd": dir, "launcher": "codex-app", "prompt": "Fix the CSV export\nand test it"})
	if err != nil {
		t.Fatal(err)
	}
	seeds := seed.seeded()
	if len(seeds) != 1 || seeds[0].dir != dir || seeds[0].title != "Fix the CSV export" || seeds[0].home != n.codexHome() {
		t.Fatalf("seeded %+v", seeds)
	}
	thread := seeds[0].thread
	if s.SessionID != thread || s.Cwd != dir || s.Agent != "codex" || s.Title != "Fix the CSV export" {
		t.Fatalf("session %+v", s)
	}
	if len(*opened) != 1 || (*opened)[0] != thread {
		t.Fatalf("opened %v", *opened)
	}
	var p map[string]any
	select {
	case p = <-bus.turns:
	case <-time.After(10 * time.Second):
		t.Fatal("the App ran no turn")
	}
	req := p["turnStart"].(map[string]any)["request"].(map[string]any)
	if in := req["input"].([]any)[0].(map[string]any); req["threadId"] != thread || in["text"] != "Fix the CSV export\nand test it" {
		t.Fatalf("turn %v", req)
	}
	if !codexDesktopChat(codexSource{home: n.codexHome(), thread: thread, path: rolloutPath(n.codexHome(), thread)}) {
		t.Fatal("not listed as one of the App's chats")
	}
}

func TestCodexAppSettingsChangeSupersedesPendingSetup(t *testing.T) {
	for _, tc := range []struct {
		name, model, effort, wantModel, wantEffort string
		refused                                    bool
	}{
		{name: "model only", model: "gpt-y", wantModel: "gpt-y", wantEffort: "high"},
		{name: "effort only", effort: "low", wantModel: "gpt-x", wantEffort: "low"},
		{name: "both", model: "gpt-y", effort: "low", wantModel: "gpt-y", wantEffort: "low"},
		{name: "new model reconciles effort", model: "gpt-low", wantModel: "gpt-low", wantEffort: "low"},
		{name: "new model has no effort", model: "gpt-plain", wantModel: "gpt-plain"},
		{name: "incompatible explicit effort", model: "gpt-plain", effort: "high", refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, bus, _, _ := codexAppNode(t)
			n.codices = newCodexRegistry()
			n.configureCodex(proto.CodexConfig{LegacyHome: n.codexHome()})
			withCodexAppModels(t, n.codexHome(), map[string]codexModelEfforts{
				"gpt-x": {levels: []string{"low", "high"}}, "gpt-y": {levels: []string{"low", "high"}},
				"gpt-low": {levels: []string{"low"}, def: "low"}, "gpt-plain": {},
			})
			thread := newUUID()
			path := writeCodexAppRollout(t, n.codexHome(), thread, codexAppClient, t.TempDir())
			bus.later = thread
			id := n.codexPublicID(n.codexHome(), thread)
			n.state.put(id, sessionState{Launcher: codexAppLauncher, Cwd: filepath.Dir(path),
				DesktopPendingModel: "gpt-x", DesktopPendingEffort: "high"})
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			err := n.codexDesktopModes(ctx, id, codexSource{home: n.codexHome(), thread: thread, path: path}, tc.model, "", tc.effort)
			state, _ := n.state.get(id)
			if tc.refused {
				if err == nil || state.DesktopPendingModel != "gpt-x" || state.DesktopPendingEffort != "high" || n.codexAppPickOf(id) != nil {
					t.Fatalf("incompatible patch changed pending or confirmed settings: %+v, %v", state, err)
				}
				if len(bus.settings) != 0 {
					t.Fatal("incompatible patch reached the desktop")
				}
				return
			}
			if err != nil || state.DesktopPendingModel != "" || state.DesktopPendingEffort != "" {
				t.Fatalf("successful patch left older setup pending: %+v, %v", state, err)
			}
			select {
			case request := <-bus.settings:
				settings, _ := request["threadSettings"].(map[string]any)
				actualEffort, _ := settings["effort"].(string)
				if settings["model"] != tc.wantModel || actualEffort != tc.wantEffort {
					t.Fatalf("partial patch did not resolve the remaining setup: %v", request)
				}
			default:
				t.Fatal("settings were not confirmed by the desktop")
			}
			if err := n.send(ctx, id, "hello", nil, sendOpts{}); err != nil {
				t.Fatal(err)
			}
			if len(bus.settings) != 0 {
				t.Fatal("first message replayed superseded settings")
			}
			if pick := n.codexAppPickOf(id); pick == nil || pick.model != tc.wantModel || pick.effort != tc.wantEffort {
				t.Fatalf("first message changed the confirmed choice: %+v", pick)
			}
		})
	}
}

func TestCodexAppSettingsFailureRetriesOnCreatedSession(t *testing.T) {
	n, bus, seed, _ := codexAppNode(t)
	n.codices = newCodexRegistry()
	n.configureCodex(proto.CodexConfig{LegacyHome: n.codexHome()})
	withCodexAppModels(t, n.codexHome(), map[string]codexModelEfforts{
		"gpt-x": {levels: []string{"low", "high"}, def: "low"},
	})
	var persisted []byte
	n.state.persist = func(_ string, state sessionState) {
		var err error
		persisted, err = json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
	}
	bus.mu.Lock()
	bus.refuse = "thread-stream-owner-unavailable"
	bus.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, err := n.newCodexAppChat(ctx, runtimeStartParams{
		Cwd: t.TempDir(), Launcher: codexAppLauncher, Prompt: "hello", Model: "gpt-x", Effort: "high",
	}, "")
	if s == nil {
		t.Fatalf("settings failure lost the created session: %v", err)
	}
	checkDesktopCreationError(t, err, s.SessionID)
	var re *rpc.Error
	if !errors.As(err, &re) || !strings.Contains(string(re.Data), `"first_message_delivered":"none"`) {
		t.Fatalf("settings failure claimed a possible first message: %v", err)
	}
	if s.Model != "" || s.Effort != "" || n.codexAppPickOf(s.SessionID) != nil {
		t.Fatalf("unconfirmed settings were reported as applied: %+v", s)
	}
	// The shared creation response and a node restart must keep setup intent on this native ID.
	_, _ = n.creationResult(s, err, "", "creation", "incarnation")
	var saved sessionState
	if err := json.Unmarshal(persisted, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.DesktopPendingModel != "gpt-x" || saved.DesktopPendingEffort != "high" {
		t.Fatalf("initial setup was not persisted: %+v", saved)
	}
	n.state = newState()
	n.state.merge(map[string]sessionState{s.SessionID: saved})
	if err := n.send(ctx, s.SessionID, "hello", nil, sendOpts{}); refusalCode(err) != "codex_refused" {
		t.Fatalf("retry ignored the failed initial setup: %v", err)
	}
	select {
	case turn := <-bus.turns:
		t.Fatalf("message reached the desktop before settings were confirmed: %v", turn)
	default:
	}
	if state, _ := n.state.get(s.SessionID); state.DesktopPendingModel != "gpt-x" || state.DesktopPendingEffort != "high" {
		t.Fatalf("failed retry erased setup intent: %+v", state)
	}
	bus.mu.Lock()
	bus.refuse = ""
	bus.mu.Unlock()
	if err := n.send(ctx, s.SessionID, "hello", nil, sendOpts{}); err != nil {
		t.Fatalf("retry could not finish setup on the original native session: %v", err)
	}
	seeds := seed.seeded()
	if len(seeds) != 1 || n.codexPublicID(n.codexHome(), seeds[0].thread) != s.SessionID {
		t.Fatalf("retry duplicated the native session: %+v", seeds)
	}
	for range 3 {
		select {
		case settings := <-bus.settings:
			values, _ := settings["threadSettings"].(map[string]any)
			if settings["conversationId"] != seeds[0].thread || values["model"] != "gpt-x" || values["effort"] != "high" {
				t.Fatalf("retry changed setup intent or the native session: %v", settings)
			}
		default:
			t.Fatal("retry did not reapply the requested settings before sending")
		}
	}
	select {
	case turn := <-bus.turns:
		if turn["conversationId"] != seeds[0].thread {
			t.Fatalf("retry sent to another native session: %v", turn)
		}
	case <-ctx.Done():
		t.Fatal("retry sent no first message")
	}
	if state, _ := n.state.get(s.SessionID); state.DesktopPendingModel != "" || state.DesktopPendingEffort != "" {
		t.Fatalf("confirmed setup was left pending: %+v", state)
	}
	if pick := n.codexAppPickOf(s.SessionID); pick == nil || pick.model != "gpt-x" || pick.effort != "high" {
		t.Fatalf("confirmed settings were not exposed: %+v", pick)
	}
}

// With no folder the chat gets one of its own where the App keeps a chat in no project, named after the title.
func TestACodexAppStartWithNoFolderGetsOneWhereTheAppKeepsChats(t *testing.T) {
	n, _, seed, _ := codexAppNode(t)
	day := filepath.Join(n.home, "Documents", "Codex", time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(filepath.Join(day, "csv-export"), 0o755); err != nil { // another chat's
		t.Fatal(err)
	}
	s, err := startCodexApp(t, n, map[string]any{"cwd": "", "launcher": "codex-app", "prompt": "CSV export: 修复编码\nand the rest"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(day, "csv-export-2"); s.Cwd != want || seed.seeded()[0].dir != want {
		t.Fatalf("cwd %s, want %s", s.Cwd, want)
	}
	if st, err := os.Stat(s.Cwd); err != nil || !st.IsDir() {
		t.Fatal("its folder was not made")
	}
}

// What the App could not run as asked is refused before a chat is started: no first message (a chat with none is not
// in the App's sidebar), a read-only plan, a folder that is not there, a secret hand-off, the App not running.
func TestACodexAppStartRefusesWhatTheAppCannotRun(t *testing.T) {
	n, _, seed, opened := codexAppNode(t)
	dir := t.TempDir()
	for _, c := range []struct {
		params map[string]any
		code   string
		says   string
	}{
		{map[string]any{"cwd": dir, "launcher": "codex-app", "prompt": "  "}, "", "第一条消息"},
		{map[string]any{"cwd": dir, "launcher": "codex-app", "task_instructions": "Keep the task scope."}, "codex_needs_prompt", ""},
		{map[string]any{"cwd": dir, "launcher": "codex-app", "prompt": "look", "permission_mode": "plan"}, "readonly_unsupported", ""},
		{map[string]any{"cwd": filepath.Join(dir, "a", "b"), "launcher": "codex-app", "prompt": "hi"}, "", "does not exist"},
		{map[string]any{"cwd": dir, "launcher": "codex-app", "prompt": "hi", "secret_ticket": strings.Repeat("a", 64)}, secretEnvAgentCode, ""},
	} {
		_, err := startCodexApp(t, n, c.params)
		var re *rpc.Error
		if err == nil || c.code != "" && (!errors.As(err, &re) || re.Code != c.code) || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%v: %v, want %q %q", c.params, err, c.code, c.says)
		}
	}
	if len(seed.seeded()) != 0 || len(*opened) != 0 {
		t.Fatalf("seeded %v, opened %v", seed.seeded(), *opened)
	}
	n.home = t.TempDir() // a home with no bus: the App is not running for it
	noFakeCodexBus(t)
	_, err := startCodexApp(t, n, map[string]any{"cwd": dir, "launcher": "codex-app", "prompt": "hi"})
	if code, data := refusalOf(t, err); code != "codex_read_only" || data["delivered"] != deliveredNone {
		t.Fatalf("no App: %v", err)
	}
}

// The launcher is offered where the App runs for the node's Codex home and a Codex CLI is found; a login shell's of
// its name runs instead. Its models are the App's; Settings › Models' Test has nothing to run; no terminal runs it.
func TestTheCodexAppLauncherIsOfferedWhereTheAppRuns(t *testing.T) {
	n, _, _, _ := codexAppNode(t)
	list, added := n.withCodexAppLauncher([]proto.Launcher{{Name: "codex"}})
	if !added || list[1] != (proto.Launcher{Name: "codex-app", Source: "codex_app", Agent: "codex", DisplayName: "Codex App"}) {
		t.Fatalf("list %+v", list)
	}
	if models, err := n.launcherModels(context.Background(), "codex-app", "", false); err != nil || models.Agent != "codex" || len(models.Models) != 0 || models.Locked != "" {
		t.Fatalf("models %+v %v", models, err)
	}
	cacheJSON := `{"models":[
		{"slug":"acme/gpt-5-pro","display_name":"Acme gpt-5-pro","visibility":"list","default_reasoning_level":"medium","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]},
		{"slug":"beta/gpt-5-pro","display_name":"Beta gpt-5-pro","visibility":"list","default_reasoning_level":"high","supported_reasoning_levels":[{"effort":"high"},{"effort":"xhigh"}]},
		{"slug":"hidden/internal","visibility":"hide"}
	]}`
	if err := os.WriteFile(filepath.Join(n.codexHome(), "models_cache.json"), []byte(cacheJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.codexHome(), "config.toml"), []byte("model = \"beta/gpt-5-pro\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if models, err := n.launcherModels(context.Background(), "codex-app", "", true); err != nil || models.Locked != "" || len(models.Models) != 2 || models.Models[0].ID != "acme/gpt-5-pro" || models.Models[0].Name != "Acme gpt-5-pro" || !models.Models[1].Default {
		t.Fatalf("cached models %+v %v", models, err)
	}
	if !n.knownLauncher(context.Background(), "codex-app") {
		t.Fatal("not known")
	}
	if c := n.launcherCheck(context.Background(), "codex-app"); c.Error != codexAppCheckMsg {
		t.Fatalf("check %+v", c)
	}
	if _, err := n.runAgentInPane(context.Background(), "w1:p1", "codex-app"); !errors.Is(err, errCodexAppLauncherInTerminal) {
		t.Errorf("Run Agent Here: %v", err)
	}
	if got, err := terminalLauncher("codex-app", sidA); err != nil || got != "codex" {
		t.Errorf("resume: %q %v", got, err)
	}
	if _, err := terminalLauncher("codex-app", ""); !errors.Is(err, errCodexAppLauncherInTerminal) {
		t.Errorf("terminal start: %v", err)
	}
	old := codexAppCLI
	codexAppCLI = func() (string, error) { return "", errors.New("no codex") }
	if _, added := n.withCodexAppLauncher(nil); added {
		t.Error("offered with no Codex CLI")
	}
	codexAppCLI = old
	n.home = t.TempDir() // the original home still has the fake App listener
	noFakeCodexBus(t)
	if _, added := n.withCodexAppLauncher(nil); added {
		t.Error("offered with the App not running")
	}
	shellLaunchers(t, "codex-app")
	if isCodexAppLauncher("codex-app") {
		t.Error("the login shell's codex-app is not the App's")
	}
}

// A chat Persona started in the App is one of the App's chats without a record in the App's state, and its subagents
// are the App's subagents; Persona's other Codex threads (the assistant's, originator "persona") are not.
func TestPersonasAppChatsAreTheAppsChats(t *testing.T) {
	home, _ := startFakeCodexBus(t, "x")
	chat := writeCodexAppRollout(t, home, "aaaaaaaa-0000-4000-8000-000000000001", codexAppClient, "/w")
	assistant := writeCodexAppRollout(t, home, "aaaaaaaa-0000-4000-8000-000000000002", "persona", "/w")
	sub := filepath.Join(home, "sub.jsonl")
	os.WriteFile(sub, []byte(`{"type":"session_meta","payload":{"originator":"`+codexAppClient+`","source":{"subagent":{"thread_spawn":{"parent_thread_id":"aaaaaaaa-0000-4000-8000-000000000001","depth":1}}}}}`+"\n"), 0o600)
	if !codexDesktopChat(codexSource{home: home, thread: "aaaaaaaa-0000-4000-8000-000000000001", path: chat}) {
		t.Error("Persona's App chat is not the App's")
	}
	if codexDesktopChat(codexSource{home: home, thread: "aaaaaaaa-0000-4000-8000-000000000002", path: assistant}) {
		t.Error("the assistant's thread taken for the App's")
	}
	if o := readCodexOrigin(sub); !o.desktop || o.parent == "" || codexDesktopChat(codexSource{home: home, thread: "sub", path: sub}) {
		t.Errorf("subagent origin %+v", o)
	}
}

// The App's codex starts the chat over stdio as Persona's App client, in its folder, and names it, which writes it; it
// never starts a turn.
func TestCodexAppChatStartsWithTheAppsCodexAndNoTurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the native Windows seed is covered by TestWindowsCodexMetadataSession")
	}
	bin, seen := t.TempDir(), filepath.Join(t.TempDir(), "seen")
	cli := filepath.Join(bin, "codex")
	// A fake app-server: answers metadata requests in order, writes what it got and where it ran.
	script := `#!/bin/sh
{ pwd; echo "args:$*"; echo "home:$CODEX_HOME"; } > ` + seen + `
read l1; echo "$l1" >> ` + seen + `; printf '{"id":1,"result":{}}\n'
read l2; echo "$l2" >> ` + seen + `
read l3; echo "$l3" >> ` + seen + `; printf '{"id":2,"error":{"code":-32601,"message":"unknown method"}}\n'
read l4; echo "$l4" >> ` + seen + `; printf '{"method":"thread/started","params":{}}\n{"id":3,"result":{"thread":{"id":"aaaaaaaa-0000-4000-8000-000000000111","path":"/x"}}}\n'
read l5; echo "$l5" >> ` + seen + `; printf '{"id":4,"result":{}}\n'
read l6
`
	if err := os.WriteFile(cli, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, home := t.TempDir(), t.TempDir()
	thread, err := startCodexAppChat(context.Background(), cli, home, dir, "修复导出")
	if err != nil || thread != "aaaaaaaa-0000-4000-8000-000000000111" {
		t.Fatalf("%q %v", thread, err)
	}
	b, _ := os.ReadFile(seen)
	got := string(b)
	real, _ := filepath.EvalSymlinks(dir)
	if first := strings.SplitN(got, "\n", 2)[0]; first != dir && first != real {
		t.Errorf("ran in %q", first)
	}
	for _, want := range []string{"args:app-server", "home:" + home, `"name":"persona_codex_app"`, `"method":"initialized"`,
		`"method":"project/list"`, `"method":"thread/start","params":{"cwd":"` + dir + `"}`, `"method":"thread/name/set","params":{"name":"修复导出","threadId":"aaaaaaaa-0000-4000-8000-000000000111"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "turn/start") {
		t.Error("a turn was started")
	}
}

func TestCodexStdioCancellationStopsWrapperChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the wrapper fixture needs Unix shell and FIFO semantics")
	}
	home := t.TempDir()
	cli := filepath.Join(home, "codex")
	// The wrapper waits for a child with inherited stdout. Closing the wrapper alone cannot unblock initialize.
	script := `#!/bin/sh
mkfifo "$CODEX_HOME/block"
cat "$CODEX_HOME/block" &
printf '%s\n' "$!" > "$CODEX_HOME/child"
wait
`
	if err := os.WriteFile(cli, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		server, err := startCodexStdio(ctx, cli, home, home)
		if server != nil {
			server.close()
		}
		finished <- err
	}()
	var child int
	waitFor(t, "wrapper child to start", func() bool {
		b, _ := os.ReadFile(filepath.Join(home, "child"))
		child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return child > 0
	})
	t.Cleanup(func() { _ = osproc.Kill(child) })
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("initialize succeeded after cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("initialize stayed blocked on the wrapper child's stdout")
	}
	waitFor(t, "wrapper child to exit", func() bool { return !osproc.Alive(child) })
}

func TestCodexAppSlugIsTheTitlesASCIIWords(t *testing.T) {
	for _, c := range []struct{ title, want string }{
		{"Fix the CSV export!", "fix-the-csv-export"},
		{"修复 CSV 导出的编码", "csv"},
		{"整理周报", "chat"},
		{strings.Repeat("abcdefghij ", 6), "abcdefghij-abcdefghij-abcdefghij"},
	} {
		if got := codexAppSlug(c.title); got != c.want {
			t.Errorf("%q: %q, want %q", c.title, got, c.want)
		}
	}
}

type codexStdioTestInput struct{ bytes.Buffer }

func (*codexStdioTestInput) Close() error { return nil }

func TestCodexStdioSupportsModelListAndPreservesRPCErrors(t *testing.T) {
	in := &codexStdioTestInput{}
	s := &codexStdio{ctx: context.Background(), cancel: func() {}, in: in,
		lines: bufio.NewScanner(strings.NewReader(`{"method":"thread/status","params":{}}
{"id":99,"result":{}}
{"id":1,"result":{"data":[{"model":"gpt-example"}],"nextCursor":null}}
{"id":2,"error":{"code":-32601,"message":"unknown method"}}
`))}
	var models struct {
		Data []struct {
			Model string `json:"model"`
		} `json:"data"`
	}
	if err := s.call(context.Background(), "model/list", map[string]any{"includeHidden": true}, &models); err != nil ||
		len(models.Data) != 1 || models.Data[0].Model != "gpt-example" {
		t.Fatalf("model/list = %+v, %v", models, err)
	}
	err := s.call(context.Background(), "project/list", map[string]any{}, nil)
	var rpcErr *codexAppServerError
	if !errors.As(err, &rpcErr) || rpcErr.Method != "project/list" || rpcErr.Code != -32601 {
		t.Fatalf("lost native RPC error: %v", err)
	}
	if !strings.Contains(in.String(), `"method":"model/list","params":{"includeHidden":true}`) {
		t.Fatalf("model request changed: %s", in.String())
	}
}
