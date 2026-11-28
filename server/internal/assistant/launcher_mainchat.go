package assistant

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- which launchers can run the assistant's conversations ----
//
// The main chat, a scheduled task's conversation and the side conversations run headless on their machine: claude -p
// or codex app-server started through the launcher's shell command (node/assistant_start.go), or a runtime that
// declared caps.assistant. The picker (?purpose=assistant) lists assistantLaunchers (modelProviderCompatible), and the
// check before each message (sendModel) holds a launcher to the same list through mainChatFit. On top of that, the
// pickers leave out a launcher whose latest health check failed (launcher_health_check.go, withHealth): health keeps a
// message from being refused as launcher_missing on a wrong read, but a failing launcher is not refused here, and a
// model configuration that names one is left alone.

// mainChatFit is whether launcher name can run an assistant conversation on machine: in the main chat's list (fit),
// one the machine has that can't run it (refused, with why in the user's language), or not there at all (gone).
// Plain claude fits without asking; a list that can't be read fits too, as launchersHere has it: nothing is refused
// or replaced on a guess.
type inboxAssistantFitKey struct{}

type mainChatFit int

const (
	mainChatFits mainChatFit = iota
	mainChatRefused
	mainChatGone
)

type mainChatFitResult struct {
	fit          mainChatFit
	refusal      error
	transient    bool
	launchers    []ConfiguredLauncher
	discoveryErr error
}

func (s *Service) mainChatFit(ctx context.Context, machine, name string) (mainChatFit, error) {
	r := s.mainChatFitState(ctx, machine, name, false)
	return r.fit, r.refusal
}

// mainChatFitState also reports whether gone is a discovery miss that may recover, the Assistant-compatible catalog
// from this read, and a discovery error. Recovery reuses that catalog at its deadline instead of starting an unbudgeted
// final read. A stored provider outside the machine's scope is not a transient miss and must not hold a message.
func (s *Service) mainChatFitState(ctx context.Context, machine, name string, refresh bool) mainChatFitResult {
	if proto.IsExternalLauncher(name) {
		_, err := s.externalTarget(ctx, machine, name)
		if err != nil {
			return mainChatFitResult{fit: mainChatRefused, refusal: err}
		}
		return mainChatFitResult{fit: mainChatFits}
	}
	if name == "" || name == "claude" {
		return mainChatFitResult{fit: mainChatFits}
	}
	ctx = withUnhidden(withSettingsMemo(ctx), name)
	if m, managed := s.managedNamed(ctx, name); managed { // the same on every machine in its scope, except managed agy (needs the agy runtime there)
		if !s.usableOn(ctx, m, machine) {
			return mainChatFitResult{fit: mainChatGone}
		}
		if m.Kind == "api" || m.Agent != "agy" {
			allowed, err := s.assistantProviders(ctx, machine, nil)
			if err != nil || allowed[name] {
				return mainChatFitResult{fit: mainChatFits}
			}
			return mainChatFitResult{fit: mainChatRefused, refusal: s.mainChatRefusal(ctx, machine, name, "")}
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if refresh {
		cctx = withLauncherRefresh(cctx)
	}
	picker, err := s.pickerLaunchers(cctx, machine)
	if err != nil {
		if refresh {
			return mainChatFitResult{fit: mainChatGone, transient: true, discoveryErr: err} // a failed retry is not recovery
		}
		return mainChatFitResult{fit: mainChatFits, discoveryErr: err} // an ordinary unreadable list is not proof of a miss
	}
	if cctx.Value(inboxAssistantFitKey{}) == true {
		for _, launcher := range picker {
			if launcher.Name == name && cmp.Or(launcher.AgentType, launcher.Agent) == "traex" {
				return mainChatFitResult{fit: mainChatFits}
			}
		}
	}
	fits, fitErr := s.assistantLaunchersOf(cctx, machine, picker)
	if !slices.ContainsFunc(picker, func(l ConfiguredLauncher) bool {
		source := compat.Source(l.Source)
		return l.Source != "" && source != "persona" && source != "builtin"
	}) {
		if refresh {
			// Managed/built-in entries can be valid substitutes, but their presence alone is not evidence that the
			// requested shell launcher recovered. Older nodes and hidden shell catalogs must not release the send early.
			return mainChatFitResult{fit: mainChatGone, transient: true, launchers: fits, discoveryErr: fitErr}
		}
		return mainChatFitResult{fit: mainChatFits} // the machine's own ordinary list did not come
	}
	named := func(l ConfiguredLauncher) bool { return l.Name == name }
	if fitErr != nil {
		if refresh {
			return mainChatFitResult{fit: mainChatGone, transient: true, discoveryErr: fitErr}
		}
		return mainChatFitResult{fit: mainChatFits}
	}
	switch {
	case slices.ContainsFunc(fits, named):
		return mainChatFitResult{fit: mainChatFits, launchers: fits}
	case slices.ContainsFunc(picker, named):
		label := launcherLabel(picker[slices.IndexFunc(picker, named)])
		instead, _ := assistantFor(fits, name)
		return mainChatFitResult{fit: mainChatRefused, refusal: s.mainChatRefusal(ctx, machine, label, launcherLabel(instead)), launchers: fits}
	}
	return mainChatFitResult{fit: mainChatGone, transient: true, launchers: fits}
}

const (
	defaultLauncherRecoveryWait = 30 * time.Second
	maxLauncherRecoveryWait     = 2 * time.Minute
)

var launcherRecoveryPoll = time.Second

func (s *Service) launcherRecovery(ctx context.Context, machine, name string) (time.Duration, []string) {
	cfg := s.hello(ctx).Models.LauncherRecovery[machine]
	wait := defaultLauncherRecoveryWait
	if cfg.WaitSeconds > 0 {
		wait = time.Duration(cfg.WaitSeconds) * time.Second
	}
	if wait > maxLauncherRecoveryWait {
		wait = maxLauncherRecoveryWait
	}
	return wait, cfg.Fallbacks[name]
}

// waitMainChatLauncher retries fresh discovery inside one total budget, starting immediately. Request cancellation
// wins. The last successful fresh result, including its catalog, is retained at exhaustion so fallback selection needs
// no read after the deadline and a launcher found by the last successful read is used rather than replaced. If no
// catalog arrived, the latest discovery error is returned.
func (s *Service) waitMainChatLauncher(ctx context.Context, machine, name string, wait time.Duration) mainChatFitResult {
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	ticker := time.NewTicker(launcherRecoveryPoll)
	defer ticker.Stop()
	last := mainChatFitResult{fit: mainChatGone, transient: true}
	haveCatalog := false
	for {
		if wctx.Err() != nil {
			return last
		}
		r := s.mainChatFitState(wctx, machine, name, true)
		if ctx.Err() != nil {
			return mainChatFitResult{fit: mainChatGone, transient: true, discoveryErr: ctx.Err()}
		}
		if r.fit != mainChatGone {
			return r
		}
		if r.discoveryErr == nil {
			last, haveCatalog = r, true
		} else if !haveCatalog {
			last = r // report the discovery failure only when no fresh catalog ever arrived
		}
		select {
		case <-wctx.Done():
			return last
		case <-ticker.C:
		}
	}
}

// mainChatOffers: p is one of the main chat's purposes (its own, a scheduled task's, the background supervisor's; not
// the In-meeting conversation's, which has its own rule, models_room.go) and its computer's main-chat picker offers p's
// launcher, by the picker's rule (assistantProviders) over that computer's picker as last seen (pickerLaunchersSeen):
// no node is asked, not even whether a provider that skips a computer with its own service is used there (an answer
// missing or out of date counts as no service, usableOnAsking), so reading Model settings never waits on one. This is
// how Model settings' status tells a launcher the provider list leaves out from one that is not there: a runtime's
// launcher that runs the assistant (a runtime declaring caps.assistant, #11772) or a hub.json wrapper only that
// computer names. One gone from the list, or there but unable to run the assistant (a runtime without the capability),
// is not offered, and neither is the computer's own launcher by the name of a Persona-managed provider in its scope:
// the picker and mainChatFit run the provider, so its own compatibility is the answer. A hidden one is still offered,
// as mainChatFit has it: hiding takes it out of the pickers only.
func (s *Service) mainChatOffers(ctx context.Context, p ModelPurpose) bool {
	if p.Kind != "launcher" || p.CategoryID != "assistant" || p.ID == purposeMeetingRoom || p.MachineID == "" {
		return false
	}
	name, ok := strings.CutPrefix(p.Effective.Provider, "launcher.")
	if !ok || name == "" || proto.IsExternalLauncher(name) {
		return false
	}
	ctx = withUnhidden(ctx, name)
	var seen []proto.Launcher
	snapshot, _ := s.zshrcLaunchersLastSeen(ctx)
	for _, m := range snapshot {
		if m.MachineID == p.MachineID {
			seen = m.Launchers
		}
	}
	picker := s.pickerLaunchersSeen(ctx, p.MachineID, seen, false)
	if !slices.ContainsFunc(picker, func(l ConfiguredLauncher) bool { return l.Name == name }) {
		return false
	}
	allowed, err := s.assistantProviders(ctx, p.MachineID, picker)
	return err == nil && allowed[name]
}

// finishMainChatModel validates the binding after model resolution, including a default or a legacy fallback that
// was not the caller's explicit pick. A discovery miss gets one bounded rediscovery window. If the launcher still
// cannot run Assistant there, only launchers explicitly named in its recovery policy may replace it, and only ones of
// the same agent kind (mainChatInstead): a Codex conversation started on Claude, or the other way round, would run
// without the configured model or credentials (#4659). Built-in claude has no implicit priority or eligibility. The
// model stays when an approved substitute's list has it. With no approved substitute, nothing is sent and the user is
// told why. note says which launcher runs instead, in the user's language.
func (s *Service) finishMainChatModel(ctx context.Context, machine, purpose string, params map[string]any, picked bool) (note string, err error) {
	name, _ := params["launcher"].(string)
	if proto.IsExternalLauncher(name) {
		if _, ok := ctx.Value(externalPickKey{}).(externalPickScope); !ok {
			return "", i18n.Err("external-adapter.scope_required")
		}
		_, err := s.externalTarget(ctx, machine, name)
		return "", err // never swap an explicit external selection
	}
	r := s.mainChatFitState(ctx, machine, name, false)
	wait, fallbacks := s.launcherRecovery(ctx, machine, name)
	recovered := r.fit == mainChatGone && r.transient && wait > 0
	if recovered {
		s.logf("[%s] resolved assistant launcher %s is missing; refreshing discovery for up to %s", machine, name, wait)
		r = s.waitMainChatLauncher(ctx, machine, name, wait)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if r.fit == mainChatFits {
		return "", nil
	}
	if r.discoveryErr != nil {
		return "", r.discoveryErr
	}
	launchers := r.launchers
	if !recovered && launchers == nil {
		// A non-transient managed-provider miss has no picker snapshot to reuse. This is not a post-deadline retry.
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var err error
		launchers, err = s.assistantLaunchers(withLauncherRefresh(cctx), machine)
		if err != nil {
			return "", err
		}
	}
	kind := s.mainChatLauncherKind(ctx, machine, name)
	d, _ := s.device(ctx, machine)
	where := firstNonEmpty(d.Name, machine)
	to := s.mainChatInstead(ctx, d, name, kind, launchers, fallbacks)
	model, _ := params["model"].(string)
	// An unreadable catalog normally keeps the requested model. A canceled request must never authorize a fallback.
	kept := to != "" && model != "" && s.launcherHasModel(withLauncherPurpose(ctx, "assistant"), machine, to, model)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if to == "" {
		if r.refusal != nil {
			return "", r.refusal
		}
		return "", i18n.Err("assistant-start.launcher_no_substitute", i18n.Args{"launcher": name, "machine": where})
	}
	next := maps.Clone(params)
	next["launcher"] = to
	if !kept {
		delete(next, "model")
	}
	if picked {
		err = s.applyPickedModel(ctx, next)
	} else {
		err = s.applyModel(ctx, purpose, next)
	}
	if err != nil {
		return "", err
	}
	args := i18n.Args{"launcher": name, "machine": where, "to": to, "model": model}
	key := "assistant-start.launcher_swapped"
	if model != "" && !kept {
		key = "assistant-start.launcher_swapped_model"
	}
	note = i18n.T(s.lang(ctx), key, args)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	clear(params)
	maps.Copy(params, next)
	s.logf("[%s] resolved assistant launcher %s can't run it, using %s (model kept: %v)", machine, name, to, kept)
	leaveLauncherNote(ctx, note)
	return note, nil
}

// leaveLauncherNote puts note where withLauncherNote's caller reads it, if it asked.
func leaveLauncherNote(ctx context.Context, note string) {
	if slot, ok := ctx.Value(launcherNoteKey{}).(*string); ok && note != "" {
		*slot = note
	}
}

type launcherNoteKey struct{}

// withLauncherNote gives finishMainChatModel somewhere to leave its note (which launcher runs instead, and why) for
// the caller that answers the app: the start route and assistant.send_params, whose answers carry it as
// launcher_note for the app to show (#4659).
func withLauncherNote(ctx context.Context) (context.Context, *string) {
	n := new(string)
	return context.WithValue(ctx, launcherNoteKey{}, n), n
}

// mainChatInstead chooses only from names, the resolved launcher's explicitly configured ordered substitutes. Every
// candidate must currently run Assistant on the machine, have the same agent kind, and have no failed health check.
// In particular, plain claude is not implicitly preferred or eligible. "" when no approved substitute fits.
func (s *Service) mainChatInstead(ctx context.Context, d Device, name, kind string, launchers []ConfiguredLauncher, names []string) string {
	if kind == "" {
		return "" // a disappeared launcher's kind is unknown: no substitute can be proven to be the same agent
	}
	for _, n := range names {
		if n == name || proto.IsExternalLauncher(n) {
			continue
		}
		i := slices.IndexFunc(launchers, func(l ConfiguredLauncher) bool { return l.Name == n })
		if i < 0 {
			continue
		}
		l := launchers[i]
		if l.Source == proto.ExternalLauncherSource || agentKind(cmp.Or(l.AgentType, l.Agent)) != kind {
			continue
		}
		if _, bad := s.launcherUnusable(ctx, d.ID, n); !bad {
			return n
		}
	}
	return ""
}

// mainChatLauncherKind keeps the agent identity of a launcher that has just disappeared from discovery. The last
// successful per-machine snapshot is evidence, including an empty agent (the snapshot's Claude default). With no such
// evidence it returns "": a missing custom Codex alias must not become Claude merely because its catalog entry is gone.
func (s *Service) mainChatLauncherKind(ctx context.Context, machine, name string) string {
	if agent := s.launcherAgent(withUnhidden(ctx, name), machine, name); agent != "" {
		return agentKind(agent)
	}
	seen, _ := s.zshrcLaunchersLastSeen(ctx)
	for _, m := range seen {
		if m.MachineID != machine {
			continue
		}
		for _, l := range m.Launchers {
			if l.Name == name {
				return agentKind(l.Agent)
			}
		}
	}
	return ""
}

// agentKind is the agent a launcher starts: claude when it names none. A gone launcher needs mainChatLauncherKind's
// positive evidence before this default applies.
func agentKind(agent string) string { return cmp.Or(agent, "claude") }

// mainChatRefusal says that launcher can't run the assistant on machine, for the user's language, and names instead
// when a launcher there declares it runs the assistant for that one (assistantFor; "" when none does).
func (s *Service) mainChatRefusal(ctx context.Context, machine, launcher, instead string) error {
	d, _ := s.device(ctx, machine)
	args := i18n.Args{"launcher": launcher, "machine": firstNonEmpty(d.Name, machine), "instead": instead}
	if instead != "" {
		return i18n.Err("assistant-start.launcher_not_main_chat_instead", args)
	}
	return i18n.Err("assistant-start.launcher_not_main_chat", args)
}

// assistantFor is the launcher in fits (the main chat's list on a computer) that declares it runs the assistant for
// that computer's Agent launcher name (proto.Launcher.AssistantFor): integrations/grok's grok-chat for the Paseo provider
// grok when its companion was set up so. Only a declaration counts, never a likeness of names or agents: two launchers
// of one agent may run another account or gateway. The picker points name at it; nothing runs on it in name's place.
func assistantFor(fits []ConfiguredLauncher, name string) (ConfiguredLauncher, bool) {
	i := slices.IndexFunc(fits, func(f ConfiguredLauncher) bool { return f.Assistant && f.AssistantFor == name && f.Name != name })
	if i < 0 {
		return ConfiguredLauncher{}, false
	}
	return fits[i], true
}

// launcherLabel is the name people know l by: its display name (a runtime's or Paseo's for its provider) or its own;
// "" for none.
func launcherLabel(l ConfiguredLauncher) string { return cmp.Or(l.DisplayName, l.Name) }

// ---- what can't run the assistant's conversations, and why ----

// Why a launcher or provider is in the main chat picker's unavailable list (GET /api/v1/machines/{mid}/launchers?purpose=
// assistant, #5033), besides proto.CodeLauncherOutOfScope and proto.CodeLauncherOwnService.
const (
	unavailableAPIProtocol = "api_protocol"       // a direct API provider speaking Responses or Chat Completions, not Messages
	unavailableNotMainChat = "not_main_chat"      // its agent or engine can't run the assistant (a Paseo provider, managed Codex…)
	unavailableUnhealthy   = "launcher_unhealthy" // its latest health check on the computer failed (withHealth, #14535)
)

// unavailableLauncher is one launcher or provider the main chat can't run on a machine, and why in the user's words.
type unavailableLauncher struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"` // a Persona-managed provider's own name, or a runtime's for its provider ("" shows Name)
	Kind        string `json:"kind"`                   // launcher | api
	Agent       string `json:"agent,omitempty"`
	API         string `json:"api,omitempty"` // a direct API provider's protocol: messages | responses | chat
	Code        string `json:"code"`
	Reason      string `json:"reason"`
	Instead     string `json:"instead,omitempty"` // the launcher set up to run the assistant for this one here (assistantFor), by name
}

// mainChatUnavailable lists what the main chat's model panel shows as unavailable on machine, with why in lang: the
// machine's launchers (picker, pickerLaunchers) that can't run the assistant (fits, assistantLaunchersOf, are the ones
// that can), then Settings › Model providers' providers that can't run it here: a direct API that speaks Responses or
// Chat Completions, one whose agent can't, one out of the computer's device scope or skipping a computer with its
// own sign-in. Hidden launchers stay out, and so does a provider that would run here but is not listed for another
// reason (a failed health check, a list that has not come yet): nothing here refuses it.
func (s *Service) mainChatUnavailable(ctx context.Context, machine string, picker, fits []ConfiguredLauncher, lang i18n.Lang) []unavailableLauncher {
	ctx = withSettingsMemo(ctx)
	out := []unavailableLauncher{}
	seen := map[string]bool{}
	for _, l := range fits {
		seen[l.Name] = true
	}
	for _, name := range s.hiddenLaunchers(ctx) {
		seen[name] = true
	}
	stored := s.storedLaunchers(ctx)
	managed := func(name string) (storedLauncher, bool) {
		i := slices.IndexFunc(stored, func(l storedLauncher) bool { return l.Name == name })
		if i < 0 {
			return storedLauncher{}, false
		}
		return stored[i], true
	}
	d, _ := s.device(ctx, machine)
	where := firstNonEmpty(d.Name, machine)
	notMainChat := i18n.T(lang, "assistant-model_pick.unavailable_not_main_chat", i18n.Args{"machine": where})
	for _, l := range picker {
		if seen[l.Name] {
			continue
		}
		seen[l.Name] = true
		// named as the picker names it: a runtime's provider by the runtime's name for it (#8261)
		u := unavailableLauncher{Name: l.Name, DisplayName: l.DisplayName, Kind: "launcher", Agent: cmp.Or(l.AgentType, l.Agent), Code: unavailableNotMainChat,
			Reason: notMainChat}
		if proto.IsExternalLauncher(l.Name) || l.Source == proto.ExternalLauncherSource {
			if l.External != nil {
				u.DisplayName = cmp.Or(l.External.Name, l.DisplayName)
			}
			if err := s.externalLauncherRefusal(ctx, l.Launcher); err != nil {
				u.Reason = i18n.Localize(err, lang)
			}
		}
		if m, ok := managed(l.Name); ok {
			u.DisplayName = m.DisplayName
		} else if f, ok := assistantFor(fits, l.Name); ok { // the launcher set up to run the assistant for this one
			u.Instead = f.Name
			u.Reason = i18n.T(lang, "assistant-model_pick.unavailable_not_main_chat_instead", i18n.Args{"machine": where, "instead": launcherLabel(f)})
		}
		out = append(out, u)
	}
	p, err := s.modelPurposeDef(ctx, "assistant")
	p.MachineID = "" // what the provider is, its scope apart: that is the next reason
	for _, l := range stored {
		if seen[l.Name] {
			continue
		}
		seen[l.Name] = true
		kind := cmp.Or(l.Kind, "launcher")
		u := unavailableLauncher{Name: l.Name, DisplayName: l.DisplayName, Kind: kind, Agent: l.Agent}
		if kind == "api" {
			u.Agent, u.API = "", cmp.Or(l.API, "messages")
		}
		switch {
		case kind == "api" && !messagesAPI(l.API):
			u.Code, u.Reason = unavailableAPIProtocol, apiProtocolText(lang, l.API)
		case err == nil && !s.modelProviderCompatible(p, ModelProvider{ID: "launcher." + l.Name, Kind: kind, Agent: l.Agent, API: l.API}, stored):
			u.Code, u.Reason = unavailableNotMainChat, notMainChat
		case s.scopeReason(l, machine) != "":
			u.Code, u.Reason = proto.CodeLauncherOutOfScope, s.scopeText(ctx, lang, s.scopeReason(l, machine), l, machine)
		case !s.usableOn(ctx, l, machine): // its own-service rule, as the node said
			u.Code, u.Reason = proto.CodeLauncherOwnService, s.ownServiceErr(ctx, lang, l, machine).Error()
		default:
			continue
		}
		out = append(out, u)
	}
	return out
}

// ownServiceSkipped lists what New Agent's picker shows as unavailable on machine, with why in lang (#8109): the
// Persona-managed providers in the computer's scope that its own-service rule leaves out there, where the node said
// the harness has a service of its own. A provider added with the defaults (own_service skip) would otherwise just be
// missing from the picker on a computer where Claude Code is signed in. Those out of scope stay out (the scope is
// what the user chose for the provider), and so do hidden names, direct API connections (never in this picker) and
// what the picker lists already (a launcher of the computer's own by the same name).
func (s *Service) ownServiceSkipped(ctx context.Context, machine string, listed []ConfiguredLauncher, lang i18n.Lang) []unavailableLauncher {
	hidden := s.hiddenLaunchers(ctx)
	out := []unavailableLauncher{}
	for _, l := range s.storedLaunchers(ctx) {
		if l.Kind == "api" || !l.SkipsOwnService() || slices.Contains(hidden, l.Name) ||
			slices.ContainsFunc(listed, func(c ConfiguredLauncher) bool { return c.Name == l.Name }) {
			continue
		}
		if s.scopeReason(l, machine) != "" || s.usableOn(ctx, l, machine) {
			continue
		}
		out = append(out, unavailableLauncher{Name: l.Name, DisplayName: l.DisplayName, Kind: "launcher", Agent: l.Agent,
			Code: proto.CodeLauncherOwnService, Reason: s.ownServiceErr(ctx, lang, l, machine).Error()})
	}
	return out
}

// apiProtocolText says in lang why a direct API provider speaking api can't run the assistant's conversations.
func apiProtocolText(lang i18n.Lang, api string) string {
	switch api {
	case "responses":
		return i18n.T(lang, "assistant-model_pick.unavailable_api_responses")
	case "chat":
		return i18n.T(lang, "assistant-model_pick.unavailable_api_chat")
	}
	return i18n.T(lang, "assistant-model_pick.unavailable_api_other", i18n.Args{"api": api})
}
