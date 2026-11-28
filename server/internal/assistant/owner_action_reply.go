package assistant

import (
	"context"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/proto"
)

const ownerApprovalHandledParam = "owner_approval_handled"

// resolveAppOwnerReply consumes an authenticated app send's explicit proposal
// target. Human, own-words and via checks are completed by messageParams before
// this call; clientMsgID is the stable owner event, never a model-provided ID.
func (s *Service) resolveAppOwnerReply(ctx context.Context, machine, session, clientMsgID, target, text, rawCreatedAt string) (bool, error) {
	if clientMsgID == "" || target == "" || !ownerApprovalPhrase(s.settings(ctx), text) {
		return false, nil
	}
	var expected *interactionExpectation
	var expectedAt *time.Time
	if rawCreatedAt != "" {
		at, err := time.Parse(time.RFC3339Nano, rawCreatedAt)
		if err != nil {
			return false, errOwnerActionProof
		}
		expectedAt = &at
		expected = &interactionExpectation{createdAt: &at}
	}
	ix, pending := s.pendingIx(target)
	if !pending {
		book, err := s.ownerActions()
		if err != nil {
			return false, err
		}
		origin, found, err := book.targetOrigin(ctx, target)
		if err != nil {
			return false, err
		}
		if found && origin.MachineID == machine && origin.SessionID == session {
			if expectedAt != nil && !origin.CreatedAt.Equal(*expectedAt) {
				return false, errOwnerActionProof
			}
			if !origin.Terminal {
				// A restart can leave a durable unanswered proposal unrestored.
				// Never turn that state into a successful approval receipt.
				return false, errNotPending
			}
			return true, nil // the first terminal decision already won; a retry has no new effect
		}
		return false, errOwnerActionProof
	}
	if ix.ID != target || ix.MachineID != machine || ix.SessionID != session || !ownerActionProposal(ix) {
		return false, errOwnerActionProof
	}
	allow := true
	if err := s.resolveOwnerProposalAt(ctx, ix, proto.Resolution{Allow: &allow}, ownerReplyProof{
		AuthenticatedOwner: true,
		DirectParent:       true,
		ExactPhrase:        true,
		EventID:            "app:" + machine + ":" + session + ":" + clientMsgID,
	}, expected); err != nil {
		return false, err
	}
	return true, nil
}

// botApproveOwnerAction handles only an exact affirmative phrase directly
// replying to a server-owned proposal card in the authenticated owner's DM.
// Once a parent names such a card, every verification or delivery failure is
// final for this message: the words never fall through to a model turn.
func (s *Service) botApproveOwnerAction(ctx context.Context, ev botcard.MessageEvent, text, action string) bool {
	msg := ev.Message
	if msg.ChatType != "p2p" || msg.ParentID == "" || action == "" || !ownerApprovalPhrase(s.settings(ctx), text) {
		return false
	}

	id := ""
	ambiguous := false
	// An ambiguous persisted mapping answers none: never guess which operation was approved.
	s.larkModule().Book(ctx, func(b assistantwire.ChatBook) {
		for candidate, card := range b.Cards() {
			if !card.Ix.OwnerProposal || card.MessageID != msg.ParentID {
				continue
			}
			if id != "" {
				id = ""
				ambiguous = true
				break
			}
			id = candidate
		}
	})
	if id == "" && !ambiguous {
		return false
	}

	fail := func(err error) {
		target := id
		if target == "" {
			target = "parent " + msg.ParentID
		}
		s.logf("bots: exact owner approval %s: %v", target, err)
		s.chat().Note(ctx, larkhost.Target{}, msg.MessageID, i18n.T(s.botLang(ctx), "assistant-channels_plugin.answer_not_sent",
			i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
	}
	if ambiguous {
		fail(errOwnerActionProof)
		return true
	}
	ix, pending := s.pendingIx(id)
	var ref assistantwire.ChatCardRef
	hasRef := false
	s.larkModule().Book(ctx, func(b assistantwire.ChatBook) { ref, hasRef = b.CardRef(id) })
	if !pending {
		book, err := s.ownerActions()
		if err != nil {
			fail(err)
			return true
		}
		origin, found, err := book.targetOrigin(ctx, id)
		if err != nil {
			fail(err)
			return true
		}
		if found && origin.Terminal && hasRef && origin.MachineID == ref.MachineID && origin.SessionID == ref.SessionID &&
			(ref.CreatedAt.IsZero() || origin.CreatedAt.Equal(ref.CreatedAt)) {
			s.chat().Note(ctx, larkhost.Target{}, msg.MessageID, i18n.T(s.botLang(ctx), "assistant-channels_plugin.answered_elsewhere"))
		} else {
			fail(errNotPending)
		}
		return true
	}
	// ChatID is empty only for the owner's DM. The pending interaction, rather
	// than the compact card mapping, is authoritative for the complete proposal.
	if ix.ID != id || ix.MachineID == "" || ix.SessionID == "" || !ownerActionProposal(ix) {
		fail(errOwnerActionProof)
		return true
	}
	validLocation := hasRef && ref.ChatID == "" && ref.MessageID == msg.ParentID &&
		ref.ApprovalBatchID == ix.ApprovalBatchID && ref.ApprovalItemID == ix.ApprovalItemID &&
		ref.MachineID == ix.MachineID && ref.SessionID == ix.SessionID &&
		(ref.CreatedAt.IsZero() || ix.CreatedAt.IsZero() || ref.CreatedAt.Equal(ix.CreatedAt))
	if !validLocation {
		fail(errOwnerActionProof)
		return true
	}

	expected := assistantwire.OwnerApprovalProof{MessageID: msg.MessageID, ParentID: msg.ParentID, ChatID: msg.ChatID, Text: text}
	var got struct {
		Proof assistantwire.OwnerApprovalProof `json:"owner_approval_proof"`
	}
	params := map[string]any{
		"interaction_id": id, "action": action, "channel": channel.Feishu, "kind": ix.Kind,
		"approval_reply": true, "message_id": expected.MessageID, "parent_id": expected.ParentID,
		"chat_id": expected.ChatID, "text": expected.Text,
	}
	if !ref.CreatedAt.IsZero() {
		params["created_at"] = ref.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	if err := s.appCall(ctx, AppInteractions, assistantwire.InteractionsOwnerAction, params, &got); err != nil {
		fail(err)
		return true
	}
	if got.Proof != expected {
		fail(errOwnerActionProof)
		return true
	}
	allow := true
	err := s.resolveOwnerProposalAt(ctx, ix, proto.Resolution{Allow: &allow}, ownerReplyProof{
		AuthenticatedOwner: true,
		DirectParent:       true,
		ExactPhrase:        true,
		EventID:            "feishu:" + got.Proof.MessageID,
	}, &interactionExpectation{occurrence: &ix})
	if err != nil {
		if errors.Is(err, errNotPending) {
			s.chat().Note(ctx, larkhost.Target{}, msg.MessageID, i18n.T(s.botLang(ctx), "assistant-channels_plugin.answered_elsewhere"))
		} else {
			fail(err)
		}
	}
	return true
}
