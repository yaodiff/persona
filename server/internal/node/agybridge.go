package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/runtimepkg"
)

// The Antigravity (agy) companion runtime (docs/integrations/runtimes.md "Antigravity CLI"). When the machine has
// Antigravity CLI (`agy`) installed and no external `agy` runtime is connected on runtime.sock, a production RuntimeHost
// writes out the embedded persona-antigravity-runtime package and supervises `antigravity/companion.mjs` itself so the
// Persona assistant and Agents work out of the box without a manual launchd/systemd setup.

const (
	agyPackage    = "persona-antigravity-runtime"
	agyRuntimeID  = "agy"
	agyYieldGrace = 5 * time.Second
)

var (
	agyBridgeRestartMin = time.Second
	agyBridgeRestartMax = time.Minute
	agyBridgeProbeEvery = 30 * time.Second
	agyBridgeSteady     = 5 * time.Minute
	agyBridgeStopWait   = 3 * time.Second
)

var agyInstanceSeq atomic.Uint64

type agyBridge struct {
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
	instance   string
	draining   string // instance currently draining before a binary/companion upgrade
	bin        string
	wakeCh     chan struct{}
	yieldGrace time.Duration
	takeover   sync.Mutex
}

// agyChannelSpec carries per-session / per-probe channel routing and credentials to the Antigravity companion over
// the local runtime socket so each (channel_id, auth_mode, channel_rev) runs in its own isolated ACP process pool.
// Credentials stay in memory and are never placed in companion process env, arguments, files, or logs.
type agyChannelSpec struct {
	ID       string            `json:"id"`
	AuthMode string            `json:"auth_mode"`
	Rev      string            `json:"rev,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
}

// agyChannelFor resolves the isolated channel pool spec for an Antigravity launcher on demand. Native `agy` (or a
// managed agy launcher without API-key provider settings) returns nil so the companion routes it to the native OAuth pool.
func (n *Node) agyChannelFor(ctx context.Context, launcher string) (*agyChannelSpec, error) {
	if n == nil || launcher == "" || shellLauncherNames()[launcher] {
		return nil, nil
	}
	cat, ok := n.managedCatalog(launcher)
	if !ok || cat.AgentType() != agyAgent {
		return nil, nil
	}
	if err := n.ensureManaged(ctx, launcher); err != nil {
		return nil, err
	}
	l, ok := n.managedLauncher(launcher)
	if !ok || l.AgentType() != agyAgent {
		return nil, nil
	}
	rawEnv := managedEnv(l)
	if len(rawEnv) == 0 && l.APIKey == "" {
		return nil, nil
	}
	envMap := make(map[string]string, len(rawEnv)+1)
	for _, kv := range rawEnv {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			envMap[k] = v
		}
	}
	hasKey := strings.TrimSpace(envMap["GEMINI_API_KEY"]) != ""
	provider := strings.TrimSpace(envMap["PERSONA_AGY_MODEL_PROVIDER"])
	if hasKey && provider == "" {
		envMap["PERSONA_AGY_MODEL_PROVIDER"] = "gemini"
		provider = "gemini"
	}
	authMode := "oauth"
	if strings.EqualFold(provider, "gemini") || hasKey {
		authMode = "gemini-api-key"
	}
	if authMode == "oauth" && len(envMap) == 0 {
		return nil, nil
	}
	rev := strings.TrimSpace(l.Rev)
	if rev == "" {
		rev = agyChannelEnvHash(envMap)
	}
	return &agyChannelSpec{
		ID:       l.Name,
		AuthMode: authMode,
		Rev:      rev,
		Env:      envMap,
	}, nil
}

func agyChannelEnvHash(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(env[k]))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func (rh *RuntimeHost) setAgyDraining(instance string, on bool) {
	rh.agy.mu.Lock()
	defer rh.agy.mu.Unlock()
	if on {
		rh.agy.draining = instance
	} else if rh.agy.draining == instance || instance == "" {
		rh.agy.draining = ""
	}
}

func (rh *RuntimeHost) agyDraining(instance string) bool {
	rh.agy.mu.Lock()
	defer rh.agy.mu.Unlock()
	return instance != "" && rh.agy.draining == instance
}

func (rh *RuntimeHost) builtinAgyInstance() string {
	rh.agy.mu.Lock()
	defer rh.agy.mu.Unlock()
	return rh.agy.instance
}

func (rh *RuntimeHost) builtinAgyExecutable() string {
	rh.agy.mu.Lock()
	bin := rh.agy.bin
	rh.agy.mu.Unlock()
	if bin != "" {
		return bin
	}
	return findAgyExecutable()
}

func (rh *RuntimeHost) wakeAgyBridge() {
	rh.agy.mu.Lock()
	wake := rh.agy.wakeCh
	rh.agy.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

// EnableAutoCompanions turns on automatic supervision of built-in CLI companions (Antigravity `agy` and `opencode`)
// once Listen runs. Production nodes (`personad node --attach` and `personad node --stdio`) enable this; unit test
// fixtures leave it off so the host machine's installed CLIs never join synthetic runtime tests.
func (rh *RuntimeHost) EnableAutoCompanions() {
	rh.autoCompanions = true
}

func (rh *RuntimeHost) setAgyBridge(on bool) {
	b := &rh.agy
	b.mu.Lock()
	defer b.mu.Unlock()
	if on == (b.cancel != nil) {
		return
	}
	if !on {
		b.cancel()
		b.cancel = nil
		b.wakeCh = nil
		done := b.done
		b.mu.Unlock()
		<-done
		b.mu.Lock()
		return
	}
	base := rh.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	b.cancel, b.done = cancel, make(chan struct{})
	b.wakeCh = make(chan struct{}, 1)
	go rh.agyBridgeLoop(ctx, b.done, b.wakeCh)
}

// findAgyExecutable finds an installed `agy` CLI binary without spawning a login shell.
var findAgyExecutable = func() string {
	bin := "agy"
	if runtime.GOOS == "windows" {
		bin = "agy.exe"
	}
	if p, err := exec.LookPath(bin); err == nil {
		return p
	}
	home := userHome()
	candidates := []string{filepath.Join(home, ".local", "bin", bin)}
	for _, dir := range agentSystemDirs {
		candidates = append(candidates, filepath.Join(dir, bin))
	}
	candidates = append(candidates, filepath.Join(home, ".volta", "bin", bin))
	nvmDirs, _ := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin", bin))
	sort.Sort(sort.Reverse(sort.StringSlice(nvmDirs)))
	candidates = append(candidates, nvmDirs...)
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && (st.Mode().Perm()&0o111 != 0 || runtime.GOOS == "windows") {
			return p
		}
	}
	return ""
}

func agyJSVersion(ctx context.Context, js jsRuntime) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, js.Path, "-e", "import('node:sqlite').then(()=>process.stdout.write(process.versions.node),()=>process.exit(1))")
	cmd.Env = js.env(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return "", errors.New("Node.js 22.5+ with node:sqlite required")
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", errors.New("no version")
	}
	return v, nil
}

func findAgyJSRuntime(ctx context.Context) (jsRuntime, error) {
	var cands []jsRuntime
	if p := strings.TrimSpace(os.Getenv("PERSONA_NODEJS")); p != "" {
		cands = append(cands, jsRuntime{Path: p, Electron: !isNodeBinary(p), From: "PERSONA_NODEJS"})
	}
	if p, err := exec.LookPath("node"); err == nil {
		cands = append(cands, jsRuntime{Path: p, From: "PATH"})
	}
	for _, p := range commonNodePaths() {
		cands = append(cands, jsRuntime{Path: p, From: "installed"})
	}
	seen := map[string]bool{}
	var tried []string
	for _, c := range cands {
		if seen[c.Path] {
			continue
		}
		seen[c.Path] = true
		if st, err := os.Stat(c.Path); err != nil || st.IsDir() {
			continue
		}
		v, err := agyJSVersion(ctx, c)
		if err != nil {
			tried = append(tried, c.Path+": "+err.Error())
			continue
		}
		c.Version = v
		return c, nil
	}
	if len(tried) > 0 {
		return jsRuntime{}, fmt.Errorf("no usable Node.js (%s)", strings.Join(tried, "; "))
	}
	return jsRuntime{}, errors.New("no Node.js found (set PERSONA_NODEJS)")
}

var agyBridgeCommand = func(ctx context.Context, rh *RuntimeHost, agyBin, instance string) (*exec.Cmd, error) {
	pkg, err := runtimepkg.Load(agyPackage)
	if err != nil {
		return nil, err
	}
	if pkg == nil {
		return nil, errors.New("this personad carries no Antigravity runtime package (built without go generate ./internal/runtimepkg)")
	}
	dir, err := writePackageWithReservation(filepath.Join(filepath.Dir(rh.path), "runtime-packages", agyPackage+"-builtin"), pkg, true)
	if err != nil {
		return nil, fmt.Errorf("write Antigravity runtime: %w", err)
	}
	js, err := findAgyJSRuntime(ctx)
	if err != nil {
		return nil, err
	}
	args := []string{filepath.Join(dir, "antigravity", "companion.mjs"),
		"--socket", rh.dialPath(), "--agy-bin", agyBin, "--no-update", "--parent-stdin"}
	if instance != "" {
		args = append(args, "--instance", instance)
	}
	// The companion attaches `<this program> mcp` to its chats for Persona's tools, and is told which program that is:
	// a Persona Link install calls it persona-node, which a lookup for personad does not find (#14582).
	if bin := ownProgram(); bin != "" {
		args = append(args, "--personad-bin", bin)
	}
	cmd := exec.Command(js.Path, args...)
	cmd.Env = js.env(agyBridgeEnv(ctx, rh, os.Environ()))
	cmd.Dir = dir
	return cmd, nil
}

// agyBridgeEnv constructs the companion control-plane environment from base, applying only the runtime socket and
// OS/login-shell proxy settings. Channel credentials are never injected into the companion process environment; they
// are delivered per session/probe to isolated ACP pools over the local runtime socket.
func agyBridgeEnv(ctx context.Context, rh *RuntimeHost, base []string) []string {
	env := slices.Clone(base)
	if rh != nil {
		env = setEnvVar(env, "PERSONA_RUNTIME_SOCKET", rh.dialPath())
	}
	env = withSystemProxyEnv(ctx, env)
	return withShellProxyEnv(ctx, env)
}

func (rh *RuntimeHost) agyConnected() bool {
	if rh.reg == nil {
		return false
	}
	rh.reg.mu.Lock()
	defer rh.reg.mu.Unlock()
	return rh.reg.byRuntime(agyRuntimeID) != nil
}

func (rh *RuntimeHost) agyBridgeLoop(ctx context.Context, done chan struct{}, wake <-chan struct{}) {
	defer close(done)
	wait := agyBridgeRestartMin
	loggedMissingJS := false
	for ctx.Err() == nil {
		agyBin := findAgyExecutable()
		if agyBin == "" || rh.agyConnected() {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-time.After(agyBridgeProbeEvery):
			}
			continue
		}
		started := time.Now()
		yielded, err := rh.runAgyBridge(ctx, agyBin, wake)
		if ctx.Err() != nil {
			return
		}
		if yielded {
			wait = agyBridgeRestartMin
			continue
		}
		if time.Since(started) > agyBridgeSteady {
			wait = agyBridgeRestartMin
		}
		if err != nil {
			if strings.Contains(err.Error(), "Node.js") {
				if !loggedMissingJS {
					loggedMissingJS = true
					rh.logger.Printf("agy runtime: %v", err)
				}
			} else {
				rh.logger.Printf("agy runtime: %v (again in %s)", err, wait)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, agyBridgeRestartMax)
	}
}

func (rh *RuntimeHost) runAgyBridge(ctx context.Context, agyBin string, wake <-chan struct{}) (bool, error) {
	instance := fmt.Sprintf("builtin-agy-%d-%d", os.Getpid(), agyInstanceSeq.Add(1))
	rh.agy.mu.Lock()
	rh.agy.instance = instance
	rh.agy.bin = agyBin
	rh.agy.mu.Unlock()
	defer func() {
		rh.setAgyDraining(instance, false)
		rh.agy.mu.Lock()
		if rh.agy.instance == instance {
			rh.agy.instance = ""
			rh.agy.bin = ""
		}
		rh.agy.mu.Unlock()
	}()

	bin := statAgy(agyBin) // the file the companion runs; a different one later restarts it once idle (agyChange)
	cmd, err := agyBridgeCommand(ctx, rh, agyBin, instance)
	if err != nil {
		return false, err
	}
	cmd.SysProcAttr = osproc.GroupAttr()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	cmd.Stderr = cmd.Stdout
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return false, err
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("start: %w", err)
	}
	rh.logger.Printf("agy runtime: running (pid %d, agy %s)", cmd.Process.Pid, agyBin)
	logged := make(chan struct{})
	go func() {
		defer close(logged)
		logLines(out, func(line string) {
			rh.logger.Printf("agy runtime: %s", line)
		})
	}()
	exited := make(chan error, 1)
	go func() {
		<-logged
		exited <- cmd.Wait()
	}()
	stopChild := func() {
		_ = osproc.StopGroup(cmd.Process.Pid, false)
		select {
		case <-exited:
		case <-time.After(agyBridgeStopWait):
			_ = osproc.StopGroup(cmd.Process.Pid, true)
			<-exited
		}
	}
	probe := time.NewTicker(agyBridgeProbeEvery)
	defer probe.Stop()
	var change agyChange
	for {
		if _, external := rh.agyPeers(instance); external {
			rh.logger.Printf("agy runtime: yielding to external agy runtime")
			stopChild()
			return true, nil
		}
		select {
		case err := <-exited:
			return false, err
		case <-wake:
		case <-probe.C:
			if change.see(bin, statAgy(findAgyExecutable())) {
				builtin, _ := rh.agyPeers(instance)
				if builtin != nil && builtin.agyBusy() {
					rh.setAgyDraining(instance, true)
					change.logWait(rh.logger, bin)
					continue
				}
				if builtin != nil {
					// Suspend new admissions and re-verify agyBusy so no turn racing in is killed.
					builtin.peer.SuspendCalls()
					if builtin.agyBusy() {
						builtin.peer.ResumeCalls()
						rh.setAgyDraining(instance, true)
						change.logWait(rh.logger, bin)
						continue
					}
				}
				rh.setAgyDraining(instance, false)
				rh.logger.Printf("agy runtime: %s; restarting", change.describe(bin))
				stopChild()
				return true, nil
			}
			rh.setAgyDraining(instance, false)
		case <-ctx.Done():
			stopChild()
			return false, ctx.Err()
		}
	}
}

// agyBinary is an agy executable as a companion started on it: its path and the file there then (nil: none could be
// read). The node resolves agy once per companion start, so one installed elsewhere later, or replaced in place by an
// update, is noticed only by comparing (#14533).
type agyBinary struct {
	path string
	info os.FileInfo
}

func statAgy(path string) agyBinary {
	b := agyBinary{path: path}
	if path != "" {
		if info, err := os.Stat(path); err == nil {
			b.info = info
		}
	}
	return b
}

// same: o is the same path to the same file, unchanged (os.SameFile, size and modification time).
func (b agyBinary) same(o agyBinary) bool {
	if b.path != o.path || (b.info == nil) != (o.info == nil) {
		return false
	}
	return b.info == nil || os.SameFile(b.info, o.info) && b.info.Size() == o.info.Size() && b.info.ModTime().Equal(o.info.ModTime())
}

// agyChange follows what the probes find in place of the companion's agy. A change counts once two probes in a row
// see the same one, so an installer caught between removing the old file and writing the new one restarts nothing.
type agyChange struct {
	seen   *agyBinary
	logged bool
}

// see records what a probe found, now, against the companion's own bin; true once the same change was seen twice.
func (c *agyChange) see(bin, now agyBinary) bool {
	if now.same(bin) {
		c.seen, c.logged = nil, false
		return false
	}
	if c.seen == nil || !c.seen.same(now) {
		c.seen, c.logged = &now, false
		return false
	}
	return true
}

func (c *agyChange) describe(bin agyBinary) string {
	switch {
	case c.seen == nil:
		return "agy is unchanged"
	case c.seen.path == "":
		return fmt.Sprintf("agy at %s is gone", bin.path)
	case c.seen.path != bin.path:
		return fmt.Sprintf("agy moved from %s to %s", bin.path, c.seen.path)
	}
	return fmt.Sprintf("agy at %s was replaced", bin.path)
}

// logWait says once per change that the restart waits for the companion's work: an agy turn in flight, finite
// background work a session left running, or a call the node is waiting on, would be cut by it. The old file keeps
// running until then, with no deadline: a background command that never ends holds the restart until it is stopped.
func (c *agyChange) logWait(logger *log.Logger, bin agyBinary) {
	if !c.logged {
		c.logged = true
		logger.Printf("agy runtime: %s; restarting once no agy session is working or running background work", c.describe(bin))
	}
}

func (rh *RuntimeHost) agyPeers(instance string) (builtin *runtimeConn, external bool) {
	rh.reg.mu.Lock()
	defer rh.reg.mu.Unlock()
	for _, conn := range rh.reg.conns {
		select {
		case <-conn.peer.Done():
			continue
		default:
		}
		conn.mu.Lock()
		ready, hello := conn.ready, conn.hello
		conn.mu.Unlock()
		if ready && hello.Runtime == agyRuntimeID {
			if hello.Instance != instance {
				external = true
			} else if builtin == nil || conn.serial > builtin.serial {
				builtin = conn
			}
		}
	}
	return builtin, external
}

// waitAgyIdle waits until no call to the companion is pending and agyWorking is false, the companion's connection
// ends, or ctx does: the external-runtime handoff bounds it by its yield grace, so background work that runs longer is
// cut then, as a long turn is.
func (h *runtimeConn) waitAgyIdle(ctx context.Context) {
	if h.peer.WaitPending(ctx) != nil {
		return
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for h.agyWorking() {
		select {
		case <-ctx.Done():
			return
		case <-h.peer.Done():
			return
		case <-tick.C:
		}
	}
}

// agyBusy: the companion has a session at work, or the node waits on a call to it (a start, a check), without waiting.
func (h *runtimeConn) agyBusy() bool {
	return h.peer.Pending() > 0 || h.agyWorking()
}

// agyWorking: one of the companion's sessions has a turn in flight, or its turn ended while finite background work it
// started still runs in the companion's `agy acp` (agyFiniteBackground, #17200): stopping the companion now would cut
// either one.
func (h *runtimeConn) agyWorking() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, session := range h.sessions {
		if session.Status == proto.StatusWorking || agyFiniteBackground(session) {
			return true
		}
	}
	return false
}

// agyFiniteBackground: s lists background work the companion identified and that ends by itself, which a companion
// restart would stop ("All your subagents and background tasks have been stopped due to server restart"): a Background
// task agyIdentifiedFiniteTask accepts, or a running entry in Subagents or Workflows. Only identified work counts, so a
// misread row can never hold an upgrade forever: integrations/antigravity/lib/background.js extractBackground calls a
// task shell both for a matched run_command (with its command line) and for any start notice it could not match to its
// tool call (none, or two calls claiming the step), and a description cannot tell an unmatched command from an
// unmatched schedule in either direction (its own fixtures include a one-shot timer described "Check status" and a
// cron schedule described "Timer: 60s"). Schedules (kind monitor: agy's `schedule`, cron or one-shot, which the row does
// not tell apart) are left out too, so a one-shot timer, like an unmatched task, is still stopped by a restart, as
// before #17200; closing that needs the companion to carry what it knows (its computed oneShot, whether a task was
// matched), the companion-side follow-up.
// The rows are the companion's runtime.sessions: Background lists only tasks running in a session its `agy acp` has
// loaded (a terminal's tasks come separately, in agyPaneBackground, and a restart does not stop them), and the
// hub-derived BackgroundWorking / BackgroundWaiting are not read, as they count a monitor too. Unlike
// agySessionRowSafeIdle, which keeps idle compaction off every session with any background entry, this only decides
// whether a restart must wait. The list is not a full inventory: the companion lists only its recent sessions
// (rt.recent: up to 80 from the last 14 days, besides those with live work, assistants and ones the phone drove lately)
// and at most 50 running tasks per session, which this node caps at 50 too, so a command in an unlisted idle session or
// behind 50 schedules is missed. Closing that needs a drain signal the companion computes before those limits (#17200,
// the companion-side follow-up).
func agyFiniteBackground(s proto.Session) bool {
	for _, task := range s.Background {
		if agyIdentifiedFiniteTask(task) {
			return true
		}
	}
	for _, sub := range s.Subagents {
		if sub.Running {
			return true
		}
	}
	for _, wf := range s.Workflows {
		if wf.Running {
			return true
		}
	}
	return false
}

// agyIdentifiedFiniteTask: task is finite background work the companion matched to its tool call: a run_command (kind
// shell with its command line; an unmatched notice has none), a run_workflow (kind workflow) or an invoke_subagent
// (kind agent), the only kinds extractBackground gives a matched call besides schedules.
func agyIdentifiedFiniteTask(task proto.BackgroundTask) bool {
	switch task.Kind {
	case "workflow", "agent":
		return true
	case "shell":
		return task.Command != ""
	}
	return false
}
