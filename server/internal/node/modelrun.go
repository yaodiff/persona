package node

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/aiwire"
	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/shellcmd"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// model.run (docs/assistant/model-config.md "Running API purposes on a launcher", #5823, #12027): an API purpose's
// request answered by a launcher's CLI on this computer, with the user's own login here. Supports Claude Code
// (`claude -p`), Codex (`codex exec`), and Antigravity (`agy`). Every run executes headless, once, inside a fresh temp
// directory (`persona-check-*`) that is deleted afterwards, with strict zero-tool, zero-MCP, zero-hook, and
// no-session-persistence isolation. If an installed CLI lacks required isolation flags, the run fails closed instead of
// falling back to an unisolated invocation. The prompt and the answer are never logged.

// modelRunTimeout bounds a run when the manager names no time, and modelRunMax the most it may name, should its
// rpc.cancel never come.
const (
	modelRunTimeout = 2 * time.Minute
	modelRunMax     = 10 * time.Minute

	// agyModelRunAgentName is the workspace-scoped custom agent written inside the temporary directory for each agy
	// model.run invocation. Its frontmatter clears all tools, MCP servers, skills, plugins, and shell execution.
	agyModelRunAgentName = "persona-model-run"
)

// modelRunSlots: at most this many runs at a time on this computer; the others wait within their own time.
// ponytail: a fixed two, a setting if someone's computer takes more.
var modelRunSlots = make(chan struct{}, 2)

// An API purpose's request answered by a launcher here; the handler zeroes the params it is handed.
func init() { nodeRPC(proto.MethodModelRun, (*Node).modelRun) }

// modelRun answers model.run. params are zeroed once read.
func (n *Node) modelRun(ctx context.Context, params json.RawMessage) (any, error) {
	var p proto.ModelRunParams
	err := json.Unmarshal(params, &p)
	clear(params)
	if err != nil {
		return nil, errors.New("model.run: bad params") // the decoder's error is not repeated: it may quote them
	}
	if p.Launcher != "claude" && !shellcmd.NameRe.MatchString(p.Launcher) {
		return nil, fmt.Errorf("model.run: invalid launcher %q", p.Launcher)
	}
	if err := n.runnableLauncher(ctx, p.Launcher, p.Configured); err != nil {
		return nil, err // a word that is no launcher here would run as a command
	}
	if err := (modelExecution{Model: p.Model}).checkModel(); err != nil {
		return nil, err
	}
	timeout := modelRunTimeout
	if p.TimeoutMS > 0 {
		timeout = min(time.Duration(p.TimeoutMS)*time.Millisecond, modelRunMax)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	timedOut := &rpc.Error{Code: proto.CodeModelRunTimeout, Message: fmt.Sprintf("model.run: no answer in %s", timeout.Round(time.Second))}
	select {
	case modelRunSlots <- struct{}{}:
		defer func() { <-modelRunSlots }()
	case <-ctx.Done():
		return nil, timedOut
	}
	agent := n.launcherAgent(ctx, p.Launcher)
	_, managed := n.managedLauncher(p.Launcher)
	rt := n.runtimeOf(p.Launcher)
	builtinAgyBacked := false
	// Permit runtime-backed execution only for the verified built-in Antigravity companion (which wraps the local `agy`
	// CLI directly). Refuse all other non-managed runtimes (external agy runtimes, Paseo, DSH, etc.) so model.run
	// never silently executes on a different local account or unsupported runtime backend.
	if rt != nil && !managed {
		if agent != agyAgent || !n.isBuiltinAgyRuntime(rt) {
			return nil, fmt.Errorf("model.run: %s runs on runtime %s, which can't answer these requests: choose a Claude Code, Codex, or Antigravity launcher", p.Launcher, rt.label())
		}
		builtinAgyBacked = true
	} else if agent != "claude" && agent != "codex" && agent != agyAgent {
		return nil, fmt.Errorf("model.run: %s runs %s, which can't answer these requests: choose a Claude Code, Codex, or Antigravity launcher", p.Launcher, agent)
	}
	if err := validModesFor(agent, "", p.Effort); err != nil {
		return nil, err
	}
	switch agent {
	case "claude":
		dir, err := mkModelRunTempDir()
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		return n.modelRunClaude(ctx, p, dir, timedOut)
	case "codex":
		dir, err := mkModelRunTempDir()
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		return n.modelRunCodex(ctx, p, dir, timedOut)
	case agyAgent:
		return n.modelRunAgy(ctx, p, builtinAgyBacked, timedOut)
	default:
		return nil, fmt.Errorf("model.run: unsupported agent %q", agent)
	}
}

func (n *Node) isBuiltinAgyRuntime(h *runtimeConn) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	builtin := h.ready && h.daemon == "" && h.builtinAgy && h.hello.Runtime == agyRuntimeID
	inst := h.hello.Instance
	h.mu.Unlock()
	if !builtin {
		return false
	}
	if n.runtimeHost != nil {
		if cur := n.runtimeHost.builtinAgyInstance(); cur != "" && cur != inst {
			return false
		}
	}
	return true
}

func (n *Node) builtinAgyExecutable() string {
	if n.runtimeHost != nil {
		if bin := n.runtimeHost.builtinAgyExecutable(); bin != "" {
			return bin
		}
	}
	return findAgyExecutable()
}

func mkModelRunTempDir() (string, error) {
	return os.MkdirTemp("", launcherCheckDir) // a check's name: the session list drops what a run leaves (launchercheck.go)
}

func writeTempFile(dir, name, text string) (string, error) {
	path := filepath.Join(dir, name)
	return path, os.WriteFile(path, []byte(text), 0o600)
}

// modelRunClaude executes a single isolated turn via `claude -p`.
func (n *Node) modelRunClaude(ctx context.Context, p proto.ModelRunParams, dir string, timedOut error) (proto.ModelRunAnswer, error) {
	defer n.removeCheckProject(dir)
	var b strings.Builder
	// -p: headless non-interactive print mode (reads prompt from stdin, exits after completion).
	b.WriteString(" -p")
	// --output-format json: emits a structured JSON envelope with result text, modelUsage, and token usage.
	b.WriteString(" --output-format json")
	// --max-turns 1: hard cap at a single model turn so no multi-step agent loop can occur.
	b.WriteString(" --max-turns 1")
	// --no-session-persistence: prevents writing session transcripts to disk or polluting user session history.
	b.WriteString(" --no-session-persistence")
	// --tools '': disables every built-in Claude Code tool (Bash, Read, Write, Edit, WebFetch, etc.).
	b.WriteString(" --tools ''")
	// --strict-mcp-config: ignores user/project MCP server configs so zero MCP tools are loaded.
	b.WriteString(" --strict-mcp-config")
	if p.System != "" {
		// --system-prompt-file: loads the system prompt from a 0600 temp file instead of exposing it on argv.
		path, err := writeTempFile(dir, "system.md", p.System)
		if err != nil {
			return proto.ModelRunAnswer{}, err
		}
		b.WriteString(" --system-prompt-file " + scriptPath(path))
	}
	if p.Model != "" {
		b.WriteString(" --model " + shellQuote(p.Model))
	}
	if p.Effort != "" {
		b.WriteString(" --effort " + p.Effort)
	}
	// disableAllHooks: prevents user/project/Persona lifecycle hooks from executing during a background API run.
	ours, err := writeTempFile(dir, "settings.json", `{"disableAllHooks":true}`)
	if err != nil {
		return proto.ModelRunAnswer{}, err
	}
	settings, err := n.sourceClaudeSettings(ctx, p.Launcher, dir, n.claudeSettings(ctx, p.Launcher, ours, dir), "", modelhttp.Source{Role: "background"})
	if err != nil {
		return proto.ModelRunAnswer{}, err
	}
	b.WriteString(scriptSettingsFlag(settings))
	in, err := writeTempFile(dir, "prompt.md", p.Prompt)
	if err != nil {
		return proto.ModelRunAnswer{}, err
	}
	cmd := loginShell(ctx, "cd "+scriptPath(dir)+" && "+n.launcherForScript(p.Launcher)+b.String()+" < "+scriptPath(in))
	killGroupOnCancel(cmd)
	raw, runErr := cmd.CombinedOutput() // claude -p repeats no prompt: what it prints is its result or its error
	text := strings.TrimSpace(string(raw))
	if ctx.Err() != nil {
		return proto.ModelRunAnswer{}, timedOut
	}
	if r, ok := claudeResult(text); ok && !r.isError {
		return proto.ModelRunAnswer{Text: r.result, Model: r.model, Usage: r.usage}, nil
	} else if ok {
		text = r.result
	}
	if low := strings.ToLower(text); strings.Contains(low, "not logged in") || strings.Contains(low, "run /login") {
		return proto.ModelRunAnswer{}, &rpc.Error{Code: proto.CodeModelRunLoggedOut, Message: "model.run: " + p.Launcher + " is not logged in here"}
	}
	if isIsolationFlagError(text) {
		return proto.ModelRunAnswer{}, fmt.Errorf("model.run: %s CLI does not support required isolation flags; update %s: %s", p.Launcher, p.Launcher, checkOutput(text))
	}
	msg := checkOutput(text)
	if msg == "" && runErr != nil {
		msg = clipText(redactCheck(runErr.Error()), 400)
	}
	return proto.ModelRunAnswer{}, errors.New("model.run: " + msg)
}

// tomlString formats s as a TOML basic string (JSON string escaping is a strict subset of TOML basic strings).
func tomlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// modelRunCodex executes a single isolated turn via `codex exec`.
func (n *Node) modelRunCodex(ctx context.Context, p proto.ModelRunParams, dir string, timedOut error) (proto.ModelRunAnswer, error) {
	var b strings.Builder
	// exec: non-interactive single-task execution subcommand.
	b.WriteString(" exec")
	// --ephemeral: disables writing rollout/session files to $CODEX_HOME/sessions so runs leave no persisted session
	// history (and also prevents sub-agent spawning, which requires a persisted rollout).
	b.WriteString(" --ephemeral")
	// --ignore-user-config: skips $CODEX_HOME/config.toml (while still using $CODEX_HOME auth) so user-configured MCP
	// servers, custom tools, instructions, and hooks are never inherited into an untrusted background request.
	b.WriteString(" --ignore-user-config")
	// --ignore-rules: skips user and project execpolicy `.rules` files so no pre-approved command rules apply.
	b.WriteString(" --ignore-rules")
	// --skip-git-repo-check: permits running inside the empty isolated temp workspace outside any Git repository.
	b.WriteString(" --skip-git-repo-check")
	// -s read-only: OS/policy-level read-only sandbox defense-in-depth, blocking file writes even if a patch tool were
	// somehow invoked.
	b.WriteString(" -s read-only")
	// --color never: suppresses ANSI color escape sequences in diagnostic output.
	b.WriteString(" --color never")
	// --json: prints structured JSONL events (item.completed, turn.completed, turn.failed, error) to stdout for
	// reliable extraction of the final answer, token usage, and terminal errors.
	b.WriteString(" --json")
	// -c approval_policy="never": prevents interactive approval stalls in headless mode and causes any restricted
	// operation (such as apply_patch under read-only sandbox) to be rejected immediately.
	b.WriteString(" -c " + shellQuote(`approval_policy="never"`))
	// -c mcp_servers={}: clears MCP server definitions as defense-in-depth alongside --ignore-user-config.
	b.WriteString(" -c " + shellQuote(`mcp_servers={}`))
	// -c web_search="disabled": disables the native Responses web_search tool so prompts cannot make outbound web queries.
	b.WriteString(" -c " + shellQuote(`web_search="disabled"`))
	// -c agents.max_depth=0: forbids nested sub-agent delegation as defense-in-depth alongside --disable multi_agent*.
	b.WriteString(" -c " + shellQuote(`agents.max_depth=0`))
	// -c include_apply_patch_tool=false: suppresses file-patching tool declarations in addition to the read-only sandbox.
	b.WriteString(" -c " + shellQuote(`include_apply_patch_tool=false`))
	// --disable shell_tool: removes the built-in shell execution tool so the model cannot execute commands or read local files.
	b.WriteString(" --disable shell_tool")
	// --disable unified_exec: disables the unified PTY/exec backend paired with shell_tool.
	b.WriteString(" --disable unified_exec")
	// --disable apps: disables ChatGPT Apps / external connector tools.
	b.WriteString(" --disable apps")
	// --disable plugins: disables Codex plugins and plugin-supplied tools.
	b.WriteString(" --disable plugins")
	// --disable hooks: disables Codex lifecycle hooks so no external hook commands execute.
	b.WriteString(" --disable hooks")
	// --disable multi_agent_v2 / multi_agent: disables multi-agent orchestration and sub-agent spawning features.
	b.WriteString(" --disable multi_agent_v2")
	b.WriteString(" --disable multi_agent")
	// --disable sleep_tool: disables the sleep/wait tool.
	b.WriteString(" --disable sleep_tool")
	// --disable view_image: disables local image file inspection so untrusted prompts cannot read local image paths.
	b.WriteString(" --disable view_image")
	// --disable goals: disables persistent goal-management tools (create_goal/get_goal/update_goal).
	b.WriteString(" --disable goals")
	// --disable code_mode_host: disables the code-mode JS tool-orchestration host when supported.
	b.WriteString(" --disable code_mode_host")
	// --disable browser_use / computer_use / image_generation / memories: disables browser automation, desktop control,
	// image generation, and cross-session memory persistence.
	b.WriteString(" --disable browser_use")
	b.WriteString(" --disable computer_use")
	b.WriteString(" --disable image_generation")
	b.WriteString(" --disable memories")

	if p.Model != "" {
		// -m: selects the explicit model slug for this request.
		b.WriteString(" -m " + shellQuote(p.Model))
	}
	if p.Effort != "" {
		// -c model_reasoning_effort=...: maps Persona's effort level onto Codex's reasoning effort config.
		b.WriteString(" -c " + shellQuote("model_reasoning_effort="+tomlString(p.Effort)))
	}
	if p.System != "" {
		// -c model_instructions_file=...: passes the system prompt through a 0600 temp file so it never appears on argv
		// or in process listings and handles large system prompts without hitting ARG_MAX.
		sysPath, err := writeTempFile(dir, "system.md", p.System)
		if err != nil {
			return proto.ModelRunAnswer{}, err
		}
		b.WriteString(" -c " + shellQuote("model_instructions_file="+tomlString(sysPath)))
	}
	// Final positional `-`: instructs `codex exec` to read the user prompt from stdin (0600 temp file) so prompt text
	// never appears on argv or in process listings.
	b.WriteString(" -")

	in, err := writeTempFile(dir, "prompt.md", p.Prompt)
	if err != nil {
		return proto.ModelRunAnswer{}, err
	}
	cmd := loginShell(ctx, "cd "+scriptPath(dir)+" && "+codexShellWith(n.launcherForScript(p.Launcher)+b.String(), "")+" < "+scriptPath(in))
	cmd.Env = append(cmd.Environ(), modelSourceEnvironment(modelhttp.Source{Role: "background"}, n.bin)...)
	killGroupOnCancel(cmd)
	raw, runErr := cmd.CombinedOutput()
	text := strings.TrimSpace(string(raw))
	if ctx.Err() != nil {
		return proto.ModelRunAnswer{}, timedOut
	}
	res := parseCodexExecOutput(text)
	if res.completed && !res.failed && runErr == nil {
		return proto.ModelRunAnswer{Text: res.text, Model: p.Model, Usage: res.usage}, nil
	}
	errText := cmp.Or(res.errMsg, text)
	if isCodexLoggedOut(errText) || isCodexLoggedOut(text) {
		return proto.ModelRunAnswer{}, &rpc.Error{Code: proto.CodeModelRunLoggedOut, Message: "model.run: " + p.Launcher + " is not logged in here"}
	}
	// Fail closed if the installed codex CLI rejects any required isolation flag or feature toggle.
	if isIsolationFlagError(errText) || isIsolationFlagError(text) {
		return proto.ModelRunAnswer{}, fmt.Errorf("model.run: %s CLI does not support required isolation flags; update codex: %s", p.Launcher, checkOutput(errText))
	}
	msg := checkOutput(errText)
	if msg == "" && runErr != nil {
		msg = clipText(redactCheck(runErr.Error()), 400)
	}
	return proto.ModelRunAnswer{}, errors.New("model.run: " + msg)
}

type codexExecOut struct {
	text      string
	usage     aiwire.Usage
	completed bool
	failed    bool
	errMsg    string
}

// parseCodexExecOutput reads `codex exec --json` JSONL events, ignoring interactive shell noise and non-fatal
// item-level warnings (e.g. malformed user agent files or WebSocket-to-HTTPS fallback notices).
func parseCodexExecOutput(out string) codexExecOut {
	var res codexExecOut
	var lastTopErr string
	for line := range strings.SplitSeq(textfmt.StripTerminalEscapes(out), "\n") {
		line = strings.TrimSpace(line)
		i := strings.IndexByte(line, '{')
		if i < 0 {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Item    *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage *struct {
				InputTokens           int `json:"input_tokens"`
				CachedInputTokens     int `json:"cached_input_tokens"`
				CacheWriteInputTokens int `json:"cache_write_input_tokens"`
				OutputTokens          int `json:"output_tokens"`
			} `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(line[i:]), &ev) != nil || ev.Type == "" {
			continue
		}
		switch ev.Type {
		case "item.completed":
			if ev.Item != nil && ev.Item.Type == "agent_message" {
				res.text = ev.Item.Text
			}
		case "turn.completed":
			res.completed = true
			if ev.Usage != nil {
				// Codex reports total input_tokens inclusive of cached_input_tokens, matching OpenAI Responses usage.
				res.usage = aiwire.Usage{
					InputTokens:              max(ev.Usage.InputTokens-ev.Usage.CachedInputTokens, 0),
					OutputTokens:             ev.Usage.OutputTokens,
					CacheReadInputTokens:     ev.Usage.CachedInputTokens,
					CacheCreationInputTokens: ev.Usage.CacheWriteInputTokens,
				}
			}
		case "turn.failed":
			res.failed = true
			if ev.Error != nil && strings.TrimSpace(ev.Error.Message) != "" {
				res.errMsg = strings.TrimSpace(ev.Error.Message)
			}
		case "error":
			if msg := strings.TrimSpace(ev.Message); msg != "" {
				lastTopErr = msg
			}
		}
	}
	if res.errMsg == "" && (!res.completed || res.failed) {
		res.errMsg = lastTopErr
	}
	return res
}

func isCodexLoggedOut(text string) bool {
	low := strings.ToLower(text)
	for _, needle := range []string{
		"401 unauthorized",
		"missing bearer or basic authentication",
		"not logged in",
		"codex login",
		"authentication required",
		"invalid_api_key",
		"incorrect api key",
		"refresh_token_reused",
		"token expired",
		"please sign in",
		"please log in",
	} {
		if strings.Contains(low, needle) {
			return true
		}
	}
	return false
}

// isIsolationFlagError detects CLI argument/feature rejection from an older binary that lacks required isolation flags.
// model.run always fails closed on these errors rather than retrying without isolation.
func isIsolationFlagError(text string) bool {
	low := strings.ToLower(text)
	for _, needle := range []string{
		"unexpected argument",
		"unknown option",
		"unknown flag",
		"flag provided but not defined",
		"unrecognized option",
		"unknown feature flag",
	} {
		if strings.Contains(low, needle) {
			return true
		}
	}
	return false
}

// agyModelRunAgentMarkdown builds the workspace-scoped custom agent definition for `agy --agent persona-model-run`.
func agyModelRunAgentMarkdown(system string) string {
	body := strings.TrimSpace(system)
	if body == "" {
		body = "Respond directly to the user's request."
	}
	// Frontmatter security boundaries:
	// - tools: []: removes every built-in tool (file read/write/edit, shell run_command, search_web, read_url_content,
	//   invoke_subagent, define_subagent, swarm, run_workflow, schedule, browser) from model declarations.
	// - mainAgent: true, subagent: false: allows selection via --agent while disallowing subagent invocation.
	// - commandExecutionPolicy: off: disables terminal command execution at the agent policy layer.
	// - mcpServers: [], skills: [], plugins: []: prevents loading any MCP tools, skills, or plugins for this agent.
	return "---\n" +
		"name: " + agyModelRunAgentName + "\n" +
		"description: Zero-tool single-turn text completion agent for Persona model.run\n" +
		"tools: []\n" +
		"mainAgent: true\n" +
		"subagent: false\n" +
		"commandExecutionPolicy: off\n" +
		"mcpServers: []\n" +
		"skills: []\n" +
		"plugins: []\n" +
		"---\n" +
		body + "\n"
}

// agyModelRunSettings builds the strict, zero-permission settings payload passed via `agy --settings`.
// Only modelProvider and customModelsConfig (when present in the user's settings.json for Gemini API key routing) are
// preserved; permissions, hooks, statusLine, title, and trustedWorkspaces are never inherited.
func (n *Node) agyModelRunSettings() ([]byte, error) {
	cfg := map[string]any{
		// agentMode "": forces default non-auto mode so auto/accept-edits cannot auto-approve any action.
		"agentMode": "",
		// toolPermission "strict": defense-in-depth requiring explicit approval for every tool action, overriding any allow rules.
		"toolPermission": "strict",
		// allowNonWorkspaceAccess false: forbids reading or writing paths outside the empty temporary directory.
		"allowNonWorkspaceAccess": false,
		// disableSlashCommands true: paired with --disable-slash-commands so prompts starting with `/` are sent verbatim.
		"disableSlashCommands": true,
		// Disable terminal bells, tips, feedback surveys, and shell-executed statusLine/title scripts.
		"notifications":      false,
		"showTips":           false,
		"showFeedbackSurvey": false,
		"statusLine":         map[string]any{"enabled": false},
		"title":              map[string]any{"enabled": false},
		// permissions.deny: hard-blocks all permissioned tool namespaces (Deny > Ask > Allow in Antigravity).
		"permissions": map[string]any{
			"allow": []string{},
			"ask":   []string{},
			"deny": []string{
				"command(*)",
				"read_file(*)",
				"write_file(*)",
				"read_url(*)",
				"execute_url(*)",
				"mcp(*)",
				"unsandboxed(*)",
			},
		},
	}
	if existing, err := readSettingsFile(n.agyConfigPath()); err == nil {
		if mp, ok := existing["modelProvider"].(string); ok && mp != "" {
			cfg["modelProvider"] = mp
		}
		if cm, ok := existing["customModelsConfig"]; ok && cm != nil {
			cfg["customModelsConfig"] = cm
		}
	}
	return json.Marshal(cfg)
}

// prepareAgyIsolatedHome creates an ephemeral HOME inside dir so agy writes brain/, conversations/, presence/, and
// conversation_summaries.db strictly inside dir (deleted on exit) and never loads user global hooks or MCP configs
// from ~/.gemini/config/. Only read-only auth and runtime asset entries are symlinked from the real agy data root.
func (n *Node) prepareAgyIsolatedHome(dir string) (string, error) {
	isoHome := filepath.Join(dir, "agy-home")
	isoData := filepath.Join(isoHome, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(isoData, 0o700); err != nil {
		return "", err
	}
	realData := n.agyDataRoot()
	for _, entry := range []string{"antigravity-oauth-token", "installation_id", "bin", "builtin"} {
		src := filepath.Join(realData, entry)
		if st, err := os.Stat(src); err == nil {
			dst := filepath.Join(isoData, entry)
			if err := os.Symlink(src, dst); err != nil && runtime.GOOS == "windows" {
				_ = copyAgyIsolatedEntry(src, dst, st)
			}
		}
	}
	if runtime.GOOS == "darwin" && n.home != "" {
		// On macOS, Security.framework locates login.keychain-db under $HOME/Library/Keychains.
		srcKeychains := filepath.Join(n.home, "Library", "Keychains")
		if _, err := os.Stat(srcKeychains); err == nil {
			if err := os.MkdirAll(filepath.Join(isoHome, "Library"), 0o700); err == nil {
				_ = os.Symlink(srcKeychains, filepath.Join(isoHome, "Library", "Keychains"))
			}
		}
	}
	return isoHome, nil
}

// copyAgyIsolatedEntry provides a read-only copy fallback on Windows when symlink creation is unavailable.
// External symlinks inside directory trees are never followed.
func copyAgyIsolatedEntry(src, dst string, st os.FileInfo) error {
	if !st.IsDir() {
		return copyIsolatedFile(src, dst, 0o600)
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		mode := fs.FileMode(0o600)
		if info, statErr := d.Info(); statErr == nil && info.Mode()&0o111 != 0 {
			mode = 0o700
		}
		return copyIsolatedFile(path, target, mode)
	})
}

func copyIsolatedFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// modelRunAgy answers an Antigravity model.run request, reusing the Node's on-demand warm `agy acp` worker when all
// zero-side-effect guardrails hold and transparently falling back to modelRunAgyCold otherwise.
func (n *Node) modelRunAgy(ctx context.Context, p proto.ModelRunParams, builtinAgyBacked bool, timedOut error) (proto.ModelRunAnswer, error) {
	if ans, handled, err := n.modelRunAgyWarm(ctx, p, builtinAgyBacked, timedOut); handled {
		return ans, err
	}
	return n.modelRunAgyCold(ctx, p, builtinAgyBacked, timedOut)
}

// modelRunAgyCold executes a single isolated turn via `agy --input-format stream-json --output-format stream-json`.
func (n *Node) modelRunAgyCold(ctx context.Context, p proto.ModelRunParams, builtinAgyBacked bool, timedOut error) (proto.ModelRunAnswer, error) {
	dir, err := mkModelRunTempDir()
	if err != nil {
		return proto.ModelRunAnswer{}, err
	}
	defer os.RemoveAll(dir)

	// Write the workspace-scoped zero-tool custom agent definition (system prompt lives in its Markdown body).
	agentDir := filepath.Join(dir, ".agents", "agents", agyModelRunAgentName)
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return proto.ModelRunAnswer{}, err
	}
	if _, err := writeTempFile(agentDir, "agent.md", agyModelRunAgentMarkdown(p.System)); err != nil {
		return proto.ModelRunAnswer{}, err
	}
	settingsJSON, err := n.agyModelRunSettings()
	if err != nil {
		return proto.ModelRunAnswer{}, err
	}
	settingsPath, err := writeTempFile(dir, "settings.json", string(settingsJSON))
	if err != nil {
		return proto.ModelRunAnswer{}, err
	}
	isoHome, err := n.prepareAgyIsolatedHome(dir)
	if err != nil {
		return proto.ModelRunAnswer{}, err
	}
	// Pass the user prompt via NDJSON on stdin (`--input-format stream-json`) so prompt text never appears on argv or
	// in process listings (`ps`), avoids ARG_MAX limits on large payloads, and guarantees a single turn (1 line + EOF).
	inputLine, err := json.Marshal(map[string]any{
		"event":   "user",
		"message": map[string]any{"content": p.Prompt},
	})
	if err != nil {
		return proto.ModelRunAnswer{}, err
	}
	inPath, err := writeTempFile(dir, "input.ndjson", string(inputLine)+"\n")
	if err != nil {
		return proto.ModelRunAnswer{}, err
	}

	launcherCmd := n.launcherForScript(p.Launcher)
	if builtinAgyBacked {
		bin := n.builtinAgyExecutable()
		if bin == "" {
			return proto.ModelRunAnswer{}, fmt.Errorf("model.run: %s CLI executable not found", p.Launcher)
		}
		launcherCmd = scriptPath(bin)
	}

	var b strings.Builder
	// --agent persona-model-run: binds the run to our workspace zero-tool custom agent (tools: [], mcpServers: [],
	// commandExecutionPolicy: off, skills: [], plugins: []) and delivers p.System as its system prompt.
	b.WriteString(" --agent " + shellQuote(agyModelRunAgentName))
	// --input-format stream-json: reads a single NDJSON user turn from stdin rather than passing the prompt on argv.
	b.WriteString(" --input-format stream-json")
	// --output-format stream-json: emits structured NDJSON init/step_update/result events for answer, model, and usage.
	b.WriteString(" --output-format stream-json")
	// --disable-slash-commands: stops prompts starting with `/` from triggering CLI slash commands or skill expansion.
	b.WriteString(" --disable-slash-commands")
	// --settings <file>: applies session-only strict/deny settings without modifying the user's settings.json.
	b.WriteString(" --settings " + scriptPath(settingsPath))
	if p.Model != "" {
		// --model: selects the explicit Antigravity model ID for this turn.
		b.WriteString(" --model " + shellQuote(p.Model))
	}
	if p.Effort != "" {
		// --effort: passes the requested reasoning effort (low|medium|high|xhigh|max).
		b.WriteString(" --effort " + p.Effort)
	}

	// Override HOME only on the agy invocation (after loginShell sources ~/.zshrc) so shell aliases/PATH still resolve
	// while agy itself reads zero global hooks/MCP from ~/.gemini/config and persists nothing into the real ~/.gemini/antigravity-cli.
	script := "cd " + scriptPath(dir) + " && HOME=" + scriptPath(isoHome) + " " + launcherCmd + b.String() + " < " + scriptPath(inPath)
	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)

	cmd := loginShell(runCtx, script)
	killGroupOnCancel(cmd)
	return runAgyModelCommand(runCtx, cancelRun, cmd, p, timedOut)
}

var errAgyInteractiveAuth = errors.New("agy requested interactive authentication")

func runAgyModelCommand(ctx context.Context, cancel context.CancelCauseFunc, cmd *exec.Cmd, p proto.ModelRunParams, timedOut error) (proto.ModelRunAnswer, error) {
	raw, runErr := runWithEarlyAuthDetect(ctx, cancel, cmd, isAgyInteractiveAuthPrompt)
	// Preserve the first cancellation cause even if the parent deadline expires during process cleanup.
	cause := context.Cause(ctx)
	if cause != nil && cause != errAgyInteractiveAuth {
		return proto.ModelRunAnswer{}, timedOut
	}
	text := strings.TrimSpace(string(raw))
	res := parseAgyStreamOutput(text)
	if res.hasResult && res.status == "SUCCESS" {
		return proto.ModelRunAnswer{
			Text:  strings.TrimSuffix(res.response, "\n"),
			Model: cmp.Or(res.model, p.Model),
			Usage: res.usage,
		}, nil
	}
	if cause == errAgyInteractiveAuth {
		return proto.ModelRunAnswer{}, &rpc.Error{Code: proto.CodeModelRunLoggedOut, Message: "model.run: " + p.Launcher + " is not logged in here"}
	}
	errText := cmp.Or(res.errMsg, text)
	if isAgyLoggedOut(errText) || isAgyLoggedOut(text) {
		return proto.ModelRunAnswer{}, &rpc.Error{Code: proto.CodeModelRunLoggedOut, Message: "model.run: " + p.Launcher + " is not logged in here"}
	}
	// Fail closed if the installed agy CLI rejects any required isolation flag or the zero-tool custom agent.
	if isIsolationFlagError(errText) || isIsolationFlagError(text) || isAgyMissingAgentError(errText) {
		return proto.ModelRunAnswer{}, fmt.Errorf("model.run: %s CLI does not support required isolation flags; update agy: %s", p.Launcher, checkOutput(errText))
	}
	msg := checkOutput(errText)
	if msg == "" && runErr != nil {
		msg = clipText(redactCheck(runErr.Error()), 400)
	}
	return proto.ModelRunAnswer{}, errors.New("model.run: " + msg)
}

// runWithEarlyAuthDetect captures combined stdout+stderr and cancels the process immediately if matchLine detects an
// interactive login prompt (e.g. agy waiting 60 seconds for an OAuth browser callback when signed out).
func runWithEarlyAuthDetect(ctx context.Context, cancel context.CancelCauseFunc, cmd *exec.Cmd, matchLine func(string) bool) ([]byte, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// A descendant can retain the write end after the launcher dies. WaitDelay cannot interrupt Scan before Wait.
	stopReading := context.AfterFunc(ctx, func() { _ = stdout.Close() })
	defer stopReading()
	var buf bytes.Buffer
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		buf.WriteString(line)
		buf.WriteByte('\n')
		if ctx.Err() == nil && matchLine(line) {
			cancel(errAgyInteractiveAuth)
		}
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		// A child forking during the first group signal can survive it on macOS (#5942).
		osproc.SweepGroup(cmd.Process.Pid, time.Second)
	}
	return buf.Bytes(), waitErr
}

type agyStreamOut struct {
	model     string
	status    string
	response  string
	errMsg    string
	usage     aiwire.Usage
	hasResult bool
}

// parseAgyStreamOutput parses `agy --output-format stream-json` NDJSON events (as well as single-envelope `--output-format json`).
func parseAgyStreamOutput(out string) agyStreamOut {
	var res agyStreamOut
	for line := range strings.SplitSeq(textfmt.StripTerminalEscapes(out), "\n") {
		line = strings.TrimSpace(line)
		i := strings.IndexByte(line, '{')
		if i < 0 {
			continue
		}
		raw := []byte(line[i:])
		var env struct {
			Event string `json:"event"`
			Init  *struct {
				Model string `json:"model"`
			} `json:"init"`
			Result *struct {
				Status   string `json:"status"`
				Response string `json:"response"`
				Error    string `json:"error"`
				Usage    *struct {
					InputTokens     int `json:"input_tokens"`
					OutputTokens    int `json:"output_tokens"`
					CacheReadTokens int `json:"cache_read_tokens"`
				} `json:"usage"`
			} `json:"result"`
			// Flat single-envelope JSON format fallback (e.g. error envelopes printed directly on startup failure).
			Status   string `json:"status"`
			Response string `json:"response"`
			Error    string `json:"error"`
			Usage    *struct {
				InputTokens     int `json:"input_tokens"`
				OutputTokens    int `json:"output_tokens"`
				CacheReadTokens int `json:"cache_read_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(raw, &env) != nil {
			continue
		}
		if env.Event == "init" && env.Init != nil && env.Init.Model != "" {
			res.model = env.Init.Model
		}
		if env.Event == "result" && env.Result != nil {
			res.hasResult = true
			res.status = env.Result.Status
			res.response = env.Result.Response
			res.errMsg = strings.TrimSpace(env.Result.Error)
			if env.Result.Usage != nil {
				// agy reports input_tokens exclusive of cache_read_tokens (total_tokens = input_tokens + output_tokens).
				res.usage = aiwire.Usage{
					InputTokens:          env.Result.Usage.InputTokens,
					OutputTokens:         env.Result.Usage.OutputTokens,
					CacheReadInputTokens: env.Result.Usage.CacheReadTokens,
				}
			}
			continue
		}
		if env.Event == "" && env.Status != "" {
			res.hasResult = true
			res.status = env.Status
			res.response = env.Response
			res.errMsg = strings.TrimSpace(env.Error)
			if env.Usage != nil {
				res.usage = aiwire.Usage{
					InputTokens:          env.Usage.InputTokens,
					OutputTokens:         env.Usage.OutputTokens,
					CacheReadInputTokens: env.Usage.CacheReadTokens,
				}
			}
		}
	}
	return res
}

// isAgyStructuredOutputLine reports whether line is a structured JSON object (e.g. NDJSON init, step_update, or result
// payload from `agy --output-format stream-json`), which must not be matched by interactive OAuth prompt detection.
func isAgyStructuredOutputLine(line string) bool {
	trimmed := strings.TrimSpace(textfmt.StripTerminalEscapes(line))
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	var obj map[string]json.RawMessage
	return json.Unmarshal([]byte(trimmed), &obj) == nil
}

func isAgyInteractiveAuthPrompt(line string) bool {
	if isAgyStructuredOutputLine(line) {
		return false
	}
	low := strings.ToLower(textfmt.StripTerminalEscapes(line))
	return strings.Contains(low, "authentication required") || strings.Contains(low, "waiting for authentication")
}

func isAgyLoggedOut(text string) bool {
	low := strings.ToLower(text)
	for _, needle := range []string{
		"authentication required",
		"authentication failed",
		"authentication timed out",
		"waiting for authentication",
		"not logged in",
		"not signed in",
		"signed out",
		"logged out",
		"please sign in",
		"please log in",
		"invalid_grant",
		"re-authenticate",
	} {
		if strings.Contains(low, needle) {
			return true
		}
	}
	return false
}

func isAgyMissingAgentError(text string) bool {
	low := strings.ToLower(text)
	return strings.Contains(low, "unknown agent") || strings.Contains(low, "agent not found")
}
