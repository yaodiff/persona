package node

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/lazyre"
)

type codexProcess struct {
	pid            int
	home, endpoint string
	pane           string
	// herdrSession is the herdr session pane is in ("" the default server; herdr.EnvSession): the registry writes the
	// pane as the herdr host does (node herdrPaneIn).
	herdrSession string
	// desktop is true for the Codex desktop App GUI process or its bundled stdio app-server, not for CLI or unrelated
	// app-server processes.
	desktop    bool
	tmuxPane   string // TMUX_PANE (e.g. "%11")
	tmuxSocket string // tmux server socket extracted from TMUX ("" for the default server)
	herdrCore  string // HC_TERMINAL_ID
	ternPane   string // TERN_PANE
	orcaHandle string // ORCA_TERMINAL_HANDLE
	orcaPane   string // ORCA_PANE_KEY / ORCA_AGENT_PANE
}

func (p codexProcess) hasTakeoverTerminal() bool {
	return p.pane != "" || p.tmuxPane != "" || p.herdrCore != "" || p.ternPane != "" || p.orcaHandle != "" || p.orcaPane != ""
}

const (
	tmuxPaneEnv           = "TMUX_PANE"
	orcaTerminalHandleEnv = "ORCA_TERMINAL_HANDLE"
)

var (
	codexHomePS          = lazyre.New(`(?:^|\s)CODEX_HOME=(.*?)(?:\s+[A-Za-z_][A-Za-z_0-9]*=|$)`)
	codexHerdrPaneRe     = envVarRe(herdrPaneEnv)
	codexHerdrSessionRe  = envVarRe(herdrSessionEnv)
	codexHerdrSocketRe   = envVarRe(herdrSocketEnv)
	codexTmuxPaneRe      = envVarRe(tmuxPaneEnv)
	codexTmuxRe          = envVarRe(tmuxEnv)
	codexHerdrCorePaneRe = envVarRe(herdrCorePaneEnv)
	codexTernPaneRe      = envVarRe(ternPaneEnv)
	codexOrcaHandleRe    = envVarRe(orcaTerminalHandleEnv)
	codexOrcaPaneRe      = envVarRe(orcaPaneEnv)
	codexOrcaLegacyRe    = envVarRe(orcaLegacyPaneEnv)
)

// codexDesktopAppBundle reports whether exe is inside a Codex desktop App installation.
func codexDesktopAppBundle(exe string) bool {
	lower := strings.ToLower(strings.ReplaceAll(exe, `\`, "/"))
	return strings.Contains(lower, "/codex.app/") ||
		strings.Contains(lower, "/chatgpt.app/contents/resources/codex") ||
		strings.Contains(lower, "openai.codex") ||
		strings.HasSuffix(lower, "/codex-desktop") ||
		lower == "codex-desktop"
}

// codexDesktopAppGUI reports whether exe is the Codex desktop App GUI process itself (not a CLI or terminal binary).
func codexDesktopAppGUI(exe string) bool {
	lower := strings.ToLower(strings.ReplaceAll(exe, `\`, "/"))
	return strings.Contains(lower, "/codex.app/contents/macos/") ||
		strings.HasSuffix(lower, "/codex-desktop") ||
		lower == "codex-desktop" ||
		(strings.Contains(lower, "openai.codex") && (strings.HasSuffix(lower, "/codex.exe") || strings.HasSuffix(lower, "/chatgpt.exe")))
}

func codexProcessArgs(pid int, args []string, env string) (codexProcess, bool) {
	at := -1
	for i, a := range args {
		if a == "app-server" {
			at = i
			break
		}
	}
	if len(args) == 0 {
		return codexProcess{}, false
	}
	gui := codexDesktopAppGUI(args[0])
	if !gui && at < 1 && progName(args[0]) != "codex" { // progName: codex.exe on Windows
		return codexProcess{}, false
	}
	p := codexProcess{pid: pid}
	if at >= 1 {
		p.endpoint = "stdio://"
	}
	herdrEnv := map[string]string{}
	if strings.ContainsRune(env, '\x00') {
		for _, v := range strings.Split(env, "\x00") {
			switch {
			case strings.HasPrefix(v, herdrPaneEnv+"="):
				p.pane = strings.TrimPrefix(v, herdrPaneEnv+"=")
			case strings.HasPrefix(v, "CODEX_HOME="):
				p.home = strings.TrimPrefix(v, "CODEX_HOME=")
			case strings.HasPrefix(v, tmuxPaneEnv+"="):
				p.tmuxPane = strings.TrimPrefix(v, tmuxPaneEnv+"=")
			case strings.HasPrefix(v, tmuxEnv+"="):
				p.tmuxSocket = tmuxSocket(strings.TrimPrefix(v, tmuxEnv+"="))
			case strings.HasPrefix(v, herdrCorePaneEnv+"="):
				p.herdrCore = strings.TrimPrefix(v, herdrCorePaneEnv+"=")
			case strings.HasPrefix(v, ternPaneEnv+"="):
				p.ternPane = strings.TrimPrefix(v, ternPaneEnv+"=")
			case strings.HasPrefix(v, orcaTerminalHandleEnv+"="):
				p.orcaHandle = strings.TrimPrefix(v, orcaTerminalHandleEnv+"=")
			case strings.HasPrefix(v, orcaPaneEnv+"="):
				p.orcaPane = strings.TrimPrefix(v, orcaPaneEnv+"=")
			case strings.HasPrefix(v, orcaLegacyPaneEnv+"="):
				if p.orcaPane == "" {
					p.orcaPane = strings.TrimPrefix(v, orcaLegacyPaneEnv+"=")
				}
			default:
				for _, name := range []string{herdrSessionEnv, herdrSocketEnv} {
					if value, ok := strings.CutPrefix(v, name+"="); ok {
						herdrEnv[name] = value
					}
				}
			}
		}
	} else if env != "" {
		if m := codexHomePS.FindStringSubmatch(env); m != nil {
			p.home = m[1]
		}
		if m := codexHerdrPaneRe.FindStringSubmatch(env); m != nil {
			p.pane = m[1]
		}
		if m := codexHerdrSessionRe.FindStringSubmatch(env); m != nil {
			herdrEnv[herdrSessionEnv] = m[1]
		}
		if m := codexHerdrSocketRe.FindStringSubmatch(env); m != nil {
			herdrEnv[herdrSocketEnv] = m[1]
		}
		if m := codexTmuxPaneRe.FindStringSubmatch(env); m != nil {
			p.tmuxPane = m[1]
		}
		if m := codexTmuxRe.FindStringSubmatch(env); m != nil {
			p.tmuxSocket = tmuxSocket(m[1])
		}
		if m := codexHerdrCorePaneRe.FindStringSubmatch(env); m != nil {
			p.herdrCore = m[1]
		}
		if m := codexTernPaneRe.FindStringSubmatch(env); m != nil {
			p.ternPane = m[1]
		}
		if m := codexOrcaHandleRe.FindStringSubmatch(env); m != nil {
			p.orcaHandle = m[1]
		}
		if m := codexOrcaPaneRe.FindStringSubmatch(env); m != nil {
			p.orcaPane = m[1]
		} else if m := codexOrcaLegacyRe.FindStringSubmatch(env); m != nil {
			p.orcaPane = m[1]
		}
	}
	if p.pane != "" {
		p.herdrSession = herdr.EnvSession(func(name string) string { return herdrEnv[name] })
	}
	for i := at + 1; i < len(args); i++ {
		if args[i] == "--listen" && i+1 < len(args) {
			p.endpoint = args[i+1]
			break
		}
		if strings.HasPrefix(args[i], "--listen=") {
			p.endpoint = strings.TrimPrefix(args[i], "--listen=")
			break
		}
	}
	// The Codex desktop App itself or its bundled private stdio app-server (not a shared --listen daemon or an
	// unrelated CLI/assistant app-server).
	p.desktop = gui || (at >= 1 && p.endpoint == "stdio://" && codexDesktopAppBundle(args[0]))
	return p, true
}

func scanCodexProcesses() []codexProcess {
	out, _ := scanCodexProcessesChecked()
	return out
}

// scanCodexProcessesChecked is scanCodexProcesses, and whether the processes could be listed and read: a resume
// (codex_resume.go) must not take "could not tell" for "no Codex holds it". Windows reads its process table
// (scanCodexProcessesOS).
func scanCodexProcessesChecked() ([]codexProcess, bool) {
	if runtime.GOOS == "windows" {
		return scanCodexProcessesOS()
	}
	var out []codexProcess
	ok := true
	if runtime.GOOS == "linux" {
		// The shared walk of this user's /proc entries (userProcesses), as macOS's ps -U below.
		procs := userProcesses()
		for _, pid := range slices.Sorted(maps.Keys(procs)) {
			args := procs[pid]
			// Read no environments of unrelated processes.
			if _, ok := codexProcessArgs(pid, args, ""); !ok {
				continue
			}
			env, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
			if err != nil {
				if !os.IsNotExist(err) { // exited since the walk is fine; unreadable is not
					ok = false
				}
				continue
			}
			if p, ok := codexProcessArgs(pid, args, string(env)); ok {
				out = append(out, p)
			}
		}
		return out, ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "ps", "-E", "-ww", "-o", "pid=,command=", "-U", strconv.Itoa(os.Getuid())).Output()
	if err != nil {
		return nil, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		if p, ok := codexProcessArgs(pid, fields[1:], line); ok {
			out = append(out, p)
		}
	}
	return out, ok
}
