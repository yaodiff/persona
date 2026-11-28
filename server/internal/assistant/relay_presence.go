package assistant

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The owner comes first (#4925, docs/assistant/assistant.md "Messages from the system wait for the owner"): what the
// system puts into an assistant conversation (supervision messages, task notices, late ask_user answers, messages from
// other conversations) waits while the owner is talking there, and what waits goes in by rank, not in the order it
// came. A scheduled run into such a conversation waits as well (runHold, schedule.go runDue). The rules hold for the
// main conversation and leader conversations alike; they are the manager's, and the queue (assistant.relay) keeps
// its shape.
//
// On top of the presence window stands the owner-turn gate (#6394): when the owner sends a message, every queued
// message waits for the whole of the round that starts for them, not only for the fixed presence window and whatever
// rank it carries. The gate ends when a session list shows the conversation stopped after showing it working, and on
// safety limits when the list says nothing.

var (
	// ownerPresentFor is how long after the owner's last message in a conversation nothing from the system is
	// delivered to it.
	ownerPresentFor = 2 * time.Minute
	// relayDecisionMax is the longest a message that waits on a decision is held for a present owner, counted from
	// when it queued: the time a card stays on one rung (askHoldFor).
	relayDecisionMax = 5 * time.Minute
	// relaySupervisionMax is the longest a non-decision supervision digest (such as completed task reports or reviews)
	// is held for a continuously active owner, counted from when it queued: once reached, it delivers as one merged
	// summary in the next gap between owner turns instead of starving indefinitely while the owner keeps chatting.
	relaySupervisionMax = 10 * time.Minute
	// ownerTurnStartWait is the safety limit for an owner-turn gate whose round no session list ever shows working
	// (a send the node refused, a conversation the list dropped): it releases anyway so the queue cannot stick.
	ownerTurnStartWait = time.Minute
	// ownerTurnMax is the hard cap for a gate whose round a list keeps showing working without an end (a reconnect
	// back to the same turn, a stuck node). The owner speaking again refreshes it.
	ownerTurnMax = 30 * time.Minute
)

// The ranks of the queue, first to go first; within a rank the one that queued first.
const (
	relayRankAnswer   = iota // the owner's late answer to an ask_user question (ask_todo.go)
	relayRankDecision        // a supervision message or task notice that waits on a decision
	relayRankReport          // an outcome the conversation is to report to the owner (a task's notice)
	relayRankHandover        // in a direction's lead: the owner's words handed to it (relayMsg.Handover, #11699)
	relayRankOther           // everything else: other supervision messages, messages from conversations, records
)

// decisionWaiting: somebody waits on what msg starts (the owner's late answer, an agent that needs an answer or was
// cut off, a start that failed), so a present owner holds it for relayDecisionMax at most.
func (msg relayMsg) decisionWaiting() bool {
	switch msg.Tool {
	case askAnswerTool:
		return true
	case superviseTool:
		return msg.Urgent
	case noticeTool:
		return msg.State == "needs" || msg.State == "interrupted" || msg.State == "failed"
	}
	return msg.standalonePermission()
}

// rank is msg's rank in the queue of a conversation that is no direction's lead.
func (msg relayMsg) rank() int { return msg.rankIn(false) }

// rankIn is msg's rank in the queue of a direction's lead (lead) or of another conversation: a hand-over of the owner's
// words to a lead goes after the outcomes to report and before the rest; anywhere else it ranks as any message from a
// conversation does.
func (msg relayMsg) rankIn(lead bool) int {
	switch {
	case msg.Tool == askAnswerTool:
		return relayRankAnswer
	case msg.decisionWaiting():
		return relayRankDecision
	case msg.Tool == noticeTool:
		return relayRankReport
	case lead && msg.Handover != "":
		return relayRankHandover
	}
	return relayRankOther
}

// waitsOnDecision: somebody waits on what the signal asks for. Narrower than urgent, which is how soon a supervision
// message goes out: a start the supervisor's retry brought about and a report that the work is done are told soon, yet
// nobody waits on them.
func (g superviseSignal) waitsOnDecision() bool {
	switch g.Kind {
	case sigStarted:
		return false
	case sigReport:
		return g.Report == upReportQuestion || g.Report == upReportBlocked
	}
	return g.urgent()
}

// anyDecision: one of the signals of a supervision message waits on a decision (relayMsg.Urgent).
func anyDecision(list []superviseSignal) bool {
	return slices.ContainsFunc(list, func(g superviseSignal) bool { return g.waitsOnDecision() })
}

// replacing is msg taking queued message old's place at now. It has waited since old was queued (queuedAs), so a
// message replaced again and again still comes to the end of its hold and its queue wait is the whole wait. One that
// only now waits on a decision counts its relayDecisionMax from now: it starts with the decision, not with the
// ordinary message it merged into.
func (msg relayMsg) replacing(old relayMsg, now time.Time) relayMsg {
	msg = msg.queuedAs(old)
	msg.DecisionAt = time.Time{}
	if msg.decisionWaiting() {
		if old.decisionWaiting() {
			msg.DecisionAt = old.DecisionAt
		} else {
			msg.DecisionAt = now.UTC()
		}
	}
	return msg
}

// decisionSince is when msg began to wait on a decision: when it queued, unless it took the place of a message
// that waited on none (DecisionAt). One queued by a manager from before queued_at counts from its own time.
func (msg relayMsg) decisionSince() time.Time {
	switch {
	case !msg.DecisionAt.IsZero():
		return msg.DecisionAt
	case !msg.QueuedAt.IsZero():
		return msg.QueuedAt
	}
	return msg.At
}

func (msg relayMsg) queuedSince() time.Time {
	if !msg.QueuedAt.IsZero() {
		return msg.QueuedAt
	}
	return msg.At
}

// holdLocked is how long msg still has to wait before it may go into conversation to (0: it may go now); b.mu is
// held. A message that waits on a decision and does not say when it queued is not held, and neither is the owner's
// own answer on a card asked without waiting (relayMsg.Owner, #4928): the hold keeps the system out of the owner's
// way, and that answer is the owner speaking.
//
// While the conversation is on the owner's round (ownerTurns, #6394) every queued message waits whatever its rank,
// even one that waits on a decision: the owner's own message always goes before the queue. The gate releases when a
// session list shows the round ended (ownerRoundsSeen), or on its safety limits if the list never shows the round
// working or never shows it stop.
func (b *relayBook) holdLocked(to string, msg relayMsg, now time.Time) time.Duration {
	if wait := msg.Due.Sub(now); !msg.Due.IsZero() && wait > 0 {
		return wait // past the relay rate: it waits for the window (#10400)
	}
	if msg.Owner {
		return 0
	}
	if turn, ok := b.ownerTurns[to]; ok {
		limit := turn.at.Add(ownerTurnStartWait)
		if turn.working {
			limit = turn.at.Add(ownerTurnMax)
		}
		if wait := limit.Sub(now); wait > 0 {
			return wait
		}
		// The safety limit is authoritative: an expired gate releases at once and does not fall back onto the
		// presence window, or the queue could wait longer than the cap.
		delete(b.ownerTurns, to)
		return 0
	}
	spoke, ok := b.st.Spoke[to]
	if !ok {
		return 0
	}
	until := spoke.Add(ownerPresentFor)
	if msg.decisionWaiting() {
		if latest := msg.decisionSince().Add(relayDecisionMax); latest.Before(until) {
			until = latest
		}
	} else if msg.Tool == superviseTool {
		if since := msg.queuedSince(); !since.IsZero() {
			if latest := since.Add(relaySupervisionMax); latest.Before(until) {
				until = latest
			}
		}
	}
	return max(until.Sub(now), 0)
}

// recordMsgFailedLocked records when msg in conversation to failed with a retryable error (b.mu held).
func (b *relayBook) recordMsgFailedLocked(to string, msg relayMsg, at time.Time) {
	if b.msgFailedAt == nil {
		b.msgFailedAt = map[string]map[string]time.Time{}
	}
	per := b.msgFailedAt[to]
	if per == nil {
		per = map[string]time.Time{}
		b.msgFailedAt[to] = per
	}
	per[relayMsgID(to, msg)] = at
}

// clearMsgFailedLocked removes msgID's per-message failure backoff from conversation to (b.mu held).
func (b *relayBook) clearMsgFailedLocked(to, msgID string) {
	if per := b.msgFailedAt[to]; per != nil {
		delete(per, msgID)
		if len(per) == 0 {
			delete(b.msgFailedAt, to)
		}
	}
}

// msgBackoffLocked returns how long msg in conversation to still waits under relayRetryGap before it may be retried
// (0: eligible now). For the last-failed message (b.stuck), b.failedAt takes precedence when set so clearing or
// adjusting failedAt stays authoritative; other previously failed messages honor their own msgFailedAt timestamp so
// multi-message queues cannot ping-pong past relayRetryGap (#17592). b.mu is held.
func (b *relayBook) msgBackoffLocked(to string, msg relayMsg, now time.Time) time.Duration {
	if relayRetryGap <= 0 {
		return 0
	}
	if last, stuck := b.stuck[to]; stuck && msg == last {
		if at, ok := b.failedAt[to]; ok && !at.IsZero() {
			if gap := relayRetryGap - now.Sub(at); gap > 0 {
				return gap
			}
			return 0
		}
	}
	if at, ok := b.msgFailedAt[to][relayMsgID(to, msg)]; ok && !at.IsZero() {
		if gap := relayRetryGap - now.Sub(at); gap > 0 {
			return gap
		}
	}
	return 0
}

// pickLocked is the index of the message of conversation to's queue that goes in next: the best rank among those not
// held for the owner, the one that queued first within it (lead: to is a direction's lead, rankIn). The message that
// could not go in last time (b.stuck: its agent's computer unreachable, a delivery that failed) is passed over while
// another may go, so it holds up nothing but itself. Messages still inside their relayRetryGap backoff are held so
// multi-message queues do not alternate past the gap (#17592); a withdrawn one is not, since it is never sent again.
// None may go (-1): wait is how long until the first may.
// b.mu is held.
func (b *relayBook) pickLocked(to string, lead bool, now time.Time) (i int, wait time.Duration) {
	i = -1
	stuck := -1
	q := b.st.Queue[to]
	for j, m := range q {
		lookAfter, backoff := m.LookAfter.Sub(now), b.msgBackoffLocked(to, m, now)
		if m.Withdrawn {
			// No retry is sent: only check whether the original in-flight text landed. Neither its retry time nor its
			// failure backoff holds it.
			lookAfter, backoff = 0, 0
		}
		if h := max(b.holdLocked(to, m, now), lookAfter, backoff); h > 0 {
			if wait == 0 || h < wait {
				wait = h
			}
			continue
		}
		if last, ok := b.stuck[to]; ok && stuck < 0 && m == last {
			stuck = j
			continue
		}
		if i < 0 || m.rankIn(lead) < q[i].rankIn(lead) {
			i = j
		}
	}
	if i < 0 {
		i = stuck // nothing else may go: it is tried again
	}
	if i >= 0 {
		wait = 0
	}
	return i, wait
}

// frontLocked moves message i of conversation to's queue to its head, where the message going in is (what replaces
// or drops queued messages leaves the head of a draining queue alone); the others keep their order. b.mu is held.
func (b *relayBook) frontLocked(to string, i int) {
	q := b.st.Queue[to]
	if i <= 0 || i >= len(q) {
		return
	}
	msg := q[i]
	copy(q[1:i+1], q[:i])
	q[0] = msg
}

// ownerSpokeLocked notes that the owner sent conversation id a message at now; b.mu is held. It also arms the
// owner-turn gate (#6394): from now until the round is seen over, nothing of the queue goes into that conversation.
// A gate already armed keeps its working mark, since the owner may have spoken into a round already running (the
// interject case), and the new message refreshes the safety limits.
func (b *relayBook) ownerSpokeLocked(id string, now time.Time) {
	if b.st.Spoke == nil {
		b.st.Spoke = map[string]time.Time{}
	}
	b.st.Spoke[id] = now.UTC()
	if b.ownerTurns == nil {
		b.ownerTurns = map[string]ownerTurn{}
	}
	turn := b.ownerTurns[id]
	turn.at = now.UTC()
	b.ownerTurns[id] = turn
}

// ownerRoundsSeen walks a node's session list and releases owner-turn gates whose rounds are over (#6394). A
// conversation shown working is on its round; one shown stopped after that releases at once. A gate no list ever
// shows working releases after ownerTurnStartWait (the send never ran), and any gate older than ownerTurnMax
// releases as a hard cap, including one whose conversation the list no longer contains.
func (s *Service) ownerRoundsSeen(list []proto.Session) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ownerRoundsSeenLocked(list, time.Now())
}

// ownerRoundsSeenLocked is ownerRoundsSeen with b.mu held and the clock given (the unit tests construct a bare
// relayBook).
func (b *relayBook) ownerRoundsSeenLocked(list []proto.Session, now time.Time) {
	if len(b.ownerTurns) == 0 {
		return
	}
	for id, turn := range b.ownerTurns {
		if now.Sub(turn.at) > ownerTurnMax {
			delete(b.ownerTurns, id)
		}
	}
	for i := range list { // by index: a range value copies every (large) session it passes
		id, status := list[i].SessionID, list[i].Status
		turn, ok := b.ownerTurns[id]
		if !ok {
			continue
		}
		switch {
		case status == proto.StatusWorking:
			if !turn.working {
				turn.working = true
				b.ownerTurns[id] = turn
			}
		case status == proto.StatusDone || status == proto.StatusIdle || status == proto.StatusInterrupted:
			if turn.working || now.Sub(turn.at) > ownerTurnStartWait {
				delete(b.ownerTurns, id)
			}
		}
	}
}

// ownersPresent is when the owner last spoke in each conversation they are present in at now.
func (s *Service) ownersPresent(now time.Time) map[string]time.Time {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return nil
	}
	present := map[string]time.Time{}
	for id, spoke := range b.st.Spoke {
		if now.Sub(spoke) < ownerPresentFor {
			present[id] = spoke
		}
	}
	return present
}

// runHold is how long the scheduled run of t that was due at due still waits at now, because the owner is talking
// (present: ownersPresent) in the conversation it goes into: the one it came from, else its own. A run has a time
// the owner set, so it waits relayDecisionMax past that time at most, as a message that waits on a decision does;
// "Run now" is the owner's own and is not held (it does not come through runDue).
func runHold(present map[string]time.Time, t Task, due, now time.Time) time.Duration {
	id := t.RunSessionID
	if t.continuesSource() {
		id = t.SessionID
	}
	spoke, ok := present[id]
	if !ok || id == "" {
		return 0
	}
	until := spoke.Add(ownerPresentFor)
	if latest := due.Add(relayDecisionMax); latest.Before(until) {
		until = latest
	}
	return max(until.Sub(now), 0)
}

// relayWaits: msg can't go into conversation to right now even though it is not running: others wait in its queue
// already, the owner is talking in it, or it is a background supervisor being replaced (replacing). The caller queues
// it and drains (drainRelays), which delivers what may go and comes back when the hold ends.
func (s *Service) relayWaits(to string, msg relayMsg) bool {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return false
	}
	return b.replacing[to] || len(b.st.Queue[to]) > 0 || b.holdLocked(to, msg, time.Now()) > 0
}

// unresolvedSupervision reports whether msg is a supervision relay whose transcript outcome is not yet reconciled:
// a tried attempt, a legacy durable-doubt-only entry, or a legacy withdrawn entry.
func (msg relayMsg) unresolvedSupervision() bool {
	return msg.Tool == superviseTool && (!msg.TriedAt.IsZero() || !msg.DoubtedAt.IsZero() || msg.Withdrawn)
}

// relayHasUnresolvedSupervision reports whether conversation to has a queued supervision relay whose transcript
// outcome is not yet reconciled (tried, durable-doubt-only, withdrawn, or in-memory doubted). It fails closed when
// the relay state cannot be read so an interjection cannot reorder ahead of an unresolved delivery (#12567).
func (s *Service) relayHasUnresolvedSupervision(to string) bool {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return true
	}
	for _, m := range b.st.Queue[to] {
		if m.unresolvedSupervision() || m.Tool == superviseTool && !b.doubted[relayMsgID(to, m)].IsZero() {
			return true
		}
	}
	return false
}

// relayReplacing: conversation to is a background supervisor reserved for its replacement (supervise_round.go
// backgroundOutgrown): nothing goes in straight, not even a decision reply that may interject into a running turn.
func (s *Service) relayReplacing(to string) bool {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.replacing[to]
}

// drainAfter drains conversation id's queue once more in wait, when the hold on what waits there ends. One timer per
// conversation: a later call replaces it.
func (s *Service) drainAfter(machine, id string, wait time.Duration) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	s.drainAfterLocked(machine, id, wait)
}

func (s *Service) drainAfterLocked(machine, id string, wait time.Duration) {
	b := &s.relays
	if t := b.holds[id]; t != nil {
		t.Stop()
	}
	var t *time.Timer
	t = time.AfterFunc(wait, func() {
		b.mu.Lock()
		if b.holds[id] == t {
			delete(b.holds, id)
		}
		b.mu.Unlock()
		s.drainConversations(machine, []string{id})
	})
	b.holds[id] = t
}

// drainConversations drains the queues of machine's conversations ids as its list has them now. A list that can't
// be read (false) leaves them to the next look (drainHeldRelays) or the next change of the list.
func (s *Service) drainConversations(machine string, ids []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := s.sessions(ctx, machine)
	if err != nil {
		s.logf("relay: held messages of %d conversation(s) wait on: %v", len(ids), err)
		return false
	}
	list = slices.DeleteFunc(slices.Clone(list), func(c proto.Session) bool { return !slices.Contains(ids, c.SessionID) })
	s.drainRelays(machine, list)
	return true
}

// drainHeldRelays recovers queues whose timers were lost on restart or could not read the session list. It covers
// owner presence, rate limits, and tries in doubt. The owner's last message is forgotten once ownerPresentFor has
// passed and what it held was handed on.
func (s *Service) drainHeldRelays(ctx context.Context, now time.Time) {
	s.escalateStaleSupervisorRelays(ctx, now)
	leads := s.queueLeads(ctx)
	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(ctx) {
		b.mu.Unlock()
		return
	}
	var due []string
	pruned := false
	for id, q := range b.st.Queue {
		if b.draining[id] {
			continue // the in-flight attempt owns the head and its outcome
		}
		kept := slices.DeleteFunc(q, func(m relayMsg) bool {
			if !m.standalonePermission() {
				return false
			}
			_, pending := s.pendingStandalonePermission(id, m, now)
			return !pending
		})
		if len(kept) != len(q) {
			pruned = true
			if len(kept) == 0 {
				delete(b.st.Queue, id)
				continue
			}
			b.st.Queue[id] = kept
		}
		if _, spoke := b.st.Spoke[id]; spoke || b.holds[id] != nil {
			continue
		}
		if slices.ContainsFunc(kept, func(m relayMsg) bool {
			return !m.Due.IsZero() || !m.TriedAt.IsZero() || m.unresolvedSupervision() || m.standalonePermission() ||
				(m.Tool == extensionEventTool && !b.failedAt[id].IsZero())
		}) {
			if i, _ := b.pickLocked(id, leads[id], now); i >= 0 {
				due = append(due, id)
			}
		}
	}
	for id, spoke := range b.st.Spoke {
		if len(b.st.Queue[id]) == 0 {
			if now.Sub(spoke) >= ownerPresentFor {
				delete(b.st.Spoke, id)
			}
			continue
		}
		if b.holds[id] != nil || b.draining[id] {
			continue
		}
		if i, _ := b.pickLocked(id, leads[id], now); i >= 0 {
			due = append(due, id)
		}
	}
	if pruned {
		s.saveRelayLocked()
	}
	b.mu.Unlock()
	if pruned {
		s.publish(ctx)
	}
	if len(due) == 0 {
		return
	}
	machine := s.settings(ctx).MachineID
	if machine == "" || !s.drainConversations(machine, due) {
		return
	}
	b.mu.Lock()
	for _, id := range due {
		if spoke, ok := b.st.Spoke[id]; ok && now.Sub(spoke) >= ownerPresentFor {
			delete(b.st.Spoke, id) // handed on: a delivery that fails waits for the list to change, as ever
		}
	}
	b.mu.Unlock()
}
