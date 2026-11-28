package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/textfmt"
	"github.com/nwverse/persona/server/internal/transcript"
)

// ---- recording ----

var usageLimitModel = lazyre.New(`(?i)\b(opus|sonnet|haiku) limit\b`)

// limitFamilies are the model families whose own usage limit text names ("You've hit your Opus limit").
func limitFamilies(text string) []string {
	var out []string
	for _, m := range usageLimitModel.FindAllStringSubmatch(text, -1) {
		if f := strings.ToLower(m[1]); !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// limitFamiliesLeft are the families of a usage limit that an answer from model does not show over: a default
// model's answer says nothing of another family's quota.
func limitFamiliesLeft(families []string, model string) []string {
	words := strings.FieldsFunc(strings.ToLower(model), func(r rune) bool { return r < 'a' || r > 'z' })
	return slices.DeleteFunc(slices.Clone(families), func(f string) bool { return slices.Contains(words, f) })
}

// foldLimit merges a usage limit on families (none: the limit names no family), resetting at reset when known, into
// dst's limit, never taking anything away: a family named adds to dst's and its reset only gets later; a family's
// unknown reset stays unknown, whatever another family's is; a limit naming no family makes the known resets no
// earlier. It says whether dst changed.
func foldLimit(dst *LauncherHealth, families []string, reset *time.Time) (changed bool) {
	had, hadResets := dst.limitModels(), dst.limitResets()
	have, resets, agg := slices.Clone(had), maps.Clone(hadResets), dst.ResetsAt
	switch {
	case len(families) == 0 && len(have) == 0:
		agg = laterReset(agg, reset)
	case len(families) == 0:
		for f, t := range resets {
			resets[f] = *laterReset(&t, reset)
		}
	default:
		if len(have) == 0 && reset != nil {
			reset = laterReset(reset, dst.ResetsAt) // the limit that named no family holds until its own reset too
		}
		for _, f := range families {
			if !slices.Contains(have, f) {
				have = append(have, f)
			}
			if t, ok := resets[f]; ok && !t.After(time.Now()) {
				delete(resets, f) // its reset came and went: this limit is a new one
			}
			if t, ok := resets[f]; reset != nil && (!ok || reset.After(t)) {
				resets[f] = *reset
			}
		}
	}
	if len(have) > 0 {
		agg = familiesReset(have, resets)
	}
	if len(resets) == 0 {
		resets = nil
	}
	// Compared as read (limitModels, limitResets), so a record of an older format that learns nothing is unchanged.
	changed = !slices.Equal(have, had) || !maps.EqualFunc(resets, hadResets, time.Time.Equal) || !sameReset(agg, dst.ResetsAt)
	dst.LimitModels, dst.LimitResets, dst.ResetsAt = have, resets, agg
	return changed
}

func laterReset(a, b *time.Time) *time.Time {
	if a == nil || b != nil && b.After(*a) {
		return b
	}
	return a
}

func sameReset(a, b *time.Time) bool { return a == b || a != nil && b != nil && a.Equal(*b) }

// healthResult is one check's or start's outcome.
type healthResult struct {
	ok           bool
	agent, model string
	millis       int
	err          string
	source       string
	kind         string // the error's class when the outcome says it (a start's coded refusal); "": classifyLauncherError
	// resetsAt: a usage limit's reset as the outcome's source knew it (an agent's own record of the limit); nil: read
	// off err's words when they name a time zone.
	resetsAt *time.Time
	// at: when the outcome happened, when that was before it is recorded (an agent's turn whose usage limit has no
	// known reset: its transcript's time, launcherLimitTurnSettled); zero: now. The record's evidence is that old.
	at time.Time
	// observed is the record as read before a probe (nil: not a probe's result); a later version of it (another check
	// time or rev) makes that probe's result obsolete.
	observed *LauncherHealth
}

// Where a result comes from (LauncherHealth.Source).
const (
	healthSourceCheck = "check" // launchers.check, in the node's login shell
	healthSourceStart = "start" // an agent's start, in a terminal tab
	// healthSourceSession: an agent's own turn with the launcher ended on the subscription's usage limit
	// (launcherLimitTurnEnded). Nothing was run for it: no check, no repair.
	healthSourceSession = "session"
)

// terminalBlockedReason is why a launcher the terminal cannot run is blocked: no repair reaches the terminal's startup files.
const terminalBlockedReason = "not available in this computer's terminal: its shell reads other startup files than the login shell " +
	"Persona lists and checks launchers in (another shell, a container, an rc file that returns early). Define the launcher where " +
	"the terminal's shell reads it, or add it to Persona as a model provider; a start with it that works clears this"

// recordHealth keeps r as launcher's latest on machine; the apps are told when its state changes.
//
// A usage limit in force changes only by the design table in docs/server/launcher-health.md ("Usage-limit record
// rules", #9386): a failure folds into it (foldLimit), a pass clears its answering model's families, an older result
// only folds, any other failure leaves it, and every change advances the record's rev.
func (s *Service) recordHealth(ctx context.Context, machine, launcher string, r healthResult) LauncherHealth {
	b := &s.health
	b.mu.Lock()
	s.healthLoadLocked(ctx)
	key := healthKey(machine, launcher)
	rec := b.recs[key]
	limited := rec != nil && rec.Kind == errKindUsageLimit && rec.unusable()
	// The raw error is what the terminal or the CLI printed, colour, prompt marks and shell startup noise included:
	// clean it once, before anything reads its words (the classification among them).
	cleanErr := r.err
	if cleanErr != "" {
		cleanErr = textfmt.LauncherErrorSummary(cleanErr, 400)
	}
	kind := ""
	if !r.ok {
		if kind = r.kind; kind == "" {
			kind = classifyLauncherError(cleanErr)
		}
	}
	keep := func(changed bool) LauncherHealth {
		var out LauncherHealth
		if rec != nil {
			out = *rec
		}
		if changed {
			b.rev++
			rec.rev, out.rev = b.rev, b.rev
			s.healthSaveLocked(ctx)
		}
		b.mu.Unlock()
		return out
	}
	now := time.Now().UTC()
	at := now // when r happened (healthResult.at)
	if !r.at.IsZero() && r.at.Before(now) {
		at = r.at.UTC()
	}
	if o := r.observed; o != nil && (rec == nil && !o.CheckedAt.IsZero() || rec != nil && (!rec.CheckedAt.Equal(o.CheckedAt) || rec.rev != o.rev)) {
		return keep(limited && kind == errKindUsageLimit && foldLimit(rec, limitFamilies(cleanErr), limitResetOf(r, now)))
	}
	if limited && !r.ok && kind != errKindUsageLimit {
		return keep(false)
	}
	if r.ok && limited {
		if have := rec.limitModels(); len(limitFamiliesLeft(have, r.model)) > 0 {
			left, resets := limitFamiliesLeft(have, r.model), rec.limitResets()
			shrunk := len(left) < len(have)
			if shrunk {
				maps.DeleteFunc(resets, func(f string, _ time.Time) bool { return !slices.Contains(left, f) })
				if len(resets) == 0 {
					resets = nil
				}
				rec.LimitModels, rec.LimitResets, rec.ResetsAt = left, resets, familiesReset(left, resets)
				rec.CheckedAt, rec.Source = now, r.source
			}
			return keep(shrunk)
		}
	}
	if rec == nil {
		rec = &LauncherHealth{Machine: machine, Launcher: launcher}
		b.recs[key] = rec
	}
	before, old := rec.State, *rec
	if r.source != healthSourceStart && rec.Kind == errKindNotInTerminal && rec.unusable() {
		// The check runs in the login shell, where the launcher works; the terminal is what cannot run it. Only a
		// start says otherwise (#1642): a session already running says nothing of a new terminal tab.
		return keep(false)
	}
	rec.CheckedAt, rec.Source, rec.Millis = at, r.source, r.millis
	if limited && old.CheckedAt.After(at) {
		rec.CheckedAt = old.CheckedAt // folded into a limit with newer evidence: its evidence is still that new
	}
	if r.agent != "" {
		rec.Agent = r.agent
	}
	rec.ResetsAt, rec.LimitModels, rec.LimitResets = nil, nil, nil
	if r.ok {
		rec.State, rec.Kind, rec.Error, rec.Fails, rec.Repairs, rec.BlockedReason = healthOK, "", "", 0, 0, ""
		rec.MissingFails = 0
		rec.Model = r.model
	} else {
		rec.Fails++
		if missingCode(cleanErr) == proto.CodeClaudeMissing {
			rec.MissingFails++
		} else {
			rec.MissingFails = 0
		}
		rec.Error = clip(oneLine(cleanErr), 400)
		rec.Kind = kind
		switch {
		case rec.Kind == errKindNotInTerminal:
			rec.State, rec.BlockedReason = healthBlocked, terminalBlockedReason // no repair round (repairMachine takes failing ones)
		case rec.Kind == errKindUsageLimit:
			// The request got through to the subscription, whose limit is used up: nothing is broken, so no repair
			// is due (repairNow) and a block for an earlier error no longer stands. The reset clears it.
			rec.State, rec.BlockedReason = healthFailing, ""
			merged := LauncherHealth{}
			if limited {
				merged = old
			}
			foldLimit(&merged, limitFamilies(cleanErr), limitResetOf(r, at))
			rec.LimitModels, rec.LimitResets, rec.ResetsAt = merged.LimitModels, merged.LimitResets, merged.ResetsAt
		case rec.State == "" || rec.State == healthOK:
			rec.State = healthFailing
		}
	}
	b.rev++
	rec.rev = b.rev
	out := *rec
	s.healthSaveLocked(ctx)
	b.mu.Unlock()
	if before != out.State {
		s.healthChanged(ctx, out, before)
	}
	return out
}

// setHealthState moves launcher on machine to state ("": it stays; why: the blocked reason) and keeps fixed as its
// latest repair, one more round of this run of failures. Nothing changes once a check passed meanwhile, nor for a
// usage limit met meanwhile: it is neither repaired nor blocked, its reset clears it.
func (s *Service) setHealthState(ctx context.Context, machine, launcher, state, why string, fixed *LauncherFixed) {
	b := &s.health
	b.mu.Lock()
	s.healthLoadLocked(ctx)
	rec := b.recs[healthKey(machine, launcher)]
	if rec == nil || rec.State == healthOK || rec.Kind == errKindUsageLimit {
		b.mu.Unlock()
		return
	}
	before := rec.State
	if state != "" {
		rec.State = state
	}
	if state == healthBlocked {
		rec.BlockedReason = clip(oneLine(textfmt.StripTerminalEscapes(why)), 600)
	}
	if fixed != nil {
		f := *fixed
		rec.Repair = &f
		rec.Repairs++
	}
	out := *rec
	s.healthSaveLocked(ctx)
	b.mu.Unlock()
	if before != out.State {
		s.healthChanged(ctx, out, before)
	}
}

// repairWorked marks launcher's latest repair on machine as the one that fixed it (its check passed after).
func (s *Service) repairWorked(ctx context.Context, machine, launcher string, fixed LauncherFixed) {
	b := &s.health
	b.mu.Lock()
	defer b.mu.Unlock()
	s.healthLoadLocked(ctx)
	if rec := b.recs[healthKey(machine, launcher)]; rec != nil && rec.State == healthOK {
		fixed.OK = true
		rec.Repair = &fixed
		s.healthSaveLocked(ctx)
	}
}

// healthChanged logs a launcher's new state and tells the apps (an event, not a push).
func (s *Service) healthChanged(ctx context.Context, r LauncherHealth, before string) {
	if before == "" {
		before = "new"
	}
	if r.State == healthOK {
		s.logf("launcher health: %s on %s: %s → ok", r.Launcher, r.Machine, before)
	} else {
		s.logf("launcher health: %s on %s: %s → %s (%s: %s)", r.Launcher, r.Machine, before, r.State, r.Kind, clip(r.Error, 160))
	}
	s.event(ctx, map[string]any{"type": "launcher_health", "record": r})
}

// recordStartFailure: a dropped link, refused request, stalled agent or a start whose own wait ran out says nothing
// about launcher health. Other failures count, with one shared follow-up check to tell a blink from a broken launcher.
// A terminal-shell failure (errKindNotInTerminal) skips the check: the login shell is where the launcher already works.
func (s *Service) recordStartFailure(ctx context.Context, machine, launcher string, err error) {
	if err == nil || launcher == "" || proto.IsExternalLauncher(launcher) || transientStartErr(err) || errors.Is(err, ErrNoDevice) || startRefused(err) || startStalled(err) {
		return
	}
	if startWaitEnded(err) {
		// A failing record here would stand until someone rechecked by hand, and refuse every fixed-machine start.
		s.logf("launcher health: %s on %s: start ended without an answer (%v); not counted against the launcher", launcher, machine, err)
		return
	}
	if terminalMissing(err) {
		s.recordHealth(ctx, machine, launcher, healthResult{err: err.Error(), source: healthSourceStart, kind: errKindNotInTerminal})
		return
	}
	s.recordHealth(ctx, machine, launcher, healthResult{err: err.Error(), source: healthSourceStart, kind: codedLauncherErrorKind(err)})
	// Reserve before launching the goroutine: a task's immediate detour must join this check, even before it runs.
	if rc, run := s.claimHealthRecheck(ctx, machine, launcher); run {
		go s.runHealthRecheck(context.WithoutCancel(ctx), machine, launcher, rc, true)
	}
}

// recordStartSuccess: an agent started with launcher on machine, so the terminal there runs it: a record saying it
// does not (errKindNotInTerminal) is cleared. Any other record stays: a start that came up says nothing of the model
// route or the key a check found broken.
func (s *Service) recordStartSuccess(ctx context.Context, machine, launcher string) {
	if launcher == "" {
		launcher = "claude"
	}
	if r, ok := s.launcherHealth(ctx, machine, launcher); !ok || r.Kind != errKindNotInTerminal || r.State == healthOK {
		return
	}
	s.recordHealth(ctx, machine, launcher, healthResult{ok: true, source: healthSourceStart})
}

// recordTestPassed: the apps' Test availability (POST /api/v1/launchers/{name}/check) passed for launcher on machine.
// It runs the same launchers.check a health check does, so a record of the launcher failing there is taken back to ok
// as a passing check would (#1838); recordHealth keeps a not_in_terminal record, which only a start clears. Without a
// failing record nothing is kept (a launcher the checks do not cover gets no entry), and a failed Test is not recorded:
// the checks and the starts judge failures, and testing a launcher still being set up must not move starts off it.
func (s *Service) recordTestPassed(ctx context.Context, machine, launcher string, res proto.LauncherCheck, observed LauncherHealth) {
	if r, ok := s.launcherHealth(ctx, machine, launcher); !ok || r.State == healthOK {
		return
	}
	s.recordHealth(ctx, machine, launcher, healthResult{ok: true, agent: res.Agent, model: res.Model, millis: res.Millis, source: healthSourceCheck, observed: &observed})
}

// launcherSetupChanged: a save of a managed launcher changed what decides whether its requests get through (its key,
// gateway, auth, model, headers, environment or the computer it sends through), so how it failed before says nothing
// of it now.
func launcherSetupChanged(old, saved storedLauncher) bool {
	return old.KeySealed != saved.KeySealed || old.BaseURL != saved.BaseURL || old.Auth != saved.Auth || old.Model != saved.Model ||
		old.HeadersSealed != saved.HeadersSealed || old.EnvSealed != saved.EnvSealed || old.ViaMachine != saved.ViaMachine
}

// forgetLauncherFailures drops launcher's failure records on every machine after its setup changed (#1838): it counts
// as never checked there again, until the next check or start says otherwise. Two records stay, as the health rules
// keep them: not_in_terminal (the terminal's shell is not the provider's; only a start clears it) and a usage limit
// with a known reset (it holds until then).
func (s *Service) forgetLauncherFailures(ctx context.Context, launcher string) {
	b := &s.health
	b.mu.Lock()
	s.healthLoadLocked(ctx)
	var dropped []string
	for key, r := range b.recs {
		if r.Launcher != launcher || r.State == healthOK || r.Kind == errKindNotInTerminal {
			continue
		}
		if _, limited := r.limitReset(); limited && r.unusable() {
			continue
		}
		delete(b.recs, key)
		dropped = append(dropped, r.Machine)
	}
	if len(dropped) > 0 {
		s.healthSaveLocked(ctx)
	}
	b.mu.Unlock()
	if len(dropped) > 0 {
		slices.Sort(dropped)
		s.logf("launcher health: %s: setup changed, failure records dropped on %s", launcher, strings.Join(dropped, ", "))
	}
}

// limitResetOf is when the usage limit r failed on resets, as r's source knew it or as its words say (at: when it
// was recorded); nil when unknown or further off than healthLimitMax.
func limitResetOf(r healthResult, at time.Time) *time.Time {
	reset := time.Time{}
	if r.resetsAt != nil {
		reset = *r.resetsAt
	} else if t, ok := transcript.UsageLimitReset(r.err, at); ok {
		reset = t
	}
	if reset.IsZero() || reset.Sub(at) > healthLimitMax {
		return nil
	}
	reset = reset.UTC()
	return &reset
}

// launcherLimitTurnEnded: a turn of machine's session sid ended. When it ended on the subscription's usage limit,
// the launcher the session names counts as usage-limited on machine (source healthSourceSession): until the reset
// when it is known and still ahead, else until healthStale after the turn (#9646). An agent started with it there
// would be refused at once too. Nothing runs for it, no check (it would spend a request to be refused) and no repair.
// The assistant's conversations count as well: the main chat runs the machine's launcher under the same sign-in, so
// its limit is the launcher's.
func (s *Service) launcherLimitTurnEnded(ctx context.Context, machine, sid string) {
	// Once the turn stayed still, as a task's end is judged (settleDelay): right after a write the node's list may not
	// have read the transcript yet (its meta is read again at most every few seconds), and says no error meanwhile.
	time.Sleep(s.settleDelay(ctx))
	s.launcherLimitTurnSettled(ctx, machine, sid)
}

// launcherLimitTurnSettled is launcherLimitTurnEnded once the turn stayed still: after its sleep, or afterSettle's
// timer.
func (s *Service) launcherLimitTurnSettled(ctx context.Context, machine, sid string) {
	// As the node lists it, when the fleet is here: the fields below are the node's, and the decorated copy
	// session.get answers with costs a pass over the machine's whole list.
	sess, ok := s.nodeSession(machine, sid)
	if !s.fleetOn() {
		sess, ok = s.session(ctx, machine, sid)
	}
	if !ok || sess.Launcher == "" {
		return
	}
	lim := limitOfTurn(sess)
	if lim.TurnErrorKind != proto.TurnErrorUsageLimit || lim.TurnError == "" {
		return
	}
	now := time.Now()
	res := healthResult{err: lim.TurnError, source: healthSourceSession, kind: errKindUsageLimit}
	if reset, ok := usageLimitReset(lim); ok && reset.Sub(now) <= healthLimitMax {
		if !reset.After(now) {
			return // the limit it met is over
		}
		res.resetsAt = &reset
	} else {
		// No reset known, or one too far off to be read right (limitResetOf): the turn's families are limited with an
		// unknown reset (#9646), and its evidence is as old as the turn. It goes stale as any failure without a reset
		// does, healthStale after the turn: a turn the hub reads late says that much less of the quota now, and one
		// older than that says nothing.
		res.at = lim.UpdatedAt
		if res.at.IsZero() || res.at.After(now) {
			res.at = now
		}
		if now.Sub(res.at) >= healthStale {
			return
		}
	}
	// Nothing new for the limit in force (foldLimit changes nothing on the families the turn names): each turn a
	// background task wakes meets it again, and refreshing the evidence would hold off the next probe. A turn with no
	// reset known is new while the record's evidence is stale (another family's known reset holds it,
	// familyResetAhead): the turn's families hold on that evidence alone.
	if r, found := s.launcherHealth(ctx, machine, sess.Launcher); found && r.Kind == errKindUsageLimit && r.unusable() &&
		!foldLimit(&r, limitFamilies(lim.TurnError), res.resetsAt) && (res.resetsAt != nil || time.Since(r.CheckedAt) < healthStale) {
		return
	}
	s.recordHealth(ctx, machine, sess.Launcher, res)
}

// limitOfTurn is sess's latest turn error decorated with what it says of a usage limit (decorateLimit): the node's
// word, else Claude Code's last words when they are the error. Whatever sess's state now, unlike the lists'
// turnError: a background task wakes an agent its limit stopped again and again, and it counts as working meanwhile.
func limitOfTurn(sess proto.Session) proto.Session {
	out := proto.Session{TurnErrorResetsAt: sess.TurnErrorResetsAt, UpdatedAt: sess.UpdatedAt}
	out.TurnError, out.TurnErrorKind = latestTurnError(sess)
	decorateLimit(&out)
	return out
}

// terminalMissing: err is the node's refusal of a start whose new terminal tab could not run the launcher or its claude
// (launcher_missing / claude_missing with where "terminal", node/shellmiss.go, #1642).
func terminalMissing(err error) bool {
	var re *rpc.Error
	if !errors.As(err, &re) || (re.Code != proto.CodeLauncherMissing && re.Code != proto.CodeClaudeMissing) {
		return false
	}
	var data struct {
		Where string `json:"where"`
	}
	return json.Unmarshal(re.Data, &data) == nil && data.Where == "terminal"
}

// startRequestErr are the node's answers to a session.start it would not run, whatever the launcher (node/control.go
// checkStart and start, node/model_config.go check): a directory that is not there, a bad name, mode, effort or model.
var startRequestErr = lazyre.New(`^directory .* does not exist$|^invalid launcher |^invalid session id |^invalid model$|^unknown permission mode |^unknown effort level |^unsupported agent type |^permission mode and effort are only supported for Claude starts|^普通 Agent 的启动只支持|^新建 Codex 的思考强度由其启动器管理`)

// startRefusalCodes are coded refusals of the request itself by a runtime, the node, or the hub (node/control.go,
// node/codex_start.go, node/openelsewhere.go…). Codes about the machine's state (engine_missing, runtime_away,
// not_running, orca_not_registered) are not here: the launcher cannot run there now, and a task may go elsewhere.
// An engine with no provider of the launcher's kind (launcher_kind_missing, #8140) is the request's: the launcher is
// not broken, and its check, which runs it on the command line, would pass and clear any record of it.
var startRefusalCodes = []string{"bad_request", "codex_needs_prompt", "codex_resume", "codex_read_only", "orca_resume", "open_elsewhere", "runtime_session",
	proto.CodeResumeUnknown, "identity_conflict", "ultracode_unsupported", "readonly_unsupported", proto.CodeDirectoryUntypable, proto.CodeNodeOutdated,
	proto.CodeLauncherKindMissing, proto.CodeAgyCompanionOffline}

// startRefused: the start failed on the request, not the launcher: the node checked it and said no before running
// anything (one of startRefusalCodes, or of startRequestErr).
func startRefused(err error) bool {
	var re *rpc.Error
	if !errors.As(err, &re) {
		return false
	}
	if re.Code != "" {
		return slices.Contains(startRefusalCodes, re.Code)
	}
	return startRequestErr.MatchString(re.Message)
}

// startWaitEnded: the start's own context ran out or was cancelled before the node answered (its link stuck, a node
// that never replied within taskStartTimeout), here or on the hub that relayed the call (an uncoded rpc.Error with the
// context's words). The hub stopped waiting and learned nothing of the launcher. A node's own words about a timeout
// ("session.start: context deadline exceeded", errKindTimeout) are not this and still count.
func startWaitEnded(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == "" &&
		(re.Message == context.DeadlineExceeded.Error() || re.Message == context.Canceled.Error())
}

// startStalled is a node's coded timeout after opening the agent, or a start that ended in the new tab's shell before
// the launcher ran (a startup question, a start line the shell did not take; node/shellsettle.go, #2570): none says
// anything of the launcher. Text from older nodes deliberately keeps the previous failure handling; guessing from
// translated messages could suppress genuine launcher failures.
func startStalled(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && (re.Code == proto.CodeAgentStartStalled || re.Code == proto.CodeShellWaiting ||
		re.Code == proto.CodeStartLineRejected)
}
