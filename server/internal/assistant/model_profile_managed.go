package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const modelProfileBindingsKey = "model_profiles.bindings"

// modelProfileBindingsInput is persona_settings model_profiles.bindings' set value. Each purpose replaces that whole
// binding; null or {} restores inheritance on the next switch. Launcher entries patch individual fields: omitted keeps
// the profile's field, while null or "" clears it to the launcher's default on the next switch (the main model cannot
// be cleared). The catalog revision is captured while preparing the card and checked again before anything is written.
type modelProfileBindingsInput struct {
	Profile   string                     `json:"profile"`
	Purposes  map[string]json.RawMessage `json:"purposes"`
	Launchers map[string]json.RawMessage `json:"launchers"`
}

type modelProfilePurposeInput struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	PermissionMode string `json:"permission_mode"`
}

// modelProfileProposal is the exact before/after image shown on the phone. Its apply neither recaptures nor rebases:
// the catalog revision and the complete target profile must still match, or nothing is written.
type modelProfileProposal struct {
	id       string
	revision int64
	before   ModelProfile
	after    ModelProfile
	changed  []string
	capture  bool
}

func decodeManagedProfileJSON(raw json.RawMessage, dst any) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return err
	}
	if top == nil {
		return errors.New("value must be a JSON object")
	}
	if err := checkForbiddenProfilePayload(top); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func parseManagedPurpose(raw json.RawMessage) (ModelBinding, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ModelBinding{}, nil
	}
	var in modelProfilePurposeInput
	if err := decodeManagedProfileJSON(raw, &in); err != nil {
		return ModelBinding{}, err
	}
	if strings.Contains(in.Provider, " ") || in.Model != "" && !proto.ValidModelID(in.Model) {
		return ModelBinding{}, errors.New("purpose provider or model is not a valid identifier")
	}
	return ModelBinding{Provider: in.Provider, Model: in.Model, Effort: in.Effort, PermissionMode: in.PermissionMode}, nil
}

func parseManagedLauncher(raw json.RawMessage) (LauncherModels, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return LauncherModels{}, err
	}
	if fields == nil {
		return LauncherModels{}, errors.New("a launcher patch must be an object of model fields")
	}
	if err := checkForbiddenObjectKeys(fields); err != nil {
		return LauncherModels{}, err
	}
	var out LauncherModels
	known := out.fields(nil)
	for name, rawValue := range fields {
		i := slices.IndexFunc(known, func(f launcherField) bool { return f.name == name })
		if i < 0 {
			return LauncherModels{}, fmt.Errorf("unknown launcher model field %q", name)
		}
		if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
			*known[i].set = profileValue("")
			continue
		}
		var value string
		if err := json.Unmarshal(rawValue, &value); err != nil {
			return LauncherModels{}, fmt.Errorf("launcher field %s must be a string or null: %w", name, err)
		}
		if name != "effort" && value != "" && !proto.ValidModelID(value) {
			return LauncherModels{}, fmt.Errorf("launcher field %s has an invalid model identifier", name)
		}
		*known[i].set = profileValue(value)
	}
	return out, nil
}

// prepareModelProfileProposal reads and validates one target under the profile lock, but performs no write.
func (s *Service) prepareModelProfileProposal(ctx context.Context, id string, capture bool, edit func(*ModelProfile) error) (*modelProfileProposal, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("profile id is required; use model_profiles_list")
	}
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	ps, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return nil, err
	}
	if len(ps.Profiles) == 0 {
		return nil, errModelProfileEmpty
	}
	i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == id })
	if i < 0 {
		return nil, errModelProfileNotFound
	}
	before := cloneModelProfile(ps.Profiles[i])
	after := cloneModelProfile(before)
	if capture {
		purposes, launchers, _, err := s.captureModels(ctx)
		if err != nil {
			return nil, err
		}
		after.Purposes, after.Launchers = purposes, launchers
	} else if edit != nil {
		if err := edit(&after); err != nil {
			return nil, err
		}
	}
	if err := s.checkModelProfileForSave(ctx, &after, before.Tiers); err != nil {
		return nil, err
	}
	return &modelProfileProposal{id: id, revision: ps.Revision, before: before, after: after,
		changed: modelProfileChangedFields(before, after), capture: capture}, nil
}

func (s *Service) setModelProfileBindings(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var in modelProfileBindingsInput
	if err := decodeManagedProfileJSON(raw, &in); err != nil {
		return nil, err
	}
	if in.Purposes == nil && in.Launchers == nil {
		return nil, errors.New("give purposes and/or launchers to patch")
	}
	purposes := make(map[string]ModelBinding, len(in.Purposes))
	for id, value := range in.Purposes {
		binding, err := parseManagedPurpose(value)
		if err != nil {
			return nil, fmt.Errorf("purpose %s: %w", id, err)
		}
		purposes[id] = binding
	}
	launchers := make(map[string]LauncherModels, len(in.Launchers))
	for name, value := range in.Launchers {
		fields, err := parseManagedLauncher(value)
		if err != nil {
			return nil, fmt.Errorf("launcher %s: %w", name, err)
		}
		launchers[name] = fields
	}
	proposal, err := s.prepareModelProfileProposal(ctx, in.Profile, false, func(p *ModelProfile) error {
		if purposes != nil {
			p.Purposes = maps.Clone(p.Purposes)
			if p.Purposes == nil {
				p.Purposes = map[string]ModelBinding{}
			}
			for _, id := range slices.Sorted(maps.Keys(purposes)) {
				p.Purposes[id] = purposes[id]
			}
		}
		if launchers != nil {
			current := make(map[string]LauncherModels, len(p.Launchers))
			for name, fields := range p.Launchers {
				current[name] = cloneLauncherModels(fields)
			}
			for _, name := range slices.Sorted(maps.Keys(launchers)) {
				fields := cloneLauncherModels(current[name])
				patchModel := launchers[name]
				patch := patchModel.fields(nil)
				for i, f := range fields.fields(nil) {
					if *patch[i].set != nil {
						*f.set = profileValue(**patch[i].set)
					}
				}
				current[name] = fields
			}
			p.Launchers = current
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.modelProfileOwnChange(proposal, s.lang(ctx)), nil
}

func (s *Service) captureModelProfile(ctx context.Context, call ToolCall, raw json.RawMessage) (string, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := decodeManagedProfileJSON(raw, &in); err != nil {
		return "", err
	}
	setting := ownSetting{Setting: Setting{Key: "model_profile_capture", Title: "Model profile capture"}, toolName: "model_profile_capture",
		cardTitle: func(l i18n.Lang) string { return i18n.T(l, "assistant-settings_tool.title_model_capture") },
		set: func(ctx context.Context, _ json.RawMessage) (*ownChange, error) {
			proposal, err := s.prepareModelProfileProposal(ctx, in.ID, true, nil)
			if err != nil {
				return nil, err
			}
			return s.modelProfileOwnChange(proposal, s.lang(ctx)), nil
		}}
	return s.ownSettingAction(ctx, call, setting, "set", raw)
}

func (s *Service) modelProfileOwnChange(p *modelProfileProposal, lang i18n.Lang) *ownChange {
	if len(p.changed) == 0 {
		return &ownChange{summary: i18n.T(lang, "assistant-settings_tool.model_profile_unchanged", i18n.Args{"profile": p.id})}
	}
	return &ownChange{applyChecksProposal: true, revision: &p.revision, modelProfileWrite: true,
		profileAfter: map[string]ModelProfile{p.id: cloneModelProfile(p.after)}, cardText: func(l i18n.Lang) string {
			lines := make([]string, 0, len(p.changed)+2)
			for _, field := range p.changed {
				lines = append(lines, modelProfileCardChange(l, p.id, field, p.before, p.after))
			}
			if p.capture {
				lines = append(lines, i18n.T(l, "assistant-settings_tool.model_profile_capture_note"))
			}
			lines = append(lines, i18n.T(l, "assistant-settings_tool.model_profile_save_only"))
			return strings.Join(lines, "\n")
		}, apply: func(ctx context.Context) (string, error) { return s.applyModelProfileProposal(ctx, p) }}
}

func modelProfileSaveImageEqual(a, b ModelProfile) bool {
	// A planned batch edit and its approved save get their own timestamps.
	a.UpdatedAt = b.UpdatedAt
	return reflect.DeepEqual(a, b)
}

func (s *Service) applyModelProfileProposal(ctx context.Context, p *modelProfileProposal) (string, error) {
	_, err := s.saveModelProfile(ctx, &p.revision, p.id, ProfileActor{Kind: "assistant"}, func(current *ModelProfile, exists bool) error {
		if !exists || !modelProfileSaveImageEqual(*current, p.before) {
			return fmt.Errorf("the model profile changed after the card was shown, so nothing was saved: %w", approvalops.ErrStale)
		}
		*current = cloneModelProfile(p.after)
		return nil
	})
	if errors.Is(err, errModelProfileConflict) {
		err = fmt.Errorf("the model profiles changed after the card was shown, so nothing was saved: %w", approvalops.ErrStale)
	}
	if err != nil {
		return "", err
	}
	// A successful save is followed by an independent read-back. Any failure
	// below is after the commit: exact batches record the item as completed and
	// stop later items rather than falsely saying the write did not happen.
	view, err := s.modelProfilesView(ctx)
	if err != nil {
		return "", ownerActionCompletedError{err: err}
	}
	i := slices.IndexFunc(view.Profiles, func(candidate ModelProfileView) bool { return candidate.ID == p.id })
	if i < 0 {
		return "", ownerActionCompletedError{err: errors.New("the saved model profile was not present on read-back")}
	}
	saved := view.Profiles[i]
	if changed := modelProfileChangedFields(p.after, saved.ModelProfile); len(changed) != 0 {
		return "", ownerActionCompletedError{err: fmt.Errorf("saved model profile read-back differs in %s", strings.Join(changed, ", "))}
	}
	raw, err := json.Marshal(struct {
		Revision int64            `json:"revision"`
		Profile  ModelProfileView `json:"profile"`
		Note     string           `json:"note"`
	}{Revision: view.Revision, Profile: saved, Note: i18n.T(s.lang(ctx), "assistant-settings_tool.model_profile_save_only")})
	return string(raw), err
}

// modelProfileChangedFields is the save audit's ground truth. It deliberately excludes UpdatedAt and describes the
// stored value, not ModelProfileView.Changes (which is drift from the current configuration).
func modelProfileChangedFields(before, after ModelProfile) []string {
	var changed []string
	if before.Name != after.Name {
		changed = append(changed, "name")
	}
	if before.Description != after.Description {
		changed = append(changed, "description")
	}
	purposeIDs := map[string]bool{}
	for id := range before.Purposes {
		purposeIDs[id] = true
	}
	for id := range after.Purposes {
		purposeIDs[id] = true
	}
	for _, id := range slices.Sorted(maps.Keys(purposeIDs)) {
		a, aok := before.Purposes[id]
		b, bok := after.Purposes[id]
		if aok != bok || a != b {
			changed = append(changed, "purposes."+id)
		}
	}
	launcherNames := map[string]bool{}
	for name := range before.Launchers {
		launcherNames[name] = true
	}
	for name := range after.Launchers {
		launcherNames[name] = true
	}
	for _, name := range slices.Sorted(maps.Keys(launcherNames)) {
		a, aok := before.Launchers[name]
		b, bok := after.Launchers[name]
		fieldChanged := false
		af, bf := a.fields(nil), b.fields(nil)
		for i := range af {
			av, bv := *af[i].set, *bf[i].set
			if (av == nil) != (bv == nil) || av != nil && *av != *bv {
				changed = append(changed, "launchers."+name+"."+af[i].name)
				fieldChanged = true
			}
		}
		if aok != bok && !fieldChanged {
			changed = append(changed, "launchers."+name)
		}
	}
	tierNames := map[string]bool{}
	for name := range before.Tiers {
		tierNames[name] = true
	}
	for name := range after.Tiers {
		tierNames[name] = true
	}
	for _, name := range slices.Sorted(maps.Keys(tierNames)) {
		a, aok := before.Tiers[name]
		b, bok := after.Tiers[name]
		if aok != bok || !a.equal(b) {
			changed = append(changed, "tiers."+name)
		}
	}
	return changed
}

func modelProfileCardChange(l i18n.Lang, id, field string, before, after ModelProfile) string {
	beforeText, afterText := "(unknown)", "(unknown)"
	switch {
	case strings.HasPrefix(field, "purposes."):
		purpose := strings.TrimPrefix(field, "purposes.")
		beforeText, afterText = bindingCardText(l, before.Purposes, purpose), bindingCardText(l, after.Purposes, purpose)
	case strings.HasPrefix(field, "launchers."):
		remainder := strings.TrimPrefix(field, "launchers.")
		dot := strings.LastIndex(remainder, ".")
		if dot > 0 {
			name, nameField := remainder[:dot], remainder[dot+1:]
			beforeText = launcherCardText(l, before.Launchers, name, nameField)
			afterText = launcherCardText(l, after.Launchers, name, nameField)
		}
	case strings.HasPrefix(field, "tiers."):
		name := strings.TrimPrefix(field, "tiers.")
		a, aok := before.Tiers[name]
		b, bok := after.Tiers[name]
		beforeText, afterText = tierCardText(l, mapTier(a, aok)), tierCardText(l, mapTier(b, bok))
	case field == "name":
		beforeText, afterText = before.Name, after.Name
	case field == "description":
		beforeText, afterText = before.Description, after.Description
	}
	return i18n.T(l, "assistant-settings_tool.change_model_binding", i18n.Args{"profile": id, "field": field, "before": beforeText, "after": afterText})
}

func mapTier(t ModelTier, ok bool) *ModelTier {
	if !ok {
		return nil
	}
	return &t
}

func bindingCardText(l i18n.Lang, bindings map[string]ModelBinding, id string) string {
	b, ok := bindings[id]
	if !ok {
		return i18n.T(l, "assistant-settings_tool.none")
	}
	if b == (ModelBinding{}) {
		return i18n.T(l, "assistant-settings_tool.model_binding_default")
	}
	raw, _ := json.Marshal(b)
	return string(raw)
}

func launcherCardText(l i18n.Lang, launchers map[string]LauncherModels, name, field string) string {
	m, ok := launchers[name]
	if !ok {
		return i18n.T(l, "assistant-settings_tool.none")
	}
	for _, f := range m.fields(nil) {
		if f.name != field {
			continue
		}
		if *f.set == nil {
			return i18n.T(l, "assistant-settings_tool.none")
		}
		if **f.set == "" {
			return i18n.T(l, "assistant-settings_tool.model_launcher_default")
		}
		return **f.set
	}
	return i18n.T(l, "assistant-settings_tool.none")
}

func (s *Service) getModelProfileBindings(ctx context.Context) (string, error) {
	view, err := s.modelProfilesView(ctx)
	if err != nil {
		return "", err
	}
	if len(view.Profiles) == 0 {
		return "", errModelProfileEmpty
	}
	type bindings struct {
		ID        string                    `json:"id"`
		Name      string                    `json:"name"`
		Active    bool                      `json:"active,omitempty"`
		Purposes  map[string]ModelBinding   `json:"purposes,omitempty"`
		Launchers map[string]LauncherModels `json:"launchers,omitempty"`
	}
	out := struct {
		Revision int64      `json:"revision"`
		Profiles []bindings `json:"profiles"`
	}{Revision: view.Revision, Profiles: []bindings{}}
	for _, p := range view.Profiles {
		out.Profiles = append(out.Profiles, bindings{ID: p.ID, Name: p.Name, Active: p.Active, Purposes: p.Purposes, Launchers: p.Launchers})
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}
