package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const outboundIdentityKey = "assistant.outbound_identity"

var errOutboundAuditUnrecorded = errors.New("message not sent: cannot persist audit")

// MessageAuthorization is deliberately separate from a computer/directory grant:
// a repository's "send" permission must never authorize an unrelated chat message.
type MessageAuthorization struct {
	Channel  string `json:"channel"`
	Target   string `json:"target"` // empty explicitly means every chat in this channel
	Scenario string `json:"scenario"`
	Identity string `json:"identity"`
}

func validMessageIdentity(identity string) bool { return identity == "bot" || identity == "user" }

func messageScopeText(l i18n.Lang, scope MessageAuthorization) string {
	target := scope.Target
	if target == "" {
		target = i18n.T(l, "assistant-outbound.all_chats")
	}
	return i18n.T(l, "assistant-outbound.scope_confirm", i18n.Args{
		"channel": scope.Channel, "target": target,
		"scenario": i18n.T(l, "assistant-outbound."+scope.Scenario),
		"identity": i18n.T(l, "assistant-outbound."+scope.Identity),
	})
}

func checkMessageAuthorization(g StandingAuthorization) error {
	m := g.Messages
	if m.Channel != "larkcli" || !validMessageIdentity(m.Identity) ||
		!slices.Contains([]string{"inbox_reply", "assistant_message"}, m.Scenario) ||
		(m.Target != "" && !validMessageChat(m.Target)) {
		return errors.New("message scope needs channel larkcli, an exact oc_ chat (or empty for all chats), scenario inbox_reply or assistant_message, and identity bot or user")
	}
	if g.MachineID != "" || g.Directory != "" || len(g.Actions) != 1 || g.Actions[0] != "send" ||
		len(g.Conditions) != 0 || !g.Reversible {
		return errors.New("message grants must be reversible, contain only send, and have no computer, directory or unverified conditions")
	}
	return nil
}

func validMessageChat(target string) bool {
	if !strings.HasPrefix(target, "oc_") || len(target) < 4 || len(target) > 200 {
		return false
	}
	for _, ch := range target[3:] {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_') {
			return false
		}
	}
	return true
}

func (s *Service) outboundIdentity(ctx context.Context) (string, error) {
	identity := "bot" // the existing lark-cli sender; other connectors keep their own defaults
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, outboundIdentityKey, &identity); err != nil {
			return "", err
		}
	}
	if !validMessageIdentity(identity) {
		return "", errors.New("invalid stored outward-message identity")
	}
	return identity, nil
}

func (s *Service) setOutboundIdentity(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var identity string
	if json.Unmarshal(raw, &identity) != nil || !validMessageIdentity(identity) {
		return nil, errors.New("identity must be bot or user")
	}
	before, err := s.outboundIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if before == identity {
		return &ownChange{summary: "Outward-message identity is unchanged."}, nil
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-outbound.identity_change", i18n.Args{"identity": i18n.T(l, "assistant-outbound."+identity)})
	}, apply: func(ctx context.Context) (string, error) {
		s.standingMu.Lock()
		defer s.standingMu.Unlock()
		if s.store() == nil {
			return "", errors.New("outward messages need persistent settings")
		}
		if err := s.store().PutSetting(ctx, outboundIdentityKey, identity); err != nil {
			return "", err
		}
		return identity, nil
	}}, nil
}

func (s *Service) messageGrant(ctx context.Context, channel, target, scenario, identity string) (*StandingAuthorization, error) {
	if channel != "larkcli" || !validMessageChat(target) {
		return nil, nil
	}
	list, err := s.standingAuthorizations(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range list.Authorizations {
		m := g.Messages
		if m != nil && g.ID != "" && !g.GrantedAt.IsZero() && checkStandingGrant(g) == nil && m.Channel == channel && m.Scenario == scenario &&
			m.Identity == identity && (m.Target == "" || m.Target == target) {
			return &g, nil
		}
	}
	return nil, nil
}

type outboundApproval struct {
	ID, Kind, Scenario, Identity, Machine, Session, Thread string
}
type outboundApprovalKey struct{}

type outboundAuditStore interface {
	RecordSentAsUser(context.Context, db.SentAsUser) (int64, error)
	SetSentAsUserOutcome(context.Context, int64, string) error
	ClaimSend(context.Context, string, string, string) (string, error)
	SettleSend(context.Context, string, string) error
}

// Persist the complete attempt before contacting the transport. An unavailable
// audit store stops an automatic send, rather than silently losing its evidence.
func (s *Service) auditedMessage(ctx context.Context, source, target, text, sendID string, a outboundApproval, send func() (bool, error)) (bool, error) {
	st, ok := s.store().(outboundAuditStore)
	if !ok {
		return false, errOutboundAuditUnrecorded
	}
	attempt := "outbound-audit:" + sendID
	state, err := st.ClaimSend(ctx, attempt, a.Machine, a.Session)
	if err != nil {
		return false, fmt.Errorf("%w: claim attempt: %w", errOutboundAuditUnrecorded, err)
	}
	if state != "" {
		return false, errors.New("this message was already attempted; inspect its audit and destination before a manual retry")
	}
	marshal := func(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
	through := map[string]string{"kind": a.Kind, "authorization_id": a.ID, "scenario": a.Scenario, "outcome": "pending"}
	if a.Kind == "card" {
		through["interaction_id"] = a.ID
	}
	id, err := st.RecordSentAsUser(ctx, db.SentAsUser{Connector: source, Action: "message_send",
		Text:       proto.WrittenText(marshal(map[string]string{"text": text}), connector.SentTextMost),
		Summary:    clipRunesTo(oneLine(text), 300),
		Target:     marshal(map[string]string{"chat": target, "thread": a.Thread, "identity": a.Identity}),
		Origin:     marshal(map[string]string{"machine_id": a.Machine, "session_id": a.Session}),
		LetThrough: marshal(through)})
	if err != nil {
		_ = st.SettleSend(ctx, attempt, "") // no transport call happened
		return false, fmt.Errorf("%w: %w", errOutboundAuditUnrecorded, err)
	}
	already, err := send()
	outcome := "sent"
	if err != nil {
		outcome = "failed_or_unknown"
	} else if already {
		outcome = "already_delivered"
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if saveErr := st.SetSentAsUserOutcome(saveCtx, id, outcome); saveErr != nil {
		return false, fmt.Errorf("delivery may have occurred; audit %d is pending; inspect before retrying: %w", id, saveErr)
	}
	state = db.SendStateDelivered
	if err != nil {
		state = db.SendStateUnknown // even a known failure needs a deliberate, manual retry
	}
	if settleErr := st.SettleSend(saveCtx, attempt, state); settleErr != nil {
		return false, fmt.Errorf("delivery may have occurred; inspect audit %d before retrying: %w", id, settleErr)
	}
	return already, err
}

func messageTarget(thread string) string {
	if !strings.HasPrefix(thread, "larkcli:chat:") {
		return ""
	}
	target, _, _ := strings.Cut(strings.TrimPrefix(thread, "larkcli:chat:"), ":")
	if !validMessageChat(target) {
		return ""
	}
	return target
}

func connectorMessage(target, text string) connector.InboxReply {
	return connector.InboxReply{Thread: "larkcli:chat:" + target, Text: text}
}

// Called only after the suggestion has been persisted. Its normal version and
// send-once guards still decide whether this exact draft can be consumed.
func (s *Service) autoSendSuggestion(ctx context.Context, id string, version int) (bool, error) {
	s.standingMu.Lock()
	defer s.standingMu.Unlock()
	b := &s.inbox
	b.mu.Lock()
	it := b.items[id]
	var target, machine, session string
	if it != nil && it.open() && it.Source == "larkcli" && it.Connector != nil && it.Connector.CanReply {
		g := currentSuggestion(it)
		if g != nil && g.Version == version && g.Kind == SuggestReply && g.Conversation == nil && !g.Handback && len(g.Changes) == 0 {
			target = messageTarget(it.Connector.Thread)
			if it.Agent != nil {
				machine, session = it.Agent.MachineID, it.Agent.SessionID
			}
		}
	}
	b.mu.Unlock()
	if target == "" {
		return false, nil
	}
	identity, err := s.outboundIdentity(ctx)
	if err != nil {
		return false, err
	}
	grant, err := s.messageGrant(ctx, "larkcli", target, "inbox_reply", identity)
	if err != nil || grant == nil {
		return false, err
	}
	ctx = context.WithValue(ctx, outboundApprovalKey{}, outboundApproval{ID: grant.ID, Kind: "standing_authorization",
		Scenario: "inbox_reply", Identity: identity, Machine: machine, Session: session})
	_, err = s.actOnSuggestion(ctx, id, suggestionReq{Version: version, Action: suggestConfirm}, true)
	return true, err
}

func (s *Service) toolMessageSend(ctx context.Context, machine, session string, raw json.RawMessage) (string, error) {
	var in struct {
		Channel string `json:"channel"`
		Target  string `json:"target"`
		Text    string `json:"text"`
		Key     string `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return "", err
	}
	if in.Channel != "larkcli" || !validMessageChat(in.Target) || strings.TrimSpace(in.Text) == "" ||
		len(in.Text) > 16000 || strings.TrimSpace(in.Key) == "" || len(in.Key) > 200 {
		return "", errors.New("channel larkcli, an exact oc_ target, text (1–16000 bytes), and a stable idempotency_key are required")
	}
	if s.selfPolicy(ctx, session).ConfirmOutbound || s.justMeNow(machine, session) {
		return "", errors.New("this conversation cannot send outward messages")
	}
	// Read the default under the same lock as identity changes and the grant.
	s.standingMu.Lock()
	identity, err := s.outboundIdentity(ctx)
	if err != nil {
		s.standingMu.Unlock()
		return "", err
	}
	a := outboundApproval{Scenario: "assistant_message", Identity: identity, Machine: machine, Session: session}
	// Serialize revocation and identity changes with the final grant check and send.
	// Never hold this lock while waiting for a manual approval.
	grant, err := s.messageGrant(ctx, in.Channel, in.Target, a.Scenario, identity)
	if err != nil {
		s.standingMu.Unlock()
		return "", err
	}
	if grant != nil {
		a.ID, a.Kind = grant.ID, "standing_authorization"
		defer s.standingMu.Unlock()
	} else {
		s.standingMu.Unlock()
		now := time.Now().UTC()
		a.ID, a.Kind = "ix_"+randomHex(6), "card"
		res, err := s.ask(ctx, proto.Interaction{ID: a.ID, MachineID: machine, SessionID: session,
			Kind: proto.IxPermission, ToolName: "message_send", Summary: i18n.T(s.lang(ctx), "assistant-outbound.send_confirm",
				i18n.Args{"channel": in.Channel, "target": in.Target, "identity": i18n.T(s.lang(ctx), "assistant-outbound."+identity), "text": in.Text}),
			CreatedAt: now, ExpiresAt: now.Add(settingCardWait)})
		if err != nil {
			return "", err
		}
		if !res.UserAllowed() {
			return "", errors.New("the owner did not approve: nothing was sent")
		}
		// The owner approved the sender on this card, not a later default.
		s.standingMu.Lock()
		defer s.standingMu.Unlock()
	}
	ctx = context.WithValue(ctx, outboundApprovalKey{}, a)
	already, err := s.connectorReply(ctx, in.Channel, connectorMessage(in.Target, in.Text),
		"outbound:"+namedClientID(machine+"\n"+session+"\n"+in.Key))
	if err != nil {
		return "", err
	}
	if already {
		return "", i18n.Err("assistant-outbound.already_delivered")
	}
	return `{"sent":true}`, nil
}
