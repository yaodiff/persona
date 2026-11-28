package node

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

type codexCachedModel struct {
	Slug              string              `json:"slug"`
	Model             string              `json:"model"`
	ID                string              `json:"id"`
	DisplayName       string              `json:"display_name"`
	DisplayNameCamel  string              `json:"displayName"`
	Description       string              `json:"description"`
	Visibility        string              `json:"visibility"`
	Hidden            bool                `json:"hidden"`
	DefaultLevel      string              `json:"default_reasoning_level"`
	DefaultLevelCamel string              `json:"defaultReasoningEffort"`
	ServiceTiers      []proto.ServiceTier `json:"service_tiers"`
	ServiceTiersCamel []proto.ServiceTier `json:"serviceTiers"`
	Levels            []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
	LevelsCamel []struct {
		Effort string `json:"reasoningEffort"`
	} `json:"supportedReasoningEfforts"`
}

func (m codexCachedModel) modelID() string {
	return strings.TrimSpace(cmp.Or(m.Slug, m.Model, m.ID))
}

func (m codexCachedModel) visible() bool {
	if m.Hidden {
		return false
	}
	return m.Visibility == "" || m.Visibility == "list"
}

func (m codexCachedModel) efforts() (levels []string, def string, tiers []proto.ServiceTier) {
	levels = []string{}
	for _, x := range m.Levels {
		if e := strings.TrimSpace(x.Effort); e != "" && !slices.Contains(levels, e) {
			levels = append(levels, e)
		}
	}
	for _, x := range m.LevelsCamel {
		if e := strings.TrimSpace(x.Effort); e != "" && !slices.Contains(levels, e) {
			levels = append(levels, e)
		}
	}
	def = strings.TrimSpace(cmp.Or(m.DefaultLevel, m.DefaultLevelCamel))
	if len(levels) == 0 && def == "none" {
		def = ""
	}
	tiers = m.ServiceTiers
	if len(tiers) == 0 {
		tiers = m.ServiceTiersCamel
	}
	return levels, def, tiers
}

func readCodexCacheEntries(home string) ([]codexCachedModel, bool) {
	if home == "" {
		return nil, false
	}
	candidates := make([]string, 0, 2)
	if _, _, catalog := readCodexConfigTopLevel(home); catalog != "" {
		if !filepath.IsAbs(catalog) {
			catalog = filepath.Join(home, catalog)
		}
		candidates = append(candidates, catalog)
	}
	candidates = append(candidates, filepath.Join(home, "models_cache.json"))
	for _, path := range candidates {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var doc struct {
			Models []codexCachedModel `json:"models"`
		}
		if json.Unmarshal(b, &doc) != nil {
			continue
		}
		return doc.Models, true
	}
	return nil, false
}

// readCodexConfigDefault reads top-level `model` and `model_reasoning_effort` from <home>/config.toml without touching it.
func readCodexConfigDefault(home string) (model, effort string) {
	model, effort, _ = readCodexConfigTopLevel(home)
	return model, effort
}

// readCodexConfigTopLevel reads top-level `model`, `model_reasoning_effort`, and `model_catalog_json` from <home>/config.toml without touching it.
func readCodexConfigTopLevel(home string) (model, effort, catalog string) {
	if home == "" {
		return "", "", ""
	}
	f, err := os.Open(filepath.Join(home, "config.toml"))
	if err != nil {
		return "", "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			break // only top-level keys before the first table header
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if idx := strings.IndexByte(v, '#'); idx >= 0 {
			// Strip trailing comment outside quotes when simple.
			if strings.Count(v[:idx], `"`)%2 == 0 && strings.Count(v[:idx], `'`)%2 == 0 {
				v = v[:idx]
			}
		}
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		switch k {
		case "model":
			if proto.ValidModelID(v) {
				model = v
			}
		case "model_reasoning_effort":
			effort = v
		case "model_catalog_json":
			catalog = v
		}
	}
	return model, effort, catalog
}

// readCodexHomeModels reads visible models from <home>/models_cache.json and <home>/config.toml as a local fallback when
// `codex app-server` model/list is unavailable or empty. Full provider-prefixed slugs (e.g. acme/gpt-5-codex) are kept.
func readCodexHomeModels(home string) []proto.AgentModel {
	entries, hasCache := readCodexCacheEntries(home)
	cfgModel, cfgEffort := readCodexConfigDefault(home)
	if !hasCache && cfgModel == "" {
		return []proto.AgentModel{}
	}
	out := make([]proto.AgentModel, 0, len(entries))
	seen := map[string]bool{}
	hasDefault := false
	for _, m := range entries {
		id := m.modelID()
		if !m.visible() || id == "" || !proto.ValidModelID(id) || seen[id] {
			continue
		}
		seen[id] = true
		levels, def, tiers := m.efforts()
		isDef := cfgModel != "" && id == cfgModel
		if isDef {
			hasDefault = true
		}
		name := strings.TrimSpace(cmp.Or(m.DisplayName, m.DisplayNameCamel, id))
		out = append(out, proto.AgentModel{
			ID:            id,
			Name:          name,
			Description:   strings.TrimSpace(m.Description),
			Default:       isDef,
			Efforts:       levels,
			DefaultEffort: def,
			ServiceTiers:  tiers,
		})
	}
	if cfgModel != "" && !seen[cfgModel] {
		def := cfgEffort
		if def == "" {
			def = "medium"
		}
		out = slices.Insert(out, 0, proto.AgentModel{
			ID:            cfgModel,
			Name:          cfgModel,
			Default:       true,
			Efforts:       slices.Clone(codexEffortLevels),
			DefaultEffort: def,
		})
		hasDefault = true
	}
	if !hasDefault && len(out) > 0 {
		out[0].Default = true
	}
	return out
}

// readCodexHomeModelEfforts reads effort metadata (including hidden models) from <home>/models_cache.json.
func readCodexHomeModelEfforts(home string) (map[string]codexModelEfforts, bool) {
	entries, ok := readCodexCacheEntries(home)
	if !ok || len(entries) == 0 {
		return nil, false
	}
	models := make(map[string]codexModelEfforts, len(entries))
	for _, m := range entries {
		levels, def, tiers := m.efforts()
		for _, k := range []string{strings.TrimSpace(m.Slug), strings.TrimSpace(m.Model), strings.TrimSpace(m.ID)} {
			if k != "" {
				models[k] = codexModelEfforts{levels: levels, def: def, tiers: tiers}
			}
		}
	}
	return models, len(models) > 0
}

// reconcileCodexEffort picks a valid effort on meta when a model switch leaves the previous effort incompatible.
func reconcileCodexEffort(meta codexModelEfforts) string {
	if len(meta.levels) == 0 {
		return ""
	}
	if meta.def != "" && slices.Contains(meta.levels, meta.def) {
		return meta.def
	}
	if slices.Contains(meta.levels, "medium") {
		return "medium"
	}
	return meta.levels[0]
}

func (n *Node) codexAppLauncherModels(ctx context.Context, refresh bool) (proto.LauncherModels, error) {
	home := n.codexHome()
	key := n.home + "\x00" + codexAppLauncher + "\x00codex\x00" + home
	launcherModelsCache.Lock()
	at, ok := launcherModelsCache.at[key]
	cached := launcherModelsCache.got[key]
	launcherModelsCache.Unlock()
	ask := func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), launcherModelsTimeout)
		defer cancel()
		var models []proto.AgentModel
		if cli, err := codexAppCLI(); err == nil && cli != "" {
			models, _ = queryCodexCLIModels(ctx, cli, home)
		}
		if len(models) == 0 {
			models = readCodexHomeModels(home)
		}
		if models == nil {
			models = []proto.AgentModel{}
		}
		res := proto.LauncherModels{Agent: "codex", Models: models}
		launcherModelsCache.Lock()
		if len(launcherModelsCache.got) > 64 {
			launcherModelsCache.at, launcherModelsCache.got = map[string]time.Time{}, map[string]proto.LauncherModels{}
		}
		launcherModelsCache.at[key], launcherModelsCache.got[key] = time.Now(), res
		launcherModelsCache.Unlock()
		return res, nil
	}
	finish := func(res proto.LauncherModels) proto.LauncherModels {
		if !codexDesktopBusAvailable(home) {
			res.Locked = codexAppModelLocked
		}
		return res
	}
	if ok && !refresh {
		switch age := time.Since(at); {
		case age < launcherModelsTTL:
			return finish(cached), nil
		case age < launcherModelsStale:
			launcherModelsCache.flight.DoChan(key, ask)
			return finish(cached), nil
		}
	}
	v, err, _ := launcherModelsCache.flight.Do(key, ask)
	if err != nil {
		return proto.LauncherModels{}, err
	}
	return finish(v.(proto.LauncherModels)), nil
}

func queryCodexCLIModels(ctx context.Context, cli, home string) ([]proto.AgentModel, error) {
	cmd := exec.CommandContext(ctx, cli, "app-server")
	cmd.Dir = os.TempDir()
	if home != "" {
		cmd.Env = append(desktopSeedEnv(os.Environ()), "CODEX_HOME="+home)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = in.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	return codexModelList(in, out)
}
