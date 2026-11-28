package assistant

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// Replacing the main conversation (#4866, docs/assistant/assistant.md "New main chat"): the open tasks of the main
// conversation that was put away (POST /api/v1/assistant/main/new) or replaced in the settings go to the main one now.
// Their supervisor and their origin move together, as a closed leader's workers are rehomed (task_leader.go): every
// check that compares the supervisor with a conversation (agent_send's provenance, the done judge's, agent_supervise,
// task_review) then counts the new main conversation as the one that handed the task out, and the task's receipts,
// reviews and reports go to its feed instead of an archived one nobody opens. The ledger is kept as it is (criteria,
// reviews, round, parking, waits on the owner); a task waiting for review is signalled again to the new main
// conversation. Only a previously assigned legacy background notice may still finish in the old background conversation;
// no new background route is created (#5732). The card of a task the new main supervises is put there (the old
// conversation keeps its history). A task a leader supervises keeps its leader; closed tasks and the owner's own
// to-dos are not touched.

// handOverMainTasks moves the open tasks of conversation from, a main conversation that was replaced, to conversation
// to, the main one now; it answers how many moved. Nothing moves when from is a leader now (it supervises its tasks).
func (s *Service) handOverMainTasks(ctx context.Context, from, to string) int {
	if from == "" || to == "" || from == to || s.isLeaderRef(from) || s.isLeader(ctx, from) {
		return 0
	}
	moved := 0
	for _, t := range s.todosOf(ctx, "*") {
		if t.DoneAt != nil || t.Cancelled() {
			continue
		}
		heldBy := func(v *Supervision) bool { return v.Supervisor == from || v.Supervisor == "" && t.Origin == from }
		ledger := t.Supervision != nil && heldBy(t.Supervision)
		if t.Origin != from && !ledger {
			continue
		}
		if s.handOverTask(ctx, t, from, to, heldBy) {
			moved++
		}
	}
	if moved > 0 {
		s.logf("assistant: %d open task(s) of the replaced main conversation %s handed to %s", moved, short(from), short(to))
	}
	s.handOverMainScheduledTasks(ctx, from, to)
	return moved
}

type alwaysCopyStore interface {
	CopyAlways(ctx context.Context, fromSession, toSession string) (int, error)
}

// isInboxSession reports whether session is recorded as an Inbox-origin conversation on any machine
// (fail closed when the origin book cannot be loaded).
func (s *Service) isInboxSession(ctx context.Context, session string) bool {
	b := &s.origins
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.originsLocked(ctx) {
		return true
	}
	o := b.m[session]
	return o != nil && o.Origin == OriginInbox
}

// handOverMainAlways copies "Always allow in this conversation" grants from a replaced main conversation to
// the new main conversation once at replacement time (#16158). It never copies from a leader or Inbox-origin
// conversation, and is intentionally not called from the periodic handOverFormerMainTasks sweep so revocations
// made on the new main conversation always stick.
func (s *Service) handOverMainAlways(ctx context.Context, from, to string) int {
	if from == "" || to == "" || from == to || s.isLeaderRef(from) || s.isLeader(ctx, from) {
		return 0
	}
	if s.isInboxSession(ctx, from) {
		return 0
	}
	st, ok := s.store().(alwaysCopyStore)
	if !ok {
		return 0
	}
	cctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	copied, err := st.CopyAlways(cctx, from, to)
	if err != nil {
		s.logf("assistant: carry always grants from %s to %s: %v", short(from), short(to), err)
		return 0
	}
	if copied > 0 {
		s.logf("assistant: %d always-allow grant(s) of the replaced main conversation %s carried to %s", copied, short(from), short(to))
	}
	return copied
}

// mainTargetMachine resolves the machine hosting replacement main conversation to: it trusts an explicit
// MainMachineID binding first, otherwise locates to among connected devices before falling back to mainRouteMachine.
func (s *Service) mainTargetMachine(ctx context.Context, cfg Settings, to string) string {
	if cfg.Main == to && cfg.MainMachineID != "" {
		return cfg.MainMachineID
	}
	if m, _, found := s.findSession(ctx, to, cfg.mainRouteMachine()); found {
		return m
	}
	return cfg.mainRouteMachine()
}

// pendingOneOffRetry reports whether a one-off task whose initial attempt already flipped Enabled=false still has
// an automatic retry eligible to run (so main handover must migrate it too instead of leaving the retry on the old main).
func pendingOneOffRetry(t Task, runs []ScheduleRun, now time.Time) bool {
	if !t.done() {
		return false
	}
	for _, r := range slices.Backward(runs) {
		if r.RefID != t.ID {
			continue
		}
		if r.Status != runFailed || r.Manual || r.Retried || r.DeliveryUnknown {
			return false
		}
		return retryCancelled(t, true, r, runs, now) == ""
	}
	return false
}

// handOverMainScheduledTasks moves pending scheduled tasks bound to the replaced main conversation from onto
// the new main conversation to (and its machine). Quiet or repeating tasks are isolated to their own conversation
// (matching isolateScheduledConversation) so they neither fire into the archived old main conversation nor pollute
// the new one. Completed one-off follow-ups (with no pending automatic retry), space-scoped tasks, workout tasks
// and other conversations' tasks stay untouched.
func (s *Service) handOverMainScheduledTasks(ctx context.Context, from, to string) int {
	if from == "" || to == "" || from == to || s.isLeaderRef(from) || s.isLeader(ctx, from) {
		return 0
	}
	cfg := s.settings(ctx)
	toMachine := s.mainTargetMachine(ctx, cfg, to)
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return 0
	}
	now := time.Now().UTC()
	tasks := slices.Clone(sc.tasks)
	moved := 0
	for i := range tasks {
		t := &tasks[i]
		if t.SessionID != from || t.Space != "" || t.WorkoutID != "" {
			continue
		}
		if t.done() && !sc.sending[t.ID] && !pendingOneOffRetry(*t, sc.runs, now) {
			continue
		}
		if t.pushesOnlyAsked() || t.Repeat != repeatOnce {
			t.SessionID = ""
		} else {
			t.SessionID = to
			if toMachine != "" {
				t.MachineID = toMachine
			}
		}
		moved++
	}
	if moved == 0 {
		sc.mu.Unlock()
		return 0
	}
	if err := s.saveLocked(sc, tasks); err != nil {
		sc.mu.Unlock()
		s.logf("assistant: hand over %d scheduled task(s) from %s to %s: %v", moved, short(from), short(to), err)
		return 0
	}
	sc.mu.Unlock()
	s.broadcastSchedule()
	s.logf("assistant: %d scheduled task(s) of the replaced main conversation %s handed to %s", moved, short(from), short(to))
	return moved
}

// retargetFormerMainTask prevents a scheduled run from resurrecting an archived former main conversation:
// if a replacement main conversation already exists, a non-quiet one-off follow-up targets it; otherwise
// (including the gap after POST /api/v1/assistant/main/new or /clear before the new main starts, or legacy
// quiet/repeating tasks) the run falls back to its own conversation on the current assistant configuration.
func (s *Service) retargetFormerMainTask(ctx context.Context, t *Task, run *ScheduleRun) {
	if t.SessionID == "" || t.Space != "" || t.WorkoutID != "" || s.isLeaderRef(t.SessionID) || s.isLeader(ctx, t.SessionID) {
		return
	}
	cfg := s.settings(ctx)
	if cfg.Main == t.SessionID || !slices.ContainsFunc(cfg.Former, func(f FormerMain) bool { return f.SessionID == t.SessionID }) {
		return
	}
	if cfg.Main != "" && !t.pushesOnlyAsked() && t.Repeat == repeatOnce {
		t.SessionID = cfg.Main
		if m := s.mainTargetMachine(ctx, cfg, cfg.Main); m != "" {
			t.MachineID = m
		}
		return
	}
	t.SessionID = ""
	if cfg.Main == "" && !t.pushesOnlyAsked() && t.Repeat == repeatOnce && run != nil {
		run.ConversationReason = "source_unavailable"
	}
}

// handOverTask moves task t from conversation from to conversation to (handOverMainTasks): its origin when it is
// from, its supervisor when heldBy says the ledger is from's. Its card goes to the new conversation with its supervision
// (a leader keeps the card it has, and a task "Move to…" already moved has its card there). False when nothing moved
// (a write failed: the next pass tries again).
func (s *Service) handOverTask(ctx context.Context, t Todo, from, to string, heldBy func(*Supervision) bool) bool {
	// Move the owner and admit its review together, as an explicit task move does.
	s.supervise.routingMu.Lock()
	defer s.supervise.routingMu.Unlock()
	moved, supervised := false, false
	if t.Origin == from {
		if _, err := s.editTodo(ctx, t.ID, db.TodoEdit{Origin: &to}); err != nil {
			s.logf("task %s: hand over to the main conversation: origin: %v", t.ID, err)
			return false
		}
		s.todos.mu.Lock() // the stored row moved; memory follows (setTodoRow leaves the origin alone for every other writer)
		if m := s.todos.todos[t.ID]; m != nil {
			m.Origin = to
		}
		s.todos.mu.Unlock()
		s.todosChanged(ctx)
		moved = true
	}
	if t.Supervision != nil {
		pending := false
		v, changed, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
			if !heldBy(v) {
				return false // a leader's, or moved meanwhile
			}
			v.Supervisor = to
			pending = v.state(t) == reviewPending
			return true
		})
		switch {
		case err != nil:
			s.logf("task %s: hand over to the main conversation: %v", t.ID, err)
		case changed:
			moved, supervised = true, true
			if pending { // the old conversation was told; the new one has not been
				g := superviseSignal{Todo: t.ID, Kind: sigReview, Key: reviewKey(&v), Recipient: to, Detail: "主对话换了，这个任务转给你验收"} // i18n-ignore: the supervision message is the assistant's prompt
				// A handover can keep the same external-PR review key. Its previous supervisor's
				// early admission and delivery fingerprint do not acknowledge the new supervisor.
				s.supervise.mu.Lock()
				s.supervise.forgetToldLocked(g)
				s.supervise.mu.Unlock()
				s.signal(g)
			}
		}
	}
	if supervised {
		s.taskCard(ctx, to, t.ID, wakeActive) // its card follows it (the old conversation keeps its history)
	}
	if moved {
		s.logf("task %s: the main conversation %s was replaced; %s takes it over", t.ID, short(from), short(to))
	}
	return moved
}

// handOverFormerMainTasks moves what a former main conversation (Settings.Former) still holds to the main one: switches
// made before tasks moved with the main conversation, and a move a restart or a failed write cut short. It runs from the
// supervision loop and is idempotent: a moved task is no longer the former conversation's.
func (s *Service) handOverFormerMainTasks(ctx context.Context) {
	cfg := s.settings(ctx)
	if cfg.Main == "" || len(cfg.Former) == 0 {
		return
	}
	for _, f := range cfg.Former {
		if f.SessionID != cfg.Main {
			s.handOverMainTasks(ctx, f.SessionID, cfg.Main)
		}
	}
}
