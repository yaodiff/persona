package protectedapps

// Where an acting Cua Driver call lands, and whether that is System Settings or a permission prompt (#7560). The
// assistant's computer_run (extmgr) and the Agents' own Cua Driver (the node's gate) both decide by Look.Check:
//
//   - A call that names a protected app, or aims at one's pid or window, is refused whatever is on the screen (#4336).
//   - While a permission or password prompt is on the screen nothing is operated: it is modal and floats on top, and
//     #4349 found it takes synthetic clicks.
//   - While System Settings has a window, a call is refused only when it could reach it: an element token of one of
//     its windows; a point where its window can be the one on top; keys with no target while it is the frontmost app.
//     What cannot be told (a token not traced, a point in no known window, a script) is refused too. Calls on other
//     apps' windows go on.
//
// An element token carries its own pid (Cua Driver ignores the call's), so it is traced by reading each of System
// Settings' windows with since: <the token's snapshot id>: "other_window" says the token is another window's. Only
// System Settings' windows are read, never the one the call is for: a read replaces a window's current snapshot,
// which would make the caller's tokens stale.
//
// A point is in a screenshot's pixels (the window's get_window_state image, or the desktop's), which Cua Driver maps
// back by the image's scale: up to 2 (a Retina display at full size), and below 1 when the image was capped. The scale
// is not in the call, so every place the point can land is looked at: from the window's origin plus half the point,
// outward along the same line to the window's edge.

import (
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Read is one read-only Cua Driver call a check makes, and its JSON answer; ok is false when it failed or gave none.
type Read func(tool string, args map[string]any) (out json.RawMessage, ok bool)

// Verdict is a check's answer. Why is what the model is told, "" to let the call go; Rule says which rule refused it
// ("named", "prompt", "covered", "front", "script", "token", "unplaced", "unchecked") and App the protected app it is
// about, for the log.
type Verdict struct {
	Why, Rule, App string
}

// Look is the Mac as one check reads it, each part once, when first needed: its apps, its windows and whose element
// tokens are.
type Look struct {
	read Read

	apps                []App
	appsRead, appsFresh bool
	refreshed           bool

	windows                []Window
	windowsRead, windowsOK bool
	kinds                  []kind
	names                  []string

	owners map[string]owner // "<snapshot id> <window id>": whether that snapshot is that window's
}

// NewLook is a look through read; apps is a list already read for the call (nil: read when needed).
func NewLook(read Read, apps []App) *Look {
	return &Look{read: read, apps: apps, appsRead: apps != nil, owners: map[string]owner{}}
}

// Apps is the Mac's app list as the look last read it, for the caller to keep.
func (l *Look) Apps() []App {
	l.loadApps(false)
	return l.apps
}

// loadApps reads the app list, again when fresh and the list was handed in (who is frontmost changes); ok is false
// when that read failed.
func (l *Look) loadApps(fresh bool) bool {
	if l.appsRead && (!fresh || l.appsFresh) {
		return true
	}
	l.appsRead, l.appsFresh = true, true
	out, ok := l.read("list_apps", map[string]any{})
	if ok {
		l.apps = Apps(out)
	}
	return ok
}

// appOf is the app pid runs. A pid the list lacks (an app started since) reads the list once more when again is set.
func (l *Look) appOf(pid int, again bool) (App, bool) {
	l.loadApps(false)
	a, ok := AppOf(l.apps, pid)
	if !ok && again && pid > 0 && !l.refreshed {
		l.refreshed = true
		if out, read := l.read("list_apps", map[string]any{}); read {
			l.apps, l.appsFresh = Apps(out), true
			a, ok = AppOf(l.apps, pid)
		}
	}
	return a, ok
}

type kind int

const (
	otherApp kind = iota
	settings
	prompt
)

// loadWindows reads the window list and what each window is; false when it could not be read.
func (l *Look) loadWindows() bool {
	if l.windowsRead {
		return l.windowsOK
	}
	l.windowsRead = true
	out, ok := l.read("list_windows", map[string]any{})
	if !ok {
		return false
	}
	if l.windows, l.windowsOK = Windows(out); !l.windowsOK {
		return false
	}
	l.kinds, l.names = make([]kind, len(l.windows)), make([]string, len(l.windows))
	for i, w := range l.windows {
		// A window's pid is matched to its app, so System Settings counts in any language; the prompts go by their
		// process names (the app list does not carry them).
		app, has := l.appOf(w.PID, w.OnScreen)
		name := w.AppName
		if has && app.Name != "" {
			name = app.Name
		}
		switch {
		case IsPrompt(app.BundleID, name) || IsPrompt("", w.AppName):
			l.kinds[i] = prompt
		case isSettings(app.BundleID, name) || isSettings("", w.AppName):
			l.kinds[i] = settings
		}
		if name == "" {
			name = app.BundleID
		}
		l.names[i] = name
	}
	return true
}

// listed is the names of the k windows, only those on the screen when shown is set, each once.
func (l *Look) listed(k kind, shown bool) []string {
	var out []string
	for i, w := range l.windows {
		if l.kinds[i] == k && (w.OnScreen || !shown) && !slices.Contains(out, l.names[i]) {
			out = append(out, l.names[i])
		}
	}
	return out
}

// Check decides one acting Cua Driver call on where (a Mac's name, or "this Mac"): tool and its arguments,
// run_steps and run_script included.
func (l *Look) Check(where, tool string, args json.RawMessage) Verdict {
	if !Acts(tool) {
		return Verdict{}
	}
	var a struct {
		App      string          `json:"app"`
		Name     string          `json:"name"`
		BundleID string          `json:"bundle_id"`
		Script   string          `json:"script"`
		Steps    json.RawMessage `json:"steps"`
	}
	_ = json.Unmarshal(args, &a)
	var named []string
	var targets []target
	switch tool {
	case "run_script":
		apps, all := ScriptApps(a.Script)
		if !all {
			return Verdict{Why: UnreadableScript, Rule: "script"}
		}
		named = apps
	case "run_steps":
		named, _ = StepsApps(a.App, a.Steps)
		targets = stepsTargets(args)
	default:
		for _, n := range []string{a.App, a.Name, a.BundleID} {
			if strings.TrimSpace(n) != "" {
				named = append(named, n)
			}
		}
		targets = []target{targetOf(tool, args)}
	}
	operate := func(app string) Verdict {
		return Verdict{Why: Refusal(where, "it would operate "+app), Rule: "named", App: app}
	}

	// Named, or aimed at by pid: whatever is on the screen.
	for _, n := range named {
		if Is(n, n) {
			return operate(n)
		}
	}
	for _, n := range named { // a name in the Mac's own language, through the list's bundle ids
		for _, k := range l.Apps() {
			if (strings.EqualFold(k.Name, n) || strings.EqualFold(k.BundleID, n)) && Is(k.BundleID, k.Name) {
				return operate(k.Name)
			}
		}
	}
	for _, t := range targets {
		for _, pid := range t.pids {
			if app, ok := l.appOf(pid, true); ok && Is(app.BundleID, app.Name) {
				return operate(app.Name)
			}
		}
	}
	if !l.loadWindows() {
		return Verdict{Why: Unchecked, Rule: "unchecked"}
	}
	for _, t := range targets { // a pid or window the app list lacks (a prompt's), a window by its title
		if name, ok := l.aimed(t); ok {
			return operate(name)
		}
	}

	if shown := l.listed(prompt, true); len(shown) > 0 {
		return Verdict{Why: PromptRefusal(where, strings.Join(shown, ", ")), Rule: "prompt", App: shown[0]}
	}
	open := l.listed(settings, true)
	if tool == "run_script" {
		if len(open) > 0 {
			return Verdict{Why: ScriptRefusal(where, open[0]), Rule: "script", App: open[0]}
		}
		return Verdict{}
	}
	if len(l.listed(settings, false)) == 0 {
		return Verdict{}
	}
	for _, t := range targets {
		if v := l.place(where, t, open); v.Why != "" {
			return v
		}
	}
	return Verdict{}
}

// aimed is the protected app t aims at by pid, window id or window title, if any.
func (l *Look) aimed(t target) (string, bool) {
	for i, w := range l.windows {
		if l.kinds[i] == otherApp {
			continue
		}
		title := strings.ToLower(w.Title)
		if slices.Contains(t.pids, w.PID) || slices.Contains(t.wids, w.ID) ||
			(t.title != "" && t.app == "" && title != "" && strings.Contains(title, strings.ToLower(t.title))) {
			return l.names[i], true
		}
	}
	return "", false
}

// Tool kinds, for where an action lands.
var (
	pointerTools = map[string]bool{"click": true, "double_click": true, "right_click": true, "scroll": true, "drag": true,
		"move_cursor": true}
	keyTools = map[string]bool{"type_text": true, "press_key": true, "hotkey": true, "set_value": true}
	// appTools act on the app or window they name.
	appTools = map[string]bool{"launch_app": true, "bring_to_front": true, "kill_app": true, "invoke_menu": true,
		"set_window_frame": true}
	// windowless tools act on no window: the clipboard, the agent cursor, recording.
	windowless = map[string]bool{"clipboard_read": true, "clipboard_write": true, "set_agent_cursor_enabled": true,
		"set_agent_cursor_motion": true, "set_agent_cursor_theme": true, "start_recording": true, "stop_recording": true}
)

// place decides t while System Settings has a window (open: those on the screen, by name).
func (l *Look) place(where string, t target, open []string) Verdict {
	name := l.listed(settings, false)[0]
	if len(open) > 0 {
		name = open[0]
	}
	unplaced := Verdict{Why: UnplacedRefusal(where, name), Rule: "unplaced", App: name}
	switch {
	case windowless[t.tool] || strings.HasPrefix(t.tool, "browser_") || t.tool == "page":
		return Verdict{} // a browser tab's page, through the browser; no window of System Settings
	case appTools[t.tool]:
		if t.placed() || t.named {
			return Verdict{}
		}
	case !pointerTools[t.tool] && !keyTools[t.tool]:
		// Any other tool, one added later included: where it acts is not known.
	default:
		if t.token != "" {
			if v := l.tokenCheck(where, t); v.Why != "" {
				return v
			}
		}
		if len(t.points) > 0 {
			if len(open) == 0 {
				return Verdict{} // nothing of System Settings on the screen to land on
			}
			return l.pointCheck(where, t, open[0])
		}
		if t.token != "" || (t.placed() && !t.desktop) {
			return Verdict{}
		}
		if keyTools[t.tool] || t.tool == "scroll" { // keys, or a scroll by keys, to whichever app is frontmost
			return l.frontCheck(where, name)
		}
	}
	if len(open) == 0 {
		return Verdict{}
	}
	return unplaced
}

// owner is whose a snapshot is, as far as a read could tell.
type owner int

const (
	unknownOwner owner = iota
	thisWindow
	otherWindow
)

// tokenCheck traces t's element token to its window: refused when it is one of System Settings' windows, or when
// that cannot be told.
func (l *Look) tokenCheck(where string, t target) Verdict {
	snap, _, ok := strings.Cut(t.token, ":")
	if !ok || !isSnapshotID(snap) {
		// A bare row number is a row of window_id's current snapshot: the window was judged by its id (aimed).
		if len(t.wids) > 0 {
			return Verdict{}
		}
		name := l.listed(settings, false)[0]
		return Verdict{Why: TokenUnchecked(where, name), Rule: "token", App: name}
	}
	for i, w := range l.windows {
		if l.kinds[i] != settings {
			continue
		}
		switch l.ownerOf(snap, w) {
		case otherWindow:
			continue
		case thisWindow:
			return Verdict{Why: Refusal(where, "it would operate "+l.names[i]), Rule: "named", App: l.names[i]}
		default:
			return Verdict{Why: TokenUnchecked(where, l.names[i]), Rule: "token", App: l.names[i]}
		}
	}
	return Verdict{}
}

// sameWindow are the since_status answers of a read of the window the snapshot is from.
var sameWindow = map[string]bool{"no_change": true, "changed": true, "diff": true, "ok": true, "view_changed": true,
	"diff_too_large": true}

// ownerOf reads w with since: snap (the smallest read: one element, no screenshot).
func (l *Look) ownerOf(snap string, w Window) owner {
	key := snap + " " + strconv.Itoa(w.ID)
	if o, ok := l.owners[key]; ok {
		return o
	}
	o := unknownOwner
	if w.ID > 0 && w.PID > 0 {
		out, ok := l.read("get_window_state", map[string]any{"pid": w.PID, "window_id": w.ID, "since": snap,
			"max_elements": 1, "include_screenshot": false})
		var r struct {
			Status string `json:"since_status"`
		}
		if ok && json.Unmarshal(out, &r) == nil {
			switch {
			case r.Status == "other_window":
				o = otherWindow
			case sameWindow[r.Status]:
				o = thisWindow
			}
		}
	}
	l.owners[key] = o
	return o
}

// isSnapshotID: "s" and hex digits, as Cua Driver's snapshot ids are.
func isSnapshotID(s string) bool {
	if len(s) < 2 || s[0] != 's' {
		return false
	}
	for _, c := range s[1:] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// frontCheck decides keys that go to the frontmost app: refused when that is System Settings, or not known.
func (l *Look) frontCheck(where, name string) Verdict {
	if l.loadApps(true) {
		for _, a := range l.apps {
			if !a.Active {
				continue
			}
			if Is(a.BundleID, a.Name) {
				return Verdict{Why: FrontRefusal(where, a.Name), Rule: "front", App: a.Name}
			}
			return Verdict{}
		}
	}
	return Verdict{Why: UnplacedRefusal(where, name), Rule: "unplaced", App: name}
}

// pointCheck decides t's points: refused where a window of System Settings can be the one on top (open names it, for
// the text when the points are not in a known window).
func (l *Look) pointCheck(where string, t target, open string) Verdict {
	covered := func(name string) Verdict {
		return Verdict{Why: CoveredRefusal(where, name), Rule: "covered", App: name}
	}
	if t.desktop { // the screen's pixels: from its top-left corner, out to the farthest window
		var far point
		for _, w := range l.windows {
			if w.OnScreen && w.Bounds != nil {
				far.x, far.y = max(far.x, w.Bounds.X+w.Bounds.Width), max(far.y, w.Bounds.Y+w.Bounds.Height)
			}
		}
		if t.zoom { // a zoom image's pixels: anywhere on the screen
			return covered(open)
		}
		for _, p := range t.points {
			if name, ok := l.coveredAlong(ray(Rect{Width: far.x, Height: far.y}, p)); ok {
				return covered(name)
			}
		}
		return Verdict{}
	}
	frames := l.framesOf(t)
	if len(frames) == 0 {
		return Verdict{Why: UnplacedRefusal(where, open), Rule: "unplaced", App: open}
	}
	for _, f := range frames {
		if t.zoom { // a zoom image's pixels: anywhere in the window
			if name, ok := l.coveredIn(f); ok {
				return covered(name)
			}
			continue
		}
		for _, p := range t.points {
			if name, ok := l.coveredAlong(ray(*f.Bounds, p)); ok {
				return covered(name)
			}
		}
	}
	return Verdict{}
}

// framesOf is the windows on the screen t's window-local points can be in: its window ids', else its pids', apps' or
// titles' (every one of them, as Cua Driver may pick any).
func (l *Look) framesOf(t target) []Window {
	var out []Window
	pick := func(match func(Window) bool) {
		for _, w := range l.windows {
			if w.OnScreen && w.Bounds != nil && match(w) {
				out = append(out, w)
			}
		}
	}
	switch {
	case len(t.wids) > 0:
		pick(func(w Window) bool { return slices.Contains(t.wids, w.ID) })
	case len(t.pids) > 0:
		pick(func(w Window) bool { return slices.Contains(t.pids, w.PID) })
	case t.app != "":
		var pids []int
		for _, a := range l.apps {
			if a.PID > 0 && (strings.EqualFold(a.Name, t.app) || strings.EqualFold(a.BundleID, t.app)) {
				pids = append(pids, a.PID)
			}
		}
		pick(func(w Window) bool { return slices.Contains(pids, w.PID) || strings.EqualFold(w.AppName, t.app) })
		if t.title != "" {
			out = slices.DeleteFunc(out, func(w Window) bool {
				return !strings.Contains(strings.ToLower(w.Title), strings.ToLower(t.title))
			})
		}
	case t.title != "":
		pick(func(w Window) bool { return strings.Contains(strings.ToLower(w.Title), strings.ToLower(t.title)) })
	}
	return out
}

type point struct{ x, y float64 }

// ray is where window-local pixels p can land in r: from r's origin plus p at scale 2 out along the same line to
// r's edge (at scale 2 when p is not toward r).
func ray(r Rect, p point) (from, to point) {
	lo, hi := 0.5, -1.0
	if p.x > 0 {
		hi = r.Width / p.x
	}
	if p.y > 0 && (hi < 0 || r.Height/p.y < hi) {
		hi = r.Height / p.y
	}
	if p.x < 0 || p.y < 0 || hi < lo {
		hi = lo
	}
	return point{r.X + lo*p.x, r.Y + lo*p.y}, point{r.X + hi*p.x, r.Y + hi*p.y}
}

// coveredAlong is the System Settings window on top somewhere between a and b, if any: looked at each place the line
// crosses a window's edge, and between.
func (l *Look) coveredAlong(a, b point) (string, bool) {
	ts := []float64{0, 1}
	dx, dy := b.x-a.x, b.y-a.y
	cross := func(edge, from, d float64) {
		if t := (edge - from) / d; d != 0 && t > 0 && t < 1 {
			ts = append(ts, t)
		}
	}
	for _, w := range l.windows {
		if w.OnScreen && w.Bounds != nil {
			r := *w.Bounds
			cross(r.X, a.x, dx)
			cross(r.X+r.Width, a.x, dx)
			cross(r.Y, a.y, dy)
			cross(r.Y+r.Height, a.y, dy)
		}
	}
	sort.Float64s(ts)
	at := func(t float64) point { return point{a.x + t*dx, a.y + t*dy} }
	for i, t := range ts {
		if name, ok := l.coveredAt(at(t)); ok {
			return name, true
		}
		if i+1 < len(ts) {
			if name, ok := l.coveredAt(at((t + ts[i+1]) / 2)); ok {
				return name, true
			}
		}
	}
	return "", false
}

// coveredAt is the System Settings window on top at p, or one that has p while the stacking order is not known.
func (l *Look) coveredAt(p point) (string, bool) {
	top, mine, unknown := -1, -1, false
	for i, w := range l.windows {
		if !w.OnScreen || w.Bounds == nil || !w.Bounds.has(p) {
			continue
		}
		if l.kinds[i] == settings && mine < 0 {
			mine = i
		}
		if w.Z == nil {
			unknown = true
			continue
		}
		if top < 0 || above(w, l.windows[top]) {
			top = i
		}
	}
	switch {
	case mine < 0:
		return "", false
	case unknown || top < 0:
		return l.names[mine], true
	case l.kinds[top] == settings:
		return l.names[top], true
	}
	return "", false
}

// coveredIn is a System Settings window over some of f (in front of it, or in an unknown order).
func (l *Look) coveredIn(f Window) (string, bool) {
	r := *f.Bounds
	for i, w := range l.windows {
		if l.kinds[i] != settings || !w.OnScreen || w.Bounds == nil {
			continue
		}
		b := *w.Bounds
		overlap := b.X < r.X+r.Width && r.X < b.X+b.Width && b.Y < r.Y+r.Height && r.Y < b.Y+b.Height
		if overlap && (w.Z == nil || f.Z == nil || above(w, f)) {
			return l.names[i], true
		}
	}
	return "", false
}

// above: a is in front of b (both with a z_index).
func above(a, b Window) bool {
	if a.Layer != b.Layer {
		return a.Layer > b.Layer
	}
	return *a.Z > *b.Z
}

// target is where one action acts, as its arguments say; in steps, a step that names no window has the call's or
// the step before's.
type target struct {
	tool   string
	app    string // an app by name or bundle id
	title  string // a window by its title
	named  bool   // an app tool names its app (launch_app's name or bundle_id)
	pids   []int
	wids   []int
	token  string
	points []point // x, y; a drag's from and to
	zoom   bool    // the points are in a zoom image's pixels
	// desktop: the points are the screen's pixels, and keys go to the frontmost app.
	desktop bool
}

// placed says whether t names the window or app it acts on.
func (t target) placed() bool {
	return t.app != "" || t.title != "" || len(t.pids) > 0 || len(t.wids) > 0 || t.token != ""
}

// targetOf reads an action's arguments.
func targetOf(tool string, raw json.RawMessage) target {
	var a map[string]json.RawMessage
	_ = json.Unmarshal(raw, &a)
	t := target{tool: tool}
	text := func(k string) string {
		var s string
		_ = json.Unmarshal(a[k], &s)
		return strings.TrimSpace(s)
	}
	number := func(k string) (float64, bool) {
		var f float64
		v, has := a[k]
		return f, has && json.Unmarshal(v, &f) == nil
	}
	t.app, t.title, t.token = text("app"), text("window"), text("element_token")
	t.named = appTools[tool] && (text("name") != "" || text("bundle_id") != "")
	if p, ok := number("pid"); ok && p > 0 {
		t.pids = append(t.pids, int(p))
	}
	if w, ok := number("window_id"); ok && w > 0 {
		t.wids = append(t.wids, int(w))
	}
	var aim struct {
		Kind     string `json:"kind"`
		PID      int    `json:"pid"`
		WindowID int    `json:"window_id"`
	}
	if json.Unmarshal(a["target"], &aim) == nil {
		if aim.PID > 0 && !slices.Contains(t.pids, aim.PID) {
			t.pids = append(t.pids, aim.PID)
		}
		if aim.WindowID > 0 && !slices.Contains(t.wids, aim.WindowID) {
			t.wids = append(t.wids, aim.WindowID)
		}
		t.desktop = aim.Kind == "desktop"
	}
	t.desktop = t.desktop || text("scope") == "desktop"
	for _, k := range [][2]string{{"x", "y"}, {"from_x", "from_y"}, {"to_x", "to_y"}} {
		x, hasX := number(k[0])
		y, hasY := number(k[1])
		if hasX || hasY {
			t.points = append(t.points, point{x, y})
		}
	}
	_ = json.Unmarshal(a["from_zoom"], &t.zoom)
	return t
}

// stepsTargets is each action of a run_steps call: a step names its tool ({"tool": …, "args": …} or {<tool>: args});
// one naming no window has the call's default (app, window, pid, window_id), or the window of the step before.
// A step that is only a check (wait_for, expect) acts on nothing.
func stepsTargets(call json.RawMessage) []target {
	var c struct {
		Steps []map[string]json.RawMessage `json:"steps"`
	}
	_ = json.Unmarshal(call, &c)
	last := targetOf("", call)
	var out []target
	for _, s := range c.Steps {
		var actions [][2]any
		if raw, has := s["tool"]; has {
			var tool string
			_ = json.Unmarshal(raw, &tool)
			actions = append(actions, [2]any{tool, s["args"]})
		} else {
			keys := make([]string, 0, len(s))
			for k := range s {
				if k != "wait_for" && k != "expect" && k != "timeout_ms" {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				actions = append(actions, [2]any{k, s[k]})
			}
		}
		for _, x := range actions {
			raw, _ := x[1].(json.RawMessage)
			t := targetOf(x[0].(string), raw)
			if t.placed() {
				last = t
			} else {
				t.app, t.title, t.pids, t.wids, t.token = last.app, last.title, last.pids, last.wids, last.token
			}
			out = append(out, t)
		}
	}
	return out
}
