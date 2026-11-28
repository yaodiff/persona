package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// assistantCompaction is what a conversation's launcher declares about compaction (#3066). Persona sets no target of
// its own but one: the main conversation and each leader conversation on a managed launcher are held to the window the
// manager sends with them (assistant.main_compact_window, #7314, #15318) when the launcher declares a larger one. What
// is not declared is left to the agent, Claude Code by the model's own window, Codex by its model's default.
type assistantCompaction struct {
	cacheProxyBase string
	// window: the declared automatic compaction window (CLAUDE_CODE_AUTO_COMPACT_WINDOW, a managed launcher's
	// CompactWindow); capacity: the declared context window (CLAUDE_CODE_MAX_CONTEXT_TOKENS, MaxContext, the provider
	// model's ContextWindow, Codex's model_context_window). 0: not declared.
	window, capacity int
	// pin: Persona writes them for the start. Only a managed launcher: its environment is Persona's, and an assistant
	// start leaves its compact window out of the launch (perStartEnv). Any other launcher's own environment, settings
	// and the user's settings reach the agent as they are, so nothing is written over them.
	pin         bool
	ownSettings string
	// profile: the conversation's Claude config directory as the policy resolves it, "" for none (assistantUserSetup).
	profile string
	// endpoint: the ANTHROPIC_BASE_URL a Claude start's requests go to, "" Anthropic's own API, as the layers below give
	// it (claudeEndpoint); endpointKnown: they could tell (a managed provider, a launcher the probe saw); ownToolSearch:
	// the launcher sets ENABLE_TOOL_SEARCH itself. toolSearchOff: the start writes ENABLE_TOOL_SEARCH=false into its
	// settings (toolSearchFor's forceOff, #10365).
	endpoint                     string
	endpointKnown, ownToolSearch bool
	toolSearchOff                bool
}

// codexLimit is the model_auto_compact_token_limit a Codex start passes: a managed launcher's declared window (capped
// by the window the manager sent, capWindow), else none (the launcher's own -c and config.toml reach Codex as they
// are).
func (c assistantCompaction) codexLimit() int {
	if !c.pin {
		return 0
	}
	return c.window
}

// declared is the window the launcher holds the conversation to, 0 when it declares none.
func (c assistantCompaction) declared() int { return smallerCapacity(c.window, c.capacity) }

// idle is Persona's idle /compact threshold before the agent reports its model's window: 75% of the declared window,
// else assistantIdleCompact (assistantProc.followWindow moves it once a window is reported).
func (c assistantCompaction) idle() int {
	if d := c.declared(); d > 0 {
		return idleThreshold(d)
	}
	return assistantIdleCompact
}

// idleThreshold: an idle conversation is compacted at 75% of its window, below where the agent compacts on its own
// (Claude Code about 33K short of the window) and early enough to be worth it while the cache is warm.
func idleThreshold(window int) int { return window / 4 * 3 }

func smallerCapacity(a, b int) int {
	if b > 0 && (a <= 0 || b < a) {
		return b
	}
	return a
}

func contextLimit(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	if n < 0 {
		return 0
	}
	return n
}

func providerCapacity(l proto.ManagedLauncher, model string) int {
	if model == "" {
		model = l.Model
	}
	pm, _ := providerModel(l, model)
	capacity := smallerCapacity(pm.ContextWindow, l.MaxContext)
	for _, kv := range launcherEnvVars(l.Env) {
		if kv[0] == "CLAUDE_CODE_MAX_CONTEXT_TOKENS" {
			capacity = smallerCapacity(capacity, contextLimit(kv[1]))
		}
	}
	return smallerCapacity(capacity, codexCapacity(l.Args))
}

var codexContextLimit = lazyre.New(`(?m)^\s*model_context_window\s*=\s*([0-9][0-9_]*)\s*(?:#.*)?$`)

func codexCapacity(args []string) int {
	capacity := 0
	for i, arg := range args {
		if (arg == "-c" || arg == "--config") && i+1 < len(args) {
			arg = args[i+1]
		} else if v, ok := strings.CutPrefix(arg, "--config="); ok {
			arg = v
		}
		for _, m := range codexContextLimit.FindAllStringSubmatch(arg, -1) {
			capacity = smallerCapacity(capacity, contextLimit(strings.ReplaceAll(m[1], "_", "")))
		}
	}
	return capacity
}

// settingsCompaction is what a Claude settings file's env declares: the auto-compact window and the context window.
func settingsCompaction(arg, cwd, home string) (window, capacity int) {
	if arg == "" {
		return 0, 0
	}
	s, err := readLauncherSettings(arg, cwd, home)
	if err != nil {
		return 0, 0
	}
	env, _ := s["env"].(map[string]any)
	value := func(k string) int {
		switch v := env[k].(type) {
		case string:
			return contextLimit(v)
		case float64:
			return max(int(v), 0)
		}
		return 0
	}
	return value("CLAUDE_CODE_AUTO_COMPACT_WINDOW"), value("CLAUDE_CODE_MAX_CONTEXT_TOKENS")
}

// assistantCompactionPolicy is what launcher declares about compaction for a conversation in cwd (settings: Persona's
// own settings file for the start), the same for the main conversation and every other (#3066), but for the window the
// manager sent with it (capWindow). Several declarations of one value: the smallest counts (the conservative reading
// where a profile, the environment and settings disagree).
func (n *Node) assistantCompactionPolicy(launcher, cwd, settings string, execution modelExecution) assistantCompaction {
	var c assistantCompaction
	declare := func(window, capacity int) {
		c.window, c.capacity = smallerCapacity(c.window, window), smallerCapacity(c.capacity, capacity)
	}
	isCodex := n.agentOf(launcher) == "codex"
	configDir, codexHome := os.Getenv("CLAUDE_CONFIG_DIR"), os.Getenv("CODEX_HOME")
	if execution.ConfigDir != "" {
		configDir = n.assistantConfigPath(execution.ConfigDir)
	} else if dir := n.claudeStartDir.Load(); dir != nil && *dir != "" {
		configDir = proto.ExpandHome(*dir, n.home)
	}
	if l, managed := n.managedLauncher(launcher); managed {
		c.pin = true
		if isCodex {
			declare(l.CompactWindow, providerCapacity(l, execution.Model))
			for _, kv := range launcherEnvVars(l.Env) {
				if kv[0] == "CODEX_HOME" {
					codexHome = kv[1]
				}
			}
		} else {
			// What the launch itself would set (CompactWindow, else the model's window; its env last), and its settings.
			env := providerEnv(l, execution.Model, "", true)
			declare(contextLimit(env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]), providerCapacity(l, execution.Model))
			c.ownSettings = settingsArg(l.Args)
			declare(settingsCompaction(c.ownSettings, cwd, n.home))
			// Its gateway is pinned in the start's settings (mergeManagedSettings): no other layer moves it.
			c.endpoint, c.endpointKnown = managedBaseURL(l), true
			for _, kv := range launcherEnvVars(l.Env) {
				c.ownToolSearch = c.ownToolSearch || kv[0] == toolSearchEnv
			}
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		p, probeErr := n.keptProbe(ctx, launcher)
		if probeErr == nil { // the probe the message's checks just read (#9097)
			declare(contextLimit(p.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]), contextLimit(p.Env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]))
			declare(0, codexCapacity(p.Args))
			c.ownSettings = settingsArg(p.Args)
			declare(settingsCompaction(c.ownSettings, cwd, n.home))
			if dir := p.Env["CLAUDE_CONFIG_DIR"]; dir != "" && execution.ConfigDir == "" {
				configDir = dir
			}
			if dir := p.Env["CODEX_HOME"]; dir != "" {
				codexHome = dir
			}
		}
		if !isCodex {
			declare(contextLimit(os.Getenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW")), contextLimit(os.Getenv("CLAUDE_CODE_MAX_CONTEXT_TOKENS")))
			if configDir == "" {
				configDir = filepath.Join(n.home, ".claude")
			}
			declare(settingsCompaction(filepath.Join(configDir, "settings.json"), cwd, n.home))
			for _, file := range []string{"settings.json", "settings.local.json"} {
				declare(settingsCompaction(filepath.Join(cwd, ".claude", file), cwd, n.home))
			}
			if probeErr == nil {
				c.endpoint, c.ownToolSearch = claudeEndpoint(p, c.ownSettings, configDir, cwd, n.home)
				c.endpointKnown = true
			}
		}
	}
	c.profile = configDir
	if isCodex {
		if codexHome == "" {
			codexHome = n.codexHome()
		}
		if b, err := os.ReadFile(filepath.Join(codexHome, "config.toml")); err == nil {
			declare(0, codexCapacity([]string{string(b)}))
		}
	}
	declare(settingsCompaction(settings, cwd, n.home))
	if c.window > 0 && c.capacity > 0 && c.capacity < c.window {
		c.window = c.capacity // no compaction window past the context window
	}
	c.capWindow(execution)
	return c
}

// claudeEndpoint is where a claude a launcher starts sends its requests (an ANTHROPIC_BASE_URL, "" Anthropic's own
// API), and whether the launcher sets ENABLE_TOOL_SEARCH itself, read as Claude Code reads them: a settings env wins
// over the process environment, the administrator's managed settings first (claudeManagedEnvLayers, above everything),
// then the launcher's --settings (own), the project's local and shared settings in cwd and the user settings of its
// config directory; then what the launcher exports (p.Env), then the shell's environment unless the launcher unset it
// (the node's own when the probe did not see it). The first layer that names the variable counts, an empty value too
// (Anthropic's own API). A value the shell has only because the node has it is not the launcher's own; a managed one
// counts as the launcher's (nothing Persona writes would win over it).
func claudeEndpoint(p Probe, own, configDir, cwd, home string) (endpoint string, ownToolSearch bool) {
	layers, shell := claudeEnvLayers(p, own, configDir, cwd, home)
	base, set := "", false
	for _, layer := range layers {
		if v, ok := layer["ANTHROPIC_BASE_URL"]; ok && !set {
			base, set = v, true
		}
		if _, ok := layer[toolSearchEnv]; ok {
			ownToolSearch = true
		}
	}
	if !set {
		base = shell["ANTHROPIC_BASE_URL"]
	}
	if v, ok := shell[toolSearchEnv]; ok && p.Shell != nil && os.Getenv(toolSearchEnv) != v {
		ownToolSearch = true
	}
	return base, ownToolSearch
}

func claudeEnvLayers(p Probe, own, configDir, cwd, home string) ([]map[string]string, map[string]string) {
	read := func(arg string) map[string]string {
		if arg == "" {
			return nil
		}
		s, err := readLauncherSettings(arg, cwd, home)
		if err != nil {
			return nil
		}
		env, _ := s["env"].(map[string]any)
		out := map[string]string{}
		for k, v := range env {
			out[k] = fmt.Sprint(v) // a string, or a JSON true Claude Code takes as "true"
		}
		return out
	}
	var layers []map[string]string
	for _, f := range claudeManagedEnvLayers() {
		layers = append(layers, read(f))
	}
	layers = append(layers, read(own), read(filepath.Join(cwd, ".claude", "settings.local.json")),
		read(filepath.Join(cwd, ".claude", "settings.json")), read(filepath.Join(configDir, "settings.json")), p.Env)
	shell := map[string]string{}
	if p.Shell == nil {
		for _, kv := range os.Environ() {
			if k, v, ok := strings.Cut(kv, "="); ok {
				shell[k] = v
			}
		}
	} else {
		for k, v := range p.Shell {
			if !slices.Contains(p.Unset, k) {
				shell[k] = v
			}
		}
	}
	return layers, shell
}

// capWindow holds a conversation on a managed launcher (pin: Persona writes its values) to the window the manager sent
// with it (execution.MainCompactWindow: the main conversation's, #7314, and a leader conversation's, #15318) when the
// launcher declares a larger one: its compaction window, or its context window when it declares only that. The context
// window stays as declared; Claude Code compacts by the smaller of the two, Codex by codexLimit, and the idle threshold
// is 75% of it (idle). Nothing declared invents nothing, and 0 (any other conversation, a guest, an older manager, the
// setting at 0) changes nothing. Which conversations get a window is the manager's call: a node before #15318 applied
// one only with is_main, so with a newer manager a leader conversation there keeps its launcher's window.
func (c *assistantCompaction) capWindow(execution modelExecution) {
	if limit := execution.MainCompactWindow; c.pin && limit > 0 && c.declared() > limit {
		c.window = limit
	}
}

// mainWindowOn is the window the manager sent with a conversation's start (window: the main conversation's or a leader
// conversation's) as that start on launcher carries it (modelExecution.MainCompactWindow): only a managed launcher
// applies it (capWindow), so anywhere else it is 0, or it would change the fingerprint and restart the conversation,
// or miss the spare warmed for it, for nothing. assistantSend and assistantWarm both read it, so the main chat's spare
// matches its next start.
func (n *Node) mainWindowOn(launcher string, window int) int {
	if _, managed := n.managedLauncher(launcher); window > 0 && managed {
		return window
	}
	return 0
}

// claudeModelUsage is one model's entry in a stream-json result's modelUsage: what this process read with it and the
// context window Claude Code holds it to.
type claudeModelUsage struct {
	Input         int `json:"inputTokens"`
	CacheRead     int `json:"cacheReadInputTokens"`
	CacheCreate   int `json:"cacheCreationInputTokens"`
	ContextWindow int `json:"contextWindow"`
}

// reportedWindow is the context window Claude Code holds the conversation's model to, 0 when it reports none: the
// entry of model (the model of the conversation's last reply), else the entry that read the most context (a subagent
// or a helper model reads less than the conversation).
func reportedWindow(usage map[string]claudeModelUsage, model string) int {
	if u, ok := usage[model]; ok && model != "" {
		return u.ContextWindow
	}
	most, window := -1, 0
	for _, u := range usage {
		read := u.Input + u.CacheRead + u.CacheCreate
		if read > most || (read == most && u.ContextWindow < window) {
			most, window = read, u.ContextWindow
		}
	}
	return window
}

// followWindow sets the idle threshold from the window the agent reports for the conversation's model: 75% of it, or of
// the declared window when that is smaller. It reports whether the threshold changed; p.mu is held.
func (p *assistantProc) followWindow(reported int) bool {
	window := smallerCapacity(p.declaredWindow, reported)
	if window <= 0 {
		return false
	}
	idle := idleThreshold(window)
	if idle == p.compactIdleAt {
		return false
	}
	p.compactIdleAt = idle
	return true
}

// env is what a start writes into the agent's environment and merged settings: only a managed launcher's declared
// values (pin), none when it declares none.
func (c assistantCompaction) env() []string {
	if !c.pin {
		return nil
	}
	var env []string
	if c.window > 0 {
		env = append(env, "CLAUDE_CODE_AUTO_COMPACT_WINDOW="+strconv.Itoa(c.window))
	}
	if c.capacity > 0 {
		env = append(env, "CLAUDE_CODE_MAX_CONTEXT_TOKENS="+strconv.Itoa(c.capacity))
	}
	return env
}

func (n *Node) assistantCompactionSettings(id, cwd, settings string, c assistantCompaction) (string, error) {
	s := map[string]any{}
	if settings != "" {
		var err error
		s, err = readLauncherSettings(settings, cwd, n.home)
		if err != nil {
			if !os.IsNotExist(err) {
				return "", err
			}
			s = map[string]any{}
		}
	}
	// The launcher's own --settings goes under ours. A launcher that writes it to a temporary file for each start and
	// removes it when claude exits left the probe a path that is gone by now: there is nothing of its own to merge,
	// the start goes on with ours, and the launcher writes its file again when it runs claude.
	var theirs map[string]any
	if c.ownSettings != "" {
		var err error
		if theirs, err = readLauncherSettings(c.ownSettings, cwd, n.home); err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	if theirs != nil {
		mergedEnv := map[string]any{}
		if env, ok := theirs["env"].(map[string]any); ok {
			for k, v := range env {
				mergedEnv[k] = v
			}
		}
		if env, ok := s["env"].(map[string]any); ok {
			for k, v := range env {
				mergedEnv[k] = v
			}
		}
		s = mergeClaudeSettings(theirs, s)
		s["env"] = mergedEnv
	}
	env, _ := s["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	for _, kv := range c.env() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	if c.toolSearchOff { // the endpoint refused tool search: over the provider's, the launcher's and the user's (#10365)
		env[toolSearchEnv] = "false"
	}
	if c.cacheProxyBase != "" {
		env["ANTHROPIC_BASE_URL"] = c.cacheProxyBase
		// The refresh replays only requests that declare a one-hour cache (cachewarm.Replay). Claude Code declares
		// one only for a subscriber or with this switch; on an API key or a gateway it sends a bare five-minute
		// ephemeral marker, so nothing was ever retained to refresh.
		env[promptCaching1hEnv] = "1"
	}
	s["env"] = env

	// Keep the assistant's project/user instructions, data skills and auto-memory. Only the development workflows and
	// providers it does not use are hidden; bundled-skill and per-skill switches preserve explicitly enabled integrations.
	env["CLAUDE_CODE_DISABLE_BUNDLED_SKILLS"] = "1"
	plugins, _ := s["enabledPlugins"].(map[string]any)
	if plugins == nil {
		plugins = map[string]any{}
	}
	for _, name := range []string{
		"feature-dev@claude-plugins-official", "frontend-design@claude-plugins-official",
		"mattpocock-skills@claude-plugins-official", "gopls-lsp@claude-plugins-official",
		"context7@claude-plugins-official", "claude-hud@claude-hud",
	} {
		plugins[name] = false
	}
	s["enabledPlugins"] = plugins
	overrides, _ := s["skillOverrides"].(map[string]any)
	if overrides == nil {
		overrides = map[string]any{}
	}
	for _, name := range []string{"design", "doctor", "plugin-authoring", "ui-ux-pro-max"} {
		overrides[name] = "off"
	}
	s["skillOverrides"] = overrides
	n.assistantUserSetup(s, cwd, c.profile) // a private profile: ~/.claude's instructions out, the user's skill switches in
	// One memory whichever profile the conversation runs in (memory_merge.go): Claude Code takes the directory from
	// --settings before the profile's own settings. A guest keeps none (guestSettingsFile turns auto memory off).
	if dir := n.conversationMemoryDir(id, cwd); dir != "" {
		s["autoMemoryDirectory"] = dir
	}

	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	dir := n.dir
	if dir == "" {
		dir = filepath.Join(n.home, ".persona")
	}
	return writeSettings(dir, "assistant-compact-"+id+".json", b)
}
