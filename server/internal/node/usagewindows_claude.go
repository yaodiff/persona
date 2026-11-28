package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	// Embed IANA time zones so /usage reset lines parse on platforms without a system zone database.
	_ "time/tzdata"

	"golang.org/x/sync/errgroup"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Claude Code subscription usage via the local CLI (#12412, docs/server/usage-windows.md).
//
// Persona never reads Claude's OAuth tokens or calls /api/oauth/usage directly. Instead, in a bounded background poll
// (mirroring usagewindows_codex.go), the node invokes the computer's built-in `claude` launcher through the user's
// login shell (so shell functions, aliases, and PATH wrappers match what the user runs) without sending a model
// request:
//
//   1. Preferred: stream-json control protocol (`initialize`, then `get_usage` with `skip_behaviors: true`), which
//      returns structured JSON (`rate_limits_available`, `subscription_type`, `five_hour`, `seven_day`, `model_scoped`,
//      `limits[]`) with RFC 3339 timestamps.
//   2. Fallback: `claude -p /usage --output-format json --verbose` when the CLI rejects `get_usage` as unsupported or
//      reports `rate_limits_available: true` with no windows (for example `rate_limits: null` in a fresh process),
//      reading `usage_report.rate_limits` first and falling back to `/usage`'s printed text.
//
// Each config directory is probed at most once per claudeUsageMinInterval, and after the first poll only when Claude
// Code was active in that directory or a previously observed window has reset. A fresh status-line file
// (`usage/claude*.json`) still wins over a CLI reading for the same account.

const (
	claudeUsagePollParallel = 4
	claudeUsagePollTimeout  = 45 * time.Second // each CLI invocation, covering slow login shells and launcher wrappers
	claudeUsageMinInterval  = 5 * time.Minute  // minimum interval between probes for one config dir
	claudeUsageIdleBackoff  = 30 * time.Minute // retry interval when an idle dir had no subscription windows
	claudeUsageStatusFresh  = 15 * time.Minute // status-line drop newer than this wins over a CLI probe
	claudeResetSlack        = 2 * time.Hour    // clock skew slack when inferring year/day for text reset times
	claudeUsagePrefix       = "claude-cli:"
	claudeUsageSettingsJSON = `{"disableAllHooks":true}`
	claudeUsageBeginMarker  = "__PERSONA_CLAUDE_USAGE_BEGIN__"
	claudeUsageEndMarker    = "__PERSONA_CLAUDE_USAGE_END__"
)

var (
	// claudeUsageCommand builds the subprocess that runs the built-in `claude` launcher for one probe; swapped in tests.
	claudeUsageCommand = defaultClaudeUsageCommand

	// claudeUsageNow provides the current time; swapped in tests.
	claudeUsageNow = time.Now

	errClaudeControlUnsupported = errors.New("claude control get_usage unsupported")

	claudeUsageLineRE   = lazyre.New(`^Current (session|week(?: \(([^)]+)\))?):\s*([0-9.]+)% used(?:\s*·\s*resets (.+))?$`)
	claudeUsageCountsRE = lazyre.New(`(?: · )?Last [0-9]+[a-z]+ · [0-9][0-9,]* requests? · [0-9][0-9,]* sessions?`)
	claudeUsageDeniedRE = lazyre.New(`(?i)\b(401|403)\b|not (logged|signed) in|signed out|sign-in (has )?expired|unauthorized|forbidden|authentication (failed|required)|invalid (access )?token`)
)

type claudeUsageProbe struct {
	at              time.Time
	hasWindows      bool
	nextReset       time.Time
	noPrintFallback bool
}

// usageWindowClaudeDirs lists existing Claude Code config directories to poll, deduplicated by real path.
func (n *Node) usageWindowClaudeDirs() []string {
	seen := map[string]bool{}
	var out []string
	for _, dir := range n.claudeConfigDirs() {
		if dir == "" {
			continue
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue // starting Claude here would create an unused config directory
		}
		real := realPath(dir)
		if seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, dir)
	}
	return out
}

// collectClaudeCLIUsage polls each eligible Claude config directory outside the usageWin lock and returns updated
// probe timestamps alongside any collected accounts written into found.
func (n *Node) collectClaudeCLIUsage(ctx context.Context, dirs []string, prevProbes map[string]claudeUsageProbe, found map[string]proto.UsageNodeAccount) map[string]claudeUsageProbe {
	if len(dirs) == 0 || ctx.Err() != nil {
		return nil
	}
	now := claudeUsageNow().UTC()
	singleDirStatusFresh := len(dirs) == 1 && hasFreshClaudeStatusFile(found, now)

	type target struct {
		dir             string
		real            string
		noPrintFallback bool
	}
	var targets []target
	for _, dir := range dirs {
		real := realPath(dir)
		prev, seen := prevProbes[real]
		active := seen && n.claudeDirActiveSince(dir, real, prev.at)
		if !shouldProbeClaudeDir(now, prev, seen, active, singleDirStatusFresh) {
			continue
		}
		targets = append(targets, target{dir: dir, real: real, noPrintFallback: prev.noPrintFallback})
	}
	if len(targets) == 0 {
		return nil
	}
	defaultUserDir := realPath(filepath.Join(n.home, ".claude"))
	inheritedConfigDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	inheritedIsDefault := inheritedConfigDir != "" && realPath(proto.ExpandHome(inheritedConfigDir, n.home)) == defaultUserDir

	type probeResult struct {
		real    string
		account proto.UsageNodeAccount
		probe   claudeUsageProbe
		ran     bool
	}
	results := make([]probeResult, len(targets))
	var reads errgroup.Group
	reads.SetLimit(claudeUsagePollParallel)
	for i, t := range targets {
		if ctx.Err() != nil {
			break
		}
		reads.Go(func() error {
			configDir := ""
			if t.real != defaultUserDir || inheritedIsDefault {
				configDir = t.dir
			}
			env := claudeUsageEnv(configDir)
			startedAt := claudeUsageNow().UTC()
			acct, ok, modelTurn := readClaudeCLIUsage(ctx, configDir, env, !t.noPrintFallback)
			if ctx.Err() != nil {
				return nil // canceled by disable/shutdown; do not record a probe timestamp
			}
			res := probeResult{
				real: t.real,
				ran:  true,
				probe: claudeUsageProbe{
					at:              startedAt,
					hasWindows:      ok && len(acct.Windows) > 0,
					nextReset:       earliestWindowReset(acct.Windows),
					noPrintFallback: t.noPrintFallback || modelTurn,
				},
			}
			if ok && len(acct.Windows) > 0 {
				source := claudeUsagePrefix + codexUsageKey(t.real)
				res.account = proto.UsageNodeAccount{UsageAccount: acct, SourceKey: source}
			}
			results[i] = res
			return nil
		})
	}
	_ = reads.Wait()

	updates := map[string]claudeUsageProbe{}
	for _, res := range results {
		if !res.ran {
			continue
		}
		updates[res.real] = res.probe
		if len(res.account.Windows) > 0 {
			found[res.account.SourceKey] = res.account
		}
	}
	return updates
}

func shouldProbeClaudeDir(now time.Time, prev claudeUsageProbe, seen, active, singleDirStatusFresh bool) bool {
	if singleDirStatusFresh {
		return false
	}
	if !seen {
		return true
	}
	if now.Sub(prev.at) < claudeUsageMinInterval {
		return false
	}
	if active {
		return true
	}
	if !prev.nextReset.IsZero() && !now.Before(prev.nextReset) {
		return true
	}
	if !prev.hasWindows && now.Sub(prev.at) >= claudeUsageIdleBackoff {
		return true
	}
	return false
}

func hasFreshClaudeStatusFile(found map[string]proto.UsageNodeAccount, now time.Time) bool {
	for k, a := range found {
		if isClaudeStatusKey(k) && len(a.Windows) > 0 && now.Sub(a.SampledAt) <= claudeUsageStatusFresh {
			return true
		}
	}
	return false
}

func isClaudeStatusKey(k string) bool { return strings.HasPrefix(k, "claude:") }

func isClaudeStreamKey(k string) bool { return strings.HasPrefix(k, "claude-stream:") }

func earliestWindowReset(wins []proto.UsageWindow) time.Time {
	var earliest time.Time
	for _, w := range wins {
		if w.ResetsAt == nil {
			continue
		}
		if earliest.IsZero() || w.ResetsAt.Before(earliest) {
			earliest = *w.ResetsAt
		}
	}
	return earliest
}

// claudeDirActiveSince reports whether Claude Code is currently running in real or modified its local activity files
// (history.jsonl, sessions/, projects/) after since.
func (n *Node) claudeDirActiveSince(dir, real string, since time.Time) bool {
	if d := n.cdirs; d != nil {
		d.mu.Lock()
		for _, pidDir := range d.byPID {
			if pidDir == "" {
				continue
			}
			if realPath(proto.ExpandHome(pidDir, n.home)) == real {
				d.mu.Unlock()
				return true
			}
		}
		d.mu.Unlock()
	}
	if st, err := os.Stat(filepath.Join(dir, "history.jsonl")); err == nil && st.ModTime().After(since) {
		return true
	}
	for _, sub := range []string{"sessions", "projects"} {
		p := filepath.Join(dir, sub)
		if st, err := os.Stat(p); err == nil && st.ModTime().After(since) {
			return true
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if info, err := e.Info(); err == nil && info.ModTime().After(since) {
				return true
			}
		}
	}
	return false
}

// claudeUsageEnv builds the subprocess environment for probing configDir ("" means the default ~/.claude directory).
//
// On macOS, Claude Code derives the Keychain service name from CLAUDE_CONFIG_DIR whenever that variable is set, so
// explicitly setting CLAUDE_CONFIG_DIR=~/.claude misses the default Keychain login. Keep CLAUDE_CONFIG_DIR unset for
// the default ~/.claude directory unless the node's own inherited CLAUDE_CONFIG_DIR points at that same directory.
func claudeUsageEnv(configDir string) []string {
	env := withoutEnv(desktopSeedEnv(os.Environ()), []string{
		"CLAUDECODE",
		"CLAUDE_CODE_ENTRYPOINT",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
		"CLAUDE_CONFIG_DIR",
	})
	if configDir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+configDir)
	}
	return env
}

// claudeUsageShellScript builds the interactive login-shell command that runs the built-in `claude` launcher (matching
// shell functions, aliases, and PATH wrappers) with explicit CLAUDE_CONFIG_DIR handling and output framing markers.
func claudeUsageShellScript(configDir string, withEndMarker bool) string {
	var b strings.Builder
	b.WriteString("builtin printf '\\n" + claudeUsageBeginMarker + "\\n'; ")
	if configDir == "" {
		b.WriteString("unset CLAUDE_CONFIG_DIR; ")
	} else {
		b.WriteString("export CLAUDE_CONFIG_DIR=" + scriptPath(configDir) + "; ")
	}
	b.WriteString(`claude "$@"`)
	if withEndMarker {
		b.WriteString("; __persona_rc=$?; builtin printf '\\n" + claudeUsageEndMarker + "\\n'; exit $__persona_rc")
	}
	return b.String()
}

func defaultClaudeUsageCommand(ctx context.Context, configDir string, env []string, args []string, withEndMarker bool) (*exec.Cmd, error) {
	shell, err := interactiveShell()
	if err != nil {
		return nil, err
	}
	script := claudeUsageShellScript(configDir, withEndMarker)
	cmd := exec.CommandContext(ctx, shell, append([]string{"-ic", script, "persona"}, args...)...)
	cmd.Env = env
	return cmd, nil
}

// readClaudeCLIUsage reads Claude Code's subscription windows from the structured stream-json `get_usage` control
// request, falling back to `claude -p /usage --output-format json --verbose` when the CLI rejects `get_usage` as
// unsupported or reports `rate_limits_available: true` with no windows (for example `rate_limits: null` in a fresh
// process). Each subprocess step gets its own claudeUsagePollTimeout derived from ctx so slow wrappers can complete
// both steps while disable/shutdown cancels immediately.
func readClaudeCLIUsage(ctx context.Context, configDir string, env []string, allowPrintFallback bool) (proto.UsageAccount, bool, bool) {
	ctrlCtx, cancelCtrl := context.WithTimeout(ctx, claudeUsagePollTimeout)
	acct, available, err := readClaudeControlUsage(ctrlCtx, configDir, env)
	cancelCtrl()
	fallbackPlan := acct.Plan
	if err == nil {
		if !available {
			return proto.UsageAccount{}, false, false
		}
		if len(acct.Windows) > 0 {
			return acct, true, false
		}
		if !allowPrintFallback || ctx.Err() != nil {
			return proto.UsageAccount{}, false, false
		}
	} else if !allowPrintFallback || !errors.Is(err, errClaudeControlUnsupported) || ctx.Err() != nil {
		return proto.UsageAccount{}, false, false
	}
	printCtx, cancelPrint := context.WithTimeout(ctx, claudeUsagePollTimeout)
	defer cancelPrint()
	printAcct, ok, modelTurn := readClaudePrintUsage(printCtx, configDir, env)
	if ok && printAcct.Plan == "" && fallbackPlan != "" {
		printAcct.Plan = fallbackPlan
		printAcct.Name = claudeAccountName(fallbackPlan)
	}
	return printAcct, ok, modelTurn
}

func readClaudeControlUsage(ctx context.Context, configDir string, env []string) (proto.UsageAccount, bool, error) {
	tmp, err := os.MkdirTemp("", "persona-claude-usage-")
	if err != nil {
		return proto.UsageAccount{}, false, err
	}
	defer os.RemoveAll(tmp)

	cmd, err := claudeUsageCommand(ctx, configDir, env, []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--tools", "",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--settings", claudeUsageSettingsJSON,
		"--no-session-persistence",
	}, false)
	if err != nil {
		return proto.UsageAccount{}, false, err
	}
	cmd.Dir = tmp
	killGroupOnCancel(cmd)
	msysCmdLine(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return proto.UsageAccount{}, false, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return proto.UsageAccount{}, false, err
	}
	stopGroup := cmd.Cancel
	cmd.Cancel = sync.OnceValue(func() error {
		_ = in.Close()
		_ = out.Close()
		if err := stopGroup(); err != nil && cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	})
	if err := cmd.Start(); err != nil {
		return proto.UsageAccount{}, false, err
	}
	defer func() {
		_ = cmd.Cancel()
		_ = cmd.Wait()
	}()

	reader := bufio.NewReaderSize(out, 1<<20)
	initSub, err := sendClaudeControl(in, reader, "1", map[string]any{"subtype": "initialize"})
	if err != nil {
		if ctx.Err() != nil {
			return proto.UsageAccount{}, false, ctx.Err()
		}
		return proto.UsageAccount{}, false, err
	}
	var initInfo struct {
		Account *struct {
			SubscriptionType string `json:"subscriptionType"`
		} `json:"account"`
	}
	_ = json.Unmarshal(initSub, &initInfo)
	initPlan := ""
	if initInfo.Account != nil {
		initPlan = initInfo.Account.SubscriptionType
	}

	usageRaw, err := sendClaudeControl(in, reader, "2", map[string]any{"subtype": "get_usage", "skip_behaviors": true})
	if err != nil {
		if ctx.Err() != nil {
			return proto.UsageAccount{}, false, ctx.Err()
		}
		return proto.UsageAccount{Plan: normalizeClaudePlan(initPlan)}, false, err
	}
	acct, available, ok := parseClaudeGetUsage(usageRaw, initPlan, claudeUsageNow().UTC())
	if !ok {
		return proto.UsageAccount{}, false, errors.New("unexpected claude get_usage response")
	}
	return acct, available, nil
}

func sendClaudeControl(in io.Writer, reader *bufio.Reader, reqID string, req map[string]any) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": reqID,
		"request":    req,
	})
	if err != nil {
		return nil, err
	}
	if _, err := in.Write(append(payload, '\n')); err != nil {
		return nil, err
	}
	for {
		line, readErr := reader.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 && line[0] == '{' {
			var env struct {
				Type     string `json:"type"`
				Response struct {
					Subtype   string          `json:"subtype"`
					RequestID string          `json:"request_id"`
					Response  json.RawMessage `json:"response"`
					Error     string          `json:"error"`
				} `json:"response"`
			}
			if json.Unmarshal(line, &env) == nil && env.Type == "control_response" && env.Response.RequestID == reqID {
				if env.Response.Subtype != "success" {
					msg := strings.ToLower(env.Response.Error)
					if strings.Contains(msg, "unsupported") || strings.Contains(msg, "unknown") || strings.Contains(msg, "not supported") {
						return nil, fmt.Errorf("%w: %s", errClaudeControlUnsupported, env.Response.Error)
					}
					return nil, errors.New(env.Response.Error)
				}
				return env.Response.Response, nil
			}
		}
		if readErr != nil {
			return nil, readErr
		}
	}
}

type claudeRateLimitWindow struct {
	Utilization *float64        `json:"utilization"`
	ResetsAt    json.RawMessage `json:"resets_at"`
}

type claudeRateLimitRow struct {
	Kind     string          `json:"kind"`
	Percent  *float64        `json:"percent"`
	ResetsAt json.RawMessage `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

type claudeRateLimitsPayload struct {
	FiveHour       *claudeRateLimitWindow `json:"five_hour"`
	SevenDay       *claudeRateLimitWindow `json:"seven_day"`
	SevenDayOpus   *claudeRateLimitWindow `json:"seven_day_opus"`
	SevenDaySonnet *claudeRateLimitWindow `json:"seven_day_sonnet"`
	ModelScoped    []struct {
		DisplayName string          `json:"display_name"`
		Utilization *float64        `json:"utilization"`
		ResetsAt    json.RawMessage `json:"resets_at"`
	} `json:"model_scoped"`
	Limits []claudeRateLimitRow `json:"limits"`
}

// parseClaudeGetUsage parses the response body of Claude Code's `get_usage` control request.
// available is false when Claude Code explicitly reports `rate_limits_available: false` (for example an API key or
// signed-out profile), distinguishing "no subscription windows" from a malformed response (ok == false).
func parseClaudeGetUsage(data []byte, fallbackPlan string, now time.Time) (acct proto.UsageAccount, available, ok bool) {
	var resp struct {
		SubscriptionType    string                   `json:"subscription_type"`
		RateLimitsAvailable *bool                    `json:"rate_limits_available"`
		RateLimits          *claudeRateLimitsPayload `json:"rate_limits"`
	}
	if json.Unmarshal(data, &resp) != nil || resp.RateLimitsAvailable == nil {
		return proto.UsageAccount{}, false, false
	}
	if !*resp.RateLimitsAvailable {
		return proto.UsageAccount{}, false, true
	}
	plan := normalizeClaudePlan(resp.SubscriptionType)
	if plan == "" {
		plan = normalizeClaudePlan(fallbackPlan)
	}
	acct = proto.UsageAccount{
		Provider:  "claude",
		Name:      claudeAccountName(plan),
		Plan:      plan,
		SampledAt: now.UTC(),
	}
	if resp.RateLimits == nil {
		return acct, true, true
	}
	acct.Windows = parseClaudeRateLimitsPayload(resp.RateLimits, now)
	return acct, true, true
}

func parseClaudeRateLimitsPayload(rl *claudeRateLimitsPayload, now time.Time) []proto.UsageWindow {
	if rl == nil {
		return nil
	}
	type slot struct {
		minutes int
		model   string
	}
	seen := map[slot]bool{}
	var wins []proto.UsageWindow
	add := func(minutes int, model string, pct *float64, rawResets json.RawMessage) {
		if pct == nil || minutes <= 0 {
			return
		}
		model = strings.TrimSpace(model)
		k := slot{minutes, strings.ToLower(model)}
		if seen[k] {
			return
		}
		seen[k] = true
		w := proto.UsageWindow{
			Label:         proto.WindowLabel(minutes),
			WindowMinutes: minutes,
			UsedPercent:   clampPercent(*pct),
			Model:         model,
		}
		if t, ok := transcript.ParseResetsAt(rawResets); ok {
			w.ResetsAt = &t
		}
		wins = append(wins, w)
	}

	if rl.FiveHour != nil {
		add(5*60, "", rl.FiveHour.Utilization, rl.FiveHour.ResetsAt)
	}
	if rl.SevenDay != nil {
		add(7*24*60, "", rl.SevenDay.Utilization, rl.SevenDay.ResetsAt)
	}
	for _, ms := range rl.ModelScoped {
		if name := strings.TrimSpace(ms.DisplayName); name != "" {
			add(7*24*60, name, ms.Utilization, ms.ResetsAt)
		}
	}
	if rl.SevenDayOpus != nil {
		add(7*24*60, "Opus", rl.SevenDayOpus.Utilization, rl.SevenDayOpus.ResetsAt)
	}
	if rl.SevenDaySonnet != nil {
		add(7*24*60, "Sonnet", rl.SevenDaySonnet.Utilization, rl.SevenDaySonnet.ResetsAt)
	}
	// Fallback to the server's limits[] rows (for example Enterprise seats where five_hour/seven_day are omitted).
	for _, row := range rl.Limits {
		switch row.Kind {
		case "session":
			add(5*60, "", row.Percent, row.ResetsAt)
		case "weekly_all":
			add(7*24*60, "", row.Percent, row.ResetsAt)
		case "weekly_scoped":
			if row.Scope != nil && row.Scope.Model != nil {
				if name := strings.TrimSpace(row.Scope.Model.DisplayName); name != "" {
					add(7*24*60, name, row.Percent, row.ResetsAt)
				}
			}
		}
	}
	wins = liveWindows(wins, now)
	sort.SliceStable(wins, func(i, j int) bool {
		if wins[i].WindowMinutes != wins[j].WindowMinutes {
			return wins[i].WindowMinutes < wins[j].WindowMinutes
		}
		return wins[i].Model < wins[j].Model
	})
	return wins
}

func normalizeClaudePlan(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "claude ")
	s = strings.TrimSpace(s)
	switch s {
	case "", "none", "unknown":
		return ""
	default:
		return s
	}
}

func claudeAccountName(plan string) string {
	if plan == "" {
		return "Claude"
	}
	return "Claude " + strings.ToUpper(plan[:1]) + plan[1:]
}

func readClaudePrintUsage(ctx context.Context, configDir string, env []string) (proto.UsageAccount, bool, bool) {
	tmp, err := os.MkdirTemp("", "persona-claude-usage-")
	if err != nil {
		return proto.UsageAccount{}, false, false
	}
	defer os.RemoveAll(tmp)

	cmd, err := claudeUsageCommand(ctx, configDir, env, []string{
		"-p", "/usage",
		"--output-format", "json",
		"--verbose",
		"--tools", "",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--settings", claudeUsageSettingsJSON,
		"--no-session-persistence",
	}, true)
	if err != nil {
		return proto.UsageAccount{}, false, false
	}
	cmd.Dir = tmp
	cmd.Stdin = nil
	killGroupOnCancel(cmd)
	msysCmdLine(cmd)
	out, _ := cmd.Output()
	if ctx.Err() != nil {
		return proto.UsageAccount{}, false, false
	}
	out = extractClaudeUsageOutput(out)
	if len(out) == 0 {
		return proto.UsageAccount{}, false, false
	}
	return parseClaudePrintUsage(out, claudeUsageNow().UTC())
}

// extractClaudeUsageOutput strips interactive login-shell rc banners and exit lines outside our framing markers.
func extractClaudeUsageOutput(data []byte) []byte {
	if _, after, ok := bytes.Cut(data, []byte(claudeUsageBeginMarker)); ok {
		data = after
	}
	if before, _, ok := bytes.Cut(data, []byte(claudeUsageEndMarker)); ok {
		data = before
	}
	return bytes.TrimSpace(data)
}

type claudePrintItem struct {
	Type          string          `json:"type"`
	IsError       bool            `json:"is_error"`
	Result        string          `json:"result"`
	TotalCostUSD  float64         `json:"total_cost_usd"`
	DurationAPIMS float64         `json:"duration_api_ms"`
	Message       json.RawMessage `json:"message"`
	UsageReport   *struct {
		Session *struct {
			TotalCostUSD       float64 `json:"total_cost_usd"`
			TotalAPIDurationMS float64 `json:"total_api_duration_ms"`
		} `json:"session"`
		RateLimits *claudeRateLimitsPayload `json:"rate_limits"`
	} `json:"usage_report"`
}

// parseClaudePrintJSON finds a JSON array or one or more JSON objects emitted by `claude -p /usage --output-format json`,
// skipping any plain-text banner lines printed by a launcher wrapper before or after the JSON payload.
func parseClaudePrintJSON(trimmed []byte) (items []claudePrintItem, found bool) {
	for i := 0; i < len(trimmed); {
		line := bytes.TrimLeft(trimmed[i:], " \t\r")
		offset := len(trimmed) - len(line)
		if len(line) > 0 {
			switch line[0] {
			case '[':
				var arr []claudePrintItem
				dec := json.NewDecoder(bytes.NewReader(trimmed[offset:]))
				if dec.Decode(&arr) == nil && len(arr) > 0 {
					for _, it := range arr {
						if it.Type != "" || it.UsageReport != nil {
							return arr, true
						}
					}
				}
			case '{':
				var obj claudePrintItem
				dec := json.NewDecoder(bytes.NewReader(trimmed[offset:]))
				if dec.Decode(&obj) == nil && (obj.Type != "" || obj.UsageReport != nil) {
					items = append(items, obj)
					if step := int(dec.InputOffset()); step > 0 {
						i = offset + step
						continue
					}
				}
			}
		}
		nl := bytes.IndexByte(trimmed[i:], '\n')
		if nl < 0 {
			break
		}
		i += nl + 1
	}
	return items, len(items) > 0
}

// parseClaudePrintUsage reads the output of `claude -p /usage --output-format json --verbose`: structured
// `usage_report.rate_limits` first, then the `result` text, and finally plain stdout if not JSON.
// If the output indicates a billed model turn ran (an assistant `message` or nonzero API cost/duration), it rejects
// the response and reports modelTurn=true so the caller stops using the `-p /usage` fallback for that directory.
func parseClaudePrintUsage(data []byte, now time.Time) (acct proto.UsageAccount, ok bool, modelTurn bool) {
	trimmed := extractClaudeUsageOutput(data)
	if len(trimmed) == 0 {
		return proto.UsageAccount{}, false, false
	}
	if items, found := parseClaudePrintJSON(trimmed); found {
		var resultText string
		var isErr bool
		var structured []proto.UsageWindow
		for _, item := range items {
			if item.TotalCostUSD > 0 || item.DurationAPIMS > 0 {
				return proto.UsageAccount{}, false, true
			}
			if item.Type == "assistant" {
				msg := bytes.TrimSpace(item.Message)
				if len(msg) > 0 && !bytes.Equal(msg, []byte("null")) {
					return proto.UsageAccount{}, false, true
				}
			}
			if item.UsageReport != nil {
				if s := item.UsageReport.Session; s != nil && (s.TotalCostUSD > 0 || s.TotalAPIDurationMS > 0) {
					return proto.UsageAccount{}, false, true
				}
				if item.UsageReport.RateLimits != nil && len(structured) == 0 {
					structured = parseClaudeRateLimitsPayload(item.UsageReport.RateLimits, now)
				}
			}
			if item.Type == "result" {
				isErr = item.IsError
				resultText = item.Result
			}
		}
		if isErr {
			return proto.UsageAccount{}, false, false
		}
		if len(structured) > 0 {
			return proto.UsageAccount{
				Provider:  "claude",
				Name:      "Claude",
				Windows:   structured,
				SampledAt: now.UTC(),
			}, true, false
		}
		if resultText != "" {
			a, ok := parseClaudeUsageText(resultText, now)
			return a, ok, false
		}
		return proto.UsageAccount{}, false, false
	}
	a, ok := parseClaudeUsageText(string(trimmed), now)
	return a, ok, false
}

// parseClaudeUsageText parses human-readable `/usage` lines into a Claude UsageAccount.
func parseClaudeUsageText(text string, now time.Time) (proto.UsageAccount, bool) {
	clean := strings.TrimSpace(terminalEscapes.ReplaceAllString(text, ""))
	if clean == "" {
		return proto.UsageAccount{}, false
	}
	lines := strings.Split(clean, "\n")
	for _, line := range lines {
		// Strip local request counts ("Last 7d · 403 requests · 18 sessions") before checking for HTTP 401/403.
		stripped := claudeUsageCountsRE.ReplaceAllString(line, "")
		if claudeUsageDeniedRE.MatchString(stripped) {
			return proto.UsageAccount{}, false
		}
	}

	type slot struct {
		minutes int
		model   string
	}
	seen := map[slot]bool{}
	var wins []proto.UsageWindow
	for _, line := range lines {
		m := claudeUsageLineRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		used, err := strconv.ParseFloat(m[3], 64)
		if err != nil {
			continue
		}
		minutes := 5 * 60
		span := 5 * time.Hour
		model := ""
		if m[1] != "session" {
			minutes = 7 * 24 * 60
			span = 7 * 24 * time.Hour
			scope := strings.TrimSpace(m[2])
			if cut, ok := strings.CutSuffix(strings.ToLower(scope), " only"); ok && cut != "" {
				scope = strings.TrimSpace(scope[:len(cut)])
			}
			if scope != "" && !strings.EqualFold(scope, "all models") {
				model = scope
			}
		}
		k := slot{minutes, strings.ToLower(model)}
		if seen[k] {
			continue
		}
		seen[k] = true
		w := proto.UsageWindow{
			Label:         proto.WindowLabel(minutes),
			WindowMinutes: minutes,
			UsedPercent:   clampPercent(used),
			Model:         model,
		}
		if t, ok := parseClaudeResetTime(m[4], now, span); ok {
			u := t.UTC()
			w.ResetsAt = &u
		}
		wins = append(wins, w)
	}
	wins = liveWindows(wins, now)
	if len(wins) == 0 {
		return proto.UsageAccount{}, false
	}
	sort.SliceStable(wins, func(i, j int) bool {
		if wins[i].WindowMinutes != wins[j].WindowMinutes {
			return wins[i].WindowMinutes < wins[j].WindowMinutes
		}
		return wins[i].Model < wins[j].Model
	})
	return proto.UsageAccount{
		Provider:  "claude",
		Name:      "Claude",
		Windows:   wins,
		SampledAt: now.UTC(),
	}, true
}

// parseClaudeResetTime parses `/usage` reset strings such as "Nov 21 at 6:30pm (UTC)", "Oct 9, 2:59pm (America/New_York)",
// "3pm (UTC)", or RFC 3339 timestamps. When no zone is named, it uses now's location.
func parseClaudeResetTime(s string, now time.Time, span time.Duration) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	loc := now.Location()
	if i := strings.LastIndex(s, "("); i >= 0 && strings.HasSuffix(s, ")") {
		if l, err := time.LoadLocation(strings.TrimSpace(s[i+1 : len(s)-1])); err == nil {
			loc = l
		}
		s = strings.TrimSpace(s[:i])
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, "AM", "am"), "PM", "pm")
	ref := now.In(loc)
	latest := ref.Add(span + claudeResetSlack)

	for _, layout := range []string{
		"Jan 2 at 3:04pm", "Jan 2 at 3pm", "Jan 2, 2006 at 3:04pm", "Jan 2, 2006 at 3pm",
		"Jan 2, 3:04pm", "Jan 2, 3pm", "Jan 2, 2006, 3:04pm", "Jan 2, 2006, 3pm",
	} {
		t, err := time.ParseInLocation(layout, s, loc)
		if err != nil {
			continue
		}
		if t.Year() != 0 {
			return t, true
		}
		var past time.Time
		for y := ref.Year() - 1; y <= ref.Year()+1; y++ {
			d := time.Date(y, t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if !d.Before(ref.Add(-24*time.Hour)) && !d.After(latest) {
				return d, true
			}
			if d.Before(ref) {
				past = d
			}
		}
		if !past.IsZero() {
			return past, true
		}
		return time.Time{}, false
	}
	for _, layout := range []string{"3:04pm", "3pm"} {
		c, err := time.ParseInLocation(layout, s, loc)
		if err != nil {
			continue
		}
		for d := 0; d <= 2; d++ {
			t := time.Date(ref.Year(), ref.Month(), ref.Day()+d, c.Hour(), c.Minute(), 0, 0, loc)
			if t.Hour() != c.Hour() || t.Minute() != c.Minute() {
				continue
			}
			if !t.Before(ref) {
				if !t.After(latest) {
					return t, true
				}
				break
			}
		}
		for d := 0; d >= -2; d-- {
			t := time.Date(ref.Year(), ref.Month(), ref.Day()+d, c.Hour(), c.Minute(), 0, 0, loc)
			if t.Hour() == c.Hour() && t.Minute() == c.Minute() && t.Before(ref) {
				return t, true
			}
		}
		return time.Time{}, false
	}
	return time.Time{}, false
}

// dedupeClaudeUsage reconciles CLI readings (`claude-cli:*`) with status-line drops (`claude:*`) and live stream
// readings (`claude-stream:*`, #12855) that describe the same Claude account on this machine. A fresh status-line file
// wins over a CLI probe; once it ages past claudeUsageStatusFresh, a newer CLI reading takes over. When a CLI reading
// and a stream reading describe the same account (even when reset timestamps differ by a few seconds), they are merged
// in the snapshot view so the newer sample's windows win while preserving plan metadata and live per-model windows.
func dedupeClaudeUsage(accounts map[string]proto.UsageNodeAccount, now time.Time) {
	var statusKeys, streamKeys, cliKeys []string
	for k := range accounts {
		switch {
		case isClaudeStatusKey(k):
			statusKeys = append(statusKeys, k)
		case isClaudeStreamKey(k):
			streamKeys = append(streamKeys, k)
		case strings.HasPrefix(k, claudeUsagePrefix):
			cliKeys = append(cliKeys, k)
		}
	}
	if len(cliKeys) == 0 || (len(statusKeys) == 0 && len(streamKeys) == 0) {
		return
	}
	slices.Sort(statusKeys)
	slices.Sort(streamKeys)
	slices.Sort(cliKeys)
	for _, ck := range cliKeys {
		cliAcct, ok := accounts[ck]
		if !ok {
			continue
		}
		for _, sk := range statusKeys {
			stAcct, ok := accounts[sk]
			if !ok {
				continue
			}
			if !sameClaudeAccount(stAcct.UsageAccount, cliAcct.UsageAccount) {
				continue
			}
			if now.Sub(stAcct.SampledAt) <= claudeUsageStatusFresh {
				delete(accounts, ck)
				break
			}
			if cliAcct.SampledAt.After(stAcct.SampledAt) {
				delete(accounts, sk)
			} else {
				delete(accounts, ck)
				break
			}
		}
	}
	for _, ck := range cliKeys {
		cliAcct, ok := accounts[ck]
		if !ok {
			continue
		}
		for _, sk := range streamKeys {
			streamAcct, ok := accounts[sk]
			if !ok {
				continue
			}
			if !sameClaudeAccount(streamAcct.UsageAccount, cliAcct.UsageAccount) {
				continue
			}
			if streamAcct.SampledAt.After(cliAcct.SampledAt) {
				accounts[sk] = mergeClaudeUsageAccount(streamAcct, cliAcct, now)
				delete(accounts, ck)
				break
			}
			cliAcct = mergeClaudeUsageAccount(cliAcct, streamAcct, now)
			accounts[ck] = cliAcct
			delete(accounts, sk)
		}
	}
}

// mergeClaudeUsageAccount combines two readings of the same Claude account, taking primary's sample time and live
// windows while preserving plan metadata and non-overlapping live windows (such as per-model weekly allowances) from
// secondary.
func mergeClaudeUsageAccount(primary, secondary proto.UsageNodeAccount, now time.Time) proto.UsageNodeAccount {
	merged := primary
	if merged.Plan == "" && secondary.Plan != "" {
		merged.Plan = secondary.Plan
		merged.Name = secondary.Name
	}
	type slot struct {
		minutes int
		model   string
	}
	seen := map[slot]bool{}
	wins := make([]proto.UsageWindow, 0, len(primary.Windows)+len(secondary.Windows))
	for _, w := range liveWindows(primary.Windows, now) {
		seen[slot{w.WindowMinutes, strings.ToLower(w.Model)}] = true
		wins = append(wins, w)
	}
	for _, w := range liveWindows(secondary.Windows, now) {
		k := slot{w.WindowMinutes, strings.ToLower(w.Model)}
		if seen[k] {
			continue
		}
		seen[k] = true
		wins = append(wins, w)
	}
	sort.SliceStable(wins, func(i, j int) bool {
		if wins[i].WindowMinutes != wins[j].WindowMinutes {
			return wins[i].WindowMinutes < wins[j].WindowMinutes
		}
		return wins[i].Model < wins[j].Model
	})
	merged.Windows = wins
	return merged
}

// sameClaudeAccount reports whether two Claude readings share at least one plan window whose reset lands within 5s.
func sameClaudeAccount(a, b proto.UsageAccount) bool {
	const resetTolerance = 5 * time.Second
	for _, wa := range a.Windows {
		if wa.Model != "" || wa.ResetsAt == nil {
			continue
		}
		for _, wb := range b.Windows {
			if wb.Model != "" || wb.ResetsAt == nil || wa.WindowMinutes != wb.WindowMinutes {
				continue
			}
			diff := wa.ResetsAt.Sub(*wb.ResetsAt)
			if diff < 0 {
				diff = -diff
			}
			if diff <= resetTolerance {
				return true
			}
		}
	}
	return false
}
