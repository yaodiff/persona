package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- comparing with the configuration ----

// profileRecords reads the records of the categories any of ps names, each once: the view compares every profile
// with them while it holds models.mu.
func (s *Service) profileRecords(ctx context.Context, ps []ModelProfile) (map[string]modelRecord, error) {
	records := map[string]modelRecord{}
	for _, id := range modelCategoryIDs {
		if !slices.ContainsFunc(ps, func(p ModelProfile) bool { _, ok := p.Purposes[id]; return ok }) {
			continue
		}
		r, err := s.modelRecord(ctx, id)
		if err != nil {
			return nil, err
		}
		records[id] = r
	}
	return records, nil
}

// modelProfileChanges is what switching to p would change now: the categories whose choice differs from their records
// (profileRecords) as ids, and the launcher fields (<launcher>.<field>). For the profile in use: where the
// configuration has moved away from it.
func modelProfileChanges(p ModelProfile, records map[string]modelRecord, stored []storedLauncher) []string {
	changes := []string{}
	for _, id := range modelCategoryIDs {
		b, ok := p.Purposes[id]
		if !ok {
			continue
		}
		r := records[id] // a copy: the new conversations' profile is cleared on it only
		r.Binding.ConfigDir = ""
		if r.Binding != b {
			changes = append(changes, id)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(p.Launchers)) {
		i := slices.IndexFunc(stored, func(l storedLauncher) bool { return l.Name == name })
		var l *proto.ManagedLauncher
		if i >= 0 {
			l = &stored[i].ManagedLauncher
		}
		m := p.Launchers[name]
		for _, f := range m.fields(l) {
			if *f.set != nil && (l == nil || *f.launcher != **f.set) { // a launcher no longer there: all of it
				changes = append(changes, name+"."+f.name)
			}
		}
	}
	return changes
}

// ---- the view ----

// ModelProfileView is a profile as GET /api/v1/model-profiles shows it.
type ModelProfileView struct {
	ModelProfile
	Active bool `json:"active,omitempty"`
	// Changes: what switching to it would change now (for the one in use: where the configuration moved away from it).
	Changes []string `json:"changes"`
	// Trial: the last 7 days' dry-run counts for a profile not in use (#14702); omitted on the active profile.
	Trial *ModelProfileTrial `json:"trial,omitempty"`
}

// ModelProfilesView is GET /api/v1/model-profiles.
type ModelProfilesView struct {
	Revision  int64               `json:"revision"`
	Active    string              `json:"active"`
	At        *time.Time          `json:"at,omitempty"`
	By        *ProfileActor       `json:"by,omitempty"`
	Rollback  *ModelProfileUndo   `json:"rollback,omitempty"`
	Rollbacks []ModelProfileUndo  `json:"rollbacks,omitempty"` // newest first, up to maxProfileRollbacks
	Profiles  []ModelProfileView  `json:"profiles"`
	Events    []ModelProfileEvent `json:"events"` // newest first
}

// ModelProfileUndo is what a rollback would do: back to profile To ("" none), changing Changes.
type ModelProfileUndo struct {
	ID      string    `json:"id,omitempty"`
	To      string    `json:"to,omitempty"`
	At      time.Time `json:"at"`
	Changes []string  `json:"changes"`
	Redo    bool      `json:"redo,omitempty"`
}

func rollbackUndoPreview(stack []modelProfileImage, targetIdx int) ModelProfileUndo {
	prev := composeRollbackImage(stack, targetIdx)
	changes := make([]string, 0, len(prev.Purposes))
	changes = append(changes, slices.Sorted(maps.Keys(prev.Purposes))...)
	for _, name := range slices.Sorted(maps.Keys(prev.Launchers)) {
		m := prev.Launchers[name]
		for _, f := range m.fields(nil) {
			if *f.set != nil {
				changes = append(changes, name+"."+f.name)
			}
		}
	}
	return ModelProfileUndo{ID: rollbackEntryID(prev), To: prev.Profile, At: prev.At, Changes: changes, Redo: prev.Redo}
}

func (s *Service) modelProfilesView(ctx context.Context) (ModelProfilesView, error) {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	ps, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return ModelProfilesView{}, err
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return ModelProfilesView{}, err
	}
	profiles, err := s.modelProfileViewsLocked(ctx, ps.Profiles, state.Active)
	if err != nil {
		return ModelProfilesView{}, err
	}
	v := ModelProfilesView{Revision: ps.Revision, Active: state.Active, Profiles: profiles, Events: []ModelProfileEvent{}}
	if state.Active != "" {
		at, by := state.At, state.By
		v.At, v.By = &at, &by
	}
	if stack := state.rollbackStack(); len(stack) > 0 {
		v.Rollbacks = make([]ModelProfileUndo, 0, len(stack))
		for i := len(stack) - 1; i >= 0; i-- {
			v.Rollbacks = append(v.Rollbacks, rollbackUndoPreview(stack, i))
		}
		latest := v.Rollbacks[0]
		v.Rollback = &latest
	}
	for i := len(state.Events) - 1; i >= 0 && len(v.Events) < shownProfileEvents; i-- {
		v.Events = append(v.Events, state.Events[i])
	}
	return v, nil
}

// modelProfileViewsLocked compares saved profiles with the live configuration (models.mu held).
func (s *Service) modelProfileViewsLocked(ctx context.Context, profiles []ModelProfile, activeID string) ([]ModelProfileView, error) {
	views := make([]ModelProfileView, 0, len(profiles))
	if len(profiles) == 0 {
		return views, nil
	}
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return nil, err
	}
	ctx = withManagedSnapshot(ctx, stored)
	records, err := s.profileRecords(ctx, profiles)
	if err != nil {
		return nil, err
	}
	normalizedRecords := make(map[string]modelRecord, len(records))
	for _, id := range modelCategoryIDs {
		r, ok := records[id]
		if !ok {
			continue
		}
		if r.Binding.Provider == "" && r.Binding.Model != "" {
			provider, err := s.resolveLegacyModelProvider(ctx, id, nil, records, stored)
			if err != nil {
				return nil, err
			}
			if provider != "" {
				r.Binding.Provider = provider
			}
		}
		normalizedRecords[id] = r
	}
	trials, trialsKnown := s.modelProfileTrialSummaries(ctx, time.Now().UTC())
	for _, p := range profiles {
		p.Purposes, err = s.normalizeProfilePurposes(ctx, p.Purposes, records, stored)
		if err != nil {
			return nil, err
		}
		active := p.ID == activeID
		pv := ModelProfileView{ModelProfile: p, Active: active, Changes: modelProfileChanges(p, normalizedRecords, stored)}
		if !active && trialsKnown {
			t := trials[p.ID]
			pv.Trial = &ModelProfileTrial{Days: modelProfileTrialDays, Dispatches: t.Dispatches, WouldSwitch: t.WouldSwitch}
		}
		views = append(views, pv)
	}
	return views, nil
}

// ---- the routes ----

// profileActor is who sent r: the device the hub names, else the hub's own token.
func profileActor(r *http.Request) ProfileActor {
	if name := strings.TrimSpace(r.Header.Get(assistantwire.DeviceNameHeader)); name != "" {
		return ProfileActor{Kind: "device", Name: name}
	}
	return ProfileActor{Kind: "hub"}
}

func modelProfileDeleteRevision(r *http.Request) (*int64, error) {
	q := r.URL.Query().Get("revision")
	if q == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(q, 10, 64)
	if err != nil || n < 0 {
		return nil, i18n.Err("assistant-models.err_revision_required")
	}
	return &n, nil
}

// modelProfileStatus is the HTTP status of err from the profiles' functions.
func modelProfileStatus(err error) int {
	var m *i18n.Message
	switch {
	case errors.Is(err, errModelProfileAtomicStore):
		return http.StatusInternalServerError
	case errors.Is(err, errModelProfileNotFound):
		return http.StatusNotFound
	case errors.Is(err, errModelProfileConflict), errors.Is(err, errModelProfileNoRollback):
		return http.StatusConflict
	case errors.As(err, &m) && (strings.HasPrefix(m.Key, "assistant-model_profiles.") ||
		m.Key == "assistant-models.err_permission_mode_launcher_only" ||
		m.Key == "assistant-models.err_permission_mode_unsupported"):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// modelProfileInput is PUT /api/v1/model-profiles/{id}: the whole profile, or (capture) the configuration as it is now
// with the tiers kept. Unknown optional fields from newer clients are ignored during decoding while known fields are
// strictly type-checked and validated and credential/endpoint fields are rejected.
type modelProfileInput struct {
	Revision    *int64                    `json:"revision"`
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	Purposes    map[string]ModelBinding   `json:"purposes"`
	Launchers   map[string]LauncherModels `json:"launchers"`
	Tiers       map[string]ModelTier      `json:"tiers"`
	Capture     bool                      `json:"capture"`
}

// modelProfileRollbackInput is the optional body of POST /api/v1/model-profiles/rollback.
type modelProfileRollbackInput struct {
	ID string `json:"id"`
}

// forbiddenProfileFields are credential, secret, and endpoint keys that model profiles never accept
// (docs/assistant/model-profiles.md).
var forbiddenProfileFields = map[string]bool{
	"api_key":        true,
	"key":            true,
	"key_sealed":     true,
	"headers":        true,
	"headers_sealed": true,
	"env":            true,
	"env_sealed":     true,
	"base_url":       true,
	"token":          true,
	"secret":         true,
	"password":       true,
	"auth":           true,
	"proxy":          true,
	"no_proxy":       true,
}

func checkForbiddenObjectKeys(obj map[string]json.RawMessage) error {
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if forbiddenProfileFields[strings.ToLower(k)] {
			return i18n.Err("assistant-model_profiles.err_forbidden_field", i18n.Args{"field": k})
		}
	}
	return nil
}

func checkForbiddenProfilePayload(top map[string]json.RawMessage) error {
	if err := checkForbiddenObjectKeys(top); err != nil {
		return err
	}
	for _, key := range slices.Sorted(maps.Keys(top)) {
		if !strings.EqualFold(key, "purposes") && !strings.EqualFold(key, "launchers") && !strings.EqualFold(key, "tiers") {
			continue
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(top[key], &entries); err != nil || entries == nil {
			continue // non-object shapes fail type decoding cleanly in json.Unmarshal
		}
		for _, entryKey := range slices.Sorted(maps.Keys(entries)) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(entries[entryKey], &fields); err != nil || fields == nil {
				continue
			}
			if err := checkForbiddenObjectKeys(fields); err != nil {
				return err
			}
		}
	}
	return nil
}

// decodeSingleJSONObject reads at most limit bytes and decodes exactly one JSON object into dst, ignoring unknown
// optional fields while rejecting non-object values, malformed JSON, and trailing tokens.
func decodeSingleJSONObject(w http.ResponseWriter, r *http.Request, dst any, limit int64, optional bool, checkObject func(map[string]json.RawMessage) error) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return err
	}
	if optional && len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var top map[string]json.RawMessage
	if err := dec.Decode(&top); err != nil {
		return err
	}
	if top == nil {
		return errors.New("request body must be a JSON object")
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("unexpected trailing data after JSON body")
	}
	if checkObject != nil {
		if err := checkObject(top); err != nil {
			return err
		}
	}
	return json.Unmarshal(raw, dst)
}

// mergeTierInput is the tiers a PUT stores, given the profile's own (was). A tier the body sends without candidates
// is in an older client's shape, which knows only the flat fields: they replace its first candidate, and the other
// candidates and the aliases stay as they were, also when that client renamed the tier (renamedTiers). A tier with
// candidates, or with aliases, says them itself (an empty list clears them). Omitting the tier host keeps its stored
// value. A tier the body leaves out is removed, as
// it always was. An alias kept for a client that did not send it gives way to a tier name or an alias the body did
// send, and to one another tier kept first (in name order): that client cannot see it, so it could not fix the
// refusal. An alias the body sends itself is checked as it is (checkModelTiers).
func mergeTierInput(was, in map[string]ModelTier) map[string]ModelTier {
	if in == nil {
		return nil
	}
	from := renamedTiers(was, in)
	out := make(map[string]ModelTier, len(in))
	var kept []string // tiers whose aliases came from was, not from the body
	taken := map[string]bool{}
	for name, t := range in {
		taken[name] = true
		old, ok := was[name]
		if f, renamed := from[name]; renamed {
			old, ok = was[f], true
		}
		if ok && t.Candidates == nil {
			first := t.first()
			t.Candidates = slices.Clone(old.Candidates)
			t.setFirst(first)
		}
		if ok && t.Host == nil {
			t.Host = old.Host
		}
		if t.Aliases == nil {
			if ok {
				t.Aliases = slices.Clone(old.Aliases)
			}
			kept = append(kept, name)
		} else {
			for _, a := range t.Aliases {
				taken[strings.TrimSpace(a)] = true
			}
		}
		t.normalize()
		out[name] = t
	}
	slices.Sort(kept)
	for _, name := range kept {
		t := out[name]
		var aliases []string
		for _, a := range t.Aliases {
			if !taken[a] {
				aliases, taken[a] = append(aliases, a), true
			}
		}
		t.Aliases = aliases
		out[name] = t
	}
	return out
}

// renamedTiers is the stored name of each tier an older client renamed, by its new name. The older shape carries no
// earlier name, so a tier the body adds in it (a name was does not have, no candidates, no aliases) pairs with a
// stored tier the body no longer sends: the one whose first candidate and description it repeats unchanged, else,
// when exactly one tier went and one came, that one if both say the same non-empty description. Anything else stays
// unpaired, a new tier as before: deleting a tier and adding an unrelated one on the same launcher in one save must
// not hand the new one the old one's hidden candidates and aliases.
func renamedTiers(was, in map[string]ModelTier) map[string]string {
	var gone, added []string
	for _, name := range slices.Sorted(maps.Keys(was)) {
		if _, ok := in[name]; !ok {
			gone = append(gone, name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(in)) {
		if t := in[name]; t.Candidates == nil && t.Aliases == nil {
			if _, ok := was[name]; !ok {
				added = append(added, name)
			}
		}
	}
	about := func(t ModelTier) string { return strings.TrimSpace(t.Description) }
	pairs, used := map[string]string{}, map[string]bool{}
	for _, a := range added {
		var match []string
		for _, g := range gone {
			if !used[g] && was[g].first() == in[a].first() && about(was[g]) == about(in[a]) {
				match = append(match, g)
			}
		}
		if len(match) == 1 {
			pairs[a], used[match[0]] = match[0], true
		}
	}
	gone = slices.DeleteFunc(gone, func(g string) bool { return used[g] })
	added = slices.DeleteFunc(added, func(a string) bool { _, ok := pairs[a]; return ok })
	if len(gone) == 1 && len(added) == 1 {
		if d := about(in[added[0]]); d != "" && d == about(was[gone[0]]) {
			pairs[added[0]] = gone[0]
		}
	}
	return pairs
}

func (s *Service) modelProfileRoutes() {
	view := func(w http.ResponseWriter, r *http.Request) {
		v, err := s.modelProfilesView(r.Context())
		if err != nil {
			apierr.Write(w, modelProfileStatus(err), err)
			return
		}
		apierr.WriteJSON(w, v)
	}
	full := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !fullRole(r) {
				apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-model_profiles.err_role"))
				return
			}
			fn(w, r)
		}
	}
	s.handle("GET /api/v1/model-profiles", view)
	s.handle("GET /api/v1/model-profiles/{id}/candidates", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.modelProfileCandidates(r.Context(), r.PathValue("id"))
		if err != nil {
			apierr.Write(w, modelProfileStatus(err), err)
			return
		}
		apierr.WriteJSON(w, v)
	})
	s.handle("PUT /api/v1/model-profiles/{id}", full(func(w http.ResponseWriter, r *http.Request) {
		var in modelProfileInput
		if err := decodeSingleJSONObject(w, r, &in, 64<<10, false, checkForbiddenProfilePayload); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if in.Revision == nil || *in.Revision < 0 {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-models.err_revision_required"))
			return
		}
		ctx := r.Context()
		_, err := s.saveModelProfile(ctx, in.Revision, r.PathValue("id"), profileActor(r), func(p *ModelProfile, exists bool) error {
			p.Name, p.Description = in.Name, in.Description
			if !in.Capture {
				p.Purposes, p.Launchers, p.Tiers = in.Purposes, in.Launchers, mergeTierInput(p.Tiers, in.Tiers)
				return nil
			}
			purposes, launchers, tiers, err := s.captureModels(ctx)
			if err != nil {
				return err
			}
			p.Purposes, p.Launchers = purposes, launchers
			if in.Tiers != nil {
				p.Tiers = mergeTierInput(p.Tiers, in.Tiers)
			} else if !exists {
				p.Tiers = tiers
			}
			return nil
		})
		if err != nil {
			apierr.Write(w, modelProfileStatus(err), err)
			return
		}
		view(w, r)
	}))
	s.handle("DELETE /api/v1/model-profiles/{id}", full(func(w http.ResponseWriter, r *http.Request) {
		revision, err := modelProfileDeleteRevision(r)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err := s.deleteModelProfile(r.Context(), revision, r.PathValue("id"), profileActor(r)); err != nil {
			apierr.Write(w, modelProfileStatus(err), err)
			return
		}
		view(w, r)
	}))
	s.handle("DELETE /api/v1/model-profiles", full(func(w http.ResponseWriter, r *http.Request) {
		revision, err := modelProfileDeleteRevision(r)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err := s.clearModelProfiles(r.Context(), revision, profileActor(r)); err != nil {
			apierr.Write(w, modelProfileStatus(err), err)
			return
		}
		view(w, r)
	}))
	s.handle("POST /api/v1/model-profiles/deactivate", full(func(w http.ResponseWriter, r *http.Request) {
		if err := s.deactivateModelProfile(r.Context(), profileActor(r)); err != nil {
			apierr.Write(w, modelProfileStatus(err), err)
			return
		}
		view(w, r)
	}))
	s.handle("POST /api/v1/model-profiles/{id}/apply", full(func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.applyModelProfile(r.Context(), r.PathValue("id"), profileActor(r)); err != nil {
			apierr.Write(w, modelProfileStatus(err), err)
			return
		}
		view(w, r)
	}))
	s.handle("POST /api/v1/model-profiles/rollback", full(func(w http.ResponseWriter, r *http.Request) {
		var in modelProfileRollbackInput
		if err := decodeSingleJSONObject(w, r, &in, 4096, true, nil); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if _, err := s.rollbackModelProfileTo(r.Context(), strings.TrimSpace(in.ID), profileActor(r)); err != nil {
			apierr.Write(w, modelProfileStatus(err), err)
			return
		}
		view(w, r)
	}))
}
