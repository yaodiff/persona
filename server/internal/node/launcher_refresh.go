package node

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	managedLaunchConfigEnv   = "PERSONA_MANAGED_LAUNCHER_CONFIG"
	managedSettingsConfigEnv = "PERSONA_MANAGED_LAUNCHER_SETTINGS"
	managedRestartWait       = 2 * time.Minute
)

// The revision tells the node when to pull. The resolved configuration tells it whether the process must change:
// re-saving a provider, changing its device scope or comparing with a legacy push must not force a restart.
func managedClaudeFingerprint(l proto.ManagedLauncher) string {
	clear := envClearFor(l)
	slices.Sort(clear)
	effort := launcherDefaultEffort(l)
	var noEffort []string
	if effort != "" {
		noEffort = noEffortModels(l)
		slices.Sort(noEffort)
	}
	data, _ := json.Marshal(struct {
		Env, Clear, Args, NoEffort []string
		Settings                   map[string]string
		OutputLimits               map[string]int
		Model, Effort              string
		MergeNoProxy               bool
	}{
		Env: managedEnv(l), Clear: slices.Compact(clear), Args: append([]string(nil), l.Args...), NoEffort: noEffort,
		Settings: managedSettingsEnv(l), OutputLimits: outputLimits(l), Model: l.Model, Effort: effort, MergeNoProxy: mergesNoProxy(l),
	})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// Keep the stamp on the actual process, so a reconnect or a new Claude in the same pane cannot inherit a node's
// observation of its predecessor. A saved herdr resume command can point to older settings than launcher.env served.
func managedLaunchStamp(name, config, settings, nodeDir string) string {
	stamp := name + ":" + config
	if filepath.Dir(settings) == filepath.Join(nodeDir, "launcher-settings") {
		if parsed, err := readLauncherSettings(settings, nodeDir, ""); err == nil {
			env, _ := parsed["env"].(map[string]any)
			if version, _ := env[managedSettingsConfigEnv].(string); len(version) == sha256.Size*2 {
				stamp += ":" + version
			}
		}
	}
	return stamp
}

func (n *Node) readyManagedLauncher(ctx context.Context, name string) (proto.ManagedLauncher, error) {
	l, _ := n.managedLauncher(name)
	if !l.Withheld {
		return l, nil
	}
	if err := n.ensureManaged(ctx, name); err != nil {
		return proto.ManagedLauncher{}, err
	}
	l, ok := n.managedLauncher(name)
	if !ok || l.Withheld {
		return proto.ManagedLauncher{}, errManagerAway(name)
	}
	return l, nil
}

func parseProcessEnviron(raw []byte) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	env := make(map[string]string)
	for field := range strings.SplitSeq(string(raw), "\x00") {
		if key, value, ok := strings.Cut(field, "="); ok {
			env[key] = value
		}
	}
	return env
}

// For a pre-upgrade process, the node can verify only values which the launcher controls. Inherited proxy bypasses
// and variables removed from the provider's arbitrary Env have no reliable provenance in an old process.
func legacyManagedConfigDiffers(env map[string]string, l proto.ManagedLauncher) bool {
	want := providerEnv(l, "", "", false)
	for _, key := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_CUSTOM_HEADERS"} {
		if env[key] != want[key] {
			return true
		}
	}
	for _, key := range proxyVars {
		if value, set := want[key]; set && env[key] != value {
			return true
		}
	}
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		if value, set := want[key]; set && env[key] != value {
			if !mergesNoProxy(l) || mergeNoProxy(env[key], value) != env[key] {
				return true
			}
		}
	}
	for _, kv := range launcherEnvVars(l.Env) {
		if got, ok := env[kv[0]]; !ok || got != want[kv[0]] {
			return true
		}
	}
	return false
}

func (n *Node) managedLauncherProcess(id string) (pid int, start string) {
	pid, start = n.agentProcess(id)
	if pid > 0 && start == "" { // older session files do not record a platform-specific process start
		start = procStartMark(pid)
	}
	return pid, start
}

type managedRestartKey struct{}

// refreshManagedLauncher runs under the session's input lock. It carries no pending switch and cannot consume one.
func (n *Node) refreshManagedLauncher(ctx context.Context, id, pane string) (bool, error) {
	if n.restarting(id) {
		return false, nil
	}
	st, had := n.state.get(id)
	if st.Launcher != "" {
		if _, ok := n.managedCatalog(st.Launcher); !ok {
			return false, nil
		}
	}
	pid, start := n.managedLauncherProcess(id)
	if pid <= 0 || start == "" {
		return false, nil
	}
	env := managedLauncherProcessEnv(pid)
	if env == nil {
		return false, nil
	}
	name, fingerprints, stamped := strings.Cut(env[managedLaunchConfigEnv], ":")
	if !stamped {
		// A record's launcher alone does not name the process: a bare Claude can resume that conversation too.
		if run, ok := n.sessionRun(id); !ok || run.PID != pid || run.Start != start || !mcpConfigsHold(run.MCPConfig, n.agentMCPConfig()) {
			return false, nil
		}
		name = st.Launcher
	}
	l, ok := n.managedLauncher(name)
	if !ok || l.AgentType() != "claude" {
		return false, nil
	}
	pctx, cancel := context.WithTimeout(ctx, switchProbeWait)
	defer cancel()
	if l.Withheld {
		if n.switchBusy(pctx, id) != "" {
			return false, nil
		}
		var err error
		l, err = n.readyManagedLauncher(pctx, name)
		if err != nil {
			return false, refusal(notDelivered, err.Error(), nil)
		}
	}
	if l.Name != name || l.AgentType() != "claude" {
		return false, nil
	}
	config := managedClaudeFingerprint(l)
	if stamped {
		process, settings, _ := strings.Cut(fingerprints, ":")
		if process == config && (settings == "" || settings == config) {
			return false, nil
		}
	} else if !legacyManagedConfigDiffers(env, l) {
		return false, nil
	}
	if n.switchBusy(pctx, id) != "" {
		return false, nil
	}

	defer n.markSwitching(id)()
	a, h, ref, err := n.switchSession(pctx, id, false)
	if err != nil {
		return false, err
	}
	cwd := n.switchCwd(id, a)
	if cwd == "" {
		return false, refusal(notDelivered, "Cannot find the conversation's folder to resume it with updated provider settings.", nil)
	}
	execution := n.ranExecution(id, st)
	if _, _, err := n.checkSwitchLauncher(pctx, id, name, execution.Model); err != nil {
		return false, asSwitchError(err, switchUnsupported)
	}
	if err := n.refreshHosts(pctx, h); err != nil {
		return false, refusal(notDelivered, "Cannot verify that the terminal is idle to apply updated provider settings.", nil)
	}
	current, live := n.agent(id)
	if !live || current.PaneID != pane || a.PaneID != pane {
		return false, refusal(notDelivered, "The conversation moved before the updated provider settings could be applied.", nil)
	}
	if current.AgentStatus != proto.StatusIdle && current.AgentStatus != proto.StatusDone || n.switchActivity(id) != "" {
		return false, nil
	}
	if p, s := n.managedLauncherProcess(id); p != pid || s != start {
		return false, refusal(notDelivered, "The Claude process changed before the updated provider settings could be applied. Send the message again.", nil)
	}
	if err := n.checkPaneFor(pctx, pane, "claude", sendOpts{readBox: true}); err != nil {
		return false, err
	}
	if err := pctx.Err(); err != nil {
		return false, refusal(notDelivered, err.Error(), nil)
	}
	// A revision arriving during preparation will be checked on the next send if the process remains the same.
	if latest, ok := n.managedLauncher(name); !ok || latest.Withheld || latest.AgentType() != "claude" ||
		managedClaudeFingerprint(latest) != config {
		return false, refusal(notDelivered, "The provider settings changed again. Send the message again to use the latest settings.", nil)
	}
	r := n.resumeWith(id, sendOpts{})
	row := proto.Session{SessionID: id, Cwd: cwd, Launcher: name, Agent: "claude", Status: proto.StatusIdle,
		Starting: true, StartedByPersona: had, UpdatedAt: time.Now().UTC(), WorkspaceID: a.WorkspaceID, PendingSwitch: n.pendingSwitch(id)}
	if !n.switches().begin(id, row) {
		return false, switchUnderWay()
	}
	defer func() {
		if serving := n.switches().end(id); serving != nil && serving != n {
			serving.notifySessionsSoon()
		}
		n.notifySessionsSoon()
	}()
	// Like an explicit switch, finish the process replacement once the quit starts. The message still obeys ctx.
	wctx, stop := context.WithTimeout(context.WithoutCancel(ctx), managedRestartWait)
	defer stop()
	shells, dir, err := n.quitForLauncherRestart(wctx, id, name, a, h, ref)
	if err != nil {
		return false, err
	}
	sctx, stopStart := switchStartCtx(wctx, a, h, ref, shells, dir, cwd)
	defer stopStart()
	if r.taskID != "" {
		sctx, _ = withTaskSpace(sctx, r.taskID, r.workspaceID, r.workspaceLabel)
	}
	sctx = context.WithValue(sctx, managedRestartKey{}, true)
	if _, err := n.start(sctx, cwd, name, "", id, a.WorkspaceID, r.mode, r.effort, "", "", "claude", execution); err != nil {
		return false, refusal(notDelivered, "Claude Code could not resume with the updated provider settings. Check its terminal or use Resume on its terminal page.", nil)
	}
	n.logf("session %s: resumed on launcher %s with updated settings", id, name)
	if err := ctx.Err(); err != nil {
		return false, refusal(notDelivered, err.Error(), nil)
	}
	return true, nil
}
