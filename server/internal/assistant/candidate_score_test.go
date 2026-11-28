package assistant

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestScoreCandidates(t *testing.T) {
	known := func(h float64) candidateHeadroom { return candidateHeadroom{Known: true, H: h, Percent: h * 100} }
	c := func(i int, usable bool, h candidateHeadroom, st candidateStats) scoreInput {
		return scoreInput{Index: i, Launcher: "launcher-" + string(rune('a'+i)), Model: "model-x", Usable: usable, Headroom: h, Stats: st}
	}
	none := candidateStats{}
	pooled := candidateStats{N: 20, MinutesP50: 20, FirstPass: 0.75}
	cases := []struct {
		name   string
		in     []scoreInput
		prior  candidateStats
		index  int
		reason string
		scores []float64 // NaN: no score (passed over)
		skips  []string
	}{
		{name: "no data anywhere: the default prior, list order",
			in: []scoreInput{c(0, true, candidateHeadroom{}, none), c(1, true, candidateHeadroom{}, none)}, index: 0, reason: dispatchFirst,
			// T = 30, FP = 0.5: 30 × 1.5 = 45; untried, no κ term
			scores: []float64{45, 45}, skips: []string{"", ""}},
		{name: "measured faster and cleaner wins on score",
			in:    []scoreInput{c(0, true, candidateHeadroom{}, none), c(1, true, candidateHeadroom{}, candidateStats{N: 10, MinutesP50: 10, FirstPass: 1})},
			prior: pooled, index: 1, reason: dispatchScore,
			// 0: T = 20, FP = 0.75 → 25, untried. 1: T = 200/15, FP = 13.75/15 → 14.44 + 2.01 = 16.45
			scores: []float64{25, 16.45}, skips: []string{"", ""}},
		{name: "quota pressure counts, unknown headroom adds nothing",
			in:    []scoreInput{c(0, true, known(0.5), none), c(1, true, candidateHeadroom{}, none)},
			index: 1, reason: dispatchScore, scores: []float64{50, 45}, skips: []string{"", ""}},
		{name: "within one point goes to the earlier candidate",
			in:    []scoreInput{c(0, true, known(10), none), c(1, true, candidateHeadroom{}, none)},
			index: 0, reason: dispatchFirst, scores: []float64{45.3, 45}, skips: []string{"", ""}},
		{name: "no headroom is passed over",
			in:    []scoreInput{c(0, true, known(0), none), c(1, true, known(1), none)},
			index: 1, reason: dispatchQuota, scores: []float64{math.NaN(), 47.73}, skips: []string{dispatchQuota, ""}},
		{name: "not on this computer is passed over",
			in:    []scoreInput{c(0, false, candidateHeadroom{}, none), c(1, true, candidateHeadroom{}, none)},
			index: 1, reason: dispatchUnavailable, scores: []float64{math.NaN(), 45}, skips: []string{dispatchUnavailable, ""}},
		{name: "quota is named over unavailable",
			in:    []scoreInput{c(0, false, candidateHeadroom{}, none), c(1, true, known(0), none), c(2, true, candidateHeadroom{}, none)},
			index: 2, reason: dispatchQuota, scores: []float64{math.NaN(), math.NaN(), 45}, skips: []string{dispatchUnavailable, dispatchQuota, ""}},
		{name: "every usable over pace: the first usable",
			in:    []scoreInput{c(0, false, candidateHeadroom{}, none), c(1, true, known(0), none), c(2, true, known(0), none)},
			index: 1, reason: dispatchOverPace, scores: []float64{math.NaN(), math.NaN(), math.NaN()}, skips: []string{dispatchUnavailable, dispatchQuota, dispatchQuota}},
		{name: "none usable",
			in:    []scoreInput{c(0, false, candidateHeadroom{}, none)},
			index: -1, reason: dispatchUnavailable, scores: []float64{math.NaN()}, skips: []string{dispatchUnavailable}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scoreCandidates(tc.in, tc.prior)
			if got.Index != tc.index || got.Reason != tc.reason {
				t.Fatalf("chose %d (%s), want %d (%s): %+v", got.Index, got.Reason, tc.index, tc.reason, got.Scores)
			}
			for i, line := range got.Scores {
				if line.Skip != tc.skips[i] {
					t.Errorf("candidate %d skip %q, want %q", i, line.Skip, tc.skips[i])
				}
				want := tc.scores[i]
				switch {
				case math.IsNaN(want) && line.Score != nil:
					t.Errorf("candidate %d scored %v, want none", i, *line.Score)
				case !math.IsNaN(want) && (line.Score == nil || math.Abs(*line.Score-want) > 0.011):
					t.Errorf("candidate %d score %v, want %v", i, line.Score, want)
				}
			}
		})
	}
}

func TestTierStatsOf(t *testing.T) {
	samples := []db.ModelRoutingSample{
		{Tier: "dev", Launcher: "a", Model: "m", ActualMinutes: 10, FirstPass: true},
		{Tier: "dev", Launcher: "a", Model: "m", ActualMinutes: 30, FirstPass: false},
		{Tier: "dev", Launcher: "a", Model: "m", ActualMinutes: 20, FirstPass: true},
		{Tier: "dev", Launcher: "b", Model: "m", ActualMinutes: 40, FirstPass: true},
		{Tier: "dev", Launcher: "b", Model: "m", ActualMinutes: 0, FirstPass: true}, // no duration: not a sample
	}
	got := tierStatsOf(samples)
	a := got.By[candidateStatsKey{"a", "m"}]
	if a.N != 3 || a.MinutesP50 != 20 || math.Abs(a.FirstPass-2.0/3) > 1e-9 {
		t.Fatalf("a = %+v", a)
	}
	if b := got.By[candidateStatsKey{"b", "m"}]; b.N != 1 || b.MinutesP50 != 40 || b.FirstPass != 1 {
		t.Fatalf("b = %+v", b)
	}
	if got.Prior.N != 4 || got.Prior.MinutesP50 != 25 || got.Prior.FirstPass != 0.75 {
		t.Fatalf("prior = %+v", got.Prior)
	}
	if empty := tierStatsOf(nil); empty.Prior.N != 0 || len(empty.By) != 0 {
		t.Fatalf("empty = %+v", empty)
	}
}

// fakeRoutingStore answers ModelRoutingSamples for candidateTierStats.
type fakeRoutingStore struct {
	Store
	samples []db.ModelRoutingSample
	err     error
	asked   string
	since   time.Time
}

func (f *fakeRoutingStore) ModelRoutingSamples(_ context.Context, tier string, since, _ time.Time, _ int) ([]db.ModelRoutingSample, error) {
	f.asked, f.since = tier, since
	return f.samples, f.err
}

func TestCandidateTierStatsReadsFourteenDays(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	store := &fakeRoutingStore{samples: []db.ModelRoutingSample{{Tier: "dev", Launcher: "a", Model: "m", ActualMinutes: 12, FirstPass: true}}}
	s := &Service{logf: func(string, ...any) {}}
	s.setStore(func() Store { return store })
	got := s.candidateTierStats(context.Background(), "dev", now)
	if store.asked != "dev" || !store.since.Equal(now.AddDate(0, 0, -14)) {
		t.Fatalf("asked %q since %v", store.asked, store.since)
	}
	if got.By[candidateStatsKey{"a", "m"}].N != 1 || got.Prior.MinutesP50 != 12 {
		t.Fatalf("stats = %+v", got)
	}
	store.err = errors.New("down")
	if got := s.candidateTierStats(context.Background(), "dev", now); got.Prior.N != 0 {
		t.Fatalf("a failed read is no data, got %+v", got)
	}
	emptySvc := &Service{}
	emptySvc.setStore(func() Store { return nil })
	if got := emptySvc.candidateTierStats(context.Background(), "dev", now); got.Prior.N != 0 {
		t.Fatalf("no store is no data, got %+v", got)
	}
}
