package node

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	codexUsagePollParallel = 4
	codexUsagePollTimeout  = 5 * time.Second // each home, including app-server startup
)

// Usage covers standalone Codex homes as well as Desktop homes. Prefer the machine's CLI, falling back to the
// Desktop bundle when it is the only install. Desktop's own feature calls keep using its bundled CLI first.
var codexUsageCLI = func() (string, error) {
	if cli, err := agentExecutable("codex"); err == nil {
		return cli, nil
	}
	return codexAppCLI()
}

// collectCodexAppUsage prefers an official snapshot over the corresponding rollout. Each home has its own deadline,
// so an unavailable app-server cannot consume the other homes' time. Keep the number of short-lived processes bounded.
func collectCodexAppUsage(ctx context.Context, homes []string, found map[string]proto.UsageNodeAccount) {
	cli, err := codexUsageCLI()
	if err != nil {
		return
	}
	proxyEnv := codexUsageProxyEnv(ctx)
	var reads errgroup.Group
	reads.SetLimit(codexUsagePollParallel)
	accounts := make([]proto.UsageNodeAccount, len(homes))
	for i, home := range homes {
		if ctx.Err() != nil {
			break
		}
		if info, err := os.Stat(home); err != nil || !info.IsDir() {
			continue // starting Codex here would create an unused home
		}
		reads.Go(func() error {
			readCtx, cancel := context.WithTimeout(ctx, codexUsagePollTimeout)
			defer cancel()
			accounts[i] = readCodexAppUsageWithEnv(readCtx, cli, home, proxyEnv)
			return nil
		})
	}
	_ = reads.Wait()
	for _, account := range accounts {
		if len(account.Windows) > 0 {
			found["codex:"+account.SourceKey] = account
		}
	}
}

func codexUsageProxyEnv(ctx context.Context) []string {
	if hasExplicitProxyEnv(os.Environ()) {
		return nil
	}
	return withShellProxyEnv(ctx, withSystemProxyEnv(ctx, nil))
}

// readCodexAppUsage asks only for usage metadata. The CLI manages its own login. In particular, the account ID comes
// from the very same response as the windows, so changing the signed-in account never relabels a historical rollout.
func readCodexAppUsage(ctx context.Context, cli, home string) proto.UsageNodeAccount {
	return readCodexAppUsageWithEnv(ctx, cli, home, codexUsageProxyEnv(ctx))
}

func readCodexAppUsageWithEnv(ctx context.Context, cli, home string, proxyEnv []string) proto.UsageNodeAccount {
	app, err := startCodexStdioWithEnv(ctx, cli, home, os.TempDir(), proxyEnv)
	if err != nil {
		return proto.UsageNodeAccount{}
	}
	defer app.close()
	type window struct {
		UsedPercent        *float64 `json:"usedPercent"`
		WindowDurationMins *int     `json:"windowDurationMins"`
		ResetsAt           *int64   `json:"resetsAt"`
	}
	type limits struct {
		LimitID   string  `json:"limitId"`
		PlanType  string  `json:"planType"`
		Primary   *window `json:"primary"`
		Secondary *window `json:"secondary"`
	}
	var result struct {
		AccountID           string             `json:"accountId"`
		RateLimits          *limits            `json:"rateLimits"`
		RateLimitsByLimitID map[string]*limits `json:"rateLimitsByLimitId"`
	}
	params := map[string]bool{"excludeResetCreditDetails": true}
	if err := app.call(ctx, "account/rateLimits/read", params, &result); err != nil {
		return proto.UsageNodeAccount{}
	}
	bucket := result.RateLimitsByLimitID["codex"]
	if bucket == nil && result.RateLimits != nil && (result.RateLimits.LimitID == "codex" || result.RateLimits.LimitID == "") {
		bucket = result.RateLimits
	}
	if bucket == nil {
		return proto.UsageNodeAccount{}
	}
	var windows []proto.UsageWindow
	for _, w := range []*window{bucket.Primary, bucket.Secondary} {
		if w != nil {
			windows = appendCodexWindow(windows, w.UsedPercent, w.WindowDurationMins, w.ResetsAt)
		}
	}
	if len(windows) == 0 {
		return proto.UsageNodeAccount{}
	}
	// Keep recognized windows even when they have expired. The manager must see the newest snapshot for this
	// account before dropping expired windows, or a stale observation from another machine could resurrect it.
	sort.SliceStable(windows, func(i, j int) bool { return windows[i].WindowMinutes < windows[j].WindowMinutes })
	plan := strings.ToLower(strings.TrimSpace(bucket.PlanType))
	account := proto.UsageNodeAccount{
		UsageAccount: proto.UsageAccount{
			Provider: "codex", Name: codexAccountName(plan), Plan: plan,
			Windows: windows, SampledAt: time.Now().UTC(),
		},
		SourceKey: codexUsageKey(realPath(home)),
	}
	switch plan {
	case "free", "go", "plus", "pro", "prolite", "promax":
		// Workspace members can share an account ID without sharing their individual limits.
		if id := strings.TrimSpace(result.AccountID); id != "" {
			account.AccountKey = codexUsageKey(id)
		}
	}
	return account
}
