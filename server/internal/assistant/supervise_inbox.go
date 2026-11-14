package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/prompts"
)

// New Inbox messages about a supervised task reach its supervisor (#4293; docs/assistant/unified-entry.md
// "Supervision (#3124)" and "Message cards link to tasks"). The AI twin's pass takes new messages from other people into
// an item, and the judge links the item to one of the user's tasks (linkTaskFor). When that task is supervised, its
// supervisor gets the signal inbox through the usual digest: routed to the task's leader conversation or the main one,
// gathered, deduplicated and spaced like any other signal, and several messages about one task before the message goes
// out are one entry, the newest item's, with how many came. Off unless assistant.inbox_signals is on: each one is a
// supervisor turn the user pays for.
//
// Nothing Persona says itself raises one, so a supervisor's turn cannot feed the next: not the reply Persona sent for a
// confirmed suggestion (SentOf), not a bot chat (where Persona's own bot posts its pushes and cards), and not the twin's
// handling chat (what it is told goes into that chat, never into the Inbox). A task waiting for the owner's decision
// hears nothing, as with the other signals, and neither does a task leader's worker: a task leader is a coding agent
// working without permission prompts, and the Inbox's text is other people's.

// sigInbox: new Inbox messages from other people about the task.
const sigInbox = "inbox"

// inboxSignalsOn: assistant.inbox_signals is on (off by default).
func (s *Service) inboxSignalsOn(ctx context.Context) bool {
	v := s.settings(ctx).InboxSignals
	return v != nil && *v
}

// superviseInbox raises the inbox signal for those of items (the twin's pass just took new content into them) that are
// linked to a supervised task, while assistant.inbox_signals is on. linked are the items the judge linked in this pass:
// all they hold is new to the supervisor.
//
// An item linked to a task that was closed and carried on by another (continues) follows it there first
// (followContinuations), whatever the switch says: the link and the signal belong to the task doing the work now. A
// message is counted as told only once a signal took it, so one that came while its task could not hear it (closed,
// unsupervised, waiting for the owner) is not lost to the task that can.
func (s *Service) superviseInbox(ctx context.Context, items []*InboxItem, linked map[*InboxItem]bool) {
	if len(items) == 0 {
		return
	}
	s.followContinuations(ctx, items)
	if !s.inboxSignalsOn(ctx) {
		return
	}
	b := &s.inbox
	b.mu.Lock()
	todos := make([]string, len(items))
	for i, it := range items {
		if it.Agent != nil {
			todos[i] = it.Agent.TodoID
		}
	}
	b.mu.Unlock()
	hears := map[string]bool{}
	for _, id := range todos {
		if _, seen := hears[id]; !seen && id != "" {
			t, ok := s.todoNow(id)
			hears[id] = ok && s.inboxSignalHolds(t)
		}
	}
	var out []superviseSignal
	b.mu.Lock()
	for i, it := range items {
		if it.Agent == nil || it.Agent.TodoID != todos[i] || !hears[todos[i]] {
			continue // relinked meanwhile, or its task cannot hear it: the messages wait, uncounted
		}
		if g, ok := inboxSignalOf(it, linked[it]); ok {
			out = append(out, g)
		}
	}
	b.mu.Unlock()
	for _, g := range out {
		s.signal(g)
	}
}

// continuesHops bounds how far a closed task's link follows the tasks that continue it.
const continuesHops = 8

// followContinuations moves the items of items linked to a closed task to the one open task that carries it on
// (currentTaskOf), and saves them: open items from superviseInbox, handled ones from linkInboxTaskURLs (#6546). The
// move is no news: it raises nothing by itself.
func (s *Service) followContinuations(ctx context.Context, items []*InboxItem) {
	b := &s.inbox
	b.mu.Lock()
	linkedTo := map[*InboxItem]string{}
	for _, it := range items {
		if it.Agent != nil && it.Agent.TodoID != "" {
			linkedTo[it] = it.Agent.TodoID
		}
	}
	b.mu.Unlock()
	moves := map[*InboxItem]string{}
	now := map[string]string{}
	for it, id := range linkedTo {
		cur, seen := now[id]
		if !seen {
			cur, _ = s.currentTaskOf(id)
			now[id] = cur
		}
		if cur != "" && cur != id {
			moves[it] = cur
		}
	}
	if len(moves) == 0 {
		return
	}
	var writes []*inboxWrite
	b.mu.Lock()
	for it, to := range moves {
		if b.items[it.ID] != it || it.Agent == nil || it.Agent.TodoID != linkedTo[it] {
			continue // moved or relinked meanwhile
		}
		w := beginWrite(it)
		it.Agent.TodoID = to
		b.settle(w)
		writes = append(writes, w)
		s.logf("inbox: %s: its task %s was closed and %s carries it on: linked there", it.ID, linkedTo[it], to)
	}
	b.mu.Unlock()
	s.saveWrites(ctx, writes, "its link to the task that carries on its closed one")
}

// currentTaskOf is the task todo id stands for now: itself while it is open; once it is closed (done or cancelled),
// the one open task that continues it, down any line of tasks that continued each other (task_brief_todos.continues).
// "" when there is no such task, or more than one.
func (s *Service) currentTaskOf(id string) (string, bool) {
	if t, ok := s.todoNow(id); !ok {
		return "", false
	} else if taskOpen(t) {
		return id, true
	}
	next := map[string][]string{}
	b := &s.tbriefs
	b.mu.Lock()
	for todo, l := range b.links {
		if l.Continues != "" {
			next[l.Continues] = append(next[l.Continues], todo)
		}
	}
	b.mu.Unlock()
	seen := map[string]bool{id: true}
	var open []string
	for hop, line := 0, next[id]; hop < continuesHops && len(line) > 0; hop++ {
		var deeper []string
		for _, c := range line {
			if seen[c] {
				continue
			}
			seen[c] = true
			if t, ok := s.todoNow(c); ok && taskOpen(t) {
				open = append(open, c) // the one doing the work: what continues it is not looked at
			} else if ok {
				deeper = append(deeper, next[c]...)
			}
		}
		line = deeper
	}
	if len(open) != 1 {
		return "", false
	}
	return open[0], true
}

// taskOpen: t is neither done nor cancelled.
func taskOpen(t Todo) bool { return t.DoneAt == nil && !t.Cancelled() }

// inboxSignalHolds: an inbox signal about task t is told its supervisor: t is open and supervised by a conversation, and
// does not wait for the owner's decision.
func (s *Service) inboxSignalHolds(t Todo) bool {
	v := t.Supervision
	return v != nil && t.DoneAt == nil && v.state(t) != reviewNeedsUser && !s.agentSupervised(t)
}

// inboxSignalOf is the inbox signal of item it, whose new content the twin's pass just took; false when it is linked to
// no task or its newest message is not someone else's. It counts the messages received since the item count at the last
// signal it gave (receivedSince), and remembers that count. Under b.mu.
func inboxSignalOf(it *InboxItem, linked bool) (superviseSignal, bool) {
	a := it.Agent
	if a == nil || a.TodoID == "" || !it.open() || it.Kind == KindBot || a.SentOf != "" && a.SentOf == it.Last ||
		a.signaledOf != "" && a.signaledOf == it.Last {
		return superviseSignal{}, false
	}
	n, seq := receivedSince(it, a.signaled, linked)
	a.signaled = seq
	a.signaledOf = it.Last
	return superviseSignal{Todo: a.TodoID, Kind: sigInbox, Key: it.ID + "@" + it.Last, Inbox: &prompts.SuperviseInbox{
		Item: it.ID, Title: clip(oneLine(it.Title), 80), Sender: clip(oneLine(it.Sender), 40), Preview: clip(oneLine(it.Preview), 140),
		Count: n}}, true
}

// receivedSince counts the messages received since the item's count was last signaled, and returns the current count
// as the next watermark. since 0 (nothing counted yet in this run of the manager): an item linked just now counts every
// message its item count holds, any other only the newest received entry, the one surely new. A mail item's count is
// its unread count, which can fall below the watermark when mail is read elsewhere; its next batch then counts as one
// and still signals.
func receivedSince(it *InboxItem, since int, linked bool) (n, seq int) {
	seq = it.Count
	if since > 0 {
		return max(it.Count-since, 1), seq
	}
	if linked {
		return max(it.Count, 1), seq
	}
	for i := len(it.Chain) - 1; i >= 0; i-- {
		if it.Chain[i].Kind == ChainReceived {
			return max(it.Chain[i].Count, 1), seq
		}
	}
	return 1, seq
}

// joinInbox is inbox signal g in the place of cur, which still waits to be told: the newest item, with the messages of
// both.
func joinInbox(cur, g superviseSignal) superviseSignal {
	if cur.Inbox == nil || g.Inbox == nil {
		return g
	}
	in := *g.Inbox
	in.Count += cur.Inbox.Count
	g.Inbox = &in
	return g
}
