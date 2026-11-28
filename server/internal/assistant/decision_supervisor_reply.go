package assistant

import (
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Side-conversation markers, kept local so this file does not depend on task_completion.go's
// package vars (a parallel change extracts a shared prompts classifier). They match the two
// envelopes RenderSideConversation emits (prompts/assistant/side_conversation.md).
var (
	supervisorSideConversationContext = lazyre.New(`<persona-context\b[^>]*\bkind\s*=\s*["']side-conversation["'][^>]*>`)
	supervisorSideConversationMarker  = lazyre.New(`<persona-side-conversation\s*/>`)
)

// supervisorDecisionSpoke handles an owner message written in a supervisor / direction
// conversation itself. The Agent-chat path (agentDecisionSpoke) anchors on the to-do's worker
// session via activeTodoOf, which a supervisor conversation never is; this is the reverse
// lookup: the open needs_user to-dos supervised by the conversation the owner wrote in.
//
// An exact option label settles the single to-do it uniquely matches, but only once the hub
// says the message was delivered (#13041): the hub tells session.user_spoke before it sends, so
// the choice waits by the send's client_msg_id (deferSupervisorSettle) and settles on a
// session.assistant_sent reporting delivered (supervisorSettleOutcome). A failed or unknown send
// the hub keeps for its manager to send later (undelivered.go) settles when that kept message goes
// in or is found in the conversation; one that is never kept, fails or is deleted leaves the card
// waiting. A spoke without an id (an older hub, or a client that names no
// message) cannot be matched to its outcome, so it is handed back like free text instead of
// settling. A lone option letter
// ("A", "b") never settles here: unlike an Agent session, a supervisor / direction conversation
// is usually the owner's main chat and carries unrelated replies (its own lettered or yes/no
// questions), so a one-letter message cannot be tied to the card and is treated as free text
// instead. Free text never guesses an option: each affected to-do pauses its default and hands
// back to this conversation's supervisor through the existing decision_reply mechanism
// (#11852), with the reply sourced from the supervisor conversation. Provenance-marked relay /
// side-conversation input is not the owner answering and changes nothing. When one message
// could settle several to-dos at once, none is settled silently: every candidate is handed back
// for an explicit choice.
func (s *Service) supervisorDecisionSpoke(ctx context.Context, r SessionRef, at time.Time, text, clientMsgID string) {
	if !supervisorOwnerText(text) {
		return
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		s.logf("decision: supervisor reply: load tasks: %v", err)
		return
	}
	candidates := s.supervisedDecisionCandidates(r.SessionID)
	if len(candidates) == 0 {
		return
	}
	matched := make([]Todo, 0, len(candidates))
	for _, t := range candidates {
		a, ok := s.askCopy(t.ID)
		if !ok || supervisorOptionLabel(text, a.Options) == "" {
			continue
		}
		matched = append(matched, t)
	}
	switch {
	case len(matched) == 1 && len(candidates) == 1 && clientMsgID != "":
		s.holdSupervisorChoice(ctx, r, clientMsgID, matched[0].ID, at, text)
	case len(matched) == 1 && len(candidates) == 1:
		// No id to learn the send's outcome by: never close the card irreversibly on a message that may not
		// have gone in; the supervisor reads the words and confirms with task_decided.
		s.handBackSupervisorReplies(ctx, r, at, candidates, text)
	case len(matched) == 1:
		// One exact match among several open questions is still ambiguous: the same words may be
		// meant for another card, so do not settle; hand every candidate back to the supervisor.
		s.handBackSupervisorReplies(ctx, r, at, candidates, text)
	default:
		// No unique exact answer (zero or several matches): never guess, hand them all back.
		s.handBackSupervisorReplies(ctx, r, at, candidates, text)
	}
}

// supervisorOwnerText is the provenance gate for a message observed on a supervisor
// conversation: a relay, a message-source envelope or a side-conversation wrapper is another
// conversation's input, not the owner answering a card. Empty text (a hook without payload) has
// no owner words to settle with, so it is ignored here rather than treated as an answer.
func supervisorOwnerText(text string) bool {
	if text == "" {
		return false
	}
	if supervisorSideConversationContext.MatchString(text) || supervisorSideConversationMarker.MatchString(text) {
		return false
	}
	if src := prompts.ItemSource(proto.Item{Role: "user", Kind: proto.KindText, Text: text}); src != nil && src.Kind != "" {
		return false
	}
	return prompts.ParseRelaySource(text) == nil
}

// supervisedDecisionCandidates returns the open to-dos that conversation supervisorID
// supervises and whose decide ask is still waiting, as the caller's snapshot reads them. A
// supervisor conversation is never an active worker session, so activeTodoOf cannot find these;
// the Supervision.Supervisor relationship is the reverse index.
func (s *Service) supervisedDecisionCandidates(supervisorID string) []Todo {
	s.todos.mu.RLock()
	todos := make([]Todo, 0, len(s.todos.todos))
	for _, t := range s.todos.todos {
		if t.DoneAt != nil || t.Supervision == nil || t.Supervision.Supervisor != supervisorID ||
			t.Supervision.State != reviewNeedsUser {
			continue
		}
		todos = append(todos, *t)
	}
	s.todos.mu.RUnlock()
	out := todos[:0]
	for _, t := range todos {
		a, ok := s.askCopy(t.ID)
		// A can_wait decision held for a briefing has no card yet and the owner has never seen
		// this round: an ordinary supervisor-conversation message must neither release the hold
		// nor settle it (heldForBriefing also clears once a briefing lists it).
		if !ok || heldForBriefing(&a) || !decisionOpen(&a, t) || !a.RecheckAt.IsZero() ||
			ownerDecisionReturned(t.Supervision) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// supervisorOptionLabel matches a full option label only, never a lone letter. The Agent-chat
// path accepts letters because that session is scoped to one task; the supervisor conversation
// carries every conversation the owner has, so an isolated "A" there usually answers something
// else and must not settle a card irreversibly.
func supervisorOptionLabel(text string, options []DecisionOption) string {
	return matchDecisionOption(text, options, false)
}

// settleSupervisorChoice records the waiting choice p (the book's entry key, claimed by the caller: its message was
// delivered) and delivers it to the worker Agent, then ends the entry. chooseOptionLocked is the delivery path the
// card and task_decided (told_agent=false) use: it relays the choice to the task's Agent before recording it, and
// askRound revalidates the whole round (acceptAskEnded, the latest review's staleness, RecheckAt and
// ownerDecisionReturned), so neither an undelivered record-only close nor a superseded round can settle. p.asked is
// the round the owner answered: a round asked since is another question, which their words never settle.
//
// The card's action lock is held from the look at the round until the entry is gone, and the entry stays in the book,
// marked settling, until then: the deadline default and the overdue reminder look for a waiting choice under the same
// lock (handleTaskDeadline), so while this waits for the lock they still find the choice, and once it
// lets go the round is answered.
//
// The entry ends here whatever chooseOptionLocked says, since nothing would settle it later (the delivered notice,
// or the kept message going in, comes once), and one left behind would only hold the default:
//   - recorded: the round is answered.
//   - the round is over or replaced (a zero result), or the words name no option any more: the card was answered
//     another way, or asks something else; the choice no longer applies.
//   - no Agent can take it now (decisionWaitsForSupervisor, #17431): recorded and handed to the supervisor, the round
//     answered.
//   - the choice did not reach the Agent otherwise, or could not be saved: the card keeps waiting, as chooseOption leaves it for
//     the card and task_decided. Their own option has marked the deadline answered before the send
//     (deadlineAnswered), so no default follows; the recommended one failing leaves the default to take that same
//     option at the deadline.
func (s *Service) settleSupervisorChoice(ctx context.Context, key [3]string, p supervisorSettle) {
	if f := s.supervisorSettleHook; f != nil {
		f(p.todo)
	}
	id := p.todo
	s.reviewDismissals.actions.acquire(id)
	defer s.reviewDismissals.actions.release(id)
	defer s.endSupervisorSettle(key) // runs first: the entry goes under the action lock
	cur, ok := s.todoNow(id)
	if !ok || cur.DoneAt != nil || cur.Supervision == nil || cur.Supervision.State != reviewNeedsUser {
		return
	}
	// An active "Don't handle" hides this round: an option-shaped message in the supervisor
	// conversation must not reopen and execute it. Fail closed on a read error, like the
	// deadline sweep, rather than settling against an unknown dismissal state.
	if dismissed, err := s.decisionDismissed(ctx, cur); err != nil || dismissed {
		return
	}
	a, ok := s.askCopy(id)
	if !ok || heldForBriefing(&a) || !decisionOpen(&a, cur) || !a.RecheckAt.IsZero() || ownerDecisionReturned(cur.Supervision) ||
		!p.at.After(a.AskedAt) || !a.AskedAt.Equal(p.asked) {
		return
	}
	label := supervisorOptionLabel(p.text, a.Options)
	if label == "" {
		return
	}
	if _, err := s.chooseOptionLocked(ctx, id, p.asked.UTC().Format(timeKeyLayout), label); err != nil {
		s.logf("task %s: decision: deliver supervisor choice: %v", id, err)
	}
}

// handBackSupervisorReplies pauses each candidate's default and emits a decision_reply signal
// that routes back to this conversation's supervisor digest. No option is inferred: the
// supervisor reads the owner's words and confirms with task_decided or clarifies.
func (s *Service) handBackSupervisorReplies(ctx context.Context, r SessionRef, at time.Time, todos []Todo, text string) {
	for _, t := range todos {
		s.handBackOneSupervisorReply(ctx, r, at, t.ID, text)
	}
}

func (s *Service) handBackOneSupervisorReply(ctx context.Context, r SessionRef, at time.Time, id, text string) {
	s.reviewDismissals.actions.acquire(id)
	defer s.reviewDismissals.actions.release(id)
	cur, ok := s.todoNow(id)
	if !ok || cur.DoneAt != nil || cur.Supervision == nil || cur.Supervision.State != reviewNeedsUser {
		return
	}
	// A hidden ("Don't handle") round stays hidden: free text must not pause or hand it back.
	if dismissed, err := s.decisionDismissed(ctx, cur); err != nil || dismissed {
		return
	}
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return
	}
	a := b.asks[id]
	if a == nil || heldForBriefing(a) || !decideAsk(a) || a.AskedAt.IsZero() || !a.AcceptedAt.IsZero() ||
		!at.After(a.AskedAt) || acceptAskStale(a, cur) || acceptAskEnded(a, cur) || !a.RecheckAt.IsZero() ||
		ownerDecisionReturned(cur.Supervision) {
		b.mu.Unlock()
		return
	}
	if a.AgentReply != nil { // a prior reply already paused this round; keep the first hand-off
		b.mu.Unlock()
		return
	}
	a.AgentReply = &decisionAgentReply{MachineID: r.MachineID, SessionID: r.SessionID, At: at.UTC(),
		Text: clip(text, 2000), FromSupervisor: true}
	a.DueAt = at.UTC()
	// Even a failed write must not let this process take the default; the in-memory pause stands and the sweep retries.
	if err := s.saveAcceptErrLocked(ctx); err != nil {
		s.logf("task %s: decision: persist supervisor reply: %v", id, err)
	}
	cp := *a
	b.mu.Unlock()
	s.askChanged(ctx)
	s.signalSupervisorDecisionReply(ctx, id, cp)
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		s.updateAskCards(ctx, id, cp)
	}()
}

// Exact choices typed in a supervisor conversation wait here for their send's outcome (#13041). The book is memory
// only on purpose: nothing about the decision is persisted until delivery settles it, so a manager restart while one
// waits leaves the card exactly as the accept ledger has it (still waiting, its default as before), and a delivered
// notice, or a kept message going in, that the new process cannot match settles nothing.
//
// A send that does not go in at once is not always over: for a failed send the conversation refused before typing
// (busy, a 5xx) or one whose outcome is unknown, the hub hands the same message to keepUndelivered right after the
// notice, and the manager sends it later (undeliveredSend → deliverKept) or finds it in the conversation
// (undeliveredLook). Neither tells session.assistant_sent again, so the entry stays, marked kept: it holds the default
// while the kept message waits and settles where that message goes in (claimKeptSupervisorSettleLocked). It is
// dropped when the kept message fails (given up, the conversation gone, the device signed out, not found after a try
// in doubt), the owner deletes it, or the hub never keeps it (supervisorKeepGrace after the notice).
//
// A kept mark belongs to the try it was made for. A failed send the hub does not keep is forgotten by the hub, so the
// app retries the same id as a new send: its session.user_spoke comes again, then its own notice. That spoke clears a
// kept mark no kept message stands behind (deferSupervisorSettle), or the reap would drop the entry before the
// retry's delivered notice could claim it. Notices are handled apart from spokes (bus.go runs ConversationSent on its
// own), so an earlier try's failed notice can be handled after the retry's spoke: the entry counts its tries and the
// notices told, and a failed or unknown notice marks it kept only when it is the latest try's
// (markSupervisorSettleKept).
//
// Locks, outermost first: the card's action lock (s.reviewDismissals.actions), then s.undelivered.mu, then the book's
// own mutex. The accept ledger's mutex (s.accept.mu) may be taken under the action lock, and this file never holds
// it together with the other two. A choice is registered under its card's action lock (holdSupervisorChoice), the
// lock the deadline default holds while it checks the book once more and takes the recommendation (handleTaskDeadline):
// a choice either is in the book before the default looks, or finds the round answered and is not registered.
//
// A choice whose message went in stays in the book until it is recorded. Whoever learns of the delivery claims the
// entry (claimSupervisorSettle marks it settling; a kept message's is claimed under s.undelivered.mu with that
// message's drop), then takes the action lock, records the choice and only then removes the entry, still under that
// lock (settleSupervisorChoice). So the default never finds the book empty between a delivery and its record. A
// settling entry belongs to its settler alone: it does not expire, is not evicted when the book is full, is not
// reaped for a kept message that is gone (it went in), and a second notice for it claims nothing.
//
// Every other way an entry leaves the book ends the choice unsettled, and none of them decides anything on the card:
// expiry and the full book's eviction (pruneLocked), a kept message that failed, was deleted or never came
// (reapSupervisorSettles), a round that was replaced (supervisorSettlePending). They need no action lock: a claim
// that comes after one of them finds no entry and settles nothing, and the default then applies as the ledger has it.
const (
	// supervisorSettleTTL outlives the hub's longest wait for a send (five minutes to a session still starting, plus
	// the manager's preparation): an entry whose notice never comes (a send the manager refused, a hub restart)
	// stops holding the default after this. A kept entry lives as long as its kept message may still be sent.
	supervisorSettleTTL = 10 * time.Minute
	// supervisorSettleMax bounds the book: each entry is one owner message waiting for its own send.
	supervisorSettleMax = 64
	// supervisorKeepGrace is how long after a failed or unknown notice the kept message may still arrive: the hub's
	// keepUndelivered call follows the notice and waits up to ten seconds, and the two travel separately.
	supervisorKeepGrace = 30 * time.Second
)

type supervisorSettle struct {
	todo    string
	asked   time.Time // the round answered (its ask's AskedAt): it holds no other round's default and settles no other
	at      time.Time // when the owner spoke: a round asked after it is not the one answered
	text    string
	expires time.Time
	// kept: the send did not go in at once, and the hub keeps (or is about to keep) the message to send later; the
	// entry then lives only while that kept message waits. keptSeen: the keep reached this manager. Until then, and
	// until keepBy, a missing kept message may only be on its way.
	kept     bool
	keptSeen bool
	keepBy   time.Time
	// tries: the sends of this message told by session.user_spoke while no kept message stood for it; told: the
	// failed or unknown notices handled. A notice handled while told < tries is an earlier try's, with a later one on
	// its way.
	tries, told int
	// settling: the message went in and its settler has claimed the entry (claimSupervisorSettle); it stays, holding
	// the default, until the choice is recorded under the card's action lock, and only its settler removes it.
	settling bool
}

type supervisorSettleBook struct {
	mu      sync.Mutex
	pending map[[3]string]supervisorSettle // machine, session, client_msg_id
	clock   func() time.Time               // nil: time.Now (tests set it before the first choice)
}

func (b *supervisorSettleBook) now() time.Time {
	if b.clock != nil {
		return b.clock()
	}
	return time.Now()
}

func supervisorSettleKey(r SessionRef, clientMsgID string) [3]string {
	return [3]string{r.MachineID, r.SessionID, clientMsgID}
}

// pruneLocked drops expired entries and, when the book is full, the ones closest to expiring. One being settled is
// neither: its message went in, and it leaves when its choice is recorded (they are as few as the settlements under
// way, so the book stays bounded).
func (b *supervisorSettleBook) pruneLocked(now time.Time, room int) {
	for k, p := range b.pending {
		if !p.settling && !now.Before(p.expires) {
			delete(b.pending, k)
		}
	}
	for len(b.pending) > supervisorSettleMax-room {
		var oldest [3]string
		var at time.Time
		for k, p := range b.pending {
			if !p.settling && (at.IsZero() || p.expires.Before(at)) {
				oldest, at = k, p.expires
			}
		}
		if at.IsZero() {
			return // only entries being settled are left
		}
		delete(b.pending, oldest)
	}
}

// holdSupervisorChoice registers the owner's exact choice of to-do id's decision, written in r as message
// clientMsgID, to settle when that message is delivered. It runs under the card's action lock, which the deadline
// default holds too (handleTaskDeadline): once this returns the default finds the choice, and a default that got there
// first has answered the round, so nothing is registered for it.
func (s *Service) holdSupervisorChoice(ctx context.Context, r SessionRef, clientMsgID, id string, at time.Time, text string) {
	s.reviewDismissals.actions.acquire(id)
	defer s.reviewDismissals.actions.release(id)
	cur, ok := s.todoNow(id)
	if !ok || cur.DoneAt != nil || cur.Supervision == nil || cur.Supervision.State != reviewNeedsUser {
		return
	}
	// An active "Don't handle" hides this round: an option-shaped message in the supervisor
	// conversation must not hold or settle it. Fail closed on a read error, like the
	// deadline sweep, rather than acting against an unknown dismissal state.
	if dismissed, err := s.decisionDismissed(ctx, cur); err != nil || dismissed {
		return
	}
	a, ok := s.askCopy(id)
	if !ok || heldForBriefing(&a) || !decisionOpen(&a, cur) || !a.RecheckAt.IsZero() || ownerDecisionReturned(cur.Supervision) ||
		!at.After(a.AskedAt) || supervisorOptionLabel(text, a.Options) == "" {
		return
	}
	s.deferSupervisorSettle(ctx, r, clientMsgID, id, a.AskedAt, at, text)
}

// deferSupervisorSettle keeps an exact choice of the round asked until the hub says how its send went. While it
// waits, that round's deadline default holds (supervisorSettlePending): the owner has answered, and the answer may
// only be in flight.
//
// The hub tells session.user_spoke for every try of a message, also a retry it then answers from its own record of
// the send (delivered, or kept by the manager), which tells no session.assistant_sent again. So the same message
// told again while the manager keeps it changes nothing: the entry waits on that kept message as before. Told again
// while it is still on its way, it keeps its time and counts the try. Told again when the entry is marked kept but no
// kept message stands behind it (the hub did not keep the failed send and forgot the id, or the kept message is
// gone), it is a new send: the entry starts over on its way, and the retry's own notice settles it. A message with no
// entry that the manager already keeps (the entry was lost, or the choice only matches now) waits on that kept
// message from the start, not on a notice that will not come.
func (s *Service) deferSupervisorSettle(ctx context.Context, r SessionRef, clientMsgID, todo string, asked, at time.Time, text string) {
	key := supervisorSettleKey(r, clientMsgID)
	// s.undelivered.mu stays held over the registration: the kept message cannot go in between the look and the entry
	// (it claims the entry under this lock, claimKeptSupervisorSettleLocked).
	u := &s.undelivered
	u.mu.Lock()
	defer u.mu.Unlock()
	kept := false
	if s.undeliveredLocked(ctx) {
		m := s.keptLocked(r.SessionID, clientMsgID)
		kept = m != nil && m.Machine == r.MachineID && m.State == proto.UndeliveredWaiting
	}
	b := &s.supervisorSettles
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil {
		b.pending = map[[3]string]supervisorSettle{}
	}
	now := b.now()
	b.pruneLocked(now, 0)
	p := supervisorSettle{todo: todo, asked: asked, at: at, text: text, expires: now.Add(supervisorSettleTTL), tries: 1}
	if old, ok := b.pending[key]; ok {
		switch {
		case old.settling: // its message went in; its settler ends it
		case kept: // a retry the hub answers as kept: the entry waits on the kept message
			if !old.kept || !old.keptSeen {
				old.kept, old.keptSeen, old.keepBy = true, true, time.Time{}
				if e := now.Add(undeliveredGiveUp + supervisorSettleTTL); e.After(old.expires) {
					old.expires = e
				}
				b.pending[key] = old
			}
		case old.kept: // no kept message stands behind the mark: this is a new send, on its way like the first
			p.tries, p.told = old.tries+1, old.told
			b.pending[key] = p
		default: // still on its way, or an earlier try's notice is not handled yet
			old.tries++
			b.pending[key] = old
		}
		return
	}
	b.pruneLocked(now, 1)
	if kept {
		p.kept, p.keptSeen, p.expires = true, true, now.Add(undeliveredGiveUp+supervisorSettleTTL)
	}
	b.pending[key] = p
}

// supervisorSettlePending says whether an exact choice of the round to-do id's decision asks now still waits for its
// send's outcome, or is being settled now that its message went in (its settler waits for the card's action lock or
// holds it). A choice of an earlier round answers a question that is no longer asked: it is dropped here (one being
// settled is left to its settler), and never holds the default of the round that replaced it.
func (s *Service) supervisorSettlePending(id string, now time.Time) bool {
	s.reapSupervisorSettles(now)
	cur, asking := s.askCopy(id) // before the book's lock: s.accept.mu and the book are never held together
	b := &s.supervisorSettles
	b.mu.Lock()
	defer b.mu.Unlock()
	pending := false
	for k, p := range b.pending {
		if p.todo != id || !p.settling && !now.Before(p.expires) {
			continue
		}
		// Rounds only move on: an entry of a round asked after the one read here is the current one's.
		if asking && p.asked.Before(cur.AskedAt) {
			if !p.settling {
				delete(b.pending, k)
			}
			continue
		}
		pending = true
	}
	return pending
}

// markSupervisorSettleKept marks the waiting choice clientMsgID in r as kept for a later send: keepBy is until when
// its kept message may still be missing (a failed or unknown notice, which can come before the keep); zero, the keep
// itself reached this manager. A notice of an earlier try than the latest told leaves the entry on its way: the later
// try's own notice says how it went. Nothing waits under that id: nothing.
func (s *Service) markSupervisorSettleKept(r SessionRef, clientMsgID string, keepBy time.Time) {
	if clientMsgID == "" {
		return
	}
	b := &s.supervisorSettles
	b.mu.Lock()
	defer b.mu.Unlock()
	key := supervisorSettleKey(r, clientMsgID)
	p, ok := b.pending[key]
	now := b.now()
	if !ok || p.settling || !now.Before(p.expires) {
		return
	}
	if !keepBy.IsZero() {
		if p.told++; p.told < p.tries {
			b.pending[key] = p
			return
		}
	}
	p.kept = true
	if keepBy.IsZero() {
		p.keptSeen = true
	} else if keepBy.After(p.keepBy) {
		p.keepBy = keepBy
	}
	// A kept message is tried for a day before it is given up; the reap ends the entry when it fails sooner.
	if e := now.Add(undeliveredGiveUp + supervisorSettleTTL); e.After(p.expires) {
		p.expires = e
	}
	b.pending[key] = p
}

// reapSupervisorSettles drops expired entries and kept ones whose kept message no longer waits: it failed, the owner
// deleted it, or the hub never kept it. One that went in was claimed when it went in (settling) and is left to its
// settler. This book's mutex is never held while taking the undelivered side's (that side takes this book under its
// own lock), so each is read on its own; a kept message that goes in between the two reads had its entry claimed with
// its drop, under s.undelivered.mu, so the entry is settling by the time it is looked at again and stays.
func (s *Service) reapSupervisorSettles(now time.Time) {
	b := &s.supervisorSettles
	b.mu.Lock()
	b.pruneLocked(now, 0)
	type keptEntry struct {
		key     [3]string
		waiting bool // the kept message may still be on its way: missing is not gone yet
	}
	var kept []keptEntry
	for k, p := range b.pending {
		if p.kept && !p.settling {
			kept = append(kept, keptEntry{k, !p.keptSeen && now.Before(p.keepBy)})
		}
	}
	b.mu.Unlock()
	if len(kept) == 0 {
		return
	}
	u := &s.undelivered
	u.mu.Lock()
	var gone [][3]string
	if s.undeliveredLocked(context.Background()) {
		for _, e := range kept {
			m := s.keptLocked(e.key[1], e.key[2])
			if m != nil && m.Machine != e.key[0] {
				m = nil
			}
			if (m == nil && !e.waiting) || (m != nil && m.State != proto.UndeliveredWaiting) {
				gone = append(gone, e.key)
			}
		}
	}
	u.mu.Unlock()
	if len(gone) == 0 {
		return
	}
	b.mu.Lock()
	for _, k := range gone {
		if p, ok := b.pending[k]; ok && p.kept && !p.settling {
			delete(b.pending, k)
			s.logf("task %s: decision: supervisor choice not settled, its kept message did not go in", p.todo)
		}
	}
	b.mu.Unlock()
}

// claimSupervisorSettle claims the live entry for key for the caller to settle, now that its message went in: the
// entry stays in the book, marked settling, and still holds its round's default until settleSupervisorChoice has
// recorded the choice and removed it under the card's action lock. False when nothing waits under key, the entry has
// expired (it goes), or another delivery already claimed it.
func (s *Service) claimSupervisorSettle(key [3]string) (supervisorSettle, bool) {
	b := &s.supervisorSettles
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.pending[key]
	if !ok || p.settling {
		return supervisorSettle{}, false
	}
	if !b.now().Before(p.expires) {
		delete(b.pending, key)
		return supervisorSettle{}, false
	}
	p.settling = true
	b.pending[key] = p
	return p, true
}

// endSupervisorSettle removes the entry its settler claimed; the card's action lock is held.
func (s *Service) endSupervisorSettle(key [3]string) {
	b := &s.supervisorSettles
	b.mu.Lock()
	defer b.mu.Unlock()
	if p, ok := b.pending[key]; ok && p.settling {
		delete(b.pending, key)
	}
}

func keptSupervisorSettleKey(k keptMessage) [3]string {
	return supervisorSettleKey(SessionRef{MachineID: k.Machine, SessionID: k.Session}, k.ID)
}

// claimKeptSupervisorSettleLocked claims the choice waiting on kept message k, which just went in (sent, or found in
// the conversation); s.undelivered.mu is held, so the claim and the kept message's drop are one step for the reap:
// it never finds a kept entry that is not being settled without its kept message.
func (s *Service) claimKeptSupervisorSettleLocked(k keptMessage) (supervisorSettle, bool) {
	return s.claimSupervisorSettle(keptSupervisorSettleKey(k))
}

// settleKeptSupervisorChoice settles a choice claimed for kept message k, which went in; s.undelivered.mu is no
// longer held (the card's action lock comes before it).
func (s *Service) settleKeptSupervisorChoice(ctx context.Context, k keptMessage, p supervisorSettle, ok bool) {
	if ok {
		s.settleSupervisorChoice(context.WithoutCancel(ctx), keptSupervisorSettleKey(k), p)
	}
}

// supervisorSettleOutcome settles a waiting exact choice whose send was delivered. A failed or unknown send leaves
// the card waiting; its entry stays, marked kept, in case the hub keeps the message to send later
// (markSupervisorSettleKept), and goes when the hub does not.
func (s *Service) supervisorSettleOutcome(ctx context.Context, r ConversationSent) {
	if r.ClientMsgID == "" {
		return
	}
	ref := SessionRef{MachineID: r.MachineID, SessionID: r.SessionID}
	if r.Outcome != assistantwire.ConversationDelivered {
		s.markSupervisorSettleKept(ref, r.ClientMsgID, s.supervisorSettles.now().Add(supervisorKeepGrace))
		return
	}
	key := supervisorSettleKey(ref, r.ClientMsgID)
	if p, ok := s.claimSupervisorSettle(key); ok {
		s.settleSupervisorChoice(ctx, key, p)
	}
}
