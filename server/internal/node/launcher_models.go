package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/sync/singleflight"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/shellcmd"
)

// The models a launcher's agent offers, for "New Agent"'s model picker (docs/assistant/model-config.md "Choosing a model for a new Agent"): the
// list its own /model menu shows. That list is in no config file — settings.json and config.toml hold the default
// model and the gateway; the CLI makes the list from its aliases and what the gateway's /models answers — so the node
// asks the CLI itself, run the way the launcher runs it: Claude Code answers its initialize control request (headless,
// stream-json) with `models`, Codex's app-server answers model/list, and Antigravity and TRAE CLI print their catalogs.
// Nothing is sent to a model. The node also reads a managed provider's gateway models (launcher_gateway_models.go):
// the CLI's answer has them only when its discovery cache was that gateway's.

// launcherModelsTTL: a list is kept this long per launcher (and managed launcher's settings); refresh skips it.
const launcherModelsTTL = 10 * time.Minute

// launcherModelsStale: a list older than launcherModelsTTL but younger than this is still answered at once, while the CLI
// is asked again in the background for the next ask (#1501): asking takes seconds (the login shell, the CLI's start,
// the gateway's /models) and the picker would wait for it every ten minutes. A changed launcher has a new key
// (executionFingerprint), so it is never answered with its old list.
const launcherModelsStale = 24 * time.Hour

// launcherModelsTimeout covers a slow ~/.zshrc, the CLI's start and the gateway's /models.
var launcherModelsTimeout = 45 * time.Second

// Only the direct model probe asks personad launch to use the npm script's paired interpreter.
const managedModelProbeEnv = "PERSONA_MANAGED_MODEL_PROBE"

// modelCheckProbe is the model the override check asks the launcher to pass on (model_probe.go): any name the
// launcher does not fix itself.
const modelCheckProbe = "persona-model-override-check"

var launcherModelsCache = struct {
	sync.Mutex
	at     map[string]time.Time
	got    map[string]proto.LauncherModels
	flight singleflight.Group
}{at: map[string]time.Time{}, got: map[string]proto.LauncherModels{}}

// launcherModels is the launcher's model list. agentType is the agent the user said the launcher runs (the hub's
// machine config); empty: what it runs. A launcher whose agent has no list to ask for (a compatible CLI, a runtime's) answers
// with its agent and no models.
func (n *Node) launcherModels(ctx context.Context, name, agentType string, refresh bool) (proto.LauncherModels, error) {
	if name != "claude" && !shellcmd.NameRe.MatchString(name) {
		return proto.LauncherModels{}, fmt.Errorf("invalid launcher %q", name)
	}
	if proto.IsExternalLauncher(name) {
		return n.externalLauncherModels(ctx, name, refresh)
	}
	if isDesktopLauncher(name) {
		return n.claudeDesktopLauncherModels(ctx, refresh)
	}
	if isCodexAppLauncher(name) {
		return n.codexAppLauncherModels(ctx, refresh)
	}
	// A runtime's launcher, as a start in ctx's host runs it (where that host's engine runs only its own launchers, its
	// provider of the name, #8140): static advertisement or native discovery.
	h, runtimeName := n.runtimeOfIn(ctx, name), name
	if h == nil {
		if m, ok := n.managedLauncher(name); ok && m.AgentType() == agyAgent {
			h, runtimeName = n.runtimes.launcherRuntime(agyAgent), agyAgent
		}
	}
	if h != nil && !n.usesAgyPane(ctx, name, agentType) {
		res, _ := launcherModelsOf(h, runtimeName)
		return n.liveRuntimeModels(ctx, h, runtimeName, name, refresh, res)
	}
	agent := n.launcherModelsAgent(ctx, name, agentType)
	if agent == ompAgent {
		// omp accepts a typed model on a new herdr or Orca start, unless its launcher pins it.
		res := proto.LauncherModels{Agent: agent, Models: []proto.AgentModel{}}
		if err := n.checkOmpStartModel(ctx, name, modelCheckProbe); err != nil {
			res.Locked = err.Error()
		} else {
			res.SupportsStartModel = true
		}
		return res, nil
	}
	if agent == agyAgent && (assistantLauncherPurpose(ctx) || n.startKind(ctx) == agyAgent) {
		// The companion is unavailable on this path. Its catalog must stay separate from the pane's cached list.
		return proto.LauncherModels{Agent: agyAgent, Models: []proto.AgentModel{}}, nil
	}
	key := n.home + "\x00" + name + "\x00" + agent + "\x00" + n.executionFingerprint(name, modelExecution{})
	launcherModelsCache.Lock()
	at, ok := launcherModelsCache.at[key]
	cached := launcherModelsCache.got[key]
	launcherModelsCache.Unlock()
	ask := func() (any, error) {
		res := proto.LauncherModels{Agent: agent, Models: []proto.AgentModel{}}
		if agent != "claude" && agent != "codex" && agent != agyAgent && agent != traexAgent {
			return res, nil
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), launcherModelsTimeout)
		defer cancel()
		var models, gateway []proto.AgentModel
		var listErr error
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			models, listErr = n.listAgentModels(ctx, name, agent)
		}()
		// A managed provider's gateway models, read here: the CLI's own answer has them only when its one cache file
		// was this gateway's (launcher_gateway_models.go).
		if m, ok := n.managedLauncher(name); ok && agent == "claude" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				gateway = n.gatewayModels(ctx, m)
			}()
		}
		// Whether a picked model reaches the agent at all: a launcher that fixes its own --model would start with
		// that one whatever is picked (the start refuses it).
		if agent != agyAgent {
			if _, err := n.launcherModelCheck(ctx, name, modelCheckProbe); err != nil {
				res.Locked = err.Error()
			}
		}
		wg.Wait()
		if listErr != nil {
			return nil, listErr
		}
		if agent == "codex" && name == "codex" && len(models) == 0 {
			models = readCodexHomeModels(n.codexHome())
		}
		res.Models = withGatewayModels(models, gateway)
		launcherModelsCache.Lock()
		if len(launcherModelsCache.got) > 64 {
			launcherModelsCache.at, launcherModelsCache.got = map[string]time.Time{}, map[string]proto.LauncherModels{}
		}
		launcherModelsCache.at[key], launcherModelsCache.got[key] = time.Now(), res
		launcherModelsCache.Unlock()
		if agent == agyAgent && n.hasLiveAgyPane() {
			n.notifySessionsSoon()
		}
		return res, nil
	}
	if ok && !refresh {
		switch age := time.Since(at); {
		case age < launcherModelsTTL:
			return cached, nil
		case age < launcherModelsStale:
			// The kept list now, a fresh one for the next ask; an ask already running is joined, not repeated. A failed
			// one leaves the kept list as it was.
			launcherModelsCache.flight.DoChan(key, ask)
			return cached, nil
		}
	}
	v, err, _ := launcherModelsCache.flight.Do(key, ask)
	if err != nil {
		return proto.LauncherModels{}, err
	}
	return v.(proto.LauncherModels), nil
}

// knownLauncher: asking a launcher for its models runs it under the login shell, so only what this machine offers as a
// launcher is asked. One the hub's hub.json names (a wrapper the node may not list) is the hub's to vouch for.
// runnableLauncher: what the hub asks about a launcher (its probe, gateway, models, whether it forwards --model) runs it in
// a login shell as `( name … )`, so it is asked only of a launcher this machine offers (knownLauncher) or one the hub's
// hub.json names (configured). Any other word would run as a command: `reboot` (#1185).
func (n *Node) runnableLauncher(ctx context.Context, name string, configured bool) error {
	if proto.IsExternalLauncher(name) {
		_, err := n.externalInventoryTarget(ctx, name, false)
		return err
	}
	if configured || n.knownLauncher(ctx, name) {
		return nil
	}
	return fmt.Errorf("unknown launcher %q", name)
}

func (n *Node) knownLauncher(ctx context.Context, name string) bool {
	if proto.IsExternalLauncher(name) {
		_, err := n.externalInventoryTarget(ctx, name, false)
		return err == nil
	}
	if name == "claude" || name == traexAgent {
		return true
	}
	if _, ok := n.managedLauncher(name); ok {
		return true
	}
	if isDesktopLauncher(name) {
		_, offered := n.withDesktopLauncher(nil)
		return offered
	}
	if isCodexAppLauncher(name) {
		_, offered := n.withCodexAppLauncher(nil)
		return offered
	}
	if n.runtimes.launcherRuntime(name) != nil || shellLauncherNames()[name] {
		return true // the shell's last list will do: no login shell to wait for when the models are kept anyway
	}
	list, err := n.launchers(ctx)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == name })
}

// launcherModelsAgent is the agent the launcher runs: the user's word for it, else what the node knows of it.
func (n *Node) launcherModelsAgent(ctx context.Context, name, agentType string) string {
	switch {
	case agentType != "":
		return agentType
	case name == "claude":
		return "claude"
	case name == traexAgent:
		return traexAgent
	}
	if m, ok := n.managedLauncher(name); ok {
		return m.AgentType()
	}
	if n.agentKindOf(name, "") == agyAgent {
		return agyAgent
	}
	if _, ok := n.codexLauncher(ctx, name); ok {
		return "codex"
	}
	if !shellLauncherNames()[name] {
		if h := n.runtimes.launcherRuntime(name); h != nil { // a runtime's launcher (DSH): not run by a shell
			for _, l := range n.runtimes.launchers() {
				if l.Name == name && l.Agent != "" {
					return l.Agent
				}
			}
			return "runtime"
		}
	}
	if d := n.launcherProbeCached(ctx, name); d.Agent != "" {
		return d.Agent
	}
	return "claude" // a probe that failed: as claude, like every other start
}

// agentModelsCommand asks the launcher for its agent's models in dir. Managed Claude and Codex launchers skip the
// shell when the executable and its interpreter are already available.
func (n *Node) agentModelsCommand(ctx context.Context, name, agent, dir string, traexArgs ...string) (*exec.Cmd, error) {
	m, managed := n.managedLauncher(name)
	var args []string
	if agent == "claude" {
		// No hooks: this claude is no session of the user's (Persona's own, or a hook of theirs, would see one start).
		ours := filepath.Join(dir, "settings.json")
		if err := os.WriteFile(ours, []byte(`{"disableAllHooks":true}`), 0o600); err != nil {
			return nil, err
		}
		args = []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
			"--settings", n.claudeSettings(ctx, name, ours, dir)}
	}
	if managed && (agent == "claude" && canProbeManagedClaudeDirectly() || agent == "codex" && canProbeManagedCodexDirectly()) {
		if agent == "codex" {
			args = []string{"app-server"}
		}
		cmd := exec.CommandContext(ctx, n.bin, append([]string{"launch", name}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), managedModelProbeEnv+"=1")
		if agent == "codex" && m.BaseURL != "" && !slices.Contains(envNames(managedEnv(m)), "CODEX_HOME") {
			codexHome := filepath.Join(dir, "codex-home")
			if err := os.Mkdir(codexHome, 0o700); err != nil {
				return nil, fmt.Errorf("create temporary Codex home: %w", err)
			}
			cmd.Env = append(withoutEnv(cmd.Env, []string{"CODEX_HOME"}), "CODEX_HOME="+codexHome)
		}
		return cmd, nil
	}
	shell, err := interactiveShell()
	if err != nil {
		return nil, err
	}
	launcher := n.launcherForScript(name)
	switch agent {
	case agyAgent:
		launcher += " models"
	case traexAgent:
		// Frame the JSON separately from output printed by the interactive shell's startup files.
		launcher = `builtin printf '\036'; ` + launcher + ` "$@"`
		args = traexArgs
		if len(args) == 0 {
			args = []string{"debug", "models"}
		}
	case "codex":
		if managed {
			// `personad launch` runs codex itself, not the shell's codex codexShell wraps: without the subcommand
			// here the provider's codex started its TUI ("stdin is not a terminal") and listed nothing.
			launcher += " app-server"
		}
		launcher = codexShell(launcher)
	default:
		launcher += ` "$@"`
	}
	cmd := exec.CommandContext(ctx, shell, append([]string{shellFlag(shell), launcher, "persona"}, args...)...)
	cmd.Dir = dir
	return cmd, nil
}

// listAgentModels runs the launcher's agent just long enough to ask for its list, in an empty directory.
func (n *Node) listAgentModels(ctx context.Context, name, agent string) ([]proto.AgentModel, error) {
	dir, err := os.MkdirTemp("", "persona-models-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	cmd, err := n.agentModelsCommand(ctx, name, agent, dir)
	if err != nil {
		return nil, err
	}
	killGroupOnCancel(cmd)
	if agent == agyAgent {
		return discoverAgyModels(ctx, cmd, name)
	}
	if agent == traexAgent {
		models, err := readTraexModelCommand(cmd)
		if (err == nil && len(models) > 0) || ctx.Err() != nil {
			return models, err
		}
		// Older versions may only have the public catalog, which omits per-model effort metadata.
		fallback, fallbackErr := n.agentModelsCommand(ctx, name, agent, dir, "models", "--json")
		if fallbackErr != nil {
			return nil, errors.Join(err, fallbackErr)
		}
		killGroupOnCancel(fallback)
		models, fallbackErr = readTraexModelCommand(fallback)
		if fallbackErr != nil {
			return nil, errors.Join(err, fallbackErr)
		}
		return models, nil
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr tailBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	defer func() {
		_ = cmd.Cancel() // the whole group: it would sit waiting for a message
		_ = cmd.Wait()
	}()
	type result struct {
		models []proto.AgentModel
		err    error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		if agent == "codex" {
			r.models, r.err = codexModelList(stdin, stdout)
		} else {
			r.models, r.err = claudeModelList(stdin, stdout)
		}
		done <- r
	}()
	select {
	case r := <-done:
		if r.err != nil {
			if tail := strings.TrimSpace(redactSecrets(lastLines(stderr.String(), 4))); tail != "" {
				return nil, fmt.Errorf("%s 没有给出模型列表：%w（%s）", name, r.err, clipText(tail, 300))
			}
			return nil, fmt.Errorf("%s 没有给出模型列表：%w", name, r.err)
		}
		if agent == "claude" {
			r.models = n.launcherSettingsModelDefault(ctx, name, dir, r.models)
		}
		return r.models, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%s 在 %d 秒内没有给出模型列表", name, int(launcherModelsTimeout.Seconds()))
	}
}

func readTraexModelCommand(cmd *exec.Cmd) ([]proto.AgentModel, error) {
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("TRAE CLI model catalog: %w", err)
	}
	_, catalog, ok := bytes.Cut(out, []byte{0x1e})
	if !ok {
		return nil, errors.New("TRAE CLI model catalog: missing catalog marker")
	}
	return traexModelList(catalog)
}

// Both catalogs use config_name for -m. The debug catalog has the visibility and effort fields.
func traexModelList(out []byte) ([]proto.AgentModel, error) {
	type row struct {
		codexCachedModel
		ID   string `json:"config_name"`
		Name string `json:"name"`
	}
	var doc struct {
		Models []row `json:"models"`
	}
	var catalog []row
	out = bytes.TrimSpace(out)
	var err error
	if len(out) > 0 && out[0] == '{' {
		err = json.Unmarshal(out, &doc)
		catalog = doc.Models
	} else {
		err = json.Unmarshal(out, &catalog)
	}
	if err != nil {
		return nil, fmt.Errorf("decode TRAE CLI model catalog: %w", err)
	}
	models := []proto.AgentModel{}
	seen := map[string]bool{}
	for _, m := range catalog {
		if !m.visible() || !proto.ValidModelID(m.ID) || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		efforts, def, _ := m.efforts()
		efforts = proto.ProjectEfforts(efforts, proto.CodexEffortLevels)
		if !slices.Contains(efforts, def) {
			def = ""
		}
		models = append(models, proto.AgentModel{ID: m.ID, Name: firstOf(m.Name, m.DisplayName, m.Slug, m.ID),
			Description: m.Description, Efforts: efforts, DefaultEffort: def})
	}
	return models, nil
}

// discoverAgyModels waits for the catalog command to exit so a failed discovery cannot cache a partial list.
func discoverAgyModels(ctx context.Context, cmd *exec.Cmd, name string) ([]proto.AgentModel, error) {
	var stderr, stdoutTail tailBuffer
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s models: %w", name, err)
	}
	stopReading := context.AfterFunc(ctx, func() { _ = stdout.Close() })
	defer stopReading()
	models, windows, parseErr := agyModelListWithWindows(io.TeeReader(stdout, &stdoutTail))
	if parseErr != nil && ctx.Err() == nil {
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s models: %w", name, ctx.Err())
	}
	if err := errors.Join(parseErr, waitErr); err != nil {
		stderrText := clipText(strings.TrimSpace(redactSecrets(lastLines(stderr.String(), 4))), 150)
		stdoutText := clipText(strings.TrimSpace(redactSecrets(lastLines(stdoutTail.String(), 4))), 150)
		detail := strings.TrimSpace(stderrText + "\n" + stdoutText)
		if detail != "" {
			return nil, fmt.Errorf("%s models failed: %w (%s)", name, err, detail)
		}
		return nil, fmt.Errorf("%s models failed: %w", name, err)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("%s models returned no models", name)
	}
	recordAgyModelWindows(windows)
	recordAgyModelCatalog(models)
	return models, nil
}

// agyModelList reads the same tab-separated catalog as the Antigravity companion, retaining the first row per ID.
// Effort suffixes identify variants of the same model and advertise the levels present in the catalog.
func agyModelList(r io.Reader) ([]proto.AgentModel, error) {
	models, _, err := agyModelListWithWindows(r)
	return models, err
}

// agyModelListWithWindows parses `agy models` rows and also extracts any explicit context window token counts
// declared in extra tab-separated columns without guessing when omitted.
func agyModelListWithWindows(r io.Reader) ([]proto.AgentModel, map[string]int, error) {
	var models []proto.AgentModel
	seen := map[string]bool{}
	var windows map[string]int
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		id, rest, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "\t")
		if !ok || id == "" || strings.IndexFunc(id, unicode.IsSpace) >= 0 || seen[id] {
			continue
		}
		name, extra, hasExtra := strings.Cut(strings.TrimLeft(rest, "\t"), "\t")
		if name == "" {
			name = id
		}
		seen[id] = true
		models = append(models, proto.AgentModel{ID: id, Name: name})
		if hasExtra {
			for col := range strings.SplitSeq(extra, "\t") {
				if w := parseAgyModelWindow(col); w > 0 {
					if windows == nil {
						windows = map[string]int{}
					}
					windows[strings.ToLower(id)] = w
					break
				}
			}
		}
	}
	for i := range models {
		model := &models[i]
		base := model.ID
		for _, level := range proto.EffortLevels {
			if prefix, ok := strings.CutSuffix(base, "-"+level); ok {
				model.DefaultEffort, base = level, prefix
				break
			}
		}
		for _, level := range proto.EffortLevels {
			if seen[base+"-"+level] {
				model.Efforts = append(model.Efforts, level)
			}
		}
	}
	return models, windows, scanner.Err()
}

// claudeModelList asks a headless claude (stream-json both ways) for the models of its /model menu.
func claudeModelList(w io.Writer, r io.Reader) ([]proto.AgentModel, error) {
	req := `{"type":"control_request","request_id":"persona-models","request":{"subtype":"initialize"}}` + "\n"
	if _, err := io.WriteString(w, req); err != nil {
		return nil, err
	}
	var models []struct {
		Value           string   `json:"value"`
		ResolvedModel   string   `json:"resolvedModel"`
		DisplayName     string   `json:"displayName"`
		Description     string   `json:"description"`
		SupportsEffort  bool     `json:"supportsEffort"`
		SupportedEffort []string `json:"supportedEffortLevels"`
	}
	err := jsonLines(r, func(line []byte) (bool, error) {
		var msg struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string          `json:"subtype"`
				RequestID string          `json:"request_id"`
				Error     string          `json:"error"`
				Response  json.RawMessage `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(line, &msg) != nil || msg.Type != "control_response" || msg.Response.RequestID != "persona-models" {
			return false, nil
		}
		if msg.Response.Subtype == "error" {
			return true, errors.New(msg.Response.Error)
		}
		var body struct {
			Models json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(msg.Response.Response, &body); err != nil {
			return true, err
		}
		if len(body.Models) == 0 || string(body.Models) == "null" {
			return true, errors.New("这个版本的 Claude Code 不提供模型列表，升级后再试")
		}
		return true, json.Unmarshal(body.Models, &models)
	})
	if err != nil {
		return nil, err
	}
	out := make([]proto.AgentModel, 0, len(models))
	seen := map[string]bool{}
	for _, m := range models {
		// An id listed again is left out, the first stays: Claude Code lists "default" twice when its settings say
		// "model": "default", and the Android panel closed on two rows of one model (#9998).
		if m.Value == "" || seen[m.Value] {
			continue
		}
		seen[m.Value] = true
		a := proto.AgentModel{ID: m.Value, Name: m.DisplayName, Description: m.Description, Default: m.Value == "default"}
		if m.ResolvedModel != m.Value {
			a.Resolved = m.ResolvedModel
		}
		if a.Name == "" {
			a.Name = m.Value
		}
		if m.SupportsEffort {
			// Only the levels a start takes (node.validModes); the order is the CLI's, weakest first.
			for _, e := range m.SupportedEffort {
				if slices.Contains(proto.EffortLevels, e) {
					a.Efforts = append(a.Efforts, e)
				}
			}
		}
		out = append(out, a)
	}
	return out, nil
}

// codexModelList asks a codex app-server (JSON-RPC over stdio) for model/list, page by page; hidden models are left
// out as its own picker does.
func codexModelList(w io.Writer, r io.Reader) ([]proto.AgentModel, error) {
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		readErr <- jsonLines(r, func(line []byte) (bool, error) {
			select {
			case lines <- append([]byte(nil), line...):
				return false, nil
			case <-stop:
				return true, nil
			}
		})
		close(lines)
	}()
	call := func(id int, method string, params any, into any) error {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
		for line := range lines {
			var msg struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(line, &msg) != nil || msg.ID == nil || *msg.ID != id || msg.Method != "" {
				continue // a notification, or a request of the server's: nothing this asks for
			}
			if msg.Error != nil {
				return errors.New(msg.Error.Message)
			}
			if into == nil {
				return nil
			}
			return json.Unmarshal(msg.Result, into)
		}
		if err := <-readErr; err != nil {
			return err
		}
		return io.ErrUnexpectedEOF
	}
	if err := call(1, "initialize", map[string]any{"clientInfo": map[string]any{"name": "persona", "title": "Persona", "version": "1"}}, nil); err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(`{"jsonrpc":"2.0","method":"initialized"}` + "\n")); err != nil {
		return nil, err
	}
	var out []proto.AgentModel
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var res struct {
			Data []struct {
				Model         string              `json:"model"`
				ID            string              `json:"id"`
				DisplayName   string              `json:"displayName"`
				Description   string              `json:"description"`
				Hidden        bool                `json:"hidden"`
				IsDefault     bool                `json:"isDefault"`
				DefaultEffort string              `json:"defaultReasoningEffort"`
				ServiceTiers  []proto.ServiceTier `json:"serviceTiers"`
				Efforts       []struct {
					Effort string `json:"reasoningEffort"`
				} `json:"supportedReasoningEfforts"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := call(2+page, "model/list", params, &res); err != nil {
			return nil, err
		}
		for _, m := range res.Data {
			id := m.Model // what -m / thread/start take; id names the preset
			if id == "" {
				id = m.ID
			}
			if m.Hidden || id == "" {
				continue
			}
			a := proto.AgentModel{ID: id, Name: m.DisplayName, Description: m.Description, Default: m.IsDefault, DefaultEffort: m.DefaultEffort, ServiceTiers: m.ServiceTiers}
			if a.Name == "" {
				a.Name = id
			}
			for _, e := range m.Efforts {
				if e.Effort != "" {
					a.Efforts = append(a.Efforts, e.Effort)
				}
			}
			if len(a.Efforts) == 0 && a.DefaultEffort == "none" { // a model that takes no level (a gateway catalog's) has no default one
				a.DefaultEffort = ""
			}
			out = append(out, a)
		}
		if res.NextCursor == nil || *res.NextCursor == "" {
			break
		}
		cursor = *res.NextCursor
	}
	if out == nil {
		out = []proto.AgentModel{}
	}
	return out, nil
}

// jsonLines calls fn with each line of r that looks like a JSON object (an interactive zsh may print banners first)
// until fn says it is done.
func jsonLines(r io.Reader, fn func([]byte) (bool, error)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16<<20) // a gateway's whole catalogue arrives on one line
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		if done, err := fn(line); done || err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

// tailBuffer keeps the last 8 KB written to it (a process's stderr, for the error message).
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 8<<10 {
		t.buf = t.buf[len(t.buf)-8<<10:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
