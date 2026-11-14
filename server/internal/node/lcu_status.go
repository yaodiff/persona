package node

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// lcuBin is the installed `lcu` command (tests set it). The resident node's PATH is short, so the installer's
// own location is checked before PATH.
var lcuBin = findLCU

// chatgptApp reports the official ChatGPT desktop app, which supplies LCU's runtime. Persona never installs it.
var chatgptApp = findChatGPT

func findLCU() string {
	if installOS != "darwin" && installOS != "linux" {
		return ""
	}
	var places []string
	if home, err := os.UserHomeDir(); err == nil {
		places = append(places, filepath.Join(home, ".local", "share", "lcu", "current", "bin", "lcu"))
	}
	places = append(places, "/opt/lcu/current/bin/lcu")
	for _, p := range places {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	if p, err := exec.LookPath("lcu"); err == nil {
		return p
	}
	return ""
}

func findChatGPT() bool {
	path := "/usr/lib/chatgpt"
	if installOS == "darwin" {
		path = "/Applications/ChatGPT.app"
	}
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// lcuUsable: auto mode prefers LCU when the command is installed and its runtime (the ChatGPT app, or the Node the
// installer recorded) is there.
func lcuUsable() bool {
	bin := lcuBin()
	return bin != "" && (chatgptApp() || lcuNodeOK(bin))
}

func lcuNodeOK(bin string) bool {
	root := filepath.Dir(filepath.Dir(bin))
	b, err := os.ReadFile(filepath.Join(root, "node-path"))
	if err != nil {
		return false
	}
	p := strings.TrimSpace(string(b))
	st, err := os.Stat(p)
	return p != "" && err == nil && !st.IsDir()
}

// lcuVersion reads the release directory's name (lcu-0.13.5-darwin-arm64). It does not run the command: that needs
// the ChatGPT app's Node, which may be missing when the page only wants to say what is installed.
func lcuVersion(bin string) string {
	root := filepath.Dir(filepath.Dir(bin))
	if dest, err := filepath.EvalSymlinks(root); err == nil {
		root = dest
	}
	base := filepath.Base(root)
	rest, ok := strings.CutPrefix(base, "lcu-")
	if !ok {
		return ""
	}
	for _, plat := range []string{"-darwin", "-linux", "-windows"} {
		if i := strings.Index(rest, plat); i > 0 {
			return rest[:i]
		}
	}
	return ""
}

func computerBackend(params json.RawMessage) string {
	var p struct {
		Backend string `json:"backend"`
	}
	_ = json.Unmarshal(params, &p)
	return p.Backend
}

func useLCU(backend string) bool {
	switch backend {
	case "lcu":
		return true
	case "auto":
		return lcuUsable()
	default:
		return false
	}
}

// computerStatusFor is computer.status. backend is the plugin's choice: cua, lcu, or auto.
func computerStatusFor(ctx context.Context, backend string) proto.Computer {
	if useLCU(backend) {
		return lcuStatus(ctx)
	}
	c := computerStatus(ctx)
	if c.Backend == "" {
		c.Backend = "cua"
	}
	return c
}

func lcuStatus(ctx context.Context) proto.Computer {
	c := proto.Computer{Backend: "lcu"}
	if bin := lcuBin(); bin != "" {
		c.Installed = true
		c.Version = lcuVersion(bin)
		c.ChatGPT = chatgptApp() || lcuNodeOK(bin)
		c.Daemon = true
	}
	if installOS == "darwin" {
		if out, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-n", "Root", "-d1").Output(); err == nil {
			c.Desktop = parseConsole(string(out), os.Getuid())
		}
		c.ScreenSharing = screenSharing(ctx)
		c.Displays = screenDisplays(ctx, c.ScreenSharing)
		c.ScreenVideo = screenVideoStatus(ctx) // #3954: nil until advertiseScreenVideo
		c.ScreenInput = screenInputStatus(ctx) // #7775: nil without Persona Input
	}
	c.Ready, c.Note = lcuReady(c)
	c.Job = currentJob()
	c.Checked = time.Now().UTC()
	return c
}

func lcuReady(c proto.Computer) (bool, string) {
	switch {
	case installOS == "darwin" && installArch != "arm64":
		return false, "LCU 只支持 Apple 芯片的 Mac"
	case !c.Installed:
		return false, "这台 Mac 上没有装 LCU"
	case !c.ChatGPT:
		return false, "LCU 需要官方 ChatGPT 桌面版提供运行时：先安装它（Persona 不会下载或登录），再点安装 LCU"
	case c.Desktop == "locked":
		return false, "这台 Mac 锁屏了：解锁后才能操控。屏幕共享开着时，可以在它的画面上输登录密码解锁"
	case c.Desktop == "away":
		return false, "这台 Mac 现在没有你登录的桌面（退出登录或切到了别的用户），操控不了"
	}
	return true, ""
}
