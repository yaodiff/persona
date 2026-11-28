package assistant

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// Model profiles (docs/assistant/model-profiles.md): a named set of model choices the user switches between in one
// step — "performance" while the strong models' quota lasts, "economy" when it runs short. A profile holds each
// category's choice (the model configuration's records, models.go), the model fields of providers added in Persona
// (launchers.managed, models_launchers.go) and named tiers an Agent can be started on (agent_start, task_plan). A
// switch writes all of it in one transaction and keeps what it replaced, so one rollback undoes it; every switch,
// rollback and edit is in the history. Users can create profiles or use their model settings directly.

const (
	modelProfilesKey     = "model_profiles"       // modelProfiles: the profiles
	modelProfileStateKey = "model_profiles.state" // modelProfileState: which one is in use, the rollback, the history

	maxModelProfiles    = 20
	maxModelTiers       = 12
	maxTierCandidates   = 6 // the ordered candidates one tier lists (#14700)
	maxTierAliases      = 8 // older names one tier answers to
	maxProfileEvents    = 50
	maxProfileRollbacks = 10
	modelProfileName    = 40  // runes
	modelProfileAbout   = 300 // runes
	modelTierAbout      = 100 // runes: a tier's description is in every turn's snapshot
	modelProfileValue   = 200 // bytes of a provider, model or launcher name
	shownProfileEvents  = 20
	modelProfileRolling = "rollback" // not an id: POST /api/v1/model-profiles/rollback
	legacyRollbackID    = "previous" // fallback rollback id for legacy single-slot Previous images
)

var (
	modelProfileID = lazyre.New(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	modelTierName  = lazyre.New(`^[a-z][a-z0-9_-]{0,23}$`)
)

// ModelProfile is one profile: what it sets, and nothing else (a category or field it leaves out stays as it is).
type ModelProfile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Purposes: a category's choice (its record in the model configuration) by category id; an empty binding is
	// "follow the default". The profile the main chat's new conversations start in is not a profile's.
	Purposes map[string]ModelBinding `json:"purposes,omitempty"`
	// Launchers: model fields of providers added in Persona, by provider name.
	Launchers map[string]LauncherModels `json:"launchers,omitempty"`
	// Tiers: what an Agent started on a tier runs on (agent_start's and task_plan's tier).
	Tiers     map[string]ModelTier `json:"tiers,omitempty"`
	UpdatedAt time.Time            `json:"updated_at"`
}

// LauncherModels are the model fields of a managed launcher a profile sets: nil leaves a field as it is, "" clears it
// (the launcher's own default then applies; the main model cannot be cleared).
type LauncherModels struct {
	Model         *string `json:"model,omitempty"`
	SmallModel    *string `json:"small_model,omitempty"`
	SubagentModel *string `json:"subagent_model,omitempty"`
	OpusModel     *string `json:"opus_model,omitempty"`
	SonnetModel   *string `json:"sonnet_model,omitempty"`
	HaikuModel    *string `json:"haiku_model,omitempty"`
	FableModel    *string `json:"fable_model,omitempty"`
	Effort        *string `json:"effort,omitempty"`
}

// fields pairs each model field of a managed launcher a profile can set (by its JSON name) with its value in m and
// the field of l it sets (nil: only m's).
func (m *LauncherModels) fields(l *proto.ManagedLauncher) []launcherField {
	if l == nil {
		l = &proto.ManagedLauncher{}
	}
	return []launcherField{{"model", &m.Model, &l.Model}, {"small_model", &m.SmallModel, &l.SmallModel},
		{"subagent_model", &m.SubagentModel, &l.SubagentModel}, {"opus_model", &m.OpusModel, &l.OpusModel},
		{"sonnet_model", &m.SonnetModel, &l.SonnetModel}, {"haiku_model", &m.HaikuModel, &l.HaikuModel},
		{"fable_model", &m.FableModel, &l.FableModel}, {"effort", &m.Effort, &l.Effort}}
}

type launcherField struct {
	name     string
	set      **string // the profile's value
	launcher *string  // the launcher's
}

func profileValue(v string) *string { return &v }

// ModelTier is what an Agent started on a tier (an Agent preset) runs on: a launcher, and its model, effort, start
// speed and permission mode ("" for its defaults). Description says when to use it (Paseo's "When to use"): the
// assistant reads it to pick a tier, the New Agent form shows it. Host applies to the whole tier.
//
// A tier may list several ordered candidates (#14700): the hub picks one at dispatch (pickTierCandidate), in the
// user's order. The flat fields are always candidate 0, so an older client that reads or writes only them keeps
// working; Candidates is empty for a tier with one candidate, which behaves exactly as a tier did before. Aliases are
// older names the tier still answers to (strong for core, standard for dev): a stored plan task, a start-defaults
// preset or an older client naming one resolves to this tier.
type ModelTier struct {
	Launcher       string          `json:"launcher"`
	Model          string          `json:"model,omitempty"`
	Effort         string          `json:"effort,omitempty"`
	Speed          string          `json:"speed,omitempty"`
	PermissionMode string          `json:"permission_mode,omitempty"`
	Host           *string         `json:"host,omitempty"` // tier-wide start host. A profile edit can omit it to keep it or send "" to clear it.
	Description    string          `json:"description,omitempty"`
	Candidates     []TierCandidate `json:"candidates,omitempty"`
	Aliases        []string        `json:"aliases,omitempty"`
	// Runtime and Channel are candidate 0's runtime and channel (docs/assistant/execution-spec.md): given with no
	// launcher, a save fills the launcher alias that runs them. The launcher is what runs.
	Runtime string `json:"runtime,omitempty"`
	// Channel: see Runtime.
	Channel string `json:"channel,omitempty"`
}

// TierCandidate is one launcher a tier may run on, with its model, effort, start speed and permission mode.
type TierCandidate struct {
	Launcher       string `json:"launcher"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	Speed          string `json:"speed,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	// Runtime and Channel are the candidate's runtime and channel, as ModelTier's.
	Runtime string `json:"runtime,omitempty"`
	// Channel: see Runtime.
	Channel string `json:"channel,omitempty"`
}

// first is candidate 0: the flat fields.
func (t ModelTier) first() TierCandidate {
	return TierCandidate{Launcher: t.Launcher, Model: t.Model, Effort: t.Effort, Speed: t.Speed, PermissionMode: t.PermissionMode,
		Runtime: t.Runtime, Channel: t.Channel}
}

// list is every candidate in the user's order; one (the flat fields) for a tier that lists none.
func (t ModelTier) list() []TierCandidate {
	if len(t.Candidates) == 0 {
		return []TierCandidate{t.first()}
	}
	return t.Candidates
}

// on is t run on candidate c alone: c's fields with t's description, no other candidate and no alias. A start uses it,
// so the code after the pick sees the one-candidate tier it always did.
func (t ModelTier) on(c TierCandidate) ModelTier {
	return ModelTier{Launcher: c.Launcher, Model: c.Model, Effort: c.Effort, Speed: c.Speed, PermissionMode: c.PermissionMode, Host: t.Host, Description: t.Description,
		Runtime: c.Runtime, Channel: c.Channel}
}

// setFirst replaces candidate 0 with c and keeps the others.
func (t *ModelTier) setFirst(c TierCandidate) {
	t.Launcher, t.Model, t.Effort, t.Speed, t.PermissionMode = c.Launcher, c.Model, c.Effort, c.Speed, c.PermissionMode
	t.Runtime, t.Channel = c.Runtime, c.Channel
	if len(t.Candidates) > 0 {
		t.Candidates = slices.Clone(t.Candidates)
		t.Candidates[0] = c
	}
}

// normalize makes the flat fields candidate 0 and drops a list of one (the flat fields say it all), so a tier with
// one candidate is stored exactly as before. Aliases are trimmed, in order, without repeats.
func (t *ModelTier) normalize() {
	if t.Host != nil {
		if host := strings.TrimSpace(*t.Host); host != "" {
			t.Host = &host
		} else {
			t.Host = nil
		}
	}
	switch len(t.Candidates) {
	case 0:
		t.Candidates = nil
	case 1:
		c := t.Candidates[0]
		t.Candidates = nil
		t.setFirst(c)
	default:
		t.Candidates = slices.Clone(t.Candidates)
		t.setFirst(t.Candidates[0])
	}
	var aliases []string
	for _, a := range t.Aliases {
		if a = strings.TrimSpace(a); a != "" && !slices.Contains(aliases, a) {
			aliases = append(aliases, a)
		}
	}
	t.Aliases = aliases
}

// equal: the same candidates, description and aliases.
func (t ModelTier) equal(o ModelTier) bool {
	return t.first() == o.first() && t.startHost() == o.startHost() && t.Description == o.Description && slices.Equal(t.Candidates, o.Candidates) &&
		slices.Equal(t.Aliases, o.Aliases)
}

func (t ModelTier) startHost() string {
	if t.Host == nil {
		return ""
	}
	return *t.Host
}

// answersTo: name is t's alias.
func (t ModelTier) answersTo(name string) bool { return slices.Contains(t.Aliases, name) }

// runs is what the tier runs on, in a line: candidate 0's launch settings followed by the tier's host, when set.
func (t ModelTier) runs() string { return t.withHostLabel(t.first().runs()) }

// allRuns is every candidate in a line, in order (one candidate: runs).
func (t ModelTier) allRuns() string {
	parts := make([]string, 0, len(t.list()))
	for _, c := range t.list() {
		parts = append(parts, c.runs())
	}
	return t.withHostLabel(strings.Join(parts, " / "))
}

func (t ModelTier) withHostLabel(run string) string {
	if host := t.startHost(); host != "" {
		return run + " · " + host
	}
	return run
}

func (c TierCandidate) runs() string {
	parts := []string{c.Launcher}
	for _, v := range []string{c.Model, c.Effort} {
		if v != "" {
			parts = append(parts, v)
		}
	}
	if proto.FasterSpeed(c.Speed) {
		parts = append(parts, c.Speed)
	}
	if c.PermissionMode != "" {
		parts = append(parts, c.PermissionMode)
	}
	return strings.Join(parts, " · ")
}

// ProfileActor is who switched or edited a profile: a device (its name, from the hub), the hub's own token, the
// assistant (a conversation's tool call), or the manager itself (system: the templates' first use).
type ProfileActor struct {
	Kind string `json:"kind"` // device, hub, assistant, system
	Name string `json:"name,omitempty"`
}

// ModelProfileEvent is one entry of the history.
type ModelProfileEvent struct {
	At      time.Time    `json:"at"`
	Action  string       `json:"action"` // switch, rollback, save, delete, seed
	From    string       `json:"from,omitempty"`
	To      string       `json:"to,omitempty"` // switch, rollback: the profile now in use; save, delete: the profile
	By      ProfileActor `json:"by"`
	Changed []string     `json:"changed,omitempty"` // what a switch or rollback changed: categories, <launcher>.<field>; a tier edit: tiers.<name>
}

type modelProfiles struct {
	Revision int64          `json:"revision"`
	Profiles []ModelProfile `json:"profiles"`
}

type modelProfileState struct {
	Active         string              `json:"active,omitempty"` // the profile in use ("" none)
	At             time.Time           `json:"at,omitzero"`
	By             ProfileActor        `json:"by,omitzero"`
	Previous       *modelProfileImage  `json:"previous,omitempty"`         // latest rollback point (synced with Rollbacks for backward compatibility)
	Rollbacks      []modelProfileImage `json:"rollbacks,omitempty"`        // oldest first, up to maxProfileRollbacks
	NextRollbackID int64               `json:"next_rollback_id,omitempty"` // monotonic id sequence for rollback points
	Events         []ModelProfileEvent `json:"events,omitempty"`           // oldest first, the last maxProfileEvents
}

// modelProfileImage is what a switch replaced: the categories' and launcher fields' values before it, and the profile
// that was in use.
type modelProfileImage struct {
	ID        string                    `json:"id,omitempty"`
	Profile   string                    `json:"profile,omitempty"`
	At        time.Time                 `json:"at"`
	Purposes  map[string]ModelBinding   `json:"purposes,omitempty"`
	Launchers map[string]LauncherModels `json:"launchers,omitempty"`
	Redo      bool                      `json:"redo,omitempty"` // true when this single entry redoes a rollback
}

func cloneLauncherModels(m LauncherModels) LauncherModels {
	var out LauncherModels
	dst := out.fields(nil)
	for i, f := range m.fields(nil) {
		if *f.set != nil {
			*dst[i].set = profileValue(**f.set)
		}
	}
	return out
}

func cloneModelProfiles(ps modelProfiles) modelProfiles {
	out := modelProfiles{Revision: ps.Revision, Profiles: make([]ModelProfile, len(ps.Profiles))}
	for i, profile := range ps.Profiles {
		out.Profiles[i] = cloneModelProfile(profile)
	}
	return out
}

// cloneModelProfile detaches every mutable map, pointer and slice before a managed edit or an exact before/after check.
func cloneModelProfile(p ModelProfile) ModelProfile {
	p.Purposes = maps.Clone(p.Purposes)
	if p.Launchers != nil {
		launchers := make(map[string]LauncherModels, len(p.Launchers))
		for name, fields := range p.Launchers {
			launchers[name] = cloneLauncherModels(fields)
		}
		p.Launchers = launchers
	}
	if p.Tiers != nil {
		tiers := make(map[string]ModelTier, len(p.Tiers))
		for name, tier := range p.Tiers {
			if tier.Host != nil {
				tier.Host = new(*tier.Host)
			}
			tier.Candidates = slices.Clone(tier.Candidates)
			tier.Aliases = slices.Clone(tier.Aliases)
			tiers[name] = tier
		}
		p.Tiers = tiers
	}
	return p
}

func cloneProfileImage(img modelProfileImage) modelProfileImage {
	out := img
	if img.Purposes != nil {
		out.Purposes = maps.Clone(img.Purposes)
	}
	if img.Launchers != nil {
		out.Launchers = make(map[string]LauncherModels, len(img.Launchers))
		for name, m := range img.Launchers {
			out.Launchers[name] = cloneLauncherModels(m)
		}
	}
	return out
}

// rollbackStack returns the ordered rollback points (oldest first, newest last), reading legacy single-slot Previous
// when Rollbacks is empty.
func (st *modelProfileState) rollbackStack() []modelProfileImage {
	if len(st.Rollbacks) > 0 {
		out := make([]modelProfileImage, len(st.Rollbacks))
		for i, img := range st.Rollbacks {
			out[i] = cloneProfileImage(img)
		}
		return out
	}
	if st.Previous != nil {
		return []modelProfileImage{cloneProfileImage(*st.Previous)}
	}
	return nil
}

func (st *modelProfileState) nextRollbackID() string {
	st.NextRollbackID++
	return strconv.FormatInt(st.NextRollbackID, 10)
}

func (st *modelProfileState) setRollbacks(stack []modelProfileImage) {
	if n := len(stack) - maxProfileRollbacks; n > 0 {
		stack = slices.Delete(stack, 0, n)
	}
	if len(stack) == 0 {
		st.Rollbacks = nil
		st.Previous = nil
		return
	}
	out := make([]modelProfileImage, len(stack))
	for i, img := range stack {
		img = cloneProfileImage(img)
		if img.ID == "" {
			img.ID = st.nextRollbackID()
		}
		out[i] = img
	}
	st.Rollbacks = out
	top := cloneProfileImage(out[len(out)-1])
	st.Previous = &top
}

// pushRollback appends a new switch's before-image to the bounded rollback stack, clearing any redo entry so undo
// history stays strictly chronological across quick round-trip switches.
func (st *modelProfileState) pushRollback(img modelProfileImage) {
	stack := st.rollbackStack()
	stack = slices.DeleteFunc(stack, func(entry modelProfileImage) bool { return entry.Redo })
	img = cloneProfileImage(img)
	img.Redo = false
	img.ID = st.nextRollbackID()
	stack = append(stack, img)
	st.setRollbacks(stack)
}

// composeRollbackImage folds the delta images from the top of stack down to targetIdx into one atomic restore image
// matching the exact configuration before stack[targetIdx]'s switch.
func composeRollbackImage(stack []modelProfileImage, targetIdx int) modelProfileImage {
	target := stack[targetIdx]
	out := modelProfileImage{
		ID:        target.ID,
		Profile:   target.Profile,
		At:        target.At,
		Purposes:  map[string]ModelBinding{},
		Launchers: map[string]LauncherModels{},
		Redo:      target.Redo,
	}
	for i := len(stack) - 1; i >= targetIdx; i-- {
		for cat, b := range stack[i].Purposes {
			out.Purposes[cat] = b
		}
		for name, m := range stack[i].Launchers {
			cur := out.Launchers[name]
			dst := cur.fields(nil)
			for k, f := range m.fields(nil) {
				if *f.set != nil {
					*dst[k].set = profileValue(**f.set)
				}
			}
			out.Launchers[name] = cur
		}
	}
	return out
}

func rollbackEntryID(img modelProfileImage) string {
	if img.ID != "" {
		return img.ID
	}
	return legacyRollbackID
}

func findRollbackIndex(stack []modelProfileImage, id string) int {
	if len(stack) == 0 {
		return -1
	}
	if id == "" {
		return len(stack) - 1
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if rollbackEntryID(stack[i]) == id {
			return i
		}
	}
	return -1
}

// migratableProvider returns provider when it names a configured provider suitable for pinning a legacy model-only
// binding, or "" when no configured provider exists (for example an unconfigured legacy.* baseline with no BaseURL).
func (s *Service) migratableProvider(ctx context.Context, provider string) string {
	if provider == "" {
		return ""
	}
	if strings.HasPrefix(provider, "legacy.") && s.legacyModelConfig(ctx, provider).BaseURL == "" {
		return ""
	}
	return provider
}

// assistantDefaultLauncher returns the main chat's effective default launcher without mutating s.prefs' cache.
func (s *Service) assistantDefaultLauncher(ctx context.Context) string {
	var v Settings
	s.prefs.mu.Lock()
	if s.prefs.cached != nil && (s.store() == nil || time.Since(s.prefs.at) < settingsFresh) {
		v = *s.prefs.cached
		s.prefs.mu.Unlock()
		return s.effective(ctx, v).Launcher
	}
	s.prefs.mu.Unlock()
	if st := s.store(); st != nil {
		_, _ = st.Setting(ctx, settingsKey, &v)
	}
	return s.effective(ctx, v).Launcher
}

// resolveLegacyModelProvider resolves the predictable provider for a legacy model-only binding (Provider == "" and
// Model != "") on category id, preferring overlay (the profile's own bindings) over live records and reusing any
// already-loaded records without extra store reads.
func (s *Service) resolveLegacyModelProvider(ctx context.Context, id string, overlay map[string]ModelBinding, records map[string]modelRecord, stored []storedLauncher) (string, error) {
	switch id {
	case "assistant":
		return launcherProvider(s.assistantDefaultLauncher(ctx)), nil
	case "agent.new":
		return "launcher.claude", nil
	case "kb", "inbox.triage":
		legacy := "legacy.kb"
		if id == "inbox.triage" {
			legacy = "legacy.triage"
		}
		if s.legacyModelConfig(ctx, legacy).BaseURL != "" {
			return legacy, nil
		}
		if name := fallbackAPIProviderFrom(stored, s.hiddenLaunchers(ctx)); name != "" {
			return launcherProvider(name), nil
		}
		return "", nil
	case "agent.delegate", "translate":
		if overlay != nil {
			if triage, ok := overlay["inbox.triage"]; ok {
				if triage.Provider != "" {
					return s.migratableProvider(ctx, triage.Provider), nil
				}
				return s.resolveLegacyModelProvider(ctx, "inbox.triage", nil, records, stored)
			}
		}
		if records != nil {
			if r, ok := records["inbox.triage"]; ok {
				if r.Binding.Provider != "" {
					return s.migratableProvider(ctx, r.Binding.Provider), nil
				}
				return s.resolveLegacyModelProvider(ctx, "inbox.triage", nil, records, stored)
			}
		}
		r, err := s.modelRecord(ctx, "inbox.triage")
		if err != nil {
			return "", err
		}
		if records != nil {
			records["inbox.triage"] = r
		}
		if r.Binding.Provider != "" {
			return s.migratableProvider(ctx, r.Binding.Provider), nil
		}
		return s.resolveLegacyModelProvider(ctx, "inbox.triage", nil, records, stored)
	default:
		return "", nil
	}
}

// normalizeProfilePurposes migrates legacy model-only category bindings (Provider == "" && Model != "") to include
// their resolved Provider so they are displayed, diffed, and applied predictably. Empty bindings ("follow the default")
// and effort-only bindings are left untouched.
func (s *Service) normalizeProfilePurposes(ctx context.Context, purposes map[string]ModelBinding, records map[string]modelRecord, stored []storedLauncher) (map[string]ModelBinding, error) {
	if len(purposes) == 0 {
		return purposes, nil
	}
	var out map[string]ModelBinding
	for _, id := range modelCategoryIDs {
		b, ok := purposes[id]
		if !ok || b.Provider != "" || b.Model == "" {
			continue
		}
		overlay := purposes
		if out != nil {
			overlay = out
		}
		provider, err := s.resolveLegacyModelProvider(ctx, id, overlay, records, stored)
		if err != nil {
			return nil, err
		}
		if provider == "" {
			continue
		}
		if out == nil {
			out = maps.Clone(purposes)
		}
		b.Provider = provider
		out[id] = b
	}
	if out == nil {
		return purposes, nil
	}
	return out, nil
}

func (st *modelProfileState) clearActive() {
	st.Active, st.At, st.By = "", time.Time{}, ProfileActor{}
}

func (st *modelProfileState) record(e ModelProfileEvent) {
	st.Events = append(st.Events, e)
	if n := len(st.Events) - maxProfileEvents; n > 0 {
		st.Events = slices.Delete(st.Events, 0, n)
	}
}

var (
	errModelProfileNotFound    = i18n.Err("assistant-model_profiles.err_not_found")
	errModelProfileEmpty       = i18n.Err("assistant-model_profiles.err_no_profiles")
	errModelProfileConflict    = i18n.Err("assistant-model_profiles.err_conflict")
	errModelProfileNoRollback  = i18n.Err("assistant-model_profiles.err_no_rollback")
	errModelProfileAtomicStore = i18n.Err("assistant-model_profiles.err_atomic_store")
)

// ---- what is stored ----

func (s *Service) readModelProfiles(ctx context.Context) (modelProfiles, bool, error) {
	var ps modelProfiles
	st := s.store()
	if st == nil {
		return ps, false, i18n.Err("assistant-models.err_no_database")
	}
	ok, err := st.Setting(ctx, modelProfilesKey, &ps)
	// The profile the main chat's new conversations start in is never a model profile's (#4633): one stored holding
	// it anyway is read without it, so it neither shows a change it cannot make nor fails checkModelProfile at a
	// switch or an edit, and the next write stores it clean.
	for i := range ps.Profiles {
		p := &ps.Profiles[i]
		for id, b := range p.Purposes {
			if b.ConfigDir != "" {
				b.ConfigDir = ""
				p.Purposes[id] = b
			}
		}
		migrateSeededTiers(p)
	}
	return ps, ok, err
}

// seededTierNames are the two tiers Persona seeded under strength names before tiers were named by the kind of work
// (#14700), with the names they have now.
var seededTierNames = [][2]string{{"strong", "core"}, {"standard", "dev"}}

// seededTierAliases are the older names the seeded tier name answers to (strong for core, standard for dev), in a
// fresh slice. A capture gives them to the tiers it makes, as migrateSeededTiers gives them to a
// migrated tier, so a stored plan task, a start-defaults preset or an older client naming the old one resolves on
// every profile, whichever is in use.
func seededTierAliases(name string) []string {
	for _, names := range seededTierNames {
		if names[1] == name {
			return []string{names[0]}
		}
	}
	return nil
}

// migrateSeededTiers renames the two seeded tiers as p is read: strong to core, standard to dev, with the old name kept
// as an alias and nothing else changed, so a plan task, a start-defaults preset or an older client naming the old one
// resolves to the same launcher and model. The next write stores it. A tier the user made keeps its name, and so does
// a seeded one whose new name is taken.
func migrateSeededTiers(p *ModelProfile) {
	for _, names := range seededTierNames {
		from, to := names[0], names[1]
		t, ok := p.Tiers[from]
		if !ok {
			continue
		}
		if _, taken := p.lookupTier(to); taken {
			continue
		}
		tiers := maps.Clone(p.Tiers)
		delete(tiers, from)
		t.Aliases = append(slices.Clone(t.Aliases), from)
		t.normalize()
		tiers[to] = t
		p.Tiers = tiers
	}
}

// lookupTier is tier name of p, by its name, else by one of its aliases; with the tier's own name.
func (p *ModelProfile) lookupTier(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if _, ok := p.Tiers[name]; ok {
		return name, true
	}
	for _, own := range slices.Sorted(maps.Keys(p.Tiers)) {
		if p.Tiers[own].answersTo(name) {
			return own, true
		}
	}
	return "", false
}

func (s *Service) readModelProfileState(ctx context.Context) (modelProfileState, error) {
	var state modelProfileState
	st := s.store()
	if st == nil {
		return state, i18n.Err("assistant-models.err_no_database")
	}
	_, err := st.Setting(ctx, modelProfileStateKey, &state)
	return state, err
}

// putModelProfileSettings requires an atomic store: a failed write must leave every setting untouched.
func (s *Service) putModelProfileSettings(ctx context.Context, values map[string]any) error {
	for _, value := range values {
		if r, ok := value.(modelRecord); ok {
			if err := checkExternalPurpose(r.Binding); err != nil {
				return err
			}
		}
	}
	st := s.store()
	if st == nil {
		return i18n.Err("assistant-models.err_no_database")
	}
	if tx, ok := st.(interface {
		PutSettings(context.Context, map[string]any) error
	}); ok {
		return tx.PutSettings(ctx, values)
	}
	return errModelProfileAtomicStore
}

// modelProfilesLocked reads the catalog (models.mu held). An unconfigured hub has the zero-value catalog.
func (s *Service) modelProfilesLocked(ctx context.Context) (modelProfiles, error) {
	if plan := ownerProfilePlanFrom(ctx); plan != nil {
		if !plan.loaded {
			ps, _, err := s.readModelProfiles(ctx)
			if err != nil {
				return modelProfiles{}, err
			}
			plan.profiles = cloneModelProfiles(ps)
			plan.loaded = true
		}
		// Every preparation receives its own deep copy. Only an explicit planned
		// catalog write advances the shared batch snapshot.
		return cloneModelProfiles(plan.profiles), nil
	}
	ps, _, err := s.readModelProfiles(ctx)
	return ps, err
}

// captureModels is the configuration as a profile would set it: every category's own choice, the model fields of the
// providers added in Persona those categories run on, and two tiers named by the kind of work (core: the main chat's
// choice, for core design and implementation; dev: the Agents', for development, debugging and research), each
// answering to its older name too (strong, standard), as a migrated one does.
func (s *Service) captureModels(ctx context.Context) (map[string]ModelBinding, map[string]LauncherModels, map[string]ModelTier, error) {
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx = withManagedSnapshot(ctx, stored)
	purposes := map[string]ModelBinding{}
	used := map[string]bool{}
	tiers := map[string]ModelTier{}
	l := s.requestCtxLang(ctx)
	for _, id := range modelCategoryIDs {
		r, err := s.modelRecord(ctx, id)
		if err != nil {
			return nil, nil, nil, err
		}
		effective, err := s.resolveModelRecord(ctx, id, &r)
		if err != nil {
			return nil, nil, nil, err
		}
		b := r.Binding
		b.ConfigDir = ""
		if b.Provider == "" && b.Model != "" {
			if provider := s.migratableProvider(ctx, effective.Provider); provider != "" {
				b.Provider = provider
			}
		}
		purposes[id] = b
		name, ok := strings.CutPrefix(effective.Provider, "launcher.")
		if !ok {
			continue
		}
		used[name] = true
		tier := map[string]string{"assistant": "core", "agent.new": "dev"}[id]
		if tier != "" {
			model := effective.Model
			if !proto.ValidModelID(model) {
				model = "" // configured before #1644 held models to the rule: the launcher's default, as applyModel runs it
			}
			tiers[tier] = ModelTier{Launcher: name, Model: model, Effort: effective.Effort,
				Description: i18n.T(l, "assistant-model_profiles.tier_"+tier), Aliases: seededTierAliases(tier)}
		}
	}
	launchers := map[string]LauncherModels{}
	for _, l := range stored {
		if !used[l.Name] {
			continue
		}
		var m LauncherModels
		for _, f := range m.fields(&l.ManagedLauncher) {
			if l.Kind == "api" && f.name != "model" {
				continue
			}
			if *f.launcher == "" && f.name != "small_model" && strings.HasSuffix(f.name, "_model") {
				continue
			}
			*f.set = profileValue(*f.launcher)
		}
		launchers[l.Name] = m
	}
	return purposes, launchers, tiers, nil
}

// ---- tiers ----

// withTier fills a start's launcher, model and effort from tier: what the start names itself wins, and the tier's
// model and effort go only with the tier's launcher.
func withTier(t ModelTier, launcher, model, effort *string) {
	if *launcher != "" && *launcher != t.Launcher {
		return
	}
	*launcher = t.Launcher
	if *model == "" {
		*model = t.Model
	}
	if *effort == "" {
		*effort = t.Effort
	}
}
