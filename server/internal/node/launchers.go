package node

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/shellcmd"
)

var launcherCache struct {
	sync.Mutex
	at          time.Time
	list        []proto.Launcher
	generation  uint64             // incremented whenever a discovery publishes a new list
	invalidated uint64             // incremented when local PATH/config changes invalidate cached discovery
	fetch       singleflight.Group // concurrent asks share the login shell's result, including failure
	fetching    bool               // a background refresh (cachedLaunchers) is running
	// codex: how each Codex launcher runs (codex_start.go), its words as the shell would run them.
	codex map[string][]string
	// codexAliases distinguishes configured aliases from the plain PATH/bundled launchers, even when their argv matches.
	codexAliases map[string]bool
	// settings: the --settings a claude alias brings itself, as its words give it (launcher_settings.go).
	settings map[string]string
	// scripts: where each wrapper script on PATH is (pathlaunchers.go).
	scripts map[string]string
}

// invalidateLauncherCache clears cached shell/PATH launcher discovery after local environment
// changes (such as node.configure updating extra_path) so the next launchers.list re-scans.
func invalidateLauncherCache() {
	launcherCache.Lock()
	launcherCache.at = time.Time{}
	launcherCache.invalidated++
	launcherCache.Unlock()
	lastPathScan.Lock()
	lastPathScan.got = nil
	lastPathScan.Unlock()
}

// launcherScript lists the login shell's launchers in one go (sourcing ~/.zshrc takes a while): what the
// `_claude_launchers` helper prints when the rc file has one, a GS byte, then every alias as its name and its words
// as zsh itself splits and unquotes them (name US word US … RS), so quoting never has to be parsed here (bash prints
// `alias -p` after bashAliasMark instead, launchers_shell.go); then, each after a GS byte, where codex, traex, omp,
// pi, cursor-agent and agy are on the PATH, the names of the shell's functions and aliases (one a line: they come
// before a file on PATH of the same name), its PATH (for the wrapper scripts on it, pathlaunchers.go), and
// the Claude executable on that original PATH. All of it between launcherBegin and launcherEnd, so what
// the rc files print around it is never read as a section (launcherOutput).
const launcherScript = `printf '\001persona-launchers-begin\001'
__jl_path=$PATH
_claude_launchers 2>/dev/null || true
printf '\035'
if [ -n "$ZSH_VERSION" ]; then
  eval 'for __k __v in "${(@kv)aliases}"; do
    print -rn -- "$__k"$'\''\x1f'\''
    for __w in "${(@Q)${(z)__v}}"; do print -rn -- "$__w"$'\''\x1f'\''; done
    print -rn -- $'\''\x1e'\''
  done'
elif [ -n "$BASH_VERSION" ]; then
  printf '\002'
  alias -p
fi
printf '\035'
PATH="$PATH:$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin"
if [ -n "$ZSH_VERSION" ]; then whence -p codex 2>/dev/null; else command -v codex 2>/dev/null; fi
printf '\035'
if [ -n "$ZSH_VERSION" ]; then whence -p traex 2>/dev/null; else command -v traex 2>/dev/null; fi
printf '\035'
if [ -n "$ZSH_VERSION" ]; then whence -p omp 2>/dev/null; else command -v omp 2>/dev/null; fi
printf '\035'
if [ -n "$ZSH_VERSION" ]; then whence -p pi 2>/dev/null; else command -v pi 2>/dev/null; fi
printf '\035'
if [ -n "$ZSH_VERSION" ]; then whence -p cursor-agent 2>/dev/null; else command -v cursor-agent 2>/dev/null; fi
printf '\035'
if [ -n "$ZSH_VERSION" ]; then whence -p agy 2>/dev/null; else command -v agy 2>/dev/null; fi
printf '\035'
if [ -n "$ZSH_VERSION" ]; then eval 'print -rl -- ${(k)functions} ${(k)aliases}'; elif [ -n "$BASH_VERSION" ]; then compgen -A function; compgen -a; fi
printf '\035'
printf '%s' "$__jl_path"
printf '\035'
PATH=$__jl_path
if [ -n "$ZSH_VERSION" ]; then builtin whence -p claude 2>/dev/null; else builtin type -P claude 2>/dev/null; fi
printf '\001persona-launchers-end\001'`

// launcherBegin and launcherEnd frame what launcherScript prints (launcherOutput).
const (
	launcherBegin = "\x01persona-launchers-begin\x01"
	launcherEnd   = "\x01persona-launchers-end\x01"
)

// launcherOutput returns the script's framed output and whether the frame completed. Ignore rc startup text and
// exit traps outside the markers (#6310). An interrupted script can leave a body without its end marker.
func launcherOutput(out string) (string, bool) {
	i := strings.LastIndex(out, launcherBegin)
	if i < 0 {
		return "", false
	}
	body, _, complete := strings.Cut(out[i+len(launcherBegin):], launcherEnd)
	return body, complete
}

// launcherFetchObserverKey carries a test-only signal after a request joins the shared scan.
type launcherFetchObserverKey struct{}

// launchers lists the launchers of this machine's login shell: the `_claude_launchers` helper's (claude wrapper
// functions), then every alias that runs claude (`alias dmcc="ANTHROPIC_BASE_URL=… claude …"`), by name. A name
// the helper lists is not repeated. With no configured launcher, plain claude is listed when installed.
// A forced refresh requires a discovery newer than the one visible when the request arrived, and reports
// a failed scan even when an older list exists.
func (n *Node) launchers(ctx context.Context, refresh ...bool) ([]proto.Launcher, error) {
	forced := len(refresh) > 0 && refresh[0]
	launcherCache.Lock()
	observed := launcherCache.generation
	cached := launcherCache.list
	fresh := time.Since(launcherCache.at) < time.Minute
	launcherCache.Unlock()
	if !forced && fresh && cached != nil {
		return cached, nil
	}

	type discovery struct {
		list        []proto.Launcher
		generation  uint64
		invalidated uint64
		published   bool
	}
	read := func() (any, error) {
		// A scan may have completed between the initial cache read and entry into the group.
		launcherCache.Lock()
		inv := launcherCache.invalidated
		current := discovery{
			list:        launcherCache.list,
			generation:  launcherCache.generation,
			invalidated: inv,
			published:   true,
		}
		fresh := time.Since(launcherCache.at) < time.Minute
		launcherCache.Unlock()
		if current.list != nil && fresh && (current.generation != observed || !forced) {
			return current, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Only a scan that has started outlives its caller. It can still populate the cache for a later ask.
		scanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		out, err := scanLaunchersFunc(scanCtx)
		if err != nil {
			return nil, err
		}
		list, codex, settings, scripts := mergeLaunchers(string(out))
		aliases := codexAliasNames(string(out))
		launcherCache.Lock()
		if launcherCache.invalidated == inv {
			launcherCache.at, launcherCache.list, launcherCache.codex, launcherCache.settings = time.Now(), list, codex, settings
			launcherCache.codexAliases = aliases
			launcherCache.scripts = scripts
			launcherCache.generation++
			current = discovery{
				list:        list,
				generation:  launcherCache.generation,
				invalidated: inv,
				published:   true,
			}
		} else {
			current = discovery{
				list:        list,
				generation:  launcherCache.generation,
				invalidated: inv,
				published:   false,
			}
		}
		launcherCache.Unlock()
		return current, nil
	}
	entered, _ := ctx.Value(launcherFetchObserverKey{}).(func())
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result := launcherCache.fetch.DoChan("shell", read)
		if entered != nil {
			entered()
			entered = nil
		}
		shared := <-result
		if err := shared.Err; err != nil {
			if canceled := ctx.Err(); canceled != nil {
				return nil, canceled
			}
			if err == context.Canceled || err == context.DeadlineExceeded {
				// The group's leader was canceled before starting a scan. A live caller can still start one.
				continue
			}
			if forced {
				return nil, err
			}
			return lastLaunchers(err)
		}
		got := shared.Val.(discovery)
		launcherCache.Lock()
		curInv := launcherCache.invalidated
		launcherCache.Unlock()
		// An invalidation that raced with the shared scan makes pre-invalidation output stale; re-scan.
		if !got.published || got.invalidated != curInv {
			continue
		}
		// An ordinary cache hit shared through the group cannot satisfy a forced request for newer data.
		if !forced || got.generation != observed {
			return got.list, nil
		}
	}
}

var scanLaunchersFunc = func(ctx context.Context) ([]byte, error) {
	return scanLaunchers(ctx)
}

func scanLaunchers(ctx context.Context, env ...string) ([]byte, error) {
	began := time.Now()
	out, err := launcherShellOutput(ctx, launcherScript, env...)
	// A background child can hold stdout open after a complete scan. A partial frame can be just a banner.
	if _, complete := launcherOutput(string(out)); !complete {
		took := time.Since(began).Round(time.Millisecond)
		if err != nil {
			return nil, fmt.Errorf("list launchers: incomplete shell output after %v: %w", took, err)
		}
		return nil, fmt.Errorf("list launchers: the login shell exited before completing the list after %v", took)
	}
	return out, nil
}

func launcherShellOutput(ctx context.Context, script string, env ...string) ([]byte, error) {
	cmd := loginShell(ctx, script)
	cmd.Env = append(cmd.Environ(), env...)
	// A shell's children can hold stdout open after it dies. Bound the group and the pipe wait (#10952).
	killGroupOnCancel(cmd)
	return cmd.Output()
}

// lastLaunchers answers a failed read of the login shell (launchers): the last list, kept for another minute so the
// next try is not on every ask while a slow shell stays slow; the error only when there has been no list yet.
func lastLaunchers(failed error) ([]proto.Launcher, error) {
	launcherCache.Lock()
	defer launcherCache.Unlock()
	if launcherCache.list == nil {
		return nil, failed
	}
	log.Printf("node: %v; keeping the last %d launchers", failed, len(launcherCache.list))
	launcherCache.at = time.Now()
	return launcherCache.list, nil
}

// mergeLaunchers is launcherScript's output as the launcher list: the helper's, then the aliases it does not name,
// then the wrapper scripts on PATH no function or alias shadows (otherwise plain claude, when installed), then Codex's — plain
// codex when it is on the PATH (else the ChatGPT app's bundled one, bundledCodexCLI), and the aliases that run it (how each runs: codex) — then plain traex and plain omp when
// they are on the PATH, then pi, the Cursor CLI and the Antigravity CLI when their programs are (workagent.go: started in
// a herdr pane with the model the start names). settings: the claude aliases' and scripts' own --settings, by
// name (launcher_settings.go); an alias is what the shell runs for its name even when the helper lists the name too.
// scripts: where each PATH launcher is. A helper row can learn its config directory from an unshadowed PATH file.
func mergeLaunchers(out string) (list []proto.Launcher, codex map[string][]string, settings, scripts map[string]string) {
	body, _ := launcherOutput(out)
	helper, rest, _ := strings.Cut(body, "\x1d")
	aliases, rest, _ := strings.Cut(rest, "\x1d")
	if a, ok := strings.CutPrefix(aliases, bashAliasMark); ok {
		aliases = bashAliases(a)
	}
	codexPath, rest, _ := strings.Cut(rest, "\x1d")
	traexPath, rest, _ := strings.Cut(rest, "\x1d")
	ompPath, rest, _ := strings.Cut(rest, "\x1d")
	piPath, rest, _ := strings.Cut(rest, "\x1d")
	cursorPath, rest, _ := strings.Cut(rest, "\x1d")
	agyPath, rest, _ := strings.Cut(rest, "\x1d")
	shellNames, rest, _ := strings.Cut(rest, "\x1d")
	pathVar, claudePath, _ := strings.Cut(rest, "\x1d")
	list = parseLaunchers(helper)
	if list == nil {
		list = []proto.Launcher{} // an empty discovery is still a valid cache entry
	}
	helperRows := map[string]int{}
	for i, l := range list {
		helperRows[l.Name] = i
	}
	claudeAliases, settings := parseAliasLaunchers(aliases)
	for _, l := range claudeAliases {
		if _, listed := helperRows[l.Name]; !listed {
			list = append(list, l)
		}
	}
	taken := map[string]bool{}
	for _, name := range strings.Fields(shellNames) {
		taken[name] = true
	}
	for _, rec := range strings.Split(aliases, "\x1e") {
		if name, _, ok := strings.Cut(rec, "\x1f"); ok {
			taken[name] = true
		}
	}
	pathList, pathSettings, scripts := pathLaunchers(pathVar, taken)
	for _, l := range pathList {
		if i, listed := helperRows[l.Name]; listed {
			// A name printed by the helper can still resolve to the file on PATH.
			list[i].ConfigDir = l.ConfigDir
			delete(pathSettings, l.Name)
			delete(scripts, l.Name)
			continue
		}
		list = append(list, l)
	}
	maps.Copy(settings, pathSettings)
	if len(list) == 0 && strings.TrimSpace(claudePath) != "" {
		list = append(list, proto.Launcher{Name: "claude", DisplayName: "Claude Code"})
	}
	codex = map[string][]string{}
	ls, words := parseCodexAliases(aliases)
	codexWord := ""
	_, helperCodex := helperRows["codex"]
	if strings.TrimSpace(codexPath) != "" {
		codexWord = "codex"
	} else if !taken["codex"] && !helperCodex {
		codexWord = bundledCodexCLI() // the ChatGPT app's own, by its path: the shell would not find it
	}
	if codexWord != "" && words["codex"] == nil {
		list = append(list, proto.Launcher{Name: "codex", Agent: "codex", Source: "zshrc", DisplayName: "Codex", PromptOptional: true, RunHere: true})
		codex["codex"] = []string{codexWord}
	}
	for _, l := range ls {
		if _, listed := helperRows[l.Name]; !listed {
			list = append(list, l)
			codex[l.Name] = words[l.Name]
		}
	}
	if _, listed := helperRows[traexAgent]; strings.TrimSpace(traexPath) != "" && !listed && codex[traexAgent] == nil {
		list = append(list, proto.Launcher{Name: traexAgent, Agent: traexAgent, Source: "zshrc", DisplayName: "TRAE CLI"})
	}
	if _, listed := helperRows["omp"]; strings.TrimSpace(ompPath) != "" && !listed && codex["omp"] == nil {
		list = append(list, proto.Launcher{Name: "omp", Agent: "omp", Source: "zshrc", DisplayName: "Oh My Pi"})
	}
	for _, bin := range []struct{ name, agent, displayName, path string }{
		{"pi", piAgent, "", piPath},
		{"cursor", cursorAgent, "Cursor", cursorPath},
		{"agy", agyAgent, "Antigravity CLI", agyPath},
	} {
		if _, listed := helperRows[bin.name]; strings.TrimSpace(bin.path) != "" && !listed && codex[bin.name] == nil {
			list = append(list, proto.Launcher{Name: bin.name, Agent: bin.agent, Source: "zshrc",
				DisplayName: bin.displayName, RuntimeFallback: bin.agent == agyAgent && !taken[bin.name]})
		}
	}
	return list, codex, settings, scripts
}

// codexAliasNames keeps the provenance mergeLaunchers' argv map alone cannot carry:
// a configured no-op alias and the plain PATH launcher both have the words ["codex"].
func codexAliasNames(out string) map[string]bool {
	body, _ := launcherOutput(out)
	_, rest, _ := strings.Cut(body, "\x1d")
	aliases, _, _ := strings.Cut(rest, "\x1d")
	if a, ok := strings.CutPrefix(aliases, bashAliasMark); ok {
		aliases = bashAliases(a)
	}
	_, words := parseCodexAliases(aliases)
	names := make(map[string]bool, len(words))
	for name := range words {
		names[name] = true
	}
	return names
}

// cachedLaunchers never waits for the login shell: it returns the last list (possibly stale, nil before the first
// one) and starts a refresh in the background when the list is missing or old.
func (n *Node) cachedLaunchers() []proto.Launcher {
	launcherCache.Lock()
	defer launcherCache.Unlock()
	if (launcherCache.list == nil || time.Since(launcherCache.at) >= time.Minute) && !launcherCache.fetching {
		launcherCache.fetching = true
		go func() {
			_, _ = n.launchers(context.Background())
			launcherCache.Lock()
			launcherCache.fetching = false
			launcherCache.Unlock()
		}()
	}
	return launcherCache.list
}

// inferLauncher names the launcher of a claude Persona did not start (typed in a terminal), from the
// endpoint and model the scan saw in its environment. The config directory breaks ties only after neither
// launcher list yields a match by endpoint and model. It returns "" when the launcher is unknown.
func (n *Node) inferLauncher(id string) string {
	env, ok := n.sessionEnv(id)
	if !ok {
		return ""
	}
	cached := n.cachedLaunchers()
	if name := matchLauncher(cached, env.BaseURL, env.Model); name != "" {
		return name
	}
	managed := n.managedInferLaunchers()
	if name := matchLauncher(managed, env.BaseURL, env.Model); name != "" {
		return name
	}
	if env.ConfigDir == "" {
		return ""
	}
	if env.BaseURL == "" {
		// A default-endpoint match must be unique across both launcher lists.
		return matchLauncher(slices.Concat(cached, managed), "", env.Model, env.ConfigDir)
	}
	if name := matchLauncher(cached, env.BaseURL, env.Model, env.ConfigDir); name != "" {
		return name
	}
	return matchLauncher(managed, env.BaseURL, env.Model, env.ConfigDir)
}

// managedInferLaunchers returns the cached Claude managed-launcher projection so a terminal session started manually
// with `personad launch <name>` can infer its managed launcher when the endpoint/model match is unambiguous.
func (n *Node) managedInferLaunchers() []proto.Launcher {
	if n == nil {
		return nil
	}
	n.managed.mu.Lock()
	if n.managed.infer == nil && len(n.managed.byID) > 0 {
		n.managed.infer = buildManagedInferLaunchers(n.managed.byID)
	}
	out := n.managed.infer
	n.managed.mu.Unlock()
	return out
}

func buildManagedInferLaunchers(byID map[string]proto.ManagedLauncher) []proto.Launcher {
	if len(byID) == 0 {
		return nil
	}
	home := userHome()
	out := make([]proto.Launcher, 0, len(byID))
	for _, l := range byID {
		if a := l.AgentType(); a != "" && a != "claude" {
			continue
		}
		pm, _ := providerModel(l, l.Model)
		out = append(out, proto.Launcher{
			Name:      l.Name,
			BaseURL:   managedBaseURL(l),
			Model:     sdkModel(l.Model, pm),
			ConfigDir: literalConfigDir(providerEnv(l, "", "", false)["CLAUDE_CONFIG_DIR"], home),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// literalConfigDir resolves a literal CLAUDE_CONFIG_DIR value from a launcher definition (expanding a leading
// $HOME, ${HOME} or ~ against home). Returns "" when the value still depends on shell expansion (other
// variables or command substitution).
func literalConfigDir(v, home string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	rest := v
	for _, h := range []string{"${HOME}", "$HOME"} {
		if v == h || strings.HasPrefix(v, h+"/") {
			rest = v[len(h):]
			break
		}
	}
	if strings.ContainsAny(rest, "$`") {
		return ""
	}
	p := shellPath(v, home)
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

// normalizeConfigDir canonicalizes a config directory path for comparison, expanding ~ / $HOME and resolving
// symlinks when the directory exists on disk.
func normalizeConfigDir(dir, home string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(shellPath(dir, home))
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}

// matchLauncher picks the launcher that exports this base URL and model (and optional CLAUDE_CONFIG_DIR).
// Several launchers can share one endpoint (and a launcher whose model is chosen at run time shows a placeholder
// such as "(claude-choose)"), so: when baseURL is non-empty, the one launcher with both equal (an empty model is
// unknown, never evidence of a match); else the one launcher with that base URL; else, when baseURL matching is
// ambiguous, the one matching candidate with the same ConfigDir. When baseURL is empty, the one launcher with an
// empty base URL and the same ConfigDir. Never a guess between several.
func matchLauncher(list []proto.Launcher, baseURL, model string, configDir ...string) string {
	home := userHome()
	normCfgDir := ""
	if len(configDir) > 0 {
		normCfgDir = normalizeConfigDir(configDir[0], home)
	}
	if baseURL == "" {
		if normCfgDir == "" {
			return "" // the default endpoint with no config dir: nothing to tell launchers apart by
		}
		var byDir []string
		for _, l := range list {
			if l.BaseURL != "" || l.ConfigDir == "" {
				continue
			}
			if normalizeConfigDir(l.ConfigDir, home) == normCfgDir {
				byDir = append(byDir, l.Name)
			}
		}
		if len(byDir) == 1 {
			return byDir[0]
		}
		return ""
	}
	var byURL, byBoth, byURLAndDir, byBothAndDir []string
	for _, l := range list {
		if l.BaseURL != baseURL {
			continue
		}
		byURL = append(byURL, l.Name)
		dirMatch := normCfgDir != "" && l.ConfigDir != "" && normalizeConfigDir(l.ConfigDir, home) == normCfgDir
		if dirMatch {
			byURLAndDir = append(byURLAndDir, l.Name)
		}
		if model != "" && (l.Model == model || l.Model != "" && proto.TrimModelWindow(l.Model) == proto.TrimModelWindow(model)) {
			byBoth = append(byBoth, l.Name)
			if dirMatch {
				byBothAndDir = append(byBothAndDir, l.Name)
			}
		}
	}
	if len(byBoth) == 1 {
		return byBoth[0]
	}
	if len(byURL) == 1 {
		return byURL[0]
	}
	if len(byBothAndDir) == 1 {
		return byBothAndDir[0]
	}
	if len(byBoth) == 0 && len(byURLAndDir) == 1 {
		return byURLAndDir[0]
	}
	return ""
}

// parseAliasLaunchers reads launcherScript's alias records and keeps the aliases that start claude: after the
// variables set in front of it (`A=1 B=2 claude …`), through env / command / exec / noglob, by path or not, with
// `export A=1;` before it in the same alias. Their gateway and model come from ANTHROPIC_BASE_URL and ANTHROPIC_MODEL
// (or --model). Codex's are parseCodexAliases'. Sorted by name: the shell keeps no order. settings: the ones that
// bring their own --settings, by name (launcher_settings.go).
func parseAliasLaunchers(out string) (list []proto.Launcher, settings map[string]string) {
	settings = map[string]string{}
	for _, rec := range strings.Split(out, "\x1e") {
		words := strings.Split(rec, "\x1f")
		if len(words) < 2 {
			continue
		}
		name := words[0]
		if !shellcmd.NameRe.MatchString(name) || name == "claude" {
			continue
		}
		if l, s, ok := aliasLauncher(words[1 : len(words)-1]); ok {
			l.Name = name
			list = append(list, l)
			if s != "" {
				settings[name] = s
			}
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, settings
}

// parseCodexAliases keeps the aliases that run Codex: `export` statements and one codex command (through env /
// command / exec / noglob, with variables set in front). How each runs is its words for codex-start: the variables,
// then the command and its arguments. Its model is -m / --model or -c model=…; an alias that does more (`cd x && codex`)
// is left out.
func parseCodexAliases(out string) ([]proto.Launcher, map[string][]string) {
	var list []proto.Launcher
	words := map[string][]string{}
	for _, rec := range strings.Split(out, "\x1e") {
		w := strings.Split(rec, "\x1f")
		if len(w) < 2 || !shellcmd.NameRe.MatchString(w[0]) {
			continue
		}
		argv, model, ok := codexAliasArgv(w[1 : len(w)-1])
		if !ok {
			continue
		}
		list = append(list, proto.Launcher{Name: w[0], Model: model, Agent: "codex", Source: "zshrc", PromptOptional: true, RunHere: true})
		words[w[0]] = argv
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, words
}

// codexAliasArgv reads an alias's words as one codex command; ok false when it is anything else.
func codexAliasArgv(words []string) (argv []string, model string, ok bool) {
	var exported []string
	found := false
	for len(words) > 0 {
		end := len(words)
		for i, w := range words {
			if w == ";" || w == "&&" || w == "||" || w == "|" || w == "&" {
				end = i
				break
			}
		}
		cmd := words[:end]
		if end < len(words) {
			words = words[end+1:]
		} else {
			words = nil
		}
		if len(cmd) == 0 {
			continue
		}
		if cmd[0] == "export" {
			for _, w := range cmd[1:] {
				if shellcmd.AssignRe.MatchString(w) {
					exported = append(exported, w)
				}
			}
			continue
		}
		if found {
			return nil, "", false // more than one command
		}
		env := map[string]string{}
		i := shellcmd.CommandIndex(cmd, env)
		if i < 0 || filepath.Base(cmd[i]) != "codex" {
			return nil, "", false
		}
		found = true
		argv = append([]string(nil), exported...)
		for _, w := range cmd[:i] {
			if shellcmd.AssignRe.MatchString(w) {
				argv = append(argv, w)
			}
		}
		argv = append(argv, cmd[i:]...)
		for j := i + 1; j < len(cmd); j++ {
			switch {
			case (cmd[j] == "-m" || cmd[j] == "--model") && j+1 < len(cmd):
				model = cmd[j+1]
			case strings.HasPrefix(cmd[j], "--model="):
				model = strings.TrimPrefix(cmd[j], "--model=")
			case (cmd[j] == "-c" || cmd[j] == "--config") && j+1 < len(cmd) && strings.HasPrefix(cmd[j+1], "model="):
				model = strings.Trim(strings.TrimPrefix(cmd[j+1], "model="), `"'`)
			}
		}
	}
	return argv, model, found
}

// aliasLauncher finds the claude command in an alias's words and what it is started with; settings is its own
// --settings (the last one, as Claude Code takes it), "" for none: launcher_settings.go.
func aliasLauncher(words []string) (l proto.Launcher, settings string, ok bool) {
	exported := map[string]string{}
	for len(words) > 0 {
		end := len(words)
		for i, w := range words {
			if w == ";" || w == "&&" || w == "||" || w == "|" || w == "&" {
				end = i
				break
			}
		}
		cmd := words[:end]
		if end < len(words) {
			words = words[end+1:]
		} else {
			words = nil
		}
		if len(cmd) > 0 && cmd[0] == "export" {
			for _, w := range cmd[1:] {
				if k, v, ok := strings.Cut(w, "="); ok && shellcmd.AssignRe.MatchString(w) {
					exported[k] = v
				}
			}
			continue
		}
		env := map[string]string{}
		for k, v := range exported {
			env[k] = v
		}
		i := shellcmd.CommandIndex(cmd, env)
		if i < 0 {
			continue
		}
		if filepath.Base(cmd[i]) != "claude" {
			continue
		}
		l = proto.Launcher{
			BaseURL:   env["ANTHROPIC_BASE_URL"],
			Model:     env["ANTHROPIC_MODEL"],
			ConfigDir: literalConfigDir(env["CLAUDE_CONFIG_DIR"], userHome()),
			Source:    "zshrc",
		}
		for j := i + 1; j < len(cmd); j++ {
			if cmd[j] == "--model" && j+1 < len(cmd) && l.Model == "" {
				l.Model = cmd[j+1]
			} else if m, ok := strings.CutPrefix(cmd[j], "--model="); ok && l.Model == "" {
				l.Model = m
			}
			if cmd[j] == "--settings" && j+1 < len(cmd) {
				settings = cmd[j+1]
			} else if s, ok := strings.CutPrefix(cmd[j], "--settings="); ok {
				settings = s
			}
		}
		return l, settings, true
	}
	return proto.Launcher{}, "", false
}

// parseLaunchers parses `_claude_launchers` output: name padded to 26, base URL padded to 40, then model.
func parseLaunchers(out string) []proto.Launcher {
	var list []proto.Launcher
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) == 0 || !shellcmd.NameRe.MatchString(f[0]) {
			continue
		}
		l := proto.Launcher{Name: f[0]}
		rest := ""
		if len(line) > 26 {
			rest = line[26:]
		}
		if len(rest) > 40 {
			l.BaseURL = strings.TrimSpace(rest[:40])
			l.Model = strings.TrimSpace(rest[40:])
		} else {
			l.BaseURL = strings.TrimSpace(rest)
		}
		if l.BaseURL == "-" {
			l.BaseURL = ""
		}
		// Older shell helpers used display text for an unspecified model; it is not a model ID.
		if l.Model == "默认" { // i18n-ignore: legacy wire value, never displayed
			l.Model = ""
		}
		list = append(list, l)
	}
	return list
}
