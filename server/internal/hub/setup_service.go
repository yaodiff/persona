package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/update"
	"html"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The service that keeps the hub running (docs/assistant/onboarding.md 4.1 step 3): on Linux the systemd user units of
// scripts/deploy/hub-systemd.sh (socket activation, docs/operations/hub-restart.md; keep the two in step), on a Mac a launchd
// agent.

func (e *setupEnv) service(ctx context.Context, cfg *setupConfig) error {
	if e.o.noService {
		e.report("service", true, i18n.T(e.l, "hub-setup.service_skipped"), "")
		return nil
	}
	if err := e.portsFree(ctx, cfg); err != nil {
		return err
	}
	bin, err := e.hubBinary()
	if err != nil {
		return e.fail("service", i18n.T(e.l, "hub-setup.binary_copy_failed", i18n.Args{"path": filepath.Join(e.root, "bin", "personad"), "error": e.errText(err)}),
			i18n.T(e.l, "hub-setup.check_permissions"))
	}
	switch e.goos {
	case "linux":
		return e.systemdService(ctx, cfg.Config, bin)
	case "darwin":
		return e.launchdService(ctx, cfg.Config, bin)
	}
	return e.fail("service", i18n.T(e.l, "hub-setup.service_unsupported", i18n.Args{"os": e.goos}), i18n.T(e.l, "hub-setup.service_unsupported_next"))
}

// splitHub is <root>/bin/persona-hub when the hub's systemd unit runs it in personad's place (docs/server/server.md
// "Programs"): it is there, it is <root>/bin/personad's build, and update.SplitEnv is not 0 (splitOff); "" otherwise.
// note says why a persona-hub there was passed over. <root>/bin/personad, not the file it links to: the service names
// this one.
func (e *setupEnv) splitHub(ctx context.Context) (prog, note string) {
	if e.splitOff() {
		return "", ""
	}
	return update.SplitBeside(ctx, filepath.Join(e.root, "bin", "personad"), "persona-hub", e.programVersion)
}

// splitOff says setup runs with update.SplitEnv 0. The unit it writes then keeps it, so the hub and what it starts
// stay on personad too, until setup runs again without it.
func (e *setupEnv) splitOff() bool { return strings.TrimSpace(e.getenv(update.SplitEnv)) == "0" }

// macStaysNote is what setup on a Mac says when the hub's agent ran personad before (an installed Mac) and a
// persona-hub is beside it: the agent stays on personad. macOS holds a LaunchAgent and what it starts to the agent's
// program for its privacy permissions (Full Disk Access): staying keeps the entry the user allowed; moving would need
// another one added. In l, with the separator that joins it to the line before.
func macStaysNote(l i18n.Lang) string {
	return i18n.T(l, "hub-setup.sep") + i18n.T(l, "hub-setup.mac_stays")
}

// macSwitchNote is what setup on a Mac adds when the hub's agent moved to another program (to): what the agent starts
// reads with that program's privacy permissions now.
func macSwitchNote(l i18n.Lang, to string) string {
	return i18n.T(l, "hub-setup.sep") + i18n.T(l, "hub-setup.mac_switch", i18n.Args{"program": filepath.Base(to)})
}

// programVersion is what the program at path says to `version -v`.
func (e *setupEnv) programVersion(ctx context.Context, path string) (string, error) {
	out, err := e.run(ctx, nil, path, "version", "-v")
	return strings.TrimSpace(string(out)), err
}

// switchNote says the hub's service moved from one program to the other (before and after: what it ran and runs, as
// paths; "" when this run cannot tell); restarted: this run restarted the hub and the report does not say so yet;
// child: the built-in manager restarted with it, as the hub's child. In l, with the separator that joins it on.
func switchNote(l i18n.Lang, before, after string, restarted, child bool) string {
	if before == "" || after == "" || resolvedPath(before) == resolvedPath(after) {
		return ""
	}
	key := "hub-setup.switched"
	switch {
	case restarted && child:
		key = "hub-setup.switched_restarted_child"
	case restarted:
		key = "hub-setup.switched_restarted"
	case child:
		key = "hub-setup.switched_child"
	}
	return i18n.T(l, "hub-setup.sep") + i18n.T(l, key, i18n.Args{"program": filepath.Base(after)})
}

// ownProgram says p is a program setup writes into the hub's service: this root's personad (or its former name) or
// persona-hub. Anything else there (a wrapper an owner's drop-in runs) is not a switch.
func (e *setupEnv) ownProgram(p string) bool {
	if p == "" {
		return false
	}
	r := resolvedPath(p)
	for _, name := range []string{"personad", compat.DaemonBinary, "persona-hub"} {
		if r == resolvedPath(filepath.Join(e.root, "bin", name)) {
			return true
		}
	}
	return false
}

// runsPersonad says p is this root's personad (or its former name): what an installed Mac's hub agent runs.
func (e *setupEnv) runsPersonad(p string) bool {
	if p == "" {
		return false
	}
	r := resolvedPath(p)
	for _, name := range []string{"personad", compat.DaemonBinary} {
		if r == resolvedPath(filepath.Join(e.root, "bin", name)) {
			return true
		}
	}
	return false
}

// portsFree: the hub's ports are free, or held by this hub (its socket unit, or it answers with this token).
func (e *setupEnv) portsFree(ctx context.Context, cfg *setupConfig) error {
	addrs := []string{cfg.Listen}
	if cfg.Public.TLSListen != "" {
		addrs = append(addrs, cfg.Public.TLSListen)
	}
	var busy []string
	for _, a := range addrs {
		if p, err := strconv.Atoi(portOf(a)); err == nil && !e.portFree(p) {
			busy = append(busy, portOf(a))
		}
	}
	if len(busy) == 0 || e.hubAnswers(ctx, cfg.Config) || e.ownSocketActive(ctx) {
		return nil
	}
	return e.fail("service", i18n.T(e.l, "hub-setup.ports_busy", i18n.Args{"ports": strings.Join(busy, i18n.T(e.l, "hub-setup.list_separator")), "count": len(busy)}),
		i18n.T(e.l, "hub-setup.ports_busy_next", i18n.Args{"path": e.hubJSON()}))
}

func (e *setupEnv) ownSocketActive(ctx context.Context) bool {
	if e.goos != "linux" {
		return false
	}
	_, err := e.run(ctx, nil, "systemctl", "--user", "is-active", "--quiet", e.compatibleHubName("personad.socket"))
	return err == nil
}

// hubBinary is <root>/bin/personad, where the units run it from and self-update replaces it; copied from this
// executable when it is not there yet (personad run from somewhere else), with personad next to it.
func (e *setupEnv) hubBinary() (string, error) {
	return e.compatibleHubBinary()
}

// binaryStamp is where setup notes which <root>/bin/personad the service was last (re)started with.
func binaryStamp(bin string) string {
	return filepath.Join(filepath.Dir(bin), ".personad.setup-sha256")
}

// binaryChanged says whether bin differs from the one the service last started with (install.sh replaced it, an
// upgrade): the running hub is then still the old one and has to be restarted. Without a note it cannot tell, so it
// says changed: one restart, which socket activation makes unnoticeable. sum goes to noteBinary once restarted.
func binaryChanged(bin string) (sum string, changed bool) {
	f, err := os.Open(bin)
	if err != nil {
		return "", true
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", true
	}
	sum = hex.EncodeToString(h.Sum(nil))
	old, err := os.ReadFile(binaryStamp(bin))
	return sum, err != nil || strings.TrimSpace(string(old)) != sum
}

func noteBinary(bin, sum string) {
	if sum != "" {
		_ = os.WriteFile(binaryStamp(bin), []byte(sum+"\n"), 0o644)
	}
}

func copyFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := to + ".setup-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, to)
}

// --- systemd ------------------------------------------------------------------------------------------------------

func systemdQuoteValue(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(v) + `"`
}

// hubExecStart is the hub's ExecStart, as hub-systemd.sh writes it: `<root>/bin/personad hub`, or with split
// `<root>/bin/persona-hub run`.
func hubExecStart(root string, split bool) string {
	if split {
		return "ExecStart=" + strings.ReplaceAll(systemdQuoteValue(root+"/bin/persona-hub"), "$", "$$") + " run"
	}
	return "ExecStart=" + strings.ReplaceAll(systemdQuoteValue(root+"/bin/personad"), "$", "$$") + " hub"
}

// hubServiceUnit is personad.service, as hub-systemd.sh writes it; split: it runs persona-hub; splitOff: it keeps
// update.SplitEnv 0 (hub-systemd.sh keeps it, as it keeps a unit's own lines).
func hubServiceUnit(root string, split, socket, splitOff bool) string {
	description := "Description=Persona hub (personad)"
	if split {
		description = "Description=Persona hub (persona-hub)"
	}
	lines := []string{"[Unit]", description, "After=network-online.target"}
	if socket {
		lines = append(lines, "Wants=personad.socket", "After=personad.socket")
	}
	lines = append(lines, "", "[Service]",
		hubExecStart(root, split),
		"Restart=always", "RestartSec=3", "TimeoutStopSec=20",
		"Environment="+systemdQuoteValue("PERSONA_HOME="+root),
		"Environment=PATH=%h/.local/bin:%h/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin")
	if splitOff {
		lines = append(lines, "Environment="+update.SplitEnv+"=0")
	}
	lines = append(lines, "", "[Install]", "WantedBy=default.target", "")
	return strings.Join(lines, "\n")
}

// hubSocketUnit is personad.socket, as hub-systemd.sh writes it: the plaintext and TLS addresses. FreeBind lets an
// address-bound listener (a new install's "<LAN address>:18662") come up while that address is not there yet or any
// more (DHCP), instead of failing the socket and with it the plaintext port.
func hubSocketUnit(listens []string) string {
	lines := []string{"[Unit]", "Description=Persona hub's listening sockets (docs/operations/hub-restart.md)", "", "[Socket]"}
	bound := false
	for _, a := range listens {
		if strings.HasPrefix(a, ":") {
			a = a[1:]
		} else {
			bound = true
		}
		lines = append(lines, "ListenStream="+a)
	}
	lines = append(lines, "Backlog=4096", "NoDelay=true")
	if bound {
		lines = append(lines, "FreeBind=true")
	}
	lines = append(lines, "Service=personad.service", "", "[Install]", "WantedBy=sockets.target", "")
	return strings.Join(lines, "\n")
}

func (e *setupEnv) unitDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(e.home, ".config")
	}
	return filepath.Join(base, "systemd", "user")
}

// unitProgram is the program the user's systemd starts for the unit name ("personad.service"): the unit and its
// drop-ins read as systemd reads them (unitPrograms, over the user scope systemServices looks at); "" for none.
func (e *setupEnv) unitProgram(name string) string {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(e.home, ".local", "share")
	}
	scope := unitScope{user: true, dirs: []string{e.unitDir(), "/etc/systemd/user", filepath.Join(data, "systemd", "user"), "/usr/lib/systemd/user"}}
	u, ok := scope.units()[name]
	if !ok {
		return ""
	}
	programs := unitPrograms(u.path, u.dropins, e.home)
	if len(programs) == 0 {
		return ""
	}
	return programs[len(programs)-1]
}

// runningProgram is the program the hub's service runs now, its main process's; "" when it does not run or this
// cannot tell.
func (e *setupEnv) runningProgram(ctx context.Context) string {
	out, err := e.run(ctx, nil, "systemctl", "--user", "show", "-p", "MainPID", "--value", e.compatibleHubName("personad"))
	pid, perr := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || perr != nil || pid <= 0 {
		return ""
	}
	exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(exe, " (deleted)")
}

// compatDropin is the drop-in hub-systemd.sh adds for a unit that differs from its own text, with the unit's
// ExecStart in it.
const compatDropin = "90-persona-compat.conf"

// syncDropin points the ExecStart lines in [Service] of the drop-in file (compatDropin) at execStart, keeping an
// empty `ExecStart=` and every other line; changed says it wrote. Without the file there is nothing to do.
func syncDropin(file, execStart string) (changed bool, err error) {
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(b), "\n")
	section := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			section = trimmed
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if ok && section == "[Service]" && strings.TrimSpace(key) == "ExecStart" && strings.TrimSpace(value) != "" {
			lines[i] = execStart
		}
	}
	text := strings.Join(lines, "\n")
	if text == string(b) {
		return false, nil
	}
	return true, os.WriteFile(file, []byte(text), 0o644)
}

func (e *setupEnv) systemctl(ctx context.Context, args ...string) error {
	for i, name := range args {
		args[i] = e.compatibleHubName(name)
	}
	out, err := e.run(ctx, nil, "systemctl", append([]string{"--user"}, args...)...)
	if err != nil {
		if line := setupFirstLine(string(out)); line != "" {
			return errors.New(line)
		}
	}
	return err
}

func (e *setupEnv) systemdService(ctx context.Context, cfg *Config, bin string) error {
	units, unitErr := e.compatibleHubUnits()
	if unitErr != nil {
		return errors.New(e.errText(unitErr)) // printed as it is (personad's log.Fatal): in e.l
	}
	if !e.userSystemd(ctx) {
		return e.fail("service", i18n.T(e.l, "hub-setup.no_systemd"), i18n.T(e.l, "hub-setup.no_systemd_next", i18n.Args{"bin": bin}))
	}
	dir := e.unitDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return e.fail("service", i18n.T(e.l, "hub-setup.mkdir_failed", i18n.Args{"path": dir, "error": err.Error()}), i18n.T(e.l, "hub-setup.check_permissions"))
	}
	socketChoice, explicitSocket := identity.LookupEnv("PERSONA_HUB_SOCKET")
	useSocket := socketChoice != "0"
	if units.legacy && !explicitSocket {
		_, err := os.Stat(filepath.Join(dir, units.socket))
		useSocket = err == nil
	}
	listens := []string{cfg.Listen}
	if cfg.Public.TLSListen != "" {
		listens = append(listens, cfg.Public.TLSListen)
	}
	servicePath, socketPath := filepath.Join(dir, units.service+".service"), filepath.Join(dir, units.socket)
	serviceBefore, _ := os.ReadFile(servicePath)
	socketBefore, _ := os.ReadFile(socketPath)
	// A unit under the former name keeps its text, and so the program it runs.
	split, splitNote := "", ""
	if !units.legacy {
		split, splitNote = e.splitHub(ctx)
	}
	wantProgram := filepath.Join(e.root, "bin", "personad")
	if split != "" {
		wantProgram = split
	}
	programBefore := e.unitProgram(units.service + ".service")
	running := e.runningProgram(ctx)
	if running != "" {
		programBefore = running
	}
	service := compatibleUnitText(hubServiceUnit(e.root, split != "", useSocket, e.splitOff()), units)
	if units.legacy && len(serviceBefore) > 0 {
		service = string(serviceBefore)
	}
	socket := ""
	if useSocket {
		socket = compatibleUnitText(hubSocketUnit(listens), units)
		if units.legacy && sameSocketAddresses(string(socketBefore), listens) {
			socket = string(socketBefore)
		}
	}
	if err := os.WriteFile(servicePath, []byte(service), 0o644); err != nil {
		return e.fail("service", e.writeFailed(servicePath, err), i18n.T(e.l, "hub-setup.check_permissions"))
	}
	if useSocket {
		if err := os.WriteFile(socketPath, []byte(socket), 0o644); err != nil {
			return e.fail("service", e.writeFailed(socketPath, err), i18n.T(e.l, "hub-setup.check_permissions"))
		}
	} else {
		_ = os.Remove(socketPath)
	}
	changed := string(serviceBefore) != service
	if !units.legacy {
		dropin := filepath.Join(servicePath+".d", compatDropin)
		dropinChanged, err := syncDropin(dropin, hubExecStart(e.root, split != ""))
		if err != nil {
			return e.fail("service", e.writeFailed(dropin, err), i18n.T(e.l, "hub-setup.check_permissions"))
		}
		changed = changed || dropinChanged
	}
	sum, binChanged := binaryChanged(bin)
	socketChanged := string(socketBefore) != socket
	programAfter := e.unitProgram(units.service + ".service")
	// The owner changed the program in a drop-in of their own since the service started.
	runsOther := running != "" && e.ownProgram(programAfter) && resolvedPath(running) != resolvedPath(programAfter)
	steps := [][]string{{"daemon-reload"}, {"enable", "personad"}}
	serviceActive := e.systemctl(ctx, "is-active", "--quiet", "personad") == nil
	socketActive := e.systemctl(ctx, "is-active", "--quiet", e.compatibleHubName("personad.socket")) == nil
	what := i18n.T(e.l, "hub-setup.systemd_installed_socket")
	if !useSocket {
		what = i18n.T(e.l, "hub-setup.systemd_installed")
	}
	restartSaid := false
	switch {
	case useSocket && (!socketActive || socketChanged):
		// First time, or other addresses: a running hub holds the port itself; one stop to hand it to the socket.
		// Both in one stop: the service stopped alone while the socket listens is started again by the next
		// connection, and the socket then cannot start while it runs (hub-systemd.sh, the same steps).
		steps = append(steps, []string{"stop", "personad.socket", "personad"}, []string{"enable", "personad.socket"},
			[]string{"start", "personad.socket"}, []string{"start", "personad"})
	case !useSocket && (socketActive || len(socketBefore) > 0):
		steps = append(steps, []string{"stop", "personad"}, []string{"disable", "--now", "personad.socket"}, []string{"start", "personad"})
	case changed || !serviceActive:
		steps = append(steps, []string{"restart", "personad"})
	case e.restartHub:
		steps = append(steps, []string{"restart", "personad"})
		what = i18n.T(e.l, "hub-setup.service_restarted_hub", i18n.Args{"why": e.restartReason(binChanged)})
		restartSaid = true
	case binChanged:
		// The units are as they were, but the binary is new (an upgrade): the hub running is the old one. The port
		// stays with personad.socket, so the restart is not noticed (docs/operations/hub-restart.md).
		steps = append(steps, []string{"restart", "personad"})
		what = i18n.T(e.l, "hub-setup.service_updated")
		restartSaid = true
	case runsOther:
		steps = append(steps, []string{"restart", "personad"})
		what = i18n.T(e.l, "hub-setup.service_ready")
	default:
		what = i18n.T(e.l, "hub-setup.service_unchanged")
	}
	for _, s := range steps {
		if err := e.systemctl(ctx, s...); err != nil {
			return e.fail("service", i18n.T(e.l, "hub-setup.systemctl_failed", i18n.Args{"args": strings.Join(s, " "), "error": err.Error()}),
				i18n.T(e.l, "hub-setup.journal_next"))
		}
	}
	noteBinary(bin, sum)
	e.hubRestart = func(ctx context.Context) error { return e.systemctl(ctx, "restart", "personad") }
	if e.ownProgram(programAfter) {
		what += switchNote(e.l, programBefore, programAfter, serviceActive && !restartSaid, serviceActive && cfg.Assistant.StartsBuiltin())
	}
	if splitNote != "" {
		what += e.sep() + i18n.T(e.l, "hub-setup.split_skipped", i18n.Args{"why": splitNote})
	}
	detail, next := what, e.linger(ctx)
	if next != "" {
		detail += e.sep() + i18n.T(e.l, "hub-setup.no_linger")
	}
	if !units.legacy && programAfter != "" && resolvedPath(programAfter) != resolvedPath(wantProgram) {
		// Another drop-in (the owner's) sets ExecStart: the service runs what it says, which setup leaves alone.
		detail += e.sep() + i18n.T(e.l, "hub-setup.dropin_program", i18n.Args{"program": programAfter})
		fix := i18n.T(e.l, "hub-setup.dropin_fix", i18n.Args{"program": wantProgram})
		if next == "" {
			next = fix
		} else {
			next += e.sep() + fix
		}
	}
	e.report("service", true, detail, next)
	return nil
}

// linger keeps the user's services running after they log out (and starts them at boot): systemd user services stop
// with the last session otherwise. It may take sudo; that is asked once, saying why. Returns what is left to do.
func (e *setupEnv) linger(ctx context.Context) string {
	manual := i18n.T(e.l, "hub-setup.linger_manual", i18n.Args{"user": e.user})
	if _, err := e.lookPath("loginctl"); err != nil || e.user == "" {
		return ""
	}
	if out, err := e.run(ctx, nil, "loginctl", "show-user", e.user, "--property=Linger", "--value"); err == nil && strings.TrimSpace(string(out)) == "yes" {
		return ""
	}
	if _, err := e.run(ctx, nil, "loginctl", "enable-linger", e.user); err == nil {
		return ""
	}
	yes := e.o.yes
	if !yes {
		var asked bool
		yes, asked = e.ask(i18n.T(e.l, "hub-setup.linger_ask"))
		if !asked {
			return manual
		}
	}
	if !yes {
		return manual
	}
	if err := e.runTTY(ctx, "sudo", "loginctl", "enable-linger", e.user); err != nil {
		return manual
	}
	return ""
}

// --- launchd ------------------------------------------------------------------------------------------------------

func (e *setupEnv) plistPath() string {
	return filepath.Join(e.home, "Library", "LaunchAgents", hubLaunchdLabel+".plist")
}

// hubPlist is the hub's launchd agent: it runs `<split> run` (persona-hub, splitHub) when split is set, else
// `<bin> hub` (<root>/bin/personad), which self-update finds either way; splitOff keeps update.SplitEnv 0 in its
// environment, as the Linux unit does.
func hubPlist(home, root, bin, split string, splitOff bool) string {
	x := html.EscapeString
	program, arg := bin, "hub"
	if split != "" {
		program, arg = split, "run"
	}
	keep := ""
	if splitOff {
		keep = "    <key>" + update.SplitEnv + "</key><string>0</string>\n"
	}
	log := filepath.Join(root, "hub.log")
	path := strings.Join([]string{filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin"), filepath.Join(home, ".orbstack", "bin"),
		"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":")
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + hubLaunchdLabel + `</string>
  <key>ProgramArguments</key><array><string>` + x(program) + `</string><string>` + arg + `</string></array>
  <key>EnvironmentVariables</key><dict>
    <key>PERSONA_HOME</key><string>` + x(root) + `</string>
    <key>PATH</key><string>` + x(path) + `</string>
` + keep + `  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>3</integer>
  <key>StandardOutPath</key><string>` + x(log) + `</string>
  <key>StandardErrorPath</key><string>` + x(log) + `</string>
</dict></plist>
`
}

// otherHubAgent is the label of a launchd agent someone set up before that already runs this hub ("" when none):
// left as it is, like hub-systemd.sh does. Matched the way that script matches: the job runs "<personad> hub", or
// this root's persona-hub with no arguments, "run" or "hub" (launchdJob), the binary resolved through symlinks and ~,
// so an agent naming ~/.persona/bin/personad is found when ~/.persona and ~/.persona are the same directory.
func (e *setupEnv) otherHubAgent(bin string) string {
	personad, split := resolvedPath(bin), resolvedPath(filepath.Join(e.root, "bin", "persona-hub"))
	files, _ := filepath.Glob(filepath.Join(e.home, "Library", "LaunchAgents", "*.plist"))
	sort.Strings(files)
	for _, f := range files {
		if filepath.Base(f) == hubLaunchdLabel+".plist" {
			continue
		}
		job, ok := readLaunchdJob(f)
		if !ok || len(job.args) == 0 {
			continue
		}
		label, prog := job.label, resolvedPath(homePath(job.args[0], e.home))
		switch {
		case prog == personad && len(job.args) >= 2 && job.args[1] == "hub":
		case prog == split && (len(job.args) == 1 || job.args[1] == "run" || job.args[1] == "hub"):
		default:
			continue
		}
		if label == "" {
			label = strings.TrimSuffix(filepath.Base(f), ".plist")
		}
		return label
	}
	return ""
}

// resolvedPath is p with symlinks resolved, or just cleaned when it does not exist.
func resolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

type plistNode struct {
	XMLName xml.Name
	Text    string      `xml:",chardata"`
	Nodes   []plistNode `xml:",any"`
}

// plistJob is what a launchd property list runs: args[0] is the program and the rest its arguments, and disabled says
// launchd leaves the job unloaded.
type plistJob struct {
	label    string
	args     []string
	disabled bool
}

// launchdJob reads an XML property list's job. launchd runs Program when there is one, with ProgramArguments as its
// argv (ProgramArguments[0] is only the name it is given), else ProgramArguments[0].
func launchdJob(b []byte) (job plistJob, ok bool) {
	var root plistNode
	if err := xml.Unmarshal(b, &root); err != nil || root.XMLName.Local != "plist" || len(root.Nodes) == 0 || root.Nodes[0].XMLName.Local != "dict" {
		return plistJob{}, false
	}
	var program string
	kv := root.Nodes[0].Nodes
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i].XMLName.Local != "key" {
			return plistJob{}, false
		}
		v := kv[i+1]
		switch strings.TrimSpace(kv[i].Text) {
		case "Label":
			job.label = strings.TrimSpace(v.Text)
		case "Program":
			program = strings.TrimSpace(v.Text)
		case "ProgramArguments":
			for _, a := range v.Nodes {
				job.args = append(job.args, a.Text)
			}
		case "Disabled":
			job.disabled = v.XMLName.Local == "true"
		}
	}
	if program != "" {
		if len(job.args) == 0 {
			job.args = []string{program}
		} else {
			job.args[0] = program
		}
	}
	return job, true
}

func (e *setupEnv) agentPlist(label string) string {
	return filepath.Join(e.home, "Library", "LaunchAgents", label+".plist")
}

// restartReason says why setup restarts a running hub: a new binary, what config changed, or both.
func (e *setupEnv) restartReason(binChanged bool) string {
	why := e.restartWhy
	if binChanged {
		why = append([]string{i18n.T(e.l, "hub-setup.restart_why_binary")}, why...)
	}
	return strings.Join(why, i18n.T(e.l, "hub-setup.list_separator"))
}

// relaunch is a launchd agent's restart, for undoing a switch to the built-in manager's own service.
func (e *setupEnv) relaunch(target string) func(context.Context) error {
	return func(ctx context.Context) error {
		if out, err := e.run(ctx, nil, "launchctl", "kickstart", "-k", target); err != nil {
			return fmt.Errorf("%v: %s", err, setupFirstLine(string(out)))
		}
		return nil
	}
}

// kickstart restarts a loaded launchd agent (hub-systemd.sh's way on a Mac).
func (e *setupEnv) kickstart(ctx context.Context, target string) error {
	if out, err := e.run(ctx, nil, "launchctl", "kickstart", "-k", target); err != nil {
		return e.fail("service", i18n.T(e.l, "hub-setup.launchd_restart_failed", i18n.Args{"target": target, "error": setupFirstLine(string(out))}),
			i18n.T(e.l, "hub-setup.launchctl_print_next", i18n.Args{"target": target}))
	}
	return nil
}

func (e *setupEnv) launchdService(ctx context.Context, cfg *Config, bin string) error {
	domain := "gui/" + strconv.Itoa(e.uid)
	sum, binChanged := binaryChanged(bin)
	if label := e.otherHubAgent(bin); label != "" {
		detail := i18n.T(e.l, "hub-setup.launchd_existing", i18n.Args{"label": label})
		if _, err := e.run(ctx, nil, "launchctl", "print", domain+"/"+label); err != nil {
			if out, err := e.run(ctx, nil, "launchctl", "bootstrap", domain, e.agentPlist(label)); err != nil {
				return e.fail("service", i18n.T(e.l, "hub-setup.launchd_start_failed", i18n.Args{"label": label, "error": setupFirstLine(string(out))}),
					i18n.T(e.l, "hub-setup.launchctl_print_next", i18n.Args{"target": domain + "/" + label}))
			}
		} else if binChanged || e.restartHub {
			if err := e.kickstart(ctx, domain+"/"+label); err != nil {
				return err
			}
			detail += e.sep() + i18n.T(e.l, "hub-setup.restarted_it", i18n.Args{"why": e.restartReason(binChanged)})
		}
		e.hubRestart = e.relaunch(domain + "/" + label)
		noteBinary(bin, sum)
		e.report("service", true, detail, "")
		return nil
	}
	file := e.plistPath()
	before, beforeErr := os.ReadFile(file)
	programBefore := ""
	if job, ok := readLaunchdJob(file); ok && len(job.args) > 0 {
		programBefore = homePath(job.args[0], e.home)
	}
	// The program, as on Linux (splitHub), but for an agent that ran personad before (an installed Mac): it stays on
	// personad (macStaysNote). One that runs it for the opt-out keeps the variable, and so is not that.
	split, splitNote, macNote := "", "", ""
	switch {
	case e.splitOff():
	case beforeErr == nil && e.runsPersonad(programBefore) && !strings.Contains(string(before), "<key>"+update.SplitEnv+"</key>"):
		if fi, err := os.Stat(filepath.Join(e.root, "bin", "persona-hub")); err == nil && !fi.IsDir() {
			macNote = macStaysNote(e.l)
		}
	default:
		split, splitNote = e.splitHub(ctx)
	}
	if splitNote != "" {
		macNote = e.sep() + i18n.T(e.l, "hub-setup.split_skipped", i18n.Args{"why": splitNote})
	}
	programAfter := bin
	if split != "" {
		programAfter = split
	}
	want := hubPlist(e.home, e.root, bin, split, e.splitOff())
	_, loadedErr := e.run(ctx, nil, "launchctl", "print", domain+"/"+hubLaunchdLabel)
	e.hubRestart = e.relaunch(domain + "/" + hubLaunchdLabel)
	if string(before) == want && loadedErr == nil {
		detail := i18n.T(e.l, "hub-setup.launchd_unchanged", i18n.Args{"label": hubLaunchdLabel})
		if binChanged || e.restartHub {
			if err := e.kickstart(ctx, domain+"/"+hubLaunchdLabel); err != nil {
				return err
			}
			detail = i18n.T(e.l, "hub-setup.launchd_restarted", i18n.Args{"why": e.restartReason(binChanged), "label": hubLaunchdLabel})
		}
		noteBinary(bin, sum)
		e.report("service", true, detail+macNote, "")
		return nil
	}
	if os.IsNotExist(beforeErr) && e.hubAnswers(ctx, cfg) {
		// A hub with this token runs already, started some other way (a terminal, an agent setup cannot tell runs
		// it): a second agent could not get the port and would restart forever. Leave it to the user.
		e.hubRestart = nil // not restarted here: it runs with the hub.json it read when it started
		e.report("service", true, i18n.T(e.l, "hub-setup.launchd_other_hub"), i18n.T(e.l, "hub-setup.launchd_other_hub_next"))
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return e.fail("service", i18n.T(e.l, "hub-setup.mkdir_failed", i18n.Args{"path": filepath.Dir(file), "error": err.Error()}), i18n.T(e.l, "hub-setup.check_permissions"))
	}
	if err := os.WriteFile(file, []byte(want), 0o644); err != nil {
		return e.fail("service", e.writeFailed(file, err), i18n.T(e.l, "hub-setup.check_permissions"))
	}
	_, _ = e.run(ctx, nil, "launchctl", "bootout", domain+"/"+hubLaunchdLabel)
	if out, err := e.run(ctx, nil, "launchctl", "bootstrap", domain, file); err != nil {
		return e.fail("service", i18n.T(e.l, "hub-setup.launchd_bootstrap_failed", i18n.Args{"error": setupFirstLine(string(out))}),
			i18n.T(e.l, "hub-setup.launchctl_print_next", i18n.Args{"target": domain + "/" + hubLaunchdLabel}))
	}
	noteBinary(bin, sum)
	detail := i18n.T(e.l, "hub-setup.launchd_installed", i18n.Args{"label": hubLaunchdLabel, "path": filepath.Join(e.root, "hub.log")})
	if beforeErr == nil {
		if moved := switchNote(e.l, programBefore, programAfter, loadedErr == nil, loadedErr == nil && cfg.Assistant.StartsBuiltin()); moved != "" {
			detail += moved + macSwitchNote(e.l, programAfter)
		}
	}
	e.report("service", true, detail+macNote, "")
	return nil
}
