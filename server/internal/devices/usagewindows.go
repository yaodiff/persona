package devices

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Subscription rate-limit windows across machines (#2432, docs/server/usage-windows.md). Every node collects its agents'
// usage and answers usage.windows; the manager, which connects the machines (docs/server/machines.md "Who connects to computers"), asks
// the ones that are online (at most every usageHubFresh), remembers each machine's last answer, and merges them into
// one list per account: the same account on two computers shows once, with the newest numbers. Nothing here is
// stored: a restarted manager asks again, and an offline machine's last answer is only kept in memory until it is
// older than usageHubMaxAge. Moved from the hub as it was; the hub relays the route (devicewire.UsageRoutes).

const (
	usageHubFresh           = 30 * time.Second // the merged answer stands this long for every app reading it
	usageHubAsk             = 8 * time.Second  // per machine
	usageHubStaleAfter      = 30 * time.Minute // a sample older than this is shown as stale
	usageHubMaxAge          = 3 * 24 * time.Hour
	usagePassResetTolerance = 60 * time.Second // reset-time jitter tolerance when matching a local sample to a pass-through extension row

	// usageHubKept: the usage route answers with a merged answer up to this old at once and refreshes it behind
	// (usageWindowsHub.read): the apps poll every minute, twice usageHubFresh, so nearly every poll found it expired
	// and waited for every online computer (up to usageHubAsk each, #13348).
	usageHubKept = 3 * time.Minute
)

// usageSource is a machine as the aggregator sees it.
type usageSource struct {
	id     string
	name   string // what the computer is called now ("" for its id)
	online bool
	ask    func(ctx context.Context) (proto.UsageSnapshot, error)
}

type usageAnswer struct {
	accounts []proto.UsageNodeAccount
	at       time.Time // when the node answered (the manager's clock)
}

type usageWindowsHub struct {
	mu        sync.Mutex
	byMachine map[string]usageAnswer
	merged    []proto.UsageAccount
	at        time.Time
	loading   chan struct{} // closed when the poll in flight is done
	// gateways gives the samples of the account-pool gateways (usagegateways.go, #2595); nil: none.
	gateways func(now time.Time) []proto.UsageAccount
	// balances gives the launchers' prepaid balances (usagebalance.go, #11825); nil: none.
	balances func(now time.Time) []proto.UsageAccount
}

// invalidate drops the merged list so that the next read builds it again (a gateway answered or was edited).
func (u *usageWindowsHub) invalidate() {
	u.mu.Lock()
	u.at = time.Time{}
	u.mu.Unlock()
}

// providers are the providers of the accounts the last poll found (what the usage bars could show), for the admin
// page's choice of providers to hide.
func (u *usageWindowsHub) providers() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []string
	for _, a := range u.merged {
		out = append(out, canonicalUsageProvider(a.Provider))
	}
	return out
}

// usageSources are the machines whose node may be asked: the off switch in hub.json is honoured here as well as by
// the node, so a machine that is off is not even called.
func (d *Devices) usageSources() []usageSource {
	var out []usageSource
	for _, m := range d.machinesNow() {
		if !m.usageWindows() {
			continue
		}
		m.mu.Lock()
		online := m.online
		m.mu.Unlock()
		out = append(out, usageSource{id: m.cfg.ID, name: m.name(), online: online, ask: func(ctx context.Context) (proto.UsageSnapshot, error) {
			var snap proto.UsageSnapshot
			err := m.call(ctx, "usage.windows", map[string]any{}, &snap)
			return snap, err
		}})
	}
	return out
}

// read is poll for the usage route. A merged answer past usageHubFresh but younger than usageHubKept is returned at
// once and polled again in the background, so the app that reads it does not wait for the machines; the next read gets
// the new one. None yet, an older one, or one invalidated (a gateway answered, the sources were edited: invalidate zeroes
// u.at, so it reads as too old) is polled first, as before. The scheduler's capacity view keeps calling poll itself.
func (u *usageWindowsHub) read(ctx context.Context, sources []usageSource, now time.Time) []proto.UsageAccount {
	u.mu.Lock()
	kept, age, busy := u.merged, now.Sub(u.at), u.loading != nil
	if kept == nil || age < usageHubFresh || age >= usageHubKept {
		u.mu.Unlock()
		return u.poll(ctx, sources, now)
	}
	if !busy {
		done := make(chan struct{})
		u.loading = done
		go u.refresh(ctx, sources, now, done) // it detaches each machine's call from ctx, bounded by usageHubAsk
	}
	u.mu.Unlock()
	return kept
}

// poll asks the online machines (in parallel) and returns the merged accounts. Concurrent callers share one poll.
func (u *usageWindowsHub) poll(ctx context.Context, sources []usageSource, now time.Time) []proto.UsageAccount {
	u.mu.Lock()
	if u.merged != nil && !u.at.IsZero() && now.Sub(u.at) < usageHubFresh {
		out := u.merged
		u.mu.Unlock()
		return out
	}
	if wait := u.loading; wait != nil { // someone is asking already: take their answer
		u.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
		}
		u.mu.Lock()
		out := u.merged
		u.mu.Unlock()
		return out
	}
	done := make(chan struct{})
	u.loading = done
	u.mu.Unlock()
	return u.refresh(ctx, sources, now, done)
}

// refresh is the poll in flight that the caller marked as u.loading (done): it asks the machines, merges their answers
// with the gateways' and balances, and closes done.
func (u *usageWindowsHub) refresh(ctx context.Context, sources []usageSource, now time.Time, done chan struct{}) []proto.UsageAccount {
	answers := make([]*usageAnswer, len(sources))
	var wg sync.WaitGroup
	for i, s := range sources {
		if !s.online {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(context.WithoutCancel(ctx), usageHubAsk)
			defer cancel()
			snap, err := s.ask(c)
			if err != nil { // an older node does not know usage.windows; a failed call keeps the last answer
				return
			}
			if !snap.Enabled {
				answers[i] = &usageAnswer{} // the node is off: forget what it said
				return
			}
			answers[i] = &usageAnswer{accounts: snap.Accounts, at: now}
		}()
	}
	wg.Wait()

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.byMachine == nil {
		u.byMachine = map[string]usageAnswer{}
	}
	known := map[string]string{} // id: name
	for i, s := range sources {
		known[s.id] = cmp.Or(s.name, s.id)
		if a := answers[i]; a != nil {
			if a.at.IsZero() {
				delete(u.byMachine, s.id)
			} else {
				u.byMachine[s.id] = *a
			}
		}
	}
	var all []proto.UsageAccount
	for id, a := range u.byMachine {
		name, ok := known[id]
		if !ok || now.Sub(a.at) > usageHubMaxAge { // a machine taken out of hub.json, or one gone for days
			delete(u.byMachine, id)
			continue
		}
		for n, sample := range a.accounts { // legacy nodes answer in a fixed order, sorted by where they read the data
			acct := sample.UsageAccount
			// what a node says of where it came from is not taken: it is this machine, by the name it has now
			acct.Local, acct.Machines, acct.Sources, acct.Problem, acct.Ref = true, []string{name}, nil, "", ""
			acct.Origin = id + "/" + strconv.Itoa(n)
			acct.CodexAccountKey = sample.AccountKey
			if acct.Provider == "codex" && sample.SourceKey != "" {
				acct.Origin = id + "/source/" + sample.SourceKey
			}
			all = append(all, acct)
		}
	}
	if u.gateways != nil {
		all = append(all, u.gateways(now)...)
	}
	if u.balances != nil {
		all = append(all, u.balances(now)...)
	}
	u.merged, u.at = mergeUsageAccounts(all, now), now
	u.loading = nil
	close(done)
	return u.merged
}

// mergeUsageAccounts makes one account of the samples that are the same account, drops what has run out and orders
// the result for the sidebar.
//
// Codex samples are grouped by their account key and use the newest snapshot's windows. Samples without a key
// stand on their own. Other providers group by a plan window length and reset second (or, for accounts with only
// per-model windows, by model scope, window length and reset second across distinct reporting sources), except when a
// gateway reports distinct accounts. Pure-local samples whose active plan windows unambiguously match a pass-through
// extension row (Pass) of the same canonical provider (except Antigravity, reconciled per window on display) are
// folded into that extension row so the same subscription is not shown twice. Their windows are taken per length (and
// model) from the newest sample. Expired windows and accounts with no live window are dropped. Samples older than
// usageHubMaxAge are dropped, and the remaining samples are marked stale according to their source. Accounts show
// where they were seen and the gateway's name when available. The most used accounts come first, stale ones last.
func mergeUsageAccounts(in []proto.UsageAccount, now time.Time) []proto.UsageAccount {
	var live, passed []proto.UsageAccount
	for _, a := range in {
		if now.Sub(a.SampledAt) > usageHubMaxAge {
			continue
		}
		if a.Pass { // an extension's row stands as it was sent
			if a.Windows == nil {
				a.Windows = []proto.UsageWindow{}
			}
			a.ID = usageRowID(a.Provider, "extension|"+a.Origin)
			a.Provider = canonicalUsageProvider(a.Provider)
			staleAt := a.SampledAt.Add(usageGatewayStaleAfter)
			a.StaleAt = &staleAt
			a.Stale = a.Stale || now.After(staleAt)
			passed = append(passed, a)
			continue
		}
		var wins []proto.UsageWindow
		for _, w := range a.Windows {
			if w.ResetsAt != nil && w.UsedPercent == 0 && w.ResetsAt.Unix() <= 0 {
				w.ResetsAt = nil
			}
			if w.ResetsAt != nil && !w.ResetsAt.After(now) {
				continue
			}
			wins = append(wins, w)
		}
		// Keep an identified Codex snapshot for grouping even with no live windows. Its expiry must not revive an
		// older snapshot of that account. A prepaid balance has no window at all: keep it unless it has also aged out
		// above (a failed first read has no Balance but is still a balance sample, so its row can say so).
		if len(wins) == 0 && (a.Provider != "codex" || a.CodexAccountKey == "") && !isBalanceSample(a) {
			continue
		}
		a.Windows = wins
		live = append(live, a)
	}

	// union-find over the samples
	parent := make([]int, len(live))
	for i := range parent {
		parent[i] = i
	}
	var find func(i int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	type key struct {
		provider string
		scope    string
		minutes  int
		resets   int64
		local    bool
	}
	seen := map[key]int{}
	codexAccounts := map[string]int{}
	// A component can grow through several samples. Checking only the first-seen sample lets a local sample bridge two
	// different accounts of one gateway (#7324), so carry every component's refs and members and reject any conflict before joining it.
	refs := make(map[int][]string, len(live))
	compMembers := make(map[int][]int, len(live))
	for i, a := range live {
		compMembers[i] = []int{i}
		if a.Ref != "" {
			refs[i] = []string{a.Ref}
		}
	}
	// One launcher's balance samples are one row, whatever their amounts and sample times (#11825).
	balanceRefs := map[string]int{}
	for i, a := range live {
		if !isBalanceSample(a) || a.Ref == "" {
			continue
		}
		if j, ok := balanceRefs[a.Ref]; ok {
			left, right := find(i), find(j)
			if left != right {
				parent[left] = right
				compMembers[right] = append(compMembers[right], compMembers[left]...)
				delete(compMembers, left)
			}
		} else {
			balanceRefs[a.Ref] = i
		}
	}
	var modelOnlyInfos []modelOnlyInfo
	for i, a := range live {
		if a.Provider == "codex" {
			if account := a.CodexAccountKey; account != "" {
				if first, ok := codexAccounts[account]; ok {
					parent[i] = first
				} else {
					codexAccounts[account] = i
				}
			}
			continue
		}
		provider := canonicalUsageProvider(a.Provider)
		hasPlan := slices.ContainsFunc(a.Windows, func(w proto.UsageWindow) bool { return strings.TrimSpace(w.Model) == "" })
		if !hasPlan {
			if info, ok := buildModelOnlyInfo(i, provider, a); ok {
				modelOnlyInfos = append(modelOnlyInfos, info)
			}
			continue
		}
		for _, w := range a.Windows {
			if w.ResetsAt == nil || strings.TrimSpace(w.Model) != "" {
				continue
			}
			k := key{provider: provider, minutes: w.WindowMinutes, resets: w.ResetsAt.Unix()}
			if provider == antigravityUsageProvider {
				// Reconcile local and external Antigravity windows individually in the display response.
				k.scope, k.local = antigravityQuotaScope(a, w), a.Local
			}
			j, ok := seen[k]
			if !ok {
				seen[k] = i
				continue
			}
			left, right := find(i), find(j)
			if left == right {
				continue
			}
			conflict := false
			for _, aRef := range refs[left] {
				for _, bRef := range refs[right] {
					if sameSourceOtherAccount(aRef, bRef) {
						conflict = true
					}
				}
			}
			for _, mi := range compMembers[left] {
				for _, mj := range compMembers[right] {
					if sameProviderAccountConflict(live[mi], live[mj]) {
						conflict = true
					}
				}
			}
			if conflict {
				continue
			}
			parent[left] = right
			refs[right] = append(refs[right], refs[left]...)
			delete(refs, left)
			compMembers[right] = append(compMembers[right], compMembers[left]...)
			delete(compMembers, left)
		}
	}
	mergeModelOnlyAccounts(modelOnlyInfos, parent, find)
	mergeIdenticalAntigravityAccounts(live, parent, find)
	groups := map[int][]proto.UsageAccount{}
	var order []int
	for i, a := range live {
		r := find(i)
		if _, ok := groups[r]; !ok {
			order = append(order, r)
		}
		groups[r] = append(groups[r], a)
	}

	out := []proto.UsageAccount{} // never nil: nil means "not asked yet" to poll
	for _, r := range order {
		members := groups[r]
		sort.SliceStable(members, func(i, j int) bool {
			a, b := members[i], members[j]
			if a.Provider == "codex" && a.SampledAt.Equal(b.SampledAt) {
				return a.Origin < b.Origin
			}
			return a.SampledAt.After(b.SampledAt)
		})
		newest := members[0]
		if len(newest.Windows) == 0 && !isBalanceSample(newest) {
			continue
		}
		acct := proto.UsageAccount{Provider: newest.Provider, Name: newest.Name, Plan: newest.Plan, SampledAt: newest.SampledAt,
			Problem: newest.Problem, CodexAccountKey: newest.CodexAccountKey, Balance: newest.Balance, ReadingSource: newest.ReadingSource}
		// A gateway's name is one somebody gave the account; the machines only know "Codex Pro".
		naming := -1
		for i, m := range members {
			if len(m.Sources) > 0 && !m.Local {
				naming = i
				break
			}
		}
		if naming >= 0 {
			acct.Name, acct.Plan = members[naming].Name, members[naming].Plan
		}
		type slot struct {
			minutes int
			model   string
		}
		bySlot := map[slot]proto.UsageWindow{}
		provider := canonicalUsageProvider(newest.Provider)
		var sources, machines []string
		for i, m := range members {
			if acct.CodexAccountKey == "" && m.CodexAccountKey != "" {
				acct.CodexAccountKey = m.CodexAccountKey
			}
			if acct.Plan == "" && m.Plan != "" { // the newest sample that knows the plan names the account
				acct.Plan = m.Plan
				if naming < 0 {
					acct.Name = m.Name
				}
			}
			if naming < 0 && antigravityAccountIdentity(acct) == "" && antigravityAccountIdentity(m) != "" {
				acct.Name = m.Name
			}
			acct.Local = acct.Local || m.Local
			sources = append(sources, m.Sources...)
			machines = append(machines, m.Machines...)
			if acct.Provider == "codex" && i > 0 {
				continue // older snapshots may describe different quota windows
			}
			for _, w := range m.Windows {
				k := slot{w.WindowMinutes, usageWindowModelKey(provider, w.Model)}
				if _, ok := bySlot[k]; !ok {
					bySlot[k] = w
				}
			}
		}
		for _, w := range bySlot {
			acct.Windows = append(acct.Windows, w)
		}
		sort.Slice(acct.Windows, func(i, j int) bool {
			a, b := acct.Windows[i], acct.Windows[j]
			if a.WindowMinutes != b.WindowMinutes {
				return a.WindowMinutes < b.WindowMinutes
			}
			return a.Model < b.Model
		})
		if acct.Balance == nil {
			for _, m := range members { // the newest sample that has an amount (a failed read keeps the previous one)
				if m.Balance != nil {
					acct.Balance = m.Balance
					break
				}
			}
		}
		if acct.Windows == nil {
			acct.Windows = []proto.UsageWindow{} // a JSON list, not null: clients filter it as one
		}
		acct.Sources, acct.Machines = uniqueSorted(sources), uniqueSorted(machines)
		acct.Ref, acct.Origin = firstOf(members, func(m proto.UsageAccount) string { return m.Ref }), firstOf(members, func(m proto.UsageAccount) string { return m.Origin })
		// Row IDs predate provider aliases and are stored by clients that pin accounts.
		acct.ID = usageAccountID(acct)
		acct.Provider = canonicalUsageProvider(acct.Provider)
		gateway := len(newest.Sources) > 0 && !newest.Local
		after := usageHubStaleAfter
		if gateway {
			after = usageGatewayStaleAfter
		}
		// stale now (a gateway or a balance source may say so itself), and when it will be if nothing newer comes (#1905)
		acct.Stale = now.Sub(acct.SampledAt) > after || (gateway || isBalanceSample(newest)) && (newest.Stale || newest.Problem != "")
		staleAt := acct.SampledAt.Add(after)
		acct.StaleAt = &staleAt
		out = append(out, acct)
	}

	out = mergeLocalIntoPassed(out, passed, now)

	// Whatever still shares an id (two accounts with only per-model windows that reset together) is numbered in the
	// order of where it was read, so that every id is a row of its own in the clients' lists (#1904).
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		if kA, kB := cmp.Or(out[i].CodexAccountKey, antigravityAccountIdentity(out[i])), cmp.Or(out[j].CodexAccountKey, antigravityAccountIdentity(out[j])); kA != kB {
			return kA < kB
		}
		return out[i].Origin < out[j].Origin
	})
	ids := map[string]int{}
	for i := range out {
		ids[out[i].ID]++
		if n := ids[out[i].ID]; n > 1 {
			out[i].ID = fmt.Sprintf("%s-%d", out[i].ID, n)
		}
	}
	// Number same-name accounts by ID. For unidentified Codex samples, only number rows with the same visible source.
	type titleGroup struct {
		name, machines, gateways string
		unidentified             bool
	}
	count := map[titleGroup]int{}
	for i := range out {
		a := &out[i]
		group := titleGroup{name: a.Name}
		if a.Provider == "codex" && a.CodexAccountKey == "" {
			group.unidentified = true
			group.machines = strings.Join(a.Machines, "\x00")
			group.gateways = strings.Join(a.Sources, "\x00")
		}
		count[group]++
		if n := count[group]; n > 1 {
			a.Name = fmt.Sprintf("%s %d", a.Name, n)
		}
	}
	sortUsageAccounts(out)
	return append(out, passed...) // an extension's rows last, in the order it gave them
}

type passWindowRef struct {
	index  int
	resets time.Time
}

type passModelSlot struct {
	model   string
	minutes int
}

type passWindowIndex struct {
	provider   string
	plan       map[int]passWindowRef
	multiPlan  map[int]bool
	model      map[passModelSlot]passWindowRef
	multiModel map[passModelSlot]bool
}

func indexPassWindows(a proto.UsageAccount, now time.Time) passWindowIndex {
	provider := canonicalUsageProvider(a.Provider)
	idx := passWindowIndex{provider: provider}
	for i, w := range a.Windows {
		if w.WindowMinutes <= 0 || w.ResetsAt == nil || !w.ResetsAt.After(now) {
			continue
		}
		resets := *w.ResetsAt
		if strings.TrimSpace(w.Model) == "" {
			if idx.plan == nil {
				idx.plan = make(map[int]passWindowRef, len(a.Windows))
			}
			if _, exists := idx.plan[w.WindowMinutes]; exists {
				if idx.multiPlan == nil {
					idx.multiPlan = map[int]bool{}
				}
				idx.multiPlan[w.WindowMinutes] = true
				continue
			}
			idx.plan[w.WindowMinutes] = passWindowRef{index: i, resets: resets}
			continue
		}
		if idx.model == nil {
			idx.model = make(map[passModelSlot]passWindowRef, len(a.Windows))
		}
		slot := passModelSlot{model: usageWindowModelKey(provider, w.Model), minutes: w.WindowMinutes}
		if _, exists := idx.model[slot]; exists {
			if idx.multiModel == nil {
				idx.multiModel = map[passModelSlot]bool{}
			}
			idx.multiModel[slot] = true
			continue
		}
		idx.model[slot] = passWindowRef{index: i, resets: resets}
	}
	return idx
}

func withinPassResetTolerance(a, b time.Time) bool {
	diff := a.Sub(b)
	if diff < 0 {
		diff = -diff
	}
	return diff <= usagePassResetTolerance
}

func passWindowsMatch(local, pass passWindowIndex) bool {
	if local.provider == "" || local.provider == antigravityUsageProvider || local.provider != pass.provider {
		return false
	}
	if len(local.plan) == 0 || len(pass.plan) == 0 || len(local.multiPlan) > 0 || len(pass.multiPlan) > 0 {
		return false
	}
	matched := false
	for minutes, lw := range local.plan {
		pw, ok := pass.plan[minutes]
		if !ok {
			continue
		}
		if !withinPassResetTolerance(lw.resets, pw.resets) {
			return false
		}
		matched = true
	}
	if !matched {
		return false
	}
	for slot, lw := range local.model {
		pw, ok := pass.model[slot]
		if !ok {
			continue
		}
		if local.multiModel[slot] || pass.multiModel[slot] || !withinPassResetTolerance(lw.resets, pw.resets) {
			return false
		}
	}
	return true
}

func localPassConflict(a, b proto.UsageAccount, ia, ib passWindowIndex) bool {
	if a.CodexAccountKey != "" && b.CodexAccountKey != "" && a.CodexAccountKey != b.CodexAccountKey {
		return true
	}
	if a.Origin != "" && b.Origin != "" && sameMachineOtherOrigin(a.Origin, b.Origin) {
		return true
	}
	for _, ma := range a.Machines {
		if slices.Contains(b.Machines, ma) {
			return true
		}
	}
	for minutes, wa := range ia.plan {
		wb, ok := ib.plan[minutes]
		if !ok {
			continue
		}
		if !withinPassResetTolerance(wa.resets, wb.resets) {
			return true
		}
	}
	for slot, wa := range ia.model {
		wb, ok := ib.model[slot]
		if !ok {
			continue
		}
		if ia.multiModel[slot] || ib.multiModel[slot] || !withinPassResetTolerance(wa.resets, wb.resets) {
			return true
		}
	}
	return false
}

// mergeLocalIntoPassed folds pure-local accounts into matching extension pass-through rows when active plan windows
// uniquely identify the same account. The extension row keeps its stable ID, display identity, window metadata and
// ordering; local machines are merged into Machines (with Local set true) and the fresher non-stale reading wins, with
// the extension winning ties. Ambiguous or conflicting matches stay separate, and Antigravity remains handled by
// reconcileAntigravityUsage.
func mergeLocalIntoPassed(out, passed []proto.UsageAccount, now time.Time) []proto.UsageAccount {
	if len(out) == 0 || len(passed) == 0 {
		return out
	}
	passIdx := make([]passWindowIndex, len(passed))
	eligiblePass := false
	for pi, p := range passed {
		if isBalanceSample(p) || canonicalUsageProvider(p.Provider) == antigravityUsageProvider {
			continue
		}
		idx := indexPassWindows(p, now)
		if len(idx.plan) > 0 && len(idx.multiPlan) == 0 {
			passIdx[pi] = idx
			eligiblePass = true
		}
	}
	if !eligiblePass {
		return out
	}

	localIdx := make([]passWindowIndex, len(out))
	passMatches := make(map[int][]int)
	for li, a := range out {
		if !a.Local || len(a.Sources) > 0 || isBalanceSample(a) || canonicalUsageProvider(a.Provider) == antigravityUsageProvider {
			continue
		}
		idx := indexPassWindows(a, now)
		if len(idx.plan) == 0 || len(idx.multiPlan) > 0 {
			continue
		}
		localIdx[li] = idx
		matchedPass := -1
		ambiguous := false
		for pi := range passed {
			if !passWindowsMatch(idx, passIdx[pi]) {
				continue
			}
			if matchedPass >= 0 {
				ambiguous = true
				break
			}
			matchedPass = pi
		}
		if matchedPass >= 0 && !ambiguous {
			passMatches[matchedPass] = append(passMatches[matchedPass], li)
		}
	}
	if len(passMatches) == 0 {
		return out
	}

	consumed := make([]bool, len(out))
	for pi, matchedLocals := range passMatches {
		conflict := false
		for i := 0; i < len(matchedLocals) && !conflict; i++ {
			for j := i + 1; j < len(matchedLocals); j++ {
				li, lj := matchedLocals[i], matchedLocals[j]
				if localPassConflict(out[li], out[lj], localIdx[li], localIdx[lj]) {
					conflict = true
					break
				}
			}
		}
		if conflict {
			continue
		}
		sort.SliceStable(matchedLocals, func(i, j int) bool {
			ri := usageAccountReading(out[matchedLocals[i]], now)
			rj := usageAccountReading(out[matchedLocals[j]], now)
			if ri.stale != rj.stale {
				return !ri.stale
			}
			if !ri.at.Equal(rj.at) {
				return ri.at.After(rj.at)
			}
			return out[matchedLocals[i]].Origin < out[matchedLocals[j]].Origin
		})

		p := &passed[pi]
		extReading := usageAccountReading(*p, now)
		machines := slices.Clone(p.Machines)
		for _, li := range matchedLocals {
			consumed[li] = true
			machines = append(machines, out[li].Machines...)
		}
		p.Local = true
		p.Machines = uniqueSorted(machines)

		best := out[matchedLocals[0]]
		bestReading := usageAccountReading(best, now)
		if bestReading.preferredTo(extReading) {
			p.Windows = slices.Clone(p.Windows)
			updated := make([]bool, len(p.Windows))
			for _, li := range matchedLocals {
				r := usageAccountReading(out[li], now)
				if !r.preferredTo(extReading) {
					break
				}
				for minutes, lw := range localIdx[li].plan {
					if pw, ok := passIdx[pi].plan[minutes]; ok && !updated[pw.index] {
						p.Windows[pw.index].UsedPercent = out[li].Windows[lw.index].UsedPercent
						updated[pw.index] = true
					}
				}
				for slot, lw := range localIdx[li].model {
					if pw, ok := passIdx[pi].model[slot]; ok && !updated[pw.index] {
						p.Windows[pw.index].UsedPercent = out[li].Windows[lw.index].UsedPercent
						updated[pw.index] = true
					}
				}
			}
			var staleAt *time.Time
			if best.StaleAt != nil {
				t := *best.StaleAt
				staleAt = &t
			}
			p.SampledAt, p.StaleAt, p.Stale, p.Problem = best.SampledAt, staleAt, bestReading.stale, best.Problem
		}
	}

	kept := make([]proto.UsageAccount, 0, len(out))
	for li, a := range out {
		if !consumed[li] {
			kept = append(kept, a)
		}
	}
	return kept
}

func sortUsageAccounts(accounts []proto.UsageAccount) {
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		if a.Pass || b.Pass {
			return !a.Pass // keep extension rows last and in their original order
		}
		if a.Stale != b.Stale {
			return !a.Stale
		}
		if a, b := mostUsed(a), mostUsed(b); a != b {
			return a > b
		}
		return a.ID < b.ID
	})
}

// isBalanceSample: a sample of a provider that reports a prepaid balance instead of rate-limit windows (#11825). The
// balance poller marks its samples' Ref "balance:<launcher>", so a failed first read (no Balance yet) is still one.
func isBalanceSample(a proto.UsageAccount) bool {
	return a.Balance != nil || strings.HasPrefix(a.Ref, "balance:")
}

// sameSourceOtherAccount: both refs name an account of one gateway ("<source id>:<account id>") and they are not the
// same account. A gateway knows its accounts apart, whatever their reset times look like.
func sameSourceOtherAccount(a, b string) bool {
	sa, _, okA := strings.Cut(a, ":")
	sb, _, okB := strings.Cut(b, ":")
	return okA && okB && sa == sb && a != b
}

// sameMachineOtherOrigin: both origins name distinct local sources on one machine ("<machine id>/<source>"). Two
// model-only accounts read from separate local sources on the same machine are not merged together.
func sameMachineOtherOrigin(a, b string) bool {
	ma, _, okA := strings.Cut(a, "/")
	mb, _, okB := strings.Cut(b, "/")
	return okA && okB && ma != "" && ma == mb && a != b
}

// usageWindowModelKey normalizes a per-model window's scope for cross-machine deduplication and slot merging.
func usageWindowModelKey(provider, model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if provider == antigravityUsageProvider {
		return antigravityModelScope(model)
	}
	return strings.ToLower(model)
}

type modelOnlySlot struct {
	scope   string
	minutes int
}

type modelOnlyInfo struct {
	index    int
	sample   proto.UsageAccount
	provider string
	local    bool
	ref      string
	origin   string
	resets   map[modelOnlySlot]int64
	multi    map[modelOnlySlot]bool
}

func buildModelOnlyInfo(index int, provider string, a proto.UsageAccount) (modelOnlyInfo, bool) {
	info := modelOnlyInfo{
		index:    index,
		sample:   a,
		provider: provider,
		local:    a.Local,
		ref:      a.Ref,
		origin:   a.Origin,
		resets:   make(map[modelOnlySlot]int64, len(a.Windows)),
	}
	for _, w := range a.Windows {
		if w.ResetsAt == nil {
			continue
		}
		scope := usageWindowModelKey(provider, w.Model)
		if provider == antigravityUsageProvider {
			scope = antigravityQuotaScope(a, w)
		}
		slot := modelOnlySlot{scope: scope, minutes: w.WindowMinutes}
		ts := w.ResetsAt.Unix()
		if prev, exists := info.resets[slot]; exists && prev != ts {
			if info.multi == nil {
				info.multi = map[modelOnlySlot]bool{}
			}
			info.multi[slot] = true
		} else {
			info.resets[slot] = ts
		}
	}
	return info, len(info.resets) > 0
}

func modelOnlyConflict(a, b modelOnlyInfo) bool {
	if sameProviderAccountConflict(a.sample, b.sample) {
		return true
	}
	if a.ref != "" && b.ref != "" && sameSourceOtherAccount(a.ref, b.ref) {
		return true
	}
	if a.local && b.local && a.origin != "" && b.origin != "" && sameMachineOtherOrigin(a.origin, b.origin) &&
		(a.sample.CodexAccountKey == "" || a.sample.CodexAccountKey != b.sample.CodexAccountKey) {
		return true
	}
	for slot, ra := range a.resets {
		rb, ok := b.resets[slot]
		if !ok {
			continue
		}
		if ra != rb || a.multi[slot] || b.multi[slot] {
			return true
		}
	}
	return false
}

// mergeModelOnlyAccounts groups model-only accounts (samples with no plan window) across distinct sources only when
// matches are unambiguous and every shared model-pool reset signature agrees. Any sample matching mutually conflicting
// candidates, or any component containing an internal conflict, conservatively stays separate instead of performing a
// first-hit union that could overwrite another account's healthy model pool.
func mergeModelOnlyAccounts(infos []modelOnlyInfo, parent []int, find func(int) int) {
	if len(infos) < 2 {
		return
	}
	type winKey struct {
		provider string
		local    bool
		scope    string
		minutes  int
		resets   int64
	}
	byWindow := map[winKey][]int{}
	for idx, info := range infos {
		// Reconcile local and external Antigravity windows individually in the display response.
		localBucket := info.provider == antigravityUsageProvider && info.local
		for slot, resets := range info.resets {
			if info.multi[slot] {
				continue
			}
			k := winKey{provider: info.provider, local: localBucket, scope: slot.scope, minutes: slot.minutes, resets: resets}
			byWindow[k] = append(byWindow[k], idx)
		}
	}
	adj := make([]map[int]struct{}, len(infos))
	addEdge := func(u, v int) {
		if adj[u] == nil {
			adj[u] = map[int]struct{}{}
		}
		if adj[v] == nil {
			adj[v] = map[int]struct{}{}
		}
		adj[u][v] = struct{}{}
		adj[v][u] = struct{}{}
	}
	for _, group := range byWindow {
		if len(group) < 2 {
			continue
		}
		for a := 0; a < len(group); a++ {
			for b := a + 1; b < len(group); b++ {
				u, v := group[a], group[b]
				if _, exists := adj[u][v]; exists {
					continue
				}
				if !modelOnlyConflict(infos[u], infos[v]) {
					addEdge(u, v)
				}
			}
		}
	}
	ambiguous := make([]bool, len(infos))
	for u, neighbors := range adj {
		if len(neighbors) < 2 {
			continue
		}
		list := make([]int, 0, len(neighbors))
		for v := range neighbors {
			list = append(list, v)
		}
		for i := 0; i < len(list) && !ambiguous[u]; i++ {
			for j := i + 1; j < len(list); j++ {
				if modelOnlyConflict(infos[list[i]], infos[list[j]]) {
					ambiguous[u] = true
					break
				}
			}
		}
	}
	visited := make([]bool, len(infos))
	for start := range infos {
		if visited[start] || ambiguous[start] || len(adj[start]) == 0 {
			continue
		}
		visited[start] = true
		comp := []int{start}
		for head := 0; head < len(comp); head++ {
			u := comp[head]
			for v := range adj[u] {
				if visited[v] || ambiguous[v] {
					continue
				}
				visited[v] = true
				comp = append(comp, v)
			}
		}
		if len(comp) < 2 {
			continue
		}
		conflict := false
		for i := 0; i < len(comp) && !conflict; i++ {
			for j := i + 1; j < len(comp); j++ {
				if modelOnlyConflict(infos[comp[i]], infos[comp[j]]) {
					conflict = true
					break
				}
			}
		}
		if conflict {
			continue
		}
		root := find(infos[comp[0]].index)
		for _, member := range comp[1:] {
			parent[find(infos[member].index)] = root
		}
	}
}

// mergeIdenticalAntigravityAccounts joins local Antigravity samples across machines that explicitly identify the same
// signed-in account (matching AccountKey or signed-in email/name) even when every window is currently untouched (no
// ResetsAt yet), while refusing any union whose existing components conflict.
func mergeIdenticalAntigravityAccounts(live []proto.UsageAccount, parent []int, find func(int) int) {
	byIdentity := map[string][]int{}
	for i, a := range live {
		if canonicalUsageProvider(a.Provider) != antigravityUsageProvider || !a.Local || len(a.Sources) > 0 {
			continue
		}
		if a.CodexAccountKey != "" {
			byIdentity["key:"+a.CodexAccountKey] = append(byIdentity["key:"+a.CodexAccountKey], i)
		}
		if id := antigravityAccountIdentity(a); id != "" {
			byIdentity["id:"+id] = append(byIdentity["id:"+id], i)
		}
	}
	for _, group := range byIdentity {
		if len(group) < 2 {
			continue
		}
		for i := 1; i < len(group); i++ {
			left, right := find(group[0]), find(group[i])
			if left == right {
				continue
			}
			conflict := false
			for mi := range live {
				if find(mi) != left {
					continue
				}
				infoA, _ := buildModelOnlyInfo(mi, antigravityUsageProvider, live[mi])
				for mj := range live {
					if find(mj) != right {
						continue
					}
					infoB, _ := buildModelOnlyInfo(mj, antigravityUsageProvider, live[mj])
					if modelOnlyConflict(infoA, infoB) {
						conflict = true
						break
					}
				}
				if conflict {
					break
				}
			}
			if !conflict {
				parent[right] = left
			}
		}
	}
}

// mostUsed is the used percentage of the account's fullest plan window: a per-model allowance that ran out is not the
// account running out, so model windows only count when the account has no plan window (as the clients' fullest).
func mostUsed(a proto.UsageAccount) float64 {
	var plan, model float64
	hasPlan := false
	for _, w := range a.Windows {
		if w.Model == "" {
			plan, hasPlan = max(plan, w.UsedPercent), true
		} else {
			model = max(model, w.UsedPercent)
		}
	}
	if hasPlan {
		return plan
	}
	return model
}

// firstOf is the smallest non-empty value of field among the samples of one account ("" when none has one).
func firstOf(members []proto.UsageAccount, field func(proto.UsageAccount) string) string {
	first := ""
	for _, m := range members {
		if v := field(m); v != "" && (first == "" || v < first) {
			first = v
		}
	}
	return first
}

// usageAccountID gives Codex rows a key independent of quota windows. For other providers the longest plan window,
// along with the gateway ref or the origin when needed, identifies the row (#1904).
func usageAccountID(a proto.UsageAccount) string {
	if len(a.Windows) == 0 { // a balance-only row: the launcher, not a window, names it
		return usageRowID(a.Provider, "balance|"+cmp.Or(a.Ref, a.Name))
	}
	if a.Provider == "codex" {
		key := "codex|source|" + cmp.Or(a.Ref, a.Origin)
		if a.CodexAccountKey != "" {
			key = "codex|account|" + a.CodexAccountKey
		}
		return usageRowID(a.Provider, key)
	}
	w := a.Windows[len(a.Windows)-1]
	for i := len(a.Windows) - 1; i >= 0; i-- { // the longest window of the plan itself, not of one model
		if a.Windows[i].Model == "" {
			w = a.Windows[i]
			break
		}
	}
	var resets int64
	if w.ResetsAt != nil {
		resets = w.ResetsAt.Unix()
	}
	key := fmt.Sprintf("%s|%d|%d", a.Provider, w.WindowMinutes, resets)
	switch {
	case a.Ref != "":
		key += "|" + a.Ref
	case w.ResetsAt == nil:
		key += "|" + cmp.Or(a.CodexAccountKey, antigravityAccountIdentity(a), a.Origin)
	}
	return usageRowID(a.Provider, key)
}

func usageRowID(provider, key string) string {
	sum := sha256.Sum256([]byte(key))
	return provider + "-" + hex.EncodeToString(sum[:4])
}

// usageWindowRoutes: GET /api/v1/usage/windows, the accounts' windows for the sidebar's usage bars. Read-only, from
// machines and gateways (docs/server/usage-windows.md); an empty list when no source has usage.
// (#2432: its own block so that merges with the other route blocks stay additive.)
func (d *Devices) usageWindowRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/usage/windows", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		hidden := d.usageGW.config().HiddenProviders
		writeJSON(w, usageWindowsAnswer(hideUsageProviders(d.usageWin.read(r.Context(), d.usageSources(), now), hidden), hidden, now))
	})
}

// usageWindowsAnswer is the body of GET /api/v1/usage/windows. hidden_providers (#13179) names the providers the owner
// left out of the bars, so a client does not list them some other way (the web's "not connected" rows); it is left
// out when nothing is hidden, so the answer is what it was before.
func usageWindowsAnswer(accounts []proto.UsageAccount, hidden []string, now time.Time) map[string]any {
	if accounts == nil {
		accounts = []proto.UsageAccount{}
	}
	out := map[string]any{"accounts": reconcileAntigravityUsage(accounts, now), "checked_at": now.UTC()}
	if len(hidden) > 0 {
		out["hidden_providers"] = hidden
	}
	return out
}

// hideUsageProviders drops the accounts of the providers the owner chose to hide from the usage bars (#11753). It
// works on the answer, not on the poll: the scheduler's capacity view reads the same poll and keeps counting them.
func hideUsageProviders(in []proto.UsageAccount, hidden []string) []proto.UsageAccount {
	if len(hidden) == 0 || len(in) == 0 {
		return in
	}
	out := make([]proto.UsageAccount, 0, len(in))
	for _, a := range in {
		provider := canonicalUsageProvider(a.Provider)
		if !slices.ContainsFunc(hidden, func(p string) bool { return canonicalUsageProvider(p) == provider }) {
			out = append(out, a)
		}
	}
	return out
}
