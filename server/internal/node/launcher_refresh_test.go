package node

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestManagedLauncherRefreshOnContinue(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("legacy Claude environments are observed on macOS and Linux")
	}
	n, f, home, cwd, _ := switchNode(t, proto.StatusIdle, nil)
	original := proto.ManagedLauncher{Name: "work", Model: "glm-5", Auth: "bearer", APIKey: "old-fixture", NoProxy: "old.example.com", Env: "LAUNCHER_FIXTURE=old"}
	n.setManaged([]proto.ManagedLauncher{original})
	st, _ := n.state.get(sidA)
	st.Launcher = original.Name
	n.state.put(sidA, st)
	pid, stop := idleTerminalTestProcess(t, managedEnv(original)...)
	scannedClaude(t, n, home, pid, sidA, procEnv{Cmd: true, MCPConfig: n.agentMCPConfig()})

	updated := original
	updated.APIKey, updated.NoProxy, updated.Env = "new-fixture", "*", "LAUNCHER_FIXTURE=new"
	if _, err := request(t, n, "launchers.managed", map[string]any{"launchers": []proto.ManagedLauncher{updated}}); err != nil {
		t.Fatal(err)
	}
	quitsOn(f, "w1:p1", sidA, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidA), stop)
	comesUpWhen(f, "w1:p1", func(string) bool { return true })
	const message = "继续"
	if err := tell(t, n, message, false); err != nil {
		t.Fatal(err)
	}
	starts := typedStarts(f, "w1:p1")
	if len(starts) != 1 || !strings.Contains(starts[0], " launch work --resume "+sidA) {
		t.Fatalf("expected one resume on work, starts = %q", starts)
	}
	settings := filepath.Join(n.dir, "launcher-settings", "work-claude-settings.json")
	if !strings.Contains(starts[0], " --settings "+settings) && !strings.Contains(starts[0], " --settings "+shellQuote(settings)) {
		t.Fatalf("resume did not pass the generated settings: %q", starts[0])
	}
	got, err := readLauncherSettings(settings, cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	gotEnv, _ := got["env"].(map[string]any)
	launchEnv := n.launcherEnv(updated.Name)["env"].([]string)
	for key, want := range map[string]string{"ANTHROPIC_AUTH_TOKEN": updated.APIKey, "LAUNCHER_FIXTURE": "new", "NO_PROXY": updated.NoProxy} {
		if gotEnv[key] != want || envValue(launchEnv, key) != want {
			t.Errorf("%s was not updated in both the settings and launch environment", key)
		}
	}
	trace := f.trace("pane.send_keys", "pane.send_input", "agent.prompt")
	if len(trace) != 4 || !strings.Contains(trace[0], "ctrl+c") || !strings.Contains(trace[1], "ctrl+c") ||
		!strings.HasPrefix(trace[2], "pane.send_input") || trace[3] != call("agent.prompt", map[string]any{"target": "w1:p1", "text": message}) {
		t.Fatalf("expected quit, resume, then Continue, got %v", trace)
	}
}

func TestManagedLauncherRefreshCatalog(t *testing.T) {
	for _, change := range []string{"provider changed", "only the revision changed", "restored with older settings"} {
		t.Run(change, func(t *testing.T) {
			n, f, _, cwd, _ := switchNode(t, proto.StatusIdle, nil)
			old := proto.ManagedLauncher{Name: "work", Model: "glm-5", BaseURL: "https://old.example.com", APIKey: "old-fixture",
				Rev: "r1", Delivery: proto.DeliveryOnDemand}
			latest := old
			latest.Rev = "r2"
			if change != "only the revision changed" {
				latest.BaseURL, latest.Headers, latest.APIKey, latest.Env = "https://new.example.com", "X-Fixture: new", "new-fixture", "LAUNCHER_FIXTURE=new"
				latest.Proxy, latest.NoProxy = "http://127.0.0.1:9", "new.example.com"
			}
			h := &pullHub{answer: func(name string) (any, error) {
				if name != latest.Name {
					return nil, fmt.Errorf("unexpected launcher %q", name)
				}
				return map[string]any{"launcher": latest}, nil
			}}
			withPullHub(t, n, h)
			settings, err := n.mergeManagedSettings(old.Name, old, "")
			if err != nil {
				t.Fatal(err)
			}
			liveConfig := old
			if change == "restored with older settings" {
				liveConfig = latest // a herdr restore fetched the current environment with an older --settings file
			}
			stop := showManagedClaude(t, n, f, cwd, proto.StatusIdle, liveConfig, settings)
			// A reconnected node knows only the current catalog. The old process supplies the starting fingerprint.
			if _, err := request(t, n, "launchers.catalog", map[string]any{"launchers": []proto.ManagedLauncher{latest.Public()}}); err != nil {
				t.Fatal(err)
			}
			if h.count() != 0 || len(drove(f)) != 0 {
				t.Fatal("an on-demand catalog update touched the terminal or pulled the provider before a send")
			}
			quitsOn(f, "w1:p1", sidA, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidA), stop)
			comesUpWhen(f, "w1:p1", func(string) bool { return true })
			if err := tell(t, n, "继续", false); err != nil {
				t.Fatal(err)
			}
			if h.count() != 1 || len(f.trace("agent.prompt")) != 1 {
				t.Fatalf("expected one pull and one prompt, got %d pulls and %v", h.count(), f.trace("agent.prompt"))
			}
			if change == "only the revision changed" {
				assertNoLauncherRestart(t, f)
				return
			}
			starts := typedStarts(f, "w1:p1")
			if len(starts) != 1 || !strings.Contains(starts[0], " launch work --resume "+sidA) {
				t.Fatalf("expected one resume on work, starts = %q", starts)
			}
			settings = settingsArg(shellWords(starts[0]))
			parsed, err := readLauncherSettings(settings, cwd, n.home)
			if err != nil {
				t.Fatal(err)
			}
			env, _ := parsed["env"].(map[string]any)
			if env["ANTHROPIC_BASE_URL"] != latest.BaseURL || env["ANTHROPIC_CUSTOM_HEADERS"] != latest.Headers ||
				env["HTTPS_PROXY"] != latest.Proxy || env["NO_PROXY"] != latest.NoProxy || env["LAUNCHER_FIXTURE"] != "new" {
				t.Fatal("the resume did not use the pulled provider's settings")
			}
			showManagedClaude(t, n, f, cwd, proto.StatusIdle, latest, settings)
			f.reset()
			if err := tell(t, n, "next message", false); err != nil {
				t.Fatal(err)
			}
			assertNoLauncherRestart(t, f)
			if h.count() != 1 || len(f.trace("agent.prompt")) != 1 {
				t.Fatalf("second send should prompt once without re-pulling, got %d pulls and %v", h.count(), f.trace("agent.prompt"))
			}
		})
	}
}

func TestManagedLauncherRefreshDefersActiveSessions(t *testing.T) {
	for _, activity := range []string{"working", "background command"} {
		t.Run(activity, func(t *testing.T) {
			status := proto.StatusIdle
			var transcript func(string) []string
			if activity == "working" {
				status = proto.StatusWorking
			} else {
				transcript = func(cwd string) []string {
					ts := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
					return append(finishedTurn(cwd),
						`{"type":"assistant","uuid":"b1","timestamp":"`+ts+`","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"sleep 5","run_in_background":true}}]}}`,
						`{"type":"user","uuid":"b2","timestamp":"`+ts+`","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"Command running in background with ID: job1. Output is being written to: /tmp/tasks/job1.output"}]}}`,
						`{"type":"system","subtype":"turn_duration","uuid":"b3","parentUuid":"b2","durationMs":1000}`)
				}
			}
			n, f, home, cwd, _ := switchNode(t, status, transcript)
			original := proto.ManagedLauncher{Name: "work", Model: "glm-5", APIKey: "old-fixture"}
			stop := showManagedClaude(t, n, f, cwd, status, original, "")
			updated := original
			updated.APIKey = "new-fixture"
			n.setManaged([]proto.ManagedLauncher{updated})
			quitsOn(f, "w1:p1", sidA, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidA), stop)
			comesUpWhen(f, "w1:p1", func(string) bool { return true })
			if err := tell(t, n, "while busy", false); err != nil {
				t.Fatal(err)
			}
			assertNoLauncherRestart(t, f)
			if got := len(f.trace("agent.prompt")); got != 1 {
				t.Fatalf("busy send prompted %d times", got)
			}
			writeTranscript(t, home, cwd, sidA, time.Now().Add(-time.Hour), finishedTurn(cwd)...)
			f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
			if err := n.refreshHosts(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.reset()
			if err := tell(t, n, "now idle", false); err != nil {
				t.Fatal(err)
			}
			if starts := typedStarts(f, "w1:p1"); len(starts) != 1 || !strings.Contains(starts[0], " launch work --resume "+sidA) {
				t.Fatalf("deferred restart did not run at idle: %q", starts)
			}
		})
	}
}

func TestManagedLauncherRefreshFailedPullKeepsTheSession(t *testing.T) {
	n, f, _, cwd, _ := switchNode(t, proto.StatusIdle, nil)
	original := proto.ManagedLauncher{Name: "work", Model: "glm-5", APIKey: "old-fixture", Rev: "r1", Delivery: proto.DeliveryOnDemand}
	showManagedClaude(t, n, f, cwd, proto.StatusIdle, original, "")
	withPullHub(t, n, &pullHub{answer: func(string) (any, error) {
		return nil, &rpc.Error{Code: proto.CodeLauncherKeyUnresolved, Message: "test provider key could not be resolved"}
	}})
	updated := original.Public()
	updated.Rev = "r2"
	n.setCatalog([]proto.ManagedLauncher{updated})
	err := tell(t, n, "继续", false)
	if _, data := refusalOf(t, err); data["delivered"] != deliveredNone {
		t.Fatalf("expected definitive non-delivery, got %v", err)
	}
	if got := drove(f); len(got) != 0 {
		t.Fatalf("failed pull touched the old process: %v", got)
	}
}

func TestManagedLauncherRefreshSkipsAProviderRemovedWhileQuitting(t *testing.T) {
	n, f, _, cwd, _ := switchNode(t, proto.StatusIdle, nil)
	original := proto.ManagedLauncher{Name: "work", Model: "glm-5", APIKey: "old-fixture"}
	stop := showManagedClaude(t, n, f, cwd, proto.StatusIdle, original, "")
	updated := original
	updated.APIKey = "new-fixture"
	n.setManaged([]proto.ManagedLauncher{updated})
	quitsOn(f, "w1:p1", sidA, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidA), func() {
		stop()
		n.setManaged(nil)
	})
	comesUpWhen(f, "w1:p1", func(string) bool { return true })
	err := tell(t, n, "继续", false)
	if code, data := refusalOf(t, err); code != notDelivered || data["delivered"] != deliveredNone {
		t.Fatalf("expected definitive non-delivery, got %v", err)
	}
	if got := f.trace("pane.send_input", "agent.prompt"); len(got) != 0 {
		t.Fatalf("a removed provider let the restart type into the terminal: %v", got)
	}
}

func TestManagedLauncherProcessStampsSettingsAndLaunchConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX Claude executable")
	}
	home := launchKeyHome(t)
	n := &Node{home: home, dir: filepath.Dir(nodeclient.SocketPath()), bin: os.Args[0]}
	claude := filepath.Join(home, "bin", "claude")
	writeExecutable(t, claude, `#!/bin/sh
test "$ANTHROPIC_AUTH_TOKEN" = new-fixture || exit 1
printf '%s\n' "$PERSONA_MANAGED_LAUNCHER_CONFIG"
`)
	t.Setenv("PATH", filepath.Dir(claude))
	t.Setenv("NODE_TEST_LAUNCH", "1")
	old := proto.ManagedLauncher{Name: "work", Model: "glm-5", APIKey: "old-fixture", Auth: "bearer"}
	settings, err := n.mergeManagedSettings(old.Name, old, "")
	if err != nil {
		t.Fatal(err)
	}
	current := old
	current.APIKey = "new-fixture"
	n.setManaged([]proto.ManagedLauncher{current})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := n.listenOnce(ctx); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, n.bin, "launch", current.Name, "--resume", sidA, "--settings", settings).CombinedOutput()
	if err != nil {
		t.Fatalf("personad launch failed: %v\n%s", err, out)
	}
	want := current.Name + ":" + managedClaudeFingerprint(current) + ":" + managedClaudeFingerprint(old)
	if strings.TrimSpace(string(out)) != want {
		t.Fatalf("process does not distinguish its current env and older settings: %s", out)
	}
}

func showManagedClaude(t *testing.T, n *Node, f *fakeHerdr, cwd, status string, config proto.ManagedLauncher, settings string) func() {
	t.Helper()
	st, _ := n.state.get(sidA)
	st.Launcher = config.Name
	n.state.put(sidA, st)
	stamp := managedLaunchStamp(config.Name, managedClaudeFingerprint(config), settings, n.dir)
	pid, stop := idleTerminalTestProcess(t, managedLaunchConfigEnv+"="+stamp)
	file := filepath.Join(mkdir(t, filepath.Join(n.home, ".claude", "sessions")), fmt.Sprintf("%d.json", pid))
	writeFile(t, file, `{"sessionId":"`+sidA+`"}`)
	f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, status, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
	if err := n.refreshHosts(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.reset()
	return stop
}

func assertNoLauncherRestart(t *testing.T, f *fakeHerdr) {
	t.Helper()
	if got := f.trace("pane.send_keys", "pane.send_input"); len(got) != 0 {
		t.Fatalf("quit or resumed the running agent: %v", got)
	}
}
