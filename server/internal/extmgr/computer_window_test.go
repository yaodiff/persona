package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// win is one list_windows record for the tests (all names and titles are placeholders).
func win(pid, id int, app, title string, w, h, z int, current bool) string {
	return fmt.Sprintf(`{"window_id": %d, "pid": %d, "app_name": %q, "title": %q, "bounds": {"x": 0, "y": 0, "width": %d, "height": %d},
		"layer": 0, "z_index": %d, "is_on_screen": %v, "on_current_space": %v}`, id, pid, app, title, w, h, z, current, current)
}

func windowsJSON(ws ...string) string { return `{"windows": [` + strings.Join(ws, ", ") + `]}` }

func parseWindows(t *testing.T, raw string) []cuaWindow {
	t.Helper()
	ws, ok := cuaWindows(json.RawMessage(raw))
	if !ok {
		t.Fatalf("not a list: %s", raw)
	}
	return ws
}

// Two processes of one app (#7591): another copy with its own profile, whose small untitled window and a titled one
// are stacked over the user's own. Stacking does not choose between them; a titled window, the current Space and
// the app active now do; what is left goes back as candidates.
func TestPickWindowBetweenProcesses(t *testing.T) {
	const edge = "Example Browser"
	other := []string{win(2, 21, edge, "", 500, 336, 131, true), win(2, 22, edge, "Other profile", 1200, 916, 128, true)}
	mine := win(1, 11, edge, "Home", 1920, 960, 34, true)
	none := func(int) bool { return false }

	pick := pickWindow(parseWindows(t, windowsJSON(append(other, mine)...)), edge, none)
	if pick.Processes != 2 || len(pick.Candidates) != 2 || pick.PID != 0 {
		t.Fatalf("both have titled windows on this Space, neither app is active: the model chooses: %+v", pick)
	}
	if c := pick.Candidates; c[0].PID != 1 || c[0].WindowID != 11 || c[0].Title != "Home" || c[0].Width != 1920 ||
		c[1].PID != 2 || c[1].WindowID != 22 {
		t.Fatalf("each process's best window, its titled one: %+v", c)
	}
	if pick := pickWindow(parseWindows(t, windowsJSON(append(other, mine)...)), edge, func(pid int) bool { return pid == 1 }); pick.PID != 1 || pick.WindowID != 11 {
		t.Fatalf("the app active now: %+v", pick)
	}
	// As first reported: the other copy had only its small untitled window.
	if pick := pickWindow(parseWindows(t, windowsJSON(other[0], mine)), edge, none); pick.PID != 1 || pick.WindowID != 11 || pick.Processes != 2 {
		t.Fatalf("a titled window over an untitled one: %+v", pick)
	}
	elsewhere := win(1, 11, edge, "Home", 1920, 960, 34, false)
	if pick := pickWindow(parseWindows(t, windowsJSON(other[1], elsewhere)), edge, none); pick.PID != 2 {
		t.Fatalf("the current Space over another: %+v", pick)
	}
}

func TestPickWindowWithinOneProcess(t *testing.T) {
	ws := parseWindows(t, windowsJSON(
		win(7, 1, "Notes", "", 300, 200, 90, true),
		win(7, 2, "Notes", "Shopping", 800, 600, 50, true),
		win(7, 3, "Notes", "Older", 800, 600, 40, true),
		`{"window_id": 4, "pid": 7, "app_name": "Notes", "title": "Menu", "layer": 101, "z_index": 99, "is_on_screen": true, "on_current_space": true}`,
		win(8, 5, "Notes Helper Pro", "Elsewhere", 800, 600, 95, true), // matches "Notes" only as part of its name
	))
	pick := pickWindow(ws, "notes", nil)
	if pick.Processes != 1 || pick.PID != 7 || pick.WindowID != 2 {
		t.Fatalf("its titled window in front, not a menu's layer, not another app containing the name: %+v", pick)
	}
	// Named by bundle id: list_windows gives that app's windows only, and no name equals the id.
	byBundle := parseWindows(t, windowsJSON(win(8, 5, "Notes Helper Pro", "Elsewhere", 800, 600, 95, true)))
	if pick := pickWindow(byBundle, "com.example.notes-helper", nil); pick.Processes != 1 || pick.PID != 8 || pick.WindowID != 5 {
		t.Fatalf("no window names the app whole: what list_windows gave: %+v", pick)
	}
	if pick := pickWindow(nil, "Notes", nil); pick.Processes != 0 || pick.WindowID != 0 {
		t.Fatalf("no window: %+v", pick)
	}
}

func TestStepTargets(t *testing.T) {
	steps := json.RawMessage(`[{"click": {"app": "Example Browser", "role": "button", "name": "Go"}},
		{"tool": "type_text", "args": {"app": "example browser", "text": "x"}}, {"launch_app": {"name": "Calculator"}},
		{"press_key": {"key": "return"}, "expect": [{"app": "Mail", "text": "Sent"}]}, {"tool": "click", "args": {"app": "Notes"}}]`)
	if got := strings.Join(stepTargets(steps), ","); got != "Example Browser,Notes" {
		t.Fatalf("each step's own target, case aside, not what launch_app opens or a check: %s", got)
	}
}

// Through the tools (#7591): an app with two processes is not guessed at; pid and window_id choose, stand for their
// app in the checks, and go to run_steps as its default window.
func TestComputerToolsWithSeveralProcesses(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	_, seen := computerMac(t, h, "mac")
	if err := h.extensions.reg.SetEnabled(ctx, cuaExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, cuaExtensionID, json.RawMessage(`{"machines": ["mac"], "app_approval": false}`), "test"); err != nil {
		t.Fatal(err)
	}
	const edge = "Example Browser"
	screen := windowsJSON(win(2, 21, edge, "", 500, 336, 131, true), win(2, 22, edge, "Other profile", 1200, 916, 128, true),
		win(1, 11, edge, "Home", 1920, 960, 34, true))
	apps := `{"apps": [{"name": "Example Browser", "bundle_id": "com.example.browser", "pid": 1, "running": true, "active": false},
		{"name": "Example Browser", "bundle_id": "com.example.browser", "pid": 2, "running": true, "active": false},
		{"name": "System Settings", "bundle_id": "com.apple.systempreferences", "pid": 9, "running": true}]}`
	fakeNoLockMu.Lock()
	fakeWindows["mac"] = func(int) string { return screen }
	fakeApps["mac"] = apps
	fakeNoLockMu.Unlock()
	t.Cleanup(func() {
		fakeNoLockMu.Lock()
		delete(fakeWindows, "mac")
		delete(fakeApps, "mac")
		fakeNoLockMu.Unlock()
	})
	tools := func(from int) (out []string) {
		_, calls := seen()
		for _, c := range calls[from:] {
			out = append(out, c.Tool)
		}
		return out
	}
	call := func(name, args string) (string, bool, []computerCallSeen) {
		_, before := seen()
		res := h.callComputerTool(ctx, "conv1234-5678", name, json.RawMessage(args))
		_, after := seen()
		return res.Content[0].Text, res.IsError, after[len(before):]
	}

	text, isErr, calls := call("computer_state", `{"machine": "mac", "app": "Example Browser"}`)
	if !isErr || !strings.Contains(text, `"ambiguous":true`) || !strings.Contains(text, `"pid":1`) || !strings.Contains(text, `"title":"Home"`) ||
		!strings.Contains(text, `"pid":2`) || !strings.Contains(text, "pid (or window_id)") {
		t.Fatalf("two processes, nothing to tell them apart: the candidates: %s", text)
	}
	for _, c := range calls {
		if c.Tool == "get_window_state" || c.Tool == "run_script" {
			t.Fatalf("read a window anyway: %v", tools(0))
		}
	}

	text, isErr, calls = call("computer_state", `{"machine": "mac", "pid": 1}`)
	if isErr || !strings.Contains(text, "AXButton") {
		t.Fatalf("by pid: %s", text)
	}
	if w := calls[len(calls)-1]; w.Tool != "get_window_state" || w.Args["pid"] != float64(1) || w.Args["window_id"] != float64(11) {
		t.Fatalf("its own titled window: %+v", w)
	}
	if _, _, calls = call("computer_state", `{"machine": "mac", "window_id": 22}`); calls[len(calls)-1].Args["pid"] != float64(2) ||
		calls[len(calls)-1].Args["window_id"] != float64(22) {
		t.Fatalf("by window_id, its pid found: %+v", calls[len(calls)-1])
	}
	if text, isErr, _ = call("computer_state", `{"machine": "mac", "app": "Calculator", "pid": 1}`); !isErr || !strings.Contains(text, "is Example Browser, not Calculator") {
		t.Fatalf("a pid of another app than named: %s", text)
	}
	if text, isErr, _ = call("computer_state", `{"machine": "mac", "pid": 1, "window_id": 22}`); !isErr || !strings.Contains(text, "not pid 1's") {
		t.Fatalf("a window of another pid: %s", text)
	}

	// Acting: the call's pid and window go to run_steps as its default window, and the app it names, not its own.
	steps := `[{"click": {"role": "button", "name": "Go"}}]`
	text, isErr, calls = call("computer_run", `{"machine": "mac", "pid": 1, "window_id": 11, "steps": `+steps+`}`)
	if run := calls[len(calls)-1]; isErr || run.Tool != "run_steps" || run.Args["pid"] != float64(1) || run.Args["window_id"] != float64(11) || run.Args["app"] != nil {
		t.Fatalf("pid and window_id to run_steps: %s %+v", text, run)
	}
	if text, isErr, calls = call("computer_run", `{"machine": "mac", "app": "Example Browser", "steps": `+steps+`}`); !isErr ||
		!strings.Contains(text, `"ambiguous":true`) || calls[len(calls)-1].Tool == "run_steps" {
		t.Fatalf("the call's app with two processes: %s", text)
	}
	named := `[{"click": {"app": "Example Browser", "role": "button", "name": "Go"}}]`
	if text, isErr, calls = call("computer_run", `{"machine": "mac", "pid": 1, "steps": `+named+`}`); !isErr ||
		!strings.Contains(text, "leave app out of the steps") || calls[len(calls)-1].Tool == "run_steps" {
		t.Fatalf("a step naming the app itself would have Cua Driver choose again: %s", text)
	}
	if text, isErr, _ = call("computer_run", `{"machine": "mac", "script": "const a = await cua.getApp(\"Example Browser\"); await a.click({name: \"Go\"});"}`); !isErr ||
		!strings.Contains(text, "cannot tell them apart") {
		t.Fatalf("a script on an app with two processes: %s", text)
	}
	if text, isErr, _ = call("computer_run", `{"machine": "mac", "pid": 1, "script": "return 1"}`); !isErr || !strings.Contains(text, "go with steps") {
		t.Fatalf("pid with a script: %s", text)
	}
	// A pid stands for its app in the protected-app checks: never a way around them (System Settings runs, with no
	// window on the screen).
	if text, isErr, calls = call("computer_run", `{"machine": "mac", "pid": 9, "steps": `+steps+`}`); !isErr ||
		!strings.Contains(text, "it would operate System Settings") || calls[len(calls)-1].Tool == "run_steps" {
		t.Fatalf("System Settings by its pid: %s %v", text, tools(0))
	}

	// The app active now settles it.
	fakeNoLockMu.Lock()
	fakeApps["mac"] = strings.Replace(apps, `"pid": 1, "running": true, "active": false`, `"pid": 1, "running": true, "active": true`, 1)
	fakeNoLockMu.Unlock()
	text, isErr, calls = call("computer_run", `{"machine": "mac", "app": "Example Browser", "steps": `+steps+`}`)
	if run := calls[len(calls)-1]; isErr || run.Tool != "run_steps" || run.Args["pid"] != float64(1) || run.Args["app"] != nil {
		t.Fatalf("its active process, by pid (its frontmost window, a dialog over it included): %s %+v", text, run)
	}
}

// Windows that cannot be read (an LCU machine while the backend is still auto, say) leave the tools as they were:
// Cua Driver's own lookup, the app as given.
func TestComputerToolsWhenWindowsCannotBeRead(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	_, seen := computerMac(t, h, "mac")
	if err := h.extensions.reg.SetEnabled(ctx, cuaExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, cuaExtensionID, json.RawMessage(`{"machines": ["mac"], "app_approval": false}`), "test"); err != nil {
		t.Fatal(err)
	}
	fakeNoLockMu.Lock()
	fakeWindows["mac"] = func(int) string { return `{"error": "not a list"}` }
	fakeNoLockMu.Unlock()
	t.Cleanup(func() { fakeNoLockMu.Lock(); delete(fakeWindows, "mac"); fakeNoLockMu.Unlock() })
	if res := h.callComputerTool(ctx, "conv1234-5678", "computer_state", json.RawMessage(`{"machine": "mac", "app": "Calculator"}`)); res.IsError {
		t.Fatalf("computer_state: %+v", res)
	}
	_, calls := seen()
	if c := calls[len(calls)-2]; c.Tool != "run_script" || !strings.Contains(c.Args["script"].(string), `cua.getApp("Calculator")`) {
		t.Fatalf("Cua Driver's own lookup: %+v", calls)
	}
	if res := h.callComputerTool(ctx, "conv1234-5678", "computer_state", json.RawMessage(`{"machine": "mac", "pid": 63}`)); !res.IsError {
		t.Fatalf("a pid that cannot be checked is not taken on trust: %+v", res)
	}
}
