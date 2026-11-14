package assistant

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// The processing conversation (docs/assistant/inbox.md"AI twin", phase 1): with the AI twin on, every Needs you event goes to the
// manager, one conversation per event, bound to it by the event's id. What comes later — new messages, the user's
// confirmation, rejection or words, the event being closed — is sent into that same conversation (sendToSession)
// instead of starting over; a conversation found gone is replaced by a new one that carries the recap and the chain.
// The conversation hands in one suggestion at a time (inbox_suggest); what others would see runs only once the user
// confirms (POST /api/v1/inbox/{id}/suggestion), and then by the program, exactly as shown.

// inboxNote is something waiting to be told an event's conversation (prompts/inbox/handling/<Name>.md).
type inboxNote struct {
	Name    string `json:"name"`
	Text    string `json:"text,omitempty"`
	Edited  bool   `json:"edited,omitempty"`
	Failed  bool   `json:"failed,omitempty"`
	Context string `json:"context,omitempty"`
	Machine string `json:"machine,omitempty"`
	Session string `json:"session,omitempty"`
	Who     string `json:"who,omitempty"`
	Next    string `json:"next,omitempty"`
	Checks  int    `json:"checks,omitempty"`
	// Deferred holds this programmatic wake out of every direct delivery path until the unified scheduler admits it.
	// Pending is persisted but never listed, so this is durable internal bookkeeping rather than client wire state.
	Deferred bool `json:"deferred,omitempty"`
}

func renderNote(n inboxNote, owner string) string {
	return prompts.RenderInboxHandlingNote(n.Name, prompts.InboxHandlingNote{Owner: owner, Text: n.Text, Edited: n.Edited, Failed: n.Failed,
		Context: n.Context, Machine: n.Machine, Session: n.Session, Who: n.Who, Next: n.Next, Checks: n.Checks})
}

// chainLine is a chain entry as the conversation reads it, its time in loc (the user's zone).
func chainLine(e InboxChainEntry, loc *time.Location) string {
	line := e.At.In(loc).Format("01-02 15:04") + " " + e.Text
	if e.Detail != "" && !strings.Contains(e.Text, e.Detail) {
		line += "（" + clip(oneLine(e.Detail), 200) + "）" // i18n-ignore: a line of the prompt the model reads (prompts/inbox/agent.md)
	}
	return line
}

// tell queues a note for the event's conversation; wake: it asks the conversation to work (a turn starts). With no
// conversation, a waking note queues one (startInboxAgents carries the notes in its first message). Under b.mu.
func (b *inboxBook) tell(it *InboxItem, n inboxNote, wake bool, now time.Time) {
	a := it.Agent
	if a == nil {
		return
	}
	a.queueNote(n)
	if !wake {
		return
	}
	b.wakeInboxConversation(it, now)
}

// wakeInboxConversation applies tell's wake bookkeeping without adding another note. Under b.mu.
func (b *inboxBook) wakeInboxConversation(it *InboxItem, now time.Time) {
	a := it.Agent
	a.Wake = true
	if h := it.Handling; h != nil && h.State != HandlingLooking {
		a.Back = h.State
	}
	t := now.UTC()
	a.StartedAt = &t
	if a.SessionID == "" {
		a.State, a.Attempts, a.RetryAfter, a.Error = AgentQueued, 0, nil, ""
	}
}

// queueInboxWake records a capped source notification immediately. An item already occupying a processing slot reuses
// it; otherwise the note is held at the item's original first-deferred time until admitInboxSourceWake. Under b.mu.
func (b *inboxBook) queueInboxWake(it *InboxItem, n inboxNote, reuseSlot bool, now time.Time) {
	a := it.Agent
	if a == nil {
		return
	}
	n.Deferred = true
	a.queueNote(n)
	if reuseSlot || inboxConversationRunning(it) {
		b.admitInboxSourceWake(it, now)
		return
	}
	if a.DeferredAt == nil {
		t := now.UTC()
		a.DeferredAt = &t
	}
}

// admitInboxSourceWake releases every held note on one item in its original order and applies the same handling and
// restoration transitions as the former immediate paths. The caller has already reserved a slot, or the item is
// reusing its own active slot. Under b.mu.
func (b *inboxBook) admitInboxSourceWake(it *InboxItem, now time.Time) {
	a := it.Agent
	if a == nil {
		return
	}
	for i := range a.Pending {
		if !a.Pending[i].Deferred {
			continue
		}
		n := a.Pending[i]
		switch n.Name {
		case prompts.NoteAgentStopped:
			// Only a current Agent's stop is a capped wake. Older stops remain non-waking informational notes.
			if inboxCurrentAgentStopNote(it, n) {
				if it.Handling != nil {
					switch it.Handling.State {
					case HandlingAgent:
						if a.Back == HandlingAgent {
							a.Back = ""
						}
						b.setHandling(it, HandlingLooking, "", now)
					case HandlingWaiting:
						a.Back = HandlingWaiting
						b.setHandling(it, HandlingLooking, "", now)
					}
				}
				b.wakeInboxConversation(it, now)
			}
		case prompts.NoteSelfReplied:
			if it.Handling != nil && it.Handling.State == HandlingAgent {
				// The current Agent keeps its state and timeout clock. Back remembers a pre-existing wait exactly as
				// the existing immediate path did; WakeSlot below accounts for this otherwise invisible processing turn.
				restoreWaiting := a.Back == HandlingWaiting
				startedAt := a.StartedAt
				b.wakeInboxConversation(it, now)
				if restoreWaiting {
					a.Back = HandlingWaiting
				}
				a.StartedAt = startedAt
			} else {
				// The immediate path records a reply outside Agent work as waiting before waking. Reproduce that transition
				// here even when an earlier held stop or read already moved this replay through looking.
				b.setHandling(it, HandlingWaiting, "", now)
				b.wakeInboxConversation(it, now)
				b.setHandling(it, HandlingLooking, "", now)
			}
		case prompts.NoteSelfRead:
			b.wakeInboxConversation(it, now)
			b.setHandling(it, HandlingLooking, "", now)
		default:
			b.wakeInboxConversation(it, now)
		}
		a.Pending[i].Deferred = false
	}
	a.DeferredAt = nil
	// HandlingAgent does not belong in the ordinary processing-conversation count. Keep an explicit reservation
	// until a turn outcome changes handling and setHandling releases it.
	a.WakeSlot = a.Wake && !inboxOrdinaryRunning(it)
}

// flushInboxWakes is the uncapped user/new-counterpart-message path: replay every held source transition in order,
// then let the caller's own waking note establish the combined turn's restoration point. No programmatic reservation
// survives this uncapped path. Under b.mu.
func (b *inboxBook) flushInboxWakes(it *InboxItem, now time.Time) {
	a := it.Agent
	if a == nil {
		return
	}
	b.admitInboxSourceWake(it, now)
	a.DeferredAt, a.WakeSlot = nil, false
}

// inboxCurrentAgentStopNote reports whether a stop belongs to the latest Agent link, rather than an older Agent whose
// result is informational only. Under b.mu.
func inboxCurrentAgentStopNote(it *InboxItem, n inboxNote) bool {
	latest := inboxLatestAgentLink(it)
	return latest != nil && latest.MachineID == n.Machine && latest.SessionID == n.Session
}

// inboxDeferredAgentStop reports a settled current-Agent result whose notification is still held for capacity. Only
// current stops enter the deferred queue; an older Agent's stop is informational and is queued by tell instead.
// Under b.mu.
func inboxDeferredAgentStop(it *InboxItem) bool {
	if it.Agent == nil {
		return false
	}
	for _, n := range it.Agent.Pending {
		if n.Deferred && n.Name == prompts.NoteAgentStopped && inboxCurrentAgentStopNote(it, n) {
			return true
		}
	}
	return false
}

// dropInboxWakes discards held work when handling fails or closes; informational notes already eligible for delivery
// keep their existing behavior. Under b.mu.
func (b *inboxBook) dropInboxWakes(it *InboxItem) {
	a := it.Agent
	if a == nil {
		return
	}
	kept := a.Pending[:0]
	for _, n := range a.Pending {
		if !n.Deferred {
			kept = append(kept, n)
		}
	}
	a.Pending = kept
	a.DeferredAt, a.WakeSlot = nil, false
}

// setHandling moves the event's processing state, and mirrors it on Agent for the apps from before (an Agent's
// running, drafted with the reply, failed). Under b.mu.
func (b *inboxBook) setHandling(it *InboxItem, state, errText string, now time.Time) {
	h := it.Handling
	if h == nil {
		h = &InboxHandling{Owner: HandlingOwner}
		it.Handling = h
	}
	if h.State != state || h.Since == nil {
		t := now.UTC()
		h.Since = &t
	}
	h.State, h.Error = state, errText
	a := it.Agent
	if a == nil {
		return
	}
	if state != HandlingAgent {
		a.WakeSlot = false
	}
	h.MachineID, h.SessionID = a.MachineID, a.SessionID
	if g := it.Suggestion; g != nil && g.Kind == SuggestReply && !g.Handback {
		at := g.At
		a.State, a.Draft, a.Note, a.Changes, a.Error, a.DraftedAt = AgentDrafted, g.Text, g.Note, g.Changes, "", &at
		return
	}
	a.Draft, a.Note, a.Changes = "", "", nil
	switch state {
	case HandlingLooking, HandlingAgent:
		if a.State != AgentQueued {
			a.State = AgentRunning
		}
		a.Error = ""
	case HandlingFailed:
		a.State, a.Error = AgentFailed, errText
	default:
		a.State, a.Error = AgentSkipped, ""
	}
}

// failHandling: the conversation failed or timed out: back to Needs you without a suggestion, the chain says why.
// Under b.mu.
func (b *inboxBook) failHandling(it *InboxItem, reason string, now time.Time) {
	it.Suggestion = nil
	if it.Agent != nil {
		it.Agent.Wake = false
	}
	b.dropInboxWakes(it)
	b.setHandling(it, HandlingFailed, reason, now)
	b.chainAdd(it, InboxChainEntry{Kind: ChainFailed, At: now.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain.failed"), Detail: reason})
}

// closeHandling: the event is over (the user filed it, replied elsewhere…): its suggestion goes, and a conversation
// still on it (or with a suggestion out) is told to stop; why is how (prompts/inbox/handling/closed.md). It says
// whether there is something to tell. Under b.mu.
func (b *inboxBook) closeHandling(it *InboxItem, why string, now time.Time) bool {
	a := it.Agent
	if a == nil || it.Handling == nil {
		return false
	}
	active := it.handled() && !(it.Suggestion != nil && it.Suggestion.Kind == SuggestConclusion && it.Handling.State == HandlingDone)
	it.Suggestion = nil
	a.Wake, a.Pending = false, nil
	a.DeferredAt, a.WakeSlot = nil, false
	b.setHandling(it, HandlingDone, "", now)
	if !active || a.SessionID == "" {
		return false
	}
	a.queueNote(inboxNote{Name: prompts.NoteClosed, Text: why})
	return true
}

// ownSend: the source says the newest message of the item is the user's, and it is a reply Persona sent (nothing came
// in since): not a separate reply the user made in the source. SentOf still proves this after a due check or another
// wake has temporarily moved handling out of waiting. Under b.mu.
func ownSend(it *InboxItem, last string) bool {
	a := it.Agent
	return a != nil && it.Handling != nil && a.SentOf != "" && a.SentOf == it.Last && (last == "" || last == it.Last)
}

// escalate hands the manager its work, under b.mu: a newly triaged Needs you event (recent) gets a processing
// conversation (queued); new content of an event that has one — being handled, or triaged Needs you again — is sent into
// that conversation. It returns the items it moved, settled (inbox_rollback.go).
func (b *inboxBook) escalate(since, now time.Time) []*inboxWrite {
	b.mu.Lock()
	defer b.mu.Unlock()
	var changed []*inboxWrite
	for _, it := range b.items {
		a := it.Agent
		if !it.open() || it.Context == "" || a != nil && a.Of == it.Last {
			continue
		}
		handled := it.handled()
		twin := b.policyTwin(it) // the user's policy (inbox_policies.go): first takes what is not Needs you too
		if !handled && (twin == TwinNever || it.Tier != TierAction && twin != TwinFirst || it.TriagedAt != it.Last) {
			continue
		}
		// A URL-only link has an Agent record but has never asked the twin to work. It follows the same age
		// limit as an unlinked item; an established conversation can still receive its later messages.
		if (a == nil || a.Of == "" && a.StartedAt == nil && a.SessionID == "") && !it.UpdatedAt.After(since) {
			continue
		}
		w := beginWrite(it)
		if it.Follow != nil {
			it.Follow.Checks, it.Follow.Due = 0, false
		}
		if a == nil {
			a = &InboxAgent{State: AgentQueued}
			it.Agent = a
		}
		a.Of, a.Task, a.Reason = it.Last, it.Summary, it.Reason
		// (a first message has the item's content: startInboxAgents leaves this one out of it)
		b.flushInboxWakes(it, now)
		b.tell(it, inboxNote{Name: prompts.NoteNewMessages, Context: it.Context}, true, now)
		b.setHandling(it, HandlingLooking, "", now)
		b.settle(w)
		changed = append(changed, w)
	}
	return changed
}

// conversationsByEvent are the processing conversations by the event they were started for (their origin's ref),
// the newest of each.
func (s *Service) conversationsByEvent(ctx context.Context) map[string]SessionRef {
	b := &s.origins
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.originsLocked(ctx) {
		return nil
	}
	out, at := map[string]SessionRef{}, map[string]time.Time{}
	for sid, o := range b.m {
		if o.Origin == OriginInbox && o.Ref != "" && !s.isWorkMachine(o.MachineID) && (at[o.Ref].IsZero() || o.At.After(at[o.Ref])) {
			out[o.Ref], at[o.Ref] = SessionRef{MachineID: o.MachineID, SessionID: sid}, o.At
		}
	}
	return out
}

// deliverPending delivers what waits for the events' conversations.
func (s *Service) deliverPending(ctx context.Context) {
	b := &s.inbox
	b.mu.Lock()
	var todo []*InboxItem
	for _, it := range b.items {
		if a := it.Agent; a != nil && len(a.Pending) > 0 && a.SessionID != "" {
			todo = append(todo, it)
		}
	}
	b.mu.Unlock()
	for _, it := range todo {
		s.deliverNotes(ctx, it)
	}
}

// deliverNotes sends what waits for the event's conversation into it. A conversation found gone is dropped: a note
// that asks for work then queues a new one (carrying the recap and the chain); a send that failed otherwise is tried
// again on the next pass.
func (s *Service) deliverNotes(ctx context.Context, it *InboxItem) {
	b := &s.inbox
	v, _ := b.telling.LoadOrStore(it.ID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	enabled := s.inboxConfig(ctx).Agent.Enabled
	owner := s.owner(ctx)
	b.mu.Lock()
	if cur := b.items[it.ID]; cur != it {
		// Forgotten (drop) while this was on its way: nothing goes, and the lock this may have put back goes again. One
		// of an item kept under the id since is another's, and stays.
		if cur == nil {
			b.telling.CompareAndDelete(it.ID, mu)
		}
		b.mu.Unlock()
		return
	}
	if !enabled {
		w := b.releaseInboxAgent(it, HandlingFailed, i18n.T(b.userLang(), "assistant-inbox_agent.disabled"), time.Now())
		b.mu.Unlock()
		if w != nil {
			s.saveWrites(ctx, []*inboxWrite{w}, "the disabled AI twin's work")
		}
		return
	}
	a := it.Agent
	if a == nil || a.SessionID == "" || len(a.Pending) == 0 {
		b.mu.Unlock()
		return
	}
	machine, sid, wake := a.MachineID, a.SessionID, a.Wake
	pending := append([]inboxNote(nil), a.Pending...)
	base := a.told - len(pending)
	delivered := map[int]bool{}
	notes := make([]inboxNote, 0, len(pending))
	for i, n := range pending {
		if !n.Deferred {
			delivered[base+i+1] = true
			notes = append(notes, n)
		}
	}
	if len(notes) == 0 {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	if s.isWorkMachine(machine) {
		b.mu.Lock()
		if it.Agent == a && a.SessionID == sid {
			b.failHandling(it, i18n.T(s.lang(ctx), "assistant-start.work_machine"), time.Now())
		}
		b.mu.Unlock()
		s.saveInbox(ctx, []*InboxItem{it})
		return
	}
	texts := make([]string, len(notes))
	for i, n := range notes {
		texts[i] = renderNote(n, owner)
	}
	err := s.sendToSession(ctx, machine, sid, strings.Join(texts, "\n\n---\n\n"))
	gone := false
	if err != nil {
		// Gone only when its machine answers without it (an offline machine is tried again later).
		if _, known := s.device(ctx, machine); known {
			_, found := s.session(ctx, machine, sid)
			gone = !found
		}
		s.logf("inbox: tell the conversation of %s: %v (gone: %v)", it.ID, err, gone)
	}
	now := time.Now()
	queueRecovery := false
	b.mu.Lock()
	if it.Agent == a && a.SessionID == sid {
		switch {
		case err == nil:
			a.keepToldOthers(delivered) // notes queued while it was sent, including held ones, go next time
			if wake {
				a.Wake = false
				if it.Handling == nil || it.Handling.State != HandlingAgent {
					t := now.UTC()
					a.StartedAt = &t
				}
			}
		case gone:
			held := a.DeferredAt != nil
			queueRecovery = wake || held
			a.Lost = append(a.Lost, sid)
			a.MachineID, a.SessionID = "", ""
			if queueRecovery {
				// A held wake still needs the replacement conversation. startInboxAgents admits it only after reserving
				// capacity, so the first message carries both ready and formerly deferred notes in their original order.
				a.State, a.Attempts, a.RetryAfter = AgentQueued, 0, nil
				a.Wake = true
			} else {
				a.Pending = nil
			}
			a.WakeSlot = false
			if it.Handling != nil {
				it.Handling.MachineID, it.Handling.SessionID = "", ""
			}
		}
	}
	b.mu.Unlock()
	if err == nil || gone {
		s.saveInbox(ctx, []*InboxItem{it})
	}
	if gone && queueRecovery {
		s.handInbox(ctx)
	}
}

// tellSoon delivers an item's notes in the background (a request answers first).
func (s *Service) tellSoon(ctx context.Context, it *InboxItem) {
	ctx = context.WithoutCancel(ctx)
	go func() {
		s.deliverNotes(ctx, it)
		s.handInbox(ctx) // one without a conversation is started
	}()
}

// inboxAgentStarted records the Agent an event's conversation started (agent_start from it): the chain links that
// sub-session, the event is In progress (Agent is working) until the conversation hands in what it found.
func (s *Service) inboxAgentStarted(ctx context.Context, self, machine, session, prompt string) {
	if self == "" || session == "" || s.inboxReady(ctx) != nil {
		return
	}
	if hs, ok := ctx.Value(handoffStartKey{}).(*handoffStart); ok {
		s.handoffAgentStarted(hs, machine, session, prompt)
		return
	}
	enabled := s.inboxConfig(ctx).Agent.Enabled
	b := &s.inbox
	b.mu.Lock()
	it := b.itemOfAgent(self)
	if it == nil || !it.open() {
		b.mu.Unlock()
		return
	}
	now := time.Now()
	b.chainAdd(it, InboxChainEntry{Kind: ChainAgent, At: now.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain.agent",
		i18n.Args{"title": clip(oneLine(prompt), 40)}), Link: &InboxChainLink{Type: LinkSession, MachineID: machine, SessionID: session}})
	if enabled && it.Suggestion == nil {
		t := now.UTC()
		it.Agent.StartedAt = &t
		b.setHandling(it, HandlingAgent, "", now)
	}
	b.mu.Unlock()
	s.saveInbox(ctx, []*InboxItem{it})
}

// linkTaskFor asks the judge (model 2) which of the user's tasks going on the event is about
// (docs/assistant/unified-entry.md): the conversation is told that task's progress, and its reply suggestion puts a card
// in the task's conversation. Since phase 1 every Needs you event goes to the manager whatever the judge says of
// handling it; a model that fails leaves the event unlinked. w is the item's write not saved yet (nil: none): the link
// joins that write while the item still holds it (a concurrent move keeps the write from being undone over it).
func (s *Service) linkTaskFor(ctx context.Context, it *InboxItem, tasks []prompts.InboxTask, w *inboxWrite) bool {
	id, err := s.judgeInboxTask(ctx, it, tasks)
	if err != nil {
		if paused, _ := modelPaused(err); ctx.Err() == nil && !paused {
			s.logf("inbox: link %s to a task: %v", it.ID, err)
		}
		return false
	}
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	if it.Agent != nil && id != "" && inboxTaskLinkEligible(it) {
		held := w != nil && b.holds(w)
		it.Agent.TodoID = id
		if held {
			b.settle(w)
		}
		return true
	}
	return false
}

// judgeInboxTask is the task the judge links item it to, among tasks (inbox_task_scope.go): only the tasks the item's
// repository or thread allows are offered (none: no model call), and an answer counts only when it is a task it was
// shown and quotes that task's title or notes as its evidence. "" for none. Callers apply and save the result.
func (s *Service) judgeInboxTask(ctx context.Context, it *InboxItem, tasks []prompts.InboxTask) (string, error) {
	b := &s.inbox
	b.mu.Lock()
	if !inboxTaskLinkEligible(it) {
		b.mu.Unlock()
		return "", nil
	}
	sc := inboxScopeOf(it)
	b.mu.Unlock()
	if tasks = s.scopeInboxTasks(sc, tasks); len(tasks) == 0 {
		return "", nil
	}
	b.mu.Lock()
	input := prompts.RenderInboxJudgeInput(prompts.InboxJudgeInput{Summary: it.Summary, Item: b.promptItem(it), Tasks: tasks})
	b.mu.Unlock()
	system := prompts.RenderInboxJudge(prompts.InboxJudge{Owner: s.owner(ctx), Rules: s.inboxConfig(ctx).AgentRules})
	var out struct {
		TaskID   string `json:"task_id"`
		Evidence string `json:"evidence"`
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	err := s.inboxModelJSON(cctx, purposeJudge, system, input, 0, &out)
	cancel()
	if err != nil {
		return "", err
	}
	for _, t := range tasks { // only a task it was shown, with a line copied from it
		if t.ID == strings.TrimSpace(out.TaskID) {
			if !judgeEvidence(t, out.Evidence) {
				s.logf("inbox: %s: the judge named task %s without evidence from it (%q): not linked", it.ID, t.ID, clip(oneLine(out.Evidence), 80))
				return "", nil
			}
			return t.ID, nil
		}
	}
	return "", nil
}
