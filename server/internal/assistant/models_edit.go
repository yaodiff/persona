package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

type modelPatch struct {
	Revision       *int64          `json:"revision"`
	Provider       json.RawMessage `json:"provider"`
	Model          json.RawMessage `json:"model"`
	Effort         json.RawMessage `json:"effort"`
	PermissionMode json.RawMessage `json:"permission_mode"`
	// Runtime is accepted and ignored, whatever its value: an older app still sends the main chat's "cli" or
	// "agentsdk", and every assistant conversation runs on Claude Code's CLI.
	Runtime json.RawMessage `json:"runtime"`
	// ConfigDir: the main chat's profile for new conversations (#4633), private (stored as none) or legacy.
	ConfigDir json.RawMessage `json:"config_dir"`
	// Candidates: an extension's purpose only (#10398): {mode, custom}, or null for the extension's own lists.
	Candidates json.RawMessage `json:"candidates"`
}

func patchModel(b ModelBinding, in modelPatch) (ModelBinding, error) {
	if in.Provider != nil {
		var provider string
		if string(in.Provider) != "null" {
			if err := json.Unmarshal(in.Provider, &provider); err != nil {
				return b, i18n.Err("assistant-models.err_provider_type")
			}
		}
		if provider != b.Provider {
			b.Model, b.Effort, b.PermissionMode = "", "", ""
		}
	}
	for _, f := range []struct {
		raw json.RawMessage
		dst *string
	}{{in.Provider, &b.Provider}, {in.Model, &b.Model}, {in.Effort, &b.Effort}, {in.PermissionMode, &b.PermissionMode}, {in.ConfigDir, &b.ConfigDir}} {
		if f.raw == nil {
			continue
		}
		if string(f.raw) == "null" {
			*f.dst = ""
			continue
		}
		var v string
		if err := json.Unmarshal(f.raw, &v); err != nil {
			return b, i18n.Err("assistant-models.err_field_type")
		}
		if v == "" || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\r\n\x00") || len(v) > 512 {
			return b, i18n.Err("assistant-models.err_field_invalid")
		}
		*f.dst = v
	}
	if err := checkExternalPurpose(b); err != nil {
		return b, err
	}
	if b.ConfigDir != "" && !slices.Contains(assistantConfigDirs, b.ConfigDir) {
		return b, i18n.Err("assistant-models.err_config_dir_invalid")
	}
	if b.ConfigDir == assistantConfigDirPrivate {
		b.ConfigDir = "" // the default, stored as none
	}
	return b, nil
}

// modelProviderCompatible is the shared purpose and machine scope used by model settings and launcher pickers.
func (s *Service) modelProviderCompatible(p ModelPurpose, v ModelProvider, stored []storedLauncher) bool {
	if externalProvider(v.ID) {
		return false // a persisted main-chat pick is not a purpose configuration
	}
	if compat.Source(v.Source) == proto.SourceClaudeDesktop {
		return p.ID == "agent.new" // a new session in Claude's desktop app: Agents' only (models_desktop.go)
	}
	compatible := v.Kind == p.Kind
	if p.Kind == "launcher" && v.Kind == "api" { // a direct Messages API provider starts Claude on its gateway (#2455)
		compatible = messagesAPI(v.API)
	}
	if p.Kind == "api" && v.Kind == "launcher" {
		// Claude Code, Codex, and Antigravity launchers can answer on the main chat's computer via model.run with
		// zero-tool isolation (#5823, #12027); a managed launcher with a Messages gateway is called over HTTP, as
		// before. Among runtime-backed launchers, only the verified built-in Antigravity companion is admitted. With
		// no main chat computer set it stays offered when at least one computer supports model.run, and its status
		// says to choose one.
		managed := false
		for _, l := range stored {
			if v.ID == "launcher."+l.Name {
				managed = true
				compatible = messagesAPI(l.API) || modelRunAgentSupported(l.AgentType()) && (p.MachineID == "" || s.scopeReason(l, p.MachineID) == "")
				break
			}
		}
		if !managed {
			if v.modelRunMachines != nil {
				if p.MachineID == "" {
					compatible = len(v.modelRunMachines) > 0
				} else {
					compatible = slices.Contains(v.modelRunMachines, p.MachineID)
				}
			} else {
				compatible = modelRunSourceSupported(v.Source, v.Agent, v.builtinCompanion) &&
					(p.MachineID == "" || len(v.Machines) == 0 || slices.Contains(v.Machines, p.MachineID))
			}
		}
	}
	if p.Kind == "plugin" {
		compatible = v.ID == p.ID
	}
	if compatible && p.CategoryID == "assistant" && v.Kind == "launcher" {
		compatible = slices.Contains([]string{"", "claude", "codex", "agy", "omp"}, v.Agent)

	}
	if compatible && p.ID == purposeMeetingRoom { // Claude Code only (models_room.go)
		compatible = claudeCodeProvider(v, stored)
	}
	if compatible && p.Kind == "launcher" && len(v.Machines) > 0 && p.MachineID != "" {
		compatible = slices.Contains(v.Machines, p.MachineID)
	}
	if compatible && p.Kind == "launcher" && p.MachineID != "" {
		for _, l := range stored {
			if v.ID == "launcher."+l.Name {
				compatible = s.scopeReason(l, p.MachineID) == ""
				break
			}
		}
	}
	return compatible
}

// modelLauncherCompatible applies the shared provider rule to the current, merged launcher on one machine. Unlike
// the global discovery table, this includes new shell launchers, hub.json wrappers and a runtime's current caps.
func (s *Service) modelLauncherCompatible(p ModelPurpose, l ConfiguredLauncher, stored []storedLauncher) bool {
	if proto.IsExternalLauncher(l.Name) || l.Source == proto.ExternalLauncherSource {
		return p.ID == "assistant" && p.MachineID != "" && externalLauncherError(l.Launcher) == nil
	}
	v := ModelProvider{ID: "launcher." + l.Name, Kind: "launcher", Source: l.Source, Agent: cmp.Or(l.AgentType, l.Agent), Model: l.Model, builtinCompanion: l.BuiltinCompanion}
	for _, managed := range stored {
		if managed.Name == l.Name {
			v.Kind, v.API = managed.Kind, managed.API
			if v.Kind == "" {
				v.Kind = "launcher"
			}
			break
		}
	}
	runtime := runtimeSource(l.Source)
	if p.ID == purposeMeetingRoom && runtime {
		return false // a room runs on Claude Code's CLI only
	}
	if p.CategoryID == "assistant" && (runtime || v.Kind == "launcher" && (v.Agent == "agy" || v.Agent == "omp")) {
		if !l.Assistant {
			return false
		}
		// A runtime's assistant capability is explicit and does not depend on which agent it embeds.
		v.Agent = ""
	}
	return s.modelProviderCompatible(p, v, stored)
}

func effortLevelsForKind(kind string) []string {
	if kind == "api" {
		return append(slices.Clone(proto.EffortLevels), "off", "none")
	}
	return proto.EffortLevels
}

// effortLevelsForPurpose returns the effort levels valid for a purpose of kind running on provider with resolved agent.
// An API-kind purpose backed by a Codex launcher running via model.run (a shell/PATH launcher or a non-Messages
// managed launcher) accepts Codex's reasoning effort levels (including minimal and ultra) alongside the API off/none
// sentinel, without enabling minimal or ultra for Messages-backed HTTP launchers, Claude, Antigravity, or direct HTTP
// API providers.
func effortLevelsForPurpose(kind string, provider *ModelProvider, agent string) []string {
	if kind == "launcher" && provider != nil && provider.Agent == "traex" {
		return proto.CodexEffortLevels
	}
	if kind == "api" && provider != nil && provider.Kind == "launcher" && cmp.Or(agent, provider.Agent) == "codex" && (provider.Source != "persona" || !messagesAPI(provider.API)) {
		return append(slices.Clone(proto.CodexEffortLevels), "off", "none")
	}
	return effortLevelsForKind(kind)
}

// permissionModesForAgent returns the permission modes supported by an agent launcher.
// Claude Code always has Persona's five modes; proto.Launcher.PermissionModes describes other agents.
// For them an explicitly declared list governs. Otherwise Codex and Antigravity (agy) support all standard
// modes, while no-mode/legacy work agents (omp, traex, pi, cursor) take no permission mode.
func permissionModesForAgent(agent string, listed proto.PermissionModeSet) []string {
	if agent == "" || agent == "claude" {
		return proto.PermissionModes
	}
	if listed != 0 {
		return listed.List()
	}
	switch agent {
	case "codex", "agy":
		return proto.PermissionModes
	case "omp", "traex", "pi", "cursor":
		return nil
	default:
		return proto.PermissionModes
	}
}

func permissionModeSupported(agent string, listed proto.PermissionModeSet, mode string) bool {
	if mode == "" {
		return true
	}
	return slices.Contains(permissionModesForAgent(agent, listed), mode)
}

// Inbox TRAE uses the app-server's permission modes. Its terminal launcher has no mode setting.
func permissionModeSupportedForPurpose(purpose, agent string, listed proto.PermissionModeSet, mode string) bool {
	if purpose == purposeAgent && mode != "" {
		if mode == proto.ModePlan {
			return false // Plan rejects the Persona MCP calls needed for Inbox investigation and suggestions.
		}
		if agent == "traex" {
			return slices.Contains(proto.PermissionModes, mode)
		}
	}
	return permissionModeSupported(agent, listed, mode)
}

func (s *Service) targetMachineLauncher(ctx context.Context, machine, name string) (ConfiguredLauncher, bool) {
	if name == "" || machine == "" {
		return ConfiguredLauncher{}, false
	}
	ctx = withUnhidden(ctx, name)
	find := func(merged []ConfiguredLauncher) (ConfiguredLauncher, bool) {
		for _, l := range merged {
			if l.Name != name {
				continue
			}
			if c, ok := s.configuredLauncher(ctx, machine, name); ok {
				if c.PermissionModes != 0 {
					l.PermissionModes = c.PermissionModes
				} else if c.AgentType != "" && l.Agent != "" && c.AgentType != l.Agent {
					l.PermissionModes = 0
				}
			}
			return l, true
		}
		return ConfiguredLauncher{}, false
	}
	var seen []proto.Launcher
	foundMachine := false
	for _, m := range s.zshrcLaunchers(ctx, false) {
		if m.MachineID == machine {
			seen = m.Launchers
			foundMachine = true
			break
		}
	}
	// In-scope managed API providers (e.g. Messages API backing Claude Code) shadow same-named zshrc launchers in
	// machineLaunchersFrom without being emitted as non-API launchers; resolve a matching one with hub.json
	// overrides before falling back to live discovery.
	managedInScope := false
	if m, ok := s.managedNamed(ctx, name); ok && !proto.IsExternalLauncher(m.Name) && s.usableOnAsking(ctx, m, machine, false) {
		managedInScope = true
		if m.Kind == "api" {
			merged := sessionLaunchers([]ConfiguredLauncher{{Launcher: proto.Launcher{
				Name: m.Name, BaseURL: m.BaseURL, Model: m.Model, Source: "persona", Agent: m.AgentType(),
			}}}, s.hello(ctx).Models.Launchers[machine])
			if l, ok := find(merged); ok {
				return l, true
			}
		}
	}
	cachedKnown := foundMachine && (name == "claude" || managedInScope || slices.ContainsFunc(seen, func(l proto.Launcher) bool { return l.Name == name }))
	if cachedKnown {
		if l, ok := find(s.pickerLaunchersSeen(ctx, machine, seen, false)); ok {
			return l, true
		}
	}
	// When the cached machine snapshot does not contain name (or machine is absent from the cache), query the live
	// node through pickerLaunchers (bypassing the positive cache and backfilling on success) so newly added launchers
	// on machine are validated against their real capabilities and hub.json overrides rather than falling back to a
	// hub.json-only default or cross-machine provider union.
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if live, err := s.pickerLaunchers(withLauncherRefresh(cctx), machine); err == nil {
		if l, ok := find(live); ok {
			return l, true
		}
	}
	if !cachedKnown {
		if l, ok := find(s.pickerLaunchersSeen(ctx, machine, seen, false)); ok {
			return l, true
		}
	}
	return ConfiguredLauncher{}, false
}

func (s *Service) permissionModesForLauncher(ctx context.Context, machine, name string, provider *ModelProvider) []string {
	agent, listed := s.launcherPermissionModeDetails(ctx, machine, name, provider)
	return permissionModesForAgent(agent, listed)
}

func (s *Service) launcherPermissionModeDetails(ctx context.Context, machine, name string, provider *ModelProvider) (string, proto.PermissionModeSet) {
	var agent string
	var listed proto.PermissionModeSet
	machineResolved := false
	if l, ok := s.targetMachineLauncher(ctx, machine, name); ok {
		// When a specific machine is known, use that machine's merged launcher definition (managed-provider
		// precedence + discovered launcher + hub.json overrides) instead of the cross-machine provider union.
		agent = cmp.Or(l.AgentType, l.Agent)
		listed = l.PermissionModes
		machineResolved = true
	}
	if !machineResolved && provider != nil {
		agent = provider.Agent
		listed = provider.PermissionModes
	}
	if name != "" {
		if !machineResolved && agent == "" && listed == 0 {
			for _, l := range s.storedLaunchers(ctx) {
				if l.Name == name {
					agent = l.AgentType()
					break
				}
			}
		}
		if !machineResolved && agent == "" && listed == 0 {
			for _, v := range s.launcherViews(ctx) {
				if v.Name == name {
					agent = viewAgent(v)
					listed = v.PermissionModes
					break
				}
			}
		}
		if agent == "" && listed == 0 && slices.Contains([]string{"codex", "traex", "agy", "omp", "pi", "cursor"}, name) {
			agent = name
		}
	}
	return agent, listed
}

func (s *Service) validateModel(ctx context.Context, p ModelPurpose, b ModelBinding, providers []ModelProvider) error {
	if err := checkExternalPurpose(b); err != nil {
		return err
	}
	var provider *ModelProvider
	for i := range providers {
		if providers[i].ID == b.Provider {
			provider = &providers[i]
			break
		}
	}
	if p.ID == purposeMeetingRoom && !s.roomClaudeCode(ctx, p.MachineID, b.Provider, providers) {
		return errMeetingRoomClaudeOnly // before anything else: Codex, a runtime, a Responses or Chat connection
	}
	if p.Kind == "launcher" && p.CategoryID == "assistant" && p.MachineID != "" {
		// Model settings and the picker share the current machine's merged launchers, not the global discovery cache.
		name := strings.TrimPrefix(b.Provider, "launcher.")
		if provider == nil {
			// Not in the global discovery table yet: only a configured wrapper or a launcher on the machine's own list.
			_, configured := s.configuredLauncher(ctx, p.MachineID, name)
			if _, known := s.launchersHere(ctx, p.MachineID); !configured && !known {
				return i18n.Err("assistant-models.err_provider_missing")
			}
		}
		fit, refusal := s.mainChatFit(ctx, p.MachineID, name)
		switch fit {
		case mainChatGone:
			return i18n.Err("assistant-models.err_provider_missing")
		case mainChatRefused:
			return refusal
		}
	} else {
		if provider == nil {
			return i18n.Err("assistant-models.err_provider_missing")
		}
		if !slices.Contains(p.ProviderIDs, b.Provider) {
			return i18n.Err("assistant-models.err_provider_unsupported")
		}
	}
	if b.Effort != "" {
		agent := ""
		if p.Kind == "api" && provider != nil && provider.Kind == "launcher" {
			if name, ok := strings.CutPrefix(b.Provider, "launcher."); ok {
				agent = s.runLauncherAgent(ctx, cmp.Or(p.MachineID, s.settings(ctx).MachineID), name)
			}
		}
		if !slices.Contains(effortLevelsForPurpose(p.Kind, provider, agent), b.Effort) {
			return i18n.Err("assistant-models.err_effort_unsupported")
		}
	}
	if b.PermissionMode != "" {
		if p.Kind != "launcher" {
			return i18n.Err("assistant-models.err_permission_mode_launcher_only")
		}
		if !slices.Contains(proto.PermissionModes, b.PermissionMode) {
			return i18n.Err("assistant-models.err_permission_mode_unsupported")
		}
		name := strings.TrimPrefix(b.Provider, "launcher.")
		agent, listed := s.launcherPermissionModeDetails(ctx, p.MachineID, name, provider)
		if !permissionModeSupportedForPurpose(p.ID, agent, listed, b.PermissionMode) {
			return i18n.Err("assistant-models.err_permission_mode_unsupported")
		}
		if b.PermissionMode == "plan" && s.desktopAppProvider(ctx, b.Provider) {
			return i18n.Err("assistant-models.err_permission_mode_unsupported") // the app runs no read-only plan start
		}
	}
	if p.Kind == "api" {
		c, err := s.apiConfig(ctx, b)
		if err == nil && c.RunLauncher != "" {
			return checkModelID(b.Model) // passed to the CLI as --model
		}
		return err
	}
	if p.Kind == "plugin" {
		if b.Effort != "" {
			return i18n.Err("assistant-models.err_extension_effort")
		}
		if b.Model != "" {
			if provider.Status != "" {
				return errors.New(provider.Status)
			}
			if !slices.ContainsFunc(provider.Models, func(m proto.ProviderModel) bool { return m.ID == b.Model }) {
				return i18n.Err("assistant-models.err_extension_model")
			}
		}
		return nil
	}
	if p.Kind == "launcher" {
		if provider != nil && len(provider.Machines) > 0 && p.MachineID != "" && !slices.Contains(provider.Machines, p.MachineID) {
			return i18n.Err("assistant-models.err_launcher_not_on_machine")
		}
		// Claude's desktop app has no command to probe: its node checks the model id at the start, and the app takes
		// the model through its session's settings (models_desktop.go).
		if b.Model != "" && !s.desktopAppProvider(ctx, b.Provider) {
			machine := ""
			if p.MachineID == "" {
				// Like the model picker, unbound purposes use an online candidate;
				// with none available, saving leaves the model unverified.
				machine, _ = s.pickMachine(ctx, provider.Machines, "")
			} else if _, ok := s.device(ctx, p.MachineID); ok {
				machine = p.MachineID
			} else if len(provider.Machines) > 0 {
				if _, ok := s.device(ctx, provider.Machines[0]); ok {
					machine = provider.Machines[0]
				}
			}
			if machine != "" {
				var result struct {
					Agent string `json:"agent"`
				}
				params := map[string]any{"model": b.Model}
				if p.CategoryID == "assistant" {
					params["purpose"] = "assistant"
				}
				if err := s.call(ctx, machine, assistantwire.DeviceLaunchersModelCheck, s.launcherParams(ctx, machine, strings.TrimPrefix(b.Provider, "launcher."), params), &result); err != nil {
					return i18n.Err("assistant-models.err_check_model", i18n.Args{"error": i18n.Localize(err, s.requestCtxLang(ctx))})
				}
				if p.CategoryID == "agent.new" && result.Agent != "claude" && result.Agent != "codex" && result.Agent != "agy" && result.Agent != "omp" && !(result.Agent == "traex" && (p.ID == "agent.new" || p.ID == purposeAgent)) {
					return i18n.Err("assistant-models.err_agent_launcher")
				}
				if p.ID == purposeMeetingRoom && result.Agent != "" && result.Agent != "claude" {
					return errMeetingRoomClaudeOnly // the machine runs another agent under this launcher's name
				}
				if result.Agent != "" && !permissionModeSupportedForPurpose(p.ID, result.Agent, 0, b.PermissionMode) {
					return i18n.Err("assistant-models.err_permission_mode_unsupported")
				}
			}
		}
	}
	return nil
}

// providerModelUses are the current configurable purposes assigned to provider: a provider in use is not deleted underneath them.
// Automatic selection by the built-in API categories is not an assignment; current extension purposes also protect an inherited provider.
// The background supervisor's use is one of them (modelConfigIDs, #5806): its own provider, or the main chat's while it follows it;
// so are the In-meeting conversation's, the same way, and Meeting minutes' (#7980): its own provider, or Information
// processing's explicit one while it follows it, an automatic selection there being no use as for the category.
func (s *Service) providerModelUses(ctx context.Context, provider string) ([]string, error) {
	uses := []string{}
	ids := slices.Clone(modelConfigIDs)
	for _, x := range s.modelExtensions(ctx) {
		ids = append(ids, assistantwire.ExtensionPurposePrefix+x.StorageKey())
	}
	for _, id := range ids {
		p, err := s.modelPurposeDef(ctx, id)
		if err != nil {
			return nil, err
		}
		b, err := s.resolveModel(ctx, id)
		if err != nil {
			return nil, err
		}
		if b.Provider != provider {
			continue
		}
		if p.Kind == "api" && (slices.Contains(modelCategoryIDs, id) || id == purposeMeetingMinutes) {
			// Automatic selection follows the remaining providers; only an assignment that still names this provider
			// without its automatic fallback blocks deletion (Meeting minutes' own pick, or Information processing's it
			// follows, still names it). Current extension purposes keep their inherited-use guard.
			without := slices.DeleteFunc(slices.Clone(s.storedLaunchers(ctx)), func(l storedLauncher) bool {
				return launcherProvider(l.Name) == provider
			})
			withoutCtx := withManagedSnapshot(ctx, without)
			fallback, err := s.resolveModel(withoutCtx, id)
			if err != nil {
				return nil, err
			}
			if fallback.Provider != "" && fallback.Provider != provider {
				if strings.HasPrefix(fallback.Provider, "launcher.") || s.legacyModelConfig(withoutCtx, fallback.Provider).BaseURL != "" {
					continue
				}
			}
		}
		uses = append(uses, p.Name)
	}
	return uses, nil
}

// triageInUse refuses a change that leaves inbox.triage uncallable while plugins that need it are on: 422
// triage_model_in_use, naming them (not 409: the apps read a 409 from model-config as an edit conflict and would hide
// the message; a 422 they show as it is). nil when the change may go (docs/extensions/extensions.md "General rules for the Inbox").
func (s *Service) triageInUse(ctx context.Context, id string, effective ModelBinding) (map[string]any, error) {
	if id != "inbox.triage" || s.modelUsable(ctx, effective) {
		return nil, nil
	}
	var res struct {
		Names []string `json:"names"`
	}
	if err := s.appCall(ctx, AppPlugins, assistantwire.PluginsNeedTriage, map[string]any{}, &res); err != nil {
		return nil, err
	}
	if len(res.Names) == 0 {
		return nil, nil
	}
	l := s.requestCtxLang(ctx)
	return map[string]any{"code": "triage_model_in_use", "plugins": res.Names,
		"error": i18n.T(l, "assistant-models.triage_in_use", i18n.Args{"names": strings.Join(res.Names, i18n.T(l, "assistant-models.list_separator"))})}, nil
}
