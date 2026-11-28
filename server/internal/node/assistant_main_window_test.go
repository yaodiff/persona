package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The main conversation on a managed launcher is held to the window the manager sends with it (#7314) when the
// launcher declares a larger one: Claude's compact window and Codex's limit, with the idle /compact at 75% of it. A
// smaller declared window, another conversation (the manager sends it no window; a leader conversation's is in
// assistant_lead_window_test.go), a launcher Persona does not manage, nothing declared and a window of 0 (an older
// manager, or the setting at 0) keep what the launcher declares, as before.
func TestAssistantMainCompactWindow(t *testing.T) {
	fakeClaudeHome(t, "exit 0\n")
	t.Setenv("CODEX_HOME", t.TempDir())
	rc := "function testbig { export CLAUDE_CODE_AUTO_COMPACT_WINDOW=1000000 CLAUDE_CODE_MAX_CONTEXT_TOKENS=1000000; command claude \"$@\"; }\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".zshrc"), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}
	big := []proto.ProviderModel{{ID: "big", ContextWindow: 1_000_000}}
	n := codexTestNode(t)
	n.dir = t.TempDir()
	n.setManaged([]proto.ManagedLauncher{
		{Name: "gw1m", Model: "big", Models: big},
		{Name: "gw200", Model: "big", Models: big, CompactWindow: 200_000},
		{Name: "gwbare", Model: "m"},
		{Name: "codex1m", Agent: "codex", Model: "big", Models: big},
	})
	cwd := t.TempDir()
	for _, tt := range []struct {
		name, launcher  string
		main            bool
		limit           int
		env             string
		codex, idle, at int // at: the window the idle threshold follows once Claude Code reports 1M
	}{
		{"main, managed 1M", "gw1m", true, 400_000, "CLAUDE_CODE_AUTO_COMPACT_WINDOW=400000 CLAUDE_CODE_MAX_CONTEXT_TOKENS=1000000", 400_000, 300_000, 300_000},
		{"main, managed 1M, window 0", "gw1m", true, 0, "CLAUDE_CODE_AUTO_COMPACT_WINDOW=1000000 CLAUDE_CODE_MAX_CONTEXT_TOKENS=1000000", 1_000_000, 750_000, 750_000},
		{"another conversation", "gw1m", false, 0, "CLAUDE_CODE_AUTO_COMPACT_WINDOW=1000000 CLAUDE_CODE_MAX_CONTEXT_TOKENS=1000000", 1_000_000, 750_000, 750_000},
		{"declared window below it", "gw200", true, 400_000, "CLAUDE_CODE_AUTO_COMPACT_WINDOW=200000 CLAUDE_CODE_MAX_CONTEXT_TOKENS=1000000", 200_000, 150_000, 150_000},
		{"nothing declared", "gwbare", true, 400_000, "", 0, 150_000, 750_000},
		{"main, managed Codex with a 1M model", "codex1m", true, 400_000, "CLAUDE_CODE_AUTO_COMPACT_WINDOW=400000 CLAUDE_CODE_MAX_CONTEXT_TOKENS=1000000", 400_000, 300_000, 300_000},
		{"another Codex conversation", "codex1m", false, 0, "CLAUDE_CODE_MAX_CONTEXT_TOKENS=1000000", 0, 750_000, 750_000},
		{"shell launcher declaring 1M", "testbig", true, 400_000, "", 0, 750_000, 750_000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := n.assistantCompactionPolicy(tt.launcher, cwd, "", modelExecution{IsMain: tt.main, MainCompactWindow: tt.limit})
			if got := strings.Join(c.env(), " "); got != tt.env || c.codexLimit() != tt.codex || c.idle() != tt.idle {
				t.Fatalf("policy %+v: env %q codex %d idle %d, want %q %d %d", c, got, c.codexLimit(), c.idle(), tt.env, tt.codex, tt.idle)
			}
			args := strings.Join(codexArgs("personad", "test", c.codexLimit()), " ")
			if tt.codex > 0 && !strings.Contains(args, "model_auto_compact_token_limit="+strconv.Itoa(tt.codex)) {
				t.Fatalf("codex args %q, want limit %d", args, tt.codex)
			}
			p := &assistantProc{compactIdleAt: c.idle(), declaredWindow: c.declared()}
			p.mu.Lock()
			p.followWindow(1_000_000) // what Claude Code reports for a 1M model at the end of a turn
			at := p.compactIdleAt
			p.mu.Unlock()
			if at != tt.at {
				t.Fatalf("idle threshold after the reported window: %d, want %d", at, tt.at)
			}
		})
	}
}

// The window is part of what a process runs: one started with another window is not reused. 0 leaves the
// fingerprint as it was before the window existed.
func TestAssistantMainCompactWindowFingerprint(t *testing.T) {
	n := assistantNode(t)
	n.setManaged([]proto.ManagedLauncher{{Name: "gw1m", Model: "big", Models: []proto.ProviderModel{{ID: "big", ContextWindow: 1_000_000}}}})
	capped := n.executionFingerprint("gw1m", modelExecution{IsMain: true, MainCompactWindow: 400_000})
	other := n.executionFingerprint("gw1m", modelExecution{IsMain: true, MainCompactWindow: 300_000})
	uncapped := n.executionFingerprint("gw1m", modelExecution{IsMain: true})
	if capped == uncapped || capped == other {
		t.Fatal("the fingerprint does not follow the window")
	}
	if raw, _ := json.Marshal(modelExecution{IsMain: true}); strings.Contains(string(raw), "MainCompactWindow") {
		t.Fatalf("0 changes the fingerprint of a process from before: %s", raw)
	}
}

// End to end on a managed launcher: the main conversation starts with the window and keeps it for a start with no
// options (the conversation's record, which a manual compaction uses), and a message with another window starts it
// again on that one.
func TestAssistantMainCompactWindowStart(t *testing.T) {
	f := cliTest(t)
	f.fakeLaunch(t)
	f.n.setManaged([]proto.ManagedLauncher{{Name: "gw", Model: "big", Models: []proto.ProviderModel{{ID: "big", ContextWindow: 1_000_000}}}})
	execution := cliExec()
	execution.UseEffort, execution.IsMain, execution.MainCompactWindow = true, true, 400_000
	ctx := context.Background()
	id, err := f.n.assistantSend(ctx, "", t.TempDir(), "gw", "hello", nil, "", "", execution)
	if err != nil {
		t.Fatal(err)
	}
	first := f.proc(id)
	if env := first.cmd.Env; envValue(env, "CLAUDE_CODE_AUTO_COMPACT_WINDOW") != "400000" || envValue(env, "CLAUDE_CODE_MAX_CONTEXT_TOKENS") != "1000000" || first.compactIdleAt != 300_000 {
		t.Fatalf("main start: %v, idle %d", env, first.compactIdleAt)
	}
	if st, _ := f.n.state.get(id); !st.IsMain || st.MainCompactWindow != 400_000 {
		t.Fatalf("recorded %+v", st)
	}
	f.idle(t, id)
	if _, err := f.n.assistantSend(ctx, id, "", "", "again", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	if f.proc(id) != first {
		t.Fatal("a send with no options restarted the main conversation without its window")
	}
	f.idle(t, id)
	execution.MainCompactWindow = 0
	if _, err := f.n.assistantSend(ctx, id, "", "gw", "off", nil, "", "", execution); err != nil {
		t.Fatal(err)
	}
	if p := f.proc(id); p == first || p.compactIdleAt != 750_000 {
		t.Fatalf("window 0: same process %v, idle %d", p == first, p.compactIdleAt)
	}
	if st, _ := f.n.state.get(id); st.MainCompactWindow != 0 {
		t.Fatalf("recorded window %d", st.MainCompactWindow)
	}
}

// Through the node's RPC (Node.handle): assistant.send and session.send carry main_compact_window into the main
// conversation's process. A session.send with the same window reuses it, and one without (an older manager) starts it
// again on the launcher's window. On a launcher Persona does not manage the window changes nothing, so a manager that
// starts sending it restarts nothing there either.
func TestAssistantMainCompactWindowRPC(t *testing.T) {
	f := cliTest(t)
	f.fakeLaunch(t)
	f.n.setManaged([]proto.ManagedLauncher{{Name: "gw", Model: "big", Models: []proto.ProviderModel{{ID: "big", ContextWindow: 1_000_000}}}})
	send := func(method string, p map[string]any) string {
		t.Helper()
		p["text"], p["is_main"], p["model_fingerprint"] = "hello", true, "fp"
		raw, err := nodeCall(t, f.n, method, p)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		var out struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(raw, &out)
		return out.SessionID
	}
	id := send("assistant.send", map[string]any{"launcher": "gw", "cwd": t.TempDir(), "main_compact_window": 400_000})
	first := f.proc(id)
	if env := first.cmd.Env; envValue(env, "CLAUDE_CODE_AUTO_COMPACT_WINDOW") != "400000" || first.compactIdleAt != 300_000 {
		t.Fatalf("assistant.send: %v, idle %d", env, first.compactIdleAt)
	}
	f.idle(t, id)
	send("session.send", map[string]any{"session_id": id, "launcher": "gw", "main_compact_window": 400_000})
	if f.proc(id) != first {
		t.Fatal("session.send with the same window restarted the main conversation")
	}
	f.idle(t, id)
	send("session.send", map[string]any{"session_id": id, "launcher": "gw"})
	if p := f.proc(id); p == first || p.compactIdleAt != 750_000 {
		t.Fatalf("session.send without a window: same process %v, idle %d", p == first, p.compactIdleAt)
	}

	shell := send("assistant.send", map[string]any{"launcher": "claude", "cwd": t.TempDir()})
	f.idle(t, shell)
	plain := f.proc(shell)
	send("session.send", map[string]any{"session_id": shell, "launcher": "claude", "main_compact_window": 400_000})
	if f.proc(shell) != plain {
		t.Fatal("the window restarted a main conversation on a launcher Persona does not manage")
	}
	if st, _ := f.n.state.get(shell); !st.IsMain || st.MainCompactWindow != 0 {
		t.Fatalf("recorded %+v", st)
	}
}

// The main chat's warm spare (assistant.warm with is_main, #8365) starts with the window the main conversation's start
// carries, only on a managed launcher, so that start adopts it instead of starting another process.
func TestAssistantMainCompactWindowWarmSpare(t *testing.T) {
	fakeClaudeHome(t, "echo '{\"type\":\"system\",\"subtype\":\"init\"}'\nwhile read line; do :; done\n")
	n := codexTestNode(t)
	n.dir = t.TempDir() // a managed launcher's settings file goes there
	withFakeHub(t, n)
	n.setManaged([]proto.ManagedLauncher{{Name: "gw", Model: "big", Models: []proto.ProviderModel{{ID: "big", ContextWindow: 1_000_000}}}})
	for _, tt := range []struct {
		launcher string
		want     int
	}{{"gw", 400_000}, {"claude", 0}} {
		t.Run(tt.launcher, func(t *testing.T) {
			cwd := t.TempDir()
			if _, err := nodeCall(t, n, "assistant.warm", map[string]any{"launcher": tt.launcher, "cwd": cwd, "is_main": true, "main_compact_window": 400_000}); err != nil {
				t.Fatal(err)
			}
			n.mu.Lock()
			spare := n.spare
			n.mu.Unlock()
			if spare == nil || !spare.model.IsMain || spare.model.MainCompactWindow != tt.want {
				t.Fatalf("spare %+v, want window %d", spare, tt.want)
			}
			t.Cleanup(spare.stop)
			// What assistantSend runs a new main conversation's first message with.
			start := modelExecution{IsMain: true, MainCompactWindow: n.mainWindowOn(tt.launcher, 400_000), ConfigDir: spare.model.ConfigDir}
			if n.adoptSpare(cwd, tt.launcher, start) != spare {
				t.Fatal("the main conversation's start did not adopt the spare warmed for it")
			}
		})
	}
}
