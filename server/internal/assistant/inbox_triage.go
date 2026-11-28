package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/rpc"
)

const triageConcurrency = 4

// inboxTriageRev is the revision of the tier criteria (prompts/inbox/tiers.md). Every tiering stamps it on the item
// (TriageRev), the model's or the rules'. Raise it when the criteria change in a way that should reach items already
// sorted: each open "Needs you" item tiered under an older revision is sorted again once by the model (retriageStale),
// retriagePerPass of them a pass. A rule tier given because the model failed is asked again anyway (retryRuled,
// TestInboxFallbackIsRetried); one given with no model or a held connection stays.
// 1: "Needs you" is only what is aimed at the owner; pushes, alerts, chatter, @all notices and requests to others or to
// bots are FYI (#7986).
const inboxTriageRev = 1

// retriagePerPass is how many stale items one triage pass sorts again, besides the items with new content (which it
// keeps waiting no longer than a couple of model calls). Rule-tiered items (RuleOf) get as many of their own.
const retriagePerPass = 8

// Rule-tiered items (RuleOf) are asked again on the first pass after the model answers anything, else ruledBackoffMin
// after the last failure, the wait doubling with each pass whose retries got no answer, up to ruledBackoffMax: a model
// that keeps failing (a removed model answers 404 to every call) costs at most retriagePerPass calls that often,
// besides the new items each round asks about once. Variables for tests.
var (
	ruledBackoffMin = 10 * time.Minute
	ruledBackoffMax = 2 * time.Hour
)

// triageInbox sorts open items whose content changed since they were last looked at into "Needs you" / "FYI" / "Noise"
// with a one-line summary (the small-task model, inbox.triage). It runs in the background; one pass at a time.
func (s *Service) triageInbox(ctx context.Context) {
	b := &s.inbox
	if !b.triaging.TryLock() {
		return
	}
	b.mu.Lock()
	var todo, stale, ruled []*InboxItem
	for _, it := range b.items {
		if it.VisibilityExcluded || it.externallyRouted() {
			continue
		}
		if it.open() && it.TriagedAt != it.Last && it.Context != "" {
			if it.handled() { // what joins an event being handled follows it: its conversation decides (inbox_handle.go)
				it.TriagedAt = it.Last
				continue
			}
			if it.RuleOf == it.Last { // asked before, the model failed: retried below, a few at a time
				if b.retryRuled(it) {
					ruled = append(ruled, it)
				}
				continue
			}
			todo = append(todo, it)
		} else if b.retriageStale(it, false) {
			stale = append(stale, it)
		}
	}
	todo = append(todo, b.ruledDue(ruled, time.Now())...)
	// The oldest criteria's items go first, the newest activity first among them; each is tried once a run.
	slices.SortFunc(stale, func(x, y *InboxItem) int { return y.UpdatedAt.Compare(x.UpdatedAt) })
	for _, it := range stale[:min(len(stale), retriagePerPass)] {
		b.retriaged[it.ID] = true
		todo = append(todo, it)
	}
	b.mu.Unlock()
	if len(todo) == 0 {
		b.triaging.Unlock()
		s.fileFoldersSoon(ctx)
		s.handInbox(ctx)
		return
	}
	// The pass outlives the caller: a pull to refresh answers before it is done.
	ctx = context.WithoutCancel(ctx)
	go func() {
		defer b.triaging.Unlock()
		sem := make(chan struct{}, triageConcurrency)
		var wg sync.WaitGroup
		for _, it := range todo {
			wg.Add(1)
			sem <- struct{}{}
			go func(it *InboxItem) {
				defer wg.Done()
				defer func() { <-sem }()
				if w := s.triage(ctx, it); w != nil {
					// Undone, the item is untriaged again: the next pass asks the model again.
					s.saveWrites(ctx, []*inboxWrite{w}, "its triage")
				}
			}(it)
		}
		wg.Wait()
		s.fileFoldersSoon(ctx) // where the items go (inbox_folders.go), in its own background pass
		s.handInbox(ctx)
	}()
}

// triage asks the model about an item in the list whose content changed; when the model fails the item gets its
// fallback tier (and no summary). When its connection is held the item stays untriaged. An item without a fallback
// (a connector's thing its rules leave to the model) keeps what it shows until a pass gets an answer. It returns the
// change, settled (inbox_rollback.go); nil when the item was left as it was.
func (s *Service) triage(ctx context.Context, it *InboxItem) *inboxWrite {
	last, v := s.askTriage(ctx, it, 90*time.Second)
	if ctx.Err() != nil || v.state == triageExcluded {
		return nil
	}
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	if it.Last != last { // new messages arrived meanwhile: the next pass sees them
		return nil
	}
	ruled := it.RuleOf == last
	if v.state == "" {
		b.ruledAnswered()
	}
	if ruled && (v.state != "" || !b.retryRuled(it)) {
		return nil // still no answer (it keeps its rule tier, a later pass asks again), or the item moved on meanwhile
	}
	if it.TriagedAt == last { // sorted again under new criteria (retriageStale)
		if v.state == triagePaused || v.state == triageUnconfigured || v.state == triageTimeout {
			delete(b.retriaged, it.ID) // the model wasn't there: a later pass tries again
			return nil
		}
		if !b.retriageStale(it, true) { // filed, handled or re-sorted meanwhile
			return nil
		}
	}
	w := beginWrite(it)
	settled := func() *inboxWrite { b.settle(w); return w }
	fallback := b.ruleTier(it)
	switch {
	case v.state == triagePaused:
		// The model's connection is held (quota used up, busy): not triaged, so a pass after it is back asks again.
		// Meanwhile an untriaged item sits in its rule tier rather than in "Needs you".
		if it.Tier == "" && fallback != "" {
			it.Tier, it.Reason, it.TriageRev = fallback, b.byRules(), inboxTriageRev
			b.applyPolicyTier(it)
			return settled()
		}
		return nil
	case v.state == "":
		it.Tier, it.Reason = v.tier, v.reason
		if !keepsWords(it) { // a title and summary the assistant or the user wrote stay
			it.Summary = v.summary
		}
		it.Urgent = v.urgent && v.tier == TierAction
		if it.Group != nil { // an event sorted again: its tier is the model's now, not its connector's rule
			it.TriagedBy = "model"
		}
	case fallback == "" || it.TriagedAt == last:
		// No fallback, or sorted before (retriageStale): what it shows stays.
		return nil
	default:
		if v.err != nil {
			s.logf("inbox: triage %s: %v", it.ID, v.err)
		}
		it.Tier, it.Reason = fallback, b.byRules()
	}
	b.applyPolicyTier(it) // the user's policy has the last word on the tier (inbox_policies.go)
	b.liftMerged(it)      // a merged row stays at its rows' highest tier (inbox_merge.go, #7230)
	it.TriageRev, it.RuleOf = inboxTriageRev, ""
	if v.state == triageFailed || v.state == triageTimeout {
		// The model was asked and did not answer: the rule tier stands in until it does (retryRuled). Not "no model"
		// (nothing to wait for), and not a held connection (handled above). The rule-tiered items are not asked again
		// right after a failure: new items asking it in the meantime find out first whether it is back.
		it.RuleOf = last
		b.deferRuled(time.Now())
	} else {
		it.TriagedAt = last
	}
	b.noteTriaged(it)
	// Triage can settle an item read elsewhere while it was still untriaged (not one filed while the model thought).
	if it.Read && it.Tier != TierAction && it.open() {
		b.file(it, time.Now(), "read")
	}
	return settled()
}

// retryRuled: an open item in its rule tier because the model failed on what it holds now (RuleOf), which the model
// may still sort: not one handled, or anything the AI twin or a follow-up touched (handling, a suggestion, a due check,
// its agent record: their tier is theirs), as retriageStale. Under b.mu.
func (b *inboxBook) retryRuled(it *InboxItem) bool {
	return it.open() && it.RuleOf == it.Last && it.TriagedAt != it.Last && it.Context != "" && !it.handled() &&
		currentSuggestion(it) == nil && it.Handling == nil && it.Follow == nil && it.Agent == nil
}

// ruledAnswered: the model answered, so the rule-tiered items' turn is the next pass. Under b.mu.
func (b *inboxBook) ruledAnswered() { b.ruledFails, b.ruledRetryAt = 0, time.Time{} }

// deferRuled puts the rule-tiered items' next retry at least ruledBackoffMin after a failure at now. Under b.mu.
func (b *inboxBook) deferRuled(now time.Time) {
	if at := now.Add(ruledBackoffMin); at.After(b.ruledRetryAt) {
		b.ruledRetryAt = at
	}
}

// ruledDue is the rule-tiered items (retryRuled) this pass asks the model about again: none before b.ruledRetryAt,
// else the newest retriagePerPass; the next ones wait twice as long as these did, unless the model answers meanwhile
// (triage resets it). Under b.mu.
func (b *inboxBook) ruledDue(ruled []*InboxItem, now time.Time) []*InboxItem {
	if len(ruled) == 0 || now.Before(b.ruledRetryAt) {
		return nil
	}
	slices.SortFunc(ruled, func(x, y *InboxItem) int { return y.UpdatedAt.Compare(x.UpdatedAt) })
	wait := ruledBackoffMin << min(b.ruledFails, 16)
	if wait <= 0 || wait > ruledBackoffMax {
		wait = ruledBackoffMax
	}
	b.ruledFails++
	b.ruledRetryAt = now.Add(wait)
	return ruled[:min(len(ruled), retriagePerPass)]
}

// retriageStale: an open "Needs you" item whose tier was given under older criteria (inboxTriageRev), not yet sorted
// again this run (asked: it is being, so that is not checked). Only Lark and mail items and connectors' events (a
// connector's other items carry its rules' tier). Left out: anything the AI twin or a follow-up touched (handling, a
// suggestion, a due check, its agent record: their Needs you is theirs), a tier a policy sets (it would set it again),
// and items without text. Under b.mu.
func (b *inboxBook) retriageStale(it *InboxItem, asked bool) bool {
	if !it.open() || it.Tier != TierAction || it.TriageRev >= inboxTriageRev || it.Context == "" || it.TriagedAt != it.Last ||
		it.handled() || currentSuggestion(it) != nil || it.Handling != nil || it.Follow != nil || it.Agent != nil ||
		!asked && b.retriaged[it.ID] {
		return false
	}
	if it.Connector != nil && it.Group == nil {
		return false
	}
	if p := b.policyOf(it); p != nil && p.Tier != "" {
		return false
	}
	return true
}

// Why the model gave no tier (a triage verdict's state, GET /api/v1/inbox triage.state), the most telling first.
const (
	triageUnconfigured = "unconfigured" // no model is set up for inbox.triage
	triagePaused       = "paused"       // its connection is held: quota used up, or busy
	triageFailed       = "error"        // it failed or answered without a tier
	triageTimeout      = "timeout"      // too slow
	triageBehind       = "behind"       // not asked: the round had more new items than it triages (admitInbox)
	triageExcluded     = "excluded"     // not asked: the source-privacy boundary leaves the item for the owner
)

var triageStates = []string{triageUnconfigured, triagePaused, triageFailed, triageTimeout, triageBehind}

// triageVerdict is the model's answer about one item, or (state) why there is none.
type triageVerdict struct {
	tier, summary, reason string
	urgent                bool   // someone waits, a deadline is near (action only)
	state                 string // "" when answered
	why                   string // the state for people: "model out of quota"
	err                   error
}

// askTriage asks the model about one item, under the item's own rules when its source sent some (they replace
// inbox.rules; the default criteria stay underneath). last is the Last it asked about.
func (s *Service) askTriage(ctx context.Context, it *InboxItem, timeout time.Duration) (last string, v triageVerdict) {
	b := &s.inbox
	b.mu.Lock()
	last = it.Last
	b.mu.Unlock()
	allowed, err := s.inboxVisibilityModelAllowed(ctx, it)
	if err != nil {
		return last, triageVerdict{state: triageFailed, why: i18n.T(s.lang(ctx), "assistant-inbox_triage.failed"), err: err}
	}
	if !allowed {
		return last, triageVerdict{state: triageExcluded}
	}
	hello := s.hello(ctx)
	language := answerLanguage(hello.Locale)
	b.mu.Lock()
	last, input, rules := it.Last, prompts.RenderInboxItem(b.triagePromptItem(it, language)), strings.TrimSpace(it.Rules)
	b.mu.Unlock()
	return last, s.triageModelIn(ctx, input, rules, timeout, hello)
}

// triageModel asks the triage model about one item's prompt input under rules (its source's; "" the Inbox's).
func (s *Service) triageModel(ctx context.Context, input, rules string, timeout time.Duration) (v triageVerdict) {
	return s.triageModelIn(ctx, input, rules, timeout, s.hello(ctx))
}

// triageModelIn uses the same owner/language snapshot as the input wrapper.
func (s *Service) triageModelIn(ctx context.Context, input, rules string, timeout time.Duration, hello Hello) (v triageVerdict) {
	if rules == "" {
		rules = s.inboxConfig(ctx).Rules
	}
	extContext, _ := s.inboxExtensionContext(ctx)
	extContext = append(extContext, s.inboxMemoryContext(ctx, input)...) // memory_extension_source.go
	system := prompts.RenderInboxTriage(prompts.InboxTriage{Owner: hello.Owner, Rules: rules, Language: answerLanguage(hello.Locale),
		Context: extContext})
	var out struct {
		Tier    string `json:"tier"`
		Summary string `json:"summary"`
		Reason  string `json:"reason"`
		Urgent  bool   `json:"urgent"`
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	err := s.inboxModelJSON(cctx, purposeTriage, system, input, 0, &out)
	slow := errors.Is(cctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	cancel()
	paused, quota := modelPaused(err)
	l := s.lang(ctx)
	switch {
	case paused:
		v.state, v.why = triagePaused, i18n.T(l, "assistant-inbox_triage.busy")
		if quota {
			v.why = i18n.T(l, "assistant-inbox_triage.no_quota")
		}
	case out.Tier == TierAction || out.Tier == TierFYI || out.Tier == TierNoise:
		v.tier, v.summary, v.reason = out.Tier, clip(oneLine(out.Summary), 80), clip(oneLine(out.Reason), 30)
		v.urgent = out.Urgent && out.Tier == TierAction
	case modelUnconfigured(err):
		v.state, v.why = triageUnconfigured, i18n.T(l, "assistant-inbox_triage.unconfigured")
	case slow:
		v.state, v.why = triageTimeout, i18n.T(l, "assistant-inbox_triage.slow")
	default:
		v.state, v.why = triageFailed, i18n.T(l, "assistant-inbox_triage.failed")
	}
	v.err = err
	return v
}

// ruleTier is the fallback tier, without a model: people talking to me need me, groups are FYI, bots and lists are
// noise; a connector's item has the tier its rules gave, else noise; a plugin's connector's none ("": only the model
// sorts it, inbox_admit.go), nor one's whose source is not known yet to be built in (the manager just started).
func (b *inboxBook) ruleTier(it *InboxItem) string {
	if it.Connector != nil {
		switch {
		case it.Connector.RuleTier != "":
			return it.Connector.RuleTier
		case b.fromExtension(it.Source) || !b.builtinSource(it.Source):
			return ""
		}
		return TierNoise
	}
	switch it.Kind {
	case KindMention:
		// An @all without an @me is an announcement: worth knowing, not waiting on me.
		if it.Lark != nil && it.Lark.AtAll && !it.Lark.AtMe {
			return TierFYI
		}
		return TierAction
	case KindDM, KindDoc:
		return TierAction
	case KindMoment: // an @ asks for me; a comment or a reply is mostly chatter
		if it.Moment != nil && strings.HasPrefix(it.Moment.Notice, "atIn") {
			return TierAction
		}
		return TierFYI
	case KindGroup:
		return TierFYI
	case KindMail:
		if it.Mail != nil && it.Mail.Direct {
			return TierFYI
		}
	}
	return TierNoise
}

// triagePromptItem separates the latest Lark DM for triage and the no-write preview.
func (b *inboxBook) triagePromptItem(it *InboxItem, language string) prompts.InboxItem {
	p := b.promptItemIn(it, language)
	if it.Connector != nil && it.Source == "larkcli" && it.Kind == KindDM {
		p.Latest = it.Preview
	}
	return p
}

func (b *inboxBook) promptItem(it *InboxItem) prompts.InboxItem {
	return b.promptItemIn(it, answerLanguage(string(b.userLang())))
}

func (b *inboxBook) promptItemIn(it *InboxItem, language string) prompts.InboxItem {
	p := prompts.InboxItem{Language: language, Kind: it.Kind, MailDirect: it.Mail != nil && it.Mail.Direct, Context: it.Context}
	if it.Connector != nil {
		p.Source = it.Source
		p.Described = b.kindWords(it.Source, it.Kind)
	}
	return p
}

// draftModelWait is how long a draft may take (the app waits on it).
const draftModelWait = 2 * time.Minute

// onDemandDraftAuthorization binds a generated draft to the complete item state and policy under which generation began.
// Legacy instruction classification and compiled exact-text snapshots are retained independently for the two schemas.
type onDemandDraftAuthorization struct {
	before         []byte
	policy         *assistantwire.InboxReplyPolicyConfig
	policyState    []byte
	applied        bool
	instruction    string
	conversation   string
	textAuthorized bool
	textPolicy     *InboxReplyPolicySnapshot
}

// draftReply writes a reply to an inbox item in the user's voice (optionally following their instruction).
func (s *Service) draftReply(ctx context.Context, id, instruction string) (string, error) {
	return s.draftReplyVersion(ctx, id, instruction, 0)
}

// draftReplyVersion binds Workbench redraft generation to the suggestion version rendered on that card. The version
// and the pre-generation item snapshot are checked under the same lock; finishOnDemandDraft catches later changes.
func (s *Service) draftReplyVersion(ctx context.Context, id, instruction string, expectedVersion int) (string, error) {
	return s.draftReplyVersionMode(ctx, id, instruction, expectedVersion, expectedVersion > 0, nil)
}

// draftReplyVersionMode also lets the Workbench distinguish an old unversioned redraft from an ordinary new draft.
// When policy publication created a mirror, publishedVersion receives its exact version for the rendered card.
func (s *Service) draftReplyVersionMode(ctx context.Context, id, instruction string, expectedVersion int, strictVersion bool, publishedVersion *int) (string, error) {
	hello := s.hello(ctx)
	language := answerLanguage(hello.Locale)
	initialCfg, err := s.inboxConfigFresh(ctx)
	if err != nil {
		return "", errReplyPolicyDenied
	}
	if legacyReplyPolicyConfig(initialCfg.ReplyPolicy) {
		if err := s.classifyInboxReplyPolicyForDraft(ctx, id, initialCfg.ReplyPolicy); err != nil {
			return "", errReplyPolicyDenied
		}
	}
	policyCfg := initialCfg.ReplyPolicy
	policy := compileReplyPolicy(policyCfg)
	b := &s.inbox
	b.mu.Lock()
	it, ok := b.items[id]
	b.mu.Unlock()
	if !ok {
		return "", errNoInboxItem
	}
	allowed, err := s.inboxVisibilityModelAllowed(ctx, it)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", errCannotReply
	}
	b.mu.Lock()
	it, ok = b.items[id]
	var item prompts.InboxItem
	var authorization onDemandDraftAuthorization
	var policyItem replyPolicyItemData
	var existingSnap *InboxReplyPolicySnapshot
	if ok {
		if expectedVersion > 0 {
			g := currentSuggestion(it)
			if g == nil || g.Kind != SuggestReply || g.Version != expectedVersion {
				b.mu.Unlock()
				return "", errSuggestionStale
			}
		}
		authorization.before = itemState(it)
		authorization.policy = cloneReplyPolicyForSend(policyCfg)
		authorization.policyState, _ = json.Marshal(policyCfg)
		authorization.applied = replyPolicyItemApplies(policyCfg, it)
		authorization.instruction = strings.TrimSpace(instruction)
		authorization.conversation = it.Context
		if authorization.applied {
			if strictVersion && expectedVersion <= 0 {
				// An old Workbench redraft cannot prove which suggestion it rewrites. Ordinary new drafts remain unversioned,
				// but a policy-scoped replacement must start from the exact current card rather than supersede a newer one.
				b.mu.Unlock()
				return "", errSuggestionStale
			}
			if legacyReplyPolicyConfig(policyCfg) {
				if err := replyPolicySuggestionGate(it, SuggestReply, policyCfg); err != nil {
					b.mu.Unlock()
					return "", err
				}
			}
		}
		item = b.promptItemIn(it, language)
		if policy.enabled {
			policyItem = snapshotItemForReplyPolicyLocked(it)
			existingSnap = cloneReplyPolicySnapshot(it.ReplyPolicy)
		}
	}
	b.mu.Unlock()
	if !ok {
		return "", errNoInboxItem
	}
	if policy.enabled {
		if _, err := s.gateReplyPolicySuggestion(ctx, policy, policyItem, existingSnap, instruction); err != nil {
			return "", errReplyPolicyBlocked
		}
	}
	extContext, neverQuote := s.inboxExtensionContext(ctx)
	// About me as the user last edited it in the app, else the config file's (notes), as the main chat has it.
	d := prompts.InboxDraft{Owner: hello.Owner, Notes: s.notes(ctx), Language: answerLanguage(hello.Locale), Context: extContext}
	input := prompts.RenderInboxDraftInput(prompts.InboxDraftInput{Language: language, Item: item, Instruction: strings.TrimSpace(instruction)})
	d.Context = append(d.Context, s.inboxMemoryContext(ctx, input)...) // memory_extension_source.go
	cctx, cancel := context.WithTimeout(ctx, draftModelWait)
	defer cancel()
	// A draft that repeats a sentence an extension marked never to quote (what it keeps about people: evaluations,
	// guesses) is written again once, told so; if that one repeats one too, there is no draft (extension_context.go).
	for {
		text, err := s.inboxModelComplete(cctx, purposeDraft, prompts.RenderInboxDraft(d), input, 1024)
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(text)
		hit := quotesAny(text, neverQuote)
		if hit == "" {
			if legacyReplyPolicyConfig(authorization.policy) && authorization.applied && authorization.instruction != "" {
				// An instruction is owner-introduced text just like an edit on a suggestion card. Classify the generated
				// result with that instruction and the original conversation; the item's old category alone cannot authorize it.
				category, err := s.classifyReplyPolicyInput(cctx, authorization.policy, prompts.InboxReplyPolicyInput{
					Text: text, Original: authorization.conversation + "\n\nOwner draft instruction:\n" + authorization.instruction, Edit: true,
				})
				if err != nil || replyRoute(category, authorization.policy) != ReplyRouteOwnerDraft {
					return "", errReplyPolicyDenied
				}
				authorization.textAuthorized = true
			}
			liveCfg, liveErr := s.inboxConfigFresh(cctx)
			if liveErr != nil {
				return "", errReplyPolicyDenied
			}
			if livePolicy := compileReplyPolicy(liveCfg.ReplyPolicy); livePolicy.enabled {
				b.mu.Lock()
				live := b.items[id]
				if live == nil {
					b.mu.Unlock()
					return "", errNoInboxItem
				}
				if inboxVisibilityWithMembersExcludedLocked(b, live, liveCfg.Visibility) {
					b.mu.Unlock()
					return "", errCannotReply
				}
				unchanged := bytes.Equal(authorization.before, itemState(live))
				currentApplies := livePolicy.appliesToItem(live)
				liveCV := replyPolicyContentVersion(live)
				b.mu.Unlock()
				// Compiled policies reject a transition before spending another classifier call. Legacy policies use the
				// raw-configuration comparison in finishOnDemandDraft, which also catches cross-schema transitions.
				policyChanged := policy.enabled && (authorization.applied != currentApplies ||
					authorization.applied && !reflect.DeepEqual(policyCfg, liveCfg.ReplyPolicy))
				if unchanged && (authorization.applied || currentApplies) && policyChanged {
					return "", errReplyPolicyBlocked
				}
				if policy.enabled && liveCV == policyItem.contentVersion {
					classifierItem := policyItem
					if instruction = strings.TrimSpace(instruction); instruction != "" {
						classifierItem = replyPolicyItemWithInstruction(classifierItem, &InboxChainEntry{Detail: instruction})
					}
					snap, err := s.gateReplyPolicySend(ctx, policy, classifierItem, text)
					if err != nil {
						return "", errReplyPolicyBlocked
					}
					authorization.textPolicy = snap
				}
			}
			return s.finishOnDemandDraft(cctx, id, text, authorization, publishedVersion)
		}
		if d.Rewrite {
			s.logf("inbox: draft for %s withheld: the rewrite still repeats a sentence an extension marked never to quote", id)
			return "", errDraftWithheld
		}
		s.logf("inbox: draft for %s repeats a sentence an extension marked never to quote; writing it again", id)
		d.Rewrite = true
	}
}

// finishOnDemandDraft keeps the legacy transient result only while the item remains outside reply-policy scope. Inside
// scope, the exact same text becomes a normal reply suggestion and old-App mirror; direct /reply and workbench sends then
// traverse edit veto, snapshot authorization and send-once instead of mistaking model output for owner-authored text.
func (s *Service) finishOnDemandDraft(ctx context.Context, id, text string, authorization onDemandDraftAuthorization, publishedVersion *int) (string, error) {
	cfg, err := s.inboxConfigFresh(ctx)
	if err != nil {
		return "", errReplyPolicyDenied
	}
	b := &s.inbox
	b.mu.Lock()
	it := b.items[id]
	if it == nil {
		b.mu.Unlock()
		return "", errNoInboxItem
	}
	if inboxVisibilityWithMembersExcludedLocked(b, it, cfg.Visibility) {
		b.mu.Unlock()
		s.quarantineInboxVisibility(ctx, cfg.Visibility)
		return "", errCannotReply
	}
	applies := replyPolicyItemApplies(cfg.ReplyPolicy, it)
	if !applies && !authorization.applied {
		// A legacy transient draft took no authorization snapshot. Unrelated triage/read updates during generation must
		// not turn the old, policy-disabled endpoint into a stale-suggestion failure.
		b.mu.Unlock()
		return text, nil
	}
	if !bytes.Equal(authorization.before, itemState(it)) {
		b.mu.Unlock()
		return "", errSuggestionStale
	}
	if authorization.applied && !applies {
		// Leaving policy scope during generation cannot turn an already policy-governed model result back into a legacy
		// transient owner draft, even when no item field changed. Preserve the older stale classification when the item
		// itself changed too; that conflict is more specific and remains what old clients retry against.
		b.mu.Unlock()
		return "", errReplyPolicyDenied
	}
	if !applies {
		b.mu.Unlock()
		return text, nil
	}
	legacyPolicy := legacyReplyPolicyConfig(cfg.ReplyPolicy)
	if legacyPolicy && authorization.instruction != "" && !authorization.textAuthorized {
		// The legacy policy came into scope while the model ran, so no generated-text classification was made under it.
		b.mu.Unlock()
		return "", errReplyPolicyDenied
	}
	compiledPolicy := compileReplyPolicy(cfg.ReplyPolicy)
	if !legacyPolicy && compiledPolicy.enabled &&
		(authorization.textPolicy == nil || compiledPolicy.allowsOwnerDraft(authorization.textPolicy, replyPolicyContentVersion(it)) != nil) {
		b.mu.Unlock()
		return "", errReplyPolicyBlocked
	}
	if !it.open() {
		b.mu.Unlock()
		return "", errInboxItemDone
	}
	if authorization.applied && !reflect.DeepEqual(authorization.policy, cfg.ReplyPolicy) {
		b.mu.Unlock()
		return "", errReplyPolicyDenied
	}
	if legacyPolicy {
		if err := replyPolicySuggestionGate(it, SuggestReply, cfg.ReplyPolicy); err != nil {
			b.mu.Unlock()
			return "", err
		}
	}
	if uncertainLegacyReplyAttempt(it, text, nil) != nil {
		// The same physical reply already has an unresolved outcome. A new on-demand mirror must not create a fresh
		// authorization that could send it again under another suggestion version or caller identity.
		b.mu.Unlock()
		return "", errSuggestionStale
	}
	nextSeq := it.SuggestSeq
	if legacySuggestion(it) != nil {
		nextSeq = max(nextSeq, 1)
	}
	now := time.Now().UTC()
	g := &InboxSuggestion{Version: nextSeq + 1, Kind: SuggestReply, Text: text, At: now,
		SourceOf: suggestionReplyTarget(it), MailTarget: mailTarget(*it), OnDemand: true, ReplyPublicationPending: true}
	if it.Agent != nil {
		g.Owner = &SessionRef{MachineID: it.Agent.MachineID, SessionID: it.Agent.SessionID}
	}
	if legacyPolicy {
		snapshotReplyPolicy(g, it, cfg.ReplyPolicy)
	} else {
		// The compiled snapshot authorizes this exact generated text, not the item's earlier classification.
		g.ReplyPolicy = cloneReplyPolicySnapshot(authorization.textPolicy)
		it.ReplyPolicy = cloneReplyPolicySnapshot(authorization.textPolicy)
	}
	if uncertainReplyOperation(it, g, text, true) != nil {
		// This endpoint has no attempt outcome to return. Keep an unresolved physical send on its existing card instead
		// of returning the same text as though it were safe for an old client to send again.
		b.mu.Unlock()
		return "", errSuggestionStale
	}
	w := beginWrite(it)
	if it.Agent == nil {
		it.Agent = &InboxAgent{State: AgentSkipped}
	}
	it.SuggestSeq, it.Suggestion = g.Version, g
	it.Agent.Wake, it.Agent.Back = false, ""
	b.setHandling(it, HandlingDone, "", now)
	var link *InboxChainLink
	if it.Agent.MachineID != "" || it.Agent.SessionID != "" {
		link = &InboxChainLink{Type: LinkSession, MachineID: it.Agent.MachineID, SessionID: it.Agent.SessionID}
	}
	b.chainAdd(it, InboxChainEntry{Kind: ChainHandling, At: now, Text: i18n.T(b.userLang(), "assistant-inbox_chain.suggested_reply"), Detail: clip(text, 600), Link: link})
	if mirroredLegacyReply(it) == nil {
		b.restore(w)
		b.mu.Unlock()
		return "", errSuggestionStale
	}
	b.settle(w)
	b.mu.Unlock()
	failed := s.saveWrites(ctx, []*inboxWrite{w}, "its on-demand reply suggestion")
	if hook := s.inbox.onDemandDraftSaveHook; hook != nil {
		hook()
	}
	b.mu.Lock()
	undone := w.undone
	tombstone := false
	if failed[id] && !undone && b.items[id] == it && currentSuggestion(it) == g {
		// A concurrent mutation can make the generic rollback unsafe even though this exact mirror was never saved.
		// Keep its AI provenance as a fail-closed tombstone instead of letting a later old-client /reply treat the
		// unpublished model text as an owner-authored direct reply.
		g.ReplyPublicationFailed = true
		b.touched(it.ID)
		tombstone = true
	}
	current := b.items[id] == it && mirroredLegacyReply(it) == g && bytes.Equal(itemState(it), w.applied)
	b.mu.Unlock()
	if tombstone {
		// The first failed save can be followed by a successful metadata write that still carries the false marker. Persist
		// the tombstone as its own change, without saveWrites' automatic rollback of the mirror it is meant to quarantine.
		s.saveInbox(ctx, []*InboxItem{it})
	}
	if failed[id] || undone {
		return "", errInboxNotPersisted
	}
	if !current {
		// A newer device state can prevent rollback of a failed write, or can replace the mirror just after a successful
		// one. In either case this request must not return obsolete text as though its own mirror were still sendable.
		return "", errSuggestionStale
	}
	if g.ReplyPublicationPending {
		// Publication is a two-phase durable change. The first row is deliberately pending; only a separate successful
		// commit makes the model text confirmable. A restart between them therefore retains a fail-closed marker.
		b.mu.Lock()
		stillCurrent := b.items[id] == it && currentSuggestion(it) == g && mirroredLegacyReply(it) == g
		var committedState []byte
		if stillCurrent {
			g.ReplyPublicationPending = false
			b.touched(it.ID)
			committedState = itemState(it)
		}
		b.mu.Unlock()
		if !stillCurrent {
			return "", errSuggestionStale
		}
		if commitFailed := s.saveInbox(ctx, []*InboxItem{it}); commitFailed[id] {
			b.mu.Lock()
			if currentSuggestion(it) == g {
				g.ReplyPublicationPending = true
				b.touched(it.ID)
			}
			b.mu.Unlock()
			return "", errInboxNotPersisted
		}
		b.mu.Lock()
		committed := b.items[id] == it && currentSuggestion(it) == g && mirroredLegacyReply(it) == g &&
			!g.ReplyPublicationPending && bytes.Equal(itemState(it), committedState)
		b.mu.Unlock()
		if !committed {
			// The second save may have persisted a concurrent replacement. Never return this request's superseded text as
			// though the exact suggestion it published were still the live, confirmable mirror.
			return "", errSuggestionStale
		}
	}
	if publishedVersion != nil {
		*publishedVersion = g.Version
	}
	return text, nil
}

// inboxReply serializes the replies to one inbox item and remembers the last one that went out.
type inboxReply struct {
	mu sync.Mutex
	// beforeLock is a test synchronization point before this reply waits for the per-item send mutex.
	beforeLock func()
	clientID   string
	// keptOpen: the last reply went out but the item stayed open, for what came into it while the reply was out.
	keptOpen bool
	// unsaved: the last reply filed the item but its row could not be written; a repeat of it writes it again.
	unsaved bool
}

// repeated reports whether a reply with key went out last and still stands, so this one is a repeat (pressed on
// phone and watch, a timed-out request retried): the item filed as replied, or kept open by that reply. Without the
// second, a retry after new messages came in would go out again (#1828). Under b.mu, with r.mu held.
func (r *inboxReply) repeated(live *InboxItem, key string) bool {
	return r.clientID == key && (live.DoneBy == "replied" || r.keptOpen && live.open())
}

// inboxReplyReq is one reply: the item as it is in the inbox (live, under b.mu) and a copy taken for the reply, the
// text, the documents' status moves, and the item's replies (held for the whole reply).
type inboxReplyReq struct {
	id   string
	live *InboxItem
	item InboxItem
	text string
	// clientID: the app's id for the send (POST …/reply client_id); empty for one derived from the item and the text.
	clientID string
	mentions []InboxMention
	changes  []DocChange
	once     *inboxReply
	// sendID names the send for the hub, which sends it once however often it is asked (InboxSent): a retry after
	// the answer was lost (the link dropping, the manager restarting) does not go out twice (replySendID).
	sendID string
}

// replySendID names one reply: the app's id for the send when it gave one, else the item, the text and the status
// moves (the same reply asked again is the same send). Not the message it answers: a retry after new messages came
// into the item (it stayed open, replyInboxFrom) would go out again (#1828), as the derived Feishu clientId is.
func replySendID(id, text, clientID string, changes []DocChange, mentions ...InboxMention) string {
	if validClientID(clientID) {
		return namedClientID("send\n" + id + "\n" + strings.ToLower(clientID))
	}
	moves, _ := json.Marshal(changes)
	if len(mentions) > 0 {
		ms, _ := json.Marshal(mentions)
		return namedClientID("send\n" + id + "\n" + text + "\n" + string(moves) + "\n" + string(ms))
	}
	return namedClientID("send\n" + id + "\n" + text + "\n" + string(moves))
}

// replyInDoubt: the reply may have gone out though no answer said so: the link to the hub dropped while it was out,
// the time ran out, or the hub says a send under its id was cut off before (assistantwire.CodeAssistantInDoubt).
func replyInDoubt(err error) bool {
	var re *rpc.Error
	return errors.Is(err, rpc.ErrDropped) || errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &re) && re.Code == assistantwire.CodeAssistantInDoubt
}

// replyInboxFrom sends text as the user's reply to the item's source — in the Feishu chat, a document's comments, a
// Moments (company feed) post (the plugin that took Feishu over), a reply to the newest mail of the thread, a GitHub
// comment… (the hub sends it) — and files the item as done, unless something new came into it while the reply was
// out. clientID is the app's own id for the send (client_id): a retry of the same request is sent once; empty, one is
// derived from the item and the text. fromCard: it is "Send" on a message card (sendFromCard), which does not reply to an
// item filed since (errInboxItemDone). Checked with the copy for the reply, so an item filed after sendFromCard
// looked is not answered and keeps its mark (#4871).
func (s *Service) replyInboxFrom(ctx context.Context, id, text, clientID string, fromCard bool, changes ...DocChange) (InboxItem, error) {
	return s.replyInboxFromVersion(ctx, id, text, clientID, fromCard, 0, changes...)
}

// replyInboxFromVersion carries the suggestion version rendered on a Workbench card. Zero is an old unversioned card;
// it remains valid only outside reply-policy scope.
func (s *Service) replyInboxFromVersion(ctx context.Context, id, text, clientID string, fromCard bool, expectedVersion int, changes ...DocChange) (InboxItem, error) {
	return s.replyInboxDoModeVersion(ctx, id, text, clientID, fromCard, true, false, expectedVersion, changes...)
}

// replyInboxDo is replyInboxFrom; file false keeps the item open (a confirmed suggestion's reply: the event waits for
// the other side), a repeat of the reply still being answered as sent.
func (s *Service) replyInboxDo(ctx context.Context, id, text, clientID string, fromCard, file bool, changes ...DocChange) (InboxItem, error) {
	return s.replyInboxDoFull(ctx, id, text, clientID, fromCard, file, false, 0, nil, changes...)
}

// replyInboxDoMentions is replyInboxDo with explicit bound mentions forwarded to the connector.
func (s *Service) replyInboxDoMentions(ctx context.Context, id, text, clientID string, fromCard, file bool, mentions []InboxMention, changes ...DocChange) (InboxItem, error) {
	return s.replyInboxDoFull(ctx, id, text, clientID, fromCard, file, false, 0, mentions, changes...)
}

// replyInboxDoMode adds the direct /reply route's waiting mode without changing the shared send-once path used by
// handed-off suggestions. followDirect is true only for POST /api/v1/inbox/{id}/reply.
func (s *Service) replyInboxDoMode(ctx context.Context, id, text, clientID string, fromCard, file, followDirect bool, changes ...DocChange) (InboxItem, error) {
	return s.replyInboxDoModeVersion(ctx, id, text, clientID, fromCard, file, followDirect, 0, changes...)
}

func (s *Service) replyInboxDoModeVersion(ctx context.Context, id, text, clientID string, fromCard, file, followDirect bool, expectedVersion int, changes ...DocChange) (InboxItem, error) {
	if out, rerouted, err := s.routeLegacyReplyConfirmation(ctx, id, text, clientID, fromCard, followDirect, true, nil, nil, expectedVersion, changes); rerouted {
		return out, err
	}
	return s.replyInboxDoModeDirect(ctx, id, text, clientID, fromCard, file, followDirect, expectedVersion, changes...)
}

// uncertainLegacyReplyAttempt finds the same unresolved physical action even after its mirror was rejected, replaced,
// or left policy scope. A fresh caller UUID is not evidence that the previous external reply is known not to exist.
// The caller holds inbox.mu.
func uncertainLegacyReplyAttempt(it *InboxItem, text string, changes []DocChange) *InboxSuggestionAttempt {
	if it == nil {
		return nil
	}
	text = strings.TrimSpace(text)
	target := suggestionReplyTarget(it)
	for _, a := range it.SuggestionAttempts {
		if a == nil || a.Status != suggestionUnknown && a.Status != suggestionSending || a.Target != target {
			continue
		}
		actual := actualReply(a)
		if actual == nil {
			if a.Text == text && len(changes) == 0 {
				return a
			}
			continue
		}
		if actual.Text == text && slices.Equal(actual.Changes, changes) {
			return a
		}
	}
	return nil
}

func completedLegacyReplyAttempt(it *InboxItem, text string, changes []DocChange) *InboxSuggestionAttempt {
	if it == nil {
		return nil
	}
	text = strings.TrimSpace(text)
	target := suggestionReplyTarget(it)
	for _, a := range it.SuggestionAttempts {
		if a == nil || a.Status != suggestionSent || a.Target != target {
			continue
		}
		actual := actualReply(a)
		if actual == nil {
			if a.Text == text && len(changes) == 0 {
				return a
			}
			continue
		}
		if actual.Text == text && slices.Equal(actual.Changes, changes) {
			return a
		}
	}
	return nil
}

// routeLegacyReplyConfirmation recognizes a visible old-App AI mirror and a current suggestion whose durable attempt
// survived a restart. It runs before collection or per-item send locks, so the nested suggestion send cannot self-deadlock
// or lose its AI identity while waiting.
func (s *Service) routeLegacyReplyConfirmation(ctx context.Context, id, text, clientID string, fromCard, followDirect, full bool, mailUID *uint32, mailVersion *string, expectedVersion int, changes []DocChange) (InboxItem, bool, error) {
	if suggestionSendOnly(ctx) || !followDirect && !fromCard {
		return InboxItem{}, false, nil
	}
	b := &s.inbox
	b.mu.Lock()
	it := b.items[id]
	var g *InboxSuggestion
	var durable *InboxSuggestionAttempt
	var sources []string
	var uncertain *InboxSuggestionAttempt
	var completed *InboxSuggestionAttempt
	var rejectedDrafts []string
	var out InboxItem
	if it != nil {
		g = mirroredLegacyReply(it)
		if g == nil {
			candidate := currentSuggestion(it)
			if candidate != nil && candidate.Kind == SuggestReply && !candidate.Handback && suggestionAttempt(it, candidate.Version) != nil {
				g = candidate
				durable = suggestionAttempt(it, candidate.Version)
			}
		}
		sources = replyPolicySourceSet(it)
		uncertain = uncertainLegacyReplyAttempt(it, text, changes)
		if uncertain != nil {
			out = it.listed()
		}
		completed = completedLegacyReplyAttempt(it, text, changes)
		if completed != nil {
			out = it.listed()
		}
		if g != nil {
			durable = suggestionAttempt(it, g.Version)
		}
		if g == nil {
			rejectedDrafts = append([]string(nil), it.RejectedReplyDrafts...)
			if it.RejectedReplyDraft != "" {
				rejectedDrafts = append(rejectedDrafts, it.RejectedReplyDraft)
			}
		}
	}
	if g != nil {
		// The suggestion belongs to the live item. Keep the failure tombstone and version checks on the same locked snapshot
		// instead of reading a marker that a concurrent publication can change after b.mu is released.
		snapshot := *g
		g = &snapshot
	}
	b.mu.Unlock()
	if uncertain != nil {
		return out, true, unknownSuggestionReply()
	}
	if completed != nil {
		if mailUID == nil && mailVersion == nil {
			// The successful response may have been lost after its mirror was cleared. The durable sent receipt is the reply;
			// repeating the old request must not mint a fresh direct-send identity.
			return out, true, nil
		}
		var target *inboxMailTarget
		if actual := actualReply(completed); actual != nil {
			target = actual.MailTarget
		}
		if target == nil || mailUID != nil && *mailUID != target.UID || mailVersion != nil && *mailVersion != target.Version {
			return out, true, errMailReplyTargetChanged
		}
		// A checked retry is the same completed operation only when its expectations match the original proposal.
		return out, true, nil
	}
	workbench := fromCard && strings.HasPrefix(clientID, "feishu-workbench-")
	if g == nil && !workbench && len(rejectedDrafts) == 0 {
		return InboxItem{}, false, nil
	}
	if g != nil && (g.ReplyPublicationFailed || g.ReplyPublicationPending) {
		// This mirror never completed publication. Keep it on the suggestion denial path even if policy scope later
		// changes; falling through would reclassify its model text as an owner-authored direct reply.
		out, err := s.actOnSuggestion(ctx, id, suggestionReq{Version: g.Version, Action: suggestConfirm, Text: &text, ClientID: clientID, Changes: &changes}, full)
		return out, true, err
	}
	cfg, err := s.inboxConfigFresh(ctx)
	if err != nil {
		return InboxItem{}, true, &rpc.Error{Code: assistantwire.CodeInboxSendFailed, Message: "reply policy configuration unavailable"}
	}
	if !replyPolicySourcesApply(cfg.ReplyPolicy, sources) {
		return InboxItem{}, false, nil
	}
	if g == nil && slices.Contains(rejectedDrafts, strings.TrimSpace(text)) {
		// Rejection on another device removed the mirror, not its model provenance. Only different owner-authored text
		// may use the direct legacy path; no exact rejected on-demand draft can be revived as one.
		return InboxItem{}, true, errReplyPolicyDenied
	}
	if g != nil && (mailUID != nil || mailVersion != nil) {
		target := g.MailTarget
		if target == nil || mailUID != nil && *mailUID != target.UID || mailVersion != nil && *mailVersion != target.Version {
			return InboxItem{}, true, errMailReplyTargetChanged
		}
	}
	if workbench {
		switch {
		case g == nil:
			return InboxItem{}, true, errSuggestionStale
		case expectedVersion == 0:
			// An old unversioned retry may read back an uncertain or completed outcome, but cannot retry a known-none
			// failed attempt belonging to a versioned replacement card.
			if durable == nil || durable.Text != text || durable.Status == suggestionFailed {
				return InboxItem{}, true, errSuggestionStale
			}
		case g.Version != expectedVersion:
			return InboxItem{}, true, errSuggestionStale
		}
	}
	if g == nil {
		return InboxItem{}, false, nil
	}
	out, err = s.actOnSuggestion(ctx, id, suggestionReq{Version: g.Version, Action: suggestConfirm, Text: &text, ClientID: clientID, Changes: &changes}, full)
	return out, true, err
}

func (s *Service) replyInboxDoModeDirect(ctx context.Context, id, text, clientID string, fromCard, file, followDirect bool, expectedVersion int, changes ...DocChange) (InboxItem, error) {
	return s.replyInboxDoFull(ctx, id, text, clientID, fromCard, file, followDirect, expectedVersion, nil, changes...)
}

func (s *Service) replyInboxDoFull(ctx context.Context, id, text, clientID string, fromCard, file, followDirect bool, expectedVersion int, mentions []InboxMention, changes ...DocChange) (InboxItem, error) {
	b := &s.inbox
	b.mu.Lock()
	it, ok := b.items[id]
	if ok && fromCard && !it.open() {
		b.mu.Unlock()
		return InboxItem{}, errInboxItemDone
	}
	var c InboxItem
	legacyMirrorVersion := 0
	legacyMirrorText := ""
	if ok {
		if !suggestionSendOnly(ctx) {
			if g := mirroredLegacyReply(it); g != nil {
				legacyMirrorVersion, legacyMirrorText = g.Version, g.Text
			}
		}
		c = *it
		if it.Lark != nil { // collection rewrites these fields while the reply is out
			l := *it.Lark
			c.Lark = &l
		}
		if it.Mail != nil {
			m := *it.Mail
			m.UIDs = append([]uint32(nil), it.Mail.UIDs...)
			c.Mail = &m
		}
		if it.Doc != nil {
			d := *it.Doc
			c.Doc = &d
		}
		if it.Moment != nil {
			m := *it.Moment
			c.Moment = &m
		}
		if it.Connector != nil {
			k := *it.Connector
			c.Connector = &k
		}
	}
	b.mu.Unlock()
	if !ok {
		return InboxItem{}, errNoInboxItem
	}
	attempt, _ := ctx.Value(suggestionReplyOnly{}).(*InboxSuggestionAttempt)
	workbenchSend := attempt == nil && fromCard && strings.HasPrefix(clientID, "feishu-workbench-")
	var beforePolicy *assistantwire.InboxReplyPolicyConfig
	beforePolicyRead := false
	if attempt != nil {
		beforePolicy, beforePolicyRead = ctx.Value(suggestionReplyPolicyBeforeSend{}).(*assistantwire.InboxReplyPolicyConfig)
	} else if (followDirect || fromCard) && (legacyMirrorVersion != 0 || workbenchSend) {
		// Preserve the authorization seen before the per-item wait; two post-wait reads could otherwise agree on an
		// already-changed source list or route and authorize the queued physical action.
		beforeCfg, err := s.inboxConfigFresh(ctx)
		if err != nil {
			return InboxItem{}, &rpc.Error{Code: assistantwire.CodeInboxSendFailed, Message: "reply policy configuration unavailable"}
		}
		beforePolicy, beforePolicyRead = beforeCfg.ReplyPolicy, true
	}
	// Phone and watch can both press send, and a timed-out request gets retried: one reply of an item at a time,
	// held until the item is filed (the check for "already sent" relies on it).
	r, _ := b.replies.LoadOrStore(id, &inboxReply{})
	once := r.(*inboxReply)
	if once.beforeLock != nil {
		once.beforeLock()
	}
	once.mu.Lock()
	defer once.mu.Unlock()
	if attempt != nil || legacyMirrorVersion != 0 || workbenchSend {
		// The per-item send mutex and an edit veto may both wait. Reauthorize both schemas and visibility against fresh
		// configuration immediately before the outward call; a display-cache or pre-wait snapshot is not sufficient.
		vetoCfg, err := s.inboxConfigFresh(ctx)
		if err != nil {
			return InboxItem{}, &rpc.Error{Code: assistantwire.CodeInboxSendFailed, Message: "reply policy configuration unavailable"}
		}
		sendText, version := text, legacyMirrorVersion
		if attempt != nil {
			sendText, version = attempt.Text, attempt.Version
		}
		b.mu.Lock()
		live := b.items[id]
		visibilityDenied := live != nil && inboxVisibilityWithMembersExcludedLocked(b, live, vetoCfg.Visibility)
		b.mu.Unlock()
		if visibilityDenied {
			return InboxItem{}, errCannotReply
		}
		if legacyReplyPolicyConfig(vetoCfg.ReplyPolicy) {
			if err := s.vetoEditedReply(ctx, id, suggestionReq{Version: version, Action: suggestConfirm, Text: &sendText}, vetoCfg.ReplyPolicy); err != nil {
				return InboxItem{}, err
			}
		}
		freshCfg, err := s.inboxConfigFresh(ctx)
		if err != nil {
			return InboxItem{}, &rpc.Error{Code: assistantwire.CodeInboxSendFailed, Message: "reply policy configuration unavailable"}
		}
		b.mu.Lock()
		live = b.items[id]
		freshPolicy := compileReplyPolicy(freshCfg.ReplyPolicy)
		vetoApplies := live != nil && replyPolicyItemApplies(vetoCfg.ReplyPolicy, live)
		freshApplies := live != nil && replyPolicyItemApplies(freshCfg.ReplyPolicy, live)
		// Only a change into or within the scope that authorized this reply rejects it; an inert disabled-config edit does not.
		configChanged := freshApplies && (!vetoApplies || !reflect.DeepEqual(vetoCfg.ReplyPolicy, freshCfg.ReplyPolicy))
		if beforePolicyRead && freshApplies {
			// Compare with the authorization seen before the per-item mutex too. Two post-wait reads could otherwise agree
			// on an already-changed source list or route and authorize the queued physical action.
			configChanged = configChanged || !replyPolicyItemApplies(beforePolicy, live) || !reflect.DeepEqual(beforePolicy, freshCfg.ReplyPolicy)
		}
		current := false
		var policyErr error
		var out InboxItem
		if live != nil {
			out = live.listed()
		}
		switch {
		case attempt != nil:
			current = live == it && !configChanged && b.currentReplyAttempt(it, attempt) &&
				replyPolicySendGate(it, attempt.Proposal, freshCfg.ReplyPolicy) == nil &&
				!inboxVisibilityWithMembersExcludedLocked(b, it, freshCfg.Visibility)
			if current {
				policyErr = verifyReplyPolicySendAttemptLocked(it, attempt, freshPolicy)
			}
		case live != nil:
			g := mirroredLegacyReply(live)
			if g != nil {
				current = live == it && !configChanged && g.Version == legacyMirrorVersion && g.Text == legacyMirrorText &&
					(!workbenchSend || expectedVersion == 0 || g.Version == expectedVersion) &&
					replyPolicySendGate(live, g, freshCfg.ReplyPolicy) == nil &&
					!inboxVisibilityWithMembersExcludedLocked(b, live, freshCfg.Visibility)
				if current {
					policyErr = verifyReplyPolicySuggestionLocked(live, g, suggestionAttempt(live, g.Version), freshPolicy)
				}
			} else if workbenchSend && legacyMirrorVersion == 0 {
				// Explicit zero is a transient legacy draft. It remains sendable only while both schemas stayed out of scope.
				current = live == it && !configChanged && !freshApplies && !vetoApplies &&
					(!beforePolicyRead || !replyPolicyItemApplies(beforePolicy, live)) &&
					!inboxVisibilityWithMembersExcludedLocked(b, live, freshCfg.Visibility)
			}
		}
		b.mu.Unlock()
		if configChanged {
			return out, &rpc.Error{Code: assistantwire.CodeInboxSendFailed, Message: "reply policy configuration changed"}
		}
		if !current || policyErr != nil {
			if errors.Is(policyErr, errSuggestionStale) {
				return out, errSuggestionStale
			}
			if policyErr != nil && current {
				return out, errReplyPolicyBlocked
			}
			return out, errSuggestionStale
		}
	}
	if fromCard {
		// Another reply of the item may have been out (the phone's, a repeat): filed while this one waited for it, the
		// item is not answered again from a card either (#5117).
		b.mu.Lock()
		open := it.open()
		b.mu.Unlock()
		if !open {
			return InboxItem{}, errInboxItemDone
		}
	}
	req := inboxReplyReq{id: id, live: it, item: c, text: text, clientID: clientID, mentions: slices.Clone(mentions), changes: changes, once: once,
		sendID: replySendID(id, text, clientID, changes, mentions...)}
	var already bool
	var err error
	switch {
	case c.Connector == nil && migratedThread(&c) != "": // a plugin took Feishu over (inbox_migrate.go)
		already, err = s.replyMigrated(ctx, req)
	case len(changes) > 0:
		return InboxItem{}, i18n.ErrEnglish("inbox-errors.document_only")
	case c.Connector != nil:
		already, err = s.replyConnectorItem(ctx, req)
	case c.Mail != nil:
		already, err = s.replyMail(ctx, req)
	default:
		return InboxItem{}, errCannotReply
	}
	if err != nil {
		return InboxItem{}, err
	}
	if suggestionSendOnly(ctx) {
		// A confirmed suggestion commits under its version/attempt/source fence after this send returns. The
		// direct reply path must not file, mark cards sent or move a replacement suggestion in the meantime.
		b.mu.Lock()
		out := it.listed()
		b.mu.Unlock()
		return out, nil
	}
	now := time.Now()
	if already {
		if once.unsaved { // the reply's item was not written: a repeat writes it (#4627)
			s.saveReplied(ctx, it, once)
		}
		b.mu.Lock()
		out := it.listed()
		b.mu.Unlock()
		s.markCardsSent(ctx, id, now, text, true)
		return out, nil
	}
	var loc *time.Location
	if followDirect {
		loc = s.location(ctx)
	}
	b.mu.Lock()
	// The reply answers what the item held when it was sent. A move that came in meanwhile (collection runs while the
	// reply is out) is not answered: the item stays open with it, so it is not filed unseen (#1828). An item filed
	// meanwhile (read elsewhere) is filed as replied.
	kept := !file && it.open() || it.open() && it.Last != c.Last
	// A direct reply with a processing conversation waits for the other side instead of filing the matter. Old-App
	// card sends keep filing. If new messages arrived while the reply was out, the existing kept-open path is unchanged.
	if followDirect && !fromCard && file {
		resetInboxFollowChecks(it)
	}
	wait := followDirect && !fromCard && file && !kept && it.open() && it.Agent != nil && it.Agent.SessionID != ""
	kept = kept || wait
	once.keptOpen = kept
	if c.Mail != nil {
		once.clientID = text
	}
	b.noteSent(it, now)
	tell := false
	if wait {
		it.Suggestion = nil // the user's own reply supersedes a suggestion still waiting for confirmation
		it.Agent.SentOf = c.Last
		b.waitForOtherSide(it, defaultInboxFollow(now), ViaUser, now, loc, true)
		b.tell(it, inboxNote{Name: prompts.NoteSent, Text: text}, false, now)
		tell = true
	} else if !kept {
		it.done(now, "replied")
		tell = b.closeHandling(it, "replied", now) // its conversation stops
	}
	out := it.listed()
	b.mu.Unlock()
	if !kept || wait {
		s.saveReplied(ctx, it, once)
	} else if !file {
		s.saveInbox(ctx, []*InboxItem{it})
	}
	if tell {
		s.tellSoon(ctx, it)
	}
	// The item's cards in the assistant's conversations show it sent, wherever it was sent from.
	s.markCardsSent(ctx, id, now, text, true)
	return out, nil
}

// saveReplied writes the item after its reply, filed or waiting. The reply went out, so a failed write keeps the item's
// resulting state in memory (rolling it back would let a repeat send the reply again) and is remembered on the item's
// replies: a repeat writes it then, instead of the item returning to its pre-reply state after a restart. With once.mu held.
func (s *Service) saveReplied(ctx context.Context, it *InboxItem, once *inboxReply) {
	failed := s.saveInbox(ctx, []*InboxItem{it})
	once.unsaved = failed[it.ID]
	if once.unsaved {
		s.logf("inbox: %s was replied to but its resulting state could not be saved", it.ID)
	}
}

// replyMail answers the newest mail of the thread from the account that received it (the hub sends it).
func (s *Service) replyMail(ctx context.Context, r inboxReplyReq) (bool, error) {
	m := r.item.Mail
	if m == nil || len(m.UIDs) == 0 {
		return false, errCannotReply
	}
	b := &s.inbox
	b.mu.Lock()
	repeated := !suggestionSendOnly(ctx) && r.once.repeated(r.live, r.text)
	b.mu.Unlock()
	if repeated {
		return true, nil
	}
	var sent assistantwire.InboxSent
	err := s.appCall(ctx, AppInbox, inboxReplyMail, map[string]any{"account_id": m.AccountID, "uid": m.UIDs[len(m.UIDs)-1],
		"mailbox": m.Mailbox, "text": r.text, "send_id": r.sendID}, &sent)
	s.inboxSent(sent, r.sendID)
	// A hub "already" can be the first answer this manager got after an in-doubt send: this manager still writes the
	// resulting item state. Only the local repeat check above may skip that write.
	return false, err
}

// inboxPromptOwner keeps the legacy default while naming the owner in new prompt languages.
func inboxPromptOwner(hello Hello) string {
	if owner := strings.TrimSpace(hello.Owner); owner != "" {
		return owner
	}
	switch answerLanguage(hello.Locale) {
	case i18n.Ja.Name():
		return "ユーザー" // i18n-ignore: the owner's name inside a Japanese model prompt, not interface text
	case "":
		return "用户" // i18n-ignore: the legacy prompt's owner word, kept byte for byte
	default:
		return "the user"
	}
}
