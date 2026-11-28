package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const assistantShellReady = "__PERSONA_ASSISTANT_SHELL_READY__"

// An assistant process that exits within assistantEarlyExit of starting failed to start: its last assistantStderrTail
// stderr lines go to the log.
const (
	assistantEarlyExit  = 30 * time.Second
	assistantStderrTail = 5
	assistantGroupSweep = time.Second
)

// assistantStartWait: how long a send that started a fresh process waits for its first output before answering. A
// launcher that cannot start (its node socket gone, a shell error) exits within a second or two; the message written
// to it is lost, so the send fails with what it printed instead of leaving the message at "Delivering" for good.
var assistantStartWait = 8 * time.Second

// A launcher whose command is missing for a moment is started again a few times before the send fails (#2581):
// Claude Code's auto-updater reinstalls it with npm, which moves the old install away and writes the new one about
// 2.5 s later, and a machine with old long-running claude processes reinstalls every minute or two. A start in that
// window got "command not found: claude" and the user saw "send to claude: write |1: file already closed".
var (
	assistantStartRetries    = 3
	assistantStartRetryDelay = 2 * time.Second
)

// awaitStart waits up to d for a freshly spawned process to start; one that exits first failed to start, and the
// error says what it printed. Still starting after d counts as started (a slow SessionStart hook).
func (p *assistantProc) awaitStart(d time.Duration) error {
	if p.ready == nil {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-p.ready:
		return nil
	case <-t.C:
		if proto.IsExternalLauncher(p.launcher) {
			if p.cmd != nil && p.cmd.Process != nil {
				_ = osproc.StopGroup(p.cmd.Process.Pid, true)
			}
			return externalRefusal(context.Background(), "adapter_timeout")
		}
		return nil
	case <-p.exited:
	}
	select {
	case <-p.ready: // it printed and then exited: it did start
		return nil
	default:
	}
	return p.startFailure()
}

// startFailure is the error for a fresh process that exited before it started: how long it lived and its last stderr
// lines. It waits a little for the process to exit and its stderr to be read to the end: a write to it fails as soon
// as it is gone, before the output saying why has been read.
func (p *assistantProc) startFailure() error {
	select {
	case <-p.exited:
	case <-time.After(time.Second):
	}
	if p.stderrDone != nil {
		select {
		case <-p.stderrDone:
		case <-time.After(500 * time.Millisecond):
		}
	}
	p.mu.Lock()
	tail := strings.Join(p.stderrTail, " | ")
	lines := slices.Clone(p.stderrTail)
	p.mu.Unlock()
	if proto.IsExternalLauncher(p.launcher) {
		code := "adapter_unavailable"
		if len(lines) > 0 {
			code = externalStderrCode(lines[len(lines)-1])
		}
		if code == "" {
			code = "adapter_unavailable"
		}
		return externalRefusal(context.Background(), code)
	}
	if tail == "" {
		tail = "没有输出"
	}
	return fmt.Errorf("%s 没有启动起来（%s 后退出），消息没有送达：%s", p.launcher, time.Since(p.born).Round(100*time.Millisecond), tail)
}

// missingCommand: the process exited because its shell found no such command (exit status 127, "command not found"),
// as while Claude Code is being reinstalled.
func (p *assistantProc) missingCommand() bool {
	select {
	case <-p.exited:
	default:
		return false
	}
	if p.cmd != nil && p.cmd.ProcessState != nil && p.cmd.ProcessState.ExitCode() == 127 {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.ContainsFunc(p.stderrTail, func(l string) bool { return strings.Contains(l, "command not found") })
}

// startRefusal is err, the failed start of p, coded when what is missing is plain from what it printed
// (docs/assistant/onboarding.md 4.6): Claude Code itself (claude_missing: the shell has no claude, or `personad launch` found
// none), or the launcher (launcher_missing: a ~/.zshrc function or alias that is gone). The hub says it in plain words
// and keeps err's text as the detail (apierr.Call); nothing was delivered.
func (p *assistantProc) startRefusal(err error) error {
	p.mu.Lock()
	tail := slices.Clone(p.stderrTail)
	p.mu.Unlock()
	code := missingFrom(p.launcher, tail, p.missingCommand())
	if code == "" {
		return err
	}
	data, _ := json.Marshal(map[string]string{"delivered": "none", "launcher": p.launcher})
	return &rpc.Error{Code: code, Message: err.Error(), Data: data}
}

// zshNotFound and bashNotFound find the command a shell could not find: zsh's "zsh:1: command not found: x", bash's
// "bash: x: command not found" (also "bash: line 1: x: command not found").
var (
	zshNotFound  = lazyre.New(`command not found: (\S+)`)
	bashNotFound = lazyre.New(`([^\s:]+): command not found`)
)

// missingFrom is what the start of launcher was missing by the lines it printed: proto.CodeClaudeMissing,
// proto.CodeLauncherMissing, or "" when that is not what failed. Only a missing "claude" or a missing launcher
// itself is coded, the last such line winning: the launcher runs under `zsh -ic`, so ~/.zshrc often prints
// "command not found" for other commands (compdef, nvm…) that have nothing to do with the failure. notFound: it
// exited as a missing command does (status 127); without a line naming the command, that alone codes it, unless a
// line names some other missing command (then which one failed is not plain).
func missingFrom(launcher string, lines []string, notFound bool) string {
	for _, l := range lines {
		if strings.Contains(l, "claude not found in PATH") { // claudeExecutable, from a managed launcher's personad launch
			return proto.CodeClaudeMissing
		}
	}
	code, other := "", false
	for _, l := range lines {
		word := ""
		if m, ok := parseShellMiss(l); ok { // any shell's whole line (shellmiss.go): dash, ksh and fish too
			word = m.Word
		} else if m := zshNotFound.FindStringSubmatch(l); m != nil {
			word = m[1]
		} else if m := bashNotFound.FindStringSubmatch(l); m != nil {
			word = m[1]
		}
		switch {
		case word == "":
		case filepath.Base(word) == "claude":
			code = proto.CodeClaudeMissing
		case launcher != "" && word == launcher:
			code = proto.CodeLauncherMissing
		default:
			other = true
		}
	}
	switch {
	case code != "":
		return code
	case !notFound || other:
		return ""
	case launcher == "" || launcher == "claude":
		return proto.CodeClaudeMissing
	}
	return proto.CodeLauncherMissing
}

// spawnAssistant starts the conversation's process for its launcher's agent: claude -p (spawnClaude) or codex
// app-server (spawnCodex). A new Codex conversation (id "") gets its id from Codex.
func (n *Node) spawnAssistant(id, cwd, launcher string, resume, spare bool, options ...modelExecution) (*assistantProc, error) {
	return n.spawnAssistantWith(id, cwd, launcher, resume, spare, spawnRound{}, options...)
}

// spawnAssistantWith is spawnAssistant for a round of the background supervisor too (round, assistant_round.go): a Claude
// process resumed at its anchor with the round's tools. Only Claude takes a round (superviseRound).
func (n *Node) spawnAssistantWith(id, cwd, launcher string, resume, spare bool, round spawnRound, options ...modelExecution) (*assistantProc, error) {
	done, err := n.reload.enter()
	if err != nil {
		return nil, err
	}
	defer done()
	agent := n.agentOf(launcher)
	if agent != "claude" && id != "" && (n.guestOf(id) != nil || n.guestKindOf(id) == guestMeeting) {
		// A guest (a meeting's room too) runs on Claude Code only, whatever starts its process: on a launcher that runs
		// another agent by now it would get that agent's tools and the owner's setup. assistant.send refuses it before
		// this; a compaction (assistant_compact.go) resumes a process that is not running through here alone.
		return nil, errGuestAgent
	}
	if agent == "codex" || agent == traexAgent {
		return n.spawnAssistantAppServer(agent, id, cwd, launcher, resume, spare, "", options...)
	}
	if id == "" {
		id = newUUID()
	}
	return n.spawnClaudeWith(id, cwd, launcher, resume, spare, round, options...)
}

// claudeAssistantMCPAndTools are a conversation's --mcp-config and tool permission flags.
func (n *Node) claudeAssistantMCPAndTools(id, cwd string, legacyMCP bool) []string {
	return n.claudeAssistantMCPAndToolsFor(id, cwd, legacyMCP, "")
}

// claudeAssistantMCPAndToolsFor is claudeAssistantMCPAndTools for a process with tool set toolSet ("" the assistant's
// own): a supervision round (roundToolSet) mounts only Persona's server, which asks the manager for the round's tools,
// with no extension or other MCP server (--strict-mcp-config) and only the built-ins roundBuiltinTools.
func (n *Node) claudeAssistantMCPAndToolsFor(id, cwd string, legacyMCP bool, toolSet string) []string {
	if guest := n.guestOf(id); guest != nil { // assistant_guest.go: nothing else of the below
		return n.guestMCPAndTools(id, guest, legacyMCP)
	}
	// Per-process MCP config: the assistant-only tools (ask_user, agents_*, lark_*…) and the conversation they act for.
	env := assistantMCPEnv(id, n.dir)
	if toolSet != "" {
		env["PERSONA_TOOL_SET"] = toolSet
	}
	servers := map[string]any{"persona": map[string]any{
		"command": n.bin, "args": []string{"mcp"}, "env": env,
	}}
	// The extensions' MCP servers whose command is on this machine (assistant_mcp.go).
	var allow, deny []string
	builtins := assistantBuiltinTools
	inbox := toolSet == "" && n.assistantOrigin(id) == originInbox
	if toolSet == "" {
		if inbox {
			builtins = assistantInboxBuiltinTools
		}
		var extra map[string]any
		extra, allow, deny = assistantMCPServers(n.assistantLark(context.Background()).MCPServers)
		for name, s := range extra {
			servers[name] = s
		}
	} else {
		builtins = roundBuiltinTools
	}
	mcp, _ := json.Marshal(map[string]any{"mcpServers": compat.MCPRegistrationServers(servers, legacyMCP)})
	if inbox {
		// Local Read is investigation. Network, shell and mutation tools keep their normal permission flow. Credential
		// denies are appended below as individual arguments, after the legacy comma-joined MCP rules.
		allow = append(allow, "Read")
		allow = append(allow, n.inboxReadRules()...) // inbox_policy.go
	}
	allow = append(allow, healthReportRules(cwd)...) // healthdeny.go
	toolFlags := []string{"--mcp-config", string(mcp), "--allowedTools", strings.Join(append([]string{n.assistantTools(legacyMCP)}, allow...), ","), "--tools", builtins}
	if toolSet != "" {
		toolFlags = append(toolFlags, "--strict-mcp-config")
	}
	// One --disallowedTools with a word per rule (it takes several, up to the next flag): a path may hold a comma.
	if len(deny) > 0 {
		deny = []string{strings.Join(deny, ",")}
	}
	if inbox {
		deny = append(deny, n.inboxSecretDenyRules()...)
	}
	deny = append(deny, n.healthDenyRules(cwd)...) // healthdeny.go
	if len(deny) > 0 {
		toolFlags = append(toolFlags, append([]string{"--disallowedTools"}, deny...)...)
	}
	return toolFlags
}

// assistantCLICommand is the command that runs the conversation's claude on the CLI: an interactive shell so ~/.zshrc
// defines the launcher function, "$@" passing our flags through; itself: Claude Code's executable past a `claude` alias
// or function instead (claudeItself). In the agent sandbox when it is on (agentsandbox.go). A managed launcher's shell
// is not wrapped: it runs `personad launch`, which fetches the launcher's settings outside the sandbox and wraps only
// the agent it execs (launchExec).
func (n *Node) assistantCLICommand(shell, launcher, cwd string, itself bool, args, env []string) *exec.Cmd {
	command := n.launcherCommandIn(launcher, envValue(env, "CLAUDE_CONFIG_DIR"), scriptPath)
	if itself {
		command = commandInProfile("command claude", envValue(env, "CLAUDE_CONFIG_DIR"), scriptPath)
	}
	argv := append([]string{shell, shellFlag(shell), `echo ` + assistantShellReady + `; ` + command + ` "$@"`, "persona"}, args...)
	if _, managed := n.managedLauncher(launcher); !managed {
		base := env
		argv, env, _ = wrapAgent("assistant", n.home, n.dir, cwd, settingsArgs(args),
			func() *sandbox.StatusReason { return claudeInnerReason(args, base, cwd, n.home, n.claudeConfigDirs()) }, argv, env, n.logger.Printf)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = cwd, env
	return cmd
}

func (n *Node) spawnClaude(id, cwd, launcher string, resume, spare bool, options ...modelExecution) (*assistantProc, error) {
	return n.spawnClaudeWith(id, cwd, launcher, resume, spare, spawnRound{}, options...)
}

func (n *Node) spawnClaudeWith(id, cwd, launcher string, resume, spare bool, round spawnRound, options ...modelExecution) (*assistantProc, error) {
	if round.anchor != "" && (!resume || spare || n.guestOf(id) != nil) {
		return nil, errors.New("a supervision round resumes a Claude conversation")
	}
	execution := executionOption(options)
	startCtx := context.Background()
	if proto.IsExternalLauncher(launcher) {
		startCtx = externalStartContext(execution)
		if err := startCtx.Err(); err != nil {
			return nil, externalAdapterError(startCtx, err)
		}
	}
	if err := execution.check("claude"); err != nil {
		return nil, err
	}
	resolvedModel, err := n.resolveClaudeModelArg(startCtx, launcher, execution.Model)
	if err != nil {
		return nil, err
	}
	if err := n.checkExternalAssistant(startCtx, id, launcher, execution, spare); err != nil {
		return nil, err
	}
	if err := n.prepareAssistantCwd(cwd, execution.Strict); err != nil {
		return nil, err
	}
	guest := n.guestOf(id) != nil // a guest, a meeting's room too (assistant_guest.go)
	execution.ConfigDir, err = n.assistantProfileSelection(context.Background(), id, launcher, execution.ConfigDir, resume)
	if err != nil {
		return nil, err
	}
	configDir := n.assistantConfigDir(launcher, execution.ConfigDir)
	configPath := n.assistantConfigPath(execution.ConfigDir)
	if n.guestOf(id) != nil {
		// Guests never load owner source settings; only create their private profile.
		if err := os.MkdirAll(configPath, 0o700); err != nil {
			return nil, err
		}
	} else if err := n.prepareAssistantProfile(configPath); err != nil {
		return nil, err
	}
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--include-partial-messages"}
	if execution.SupervisionInstructions != "" && !guest {
		args = append(args, "--append-system-prompt", execution.SupervisionInstructions)
	}
	// Persona's hooks: a plugin, or in --settings for a claude that loads none (hooksplugin.go).
	settings, hooks := n.claudeStartSettings(context.Background(), launcher, "assistant", cwd, configPath)
	if !guest {
		var sourceErr error
		settings, sourceErr = n.sourceClaudeSettings(startCtx, launcher, cwd, settings, configPath, sourceSession(assistantModelSource(execution), id))
		if sourceErr != nil {
			return nil, sourceErr
		}
	}
	compaction := n.assistantCompactionPolicy(launcher, cwd, settings, execution)
	if hooks == "" && n.launcherOwnSettings(context.Background(), launcher) != "" {
		compaction.ownSettings = ""
	}
	toolSearch := n.assistantToolSearch(launcher, execution.Model, compaction)
	compaction.toolSearchOff = toolSearch.forceOff
	if toolSearch.forceOff {
		n.logger.Printf("assistant %s: starts with every tool loaded: %s refused tool search for model %q", short(id), launcher, execution.Model)
	}
	// The node resolves the original upstream before substituting the local endpoint.
	// A launcher/policy overriding our final settings simply never reaches it.
	startedCacheProxy := false
	cacheProxy := n.startCacheWarm(id, launcher, compaction, execution, spare || guest || round.anchor != "")
	if cacheProxy != nil {
		compaction.cacheProxyBase = cacheProxy.BaseURL()
	}
	defer func() {
		if cacheProxy != nil && !startedCacheProxy {
			cacheProxy.Close()
		}
	}()
	var settingsErr error
	settings, settingsErr = n.assistantCompactionSettings(id, cwd, settings, compaction)
	if settingsErr != nil {
		return nil, settingsErr
	}
	if n.guestKindOf(id) == guestMeeting && !guest {
		return nil, errRoomRecord // a meeting's room never runs with the assistant's tools
	}
	if guest { // none of the owner's Claude Code setup (assistant_guest.go)
		if settings, settingsErr = n.guestSettingsFile(id, cwd, configPath, settings, hooks == ""); settingsErr != nil {
			return nil, settingsErr
		}
		settings, settingsErr = n.sourceClaudeSettings(startCtx, launcher, cwd, settings, configPath, sourceSession(assistantModelSource(execution), id))
		if settingsErr != nil {
			return nil, settingsErr
		}
		args = append(args, guestSettingArgs()...)
	}
	if settings != "" {
		args = append(args, "--settings", settings)
	}
	if hooks != "" {
		args = append(args, "--plugin-dir", hooks)
	}
	// Snapshot the connection capability once for the MCP config and its permission lists. Historical tool_use
	// blocks under the old namespace remain valid on a resume with only Persona offered (verified on Claude Code 2.1.322);
	// an old hub that still addresses the legacy namespace keeps the alias.
	legacyMCP := !n.hubSupportsPersonaContracts()
	args = append(args, n.claudeAssistantMCPAndToolsFor(id, cwd, legacyMCP, round.toolSet)...)
	model := resolvedModel
	if execution.Model == "" && resume && execution.Fingerprint != "" && !guest {
		// The model configuration names none: the launcher's default, not the model the session ran on last
		// (assistant_resume_model.go). A guest keeps what it started on.
		model = n.claudeModelArg(launcher, n.resumeModel(launcher, cwd, settings, configPath))
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	// bypassPermissions only takes as a flag at start; the other modes are set with a control request (syncAssistantModes).
	var startMode, wantMode string
	if !spare {
		if wantMode, _ = n.modesOf(id); wantMode == proto.ModeBypass {
			startMode = wantMode
			args = append(args, "--permission-mode", wantMode)
		}
	}
	if resume {
		args = append(args, "--resume", id)
		if round.anchor != "" { // the end of its first turn (assistant_round.go)
			args = append(args, "--resume-session-at", round.anchor)
		}
	} else {
		args = append(args, "--session-id", id)
	}
	// Tool search by where the requests go (toolSearchFor, #10365): personad mcp marks the Persona tools used all the
	// time alwaysLoad, the rest and other MCP servers' tools wait behind ToolSearch (#5838, #7317). On Anthropic's own
	// API, ENABLE_TOOL_SEARCH=true lifts Claude Code's hold-off for a conversation that ran without ToolSearch (one from
	// before #5839), which would otherwise load every tool until it is compacted. Behind a gateway true would also lift
	// Claude Code's own rule that keeps tool search off there, so a gateway gets false (every tool up front), and a
	// start that cannot tell where its requests go gets nothing: Claude Code decides, as before #7371. A value the
	// node inherited never reaches claude.
	startEnv := []string{"PERSONA_ASSISTANT=1"}
	if toolSearch.env != "" {
		startEnv = append(startEnv, toolSearchEnv+"="+toolSearch.env)
	}
	env := identity.Environment(withoutEnv(os.Environ(), []string{toolSearchEnv, categoriesEnv}), append(startEnv, compaction.env()...)...)
	env = n.claudeProcessEnv(env, configDir)
	if cacheProxy != nil {
		env = identity.Environment(env, "ANTHROPIC_BASE_URL="+cacheProxy.BaseURL())
	}
	gateEnv, releaseGates := n.assistantLarkEnv(cwd, id) // its addresses at the gate proxy go with the process
	if guest {
		releaseGates()
		gateEnv, releaseGates = nil, func() {} // no commands to gate: it has no Bash
	}
	categories := categoriesFromEnv(gateEnv)
	cleanupMCP := func() {}
	cleanupAdapter := func() {}
	started := false
	defer func() {
		if !started {
			cleanupMCP()
			cleanupAdapter()
			releaseGates()
		}
	}()
	env = identity.Environment(env, gateEnv...)
	shell, err := interactiveShell()
	if err != nil {
		return nil, err
	}
	args, cleanupMCP, err = assistantCLIMCPFlags(args)
	if err != nil {
		return nil, fmt.Errorf("prepare assistant MCP config: %w", err)
	}
	itself := resume && n.claudeItself(id, launcher)
	if itself {
		n.logger.Printf("assistant %s: started on the Agent SDK: resumes with claude itself in its recorded profile, not the claude launcher", short(id))
	}
	var cmd *exec.Cmd
	// Interactive Claude Code in place of claude -p when the setting asks for it (assistant_tui.go): the bridge gets
	// both commands, and runs the print-mode one when the interactive one does not start. Not for an external
	// launcher, whose adapter is the command.
	var tui bool
	var tuiNote, tuiMode, tuiEffort string
	var tuiLine []byte
	if proto.IsExternalLauncher(launcher) {
		cmd, cleanupAdapter, err = n.externalCLICommand(startCtx, id, launcher, cwd, execution, args, env)
		if err != nil {
			cleanupAdapter = func() {}
			return nil, err
		}
	} else {
		cmd = n.assistantCLICommand(shell, launcher, cwd, itself, args, env)
		if tui, tuiNote = tuiWanted(execution, guest, spare, round, wantMode); tui {
			tuiMode, tuiEffort = n.tuiStartModes(id, wantMode, execution)
			full := withStartModes(args, tuiMode, tuiEffort)
			interactive := n.assistantCLICommand(shell, launcher, cwd, itself, tuiArgs(full), env)
			printCmd := n.assistantCLICommand(shell, launcher, cwd, itself, full, env)
			cmd, tuiLine = n.assistantTUICommand(tuiSpec{Session: id, Cwd: cwd, Resume: resume, AcceptBypass: tuiMode == proto.ModeBypass,
				ConfigDirs: slices.Compact(append([]string{envValue(env, "CLAUDE_CONFIG_DIR")}, n.claudeConfigDirs()...))}, interactive, printCmd)
		}
	}
	cmd.SysProcAttr = osproc.GroupAttr() // so stop() can kill the whole group
	msysCmdLine(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, closeStderrWriter, err := assistantStderrPipe(cmd)
	if err != nil {
		return nil, err
	}
	instructions := instructionsDigest(cwd)
	var startErr error
	if proto.IsExternalLauncher(launcher) {
		startErr = startExternalCommand(startCtx, cmd, stdin)
	} else {
		startErr = cmd.Start()
	}
	closeStderrWriter()
	if startErr != nil {
		_ = stderr.Close()
		if proto.IsExternalLauncher(launcher) {
			return nil, externalAdapterError(startCtx, startErr)
		}
		return nil, fmt.Errorf("start %s: %w", launcher, startErr)
	}
	started = true
	if tuiLine != nil {
		// The bridge reads its spec before anything else; one that cannot take it is a start that failed (awaitStart).
		_, _ = stdin.Write(tuiLine)
	}
	processExecution := execution
	processExecution.StartupContext = nil // a live or resident process must never keep the completed RPC's context
	// Effort is deliberately unconfirmed here: --effort cannot express auto and
	// launcher environment/policy can override a flag. syncAssistantModes must
	// receive its ACK before the user's first input, just as for live changes.
	p := &assistantProc{cacheProxy: cacheProxy, id: id, agent: "claude", launcher: launcher, cwd: cwd, cmd: cmd, stdin: stdin, exited: make(chan struct{}), ready: make(chan struct{}),
		stderrDone: make(chan struct{}), born: time.Now(), lastActive: time.Now(), mode: startMode, canBypass: startMode == proto.ModeBypass, model: processExecution, fingerprint: n.executionFingerprint(launcher, processExecution),
		instructions: instructions, personaMCPOnly: !legacyMCP, categories: categories, guest: guest, origin: execution.Origin, compactIdleAt: compaction.idle(), declaredWindow: compaction.declared(),
		round: round.anchor != "", toolSet: round.toolSet, toolSearch: toolSearch, endpoint: compaction.endpoint, tui: tui, tuiNote: tuiNote}
	startedCacheProxy = true
	if cacheProxy != nil {
		n.cacheWarmPool().put(id, cacheProxy, launcher, processExecution, n.executionFingerprint(launcher, modelExecution{}))
	}
	if tui {
		p.mode, p.effort = tuiMode, tuiEffort // its start flags: nothing to set by control request (syncAssistantModes)
	}
	p.owner.Store(n)
	liveAssistants.Store(p, struct{}{})
	if !spare {
		n.mu.Lock()
		n.procs[id] = p
		n.mu.Unlock()
	}
	go watchAssistantStderr(n.logger, p, stderr)
	go n.readAssistant(p, stdout)
	go func() {
		_ = cmd.Wait()
		if cacheProxy != nil {
			cacheProxy.CloseListener()
		}
		if proto.IsExternalLauncher(launcher) {
			if !osproc.SweepGroup(cmd.Process.Pid, externalAdapterGroupSweep) {
				n.logger.Printf("assistant %s: external process group cleanup budget exhausted", short(id))
			}
		} else if p.stopped.Load() {
			osproc.SweepGroup(cmd.Process.Pid, assistantGroupSweep)
		}
		cleanupMCP()
		cleanupAdapter()
		releaseGates()
		liveAssistants.Delete(p)
		close(p.exited)
		if n.keeper != nil {
			n.keeper.forget(p) // exited between two connections
		}
		n := p.ownerOr(n)
		n.mu.Lock()
		wasSpare := n.spare == p
		if wasSpare {
			n.spare = nil
		}
		var recovery *assistantTurnRecovery
		if n.procs[id] == p {
			delete(n.procs, id)
			recovery = n.recoveries[id] // a current process that exits without a result must not leave a working placeholder
		}
		n.mu.Unlock()
		if wasSpare {
			return
		}
		if recovery != nil {
			n.finishAssistantRecoveryOnExit(p, recovery)
			return
		}
		n.emitDelta(id, "")
		n.notifySessions()
	}()
	if proto.IsExternalLauncher(launcher) {
		ctx, cancel := context.WithTimeout(startCtx, 5*time.Second)
		err := p.control(ctx, map[string]any{"subtype": "initialize"})
		cancel()
		if err != nil || startCtx.Err() != nil {
			_ = osproc.StopGroup(cmd.Process.Pid, true)
			// The same waiter owns descendant cleanup and private settings. Bound the join; an exhausted budget is
			// unknown cleanup evidence, never a reason to claim the process group is empty.
			select {
			case <-p.exited:
			case <-time.After(externalAdapterGroupSweep + externalAdapterPipeGrace):
				n.logger.Printf("assistant %s: external initialization cleanup budget exhausted", short(id))
			}
			if startCtx.Err() != nil {
				return nil, externalAdapterError(startCtx, startCtx.Err())
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, externalRefusal(startCtx, "adapter_timeout")
			}
			return nil, p.startFailure()
		}
	}
	return p, nil
}

func assistantStderrPipe(cmd *exec.Cmd) (io.ReadCloser, func(), error) {
	if cmd.Stderr != nil {
		return nil, nil, errors.New("exec: Stderr already set")
	}
	if cmd.Process != nil {
		return nil, nil, errors.New("exec: StderrPipe after process started")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = w
	return r, func() { _ = w.Close() }, nil
}

// watchAssistantStderr logs an assistant process's error lines, and, when its stderr ends (it exited) within
// assistantEarlyExit of born, its last lines whatever they say: it did not start. WSL's "open terminal failed: not a
// terminal" has no "error" in it, and the assistant stayed silent with nothing in any log (#1994).
// The tail is kept on the process too (without blank lines and terminal colors): a send that started it reports it when
// it exits before starting (awaitStart).
func watchAssistantStderr(logger *log.Logger, p *assistantProc, stderr io.ReadCloser) {
	id, launcher, born := p.id, p.launcher, p.born
	if p.stderrDone != nil {
		defer close(p.stderrDone)
	}
	defer stderr.Close()
	var tail []string
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		l := sc.Text()
		if proto.IsExternalLauncher(launcher) {
			l = externalStderrCode(l)
			if l == "" {
				continue
			}
		}
		if strings.Contains(l, "rror") {
			logger.Printf("assistant %s: %s", id[:8], l)
		}
		if clean := strings.TrimSpace(ansiRe.ReplaceAllString(l, "")); clean != "" {
			if tail = append(tail, clean); len(tail) > assistantStderrTail {
				tail = tail[1:]
			}
			p.mu.Lock()
			p.stderrTail = append([]string(nil), tail...)
			p.mu.Unlock()
		}
	}
	if err := sc.Err(); err != nil {
		logger.Printf("assistant %s: stderr read: %v", id[:8], err)
		_, _ = io.Copy(io.Discard, stderr)
	}
	p.mu.Lock()
	stopReason := p.stopReason
	p.mu.Unlock()
	if stopReason != "" {
		logger.Printf("assistant %s: stopped by node after %s: %s", short(id), time.Since(born).Round(time.Millisecond), stopReason)
		return
	}
	if lived := time.Since(born); lived < assistantEarlyExit && len(tail) > 0 {
		logger.Printf("assistant %s: %s exited %s after it started: %s", id[:8], launcher, lived.Round(time.Millisecond),
			strings.Join(tail, " | "))
	}
}

// ansiRe: terminal color and style sequences (an interactive zsh's prompt plugins print them on stderr).
var ansiRe = lazyre.New(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
