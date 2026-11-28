package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Profile tools expose the same operations as settings, without copying credentials into model context.
func (s *Service) toolModelProfile(ctx context.Context, machine, self, name string, args json.RawMessage) (string, error) {
	if name == "model_profile_capture" {
		return s.captureModelProfile(ctx, ToolCall{MachineID: machine, SessionID: self}, args)
	}
	if name == "model_profile_deactivate" || name == "model_profile_clear" {
		return s.toolModelProfileRemoval(ctx, ToolCall{MachineID: machine, SessionID: self}, name, args)
	}
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	by := ProfileActor{Kind: "assistant"}
	var execution *ownerActionExecution
	var err error
	if name == "model_profile_switch" || name == "model_profile_rollback" {
		execution, err = s.consumeOwnerBuiltin(ctx, machine, self, name, args)
		if err != nil {
			return "", err
		}
	}
	switch name {
	case "model_profile_switch":
		if a.ID == "" {
			if execution != nil {
				return s.finishOwnerBuiltin(ctx, *execution, "", fmt.Errorf("id is required; use model_profiles_list"))
			}
			return "", fmt.Errorf("id is required; use model_profiles_list")
		}
		if execution != nil {
			_, err = s.applyApprovedModelProfileSwitch(ctx, a.ID, by, execution.Item)
		} else {
			_, err = s.applyModelProfile(ctx, a.ID, by)
		}
	case "model_profile_rollback":
		if execution != nil {
			_, err = s.applyApprovedModelProfileRollback(ctx, by, execution.Item)
		} else {
			_, err = s.rollbackModelProfile(ctx, by)
		}
	}
	if err != nil {
		if execution != nil {
			return s.finishOwnerBuiltin(ctx, *execution, "", err)
		}
		return "", err
	}
	v, err := s.modelProfilesView(ctx)
	if err != nil {
		if execution != nil {
			return s.finishOwnerBuiltin(ctx, *execution, "", ownerActionCompletedError{err: err})
		}
		return "", err
	}
	var result any = v
	if name == "model_profile_get" {
		i := slices.IndexFunc(v.Profiles, func(p ModelProfileView) bool { return p.ID == a.ID })
		if i < 0 {
			return "", errModelProfileNotFound
		}
		result = v.Profiles[i]
	}
	raw, err := json.Marshal(result)
	if execution != nil {
		if err != nil {
			err = ownerActionCompletedError{err: err}
		}
		return s.finishOwnerBuiltin(ctx, *execution, string(raw), err)
	}
	return string(raw), err
}

// An exact batch grant and a standalone phone card both approve the same proposal.
func (s *Service) toolModelProfileRemoval(ctx context.Context, call ToolCall, tool string, args json.RawMessage) (string, error) {
	if err := decodeManagedProfileJSON(args, &struct{}{}); err != nil {
		return "", err
	}
	execution, err := s.consumeOwnerBuiltin(ctx, call.MachineID, call.SessionID, tool, args)
	if err != nil {
		return "", err
	}
	title := "assistant-model_profiles.title_deactivate"
	if tool == "model_profile_clear" {
		title = "assistant-model_profiles.title_clear"
	}
	setting := ownSetting{
		Setting:   Setting{Key: tool},
		toolName:  tool,
		cardTitle: func(l i18n.Lang) string { return i18n.T(l, title) },
		set: func(ctx context.Context, _ json.RawMessage) (*ownChange, error) {
			proposal, err := s.prepareModelProfileRemoval(ctx, tool)
			if err != nil {
				return nil, err
			}
			return &ownChange{
				applyChecksProposal: true,
				revision:            &proposal.revision,
				cardText:            func(i18n.Lang) string { return proposal.display },
				apply: func(ctx context.Context) (string, error) {
					return s.applyApprovedModelProfileRemoval(ctx, tool, proposal)
				},
			}, nil
		},
	}
	return s.ownSettingActionExact(ctx, call, setting, "set", args, execution)
}

func (s *Service) prepareModelProfileRemoval(ctx context.Context, tool string) (modelProfileApproval, error) {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	return s.modelProfileRemovalApprovalLocked(ctx, tool)
}

func (s *Service) modelProfileRemovalApprovalLocked(ctx context.Context, tool string) (modelProfileApproval, error) {
	ps, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return modelProfileApproval{}, err
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return modelProfileApproval{}, err
	}
	proposal := modelProfileApproval{revision: ps.Revision}
	lang := s.lang(ctx)
	switch {
	case tool == "model_profile_clear":
		names := make([]string, 0, len(ps.Profiles))
		for _, p := range ps.Profiles {
			names = append(names, p.Name)
		}
		saved := strings.Join(names, ", ")
		if saved == "" {
			saved = i18n.T(lang, "assistant-settings_tool.none")
		}
		proposal.display = i18n.T(lang, "assistant-model_profiles.confirm_clear",
			i18n.Args{"profiles": i18n.Verbatim(saved), "rollbacks": len(state.rollbackStack())})
	case state.Active == "":
		proposal.display = i18n.T(lang, "assistant-model_profiles.confirm_already_inactive")
	default:
		name := state.Active
		if i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == state.Active }); i >= 0 {
			name = fmt.Sprintf("%s (%s)", ps.Profiles[i].Name, state.Active)
		}
		proposal.display = i18n.T(lang, "assistant-model_profiles.confirm_deactivate",
			i18n.Args{"profile": i18n.Verbatim(name)})
	}
	return proposal, nil
}

func (s *Service) applyApprovedModelProfileRemoval(ctx context.Context, tool string, approved modelProfileApproval) (string, error) {
	s.models.mu.Lock()
	fresh, err := s.modelProfileRemovalApprovalLocked(ctx, tool)
	if err == nil && fresh != approved {
		err = approvalops.ErrStale
	}
	var changed bool
	if err == nil {
		by := ProfileActor{Kind: "assistant"}
		if tool == "model_profile_clear" {
			err = s.clearModelProfilesLocked(ctx, &approved.revision, by)
			changed = err == nil
		} else {
			changed, err = s.deactivateModelProfileLocked(ctx, by)
		}
	}
	s.models.mu.Unlock()
	if err != nil {
		return "", err
	}
	if changed {
		s.ProfileChanged(ctx)
		s.profileEvent(ctx, "")
	}
	v, err := s.modelProfilesView(ctx)
	if err != nil {
		return "", ownerActionCompletedError{err: err}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", ownerActionCompletedError{err: err}
	}
	return string(raw), nil
}

func (s *Service) applyStartTier(ctx context.Context, params map[string]any) error {
	if _, err := validateStartSpeed(params); err != nil {
		return err
	}
	tier, _ := params["tier"].(string)
	delete(params, "tier") // node runtimes need only the resolved fields
	if tier == "" {
		return nil
	}
	slot := tierStartOf(ctx)
	facts := tierFacts{}
	if slot != nil {
		facts.Machine, facts.Keep = slot.machine, slot.keep
	}
	pick, err := s.pickTierCandidate(ctx, tier, facts)
	if err != nil {
		return err
	}
	t := pick.Tier
	launcher, _ := params["launcher"].(string)
	model, _ := params["model"].(string)
	effort, _ := params["effort"].(string)
	speed, _ := params["speed"].(string)
	mode, _ := params["permission_mode"].(string)
	if layTier(t, &launcher, &model, &effort) {
		if speed == "" && (model == "" || model == t.Model) {
			speed = t.Speed
		}
		if mode == "" && t.PermissionMode != "" {
			mode = t.PermissionMode
		}
		if asked, _ := params["host"].(string); asked == "" && t.Host != nil {
			params["host"] = *t.Host
		}
		if slot != nil {
			slot.pick = &pick // the tier gave the start its launcher: its pick is the dispatch's
		}
	}
	params["launcher"], params["model"], params["effort"], params["speed"] = launcher, model, effort, speed
	if mode != "" {
		params["permission_mode"] = mode
	}
	return nil
}

// layTier lays tier t over a start's launcher, model and effort, and says whether it gave them anything: not when the
// start names a launcher of its own (withTier). An explicit different model does not inherit effort intended for the
// tier's model.
func layTier(t ModelTier, launcher, model, effort *string) bool {
	if *launcher != "" && *launcher != t.Launcher {
		return false
	}
	if *model != "" && *model != t.Model {
		t.Effort = ""
	}
	withTier(t, launcher, model, effort)
	return true
}

func (s *Service) applyTaskTier(ctx context.Context, t *PlanTask) error {
	ctx, _ = withTierStart(ctx, t.Machine) // the preview picks the candidate the start on t.Machine would
	p := map[string]any{"tier": t.Tier, "launcher": t.Launcher, "model": t.Model, "effort": t.Effort, "speed": t.Speed, "permission_mode": t.Permission}
	if err := s.applyStartTier(ctx, p); err != nil {
		return err
	}
	t.Launcher, _ = p["launcher"].(string)
	t.Model, _ = p["model"].(string)
	t.Effort, _ = p["effort"].(string)
	t.Speed, _ = p["speed"].(string)
	if mode, _ := p["permission_mode"].(string); mode != "" && !t.Readonly {
		t.Permission = mode
	}
	return nil
}

// modelProfileContext is the snapshot's model profile line: read outside the ordinary source cache so every turn
// observes a switch, including a rollback to no profile, and a tier edited in the profile in use.
func (s *Service) modelProfileContext(ctx context.Context) *prompts.SnapshotProfile {
	s.models.mu.Lock() // the profiles and the state of one switch, not one from either side of it
	p, active, err := s.activeModelProfile(ctx)
	s.models.mu.Unlock()
	switch {
	case err != nil:
		return &prompts.SnapshotProfile{Unreadable: true}
	case p == nil && active != "":
		return &prompts.SnapshotProfile{ID: active, Gone: true}
	case p == nil:
		return nil
	}
	out := &prompts.SnapshotProfile{ID: p.ID, Name: p.Name}
	for _, name := range slices.Sorted(maps.Keys(p.Tiers)) {
		t := p.Tiers[name]
		// The first candidate, with how many others follow it: the whole list would grow every turn's snapshot.
		out.Tiers = append(out.Tiers, prompts.SnapshotTier{Name: name, Launcher: t.Launcher, Model: t.Model, Effort: t.Effort, Speed: t.Speed,
			PermissionMode: t.PermissionMode, Description: t.Description, More: len(t.list()) - 1, Aliases: t.Aliases})
	}
	return out
}

// activeModelProfile is the profile in use and its id: nil with "" when none is,
// nil with its id when it is gone (a rollback to a profile deleted since).
func (s *Service) activeModelProfile(ctx context.Context) (*ModelProfile, string, error) {
	ps, _, err := s.readModelProfiles(ctx)
	if err != nil {
		return nil, "", err
	}
	st, err := s.readModelProfileState(ctx)
	if err != nil {
		return nil, "", err
	}
	i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == st.Active })
	if st.Active == "" || i < 0 {
		return nil, st.Active, nil
	}
	return &ps.Profiles[i], st.Active, nil
}
