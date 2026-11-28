package assistant

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// Choosing among a tier's ordered candidates (docs/assistant/model-profiles.md "Choosing a candidate"). Each usable
// candidate gets a score, lower is better:
//
//	Score = T × (1 + β(1 − FP)) + κ·T/√(n+1) + μ/(1 + 10·H)
//
// T is the wall-clock minutes from dispatch to acceptance (p50) and FP the first-pass rate (accepted with no rework
// review), both over the last 14 days of the same tier on the same launcher + model, shrunk toward the tier's pooled
// prior with five samples' weight; a tier with no samples at all uses 30 minutes and 0.5. n is the candidate's own
// sample count (fewer samples cost more once it has been tried; an untried one carries no κ term, shortagePenalty),
// H its pace-based headroom (candidate_headroom.go; unknown adds nothing).
// A candidate with known H ≤ 0 is passed over unless every usable one is (then the first usable, over_pace). Scores
// within one point go to the earlier candidate, so with no data and room everywhere the first candidate is chosen,
// exactly as with one candidate.

const (
	scoreBeta         = 1.0
	scoreKappa        = 0.5
	scoreMu           = 30.0
	scorePriorWeight  = 5.0
	scorePriorMinutes = 30.0
	scorePriorPass    = 0.5
	scoreTie          = 1.0

	// candidateStatsDays is how far back the efficiency samples go; candidateStatsLimit bounds one tier's read.
	candidateStatsDays  = 14
	candidateStatsLimit = 2000
)

// Dispatch reason codes (PlanTask.dispatch.reason, the contract shared with the clients).
const (
	dispatchOnly        = "only"
	dispatchFirst       = "first"
	dispatchScore       = "score"
	dispatchQuota       = "quota"
	dispatchUnavailable = "unavailable"
	dispatchKept        = "kept"
	dispatchSwitched    = "switched"
	dispatchOverPace    = "over_pace"
)

// candidateStats is one (tier, launcher, model)'s measured efficiency.
type candidateStats struct {
	N          int     `json:"n"`
	MinutesP50 float64 `json:"minutes_p50,omitempty"`
	FirstPass  float64 `json:"first_pass,omitempty"`
}

// candidateStatsKey is a launcher + model within one tier.
type candidateStatsKey struct{ Launcher, Model string }

// tierStats is one tier's efficiency: each launcher + model's own, and the pooled prior of the whole tier.
type tierStats struct {
	By    map[candidateStatsKey]candidateStats
	Prior candidateStats
}

// tierStatsOf summarizes samples of one tier. Pure.
func tierStatsOf(samples []db.ModelRoutingSample) tierStats {
	out := tierStats{By: map[candidateStatsKey]candidateStats{}}
	minutes := map[candidateStatsKey][]float64{}
	passed := map[candidateStatsKey]int{}
	var all []float64
	allPassed := 0
	for _, s := range samples {
		if s.ActualMinutes <= 0 {
			continue
		}
		k := candidateStatsKey{s.Launcher, s.Model}
		m := float64(s.ActualMinutes)
		minutes[k] = append(minutes[k], m)
		all = append(all, m)
		if s.FirstPass {
			passed[k]++
			allPassed++
		}
	}
	for k, ms := range minutes {
		out.By[k] = candidateStats{N: len(ms), MinutesP50: median(ms), FirstPass: float64(passed[k]) / float64(len(ms))}
	}
	if len(all) > 0 {
		out.Prior = candidateStats{N: len(all), MinutesP50: median(all), FirstPass: float64(allPassed) / float64(len(all))}
	}
	return out
}

// median is the middle value (the mean of the two middle ones for an even count); 0 for none. It sorts a copy.
func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// scoreInput is one candidate as the chooser sees it, in the tier's order.
type scoreInput struct {
	Index    int
	Launcher string
	Model    string
	// Usable: the launcher is on this computer and its health check does not rule it out.
	Usable   bool
	Headroom candidateHeadroom
	Stats    candidateStats
}

// candidateScore is one candidate's line in the dispatch detail (model_routing, PlanTask.dispatch args).
type candidateScore struct {
	Index     int               `json:"index"`
	Launcher  string            `json:"launcher"`
	Model     string            `json:"model,omitempty"`
	Score     *float64          `json:"score,omitempty"`
	Minutes   float64           `json:"minutes,omitempty"`    // shrunk T
	FirstPass float64           `json:"first_pass,omitempty"` // shrunk FP
	Stats     candidateStats    `json:"stats"`
	Headroom  candidateHeadroom `json:"headroom"`
	// Skip is why it was passed over: unavailable (not on this computer or unhealthy) or quota (no headroom).
	Skip string `json:"skip,omitempty"`
}

// candidateChoice is the chooser's answer: the chosen index (-1 when none is usable), the reason code and every
// candidate's line.
type candidateChoice struct {
	Index  int              `json:"index"`
	Reason string           `json:"reason"`
	Scores []candidateScore `json:"scores"`
}

// shortagePenalty is the κ term: fewer samples cost more, κ·T/√(n+1), once a candidate has been tried. An untried
// candidate (n = 0) carries none (#17011): its T and FP are already the tier's pooled values, and charging it the
// full κ·T while a tried one pays κ·T/√(n+1) put it behind as soon as any other candidate had one sample; it then
// never ran, never got a sample, and stayed locked out. So a candidate the owner listed is tried once before the
// samples decide.
func shortagePenalty(n int, t float64) float64 {
	if n <= 0 {
		return 0
	}
	return scoreKappa * t / math.Sqrt(float64(n)+1)
}

// shrink pulls an observed value toward the prior with scorePriorWeight samples' weight.
func shrink(n int, observed, prior float64) float64 {
	return (float64(n)*observed + scorePriorWeight*prior) / (float64(n) + scorePriorWeight)
}

// scoreCandidates chooses among in (the tier's candidates in order) by prior. Pure. One candidate is the caller's to
// short-circuit (reason only); here it is scored like any other.
func scoreCandidates(in []scoreInput, prior candidateStats) candidateChoice {
	tPrior, fpPrior := scorePriorMinutes, scorePriorPass
	if prior.N > 0 {
		tPrior, fpPrior = prior.MinutesP50, prior.FirstPass
	}
	out := candidateChoice{Index: -1, Scores: make([]candidateScore, len(in))}
	firstUsable := -1
	var eligible []int
	for i, c := range in {
		line := candidateScore{Index: c.Index, Launcher: c.Launcher, Model: c.Model, Stats: c.Stats, Headroom: c.Headroom}
		switch {
		case !c.Usable:
			line.Skip = dispatchUnavailable
		default:
			if firstUsable < 0 {
				firstUsable = i
			}
			if c.Headroom.Known && c.Headroom.H <= 0 {
				line.Skip = dispatchQuota
				break
			}
			t := shrink(c.Stats.N, c.Stats.MinutesP50, tPrior)
			fp := shrink(c.Stats.N, c.Stats.FirstPass, fpPrior)
			score := t*(1+scoreBeta*(1-fp)) + shortagePenalty(c.Stats.N, t)
			if c.Headroom.Known {
				score += scoreMu / (1 + 10*c.Headroom.H)
			}
			score = math.Round(score*100) / 100
			line.Score, line.Minutes, line.FirstPass = &score, roundScore(t), roundScore(fp)
			eligible = append(eligible, i)
		}
		out.Scores[i] = line
	}
	if firstUsable < 0 {
		out.Reason = dispatchUnavailable
		return out
	}
	if len(eligible) == 0 {
		out.Index, out.Reason = in[firstUsable].Index, dispatchOverPace
		return out
	}
	least := math.Inf(1)
	for _, i := range eligible {
		least = math.Min(least, *out.Scores[i].Score)
	}
	chosen := eligible[0]
	for _, i := range eligible {
		if *out.Scores[i].Score-least < scoreTie {
			chosen = i
			break
		}
	}
	out.Index = in[chosen].Index
	out.Reason = choiceReason(out.Scores, chosen, eligible[0])
	return out
}

// choiceReason names why position chosen won: first when nothing before it was passed over, score when an earlier
// eligible candidate lost on score, otherwise the earlier candidates' skip (quota wins over unavailable: it is the
// one the owner can wait out).
func choiceReason(lines []candidateScore, chosen, firstEligible int) string {
	if chosen == 0 {
		return dispatchFirst
	}
	if chosen != firstEligible {
		return dispatchScore
	}
	reason := dispatchUnavailable
	for _, l := range lines[:chosen] {
		if l.Skip == dispatchQuota {
			reason = dispatchQuota
		}
	}
	return reason
}

func roundScore(v float64) float64 { return math.Round(v*100) / 100 }

// candidateStatsStore reads the efficiency samples (internal/db model_routing_samples.go).
type candidateStatsStore interface {
	ModelRoutingSamples(ctx context.Context, tier string, since, now time.Time, limit int) ([]db.ModelRoutingSample, error)
}

var _ candidateStatsStore = (*db.DB)(nil)

// candidateTierStats is tier's efficiency over the last candidateStatsDays; empty when the store cannot say (no
// samples, no database, a failed read: every candidate then has the prior).
func (s *Service) candidateTierStats(ctx context.Context, tier string, now time.Time) tierStats {
	store, ok := s.store().(candidateStatsStore)
	if !ok || tier == "" {
		return tierStats{}
	}
	samples, err := store.ModelRoutingSamples(ctx, tier, now.AddDate(0, 0, -candidateStatsDays), now, candidateStatsLimit)
	if err != nil {
		s.logf("candidate stats %s: %v", tier, err)
		return tierStats{}
	}
	return tierStatsOf(samples)
}

// scoreTier scores a tier's candidates as pickTierCandidate judged them (in.Checks: Available is usable here), with
// the headroom read once from the merged usage accounts and the tier's 14-day efficiency.
func (s *Service) scoreTier(ctx context.Context, in tierRankInput) candidateChoice {
	now := time.Now()
	accounts := s.usageAccounts(ctx)
	stats := s.candidateTierStats(ctx, in.Tier, now)
	inputs := make([]scoreInput, len(in.List))
	for i, c := range in.List {
		usable := i < len(in.Checks) && in.Checks[i].Available
		inputs[i] = scoreInput{Index: i, Launcher: c.Launcher, Model: c.Model, Usable: usable,
			Headroom: s.candidateHeadroomAt(ctx, accounts, in.Machine, c.Launcher, c.Model, now),
			Stats:    stats.By[candidateStatsKey{c.Launcher, c.Model}]}
	}
	return scoreCandidates(inputs, stats.Prior)
}
