package assistant

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A relay send that failed is classified once, by what the failure says, and every path that relays into an assistant
// conversation asks the same function (#11393, docs/assistant/assistant.md "Between conversations"): conversation_send,
// the queue's drain, supervision delivery, relayOrQueue (task ends, spawn notices, chat replies), the owner's card
// answers, approval results and quiet tasks' notices. A message refused for good leaves delivery once, by its kind
// (relayRefused), instead of being retried every relayRetryGap without end.

// relayFailure is what a failed relay send says about its message.
type relayFailure int

const (
	// relayFailedInDoubt: it may have gone in (the time ran out, the link dropped while it went; relayInDoubt). It is
	// looked for in the conversation, then tried again under the same once_id (#10690).
	relayFailedInDoubt relayFailure = iota
	// relayFailedNotNow: the conversation could not take it now (its process did not start, its computer is away): a
	// node error with no code, internal, machine_offline or node_closed. Queued, by conversation_send too (#10194).
	relayFailedNotNow
	// relayFailedBusy: not now either, by a code the node gives while the conversation is at work or away
	// (relayBusyCodes). Queued; conversation_send still returns it to the sender (#11393, decision 1).
	relayFailedBusy
	// relayFailedSetup: the owner must fix the setup first (relaySetupCodes). Queued until it goes in; conversation_send
	// returns it.
	relayFailedSetup
	// relayFailedGone: the conversation is gone (or the runtime reports not_found). Dropped; conversation_send returns it.
	relayFailedGone
	// relayFailedLocal: the manager's own refusal, with no code (a pick, model or connection that is gone or cannot be
	// read). Queued, as before; conversation_send returns it.
	relayFailedLocal
	// relayFailedRefused: refused for good, any other code (assistant_supervision_runtime among them). It leaves
	// delivery once, by its kind (relayRefused); conversation_send returns it.
	relayFailedRefused
)

// relayBusyCodes are the refusals of a conversation that cannot take a message now and will later: its process is at
// work (busy on a role change, adapter_busy on an external launcher), compacting, out of start attempts for now
// (assistant_control_failed), waiting on its adapter or changed before it could start (adapter_timeout,
// adapter_unavailable, adapter_revision_mismatch); its runtime is away or hands it to the runtime taking over
// (runtime_away, handover); its node is restarting (node_restarting); or the call ran out of time with the node on it
// (deadline). Read from the node's and the manager's devices layer's refusals on assistant.send, not observed: a code
// missing here and in relaySetupCodes reads as refused for good.
var relayBusyCodes = []string{"busy", "adapter_busy", "compacting", "runtime_away", "assistant_control_failed",
	"adapter_timeout", "adapter_unavailable", "adapter_revision_mismatch", "handover", "node_restarting", devicewire.CodeDeadline}

// relaySetupCodes are the refusals the owner ends by fixing the setup, after which the message goes in: Claude Code or
// the launcher missing on the computer, a computer the hub does not know, a node too old for what the message carries
// (node_outdated, which the devices layer answers before the node is reached), a conversation's model pick that cannot
// run there (out of the provider's scope, its own service, another agent kind, another launcher's private profile),
// and an external launcher's connection that is off, unsigned, misconfigured or cannot serve this conversation
// (relayAdapterSetupCodes).
var relaySetupCodes = append([]string{proto.CodeClaudeMissing, proto.CodeLauncherMissing, assistantwire.CodeNoDevice,
	proto.CodeNodeOutdated, proto.CodeLauncherOutOfScope, proto.CodeLauncherOwnService, assistantwire.CodeLauncherOtherKind,
	"assistant_launcher_profile_switch", "assistant_model_unresolved"}, relayAdapterSetupCodes...)

// relayAdapterSetupCodes are an external launcher's refusals (node/external_assistant.go externalStderrCode) that its
// connection's setup ends: the owner enables, signs in, reconfigures, updates or restores the connection, or picks
// another model for the conversation. Its other refusals are in relayBusyCodes.
var relayAdapterSetupCodes = []string{"adapter_disabled", "adapter_credentials_missing", "adapter_config_conflict",
	"adapter_scope_required", "adapter_sandbox_unsupported", "adapter_probe_unsupported", "adapter_protocol_unsupported",
	"adapter_runtime_unsupported", "adapter_purpose_unsupported", "adapter_platform_unsupported", "adapter_target_not_found",
	"adapter_model_not_found", "adapter_inventory_invalid"}

// relayFailureOf classifies err, the failure of a relay send (not nil).
func relayFailureOf(err error) relayFailure {
	if relayInDoubt(err) {
		return relayFailedInDoubt
	}
	if strings.Contains(err.Error(), "is gone") {
		return relayFailedGone
	}
	if errors.Is(err, apierr.ErrOffline) || errors.Is(err, rpc.ErrNotSent) || errors.Is(err, rpc.ErrNotWritten) { // machine_offline or transport proved nothing went out
		return relayFailedNotNow
	}
	code, coded := relayErrCode(err)
	switch {
	case !coded:
		return relayFailedLocal
	case code == "not_found":
		return relayFailedGone
	case code == "" || code == "internal" || code == CodeMachineOffline || code == CodeNodeClosed:
		return relayFailedNotNow
	case slices.Contains(relayBusyCodes, code):
		return relayFailedBusy
	case slices.Contains(relaySetupCodes, code):
		return relayFailedSetup
	}
	return relayFailedRefused
}

// sendNotTaken: the failed send err (not nil) provably did not reach its conversation, so what it carried took no
// effect there (a direction's notes block, direction_notes.go; #11392). What the node said decides (delivered);
// without it, err is read through relayFailureOf, so an error the relays class anew is read anew here too, and only
// what rules out its having gone in counts, which within three classes is narrower than the class:
//   - a setup to fix (relayFailedSetup: node_outdated, which the manager's devices layer answers before the computer is
//     reached; no_device) and a conversation that is gone;
//   - the computer away (relayFailedNotNow by apierr.ErrOffline or machine_offline), but not the class's node error
//     without a code or link closed with the send on it (node_closed), which may have gone in;
//   - the manager's own refusal (relayFailedLocal: the bus down or suspended, a call cancelled before it was written),
//     but not a cancel after the request was written;
//   - of what relayFailureOf calls in doubt, only what the transport says was never written (rpc.ErrNotSent,
//     rpc.ErrNotWritten: a deadline that ran out before it left, which relayFailureOf reads as any deadline).
//
// A conversation at work and a refusal for good (relayFailedBusy, relayFailedRefused) count as taken without the
// node's word: the node says delivered "none" when it typed nothing, and the class holds a deadline with the node on it.
func sendNotTaken(err error) bool {
	switch nodeDelivered(err) {
	case "none":
		return true
	case "unknown", "yes":
		return false
	}
	unwritten := errors.Is(err, rpc.ErrNotSent) || errors.Is(err, rpc.ErrNotWritten)
	switch relayFailureOf(err) {
	case relayFailedSetup, relayFailedGone:
		return true
	case relayFailedNotNow:
		code, _ := relayErrCode(err)
		return unwritten || errors.Is(err, apierr.ErrOffline) || code == CodeMachineOffline
	case relayFailedLocal:
		return unwritten || !errors.Is(err, context.Canceled)
	case relayFailedInDoubt:
		return unwritten
	}
	return false
}

// relayErrCode is err's code as the node or the hub gave it: an rpc.Error's, or the one of an error the bus gave back
// its kind (busError). coded is false for an error with neither: the manager's own.
func relayErrCode(err error) (code string, coded bool) {
	var ce *codedError
	if errors.As(err, &ce) {
		return ce.code, true
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code, true
	}
	return "", false
}

// roleChangeBusy is the node's specific refusal to replace a still-working process after its task-follow-up role
// changed. Other busy codes (compaction, an adapter, a hand-over) keep their ordinary retry behavior.
func roleChangeBusy(err error) bool {
	code, _ := relayErrCode(err)
	return code == assistantwire.CodeConversationBusy
}

// refusedForGood: the relay send failed with err, and the conversation will never take the message.
func refusedForGood(err error) bool {
	return err != nil && relayFailureOf(err) == relayFailedRefused
}

// approvalResultFrom starts the From of an approval result's relay message (approval_journal.go), which is otherwise a
// card answer's (askAnswerTool).
const approvalResultFrom = "approval-result-"

// relayRefused is what becomes of message msg once conversation to refused it for good with err, by the message's
// kind: the one place that knows (#11393; a queue_refused work event would be written here). stays: msg waits in to's
// queue for a later try, as an approval result does; otherwise it leaves delivery and is not tried again. then, when
// not nil, is what follows: the caller runs it once the queue no longer holds msg, outside the relay lock, so the
// owner's push comes after the message left. Each refusal is asked about once, so each is told once: moved, a line to
// its sender, or a push.
func (s *Service) relayRefused(machine, to string, msg relayMsg, err error) (stays bool, then func(context.Context)) {
	code, _ := relayErrCode(err)
	ctx := context.Background()
	what := "it leaves the queue; the owner is told"
	defer func() { s.logf("relay %s to %s: refused for good (%s): %s", msg.Tool, short(to), code, what) }()
	push := func(ctx context.Context) { s.reportRelayRefused(ctx, machine, to, msg.Tool, code) }
	switch {
	case msg.standalonePermission():
		s.workEvent(machine, to, "ask_internal_delivery_failed", map[string]any{"id": msg.Card, "to": to, "code": code})
		what = "the card can return to the app when its hold ends"
		return false, nil
	case msg.Tool == superviseTool:
		// One try and the owner's push; the task stays open for review. Whether it should move to the main
		// conversation's supervision route instead waits on #11393's decision 2.
		what = "delivery stopped; the owner is told"
		return false, push
	case msg.Tool == askAnswerTool && strings.HasPrefix(msg.From, approvalResultFrom):
		// It waits for the conversation the approval came from: it has no fallback (deliverApprovalResults). A
		// runtime conversation that cannot take it at all ends it with the owner's push, as it always did (#10338).
		if supervisionRefusalCode(code) {
			return false, push
		}
		what = "an approval result stays queued"
		return true, nil
	case msg.Tool == askAnswerTool:
		// The owner's answer to a card goes to the main conversation, as when the conversation that asked is gone
		// (askTarget). Moved before it leaves, so it is never lost; one the main conversation refuses stays there.
		here := s.settings(ctx)
		if here.Main == "" || here.Main == to {
			what = "a card answer stays queued"
			return true, nil
		}
		if !s.moveRelay(here.Main, msg) {
			what = "a card answer stays queued: the main conversation's queue could not be saved"
			return true, nil
		}
		what = "a card answer moved to the main conversation"
		return false, func(ctx context.Context) {
			s.publish(ctx)
			if c, ok := s.session(ctx, here.MachineID, here.Main); ok && c.Status != proto.StatusWorking && !c.Starting {
				s.drainRelays(here.MachineID, []proto.Session{c})
			}
		}
	case msg.Tool == "conversation_send" || msg.Tool == "conversation_new":
		what = "its sender is told"
		s.handoverEnded(msg.Handover, code) // a hand-over of the owner's words it carried (relay_handover.go)
		return false, func(ctx context.Context) { s.tellRelayRefused(ctx, machine, to, msg, code) }
	case msg.Tool == noticeTool && (msg.State == "needs" || msg.State == "failed"):
		what = "the push it stood for goes out"
		return false, func(ctx context.Context) { s.quietNoticeRefused(ctx, msg) }
	case msg.Tool == chatReplyTool:
		// The chat's later batches wait the gap as after one that went in, and the owner is pushed once for this
		// conversation until a chat reply goes into it again (chatReplyRefused).
		if !s.chatReplyRefused(to, msg) {
			what = "it leaves the queue; the owner was told of an earlier chat reply"
			return false, nil
		}
	}
	// Persona's own notices: a quiet task's other notices, a task's end, a spawned conversation's notice, a chat's
	// reply, the line to a sender. Nothing else records the refusal yet, so the owner is pushed once.
	return false, push
}

// moveRelay queues msg, which another conversation refused, for conversation to instead, once: a copy already
// waiting there (from a manager that stopped before the refusing conversation's queue let go of it) is not added
// again. False when the queue cannot be read or saved: nothing was added.
func (s *Service) moveRelay(to string, msg relayMsg) bool {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return false
	}
	msg.TriedAt, msg.Looks, msg.LookAfter = time.Time{}, 0, time.Time{} // no try of it went to to
	id := relayMsgID(to, msg)
	q := b.st.Queue[to]
	if slices.ContainsFunc(q, func(m relayMsg) bool { return relayMsgID(to, m) == id }) {
		return true
	}
	msg.QueuedAt, msg.Depth = time.Now().UTC(), len(q)+1
	b.st.Queue[to] = append(q, msg)
	if s.saveRelayLocked() != nil {
		b.st.Queue[to] = q
		if len(q) == 0 {
			delete(b.st.Queue, to)
		}
		return false
	}
	go s.releaseHeldAsks(to) // as queueRelay: a turn to holds with its own question lets go of it for the answer
	return true
}

// reportRelayRefused pushes the owner that conversation to refused a message of kind tool for good (code): it was not
// delivered and will not be sent again. A supervision message's push says the task is still there to open, and with
// assistant_supervision_runtime that the follow-up needs a compatible launcher on this computer; any other message's
// says that this runtime cannot take Persona's separate instructions, or else that an update was refused.
func (s *Service) reportRelayRefused(ctx context.Context, machine, to, tool, code string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	lang := s.lang(ctx)
	args := i18n.Args{}
	if supervisionRefusalCode(code) {
		args = s.supervisionLauncherArgs(ctx, machine, lang)
	}
	s.push(withCard(ctx, cardRelayRefusal), Notification{DeviceID: machine, SessionID: to,
		Title: i18n.T(lang, "assistant-conversations.relay_delivery_failed"), Body: i18n.T(lang, relayRefusedBody(tool, code), args)})
}

// reportSupervisionUnconfirmed pushes the owner when an unconfirmed supervision relay exhausts its confirmation
// retries and is retired so newer supervision digests can deliver without manual intervention.
func (s *Service) reportSupervisionUnconfirmed(ctx context.Context, machine, to string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	lang := s.lang(ctx)
	s.push(withCard(ctx, cardRelayRefusal), Notification{DeviceID: machine, SessionID: to,
		Title: i18n.T(lang, "assistant-conversations.relay_delivery_failed"),
		Body:  i18n.T(lang, "assistant-conversations.supervision_delivery_unconfirmed")})
}

// relayRefusedBody is the key of the push's text when a message of kind tool is refused for good with code.
func relayRefusedBody(tool, code string) string {
	runtime := supervisionRefusalCode(code)
	switch {
	case code == assistantwire.CodeSupervisionAccount:
		return "rpc-errors.assistant_supervision_account"
	case tool == superviseTool && runtime:
		return "assistant-conversations.supervision_delivery_failed"
	case tool == superviseTool:
		return "assistant-conversations.supervision_delivery_refused"
	case runtime:
		return "rpc-errors.assistant_supervision_runtime"
	}
	return "assistant-conversations.notice_delivery_refused"
}

// relayRefusedTool marks the line that tells a conversation its message was refused for good (relayMsg.Tool);
// relayMsg.From is then the conversation that refused it. It is Persona's own notice: refused in turn, it leaves.
const relayRefusedTool = "relay_refused"

// tellRelayRefused gives msg's sender one line saying its message to conversation to was not delivered and will not
// be tried again (code: why). A hand-over that carries the owner's words is to have its own status line here.
func (s *Service) tellRelayRefused(ctx context.Context, machine, to string, msg relayMsg, code string) {
	if msg.From == "" || msg.From == to {
		return
	}
	name := short(to)
	if c, ok := s.session(ctx, machine, to); ok {
		name = conversationName(c, s.settings(ctx).Main)
	}
	text := prompts.RenderRelayRefused(prompts.RelayRefused{Language: answerLanguage(s.hello(ctx).Locale), Tool: msg.Tool,
		ID: to, Title: name, Code: code, Excerpt: clip(oneLine(msg.Text), 120)})
	s.relayOrQueue(ctx, msg.From, relayMsg{From: to, FromTitle: "对话「" + name + "」", Tool: relayRefusedTool, Text: text, // i18n-ignore: the queue's label, as the task notices'
		At: time.Now().UTC()})
}

// quietNoticeRefused: the conversation refused quiet task msg.From's notice for good, so the push the notice stood
// for goes out after all (quiet.go): the card it stands for while that card still waits (needs), or the failed start
// (failed). A needs notice queued before it named its card stands for the task's newest waiting card.
func (s *Service) quietNoticeRefused(ctx context.Context, msg relayMsg) {
	t, ok := s.todoNow(msg.From)
	if !ok {
		return
	}
	switch msg.State {
	case "needs":
		ix, ok := s.pendingIx(msg.Card)
		if msg.Card == "" {
			ix, ok = s.newestTaskCard(t)
		}
		if ok && !s.muted(ix.MachineID, ix.SessionID) {
			s.notifyInteraction(s.withTask(ix), s.machineName(ix.MachineID), s.linkCovers(ix))
		}
	case "failed":
		machine, reason := "", ""
		for _, a := range t.Agents {
			if a.StartError != "" {
				machine, reason = a.MachineID, a.StartError
			}
		}
		s.notifyTaskFailed(ctx, t, machine, reason)
	}
}

// newestTaskCard is the card raised last among those task t's agents wait on; false when none waits.
func (s *Service) newestTaskCard(t Todo) (proto.Interaction, bool) {
	want := map[string]map[string]bool{}
	for _, a := range t.Agents {
		if a.ReleasedAt != nil {
			continue
		}
		if want[a.MachineID] == nil {
			want[a.MachineID] = map[string]bool{}
		}
		want[a.MachineID][a.SessionID] = true
	}
	var newest proto.Interaction
	found := false
	for _, list := range s.pendingOf(want) {
		for _, ix := range list {
			if !found || ix.CreatedAt.After(newest.CreatedAt) {
				newest, found = ix, true
			}
		}
	}
	return newest, found
}

// refusedDirect: a direct try of msg into conversation to failed with err. A refusal for good is handled by msg's kind
// (relayRefused) at once; true when msg left delivery with it, and the caller queues nothing. Any other failure, and a
// kind that stays, is false: the caller queues msg as before.
func (s *Service) refusedDirect(ctx context.Context, machine, to string, msg relayMsg, err error) bool {
	if !refusedForGood(err) {
		return false
	}
	stays, then := s.relayRefused(machine, to, msg, err)
	if then != nil {
		then(ctx)
	}
	return !stays
}
