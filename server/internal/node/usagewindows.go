package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Subscription rate-limit windows of the agents on this machine (#2432, docs/server/usage-windows.md): how much of an
// account's 5-hour and weekly allowance is used. Persona never reads login credentials:
//
//   - Codex app-server can return the limits together with the account ID. The node sends only a digest of that ID.
//     When the app-server cannot supply windows, read the newest rollout's token_count event without an account ID.
//   - Claude Code: in Persona's own Claude assistant conversations, stream-json emits a rate_limit_event after each
//     reply (usagewindows_claudestream.go, #12855) with no extra process; in addition, a bounded background poll
//     (usagewindows_claude.go, #12412) invokes the built-in `claude` launcher through the login shell via stream-json
//     `get_usage` (falling back to `-p /usage` when unsupported or when `get_usage` returns no windows) without
//     touching credentials or making a model request. An opt-in status line drop (<node dir>/usage/claude*.json)
//     keeps working and wins when fresh.
//
// The node polls every usageWindowsEvery, keeps the last sample of every source (a source that goes quiet keeps its
// number for three days), and answers usage.windows from that. hub.json machines[].usage_windows false turns it off.

const (
	usageWindowsEvery  = 5 * time.Minute
	usageWindowsFirst  = 20 * time.Second // the hub's node.configure (the off switch) comes first
	usageWindowsMaxAge = 3 * 24 * time.Hour
	usageWindowsTail   = 512 << 10 // how much of a rollout's end is searched for the last token_count
	usageWindowsScan   = 12        // rollouts looked into per Codex home
)

type usageWindowsState struct {
	mu           sync.Mutex
	off          bool // the hub said usage_windows false
	cancel       context.CancelFunc
	at           time.Time
	accounts     map[string]proto.UsageNodeAccount // by source ("codex:<source digest>", "claude:<file>", "claude-cli:<dir digest>", "claude-stream:<launcher+dir digest>")
	claudeProbes map[string]claudeUsageProbe       // by realPath(configDir): last probe time and next reset
}

// setUsageWindows is the hub's word (node.configure usage_windows): whether the node reads the windows at all. Off
// forgets what was read.
func (n *Node) setUsageWindows(on bool) {
	st := &n.usageWin
	st.mu.Lock()
	defer st.mu.Unlock()
	st.off = !on
	if !on {
		if st.cancel != nil {
			st.cancel()
		}
		st.at, st.accounts, st.claudeProbes = time.Time{}, nil, nil
	}
}

// runUsageWindows polls on a schedule so that an answer is never a cold scan for the hub.
func (n *Node) runUsageWindows(ctx context.Context) {
	t := time.NewTimer(usageWindowsFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n.refreshUsageWindows(ctx)
		t.Reset(usageWindowsEvery)
	}
}

// refreshUsageWindows keeps CLI / app-server calls outside the lock, so an RPC can still read local files or return the
// last sample while Codex or Claude checks the account. Disabling polling also cancels and discards an in-flight round.
func (n *Node) refreshUsageWindows(ctx context.Context) {
	st := &n.usageWin
	st.mu.Lock()
	if st.off || ctx.Err() != nil {
		st.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	st.cancel = cancel
	prevProbes := make(map[string]claudeUsageProbe, len(st.claudeProbes))
	for k, v := range st.claudeProbes {
		prevProbes[k] = v
	}
	st.mu.Unlock()
	defer cancel()

	homes := n.usageWindowCodexHomes()
	claudeDirs := n.usageWindowClaudeDirs()
	found := n.collectUsageWindows(time.Now(), homes)
	collectCodexAppUsage(ctx, homes, found)
	probeUpdates := n.collectClaudeCLIUsage(ctx, claudeDirs, prevProbes, found)

	st.mu.Lock()
	defer st.mu.Unlock()
	st.cancel = nil
	if ctx.Err() == nil {
		st.at = time.Now()
		if len(probeUpdates) > 0 {
			if st.claudeProbes == nil {
				st.claudeProbes = map[string]claudeUsageProbe{}
			}
			for k, v := range probeUpdates {
				st.claudeProbes[k] = v
			}
		}
		st.accounts = mergeUsage(st.accounts, found, st.at)
	}
}

// usageSnapshot is usage.windows: the last poll, or local files until the first poll finishes.
func (n *Node) usageSnapshot() proto.UsageSnapshot {
	st := &n.usageWin
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.off {
		return proto.UsageSnapshot{Accounts: []proto.UsageNodeAccount{}}
	}
	if st.accounts == nil {
		st.at = time.Now()
		st.accounts = n.collectUsageWindows(st.at, n.usageWindowCodexHomes())
	}
	view := make(map[string]proto.UsageNodeAccount, len(st.accounts))
	for k, a := range st.accounts {
		view[k] = a
	}
	dedupeClaudeUsage(view, claudeUsageNow().UTC())
	keys := make([]string, 0, len(view))
	for k := range view {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := proto.UsageSnapshot{Enabled: true, CollectedAt: st.at, Accounts: []proto.UsageNodeAccount{}}
	for _, k := range keys {
		out.Accounts = append(out.Accounts, view[k])
	}
	return out
}

// mergeUsage lays this poll over the kept samples. An old rollout does not displace a newer verified Codex
// snapshot during a failed refresh. A quiet source keeps its last sample for usageWindowsMaxAge.
func mergeUsage(kept, found map[string]proto.UsageNodeAccount, now time.Time) map[string]proto.UsageNodeAccount {
	out := map[string]proto.UsageNodeAccount{}
	for k, a := range kept {
		if now.Sub(a.SampledAt) <= usageWindowsMaxAge {
			out[k] = a
		}
	}
	for k, a := range found {
		if previous := out[k]; previous.AccountKey != "" && a.AccountKey == "" && !a.SampledAt.After(previous.SampledAt) {
			continue
		}
		out[k] = a
	}
	return out
}

// runtimeUsageWindows stores one local runtime's subscription snapshot beside the file and app-server sources. The
// runtime id, rather than a process instance, is the source key so a companion restart replaces its previous report.
func (n *Node) runtimeUsageWindows(h *runtimeConn, params json.RawMessage, now time.Time) {
	if h.isDaemon() {
		return
	}
	var a proto.UsageAccount
	if json.Unmarshal(params, &a) != nil {
		return
	}
	var required struct {
		AccountKey string `json:"account_key"`
		Windows    []struct {
			UsedPercent *float64 `json:"used_percent"`
		} `json:"windows"`
	}
	// A missing or null number becomes a float64 zero, so check its presence at the raw event boundary too.
	if json.Unmarshal(params, &required) != nil || len(required.Windows) != len(a.Windows) {
		return
	}
	accountKey := strings.TrimSpace(required.AccountKey)
	a.Provider = strings.TrimSpace(a.Provider)
	a.Name = strings.TrimSpace(a.Name)
	a.Plan = strings.TrimSpace(a.Plan)
	if a.Provider == "" || len(a.Provider) > 64 || a.Name == "" || len(a.Name) > 128 || len(a.Plan) > 128 || len(accountKey) > 128 ||
		a.SampledAt.IsZero() || now.Sub(a.SampledAt) > usageWindowsMaxAge || a.SampledAt.After(now.Add(5*time.Minute)) ||
		a.Windows == nil || len(a.Windows) > 32 {
		return
	}
	windows := make([]proto.UsageWindow, 0, len(a.Windows))
	for i, w := range a.Windows {
		if required.Windows[i].UsedPercent == nil || w.WindowMinutes <= 0 || len(w.Model) > 256 {
			return
		}
		w.Label = proto.WindowLabel(w.WindowMinutes)
		w.UsedPercent = clampPercent(w.UsedPercent)
		if w.ResetsAt != nil {
			t := w.ResetsAt.UTC()
			w.ResetsAt = &t
		}
		windows = append(windows, w)
	}
	a.ReadingSource = "" // A runtime event is independent of this node's rollout reader.
	a.Windows = liveWindows(windows, now)
	a.SampledAt = a.SampledAt.UTC()
	enrichLegacyAgyUsage(&a, &accountKey, now)

	h.mu.Lock()
	source := "runtime:" + h.hello.Runtime
	h.mu.Unlock()
	st := &n.usageWin
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.off {
		return
	}
	if st.accounts == nil {
		st.accounts = map[string]proto.UsageNodeAccount{}
	}
	if previous, ok := st.accounts[source]; ok && a.SampledAt.Before(previous.SampledAt) {
		return
	}
	if len(a.Windows) == 0 {
		delete(st.accounts, source)
	} else {
		st.accounts[source] = proto.UsageNodeAccount{UsageAccount: a, AccountKey: accountKey, SourceKey: source}
	}
	st.at = now
}

// collectUsageWindows reads the local files of every source once.
func (n *Node) collectUsageWindows(now time.Time, homes []string) map[string]proto.UsageNodeAccount {
	out := map[string]proto.UsageNodeAccount{}
	for _, home := range homes {
		if a, ok := codexUsageAccount(home, now); ok {
			source := codexUsageKey(realPath(home))
			out["codex:"+source] = proto.UsageNodeAccount{UsageAccount: a, SourceKey: source}
		}
	}
	files, _ := filepath.Glob(filepath.Join(n.dir, "usage", "claude*.json"))
	for _, p := range files {
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() || st.Size() > 1<<20 || now.Sub(st.ModTime()) > usageWindowsMaxAge {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if a, ok := parseClaudeStatusLimits(data, st.ModTime(), now); ok {
			out["claude:"+filepath.Base(p)] = proto.UsageNodeAccount{UsageAccount: a}
		}
	}
	return out
}

// The registry may list the default home again through an alias. Poll one source for each physical home, using
// the first spelling to start the app-server with the same CODEX_HOME as the existing local install.
func (n *Node) usageWindowCodexHomes() []string {
	seen := map[string]bool{}
	return slices.DeleteFunc(n.usageCodexHomes(), func(home string) bool {
		path := realPath(home)
		duplicate := seen[path]
		seen[path] = true
		return duplicate
	})
}

// usageCodexHomes are the default and registered Codex homes.
func (n *Node) usageCodexHomes() []string {
	homes := []string{n.codexHome()}
	if r := n.codices; r != nil {
		r.mu.Lock()
		for _, h := range r.config.Homes {
			if h != "" && !containsString(homes, h) {
				homes = append(homes, h)
			}
		}
		r.mu.Unlock()
	}
	return homes
}

func codexUsageKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// codexUsageAccount is the account a Codex home is signed in to, as its newest rollout last saw it.
func codexUsageAccount(home string, now time.Time) (proto.UsageAccount, bool) {
	for i, r := range rollouts(home) {
		if i >= usageWindowsScan || now.Sub(r.mtime) > usageWindowsMaxAge {
			break
		}
		s, ok := lastCodexSample(r.path)
		if !ok || now.Sub(s.at) > usageWindowsMaxAge {
			continue
		}
		wins := liveWindows(s.windows, now)
		if len(wins) == 0 {
			continue
		}
		return proto.UsageAccount{Provider: "codex", Name: codexAccountName(s.plan), Plan: s.plan,
			Windows: wins, SampledAt: s.at, ReadingSource: proto.UsageReadingRollout}, true
	}
	return proto.UsageAccount{}, false
}

func codexAccountName(plan string) string {
	if plan == "" {
		return "Codex"
	}
	return "Codex " + strings.ToUpper(plan[:1]) + plan[1:]
}

// liveWindows drops the windows that have already reset (their number says nothing about the new one) and orders
// the rest shortest first.
func liveWindows(in []proto.UsageWindow, now time.Time) []proto.UsageWindow {
	var out []proto.UsageWindow
	for _, w := range in {
		if w.ResetsAt != nil && !w.ResetsAt.After(now) {
			continue
		}
		out = append(out, w)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].WindowMinutes < out[j].WindowMinutes })
	return out
}

// codexLimitSample is one token_count event's rate_limits.
type codexLimitSample struct {
	at      time.Time
	plan    string
	windows []proto.UsageWindow
}

// lastCodexSample is the newest token_count rate_limits in the tail of a rollout.
func lastCodexSample(path string) (codexLimitSample, bool) {
	f, err := os.Open(path)
	if err != nil {
		return codexLimitSample{}, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return codexLimitSample{}, false
	}
	off := int64(0)
	if st.Size() > usageWindowsTail {
		off = st.Size() - usageWindowsTail
	}
	buf := make([]byte, st.Size()-off)
	got, err := f.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return codexLimitSample{}, false
	}
	buf = buf[:got]
	if off > 0 { // the first line of the tail is cut
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			return codexLimitSample{}, false
		}
		buf = buf[i+1:]
	}
	for end := len(buf); end > 0; {
		start := bytes.LastIndexByte(buf[:end], '\n') + 1
		if s, ok := parseCodexRateLimits(buf[start:end]); ok {
			return s, true
		}
		end = start - 1
	}
	return codexLimitSample{}, false
}

type codexLimitWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowMinutes *int     `json:"window_minutes"`
	ResetsAt      *int64   `json:"resets_at"`
}

// parseCodexRateLimits reads one rollout line; ok only for a token_count event of Codex's own limit that carries at
// least one window with a length.
func parseCodexRateLimits(line []byte) (codexLimitSample, bool) {
	if !bytes.Contains(line, []byte(`"rate_limits"`)) {
		return codexLimitSample{}, false
	}
	var l struct {
		Timestamp string `json:"timestamp"`
		Payload   struct {
			Type       string `json:"type"`
			RateLimits *struct {
				LimitID   *string           `json:"limit_id"`
				PlanType  *string           `json:"plan_type"`
				Primary   *codexLimitWindow `json:"primary"`
				Secondary *codexLimitWindow `json:"secondary"`
			} `json:"rate_limits"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &l) != nil || l.Payload.Type != "token_count" || l.Payload.RateLimits == nil {
		return codexLimitSample{}, false
	}
	rl := l.Payload.RateLimits
	if rl.LimitID != nil && *rl.LimitID != "" && *rl.LimitID != "codex" { // a model's own limit ("premium")
		return codexLimitSample{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil {
		return codexLimitSample{}, false
	}
	s := codexLimitSample{at: at}
	if rl.PlanType != nil {
		s.plan = strings.ToLower(strings.TrimSpace(*rl.PlanType))
	}
	for _, w := range []*codexLimitWindow{rl.Primary, rl.Secondary} {
		if w != nil {
			s.windows = appendCodexWindow(s.windows, w.UsedPercent, w.WindowMinutes, w.ResetsAt)
		}
	}
	return s, len(s.windows) > 0
}

func appendCodexWindow(windows []proto.UsageWindow, usedPercent *float64, minutes *int, resetsAt *int64) []proto.UsageWindow {
	if usedPercent == nil || minutes == nil || *minutes <= 0 {
		return windows
	}
	win := proto.UsageWindow{Label: proto.WindowLabel(*minutes), WindowMinutes: *minutes, UsedPercent: clampPercent(*usedPercent)}
	if resetsAt != nil && *resetsAt > 0 {
		t := time.Unix(*resetsAt, 0).UTC()
		win.ResetsAt = &t
	}
	return append(windows, win)
}

func clampPercent(p float64) float64 {
	if math.IsNaN(p) || p < 0 {
		return 0
	}
	return math.Min(p, 100)
}

// claudeStatusWindows are the rate_limits keys of Claude Code's status line input and the length of each window.
var claudeStatusWindows = []struct {
	key     string
	minutes int
}{{"five_hour", 5 * 60}, {"seven_day", 7 * 24 * 60}}

// parseClaudeStatusLimits reads what a status line wrapper dropped: Claude Code's status line JSON (or just its
// rate_limits field) with five_hour / seven_day {used_percentage, resets_at}. mod is when it was written. The field
// is described in Claude Code's changelog ("rate_limits … 5-hour and 7-day windows with used_percentage and
// resets_at"); resets_at is read as epoch seconds (milliseconds and RFC 3339 are accepted too).
func parseClaudeStatusLimits(data []byte, mod, now time.Time) (proto.UsageAccount, bool) {
	var in struct {
		RateLimits map[string]json.RawMessage `json:"rate_limits"`
	}
	if json.Unmarshal(data, &in) != nil {
		return proto.UsageAccount{}, false
	}
	var wins []proto.UsageWindow
	for _, def := range claudeStatusWindows {
		raw, ok := in.RateLimits[def.key]
		if !ok {
			continue
		}
		var w struct {
			UsedPercentage *float64        `json:"used_percentage"`
			ResetsAt       json.RawMessage `json:"resets_at"`
		}
		if json.Unmarshal(raw, &w) != nil || w.UsedPercentage == nil {
			continue
		}
		win := proto.UsageWindow{Label: proto.WindowLabel(def.minutes), WindowMinutes: def.minutes, UsedPercent: clampPercent(*w.UsedPercentage)}
		if t, ok := transcript.ParseResetsAt(w.ResetsAt); ok {
			win.ResetsAt = &t
		}
		wins = append(wins, win)
	}
	wins = liveWindows(wins, now)
	if len(wins) == 0 {
		return proto.UsageAccount{}, false
	}
	return proto.UsageAccount{Provider: "claude", Name: "Claude", Windows: wins, SampledAt: mod.UTC()}, true
}
