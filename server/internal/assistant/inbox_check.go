package assistant

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// inboxWakeCandidate is one item's next programmatic admission. A deferred source wake and a due check on the same
// conversation share one position and one slot; the earlier eligibility time represents the pair.
type inboxWakeCandidate struct {
	it     *InboxItem
	at     time.Time
	source bool
	check  bool
}

// inboxWakeCandidates builds the worker's single eligibility order. The caller holds inbox.mu.
func (b *inboxBook) inboxWakeCandidates(cfg InboxAgentConfig, due []*InboxItem) []inboxWakeCandidate {
	byItem := map[*InboxItem]*inboxWakeCandidate{}
	if cfg.Enabled {
		for _, it := range b.items {
			a := it.Agent
			if !it.open() || a == nil || a.SessionID == "" || a.DeferredAt == nil {
				continue
			}
			held := false
			for _, n := range a.Pending {
				held = held || n.Deferred
			}
			if held {
				byItem[it] = &inboxWakeCandidate{it: it, at: *a.DeferredAt, source: true}
			}
		}
		for _, it := range due {
			a, f := it.Agent, it.Follow
			if a == nil || a.SessionID == "" || f.Checks >= inboxFollowUpAskChecks {
				continue
			}
			c := byItem[it]
			if c == nil {
				c = &inboxWakeCandidate{it: it, at: *f.CheckAt}
				byItem[it] = c
			} else if f.CheckAt.Before(c.at) {
				c.at = *f.CheckAt
			}
			c.check = true
		}
	}
	out := make([]inboxWakeCandidate, 0, len(byItem))
	for _, c := range byItem {
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b inboxWakeCandidate) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return cmp.Compare(a.it.ID, b.it.ID)
	})
	return out
}

func (b *inboxBook) inboxRunning() int {
	running := 0
	for _, it := range b.items {
		if inboxConversationRunning(it) {
			running++
		}
	}
	return running
}

// admitFreshInboxWakes preserves the old immediate source path only while doing so cannot overtake an older queue
// entry. Anything else is left for checkInboxFollowUps, whose writes also cover items outside the current source round.
// fresh contains only items whose current source write already covers the admission mutation. Under b.mu.
func (b *inboxBook) admitFreshInboxWakes(cfg InboxAgentConfig, now time.Time, fresh map[*InboxItem]bool) {
	if !cfg.Enabled || len(fresh) == 0 {
		return
	}
	var due []*InboxItem
	for _, it := range b.items {
		if inboxFollowCheckDue(it, now) && it.Agent != nil && it.Agent.Of == it.Last {
			due = append(due, it)
		}
	}
	running := b.inboxRunning()
	for _, c := range b.inboxWakeCandidates(cfg, due) {
		if c.check || !c.source || !fresh[c.it] || running >= agentConcurrency(cfg) {
			return
		}
		b.admitInboxSourceWake(c.it, now)
		if inboxConversationRunning(c.it) {
			running++
		}
	}
}

// checkInboxFollowUps is the unified worker admission pass. Deferred source wakes use their first-deferred time and
// due checks use Follow.CheckAt; item id breaks ties. Already active conversations count before either kind, and every
// new admission consumes capacity immediately. Non-waking terminal due-check outcomes retain their old behavior.
func (s *Service) checkInboxFollowUps(ctx context.Context, cfg InboxAgentConfig, now time.Time) {
	b := &s.inbox
	now = now.UTC()
	b.mu.Lock()
	var due []*InboxItem
	for _, it := range b.items {
		if inboxFollowCheckDue(it, now) && it.Agent != nil && it.Agent.Of == it.Last {
			// Content the conversation has not seen goes through escalate first: it is news, not an empty due check. This
			// also leaves it waiting while the twin is off, so enabling the twin again still delivers that content.
			due = append(due, it)
		}
	}
	slices.SortFunc(due, func(a, b *InboxItem) int {
		if c := a.Follow.CheckAt.Compare(*b.Follow.CheckAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	running := b.inboxRunning()
	var changed []*inboxWrite
	checked := map[*InboxItem]bool{}
	sourceAdmitted := map[*InboxItem]bool{}
	for _, c := range b.inboxWakeCandidates(cfg, due) {
		if running >= agentConcurrency(cfg) {
			break
		}
		w := beginWrite(c.it)
		if c.source {
			b.admitInboxSourceWake(c.it, now)
			sourceAdmitted[c.it] = true
		}
		if c.check {
			f := c.it.Follow
			f.Checks++
			f.Due, f.CheckAt = false, nil
			b.addInboxCheckChain(c.it, "check_started", now)
			b.tell(c.it, inboxNote{Name: prompts.NoteCheck, Who: f.Who, Next: f.Next, Checks: f.Checks}, true, now)
			b.setHandling(c.it, HandlingLooking, "", now)
			checked[c.it] = true
		}
		b.settle(w)
		changed = append(changed, w)
		if inboxConversationRunning(c.it) {
			running++
		}
	}
	for _, it := range due {
		if checked[it] || sourceAdmitted[it] {
			continue // an admitted source wake owns this pass's slot, including after the third empty check
		}
		f := it.Follow
		hasConversation := it.Agent != nil && it.Agent.SessionID != ""
		if cfg.Enabled && hasConversation && f.Checks < inboxFollowUpAskChecks {
			continue // admitted above, or still due behind capacity and the older queue entries
		}
		w := beginWrite(it)
		switch {
		case !cfg.Enabled || !hasConversation:
			f.Due, f.CheckAt = true, nil
			it.Tier = TierAction
			b.setHandling(it, HandlingDone, "", now)
			b.addInboxCheckChain(it, "check_due", now)
		default:
			f.Due, f.CheckAt = false, nil
			it.Tier = TierAction
			b.setHandling(it, HandlingDone, "", now)
			b.chainAdd(it, InboxChainEntry{Kind: ChainCheck, At: now, Via: ViaAssistant,
				Text: i18n.T(b.userLang(), "assistant-inbox_chain.check_follow_up_ask")})
		}
		b.settle(w)
		changed = append(changed, w)
	}
	b.mu.Unlock()
	s.saveWrites(ctx, changed, "its due follow-up checks")
}

// inboxFollowCheckDue is one open matter waiting for its scheduled look, with no suggestion already awaiting the user.
// The caller holds the inbox lock.
func inboxFollowCheckDue(it *InboxItem, now time.Time) bool {
	return it.open() && it.Handling != nil && it.Handling.State == HandlingWaiting && it.Follow != nil &&
		it.Follow.CheckAt != nil && !it.Follow.CheckAt.After(now) && currentSuggestion(it) == nil
}

// addInboxCheckChain records a server-written due-check step, with the next step when there is one. Under b.mu.
func (b *inboxBook) addInboxCheckChain(it *InboxItem, key string, now time.Time) {
	var args i18n.Args
	if next := it.Follow.Next; next != "" {
		key += "_next"
		args = i18n.Args{"next": next}
	}
	b.chainAdd(it, InboxChainEntry{Kind: ChainCheck, At: now, Via: ViaAssistant, Detail: it.Follow.Next,
		Text: i18n.T(b.userLang(), "assistant-inbox_chain."+key, args)})
}

// resumeInboxWaiting ends a turn that came from waiting without a suggestion: a recap-only answer, a timeout or an
// idle conversation all wait again rather than fail. The number of the check already made stays; waiting always has a
// next time, so a check that cleared it gets the default 24 hours. Under b.mu.
func (b *inboxBook) resumeInboxWaiting(it *InboxItem, now time.Time) {
	ensureInboxFollowCheckAt(it, now)
	if it.Agent != nil {
		it.Agent.Wake = false
	}
	b.setHandling(it, HandlingWaiting, "", now)
}
