package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/osproc"
)

// codexArgs are app-server's own flags: the persona MCP server (its session is the conversation's key until the
// thread exists) with a timeout long enough for ask_user, nothing else; sandbox and approvals go with each thread
// and turn.
func codexArgs(bin, key string, windows ...int) []string {
	return codexArgsForHome(bin, key, "", true, windows...)
}

func (n *Node) codexAssistantArgs(key, home, cwd string, windows ...int) ([]string, bool) {
	personaMCPOnly := n.hubSupportsPersonaContracts()
	args := codexArgsForHome(n.bin, key, home, !personaMCPOnly, windows...)
	if assistantskills.WorkGuideReady(cwd) {
		// A discovery in the current turn changes which original MCP tools Codex can call.
		args = append(args, "-c", "features.mcp_tool_list_changed=true")
	}
	return args, personaMCPOnly
}

// codexArgsForHome: windows[0] is a compact limit to pass (a managed launcher's declared one, #3066, capped for the
// main conversation, #7314); none or 0 passes none, and Codex compacts by its config or its model's default.
func codexArgsForHome(bin, key, home string, legacyMCP bool, windows ...int) []string {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) } // a JSON string is a TOML basic string
	mcpEnv := "{PERSONA_ASSISTANT=" + q("1") + ",PERSONA_SESSION_ID=" + q(key)
	if home != "" {
		mcpEnv += ",PERSONA_HOME=" + q(home)
	}
	mcpEnv += "}"
	args := []string{
		"-c", "mcp_servers.persona.command=" + q(bin),
		"-c", `mcp_servers.persona.args=["mcp"]`,
		"-c", "mcp_servers.persona.env=" + mcpEnv,
		"-c", fmt.Sprintf("mcp_servers.persona.tool_timeout_sec=%d", codexToolTimeout),
		"-c", "mcp_servers.persona.startup_timeout_sec=30",
		// Commands get the process's environment like Claude's Bash does: the default excludes (*KEY*, *TOKEN*…)
		// would strip the credentials of a gate that still puts them there (auth env, gateproxy.go), and its command
		// would fail.
		"-c", "shell_environment_policy.inherit=" + q("all"),
		"-c", "shell_environment_policy.ignore_default_excludes=true",
		// The assistant's memory is the one the app's "Memory" page shows, shared with Claude (codexMemoryPrompt); Codex's
		// own (~/.codex/memories) is one store for every Codex on the machine, so it stays off here.
		"-c", "features.memories=false",
		// The ChatGPT apps of a ChatGPT sign-in (codex_apps: every connector of that account) are declared in full in
		// every request, inside code mode's exec tool: measured at ~140k tokens of a ~195k request on one install, for
		// tools the assistant never calls (it has Persona's own). Off here; the machine's own Codex keeps them (#10275).
		"-c", "features.apps=false",
	}
	if len(windows) > 0 && windows[0] > 0 {
		args = append(args, "-c", fmt.Sprintf("model_auto_compact_token_limit=%d", windows[0]))
	}
	if legacyMCP {
		return compat.CodexMCPAliases(args)
	}
	return args
}

// codexShell runs the launcher with `codex` turned into `codex <its flags> app-server <ours>`: the launcher's
// function sets what it sets (env, -c model_provider, -m …) exactly as when it starts the TUI.
// Use alias-safe definitions and calls: zsh expands aliases while parsing the script.
func codexShell(launcher string) string { return codexShellWith(launcher, "app-server") }

// codexShellWith is codexShell for another subcommand of the launcher's codex (`debug models`,
// assistant_fallback_window.go).
func codexShellWith(launcher, sub string) string {
	return `__persona_args=("$@")
function __persona_codex {
  if [[ -n "${PERSONA_MODEL_SOURCE_BIN-}" && -n "${PERSONA_MODEL_SOURCE_HOSTS-}" ]]; then
    "${PERSONA_MODEL_SOURCE_BIN}" model-source codex "$@" ` + sub + ` "${__persona_args[@]}"
  else
    builtin command codex "$@" ` + sub + ` "${__persona_args[@]}"
  fi
}
if [[ -n "${ZSH_VERSION-}" ]] && (( $+functions[codex] )); then
  # Keep a user-defined codex() launcher intact while routing its final command through the app-server wrapper.
  # Copy it through the functions table: functions -c needs zsh 5.8, and a subscript never expands aliases.
  functions[__persona_user_codex]=$functions[codex]
  function codex { __persona_user_codex "$@"; }
else
  function codex { __persona_codex "$@"; }
fi
function command { if [[ "$1" == codex ]]; then shift; __persona_codex "$@"; else builtin command "$@"; fi; }
` + launcher
}

func (n *Node) spawnCodex(id, cwd, launcher string, resume, spare bool, options ...modelExecution) (*assistantProc, error) {
	return n.spawnCodexWithMode(id, cwd, launcher, resume, spare, "", options...)
}

// codexAssistantCommand is the command that runs the Codex assistant's app-server with launcher in shell.
func (n *Node) codexAssistantCommand(shell, launcher string, args []string) *exec.Cmd {
	env := identity.Environment(withoutEnv(os.Environ(), []string{categoriesEnv}), "PERSONA_ASSISTANT=1")
	var script string
	if provider, ok := n.managedLauncher(launcher); ok && provider.AgentType() == "codex" {
		catalog, _ := n.codexCatalogFor(provider)
		managed := managedEnv(provider)
		prefix := launchArgv("codex", nil, provider.Args, launcherDefaultEffort(provider), provider.Model, managed, catalog)
		// Keep the assistant's MCP and provider flags before app-server. Running
		// personad launch here bypasses the shell's codex wrapper and starts a TUI.
		args = append(append(prefix[1:], args...), "app-server")
		script = `if [[ -n "${PERSONA_MODEL_SOURCE_BIN-}" && -n "${PERSONA_MODEL_SOURCE_HOSTS-}" ]]; then "${PERSONA_MODEL_SOURCE_BIN}" model-source codex "$@"; else builtin command codex "$@"; fi`
		env = identity.Environment(withoutEnv(env, envClearFor(provider)), managed...)
		var proxies []string
		for _, assignment := range managed {
			key, _, _ := strings.Cut(assignment, "=")
			if len(otherSpelling(key)) > 0 {
				proxies = append(proxies, assignment)
			}
		}
		if len(proxies) > 0 {
			// The interactive shell can export a proxy or bypass list of its own. Restore the managed values after its rc files.
			carriers, extraEnv := codexShellEnvCarriers(proxies)
			env = append(env, extraEnv...)
			args = append(carriers, args...)
			script = codexShellRestoreEnv + script
		}
	} else {
		script = codexShell(n.launcherForScript(launcher))
	}
	cmd := exec.Command(shell, append([]string{shellFlag(shell), script, "persona"}, args...)...)
	cmd.Env = env
	return cmd
}

func (n *Node) spawnCodexWithMode(id, cwd, launcher string, resume, spare bool, mode string, options ...modelExecution) (*assistantProc, error) {
	return n.spawnAssistantAppServer("codex", id, cwd, launcher, resume, spare, mode, options...)
}

func (n *Node) spawnAssistantAppServer(agent, id, cwd, launcher string, resume, spare bool, mode string, options ...modelExecution) (*assistantProc, error) {
	execution := executionOption(options)
	if agent == traexAgent && (spare || execution.Origin != originInbox || !execution.OriginKnown) {
		return nil, errTraexAssistantOrigin
	}
	if err := execution.check(agent); err != nil {
		return nil, err
	}
	if err := n.prepareAssistantCwd(cwd, execution.Strict); err != nil {
		return nil, err
	}
	root, memDir := n.codexDirs(cwd)
	key := "proc-" + randHex(8)
	shell, err := interactiveShell()
	if err != nil {
		return nil, err
	}
	compaction := n.assistantCompactionPolicy(launcher, cwd, "", execution)
	args, personaMCPOnly := n.codexAssistantArgs(key, n.dir, cwd, compaction.codexLimit())
	if instructions := n.assistantCompactInstructions(id); instructions != "" {
		args = append(args, codexCompactPromptArgs(instructions)...)
	}
	var cmd *exec.Cmd
	if agent == traexAgent {
		args, personaMCPOnly = n.traexAssistantArgs(key, execution.Effort)
		cmd = n.traexAssistantCommand(shell, launcher, args)
	} else {
		cmd = n.codexAssistantCommand(shell, launcher, args)
	}
	cmd.Dir = root
	gateEnv, releaseGates, nameGates := n.assistantLarkEnvNamed(cwd, id) // its addresses at the gate proxy go with the process
	categories := categoriesFromEnv(gateEnv)
	started := false
	defer func() {
		if !started {
			releaseGates()
		}
	}()
	cmd.Env = identity.Environment(cmd.Env, gateEnv...)
	cmd.Env = identity.Environment(cmd.Env, modelSourceEnvironment(sourceSession(assistantModelSource(execution), id), n.bin)...)
	cmd.SysProcAttr = osproc.SessionGroupAttr()
	msysCmdLine(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	instructions := instructionsDigest(cwd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", launcher, err)
	}
	started = true
	p := &assistantProc{id: id, agent: agent, launcher: launcher, cwd: cwd, cmd: cmd, stdin: stdin, exited: make(chan struct{}),
		born: time.Now(), lastActive: time.Now(), model: execution, fingerprint: n.executionFingerprint(launcher, execution),
		instructions: instructions, personaMCPOnly: personaMCPOnly, categories: categories, origin: execution.Origin}
	c := &codexConn{n: n, p: p, key: key, memDir: memDir, pending: map[int64]chan codexReply{}, asks: map[string]context.CancelFunc{}, files: map[string][]string{},
		nameGates: nameGates}
	p.cx = c
	n.mu.Lock()
	n.akeys[key] = p
	n.mu.Unlock()
	go logLines(stderr, func(l string) {
		if strings.Contains(l, "ERROR") && !strings.Contains(l, "unrecognized configuration") {
			n.logger.Printf("assistant %s %s: %s", agent, key, l)
		}
	})
	p.owner.Store(n)
	liveAssistants.Store(p, struct{}{})
	go c.read(stdout)
	go func() {
		_ = cmd.Wait()
		if p.stopped.Load() {
			osproc.SweepGroup(cmd.Process.Pid, assistantGroupSweep)
		}
		releaseGates()
		c.mu.Lock()
		c.closed = true
		for _, cancel := range c.asks {
			cancel()
		}
		c.mu.Unlock()
		liveAssistants.Delete(p)
		close(p.exited)
		if n.keeper != nil {
			n.keeper.forget(p)
		}
		n := p.ownerOr(n)
		n.mu.Lock()
		delete(n.akeys, key)
		wasSpare := n.spare == p
		if wasSpare {
			n.spare = nil
		}
		var recovery *assistantTurnRecovery
		if p.id != "" && n.procs[p.id] == p {
			delete(n.procs, p.id)
			recovery = n.recoveries[p.id] // do not confuse a replaced process's late exit with the current one
		}
		n.mu.Unlock()
		if wasSpare || p.id == "" {
			return
		}
		if recovery != nil {
			n.finishAssistantRecoveryOnExit(p, recovery)
			return
		}
		n.emitDelta(p.id, "")
		n.notifySessions()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), codexStartTimeout)
	defer cancel()
	if err := c.start(ctx, id, root, resume, mode); err != nil {
		p.stop()
		return nil, fmt.Errorf("%s（%s app-server）没有启动起来: %w", launcher, agent, err)
	}
	if !spare {
		n.mu.Lock()
		n.procs[p.id] = p
		n.mu.Unlock()
	}
	return p, nil
}

// codexDirs is the workspace and the memory directory (made if missing) as Codex gets them, symlinks resolved: both
// are writable roots, and its macOS sandbox refuses one with a symlink in it (~/.persona → .persona, a ~/.claude kept in
// dotfiles), turning every command down before it runs. Only Codex sees the real paths; the session keeps its cwd.
func (n *Node) codexDirs(cwd string) (root, memDir string) {
	memDir = n.memoryForUse(cwd)
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		n.logger.Printf("assistant codex: memory dir: %v", err)
	}
	return n.realDir(cwd), n.realDir(memDir)
}

func (n *Node) realDir(dir string) string {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		n.logger.Printf("assistant codex: resolve %s: %v", dir, err)
		return dir
	}
	return real
}

// Codex uses this prompt for both manual and automatic compaction; JSON strings are valid TOML basic strings.
func codexCompactPromptArgs(instructions string) []string {
	raw, _ := json.Marshal(instructions)
	return []string{"-c", "compact_prompt=" + string(raw)}
}
