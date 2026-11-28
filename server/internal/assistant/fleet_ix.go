package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The interactions (docs/protocol/protocol.md "Interactions"): the cards the user answers on the phone — a hook's permission, question
// or plan (a node's interaction.create, waited for until it is answered, expires or its node goes; a resumable one
// outlives its node for its hook to claim again), a TUI dialog read off the screen (a "screen" card the manager
// answers itself with keys), a Codex question or approval the node lists, the assistant's own questions and cards,
// and what the hub's own features ask (fleet.ask). They are kept here, recorded in the interactions table, told the
// hub (fleet.interaction / fleet.interaction_resolved), pushed (fleet_push.go), and answered through the hub's
// relayed route (fleet_routes.go).

type pending struct {
	ix   proto.Interaction
	done chan proto.Resolution
	// prepared is the authoritative process-local operation behind an assistant-owned approvalops.
	// Clarification releases only its original caller; the card and this operation stay pending.
	prepared *preparedApproval
	// resolved broadcasts a claimed answer to every overlapping resumable waiter.
	resolved        chan struct{}
	resolution      *proto.Resolution
	persisting      *questionPersistence
	persistenceWake chan struct{}
	persistenceErr  error
	// local is set for interactions the manager resolves itself (screen dialogs, Codex cards): it acts on the answer,
	// and an error goes back to whoever answered (prompt_changed: nothing was pressed).
	local func(context.Context, proto.Resolution) error
	// orphaned is set while no hook waits on a resumable interaction (its node went away, or the manager restarted):
	// it ends the interaction unless the hook claims it again in time.
	orphaned *time.Timer
	// checkedAt / checking bound transcript reads for a plan answered outside Persona.
	checkedAt time.Time
	checking  bool
	// answeredBy is who resolved it (Resolution.AnsweredBy), so the hub can say the assistant answered, with why
	// (Resolution.Reason) and its answers then (a question's: never a secret).
	answeredBy, answeredReason string
	answeredWith               map[string]string
	// codex: a card a node listed for a Codex TUI; it lasts as long as the node lists it.
	codex bool
	// claimed: an answer was taken for this hook-waited card (resolve). It stays in the map until await or
	// finishInteraction ends it, but a second answer finds it no longer pending.
	claimed bool
	// graceEnded: its grace period ran out (orphanLocked) and it is being ended: a hook back now hands over.
	graceEnded bool
	// expired: it ended because its time ran out, unanswered (await); finished() reads it.
	expired bool
	// endedBy is who gave the answer claimed on it, for the history (interactions.ended_by, recordEndBy): a device,
	// the assistant, a kept "Always"… "" while none is claimed, or when the way it came does not say.
	endedBy string
	// announced: a screen card that rang (announce), carried through a detach (screenGone.announced), so the card
	// read again once the node is back rings at the window's end only if its predecessor never did (#4979).
	announced bool
	// told: it is the user's: it went through tell, when the phone is pushed, or it is a screen card back from a
	// detach whose predecessor rang. Not while it is on its way there (recorded, then announced), while the assistant
	// decides on it, nor when it is only put back on the apps (show) by a way back that cannot tell whether it rang
	// before (a restart, a hook asking again for a card only the history has, a Codex card listed again): those count
	// once tell rings them. A hook claiming its card again in the same manager resumes this pending, and keeps it.
	// Persona Link's summary notifies by it (fleet_attention.go).
	told bool
	// route: on its way up to the user through its task's supervisors (ask_route.go); nil once it is the user's.
	route *askRoute
	// seenTTL and deadlineChanged govern owner-only timed cards whose short countdown starts only
	// when the user views the card in an app (owner_card_expiry.go).
	seenTTL         time.Duration
	deadlineChanged chan struct{}
	// remindTimer fires once after pendingReminderDelay to re-push an unanswered owner-facing card (#16159).
	remindTimer *time.Timer
	reminded    bool
}

func (p *pending) stopReminderLocked() {
	if p != nil && p.remindTimer != nil {
		p.remindTimer.Stop()
		p.remindTimer = nil
	}
}

// pendingReminderDelay is how long an unanswered owner-facing approval card waits before one follow-up push (#16159).
var pendingReminderDelay = 30 * time.Minute

// resumeGrace is how long a resumable interaction outlives its node: a little longer than hooks keep reconnecting
// (nodeclient.ResumeWait).
var resumeGrace = 3 * time.Minute

// ixMapLocked is the pending interactions, made on first use. Callers hold s.fleet.mu.
func (s *Service) ixMapLocked() map[string]*pending {
	if s.fleet.interactions == nil {
		s.fleet.interactions = map[string]*pending{}
	}
	return s.fleet.interactions
}

// hasHooks: Persona's hooks run in the session — it sent a hook event since the manager started, or Persona started
// it (with its hooks) — so they ask for most dialogs themselves and a screen card waits for them a little.
func (s *Service) hasHooks(machine string, sess proto.Session) bool {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	return s.fleet.hooked[[2]string{machine, sess.SessionID}] || sess.StartedByPersona
}

// elicitationReachable: an MCP form or link ix can reach the user, as the hub says (fleet.clients): an app that takes
// one is connected, or a phone that registered for them would be pushed one (#2433), and this card would be pushed (no
// muted agent, no quiet task's). Otherwise nothing could show it and it goes back to the terminal at once.
func (s *Service) elicitationReachable(ix proto.Interaction) bool {
	s.fleet.mu.Lock()
	c := s.fleet.clients
	s.fleet.mu.Unlock()
	return c.Elicitation || c.ElicitationPush && !s.quietSession(ix.MachineID, ix.SessionID) && !s.muted(ix.MachineID, ix.SessionID)
}

// ---- telling ----

// announce puts a new card on the apps (through the hub), pushes it, and gives it to the channels. A supervised task's
// parsed terminal question, and its other eligible cards when leaders are configured, go to the supervising conversation
// first (ask_route.go); a question the assistant may answer for the user goes to it first (fleet_delegate.go).
func (s *Service) announce(ix proto.Interaction) {
	// Standing authorizations answer first, as for any session: hiding a folder changes what is shown, not what the
	// user already approved.
	if s.approveStandingInteraction(ix) {
		return
	}
	if s.directoryHiddenSession(ix.MachineID, ix.SessionID) {
		s.tellApps(ix) // Retain the card for explicit inspection without routing it into Assistant context.
		return
	}
	if s.routeAsk(ix) {
		return
	}
	if forget := s.delegable(ix); forget != nil {
		s.delegate(ix, forget)
		return
	}
	if s.handNonOwnerCard(ix) {
		return
	}
	s.tell(ix)
}

// tell is announce once nothing is in the way: the card on the apps, the push, the channels, or the main conversation
// for a quiet task.
func (s *Service) tell(ix proto.Interaction) {
	ix = s.tellApps(ix)
	if s.directoryHiddenSession(ix.MachineID, ix.SessionID) {
		return
	}
	if s.quietSession(ix.MachineID, ix.SessionID) {
		// A quiet task's question is no push, bot card or channel message: the main conversation is told (#1731).
		go s.InteractionRaised(context.Background(), InteractionRaised{Interaction: ix})
		return
	}
	s.tellUser(ix, true)
}

// tellApps makes card ix the user's on the apps (pending.told), named with its task; the card as told.
func (s *Service) tellApps(ix proto.Interaction) proto.Interaction {
	s.taskPlanApproval()
	ix = s.withTask(ix)
	s.markTold(ix)
	s.tellCard(ix)
	s.publishSessions(ix.MachineID)
	s.liveChanged(false) // whether it is told counts for Persona Link's summary, not for the list
	s.cardStateChanged(ix)
	return ix
}

// tellUser rings card ix, already on the apps: the push (push false: one of several going out as one push, ask_route.go)
// and the topic of the agent's todo unless the agent is muted, and the Feishu bot and the channels.
func (s *Service) tellUser(ix proto.Interaction, push bool) {
	if s.directoryHiddenSession(ix.MachineID, ix.SessionID) {
		return
	}
	s.recordConfirmationNotification(ix)
	if push && !s.muted(ix.MachineID, ix.SessionID) {
		s.notifyInteraction(ix, s.machineName(ix.MachineID), s.linkCovers(ix))
		s.armPendingReminder(ix)
		go s.threadAgentAsks(ix) // and the topic of the todo this agent works on, if it has one
	}
	s.InteractionAnnounced(context.Background(), InteractionAnnounced{Interaction: ix}) // the Feishu bot, the channels
}

// reminderEligible reports whether an owner-facing card should receive one follow-up push when left unanswered (#16159).
func reminderEligible(ix proto.Interaction) bool {
	return ix.Kind == proto.IxPermission || ix.Kind == proto.IxQuestion || ix.Kind == proto.IxPlan
}

// armPendingReminder schedules at most one follow-up push after pendingReminderDelay for a pending, told
// approval card whose remaining lifetime exceeds the delay (#16159).
func (s *Service) armPendingReminder(ix proto.Interaction) {
	delay := pendingReminderDelay
	if delay <= 0 || !reminderEligible(ix) {
		return
	}
	if !ix.NoExpiry && !ix.ExpiresAt.IsZero() && time.Until(ix.ExpiresAt) <= delay {
		return
	}
	s.fleet.mu.Lock()
	p := s.fleet.interactions[ix.ID]
	if p == nil || p.claimed || p.graceEnded || !p.told || p.reminded || p.remindTimer != nil {
		s.fleet.mu.Unlock()
		return
	}
	p.remindTimer = time.AfterFunc(delay, func() { s.firePendingReminder(ix.ID, p) })
	s.fleet.mu.Unlock()
}

func (s *Service) firePendingReminder(id string, p *pending) {
	s.fleet.mu.Lock()
	if s.fleet.interactions[id] != p || p.claimed || p.graceEnded || !p.told || p.reminded {
		s.fleet.mu.Unlock()
		return
	}
	if a := p.prepared; a != nil && (a.terminal || a.paused) {
		p.remindTimer = nil
		s.fleet.mu.Unlock()
		return
	}
	p.reminded = true
	p.remindTimer = nil
	ix := p.ix
	s.fleet.mu.Unlock()
	if !ix.NoExpiry && !ix.ExpiresAt.IsZero() && !time.Now().Before(ix.ExpiresAt) {
		return
	}
	if s.directoryHiddenSession(ix.MachineID, ix.SessionID) || s.muted(ix.MachineID, ix.SessionID) || s.quietSession(ix.MachineID, ix.SessionID) {
		return
	}
	s.notifyInteraction(ix, s.machineName(ix.MachineID), s.linkCovers(ix))
}

// show puts a card on every app without a push: one they were told about already (a way back), or one that is not the
// user's yet (the assistant decides on it; a screen card whose dialog was not read, #4450). It never makes a card the
// user's (pending.told): only tell does.
func (s *Service) show(ix proto.Interaction) {
	s.taskPlanApproval()
	shown := s.withTask(ix)
	s.tellCard(shown)
	s.publishSessions(ix.MachineID)
	s.liveChanged(false) // the card, and whether the assistant decides on it, count for Persona Link's summary
	s.cardStateChanged(shown)
}

// tellCard gives the hub card ix (fleet.interaction), as tellApps and show do. A card can end while it is on its way:
// its node withdraws it at once (interaction.cancel: OpenCode's TUI in auto accept answers a permission the moment it
// is asked, and the plugin cancels the card it was raising), or it ends between a caller's check and this tell
// (escalate, ringAsks). finished() then told the hub its end before the card itself reached it, and the hub would keep
// it pending for good ("needs you", and 410 when answered). So once the hub has the card, a card no longer held under
// its id is told ended again, after it. One that ends after the check is told ended by finished(), which then comes
// after the card too. A newer card held under the id (another CreatedAt) is left alone: its own tell settles it, and
// ending the id would take it off the hub. cardTell keeps a newer card's tell from coming between the check and the
// end told here; it is not fleet.mu, since a tell is a write on the hub's connection.
func (s *Service) tellCard(ix proto.Interaction) {
	s.fleet.cardTell.Lock()
	defer s.fleet.cardTell.Unlock()
	if err := s.hub.Tell(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: ix}); err != nil {
		return // the hub did not get it: nothing of it to take back (its handshake gets the pending cards again)
	}
	held := func() bool {
		s.fleet.mu.Lock()
		defer s.fleet.mu.Unlock()
		_, ok := s.fleet.interactions[ix.ID]
		return ok
	}
	if !held() {
		s.hub.Tell(assistantwire.EventFleetResolved, assistantwire.FleetResolved{ID: ix.ID, CreatedAt: ix.CreatedAt})
		return
	}
	if !ix.MirrorMain {
		return // display intent is created before publication, never inferred here
	}
	mirror, ok := s.mainApprovalMirror(context.Background(), ix)
	if !ok {
		return
	}
	if err := s.hub.Tell(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: mirror}); err != nil {
		return
	}
	// A mirror is held only through its authoritative interaction. Recheck after
	// publishing it so a concurrent resolution cannot leave an orphaned copy.
	if !held() {
		s.hub.Tell(assistantwire.EventFleetResolved, assistantwire.FleetResolved{ID: mirror.ID, CreatedAt: mirror.CreatedAt})
	}
}

// markTold notes that pending card ix is the user's: tell is ringing it (pending.told). Only that card: a newer one
// under the same id (a screen card made again after blocked -> working -> blocked) was not told yet.
func (s *Service) markTold(ix proto.Interaction) {
	s.fleet.mu.Lock()
	if p := s.fleet.interactions[ix.ID]; p != nil && p.ix.CreatedAt.Equal(ix.CreatedAt) {
		p.told = true
	}
	s.fleet.mu.Unlock()
}

// ---- waiting ----

// nodeInteraction is a node's interaction.create: allowed right away when "Always allow in this session" already covers it, else
// shown and waited for.
func (s *Service) nodeInteraction(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	// Display-copy and exact-proposal identity are manager-owned. A runtime
	// cannot redirect its waiter, forge an owner-action proposal or waive expiry.
	ix.MirrorOf, ix.MirrorMain = "", false
	ix.ApprovalBatchID, ix.ApprovalItemID = "", ""
	ix.OwnerProposal, ix.NoExpiry = false, false
	ix.OwnerApprovalPhrases = nil
	ix.NoHandBack = false
	ix.OutwardWrite = ix.OutwardWrite && ix.Kind == proto.IxPermission
	if reservedCard(ix) {
		return proto.Resolution{}, errReservedCard
	}
	// Defense behind the node hook: question-text-keyed answers could not be kept apart on a duplicate card.
	if ix.Kind == proto.IxQuestion && proto.HasDuplicateQuestionText(ix.Questions) {
		return proto.Resolution{}, i18n.Err("hub-hub.duplicate_question")
	}
	// Plan mode enforces a read-only task's launch constraint. Its supervisor report and strictly read-only
	// inspection Bash commands may run once without a card, while exiting plan mode stays denied and any write,
	// ambiguous, credential-touching or inbox-origin Bash command still asks. Neither path registers a card or grants permissions.
	report := ix.Kind == proto.IxPermission && slices.Contains(compat.MCPToolNames([]string{"report_to_supervisor"}), ix.ToolName)
	safeReadonlyBash := ix.Kind == proto.IxPermission && ix.ToolName == "Bash" &&
		!s.isInboxOrigin(ctx, ix.MachineID, ix.SessionID, false) &&
		readonlyTaskSafeBash(ix.ToolInput, ix.Cwd)
	if ix.Kind == proto.IxPlan && ix.ToolName == "ExitPlanMode" || report || safeReadonlyBash {
		if todo, ok := s.taskOf(ctx, ix.MachineID, ix.SessionID); ok {
			plans, err := s.conversationPlans(ctx, todo.Origin)
			if err != nil {
				return proto.Resolution{}, err
			}
			for _, plan := range plans {
				if plan.State != planStarted {
					continue
				}
				for _, task := range plan.Tasks {
					if task.TodoID == todo.ID && task.Readonly {
						if report {
							if todo.Supervision == nil || todo.Supervision.Supervisor == "" {
								continue
							}
							allow := true
							return proto.Resolution{Allow: &allow}, nil
						}
						if safeReadonlyBash {
							allow := true
							return proto.Resolution{Allow: &allow}, nil
						}
						approve := false
						return proto.Resolution{Approve: &approve, Feedback: prompts.RenderReadonlyTaskPlanExit()}, nil
					}
				}
			}
		}
	}
	inAssistant := s.ixInAssistant(ctx, ix)
	inboxWholeBash := s.isInboxWholeBashAlways(ctx, ix)
	if inboxWholeBash {
		ix.AlwaysKeys = nil
	}
	confirms := s.ixPolicy(ctx, ix).ConfirmOutbound || inboxWholeBash ||
		(strings.EqualFold(ix.ToolName, "Bash") && len(ix.AlwaysKeys) == 0 && s.isInboxOrigin(ctx, ix.MachineID, ix.SessionID, false))
	ix = nodeCard(ix, s.fleetStore() != nil, confirms, inAssistant)
	if ix.Kind == proto.IxPermission {
		l := s.lang(ctx)
		ix.ToolTitle = personaToolTitle(l, ix.ToolName, ix.ToolInput)
		if name := personaToolTitle(l, ix.ToolName, nil); name != ix.ToolTitle {
			ix.ToolNameTitle = name // the tool alone, for what Always covers: no report's kind
		}
	}
	ix.Detachable = ix.Detachable && ix.Kind == proto.IxPermission && ix.SessionID != "" && inAssistant &&
		len(ix.ToolInput) > 0 && ix.ExecutionCwd != ""
	if ix.Kind == proto.IxPermission {
		ix.NoHandBack = s.permissionNoHandBack(ix, inAssistant)
	}
	if ix.Detachable && ix.Resumable {
		// A journaled original is a retained request or outcome. Check it
		// before detachedOriginalReplay, Always and new-card registration so
		// even a replay ahead of restoreInteractions fails closed.
		if r, ok, err := s.journaledDetachedReplay(ctx, ix); err != nil {
			return proto.Resolution{}, err
		} else if ok {
			return r, nil
		}
		// The original request is never its own continuation. In particular it
		// must not spend its unused grant or re-enter through a newly stored
		// Always rule. A late outcome carries ApprovalID and waitDetachedCard
		// turns it into a non-executing replay result.
		replay, err := s.detachedOriginalReplay(ctx, ix)
		if err != nil {
			return proto.Resolution{}, err
		}
		if replay {
			return s.waitDetachedCard(ctx, ix)
		}
	}
	if ix.Detachable {
		if r, ok, err := s.consumeDetachedGrant(ctx, ix); err != nil {
			return proto.Resolution{}, err
		} else if ok {
			return r, nil
		}
	}
	if !confirms {
		if r, ok := s.alwaysAllowed(ctx, ix); ok {
			if ix.Resumable { // its hook's earlier attempt left the card pending: only that same occurrence is answered too
				if err := s.checkRememberedInteractionOccurrence(ctx, ix); err != nil {
					return proto.Resolution{}, err
				}
				_ = s.resolveRememberedOccurrence(ix, r)
			}
			return r, nil
		}
	}
	if ix.Detachable {
		return s.waitDetachedCard(ctx, ix)
	}
	return s.waitInteraction(ctx, ix)
}

// lockInteractionAdmission orders duplicate detection, durable-history checks
// and live-map installation for one ID. The returned release runs after
// registration, before announcements or waits; a completed occurrence can then
// be replayed from its durable outcome instead of displacing its successor.
func (s *Service) lockInteractionAdmission(id string) func() {
	return s.acquireInteractionAdmission(id, true)
}

func (s *Service) acquireInteractionAdmission(id string, wait bool) func() {
	s.fleet.mu.Lock()
	if s.fleet.admissions == nil {
		s.fleet.admissions = map[string]*interactionAdmission{}
	}
	gate := s.fleet.admissions[id]
	if gate == nil {
		gate = &interactionAdmission{}
		s.fleet.admissions[id] = gate
	}
	if !wait && !gate.mu.TryLock() {
		s.fleet.mu.Unlock()
		return nil
	}
	gate.refs++
	s.fleet.mu.Unlock()

	if wait {
		gate.mu.Lock()
	}
	return func() {
		gate.mu.Unlock()
		s.fleet.mu.Lock()
		gate.refs--
		if gate.refs == 0 && s.fleet.admissions[id] == gate {
			delete(s.fleet.admissions, id)
		}
		s.fleet.mu.Unlock()
	}
}

// waitInteraction registers a hook interaction and blocks until it is resolved, cancelled or expires.
// A resumable one asked again with the same id (its hook reconnected after a manager or node restart) is the same
// interaction: the wait resumes on it, or returns the answer given meanwhile. One the history has as still pending
// (the manager restarted with it on the phone) is shown again without a second push.
func (s *Service) waitInteraction(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	return s.waitInteractionPrepared(ctx, ix, nil)
}

func (s *Service) waitInteractionPrepared(ctx context.Context, ix proto.Interaction, prepared *preparedApproval) (proto.Resolution, error) {
	return s.waitInteractionReady(ctx, ix, prepared, nil)
}

// waitInteractionReady is waitInteractionPrepared with an optional registration
// notification for server-owned proposal groups. The notification is closed
// after the pending entry and its announcement exist, before this waiter blocks.
func (s *Service) waitInteractionReady(ctx context.Context, ix proto.Interaction, prepared *preparedApproval, ready chan<- struct{}) (proto.Resolution, error) {
	var registered func(*pending)
	if ready != nil {
		registered = func(*pending) { close(ready) }
	}
	return s.waitRegisteredInteraction(ctx, ix, prepared, registered)
}

// waitInteractionReadyResult also reports registration failures to callers that
// must release a separate tool wait without waiting for the proposal's decision.
func (s *Service) waitInteractionReadyResult(ctx context.Context, ix proto.Interaction, prepared *preparedApproval, ready chan<- error) (result proto.Resolution, resultErr error) {
	readySent := false
	notifyReady := func(err error) {
		if ready != nil && !readySent {
			ready <- err
			readySent = true
		}
	}
	defer func() {
		if resultErr == nil {
			notifyReady(errNotPending)
			return
		}
		notifyReady(resultErr)
	}()
	return s.waitRegisteredInteraction(ctx, ix, prepared, func(*pending) { notifyReady(nil) })
}

func (s *Service) waitRegisteredInteraction(ctx context.Context, ix proto.Interaction, prepared *preparedApproval, registered func(*pending)) (proto.Resolution, error) {
	// The node may send its first request while FleetUp is still restoring the last manager's cards.
	// Do not let that recovery cancel a card this process is about to persist.
	if s.fleetOn() {
		s.restoreInteractions(ctx)
	}
	unlockAdmission := s.lockInteractionAdmission(ix.ID)
	admissionHeld := true
	releaseAdmission := func() {
		if admissionHeld {
			admissionHeld = false
			unlockAdmission()
		}
	}
	defer releaseAdmission()
	ix = cleanTitle(ix)
	// Only an operation registered by this process can advertise clarification.
	ix.CanClarify = prepared != nil
	if ix.Kind == proto.IxPermission && (prepared != nil || ix.OwnerProposal) {
		ix.NoHandBack = true
	}
	// A Just me turn's card, the node's or the manager's own (ask_user, a confirmation): pushed without its content,
	// never to Lark or a channel (meeting_justme.go).
	ix.Private = ix.Private || s.justMeNow(ix.MachineID, ix.SessionID)
	ix, seenTTL := applyOwnerCardDeadline(ix, time.Now().UTC())
	if ix.ExpiresAt.IsZero() && !persistentOwnerProposal(ix) {
		ix.ExpiresAt = time.Now().Add(s.notifyTimings().hookTimeout()).UTC()
	}
	// Restore this exact pending occurrence before considering the current
	// preference. A resumable node hook carries no manager-owned display fields.
	if intent, at, found := s.recordedApprovalMirrorIntent(ctx, ix); found {
		ix.MirrorMain, ix.CreatedAt = intent, at
	} else if !ix.MirrorMain {
		if _, ok := s.mainApprovalMirror(ctx, ix); ok {
			ix.MirrorMain = true
		}
	}
	var recovered *approvalJournalEntry
	if ix.Resumable && ix.Detachable {
		entry, found, err := s.approvalJournalRecord(ctx, ix)
		if err != nil {
			return proto.Resolution{}, err
		}
		if found {
			recovered = &entry
		}
	}
	if !ownerActionProposal(ix) {
		ix.NoExpiry = false
	}
	freshLogical, err := s.logicalPublishInteraction(ctx, ix)
	if err != nil {
		return proto.Resolution{}, err
	}
	again := false
	if !freshLogical {
		restartPrepared := false
		upgradedNoHandBack := false
		var updatedIx proto.Interaction
		s.fleet.mu.Lock()
		if p := s.fleet.interactions[ix.ID]; p != nil {
			if p.local != nil {
				s.fleet.mu.Unlock()
				return proto.Resolution{}, fmt.Errorf("%w: %s", errInteractionIDReused, ix.ID)
			}
			// Detachable original replays use exact call identity; ordinary
			// resumable cards use their creation timestamp. Legacy zero-time
			// non-resumable duplicates retain ID-only joining. A persistent owner
			// proposal may rejoin only the same durable batch/item after restart
			// normalization changed its timestamp; decisions remain occurrence-bound.
			sameOwnerOperation := persistentOwnerProposal(ix) && persistentOwnerProposal(p.ix) &&
				ix.MachineID == p.ix.MachineID && ix.SessionID == p.ix.SessionID &&
				ix.ApprovalBatchID == p.ix.ApprovalBatchID && ix.ApprovalItemID == p.ix.ApprovalItemID
			if (ix.Detachable || !ix.CreatedAt.IsZero()) && !sameApprovalOccurrence(ix, p.ix) && !sameOwnerOperation {
				s.fleet.mu.Unlock()
				return proto.Resolution{}, fmt.Errorf("%w: %s", errInteractionIDReused, ix.ID)
			}
			if p.prepared == nil && recovered != nil {
				// A restored process has no executable operation. Close a journaled
				// occurrence before its replay can attach an ordinary waiter and
				// before an owner answer can turn recovery into an execution Allow.
				s.fleet.mu.Unlock()
				releaseAdmission()
				if err := s.finishRecoveredApproval(*recovered); err != nil {
					return proto.Resolution{}, err
				}
				if ix.Kind == proto.IxElicitation && !s.elicitationReachable(ix) {
					_ = s.resolve(ix.ID, proto.Resolution{Dismiss: true})
				}
				if registered != nil {
					registered(p)
				}
				// Resumable node cards reconnect through reporter-less interaction.create requests; MCP elicitation cards
				// are not resumable, so this branch never has a wait reporter to restore.
				return s.await(ctx, p)
			}
			if !p.claimed && len(p.ix.AlwaysKeys) > 0 && len(ix.AlwaysKeys) == 0 && ix.AlwaysHonored != nil && !*ix.AlwaysHonored {
				// Reclaiming a resumable card after origin registration or a manager upgrade must not
				// retain a previously stripped/disallowed Always option (#15267).
				p.ix.AlwaysKeys = nil
				p.ix.AlwaysHonored = ix.AlwaysHonored
			}
			if !p.claimed && ix.NoHandBack && !p.ix.NoHandBack {
				p.ix.NoHandBack = true
				upgradedNoHandBack = true
			}
			if p.prepared != nil {
				// The reconnecting hook is now the caller whose lifetime governs the
				// initial wait. The disconnected context must not expire a card its
				// resumable request has already reclaimed.
				p.prepared.caller = ctx
			} else if prepared != nil && !p.claimed {
				// A card restored from Postgres after a manager restart has p.prepared == nil
				// until its still-running hook reconnects with the prepared coordinator (#12918).
				p.prepared = prepared
				p.ix.CanClarify = true
				if p.prepared.decisionDeadline.IsZero() {
					p.prepared.decisionDeadline = p.ix.ExpiresAt
				}
			}
			if p.graceEnded {
				// The grace period ended and the card is being ended outside the lock (the history, the apps): the
				// hook hands over, as one back just after would (#5043's kind of gap). Preserve which unanswered end won.
				reason := dismissedReason("cancelled")
				if p.expired {
					reason = dismissedReason("expired")
				}
				s.fleet.mu.Unlock()
				return proto.Resolution{Dismiss: true, Reason: reason}, nil
			}
			if p.orphaned != nil {
				p.orphaned.Stop() // fired but not decided yet: it finds p.orphaned nil and leaves the card to the hook
				p.orphaned = nil
			}
			if a := p.prepared; ix.Resumable && a != nil && !a.clarified && !a.terminal {
				// The initial timer may have fired while the card was orphaned and
				// deliberately done nothing. Reclaiming starts a fresh initial wait.
				if a.decisionTimer != nil {
					a.decisionTimer.Stop()
				}
				restartPrepared = true
			}
			if upgradedNoHandBack {
				updatedIx = p.ix
			}
			s.fleet.mu.Unlock()
			releaseAdmission()
			if upgradedNoHandBack {
				s.show(updatedIx)
			}
			if restartPrepared {
				s.startPreparedTimer(p)
			}
			if ix.Kind == proto.IxElicitation && !s.elicitationReachable(ix) {
				_ = s.resolve(ix.ID, proto.Resolution{Dismiss: true})
			}
			if registered != nil {
				registered(p)
			}
			// Resumable node cards reconnect through reporter-less interaction.create requests; MCP elicitation cards
			// are not resumable, so this branch never has a wait reporter to restore.
			return s.await(ctx, p)
		}
		s.fleet.mu.Unlock()
		if ix.Resumable {
			if err := s.checkRecordedInteractionOccurrence(ctx, ix); err != nil {
				return proto.Resolution{}, err
			}
			status, r, ended := s.outcome(ix.ID)
			if ended {
				return r, nil
			}
			again = status == "pending"
		}
	}
	if ix.Kind == proto.IxElicitation && !s.elicitationReachable(ix) {
		return proto.Resolution{Dismiss: true}, nil
	}
	p := &pending{ix: ix, done: make(chan proto.Resolution, 1), prepared: prepared, seenTTL: seenTTL, deadlineChanged: make(chan struct{}, 1)}
	var replaced *pending
	var dropped []*pending
	for {
		s.fleet.mu.Lock()
		// A newer equivalent proposal atomically supersedes an older Clarifying
		// one. An in-flight question finishes first; a final answer already
		// claimed on the old card wins instead.
		replaced = nil
		var attempt *clarifyAttempt
		if prepared != nil && prepared.op.Replacement != "" {
			for _, old := range s.fleet.interactions {
				if old.prepared != nil && old.prepared.clarified && !old.claimed && !old.prepared.terminal &&
					old.prepared.op.Replacement == prepared.op.Replacement && old.ix.MachineID == ix.MachineID && old.ix.SessionID == ix.SessionID {
					if old.prepared.attempt != nil {
						attempt = old.prepared.attempt
						break
					}
					replaced = old
					old.claimed = true
					old.prepared.terminal = true
					old.prepared.reportStatus = "replaced"
					old.prepared.outcome = approvalops.Outcome{Text: fmt.Sprintf("Newer approval %s replaced this proposal. Nothing changed; a decision on the fresh card is required.", ix.ID)}
					old.prepared.result = proto.Resolution{Dismiss: true, Reason: "replaced"}
					old.resolution = &old.prepared.result
					if old.prepared.decisionTimer != nil {
						old.prepared.decisionTimer.Stop()
					}
					if old.prepared.pauseTimer != nil {
						old.prepared.pauseTimer.Stop()
					}
					break
				}
			}
		}
		if attempt != nil {
			s.fleet.mu.Unlock()
			<-attempt.done
			continue
		}
		// A screen interaction for the same session was a premature guess; the hook knows better.
		// Tell the app now: the wait below can last until the hook times out.
		dropped = nil
		m := s.ixMapLocked()
		for id, old := range m {
			if old.ix.Kind == proto.IxScreen && old.ix.MachineID == ix.MachineID && old.ix.SessionID == ix.SessionID {
				delete(m, id)
				dropped = append(dropped, old)
			}
		}
		m[ix.ID] = p
		s.fleet.mu.Unlock()
		releaseAdmission()
		break
	}
	if replaced != nil {
		s.finishPrepared(replaced, "cancelled")
	}
	for _, old := range dropped { // as a Codex card withdraws a screen card
		s.recordEndBy(old.ix.ID, "cancelled", db.EndedByCleared, nil)
		s.finished(old)
	}
	if again {
		// A card the history has as pending and this manager does not hold was left by a manager that restarted: its
		// node may claim it before the hub's handshake restores the others (restoreInteractions), so it finds its route
		// here too (restoreRoute), or the assistant goes on deciding on it (resumeDelegations). Any other is only shown
		// again, as before.
		if !s.restoreRoute(ctx, ix) && !s.resumeDelegations(ctx, ix) {
			s.show(ix)
		}
	} else {
		s.record(ix)
		s.announce(ix)
	}
	if prepared != nil {
		s.startPreparedTimer(p)
	}
	if registered != nil {
		registered(p)
	}
	resume := pushMCPInteraction(ctx, ix)
	defer resume()
	return s.await(ctx, p)
}

var errAwaitRetry = errors.New("interaction answer claim was released")

// await blocks on a registered interaction. When ctx ends — the node's connection is gone — a resumable
// interaction stays pending for its hook to claim again. A rejected owner
// decision releases only its temporary claim; the waiter then arbitrates again.
func (s *Service) await(ctx context.Context, p *pending) (proto.Resolution, error) {
	for {
		r, err := s.awaitOnce(ctx, p)
		if !errors.Is(err, errAwaitRetry) {
			return r, err
		}
	}
}

func (s *Service) awaitOnce(ctx context.Context, p *pending) (proto.Resolution, error) {
	s.fleet.mu.Lock()
	if p.prepared != nil {
		s.fleet.mu.Unlock()
		return s.awaitPrepared(ctx, p, false)
	}
	if ownerActionProposal(p.ix) && p.persistenceErr != nil {
		err := p.persistenceErr
		s.fleet.mu.Unlock()
		return proto.Resolution{}, err
	}
	if p.resolved == nil {
		p.resolved = make(chan struct{})
	}
	if p.deadlineChanged == nil {
		p.deadlineChanged = make(chan struct{}, 1)
	}
	resolved := p.resolved
	deadlineChanged := p.deadlineChanged
	var persistenceWake <-chan struct{}
	if ownerActionProposal(p.ix) {
		persistenceWake = persistenceWakeLocked(p)
	}
	id := p.ix.ID
	expiresAt := p.ix.ExpiresAt
	persistent := persistentOwnerProposal(p.ix)
	s.fleet.mu.Unlock()
	var timer *time.Timer
	var timeout <-chan time.Time
	resetTimer := func(at time.Time) {
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer = nil
			timeout = nil
		}
		if !at.IsZero() && !persistent {
			timer = time.NewTimer(time.Until(at))
			timeout = timer.C
		}
	}
	resetTimer(expiresAt)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	status := "cancelled"
awaitLoop:
	for {
		select {
		case <-resolved:
			s.finishPending(p)
			return *p.resolution, nil
		case r := <-p.done:
			s.finishPending(p)
			return r, nil
		case <-deadlineChanged:
			s.fleet.mu.Lock()
			expiresAt = p.ix.ExpiresAt
			s.fleet.mu.Unlock()
			resetTimer(expiresAt)
		case <-timeout:
			status = "expired"
			break awaitLoop
		case <-persistenceWake:
			return proto.Resolution{}, errAwaitRetry
		case <-ctx.Done():
			break awaitLoop
		}
	}
	// Decide under the same lock as resolve: an answer already claimed must be
	// persisted before any waiter can record a competing timeout or disconnect.
	s.fleet.mu.Lock()
	if p.persisting != nil {
		attempt := p.persisting
		s.fleet.mu.Unlock()
		<-attempt.done
		if attempt.retry {
			return proto.Resolution{}, errAwaitRetry
		}
		if attempt.err != nil {
			return proto.Resolution{}, attempt.err
		}
		<-resolved
		s.finishPending(p)
		return *p.resolution, nil
	}
	if p.persistenceErr != nil {
		err := p.persistenceErr
		s.fleet.mu.Unlock()
		return proto.Resolution{}, err
	}
	if p.claimed {
		hasAnswer := p.resolution != nil
		s.fleet.mu.Unlock()
		if hasAnswer {
			<-resolved
			s.finishPending(p)
			return *p.resolution, nil
		}
		return proto.Resolution{Dismiss: true, Reason: dismissedReason(status)}, nil
	}
	if s.fleet.interactions[id] != p {
		s.fleet.mu.Unlock()
		// No longer the card pending under its id: a newer one replaced it, or it was cleared with the others.
		return proto.Resolution{Dismiss: true, Reason: proto.DismissedCleared}, nil
	}
	if status == "cancelled" && persistentOwnerProposal(p.ix) {
		// Only this caller is leaving. The durable proposal and every original
		// surface remain pending; there is no interrupted Apply to replay.
		s.fleet.mu.Unlock()
		return proto.Resolution{Dismiss: true, Reason: "owner-action proposal remains pending"}, nil
	}
	if status == "cancelled" && p.ix.Resumable && s.orphanLocked(p) {
		s.fleet.mu.Unlock()
		// disconnected hook; a new one can resume
		return proto.Resolution{Dismiss: true, Reason: proto.DismissedWithdrawn}, nil
	}
	p.claimed, p.graceEnded = true, true
	p.expired = status == "expired"
	expiredIx := p.ix
	s.fleet.mu.Unlock()
	if status == "expired" && ownerOnlyTimedCard(expiredIx) {
		s.noteOwnerActionCardExpired(expiredIx)
	}
	s.recordEndBy(id, status, endedByOfStatus(status), nil)
	s.finishPending(p)
	// Why nobody answered it, so that whoever waits on it never takes the end for the user's decline: its time
	// ran out, or the one waiting went away first.
	return proto.Resolution{Dismiss: true, Reason: dismissedReason(status)}, nil
}

// dismissedReason is the Reason of the Dismiss await hands back for a card that ended with status (its history's)
// without an answer: its time ran out (expired), or the one waiting went away first (cancelled: withdrawn).
func dismissedReason(status string) string {
	if status == "expired" {
		return proto.DismissedExpired
	}
	return proto.DismissedWithdrawn
}

// orphanLocked starts p's grace period: it ends (cancelled) unless its hook claims it again first. Callers hold
// s.fleet.mu.
func (s *Service) orphanLocked(p *pending) bool {
	if persistentOwnerProposal(p.ix) {
		return true // no grace deadline: the proposal itself is reconstructable
	}
	if p.orphaned != nil {
		return true
	}
	wait := min(resumeGrace, time.Until(p.ix.ExpiresAt))
	status := "cancelled"
	if outboundSettingsCard(p.ix) {
		wait = time.Until(p.ix.ExpiresAt)
		status = "expired"
	}
	if wait <= 0 {
		return false
	}
	p.orphaned = time.AfterFunc(wait, func() {
		s.fleet.mu.Lock()
		// Claimed under the lock that decides it, as resolve claims an answer: an answer being written now is not
		// cancelled over, and one given while this end is written finds the card no longer pending.
		still := s.fleet.interactions[p.ix.ID] == p && p.orphaned != nil && !p.claimed
		if still {
			p.claimed, p.graceEnded = true, true
		}
		s.fleet.mu.Unlock()
		if still {
			s.recordEnd(p.ix.ID, status, nil)
			s.finishPending(p)
		}
	})
	return true
}

// checkRecordedInteractionOccurrence keeps a runtime's reused id from taking
// an earlier operation's pending state or answer. Ordinary zero creation times
// retain the legacy id-only resume behavior. Detachable original replays use
// exact call identity, and a late approval still never becomes permission to
// replay that original hook.
func (s *Service) checkRecordedInteractionOccurrence(ctx context.Context, ix proto.Interaction) error {
	if !ix.Detachable && ix.CreatedAt.IsZero() {
		return nil
	}
	st, ok := s.store().(interactionRecorder)
	if !ok {
		return nil
	}
	readCtx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	rec, err := st.InteractionRecord(readCtx, ix.ID)
	if err != nil {
		return err
	}
	if rec != nil && !sameApprovalOccurrence(ix, rec.Interaction) {
		return fmt.Errorf("%w: %s", errInteractionIDReused, ix.ID)
	}
	return nil
}

// checkRememberedInteractionOccurrence keeps an Always rule for a new request
// from resolving an older pending card with the same ID. A matching live card
// is the occurrence to answer; otherwise durable history makes the same check.
func (s *Service) checkRememberedInteractionOccurrence(ctx context.Context, ix proto.Interaction) error {
	s.fleet.mu.Lock()
	p := s.fleet.interactions[ix.ID]
	if p != nil {
		mismatch := (ix.Detachable || !ix.CreatedAt.IsZero()) && !sameApprovalOccurrence(ix, p.ix)
		s.fleet.mu.Unlock()
		if mismatch {
			return fmt.Errorf("%w: %s", errInteractionIDReused, ix.ID)
		}
		return nil
	}
	s.fleet.mu.Unlock()
	return s.checkRecordedInteractionOccurrence(ctx, ix)
}

// outcome reports how a resumable interaction no longer pending here ended: the answer given before its hook could
// read it (the manager or node restarted right then), or a dismiss when it was cancelled or expired. status is the
// history's ("pending": recorded, never ended).
func (s *Service) outcome(id string) (status string, r proto.Resolution, ended bool) {
	st := s.fleetStore()
	if st == nil {
		return "", proto.Resolution{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyTimeout)
	defer cancel()
	status, res, err := st.InteractionOutcome(ctx, id)
	switch {
	case err != nil || status == "" || status == "pending":
		return status, proto.Resolution{}, false
	case res != nil && (status == "resolved" || status == "cancelled" && res.Cancel):
		return status, *res, true
	default:
		r := proto.Resolution{Dismiss: true}
		if res != nil && res.Dismiss {
			r = *res
		}
		if r.Reason == "" && (status == "expired" || status == "cancelled") {
			r.Reason = dismissedReason(status)
		}
		return status, r, true
	}
}

// historyTimeout bounds each interaction history read or write. They run before the apps see a new card and before
// a hook hears its answer, so a slow or locked database must not hold either back for longer than this.
var historyTimeout = 5 * time.Second

// record / recordEnd keep the interaction history in the database (best effort, within historyTimeout).
func (s *Service) record(ix proto.Interaction) {
	st := s.fleetStore()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyTimeout)
	defer cancel()
	if err := st.InsertInteraction(ctx, ix); err != nil {
		s.logf("record interaction: %v", err)
	}
}

func (s *Service) recordEnd(id, status string, r *proto.Resolution) {
	if err := s.recordEndStrict(id, status, r); err != nil {
		s.logf("record interaction outcome: %v", err)
	}
}

// recordEndBy is recordEnd that keeps who or what ended the card (interactions.ended_by, db.EndedBy*): by, or when by
// is empty, what its status and its answer tell (endedByOf).
func (s *Service) recordEndBy(id, status, by string, r *proto.Resolution) {
	if by == "" {
		by = endedByOf(status, r)
	}
	if err := s.recordEndStrictBy(id, status, by, r); err != nil {
		s.logf("record interaction outcome: %v", err)
	}
}

func (s *Service) recordEndStrict(id, status string, r *proto.Resolution) error {
	return s.recordEndStrictBy(id, status, endedByOf(status, r), r)
}

func (s *Service) recordEndStrictBy(id, status, by string, r *proto.Resolution) error {
	st := s.fleetStore()
	if st == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(db.WithEndedBy(context.Background(), by), historyTimeout)
	defer cancel()
	return st.FinishInteraction(ctx, id, status, r)
}

// recordEndOccurrenceStrict requires a durable terminal record for this exact
// occurrence. A different pending occurrence under the same ID is left alone
// and keeps the journal undelivered; once that occurrence is terminal, its row
// itself prevents the older original from replaying through an Always rule.
func (s *Service) recordEndOccurrenceStrict(ix proto.Interaction, status string, r *proto.Resolution) error {
	return s.recordEndOccurrenceStrictBy(ix, status, endedByOf(status, r), r)
}

func (s *Service) recordEndOccurrenceStrictBy(ix proto.Interaction, status, by string, r *proto.Resolution) error {
	st := s.fleetStore()
	if st == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(db.WithEndedBy(context.Background(), by), historyTimeout)
	defer cancel()
	matched, currentTerminal, err := st.FinishInteractionOccurrence(ctx, ix, status, r)
	if err != nil {
		return err
	}
	if !matched && !currentTerminal {
		return fmt.Errorf("interaction %s now belongs to a different pending occurrence", ix.ID)
	}
	return nil
}

var errInteractionIDReused = errors.New("interaction id belongs to a different occurrence")

// endedByOf is who or what ended a card, as far as its history's status and the answer kept on it tell: someone who
// answered for the user (AnsweredBy), a kept "Always allow", its time running out, its asker going away, or a newer
// card. "" when they do not tell (an answer whose route does not say it came from a device).
func endedByOf(status string, r *proto.Resolution) string {
	if r != nil {
		switch {
		case r.AnsweredBy != "":
			return r.AnsweredBy
		case r.Remembered:
			return db.EndedByRemembered
		case r.Dismiss && (r.Reason == proto.DismissedCleared || r.Reason == "replaced"):
			return db.EndedByCleared
		case r.Dismiss && r.Reason == proto.DismissedWithdrawn:
			return db.EndedByWithdrawn
		case r.Dismiss && r.Reason == proto.DismissedExpired:
			return db.EndedByExpired
		}
	}
	return endedByOfStatus(status)
}

// endedByOfStatus is who or what ended a card nobody answered, by its history's status: its time ran out (expired), or
// its asker went away (cancelled). "" for any other status.
func endedByOfStatus(status string) string {
	switch status {
	case "expired":
		return db.EndedByExpired
	case "cancelled":
		return db.EndedByWithdrawn
	}
	return ""
}

// errNotPending: the interaction was answered, withdrawn or expired already.
var errNotPending = errors.New("no longer pending")

// answeredJustNow is errNotPending for a card another answer claimed a moment ago. That answer reaches the history
// only after the claim (recordEnd), so whoever asks in between is told it from the claim (#1230).
type answeredJustNow struct {
	id     string
	status string
	kept   proto.Resolution // as the history keeps it
}

func (e *answeredJustNow) Error() string {
	return fmt.Sprintf("interaction %s is %v", e.id, errNotPending)
}
func (e *answeredJustNow) Unwrap() error { return errNotPending }

// endStatus is how an answer ends its card in the history.
func endStatus(r proto.Resolution) string {
	switch {
	case r.Cancel:
		return "cancelled"
	case r.Dismiss:
		return "dismissed"
	}
	return "resolved"
}

var errInvalidElicitation = errors.New("elicitation requires action accept, decline or cancel")
var errMissingAllow = errors.New("permission resolution requires allow")
var errMissingApprove = errors.New("plan resolution requires approve")

func validateInteractionDecision(ix proto.Interaction, r proto.Resolution) error {
	if r.Cancel && (!ix.CanCancel || ix.Kind != proto.IxQuestion || !r.IsQuestionCancel()) {
		return errInvalidQuestionCancel
	}
	if r.Dismiss {
		return nil
	}
	switch {
	case ix.Kind == proto.IxPermission && r.Allow == nil:
		return errMissingAllow
	case ix.Kind == proto.IxPlan && r.Approve == nil:
		return errMissingApprove
	}
	return nil
}

var errInvalidEditedAnswer = errors.New("edited_text requires an editable permission card and a plain allow")

// editedAnswerAllowed rejects an edit before its card or channel owner action is consumed. The node still has to
// prove that the rewritten command has the same tool, gate, method, path and query; this checks only the answer envelope.
func editedAnswerAllowed(ix proto.Interaction, r proto.Resolution) bool {
	if r.EditedText == "" {
		return true
	}
	return ix.Kind == proto.IxPermission && ix.EditableText != "" && len(ix.Fields) == 0 &&
		strings.TrimSpace(r.EditedText) != "" && r.PlainAllowWithEditedText()
}

// resolve answers an interaction. A screen card acts right away and its error comes back (the dialog changed:
// nothing pressed, a new card follows).
func (s *Service) resolve(id string, r proto.Resolution) error {
	return s.resolveRemembered(id, r, true)
}

// interactionExpectation is checked under the same claim lock as resolution.
// Automatic remembered answers carry the whole occurrence because a detachable
// call can be replaced even when its timestamp changes; legacy zero-time
// non-detachable requests retain their old ID-only behavior.
type interactionExpectation struct {
	createdAt  *time.Time
	occurrence *proto.Interaction
}

func (e *interactionExpectation) matches(ix proto.Interaction) bool {
	if e == nil {
		return true
	}
	if e.occurrence != nil {
		if !e.occurrence.Detachable && e.occurrence.CreatedAt.IsZero() {
			return true
		}
		return sameApprovalOccurrence(*e.occurrence, ix)
	}
	return e.createdAt == nil || ix.CreatedAt.Equal(*e.createdAt)
}

// Automatically replayed grants must not write themselves back after a concurrent revocation.
func (s *Service) resolveRemembered(id string, r proto.Resolution, remember bool) error {
	return s.resolveWithStanding(id, r, remember, nil)
}

func (s *Service) resolveRememberedOccurrence(ix proto.Interaction, r proto.Resolution) error {
	return s.resolveAnswerAt(context.Background(), ix.ID, r, false, nil, nil, &interactionExpectation{occurrence: &ix})
}

func (s *Service) resolveWithStanding(id string, r proto.Resolution, remember bool, authorization *standingResolution) error {
	return s.resolveAnswer(context.Background(), id, r, remember, authorization, nil)
}

// resolveApproved is resolve for an app's answer the hub relayed, with the device it vouched may approve a hand-off
// card (nil: none). Only this way does a hand-off card take more than a no (secret_handoff.go).
func (s *Service) resolveApproved(ctx context.Context, id string, r proto.Resolution, approver *assistantwire.HandoffApprover) error {
	return s.resolveApprovedAt(ctx, id, r, approver, nil)
}

func (s *Service) resolveApprovedAt(ctx context.Context, id string, r proto.Resolution, approver *assistantwire.HandoffApprover, expected *time.Time) error {
	if strings.HasPrefix(id, outboundSettingsCardPrefix) {
		return s.resolveOutboundSettingsCard(ctx, id, r)
	}
	if mine, err := s.sessionReviewResolve(ctx, id, r, approver); mine {
		return err
	}
	s.fleet.mu.Lock()
	prepared := s.fleet.interactions[id]
	ownerSession := ""
	if prepared != nil && prepared.prepared != nil && !r.Cancel {
		ownerSession = prepared.ix.SessionID
	}
	s.fleet.mu.Unlock()
	var expectation *interactionExpectation
	if expected != nil {
		expectation = &interactionExpectation{createdAt: expected}
	}
	if prepared != nil && ownerActionProposal(prepared.ix) {
		return s.resolveOwnerProposalAt(ctx, prepared.ix, r, ownerActionCardProof(prepared.ix, "app"), expectation)
	}
	if ownerSession != "" {
		// The web route resolves prepared cards directly instead of through Action;
		// preserve the same owner-activity notification as every other answer path.
		s.ownerHeard(ownerSession)
	}
	return s.resolveAnswerAt(ctx, id, r, true, nil, approver, expectation)
}

func (s *Service) resolveAnswer(ctx context.Context, id string, r proto.Resolution, remember bool, authorization *standingResolution,
	approver *assistantwire.HandoffApprover) error {
	return s.resolveAnswerAt(ctx, id, r, remember, authorization, approver, nil)
}

func (s *Service) resolveAnswerAt(ctx context.Context, id string, r proto.Resolution, remember bool, authorization *standingResolution,
	approver *assistantwire.HandoffApprover, expected *interactionExpectation) error {
	if strings.HasPrefix(id, outboundSettingsCardPrefix) {
		return errOutboundOwnerAnswer
	}
	return s.logicalInteractionAnswer(ctx, id, r, func() error {
		return s.resolveActiveAnswer(ctx, id, r, remember, authorization, approver, expected)
	})
}

func (s *Service) resolveActiveAnswer(ctx context.Context, id string, r proto.Resolution, remember bool, authorization *standingResolution,
	approver *assistantwire.HandoffApprover, expected *interactionExpectation) error {
	r.ModeActor = nil // provenance is never taken from an answer body
	// Who answered, for the history (interactions.ended_by): the route the apps answer through says it came from a
	// device (db.WithEndedBy); someone answering for the user, or a kept "Always", says so in the answer itself.
	by := cmpOr(endedByOf("", &r), db.EndedBy(ctx))
	// A manager work card is answered in the manager, not by the stopped agent. Its local dismiss/defer choices remain
	// valid, but an answer that would send text must be refused before the card is claimed and its answer is retried.
	if ix, ok := s.pendingIx(id); ok && ix.ToolName == workCardTool && workResolutionSends(ix, r) {
		if sess, found := s.decoratedSession(ix.MachineID, ix.SessionID); found && stoppedAgent(sess) {
			return &rpc.Error{Code: "agent_stopped", Message: "the agent has stopped and cannot receive this answer"}
		}
	}
	s.fleet.mu.Lock()
	p := s.fleet.interactions[id]
	if p != nil && p.prepared != nil {
		ix := p.ix
		s.fleet.mu.Unlock()
		if err := validateInteractionDecision(ix, r); err != nil {
			return err
		}
		return s.resolvePreparedAnswerAt(ctx, p, r, remember, authorization, expected)
	}
	if p != nil && !expected.matches(p.ix) {
		s.fleet.mu.Unlock()
		return fmt.Errorf("interaction %s is %w", id, errNotPending)
	}
	handoff := p != nil && p.ix.ToolName == proto.SecretHandoffTool
	if handoff && !proto.SecretHandoffDeclines(r) && (approver == nil || approver.DeviceID == "" || authorization != nil) {
		s.fleet.mu.Unlock()
		return errHandoffElsewhere // a channel, the assistant, a remembered answer, an older hub: a no only
	}
	if p != nil && proto.SecretRunEnvInteraction(p.ix) && !proto.SecretHandoffDeclines(r) &&
		(approver == nil || approver.DeviceID == "" || authorization != nil) {
		s.fleet.mu.Unlock()
		return errSecretRunEnv // an older specialized client would show no env warning before its Run
	}
	if p != nil && !p.claimed {
		if handoff && reflect.ValueOf(r).IsZero() {
			// A vouched-for device's legacy empty hand-off answer is a decline.
			deny := false
			r.Allow = &deny
		}
		if err := validateInteractionDecision(p.ix, r); err != nil {
			s.fleet.mu.Unlock()
			return err
		}
	}
	if p != nil && !editedAnswerAllowed(p.ix, r) {
		s.fleet.mu.Unlock()
		return errInvalidEditedAnswer
	}
	if p != nil && p.ix.Kind == proto.IxElicitation && !r.Dismiss &&
		r.Action != "accept" && r.Action != "decline" && r.Action != "cancel" {
		s.fleet.mu.Unlock()
		return errInvalidElicitation
	}
	if p != nil && r.Allow != nil && *r.Allow {
		for _, f := range p.ix.Fields {
			if !f.Optional && strings.TrimSpace(r.Values[f.Key]) == "" {
				s.fleet.mu.Unlock()
				return i18n.Err("hub-hub.fill_in", i18n.Args{"field": f.Label})
			}
		}
	}
	if p != nil {
		if p.ix.Kind == proto.IxPlan {
			r.ModeActor = devicewire.ModeActorOf(ctx)
			if r.ModeActor == nil && r.AnsweredBy == proto.AnsweredByAssistant {
				r.ModeActor = devicewire.AssistantModeActor()
			}
		}
		r.Values = declaredValues(p.ix, r.Values)
		r = honest(p.ix, r)
	}
	if p != nil && p.persisting != nil {
		attempt := p.persisting
		s.fleet.mu.Unlock()
		<-attempt.done
		if attempt.retry {
			return errOwnerActionConflict
		}
		if attempt.err != nil {
			return attempt.err
		}
		return fmt.Errorf("interaction %s is %w", id, errNotPending)
	}
	var first *answeredJustNow
	if p != nil && p.claimed {
		if p.resolution != nil {
			first = &answeredJustNow{id: id, status: endStatus(*p.resolution), kept: redacted(p.ix, *p.resolution)}
		}
		p = nil // answered a moment ago: the first answer is the one the hook and the history get
	}
	if p != nil && p.local != nil && authorization != nil {
		s.fleet.mu.Unlock()
		return fmt.Errorf("standing authorizations require a hook-waited card")
	}
	if p != nil && p.local != nil {
		p.stopReminderLocked()
		delete(s.fleet.interactions, id) // claimed: a second answer to the same card is not pending any more
		if p.ix.Kind == proto.IxScreen && !r.Dismiss {
			// Marked before the keys go: the node's next list may come while they are on the way (#4391).
			if s.fleet.screenAnswered == nil {
				s.fleet.screenAnswered = map[string]answeredScreen{}
			}
			s.fleet.screenAnswered[id] = answeredScreen{machine: p.ix.MachineID, session: p.ix.SessionID,
				until: time.Now().Add(screenAnsweredQuiet)}
		} else if p.ix.Kind == proto.IxScreen {
			// Handed back to the computer: no key goes, the dialog stays, and the next list must not ask it again.
			if s.fleet.screenHandedBack == nil {
				s.fleet.screenHandedBack = map[string]screenGone{}
			}
			s.fleet.screenHandedBack[id] = screenGone{machine: p.ix.MachineID, session: p.ix.SessionID, at: time.Now()}
		}
	} else if p != nil {
		p.claimed = true
		p.resolution = &r
		p.endedBy = by
		if durableQuestion(p.ix) || authorization != nil {
			p.persisting = &questionPersistence{done: make(chan struct{})}
			p.persistenceErr = nil
		}
		if p.resolved == nil {
			p.resolved = make(chan struct{})
		}
	}
	if p != nil && handoff {
		s.claimHandoffLocked(id, r, approver) // claimed with this answer: an approval, and who gave it
	}
	if p != nil {
		p.answeredBy, p.answeredReason = r.AnsweredBy, r.Reason // read by finished(), under this lock
		if r.AnsweredBy != "" {
			p.answeredWith = r.Answers
		}
	}
	s.fleet.mu.Unlock()
	if first != nil {
		return first
	}
	if p == nil {
		return fmt.Errorf("interaction %s is %w", id, errNotPending)
	}
	status := endStatus(r)
	// Everyone told of the answer (the channels, the task feeds) gets its secret fields masked, as the history keeps them;
	// only the one waiting on the card (p.local, p.done) gets what was typed into them.
	told := redacted(p.ix, r)
	if p.local != nil {
		err := p.local(ctx, r)
		if err != nil {
			status = "failed"
			s.fleet.mu.Lock()
			back := s.fleet.interactions[id] == p
			s.fleet.mu.Unlock()
			if back { // a Codex card the node could not answer is open again (codexPending): it is not over
				s.interactionResolvedSoon(context.Background(), InteractionResolved{Interaction: p.ix, Resolution: told, Status: status})
				return err
			}
		} else {
			s.channelsEnded(p.ix, &told)
		}
		// What was typed into a terminal may be a password: the history keeps that something was, not what.
		kept := told
		if kept.Text != "" {
			kept.Text = redactedValue
		}
		if err := s.recordEndOccurrenceStrictBy(p.ix, status, by, &kept); err != nil {
			s.logf("record local interaction occurrence outcome: %v", err)
		}
		s.finished(p)
		s.interactionResolvedSoon(context.Background(), InteractionResolved{Interaction: p.ix, Resolution: told, Status: status})
		return err
	}
	if authorization != nil {
		if err := s.persistStandingInteraction(p, told, authorization); err != nil {
			return err
		}
	} else if durableQuestion(p.ix) {
		if err := s.persistQuestion(p, status, told); err != nil {
			return err
		}
	} else {
		s.recordEndBy(id, status, by, &told)
	}
	s.channelsEnded(p.ix, &told)
	if ownerOnlyTimedCard(p.ix) {
		s.clearOwnerActionCardWait(p.ix)
	}
	s.interactionResolvedSoon(context.Background(), InteractionResolved{Interaction: p.ix, Resolution: told, Status: status})
	if remember {
		s.keepAlways(p.ix, r) // kept before the hook hears: its next card may follow at once
	}
	close(p.resolved)
	select {
	case p.done <- r:
	default:
	}
	s.finishPending(p)
	return nil
}

// channelsEnded tells the channels a card is over: r is how it was answered (nil: withdrawn, expired, or over after
// the answer that was told already).
func (s *Service) channelsEnded(ix proto.Interaction, r *proto.Resolution) {
	if f := s.fleetEnded; f != nil {
		f(ix, r)
	}
	s.InteractionEnded(context.Background(), InteractionEnded{Interaction: ix, Resolution: r})
}

// redactedValue stands for what was typed into a secret field (or a terminal) wherever the answer is kept or told.
const redactedValue = "<redacted>"

// redacted is the resolution as the history keeps it: secret fields say they were filled in, not what with. A secret
// card's typed text is masked here too; a terminal's other text reaches the channels as typed and only the history masks
// it (resolveRemembered's kept).
func redacted(ix proto.Interaction, r proto.Resolution) proto.Resolution {
	if ix.Secret && r.Text != "" {
		r.Text = redactedValue
	}
	if len(r.Values) > 0 {
		secret := map[string]bool{}
		for _, f := range ix.Fields {
			secret[f.Key] = f.Secret
		}
		vals := make(map[string]string, len(r.Values))
		for k, v := range r.Values {
			if secret[k] || secretName.MatchString(k) {
				v = redactedValue
			}
			vals[k] = v
		}
		r.Values = vals
	}
	if len(r.Content) > 0 {
		var schema struct {
			Properties map[string]struct {
				Format    string `json:"format"`
				WriteOnly bool   `json:"writeOnly"`
			} `json:"properties"`
		}
		if ix.Elicitation != nil {
			_ = json.Unmarshal(ix.Elicitation.RequestedSchema, &schema)
		}
		content := make(map[string]any, len(r.Content))
		for k, v := range r.Content {
			p := schema.Properties[k]
			if p.Format == "password" || p.WriteOnly || secretName.MatchString(k) {
				v = redactedValue
			}
			content[k] = v
		}
		r.Content = content
	}
	return r
}

// declaredValues are the values of the fields ix declares; any other key is dropped as the answer comes in: nothing
// reads one, and the history and everyone told of the answer would get it as typed.
func declaredValues(ix proto.Interaction, values map[string]string) map[string]string {
	if len(values) == 0 {
		return values
	}
	var out map[string]string
	for _, f := range ix.Fields {
		if v, ok := values[f.Key]; ok {
			if out == nil {
				out = map[string]string{}
			}
			out[f.Key] = v
		}
	}
	return out
}

// hasSecretField: ix asks for something the history redacts (a password).
func hasSecretField(ix proto.Interaction) bool {
	for _, f := range ix.Fields {
		if f.Secret {
			return true
		}
	}
	return false
}

// takePending atomically removes only the exact occurrence the caller finished.
// A stale waiter, orphan timer or prepared finalizer must not delete a same-ID
// replacement that has already been admitted.
func (s *Service) takePending(expected *pending) *pending {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	p := s.fleet.interactions[expected.ix.ID]
	if p != expected {
		return nil
	}
	delete(s.fleet.interactions, expected.ix.ID)
	if p.orphaned != nil {
		p.orphaned.Stop()
	}
	return p
}

func (s *Service) finishPending(expected *pending) {
	if p := s.takePending(expected); p != nil {
		s.finished(p)
	}
}

// finishInteraction is for lifecycle callers that genuinely have only an ID.
// Occurrence-holding waiters and finalizers use finishPending instead.
func (s *Service) finishInteraction(id string) {
	if routes, ok := s.store().(logicalInteractionStore); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = routes.FinishConversationInteraction(ctx, id)
		cancel()
	}
	s.fleet.mu.Lock()
	p, ok := s.fleet.interactions[id]
	delete(s.fleet.interactions, id)
	if ok {
		if p.orphaned != nil {
			p.orphaned.Stop()
		}
		p.stopReminderLocked()
	}
	s.fleet.mu.Unlock()
	if ok {
		s.finished(p)
	}
}

// finishExpiredPending retains the logical-route finalization of finishInteraction
// while removing only the occurrence collected by the expiry pass. The database
// call stays outside fleet.mu; a same-ID replacement admitted meanwhile is not
// removed or announced as ended by the older card's expiry.
func (s *Service) finishExpiredPending(expected *pending) {
	p := s.takePending(expected)
	if p == nil {
		return
	}
	if routes, ok := s.store().(logicalInteractionStore); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = routes.FinishConversationInteraction(ctx, p.ix.ID)
		cancel()
	}
	s.finished(p)
}

// finished tells everyone an interaction, already out of the pending set, is over.
func (s *Service) finished(p *pending) {
	s.taskPlanApproval()
	s.fleet.mu.Lock()
	ix := p.ix
	done := assistantwire.FleetResolved{ID: ix.ID, CreatedAt: ix.CreatedAt, AnsweredBy: p.answeredBy}
	if done.AnsweredBy != "" {
		done.Reason, done.Answers = p.answeredReason, p.answeredWith
	}
	var how *proto.Resolution
	if p.expired && ix.ToolName == "ask_user" { // the assistant's question becomes a to-do (ask_todo.go): its card says so
		how = &proto.Resolution{Dismiss: true, Reason: proto.DismissedExpired}
	}
	resyncScreen := p.expired && p.local == nil && !ix.NoHandBack &&
		(ix.Kind == proto.IxPermission || ix.Kind == proto.IxQuestion && ix.ToolName == "AskUserQuestion")
	s.fleet.mu.Unlock()
	go s.urgentCardDone(ix.ID) // a call about it stops ringing (urgent_call_loop.go)
	if how != nil && s.askLaterSpokeOf(ix.ID) {
		// Asked without waiting, and the owner wrote in the conversation meanwhile: it makes no to-do (ask_later.go).
		how.Reason = proto.DismissedInChat
	}
	s.channelsEnded(ix, how)
	// the app marks a question the assistant answered, and why (docs/assistant/assistant.md)
	s.tellResolved(done)
	s.publishSessions(ix.MachineID)
	s.cardStateChanged(ix)
	if resyncScreen {
		s.syncScreenAfterHookExpiry(ix.MachineID, ix.SessionID)
	}
}

// The hub treats omissions from a reconnect inventory as resolutions. Order the read and the
// write together so a delayed snapshot cannot remove a new card or restore a withdrawn one.
func (s *Service) tellPendingInteractions(ctx context.Context) {
	s.fleet.cardTell.Lock()
	defer s.fleet.cardTell.Unlock()
	s.fleet.cardEnd.Lock()
	defer s.fleet.cardEnd.Unlock()
	s.hub.Tell(assistantwire.EventFleetInteractions, assistantwire.FleetInteractions{Interactions: s.pendingDisplayInteractions(ctx)})
}

func (s *Service) tellResolved(done assistantwire.FleetResolved) {
	s.fleet.cardEnd.Lock()
	defer s.fleet.cardEnd.Unlock()
	s.hub.Tell(assistantwire.EventFleetResolved, done)
}

// pendingInteractions is the authoritative pending inventory for transport and lifecycle operations.
// Visibility projections must never remove cards from reconnect snapshots.
func (s *Service) pendingInteractions() []proto.Interaction {
	out, _ := s.pendingCards()
	return out
}

// pendingCards is pendingInteractions and, read with them, the ids of those that are the user's (pending.told).
func (s *Service) pendingCards() ([]proto.Interaction, map[string]bool) {
	s.fleet.mu.Lock()
	out := []proto.Interaction{}
	told := map[string]bool{}
	for _, p := range s.fleet.interactions {
		out = append(out, p.ix)
		if p.told {
			told[p.ix.ID] = true
		}
	}
	s.fleet.mu.Unlock()
	reviewCards, _ := s.sessionReviewInteractions(context.Background())
	reviewCurrent := map[string]bool{}
	for _, ix := range reviewCards {
		reviewCurrent[ix.ID] = true
	}
	seen := map[string]bool{}
	unique := out[:0]
	for _, ix := range out {
		if ix.ToolName == sessionReviewTool && !reviewCurrent[ix.ID] {
			continue // the book's terminal outcome wins while the queue's withdrawal is still on its way
		}
		if !seen[ix.ID] {
			seen[ix.ID] = true
			unique = append(unique, ix)
		}
	}
	for _, ix := range reviewCards {
		if !seen[ix.ID] {
			seen[ix.ID] = true
			unique = append(unique, ix)
		}
	}
	out = unique
	for i := range out {
		out[i] = s.withTask(out[i])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, told
}

// visiblePendingCards is the ambient projection, never the authoritative transport inventory.
func (s *Service) visiblePendingCards() ([]proto.Interaction, map[string]bool) {
	out, told := s.pendingCards()
	out = slices.DeleteFunc(out, func(ix proto.Interaction) bool {
		if !s.directoryHiddenSession(ix.MachineID, ix.SessionID) {
			return false
		}
		delete(told, ix.ID)
		return true
	})
	return out, told
}

func (s *Service) visiblePendingInteractions() []proto.Interaction {
	out, _ := s.visiblePendingCards()
	return out
}

// refreshPendingCards replays actionable state after session visibility changes.
// It neither marks a card as told nor sends pushes or delegates it again. The cards
// are read under fleet.mu and told through tellCard after it is released: a write
// that stalls on the hub's connection must not hold up pendingIx or resolve, and
// tellCard tells a card that ended meanwhile ended again, so none is resurrected.
func (s *Service) refreshPendingCards() {
	s.fleet.mu.Lock()
	replay := make([]proto.Interaction, 0, len(s.fleet.interactions))
	for _, p := range s.fleet.interactions {
		if !p.claimed {
			replay = append(replay, p.ix)
		}
	}
	s.fleet.mu.Unlock()
	sort.Slice(replay, func(i, j int) bool { return replay[i].CreatedAt.Before(replay[j].CreatedAt) })
	for _, ix := range replay {
		s.tellCard(s.withTask(ix))
	}
}

// pendingOf are the pending interactions of the sessions in want (by computer, the session ids), by computer and
// session, as they are held, in no order: without the task each agent works for (withTask), which a to-do's view
// does not read. Claimed cards are skipped, machine IDs are canonicalized, and folded sub-agent / side-session cards
// are resolved to their folded parent outside fleet.mu so to-do projections see supervisor-held child cards.
func (s *Service) pendingOf(want map[string]map[string]bool) map[[2]string][]proto.Interaction {
	out := map[[2]string][]proto.Interaction{}
	if len(want) == 0 {
		return out
	}
	s.fleet.mu.Lock()
	snap := make([]proto.Interaction, 0, len(s.fleet.interactions))
	for _, p := range s.fleet.interactions {
		if !p.claimed {
			snap = append(snap, p.ix)
		}
	}
	s.fleet.mu.Unlock()
	if len(snap) == 0 {
		return out
	}
	canonWant := make(map[string]map[string]bool, len(want))
	for m, ids := range want {
		cm := s.canonical(m)
		if canonWant[cm] == nil {
			canonWant[cm] = map[string]bool{}
		}
		for sid, ok := range ids {
			if ok {
				canonWant[cm][sid] = true
			}
		}
	}
	seen := map[[2]string]map[string]bool{}
	emit := func(m, sid string, ix proto.Interaction) {
		if !canonWant[m][sid] {
			return
		}
		k := [2]string{m, sid}
		if seen[k] == nil {
			seen[k] = map[string]bool{}
		}
		if ix.ID != "" && seen[k][ix.ID] {
			return
		}
		if ix.ID != "" {
			seen[k][ix.ID] = true
		}
		out[k] = append(out[k], ix)
	}
	for _, ix := range snap {
		mid := s.canonical(ix.MachineID)
		ix.MachineID = mid
		if ix.FoldedUnder == "" {
			underMid, under, as, side := s.foldedUnderWithMachine(mid, ix.SessionID)
			if under != "" {
				ix.FoldedUnder = under
				ix.FoldedAs = as
				ix.FoldedSide = side
				if underMid != "" {
					ix.FoldedUnderMachineID = s.canonical(underMid)
				}
			}
		} else if ix.FoldedUnderMachineID != "" {
			ix.FoldedUnderMachineID = s.canonical(ix.FoldedUnderMachineID)
		}
		emit(mid, ix.SessionID, ix)
		if ix.FoldedUnder != "" {
			emit(cmp.Or(ix.FoldedUnderMachineID, mid), ix.FoldedUnder, ix)
		}
		if ix.FoldedSide != "" {
			emit(mid, ix.FoldedSide, ix)
		}
	}
	return out
}

// heldOnlySessions returns canonical (machine, session) keys whose pending cards (direct or folded) are all held by a
// supervisor or assistant (Delegating == true), with at least one pending card and zero non-delegating owner cards.
func (s *Service) heldOnlySessions() map[[2]string]bool {
	s.fleet.mu.Lock()
	snap := make([]proto.Interaction, 0, len(s.fleet.interactions))
	for _, p := range s.fleet.interactions {
		if !p.claimed {
			snap = append(snap, p.ix)
		}
	}
	s.fleet.mu.Unlock()
	if len(snap) == 0 {
		return nil
	}
	type flags struct {
		held bool
		user bool
	}
	bySession := map[[2]string]flags{}
	mark := func(m, sid string, delegating bool) {
		if m == "" || sid == "" {
			return
		}
		k := [2]string{m, sid}
		st := bySession[k]
		if delegating {
			st.held = true
		} else {
			st.user = true
		}
		bySession[k] = st
	}
	for _, ix := range snap {
		mid := s.canonical(ix.MachineID)
		underMid, under, _, side := s.foldedUnderWithMachine(mid, ix.SessionID)
		if under == "" && ix.FoldedUnder != "" {
			under, underMid, side = ix.FoldedUnder, ix.FoldedUnderMachineID, ix.FoldedSide
		}
		mark(mid, ix.SessionID, ix.Delegating)
		if under != "" {
			mark(s.canonical(cmp.Or(underMid, mid)), under, ix.Delegating)
		}
		if side != "" {
			mark(mid, side, ix.Delegating)
		}
	}
	out := map[[2]string]bool{}
	for k, st := range bySession {
		if st.held && !st.user {
			out[k] = true
		}
	}
	return out
}

// cardStateChanged refreshes todo views and the direction tree when a card is routed, escalated, or resolved, even if
// the machine's serialized session list did not change (for example, flipping Delegating on a pending card or updating
// a cross-machine folded sub-agent's card).
func (s *Service) cardStateChanged(ix proto.Interaction) {
	mid := s.canonical(ix.MachineID)
	underMid, under, _, _ := s.foldedUnderWithMachine(mid, ix.SessionID)
	if under == "" && ix.FoldedUnder != "" {
		under, underMid = ix.FoldedUnder, ix.FoldedUnderMachineID
	}
	notify := func(machine string) {
		if machine == "" {
			return
		}
		s.todoRunsChanged(machine)
		if s.todoRunsOn(machine) {
			s.directionTreeSoon()
		}
	}
	notify(mid)
	if under != "" {
		parentMid := s.canonical(cmp.Or(underMid, mid))
		if parentMid != mid {
			notify(parentMid)
		}
	}
}

// pendingIx is one pending interaction; false when it is not pending.
func (s *Service) pendingIx(id string) (proto.Interaction, bool) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if p := s.fleet.interactions[id]; p != nil {
		return p.ix, true
	}
	return proto.Interaction{}, false
}

func (s *Service) expireLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.retrySeen(ctx)                // a database that failed when the manager started
		s.republishFinished(time.Now()) // Just finished rows whose window ended (#9751)
		s.fleet.mu.Lock()
		var plans []string
		seen := map[string]bool{}
		for _, p := range s.fleet.interactions {
			if terminalReconciled(p) && !seen[p.ix.MachineID] {
				plans = append(plans, p.ix.MachineID)
				seen[p.ix.MachineID] = true
			}
		}
		s.fleet.mu.Unlock()
		for _, m := range plans {
			s.syncPlanInteractions(m) // also converge after a lost rev/status event
		}
		s.expireLocalInteractions(time.Now())
	}
}

// expireLocalInteractions finishes local cards whose deadlines have passed.
// The tick supplies one cutoff for the whole pass.
func (s *Service) expireLocalInteractions(now time.Time) {
	s.fleet.mu.Lock()
	var expired []*pending
	for _, p := range s.fleet.interactions {
		if p.local != nil && !p.ix.ExpiresAt.IsZero() && now.After(p.ix.ExpiresAt) {
			expired = append(expired, p)
		}
	}
	s.fleet.mu.Unlock()
	for _, p := range expired {
		s.finishExpiredPending(p)
	}
}

// errReservedCard: a card from a node (a hook, a runtime, a daemon, a Codex list) or an extension may not pass for the
// password card, the hand-off card, an approval-display identity or a command-input card: only the assistant's
// secret_run tool makes the first (secret_run.go), only the manager's Agent start the second, display identity is
// manager-owned, and only a node's own card list the fourth (fleet_ix_command.go).
var errReservedCard = errors.New("the tool names " + proto.SecretRunTool + ", " + proto.SecretHandoffTool + " and " +
	proto.CommandInputTool + ", and approval display IDs, are reserved for manager-owned cards")

// reservedCard: ix calls itself the password card, the hand-off card, a command-input card, or an
// authoritative operation inside the display-copy ID namespace.
func reservedCard(ix proto.Interaction) bool {
	return ix.ToolName == proto.SecretRunTool || ix.ToolName == proto.SecretHandoffTool || ix.ToolName == proto.CommandInputTool ||
		strings.HasPrefix(ix.ID, proto.ApprovalMirrorIDPrefix) ||
		strings.HasPrefix(ix.ID, outboundSettingsCardPrefix)
}

// askHub is fleet.ask: a card one of the hub's own features asks (a mail send, Cua's apps, persona_settings),
// waited for like a hook's.
func (s *Service) askHub(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	if reservedCard(ix) {
		return proto.Resolution{}, errReservedCard
	}
	if ix.ID == "" {
		ix.ID = "ix_" + randomHex(6)
	}
	if ix.CreatedAt.IsZero() {
		ix.CreatedAt = time.Now().UTC()
	}
	if m, ok := s.fleetMachine(ix.MachineID); ok {
		ix.MachineID = m.ID
	}
	if ix.Kind == proto.IxPermission && !ix.OwnerProposal && len(ix.ToolInput) > 0 {
		if book, ok := newOwnerActionBook(s.store()); ok {
			granted, err := book.consume(ctx, ix.MachineID, ix.SessionID, ix.ToolName, ix.ToolInput)
			if err != nil {
				return proto.Resolution{}, err
			}
			if granted {
				allow := true
				return proto.Resolution{Allow: &allow, Reason: "exact owner-action grant"}, nil
			}
		}
	}
	return s.waitInteraction(ctx, s.ownCard(ctx, ix))
}

// ---- "Always allow in this session" (#2202, docs/assistant/assistant.md "Always allow in this session") ----

// The node names what a card grants (proto.Interaction.AlwaysKeys, only on assistant conversations). The manager
// remembers explicit Always answers and rechecks every later card against the stored keys, including after a
// process restart. Nodes must not cache session grants that would bypass a subsequent revocation (#5867).

// maxAlwaysKeys bounds what one card may name (a file change's paths, a gated command's calls).
const maxAlwaysKeys = 32

// Whether a card's "Always allow" is kept is the manager's word (proto.Interaction.AlwaysHonored, #3590), never a
// node's: the apps offer it only where it is.

// nodeCard is a node's card as the apps get it. On an assistant conversation (assistant) only the manager keeps
// Always (its agent is given no session rule): honored when the manager will remember the keys the card names
// (remembering is a conversation's store), not when it names keys it will not (too many, a card with fields, no store),
// names none (a gated command confirmed whole: nothing narrower to keep, #9075) or is a card of a space that confirms
// (confirms: confirmed each time). Any other node card keeps the apps' own rule (an Agents session's agent keeps it itself). A
// node names no tool for people, and has no say in either: only the manager does, on its own cards and on a node's
// card for one of Persona's own MCP tools (personaToolTitle).
func nodeCard(ix proto.Interaction, remembers, confirms, assistant bool) proto.Interaction {
	ix.AlwaysHonored, ix.ToolTitle, ix.ToolNameTitle = nil, "", ""
	if ix.Kind != proto.IxPermission || ix.SessionID == "" || (len(ix.AlwaysKeys) == 0 && !confirms && !assistant) {
		return ix
	}
	kept := remembers && !confirms && alwaysCard(ix)
	ix.AlwaysHonored = &kept
	return ix
}

// ixInAssistant: ix is a card of one of this manager's assistant conversations (isAssistant), by the working directory
// the node gave it, else the conversation's as the machine lists it.
func (s *Service) ixInAssistant(ctx context.Context, ix proto.Interaction) bool {
	m, ok := s.fleetMachine(ix.MachineID)
	if !ok || ix.SessionID == "" {
		return false
	}
	sess := proto.Session{SessionID: ix.SessionID, Cwd: ix.Cwd}
	if sess.Cwd == "" {
		if c, ok := s.nodeSession(ix.MachineID, ix.SessionID); ok {
			sess = c
		} else if c, ok := s.session(ctx, ix.MachineID, ix.SessionID); ok {
			sess = c
		}
	}
	return m.isAssistant(sess)
}

// honest is r without an Always the card does not keep (AlwaysHonored false): an older app, or one that does not
// read the field, may still send it, and the answer's history must not say "always allowed" for a grant no one keeps.
// An approved plan likewise uses the mode its start recorded, not an older client's hard-coded mode.
func honest(ix proto.Interaction, r proto.Resolution) proto.Resolution {
	if ix.Kind == proto.IxPlan && r.Approve != nil && *r.Approve {
		if mode := proto.PlanAfterMode(ix.PlanThen); mode != "" {
			r.Mode = mode
		}
	}
	if ix.AlwaysHonored != nil && !*ix.AlwaysHonored {
		r.Always = false
	}
	return r
}

// ownCard is a permission card the manager asks itself (its tools', the extensions', the hub's persona_settings): its
// Always is kept only where the card says what it keeps (AlwaysLabel: the apps a computer may use, LCU's own scopes),
// nowhere else, and the tool gets its name for people when the catalog has one. Manager-owned permission cards have no
// computer terminal to hand back to, so NoHandBack is always set.
func (s *Service) ownCard(ctx context.Context, ix proto.Interaction) proto.Interaction {
	if ix.Kind != proto.IxPermission {
		return ix
	}
	ix.NoHandBack = true
	kept := strings.TrimSpace(ix.AlwaysLabel) != ""
	ix.AlwaysHonored = &kept
	if ix.ToolTitle == "" {
		ix.ToolTitle = toolTitle(s.lang(ctx), ix.ToolName)
	}
	return ix
}

// permissionNoHandBack reports whether a node permission card has no interactive computer terminal or UI to hand
// back to: headless assistant conversations (Claude -p, Codex app-server, AGY ACP), command-gate approvals
// (Detachable / OutwardWrite), and headless ACP companions (Antigravity, Grok / grok-chat, builtin OpenCode
// companion) where dismiss cancels or denies instead of handing the request back to a terminal.
func (s *Service) permissionNoHandBack(ix proto.Interaction, inAssistant bool) bool {
	if ix.Kind != proto.IxPermission {
		return false
	}
	if inAssistant || ix.Detachable || ix.OutwardWrite {
		return true
	}
	if row, ok := s.nodeSession(ix.MachineID, ix.SessionID); ok {
		if row.PaneID == "" {
			if row.Assistant {
				return true
			}
			if m, ok := s.fleetMachine(ix.MachineID); ok && m.isAssistant(row) {
				return true
			}
		}
		if row.SourceKind == proto.SourceRuntime && row.SourceInstanceID != "" {
			if interactivePermissionRuntimeHost(row.SourceInstanceID) {
				return false
			}
			if headlessPermissionRuntimeInstance(row.SourceInstanceID) {
				return true
			}
			return headlessPermissionAgent(row.Agent) || headlessPermissionAgent(ix.Agent)
		}
		if headlessPermissionAgent(row.Agent) {
			return true
		}
	}
	return headlessPermissionAgent(ix.Agent)
}

// interactivePermissionRuntimeHost reports runtime hosts with a local computer TUI, desktop app, or web UI
// where handing a permission card back (dismiss) lets the user answer it there instead of cancelling the call.
func interactivePermissionRuntimeHost(instanceID string) bool {
	switch instanceID {
	case "paseo", "orca", "cindy", "dsh", "managed-agents":
		return true
	}
	return strings.HasPrefix(instanceID, "opencode-") || strings.HasPrefix(instanceID, "dsh-")
}

// headlessPermissionRuntimeInstance reports runtime instance IDs whose own permission bridge is headless ACP.
func headlessPermissionRuntimeInstance(instanceID string) bool {
	switch instanceID {
	case "agy", "grok", "grok-chat", "opencode":
		return true
	}
	return false
}

// headlessPermissionAgent reports agents whose non-hosted permission cards come from a headless ACP companion.
func headlessPermissionAgent(agent string) bool {
	return agent == "agy" || agent == "grok"
}

// toolTitle is one of the manager's own tools by its name for people ("iPhone control"), "" for any other.
func toolTitle(l i18n.Lang, tool string) string {
	key := "assistant-tool_titles." + strings.ReplaceAll(tool, ".", "_")
	if tool == "" || !i18n.Has(key) {
		return ""
	}
	return i18n.T(l, key)
}

// personaToolTitle is one of Persona's own MCP tools, called by its full name (mcp__persona__…, the compatibility
// namespace too), by its name for people in l for the title of a node's card (#4966): the catalog's name
// (assistant-tool_titles), a report to the supervisor with what it reports ("Report to supervisor · Done"), the
// phone's and mail tools by their family and the rest of the name as the chat's tool rows say them ("iPhone · contacts
// search"), any other "Persona · agent read". "" for every other tool: the apps name those themselves. Without input,
// the tool alone ("Report to supervisor"; Interaction.ToolNameTitle).
func personaToolTitle(l i18n.Lang, name string, input json.RawMessage) string {
	name = compat.CanonicalMCPToolName(name)
	rest, ok := strings.CutPrefix(name, "mcp__")
	i := strings.LastIndex(rest, "__")
	if !ok || i <= 0 || !compat.IsMCPServer(strings.TrimPrefix(rest[:i], "plugin_")) || rest[i+2:] == "" {
		return ""
	}
	tool := rest[i+2:]
	if tool == "report_to_supervisor" {
		var a struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(input, &a)
		key := "assistant-tool_titles.report_to_supervisor_" + a.Kind
		if a.Kind != "" && !strings.ContainsAny(a.Kind, ". ") && i18n.Has(key) {
			return i18n.T(l, "assistant-tool_titles.report_to_supervisor") + " · " + i18n.T(l, key)
		}
	}
	if t := toolTitle(l, tool); t != "" {
		return t
	}
	words := func(s string) string { return strings.ReplaceAll(s, "_", " ") }
	for _, family := range []string{"phone", "mail"} {
		if what, ok := strings.CutPrefix(tool, family+"_"); ok && what != "" {
			return i18n.T(l, "assistant-tool_titles.family_"+family) + " · " + words(what)
		}
	}
	return "Persona · " + words(tool)
}

func alwaysCard(ix proto.Interaction) bool {
	return ix.Kind == proto.IxPermission && ix.SessionID != "" && len(ix.AlwaysKeys) > 0 &&
		len(ix.AlwaysKeys) <= maxAlwaysKeys && len(ix.Fields) == 0
}

type interactionOriginKey struct{}

func withInteractionOrigin(ctx context.Context, origin string) context.Context {
	return context.WithValue(ctx, interactionOriginKey{}, origin)
}

func interactionOrigin(ctx context.Context) string {
	origin, _ := ctx.Value(interactionOriginKey{}).(string)
	return origin
}

// isInboxOrigin checks the node's attested interaction.create origin hint first (covering the
// first-turn window before Start records the origin and transient store errors), then the
// manager's origin book. When failClosed is true, an unreadable origin book is treated as Inbox.
func (s *Service) isInboxOrigin(ctx context.Context, machine, session string, failClosed bool) bool {
	if interactionOrigin(ctx) == OriginInbox {
		return true
	}
	b := &s.origins
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.originsLocked(ctx) {
		return failClosed
	}
	o := b.m[session]
	if o == nil {
		if parent, _, ok := strings.Cut(session, "@"); ok && parent != "" {
			o = b.m[parent]
		}
	}
	return o != nil && o.Origin == OriginInbox && (o.MachineID == "" || s.sameMachine(machine, o.MachineID))
}

// isWholeBashAlwaysKey reports whether key grants whole-tool Bash ("tool:Bash" or "runtime:<rt>:tool:Bash").
func isWholeBashAlwaysKey(key string) bool {
	if strings.EqualFold(key, "tool:Bash") {
		return true
	}
	if rest, ok := strings.CutPrefix(key, "runtime:"); ok {
		if _, tool, ok := strings.Cut(rest, ":tool:"); ok && strings.EqualFold(tool, "Bash") {
			return true
		}
	}
	return false
}

// isInboxWholeBashAlways reports whether ix asks for a whole-tool Bash session grant on an
// Inbox handling conversation. Inbox turns process untrusted external messages, so a single Always
// click on a benign read command must never auto-approve arbitrary later shell mutations (#15267).
func (s *Service) isInboxWholeBashAlways(ctx context.Context, ix proto.Interaction) bool {
	return slices.ContainsFunc(ix.AlwaysKeys, isWholeBashAlwaysKey) &&
		s.isInboxOrigin(ctx, ix.MachineID, ix.SessionID, true)
}

// alwaysAllowed: every key ix names was granted with "Always allow in this session" in its conversation.
func (s *Service) alwaysAllowed(ctx context.Context, ix proto.Interaction) (proto.Resolution, bool) {
	st := s.fleetStore()
	if st == nil || !alwaysCard(ix) || s.isInboxWholeBashAlways(ctx, ix) {
		return proto.Resolution{}, false
	}
	ok, err := st.AlwaysAllowed(ctx, ix.SessionID, ix.AlwaysKeys)
	if err != nil {
		s.logf("[%s] 本会话总是允许: %v", ix.MachineID, err)
		return proto.Resolution{}, false
	}
	if !ok {
		return proto.Resolution{}, false
	}
	s.logf("[%s] %s allowed: 本会话总是允许 in this conversation", ix.MachineID, ix.ToolName)
	yes := true
	return proto.Resolution{Allow: &yes, Always: true, Remembered: true}, true
}

// keepAlways keeps what a card answered with "Always allow in this session" grants, then answers the conversation's other cards
// waiting meanwhile that it covers now (parallel tool calls ask at once). It runs before the hook hears the answer,
// so each database call is bounded by historyTimeout: a grant that cannot be kept is only logged.
func (s *Service) keepAlways(ix proto.Interaction, r proto.Resolution) {
	st := s.fleetStore()
	if st == nil || !alwaysCard(ix) || r.Allow == nil || !*r.Allow || !r.Always {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyTimeout)
	if s.isInboxWholeBashAlways(ctx, ix) || s.ixPolicy(ctx, ix).ConfirmOutbound {
		cancel()
		return
	}
	err := st.AllowAlways(ctx, ix.SessionID, ix.ToolName, ix.AlwaysKeys)
	cancel()
	if err != nil {
		s.logf("[%s] keep 本会话总是允许: %v", ix.MachineID, err)
		return
	}
	s.fleet.mu.Lock()
	var waiting []proto.Interaction
	for _, p := range s.fleet.interactions {
		if p.ix.ID != ix.ID && p.ix.SessionID == ix.SessionID && p.ix.MachineID == ix.MachineID &&
			p.local == nil && !p.codex && !p.claimed && alwaysCard(p.ix) && (p.ix.AlwaysHonored == nil || *p.ix.AlwaysHonored) {
			waiting = append(waiting, p.ix)
		}
	}
	s.fleet.mu.Unlock()
	for _, w := range waiting {
		ctx, cancel := context.WithTimeout(context.Background(), historyTimeout)
		if s.ixPolicy(ctx, w).ConfirmOutbound {
			cancel()
			continue
		}
		res, ok := s.alwaysAllowed(ctx, w)
		cancel()
		if ok {
			_ = s.resolveRememberedOccurrence(w, res)
		}
	}
}

// ---- a node gone, the manager restarted ----

// NodeDetached: a machine's node connection ended (devices.Options.Detached): the cards waiting on it that cannot be
// claimed again end (resumable ones wait for their hook through the next connection), its list shows it.
func (s *Service) NodeDetached(machine string) {
	s.invalidateLauncherListCache(machine)
	s.noteNodeOffline(machine, time.Now())
	if !s.fleetOn() {
		return
	}
	s.cancelMachineInteractions(machine)
	if s.devs != nil && !s.devs.Has(machine) {
		s.pruneRemovedFoldMachines()
	}
	s.sessionsChanged(machine)
	s.directionTreeSessions(DeviceSessions{MachineID: machine}) // a direction's session there now has its computer offline
}

func (s *Service) cancelMachineInteractions(machineID string) {
	s.fleet.mu.Lock()
	var ids []string
	now := time.Now()
	for id, p := range s.fleet.interactions {
		// Resumable ones wait for their hook to come back through the next node (await).
		if p.ix.MachineID == machineID && !p.ix.Resumable {
			ids = append(ids, id)
			if p.ix.Kind == proto.IxScreen { // its dialog may still be there when the node is back (screenBackLocked)
				if s.fleet.screenDetached == nil {
					s.fleet.screenDetached = map[string]screenGone{}
				}
				s.fleet.screenDetached[id] = screenGone{machine: machineID, session: p.ix.SessionID, at: now, announced: p.announced,
					told: p.told}
			}
		}
	}
	s.fleet.mu.Unlock()
	s.larkModule().AskDetached(ids, resumeGrace)
	for _, id := range ids {
		if s.endPreparedByID(id, "cancelled", "The computer that owned this approval disconnected; nothing changed.") {
			continue
		}
		s.finishInteraction(id)
	}
}

// cancelSessionInteractions withdraws leftover approval gates and pending cards
// for one superseded session on a returning node (node_recovery.go, #14781).
func (s *Service) cancelSessionInteractions(machineID, sessionID string) {
	s.fleet.mu.Lock()
	var ids []string
	for id, p := range s.fleet.interactions {
		if p.ix.MachineID == machineID && p.ix.SessionID == sessionID {
			ids = append(ids, id)
		}
	}
	s.fleet.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	s.larkModule().AskDetached(ids, resumeGrace)
	for _, id := range ids {
		if s.endPreparedByID(id, "cancelled", "The superseded Agent was released during node recovery; nothing changed.") {
			continue
		}
		s.finishInteraction(id)
	}
}

// restoreInteractions takes back, as the manager starts, the resumable cards the last one left pending: their hooks
// reconnect through the next node and claim them again (waitInteraction), without a second push; the others it left
// are over (cancelled in the history), and the hub is told so: a hub that took over from the one they were shown by
// does not know them, and an app that resumed its event stream across the switch would keep them up, next to the
// question asked again (#4568). A card that was on its way up to the user finds its route again (restoreRoute); one
// the assistant was deciding on goes on (resumeDelegations). Both before the hub gets the full list (FleetUp).
func (s *Service) restoreInteractions(ctx context.Context) {
	st, ok := s.store().(staleInteractionStore)
	if ok {
		s.fleet.restored.Do(func() { s.restoreStoredInteractions(context.WithoutCancel(ctx), st) })
	}
}

type staleInteractionStore interface {
	ExpireStaleInteractions(context.Context) ([]proto.Interaction, []proto.Interaction, error)
}

func (s *Service) restoreStoredInteractions(ctx context.Context, st staleInteractionStore) {
	store := s.store()
	resumable, cancelled, err := func() (out []proto.Interaction, cancelled []proto.Interaction, err error) {
		defer guard(&err) // a test's store may not keep the table
		return st.ExpireStaleInteractions(ctx)
	}()
	if err != nil {
		s.logf("expire stale interactions: %v", err)
		s.retryRestoreInteractionRead(store)
		return
	}
	// The database has now retained exactly the explicitly persistent proposal
	// cards. Cancel only their nonpersistent durable counterparts left by the
	// crashed caller; persistent proposals and recorded grants stay untouched.
	if book, supported := newOwnerActionBook(s.store()); supported {
		if err := book.cancelNonpersistentPending(ctx); err != nil {
			s.logf("reconcile owner-action proposals after restart: %v", err)
		}
	}
	for _, ix := range cancelled {
		s.tellResolved(assistantwire.FleetResolved{ID: ix.ID, CreatedAt: ix.CreatedAt})
	}
	if recStore, ok := s.store().(interactionRecorder); ok && len(cancelled) > 0 {
		for _, cancelledIX := range cancelled {
			readCtx, cancel := context.WithTimeout(ctx, historyTimeout)
			rec, err := func() (rec *db.InteractionRecord, err error) {
				defer guard(&err)
				return recStore.InteractionRecord(readCtx, cancelledIX.ID)
			}()
			cancel()
			if err != nil {
				s.logf("read invalidated interaction %s: %v", cancelledIX.ID, err)
				continue
			}
			if rec != nil {
				s.recordRestartInvalidation(ctx, rec.Interaction)
			}
		}
	}
	s.restoreInteractionSnapshots(ctx, store, resumable, false)
}

func (s *Service) restoreInteractionPolicy(ctx context.Context, ix *proto.Interaction) {
	if s.isInboxWholeBashAlways(ctx, *ix) {
		ix.AlwaysKeys = nil
		notKept := false
		ix.AlwaysHonored = &notKept
	}
	if ix.Kind == proto.IxPermission {
		inAssistant := false
		if ix.Cwd != "" {
			inAssistant = s.ixInAssistant(ctx, *ix)
		} else {
			lookupCtx, cancel := context.WithTimeout(ctx, historyTimeout)
			inAssistant = s.ixInAssistant(lookupCtx, *ix)
			cancel()
		}
		if ix.OwnerProposal || ix.Detachable || s.permissionNoHandBack(*ix, inAssistant) {
			ix.NoHandBack = true
		}
	}
}

// resumableInteractionReader is the non-mutating half of startup expiration.
// A retry must never cancel cards created by the current process.
type resumableInteractionReader interface {
	ResumableInteractions(ctx context.Context) ([]proto.Interaction, error)
}

func (s *Service) retryRestoreInteractionRead(store Store) {
	reader, ok := store.(resumableInteractionReader)
	if !ok {
		s.logf("retry restored interactions without a read-only resumable query is unavailable")
		return
	}
	time.AfterFunc(time.Minute, func() {
		if s.runCtx().Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(s.runCtx(), historyTimeout)
		snapshot, err := reader.ResumableInteractions(ctx)
		cancel()
		if err != nil {
			s.logf("read resumable interactions for restore retry: %v", err)
			s.retryRestoreInteractionRead(store)
			return
		}
		s.restoreInteractionSnapshots(s.runCtx(), store, snapshot, true)
	})
}

// restoreInteractionSnapshots installs only rows that are still the same pending
// occurrence. A transient per-row read schedules another snapshot pass, never a
// second ExpireStaleInteractions call: expiring again could cancel cards created
// after startup. The fleet mutex keeps each revalidation serialized with journal
// recovery, including on retries.
func (s *Service) restoreInteractionSnapshots(ctx context.Context, store Store, snapshot []proto.Interaction, publish bool) {
	recorder, canValidate := store.(interactionRecorder)
	if !canValidate {
		s.fleet.mu.Lock()
		for _, ix := range snapshot {
			if s.ixMapLocked()[ix.ID] == nil {
				s.fleet.mu.Unlock()
				s.logf("restore interaction %s without occurrence validation is unavailable", ix.ID)
				s.fleet.mu.Lock()
			}
		}
		s.fleet.mu.Unlock()
		return
	}
	var back, retry []proto.Interaction
	for _, ix := range snapshot {
		s.restoreInteractionPolicy(ctx, &ix)
		var entry approvalJournalEntry
		if ix.Detachable {
			readCtx, cancel := context.WithTimeout(ctx, historyTimeout)
			var err error
			entry, _, err = s.approvalJournalRecord(readCtx, ix)
			cancel()
			if err != nil {
				retry = append(retry, ix)
				s.logf("read restored approval journal %s: %v", ix.ID, err)
				continue
			}
		}
		releaseAdmission := s.lockInteractionAdmission(ix.ID)
		s.fleet.mu.Lock()
		m := s.ixMapLocked()
		if m[ix.ID] != nil {
			s.fleet.mu.Unlock()
			releaseAdmission()
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, historyTimeout)
		rec, err := recorder.InteractionRecord(readCtx, ix.ID)
		cancel()
		if err != nil {
			retry = append(retry, ix)
			s.logf("revalidate restored interaction %s: %v", ix.ID, err)
			s.fleet.mu.Unlock()
			releaseAdmission()
			continue
		}
		if rec != nil && rec.Status == "pending" && sameDurableApprovalOccurrence(ix, rec.Interaction) {
			p := &pending{ix: ix, done: make(chan proto.Resolution, 1)}
			if !s.prepareRestoredDetached(p, rec.Interaction, entry) {
				s.fleet.mu.Unlock()
				releaseAdmission()
				continue
			}
			m[ix.ID] = p
			if p.prepared == nil {
				s.orphanLocked(p)
			}
			back = append(back, p.ix)
		}
		s.fleet.mu.Unlock()
		releaseAdmission()
	}
	var unrouted []proto.Interaction
	for _, ix := range back {
		if s.restoreRoute(ctx, ix) {
			continue
		}
		if publish {
			// A delayed retry can run after the hub's initial full snapshot. Match
			// an ordinary reconnect: an unrouted restored card must become visible.
			if !s.resumeDelegations(ctx, ix) {
				s.show(ix)
			}
		} else {
			unrouted = append(unrouted, ix)
		}
	}
	if !publish {
		s.resumeDelegations(ctx, unrouted...)
	}
	if len(retry) > 0 {
		time.AfterFunc(time.Minute, func() {
			if s.runCtx().Err() != nil {
				return
			}
			s.restoreInteractionSnapshots(s.runCtx(), store, retry, true)
		})
	}
}

// restoreRoute puts card ix, restored after a restart, back on its way to the supervisor of the task it belongs to:
// the route lived on the pending card only, and the apps got the card back without it. The ladder is derived now, as
// routeAsk does for a new card, so a card whose task is done or already waits for the user stays the user's. The
// supervisor hears of it as any card held with it (signalAsk). Whether the user was told of the card before the
// restart is not kept: a question that may have rung then only goes back on the apps when its time is up, while an
// approval rings (ringAsks). True when it was routed.
func (s *Service) restoreRoute(ctx context.Context, ix proto.Interaction) bool {
	if s.activeInternalCard(ix) {
		return s.handNonOwnerCard(ix)
	}
	if !routable(ix) {
		return false
	}
	s.supervise.routingMu.Lock()
	defer s.supervise.routingMu.Unlock()
	ladder, todo, home, ok := s.askLadder(ctx, ix)
	if !ok {
		return s.handNonOwnerCard(ix)
	}
	s.fleet.mu.Lock()
	p := s.fleet.interactions[ix.ID]
	if p == nil || p.claimed || p.told || !p.ix.CreatedAt.Equal(ix.CreatedAt) {
		s.fleet.mu.Unlock()
		return false
	}
	now := time.Now()
	// A question may have rung the user before the restart: its timeout only puts it back on the apps. An approval is
	// not quiet: only the user can answer it, so its timeout rings (ringAsks) instead of leaving the agent blocked
	// with nobody told.
	p.route = &askRoute{todo: todo, machine: home, ladder: ladder, routedAt: now, since: now, held: now, maybeTold: ix.Kind == proto.IxQuestion}
	p.ix.Delegating = true
	shown := p.ix
	s.fleet.mu.Unlock()
	if ix.Kind == proto.IxScreen {
		// Detaching ended the earlier instance of this dialog. The first
		// explicit escalation after reconnect must still be able to post it.
		s.chans.mu.Lock()
		if endedAt, ok := s.chans.endedOccurrence[ix.ID]; ok {
			if !endedAt.Equal(ix.CreatedAt) {
				delete(s.chans.ended, ix.ID)
				delete(s.chans.endedOccurrence, ix.ID)
			}
		} else if ended, ok := s.chans.ended[ix.ID]; ok && ended.Before(ix.CreatedAt) {
			delete(s.chans.ended, ix.ID)
		}
		s.chans.mu.Unlock()
	}
	s.show(shown)
	s.signalAsk(todo, ladder[0], ix, 0)
	return true
}
