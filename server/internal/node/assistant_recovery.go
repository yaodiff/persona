package node

import (
	"cmp"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/modelretry"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A failed Claude turn is recovered before its idle state reaches the manager and the scheduled-run watcher. The
// budget belongs to the conversation, not assistantProc: selecting another model restarts that process.
type assistantTurnRecovery struct {
	retried  map[string]bool // one same-model continuation per model
	used     map[string]bool // models this request has already tried, never selected again
	proc     *assistantProc  // working-status placeholder while a model switch has no registered process
	done     chan struct{}   // non-nil while runAssistantRecovery can still send; cleared after its final state update
	running  bool
	finished bool // a result completed while handover still owned the process; adopt must not migrate it
	pending  *assistantPendingFailure
	// launcher and from: what the conversation was asked to run when this chain began; switched: it moved to another
	// model. probeFrom is the first concrete identity of a requested launcher default, captured once for this chain.
	// A switched chain that answers holds the conversation there (assistant_fallback_hold.go). Under Node.mu.
	launcher  string
	from      modelExecution
	probeFrom string
	switched  bool
	// lastResult and lastReset are the most recent failure's words and any recovery time they named, kept so the
	// model-recovery marker (#7824) can say why the chain switched and when the configured model is back.
	lastResult string
	lastReset  *time.Time
	// triedOrder keeps r.used in first-use order for the marker's tried-model list.
	triedOrder []string
	// toolSearchRetried: the turn was resent once in a new process with every tool loaded after its endpoint refused
	// tool search (#10365). Its own budget: the same-model and next-model ones stay as they were.
	toolSearchRetried bool
}

// newAssistantRecovery is a conversation's chain begun by a failure of launcher running execution (key and current as
// recoverAssistantResultFor has them), stored on n. Refused again soon after a fallback answered, the same-model retry
// is spent already: a resend carries the refused turns again (#3788). n.mu is held.
func (n *Node) newAssistantRecovery(id, launcher string, execution modelExecution, key, current, replyModel string) *assistantTurnRecovery {
	r := &assistantTurnRecovery{retried: map[string]bool{}, used: map[string]bool{}, launcher: launcher, from: execution}
	if execution.Model == "" || execution.Model == "default" {
		r.probeFrom = replyModel // an unresolved launcher default may still name its concrete model in message_start
	}
	if current != "" {
		r.used[current] = true
		r.triedOrder = append(r.triedOrder, current)
	}
	if from, l, ok := n.recentFallback(id); ok && l == launcher && !execution.NewModelChoice {
		held := n.fallbacks[id]
		if sameModelChoice(execution, from) || held != nil && sameModelChoice(execution, held.to) {
			r.from = from // what the user's setting asks for, not the fallback a failure there came from
			r.retried[key] = true
		}
	}
	if n.recoveries == nil {
		n.recoveries = map[string]*assistantTurnRecovery{}
	}
	n.recoveries[id] = r
	return r
}

// assistantPendingFailure is a new turn result that arrived while its recovery send was still returning. It is not a
// duplicate of the previous result: swallowing it would leave the conversation working forever.
type assistantPendingFailure struct {
	text  string
	kinds []string
}

type assistantRecoveryContextKey struct{}

// assistantModelServiceFailure is an internal second error-kind entry: Codex's structured error proved a 429/5xx even
// when its human-readable message contains no status. It is not published as the session's TurnErrorKind.
const assistantModelServiceFailure = "model-service"

// assistantRecordKind is an internal second error-kind entry: the agent's record classified the failed turn, so its
// kind (kinds[0], "" for none) is the whole answer on a usage limit and the words are not read for one (#3788).
const assistantRecordKind = "record"

type assistantRecoveryCall struct {
	recovery *assistantTurnRecovery
	restart  bool
}

var (
	errAssistantRecoveryCancelled = errors.New("assistant recovery superseded")
	errAssistantRecoveryTasks     = errors.New("assistant background tasks are running or not explicitly known to be empty")
)

func withAssistantRecovery(ctx context.Context, recovery *assistantTurnRecovery, restart bool) context.Context {
	return context.WithValue(ctx, assistantRecoveryContextKey{}, assistantRecoveryCall{recovery: recovery, restart: restart})
}

func assistantRecoveryOf(ctx context.Context) (*assistantTurnRecovery, bool) {
	call, _ := ctx.Value(assistantRecoveryContextKey{}).(assistantRecoveryCall)
	return call.recovery, call.restart
}

// noteAssistantRecoveryProcess follows the current process through a model replacement. During keeper handover it
// distinguishes a current result from a replaced process's late result; the latter must not overwrite pending work.
func (n *Node) noteAssistantRecoveryProcess(ctx context.Context, p *assistantProc) {
	recovery, _ := assistantRecoveryOf(ctx)
	if recovery == nil || p == nil {
		return
	}
	n.mu.Lock()
	if n.recoveries[p.id] == recovery {
		recovery.proc = p
	}
	n.mu.Unlock()
}

var (
	assistantServerError = lazyre.New(`\b5[0-9][0-9]\b`)
	assistantRateLimit   = lazyre.New(`\b429\b`)
	assistantClientError = lazyre.New(`\b4(?:0[0-9]|1[0-9]|2[0-8]|[3-9][0-9])\b`) // every 4xx but 429
)

// assistantRecoverableError is deliberately narrower than the Agents' transient-continuation rule. Only the failure
// classes in #3204 replay a request: an upstream safeguards refusal, throttling, or a 5xx. In particular, the hub's
// own 400s, authentication, model routing, usage limits and over-context requests remain the user's to fix. limitWords:
// the words may tell a usage limit, as they must when no record classified the failure. With a record, a gateway's
// "upstream account quota usage limit has been reached" 429 is an ordinary throttle another model gets past (#3788).
func assistantRecoverableError(failed bool, result string, limitWords bool) bool {
	if !failed {
		return false
	}
	t := strings.ToLower(strings.TrimSpace(result))
	if assistantSafeguardsRefusal(t) {
		return true // the provider's transient refusal may itself be an HTTP 400 (#3204)
	}
	if limitWords && (strings.Contains(t, "usage limit") || strings.Contains(t, "usage_limit") || strings.Contains(t, "account_quota_exhausted")) {
		return false // no record to say otherwise: a subscription reset is not an ordinary 429 retry
	}
	if assistantClientError.MatchString(t) {
		return false // a 400, 401, 403…: ours or the user's to fix, even when the detail mentions an upstream 5xx (#3495)
	}
	return assistantRateLimit.MatchString(t) || strings.Contains(t, "rate limit") || strings.Contains(t, "rate_limit") ||
		strings.Contains(t, "too many requests") || assistantServerError.MatchString(t)
}

// assistantSafeguardsRefusal: the provider's safeguards refused the turn. Newer CLIs do not name the safeguard: they
// stop on a frontier model's refusal and tell the user to start a new session, the refused prompt being stuck in the
// resumed context.
func assistantSafeguardsRefusal(result string) bool {
	t := strings.ToLower(result)
	return strings.Contains(t, "safeguards flagged") ||
		strings.Contains(t, "can't help with this") && strings.Contains(t, "start a new session to continue")
}

// assistantModelUnavailable: the model asked for does not exist there, or this account cannot use it (Claude Code's
// "There's an issue with the selected model (…). It may not exist or you may not have access to it.", model_not_found).
func assistantModelUnavailable(result string) bool {
	t := strings.ToLower(result)
	return strings.Contains(t, "issue with the selected model") || strings.Contains(t, "model_not_found") ||
		strings.Contains(t, "model not found") || strings.Contains(t, "model does not exist")
}

// assistantModelRouteRe matches gateway model-routing rejections beyond literal model_not_found strings (matching
// assistant/launcher_health.go's errKindModelRoute).
var assistantModelRouteRe = lazyre.New(`(?i)bare claude model name|cannot be routed|no account match|model[^.\n]{0,60}(not found|not supported|not available|does not exist|unknown)|unknown model|invalid model`)

// assistantModelRouteUnavailable reports whether a failed turn's error says the requested model does not exist or
// cannot be routed on this launcher (model_not_found / model_route), excluding tool-search refusals and transient
// upstream outages/pool exhaustion.
func assistantModelRouteUnavailable(result string) bool {
	if assistantModelUnavailable(result) {
		return true
	}
	if assistantwire.ToolSearchRefused(result) || modelretry.AccountExhausted(result) || modelretry.UpstreamServerError(result) {
		return false
	}
	return assistantModelRouteRe.MatchString(result)
}

// recoverySwitched: conversation id's chain has moved it to another model.
func (n *Node) recoverySwitched(id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	r := n.recoveries[id]
	return r != nil && r.switched
}

// claudeResultKinds is what the transcript says of the failed turn that just ended (transcript.claudeErrorKind): a
// result carries only its text, and a usage limit's text need not say "usage limit" (#3495), nor is every text that
// says it one (#3788). A turn Claude Code says it goes on with by itself after the reset counts as a usage limit:
// typing into it would cancel that. A turn error read there comes with assistantRecordKind. Read fresh, not from the
// list's cache, which keeps a growing transcript's meta for a few seconds.
func (n *Node) claudeResultKinds(id string) []string {
	path := n.transcriptPath(id)
	if path == "" {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	f := readQuickMeta(path, st).turnErr
	if f.resumes {
		return []string{proto.TurnErrorUsageLimit, assistantRecordKind}
	}
	if f.text == "" {
		return nil // no failed turn written (yet): the words decide
	}
	if f.kind == proto.TurnErrorUsageLimit && assistantwire.GatewayQuotaText(f.text) {
		st, _ := n.state.get(id)
		if !n.recoveryProtected(id) || st.TaskExecution {
			// Workers and strict task turns keep their existing bounded same-model retry policy.
			return []string{proto.TurnErrorTransient, assistantRecordKind}
		}
		// Ordinary protected conversations retain the reset wait, without choosing another model.
	}
	return []string{f.kind, assistantRecordKind}
}

// assistantRecoverable: whether a failed turn is replayed, from its words and the kinds the agent's record gives
// (kinds[0] is the session's TurnErrorKind). The structured kind wins over the words (#3495): a usage limit has its
// own continuation that an immediate retry cannot spend back, and authentication, a model route or a bad request are
// the user's. Only a safeguards refusal, which may itself come as a bad request, is replayed whatever the kind.
func assistantRecoverable(failed bool, result string, kinds []string) bool {
	if !failed {
		return false
	}
	if len(kinds) > 0 {
		switch kinds[0] {
		case proto.TurnErrorUsageLimit:
			return false
		case proto.TurnErrorUser:
			if !assistantSafeguardsRefusal(result) {
				return false
			}
		}
	}
	return assistantRecoverableError(failed, result, !slices.Contains(kinds, assistantRecordKind)) ||
		slices.Contains(kinds, assistantModelServiceFailure)
}

// clearAssistantRecovery ends p's chain on a result that needs no recovery. answered: the turn succeeded, so a chain
// that had moved to another model holds the conversation on it (assistant_fallback_hold.go).
func (n *Node) clearAssistantRecovery(p *assistantProc, answered bool) {
	if p == nil || p.id == "" {
		return
	}
	p.mu.Lock()
	launcher, model := p.launcher, p.model
	p.mu.Unlock()
	n.mu.Lock()
	if n.recoveryClosed && n.procs[p.id] != p {
		if r := n.recoveries[p.id]; r != nil && (r.proc == nil || r.proc == p) {
			r.finished = true // the result handler may clear busy only after adopt has inspected it
		}
		owner := p.ownerOr(n)
		n.mu.Unlock()
		if owner != n { // the result handler read the old owner before adoption; finish on the current one
			owner.clearAssistantRecovery(p, answered)
		}
		return
	}
	needPersist := false
	var completed *assistantTurnRecovery
	if n.procs[p.id] == p { // a replaced process's late result cannot clear its replacement's recovery
		r := n.recoveries[p.id]
		completed = r
		if answered && r != nil && r.switched && r.launcher == launcher {
			// Answering on the configured model again ends the recovery marker; answering on the fallback keeps it so
			// the apps keep naming the model that actually answers and why it switched (#7824). n.mu is already held,
			// so do only the locked half; persist and notify after the unlock below (a reentrant lock would deadlock).
			if r.from.Model == model.Model {
				needPersist = n.releaseModelRecoveryLocked(p.id, r.from.Model)
			}
			// Write the hold after any release: a configured-model answer makes a from→from sticky record, which the
			// release's fallback cleanup must not immediately delete (#3788).
			n.holdFallback(p.id, launcher, r.from, model)
		}
		delete(n.recoveries, p.id)
	}
	n.mu.Unlock()
	p.mu.Lock()
	if completed != nil && p.turnRecovery == completed {
		p.turnRecovery = nil // the next queued turn must not inherit the finished turn's chain
	}
	p.mu.Unlock()
	if needPersist {
		n.persistModelRecovery(p.id)
		n.notifySessions()
	}
}

// cancelAssistantRecovery lets a user's newer message take over. Release the old turn's held busy flag too: if that
// newer send is itself refused before delivery, the old conversation must not remain working forever. No idle event is
// published here; a successful new send marks itself working, and a refused one is returned to its caller as before.
// A recovery turn already delivered is answering: its busy is the process's own, and clearing it would let the newer
// message restart the process mid-answer (#3495).
func (n *Node) cancelAssistantRecovery(id string) {
	if id == "" {
		return
	}
	n.mu.Lock()
	r := n.recoveries[id]
	held := r != nil && (r.running || r.pending != nil)
	p := n.procs[id]
	if r != nil && p != nil {
		p.mu.Lock()
		p.turnRecovery = r // cancellation of the map entry does not rewrite this process's busy turn
		p.mu.Unlock()
	}
	delete(n.recoveries, id)
	n.mu.Unlock()
	if held && p != nil {
		p.mu.Lock()
		p.busy = false
		p.mu.Unlock()
	}
}

// recoverAssistantResult reserves this model's same-model retry, or a move to the next model after that retry failed.
// True means the caller must keep the turn working: publishing the error now would let a scheduled run fail (and rerun
// the whole task) before recovery ends.
func (n *Node) recoverAssistantResult(p *assistantProc, failed bool, result string, kinds ...string) bool {
	return n.recoverAssistantResultFor(p, nil, failed, result, kinds...)
}

// recoverAssistantResultFor optionally requires the recovery that captured a pending result to still own the session.
// That identity check prevents a result delayed by process replacement from restarting a turn the user has cancelled.
func (n *Node) recoverAssistantResultFor(p *assistantProc, expected *assistantTurnRecovery, failed bool, result string, kinds ...string) bool {
	if proto.IsExternalLauncher(p.launcher) {
		return false // an explicit adapter pick never enters automatic model recovery
	}
	// A model this chain switched to that the gateway does not have was the recovery's pick, not the user's: it is
	// spent, remembered for the launcher, and the chain goes on to the next one (#4283, #5234). One the user asked for
	// stays theirs to fix, unless it answered here within the hour: then the gateway failed for a moment (#5234).
	missing := failed && assistantModelRouteUnavailable(result)
	unavailable := missing && n.recoverySwitched(p.id)
	p.mu.Lock()
	executionProtected := strictProtectedExecution(p.model)
	strict := p.model.Strict
	transient404 := missing && !unavailable && p.replyModel != "" && !p.repliedAt.IsZero() &&
		time.Since(p.repliedAt) < assistantAnsweredRecently
	p.mu.Unlock()
	if failed && (!strict || modelretry.Permanent(result)) &&
		(modelretry.Temporary(result) || modelretry.Permanent(result) || slices.Contains(kinds, assistantModelServiceFailure) || len(kinds) > 0 && kinds[0] == proto.TurnErrorTransient) &&
		!assistantSafeguardsRefusal(result) && !assistantModelRouteUnavailable(result) && !assistantwire.ToolSearchRefused(result) && ModelRecoveryReason(result) != proto.ModelRecoveryReasonQuota {
		// Ordinary service failures go to the manager's durable scheduler. Strict task turns keep their
		// bounded same-model retry here, where the exact execution and existing retry budget are known.
		return false
	}
	ordinary := unavailable || transient404 || assistantRecoverable(failed, result, kinds)
	// The endpoint refused Claude Code's tool search (#10365): remembered for the launcher and model, so their next
	// start loads every tool, and the turn resent once in such a process, unseen. A process that started that way and
	// is refused again ends the turn on the refusal (nothing loops).
	toolSearch := failed && p.agent == "claude" && !p.toolSearch.forceOff && assistantwire.ToolSearchRefused(result)
	if toolSearch && n.noteToolSearchRefused(p.launcher, p.model.Model, p.endpoint) {
		n.logger.Printf("assistant %s: %s refused tool search for model %q: remembered, its next start loads every tool", short(p.id), p.launcher, p.model.Model)
	}
	// The owner selected this model for the main/supervisor conversation. Quota and service failures belong to
	// the manager's durable wait/continuation path; a refusal must not select another model either. Tool search
	// has one independent same-model restart below, which preserves the selected execution.
	if !strict && !toolSearch && (executionProtected || n.recoveryProtected(p.id)) {
		return false
	}
	if !ordinary && !toolSearch {
		return false
	}
	p.mu.Lock()
	if p.manual || p.compacting {
		p.mu.Unlock()
		return false
	}
	launcher, cwd, execution := p.launcher, p.cwd, p.model
	replyModel := p.replyModel
	busy, turnRecovery := p.busy, p.turnRecovery
	key := execution.Model // the launcher's default stays one budget even when its concrete name arrives on a later attempt
	current := key
	if current == "" {
		current = replyModel // used only to locate that default in the launcher's ordered list
	}
	p.mu.Unlock()

	n.mu.Lock()
	r := n.recoveries[p.id]
	if busy && turnRecovery != nil && (expected == nil || turnRecovery == expected) {
		// The busy process's own chain is authoritative even if a newer request has already entered the map.
		r = turnRecovery
	}
	if r != nil && r.probeFrom == "" && (r.from.Model == "" || r.from.Model == "default") &&
		(execution.Model == "" || execution.Model == "default") {
		r.probeFrom = replyModel // a retry may be the first attempt that names the concrete launcher default
	}
	if n.recoveryClosed {
		if r == nil {
			owner := p.ownerOr(n)
			if owner != n { // a result that read the old owner just before adoption continues on the new one
				n.mu.Unlock()
				return owner.recoverAssistantResultFor(p, expected, failed, result, kinds...)
			}
			// A turn's first failure in the gap: start its budget here, for the keeper to carry it (#3495). Only while the
			// next adopt will see it: the process is not handed to the keeper yet, or the keeper still holds it.
			if expected != nil || n.procs[p.id] != p && (n.keeper == nil || !n.keeper.carryRecovery(p, n)) {
				n.mu.Unlock()
				return false
			}
			r = n.newAssistantRecovery(p.id, launcher, execution, key, current, replyModel)
		}
		if (expected != nil && r != expected) || (r.proc != nil && r.proc != p) {
			n.mu.Unlock()
			return false
		}
		n.recoveries[p.id] = r
		// No new goroutine runs on the ended connection. The keeper carries this failure and the remaining budget to
		// the next Node, even when the result arrives after the process map has already handed over.
		r.pending = &assistantPendingFailure{text: result, kinds: kinds}
		r.lastResult = result
		if t, ok := GatewayAvailableAgain(result, time.Now()); ok {
			t := t
			r.lastReset = &t
		}
		r.finished = false
		r.proc = p
		n.mu.Unlock()
		p.mu.Lock()
		p.busy = true
		p.mu.Unlock()
		return true
	}
	if n.procs[p.id] != p || (expected != nil && r != expected) {
		n.mu.Unlock()
		return false
	}
	if r == nil {
		r = n.newAssistantRecovery(p.id, launcher, execution, key, current, replyModel)
	}
	n.recoveries[p.id] = r
	r.proc = p
	if r.running {
		r.pending = &assistantPendingFailure{text: result, kinds: kinds}
		r.lastResult = result
		if t, ok := GatewayAvailableAgain(result, time.Now()); ok {
			t := t
			r.lastReset = &t
		}
		n.mu.Unlock()
		p.mu.Lock()
		p.busy = true
		p.mu.Unlock()
		return true
	}
	if toolSearch && !r.toolSearchRetried {
		// The same model in a new process (runAssistantRecovery restarts a stale one); no other budget spent.
		r.toolSearchRetried = true
		r.running = true
		done := make(chan struct{})
		r.done = done
		n.mu.Unlock()
		p.mu.Lock()
		p.busy = true
		p.mu.Unlock()
		n.logger.Printf("assistant %s: resent once with every tool loaded", short(p.id))
		go func() {
			defer func() {
				n.mu.Lock()
				if r.done == done {
					r.done = nil
				}
				n.mu.Unlock()
				close(done)
			}()
			n.runAssistantRecovery(p, r, launcher, cwd, execution, current, true)
		}()
		return true
	}
	if !ordinary { // a refusal again, its one retry spent: the user's to see
		n.mu.Unlock()
		return false
	}
	if unavailable {
		n.noteUnavailable(launcher, key)
		if replyModel != "" && replyModel != key {
			n.noteUnavailable(launcher, replyModel)
		}
	}
	if unavailable || p.agent == "claude" && assistantSafeguardsRefusal(result) {
		// Claude Code continues a safeguards refusal once by itself ("continuing once with that noted"): a resend on the
		// same model would be the third try at it (#4283).
		r.retried[key] = true
	}
	same := !r.retried[key]
	if !same && execution.Strict {
		// Leave the chain for the result handler to clear from both the Node and the process.
		n.mu.Unlock()
		return false
	}
	if same {
		r.retried[key] = true
	}
	if current != "" {
		r.used[current] = true
		r.triedOrder = append(r.triedOrder, current)
	}
	if replyModel != "" && replyModel != current {
		r.used[replyModel] = true // an alias and the concrete model it resolved to share one retry budget
		r.triedOrder = append(r.triedOrder, replyModel)
	}
	r.lastResult = result
	if t, ok := GatewayAvailableAgain(result, time.Now()); ok {
		t := t
		r.lastReset = &t
	}
	r.running = true
	done := make(chan struct{})
	r.done = done
	n.mu.Unlock()

	if unavailable {
		n.clearStalePersistedModel(p.id, launcher, key)
		if replyModel != "" && replyModel != key {
			n.clearStalePersistedModel(p.id, launcher, replyModel)
		}
	}

	p.mu.Lock()
	p.busy = true // before the goroutine can finish: the caller must never overwrite an already-published final idle
	p.mu.Unlock()
	go func() {
		defer func() {
			n.mu.Lock()
			if r.done == done {
				r.done = nil
			}
			n.mu.Unlock()
			close(done)
		}()
		n.runAssistantRecovery(p, r, launcher, cwd, execution, current, same)
	}()
	return true
}

// nextAssistantModel is the next concrete entry in the launcher's own ordered list: the entries after current, then
// those before it (#10277). "default" is a placeholder, not a fallback. A configured model ID can match an alias's
// resolved model; custom entries absent from the list start at its first concrete entry. Where fit knows the windows
// (a Codex launcher's catalog), an entry whose window cannot hold the conversation is skipped, and those whose window is
// at least the asked-for model's come first; with none known the order is the list's. Nothing here invents an order
// for the named Agent presets in ModelProfile.Tiers.
func nextAssistantModel(list proto.LauncherModels, current string, used map[string]bool, fit assistantFit) (string, bool) {
	if list.Locked != "" {
		return "", false
	}
	at := -1
	if current == "" || current == "default" {
		for _, m := range list.Models {
			if m.Default || m.ID == "default" {
				// A failed request may never emit message_start. Resolve the default to avoid selecting its own alias again.
				current = m.Resolved
				if current == "" && m.ID != "default" {
					current = m.ID
				}
				break
			}
		}
	}
	for i, m := range list.Models {
		if current != "" && (m.ID == current || m.Resolved == current) {
			at = i
			break
		}
	}
	order := make([]int, 0, len(list.Models))
	for i := at + 1; i < len(list.Models); i++ {
		order = append(order, i)
	}
	for i := 0; i < at; i++ {
		order = append(order, i)
	}
	next := ""
	for _, i := range order {
		m := list.Models[i]
		if m.Default || m.ID == "" || m.ID == "default" || !proto.ValidModelID(m.ID) || m.ID == current ||
			(m.Resolved != "" && (m.Resolved == current || used[m.Resolved])) || used[m.ID] || !fit.holds(m) {
			continue
		}
		if fit.preferred(m) {
			return m.ID, true
		}
		if next == "" {
			next = m.ID
		}
	}
	return next, next != ""
}

// resolvedAssistantDefault is the model the launcher's default stands for, when current is that default (unnamed or
// "default"); "" otherwise.
func resolvedAssistantDefault(list proto.LauncherModels, current string) string {
	if current != "" && current != "default" {
		return ""
	}
	for _, m := range list.Models {
		if m.Default || m.ID == "default" {
			if m.Resolved != "" {
				return m.Resolved
			}
			if m.ID != "default" {
				return m.ID
			}
			return ""
		}
	}
	return ""
}

// assistantExecutionForModel keeps the requested effort when the fallback supports it. Otherwise it follows the
// launcher's default, matching the model picker instead of making the new model refuse an unsupported level.
func assistantExecutionForModel(execution modelExecution, list proto.LauncherModels, id string) modelExecution {
	execution.Model = id
	if execution.Effort == "" || execution.Effort == effortAuto {
		return execution
	}
	i := slices.IndexFunc(list.Models, func(m proto.AgentModel) bool { return m.ID == id })
	if i < 0 || !slices.Contains(list.Models[i].Efforts, execution.Effort) {
		execution.Effort = effortAuto
	}
	return execution
}

// assistantRecoveryModels is launchers.models, replaced by deterministic tests without a CLI or model call.
var assistantRecoveryModels = func(ctx context.Context, n *Node, launcher string) (proto.LauncherModels, error) {
	return n.launcherModels(withLauncherPurpose(ctx, "assistant"), launcher, "", false)
}

// assistantRecoverySend is the ordinary assistantSend recovery path, replaceable by a deterministic process handoff test.
var assistantRecoverySend func(context.Context, *Node, *assistantProc, string, string, modelExecution) error

func init() {
	assistantRecoverySend = sendAssistantRecovery
}

func sendAssistantRecovery(ctx context.Context, n *Node, p *assistantProc, launcher, cwd string, execution modelExecution) error {
	execution.OriginDecisionRequired = true
	_, err := n.assistantSend(ctx, p.id, cwd, launcher, prompts.RenderModelRetry(), nil, "", "", execution)
	return err
}

func (n *Node) runAssistantRecovery(p *assistantProc, r *assistantTurnRecovery, launcher, cwd string, execution modelExecution, current string, same bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if !same && (strictProtectedExecution(execution) || n.recoveryProtected(p.id)) {
		// A recovery queued before an upgrade/handover must obey the current policy too.
		n.finishAssistantRecovery(p, r)
		return
	}
	if !same {
		list, err := assistantRecoveryModels(ctx, n, launcher)
		if err != nil {
			n.logger.Printf("assistant %s: list fallback models: %v", p.id[:8], err)
			n.finishAssistantRecovery(p, r)
			return
		}
		if resolved := resolvedAssistantDefault(list, current); resolved != "" {
			r.used[resolved] = true // the default that failed before naming its model: never selected again (#3495)
		}
		n.mu.Lock()
		n.markUnavailableUsed(launcher, r.used) // a model another chain found the gateway does not serve (#5234)
		n.mu.Unlock()
		fit := n.assistantFallbackFit(ctx, p.id, launcher, list, r.from.Model, current)
		next, ok := nextAssistantModel(list, current, r.used, fit)
		if !ok {
			if fit.need > 0 && len(fit.windows) > 0 {
				n.logger.Printf("assistant %s: no fallback model left whose window holds %d tokens", p.id[:8], fit.need)
			}
			n.finishAssistantRecovery(p, r)
			return
		}
		if w := fit.windows[next]; w > 0 {
			n.logger.Printf("assistant %s: fallback %s (window %d) for a conversation needing %d", p.id[:8], next, w, fit.need)
		}
		execution = assistantExecutionForModel(execution, list, next)
		execution.PersistedModel = false
		execution.FromRecovery = true
		r.used[next] = true
		r.triedOrder = append(r.triedOrder, next)
		n.mu.Lock()
		r.switched = true
		n.mu.Unlock()
		// Make the node-driven switch visible: the marker names the configured model, the model now answering, why it
		// switched and, when the upstream said so, when the configured model is available again (#7824).
		if r.probeFrom == "" {
			r.probeFrom = resolvedAssistantDefault(list, r.from.Model)
		}
		n.recordRecoverySwitch(p.id, launcher, r.from.Model, r.probeFrom, next,
			ModelRecoveryReason(r.lastResult), r.lastReset, r.triedOrder, r.from)
	}
	// Another model, or the same one whose endpoint refused tool search or whose categories changed since start: a new process.
	err := assistantRecoverySend(withAssistantRecovery(ctx, r, !same || n.toolSearchStale(p) || n.categoriesStale(p, cwd)), n, p, launcher, cwd, execution)
	var pending *assistantPendingFailure
	var currentProc *assistantProc
	n.mu.Lock()
	if n.recoveries[p.id] == r {
		r.running = false
		pending, r.pending = r.pending, nil
		currentProc = n.procs[p.id]
	}
	n.mu.Unlock()
	if err == nil {
		if pending != nil {
			// A model switch replaces assistantProc. Its first result may already be pending when the send returns;
			// retry that current process, not the old process that started this recovery attempt.
			if currentProc != nil && n.recoverAssistantResultFor(currentProc, r, true, pending.text, pending.kinds...) {
				return
			}
			if currentProc == nil {
				currentProc = p
			}
			n.finishAssistantRecovery(currentProc, r)
		}
		return
	}
	n.logger.Printf("assistant %s: model recovery did not start: %v", p.id[:8], err)
	if currentProc == nil {
		currentProc = p
	}
	n.finishAssistantRecovery(currentProc, r)
}

// finishAssistantRecoveryOnExit waits for an in-flight send before treating a process exit as final. A failed starter
// may be replaced by assistantSend's own missing-command retry; its old exit event must not clear the replacement's recovery.
func (n *Node) finishAssistantRecoveryOnExit(p *assistantProc, r *assistantTurnRecovery) {
	defer n.typing.lock(p.id)()
	n.mu.Lock()
	current := n.procs[p.id]
	valid := n.recoveries[p.id] == r
	n.mu.Unlock()
	if valid && current == nil {
		n.finishAssistantRecovery(p, r)
	} else if !valid { // cancelled meanwhile (a Stop): the exit is the last word, as without a recovery (#3495)
		n.emitDelta(p.id, "")
		n.notifySessions()
	}
}

// finishAssistantRecovery publishes the exhausted or undeliverable recovery's final idle state. Its transcript keeps
// the original upstream error; the scheduled-run watcher can fail only now, not between two recovery attempts.
func (n *Node) finishAssistantRecovery(p *assistantProc, r *assistantTurnRecovery) {
	n.mu.Lock()
	if n.recoveries[p.id] != r {
		n.mu.Unlock()
		return
	}
	r.running = false
	current := n.procs[p.id] == p
	delete(n.recoveries, p.id) // an exhausted chain leaves no per-conversation bookkeeping behind
	n.mu.Unlock()
	// The failure card needs the exhausted marker so it can offer Retry with the cause and reset time. A chain that
	// switched keeps its From/To on the record; one that never moved leaves them empty. A chain that only resent a
	// tool-search refusal (#10365) records none: the model service did not fail, and the hub words that error itself.
	if !r.toolSearchRetried || r.switched || len(r.retried) > 0 {
		n.recordRecoveryExhausted(p.id, ModelRecoveryReason(r.lastResult), r.lastReset, r.triedOrder)
	}
	if r.switched && assistantModelRouteUnavailable(r.lastResult) {
		n.handleModelRouteFailureForced(p, r.lastResult, true)
	}
	if current {
		p.mu.Lock()
		p.busy = false
		p.lastActive = time.Now()
		if p.turnRecovery == r {
			p.turnRecovery = nil
		}
		p.mu.Unlock()
	}
	n.emitDelta(p.id, "")
	n.notifySessions()
}

// handleModelRouteFailure clears a stale persisted or recovery-selected model from sessionState when a turn fails with
// model_not_found / model_route, so subsequent turns automatically fall back to the launcher's default model instead of
// repeating the failure indefinitely (#15622). An explicit per-turn user/hub model pick is left untouched.
func (n *Node) handleModelRouteFailure(p *assistantProc, result string) {
	n.handleModelRouteFailureForced(p, result, false)
}

func (n *Node) handleModelRouteFailureForced(p *assistantProc, result string, forceFromRecovery bool) {
	if p == nil || p.id == "" || !assistantModelRouteUnavailable(result) {
		return
	}
	p.mu.Lock()
	launcher := p.launcher
	model := p.model.Model
	persisted := p.model.PersistedModel
	fromRecovery := forceFromRecovery || p.model.FromRecovery
	p.mu.Unlock()
	if model == "" {
		return
	}
	st, ok := n.state.get(p.id)
	staleRecovery := ok && (st.ModelFromRecovery ||
		(st.ModelRecovery != nil && st.ModelRecovery.To == model && !modelRecoveryIsOriginal(st.ModelRecovery, model)))
	if !persisted && !fromRecovery && !staleRecovery {
		return
	}
	n.noteModelRouteUnavailable(p.id, launcher, model)
	if n.clearStalePersistedModel(p.id, launcher, model) {
		n.logger.Printf("assistant %s: cleared stale persisted model %q on %s after model-route failure", short(p.id), model, launcher)
	}
	if ok && st.ModelRecovery != nil && st.ModelRecovery.To == model && !modelRecoveryIsOriginal(st.ModelRecovery, model) && !st.ModelRecovery.ChainExhausted {
		n.clearModelRecovery(p.id)
	}
}

// noteModelRouteUnavailable records model (and its resolved gateway arg, if any) as unavailable on launcher and drops
// any active fallback hold targeting that failing model.
func (n *Node) noteModelRouteUnavailable(id, launcher, model string) {
	if launcher == "" || model == "" {
		return
	}
	resolved := n.claudeModelArg(launcher, model)
	n.mu.Lock()
	n.noteUnavailable(launcher, model)
	if resolved != "" && resolved != model {
		n.noteUnavailable(launcher, resolved)
	}
	if f := n.fallbacks[id]; f != nil && f.launcher == launcher && f.to.Model == model {
		delete(n.fallbacks, id)
	}
	n.mu.Unlock()
}

// clearStalePersistedModel clears session id's persisted Model when it still names badModel on launcher, resetting
// ModelFingerprint to "launcher-default" so the next turn starts cleanly on the launcher's default model.
func (n *Node) clearStalePersistedModel(id, launcher, badModel string) bool {
	if id == "" || badModel == "" || n.state == nil {
		return false
	}
	cleared := false
	n.state.update(id, func(v *sessionState) bool {
		if v.Model != badModel || (launcher != "" && v.Launcher != "" && v.Launcher != launcher) {
			return false
		}
		v.Model = ""
		v.ModelEffort = ""
		v.ModelFingerprint = "launcher-default"
		v.ModelFromRecovery = false
		cleared = true
		return true
	})
	return cleared
}

// modelRecoveryIsOriginal reports whether model is the conversation's original configured model recorded in marker r,
// rather than a fallback target chosen by automatic recovery (#15622).
func modelRecoveryIsOriginal(r *proto.ModelRecovery, model string) bool {
	if r == nil || model == "" {
		return false
	}
	return model == r.From || ((r.From == "" || r.From == "default") && r.ProbeFrom != "" && model == r.ProbeFrom)
}

// modelRecoveryTargetsModel reports whether marker r identifies model as a fallback target chosen by automatic
// recovery rather than the conversation's original configured model (#15622).
func modelRecoveryTargetsModel(r *proto.ModelRecovery, model string) bool {
	if r == nil || model == "" || modelRecoveryIsOriginal(r, model) {
		return false
	}
	if r.To == model {
		return true
	}
	// A chain that never switched leaves From and To empty while still recording the original model in Tried.
	if !r.ChainExhausted || (r.From == "" && r.To == "") {
		return false
	}
	return slices.Contains(r.Tried, model)
}

// sanitizePersistedModel drops a persisted sessionState.Model back to the launcher's default when reusing it would
// trap the conversation on a stale, unavailable, or recovery-selected model (#15622). Explicit per-turn choices never
// reach this helper (execution.PersistedModel is false).
func (n *Node) sanitizePersistedModel(ctx context.Context, id, launcher string, st sessionState, execution *modelExecution, crossEngine, followsSetting bool, callerEffort string) {
	model := execution.Model
	if model == "" {
		return
	}
	drop := func(reason string) {
		n.logger.Printf("assistant %s: dropping persisted model %q on %s (%s); falling back to launcher default", short(id), model, launcher, reason)
		execution.Model = ""
		execution.Effort = callerEffort
		execution.UseEffort = true
		execution.Fingerprint = "launcher-default"
		execution.PersistedModel = false
	}
	if !proto.ValidModelID(model) {
		drop("invalid model id")
		return
	}
	if n.isModelUnavailable(launcher, model) {
		drop("marked unavailable")
		return
	}
	if resolved := n.claudeModelArg(launcher, model); resolved != "" && resolved != model && n.isModelUnavailable(launcher, resolved) {
		drop("resolved model marked unavailable")
		return
	}
	if st.ModelFromRecovery {
		drop("persisted from automatic recovery")
		return
	}
	if r := st.ModelRecovery; modelRecoveryTargetsModel(r, model) {
		if !crossEngine && r.From != "" && r.From != model && proto.ValidModelID(r.From) && !n.isModelUnavailable(launcher, r.From) &&
			n.persistedModelAvailableOnLauncher(ctx, launcher, r.From, false) {
			n.logger.Printf("assistant %s: restoring configured model %q in place of recovery model %q on %s", short(id), r.From, model, launcher)
			execution.Model = r.From
			return
		}
		drop("persisted from model recovery")
		return
	}
	if agent, err := n.assistantAgent(ctx, launcher); err == nil && agent == "claude" {
		if _, err := n.resolveClaudeModelArg(ctx, launcher, model); err != nil {
			drop("unresolved tier alias")
			return
		}
	}
	if !crossEngine {
		return
	}
	// The request's options belonged to a different engine, but the saved model may belong to the launcher we
	// are actually resuming on. For a session that follows global settings, require proof from that launcher
	// before reusing a saved model; an unknown legacy model still falls back to the launcher's default.
	if !n.persistedModelAvailableOnLauncher(ctx, launcher, model, followsSetting) {
		drop("not in launcher configuration or catalog")
	}
}

// persistedModelAvailableOnLauncher reports whether a persisted model is valid for launcher without spawning a CLI
// probe. Managed launchers validate against their declared model list or configured primary/tier models; cached
// launcher catalogs are consulted when present. requireKnown rejects a model when neither source can confirm it.
func (n *Node) persistedModelAvailableOnLauncher(ctx context.Context, launcher, model string, requireKnown bool) bool {
	if model == "" || !proto.ValidModelID(model) {
		return false
	}
	want := declaredModelID(model)
	resolved := model
	agent := ""
	if l, ok := n.managedLauncher(launcher); ok {
		agent = l.AgentType()
		if agent == "claude" {
			arg, err := n.resolveClaudeModelArg(ctx, launcher, model)
			if err != nil {
				return false
			}
			if arg == "" {
				// Explicit default alias or matches the launcher's own default model.
				return true
			}
			resolved = arg
		}
		resolvedWant := declaredModelID(cmp.Or(resolved, model))
		if len(l.Models) > 0 {
			for _, pm := range l.Models {
				if id := declaredModelID(pm.ID); id != "" && (id == want || id == resolvedWant) {
					return true
				}
			}
			for _, extra := range []string{l.Model, l.CustomModel} {
				if id := declaredModelID(extra); id != "" && (id == want || id == resolvedWant) {
					return true
				}
			}
			return false
		}
		// No explicit l.Models list: check the launcher's configured primary/tier models (excluding small/haiku
		// background tiers unless they also equal the primary model, since stale small_model/haiku_model entries
		// are a common source of model_not_found deadlocks after automatic fallback).
		if configured := managedConfiguredModels(l); len(configured) > 0 {
			return configured[want] || configured[resolvedWant]
		}
	}
	if cached, ok := n.cachedLauncherModels(launcher, agent); ok {
		if cached.Locked != "" {
			return false
		}
		if len(cached.Models) > 0 {
			resolvedWant := declaredModelID(cmp.Or(resolved, model))
			for _, m := range cached.Models {
				if id := declaredModelID(m.ID); id != "" && (id == want || id == resolvedWant) {
					return true
				}
				if r := declaredModelID(m.Resolved); r != "" && (r == want || r == resolvedWant) {
					return true
				}
			}
			return false
		}
	}
	return !requireKnown
}

func managedConfiguredModels(l proto.ManagedLauncher) map[string]bool {
	out := map[string]bool{}
	add := func(m string) {
		if id := declaredModelID(m); id != "" && !isTierAlias(id) {
			out[id] = true
		}
	}
	add(l.Model)
	add(l.OpusModel)
	add(l.SonnetModel)
	add(l.FableModel)
	add(l.CustomModel)
	if l.AgentType() == "claude" {
		env := providerEnv(l, "", "", false)
		add(env["ANTHROPIC_MODEL"])
		add(env["ANTHROPIC_DEFAULT_OPUS_MODEL"])
		add(env["ANTHROPIC_DEFAULT_SONNET_MODEL"])
		add(env["ANTHROPIC_DEFAULT_FABLE_MODEL"])
		add(env["ANTHROPIC_CUSTOM_MODEL_OPTION"])
	}
	return out
}

func (n *Node) cachedLauncherModels(name, agent string) (proto.LauncherModels, bool) {
	fp := n.executionFingerprint(name, modelExecution{})
	candidates := []string{agent}
	if agent == "" {
		candidates = []string{"claude", "codex"}
	}
	launcherModelsCache.Lock()
	defer launcherModelsCache.Unlock()
	for _, a := range candidates {
		if a == "" {
			continue
		}
		key := n.home + "\x00" + name + "\x00" + a + "\x00" + fp
		if at, ok := launcherModelsCache.at[key]; ok && time.Since(at) < launcherModelsStale {
			return launcherModelsCache.got[key], true
		}
	}
	return proto.LauncherModels{}, false
}
