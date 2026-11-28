package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// ---- the suggestion tool (inbox_suggest; inbox_draft is its older name for a reply) ----

// suggestReported is one externally visible step the processing conversation says it or an Agent took. The hub records
// the statement without treating it as execution the program observed.
type suggestReported struct {
	Text string `json:"text"`
	By   string `json:"by"`
}

type suggestIn struct {
	Kind     string           `json:"kind"`
	Text     string           `json:"text"`
	Note     string           `json:"note"`
	Handback bool             `json:"handback"`
	Outcome  string           `json:"outcome"`
	Recap    string           `json:"recap"`
	Title    string           `json:"title"`
	Summary  string           `json:"summary"`
	Machine  string           `json:"machine"`
	Session  string           `json:"session"`
	Cwd      string           `json:"cwd"`
	Due      string           `json:"due"`
	Related  []string         `json:"related"`
	Mentions []InboxMention   `json:"mentions"`
	Changes  []DocChange      `json:"changes"`
	Follow   *suggestFollow   `json:"follow"`
	Reported *suggestReported `json:"reported"`
	// Item names the event: needed from a conversation it was handed to (Hand to AI, inbox_handoff.go).
	Item           string `json:"item"`
	ProcessingTurn string `json:"processing_turn"`
}

var errNotProcessing = i18n.ErrEnglish("inbox-errors.not_processing")

// errSuggestion is a suggestion the tool refuses, said so the model can hand in a better one.
type errSuggestion struct{ msg string }

func (e errSuggestion) Error() string { return e.msg }

var errStaleInboxTurn = errSuggestion{"this Inbox processing turn has been superseded or its processing_turn is missing; nothing was written; end this turn"}

var errSuggestFiled = errSuggestion{"this inbox item is already filed, so the suggestion was not submitted and the user cannot see it; end this turn"}
var errReportedFiled = errSuggestion{"this inbox item is already filed, so the reported step was not recorded; end this turn"}
var errTodoFromHandoff = errSuggestion{"a to-do is suggested by the conversation that processes the item, not one it was handed to"}

// check holds a suggestion to the rules before anything is kept.
func (in *suggestIn) check() error {
	in.Kind, in.Text, in.Outcome = strings.TrimSpace(in.Kind), strings.TrimSpace(in.Text), strings.TrimSpace(in.Outcome)
	in.Machine, in.Session = strings.TrimSpace(in.Machine), strings.TrimSpace(in.Session)
	if in.Kind != "" && in.Follow != nil {
		return errSuggestion{"give either kind (reply, agent, handoff, conclusion or todo) or follow, not both: follow says this turn only waits on the other side"}
	}
	if in.Reported != nil {
		in.Reported.Text = clip(oneLine(strings.TrimSpace(in.Reported.Text)), 120)
		in.Reported.By = strings.TrimSpace(in.Reported.By)
		if in.Reported.Text == "" {
			return errSuggestion{"reported.text is required: say one thing that was actually done"}
		}
		switch in.Reported.By {
		case "":
			in.Reported.By = ViaAssistant
		case ViaAgent, ViaAssistant:
		default:
			return errSuggestion{fmt.Sprintf("reported.by %q: one of agent, assistant", in.Reported.By)}
		}
	}
	switch in.Kind {
	case "":
		if strings.TrimSpace(in.Recap) == "" && in.Reported == nil {
			return errSuggestion{"give kind (reply, agent, handoff, conclusion or todo) with text, at least recap, or a reported step"}
		}
		if in.Session != "" {
			return errSuggestion{"session goes with kind handoff only"}
		}
		return nil
	case SuggestReply, SuggestAgent, SuggestHandoff, SuggestConclusion, SuggestTodo:
	default:
		return errSuggestion{fmt.Sprintf("kind %q: one of reply, agent, handoff, conclusion, todo", in.Kind)}
	}
	if in.Text == "" {
		return errSuggestion{"text is required"}
	}
	if in.Kind == SuggestReply && !in.Handback {
		// The user sends it with one tap: a blank left to fill in (<expected completion time>) would go out as it is.
		if p := draftPlaceholder(in.Text); p != "" {
			return errSuggestion{fmt.Sprintf("the reply leaves %s to fill in, and it goes out as it is: leave out what you could not find (say so in note) and submit again", p)}
		}
	}
	if in.Kind == SuggestHandoff {
		if strings.TrimSpace(in.Item) != "" {
			return errSuggestion{"a handoff is suggested by the conversation that processes the item, not one it was handed to"}
		}
		if in.Machine == "" || in.Session == "" {
			return errSuggestion{"handoff requires both machine and session: choose an eligible conversation listed in the prompt, or fall back to reply/agent"}
		}
	} else if in.Session != "" {
		return errSuggestion{"session goes with kind handoff only"}
	}
	in.Due = strings.TrimSpace(in.Due)
	if in.Kind == SuggestTodo {
		if err := checkSuggestedDue(in.Due); err != nil {
			return err
		}
	} else if in.Due != "" {
		return errSuggestion{"due goes with a to-do only"}
	}
	if in.Kind != SuggestReply && len(in.Changes) > 0 {
		return errSuggestion{"changes go with a reply only"}
	}
	if in.Kind != SuggestReply && len(in.Mentions) > 0 {
		return errSuggestion{"mentions go with a reply only"}
	}
	if len(in.Mentions) > 0 {
		normalized, err := normalizeReplyMentions(in.Text, in.Mentions)
		if err != nil {
			return err
		}
		in.Mentions = normalized
	}
	if in.Kind == SuggestConclusion {
		switch in.Outcome {
		case "":
			in.Outcome = TierFYI
		case TierFYI, "done":
		default:
			return errSuggestion{fmt.Sprintf("outcome %q: fyi or done", in.Outcome)}
		}
	}
	return nil
}

// suggest keeps the suggestion of the conversation sessionID on its event: the item's id, or a change the item does
// not offer (nothing kept then); errNotProcessing when no event's conversation runs there.
func (s *Service) suggest(ctx context.Context, sessionID string, in suggestIn) (string, *DocChange, error) {
	id, refused, _, _, err := s.suggestFrom(ctx, sessionID, in)
	return id, refused, err
}

// suggestFrom is suggest, also naming the conversation the event was handed to when the suggestion came from one
// (in.Item; nil for the event's processing conversation). titleIgnored says a source-named item kept its own title.
func (s *Service) suggestFrom(ctx context.Context, sessionID string, in suggestIn) (string, *DocChange, *SessionRef, bool, error) {
	if err := in.check(); err != nil {
		return "", nil, nil, false, err
	}
	var follow *InboxFollow
	var loc *time.Location
	if in.Follow != nil {
		var err error
		follow, err = in.Follow.follow(time.Now())
		if err != nil {
			return "", nil, nil, false, err
		}
		loc = s.location(ctx)
	}
	if s.inboxReady(ctx) != nil {
		return "", nil, nil, false, errInboxUnavailable
	}
	b := &s.inbox
	cfg, err := s.inboxConfigFresh(ctx)
	if err != nil {
		// Every publication observes the current source-privacy boundary; actionable cards also need the current reply policy.
		return "", nil, nil, false, errReplyPolicyDenied
	}
	replyPolicy := cfg.ReplyPolicy
	visibility := cfg.Visibility
	var policy replyPolicySpec
	policyAuthorization := in.Kind == SuggestReply || in.Kind == SuggestAgent
	if policyAuthorization {
		policy = compileReplyPolicy(replyPolicy)
	}
	b.mu.Lock()
	var it *InboxItem
	var from *SessionRef
	item := strings.TrimSpace(in.Item)
	lookup := func() (*InboxItem, *SessionRef, bool, error) {
		if item != "" {
			got, ref, err := b.handedItem(item, sessionID)
			// While the item's own agent is still starting (SessionID not yet recorded), both a lookup error and a
			// hand-off match are provisional: once Agent.SessionID is populated, handedItem gives the item's own
			// processing conversation precedence over any recorded hand-off to the same session.
			return got, ref, sessionID != "" && (err != nil || ref != nil) && b.itemAgentStarting(item), err
		}
		got := b.itemOfAgent(sessionID)
		if got == nil {
			return nil, nil, sessionID != "" && b.agentStarting(), errNotProcessing
		}
		return got, nil, false, nil
	}
	var waitMore bool
	it, from, waitMore, err = lookup()
	// A conversation that answers before its start returned (an agent runtime already running starts a turn at
	// once): its item learns the conversation's id a moment later.
	waitedForAssociation := false
	for wait := time.Now().Add(inboxDraftWait); waitMore && time.Now().Before(wait); {
		waitedForAssociation = true
		hook := b.waitHook
		b.mu.Unlock()
		if hook != nil {
			hook()
		} else {
			time.Sleep(100 * time.Millisecond)
		}
		b.mu.Lock()
		it, from, waitMore, err = lookup()
	}
	if waitedForAssociation && err == nil {
		// The first fresh read can predate a 15-second association wait. Reread after the wait, without holding the
		// inbox lock across the RPC, then re-resolve the item before authorizing publication.
		itemID, waitingFrom := it.ID, from
		b.mu.Unlock()
		cfg, freshErr := s.inboxConfigFresh(ctx)
		b.mu.Lock()
		if freshErr != nil {
			b.mu.Unlock()
			return "", nil, nil, false, errReplyPolicyDenied
		}
		associated, associatedFrom, _, associationErr := lookup()
		sameFrom := waitingFrom == nil && associatedFrom == nil ||
			waitingFrom != nil && associatedFrom != nil && *waitingFrom == *associatedFrom
		if associationErr != nil {
			b.mu.Unlock()
			return "", nil, nil, false, associationErr
		}
		if associated == nil || associated != it || associated.ID != itemID || b.items[itemID] != associated || !sameFrom {
			b.mu.Unlock()
			return "", nil, nil, false, errNotProcessing
		}
		it, from = associated, associatedFrom
		replyPolicy = cfg.ReplyPolicy
		visibility = cfg.Visibility
		if policyAuthorization {
			policy = compileReplyPolicy(replyPolicy)
		} else {
			policy = replyPolicySpec{}
		}
	}
	if err != nil {
		b.mu.Unlock()
		return "", nil, nil, false, err
	}
	if from != nil && in.Kind == SuggestTodo {
		b.mu.Unlock()
		return "", nil, nil, false, errTodoFromHandoff
	}
	if from == nil && in.ProcessingTurn != it.Agent.ProcessingTurn {
		b.mu.Unlock()
		return "", nil, nil, false, errStaleInboxTurn
	}
	if inboxVisibilityWithMembersExcludedLocked(b, it, visibility) {
		b.mu.Unlock()
		return "", nil, nil, false, errCannotReply
	}
	if from == nil && (in.Kind == SuggestReply || in.Kind == SuggestAgent) && it.Agent != nil &&
		(replyPolicyItemApplies(replyPolicy, it) || policy.appliesToItem(it)) {
		for _, note := range it.Agent.Pending {
			if note.Name == prompts.NoteNewMessages {
				// Collection has newer content that this turn has not seen. Classification may already have advanced while
				// delivery still waits; publishing now would stamp old text with the new target and authorization.
				b.mu.Unlock()
				return "", nil, nil, false, errStaleInboxTurn
			}
		}
	}
	if from == nil && (in.Kind != "" || in.Follow != nil) && !it.open() {
		b.mu.Unlock()
		return "", nil, nil, false, errSuggestFiled
	}
	if in.Reported != nil && !it.open() {
		b.mu.Unlock()
		return "", nil, nil, false, errReportedFiled
	}
	var handoffTarget inboxHandoffTarget
	if in.Kind == SuggestHandoff {
		itemID, todoID := it.ID, ""
		if it.Agent != nil {
			todoID = it.Agent.TodoID
		}
		b.mu.Unlock()
		var err error
		handoffTarget, err = s.validateHandoffTarget(ctx, todoID, sessionID, in.Machine, in.Session)
		if err != nil {
			return "", nil, nil, false, err
		}
		b.mu.Lock()
		it = b.items[itemID]
		if it == nil || it.Agent == nil || it.Agent.SessionID != sessionID {
			b.mu.Unlock()
			return "", nil, nil, false, errNotProcessing
		}
		if in.ProcessingTurn != it.Agent.ProcessingTurn {
			b.mu.Unlock()
			return "", nil, nil, false, errStaleInboxTurn
		}
		if !it.open() {
			b.mu.Unlock()
			return "", nil, nil, false, errSuggestFiled
		}
		if handoffTarget.Role == handoffRoleTask && it.Agent.TodoID != todoID {
			b.mu.Unlock()
			return "", nil, nil, false, errSuggestion{"the item's linked task changed while validating the handoff target: choose an eligible target or fall back to reply/agent"}
		}
	}
	instructionID := ""
	if (in.Kind == SuggestReply || in.Kind == SuggestAgent) && legacyReplyPolicyConfig(replyPolicy) && replyPolicyItemApplies(replyPolicy, it) {
		if instruction, needsClassification := replyInstructionForSuggestion(it, in.Text, replyPolicy); instruction != nil {
			instructionID = instruction.ID
			if needsClassification {
				// An unconsumed owner instruction is part of the generated replacement's authority. Intervening reported
				// progress does not consume it; classify with the original conversation, then revalidate the whole item.
				before := itemState(it)
				itemID, conversation := it.ID, it.Context
				instructionText, policy := instruction.Detail, replyPolicy
				b.mu.Unlock()
				category, classifyErr := s.classifyReplyPolicyInput(ctx, policy, prompts.InboxReplyPolicyInput{
					Text: in.Text, Original: conversation + "\n\nOwner draft instruction:\n" + instructionText, Edit: true,
				})
				freshCfg, freshErr := s.inboxConfigFresh(ctx)
				b.mu.Lock()
				if freshErr != nil {
					b.mu.Unlock()
					return itemID, nil, nil, false, errReplyPolicyDenied
				}
				if b.items[itemID] != it || !bytes.Equal(before, itemState(it)) {
					b.mu.Unlock()
					return itemID, nil, nil, false, errStaleInboxTurn
				}
				if inboxVisibilityWithMembersExcludedLocked(b, it, freshCfg.Visibility) {
					b.mu.Unlock()
					return itemID, nil, nil, false, errCannotReply
				}
				visibility = freshCfg.Visibility
				if !reflect.DeepEqual(policy, freshCfg.ReplyPolicy) {
					b.mu.Unlock()
					return itemID, nil, nil, false, errReplyPolicyDenied
				}
				replyPolicy = freshCfg.ReplyPolicy
				if classifyErr != nil || replyRoute(category, policy) != ReplyRouteOwnerDraft {
					b.mu.Unlock()
					return itemID, nil, nil, false, errReplyPolicyDenied
				}
			}
		}
	}
	if legacyReplyPolicyConfig(replyPolicy) {
		if err := replyPolicySuggestionGate(it, in.Kind, replyPolicy); err != nil {
			b.mu.Unlock()
			return it.ID, nil, nil, false, err
		}
	}
	for _, ch := range in.Changes {
		if it.Doc == nil || !docOffers(*it.Doc, ch) {
			b.mu.Unlock()
			return it.ID, &ch, nil, false, nil
		}
	}
	var policySnap *InboxReplyPolicySnapshot
	if policy.appliesToItem(it) && (in.Kind == SuggestReply || in.Kind == SuggestAgent) && it.open() {
		instruction, needsInstructionReview := replyInstructionForSuggestion(it, in.Text, policy)
		if instruction != nil {
			instructionID = instruction.ID
		}
		// Every policy-applicable reply, Agent instruction, and handback needs a snapshot bound to its exact text.
		// An active rewrite adds its instruction to the classifier input; it never substitutes for that snapshot.
		itemData := snapshotItemForReplyPolicyLocked(it)
		classifierItem := itemData
		if needsInstructionReview {
			classifierItem = replyPolicyItemWithInstruction(classifierItem, instruction)
		}
		existingSnap := cloneReplyPolicySnapshot(it.ReplyPolicy)
		b.mu.Unlock()
		snap, gateErr := s.gateReplyPolicySuggestion(ctx, policy, classifierItem, existingSnap, in.Text)
		freshCfg, freshErr := s.inboxConfigFresh(ctx)
		b.mu.Lock()
		if freshErr != nil {
			b.mu.Unlock()
			return "", nil, nil, false, errReplyPolicyBlocked
		}
		livePolicy := compileReplyPolicy(freshCfg.ReplyPolicy)
		if gateErr != nil {
			b.mu.Unlock()
			return "", nil, nil, false, replyPolicySuggestionErr(snap, gateErr)
		}
		if b.items[itemData.id] != it || !it.open() {
			b.mu.Unlock()
			return "", nil, nil, false, errSuggestFiled
		}
		if from == nil && (it.Agent == nil || in.ProcessingTurn != it.Agent.ProcessingTurn) {
			b.mu.Unlock()
			return "", nil, nil, false, errStaleInboxTurn
		}
		if inboxVisibilityWithMembersExcludedLocked(b, it, freshCfg.Visibility) {
			b.mu.Unlock()
			return "", nil, nil, false, errCannotReply
		}
		visibility = freshCfg.Visibility
		if !livePolicy.appliesToItem(it) || livePolicy.revision != policy.revision || replyPolicyContentVersion(it) != itemData.contentVersion ||
			livePolicy.allowsOwnerDraft(snap, itemData.contentVersion) != nil {
			b.mu.Unlock()
			return "", nil, nil, false, replyPolicySuggestionErr(snap, errReplyPolicyStaleSnapshot)
		}
		policySnap = snap
	}
	now := time.Now().UTC()
	w := beginWrite(it)
	if r := in.Reported; r != nil {
		duplicate := false
		for i := len(it.Chain) - 1; i >= 0; i-- {
			if it.Chain[i].Kind != ChainReported {
				continue
			}
			duplicate = it.Chain[i].Detail == r.Text
			break
		}
		if !duplicate {
			b.chainAdd(it, InboxChainEntry{Kind: ChainReported, At: now,
				Text:   i18n.T(b.userLang(), "assistant-inbox_chain.reported", i18n.Args{"text": r.Text}),
				Detail: r.Text, Via: r.By})
		}
	}
	if r := strings.TrimSpace(in.Recap); r != "" {
		it.Recap = clip(r, recapMax)
	}
	titleIgnored := false
	if title := strings.TrimSpace(in.Title); title != "" {
		if suggestWritesTitle(it) {
			it.Title, it.TitleBy = clip(oneLine(title), 40), TitleByAssistant
		} else {
			titleIgnored = true
		}
	}
	if summary := strings.TrimSpace(in.Summary); summary != "" {
		it.Summary, it.TitleBy = clip(oneLine(summary), 80), TitleByAssistant
	}
	for _, id := range in.Related {
		relate(it, b.items[strings.TrimSpace(id)])
	}
	var card *MessageCard
	var handed *handoffCard
	stop := false
	if from == nil && in.Kind == "" && in.Follow == nil && it.open() && it.Handling != nil && it.Handling.State == HandlingLooking {
		// No kind or follow: this turn offers nothing new to confirm, so the event goes back to where it was.
		back := it.Agent.Back
		if it.Suggestion != nil || back == "" || back == HandlingLooking || back == HandlingFailed {
			back = HandlingDone
		}
		it.Agent.Wake = false
		if back == HandlingWaiting {
			agentStopped := len(it.Chain) > 0 && it.Chain[len(it.Chain)-1].Kind == ChainAgentStopped
			b.resumeInboxWaiting(it, now)
			if agentStopped {
				// The stop turn's recap consumed its restoration point; later turns must not restore it again.
				it.Agent.Back = ""
			}
		} else {
			b.setHandling(it, back, "", now)
		}
	}
	if in.Kind != "" && it.open() {
		if from != nil {
			for i := range it.Handoffs {
				if it.Handoffs[i].MachineID == from.MachineID && it.Handoffs[i].SessionID == from.SessionID {
					it.Handoffs[i].PendingSuggestion = false
				}
			}
		}
		if legacySuggestion(it) != nil {
			it.SuggestSeq = max(it.SuggestSeq, 1)
		}
		g := &InboxSuggestion{Version: it.SuggestSeq + 1, Kind: in.Kind, Text: in.Text, Note: clip(strings.TrimSpace(in.Note), 600),
			Handback: in.Handback, Due: in.Due, Mentions: slices.Clone(in.Mentions), Changes: in.Changes, At: now, Cwd: strings.TrimSpace(in.Cwd), Conversation: from,
			ReplyPolicy: cloneReplyPolicySnapshot(policySnap), ReplyInstruction: instructionID}
		if policySnap != nil {
			it.ReplyPolicy = cloneReplyPolicySnapshot(policySnap)
		}
		if from != nil {
			owner := *from
			g.Owner = &owner
		} else if it.Agent != nil {
			g.Owner = &SessionRef{MachineID: it.Agent.MachineID, SessionID: it.Agent.SessionID}
		}
		g.SourceOf = suggestionReplyTarget(it)
		g.ReplyInstruction = instructionID
		snapshotReplyPolicy(g, it, replyPolicy)
		if in.Kind == SuggestReply && !in.Handback {
			g.MailTarget = mailTarget(*it)
		}
		if in.Kind == SuggestAgent {
			if m, ok := s.machineByName(ctx, strings.TrimSpace(in.Machine)); ok {
				g.MachineID = m.ID
			}
		}
		if in.Kind == SuggestHandoff {
			g.MachineID = handoffTarget.MachineID
			g.SessionID = handoffTarget.SessionID
			g.TargetTitle = handoffTarget.Title
		}
		if samePendingReply(it, g) {
			// The reply and its authority/target are unchanged. Keep its card/version and any failed or uncertain
			// attempt, while still saving this call's independently supplied recap/title/relationship updates.
			goto suggestionKept
		}
		it.SuggestSeq++
		it.Suggestion = g
		inheritReplyUncertainty(it, g, g.Text)
		var link *InboxChainLink
		if from == nil {
			it.Agent.Wake, it.Agent.Back = false, ""
			b.setHandling(it, HandlingDone, "", now)
			link = &InboxChainLink{Type: LinkSession, MachineID: it.Agent.MachineID, SessionID: it.Agent.SessionID}
		} else {
			clearDraft(it)
			link = &InboxChainLink{Type: LinkSession, MachineID: from.MachineID, SessionID: from.SessionID}
		}
		l := b.userLang()
		switch {
		case in.Kind == SuggestConclusion:
			b.chainAdd(it, InboxChainEntry{Kind: ChainConcluded, At: now, Text: i18n.T(l, "assistant-inbox_chain.concluded"), Detail: in.Text})
			if in.Outcome == "done" {
				it.Suggestion = nil
				b.file(it, now, doneByManager)
				if from != nil {
					stop = b.closeHandling(it, "read", now) // a processing conversation still on it stops
				}
			} else {
				it.Tier = TierFYI
			}
		default:
			var chainText string
			switch {
			case in.Handback:
				chainText = i18n.T(l, "assistant-inbox_chain.suggested_handback")
			case in.Kind == SuggestHandoff:
				if handoffTarget.Main {
					chainText = i18n.T(l, "assistant-inbox_chain.suggested_handoff_main")
				} else if title := clip(oneLine(handoffTarget.Title), 40); title != "" {
					chainText = i18n.T(l, "assistant-inbox_chain.suggested_handoff", i18n.Args{"title": title})
				} else {
					chainText = i18n.T(l, "assistant-inbox_chain.suggested_handoff_generic")
				}
			case in.Kind == SuggestAgent:
				chainText = i18n.T(l, "assistant-inbox_chain.suggested_agent")
			case in.Kind == SuggestTodo:
				chainText = i18n.T(l, "assistant-inbox_chain.suggested_todo")
			default:
				chainText = i18n.T(l, "assistant-inbox_chain.suggested_reply")
			}
			b.chainAdd(it, InboxChainEntry{Kind: ChainHandling, At: now, Text: chainText, Detail: clip(in.Text, 600), Link: link})
		}
		if in.Kind == SuggestReply && from == nil {
			card = messageCardOf(it)
		}
		handed = handoffCardOf(it, g, in.Outcome)
	}
suggestionKept:
	if in.Follow != nil && it.open() {
		if g := legacySuggestion(it); g != nil {
			it.SuggestSeq = max(it.SuggestSeq, 1)
			it.Suggestion = g
		}
		keepAgent := from == nil && it.Handling != nil && it.Handling.State == HandlingAgent
		b.waitForOtherSide(it, follow, ViaAssistant, now, loc, true)
		if keepAgent {
			// The follow is this turn's outcome, but the current Agent still owns the matter until it stops.
			it.Agent.Back = HandlingWaiting
			b.setHandling(it, HandlingAgent, "", now)
		}
	}
	id := it.ID
	version := it.SuggestSeq
	b.settle(w)
	b.mu.Unlock()
	// An undone suggestion gets no card, and the conversation hears it failed: the inbox must not promise what a
	// restart would lose. Undone, the event is back where the turn found it, so handing it in again works. One that
	// something else moved meanwhile stays (that move's save writes the row) and goes on as kept.
	if s.saveWrites(ctx, []*inboxWrite{w}, "its suggestion"); w.undone {
		return id, nil, from, titleIgnored, errInboxNotPersisted
	}
	if sent, err := s.autoSendSuggestion(ctx, id, version); sent || err != nil {
		return id, nil, from, titleIgnored, err
	}
	if card != nil {
		s.messageCard(ctx, id, *card)
	}
	if handed != nil {
		s.postHandoffCard(ctx, *from, id, *handed)
	}
	if stop {
		s.tellSoon(ctx, it)
	}
	return id, nil, from, titleIgnored, nil
}

// suggestWritesTitle is whether the processing conversation may name a chat event. Grouped events keep their
// connector block too, so Group / Lark identify the chat; connector threads and source-titled items keep their title.
func suggestWritesTitle(it *InboxItem) bool {
	return (it.Group != nil || it.Lark != nil) && it.Mail == nil && it.Doc == nil && it.Moment == nil
}

// inboxDraft keeps the reply drafted in conversation sessionID (inbox_draft, the older name): suggest with kind reply.
func (s *Service) inboxDraft(ctx context.Context, sessionID, text, note string, proposed []DocChange, processingTurn string) (string, *DocChange, error) {
	id, refused, err := s.suggest(ctx, sessionID, suggestIn{Kind: SuggestReply, Text: text, Note: note, Changes: proposed, ProcessingTurn: processingTurn})
	var se errSuggestion
	if err != nil && !errors.Is(err, errInboxNotPersisted) && !errors.Is(err, errSuggestFiled) && !errors.Is(err, errStaleInboxTurn) && !errors.As(err, &se) {
		return "", nil, nil
	}
	return id, refused, err
}

func (s *Service) toolInboxSuggest(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var in suggestIn
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	_, refused, from, titleIgnored, err := s.suggestFrom(ctx, self, in)
	if err != nil {
		return "", err
	}
	if refused != nil {
		return "", fmt.Errorf("change %+v is not one the item lists (ref, field and one of its values)", *refused)
	}
	notice := ""
	if titleIgnored {
		notice = " The title was left unchanged because this item keeps its source's own title; the title argument was ignored."
	}
	recorded := ""
	if in.Reported != nil {
		recorded = "The reported step is recorded. "
	}
	if in.Follow != nil {
		message := "The recap and follow-up are updated; the item is waiting on the other side."
		if in.Reported != nil && strings.TrimSpace(in.Recap) == "" {
			message = "The follow-up is updated; the item is waiting on the other side."
		}
		return recorded + message + notice, nil
	}
	if in.Kind == "" {
		if in.Reported != nil && strings.TrimSpace(in.Recap) == "" {
			return strings.TrimSpace(recorded) + notice, nil
		}
		return recorded + "The recap is updated." + notice, nil
	}
	if from != nil {
		return recorded + "The suggestion is in the user's inbox and shown in this chat as a card (no need to repeat it in full); nothing goes out until they confirm. What they do with it is told here; later messages on the item are not." + notice, nil
	}
	return recorded + "The suggestion is in the user's inbox; nothing goes out until they confirm. End this turn: what comes next (their answer, new messages) is sent here." + notice, nil
}

// legacySuggestion is an AI twin draft stored before suggestions, as a reply suggestion (version 1); nil for none.
func legacySuggestion(it *InboxItem) *InboxSuggestion {
	a := it.Agent
	if it.Suggestion != nil || it.Handling != nil || a == nil || a.State != AgentDrafted || strings.TrimSpace(a.Draft) == "" {
		return nil
	}
	g := &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: a.Draft, Note: a.Note, Changes: a.Changes}
	g.At = it.UpdatedAt.UTC()
	if a.DraftedAt != nil {
		g.At = a.DraftedAt.UTC()
	}
	return g
}

// currentSuggestion is the item's suggestion as listed (an old draft's too).
func currentSuggestion(it *InboxItem) *InboxSuggestion {
	if it.Suggestion != nil {
		return it.Suggestion
	}
	return legacySuggestion(it)
}

var (
	mentionFenceOpenRe  = regexp.MustCompile("^```[a-zA-Z0-9_+#-]*\\s*$")
	mentionFenceCloseRe = regexp.MustCompile("^```\\s*$")
	mentionCodeOrLinkRe = regexp.MustCompile("`[^`\\n]+`|\\[[^\\]\\n]+\\]\\(https?://[^\\s<>()]+\\)")
)

func isMentionBoundary(rest string) bool {
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-')
}

func mentionPresentInSegment(segment, needle string) bool {
	for offset := 0; offset <= len(segment)-len(needle); {
		idx := strings.Index(segment[offset:], needle)
		if idx < 0 {
			return false
		}
		end := offset + idx + len(needle)
		if isMentionBoundary(segment[end:]) {
			return true
		}
		offset += idx + 1
	}
	return false
}

// mentionPresentInText reports whether @name appears as a complete mention token in prose outside fenced code blocks,
// inline code spans, and Markdown HTTP(S) links, matching the connector mention boundary rules.
func mentionPresentInText(text, name string) bool {
	if text == "" || name == "" {
		return false
	}
	needle := "@" + name
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
	for i := 0; i < len(lines); {
		if mentionFenceOpenRe.MatchString(lines[i]) {
			j := i + 1
			for j < len(lines) && !mentionFenceCloseRe.MatchString(lines[j]) {
				j++
			}
			if j < len(lines) {
				i = j + 1
				continue
			}
		}
		line := lines[i]
		pos := 0
		for _, loc := range mentionCodeOrLinkRe.FindAllStringIndex(line, -1) {
			if loc[0] > pos && mentionPresentInSegment(line[pos:loc[0]], needle) {
				return true
			}
			pos = loc[1]
		}
		if pos < len(line) && mentionPresentInSegment(line[pos:], needle) {
			return true
		}
		i++
	}
	return false
}

// normalizeReplyMentions validates and deduplicates explicit mention bindings submitted with a reply suggestion.
func normalizeReplyMentions(text string, raw []InboxMention) ([]InboxMention, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > 100 {
		return nil, errSuggestion{"mentions must contain at most 100 entries"}
	}
	out := make([]InboxMention, 0, len(raw))
	byID := make(map[string]string, len(raw))
	byName := make(map[string]string, len(raw))
	for _, m := range raw {
		id := strings.TrimSpace(string(m.ID))
		if id == "" || strings.ContainsAny(id, " \t\r\n") {
			return nil, errSuggestion{"each mention requires a non-empty id without whitespace"}
		}
		name := m.Name
		if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "@\r\n") {
			return nil, errSuggestion{"each mention name must be one trimmed non-empty line without @"}
		}
		if prevName, exists := byID[id]; exists {
			if prevName != name {
				return nil, errSuggestion{"duplicate mention id has conflicting names"}
			}
			continue
		}
		if prevID, exists := byName[name]; exists && prevID != id {
			return nil, errSuggestion{"two mentioned people cannot share the same name"}
		}
		if !mentionPresentInText(text, name) {
			return nil, errSuggestion{fmt.Sprintf("mention %q must appear as @%s in text", name, name)}
		}
		byID[id] = name
		byName[name] = id
		out = append(out, InboxMention{ID: InboxMentionUserID(id), Name: name})
	}
	return out, nil
}

// activeReplyMentions retains only bound mentions whose complete @name token still appears in prose after user edits.
func activeReplyMentions(text string, mentions []InboxMention) []InboxMention {
	if len(mentions) == 0 || text == "" {
		return nil
	}
	var out []InboxMention
	for _, m := range mentions {
		if m.ID != "" && m.Name != "" && mentionPresentInText(text, m.Name) {
			out = append(out, m)
		}
	}
	return out
}
