package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the nodes' events and requests ----

// NodeEvent takes a node's event before the hub does (devices.Options.Event); false: the hub does not get it (the
// manager tells it what follows instead).
func (s *Service) nodeEvent(machine, event string, params json.RawMessage) bool {
	if event == "launchers.changed" {
		s.launchersChanged(machine)
		return false
	}
	if err := s.conversationParamsAvailable(context.Background(), machine, params); err != nil {
		return false
	}
	var hook map[string]any
	if event == "hook.event" && json.Unmarshal(params, &hook) == nil {
		if sid, _ := hook["session_id"].(string); sid != "" {
			s.agentDecisionPrompt(machine, sid, hook)
		}
	}
	if !s.fleetOn() {
		return true
	}
	switch event {
	case "sessions.changed":
		// The devices took this very list just before passing the event on (Machine.onEvent, and BusUp's replay of
		// it): a copy of theirs, not a second decode of the whole list on every change. Its nested slices are shared
		// with theirs, as decorated's are: never written into.
		if s.devs != nil {
			if list, ok := s.devs.Sessions(machine); ok {
				s.setSessions(machine, list)
				return false
			}
		}
		var p struct {
			Sessions []proto.Session `json:"sessions"`
		}
		if json.Unmarshal(params, &p) == nil {
			s.setSessions(machine, p.Sessions)
		}
		return false // the hub gets the list as the apps do (fleet.sessions)
	case "sessions.revs":
		s.syncPlanInteractions(machine)
	case "session.items": // the channels' (fleet_follow.go); the apps get them from the hub
		s.followedItems(machine, params)
	case "session.delta":
		s.followedDelta(machine, params)
	case "interaction.dismiss":
		var p nodeInteractionEnd
		if json.Unmarshal(params, &p) == nil {
			s.endNodeInteraction(p, "dismissed", "The approval was dismissed on the computer; this card did not apply the change.", &proto.Resolution{Dismiss: true, AnsweredBy: proto.AnsweredByTerminal})
		}
		return false
	case "interaction.cancel":
		var p nodeInteractionEnd
		if json.Unmarshal(params, &p) == nil {
			s.endNodeInteraction(p, "cancelled", "The approval was cancelled on the computer; nothing changed.", nil)
		}
		return false
	case "codex.interactions": // the Codex questions and approvals open on this machine (fleet_ix_codex.go)
		s.syncCodexInteractions(machine, params)
		return false
	case "hook.event":
		if p := hook; p != nil {
			if sid, _ := p["session_id"].(string); sid != "" {
				// A report-only hook (node/globalhooks.go) is not Persona's hooks running in the session: it asks for
				// no card, so it must not hold the screen's back (hasHooks). Its Stop still counts for stopDuplicate.
				if p["report_only"] != true {
					s.fleet.mu.Lock()
					if s.fleet.hooked == nil {
						s.fleet.hooked = map[[2]string]bool{}
					}
					s.fleet.hooked[[2]string{machine, sid}] = true
					s.fleet.mu.Unlock()
				}
				s.resetIdleObservation(machine, sid)
				s.noteStop(machine, sid, p)
				s.meetingTurnPrompt(machine, sid, p) // the user's own prompt to an Agent ends its meeting turn (meeting_turns.go)
			}
		}
		// the hub shows it to the apps (hook_event)
	}
	return true
}

type nodeInteractionEnd struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	target    *pending  // exact live occurrence pinned before admission, including legacy zero timestamps
}

// endNodeInteraction applies a node's cancellation only to the occurrence that
// node was waiting on. Occurrence-less events retain legacy ID-only behavior;
// newer peers never let a delayed cancel/dismiss end a same-ID replacement.
func (s *Service) endNodeInteraction(p nodeInteractionEnd, status, preparedText string, resolution *proto.Resolution) {
	runCtx := s.runCtx()
	retry := func(event nodeInteractionEnd) {
		// A database-only legacy row can have no creation time. Its ID
		// alone cannot identify the same card on a later attempt.
		if event.CreatedAt.IsZero() && event.target == nil {
			return
		}
		time.AfterFunc(time.Minute, func() {
			if runCtx.Err() == nil {
				s.endNodeInteraction(event, status, preparedText, resolution)
			}
		})
	}
	// Serialize the absent-card check and its durable end with installation
	// of this ID. Once installed, only the live coordinator may decide it.
	release := s.acquireInteractionAdmission(p.ID, p.target != nil || !p.CreatedAt.IsZero())
	if release == nil {
		// A legacy event has no occurrence yet. Pin the card already visible
		// in history before waiting behind a same-ID registration.
		s.fleet.mu.Lock()
		live := s.fleet.interactions[p.ID]
		if live != nil {
			p.target = live
			p.CreatedAt = live.ix.CreatedAt
		}
		s.fleet.mu.Unlock()
		if live == nil {
			if recorder, ok := s.store().(interactionRecorder); ok {
				ctx, cancel := context.WithTimeout(runCtx, historyTimeout)
				rec, err := recorder.InteractionRecord(ctx, p.ID)
				cancel()
				if err == nil && rec != nil && rec.Status == "pending" {
					p.CreatedAt = rec.Interaction.CreatedAt
				}
			}
		}
		if p.target != nil || !p.CreatedAt.IsZero() {
			go func() {
				if runCtx.Err() == nil {
					s.endNodeInteraction(p, status, preparedText, resolution)
				}
			}()
		}
		return
	}
	defer release()
	s.fleet.mu.Lock()
	live := s.fleet.interactions[p.ID]
	if p.target != nil && live != p.target {
		s.fleet.mu.Unlock()
		return
	}
	if live != nil {
		ix := live.ix
		prepared := live.prepared != nil
		released := prepared && ix.Detachable && live.prepared.waiting
		s.fleet.mu.Unlock()
		if released {
			return // never wait for a clarification send on the node's event reader
		}
		if !p.CreatedAt.IsZero() && !p.CreatedAt.Equal(ix.CreatedAt) {
			return
		}
		if prepared {
			by := ""
			if resolution != nil {
				by = resolution.AnsweredBy
			}
			// Once the hook has released its turn, transport cancellation and
			// terminal dismissal cannot take the decision away from the owner.
			s.endPreparedIfBy(live, status, preparedText, func(a *preparedApproval) bool {
				return !ix.Detachable || !a.waiting
			}, by)
			return
		}
		if p.CreatedAt.IsZero() {
			s.recordEnd(p.ID, status, resolution)
			s.finishInteraction(p.ID)
			return
		} else if err := s.recordEndOccurrenceStrict(ix, status, resolution); err != nil {
			s.logf("record node interaction occurrence outcome: %v", err)
		}
		s.finishPending(live)
		return
	}
	s.fleet.mu.Unlock()

	// Legacy nodes send only an ID. Preserve their direct terminal write for
	// unjournaled cards; no occurrence read or delayed ID-only write is needed.
	if p.CreatedAt.IsZero() {
		readCtx, cancel := context.WithTimeout(runCtx, historyTimeout)
		_, protected, err := s.findApprovalJournal(readCtx, func(a approvalJournalEntry) bool {
			// Legacy commands lack both occurrence fields and may lack a kind.
			legacyCommand := a.CreatedAt.IsZero() && (a.Kind == "" || a.Kind == proto.IxPermission)
			if a.ID != p.ID || a.Finalized || (a.Fingerprint == "" && !legacyCommand) {
				return false
			}
			return a.Status == "applying" || a.Result != "" || !a.WaitingUntil.IsZero()
		})
		cancel()
		if err == nil && !protected {
			s.recordEnd(p.ID, status, resolution)
			s.finishInteraction(p.ID)
			return
		}
	}
	// The waiter may already have returned while its durable row remains. Read
	// the full occurrence so strict finalization never inserts or ends a guessed
	// ID-only row, and never touch a different current occurrence.
	recorder, ok := s.store().(interactionRecorder)
	if ok {
		ctx, cancel := context.WithTimeout(context.Background(), historyTimeout)
		defer cancel()
		rec, err := recorder.InteractionRecord(ctx, p.ID)
		if err != nil {
			s.logf("read node interaction occurrence: %v", err)
			// Only an identified occurrence can be retried. An ID-only end
			// must never be delivered later to a replacement on another clock.
			if !p.CreatedAt.IsZero() {
				retry(p)
			}
			return
		}
		if rec != nil && !p.CreatedAt.IsZero() && !p.CreatedAt.Equal(rec.Interaction.CreatedAt) {
			return
		}
		if rec != nil && rec.Status == "pending" {
			released, err := s.detachedNodeEndAfterRelease(ctx, rec.Interaction)
			if err != nil {
				// The terminal event is one-shot. Recheck this exact stored
				// occurrence after the journal recovers, including legacy events.
				s.logf("defer node outcome for %s: %v", p.ID, err)
				retry(nodeInteractionEnd{ID: p.ID, CreatedAt: rec.Interaction.CreatedAt})
				return
			}
			if released {
				return
			}
			if !p.CreatedAt.IsZero() {
				if err := s.recordEndOccurrenceStrict(rec.Interaction, status, resolution); err != nil {
					s.logf("record node interaction occurrence outcome: %v", err)
				}
				return
			}
		}
	}
	if p.CreatedAt.IsZero() {
		s.recordEnd(p.ID, status, resolution)
		s.finishInteraction(p.ID)
	}
}

// NodeRequest answers what of a node's requests is the manager's (devices.Options.Answer, with NodeAnswer's).
func (s *Service) nodeRequest(ctx context.Context, machine, method string, params json.RawMessage) (any, bool, error) {
	// These ids name a direction and its optional coding Agent, not a Persona conversation on the calling node; do not
	// run conversation-tombstone validation over them.
	switch method {
	case "direction.create":
		if !s.fleetOn() {
			return nil, false, nil
		}
		var in directionCreateInput
		if err := json.Unmarshal(params, &in); err != nil {
			return directionNodeReply(nil, directionInvalidf("arguments: %v", err)), true, nil
		}
		view, err := s.createOutsideDirection(ctx, in)
		return directionNodeReply(view, err), true, nil
	case "direction.progress":
		if !s.fleetOn() {
			return nil, false, nil
		}
		var in struct {
			ID string `json:"id"`
			directionProgressInput
		}
		if err := json.Unmarshal(params, &in); err != nil {
			return directionNodeReply(nil, directionInvalidf("arguments: %v", err)), true, nil
		}
		view, err := s.writeDirectionProgress(ctx, strings.TrimSpace(in.ID), in.directionProgressInput)
		return directionNodeReply(view, err), true, nil
	}
	if err := s.conversationParamsAvailable(ctx, machine, params); err != nil {
		return nil, true, err
	}
	if !s.fleetOn() {
		return nil, false, nil
	}
	switch method {
	case "computer.elicit": // LCU asked the person something; the card is the same one as app approval
		if s.ext == nil {
			return map[string]any{"action": "cancel"}, true, nil
		}
		var p struct {
			CallID json.RawMessage `json:"call_id"`
		}
		_ = json.Unmarshal(params, &p)
		var callID string
		_ = json.Unmarshal(p.CallID, &callID)
		ctx = s.mcpWaitElicitContext(ctx, callID)
		out, err := s.ext.AnswerComputerElicit(ctx, machine, params)
		return out, true, err
	case "interaction.create":
		var p struct {
			Interaction proto.Interaction `json:"interaction"`
			Origin      string            `json:"origin"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, true, err
		}
		p.Interaction.MachineID = machine
		s.resetIdleObservation(machine, p.Interaction.SessionID)
		s.placeCard(&p.Interaction)
		// A write through a skill's gated command (node/larkgate.go): its plugin names what the ids are.
		if sum, ok := s.assistantSummary(ctx, p.Interaction.ToolName, p.Interaction.Summary); ok {
			p.Interaction.Summary = sum
		}
		if p.Origin != "" {
			ctx = withInteractionOrigin(ctx, p.Origin)
		}
		r, err := s.nodeInteraction(ctx, p.Interaction)
		return r, true, err
	case assistantwire.MethodCloseoutCheck: // a turn that did work ends without a closeout (agents_closeout.go)
		var p assistantwire.CloseoutCheckRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, true, err
		}
		return s.closeoutCheck(ctx, machine, p), true, nil
	case devicewire.MethodAuthorizeTaskTerminal:
		var p devicewire.TaskTerminalCloseRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, true, err
		}
		return map[string]bool{"authorized": s.authorizeTaskTerminal(ctx, machine, p)}, true, nil
	case assistantwire.MethodCloseoutBrief: // a session starts: the closeout's form, up front (agents_closeout_brief.go)
		var p assistantwire.CloseoutBriefRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, true, err
		}
		return s.closeoutStart(ctx, machine, p), true, nil
	case "assistant.lark":
		out, err := s.assistantLark(ctx, machine)
		return out, true, err
	case proto.MethodLarkSent: // a write the gate proxy forwarded and the API took (node/gateproxy.go)
		if s.ext == nil {
			return nil, true, errors.New("lark.sent: no extensions, there is no ledger")
		}
		out, err := s.ext.RecordNodeSent(ctx, machine, params)
		return out, true, err
	case proto.MethodGateRelay: // a relayed command gate's call, for the extension's socket here (#12904)
		r, ok := s.ext.(interface {
			RelayGate(ctx context.Context, machine string, params json.RawMessage) (any, error)
		})
		if !ok {
			return nil, true, errors.New("gate.relay: no extensions here to relay to")
		}
		out, err := r.RelayGate(ctx, machine, params)
		return out, true, err
	case proto.MethodPluginNodeHubCall: // a program on that computer calls the hub (extmgr/extension_node_calls.go)
		r, ok := s.ext.(interface {
			NodeProgramHubCall(ctx context.Context, machine string, params json.RawMessage) (any, error)
		})
		if !ok {
			return nil, true, errors.New("plugin.node.hub_call: no extensions here")
		}
		out, err := r.NodeProgramHubCall(ctx, machine, params)
		return out, true, err
	case proto.MethodMailAttachment: // mail_save_attachment's bytes for the node that saves them (fleet_tools.go)
		var p proto.MailAttachmentRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, true, err
		}
		out, err := s.mailAttachment(ctx, machine, p)
		return out, true, err
	case "mcp.tools":
		ctx = withToolSet(ctx, params) // a narrower set the process asks for (supervise_round.go)
		var p struct {
			Assistant  bool   `json:"assistant"`
			SessionID  string `json:"session_id"`
			CallerCwd  string `json:"caller_cwd"`
			Guest      bool   `json:"guest"` // the node's word that it is a guest conversation (guest.go)
			Revision   string `json:"revision"`
			LeaderOnly bool   `json:"leader_only"`
		}
		_ = json.Unmarshal(params, &p)
		ctx = withNodeCallerCwd(ctx, machine, p.SessionID, p.CallerCwd)
		if p.LeaderOnly {
			return map[string]any{"tools": s.runtimeLeaderTools(ctx, machine, p.SessionID, p.Guest)}, true, nil
		}
		out := s.sessionMCPToolsSince(ctx, machine, p.SessionID, p.Assistant, p.Guest, p.Revision)
		// Only an assistant conversation defers tools: a guest, by the node's word or by the manager's own record
		// (guest.go), has no ToolSearch to load one with (node/assistant_guest.go). An unchanged or failed answer
		// carries no list to mark.
		if tools, ok := out["tools"].([]proto.Tool); ok {
			deferring := p.Assistant && !p.Guest && !s.isGuest(ctx, machine, p.SessionID)
			out["tools"] = s.withToolLoading(ctx, p.SessionID, tools, deferring)
		}
		return out, true, nil
	case "mcp.call":
		var p struct {
			Name             string                 `json:"name"`
			Args             json.RawMessage        `json:"args"`
			SessionID        string                 `json:"session_id"`
			CallerCwd        string                 `json:"caller_cwd"`
			Origin           string                 `json:"origin"` // the node's attested origin, including during assistant startup
			Assistant        bool                   `json:"assistant"`
			Guest            bool                   `json:"guest"`
			WaitUpdates      bool                   `json:"wait_updates"`
			CallID           json.RawMessage        `json:"call_id"`
			SourcePermission proto.SourcePermission `json:"source_permission"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, true, err
		}
		ctx = withNodeCallerCwd(ctx, machine, p.SessionID, p.CallerCwd)
		ctx = devicewire.WithSourcePermission(ctx, proto.SourcePermission{})
		if p.SourcePermission.Matches(p.SessionID, p.Name, p.Args) {
			ctx = devicewire.WithSourcePermission(ctx, p.SourcePermission)
		}
		var callID string
		_ = json.Unmarshal(p.CallID, &callID)
		ctx = s.mcpWaitContext(ctx, machine, callID, p.WaitUpdates)
		defer s.endMCPWait(ctx)
		if res, refused := s.guestRefuses(ctx, machine, p.SessionID, p.Name, p.Guest); refused {
			return res, true, nil
		}
		// A proxy reply's one tool is answered here, before a meeting room's (bot_handoff.go).
		if id, handoff, err := s.handoffOf(ctx, machine, p.SessionID); err == nil && handoff {
			if s.isWorkMachine(machine) {
				return proto.TextResult("unknown tool "+p.Name, true), true, nil
			}
			return s.larkModule().HandoffToolCall(ctx, id, p.Name, p.Args), true, nil
		}
		// A group guest's reply tool is answered here; its other tools run with a sanitized step (bot_guest.go).
		if key, group, err := s.groupGuestOf(ctx, machine, p.SessionID); err == nil && group {
			if s.isWorkMachine(machine) {
				return proto.TextResult("unknown tool "+p.Name, true), true, nil
			}
			lark := s.larkModule()
			if res, own := lark.GuestToolCall(ctx, key, p.SessionID, p.Name, p.Args); own {
				return res, true, nil
			}
			done := lark.GuestToolStep(ctx, key, p.Name)
			res := s.mcpCallWithOrigin(ctx, machine, p.SessionID, p.Name, p.Args, false, p.Origin)
			done(res.IsError)
			return res, true, nil
		}
		if rec, room, err := s.roomOf(ctx, machine, p.SessionID); err == nil && room {
			if s.isWorkMachine(machine) {
				return proto.TextResult("unknown tool "+p.Name, true), true, nil
			}
			return s.roomToolCall(ctx, rec, p.Name, p.Args), true, nil // bound to its own recording
		}
		return s.mcpCallWithOrigin(ctx, machine, p.SessionID, p.Name, p.Args, p.Assistant, p.Origin), true, nil
	}
	return nil, false, nil
}

// FleetUp: the connection to the hub is (again) up: it gets every pending interaction, every session list and the
// fleet again (it may have restarted and know nothing).
func (s *Service) FleetUp(ctx context.Context) {
	if !s.fleetOn() {
		return
	}
	s.fleetStart(ctx)
	defer s.boardFleetUp() // the pending cards are read: the board may have changed while the assistant was away
	s.fleet.sentMu.Lock()
	s.fleet.sent, s.fleet.archived = nil, nil
	s.fleet.sentMu.Unlock()
	s.tellPendingInteractions(ctx)
	// Reconnect reads only session reviews. Other phone cards still belong to their
	// active callers, including an ask_user started while the connection came up.
	s.pubMu.Lock()
	reviewInteractions, complete := s.sessionReviewInteractions(ctx)
	s.reconcilePhoneAsks(reviewInteractions, func(id string) bool { return complete && s.sessionReviewPossible(id) })
	s.pubMu.Unlock()
	for _, m := range s.fleetMachines() {
		s.publishSessions(m.ID)
	}
	s.liveChanged(true)
}

// FleetEvent takes the hub's fleet.* events.
func (s *Service) FleetEvent(ctx context.Context, name string, params json.RawMessage) bool {
	switch name {
	case assistantwire.EventClients:
		var c assistantwire.Clients
		if json.Unmarshal(params, &c) == nil {
			s.fleet.mu.Lock()
			before := s.fleet.clients.Viewing
			wasOnline := s.fleet.clients.AppOnline
			s.fleet.clients = c
			s.fleet.mu.Unlock()
			for _, ref := range c.Viewing {
				s.cancelSessionRetirement(ref.MachineID, ref.SessionID)
			}
			for _, ref := range before {
				if !slices.Contains(c.Viewing, ref) {
					// Leaving a quiet page is the last owner activity, not its old transcript time.
					s.rememberSessionActivity(ctx, ref.MachineID, ref.SessionID)
				}
			}
			if c.AppOnline && !wasOnline {
				go s.ownerAppeared(context.WithoutCancel(ctx), "app_online", "")
			}
		}
	case assistantwire.EventMutes:
		var p assistantwire.FleetMutes
		if json.Unmarshal(params, &p) != nil || p.Mutes == nil {
			return true // not the mutes (a hub from before they came with the event): the ones handed over last stay
		}
		s.setMutes(*p.Mutes)
		for _, m := range s.fleetMachines() {
			s.publishSessions(m.ID)
		}
		s.liveChanged(false)
	case assistantwire.EventSeen:
		var p assistantwire.FleetSeen
		if json.Unmarshal(params, &p) == nil && p.MachineID != "" && p.SessionID != "" {
			if m, ok := s.fleetMachine(p.MachineID); ok {
				s.markSeen(ctx, m.ID, p.SessionID)
			}
		}
	case assistantwire.EventInteractionSeen:
		var p assistantwire.FleetInteractionSeen
		if json.Unmarshal(params, &p) == nil && p.InteractionID != "" {
			s.markInteractionSeen(ctx, p.InteractionID)
		}
	default:
		return false
	}
	return true
}
