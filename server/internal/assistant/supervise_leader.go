package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Direction leads supervise the tasks they plan; main supervises its own work (#13362). Explicit transfers
// override that initial assignment (#13344). Ordinary signals and start remedies stay with that supervisor;
// owner-decision escalations go to main. Task leaders' workers keep their leader Agent until it closes.
// Workers cannot hand out Persona tasks and use their runtime's in-session subagents instead.

// isLeader: conversation id is one the user made a leader (Settings.Leaders), and not the main conversation.
func (s *Service) isLeader(ctx context.Context, id string) bool {
	if id == "" {
		return false
	}
	cfg := s.settings(ctx)
	return id != cfg.Main && slices.Contains(cfg.Leaders, id)
}

// validateSupervisorLead checks a chosen direction against its stored identity. A real leader can be offline.
func (s *Service) validateSupervisorLead(ctx context.Context, id string) error {
	if !s.isLeader(ctx, id) {
		return fmt.Errorf("%s is not a leader conversation (GET /api/v1/assistant/leaders lists them)", id)
	}
	d, _, loaded := s.directionSnapshot(ctx, id)
	if !loaded {
		return errDirectionUnavailable
	}
	if d.Outside != nil {
		return i18n.Err("assistant-conversations.reporting_direction_supervisor")
	}
	return nil
}

// supervises: conversation id may hand out tasks rather than being a worker: the main conversation or a leader.
// Planning direction leads own their tasks from dispatch through review and cancellation.
func (s *Service) supervises(ctx context.Context, id string) bool {
	return s.isMain(ctx, id) || s.isLeader(ctx, id)
}

// defaultPlanSupervisor assigns a direction lead or live assistant side conversation its own plans, and otherwise uses
// main. A task leader's workers and active reporting conversations retain their specialized topology.
func (s *Service) defaultPlanSupervisor(ctx context.Context, conversation, parent string) (string, bool) {
	if parent != "" || conversation == "" {
		return "", false
	}
	if s.isLeader(ctx, conversation) {
		return conversation, true
	}
	if _, reports := s.reporterOf(ctx, conversation); reports {
		return "", false
	}
	if s.liveSideSupervisor(ctx, conversation) {
		return conversation, true
	}
	main := s.settings(ctx).Main
	return main, main != ""
}

// planTaskSupervisor resolves ownership before dispatch, including failed starts. Retrying or continuing an
// assigned task must not overwrite a later transfer with the saved plan's original conversation.
func (s *Service) planTaskSupervisor(ctx context.Context, conversation string, task PlanTask) (string, error) {
	cfg := s.settings(ctx)
	supervisor := ""
	for _, id := range []string{task.TodoID, task.ReuseTodo} {
		if t, ok := s.todoNow(id); ok && t.Supervision.assigned() {
			supervisor = t.Supervision.Supervisor
			break
		}
	}
	if supervisor == "" {
		if task.Parent != "" {
			return conversation, nil
		}
		supervisor, _ = s.defaultPlanSupervisor(ctx, conversation, "")
		if task.Supervisor != "" && s.isLeader(ctx, task.Supervisor) {
			supervisor = task.Supervisor
		} else if conversation != cfg.Main {
			if c, ok := s.session(ctx, cfg.MachineID, conversation); ok && c.Assistant && runtimeSessionSupportsSupervisionPolicy(c) && !s.isBackground(ctx, conversation) {
				supervisor = conversation
			}
		}
	}
	if supervisor == "" {
		return "", nil
	}
	machine := cfg.MachineID
	if supervisor == cfg.Main {
		machine = cfg.mainRouteMachine()
	}
	if !s.runtimeWithoutSupervisionPolicy(ctx, machine, supervisor) {
		return supervisor, nil
	}
	if s.isLeader(ctx, supervisor) {
		// A lead that cannot receive supervision must not silently hand its work to main.
		return "", planInvalid{i18n.Err("assistant-conversations.supervision_reviewer_required")}
	}
	if !s.runtimeWithoutSupervisionPolicy(ctx, cfg.mainRouteMachine(), cfg.Main) {
		return cfg.Main, nil
	}
	if background := s.runtimeSupervisorFallback(ctx, cfg); background != cfg.Main {
		return cfg.Main, nil
	}
	return "", planInvalid{i18n.Err("assistant-conversations.supervision_reviewer_required")}
}

// effectiveSupervisor is the conversation that owns t: its recorded supervisor, main when it has none. Whoever handed a
// task out keeps it; tasks planned before the supervisor source existed get theirs once, on load
// (backfillPlanSupervisors), instead of a read-time legacy guess.
func (s *Service) effectiveSupervisor(ctx context.Context, cfg Settings, t Todo) string {
	if t.Supervision == nil || t.Supervision.Supervisor == "" {
		return cfg.Main
	}
	return t.Supervision.Supervisor
}

// plannedLead is the direction lead that handed out t with task_plan before the supervisor source was recorded: the
// lead its saved plan task named, else the lead conversation it came from ("a task from conversation X", Origin).
// "" when neither is a lead now.
func (s *Service) plannedLead(ctx context.Context, t Todo) string {
	if p, err := s.loadPlan(ctx, t.PlanID); err == nil {
		for _, task := range p.Tasks {
			if task.TodoID == t.ID && s.isLeader(ctx, task.Supervisor) {
				return task.Supervisor
			}
		}
	}
	if s.isLeader(ctx, t.Origin) {
		return t.Origin
	}
	return ""
}

// backfillPlanSupervisors gives the tasks a direction lead handed out before #13346 back to that lead, once: the ones
// it planned and the ones whose origin is the lead (handed out without a plan: agent_start, todo_create), open or done
// (a done one keeps the right supervisor on record for a reopen and the history). The ledger records the lead with
// source plan, so routing, remedies and task_review read the lead from then on and a later load skips them. A lead's
// unmarked no-plan task with an Agent needs that source even when its supervisor is already the lead: without it,
// needs_user keeps the legacy main route (#13619). A direction to-do with no Agent yet stays unmarked. Signals already
// queued for the former supervisor (main, as the removed rehome left them) are gathered again for the lead. Task
// leaders' workers, explicit transfers and tasks other conversations planned keep their recorded supervisor.
func (s *Service) backfillPlanSupervisors(ctx context.Context) {
	cfg := s.settings(ctx)
	todos := s.todosOf(ctx, "*")
	moved := map[string]map[string]bool{} // former supervisor → the tasks the lead took back
	for _, t := range todos {
		if t.Supervision == nil || t.Supervision.assigned() || t.PlanID == "" && t.Origin == "" {
			continue
		}
		old := t.Supervision.Supervisor
		if _, _, worker := parseLeaderRef(old); worker {
			continue
		}
		lead := s.plannedLead(ctx, t)
		if lead == "" || t.PlanID == "" && old == lead && len(t.Agents) == 0 {
			continue // a direction to-do with no Agent yet stays unmarked; no-plan Agent work still needs its source
		}
		if s.runtimeWithoutSupervisionPolicy(ctx, cfg.MachineID, lead) {
			s.logf("task %s: backfill supervision: lead %s cannot receive supervision; left with %s", t.ID, short(lead), short(old))
			continue
		}
		if _, _, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
			if v.SupervisorSource != "" {
				return false
			}
			v.Supervisor, v.SupervisorSource = lead, "plan"
			return true
		}); err != nil {
			s.logf("task %s: backfill supervision: %v", t.ID, err)
			continue
		}
		if old == lead || old == "" {
			continue
		}
		s.logf("task %s: supervision backfilled from %s to its planning lead %s", t.ID, short(old), short(lead))
		if t.DoneAt != nil {
			continue // done: nothing queued to move, no card to show
		}
		if moved[old] == nil {
			moved[old] = map[string]bool{}
		}
		moved[old][t.ID] = true
		s.taskCard(ctx, lead, t.ID, "")
	}
	if len(moved) > 0 {
		web := s.webBase(ctx)
		for old, ids := range moved {
			s.rehomeQueuedSupervision(ctx, old, ids, todos, web)
		}
	}
}

// rehomeQueuedSupervision takes the rehomed tasks' signals out of an old side conversation's durable queue and gathers
// them again under their new supervisor. Queue admission already acknowledged their outbox entries, and their old dedup
// reservation would otherwise acknowledge the replay without delivering it. Other tasks in a mixed digest are regathered
// too, exactly as dropQueuedSupervisionTasks preserves them for /stop (#5732).
func (s *Service) rehomeQueuedSupervision(ctx context.Context, old string, ids map[string]bool, known []Todo, web string) {
	b := &s.supervise
	b.dedupMu.Lock()
	defer b.dedupMu.Unlock()
	b.mu.Lock()
	b.init()
	var memory, remaining []superviseSignal
	for _, g := range b.queued[old] {
		if ids[g.Todo] {
			memory = append(memory, g)
		} else {
			remaining = append(remaining, g)
		}
	}
	var replay []superviseSignal
	_, _ = s.dropQueuedSupervisionTasksAtomic(old, ids, known, func(pending map[string]superviseSignal, queued, regather []superviseSignal) bool {
		replay = mergeSignals(regather, remaining)
		replay = mergeSignals(replay, queued)
		replay = mergeSignals(replay, memory)
		for _, g := range pending { // an earlier attempt may already have committed the replay before its supervisor write
			if ids[g.Todo] {
				replay = mergeSignals(replay, []superviseSignal{g})
			}
		}
		dirty := false
		for _, g := range replay {
			cur, ok := b.pending[g.id()]
			if !ok {
				// First-load rehoming precedes the normal outbox restore. A newer durable stretch with the same id
				// must win over this older queued replay rather than being overwritten before that restore reads it.
				cur, ok = pending[g.id()]
			}
			if !ok {
				cur = g
			}
			if old, exists := pending[g.id()]; !exists || old.Key != cur.Key {
				dirty = true
			}
			b.pending[g.id()] = cur
			pending[g.id()] = cur
		}
		return dirty
	})
	if len(memory) > 0 {
		b.setQueuedLocked(old, nil)
	}
	for _, g := range replay {
		delete(b.sent, g.id())
	}
	b.mu.Unlock()

	if len(replay) > 0 {
		s.forgetSupervision(ctx, replay, web)
		b.mu.Lock()
		s.scheduleDigestLocked(time.Now())
		b.mu.Unlock()
	}
}

// digestTo routes supervision to the persisted planning/transfer assignment. Explicit escalations and card rungs
// keep their special target; unmarked legacy tasks retain their compatibility policy. Task
// leaders' Agent-supervised workers are handled by leaderRoute before this function.
func (s *Service) digestTo(ctx context.Context, cfg Settings, g superviseSignal) string {
	// Explicit escalations keep their destination; needs_user alone is not an escalation.
	if t, ok := s.todoNow(g.Todo); ok && t.Supervision != nil && t.Supervision.assigned() &&
		g.Kind != sigAsk && g.Kind != sigEscalated &&
		!(g.Kind == sigDecide && g.Unreachable && g.Recipient != "" && g.To == cfg.Main) {
		return s.effectiveSupervisor(ctx, cfg, t)
	}
	own := s.ownSupervisor(cfg)
	if g.Kind == sigTodoReady || g.Kind == sigParentCloseout || g.todoV2Wake() {
		// These task wakes go to the owning supervisor conversation (Leader or Main chat),
		// never to the background supervisor.
		if g.To != "" && g.To != cfg.BackgroundSupervisor {
			return g.To
		}
		return cfg.Main
	}
	if g.To != "" {
		switch {
		case (g.Kind == sigDecide || g.Kind == sigCheck) && g.To == cfg.Main:
			// A task leader's decision, or the owner's answer to a check's decision on its worker, goes to the conversation
			// above it. ownSupervisor is main for new work (#5732); only legacy queued work keeps a background route.
			return own
		case g.To == cfg.BackgroundSupervisor && g.To != own &&
			(cfg.MainSupervisesOwnTasks != nil && *cfg.MainSupervisesOwnTasks || cfg.BackgroundSupervisorMachineID != cfg.MachineID):
			return cfg.Main // a legacy card named a background supervisor that was switched off or moved
		}
		return g.To
	}
	switch g.Kind {
	case sigEscalated, sigStarted, sigLeaderStuck:
		return cfg.Main
	}
	if t, ok := s.todoNow(g.Todo); ok && t.Supervision != nil {
		sup := s.effectiveSupervisor(ctx, cfg, t)
		if sup != cfg.Main && slices.Contains(cfg.Leaders, sup) {
			if t.Supervision.state(t) == reviewNeedsUser {
				return cfg.Main
			}
			return sup
		}
		if g.Kind == sigFailed {
			// Only an explicitly assigned manual leader keeps this route. New task_plan work, including a capable-runtime
			// side chat's, still uses main's failed-start remedies (#5732).
			return cfg.Main
		}
		// The explicit capable-runtime topology is the one side-chat exception to universal main supervision.
		if sup != cfg.Main && sup == t.Origin && !s.isLeader(ctx, sup) && s.runtimeWithSupervisionPolicy(ctx, cfg.MachineID, sup) {
			return sup
		}
	}
	return cfg.Main
}

// runtimeSupervisorFallback is the existing background/main compatibility route for work whose runtime supervisor
// cannot receive policy. It is not the supervisor for ordinary new tasks: ownSupervisor deliberately keeps those on main.
func (s *Service) runtimeSupervisorFallback(ctx context.Context, cfg Settings) string {
	if id := cfg.BackgroundSupervisor; s.backgroundOn(cfg) && id != "" && id != cfg.Main &&
		cfg.BackgroundSupervisorMachineID == cfg.MachineID && !s.runtimeWithoutSupervisionPolicy(ctx, cfg.MachineID, id) {
		return id
	}
	return cfg.Main
}

// digestConversation resolves the notification recipient for immediate delivery and queue drains.
// Outside directions have no conversation. Main receives their signals without taking ownership of the task.
func (s *Service) digestConversation(ctx context.Context, cfg Settings, g superviseSignal, to string) string {
	if to == "" || to == cfg.BackgroundSupervisor {
		return to
	}
	if to != cfg.Main {
		d, _, loaded := s.directionSnapshot(ctx, to)
		if !loaded {
			return ""
		}
		if d.Outside != nil {
			return cfg.Main
		}
	}
	if t, ok := s.todoNow(g.Todo); ok && t.Supervision.assigned() && to == t.Supervision.Supervisor && to != cfg.Main && s.runtimeWithoutSupervisionPolicy(ctx, cfg.MachineID, to) {
		return "" // retain the assigned lead's work until it can receive policy; never route it to main
	}
	runtimeOrigin := false
	if t, ok := s.todoNow(g.Todo); ok {
		runtimeOrigin = s.runtimeWithoutSupervisionPolicy(ctx, cfg.MachineID, t.Origin)
	}
	if to == cfg.Main && !runtimeOrigin {
		return to
	}
	if to != cfg.Main && !s.runtimeWithoutSupervisionPolicy(ctx, cfg.MachineID, to) {
		return to
	}
	if g.Kind == sigFailed || g.Kind == sigTodoReady || g.Kind == sigParentCloseout || g.todoV2Wake() {
		return cfg.Main // the background supervisor has no start remedies and does not receive these task wakes
	}
	own := s.runtimeSupervisorFallback(ctx, cfg)
	s.logf("supervise: %s runs on a runtime that cannot take supervision policy: its signal goes to %s", short(to), short(own))
	return own
}

// digestTarget resolves the delivery conversation for a new signal.
func (s *Service) digestTarget(ctx context.Context, cfg Settings, g superviseSignal) string {
	return s.digestConversation(ctx, cfg, g, s.digestTo(ctx, cfg, g))
}

// escalatedIn: one of the entries is a leader's needs_user.
func escalatedIn(list []prompts.SuperviseEntry) bool {
	return slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind == sigEscalated })
}

// askedIn: one of the entries is a card that came to the supervisor before the user (ask_route.go).
func askedIn(list []prompts.SuperviseEntry) bool {
	return slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind == sigAsk })
}

// escalateToMain tells the main conversation that leader task t needs the user (task_review needs_user from its leader):
// why, and the option the leader recommends. The user's decision card goes out as for any needs_user.
func (s *Service) escalateToMain(t Todo, round int, why, recommend string) {
	s.signal(superviseSignal{Todo: t.ID, Kind: sigEscalated, Key: fmt.Sprintf("needs_user@%d@%s", round, time.Now().UTC().Format(time.RFC3339Nano)),
		Detail: clip(oneLine(why), 200), Ask: clip(oneLine(recommend), 200)})
}

// workerRefusal refuses task_plan and agent_start from an agent handed out as a task's agent: it does the work itself,
// with its own in-session subagents, so tasks do not spawn tasks without end. The main conversation and leaders hand
// out freely.
func (s *Service) workerRefusal(ctx context.Context, machine, self string) error {
	if self == "" || s.supervises(ctx, self) {
		return nil
	}
	if s.devs != nil {
		machine = s.devs.Canonical(machine) // as the task's agents are kept
	}
	t, ok := s.taskOf(ctx, machine, self)
	if !ok {
		return nil
	}
	return fmt.Errorf("you are the agent of task %s (%s): an agent handed out as a task does not hand out Persona tasks "+
		"(task_plan, agent_start). Do the work here, with your own in-session subagents if it splits; if it needs another "+
		"task, say so to your supervisor (report_to_supervisor)", t.ID, t.Title)
}

// ---- assistant.leaders ----

func (s *Service) getLeaders(ctx context.Context) (string, error) {
	list := s.settings(ctx).Leaders
	if len(list) == 0 {
		return "[] (none: only the main conversation supervises)", nil
	}
	raw, _ := json.Marshal(list)
	return string(raw), nil
}

// setLeaders takes the whole list of leader conversations: each an assistant conversation of the assistant's machine
// other than the main one.
func (s *Service) setLeaders(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var in []string
	if err := json.Unmarshal(v, &in); err != nil {
		return nil, errors.New("value must be a list of conversation ids ([] for none)")
	}
	return s.leadersChange(ctx, s.settings(ctx), in)
}

func (s *Service) leadersChange(ctx context.Context, cfg Settings, in []string) (*ownChange, error) {
	list, err := s.leaderList(ctx, cfg, in)
	if err != nil {
		return nil, err
	}
	before := cfg.Leaders
	if sameLeaders(list, before) {
		return &ownChange{summary: "assistant.leaders did not change"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	shown := func(l i18n.Lang, ids []string) string { return clipped(l, strings.Join(ids, ", ")) }
	// The approval compares the whole list the card was raised against with the current one, not the card's text
	// (#11392): the card clips both lists to 120 characters, three ids and a bit, so with four or more leaders the
	// owner's pin or unpin after them left the text as it was, and approving wrote the card's list over it.
	return &ownChange{applyChecksProposal: true, cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_leaders", i18n.Args{"before": shown(l, before), "after": shown(l, list)})
	},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.leaderList(ctx, s.settings(ctx), in); err != nil {
				return "", fmt.Errorf("%w: validate the approved operation against current state: %v", approvalops.ErrNotApplied, err)
			}
			if _, err := s.updateSettings(ctx, func(v *Settings) error {
				if !sameLeaders(v.Leaders, before) {
					return fmt.Errorf("assistant.leaders changed after the card was shown, so nothing was saved: get it and set it again for a new card (%w)", approvalops.ErrStale)
				}
				v.Leaders = list
				s.directionRolesChanged(ctx, before, list)
				return nil
			}); err != nil {
				return "", err
			}
			// A lead taken out ends its direction as Unpin does: its open tasks go to main's supervision.
			handed := 0
			for _, id := range before {
				if !slices.Contains(list, id) {
					handed += s.handBackDirectionTasks(context.WithoutCancel(ctx), id)
				}
			}
			s.settingsChanged(ctx)
			out := fmt.Sprintf("assistant.leaders is %q: a manually started Agent the owner explicitly puts under one keeps its ordinary "+
				"supervision there. A direction lead supervises its own task_plan work, including failed starts, retry and review; "+
				"main supervises its own tasks and receives the lead's owner-decision escalations.", list)
			if handed > 0 {
				out += fmt.Sprintf(" The %d open tasks of the leads taken out are supervised by the main conversation now, as "+
					"agent_transfer moves a task; making a lead again does not take them back.", handed)
			}
			return out, nil
		}}, nil
}

// leaderList is in as assistant.leaders would keep it with settings cfg: trimmed, without blanks or repeats, each a
// Persona conversation on the assistant's machine other than the main one and the background supervisor, at most
// leadersMost.
func (s *Service) leaderList(ctx context.Context, cfg Settings, in []string) ([]string, error) {
	list := []string{}
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || slices.Contains(list, id) {
			continue
		}
		if id == cfg.Main {
			return nil, errors.New("the main conversation supervises already: a leader is a side conversation")
		}
		if id == cfg.BackgroundSupervisor {
			return nil, errors.New("the background supervisor is built in and follows up the tasks no leader supervises: a leader is another side conversation")
		}
		if d := s.directionOf(id); d != nil && d.Outside != nil {
			list = append(list, id) // a created conversationless direction survives whole-list edits
			continue
		}
		if c, ok := s.session(ctx, cfg.MachineID, id); !ok || !c.Assistant {
			return nil, fmt.Errorf("%s is not a Persona conversation on the assistant's machine (conversations_list has their ids)", id)
		}
		list = append(list, id)
	}
	if len(list) > leadersMost {
		return nil, fmt.Errorf("at most %d leader conversations", leadersMost)
	}
	return list, nil
}

// sameLeaders: lists a and b name the same leaders in the same order (nil and [] are both none).
func sameLeaders(a, b []string) bool {
	return slices.Equal(a, b) || len(a) == 0 && len(b) == 0
}

// endSessionCards ends the cards machine/session still waits on (cancelled in the history), as when its node goes away.
// A caller that inspected particular cards before stopping the session can limit cancellation to those ids.
func (s *Service) endSessionCards(machine, session string, only ...string) {
	s.fleet.mu.Lock()
	var ids []string
	for id, p := range s.fleet.interactions {
		if len(only) > 0 && !slices.Contains(only, id) {
			continue
		}
		if p.ix.SessionID == session && s.sameMachine(p.ix.MachineID, machine) && (p.prepared != nil || !p.claimed) {
			if p.prepared == nil {
				p.claimed = true
			}
			ids = append(ids, id)
		}
	}
	s.fleet.mu.Unlock()
	s.larkModule().AskDetached(ids, resumeGrace)
	for _, id := range ids {
		if s.endPreparedByID(id, "cancelled", "The conversation that owned this approval ended; nothing changed.") {
			continue
		}
		s.recordEnd(id, "cancelled", nil)
		s.finishInteraction(id)
	}
}
