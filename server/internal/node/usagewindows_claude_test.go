package node

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestParseClaudeGetUsage(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	acct, avail, ok := parseClaudeGetUsage(usageFixture(t, "claude-get-usage.json"), "", now)
	if !ok || !avail {
		t.Fatalf("got ok=%v avail=%v", ok, avail)
	}
	if acct.Provider != "claude" || acct.Name != "Claude Pro" || acct.Plan != "pro" {
		t.Errorf("account header = %+v", acct)
	}
	if len(acct.Windows) != 3 {
		t.Fatalf("expected 5h, 7d, 7d Sonnet; got %+v", acct.Windows)
	}
	if w := acct.Windows[0]; w.Label != "5h" || w.WindowMinutes != 300 || w.UsedPercent != 42 || w.Model != "" || w.ResetsAt == nil {
		t.Errorf("5h = %+v", w)
	}
	if w := acct.Windows[1]; w.Label != "7d" || w.WindowMinutes != 10080 || w.UsedPercent != 67.5 || w.Model != "" {
		t.Errorf("7d = %+v", w)
	}
	if w := acct.Windows[2]; w.Label != "7d" || w.Model != "Sonnet" || w.UsedPercent != 15 {
		t.Errorf("7d Sonnet = %+v", w)
	}

	// Enterprise seat: five_hour/seven_day are null, windows come from limits[].
	ent, avail, ok := parseClaudeGetUsage(usageFixture(t, "claude-get-usage-enterprise.json"), "", now)
	if !ok || !avail || ent.Name != "Claude Enterprise" || len(ent.Windows) != 2 {
		t.Fatalf("enterprise = %+v avail=%v ok=%v", ent, avail, ok)
	}
	if ent.Windows[0].Label != "5h" || ent.Windows[0].UsedPercent != 28 {
		t.Errorf("enterprise 5h = %+v", ent.Windows[0])
	}
	if ent.Windows[1].Label != "7d" || ent.Windows[1].Model != "Opus" || ent.Windows[1].UsedPercent != 11 {
		t.Errorf("enterprise 7d Opus = %+v", ent.Windows[1])
	}

	// Unavailable (API key / signed out): authoritative false, ok true so caller does not fall back.
	_, avail, ok = parseClaudeGetUsage([]byte(`{"rate_limits_available":false,"rate_limits":null}`), "Claude Pro", now)
	if !ok || avail {
		t.Errorf("unavailable: got ok=%v avail=%v, want ok=true avail=false", ok, avail)
	}

	// Fallback subscriptionType from initialize when get_usage omits it, plus percent clamping.
	acct, avail, ok = parseClaudeGetUsage([]byte(`{"rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":125,"resets_at":"2026-11-21T15:00:00Z"}}}`), "Claude Max", now)
	if !ok || !avail || acct.Name != "Claude Max" || acct.Plan != "max" || len(acct.Windows) != 1 || acct.Windows[0].UsedPercent != 100 {
		t.Errorf("fallback plan/clamp = %+v avail=%v ok=%v", acct, avail, ok)
	}

	// Available with null rate_limits (observed on fresh Linux processes): preserves subscription plan with no windows
	// so the caller can fall back to `-p /usage`.
	acct, avail, ok = parseClaudeGetUsage([]byte(`{"subscription_type":"max","rate_limits_available":true,"rate_limits":null}`), "", now)
	if !ok || !avail || acct.Name != "Claude Max" || acct.Plan != "max" || len(acct.Windows) != 0 {
		t.Errorf("available empty limits: got acct=%+v avail=%v ok=%v", acct, avail, ok)
	}

	// Malformed payload is rejected with ok=false.
	if _, _, ok := parseClaudeGetUsage([]byte(`{"unexpected":true}`), "", now); ok {
		t.Error("malformed payload unexpectedly parsed")
	}
}

func TestParseClaudePrintUsage(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

	// Verbose JSON prefers structured usage_report.rate_limits (session, weekly_all, weekly_scoped).
	acct, ok, modelTurn := parseClaudePrintUsage(usageFixture(t, "claude-print-usage-verbose.json"), now)
	if !ok || modelTurn || acct.Provider != "claude" || len(acct.Windows) != 3 {
		t.Fatalf("verbose json = %+v ok=%v modelTurn=%v", acct, ok, modelTurn)
	}
	if acct.Windows[0].Label != "5h" || acct.Windows[0].UsedPercent != 55 || acct.Windows[0].ResetsAt == nil || acct.Windows[0].ResetsAt.Hour() != 16 {
		t.Errorf("5h = %+v", acct.Windows[0])
	}
	if acct.Windows[1].Label != "7d" || acct.Windows[1].Model != "" || acct.Windows[1].UsedPercent != 72 {
		t.Errorf("7d = %+v", acct.Windows[1])
	}
	if acct.Windows[2].Label != "7d" || acct.Windows[2].Model != "Sonnet" || acct.Windows[2].UsedPercent != 18 {
		t.Errorf("7d Sonnet = %+v", acct.Windows[2])
	}

	// Login-shell rc banners, wrapper log lines, and trailing `exit` are stripped before JSON decoding.
	wrapped := "rc-banner-no-newline\n" + claudeUsageBeginMarker + "\n[wrapper] auth ready\n" +
		string(usageFixture(t, "claude-print-usage-verbose.json")) + "\n[wrapper] done\n" +
		claudeUsageEndMarker + "\nexit\n"
	acct, ok, modelTurn = parseClaudePrintUsage([]byte(wrapped), now)
	if !ok || modelTurn || len(acct.Windows) != 3 || acct.Windows[2].Model != "Sonnet" {
		t.Fatalf("wrapped verbose json = %+v ok=%v modelTurn=%v", acct, ok, modelTurn)
	}

	// Plain text fallback (including "Last 7d · 403 requests" which must not trigger 403 denial).
	acct, ok, modelTurn = parseClaudePrintUsage(usageFixture(t, "claude-print-usage.txt"), now)
	if !ok || modelTurn || len(acct.Windows) != 3 {
		t.Fatalf("plain text = %+v ok=%v modelTurn=%v", acct, ok, modelTurn)
	}
	if w := acct.Windows[0]; w.Label != "5h" || w.UsedPercent != 78 || w.ResetsAt == nil || !w.ResetsAt.Equal(time.Date(2026, 11, 21, 18, 30, 0, 0, time.UTC)) {
		t.Errorf("5h text = %+v", w)
	}
	if w := acct.Windows[1]; w.Label != "7d" || w.Model != "" || w.UsedPercent != 87 || w.ResetsAt == nil || !w.ResetsAt.Equal(time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("7d text = %+v", w)
	}
	if w := acct.Windows[2]; w.Label != "7d" || w.Model != "Sonnet" || w.UsedPercent != 14 {
		t.Errorf("7d Sonnet text = %+v", w)
	}

	// Single JSON result object with is_error=true or auth error text is rejected.
	if _, ok, _ := parseClaudePrintUsage([]byte(`{"type":"result","is_error":true,"result":"Current session: 10% used"}`), now); ok {
		t.Error("is_error result parsed")
	}
	if _, ok := parseClaudeUsageText("Error: HTTP 401 Unauthorized\nCurrent session: 10% used", now); ok {
		t.Error("auth error text parsed")
	}

	// Output containing an assistant model turn or nonzero API cost/duration is rejected and flagged as modelTurn,
	// even when preceded by wrapper banner lines.
	if _, ok, mt := parseClaudePrintUsage([]byte("[wrapper] ok\n"+`[{"type":"assistant","message":{"role":"assistant","content":"hi"}},{"type":"result","subtype":"success","result":"Current session: 10% used"}]`), now); ok || !mt {
		t.Errorf("assistant model turn: got ok=%v modelTurn=%v, want false, true", ok, mt)
	}
	if _, ok, mt := parseClaudePrintUsage([]byte(`{"type":"result","subtype":"success","total_cost_usd":0.01,"duration_api_ms":800,"result":"Current session: 10% used"}`), now); ok || !mt {
		t.Errorf("billed result: got ok=%v modelTurn=%v, want false, true", ok, mt)
	}
}

func TestParseClaudeResetTime(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

	got, ok := parseClaudeResetTime("Nov 21 at 6:30pm (UTC)", now, 5*time.Hour)
	want := time.Date(2026, 11, 21, 18, 30, 0, 0, time.UTC)
	if !ok || !got.Equal(want) {
		t.Errorf("UTC date: got %v (%v), want %v", got, ok, want)
	}

	// Named IANA timezone conversion (America/New_York is UTC-5 in November).
	got, ok = parseClaudeResetTime("Nov 21 at 1:30pm (America/New_York)", now, 5*time.Hour)
	if !ok || !got.UTC().Equal(want) {
		t.Errorf("New_York date: got %v (%v), want %v", got.UTC(), ok, want)
	}

	// Comma separator and time-only format.
	got, ok = parseClaudeResetTime("Nov 23, 9am (UTC)", now, 7*24*time.Hour)
	wantWeek := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	if !ok || !got.Equal(wantWeek) {
		t.Errorf("comma date: got %v (%v), want %v", got, ok, wantWeek)
	}

	got, ok = parseClaudeResetTime("3:30pm (UTC)", now, 5*time.Hour)
	wantToday := time.Date(2026, 11, 21, 15, 30, 0, 0, time.UTC)
	if !ok || !got.Equal(wantToday) {
		t.Errorf("time-only: got %v (%v), want %v", got, ok, wantToday)
	}

	// Year rollover: Dec 30 reading a Jan 2 weekly reset lands in the next year.
	dec := time.Date(2026, 12, 30, 12, 0, 0, 0, time.UTC)
	got, ok = parseClaudeResetTime("Jan 2 at 9am (UTC)", dec, 7*24*time.Hour)
	wantJan := time.Date(2027, 1, 2, 9, 0, 0, 0, time.UTC)
	if !ok || !got.Equal(wantJan) {
		t.Errorf("year rollover: got %v (%v), want %v", got, ok, wantJan)
	}
}

func TestDedupeClaudeUsagePrefersFreshStatusLine(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	r1 := now.Add(3 * time.Hour)
	r1Jitter := r1.Add(time.Second) // 1s apart from status line
	rOther := now.Add(4 * time.Hour)

	// Fresh status line beats CLI sample for the same account; distinct account stays.
	accounts := map[string]proto.UsageNodeAccount{
		"claude:claude.json": {
			UsageAccount: proto.UsageAccount{
				Provider: "claude", Name: "Claude", SampledAt: now.Add(-2 * time.Minute),
				Windows: []proto.UsageWindow{{Label: "5h", WindowMinutes: 300, UsedPercent: 31, ResetsAt: &r1}},
			},
		},
		claudeUsagePrefix + "same": {
			UsageAccount: proto.UsageAccount{
				Provider: "claude", Name: "Claude Pro", SampledAt: now,
				Windows: []proto.UsageWindow{{Label: "5h", WindowMinutes: 300, UsedPercent: 32, ResetsAt: &r1Jitter}},
			},
		},
		claudeUsagePrefix + "other": {
			UsageAccount: proto.UsageAccount{
				Provider: "claude", Name: "Claude Max", SampledAt: now,
				Windows: []proto.UsageWindow{{Label: "5h", WindowMinutes: 300, UsedPercent: 10, ResetsAt: &rOther}},
			},
		},
	}
	dedupeClaudeUsage(accounts, now)
	if _, ok := accounts["claude:claude.json"]; !ok {
		t.Error("fresh status-line sample was dropped")
	}
	if _, ok := accounts[claudeUsagePrefix+"same"]; ok {
		t.Error("duplicate CLI sample was not suppressed by fresh status-line sample")
	}
	if _, ok := accounts[claudeUsagePrefix+"other"]; !ok {
		t.Error("distinct Claude account was dropped")
	}

	// Stale status line yields to a fresher CLI sample.
	staleAccounts := map[string]proto.UsageNodeAccount{
		"claude:claude.json": {
			UsageAccount: proto.UsageAccount{
				Provider: "claude", Name: "Claude", SampledAt: now.Add(-2 * time.Hour),
				Windows: []proto.UsageWindow{{Label: "5h", WindowMinutes: 300, UsedPercent: 31, ResetsAt: &r1}},
			},
		},
		claudeUsagePrefix + "same": {
			UsageAccount: proto.UsageAccount{
				Provider: "claude", Name: "Claude Pro", SampledAt: now,
				Windows: []proto.UsageWindow{{Label: "5h", WindowMinutes: 300, UsedPercent: 45, ResetsAt: &r1Jitter}},
			},
		},
	}
	dedupeClaudeUsage(staleAccounts, now)
	if _, ok := staleAccounts["claude:claude.json"]; ok {
		t.Error("stale status-line sample was not replaced by newer CLI sample")
	}
	if _, ok := staleAccounts[claudeUsagePrefix+"same"]; !ok {
		t.Error("newer CLI sample was dropped")
	}
}

func TestDedupeClaudeUsageReconcilesStreamAndCLI(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	r5 := now.Add(3 * time.Hour)
	r5Jitter := r5.Add(time.Second) // 1s apart between rate_limit_event and CLI probe
	r7 := now.Add(5 * 24 * time.Hour)
	rExpired := now.Add(-time.Minute)
	rOther := now.Add(4 * time.Hour)

	// Newer stream sample wins on overlapping plan windows while preserving CLI plan metadata and live per-model
	// windows, without duplicating the account or leaking expired extra windows.
	streamKey := claudeStreamUsageKey("claude", "")
	accounts := map[string]proto.UsageNodeAccount{
		streamKey: {
			UsageAccount: proto.UsageAccount{
				Provider: "claude", Name: "Claude", SampledAt: now,
				Windows: []proto.UsageWindow{
					{Label: "5h", WindowMinutes: 300, UsedPercent: 40, ResetsAt: &r5},
					{Label: "7d", WindowMinutes: 10080, UsedPercent: 25, ResetsAt: &r7},
				},
			},
		},
		claudeUsagePrefix + "same": {
			UsageAccount: proto.UsageAccount{
				Provider: "claude", Name: "Claude Max", Plan: "max", SampledAt: now.Add(-5 * time.Minute),
				Windows: []proto.UsageWindow{
					{Label: "5h", WindowMinutes: 300, UsedPercent: 32, ResetsAt: &r5Jitter},
					{Label: "7d", WindowMinutes: 10080, UsedPercent: 20, ResetsAt: &r7},
					{Label: "7d", WindowMinutes: 10080, UsedPercent: 12, Model: "Sonnet", ResetsAt: &r7},
					{Label: "7d", WindowMinutes: 10080, UsedPercent: 9, Model: "Opus", ResetsAt: &rExpired},
				},
			},
		},
		claudeUsagePrefix + "other": {
			UsageAccount: proto.UsageAccount{
				Provider: "claude", Name: "Claude Pro", Plan: "pro", SampledAt: now.Add(-2 * time.Minute),
				Windows: []proto.UsageWindow{{Label: "5h", WindowMinutes: 300, UsedPercent: 10, ResetsAt: &rOther}},
			},
		},
	}
	dedupeClaudeUsage(accounts, now)
	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts after reconciling stream+CLI, got %d: %+v", len(accounts), accounts)
	}
	if _, ok := accounts[claudeUsagePrefix+"same"]; ok {
		t.Error("duplicate CLI entry was not folded into newer stream entry")
	}
	merged, ok := accounts[streamKey]
	if !ok {
		t.Fatal("newer stream entry was dropped")
	}
	if merged.Name != "Claude Max" || merged.Plan != "max" || !merged.SampledAt.Equal(now) {
		t.Errorf("merged header = %+v, want Claude Max / max at %v", merged.UsageAccount, now)
	}
	if len(merged.Windows) != 3 {
		t.Fatalf("expected 5h, 7d, 7d Sonnet (expired Opus dropped); got %+v", merged.Windows)
	}
	if merged.Windows[0].UsedPercent != 40 || merged.Windows[1].UsedPercent != 25 || merged.Windows[2].Model != "Sonnet" || merged.Windows[2].UsedPercent != 12 {
		t.Errorf("merged windows = %+v", merged.Windows)
	}
	if _, ok := accounts[claudeUsagePrefix+"other"]; !ok {
		t.Error("distinct Claude CLI account was dropped")
	}

	// Newer CLI sample replaces older stream sample for the same account while keeping one entry.
	cliNewer := map[string]proto.UsageNodeAccount{
		streamKey: {
			UsageAccount: proto.UsageAccount{
				Provider: "claude", Name: "Claude", SampledAt: now.Add(-20 * time.Minute),
				Windows: []proto.UsageWindow{
					{Label: "5h", WindowMinutes: 300, UsedPercent: 15, ResetsAt: &r5},
				},
			},
		},
		claudeUsagePrefix + "same": {
			UsageAccount: proto.UsageAccount{
				Provider: "claude", Name: "Claude Max", Plan: "max", SampledAt: now,
				Windows: []proto.UsageWindow{
					{Label: "5h", WindowMinutes: 300, UsedPercent: 48, ResetsAt: &r5Jitter},
					{Label: "7d", WindowMinutes: 10080, UsedPercent: 30, ResetsAt: &r7},
				},
			},
		},
	}
	dedupeClaudeUsage(cliNewer, now)
	if len(cliNewer) != 1 {
		t.Fatalf("expected 1 account when CLI is newer than stream, got %d: %+v", len(cliNewer), cliNewer)
	}
	got, ok := cliNewer[claudeUsagePrefix+"same"]
	if !ok || got.Windows[0].UsedPercent != 48 || got.Plan != "max" {
		t.Errorf("newer CLI entry = %+v (ok=%v)", got, ok)
	}
}

func TestClaudeCLIUsageRefreshProtocolFallbackThrottleAndCancel(t *testing.T) {
	useFakeClaudeUsageCLI(t)
	// Keep Codex CLI out of this test so only Claude is probed.
	prevCodex := codexUsageCLI
	codexUsageCLI = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { codexUsageCLI = prevCodex })

	home := t.TempDir()
	defaultDir := filepath.Join(home, ".claude")
	customDir := filepath.Join(home, ".claude-work")
	for _, d := range []string{defaultDir, customDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PERSONA_TEST_CLAUDE_DEFAULT_DIR", defaultDir)

	n := &Node{home: home, dir: t.TempDir(), cdirs: newClaudeDirs()}
	n.cdirs.scan = func() map[int]procEnv { return nil }
	n.cdirs.known = []string{customDir}

	now := time.Now().UTC().Truncate(time.Second)
	prevNow := claudeUsageNow
	claudeUsageNow = func() time.Time { return now }
	t.Cleanup(func() { claudeUsageNow = prevNow })

	// Default dir answers via structured get_usage; custom dir rejects get_usage and falls back to -p /usage.
	writeClaudeUsageFixture(t, defaultDir, "control", fmt.Sprintf(`{
		"subscription_type":"pro",
		"rate_limits_available":true,
		"rate_limits":{
			"five_hour":{"utilization":44,"resets_at":%q},
			"seven_day":{"utilization":71,"resets_at":%q}
		}
	}`, now.Add(2*time.Hour).Format(time.RFC3339), now.Add(48*time.Hour).Format(time.RFC3339)))

	writeClaudeUsageFixture(t, customDir, "control", "unsupported")
	writeClaudeUsageFixture(t, customDir, "print", fmt.Sprintf(
		"You are currently using your subscription to power your Claude Code usage\n\nCurrent session: 19%% used · resets %s\n",
		now.Add(3*time.Hour).Format(time.RFC3339),
	))

	n.refreshUsageWindows(t.Context())
	snap := n.usageSnapshot()
	if len(snap.Accounts) != 2 {
		t.Fatalf("expected 2 Claude accounts, got %+v", snap.Accounts)
	}
	// Verify default dir ran with CLAUDE_CONFIG_DIR unset (macOS Keychain compatibility), custom dir with it set.
	if envSeen, err := os.ReadFile(filepath.Join(defaultDir, "fixture-config-dir-env")); err != nil || string(envSeen) != "<unset>" {
		t.Errorf("default dir CLAUDE_CONFIG_DIR = %q (err %v), want <unset>", string(envSeen), err)
	}
	if envSeen, err := os.ReadFile(filepath.Join(customDir, "fixture-config-dir-env")); err != nil || string(envSeen) != customDir {
		t.Errorf("custom dir CLAUDE_CONFIG_DIR = %q (err %v), want %q", string(envSeen), err, customDir)
	}

	var gotDefault, gotCustom bool
	for _, a := range snap.Accounts {
		if a.Name == "Claude Pro" && len(a.Windows) == 2 && a.Windows[0].UsedPercent == 44 {
			gotDefault = true
		}
		if a.Name == "Claude Pro" && len(a.Windows) == 1 && a.Windows[0].UsedPercent == 19 {
			gotCustom = true
		}
	}
	if !gotDefault || !gotCustom {
		t.Fatalf("unexpected accounts: %+v", snap.Accounts)
	}

	// Throttling: immediate second refresh (within min interval) and idle refresh (after min interval with no activity)
	// must not re-invoke the CLI.
	defaultCountBefore := readProbeCount(t, defaultDir)
	now = now.Add(2 * time.Minute)
	n.refreshUsageWindows(t.Context())
	if got := readProbeCount(t, defaultDir); got != defaultCountBefore {
		t.Fatalf("re-probed within min interval: %d -> %d", defaultCountBefore, got)
	}

	now = now.Add(10 * time.Minute)
	n.refreshUsageWindows(t.Context())
	if got := readProbeCount(t, defaultDir); got != defaultCountBefore {
		t.Fatalf("re-probed idle directory with no activity or reset: %d -> %d", defaultCountBefore, got)
	}

	// Activity (history.jsonl touched after last probe) triggers a re-probe after min interval.
	hist := filepath.Join(defaultDir, "history.jsonl")
	if err := os.WriteFile(hist, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(hist, now.Add(time.Second), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	n.refreshUsageWindows(t.Context())
	if got := readProbeCount(t, defaultDir); got != defaultCountBefore+1 {
		t.Fatalf("expected re-probe after activity: %d -> %d", defaultCountBefore, got)
	}

	// Blocking probe must not block usageSnapshot, and setUsageWindows(false) cancels it.
	writeClaudeUsageFixture(t, defaultDir, "control", "block")
	if err := os.Chtimes(hist, now.Add(10*time.Minute), now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Minute)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		n.refreshUsageWindows(ctx)
	}()
	defer func() { cancel(); <-done }()
	waitFor(t, "claude usage probe start", func() bool {
		_, err := os.Stat(filepath.Join(defaultDir, "fixture-started"))
		return err == nil
	})
	respCh := make(chan proto.UsageSnapshot, 1)
	go func() { respCh <- n.usageSnapshot() }()
	select {
	case snap := <-respCh:
		if len(snap.Accounts) == 0 {
			t.Fatalf("expected cached accounts during probe, got %+v", snap)
		}
	case <-time.After(time.Second):
		t.Fatal("usageSnapshot blocked on in-flight Claude probe")
	}
	n.setUsageWindows(false)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("setUsageWindows(false) did not cancel in-flight Claude probe")
	}
	n.usageWin.mu.Lock()
	accountsAfter, probesAfter := n.usageWin.accounts, n.usageWin.claudeProbes
	n.usageWin.mu.Unlock()
	if accountsAfter != nil || probesAfter != nil {
		t.Fatalf("canceled refresh repopulated cache: accounts=%+v probes=%+v", accountsAfter, probesAfter)
	}
}

func TestClaudeCLIUsageKeepsDefaultEnvUnsetWithInheritedNonDefaultDir(t *testing.T) {
	useFakeClaudeUsageCLI(t)
	prevCodex := codexUsageCLI
	codexUsageCLI = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { codexUsageCLI = prevCodex })

	home := t.TempDir()
	defaultDir := filepath.Join(home, ".claude")
	inheritedDir := filepath.Join(home, ".claude-inherited")
	for _, d := range []string{defaultDir, inheritedDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", inheritedDir)
	t.Setenv("PERSONA_TEST_CLAUDE_DEFAULT_DIR", defaultDir)

	now := time.Now().UTC().Truncate(time.Second)
	writeClaudeUsageFixture(t, defaultDir, "control", fmt.Sprintf(`{
		"subscription_type":"pro",
		"rate_limits_available":true,
		"rate_limits":{"five_hour":{"utilization":20,"resets_at":%q}}
	}`, now.Add(2*time.Hour).Format(time.RFC3339)))
	writeClaudeUsageFixture(t, inheritedDir, "control", fmt.Sprintf(`{
		"subscription_type":"max",
		"rate_limits_available":true,
		"rate_limits":{"five_hour":{"utilization":60,"resets_at":%q}}
	}`, now.Add(3*time.Hour).Format(time.RFC3339)))

	n := &Node{home: home, dir: t.TempDir(), cdirs: newClaudeDirs()}
	n.cdirs.scan = func() map[int]procEnv { return nil }
	n.refreshUsageWindows(t.Context())

	if envSeen, err := os.ReadFile(filepath.Join(defaultDir, "fixture-config-dir-env")); err != nil || string(envSeen) != "<unset>" {
		t.Errorf("default dir CLAUDE_CONFIG_DIR = %q (err %v), want <unset> even when inherited dir points elsewhere", string(envSeen), err)
	}
	if envSeen, err := os.ReadFile(filepath.Join(inheritedDir, "fixture-config-dir-env")); err != nil || string(envSeen) != inheritedDir {
		t.Errorf("inherited dir CLAUDE_CONFIG_DIR = %q (err %v), want %q", string(envSeen), err, inheritedDir)
	}
	if snap := n.usageSnapshot(); len(snap.Accounts) != 2 {
		t.Fatalf("expected 2 Claude accounts, got %+v", snap.Accounts)
	}
}

func TestClaudeCLIUsageSkipsSpawnWhenSingleDirStatusLineIsFresh(t *testing.T) {
	useFakeClaudeUsageCLI(t)
	prevCodex := codexUsageCLI
	codexUsageCLI = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { codexUsageCLI = prevCodex })

	home := t.TempDir()
	defaultDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(defaultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PERSONA_TEST_CLAUDE_DEFAULT_DIR", defaultDir)

	nodeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(nodeDir, "usage"), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	drop := fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":25,"resets_at":%d}}}`, now.Add(2*time.Hour).Unix())
	if err := os.WriteFile(filepath.Join(nodeDir, "usage", "claude.json"), []byte(drop), 0o600); err != nil {
		t.Fatal(err)
	}

	n := &Node{home: home, dir: nodeDir, cdirs: newClaudeDirs()}
	n.cdirs.scan = func() map[int]procEnv { return nil }
	n.refreshUsageWindows(t.Context())
	if got := readProbeCount(t, defaultDir); got != 0 {
		t.Fatalf("expected no CLI spawn when single-dir status line is fresh, got %d", got)
	}
	if snap := n.usageSnapshot(); len(snap.Accounts) != 1 || snap.Accounts[0].Windows[0].UsedPercent != 25 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}

func TestClaudeCLIUsageInitializeFailureDoesNotFallbackAndModelTurnDisablesPrint(t *testing.T) {
	useFakeClaudeUsageCLI(t)
	prevCodex := codexUsageCLI
	codexUsageCLI = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { codexUsageCLI = prevCodex })

	home := t.TempDir()
	defaultDir := filepath.Join(home, ".claude")
	fallbackDir := filepath.Join(home, ".claude-old")
	for _, d := range []string{defaultDir, fallbackDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PERSONA_TEST_CLAUDE_DEFAULT_DIR", defaultDir)

	now := time.Now().UTC().Truncate(time.Second)
	prevNow := claudeUsageNow
	claudeUsageNow = func() time.Time { return now }
	t.Cleanup(func() { claudeUsageNow = prevNow })

	// defaultDir crashes during initialize: must NOT fall back to `-p /usage`.
	writeClaudeUsageFixture(t, defaultDir, "control", "init-crash")
	writeClaudeUsageFixture(t, defaultDir, "print", fmt.Sprintf("Current session: 15%% used · resets %s\n", now.Add(2*time.Hour).Format(time.RFC3339)))

	// fallbackDir rejects get_usage as unsupported, then `-p /usage` returns a billed model turn: must be rejected and
	// disable further `-p /usage` fallbacks for that directory.
	writeClaudeUsageFixture(t, fallbackDir, "control", "unsupported")
	writeClaudeUsageFixture(t, fallbackDir, "print", `[{"type":"assistant","message":{"role":"assistant","content":"hi"}},{"type":"result","subtype":"success","total_cost_usd":0.01,"duration_api_ms":700,"result":"Current session: 50% used"}]`)

	n := &Node{home: home, dir: t.TempDir(), cdirs: newClaudeDirs()}
	n.cdirs.scan = func() map[int]procEnv { return nil }
	n.cdirs.known = []string{fallbackDir}

	n.refreshUsageWindows(t.Context())
	if got := readProbeCount(t, defaultDir); got != 1 {
		t.Fatalf("initialize crash triggered fallback spawn: count=%d, want 1", got)
	}
	if got := readProbeCount(t, fallbackDir); got != 2 {
		t.Fatalf("expected 1 control + 1 print probe on first unsupported run, got %d", got)
	}
	if snap := n.usageSnapshot(); len(snap.Accounts) != 0 {
		t.Fatalf("expected no accounts from crashed/billed probes, got %+v", snap.Accounts)
	}

	// Next active probe on fallbackDir must skip `-p /usage` now that modelTurn disabled it.
	hist := filepath.Join(fallbackDir, "history.jsonl")
	if err := os.WriteFile(hist, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(hist, now.Add(time.Second), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	n.refreshUsageWindows(t.Context())
	if got := readProbeCount(t, fallbackDir); got != 3 {
		t.Fatalf("expected no second print fallback after modelTurn, got %d total spawns", got)
	}
}

func TestClaudeCLIUsageRetainsNewerCLISampleAfterStatusLineAges(t *testing.T) {
	useFakeClaudeUsageCLI(t)
	prevCodex := codexUsageCLI
	codexUsageCLI = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { codexUsageCLI = prevCodex })

	home := t.TempDir()
	defaultDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(defaultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PERSONA_TEST_CLAUDE_DEFAULT_DIR", defaultDir)

	nodeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(nodeDir, "usage"), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	prevNow := claudeUsageNow
	claudeUsageNow = func() time.Time { return now }
	t.Cleanup(func() { claudeUsageNow = prevNow })

	resetAt := now.Add(2 * time.Hour)
	writeClaudeUsageFixture(t, defaultDir, "control", fmt.Sprintf(`{
		"subscription_type":"pro",
		"rate_limits_available":true,
		"rate_limits":{"five_hour":{"utilization":45,"resets_at":%q}}
	}`, resetAt.Format(time.RFC3339)))

	n := &Node{home: home, dir: nodeDir, cdirs: newClaudeDirs()}
	n.cdirs.scan = func() map[int]procEnv { return nil }

	// First refresh collects the newer CLI sample (45% at now).
	n.refreshUsageWindows(t.Context())

	// A fresh status-line drop (31% at now-2m) arrives for the same account.
	statusPath := filepath.Join(nodeDir, "usage", "claude.json")
	drop := fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":31,"resets_at":%d}}}`, resetAt.Unix())
	if err := os.WriteFile(statusPath, []byte(drop), 0o600); err != nil {
		t.Fatal(err)
	}
	statusTime := now.Add(-2 * time.Minute)
	if err := os.Chtimes(statusPath, statusTime, statusTime); err != nil {
		t.Fatal(err)
	}
	n.refreshUsageWindows(t.Context())

	// While status-line is fresh (2m old <= 15m), status-line wins.
	snap := n.usageSnapshot()
	if len(snap.Accounts) != 1 || snap.Accounts[0].Windows[0].UsedPercent != 31 {
		t.Fatalf("expected fresh status-line sample (31%%), got %+v", snap.Accounts)
	}

	// Once status-line ages past 15m without any new CLI probe, the preserved newer CLI sample (45%) takes over.
	now = now.Add(20 * time.Minute)
	snap = n.usageSnapshot()
	if len(snap.Accounts) != 1 || snap.Accounts[0].Name != "Claude Pro" || snap.Accounts[0].Windows[0].UsedPercent != 45 {
		t.Fatalf("expected preserved newer CLI sample (45%%) after status-line aged, got %+v", snap.Accounts)
	}
}

func TestClaudeCLIUsageFallsBackWhenGetUsageReturnsNoWindows(t *testing.T) {
	useFakeClaudeUsageCLI(t)
	prevCodex := codexUsageCLI
	codexUsageCLI = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { codexUsageCLI = prevCodex })

	home := t.TempDir()
	maxDir := filepath.Join(home, ".claude")
	apiDir := filepath.Join(home, ".claude-api")
	for _, d := range []string{maxDir, apiDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PERSONA_TEST_CLAUDE_DEFAULT_DIR", maxDir)

	now := time.Now().UTC().Truncate(time.Second)
	prevNow := claudeUsageNow
	claudeUsageNow = func() time.Time { return now }
	t.Cleanup(func() { claudeUsageNow = prevNow })

	// Fresh process reports rate_limits_available: true with rate_limits: null; `-p /usage` returns limits[].
	writeClaudeUsageFixture(t, maxDir, "control", `{"subscription_type":"max","rate_limits_available":true,"rate_limits":null}`)
	writeClaudeUsageFixture(t, maxDir, "print", fmt.Sprintf(`[
		{"type":"system","subtype":"init"},
		{"type":"assistant","usage_report":{
			"session":{"total_cost_usd":0,"total_api_duration_ms":0},
			"rate_limits":{"limits":[
				{"kind":"session","percent":34,"resets_at":%q},
				{"kind":"weekly_all","percent":52,"resets_at":%q},
				{"kind":"weekly_scoped","percent":12,"resets_at":%q,"scope":{"model":{"display_name":"Sonnet"}}}
			]}
		}},
		{"type":"result","subtype":"success","is_error":false,"result":""}
	]`,
		now.Add(2*time.Hour).Format(time.RFC3339),
		now.Add(72*time.Hour).Format(time.RFC3339),
		now.Add(72*time.Hour).Format(time.RFC3339),
	))

	// Authoritative rate_limits_available: false must still NOT fall back to `-p /usage`.
	writeClaudeUsageFixture(t, apiDir, "control", `{"rate_limits_available":false,"rate_limits":null}`)
	writeClaudeUsageFixture(t, apiDir, "print", fmt.Sprintf("Current session: 99%% used · resets %s\n", now.Add(2*time.Hour).Format(time.RFC3339)))

	n := &Node{home: home, dir: t.TempDir(), cdirs: newClaudeDirs()}
	n.cdirs.scan = func() map[int]procEnv { return nil }
	n.cdirs.known = []string{apiDir}

	n.refreshUsageWindows(t.Context())
	if got := readProbeCount(t, maxDir); got != 2 {
		t.Fatalf("expected 1 control + 1 print probe for empty get_usage, got %d", got)
	}
	if got := readProbeCount(t, apiDir); got != 1 {
		t.Fatalf("rate_limits_available:false triggered print fallback: count=%d, want 1", got)
	}
	snap := n.usageSnapshot()
	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 Claude account, got %+v", snap.Accounts)
	}
	acct := snap.Accounts[0]
	if acct.Name != "Claude Max" || acct.Plan != "max" || len(acct.Windows) != 3 {
		t.Fatalf("unexpected account: %+v", acct)
	}
	if acct.Windows[0].Label != "5h" || acct.Windows[0].UsedPercent != 34 ||
		acct.Windows[1].Label != "7d" || acct.Windows[1].Model != "" || acct.Windows[1].UsedPercent != 52 ||
		acct.Windows[2].Label != "7d" || acct.Windows[2].Model != "Sonnet" || acct.Windows[2].UsedPercent != 12 {
		t.Fatalf("unexpected windows: %+v", acct.Windows)
	}
}

func TestClaudeCLIUsageRunsThroughLoginShellWrapper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX interactive shell rc files")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	prevCmd := claudeUsageCommand
	claudeUsageCommand = defaultClaudeUsageCommand
	t.Cleanup(func() { claudeUsageCommand = prevCmd })
	prevCodex := codexUsageCLI
	codexUsageCLI = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { codexUsageCLI = prevCodex })

	home := t.TempDir()
	defaultDir := filepath.Join(home, ".claude")
	customDir := filepath.Join(home, ".claude-work")
	for _, d := range []string{defaultDir, customDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Write both .zshrc and .bashrc so loginShellProgram() finds the shell function wrapper on zsh and bash hosts.
	rc := "echo 'rc-welcome-banner'\n" +
		"export CLAUDE_CONFIG_DIR=/should/be/overridden\n" +
		"claude() {\n" +
		"  PERSONA_TEST_CLAUDE_WRAPPER=1 " + shellQuote(binary) + " -- \"$@\"\n" +
		"}\n"
	for _, name := range []string{".zshrc", ".bashrc"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(rc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PERSONA_TEST_CLAUDE_USAGE_CLI", "1")
	t.Setenv("PERSONA_TEST_REQUIRE_WRAPPER", "1")
	t.Setenv("PERSONA_TEST_CLAUDE_DEFAULT_DIR", defaultDir)

	now := time.Now().UTC().Truncate(time.Second)
	prevNow := claudeUsageNow
	claudeUsageNow = func() time.Time { return now }
	t.Cleanup(func() { claudeUsageNow = prevNow })

	// Default dir: wrapper-gated get_usage returns rate_limits: null, falling back to -p /usage through the wrapper.
	writeClaudeUsageFixture(t, defaultDir, "control", `{"subscription_type":"max","rate_limits_available":true,"rate_limits":null}`)
	writeClaudeUsageFixture(t, defaultDir, "print", fmt.Sprintf("[wrapper] ok\n[{\"type\":\"assistant\",\"usage_report\":{\"session\":{\"total_cost_usd\":0,\"total_api_duration_ms\":0},\"rate_limits\":{\"limits\":[{\"kind\":\"session\",\"percent\":41,\"resets_at\":%q}]}}},{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"\"}]\n",
		now.Add(2*time.Hour).Format(time.RFC3339),
	))
	// Custom dir: direct get_usage through the same wrapper.
	writeClaudeUsageFixture(t, customDir, "control", fmt.Sprintf(`{"subscription_type":"pro","rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":23,"resets_at":%q}}}`,
		now.Add(3*time.Hour).Format(time.RFC3339),
	))

	n := &Node{home: home, dir: t.TempDir(), cdirs: newClaudeDirs()}
	n.cdirs.scan = func() map[int]procEnv { return nil }
	n.cdirs.known = []string{customDir}

	n.refreshUsageWindows(t.Context())
	if envSeen, err := os.ReadFile(filepath.Join(defaultDir, "fixture-config-dir-env")); err != nil || string(envSeen) != "<unset>" {
		t.Errorf("default dir CLAUDE_CONFIG_DIR = %q (err %v), want <unset>", string(envSeen), err)
	}
	if envSeen, err := os.ReadFile(filepath.Join(customDir, "fixture-config-dir-env")); err != nil || string(envSeen) != customDir {
		t.Errorf("custom dir CLAUDE_CONFIG_DIR = %q (err %v), want %q", string(envSeen), err, customDir)
	}
	snap := n.usageSnapshot()
	if len(snap.Accounts) != 2 {
		t.Fatalf("expected 2 Claude accounts via login shell wrapper, got %+v", snap.Accounts)
	}
	var gotMax, gotPro bool
	for _, a := range snap.Accounts {
		if a.Name == "Claude Max" && a.Plan == "max" && len(a.Windows) == 1 && a.Windows[0].UsedPercent == 41 {
			gotMax = true
		}
		if a.Name == "Claude Pro" && a.Plan == "pro" && len(a.Windows) == 1 && a.Windows[0].UsedPercent == 23 {
			gotPro = true
		}
	}
	if !gotMax || !gotPro {
		t.Fatalf("unexpected accounts from login shell wrapper: %+v", snap.Accounts)
	}
}

func useFakeClaudeUsageCLI(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude CLI launcher uses a POSIX shell")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nexec " + shellQuote(binary) + " -- \"$@\"\n"
	if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := claudeUsageCommand
	claudeUsageCommand = func(ctx context.Context, _ string, env []string, args []string, _ bool) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, cli, args...)
		cmd.Env = env
		return cmd, nil
	}
	t.Cleanup(func() { claudeUsageCommand = previous })
	t.Setenv("PERSONA_TEST_CLAUDE_USAGE_CLI", "1")
}

func writeClaudeUsageFixture(t *testing.T, dir, kind, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "fixture-"+kind), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readProbeCount(t *testing.T, dir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "fixture-count"))
	if err != nil {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(string(b)), "\n"))
}

// runFakeClaudeUsageCLI implements both Claude Code's stream-json control protocol and `-p /usage` for tests.
// Invoked from TestMain before the test harness unsets CLAUDE_CONFIG_DIR.
func runFakeClaudeUsageCLI() {
	if os.Getenv("PERSONA_TEST_REQUIRE_WRAPPER") == "1" && os.Getenv("PERSONA_TEST_CLAUDE_WRAPPER") != "1" {
		os.Exit(1)
	}
	cfgEnv, set := os.LookupEnv("CLAUDE_CONFIG_DIR")
	dir := cfgEnv
	if !set {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	if _, err := os.Stat(filepath.Join(dir, "fixture-control")); err != nil {
		if h := os.Getenv("PERSONA_TEST_CLAUDE_DEFAULT_DIR"); h != "" {
			dir = h
		}
	}
	val := "<unset>"
	if set {
		val = cfgEnv
	}
	_ = os.WriteFile(filepath.Join(dir, "fixture-config-dir-env"), []byte(val), 0o600)
	f, err := os.OpenFile(filepath.Join(dir, "fixture-count"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.WriteString("1\n")
		_ = f.Close()
	}

	args := os.Args
	if slices.Contains(args, "/usage") {
		body, _ := os.ReadFile(filepath.Join(dir, "fixture-print"))
		_, _ = os.Stdout.Write(body)
		return
	}

	scanner := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var msg struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil || msg.Type != "control_request" {
			continue
		}
		switch msg.Request.Subtype {
		case "initialize":
			body, _ := os.ReadFile(filepath.Join(dir, "fixture-control"))
			if strings.TrimSpace(string(body)) == "init-crash" {
				os.Exit(1)
			}
			_ = out.Encode(map[string]any{
				"type": "control_response",
				"response": map[string]any{
					"subtype":    "success",
					"request_id": msg.RequestID,
					"response":   map[string]any{"account": map[string]any{"subscriptionType": "Claude Pro"}},
				},
			})
		case "get_usage":
			body, err := os.ReadFile(filepath.Join(dir, "fixture-control"))
			if err != nil {
				os.Exit(1)
			}
			switch strings.TrimSpace(string(body)) {
			case "block":
				_ = os.WriteFile(filepath.Join(dir, "fixture-started"), nil, 0o600)
				continue // stay blocked in scanner.Scan() until killed by cancel
			case "unsupported":
				_ = out.Encode(map[string]any{
					"type": "control_response",
					"response": map[string]any{
						"subtype":    "error",
						"request_id": msg.RequestID,
						"error":      "Unsupported control request subtype: get_usage",
					},
				})
				return
			default:
				_ = out.Encode(map[string]any{
					"type": "control_response",
					"response": map[string]any{
						"subtype":    "success",
						"request_id": msg.RequestID,
						"response":   json.RawMessage(body),
					},
				})
				return
			}
		}
	}
}
