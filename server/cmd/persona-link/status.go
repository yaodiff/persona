package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
)

// report is `persona-link status --json`: what is known here about the link, without asking the running persona-link
// (it keeps its last status on disk, link.status.json) and without touching the resident node (a connection to its
// socket would take the node from the hub).
type report struct {
	// Running: a persona-link holds the run lock (whoever started it: the service, a compatible parent or a person).
	Running bool `json:"running"`
	// Mode: active (this computer dials the hub) | passive (the hub dials it).
	Mode string `json:"mode"`
	// Paired, Machine, HubID, HubName, Endpoints: active mode's pairing (also there in passive mode, for switching back).
	Paired  bool   `json:"paired"`
	Machine string `json:"machine,omitempty"`
	// MachineName: what the hub calls this computer, as it last said (older hubs say nothing: Machine, its id).
	MachineName string `json:"machine_name,omitempty"`
	HubID       string `json:"hub_id,omitempty"`
	HubName     string `json:"hub_name,omitempty"`
	// Endpoints: the candidates in link.json, in the order they are tried.
	Endpoints []link.Endpoint `json:"endpoints,omitempty"`
	// SSH: the optional SSH tunnel in link.json.
	SSH *link.SSH `json:"ssh,omitempty"`
	// Trust: how the hub is checked, "pin" (its key, from the pairing) | "system" (behind an HTTPS reverse proxy:
	// the system's CAs and the host name).
	Trust string `json:"trust,omitempty"`
	// Link is the last status the running persona-link reported; with running false, the last one before it stopped.
	Link *link.SavedStatus `json:"link,omitempty"`
	// Passive is passive mode's pairing, whichever mode is on.
	Passive *passiveReport `json:"passive,omitempty"`
	Service *serviceState  `json:"service,omitempty"`
	Config  string         `json:"config"`
	Log     string         `json:"log"`
	Error   string         `json:"error,omitempty"` // link.json there but unreadable
}

// passiveReport is passive mode's part of link.json, without the credential's hash.
type passiveReport struct {
	Listen      string     `json:"listen"`
	Paired      bool       `json:"paired"`
	HubID       string     `json:"hub_id,omitempty"`
	HubName     string     `json:"hub_name,omitempty"`
	Machine     string     `json:"machine,omitempty"`
	MachineName string     `json:"machine_name,omitempty"`
	PairedAt    *time.Time `json:"paired_at,omitempty"`
	CodeExpires *time.Time `json:"code_expires,omitempty"` // a pairing code from pair --listen is waiting until then
	FP          string     `json:"fp,omitempty"`           // the connector's pin (public: anyone reaching the port sees the key)
}

// serviceState is the systemd user service (Linux), launchd user agent (Mac) or scheduled task (Windows).
type serviceState struct {
	Manager   string `json:"manager"` // systemd, launchd, schtasks
	Unit      string `json:"unit"`    // the unit, the task's name
	File      string `json:"file"`
	Installed bool   `json:"installed"`
	Active    string `json:"active,omitempty"`  // systemctl --user is-active; the task's status as schtasks words it
	Enabled   string `json:"enabled,omitempty"` // systemctl --user is-enabled
	Linger    bool   `json:"linger"`
	Missing   string `json:"missing,omitempty"` // why there is no systemd user instance here
}

func gather(e *env) report {
	cfg := e.configPath()
	r := report{Running: link.Running(cfg), Mode: link.ModeActive, Config: cfg, Log: e.logPath()}
	if c, err := link.Load(cfg); err == nil {
		r.Paired, r.Machine, r.HubID, r.HubName, r.Endpoints = true, c.Machine, c.Hub, c.HubName, link.Ranked(c.Endpoints)
		r.MachineName = c.MachineName
		r.SSH = c.SSH
		r.Trust = "pin"
		if c.SystemTrust() {
			r.Trust = link.TrustSystem
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		r.Error = err.Error()
	}
	if c, err := link.Read(cfg); err == nil {
		r.Mode = c.CurrentMode()
		if r.SSH == nil {
			r.SSH = c.SSH
		}
		if p := c.Passive; p != nil || r.Mode == link.ModePassive {
			pr := &passiveReport{Listen: p.ListenAddr(), Paired: p.Paired()}
			if p != nil {
				pr.HubID, pr.HubName, pr.Machine, pr.MachineName, pr.PairedAt = p.HubID, p.HubName, p.Machine, p.MachineName, p.PairedAt
				if until := p.CodeUntil(e.now()); !until.IsZero() {
					pr.CodeExpires = &until
				}
			}
			pr.FP = link.ReadPin(link.TLSDir(cfg)) // read only: status makes nothing
			r.Passive = pr
		}
	}
	if st, err := link.ReadStatus(cfg); err == nil {
		r.Link = st
		if r.HubName == "" {
			r.HubName = st.HubName
		}
	}
	if e.goos == "linux" {
		s := &serviceState{Manager: "systemd", Unit: e.linkUnitName(), File: e.unitPath(), Linger: e.lingering()}
		s.Installed = fileExists(s.File)
		if s.Missing = e.systemdMissing(); s.Missing == "" {
			// Both exit non-zero for inactive and disabled; the word printed is the answer.
			s.Active, _ = e.systemctl("is-active", s.Unit)
			s.Enabled, _ = e.systemctl("is-enabled", s.Unit)
			s.Active, s.Enabled = firstLine(s.Active), firstLine(s.Enabled)
		}
		r.Service = s
	}
	if e.goos == "darwin" {
		r.Service = e.macServiceState() // service_mac.go
	}
	if e.goos == "windows" {
		r.Service = e.taskState() // winservice.go
	}
	return r
}

func status(e *env, args []string) error {
	asJSON := false
	for _, a := range args {
		if a != "--json" {
			return errUsage
		}
		asJSON = true
	}
	r := gather(e)
	if asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			return err
		}
	} else {
		fmt.Fprint(e.stdout, r.text(e.now(), e.userOr("$USER")))
	}
	if r.healthy() {
		return nil
	}
	return exitCode(1)
}

// healthy: running and connected; in passive mode also listening for a hub that paired (it comes when it wants to).
func (r report) healthy() bool {
	if !r.Running || r.Link == nil || r.Link.Mode != r.Mode && r.Link.Mode != "" {
		return false
	}
	return r.Link.State == "connected" || (r.Mode == link.ModePassive && r.Link.State == "listening")
}

// text is the report for a person.
func (r report) text(now time.Time, user string) string {
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	st := r.Link
	live := r.Running && st != nil
	if r.Mode == link.ModePassive {
		r.passiveText(line, now, live)
		r.serviceText(line, user)
		if live {
			line("进程：pid %d · 版本 %s", st.PID, st.Version)
		}
		line("日志：%s（persona-link logs -f）", r.Log)
		return b.String()
	}
	line("模式：%s", modeName(r.Mode))
	switch {
	case r.Error != "":
		line("凭据：读不了（%s）", r.Error)
	case !r.Paired:
		line("链路：还没配对。在 hub 机器上运行 personad hub pair --machine <这台的 id> --add，再在这里 persona-link pair '<链接>'")
	case !r.Running && st != nil && st.State != "stopped":
		// How long ago, not a clock time: this computer's zone may well be UTC (a server) and not the user's.
		line("链路：没在运行（%s前最后一次报告：%s）", duration(now.Sub(st.Updated)), stateText(&st.Status, now))
	case !r.Running:
		line("链路：没在运行")
	case st == nil:
		line("链路：在运行，还没报告状态")
	default:
		line("链路：%s", stateText(&st.Status, now))
		if st.State == "waiting" || st.State == "refused" {
			for _, part := range errorParts(st.Error) {
				line("  · %s", part)
			}
		}
	}
	if r.Paired {
		mesh := 0
		if live && st.State == "connected" {
			mesh = st.MeshHubs
		}
		line("hub：%s", hubText(r.HubName, mesh, r.HubID, live && st.State == "connected"))
		if live && st.State == "connected" {
			switch st.Node {
			case "online":
				line("本机 node：在线")
			case "offline":
				line("本机 node：hub 连不上（%s）%s", st.NodeError, nodeHint(st.NodeError))
			default:
				line("本机 node：hub 正在连")
			}
		}
		line("这台电脑：hub 上的%s", machineText(r.MachineName, r.Machine))
		if r.Trust == link.TrustSystem {
			line("校验：经反向代理 · 系统证书（按系统信任的 CA 和域名，没有钉住 hub 的公钥）")
		}
		if len(r.Endpoints) > 0 || r.SSH != nil {
			line("候选地址（按尝试顺序）：")
			sshLine := func() {
				if r.SSH == nil {
					return
				}
				var notes []string
				if live && st.State == "connected" && st.Kind == link.KindSSH {
					notes = append(notes, "在用")
				}
				if r.SSH.Always {
					notes = append(notes, "总是走 SSH")
				}
				note := ""
				if len(notes) > 0 {
					note = "（" + strings.Join(notes, "，") + "）"
				}
				line("  · %s %s → %s%s", kindName(link.KindSSH), r.SSH.Server, r.SSH.Target, note)
			}
			if r.SSH != nil && r.SSH.Always {
				sshLine()
			}
			for _, ep := range r.Endpoints {
				var notes []string
				if live && st.State == "connected" && st.Kind != link.KindSSH && st.Address == ep.URL {
					notes = append(notes, "在用")
				}
				if ep.Manual {
					notes = append(notes, "手动加的")
				}
				if r.SSH != nil && r.SSH.Always {
					notes = append(notes, "已设总是走 SSH")
				}
				note := ""
				if len(notes) > 0 {
					note = "（" + strings.Join(notes, "，") + "）"
				}
				line("  · %s %s%s", kindName(ep.Kind), ep.URL, note)
			}
			if r.SSH != nil && !r.SSH.Always {
				sshLine()
			}
		}
	}
	r.serviceText(line, user)
	if live {
		line("进程：pid %d · 版本 %s", st.PID, st.Version)
	}
	line("日志：%s（persona-link logs -f）", r.Log)
	return b.String()
}

// passiveText is the report's part about passive mode.
func (r report) passiveText(line func(string, ...any), now time.Time, live bool) {
	p, st := r.Passive, r.Link
	listen := link.DefaultListen
	if p != nil {
		listen = p.Listen
	}
	if live && st.Listen != "" {
		listen = st.Listen
	}
	line("模式：%s，监听 %s", modeName(link.ModePassive), listen)
	paired := p != nil && p.Paired
	switch {
	case r.Error != "":
		line("凭据：读不了（%s）", r.Error)
	case !r.Running && st != nil && st.State != "stopped":
		line("链路：没在运行（%s最后一次报告：%s）", ago(now, st.Updated), stateText(&st.Status, now))
	case !r.Running:
		line("链路：没在运行")
	case st == nil:
		line("链路：在运行，还没报告状态")
	default:
		line("链路：%s", stateText(&st.Status, now))
		if st.State == "waiting" {
			for _, part := range errorParts(st.Error) {
				line("  · %s", part)
			}
		}
	}
	if !paired {
		line("配对：还没有 hub 来配对。persona-link pair --listen 打印链接，粘贴到 hub 管理界面「电脑 › 添加电脑 › hub 来连」")
	}
	if p != nil && p.CodeExpires != nil {
		line("配对链接：还剩 %s（只能用一次）", duration(p.CodeExpires.Sub(now)))
	}
	if paired {
		hub := "hub" + passiveHubText(&link.Passive{HubID: p.HubID, HubName: p.HubName})
		if p.PairedAt != nil {
			hub += fmt.Sprintf("（%s配对）", ago(now, *p.PairedAt))
		}
		line("%s", hub)
	}
	if live && st.LastSeen != nil {
		line("上一次 hub 连进来：%s，来自 %s", ago(now, *st.LastSeen), st.From)
	}
	if live && st.Node != "" {
		switch {
		case st.State == "connected" && st.Node == "online":
			line("本机 node：在线")
		case st.Node == "offline":
			line("本机 node：hub 连不上（%s）%s", st.NodeError, nodeHint(st.NodeError))
		}
	}
	if paired && p.Machine != "" {
		line("这台电脑：hub 上的%s", machineText(p.MachineName, p.Machine))
	}
	if p != nil && p.FP != "" {
		line("连接器指纹：%s", p.FP)
	}
	if live && len(st.Addrs) > 0 {
		line("hub 可以连的本机地址（按顺序）：")
		for _, a := range st.Addrs {
			line("  · %s", a)
		}
	}
	if r.Paired {
		line("主动模式也配对过（hub 上的「%s」）：persona-link mode active 切回去", r.Machine)
	}
}

// serviceText is the report's line about the connector's platform service.
func (r report) serviceText(line func(string, ...any), user string) {
	if s := r.Service; s != nil && s.Manager == "schtasks" {
		taskText(s, line)
	} else if s != nil && s.Manager == "launchd" {
		text := func(key string, args ...i18n.Args) string {
			return i18n.T(i18n.Terminal(), key, args...)
		}
		switch {
		case !s.Installed:
			line("%s", text("link-service.status_missing"))
		case s.Active != "":
			line("%s", text("link-service.status_loaded", i18n.Args{"label": s.Unit, "state": s.Active}))
		default:
			line("%s", text("link-service.status_not_loaded", i18n.Args{"label": s.Unit, "path": s.File}))
		}
	} else if s != nil {
		switch {
		case strings.HasPrefix(s.Missing, noUserInstance()) && s.Installed && !s.Linger:
			line("后台服务：装了 %s，但%s（%s）；没开 linger 时没人登录它就不在：sudo loginctl enable-linger %s",
				s.Unit, noUserInstance(), strings.TrimPrefix(s.Missing, noUserInstance()+"："), user)
		case s.Missing != "":
			line("后台服务：没有 systemd 用户服务（%s）", s.Missing)
		case !s.Installed:
			line("后台服务：没装（persona-link install）")
		default:
			linger := "linger 已开"
			if !s.Linger {
				linger = "linger 没开，退出登录就停：sudo loginctl enable-linger " + user
			}
			line("后台服务：systemd 用户服务 %s · %s · %s · %s", s.Unit, s.Active, s.Enabled, linger)
		}
	}
}

var kindNames = map[string]string{"lan": "内网", "ipv6": "公网 IPv6", "name": "域名", "ipv4": "公网 IPv4", link.KindSSH: "SSH"}

func kindName(kind string) string {
	if n, ok := kindNames[kind]; ok {
		return n
	}
	return "地址"
}

// addressText is a hub URL as the menu bar app shows it: its kind and host:port.
func addressText(kind, address string) string {
	host := address
	if u, err := url.Parse(address); err == nil && u.Host != "" {
		host = u.Host + u.Path // a reverse proxy's path says which hub
	}
	return kindName(kind) + " " + host
}

// machineText names this computer as the hub calls it, with its id when that is another ("「Studio」（id studio-pc）").
func machineText(name, id string) string {
	switch {
	case name == "" || name == id:
		return "「" + id + "」"
	case id == "":
		return "「" + name + "」"
	}
	return "「" + name + "」（id " + id + "）"
}

// hubText names the hub: by the name it gave, with its mesh's size when known (mesh 0); else by id, and a hub that
// is connected without having said its name is an older one.
func hubText(name string, mesh int, id string, connected bool) string {
	var s string
	switch {
	case name != "":
		s = "「" + name + "」"
	case id != "" && connected:
		s = id + "（旧版 hub 不报名字）"
	case id != "":
		s = id
	default:
		s = "hub"
	}
	switch {
	case mesh > 1:
		s += fmt.Sprintf("，互联组共 %d 台 hub", mesh)
	case mesh == 1:
		s += "，没和别的 hub 互联"
	}
	return s
}

// stateText is a status in a few words.
func stateText(s *link.Status, now time.Time) string {
	if s.Mode == link.ModePassive {
		switch s.State {
		case "connected":
			t := "已连接 · hub 从 " + s.Address + " 连进来"
			if s.Since != nil {
				t += " · 已连 " + duration(now.Sub(*s.Since))
			}
			return t
		case "listening":
			return "在监听，等 hub 连进来"
		case "unpaired":
			return "在监听，还没配对"
		case "waiting":
			t := "监听不了"
			if s.Retry != nil {
				if d := s.Retry.Sub(now).Round(time.Second); d > 0 {
					t += " · " + duration(d) + "后重试"
				} else {
					t += " · 马上重试"
				}
			}
			return t
		}
	}
	switch s.State {
	case "connected":
		t := "已连接 · " + addressText(s.Kind, s.Address)
		if s.Since != nil {
			t += " · 已连 " + duration(now.Sub(*s.Since))
		}
		return t
	case "connecting":
		return "正在找 hub"
	case "unpaired":
		return "还没配对"
	case "refused":
		return "hub 拒绝了这台的凭据"
	case "stopped":
		return "已停止"
	}
	t := "未连接"
	if s.Retry != nil {
		if d := s.Retry.Sub(now).Round(time.Second); d > 0 {
			t += " · " + duration(d) + "后重试"
		} else {
			t += " · 马上重试"
		}
	}
	return t
}

// errorParts: persona-link joins why each candidate failed with a full-width semicolon.
func errorParts(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "；") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func duration(d time.Duration) string {
	s := int(max(d, 0) / time.Second)
	switch {
	case s < 60:
		return fmt.Sprintf("%d 秒", s)
	case s < 3600:
		return fmt.Sprintf("%d 分钟", s/60)
	case s < 86400:
		return fmt.Sprintf("%d 小时 %d 分钟", s/3600, s%3600/60)
	}
	return fmt.Sprintf("%d 天 %d 小时", s/86400, s%86400/3600)
}
