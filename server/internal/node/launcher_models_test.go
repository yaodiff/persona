package node

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

// Claude Code's initialize answer, as a 2.1 CLI with a gateway gives it: the /model menu's aliases resolved with the
// launcher's settings, then the gateway's models.
const claudeInitAnswer = `{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"commands":[],"models":[` +
	`{"value":"default","resolvedModel":"claude-opus-5-5[1m]","displayName":"Default (recommended)","description":"Use the default model","supportsEffort":true,"supportedEffortLevels":["low","medium","high","max"]},` +
	`{"value":"haiku","resolvedModel":"example-small","displayName":"example-small","description":"Custom Haiku model"},` +
	`{"value":"gateway/model-a","resolvedModel":"gateway/model-a","displayName":"model-a","description":"From gateway","supportsEffort":true,"supportedEffortLevels":["low","high","ultra"]},` +
	`{"value":"","displayName":"nameless"}]}}}`

func TestClaudeModelListReadsInitialize(t *testing.T) {
	var sent strings.Builder
	out := "zsh: a banner from ~/.zshrc\n" + `{"type":"system","subtype":"status"}` + "\n" + claudeInitAnswer + "\n"
	got, err := claudeModelList(&sent, strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent.String(), `"subtype":"initialize"`) {
		t.Fatalf("asked %q", sent.String())
	}
	want := []proto.AgentModel{
		{ID: "default", Name: "Default (recommended)", Description: "Use the default model", Resolved: "claude-opus-5-5[1m]", Default: true, Efforts: []string{"low", "medium", "high", "max"}},
		{ID: "haiku", Name: "example-small", Description: "Custom Haiku model", Resolved: "example-small"},
		// "ultra" is no effort a Claude start takes
		{ID: "gateway/model-a", Name: "model-a", Description: "From gateway", Efforts: []string{"low", "high"}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Name != want[i].Name || got[i].Resolved != want[i].Resolved ||
			got[i].Default != want[i].Default || !slices.Equal(got[i].Efforts, want[i].Efforts) {
			t.Fatalf("model %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

// With "model": "default" in its settings, Claude Code's list names default twice (#9998): one row of it reaches the apps.
func TestClaudeModelListKeepsOneRowPerID(t *testing.T) {
	out := `{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"models":[` +
		`{"value":"default","resolvedModel":"claude-opus-5-5","displayName":"Default (recommended)"},` +
		`{"value":"opus","resolvedModel":"claude-opus-5-5","displayName":"Opus"},` +
		`{"value":"default","resolvedModel":"default","displayName":"default","description":"Custom model"}]}}}`
	got, err := claudeModelList(io.Discard, strings.NewReader(out+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "default" || got[0].Name != "Default (recommended)" || got[1].ID != "opus" {
		t.Fatalf("got %+v, want default (the first one) and opus", got)
	}
}

func TestClaudeModelListErrors(t *testing.T) {
	for name, out := range map[string]string{
		"refused":   `{"type":"control_response","response":{"subtype":"error","request_id":"persona-models","error":"Not logged in"}}`,
		"old CLI":   `{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"commands":[]}}}`,
		"no answer": `zsh: command not found: claude`,
	} {
		if _, err := claudeModelList(io.Discard, strings.NewReader(out+"\n")); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestManagedClaudeModelListWithNode(t *testing.T) {
	for _, setup := range []string{
		"NVM", "Homebrew prefix", "Node on service PATH", "Node from shell startup", "Claude shell wrapper",
	} {
		t.Run(setup, func(t *testing.T) {
			testManagedClaudeWithNode(t, setup, false)
		})
	}
}

func TestManagedClaudeLaunchPreservesPath(t *testing.T) {
	testManagedClaudeWithNode(t, "Homebrew prefix", true)
}

func testManagedClaudeWithNode(t *testing.T, setup string, ordinaryLaunch bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix npm launchers and node sockets")
	}
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no Node.js to run the npm launcher fixture")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := launchKeyHome(t)
	n, _ := testNode(t)
	n.home, n.dir = home, filepath.Dir(nodeclient.SocketPath())
	programDir := filepath.Join(home, "Persona Link")
	if err := os.MkdirAll(programDir, 0o700); err != nil {
		t.Fatal(err)
	}
	n.bin = filepath.Join(programDir, "personad")
	if err := os.Symlink(self, n.bin); err != nil {
		t.Fatal(err)
	}
	serviceBin := filepath.Join(home, "service-bin")
	link := func(target, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	link(zsh, filepath.Join(serviceBin, "zsh"))
	t.Setenv("NVM_DIR", "")
	t.Setenv("PERSONA_NODEJS", "")
	t.Setenv("PATH", serviceBin)
	localClaude := filepath.Join(home, ".local", "bin", "claude")
	cli := localClaude
	var prefix string
	if setup == "NVM" {
		root := filepath.Join(home, "custom NVM")
		prefix = filepath.Join(root, "versions", "node", "v22.1.0")
		t.Setenv("NVM_DIR", root)
		// The service knows one Node installation; a different interpreter on PATH must not win.
		t.Setenv("PERSONA_NODEJS", filepath.Join(prefix, "bin", "node"))
		writeExecutable(t, filepath.Join(serviceBin, "node"), "#!/bin/sh\nexit 17\n")
	} else if setup == "Homebrew prefix" {
		prefix = filepath.Join(home, "opt", "homebrew")
	}
	if prefix != "" {
		cli = filepath.Join(prefix, "lib", "node_modules", "@anthropic-ai", "claude-code", "cli.js")
		bin := filepath.Join(prefix, "bin")
		rel, err := filepath.Rel(bin, cli)
		if err != nil {
			t.Fatal(err)
		}
		link(rel, filepath.Join(bin, "claude"))
		link(node, filepath.Join(bin, "node"))
		if setup == "Homebrew prefix" {
			link(filepath.Join(bin, "claude"), localClaude)
		}
	} else if setup == "Node on service PATH" {
		link(node, filepath.Join(serviceBin, "node"))
	}
	if ordinaryLaunch {
		writeExecutable(t, filepath.Join(serviceBin, "node"),
			"#!/bin/sh\nFIXTURE_NODE=service exec "+shellQuote(node)+` "$@"`+"\n")
		writeExecutable(t, filepath.Join(serviceBin, "fixture-tool"), "#!/bin/sh\nexit 0\n")
		writeExecutable(t, filepath.Join(prefix, "bin", "fixture-tool"), "#!/bin/sh\nexit 17\n")
	}

	shellWrapper := setup == "Claude shell wrapper"
	if shellWrapper {
		cli = filepath.Join(home, "wrapped Claude", "cli.js")
		writeExecutable(t, localClaude, "#!/bin/sh\nexec node "+shellQuote(cli)+` "$@"`+"\n")
	}
	shellSetup := setup == "Node from shell startup" || shellWrapper
	wantPath := serviceBin
	rc := `: > "$HOME/rc-ran"` + "\n/bin/sleep 20\n"
	if shellSetup {
		shellBin := filepath.Join(home, "shell runtime", "bin")
		link(node, filepath.Join(shellBin, "node"))
		wantPath = shellBin + ":" + loginShellPATH(t, serviceBin)
		rc = `: > "$HOME/rc-ran"` + "\nexport PATH=" + shellQuote(shellBin) + `:"$PATH"` + "\n"
	}
	t.Setenv("FIXTURE_EXPECTED_PATH", wantPath)
	writeFile(t, filepath.Join(home, ".zshrc"), rc)
	writeExecutable(t, cli, `#!/usr/bin/env node
const fs = require('node:fs');
const readline = require('node:readline');
const args = process.argv.slice(2);
if (process.env.PATH !== process.env.FIXTURE_EXPECTED_PATH || process.env.PERSONA_MANAGED_MODEL_PROBE !== undefined) {
  throw new Error('Managed Claude inherited a changed PATH or the internal probe marker');
}
if (args.includes('--version')) {
  const {spawnSync} = require('node:child_process');
  if (process.env.FIXTURE_NODE !== 'service' || process.env.ANTHROPIC_AUTH_TOKEN !== 'probe-fixture' ||
      spawnSync('fixture-tool').status !== 0) {
    throw new Error('Managed Claude did not use the Node.js and tool from the service PATH');
  }
  process.exit(0);
}
const settings = JSON.parse(fs.readFileSync(args[args.indexOf('--settings') + 1]));
const rl = readline.createInterface({input: process.stdin});
rl.on('line', (line) => {
  const req = JSON.parse(line);
  if (req.type !== 'control_request' || req.request.subtype !== 'initialize' ||
      !settings.disableAllHooks || settings.env.ANTHROPIC_AUTH_TOKEN !== 'probe-fixture' ||
      process.env.ANTHROPIC_AUTH_TOKEN !== 'probe-fixture' ||
      !args.includes('stream-json') || !args.includes('--verbose')) {
    throw new Error('Missing managed model probe configuration');
  }
  console.log(JSON.stringify({type: 'control_response', response: {
    subtype: 'success', request_id: req.request_id, response: {models: [
      {value: 'npm-model', displayName: 'NPM model'}
    ]}
  }}));
});
`)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("NODE_OPTIONS", "")
	t.Setenv("NODE_TEST_LAUNCH", "1")
	n.setManaged([]proto.ManagedLauncher{{Name: "store", APIKey: "probe-fixture", Auth: "bearer"}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := n.listenOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ordinaryLaunch {
		out, err := exec.CommandContext(ctx, n.bin, "launch", "store", "--version").CombinedOutput()
		if err != nil {
			t.Fatalf("managed launch: %v: %s", err, out)
		}
	} else {
		models, err := n.listAgentModels(ctx, "store", "claude")
		if err != nil || len(models) != 1 || models[0].ID != "npm-model" {
			t.Fatalf("managed models = %+v, %v", models, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "rc-ran")); (shellSetup && err != nil) || (!shellSetup && !os.IsNotExist(err)) {
		t.Fatalf("shell initialization: expected ran=%t, stat error=%v", shellSetup, err)
	}
}

// fakeModelServer answers like codex app-server: initialize, then model/list in two pages, with a notification and a
// request of its own in between.
func fakeModelServer(t *testing.T, in io.Reader, out io.WriteCloser) {
	t.Helper()
	defer out.Close()
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		var req struct {
			ID     *int           `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		reply := func(result string) {
			io.WriteString(out, `{"jsonrpc":"2.0","method":"thread/status","params":{}}`+"\n")
			io.WriteString(out, `{"jsonrpc":"2.0","id":99,"method":"account/refresh","params":{}}`+"\n")
			b, _ := json.Marshal(*req.ID)
			io.WriteString(out, `{"jsonrpc":"2.0","id":`+string(b)+`,"result":`+result+"}\n")
		}
		switch {
		case req.Method == "initialize":
			reply(`{"userAgent":"codex"}`)
		case req.Method == "model/list" && req.Params["cursor"] == nil:
			reply(`{"data":[{"id":"preset-a","model":"model-a","displayName":"Model A","description":"default one","isDefault":true,"hidden":false,` +
				`"defaultReasoningEffort":"medium","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"medium"},{"reasoningEffort":"high"}],` +
				`"serviceTiers":[{"id":"priority","name":"Fast","description":"Increased usage"}]},` +
				`{"id":"old","model":"old","displayName":"Old","hidden":true}],"nextCursor":"p2"}`)
		case req.Method == "model/list" && req.Params["cursor"] == "p2":
			reply(`{"data":[{"id":"gateway/model-b","model":"gateway/model-b","displayName":"","hidden":false,` +
				`"defaultReasoningEffort":"none","supportedReasoningEfforts":[]}],"nextCursor":null}`)
		default:
			io.WriteString(out, `{"jsonrpc":"2.0","id":1,"error":{"message":"unexpected"}}`+"\n")
		}
	}
}

func TestCodexModelListPagesAndHides(t *testing.T) {
	toServer, fromClient := io.Pipe()
	fromServer, toClient := io.Pipe()
	go fakeModelServer(t, toServer, toClient)
	got, err := codexModelList(fromClient, fromServer)
	fromClient.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	a, b := got[0], got[1]
	if a.ID != "model-a" || a.Name != "Model A" || !a.Default || a.DefaultEffort != "medium" || !slices.Equal(a.Efforts, []string{"low", "medium", "high"}) {
		t.Fatalf("first %+v", a)
	}
	raw, _ := json.Marshal(a)
	if !strings.Contains(string(raw), `"service_tiers":[{"id":"priority","name":"Fast","description":"Increased usage"}]`) {
		t.Fatalf("first lost its native speed choices: %s", raw)
	}
	// A model that takes no level (a gateway catalog's) offers none, and no default one either.
	if b.ID != "gateway/model-b" || b.Name != "gateway/model-b" || b.Default || len(b.Efforts) != 0 || b.DefaultEffort != "" {
		t.Fatalf("second %+v", b)
	}
}

// fakeCLIHome is a home whose ~/.zshrc defines launchers running a fake claude (on PATH) that writes its arguments
// to args.txt, answers initialize with the model its launcher set and then waits, as a real one waits for a message.
func fakeCLIHome(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("no zsh")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	claude := `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > "$HOME/args.txt"
for a in "$@"; do case "$a" in /*settings*) cat "$a" > "$HOME/settings.txt" ;; esac; done
read line
echo "a line that is no JSON"
echo '{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"models":[{"value":"default","resolvedModel":"'"$LAUNCHER_MODEL"'","displayName":"Default"}]}}}'
sleep 60
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(claude), 0o700); err != nil {
		t.Fatal(err)
	}
	zshrc := `mine() { LAUNCHER_MODEL=model-of-mine command claude "$@" }
fixed() { LAUNCHER_MODEL=fixed-model command claude --model fixed-model "$@" }
`
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(zshrc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("ZDOTDIR", home)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	// The probe's login shell inherits this process's environment, and a runner started from an agent has the
	// agent's model and effort there (Claude Code sets ANTHROPIC_MODEL, CLAUDE_CODE_EFFORT_LEVEL, ...): they would
	// decide the fixture's default model and effort (#9113). t.Setenv restores each one afterwards.
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE_") {
			t.Setenv(name, "")
			os.Unsetenv(name)
		}
	}
	launcherCache.Lock()
	oldAt, oldList, oldSettings := launcherCache.at, launcherCache.list, launcherCache.settings
	launcherCache.at, launcherCache.list, launcherCache.settings = time.Now(), []proto.Launcher{{Name: "mine"}, {Name: "fixed"}}, map[string]string{}
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list, launcherCache.settings = oldAt, oldList, oldSettings
		launcherCache.Unlock()
	})
	return home
}

// The list comes from the CLI run as the launcher runs it (its environment), with hooks off, and the CLI is not
// waited for once it has answered.
func TestLauncherModelsRunsTheLauncher(t *testing.T) {
	home := fakeCLIHome(t)
	n, _ := testNode(t)
	n.home = home
	started := time.Now()
	res, err := n.launcherModels(context.Background(), "mine", "claude", true)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 20*time.Second {
		t.Fatalf("waited %v for the CLI to exit", took)
	}
	if res.Agent != "claude" || res.Locked != "" || len(res.Models) != 1 || res.Models[0].Resolved != "model-of-mine" || !res.Models[0].Default {
		t.Fatalf("got %+v", res)
	}
	args, _ := os.ReadFile(filepath.Join(home, "args.txt"))
	for _, want := range []string{"-p", "--input-format", "stream-json", "--settings"} {
		if !slices.Contains(strings.Split(string(args), "\n"), want) {
			t.Fatalf("claude got %q", args)
		}
	}
	settings, _ := os.ReadFile(filepath.Join(home, "settings.txt"))
	if !strings.Contains(string(settings), `"disableAllHooks":true`) {
		t.Fatalf("settings %q", settings)
	}

	// Kept: the second ask does not run the CLI again.
	os.Remove(filepath.Join(home, "args.txt"))
	if _, err := n.launcherModels(context.Background(), "mine", "claude", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "args.txt")); err == nil {
		t.Fatal("asked the CLI again")
	}
}

// A managed Codex provider's list is its codex's app-server's, through personad launch: the node used to run
// `personad launch <name>` alone, whose codex started its TUI ("stdin is not a terminal") and listed nothing.
func TestManagedCodexModelsAskTheAppServer(t *testing.T) {
	home := fakeCLIHome(t)
	n, _ := testNode(t)
	n.home, n.dir = home, filepath.Join(home, ".persona")
	// personad launch, as the provider's codex: an app-server when asked for one, else a TUI with no terminal.
	n.bin = filepath.Join(home, "bin", "personad")
	writeFile(t, n.bin, `#!/bin/sh
printf '%s\n' "$@" > "$HOME/personad-args.txt"
case " $* " in *" app-server "*) ;; *) echo "Error: stdin is not a terminal" >&2; exit 1 ;; esac
read line
echo '{"jsonrpc":"2.0","id":1,"result":{"userAgent":"codex"}}'
read line
read line
echo '{"jsonrpc":"2.0","id":2,"result":{"data":[{"id":"example-big","model":"example-big","displayName":"Example Big","hidden":false,"defaultReasoningEffort":"high","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}]},{"id":"example-plain","model":"example-plain","displayName":"Example Plain","hidden":false,"defaultReasoningEffort":"none","supportedReasoningEfforts":[]}],"nextCursor":null}}'
sleep 60
`)
	if err := os.Chmod(n.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	n.setManaged([]proto.ManagedLauncher{{Name: "gw", Agent: "codex", BaseURL: "http://127.0.0.1:9/v1", APIKey: "k", Model: "example-big"}})
	res, err := n.launcherModels(context.Background(), "gw", "", true)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(home, "personad-args.txt"))
	if got := strings.Fields(string(args)); !slices.Equal(got, []string{"launch", "gw", "app-server"}) {
		t.Fatalf("personad got %q", got)
	}
	if res.Agent != "codex" || len(res.Models) != 2 {
		t.Fatalf("got %+v", res)
	}
	big, plain := res.Models[0], res.Models[1]
	if big.ID != "example-big" || !slices.Equal(big.Efforts, []string{"low", "high"}) || big.DefaultEffort != "high" {
		t.Fatalf("example-big %+v", big)
	}
	if plain.ID != "example-plain" || len(plain.Efforts) != 0 || plain.DefaultEffort != "" {
		t.Fatalf("a model that takes no level: %+v", plain)
	}
}

func TestManagedCodexModelProbeHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix npm launcher and node socket")
	}
	home := launchKeyHome(t)
	n, _ := testNode(t)
	n.home, n.dir = home, filepath.Dir(nodeclient.SocketPath())
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	n.bin = self
	localBin := filepath.Join(home, ".local", "bin")
	writeExecutable(t, filepath.Join(localBin, "codex"), `#!/usr/bin/env node
case " $* " in *" app-server "*) ;; *) exit 17 ;; esac
[ -z "$PERSONA_MANAGED_MODEL_PROBE" ] || exit 18
printf '%s' "$CODEX_HOME"
`)
	writeExecutable(t, filepath.Join(localBin, "node"), "#!/bin/sh\nexec /bin/sh \"$@\"\n")
	serviceBin := filepath.Join(home, "service-bin")
	writeExecutable(t, filepath.Join(serviceBin, "node"), "#!/bin/sh\nexit 19\n")
	writeFile(t, filepath.Join(home, ".zshrc"), `: > "$HOME/rc-ran"`+"\n")
	t.Setenv("PATH", serviceBin+":/usr/bin:/bin")
	t.Setenv("PERSONA_NODEJS", "")
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ZDOTDIR", home)
	t.Setenv("NODE_TEST_LAUNCH", "1")
	inheritedHome := filepath.Join(home, "inherited codex")
	providerHome := filepath.Join(home, "provider codex")
	t.Setenv("CODEX_HOME", inheritedHome)
	if _, err := n.listenOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	const gateway = "https://gw.example.com/v1"
	for _, tc := range []struct {
		name, baseURL, providerEnv, wantHome string
		isolate                              bool
	}{
		{name: "custom gateway", baseURL: gateway, isolate: true},
		{name: "default gateway", wantHome: inheritedHome},
		{name: "provider home", baseURL: gateway, providerEnv: "export CODEX_HOME=" + shellQuote(providerHome), wantHome: providerHome},
		{name: "explicit empty home", baseURL: gateway, providerEnv: "CODEX_HOME="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n.setManaged([]proto.ManagedLauncher{{Name: "gw", Agent: "codex", BaseURL: tc.baseURL, Env: tc.providerEnv}})
			dir := t.TempDir()
			isolatedHome := filepath.Join(dir, "codex-home")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd, err := n.agentModelsCommand(ctx, "gw", "codex", dir)
			if err != nil {
				t.Fatal(err)
			}
			if cmd.Path != n.bin || cmd.Dir != dir {
				t.Fatalf("probe executable = %q in %q, want %q in %q", cmd.Path, cmd.Dir, n.bin, dir)
			}
			if !slices.Equal(cmd.Args[1:], []string{"launch", "gw", "app-server"}) {
				t.Fatalf("probe command args = %q, want launch gw app-server", cmd.Args[1:])
			}
			wantHome := tc.wantHome
			if tc.isolate {
				wantHome = isolatedHome
				if st, err := os.Stat(isolatedHome); err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
					t.Fatalf("private Codex home: %v, %v", st, err)
				}
			} else if _, err := os.Stat(isolatedHome); !os.IsNotExist(err) {
				t.Fatalf("unexpected temporary Codex home: %v", err)
			}
			out, err := cmd.CombinedOutput()
			if err != nil || string(out) != wantHome {
				t.Fatalf("Codex saw CODEX_HOME %q, want %q: %v", out, wantHome, err)
			}
			if _, err := os.Stat(filepath.Join(home, "rc-ran")); !os.IsNotExist(err) {
				t.Fatalf("shell startup ran during the direct probe: %v", err)
			}
		})
	}
}

// A list past its TTL is answered at once, and the CLI is asked again in the background for the next ask (#1501): the
// picker does not wait seconds for the login shell and the CLI every ten minutes.
func TestLauncherModelsAnswersAnExpiredListAtOnce(t *testing.T) {
	home := fakeCLIHome(t)
	n, _ := testNode(t)
	n.home = home
	if res, err := n.launcherModels(context.Background(), "mine", "claude", true); err != nil || len(res.Models) != 1 || res.Models[0].Resolved != "model-of-mine" {
		t.Fatalf("first ask: %+v %v", res, err)
	}
	// The launcher now answers slowly with another model, and the kept list has expired.
	zshrc := "mine() { sleep 2; LAUNCHER_MODEL=model-of-mine-2 command claude \"$@\" }\n"
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(zshrc), 0o600); err != nil {
		t.Fatal(err)
	}
	launcherModelsCache.Lock()
	for k := range launcherModelsCache.at {
		if strings.HasPrefix(k, home+"\x00") {
			launcherModelsCache.at[k] = time.Now().Add(-launcherModelsTTL - time.Minute)
		}
	}
	launcherModelsCache.Unlock()

	started := time.Now()
	res, err := n.launcherModels(context.Background(), "mine", "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > time.Second {
		t.Fatalf("an expired list waited %v for the CLI", took)
	}
	if len(res.Models) != 1 || res.Models[0].Resolved != "model-of-mine" {
		t.Fatalf("expired ask: %+v", res)
	}
	// The background ask replaces it; asks meanwhile join it and keep answering the kept list.
	deadline := time.Now().Add(30 * time.Second)
	for {
		res, err = n.launcherModels(context.Background(), "mine", "claude", false)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Models) == 1 && res.Models[0].Resolved == "model-of-mine-2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the background ask never replaced the list: %+v", res)
		}
		time.Sleep(50 * time.Millisecond) // sleep-ok: the poll interval of a wait bounded by the deadline above
	}

	// Too old to show: the ask waits for the CLI, as it does with no list.
	launcherModelsCache.Lock()
	for k := range launcherModelsCache.at {
		if strings.HasPrefix(k, home+"\x00") {
			launcherModelsCache.at[k] = time.Now().Add(-launcherModelsStale - time.Minute)
		}
	}
	launcherModelsCache.Unlock()
	zshrc = "mine() { LAUNCHER_MODEL=model-of-mine-3 command claude \"$@\" }\n"
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(zshrc), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err := n.launcherModels(context.Background(), "mine", "claude", false); err != nil || len(res.Models) != 1 || res.Models[0].Resolved != "model-of-mine-3" {
		t.Fatalf("a list past launcherModelsStale was answered: %+v %v", res, err)
	}
}

// A launcher that passes its own --model would start with it whatever is picked: the list says so.
func TestLauncherModelsLockedByFixedModel(t *testing.T) {
	home := fakeCLIHome(t)
	n, _ := testNode(t)
	n.home = home
	res, err := n.launcherModels(context.Background(), "fixed", "claude", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Locked == "" || len(res.Models) != 1 {
		t.Fatalf("got %+v", res)
	}
}

// Asking a launcher for its models runs it, so a name this machine does not offer as a launcher is not run, unless the
// hub says its hub.json names it.
func TestLauncherModelsOnlyRunsKnownLaunchers(t *testing.T) {
	home := fakeCLIHome(t)
	n, _ := testNode(t)
	// A node directory of its own: the managed launcher's merged settings would otherwise land in the source tree.
	n.home, n.dir = home, filepath.Join(home, ".persona")
	marker := filepath.Join(home, "ran")
	f, err := os.OpenFile(filepath.Join(home, ".zshrc"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("notalauncher() { echo ran >> " + marker + " }\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	ask := func(name string, configured bool) error {
		params, _ := json.Marshal(map[string]any{"name": name, "refresh": true, "configured": configured})
		_, err := n.handle(context.Background(), "launchers.models", params)
		return err
	}
	for _, name := range []string{"notalauncher", "true"} {
		if err := ask(name, false); err == nil {
			t.Errorf("asked %s, not a launcher, for its models", name)
		}
	}
	if b, err := os.ReadFile(marker); err == nil {
		t.Fatalf("ran what is not a launcher: %s", b)
	}
	if err := ask("mine", false); err != nil {
		t.Fatalf("a launcher of this machine: %v", err)
	}
	_ = ask("notalauncher", true) // the hub vouches for it: asked, whatever it answers
	if _, err := os.ReadFile(marker); err != nil {
		t.Fatalf("a launcher in hub.json was not asked: %v", err)
	}
	_ = os.Remove(marker)
	params, _ := json.Marshal(map[string]any{"name": "notalauncher", "agent_type": "claude", "refresh": true}) // an old hub says it with this
	_, _ = n.handle(context.Background(), "launchers.models", params)
	if _, err := os.ReadFile(marker); err != nil {
		t.Fatalf("a launcher with its type from hub.json was not asked: %v", err)
	}
	n.setManaged([]proto.ManagedLauncher{{Name: "managed"}})
	for _, name := range []string{"claude", "traex", "managed"} {
		if err := ask(name, false); err != nil && strings.Contains(err.Error(), "unknown launcher") {
			t.Errorf("%s refused: %v", name, err)
		}
	}
}

// A runtime's launcher is a launcher too, though no shell lists it.
func TestLauncherModelsKnowsRuntimeLaunchers(t *testing.T) {
	n, _ := runtimeNode(t)
	dialRuntime(t, n, dshHello())
	var name string
	for _, l := range n.runtimes.launchers() {
		name = l.Name
	}
	if name == "" {
		t.Fatal("no runtime launcher")
	}
	params, _ := json.Marshal(map[string]any{"name": name})
	if _, err := n.handle(context.Background(), "launchers.models", params); err != nil && strings.Contains(err.Error(), "unknown launcher") {
		t.Fatalf("%s refused: %v", name, err)
	}
}

func TestLauncherModelsTraex(t *testing.T) {
	n, _ := testNode(t)
	t.Setenv("HOME", n.home)
	t.Setenv("ZDOTDIR", n.home)
	for _, rc := range []string{".bashrc", ".zshrc"} {
		if err := os.WriteFile(filepath.Join(n.home, rc), []byte("printf 'Shell startup [notice]\\n'\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/bin/sh
case "$#:$*" in
  '2:debug models')
    test "$TRAE_TEST_LEGACY" != 1 || exit 2
    printf '%s\n' '{"models":[{"config_name":"example-fast","slug":"Example Fast","description":"Fast model","default_reasoning_level":"medium","supported_reasoning_levels":[{"effort":"medium"},{"effort":"xhigh"}],"visibility":"list"},{"config_name":"hidden","visibility":"hide"},{"config_name":"example-fast","slug":"Duplicate"},{"slug":"Display only"},{"config_name":"--flag"},{"config_name":"example-small"}]}'
    ;;
  '2:models --json')
    printf '%s\n' '[{"config_name":"example-fast","name":"Example Fast","description":"Fast model","backend_model":"backend-only"},{"config_name":"example-small"}]'
    ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "traex"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := n.launcherModels(context.Background(), traexAgent, "", true)
	want := []proto.AgentModel{
		{ID: "example-fast", Name: "Example Fast", Description: "Fast model", Efforts: []string{"medium", "xhigh"}, DefaultEffort: "medium"},
		{ID: "example-small", Name: "example-small"},
	}
	if err != nil || res.Agent != traexAgent || len(res.Models) != len(want) {
		t.Fatalf("got %+v %v", res, err)
	}
	for i, m := range res.Models {
		if m.ID != want[i].ID || m.Name != want[i].Name || m.Description != want[i].Description || m.Default ||
			!slices.Equal(m.Efforts, want[i].Efforts) || m.DefaultEffort != want[i].DefaultEffort {
			t.Errorf("model %d = %+v, want %+v", i, m, want[i])
		}
	}
	t.Setenv("TRAE_TEST_LEGACY", "1")
	cached, err := n.launcherModels(context.Background(), traexAgent, "", false)
	if err != nil || len(cached.Models) != 2 || !slices.Equal(cached.Models[0].Efforts, []string{"medium", "xhigh"}) {
		t.Fatalf("cached TRAE catalog: %+v, %v", cached, err)
	}
	fallback, err := n.launcherModels(context.Background(), traexAgent, "", true)
	if err != nil || len(fallback.Models) != 2 || fallback.Models[0].ID != "example-fast" || len(fallback.Models[0].Efforts) != 0 {
		t.Fatalf("public TRAE catalog: %+v, %v", fallback, err)
	}
}

func TestLauncherModelsRejectsInvalidName(t *testing.T) {
	n, _ := testNode(t)
	if _, err := n.launcherModels(context.Background(), "bad name;", "", false); err == nil {
		t.Fatal("ran an invalid launcher name")
	}
}

// The probe, the gateway (quota) and the --model check run the launcher too: the same rule as its models. A word this
// machine does not offer as a launcher is not run, unless the hub's hub.json names it (#1185).
func TestLauncherProbesOnlyRunKnownLaunchers(t *testing.T) {
	home := fakeCLIHome(t)
	n, _ := testNode(t)
	n.home, n.dir = home, filepath.Join(home, ".persona")
	marker := filepath.Join(home, "ran")
	f, err := os.OpenFile(filepath.Join(home, ".zshrc"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("notalauncher() { echo ran >> " + marker + " }\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	probeCache.Lock()
	delete(probeCache.at, "notalauncher")
	probeCache.Unlock()
	ask := func(method, name string, configured bool) error {
		params, _ := json.Marshal(map[string]any{"name": name, "model": "claude-opus-5-5", "configured": configured})
		_, err := n.handle(context.Background(), method, params)
		return err
	}
	methods := []string{"launchers.probe", "launchers.http", "launchers.model.check"}
	for _, method := range methods {
		for _, name := range []string{"notalauncher", "true"} {
			if err := ask(method, name, false); err == nil || !strings.Contains(err.Error(), "unknown launcher") {
				t.Errorf("%s %s, not a launcher: %v", method, name, err)
			}
		}
	}
	if b, err := os.ReadFile(marker); err == nil {
		t.Fatalf("ran what is not a launcher: %s", b)
	}
	for _, method := range methods {
		if err := ask(method, "mine", false); err != nil && strings.Contains(err.Error(), "unknown launcher") {
			t.Errorf("%s of a launcher of this machine refused: %v", method, err)
		}
	}
	_ = ask("launchers.probe", "notalauncher", true) // the hub vouches for it: asked, whatever it answers
	if _, err := os.ReadFile(marker); err != nil {
		t.Fatalf("a launcher in hub.json was not probed: %v", err)
	}
}

// loginShellPATH is the PATH a zsh started as the node starts it (shellFlag) has before ~/.zshrc, from path: on macOS
// a login shell, whose /etc/zprofile puts path_helper's directories first (#16217); elsewhere path as it is.
func loginShellPATH(t *testing.T, path string) string {
	t.Helper()
	const helper = "/usr/libexec/path_helper"
	if !strings.Contains(shellFlag("zsh"), "l") {
		return path
	}
	if _, err := os.Stat(helper); err != nil {
		return path
	}
	cmd := exec.Command(helper, "-s")
	cmd.Env = []string{"PATH=" + path}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	// PATH="<dirs>"; export PATH;
	v, ok := strings.CutPrefix(strings.TrimSpace(string(out)), `PATH="`)
	v, _, found := strings.Cut(v, `";`)
	if !ok || !found {
		t.Fatalf("path_helper printed %q", out)
	}
	return v
}
