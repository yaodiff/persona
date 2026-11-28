package node

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func init() {
	agyLocalUserStatus = func() (agyUserIdentity, bool) { return agyUserIdentity{}, false }
}

func usageFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "usagewindows", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLastCodexSampleTakesTheNewestOfCodexsOwnLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, usageFixture(t, "codex-rollout.jsonl"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, ok := lastCodexSample(path)
	if !ok {
		t.Fatal("no sample")
	}
	// the 08:20 event is another limit ("premium") and the 08:30 one is the newest of Codex's own
	if want := time.Date(2026, 11, 4, 8, 30, 0, 0, time.UTC); !s.at.Equal(want) {
		t.Errorf("sampled at %v, want %v", s.at, want)
	}
	if s.plan != "plus" || len(s.windows) != 2 {
		t.Fatalf("plan %q windows %+v", s.plan, s.windows)
	}
	w := s.windows[0]
	if w.Label != "5h" || w.WindowMinutes != 300 || w.UsedPercent != 42.5 || w.ResetsAt == nil || w.ResetsAt.Unix() != 1793780000 {
		t.Errorf("5h window = %+v", w)
	}
	if w := s.windows[1]; w.Label != "7d" || w.UsedPercent != 61 {
		t.Errorf("7d window = %+v", w)
	}
}

func TestLastCodexSampleWeeklyOnlyAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, usageFixture(t, "codex-weekly-only.jsonl"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, ok := lastCodexSample(path)
	if !ok || s.plan != "pro" || len(s.windows) != 1 || s.windows[0].Label != "7d" || s.windows[0].UsedPercent != 16 {
		t.Fatalf("got %+v %v", s, ok)
	}
}

func TestParseCodexRateLimitsIgnoresWhatIsNotAWindow(t *testing.T) {
	for name, line := range map[string]string{
		"no windows":    string(usageFixture(t, "codex-no-windows.jsonl")),
		"not token":     `{"timestamp":"2026-11-04T09:00:00.000Z","type":"event_msg","payload":{"type":"agent_message","rate_limits":{"limit_id":"codex","primary":{"used_percent":1,"window_minutes":300}}}}`,
		"no length":     `{"timestamp":"2026-11-04T09:00:00.000Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","primary":{"used_percent":1}}}}`,
		"bad timestamp": `{"timestamp":"yesterday","type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","primary":{"used_percent":1,"window_minutes":300}}}}`,
		"not json":      `{"rate_limits": nope`,
	} {
		if s, ok := parseCodexRateLimits([]byte(line)); ok {
			t.Errorf("%s: parsed %+v", name, s)
		}
	}
	// a limit without an id is Codex's own (older Codex versions)
	line := `{"timestamp":"2026-11-04T09:00:00.000Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":-3,"window_minutes":300,"resets_at":0}}}}`
	s, ok := parseCodexRateLimits([]byte(line))
	if !ok || s.windows[0].UsedPercent != 0 || s.windows[0].ResetsAt != nil {
		t.Errorf("no id: %+v %v", s, ok)
	}
}

func TestParseCodexRateLimitsCapsOverusedAtAHundred(t *testing.T) {
	line := `{"timestamp":"2026-11-04T09:00:00.000Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","primary":{"used_percent":130,"window_minutes":300}}}}`
	s, ok := parseCodexRateLimits([]byte(line))
	if !ok || s.windows[0].UsedPercent != 100 {
		t.Errorf("got %+v %v", s, ok)
	}
}

func TestLastCodexSampleReadsOnlyTheTailAndSkipsTheCutLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	old := `{"timestamp":"2026-11-03T08:00:00.000Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","primary":{"used_percent":1,"window_minutes":300}}}}` + "\n"
	pad := `{"timestamp":"2026-11-04T08:00:00.000Z","type":"response_item","payload":{"text":"` + strings.Repeat("x", usageWindowsTail) + `"}}` + "\n"
	if err := os.WriteFile(path, []byte(old+pad), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, ok := lastCodexSample(path); ok {
		t.Errorf("found %+v beyond the tail", s)
	}
	// a sample after the long line is found though the line before the tail's start is cut
	newer := strings.Replace(old, "2026-11-03", "2026-11-04", 1)
	if err := os.WriteFile(path, []byte(old+pad+newer), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, ok := lastCodexSample(path); !ok || s.at.Day() != 4 {
		t.Errorf("got %+v %v", s, ok)
	}
}

func TestParseClaudeStatusLimits(t *testing.T) {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	mod := now.Add(-time.Minute)
	a, ok := parseClaudeStatusLimits(usageFixture(t, "claude-statusline.json"), mod, now.Add(-100*24*time.Hour))
	if !ok || a.Provider != "claude" || a.Name != "Claude" || !a.SampledAt.Equal(mod) || len(a.Windows) != 2 {
		t.Fatalf("got %+v %v", a, ok)
	}
	if w := a.Windows[0]; w.Label != "5h" || w.UsedPercent != 23.5 || w.ResetsAt == nil || w.ResetsAt.Unix() != 1793780000 {
		t.Errorf("5h = %+v", w)
	}
	if w := a.Windows[1]; w.Label != "7d" || w.UsedPercent != 41 {
		t.Errorf("7d = %+v", w)
	}

	// only rate_limits, a string time and milliseconds; the gateway's spend_limit is not a window
	a, ok = parseClaudeStatusLimits(usageFixture(t, "claude-rate-limits-only.json"), mod, now.Add(-100*24*time.Hour))
	if !ok || len(a.Windows) != 2 {
		t.Fatalf("got %+v %v", a, ok)
	}
	if got := a.Windows[0].ResetsAt; got == nil || !got.Equal(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("RFC 3339 resets_at = %v", got)
	}
	if got := a.Windows[1].ResetsAt; got == nil || got.Unix() != 1794300000 {
		t.Errorf("millisecond resets_at = %v", got)
	}

	for name, in := range map[string]string{
		"no field":   `{"model":{}}`,
		"no percent": `{"rate_limits":{"five_hour":{"resets_at":1793780000}}}`,
		"not json":   `statusline`,
		"empty":      `{"rate_limits":{}}`,
	} {
		if a, ok := parseClaudeStatusLimits([]byte(in), mod, now); ok {
			t.Errorf("%s: parsed %+v", name, a)
		}
	}
}

func TestParseClaudeStatusLimitsCapsOverusedAtAHundred(t *testing.T) {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	in := `{"rate_limits":{"five_hour":{"used_percentage":130},"seven_day":{"used_percentage":100}}}`
	a, ok := parseClaudeStatusLimits([]byte(in), now.Add(-time.Minute), now)
	if !ok || len(a.Windows) != 2 || a.Windows[0].UsedPercent != 100 || a.Windows[1].UsedPercent != 100 {
		t.Errorf("got %+v %v", a, ok)
	}
}

func TestWindowsThatAlreadyResetAreDropped(t *testing.T) {
	now := time.Unix(1794000000, 0)
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	got := liveWindows([]proto.UsageWindow{
		{Label: "7d", WindowMinutes: 10080, UsedPercent: 50, ResetsAt: &future},
		{Label: "5h", WindowMinutes: 300, UsedPercent: 90, ResetsAt: &past},
		{Label: "1h", WindowMinutes: 60, UsedPercent: 5},
	}, now)
	if len(got) != 2 || got[0].Label != "1h" || got[1].Label != "7d" {
		t.Errorf("got %+v", got)
	}
}

// rolloutAt writes a rollout of a Codex home for today, with the given lines.
func rolloutAt(t *testing.T, home string, mtime time.Time, id string, lines ...string) {
	t.Helper()
	dir := filepath.Join(home, "sessions", mtime.Format("2006"), mtime.Format("01"), mtime.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-"+mtime.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func tokenCount(at time.Time, used float64, mins int, resets time.Time) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","primary":{"used_percent":%v,"window_minutes":%d,"resets_at":%d},"secondary":null,"plan_type":"plus"}}}`,
		at.UTC().Format(time.RFC3339Nano), used, mins, resets.Unix())
}

func TestCollectUsageWindowsReadsCodexAndTheClaudeDrop(t *testing.T) {
	dir := t.TempDir()
	n := &Node{home: filepath.Join(dir, "home"), dir: filepath.Join(dir, "node")}
	now := time.Now()

	// Codex: the newest rollout has no rate_limits yet, the one before it has
	older := now.Add(-2 * time.Hour)
	rolloutAt(t, n.codexHome(), older, "22222222-2222-4222-8222-222222222222", tokenCount(older, 33, 300, now.Add(time.Hour)))
	rolloutAt(t, n.codexHome(), now.Add(-time.Minute), "33333333-3333-4333-8333-333333333333", `{"type":"session_meta","payload":{}}`)

	// Claude: a wrapper's drop, a file too old to count, and a window that has reset
	if err := os.MkdirAll(filepath.Join(n.dir, "usage"), 0o700); err != nil {
		t.Fatal(err)
	}
	drop := fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":12,"resets_at":%d},"seven_day":{"used_percentage":40,"resets_at":%d}}}`,
		now.Add(-time.Hour).Unix(), now.Add(48*time.Hour).Unix())
	write := func(name, body string, age time.Duration) {
		p := filepath.Join(n.dir, "usage", name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	write("claude.json", drop, time.Minute)
	write("claude-old.json", drop, usageWindowsMaxAge+time.Hour)
	write("other.json", drop, time.Minute) // not claude*.json

	got := n.collectUsageWindows(now, n.usageWindowCodexHomes())
	if len(got) != 2 {
		t.Fatalf("sources: %+v", got)
	}
	c := got["codex:"+codexUsageKey(realPath(n.codexHome()))]
	if c.ReadingSource != "rollout" || c.Name != "Codex Plus" || len(c.Windows) != 1 || c.Windows[0].UsedPercent != 33 || now.Sub(c.SampledAt) < time.Hour {
		t.Errorf("codex = %+v", c)
	}
	cl := got["claude:claude.json"]
	if len(cl.Windows) != 1 || cl.Windows[0].Label != "7d" {
		t.Errorf("claude = %+v (the reset 5h window must be gone)", cl)
	}
}

func TestUsageSnapshotKeepsTheLastSampleAndHonoursTheOffSwitch(t *testing.T) {
	dir := t.TempDir()
	n := &Node{home: filepath.Join(dir, "home"), dir: filepath.Join(dir, "node")}
	now := time.Now()
	rolloutAt(t, n.codexHome(), now.Add(-time.Hour), "44444444-4444-4444-8444-444444444444", tokenCount(now.Add(-time.Hour), 7, 10080, now.Add(24*time.Hour)))

	snap := n.usageSnapshot()
	if !snap.Enabled || len(snap.Accounts) != 1 || snap.Accounts[0].Windows[0].UsedPercent != 7 {
		t.Fatalf("first poll: %+v", snap)
	}

	// the rollout goes away (cleaned up): the last sample stays
	if err := os.RemoveAll(filepath.Join(n.codexHome(), "sessions")); err != nil {
		t.Fatal(err)
	}
	rolloutCaches.Lock()
	delete(rolloutCaches.byHome, n.codexHome())
	rolloutCaches.Unlock()
	if snap = n.usageSnapshot(); len(snap.Accounts) != 1 {
		t.Fatalf("the kept sample is gone: %+v", snap)
	}

	// off: nothing is answered, and what was kept is forgotten
	n.setUsageWindows(false)
	if snap = n.usageSnapshot(); snap.Enabled || len(snap.Accounts) != 0 {
		t.Fatalf("off: %+v", snap)
	}
	n.setUsageWindows(true)
	if snap = n.usageSnapshot(); !snap.Enabled || len(snap.Accounts) != 0 {
		t.Fatalf("back on, nothing to read: %+v", snap)
	}
}

func TestMergeUsageForgetsSamplesOlderThanThreeDays(t *testing.T) {
	now := time.Now()
	kept := map[string]proto.UsageNodeAccount{
		"fresh": {UsageAccount: proto.UsageAccount{Name: "a", SampledAt: now.Add(-time.Hour)}},
		"old":   {UsageAccount: proto.UsageAccount{Name: "b", SampledAt: now.Add(-usageWindowsMaxAge - time.Minute)}},
	}
	found := map[string]proto.UsageNodeAccount{"fresh": {UsageAccount: proto.UsageAccount{Name: "a2", SampledAt: now}}}
	got := mergeUsage(kept, found, now)
	if len(got) != 1 || got["fresh"].Name != "a2" {
		t.Errorf("got %+v", got)
	}
}

func TestUsageSnapshotWithNothingToReadAnswersAnEmptyList(t *testing.T) {
	n := &Node{home: t.TempDir(), dir: t.TempDir()}
	if snap := n.usageSnapshot(); !snap.Enabled || snap.Accounts == nil || len(snap.Accounts) != 0 {
		t.Errorf("an empty answer must be [] not null: %+v", snap)
	}
}

func TestCodexUsageRefreshPrefersAccountBoundWindowsAndFallsBackBySource(t *testing.T) {
	useFakeCodexUsageApp(t)
	n, _ := testNode(t)
	n.dir = t.TempDir()
	n.codices = newCodexRegistry()
	legacyHome := t.TempDir()
	now := time.Now()
	resets := now.Add(time.Hour)
	rolloutAt(t, n.codexHome(), now.Add(-time.Minute), "44444444-4444-4444-8444-444444444444",
		tokenCount(now.Add(-time.Minute), 7, 10080, resets))
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(n.codexHome(), alias); err != nil {
		t.Fatal(err)
	}
	n.codices.config.Homes = []string{legacyHome, alias}
	source := codexUsageKey(realPath(n.codexHome()))
	const id = "fixture-account"
	writeCodexUsageReply(t, n.codexHome(), fmt.Sprintf(`{"accountId":%q,
		"rateLimits":{"limitId":"premium","primary":{"usedPercent":99,"windowDurationMins":300}},
		"rateLimitsByLimitId":{"codex":{"planType":"pro","primary":{"usedPercent":42,"windowDurationMins":10080,"resetsAt":%d}}}}`, id, resets.Unix()))
	writeCodexUsageReply(t, legacyHome, `{"rateLimits":{"planType":"plus","primary":{"usedPercent":0,"windowDurationMins":300}}}`)

	n.refreshUsageWindows(t.Context())
	result, err := n.handle(t.Context(), "usage.windows", nil)
	if err != nil {
		t.Fatal(err)
	}
	snap := result.(proto.UsageSnapshot)
	if len(snap.Accounts) != 2 {
		t.Fatalf("expected two homes, got %+v", snap)
	}
	for _, account := range snap.Accounts {
		switch account.SourceKey {
		case source:
			if account.AccountKey != codexUsageKey(id) || account.Name != "Codex Pro" ||
				len(account.Windows) != 1 || account.Windows[0].UsedPercent != 42 ||
				account.Windows[0].ResetsAt == nil || account.Windows[0].ResetsAt.Unix() != resets.Unix() {
				t.Errorf("app-server sample: %+v", account)
			}
		case codexUsageKey(realPath(legacyHome)):
			if account.ReadingSource != "" || account.AccountKey != "" || account.Name != "Codex Plus" ||
				len(account.Windows) != 1 || account.Windows[0].UsedPercent != 0 {
				t.Errorf("legacy app-server sample: %+v", account)
			}
		default:
			t.Errorf("unexpected source: %+v", account)
		}
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), id) || strings.Contains(string(data), n.codexHome()) || strings.Contains(string(data), legacyHome) {
		t.Error("node RPC exposed a raw account ID or home")
	}

	// A model-specific answer has no plan windows. A failed read keeps a newer verified sample.
	writeCodexUsageReply(t, n.codexHome(), `{"accountId":"different-account",
		"rateLimits":{"limitId":"premium","primary":{"usedPercent":99,"windowDurationMins":300}}}`)
	n.refreshUsageWindows(t.Context())
	current := func() proto.UsageNodeAccount {
		t.Helper()
		for _, account := range n.usageSnapshot().Accounts {
			if account.SourceKey == source {
				return account
			}
		}
		t.Fatal("missing Codex home")
		return proto.UsageNodeAccount{}
	}
	if account := current(); account.AccountKey != codexUsageKey(id) || account.Windows[0].UsedPercent != 42 {
		t.Fatalf("old rollout replaced a newer verified sample: %+v", account)
	}

	// A newer rollout can be used as a fallback. It never inherits an identity from the app-server.
	newer := time.Now().Add(time.Second)
	rolloutAt(t, n.codexHome(), newer, "55555555-5555-4555-8555-555555555555", tokenCount(newer, 11, 10080, resets))
	rolloutCaches.Lock()
	delete(rolloutCaches.byHome, n.codexHome())
	rolloutCaches.Unlock()
	n.refreshUsageWindows(t.Context())
	if account := current(); account.ReadingSource != "rollout" || account.AccountKey != "" || account.Windows[0].UsedPercent != 11 {
		t.Fatalf("new rollout did not become an unidentified source: %+v", account)
	}

	expired := now.Add(-time.Minute)
	writeCodexUsageReply(t, n.codexHome(), fmt.Sprintf(`{"accountId":%q,
		"rateLimits":{"limitId":"codex","planType":"pro","primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":%d}}}`, id, expired.Unix()))
	n.refreshUsageWindows(t.Context())
	if account := current(); account.AccountKey != codexUsageKey(id) ||
		len(account.Windows) != 1 || account.Windows[0].ResetsAt == nil || account.Windows[0].ResetsAt.Unix() != expired.Unix() {
		t.Fatalf("node did not forward expired authoritative window: %+v", account)
	}

	writeCodexUsageReply(t, n.codexHome(), fmt.Sprintf(`{"accountId":%q,
		"rateLimits":{"limitId":"codex","planType":"team","primary":{"usedPercent":10,"windowDurationMins":300}}}`, id))
	n.refreshUsageWindows(t.Context())
	if account := current(); account.ReadingSource != "" || account.AccountKey != "" || account.Plan != "team" || account.Windows[0].UsedPercent != 10 {
		t.Fatalf("workspace ID was used as a personal quota ID: %+v", account)
	}
}

func TestCodexUsageRefreshWithNPMCodex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("npm uses a POSIX shebang launcher on Unix")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	node := "#!/bin/sh\n[ \"$2\" = app-server ] || exit 1\nexec " + shellQuote(binary) + " -test.run='^TestFakeCodexUsageApp$'\n"

	previous := codexUsageCLI
	codexUsageCLI = func() (string, error) { return agentExecutable("codex") }
	t.Cleanup(func() { codexUsageCLI = previous })

	for _, tc := range []struct {
		name        string
		serviceNode bool
	}{
		{name: "paired Node.js"},
		{name: "Node.js already on PATH", serviceNode: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			prefix := filepath.Join(home, ".nvm", "versions", "node", "v22.0.0")
			cli := writeExecutable(t, filepath.Join(prefix, "lib", "node_modules", "@openai", "codex", "bin", "codex.js"), "#!/usr/bin/env node\n")
			bin := filepath.Join(prefix, "bin")
			mkdir(t, bin)
			npmLauncher := filepath.Join(bin, "codex")
			if err := os.Symlink(cli, npmLauncher); err != nil {
				t.Fatal(err)
			}
			localBin := filepath.Join(home, ".local", "bin")
			mkdir(t, localBin)
			if err := os.Symlink(npmLauncher, filepath.Join(localBin, "codex")); err != nil {
				t.Fatal(err)
			}
			serviceBin := filepath.Join(home, "service-bin")
			mkdir(t, serviceBin)
			if tc.serviceNode {
				writeExecutable(t, filepath.Join(serviceBin, "node"), node)
				writeExecutable(t, filepath.Join(bin, "node"), "#!/bin/sh\nexit 1\n")
			} else {
				writeExecutable(t, filepath.Join(bin, "node"), node)
			}

			t.Setenv("HOME", home)
			t.Setenv("PATH", serviceBin)
			t.Setenv("PERSONA_NODEJS", "")
			t.Setenv("PERSONA_TEST_CODEX_USAGE_APP", "1")
			n := &Node{home: home, dir: t.TempDir()}
			mkdir(t, n.codexHome())
			const accountID = "fixture-npm-account"
			writeCodexUsageReply(t, n.codexHome(), fmt.Sprintf(`{"accountId":%q,
				"rateLimits":{"planType":"pro","primary":{"usedPercent":42,"windowDurationMins":10080}}}`, accountID))

			n.refreshUsageWindows(t.Context())
			snap := n.usageSnapshot()
			if len(snap.Accounts) != 1 {
				t.Fatalf("expected one account from npm Codex, got %+v", snap)
			}
			account := snap.Accounts[0]
			if account.AccountKey != codexUsageKey(accountID) || len(account.Windows) != 1 || account.Windows[0].UsedPercent != 42 {
				t.Fatalf("expected account-bound usage, got %+v", account)
			}
			if got := os.Getenv("PATH"); got != serviceBin {
				t.Fatalf("poll changed the node's PATH to %q", got)
			}
		})
	}
}

func TestCodexUsageRefreshDoesNotBlockSnapshotsAndDisableCancelsIt(t *testing.T) {
	useFakeCodexUsageApp(t)
	n := &Node{home: t.TempDir(), dir: t.TempDir()}
	now := time.Now()
	rolloutAt(t, n.codexHome(), now, "44444444-4444-4444-8444-444444444444", tokenCount(now, 7, 300, now.Add(time.Hour)))
	writeCodexUsageReply(t, n.codexHome(), "block")

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		n.refreshUsageWindows(ctx)
	}()
	defer func() { cancel(); <-done }()
	waitFor(t, "app-server usage request", func() bool {
		_, err := os.Stat(filepath.Join(n.codexHome(), "fixture-usage-started"))
		return err == nil
	})
	// A cold usage RPC returns a local sample even though the app-server is still waiting.
	response := make(chan proto.UsageSnapshot, 1)
	go func() { response <- n.usageSnapshot() }()
	select {
	case snap := <-response:
		if len(snap.Accounts) != 1 || snap.Accounts[0].AccountKey != "" || snap.Accounts[0].Windows[0].UsedPercent != 7 {
			t.Fatalf("cold sample: %+v", snap)
		}
	case <-time.After(time.Second):
		t.Fatal("usage RPC blocked on app-server")
	}
	n.setUsageWindows(false)
	n.setUsageWindows(true)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disable did not cancel the usage request")
	}
	n.usageWin.mu.Lock()
	defer n.usageWin.mu.Unlock()
	if n.usageWin.accounts != nil {
		t.Error("a canceled refresh repopulated the cache")
	}
}

func TestCodexUsageRefreshForwardsLoginShellProxyEnv(t *testing.T) {
	useFakeCodexUsageApp(t)
	prevShell := detectShellProxy
	detectShellProxy = func(context.Context) shellProxy {
		return shellProxy{HTTPSProxy: "http://proxy.example.test:8080", NoProxy: "127.0.0.1,localhost"}
	}
	t.Cleanup(func() { detectShellProxy = prevShell })
	for _, k := range shellProxyEnvClear {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}

	n := &Node{home: t.TempDir(), dir: t.TempDir()}
	mkdir(t, n.codexHome())
	writeCodexUsageReply(t, n.codexHome(), `{"accountId":"proxy-acct","rateLimits":{"planType":"plus","primary":{"usedPercent":17,"windowDurationMins":10080}}}`)

	n.refreshUsageWindows(t.Context())
	snap := n.usageSnapshot()
	if len(snap.Accounts) != 1 || snap.Accounts[0].AccountKey != codexUsageKey("proxy-acct") {
		t.Fatalf("expected account-bound usage, got %+v", snap)
	}
	gotProxy, err := os.ReadFile(filepath.Join(n.codexHome(), "fixture-usage-https-proxy"))
	if err != nil || string(gotProxy) != "http://proxy.example.test:8080" {
		t.Fatalf("expected app-server to receive HTTPS_PROXY, got %q (err=%v)", string(gotProxy), err)
	}
}

func TestRuntimeUsageWindowsPreservesAccountKeyAndEnrichesLegacyAgy(t *testing.T) {
	now := time.Date(2026, 11, 27, 2, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour).Format(time.RFC3339)

	n := &Node{home: t.TempDir(), dir: t.TempDir()}
	conn := &runtimeConn{hello: runtimeHello{Runtime: "agy"}}
	payload := fmt.Sprintf(`{"provider":"agy","name":"alice@example.com","plan":"Google AI Ultra","account_key":"acct-digest-alice","sampled_at":%q,"windows":[{"label":"5h","window_minutes":300,"used_percent":12.5,"resets_at":%q,"model":"Gemini Models"}]}`, now.Format(time.RFC3339), reset)
	n.runtimeUsageWindows(conn, json.RawMessage(payload), now)
	snap := n.usageSnapshot()
	if len(snap.Accounts) != 1 || snap.Accounts[0].AccountKey != "acct-digest-alice" || snap.Accounts[0].Name != "alice@example.com" {
		t.Fatalf("expected runtime account_key in snapshot, got %+v", snap)
	}

	parsed, ok := parseAgyUserStatusJSON([]byte(`{"userStatus":{"email":"bob@example.com","name":"Bob","userTier":{"name":"Google AI Ultra"}}}`))
	if !ok || parsed.name != "bob@example.com" || parsed.plan != "Google AI Ultra" || len(parsed.accountKey) != 64 {
		t.Fatalf("unexpected parsed user status: %+v ok=%v", parsed, ok)
	}
	prev := agyLocalUserStatus
	agyLocalUserStatus = func() (agyUserIdentity, bool) { return parsed, true }
	agyIdentityCache.mu.Lock()
	agyIdentityCache.checked = false
	agyIdentityCache.mu.Unlock()
	t.Cleanup(func() {
		agyLocalUserStatus = prev
		agyIdentityCache.mu.Lock()
		agyIdentityCache.checked = false
		agyIdentityCache.mu.Unlock()
	})

	legacy := fmt.Sprintf(`{"provider":"agy","name":"Antigravity","sampled_at":%q,"windows":[{"label":"5h","window_minutes":300,"used_percent":20,"resets_at":%q,"model":"Gemini Models"}]}`, now.Add(time.Minute).Format(time.RFC3339), reset)
	n.runtimeUsageWindows(conn, json.RawMessage(legacy), now.Add(time.Minute))
	snap = n.usageSnapshot()
	if len(snap.Accounts) != 1 || snap.Accounts[0].Name != "bob@example.com" || snap.Accounts[0].Plan != "Google AI Ultra" || snap.Accounts[0].AccountKey != parsed.accountKey {
		t.Fatalf("expected legacy agy sample to be enriched with local identity, got %+v", snap)
	}
}

func useFakeCodexUsageApp(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake app-server launcher uses a POSIX shell")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nexec " + shellQuote(binary) + " -test.run='^TestFakeCodexUsageApp$'\n"
	if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := codexUsageCLI
	codexUsageCLI = func() (string, error) { return cli, nil }
	t.Cleanup(func() { codexUsageCLI = previous })
	t.Setenv("PERSONA_TEST_CODEX_USAGE_APP", "1")
}

func writeCodexUsageReply(t *testing.T, home, result string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "fixture-usage-result"), []byte(result), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestFakeCodexUsageApp supplies the official app-server JSON-RPC contract to the collector's real stdio client.
func TestFakeCodexUsageApp(t *testing.T) {
	if os.Getenv("PERSONA_TEST_CODEX_USAGE_APP") == "" {
		return
	}
	home := os.Getenv("CODEX_HOME")
	scanner := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	var initialized bool
	for scanner.Scan() {
		var message struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			t.Fatal(err)
		}
		switch message.Method {
		case "initialize":
			_ = out.Encode(map[string]any{"id": message.ID, "result": map[string]any{}})
		case "initialized":
			initialized = true
		case "account/rateLimits/read":
			if p := os.Getenv("HTTPS_PROXY"); p != "" {
				_ = os.WriteFile(filepath.Join(home, "fixture-usage-https-proxy"), []byte(p), 0o600)
			}
			var params map[string]bool
			if err := json.Unmarshal(message.Params, &params); err != nil || !initialized || !params["excludeResetCreditDetails"] {
				t.Fatal("unexpected usage request")
			}
			body, err := os.ReadFile(filepath.Join(home, "fixture-usage-result"))
			if err != nil {
				t.Fatal(err)
			}
			if string(body) == "block" {
				if err := os.WriteFile(filepath.Join(home, "fixture-usage-started"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				continue
			}
			_ = out.Encode(map[string]any{"id": message.ID, "result": json.RawMessage(body)})
		default:
			t.Fatalf("unexpected app-server method %q", message.Method)
		}
	}
}
