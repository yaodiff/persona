package assistant

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Open tasks nobody works on (docs/assistant/todos.md "Nobody is working on this", #3618). An open to-do keeps
// reading as in progress after its agents are gone: one ended on a model error nothing retries and its tab was closed,
// the user closed the tab, or its computer went offline. When every agent of an open to-do is closed or offline and
// the manager has nothing pending that brings one back (a wake, a "Continue", a card, queued input: session_work), the
// to-do is orphaned, with why. GET /api/v1/todos carries it (`orphaned`), the snapshot's task line says it, and a
// supervised task's supervisor is told once (the signal `orphaned`, supervise_signals.go).
//
// A start still waiting for a computer or workspace can also be orphaned after a grace period.
// Not orphaned: a done to-do; one with no agent or queued start; a
// supervised task waiting for its review or for the user's decision (it waits on them, not on an agent); one with an
// agent that is an assistant conversation (a message always reaches it).

// Why an open to-do has nobody working on it (TodoOrphan.Reason; stable: clients branch on them).
const (
	orphanError   = "error"   // its agent ended on a model error nothing retries, and is closed
	orphanClosed  = "closed"  // its agent's tab was closed (by the user, or for being idle) and nothing brings it back
	orphanOffline = "offline" // its agent's computer is not connected
	orphanQueued  = "queued"  // no agent has started and placement or a workspace remains queued
)

const orphanQueuedAfter = 10 * time.Minute

// orphanOfflineAfter is how long a computer must have been away before its tasks count as orphaned: a dropped link or
// a restart of its node comes back within minutes.
var orphanOfflineAfter = 10 * time.Minute

// orphanReconnectWait is how long after its node connected again a computer's agents are not taken as gone: a node
// that started over (an ssh node whose link dropped) lists its agents again only once it has found them.
var orphanReconnectWait = 2 * time.Minute

// TodoOrphan says an open to-do has nobody working on it: why, and since when the manager has seen it so.
type TodoOrphan struct {
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
	// What the supervisor's signal needs: the stretch of state it is about (key), the agent it names and its error.
	key              string
	machine, session string
	detail           string
	workerSet        string
}

// orphanAgent is one agent of a to-do as the pass found it: whether its computer answers, whether that computer lists
// the session, the session, and when its computer's node connected (zero: not known).
type orphanAgent struct {
	machine, session  string
	reachable, listed bool
	c                 proto.Session
	connected         time.Time
}

// A stopped read-only node session may retain the name of its host after its
// foreground Agent disappears. A live pane or an unsupported status is not
// evidence that the Agent stopped.
func orphanSessionClosed(c proto.Session) bool {
	return sessionClosed(c) || c.PaneID == "" && !c.Starting &&
		(c.Status == proto.StatusInterrupted || c.Status == proto.StatusOffline) &&
		c.Capabilities != nil && c.Capabilities.Read && c.Capabilities.Terminal && !c.Capabilities.Send
}

// agentGone says why a is not working and will not by itself ("": it works, or something pending brings it back).
func agentGone(a orphanAgent, now time.Time) string {
	if !a.reachable {
		return orphanOffline
	}
	if !a.connected.IsZero() && now.Sub(a.connected) < orphanReconnectWait {
		return "" // its node connected again a moment ago: it may not list the agent as it is yet
	}
	if !a.listed {
		return orphanClosed // its computer no longer lists it: nothing runs it
	}
	c := a.c
	switch {
	case c.Assistant || c.Starting:
		return ""
	case !orphanSessionClosed(c):
		return "" // in a host (stopped or working, the task's other states say which), or working
	case c.State == proto.StateNeedsYou || c.PendingInteractions > 0 || len(c.Queued) > 0:
		return "" // a card waits for the user, or input waits to go in
	case wakePending(c) || usageLimitWaiting(c, now):
		return "" // the manager wakes or continues it
	case c.TurnError != "" && !proto.SilentTurnKind(c.TurnErrorKind):
		return orphanError // not the hub's word on a turn with no output, which no error ended (silent_turns.go)
	}
	return orphanClosed
}

// taskOrphan says whether open to-do t, with its active agents as found, has nobody working on it, and why: the
// error when an agent ended on one, else the computer being away when one's is, else closed.
func taskOrphan(t Todo, agents []orphanAgent, now time.Time) (TodoOrphan, bool) {
	if t.DoneAt != nil || len(agents) == 0 {
		return TodoOrphan{}, false
	}
	if orphanWaitsOnDecision(t) {
		return TodoOrphan{}, false // a parked or timed-out task waits on purpose: nobody is meant to work on it
	}
	rank := map[string]int{orphanError: 3, orphanOffline: 2, orphanClosed: 1}
	var o TodoOrphan
	var ids []string
	var moved time.Time
	for _, a := range agents {
		why := agentGone(a, now)
		if why == "" {
			return TodoOrphan{}, false
		}
		ids = append(ids, a.machine+"/"+a.session)
		if a.listed && a.c.UpdatedAt.After(moved) {
			moved = a.c.UpdatedAt
		}
		if rank[why] > rank[o.Reason] {
			o.Reason, o.machine, o.session, o.detail = why, a.machine, a.session, ""
			if why == orphanError {
				o.detail = clip(oneLine(a.c.TurnError), 160)
			}
		}
	}
	// One stretch: the same agents, none of which moved since. Why they are gone may change within it (a closed
	// agent's computer going to sleep), which is not news to the supervisor.
	slices.Sort(ids)
	o.key = "orphaned:" + strings.Join(ids, ",") + "@" + moved.UTC().Format(time.RFC3339)
	return o, true
}

const (
	orphanActivityGrace = 2 * time.Minute  // keep treating a closed-tab session as active while transcript entries advance
	orphanFreshActivity = 45 * time.Second // recent rollout activity before the first pass (#12389)
)

type orphanProgress struct {
	rev         int64
	updatedAt   time.Time
	lastMovedAt time.Time
	closed      bool
}

// orphanBook is what the pass found last: the orphaned to-dos by id, and since when each candidate was seen.
type orphanBook struct {
	mu       sync.Mutex
	now      map[string]TodoOrphan
	seen     map[string]time.Time
	progress map[string]map[[2]string]orphanProgress
}

// orphanOf is to-do id's orphan state as the last pass found it.
func (s *Service) orphanOf(id string) (TodoOrphan, bool) {
	b := &s.orphans
	b.mu.Lock()
	defer b.mu.Unlock()
	o, ok := b.now[id]
	return o, ok
}

// orphanAgents are open to-do t's active agents as their computers show them now; known is false when a computer that
// is connected could not be asked, so nothing can be said of t this time.
func (s *Service) orphanAgents(ctx context.Context, t Todo, reachable map[string]bool, lists map[string]map[string]proto.Session, failed map[string]bool) (agents []orphanAgent, known bool) {
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		oa := orphanAgent{machine: a.MachineID, session: a.SessionID, reachable: reachable[a.MachineID]}
		if oa.reachable {
			if s.devs != nil {
				oa.connected = s.devs.ConnectedAt(a.MachineID)
			}
			if _, asked := lists[a.MachineID]; !asked && !failed[a.MachineID] {
				got, err := s.sessions(ctx, a.MachineID)
				if err != nil {
					failed[a.MachineID] = true
				} else {
					list := make(map[string]proto.Session, len(got))
					for _, c := range got {
						list[c.SessionID] = c
					}
					lists[a.MachineID] = list
				}
			}
			if failed[a.MachineID] {
				return nil, false
			}
			oa.c, oa.listed = lists[a.MachineID][a.SessionID]
		}
		agents = append(agents, oa)
	}
	return agents, true
}

// workerCompletion is derived from stored child rows. Reopening and accepting a
// child again produces a new generation. Failed, cancelled or unreviewed rows
// cannot stand in for an accepted result.
type workerCompletion struct {
	accepted   []string
	incomplete bool
}

func completedWorkerSets(todos map[string]*Todo) map[string]workerCompletion {
	groups := map[string]workerCompletion{}
	for _, t := range todos {
		parent := t.PartOf
		if parent == "" {
			for _, a := range t.Agents {
				if a.Source == sourceWorker && a.Via != "" {
					parent = a.Via
					break
				}
			}
		}
		if parent == "" || todos[parent] == nil || todos[parent].DoneAt != nil {
			continue
		}
		group := groups[parent]
		if t.DoneAt == nil || t.StartError != "" || t.Supervision.state(*t) != reviewAccepted {
			group.incomplete = true
		} else {
			group.accepted = append(group.accepted, t.ID+"@"+t.DoneAt.UTC().Format(time.RFC3339Nano))
		}
		groups[parent] = group
	}
	return groups
}

func (w workerCompletion) key() string {
	if w.incomplete || len(w.accepted) == 0 {
		return ""
	}
	slices.Sort(w.accepted)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(w.accepted, "|"))))
}

// orphanWatch looks at the open to-dos' agents and records which to-dos nobody works on (taskOrphan). A to-do one of
// whose computers could not be asked keeps what was found before. The apps are told to read the to-dos again when
// what they show changed.
func (s *Service) orphanWatch(ctx context.Context, now time.Time) {
	if !s.todosLoaded() {
		return
	}
	s.todos.mu.RLock()
	workers := completedWorkerSets(s.todos.todos)
	var open []Todo
	for _, t := range s.todos.todos {
		if t.DoneAt == nil && (len(t.Agents) > 0 || t.Origin != "") {
			open = append(open, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()

	queued := map[string]TodoOrphan{}
	if len(open) > 0 {
		for _, p := range s.queueCandidatePlans(ctx) {
			if p.State != planStarted {
				continue
			}
			for _, t := range p.Tasks {
				if q, ok := queuedTaskOrphan(p.ID, t, now); ok {
					queued[t.TodoID] = q
				}
			}
		}
	}
	reachable := map[string]bool{}
	if len(open) > 0 {
		devices := s.devices(ctx)
		for _, d := range devices {
			reachable[d.ID] = d.Reachable
		}
	}
	lists, failed := map[string]map[string]proto.Session{}, map[string]bool{}
	found, unknown := map[string]TodoOrphan{}, map[string]bool{}
	listed := map[string][]orphanAgent{}
	for _, t := range open {
		if len(reachable) == 0 && slices.ContainsFunc(t.Agents, func(a TodoAgent) bool { return a.active() }) {
			unknown[t.ID] = true // an unavailable inventory says nothing about linked agents
			continue
		}
		agents, known := s.orphanAgents(ctx, t, reachable, lists, failed)
		if !known {
			unknown[t.ID] = true
			continue
		}
		listed[t.ID] = agents
		o, orphaned := taskOrphan(t, agents, now)
		if q, ok := queued[t.ID]; ok && len(agents) == 0 &&
			!queuedStartWaits(t) && !s.taskStartOnWay(t.ID) {
			found[t.ID] = q
		} else if orphaned {
			if group := workers[t.ID]; !group.incomplete && len(group.accepted) > 0 && slices.ContainsFunc(t.Agents, func(a TodoAgent) bool {
				return a.active() && a.Source == sourceLeader
			}) {
				o.workerSet = group.key()
				o.key += "|workers:" + o.workerSet
				o.detail = strings.TrimSpace(o.detail + " " + i18n.T(s.botLang(ctx), "assistant-tasks.workers_accepted", i18n.Args{"count": len(group.accepted)}))
			}
			found[t.ID] = o
		}
	}

	b := &s.orphans
	b.mu.Lock()
	next, seen := map[string]TodoOrphan{}, map[string]time.Time{}
	progress := map[string]map[[2]string]orphanProgress{}
	for id := range unknown { // not asked: as before
		if at, ok := b.seen[id]; ok {
			seen[id] = at
		}
		if o, ok := b.now[id]; ok {
			next[id] = o
		}
		if p, ok := b.progress[id]; ok {
			progress[id] = p
		}
	}
	for id, agents := range listed {
		cur := map[[2]string]orphanProgress{}
		active := false
		prevTodo := b.progress[id]
		for _, a := range agents {
			if !a.listed || !orphanSessionClosed(a.c) {
				continue
			}
			k := [2]string{a.machine, a.session}
			prev, had := prevTodo[k]
			movedAt := prev.lastMovedAt
			if had && prev.closed && (a.c.Rev > prev.rev || a.c.UpdatedAt.After(prev.updatedAt)) {
				movedAt = now
			} else if !had && a.c.TurnError == "" && (a.c.Capabilities == nil || a.c.Capabilities.StopCause == "") &&
				!a.c.UpdatedAt.IsZero() && now.Sub(a.c.UpdatedAt) >= 0 && now.Sub(a.c.UpdatedAt) < orphanFreshActivity {
				movedAt = a.c.UpdatedAt
			}
			if !movedAt.IsZero() && now.Sub(movedAt) < orphanActivityGrace {
				active = true
			}
			cur[k] = orphanProgress{rev: a.c.Rev, updatedAt: a.c.UpdatedAt, lastMovedAt: movedAt, closed: true}
		}
		if len(cur) > 0 {
			progress[id] = cur
		}
		if active {
			if o, ok := found[id]; ok && o.Reason == orphanClosed {
				delete(found, id)
			}
		}
	}
	for id, o := range found {
		since, ok := b.seen[id]
		if !ok {
			since = now
		}
		seen[id] = since
		prev, was := b.now[id]
		if o.Reason == orphanOffline && !was && now.Sub(since) < orphanOfflineAfter {
			continue // its computer may be back in a moment
		}
		if o.Since.IsZero() {
			o.Since = since.UTC()
			if was && prev.Reason != orphanQueued {
				o.Since = prev.Since
			}
		}
		next[id] = o
	}
	changed := len(next) != len(b.now)
	for id, o := range next {
		if prev, ok := b.now[id]; !ok || prev.Reason != o.Reason || o.Reason == orphanQueued && o.key != prev.key {
			changed = true
		}
	}
	b.now, b.seen, b.progress = next, seen, progress
	b.mu.Unlock()
	if changed {
		s.trevision.changed()
		s.todosPushSoon()
		s.boardChanged()
	}
}

// orphanWaitsOnDecision says whether supervised task t waits on purpose, for its review or for the owner, so that
// nobody is meant to work on it and an agent it no longer has is no orphan. The owner's check is not such a wait for
// its agents: a worker gone during it is still an orphan, so the supervisor hears of it and can review findings that
// went stale meanwhile (#15578).
func orphanWaitsOnDecision(t Todo) bool {
	switch t.Supervision.state(t) {
	case reviewPending, reviewNeedsUser, reviewParked, reviewTimedOut, reviewClosing:
		return true
	}
	return false
}

// queuedStartWaits says whether a start still queued for t waits on purpose: as orphanWaitsOnDecision, and also while
// the owner checks the result, when no start is due (#17090).
func queuedStartWaits(t Todo) bool {
	return orphanWaitsOnDecision(t) || t.Supervision.state(t) == reviewUserCheck
}

func queuedTaskOrphan(planID string, t PlanTask, now time.Time) (TodoOrphan, bool) {
	q := t.Queue
	if t.TodoID == "" || q == nil || q.Claimed || q.QueuedAt.IsZero() || len(q.Blockers) != 0 ||
		q.Key != pendingPlacementQueueKey && q.Key != workspaceQueueKey || now.Sub(q.QueuedAt) < orphanQueuedAfter {
		return TodoOrphan{}, false
	}
	return TodoOrphan{Reason: orphanQueued, Since: q.QueuedAt,
		key:     "orphaned:queued:" + planID + "@" + q.QueuedAt.UTC().Format(time.RFC3339Nano),
		machine: t.Machine, detail: clip(oneLine(t.queueReason()), 160)}, true
}

// orphanSignal is the supervisor's signal for supervised task t while nobody works on it: one per stretch (its key).
func (s *Service) orphanSignal(t Todo) (superviseSignal, bool) {
	o, ok := s.orphanOf(t.ID)
	if !ok || t.Supervision == nil || t.DoneAt != nil {
		return superviseSignal{}, false
	}
	if st := t.Supervision.state(t); st == reviewTimedOut || st == reviewClosing {
		return superviseSignal{}, false
	}
	return superviseSignal{Todo: t.ID, Kind: sigOrphaned, Key: o.key, Machine: o.machine, Session: o.session,
		Detail: o.detail, Orphan: o.Reason, WorkerSet: o.workerSet}, true
}
