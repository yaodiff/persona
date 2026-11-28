package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	suggestionSending = "sending"
	suggestionFailed  = "failed"
	suggestionUnknown = "unknown"
	suggestionSent    = "sent"
)

// InboxSuggestionAttempt retains a confirmed reply's result independently of the current suggestion. The first
// client ID and payload are authoritative for that version; listed copies omit those internal send bindings.
type InboxSuggestionAttempt struct {
	Version        int              `json:"version"`
	Status         string           `json:"status"`
	Retryable      bool             `json:"retryable"`
	Code           string           `json:"code,omitempty"`
	Text           string           `json:"text"`
	Mentions       []InboxMention   `json:"mentions,omitempty"`
	MentionsSet    bool             `json:"mentions_set,omitempty"`
	At             time.Time        `json:"at"` // latest reservation or outcome, not the replacement suggestion's time
	Conversation   *SessionRef      `json:"conversation,omitempty"`
	RetryAfter     int              `json:"retry_after,omitempty"`
	ClientID       string           `json:"client_id,omitempty"`
	Proposal       *InboxSuggestion `json:"proposal,omitempty"`
	Target         string           `json:"target,omitempty"`
	Category       string           `json:"category,omitempty"`
	Route          string           `json:"route,omitempty"`
	ContentVersion string           `json:"content_version,omitempty"`
	PolicyRevision string           `json:"policy_revision,omitempty"`
	Reviewed       bool             `json:"reviewed,omitempty"`
	active         bool
}

func listedSuggestionAttempts(attempts []*InboxSuggestionAttempt) []*InboxSuggestionAttempt {
	if len(attempts) == 0 {
		return nil
	}
	out := make([]*InboxSuggestionAttempt, 0, len(attempts))
	for _, a := range attempts {
		if a == nil {
			continue
		}
		c := *a
		c.Mentions = slices.Clone(a.Mentions)
		c.MentionsSet = false
		c.ClientID, c.Target, c.Proposal, c.active = "", "", nil, false
		c.Category, c.Route, c.ContentVersion, c.PolicyRevision = "", "", "", ""
		c.Reviewed = false
		if c.Status == suggestionSending && !a.active {
			c.Status, c.Retryable = suggestionUnknown, false
		}
		if c.Conversation != nil {
			ref := *c.Conversation
			c.Conversation = &ref
		}
		out = append(out, &c)
	}
	return out
}

func suggestionAttempt(it *InboxItem, version int) *InboxSuggestionAttempt {
	for _, a := range it.SuggestionAttempts {
		if a != nil && a.Version == version {
			return a
		}
	}
	return nil
}

// Resolved display history uses the item's chain bound. An unresolved operation keeps at least one proof even
// after its card is replaced; otherwise cycling through other suggestions could permit the same uncertain send.
func keepSuggestionAttempt(it *InboxItem, a *InboxSuggestionAttempt) {
	it.SuggestionAttempts = append(it.SuggestionAttempts, a)
	for len(it.SuggestionAttempts) > chainMax {
		i := slices.IndexFunc(it.SuggestionAttempts, func(v *InboxSuggestionAttempt) bool {
			if v == nil {
				return true
			}
			if v == a || v.active || it.Suggestion != nil && v.Version == it.Suggestion.Version {
				return false
			}
			if v.Status != suggestionUnknown && v.Status != suggestionSending {
				return true
			}
			// A projection may take the place of an older display record, but never of the last proof for its
			// exact send identity, actual action and authority. Distinct unresolved operations remain protected.
			return slices.ContainsFunc(it.SuggestionAttempts, func(other *InboxSuggestionAttempt) bool {
				return other != v && other != nil && (other.Status == suggestionUnknown || other.Status == suggestionSending) && sameReplyOperation(v, other)
			})
		})
		if i < 0 {
			break
		}
		it.SuggestionAttempts = slices.Delete(it.SuggestionAttempts, i, i+1)
	}
}

func actualReply(a *InboxSuggestionAttempt) *InboxSuggestion {
	if a == nil || a.Proposal == nil {
		return nil
	}
	actual := *a.Proposal
	actual.Text = a.Text
	if a.MentionsSet || len(a.Mentions) > 0 {
		actual.Mentions = slices.Clone(a.Mentions)
	} else {
		actual.Mentions = activeReplyMentions(a.Text, a.Proposal.Mentions)
	}
	return &actual
}

func sameReplyOperation(a, b *InboxSuggestionAttempt) bool {
	return a != nil && b != nil && a.ClientID != "" && a.ClientID == b.ClientID && a.Target == b.Target && sameReplySuggestion(actualReply(a), actualReply(b))
}

func uncertainReplyOperation(it *InboxItem, g *InboxSuggestion, text string, ignoreDraftingProvenance bool) *InboxSuggestionAttempt {
	if g == nil {
		return nil
	}
	actual := *g
	actual.Text = text
	actual.Mentions = activeReplyMentions(text, g.Mentions)
	target := suggestionReplyTarget(it)
	for _, a := range it.SuggestionAttempts {
		if a != nil && (a.Status == suggestionUnknown || a.Status == suggestionSending) && a.Target == target && sameReplySuggestionMode(actualReply(a), &actual, ignoreDraftingProvenance) {
			return a
		}
	}
	return nil
}

// A newer card is not a new delivery intent for the exact same still-uncertain action. Project that operation's
// receipt onto its version, keeping its send identity and outcome time without inventing another active sender.
func inheritReplyUncertainty(it *InboxItem, g *InboxSuggestion, text string) *InboxSuggestionAttempt {
	proof := uncertainReplyOperation(it, g, text, false)
	if proof == nil {
		return nil
	}
	a := suggestionAttempt(it, g.Version)
	if a == nil {
		a = &InboxSuggestionAttempt{Version: g.Version, Text: text, Mentions: activeReplyMentions(text, g.Mentions), MentionsSet: true, ClientID: proof.ClientID, Proposal: copySuggestion(g), Conversation: g.Conversation, Target: proof.Target}
		keepSuggestionAttempt(it, a)
	}
	a.Status, a.Retryable, a.Code, a.At, a.active = suggestionUnknown, false, assistantwire.CodeAssistantInDoubt, proof.At, false
	a.RetryAfter = 0
	hideReplyDraft(it)
	return a
}

func hideReplyDraft(it *InboxItem) {
	if it.Agent != nil {
		it.Agent.Draft, it.Agent.Note, it.Agent.Changes = "", "", nil
		if it.Agent.State == AgentDrafted {
			it.Agent.State = AgentSkipped
		}
	}
}

// Settle only display receipts for this exact send. They may belong to a newer card, whose suggestion/handling
// effects remain independently fenced. Local rejection of an older version before sending is still known-none
// for a current equivalent card: it may retry the frozen action and ID rather than remain permanently uncertain.
func (b *inboxBook) settleReplyAliases(it *InboxItem, a *InboxSuggestionAttempt) {
	for _, related := range it.SuggestionAttempts {
		if related == nil || related == a || related.active || !sameReplyOperation(a, related) {
			continue
		}
		related.Status, related.Retryable, related.Code, related.RetryAfter, related.At = a.Status, a.Retryable, a.Code, a.RetryAfter, a.At
		if a.Status == suggestionFailed && a.Code == "suggestion_stale" && b.currentReplyAttempt(it, related) {
			related.Code, related.Retryable = assistantwire.CodeInboxSendFailed, true
		}
	}
}

func copySuggestion(g *InboxSuggestion) *InboxSuggestion {
	if g == nil {
		return nil
	}
	raw, _ := json.Marshal(g)
	var out InboxSuggestion
	_ = json.Unmarshal(raw, &out)
	return &out
}

func sameReplySuggestion(a, b *InboxSuggestion) bool {
	return sameReplySuggestionMode(a, b, false)
}

func sameReplySuggestionMode(a, b *InboxSuggestion, ignoreDraftingProvenance bool) bool {
	if a == nil || b == nil || a.Kind != SuggestReply || b.Kind != SuggestReply || a.Handback || b.Handback {
		return false
	}
	x, y := *a, *b
	// Notes only explain the card; due dates, directories and machines do not execute on a reply. Changing one
	// cannot turn the same actual uncertain send into a new version with another send identity.
	x.Note, y.Note, x.Due, y.Due, x.Cwd, y.Cwd, x.MachineID, y.MachineID = "", "", "", "", "", "", "", ""
	if ignoreDraftingProvenance {
		// Only an on-demand draft may project an earlier uncertain physical send onto its own card. Ordinary new
		// suggestions keep Conversation and Owner as distinct explicit intents and must never inherit that outcome.
		x.Conversation, y.Conversation, x.Owner, y.Owner = nil, nil, nil, nil
	}
	// Classification snapshots authorize a card; they do not identify the physical reply. A policy revision must not
	// turn the same recipient, text and send ID into a new operation after an uncertain outcome.
	x.ReplyCategory, y.ReplyCategory, x.ReplyMode, y.ReplyMode = "", "", "", ""
	x.ReplyClassifiedOf, y.ReplyClassifiedOf, x.ReplyPolicyRev, y.ReplyPolicyRev = "", "", "", ""
	x.ReplyReviewed, y.ReplyReviewed = false, false
	x.ReplyInstruction, y.ReplyInstruction = "", ""
	x.ReplyPublicationFailed, y.ReplyPublicationFailed = false, false
	x.OnDemand, y.OnDemand = false, false
	x.ReplyPublicationPending, y.ReplyPublicationPending = false, false
	if !slices.Equal(x.Changes, y.Changes) || !slices.Equal(x.Mentions, y.Mentions) {
		return false
	}
	x.Changes, y.Changes = nil, nil
	x.Mentions, y.Mentions = nil, nil
	x.ReplyPolicy, y.ReplyPolicy = nil, nil
	x.Version, y.Version, x.At, y.At = 0, 0, time.Time{}, time.Time{}
	return reflect.DeepEqual(x, y)
}

// Metadata rollback must not replace an unchanged active reservation with a decoded pointer: the in-flight
// sender still holds that pointer, and the process-local active bit is deliberately absent from stored JSON.
func restoreSuggestionAttempts(previous, current []*InboxSuggestionAttempt) []*InboxSuggestionAttempt {
	for i, old := range previous {
		if old == nil {
			continue
		}
		for _, live := range current {
			if live == nil {
				continue
			}
			x, y := *old, *live
			x.active, y.active = false, false
			if reflect.DeepEqual(x, y) {
				previous[i] = live
				break
			}
		}
	}
	return previous
}

func sameReplyAuthorization(a, b *InboxSuggestion) bool {
	return a != nil && b != nil && a.ReplyCategory == b.ReplyCategory && a.ReplyMode == b.ReplyMode &&
		a.ReplyClassifiedOf == b.ReplyClassifiedOf && a.ReplyPolicyRev == b.ReplyPolicyRev && a.ReplyReviewed == b.ReplyReviewed &&
		a.ReplyInstruction == b.ReplyInstruction && a.ReplyPublicationFailed == b.ReplyPublicationFailed &&
		a.OnDemand == b.OnDemand && a.ReplyPublicationPending == b.ReplyPublicationPending &&
		sameReplyPolicyBinding(a.ReplyPolicy, b.ReplyPolicy)
}

func sameReplyCard(a, b *InboxSuggestion) bool {
	return sameReplySuggestion(a, b) && sameReplyAuthorization(a, b)
}

func samePendingReply(it *InboxItem, proposed *InboxSuggestion) bool {
	current := currentSuggestion(it)
	if current != nil {
		if a := suggestionAttempt(it, current.Version); a != nil && a.Proposal != nil {
			actual := *a.Proposal
			actual.Text = a.Text
			if !sameReplySuggestion(&actual, proposed) {
				return false
			}
			// An uncertain physical operation keeps its card and send identity even across reclassification. A known-none
			// failed attempt, like a card never attempted, may be replaced by a freshly authorized snapshot.
			if a.Status == suggestionUnknown || a.Status == suggestionSending {
				return true
			}
			return sameReplyAuthorization(&actual, proposed)
		}
	}
	return sameReplyCard(current, proposed)
}

// A reply's recipient and source position must not change while a confirmation is saved or sent. This does not
// depend on title/triage/read state, which may change without changing what the reply answers.
func suggestionReplyTarget(it *InboxItem) string {
	target := struct {
		Source, Account, Kind, Last, Thread, ReplyTo, ThreadKind string
		CanReply                                                 bool
		Mail                                                     *inboxMailTarget
		Doc                                                      *DocReply
		DocURL, DocLink, DocType                                 string
	}{Source: it.Source, Account: it.Account, Kind: it.Kind, Last: it.Last, Thread: migratedThread(it), Mail: mailTarget(*it)}
	if it.Lark != nil {
		target.ReplyTo, target.ThreadKind = it.Lark.ReplyTo, it.Lark.ThreadKind
	}
	if it.Connector != nil {
		target.Thread, target.ReplyTo, target.CanReply = it.Connector.Thread, it.Connector.ReplyTo, it.Connector.CanReply
	}
	if it.Doc != nil {
		target.DocURL, target.DocLink, target.DocType = it.Doc.URL, it.Doc.Link, it.Doc.Type
		if it.Doc.Reply != nil {
			reply := *it.Doc.Reply
			reply.Label = ""
			target.Doc = &reply
		}
	}
	if it.Moment != nil {
		target.ReplyTo = it.Moment.ReplyTo
	}
	raw, _ := json.Marshal(target)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (b *inboxBook) currentReplyAttempt(it *InboxItem, a *InboxSuggestionAttempt) bool {
	if a.Proposal != nil && a.Proposal.Conversation == nil && a.Proposal.Owner != nil &&
		(it.Agent == nil || it.Agent.MachineID != a.Proposal.Owner.MachineID || it.Agent.SessionID != a.Proposal.Owner.SessionID) {
		return false
	}
	return b.items[it.ID] == it && it.open() && suggestionAttempt(it, a.Version) == a &&
		it.Suggestion != nil && it.Suggestion.Version == a.Version && sameReplySuggestion(it.Suggestion, a.Proposal) &&
		suggestionReplyTarget(it) == a.Target
}

func unknownSuggestionReply() error {
	return &rpc.Error{Code: assistantwire.CodeAssistantInDoubt, Message: i18n.T("", "rpc-errors.in_doubt")}
}

func unrecordedSuggestionReply() error {
	return &rpc.Error{Code: assistantwire.CodeInboxSendUnrecorded, Message: i18n.T("", "inbox-errors.send_unrecorded")}
}

// persistReplyReservation must actually include this item and reservation in a successful store write. A nil
// failure map alone is not proof: saveInbox may skip an unloaded, forgotten or replaced item, or have no store.
func (s *Service) persistReplyReservation(ctx context.Context, it *InboxItem, a *InboxSuggestionAttempt) bool {
	if s.inboxDB() == nil || !s.inbox.loaded.Load() {
		return false
	}
	committed := false
	s.saveInboxPrepared(ctx, []*InboxItem{it}, nil, func(failed map[string]bool) {
		committed = !failed[it.ID] && s.inbox.items[it.ID] == it && suggestionAttempt(it, a.Version) == a && a.active && a.Status == suggestionSending
	})
	return committed
}

// suggestionReplyOnly marks a confirmed reply's shared send path: the attempt-fenced completion below, rather
// than the direct /reply helper, commits its local effects. This marker is internal, never supplied by a client.
type suggestionReplyOnly struct{}

// suggestionReplyPolicyBeforeSend carries the exact authorization under which this durable attempt was reserved.
// It is internal context only: the per-item send mutex may wait while the live manager configuration changes.
type suggestionReplyPolicyBeforeSend struct{}

func cloneReplyPolicyForSend(cfg *assistantwire.InboxReplyPolicyConfig) *assistantwire.InboxReplyPolicyConfig {
	if cfg == nil {
		return nil
	}
	out := *cfg
	out.Sources = slices.Clone(cfg.Sources)
	if cfg.Categories != nil {
		out.Categories = make(map[string]string, len(cfg.Categories))
		for category, route := range cfg.Categories {
			out.Categories[category] = route
		}
	}
	if cfg.Routes != nil {
		out.Routes = make(map[string]string, len(cfg.Routes))
		for category, route := range cfg.Routes {
			out.Routes[category] = route
		}
	}
	return &out
}

func suggestionSendOnly(ctx context.Context) bool {
	_, only := ctx.Value(suggestionReplyOnly{}).(*InboxSuggestionAttempt)
	return only
}

// confirmSuggestionReplyLocked starts with inbox.mu held and returns without it. Known-none retryable failures
// retain one current action; uncertain sends remain visible but cannot be reissued with a new caller UUID.
func (s *Service) confirmSuggestionReplyLocked(ctx context.Context, it *InboxItem, g *InboxSuggestion, in suggestionReq,
	policyInput any, policy replyPolicySpec, preVeto *InboxReplyPolicySnapshot, preVetoErr error) (InboxItem, error) {
	b := &s.inbox
	var replyPolicy *assistantwire.InboxReplyPolicyConfig
	switch value := policyInput.(type) {
	case *assistantwire.InboxReplyPolicyConfig:
		replyPolicy = value
	case assistantwire.InboxReplyPolicyConfig:
		replyPolicy = &value
	}
	if g.ReplyPublicationFailed || g.ReplyPublicationPending {
		out := it.listed()
		b.mu.Unlock()
		return out, errInboxNotPersisted
	}
	// Let the dedicated target check below report a changed recipient after unresolved durable outcomes take precedence.
	if g.SourceOf == "" || g.SourceOf == suggestionReplyTarget(it) {
		if err := replyPolicySendGate(it, g, replyPolicy); err != nil {
			out := it.listed()
			b.mu.Unlock()
			return out, err
		}
	}
	a := suggestionAttempt(it, g.Version)
	if in.Changes != nil {
		selected := slices.Clone(*in.Changes)
		if a != nil {
			var frozen []DocChange
			if a.Proposal != nil {
				frozen = a.Proposal.Changes
			}
			if !slices.Equal(selected, frozen) {
				// The first confirmation's explicit selection is part of the physical action. A retry cannot add back a
				// deselected move (or drop one that may already have executed) under the same durable attempt.
				out := it.listed()
				b.mu.Unlock()
				return out, errSuggestionStale
			}
		} else {
			// Freeze what the old client actually selected into both the current card and its first attempt. In particular,
			// an omitted /reply changes array is an explicit empty selection, not permission to execute the stored moves.
			g.Changes = selected
		}
	}
	if a != nil && (a.Status == suggestionSending || a.Status == suggestionUnknown) {
		out := it.listed()
		b.mu.Unlock()
		return out, unknownSuggestionReply()
	}
	if g.SourceOf != "" && g.SourceOf != suggestionReplyTarget(it) {
		var targetErr error = errSuggestionStale
		if it.Mail != nil || g.MailTarget != nil {
			targetErr = errMailReplyTargetChanged
		}
		out := it.listed()
		b.mu.Unlock()
		if a != nil {
			return s.finishSuggestionReply(ctx, it, a, targetErr)
		}
		return out, targetErr
	}
	if it.Suggestion == nil {
		it.Suggestion = copySuggestion(g)
		it.SuggestSeq = max(it.SuggestSeq, g.Version)
		g = it.Suggestion
	}
	if a == nil {
		// A pre-upgrade suggestion has no private action binding yet. Freeze the trusted current author/source
		// at its first confirmation so it cannot later evade historical guards merely by gaining those fields.
		if g.Owner == nil {
			if g.Conversation != nil {
				owner := *g.Conversation
				g.Owner = &owner
			} else if it.Agent != nil {
				g.Owner = &SessionRef{MachineID: it.Agent.MachineID, SessionID: it.Agent.SessionID}
			}
		}
		if g.SourceOf == "" {
			g.SourceOf = suggestionReplyTarget(it)
		}
	}
	text := g.Text
	if a != nil {
		text = a.Text
	}
	if in.Text != nil {
		proposed := strings.TrimSpace(*in.Text)
		if proposed != "" || in.Changes != nil {
			text = proposed
			if a != nil && text != a.Text {
				out := it.listed()
				b.mu.Unlock()
				return out, errSuggestionStale
			}
		}
	}
	if inherited := inheritReplyUncertainty(it, g, text); inherited != nil {
		b.touched(it.ID)
		b.mu.Unlock()
		// The older proof was already persisted before its send. If this projection cannot be saved, loading
		// and checking that proof still refuses a fresh-ID retry; no new outward action occurs here.
		s.saveInbox(context.WithoutCancel(ctx), []*InboxItem{it})
		b.mu.Lock()
		out := InboxItem{}
		if live := b.items[it.ID]; live != nil {
			out = live.listed()
		}
		b.mu.Unlock()
		return out, unknownSuggestionReply()
	}
	if a != nil {
		if !b.currentReplyAttempt(it, a) {
			out := it.listed()
			b.mu.Unlock()
			return out, errSuggestionStale
		}
		if a.Status != suggestionFailed || !a.Retryable {
			out := it.listed()
			status, code, retryAfter := a.Status, a.Code, a.RetryAfter
			b.mu.Unlock()
			if status == suggestionSent {
				return out, errSuggestionStale
			}
			if status == suggestionFailed {
				switch code {
				case "mail_reply_target_changed":
					return out, errMailReplyTargetChanged
				case "suggestion_stale":
					return out, errSuggestionStale
				case assistantwire.CodeInboxReplyPolicyBlocked:
					return out, errReplyPolicyBlocked
				case assistantwire.CodeInboxCannotReply:
					return out, errCannotReply
				}
				var data json.RawMessage
				if retryAfter > 0 {
					data, _ = json.Marshal(map[string]int{"retry_after": retryAfter})
				}
				return out, &rpc.Error{Code: code, Message: i18n.T("", "inbox-errors.send_failed"), Data: data}
			}
			return out, unknownSuggestionReply()
		}
	}
	var policySnap *InboxReplyPolicySnapshot
	if policy.appliesToItem(it) {
		// Gate 3a: verify the suggestion's reply-policy snapshot is present, fresh for the live content version
		// and policy revision, and still resolves to owner_draft.
		if err := verifyReplyPolicySuggestionLocked(it, g, a, policy); err != nil {
			var snap *InboxReplyPolicySnapshot
			if g.ReplyPolicy != nil {
				snap = cloneReplyPolicySnapshot(g.ReplyPolicy)
			}
			return s.rejectReplyPolicySendLocked(ctx, it, g, a, in.ClientID, text, snap, replyPolicyContentVersion(it), policy.revision)
		}
		// Gate 3b: final-text veto. Classify the actual text to be sent (original or edited) outside the lock.
		itemData := snapshotItemForReplyPolicyLocked(it)
		expectedVersion := g.Version
		expectedTarget := suggestionReplyTarget(it)
		b.mu.Unlock()
		finalSnap := preVeto
		vetoErr := preVetoErr
		if finalSnap == nil && vetoErr == nil {
			finalSnap, vetoErr = s.gateReplyPolicySend(ctx, policy, itemData, text)
		}
		freshCfg, freshErr := s.inboxConfigFresh(ctx)
		livePolicy := compileReplyPolicy(freshCfg.ReplyPolicy)
		b.mu.Lock()
		if b.items[itemData.id] != it {
			b.mu.Unlock()
			return InboxItem{}, errNoInboxItem
		}
		if !it.open() {
			b.mu.Unlock()
			return InboxItem{}, errInboxItemDone
		}
		if inboxVisibilityWithMembersExcludedLocked(b, it, freshCfg.Visibility) {
			out := it.listed()
			b.mu.Unlock()
			return out, errCannotReply
		}
		g = currentSuggestion(it)
		if g == nil || g.Version != expectedVersion || g.Kind != SuggestReply || g.Handback {
			out := it.listed()
			b.mu.Unlock()
			return out, errSuggestionStale
		}
		a = suggestionAttempt(it, expectedVersion)
		if a != nil && (a.Status == suggestionSending || a.Status == suggestionUnknown) {
			out := it.listed()
			b.mu.Unlock()
			return out, unknownSuggestionReply()
		}
		if a != nil && a.Status == suggestionSent {
			out := it.listed()
			b.mu.Unlock()
			return out, errSuggestionStale
		}
		if a != nil && text != a.Text {
			out := it.listed()
			b.mu.Unlock()
			return out, errSuggestionStale
		}
		if a != nil && a.Status == suggestionFailed && !a.Retryable {
			out := it.listed()
			code, retryAfter := a.Code, a.RetryAfter
			b.mu.Unlock()
			switch code {
			case "mail_reply_target_changed":
				return out, errMailReplyTargetChanged
			case "suggestion_stale":
				return out, errSuggestionStale
			case assistantwire.CodeInboxReplyPolicyBlocked:
				return out, errReplyPolicyBlocked
			case assistantwire.CodeInboxCannotReply:
				return out, errCannotReply
			}
			var data json.RawMessage
			if retryAfter > 0 {
				data, _ = json.Marshal(map[string]int{"retry_after": retryAfter})
			}
			return out, &rpc.Error{Code: code, Message: i18n.T("", "inbox-errors.send_failed"), Data: data}
		}
		if suggestionReplyTarget(it) != expectedTarget {
			var targetErr error = errSuggestionStale
			if it.Mail != nil || g.MailTarget != nil {
				targetErr = errMailReplyTargetChanged
			}
			out := it.listed()
			b.mu.Unlock()
			if a != nil {
				return s.finishSuggestionReply(ctx, it, a, targetErr)
			}
			return out, targetErr
		}
		rev := policy.revision
		if livePolicy.enabled {
			rev = livePolicy.revision
		}
		if freshErr != nil {
			return s.failReplyPolicyConfigReadLocked(ctx, it, g, a, in.ClientID, text, finalSnap, replyPolicyContentVersion(it), rev)
		}
		if vetoErr != nil || !livePolicy.enabled || livePolicy.revision != policy.revision ||
			replyPolicyContentVersion(it) != itemData.contentVersion ||
			verifyReplyPolicySuggestionLocked(it, g, a, livePolicy) != nil ||
			livePolicy.allowsOwnerDraft(finalSnap, itemData.contentVersion) != nil {
			return s.rejectReplyPolicySendLocked(ctx, it, g, a, in.ClientID, text, finalSnap, replyPolicyContentVersion(it), rev)
		}
		policySnap = finalSnap
	}
	if a == nil {
		id := strings.ToLower(strings.TrimSpace(in.ClientID))
		if !validClientID(id) {
			id = namedClientID(fmt.Sprintf("suggestion\n%s\n%d", it.ID, g.Version))
		}
		a = &InboxSuggestionAttempt{Version: g.Version, Text: text, Mentions: activeReplyMentions(text, g.Mentions), MentionsSet: true, ClientID: id, Proposal: copySuggestion(g),
			Conversation: g.Conversation, Target: suggestionReplyTarget(it)}
		keepSuggestionAttempt(it, a)
	}
	if policySnap != nil {
		a.Category = policySnap.Category
		a.Route = policySnap.Route
		a.ContentVersion = policySnap.ContentVersion
		a.PolicyRevision = policySnap.PolicyRevision
		a.Reviewed = policySnap.Reviewed
	}
	mail := it.Mail != nil
	a.Status, a.Retryable, a.Code, a.RetryAfter, a.At, a.active = suggestionSending, false, "", 0, time.Now().UTC(), true
	hideReplyDraft(it)
	b.touched(it.ID)
	b.mu.Unlock()
	if !s.persistReplyReservation(ctx, it, a) {
		b.mu.Lock()
		a.Status, a.Retryable, a.Code, a.At, a.active = suggestionFailed, true, assistantwire.CodeInboxSendUnrecorded, time.Now().UTC(), false
		b.settleReplyAliases(it, a)
		if live := b.items[it.ID]; live != it {
			out := InboxItem{}
			if live != nil {
				out = live.listed()
			}
			b.mu.Unlock()
			if live == nil {
				return out, errNoInboxItem
			}
			return out, errSuggestionStale
		}
		out := it.listed()
		b.mu.Unlock()
		s.saveInbox(context.WithoutCancel(ctx), []*InboxItem{it})
		return out, unrecordedSuggestionReply()
	}
	// The save may have waited behind another write. Recheck the reservation, both reply-policy schemas and visibility
	// against one fresh configuration before any outward action; neither display-cache policy nor a pre-save snapshot is sufficient.
	freshCfg, configErr := s.inboxConfigFresh(ctx)
	if configErr != nil {
		return s.finishSuggestionReply(ctx, it, a, &rpc.Error{Code: assistantwire.CodeInboxSendFailed, Message: "inbox configuration unavailable"})
	}
	livePolicy := compileReplyPolicy(freshCfg.ReplyPolicy)
	b.mu.Lock()
	current := b.currentReplyAttempt(it, a) && replyPolicySendGate(it, g, freshCfg.ReplyPolicy) == nil &&
		!inboxVisibilityWithMembersExcludedLocked(b, it, freshCfg.Visibility)
	policyErr := verifyReplyPolicySendAttemptLocked(it, a, livePolicy)
	b.mu.Unlock()
	if !current {
		return s.finishSuggestionReply(ctx, it, a, errSuggestionStale)
	}
	if policyErr != nil {
		return s.finishSuggestionReply(ctx, it, a, errReplyPolicyBlocked)
	}
	sendCtx := context.WithValue(ctx, suggestionReplyOnly{}, a)
	sendCtx = context.WithValue(sendCtx, suggestionReplyPolicyBeforeSend{}, cloneReplyPolicyForSend(freshCfg.ReplyPolicy))
	var err error
	if target := a.Proposal.MailTarget; target != nil {
		_, err = s.replyInboxCheckedDo(sendCtx, it.ID, a.Text, a.ClientID, false, false, &target.UID, &target.Version, a.Proposal.Changes...)
	} else if mail {
		err = errMailReplyTargetChanged
	} else {
		_, err = s.replyInboxDoMentions(sendCtx, it.ID, a.Text, a.ClientID, false, false, a.Mentions, a.Proposal.Changes...)
	}
	return s.finishSuggestionReply(ctx, it, a, err)
}

// rejectReplyPolicySendLocked records a non-retryable, known-none policy veto on the suggestion attempt with
// zero sender calls. Called with b.mu held; returns without it.
func (s *Service) rejectReplyPolicySendLocked(ctx context.Context, it *InboxItem, g *InboxSuggestion, a *InboxSuggestionAttempt,
	clientID, text string, snap *InboxReplyPolicySnapshot, contentVersion, policyRevision string) (InboxItem, error) {
	out := s.recordReplyPolicySendFailureLocked(ctx, it, g, a, clientID, text, snap, contentVersion, policyRevision,
		assistantwire.CodeInboxReplyPolicyBlocked, false, "assistant-inbox.reply_policy_blocked")
	return out, errReplyPolicyBlocked
}

// failReplyPolicyConfigReadLocked records a retryable known-none failure when authorization could not be refreshed.
// No reservation or sender call exists yet, so this is distinct from a definitive policy veto.
func (s *Service) failReplyPolicyConfigReadLocked(ctx context.Context, it *InboxItem, g *InboxSuggestion, a *InboxSuggestionAttempt,
	clientID, text string, snap *InboxReplyPolicySnapshot, contentVersion, policyRevision string) (InboxItem, error) {
	out := s.recordReplyPolicySendFailureLocked(ctx, it, g, a, clientID, text, snap, contentVersion, policyRevision,
		assistantwire.CodeInboxSendFailed, true, "assistant-inbox_chain.failed_send")
	return out, &rpc.Error{Code: assistantwire.CodeInboxSendFailed, Message: "reply policy configuration unavailable"}
}

// recordReplyPolicySendFailureLocked persists the common known-none attempt and proposal state for policy and config failures.
func (s *Service) recordReplyPolicySendFailureLocked(ctx context.Context, it *InboxItem, g *InboxSuggestion, a *InboxSuggestionAttempt,
	clientID, text string, snap *InboxReplyPolicySnapshot, contentVersion, policyRevision, code string, retryable bool, reasonKey string) InboxItem {
	b := &s.inbox
	now := time.Now().UTC()
	if a == nil {
		id := strings.ToLower(strings.TrimSpace(clientID))
		if !validClientID(id) {
			id = namedClientID(fmt.Sprintf("suggestion\n%s\n%d", it.ID, g.Version))
		}
		a = &InboxSuggestionAttempt{
			Version:      g.Version,
			Text:         text,
			Mentions:     activeReplyMentions(text, g.Mentions),
			MentionsSet:  true,
			ClientID:     id,
			Proposal:     copySuggestion(g),
			Conversation: g.Conversation,
			Target:       suggestionReplyTarget(it),
		}
		keepSuggestionAttempt(it, a)
	}
	if snap != nil {
		a.Category = snap.Category
		a.Route = snap.Route
	} else {
		a.Category = ""
		a.Route = ""
	}
	a.ContentVersion = contentVersion
	a.PolicyRevision = policyRevision
	a.Reviewed = snap != nil && snap.Reviewed
	a.Status, a.Retryable, a.Code, a.RetryAfter, a.At, a.active = suggestionFailed, retryable, code, 0, now, false
	hideReplyDraft(it)
	b.settleReplyAliases(it, a)
	reason := i18n.T(b.userLang(), reasonKey)
	if a.Conversation == nil {
		if it.Agent != nil {
			it.Agent.Wake = false
		}
		b.dropInboxWakes(it)
		b.setHandling(it, HandlingFailed, reason, now)
	}
	b.chainAdd(it, InboxChainEntry{Kind: ChainFailed, At: now, Text: i18n.T(b.userLang(), "assistant-inbox_chain.failed"), Detail: reason})
	b.touched(it.ID)
	out := it.listed()
	b.mu.Unlock()
	s.saveInbox(context.WithoutCancel(ctx), []*InboxItem{it})
	return out
}

func (s *Service) finishSuggestionReply(ctx context.Context, it *InboxItem, a *InboxSuggestionAttempt, err error) (InboxItem, error) {
	b := &s.inbox
	now := time.Now().UTC()
	loc := s.location(ctx)
	b.mu.Lock()
	current := b.currentReplyAttempt(it, a)
	if !current {
		// A newer version can be an exact receipt of this same user-confirmed operation, not foreign work.
		// Its own proposal snapshot, live owner and source must still match before applying success policy.
		for _, related := range it.SuggestionAttempts {
			if related != a && sameReplyOperation(a, related) && b.currentReplyAttempt(it, related) {
				current = true
				break
			}
		}
	}
	a.active, a.At, a.Retryable, a.Code, a.RetryAfter = false, now, false, "", 0
	switch {
	case err == nil:
		a.Status = suggestionSent
	case errors.Is(err, errSuggestionStale):
		a.Status, a.Code = suggestionFailed, "suggestion_stale"
	case errors.Is(err, errMailReplyTargetChanged):
		a.Status, a.Code = suggestionFailed, "mail_reply_target_changed"
	case errors.Is(err, errReplyPolicyBlocked):
		a.Status, a.Code = suggestionFailed, assistantwire.CodeInboxReplyPolicyBlocked
	case errors.Is(err, errCannotReply):
		a.Status = suggestionFailed
		a.Code = assistantwire.CodeInboxCannotReply
	case replyInDoubt(err):
		a.Status, a.Code, err = suggestionUnknown, assistantwire.CodeAssistantInDoubt, unknownSuggestionReply()
	default:
		var re *rpc.Error
		if errors.As(err, &re) {
			if f, ok := inboxSendFailures[re.Code]; ok {
				a.Status, a.Code, a.Retryable = suggestionFailed, re.Code, f.retryable
				var data struct {
					RetryAfter int `json:"retry_after"`
				}
				if json.Unmarshal(re.Data, &data) == nil && data.RetryAfter > 0 {
					a.RetryAfter = data.RetryAfter
				}
				break
			}
		}
		a.Status, a.Code, err = suggestionUnknown, assistantwire.CodeAssistantInDoubt, unknownSuggestionReply()
	}
	// These records describe the same physical send, not fresh attempts. A known canonical outcome may settle
	// their display receipts, while the normal current-version fence below still protects newer local work.
	b.settleReplyAliases(it, a)
	var note *inboxNote
	tellProcessing := false
	if current {
		if err != nil {
			if a.Conversation == nil {
				if it.Agent != nil {
					it.Agent.Wake = false
				}
				b.dropInboxWakes(it)
				reason := i18n.T(b.userLang(), "assistant-inbox_chain.failed_send")
				b.setHandling(it, HandlingFailed, reason, now)
				b.chainAdd(it, InboxChainEntry{Kind: ChainFailed, At: now, Text: i18n.T(b.userLang(), "assistant-inbox_chain.failed"), Detail: reason})
			} else {
				b.chainAdd(it, InboxChainEntry{Kind: ChainFailed, At: now, Text: i18n.T(b.userLang(), "assistant-inbox_chain.failed"), Detail: i18n.T(b.userLang(), "assistant-inbox_chain.failed_send")})
			}
		} else {
			resetInboxFollowChecks(it)
			it.Suggestion = nil
			it.ReplyInstruction = nil
			b.chainAdd(it, confirmedEntry(b.userLang(), a.Text, a.Text != a.Proposal.Text, now))
			b.noteSent(it, now)
			if a.Conversation == nil {
				if it.Agent == nil {
					it.Agent = &InboxAgent{State: AgentSkipped}
				}
				it.Agent.SentOf = it.Last
				b.waitForOtherSide(it, defaultInboxFollow(now), ViaUser, now, loc, false)
				b.tell(it, inboxNote{Name: prompts.NoteSent, Text: a.Text, Edited: a.Text != a.Proposal.Text}, false, now)
				tellProcessing = true
			} else {
				it.done(now, "replied")
				tellProcessing = b.closeHandling(it, "replied", now)
				note = &inboxNote{Name: prompts.NoteSent, Text: a.Text, Edited: a.Text != a.Proposal.Text}
			}
		}
	}
	if b.items[it.ID] == it {
		b.touched(it.ID)
	}
	out := InboxItem{}
	if live := b.items[it.ID]; live != nil {
		out = live.listed()
	}
	committedMark := b.mark[it.ID]
	b.mu.Unlock()
	// A completed send is never rolled back if this write fails. Its persisted reservation still carries the
	// original identity and becomes unknown after reload, rather than permitting a second fresh-ID send.
	s.saveInbox(context.WithoutCancel(ctx), []*InboxItem{it})
	b.mu.Lock()
	still := b.items[it.ID] == it && b.mark[it.ID] == committedMark
	if live := b.items[it.ID]; live != nil {
		out = live.listed()
	}
	b.mu.Unlock()
	if current && still && err == nil {
		s.markCardsSent(ctx, it.ID, now, a.Text, true)
		if note != nil {
			s.tellHandoff(ctx, it.ID, clip(oneLine(it.Title), 80), *a.Conversation, *note)
		}
		if tellProcessing {
			s.tellSoon(ctx, it)
		}
	}
	return out, err
}
