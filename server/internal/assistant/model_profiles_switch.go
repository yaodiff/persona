package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/i18n"
)

// ---- switching ----

// modelProfileSwitch is what applyModelProfile and rollbackModelProfile did.
type modelProfileSwitch struct {
	From, To string
	Changed  []string
}

// modelProfileApproval is a dry-run's exact display and expected catalog
// revision. A non-nil preview makes the locked switch/rollback plan read-only.
type modelProfileApproval struct {
	revision int64
	display  string
}

type modelProfileDisplayChange struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// applyModelProfile switches to profile id: every category it sets is checked as a save in Settings › Model
// configuration would check it, then all of it, the launcher fields, what they replace (the rollback) and the history
// are written in one transaction. Nothing is written when anything fails. A switch to the profile already in use
// that changes nothing keeps the rollback as it was.
func (s *Service) applyModelProfile(ctx context.Context, id string, by ProfileActor) (modelProfileSwitch, error) {
	s.models.mu.Lock()
	sw, pushed, err := s.applyModelProfileLocked(ctx, id, by)
	s.models.mu.Unlock()
	if err == nil && (sw.From != sw.To || len(sw.Changed) > 0) {
		s.modelProfileSwitched(ctx, sw, pushed)
	}
	return sw, err
}

func (s *Service) applyModelProfileLocked(ctx context.Context, id string, by ProfileActor) (modelProfileSwitch, bool, error) {
	return s.modelProfileSwitchLocked(ctx, id, by, nil)
}

func (s *Service) modelProfileSwitchLocked(ctx context.Context, id string, by ProfileActor, preview *modelProfileApproval) (modelProfileSwitch, bool, error) {
	ps, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return modelProfileSwitch{}, false, err
	}
	if preview != nil {
		preview.revision = ps.Revision
	}
	i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == id })
	if i < 0 {
		return modelProfileSwitch{}, false, errModelProfileNotFound
	}
	p := ps.Profiles[i]
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return modelProfileSwitch{}, false, err
	}
	ctx = withManagedSnapshot(ctx, stored)
	if p.Purposes, err = s.normalizeProfilePurposes(ctx, p.Purposes, map[string]modelRecord{}, stored); err != nil {
		return modelProfileSwitch{}, false, err
	}
	if err := checkModelProfile(&p, stored); err != nil {
		return modelProfileSwitch{}, false, err
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return modelProfileSwitch{}, false, err
	}
	now := time.Now().UTC()
	image := &modelProfileImage{Profile: state.Active, At: now, Purposes: map[string]ModelBinding{}, Launchers: map[string]LauncherModels{}}
	writes := map[string]any{}
	sw := modelProfileSwitch{From: state.Active, To: id}
	providers := s.modelProviders(ctx)
	for _, cat := range modelCategoryIDs {
		b, ok := p.Purposes[cat]
		if !ok {
			continue
		}
		purpose, err := s.modelPurpose(ctx, cat, providers)
		if err != nil {
			return sw, false, err
		}
		b.ConfigDir = purpose.Override.ConfigDir // where the main chat's new conversations start is not a profile's (#4633)
		if b == purpose.Override {
			continue
		}
		base, _, err := s.modelBaseline(ctx, purpose)
		if err != nil {
			return sw, false, err
		}
		effective := effectiveModel(purpose, base, b)
		if body, err := s.triageInUse(ctx, cat, effective); err != nil {
			return sw, false, err
		} else if body != nil {
			return sw, false, i18n.Err("assistant-model_profiles.err_cannot_run", i18n.Args{"purpose": purpose.Name, "error": body["error"]})
		}
		if b.Effort != "" && purpose.Kind == "api" {
			config, err := s.apiConfig(ctx, effective)
			if err != nil {
				return sw, false, err
			}
			if !messagesAPI(config.API) && config.API != "responses" && config.API != "chat" {
				return sw, false, i18n.Err("assistant-models.err_api_effort")
			}
		}
		modelOnly := b
		modelOnly.ConfigDir = ""
		if modelOnly != (ModelBinding{}) {
			if err := s.validateModel(ctx, purpose, effective, providers); err != nil {
				return sw, false, i18n.Err("assistant-model_profiles.err_cannot_run", i18n.Args{"purpose": purpose.Name, "error": err})
			}
		}
		beforeBinding := purpose.Override
		if beforeBinding.Provider == "" && beforeBinding.Model != "" {
			if provider := s.migratableProvider(ctx, base.Provider); provider != "" {
				beforeBinding.Provider = provider
			}
		}
		image.Purposes[cat] = beforeBinding
		writes[modelRecordKey(cat)] = modelRecord{Revision: purpose.Revision + 1, Binding: b}
		sw.Changed = append(sw.Changed, cat)
	}
	changedLaunchers := false
	for _, name := range slices.Sorted(maps.Keys(p.Launchers)) {
		li := slices.IndexFunc(stored, func(l storedLauncher) bool { return l.Name == name })
		if li < 0 {
			return sw, false, i18n.Err("assistant-model_profiles.err_launcher_missing", i18n.Args{"launcher": name})
		}
		if stored[li].channelAlias != "" {
			return sw, false, errChannelAlias(stored[li])
		}
		set := p.Launchers[name]
		var before LauncherModels
		was := before.fields(&stored[li].ManagedLauncher)
		for k, f := range set.fields(&stored[li].ManagedLauncher) {
			if *f.set == nil || *f.launcher == **f.set {
				continue
			}
			if stored[li].Kind == "api" && f.name != "model" && **f.set != "" {
				return sw, false, i18n.Err("assistant-model_profiles.err_launcher_api", i18n.Args{"launcher": name, "field": f.name})
			}
			*was[k].set = profileValue(*f.launcher)
			*f.launcher = **f.set
			sw.Changed = append(sw.Changed, name+"."+f.name)
			changedLaunchers = true
		}
		if before != (LauncherModels{}) {
			image.Launchers[name] = before
			stored[li].Updated = now
		}
	}
	if len(sw.Changed) == 0 && state.Active == id {
		// Already in use: keep the rollback from the switch that got here.
		if preview != nil {
			preview.display = modelProfileApprovalDisplay("switch", "", sw, image, p.Purposes, stored)
			return sw, false, nil
		}
		return sw, false, nil
	}
	if preview != nil {
		preview.display = modelProfileApprovalDisplay("switch", "", sw, image, p.Purposes, stored)
		return sw, false, nil
	}
	if changedLaunchers {
		writes[launchersManagedKey] = persistedLaunchers(stored)
	}
	state.Active, state.At, state.By = id, now, by
	state.pushRollback(*image)
	state.record(ModelProfileEvent{At: now, Action: "switch", From: sw.From, To: id, By: by, Changed: sw.Changed})
	writes[modelProfileStateKey] = state
	if err := s.putModelProfileSettings(ctx, writes); err != nil {
		return sw, false, err
	}
	return sw, changedLaunchers, nil
}

// rollbackModelProfile undoes the most recent switch: what it replaced goes back, and the profile in use before it is
// in use again. Earlier switches stay on the bounded rollback stack so a quick A -> B -> A round trip can still be
// unwound to the pre-switch configuration; rolling back the last remaining point keeps a redo entry.
func (s *Service) rollbackModelProfile(ctx context.Context, by ProfileActor) (modelProfileSwitch, error) {
	return s.rollbackModelProfileTo(ctx, "", by)
}

// rollbackModelProfileTo rolls back atomically to targetID ("" for the latest rollback point), folding any intermediate
// rollback points so the configuration matches the state before targetID's switch.
func (s *Service) rollbackModelProfileTo(ctx context.Context, targetID string, by ProfileActor) (modelProfileSwitch, error) {
	s.models.mu.Lock()
	sw, pushed, err := s.rollbackModelProfileLocked(ctx, targetID, by)
	s.models.mu.Unlock()
	if err == nil && (sw.From != sw.To || len(sw.Changed) > 0) {
		s.modelProfileSwitched(ctx, sw, pushed)
	}
	return sw, err
}

func (s *Service) rollbackModelProfileLocked(ctx context.Context, targetID string, by ProfileActor) (modelProfileSwitch, bool, error) {
	return s.modelProfileRollbackLocked(ctx, targetID, by, nil)
}

func (s *Service) modelProfileRollbackLocked(ctx context.Context, targetID string, by ProfileActor, preview *modelProfileApproval) (modelProfileSwitch, bool, error) {
	if preview != nil {
		ps, err := s.modelProfilesLocked(ctx)
		if err != nil {
			return modelProfileSwitch{}, false, err
		}
		preview.revision = ps.Revision
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return modelProfileSwitch{}, false, err
	}
	stack := state.rollbackStack()
	idx := findRollbackIndex(stack, targetID)
	if idx < 0 {
		return modelProfileSwitch{}, false, errModelProfileNoRollback
	}
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return modelProfileSwitch{}, false, err
	}
	ctx = withManagedSnapshot(ctx, stored)
	prev := composeRollbackImage(stack, idx)
	records := map[string]modelRecord{}
	if prev.Purposes, err = s.normalizeProfilePurposes(ctx, prev.Purposes, records, stored); err != nil {
		return modelProfileSwitch{}, false, err
	}
	for name := range prev.Launchers {
		if !slices.ContainsFunc(stored, func(l storedLauncher) bool { return l.Name == name }) {
			return modelProfileSwitch{}, false, i18n.Err("assistant-model_profiles.err_launcher_missing", i18n.Args{"launcher": name})
		}
	}
	var providers []ModelProvider
	for _, b := range prev.Purposes {
		name, ok := strings.CutPrefix(b.Provider, "launcher.")
		if !ok {
			continue
		}
		if providers == nil {
			providers = s.modelProviders(ctx)
		}
		if !slices.ContainsFunc(providers, func(p ModelProvider) bool { return p.ID == b.Provider }) {
			return modelProfileSwitch{}, false, i18n.Err("assistant-model_profiles.err_launcher_missing", i18n.Args{"launcher": name})
		}
	}
	now := time.Now().UTC()
	sw := modelProfileSwitch{From: state.Active, To: prev.Profile}
	undo := &modelProfileImage{Profile: state.Active, At: now, Purposes: map[string]ModelBinding{}, Launchers: map[string]LauncherModels{}}
	writes := map[string]any{}
	for _, cat := range modelCategoryIDs {
		b, ok := prev.Purposes[cat]
		if !ok {
			continue
		}
		// Restore model choices while preserving the new conversations' profile selected since the switch.
		r, cached := records[cat]
		if !cached {
			r, err = s.modelRecord(ctx, cat)
			if err != nil {
				return sw, false, err
			}
			records[cat] = r
		}
		b.ConfigDir = r.Binding.ConfigDir
		liveNormalized := r.Binding
		if liveNormalized.Provider == "" && liveNormalized.Model != "" {
			if provider, err := s.resolveLegacyModelProvider(ctx, cat, nil, records, stored); err != nil {
				return sw, false, err
			} else if provider != "" {
				liveNormalized.Provider = provider
			}
		}
		if b == liveNormalized {
			continue
		}
		undo.Purposes[cat] = liveNormalized
		writes[modelRecordKey(cat)] = modelRecord{Revision: r.Revision + 1, Binding: b}
		sw.Changed = append(sw.Changed, cat)
	}
	changedLaunchers := false
	for _, name := range slices.Sorted(maps.Keys(prev.Launchers)) {
		li := slices.IndexFunc(stored, func(l storedLauncher) bool { return l.Name == name })
		if li < 0 || stored[li].channelAlias != "" {
			continue // gone, or now a channel's alias: its channel keeps its models
		}
		set := prev.Launchers[name]
		var before LauncherModels
		was := before.fields(&stored[li].ManagedLauncher)
		for k, f := range set.fields(&stored[li].ManagedLauncher) {
			if *f.set == nil || *f.launcher == **f.set {
				continue
			}
			*was[k].set = profileValue(*f.launcher)
			*f.launcher = **f.set
			sw.Changed = append(sw.Changed, name+"."+f.name)
			changedLaunchers = true
		}
		if before != (LauncherModels{}) {
			undo.Launchers[name] = before
			stored[li].Updated = now
		}
	}
	if changedLaunchers {
		writes[launchersManagedKey] = persistedLaunchers(stored)
	}
	if preview != nil {
		preview.display = modelProfileApprovalDisplay("rollback", rollbackEntryID(stack[idx]), sw, undo, prev.Purposes, stored)
		return sw, false, nil
	}
	if len(sw.Changed) == 0 && state.Active == prev.Profile {
		return sw, false, nil
	}
	state.Active, state.At, state.By = prev.Profile, now, by
	if remaining := slices.Clone(stack[:idx]); len(remaining) > 0 {
		state.setRollbacks(remaining)
	} else {
		undo.Redo = !prev.Redo
		undo.ID = state.nextRollbackID()
		state.setRollbacks([]modelProfileImage{*undo})
	}
	state.record(ModelProfileEvent{At: now, Action: "rollback", From: sw.From, To: sw.To, By: by, Changed: sw.Changed})
	writes[modelProfileStateKey] = state
	if err := s.putModelProfileSettings(ctx, writes); err != nil {
		return sw, false, err
	}
	return sw, changedLaunchers, nil
}

func modelProfileApprovalDisplay(action, rollbackID string, sw modelProfileSwitch, before *modelProfileImage, afterPurposes map[string]ModelBinding, stored []storedLauncher) string {
	changes := make([]modelProfileDisplayChange, 0, len(sw.Changed))
	for _, field := range sw.Changed {
		afterBinding, afterPurpose := afterPurposes[field]
		beforeBinding, beforePurpose := before.Purposes[field]
		if afterPurpose || beforePurpose {
			var a, b any
			if beforePurpose {
				beforeBinding.ConfigDir = ""
				a = beforeBinding
			}
			if afterPurpose {
				afterBinding.ConfigDir = ""
				b = afterBinding
			}
			changes = append(changes, modelProfileDisplayChange{Field: field, Before: a, After: b})
			continue
		}
		dot := strings.LastIndex(field, ".")
		if dot < 0 {
			continue
		}
		name, launcherField := field[:dot], field[dot+1:]
		var after LauncherModels
		if i := slices.IndexFunc(stored, func(l storedLauncher) bool { return l.Name == name }); i >= 0 {
			for _, f := range after.fields(&stored[i].ManagedLauncher) {
				*f.set = profileValue(*f.launcher)
			}
		}
		changes = append(changes, modelProfileDisplayChange{Field: field,
			Before: modelProfileLauncherValue(before.Launchers[name], launcherField),
			After:  modelProfileLauncherValue(after, launcherField)})
	}
	raw, _ := json.Marshal(struct {
		Action     string                      `json:"action"`
		From       string                      `json:"from"`
		To         string                      `json:"to"`
		RollbackID string                      `json:"rollback_id,omitempty"`
		Changes    []modelProfileDisplayChange `json:"changes"`
	}{Action: action, From: sw.From, To: sw.To, RollbackID: rollbackID, Changes: changes})
	return string(raw)
}

func modelProfileLauncherValue(models LauncherModels, field string) any {
	for _, f := range models.fields(nil) {
		if f.name == field && *f.set != nil {
			return **f.set
		}
	}
	return nil
}

func (s *Service) prepareModelProfileSwitch(ctx context.Context, id string) (modelProfileApproval, error) {
	var approval modelProfileApproval
	if strings.TrimSpace(id) == "" {
		return approval, fmt.Errorf("id is required; use model_profiles_list")
	}
	s.models.mu.Lock()
	_, _, err := s.modelProfileSwitchLocked(ctx, id, ProfileActor{Kind: "assistant"}, &approval)
	s.models.mu.Unlock()
	return approval, err
}

func (s *Service) prepareModelProfileRollback(ctx context.Context) (modelProfileApproval, error) {
	var approval modelProfileApproval
	s.models.mu.Lock()
	_, _, err := s.modelProfileRollbackLocked(ctx, "", ProfileActor{Kind: "assistant"}, &approval)
	s.models.mu.Unlock()
	return approval, err
}

func modelProfileApprovalMatches(fresh modelProfileApproval, item ownerActionItem) bool {
	return item.Revision != nil && fresh.revision == *item.Revision && item.Display != "" && fresh.display == item.Display
}

func (s *Service) applyApprovedModelProfileSwitch(ctx context.Context, id string, by ProfileActor, item ownerActionItem) (modelProfileSwitch, error) {
	s.models.mu.Lock()
	var fresh modelProfileApproval
	_, _, err := s.modelProfileSwitchLocked(ctx, id, by, &fresh)
	if err == nil && !modelProfileApprovalMatches(fresh, item) {
		err = fmt.Errorf("the model profile switch changed after the batch card was shown: %w", approvalops.ErrStale)
	}
	var sw modelProfileSwitch
	pushed := false
	if err == nil {
		sw, pushed, err = s.applyModelProfileLocked(ctx, id, by)
	}
	s.models.mu.Unlock()
	if err == nil && (sw.From != sw.To || len(sw.Changed) > 0) {
		s.modelProfileSwitched(ctx, sw, pushed)
	}
	return sw, err
}

func (s *Service) applyApprovedModelProfileRollback(ctx context.Context, by ProfileActor, item ownerActionItem) (modelProfileSwitch, error) {
	s.models.mu.Lock()
	var fresh modelProfileApproval
	_, _, err := s.modelProfileRollbackLocked(ctx, "", by, &fresh)
	if err == nil && !modelProfileApprovalMatches(fresh, item) {
		err = fmt.Errorf("the model profile rollback changed after the batch card was shown: %w", approvalops.ErrStale)
	}
	var sw modelProfileSwitch
	pushed := false
	if err == nil {
		sw, pushed, err = s.rollbackModelProfileLocked(ctx, "", by)
	}
	s.models.mu.Unlock()
	if err == nil && (sw.From != sw.To || len(sw.Changed) > 0) {
		s.modelProfileSwitched(ctx, sw, pushed)
	}
	return sw, err
}

// modelProfileSwitched tells everyone after a switch or rollback: the nodes get the launchers (when their fields
// changed), the next message to the assistant a new snapshot, the apps an event.
func (s *Service) modelProfileSwitched(ctx context.Context, sw modelProfileSwitch, launchers bool) {
	if launchers {
		s.pushManaged(context.WithoutCancel(ctx), "")
	}
	s.ProfileChanged(ctx)
	s.profileEvent(ctx, sw.To)
}

// profileEvent tells the apps the profiles changed (a switch, a rollback, an edit): they read them again.
func (s *Service) profileEvent(ctx context.Context, active string) {
	s.event(ctx, map[string]any{"type": "model_profile", "active": active})
}
