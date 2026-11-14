package node

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/protectedapps"
)

// fakeCuaLook says what the gate's look at the Mac finds (cua_gate.go), on a Mac, and counts the looks: list_apps
// answers apps, list_windows what windows says now, and get_window_state traces element tokens: a snapshot in
// mine is System Settings' window's, any other another window's.
func fakeCuaLook(t *testing.T, apps string, windows func() string) *int {
	t.Helper()
	looks := 0
	oldRead, oldOS := cuaRead, cuaGateOS
	cuaGateOS = "darwin"
	cuaRead = func(_ context.Context, tool string, args map[string]any) (json.RawMessage, bool) {
		looks++
		switch tool {
		case "list_apps":
			return json.RawMessage(apps), true
		case "list_windows":
			return json.RawMessage(windows()), true
		case "get_window_state":
			if args["since"] == "s00000070" {
				return json.RawMessage(`{"since_status": "no_change"}`), true
			}
			return json.RawMessage(`{"since_status": "other_window"}`), true
		}
		return nil, false
	}
	t.Cleanup(func() { cuaRead, cuaGateOS = oldRead, oldOS })
	return &looks
}

const gateApps = `{"apps": [{"name": "Calculator", "bundle_id": "com.apple.calculator", "pid": 63, "running": true},
	{"name": "Réglages Système", "bundle_id": "com.apple.systempreferences", "pid": 70, "running": true}]}`

func TestCuaToolOf(t *testing.T) {
	for name, want := range map[string]string{
		"mcp__cua-driver__click": "click", "mcp__cua__run_script": "run_script", "mcp__my_CUA_mac__press_key": "press_key",
		"mcp__persona__click": "", "mcp__computer__click": "", "Bash": "", "mcp__cua-driver": "",
	} {
		if got := cuaToolOf(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

func TestCuaCalls(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		want string // tool:args;…
		ok   bool
	}{
		{`cua-driver call click '{"pid": 70}'`, `click:{"pid": 70}`, true},
		{`~/.local/bin/cua-driver call --screenshot-out-file /tmp/x press_key '{"key":"return"}'`, `press_key:{"key":"return"}`, true},
		{`FOO=1 env -i cua-driver call list_windows`, `list_windows:`, true},
		{`echo '{}' | cua-driver call click`, `click:`, true},
		{`bash -lc "cua-driver call click '{\"pid\": 1}'"`, `click:{"pid": 1}`, true},
		{`cua-driver status && cua-driver permissions grant`, ``, true},
		// A mention as an argument is no call.
		{`grep -rn cua-driver docs | head`, ``, true},
		{`git commit -m "make cua-driver call click safer"`, ``, true},
		{`ls`, ``, true},
		// What cannot be followed is refused only when it looks like a call.
		{"cat <<EOF | cua-driver call click\n{}\nEOF", ``, false},
		{"cat <<EOF\nabout cua-driver\nEOF", ``, true},
	} {
		calls, ok := cuaCalls(c.cmd)
		var got []string
		for _, x := range calls {
			got = append(got, x.tool+":"+string(x.args))
		}
		if strings.Join(got, ";") != c.want || ok != c.ok {
			t.Errorf("%q: %v %v, want %q %v", c.cmd, got, ok, c.want, c.ok)
		}
	}
}

// An Agent's own Cua Driver gets #4336's rules (#4349, #7560): a call naming System Settings or a prompt, aimed at one
// by pid or with one of its element tokens, or made while a prompt is on the screen, is refused; System Settings open
// on the screen stops only what could reach it. So is a script whose apps cannot be read, or a call whose arguments
// could not be read; reads pass without a look; elsewhere than a Mac everything passes. The rules themselves are
// protectedapps' (check_test.go); this is the gate's use of them.
func TestCuaVerdict(t *testing.T) {
	ctx := context.Background()
	onScreen := `{"windows": []}`
	looks := fakeCuaLook(t, gateApps, func() string { return onScreen })
	verdict := func(tool, args string) string {
		var raw json.RawMessage
		if args != "" {
			raw = json.RawMessage(args)
		}
		return cuaVerdict(ctx, tool, raw)
	}
	const themselves = "do this themselves, on the Mac or through its live screen"

	*looks = 0
	if v := verdict("list_windows", `{}`); v != "" || *looks != 0 {
		t.Fatalf("a read passes, without a look: %q %d", v, *looks)
	}
	if v := verdict("click", `{"app": "Calculator", "element_token": "s1:5"}`); v != "" {
		t.Fatalf("Calculator: %q", v)
	}
	for _, c := range []struct{ tool, args string }{
		{"click", `{"app": "System Settings", "element_token": "s1:5"}`},
		{"launch_app", `{"bundle_id": "com.apple.systempreferences"}`},
		{"type_text", `{"pid": 70, "window_id": 9, "text": "x"}`}, // by its pid, whatever it is called
		{"click", `{"name": "Réglages Système"}`},                 // by its name on this Mac, through the list
		{"run_steps", `{"steps": [{"click": {"app": "SecurityAgent", "name": "OK"}}]}`},
		{"run_script", `{"script": "await cua.launch('システム設定')"}`},
	} {
		if v := verdict(c.tool, c.args); !strings.Contains(v, themselves) {
			t.Errorf("%s %s: refused, the user told to do it: %q", c.tool, c.args, v)
		}
	}
	if v := verdict("run_script", `{"script": "const a = await cua.getApp(name)"}`); v != protectedapps.UnreadableScript {
		t.Errorf("an unreadable script: %q", v)
	}
	if v := verdict("click", ""); !strings.Contains(v, "could not read") {
		t.Errorf("arguments not read: %q", v)
	}

	onScreen = `{"windows": [{"app_name": "UserNotificationCenter", "pid": 99, "is_on_screen": true}]}`
	if v := verdict("press_key", `{"app": "Calculator", "key": "return"}`); !strings.Contains(v, "prompt (UserNotificationCenter) is on its screen") {
		t.Errorf("a prompt on the screen stops any action: %q", v)
	}
	onScreen = `{"windows": [{"app_name": "Réglages Système", "pid": 70, "window_id": 7, "is_on_screen": true, "z_index": 3,
		"bounds": {"x": 0, "y": 0, "width": 500, "height": 400}}]}`
	for _, args := range []string{`{"app": "Calculator", "key": "return"}`, `{"pid": 63, "key": "return"}`, `{"element_token": "s00000063:4", "key": "return"}`} {
		if v := verdict("press_key", args); v != "" {
			t.Errorf("System Settings open behind: Calculator still goes (%s): %q", args, v)
		}
	}
	if v := verdict("click", `{"element_token": "s00000070:4"}`); !strings.Contains(v, themselves) {
		t.Errorf("one of System Settings' own tokens: %q", v)
	}
	if v := verdict("click", `{"scope": "desktop", "x": 200, "y": 150}`); !strings.Contains(v, "Réglages Système is on top where this would click") {
		t.Errorf("a point on System Settings: %q", v)
	}
	onScreen = `{"windows": [{"app_name": "Réglages Système", "pid": 70, "is_on_screen": false}]}`
	if v := verdict("press_key", `{"app": "Calculator", "key": "return"}`); v != "" {
		t.Errorf("off the screen it is not in the way: %q", v)
	}
	onScreen = `{"oops": 1}`
	if v := verdict("press_key", `{"app": "Calculator", "key": "return"}`); v != protectedapps.Unchecked {
		t.Errorf("the windows unreadable: %q", v)
	}

	cuaGateOS = "linux"
	if v := verdict("click", `{"app": "System Settings"}`); v != "" {
		t.Errorf("not a Mac: nothing of macOS's to protect: %q", v)
	}
}

func TestCuaBashVerdict(t *testing.T) {
	fakeCuaLook(t, gateApps, func() string { return `{"windows": []}` })
	ctx := context.Background()
	if v := cuaBashVerdict(ctx, `cua-driver call launch_app '{"bundle_id": "com.apple.systempreferences"}'`); !strings.Contains(v, "Refused on this Mac") {
		t.Errorf("launching System Settings: %q", v)
	}
	if v := cuaBashVerdict(ctx, `grep -rn cua-driver . && cua-driver call list_apps`); v != "" {
		t.Errorf("a grep and a read: %q", v)
	}
	if v := cuaBashVerdict(ctx, "cat <<EOF | cua-driver call click\n{}\nEOF"); !strings.Contains(v, "could not follow") {
		t.Errorf("a call that cannot be followed: %q", v)
	}
}

// The hook itself: a Cua Driver MCP tool and a Bash cua-driver call of an Agent's session are denied with the reason;
// anything else is left to the usual flow.
func TestCuaGateHook(t *testing.T) {
	n := codexTestNode(t)
	withFakeHub(t, n)
	fakeCuaLook(t, gateApps, func() string { return `{"windows": []}` })
	const agent = "33333333-3333-4333-8333-333333333333"
	hook := func(tool, input string) string {
		t.Helper()
		raw, _ := json.Marshal(hookInput{HookEventName: "PreToolUse", SessionID: agent, ToolName: tool, ToolUseID: "toolu_1",
			ToolInput: json.RawMessage(input)})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := n.handleHook(ctx, sockRequest{Input: raw, Attempt: "a1"})
		if err != nil {
			t.Fatal(err)
		}
		if out == nil {
			return ""
		}
		b, _ := json.Marshal(out)
		return string(b)
	}
	if out := hook("mcp__cua-driver__click", `{"app": "System Settings", "element_token": "s1:5"}`); !strings.Contains(out, `"permissionDecision":"deny"`) ||
		!strings.Contains(out, "do this themselves") {
		t.Fatalf("denied, with the reason: %s", out)
	}
	if out := hook("mcp__cua-driver__click", `{"app": "Calculator", "element_token": "s1:5"}`); out != "" {
		t.Fatalf("Calculator: left to the usual flow: %s", out)
	}
	if out := hook("Bash", `{"command": "cua-driver call click '{\"pid\": 70}'"}`); !strings.Contains(out, `"permissionDecision":"deny"`) {
		t.Fatalf("a Bash cua-driver call at System Settings' pid: %s", out)
	}
	if out := hook("Bash", `{"command": "ls"}`); out != "" {
		t.Fatalf("an Agent's other Bash: nothing: %s", out)
	}
}

func TestCuaOnlyHookTakesOnlyCuaDriverCommands(t *testing.T) {
	// Restored after RunHook sets it: left behind, the later tests' node.sock would sit under a removed temporary
	// directory, past macOS's Unix socket path limit (#4581).
	t.Setenv("PERSONA_HOME", t.TempDir())
	for input, want := range map[string]bool{
		`{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": {"command": "cua-driver call click '{}'"}}`: true,
		`{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": {"command": "ls -la"}}`:                     false,
		`{"hook_event_name": "PreToolUse", "tool_name": "Read", "tool_input": {"file_path": "cua-driver"}}`:               false,
	} {
		var out strings.Builder
		start := time.Now()
		// No node is there to answer: a command that is not the gate's returns at once, printing nothing; one that is
		// would try the node (and, finding none, print nothing too).
		nodeclient.RunHook(context.Background(), []string{nodeclient.HomeFlag, t.TempDir(), nodeclient.CuaOnlyFlag}, strings.NewReader(input), &out)
		if out.Len() != 0 {
			t.Errorf("%s: printed %q", input, out.String())
		}
		if !want && time.Since(start) > time.Second {
			t.Errorf("%s: not the gate's, yet it took %v", input, time.Since(start))
		}
	}
}
