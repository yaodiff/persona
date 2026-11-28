package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The manager carries the extensions, the connectors and the bots too (internal/extmgr, docs/extensions/extensions.md "Who owns what"):
// this file is how the bus connection hands that layer the hub's extensions.* calls and events and the relayed
// requests, and how what the assistant asked the hub for of them (the inbox's sources, the channels' transport, the
// connectors for the knowledge base and its directory) goes to it directly instead, as do the assistant's events that
// were the hub's to pass on to the extensions (inbox.changed, kb.changed).

// Extensions is the manager's extensions, connectors and bots (internal/extmgr.Manager).
type Extensions interface {
	waterExtensions
	Hello(res json.RawMessage)
	BusUp() // returns once its report went: the outbox goes after it (outbox.go)
	Request(ctx context.Context, method string, params json.RawMessage) (any, bool, error)
	Event(name string, params json.RawMessage) bool
	// AppCall answers the assistant's app.calls that are its (handled false: the hub's).
	AppCall(ctx context.Context, p assistantwire.AppCall) (json.RawMessage, bool, error)
	// AssistantSaid takes the assistant's events that are its (handled false: they go to the hub).
	AssistantSaid(event string, params json.RawMessage) bool
	http.Handler
}

// waterExtensions exposes only the legacy water migration, never arbitrary extension files.
type waterExtensions interface {
	WaterExtension(ctx context.Context, readState bool) (*assistantwire.WaterExtension, error)
	DisableWaterExtension(w http.ResponseWriter, r *http.Request, storageKey string) bool
}

func (b *BusHub) WaterExtension(ctx context.Context, readState bool) (*assistantwire.WaterExtension, error) {
	if e := b.opts.Extensions; e != nil {
		return e.WaterExtension(ctx, readState)
	}
	return nil, nil
}

func (b *BusHub) DisableWaterExtension(w http.ResponseWriter, r *http.Request, storageKey string) bool {
	if e := b.opts.Extensions; e != nil {
		return e.DisableWaterExtension(w, r, storageKey)
	}
	apierr.Write(w, http.StatusServiceUnavailable, i18n.Err("assistant-health_intake.unavailable")) // as the Health module answers
	return false
}

// localAppCall is an app.call the manager's extensions answer.
func (b *BusHub) localAppCall(ctx context.Context, p AppCall) (json.RawMessage, bool, error) {
	if e := b.opts.Extensions; e != nil {
		return e.AppCall(ctx, p)
	}
	return nil, false, nil
}

// notifyHub sends the hub an event, unless it is the extensions' to take here.
func (b *BusHub) notifyHub(event string, params any) error {
	if e := b.opts.Extensions; e != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		if e.AssistantSaid(event, raw) {
			return nil
		}
		return b.client.Notify(event, json.RawMessage(raw))
	}
	return b.client.Notify(event, params)
}

// Answer is what the extensions ask the assistant in this process: the hub's calls they used to make over the bus
// (channels.receive, feishu.message, inbox.collected, kb.*…), answered as the bus would.
func (b *BusHub) Answer(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	out, err := b.answer(ctx, method, params)
	if err != nil {
		return nil, err
	}
	if raw, ok := out.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(out)
}

// TellLocal is an event the extensions tell the assistant in this process (feishu.owner, kb.kick…).
func (b *BusHub) TellLocal(event string, params json.RawMessage) { b.queueEvent(event, params) }

// ExtensionTools are the nodes' MCP tools and gated commands that are the extensions' (internal/extmgr, in this
// process): the connectors', Cua's, ego's, a gated command's card text, lark-cli's settings for the assistant's machine.
type ExtensionTools interface {
	AgentTools(ctx context.Context, assistant bool) (agents, assistantOnly []proto.Tool)
	CallTool(ctx context.Context, machine, session, name string, args json.RawMessage, assistant bool) (proto.ToolResult, bool)
	// ConnectorTool says whether name is a connector's tool (an extension's), not Cua's or ego's: what a guest
	// conversation may be given (guest.go).
	ConnectorTool(name string) bool
	// ToolAccess classifies an extension-owned tool without executing it. Read-only connector tools and subscription
	// reads may be used by an Inbox handling chat; writes, computer control and unknown extension actions may not.
	ToolAccess(name string) (known, readOnly bool)
	AssistantSummary(ctx context.Context, tool, summary string) (string, bool)
	AssistantLark(ctx context.Context) (proto.AssistantLark, error)
	// AnswerComputerElicit answers the node's computer.elicit (LCU runtime confirmations on the phone/web cards).
	AnswerComputerElicit(ctx context.Context, machine string, params json.RawMessage) (map[string]any, error)
	// RecordNodeSent keeps a write the node's gate proxy forwarded in the sent-as-user ledger (the node's lark.sent);
	// machine is the connection's, never a field of params.
	RecordNodeSent(ctx context.Context, machine string, params json.RawMessage) (any, error)
	// MailAttachment is one mail attachment's bytes for the node that saves it (mail_save_attachment, the node's
	// mail.attachment), only while the mail connector offers the tool to that caller; the caller's machine and
	// conversation are checked first (fleet_tools.go mailAttachment).
	MailAttachment(ctx context.Context, req proto.MailAttachmentRequest) (proto.MailAttachment, error)
}

// ownerHeard is told when the owner speaks in one of the assistant's conversations, a message or an answer to its
// question (extmgr: a computer-use session the user stopped on a Mac starts again on that conversation's next call,
// #7590). It must not block.
type ownerHeard interface {
	OwnerSpoke(session string)
}

// ownerHeard tells the extensions that the owner spoke in conversation session.
func (s *Service) ownerHeard(session string) {
	if s.heard != nil && session != "" {
		s.heard.OwnerSpoke(session)
	}
}

// domainTrackKinds is the in-process source of extension-owned track kinds.
type domainTrackKinds interface {
	DomainTrackKindsJSON(context.Context) (json.RawMessage, error)
}

// SetExtensions gives the service the extensions' tools (before Run).
func (s *Service) SetExtensions(e ExtensionTools) {
	s.ext = e
	s.integrationReads, _ = e.(integrationReads)
	s.trackKindsExt, _ = e.(domainTrackKinds)
	s.heard, _ = e.(ownerHeard)
	if r, ok := e.(launcherSecrets); ok {
		s.refs = r
	}
	s.trans, _ = e.(transcribers)
	s.repoExt, _ = e.(repository.Extensions)
	s.repoStarted = nil
	if started, ok := e.(interface{ Started() <-chan struct{} }); ok && s.repoExt != nil {
		s.repoStarted = started.Started()
	}
	s.transcriptionReadinessReset()
	s.setMemorySources(e) // the memory extensions' entries (memory_extension_source.go)
}

// AskCard is askHub for the extensions in this process: a card of theirs (a mail send, Cua's apps, persona_settings),
// waited for like a hook's; ctx ending withdraws it.
func (s *Service) AskCard(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	return s.askHub(ctx, ix)
}

// AskPreparedCard is the extensions' prepared-assistant-operation bridge. Only
// callers that supply an executable owner operation get CanClarify.
func (s *Service) AskPreparedCard(ctx context.Context, ix proto.Interaction, op approvalops.Operation) (approvalops.Outcome, error) {
	return s.waitPreparedCard(ctx, ix, op)
}

// askPreparedHub coordinates a hub-owned retained setting. Only its opaque ID
// and replacement scope are public; the apply closure remains in the hub.
func (b *BusHub) askPreparedHub(ctx context.Context, ix proto.Interaction, replacement string) (approvalops.Outcome, error) {
	op := approvalops.Operation{
		Replacement: replacement,
		Denied:      "The user declined the change: nothing changed.",
		Unanswered:  prompts.RenderSettingUnanswered(),
		Apply: func(ctx context.Context, r proto.Resolution) (string, error) {
			out, err := buswire.Call(ctx, b.client, assistantwire.FleetApprovalApplyMethod,
				assistantwire.FleetApprovalApply{InteractionID: ix.ID, Resolution: r})
			if err != nil {
				return "", busError(err)
			}
			if out.Stale {
				return "", approvalops.ErrStale
			}
			return out.Text, nil
		},
		Discard: func(ctx context.Context, status string) {
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			_, _ = buswire.Call(ctx, b.client, assistantwire.FleetApprovalDiscardMethod,
				assistantwire.FleetApprovalDiscard{InteractionID: ix.ID, Status: status})
		},
	}
	return b.svc.waitPreparedCard(ctx, ix, op)
}

// mainMachine is where the assistant's conversations run (the extensions give only it the skills' gated commands).
func (s *Service) mainMachine(ctx context.Context) string { return s.settings(ctx).MachineID }

// userTZ is the user's time zone by name, "" while it is not known.
func (s *Service) userTZ() string { return s.settings(context.Background()).Timezone }

// toolsNow are the assistant's tools (a connector's may not shadow one).
func (s *Service) toolsNow() []proto.Tool {
	return append(s.Tools(context.Background()), toolsNamed(healthToolNames)...)
}
