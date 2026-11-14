package node

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCuaParsing(t *testing.T) {
	if v := parseCuaVersion("cua-driver 0.38.0\n"); v != "0.38.0" {
		t.Fatal(v)
	}
	if !cuaDaemonRunning("Cua Driver daemon is running\n  socket: …") || cuaDaemonRunning("Cua Driver daemon is not running") {
		t.Fatal("status")
	}
	if a, s := parseCuaPermissions(`{"accessibility": true, "screen_recording": false, "source": {}}`); a != "granted" || s != "missing" {
		t.Fatal(a, s)
	}
	if a, s := parseCuaPermissions(`{"daemon_running": false, "reason": "No CuaDriver daemon is running"}`); a != "unknown" || s != "unknown" {
		t.Fatal("without the daemon neither is known:", a, s)
	}
}

// ioreg's console: whose desktop is on screen and whether it is locked.
func TestParseConsole(t *testing.T) {
	user := func(uid string, extra string) string {
		return `{"kCGSSessionOnConsoleKey"=Yes,"kCGSSessionUserIDKey"=` + uid + `,"kCGSSessionLoginDoneKey"=Yes` + extra + `}`
	}
	root := func(locked string, users ...string) string {
		return `+-o Root  <class IORegistryEntry, id 0x100000100, retain 25>
    {
      "IOConsoleLocked" = ` + locked + `
      "IOConsoleUsers" = (` + strings.Join(users, ",") + `)
    }`
	}
	for _, c := range []struct{ name, out, want string }{
		{"mine, unlocked", root("No", user("501", "")), "active"},
		{"mine, locked", root("Yes", user("501", `,"CGSSessionScreenIsLocked"=Yes`)), "locked"},
		{"another user switched in", root("No", strings.Replace(user("501", ""), "OnConsoleKey\"=Yes", "OnConsoleKey\"=No", 1), user("502", "")), "away"},
		{"uid 5010 is not 501", root("No", user("5010", "")), "away"},
		{"nobody logged in", root("No"), "away"},
		{"not ioreg's root", "nothing", ""},
	} {
		if got := parseConsole(c.out, 501); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The live screen needs macOS's Screen Sharing: something on the port that greets like VNC.
func TestAnswersRFB(t *testing.T) {
	listen := func(greeting string) string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				_, _ = c.Write([]byte(greeting))
				_ = c.Close()
			}
		}()
		return l.Addr().String()
	}
	ctx := context.Background()
	if !answersRFB(ctx, listen("RFB 003.889\n")) {
		t.Fatal("Screen Sharing")
	}
	if answersRFB(ctx, listen("HTTP/1.1 400 Bad Request\r\n")) {
		t.Fatal("not VNC")
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close()
	if answersRFB(ctx, addr) {
		t.Fatal("nothing there")
	}
}

func TestComputerReadySaysWhatIsMissing(t *testing.T) {
	ok := proto.Computer{Installed: true, Daemon: true, Accessibility: "granted", ScreenRecording: "granted", Desktop: "active"}
	if ready, note := computerReady(ok); !ready || note != "" {
		t.Fatal(note)
	}
	for _, c := range []struct {
		edit func(*proto.Computer)
		want string
	}{
		{func(c *proto.Computer) { c.Installed = false }, "没有装"},
		{func(c *proto.Computer) { c.Daemon = false }, "自动打开"},
		{func(c *proto.Computer) { c.Accessibility, c.ScreenRecording = "missing", "missing" }, "辅助功能、屏幕录制权限"},
		{func(c *proto.Computer) { c.ScreenRecording = "missing" }, "还没有屏幕录制权限"},
		{func(c *proto.Computer) { c.Desktop = "locked" }, "锁屏"},
		{func(c *proto.Computer) { c.Desktop = "away" }, "没有你登录的桌面"},
	} {
		s := ok
		c.edit(&s)
		if ready, note := computerReady(s); ready || !strings.Contains(note, c.want) {
			t.Errorf("%+v: %v %q", s, ready, note)
		}
	}
}

// fakeCua is a cua-driver that is running, answers call with what it was given, and writes a 4×2 PNG as the
// screenshot of a window read.
func fakeCua(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	img.Set(1, 1, color.NRGBA{R: 255, A: 255})
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	pngPath := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(pngPath, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$1" in
status) echo "Cua Driver daemon is running"; exit 0 ;;
call)
  out="$3"; tool="$4"; input=$(cat)
  case "$tool" in get_window_state) cp "` + pngPath + `" "$out" ;; esac
  case "$input" in *fail*) echo "boom: no window" >&2; exit 3 ;; esac
  printf '{"tool": "%s", "input": %s}\n' "$tool" "$input" ;;
esac
`
	bin := filepath.Join(dir, "cua-driver")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestComputerCall(t *testing.T) {
	bin := fakeCua(t)
	cuaBin = func() string { return bin }
	t.Cleanup(func() { cuaBin = findCua })
	ctx := context.Background()

	res, err := computerCall(ctx, json.RawMessage(`{"session": "persona-c1", "tool": "get_window_state", "args": {"pid": 63, "window_id": 2318}}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tool  string         `json:"tool"`
		Input map[string]any `json:"input"`
	}
	if err := json.Unmarshal(res.Output, &got); err != nil || got.Tool != "get_window_state" || got.Input["session"] != "persona-c1" ||
		got.Input["window_id"] != float64(2318) {
		t.Fatalf("the args on stdin, with the session label: %s %v", res.Output, err)
	}
	if res.Image == nil || res.Image.Width != 4 || res.Image.Height != 2 {
		t.Fatalf("the screenshot beside the JSON: %+v", res.Image)
	}
	data, _ := base64.StdEncoding.DecodeString(res.Image.Data)
	if _, format, err := image.Decode(bytes.NewReader(data)); err != nil || "image/"+format != res.Image.Mime {
		t.Fatalf("%s %v", format, err)
	}

	res, err = computerCall(ctx, json.RawMessage(`{"session": "persona-c1", "tool": "run_script", "args": {"script": "return 1", "session": "mine"}}`))
	if err != nil || !strings.Contains(string(res.Output), `"session":"mine"`) || res.Image != nil {
		t.Fatalf("a session the caller gave is kept; no screenshot: %s %v", res.Output, err)
	}
	if _, err := computerCall(ctx, json.RawMessage(`{"tool": "run_steps", "args": {"steps": "fail"}}`)); err == nil || !strings.Contains(err.Error(), "boom: no window") {
		t.Fatalf("what it said on failure: %v", err)
	}
	for _, tool := range []string{"set_config", "clipboard_read", "kill_app", "update"} {
		if _, err := computerCall(ctx, json.RawMessage(`{"tool": "`+tool+`", "args": {}}`)); err == nil {
			t.Errorf("%s is not the hub's to call", tool)
		}
	}
	// args null is no args: not a panic (a nil map written to) that takes the whole node down.
	res, err = computerCall(ctx, json.RawMessage(`{"session": "persona-c1", "tool": "list_apps", "args": null}`))
	if err != nil || !strings.Contains(string(res.Output), `"session":"persona-c1"`) {
		t.Fatalf("args null: %s %v", res.Output, err)
	}
	cuaBin = func() string { return "" }
	if _, err := computerCall(ctx, json.RawMessage(`{"tool": "list_apps"}`)); err == nil || !strings.Contains(err.Error(), "没有装") {
		t.Fatalf("not installed: %v", err)
	}
}

// The real Cua Driver on this Mac (PERSONA_CUA_E2E=1, on a Mac with it set up and Calculator allowed to be clicked):
// 6 × 7 in Calculator through run_steps, then the window read under the same session, its screenshot written to
// PERSONA_CUA_SHOT when set (docs/extensions/computer-use.md "Verification").
func TestCuaEndToEnd(t *testing.T) {
	if os.Getenv("PERSONA_CUA_E2E") == "" {
		t.Skip("needs Cua Driver on this Mac: PERSONA_CUA_E2E=1")
	}
	ctx := context.Background()
	st := computerStatus(ctx)
	t.Logf("status: %+v", st)
	if !st.Ready {
		t.Fatalf("not ready: %s", st.Note)
	}
	steps := `{"session": "persona-e2e", "tool": "run_steps", "args": {"steps": [
		{"click": {"app": "Calculator", "role": "button", "name": "All Clear"}},
		{"click": {"app": "Calculator", "role": "button", "name": "6"}},
		{"click": {"app": "Calculator", "role": "button", "name": "Multiply"}},
		{"click": {"app": "Calculator", "role": "button", "name": "7"}},
		{"click": {"app": "Calculator", "role": "button", "name": "Equals"}}]}}`
	res, err := computerCall(ctx, json.RawMessage(steps))
	if err != nil || !strings.Contains(string(res.Output), `"ok": true`) && !strings.Contains(string(res.Output), `"ok":true`) {
		t.Fatalf("%v %s", err, res.Output)
	}
	found, err := computerCall(ctx, json.RawMessage(`{"session": "persona-e2e", "tool": "run_script", "args": {"script": "const a = await cua.getApp(\"Calculator\"); return {pid: a.pid, window_id: a.windowId};"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Value struct {
			PID      int `json:"pid"`
			WindowID int `json:"window_id"`
		} `json:"value"`
	}
	_ = json.Unmarshal(found.Output, &w)
	state, err := computerCall(ctx, json.RawMessage(fmt.Sprintf(`{"session": "persona-e2e", "tool": "get_window_state", "args": {"pid": %d, "window_id": %d}}`,
		w.Value.PID, w.Value.WindowID)))
	if err != nil || state.Image == nil || !strings.Contains(string(state.Output), "42") {
		t.Fatalf("%v %.400s", err, state.Output)
	}
	t.Logf("screenshot %s %dx%d, %d bytes of base64", state.Image.Mime, state.Image.Width, state.Image.Height, len(state.Image.Data))
	if out := os.Getenv("PERSONA_CUA_SHOT"); out != "" {
		data, _ := base64.StdEncoding.DecodeString(state.Image.Data)
		_ = os.WriteFile(out, data, 0o600)
	}
}

// fakeCuaEnded is a cua-driver that has ended the session labels named by files in its ended directory (#7590): it
// refuses every call under one as Cua Driver does, until start_session; it logs each call's tool and input.
func fakeCuaEnded(t *testing.T, ended ...string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	gone := filepath.Join(dir, "ended")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, l := range ended {
		if err := os.WriteFile(filepath.Join(gone, l), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	log = filepath.Join(dir, "calls")
	script := `#!/bin/sh
case "$1" in
status) echo "Cua Driver daemon is running"; exit 0 ;;
call)
  tool="$4"; input=$(cat)
  echo "$tool $input" >> "` + log + `"
  label=$(printf '%s' "$input" | sed -n 's/.*"session":"\([^"]*\)".*/\1/p')
  if [ -n "$label" ] && [ -e "` + gone + `/$label" ]; then
    if [ "$tool" = start_session ]; then rm "` + gone + `/$label"; printf '{"active": true, "revived": true}\n'; exit 0; fi
    echo "session has ended; tool call '$tool' was rejected. Call start_session with session '$label' to start it again, or use a new session label." >&2
    exit 1
  fi
  printf '{"tool": "%s", "input": %s}\n' "$tool" "$input" ;;
esac
`
	bin = filepath.Join(dir, "cua-driver")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

// The hub may start the call's own label again, and nothing else with it; the node's own label starts again by
// itself once Cua Driver ended it (5 minutes idle), a conversation's is the hub's to decide (#7590).
func TestComputerCallSessionEnded(t *testing.T) {
	bin, log := fakeCuaEnded(t, "persona-gate", "persona-c1")
	cuaBin = func() string { return bin }
	t.Cleanup(func() { cuaBin = findCua })
	ctx := context.Background()
	calls := func() string { b, _ := os.ReadFile(log); return string(b) }

	res, err := computerCall(ctx, json.RawMessage(`{"session": "persona-gate", "tool": "list_windows", "args": {}}`))
	if err != nil || !strings.Contains(string(res.Output), `"tool": "list_windows"`) {
		t.Fatalf("the node's own label is started again and read: %v %s\n%s", err, res.Output, calls())
	}
	if got := calls(); strings.Count(got, "list_windows") != 2 || !strings.Contains(got, `start_session {"session":"persona-gate"}`) {
		t.Fatalf("refused, started again, read again: %s", got)
	}

	_, err = computerCall(ctx, json.RawMessage(`{"session": "persona-c1", "conversation": "c1", "tool": "list_windows", "args": {}}`))
	if err == nil || !strings.Contains(err.Error(), "cua-driver list_windows: session has ended") {
		t.Fatalf("a conversation's label: Cua Driver's own answer, for the hub: %v", err)
	}
	if strings.Contains(calls(), `start_session {"session":"persona-c1"}`) {
		t.Fatalf("the node started a conversation's label again by itself: %s", calls())
	}

	res, err = computerCall(ctx, json.RawMessage(`{"session": "persona-c1", "tool": "start_session",
		"args": {"session": "someone-else", "cursor_theme": {"theme_id": "x"}}}`))
	if err != nil || !strings.Contains(string(res.Output), `"revived": true`) || !strings.Contains(calls(), `start_session {"session":"persona-c1"}`+"\n") {
		t.Fatalf("start_session: the call's own label and nothing else: %v %s\n%s", err, res.Output, calls())
	}
	if _, err := computerCall(ctx, json.RawMessage(`{"tool": "start_session", "args": {"session": "someone-else"}}`)); err == nil {
		t.Fatal("start_session with no label of the call's own")
	}
}
