package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// persona_settings model_profiles.tiers (docs/assistant/model-profiles.md "Agent presets"): the tiers of the model
// profiles, read and edited in a conversation, so they can be set up from every app without an editor of their own.
// A set changes one tier in every profile unless it names some, so a switch keeps it. What it would change is checked
// first (a launcher no computer has never reaches the card), shown on the approval card, and saved at the revision the
// card was made from: profiles edited in the meantime are a conflict, never overwritten.

// tierEdit is model_profiles.tiers' set value. A field left out keeps the tier's own. The flat fields are the first
// candidate's: a different launcher drops its old model and effort (they were the old launcher's), as a provider change
// does in Model Settings, and keeps the other candidates. Candidates, when given, replace the whole ordered list (the
// flat fields then edit its first); aliases, when given, replace the older names the tier answers to. Tier may name a
// tier by an alias.
type tierEdit struct {
	Tier           string           `json:"tier"`
	Launcher       *string          `json:"launcher"`
	Model          *string          `json:"model"`
	Effort         *string          `json:"effort"`
	Speed          *string          `json:"speed"`
	PermissionMode *string          `json:"permission_mode"`
	Host           *string          `json:"host"`
	Description    *string          `json:"description"`
	Candidates     *[]TierCandidate `json:"candidates"`
	Aliases        *[]string        `json:"aliases"`
	Profiles       []string         `json:"profiles"`
	Remove         bool             `json:"remove"`
}

// tierChange is what a set does to one profile: the tier (its own name, an alias resolved) before and after (nil: none).
type tierChange struct {
	ID, Name, Tier string
	Before, After  *ModelTier
}

// editTiers applies e to ps (fresh from the store; changed in place) and says what changed in which profile. Only the
// tiers are checked: something else in a profile that no longer holds (a deleted provider) does not stop a tier edit.
func editTiers(ps *modelProfiles, e tierEdit, now time.Time) ([]tierChange, error) {
	if len(ps.Profiles) == 0 {
		return nil, errModelProfileEmpty
	}
	targets := e.Profiles
	if len(targets) == 0 {
		for _, p := range ps.Profiles {
			targets = append(targets, p.ID)
		}
	}
	var changes []tierChange
	for _, id := range targets {
		i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == id })
		if i < 0 {
			return nil, fmt.Errorf("no model profile %q (profiles: %s)", id, profileIDs(ps.Profiles))
		}
		p := ps.Profiles[i]
		p.Tiers = maps.Clone(p.Tiers)
		name := e.Tier
		if own, ok := p.lookupTier(e.Tier); ok {
			name = own // an alias edits the tier that answers to it
		}
		was, had := p.Tiers[name]
		if e.Remove {
			if !had {
				continue
			}
			delete(p.Tiers, name)
		} else {
			t := was
			if e.Candidates != nil {
				t.Candidates = slices.Clone(*e.Candidates)
				for i := range t.Candidates {
					c := &t.Candidates[i]
					c.Launcher, c.Model, c.Effort = strings.TrimSpace(c.Launcher), strings.TrimSpace(c.Model), strings.TrimSpace(c.Effort)
					c.Speed, c.PermissionMode = strings.TrimSpace(c.Speed), strings.TrimSpace(c.PermissionMode)
				}
				if len(t.Candidates) == 0 {
					return nil, fmt.Errorf("tier %s needs at least one candidate", e.Tier)
				}
				t.normalize()
			}
			first := t.first()
			if e.Launcher != nil && strings.TrimSpace(*e.Launcher) != first.Launcher {
				first = TierCandidate{Launcher: strings.TrimSpace(*e.Launcher)}
			}
			for _, f := range []struct {
				set  *string
				into *string
			}{{e.Model, &first.Model}, {e.Effort, &first.Effort}, {e.Speed, &first.Speed}, {e.PermissionMode, &first.PermissionMode}, {e.Description, &t.Description}} {
				if f.set != nil {
					*f.into = strings.TrimSpace(*f.set)
				}
			}
			t.setFirst(first)
			if e.Host != nil {
				t.Host = e.Host
			}
			if e.Aliases != nil {
				t.Aliases = slices.Clone(*e.Aliases)
			}
			t.normalize()
			if !had && t.Launcher == "" {
				return nil, fmt.Errorf("%s (%s) has no tier %s yet: give its launcher to add it there, or name other profiles", p.Name, p.ID, e.Tier)
			}
			if had && t.equal(was) {
				continue
			}
			if p.Tiers == nil {
				p.Tiers = map[string]ModelTier{}
			}
			p.Tiers[name] = t
		}
		if err := checkModelTiers(p.Tiers); err != nil {
			return nil, fmt.Errorf("%s (%s): %w", p.Name, p.ID, err)
		}
		if t, ok := p.Tiers[name]; ok {
			var before *ModelTier
			if had {
				before = &was
			}
			if err := checkTierModel(name, t, before); err != nil { // only the tier edited: another's old model blocks nothing
				return nil, fmt.Errorf("%s (%s): %w", p.Name, p.ID, err)
			}
		}
		if len(p.Tiers) == 0 {
			p.Tiers = nil
		}
		c := tierChange{ID: p.ID, Name: p.Name, Tier: name}
		if had {
			c.Before = &was
		}
		if t, ok := p.Tiers[name]; ok {
			c.After = &t
		}
		p.UpdatedAt = now
		ps.Profiles[i] = p
		changes = append(changes, c)
	}
	if e.Remove && len(changes) == 0 {
		return nil, fmt.Errorf("no profile named has a tier %s", e.Tier)
	}
	return changes, nil
}

// sameTierChanges: whether two sets of changes are the same, profile by profile.
func sameTierChanges(a, b []tierChange) bool {
	same := func(x, y *ModelTier) bool { return (x == nil) == (y == nil) && (x == nil || x.equal(*y)) }
	return slices.EqualFunc(a, b, func(x, y tierChange) bool {
		return x.ID == y.ID && x.Tier == y.Tier && same(x.Before, y.Before) && same(x.After, y.After)
	})
}

func profileIDs(ps []ModelProfile) string {
	ids := make([]string, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	return strings.Join(ids, ", ")
}

// tierLauncherCheck says whether launcher can start an Agent somewhere: plain claude, a provider added in Persona that
// runs as a launcher, or one a computer lists (as last seen, and asked again before saying no: one just added to an
// rc file is not refused).
func (s *Service) tierLauncherCheck(ctx context.Context, name string) error {
	if proto.IsExternalLauncher(name) {
		return i18n.Err("external-adapter.purpose_unsupported")
	}
	if name == "" || name == "claude" {
		return nil // "" is checkModelTiers' to refuse
	}
	known := []string{"claude"}
	for _, l := range s.storedLaunchers(ctx) {
		if l.Name == name {
			if l.Kind == "api" && !messagesAPI(l.API) {
				return i18n.Err("assistant-models.err_api_as_launcher")
			}
			return nil
		}
		if l.Kind != "api" || messagesAPI(l.API) {
			known = append(known, l.Name)
		}
	}
	for _, refresh := range []bool{false, true} {
		for _, m := range s.zshrcLaunchers(ctx, refresh) {
			for _, l := range m.Launchers {
				if l.Name == name {
					return nil
				}
				known = append(known, l.Name)
			}
		}
	}
	slices.Sort(known)
	return fmt.Errorf("no computer has a launcher %q (launchers: %s)", name, strings.Join(slices.Compact(known), ", "))
}

func (s *Service) setModelTiers(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var e tierEdit
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return nil, fmt.Errorf("value must be an object: %s (%v)", modelTiersValue, err)
	}
	e.Tier = strings.TrimSpace(e.Tier)
	if !modelTierName.MatchString(e.Tier) {
		return nil, i18n.Err("assistant-model_profiles.err_tier_name", i18n.Args{"tier": e.Tier})
	}
	given := e.Launcher != nil || e.Model != nil || e.Effort != nil || e.Speed != nil || e.PermissionMode != nil ||
		e.Host != nil || e.Description != nil || e.Candidates != nil || e.Aliases != nil
	switch {
	case e.Remove && given:
		return nil, errors.New("remove takes only tier and profiles")
	case !e.Remove && !given:
		return nil, errors.New("give what to set (launcher, model, effort, speed, permission_mode, host, description, candidates, aliases), or remove: true")
	}
	var launchers []string
	if e.Candidates != nil {
		for _, c := range *e.Candidates {
			launchers = append(launchers, strings.TrimSpace(c.Launcher))
		}
	}
	if e.Launcher != nil {
		launchers = append(launchers, strings.TrimSpace(*e.Launcher))
	}
	s.models.mu.Lock()
	ps, err := s.modelProfilesLocked(ctx)
	var changes []tierChange
	if err == nil {
		changes, err = editTiers(&ps, e, time.Now().UTC())
	}
	s.models.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, l := range slices.Compact(slices.Sorted(slices.Values(launchers))) {
		// Before the card, and outside models.mu: a computer may be asked for its launchers.
		if err := s.tierLauncherCheck(ctx, l); err != nil {
			return nil, err
		}
	}
	for _, c := range changes {
		if c.After == nil {
			continue
		}
		for _, cand := range c.After.list() {
			if cand.PermissionMode != "" && !slices.Contains(s.permissionModesForLauncher(ctx, "", cand.Launcher, nil), cand.PermissionMode) {
				return nil, fmt.Errorf("%s (%s): %w", c.Name, c.ID, i18n.Err("assistant-models.err_permission_mode_unsupported"))
			}
		}
	}
	if len(changes) == 0 {
		return &ownChange{summary: "Nothing to change: tier " + e.Tier + " is already like that in every profile named."}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	revision := ps.Revision
	profileAfter := map[string]ModelProfile{}
	for _, change := range changes {
		if i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == change.ID }); i >= 0 {
			profileAfter[change.ID] = cloneModelProfile(ps.Profiles[i])
		}
	}
	return &ownChange{applyChecksProposal: true, revision: &revision, modelProfileWrite: true, profileAfter: profileAfter, cardText: func(l i18n.Lang) string {
		lines := make([]string, 0, len(changes))
		for _, c := range changes {
			lines = append(lines, i18n.T(l, "assistant-settings_tool.change_model_tier", i18n.Args{"profile": c.Name, "tier": c.Tier,
				"before": tierCardText(l, c.Before), "after": tierCardText(l, c.After)}))
		}
		return strings.Join(lines, "\n")
	},
		apply: func(ctx context.Context) (string, error) { return s.saveTierEdit(ctx, revision, e, changes) },
	}, nil
}

// tierCardText is a tier as the approval card shows it in l: every candidate, in order.
func tierCardText(l i18n.Lang, t *ModelTier) string {
	if t == nil {
		return i18n.T(l, "assistant-settings_tool.none")
	}
	text := t.allRuns()
	if t.Description != "" {
		text += " — " + t.Description
	}
	return clipped(l, text)
}

// saveTierEdit makes the edit the user approved, at the revision its card was made from, and only when it still changes
// what the card said.
func (s *Service) saveTierEdit(ctx context.Context, revision int64, e tierEdit, card []tierChange) (string, error) {
	s.models.mu.Lock()
	text, active, err := s.saveTierEditLocked(ctx, revision, e, card)
	s.models.mu.Unlock()
	if err == nil {
		s.profileEvent(ctx, active)
	}
	return text, err
}

func (s *Service) saveTierEditLocked(ctx context.Context, revision int64, e tierEdit, card []tierChange) (string, string, error) {
	ps, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return "", "", err
	}
	stale := fmt.Errorf("the model profiles changed after the card was shown, so nothing was saved: set it again for a new card (%w)", approvalops.ErrStale)
	if ps.Revision != revision {
		return "", "", stale
	}
	now := time.Now().UTC()
	changes, err := editTiers(&ps, e, now)
	if err != nil {
		return "", "", err
	}
	if !sameTierChanges(changes, card) {
		return "", "", stale
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return "", "", err
	}
	// one event for the edit, however many profiles it changed: <profile>.tiers.<name> each
	event := ModelProfileEvent{At: now, Action: "save", By: ProfileActor{Kind: "assistant"}}
	for _, c := range changes {
		event.Changed = append(event.Changed, c.ID+".tiers."+c.Tier)
	}
	state.record(event)
	ps.Revision++
	if err := s.putModelProfileSettings(ctx, map[string]any{modelProfilesKey: ps, modelProfileStateKey: state}); err != nil {
		return "", "", err
	}
	s.ProfileChanged(ctx)
	var done, without []string
	for _, c := range changes {
		done = append(done, c.ID)
	}
	for _, p := range ps.Profiles {
		if _, ok := p.lookupTier(e.Tier); !ok {
			without = append(without, p.ID)
		}
	}
	verb := "Saved tier "
	if e.Remove {
		verb = "Removed tier "
	}
	text := verb + e.Tier + " in " + strings.Join(done, ", ") + "."
	if len(without) > 0 && !e.Remove {
		text += " Profiles without it (a start on it fails while one of them is in use): " + strings.Join(without, ", ") + "."
	}
	if state.Active == "" {
		text += " No profile is in use: tiers start nothing until one is switched to (model_profile_switch)."
	}
	return text, state.Active, nil
}

// getModelTiers is model_profiles.tiers' get: every profile's tiers, the one in use marked.
func (s *Service) getModelTiers(ctx context.Context) (string, error) {
	v, err := s.modelProfilesView(ctx)
	if err != nil {
		return "", err
	}
	if len(v.Profiles) == 0 {
		return "", errModelProfileEmpty
	}
	var b strings.Builder
	for _, p := range v.Profiles {
		b.WriteString("- " + p.Name + " (" + p.ID + ")")
		if p.Active {
			b.WriteString(", in use")
		}
		b.WriteByte(':')
		if len(p.Tiers) == 0 {
			b.WriteString(" no tiers")
		}
		for _, name := range slices.Sorted(maps.Keys(p.Tiers)) {
			t := p.Tiers[name]
			b.WriteString("\n  - " + name + " = " + t.allRuns())
			if t.Description != "" {
				b.WriteString(" — " + t.Description)
			}
			if len(t.Aliases) > 0 {
				b.WriteString(" (also answers to " + strings.Join(t.Aliases, ", ") + ")")
			}
		}
		b.WriteByte('\n')
	}
	if v.Active == "" {
		b.WriteString("No profile is in use: tiers start nothing until one is switched to (model_profile_switch).\n")
	}
	b.WriteString("Effort levels: " + strings.Join(proto.EffortLevels, ", ") + ".")
	return b.String(), nil
}

// modelTiersValue is the shape of model_profiles.tiers' set value, as the setting's list entry says it.
const modelTiersValue = `{"tier": a name (lowercase letters, digits, _ -), "launcher": a launcher name, "model": a model id ("" the launcher's default), ` +
	`"effort": a level ("" the launcher's), "speed": standard | fast | ultrafast ("" the launcher's), ` +
	`"host": agy or an enabled engine such as herdr ("" clears the tier host), ` +
	`"permission_mode": default | acceptEdits | plan | auto | bypassPermissions ("" the launcher's), "description": when to use it (one line, at most 100 characters), ` +
	`"candidates": [{"launcher", "model", "effort", "speed", "permission_mode"}, …] the ordered launchers the tier may run on, at most 6 (the hub takes the first usable one; the flat fields are the first), ` +
	`"aliases": [older names the tier answers to], "profiles": [profile ids] (default: every profile), "remove": true to delete it}; ` +
	`a field left out keeps the tier's own (a different launcher drops the first candidate's old model, effort, speed and permission_mode), a new tier needs its launcher`
