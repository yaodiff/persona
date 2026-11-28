package devices

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update"
)

// Redeploying a node from the hub (POST /api/v1/machines/{id}/deploy, the button where a machine is not the hub's
// build): deploy.sh leaves every node binary of the build it deployed on the hub machine
// (~/.persona/bin/nodes/personad-<os>-<arch>, and persona-node-<os>-<arch> with its .version), and the manager fetches
// the release's where the hub has none (packages.go), so the hub sends a machine the build of its platform and kind of
// program over ssh, kills the resident node
// if there is one (launchd starts it again from the new binary; `launchctl kickstart` from an ssh session did not always
// take, which is how two Macs kept an old node through deploys), and connects again.

func nodeBinaryDir() string { return identity.NodeBinaryDir() }

// isPersonaNodeBuild: bin is a persona-node build (packages.go picked it: the node binary dir's, or the release's in
// the cache), which the deploy script never puts where personad is.
func isPersonaNodeBuild(bin string) bool {
	return strings.HasPrefix(filepath.Base(bin), nodeproto.ProgramPersonaNode+"-")
}

// deviceNode identifies a device's own executable path for SSH deployment, including paths that hold personad
// from an earlier deployment.
func deviceNode(info proto.NodeInfo) bool {
	return info.Program == nodeproto.ProgramPersonaNode || (info.Executable != "" && update.IsDeviceFile(info.Executable))
}

// remoteTarget is the file an ssh deploy of bin replaces on the machine: the device's own file for a node that runs
// from it (whichever build goes there), never personad's for a persona-node build, else remote_bin.
func remoteTarget(cfg MachineConfig, info proto.NodeInfo, bin string) string {
	if deviceNode(info) && info.Executable != "" {
		return info.Executable
	}
	if isPersonaNodeBuild(bin) {
		return "~/.persona/bin/" + nodeproto.ProgramPersonaNode
	}
	if cfg.RemoteBin != "" {
		return cfg.RemoteBin
	}
	return defaultRemoteBin
}

// gzipPipe is r gzipped as it is read, for an ssh's stdin. The returned cleanup closes the pipe and waits for the
// writer goroutine to return: exec copies a non-*os.File Stdin in its own goroutine and never closes it, so once ssh
// exits early (auth failure, connect timeout, the remote script failing, ctx done) nothing reads it, and the gzip
// writer would block for good (#2377). Closing it makes that Write fail with io.ErrClosedPipe so it returns before the
// caller closes r.
func gzipPipe(r io.Reader) (*io.PipeReader, func()) {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		zw, _ := gzip.NewWriterLevel(pw, gzip.BestSpeed)
		_, err := io.Copy(zw, r)
		if err == nil {
			err = zw.Close()
		}
		pw.CloseWithError(err)
	}()
	return pr, func() {
		_ = pr.Close()
		<-done
	}
}

// validateSSHHost rejects hosts that could be parsed as ssh options, ssh:// URIs, or inject shell/ProxyCommand
// characters across OpenSSH versions (including CVE-2023-51385).
func validateSSHHost(host string) error {
	if host == "" {
		return errors.New("empty ssh host")
	}
	const badChars = "/`$;|&'\"\\()<>*?"
	for _, r := range host {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(badChars, r) {
			return fmt.Errorf("invalid ssh host %q: contains forbidden character", host)
		}
	}
	hostPart := host
	if user, rest, hasAt := strings.Cut(host, "@"); hasAt {
		if user == "" || rest == "" || strings.ContainsRune(rest, '@') {
			return fmt.Errorf("invalid ssh host %q: malformed user@host", host)
		}
		if strings.HasPrefix(user, "-") || strings.ContainsRune(user, '%') {
			return fmt.Errorf("invalid ssh host %q: invalid username", host)
		}
		hostPart = rest
	}
	if strings.HasPrefix(hostPart, "-") {
		return fmt.Errorf("invalid ssh host %q: must not start with '-'", host)
	}
	if strings.HasPrefix(hostPart, "[") || strings.HasSuffix(hostPart, "]") {
		if !strings.HasPrefix(hostPart, "[") || !strings.HasSuffix(hostPart, "]") {
			return fmt.Errorf("invalid ssh host %q: unmatched bracket", host)
		}
		inner := hostPart[1 : len(hostPart)-1]
		if strings.ContainsAny(inner, "[]") || !validIPv6Literal(inner) {
			return fmt.Errorf("invalid ssh host %q: invalid bracketed IPv6 host", host)
		}
		return nil
	}
	if strings.ContainsAny(hostPart, "[]") {
		return fmt.Errorf("invalid ssh host %q: unexpected bracket", host)
	}
	if strings.ContainsRune(hostPart, '%') && !validIPv6Literal(hostPart) {
		return fmt.Errorf("invalid ssh host %q: forbidden '%%' outside IPv6 zone", host)
	}
	return nil
}

func validIPv6Literal(s string) bool {
	addr, err := netip.ParseAddr(s)
	if err != nil || !addr.Is6() {
		return false
	}
	if z := addr.Zone(); z != "" {
		if strings.HasPrefix(z, "-") {
			return false
		}
		for _, r := range z {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
				return false
			}
		}
	}
	return true
}

// validateRemotePath rejects empty, option-like, directory-like, or multi-line/control remote binary paths
// (anyShellQuote requires one line because csh rejects newlines inside quotes, and remoteTarget may come from
// node-reported NodeInfo.Executable).
func validateRemotePath(remote string) error {
	if remote == "" {
		return errors.New("empty remote target path")
	}
	if strings.HasPrefix(remote, "-") {
		return fmt.Errorf("invalid remote target path %q: must not start with '-'", remote)
	}
	if strings.HasSuffix(remote, "/") {
		return fmt.Errorf("invalid remote target path %q: must name a file", remote)
	}
	for _, r := range remote {
		if r < 0x20 || r == 0x7f || unicode.IsControl(r) {
			return fmt.Errorf("invalid remote target path %q: contains control character", remote)
		}
	}
	if base := path.Base(remote); base == "." || base == ".." {
		return fmt.Errorf("invalid remote target path %q: invalid file name %q", remote, base)
	}
	return nil
}

// errFallbackOnRedeploy: an update the manager makes by itself found a fallback build (the newest release, another
// version than the hub's), which only an explicit Redeploy sends.
var errFallbackOnRedeploy = errors.New("a fallback build is sent only on Redeploy")

var errAutoNodeUpdatesDisabled = errors.New("automatic node upgrades are disabled")

// deployRun is one deployment's private completion signal. Its error is written before done closes and never
// overwritten by a later deployment.
type deployRun struct {
	done chan struct{}
	err  error
}

// Redeploy starts the same explicit deployment as the app's Redeploy route and waits for its outcome. Canceling the
// caller stops only the wait: the deployment keeps its own manager context and machine-card state.
func (d *Devices) Redeploy(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := d.machineByID(id)
	if m == nil {
		return errUnknownMachine
	}
	run, err := m.startDeployOf(false)
	if err != nil {
		return err
	}
	return m.waitDeploy(ctx, run)
}

func (m *Machine) waitDeploy(ctx context.Context, run *deployRun) error {
	if run == nil {
		return errors.New("redeploy is running but its result cannot be awaited")
	}
	select {
	case <-run.done:
		return run.err
	case <-ctx.Done():
		select {
		case <-run.done:
			return run.err
		default:
		}
		return fmt.Errorf("redeploy is still running; its result is not confirmed: %w", ctx.Err())
	}
}

// beginDeployLocked starts one outcome wait for every caller that joins this deployment.
func (m *Machine) beginDeployLocked(phase string) *deployRun {
	m.deploying, m.deployErr, m.deployPhase = true, "", phase
	m.deploySource, m.deployVersion = "", ""
	m.deployRun = &deployRun{done: make(chan struct{})}
	return m.deployRun
}

func (m *Machine) endDeployLocked(err error) {
	m.deploying, m.deployPhase = false, ""
	if m.deployRun != nil {
		m.deployRun.err = err
		close(m.deployRun.done)
		m.deployRun = nil
	}
}

// startDeploy checks a deploy can start and runs it in the background; the machine list shows its progress. It is the
// explicit Redeploy: any build the node's platform resolves to goes, a fallback too.
func (m *Machine) startDeploy() error {
	_, err := m.startDeployOf(false)
	return err
}

// startAutoDeploy is startDeploy for the updates the manager makes by itself (a machine back on another build, the
// update after the hub's): only a build of the hub's own version goes, the hub's or its release's; a fallback build
// is left to Redeploy (errFallbackOnRedeploy), as nobody chose to run another version than the hub's.
func (m *Machine) startAutoDeploy() error {
	_, err := m.startDeployOf(true)
	return err
}

func (m *Machine) startDeployOf(auto bool) (*deployRun, error) {
	if auto && !m.d.config().AutoUpdateNodes {
		return nil, errAutoNodeUpdatesDisabled
	}
	if m.d.linkSwitchUnfinished(m.cfg.ID) { // the switch replaces the node's connection and may install its service
		return nil, errLinkSwitchDeploy
	}
	m.mu.Lock()
	if m.deploying {
		run := m.deployRun
		m.mu.Unlock()
		return run, nil
	}
	blocked := m.canDeployLocked()
	info, linked := m.info, m.rpcDeploymentLocked()
	m.mu.Unlock()
	if blocked != nil {
		return nil, blocked
	}
	// The build for its platform and kind of program (packages.go): looked up afresh, a release's manifest at most.
	// lookupShared enforces packageLookup internally so a site timeout still runs offline fallbacks and caches backoff.
	pkg, err := m.d.nodePackageForInfo(m.d.deployContext(), info)
	m.d.changed() // what the machine should run may be another build now
	if err != nil {
		return nil, err
	}
	if auto {
		cfg := m.d.config()
		if !cfg.AutoUpdateNodes {
			return nil, errAutoNodeUpdatesDisabled
		}
		if pkg.Fallback || pkg.Version != cfg.HubVersion {
			return nil, errFallbackOnRedeploy
		}
	}
	// A temporary local node takes the manager's build when spawned. For a
	// resident, never put a stand-in release into a shared local executable.
	if m.cfg.Kind == "local" && (pkg.Fallback || pkg.Version != m.d.hubVersion()) {
		return nil, i18n.Err("hub-machine.deploy_local")
	}
	if linked {
		return m.startRPCDeploymentRun(pkg, nil)
	}
	m.mu.Lock()
	if m.deploying {
		run := m.deployRun
		m.mu.Unlock()
		return run, nil
	}
	run := m.beginDeployLocked("transferring")
	m.mu.Unlock()
	m.d.changed()
	target := pkg.Version
	go func() {
		ctx := m.d.deployContext()
		bin, err := m.fetchDeployment(ctx, pkg) // a release's: downloaded and checked first (still "transferring")
		if err == nil {
			err = m.d.CheckDevelopmentTarget(ctx, pkg.development)
		}
		if err == nil {
			err = m.deploy(ctx, bin, target)
		}
		m.mu.Lock()
		m.endDeployLocked(err)
		if err != nil {
			m.deployErr = err.Error()
		}
		m.mu.Unlock()
		if err != nil {
			m.d.logf("[%s] deploy: %v", m.cfg.ID, err)
		} else {
			m.d.logf("[%s] deployed build %s", m.cfg.ID, target)
		}
		m.saveDeployOutcome(ctx, target, err)
		m.d.changed()
	}()
	return run, nil
}

// deployExitWrapper is the deploy script's exit status when bin is a script (remote_bin a wrapper that sets the node's
// environment, PERSONA_HOME say, and execs personad): replacing it would move the node to another data directory and
// machine uid, so nothing is replaced and the owner updates the binary the wrapper runs.
const deployExitWrapper = 5

// deployScript installs the gzipped binary on stdin as bin and asks a
// legacy resident service using it to reload once idle.
// The staged binary must run `version` before it can replace the installed one.
// An installed file that is a script is never replaced (deployExitWrapper); one that is replaced is kept as bin.prev.
// personaNode: the binary is persona-node, which never replaces a file that is not persona-node already (a personad,
// which may run a hub or a manager too, put there since the node said what it runs). The file says which it is in
// node-update-check; a build that does not say is personad.
func deployScript(bin string, personaNode bool) string {
	target := shellQuote(bin)
	if rest, ok := strings.CutPrefix(bin, "~/"); ok {
		target = `"$HOME"/` + shellQuote(rest)
	}
	keep := ""
	if personaNode {
		keep = `if [ -e "$b" ] && ! "$b" node-update-check 2>/dev/null | grep -q '"program":"persona-node"'; then rm -f "$b.new"; ` +
			`echo "$b is personad, which may run a hub or a manager too: not replacing it with persona-node" >&2; exit 3; fi; `
	}
	legacy := shellQuote(compat.DaemonBinary)
	return posixCommand("set -e; b=" + target + `; mkdir -p -- "$(dirname -- "$b")"; d=$(cd -P -- "$(dirname -- "$b")" && pwd); b="$d/${b##*/}"; ` +
		`if [ -L "$b" ]; then case "${b##*/}:$(readlink "$b")" in ` + legacy + `:personad|` + legacy + `:"$d/personad") b="$d/personad";; *) exit ` +
		strconv.Itoa(deployExitWrapper) + `;; esac; fi; gzip -dc > "$b.new"; chmod 755 "$b.new"; ` + keep +
		`if ! "$b.new" version >/dev/null; then rm -f "$b.new"; echo "$b failed its staged version check: keeping the current binary and node" >&2; exit 4; fi; ` +
		`if [ -f "$b" ] && [ "$(head -c 2 "$b")" = '#!' ]; then rm -f "$b.new"; ` +
		`echo "$b is a script, not personad: not replacing it (update the personad it runs instead)" >&2; exit ` + strconv.Itoa(deployExitWrapper) + `; fi; ` +
		`if [ -f "$b" ]; then ln -f "$b" "$b.prev" 2>/dev/null || cp -p "$b" "$b.prev"; fi; ` +
		`mv "$b.new" "$b"; ` + reloadLegacyResident)
}

// Capable nodes use update RPC directly. Older installations check their
// managed services and signal only the main process using the replaced image.
// Must remain a single line because deployScript wraps it with anyShellQuote
// and csh/tcsh reject newlines inside quotes.
const reloadLegacyResident = `if [ -d /proc ] && command -v systemctl >/dev/null; then ` +
	`for unit in personad-node.service persona-node.service ` + compat.HubService + `-node.service; do ` +
	`pid=$(systemctl --user show --property=MainPID --value "$unit" 2>/dev/null) || continue; ` +
	`case "$pid" in ''|*[!0-9]*) continue;; esac; ` +
	`[ "$pid" -gt 1 ] || continue; ` +
	`exe=$(readlink "/proc/$pid/exe" 2>/dev/null) || continue; ` +
	`[ "$exe" = "$b (deleted)" ] || continue; ` +
	`awk '$1 == "SigCgt:" && $2 ~ /[13579bdf]$/ { ok=1 } END { exit !ok }' "/proc/$pid/status" 2>/dev/null || continue; ` +
	`kill -HUP "$pid" 2>/dev/null || true; ` +
	`break; ` +
	`done; ` +
	`elif command -v launchctl >/dev/null; then ` +
	`for label in com.persona.node ` + compat.NodeLabel + `; do ` +
	`job=$(launchctl print "gui/$(id -u)/$label" 2>/dev/null) || continue; ` +
	`exe=$(printf '%s\n' "$job" | sed -n 's/^[[:space:]]*program = //p'); ` +
	`[ "$exe" = "$b" ] || { [ "$exe" = "$d/` + compat.DaemonBinary + `" ] && [ "$b" = "$d/personad" ]; } || continue; ` +
	`pid=$(printf '%s\n' "$job" | sed -n 's/^[[:space:]]*pid = //p'); ` +
	`case "$pid" in ''|*[!0-9]*) continue;; esac; ` +
	`[ "$pid" -gt 1 ] || continue; ` +
	`kill -HUP "$pid" 2>/dev/null || true; ` +
	`break; ` +
	`done; ` +
	`fi`

// shellQuote quotes s for a POSIX shell only (the script inside posixCommand); a remote command goes through
// anyShellQuote.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// posixCommand is a remote command that runs a POSIX sh script on any machine. sshd hands the remote command to the
// user's login shell, which is not always a POSIX shell: on a machine whose login shell is fish the script failed at
// its first assignment and nothing was installed (#1770). `sh -c <script>` reads the same in sh, bash, zsh, fish and
// csh once the script is quoted by anyShellQuote; sh then runs the script itself.
func posixCommand(script string) string { return "sh -c " + anyShellQuote(script) }

// anyShellQuote quotes s as one word that POSIX shells, fish and csh all read back as s. Inside single quotes fish
// still takes \\ and \' as escapes and csh still expands !history (POSIX shells take all three literally), so a quote,
// a backslash or a ! is put outside the quotes, backslash-escaped, which every one of them reads alike. s must be one
// line: csh rejects a newline inside quotes.
func anyShellQuote(s string) string {
	return "'" + strings.NewReplacer(`'`, `'\''`, `\`, `'\\'`, `!`, `'\!'`).Replace(s) + "'"
}

// deploy installs bin, the build target, on the machine and waits for its node to come back running it.
func (m *Machine) deploy(ctx context.Context, bin, target string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute) // a slow link: about 8 MB gzipped
	defer cancel()
	m.mu.Lock()
	via := m.via
	m.mu.Unlock()
	if via == "tailcat" { // no ssh to it now: the node takes the binary over tailcat itself
		if err := m.deployTailcat(ctx, bin); err != nil {
			return explain(i18n.Err("devices-deploy.install_failed", i18n.Args{"err": err.Error()}), err)
		}
		// The node exits about a second after answering: reconnecting before that would land on the old one.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
		return m.waitDeployed(ctx, target)
	}
	m.mu.Lock()
	remote := remoteTarget(m.cfg, m.info, bin)
	m.mu.Unlock()
	if err := validateRemotePath(remote); err != nil {
		return explain(i18n.Err("devices-deploy.install_failed", i18n.Args{"err": err.Error()}), err)
	}
	host := m.cfg.SSHHost
	if host == "" {
		host = m.cfg.ID
	}
	if err := validateSSHHost(host); err != nil {
		return explain(i18n.Err("devices-deploy.install_failed", i18n.Args{"err": err.Error()}), err)
	}
	f, err := os.Open(bin)
	if err != nil {
		return err
	}
	defer f.Close()
	// The guard keeps persona-node off a file that is not the device's own (a personad installed since the node said
	// what it runs); the device's file takes either program.
	guard := isPersonaNodeBuild(bin) && !update.IsDeviceFile(remote)
	cmd := exec.CommandContext(ctx, "ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "--", host, deployScript(remote, guard))
	pr, closePipe := gzipPipe(f)
	defer closePipe()
	cmd.Stdin = pr
	if out, err := cmd.CombinedOutput(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == deployExitWrapper {
			return i18n.Err("devices-deploy.wrapper", i18n.Args{"path": remote})
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return i18n.Err("devices-deploy.install_failed", i18n.Args{"err": msg})
	}
	return m.waitDeployed(ctx, target)
}

// deployWait bounds how long waitDeployed waits for the machine to come back (a variable for tests): the new node
// answers within a few seconds, a resident one waits for launchd's restart (ThrottleInterval 10 s), and over tailcat
// the new node first brings its endpoint up again.
var deployWait, deployPoll = 2 * time.Minute, 2 * time.Second

// waitText is how long a wait was, in the owner's words.
func waitText(l i18n.Lang, d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return i18n.T(l, "devices-deploy.minutes", i18n.Args{"count": int(d / time.Minute)})
	}
	return i18n.T(l, "devices-deploy.seconds", i18n.Args{"count": int(d.Round(time.Second) / time.Second)})
}

// waitDeployed reconnects and waits for the machine to come back on the hub's build.
func (m *Machine) waitDeployed(ctx context.Context, target string) error {
	start := time.Now()
	m.reconnect()
	deadline := start.Add(deployWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(deployPoll):
		}
		m.mu.Lock()
		online, version := m.online, m.info.Version
		m.mu.Unlock()
		if online && !buildDiffers(target, version) {
			return nil
		}
	}
	m.mu.Lock()
	online, version, why := m.online, m.info.Version, m.err
	if m.dialErrAt.Before(start) {
		why = "" // how the old connection ended (our own reconnect), not why the new node is not reached
	}
	m.mu.Unlock()
	if online && !buildDiffers(target, version) {
		return nil // came back just now
	}
	if !online {
		// Not connected: what it runs is not known (m.info is still the node from before the deploy), so saying it
		// "still runs" that build sent the owner to restart a node that may not start at all (#3393).
		l := i18n.Default // Keep stored errors canonical; the hub translates at response time.
		if why != "" {
			return i18n.Err("devices-deploy.not_back_why", i18n.Args{"wait": waitText(l, deployWait), "why": why})
		}
		return i18n.Err("devices-deploy.not_back", i18n.Args{"wait": waitText(l, deployWait)})
	}
	return i18n.Err("devices-deploy.still_old", i18n.Args{"version": version})
}

// updateNodes redeploys each connected machine whose node is not the hub's build, one at a time, and queues the
// update of an offline one reached over its link or connector (devices.update_nodes: the hub updated itself and put
// the release's node binaries where "Redeploy" takes them).
func (d *Devices) updateNodes(ctx context.Context) {
	if !d.config().AutoUpdateNodes {
		return
	}
	for _, m := range d.machinesNow() {
		m.mu.Lock()
		temporary := m.cfg.Kind == "local" && !m.residentUpdateLocked()
		online, info := m.online, m.info
		offlineLinked := !online && !temporary && m.rpcDeploymentLocked()
		m.mu.Unlock()
		if temporary {
			continue
		}
		if offlineLinked {
			if err := m.queueOfflineNodeUpdate(ctx); err != nil {
				d.logf("update: [%s] queue node update: %v", m.cfg.ID, err)
			}
			continue
		}
		if !online || !buildDiffers(d.hubVersion(), info.Version) {
			continue
		}
		// An explicit package check must look beyond a cached fallback. A
		// private publisher may have activated the exact build since then.
		if err := m.startAutoDeploy(); err != nil {
			d.logf("update: [%s] %v", m.cfg.ID, err)
			continue
		}
		for i := 0; i < 500; i++ { // deploy gives up after 15 minutes
			m.mu.Lock()
			busy := m.deploying
			m.mu.Unlock()
			if !busy {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}

// updateOnConnect gives a machine that comes back on another node build the hub's own (conn.go serve has just
// connected it). Only a hub that updates itself asks the manager to redeploy (selfupdate.go confirm ->
// devices.update_nodes); scripts/deploy/deploy.sh writes the hub's binaries where Redeploy takes them and restarts
// the hub, which is a plain start, so a machine that was asleep then, or that it could not reach, kept the old
// node: serve only logged the mismatch and the machine showed the version mismatch until someone pressed Redeploy.
//
// One attempt per machine and build: its outcome is kept (node.deploy.<id>, so it is known again after a hub
// restart) and a build that failed (the machine unreachable, a node that cannot run it, one that rolled back after
// a failed start) is not sent again on every reconnect. Redeploy still tries. When that record cannot be read it
// does not deploy either: it could not record the outcome, so a flapping machine would get a transfer per reconnect.
func (m *Machine) updateOnConnect(ctx context.Context) {
	if !m.d.config().AutoUpdateNodes {
		return
	}
	m.mu.Lock()
	online, deploying, info := m.online, m.deploying, m.info
	temporary := m.cfg.Kind == "local" && !m.residentUpdateLocked()
	m.mu.Unlock()
	if temporary || m.d.hubVersion() == "" || !online || deploying {
		return
	}
	if m.d.linkSwitchUnfinished(m.cfg.ID) { // nothing recorded: the next connection after the switch looks again
		return
	}
	// The hub's build, or the release's its platform resolves to. Not known while the release site is away: nothing is
	// sent or recorded then (a queued update waits too), and the next connection looks again.
	pkg, known := m.d.resolvedPackageKnown(ctx, info)
	if !known {
		m.d.logf("[%s] node %s: which build it should run is not known now (the release site does not answer)", m.cfg.ID, info.Version)
		return
	}
	version := pkg.Version
	if !nodeMismatch(version, info) {
		return
	}
	if pkg.Fallback {
		// Another version than the hub's: offered on the machine's card, sent only when someone chooses Redeploy.
		m.d.logf("[%s] node %s is not build %s, a fallback (not the hub's version): sent only on Redeploy", m.cfg.ID, info.Version, version)
		return
	}
	if m.d.opts.Store != nil {
		var record nodeDeployRecord
		found, err := m.d.opts.Store.Setting(ctx, m.deployRecordKey(), &record)
		switch {
		case err != nil:
			m.d.logf("[%s] read the last node update: %v", m.cfg.ID, err)
			return
		case found && record.Target == version:
			return // this build was tried already: a queued one continues in nodeUpdateConnected, a failed one waits for Redeploy
		}
	}
	m.d.logf("[%s] node %s is not build %s: send it that", m.cfg.ID, info.Version, version)
	if err := m.startAutoDeploy(); errors.Is(err, errFallbackOnRedeploy) {
		m.d.logf("[%s] build %s turned out a fallback: sent only on Redeploy", m.cfg.ID, version)
	} else if err != nil {
		m.d.logf("[%s] update: %v", m.cfg.ID, err)
		// the reason stays on the machine's card, as a failed Redeploy's does
		m.mu.Lock()
		m.deployErr = err.Error()
		m.mu.Unlock()
		m.d.changed()
		m.saveDeployOutcome(ctx, version, err)
	}
}

// saveDeployOutcome keeps what came of a deploy over ssh or tailcat (the update protocol saves its own,
// deploy_rpc.go), so updateOnConnect does not send a machine the same build again on every reconnect.
func (m *Machine) saveDeployOutcome(ctx context.Context, target string, err error) {
	if target == "" {
		return
	}
	record := nodeDeployRecord{Target: target, State: "succeeded", At: time.Now().UTC()}
	m.mu.Lock()
	if m.deployVersion == target {
		record.Source = m.deploySource
	}
	m.mu.Unlock()
	if err != nil {
		record.State, record.Error = "failed", err.Error()
	}
	if saveErr := m.saveDeployRecord(ctx, record); saveErr != nil {
		m.d.logf("[%s] save node deployment: %v", m.cfg.ID, saveErr)
	}
}
