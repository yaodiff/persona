package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// Asks go to the supervisor first (docs/assistant/unified-entry.md "Asks go to the supervisor first"). A card a
// supervised task's agent raises — a question, a tool approval, a parsed terminal dialog, or (with assistant.leaders
// configured) a plan or owner-only approval — goes to the conversation supervising the task, not to the user: the apps
// show it marked delegating, nothing rings, and the supervisor gets it as a supervision entry. The supervisor owns its
// agents' cards (the decision rights of 2026-11-23: the user decides only the goal, what only they know, what goes out
// in their name, spending, what cannot be undone, and what only they can do). It answers a question itself
// (interaction_answer; the answer becomes the task's decision), and allows or denies a tool permission or a plan itself
// too (interaction_answer with allow; answeredApproval): the agent gets it marked as the assistant's answer
// (AnsweredBy), never as the user's. Allowing what stays the user's — a card with a form or a secret, or an approval
// that would send, reply or comment in the user's name, spend money or delete (userApproval) — needs ask_escalate:
// every card of the task still waiting then rings the user once, with the supervisor's reason and recommendation, and a
// leader's escalation is an escalated entry in the main conversation, so the layers above know. Denying any permission
// or plan remains the supervisor's: denial executes nothing and returns its reason to the agent. Without leaders
// configured (assistant.leaders empty), six-class owner cards and plans stay with the user directly while non-six-class
// agent permissions and questions and parsed terminal dialogs still take the active supervision route on the task's
// supervisor (#16291).
//
// Nothing reaches the user on a timeout. A card nobody handles within askHoldFor moves one rung up (leader, then the
// main conversation; one rung, the background supervisor or the main conversation, for a task no leader supervises),
// and a permission or plan moves up at once when the conversation holding it itself waits for the user. Past its last
// rung a card goes back to its task's supervisor and stays there (parked): the supervisor is reminded of it each
// askHoldFor, at most askRemindMax times, the card stays on the apps marked as the supervisor's, and only ask_escalate
// hands it to the user. A parsed terminal dialog (supervisedScreen: an ordinary choice, or a command approval Claude
// Code asks on its own screen) stays with its supervising conversation, which picks its option itself
// (interaction_answer; answeredScreen) unless it is the user's (userScreen), or escalates it explicitly.
//
// The route lives on the pending card, not in the store: after a manager restart the card is back on the apps without
// a push (as every card is), and its route is made again from the task's supervisor (restoreRoute) so that the
// supervisor can still answer or escalate it.

// askHoldFor is how long a card stays with one rung of its ladder before it moves up. The time does not count while
// that rung's conversation is on a turn (askTick): a supervisor working through a long round has not neglected the
// card, and a longer fixed hold would run out under a longer turn all the same.
var askHoldFor = 5 * time.Minute

// askBusyMax bounds that: a rung whose conversation is still on a turn this long after the card came is stuck rather
// than working through it, and the card's hold runs again (askTick), so a conversation that never comes back cannot
// keep a card for ever.
var askBusyMax = time.Hour

// askRemindMax bounds how often a card parked with its task's supervisor (past its last rung) reminds that supervisor,
// once each askHoldFor: after that it only waits there, on the apps as the supervisor's and in its supervision
// entries, so an agent is not left stuck unseen and a supervisor is not reminded for ever.
var askRemindMax = 3

// askRoute is where a pending card is on its way to the user: the conversations it goes through, nearest first (its
// task's leader, then the main conversation), the one it is with (step; past the last: the user's), and since when.
type askRoute struct {
	todo     string
	machine  string // where its rungs are: the assistant's conversations, on the assistant's machine
	ladder   []string
	step     int
	routedAt time.Time // when this route began
	since    time.Time
	held     time.Time // when it reached this rung: askBusyMax counts from here
	// maybeTold: a question card restored after a restart. Whether the user was told of it is not kept, so should it
	// reach the user (an escalation) without a supervisor's context, it only goes back on the apps (ringAsks).
	maybeTold       bool
	delivered       bool      // this rung has actually received the card, rather than merely queued its signal
	standaloneUntil time.Time // when a permission with no open to-do becomes answerable in the app
	// A card that runs past its last rung goes back to its task's supervisor (reminded) and stays there (parked) until
	// someone answers or escalates it, reminding the supervisor each askHoldFor, reminders times so far, at most
	// askRemindMax. None of it is kept over a restart: a restored route starts again from the supervisor.
	reminded, parked bool
	reminders        int
}

func (r *askRoute) at() string {
	if r == nil || r.step >= len(r.ladder) {
		return ""
	}
	return r.ladder[r.step]
}

// escalationBook keeps which cards the user got through an escalation, so their answers become the task's decisions.
type escalationBook struct {
	escalated map[string]escalatedCard // by interaction id
}

type escalatedCard struct {
	todo string
	at   time.Time
}

// maxDialogChoices is how many of a terminal dialog's choices its text names (the bot's card shows as many buttons).
const maxDialogChoices = 8

// escalatedKeep bounds how long an escalated card is remembered unanswered (it expired, was cancelled or its node went).
const escalatedKeep = 48 * time.Hour

// terminalQuestion is a parsed ordinary choice: a question with plain options and nothing about a command, a warning
// or a countdown (the bot's and the main conversation's wording of it). Never forward the raw screen, typed values or
// secrets to the model.
func terminalQuestion(ix proto.Interaction) bool {
	d := ix.Dialog
	if !supervisedScreen(ix) || d.Command != "" || d.Warning != "" || d.Risk != "" || d.Deadline != "" || d.DeadlineAction != "" {
		return false
	}
	return userScreen(ix) == ""
}

// supervisedScreen is a terminal dialog of an agent its task's supervisor decides on, as it decides on the agent's
// tool permissions (#13752): a parsed dialog with a question and options to pick, a command approval Claude Code asks
// on its own screen (a safety check its hooks never see, "Dangerous rmdir operation…") among them. Unparsed screens,
// typed values, secrets and forms are not: the supervisor could not read them, so they stay with the user.
func supervisedScreen(ix proto.Interaction) bool {
	if !safeScreenDialog(ix) {
		return false
	}
	return slices.ContainsFunc(ix.Dialog.Options, func(o screen.Option) bool { return o.Kind == "" && o.Label != "" })
}

func safeScreenDialog(ix proto.Interaction) bool {
	return ix.Kind == proto.IxScreen && !ix.Private && !ix.Secret && len(ix.Fields) == 0 &&
		ix.Dialog != nil && strings.TrimSpace(ix.Dialog.Question) != ""
}

// userScreen says why supervised terminal dialog ix is the user's to decide, not its supervisor's ("" when the
// supervisor may pick its option): Claude's startup trust, external imports and Bypass Permissions confirmations,
// and a command that would send, reply or comment in the user's name, spend money or delete (userApprovalWords).
// The supervisor hands those to the user with ask_escalate.
func userScreen(ix proto.Interaction) string {
	d := ix.Dialog
	if d == nil {
		return "is not a dialog a supervisor can read"
	}
	for _, o := range d.Options {
		label := strings.ToLower(strings.TrimSpace(o.Label))
		trust := label == "yes, i trust this folder" || label == "yes, allow external imports"
		bypass := label == "yes, i accept" && strings.Contains(d.Detail+" "+d.Question, "Bypass Permissions mode")
		if trust || bypass {
			return "authorizes the agent's access, which only the user grants"
		}
	}
	if d.Risk == screen.RiskDelete {
		return "may delete files (" + clip(oneLine(d.Warning), 80) + ")"
	}
	text := strings.ToLower(strings.Join([]string{d.Question, d.Command, d.Description}, "\n"))
	var hit []string
	for _, w := range userApprovalWords {
		if strings.Contains(text, w) {
			hit = append(hit, w)
		}
	}
	if len(hit) > 0 {
		return fmt.Sprintf("would send, reply or comment in the user's name, spend money or delete (%s)", strings.Join(hit, ", "))
	}
	return ""
}

// terminalQuestionText is the question and its choice labels, as plain text in the
// supervisor's conversation and the bot's text fallback, with the command it asks about and the agent's warning when
// it has them. A free-text row may contain what the user is typing, so only selectable labels are quoted.
func terminalQuestionText(ix proto.Interaction) string {
	d := ix.Dialog
	choices := make([]string, 0, len(d.Options))
	for _, o := range d.Options {
		if o.Kind == "" && o.Label != "" {
			choices = append(choices, clip(oneLine(o.Label), 60))
			if len(choices) == maxDialogChoices {
				break
			}
		}
	}
	question := clip(oneLine(d.Question), 300)
	if d.Command != "" {
		question += " · $ " + clip(oneLine(d.Command), 300)
	}
	if d.Warning != "" {
		question += " · ⚠ " + clip(oneLine(d.Warning), 160)
	}
	if len(choices) == 0 {
		return question
	}
	return question + " · " + strings.Join(choices, " / ")
}

// routable: ix is a card a supervisor can decide on or pass up. Unparsed
// terminal screens, forms, secrets and the assistant's own questions stay with the user.
func routable(ix proto.Interaction) bool {
	if len(ix.Fields) > 0 {
		return false
	}
	switch ix.Kind {
	case proto.IxQuestion:
		return len(ix.Questions) > 0 && (ix.ToolName != "ask_user" || questionOwnerSource(ix) == "") && asksForInput(ix)
	case proto.IxPermission, proto.IxPlan:
		return true
	case proto.IxScreen:
		return supervisedScreen(ix)
	}
	return false
}

// supervisorConversation identifies supervising sessions. Their ordinary internal
// cards go to an independent reviewer through handNonOwnerCard, never back to
// themselves. Owner-only cards retain their explicit owner path.
func (v Settings) supervisorConversation(session string) bool {
	return session != "" && (session == v.Main || session == v.BackgroundSupervisor || slices.Contains(v.Leaders, session))
}

// noLeaderAskRoutable reports whether ix should route through askLadder when assistant.leaders is empty (#16291).
// Parsed terminal dialogs keep their existing route, and non-six-class agent permissions and questions on open
// supervised tasks take the active supervision route (sigAsk + askTick) instead of falling through to
// handNonOwnerCard's passive keepRelayLater queue. Six-class owner cards (secrets/forms, outward writes,
// userApproval matches, manager/confirm_outbound cards) and plans stay out of askLadder when no leader is configured.
func (s *Service) noLeaderAskRoutable(ix proto.Interaction) bool {
	switch ix.Kind {
	case proto.IxScreen:
		return true
	case proto.IxPermission:
		return s.inputCardSource(ix, false) == cardAgentPermission && !secretOrForm(ix) && userApproval(ix) == ""
	case proto.IxQuestion:
		return s.inputCardSource(ix, false) == cardAgentQuestion && !secretOrForm(ix)
	default:
		return false
	}
}

// askLadder is where a card of machine/sid's goes first: the conversations supervising its task, nearest first, the
// task, and the machine those conversations are on (the assistant's: its leaders and its main conversation). None when
// the agent is no supervised task's, the task already waits for the user, or the agent is a supervising conversation
// (supervisorConversation). Without configured leaders, parsed terminal dialogs and non-six-class agent permissions and
// questions still route to the task's supervisor (noLeaderAskRoutable), while six-class owner cards and plans stay with
// the user.
//
// A task no leader supervises has one rung: the task's own supervisor (ownSupervisor, supervise_background.go — the
// main conversation).
func (s *Service) askLadder(ctx context.Context, ix proto.Interaction) (ladder []string, todo, home string, ok bool) {
	machine, sid := ix.MachineID, ix.SessionID
	cfg := s.settings(ctx)
	if cfg.Main == "" || sid == "" {
		return nil, "", "", false
	}
	if cfg.supervisorConversation(sid) {
		if !routable(ix) || !s.internalCard(ix) {
			return nil, "", "", false
		}
		to, home := s.internalReviewer(ctx, ix)
		if to == "" || home == "" {
			return nil, "", "", false
		}
		return []string{to}, "", home, true
	}
	if len(cfg.Leaders) == 0 && !s.noLeaderAskRoutable(ix) {
		return nil, "", "", false
	}
	home = cfg.MachineID
	if s.devs != nil {
		machine = s.devs.Canonical(machine) // as the task's agents are kept
		home = s.devs.Canonical(home)       // and as the session lists the apps see are (busyLocked reads stateSince)
	}
	t, found := s.taskOf(ctx, machine, sid)
	if !found || t.Supervision == nil {
		return nil, "", "", false
	}
	switch t.Supervision.state(t) {
	case reviewAccepted, reviewCancelled:
		return nil, "", "", false
	}
	if t.Supervision.state(t) == reviewNeedsUser && !t.Supervision.assigned() {
		return nil, "", "", false
	}
	lead := s.effectiveSupervisor(ctx, cfg, t)
	if s.agentSupervised(t) {
		if ix.Kind != proto.IxScreen {
			return nil, "", "", false
		}
		lead, _ = s.supervisorAboveTask(ctx, t)
	}
	if ix.Kind == proto.IxScreen {
		if s.ixPolicy(ctx, ix).ConfirmOutbound || s.todoPolicy(ctx, t).ConfirmOutbound {
			return nil, "", "", false
		}
		if lead == "" || lead == cfg.Main {
			lead = s.ownSupervisor(cfg)
		}
		// Side conversations supervise their own work too. Resolve the same
		// destination as a supervision message when a runtime cannot receive it.
		to := s.digestTarget(ctx, cfg, superviseSignal{Todo: t.ID, Kind: sigAsk, To: lead})
		return []string{to}, t.ID, home, to != ""
	}
	if lead != cfg.Main && slices.Contains(cfg.Leaders, lead) {
		if t.Supervision.assigned() {
			return []string{lead}, t.ID, home, true // only owner-decision escalations go to main
		}
		return []string{lead, cfg.Main}, t.ID, home, true
	}
	return []string{s.ownSupervisor(cfg)}, t.ID, home, true
}

// routeAsk sends new card ix to its supervisor instead of the user (announce); false when it is not one to route.
func (s *Service) routeAsk(ix proto.Interaction) bool {
	if s.activeInternalCard(ix) {
		return s.handNonOwnerCard(ix)
	}
	if !routable(ix) {
		return false
	}
	s.supervise.routingMu.Lock()
	defer s.supervise.routingMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ladder, todo, home, ok := s.askLadder(ctx, ix)
	if !ok {
		return false
	}
	s.fleet.mu.Lock()
	p := s.fleet.interactions[ix.ID]
	if p == nil || p.claimed || p.told || !p.ix.CreatedAt.Equal(ix.CreatedAt) {
		s.fleet.mu.Unlock()
		return false
	}
	now := time.Now()
	p.route = &askRoute{todo: todo, machine: home, ladder: ladder, routedAt: now, since: now, held: now}
	p.ix.Delegating = true
	s.fleet.mu.Unlock()
	shown := ix
	shown.Delegating = true
	s.show(shown)
	s.workEvent(ix.MachineID, ix.SessionID, "ask_routed", map[string]any{"to": ladder[0], "kind": ix.Kind, "todo": todo, "id": ix.ID})
	s.signalAsk(todo, ladder[0], ix, 0)
	return true
}

// signalAsk tells conversation to that task todo's card ix (step: the rung it reached) waits for it.
func (s *Service) signalAsk(todo, to string, ix proto.Interaction, step int) {
	if s.askAlreadyQueued(todo, to) {
		return
	}
	s.signal(superviseSignal{Todo: todo, Kind: sigAsk, To: to, Machine: ix.MachineID, Session: ix.SessionID,
		Key: fmt.Sprintf("%s@%d", ix.ID, step)})
}

// quietRemind is the timeout of card ix of task todo past its last rung: instead of ringing the user, supervisor to
// gets the card once more (the nth time). Only the work event and supervisor signal
// are emitted: an internal reminder never constructs an owner notification.
func (s *Service) quietRemind(todo, to string, ix proto.Interaction, nth int) {
	s.logf("ask %s of task %s: not handled in time; not pushed to the user, %s reminded (%d/%d)", ix.ID, todo, to, nth, askRemindMax)
	s.workEvent(ix.MachineID, ix.SessionID, "ask_quiet_reminded", map[string]any{"to": to, "kind": ix.Kind, "todo": todo, "id": ix.ID, "nth": nth})
	key := ix.ID + "@remind"
	if nth > 1 {
		key += strconv.Itoa(nth)
	}
	s.signal(superviseSignal{Todo: todo, Kind: sigAsk, To: to, Machine: ix.MachineID, Session: ix.SessionID, Key: key})
}

// heldAsks are the cards of task todo waiting with conversation to, oldest first, as a supervision entry lists them.
func (s *Service) heldAsks(todo, to string) []prompts.SuperviseAsk {
	type held struct {
		ix       proto.Interaction
		step     int
		reminded bool
	}
	var list []held
	s.fleet.mu.Lock()
	for _, p := range s.fleet.interactions {
		if r := p.route; r != nil && !p.claimed && r.todo == todo && r.at() == to {
			list = append(list, held{p.ix, r.step, r.reminded})
		}
	}
	s.fleet.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].ix.CreatedAt.Before(list[j].ix.CreatedAt) })
	out := make([]prompts.SuperviseAsk, 0, len(list))
	for _, h := range list {
		out = append(out, prompts.SuperviseAsk{ID: h.ix.ID, Kind: h.ix.Kind, Machine: s.machineName(h.ix.MachineID),
			Session: short(h.ix.SessionID), Text: noticeAsk(h.ix), Raised: h.step > 0, Reminded: h.reminded})
	}
	return out
}

// routedSessions are the agents with a card on its way up: their needs_you is the card's entry already.
func (s *Service) routedSessions() map[[2]string]bool {
	out := map[[2]string]bool{}
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	for _, p := range s.fleet.interactions {
		if p.route != nil && !p.claimed {
			out[[2]string{p.ix.MachineID, p.ix.SessionID}] = true
			if s.devs != nil { // the watch has the task's agents under their canonical machine
				out[[2]string{s.devs.Canonical(p.ix.MachineID), p.ix.SessionID}] = true
			}
		}
	}
	return out
}

// routedTo: pending card id is on its way up and with conversation self now, or with one below it (the main
// conversation answering what its leader still holds); its task. main: self is the main conversation, which may
// answer any card still on its way up, the background supervisor's too (its ladder does not pass the main conversation).
func (s *Service) routedTo(id, self string, main bool) (string, bool) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	p := s.fleet.interactions[id]
	if p == nil || p.route == nil || p.claimed || self == p.ix.SessionID || p.route.step >= len(p.route.ladder) || !main && !slices.Contains(p.route.ladder[p.route.step:], self) {
		return "", false
	}
	return p.route.todo, true
}

// askTick moves each card held past askHoldFor one rung up, to the next conversation. The time a card has spent with
// a conversation on a turn does not count (busyLocked): it is being worked through, not waiting to be looked at. A
// conversation still on a turn askBusyMax after the card came is stuck instead, and its time counts again. A permission
// or plan moves up at once when the conversation holding it itself needs the user (needsYouLocked) and there is a rung
// above it.
//
// A task's card goes back to its supervisor past the last rung. It stays there, parked, reminding the supervisor
// each askHoldFor up to askRemindMax times; only ask_escalate hands it to the user. A route with no rung at all
// has no supervisor to hold it. A standalone permission has no task to escalate and becomes answerable in the app
// if the main conversation has not handled it within askHoldFor.
func (s *Service) askTick(now time.Time) {
	type up struct {
		todo, to string
		ix       proto.Interaction
		step     int
		reason   string
	}
	var ups, reminds []up
	var standalone []proto.Interaction
	toUser := map[string][]string{}
	s.fleet.mu.Lock()
	for id, p := range s.fleet.interactions {
		r := p.route
		if r == nil || p.claimed || p.ix.Kind == proto.IxScreen {
			continue // A terminal question stays with its supervisor until an explicit escalation.
		}
		if !r.standaloneUntil.IsZero() {
			if !now.Before(r.standaloneUntil) && now.Before(p.ix.ExpiresAt) {
				p.route = nil
				p.ix.Delegating = false
				standalone = append(standalone, p.ix)
			}
			continue
		}
		if len(r.ladder) == 0 {
			toUser[r.todo] = append(toUser[r.todo], id)
			continue
		}
		ownerOnly := p.ix.Kind == proto.IxPermission || p.ix.Kind == proto.IxPlan
		release := ownerOnly && !r.parked && r.step+1 < len(r.ladder) && r.needsYouLocked(s)
		if !release {
			if r.busyLocked(s) && now.Sub(r.held) < askBusyMax {
				r.since = now // its conversation is on a turn: the hold starts again once the turn ends
				continue
			}
			if now.Sub(r.since) < askHoldFor {
				continue
			}
		}
		if r.parked {
			r.since, r.held = now, now
			if r.reminders < askRemindMax {
				r.reminders++
				reminds = append(reminds, up{r.todo, r.at(), p.ix, r.reminders, ""})
			}
			continue
		}
		r.step++
		r.since, r.held = now, now
		if to := r.at(); to != "" {
			reason := fmt.Sprintf("not handled in %v", askHoldFor)
			if release {
				reason = "its supervisor needs the user"
			}
			ups = append(ups, up{r.todo, to, p.ix, r.step, reason})
			continue
		}
		// Past its last rung: back to the task's supervisor, not the user, and kept there.
		r.step, r.reminded, r.parked, r.reminders = 0, true, true, 1
		reminds = append(reminds, up{r.todo, r.ladder[0], p.ix, 1, ""})
	}
	s.fleet.mu.Unlock()
	for _, u := range ups {
		s.logf("ask %s: %s, up to %s", u.ix.ID, u.reason, u.to)
		s.signalAsk(u.todo, u.to, u.ix, u.step)
	}
	for _, u := range reminds {
		s.quietRemind(u.todo, u.to, u.ix, u.step)
	}
	for todo, ids := range toUser {
		s.logf("asks %v of task %s: no supervisor holds them, to the user", ids, todo)
		s.ringAsks(todo, ids, nil, escalation{})
	}
	for _, ix := range standalone {
		s.show(ix) // Restore the app controls without sending an alert to another surface.
	}
}

// needsYouLocked: the conversation holding the card needs the user itself, so it cannot handle an owner-only card.
// s.fleet.mu is held.
func (r *askRoute) needsYouLocked(s *Service) bool {
	to := r.at()
	if to == "" || r.machine == "" {
		return false
	}
	mk, ok := s.fleet.stateSince[[2]string{r.machine, to}]
	return ok && mk.state == proto.StateNeedsYou
}

// busyLocked: the conversation the card waits with is on a turn now, as the state the apps show it says: it is working
// through the card (or whatever else), so its hold does not run out meanwhile. s.fleet.mu is held.
func (r *askRoute) busyLocked(s *Service) bool {
	to := r.at()
	if to == "" || r.machine == "" {
		return false
	}
	mk, ok := s.fleet.stateSince[[2]string{r.machine, to}]
	return ok && mk.state == proto.StateWorking
}

// ringAsks tells the user of task todo's cards ids, still pending and not told yet, as one: the apps get each card
// (with the supervisor's context d, when it gave some) and the channels each card to answer, but only the first one
// pushes. Cards that come later are not folded into it: they go to the supervisor as any. d nil is a card without a
// supervisor's context (a route with no rung, askTick): a quiet task's cards then only go on the apps, as they did
// before routing (main_tasks_quiet: never pushed unless the supervisor says so), and so does a question restored after
// a restart, which may have rung the user before it. The cards rung.
//
// esc is an escalation's push level (escalateLevel): its push carries it, and a P1 one in low-distraction mode (held) is put on the
// apps and given to the hub as a P1 push only, which the hub holds for the "Needs you" list and the briefing: no alert,
// no Feishu card, no channel message.
func (s *Service) ringAsks(todo string, ids []string, d *proto.Delegate, esc escalation) []proto.Interaction {
	var list []proto.Interaction
	maybeTold := map[string]bool{}
	s.fleet.mu.Lock()
	for _, id := range ids {
		p := s.fleet.interactions[id]
		if p == nil || p.claimed || p.told {
			continue
		}
		if p.route != nil && p.route.maybeTold {
			maybeTold[id] = true
		}
		p.route = nil
		p.ix.Delegating = false
		if esc.level != "" {
			p.ix.PushLevel = esc.level
		}
		if d != nil {
			c := *d
			p.ix.Delegate = &c
		}
		list = append(list, p.ix)
	}
	if len(list) > 0 && s.askUp.escalated == nil {
		s.askUp.escalated = map[string]escalatedCard{}
	}
	now := time.Now()
	for id, c := range s.askUp.escalated {
		if now.Sub(c.at) > escalatedKeep {
			delete(s.askUp.escalated, id)
		}
	}
	for _, ix := range list {
		s.askUp.escalated[ix.ID] = escalatedCard{todo: todo, at: now}
	}
	s.fleet.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	pushed := false
	for _, ix := range list {
		if d != nil {
			ix = s.suggestOnCard(ix) // a card a supervisor passed up may offer a standing rule (rule_suggest.go)
		}
		ix = s.tellApps(ix)
		if d == nil && (maybeTold[ix.ID] || s.quietSession(ix.MachineID, ix.SessionID)) {
			continue // a question that may have rung before, or a quiet task's: the apps, no second push
		}
		switch {
		case esc.held:
			s.recordConfirmationNotification(ix)
			s.heldAnnounced(ix.ID) // no Feishu card or channel message, now or when the channels catch up
			if !pushed {
				s.notifyInteractionAt(ix, s.machineName(ix.MachineID), false, esc.level, esc.key)
			}
		case esc.level != "":
			s.recordConfirmationNotification(ix)
			if !pushed && !s.muted(ix.MachineID, ix.SessionID) {
				s.notifyInteractionAt(ix, s.machineName(ix.MachineID), s.linkCovers(ix), esc.level, esc.key)
				go s.threadAgentAsks(ix)
			}
			s.InteractionAnnounced(context.Background(), InteractionAnnounced{Interaction: ix})
		default:
			s.tellUser(ix, !pushed)
		}
		pushed = true
	}
	return list
}

// waitingCards are the pending cards of task t's agents the user has not been told of: held by a supervisor, or put
// back on the apps after a restart without a route (its task is not supervised any more).
func (s *Service) waitingCards(t Todo, terminalOnly bool) []string {
	agents := map[[2]string]bool{}
	for _, a := range t.Agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil {
			agents[[2]string{a.MachineID, a.SessionID}] = true
		}
	}
	var ids []string
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	for id, p := range s.fleet.interactions {
		if terminalOnly && p.ix.Kind != proto.IxScreen || p.ix.Kind == proto.IxScreen && p.route == nil {
			continue
		}
		if p.claimed || p.told || !routable(p.ix) || p.route == nil && p.ix.Delegating {
			continue // the user's already, or the assistant deciding on it for them (fleet_delegate.go): it tells the user itself
		}
		mid := p.ix.MachineID
		if s.devs != nil {
			mid = s.devs.Canonical(mid)
		}
		if p.route != nil && p.route.todo == t.ID || agents[[2]string{mid, p.ix.SessionID}] {
			ids = append(ids, id)
		}
	}
	return ids
}

// ---- ask_escalate (prompts/mcp/agents) ----

// escalation is how an escalated card rings (ringAsks): its push level and related key, and whether low-distraction mode holds it
// back from the phone. The zero value is a timeout's: the level the hub derives, as before.
type escalation struct {
	level, key string
	held       bool
}

// quietPushWhys are the why_owner values whose escalation still pushes in low-distraction mode: what cannot be undone, what other
// people will see, and what only the owner knows. priority and acceptance wait for the "Needs you" list and the briefing.
var quietPushWhys = []string{whyIrreversible, whyVisible, whyOnlyOwnerKnow}

// escalateLevel is the push level of an escalation that says why the owner must decide (why_owner) and whether the
// supervisor marked it P0: P0 for one of quietPushWhys or the mark, else P1. With low-distraction mode off both push as before.
func escalateLevel(why string, p0 bool) string {
	if p0 || slices.Contains(quietPushWhys, why) {
		return assistantwire.LevelP0
	}
	return assistantwire.LevelP1
}

// toolAskEscalate hands the cards task todo_id's agents wait on to the user, as one, with why and what the
// supervisor recommends; only the task's supervisor and the main conversation may.
func (s *Service) toolAskEscalate(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		TodoID    string `json:"todo_id"`
		Reason    string `json:"reason"`
		Recommend string `json:"recommend"`
		WhyOwner  string `json:"why_owner"`
		P0        bool   `json:"p0"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	a.WhyOwner = strings.TrimSpace(a.WhyOwner)
	if a.WhyOwner != "" && !slices.Contains(whyOwners, a.WhyOwner) {
		return "", fmt.Errorf("why_owner is one of %s", strings.Join(whyOwners, ", "))
	}
	reason, recommend := clip(oneLine(a.Reason), 120), clip(oneLine(a.Recommend), 120)
	if reason == "" {
		return "", errors.New("reason is required: in a sentence, what the user is to decide")
	}
	if s.ensureTodosLoaded(ctx) != nil {
		return "", errTodosUnavailable
	}
	t, ok := s.todoNow(strings.TrimSpace(a.TodoID))
	if !ok || t.DoneAt != nil || t.Supervision == nil {
		return "", fmt.Errorf("no open supervised task %s", a.TodoID)
	}
	terminalOnly := false
	main := s.isMain(ctx, self)
	if !main && t.Supervision.Supervisor != self && !s.backgroundSupervises(ctx, self, t) {
		above, _ := s.supervisorAboveTask(ctx, t)
		if above == "" || above != self {
			return "", fmt.Errorf("task %s is not yours to supervise: its supervisor or the main conversation escalates its cards", t.ID)
		}
		terminalOnly = true // The conversation above a worker's leader can escalate only its routed terminal question.
	}
	ids := s.waitingCards(t, terminalOnly)
	if len(ids) == 0 {
		return "", fmt.Errorf("task %s has no card waiting that the user has not been told of", t.ID)
	}
	esc := escalation{level: escalateLevel(a.WhyOwner, a.P0)}
	esc.held = esc.level != assistantwire.LevelP0 && s.quietModeOn(ctx)
	rung := s.ringAsks(t.ID, ids, &proto.Delegate{Reason: reason, Recommend: recommend}, esc)
	if len(rung) == 0 {
		return "", fmt.Errorf("task %s has no card waiting any more", t.ID)
	}
	if esc.held {
		s.logf("ask_escalate of task %s by %s: low-distraction mode, why_owner %q: %d card(s) to the Needs you list, not pushed", t.ID, self, a.WhyOwner, len(rung))
	}
	cards := make([]prompts.SuperviseAsk, 0, len(rung))
	for _, ix := range rung {
		cards = append(cards, prompts.SuperviseAsk{ID: ix.ID, Kind: ix.Kind, Machine: s.machineName(ix.MachineID),
			Session: short(ix.SessionID), Text: noticeAsk(ix)})
	}
	for _, ix := range rung {
		// card, kind and recommend: what a suggested rule counts the owner's answer against (rule_suggest.go).
		s.workEvent(ix.MachineID, ix.SessionID, "ask_escalated", map[string]any{"by": self, "todo": t.ID, "card": ix.ID, "kind": ix.Kind, "recommend": recommend,
			"why_owner": a.WhyOwner, "level": esc.level, "held": esc.held})
	}
	if !main {
		// The main conversation knows the user was asked, and what this leader recommends: it does not ask again.
		cfg := s.settings(ctx)
		s.signal(superviseSignal{Todo: t.ID, Kind: sigEscalated, To: cfg.Main, Key: "ask@" + rung[0].ID,
			Detail: reason, Ask: recommend, Cards: cards})
	}
	if esc.held {
		return fmt.Sprintf("Low-distraction mode is on and this is not the user's alone (why_owner %q, no p0): %d card(s) of task %s "+
			"went to their Needs you list and briefing without a push, with your reason and recommendation. Their answers go to the "+
			"agents and are kept as the task's decisions; don't ask them the same again. These cards cannot be escalated again: "+
			"next time, when the user must act on it themselves now, escalate with p0 or the right why_owner from the start.",
			a.WhyOwner, len(rung), t.ID), nil
	}
	return fmt.Sprintf("The user is asked now: %d card(s) of task %s went to them as one, with your reason and "+
		"recommendation. Their answers go to the agents and are kept as the task's decisions; don't ask them the same again.", len(rung), t.ID), nil
}

// ---- the answers, as the task's decisions ----

// askAnswered keeps a supervisor's answer to a card of task todo (interaction_answer on a routed card) as the
// task's decision: the agent's call, not the user's, made by conversation self.
func (s *Service) askAnswered(ctx context.Context, todo, self string, ix proto.Interaction, answers map[string]string, reason string) {
	st := s.todoDB()
	if st == nil || todo == "" {
		return
	}
	at := decisionNow()
	wrote := false
	for i, q := range ix.Questions {
		c := decisionText(answers[q.Question])
		if c == "" {
			continue
		}
		ok, err := s.insertDecision(ctx, st, db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: todo,
			Question: decisionText(q.Question), Choice: c, Reason: decisionText(reason), Source: db.DecisionAgent,
			SourceSession: self, SourceMessage: ix.ID, CreatedAt: at.Add(time.Duration(i) * time.Microsecond)})
		if err != nil {
			s.logf("decisions: todo %s, card %s: %v", todo, ix.ID, err)
			return
		}
		wrote = wrote || ok
	}
	if wrote {
		s.decisionsChanged(ctx, todo, at)
	}
}

// userApprovalWords are the riskyWords (agents_work_turns.go) that make an approval the user's own rather than its
// supervisor's: what goes out in their name (a message, a reply, a comment), spending money, and deleting. Pushing,
// merging and deploying are not among them: those are the supervisor's within the task the user asked for.
var userApprovalWords = []string{"发消息", "回复", "评论", "comment", "send", "发送", "删除", "delete", "购买", "付费", "续费", "充值"} // i18n-ignore: matched against the card

// userApprovalActionVerbs are tool-name verbs, normalized by connector.WriteVerb, that delete or spend even when the
// payload has no risk word. Outward verbs use the connector gate's existing list (outboundVerbs in card_layers.go).
var userApprovalActionVerbs = []string{"delete", "remove", "destroy", "erase", "purge", "trash", "buy", "purchase", "pay", "charge"}

// localFileEditPermission reports whether ix's payload is local file data. Antigravity's ACP permission
// callback passes the display title to mapToolCall, so a local write arrives as "File <path>" with the
// original raw input instead of map.js's canonical Write/Edit name. Recognize that display shape only
// with a known string write/edit field; a path-like title alone never suppresses action classification.
func localFileEditPermission(ix proto.Interaction) bool {
	switch ix.ToolName {
	case "Write", "Edit", "MultiEdit", "NotebookEdit", "apply_patch":
		return true
	}
	path, ok := strings.CutPrefix(ix.ToolName, "File ")
	if !ok || strings.TrimSpace(path) == "" {
		return false
	}
	var input map[string]json.RawMessage
	if json.Unmarshal(ix.ToolInput, &input) != nil {
		return false
	}
	for _, key := range [...]string{
		"CodeContent", "Content", "content",
		"TargetContent", "Target", "old_string",
		"ReplacementContent", "Replacement", "new_string",
	} {
		raw, exists := input[key]
		if !exists {
			continue
		}
		var content string
		if json.Unmarshal(raw, &content) == nil {
			return true
		}
	}
	return false
}

// userApproval says why permission or plan card ix is the user's to allow, not its supervisor's ("" when the
// supervisor may allow it; either may always deny it): it asks for a form or a secret, it is an owner-action proposal
// or a secret hand-off, or its actual action goes out in the user's name, spends money or deletes. A local file edit's
// payload is data, not an outward or destructive action; known tool actions are classified structurally before the
// remaining text falls back to userApprovalWords.
func userApproval(ix proto.Interaction) string {
	switch {
	case len(ix.Fields) > 0, ix.Secret, ix.Elicitation != nil:
		return "asks for a form or a secret, which only the user fills in"
	case ix.OwnerProposal, ix.ApprovalBatchID != "", ix.ToolName == proto.SecretHandoffTool:
		return "is an owner action"
	}
	if ix.Kind == proto.IxPermission && (ix.ToolName == "Skill" || localFileEditPermission(ix)) {
		// Skill only loads instructions; later tool calls are classified on their own.
		return ""
	}
	if verb := connector.WriteVerb(ix.ToolName); slices.Contains(outboundVerbs, verb) || slices.Contains(userApprovalActionVerbs, verb) {
		return fmt.Sprintf("would send, reply or comment in the user's name, spend money or delete (tool action %s)", verb)
	}
	text := strings.ToLower(strings.Join([]string{ix.ToolName, ix.ToolTitle, ix.Summary, ix.Reason, ix.Plan, ix.EditableText,
		string(ix.ToolInput)}, "\n"))
	var hit []string
	for _, w := range userApprovalWords {
		if strings.Contains(text, w) {
			hit = append(hit, w)
		}
	}
	if len(hit) > 0 {
		return fmt.Sprintf("would send, reply or comment in the user's name, spend money or delete (%s)", strings.Join(hit, ", "))
	}
	return ""
}

// answeredApproval allows (or denies) permission or plan card ix of task todo for its supervisor, conversation self,
// and keeps that as the task's decision: the supervisor's call, made by self, never the user's. The agent gets it
// marked as the assistant's answer (AnsweredBy), with reason.
func (s *Service) answeredApproval(ctx context.Context, todo, self string, ix proto.Interaction, allow bool, reason string) error {
	r := proto.Resolution{AnsweredBy: proto.AnsweredByAssistant, Reason: reason}
	if ix.Kind == proto.IxPlan {
		r.Approve = &allow
		if allow {
			r.Mode = "default"
		} else {
			r.Feedback = reason
		}
	} else {
		r.Allow = &allow
		if !allow {
			r.Message = reason
		}
	}
	if err := s.resolveRemembered(ix.ID, r, false); err != nil {
		return err
	}
	s.workEvent(ix.MachineID, ix.SessionID, "ask_supervisor_answered", map[string]any{"by": self, "todo": todo, "card": ix.ID, "kind": ix.Kind, "allow": allow})
	st := s.todoDB()
	if st == nil || todo == "" {
		return nil
	}
	l := s.lang(ctx)
	q, c := decisionText(interactionQuestion(l, ix)), decisionText(resolutionText(l, ix, r))
	if q == "" || c == "" {
		return nil
	}
	at := decisionNow()
	wrote, err := s.insertDecision(ctx, st, db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: todo, Question: q, Choice: c,
		Reason: decisionText(reason), Source: db.DecisionAgent, SourceSession: self, SourceMessage: ix.ID, CreatedAt: at})
	if err != nil {
		s.logf("decisions: todo %s, card %s: %v", todo, ix.ID, err)
		return nil
	}
	if wrote {
		s.decisionsChanged(ctx, todo, at)
	}
	return nil
}

// answeredScreen picks option o of terminal dialog ix of task todo for its supervisor, conversation self, as
// answeredApproval allows a permission: the keys go to the agent's terminal (the card's own answer, screenAnswer), the
// history and the channels say the assistant answered (AnsweredBy), and the pick is kept as the task's decision, made
// by self, never the user's.
func (s *Service) answeredScreen(ctx context.Context, todo, self string, ix proto.Interaction, o screen.Option, reason string) error {
	r := proto.Resolution{Option: o.Key, AnsweredBy: proto.AnsweredByAssistant, Reason: reason}
	if err := s.resolveRemembered(ix.ID, r, false); err != nil {
		return err
	}
	s.workEvent(ix.MachineID, ix.SessionID, "ask_supervisor_answered", map[string]any{"by": self, "todo": todo, "card": ix.ID, "kind": ix.Kind, "option": o.Key})
	st := s.todoDB()
	if st == nil || todo == "" {
		return nil
	}
	l := s.lang(ctx)
	q, c := decisionText(screenQuestion(l, ix)), decisionText(o.Label)
	if q == "" || c == "" {
		return nil
	}
	at := decisionNow()
	wrote, err := s.insertDecision(ctx, st, db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: todo, Question: q, Choice: c,
		Reason: decisionText(reason), Source: db.DecisionAgent, SourceSession: self, SourceMessage: ix.ID, CreatedAt: at})
	if err != nil {
		s.logf("decisions: todo %s, card %s: %v", todo, ix.ID, err)
		return nil
	}
	if wrote {
		s.decisionsChanged(ctx, todo, at)
	}
	return nil
}

// escalatedResolved keeps the user's answer to an approval or plan they got through an escalation as the task's
// decision (a question's answer is kept as every card's is, cardDecisions), so a later card cannot quietly go the other way.
func (s *Service) escalatedResolved(ctx context.Context, ix proto.Interaction, r proto.Resolution, status string) {
	if status == "failed" {
		return // a Codex card the node could not answer is open again: its next answer counts
	}
	s.fleet.mu.Lock()
	card, ok := s.askUp.escalated[ix.ID]
	delete(s.askUp.escalated, ix.ID)
	s.fleet.mu.Unlock()
	todo := card.todo
	st := s.todoDB()
	if !ok || st == nil || status != "resolved" || r.Dismiss || ix.Kind == proto.IxQuestion {
		return
	}
	if r.AnsweredBy != "" || r.Late {
		return // answered for the user (the assistant, a supervisor, the terminal, a default), not the user's own decision
	}
	l := s.lang(ctx)
	q, c := decisionText(interactionQuestion(l, ix)), decisionText(resolutionText(l, ix, r))
	if q == "" || c == "" {
		return
	}
	at := decisionNow()
	wrote, err := s.insertDecision(ctx, st, db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: todo, Question: q, Choice: c,
		Source: db.DecisionCard, SourceMachine: ix.MachineID, SourceSession: ix.SessionID, SourceMessage: ix.ID, CreatedAt: at})
	if err != nil {
		s.logf("decisions: todo %s, card %s: %v", todo, ix.ID, err)
		return
	}
	if wrote {
		s.decisionsChanged(ctx, todo, at)
	}
}
