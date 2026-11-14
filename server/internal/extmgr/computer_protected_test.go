package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestProtectedApp(t *testing.T) {
	for _, c := range []struct {
		id, name string
		want     bool
	}{
		{"com.apple.systempreferences", "System Settings", true},
		{"com.apple.systempreferences", "Réglages Système", true}, // by its id, in any language
		{"name:system settings", "System Settings", true},
		{"name:系统设置", "系统设置", true},
		{"name:システム設定", "システム設定", true},
		{"name:com.apple.systempreferences", "com.apple.systempreferences", true}, // a bundle id given as the name
		{"com.apple.SecurityAgent", "", true},
		{"", "UserNotificationCenter", true},
		{"com.apple.LocalAuthentication.UIAgent", "coreautha", true},
		{"com.apple.calculator", "Calculator", false},
		{"com.apple.Terminal", "Terminal", false}, // asked every time, not refused
		{"name:settings", "Settings", false},
	} {
		if got := protectedApp(approvedApp{BundleID: c.id, Name: c.name}); got != c.want {
			t.Errorf("%q %q: %v, want %v", c.id, c.name, got, c.want)
		}
	}
}

func TestUnnamedSteps(t *testing.T) {
	for _, c := range []struct {
		app, steps string
		want       bool
	}{
		{"Calculator", `[{"tool": "press_key", "args": {"key": "return"}}]`, false}, // the call's app
		{"", `[{"click": {"app": "Calculator", "name": "7"}}]`, false},
		{"", `[{"tool": "click", "args": {"app": "Calculator", "x": 1, "y": 2}}]`, false},
		{"", `[{"launch_app": {"name": "Notes"}}, {"click": {"app": "Notes"}}]`, false},
		{"", `[{"tool": "press_key", "args": {"key": "return"}}]`, true},
		{"", `[{"click": {"app": "Calculator"}}, {"press_key": {"key": "return"}}]`, true},
		{"", `[{"launch_app": {}}]`, true},
	} {
		if got := unnamedSteps(c.app, json.RawMessage(c.steps)); got != c.want {
			t.Errorf("%q %s: %v, want %v", c.app, c.steps, got, c.want)
		}
	}
}

// The assistant never operates System Settings or a permission or password prompt (#4336): a call naming one is
// refused before any card or action, app approval on or off; on Cua Driver so is any call while a prompt is on the
// screen, and, while System Settings is open, a call that could reach it (#7560): other apps still go. Steps naming
// no app go one at a time, stopping when a prompt comes up. Reading stays as it was. The rules themselves are
// protectedapps' (check_test.go); this is computer_run's use of them.
func TestComputerRunNeverOperatesProtectedApps(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	const id = "mac-protected"
	_, seen := computerMac(t, h, id)
	deskOf(h)
	if err := h.extensions.reg.SetEnabled(ctx, cuaExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	config := func(c string) {
		t.Helper()
		if err := h.extensions.reg.SetConfig(ctx, cuaExtensionID, json.RawMessage(c), "test"); err != nil {
			t.Fatal(err)
		}
	}
	config(`{"machines": ["mac-protected"]}`)
	setWindows := func(f func(runs int) string) {
		fakeNoLockMu.Lock()
		fakeWindows[id] = f
		fakeNoLockMu.Unlock()
	}
	fakeNoLockMu.Lock()
	fakeApps[id] = `{"apps": [{"name": "Calculator", "bundle_id": "com.apple.calculator", "pid": 63, "running": true},
		{"name": "Réglages Système", "bundle_id": "com.apple.systempreferences", "pid": 70, "running": true}]}`
	fakeNoLockMu.Unlock()
	t.Cleanup(func() {
		fakeNoLockMu.Lock()
		delete(fakeApps, id)
		delete(fakeWindows, id)
		fakeNoLockMu.Unlock()
	})
	run := func(args string) proto.ToolResult {
		return h.callComputerTool(ctx, "conv-protected", "computer_run", json.RawMessage(args))
	}
	acted := func(calls []computerCallSeen) (steps [][]any) {
		for _, c := range calls {
			if c.Tool == "run_script" {
				t.Fatalf("a script ran: %+v", c)
			}
			if c.Tool == "run_steps" {
				s, _ := c.Args["steps"].([]any)
				steps = append(steps, s)
			}
		}
		return steps
	}
	refused := func(what, args, want string) {
		t.Helper()
		_, before := seen()
		res := run(args)
		if !res.IsError || !strings.Contains(res.Content[0].Text, want) {
			t.Fatalf("%s: refused (%q): %+v", what, want, res)
		}
		if _, after := seen(); len(acted(after[len(before):])) != 0 {
			t.Fatalf("%s: something was done on the Mac", what)
		}
		select {
		case c := <-deskOf(h).ch:
			t.Fatalf("%s: a card to allow it: %+v", what, c.ix)
		default:
		}
	}
	const themselves = "do this themselves, on the Mac or through its live screen"

	// A card can wait for minutes: a prompt that came up meanwhile is looked for again after the answer.
	calcAsked := make(chan proto.ToolResult, 1)
	go func() {
		calcAsked <- run(`{"machine": "mac-protected", "steps": [{"click": {"app": "Calculator", "name": "7"}}]}`)
	}()
	select {
	case c := <-deskOf(h).ch:
		setWindows(func(int) string {
			return `{"windows": [{"app_name": "SecurityAgent", "pid": 98, "is_on_screen": true}]}`
		})
		yes := true
		c.answer <- proto.Resolution{Allow: &yes}
	case <-time.After(5 * time.Second):
		t.Fatal("no card for Calculator")
	}
	if res := <-calcAsked; !res.IsError || !strings.Contains(res.Content[0].Text, "prompt (SecurityAgent) is on its screen") {
		t.Fatalf("allowed on the phone, then refused for the prompt that came up meanwhile: %+v", res)
	}
	if _, calls := seen(); len(acted(calls)) != 0 {
		t.Fatal("nothing ran")
	}
	setWindows(func(int) string { return `{"windows": []}` })

	for _, args := range []string{
		`{"machine": "mac-protected", "steps": [{"click": {"app": "System Settings", "name": "Screen & System Audio Recording"}}]}`,
		`{"machine": "mac-protected", "app": "Réglages Système", "steps": [{"tool": "press_key", "args": {"key": "space"}}]}`,
		`{"machine": "mac-protected", "steps": [{"launch_app": {"bundle_id": "com.apple.systempreferences"}}]}`,
		`{"machine": "mac-protected", "script": "const s = await cua.getApp(\"系统设置\"); return 1"}`,
		`{"machine": "mac-protected", "script": "await cua.launch('System Settings'); return 1"}`,
		`{"machine": "mac-protected", "steps": [{"click": {"app": "SecurityAgent", "name": "OK"}}]}`,
	} {
		refused(args, args, themselves)
	}
	refused("a script naming its apps by expression", `{"machine": "mac-protected", "script": "const s = await cua.getApp(n); return 1"}`, "cua.getApp")

	config(`{"machines": ["mac-protected"], "app_approval": false}`)
	refused("app approval off", `{"machine": "mac-protected", "steps": [{"click": {"app": "System Settings", "name": "x"}}]}`, themselves)

	calc := `{"machine": "mac-protected", "steps": [{"click": {"app": "Calculator", "name": "7"}}]}`
	settingsOpen := func(int) string {
		return `{"windows": [{"app_name": "Réglages Système", "pid": 70, "window_id": 7, "is_on_screen": true, "z_index": 9,
			"bounds": {"x": 100, "y": 100, "width": 600, "height": 500}},
			{"app_name": "Calculator", "pid": 63, "window_id": 3, "is_on_screen": true, "z_index": 4,
			"bounds": {"x": 0, "y": 0, "width": 300, "height": 400}}]}`
	}
	setWindows(settingsOpen)
	if res := run(calc); res.IsError {
		t.Fatalf("System Settings open behind: Calculator still goes: %+v", res)
	}
	if res := run(`{"machine": "mac-protected", "steps": [{"click": {"element_token": "s00000063:5"}}]}`); res.IsError {
		t.Fatalf("System Settings open: another window's element token goes: %+v", res)
	}
	refused("one of System Settings' own element tokens", `{"machine": "mac-protected", "steps": [{"click": {"element_token": "s00000070:5"}}]}`, themselves)
	refused("a point where System Settings is on top", `{"machine": "mac-protected", "steps": [{"click": {"app": "Calculator", "x": 250, "y": 300}}]}`,
		"Réglages Système is on top where this would click")
	refused("a script while it is open", `{"machine": "mac-protected", "script": "await cua.getApp(\"Calculator\"); return 1"}`, "use steps instead")
	setWindows(func(int) string {
		return `{"windows": [{"app_name": "UserNotificationCenter", "pid": 99, "is_on_screen": true}]}`
	})
	refused("a permission prompt on the screen, by its process name", calc, "prompt (UserNotificationCenter) is on its screen")
	setWindows(func(int) string { return `{"oops": true}` })
	refused("the windows unreadable", calc, "could not be read")
	setWindows(func(int) string {
		return `{"windows": [{"app_name": "Réglages Système", "pid": 70, "is_on_screen": false}]}`
	})
	if res := run(calc); res.IsError {
		t.Fatalf("System Settings off the screen is not in the way: %+v", res)
	}

	keys := `{"machine": "mac-protected", "steps": [{"tool": "press_key", "args": {"key": "return"}}, {"tool": "press_key", "args": {"key": "space"}}]}`
	_, sofar := seen()
	base := len(acted(sofar))
	setWindows(func(runs int) string {
		if runs > base { // the first key of this call brought a prompt up
			return `{"windows": [{"app_name": "UserNotificationCenter", "pid": 99, "is_on_screen": true}]}`
		}
		return `{"windows": []}`
	})
	_, before := seen()
	res := run(keys)
	_, after := seen()
	if !res.IsError || !strings.Contains(res.Content[0].Text, "Stopped before step 2 of 2") || !strings.Contains(res.Content[0].Text, "answer it themselves") {
		t.Fatalf("a prompt the first key brought up stops the second: %+v", res)
	}
	if steps := acted(after[len(before):]); len(steps) != 1 || len(steps[0]) != 1 {
		t.Fatalf("one step ran, alone: %v", steps)
	}
	setWindows(func(int) string { return `{"windows": []}` })
	_, before = seen()
	res = run(keys)
	_, after = seen()
	if res.IsError || !strings.Contains(res.Content[0].Text, `"one_by_one":true`) {
		t.Fatalf("steps naming no app run one at a time: %+v", res)
	}
	if steps := acted(after[len(before):]); len(steps) != 2 || len(steps[0]) != 1 || len(steps[1]) != 1 {
		t.Fatalf("each step alone: %v", steps)
	}

	if res := h.callComputerTool(ctx, "conv-protected", "computer_state", json.RawMessage(`{"machine": "mac-protected", "app": "System Settings"}`)); res.IsError {
		t.Fatalf("reading System Settings is not refused: %+v", res)
	}
}

// LCU asks before it touches an app: for System Settings or a prompt the answer is no, with no card (#4336).
func TestLCUElicitationRefusesProtectedApps(t *testing.T) {
	h, _ := extensionTestHub(t)
	_, _ = computerMac(t, h, "mac-lcu-protected")
	mac := h.machineByName("mac-lcu-protected")
	deskOf(h)
	for _, app := range []string{`"com.apple.systempreferences"},"tool_params_display":[{"name":"app","value":"System Settings"}]`,
		`"com.apple.UserNotificationCenter"},"tool_params_display":[{"name":"app","value":"UserNotificationCenter"}]`} {
		raw := json.RawMessage(`{"conversation":"c","params":{"mode":"form","message":"Allow Computer Use to use this app?","requestedSchema":{"type":"object","properties":{}},` +
			`"_meta":{"codex_approval_kind":"mcp_tool_call","connector_id":"computer-use","tool_params":{"app":` + app + `,"persist":["always","session"]}}}`)
		res, err := h.answerComputerElicit(context.Background(), mac, raw)
		if err != nil || res["action"] != "decline" {
			t.Fatalf("%s: declined: %v %v", app, res, err)
		}
		select {
		case c := <-deskOf(h).ch:
			t.Fatalf("%s: a card: %+v", app, c.ix)
		default:
		}
	}
}
