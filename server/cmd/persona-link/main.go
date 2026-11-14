// Command persona-link (persona-link before #1159; cmd/persona-link is a link to this directory) keeps this computer
// reachable from the Persona hub when the hub cannot reach it — a laptop on
// whatever network it is on, a server or NAS behind NAT (docs/operations/link.md). It finds the hub, says which machine it is and
// hands each stream the hub opens on the link to this computer's resident node. Nothing else: everything a node does
// stays in personad. `persona-link install` makes it a systemd user service on Linux, a launchd agent on Mac or a
// scheduled task on Windows; the Electron desktop's optional setup window does not have to run alongside it.
package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/netaddr"
	"github.com/nwverse/persona/server/internal/update"
)

// What the publisher writes in (-X), as personad's and persona-node's: the connector's build must also be verifiable
// from the compressed release, including on another platform (update.VerifyRelease).
var (
	version = "dev"
	channel = "dev"
	build   = ""
)

const usage = `用法：
  persona-link pair [--machine <id>] [--ssh <服务器> [--ssh-target <地址:端口>] [--ssh-always|--no-ssh-always]] [<配对链接>|-]
                                                 主动模式（我去连 hub）：用 personad hub pair 给的链接配对，凭据写进
                                                 ~/.persona/link.json；不给链接或给 - 时从标准输入读（终端里会提示粘贴）；
                                                 链接里没带机器 id 时用 --machine 指定；hub 只开了 SSH 时加 --ssh 走隧道
  persona-link pair --listen [<监听地址>] [--addr <地址>]…
                                                 被动模式（让 hub 来连）：在本机监听（默认 :18663），打印给 hub 管理界面
                                                 粘贴的链接（10 分钟内有效，只能用一次）；--addr 指定链接里给 hub 的地址
  persona-link mode [active|passive] [--listen <监听地址>]
                                                 看 / 切换模式：active 我去连 hub，passive 让 hub 来连；
                                                 两种模式的配对各自留着，切回去不用重新配对
  persona-link install [--pair <链接>|-] [--machine <id>] [--ssh <服务器> [--ssh-target <地址:端口>] [--ssh-always|--no-ssh-always]]
                    [--listen [<监听地址>]] [--addr <地址>]… [--node-bin <路径> | --personad <路径>] [--no-node]
                                                 Linux：装成 systemd 用户服务并启动，等它连上 hub（被动模式：等它开始监听）；
                                                 --pair 先配对（主动），--listen 改成被动模式、还没配对时打印配对链接；
                                                 常驻 node 的服务没装时一起装上（--no-node 不装）。
                                                 没有 systemd 时告诉你别的办法。
                                                 Mac：装成 launchd 的 com.persona.link 用户代理；独立 Link 安装包
                                                 会把 persona-node 一起放好，Electron 桌面设置界面可以调用这条命令。
                                                 Windows：装成登录时启动的计划任务「Persona Link」，常驻 node 和
                                                 persona-link 都在里面，退出了重启；--personad 给的 personad.exe 每次都换上。
  persona-link node install [--node-bin <路径> | --personad <路径>] | switch [--to personad] | status [--json] | uninstall
                                                 常驻 node（node --attach）的服务：Linux systemd 用户服务
                                                 personad-node，Mac launchd 的 com.persona.node；已经装了的保持原样。
                                                 跑设备自己的程序 ~/.persona/bin/persona-node（有的话），否则 personad；
                                                 --node-bin 给的程序按它是哪个放进去，--personad 给的当 personad。
                                                 switch：已装的服务改跑 persona-node（--to personad 改回），新的 node
                                                 应答前保留旧服务文件，应答不了就换回去
  persona-link discover [--wait <秒>] [--json]    局域网里有哪些 hub（mDNS）；听到了退出码 0，否则 1
  persona-link status [--json]                    模式、链路、hub、本机 node、后台服务的状态；连着（被动模式：在监听）时
                                                 退出码 0，否则 1
  persona-link reconnect                            请正在运行的连接器立即重连（Windows 也适用，桌面设置界面用这个命令）
  persona-link logs [-f] [-n <行数>]              看日志（~/.persona/link.log）；-f 一直跟着
  persona-link uninstall [--purge]                停掉并删掉 systemd 用户服务（Windows：计划任务）；--purge 连凭据、状态、日志、
                                                 连接器的 TLS 身份和 ~/.persona/bin/persona-link 一起删
  persona-link run [--parent] [--log <文件>]      保持链路（前台），状态一行一个 JSON 打到标准输出；
                                                 --parent：标准输入关掉就退出（供兼容旧版父进程的控制台使用）；
                                                 --log：日志写进这个文件（满 1 MB 换一个），不写标准错误
  persona-link name [<新名字>|--reset]           看 / 改这台电脑在 hub 上的名字（App、网页和管理界面里显示的那个；id 不变）：
                                                 请正在运行、连着 hub 的 persona-link 去问 hub；--reset 改回 id
  persona-link ssh [<服务器> | --off] [--target <地址:端口>] [--always|--no-always] [--test]
                                                 看 / 设 / 移除主动模式连 hub 用的 SSH 隧道（[user@]host[:port] 或
                                                 ~/.ssh/config 别名；默认转发到配对链接里的 127.0.0.1:<TLS 端口>）；
                                                 --always 只走隧道，--off 移除隧道，--test 测试连接
  persona-link address add <地址>                 手动加一个 hub 的候选地址（https://域名:端口，或 域名）
  persona-link address remove <地址>              去掉手动加的地址
  persona-link version [-v]                    看版本；-v 加发布渠道和构建号

信号（Linux、Mac）：SIGUSR1 网络变了，马上看一眼链路；SIGUSR2 断开重连。Windows 上是同名的命名事件（reconnect 命令发）。`

// exitCode ends the program with a code and nothing printed: what it had to say is already out.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func main() {
	self := commandName(os.Args[0], os.Getenv)
	stdout, stderr := io.Writer(os.Stdout), io.Writer(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix(self + ": ")
	if len(os.Args) < 2 {
		fmt.Fprintln(stderr, usage)
		os.Exit(2)
	}
	if sub := os.Args[1]; helpTopics[sub] && wantsHelp(os.Args[2:]) {
		// `persona-link <sub> -h`: that subcommand's lines of the usage, and nothing failed
		fmt.Fprintln(stdout, subcommandHelp(sub))
		return
	}
	e := newEnv()
	e.stdout, e.stderr = stdout, stderr
	var err error
	switch args := os.Args[2:]; os.Args[1] {
	case "pair":
		err = pairCmd(e, args)
	case "mode":
		err = modeCmd(e, args)
	case "node":
		err = nodeCmd(e, args)
	case "discover":
		err = discover(e, args)
	case "install":
		err = install(e, args)
	case "uninstall":
		err = uninstall(e, args)
	case "status":
		err = status(e, args)
	case "reconnect":
		err = reconnectNow(e, args)
	case "logs", "log":
		err = logs(e, args)
	case "run":
		err = run(args)
	case "supervise": // what the Windows scheduled task runs (supervise_windows.go); not for people
		err = supervise(e, args)
	case "ssh":
		err = sshCmd(e, args)
	case "address":
		err = address(args)
	case "name":
		err = nameCmd(e, args)
	case "version", "--version", "-v":
		fmt.Println(version)
		if slices.Contains(args, "-v") {
			b := update.Make(version, channel, build, "")
			fmt.Printf("channel %s, build %d\n", b.Channel, b.Number)
		}
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, usage)
	default:
		fmt.Fprintln(stderr, usage)
		os.Exit(2)
	}
	var code exitCode
	switch {
	case errors.As(err, &code):
		os.Exit(int(code))
	case errors.Is(err, errUsage):
		fmt.Fprintln(stderr, usage)
		os.Exit(2)
	case err != nil:
		fmt.Fprintln(stderr, explain(err))
		os.Exit(1)
	}
}

var errUsage = errors.New("usage")

// helpTopics are the subcommands `<sub> -h` answers for (supervise is not for people: its -h shows the whole usage).
var helpTopics = map[string]bool{"pair": true, "mode": true, "node": true, "discover": true, "install": true,
	"uninstall": true, "status": true, "reconnect": true, "logs": true, "log": true, "run": true, "supervise": true, "ssh": true,
	"address": true, "name": true}

// wantsHelp says whether args ask for help (-h, -help or --help anywhere).
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "-help" || a == "--help" {
			return true
		}
	}
	return false
}

// subcommandHelp is the usage's entries for sub with their continuation lines, or the whole usage when it has none.
func subcommandHelp(sub string) string {
	if sub == "log" {
		sub = "logs"
	}
	var out []string
	in := false
	for _, line := range strings.Split(usage, "\n") {
		switch rest, isEntry := strings.CutPrefix(line, "  persona-link "); {
		case isEntry:
			name, _, _ := strings.Cut(rest, " ")
			in = name == sub
		case strings.TrimSpace(line) == "" || !strings.HasPrefix(line, " "):
			in = false
		}
		if in {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return usage
	}
	return "用法：\n" + strings.Join(out, "\n")
}

// flagValue reads `--name value` or `--name=value` at args[i]; ok says it was that flag, next is where to go on.
func flagValue(args []string, i int, name string) (value string, next int, ok bool, err error) {
	a := compatibleFlag(args[i], name)
	if v, found := strings.CutPrefix(a, name+"="); found {
		return v, i, true, nil
	}
	if a != name {
		return "", i, false, nil
	}
	if i+1 >= len(args) {
		return "", i, true, fmt.Errorf("%s 后面要跟一个值", name)
	}
	return args[i+1], i + 1, true, nil
}

// optionalValue reads `--name`, `--name value` or `--name=value` at args[i], the value being optional: the next
// argument is taken unless it is another flag.
func optionalValue(args []string, i int, name string) (value string, next int, ok bool) {
	a := compatibleFlag(args[i], name)
	if v, found := strings.CutPrefix(a, name+"="); found {
		return v, i, true
	}
	if a != name {
		return "", i, false
	}
	if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
		return args[i+1], i + 1, true
	}
	return "", i, true
}

func pairCmd(e *env, args []string) error {
	var machine, raw, listen string
	var addrs []string
	var sshFlags sshPairFlags
	seen, passive := false, false
	for i := 0; i < len(args); i++ {
		if v, next, ok := optionalValue(args, i, "--listen"); ok {
			passive, listen, i = true, v, next
			continue
		}
		if next, ok, err := sshFlags.consume(args, i); ok || err != nil {
			if err != nil {
				return err
			}
			i = next
			continue
		}
		v, next, ok, err := flagValue(args, i, "--machine")
		if !ok && err == nil {
			if v, next, ok, err = flagValue(args, i, "--addr"); ok && err == nil {
				addrs, i = append(addrs, v), next
				continue
			}
		}
		switch {
		case err != nil:
			return err
		case ok:
			machine, i = v, next
		case !seen:
			raw, seen = args[i], true
		default:
			return errUsage
		}
	}
	if passive || len(addrs) > 0 {
		if sshFlags.any() {
			return errors.New("pair --listen 不要 --ssh：SSH 隧道只在主动模式（我去连 hub）下用")
		}
		if seen || machine != "" {
			return errors.New("pair --listen 不要配对链接和 --machine：被动模式的链接是这台电脑给 hub 的，机器 id 由 hub 定")
		}
		return pairListen(e, listen, addrs)
	}
	c, err := pairWithSSH(e, raw, machine, sshFlags)
	if err != nil {
		return err
	}
	pokeRunning(e.configPath())
	fmt.Fprintf(e.stdout, "配对好了：这台是 hub 上的「%s」，凭据存在 %s。\n", cmp.Or(c.MachineName, c.Machine), e.configPath())
	switch {
	case link.Running(e.configPath()):
		fmt.Fprintln(e.stdout, "在运行的 persona-link 几秒内就会用上新凭据，不用重启。")
	case e.goos == "linux":
		fmt.Fprintln(e.stdout, "接下来：persona-link install（装成 systemd 用户服务，一直连着）。")
	}
	return nil
}

// pair redeems a pairing link: raw, or read from stdin when raw is "" or "-" (with a prompt on a terminal).
func pair(e *env, raw, machine string) (*link.Config, error) {
	return pairWithSSH(e, raw, machine, sshPairFlags{})
}

func pairWithSSH(e *env, raw, machine string, sshFlags sshPairFlags) (*link.Config, error) {
	if raw == "" || raw == "-" {
		in := bufio.NewReader(io.LimitReader(e.stdin, 16<<10))
		if e.stdinIsTerminal() {
			// One pasted line, then Enter: not waiting for the end of input.
			fmt.Fprint(e.stderr, "粘贴 personad hub pair 给的配对链接，回车：")
			line, err := in.ReadString('\n')
			if err != nil && line == "" {
				return nil, err
			}
			raw = line
		} else {
			b, err := io.ReadAll(in)
			if err != nil {
				return nil, err
			}
			raw = string(b)
		}
		raw = strings.TrimSpace(raw)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("没有配对链接：在 hub 机器上运行 personad hub pair --machine <这台的 id> --add，把拿到的链接给我")
	}
	sshCfg, err := sshFlags.build(raw)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return link.PairWithSSH(ctx, raw, strings.TrimSpace(machine), "", e.configPath(), sshCfg, e.sshBin)
}

func address(args []string) error {
	if len(args) != 2 || (args[0] != "add" && args[0] != "remove") {
		return errUsage
	}
	if args[0] == "remove" {
		return link.RemoveAddress(link.DefaultPath(), args[1])
	}
	e, err := link.AddAddress(link.DefaultPath(), args[1])
	if err == nil {
		fmt.Printf("加好了：%s\n", e.URL)
	}
	return err
}

func run(args []string) error {
	var parent bool
	var logPath string
	for i := 0; i < len(args); i++ {
		if args[i] == "--parent" {
			parent = true
			continue
		}
		v, next, ok, err := flagValue(args, i, "--log")
		switch {
		case err != nil:
			return err
		case !ok:
			return errUsage
		}
		logPath, i = v, next
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var app *fromApp
	if parent {
		app = &fromApp{presence: link.NewPresenceBox(), confirms: link.NewConfirms()}
		go func() {
			readParent(os.Stdin, app)
			stop() // the optional parent process went away, however it did
		}()
	}
	return keepLink(ctx, link.DefaultPath(), logPath, os.Stdout, app)
}

// fromApp is an optional parent process's presence and notification confirmations (internal/link presence.go).
// The independent Link service does not send either; the Electron desktop handles its own presence and notifications.
type fromApp struct {
	presence *link.PresenceBox
	confirms *link.Confirms
}

// readParent reads a compatible parent process's lines on stdin until it closes, into app; the rest is not for this build. A line
// too long to scan ends the scan, not the reading: only stdin's end says the app went away.
func readParent(r io.Reader, app *fromApp) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if p, ok := link.ParsePresenceLine(sc.Bytes()); ok {
			app.presence.Set(p, time.Now())
		} else if c, ok := link.ParseRangLine(sc.Bytes()); ok {
			app.confirms.Push(c)
		}
	}
	_, _ = io.Copy(io.Discard, r)
}

// keepLink is `persona-link run`: the link for the link.json at path until ctx ends. Every status goes to stdout as a
// JSON line and to link.status.json for `persona-link status`; the log to logPath when there is one. app is what the
// parent process says (nil without it).
func keepLink(ctx context.Context, path, logPath string, stdout io.Writer, app *fromApp) error {
	release, err := link.LockRun(path)
	if err != nil {
		return err
	}
	defer release()
	if logPath != "" {
		lf, err := openRotating(logPath, logMax)
		if err != nil {
			return err
		}
		defer lf.Close()
		log.SetOutput(lf)
		defer log.SetOutput(os.Stderr)
	}
	// A node switch whose persona-link died half way (nodeswitch.go) is undone now and whenever one turns up: by the
	// one persona-link run that holds the lock, saying what it did in the log (stdout is reserved for JSON status lines).
	go watchSwitch(ctx)
	check, redial := make(chan struct{}, 1), make(chan struct{}, 1)
	poke := func(ch chan struct{}) {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	notifySignals(ctx, path, func() { poke(check) }, func() { poke(redial) })
	// Address changes: netlink on Linux; elsewhere the platform's address watcher or a slow poll.
	go func() {
		first := true
		for range netaddr.Watch(ctx) {
			if !first {
				poke(check)
			}
			first = false
		}
	}()
	var mu sync.Mutex
	out := json.NewEncoder(stdout)
	var last link.Status
	ended := false // a stream ending late must not write over "stopped"
	save := func(s link.Status) {
		if err := link.WriteStatus(path, link.SavedStatus{Status: s, PID: os.Getpid(), Version: version, Updated: time.Now()}); err != nil {
			log.Printf("status: %v", err)
		}
	}
	log.Printf("persona-link %s started", version)
	o := link.Options{Path: path, Target: link.AttachSocket(), Version: version,
		Status: func(s link.Status) {
			mu.Lock()
			defer mu.Unlock()
			if ended {
				return
			}
			_ = out.Encode(s)
			last = s
			save(s)
		},
		Check: check, Redial: redial, Logf: log.Printf}
	if app != nil {
		o.Presence, o.Confirms = app.presence, app.confirms
	}
	err = link.Run(ctx, o)
	mu.Lock()
	defer mu.Unlock()
	ended = true
	save(link.Status{State: "stopped", Mode: last.Mode, Machine: last.Machine, HubName: last.HubName, Endpoints: last.Endpoints,
		SSH: last.SSH, Listen: last.Listen, LastSeen: last.LastSeen, From: last.From})
	log.Printf("persona-link stopped")
	return err
}
