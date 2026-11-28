package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
)

// What the inbox asks of the hub: its settings, the sources' (a mail's text, a reply, a connector's group tools…),
// the knowledge base's. The models are the assistant's own (models.go), reached in this file only (inboxModelJSON,
// inboxModelComplete).

// The model configuration's purposes the inbox uses.
const (
	purposeTriage = "inbox.triage" // model 1: triage and summary, relating batches to events
	purposeJudge  = "inbox.judge"  // model 2: which "Needs you" items the AI twin takes
	purposeDraft  = "inbox.draft"  // "AI draft reply"
	purposeAgent  = "inbox.agent"  // model 3: the AI twin's own conversation (a launcher)
	// purposeTranslate translates mail and short texts (translation, inbox_translate.go).
	purposeTranslate = "translate"
)

// inboxModelJSON asks purpose's model (the assistant's model configuration, models.go) for a JSON object into out;
// maxTokens is the room the answer needs (0: the model's default). ctx's deadline bounds the call.
func (s *Service) inboxModelJSON(ctx context.Context, purpose, system, input string, maxTokens int, out any) error {
	if maxTokens > 0 {
		return s.purposeModel(purpose).JSONMax(ctx, system, input, maxTokens, out)
	}
	return s.purposeModel(purpose).JSON(ctx, system, input, out)
}

// inboxModelComplete asks purpose's model for text (ctx with a deadline, like inboxModelJSON).
func (s *Service) inboxModelComplete(ctx context.Context, purpose, system, input string, maxTokens int) (string, error) {
	return s.purposeModel(purpose).Complete(ctx, system, input, maxTokens)
}

// modelUnconfigured: nothing is set up for the purpose.
func modelUnconfigured(err error) bool { return ai.IsNotConfigured(err) }

// modelPaused: the model's connection is held; quota: its quota is used up (rather than busy).
func modelPaused(err error) (paused, quota bool) {
	var p *ai.PausedError
	if !errors.As(err, &p) {
		return false, false
	}
	return true, p.Reason == ai.ReasonQuota
}

// The hub's inbox methods (the sources'), by their wire names.
const (
	inboxKick          = assistantwire.InboxKick
	inboxReplyMail     = assistantwire.InboxReplyMail
	inboxReplyMethod   = assistantwire.InboxReply
	inboxSettle        = assistantwire.InboxSettle
	inboxGroupTool     = assistantwire.InboxGroupTool
	inboxGroupFallback = assistantwire.InboxGroupFallback
	inboxGroupAppend   = assistantwire.InboxGroupAppend
)

// inboxConfigFor is how long the hub's inbox settings are taken as they were read.
const inboxConfigFor = 5 * time.Second

// queryInboxConfig distinguishes a valid configuration object (including {}) from an absent response. appCall accepts
// empty responses for other methods, but an authorization configuration must be an actual JSON object.
func (s *Service) queryInboxConfig(ctx context.Context) (assistantwire.InboxConfig, error) {
	params, _ := json.Marshal(map[string]any{})
	p := AppCall{AppID: AppInbox, Method: assistantwire.InboxConfigMethod, Params: params}
	res, ok, err := s.localFleetAppCall(ctx, p)
	if !ok {
		res, err = s.hub.AppCall(ctx, p)
	}
	if err != nil {
		return assistantwire.InboxConfig{}, err
	}
	res = bytes.TrimSpace(res)
	if len(res) == 0 || bytes.Equal(res, []byte("null")) {
		return assistantwire.InboxConfig{}, errors.New("empty inbox configuration response")
	}
	var cfg assistantwire.InboxConfig
	if err := json.Unmarshal(res, &cfg); err != nil {
		return assistantwire.InboxConfig{}, err
	}
	return cfg, nil
}

// inboxConfig is the hub's inbox settings (InboxConfigMethod), read again after a few seconds; while the hub can't
// say, the last ones (none: the zero settings).
func (s *Service) inboxConfig(ctx context.Context) assistantwire.InboxConfig {
	b := &s.inbox
	b.cfgMu.Lock()
	defer b.cfgMu.Unlock()
	if !b.cfgAt.IsZero() && time.Since(b.cfgAt) < inboxConfigFor {
		return b.cfg
	}
	cfg, err := s.queryInboxConfig(ctx)
	if err != nil {
		s.logf("inbox: the hub's settings: %v", err)
		return b.cfg
	}
	b.cfg, b.cfgAt = cfg, time.Now()
	return cfg
}

// inboxConfigFresh is the outbound authorization read. Unlike the display/background cache, it never substitutes a
// previous or zero configuration when the hub cannot answer: an outward action must not rely on an unknown policy.
func (s *Service) inboxConfigFresh(ctx context.Context) (assistantwire.InboxConfig, error) {
	b := &s.inbox
	b.cfgMu.Lock()
	defer b.cfgMu.Unlock()
	cfg, err := s.queryInboxConfig(ctx)
	if err != nil {
		s.logf("inbox: fresh settings before an outward action: %v", err)
		return assistantwire.InboxConfig{}, err
	}
	b.cfg, b.cfgAt = cfg, time.Now()
	return cfg, nil
}

// inboxMailBody is one mail read in full by the hub.
func (s *Service) inboxMailBody(ctx context.Context, ref assistantwire.InboxMailRef) (assistantwire.InboxMailMessage, error) {
	var m assistantwire.InboxMailMessage
	err := s.appCall(ctx, AppInbox, assistantwire.InboxMailBody, ref, &m)
	return m, err
}

type inboxExpectedSenderKey struct{}

var errInboxSenderChanged = i18n.Err("assistant-outbound.scope_changed")

// connectorReply sends a reply (or a document's status moves) through connector source at the hub, once per sendID
// ("": no such guard; inboxSent).
func (s *Service) connectorReply(ctx context.Context, source string, r connector.InboxReply, sendID string) (bool, error) {
	var sent assistantwire.InboxSent
	params := map[string]any{"source": source, "reply": r, "send_id": sendID}
	if source == "larkcli" {
		identity, err := s.outboundIdentity(ctx)
		if err != nil {
			return false, err
		}
		params["sender_identity"] = identity
	}
	if expected, _ := ctx.Value(inboxExpectedSenderKey{}).(string); expected != "" && params["sender_identity"] != expected {
		return false, errInboxSenderChanged
	}
	approval, automatic := ctx.Value(outboundApprovalKey{}).(outboundApproval)
	if automatic && params["sender_identity"] != approval.Identity {
		return false, i18n.Err("assistant-outbound.scope_changed")
	}
	if automatic && approval.Kind == "standing_authorization" {
		// Collection may have updated the item after its suggestion was checked.
		// Authorize the actual recipient at the transport boundary, not the snapshot.
		grant, err := s.messageGrant(ctx, source, messageTarget(r.Thread), approval.Scenario, approval.Identity)
		if err != nil {
			return false, err
		}
		if grant == nil || grant.ID != approval.ID {
			return false, i18n.Err("assistant-outbound.scope_changed")
		}
	}
	if automatic || params["sender_identity"] == "user" {
		var policy struct {
			SenderIdentity bool `json:"sender_identity"`
		}
		if err := s.appCall(ctx, AppInbox, assistantwire.InboxMessagePolicy, map[string]any{}, &policy); err != nil || !policy.SenderIdentity {
			return false, errors.New("message not sent: upgrade the extension manager before using outward-message settings")
		}
	}
	send := func() (bool, error) {
		err := s.appCall(ctx, AppInbox, inboxReplyMethod, params, &sent)
		return sent.Already, err
	}
	var err error
	var already bool
	if automatic {
		approval.Thread = r.Thread
		params["sender_identity"] = approval.Identity
		if approval.Kind == "standing_authorization" && approval.Scenario == "inbox_reply" {
			// Rewording a failed/uncertain draft must not turn it into a new
			// automatic attempt against the same incoming message.
			sendID = "auto-reply:" + namedClientID(source+"\n"+r.Thread+"\n"+r.Last+"\n"+approval.Identity)
			params["send_id"] = sendID
		}
		already, err = s.auditedMessage(ctx, source, messageTarget(r.Thread), r.Text, sendID, approval, send)
		if errors.Is(err, errOutboundAuditUnrecorded) {
			return false, unrecordedSuggestionReply()
		}
	} else {
		already, err = send()
	}
	s.inboxSent(sent, sendID)
	return already, err
}

// inboxSent notes a reply the hub had sent before under the same send id (a retry after the answer was lost): it is
// taken as sent.
func (s *Service) inboxSent(sent assistantwire.InboxSent, sendID string) {
	if sent.Already {
		s.logf("inbox: reply %s had gone out before: not sent again", sendID)
	}
}
