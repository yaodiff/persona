package assistant

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func (s *Service) launcherRoutes() {
	s.handle("GET /api/v1/launchers", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), launcherSyncTimeout+5*time.Second)
		defer cancel()
		// A last-seen snapshot answers at once; a first-ever list still fills before it answers.
		zshrc, cached := s.zshrcLaunchersLastSeen(ctx)
		if cached {
			s.refreshZshrcLaunchers(r.Context())
		} else {
			zshrc = s.zshrcLaunchers(ctx, true)
		}
		views := s.withKeyMachines(ctx, s.launcherViewsOf(ctx, zshrc))
		if b, ok := s.builtinClaude(ctx, zshrc); ok {
			views = append(views, b)
		}
		apierr.WriteJSON(w, map[string]any{"launchers": views, "sync": launcherSyncList(zshrc)})
	})
	s.handle("GET /api/v1/launchers/{name}", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		name := r.PathValue("name")
		if b, ok := s.builtinClaude(ctx, s.zshrcLaunchers(ctx, false)); ok && name == b.Name {
			apierr.WriteJSON(w, map[string]any{"launcher": b})
			return
		}
		for _, v := range s.withKeyMachines(ctx, s.launcherViews(ctx)) {
			if v.Name != name {
				continue
			}
			if machineSource(v.Source) {
				// Every machine at once, the text and the probe apart: a launcher that fetches a token first is slow to
				// probe (the node allows 15 s), and its text should still show.
				v.Definitions, v.Details = map[string]string{}, map[string]proto.LauncherDetail{}
				var mu sync.Mutex
				var wg sync.WaitGroup
				for _, id := range v.Machines {
					wg.Add(2)
					go func() {
						defer wg.Done()
						var res struct {
							Definition string `json:"definition"`
						}
						if err := s.call(ctx, id, assistantwire.DeviceLaunchersShow, map[string]any{"name": name}, &res); err == nil {
							mu.Lock()
							v.Definitions[id] = res.Definition
							mu.Unlock()
						}
					}()
					go func() {
						defer wg.Done()
						var res struct {
							Detail proto.LauncherDetail `json:"detail"`
						}
						if err := s.call(ctx, id, DeviceLaunchersProbe, s.launcherParams(ctx, id, name, map[string]any{}), &res); err == nil {
							mu.Lock()
							v.Details[id] = res.Detail
							mu.Unlock()
						}
					}()
				}
				wg.Wait()
			} else if v.Source == "agy" || (v.Agent == "agy" && v.Source != "" && v.Source != "persona" && v.Source != "builtin") {
				v.Details = map[string]proto.LauncherDetail{}
				auth := "Antigravity OAuth (~/.gemini/antigravity-cli)"
				if v.Source != "agy" {
					auth = "Antigravity ACP (" + v.Source + ")"
				}
				for _, id := range v.Machines {
					l := s.requestCtxLang(ctx)
					models := []proto.LauncherModel{{Role: i18n.T(l, "assistant-models_launchers.role_default"), Env: "agy acp", Model: v.Model}}
					if res, err := s.askLauncherModels(ctx, id, name, false); err == nil && len(res.Models) > 0 {
						models = nil
						for _, am := range res.Models {
							role := i18n.T(l, "assistant-models_launchers.role_optional")
							if am.Default {
								role = i18n.T(l, "assistant-models_launchers.role_default")
							}
							models = append(models, proto.LauncherModel{Role: role, Env: am.ID, Model: am.Name})
						}
					}
					v.Details[id] = proto.LauncherDetail{
						Agent:  "agy",
						Auth:   auth,
						Models: models,
					}
				}
			}
			apierr.WriteJSON(w, map[string]any{"launcher": v})
			return
		}
		apierr.Write(w, http.StatusNotFound, errors.New("no such launcher"))
	})
	s.handle("POST /api/v1/launchers", func(w http.ResponseWriter, r *http.Request) {
		var in launcherInput
		if err := decodeBody(w, r, &in, 1<<20); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		ctx := r.Context()
		if err := s.presetFill(ctx, &in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		for _, id := range in.Assign {
			if !slices.Contains(assignablePurposes, id) {
				apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-models_launchers.err_assign", i18n.Args{"purpose": strconv.Quote(id)}))
				return
			}
		}
		stored, err := s.readStoredLaunchers(ctx)
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		views := s.launcherViewsFrom(ctx, s.zshrcLaunchers(ctx, false), stored)
		taken := func(n string) bool {
			return slices.ContainsFunc(views, func(v LauncherView) bool { return v.Name == n })
		}
		if in.Name == "" {
			display := ""
			if in.DisplayName != nil {
				display = strings.TrimSpace(*in.DisplayName)
			}
			var n int
			if in.Preset != "" {
				in.Name, n = launcherNameFor(in.Preset, taken)
				if display == "" {
					d := s.presetDisplayName(ctx, in.Preset, n)
					in.DisplayName = &d
				}
			} else {
				// Without a preset (launcher_auto_name, #5029): named after the display name, else after the gateway's
				// host, which then is the display name too.
				host := gatewayHost(strings.TrimSpace(in.BaseURL))
				in.Name, n = customLauncherName(display, host, taken)
				if display == "" && host != "" {
					d := numberedName(host, n)
					in.DisplayName = &d
				}
			}
		}
		if err := in.check(); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err := s.checkScope(ctx, in.scopeInput); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err := s.checkVia(in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if taken(in.Name) {
			apierr.Write(w, http.StatusConflict, i18n.Err("assistant-models_launchers.err_name_taken", i18n.Args{"name": in.Name}))
			return
		}
		if err := s.sealKeyReferences(ctx, &in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		l, err := s.saveLauncher(ctx, in, nil)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errAPITransport) || errors.Is(err, errChatTransport) || errors.Is(err, errChatLaunchSettings) || errors.Is(err, errRuntimeHeaders) || errors.Is(err, errScopeEmpty) {
				status = http.StatusBadRequest
			}
			apierr.Write(w, status, err)
			return
		}
		out := map[string]any{"launcher": s.withKeyMachines(ctx, []LauncherView{s.launcherView(l)})[0]}
		if in.Assign != nil {
			assigned, kept := s.assignIfDefault(ctx, launcherProvider(l.Name), in.Assign)
			out["assigned"], out["kept"] = assigned, kept
		}
		apierr.WriteJSON(w, out)
	})
	s.handle("PUT /api/v1/launchers/{name}", func(w http.ResponseWriter, r *http.Request) {
		var in launcherInput
		if err := decodeBody(w, r, &in, 1<<20); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		in.Name = r.PathValue("name")
		stored, err := s.readStoredLaunchers(r.Context())
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		var old *storedLauncher
		for _, l := range stored {
			if l.Name == in.Name {
				l := l
				old = &l
				break
			}
		}
		if old != nil && in.Agent == "" && in.Kind != "api" {
			in.Agent = old.Agent
		}
		if err := in.check(); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if old == nil {
			apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-models_launchers.err_not_managed"))
			return
		}
		if old.channelAlias != "" {
			apierr.Write(w, http.StatusBadRequest, errChannelAlias(*old))
			return
		}
		if err := s.checkScope(r.Context(), in.scopeInput); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err := s.checkVia(in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err := s.sealKeyReferences(r.Context(), &in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		saved, err := s.saveLauncher(r.Context(), in, old)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errChatProtocolChange) || errors.Is(err, errAPITransport) || errors.Is(err, errChatTransport) || errors.Is(err, errChatLaunchSettings) || errors.Is(err, errRuntimeHeaders) || errors.Is(err, errScopeEmpty) {
				status = http.StatusBadRequest
			}
			apierr.Write(w, status, err)
			return
		}
		if launcherSetupChanged(*old, saved) {
			s.forgetLauncherFailures(r.Context(), saved.Name)
		}
		apierr.WriteJSON(w, map[string]any{"launcher": s.withKeyMachines(r.Context(), []LauncherView{s.launcherView(saved)})[0]})
	})
	s.handle("DELETE /api/v1/launchers/{name}", func(w http.ResponseWriter, r *http.Request) {
		ctx, name := r.Context(), r.PathValue("name")
		s.models.mu.Lock()
		list, err := s.readStoredLaunchers(ctx)
		if err != nil {
			s.models.mu.Unlock()
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		if i := slices.IndexFunc(list, func(l storedLauncher) bool { return l.Name == name }); i >= 0 {
			if list[i].channelAlias != "" {
				s.models.mu.Unlock()
				apierr.Write(w, http.StatusBadRequest, errChannelAlias(list[i]))
				return
			}
			// Check dependencies, delete and publish the confirmed snapshot under the configuration lock.
			uses, err := s.providerModelUses(withManagedSnapshot(ctx, list), launcherProvider(name))
			if err == nil && len(uses) == 0 {
				err = s.storeManagedLaunchers(ctx, slices.Delete(list, i, i+1))
			}
			s.models.mu.Unlock()
			if err != nil {
				apierr.Write(w, 500, err)
				return
			}
			if len(uses) > 0 {
				apierr.Write(w, 409, i18n.Err("assistant-models_launchers.err_in_use", i18n.Args{"uses": strings.Join(uses, i18n.T(s.requestCtxLang(r.Context()), "assistant-models_launchers.list_sep"))}))
				return
			}
			s.forgetLauncherSecrets(name) // what was kept of its keys (launcher_secrets.go)
			apierr.WriteJSON(w, map[string]any{"ok": true, "deleted": true})
			return
		}
		// an rc-file launcher, or plain claude (#4384): hidden in Persona, the file is not touched; a start that names it,
		// or a default that runs it, still runs it. Under the lock, like an import's hiding and a restore.
		hidden, err := s.readHiddenLaunchers(ctx)
		if err == nil && !slices.Contains(hidden, name) {
			err = s.putSetting(ctx, launchersHiddenKey, append(hidden, name))
		}
		s.models.mu.Unlock()
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		s.invalidateLauncherListCache("")
		apierr.WriteJSON(w, map[string]any{"ok": true, "hidden": true})
	})
	// "Test": one real request through the launcher on a machine (default: the assistant's).
	s.handle("POST /api/v1/launchers/{name}/check", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if proto.IsExternalLauncher(name) {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("external-adapter.scope_required"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), launcherCheckTimeout)
		defer cancel()
		provider := ModelProvider{ID: launcherProvider(name), Kind: "launcher"}
		for _, l := range s.storedLaunchers(ctx) {
			if l.Name == name && l.Kind == "api" {
				provider.Kind, provider.Model = "api", l.Model
				break
			}
		}
		machine, res, err := s.checkModelProvider(ctx, provider, r.URL.Query().Get("machine"), r.URL.Query().Get("purpose"))
		if err != nil {
			if errors.Is(err, errNoLauncherCheckMachine) {
				apierr.Write(w, http.StatusBadRequest, err)
			} else {
				apierr.Call(w, err)
			}
			return
		}
		out := map[string]any{"check": res}
		if machine != "" {
			out["machine"] = machine
		}
		apierr.WriteJSON(w, out)
	})
	// "Refresh key": the provider extension is asked again for the launcher's keys that are references (the secret was
	// rotated in it); until then what is kept answers every use (launcher_secrets.go). Never a value in the answer.
	s.handle("POST /api/v1/launchers/{name}/refresh-key", s.refreshLauncherKey)
	// What still names a launcher as its own (Model configuration's purposes, extensions' too, scheduled tasks, model
	// profiles and their tiers), in words, for the hide confirmation (#4661): hiding an rc-file launcher only takes it
	// out of the pickers, and these keep running on it.
	s.handle("GET /api/v1/launchers/{name}/uses", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		f, err := s.importHideFacts(ctx, "", nil, s.storedLaunchers(ctx))
		var uses []string
		if err == nil {
			uses, err = s.aliasUses(ctx, f, r.PathValue("name"))
		}
		if err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"uses": append([]string{}, uses...)})
	})
	s.handle("POST /api/v1/launchers/{name}/restore", func(w http.ResponseWriter, r *http.Request) {
		ctx, name := r.Context(), r.PathValue("name")
		s.models.mu.Lock() // launchers.hidden changes one at a time (the import, hiding, restoring)
		hidden, err := s.readHiddenLaunchers(ctx)
		if err == nil {
			err = s.putSetting(ctx, launchersHiddenKey, slices.DeleteFunc(hidden, func(n string) bool { return n == name }))
		}
		s.models.mu.Unlock()
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		s.invalidateLauncherListCache("")
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}

// sessionLaunchers is the machine's launchers with the configured ones (hub.json machines[].launchers) applied: a
// configured launcher overrides the type, base URL and model of the one with its name, or adds a wrapper the node
// does not list. A launcher without a type gets the agent it was found to start, else one by its name.
func sessionLaunchers(discovered, configured []ConfiguredLauncher) []ConfiguredLauncher {
	list := append([]ConfiguredLauncher{}, discovered...)
	for _, override := range configured {
		if proto.IsExternalLauncher(override.Name) || override.Source == proto.ExternalLauncherSource {
			continue // only this machine's node may advertise an external target
		}
		found := false
		for i := range list {
			if list[i].Name == override.Name {
				if override.AgentType != "" {
					list[i].AgentType = override.AgentType
				}
				if override.BaseURL != "" {
					list[i].BaseURL = override.BaseURL
				}
				if override.Model != "" {
					list[i].Model = override.Model
				}
				found = true
				break
			}
		}
		if !found && override.Name != "" {
			list = append(list, override)
		}
	}
	for i := range list {
		if list[i].AgentType == "" {
			switch {
			case list[i].Agent != "":
				list[i].AgentType = list[i].Agent
			case list[i].Name == "codex" || list[i].Name == "traex" || list[i].Name == "agy":
				list[i].AgentType = list[i].Name
			default:
				list[i].AgentType = "claude"
			}
		}
	}
	return list
}

var errUnknownMachine = errors.New("unknown machine")

// pickerTier is one of the New Agent form's presets: a tier of the profile in use with a candidate this machine starts,
// shown on that candidate (the first one it has, in the tier's order; Candidate is its position, 0 the first).
type pickerTier struct {
	ModelTier
	Name      string `json:"name"`
	Candidate int    `json:"candidate,omitempty"`
}

// pickerTiers are the presets the New Agent form offers on a machine with these launchers: the tiers of the profile
// in use, by name, each on its first candidate the machine has, but those with no candidate the machine has (a direct
// API provider is in no machine's list) or the list leaves out because its latest check failed. None when no profile is in use or it cannot be read: the
// form then has no preset row.
func (s *Service) pickerTiers(ctx context.Context, machine string, launchers []ConfiguredLauncher) (*ModelProfile, []pickerTier) {
	p, _, err := s.activeModelProfile(ctx)
	if err != nil || p == nil {
		return nil, nil
	}
	hidden := s.hiddenLaunchers(ctx)
	var out []pickerTier
	for _, name := range slices.Sorted(maps.Keys(p.Tiers)) {
		t := p.Tiers[name]
		for i, c := range t.list() {
			if proto.IsExternalLauncher(c.Launcher) {
				continue
			}
			if slices.ContainsFunc(launchers, func(l ConfiguredLauncher) bool { return l.Name == c.Launcher }) ||
				slices.Contains(hidden, c.Launcher) && s.hiddenUsable(ctx, machine, c.Launcher) { // hidden still runs (#4661)
				out = append(out, pickerTier{ModelTier: t.on(c), Name: name, Candidate: i})
				break
			}
		}
	}
	return p, out
}

// hiddenUsable: machine has hidden launcher name (launchersHere, which a start goes by) and its latest check did not
// fail, as withHealth holds the visible ones.
func (s *Service) hiddenUsable(ctx context.Context, machine, name string) bool {
	_, _, usable := s.hiddenLauncherInventory(ctx, machine, name)
	return usable
}

// hiddenLauncherInventory retains the explicit-hidden read so candidate metadata can come from the same RPC.
func (s *Service) hiddenLauncherInventory(ctx context.Context, machine, name string) (list []proto.Launcher, known, usable bool) {
	if _, bad := s.launcherUnusable(ctx, machine, name); bad {
		return nil, false, false
	}
	list, known = s.launchersHere(withUnhidden(ctx, name), machine)
	return list, known, known && slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == name })
}

func queryRefresh(r *http.Request) bool {
	v := strings.TrimSpace(r.URL.Query().Get("refresh"))
	return v == "1" || strings.EqualFold(v, "true")
}

// machineLauncherRoutes are "New Agent"'s pickers on one machine: its launchers with the agent each starts
// (sessionLaunchers), and a launcher's /model list (node/launcher_models.go; the node keeps a list 10 minutes,
// ?refresh=1 asks the CLI again) with the ids the user typed for it before (models_custom.go).
func (s *Service) machineLauncherRoutes() {
	s.handle("GET /api/v1/machines/{mid}/launchers", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		mid := r.PathValue("mid")
		d, ok := s.device(ctx, mid)
		if !ok {
			apierr.Write(w, http.StatusNotFound, errUnknownMachine)
			return
		}
		// New Agent lists what a start in its host can run (?host=, else the computer's default): where that is an
		// engine that runs only its own launchers (Paseo), its providers alone (#8140). The main chat's menu lists the
		// computer's own launchers: the assistant runs them on its command line, whatever the engine.
		purpose := r.URL.Query().Get("purpose")
		refresh := queryRefresh(r)
		host := ""
		if purpose == "" || purpose == "agent.new" {
			host = agentStartHost(d, r.URL.Query().Get("host"))
		}
		var list []ConfiguredLauncher
		var err error
		var unavailable []unavailableLauncher
		var all []ConfiguredLauncher
		switch purpose {
		case "assistant", purposeSupervise:
			if !d.ExternalInventory {
				// Leave a short window for own-service checks even when the shell uses its full sync timeout.
				ctx = context.WithValue(ctx, pickerDiscoveryDeadline{}, time.Now().Add(launcherSyncTimeout+time.Second))
			}
			all, list, unavailable, err = s.assistantPickerLaunchers(ctx, d, refresh, s.requestLang(r))
		case "task":
			callCtx := ctx
			if refresh {
				callCtx = withLauncherRefresh(ctx)
			}
			all, err = s.pickerLaunchersOn(callCtx, mid, "")
			if err == nil {
				list, err = s.assistantLaunchersOf(ctx, mid, all)
				if err == nil {
					unavailable = s.mainChatUnavailable(ctx, mid, all, list, s.requestLang(r))
				}
			}
		default:
			callCtx := ctx
			if refresh {
				callCtx = withLauncherRefresh(ctx)
			}
			list, err = s.pickerLaunchersOn(callCtx, mid, host)
			list = slices.DeleteFunc(list, func(l ConfiguredLauncher) bool {
				return proto.IsExternalLauncher(l.Name) || l.Source == proto.ExternalLauncherSource
			})
		}
		if err != nil {
			apierr.Call(w, err)
			return
		}
		if purpose == purposeSupervise {
			list, unavailable = s.supervisionPickerLaunchers(list, unavailable, s.requestLang(r))
		}
		// A launcher whose latest check failed is left out of the pickers and of the presets that name it; the launcher
		// the configuration runs with no pick stays, with its record, so the form can still say why this start may fail.
		// New Agent lists the ones left out as unavailable, with why (#14535).
		def := s.pickerDefault(ctx, r, all)
		if (purpose == "" || purpose == "agent.new") && host == "" {
			def = s.desktopDefaultHere(ctx, d, def, list) // no Claude desktop app here: what the start runs instead
		}
		keep, _ := def["provider"].(string)
		shown, left := s.withHealth(ctx, mid, list, keep)
		// A Persona-managed provider shows its display name over its ID (launcher_auto_name), and a runtime's provider the
		// name the runtime gives it (Paseo's label, the node's display_name, #8261); tiers and default still name the
		// ID, which clients look up here.
		stored := s.storedLaunchers(ctx)
		shownName := func(l ConfiguredLauncher) string {
			if proto.IsExternalLauncher(l.Name) && l.External != nil {
				return l.External.Name
			}
			j := slices.IndexFunc(stored, func(m storedLauncher) bool { return m.Name == l.Name })
			if j >= 0 && proto.CanonicalLauncherSource(l.Source) == "persona" {
				return stored[j].DisplayName
			}
			return l.DisplayName
		}
		for i, l := range shown {
			shown[i].DisplayName = shownName(l.ConfiguredLauncher)
		}
		for i, l := range left {
			left[i].DisplayName = shownName(l.ConfiguredLauncher)
		}
		out := map[string]any{"launchers": shown, "default": def}
		if unavailable != nil {
			out["unavailable"] = unavailable
		}
		if purpose == "" || purpose == "agent.new" {
			// where a start that keeps the default opens, when the computer starts New Agents as Orca chats (#17650)
			if opens := s.orcaChatDefault(ctx, d, def, r.URL.Query().Get("host")); opens != nil {
				out["default_opens"] = opens
			}
			usable := make([]ConfiguredLauncher, 0, len(shown))
			for _, l := range shown {
				usable = append(usable, l.ConfiguredLauncher)
			}
			if p, tiers := s.pickerTiers(ctx, mid, usable); len(tiers) > 0 {
				out["tiers"], out["tiers_profile"] = tiers, map[string]string{"id": p.ID, "name": p.Name}
			}
			// the launchers whose latest check failed here (#14535), and the providers left out here for their
			// own-service rule (#8109), as the main chat's has them: listed off, with why
			lang := s.requestLang(r)
			skipped := unhealthyUnavailable(lang, firstNonEmpty(d.Name, mid), left)
			skipped = append(skipped, s.ownServiceSkipped(ctx, mid, list, lang)...)
			if len(skipped) > 0 {
				out["unavailable"] = skipped
			}
		}
		apierr.WriteJSON(w, out)
	})
	s.handle("GET /api/v1/machines/{mid}/launchers/{name}/models", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(withLauncherPurpose(r.Context(), r.URL.Query().Get("purpose")), 70*time.Second)
		defer cancel()
		mid := r.PathValue("mid")
		if _, ok := s.device(ctx, mid); !ok {
			apierr.Write(w, http.StatusNotFound, errUnknownMachine)
			return
		}
		if host := r.URL.Query().Get("host"); launcherPurpose(ctx) == "" && (host == "agy" || proto.IsEngineHost(host)) {
			ctx = context.WithValue(ctx, launcherModelHostKey{}, host)
		}
		res, err := s.askLauncherModels(ctx, mid, r.PathValue("name"), r.URL.Query().Get("refresh") == "1")
		if err != nil {
			writeLauncherModelsErr(w, err)
			return
		}
		apierr.WriteJSON(w, s.withCustomModels(ctx, mid, r.PathValue("name"), res)) // the ids typed here before (#1644)
	})
	s.customModelRoutes()
	s.handle("GET /api/v1/launchers/presets", func(w http.ResponseWriter, r *http.Request) {
		apierr.WriteJSON(w, map[string]any{"presets": s.launcherPresets(r.Context())})
	})
}

// pickMachine is the machine to ask about a launcher the machines in has have: want when it has it, else the
// assistant's, the hub's own, then the others; the first of them online. false when none is.
func (s *Service) pickMachine(ctx context.Context, has []string, want string) (string, bool) {
	devices := s.devices(ctx)
	local := ""
	for _, d := range devices {
		if d.Kind == "local" {
			local = d.ID
			break
		}
	}
	order := append([]string{want, s.settings(ctx).MachineID, local}, has...)
	for _, id := range order {
		if id == "" || !slices.Contains(has, id) {
			continue
		}
		if slices.ContainsFunc(devices, func(d Device) bool { return d.ID == id && d.Online }) {
			return id, true
		}
	}
	return "", false
}

// providerModels is GET /api/v1/model-config/providers/{id}/models: a launcher provider's /model list for Model configuration
// (docs/assistant/model-config.md "Choosing a model in model configuration"). Model configuration is not one machine's, so the list comes from a machine that has the
// launcher (pickMachine), and the answer says which. Other providers have no list to ask for: a plugin's comes with
// it, an API connection has none.
func (s *Service) providerModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	name, ok := strings.CutPrefix(r.PathValue("id"), "launcher.")
	if !ok || name == "" {
		apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-models_launchers.err_no_models"))
		return
	}
	found, everywhere := name == "claude", name == "claude"
	var has []string
	for _, v := range s.launcherViews(ctx) {
		if v.Name != name {
			continue
		}
		if v.Kind == "api" {
			apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-models_launchers.err_api_no_models"))
			return
		}
		found = true
		if compat.Source(v.Source) == "persona" { // personad launch runs on every computer in the provider's scope
			if l, ok := s.managedNamed(ctx, name); ok {
				for _, d := range s.devices(ctx) {
					if s.scopeReason(l, d.ID) == "" {
						has = append(has, d.ID)
					}
				}
				continue
			}
		}
		everywhere = everywhere || len(v.Machines) == 0
		has = append(has, v.Machines...)
	}
	if !found && name == claudeDesktopLauncher {
		// Claude's desktop app is Agents' provider, not a listed one (models_desktop.go): the computers that have it.
		p, ok := s.claudeDesktopProvider(ctx, s.zshrcLaunchers(ctx, false), nil)
		found, has = ok, p.Machines
	}
	if !found {
		apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-models_launchers.err_no_provider"))
		return
	}
	if everywhere {
		has = has[:0]
		for _, d := range s.devices(ctx) {
			has = append(has, d.ID)
		}
	}
	machine, ok := s.pickMachine(ctx, has, r.URL.Query().Get("machine"))
	if !ok {
		apierr.Write(w, http.StatusServiceUnavailable, i18n.Err("assistant-models_launchers.err_all_offline"))
		return
	}
	res, err := s.askLauncherModels(ctx, machine, name, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeLauncherModelsErr(w, err)
		return
	}
	apierr.WriteJSON(w, struct {
		proto.LauncherModels
		Machine string `json:"machine"`
	}{s.withCustomModels(ctx, machine, name, res), machine})
}

// writeLauncherModelsErr answers a failed /model list: a node too old to list them is 404 like an older hub (the apps
// leave the picker out instead of offering a retry that cannot work).
func writeLauncherModelsErr(w http.ResponseWriter, err error) {
	if strings.HasPrefix(err.Error(), "unknown method") {
		apierr.WriteCode(w, http.StatusNotFound, proto.CodeNodeOutdated, i18n.Err("assistant-models_launchers.err_node_outdated"))
		return
	}
	apierr.Call(w, err)
}
