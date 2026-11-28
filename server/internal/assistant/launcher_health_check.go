package assistant

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// ---- checking ----

// probeLauncherHealth runs launchers.check for launcher on machine; false when the machine could not be asked (no result).
func (s *Service) probeLauncherHealth(ctx context.Context, machine, launcher string) (healthResult, bool) {
	if proto.IsExternalLauncher(launcher) || s.launcherScopeErr(ctx, machine, launcher) != nil {
		return healthResult{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()
	observed, _ := s.launcherHealth(ctx, machine, launcher)
	params := map[string]any{"name": launcher}
	if observed.Kind == errKindUsageLimit && observed.unusable() {
		if models := observed.limitModels(); len(models) > 0 {
			params["model"] = models[0] // one family per probe; a pass clears it and the next probe asks the next
		}
	}
	var res proto.LauncherCheck
	if err := s.call(ctx, machine, assistantwire.DeviceLaunchersCheck, params, &res); err != nil {
		s.logf("launcher health: check %s on %s: %v", launcher, machine, err)
		return healthResult{}, false
	}
	return healthResult{ok: res.OK, agent: res.Agent, model: res.Model, millis: res.Millis, err: res.Error, source: healthSourceCheck, observed: &observed}, true
}

// healthLaunchers are the launchers to check on machine: those it can start (its ~/.zshrc's, the managed ones) and
// the default one when it needs no inventory lookup (plain claude or an in-scope managed launcher,
// launcher_fit.go); not an API provider's. An explicit inventory is authoritative for plain Claude; an older
// node without a catalog still falls back to probing it. A runtime's launcher only when the main chat or Agents run on it (#10369):
// those are in whileFailing, checked only while their record on machine says they do not work, through the runtime's
// own launchers.check as the on-demand probe does. So a failure is rechecked and a pass clears it, but no runtime is
// asked to spend a request each interval, and the record a pass leaves stays while the machine lists the launcher.
func (s *Service) healthLaunchers(ctx context.Context, machine string) (names []string, whileFailing map[string]bool, err error) {
	list, _, known, err := s.machineLaunchersOn(ctx, machine, "")
	if err != nil {
		return nil, nil, err
	}
	names, whileFailing, _ = s.healthLaunchersOf(ctx, machine, list, known)
	return names, whileFailing, nil
}

// healthLaunchersOf is healthLaunchers from the machine's list as read; known says whether it is an authoritative
// catalog rather than an older node's fallback. Runtimes are all its runtimes' launchers.
func (s *Service) healthLaunchersOf(ctx context.Context, machine string, list []proto.Launcher, known ...bool) (names []string, whileFailing map[string]bool, runtimes []string) {
	for _, l := range s.launchersWithinScope(ctx, machine, slices.Clone(list)) {
		switch {
		case startableLauncher(l), l.Name == "claude" && l.Source == "builtin":
			names = append(names, l.Name)
		case runtimeLauncher(l):
			runtimes = append(runtimes, l.Name)
		}
	}
	d := s.startLauncher(ctx, "")
	if !slices.Contains(names, d) && (d != "claude" || len(known) == 0 || !known[0]) {
		if agent, _ := s.launcherWithoutInventory(ctx, machine, d); agent.agent != "" {
			names = append(names, d)
		}
	}
	for _, name := range []string{s.launcherNow(ctx), d} {
		if slices.Contains(runtimes, name) && !slices.Contains(names, name) {
			names = append(names, name)
			if whileFailing == nil {
				whileFailing = map[string]bool{}
			}
			whileFailing[name] = true
		}
	}
	return names, whileFailing, runtimes
}

// runtimeLauncher: l is a runtime's launcher (DSH, Paseo, agy…: its source is the runtime's id), which the node checks
// through the runtime. Not the desktop apps' (their check runs no prompt and always fails) nor an external source's.
func runtimeLauncher(l proto.Launcher) bool {
	source := compat.Source(l.Source)
	return runtimeSource(l.Source) && source != proto.SourceClaudeDesktop && source != codexAppSource &&
		source != proto.ExternalLauncherSource && !proto.IsExternalLauncher(l.Name)
}

// codexAppSource is the source of codex-app, a new chat in the Codex App (node/codexapp_new.go).
const codexAppSource = "codex_app"

// startLauncher is the launcher a start with launcher runs: itself, else model settings › Agents', else claude.
func (s *Service) startLauncher(ctx context.Context, launcher string) string {
	if launcher != "" {
		return launcher
	}
	if b, err := s.resolveModel(ctx, "agent.new"); err == nil {
		if name, ok := strings.CutPrefix(b.Provider, "launcher."); ok && name != "" {
			return name
		}
	}
	return "claude"
}

// healthRound is one look at the machines: the launchers due on each reachable one are checked (each machine's one
// after another, the machines side by side), then repaired; the repair agents at work are followed. wait: return
// when it is all done (tests, a forced check), else it goes on in the background.
func (s *Service) healthRound(ctx context.Context, wait bool) {
	cfg := s.healthConfig(ctx)
	devices := s.devices(ctx)
	b := &s.health
	var wg sync.WaitGroup
	round := time.Now()
	var scheduled time.Duration // machines started so far, times healthStagger: see healthStagger
	for _, d := range devices {
		b.mu.Lock()
		s.healthLoadLocked(ctx)
		cameOnline := d.Reachable && !b.online[d.ID]
		b.online[d.ID] = d.Reachable
		busy := b.checking[d.ID]
		if d.Reachable && cfg.on && !busy {
			b.checking[d.ID] = true
		}
		b.mu.Unlock()
		if !d.Reachable || !cfg.on || busy {
			continue
		}
		wg.Add(1)
		due := round.Add(scheduled) // an absolute time, so a goroutine that is scheduled late does not slip into the one before
		scheduled += healthStagger
		go func() {
			defer wg.Done()
			defer func() {
				b.mu.Lock()
				delete(b.checking, d.ID)
				b.mu.Unlock()
			}()
			if left := time.Until(due); left > 0 { // not healthRound's wait: this goroutine only
				select {
				case <-ctx.Done():
					return
				case <-time.After(left):
				}
			}
			_, eligible := s.checkMachine(ctx, d, cfg, cameOnline, "")
			s.repairMachine(ctx, d, cfg, eligible)
		}()
	}
	if wait {
		wg.Wait()
	}
	s.followRepairs(ctx, cfg)
}

// checkMachine checks machine d's launchers that are due (only: one by that name, at once); the records of launchers
// it no longer has go. It returns the results and the launchers in scope for this check.
func (s *Service) checkMachine(ctx context.Context, d Device, cfg healthConfig, cameOnline bool, only string) ([]LauncherHealth, []string) {
	list, _, known, err := s.machineLaunchersOn(ctx, d.ID, "")
	if err != nil {
		s.logf("launcher health: %s: launchers: %v", d.ID, err)
		return nil, nil
	}
	names, whileFailing, runtimes := s.healthLaunchersOf(ctx, d.ID, list, known)
	// "Test again" of one runtime launcher the machine lists (#14534): a person asked for this check, so it runs
	// whatever the launcher's record says, though rounds check a runtime's launcher only as whileFailing allows.
	asked := only != "" && slices.Contains(runtimes, only)
	if asked && !slices.Contains(names, only) {
		names = append(names, only)
	}
	// A record goes with its launcher: one the machine no longer lists, not one this run merely does not check (a
	// runtime's launcher nobody runs on keeps the record a failed start left, #14534).
	listed := slices.Clone(names)
	for _, l := range list {
		listed = append(listed, l.Name)
	}
	b := &s.health
	b.mu.Lock()
	s.healthLoadLocked(ctx)
	removed := false
	for key, r := range b.recs {
		if r.Machine == d.ID && !slices.Contains(listed, r.Launcher) {
			delete(b.recs, key)
			delete(b.recheckAt, key)
			removed = true
		}
	}
	if removed {
		s.healthSaveLocked(ctx)
	}
	b.mu.Unlock()
	var out []LauncherHealth
	for _, name := range names {
		if ctx.Err() != nil {
			break
		}
		if only != "" && name != only {
			continue
		}
		r, ok := s.launcherHealth(ctx, d.ID, name)
		if whileFailing[name] && !(ok && r.unusable()) && !(asked && name == only) {
			continue // a runtime's launcher that works, or was never seen failing here: nothing to recheck (#10369)
		}
		if only == "" {
			since := time.Since(r.CheckedAt)
			if reset, limited := r.limitReset(); limited {
				// A check before the reset is refused too, and spends a request for it; the first round after it
				// checks, whatever the interval. From a minute after, as the manager continues a turn its limit
				// stopped (limitContinueAfter): one right at the reset may still be refused.
				if time.Now().Before(reset.Add(limitContinueAfter)) {
					continue
				}
			} else if ok && since < cfg.interval && !(cameOnline && since >= healthOnlineRecheck) {
				continue
			}
			if ok && r.Kind == errKindNotInTerminal && r.unusable() {
				continue // the login shell's check cannot tell (recordHealth): it waits for a start, or goes stale
			}
		}
		if res, ok := s.probeLauncherWithRetry(ctx, d.ID, name); ok {
			out = append(out, s.recordHealth(ctx, d.ID, name, res))
		}
	}
	return out, names
}

// probeLauncherWithRetry is probeLauncherHealth, asked once more right away when the first check timed out: only a
// second timeout in a row is recorded. A gateway can leave one request without a first token while the same account
// answers the others in seconds (2026-11-14: one shared account left a few percent of its requests silent for 90
// seconds and longer), and a single silent request would otherwise mark a working launcher failing until the next
// round, half an hour later. A fresh request is a new upstream stream and usually answers.
func (s *Service) probeLauncherWithRetry(ctx context.Context, machine, launcher string) (healthResult, bool) {
	res, ok := s.probeLauncherHealth(ctx, machine, launcher)
	if !ok || res.ok || classifyLauncherError(res.err) != errKindTimeout || ctx.Err() != nil {
		return res, ok
	}
	s.logf("launcher health: %s on %s timed out, checking once more: %s", launcher, machine, clip(oneLine(res.err), 200))
	if again, ok := s.probeLauncherHealth(ctx, machine, launcher); ok {
		return again, true
	}
	return res, true
}

// ---- picking a machine ----

// healthyMachine is another reachable machine that runs launcher and whose check of it did not fail: one where it
// passed first, then one where it was never checked. Optional filters keep candidates that can host the work.
func (s *Service) healthyMachine(ctx context.Context, launcher, except string, suitable ...func(Device) bool) (Device, bool) {
	best, rank := Device{}, 3
	for _, d := range s.devices(ctx) {
		if d.ID == except {
			continue
		}
		p := placementOf(ctx)
		if p != nil {
			p.candidate(d.ID)
		}
		if !d.Reachable {
			p.hit(d.ID, "offline", nil)
			continue
		}
		r, ok := s.launcherHealth(ctx, d.ID, launcher)
		n := 1
		switch {
		case ok && r.unusable():
			p.hit(d.ID, "launcher_unhealthy", i18n.Args{"kind": r.Kind})
			continue
		case ok && r.State == healthOK:
			n = 0
		}
		if p != nil {
			p.candidate(d.ID).HealthRank = &n
		}
		if n >= rank {
			continue
		}
		if !s.launcherOn(ctx, d.ID, launcher) {
			p.hit(d.ID, "launcher_missing", nil)
			continue
		}
		if slices.ContainsFunc(suitable, func(accept func(Device) bool) bool { return !accept(d) }) {
			continue
		}
		best, rank = d, n
	}
	return best, rank < 3
}

// launcherOn: machine has launcher (managed launchers need to be in scope; a failed list says no).
func (s *Service) launcherOn(ctx context.Context, machine, launcher string) bool {
	if proto.IsExternalLauncher(launcher) {
		return false // external targets are never automatic relocation candidates
	}
	if s.launcherScopeErr(ctx, machine, launcher) != nil {
		return false
	}
	if launcher == "claude" || slices.ContainsFunc(s.storedLaunchers(ctx), func(l storedLauncher) bool { return l.Name == launcher && l.Kind != "api" }) {
		return true
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := s.machineLaunchers(cctx, machine)
	return err == nil && slices.ContainsFunc(list, func(l proto.Launcher) bool {
		return l.Name == launcher && l.Source != proto.ExternalLauncherSource && !proto.IsExternalLauncher(l.Name)
	})
}

// rehome moves dir from one machine's home to another's (a path outside the home stays).
func rehome(dir string, from, to Device) string {
	if from.Home == "" || to.Home == "" || from.Home == to.Home {
		return dir
	}
	if dir == from.Home {
		return to.Home
	}
	if rest, ok := strings.CutPrefix(dir, from.Home+"/"); ok {
		return path.Join(to.Home, rest)
	}
	return dir
}

// recheckLauncherNow probes machine's launcher on the spot when its record says the launcher fails, so a fix since
// the last round (a CLI installed, a PATH corrected) clears the record and the start stays on the asked machine.
// A probe already running is joined; a probe within healthRecheckThrottle is not repeated (checked=false), and the
// caller keeps the usual reroute behavior. got=false means the machine could not be asked.
func (s *Service) recheckLauncherNow(ctx context.Context, machine, launcher string) (res healthResult, got, checked bool) {
	rc, run := s.claimHealthRecheck(ctx, machine, launcher)
	if rc == nil {
		return healthResult{}, false, false
	}
	if run {
		s.runHealthRecheck(ctx, machine, launcher, rc, false)
	}
	select {
	case <-rc.done:
		return rc.res, rc.got, true
	case <-ctx.Done():
		return healthResult{}, false, false
	}
}

// claimHealthRecheck reserves one probe for both a failed start and the detour that follows it. A nil result is
// throttled; run means the caller owns the probe, otherwise it joins the already reserved one.
func (s *Service) claimHealthRecheck(ctx context.Context, machine, launcher string) (rc *healthRecheck, run bool) {
	b := &s.health
	key := healthKey(machine, launcher)
	b.mu.Lock()
	defer b.mu.Unlock()
	s.healthLoadLocked(ctx)
	if rc := b.recheck[key]; rc != nil {
		rc.waiters++
		return rc, false
	}
	if at := b.recheckAt[key]; !at.IsZero() && time.Since(at) < healthRecheckThrottle {
		return nil, false
	}
	rc = &healthRecheck{done: make(chan struct{})}
	b.recheck[key] = rc
	b.recheckAt[key] = time.Now()
	return rc, true
}

// runHealthRecheck publishes the health result before releasing waiters. A start's follow-up records failures too;
// an on-demand detour also records usage-limit failures so a later reset is not discarded. Other failures keep the
// periodic repair cadence.
func (s *Service) runHealthRecheck(ctx context.Context, machine, launcher string, rc *healthRecheck, recordFailure bool) {
	defer func() {
		s.health.mu.Lock()
		delete(s.health.recheck, healthKey(machine, launcher))
		close(rc.done)
		s.health.mu.Unlock()
	}()
	rc.res, rc.got = s.probeLauncherHealth(ctx, machine, launcher)
	if rc.got && (rc.res.ok || recordFailure || classifyLauncherError(rc.res.err) == errKindUsageLimit) {
		s.recordHealth(ctx, machine, launcher, rc.res)
	}
}

// launcherUnusableNow is launcherUnusable after one on-the-spot probe of a failing record, for a start about to
// move or be refused because of it: the record can lag behind a fix, so a pass clears it and the start stays on the
// asked machine (#2018). A usage limit can be over before its reset, or with its reset unknown (#9386): it is probed
// too, once its evidence is healthRecheckThrottle old, and the claim's throttle spaces the probes after that. A
// terminal-shell failure cannot be settled by the probe's login shell; only a real start clears it.
func (s *Service) launcherUnusableNow(ctx context.Context, machine, launcher string) (LauncherHealth, bool) {
	r, bad := s.launcherUnusable(ctx, machine, launcher)
	if !bad || r.Kind == errKindNotInTerminal {
		return r, bad
	}
	if r.Kind == errKindUsageLimit && time.Since(r.CheckedAt) < healthRecheckThrottle {
		return r, bad
	}
	s.recheckLauncherNow(ctx, machine, launcher)
	// The shared follow-up may have finished between the first read and the throttled claim. Decide from its
	// published result, not the stale failure that led us here.
	return s.launcherUnusable(ctx, machine, launcher)
}

// healthDetour is where a start with launcher on machine m goes instead: another machine when its last check failed
// there and another has it healthy (moved), with a line saying so for whoever started it; no other: m, and the line
// says it may fail. A usage limit with a known reset says when that is, and a saturated account pool (capacity.go)
// adds the fleet-wide "no schedulable account" fact and its nearest recovery when the data is fresh.
func (s *Service) healthDetour(ctx context.Context, m Device, launcher string, suitable ...func(Device) bool) (Device, string, bool) {
	r, bad := s.launcherUnusableNow(ctx, m.ID, launcher)
	if !bad {
		return m, "", false
	}
	placementOf(ctx).hit(m.ID, "launcher_unhealthy", i18n.Args{"launcher": launcher, "kind": r.Kind})
	alt, moved := s.healthyMachine(ctx, launcher, m.ID, suitable...)
	if p := placementOf(ctx); p != nil && moved {
		p.ReasonKey = "healthy_launcher"
	}
	// The pool is provider-level: resolve it once from the failed machine. Missing or stale data gives no note, and
	// a pool with any schedulable account stays quiet too. Read only after the on-the-spot recheck, since a passing
	// probe returns above without any detour line.
	poolNote := poolSuffix(s.capacityNote(ctx, s.launcherProvider(ctx, m.ID, launcher)))
	if note := r.resetNote(); note != "" {
		what := fmt.Sprintf("launcher %s is at its subscription usage limit on %s (%s)", launcher, m.ID, note)
		if moved {
			return alt, what + poolNote + fmt.Sprintf(", so this runs on %s instead", alt.ID), true
		}
		return m, what + poolNote + " and no other machine has it healthy; started there anyway", false
	}
	why := fmt.Sprintf("%s: %s", r.Kind, clip(r.Error, 160))
	if moved {
		return alt, fmt.Sprintf("launcher %s failed its health check on %s (%s), so this runs on %s instead%s", launcher, m.ID, why, alt.ID, poolNote), true
	}
	return m, fmt.Sprintf("launcher %s failed its last health check on %s (%s) and no other machine has it healthy; started there anyway%s", launcher, m.ID, why, poolNote), false
}

// healthLines are agents_list's lines about the launchers that do not work (machine: only there).
func (s *Service) healthLines(ctx context.Context, machine string) string {
	var b strings.Builder
	for _, r := range s.healthRecords(ctx, "", machine) {
		if !r.unusable() {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("Launchers that do not work now (agent_start / task_plan move off them):\n")
		}
		fmt.Fprintf(&b, "- machine=%s launcher=%s · %s · %s: %s · checked %s", r.Machine, r.Launcher, r.State, r.Kind, clip(r.Error, 200), ago(r.CheckedAt))
		if note := r.resetNote(); note != "" {
			b.WriteString(" · " + note)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// launcherWithHealth is a launcher of the "New Agent" picker with how its last check went on that machine. Its
// display_name is the launcher's own (proto.Launcher.DisplayName): a runtime's name for its provider (#8261), which a
// Persona-managed provider's own name replaces (launcher_auto_name).
type launcherWithHealth struct {
	ConfiguredLauncher
	Health *launcherHealthView `json:"health,omitempty"`
}

type launcherHealthView struct {
	State     string     `json:"state"`
	Kind      string     `json:"kind,omitempty"`
	Error     string     `json:"error,omitempty"`
	CheckedAt time.Time  `json:"checked_at"`
	ResetsAt  *time.Time `json:"resets_at,omitempty"` // a usage limit's reset: unusable until then
}

// withHealth puts machine's health records on its launchers (none: never checked) and leaves out the ones that do not
// work now. The launcher pickers list only what a start can use: a launcher whose latest check failed (failing,
// repairing or blocked within healthStale, a usage limit until its reset) is hidden until a check passes again. A
// launcher never checked stays: no result is not a failure, and launcher health is off unless the manager turns it on.
//
// Two stay listed with their record: a not_in_terminal launcher (its checks pass, only a real start clears it, and
// hiding it would leave no way to make that start) and keep, the launcher the configuration runs with no pick. That one
// will be used by the next start, so the form must be able to say why it may fail (clients mark the Default row and its
// line); hiding it would fail the start with no warning at all.
func (s *Service) withHealth(ctx context.Context, machine string, list []ConfiguredLauncher, keep string) (out []launcherWithHealth, left []unhealthyLauncher) {
	out = make([]launcherWithHealth, 0, len(list))
	for _, l := range list {
		v := launcherWithHealth{ConfiguredLauncher: l}
		if r, ok := s.launcherHealth(ctx, machine, l.Name); ok {
			if r.unusable() && r.Kind != errKindNotInTerminal && l.Name != keep {
				left = append(left, unhealthyLauncher{ConfiguredLauncher: l, record: r})
				continue
			}
			if r.State == healthOK || r.unusable() { // not a failure too old to count
				v.Health = &launcherHealthView{State: r.State, Kind: r.Kind, Error: r.Error, CheckedAt: r.CheckedAt}
				if reset, limited := r.limitReset(); limited {
					v.Health.ResetsAt = &reset
				}
			}
		}
		out = append(out, v)
	}
	return out, left
}

// unhealthyLauncher is a launcher withHealth left out of a picker, with the record that says why.
type unhealthyLauncher struct {
	ConfiguredLauncher
	record LauncherHealth
}

// unhealthyUnavailable lists the launchers withHealth left out as New Agent's unavailable ones (#14535): listed off,
// with why in lang, rather than missing, so the person can tell a failing launcher from one the computer does not have.
// The reason is the failure's class on where, then the record's own line when it has one.
func unhealthyUnavailable(lang i18n.Lang, where string, left []unhealthyLauncher) []unavailableLauncher {
	out := make([]unavailableLauncher, 0, len(left))
	for _, l := range left {
		reason := i18n.T(lang, unhealthyReasonKey(l.record.Kind), i18n.Args{"machine": where})
		if line := textfmt.Clip(textfmt.OneLine(l.record.Error), unhealthyLineMax); line != "" {
			reason = i18n.T(lang, "assistant-model_pick.unhealthy_with_error", i18n.Args{"reason": reason, "error": line})
		}
		out = append(out, unavailableLauncher{Name: l.Name, DisplayName: l.DisplayName, Kind: "launcher",
			Agent: cmp.Or(l.AgentType, l.Agent), Code: unavailableUnhealthy, Reason: reason})
	}
	return out
}

// unhealthyLineMax bounds the record's line in a picker row's reason; the record keeps the whole of it.
const unhealthyLineMax = 160

// unhealthyReasonKey is the catalog key that words a failure class in a picker row; an unknown class is "other".
func unhealthyReasonKey(kind string) string {
	switch kind {
	case errKindAuth:
		return "assistant-model_pick.unhealthy_auth"
	case errKindModelRoute:
		return "assistant-model_pick.unhealthy_model_route"
	case errKindCLIOutdated:
		return "assistant-model_pick.unhealthy_cli_outdated"
	case errKindTimeout:
		return "assistant-model_pick.unhealthy_timeout"
	case errKindUsageLimit:
		return "assistant-model_pick.unhealthy_usage_limit"
	case errKindUpstream:
		return "assistant-model_pick.unhealthy_upstream"
	case errKindOnboarding:
		return "assistant-model_pick.unhealthy_onboarding"
	}
	return "assistant-model_pick.unhealthy_other"
}
