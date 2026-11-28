package assistant

import (
	"cmp"
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/proto"
)

// Pace-based quota headroom of a tier's candidate (docs/assistant/model-profiles.md "Choosing a candidate",
// docs/server/usage-windows.md "UI (web)" pace). The web's pace (web/src/lib/usage-pace.ts) compares a window's use
// with an even spread: expected = elapsed / window length. Dispatch uses the same elapsed share with two additions:
//
//   - a start share, so a window that has just reset lets some work start instead of none (5 h: one hour's share,
//     0.2; 7 d: one day's share, 1/7);
//   - a reserve kept for the owner's own conversations (5 h: 15 %, 7 d: 10 %).
//
// Per account and window: headroom (percentage points) = max(elapsed share, start share) × (1 − reserve) × 100 − used.
// An account's headroom is the smallest of its windows that apply to the candidate's model, floored at zero; the
// candidate's headroom H is the sum over the deduplicated accounts of its launcher's usage provider, in units of one
// account-window (0.17 = 17 points). What was not used earlier stays available later, because the elapsed share only
// grows. A stale reading, or none at all, is unknown, never full and never empty.

// paceWindow is one window length's start share and reserve.
type paceWindow struct {
	start, reserve float64
}

var (
	paceShort = paceWindow{start: 0.2, reserve: 0.15}     // the 5-hour window
	paceLong  = paceWindow{start: 1.0 / 7, reserve: 0.10} // the weekly window
)

// paceFor is the parameters of a window of this length: a window under a day is paced like the 5-hour one, a longer
// one like the weekly one.
func paceFor(minutes int) paceWindow {
	if minutes < 24*60 {
		return paceShort
	}
	return paceLong
}

// usageWindowMinutes is a window's length from its label ("5h", "7d"), falling back to window_minutes, as the web's
// windowMinutes does.
func usageWindowMinutes(w proto.UsageWindow) int {
	label := strings.TrimSpace(strings.ToLower(w.Label))
	if n := len(label); n > 1 {
		var v int
		ok := true
		for _, r := range label[:n-1] {
			if r < '0' || r > '9' {
				ok = false
				break
			}
			v = v*10 + int(r-'0')
		}
		if ok && v > 0 {
			switch label[n-1] {
			case 'm':
				return v
			case 'h':
				return v * 60
			case 'd':
				return v * 24 * 60
			}
		}
	}
	return w.WindowMinutes
}

// windowHeadroom is one window's headroom in percentage points and when it turns positive again; ok is false when the
// window cannot be judged (no length, no reset time on a window in use, or a reset time that has passed or lies beyond
// one window). A window with nothing used and no reset time has not started its clock (Antigravity's per-model pools
// report no reset until their first use, #17011): it is judged as one that has just reset, its start share.
func windowHeadroom(w proto.UsageWindow, now time.Time) (pp float64, recovers time.Time, ok bool) {
	minutes := usageWindowMinutes(w)
	if minutes <= 0 {
		return 0, time.Time{}, false
	}
	if w.ResetsAt == nil {
		if w.UsedPercent != 0 {
			return 0, time.Time{}, false
		}
		p := paceFor(minutes)
		return p.start * (1 - p.reserve) * 100, now, true
	}
	length := time.Duration(minutes) * time.Minute
	left := w.ResetsAt.Sub(now)
	if left <= 0 || left > length {
		return 0, time.Time{}, false
	}
	p := paceFor(minutes)
	elapsed := float64(length-left) / float64(length)
	share := max(elapsed, p.start)
	allowed := (1 - p.reserve) * 100
	pp = share*allowed - w.UsedPercent
	if pp > 0 {
		return pp, now, true
	}
	// The window turns positive once the elapsed share needs no more than used; past the end, at its reset.
	need := w.UsedPercent / allowed
	start := w.ResetsAt.Add(-length)
	recovers = *w.ResetsAt
	if need < 1 {
		if at := start.Add(time.Duration(need * float64(length))); at.Before(recovers) {
			recovers = at
		}
	}
	return pp, recovers, true
}

// windowForModel is whether window w measures model's use: a plan window always does; a per-model window
// (proto.UsageWindow.Model, such as one pool of a provider's models) only when the candidate's model belongs to it.
// Without a model the per-model windows cannot be matched and are left out.
func windowForModel(w proto.UsageWindow, model string) bool {
	if w.Model == "" {
		return true
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	named := strings.ToLower(strings.TrimSpace(w.Model))
	if named == model || strings.Contains(model, named) || strings.Contains(named, model) {
		return true
	}
	// A window named for a group of models ("Alpha and Beta models") matches a model id that carries one of its
	// family words ("alpha-2-large").
	for _, word := range strings.FieldsFunc(named, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(word) < 3 || modelGroupFiller[word] {
			continue
		}
		if strings.Contains(model, word) {
			return true
		}
	}
	return false
}

// modelGroupFiller are the words of a group window's name that name no model family.
var modelGroupFiller = map[string]bool{"and": true, "models": true, "model": true, "the": true, "all": true, "other": true, "others": true}

// candidateHeadroom is a candidate's pace-based headroom (the contract's dispatch headroom).
type candidateHeadroom struct {
	// Known: at least one fresh account of the provider had a window that applies. Unknown headroom is neither full
	// nor empty: it adds nothing to the score and excludes nothing.
	Known bool `json:"known"`
	// H is the sum of the accounts' headroom in account-windows (0.17 = 17 percentage points); meaningful with Known.
	H float64 `json:"h"`
	// Percent is H in percentage points, as the candidates route shows it.
	Percent float64 `json:"percent"`
	// Stale: the provider had accounts, but every one of them was out of date.
	Stale bool `json:"stale"`
	// Accounts is how many fresh accounts were counted.
	Accounts int `json:"accounts,omitempty"`
	// RecoversAt is, when H ≤ 0, the earliest time one counted account has headroom again; nil otherwise.
	RecoversAt *time.Time `json:"recovers_at,omitempty"`
}

// canonicalHeadroomProvider is a provider id as the merged usage accounts name it (devices canonicalUsageProvider):
// lower case, with the Antigravity launcher's short name folded into the provider's.
func canonicalHeadroomProvider(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "agy" {
		return "antigravity"
	}
	return p
}

// accountStale is the client rule of docs/server/usage-windows.md: Stale, or past StaleAt.
func accountStale(a proto.UsageAccount, now time.Time) bool {
	return a.Stale || a.StaleAt != nil && !now.Before(*a.StaleAt)
}

// headroomOf is provider's pace-based headroom for model over the merged accounts at now. Pure. Accounts are
// deduplicated by their row id (the merge already folds one account seen by several sources into one row).
func headroomOf(accounts []proto.UsageAccount, provider, model string, now time.Time) candidateHeadroom {
	provider = canonicalHeadroomProvider(provider)
	var out candidateHeadroom
	if provider == "" {
		return out
	}
	seen := map[string]bool{}
	matched, stale := 0, 0
	var recovers *time.Time
	for _, a := range accounts {
		if canonicalHeadroomProvider(a.Provider) != provider || len(a.Windows) == 0 {
			continue
		}
		if key := cmp.Or(a.Ref, a.ID); key != "" {
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		matched++
		if accountStale(a, now) {
			stale++
			continue
		}
		pp, at, ok := accountHeadroom(a, model, now)
		if !ok {
			continue
		}
		out.Known = true
		out.Accounts++
		if pp > 0 {
			out.H += pp / 100
			continue
		}
		if recovers == nil || at.Before(*recovers) {
			t := at
			recovers = &t
		}
	}
	out.Stale = matched > 0 && stale == matched
	out.Percent = out.H * 100
	if out.Known && out.H <= 0 {
		out.RecoversAt = recovers
	}
	return out
}

// accountHeadroom is one account's headroom: the smallest of its windows that apply to model, with the time the
// binding windows have all turned positive; false when none applies or can be judged.
func accountHeadroom(a proto.UsageAccount, model string, now time.Time) (float64, time.Time, bool) {
	var (
		least    float64
		recovers time.Time
		found    bool
	)
	for _, w := range a.Windows {
		if !windowForModel(w, model) {
			continue
		}
		pp, at, ok := windowHeadroom(w, now)
		if !ok {
			continue
		}
		if !found || pp < least {
			least = pp
		}
		if pp <= 0 && at.After(recovers) {
			recovers = at
		}
		found = true
	}
	return least, recovers, found
}

// usageAccounts is the merged usage accounts; nil without a fleet.
func (s *Service) usageAccounts(ctx context.Context) []proto.UsageAccount {
	if s.devs == nil {
		return nil
	}
	return s.devs.UsageAccounts(ctx)
}

// candidateHeadroomAt is the headroom of launcher + model on machine, given the accounts read once for a dispatch. The
// usage provider is the launcher's (docs/assistant/model-profiles.md "Choosing a candidate"): a launcher in an
// admission group with a gateway quota_pool reads that pool's provider, else an alias of a channel that declares its
// quota source (execution_channels.go) the channel's, the model counted under the pool the channel names for it; any
// other the agent it runs (launcherProvider: claude, codex, agy → antigravity); "" is unknown.
//
// A launcher with a gateway quota_pool counts only that gateway's accounts (poolAccounts), never a computer's own
// subscription of the same provider (#17011). When the usage windows say nothing fresh about the provider (a gateway
// lists accounts whose numbers carry no time window, so they never become usage rows), the account-pool view that
// already decides detours answers instead: the pool's inventory for a quota_pool launcher, the provider's capacity
// otherwise (capacityHeadroom).
func (s *Service) candidateHeadroomAt(ctx context.Context, accounts []proto.UsageAccount, machine, launcher, model string, now time.Time) candidateHeadroom {
	pool, pooled := s.gatewayPool(ctx, launcher)
	provider := canonicalHeadroomProvider(pool.Provider)
	if !pooled {
		provider = canonicalHeadroomProvider(s.launcherProvider(ctx, machine, launcher))
	}
	if provider == "" {
		return candidateHeadroom{}
	}
	if pooled {
		accounts = poolAccounts(accounts, pool.Source)
		model = s.channelPoolModel(ctx, launcher, model)
	}
	h := headroomOf(accounts, provider, model, now)
	if h.Known || h.Stale || s.devs == nil {
		return h
	}
	if pooled {
		return inventoryHeadroom(s.devs.UsageGatewayAccounts(), pool, now)
	}
	return capacityHeadroom(s.capacitySnapshot(ctx), provider)
}

// poolAccounts keeps the usage rows gateway source reported: their gateway reference or origin is scoped by the
// source's id ("<source>:<account>", "<source>#<n>").
func poolAccounts(accounts []proto.UsageAccount, source string) []proto.UsageAccount {
	var out []proto.UsageAccount
	for _, a := range accounts {
		if strings.HasPrefix(a.Ref, source+":") || strings.HasPrefix(a.Origin, source+":") || strings.HasPrefix(a.Origin, source+"#") {
			out = append(out, a)
		}
	}
	return out
}

// noReadingHeadroom is what one schedulable account with no window reading counts: a window that has just reset, the
// short window's start share (0.17).
var noReadingHeadroom = paceShort.start * (1 - paceShort.reserve)

// inventoryHeadroom is a gateway pool's headroom from its account list when its accounts carry no window numbers.
// Pure. Each fresh, enabled account of the pool's provider counts noReadingHeadroom when the gateway can schedule it
// now, and nothing when it cannot (cooling down, exhausted), recovering at its cooldown end or nearest window reset.
// Unknown when the pool has no such account or every one is stale.
func inventoryHeadroom(inv proto.GatewayAccounts, pool GatewayQuotaPool, now time.Time) candidateHeadroom {
	var out candidateHeadroom
	matched, stale := 0, 0
	var recovers *time.Time
	for _, a := range inv.Accounts {
		if a.Source != pool.Source || a.Provider != pool.Provider || a.Status == "disabled" {
			continue
		}
		matched++
		if a.Stale {
			stale++
			continue
		}
		out.Known = true
		out.Accounts++
		cooling := a.CooldownUntil != nil && a.CooldownUntil.After(now)
		if a.Schedulable && !cooling {
			out.H += noReadingHeadroom
			continue
		}
		at := inventoryRecovery(a, cooling, now)
		if at != nil && (recovers == nil || at.Before(*recovers)) {
			recovers = at
		}
	}
	out.Stale = matched > 0 && stale == matched
	out.Percent = out.H * 100
	if out.Known && out.H <= 0 {
		out.RecoversAt = recovers
	}
	return out
}

// inventoryRecovery is when an account the gateway cannot schedule may take work again: its cooldown end, else its
// nearest future window reset; nil when no time is known.
func inventoryRecovery(a proto.GatewayAccount, cooling bool, now time.Time) *time.Time {
	if cooling {
		t := *a.CooldownUntil
		return &t
	}
	var soonest *time.Time
	for _, w := range a.Windows {
		if w.ResetsAt != nil && w.ResetsAt.After(now) && (soonest == nil || w.ResetsAt.Before(*soonest)) {
			t := *w.ResetsAt
			soonest = &t
		}
	}
	return soonest
}

// capacityHeadroom is a provider's headroom from the account-pool capacity view when no window says more. Pure.
// Fresh and saturated: known, none, recovering at the nearest recovery. Fresh with schedulable accounts: each counts
// noReadingHeadroom. Stale or not known: unknown.
func capacityHeadroom(snap proto.CapacitySnapshot, provider string) candidateHeadroom {
	p, ok := snap.Provider(provider)
	if !ok || p.Accounts == 0 {
		return candidateHeadroom{}
	}
	if p.Stale {
		return candidateHeadroom{Stale: true}
	}
	out := candidateHeadroom{Known: true, Accounts: p.Accounts, H: float64(p.Available) * noReadingHeadroom}
	out.Percent = out.H * 100
	if out.H <= 0 && p.EarliestRecovery != nil {
		t := *p.EarliestRecovery
		out.RecoversAt = &t
	}
	return out
}
