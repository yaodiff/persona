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
	before := s.fleetPrevious(machine)
	now := time.Now()
	for i := range list {
		list[i].MachineID = machine
		// A node from before titles were stripped first sends a clipped injected tag as the title.
		list[i].Title = transcript.Title(list[i].Title)
	}
	for _, sess := range list {
		// Any agent (herdr or headless, started by Persona or not) finishing a turn may notify the phone.
		was, key := before[sess.SessionID], machine+"/"+sess.SessionID
		s.availSeen(machine, &sess, was, now) // the owner's wait for a reply (availability.go)
		prev := was.Status
		// The turn stopped on background work and its report woke it again within one of the node's list ticks: the
		// stop never showed, only the woken flag turning on. It is held work all the same (#4187, #4419). By the
		// background seen during the turn, not only in the last list: the list's background and its woken flag are
		// not always read together.
		if prev == proto.StatusWorking && sess.WokeByTask && !was.WokeByTask {
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
			ref := SessionRef{MachineID: machine, SessionID: sess.SessionID}
			s.inOrder(func() { s.turnStartedObserved(context.Background(), ref, newTurn) }) // after the items that came before it
		case prev == proto.StatusWorking && (sess.Status == proto.StatusDone || sess.Status == proto.StatusIdle):
			ref := SessionRef{MachineID: machine, SessionID: sess.SessionID}
			// After its finished push is decided against the mark; held while its background work runs.
			defer s.justMeEnded(machine, sess.SessionID, backgroundRunning(sess))
			if s.meetingTurnEnded(machine, sess.SessionID) { // no finished push, and the other turns' stay as they were
				s.inOrder(func() { go s.turnEnded(context.Background(), ref, true) })
				break
			}
			s.notifyFinished(machine, sess.SessionID, s.turnsBook().ended(key, now, backgroundRunning(sess)))
			s.suggestTurnEnded(machine, sess) // the next message, when nothing else suggests it (suggest_next.go)
			// A new assistant conversation is named after its first turn; a task from one may be done. After the
			// items of the turn (a channel's answer is read from them).
			s.inOrder(func() { go s.TurnEnded(context.Background(), ref) })
		case (sess.Status == proto.StatusDone || sess.Status == proto.StatusIdle) &&
			(sess.Status != prev || sess.Rev != was.Rev || sess.LastText != was.LastText || !sess.UpdatedAt.Equal(was.UpdatedAt)) &&
			s.takeTaskAnswerRetry(machine, sess.SessionID):
			ref := SessionRef{MachineID: machine, SessionID: sess.SessionID}
			s.inOrder(func() { s.afterSettle(context.Background(), s.taskTurnSettled, ref.MachineID, ref.SessionID) })
		}
		if sess.Status != prev && sess.Status != proto.StatusWorking && !(prev == proto.StatusWorking && (sess.Status == proto.StatusDone || sess.Status == proto.StatusIdle)) {
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
	s.fleetKeepPrevious(machine, list)
	s.roundsSeen(list)      // whose round each conversation is on (owner_preempt.go)
	s.ownerRoundsSeen(list) // the owner-turn gates end with their rounds (relay_presence.go, #6394)
	s.sessionsChanged(machine)
	s.searchWake() // the chat search index reads what changed (search_index.go)
	// Messages from other conversations, for those no longer running; the todos' links and herdr marks.
	var connected time.Time
	if s.devs != nil {
		connected = s.devs.ConnectedAt(machine)
	}
	d := DeviceSessions{MachineID: machine, Sessions: list, Connected: connected}
	s.inOrder(func() { s.SessionsChanged(context.Background(), d) })
}

// turnPrev is what setSessions reads of a session's previous row: the turn's starts and ends are against it. Only
// these fields, not the whole row: a session is about 1.5 KB, and the manager would keep a copy of every list.
type turnPrev struct {
	Status     string
	WokeByTask bool
	Rev        int64
	LastText   string
	UpdatedAt  time.Time
}

// fleetPrevious is the machine's list setSessions saw last, by session id (the turns' starts and ends are against
// it). The map is never written once kept: fleetKeepPrevious replaces it.
func (s *Service) fleetPrevious(machine string) map[string]turnPrev {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	return s.fleet.prev[machine]
}

// fleetKeepPrevious keeps what setSessions reads of list for the machine's next one. A session id listed twice keeps
// its later row.
func (s *Service) fleetKeepPrevious(machine string, list []proto.Session) {
	next := make(map[string]turnPrev, len(list))
	for i := range list { // by index: a range value copies every (large) session it passes
		sess := &list[i]
		next[sess.SessionID] = turnPrev{Status: sess.Status, WokeByTask: sess.WokeByTask, Rev: sess.Rev,
			LastText: sess.LastText, UpdatedAt: sess.UpdatedAt}
	}
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.prev == nil {
		s.fleet.prev = map[string]map[string]turnPrev{}
	}
	s.fleet.prev[machine] = next
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
	f := &s.fleet
	f.sentMu.Lock()
	defer f.sentMu.Unlock()
	list := s.decorated(machine)
	// A review's reviewer is no agent of the user's (supervise_autoreview.go): its result is on the task it reviewed.
	if hide := s.reviewerSessions(); len(hide) > 0 {
		kept := make([]proto.Session, 0, len(list))
		for _, sess := range list {
			if !hide[[2]string{machine, sess.SessionID}] {
				kept = append(kept, sess)
			}
		}
		list = kept
	}
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
	return list
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

// decorateInventory owns list. A subset gets the same live fields as the full
// inventory, but must not forget state marks belonging to rows it did not read.
func (s *Service) decorateInventory(machine string, list []proto.Session, complete bool) []proto.Session {
	m, _ := s.fleetMachine(machine)
	s.fleet.mu.Lock()
	counts := map[string]int{}
	asking := map[string]proto.Interaction{} // a session's card: its own before the manager's
	for _, p := range s.fleet.interactions {
		if p.ix.MachineID == machine {
			counts[p.ix.SessionID]++
			if cur, ok := asking[p.ix.SessionID]; !ok || cur.ToolName == assistantwire.WorkCardTool {
				asking[p.ix.SessionID] = p.ix
			}
		}
	}
	seen := make(map[string]time.Time, min(len(list), len(s.fleet.seen)))
	for _, sess := range list {
		if t, ok := s.fleet.seen[[2]string{machine, sess.SessionID}]; ok {
			seen[sess.SessionID] = t
		}
	}
	s.fleet.mu.Unlock()
	r := &s.fleet.rep
	r.mu.RLock()
	shown, via := r.sessions, r.marks
	r.mu.RUnlock()
	lang := s.lang(context.Background())
	dir := m.assistantCwd() // once, not a path.Join for each row
	asks := s.fleet.asks.reader(machine, complete)
	defer asks.done()
	suggested := s.hubSuggestions(machine)
	for i := range list {
		list[i].MachineID = machine
		list[i].PendingInteractions = counts[list[i].SessionID]
		list[i].Assistant = isAssistantSession(&list[i], dir, m.manager)
		if list[i].Assistant {
			list[i].Space = identity.SpaceOf(list[i].Cwd)
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
		}
		list[i].SeenAt = seen[list[i].SessionID]
		localizeSession(lang, &list[i]) // the node's Chinese activity line… (fleet_text.go)
		if list[i].StartErrorCode != "" {
			list[i].StartError = startErrorText(lang, cmp.Or(m.Name, machine), list[i])
		}
		list[i].State, list[i].BackgroundWorking = stateWithBackground(agentState(list[i], list[i].SeenAt), list[i])
		list[i].BackgroundWaiting = backgroundWaiting(list[i])
		list[i].TurnError, list[i].TurnErrorKind = turnError(list[i])
		decorateLimit(&list[i]) // a subscription's usage limit and when it resets (usage_limit.go)
		list[i].TurnErrorCode = sessionTurnErrorCode(list[i])
		if list[i].TurnError != "" {
			if list[i].TurnErrorCode == proto.TurnErrorModelFallbackUnavailable {
				list[i].TurnErrorKind = proto.TurnErrorUser
			}
			// Not worth a "Continue": the user signs in, fixes the key or the model. Only where agents_work would not retry it
			// anyway: a gateway's 5xx that mentions the model stays transient.
			if list[i].TurnErrorKind == "" && userTurnError(list[i].TurnErrorCode) && !transientError(list[i].TurnError) {
				list[i].TurnErrorKind = proto.TurnErrorUser
			}
		}
		s.decorateWork(machine, &list[i], asking)
		decorateAwaits(&list[i], asks)
		decorateSuggestion(&list[i], suggested)
	}
	s.decorateTodos(machine, list)
	for i := range list {
		// The todos' alone, stripped before push to client (#762).
		list[i].SpawnFrom, list[i].Chat, list[i].Topic, list[i].Dispatched = "", "", "", nil
	}
	s.fillStateMarks(machine, list, time.Now(), complete)
	if !complete {
		s.foldSubset(machine, list) // a complete list is folded once its archive is applied (archivedInventory)
	}
	return list
}

// decoratedSession is one of the machine's sessions as the apps get it. Each row is decorated on its own, so the one
// the node lists is decorated alone (listedSession, as readinessMain does), not the machine's whole list for each
// finished turn; one the node does not list may still be an archived row (archivedSessions), read from its record
// alone (archivedRow): the records only grow, and the whole list carries every one of them.
func (s *Service) decoratedSession(machine, sid string) (proto.Session, bool) {
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
	one := []proto.Session{row}
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
	return list[0], true
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
			list[i].SetCompletedTodo(&proto.TodoRef{ID: l.TodoID, Title: l.Title})
		}
		list[i].Quiet = r.quiet[key]
	}
}
