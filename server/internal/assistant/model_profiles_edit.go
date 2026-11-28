package assistant

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- checking a profile ----

func validProfileValue(v string) bool {
	return v == strings.TrimSpace(v) && !strings.ContainsAny(v, "\r\n\t\x00") && len(v) <= modelProfileValue
}

// checkModelProfile is what saving p needs: its id, name and the form of what it sets. Whether the providers and
// models can run is checked when it is switched to (applyModelProfile), against the configuration of that moment.
func checkModelProfile(p *ModelProfile, stored []storedLauncher) error {
	p.Name, p.Description = strings.TrimSpace(p.Name), strings.TrimSpace(p.Description)
	if !modelProfileID.MatchString(p.ID) || p.ID == modelProfileRolling {
		return i18n.Err("assistant-model_profiles.err_id")
	}
	if p.Name == "" || utf8.RuneCountInString(p.Name) > modelProfileName || strings.ContainsAny(p.Name, "\r\n\x00") {
		return i18n.Err("assistant-model_profiles.err_name", i18n.Args{"max": modelProfileName})
	}
	if utf8.RuneCountInString(p.Description) > modelProfileAbout || strings.ContainsRune(p.Description, 0) {
		return i18n.Err("assistant-model_profiles.err_description", i18n.Args{"max": modelProfileAbout})
	}
	for id, b := range p.Purposes {
		if err := checkExternalPurpose(b); err != nil {
			return err
		}
		if !slices.Contains(modelCategoryIDs, id) {
			return i18n.Err("assistant-model_profiles.err_purpose", i18n.Args{"purpose": id, "purposes": strings.Join(modelCategoryIDs, ", ")})
		}
		if b.ConfigDir != "" {
			return i18n.Err("assistant-model_profiles.err_config_dir")
		}
		for field, v := range map[string]string{"provider": b.Provider, "model": b.Model} {
			if !validProfileValue(v) {
				return i18n.Err("assistant-model_profiles.err_value", i18n.Args{"where": id, "field": field})
			}
		}
		kind := ""
		for _, purpose := range modelPurposes {
			if purpose.ID == id {
				kind = purpose.Kind
				break
			}
		}
		if b.Effort != "" && !slices.Contains(effortLevelsForKind(kind), b.Effort) {
			return profileEffortErr(id, b.Effort, effortLevelsForKind(kind))
		}
		if b.PermissionMode != "" {
			if kind != "launcher" {
				return i18n.Err("assistant-models.err_permission_mode_launcher_only")
			}
			if !slices.Contains(proto.PermissionModes, b.PermissionMode) {
				return profilePermissionModeErr(id, b.PermissionMode)
			}
			if name, ok := strings.CutPrefix(b.Provider, "launcher."); ok {
				agent := ""
				for _, l := range stored {
					if l.Name == name {
						agent = l.AgentType()
						break
					}
				}
				if agent == "" && slices.Contains([]string{"codex", "traex", "agy", "omp", "pi", "cursor"}, name) {
					agent = name
				}
				if !permissionModeSupported(agent, 0, b.PermissionMode) {
					return i18n.Err("assistant-models.err_permission_mode_unsupported")
				}
			}
		}
	}
	for name, m := range p.Launchers {
		if proto.IsExternalLauncher(name) {
			return i18n.Err("external-adapter.purpose_unsupported")
		}
		i := slices.IndexFunc(stored, func(l storedLauncher) bool { return l.Name == name })
		if i < 0 {
			return i18n.Err("assistant-model_profiles.err_launcher_missing", i18n.Args{"launcher": name})
		}
		empty := true
		for _, f := range m.fields(nil) {
			v := *f.set
			if v == nil {
				continue
			}
			empty = false
			if !validProfileValue(*v) {
				return i18n.Err("assistant-model_profiles.err_value", i18n.Args{"where": name, "field": f.name})
			}
			if f.name == "model" && *v == "" {
				return i18n.Err("assistant-model_profiles.err_launcher_model", i18n.Args{"launcher": name})
			}
			if f.name == "effort" && *v != "" && !slices.Contains(proto.EffortLevels, *v) {
				return profileEffortErr(name, *v, proto.EffortLevels)
			}
			if stored[i].Kind == "api" && f.name != "model" && *v != "" {
				return i18n.Err("assistant-model_profiles.err_launcher_api", i18n.Args{"launcher": name, "field": f.name})
			}
		}
		if empty {
			delete(p.Launchers, name)
		}
	}
	if err := checkModelTiers(p.Tiers); err != nil {
		return err
	}
	if len(p.Purposes) == 0 {
		p.Purposes = nil
	}
	if len(p.Launchers) == 0 {
		p.Launchers = nil
	}
	if len(p.Tiers) == 0 {
		p.Tiers = nil
	}
	return nil
}

// checkModelTiers is what saving tiers needs (normalized in place): names, at most maxTierCandidates candidates each,
// a launcher per candidate, model ids, effort levels, speeds, permission modes, one-line descriptions, and aliases
// that are tier names no other tier has or answers to. Whether a launcher exists is the editor's to check
// (settings_tiers.go): a ~/.zshrc one is known only to its computers.
func checkModelTiers(tiers map[string]ModelTier) error {
	if len(tiers) > maxModelTiers {
		return i18n.Err("assistant-model_profiles.err_tiers", i18n.Args{"max": maxModelTiers})
	}
	for name, t := range tiers {
		if !modelTierName.MatchString(name) {
			return i18n.Err("assistant-model_profiles.err_tier_name", i18n.Args{"tier": name})
		}
		if len(t.Candidates) > maxTierCandidates {
			return i18n.Err("assistant-model_profiles.err_tier_candidates", i18n.Args{"tier": name, "max": maxTierCandidates})
		}
		t.normalize()
		if checkStartHostName(t.startHost()) != nil {
			return i18n.Err("assistant-model_profiles.err_value", i18n.Args{"where": name, "field": "host"})
		}
		for i, c := range t.list() {
			if err := checkTierCandidate(candidateWhere(name, i), c); err != nil {
				return err
			}
		}
		if t.Description = strings.TrimSpace(t.Description); utf8.RuneCountInString(t.Description) > modelTierAbout ||
			strings.ContainsAny(t.Description, "\r\n\x00") {
			return i18n.Err("assistant-model_profiles.err_tier_description", i18n.Args{"tier": name, "max": modelTierAbout})
		}
		tiers[name] = t
	}
	// Aliases resolve a name to one tier only: none is a tier's name, and none is two tiers' alias.
	owner := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(tiers)) {
		t := tiers[name]
		if len(t.Aliases) > maxTierAliases {
			return i18n.Err("assistant-model_profiles.err_tier_alias", i18n.Args{"tier": name, "alias": t.Aliases[maxTierAliases]})
		}
		for _, a := range t.Aliases {
			_, isTier := tiers[a]
			if _, taken := owner[a]; taken || isTier || !modelTierName.MatchString(a) {
				return i18n.Err("assistant-model_profiles.err_tier_alias", i18n.Args{"tier": name, "alias": a})
			}
			owner[a] = name
		}
	}
	return nil
}

// candidateWhere names candidate i of tier name in a message: the tier itself for candidate 0 (as before candidates),
// "name #2" for the second.
func candidateWhere(name string, i int) string {
	if i == 0 {
		return name
	}
	return name + " #" + strconv.Itoa(i+1)
}

// checkTierCandidate is what one candidate of a tier needs: a launcher Persona can start an Agent on, and the form of
// its model, effort, speed and permission mode.
func checkTierCandidate(where string, c TierCandidate) error {
	if proto.IsExternalLauncher(c.Launcher) {
		return i18n.Err("external-adapter.purpose_unsupported")
	}
	if c.Launcher == "" {
		return i18n.Err("assistant-model_profiles.err_tier_launcher", i18n.Args{"tier": where})
	}
	for field, v := range map[string]string{"launcher": c.Launcher, "model": c.Model} {
		if !validProfileValue(v) || strings.Contains(v, " ") {
			return i18n.Err("assistant-model_profiles.err_value", i18n.Args{"where": where, "field": field})
		}
	}
	if c.Effort != "" && !slices.Contains(proto.EffortLevels, c.Effort) {
		return profileEffortErr(where, c.Effort, proto.EffortLevels)
	}
	if !proto.ValidSpeed(c.Speed) {
		return i18n.Err("assistant-model_profiles.err_tier_speed", i18n.Args{"tier": where, "speed": c.Speed})
	}
	if c.PermissionMode != "" {
		if !slices.Contains(proto.PermissionModes, c.PermissionMode) {
			return profilePermissionModeErr(where, c.PermissionMode)
		}
		if slices.Contains([]string{"omp", "traex", "pi", "cursor"}, c.Launcher) {
			return i18n.Err("assistant-models.err_permission_mode_unsupported")
		}
	}
	return nil
}

// checkTierModel is what a tier added or changed needs of its models: each goes to the agent as --model, so one that
// is no model id would fail every start on it (#1644). Only the candidates added or changed since was (nil: a new
// tier) are held to it: a tier saved before this was checked is left as it is, so a switch or an edit of something
// else never stops on it (a start on one says why).
func checkTierModel(name string, t ModelTier, was *ModelTier) error {
	var before []TierCandidate
	if was != nil {
		before = was.list()
	}
	for i, c := range t.list() {
		if i < len(before) && before[i].Model == c.Model {
			continue
		}
		if c.Model != "" && !proto.ValidModelID(c.Model) {
			return i18n.Err("assistant-model_profiles.err_tier_model", i18n.Args{"tier": candidateWhere(name, i), "model": c.Model})
		}
	}
	return nil
}

func profileEffortErr(where, effort string, levels []string) error {
	return i18n.Err("assistant-model_profiles.err_effort", i18n.Args{"where": where, "effort": effort, "levels": strings.Join(levels, " / ")})
}

func profilePermissionModeErr(where, mode string) error {
	return i18n.Err("assistant-model_profiles.err_permission_mode", i18n.Args{"where": where, "mode": mode, "modes": strings.Join(proto.PermissionModes, " / ")})
}

func (s *Service) checkProfilePermissionModes(ctx context.Context, p ModelProfile) error {
	for id, b := range p.Purposes {
		if b.PermissionMode == "" {
			continue
		}
		provider := b.Provider
		if provider == "" {
			if eff, err := s.resolveModel(ctx, id); err == nil {
				provider = eff.Provider
			}
		}
		name, ok := strings.CutPrefix(provider, "launcher.")
		if !ok || name == "" {
			continue
		}
		machine := ""
		if id == "assistant" {
			machine = s.settings(ctx).MachineID
		}
		if !slices.Contains(s.permissionModesForLauncher(ctx, machine, name, nil), b.PermissionMode) {
			return i18n.Err("assistant-models.err_permission_mode_unsupported")
		}
	}
	for _, t := range p.Tiers {
		for _, c := range t.list() {
			if c.PermissionMode == "" {
				continue
			}
			if !slices.Contains(s.permissionModesForLauncher(ctx, "", c.Launcher, nil), c.PermissionMode) {
				return i18n.Err("assistant-models.err_permission_mode_unsupported")
			}
		}
	}
	return nil
}

// checkModelProfileForSave normalizes and validates the exact profile a save or approval card will store. saved tiers
// are the pre-edit tiers: only added or changed tier models are held to the newer model-ID check.
func (s *Service) checkModelProfileForSave(ctx context.Context, p *ModelProfile, saved map[string]ModelTier) error {
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return err
	}
	ctx = withManagedSnapshot(ctx, stored)
	if err := s.projectTiers(ctx, p.Tiers); err != nil {
		return err
	}
	if p.Purposes, err = s.normalizeProfilePurposes(ctx, p.Purposes, map[string]modelRecord{}, stored); err != nil {
		return err
	}
	if err := checkModelProfile(p, stored); err != nil {
		return err
	}
	if err := s.checkProfilePermissionModes(ctx, *p); err != nil {
		return err
	}
	for name, tier := range p.Tiers {
		var before *ModelTier
		if was, ok := saved[name]; ok {
			before = &was
		}
		if err := checkTierModel(name, tier, before); err != nil {
			return err
		}
	}
	return nil
}

// ---- editing ----

// saveModelProfile changes (or makes) profile id with edit, at the profiles' revision when one is given; the history
// says who. Saving the profile in use does not switch to it again: its changes run after the next switch (its tiers
// at the next start on one, as they are read then).
func (s *Service) saveModelProfile(ctx context.Context, revision *int64, id string, by ProfileActor, edit func(p *ModelProfile, exists bool) error) (ModelProfile, error) {
	s.models.mu.Lock()
	p, active, err := s.saveModelProfileLocked(ctx, revision, id, by, edit)
	s.models.mu.Unlock()
	if err == nil {
		s.profileEvent(ctx, active)
	}
	return p, err
}

func (s *Service) saveModelProfileLocked(ctx context.Context, revision *int64, id string, by ProfileActor, edit func(p *ModelProfile, exists bool) error) (ModelProfile, string, error) {
	ps, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return ModelProfile{}, "", err
	}
	if revision != nil && *revision != ps.Revision {
		return ModelProfile{}, "", errModelProfileConflict
	}
	i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == id })
	p := ModelProfile{ID: id}
	before := ModelProfile{}
	if i >= 0 {
		before = cloneModelProfile(ps.Profiles[i])
		p = cloneModelProfile(before)
	} else if len(ps.Profiles) >= maxModelProfiles {
		return p, "", i18n.Err("assistant-model_profiles.err_profiles", i18n.Args{"max": maxModelProfiles})
	}
	if err := edit(&p, i >= 0); err != nil {
		return p, "", err
	}
	p.ID = id
	if err := s.checkModelProfileForSave(ctx, &p, before.Tiers); err != nil {
		return p, "", err
	}
	changed := modelProfileChangedFields(before, p)
	now := time.Now().UTC()
	p.UpdatedAt = now
	if i >= 0 {
		ps.Profiles[i] = p
	} else {
		ps.Profiles = append(ps.Profiles, p)
	}
	ps.Revision++
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return p, "", err
	}
	state.record(ModelProfileEvent{At: now, Action: "save", To: id, By: by, Changed: changed})
	if err := s.putModelProfileSettings(ctx, map[string]any{modelProfilesKey: ps, modelProfileStateKey: state}); err != nil {
		return p, "", err
	}
	s.ProfileChanged(ctx)
	return p, state.Active, nil
}

// deleteModelProfile removes a saved profile and clears its active designation if in use.
func (s *Service) deleteModelProfile(ctx context.Context, revision *int64, id string, by ProfileActor) error {
	s.models.mu.Lock()
	active, err := s.deleteModelProfileLocked(ctx, revision, id, by)
	s.models.mu.Unlock()
	if err == nil {
		s.profileEvent(ctx, active)
	}
	return err
}

func (s *Service) deleteModelProfileLocked(ctx context.Context, revision *int64, id string, by ProfileActor) (string, error) {
	ps, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return "", err
	}
	if revision != nil && *revision != ps.Revision {
		return "", errModelProfileConflict
	}
	i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == id })
	if i < 0 {
		return "", errModelProfileNotFound
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return "", err
	}
	if state.Active == id {
		state.clearActive()
	}
	ps.Profiles = slices.Delete(ps.Profiles, i, i+1)
	ps.Revision++
	state.record(ModelProfileEvent{At: time.Now().UTC(), Action: "delete", To: id, By: by})
	if err := s.putModelProfileSettings(ctx, map[string]any{modelProfilesKey: ps, modelProfileStateKey: state}); err != nil {
		return "", err
	}
	s.ProfileChanged(ctx)
	return state.Active, nil
}

func (s *Service) deactivateModelProfile(ctx context.Context, by ProfileActor) error {
	s.models.mu.Lock()
	changed, err := s.deactivateModelProfileLocked(ctx, by)
	s.models.mu.Unlock()
	if err == nil && changed {
		s.ProfileChanged(ctx)
		s.profileEvent(ctx, "")
	}
	return err
}

func (s *Service) deactivateModelProfileLocked(ctx context.Context, by ProfileActor) (bool, error) {
	state, err := s.readModelProfileState(ctx)
	if err != nil || state.Active == "" {
		return false, err
	}
	state.record(ModelProfileEvent{At: time.Now().UTC(), Action: "deactivate", From: state.Active, By: by})
	state.clearActive()
	if err := s.putModelProfileSettings(ctx, map[string]any{modelProfileStateKey: state}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) clearModelProfiles(ctx context.Context, revision *int64, by ProfileActor) error {
	s.models.mu.Lock()
	err := s.clearModelProfilesLocked(ctx, revision, by)
	s.models.mu.Unlock()
	if err == nil {
		s.ProfileChanged(ctx)
		s.profileEvent(ctx, "")
	}
	return err
}

func (s *Service) clearModelProfilesLocked(ctx context.Context, revision *int64, by ProfileActor) error {
	ps, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return err
	}
	if revision != nil && *revision != ps.Revision {
		return errModelProfileConflict
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return err
	}
	state.record(ModelProfileEvent{At: time.Now().UTC(), Action: "clear", From: state.Active, By: by})
	state.clearActive()
	state.setRollbacks(nil)
	catalog := modelProfiles{Revision: ps.Revision + 1, Profiles: []ModelProfile{}}
	return s.putModelProfileSettings(ctx, map[string]any{modelProfilesKey: catalog, modelProfileStateKey: state})
}
