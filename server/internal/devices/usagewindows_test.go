package devices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func uw(label string, minutes int, used float64, resets time.Time) proto.UsageWindow {
	return proto.UsageWindow{Label: label, WindowMinutes: minutes, UsedPercent: used, ResetsAt: &resets}
}

func TestMergeUsageAccountsStillDedupesClaudeByWindow(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	h5, d7 := now.Add(2*time.Hour), now.Add(3*24*time.Hour)
	other5, other7 := now.Add(4*time.Hour), now.Add(5*24*time.Hour)
	in := []proto.UsageAccount{
		// machine A saw the account a while ago, machine B saw it just now
		{Provider: "claude", Name: "Claude Max", Plan: "max", SampledAt: now.Add(-2 * time.Hour), Windows: []proto.UsageWindow{uw("5h", 300, 10, h5), uw("7d", 10080, 20, d7)}},
		{Provider: "claude", Name: "Claude Max", Plan: "max", SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("5h", 300, 30, h5), uw("7d", 10080, 25, d7)}},
		// another account: other reset times
		{Provider: "claude", Name: "Claude Max", Plan: "max", SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("5h", 300, 5, other5), uw("7d", 10080, 90, other7)}},
		// the same reset second under another provider is not the same account
		{Provider: "gemini", Name: "Gemini", SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 1, d7)}},
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != 3 {
		t.Fatalf("accounts: %+v", got)
	}
	// the most used first
	if got[0].Windows[1].UsedPercent != 90 {
		t.Errorf("first = %+v", got[0])
	}
	var merged *proto.UsageAccount
	for i := range got {
		if got[i].Provider == "claude" && got[i].Windows[0].UsedPercent == 30 {
			merged = &got[i]
		}
	}
	if merged == nil || merged.Windows[1].UsedPercent != 25 || !merged.SampledAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("the newest sample must win: %+v", got)
	}
	// two accounts with one name are numbered (which one gets the 2 follows their ids, not their numbers)
	names := map[string]bool{}
	for _, a := range got {
		names[a.Name] = true
	}
	if !names["Claude Max"] || !names["Claude Max 2"] || !names["Gemini"] || len(names) != 3 {
		t.Errorf("names: %v", names)
	}
	for _, a := range got {
		if a.ID == "" || a.Stale {
			t.Errorf("account %+v", a)
		}
	}
}

func TestUsageWindowsReconcilesAntigravitySources(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	five, week := now.Add(time.Hour), now.Add(3*24*time.Hour)
	gemini5, gemini7 := uw("5h", 300, 3, five), uw("7d", 10080, 38, week)
	claude5, claude7 := uw("5h", 300, 0, five), uw("7d", 10080, 0, week)
	gemini5.Model, gemini7.Model = "Gemini Models", "Gemini Models"
	claude5.Model, claude7.Model = "Claude and GPT models", "Claude and GPT models"
	node := proto.UsageAccount{Provider: "agy", Name: "Antigravity", Local: true, Origin: "computer/0",
		Machines: []string{"Computer"}, SampledAt: now, Windows: []proto.UsageWindow{gemini5, gemini7, claude5, claude7}}
	gemini := proto.UsageAccount{Provider: "antigravity", Name: "Antigravity · Gemini", Pass: true, Origin: "external#0",
		Sources: []string{"External"}, SampledAt: now.Add(-time.Minute), Headline: "Quota details",
		Windows: []proto.UsageWindow{uw("5h", 300, 2, five), uw("7d", 10080, 36, week.Add(time.Millisecond))}}
	gemini.Windows[1].Title = "Weekly Gemini quota"
	protection := 90.0
	gemini.Windows[1].ProtectPercent = &protection
	claude := proto.UsageAccount{Provider: "antigravity", Name: "Antigravity · Claude/GPT", Pass: true, Origin: "external#1",
		Sources: []string{"External"}, SampledAt: now, Windows: []proto.UsageWindow{uw("5h", 300, 0, five), uw("7d", 10080, 0, week)}}
	codex := proto.UsageAccount{Provider: "codex", Name: "Codex Pro", SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 23, week)}}
	merged := mergeUsageAccounts([]proto.UsageAccount{node, gemini, claude, codex}, now)
	got := usageWindowsAnswer(merged, nil, now)["accounts"].([]proto.UsageAccount)
	if len(got) != 3 || got[0].Name != "Codex Pro" || got[1].Name != gemini.Name || got[2].Name != claude.Name {
		t.Fatalf("want Codex and two distinct external Antigravity quota groups: %+v", got)
	}
	g := got[1]
	if g.ID != usageRowID("antigravity", "extension|external#0") || g.Headline != gemini.Headline ||
		!g.Local || !slices.Equal(g.Machines, node.Machines) || !slices.Equal(g.Sources, gemini.Sources) ||
		!g.SampledAt.Equal(now) || g.StaleAt == nil || !g.StaleAt.Equal(now.Add(usageHubStaleAfter)) {
		t.Fatalf("external row identity, sources or sample time lost: %+v", g)
	}
	if len(g.Windows) != 2 || g.Windows[0].UsedPercent != 3 || g.Windows[1].UsedPercent != 38 ||
		g.Windows[1].Title != gemini.Windows[1].Title || g.Windows[1].ProtectPercent != &protection {
		t.Fatalf("Gemini needs the newest percentages and the external window metadata: %+v", g.Windows)
	}
	if c := got[2]; len(c.Windows) != 2 || mostUsed(c) != 0 || !c.Local {
		t.Fatalf("Claude/GPT has a separate quota even with the same resets: %+v", c)
	}
	if len(merged) != 4 || merged[0].Provider != "antigravity" || merged[0].ID != usageAccountID(node) ||
		len(merged[0].Windows) != 4 || merged[2].Windows[1].UsedPercent != 36 || gemini.Windows[1].UsedPercent != 36 {
		t.Fatalf("the display merge changed the poll's cached samples or the local row ID: %+v", merged)
	}
}

func TestUsageWindowsKeepsDistinctAntigravityWindows(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	week := now.Add(24 * time.Hour)
	gemini := uw("7d", 10080, 38, week)
	gemini.Model = "Gemini Models"
	claude := uw("7d", 10080, 16, week)
	claude.Model = "Claude and GPT models"
	local := proto.UsageAccount{Provider: "agy", Name: "Antigravity", Local: true, Origin: "computer/0",
		SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{claude, gemini}}
	remote := proto.UsageAccount{Provider: "antigravity", Name: "Antigravity · Gemini", Ref: "source:gemini",
		Sources: []string{"External"}, SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 39, week)}}
	answer := func(accounts ...proto.UsageAccount) []proto.UsageAccount {
		return usageWindowsAnswer(mergeUsageAccounts(accounts, now), nil, now)["accounts"].([]proto.UsageAccount)
	}
	got := answer(local, remote)
	if len(got) != 2 || mostUsed(got[0]) != 39 || len(got[1].Windows) != 1 || got[1].Windows[0].Model != claude.Model {
		t.Fatalf("want the newer Gemini reading and the unmatched Claude/GPT window: %+v", got)
	}
	otherSeat := remote
	otherSeat.Ref = "source:other-seat"
	if got := answer(local, remote, otherSeat); len(got) != 3 || len(got[2].Windows) != 2 {
		t.Fatalf("matching resets on multiple gateway seats cannot identify the local subscription: %+v", got)
	}
	for _, remoteWindow := range []proto.UsageWindow{
		uw("7d", 10080, 39, week.Add(time.Second)),
		uw("5h", 300, 39, week),
		{Label: "7d", WindowMinutes: 10080, UsedPercent: 39},
	} {
		remote.Windows = []proto.UsageWindow{remoteWindow}
		if got := answer(local, remote); len(got) != 2 || len(got[1].Windows) != 2 {
			t.Fatalf("window %+v must leave both local quotas intact: %+v", remoteWindow, got)
		}
	}

	remote.Name = "Gateway seat"
	remote.Windows = []proto.UsageWindow{uw("7d", 10080, 39, week)}
	remote.Windows[0].Model = "gemini"
	remote.Stale, remote.Problem = true, "unreachable"
	if got := answer(local, remote); len(got) != 2 || mostUsed(got[0]) != 38 || got[0].Stale || got[0].Problem != "" ||
		got[0].Windows[0].Model != "gemini" || len(got[1].Windows) != 1 || got[1].Windows[0].Model != claude.Model {
		t.Fatalf("an older fresh reading should replace the stale model-scoped window: %+v", got)
	}
}

func TestAntigravityGenericIdleSibling(t *testing.T) {
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	week := now.Add(7 * 24 * time.Hour)
	for _, tc := range []struct {
		name, scope, fiveHour  string
		geminiOffset, localAge time.Duration
		wantUsed               float64
	}{
		{"epoch filtered", "Claude/GPT", `{"label":"5h","used_percent":80,"resets_at":"1970-01-01T00:00:00Z"},`, 0, 0, 0},
		{"missing with unmatched Gemini", "Claude / GPT", "", time.Second, 0, 0},
		{"both idle", "Claude/GPT", `{"label":"5h","used_percent":0,"resets_at":null},`, 0, 0, 0},
		{"nil resets prefer newer local", "Claude/GPT", `{"label":"5h","used_percent":8},`, 0, 0, 0},
		{"nil resets prefer external on tie", "Claude/GPT", `{"label":"5h","used_percent":8},`, 0, time.Minute, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seatName := "Antigravity · " + tc.scope
			gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"accounts":[
					{"id":"seat-c","provider":"antigravity","name":%q,"sampled_at":%q,"windows":[
						%s{"label":"1d","used_percent":97,"resets_at":"2026-11-05T08:59:59Z"},
						{"label":"7d","used_percent":42,"resets_at":%q}]},
					{"id":"seat-g","provider":"antigravity","name":"Antigravity · Gemini","windows":[
						{"label":"7d","used_percent":76,"resets_at":%q}]}]}`,
					seatName, now.Add(-time.Minute).Format(time.RFC3339), tc.fiveHour, week.Add(time.Millisecond).Format(time.RFC3339Nano),
					week.Add(tc.geminiOffset).Format(time.RFC3339))
			}))
			defer gate.Close()
			var gateway usageGateways
			gateway.init(UsageConfig{Sources: []UsageSourceConfig{{ID: "pool", Name: "Gateway", Type: "generic", URL: gate.URL, Key: "k"}}}, func() {}, t.Logf)
			gateway.pollDue(t.Context(), now)

			claude7, gemini7 := uw("7d", 10080, 45, week), uw("7d", 10080, 79, week)
			claude7.Model, gemini7.Model = "Claude and GPT models", "Gemini Models"
			idle := proto.UsageWindow{Label: "5h", WindowMinutes: 300, Model: claude7.Model}
			node := local("agy", "Antigravity", now.Add(-tc.localAge), gemini7, idle, claude7)
			node.Machines = []string{"Computer"}
			cache := mergeUsageAccounts(append(gateway.accounts(now), node), now)
			if len(cache) != 3 {
				t.Fatalf("expected a native sample and two gateway seats: %+v", cache)
			}
			before, err := json.Marshal(cache)
			if err != nil {
				t.Fatal(err)
			}
			got := usageWindowsAnswer(cache, nil, now)["accounts"].([]proto.UsageAccount)
			after, err := json.Marshal(cache)
			if err != nil || string(before) != string(after) {
				t.Fatalf("display changed poll cache: %s -> %s (%v)", before, after, err)
			}
			rows := map[string]proto.UsageAccount{}
			for _, a := range got {
				rows[a.Name] = a
			}
			c := rows[seatName]
			if c.ID == "" || !slices.ContainsFunc(cache, func(a proto.UsageAccount) bool { return a.ID == c.ID && a.Ref == "pool:seat-c" }) ||
				!c.Local || !slices.Equal(c.Sources, []string{"Gateway"}) || !slices.Equal(c.Machines, node.Machines) {
				t.Fatalf("Claude/GPT seat lost identity or sources: %+v", c)
			}
			if len(c.Windows) != 2 || c.Windows[0].WindowMinutes != 300 || c.Windows[0].ResetsAt != nil || c.Windows[0].UsedPercent != tc.wantUsed ||
				c.Windows[1].WindowMinutes != 10080 || c.Windows[1].ResetsAt == nil || c.Windows[1].ResetsAt.Unix() != week.Unix() {
				t.Fatalf("expected one idle 5h and one active 7d window: %+v", c.Windows)
			}
			g := rows["Antigravity · Gemini"]
			if len(g.Windows) != 1 || g.Windows[0].ResetsAt == nil || !g.Windows[0].ResetsAt.Equal(week.Add(tc.geminiOffset)) {
				t.Fatalf("wrong Gemini window: %+v", g)
			}
			if tc.geminiOffset == 0 {
				if !g.Local || len(got) != 2 {
					t.Fatalf("expected only the two matched seats: %+v", got)
				}
			} else if rest := rows[node.Name]; g.Local || len(got) != 3 || len(rest.Windows) != 1 || !reflect.DeepEqual(rest.Windows[0], gemini7) {
				t.Fatalf("only unmatched Gemini should remain on the native row: %+v", got)
			}
		})
	}
}

func TestAntigravitySiblingCollisions(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	week, day := now.Add(7*24*time.Hour), now.Add(24*time.Hour)
	makeNode := func(origin string, wins ...proto.UsageWindow) proto.UsageAccount {
		a := local("agy", "Antigravity", now, wins...)
		a.Origin = origin
		for i := range a.Windows {
			a.Windows[i].Model = "Claude and GPT models"
		}
		return a
	}
	seat := func(id string, wins ...proto.UsageWindow) proto.UsageAccount {
		return gw("Gateway", id, "antigravity", "Antigravity · Claude/GPT", now, wins...)
	}
	idle := proto.UsageWindow{WindowMinutes: 300, Label: "5h"}
	w7, w1 := uw("7d", 10080, 32, week), uw("1d", 1440, 23, day)
	node := makeNode("computer/0", idle, w1, w7)
	for _, tc := range []struct {
		name           string
		in             []proto.UsageAccount
		localRemaining []int // durations on the first local source
		foldedWeek     bool
	}{
		{"two seats share reset", []proto.UsageAccount{node, seat("a", w1, w7), seat("b", w7)}, []int{300, 1440, 10080}, false},
		{"one scope hits two seats on different durations", []proto.UsageAccount{node, seat("a", w1), seat("b", w7)}, []int{300, 1440, 10080}, false},
		{"two local sources share one seat", []proto.UsageAccount{node, makeNode("computer/1", idle, w1), seat("a", w7, w1)}, []int{300, 1440, 10080}, false},
		{"different timed resets block idle sibling", []proto.UsageAccount{node, seat("a", w7, uw("1d", 1440, 28, day.Add(time.Second)))}, []int{300, 1440}, true},
		{"active external slot and idle local slot", []proto.UsageAccount{node, seat("a", w7, uw("5h", 300, 28, now.Add(time.Hour)))}, []int{300, 1440}, true},
		{"different reset second", []proto.UsageAccount{node, seat("a", uw("7d", 10080, 33, week.Add(time.Second)))}, []int{300, 1440, 10080}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := func(in []proto.UsageAccount) []proto.UsageAccount {
				return usageWindowsAnswer(mergeUsageAccounts(in, now), nil, now)["accounts"].([]proto.UsageAccount)
			}
			got := answer(tc.in)
			if len(got) != len(tc.in) {
				t.Fatalf("lost a conflicting row: %+v", got)
			}
			for _, a := range got {
				if a.Origin == node.Origin {
					var durations []int
					for _, w := range a.Windows {
						durations = append(durations, w.WindowMinutes)
					}
					if !slices.Equal(durations, tc.localRemaining) {
						t.Fatalf("remaining local slots = %v", durations)
					}
				} else if len(a.Sources) > 0 {
					if a.Local != tc.foldedWeek {
						t.Fatalf("unexpected local association: %+v", a)
					}
					for _, src := range tc.in {
						if src.Ref != a.Ref {
							continue
						}
						if len(src.Windows) != len(a.Windows) {
							t.Fatalf("extra external sibling: %+v", a)
						}
						for _, w := range src.Windows {
							if !slices.Contains(a.Windows, w) {
								t.Fatalf("lost external window %+v from %+v", w, a)
							}
						}
					}
				}
			}
			reordered := slices.Clone(tc.in)
			slices.Reverse(reordered)
			for i, a := range reordered {
				reordered[i].Windows = slices.Clone(a.Windows)
				slices.Reverse(reordered[i].Windows)
			}
			if other := answer(reordered); !reflect.DeepEqual(got, other) {
				t.Fatalf("order changed result: %+v vs %+v", got, other)
			}
		})
	}
}

// A local sample is not a bridge between two different accounts of one gateway (#7324).
func TestMergeUsageAccountsDoesNotBridgeGatewayAccounts(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	resets := now.Add(3 * 24 * time.Hour)
	in := []proto.UsageAccount{
		{Provider: "claude", Name: "Claude Max", Local: true, Origin: "mac/0", SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 10, resets)}},
		{Provider: "claude", Name: "Seat A", Ref: "s1:a", Origin: "s1:a", Sources: []string{"Relay"}, SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 20, resets)}},
		{Provider: "claude", Name: "Seat B", Ref: "s1:b", Origin: "s1:b", Sources: []string{"Relay"}, SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 30, resets)}},
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != 2 {
		t.Fatalf("two gateway seats merged through a local sample: got %d account(s), want 2: %+v", len(got), got)
	}
	used := map[string]float64{}
	for _, a := range got {
		used[a.Ref] = mostUsed(a)
	}
	if used["s1:a"] != 10 || used["s1:b"] != 30 {
		t.Fatalf("a gateway seat or its usage disappeared: %+v", got)
	}
}

func TestMergeUsageAccountsDropsResetStaleAndOld(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	in := []proto.UsageAccount{
		{Provider: "codex", Name: "reset", SampledAt: now, Windows: []proto.UsageWindow{uw("5h", 300, 99, now.Add(-time.Second))}},
		{Provider: "codex", Name: "old", SampledAt: now.Add(-usageHubMaxAge - time.Hour), Windows: []proto.UsageWindow{uw("7d", 10080, 1, now.Add(time.Hour))}},
		{Provider: "codex", Name: "stale", SampledAt: now.Add(-usageHubStaleAfter - time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 80, now.Add(time.Hour))}},
		{Provider: "claude", Name: "fresh", SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 5, now.Add(time.Hour))}},
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != 2 || got[0].Name != "fresh" || got[1].Name != "stale" || !got[1].Stale {
		t.Fatalf("got %+v", got)
	}
	if got := mergeUsageAccounts(nil, now); got == nil || len(got) != 0 {
		t.Errorf("nothing merges to [] not nil: %#v", got)
	}
}

func TestMergeUsageAccountsCodexUsesLatestSnapshotAcrossAReset(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	week := now.Add(3 * 24 * time.Hour)
	in := []proto.UsageAccount{
		{Provider: "codex", Name: "Codex Pro", CodexAccountKey: "same", SampledAt: now.Add(-3 * time.Hour), Windows: []proto.UsageWindow{uw("5h", 300, 95, now.Add(time.Hour)), uw("7d", 10080, 95, week.Add(-time.Second))}},
		{Provider: "codex", Name: "Codex Pro", CodexAccountKey: "same", SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 2, week)}},
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != 1 || len(got[0].Windows) != 1 || got[0].Windows[0].UsedPercent != 2 || !got[0].Windows[0].ResetsAt.Equal(week) {
		t.Fatalf("want only the latest snapshot: %+v", got)
	}
	in[1].Windows = []proto.UsageWindow{uw("7d", 10080, 3, week.Add(7*24*time.Hour))}
	if after := mergeUsageAccounts(in, now); len(after) != 1 || after[0].ID != got[0].ID {
		t.Errorf("new quota cycle changed the row ID: %+v to %+v", got, after)
	}
	in[1].Windows = []proto.UsageWindow{uw("7d", 10080, 80, now.Add(-time.Second))}
	if after := mergeUsageAccounts(in, now); len(after) != 0 {
		t.Errorf("an expired latest snapshot restored an older one: %+v", after)
	}
}

func TestUsagePollAsksOnlineMachinesAndRemembersTheirLastAnswer(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	var asked atomic.Int32
	win := []proto.UsageWindow{uw("7d", 10080, 12, now.Add(time.Hour))}
	var fail atomic.Bool
	on := usageSource{id: "m1", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
		asked.Add(1)
		if fail.Load() {
			return proto.UsageSnapshot{}, errors.New("unknown method")
		}
		return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{UsageAccount: proto.UsageAccount{Provider: "codex", Name: "Codex", SampledAt: now, Windows: win}}}}, nil
	}}
	off := usageSource{id: "m2", online: false, ask: func(context.Context) (proto.UsageSnapshot, error) {
		t.Error("an offline machine is not asked")
		return proto.UsageSnapshot{}, nil
	}}
	var u usageWindowsHub
	got := u.poll(context.Background(), []usageSource{on, off}, now)
	if len(got) != 1 || asked.Load() != 1 {
		t.Fatalf("got %+v asked %d", got, asked.Load())
	}
	// within the fresh time: no second call
	u.poll(context.Background(), []usageSource{on, off}, now.Add(time.Second))
	if asked.Load() != 1 {
		t.Errorf("asked again within %v", usageHubFresh)
	}
	// later, the machine fails (an older node): its last answer stays
	fail.Store(true)
	got = u.poll(context.Background(), []usageSource{on, off}, now.Add(usageHubFresh+time.Second))
	if len(got) != 1 {
		t.Errorf("last answer lost: %+v", got)
	}
	// a machine taken out of hub.json is forgotten
	got = u.poll(context.Background(), nil, now.Add(2*usageHubFresh+2*time.Second))
	if len(got) != 0 {
		t.Errorf("removed machine kept: %+v", got)
	}
}

func TestUsagePollForgetsAMachineThatTurnedItOff(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	enabled := true
	src := usageSource{id: "m1", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
		if !enabled {
			return proto.UsageSnapshot{Enabled: false}, nil
		}
		return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{UsageAccount: proto.UsageAccount{Provider: "claude", Name: "Claude", SampledAt: now, Windows: []proto.UsageWindow{uw("5h", 300, 1, now.Add(time.Hour))}}}}}, nil
	}}
	var u usageWindowsHub
	if got := u.poll(context.Background(), []usageSource{src}, now); len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	enabled = false
	if got := u.poll(context.Background(), []usageSource{src}, now.Add(usageHubFresh+time.Second)); len(got) != 0 {
		t.Errorf("off machine still shown: %+v", got)
	}
}

func TestMachineUsageWindowsSwitch(t *testing.T) {
	off := false
	if !(&Machine{}).usageWindows() || (&Machine{cfg: MachineConfig{UsageWindows: &off}}).usageWindows() {
		t.Error("usage_windows: unset is on, false is off")
	}
}

// #3794: a model's own allowance running out does not make the account the fullest; the plan windows decide the order.
func TestMergeUsageAccountsOrdersByThePlanWindows(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fable := uw("7d", 10080, 100, now.Add(3*24*time.Hour))
	fable.Model = "fable"
	modelOnly := uw("7d", 10080, 70, now.Add(6*24*time.Hour))
	modelOnly.Model = "argon"
	in := []proto.UsageAccount{
		{Provider: "claude", Name: "A", SampledAt: now, Ref: "gw:a", Windows: []proto.UsageWindow{uw("5h", 300, 10, now.Add(time.Hour)), fable}},
		{Provider: "claude", Name: "B", SampledAt: now, Ref: "gw:b", Windows: []proto.UsageWindow{uw("5h", 300, 60, now.Add(2*time.Hour))}},
		// no plan window at all: its model window is what there is
		{Provider: "gemini", Name: "C", SampledAt: now, Ref: "gw:c", Windows: []proto.UsageWindow{modelOnly}},
	}
	got := mergeUsageAccounts(in, now)
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	if len(names) != 3 || names[0] != "C" || names[1] != "B" || names[2] != "A" {
		t.Fatalf("order = %v, want [C B A]", names)
	}
}

// Each account says when it turns stale (#1905): a client that keeps the answer while refreshes fail judges it itself,
// with the threshold of where the newest sample came from (a computer's 30 minutes, a gateway's 6 hours).
func TestMergeUsageAccountsSaysWhenEachTurnsStale(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	week := now.Add(3 * 24 * time.Hour)
	in := []proto.UsageAccount{
		{Provider: "codex", Name: "machine", SampledAt: now.Add(-10 * time.Minute), Local: true, Windows: []proto.UsageWindow{uw("7d", 10080, 10, week)}},
		{Provider: "claude", Name: "gateway", SampledAt: now.Add(-time.Hour), Sources: []string{"Relay"}, Windows: []proto.UsageWindow{uw("7d", 10080, 20, week.Add(time.Hour))}},
		{Provider: "kimi", Name: "flagged", SampledAt: now, Sources: []string{"Relay"}, Stale: true, Windows: []proto.UsageWindow{uw("7d", 10080, 30, week)}},
	}
	got := map[string]proto.UsageAccount{}
	for _, a := range mergeUsageAccounts(in, now) {
		got[a.Name] = a
	}
	for name, want := range map[string]time.Time{
		"machine": now.Add(-10 * time.Minute).Add(usageHubStaleAfter),
		"gateway": now.Add(-time.Hour).Add(usageGatewayStaleAfter),
		"flagged": now.Add(usageGatewayStaleAfter),
	} {
		a := got[name]
		if a.StaleAt == nil || !a.StaleAt.Equal(want) {
			t.Errorf("%s: stale_at %v, want %v", name, a.StaleAt, want)
		}
	}
	if got["machine"].Stale || got["gateway"].Stale || !got["flagged"].Stale {
		t.Errorf("stale now: %+v", got)
	}
}

// An account read on computers names them (#1907): a phone or another computer cannot tell "this computer" apart from
// the device in hand. The same account on two computers names both; a node's own say is not taken, a rename is.
func TestUsagePollNamesTheComputersThatReadAnAccount(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	week := now.Add(3 * 24 * time.Hour)
	const accountKey = "a6604f017eb48c3a5c675159c4736adf3f68f4f3ba603c1ca4ebca6bb683ffb8"
	const sourceKey = "e7ad658cfab6ee96d59605b5273c952bddcdf31b7b93f2bc68020f2f2088c458"
	machine := func(id, name string, used float64) usageSource {
		return usageSource{id: id, name: name, online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
			return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{
				AccountKey: accountKey, SourceKey: sourceKey,
				UsageAccount: proto.UsageAccount{Provider: "codex", Name: "Codex Pro", SampledAt: now,
					Machines: []string{"what the node says"}, Windows: []proto.UsageWindow{uw("7d", 10080, used, week)}},
			}}}, nil
		}}
	}
	var u usageWindowsHub
	u.gateways = func(time.Time) []proto.UsageAccount {
		return []proto.UsageAccount{{Provider: "claude", Name: "seat", SampledAt: now, Sources: []string{"Relay"}, Windows: []proto.UsageWindow{uw("7d", 10080, 1, week)}}}
	}
	got := u.poll(context.Background(), []usageSource{machine("m2", "Studio", 10), machine("m1", "", 20)}, now)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), accountKey) || strings.Contains(string(body), sourceKey) || strings.Contains(string(body), `"account_key"`) || strings.Contains(string(body), `"source_key"`) {
		t.Errorf("the usage response exposed an internal key: %s", body)
	}
	for _, a := range got {
		switch a.Provider {
		case "codex": // one account on two computers; one without a name goes by its id
			if !a.Local || !slices.Equal(a.Machines, []string{"Studio", "m1"}) {
				t.Errorf("codex: %+v", a)
			}
		case "claude":
			if a.Local || a.Machines != nil {
				t.Errorf("a gateway-only account names no computer: %+v", a)
			}
		}
	}
	got = u.poll(context.Background(), []usageSource{machine("m2", "Mac Studio", 10), machine("m1", "", 20)}, now.Add(usageHubFresh+time.Second))
	for _, a := range got {
		if a.Provider == "codex" && !slices.Equal(a.Machines, []string{"Mac Studio", "m1"}) {
			t.Errorf("renamed: %+v", a)
		}
	}
}

// Every account in the answer has its own id (#1904): the clients use it as a row key, and same-name numbering follows
// it. Two accounts of one gateway, and accounts with no reset time, used to share one.
func TestMergeUsageAccountsGivesEveryAccountItsOwnID(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	noReset := func(used float64) []proto.UsageWindow {
		return []proto.UsageWindow{{Label: "7d", WindowMinutes: 10080, UsedPercent: used}}
	}
	same := now.Add(3 * 24 * time.Hour)
	in := []proto.UsageAccount{
		// two seats of one gateway without a reset time, and two with the same one
		{Provider: "codex", Name: "Seat A", SampledAt: now, Ref: "s1:a", Origin: "s1:a", Sources: []string{"Relay"}, Windows: noReset(10)},
		{Provider: "codex", Name: "Seat B", SampledAt: now, Ref: "s1:b", Origin: "s1:b", Sources: []string{"Relay"}, Windows: noReset(20)},
		{Provider: "codex", Name: "Seat C", SampledAt: now, Ref: "s1:c", Origin: "s1:c", Sources: []string{"Relay"}, Windows: []proto.UsageWindow{uw("7d", 10080, 30, same)}},
		{Provider: "codex", Name: "Seat D", SampledAt: now, Ref: "s1:d", Origin: "s1:d", Sources: []string{"Relay"}, Windows: []proto.UsageWindow{uw("7d", 10080, 40, same)}},
		// a gateway account without an id of its own
		{Provider: "codex", Name: "Pooled", SampledAt: now, Origin: "s2#0", Sources: []string{"Pool"}, Windows: noReset(50)},
		// two computers, each signed in to a different Codex account, neither with a reset time; and two Codex homes on one
		{Provider: "codex", Name: "Codex Pro", SampledAt: now, Local: true, Origin: "mac/0", Windows: noReset(60)},
		{Provider: "codex", Name: "Codex Pro", SampledAt: now, Local: true, Origin: "linux/0", Windows: noReset(70)},
		{Provider: "codex", Name: "Codex Pro", SampledAt: now, Local: true, Origin: "linux/1", Windows: noReset(80)},
		// only per-model windows that reset together: nothing but the suffix tells them apart
		{Provider: "claude", Name: "Fable seat", SampledAt: now, Local: true, Origin: "mac/1", Windows: []proto.UsageWindow{{Label: "7d", WindowMinutes: 10080, UsedPercent: 5, ResetsAt: &same, Model: "Fable"}}},
		{Provider: "claude", Name: "Fable seat", SampledAt: now, Local: true, Origin: "mac/2", Windows: []proto.UsageWindow{{Label: "7d", WindowMinutes: 10080, UsedPercent: 6, ResetsAt: &same, Model: "Fable"}}},
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != len(in) {
		t.Fatalf("accounts merged that are not one: %d of %d: %+v", len(got), len(in), got)
	}
	ids := map[string]string{}
	for _, a := range got {
		if prev, dup := ids[a.ID]; dup {
			t.Errorf("%q and %q share the id %s", prev, a.Name, a.ID)
		}
		ids[a.ID] = a.Name
	}

	// The same answer in another order (the machines come from a map) gives the same ids and the same names to the same accounts.
	byOrigin := func(out []proto.UsageAccount) map[string]string {
		m := map[string]string{}
		for _, a := range out {
			m[a.Origin] = a.ID + " " + a.Name
		}
		return m
	}
	want := byOrigin(got)
	for range 20 {
		shuffled := slices.Clone(in)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if again := byOrigin(mergeUsageAccounts(shuffled, now)); !maps.Equal(again, want) {
			t.Fatalf("order changed ids or names:\n%v\n%v", again, want)
		}
	}

	// and the numbers moving does not move the id
	later := slices.Clone(in)
	for i := range later {
		later[i].Windows = slices.Clone(later[i].Windows)
		later[i].Windows[0].UsedPercent += 1
		later[i].SampledAt = now.Add(time.Minute)
	}
	if again := byOrigin(mergeUsageAccounts(later, now.Add(time.Minute))); !maps.Equal(again, want) {
		t.Errorf("new numbers changed ids:\n%v\n%v", again, want)
	}
}

func TestUsagePollKeepsCodexSamplesWithoutMatchingKeysApart(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	week := now.Add(3 * 24 * time.Hour)
	machine := func(id, accountKey, sourceKey string) usageSource {
		return usageSource{id: id, online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
			return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{
				AccountKey: accountKey, SourceKey: sourceKey,
				UsageAccount: proto.UsageAccount{Provider: "codex", Name: "Codex Pro", SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 10, week)}},
			}}}, nil
		}}
	}
	var u usageWindowsHub
	u.gateways = func(time.Time) []proto.UsageAccount {
		return []proto.UsageAccount{{Provider: "codex", Name: "Codex Pro", Ref: "s1:seat", Sources: []string{"Relay"}, SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, 10, week)}}}
	}
	sources := []usageSource{machine("mac", "a", ""), machine("linux", "b", ""), machine("old-node", "", ""), machine("new-node", "", "home")}
	got := u.poll(context.Background(), sources, now)
	if len(got) != 5 {
		t.Fatalf("got %+v", got)
	}
	ids := map[string]string{}
	for _, a := range got {
		source := a.Machines
		if len(source) == 0 {
			source = a.Sources
		}
		if len(source) != 1 {
			t.Fatalf("sources merged: %+v", a)
		}
		ids[source[0]] = a.ID
		if a.CodexAccountKey == "" && a.Name != "Codex Pro" {
			t.Errorf("an unidentified row was named as another account: %+v", a)
		}
	}
	week = week.Add(7 * 24 * time.Hour)
	got = u.poll(context.Background(), sources, now.Add(usageHubFresh+time.Second))
	if len(got) != len(ids) {
		t.Fatalf("new quota cycle changed the row count: %+v", got)
	}
	for _, a := range got {
		source := append(slices.Clone(a.Machines), a.Sources...)
		if len(source) != 1 || ids[source[0]] != a.ID {
			t.Errorf("new quota cycle changed the source's row ID: %+v", a)
		}
	}
}

func TestUsagePollPreservesStaleCodexRollout(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	sampled := now.Add(-31 * time.Minute)
	reset := now.Add(4 * time.Hour)
	machine := func(name, accountKey string, readingSource proto.UsageReadingSource, at time.Time, used float64) usageSource {
		return usageSource{id: name, online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
			return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{
				AccountKey: accountKey,
				UsageAccount: proto.UsageAccount{Provider: "codex", Name: "Codex Pro", ReadingSource: readingSource,
					SampledAt: at, Windows: []proto.UsageWindow{uw("5h", 300, used, reset)}},
			}}}, nil
		}}
	}
	sources := []usageSource{machine("Machine A", "account-a", "", now, 13), machine("Machine B", "", "rollout", sampled, 47)}
	var u usageWindowsHub
	got := u.poll(t.Context(), sources, now)
	if len(got) != 2 {
		t.Fatalf("expected separate live and historical rows, got %+v", got)
	}
	live, old := got[0], got[1] // live accounts are sorted before stale readings
	if live.ReadingSource != "" || live.Stale || !slices.Equal(live.Machines, []string{"Machine A"}) ||
		old.ReadingSource != "rollout" || !old.Stale || !old.SampledAt.Equal(sampled) ||
		!slices.Equal(old.Machines, []string{"Machine B"}) || len(old.Windows) != 1 || old.Windows[0].UsedPercent != 47 {
		t.Fatalf("expected a live account and Machine B's original stale rollout, got %+v", got)
	}

	// An unambiguously matching extension seat still absorbs local readings for all clients.
	u.gateways = func(time.Time) []proto.UsageAccount {
		return []proto.UsageAccount{{Provider: "codex", Name: "Seat", Pass: true, Sources: []string{"Pool"},
			SampledAt: now, Windows: []proto.UsageWindow{uw("5h", 300, 13, reset)}}}
	}
	got = u.poll(t.Context(), sources, now.Add(usageHubFresh+time.Second))
	if len(got) != 1 || !got[0].Pass || got[0].ReadingSource != "" || !slices.Equal(got[0].Machines, []string{"Machine A", "Machine B"}) {
		t.Fatalf("expected one extension account for the matching local readings, got %+v", got)
	}
}

func TestUsagePollDistinguishesUnidentifiedCodexHomesOnOneMachine(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	sample := func(key string, used float64) proto.UsageNodeAccount {
		return proto.UsageNodeAccount{
			SourceKey:    key,
			UsageAccount: proto.UsageAccount{Provider: "codex", Name: "Codex Pro", SampledAt: now, Windows: []proto.UsageWindow{uw("7d", 10080, used, now.Add(time.Hour))}},
		}
	}
	accounts := []proto.UsageNodeAccount{sample("home-a", 10), sample("home-b", 20)}
	source := usageSource{id: "mac", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
		return proto.UsageSnapshot{Enabled: true, Accounts: accounts}, nil
	}}
	type row struct{ id, name string }
	rows := func(accounts []proto.UsageAccount) map[float64]row {
		result := map[float64]row{}
		for _, account := range accounts {
			result[mostUsed(account)] = row{account.ID, account.Name}
		}
		return result
	}
	var u usageWindowsHub
	first := rows(u.poll(context.Background(), []usageSource{source}, now))
	names := []string{first[10].name, first[20].name}
	slices.Sort(names)
	if len(first) != 2 || first[10].id == first[20].id || !slices.Equal(names, []string{"Codex Pro", "Codex Pro 2"}) {
		t.Fatalf("two same-name homes on one machine need distinct IDs and names: %v", first)
	}
	slices.Reverse(accounts)
	after := rows(u.poll(context.Background(), []usageSource{source}, now.Add(usageHubFresh)))
	if !maps.Equal(first, after) {
		t.Errorf("changing the source order changed the row IDs or names: %v to %v", first, after)
	}
}

func TestMergeUsageAccountsDedupesModelOnlyAccountsAcrossMachines(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	fiveA, weekA := now.Add(2*time.Hour), now.Add(3*24*time.Hour)
	fiveB, weekB := now.Add(3*time.Hour), now.Add(4*24*time.Hour)
	mw := func(label string, minutes int, used float64, resets time.Time, model string) proto.UsageWindow {
		w := uw(label, minutes, used, resets)
		w.Model = model
		return w
	}

	// Three machines reporting the same local Antigravity account (only per-model windows) must merge into one row.
	agyMachines := []usageSource{
		{id: "node-a", name: "node-a", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
			return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{UsageAccount: proto.UsageAccount{
				Provider: "agy", Name: "Antigravity", SampledAt: now.Add(-5 * time.Minute),
				Windows: []proto.UsageWindow{
					mw("5h", 300, 100, fiveA, "Claude and GPT models"),
					mw("7d", 10080, 100, weekA, "Claude and GPT models"),
					mw("5h", 300, 10, fiveB, "Gemini Models"),
					mw("7d", 10080, 20, weekB, "Gemini Models"),
				},
			}}}}, nil
		}},
		{id: "node-b", name: "node-b", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
			return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{UsageAccount: proto.UsageAccount{
				Provider: "agy", Name: "Antigravity", SampledAt: now.Add(-time.Minute),
				Windows: []proto.UsageWindow{
					mw("5h", 300, 100, fiveA, "Claude and GPT models"),
					mw("7d", 10080, 100, weekA, "Claude and GPT models"),
					mw("5h", 300, 15, fiveB, "Gemini Models"),
					mw("7d", 10080, 25, weekB, "Gemini Models"),
				},
			}}}}, nil
		}},
		{id: "node-c", name: "node-c", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
			return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{UsageAccount: proto.UsageAccount{
				Provider: "agy", Name: "Antigravity", SampledAt: now.Add(-3 * time.Minute),
				Windows: []proto.UsageWindow{
					mw("5h", 300, 95, fiveA, "Claude and GPT models"),
					mw("7d", 10080, 98, weekA, "Claude and GPT models"),
					mw("5h", 300, 12, fiveB, "Gemini Models"),
					mw("7d", 10080, 22, weekB, "Gemini Models"),
				},
			}}}}, nil
		}},
	}
	var hub usageWindowsHub
	got := hub.poll(context.Background(), agyMachines, now)
	if len(got) != 1 {
		t.Fatalf("expected 3 machines with same model-only account to merge into 1 row, got %d: %+v", len(got), got)
	}
	acct := got[0]
	if acct.Provider != "antigravity" || acct.Name != "Antigravity" || !slices.Equal(acct.Machines, []string{"node-a", "node-b", "node-c"}) {
		t.Fatalf("unexpected merged account metadata: %+v", acct)
	}
	if !acct.SampledAt.Equal(now.Add(-time.Minute)) || len(acct.Windows) != 4 {
		t.Fatalf("expected newest sample and 4 deduplicated windows: %+v", acct)
	}
	bySlot := map[string]float64{}
	for _, w := range acct.Windows {
		bySlot[w.Label+"|"+w.Model] = w.UsedPercent
	}
	if bySlot["5h|Gemini Models"] != 15 || bySlot["7d|Gemini Models"] != 25 || bySlot["5h|Claude and GPT models"] != 100 {
		t.Fatalf("newest machine's per-model windows did not win: %v", bySlot)
	}

	// Generic provider with only per-model windows also merges across machines and normalizes model casing.
	generic := []proto.UsageAccount{
		{Provider: "acct-alpha", Name: "Alpha", Local: true, Origin: "node-a/0", Machines: []string{"node-a"}, SampledAt: now.Add(-4 * time.Minute),
			Windows: []proto.UsageWindow{mw("5h", 300, 10, fiveA, "Model-A"), mw("7d", 10080, 30, weekB, "model-b")}},
		{Provider: "acct-alpha", Name: "Alpha", Local: true, Origin: "node-b/0", Machines: []string{"node-b"}, SampledAt: now.Add(-time.Minute),
			Windows: []proto.UsageWindow{mw("5h", 300, 20, fiveA, "model-a"), mw("7d", 10080, 40, weekB, "Model-B")}},
	}
	mergedGeneric := mergeUsageAccounts(generic, now)
	if len(mergedGeneric) != 1 || mergedGeneric[0].Name != "Alpha" || !slices.Equal(mergedGeneric[0].Machines, []string{"node-a", "node-b"}) || len(mergedGeneric[0].Windows) != 2 {
		t.Fatalf("expected generic model-only multi-machine merge: %+v", mergedGeneric)
	}
	if mergedGeneric[0].Windows[0].UsedPercent != 20 || mergedGeneric[0].Windows[1].UsedPercent != 40 {
		t.Fatalf("expected newest generic window values: %+v", mergedGeneric[0].Windows)
	}
	// Permutation invariance across input order.
	reversed := mergeUsageAccounts([]proto.UsageAccount{generic[1], generic[0]}, now)
	if len(reversed) != 1 || reversed[0].ID != mergedGeneric[0].ID || reversed[0].Name != mergedGeneric[0].Name {
		t.Fatalf("order changed merged row: %+v vs %+v", reversed, mergedGeneric)
	}
}

func TestMergeUsageAccountsKeepsDistinctModelOnlyAccountsApart(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	week := now.Add(3 * 24 * time.Hour)
	mw := func(used float64, resets time.Time, model string) proto.UsageWindow {
		w := uw("7d", 10080, used, resets)
		w.Model = model
		return w
	}

	// Different reset times across machines remain distinct accounts.
	diffResets := mergeUsageAccounts([]proto.UsageAccount{
		{Provider: "agy", Name: "Antigravity", Local: true, Origin: "node-a/0", Machines: []string{"node-a"}, SampledAt: now, Windows: []proto.UsageWindow{mw(10, week, "Gemini Models")}},
		{Provider: "agy", Name: "Antigravity", Local: true, Origin: "node-b/0", Machines: []string{"node-b"}, SampledAt: now, Windows: []proto.UsageWindow{mw(20, week.Add(time.Hour), "Gemini Models")}},
	}, now)
	if len(diffResets) != 2 {
		t.Fatalf("accounts with different resets must stay separate: %+v", diffResets)
	}

	// Same reset time on different model pools must stay separate.
	diffModels := mergeUsageAccounts([]proto.UsageAccount{
		{Provider: "acct-alpha", Name: "Alpha", Local: true, Origin: "node-a/0", Machines: []string{"node-a"}, SampledAt: now, Windows: []proto.UsageWindow{mw(10, week, "model-a")}},
		{Provider: "acct-alpha", Name: "Alpha", Local: true, Origin: "node-b/0", Machines: []string{"node-b"}, SampledAt: now, Windows: []proto.UsageWindow{mw(20, week, "model-b")}},
	}, now)
	if len(diffModels) != 2 {
		t.Fatalf("accounts with different model scopes must stay separate: %+v", diffModels)
	}

	// An ambiguous remote sample matching two distinct model-only local sources on one machine must conservatively stay separate.
	ambiguousLocal := []proto.UsageAccount{
		{Provider: "acct-alpha", Name: "Seat", Local: true, Origin: "node-a/0", Machines: []string{"node-a"}, SampledAt: now, Windows: []proto.UsageWindow{mw(10, week, "model-a")}},
		{Provider: "acct-alpha", Name: "Seat", Local: true, Origin: "node-b/0", Machines: []string{"node-b"}, SampledAt: now.Add(time.Minute), Windows: []proto.UsageWindow{mw(15, week, "model-a")}},
		{Provider: "acct-alpha", Name: "Seat", Local: true, Origin: "node-a/1", Machines: []string{"node-a"}, SampledAt: now, Windows: []proto.UsageWindow{mw(20, week, "model-a")}},
	}
	for _, perm := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		got := mergeUsageAccounts([]proto.UsageAccount{ambiguousLocal[perm[0]], ambiguousLocal[perm[1]], ambiguousLocal[perm[2]]}, now)
		if len(got) != 3 {
			t.Fatalf("ambiguous remote sample must stay separate across permutation %v: got %d: %+v", perm, len(got), got)
		}
	}

	// Distinct gateway seats with only per-model windows and matching resets stay separate.
	gatewaySeats := mergeUsageAccounts([]proto.UsageAccount{
		{Provider: "acct-alpha", Name: "Seat A", Ref: "gw1:a", Origin: "gw1:a", Sources: []string{"Relay"}, SampledAt: now, Windows: []proto.UsageWindow{mw(10, week, "model-a")}},
		{Provider: "acct-alpha", Name: "Seat B", Ref: "gw1:b", Origin: "gw1:b", Sources: []string{"Relay"}, SampledAt: now, Windows: []proto.UsageWindow{mw(20, week, "model-a")}},
	}, now)
	if len(gatewaySeats) != 2 {
		t.Fatalf("two seats of one gateway must stay separate: %+v", gatewaySeats)
	}

	// A plan-windowed account and a model-only account sharing only a model window reset must stay separate.
	planVsModelOnly := mergeUsageAccounts([]proto.UsageAccount{
		{Provider: "claude", Name: "Plan", Local: true, Origin: "node-a/0", Machines: []string{"node-a"}, SampledAt: now,
			Windows: []proto.UsageWindow{uw("7d", 10080, 10, week.Add(time.Hour)), mw(90, week, "model-a")}},
		{Provider: "claude", Name: "ModelOnly", Local: true, Origin: "node-b/0", Machines: []string{"node-b"}, SampledAt: now,
			Windows: []proto.UsageWindow{mw(50, week, "model-a")}},
	}, now)
	if len(planVsModelOnly) != 2 {
		t.Fatalf("plan-windowed account must not merge with model-only account via per-model window: %+v", planVsModelOnly)
	}

	// Two local Antigravity machines merge first, then reconcile cleanly with an external Antigravity source.
	nodeA := proto.UsageAccount{Provider: "agy", Name: "Antigravity", Local: true, Origin: "node-a/0", Machines: []string{"node-a"}, SampledAt: now.Add(-2 * time.Minute),
		Windows: []proto.UsageWindow{mw(100, week, "Claude and GPT models"), mw(30, week, "Gemini Models")}}
	nodeB := proto.UsageAccount{Provider: "agy", Name: "Antigravity", Local: true, Origin: "node-b/0", Machines: []string{"node-b"}, SampledAt: now,
		Windows: []proto.UsageWindow{mw(100, week, "Claude and GPT models"), mw(35, week, "Gemini Models")}}
	extGemini := proto.UsageAccount{Provider: "antigravity", Name: "Antigravity · Gemini", Pass: true, Origin: "ext#0", Sources: []string{"External"}, SampledAt: now.Add(-time.Minute),
		Windows: []proto.UsageWindow{uw("7d", 10080, 32, week)}}
	reconciled := usageWindowsAnswer(mergeUsageAccounts([]proto.UsageAccount{nodeA, nodeB, extGemini}, now), nil, now)["accounts"].([]proto.UsageAccount)
	if len(reconciled) != 2 {
		t.Fatalf("expected 1 external Gemini row + 1 merged local Claude/GPT row, got %d: %+v", len(reconciled), reconciled)
	}
	var gotLocal, gotExt *proto.UsageAccount
	for i := range reconciled {
		if reconciled[i].Pass {
			gotExt = &reconciled[i]
		} else {
			gotLocal = &reconciled[i]
		}
	}
	if gotExt == nil || gotLocal == nil || !slices.Equal(gotExt.Machines, []string{"node-a", "node-b"}) || gotExt.Windows[0].UsedPercent != 35 {
		t.Fatalf("external row did not receive merged multi-machine reading: ext=%+v local=%+v", gotExt, gotLocal)
	}
	if gotLocal.Name != "Antigravity" || len(gotLocal.Windows) != 1 || gotLocal.Windows[0].Model != "Claude and GPT models" || !slices.Equal(gotLocal.Machines, []string{"node-a", "node-b"}) {
		t.Fatalf("remaining local pool should be one merged row: %+v", gotLocal)
	}
}

func TestMergeUsageAccountsDoesNotClobberHealthyPoolOnSharedModelReset(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	tG := now.Add(3 * 24 * time.Hour)
	tC0 := now.Add(4 * 24 * time.Hour)
	tC1 := now.Add(5 * 24 * time.Hour)
	mw := func(used float64, resets time.Time, model string) proto.UsageWindow {
		w := uw("7d", 10080, used, resets)
		w.Model = model
		return w
	}

	// A0 and A1 are distinct local accounts on node-a sharing Gemini 7d reset tG but with different Claude 7d resets.
	// B on node-b is a newer reading of A1 (Claude exhausted 100% at tC1). Across all input permutations, B must merge
	// with A1 and never first-hit union into A0 or overwrite A0's healthy Claude 0% pool.
	a0 := proto.UsageAccount{
		Provider: "agy", Name: "Antigravity", Local: true, Origin: "node-a/0", Machines: []string{"node-a"},
		SampledAt: now.Add(-5 * time.Minute),
		Windows:   []proto.UsageWindow{mw(20, tG, "Gemini Models"), mw(0, tC0, "Claude and GPT models")},
	}
	a1 := proto.UsageAccount{
		Provider: "agy", Name: "Antigravity", Local: true, Origin: "node-a/1", Machines: []string{"node-a"},
		SampledAt: now.Add(-3 * time.Minute),
		Windows:   []proto.UsageWindow{mw(20, tG, "Gemini Models"), mw(100, tC1, "Claude and GPT models")},
	}
	b := proto.UsageAccount{
		Provider: "agy", Name: "Antigravity", Local: true, Origin: "node-b/0", Machines: []string{"node-b"},
		SampledAt: now.Add(-time.Minute),
		Windows:   []proto.UsageWindow{mw(25, tG, "Gemini Models"), mw(100, tC1, "Claude and GPT models")},
	}
	base := []proto.UsageAccount{a0, a1, b}
	perms := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}

	var wantSummary map[string]string
	for _, perm := range perms {
		in := []proto.UsageAccount{base[perm[0]], base[perm[1]], base[perm[2]]}
		got := mergeUsageAccounts(in, now)
		if len(got) != 2 {
			t.Fatalf("perm %v: want 2 accounts (healthy A0 and merged A1+B), got %d: %+v", perm, len(got), got)
		}
		summary := map[string]string{}
		var healthy, exhausted *proto.UsageAccount
		for i := range got {
			summary[got[i].Origin] = got[i].ID + "|" + got[i].Name
			switch got[i].Origin {
			case "node-a/0":
				healthy = &got[i]
			case "node-a/1":
				exhausted = &got[i]
			}
		}
		if healthy == nil || exhausted == nil {
			t.Fatalf("perm %v: expected origins node-a/0 and node-a/1, got %+v", perm, got)
		}
		if !slices.Equal(healthy.Machines, []string{"node-a"}) {
			t.Fatalf("perm %v: healthy A0 merged with remote node: %+v", perm, healthy)
		}
		if !slices.Equal(exhausted.Machines, []string{"node-a", "node-b"}) {
			t.Fatalf("perm %v: A1 and B did not merge: %+v", perm, exhausted)
		}
		windowOf := func(acct *proto.UsageAccount, model string) proto.UsageWindow {
			for _, w := range acct.Windows {
				if w.Model == model {
					return w
				}
			}
			t.Fatalf("perm %v: missing %q window in %+v", perm, model, acct)
			return proto.UsageWindow{}
		}
		healthyClaude := windowOf(healthy, "Claude and GPT models")
		if healthyClaude.UsedPercent != 0 || healthyClaude.ResetsAt == nil || !healthyClaude.ResetsAt.Equal(tC0) {
			t.Fatalf("perm %v: healthy A0 Claude pool was clobbered: %+v", perm, healthyClaude)
		}
		healthyGemini := windowOf(healthy, "Gemini Models")
		if healthyGemini.UsedPercent != 20 {
			t.Fatalf("perm %v: healthy A0 Gemini pool changed: %+v", perm, healthyGemini)
		}
		mergedClaude := windowOf(exhausted, "Claude and GPT models")
		mergedGemini := windowOf(exhausted, "Gemini Models")
		if mergedClaude.UsedPercent != 100 || !mergedClaude.ResetsAt.Equal(tC1) || mergedGemini.UsedPercent != 25 {
			t.Fatalf("perm %v: merged A1+B windows unexpected: claude=%+v gemini=%+v", perm, mergedClaude, mergedGemini)
		}
		if wantSummary == nil {
			wantSummary = summary
		} else if !maps.Equal(summary, wantSummary) {
			t.Fatalf("perm %v: order changed IDs or names: got %v, want %v", perm, summary, wantSummary)
		}
	}

	// Two accounts on different machines sharing one model reset (tG) but disagreeing on another pool (tC0 vs tC1)
	// must stay separate regardless of order so the newer reading cannot overwrite the healthy pool.
	for _, pair := range [][]proto.UsageAccount{{a0, b}, {b, a0}} {
		got := mergeUsageAccounts(pair, now)
		if len(got) != 2 {
			t.Fatalf("conflicting sibling pool resets must stay separate: %+v", got)
		}
	}

	// A partial remote sample (Gemini only) matching two conflicting accounts must stay conservatively separate
	// across every permutation and must not bridge them either.
	partial := proto.UsageAccount{
		Provider: "agy", Name: "Antigravity", Local: true, Origin: "node-c/0", Machines: []string{"node-c"},
		SampledAt: now,
		Windows:   []proto.UsageWindow{mw(50, tG, "Gemini Models")},
	}
	ambig := []proto.UsageAccount{a0, b, partial}
	for _, perm := range perms {
		got := mergeUsageAccounts([]proto.UsageAccount{ambig[perm[0]], ambig[perm[1]], ambig[perm[2]]}, now)
		if len(got) != 3 {
			t.Fatalf("perm %v: partial sample matching conflicting accounts must stay separate: got %d: %+v", perm, len(got), got)
		}
	}

	// End-to-end through usageWindowsHub.poll: node-a reports both A0 and A1 (assigned origins node-a/0 and node-a/1)
	// while node-b reports newer B; poll must keep A0's healthy Claude 0% pool intact and merge B with A1.
	var hub usageWindowsHub
	polled := hub.poll(context.Background(), []usageSource{
		{id: "node-a", name: "node-a", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
			return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{
				{UsageAccount: proto.UsageAccount{Provider: "agy", Name: "Antigravity", SampledAt: now.Add(-5 * time.Minute), Windows: a0.Windows}},
				{UsageAccount: proto.UsageAccount{Provider: "agy", Name: "Antigravity", SampledAt: now.Add(-3 * time.Minute), Windows: a1.Windows}},
			}}, nil
		}},
		{id: "node-b", name: "node-b", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
			return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{
				{UsageAccount: proto.UsageAccount{Provider: "agy", Name: "Antigravity", SampledAt: now.Add(-time.Minute), Windows: b.Windows}},
			}}, nil
		}},
	}, now)
	if len(polled) != 2 {
		t.Fatalf("poll: want 2 accounts (healthy A0 and merged A1+B), got %d: %+v", len(polled), polled)
	}
	healthyFound := false
	for _, acct := range polled {
		for _, w := range acct.Windows {
			if w.Model == "Claude and GPT models" && w.UsedPercent == 0 && w.ResetsAt != nil && w.ResetsAt.Equal(tC0) && slices.Equal(acct.Machines, []string{"node-a"}) {
				healthyFound = true
			}
		}
	}
	if !healthyFound {
		t.Fatalf("poll: healthy A0 Claude pool was not preserved intact: %+v", polled)
	}
}

func TestMergeUsageAccountsAntigravityNamedAccountAndPlan(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	fiveG, weekG := now.Add(2*time.Hour), now.Add(3*24*time.Hour)
	fiveC, weekC := now.Add(3*time.Hour), now.Add(4*24*time.Hour)
	mw := func(label string, minutes int, used float64, resets time.Time, model string) proto.UsageWindow {
		w := uw(label, minutes, used, resets)
		w.Model = model
		return w
	}
	legacyWins := []proto.UsageWindow{
		mw("5h", 300, 20, fiveG, "Gemini Models"),
		mw("7d", 10080, 30, weekG, "Gemini Models"),
		mw("5h", 300, 5, fiveC, "Claude and GPT models"),
		mw("7d", 10080, 10, weekC, "Claude and GPT models"),
	}
	namedWins := []proto.UsageWindow{
		mw("5h", 300, 25, fiveG, "Gemini Models"),
		mw("7d", 10080, 35, weekG, "Gemini Models"),
		mw("5h", 300, 5, fiveC, "Claude and GPT models"),
		mw("7d", 10080, 10, weekC, "Claude and GPT models"),
	}

	// Row ID stays stable when a native agy account gains its real name and plan.
	legacyOnly := mergeUsageAccounts([]proto.UsageAccount{{
		Provider: "agy", Name: "Antigravity", Local: true, Origin: "node-a/0", Machines: []string{"node-a"},
		SampledAt: now.Add(-2 * time.Minute), Windows: legacyWins,
	}}, now)
	namedOnly := mergeUsageAccounts([]proto.UsageAccount{{
		Provider: "agy", Name: "alice@example.com", Plan: "Google AI Ultra", Local: true, Origin: "node-b/0", Machines: []string{"node-b"},
		SampledAt: now.Add(-time.Minute), Windows: namedWins,
	}}, now)
	if len(legacyOnly) != 1 || len(namedOnly) != 1 || legacyOnly[0].ID != namedOnly[0].ID {
		t.Fatalf("expected stable row ID across unnamed and named agy samples: legacy=%+v named=%+v", legacyOnly, namedOnly)
	}

	// Rolling upgrade across two machines: whichever sample is newer, merged row preserves the account name and plan.
	for _, tc := range []struct {
		name     string
		legacyAt time.Time
		namedAt  time.Time
		wantUsed float64
	}{
		{"named newer than legacy", now.Add(-4 * time.Minute), now.Add(-time.Minute), 25},
		{"legacy newer than named", now.Add(-time.Minute), now.Add(-4 * time.Minute), 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeUsageAccounts([]proto.UsageAccount{
				{Provider: "agy", Name: "Antigravity", Local: true, Origin: "node-a/0", Machines: []string{"node-a"}, SampledAt: tc.legacyAt, Windows: legacyWins},
				{Provider: "agy", Name: "alice@example.com", Plan: "Google AI Ultra", Local: true, Origin: "node-b/0", Machines: []string{"node-b"}, SampledAt: tc.namedAt, Windows: namedWins},
			}, now)
			if len(got) != 1 {
				t.Fatalf("expected 1 merged account, got %d: %+v", len(got), got)
			}
			acct := got[0]
			if acct.Provider != "antigravity" || acct.Name != "alice@example.com" || acct.Plan != "Google AI Ultra" {
				t.Fatalf("unexpected merged identity: %+v", acct)
			}
			if !slices.Equal(acct.Machines, []string{"node-a", "node-b"}) {
				t.Fatalf("expected both machines, got %v", acct.Machines)
			}
			if acct.ID != legacyOnly[0].ID {
				t.Fatalf("merged ID changed from %q to %q", legacyOnly[0].ID, acct.ID)
			}
			var gotGemini5h float64
			for _, w := range acct.Windows {
				if w.Label == "5h" && w.Model == "Gemini Models" {
					gotGemini5h = w.UsedPercent
				}
			}
			if gotGemini5h != tc.wantUsed {
				t.Fatalf("expected newest 5h Gemini used=%v, got %v", tc.wantUsed, gotGemini5h)
			}
		})
	}

	// External Antigravity reconciliation folds matching windows and retains the local account name and plan on unmatched pools.
	localNamed := proto.UsageAccount{
		Provider: "agy", Name: "alice@example.com", Plan: "Google AI Ultra", Local: true, Origin: "node-a/0", Machines: []string{"node-a"},
		SampledAt: now, Windows: []proto.UsageWindow{mw("7d", 10080, 35, weekG, "Gemini Models"), mw("7d", 10080, 10, weekC, "Claude and GPT models")},
	}
	extGemini := proto.UsageAccount{
		Provider: "antigravity", Name: "Antigravity · Gemini", Pass: true, Origin: "ext#0", Sources: []string{"External"},
		SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 30, weekG)},
	}
	reconciled := reconcileAntigravityUsage(mergeUsageAccounts([]proto.UsageAccount{localNamed, extGemini}, now), now)
	if len(reconciled) != 2 {
		t.Fatalf("expected external row + remaining local pool, got %d: %+v", len(reconciled), reconciled)
	}
	var gotLocal, gotExt *proto.UsageAccount
	for i := range reconciled {
		if reconciled[i].Pass {
			gotExt = &reconciled[i]
		} else {
			gotLocal = &reconciled[i]
		}
	}
	if gotExt == nil || gotLocal == nil || gotExt.Windows[0].UsedPercent != 35 {
		t.Fatalf("external row did not reconcile with named local account: ext=%+v local=%+v", gotExt, gotLocal)
	}
	if gotLocal.Name != "alice@example.com" || gotLocal.Plan != "Google AI Ultra" || len(gotLocal.Windows) != 1 || gotLocal.Windows[0].Model != "Claude and GPT models" {
		t.Fatalf("remaining local pool lost account identity: %+v", gotLocal)
	}

	// Two distinct named Antigravity accounts remain separate even when their reset timestamps match to the second
	// (for example, members of a shared Google One family pool) or when AccountKeys differ, and preserve deterministic IDs.
	sharedResetA := proto.UsageAccount{
		Provider: "agy", Name: "alice@example.com", Plan: "Google AI Ultra", CodexAccountKey: "agy-key-alice",
		Local: true, Origin: "node-a/0", Machines: []string{"node-a"}, SampledAt: now.Add(-time.Minute),
		Windows: []proto.UsageWindow{mw("5h", 300, 12, fiveG, "Gemini Models"), mw("7d", 10080, 40, weekG, "Gemini Models")},
	}
	sharedResetB := proto.UsageAccount{
		Provider: "agy", Name: "bob@example.com", Plan: "Google AI Ultra", CodexAccountKey: "agy-key-bob",
		Local: true, Origin: "node-b/0", Machines: []string{"node-b"}, SampledAt: now,
		Windows: []proto.UsageWindow{mw("5h", 300, 12, fiveG, "Gemini Models"), mw("7d", 10080, 40, weekG, "Gemini Models")},
	}
	forwardShared := mergeUsageAccounts([]proto.UsageAccount{sharedResetA, sharedResetB}, now)
	reverseShared := mergeUsageAccounts([]proto.UsageAccount{sharedResetB, sharedResetA}, now)
	if len(forwardShared) != 2 || len(reverseShared) != 2 {
		t.Fatalf("expected 2 separate accounts on shared reset times, got %+v / %+v", forwardShared, reverseShared)
	}
	byName := map[string]proto.UsageAccount{}
	for _, acct := range forwardShared {
		byName[acct.Name] = acct
	}
	if !slices.Equal(byName["alice@example.com"].Machines, []string{"node-a"}) || !slices.Equal(byName["bob@example.com"].Machines, []string{"node-b"}) {
		t.Fatalf("distinct Antigravity accounts merged machines: %+v", forwardShared)
	}
	for _, acct := range reverseShared {
		if acct.ID != byName[acct.Name].ID {
			t.Fatalf("input order changed ID for %s: %q vs %q", acct.Name, acct.ID, byName[acct.Name].ID)
		}
	}

	// Same Antigravity account across two machines merges even when all windows are untouched (nil ResetsAt).
	idleSame := mergeUsageAccounts([]proto.UsageAccount{
		{
			Provider: "agy", Name: "alice@example.com", Plan: "Google AI Ultra", CodexAccountKey: "agy-key-alice",
			Local: true, Origin: "node-a/0", Machines: []string{"node-a"}, SampledAt: now.Add(-2 * time.Minute),
			Windows: []proto.UsageWindow{{Label: "5h", WindowMinutes: 300, UsedPercent: 0, Model: "Gemini Models"}},
		},
		{
			Provider: "agy", Name: "alice@example.com", Plan: "Google AI Ultra", CodexAccountKey: "agy-key-alice",
			Local: true, Origin: "node-b/0", Machines: []string{"node-b"}, SampledAt: now,
			Windows: []proto.UsageWindow{{Label: "5h", WindowMinutes: 300, UsedPercent: 0, Model: "Gemini Models"}},
		},
	}, now)
	if len(idleSame) != 1 || !slices.Equal(idleSame[0].Machines, []string{"node-a", "node-b"}) {
		t.Fatalf("expected idle same-account Antigravity samples to merge across machines, got %+v", idleSame)
	}
}

func TestMergeUsageAccountsDeduplicatesLocalIntoPassExtension(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	five := now.Add(2 * time.Hour)
	week := now.Add(3 * 24 * time.Hour)
	p95, p90 := 95.0, 90.0

	makeExt := func(sampled time.Time) proto.UsageAccount {
		w5 := uw("5h", 300, 25, five)
		w5.Group, w5.Title, w5.Note, w5.ProtectPercent = "Primary", "5-hour", "Rolling", &p95
		w7 := uw("7d", 10080, 40, week)
		w7.Group, w7.Title, w7.Note, w7.ProtectPercent = "Weekly", "7-day", "Plan", &p90
		return proto.UsageAccount{
			Provider:  "codex",
			Name:      "Work Codex",
			Plan:      "Pro",
			Headline:  "Active seat",
			Pass:      true,
			Origin:    "ext-src#0",
			Sources:   []string{"Extension"},
			SampledAt: sampled,
			Windows:   []proto.UsageWindow{w5, w7},
		}
	}

	t.Run("fresh local and pass extension with matching resets merge", func(t *testing.T) {
		localAcct := proto.UsageAccount{
			Provider:        "codex",
			Name:            "Codex Pro",
			Plan:            "pro",
			Local:           true,
			Origin:          "workstation/0",
			Machines:        []string{"workstation"},
			CodexAccountKey: "acct-alpha",
			SampledAt:       now.Add(-time.Minute),
			Windows:         []proto.UsageWindow{uw("5h", 300, 28, five.Add(15*time.Second)), uw("7d", 10080, 42, week.Add(-10*time.Second))},
		}
		ext := makeExt(now.Add(-4 * time.Minute))
		wantID := usageRowID("codex", "extension|ext-src#0")

		for _, pair := range [][]proto.UsageAccount{{localAcct, ext}, {ext, localAcct}} {
			got := mergeUsageAccounts(pair, now)
			if len(got) != 1 {
				t.Fatalf("want 1 merged account, got %d: %+v", len(got), got)
			}
			a := got[0]
			if a.ID != wantID || !a.Pass || a.Name != "Work Codex" || a.Plan != "Pro" || a.Headline != "Active seat" {
				t.Fatalf("extension identity or metadata lost: %+v", a)
			}
			if !a.Local || !slices.Equal(a.Machines, []string{"workstation"}) || !slices.Equal(a.Sources, []string{"Extension"}) {
				t.Fatalf("combined local and extension sources missing: %+v", a)
			}
			if a.Stale || !a.SampledAt.Equal(now.Add(-time.Minute)) || a.StaleAt == nil || !a.StaleAt.Equal(now.Add(-time.Minute+usageHubStaleAfter)) {
				t.Fatalf("expected fresher local sample time and non-stale state: %+v", a)
			}
			if len(a.Windows) != 2 || a.Windows[0].UsedPercent != 28 || a.Windows[1].UsedPercent != 42 ||
				a.Windows[0].Group != "Primary" || a.Windows[0].Title != "5-hour" || a.Windows[0].Note != "Rolling" || a.Windows[0].ProtectPercent != &p95 ||
				a.Windows[1].Group != "Weekly" || a.Windows[1].Title != "7-day" || a.Windows[1].Note != "Plan" || a.Windows[1].ProtectPercent != &p90 {
				t.Fatalf("expected fresher percentages with preserved extension window metadata: %+v", a.Windows)
			}
		}
		if ext.Windows[0].UsedPercent != 25 || ext.Windows[1].UsedPercent != 40 {
			t.Fatalf("mergeUsageAccounts mutated caller extension slice: %+v", ext.Windows)
		}
	})

	t.Run("fresher extension keeps extension numbers while absorbing local machine", func(t *testing.T) {
		localAcct := proto.UsageAccount{
			Provider:        "codex",
			Name:            "Codex Pro",
			Plan:            "pro",
			Local:           true,
			Origin:          "workstation/0",
			Machines:        []string{"workstation"},
			CodexAccountKey: "acct-alpha",
			SampledAt:       now.Add(-10 * time.Minute),
			Windows:         []proto.UsageWindow{uw("5h", 300, 19, five.Add(20*time.Second)), uw("7d", 10080, 33, week.Add(-20*time.Second))},
		}
		ext := makeExt(now.Add(-2 * time.Minute))
		got := mergeUsageAccounts([]proto.UsageAccount{localAcct, ext}, now)
		if len(got) != 1 {
			t.Fatalf("want 1 merged account, got %d: %+v", len(got), got)
		}
		a := got[0]
		if a.Stale || !a.SampledAt.Equal(now.Add(-2*time.Minute)) || a.StaleAt == nil || !a.StaleAt.Equal(now.Add(-2*time.Minute+usageGatewayStaleAfter)) {
			t.Fatalf("expected fresher extension timestamp and stale_at: %+v", a)
		}
		if a.Windows[0].UsedPercent != 25 || a.Windows[1].UsedPercent != 40 || !a.Local || !slices.Equal(a.Machines, []string{"workstation"}) {
			t.Fatalf("expected extension windows with local machine attached: %+v", a)
		}
	})

	t.Run("stale local with future 7d reset merges into live extension without turning stale", func(t *testing.T) {
		localStale := proto.UsageAccount{
			Provider:        "codex",
			Name:            "Codex Pro",
			Plan:            "pro",
			Local:           true,
			Origin:          "workstation/0",
			Machines:        []string{"workstation"},
			CodexAccountKey: "acct-alpha",
			SampledAt:       now.Add(-4 * time.Hour),
			Windows: []proto.UsageWindow{
				uw("5h", 300, 88, now.Add(-time.Hour)), // expired before now: dropped during live filtering
				uw("7d", 10080, 31, week),              // still resets in the future
			},
		}
		ext := makeExt(now.Add(-4 * time.Minute))
		extWeek := week.Add(5 * time.Second)
		ext.Windows[1].ResetsAt = &extWeek

		got := mergeUsageAccounts([]proto.UsageAccount{localStale, ext}, now)
		if len(got) != 1 {
			t.Fatalf("want stale local merged into live extension, got %d: %+v", len(got), got)
		}
		a := got[0]
		if a.ID != usageRowID("codex", "extension|ext-src#0") || a.Name != "Work Codex" || a.Stale {
			t.Fatalf("merged stale local should not make fresh extension row stale: %+v", a)
		}
		if !a.SampledAt.Equal(now.Add(-4*time.Minute)) || a.StaleAt == nil || !a.StaleAt.Equal(now.Add(-4*time.Minute+usageGatewayStaleAfter)) {
			t.Fatalf("expected extension sample time and stale_at: %+v", a)
		}
		if len(a.Windows) != 2 || a.Windows[0].UsedPercent != 25 || a.Windows[1].UsedPercent != 40 || a.Windows[1].Title != "7-day" {
			t.Fatalf("expected fresh extension 5h and 7d windows intact: %+v", a.Windows)
		}
		if !a.Local || !slices.Equal(a.Machines, []string{"workstation"}) || !slices.Equal(a.Sources, []string{"Extension"}) {
			t.Fatalf("expected combined sources: %+v", a)
		}
	})

	t.Run("fresh local replaces stale failed extension reading", func(t *testing.T) {
		localFresh := proto.UsageAccount{
			Provider:        "codex",
			Name:            "Codex Pro",
			Local:           true,
			Origin:          "workstation/0",
			Machines:        []string{"workstation"},
			CodexAccountKey: "acct-alpha",
			SampledAt:       now.Add(-time.Minute),
			Windows:         []proto.UsageWindow{uw("5h", 300, 44, five), uw("7d", 10080, 51, week)},
		}
		extStale := makeExt(now.Add(-10 * time.Minute))
		extStale.Stale = true
		extStale.Problem = "unreachable"

		got := mergeUsageAccounts([]proto.UsageAccount{localFresh, extStale}, now)
		if len(got) != 1 {
			t.Fatalf("want 1 merged account, got %d: %+v", len(got), got)
		}
		a := got[0]
		if a.Stale || a.Problem != "" || !a.SampledAt.Equal(now.Add(-time.Minute)) || a.Windows[0].UsedPercent != 44 || a.Windows[1].UsedPercent != 51 {
			t.Fatalf("fresh local should refresh stale failed extension row: %+v", a)
		}
		if a.ID != usageRowID("codex", "extension|ext-src#0") || a.Name != "Work Codex" || a.Windows[0].Title != "5-hour" {
			t.Fatalf("extension identity or window metadata lost: %+v", a)
		}
	})

	t.Run("poll end-to-end deduplicates stale local codex node against live extension", func(t *testing.T) {
		var hub usageWindowsHub
		hub.gateways = func(time.Time) []proto.UsageAccount {
			return []proto.UsageAccount{makeExt(now.Add(-4 * time.Minute))}
		}
		node := usageSource{id: "m1", name: "Workstation", online: true, ask: func(context.Context) (proto.UsageSnapshot, error) {
			return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{
				AccountKey: "acct-alpha",
				UsageAccount: proto.UsageAccount{
					Provider:  "codex",
					Name:      "Codex Pro",
					Plan:      "pro",
					SampledAt: now.Add(-4 * time.Hour),
					Windows:   []proto.UsageWindow{uw("7d", 10080, 31, week.Add(-3*time.Second))},
				},
			}}}, nil
		}}
		polled := hub.poll(context.Background(), []usageSource{node}, now)
		got := usageWindowsAnswer(polled, nil, now)["accounts"].([]proto.UsageAccount)
		if len(got) != 1 || got[0].Name != "Work Codex" || got[0].Stale || !got[0].Local ||
			!slices.Equal(got[0].Machines, []string{"Workstation"}) || !slices.Equal(got[0].Sources, []string{"Extension"}) {
			t.Fatalf("expected 1 merged live account from poll+answer, got %+v", got)
		}
	})

	t.Run("unmatched local sibling does not get numbered after matched sibling absorbs into extension", func(t *testing.T) {
		matchedLocal := proto.UsageAccount{
			Provider: "codex", Name: "Codex Pro", Local: true, Origin: "node-a/0", Machines: []string{"node-a"},
			CodexAccountKey: "acct-alpha", SampledAt: now.Add(-2 * time.Hour),
			Windows: []proto.UsageWindow{uw("7d", 10080, 30, week)},
		}
		otherLocal := proto.UsageAccount{
			Provider: "codex", Name: "Codex Pro", Local: true, Origin: "node-b/0", Machines: []string{"node-b"},
			CodexAccountKey: "acct-beta", SampledAt: now.Add(-time.Minute),
			Windows: []proto.UsageWindow{uw("7d", 10080, 55, week.Add(24*time.Hour))},
		}
		ext := makeExt(now.Add(-2 * time.Minute))

		got := mergeUsageAccounts([]proto.UsageAccount{matchedLocal, otherLocal, ext}, now)
		if len(got) != 2 {
			t.Fatalf("want 2 accounts, got %d: %+v", len(got), got)
		}
		if got[0].Name != "Codex Pro" || !slices.Equal(got[0].Machines, []string{"node-b"}) {
			t.Fatalf("unmatched local account should keep clean name without suffix: %+v", got[0])
		}
		if got[1].Name != "Work Codex" || !got[1].Local || !slices.Equal(got[1].Machines, []string{"node-a"}) {
			t.Fatalf("matched extension account unexpected: %+v", got[1])
		}
	})
}

func TestMergeUsageAccountsRefusesAmbiguousOrDistinctPassMerges(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	five := now.Add(2 * time.Hour)
	week := now.Add(3 * 24 * time.Hour)

	passCodex := func(origin, name string, wins ...proto.UsageWindow) proto.UsageAccount {
		return proto.UsageAccount{
			Provider:  "codex",
			Name:      name,
			Pass:      true,
			Origin:    origin,
			Sources:   []string{"Extension"},
			SampledAt: now.Add(-2 * time.Minute),
			Windows:   wins,
		}
	}

	t.Run("two extension accounts sharing reset time stay unmerged", func(t *testing.T) {
		localAcct := proto.UsageAccount{
			Provider: "codex", Name: "Codex Pro", Local: true, Origin: "node-a/0", Machines: []string{"node-a"},
			CodexAccountKey: "acct-alpha", SampledAt: now.Add(-4 * time.Hour),
			Windows: []proto.UsageWindow{uw("7d", 10080, 30, week)},
		}
		seatA := passCodex("ext#0", "Seat A", uw("7d", 10080, 35, week))
		seatB := passCodex("ext#1", "Seat B", uw("7d", 10080, 60, week.Add(20*time.Second)))

		got := mergeUsageAccounts([]proto.UsageAccount{localAcct, seatA, seatB}, now)
		if len(got) != 3 {
			t.Fatalf("ambiguous extension seats must not merge with local sample: got %d: %+v", len(got), got)
		}
		for _, a := range got {
			if a.Pass && (a.Local || len(a.Machines) != 0) {
				t.Fatalf("ambiguous pass row must not receive local machines: %+v", a)
			}
		}
	})

	t.Run("two conflicting local codex keys sharing reset time stay unmerged", func(t *testing.T) {
		localA := proto.UsageAccount{
			Provider: "codex", Name: "Codex Pro", Local: true, Origin: "node-a/0", Machines: []string{"node-a"},
			CodexAccountKey: "acct-a", SampledAt: now.Add(-time.Minute),
			Windows: []proto.UsageWindow{uw("7d", 10080, 20, week)},
		}
		localB := proto.UsageAccount{
			Provider: "codex", Name: "Codex Pro", Local: true, Origin: "node-b/0", Machines: []string{"node-b"},
			CodexAccountKey: "acct-b", SampledAt: now.Add(-time.Minute),
			Windows: []proto.UsageWindow{uw("7d", 10080, 50, week)},
		}
		seat := passCodex("ext#0", "Seat A", uw("7d", 10080, 22, week))

		got := mergeUsageAccounts([]proto.UsageAccount{localA, localB, seat}, now)
		if len(got) != 3 {
			t.Fatalf("conflicting local accounts must not merge into one pass row: got %d: %+v", len(got), got)
		}
	})

	t.Run("two local homes on one machine sharing reset time stay unmerged", func(t *testing.T) {
		homeA := proto.UsageAccount{
			Provider: "codex", Name: "Codex Pro", Local: true, Origin: "node-a/source/home-a", Machines: []string{"node-a"},
			SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 10, week)},
		}
		homeB := proto.UsageAccount{
			Provider: "codex", Name: "Codex Pro", Local: true, Origin: "node-a/source/home-b", Machines: []string{"node-a"},
			SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 40, week)},
		}
		seat := passCodex("ext#0", "Seat A", uw("7d", 10080, 12, week))

		got := mergeUsageAccounts([]proto.UsageAccount{homeA, homeB, seat}, now)
		if len(got) != 3 {
			t.Fatalf("two local homes on one machine must not merge into one pass row: got %d: %+v", len(got), got)
		}
	})

	t.Run("different reset times stay separate", func(t *testing.T) {
		localAcct := proto.UsageAccount{
			Provider: "codex", Name: "Codex Pro", Local: true, Origin: "node-a/0", Machines: []string{"node-a"},
			CodexAccountKey: "acct-alpha", SampledAt: now.Add(-time.Minute),
			Windows: []proto.UsageWindow{uw("7d", 10080, 20, week)},
		}
		for _, delta := range []time.Duration{61 * time.Second, 2 * time.Hour} {
			ext := passCodex("ext#0", "Other Codex", uw("7d", 10080, 20, week.Add(delta)))
			got := mergeUsageAccounts([]proto.UsageAccount{localAcct, ext}, now)
			if len(got) != 2 {
				t.Fatalf("delta %v must keep accounts separate: got %d: %+v", delta, len(got), got)
			}
		}
	})

	t.Run("conflicting 5h reset blocks merge and disambiguates matching seat", func(t *testing.T) {
		localAcct := proto.UsageAccount{
			Provider: "codex", Name: "Codex Pro", Local: true, Origin: "node-a/0", Machines: []string{"node-a"},
			CodexAccountKey: "acct-alpha", SampledAt: now.Add(-5 * time.Minute),
			Windows: []proto.UsageWindow{uw("5h", 300, 15, five), uw("7d", 10080, 30, week)},
		}
		wrongSeat := passCodex("ext#0", "Wrong Seat", uw("5h", 300, 50, five.Add(2*time.Hour)), uw("7d", 10080, 30, week))
		if got := mergeUsageAccounts([]proto.UsageAccount{localAcct, wrongSeat}, now); len(got) != 2 {
			t.Fatalf("conflicting 5h window must prevent merge even with matching 7d reset: %+v", got)
		}

		rightSeat := passCodex("ext#1", "Right Seat", uw("5h", 300, 18, five.Add(5*time.Second)), uw("7d", 10080, 32, week.Add(5*time.Second)))
		got := mergeUsageAccounts([]proto.UsageAccount{localAcct, wrongSeat, rightSeat}, now)
		if len(got) != 2 {
			t.Fatalf("5h window should disambiguate matching seat: got %d: %+v", len(got), got)
		}
		if got[0].Name != "Wrong Seat" || got[0].Local || got[1].Name != "Right Seat" || !got[1].Local || !slices.Equal(got[1].Machines, []string{"node-a"}) {
			t.Fatalf("unexpected disambiguated merge result: %+v", got)
		}
	})

	t.Run("model window conflicts and model-only local accounts do not merge into pass plan rows", func(t *testing.T) {
		localModel := uw("7d", 10080, 10, week)
		localModel.Model = "fast"
		extModel := uw("7d", 10080, 15, week.Add(2*time.Hour))
		extModel.Model = "fast"

		localPlan := proto.UsageAccount{
			Provider: "claude", Name: "Claude Max", Local: true, Origin: "node-a/0", Machines: []string{"node-a"},
			SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 20, week), localModel},
		}
		extPlan := proto.UsageAccount{
			Provider: "claude", Name: "Work Claude", Pass: true, Origin: "ext#0", Sources: []string{"Extension"},
			SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 20, week), extModel},
		}
		if got := mergeUsageAccounts([]proto.UsageAccount{localPlan, extPlan}, now); len(got) != 2 {
			t.Fatalf("conflicting shared model window must prevent merge: %+v", got)
		}

		poolA := uw("7d", 10080, 10, week)
		poolA.Model = "Pool A"
		poolB := uw("7d", 10080, 20, week)
		poolB.Model = "Pool B"
		modelOnlyLocal := proto.UsageAccount{
			Provider: "custom", Name: "Custom", Local: true, Origin: "node-a/0", Machines: []string{"node-a"},
			SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{poolA, poolB},
		}
		splitExt := proto.UsageAccount{
			Provider: "custom", Name: "Custom · Pool A", Pass: true, Origin: "ext#0", Sources: []string{"Extension"},
			SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 10, week)},
		}
		if got := mergeUsageAccounts([]proto.UsageAccount{modelOnlyLocal, splitExt}, now); len(got) != 2 {
			t.Fatalf("model-only local account must not merge into a pass split row: %+v", got)
		}
	})

	t.Run("composite pass row with duplicate plan window lengths never merges", func(t *testing.T) {
		localAcct := proto.UsageAccount{
			Provider: "codex", Name: "Codex Pro", Local: true, Origin: "node-a/0", Machines: []string{"node-a"},
			CodexAccountKey: "acct-alpha", SampledAt: now.Add(-time.Minute),
			Windows: []proto.UsageWindow{uw("7d", 10080, 20, week)},
		}
		composite := passCodex("ext#0", "Combined Pool", uw("7d", 10080, 20, week), uw("7d", 10080, 80, week.Add(24*time.Hour)))
		if got := mergeUsageAccounts([]proto.UsageAccount{localAcct, composite}, now); len(got) != 2 {
			t.Fatalf("composite pass row with duplicate plan window minutes must not merge: %+v", got)
		}
	})

	t.Run("non-local gateway, balance, and other-provider rows stay separate", func(t *testing.T) {
		gwRow := proto.UsageAccount{
			Provider: "codex", Name: "Relay Seat", Ref: "gw1:a", Origin: "gw1:a", Sources: []string{"Relay"},
			SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 20, week)},
		}
		extCodex := passCodex("ext#0", "Work Codex", uw("7d", 10080, 20, week))
		if got := mergeUsageAccounts([]proto.UsageAccount{gwRow, extCodex}, now); len(got) != 2 {
			t.Fatalf("non-local gateway row must not merge into pass row: %+v", got)
		}

		balanceRow := proto.UsageAccount{
			Provider: "codex", Name: "Prepaid", Local: true, Ref: "balance:l1", Origin: "balance:l1",
			SampledAt: now.Add(-time.Minute), Balance: &proto.UsageBalance{Amount: "12.34", Currency: "USD"},
		}
		if got := mergeUsageAccounts([]proto.UsageAccount{balanceRow, extCodex}, now); len(got) != 2 {
			t.Fatalf("balance row must not merge into pass row: %+v", got)
		}

		localCodex := proto.UsageAccount{
			Provider: "codex", Name: "Codex Pro", Local: true, Origin: "node-a/0", Machines: []string{"node-a"},
			CodexAccountKey: "acct-alpha", SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 20, week)},
		}
		extClaude := proto.UsageAccount{
			Provider: "claude", Name: "Work Claude", Pass: true, Origin: "ext#0", Sources: []string{"Extension"},
			SampledAt: now.Add(-time.Minute), Windows: []proto.UsageWindow{uw("7d", 10080, 20, week)},
		}
		if got := mergeUsageAccounts([]proto.UsageAccount{localCodex, extClaude}, now); len(got) != 2 {
			t.Fatalf("different providers sharing reset time must not merge: %+v", got)
		}
	})
}

func TestMergeUsageAccountsPreservesIdleEpochZeroResetWindows(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	five, week := now.Add(2*time.Hour), now.Add(3*24*time.Hour)
	epoch := time.Unix(0, 0).UTC()

	extClaude := proto.UsageAccount{
		Provider:  "antigravity",
		Name:      "Antigravity Claude/GPT",
		Plan:      "Ultra",
		Ref:       "src:agy-3p",
		Origin:    "src:agy-3p",
		Sources:   []string{"Antigravity"},
		SampledAt: now,
		Windows: []proto.UsageWindow{
			uw("5h", 300, 0, epoch),
			uw("7d", 10080, 0, epoch),
		},
	}
	extGemini := proto.UsageAccount{
		Provider:  "antigravity",
		Name:      "Antigravity Gemini",
		Plan:      "Ultra",
		Ref:       "src:agy-gemini",
		Origin:    "src:agy-gemini",
		Sources:   []string{"Antigravity"},
		SampledAt: now,
		Windows: []proto.UsageWindow{
			uw("5h", 300, 8, five),
			uw("7d", 10080, 15, week),
		},
	}
	localAgy := proto.UsageAccount{
		Provider:  "agy",
		Name:      "Antigravity",
		Local:     true,
		Origin:    "host-a/0",
		Machines:  []string{"host-a"},
		SampledAt: now,
		Windows: []proto.UsageWindow{
			{Label: "5h", WindowMinutes: 300, UsedPercent: 0, Model: "Claude and GPT models"},
			{Label: "7d", WindowMinutes: 10080, UsedPercent: 0, Model: "Claude and GPT models"},
			{Label: "5h", WindowMinutes: 300, UsedPercent: 8, ResetsAt: &five, Model: "Gemini Models"},
			{Label: "7d", WindowMinutes: 10080, UsedPercent: 15, ResetsAt: &week, Model: "Gemini Models"},
		},
	}

	merged := mergeUsageAccounts([]proto.UsageAccount{extClaude, extGemini, localAgy}, now)
	var claudeSeat *proto.UsageAccount
	for i := range merged {
		if merged[i].Name == "Antigravity Claude/GPT" {
			claudeSeat = &merged[i]
		}
	}
	if claudeSeat == nil || len(claudeSeat.Windows) != 2 {
		t.Fatalf("expected untouched epoch-zero external seat to survive merge, got %+v", merged)
	}
	for _, w := range claudeSeat.Windows {
		if w.ResetsAt != nil || w.UsedPercent != 0 {
			t.Fatalf("expected nil ResetsAt on untouched epoch-zero window, got %+v", w)
		}
	}
}
