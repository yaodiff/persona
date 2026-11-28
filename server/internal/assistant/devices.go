package assistant

import (
	"context"
	"encoding/json"
	"github.com/nwverse/persona/server/internal/devicewire"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant is the chief manager, and the user's computers are its (docs/server/machines.md "Who connects to computers"): its process
// connects them itself (internal/devices), the hub only relays to it. This file is how the bus connection (bus.go)
// takes the hub's devices.* calls and events to that layer, and how the assistant's own calls to a machine go to it
// directly instead of round the hub.

// Devices is the manager's own connections to the computers (internal/devices.Devices).
type Devices interface {
	// Hello takes manager.hello's answer (its devices section); BusUp is told once the connection is up.
	Hello(res json.RawMessage)
	BusUp()
	// Request and Event take the hub's devices.* calls and events (handled false: not one).
	Request(ctx context.Context, method string, params json.RawMessage) (any, bool, error)
	Event(name string, params json.RawMessage) bool
	// Handler serves the apps' routes on machines that are the manager's, the rest going to next.
	Handler(next http.Handler) http.Handler
	// Has: a machine by this id; CallNode calls its node (apierr.ErrOffline, rpc.ErrClosed, the node's errors).
	Has(id string) bool
	CallNode(ctx context.Context, id, method string, params json.RawMessage) (json.RawMessage, error)
	// SetKeep: the sessions each machine's node lists however quiet (the snapshot's profile).
	SetKeep(keep map[string][]string)
	// What the fleet reads of the machines (fleet_machines.go): every machine, one's id as the devices know it (an id
	// another one absorbed: that one), its sessions as its node listed them, when its node's connection began, the
	// token usage.
	List() []devices.Summary
	// Summary is List's entry for the machine id names (Canonical(id)'s), false when List has none: one machine,
	// without building the others.
	Summary(id string) (devices.Summary, bool)
	// Shown is whether a machine's list is its node's and until when it is shown as before while its node is away
	// (devices.Shown); false when there is no such machine.
	Shown(id string) (devices.Shown, bool)
	// MachineResources returns the retained system and latest sample, including a stale sample's timestamp.
	MachineResources(id string) (*proto.Machine, *proto.MetricSample, *bool)
	Canonical(id string) string
	// Sessions copies the whole list out: the caller owns its rows (not their slices, shared and never written into).
	Sessions(id string) ([]proto.Session, bool)
	// AppendSessions is Sessions appended to dst: a reader that copies the same machine's list again and again passes
	// its last copy as dst[:0] and keeps one array (liveLoop).
	AppendSessions(dst []proto.Session, id string) ([]proto.Session, bool)
	// SessionsWhere copies out only the sessions keep accepts, read in place under the devices' lock (keep must not
	// change or keep them, nor call the devices): a view that keeps a few rows of a busy machine copies only those.
	SessionsWhere(id string, keep func(*proto.Session) bool) ([]proto.Session, bool)
	Session(id, session string) (proto.Session, bool)
	// Watched: the hub watches the session for its apps (session.watch), so one has it open.
	Watched(id, session string) bool
	ConnectedAt(id string) time.Time
	Usage() assistantwire.UsageSummary
	// CapacitySnapshot is the account-pool capacity view for scheduling (proto.CapacitySnapshot): how many
	// subscription accounts of each upstream provider are schedulable now and when the nearest one recovers.
	// Read-only, from the manager's in-memory caches; the zero value when no pool is configured.
	CapacitySnapshot(ctx context.Context) proto.CapacitySnapshot
	// UsageAccounts is the merged subscription accounts with their 5 h / 7 d windows (proto.UsageAccount), for the
	// pace-based headroom a tier's candidates are scored by (candidate_headroom.go). Read-only, from the same cache as
	// CapacitySnapshot; nil when nothing is known.
	UsageAccounts(ctx context.Context) []proto.UsageAccount
	// UsageGatewayAccounts is the account-pool gateways' account list as last read (proto.GatewayAccounts), for a
	// gateway pool's headroom when its accounts carry no window numbers (candidate_headroom.go). Never reads a gateway.
	UsageGatewayAccounts() proto.GatewayAccounts
	// GatewayRetryAt uses a bound, complete gateway inventory with fresh account observations. Zero means no hold.
	GatewayRetryAt(source, provider string, now time.Time) time.Time
	// Follow has the node send the manager a session's items and text as they are made (on), or stop.
	Follow(ctx context.Context, machine, session string, on bool) error
	// SetAttention hands over Persona Link's "Needs you" summary (fleet_attention.go) on every look; it goes on to the
	// computers only when it changed.
	SetAttention(a link.Attention)
	// LinkRang waits, within one deadline and while ctx lasts, for one of the manager's Persona Link desktops to have
	// confirmed it handed card's notification to Notification Center: false at once while there is none
	// (notifyInteraction).
	LinkRang(ctx context.Context, card string, within time.Duration) bool
	// HeldMessages are the owner's messages held for machine's Agents while it was away (internal/devices held.go,
	// #10661), by session id, oldest first, worded in lang for their rows; nil when there are none.
	HeldMessages(machine string, lang i18n.Lang) map[string][]proto.UndeliveredMessage
	// HeldRetry and HeldDelete are Retry and Delete on held message msg of session sid, whatever its machine: the
	// assistant's undelivered routes, which older apps call for every row, end there for one they do not keep.
	// found is false when no machine holds such a message; err is an *rpc.Error coded for the app otherwise.
	HeldRetry(ctx context.Context, sid, msg string) (found bool, err error)
	HeldDelete(ctx context.Context, sid, msg string) (found bool, err error)
	// AgentTransfer moves a file or a folder from one computer to another for file_transfer (devices agent_transfer.go,
	// file_transfer.go here), answering once the target has it; the error is a *devices.AgentTransferError when it
	// did not happen.
	AgentTransfer(ctx context.Context, t devices.AgentTransfer) (devices.AgentTransferResult, error)
}

// localDeviceCall is a device.call the manager answers itself: a node's method on a machine it has. The hub still
// answers what it holds (assistantwire.HubDeviceMethods: devices.list with the phone, the interactions, the sessions
// as the apps get them, following a conversation for a channel, a send under the apps' once-only guard).
func (b *BusHub) localDeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, bool, error) {
	d := b.opts.Devices
	if d == nil || p.Device == assistantwire.DeviceAll || assistantwire.HubDeviceMethods[p.Method] || !d.Has(p.Device) {
		return nil, false, nil
	}
	if p.Method == DeviceSessionModes && devicewire.ModeActorOf(ctx) == nil {
		ctx = devicewire.WithModeActor(ctx, devicewire.AssistantModeActor())
	}
	out, err := d.CallNode(ctx, p.Device, p.Method, p.Params)
	return out, true, err
}

// keepFromSnapshot hands the devices what the snapshot's profile says each node keeps listing.
func (b *BusHub) keepFromSnapshot(s Snapshot) {
	d := b.opts.Devices
	if d == nil || s.Profile == nil || s.Profile.Keep == nil {
		return
	}
	keep := map[string][]string{}
	for _, r := range s.Profile.Keep {
		keep[r.MachineID] = append(keep[r.MachineID], r.SessionID)
	}
	d.SetKeep(keep)
}

// NodeAnswer answers the requests of a node that are the assistant's own (devices.Options.Answer): which launcher and
// model a message the node sends a conversation by itself runs on (assistant.model), and the live snapshot before
// each message (assistant.context).
func (s *Service) nodeAnswer(ctx context.Context, machine, method string, params json.RawMessage) (any, bool, error) {
	if err := s.conversationParamsAvailable(ctx, machine, params); err != nil {
		return nil, true, err
	}
	var p struct {
		SessionID       string                           `json:"session_id"`
		Cwd             string                           `json:"cwd"`
		RecoveryID      string                           `json:"recovery_id"`
		RecoveryOnly    bool                             `json:"recovery_only"`
		PrepareRecovery bool                             `json:"prepare_recovery"`
		Query           string                           `json:"query"`
		Ack             []assistantwire.ViewErrorReceipt `json:"ack"`
	}
	switch method {
	case assistantwire.DecisionRaiseMethod.Name:
		var p assistantwire.OutsideDecisionRaiseParams
		if err := json.Unmarshal(params, &p); err != nil {
			// The legacy decoder ignored cwd and used its original shape in JSON type errors.
			type legacyRaiseJSON = struct {
				Decision assistantwire.OutsideDecisionRequest `json:"decision"`
				Source   string                               `json:"source"`
			}
			var legacy legacyRaiseJSON
			if err := json.Unmarshal(params, &legacy); err != nil {
				return assistantwire.OutsideDecisionReply{Code: "invalid_decision", Error: err.Error()}, true, nil
			}
			p.Decision, p.Source = legacy.Decision, legacy.Source
		}
		caller := &outsideCaller{key: "node:" + machine, kind: "script", name: p.Source}
		rec, inserted, err := s.raiseOutsideDecision(ctx, caller, p.Decision)
		if err != nil {
			return outsideErrorReply(err), true, nil
		}
		return outsideReply(rec, inserted), true, nil
	case assistantwire.DecisionGetMethod.Name, assistantwire.DecisionWithdrawMethod.Name:
		var p assistantwire.OutsideDecisionIDParams
		if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
			return assistantwire.OutsideDecisionReply{Code: "invalid_decision", Error: "a decision id is required"}, true, nil
		}
		caller := &outsideCaller{key: "node:" + machine, kind: "script", name: machine}
		var rec *outsideDecision
		var err error
		if method == assistantwire.DecisionGetMethod.Name {
			rec, err = s.outsideForCaller(ctx, caller, string(p.ID))
		} else {
			rec, err = s.withdrawOutsideDecision(ctx, caller, string(p.ID))
		}
		if err != nil {
			return outsideErrorReply(err), true, nil
		}
		return outsideReply(rec, false), true, nil
	case NodeLaunchersPull: // a provider's secrets for this computer (models_launchers_scope.go): the connection's machine decides
		out, err := s.pullLauncher(ctx, machine, params)
		return out, true, err
	case NodeAgentsDefault: // what pi starts a new session on (models_launchers_scope.go agentsDefault)
		return s.agentsDefault(ctx, machine), true, nil
	case "assistant.model":
		_ = json.Unmarshal(params, &p)
		allowed := s.assistantCaller(ctx, machine, p.SessionID)
		if !allowed {
			// The main conversation may still run on its confirmed computer after the
			// default computer changes. Only its kept external pick grants this scope.
			pick, kept := s.modelPickOf(ctx, machine, p.SessionID)
			allowed = kept && proto.IsExternalLauncher(pick.Launcher) && s.externalMainScope(ctx, machine, p.SessionID)
		}
		if !allowed {
			return map[string]any{}, true, nil
		}
		out, err := s.SendParams(ctx, SendParams{MachineID: machine, SessionID: p.SessionID, Params: map[string]any{}})
		return out, true, err
	case "assistant.context":
		_ = json.Unmarshal(params, &p)
		// The machine is the connection's, not anything in params. A guest conversation gets no snapshot: it is the
		// user's state (guest.go).
		if s.isGuest(ctx, machine, p.SessionID) {
			return map[string]any{}, true, nil
		}
		if !s.assistantCaller(ctx, machine, p.SessionID) {
			return ContextResult{}, true, nil
		}
		return s.Context(ctx, ContextParams{SessionID: p.SessionID, MachineID: machine, Cwd: p.Cwd, Ack: p.Ack, RecoveryID: p.RecoveryID, RecoveryOnly: p.RecoveryOnly, PrepareRecovery: p.PrepareRecovery, Query: p.Query}), true, nil
	}
	return nil, false, nil
}
