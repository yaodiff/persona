package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
)

// Permission mode and effort level of a Claude session, chosen from the app (docs/apple/ios.md "Effort and permission modes").
//
// Shown: the transcript records the mode on every prompt and the effort on every reply; a value set from the app
// wins until the transcript shows a newer one (a TUI only writes the mode down with the next prompt). An assistant
// conversation's explicit choice is persisted with its session state and wins over transcript/default history;
// an inherited or legacy value follows the manager's current default on the next stopped-process resume.
// Set: at start as --permission-mode / --effort; later, headless conversations take control requests on stdin and
// TUI sessions are driven like a person would: shift+tab cycles the mode, and the /effort picker advertises the
// confirmation key (`/effort <level>` would also save the level as the default for new sessions).

// "auto" is the model's own default level (Claude Code 2.1.316). It is not a level Claude Code takes everywhere:
// --effort rejects it (with a warning, falling back to the default), settings.json ignores it, and the /effort
// picker has no slot for it; only `/effort auto` knows it, and that also clears the model's saved default. So
// auto starts a session without --effort, is `effortLevel: null` for a headless one, and in a TUI is the picker's
// slot marked as the model's default. Transcripts record the level replies actually ran at, never "auto".

const effortAuto = "auto"

var effortLevels = append([]string{effortAuto}, proto.EffortLevels...)

// effortPickerLevels are the slots of the /effort picker, left to right.
var effortPickerLevels = proto.EffortLevels

// effortPickerDefault is the hint under the picker's marker when it is on the model's default level.
const effortPickerDefault = "The default effort for this model"

var errEffortPickerNotOpen = errors.New("没打开思考强度的选择器（这个 Claude Code 版本可能不支持 /effort）")

var permissionModes = proto.PermissionModes

// tuiModeCycle is the order shift+tab walks through (Claude Code 2.1.x); bypassPermissions is only in the cycle
// of sessions started with it.
var tuiModeCycle = []string{"default", "acceptEdits", "plan", "auto", "bypassPermissions"}

// processArgv reads a process' arguments without running anything (kern.procargs2, /proc/<pid>/cmdline); tests stub it.
var processArgv = updateProcessArgv

// claudeCanBypass: the claude process pid was started with a flag that puts bypassPermissions in its shift+tab cycle,
// so tuiSetMode can reach Allow All while it runs (#1379). False when its arguments cannot be read.
func claudeCanBypass(pid int) bool {
	if pid <= 0 {
		return false
	}
	args, err := processArgv(pid)
	return err == nil && argsCanBypass(args)
}

// argsCanBypass: --dangerously-skip-permissions, --allow-dangerously-skip-permissions or --permission-mode
// bypassPermissions among a claude's arguments, before a "--" (what follows it is the prompt).
func argsCanBypass(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--":
			return false
		case a == "--dangerously-skip-permissions", a == "--allow-dangerously-skip-permissions", a == "--permission-mode="+proto.ModeBypass:
			return true
		case a == "--permission-mode" && i+1 < len(args) && args[i+1] == proto.ModeBypass:
			return true
		}
	}
	return false
}

// argsPermissionMode returns the explicit --permission-mode passed before "--" in a claude process's arguments, or "".
func argsPermissionMode(args []string) string {
	mode := ""
	for i, a := range args {
		switch {
		case a == "--":
			return mode
		case strings.HasPrefix(a, "--permission-mode="):
			mode = strings.TrimPrefix(a, "--permission-mode=")
		case a == "--permission-mode" && i+1 < len(args):
			mode = args[i+1]
		}
	}
	return mode
}

// sessionModes is a mode / effort the app set, with when, until the transcript catches up.
type sessionModes struct {
	Mode, Effort     string
	ModeAt, EffortAt time.Time
	// Codex: an effort waiting for the phone's next turn/start on a Codex thread whose app-server has no
	// thread/settings/update (codex_effort.go); nil none. Apart from Mode / Effort, which are Claude's.
	Codex *codexPending
	// App: the effort the phone set for the next turn of a chat of the Codex App (codex_desktop_effort.go); nil none.
	App *codexAppPick
	// Model: the model the app switched a Claude Code session to, shown until its transcript shows another (model_switch.go).
	Model *pendingModel
	// Awaited: a model switch typed into Claude Code and not confirmed in time, taken up as Model once Claude Code's
	// own record of it shows in the transcript (model_switch.go).
	Awaited   *awaitedModel
	Ultracode *ultracodeState // independent, session-only workflow switch (ultracode.go)
}

func validModes(mode, effort string) error {
	if mode != "" && !slices.Contains(permissionModes, mode) {
		return fmt.Errorf("unknown permission mode %q", mode)
	}
	if effort != "" && !slices.Contains(effortLevels, effort) {
		return fmt.Errorf("unknown effort level %q", effort)
	}
	return nil
}

// validModesFor validates a start's mode / effort against the launcher it runs on: a Codex start takes Codex's own
// effort list (ultra included, proto.CodexEffortLevels). TRAE CLI uses the same vocabulary without auto.
func validModesFor(launcher, mode, effort string) error {
	if mode != "" && !slices.Contains(permissionModes, mode) {
		return fmt.Errorf("unknown permission mode %q", mode)
	}
	levels := effortLevels
	switch launcherAgentType(launcher) {
	case "codex":
		levels = append([]string{effortAuto}, proto.CodexEffortLevels...)
	case traexAgent:
		levels = proto.CodexEffortLevels
	case agyAgent:
		levels = proto.EffortLevels
	}
	if effort != "" && !slices.Contains(levels, effort) {
		return fmt.Errorf("unknown effort level %q", effort)
	}
	return nil
}

// An assistant's custom launcher name does not identify its agent. Only resolve the launcher for levels that need
// Codex: ordinary Claude levels remain a cheap check, and unknown levels never become valid through a probe.
func (n *Node) validAssistantModes(ctx context.Context, launcher, mode, effort string) error {
	if err := validModes(mode, ""); err != nil {
		return err
	}
	if effort == "" || slices.Contains(effortLevels, effort) {
		return nil
	}
	if !slices.Contains(proto.CodexEffortLevels, effort) {
		return validModes(mode, effort)
	}
	agent, err := n.assistantAgent(ctx, launcherOr(launcher, "claude"))
	if err != nil {
		return err
	}
	return validModesFor(agent, mode, effort)
}

// modeFlags are the claude flags starting a session with a mode / effort (values already validated).
func modeFlags(mode, effort string) string {
	var b strings.Builder
	if mode != "" {
		b.WriteString(" --permission-mode " + mode)
	}
	if effort != "" && effort != effortAuto { // no flag is the model's default
		b.WriteString(" --effort " + effort)
	}
	return b.String()
}

func (n *Node) noteModes(id, mode, effort string) {
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	m := n.modes[id]
	if mode != "" {
		m.Mode, m.ModeAt = mode, now
	}
	if effort != "" {
		m.Effort, m.EffortAt = effort, now
	}
	n.modes[id] = m
}

// persistModes keeps an assistant conversation's mode / effort in its session state (the hub's Postgres), so they
// outlive the node. The optional explicit flag marks a deliberate mode choice without a second state write.
func (n *Node) persistModes(id, mode, effort string, explicit ...bool) {
	if n.state == nil || id == "" || (mode == "" && effort == "") {
		return
	}
	markExplicit := len(explicit) > 0 && explicit[0] && mode != ""
	st, _ := n.state.get(id)
	if (mode == "" || st.AppMode == mode) && (effort == "" || st.AppEffort == effort) && (!markExplicit || st.AppModeExplicit) {
		return
	}
	if mode != "" {
		st.AppMode = mode
	}
	if markExplicit {
		st.AppModeExplicit = true
	}
	if effort != "" {
		st.AppEffort = effort
	}
	n.state.put(id, st)
}

// currentModes is the session's mode and effort: from its transcript, or what the app set since.
func (n *Node) currentModes(id string, m quickMeta) (mode, effort string) {
	return n.currentModesWithEffort(id, m, "", time.Time{})
}

func (n *Node) currentModesWithEffort(id string, m quickMeta, confirmed string, observedAt time.Time) (mode, effort string) {
	mode, effort = m.mode, m.effort
	if mode != "" && !slices.Contains(permissionModes, mode) && n.isAssistantSession(id) {
		mode = modeFromCodex(mode, m.sandbox) // a Codex rollout records its approval policy and sandbox
	}
	// An assistant conversation's mode and effort set from the app (persisted, persistModes) win over its transcript:
	// only the node changes them, and a transcript keeps recording the mode it was started in until the next prompt.
	if n.state != nil {
		if st, ok := n.state.get(id); ok {
			if st.AppMode != "" {
				mode = st.AppMode
			}
			if st.AppEffort != "" {
				effort = st.AppEffort
			}
		}
	}
	n.mu.Lock()
	set, ok := n.modes[id]
	n.mu.Unlock()
	if ok {
		if set.Mode != "" && set.ModeAt.After(m.modeAt) {
			mode = set.Mode
		}
		if set.Effort != "" && set.EffortAt.After(m.effortAt) {
			effort, m.effortAt = set.Effort, set.EffortAt
		}
	}
	if observedAt.After(m.effortAt) {
		effort = confirmed
	}
	return mode, effort
}

// modeFromCodex reads the app's mode back from what an assistant's Codex turn ran with (codexPolicy in reverse), so
// it survives a node restart. acceptEdits runs like default and reads back as default.
func modeFromCodex(approval, sandbox string) string {
	// Older assistant turns used node-approved untrusted requests for full access.
	if approval == "untrusted" && sandbox == "danger-full-access" {
		return proto.ModeBypass
	}
	for _, mode := range []string{"default", "auto", "plan", proto.ModeBypass} {
		if a, s := codexPolicy(mode); a == approval && s == sandbox {
			return mode
		}
	}
	return ""
}

// modesOf is currentModes for a session id, reading its transcript meta when there is one.
func (n *Node) modesOf(id string) (mode, effort string) {
	var m quickMeta
	if p := n.transcriptPath(id); p != "" {
		if st, err := os.Stat(p); err == nil {
			m = n.meta(p, st)
		}
	}
	n.mu.Lock()
	a := n.agents[id]
	n.mu.Unlock()
	if a.Agent == "claude" && a.PaneID != "" {
		_, confirmed := n.switches().terminalEfforts(id)
		if confirmed.pane == a.PaneID {
			return n.currentModesWithEffort(id, m, confirmed.value, confirmed.at)
		}
		return n.currentModes(id, m)
	}
	if observed, pid := n.ccPlugins.observedEffort(id); pid != 0 {
		if c, ok := n.outsideClaude(id); ok && c.pid == pid && n.outsideSource(c) == proto.SourceClaudeDesktop {
			return n.currentModesWithEffort(id, m, observed.Level, time.UnixMilli(observed.AtMS))
		}
	}
	return n.currentModes(id, m)
}

// setModes changes a running session's permission mode and / or effort level ("" leaves one as it is).
func (n *Node) setModes(ctx context.Context, id, mode, effort string) error {
	return n.updateModes(ctx, id, mode, effort, nil, nil)
}

func (n *Node) updateModes(ctx context.Context, id, mode, effort string, pending, changed *bool) error {
	markChanged := func() {
		if changed != nil {
			*changed = true
		}
	}
	if mode == "" && effort == "" {
		return nil
	}
	if !n.isAssistantSession(id) {
		if err := n.switchingRefusal(id); err != nil { // a launcher switch, maybe on the last connection's Node
			return err
		}
		// A Codex thread on its app-server takes Codex's efforts, checked against its model (codex_effort.go).
		if c, t, ok := n.codexEffortThread(id); ok {
			if mode != "" {
				return fmt.Errorf("Codex 的会话在手机上只能改思考强度和模型，权限模式要在电脑上改")
			}
			if effort == "" {
				return nil
			}
			if err := n.setCodexEffort(ctx, id, c, t, effort); err != nil {
				return err
			}
			markChanged()
			return nil
		}
	}
	if err := validModes(mode, effort); err != nil {
		return err
	}
	if n.isAssistantSession(id) {
		if mode == proto.ModePlan && n.conversationAgent(id) == traexAgent {
			return fmt.Errorf("TRAE CLI Inbox does not support plan mode")
		}
		n.mu.Lock()
		p := n.procs[id]
		n.mu.Unlock()
		if p != nil && p.alive() && p.agent == "claude" {
			p.settingsMu.Lock()
			defer p.settingsMu.Unlock()
			p.mu.Lock()
			effortRestart := p.effortNeedsRestartLocked(effort) // an interactive process's effort (assistant_tui.go)
			p.mu.Unlock()
			if effortRestart && !p.needsRestartFor(mode) {
				n.noteModes(id, mode, effort)
				n.persistModes(id, mode, effort, true)
				p.mu.Lock()
				active := p.busy || len(p.tasks) > 0
				p.mu.Unlock()
				if !active {
					n.stopAssistant(id)
				}
				n.notifySessions()
				markChanged()
				return nil // an active one restarts with it at the next idle message
			}
			if p.needsRestartFor(mode) {
				// bypassPermissions requires a new process, as does any mode for interactive Claude Code. Record the
				// choice while holding the same lock as ordinary changes, so an older ACK cannot overwrite it.
				n.noteModes(id, mode, "")
				n.persistModes(id, mode, "", true)
				p.mu.Lock()
				active := p.busy || len(p.tasks) > 0
				tui := p.tui && p.tuiNote == ""
				p.mu.Unlock()
				if active && tui {
					n.noteModes(id, "", effort)
					n.persistModes(id, "", effort)
					n.notifySessions()
					markChanged()
					return nil // both wait for the restart at the next idle message
				}
				if !active {
					n.stopAssistant(id)
					n.noteModes(id, "", effort)
					n.persistModes(id, "", effort)
					n.notifySessions()
					markChanged()
					return nil
				}
				mode = "" // Keep the active turn; the next idle message restarts it in bypass mode.
			}
			if err := n.applyAssistantModes(ctx, p, mode, effort); err != nil {
				return err
			}
			markChanged()
			return nil
		}
		n.noteModes(id, mode, effort)
		n.persistModes(id, mode, effort, true)
		if n.runtimeAssistant(id) {
			if h, _, _ := n.runtimes.belongs(id); h != nil {
				st, _ := n.state.get(id)
				params := map[string]any{"session_id": id, "permission_mode": mode, "effort": effort, "assistant": true}
				if env := n.assistantEnvForRuntime(h, n.runtimeAssistantCwd(id, "", st), id); env != nil {
					params["env"] = env
				}
				var res struct {
					PermissionMode string `json:"permission_mode"`
				}
				cctx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
				err := h.peer.Call(cctx, "session.modes", params, &res)
				cancel()
				if err != nil && mode == "auto" && h.agent() == agyAgent && isAgyAutoModeUnavailableErr(err) {
					params["permission_mode"] = proto.ModeBypass
					rctx, rcancel := context.WithTimeout(ctx, runtimeCallTimeout)
					if rerr := h.peer.Call(rctx, "session.modes", params, &res); rerr == nil && res.PermissionMode == "" {
						res.PermissionMode = proto.ModeBypass
					}
					rcancel()
				}
				if res.PermissionMode != "" && res.PermissionMode != mode && slices.Contains(permissionModes, res.PermissionMode) {
					mode = res.PermissionMode
					n.noteModes(id, mode, effort)
					n.persistModes(id, mode, effort, true)
				}
			}
		}
		if p != nil && p.alive() {
			if effort != "" {
				p.mu.Lock()
				p.model.Effort = effort
				p.mu.Unlock()
				if st, ok := n.state.get(id); ok {
					st.ModelEffort = effort
					n.state.put(id, st)
				}
			}
		} // else: applied when the next message starts the process
		n.notifySessions()
		markChanged()
		return nil
	}
	if n.codexPath(id) != "" || n.traexPath(id) != "" { // not driven through an app-server: no way to set either
		return fmt.Errorf("Codex / TRAE CLI 的会话不支持在手机上改权限模式和思考强度")
	}
	if mode == "" && effort != "" {
		if handled, err := n.ccPluginSetSettings(ctx, id, "", effort); handled {
			if err != nil {
				return err
			}
			n.noteModes(id, "", effort)
			n.notifySessions()
			markChanged()
			return nil
		}
	}
	applied := false
	if mode != "" {
		// Allow All for a claude that has no bypass flag: no key reaches it, so it is restarted in it once its turn is
		// over (mode_restart.go, #6476). Any other pick drops such a restart that waits.
		if n.needsModeRestart(id, mode) {
			if err := n.restartable(ctx, id); err == nil {
				if err := n.queueModeRestart(id, mode); err != nil {
					return err
				}
				applied = true
				mode = ""
				if effort == "" {
					markChanged()
					return nil
				}
			}
		} else {
			if n.dropModeRestart(id, mode) {
				applied = true
			}
		}
	}
	if effort != "" {
		unlock, err := n.inputTurn(ctx, id)
		if err != nil {
			if applied {
				return partialModesErr(err)
			}
			return err
		}
		defer unlock()
	}
	unlockModes := n.switches().modesInput.lock(id)
	defer unlockModes()
	pane, err := n.paneFor(ctx, id)
	if err != nil {
		if applied {
			return partialModesErr(n.noPaneForModes(id))
		}
		return n.noPaneForModes(id)
	}
	if mode != "" {
		stopDriving := n.driveModes(id)
		modeChanged, err := n.tuiSetMode(ctx, pane, mode)
		stopDriving()
		if err != nil {
			if applied || modeChanged {
				return partialModesErr(err)
			}
			return err
		}
		n.noteModes(id, mode, "")
		if modeChanged {
			applied = true
		}
	}
	if effort != "" {
		if c, ok := n.outsideClaude(id); ok && (c.Status == "busy" || c.Status == "waiting") {
			err := fmt.Errorf("Agent 正在工作，这一轮结束后再改思考强度")
			if applied {
				return partialModesErr(err)
			}
			return err
		}
		if err := n.setTerminalEffort(ctx, id, pane, effort, pending); err != nil {
			if applied {
				return partialModesErr(err)
			}
			return err
		}
		applied = true
	}
	if applied {
		markChanged()
	}
	n.notifySessions()
	return nil
}

// partialModesErr preserves an error's code and details while marking delivered="unknown", partial=true and
// retryable=false (with a partial-change summary) when an earlier change in the same session.modes request (model,
// launcher cancellation or permission mode) already took effect, so clients reconcile current settings while
// client_ops records the partial request as "unknown" instead of "failed".
func partialModesErr(err error) error {
	if err == nil {
		return nil
	}
	var re *rpc.Error
	code := ""
	origMsg := err.Error()
	var data map[string]any
	if errors.As(err, &re) {
		code = re.Code
		origMsg = re.Message
		if len(re.Data) > 0 {
			_ = json.Unmarshal(re.Data, &data)
		}
	}
	if data == nil {
		data = map[string]any{}
	}
	data["delivered"] = deliveredUnknown
	data["partial"] = true
	data["retryable"] = false
	partialKey := "rpc-errors.modes_partial_failed"
	switch code {
	case notDelivered:
		if reason, _ := data["reason"].(string); reason == "terminal_finished" {
			partialKey = "rpc-errors.modes_partial_terminal_finished"
		} else {
			partialKey = "rpc-errors.modes_partial_not_delivered"
		}
	case screen.DialogOpen:
		partialKey = "rpc-errors.modes_partial_dialog_open"
	}
	if _, hasDetail := data["detail"]; !hasDetail && origMsg != "" {
		data["detail"] = origMsg
	}
	msg := i18n.T(i18n.Default, partialKey)
	if code == "" && origMsg != "" {
		msg += "：" + origMsg
	}
	b, _ := json.Marshal(data)
	return &rpc.Error{Code: code, Message: msg, Data: b}
}

// noPaneForModes is the refusal of a mode, effort, model or ultracode change for a session with no pane to drive. A
// Claude running in Claude's desktop app or a terminal Persona does not drive is told to change it there: its peer
// inbox carries messages only and a message never gives it a pane, so "send a message first" would not help (#1843).
func (n *Node) noPaneForModes(id string) error {
	if _, outside := n.outsideClaude(id); outside {
		return refusal(proto.CodeModesOnComputer, i18n.T(i18n.Default, "rpc-errors.modes_on_computer"), nil)
	}
	return refusal(proto.CodeModesNotRunning, i18n.T(i18n.Default, "rpc-errors.modes_not_running"), nil)
}

// tuiFooterLines returns the lines under a real input box's bottom rule, or nil when no composer is visible. Two
// rules alone are not enough: question and permission dialogs draw them too, and their hints are not permission modes.
func tuiFooterLines(text string) []string {
	lines := strings.Split(text, "\n")
	end := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "──") {
			end = i
			break
		}
	}
	if end < 1 {
		return nil
	}
	start := -1
	for i := end - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "──") {
			start = i
			break
		}
	}
	if start < 0 || start+1 >= end {
		return nil
	}
	prompt := strings.TrimSpace(lines[start+1])
	if !strings.HasPrefix(prompt, "❯") && !strings.HasPrefix(prompt, "!") {
		return nil
	}
	if screen.ParseDialog(text) != nil {
		return nil
	}
	return lines[end+1:]
}

// tuiComposerVisible reports whether the input box's bottom rule is visible, even when a draft or dialog hides the
// idle hint that would identify a specific mode.
func tuiComposerVisible(screen string) bool {
	return tuiFooterLines(screen) != nil
}

// tuiPromptingScreen reports a visible composer without a recognized bypass or plan mode. Other screens, including a
// status panel that exposes no current mode, do not override transcript evidence.
func tuiPromptingScreen(text string) bool {
	return tuiComposerVisible(text)
}

// tuiMode reads the permission mode off the TUI's footer: the lines under the input box's bottom rule.
func tuiMode(screen string) string {
	for _, l := range tuiFooterLines(screen) {
		switch {
		case strings.Contains(l, "accept edits on"):
			return "acceptEdits"
		case strings.Contains(l, "plan mode on"):
			return "plan"
		case strings.Contains(l, "auto mode on"):
			return "auto"
		case strings.Contains(l, "don't ask"):
			return "dontAsk"
		case strings.Contains(l, "bypass permissions on"):
			return "bypassPermissions"
		case strings.Contains(l, "manual mode on"), strings.Contains(l, "? for shortcuts"):
			return "default"
		}
	}
	return ""
}

// tuiSetMode presses shift+tab until the footer shows the wanted mode, reporting whether any key was pressed.
func (n *Node) tuiSetMode(ctx context.Context, pane, mode string) (bool, error) {
	changed := false
	for range len(tuiModeCycle) + 1 {
		screen, err := n.readPane(ctx, pane, "visible", 60)
		if err != nil {
			return changed, err
		}
		cur := tuiMode(screen)
		if cur == mode {
			return changed, nil
		}
		if cur == "" {
			return changed, fmt.Errorf("终端里没看到 Claude Code 的输入框（可能有对话框挡着）")
		}
		if err := n.sendKeys(ctx, pane, "shift+tab"); err != nil {
			return changed, err
		}
		changed = true
		time.Sleep(350 * time.Millisecond)
	}
	if mode == "bypassPermissions" {
		return changed, fmt.Errorf("「全部放行」只能在新开 Agent 时选，运行中切不过去")
	}
	return changed, fmt.Errorf("切换权限模式没成功：终端里循环一圈也没出现 %s", mode)
}

// tuiPromptEmpty reports whether nothing is typed in the TUI's input box (the hint text on a fresh session counts as
// empty), so the /effort command is not appended to someone's draft.
func tuiPromptEmpty(screen string) bool {
	text, ok := tuiPromptText(screen)
	return ok && (text == "" || strings.HasPrefix(text, `Try "`))
}

// tuiPromptText is the text of the last `❯` row of a plain screen, with the rows under it when a rule closes them (the
// box's bottom rule: a draft that wraps or has several lines), trimmed; ok is false when no row starts with `❯`.
func tuiPromptText(screen string) (string, bool) {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		rest, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "❯")
		if !ok {
			continue
		}
		rows := []string{strings.TrimSpace(rest)}
		for j, l := range lines[i+1:] {
			if strings.HasPrefix(strings.TrimSpace(l), "──") {
				for _, r := range lines[i+1 : i+1+j] {
					rows = append(rows, strings.TrimSpace(r))
				}
				break
			}
		}
		return strings.TrimSpace(strings.Join(rows, "\n")), true
	}
	return "", false
}

// promptHasDraft: something the user typed sits in Claude's input box. Read with colors where the host can, so the
// grey next-message suggestion Claude Code shows after a turn (and its new-session tip) count as empty; a plain read
// can only tell the tip apart (tuiPromptEmpty).
func (n *Node) promptHasDraft(ctx context.Context, pane string) (bool, error) {
	_, draft, err := n.promptDraft(ctx, pane)
	return draft, err
}

// promptDraft is promptHasDraft with the box's text as shown, for telling the user what is there. A plain screen
// (host.PlainScreen: Orca) has no colors, so any text in the box is a draft, Claude's grey suggestion too: Orca's
// screen read gives the box's text without telling which part is grey (orcart.Screen.Draft).
func (n *Node) promptDraft(ctx context.Context, pane string) (string, bool, error) {
	if !n.plainScreen(pane) {
		if s, err := n.readPaneANSI(ctx, pane, "visible", 60); err == nil {
			if in := screen.ClaudeInput(s); in.Box {
				return in.Text, in.Draft, nil
			}
		}
	}
	plain, err := n.readPane(ctx, pane, "visible", 60)
	if err != nil {
		return "", false, err
	}
	text, _ := tuiPromptText(plain)
	return text, !tuiPromptEmpty(plain), nil
}

// inputDraftRefusal refuses an effort or speed change while the box holds text, nothing typed: the text is named so
// the user can find it, and on a plain screen the message says Claude's grey suggestion counts too (key is
// rpc-errors.<what>_input_draft, with _plain for that variant).
func (n *Node) inputDraftRefusal(pane, what, text string) error {
	key := "rpc-errors." + what + "_input_draft"
	if n.plainScreen(pane) {
		key += "_plain"
	}
	if r := []rune(text); len(r) > 200 {
		text = string(r[:200]) + "…"
	}
	return refusal(screen.InputDraft, i18n.T(i18n.Default, key, i18n.Args{"draft": text}), map[string]any{"draft": text})
}

// tuiSetEffort opens /effort, moves to the requested level and follows the picker's confirmation hints.
func (n *Node) tuiSetEffort(ctx context.Context, pane, effort string) error {
	text, draft, err := n.promptDraft(ctx, pane)
	if err != nil {
		return err
	}
	if draft {
		return n.inputDraftRefusal(pane, "effort", text)
	}
	if !n.canTypeText(pane) { // a host that types only whole submissions (herdr-core): the command is one
		if err := n.hostPrompt(ctx, pane, "/effort"); err != nil {
			if r := deliveryRefusal(err); r != nil {
				return r
			}
			return err
		}
	} else {
		if err := n.sendText(ctx, pane, "/effort"); err != nil {
			return err
		}
		time.Sleep(300 * time.Millisecond)
		if err := n.sendKeys(ctx, pane, "enter"); err != nil {
			return err
		}
	}
	var picker string
	var refreshConfirmKey bool
	for range 15 {
		time.Sleep(200 * time.Millisecond)
		if s, err := n.readPane(ctx, pane, "visible", 60); err == nil {
			if picker = effortPicker(s); picker != "" {
				refreshConfirmKey = effortSliderConfirmKey(s) != ""
				break
			}
		}
	}
	if picker == "" {
		_ = n.sendKeys(ctx, pane, "esc")
		return errEffortPickerNotOpen
	}
	if effort == effortAuto {
		if err := n.tuiPickDefaultEffort(ctx, pane); err != nil {
			_ = n.sendKeys(ctx, pane, "esc")
			return err
		}
	} else if err := n.sendKeys(ctx, pane, effortPickerKeys(effort)...); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond)
	confirm := "s"
	switch {
	case refreshConfirmKey:
		// The binding can change with the selected level in Claude Code 2.1.330 and later.
		s, err := n.readPane(ctx, pane, "visible", 60)
		if err != nil {
			return err
		}
		confirm = effortSliderConfirmKey(s)
		if confirm == "" {
			return errEffortPickerNotOpen
		}
	case picker == "slider":
		confirm = "enter"
	}
	if err := n.sendKeys(ctx, pane, confirm); err != nil {
		return err
	}
	for range 15 {
		time.Sleep(200 * time.Millisecond)
		s, err := n.readPane(ctx, pane, "visible", 60)
		if err == nil && effortPicker(s) == "" {
			return effortOutcome(s, effort)
		}
	}
	_ = n.sendKeys(ctx, pane, "esc")
	return fmt.Errorf("思考强度的选择器没有关闭，已取消")
}

// effortPickerKeys moves the /effort picker from wherever it opened to a level: all the way left, then right.
// From the left end (low) one stop right per level; the slider has stops past max (ultracode), so 6 lefts reach low.
func effortPickerKeys(effort string) []string {
	keys := []string{"left", "left", "left", "left", "left", "left"}
	for range slices.Index(effortPickerLevels, effort) {
		keys = append(keys, "right")
	}
	return keys
}

// tuiPickDefaultEffort walks the open /effort picker from the left until its marker is on the model's default level
// (the picker shows effortPickerDefault under it). That is auto for this session, without `/effort auto` clearing
// the saved default for new sessions; it stays at that level if the session later switches models.
func (n *Node) tuiPickDefaultEffort(ctx context.Context, pane string) error {
	if err := n.sendKeys(ctx, pane, effortPickerKeys(effortPickerLevels[0])...); err != nil {
		return err
	}
	for i := range effortPickerLevels {
		if i > 0 {
			if err := n.sendKeys(ctx, pane, "right"); err != nil {
				return err
			}
		}
		time.Sleep(200 * time.Millisecond)
		s, err := n.readPane(ctx, pane, "visible", 60)
		if err != nil {
			return err
		}
		if strings.Contains(s, effortPickerDefault) {
			return nil
		}
	}
	return fmt.Errorf("思考强度的选择器里没找到模型的默认档位（可能被组织设置覆盖了），已取消")
}

// effortPicker says which /effort picker is on screen: "list" and "session-slider" confirm for this session with "s",
// "slider" with Enter; "" is none. Claude Code 2.1.330's slider footer says "s to use this session only" where Enter
// would save the level as the default, and Enter itself says "use this session only" when that is all it does (no
// "s" then). A narrow pane wraps the footer, so it is read with the lines joined.
func effortPicker(screen string) string {
	switch effortSliderConfirmKey(screen) {
	case "s":
		return "session-slider"
	case "enter":
		return "slider"
	}

	flat := strings.Join(strings.Fields(screen), " ")
	switch {
	case strings.Contains(flat, "for this session only"):
		return "list"
	case strings.Contains(flat, "Effort") && strings.Contains(flat, "adjust") && strings.Contains(flat, "Enter to confirm"):
		return "slider"
	}
	return ""
}

var (
	effortSliderTitle  = lazyre.New(`(?m)^[\t ]*Effort[\t ]*\r?$`)
	effortSliderFooter = lazyre.New(`(?i)^←/→ (?:to )?adjust · (?:(s) (?:to )?use this session only · enter (?:to )?save|enter (?:to )?use this session only) · esc(?:ape)? (?:to )?cancel$`)
)

// effortSliderConfirmKey returns the session-only key from the active slider footer introduced in 2.1.330.
// It returns an empty string when the heading or footer is missing. Joining fields handles narrow panes.
func effortSliderConfirmKey(screen string) string {
	start := strings.LastIndex(screen, "←/→")
	if start < 0 || !effortSliderTitle.MatchString(screen[:start]) {
		return ""
	}
	match := effortSliderFooter.FindStringSubmatch(strings.Join(strings.Fields(screen[start:]), " "))
	if match == nil {
		return ""
	}
	if match[1] != "" {
		return "s"
	}
	return "enter"
}

// effortStatus matches Claude's effort indicator above the input box, even if the result text scrolled off.
var effortStatus = lazyre.New(`(?m)^[\t ]*[○◐●◉◈][\t ]+(low|medium|high|xhigh|max)[\t ]+·[\t ]+/effort[\t ]*\r?$`)

// effortOutcome reads what Claude Code printed once the picker closed. An effort set in the environment
// (CLAUDE_CODE_EFFORT_LEVEL: the env block of Claude's settings.json, or a launcher) wins over the session's: say so
// instead of claiming it changed. Auto lands on whichever level is the model's default, so any level counts.
func effortOutcome(screen, effort string) error {
	flat := strings.Join(strings.Fields(screen), " ")
	if i := strings.LastIndex(flat, "CLAUDE_CODE_EFFORT_LEVEL="); i >= 0 {
		v := strings.TrimRight(strings.Fields(flat[i+len("CLAUDE_CODE_EFFORT_LEVEL="):] + " ")[0], ".,;:")
		if v != effort && strings.Contains(flat[i:], "overrides") {
			return fmt.Errorf("这台电脑设了 CLAUDE_CODE_EFFORT_LEVEL=%s（Claude 的 settings.json 的 env 或启动器里），它优先于会话里改的值，要改得先去掉它", v)
		}
	}
	if status := effortStatus.FindStringSubmatch(screen); status != nil && (effort == effortAuto || status[1] == effort) {
		return nil
	}
	if effort == effortAuto {
		if strings.Contains(flat, "effort level to ") {
			return nil
		}
	} else if strings.Contains(flat, "effort level to "+effort) || strings.Contains(flat, "● "+effort+" ") || strings.Contains(flat, "◈ "+effort+" ") {
		return nil
	}
	return fmt.Errorf("思考强度可能没改成 %s，请在终端里确认", effort)
}

// syncAssistantModes applies the settings for a new or resumed process before its next message.
func (n *Node) syncAssistantModes(ctx context.Context, p *assistantProc) error {
	if p.cx != nil {
		return nil // App-server launchers get their settings with each turn (codexPolicy).
	}
	p.settingsMu.Lock()
	defer p.settingsMu.Unlock()
	n = p.ownerOr(n)
	mode, effort := n.modesOf(p.id)
	n.mu.Lock()
	if n.modes[p.id].Effort == effortAuto {
		// The concrete level in the transcript is what auto resolved to, not a new choice.
		effort = effortAuto
	}
	n.mu.Unlock()
	p.mu.Lock()
	if p.model.UseEffort {
		effort = p.model.Effort
	}
	p.mu.Unlock()
	return n.setAssistantModes(ctx, p, mode, effort)
}

// applyAssistantModes is setAssistantModes for a change made in the app: the list goes out with it at once.
func (n *Node) applyAssistantModes(ctx context.Context, p *assistantProc, mode, effort string) error {
	defer func() { p.ownerOr(n).notifySessions() }()
	return n.setAssistantModes(ctx, p, mode, effort, true)
}

// setAssistantModes commits each field only after Claude acknowledges it. A refused startup setting also
// restores the previously recorded choice, so the next message does not silently retry it. settingsMu is held.
// It pushes no list: the message it runs for (syncAssistantModes) has one pushed once its line is written, and
// building one here held every send up for as long as a list takes on a busy computer.
func (n *Node) setAssistantModes(ctx context.Context, p *assistantProc, mode, effort string, explicit ...bool) error {
	for _, field := range []string{"mode", "effort"} {
		p.mu.Lock()
		want, previous := mode, p.mode
		if field == "effort" {
			want, previous = effort, p.effort
		}
		restart := field == "mode" && p.needsRestartForLocked(want) || field == "effort" && p.effortNeedsRestartLocked(want)
		p.mu.Unlock()
		if want == "" || restart {
			continue
		}
		var err error
		if want != previous {
			req := map[string]any{"subtype": "set_permission_mode", "mode": want}
			if field == "effort" {
				var level any = want
				if want == effortAuto {
					level = nil
				}
				req = map[string]any{"subtype": "apply_flag_settings", "settings": map[string]any{"effortLevel": level}}
			}
			err = p.control(ctx, req)
		}
		if err != nil {
			// Startup may already have recorded both requested values. Restore every field that was not
			// accepted, including an effort request we never reached after a refused mode.
			n.recordAssistantMode(p, field, previous)
			if field == "mode" && effort != "" {
				p.mu.Lock()
				previousEffort := p.effort
				p.mu.Unlock()
				n.recordAssistantMode(p, "effort", previousEffort)
			}
			var refused *rpc.Error
			if !errors.As(err, &refused) {
				// An unconfirmed request might still apply later. Retire this process so a late response
				// cannot leave the conversation running with a setting the client was told had failed.
				var timeout *assistantControlTimeoutError
				if errors.As(err, &timeout) {
					n.logf("assistant %s: control_timeout stage=%s wait_ms=%d", short(p.id), timeout.stage, timeout.wait.Milliseconds())
				}
				p.mu.Lock()
				p.stopReason = err.Error()
				p.mu.Unlock()
				n.logf("assistant %s: control request %s failed after startup elapsed_ms=%d: %v; stopping process from node", short(p.id), field, time.Since(p.born).Milliseconds(), err)
				p.stop()
				message := i18n.T(i18n.Default, "node-assistant-start.stopped_by_node", i18n.Args{"reason": err.Error()})
				return &assistantControlUnconfirmed{error: refusal("assistant_control_failed", message, nil), cause: err}
			}
			return err
		}
		n.recordAssistantMode(p, field, want, field == "mode" && len(explicit) > 0 && explicit[0])
	}
	return nil
}

func (n *Node) recordAssistantMode(p *assistantProc, field, value string, explicit ...bool) {
	if value == "" {
		value = "default"
		if field == "effort" {
			value = effortAuto
		}
	}
	n = p.ownerOr(n)
	p.mu.Lock()
	if field == "mode" {
		p.mode = value
	} else {
		p.effort, p.model.Effort = value, value
	}
	p.mu.Unlock()
	if field == "mode" {
		n.noteModes(p.id, value, "")
		n.persistModes(p.id, value, "", explicit...)
	} else {
		n.noteModes(p.id, "", value)
		n.persistModes(p.id, "", value)
		if n.state != nil {
			if st, ok := n.state.get(p.id); ok && st.ModelEffort != value {
				st.ModelEffort = value
				n.state.put(p.id, st)
			}
		}
	}
}
