package extmgr

// Which window of an app the computer tools use when the app runs as more than one process (#7591). Two processes of
// one app can share its name and bundle id: the user's own browser and another copy of it started with a profile of
// its own, say. Cua Driver's own lookup (cua.getApp, run_steps' app) takes whichever it finds first, so it read another
// process's small untitled window and the user's could not be chosen. With more than one process the manager
// chooses: each process's best window (titled, an ordinary layer-0 window, on the current Space, in front of the
// process's others), then between processes the one with a titled window, on the current Space, the app that is
// active now. What still leaves more than one goes back to the model as candidates, to call again with pid or
// window_id. Window stacking does not choose between processes: another process's windows stacked over the user's
// say nothing of which one the user means (they were, in the case that started this).

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/nwverse/persona/server/internal/protectedapps"
	"github.com/nwverse/persona/server/internal/proto"
)

// cuaWindow is one of Cua Driver's list_windows.
type cuaWindow struct {
	WindowID int    `json:"window_id"`
	PID      int    `json:"pid"`
	AppName  string `json:"app_name"`
	Title    string `json:"title"`
	Bounds   struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"bounds"`
	Layer          int  `json:"layer"`
	ZIndex         *int `json:"z_index"` // null: the stacking is not known
	OnScreen       bool `json:"is_on_screen"`
	OnCurrentSpace bool `json:"on_current_space"`
}

func (w cuaWindow) titled() bool { return strings.TrimSpace(w.Title) != "" }

func (w cuaWindow) z() int {
	if w.ZIndex == nil {
		return -1
	}
	return *w.ZIndex
}

// cuaWindows reads list_windows' answer; ok false when it is not one.
func cuaWindows(raw json.RawMessage) (windows []cuaWindow, ok bool) {
	var out struct {
		Windows *[]cuaWindow `json:"windows"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Windows == nil {
		return nil, false
	}
	return *out.Windows, true
}

// betterWindow: a is the window to take over b, of one process: titled, on the current Space, on screen, in front.
func betterWindow(a, b cuaWindow) bool {
	switch {
	case a.titled() != b.titled():
		return a.titled()
	case a.OnCurrentSpace != b.OnCurrentSpace:
		return a.OnCurrentSpace
	case a.OnScreen != b.OnScreen:
		return a.OnScreen
	}
	return a.z() > b.z()
}

// windowCandidate is one process's best window, as the model is shown it when it must choose.
type windowCandidate struct {
	PID          int    `json:"pid"`
	WindowID     int    `json:"window_id"`
	App          string `json:"app"`
	Title        string `json:"title"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	CurrentSpace bool   `json:"on_current_space"`
}

// windowPick is the window chosen of an app: one (PID, WindowID), or Candidates when more than one process is left.
// Processes is how many processes of the app have an ordinary window.
type windowPick struct {
	PID, WindowID int
	Processes     int
	Candidates    []windowCandidate
}

// pickWindow chooses app's window among windows (list_windows filtered by app or pid). active says whether a process
// is the app that is active now; it is asked only when processes are left to choose between.
func pickWindow(windows []cuaWindow, app string, active func(pid int) bool) windowPick {
	exact := false
	for _, w := range windows {
		exact = exact || strings.EqualFold(w.AppName, app)
	}
	best := map[int]cuaWindow{}
	for _, w := range windows {
		if w.Layer != 0 || w.WindowID == 0 || exact && !strings.EqualFold(w.AppName, app) {
			continue // a menu, panel or overlay; an app whose name only contains app's when one has it whole
		}
		if b, ok := best[w.PID]; !ok || betterWindow(w, b) {
			best[w.PID] = w
		}
	}
	left := make([]cuaWindow, 0, len(best))
	for _, w := range best {
		left = append(left, w)
	}
	pick := windowPick{Processes: len(left)}
	keep := func(ok func(cuaWindow) bool) {
		var kept []cuaWindow
		for _, w := range left {
			if ok(w) {
				kept = append(kept, w)
			}
		}
		if len(kept) > 0 {
			left = kept
		}
	}
	if len(left) > 1 {
		keep(cuaWindow.titled)
		keep(func(w cuaWindow) bool { return w.OnCurrentSpace })
	}
	if len(left) > 1 && active != nil {
		keep(func(w cuaWindow) bool { return active(w.PID) })
	}
	switch len(left) {
	case 0:
		return pick
	case 1:
		pick.PID, pick.WindowID = left[0].PID, left[0].WindowID
		return pick
	}
	sort.Slice(left, func(i, j int) bool { return left[i].PID < left[j].PID })
	for _, w := range left {
		pick.Candidates = append(pick.Candidates, windowCandidate{PID: w.PID, WindowID: w.WindowID, App: w.AppName, Title: w.Title,
			Width: int(w.Bounds.Width), Height: int(w.Bounds.Height), CurrentSpace: w.OnCurrentSpace})
	}
	return pick
}

// activeApps is which processes on m are the app active now (Cua Driver's list_apps), by pid; nil when it cannot be read.
func (h *Manager) activeApps(ctx context.Context, m *Machine, session, conversation string) func(pid int) bool {
	res, err := h.computerCall(ctx, m, session, conversation, "list_apps", map[string]any{})
	if err != nil {
		return nil
	}
	var out struct {
		Apps []struct {
			PID    int  `json:"pid"`
			Active bool `json:"active"`
		} `json:"apps"`
	}
	if json.Unmarshal(res.Output, &out) != nil {
		return nil
	}
	on := map[int]bool{}
	for _, a := range out.Apps {
		if a.Active {
			on[a.PID] = true
		}
	}
	return func(pid int) bool { return on[pid] }
}

// chooseWindow is the window of app (or of process pid) to read or act in on m: pick.Processes 0 when none has a
// window; Candidates when the model must choose.
func (h *Manager) chooseWindow(ctx context.Context, m *Machine, session, conversation, app string, pid int) (windowPick, error) {
	in := map[string]any{"app": app}
	if pid != 0 {
		in = map[string]any{"pid": pid}
	}
	res, err := h.computerCall(ctx, m, session, conversation, "list_windows", in)
	if err != nil {
		return windowPick{}, err
	}
	windows, ok := cuaWindows(res.Output)
	if !ok {
		return windowPick{}, fmt.Errorf("the windows of %s could not be read", app)
	}
	if pid != 0 {
		windows = slices.DeleteFunc(windows, func(w cuaWindow) bool { return w.PID != pid })
	}
	pick := pickWindow(windows, app, nil)
	if len(pick.Candidates) > 0 {
		if active := h.activeApps(ctx, m, session, conversation); active != nil {
			pick = pickWindow(windows, app, active)
		}
	}
	return pick, nil
}

// ambiguousWindows is what the model is told when it must choose between processes of app: their windows, and to
// call again with pid or window_id (inSteps: a step named the app, which must go from the steps then).
func ambiguousWindows(app, tool string, pick windowPick, inSteps bool) proto.ToolResult {
	how := "Call " + tool + " again with the pid (or window_id) of the right one"
	if inSteps {
		how += " on computer_run itself, and leave app out of the steps (a step's own app has Cua Driver choose again)"
	}
	body, _ := json.Marshal(map[string]any{
		"ambiguous":  true,
		"note":       fmt.Sprintf("Nothing was done: %d processes of %s have windows. %s; if unsure which, ask the user (by the window titles).", len(pick.Candidates), app, how),
		"candidates": pick.Candidates,
	})
	return proto.TextResult(string(body), true)
}

// computerTarget is the app that pid or windowID (when given) is on m, for the checks and approvals that go by app,
// and that window's pid: an error when they name nothing open or not app. Without either, app as given.
func (h *Manager) computerTarget(ctx context.Context, m *Machine, session, conversation, app string, pid, windowID int) (string, int, error) {
	if pid == 0 && windowID == 0 {
		return app, 0, nil
	}
	in := map[string]any{}
	if pid != 0 {
		in["pid"] = pid
	}
	res, err := h.computerCall(ctx, m, session, conversation, "list_windows", in)
	if err != nil {
		return "", 0, err
	}
	windows, ok := cuaWindows(res.Output)
	if !ok {
		return "", 0, fmt.Errorf("the windows on %s could not be read", m.name())
	}
	name := ""
	for _, w := range windows {
		if windowID != 0 && w.WindowID == windowID {
			if pid != 0 && w.PID != pid {
				return "", 0, fmt.Errorf("window %d is not pid %d's (it is pid %d's)", windowID, pid, w.PID)
			}
			pid, name = w.PID, w.AppName
			break
		}
		if windowID == 0 && w.PID == pid && name == "" {
			name = w.AppName
		}
	}
	known := h.knownApps(ctx, m)
	if a, ok := protectedapps.AppOf(known, pid); ok && name == "" {
		name = a.Name
	}
	switch {
	case windowID != 0 && name == "":
		return "", 0, fmt.Errorf("no window %d on %s: read the screen again (computer_state without app lists them)", windowID, m.name())
	case name == "":
		return "", 0, fmt.Errorf("no app with pid %d on %s", pid, m.name())
	case app != "" && !sameApp(app, name, pid, known):
		return "", 0, fmt.Errorf("pid %d is %s, not %s", pid, name, app)
	}
	return name, pid, nil
}

// sameApp: app (a name or bundle id as the model gave it) is the app named name running as pid.
func sameApp(app, name string, pid int, known []protectedapps.App) bool {
	if strings.EqualFold(app, name) {
		return true
	}
	a, ok := protectedapps.AppOf(known, pid)
	return ok && (strings.EqualFold(app, a.BundleID) || strings.EqualFold(app, a.Name))
}

// pinSteps sets run_steps' default window in in: the pid (and window) given, the process chosen of an app with
// several, or else the app. Steps that name an app with several processes themselves are refused with its windows:
// a step's own app wins over the default, and Cua Driver would choose the window again.
func (h *Manager) pinSteps(ctx context.Context, m *Machine, session, conversation, app string, pid, windowID int, steps json.RawMessage,
	in map[string]any) *proto.ToolResult {
	refuse := func(res proto.ToolResult) *proto.ToolResult { return &res }
	picks := map[string]windowPick{}
	pickOf := func(name string) (windowPick, error) {
		k := strings.ToLower(name)
		if p, ok := picks[k]; ok {
			return p, nil
		}
		p, err := h.chooseWindow(ctx, m, session, conversation, name, 0)
		if err == nil {
			picks[k] = p
		}
		return p, err
	}
	switch {
	case pid != 0:
		in["pid"] = pid
		if windowID != 0 {
			in["window_id"] = windowID
		}
	case app != "":
		pick, err := pickOf(app)
		switch {
		case err != nil: // the windows not read: the app as given, as before
			in["app"] = app
		case len(pick.Candidates) > 0:
			return refuse(ambiguousWindows(app, "computer_run", pick, false))
		case pick.Processes > 1:
			in["pid"] = pick.PID // not its window: a dialog that opens over it in that process is still found
		default:
			in["app"] = app
		}
	}
	for _, named := range stepTargets(steps) {
		if pick, err := pickOf(named); err == nil && pick.Processes > 1 { // not read: the step's own run says what is wrong
			if len(pick.Candidates) == 0 { // chosen, but a step naming it would have Cua Driver choose again
				pick, _ = h.everyProcess(ctx, m, session, conversation, named)
			}
			return refuse(ambiguousWindows(named, "computer_run", pick, true))
		}
	}
	return nil
}

// everyProcess is app's processes' best windows as candidates, without choosing between them.
func (h *Manager) everyProcess(ctx context.Context, m *Machine, session, conversation, app string) (windowPick, error) {
	res, err := h.computerCall(ctx, m, session, conversation, "list_windows", map[string]any{"app": app})
	if err != nil {
		return windowPick{}, err
	}
	windows, _ := cuaWindows(res.Output)
	return pickWindow(windows, app, func(int) bool { return false }), nil
}

// scriptProcesses refuses a script that names an app with several processes: cua.getApp("…") cannot tell them apart.
func (h *Manager) scriptProcesses(ctx context.Context, m *Machine, session, conversation, script string) *proto.ToolResult {
	names, all := protectedapps.ScriptApps(script)
	if !all {
		return nil // refused below as unreadable, before anything is asked of the Mac
	}
	for _, name := range names {
		pick, err := h.chooseWindow(ctx, m, session, conversation, name, 0)
		if err != nil || pick.Processes < 2 {
			continue // the script's own run says what is wrong
		}
		res := proto.TextResult(fmt.Sprintf("Nothing was done: %d processes of %s have windows, and cua.getApp(%s) in a script cannot "+
			"tell them apart. Read the one you mean with computer_state (it lists them), then act on it with steps, passing its pid "+
			"(or window_id) on computer_run itself.", pick.Processes, name, jsString(name)), true)
		return &res
	}
	return nil
}

// stepTargets are the apps run_steps steps name as their own target ({"tool": …, "args": {"app": …}} or
// {"click": {"app": …}}), case aside; not an app a launch_app step opens.
func stepTargets(steps json.RawMessage) []string {
	var list []map[string]json.RawMessage
	_ = json.Unmarshal(steps, &list)
	var out []string
	add := func(raw json.RawMessage) {
		var args struct {
			App string `json:"app"`
		}
		_ = json.Unmarshal(raw, &args)
		if a := strings.TrimSpace(args.App); a != "" && !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, a) }) {
			out = append(out, a)
		}
	}
	for _, s := range list {
		if _, launch := protectedapps.LaunchTarget(s); launch {
			continue
		}
		if raw, ok := s["args"]; ok {
			add(raw)
			continue
		}
		for k, raw := range s {
			if k != "tool" && k != "wait_for" && k != "expect" && k != "timeout_ms" {
				add(raw)
			}
		}
	}
	return out
}
