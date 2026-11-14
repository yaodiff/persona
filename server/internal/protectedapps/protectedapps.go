// Package protectedapps is the rule that Persona never operates System Settings or macOS's permission and
// authorization prompts for anyone (#4336, #4349, #7560; docs/extensions/computer-use.md "c) Per-App approval").
// macOS's privacy permissions are meant to be answered by a person. The assistant's computer_run (extmgr) and the
// Agents' own Cua Driver tools (the node's PreToolUse gate) both go by this package: which apps, how a call names the
// apps it touches, and where on the Mac a call acts (check.go).
package protectedapps

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// settingsIDs and settingsNames: System Settings, by both bundle ids it has had and by the names macOS gives it, in
// the languages it ships.
var settingsIDs = map[string]bool{"com.apple.systempreferences": true, "com.apple.settings": true}

var settingsNames = []string{
	"system settings", "system preferences",
	// i18n-ignore: the names macOS itself gives System Settings, matched against what a call or a window says
	"系统设置", "系統設定", "システム設定", "系统偏好设置", "システム環境設定",
}

// promptIDs and promptNames: the processes that show macOS's permission and authorization prompts (the privacy
// alerts, administrator sheets, Touch ID / password prompts, the Screen Recording alert and the Accessibility alert of
// older systems), by bundle id and by process name, for Cua Driver's app list does not carry them (they run only
// while they show). They are modal and float on top, so while one shows nothing is operated at all.
var promptIDs = map[string]bool{
	"com.apple.usernotificationcenter": true, "com.apple.securityagent": true,
	"com.apple.localauthentication.uiagent": true, "com.apple.accessibility.universalaccessauthwarn": true,
}

var promptNames = []string{"usernotificationcenter", "securityagent", "coreautha", "universalaccessauthwarn"}

// Is says whether an app is one Persona never operates: by bundle id, or by name (a name that is a bundle id counts as
// one). A bundle id that is not a real one ("name:…", "") is matched by the name alone.
func Is(bundleID, name string) bool { return isSettings(bundleID, name) || IsPrompt(bundleID, name) }

// IsPrompt says whether an app is one of the permission and password prompts' processes (Is, without System
// Settings).
func IsPrompt(bundleID, name string) bool {
	id, name := strings.ToLower(strings.TrimSpace(bundleID)), strings.ToLower(strings.TrimSpace(name))
	return promptIDs[id] || promptIDs[name] || slices.Contains(promptNames, name)
}

func isSettings(bundleID, name string) bool {
	id, name := strings.ToLower(strings.TrimSpace(bundleID)), strings.ToLower(strings.TrimSpace(name))
	return settingsIDs[id] || settingsIDs[name] || slices.Contains(settingsNames, name)
}

// App is one of Cua Driver's list_apps: bundle id, name, pid while running, and whether it is the frontmost app.
type App struct {
	Name     string `json:"name"`
	BundleID string `json:"bundle_id"`
	PID      int    `json:"pid"`
	Running  bool   `json:"running"`
	Active   bool   `json:"active"`
}

// Window is one of Cua Driver's list_windows: its bounds in screen points (top-left origin), and its place in the
// stacking order (a higher z_index is in front; null when the order is not known).
type Window struct {
	ID       int    `json:"window_id"`
	AppName  string `json:"app_name"`
	Title    string `json:"title"`
	PID      int    `json:"pid"`
	OnScreen bool   `json:"is_on_screen"`
	Bounds   *Rect  `json:"bounds"`
	Layer    int    `json:"layer"`
	Z        *int   `json:"z_index"`
}

// Rect is a window's bounds, in screen points.
type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// has says whether p is inside r (its right and bottom edges are outside).
func (r Rect) has(p point) bool {
	return p.x >= r.X && p.x < r.X+r.Width && p.y >= r.Y && p.y < r.Y+r.Height
}

// Apps reads list_apps' answer (nil when it does not read).
func Apps(raw json.RawMessage) []App {
	var out struct {
		Apps []App `json:"apps"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Apps
}

// Windows reads list_windows' answer; ok is false when it has no window list at all (not a list_windows answer).
func Windows(raw json.RawMessage) (windows []Window, ok bool) {
	var out struct {
		Windows *[]Window `json:"windows"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Windows == nil {
		return nil, false
	}
	return *out.Windows, true
}

// AppOf is the running app with that pid in apps, ok false when none is.
func AppOf(apps []App, pid int) (App, bool) {
	if pid <= 0 {
		return App{}, false
	}
	for _, a := range apps {
		if a.PID == pid {
			return a, true
		}
	}
	return App{}, false
}

var (
	getAppLiteral = lazyre.New(`getApp\(\s*(?:"([^"\\]+)"|'([^'\\]+)')\s*\)`)
	launchLiteral = lazyre.New(`launch\(\s*(?:"([^"\\]+)"|'([^'\\]+)')\s*\)`)
	// getAppWord and launchWord count every occurrence of the callee's name. A call the literal patterns cannot
	// read (space or a comment before the parentheses, a computed callee, an argument that is not a plain string)
	// then makes the script's apps unknown instead of being missed: comparing substring counts of "getApp(" would
	// miss the same calls the literal pattern misses, and judge the script fully known.
	getAppWord = lazyre.New(`\bgetApp\b`)
	launchWord = lazyre.New(`\blaunch\b`)
)

// ScriptApps is the apps a Cua run_script names with literal cua.getApp("…") / cua.launch("…") calls, and whether
// that is all of them (false: one named by expression, a call the literal reader cannot read, or none at all).
func ScriptApps(script string) (apps []string, all bool) {
	add := func(a string) {
		if a = strings.TrimSpace(a); a != "" && !slices.Contains(apps, a) {
			apps = append(apps, a)
		}
	}
	for _, m := range getAppLiteral.FindAllStringSubmatch(script, -1) {
		add(m[1] + m[2])
	}
	for _, m := range launchLiteral.FindAllStringSubmatch(script, -1) {
		add(m[1] + m[2])
	}
	return apps, len(apps) > 0 && len(getAppWord.FindAllString(script, -1)) == len(getAppLiteral.FindAllString(script, -1)) &&
		len(launchWord.FindAllString(script, -1)) == len(launchLiteral.FindAllString(script, -1))
}

// StepsApps is the apps Cua run_steps steps name (each step's own app, else the call's), an app a launch_app step
// opens included, and whether that is all they may touch (false: a step with no app of its own or the call's, a
// launch_app naming no app).
func StepsApps(app string, steps json.RawMessage) (apps []string, all bool) {
	add := func(a string) {
		if a = strings.TrimSpace(a); a != "" && !slices.Contains(apps, a) {
			apps = append(apps, a)
		}
	}
	var list []map[string]json.RawMessage
	_ = json.Unmarshal(steps, &list)
	all = len(list) > 0
	for _, s := range list {
		if opened, ok := LaunchTarget(s); ok {
			if opened == "" {
				all = false
			} else {
				add(opened)
			}
			continue
		}
		found := stepApp(s)
		if found == "" {
			found = app
		}
		if found == "" {
			all = false
			continue
		}
		add(found)
	}
	return apps, all
}

// stepApp is the app a step names itself, in either form ({"click": {"app": …}} or {"tool": …, "args": {"app": …}}).
func stepApp(s map[string]json.RawMessage) string {
	found := ""
	for k, raw := range s {
		if k == "wait_for" || k == "expect" || k == "tool" {
			continue
		}
		var named struct {
			App string `json:"app"`
		}
		if json.Unmarshal(raw, &named) == nil && strings.TrimSpace(named.App) != "" {
			found = named.App
		}
	}
	return found
}

// LaunchTarget is the app a launch_app step opens, {"launch_app": {"name": …}} or {"tool": "launch_app", "args": {…}}:
// its name or bundle id, "" when it names neither (not the call's default app: what opens then is the backend's
// choice). ok is false for any other step.
func LaunchTarget(s map[string]json.RawMessage) (opened string, ok bool) {
	var tool string
	args := s["launch_app"]
	if raw, has := s["tool"]; has {
		_ = json.Unmarshal(raw, &tool)
	}
	switch {
	case tool == "launch_app":
		args = s["args"]
	case tool != "" || args == nil:
		return "", false
	}
	var a struct {
		Name     string `json:"name"`
		BundleID string `json:"bundle_id"`
	}
	_ = json.Unmarshal(args, &a)
	if opened = strings.TrimSpace(a.Name); opened == "" {
		opened = strings.TrimSpace(a.BundleID)
	}
	return opened, true
}

// UnnamedSteps: some step names no app of its own and the call names none either (it acts on whatever has the
// focus, or where it points).
func UnnamedSteps(app string, steps json.RawMessage) bool {
	if strings.TrimSpace(app) != "" {
		return false
	}
	var list []map[string]json.RawMessage
	_ = json.Unmarshal(steps, &list)
	for _, s := range list {
		if opened, ok := LaunchTarget(s); ok {
			if opened == "" {
				return true
			}
			continue
		}
		if stepApp(s) == "" {
			return true
		}
	}
	return false
}

// reads are Cua Driver's tools that only look (or manage Cua itself) and never act on an app. Any other tool, one
// added later included, counts as acting.
var reads = map[string]bool{
	"get_window_state": true, "get_desktop_state": true, "get_accessibility_tree": true, "get_browser_state": true,
	"list_apps": true, "list_windows": true, "get_screen_size": true, "get_cursor_position": true, "zoom": true,
	"parse_visual_regions": true, "verify_state": true, "check_permissions": true, "get_config": true, "get_guide": true,
	"health_report": true, "check_for_update": true, "get_session": true, "get_session_state": true, "list_sessions": true,
	"get_recording_state": true, "get_agent_cursor_state": true, "start_session": true, "end_session": true,
	"history_status": true, "history_query": true,
}

// Acts says whether a Cua Driver tool may act on an app (anything but the reads).
func Acts(tool string) bool { return !reads[tool] }

// Refusal is what the model is told when a call is refused on where (a Mac's name, or "this Mac") for why: it would
// operate one of the protected apps itself. It passes the gist on to the user. The texts below are for the other
// cases, each saying what can still be done (#7560): only the protected app is off limits.
func Refusal(where, why string) string {
	return "Refused on " + where + ": " + why + ". Persona never operates System Settings or macOS's permission and " +
		"password prompts for anyone. Tell the user to do this themselves, on the Mac or through its live screen in the " +
		"Persona app. Do not retry it or look for another way."
}

// PromptRefusal: a permission or password prompt (names) is on where's screen. Modal and on top, it could take any
// click or key, so nothing is operated while one shows.
func PromptRefusal(where, names string) string {
	return "Refused on " + where + ": a macOS permission or password prompt (" + names + ") is on its screen, and " +
		"while one shows Persona operates nothing there, since a click or key could answer it. Tell the user to answer it " +
		"themselves, on the Mac or through its live screen in the Persona app; then this can be tried again."
}

// CoveredRefusal: app is the window on top where a pointer action would land.
func CoveredRefusal(where, app string) string {
	return "Refused on " + where + ": " + app + " is on top where this would click, and Persona never operates it. " +
		"Only " + app + " is off limits; other apps can still be operated. Act on the element by its element_token " +
		"instead of a point, or ask the user to move " + app + " out of the way or close it."
}

// FrontRefusal: keys with no target while app is the frontmost app.
func FrontRefusal(where, app string) string {
	return "Refused on " + where + ": " + app + " is the frontmost app, so keys sent without a target would go to it. " +
		"Only " + app + " is off limits; other apps can still be operated. Say which app the keys are for (its name, " +
		"pid or an element_token), or ask the user to switch away from " + app + "."
}

// ScriptRefusal: a script while app is open on the screen.
func ScriptRefusal(where, app string) string {
	return "Refused on " + where + ": scripts don't run while " + app + " is open on its screen, because what a script " +
		"will click can't be checked beforehand. Only " + app + " is off limits: use steps instead, which are checked " +
		"one by one, or ask the user to close " + app + "."
}

// UnplacedRefusal: where a call acts can't be told while app is open.
func UnplacedRefusal(where, app string) string {
	return "Refused on " + where + ": Persona can't tell where this acts, and " + app + " is open there. Say which app " +
		"or window it is for (app, pid and window_id, or an element_token); only " + app + " is off limits, other apps " +
		"can still be operated."
}

// TokenUnchecked: an element token that could not be traced to its window while app is open.
func TokenUnchecked(where, app string) string {
	return "Nothing was done on " + where + ": Persona could not tell which window this element token belongs to, and " +
		app + " is open there. Read the window again and use its new element tokens."
}

// UnreadableScript is what the model is told about a script whose apps cannot all be read.
const UnreadableScript = "Refused: this script names the apps it operates by expression, or names none, so they " +
	"cannot be checked. Name each one with a literal cua.getApp(\"…\") or cua.launch(\"…\"), or use steps."

// Unchecked is what the model is told when the windows on the screen could not be read to check.
const Unchecked = "Nothing was done: the Mac's windows could not be read to check for System Settings or a " +
	"permission prompt. Try again shortly."
