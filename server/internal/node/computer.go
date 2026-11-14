package node

// Operating this Mac for the assistant through Cua Driver (the built-in plugin cua, docs/extensions/computer-use.md). The hub
// asks only about a machine the user turned it on for: a node never looks for cua-driver by itself.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png" // cua-driver's screenshots
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// cuaBin is the cua-driver CLI, "" when not installed (tests set it). The resident node (launchd) has only the
// system's PATH: also where Cua's installer puts it, and the app's own copy.
var cuaBin = findCua

// cuaBundle is CuaDriver.app: opening it starts the daemon, which holds the macOS permissions (TCC) under its own name.
const cuaBundle = "com.trycua.driver"

func findCua() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	if p, err := exec.LookPath("cua-driver"); err == nil {
		return p
	}
	var places []string
	if home, err := os.UserHomeDir(); err == nil {
		places = append(places, filepath.Join(home, ".local", "bin", "cua-driver"))
	}
	places = append(places, "/Applications/CuaDriver.app/Contents/MacOS/cua-driver")
	for _, p := range places {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// cuaRun runs cua-driver with args and returns what it printed (stdout and stderr together).
func cuaRun(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	return string(out), err
}

// computerStatus is what this Mac has of Cua Driver now (proto.Computer; Enabled is the hub's to say).
func computerStatus(ctx context.Context) proto.Computer {
	c := proto.Computer{}
	if bin := cuaBin(); bin != "" {
		c.Installed = true
		if out, err := cuaRun(ctx, bin, "--version"); err == nil {
			c.Version = parseCuaVersion(out)
		}
		out, _ := cuaRun(ctx, bin, "status")
		c.Daemon = cuaDaemonRunning(out)
		c.Accessibility, c.ScreenRecording = "unknown", "unknown"
		if c.Daemon {
			if out, err := cuaRun(ctx, bin, "permissions", "status", "--json"); err == nil {
				c.Accessibility, c.ScreenRecording = parseCuaPermissions(out)
			}
		}
	}
	if out, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-n", "Root", "-d1").Output(); err == nil {
		c.Desktop = parseConsole(string(out), os.Getuid())
	}
	c.ScreenSharing = screenSharing(ctx)
	c.Displays = screenDisplays(ctx, c.ScreenSharing)
	c.ScreenVideo = screenVideoStatus(ctx) // #3954: nil until advertiseScreenVideo
	c.Ready, c.Note = computerReady(c)
	c.Backend = "cua"
	c.Job = currentJob()
	// Last, after the job was read: the hub keeps the answer with the latest checked, whatever order the answers of
	// questions asked at once arrive in.
	c.Checked = time.Now().UTC()
	return c
}

// screenPort is where macOS's Screen Sharing (VNC) listens; the live screen reaches it through the hub's port relay.
const screenPort = 5900

// screenSharing: macOS's Screen Sharing answers on 127.0.0.1:5900.
func screenSharing(ctx context.Context) bool {
	return answersRFB(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(screenPort)))
}

// answersRFB: what listens at addr greets with RFB's version ("RFB 003.889" from macOS).
func answersRFB(ctx context.Context, addr string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	greeting := make([]byte, 4)
	_, err = io.ReadFull(c, greeting)
	return err == nil && string(greeting) == "RFB "
}

// parseCuaVersion: "cua-driver 0.38.0" → "0.38.0".
func parseCuaVersion(out string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if f := strings.Fields(line); len(f) > 0 {
		return f[len(f)-1]
	}
	return ""
}

// cuaDaemonRunning reads `cua-driver status`: "Cua Driver daemon is running" (or "… is not running").
func cuaDaemonRunning(out string) bool {
	return strings.Contains(out, "daemon is running")
}

// parseCuaPermissions reads `cua-driver permissions status --json`: booleans when the daemon answered; without it
// (daemon_running false) neither can be told.
func parseCuaPermissions(out string) (accessibility, screenRecording string) {
	var p map[string]json.RawMessage
	if json.Unmarshal([]byte(out), &p) != nil {
		return "unknown", "unknown"
	}
	grant := func(raw json.RawMessage) string {
		switch strings.TrimSpace(string(raw)) {
		case "true":
			return "granted"
		case "false":
			return "missing"
		}
		return "unknown"
	}
	return grant(p["accessibility"]), grant(p["screen_recording"])
}

var consoleLocked = lazyre.New(`"IOConsoleLocked" = Yes`)

// parseConsole reads `ioreg -n Root -d1`: whose desktop is on screen (IOConsoleUsers) and whether it is locked.
// active: uid's desktop is on the console and unlocked; locked; away: uid has no desktop on the console (logged out,
// another user switched in, the login window). "" when ioreg says nothing about it.
func parseConsole(out string, uid int) string {
	_, users, ok := strings.Cut(out, `"IOConsoleUsers" = (`)
	if !ok {
		return ""
	}
	mine := regexp.MustCompile(`"kCGSSessionUserIDKey"=` + strconv.Itoa(uid) + `\b`)
	for _, d := range strings.Split(users, "{") {
		if !mine.MatchString(d) || !strings.Contains(d, `"kCGSSessionOnConsoleKey"=Yes`) {
			continue
		}
		if strings.Contains(d, `"CGSSessionScreenIsLocked"=Yes`) || consoleLocked.MatchString(out) {
			return "locked"
		}
		return "active"
	}
	return "away"
}

// computerReady: whether the assistant can operate the Mac now, and if not what is missing, for the app.
func computerReady(c proto.Computer) (bool, string) {
	var missing []string
	if c.Accessibility == "missing" {
		missing = append(missing, "辅助功能")
	}
	if c.ScreenRecording == "missing" {
		missing = append(missing, "屏幕录制")
	}
	switch {
	case !c.Installed:
		return false, "这台 Mac 上没有装 Cua Driver"
	case !c.Daemon:
		return false, "Cua Driver 没在运行，助手要用时会自动打开它"
	case len(missing) > 0:
		return false, "Cua Driver 还没有" + strings.Join(missing, "、") + "权限：在这台 Mac 上运行 cua-driver permissions grant，在弹出的窗口里允许"
	case c.Accessibility != "granted" || c.ScreenRecording != "granted":
		return false, "读不到 Cua Driver 的权限"
	case c.Desktop == "locked":
		return false, "这台 Mac 锁屏了：解锁后才能操控。屏幕共享开着时，可以在它的画面上输登录密码解锁"
	case c.Desktop == "away":
		return false, "这台 Mac 现在没有你登录的桌面（退出登录或切到了别的用户），操控不了"
	}
	return true, ""
}

// computerTools are the Cua Driver tools the hub may call: reading, and run_script / run_steps, which act through the
// daemon's own checks; start_session starts the call's own label again after it ended (#7590, the hub's
// computer_session.go says when). Not its settings, updates, extensions or clipboard.
var computerTools = map[string]bool{
	"get_desktop_state": true, "get_accessibility_tree": true, "get_window_state": true, "list_apps": true, "list_windows": true,
	"run_script": true, "run_steps": true, "start_session": true,
}

// nodeSessions are the labels the node reads the screen under for itself (cua_gate.go). Cua Driver ends a named
// session after 5 minutes without a call, and then refuses every call under it ("session has ended") until
// start_session; no one stops these from the screen (they only read: no cursor, no bar), so one that ended is started
// again and the call retried once (#7590).
var nodeSessions = map[string]bool{"persona-gate": true}

// openPath opens an app (startCua); a test's stand-in records what it was asked.
var openPath = "/usr/bin/open"

// computerCallTimeout: a run_script may take up to 120 s (its timeout_ms); a read is a few seconds.
const computerCallTimeout = 150 * time.Second

// computerCall runs one Cua Driver tool (computer.call): args go to `cua-driver call` with the hub's session label,
// so element tokens from one call work in the next one of the same conversation; a screenshot comes back as an image
// beside the tool's JSON, not inside it.
func computerCall(ctx context.Context, params json.RawMessage) (*proto.ComputerResult, error) {
	var p struct {
		Session string          `json:"session"`
		Tool    string          `json:"tool"`
		Args    json.RawMessage `json:"args"`
		// The Mac stays awake for a while after (awake.go), as the plugin's "Keep awake while operating or with VNC open" says.
		KeepAwake bool `json:"keep_awake"`
		// The plugin's "Click sound": none, foreground or all (cua_extras.go).
		ClickSound string `json:"click_sound"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if !computerTools[p.Tool] {
		return nil, fmt.Errorf("computer.call: %q is not a tool the hub may call", p.Tool)
	}
	if p.KeepAwake {
		awake.operated()
	}
	bin := cuaBin()
	if bin == "" {
		return nil, &rpc.Error{Code: "not_installed", Message: "这台 Mac 上没有装 Cua Driver"}
	}
	args := map[string]any{}
	if len(p.Args) > 0 {
		if err := json.Unmarshal(p.Args, &args); err != nil {
			return nil, errors.New("computer.call: args must be an object")
		}
	}
	if args == nil { // "args": null: Unmarshal set the map to nil, and writing the session label to it would panic
		args = map[string]any{}
	}
	if p.Tool == "start_session" { // the call's own label, nothing else: no other's, no cursor or capture settings
		if p.Session == "" {
			return nil, errors.New("computer.call: start_session needs the call's session label")
		}
		args = map[string]any{}
	}
	if _, ok := args["session"]; !ok && p.Session != "" {
		args["session"] = p.Session
	}
	playClicks(p.ClickSound, p.Tool, args)
	input, _ := json.Marshal(args)
	if out, _ := cuaRun(ctx, bin, "status"); !cuaDaemonRunning(out) {
		startCua(ctx, bin)
	}
	res, err := cuaInvoke(ctx, bin, p.Tool, input)
	if err != nil && nodeSessions[p.Session] && cuaEnded(err) {
		start, _ := json.Marshal(map[string]any{"session": p.Session})
		if _, serr := cuaInvoke(ctx, bin, "start_session", start); serr == nil {
			res, err = cuaInvoke(ctx, bin, p.Tool, input)
		}
	}
	return res, err
}

// cuaEnded: Cua Driver refused a call because its session had ended.
func cuaEnded(err error) bool { return strings.Contains(err.Error(), "session has ended") }

// cuaInvoke runs `cua-driver call tool` with input on stdin: its JSON, or what it said on failure as
// "cua-driver <tool>: …" (the hub tells Cua Driver's own answers by that), and the screenshot it wrote.
func cuaInvoke(ctx context.Context, bin, tool string, input []byte) (*proto.ComputerResult, error) {
	dir, err := os.MkdirTemp("", "persona-computer-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	shot := filepath.Join(dir, "screenshot")
	cctx, cancel := context.WithTimeout(ctx, computerCallTimeout)
	defer cancel()
	// The input on stdin: no length limit, and not in the process list for anyone to read.
	cmd := exec.CommandContext(cctx, bin, "call", "--screenshot-out-file", shot, tool)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	res := &proto.ComputerResult{}
	switch out := bytes.TrimSpace(stdout.Bytes()); {
	case len(out) > 0 && json.Valid(out):
		res.Output = json.RawMessage(out) // refusals too: they say what to do next
	case runErr != nil:
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = runErr.Error()
		}
		if cctx.Err() != nil {
			msg = "超时（" + computerCallTimeout.String() + "）"
		}
		return nil, fmt.Errorf("cua-driver %s: %s", tool, firstLine(msg))
	default:
		res.Text = string(out)
	}
	res.Image = screenshotImage(shot)
	return res, nil
}

// startCua opens CuaDriver.app in the background (its daemon, under its own permissions) and waits for it a little;
// without the agent cursor while this node has turned that off (cua_extras.go).
func startCua(ctx context.Context, bin string) {
	open := []string{"-g", "-b", cuaBundle}
	if cursorOff.Load() {
		open = append(open, "--args", "serve", "--no-overlay")
	}
	if err := exec.CommandContext(ctx, openPath, open...).Run(); err != nil {
		return
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if out, _ := cuaRun(ctx, bin, "status"); cuaDaemonRunning(out) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// screenshotImage is the screenshot cua-driver wrote, as a JPEG when that is smaller (a window's transparent corners
// on white): a desktop's PNG runs to megabytes.
func screenshotImage(path string) *proto.Image {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return &proto.Image{Mime: http.DetectContentType(data), Data: base64.StdEncoding.EncodeToString(data)}
	}
	b := img.Bounds()
	// The bytes' own type: a screenshot may come as a JPEG (the iPhone bridge's), not only a PNG.
	out := &proto.Image{Mime: http.DetectContentType(data), Data: base64.StdEncoding.EncodeToString(data), Width: b.Dx(), Height: b.Dy()}
	flat := image.NewRGBA(b)
	draw.Draw(flat, b, &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(flat, b, img, b.Min, draw.Over)
	var j bytes.Buffer
	if jpeg.Encode(&j, flat, &jpeg.Options{Quality: 85}) == nil && j.Len() < len(data) {
		out.Mime, out.Data = "image/jpeg", base64.StdEncoding.EncodeToString(j.Bytes())
	}
	return out
}
