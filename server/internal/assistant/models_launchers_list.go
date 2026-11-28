package assistant

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// launcherListing is a node's launchers.list: the launchers, and the shell and rc file they came from ("" from an
// older node).
type launcherListing struct {
	Source      string           `json:"source,omitempty"`
	Launchers   []proto.Launcher `json:"launchers"`
	HostRuntime bool             `json:"host_runtime,omitempty"`
	Shell       string           `json:"shell"`
	RCFile      string           `json:"rc_file"`
}

// listLaunchers asks one online machine for its list, giving it launcherSyncTimeout in total.
// Ordinary lists never load external sources; those use their own handshake-gated, source-scoped RPC.
func (s *Service) listLaunchers(ctx context.Context, d Device, refresh ...bool) (launcherListing, error) {
	return s.listLaunchersWith(ctx, d, launcherListReadOpts{refresh: len(refresh) > 0 && refresh[0]})
}

func (s *Service) listLaunchersWith(ctx context.Context, d Device, opts launcherListReadOpts) (launcherListing, error) {
	if !d.Online {
		return launcherListing{}, apierr.ErrOffline
	}
	cctx, cancel := context.WithTimeout(ctx, launcherSyncTimeout)
	defer cancel()
	var res launcherListing
	var err error
	if opts.refresh {
		// Retry starts a real scan. Reserve part of the same budget for the last node snapshot if the scan is slow
		// or fails. Execution recovery uses machineLaunchersOn and requires a successful scan.
		reserve := min(time.Second, launcherSyncTimeout/2)
		rctx, stop := context.WithTimeout(cctx, launcherSyncTimeout-reserve)
		err = s.cachedLauncherList(rctx, d.ID, launcherListReadOpts{refresh: true}, &res)
		stop()
		if err != nil && cctx.Err() == nil {
			var cached launcherListing
			cacheErr := s.call(cctx, d.ID, DeviceLaunchersList, map[string]any{"cache_only": true}, &cached)
			if cacheErr == nil && len(cached.Launchers) > 0 {
				return cached, nil
			}
		}
	} else {
		err = s.cachedLauncherList(cctx, d.ID, opts, &res)
	}
	if err != nil && cctx.Err() != nil && ctx.Err() == nil {
		err = context.DeadlineExceeded
	}
	return res, err
}

// launcherSyncState names why a machine's list did not come: offline, timeout, or error with the reason.
func launcherSyncState(ctx context.Context, err error) (state, reason string) {
	switch {
	case errors.Is(err, apierr.ErrOffline):
		return "offline", ""
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		return "timeout", ""
	}
	return "error", textfmt.Clip(strings.TrimSpace(err.Error()), 300)
}

// configuredLauncher is machine's hub.json launcher name (machines[].launchers, in the hello): a wrapper the node may
// not list, the agent the user said it runs. The node runs a launcher it does not list only when it is vouched for
// like this (configured, node runnableLauncher).
func (s *Service) configuredLauncher(ctx context.Context, machine, name string) (ConfiguredLauncher, bool) {
	if proto.IsExternalLauncher(name) {
		return ConfiguredLauncher{}, false
	}
	for _, l := range s.hello(ctx).Models.Launchers[machine] {
		if l.Name == name {
			return l, true
		}
	}
	return ConfiguredLauncher{}, false
}

// launcherParams are a node launcher method's params for name on machine: configured (and its agent_type) when
// hub.json names it there.
func (s *Service) launcherParams(ctx context.Context, machine, name string, params map[string]any) map[string]any {
	params["name"] = name
	if l, ok := s.configuredLauncher(ctx, machine, name); ok {
		params["configured"] = true
		if l.AgentType != "" {
			params["agent_type"] = l.AgentType
		}
	}
	return params
}

// machineSource: a launcher a machine defines itself, a ~/.zshrc function or alias ("zshrc") or a wrapper script on
// its PATH ("path", node/pathlaunchers.go); its page shows its text and what a probe found.
func machineSource(source string) bool { return source == "zshrc" || source == "path" }

// listedRCLauncher: Settings › Model providers lists l from a computer's list. A runtime's launcher without model
// configuration (DSH, node/runtimes.go) is not.
func listedRCLauncher(l proto.Launcher) bool {
	return !proto.IsExternalLauncher(l.Name) && l.Source != proto.ExternalLauncherSource &&
		(l.Source == "" || machineSource(l.Source) || l.Agent == "agy" || l.Agent == "omp")
}

// launcherViews is the whole list: managed first (newest last), then ~/.zshrc ones by name; hidden ones flagged.
func (s *Service) launcherViews(ctx context.Context) []LauncherView {
	return s.launcherViewsOf(ctx, s.zshrcLaunchers(ctx, false))
}

func (s *Service) launcherViewsOf(ctx context.Context, zshrc []zshrcLaunchers) []LauncherView {
	return s.launcherViewsFrom(ctx, zshrc, s.storedLaunchers(ctx))
}

func (s *Service) launcherViewsFrom(ctx context.Context, zshrc []zshrcLaunchers, stored []storedLauncher) []LauncherView {
	var out []LauncherView
	managed := map[string]bool{}
	for _, l := range stored {
		managed[l.Name] = true
		out = append(out, s.launcherView(l))
	}
	hidden := s.hiddenLaunchers(ctx)
	byName := map[string]*LauncherView{}
	unrestrictedModes := map[string]bool{}
	var names []string
	for _, m := range zshrc {
		for _, l := range m.Launchers {
			if managed[l.Name] || l.Name == "claude" || !listedRCLauncher(l) {
				continue
			}
			v := byName[l.Name]
			if v == nil {
				src := "zshrc"
				if l.Source != "" {
					src = l.Source
				}
				v = &LauncherView{Name: l.Name, DisplayName: l.DisplayName, Source: src, Agent: l.Agent, BaseURL: l.BaseURL, Model: l.Model, Hidden: slices.Contains(hidden, l.Name), PermissionModes: l.PermissionModes, builtinCompanion: l.BuiltinCompanion, modelRunMachines: []string{}}
				byName[l.Name] = v
				names = append(names, l.Name)
				if l.PermissionModes == 0 {
					unrestrictedModes[l.Name] = true
				}
			} else {
				if v.DisplayName == "" && l.DisplayName != "" {
					v.DisplayName = l.DisplayName
				}
				if l.PermissionModes == 0 {
					unrestrictedModes[l.Name] = true
					v.PermissionModes = 0
				} else if !unrestrictedModes[l.Name] {
					v.PermissionModes |= l.PermissionModes
				}
			}
			v.Machines = append(v.Machines, m.MachineID)
			if l.BuiltinCompanion {
				v.builtinCompanion = true
			}
			if modelRunSourceSupported(l.Source, viewAgent(LauncherView{Name: l.Name, Agent: l.Agent}), l.BuiltinCompanion) && !slices.Contains(v.modelRunMachines, m.MachineID) {
				v.modelRunMachines = append(v.modelRunMachines, m.MachineID)
			}
			if rc := cmp.Or(m.Sync.RCFile, defaultRCFile); v.Source == "zshrc" && !slices.Contains(v.RCFiles, rc) {
				v.RCFiles = append(v.RCFiles, rc)
			}
			if m.Sync.State != "" && m.Sync.State != "ok" {
				v.Stale = append(v.Stale, m.MachineID) // the machine's last list: it is offline or did not answer now
			}
		}
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, *byName[n])
	}
	return out
}

// builtinClaude is the plain `claude` of every machine that has Claude Code (the default launcher, “Claude Code
// default” in Model Configuration; assistant.launcher "claude"): one view, its machines and their versions. ok false
// when no machine is known to have it, or a provider added in Persona took the name.
func (s *Service) builtinClaude(ctx context.Context, zshrc []zshrcLaunchers) (LauncherView, bool) {
	if slices.ContainsFunc(s.storedLaunchers(ctx), func(l storedLauncher) bool { return l.Name == "claude" }) {
		return LauncherView{}, false
	}
	// hidden like an rc-file launcher (#4384): out of the pickers, shown under Hidden to restore
	v := LauncherView{Name: "claude", Source: "builtin", Versions: map[string]string{}, Hidden: slices.Contains(s.hiddenLaunchers(ctx), "claude")}
	for _, m := range zshrc {
		listed := slices.ContainsFunc(m.Launchers, func(l proto.Launcher) bool { return l.Name == "claude" })
		if m.ClaudeVersion == "" && !listed {
			continue
		}
		v.Machines = append(v.Machines, m.MachineID)
		if m.ClaudeVersion != "" {
			v.Versions[m.MachineID] = m.ClaudeVersion
		}
		if m.Sync.State != "" && m.Sync.State != "ok" {
			v.Stale = append(v.Stale, m.MachineID)
		}
	}
	return v, len(v.Machines) > 0
}

// launcherSyncList is every machine's sync state, in the machines' order (the ones asked).
func launcherSyncList(zshrc []zshrcLaunchers) []LauncherSync {
	out := []LauncherSync{}
	for _, m := range zshrc {
		if m.Sync.State != "" {
			out = append(out, m.Sync)
		}
	}
	return out
}

// machineLaunchers is what a machine can start (the pickers: new chat, the assistant's model): its own launchers
// (the node's list) but the hidden ones, its reported built-in claude, then the managed ones.
func (s *Service) machineLaunchers(ctx context.Context, machine string) ([]proto.Launcher, error) {
	list, _, _, err := s.machineLaunchersOn(ctx, machine, "")
	return list, err
}

// machineLaunchersOn is machineLaunchers for a new Agent's start in host (its engine host; "": the machine's own list,
// which Settings and the main chat read, the assistant running on the command line). hostRuntime: the node answered
// that every start there goes to an engine runtime that runs only its own launchers (#8140, Paseo): the list is its
// providers and what starts elsewhere whatever the host. A provider kept in Persona and plain claude are not added
// then (the engine would run one of its own in their place), and one named like an engine provider is that provider;
// only a managed Antigravity launcher, which its own runtime runs on any host, still is.
type machineLauncherListing struct {
	Launchers   []proto.LauncherEnvelope `json:"launchers"`
	HostRuntime bool                     `json:"host_runtime"`
}

type launcherRefreshKey struct{}

// withLauncherRefresh asks the node to replace its ordinary one-minute shell-launcher snapshot. It is used only for
// bounded recovery of a specific Assistant launcher, not ordinary picker loads.
func withLauncherRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, launcherRefreshKey{}, true)
}

// known distinguishes an explicit node list (including []) from a missing/null catalog on an older node.
func (s *Service) machineLaunchersOn(ctx context.Context, machine, host string) (list []proto.Launcher, hostRuntime, known bool, err error) {
	var res machineLauncherListing
	refresh, _ := ctx.Value(launcherRefreshKey{}).(bool)
	if err := s.cachedLauncherList(ctx, machine, launcherListReadOpts{host: host, refresh: refresh}, &res); err != nil {
		return nil, false, false, err
	}
	list, hostRuntime = s.machineLaunchersFrom(ctx, machine, res, true)
	return list, hostRuntime, res.Launchers != nil, nil
}

// machineLaunchersFrom is machine's list from res, its node's answer. ask: a provider's own-service rule may ask the
// node as the pickers do (usableOn); false goes by what it said before only (usableOnAsking), for a status that asks no
// node.
func (s *Service) machineLaunchersFrom(ctx context.Context, machine string, res machineLauncherListing, ask bool) ([]proto.Launcher, bool) {
	d, _ := s.device(ctx, machine)
	hidden := s.hiddenLaunchers(ctx)
	// Only the providers in this computer's scope: one out of it is not named to the node, whose own launcher by
	// that name (if any) is what runs there.
	managed := slices.DeleteFunc(slices.Clone(s.storedLaunchers(ctx)), func(m storedLauncher) bool {
		return proto.IsExternalLauncher(m.Name) || !s.usableOnAsking(ctx, m, machine, ask)
	})
	if res.HostRuntime {
		managed = slices.DeleteFunc(managed, func(m storedLauncher) bool { return m.Agent != "agy" })
	}
	out := make([]proto.Launcher, 0, len(res.Launchers)+len(managed))
	for _, envelope := range res.Launchers {
		l := proto.Launcher(envelope)
		if (proto.IsExternalLauncher(l.Name) || l.Source == proto.ExternalLauncherSource) && !d.ExternalInventory {
			continue // a historical snapshot never replaces the current node's enabled-source handshake
		}
		if slices.Contains(hidden, l.Name) || slices.ContainsFunc(managed, func(m storedLauncher) bool { return m.Name == l.Name }) {
			continue
		}
		l.Source = compat.Source(l.Source)
		if l.Source == "" { // a runtime's launcher (node/runtimes.go) keeps its own source, e.g. "dsh"
			l.Source = "zshrc"
		}
		out = append(out, l)
	}
	// A node that starts its own Codex launchers with no first message starts a managed one so too (they share
	// codex-start, node/codex_waiting.go); an older node's Codex starts all need it.
	codexOptional := slices.ContainsFunc(res.Launchers, func(l proto.LauncherEnvelope) bool { return l.Agent == "codex" && l.PromptOptional })
	// Likewise in a pane the user picked (Run Agent Here, #5504): the same start, which keeps that pane when it fails.
	codexHere := slices.ContainsFunc(res.Launchers, func(l proto.LauncherEnvelope) bool { return l.Agent == "codex" && l.RunHere })
	// The node leaves plain claude out when its shell has another Claude launcher. devices.list's version is the
	// same installation signal used by the built-in provider and Agent-start fallback, and works with older nodes.
	if !res.HostRuntime && d.ClaudeVersion != "" && !slices.Contains(hidden, "claude") &&
		!slices.ContainsFunc(out, func(l proto.Launcher) bool { return l.Name == "claude" }) {
		out = append(out, proto.Launcher{Name: "claude", Source: "builtin", DisplayName: "Claude Code"})
	}
	agyAssistant := slices.ContainsFunc(res.Launchers, func(l proto.LauncherEnvelope) bool { return l.Agent == "agy" && l.Assistant })
	agyPolicy := slices.ContainsFunc(res.Launchers, func(l proto.LauncherEnvelope) bool { return l.Agent == "agy" && l.Assistant && l.SupervisionPolicy })
	for _, m := range managed {
		if m.Kind == "api" {
			continue
		}
		out = append(out, proto.Launcher{Name: m.Name, BaseURL: m.BaseURL, Model: m.Model, Source: "persona", Agent: m.Agent, Assistant: m.Agent == "agy" && agyAssistant, SupervisionPolicy: m.Agent == "agy" && agyPolicy,
			PromptOptional: m.Agent == "codex" && codexOptional, RunHere: m.Agent == "codex" && codexHere, Pi: m.PiAPI() != ""})
	}
	return out, res.HostRuntime
}

// pickerLaunchers is what machine can start, as New Agent's picker lists it: machineLaunchers with hub.json's
// launchers for it applied (sessionLaunchers).
func (s *Service) pickerLaunchers(ctx context.Context, machine string) ([]ConfiguredLauncher, error) {
	return s.pickerLaunchersOn(ctx, machine, "")
}

// pickerLaunchersOn is pickerLaunchers for a new Agent's start in host (machineLaunchersOn).
func (s *Service) pickerLaunchersOn(ctx context.Context, machine, host string) ([]ConfiguredLauncher, error) {
	ls, _, err := s.pickerLaunchersKnown(ctx, machine, host)
	return ls, err
}

func (s *Service) pickerLaunchersKnown(ctx context.Context, machine, host string) ([]ConfiguredLauncher, bool, error) {
	ls, _, known, err := s.machineLaunchersOn(ctx, machine, host)
	if err != nil {
		return nil, false, err
	}
	return s.pickerLaunchersFrom(ctx, machine, ls), known, nil
}

func (s *Service) pickerLaunchersFrom(ctx context.Context, machine string, ls []proto.Launcher) []ConfiguredLauncher {
	discovered := make([]ConfiguredLauncher, 0, len(ls))
	for _, l := range ls {
		discovered = append(discovered, ConfiguredLauncher{Launcher: l})
	}
	return sessionLaunchers(discovered, s.hello(ctx).Models.Launchers[machine])
}

// assistantPickerLaunchers bounds live shell discovery to the launcher sync deadline. If the shell is slow to start,
// the machine's last known shell launchers and scoped Persona providers can still populate the main chat's menu,
// including providers that cannot run here and why.
func (s *Service) assistantPickerLaunchers(ctx context.Context, d Device, refresh bool, lang i18n.Lang) (picker, fits []ConfiguredLauncher, unavailable []unavailableLauncher, err error) {
	var syncErr error
	if d.ExternalInventory {
		picker, err = s.externalPickerLaunchers(ctx, d, refresh)
		if err != nil {
			return nil, nil, nil, err
		}
	} else {
		var listing launcherListing
		listing, syncErr = s.listLaunchers(ctx, d, refresh)
		if syncErr != nil {
			if ctx.Err() != nil || apierr.NodeCode(syncErr) != "timeout" {
				return nil, nil, nil, syncErr
			}
			listing = launcherListing{} // the saved snapshot was requested without an engine host
			lastSeen, _ := s.zshrcLaunchersLastSeen(ctx)
			if i := slices.IndexFunc(lastSeen, func(m zshrcLaunchers) bool { return m.MachineID == d.ID }); i >= 0 {
				// A saved runtime advertisement does not establish that the runtime is still connected.
				for _, l := range lastSeen[i].Launchers {
					if l.Source == "" || machineSource(l.Source) {
						listing.Launchers = append(listing.Launchers, l)
					}
				}
			}
		}
		res := machineLauncherListing{
			Launchers:   make([]proto.LauncherEnvelope, 0, len(listing.Launchers)),
			HostRuntime: listing.HostRuntime,
		}
		for _, l := range listing.Launchers {
			res.Launchers = append(res.Launchers, proto.LauncherEnvelope(l))
		}
		launchers, _ := s.machineLaunchersFrom(ctx, d.ID, res, true)
		picker = s.pickerLaunchersFrom(ctx, d.ID, launchers)
	}
	fits, err = s.assistantLaunchersOf(ctx, d.ID, picker)
	if err != nil {
		return nil, nil, nil, err
	}
	unavailable = s.mainChatUnavailable(ctx, d.ID, picker, fits, lang)
	if len(fits) == 0 && len(unavailable) == 0 {
		err = syncErr
	}
	return picker, fits, unavailable, err
}

// pickerLaunchersSeen is pickerLaunchers from seen, machine's own launchers as last seen, without asking its node for
// them: the same rule (machineLaunchersFrom: a Persona-managed provider in the computer's scope takes the place of its
// own launcher by that name, a hidden one is left out), then hub.json's launchers for it (sessionLaunchers). ask is
// machineLaunchersFrom's: false asks the node nothing at all.
func (s *Service) pickerLaunchersSeen(ctx context.Context, machine string, seen []proto.Launcher, ask bool) []ConfiguredLauncher {
	res := machineLauncherListing{}
	for _, l := range seen {
		res.Launchers = append(res.Launchers, proto.LauncherEnvelope(l))
	}
	list, _ := s.machineLaunchersFrom(ctx, machine, res, ask)
	return s.pickerLaunchersFrom(ctx, machine, list)
}

// assistantLaunchers are the machine's launchers backed by a provider the main conversation supports.
func (s *Service) assistantLaunchers(ctx context.Context, machine string) ([]ConfiguredLauncher, error) {
	picker, err := s.pickerLaunchers(ctx, machine)
	if err != nil {
		return nil, err
	}
	return s.assistantLaunchersOf(ctx, machine, picker)
}

// assistantProviders are the launcher names whose current, merged configuration supports the main conversation on
// machine. picker is the node's current list; managed and direct Messages providers are added from their stored records.
func (s *Service) assistantProviders(ctx context.Context, machine string, picker []ConfiguredLauncher) (map[string]bool, error) {
	p, err := s.modelPurposeDef(ctx, "assistant")
	if err != nil {
		return nil, err
	}
	p.MachineID = machine
	stored := s.storedLaunchers(ctx)
	current := slices.Clone(picker)
	agyAssistant := picker == nil || slices.ContainsFunc(picker, func(l ConfiguredLauncher) bool { return cmp.Or(l.AgentType, l.Agent) == "agy" && l.Assistant })
	for _, managed := range stored {
		if !slices.ContainsFunc(current, func(l ConfiguredLauncher) bool { return l.Name == managed.Name }) {
			current = append(current, ConfiguredLauncher{Launcher: proto.Launcher{
				Name: managed.Name, BaseURL: managed.BaseURL, Model: managed.Model, Source: "persona", Agent: managed.Agent,
				Assistant: managed.Agent == "agy" && agyAssistant,
			}})
		}
	}
	current = sessionLaunchers(current, s.hello(ctx).Models.Launchers[machine])
	hidden := s.hiddenLaunchers(ctx)
	allowed := map[string]bool{}
	for _, l := range current {
		if l.Name == "" || slices.Contains(hidden, l.Name) && compat.Source(l.Source) != "persona" {
			continue
		}
		if s.modelLauncherCompatible(p, l, stored) {
			allowed[l.Name] = true
		}
	}
	return allowed, nil
}

// assistantLaunchersOf is assistantLaunchers from the machine's picker list (pickerLaunchers).
func (s *Service) assistantLaunchersOf(ctx context.Context, machine string, picker []ConfiguredLauncher) ([]ConfiguredLauncher, error) {
	ctx = withSettingsMemo(ctx)
	allowed, err := s.assistantProviders(ctx, machine, picker)
	if err != nil {
		return nil, err
	}
	stored := s.storedLaunchers(ctx)
	out := []ConfiguredLauncher{}
	seen := map[string]bool{}
	for _, l := range picker {
		if allowed[l.Name] {
			out = append(out, l)
			seen[l.Name] = true
		}
	}
	// Ordinary Agent lists omit direct API providers; Messages providers are already sent to nodes as launchers.
	for _, l := range stored {
		if l.Kind == "api" && allowed[l.Name] && !seen[l.Name] && s.usableOn(ctx, l, machine) {
			out = append(out, ConfiguredLauncher{Launcher: proto.Launcher{Name: l.Name, BaseURL: l.BaseURL, Model: l.Model, Source: "persona"}, AgentType: "claude"})
		}
	}
	return out, nil
}

// launcherHasModel: whether the launcher offers model (its id or what an alias resolves to), from its list as it was
// a few minutes ago at most, or the user typed it for this launcher and machine before (models_custom.go). Unknown (an
// agent with no list, a failed call) counts as yes.
func (s *Service) launcherHasModel(ctx context.Context, machine, launcher, model string) bool {
	_, ok := s.resolveLauncherModel(ctx, machine, launcher, model)
	return ok
}

// resolveLauncherModel is model as the launcher's own spelling, and whether the launcher offers it at all. An unknown
// list (an agent with no list, a failed call) counts as yes and keeps model. An exact id passes as it is; a transcript
// echo the gateway rewrote — routed under a prefix, or with/without the 1M marker — folds to the declared id (or stays,
// as a remembered typed id). A model in no list and no remembered id is not offered: ok false.
func (s *Service) resolveLauncherModel(ctx context.Context, machine, launcher, model string) (string, bool) {
	set := s.launcherModelSet(ctx, machine, launcher)
	if set == nil {
		return model, true
	}
	if set[model] {
		return model, true
	}
	declared := s.declaredLauncherModels(ctx, machine, launcher)
	base := proto.TrimModelWindow(model)
	for _, d := range declared {
		if strings.EqualFold(base, proto.TrimModelWindow(d)) || proto.SameLauncherModel(model, d) {
			if s.isCustomModel(ctx, machine, launcher, model) {
				return model, true
			}
			return d, true
		}
	}
	if norm := proto.NormalizeSessionModel(model, declared); norm != model && set[norm] {
		return norm, true
	}
	if s.isCustomModel(ctx, machine, launcher, model) {
		return model, true
	}
	return "", false
}

// declaredLauncherModels is every id the launcher declared in its cached list (ids, resolved ids, folded aliases). The
// list is read through launcherModelSet first, so it is there; unknown is nil.
func (s *Service) declaredLauncherModels(ctx context.Context, machine, launcher string) []string {
	key := launcherModelCacheKey(ctx, machine, launcher)
	c := &s.models
	c.cacheMu.Lock()
	if c.cacheAt != nil && time.Since(c.cacheAt[key]) < 5*time.Minute {
		out := c.declared[key]
		c.cacheMu.Unlock()
		return out
	}
	c.cacheMu.Unlock()
	s.launcherModelSet(ctx, machine, launcher)
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	return c.declared[key]
}

// launcherModelSet is the launcher's own /model list on machine (ids and what aliases resolve to), as it was a few
// minutes ago at most; nil when it is not known (an agent with no list, a failed call).
func (s *Service) launcherModelSet(ctx context.Context, machine, launcher string) map[string]bool {
	key := launcherModelCacheKey(ctx, machine, launcher)
	c := &s.models
	c.cacheMu.Lock()
	if c.cacheAt != nil && time.Since(c.cacheAt[key]) < 5*time.Minute {
		set := c.have[key]
		c.cacheMu.Unlock()
		return set
	}
	c.cacheMu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := s.askLauncherModels(cctx, machine, launcher, false)
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	declared := make([]string, 0, len(res.Models)*2)
	aliases := map[string]string{}
	defaultModel := ""
	for _, x := range res.Models {
		set[x.ID] = true
		declared = append(declared, x.ID)
		if x.Resolved != "" {
			set[x.Resolved] = true
			declared = append(declared, x.Resolved)
			aliases[x.ID] = x.Resolved
		}
		// a folded row's other spellings (oneRowPerModel) stand for the model it runs
		for _, a := range x.Aliases {
			set[a] = true
			declared = append(declared, a)
			if a != cmp.Or(x.Resolved, x.ID) {
				aliases[a] = cmp.Or(x.Resolved, x.ID)
			}
		}
		if x.Default && defaultModel == "" {
			defaultModel = x.ID
			if x.Resolved != "" {
				defaultModel = x.Resolved
			}
		}
	}
	if len(set) == 0 {
		return nil
	}
	c.cacheMu.Lock()
	if c.cacheAt == nil {
		c.cacheAt, c.have = map[string]time.Time{}, map[string]map[string]bool{}
	}
	if c.declared == nil {
		c.declared = map[string][]string{}
	}
	if c.aliases == nil {
		c.aliases, c.defaults = map[string]map[string]string{}, map[string]string{}
	}
	c.cacheAt[key], c.have[key], c.declared[key] = time.Now(), set, declared
	c.aliases[key], c.defaults[key] = aliases, defaultModel
	c.cacheMu.Unlock()
	return set
}

// launcherModelResolution reads the alias and default relationships alongside launcherModelSet's cached ids. A failed
// or empty model read is unknown, not proof that a reply from another model belongs to this binding.
func (s *Service) launcherModelResolution(ctx context.Context, machine, launcher string) (map[string]string, string) {
	key := launcherModelCacheKey(ctx, machine, launcher)
	c := &s.models
	c.cacheMu.Lock()
	if c.cacheAt != nil && time.Since(c.cacheAt[key]) < 5*time.Minute {
		aliases, defaultModel := c.aliases[key], c.defaults[key]
		c.cacheMu.Unlock()
		return aliases, defaultModel
	}
	c.cacheMu.Unlock()
	if set := s.launcherModelSet(ctx, machine, launcher); set == nil {
		return nil, ""
	}
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	return c.aliases[key], c.defaults[key]
}

// askLauncherModels is machine's node's /model list of launcher name (node/launcher_models.go), with the models of
// the plugin gateway behind it, one row per model (oneRowPerModel).
func (s *Service) askLauncherModels(ctx context.Context, machine, name string, refresh bool) (proto.LauncherModels, error) {
	res, err := s.launcherModelRows(ctx, machine, name, refresh)
	if err != nil {
		return res, err
	}
	res = s.oneRowPerModel(ctx, name, s.presetModels(ctx, machine, name, res))
	for i, m := range res.Models {
		res.Models[i].ResolvedOffered = false // only used with the node's original rows for legacy sessions
		id := m.ID
		if m.Resolved != "" {
			id = m.Resolved
		}
		res.Models[i].Price = nil
		if c, ok := catalogModel(id); ok && c.InheritedFrom == "" {
			res.Models[i].Price = c.Price
		}
	}
	return res, nil
}

// launcherModelRows reads the target node's rows before the picker merges gateway models, routes and aliases.
func (s *Service) launcherModelRows(ctx context.Context, machine, name string, refresh bool) (proto.LauncherModels, error) {
	var res proto.LauncherModels
	params := map[string]any{"refresh": refresh}
	if purpose := launcherPurpose(ctx); purpose != "" {
		params["purpose"] = purpose
	}
	if host, _ := ctx.Value(launcherModelHostKey{}).(string); host != "" {
		params["host"] = host
	}
	if err := s.call(ctx, machine, assistantwire.DeviceLaunchersModels, s.launcherParams(ctx, machine, name, params), &res); err != nil {
		return res, err
	}
	if res.Models == nil {
		res.Models = []proto.AgentModel{}
	}
	return res, nil
}
