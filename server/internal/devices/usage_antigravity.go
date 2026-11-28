package devices

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const antigravityUsageProvider = "antigravity"

func canonicalUsageProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "agy" {
		return antigravityUsageProvider
	}
	return provider
}

// antigravityAccountIdentity extracts an explicit signed-in Antigravity user identity when present, returning ""
// for generic placeholder labels ("Antigravity", "Antigravity · Gemini", numbered suffixes, etc.).
func antigravityAccountIdentity(a proto.UsageAccount) string {
	if canonicalUsageProvider(a.Provider) != antigravityUsageProvider {
		return ""
	}
	head, _, _ := strings.Cut(a.Name, "·")
	head = strings.ToLower(strings.TrimSpace(head))
	if head == "" || head == antigravityUsageProvider || head == "agy" {
		return ""
	}
	if i := strings.LastIndexByte(head, ' '); i >= 0 {
		base := strings.TrimSpace(head[:i])
		if n, err := strconv.Atoi(head[i+1:]); err == nil && n > 1 && (base == antigravityUsageProvider || base == "agy") {
			return ""
		}
	}
	if strings.Contains(head, "@") {
		return "email:" + head
	}
	if a.Local && len(a.Sources) == 0 {
		return "name:" + head
	}
	return ""
}

// sameProviderAccountConflict reports whether two samples of the same provider carry distinct explicit account
// identities (either distinct cryptographic node AccountKeys, or distinct signed-in Antigravity emails/names of the
// same kind). An unnamed/generic sample never conflicts here so rolling upgrades and generic external pools still reconcile.
func sameProviderAccountConflict(a, b proto.UsageAccount) bool {
	if a.CodexAccountKey != "" && b.CodexAccountKey != "" && a.CodexAccountKey != b.CodexAccountKey {
		return true
	}
	idA, idB := antigravityAccountIdentity(a), antigravityAccountIdentity(b)
	if idA == "" || idB == "" || idA == idB {
		return false
	}
	kindA, _, _ := strings.Cut(idA, ":")
	kindB, _, _ := strings.Cut(idB, ":")
	return kindA == kindB
}

// The companion sends model groups in one account. An external source can send the same groups as separate
// accounts whose plan windows have no Model field.
func antigravityQuotaScope(a proto.UsageAccount, w proto.UsageWindow) string {
	if w.Model != "" {
		return antigravityModelScope(w.Model)
	}
	_, suffix, _ := strings.Cut(a.Name, "·")
	// The merged list numbers same-name accounts before the display response is assembled.
	suffix = strings.TrimSpace(suffix)
	if i := strings.LastIndexByte(suffix, ' '); i >= 0 {
		if n, err := strconv.Atoi(suffix[i+1:]); err == nil && n > 1 {
			suffix = suffix[:i]
		}
	}
	for _, name := range []string{w.Group, suffix} {
		if scope := antigravityModelScope(name); scope == "gemini" || scope == "claude/gpt" {
			return scope
		}
	}
	return ""
}

func antigravityModelScope(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if left, right, ok := strings.Cut(name, "/"); ok && strings.TrimSpace(left) == "claude" && strings.TrimSpace(right) == "gpt" {
		return "claude/gpt"
	}
	switch name {
	case "gemini models":
		return "gemini"
	case "claude and gpt models", "claude gpt", "claude_gpt":
		return "claude/gpt"
	}
	return name
}

type antigravityQuotaKey struct {
	scope   string
	minutes int
	reset   int64
}

func antigravityWindowKey(a proto.UsageAccount, w proto.UsageWindow, now time.Time) (antigravityQuotaKey, bool) {
	if w.ResetsAt == nil || !w.ResetsAt.After(now) {
		return antigravityQuotaKey{}, false
	}
	return antigravityQuotaKey{antigravityQuotaScope(a, w), w.WindowMinutes, w.ResetsAt.Unix()}, true
}

type usageReading struct {
	at    time.Time
	stale bool
}

func usageAccountReading(a proto.UsageAccount, now time.Time) usageReading {
	return usageReading{a.SampledAt, a.Stale || a.StaleAt != nil && !a.StaleAt.After(now)}
}

func (a usageReading) preferredTo(b usageReading) bool {
	if a.stale != b.stale {
		return !a.stale
	}
	return a.at.After(b.at)
}

type antigravityAccountScope struct {
	account int
	scope   string
}

// A negative peer means that exact matches identified more than one account for this scope.
type antigravityPeers map[antigravityAccountScope]int

func (peers antigravityPeers) record(group antigravityAccountScope, peer int) {
	if previous, ok := peers[group]; ok && previous != peer {
		peer = -1
	}
	peers[group] = peer
}

func antigravityPairs(accounts []proto.UsageAccount, now time.Time) antigravityPeers {
	externals := map[antigravityQuotaKey][]int{}
	for i, a := range accounts {
		if a.Provider != antigravityUsageProvider || a.Local || len(a.Sources) == 0 {
			continue
		}
		for _, w := range a.Windows {
			if key, ok := antigravityWindowKey(a, w, now); ok {
				externals[key] = append(externals[key], i)
			}
		}
	}
	local, external := antigravityPeers{}, antigravityPeers{}
	for i, a := range accounts {
		if a.Provider != antigravityUsageProvider || !a.Local || len(a.Sources) != 0 {
			continue
		}
		for _, w := range a.Windows {
			if key, ok := antigravityWindowKey(a, w, now); ok {
				for _, peer := range externals[key] {
					if sameProviderAccountConflict(a, accounts[peer]) {
						continue
					}
					local.record(antigravityAccountScope{i, key.scope}, peer)
					external.record(antigravityAccountScope{peer, key.scope}, i)
				}
			}
		}
	}
	for group, peer := range local {
		if peer < 0 || external[antigravityAccountScope{peer, group.scope}] != group.account {
			delete(local, group)
		}
	}
	return local
}

type antigravityWindowRef struct {
	account, window int
}

// Collect exact matches first. A disagreement in any comparable timed slot disqualifies
// the whole scope from sibling inference, even if we already found a matching timed slot.
func antigravityFoldTargets(accounts []proto.UsageAccount, now time.Time) map[antigravityWindowRef]antigravityWindowRef {
	targets := map[antigravityWindowRef]antigravityWindowRef{}
	for group, peer := range antigravityPairs(accounts, now) {
		local, remote := accounts[group.account], accounts[peer]
		slots := map[int][]int{}
		for j, w := range remote.Windows {
			if antigravityQuotaScope(remote, w) == group.scope {
				slots[w.WindowMinutes] = append(slots[w.WindowMinutes], j)
			}
		}
		foldSiblings := group.scope != ""
		idle := map[int]int{} // local window index -> external index (-1 for a missing slot)
		for i, w := range local.Windows {
			if antigravityQuotaScope(local, w) != group.scope {
				continue
			}
			slot := slots[w.WindowMinutes]
			if w.ResetsAt == nil {
				switch {
				case len(slot) == 0:
					idle[i] = -1
				case len(slot) == 1 && remote.Windows[slot[0]].ResetsAt == nil:
					idle[i] = slot[0]
				}
				continue
			}
			if !w.ResetsAt.After(now) {
				continue
			}
			match, matches := -1, 0
			for _, j := range slot {
				reset := remote.Windows[j].ResetsAt
				if reset == nil || !reset.After(now) {
					continue
				}
				if reset.Unix() != w.ResetsAt.Unix() {
					foldSiblings = false
				} else {
					match = j
					matches++
				}
			}
			if matches == 1 {
				targets[antigravityWindowRef{group.account, i}] = antigravityWindowRef{peer, match}
			} else if matches > 1 {
				foldSiblings = false
			}
		}
		if foldSiblings {
			for i, j := range idle {
				targets[antigravityWindowRef{group.account, i}] = antigravityWindowRef{peer, j}
			}
		}
	}
	return targets
}

// Reconcile the display response on copies so that the scheduler still sees the gateway inventory.
func reconcileAntigravityUsage(accounts []proto.UsageAccount, now time.Time) []proto.UsageAccount {
	targets := antigravityFoldTargets(accounts, now)
	if len(targets) == 0 {
		return accounts
	}
	out := slices.Clone(accounts)
	for i, a := range accounts {
		if !a.Local || a.Provider != antigravityUsageProvider || len(a.Sources) != 0 {
			continue
		}
		reading := usageAccountReading(a, now)
		kept := make([]proto.UsageWindow, 0, len(a.Windows))
		for j, w := range a.Windows {
			to, ok := targets[antigravityWindowRef{i, j}]
			if !ok {
				kept = append(kept, w)
				continue
			}
			remote := &out[to.account]
			if !remote.Local {
				remote.Local = true
				remote.Windows = slices.Clone(remote.Windows)
			}
			remote.Machines = uniqueSorted(slices.Concat(remote.Machines, a.Machines))
			if to.window < 0 {
				remote.Windows = append(remote.Windows, w)
			} else if reading.preferredTo(usageAccountReading(accounts[to.account], now)) {
				remote.Windows[to.window].UsedPercent = w.UsedPercent
			}
			if reading.preferredTo(usageAccountReading(*remote, now)) {
				remote.SampledAt, remote.StaleAt, remote.Stale, remote.Problem = a.SampledAt, a.StaleAt, reading.stale, a.Problem
			}
		}
		if len(kept) != len(a.Windows) {
			out[i].Windows = kept
		}
	}
	for i, a := range out {
		if !a.Pass && len(a.Windows) > len(accounts[i].Windows) {
			slices.SortStableFunc(a.Windows, func(a, b proto.UsageWindow) int {
				if n := cmp.Compare(a.WindowMinutes, b.WindowMinutes); n != 0 {
					return n
				}
				return strings.Compare(a.Model, b.Model)
			})
		}
	}
	out = slices.DeleteFunc(out, func(a proto.UsageAccount) bool {
		return a.Provider == antigravityUsageProvider && a.Local && len(a.Sources) == 0 && len(a.Windows) == 0 && !isBalanceSample(a)
	})
	sortUsageAccounts(out)
	return out
}
