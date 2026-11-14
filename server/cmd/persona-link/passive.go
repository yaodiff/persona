package main

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
)

// Passive mode on the command line (docs/operations/link.md "Passive mode"): `pair --listen` makes the link the hub's admin page
// takes, `mode` switches between the two modes.

// pairListen is `persona-link pair --listen [addr] [--addr …]`: the link alone on stdout (for a pipe), the rest on stderr.
func pairListen(e *env, listen string, addrs []string) error {
	before, _ := link.Read(e.configPath())
	inv, err := link.PairListen(e.configPath(), listen, addrs, e.now())
	if err != nil {
		return err
	}
	fmt.Fprintln(e.stdout, inv.Link)
	w := e.stderr
	// Keep stdout a single canonical URL for pipes; baseline hubs can use the
	// alternate spelling on stderr. Both redeem the same one-time code.
	fmt.Fprintln(w, inv.LegacyLink)
	fmt.Fprintf(w, "这台电脑改成「让 hub 来连」（被动模式）：监听 %s，连接器指纹 %s。\n", inv.Listen, inv.FP)
	fmt.Fprintf(w, "把上面的链接粘贴到 hub 管理界面「电脑 › 添加电脑 › hub 来连」：%s内有效，只能用一次。\n", duration(link.PairCodeLife))
	fmt.Fprintf(w, "链接里给 hub 的地址（按顺序试）：%s；hub 连不到这些地址时在那里改，或者这里用 --addr 指定。\n", strings.Join(inv.Addrs, "、"))
	if before != nil && before.Passive.Paired() {
		fmt.Fprintf(w, "现在配对的是 hub%s；别的 hub 用这条链接配对以后，它就连不进来了。\n", passiveHubText(before.Passive))
	}
	fmt.Fprintln(w, firewallHint(e.goos, inv.Listen))
	switch {
	case link.Running(e.configPath()):
		fmt.Fprintln(w, "在运行的 persona-link 一两秒内就开始监听，不用重启。")
	case e.goos == "linux":
		fmt.Fprintln(w, "接下来：persona-link install --listen（装成 systemd 用户服务，一直监听），或者前台跑 persona-link run。")
	case e.goos == "windows":
		fmt.Fprintln(w, "接下来：persona-link install --listen（装成登录时启动的计划任务，一直监听），或者前台跑 persona-link run。")
	case e.goos == "darwin":
		fmt.Fprintln(w, i18n.T(i18n.Terminal(), "link-service.passive_mac_next"))
	default:
		fmt.Fprintln(w, "接下来让 persona-link 跑起来（前台跑 persona-link run，或者用这台的进程管理）。")
	}
	return nil
}

// firewallHint says which port to open, and to whom.
func firewallHint(goos, listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		port = strings.TrimPrefix(listen, ":")
	}
	if goos == "windows" {
		return fmt.Sprintf("防火墙：只对 hub 放行 TCP %s，别对所有人开（Windows 防火墙，在管理员的 PowerShell 里："+
			"New-NetFirewallRule -DisplayName 'Persona Link' -Direction Inbound -Protocol TCP -LocalPort %s -RemoteAddress <hub 的地址> -Action Allow）。"+
			"hub 经代理连进来时放行的是代理的地址。", port, port)
	}
	return fmt.Sprintf("防火墙：只对 hub 放行 TCP %s，别对所有人开（例如 ufw：sudo ufw allow from <hub 的地址> to any port %s proto tcp；"+
		"firewalld、nftables、路由器同理）。hub 经代理连进来时放行的是代理的地址。", port, port)
}

// passiveHubText names the hub passive mode is paired with.
func passiveHubText(p *link.Passive) string {
	switch {
	case p == nil:
		return ""
	case p.HubName != "":
		return "「" + p.HubName + "」"
	case p.HubID != "":
		return " " + p.HubID
	}
	return ""
}

func modeName(mode string) string {
	if mode == link.ModePassive {
		return "让 hub 来连（被动）"
	}
	return "我去连 hub（主动）"
}

// modeCmd is `persona-link mode [active|passive] [--listen addr]`.
func modeCmd(e *env, args []string) error {
	var mode, listen string
	for i := 0; i < len(args); i++ {
		v, next, ok, err := flagValue(args, i, "--listen")
		switch {
		case err != nil:
			return err
		case ok:
			listen, i = v, next
		case mode == "" && (args[i] == link.ModeActive || args[i] == link.ModePassive):
			mode = args[i]
		default:
			return errUsage
		}
	}
	if mode == "" && listen == "" {
		c, _ := link.Read(e.configPath())
		cur := c.CurrentMode()
		fmt.Fprintf(e.stdout, "模式：%s\n", modeName(cur))
		if cur == link.ModePassive {
			fmt.Fprintf(e.stdout, "监听：%s\n", c.Passive.ListenAddr())
		}
		return nil
	}
	if mode == "" {
		c, _ := link.Read(e.configPath())
		mode = c.CurrentMode()
	}
	c, err := link.SetMode(e.configPath(), mode, listen)
	if err != nil {
		return err
	}
	line := "改好了：" + modeName(mode)
	if mode == link.ModePassive {
		line += "，监听 " + c.Passive.ListenAddr()
	}
	fmt.Fprintln(e.stdout, line+"。")
	switch {
	case mode == link.ModePassive && !c.Passive.Paired():
		fmt.Fprintln(e.stdout, "还没有 hub 来配对：persona-link pair --listen 打印给 hub 管理界面粘贴的链接。")
	case mode == link.ModeActive && c.Token == "":
		fmt.Fprintln(e.stdout, "主动模式还没配对：persona-link pair '<personad hub pair --machine <这台的 id> 给的链接>'。")
	}
	if mode == link.ModePassive {
		fmt.Fprintln(e.stdout, firewallHint(e.goos, c.Passive.ListenAddr()))
	}
	if link.Running(e.configPath()) {
		fmt.Fprintln(e.stdout, "在运行的 persona-link 一两秒内就换过去。")
	}
	return nil
}

// ago says how long ago t was, for a person.
func ago(now, t time.Time) string {
	return duration(now.Sub(t)) + "前"
}
