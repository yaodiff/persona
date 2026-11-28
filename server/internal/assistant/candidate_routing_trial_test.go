package assistant

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The four defects a two-day trial of an inactive profile found (#17011), each on fake data, and a replay of the
// trial's fifteen dev dispatches.

// routingRig is a manager with two machines, a settings store and a fleet fake whose usage, capacity and gateway
// inventory a test controls.
func routingRig(t *testing.T) (*Service, *fleetDevs) {
	t.Helper()
	f := newFakeHub()
	f.devices = []Device{
		{ID: "m1", Name: "m1", Home: "/home/me", Online: true, Reachable: true},
		{ID: "m2", Name: "m2", Home: "/home/me", Online: true, Reachable: true},
	}
	d := newFleetDevs()
	d.add("m1", "m1")
	d.add("m2", "m2")
	st := &memStore{}
	return New(Options{Hub: f, Store: func() Store { return st }, Logf: t.Logf, Devices: d}), d
}

// putRcLaunchers stores machine → launchers as the last-seen rc-file list (launchers.zshrc).
func putRcLaunchers(t *testing.T, s *Service, seen map[string][]proto.Launcher) {
	t.Helper()
	if err := s.putSetting(context.Background(), launchersZshrcKey, seen); err != nil {
		t.Fatal(err)
	}
}

// Defect 1: an rc-file or companion launcher names its agent; one named after an agent runs it.
func TestLauncherProviderReadsRcFileLaunchers(t *testing.T) {
	s, _ := routingRig(t)
	ctx := context.Background()
	putRcLaunchers(t, s, map[string][]proto.Launcher{
		"m1": {{Name: "agy", Source: "agy", Agent: "agy"}, {Name: "codex"}, {Name: "relay-claude"}},
		"m2": {{Name: "agy"}},
	})
	for _, c := range []struct{ machine, launcher, want string }{
		// its agent field
		{"m1", "agy", "agy"},
		// no agent field: named after the agent
		{"m2", "agy", "agy"},
		// the same for codex
		{"m1", "codex", "codex"},
		// names no agent: unknown, never guessed
		{"m1", "relay-claude", ""},
		// not listed
		{"m1", "missing", ""},
		// another machine's list is not this one's
		{"m3", "agy", ""},
	} {
		if got := s.launcherProvider(ctx, c.machine, c.launcher); got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.machine, c.launcher, got, c.want)
		}
	}
	// The health record still comes first.
	s.recordHealth(ctx, "m1", "relay-claude", healthResult{agent: "claude", ok: true, source: healthSourceCheck})
	if got := s.launcherProvider(ctx, "m1", "relay-claude"); got != "claude" {
		t.Fatalf("health record: %q", got)
	}
}

// Defect 1 end to end: an rc-file agy launcher reads the antigravity account's windows.
func TestAgyCandidateReadsAntigravityHeadroom(t *testing.T) {
	s, d := routingRig(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 7, 35, 0, 0, time.UTC)
	putRcLaunchers(t, s, map[string][]proto.Launcher{"m1": {{Name: "agy", Source: "agy", Agent: "agy"}}})
	d.accounts = []proto.UsageAccount{antigravityAccount(now)}
	got := s.candidateHeadroomAt(ctx, d.accounts, "m1", "agy", "gemini", now)
	if !got.Known || math.Abs(got.Percent-gemini7dHeadroom) > 0.01 {
		t.Fatalf("agy/gemini headroom = %+v, want known %.2f%%", got, gemini7dHeadroom)
	}
	// 2: the Claude and GPT pool has not started its clock: the weekly window's start share binds.
	got = s.candidateHeadroomAt(ctx, d.accounts, "m1", "agy", "claude", now)
	if !got.Known || math.Abs(got.H-0.9/7) > 1e-9 {
		t.Fatalf("agy/claude headroom = %+v, want known %.4f", got, 0.9/7)
	}
}

// The trial's Antigravity reading: Gemini 5 h 33.1 % used with two hours left, 7 d 54.5 % used with two days left;
// the Claude and GPT pool unused, so it reports no reset time.
func antigravityAccount(now time.Time) proto.UsageAccount {
	return proto.UsageAccount{ID: "agy-1", Provider: "antigravity", SampledAt: now, Local: true, Windows: []proto.UsageWindow{
		paceAt(now, "5h", 300, 3*time.Hour, 33.1, "Gemini models"),
		paceAt(now, "7d", 10080, 5*24*time.Hour, 54.5, "Gemini models"),
		{Label: "5h", WindowMinutes: 300, Model: "Claude and GPT models"},
		{Label: "7d", WindowMinutes: 10080, Model: "Claude and GPT models"},
	}}
}

// gemini7dHeadroom is that reading's binding window, the weekly one: 5/7 × 90 − 54.5 points.
var gemini7dHeadroom = 5.0/7*90 - 54.5

// Defect 2: an unused window with no reset time is one that has just reset; a used one without a reset stays unknown.
func TestWindowHeadroomUnusedWithoutReset(t *testing.T) {
	now := time.Date(2026, 11, 26, 7, 35, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		w    proto.UsageWindow
		ok   bool
		pp   float64
	}{
		{"5h unused, no reset: the start share", proto.UsageWindow{Label: "5h", WindowMinutes: 300}, true, 17},
		{"7d unused, no reset: the start share", proto.UsageWindow{Label: "7d", WindowMinutes: 10080}, true, 90.0 / 7},
		{"used, no reset: unknown", proto.UsageWindow{Label: "5h", WindowMinutes: 300, UsedPercent: 0.5}, false, 0},
		{"no length: unknown", proto.UsageWindow{}, false, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			pp, _, ok := windowHeadroom(c.w, now)
			if ok != c.ok || math.Abs(pp-c.pp) > 1e-9 {
				t.Fatalf("pp %v ok %v, want %v %v", pp, ok, c.pp, c.ok)
			}
		})
	}
}

// Defect 3: a quota_pool launcher counts only its gateway's accounts, never a computer's own subscription of the provider.
func TestPoolHeadroomReadsOnlyThePoolsAccounts(t *testing.T) {
	s, d := routingRig(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 7, 35, 0, 0, time.UTC)
	withLauncherGroups(s, LauncherGroup{Name: "gw", Launchers: []string{"gw-codex"}, Cap: 2,
		QuotaPool: &GatewayQuotaPool{Source: "pool-a", Provider: "codex"}})
	local := proto.UsageAccount{ID: "local", Provider: "codex", Local: true, SampledAt: now,
		Windows: []proto.UsageWindow{paceAt(now, "5h", 300, time.Hour, 0, "")}}
	pooled := proto.UsageAccount{ID: "gw", Provider: "codex", Ref: "pool-a:acct-1", Sources: []string{"Pool A"}, SampledAt: now,
		Windows: []proto.UsageWindow{paceAt(now, "5h", 300, time.Hour, 10, "")}}
	other := proto.UsageAccount{ID: "gw-b", Provider: "codex", Ref: "pool-b:acct-1", Sources: []string{"Pool B"}, SampledAt: now,
		Windows: []proto.UsageWindow{paceAt(now, "5h", 300, time.Hour, 0, "")}}
	d.accounts = []proto.UsageAccount{local, pooled, other}
	got := s.candidateHeadroomAt(ctx, d.accounts, "m1", "gw-codex", "gpt", now)
	if !got.Known || got.Accounts != 1 || math.Abs(got.H-0.07) > 1e-9 {
		t.Fatalf("pool headroom = %+v, want only pool-a's 7 points", got)
	}
	// Without the pool's rows the local subscription is still not read.
	d.accounts = []proto.UsageAccount{local}
	if got := s.candidateHeadroomAt(ctx, d.accounts, "m1", "gw-codex", "gpt", now); got.Known {
		t.Fatalf("a computer's own subscription read as the pool: %+v", got)
	}
}

// Defect 3: a pool whose accounts carry no window numbers is judged from the gateway's account list.
func TestPoolHeadroomFallsBackToTheInventory(t *testing.T) {
	s, d := routingRig(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 7, 35, 0, 0, time.UTC)
	withLauncherGroups(s, LauncherGroup{Name: "gw", Launchers: []string{"gw-claude"}, Cap: 2,
		QuotaPool: &GatewayQuotaPool{Source: "pool-a", Provider: "claude"}})
	cool := now.Add(40 * time.Minute)
	reset := now.Add(3 * time.Hour)
	d.gateways = proto.GatewayAccounts{Accounts: []proto.GatewayAccount{
		{Source: "pool-a", AccountID: "1", Provider: "claude", Status: "active", Schedulable: true},
		{Source: "pool-a", AccountID: "2", Provider: "claude", Status: "active", Schedulable: true},
		{Source: "pool-a", AccountID: "3", Provider: "claude", Status: "cooldown", CooldownUntil: &cool},
		// switched off
		{Source: "pool-a", AccountID: "4", Provider: "claude", Status: "disabled", Schedulable: true},
		// another provider
		{Source: "pool-a", AccountID: "5", Provider: "codex", Status: "active", Schedulable: true},
		// another gateway
		{Source: "pool-b", AccountID: "6", Provider: "claude", Status: "active", Schedulable: true},
	}}
	got := s.candidateHeadroomAt(ctx, nil, "m1", "gw-claude", "claude-opus-5-5", now)
	if !got.Known || got.Accounts != 3 || math.Abs(got.H-2*0.17) > 1e-9 || got.RecoversAt != nil {
		t.Fatalf("inventory headroom = %+v, want two schedulable accounts' start share", got)
	}
	// All unschedulable: none, recovering at the nearest cooldown end or window reset.
	d.gateways.Accounts = []proto.GatewayAccount{
		{Source: "pool-a", AccountID: "3", Provider: "claude", Status: "cooldown", CooldownUntil: &cool},
		{Source: "pool-a", AccountID: "7", Provider: "claude", Status: "exhausted", Windows: []proto.GatewayWindow{{Kind: "five_hour", ResetsAt: &reset}}},
	}
	got = s.candidateHeadroomAt(ctx, nil, "m1", "gw-claude", "claude-opus-5-5", now)
	if !got.Known || got.H != 0 || got.RecoversAt == nil || !got.RecoversAt.Equal(cool) {
		t.Fatalf("saturated pool = %+v, want none until %v", got, cool)
	}
	// Every account stale: unknown, never full.
	d.gateways.Accounts = []proto.GatewayAccount{{Source: "pool-a", AccountID: "1", Provider: "claude", Status: "active", Schedulable: true, Stale: true}}
	if got := s.candidateHeadroomAt(ctx, nil, "m1", "gw-claude", "claude-opus-5-5", now); got.Known || !got.Stale {
		t.Fatalf("stale pool = %+v", got)
	}
}

// Defect 3: a launcher with no pool whose provider has no window reading is judged from the provider's capacity.
func TestHeadroomFallsBackToCapacity(t *testing.T) {
	s, d := routingRig(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 7, 35, 0, 0, time.UTC)
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw-claude", Agent: "claude", BaseURL: "https://gw.example", Model: "m"}})
	// windows of another provider only
	d.accounts = []proto.UsageAccount{antigravityAccount(now)}
	d.capacity = proto.CapacitySnapshot{CheckedAt: now, Providers: []proto.ProviderCapacity{{Provider: "claude", Accounts: 3, Available: 2}}}
	got := s.candidateHeadroomAt(ctx, d.accounts, "m1", "gw-claude", "claude-opus-5-5", now)
	if !got.Known || math.Abs(got.H-0.34) > 1e-9 {
		t.Fatalf("capacity headroom = %+v, want 0.34", got)
	}
	recovery := now.Add(time.Hour)
	d.capacity = saturated("claude", recovery, false)
	got = s.candidateHeadroomAt(ctx, d.accounts, "m1", "gw-claude", "claude-opus-5-5", now)
	if !got.Known || got.H != 0 || got.RecoversAt == nil || !got.RecoversAt.Equal(recovery) {
		t.Fatalf("saturated provider = %+v", got)
	}
	d.capacity = saturated("claude", recovery, true)
	if got := s.candidateHeadroomAt(ctx, d.accounts, "m1", "gw-claude", "claude-opus-5-5", now); got.Known || !got.Stale {
		t.Fatalf("stale capacity = %+v", got)
	}
	d.capacity = proto.CapacitySnapshot{}
	if got := s.candidateHeadroomAt(ctx, d.accounts, "m1", "gw-claude", "claude-opus-5-5", now); got.Known || got.Stale {
		t.Fatalf("nothing known = %+v", got)
	}
}

// Defect 4: one sample on another candidate no longer locks an untried one out.
func TestUntriedCandidateIsNotLockedOut(t *testing.T) {
	tried := candidateStats{N: 1, MinutesP50: 110, FirstPass: 1}
	in := []scoreInput{
		{Index: 0, Launcher: "a", Model: "m", Usable: true},
		{Index: 1, Launcher: "b", Model: "m", Usable: true, Stats: tried},
	}
	got := scoreCandidates(in, tried)
	// 0: T = 110, FP = 1, no κ term → 110. 1: 110 + 0.5 × 110 / √2 = 148.89.
	if got.Index != 0 || got.Reason != dispatchFirst || *got.Scores[0].Score != 110 || *got.Scores[1].Score != 148.89 {
		t.Fatalf("chose %d (%s): %+v", got.Index, got.Reason, got.Scores)
	}
	// Once tried, fewer samples cost more again.
	if p1, p8 := shortagePenalty(1, 100), shortagePenalty(8, 100); p1 <= p8 || p8 <= 0 || shortagePenalty(0, 100) != 0 {
		t.Fatalf("penalties n=1 %v, n=8 %v", p1, p8)
	}
}

// trialDispatch is one of the trial's dev dispatches: the machine, how many of the gateway candidate's accepted dev
// samples existed then, and the score the trial recorded for that candidate (0: not in the report).
type trialDispatch struct {
	machine  string
	samples  int
	recorded float64
}

// The trial's accepted dev samples, all on the gateway Claude candidate (the active profile ran every dev task there),
// in acceptance order. Reconstructed from the report's recorded scores: n = 1 → p50 110, FP 1; n = 5 → 105, 1;
// n = 6 → 107.5, 5/6; n = 7 → 105, 6/7.
var trialSamples = []struct {
	minutes int
	pass    bool
}{{110, true}, {28, true}, {105, true}, {180, true}, {100, true}, {120, false}, {60, true}, {115, true}}

// The fifteen dev dispatches of the 48-hour window, in order: three before any sample, then twelve as the samples came.
var trialDispatches = []trialDispatch{
	{"m1", 0, 0}, {"m1", 0, 0}, {"m1", 0, 0},
	{"m1", 1, 148.89}, {"m1", 1, 0}, {"m1", 2, 0}, {"m1", 3, 0}, {"m1", 4, 0},
	{"m2", 5, 126.43}, {"m1", 5, 0}, {"m2", 6, 145.73}, {"m1", 6, 0},
	{"m1", 7, 138.56}, {"m1", 7, 0}, {"m1", 8, 0},
}

// trialStats is the dev tier's efficiency after the first n samples.
func trialStats(n int) tierStats {
	var rows []db.ModelRoutingSample
	for _, s := range trialSamples[:n] {
		rows = append(rows, db.ModelRoutingSample{Tier: "dev", Launcher: "gw-claude", Model: "claude-opus-5-5", ActualMinutes: s.minutes, FirstPass: s.pass})
	}
	return tierStatsOf(rows)
}

// replayTrial scores the trial's dev candidates (agy/gemini, gw-claude/claude-opus-5-5, agy/claude) for each dispatch
// with the headroom the manager reads now, and checks the gateway candidate's score against the recorded one.
func replayTrial(t *testing.T, s *Service, d *fleetDevs, now time.Time) []candidateChoice {
	t.Helper()
	list := []struct{ launcher, model string }{{"agy", "gemini"}, {"gw-claude", "claude-opus-5-5"}, {"agy", "claude"}}
	var out []candidateChoice
	for i, dispatch := range trialDispatches {
		stats := trialStats(dispatch.samples)
		in := make([]scoreInput, len(list))
		for j, c := range list {
			in[j] = scoreInput{Index: j, Launcher: c.launcher, Model: c.model, Usable: true,
				Headroom: s.candidateHeadroomAt(context.Background(), d.accounts, dispatch.machine, c.launcher, c.model, now),
				Stats:    stats.By[candidateStatsKey{c.launcher, c.model}]}
		}
		got := scoreCandidates(in, stats.Prior)
		// The gateway candidate's κ term is unchanged, so with unknown headroom it scores what the trial recorded.
		if dispatch.recorded != 0 && !in[1].Headroom.Known && *got.Scores[1].Score != dispatch.recorded {
			t.Fatalf("dispatch %d: gateway score %v, recorded %v", i+1, *got.Scores[1].Score, dispatch.recorded)
		}
		out = append(out, got)
	}
	return out
}

// The trial replayed: before the fixes every candidate's headroom was unknown and twelve of fifteen went to the
// gateway candidate. With the cold-start fix alone no dispatch is locked there; with all four, every candidate's
// headroom is known and the quota pressure decides.
func TestReplayRoutingTrialDevDispatches(t *testing.T) {
	now := time.Date(2026, 11, 26, 7, 35, 0, 0, time.UTC)

	t.Run("headroom unknown everywhere, as in the trial", func(t *testing.T) {
		s, d := routingRig(t)
		for i, c := range replayTrial(t, s, d, now) {
			if c.Index != 0 || c.Reason != dispatchFirst {
				t.Errorf("dispatch %d: chose %d (%s), want the untried first candidate: %+v", i+1, c.Index, c.Reason, c.Scores)
			}
		}
	})

	t.Run("all four fixes", func(t *testing.T) {
		s, d := routingRig(t)
		putRcLaunchers(t, s, map[string][]proto.Launcher{
			"m1": {{Name: "agy", Source: "agy", Agent: "agy"}},
			"m2": {{Name: "agy", Source: "agy", Agent: "agy"}},
		})
		putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw-claude", Agent: "claude", BaseURL: "https://gw.example", Model: "claude-opus-5-5"}})
		d.accounts = []proto.UsageAccount{antigravityAccount(now)}
		d.capacity = proto.CapacitySnapshot{CheckedAt: now, Providers: []proto.ProviderCapacity{{Provider: "claude", Accounts: 3, Available: 2}}}
		choices := replayTrial(t, s, d, now)
		gateway := 0
		for i, c := range choices {
			for j, line := range c.Scores {
				if !line.Headroom.Known {
					t.Errorf("dispatch %d candidate %d: headroom unknown: %+v", i+1, j, line.Headroom)
				}
			}
			if c.Index == 1 {
				gateway++
			}
			if trialDispatches[i].samples > 0 && c.Index == 1 {
				t.Errorf("dispatch %d: locked on the gateway candidate again: %+v", i+1, c.Scores)
			}
		}
		// With no samples, the gateway pool's larger headroom (two schedulable accounts) wins the first three.
		if gateway != 3 {
			t.Errorf("gateway candidate chosen %d times, want the three cold dispatches", gateway)
		}
	})
}
