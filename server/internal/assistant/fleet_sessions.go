package assistant

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// ---- the session lists ----

// setSessions takes a node's session list: the turns it shows starting and ending (the finished push, the
// assistant's own business), then the list the apps get.
func (s *Service) setSessions(machine string, list []proto.Session) {
	list = s.deletionRows(context.Background(), machine, list, false)
	list = slices.DeleteFunc(list, func(row proto.Session) bool { return row.IsDeleted() })
	before, beforeConn := s.fleetPrevious(machine), s.fleetPreviousConn(machine)
	var connected time.Time
	if s.devs != nil {
		connected = s.devs.ConnectedAt(machine)
	}
	// The first list of a new connection after one with a list: a node that started again (an ssh node whose link
	// dropped) can list a turn still running as stopped. Its transcript says whether such a stop ended the turn.
	reconnected := len(before) > 0 && !connected.Equal(beforeConn)
	var unsure []proto.Session
	var inlineUpdates []SessionRef
	var a2aIdleRefs []a2aRef
	fm, hasMachine := s.fleetMachine(machine)
	_, hasInlineStore := s.store().(inlineViewStore)
	now := time.Now()
	for i := range list {
		list[i].MachineID = machine
		// A node from before titles were stripped first sends a clipped injected tag as the title.
		list[i].Title = transcript.Title(list[i].Title)
	}
	for _, sess := range list {
		// Any agent (herdr or headless, started by Persona or not) finishing a turn may notify the phone.
		was, key := before[sess.SessionID], machine+"/"+sess.SessionID
		s.noteUpstreamRow(machine, sess, now) // consecutive upstream 5xx / pool-exhaustion turns (fleet_upstream.go)
		s.availSeen(machine, &sess, was, now) // the owner's wait for a reply (availability.go)
		prev := was.Status
		s.projectReviewSession(s.runCtx(), sess, prev)
		wasWorking := prev == proto.StatusWorking || was.WorkingBeforeUnknown
		reconnectStop := reconnected || was.ReconnectedBeforeUnknown
		if a2aIdle(sess) && (!was.A2AIdle || reconnected) {
			a2aIdleRefs = append(a2aIdleRefs, a2aRef{machine, sess.SessionID})
		}
		// The turn stopped on background work and its report woke it again within one of the node's list ticks: the
		// stop never showed, only the woken flag turning on. It is held work all the same (#4187, #4419). By the
		// background seen during the turn, not only in the last list: the list's background and its woken flag are
		// not always read together.
		if wasWorking && sess.WokeByTask && !was.WokeByTask {
			s.turnsBook().holdIfBackground(key)
		}
		switch {
		case sess.Status == proto.StatusWorking && prev != proto.StatusWorking:
			// A first list or an offline-to-working transition can be a
			// reconnect to the same turn. Only a known stopped turn proves a
			// new one and resets its persisted loop state.
			newTurn := prev == proto.StatusDone || prev == proto.StatusIdle || prev == proto.StatusInterrupted
			s.justMeStarted(machine, sess.SessionID, newTurn, sess.WokeByTask) // meeting_justme.go
			if s.meetingTurnStarted(machine, sess.SessionID, newTurn) {
				break // a meeting's batch: a side exchange, neither the turn tracker's nor a task's or the Agents' (meeting_turns.go)
			}
			s.takeTaskAnswerRetry(machine, sess.SessionID)
			s.suggestDrop(machine, sess.SessionID) // a new turn: the last one's suggestion goes, a call on its way is cancelled
			s.turnsBook().working(key, now)
			s.replyWorked(machine, sess.SessionID) // answered: a question it asks next is a new one (agents_reply.go)
			ref, row := SessionRef{MachineID: machine, SessionID: sess.SessionID}, startRow{Rev: sess.Rev, At: sess.UpdatedAt, NativeLoop: sess.Loop != nil || was.NativeLoop}
			// Every non-working -> working edge except unblocking an interactive blocked turn (StatusBlocked)
			// or recovering from a transient Unknown blip during the same turn (wasWorking) starts a new
			// clarification answer turn, including reaped/stopped/reopened StatusOffline sessions.
			approvalStart := prev != proto.StatusBlocked && !wasWorking
			s.inOrder(func() {
				if approvalStart {
					s.approvalAnswerTurnStarted(ref)
				}
				s.turnStartedObserved(context.Background(), ref, newTurn, row)
			}) // after the items that came before it
		case wasWorking && (sess.Status == proto.StatusDone || sess.Status == proto.StatusIdle) && reconnectStop:
			unsure = append(unsure, sess) // reconnectedTurnEnd, once the list is kept
		case wasWorking && (sess.Status == proto.StatusDone || sess.Status == proto.StatusIdle):
			// After its finished push is decided against the mark; held while its background work runs.
			defer s.justMeEnded(machine, sess.SessionID, backgroundRunning(sess))
			s.turnEndSeen(machine, sess, now)
		case (sess.Status == proto.StatusDone || sess.Status == proto.StatusIdle) &&
			(sess.Status != prev || sess.Rev != was.Rev || sess.LastText != was.LastText || !sess.UpdatedAt.Equal(was.UpdatedAt)) &&
			s.takeTaskAnswerRetry(machine, sess.SessionID):
			ref := SessionRef{MachineID: machine, SessionID: sess.SessionID}
			s.inOrder(func() { s.afterSettle(context.Background(), s.taskTurnSettled, ref.MachineID, ref.SessionID) })
		}
		// A stopped assistant session whose rev or last text advances (for example after an unexported runtime turn
		// flushes to disk with final persisted item ids) registers its latest persona-view blocks without waiting for
		// the full settle window.
		if hasInlineStore && hasMachine &&
			(prev == proto.StatusDone || prev == proto.StatusIdle) &&
			(sess.Status == proto.StatusDone || sess.Status == proto.StatusIdle) &&
			(sess.Rev != was.Rev || sess.LastText != was.LastText) &&
			fm.isAssistant(sess) {
			inlineUpdates = append(inlineUpdates, SessionRef{MachineID: machine, SessionID: sess.SessionID})
		}
		if sess.Status != prev && sess.Status != proto.StatusWorking && !(wasWorking && (sess.Status == proto.StatusDone || sess.Status == proto.StatusIdle)) {
			// Stopped with no end seen (a restart, an interrupt, a card): a meeting turn there is over (meeting_turns.go).
			s.meetingTurnStopped(machine, sess.SessionID, sess, prev == "")
			if id := sess.SessionID; prev == proto.StatusWorking { // by id: a closure over sess would put every row on the heap
				s.inOrder(func() { s.availStopped(machine, id, now) }) // its round is over (availability.go)
			}
		}
		if sess.Status == proto.StatusWorking && backgroundRunning(sess) && !s.meetingTurnNow(machine, sess.SessionID) {
			s.turnsBook().sawBackground(key, now)
		}
	}
	s.availSweep(machine, list, now) // the waits of conversations no longer listed (availability.go)
	s.pruneGoneQuiet(machine)        // the verdicts and Stops of task Agents no longer listed (silent_turns.go)
	s.pruneGoneUpstream(machine, list)
	s.observeIdleSessions(machine, list, connected)
	s.fleetKeep(machine, list, connected, unsure, before, reconnected)
	for _, ref := range a2aIdleRefs {
		s.inOrder(func() { s.a2aRecipientIdle(context.Background(), ref) })
	}
	cfg := s.settings(context.Background())
	mainSession, mainMachine := cfg.Main, cfg.mainRouteMachine()
	for _, sess := range list {
		s.refreshMainConversationAttention(sess, mainSession, mainMachine)
	}
	if len(list) > 0 && (len(before) == 0 || reconnected) {
		go s.recheckMissingReplies(context.Background(), machine, list)
	}
	for _, ref := range inlineUpdates {
		s.inOrder(func() { go s.registerInlineTurn(context.Background(), ref) })
	}
	for _, sess := range unsure {
		go s.reconnectedTurnEnd(machine, sess)
	}
	s.roundsSeen(list)      // whose round each conversation is on (owner_preempt.go)
	s.ownerRoundsSeen(list) // the owner-turn gates end with their rounds (relay_presence.go, #6394)
	s.sessionsChanged(machine)
	s.searchWake() // the chat search index reads what changed (search_index.go)
	s.historyWake()
	// Messages from other conversations, for those no longer running; the todos' links and herdr marks.
	d := DeviceSessions{MachineID: machine, Sessions: list, Connected: connected}
	s.inOrder(func() { s.SessionsChanged(context.Background(), d) })
}

// turnEndSeen is what a working → stopped edge of sess starts: its finished push, the next message's suggestion and
// the turn's end for the conversations and tasks (TurnEnded). The caller ends a "just me" mark after it (justMeEnded).
func (s *Service) turnEndSeen(machine string, sess proto.Session, now time.Time) {
	ref := SessionRef{MachineID: machine, SessionID: sess.SessionID}
	if s.meetingTurnEnded(machine, sess.SessionID) { // no finished push, and the other turns' stay as they were
		s.inOrder(func() { go s.turnEnded(context.Background(), ref, true) })
		return
	}
	ctx := context.Background()
	background := backgroundRunning(sess)
	end := s.endSupervisionTurn(ctx, sess.SessionID, now, background || limitTakenUp(sess, now))
	s.notifyFinished(machine, sess.SessionID, s.turnsBook().ended(machine+"/"+sess.SessionID, now, background), end)
	s.suggestTurnEnded(machine, sess) // the next message, when nothing else suggests it (suggest_next.go)
	// A new assistant conversation is named after its first turn; a task from one may be done. After the items of the
	// turn (a channel's answer is read from them). Advance approval clarification turn state synchronously inside
	// inOrder so a delayed TurnEnded goroutine (for example blocked in A2AReady) cannot race out of order against a
	// subsequent turn-start.
	endCtx := context.WithValue(context.WithValue(ctx, supervisionEndKey{}, end), approvalEndHandledKey{}, true)
	s.inOrder(func() {
		tells := s.approvalAnswerTurnEndedSync(ref)
		go func() {
			for _, ix := range tells {
				s.show(ix)
			}
			s.TurnEnded(endCtx, ref)
		}()
	})
}

// reconnectedTurnEnd is sess's stop in the first list of its node's new connection, taken as the end of its turn only
// once its transcript shows that turn closed (turnEndOf): a node that started again may not know yet that a turn still
// runs. Not closed or not read: no end. The next working → stopped edge is its end then, or the supervision sweep
// takes up one that ended unheard (task_turns.go).
func (s *Service) reconnectedTurnEnd(machine string, sess proto.Session) {
	items, read := s.lastTurnItems(context.Background(), machine, sess.SessionID)
	if _, closed := turnEndOf(items); !read || !closed {
		s.logf("agent %s/%s listed stopped as its node connected again, with no turn end in its transcript: not taken as one", machine, short(sess.SessionID))
		return
	}
	if p, ok := s.fleetPrevious(machine)[sess.SessionID]; !ok || p.Status == proto.StatusWorking {
		return // no longer listed, or working again: that is its turn now
	}
	s.turnEndSeen(machine, sess, time.Now())
	s.justMeEnded(machine, sess.SessionID, backgroundRunning(sess))
}

// turnPrev is what setSessions reads of a session's previous row: the turn's starts and ends are against it. Only
// these fields, not the whole row: a session is about 1.5 KB, and the manager would keep a copy of every list.
type turnPrev struct {
	NativeLoop bool
	A2AIdle    bool
	Status     string
	WokeByTask bool
	Rev        int64
	LastText   string
	UpdatedAt  time.Time
	// WorkingBeforeUnknown remembers that the session was working immediately before one or more transient
	// StatusUnknown polls, so working -> unknown -> done|idle still triggers turnEndSeen without losing the
	// turn-end edge. ReconnectedBeforeUnknown carries whether a reconnect occurred before or during that gap.
	WorkingBeforeUnknown     bool
	ReconnectedBeforeUnknown bool
	// Launcher is what the session runs on: the admission gate's launcher groups count by it (admission.go).
	Launcher string
}

// fleetPrevious is the machine's list setSessions saw last, by session id (the turns' starts and ends are against
// it). The map is never written once kept: fleetKeepPrevious replaces it.
func (s *Service) fleetPrevious(machine string) map[string]turnPrev {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	return s.fleet.prev[machine]
}

// fleetPreviousConn is the node connection the machine's previous list came on (zero: none, or no devices).
func (s *Service) fleetPreviousConn(machine string) time.Time {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	return s.fleet.prevConn[machine]
}

// fleetKeepPrevious keeps what setSessions reads of list for the machine's next one. A session id listed twice keeps
// its later row.
func (s *Service) fleetKeepPrevious(machine string, list []proto.Session) {
	s.fleetKeep(machine, list, time.Time{}, nil, nil, false)
}

// fleetKeep is fleetKeepPrevious for a list that came on the node connection conn. The rows of unsure are kept as
// unknown without preserving prior working state: a stop not taken as the end of a turn, so neither working again
// nor a repeated stopped poll fires a spurious turn start or end.
func (s *Service) fleetKeep(machine string, list []proto.Session, conn time.Time, unsure []proto.Session, before map[string]turnPrev, reconnected bool) {
	next := make(map[string]turnPrev, len(list))
	for i := range list { // by index: a range value copies every (large) session it passes
		sess := &list[i]
		p := turnPrev{Status: sess.Status, NativeLoop: sess.Loop != nil, WokeByTask: sess.WokeByTask, Rev: sess.Rev,
			LastText: sess.LastText, UpdatedAt: sess.UpdatedAt, Launcher: sess.Launcher, A2AIdle: a2aIdle(*sess)}
		if sess.Status == proto.StatusUnknown {
			if was := before[sess.SessionID]; was.Status == proto.StatusWorking || was.WorkingBeforeUnknown {
				p.WorkingBeforeUnknown = true
				p.ReconnectedBeforeUnknown = reconnected || was.ReconnectedBeforeUnknown
			}
		}
		next[sess.SessionID] = p
	}
	for i := range unsure {
		p := next[unsure[i].SessionID]
		p.Status = proto.StatusUnknown
		p.WorkingBeforeUnknown = false
		p.ReconnectedBeforeUnknown = false
		next[unsure[i].SessionID] = p
	}
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.prev == nil {
		s.fleet.prev = map[string]map[string]turnPrev{}
	}
	if s.fleet.prevConn == nil {
		s.fleet.prevConn = map[string]time.Time{}
	}
	s.fleet.prev[machine] = next
	s.fleet.prevConn[machine] = conn
}

func (s *Service) turnsBook() *turnTracker {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.turns == nil {
		s.fleet.turns = newTurnTracker()
	}
	return s.fleet.turns
}

// sessionsChanged: what the apps get of the machine's sessions may have changed: the hub is told, the screen and
// plan cards follow.
func (s *Service) sessionsChanged(machine string) {
	if _, ok := s.fleetMachine(machine); !ok {
		return
	}
	list := s.publishList(machine)
	s.syncScreenInteractions(machine, list)
	s.syncPlanInteractions(machine)
}

// publishSessions tells the hub the machine's session list as the apps get it now.
func (s *Service) publishSessions(machine string) {
	if _, ok := s.fleetMachine(machine); !ok {
		return
	}
	s.publishList(machine)
}

// publishList tells the hub the machine's list as the apps get it now, when it is not what it was told last
// (fleet.sessions); the fleet follows. It returns the list.
//
// The list is decorated and told under sentMu: two publishers (FleetUp's lists, a snapshot's changed machines) would
// otherwise each decorate, then tell in either order, and an older list could land last on the hub and stay there.
func (s *Service) publishList(machine string) []proto.Session {
	list := s.publishListAtArchiveMark(machine)
	s.syncPendingInteractionsToNode(machine)
	return list
}

// publishListAtArchiveMark publishes with a fixed archive read marker when an operation supplies one. Otherwise it
// takes the current marker under sentMu, as publishList always has. A fixed marker keeps a close completion's publish
// from treating a newer archive write as activity visible to that close.
func (s *Service) publishListAtArchiveMark(machine string, fixed ...uint64) []proto.Session {
	f := &s.fleet
	f.sentMu.Lock()
	defer f.sentMu.Unlock()
	// A hub that takes the archived rows apart (#6836) gets the rows the apps see here and the archived ones on their
	// own, when they change: they are the most of a machine that has been used for months, and the hub leaves them
	// out of everything it sends the apps.
	apart := s.hello(context.Background()).ArchivedApart
	mark := s.archiveMark()
	if len(fixed) > 0 {
		mark = fixed[0]
	}
	var list []proto.Session
	var archived archivedPart
	if apart {
		list, archived = s.decoratedApart(machine, mark)
	} else {
		var err error
		list, _, err = s.archivedInventoryAtMark(context.Background(), machine, mark)
		if err != nil {
			s.logf("load agent archives: %v", err)
		}
	}
	// A deleted conversation's row is the assistant's, never an archived one: it goes with the rows the apps see.
	list = s.deletionRows(context.Background(), machine, list, true)
	// A review's reviewer is no agent of the user's (supervise_autoreview.go): its result is on the task it reviewed.
	// Forked background supervisor round branches are also internal and must not flood the ordinary Agents list (#12650).
	hide := s.reviewerSessions()
	if roundForks := s.hiddenBackgroundRoundForks(context.Background(), machine, list); len(roundForks) > 0 {
		if hide == nil {
			hide = roundForks
		} else {
			for k := range roundForks {
				hide[k] = true
			}
		}
	}
	if apart {
		if roundForks := s.hiddenBackgroundRoundForks(context.Background(), machine, archived.rows); len(roundForks) > 0 {
			if hide == nil {
				hide = roundForks
			} else {
				for k := range roundForks {
					hide[k] = true
				}
			}
		}
	}
	reviewWorktrees := map[string]bool{}
	for _, e := range mustSessionReviews(s) {
		if e.MachineID == machine && e.Worktree != "" {
			reviewWorktrees[e.Worktree] = true
		}
	}
	if len(hide) > 0 || len(reviewWorktrees) > 0 {
		kept := make([]proto.Session, 0, len(list))
		for _, sess := range list {
			if !hide[[2]string{machine, sess.SessionID}] && !reviewWorktrees[cmp.Or(sess.WorkspaceCwd, sess.Cwd)] {
				kept = append(kept, sess)
			}
		}
		list = kept
	}
	if apart {
		// Before the list: a session just archived is never in neither of them on the hub.
		s.tellArchived(machine, archived, hide)
	}
	// Whether told now or the same as told last: when its first Just finished row runs out (republishFinished).
	s.noteFinishedDue(machine, finishedDue(list, justFinishedFor(s.triageCfg(context.Background()))))
	s.attachViews(machine, list) // Session.view (fleet_views.go)
	msg := assistantwire.FleetSessions{MachineID: machine, Sessions: list, Muted: s.mutedSessions(machine, list)}
	// json.Marshal's bytes, encoded into the one buffer and compared with the list told last by length and SHA-256: a
	// list that is the same (most of them) is not told again, and none of them is kept (#3197: 3.6 MB at 20 computers
	// of 200 sessions, when each computer's last bytes were).
	f.enc.Reset()
	var b []byte // nil when the list does not encode, as json.Marshal gives
	var told sentList
	if jsonv2.MarshalWrite(&f.enc, msg, json.DefaultOptionsV1()) == nil {
		told = sentList{n: f.enc.Len(), sum: sha256.Sum256(f.enc.Bytes())}
		if told == f.sent[machine] {
			return list
		}
		b = bytes.Clone(f.enc.Bytes()) // enc is the next list's: what is told is never written again
	} else if f.sent[machine].n == 0 {
		return list
	}
	if f.sent == nil {
		f.sent = map[string]sentList{}
	}
	f.sent[machine] = told
	f.shownMu.Lock()
	if f.shown == nil {
		f.shown = map[string][]proto.Session{}
	}
	f.shown[machine] = awaitingReply(list)
	f.shownMu.Unlock()
	// As json.Marshal wrote them (rpc.Marshaled): the bus puts them into its line as they are. As json.RawMessage its
	// encoder checked and copied the whole list twice more to write the same line. Nil (the list does not encode) is
	// told as null either way.
	s.hub.Tell(assistantwire.EventFleetSessions, rpc.Marshaled(b))
	s.liveChanged(false)
	s.todoRunsChanged(machine) // a card, a seen turn: a to-do's view may file it elsewhere (todo_view_push.go)
	return list
}

// decoratedApart is decorated told apart at mark (archiveApplied.apart): the rows the apps see, folded as the whole
// list folds them, and the archived rows.
func (s *Service) decoratedApart(machine string, mark uint64) ([]proto.Session, archivedPart) {
	a, _, err := s.applyArchives(context.Background(), machine, s.decoratedUnarchived(machine), mark, true)
	if err != nil {
		s.logf("load agent archives: %v", err)
	}
	shown, archived := a.apart()
	// The archived rows are not listed in a fold (foldSessions): the visible ones fold as in the whole list,
	// while archived stays in the subagent negative-cache retention set.
	s.foldInventoryWithRetained(machine, shown, archived)
	s.foldSubset(machine, archived.rows)
	return shown, archived
}

// archivedSent is what was told of a machine's archived rows last (fleet.archived): the record set and listed keys
// the archive's own rows came from (archiveTail), the reviewers left out, and the node's archived rows by the length
// and SHA-256 of their JSON. While it is the same, the archive's rows are not made again, let alone told.
type archivedSent struct {
	of     *map[string]agentArchive
	listed string
	hidden string
	n      int
	sum    [sha256.Size]byte
}

// tellArchived tells the hub machine's archived rows (fleet.archived) when they are not what it was told last, the
// reviewers in hide left out as from the list. sentMu is held.
func (s *Service) tellArchived(machine string, part archivedPart, hide map[[2]string]bool) {
	f := &s.fleet
	var hidden []string
	for k := range hide {
		if k[0] == machine {
			hidden = append(hidden, k[1])
		}
	}
	sort.Strings(hidden)
	rows := make([]proto.Session, 0, len(part.rows))
	for _, r := range part.rows {
		if !hide[[2]string{machine, r.SessionID}] {
			rows = append(rows, r)
		}
	}
	b, _ := json.Marshal(rows)
	sent := archivedSent{of: part.of, listed: part.listed, hidden: strings.Join(hidden, "\n"), n: len(b), sum: sha256.Sum256(b)}
	if last, ok := f.archived[machine]; ok && last == sent {
		return
	}
	rows = slices.Grow(rows, len(part.keys))
	for _, key := range part.keys {
		if r := archiveRow(part.records[key]); !hide[[2]string{machine, r.SessionID}] {
			rows = append(rows, r)
		}
	}
	if f.archived == nil {
		f.archived = map[string]archivedSent{}
	}
	f.archived[machine] = sent
	s.attachArchivedViews(machine, rows) // Session.view on the archived rows too (fleet_views.go)
	s.hub.Tell(assistantwire.EventFleetArchived, assistantwire.FleetArchived{MachineID: machine, Sessions: rows})
}

// awaitingReply are the rows of list the reply reminders can pick (replyWaits' first check), copied: only those are read
// of the list told last, and the whole decorated list is several KB a session. Never nil, even when none qualifies:
// a machine told once is not decorated again for the reminders (shownList).
func awaitingReply(list []proto.Session) []proto.Session {
	out := []proto.Session{}
	for i := range list {
		if list[i].AwaitsReply && list[i].State == proto.StateIdle {
			out = append(out, list[i])
		}
	}
	return out
}

// sentList is what is kept of the list last told the hub for a machine: the length and SHA-256 of its JSON. Zero: none,
// or one that did not encode (told as null), so the next list that encodes is told.
type sentList struct {
	n   int
	sum [sha256.Size]byte
}

// decorated returns a machine's sessions as the apps get them: the unified states, the pending interaction counts,
// what the assistant says of its conversations, the work and todos.
func (s *Service) decoratedUnarchived(machine string) []proto.Session {
	return s.decorateInventory(machine, s.nodeSessions(machine), true) // a copy already: the devices' list stays as it is
}

// addHeldRows keeps held messages reachable when their Agent is absent from a complete node list. It only changes
// the displayed inventory: a real listed row always wins, and the placeholder goes when no message is held for it.
func (s *Service) addHeldRows(machine string, list []proto.Session, held map[string][]proto.UndeliveredMessage) []proto.Session {
	f := &s.fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(held) == 0 {
		delete(f.heldRows, machine)
		return list
	}
	previous := f.heldRows[machine]
	next := make(map[string]proto.Session, len(held))
	listed := make(map[string]bool, len(list))
	for i := range list {
		sid := list[i].SessionID
		listed[sid] = true
		if len(held[sid]) > 0 {
			next[sid] = archiveSnapshot(list[i])
		}
	}
	missing := make([]string, 0, len(held))
	for sid, messages := range held {
		if len(messages) > 0 && !listed[sid] {
			missing = append(missing, sid)
		}
	}
	sort.Strings(missing)
	for _, sid := range missing {
		if row, ok := previous[sid]; ok {
			next[sid] = row
			list = append(list, archiveSnapshot(row)) // a fresh copy: decoration must not change the cache
		} else {
			list = append(list, proto.Session{MachineID: machine, SessionID: sid, Status: proto.StatusOffline})
		}
	}
	if len(next) == 0 {
		delete(f.heldRows, machine)
	} else {
		if f.heldRows == nil {
			f.heldRows = map[string]map[string]proto.Session{}
		}
		f.heldRows[machine] = next
	}
	return list
}

// decorateInventory owns list. A subset gets the same live fields as the full
// inventory, but must not forget state marks belonging to rows it did not read.
func (s *Service) decorateInventory(machine string, list []proto.Session, complete bool) []proto.Session {
	m, _ := s.fleetMachine(machine)
	r := &s.fleet.rep
	r.mu.RLock()
	profile, shown, via := r.profile, r.sessions, r.marks
	r.mu.RUnlock()
	var mainSession, mainMachine string
	if profile != nil {
		mainSession, mainMachine = profile.Main, cmp.Or(profile.MainMachineID, profile.MachineID)
	} else {
		mainSession, mainMachine = s.mainConversationRoute()
	}
	s.fleet.mu.Lock()
	counts := map[string]int{}
	asking := map[string]proto.Interaction{} // a session's card: its own before the manager's
	for _, p := range s.fleet.interactions {
		if p.ix.MachineID != machine || machine == mainMachine && p.ix.SessionID == mainSession && !directUserInteraction(p.ix) {
			continue
		}
		counts[p.ix.SessionID]++
		if cur, ok := asking[p.ix.SessionID]; !ok || cur.ToolName == assistantwire.WorkCardTool {
			asking[p.ix.SessionID] = p.ix
		}
	}
	seen := make(map[string]time.Time, min(len(list), len(s.fleet.seen)))
	for _, sess := range list {
		if t, ok := s.fleet.seen[[2]string{machine, sess.SessionID}]; ok {
			seen[sess.SessionID] = t
		}
	}
	s.fleet.mu.Unlock()
	lang := s.lang(context.Background())
	// The owner's messages to Agents held while their computer is away (internal/devices held.go, #10661): once for
	// the whole list, worded as the rows are.
	var held map[string][]proto.UndeliveredMessage
	if s.devs != nil {
		held = s.devs.HeldMessages(machine, lang)
	}
	if complete {
		list = s.addHeldRows(machine, list, held)
	}
	// A done turn nobody has seen is Just finished when it ended after this (#9751).
	finishedSince := time.Now().Add(-justFinishedFor(s.triageCfg(context.Background())))
	dir := m.assistantCwd() // once, not a path.Join for each row
	asks := s.fleet.asks.reader(machine, complete)
	defer asks.done()
	suggested := s.hubSuggestions(machine)
	quiet := s.quietMarks(machine)       // turns of task Agents that had no output (silent_turns.go)
	upstream := s.upstreamMarks(machine) // consecutive no-work upstream 5xx turns (fleet_upstream.go)
	bgMatch := s.backgroundMatcher(context.Background(), machine)
	for i := range list {
		list[i].MachineID = machine
		list[i].PendingInteractions = counts[list[i].SessionID]
		if !list[i].LogicalPlaceholder {
			list[i].Assistant = isAssistantSession(&list[i], dir, m.manager)
		}
		if !list[i].Assistant && bgMatch.matches(&list[i]) {
			list[i].Assistant = true
		}
		if list[i].Assistant && list[i].Space == "" {
			if list[i].LogicalPlaceholder {
				if sp, known := s.logicalRowSpace(context.Background(), list[i]); known {
					list[i].Space = sp
				}
			} else {
				list[i].Space = identity.SpaceOf(list[i].Cwd)
			}
		}
		if t := shown[list[i].SessionID].Title; t != "" && list[i].Assistant {
			list[i].Title = t // "MMDD | type | topic", the user's own, "Persona" for the main one (titles.go)
		}
		s.nameAgentRow(machine, &list[i])
		if ch := via[[2]string{machine, list[i].SessionID}]; len(ch) > 0 {
			list[i].Channels = ch // where its messages came from (docs/extensions/channels.md "Source markers")
		}
		if list[i].Assistant {
			list[i].Archived = shown[list[i].SessionID].Archived
			list[i].Tags = rowTagsOf(shown[list[i].SessionID].Tags)
			v := shown[list[i].SessionID]
			list[i].Origin, list[i].OriginRef, list[i].OpenedBy = v.Origin, v.OriginRef, v.OpenedBy // origins.go
			list[i].ModelPick = v.ModelPick                                                         // what its next messages run on (model_pick.go)
			// Messages from other conversations wait in the assistant while it runs: shown like typed-ahead input, after
			// the user's own (the node's), and counted so the apps leave them alone: the assistant delivers them when the
			// turn ends, and the queue's actions are claude's (#4342).
			if q := shown[list[i].SessionID].Queued; len(q) > 0 {
				list[i].Queued = append(slices.Clip(list[i].Queued), q...) // the node's slice is shared: never write into it
				list[i].QueuedRelayed = uint16(min(len(q), math.MaxUint16))
			}
			if u := shown[list[i].SessionID].Undelivered; len(u) > 0 {
				kept := slices.Clone(u) // the owner's messages that could not go in (undelivered.go, #10194)
				list[i].Undelivered = &kept
			}
		} else if u := held[list[i].SessionID]; len(u) > 0 {
			kept := u // an Agent's, fresh from HeldMessages; its own variable, so only a row with some is allocated
			list[i].Undelivered = &kept
		}
		list[i].SeenAt = seen[list[i].SessionID]
		localizeSession(lang, &list[i]) // the node's Chinese activity line… (fleet_text.go)
		if list[i].StartErrorCode != "" {
			list[i].StartError = startErrorText(lang, cmp.Or(m.Name, machine), list[i])
		}
		list[i].State, list[i].BackgroundWorking = stateWithBackground(agentState(list[i], list[i].SeenAt), list[i])
		list[i].State = logicalDeliveryState(list[i]) // never idle while its input has not gone in (logical_undelivered.go)
		list[i].BackgroundWaiting = backgroundWaiting(list[i])
		if !list[i].BackgroundWorking && !stalledAfterTurn(list[i]) || backgroundIsWork(list[i]) {
			list[i].BackgroundStalledSince = 0 // only background commands that are all there is to the work can have stalled
		}
		list[i].TurnError, list[i].TurnErrorKind = turnError(list[i])
		// A task Agent's turn with no output: interrupted or stopped, not done (silent_turns.go, #10187).
		putQuietTurn(&list[i], quiet, lang)
		decorateLimit(&list[i]) // a subscription's usage limit and when it resets (usage_limit.go)
		list[i].TurnErrorCode = sessionTurnErrorCode(list[i])
		putUpstreamTurn(&list[i], upstream)
		if list[i].TurnError != "" {
			if proto.UnrecoverableTurnErrorCode(list[i].TurnError) != "" {
				list[i].TurnErrorKind = proto.TurnErrorUnrecoverable
			}
			if list[i].TurnErrorCode == proto.TurnErrorModelFallbackUnavailable {
				list[i].TurnErrorKind = proto.TurnErrorUser
			}
			// Not worth a "Continue": the user signs in, fixes the key or the model. Only where agents_work would not retry it
			// anyway: a gateway's 5xx that mentions the model stays transient.
			if list[i].TurnErrorKind == "" && userTurnError(list[i].TurnErrorCode) && !transientError(list[i].TurnError) {
				list[i].TurnErrorKind = proto.TurnErrorUser
			}
		}
		s.decorateWork(machine, &list[i], asking, finishedSince)
		decorateAwaits(&list[i], asks)
		if list[i].SessionID == mainSession && list[i].MachineID == mainMachine && mainConversationAttentionCandidate(list[i].Assistant, list[i].Status, list[i].State, list[i].Starting, list[i].PendingInteractions) {
			s.applyMainConversationAttention(&list[i], mainSession, mainMachine)
		}
		decorateSuggestion(&list[i], suggested)
	}
	s.decorateSubagents(context.Background(), machine, list)
	s.decorateTodos(machine, list)
	for i := range list {
		// The todos' alone, stripped before push to client (#762).
		list[i].SpawnFrom, list[i].Chat, list[i].Topic, list[i].Dispatched = "", "", "", nil
	}
	s.fillStateMarks(machine, list, time.Now(), complete)
	if !complete {
		s.foldSubset(machine, list) // a complete list is folded once its archive is applied (archivedInventory)
	}
	return s.deletionRows(context.Background(), machine, list, false)
}

// decoratedSession is one of the machine's sessions as the apps get it. Each row is decorated on its own, so the one
// the node lists is decorated alone (listedSession, as readinessMain does), not the machine's whole list for each
// finished turn; one the node does not list may still be an archived row (archivedSessions), read from its record
// alone (archivedRow): the records only grow, and the whole list carries every one of them.
func (s *Service) decoratedSession(machine, sid string) (proto.Session, bool) {
	if row, ok, err := s.deletedConversation(context.Background(), machine, sid); err == nil && ok {
		return s.deletedRow(context.Background(), row), true
	}
	if row, ok := s.listedSession(machine, sid); ok {
		return row, true
	}
	row, ok, err := s.archivedRow(context.Background(), machine, sid)
	if err != nil {
		s.logf("load agent archives: %v", err)
	}
	if !ok {
		return proto.Session{}, false
	}
	row.SetHiddenByDirectory(s.hiddenByDirectory(machine, row))
	one := []proto.Session{row}
	s.decorateSubagents(context.Background(), machine, one)
	s.foldSubset(machine, one) // what the last complete list folded it as, as for a row the node lists
	return one[0], true
}

// listedSession is decoratedSession for a row machine's node lists: false for one only the archive keeps.
func (s *Service) listedSession(machine, sid string) (proto.Session, bool) {
	mark := s.archiveMark()
	row, ok := s.nodeSession(machine, sid)
	if !ok {
		return proto.Session{}, false
	}
	list, err := s.archivedListed(context.Background(), machine, s.decorateInventory(machine, []proto.Session{row}, false), mark)
	if err != nil {
		s.logf("load agent archives: %v", err)
	}
	if len(list) == 0 {
		return proto.Session{}, false // an unreadable deletion ledger hides assistant rows rather than exposing them
	}
	row = list[0]
	row.SetHiddenByDirectory(s.hiddenByDirectory(machine, row))
	return row, true
}

// listedSessions are the sessions machine's node lists, each as the apps get it (listedSession, for the whole list,
// folded as the last complete list was): sessions without the rows only the archive keeps, for a reader of what runs
// on a machine. Those rows are offline, and every record of the machine is one (they only grow). Without the fleet
// in this process it is sessions, archive rows and all.
func (s *Service) listedSessions(ctx context.Context, machine string) ([]proto.Session, error) {
	if !s.fleetOn() || machine == DeviceAll {
		return s.sessions(ctx, machine)
	}
	m, ok := s.fleetMachine(machine)
	if !ok {
		return nil, ErrNoDevice // as sessions.list says
	}
	mark := s.archiveMark()
	list, err := s.archivedListed(context.Background(), m.ID, s.decorateInventory(m.ID, s.nodeSessions(m.ID), true), mark)
	if err != nil {
		s.logf("load agent archives: %v", err)
	}
	for i := range list {
		list[i].SetHiddenByDirectory(s.hiddenByDirectory(m.ID, list[i]))
	}
	s.foldSubset(m.ID, list) // never stored: the fold is the complete list's, archive rows and all (foldInventory)
	return list, nil
}

// decorateTodos keeps active work and completed ownership separate, with quiet still decided by its own report.
func (s *Service) decorateTodos(mid string, list []proto.Session) {
	r := &s.fleet.rep
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range list {
		key := [2]string{mid, list[i].SessionID}
		list[i].Todo = nil
		list[i].SetCompletedTodo(nil)
		if l, ok := r.links[key]; ok {
			list[i].Todo = &proto.TodoRef{ID: l.TodoID, Title: l.Title}
		} else if l, ok := r.completed[key]; ok {
			list[i].SetCompletedTodo(&proto.TodoRef{ID: l.TodoID, Title: l.Title, Cancelled: l.Cancelled})
		}
		list[i].Quiet = r.quiet[key]
	}
}
