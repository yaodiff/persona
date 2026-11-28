package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/wireids"
)

func logicalRefusal(ctx context.Context, code string) error {
	wire := CodeBadRequest
	if code == "busy" {
		wire = assistantwire.CodeConversationBusy
	}
	return &rpc.Error{Code: wire, Message: i18n.T(i18n.From(ctx), "assistant-conversation_segments."+code), Data: json.RawMessage(`{"delivered":"none"}`)}
}

type logicalTransportKey struct{}

// This is the single raw source boundary. Its callers have already resolved and
// fenced public identity; it never re-applies a physical session's model or role.
func (s *Service) logicalPhysical(ctx context.Context, machine, method string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	if startScopeMethods[method] || method == DeviceSessionSendOnce || method == "assistant.segment.prepare" || method == "session.modes" || method == "session.switch" {
		var execution struct {
			Launcher string `json:"launcher"`
		}
		if json.Unmarshal(raw, &execution) == nil && execution.Launcher != "" {
			if err := s.launcherScopeErr(ctx, machine, execution.Launcher); err != nil {
				return nil, err
			}
		}
	}
	ctx = context.WithValue(ctx, logicalSourceKey{}, true)
	if d := s.physicalDevices(); d != nil {
		if original, ok := ctx.Value(logicalTransportKey{}).(devicewire.NodeCall); ok {
			original.MachineID, original.Method, original.Params = machine, method, raw
			request, _ := json.Marshal(original)
			out, handled, err := d.Request(ctx, devicewire.MethodCall, request)
			if handled {
				if err != nil {
					return nil, err
				}
				return json.Marshal(out)
			}
		}
		return d.CallNode(ctx, machine, method, raw)
	}
	return s.hub.DeviceCall(ctx, DeviceCall{Device: machine, Method: method, Params: raw})
}

func logicalSendMethod(method string) bool {
	return method == DeviceAssistantSend || method == DeviceSessionSend || method == DeviceSessionSendOnce
}

func (s *Service) logicalCall(ctx context.Context, machine, method string, raw json.RawMessage) (json.RawMessage, bool, error) {
	if ctx.Value(logicalSourceKey{}) != nil || ctx.Value(historySourceKey{}) != nil {
		return nil, false, nil
	}
	if method == DeviceSessionGet {
		// One session's row is the manager's to answer from its projected list (logicalDevices.Session: the public
		// root shows its active segment, a private segment is not found), never a node's: a node knows no
		// session.get, and the fleet hides the active segment's physical id, so rewriting to it made every
		// switched conversation look gone and no further switch could start.
		return nil, false, nil
	}
	st, ok := s.store().(logicalConversationStore)
	if !ok {
		return nil, false, nil
	}
	var p map[string]any
	if json.Unmarshal(raw, &p) != nil {
		return nil, false, nil
	}
	id, _ := p["session_id"].(string)
	if id == "" {
		return nil, false, nil
	}
	c, err := s.physicalLogicalConversation(ctx, st, machine, id)
	if err != nil {
		return nil, true, err
	}
	if c == nil {
		return nil, false, nil
	}
	if c.Deleted {
		return nil, true, db.ErrConversationDeleted
	}
	if c.MachineID != machine || c.SessionID != id {
		return nil, true, logicalRefusal(ctx, "private_segment")
	}
	// The hub owns the public send-once ledger and clarification proofs. Keep
	// those checks on the logical root; its normalized session.send (or guarded
	// answer-again) comes back through this boundary before physical dispatch.
	if method == DeviceSessionSendOnce || method == assistantwire.DeviceAssistantAnswerAgain && p["once_id"] == nil {
		if _, external := ctx.Value(logicalTransportKey{}).(devicewire.NodeCall); !external {
			return nil, false, nil
		}
		return nil, true, logicalRefusal(ctx, "message_id_required")
	}
	navigation := method == proto.MethodSessionInputPreviews || method == proto.MethodSessionItemsWindow
	if method == DeviceSessionItems || method == "session.item" || method == "session.image" || method == DeviceAssistantHandoff || navigation {
		var out json.RawMessage
		for attempt := 0; attempt < 3; attempt++ {
			out, err = s.logicalHistoryCall(ctx, st, c, method, raw)
			if !errors.Is(err, db.ErrConversationHistoryChanged) {
				break
			}
			if navigation {
				return nil, true, &rpc.Error{Code: proto.CodeNavigationChanged, Message: i18n.T(i18n.From(ctx), "chat-navigation.history_changed")}
			}
			if attempt == 2 {
				break
			}
			c, err = st.LogicalConversation(ctx, machine, id)
			if err != nil {
				break
			}
			if c == nil || c.Deleted {
				return nil, true, db.ErrConversationDeleted
			}
		}
		return out, true, err
	}
	active, ok := c.Active()
	if !ok {
		return nil, true, logicalRefusal(ctx, "unavailable")
	}
	if logicalSendMethod(method) && c.Switch == nil && s.isVanishedZeroTurnSegment(ctx, c, active) {
		client, _ := p["client_msg_id"].(string)
		if client == "" {
			client, _ = p["once_id"].(string)
		}
		if _, external := ctx.Value(logicalTransportKey{}).(devicewire.NodeCall); !external || client != "" {
			recovered, err := s.beginZeroTurnRecoverySwitch(ctx, st, c, active)
			if err != nil {
				return nil, true, err
			}
			c = recovered
			active, ok = c.Active()
			if !ok {
				return nil, true, logicalRefusal(ctx, "unavailable")
			}
		}
	}
	if method == "session.watch" || method == "session.unwatch" {
		s.logical.mu.Lock()
		if s.logical.watches == nil {
			s.logical.watches = map[[2]string]json.RawMessage{}
		}
		if method == "session.watch" {
			s.logical.watches[[2]string{machine, id}] = append(json.RawMessage(nil), raw...)
		} else {
			delete(s.logical.watches, [2]string{machine, id})
		}
		s.logical.mu.Unlock()
	}
	var out json.RawMessage
	withEpoch := st.WithConversationEpoch
	var a2aMachine string
	if a2aDeliveryMethod(method) {
		a2aMachine = active.MachineID
		if !s.a2aMachineAllowed(ctx, s.a2aConfig(ctx), a2aMachine) {
			return nil, true, logicalRefusal(ctx, "unavailable")
		}
		// The epoch lock holds a database connection across the node call.
		// Leave room in the pool for callbacks and regular conversation traffic.
		select {
		case s.a2aLogicalWrites <- struct{}{}:
			defer func() { <-s.a2aLogicalWrites }()
		default:
			return nil, true, logicalRefusal(ctx, "busy")
		}
		withEpoch = st.WithDrainedConversationEpoch
	}
	// The queue may outlive these permissions and dispatch to another physical segment. Its
	// durable payload and receipt always use the ordinary text prepared by the manager.
	diagnostic, _ := ctx.Value(agyRelayContextKey{}).(agyRelayContext)
	withDiagnostic := diagnostic.matches(machine, method, p)
	if withDiagnostic {
		p["text"] = diagnostic.plain
	}
	dispatched := false
	err = withEpoch(ctx, machine, id, c.Epoch, func(current *db.LogicalConversation) error {
		active, ok = current.Active()
		if !ok {
			return logicalRefusal(ctx, "unavailable")
		}
		if a2aMachine != "" && active.MachineID != a2aMachine {
			return logicalRefusal(ctx, "busy")
		}
		if current.Switch != nil && (method == "session.modes" || method == "session.switch" || method == "session.resume" || method == DeviceAssistantWarm) {
			return logicalRefusal(ctx, "busy")
		}
		if method == assistantwire.DeviceAssistantAnswerAgain {
			if current.Switch != nil {
				return logicalRefusal(ctx, "busy")
			}
			if err := s.putSupervisionInstructions(ctx, machine, id, p); err != nil {
				return err
			}
		}
		if logicalSendMethod(method) {
			if err := s.putSupervisionInstructions(ctx, machine, id, p); err != nil {
				return err
			}
			// Queues are used throughout a switch, including its first target turn.
			pending := false
			queue, queued := s.store().(logicalSwitchStore)
			if queued {
				var err error
				pending, err = queue.ConversationInputPending(ctx, machine, id)
				if err != nil {
					return err
				}
			}
			client, _ := p["client_msg_id"].(string)
			if client == "" {
				client, _ = p["once_id"].(string)
				if client != "" {
					client = "segment-once:" + client
					p["client_msg_id"] = client
				}
			}
			needsQueue := current.Switch != nil || pending
			if _, external := ctx.Value(logicalTransportKey{}).(devicewire.NodeCall); !needsQueue && queued && current.Epoch > 1 && (client != "" || !external) {
				// The handoff an idle acceptance kept goes with this input (#14488): the queue attaches it once. A node's
				// own send without a message id cannot be queued, and goes on without it as before.
				handoff, err := queue.ConversationPendingHandoff(ctx, machine, id, active.ID)
				if err != nil {
					return err
				}
				needsQueue = len(handoff) > 0
			}
			if client == "" && needsQueue {
				if _, external := ctx.Value(logicalTransportKey{}).(devicewire.NodeCall); external {
					return logicalRefusal(ctx, "message_id_required")
				}
				client = "segment-input:" + randomHex(16)
				p["client_msg_id"] = client
			}
			if client != "" {
				body, e := marshalLogicalInput(ctx, method, p)
				if e != nil {
					return e
				}
				seen, e := st.ConversationInputReceipt(ctx, machine, id, client, body)
				if e != nil {
					return e
				}
				if seen != "" || needsQueue {
					if seen == "" {
						if e = st.EnqueueConversationInput(ctx, machine, id, client, current.Epoch, body); e != nil {
							return e
						}
					}
					reply := logicalQueuedReply{OK: true, Queued: seen != "accepted" && seen != "rejected" && seen != db.ConversationInputDismissed, Duplicate: seen != "", SessionID: wireids.SessionID(id), ClientMsgID: wireids.ClientMsgID(client)}
					if reply.Queued {
						// Not gone in yet: the sender sees its bubble with why at once (logical_undelivered.go), and so does
						// every other device from the next snapshot. Never a bare success that then vanishes.
						kept := s.logicalQueuedMessage(ctx, machine, id, client)
						reply.Delivered, reply.Undelivered = logicalDeliveredLater, &kept
						s.publishSoon()
					}
					out, e = json.Marshal(reply)
					s.historyWake()
					return e
				}
			}
		}
		if logicalSendMethod(method) && logicalSourceMayBeRetired(current) {
			if _, err := s.logicalPhysical(ctx, active.MachineID, "assistant.segment.restore", map[string]any{"session_id": active.SessionID}); err != nil {
				return err
			}
		}
		if launcher, _ := p["launcher"].(string); launcher != "" && launcher != active.Launcher && (logicalSendMethod(method) || method == "session.modes" || method == "session.switch") {
			target, err := s.logicalTarget(ctx, active.MachineID, launcher)
			if err != nil {
				return err
			}
			if target.Engine != active.Engine {
				return logicalRefusal(ctx, "explicit_switch_required")
			}
		}
		physical := cloneLogicalParams(p, active)
		if withDiagnostic && active.MachineID == machine && active.SessionID == id {
			// This delivery neither queues nor changes the verified recipient, under the same epoch lock.
			physical["text"] = diagnostic.block + diagnostic.plain
		}
		for _, key := range []string{"at_item_id", "answer_again_after"} {
			if ref, _ := physical[key].(string); ref != "" {
				source, err := activeLogicalItemID(active, ref)
				if err != nil {
					return logicalRefusal(ctx, "private_segment")
				}
				physical[key] = source
			}
		}
		if method == "session.watch" {
			for _, key := range []string{"since_seq", "after_seq"} {
				if n, ok := physical[key].(float64); ok {
					physical[key] = max(0, int(n)-active.Offset)
				}
			}
			physical["history"] = active.SourceHistory
		}
		if active.Ordinal > 1 && (logicalSendMethod(method) || method == assistantwire.DeviceAssistantAnswerAgain) {
			marker, ok := s.store().(logicalSegmentInputStore)
			if !ok {
				return logicalRefusal(ctx, "unavailable")
			}
			if err := marker.MarkConversationSegmentDispatched(ctx, machine, id, active.ID); err != nil {
				return err
			}
		}
		var e error
		dispatched = true
		out, e = s.logicalPhysical(ctx, active.MachineID, method, physical)
		if e == nil && method == "session.modes" {
			if modes, ok := s.store().(logicalModesStore); ok {
				value := func(key string) *string {
					if v, ok := p[key].(string); ok {
						copy := v
						return &copy
					}
					return nil
				}
				e = modes.SetConversationSegmentModes(ctx, machine, id, current.Epoch, active.ID, value("model"), value("effort"), value("permission_mode"))
				s.logicalChanged()
			}
		}
		return e
	})
	if errors.Is(err, db.ErrConversationEpoch) || errors.Is(err, db.ErrConversationSwitchBusy) {
		return nil, true, logicalRefusal(ctx, "busy")
	}
	if err != nil {
		if a2aMachine != "" && !dispatched {
			return nil, true, logicalRefusal(ctx, "busy")
		}
		return nil, true, err
	}
	if method == "session.fork" {
		// The response identifies the newly created branch, not its source.
		return out, true, nil
	}
	// Control replies may contain the physical session row. Only the public root
	// is returned to the application; source references stay inside the manager.
	return publicLogicalReply(out, c), true, nil
}

// Automatic role context is resolved again at delivery and must not change a
// client's idempotency identity or leave another copy in the durable queue.
func marshalLogicalInput(ctx context.Context, method string, p map[string]any) (json.RawMessage, error) {
	params := maps.Clone(p)
	delete(params, "supervision_instructions")
	queued := logicalInput{Method: method, Params: params}
	if original, ok := ctx.Value(logicalTransportKey{}).(devicewire.NodeCall); ok {
		original.Params, original.Fallback = nil, nil
		original.Request = ""
		queued.Transport = &original
	}
	return json.Marshal(queued)
}

func cloneLogicalParams(p map[string]any, active conversationlog.Segment) map[string]any {
	out := make(map[string]any, len(p))
	for k, v := range p {
		out[k] = v
	}
	out["session_id"] = active.SessionID
	return out
}

func publicLogicalReply(raw json.RawMessage, c *db.LogicalConversation) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return raw
	}
	if _, ok := obj["session_id"]; ok {
		obj["session_id"], _ = json.Marshal(c.SessionID)
	}
	if _, ok := obj["machine_id"]; ok {
		obj["machine_id"], _ = json.Marshal(c.MachineID)
	}
	if row, ok := obj["session"]; ok {
		obj["session"] = publicLogicalReply(row, c)
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}

// Called only after the node/hub authenticated the actual physical caller.
// An alias is never itself evidence that a retired process holds today's role.
func (s *Service) logicalCaller(ctx context.Context, machine, session string) (string, string, *db.LogicalConversation, error) {
	st, ok := s.store().(logicalConversationStore)
	if !ok {
		return machine, session, nil, nil
	}
	c, err := s.physicalLogicalConversation(ctx, st, machine, session)
	if err != nil || c == nil {
		return machine, session, c, err
	}
	a, ok := c.Active()
	if c.Deleted || !ok || !s.physicalSameMachine(a.MachineID, machine) || a.SessionID != session {
		return "", "", c, logicalRefusal(ctx, "retired")
	}
	return c.MachineID, c.SessionID, c, nil
}

func logicalNotTaken(err error) bool {
	if err == nil {
		return false
	}
	// This boundary has already called the physical node. An untyped error
	// cannot be classified as a local validation refusal: the reply may be lost.
	_, coded := relayErrCode(err)
	if (coded || errors.Is(err, apierr.ErrOffline) || errors.Is(err, rpc.ErrNotSent) || errors.Is(err, rpc.ErrNotWritten)) && sendNotTaken(err) {
		return true
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		return strings.Contains(string(re.Data), `"delivered":"none"`)
	}
	return false
}

// A switched conversation has an explicit execution choice. Global new-chat
// defaults cannot silently select its retired engine on the next internal send.
func (s *Service) logicalSendModel(ctx context.Context, machine, session string, p map[string]any) (bool, error) {
	st, ok := s.store().(logicalConversationStore)
	if !ok {
		return false, nil
	}
	c, err := st.LogicalConversation(ctx, machine, session)
	if err != nil {
		s.logf("load logical conversation model (%s/%s): %v", machine, short(session), err)
		return true, i18n.Err("assistant-model_pick.err_unreadable")
	}
	if c == nil || c.Epoch == 1 {
		return false, nil
	}
	a, ok := c.Active()
	if !ok || c.Deleted {
		return true, logicalRefusal(ctx, "unavailable")
	}
	if a.Ordinal == 1 {
		return false, nil
	}
	launcher, _ := p["launcher"].(string)
	if launcher != "" {
		target, err := s.logicalTarget(ctx, a.MachineID, launcher)
		if err != nil {
			return true, err
		}
		engine := target.Engine
		if engine != a.Engine {
			return true, logicalRefusal(ctx, "explicit_switch_required")
		}
	} else {
		launcher = a.Launcher
		p["launcher"] = launcher
	}
	if launcher == "" {
		return true, logicalRefusal(ctx, "unknown_engine")
	}
	if model, _ := p["model"].(string); strings.TrimSpace(model) == "" {
		if a.Model != "" {
			p["model"] = a.Model
		} else if inherited, _ := p[larkAgentInheritedModelKey].(bool); !inherited && !proto.IsExternalLauncher(launcher) {
			if effort, _ := p["effort"].(string); effort == "" && a.Effort != "" {
				p["effort"] = a.Effort
			}
			if mode, _ := p["permission_mode"].(string); mode == "" && a.PermissionMode != "" {
				p["permission_mode"] = a.PermissionMode
			}
			delete(p, "model")
			purpose := s.conversationModelPurpose(ctx, machine, session)
			if err := preservePermissionMode(p, func() error { return s.applyModel(ctx, purpose, p) }); err != nil {
				return true, err
			}
			return true, nil
		} else {
			p["model"] = a.Model
		}
	} else if err := checkModelID(model); err != nil {
		return true, err
	}
	if effort, _ := p["effort"].(string); effort == "" {
		p["effort"] = a.Effort
	}
	if mode, _ := p["permission_mode"].(string); mode == "" {
		p["permission_mode"] = a.PermissionMode
	}
	return true, nil
}

// Execution controls may only refer to the active physical source. A retired
// reference must never select an unrelated same-ID item in the new engine.
func activeLogicalItemID(active conversationlog.Segment, ref string) (string, error) {
	if id, source, ok := proto.ParseSegmentItemID(ref); ok {
		if active.Ordinal > 1 && active.ID == id {
			return source, nil
		}
		return "", db.ErrConversationEpoch
	}
	if active.Ordinal == 1 {
		return ref, nil
	}
	return "", db.ErrConversationEpoch
}
