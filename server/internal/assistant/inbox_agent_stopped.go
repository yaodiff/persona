package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// An Agent linked by an item's agent chain entry reports each settled stop back to that item. The chain entry's At
// keeps the display order; its TurnAt is the node's activity time, the only clock the once-per-stop check compares.
// That makes the check survive a manager restart without mixing the hub's and the node's clocks.

// inboxAgentTurnSettled is called after a turn stayed still. Look up the session again: more input or background work
// may have arrived during the settle window. Failed turns count as stopped, as they do in spawnedTurnSettled.
func (s *Service) inboxAgentTurnSettled(ctx context.Context, machine, sid string) {
	if s.inboxReady(ctx) != nil || !s.inboxHasAgentLink(machine, sid) {
		return
	}
	sess, ok := s.decoratedSession(machine, sid)
	if !ok {
		sess, ok = s.session(ctx, machine, sid)
	}
	if !ok {
		return
	}
	sess.MachineID = machine
	s.recordInboxAgentStops(ctx, sess)
}

// inboxHasAgentLink: an open item links this session as an Agent. The item lookup comes before the node call so every
// unrelated turn does not cost one. Callers have checked inboxReady.
func (s *Service) inboxHasAgentLink(machine, sid string) bool {
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, it := range b.items {
		if it.open() && inboxItemLinksAgent(it, machine, sid) {
			return true
		}
	}
	return false
}

// inboxAgentStopState is the same settled check as spawnedTurnSettled, plus the node's own queued input and any wait
// for the user (blocked, needs_you or a pending interaction). A failure is a stop even with queued or background work. detail is the Agent's last words, or its failure, ready for the chain.
func (s *Service) inboxAgentStopState(sess proto.Session) (settled, failed bool, detail string) {
	if sess.Status == proto.StatusWorking || sess.Status == proto.StatusBlocked || sess.Starting ||
		sess.State == proto.StateNeedsYou || sess.PendingInteractions > 0 {
		return false, false, ""
	}
	turnError, _ := latestTurnError(sess)
	failed = strings.TrimSpace(turnError) != ""
	if !failed && (s.queuedFor(sess.SessionID) > 0 || len(sess.Queued) > 0 || backgroundRunning(sess)) {
		return false, false, ""
	}
	detail = sess.LastText
	if failed {
		detail = turnError
	}
	return true, failed, clip(oneLine(detail), 300)
}

// recordInboxAgentStops records sess's current stop once on every open item that links it. Each item has its own
// marker and notification: two matters may deliberately share an Agent. A save that is undone does not wake anyone.
// False means it stopped being settled, or a write was undone; sweep must wait for the next pass rather than turn an
// unrecorded stop into a timeout. Task supervision notices are independent and never replace the Inbox wake.
func (s *Service) recordInboxAgentStops(ctx context.Context, sess proto.Session) bool {
	settled, failed, detail := s.inboxAgentStopState(sess)
	if !settled {
		return false
	}
	agentCfg := s.inboxConfig(ctx).Agent
	b := &s.inbox
	now := time.Now().UTC()
	var writes []*inboxWrite
	fresh := map[*InboxItem]bool{}
	deliver := map[*inboxWrite]bool{}
	b.mu.Lock()
	for _, it := range b.items {
		linked, duplicate := inboxAgentStopDuplicate(it, sess)
		if !it.open() || !linked || duplicate {
			continue
		}
		w := beginWrite(it)
		reuseSlot := inboxConversationRunning(it)
		latest := inboxLatestAgentLink(it)
		current := latest != nil && latest.MachineID == sess.MachineID && latest.SessionID == sess.SessionID
		turnAt := sess.UpdatedAt.UTC()
		key := "assistant-inbox_chain.agent_stopped"
		if failed {
			key += "_failed"
		}
		b.chainAdd(it, InboxChainEntry{Kind: ChainAgentStopped, At: inboxAgentStopDisplayTime(it, sess, now), TurnAt: &turnAt,
			Text: i18n.T(b.userLang(), key), Detail: detail, Via: ViaAgent,
			Link: &InboxChainLink{Type: LinkSession, MachineID: sess.MachineID, SessionID: sess.SessionID}})

		a := it.Agent
		if agentCfg.Enabled && a != nil && a.SessionID != "" {
			note := inboxNote{Name: prompts.NoteAgentStopped, Text: detail, Failed: failed,
				Machine: sess.MachineID, Session: sess.SessionID}
			if current {
				// A stopped current Agent is never a state to restore; an earlier waiting restoration point stays intact.
				// HandlingAgent or waiting remains visible until admission, so a held stop consumes no looking slot.
				if a.Back == HandlingAgent {
					a.Back = ""
				}
				b.queueInboxWake(it, note, reuseSlot, now)
				fresh[it] = a.DeferredAt != nil
			} else {
				// An older Agent's result is informational only and neither flushes a held wake nor reserves capacity.
				b.tell(it, note, false, now)
			}
			deliver[w] = true
		}
		writes = append(writes, w)
	}
	b.admitFreshInboxWakes(agentCfg, now, fresh)
	for _, w := range writes {
		b.settle(w)
	}
	b.mu.Unlock()

	s.saveWrites(ctx, writes, "an Agent's stop")
	saved := true
	for _, w := range writes {
		if w.undone {
			saved = false
			continue
		}
		if deliver[w] {
			s.tellSoon(ctx, w.it)
		}
	}
	return saved
}

// inboxItemLinksAgent: an agent entry, not merely a handling or agent_stopped entry, links machine/session.
func inboxItemLinksAgent(it *InboxItem, machine, sid string) bool {
	for _, e := range it.Chain {
		if e.Kind == ChainAgent && inboxChainSession(e, machine, sid) {
			return true
		}
	}
	return false
}

// inboxAgentStopDuplicate reads the item's persisted stop marker. A newer agent link for the same session starts a new
// assignment. Otherwise the stop is new only when the node's activity time moved past the last recorded TurnAt. A zero
// time compares correctly too: it deduplicates until a node that supplies activity times reports some.
func inboxAgentStopDuplicate(it *InboxItem, sess proto.Session) (linked, duplicate bool) {
	agentAt, stoppedAt := -1, -1
	var stopped InboxChainEntry
	for i, e := range it.Chain {
		if !inboxChainSession(e, sess.MachineID, sess.SessionID) {
			continue
		}
		switch e.Kind {
		case ChainAgent:
			agentAt = i
		case ChainAgentStopped:
			stoppedAt, stopped = i, e
		}
	}
	if agentAt < 0 {
		return false, false
	}
	if stoppedAt > agentAt && stopped.TurnAt != nil && !sess.UpdatedAt.After(*stopped.TurnAt) {
		return true, true
	}
	return true, false
}

func inboxChainSession(e InboxChainEntry, machine, sid string) bool {
	return e.Link != nil && e.Link.Type == LinkSession && e.Link.MachineID == machine && e.Link.SessionID == sid
}

// inboxAgentStopDisplayTime keeps chain timestamps nondecreasing, including across a slow node's clock. This is only
// display bookkeeping; deduplication never compares sess.UpdatedAt with this hub-side time.
func inboxAgentStopDisplayTime(it *InboxItem, sess proto.Session, now time.Time) time.Time {
	at := now.UTC()
	if sess.UpdatedAt.After(at) {
		at = sess.UpdatedAt.UTC()
	}
	if n := len(it.Chain); n > 0 && it.Chain[n-1].At.After(at) {
		at = it.Chain[n-1].At.UTC()
	}
	return at
}

// inboxAgentSession separates an unreachable node (unknown, so only the bounded timeout applies) from a reachable
// node saying the session does not exist (a stopped/lost Agent). Service.session deliberately collapses those cases.
func (s *Service) inboxAgentSession(ctx context.Context, machine, sid string) (sess proto.Session, found, reachable bool) {
	var res struct {
		Session proto.Session `json:"session"`
		OK      bool          `json:"ok"`
	}
	if err := s.call(ctx, machine, DeviceSessionGet, map[string]any{"session_id": sid}, &res); err != nil {
		return proto.Session{}, false, false
	}
	res.Session.MachineID = machine
	return res.Session, res.OK, true
}

// inboxLatestAgentLink is the sub-session HandlingAgent currently waits on.
func inboxLatestAgentLink(it *InboxItem) *InboxChainLink {
	for i := len(it.Chain) - 1; i >= 0; i-- {
		e := it.Chain[i]
		if e.Kind == ChainAgent && e.Link != nil && e.Link.Type == LinkSession && e.Link.MachineID != "" && e.Link.SessionID != "" {
			link := *e.Link
			return &link
		}
	}
	return nil
}
