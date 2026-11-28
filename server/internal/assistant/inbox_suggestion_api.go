package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// ---- POST /api/v1/inbox/{id}/suggestion ----

// Actions on a suggestion.
const (
	suggestConfirm  = "confirm"
	suggestReject   = "reject"
	suggestInstruct = "instruct"
)

type suggestionReq struct {
	Version int     `json:"version"`
	Action  string  `json:"action"`
	Text    *string `json:"text"`
	// Changes is the exact document-move selection a legacy mirror confirmation made. Nil preserves the card's stored
	// selection; a non-nil empty slice explicitly deselects every stored move.
	Changes *[]DocChange `json:"changes,omitempty"`
	// Due is a to-do suggestion's due time as the user left it (a day or a moment, "" none); absent keeps the suggestion's.
	Due      *string `json:"due"`
	ClientID string  `json:"client_id"`
}

var (
	errSuggestionStale = i18n.ErrEnglish("inbox-errors.suggestion_stale")
	errAnswerOnly      = i18n.ErrEnglish("inbox-errors.answer_only")
)

func (s *Service) inboxSuggestionRoutes() {
	s.handle("POST /api/v1/inbox/{id}/suggestion", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in suggestionReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		switch in.Action {
		case suggestConfirm, suggestReject:
		case suggestInstruct:
			if in.Text == nil || strings.TrimSpace(*in.Text) == "" {
				apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.text_required"))
				return
			}
		default:
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.suggestion_action"))
			return
		}
		it, err := s.actOnSuggestion(r.Context(), r.PathValue("id"), in, fullRole(r))
		l := i18n.OfWriter(w)
		switch {
		case errors.Is(err, errNoInboxItem):
			apierr.Write(w, http.StatusNotFound, err)
		case errors.Is(err, errSuggestionStale):
			apierr.WriteBody(w, http.StatusConflict, map[string]any{"code": "suggestion_stale", "error": i18n.T(l, "assistant-inbox.suggestion_stale"), "item": it})
		case errors.Is(err, errInboxItemDone):
			apierr.WriteBody(w, http.StatusConflict, map[string]any{"code": "inbox_item_done", "error": i18n.T(l, "assistant-inbox.item_done"), "retryable": false, "delivered": "none"})
		case errors.Is(err, errAnswerOnly):
			apierr.Write(w, http.StatusForbidden, err)
		case errors.Is(err, errMailReplyTargetChanged):
			apierr.WriteBody(w, http.StatusConflict, map[string]any{"code": "mail_reply_target_changed", "error": i18n.T(l, "assistant-inbox.mail_target_changed"), "retryable": false, "delivered": "none", "item": it})
		case errors.As(err, new(errSuggestion)):
			apierr.WriteBody(w, http.StatusConflict, map[string]any{"code": "handoff_target_unavailable", "error": err.Error(), "retryable": false, "delivered": "none", "item": it})
		case errors.Is(err, errReplyPolicyBlocked):
			apierr.WriteBody(w, http.StatusConflict, map[string]any{"code": assistantwire.CodeInboxReplyPolicyBlocked, "error": i18n.T(l, "assistant-inbox.reply_policy_blocked"), "retryable": false, "delivered": "none", "item": it})
		case err != nil && s.writeInboxSendError(w, r, "suggestion", r.PathValue("id"), err, it):
			// in doubt (delivered unknown), or why nothing was sent (#9125)
		case err != nil:
			apierr.Write(w, http.StatusBadGateway, err)
		default:
			apierr.WriteJSON(w, it)
		}
	})
}

// actOnSuggestion confirms, rejects or answers the item's current suggestion, at the version the user saw. An
// instruction at version 0 is also accepted when no suggestion is current. full is false for an answer-only device
// (the watch): it may only confirm a reply or a conclusion, as text.
func (s *Service) actOnSuggestion(ctx context.Context, id string, in suggestionReq, full bool) (InboxItem, error) {
	inboxCfg := s.inboxConfig(ctx)
	var preVeto *InboxReplyPolicySnapshot
	var vetoErr error
	if in.Action == suggestConfirm {
		b := &s.inbox
		b.mu.Lock()
		it := b.items[id]
		policyConfirm, policyReply, edited := false, false, false
		var itemData replyPolicyItemData
		if it != nil {
			g := currentSuggestion(it)
			if g != nil && g.Kind == SuggestReply && !g.Handback && g.Version == in.Version {
				if a := suggestionAttempt(it, g.Version); a != nil && (a.Status == suggestionSending || a.Status == suggestionUnknown) {
					out := it.listed()
					b.mu.Unlock()
					return out, unknownSuggestionReply()
				}
				if g.ReplyPublicationFailed || g.ReplyPublicationPending {
					out := it.listed()
					b.mu.Unlock()
					return out, errInboxNotPersisted
				}
			}
			policyReply = g != nil && g.Kind == SuggestReply && !g.Handback && g.Version == in.Version
			policyConfirm = g != nil && (g.Kind == SuggestReply || g.Kind == SuggestAgent) && g.Version == in.Version
			if policyConfirm && in.Text != nil {
				proposed := strings.TrimSpace(*in.Text)
				edited = proposed != "" && proposed != g.Text
				if edited {
					itemData = snapshotItemForReplyPolicyLocked(it)
					instruction := it.ReplyInstruction
					if instruction == nil {
						instruction = chainReplyInstruction(it.Chain)
					}
					itemData = replyPolicyItemWithInstruction(itemData, instruction)
				}
			}
		}
		b.mu.Unlock()
		if policyConfirm {
			// Reply and Agent confirmations use fresh authorization. The edit veto runs before consuming any card or
			// reserving a send, and a second fresh read closes a policy transition during its model call.
			vetoCfg, err := s.inboxConfigFresh(ctx)
			if err != nil {
				return InboxItem{}, errReplyPolicyBlocked
			}
			b.mu.Lock()
			live := b.items[id]
			visibilityDenied := live != nil && inboxVisibilityWithMembersExcludedLocked(b, live, vetoCfg.Visibility)
			var visibilityItem InboxItem
			if visibilityDenied {
				visibilityItem = live.listed()
			}
			b.mu.Unlock()
			if visibilityDenied {
				return visibilityItem, errCannotReply
			}
			if legacyReplyPolicyConfig(vetoCfg.ReplyPolicy) {
				vetoErr = s.vetoEditedReply(ctx, id, in, vetoCfg.ReplyPolicy)
				freshCfg, err := s.inboxConfigFresh(ctx)
				if err != nil {
					return InboxItem{}, errReplyPolicyDenied
				}
				b.mu.Lock()
				live = b.items[id]
				vetoApplies := live != nil && replyPolicyItemApplies(vetoCfg.ReplyPolicy, live)
				freshApplies := live != nil && replyPolicyItemApplies(freshCfg.ReplyPolicy, live)
				configChanged := live != nil && freshApplies &&
					(!vetoApplies || !reflect.DeepEqual(vetoCfg.ReplyPolicy, freshCfg.ReplyPolicy))
				visibilityDenied = live != nil && inboxVisibilityWithMembersExcludedLocked(b, live, freshCfg.Visibility)
				if visibilityDenied {
					visibilityItem = live.listed()
				}
				b.mu.Unlock()
				if visibilityDenied {
					return visibilityItem, errCannotReply
				}
				if vetoErr != nil {
					return InboxItem{}, vetoErr
				}
				if configChanged {
					return InboxItem{}, errReplyPolicyDenied
				}
				// Agent starts and handbacks must be denied before their cards are consumed.
				b.mu.Lock()
				live = b.items[id]
				g := currentSuggestion(live)
				denied := live != nil && g != nil && g.Version == in.Version &&
					(g.Kind == SuggestAgent || g.Kind == SuggestReply && g.Handback) &&
					replyPolicyActionGate(live, g, freshCfg.ReplyPolicy) != nil
				b.mu.Unlock()
				if denied {
					return InboxItem{}, errReplyPolicyDenied
				}
				inboxCfg = freshCfg
			} else {
				vetoPolicy := compileReplyPolicy(vetoCfg.ReplyPolicy)
				b.mu.Lock()
				vetoApplies := vetoPolicy.appliesToItem(b.items[id])
				b.mu.Unlock()
				if edited && vetoApplies {
					preVeto, vetoErr = s.gateReplyPolicySend(ctx, vetoPolicy, itemData, strings.TrimSpace(*in.Text))
				}
				freshCfg, err := s.inboxConfigFresh(ctx)
				if err != nil {
					return InboxItem{}, errReplyPolicyBlocked
				}
				freshPolicy := compileReplyPolicy(freshCfg.ReplyPolicy)
				b.mu.Lock()
				live = b.items[id]
				configChanged := live != nil && replyPolicyItemApplies(freshCfg.ReplyPolicy, live) &&
					(!replyPolicyItemApplies(vetoCfg.ReplyPolicy, live) || !reflect.DeepEqual(vetoCfg.ReplyPolicy, freshCfg.ReplyPolicy))
				visibilityDenied = live != nil && inboxVisibilityWithMembersExcludedLocked(b, live, freshCfg.Visibility)
				if visibilityDenied {
					visibilityItem = live.listed()
				}
				b.mu.Unlock()
				if visibilityDenied {
					return visibilityItem, errCannotReply
				}
				// A stable reply veto uses the existing failed-attempt path so the exact edited text and proposal remain durable.
				// The classifier has already run before any state was consumed. Agent vetoes and policy transitions still return
				// immediately, preserving their no-reservation guarantee.
				recordVeto := vetoErr != nil && policyReply && !configChanged && freshPolicy.enabled &&
					reflect.DeepEqual(vetoCfg.ReplyPolicy, freshCfg.ReplyPolicy)
				if (vetoErr != nil || configChanged) && !recordVeto {
					return InboxItem{}, errReplyPolicyBlocked
				}
				if vetoErr == nil {
					// Agent starts and handbacks otherwise consume their card before their final authorization runs. Check those
					// paths before any state transition. Ordinary replies keep the durable failed-attempt path below.
					b.mu.Lock()
					live = b.items[id]
					g := currentSuggestion(live)
					denied := live != nil && g != nil && g.Version == in.Version &&
						(g.Kind == SuggestAgent || g.Kind == SuggestReply && g.Handback) &&
						verifyReplyPolicySuggestionLocked(live, g, nil, freshPolicy) != nil
					b.mu.Unlock()
					if denied {
						return InboxItem{}, errReplyPolicyBlocked
					}
				}
				inboxCfg = freshCfg
			}
		} else if it != nil {
			// Local to-do/conclusion operations need fresh configuration only when a visibility boundary is configured.
			// With no such boundary, retain upstream's behavior during a reply-policy configuration outage.
			freshCfg, err := s.inboxConfigFresh(ctx)
			if err != nil {
				if inboxCfg.Visibility != nil {
					return InboxItem{}, errCannotReply
				}
			} else {
				inboxCfg = freshCfg
			}
		}
	}
	enabled := inboxCfg.Agent.Enabled
	policy := compileReplyPolicy(inboxCfg.ReplyPolicy)
	b := &s.inbox
	b.mu.Lock()
	it := b.items[id]
	if it == nil {
		b.mu.Unlock()
		return InboxItem{}, errNoInboxItem
	}
	g := currentSuggestion(it)
	switch {
	case !it.open():
		b.mu.Unlock()
		return InboxItem{}, errInboxItemDone
	case g == nil && (in.Version != 0 || in.Action != suggestInstruct):
		out := it.listed()
		b.mu.Unlock()
		return out, errSuggestionStale
	case g != nil && g.Version != in.Version:
		out := it.listed()
		b.mu.Unlock()
		return out, errSuggestionStale
	}
	if inboxVisibilityWithMembersExcludedLocked(b, it, inboxCfg.Visibility) && in.Action != suggestReject {
		out := it.listed()
		b.mu.Unlock()
		return out, errCannotReply
	}
	text := ""
	if g != nil {
		text = g.Text
	}
	if in.Text != nil && strings.TrimSpace(*in.Text) != "" {
		text = strings.TrimSpace(*in.Text)
	}
	if !full && (g == nil || in.Action != suggestConfirm || g.Kind == SuggestAgent || g.Kind == SuggestTodo || g.Kind == SuggestHandoff || g.Handback || len(g.Changes) > 0 || assistantwire.AsksAgent(text)) {
		b.mu.Unlock()
		return InboxItem{}, errAnswerOnly
	}
	if g != nil && in.Action == suggestConfirm {
		// A changed reply recipient has a dedicated stale/mail-target error after unresolved durable outcomes are checked.
		// The expanded compiled content version also names that target; do not mask the more specific result as a policy veto.
		targetChanged := g.Kind == SuggestReply && g.SourceOf != "" && g.SourceOf != suggestionReplyTarget(it)
		if !targetChanged && legacyReplyPolicyConfig(inboxCfg.ReplyPolicy) {
			if err := replyPolicyActionGate(it, g, inboxCfg.ReplyPolicy); err != nil {
				out := it.listed()
				b.mu.Unlock()
				return out, err
			}
		}
	}
	if g != nil && g.Kind == SuggestReply && in.Action == suggestConfirm && !g.Handback {
		return s.confirmSuggestionReplyLocked(ctx, it, g, in, inboxCfg.ReplyPolicy, policy, preVeto, vetoErr)
	}
	if g != nil && g.Conversation != nil {
		resetInboxFollowChecks(it)
		return s.actOnHandedSuggestion(ctx, it, g, in, text) // it lets b.mu go
	}
	w := beginWrite(it)
	resetInboxFollowChecks(it)
	if it.Agent == nil { // an old draft's item, or a first instruction: its conversation's bookkeeping starts here
		it.Agent = &InboxAgent{State: AgentSkipped}
	}
	now := time.Now()
	l := b.userLang()
	edited := g != nil && text != g.Text
	// The suggestion is taken now, under the lock: a second confirmation of it (another device, a retry) is stale.
	if g != nil && g.OnDemand && (in.Action == suggestReject || in.Action == suggestInstruct) {
		rememberRejectedReplyDraft(it, g.Text)
	}
	it.Suggestion = nil
	if in.Action == suggestConfirm || in.Action == suggestReject {
		it.ReplyInstruction = nil
	}
	if it.Agent.State == AgentDrafted {
		it.Agent.State = AgentSkipped
	}
	execute := g != nil && in.Action == suggestConfirm && !g.Handback
	switch {
	case in.Action == suggestReject:
		it.ReplyInstruction = nil
		if g.OnDemand {
			it.RejectedReplyDraft = g.Text // legacy single-value field; keep it for stored-row compatibility
			it.RejectedReplyDrafts = append(it.RejectedReplyDrafts, g.Text)
		}
		b.chainAdd(it, rejectedEntry(l, g.Text, now))
		b.flushInboxWakes(it, now)
		b.tell(it, inboxNote{Name: prompts.NoteRejected, Text: g.Text}, true, now)
		b.setHandling(it, HandlingLooking, "", now)
	case in.Action == suggestInstruct:
		words := strings.TrimSpace(*in.Text)
		b.chainAdd(it, instructedEntry(l, words, now))
		instruction := it.Chain[len(it.Chain)-1]
		it.ReplyInstruction = &instruction
		b.flushInboxWakes(it, now)
		b.tell(it, inboxNote{Name: prompts.NoteInstructed, Text: words}, true, now)
		b.setHandling(it, HandlingLooking, "", now)
	case !execute: // handback: the conversation does it
		b.chainAdd(it, confirmedEntry(l, text, edited, now))
		b.flushInboxWakes(it, now)
		b.tell(it, inboxNote{Name: prompts.NoteHandback, Text: text, Edited: edited}, true, now)
		b.setHandling(it, HandlingLooking, "", now)
	case g.Kind == SuggestConclusion:
		b.chainAdd(it, confirmedEntry(l, text, false, now))
		b.setHandling(it, HandlingDone, "", now)
		b.file(it, now, "user")
	default:
		b.chainAdd(it, confirmedEntry(l, text, edited, now))
	}
	if !enabled && !execute {
		b.releaseInboxAgent(it, HandlingFailed, i18n.T(b.userLang(), "assistant-inbox_agent.disabled"), now, false)
		b.settle(w)
	}
	last, externalAsked := it.Last, it.ExternalAsked
	b.mu.Unlock()
	if !execute || g.Kind == SuggestConclusion {
		if !enabled && !execute {
			if failed := s.saveWrites(ctx, []*inboxWrite{w}, "its suggestion response"); failed[it.ID] {
				return InboxItem{}, errInboxNotPersisted
			}
		} else {
			s.saveInbox(ctx, []*InboxItem{it})
		}
		if enabled && it.Agent != nil && len(it.Agent.Pending) > 0 {
			s.tellSoon(ctx, it)
		}
		b.mu.Lock()
		out := it.listed()
		b.mu.Unlock()
		return out, nil
	}
	if g.Kind == SuggestAgent {
		return s.confirmAgent(ctx, it, g, text, edited)
	}
	if g.Kind == SuggestHandoff {
		return s.confirmHandoff(ctx, it, g, text, in.ClientID)
	}
	if g.Kind == SuggestTodo {
		due := g.Due
		if in.Due != nil {
			due = strings.TrimSpace(*in.Due)
		}
		if externalAsked != "" && g.SourceOf == externalAsked {
			return s.confirmTodo(ctx, it, text, due, g.Note)
		}
		return s.confirmTodo(ctx, it, text, due)
	}
	return s.confirmReply(ctx, it, g, text, edited, last, in.ClientID)
}

func confirmedEntry(l i18n.Lang, text string, edited bool, now time.Time) InboxChainEntry {
	key := "assistant-inbox_chain.confirmed"
	if edited {
		key = "assistant-inbox_chain.confirmed_edited"
	}
	return InboxChainEntry{Kind: ChainConfirmed, At: now.UTC(), Text: i18n.T(l, key), Detail: text, Edited: edited}
}

func rememberRejectedReplyDraft(it *InboxItem, text string) {
	if it == nil || text == "" {
		return
	}
	it.RejectedReplyDraft = text
	for _, old := range it.RejectedReplyDrafts {
		if old == text {
			return
		}
	}
	it.RejectedReplyDrafts = append(it.RejectedReplyDrafts, text)
}

func rejectedEntry(l i18n.Lang, text string, now time.Time) InboxChainEntry {
	return InboxChainEntry{Kind: ChainRejected, At: now.UTC(), Text: i18n.T(l, "assistant-inbox_chain.rejected"), Detail: clip(text, 600)}
}

func instructedEntry(l i18n.Lang, words string, now time.Time) InboxChainEntry {
	return InboxChainEntry{Kind: ChainInstructed, At: now.UTC(), Text: i18n.T(l, "assistant-inbox_chain.instructed",
		i18n.Args{"text": clip(oneLine(words), 40)}), Detail: words}
}

// confirmReply sends the confirmed reply as shown (or as the user edited it) through the same once-only path as
// /reply, but keeps the event open: it waits for the other side (handling waiting), and the conversation is told.
// A send that fails leaves the event Needs you without a suggestion.
func (s *Service) confirmReply(ctx context.Context, it *InboxItem, g *InboxSuggestion, text string, edited bool, last, clientID string) (InboxItem, error) {
	if !validClientID(clientID) { // the same suggestion confirmed again is the same send
		clientID = namedClientID(fmt.Sprintf("suggestion\n%s\n%d", it.ID, g.Version))
	}
	var err error
	if target := g.MailTarget; target != nil {
		_, err = s.replyInboxCheckedDo(ctx, it.ID, text, clientID, false, false, &target.UID, &target.Version, g.Changes...)
	} else if it.Mail != nil {
		// A pre-upgrade mail suggestion cannot prove which message its text answered. Never guess after a read sync.
		err = errMailReplyTargetChanged
	} else {
		// Non-mail suggestions keep their legacy path; new mail suggestions always name a target.
		_, err = s.replyInboxDoMentions(ctx, it.ID, text, clientID, false, false, activeReplyMentions(text, g.Mentions), g.Changes...)
	}
	b := &s.inbox
	now := time.Now()
	loc := s.location(ctx)
	b.mu.Lock()
	if err != nil {
		b.failHandling(it, i18n.T(b.userLang(), "assistant-inbox_chain.failed_send"), now)
	} else {
		it.Agent.SentOf = last
		b.waitForOtherSide(it, defaultInboxFollow(now), ViaUser, now, loc, false)
		b.tell(it, inboxNote{Name: prompts.NoteSent, Text: text, Edited: edited}, false, now)
	}
	out := it.listed()
	b.mu.Unlock()
	s.saveInbox(ctx, []*InboxItem{it})
	if err == nil {
		s.tellSoon(ctx, it)
	}
	return out, err
}

// confirmAgent starts the Agent with the confirmed instruction, as the event's conversation would (agent_start from
// it: the chain links the sub-session; when it stops, its last words or failure wake the conversation for the next suggestion).
func (s *Service) confirmAgent(ctx context.Context, it *InboxItem, g *InboxSuggestion, text string, edited bool) (InboxItem, error) {
	b := &s.inbox
	b.mu.Lock()
	self := it.Agent.SessionID
	b.mu.Unlock()
	hubMachine := s.settings(ctx).MachineID
	machine := g.MachineID
	if machine == "" {
		machine = hubMachine
	}
	cwd := g.Cwd
	if cwd == "" {
		cwd = "~"
	}
	args, _ := json.Marshal(map[string]string{"machine": machine, "cwd": cwd, "prompt": text})
	// Settings resolution can block. Reauthorize the live item and exact suggestion immediately before agent_start.
	freshCfg, err := s.queryInboxConfig(ctx)
	if err != nil {
		err = errReplyPolicyBlocked
	} else {
		b.mu.Lock()
		var legacyErr error
		if legacyReplyPolicyConfig(freshCfg.ReplyPolicy) {
			legacyErr = replyPolicyActionGate(it, g, freshCfg.ReplyPolicy)
		}
		switch {
		case inboxVisibilityWithMembersExcludedLocked(b, it, freshCfg.Visibility):
			err = errCannotReply
		case legacyErr != nil:
			err = legacyErr
		case verifyReplyPolicySuggestionLocked(it, g, nil, compileReplyPolicy(freshCfg.ReplyPolicy)) != nil:
			err = errReplyPolicyBlocked
		}
		b.mu.Unlock()
	}
	res := ""
	if err == nil {
		res, err = s.runTool(ownerConfirmed(ctx), hubMachine, self, "agent_start", args)
	}
	enabled := s.inboxConfig(ctx).Agent.Enabled
	now := time.Now()
	b.mu.Lock()
	if err != nil {
		b.failHandling(it, i18n.T(b.userLang(), "assistant-inbox_chain.failed_agent"), now)
	} else {
		session := ""
		for _, e := range it.Chain {
			if e.Kind == ChainAgent && e.Link != nil {
				session = e.Link.SessionID
			}
		}
		if self == "" { // no conversation recorded the start: the chain still links it
			b.chainAdd(it, InboxChainEntry{Kind: ChainAgent, At: now.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain.agent",
				i18n.Args{"title": clip(oneLine(text), 40)})})
		}
		b.setHandling(it, HandlingAgent, "", now)
		b.tell(it, inboxNote{Name: prompts.NoteAgentStarted, Text: text, Edited: edited, Machine: machine, Session: session}, false, now)
		_ = res
	}
	if !enabled {
		b.releaseInboxAgent(it, HandlingFailed, i18n.T(b.userLang(), "assistant-inbox_agent.disabled"), now, false)
	}
	out := it.listed()
	b.mu.Unlock()
	s.saveInbox(ctx, []*InboxItem{it})
	if err == nil {
		s.tellSoon(ctx, it)
	}
	return out, err
}
