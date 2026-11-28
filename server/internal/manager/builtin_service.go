package manager

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/update"
)

// The built-in manager's own service (docs/assistant/managers.md "The built-in manager as its own service"): on Linux
// the systemd user unit persona-manager-assistant.service, on a Mac the launchd agent com.persona.manager.assistant
// (managerconf.BuiltinService, BuiltinLabel). Each runs `<binary> assistant` with PERSONA_ASSISTANT_DEPLOY=service on
// the data directory it shares with the hub, the binary persona-manager when builtinBinary finds it. `install
// --builtin` binds it when it has to and installs the service (`personad hub setup` runs it with --join), `uninstall
// --builtin` takes the service away and leaves the rest.

// builtinUnit is the built-in manager's systemd user unit: bin's `assistant` on the data directory home, keeping
// update.SplitEnv 0 when splitOff. Exit status 2 is a configuration mistake (not bound, another one running):
// restarting cannot fix it.
func builtinUnit(bin, home string, splitOff bool) string {
	keep := ""
	if splitOff {
		keep = "Environment=" + update.SplitEnv + "=0\n"
	}
	return `# Written by personad manager install --builtin (personad hub setup runs it); personad manager uninstall
# --builtin removes it (docs/assistant/managers.md "The built-in manager as its own service").
[Unit]
Description=Persona built-in manager
After=network-online.target

[Service]
ExecStart=` + systemdQuote(bin) + ` assistant
Environment=` + systemdEnvQuote("PERSONA_ASSISTANT_DEPLOY="+assistant.DeployService) + `
Environment=` + systemdEnvQuote("PERSONA_HOME="+home) + `
Environment=PATH=%h/.local/bin:%h/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin
` + keep + `Restart=always
RestartSec=5
RestartPreventExitStatus=2
TimeoutStopSec=20

[Install]
WantedBy=default.target
`
}

// builtinPlist is the built-in manager's launchd agent, with the hub's agent's PATH, keeping update.SplitEnv 0 when
// splitOff; its log is manager.log in its own directory.
func builtinPlist(bin, home, userHome string, splitOff bool) string {
	x := html.EscapeString
	keep := ""
	if splitOff {
		keep = "    <key>" + update.SplitEnv + "</key><string>0</string>\n"
	}
	path := strings.Join([]string{filepath.Join(userHome, ".local", "bin"), filepath.Join(userHome, "bin"),
		filepath.Join(userHome, ".orbstack", "bin"), "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":")
	log := filepath.Join(managerconf.DefaultDir(home), "manager.log")
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + managerconf.BuiltinLabel + `</string>
  <key>ProgramArguments</key><array><string>` + x(bin) + `</string><string>assistant</string></array>
  <key>EnvironmentVariables</key><dict>
    <key>PERSONA_ASSISTANT_DEPLOY</key><string>` + assistant.DeployService + `</string>
    <key>PERSONA_HOME</key><string>` + x(home) + `</string>
    <key>PATH</key><string>` + x(path) + `</string>
` + keep + `  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>5</integer>
  <key>StandardOutPath</key><string>` + x(log) + `</string>
  <key>StandardErrorPath</key><string>` + x(log) + `</string>
</dict></plist>
`
}

// bootstrapTries and bootstrapWait: how often, and how far apart, launchctl bootstrap is tried after a bootout.
var (
	bootstrapTries = 5
	bootstrapWait  = time.Second
)

// bootstrapAgent runs launchctl bootstrap for the plist at path in domain. Right after a bootout, bootstrap can fail
// for a moment (5: Input/output error, docs/lessons/server.md), so it is tried bootstrapTries times, bootstrapWait
// apart. The last attempt's output and error come back.
func bootstrapAgent(ctx context.Context, e *env, domain, path string) (string, error) {
	var out string
	var err error
	for try := 0; try < bootstrapTries; try++ {
		if try > 0 {
			time.Sleep(bootstrapWait)
		}
		if out, err = e.run(ctx, "launchctl", "bootstrap", domain, path); err == nil {
			return out, nil
		}
	}
	return out, err
}

// isBuiltin: the command's arguments ask for the built-in manager (--builtin).
func isBuiltin(args []string) bool {
	for _, a := range args {
		if a == "--builtin" || a == "-builtin" {
			return true
		}
	}
	return false
}

// builtinJoin is how install --builtin may bind the built-in manager: the hub's address and the code (from
// managerconf.BuiltinCodeEnv), with the pin or the plaintext allowance a join takes. always: trade the code even when
// the binding works (join --builtin --install).
type builtinJoin struct {
	hub, code, fingerprint string
	plaintext, always      bool
}

// installBuiltinArgs are install --builtin's arguments: `[--join <hub address>] [--fingerprint F] [--allow-plaintext]`,
// or `--check` alone.
type installBuiltinArgs struct {
	join      builtinJoin
	check     bool
	codeTaken bool // the code came from managerconf.BuiltinCodeEnv
}

// parseInstallBuiltin parses `install --builtin …` (managerconf.BuiltinInstallArgs and BuiltinCheckArgs are what the
// hub's setup runs). The code comes from managerconf.BuiltinCodeEnv, read with getenv.
func parseInstallBuiltin(args []string, getenv func(string) string, out io.Writer) (installBuiltinArgs, error) {
	var a installBuiltinArgs
	fs := flag.NewFlagSet("manager install --builtin", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Bool("builtin", true, "the built-in manager")
	join := fs.String("join", "", "the hub's address: bind it first with the code in "+managerconf.BuiltinCodeEnv+", unless its binding works")
	fp := fs.String("fingerprint", "", "the hub's TLS fingerprint (an https address with the hub's own certificate)")
	plain := fs.Bool("allow-plaintext", false, "allow a plaintext http hub address on the public internet")
	check := fs.Bool("check", false, "only say whether its service can be installed here (exit 0), changing nothing")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return a, err
	}
	if len(pos) != 0 || (*check && (*join != "" || *fp != "" || *plain)) {
		return a, errUsage
	}
	a.check = *check
	a.join = builtinJoin{hub: *join, fingerprint: *fp, plaintext: *plain}
	if a.join.hub != "" {
		a.join.code = strings.TrimSpace(getenv(managerconf.BuiltinCodeEnv))
		a.codeTaken = a.join.code != ""
	}
	return a, nil
}

// cmdInstallBuiltin is `install --builtin [--join <hub address>] [--fingerprint F] [--allow-plaintext]` or
// `install --builtin --check`.
func cmdInstallBuiltin(ctx context.Context, e *env, args []string) error {
	a, err := parseInstallBuiltin(args, e.getenv, e.stdout)
	if err != nil {
		return err
	}
	if a.codeTaken {
		_ = os.Unsetenv(managerconf.BuiltinCodeEnv) // not handed on to systemctl / launchctl
	}
	if a.check {
		if _, err := e.builtinPreflight(ctx); err != nil {
			return err
		}
		fmt.Fprintln(e.stdout, "The built-in manager's service can be installed here.")
		return nil
	}
	return e.installBuiltin(ctx, dataDir(), a.join)
}

// builtinPreflight is what can stop installing the built-in manager's service, checked before a code is spent; it
// returns the binary the service will run.
func (e *env) builtinPreflight(ctx context.Context) (string, error) {
	if e.goos != "linux" && e.goos != "darwin" {
		return "", fmt.Errorf("no service for it on %s: run it yourself with PERSONA_ASSISTANT_DEPLOY=%s %s assistant",
			e.goos, assistant.DeployService, e.self())
	}
	if err := e.checkCompatibleService(assistantwire.AssistantID); err != nil {
		return "", err
	}
	if e.goos == "linux" {
		if why := e.noSystemd(ctx); why != "" {
			return "", fmt.Errorf("cannot install its service (%s): run it yourself with PERSONA_ASSISTANT_DEPLOY=%s %s assistant",
				why, assistant.DeployService, e.self())
		}
	}
	return e.binary()
}

// installBuiltin installs the built-in manager's service on the data directory home and starts it, binding it first
// when j says how and its binding does not work (or j.always). What can stop the install is checked before a code is
// spent; a service that runs on the binding before a new one is restarted.
func (e *env) installBuiltin(ctx context.Context, home string, j builtinJoin) error {
	bin, err := e.builtinPreflight(ctx)
	if err != nil {
		return err
	}
	bin = e.builtinBinary(ctx, bin)
	b, traded, err := e.builtinBinding(ctx, home, j)
	if err != nil {
		return err
	}
	if traded {
		fmt.Fprintf(e.stdout, "Binding: bound to the hub at %s (in %s).\n", b.Hub, managerconf.DefaultFile(home))
	} else {
		fmt.Fprintf(e.stdout, "Binding: kept, to the hub at %s.\n", b.Hub)
	}
	return e.builtinService(ctx, home, bin, traded)
}

// builtinBinary is what the built-in manager's service runs: persona-manager beside bin when it is bin's build
// (docs/server/server.md "Programs"), else bin. A unit or agent under its former name, or update.SplitEnv 0
// (splitOff), keeps bin, and so does a Mac's agent that ran personad before (macKeepsPersonad).
func (e *env) builtinBinary(ctx context.Context, bin string) string {
	id := assistantwire.AssistantID
	if e.goos == "darwin" {
		if p, ok := e.macKeepsPersonad(id, bin); ok {
			return p
		}
	}
	former := (e.goos == "linux" && e.compatUnit(id) != unitName(id)) || (e.goos == "darwin" && e.compatLabel(id) != launchdLabel(id))
	if e.splitOff() || former {
		return bin
	}
	pm, note := update.SplitBeside(ctx, bin, "persona-manager", e.version)
	if pm == "" {
		if note != "" {
			fmt.Fprintf(e.stdout, "Program: %s (persona-manager not used: %s).\n", bin, note)
		}
		return bin
	}
	return pm
}

// hubLaunchdLabels are the hub's launchd agent's labels, today's (hub setup's hubLaunchdLabel) and the former one.
var hubLaunchdLabels = []string{"com.persona.hub", compat.HubLabel}

// macKeepsPersonad: this Mac was installed with personad, so the built-in manager's agent stays on it. That is the
// manager's own agent when it runs personad, or, before it has one, the hub's agent running personad: with the hub
// starting the manager as its child, the node of this computer runs under the hub's agent, so the user already allowed
// personad in Full Disk Access (macOS holds an agent and what it starts to the agent's program; the node of this
// computer runs under the manager's own agent once it has one). Staying keeps that entry; moving would need another
// one added. It stays on bin, or on the personad the agent ran when bin is persona-manager; when that one is gone it
// does not stay. Said when a persona-manager is beside it. An agent that runs personad for the opt-out keeps
// update.SplitEnv, and so is not that: it follows splitOff, as on Linux.
func (e *env) macKeepsPersonad(id, bin string) (string, bool) {
	was, ok := macPersonadAgent(e.plistPath(id))
	if !ok {
		if fileExists(e.plistPath(id)) {
			return "", false
		}
		for _, label := range hubLaunchdLabels {
			if was, ok = macPersonadAgent(filepath.Join(e.home, "Library", "LaunchAgents", label+".plist")); ok {
				break
			}
		}
		if !ok {
			return "", false
		}
	}
	p := bin
	if filepath.Base(bin) == "persona-manager" {
		if !fileExists(was) {
			return "", false
		}
		p = was
	}
	if fileExists(update.ProgramBeside(p, "persona-manager")) {
		fmt.Fprintf(e.stdout, "Program: %s (this Mac was installed with personad, which Full Disk Access allows for this computer's "+
			"node; it stays on it, so no new program has to be added there).\n", p)
	}
	return p, true
}

// macPersonadAgent is the personad a launchd agent's plist at path runs, true when it runs one (or the former name)
// without update.SplitEnv in it.
func macPersonadAgent(path string) (string, bool) {
	text, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(text), "<key>"+update.SplitEnv+"</key>") {
		return "", false
	}
	was := serviceProgram(string(text))
	if name := filepath.Base(was); was == "" || name != nodeproto.ProgramPersonad && name != compat.DaemonBinary {
		return "", false
	}
	return was, true
}

// splitOff says update.SplitEnv is 0 here. The unit written then keeps it, so the manager and the node it starts stay
// on personad too, until install --builtin runs again without it.
func (e *env) splitOff() bool { return strings.TrimSpace(e.getenv(update.SplitEnv)) == "0" }

// serviceProgram is the program a unit's or an agent's text runs: its first ExecStart with a value, or the first
// string of its ProgramArguments; "" when it has neither.
func serviceProgram(text string) string {
	if _, rest, ok := strings.Cut(text, "<key>ProgramArguments</key>"); ok {
		if _, rest, ok = strings.Cut(rest, "<string>"); ok {
			if arg, _, ok := strings.Cut(rest, "</string>"); ok {
				return html.UnescapeString(arg)
			}
		}
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		value = strings.TrimSpace(value)
		if !ok || strings.TrimSpace(key) != "ExecStart" || value == "" {
			continue
		}
		if !strings.HasPrefix(value, `"`) {
			prog, _, _ := strings.Cut(value, " ")
			return prog
		}
		var b strings.Builder
		for i := 1; i < len(value); i++ {
			switch c := value[i]; {
			case c == '"':
				return strings.NewReplacer("%%", "%", "$$", "$").Replace(b.String())
			case c == '\\' && i+1 < len(value):
				i++
				b.WriteByte(value[i])
			default:
				b.WriteByte(c)
			}
		}
		return ""
	}
	return ""
}

// programSwitch says the service's file (what: "the unit", "the agent") runs bin now when the text it had (before)
// ran another program; "" otherwise. The file's: a drop-in of the owner's may run another.
func programSwitch(what, before, bin string) string {
	if was := serviceProgram(before); was != "" && filepath.Clean(was) != filepath.Clean(bin) {
		return fmt.Sprintf("; %s runs %s now, not %s", what, bin, was)
	}
	return ""
}

// builtinBinding is the built-in manager's binding: the one in its manager.json when there is no code to trade, or
// when its hub still takes its token (a bare dial, which takes nothing over: probeToken); else a new one for j's code
// (traded true).
func (e *env) builtinBinding(ctx context.Context, home string, j builtinJoin) (b managerconf.Binding, traded bool, err error) {
	b, err = managerconf.ReadBinding(home)
	switch {
	case j.always && j.hub != "" && j.code != "":
	case err == nil:
		if j.hub == "" || j.code == "" {
			return b, false, nil
		}
		c := &assistant.ManagerConfig{ID: assistantwire.AssistantID, Hub: b.Hub, Token: b.Token, Fingerprint: b.Fingerprint}
		if probeToken(ctx, c) == nil {
			return b, false, nil // the code stays unspent and runs out
		}
	case !errors.Is(err, managerconf.ErrNotBound):
		return b, false, ConfigError{fmt.Errorf("%s: %w", managerconf.DefaultFile(home), err)}
	case j.hub == "":
		return b, false, ConfigError{errors.New("the built-in manager is not bound to a hub: persona-hub manager-code on " +
			"the hub's machine prints a code, then PERSONA_MANAGER_CODE=<the code> persona-manager join --builtin <hub address> --install")}
	case j.code == "":
		return b, false, ConfigError{fmt.Errorf("the built-in manager is not bound to a hub, and %s has no code: "+
			"persona-hub manager-code on the hub's machine prints one", managerconf.BuiltinCodeEnv)}
	}
	b, err = JoinBuiltin(ctx, JoinOptions{Hub: j.hub, Code: j.code, Lang: e.l(), Fingerprint: j.fingerprint, AllowPlaintext: j.plaintext}, home)
	return b, err == nil, err
}

// builtinService writes the built-in manager's unit (or agent) and makes sure it runs: started when it was not,
// restarted when the unit changed or bounce says so (a new binding), else left running.
func (e *env) builtinService(ctx context.Context, home, bin string, bounce bool) error {
	id := assistantwire.AssistantID
	switch e.goos {
	case "linux":
		path := e.unitPath(id)
		unit := e.compatUnit(id)
		want := e.compatUnitText(id, builtinUnit(bin, home, e.splitOff()))
		before, _ := os.ReadFile(path)
		changed := string(before) != want
		if changed {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := writeServiceFile(path, want); err != nil {
				return err
			}
		}
		_, activeErr := e.systemctl(ctx, "is-active", "--quiet", unit)
		steps := [][]string{{"daemon-reload"}, {"enable", unit}}
		what := "running already, left as it was"
		switch {
		case activeErr != nil:
			steps, what = append(steps, []string{"restart", unit}), "started"
		case changed || bounce:
			steps, what = append(steps, []string{"restart", unit}), "restarted"
		}
		for _, step := range steps {
			if out, err := e.systemctl(ctx, step...); err != nil {
				return fmt.Errorf("systemctl --user %s failed: %s", strings.Join(step, " "), firstLine(out+" "+err.Error()))
			}
		}
		fmt.Fprintf(e.stdout, "Service: systemd user unit %s, %s%s.\n  Log: journalctl --user -u %s -f\n", unit, what, programSwitch("the unit", string(before), bin), unit)
		if e.user != "" && !fileExists("/var/lib/systemd/linger/"+e.user) {
			fmt.Fprintf(e.stdout, "  Linger is off: it stops when you log out. sudo loginctl enable-linger %s keeps it running\n", e.user)
		}
		return nil
	case "darwin":
		path := e.plistPath(id)
		label := e.compatLabel(id)
		want := e.compatLabelText(id, builtinPlist(bin, home, e.home, e.splitOff()))
		before, _ := os.ReadFile(path)
		changed := string(before) != want
		if err := os.MkdirAll(managerconf.DefaultDir(home), 0o700); err != nil { // its log's directory
			return err
		}
		if changed {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := writeServiceFile(path, want); err != nil {
				return err
			}
		}
		domain := "gui/" + strconv.Itoa(e.uid)
		_, loadedErr := e.run(ctx, "launchctl", "print", domain+"/"+label)
		what := "running already, left as it was"
		switch {
		case changed || loadedErr != nil:
			_, _ = e.run(ctx, "launchctl", "bootout", domain+"/"+label)
			if out, err := bootstrapAgent(ctx, e, domain, path); err != nil {
				return fmt.Errorf("launchctl bootstrap failed: %s", firstLine(out+" "+err.Error()))
			}
			what = "started"
		case bounce:
			if out, err := e.run(ctx, "launchctl", "kickstart", "-k", domain+"/"+label); err != nil {
				return fmt.Errorf("launchctl kickstart failed: %s", firstLine(out+" "+err.Error()))
			}
			what = "restarted"
		}
		moved := programSwitch("the agent", string(before), bin)
		fmt.Fprintf(e.stdout, "Service: launchd agent %s, %s%s; it runs whenever you are logged in.\n  Log: %s\n", label, what,
			moved, filepath.Join(managerconf.DefaultDir(home), "manager.log"))
		if moved != "" {
			fmt.Fprintf(e.stdout, "  Full Disk Access: this computer's node now reads with %s's permissions (macOS holds what an agent "+
				"starts to its program); add it there if the node needs Desktop, Documents, Downloads or Messages.\n", filepath.Base(bin))
		}
		return nil
	}
	return fmt.Errorf("no service for it on %s", e.goos)
}

// cmdUninstallBuiltin is `uninstall --builtin`: the service goes, its binding and its data stay.
func cmdUninstallBuiltin(ctx context.Context, e *env, args []string) error {
	for _, a := range args {
		if !isBuiltin([]string{a}) {
			return errUsage
		}
	}
	if err := e.removeService(ctx, &assistant.ManagerConfig{ID: assistantwire.AssistantID}); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Its binding and its data stay (%s). While the hub has assistant.mode service nothing runs the "+
		"built-in manager now: %s manager install --builtin puts the service back, or the hub starts it itself again "+
		"after persona-hub manager-mode child and persona-hub switch.\n", managerconf.DefaultDir(dataDir()), e.self())
	return nil
}

// builtinStatus prints the built-in manager's binding and service, when it has either.
func (e *env) builtinStatus(ctx context.Context, always bool) {
	home := dataDir()
	id := assistantwire.AssistantID
	installed := (e.goos == "linux" && fileExists(e.unitPath(id))) || (e.goos == "darwin" && fileExists(e.plistPath(id)))
	b, err := managerconf.ReadBinding(home)
	if !always && !installed && errors.Is(err, managerconf.ErrNotBound) {
		return
	}
	binding := "hub " + b.Hub
	switch {
	case errors.Is(err, managerconf.ErrNotBound):
		binding = "not bound"
	case err != nil:
		binding = err.Error()
	}
	service := "not installed (" + e.self() + " manager install --builtin)"
	if installed {
		service = e.serviceState(ctx, id)
	}
	fmt.Fprintf(e.stdout, "Built-in manager (%s)\n  Directory: %s\n  Binding: %s\n  Service: %s\n", id,
		managerconf.DefaultDir(home), binding, service)
}

// --- a new binary ---------------------------------------------------------------------------------------------------

// The built-in manager's service starts again in place on a new binary (RunBuiltin): an update puts one where it runs
// from (the hub's self-update swaps it with the hub's), and nothing else restarts this service. It does so only with
// a binary that runs (checkBinary) and once nothing is going on (assistant.Idle), waiting idleMost at most. A new file
// that does not run, or that it cannot start on, is not tried again: it goes on with the code it runs until another
// file is there.
var (
	binaryCheck       = time.Minute      // how often it looks at the file
	idleCheck         = 10 * time.Second // how often it asks whether it is idle, once a new binary passed its check
	idleMost          = 30 * time.Minute // then it starts again anyway, and says so (RunBuiltin only)
	versionWait       = 30 * time.Second // how long the new binary's `version -v` may take
	restartLoopWindow = 5 * time.Minute  // cross-process restart-loop guard window for the same target binary
	binaryWatchNow    = time.Now
)

const binaryRestartGuardFile = ".binary-restart.json"

type binaryRestartRecord struct {
	Target fileIdentity `json:"target"`
	At     time.Time    `json:"at"`
}

// runningBinary is the file this process runs from and what it was when looked at; ok false when it cannot tell.
var runningBinary = func() (path string, fi os.FileInfo, ok bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil, false
	}
	exe = strings.TrimSuffix(exe, " (deleted)")
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	fi, err = runningBinaryInfo(exe)
	if err != nil {
		return "", nil, false
	}
	return exe, fi, true
}

// sameFile: a and b are the same file, as it was (written over in place, it is not).
func sameFile(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.ModTime().Equal(b.ModTime()) && a.Size() == b.Size()
}

// checkBinary: the file at path runs: an executable regular file whose `version -v` answers (exit 0, something
// said) within versionWait.
var checkBinary = func(ctx context.Context, path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable file", path)
	}
	ctx, cancel := context.WithTimeout(ctx, versionWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version", "-v")
	cmd.WaitDelay = time.Second
	killGroup(cmd) // a hung one goes with whatever it started
	b, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return fmt.Errorf("%s version -v: %w: %s", path, err, firstLine(string(ee.Stderr)))
		}
		return fmt.Errorf("%s version -v: %w", path, err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return fmt.Errorf("%s version -v said nothing", path)
	}
	return nil
}

// binaryWatch follows the file a supervised manager service runs from.
type binaryWatch struct {
	path string
	// seen is the file there last looked at that needs no restart: the one at start, or one that did not run or that
	// it could not start on (not tried again).
	seen os.FileInfo
	out  io.Writer
	// logPrefix names the component in log lines (defaults to "assistant").
	logPrefix string
	// guardPath optionally persists the last restart target identity across process restarts to break restart loops.
	guardPath string
	// waitUntilIdle, when true, never interrupts active turns past idleMost and cancels waiting if the file reverts.
	waitUntilIdle bool
	hostDataDir   string
	hubVersion    atomic.Pointer[func() string]
}

// newBinaryWatch follows the file this process runs from; nil when it cannot tell which.
func newBinaryWatch(out io.Writer) *binaryWatch {
	return newBinaryWatchWithGuard(out, "")
}

// newBinaryWatchWithGuard follows the file this process runs from, cleaning up guardPath if the current process is
// already running the previously targeted binary.
func newBinaryWatchWithGuard(out io.Writer, guardPath string) *binaryWatch {
	path, fi, ok := runningBinary()
	if !ok {
		return nil
	}
	w := &binaryWatch{path: path, seen: fi, out: out, logPrefix: "assistant", guardPath: guardPath}
	if guardPath != "" {
		if rec, ok := w.readRestartRecord(); ok && rec.Target == fileIdentityOf(fi) {
			_ = os.Remove(guardPath)
		}
	}
	return w
}

func (w *binaryWatch) prefix() string {
	if w.logPrefix != "" {
		return w.logPrefix
	}
	return "assistant"
}

// A service using the Hub's installed programs follows the slot that
// actually took over. This also holds it back during a self-update trial.
func (w *binaryWatch) confirmedReplacement(ctx context.Context) bool {
	if w.hostDataDir == "" {
		return true
	}
	bin, err := filepath.EvalSymlinks(filepath.Join(w.hostDataDir, "bin"))
	if err != nil || filepath.Dir(w.path) != bin {
		return true
	}
	pending := update.PendingPath(filepath.Join(w.hostDataDir, "update"))
	if _, err := os.Lstat(pending); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	if _, err := os.Lstat(update.DevelopmentAdoptionPath(filepath.Dir(pending))); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	control := filepath.Join(w.hostDataDir, "run", "hub-edge.sock")
	if info, err := os.Stat(control); err != nil || info.Mode()&os.ModeSocket == 0 {
		return true // standalone host or a Hub without an edge
	}
	hubVersion := w.hubVersion.Load()
	if hubVersion == nil {
		return false
	}
	want := (*hubVersion)()
	if want == "" {
		return false
	}
	got, err := update.BuildOf(ctx, w.path)
	version, _, _ := strings.Cut(got, "\n")
	return err == nil && version == want && (*hubVersion)() == want
}

func (w *binaryWatch) setHubVersion(version func() string) {
	if w != nil {
		w.hubVersion.Store(&version)
	}
}

func (w *binaryWatch) readRestartRecord() (binaryRestartRecord, bool) {
	if w.guardPath == "" {
		return binaryRestartRecord{}, false
	}
	b, err := os.ReadFile(w.guardPath)
	if err != nil {
		return binaryRestartRecord{}, false
	}
	var rec binaryRestartRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return binaryRestartRecord{}, false
	}
	return rec, true
}

func (w *binaryWatch) loopGuarded(fi os.FileInfo) bool {
	if w.guardPath == "" || fi == nil {
		return false
	}
	rec, ok := w.readRestartRecord()
	if !ok || rec.At.IsZero() {
		return false
	}
	if rec.Target != fileIdentityOf(fi) {
		return false
	}
	age := binaryWatchNow().Sub(rec.At)
	return age >= 0 && age < restartLoopWindow
}

func (w *binaryWatch) recordRestartAttempt(fi os.FileInfo) {
	if w.guardPath == "" || fi == nil {
		return
	}
	b, err := json.Marshal(binaryRestartRecord{Target: fileIdentityOf(fi), At: binaryWatchNow().UTC()})
	if err != nil {
		return
	}
	_ = managerconf.WriteFileAtomic(w.guardPath, b, 0o600)
}

type waitIdleResult int

const (
	waitIdleCanceled waitIdleResult = iota
	waitIdleReady
	waitIdleAborted
)

// next returns once another file is there that runs and the manager is idle (idle gives its Idle, nil while it has
// none yet), or idleMost passed (when waitUntilIdle is false): true then. False when ctx ends first.
func (w *binaryWatch) next(ctx context.Context, idle func() assistant.Idle) bool {
	return w.nextWith(ctx, idle, nil)
}

// nextWith is next, additionally notifying onPending(true) while waiting on busy work and onPending(false) if the
// replaced file is reverted or invalidated before the manager becomes idle.
func (w *binaryWatch) nextWith(ctx context.Context, idle func() assistant.Idle, onPending func(bool)) bool {
	t := time.NewTicker(binaryCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
		fi, err := os.Stat(w.path)
		if err != nil || sameFile(fi, w.seen) || !w.confirmedReplacement(ctx) {
			continue
		}
		if w.loopGuarded(fi) {
			fmt.Fprintf(w.out, "%s: %s was replaced, but a restart onto this same binary was already attempted recently "+
				"(restart-loop guard): going on with this one until it is replaced again\n", w.prefix(), w.path)
			w.seen = fi
			continue
		}
		if err := checkBinary(ctx, w.path); err != nil {
			if ctx.Err() != nil {
				return false
			}
			fmt.Fprintf(w.out, "%s: %s was replaced, but the new one does not run (%v): going on with this one until "+
				"it is replaced again\n", w.prefix(), w.path, err)
			w.seen = fi
			continue
		}
		if w.waitUntilIdle {
			fmt.Fprintf(w.out, "%s: %s was replaced (an update): restarting gracefully once nothing is going on\n", w.prefix(), w.path)
		} else {
			fmt.Fprintf(w.out, "%s: %s was replaced (an update): starting again on the new one once nothing is going on "+
				"(at most %s from now)\n", w.prefix(), w.path, idleMost)
		}
		switch w.waitIdle(ctx, fi, idle, onPending) {
		case waitIdleReady:
			return true
		case waitIdleAborted:
			continue
		default:
			return false
		}
	}
}

// idleAnswer is one answer of the manager's Idle.
type idleAnswer struct {
	idle bool
	why  string
}

// waitIdle returns waitIdleReady once the manager is idle (or idleMost passed when !waitUntilIdle), waitIdleAborted
// if the replacement file disappeared/reverted/failed re-verification while waiting under waitUntilIdle, or
// waitIdleCanceled when ctx ends first. An Idle that does not answer within idleCheck counts as busy, and is not
// asked again until it has answered; that late answer is dropped (it is old by then), and it is asked anew.
func (w *binaryWatch) waitIdle(ctx context.Context, target os.FileInfo, idle func() assistant.Idle, onPending func(bool)) waitIdleResult {
	deadline := time.Now().Add(idleMost)
	said := ""
	reportedPending := false
	setPending := func(v bool) {
		if onPending != nil && reportedPending != v {
			reportedPending = v
			onPending(v)
		}
	}
	var asked chan idleAnswer // an Idle asked and not answered yet
	late := false             // asked has not answered in time
	const noAnswer = "its check of what is going on does not answer"
	first := true
	for {
		if w.waitUntilIdle && !first {
			cur, err := os.Stat(w.path)
			switch {
			case err != nil || sameFile(cur, w.seen):
				setPending(false)
				fmt.Fprintf(w.out, "%s: pending restart for %s cancelled (file reverted or gone): continuing on current binary\n", w.prefix(), w.path)
				return waitIdleAborted
			case !sameFile(cur, target):
				if w.loopGuarded(cur) {
					setPending(false)
					fmt.Fprintf(w.out, "%s: %s was replaced, but a restart onto this same binary was already attempted recently "+
						"(restart-loop guard): going on with this one until it is replaced again\n", w.prefix(), w.path)
					w.seen = cur
					return waitIdleAborted
				}
				if err := checkBinary(ctx, w.path); err != nil {
					if ctx.Err() != nil {
						return waitIdleCanceled
					}
					setPending(false)
					fmt.Fprintf(w.out, "%s: %s was replaced while waiting for idle, but the new one does not run (%v): "+
						"going on with this one until it is replaced again\n", w.prefix(), w.path, err)
					w.seen = cur
					return waitIdleAborted
				}
				target = cur
			}
		}
		first = false
		why := "it is not running yet"
		if p := idle(); p != nil {
			if late {
				select {
				case <-asked: // answered at last: too old to go by
					asked, late = nil, false
				default:
				}
			}
			if asked == nil {
				asked = make(chan idleAnswer, 1)
				go func(ch chan<- idleAnswer, most time.Duration) {
					pctx, cancel := context.WithTimeout(ctx, most)
					defer cancel()
					ok, busy := p(pctx)
					ch <- idleAnswer{ok, busy}
				}(asked, idleCheck) // read here: the check may outlive this wait
			}
			why = noAnswer
			if !late {
				select {
				case a := <-asked:
					asked = nil
					if a.idle {
						return waitIdleReady
					}
					why = a.why
				case <-time.After(idleCheck):
					late = true
				case <-ctx.Done():
					return waitIdleCanceled
				}
			}
		}
		if ctx.Err() != nil {
			return waitIdleCanceled
		}
		setPending(true)
		if !w.waitUntilIdle && !time.Now().Before(deadline) {
			fmt.Fprintf(w.out, "%s: still not idle after %s (%s): starting again on the new binary anyway\n", w.prefix(), idleMost, why)
			return waitIdleReady
		}
		if why != said {
			fmt.Fprintf(w.out, "%s: waiting to start again on the new binary: %s\n", w.prefix(), why)
			said = why
		}
		select {
		case <-ctx.Done():
			return waitIdleCanceled
		case <-time.After(idleCheck):
		}
	}
}

// prepareExitForNew re-verifies the replaced file after the manager has stopped cleanly. When valid and not guarded,
// it records the restart-loop guard and returns true so the process exits cleanly for systemd/launchd to restart onto
// the new binary. Otherwise it updates w.seen when appropriate and returns false so the manager keeps running.
func (w *binaryWatch) prepareExitForNew(ctx context.Context) bool {
	fi, err := os.Stat(w.path)
	switch {
	case ctx.Err() != nil:
		return false
	case !w.confirmedReplacement(ctx):
		return false
	case err != nil:
		fmt.Fprintf(w.out, "%s: the new %s is gone (%v): going on with this one\n", w.prefix(), w.path, err)
		return false
	case sameFile(fi, w.seen):
		fmt.Fprintf(w.out, "%s: %s is a file it does not start on again (put back): going on with this one\n", w.prefix(), w.path)
		return false
	case w.loopGuarded(fi):
		fmt.Fprintf(w.out, "%s: restart onto %s blocked by restart-loop guard: going on with this one\n", w.prefix(), w.path)
		w.seen = fi
		return false
	}
	if err := checkBinary(ctx, w.path); err != nil {
		if ctx.Err() == nil {
			fmt.Fprintf(w.out, "%s: the new %s does not run any more (%v): going on with this one\n", w.prefix(), w.path, err)
			w.seen = fi
		}
		return false
	}
	w.recordRestartAttempt(fi)
	fmt.Fprintf(w.out, "%s: exiting gracefully to restart on the new %s\n", w.prefix(), w.path)
	return true
}

// restartOnNew starts this process again in place on the new file (the manager stopped). It checks the file once
// more first; a file that does not run then, or an exec that fails, leaves it running the code it has (RunBuiltin
// runs the manager again), and that file is not tried again. Tests (restart nil): as though it had started again.
func (w *binaryWatch) restartOnNew(ctx context.Context, env []string) {
	fi, err := os.Stat(w.path)
	switch {
	case ctx.Err() != nil:
		return
	case !w.confirmedReplacement(ctx):
		return
	case err != nil:
		fmt.Fprintf(w.out, "assistant: the new %s is gone (%v): going on with this one\n", w.path, err)
		return
	case sameFile(fi, w.seen):
		// put back while it waited (the very file it started with, renamed back; or one it did not take)
		fmt.Fprintf(w.out, "assistant: %s is a file it does not start on again (put back): going on with this one\n", w.path)
		return
	}
	if err := checkBinary(ctx, w.path); err != nil {
		if ctx.Err() == nil {
			fmt.Fprintf(w.out, "assistant: the new %s does not run any more (%v): going on with this one\n", w.path, err)
			w.seen = fi
		}
		return
	}
	fmt.Fprintf(w.out, "assistant: starting again on the new %s\n", w.path)
	if restart == nil {
		w.seen = fi
		return
	}
	if err := restart(env); err != nil {
		fmt.Fprintf(w.out, "assistant: could not start again on the new %s (%v): going on with this one until it is "+
			"replaced again\n", w.path, err)
		w.seen = fi
	}
}

// BindBuiltin binds the built-in manager to the hub at hub unless its binding works (its hub takes its token, a bare
// dial that takes nothing over): then code gives a one-time code (the hub's machine makes it), traded here. A
// container's supervisor (internal/supervise) runs it before it starts the built-in manager; traded: a new binding.
func BindBuiltin(ctx context.Context, home, hub string, code func(context.Context) (string, error)) (traded bool, err error) {
	b, err := managerconf.ReadBinding(home)
	switch {
	case err == nil:
		c := &assistant.ManagerConfig{ID: assistantwire.AssistantID, Hub: b.Hub, Token: b.Token, Fingerprint: b.Fingerprint}
		if probeToken(ctx, c) == nil {
			return false, nil
		}
	case !errors.Is(err, managerconf.ErrNotBound):
		return false, ConfigError{fmt.Errorf("%s: %w", managerconf.DefaultFile(home), err)}
	}
	c, err := code(ctx)
	if err != nil {
		return false, err
	}
	if _, err := JoinBuiltin(ctx, JoinOptions{Hub: hub, Code: c}, home); err != nil {
		return false, err
	}
	return true, nil
}
