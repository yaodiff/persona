package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// Dry-run trial of inactive model profiles at every dispatch, and per-candidate status for a profile (#14702,
// docs/assistant/model-profiles.md).

const modelProfileTrialDays = 7

// startTrial runs a dispatch's trial off the dispatch path. Tests make it synchronous.
var startTrial = func(f func()) { go f() }

// ModelProfileTrial is the last 7 days' dry-run summary shown on each inactive profile in GET /api/v1/model-profiles.
type ModelProfileTrial struct {
	Days        int `json:"days"`
	Dispatches  int `json:"dispatches"`
	WouldSwitch int `json:"would_switch"`
}

// ModelProfileCandidatesView is GET /api/v1/model-profiles/{id}/candidates.
type ModelProfileCandidatesView struct {
	Days  int                               `json:"days"`
	Tiers map[string][]ModelCandidateStatus `json:"tiers"`
}

// ModelCandidateStatus is one candidate's current quota headroom and 14-day measured efficiency.
type ModelCandidateStatus struct {
	Index    int                    `json:"index"`
	Launcher string                 `json:"launcher"`
	Model    string                 `json:"model,omitempty"`
	Headroom ModelCandidateHeadroom `json:"headroom"`
	Stats    ModelCandidateStats    `json:"stats"`
}

// ModelCandidateStats is a candidate's last 14 days: minutes_p50 and first_pass are present exactly when n > 0, so a
// measured 0 is still sent.
type ModelCandidateStats struct {
	N          int      `json:"n"`
	MinutesP50 *float64 `json:"minutes_p50,omitempty"`
	FirstPass  *float64 `json:"first_pass,omitempty"`
}

func candidateStatsView(c candidateStats) ModelCandidateStats {
	v := ModelCandidateStats{N: c.N}
	if c.N > 0 {
		minutes, firstPass := c.MinutesP50, c.FirstPass
		v.MinutesP50, v.FirstPass = &minutes, &firstPass
	}
	return v
}

// ModelCandidateHeadroom is the contract shape for a candidate's headroom on GET /api/v1/model-profiles/{id}/candidates.
type ModelCandidateHeadroom struct {
	Known      bool       `json:"known"`
	Percent    float64    `json:"percent"`
	Stale      bool       `json:"stale"`
	RecoversAt *time.Time `json:"recovers_at,omitempty"`
}

// modelRoutingTrialStore reads per-profile trial summaries (internal/db model_routing_trial.go).
type modelRoutingTrialStore interface {
	ModelRoutingTrialSummaries(ctx context.Context, since time.Time) ([]db.ModelRoutingTrialSummary, error)
}

var _ modelRoutingTrialStore = (*db.DB)(nil)

// recordTrialRoutings evaluates every inactive profile (at most maxModelProfiles, in stable ID order) for the same
// tier as activeRow, without starting anything, and writes one active=false model_routing row per profile that has the
// tier: by its name or one of the active tier's aliases, each matched against the profile's names and aliases. It runs
// within its own bounded context and logs any error or panic so a trial can never fail or alter the real dispatch.
func (s *Service) recordTrialRoutings(ctx context.Context, activeRow db.ModelRoutingRow) {
	defer func() {
		if r := recover(); r != nil {
			s.logf("model routing trial panic (%s): %v", activeRow.Tier, r)
		}
	}()
	if ctx.Err() != nil || activeRow.Tier == "" {
		return
	}
	st := s.store()
	if st == nil {
		return
	}
	ps, ok, err := s.readModelProfiles(ctx)
	if err != nil {
		s.logf("model routing trial profiles: %v", err)
		return
	}
	if !ok || len(ps.Profiles) == 0 {
		return
	}
	var detail modelRoutingDetail
	if len(activeRow.Detail) > 0 {
		_ = json.Unmarshal(activeRow.Detail, &detail)
	}
	machine := detail.Machine
	names := []string{activeRow.Tier}
	inactive := make([]ModelProfile, 0, len(ps.Profiles))
	for _, p := range ps.Profiles {
		if p.ID == activeRow.ProfileID {
			if t, ok := p.Tiers[activeRow.Tier]; ok {
				names = append(names, t.Aliases...)
			}
			continue
		}
		if p.ID == "" {
			continue
		}
		inactive = append(inactive, p)
	}
	slices.SortFunc(inactive, func(a, b ModelProfile) int { return cmp.Compare(a.ID, b.ID) })
	if len(inactive) > maxModelProfiles {
		inactive = inactive[:maxModelProfiles]
	}
	for i := range inactive {
		if ctx.Err() != nil {
			return
		}
		prof := inactive[i]
		tier := ""
		for _, name := range names {
			if own, found := prof.lookupTier(name); found {
				tier = own
				break
			}
		}
		if tier == "" {
			continue
		}
		pick, err := s.pickTierCandidateIn(ctx, &prof, false, tier, machine)
		if err != nil {
			s.logf("model routing trial %s/%s: %v", prof.ID, tier, err)
			continue
		}
		trial := modelRoutingOf(pick, machine)
		trial.At = activeRow.At
		trial.TodoID = activeRow.TodoID
		trial.PlanID = activeRow.PlanID
		trial.TaskIndex = activeRow.TaskIndex
		trial.SessionID = activeRow.SessionID
		trial.Active = false
		if err := st.PutModelRouting(ctx, trial); err != nil {
			s.logf("model routing trial write %s/%s: %v", trial.ProfileID, trial.Tier, err)
		}
	}
}

// modelProfileTrialSummaries returns the last 7 days' trial counts by profile id, and false when the store cannot say
// (then no profile shows a trial rather than a wrong 0).
func (s *Service) modelProfileTrialSummaries(ctx context.Context, now time.Time) (map[string]db.ModelRoutingTrialSummary, bool) {
	store, ok := s.store().(modelRoutingTrialStore)
	if !ok {
		return nil, false
	}
	rows, err := store.ModelRoutingTrialSummaries(ctx, now.AddDate(0, 0, -modelProfileTrialDays))
	if err != nil {
		s.logf("model routing trial summaries: %v", err)
		return nil, false
	}
	out := make(map[string]db.ModelRoutingTrialSummary, len(rows))
	for _, r := range rows {
		out[r.ProfileID] = r
	}
	return out, true
}

// modelProfileCandidates builds GET /api/v1/model-profiles/{id}/candidates: per tier, per candidate in 0-based order,
// current pace-based headroom and 14-day measured efficiency. Returns errModelProfileNotFound for an unknown id.
func (s *Service) modelProfileCandidates(ctx context.Context, id string) (ModelProfileCandidatesView, error) {
	s.models.mu.Lock()
	ps, err := s.modelProfilesLocked(ctx)
	s.models.mu.Unlock()
	if err != nil {
		return ModelProfileCandidatesView{}, err
	}
	idx := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == id })
	if idx < 0 {
		return ModelProfileCandidatesView{}, errModelProfileNotFound
	}
	tiersCopy := maps.Clone(ps.Profiles[idx].Tiers)
	now := time.Now().UTC()
	accounts := s.usageAccounts(ctx)
	machine := s.settings(ctx).MachineID
	view := ModelProfileCandidatesView{
		Days:  candidateStatsDays,
		Tiers: make(map[string][]ModelCandidateStatus, len(tiersCopy)),
	}
	for _, tierName := range slices.Sorted(maps.Keys(tiersCopy)) {
		t := tiersCopy[tierName]
		list := t.list()
		stats := s.candidateTierStats(ctx, tierName, now)
		items := make([]ModelCandidateStatus, len(list))
		for i, c := range list {
			h := s.candidateHeadroomAt(ctx, accounts, machine, c.Launcher, c.Model, now)
			items[i] = ModelCandidateStatus{
				Index:    i,
				Launcher: c.Launcher,
				Model:    c.Model,
				Headroom: ModelCandidateHeadroom{
					Known:      h.Known,
					Percent:    roundScore(h.Percent),
					Stale:      h.Stale,
					RecoversAt: h.RecoversAt,
				},
				Stats: candidateStatsView(stats.By[candidateStatsKey{c.Launcher, c.Model}]),
			}
		}
		view.Tiers[tierName] = items
	}
	return view, nil
}
