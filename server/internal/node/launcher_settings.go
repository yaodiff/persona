package node

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

var nativeClaudeModel = lazyre.New(`(?i)(?:^|[/:.@_-])claude(?:[-_]|$)`)

func nonClaudeDispatchModel(models ...string) string {
	for _, model := range models {
		if proto.ValidModelID(model) && !isTierAlias(model) && !nativeClaudeModel.MatchString(proto.TrimModelWindow(model)) {
			return model
		}
	}
	return "deepseek-chat"
}

func dispatchClaudeEnv(env map[string]any, aliases proto.ModelAliases, model string) string {
	get := func(key string) string {
		value, _ := env[key].(string)
		return value
	}
	configured := nonClaudeDispatchModel(get("ANTHROPIC_MODEL"), model)
	for _, key := range tierModelVars {
		env[key] = nonClaudeDispatchModel(get(key), configured)
	}
	for key, value := range aliases.Env() {
		env[key] = value
	}
	if aliases["subagent_model"] == "" {
		env["CLAUDE_CODE_WORKFLOW_SUBAGENT_MODEL"] = get("CLAUDE_CODE_SUBAGENT_MODEL")
	}
	env["CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK"] = "1"
	return configured
}

func (n *Node) claudeModelAliasSettings(ctx context.Context, launcher, cwd, settings string, aliases proto.ModelAliases, requested string) (string, string, error) {
	if err := aliases.Validate(); err != nil {
		return "", "", err
	}
	if settings == "" {
		if _, managed := n.managedLauncher(launcher); managed {
			return "", "", errors.New("cannot prepare managed Claude dispatch settings")
		}
		settings = n.launcherOwnSettings(ctx, launcher)
	}
	merged := map[string]any{}
	if settings != "" {
		var err error
		merged, err = readLauncherSettings(settings, cwd, n.home)
		if err != nil {
			return "", "", errors.New("cannot read Claude settings for model_aliases")
		}
	}
	env, _ := merged["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	model, _ := merged["model"].(string)
	configured := dispatchClaudeEnv(env, aliases, model)
	selected := resolveTier(planTier(requested), func(key string) string {
		value, _ := env[key].(string)
		return value
	})
	if selected == "" || isTierAlias(selected) || strings.EqualFold(proto.TrimModelWindow(selected), "claude") {
		selected = withMarker(requested, configured)
	}
	env["ANTHROPIC_MODEL"] = selected
	merged["model"], merged["env"] = selected, env
	raw, err := json.Marshal(merged)
	if err != nil {
		return "", "", errors.New("cannot encode Claude settings for model_aliases")
	}
	path, err := writeSettings(n.dir, "model-aliases-"+newUUID()+".json", raw)
	return path, selected, err
}

// A launcher alias can bring its own settings (`alias work="claude --settings ~/.claude/settings.work.json …"`, with
// the gateway, its token and the model in that file's env). Every claude start Persona makes adds its own --settings
// after the launcher's words (the hooks that bring questions, permission requests and turn ends to the phone), and
// Claude Code keeps only the last --settings it is given: the launcher's file was dropped, and the claude it started
// answered every prompt with "Not logged in". So the start passes one file that is both: the launcher's settings
// with Persona's hooks added to its own, as the last --settings. That is for a claude that does not load plugins: one
// that does gets the hooks as a plugin (hooksplugin.go), and nothing of the launcher's is replaced, a function's
// --settings too, which no merge can see.

// claudeSettings is the --settings a claude start of launcher passes: ours (a file in n.dir), or, when the launcher
// is an alias with its own --settings, the two merged into launcher-settings/<launcher>-<ours>. cwd is where claude
// runs (a relative path in the alias is from there). Anything wrong with the launcher's file: ours, as before.
func (n *Node) claudeSettings(ctx context.Context, launcher, ours, cwd string, configDir ...string) string {
	if m, ok := n.managedLauncher(launcher); ok {
		// A managed launcher has no settings file of its own, but its gateway and key must beat the user's global
		// settings env (another gateway): put them in ours.
		if path, err := n.mergeManagedSettings(launcher, m, ours, configDir...); err != nil {
			n.logf("launcher %s: its gateway not merged with Persona's settings: %v", launcher, err)
			return ours
		} else {
			return path
		}
	}
	own := n.launcherOwnSettings(ctx, launcher)
	if own == "" {
		return ours
	}
	path, err := n.mergeLauncherSettings(launcher, own, ours, cwd)
	if err != nil {
		// Not the value: an inline --settings '{…}' can hold a token.
		n.logf("launcher %s: its own --settings not merged with Persona's, it will be ignored: %v", launcher, err)
		return ours
	}
	return path
}

// launcherOwnSettings is the --settings the launcher's alias gives claude, "" for none (plain claude, a managed
// launcher, a function, an alias without one). The list is read first when it has not been since the node started.
func (n *Node) launcherOwnSettings(ctx context.Context, launcher string) string {
	if launcher == "" || launcher == "claude" {
		return ""
	}
	if _, ok := n.managedLauncher(launcher); ok {
		return "" // personad launch sets its environment and execs claude: no settings of its own
	}
	launcherCache.Lock()
	loaded := !launcherCache.at.IsZero()
	launcherCache.Unlock()
	if !loaded {
		_, _ = n.launchers(ctx)
	}
	launcherCache.Lock()
	defer launcherCache.Unlock()
	return launcherCache.settings[launcher]
}

// mergeLauncherSettings writes the launcher's settings (own: what its alias passes) merged with ours, and says where.
// Written again on every start: the launcher's file may have changed. 0600: it holds the launcher's token.
func (n *Node) mergeLauncherSettings(launcher, own, ours, cwd string) (string, error) {
	theirs, err := readLauncherSettings(own, cwd, n.home)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(ours)
	if err != nil {
		return "", err
	}
	var mine map[string]any
	if err := json.Unmarshal(b, &mine); err != nil {
		return "", err
	}
	out, err := json.MarshalIndent(mergeClaudeSettings(theirs, mine), "", "  ")
	if err != nil {
		return "", err
	}
	return writeSettings(n.dir, launcher+"-"+filepath.Base(ours), out)
}

// mergeManagedSettings writes ours with the managed launcher's env on top and says where (see writeSettings). ours
// "": the env alone, for a start whose hooks go in a plugin (hooksplugin.go).
func (n *Node) mergeManagedSettings(launcher string, l proto.ManagedLauncher, ours string, configDir ...string) (string, error) {
	mine := map[string]any{}
	name := launcher + "-env-settings.json"
	if ours != "" {
		name = launcher + "-" + filepath.Base(ours)
		b, err := os.ReadFile(ours)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// Not written yet (the node writes it as it starts serving; a test's node never does): the launcher's env
			// alone, rather than no settings with the gateway at all (#1364).
		case err != nil:
			return "", err
		default:
			if err := json.Unmarshal(b, &mine); err != nil {
				return "", err
			}
			if mine == nil { // the file says null
				mine = map[string]any{}
			}
		}
	}
	env := map[string]any{}
	if cur, ok := mine["env"].(map[string]any); ok {
		for k, v := range cur {
			env[k] = v
		}
	}
	for k, v := range managedSettingsEnv(l) {
		env[k] = v
	}
	// A provider that follows the computer's proxy adds its bypass list to each spelling the user's settings env sets
	// (which Claude Code applies over the environment), pinned here. A spelling they leave alone is the launch
	// environment's, which `personad launch` fills from the shell, else the user's other spelling (withInheritedNoProxy),
	// and which a pinned one here would replace (#6238). Any other list is pinned as it is.
	if ours, _ := env["NO_PROXY"].(string); mergesNoProxy(l) && ours != "" {
		user := n.userNoProxy(l, configDir...)
		for _, k := range []string{"NO_PROXY", "no_proxy"} {
			if theirs := user[k]; theirs != "" {
				env[k] = mergeNoProxy(ours, theirs)
			} else {
				delete(env, k)
			}
		}
	}
	// A proxy the user's settings give every claude that no longer answers would hold this launcher's requests until
	// they time out; one the launcher does not set itself is taken away (deadproxy.go, #5953).
	if len(configDir) > 0 {
		// Config selection precedes settings load. The caller supplies the real
		// profile; do not turn that selection into a settings.env override.
		delete(env, "CLAUDE_CONFIG_DIR")
	}
	for _, k := range n.deadSettingsProxies(env, l.BaseURL, configDir...) {
		env[k] = ""
		n.logf("launcher %s: the proxy in Claude's settings.json (%s) does not answer; it runs without it", launcher, k)
	}
	if l.AgentType() == "claude" && !l.Withheld {
		env[managedSettingsConfigEnv] = managedClaudeFingerprint(l)
	}
	mine["env"] = env
	mine["enableArtifact"] = false // artifactsOff: a gateway cannot carry them
	// A managed launcher brings its own gateway key, so the computer's user-global apiKeyHelper (~/.claude/settings.json)
	// can only authenticate it against the wrong gateway: Claude Code runs the helper and sends its token in the other
	// auth header, and the launcher's gateway answers 401. An empty string disables the helper for this start.
	mine["apiKeyHelper"] = ""
	out, err := json.MarshalIndent(mine, "", "  ")
	if err != nil {
		return "", err
	}
	return writeSettings(n.dir, name, out)
}

// writeSettings stores a launcher's merged settings under n.dir as name and returns the path. A temporary file of its
// own: two starts of the same launcher at once must not rename each other's away.
func writeSettings(dir, name string, out []byte) (string, error) {
	d := filepath.Join(dir, "launcher-settings")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(d, name)
	tmp, err := os.CreateTemp(d, "."+name+"-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(tmp.Name(), path)
}

// readLauncherSettings reads what an alias passes as --settings: a file (~ and $HOME are the home, a relative path
// is from cwd) or, as Claude Code also takes it, the JSON itself.
func readLauncherSettings(arg, cwd, home string) (map[string]any, error) {
	s := strings.TrimSpace(arg)
	var b []byte
	if strings.HasPrefix(s, "{") {
		b = []byte(s)
	} else {
		path := shellPath(s, home)
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		var err error
		if b, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, errors.New("not a JSON object")
	}
	if v == nil {
		v = map[string]any{}
	}
	return v, nil
}

// shellPath is a path from an alias's words (they come unexpanded) as the shell would read ~ and $HOME in it.
func shellPath(p, home string) string {
	for _, h := range []string{"${HOME}", "$HOME"} {
		if p == h || strings.HasPrefix(p, h+"/") {
			p = "~" + p[len(h):]
			break
		}
	}
	return proto.ExpandHome(p, home)
}

// mergeClaudeSettings is the launcher's settings (theirs) with ours on top: for each hook event our hooks after its
// own, so both run; any other key of ours (there is none today) in place of its.
func mergeClaudeSettings(theirs, mine map[string]any) map[string]any {
	out := make(map[string]any, len(theirs)+len(mine))
	for k, v := range theirs {
		out[k] = v
	}
	for k, v := range mine {
		ourHooks, ok := v.(map[string]any)
		if k != "hooks" || !ok {
			out[k] = v
			continue
		}
		hooks := map[string]any{}
		if theirHooks, ok := theirs["hooks"].(map[string]any); ok {
			for event, list := range theirHooks {
				hooks[event] = list
			}
		}
		for event, list := range ourHooks {
			prev, _ := hooks[event].([]any)
			add, _ := list.([]any)
			hooks[event] = append(append([]any{}, prev...), add...)
		}
		out["hooks"] = hooks
	}
	return out
}
