package protectedapps

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// fakeMac answers a check's reads as Cua Driver does on a Mac with System Settings open behind other windows (#7560):
//
//	Microsoft Edge  window 130    (0, 30)     900×900  z 20
//	Finder          window 19981  (660, 174)  920×436  z 24
//	System Settings window 18640  (945, 196)  723×625  z 17, "Screen & System Audio Recording"
//	Notes           window 159    (460, 195)  1000×660 z 3
type fakeMac struct {
	active   string            // the frontmost app's name
	settings bool              // System Settings' window is on the screen (else minimized)
	gone     bool              // System Settings has no window
	noZ      bool              // the stacking order is not known
	extra    string            // more windows, JSON
	broken   bool              // list_windows fails
	since    map[string]string // a snapshot id → what a read of System Settings' window says of it ("" fails)
	reads    []string          // each read: its tool, and get_window_state's pid, window and since
}

func (m *fakeMac) read(tool string, args map[string]any) (json.RawMessage, bool) {
	switch tool {
	case "list_apps":
		m.reads = append(m.reads, tool)
		var apps []string
		for _, a := range []struct {
			name, id string
			pid      int
		}{{"Microsoft Edge", "com.microsoft.edgemac", 626}, {"Finder", "com.apple.finder", 671},
			{"System Settings", "com.apple.systempreferences", 3341}, {"Notes", "com.apple.Notes", 656}} {
			apps = append(apps, fmt.Sprintf(`{"name": %q, "bundle_id": %q, "pid": %d, "running": true, "active": %v}`,
				a.name, a.id, a.pid, a.name == m.active))
		}
		return json.RawMessage(`{"apps": [` + strings.Join(apps, ",") + `]}`), true
	case "list_windows":
		m.reads = append(m.reads, tool)
		if m.broken {
			return nil, false
		}
		z := func(n int) string {
			if m.noZ {
				return "null"
			}
			return fmt.Sprint(n)
		}
		w := func(id, pid int, app, title string, x, y, width, height float64, z string, shown bool) string {
			return fmt.Sprintf(`{"window_id": %d, "pid": %d, "app_name": %q, "title": %q, "layer": 0, "z_index": %s, `+
				`"is_on_screen": %v, "bounds": {"x": %v, "y": %v, "width": %v, "height": %v}}`, id, pid, app, title, z, shown, x, y, width, height)
		}
		list := []string{
			w(130, 626, "Microsoft Edge", "Auth0", 0, 30, 900, 900, z(20), true),
			w(19981, 671, "Finder", "Downloads", 660, 174, 920, 436, z(24), true),
			w(159, 656, "Notes", "Notes", 460, 195, 1000, 660, z(3), true),
		}
		if !m.gone {
			list = append(list, w(18640, 3341, "System Settings", "Screen & System Audio Recording", 945, 196, 723, 625, z(17), m.settings))
		}
		if m.extra != "" {
			list = append(list, m.extra)
		}
		return json.RawMessage(`{"windows": [` + strings.Join(list, ",") + `]}`), true
	case "get_window_state":
		m.reads = append(m.reads, fmt.Sprintf("%s %v %v %v", tool, args["pid"], args["window_id"], args["since"]))
		status := m.since[fmt.Sprint(args["since"])]
		if status == "" {
			return nil, false
		}
		return json.RawMessage(`{"since_status": "` + status + `", "snapshot_id": "s000000ff"}`), true
	}
	m.reads = append(m.reads, tool)
	return nil, false
}

// check runs one call through a fresh look at m.
func (m *fakeMac) check(tool, args string) Verdict {
	return NewLook(m.read, nil).Check("this Mac", tool, json.RawMessage(args))
}

func behindEdge() *fakeMac {
	return &fakeMac{active: "Microsoft Edge", settings: true,
		since: map[string]string{"s0000024a": "other_window", "s00000099": "no_change", "s0000009a": "view_changed"}}
}

type checkCase struct {
	what, tool, args, rule string // rule "": let go
}

func runChecks(t *testing.T, m *fakeMac, cases []checkCase) {
	t.Helper()
	for _, c := range cases {
		if v := m.check(c.tool, c.args); v.Rule != c.rule || (c.rule == "") != (v.Why == "") {
			t.Errorf("%s: %s %s: rule %q (%q), want %q", c.what, c.tool, c.args, v.Rule, v.Why, c.rule)
		}
	}
}

// 1. Named, or aimed at by pid, window or title: refused whatever is on the screen, System Settings minimized too.
func TestCheckNamedOrAimed(t *testing.T) {
	for _, shown := range []bool{true, false} {
		m := behindEdge()
		m.settings = shown
		runChecks(t, m, []checkCase{
			{"by name", "click", `{"app": "System Settings", "element_token": "s0000024a:5"}`, "named"},
			{"by pid", "type_text", `{"pid": 3341, "text": "x"}`, "named"},
			{"by its window", "click", `{"window_id": 18640, "x": 10, "y": 10}`, "named"},
			{"by a window target", "click", `{"target": {"kind": "window", "pid": 626, "window_id": 18640}, "x": 1, "y": 1}`, "named"},
			{"by a window's title", "run_steps", `{"steps": [{"click": {"window": "audio recording", "role": "checkbox"}}]}`, "named"},
			{"the steps' app", "run_steps", `{"app": "com.apple.systempreferences", "steps": [{"press_key": {"key": "space"}}]}`, "named"},
			{"opened by a step", "run_steps", `{"steps": [{"tool": "launch_app", "args": {"bundle_id": "com.apple.systempreferences"}}]}`, "named"},
			{"a script", "run_script", `{"script": "await cua.getApp(\"系统设置\")"}`, "named"},
			{"moved", "set_window_frame", `{"pid": 3341, "window_id": 18640, "x": 0, "y": 0}`, "named"},
			{"a prompt by name", "click", `{"app": "SecurityAgent"}`, "named"},
		})
	}
}

// 2. An element token is traced to its window by reading System Settings' window only; another app's token goes.
func TestCheckTokens(t *testing.T) {
	m := behindEdge()
	runChecks(t, m, []checkCase{
		{"Edge's token", "click", `{"element_token": "s0000024a:5"}`, ""},
		{"System Settings' token", "click", `{"element_token": "s00000099:3"}`, "named"},
		{"System Settings', an older read", "set_value", `{"element_token": "s0000009a:3", "value": "on"}`, "named"},
		{"a token no read could trace", "click", `{"element_token": "s000000aa:1"}`, "token"},
		{"a bare row of a named window", "click", `{"element_token": "11", "window_id": 130}`, ""},
		{"a bare row of no window", "click", `{"element_token": "11"}`, "token"},
		{"a pid given beside it does not count", "click", `{"pid": 626, "element_token": "s00000099:3"}`, "named"},
		{"in a step", "run_steps", `{"app": "Microsoft Edge", "steps": [{"click": {"element_token": "s00000099:3"}}]}`, "named"},
	})
	m.reads = nil
	if v := m.check("run_steps", `{"steps": [{"click": {"element_token": "s0000024a:5"}}, {"type_text": {"element_token": "s0000024a:6", "text": "https://example.com/callback"}}]}`); v.Why != "" {
		t.Fatalf("Edge's tokens: %+v", v)
	}
	slices.Sort(m.reads)
	if want := []string{"get_window_state 3341 18640 s0000024a", "list_apps", "list_windows"}; !slices.Equal(m.reads, want) {
		t.Fatalf("read System Settings' window once, never Edge's: %v", m.reads)
	}

	// Minimized, System Settings' tokens still reach it: still traced.
	m.settings = false
	if v := m.check("click", `{"element_token": "s00000099:3"}`); v.Rule != "named" {
		t.Fatalf("System Settings minimized, its token: %+v", v)
	}
	// No window of it: nothing to trace, nothing read.
	m.gone, m.reads = true, nil
	if v := m.check("click", `{"element_token": "s00000099:3"}`); v.Why != "" ||
		slices.ContainsFunc(m.reads, func(r string) bool { return strings.HasPrefix(r, "get_window_state") }) {
		t.Fatalf("no System Settings window: %+v %v", v, m.reads)
	}
}

// 3. A point is refused only where System Settings' window can be the one on top, at whatever scale the pixels are.
func TestCheckPoints(t *testing.T) {
	m := behindEdge()
	runChecks(t, m, []checkCase{
		{"in Edge, far from System Settings", "click", `{"pid": 626, "window_id": 130, "x": 100, "y": 100}`, ""},
		{"in Notes, where System Settings is above it", "click", `{"pid": 656, "window_id": 159, "x": 700, "y": 400}`, "covered"},
		{"in Notes, by its pid alone", "double_click", `{"pid": 656, "x": 700, "y": 400}`, "covered"},
		{"in Notes, by its app in a step", "run_steps", `{"steps": [{"click": {"app": "Notes", "x": 700, "y": 400}}]}`, "covered"},
		{"in Notes, under Finder", "click", `{"pid": 656, "window_id": 159, "x": 600, "y": 50}`, ""},
		{"a drag in Edge", "drag", `{"pid": 626, "window_id": 130, "from_x": 10, "from_y": 10, "to_x": 800, "to_y": 800}`, ""},
		{"a drag ending in it", "drag", `{"pid": 656, "window_id": 159, "from_x": 10, "from_y": 10, "to_x": 700, "to_y": 400}`, "covered"},
		{"a scroll there", "scroll", `{"pid": 656, "window_id": 159, "x": 700, "y": 400, "direction": "down"}`, "covered"},
		{"the screen's pixels on it", "click", `{"scope": "desktop", "x": 1300, "y": 700}`, "covered"},
		{"the screen's pixels elsewhere", "click", `{"target": {"kind": "desktop", "display_id": "primary"}, "x": 300, "y": 300}`, ""},
		{"a zoom image of Notes", "click", `{"pid": 656, "window_id": 159, "x": 5, "y": 5, "from_zoom": true}`, "covered"},
		{"a zoom image of Edge", "click", `{"pid": 626, "window_id": 130, "x": 5, "y": 5, "from_zoom": true}`, ""},
		{"a point in no window", "click", `{"x": 5, "y": 5}`, "unplaced"},
		{"a step after Notes", "run_steps", `{"steps": [{"click": {"app": "Notes", "role": "button", "name": "New"}}, {"click": {"x": 700, "y": 400}}]}`, "covered"},
	})
	m.noZ = true // the order unknown: by the windows' bounds
	runChecks(t, m, []checkCase{
		{"in Edge, never over it", "click", `{"pid": 626, "window_id": 130, "x": 100, "y": 100}`, ""},
		{"in Notes, over it", "click", `{"pid": 656, "window_id": 159, "x": 600, "y": 50}`, "covered"},
	})
	m.noZ, m.settings = false, false
	runChecks(t, m, []checkCase{
		{"minimized, nothing of it to land on", "click", `{"pid": 656, "window_id": 159, "x": 700, "y": 400}`, ""},
	})
}

// 4. Keys aimed at an app are judged by that app; keys with no target only by the frontmost app.
func TestCheckKeys(t *testing.T) {
	m := behindEdge()
	edge := []checkCase{
		{"to Edge's pid", "type_text", `{"pid": 626, "text": "https://example.com/callback"}`, ""},
		{"to an element of Edge", "press_key", `{"element_token": "s0000024a:9", "key": "return"}`, ""},
		{"steps on Edge's window", "run_steps", `{"steps": [{"click": {"app": "Microsoft Edge", "role": "button", "name": "Save"}}, {"press_key": {"key": "return"}}]}`, ""},
		{"to Edge by name", "run_steps", `{"app": "Microsoft Edge", "steps": [{"hotkey": {"keys": ["cmd", "s"]}}]}`, ""},
	}
	runChecks(t, m, append(edge,
		checkCase{"no target, Edge in front", "press_key", `{"key": "return"}`, ""},
		checkCase{"the front app, Edge", "hotkey", `{"scope": "desktop", "keys": ["cmd", "v"]}`, ""},
	))
	m.active = "System Settings"
	runChecks(t, m, append(edge,
		checkCase{"no target, System Settings in front", "press_key", `{"key": "space"}`, "front"},
		checkCase{"the front app, System Settings", "hotkey", `{"scope": "desktop", "keys": ["cmd", "v"]}`, "front"},
		checkCase{"a step with no app", "run_steps", `{"steps": [{"type_text": {"text": "x"}}]}`, "front"},
		checkCase{"a scroll by keys", "scroll", `{"direction": "down"}`, "front"},
	))
	m.active = ""
	runChecks(t, m, []checkCase{{"no front app known", "press_key", `{"key": "space"}`, "unplaced"}})
}

// 5. A permission or password prompt on the screen stops everything: it is modal and on top.
func TestCheckPrompts(t *testing.T) {
	m := behindEdge()
	m.extra = `{"window_id": 900, "pid": 98, "app_name": "universalAccessAuthWarn", "layer": 0, "z_index": 30, "is_on_screen": true,
		"bounds": {"x": 700, "y": 300, "width": 260, "height": 300}}`
	for _, c := range []checkCase{
		{"Edge's token", "click", `{"element_token": "s0000024a:5"}`, "prompt"},
		{"keys to Edge", "type_text", `{"pid": 626, "text": "x"}`, "prompt"},
		{"a browser page", "browser_click", `{"ref": "e1"}`, "prompt"},
		{"the clipboard", "clipboard_write", `{"text": "x"}`, "prompt"},
	} {
		v := m.check(c.tool, c.args)
		if v.Rule != "prompt" || !strings.Contains(v.Why, "(universalAccessAuthWarn) is on its screen") {
			t.Errorf("%s: %+v", c.what, v)
		}
	}
	m.extra = strings.Replace(m.extra, `"is_on_screen": true`, `"is_on_screen": false`, 1)
	runChecks(t, m, []checkCase{{"gone from the screen", "type_text", `{"pid": 626, "text": "x"}`, ""}})
}

// 6. What cannot be checked is refused: windows not read, a script, a tool whose place is not known.
func TestCheckUnknowable(t *testing.T) {
	m := behindEdge()
	runChecks(t, m, []checkCase{
		{"a script while it is open", "run_script", `{"script": "const e = await cua.getApp(\"Microsoft Edge\"); await e.click({role: \"button\", name: \"Save\"})"}`, "script"},
		{"a script naming apps by expression", "run_script", `{"script": "await cua.getApp(name)"}`, "script"},
		{"a tool added later", "some_new_tool", `{"pid": 626}`, "unplaced"},
		{"a click with no target", "click", `{"button": "left"}`, "unplaced"},
		{"a browser page", "browser_click", `{"ref": "e1"}`, ""},
		{"the clipboard", "clipboard_write", `{"text": "x"}`, ""},
		{"opening another app", "launch_app", `{"name": "Notes"}`, ""},
	})
	m.reads = nil
	if v := m.check("get_window_state", `{"pid": 3341, "window_id": 18640}`); v.Why != "" || len(m.reads) != 0 {
		t.Fatalf("a read goes, with no look: %+v %v", v, m.reads)
	}
	m.settings = false
	runChecks(t, m, []checkCase{
		{"a script, System Settings minimized", "run_script", `{"script": "await cua.getApp(\"Notes\")"}`, ""},
		{"a tool added later, minimized", "some_new_tool", `{}`, ""},
	})
	m.broken = true
	if v := m.check("click", `{"pid": 626, "element_token": "s0000024a:1"}`); v.Why != Unchecked {
		t.Fatalf("windows not read: %+v", v)
	}
}

// 7. Each refusal says which case it is and what can still be done; none says no app can be operated.
func TestCheckWording(t *testing.T) {
	m := behindEdge()
	m.active = "System Settings"
	for _, c := range []struct{ tool, args, want string }{
		{"click", `{"pid": 656, "window_id": 159, "x": 700, "y": 400}`, "System Settings is on top where this would click"},
		{"press_key", `{"key": "space"}`, "System Settings is the frontmost app, so keys sent without a target would go to it"},
		{"run_script", `{"script": "await cua.getApp(\"Notes\")"}`, "use steps instead"},
		{"click", `{"element_token": "s000000aa:1"}`, "Read the window again"},
		{"click", `{"x": 5, "y": 5}`, "Say which app or window it is for"},
	} {
		v := m.check(c.tool, c.args)
		if !strings.Contains(v.Why, c.want) || !strings.Contains(strings.ToLower(v.Why), "only system settings is off limits") &&
			v.Rule != "token" {
			t.Errorf("%s %s: %q", c.tool, c.args, v.Why)
		}
		if strings.Contains(strings.ToLower(v.Why), "any app") {
			t.Errorf("%s: says no app can be operated: %q", c.tool, v.Why)
		}
	}
}
