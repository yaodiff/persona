package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Picking a tier's candidate (#14700, docs/assistant/model-profiles.md "Candidates and aliases"). A tier (an Agent
// preset) lists one or more ordered candidates; every start on a tier goes through pickTierCandidate, so the plan
// card's preview, a task's start, agent_start and conversation_new all take the same one:
//
//   - the tier is found by its name, else by an alias (strong still finds core);
//   - one candidate is returned as it is: nothing is read, and the start runs exactly as it did before candidates;
//   - with several, a candidate whose launcher this computer lacks or whose latest health check failed is passed over,
//     and the first usable one in the user's order is taken; with none usable, the first, so the start's own refusal
//     or health detour says why as it always did.
//
// Nothing outside the tier's list is ever chosen. Among the usable candidates the scorer (candidate_score.go, #14701)
// picks by quota headroom and measured efficiency, keeping the user's order on a tie; tierRank, when set, replaces it.
// A continuation or retry keeps the candidate its previous Agent ran on (candidate_keep.go) unless a clear problem
// re-picks it.

const (
	tierReasonOnly        = "only"        // the tier lists one candidate
	tierReasonFirst       = "first"       // the first candidate, usable (or none was)
	tierReasonUnavailable = "unavailable" // an earlier candidate cannot start on this computer now
)

// tierFacts are what a pick knows of the start.
type tierFacts struct {
	Machine string        // the computer the start is for ("" unknown: availability is then not judged on a list)
	Profile *ModelProfile // nil: the profile in use; a trial (#14702) passes one that is not
	// Keep is the previous Agent's dispatch of the same task and what the re-pick rule reads (nil: a first start).
	Keep *tierKeep
}

// tierPick is the candidate chosen for a start on a tier, and why.
type tierPick struct {
	Profile   string    // the profile's id
	Active    bool      // the profile is the one in use (a trial's is not)
	Name      string    // the tier's own name (an alias resolved)
	Tier      ModelTier // the chosen candidate as a one-candidate tier, with the tier's description
	Candidate int       // its position, 0 the first
	Of        int       // how many the tier lists
	Reason    string    // tierReasonOnly, tierReasonFirst, tierReasonUnavailable, or the ranker's
	Checks    []tierCandidateCheck
	// Scores is every candidate's score, headroom and efficiency when the scorer chose (several candidates only).
	Scores []candidateScore
	// Args are the reason's arguments beyond the passed-over list: the kept candidate, the rule that switched.
	Args map[string]any
}

// tierCandidateCheck is one candidate as the pick judged it (several candidates only).
type tierCandidateCheck struct {
	Index     int    `json:"index"`
	Launcher  string `json:"launcher"`
	Model     string `json:"model,omitempty"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// tierRankInput is what a ranker is given: the tier, the computer, and every candidate as judged, the usable ones
// (Available) in the user's order.
type tierRankInput struct {
	Profile string
	Active  bool
	Tier    string
	Machine string
	List    []TierCandidate
	Checks  []tierCandidateCheck
}

// tierRanker picks among the usable candidates: the index of one of them with its reason, or false to keep the
// user's order. An index that is not a usable candidate is ignored.
type tierRanker func(ctx context.Context, in tierRankInput) (index int, reason string, ok bool)

// pickTierCandidate is the candidate a start on tier runs on, in the profile in use (or facts.Profile).
func (s *Service) pickTierCandidate(ctx context.Context, tier string, facts tierFacts) (tierPick, error) {
	p, active := facts.Profile, false
	if p == nil {
		var id string
		var err error
		p, id, err = s.activeModelProfile(ctx)
		if err != nil {
			return tierPick{}, err
		}
		if p == nil && id != "" {
			return tierPick{}, i18n.ErrEnglish("assistant-model_profiles.err_active_profile_missing", i18n.Args{"profile": i18n.Verbatim(id)})
		}
		if p == nil {
			return tierPick{}, i18n.ErrEnglish("assistant-model_profiles.err_no_active_profile")
		}
		active = true
	}
	return s.pickTierCandidateKeeping(ctx, p, active, tier, facts.Machine, facts.Keep)
}

// pickTierCandidateIn is pickTierCandidate in profile p, which is the one in use when active.
func (s *Service) pickTierCandidateIn(ctx context.Context, p *ModelProfile, active bool, tier, machine string) (tierPick, error) {
	return s.pickTierCandidateKeeping(ctx, p, active, tier, machine, nil)
}

// pickTierCandidateKeeping is pickTierCandidateIn for a start that may keep its previous Agent's candidate (keep).
func (s *Service) pickTierCandidateKeeping(ctx context.Context, p *ModelProfile, active bool, tier, machine string, keep *tierKeep) (tierPick, error) {
	name, ok := p.lookupTier(tier)
	if !ok {
		return tierPick{}, i18n.ErrEnglish("assistant-model_profiles.err_tier_not_found", i18n.Args{
			"profile": i18n.Verbatim(cmp.Or(p.Name, p.ID)), "tier": i18n.Verbatim(strings.TrimSpace(tier)),
			"tiers": i18n.Verbatim(strings.Join(slices.Sorted(maps.Keys(p.Tiers)), ", ")),
		})
	}
	t := p.Tiers[name]
	list := s.reprojectCandidates(ctx, t.list())
	pick := tierPick{Profile: p.ID, Active: active, Name: name, Of: len(list), Reason: tierReasonOnly}
	if !s.keepTierCandidate(ctx, p, machine, keep, &pick, list) && len(list) > 1 {
		s.chooseTierCandidate(ctx, machine, &pick, list, -1)
	}
	c := list[pick.Candidate]
	if proto.IsExternalLauncher(c.Launcher) {
		return tierPick{}, i18n.Err("external-adapter.purpose_unsupported")
	}
	pick.Tier = t.on(c)
	return pick, nil
}

// chooseTierCandidate judges every candidate of a tier with several on machine and takes the first usable one in
// order (the scorer, or tierRank when set, may take another usable one); with none usable, the first. exclude (-1:
// none) is a candidate a continuation switched away from, judged unusable for this pick.
func (s *Service) chooseTierCandidate(ctx context.Context, machine string, pick *tierPick, list []TierCandidate, exclude int) {
	avail := s.availabilityOn(machine)
	usable := -1
	for i, c := range list {
		check := tierCandidateCheck{Index: i, Launcher: c.Launcher, Model: c.Model}
		if proto.IsExternalLauncher(c.Launcher) {
			check.Reason = "no Agent starts on " + c.Launcher
		} else if i == exclude {
			check.Reason = "the previous Agent's candidate, switched away from"
		} else {
			check.Available, check.Reason, _ = avail.check(ctx, c.Launcher)
		}
		pick.Checks = append(pick.Checks, check)
		if check.Available && usable < 0 {
			usable = i
		}
	}
	switch {
	case usable < 0:
		pick.Candidate, pick.Reason = 0, tierReasonFirst
		return
	case usable == 0:
		pick.Candidate, pick.Reason = 0, tierReasonFirst
	default:
		pick.Candidate, pick.Reason = usable, tierReasonUnavailable
	}
	in := tierRankInput{Profile: pick.Profile, Active: pick.Active, Tier: pick.Name, Machine: machine, List: list, Checks: pick.Checks}
	if s.tierRank == nil {
		choice := s.scoreTier(ctx, in)
		pick.Scores = choice.Scores
		if i := choice.Index; i >= 0 && i < len(list) && pick.Checks[i].Available {
			pick.Candidate, pick.Reason = i, choice.Reason
		}
		return
	}
	if i, reason, ok := s.tierRank(ctx, in); ok && i >= 0 && i < len(list) && pick.Checks[i].Available && reason != "" {
		pick.Candidate, pick.Reason = i, reason
	}
}

// ---- a start's pick, carried through resolveStart ----

type tierStartKey struct{}

// tierStart carries a start's computer into applyStartTier and its pick back out, so resolveStart's signature stays
// as every caller has it.
type tierStart struct {
	machine string
	keep    *tierKeep // the previous Agent's candidate a continuation or retry keeps (candidate_keep.go)
	pick    *tierPick // the tier's pick when the tier gave the start its launcher; nil otherwise
}

func withTierStart(ctx context.Context, machine string) (context.Context, *tierStart) {
	slot := &tierStart{machine: machine}
	return context.WithValue(ctx, tierStartKey{}, slot), slot
}

func tierStartOf(ctx context.Context) *tierStart {
	slot, _ := ctx.Value(tierStartKey{}).(*tierStart)
	return slot
}

// ---- what a dispatch records ----

// TaskDispatch is the hub's record of how a plan task's tier was resolved at its latest start (PlanTask.Dispatch): the
// tier, the candidate taken of how many, what it runs, and the reason code the clients render. Hub-owned: a tool call
// never sets it.
type TaskDispatch struct {
	Tier      string         `json:"tier"`
	Candidate int            `json:"candidate"`
	Of        int            `json:"of"`
	Launcher  string         `json:"launcher"`
	Model     string         `json:"model,omitempty"`
	Reason    string         `json:"reason"`
	Args      map[string]any `json:"args,omitempty"`
	// Failed counts this candidate's failed starts in a row for the task (a continuation re-picks at three).
	Failed int `json:"failed,omitempty"`
}

// dispatch is the pick as a plan task records it.
func (p tierPick) dispatch() *TaskDispatch {
	d := &TaskDispatch{Tier: p.Name, Candidate: p.Candidate, Of: p.Of, Launcher: p.Tier.Launcher, Model: p.Tier.Model, Reason: p.Reason}
	if p.Reason == tierReasonUnavailable || p.Reason == dispatchQuota {
		var passed []string
		for _, c := range p.Checks[:p.Candidate] {
			passed = append(passed, c.Launcher)
		}
		d.Args = map[string]any{"passed_over": passed}
	}
	if len(p.Args) > 0 || len(p.Scores) > 0 && p.Reason != tierReasonFirst {
		if d.Args == nil {
			d.Args = map[string]any{}
		}
		for k, v := range p.Args {
			d.Args[k] = v
		}
		if len(p.Scores) > 0 {
			d.Args["candidates"] = p.Scores
		}
	}
	return d
}

// modelRoutingDetail is a model_routing row's detail: every candidate as the pick judged it.
type modelRoutingDetail struct {
	Machine    string               `json:"machine,omitempty"`
	Candidates []tierCandidateCheck `json:"candidates,omitempty"`
	// Scores is every candidate's score, headroom and efficiency when the scorer chose (#14701).
	Scores []candidateScore `json:"scores,omitempty"`
	// Args are the dispatch reason's arguments (kept, switched).
	Args map[string]any `json:"args,omitempty"`
}

// modelRoutingOf is one row of model_routing for pick: where the start went (todo, plan task, session) is the caller's.
func modelRoutingOf(pick tierPick, machine string) db.ModelRoutingRow {
	raw, _ := json.Marshal(modelRoutingDetail{Machine: machine, Candidates: pick.Checks, Scores: pick.Scores, Args: pick.Args})
	return db.ModelRoutingRow{At: time.Now().UTC(), ProfileID: pick.Profile, Active: pick.Active, Tier: pick.Name,
		Candidate: pick.Candidate, Of: pick.Of, Launcher: pick.Tier.Launcher, Model: pick.Tier.Model, Effort: pick.Tier.Effort,
		Reason: pick.Reason, Detail: raw}
}

// recordModelRouting writes one decision to model_routing, on its own time: a dispatch never fails for its record.
// For an active dispatch it then trials every inactive profile on its own goroutine and budget (#14702), so the
// dispatch never waits for the trial.
func (s *Service) recordModelRouting(ctx context.Context, row db.ModelRoutingRow) {
	st := s.store()
	if st == nil {
		return
	}
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := st.PutModelRouting(write, row); err != nil {
		s.logf("model routing %s/%s: %v", row.ProfileID, row.Tier, err)
	}
	if row.Active {
		startTrial(func() {
			trial, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer done()
			s.recordTrialRoutings(trial, row)
		})
	}
}
