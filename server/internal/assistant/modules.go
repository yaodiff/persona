package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/kernel"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/modapi"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/wireids"
)

// The built-in feature modules (docs/architecture/core-and-modules.md): the assistant reaches them only through its
// kernel (internal/assistant/kernel), which loads those compiled in (internal/modules/all) and switched on.

// modulesKey is the settings row that switches built-in modules off: {"disabled": ["<id>", …]}. A module switched off
// is not loaded at the manager's next start, as if it were not compiled in; its settings stay stored, hidden.
const modulesKey = "modules"

// modulesFeature marks a manager that publishes module.<id> feature flags. It is present even with every module off or
// built out, so clients can distinguish that case from an older manager whose hello has no module flags.
const modulesFeature = "modules"

// ModuleSwitch is the value of the modules settings row.
type ModuleSwitch struct {
	Disabled []string `json:"disabled"`
}

// ReadModuleSwitch reads the modules settings row: the ids of the modules switched off, built-in ones, client-only
// ones (modapi.ClientModules) and those installed extensions may supply (modapi.ExtensionModules). The manager reads it
// once, when it starts (main.go), and hands it to New, so the switch takes effect at the next start; New itself reads
// no database. A row that cannot be read switches nothing off.
func ReadModuleSwitch(ctx context.Context, st Store, logf func(format string, args ...any)) []string {
	if st == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var sw ModuleSwitch
	if _, err := st.Setting(ctx, modulesKey, &sw); err != nil {
		if logf != nil {
			logf("modules: reading the %s setting: %v; loading every built-in module", modulesKey, err)
		}
		return nil
	}
	return sw.Disabled
}

// storedModuleSwitch reads the row for persona_settings. A missing row or stored null is the default, every module on.
func (s *Service) storedModuleSwitch(ctx context.Context) (ModuleSwitch, error) {
	var sw ModuleSwitch
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, modulesKey, &sw); err != nil {
			return ModuleSwitch{}, err
		}
	}
	return canonicalModuleSwitch(sw), nil
}

func canonicalModuleSwitch(sw ModuleSwitch) ModuleSwitch {
	sw.Disabled = slices.Clone(sw.Disabled)
	if sw.Disabled == nil {
		sw.Disabled = []string{}
	}
	slices.Sort(sw.Disabled)
	return sw
}

// getModuleSwitch is persona_settings' read of the runtime module switch. A missing row, stored null and an empty list
// all mean the default: every compiled-in module is on. Unlike startup's best-effort read, a database error is returned.
func (s *Service) getModuleSwitch(ctx context.Context) (string, error) {
	sw, err := s.storedModuleSwitch(ctx)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(sw)
	return string(raw), err
}

// setModuleSwitch prepares a validated, owner-approved change of the runtime module switch. The manager reads this row
// only at startup, so saving never pretends to change the modules in the running process.
func (s *Service) setModuleSwitch(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var next ModuleSwitch
	if err := json.Unmarshal(raw, &next); err != nil {
		return nil, errors.New("modules must be null or an object with a disabled string list: " + err.Error())
	}
	seen := map[string]bool{}
	for _, id := range next.Disabled {
		if !s.modules.Loaded(id) && s.modules.Skipped(id) == "" && !slices.Contains(modapi.ClientModules(), id) && !extensionModule(id) {
			return nil, errors.New("unknown module " + strconv.Quote(id) + "; client-only modules: " + strings.Join(modapi.ClientModules(), ", ") +
				"; modules from extensions: " + strings.Join(extensionModuleIDs(), ", "))
		}
		if seen[id] {
			return nil, errors.New("duplicate module " + strconv.Quote(id))
		}
		seen[id] = true
	}
	next = canonicalModuleSwitch(next)
	before, err := s.storedModuleSwitch(ctx)
	if err != nil {
		return nil, err
	}
	if slices.Equal(before.Disabled, next.Disabled) {
		return &ownChange{summary: i18n.T(s.lang(ctx), "assistant-settings_tool.modules_unchanged")}, nil
	}
	st := s.store()
	if st == nil {
		return nil, errors.New("no database on this hub")
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(next)
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_modules", i18n.Args{"before": string(beforeJSON), "after": string(afterJSON)})
	}, apply: func(ctx context.Context) (string, error) {
		if err := st.PutSetting(ctx, modulesKey, next); err != nil {
			return "", err
		}
		return i18n.T(s.lang(ctx), "assistant-settings_tool.modules_saved", i18n.Args{"after": string(afterJSON)}), nil
	}}, nil
}

// extensionModule: id is a module an approved installed extension may supply (modapi.ExtensionModules). The modules
// setting can switch it off on any build, one without the built-in module of that id too.
func extensionModule(id string) bool {
	return slices.Contains(extensionModuleIDs(), id)
}

// extensionModuleIDs are the ids of modapi.ExtensionModules.
func extensionModuleIDs() []string {
	var out []string
	for _, m := range modapi.ExtensionModules() {
		out = append(out, string(m.ID))
	}
	return out
}

// loadModules loads mods (nil: the built-in ones) into the kernel, leaving out those switched off. Service.New calls
// this after its core routes are registered, so the host checks below see the complete core namespace.
func (s *Service) loadModules(mods []modapi.Module, off []string) {
	if mods == nil {
		mods = kernel.Builtin()
	}
	coreTools := reservedCoreToolNames()
	coreRoutes := slices.Concat(s.routes, s.routeFallbacks)
	var acceptedRoutes []string
	check := func(routes, tools []string) error {
		for _, name := range tools {
			if coreTools[name] || compat.Tool(name) != name {
				return fmt.Errorf("tool %q is a core tool's", name)
			}
		}
		if err := checkModuleRoutes(coreRoutes, acceptedRoutes, routes); err != nil {
			return err
		}
		// The kernel commits this module immediately after a nil result, so these routes belong to the next check's
		// accepted set. A failed tool or route check returns before this append and reserves nothing.
		acceptedRoutes = append(acceptedRoutes, routes...)
		return nil
	}
	s.modulesOff = slices.Clone(off)
	s.modules = kernel.NewWithOptions(mods, kernel.LoadOptions{
		Off:   func(id string) bool { return slices.Contains(off, id) },
		Logf:  s.logf,
		Check: check,
		Ports: s.modulePorts(),
	})
	for _, pattern := range s.modules.HubRoutes() {
		handler, _ := s.modules.HubRoute(pattern)
		s.handle(pattern, func(w http.ResponseWriter, r *http.Request) {
			// A module's handler is the kernel's, shared by every manager in the process: the request says which
			// manager's lark layer it is for (larkhost.WithLayer).
			handler(w, r.WithContext(larkhost.WithLayer(r.Context(), s.larkModule)))
		})
	}
	s.logf("modules: loaded %s", loadedModules(s.modules))
}

// reservedCoreToolNames are names a module may not take, whether a conditional core tool is enabled now or later.
func reservedCoreToolNames() map[string]bool {
	out := map[string]bool{}
	for _, name := range append(slices.Clone(toolNames), ownerActionPrepareTool, "todo_split", "todo_merge", "health_intake", "health_strength", "urgent_call", knowledgeSearchTool, knowledgeReadTool, spaceStateTool, externalSourcesReadTool, externalSourcesWriteTool,
		"resource_acquire", "resource_get", "resource_renew", "resource_release", "resource_cancel") {
		out[name] = true
	}
	return out
}

// checkModuleRoutes applies the real ServeMux registration rules without changing the live mux. The core and accepted
// patterns are already valid; a proposed conflict or malformed pattern returns an error for the whole module.
func checkModuleRoutes(core, accepted, proposed []string) error {
	mux := http.NewServeMux()
	register := func(pattern string) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
		}()
		mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
		return nil
	}
	for _, patterns := range [][]string{core, accepted} {
		for _, pattern := range patterns {
			if err := register(pattern); err != nil {
				return fmt.Errorf("existing route %q: %v", pattern, err)
			}
		}
	}
	for _, pattern := range proposed {
		if err := register(pattern); err != nil {
			return fmt.Errorf("route %q conflicts with a core or loaded module route: %v", pattern, err)
		}
	}
	return nil
}

// loadedModules names the loaded modules for the manager's log, in load order ("none" with none): a core-only build's
// manager says "none" (scripts/ci/core-only-smoke.py reads the line).
func loadedModules(k *kernel.Kernel) string {
	var ids []string
	for _, info := range k.Modules() {
		ids = append(ids, info.ID)
	}
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

// moduleFeatures are the module protocol marker, the loaded modules' wire feature flags in load order, then the flags
// of the client-only modules (modapi.ClientModules) that the modules setting leaves on. An off or excluded module
// announces no flag of its own; the marker remains so clients know the absence means off. An id that is also compiled
// in as a built-in module is that module's to announce; the list never holds one (internal/modules/all tests it), since
// a module built out is unknown to the kernel and the list would announce it.
func (s *Service) moduleFeatures() []string {
	out := []string{modulesFeature}
	for _, m := range s.modules.Modules() {
		if m.Feature != "" && !slices.Contains(out, m.Feature) {
			out = append(out, m.Feature)
		}
	}
	for _, id := range modapi.ClientModules() {
		if slices.Contains(s.modulesOff, id) || s.modules.Loaded(id) || s.modules.Skipped(id) != "" {
			continue
		}
		if f := "module." + id; !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// moduleTools are the loaded modules' MCP definitions. Off modules contribute none.
func (s *Service) moduleTools() []proto.Tool {
	var out []proto.Tool
	for _, tool := range s.modules.MCPTools() {
		out = append(out, proto.Tool{Name: tool.Name, Description: tool.Description, InputSchema: json.RawMessage(tool.InputSchema)})
	}
	return out
}

// runModuleTool dispatches a loaded module's tool; ok is false for an off or unknown tool. The caller's bare machine
// and session strings become the contract's ID types here, once, and the module's result becomes the text every
// tool returns.
func (s *Service) runModuleTool(ctx context.Context, machine, self, name string, args json.RawMessage) (out string, err error, ok bool) {
	tool, ok := s.modules.MCPTool(name)
	if !ok {
		return "", nil, false
	}
	res, err := tool.Run(ctx, modapi.ToolCall{Machine: wireids.MachineID(machine), Session: wireids.SessionID(self), Args: modapi.ToolArgs(args)})
	return res.Text, err, true
}

// runModuleJobs starts every loaded module's loop under the Service Run context. Cancellation is normal shutdown.
func (s *Service) runModuleJobs(ctx context.Context) {
	for _, job := range s.modules.BackgroundJobs() {
		job := job
		go func() {
			if err := job.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.logf("module background job %s: %v", job.Name, err)
			}
		}()
	}
}

// moduleValue is a module setting's value as the assistant's settings keep it: the JSON as stored, nil when never set.
// A stored null reads as never set, so it is left out when the settings are written again, as the typed pointer it
// replaces was.
type moduleValue json.RawMessage

func (v moduleValue) MarshalJSON() ([]byte, error) {
	if len(v) == 0 {
		return []byte("null"), nil
	}
	return json.RawMessage(v).MarshalJSON()
}

func (v *moduleValue) UnmarshalJSON(data []byte) error {
	if string(bytes.TrimSpace(data)) == "null" {
		*v = nil
		return nil
	}
	*v = append(moduleValue(nil), data...) // a copy: never the backing array of a value another copy holds
	return nil
}

// moduleStored is what is stored for a module setting key (nil: never set). Each module key the assistant keeps in
// its own settings has its field here, so a switched-off module's value stays as it was.
func moduleStored(v Settings, key string) json.RawMessage {
	switch key {
	case "assistant.briefing_waits":
		return json.RawMessage(v.BriefingWaits)
	}
	return nil
}

func moduleStore(v *Settings, key string, raw json.RawMessage) error {
	if string(bytes.TrimSpace(raw)) == "null" {
		raw = nil
	}
	switch key {
	case "assistant.briefing_waits":
		v.BriefingWaits = moduleValue(raw)
		return nil
	}
	return errors.New(key + " has no place in the assistant's settings")
}

func unavailableModuleSetting(key string) error {
	return errors.New(key + " is not available: its module is off")
}

// moduleSettingGet is persona_settings' get of a module setting: the value in force.
func (s *Service) moduleSettingGet(key string) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		m, ok := s.modules.Setting(key)
		if !ok {
			return "", unavailableModuleSetting(key)
		}
		return string(m.Value(moduleStored(s.settings(ctx), key))), nil
	}
}

// moduleSettingSet is an ordinary persona_settings change of a module setting: the tool only prepares a before/after
// card (cardKey, in the settings tool's namespace, with before and after), and the existing approval path calls apply.
// null restores the module's defaults. The allowlisted briefing setting also carries its raw state for the audited
// automatic path; switch-off keeps the module's effective-value no-op behavior.
func (s *Service) moduleSettingSet(key, cardKey, applied string) func(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	return func(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
		m, ok := s.modules.Setting(key)
		if !ok {
			return nil, unavailableModuleSetting(key)
		}
		next, err := m.Check(raw)
		if err != nil {
			return nil, err
		}
		saved := s.settings(ctx)
		beforeRaw := reversibleSettingJSON(moduleStored(saved, key))
		afterRaw := reversibleSettingJSON(next)
		before, after := m.Value(moduleStored(saved, key)), m.Value(next)
		if s.hello(ctx).Assistant.AutoApproveReversibleSettings && reversibleSettingAutoApproved(key) {
			if bytes.Equal(beforeRaw, afterRaw) {
				return &ownChange{summary: key + " is unchanged"}, nil
			}
		} else if bytes.Equal(before, after) {
			return &ownChange{summary: key + " is unchanged"}, nil
		}
		if s.store() == nil {
			return nil, errors.New("no database on this hub")
		}
		change := &ownChange{cardText: func(l i18n.Lang) string {
			return i18n.T(l, "assistant-settings_tool."+cardKey, i18n.Args{"before": m.Describe(before), "after": m.Describe(after)})
		}, apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { return moduleStore(v, key, next) }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return applied, nil
		}}
		if reversibleSettingAutoApproved(key) {
			change.reversible = &reversibleSetting{
				before: beforeRaw,
				after:  afterRaw,
				current: func(v Settings) json.RawMessage {
					return reversibleSettingJSON(moduleStored(v, key))
				},
				// Check returns nil for null and canonical JSON otherwise, so this is moduleStore's validated assignment.
				assign:  func(v *Settings) { v.BriefingWaits = moduleValue(next) },
				applied: applied,
			}
		}
		return change, nil
	}
}

// briefingWaits are the wait rules of the routine facts' module for the settings v; false when no module gives the
// routine facts (the briefing is off or not compiled in).
func (s *Service) briefingWaits(v Settings) (modapi.WaitRules, bool) {
	f, ok := s.modules.ScheduleFacts()
	if !ok {
		return nil, false
	}
	return f.Waits(moduleStored(v, f.Setting)), true
}

// briefingOn says whether a module gives the routine facts: without one a routine with facts on opens with none, and
// a daily report with the north star's lines alone.
func (s *Service) briefingOn() bool {
	_, ok := s.modules.ScheduleFacts()
	return ok
}

// briefingRunner returns the loaded briefing module's runtime runner, or false when the module is off or built out.
func (s *Service) briefingRunner() (modapi.ScheduleFactsRunner, bool) {
	f, ok := s.modules.ScheduleFacts()
	if !ok || f.Runner == nil {
		return nil, false
	}
	return f.Runner, true
}
