package assistant

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

type logicalDeliveryStore interface {
	RequeueClaimedConversationInput(context.Context, string, string, int64, db.ConversationInput, string) error
	ConversationInputClaimCurrent(context.Context, string, string, int64, db.ConversationInput) (bool, error)
	ResolveClaimedConversationDelivery(context.Context, string, string, int64, db.ConversationInput, string, string) error
	ResolveHistoricalConversationDelivery(context.Context, string, string, int64, db.ConversationInput, string) error
	NoteConversationInputBlocked(context.Context, string, string, int64, db.ConversationInput, string) error
	ResolveConversationInputManually(context.Context, string, string, string, db.ConversationInputResolution) error
}

func (s *Service) logicalBlockedInput(ctx context.Context, st logicalSwitchStore, c *db.LogicalConversation) *db.ConversationInput {
	if c == nil {
		return nil
	}
	input, err := st.ConversationUnresolvedInput(ctx, c.MachineID, c.SessionID)
	if err != nil {
		return &db.ConversationInput{BlockedReason: "delivery diagnostics unavailable"}
	}
	return input
}

func (s *Service) reconcileLogicalInput(ctx context.Context, st logicalSwitchStore, c *db.LogicalConversation, active conversationlog.Segment, input db.ConversationInput) error {
	delivery, ok := st.(logicalDeliveryStore)
	if !ok {
		return logicalRefusal(ctx, "unavailable")
	}
	// Uncertain is bounded: past its deadline the same attempt expires (CAS) and the queue goes on.
	blocked := func(reason string) error {
		if err := delivery.NoteConversationInputBlocked(ctx, c.MachineID, c.SessionID, c.Epoch, input, reason); err != nil {
			return err
		}
		return s.expireLogicalInput(ctx, st, c, input, reason)
	}
	if input.DispatchSegment == "" || input.DispatchMachine == "" || input.DispatchSession == "" || input.DispatchEpoch <= 0 {
		return blocked("original dispatch identity unknown; explicit audited resolution required")
	}
	raw, err := s.logicalPhysical(ctx, input.DispatchMachine, "assistant.segment.input", map[string]any{"session_id": input.DispatchSession, "client_msg_id": input.ClientMsgID})
	var receipt struct {
		Accepted bool `json:"accepted"`
		Rejected bool `json:"rejected"`
	}
	if err != nil {
		return blocked("original dispatch receipt unavailable; automatic replay disabled")
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.Accepted && receipt.Rejected {
		return blocked("invalid original dispatch receipt; automatic replay disabled")
	}
	if !receipt.Accepted && !receipt.Rejected {
		return blocked("original dispatch receipt absent; delivery remains uncertain; automatic replay disabled")
	}
	outcome := "rejected"
	if receipt.Accepted {
		outcome = "accepted"
	}
	if input.DispatchEpoch < c.Epoch {
		err = delivery.ResolveHistoricalConversationDelivery(ctx, c.MachineID, c.SessionID, c.Epoch, input, outcome)
	} else {
		if input.DispatchEpoch != c.Epoch || input.DispatchSegment != active.ID || input.DispatchMachine != active.MachineID || input.DispatchSession != active.SessionID {
			return blocked("dispatch identity conflicts with current activation; explicit audited resolution required")
		}
		operation := ""
		if c.Switch != nil && c.Switch.Phase == "active" && c.Switch.ExpectedEpoch+1 == c.Epoch && c.Switch.TargetSegment == input.DispatchSegment {
			operation = c.Switch.OperationID
		}
		err = delivery.ResolveClaimedConversationDelivery(ctx, c.MachineID, c.SessionID, c.Epoch, input, operation, outcome)
	}
	if err != nil {
		return err
	}
	next, err := st.LogicalConversation(ctx, c.MachineID, c.SessionID)
	if err != nil {
		return err
	}
	s.logicalRebind(ctx, next)
	s.logicalChanged()
	s.publishSoon()
	s.historyWake()
	return nil
}

func (s *Service) logicalDeliveryRoutes() {
	s.handle("POST /api/v1/assistant/conversations/{id}/inputs/resolve", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, 403, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		machine, session := r.URL.Query().Get("machine_id"), r.PathValue("id")
		st, ok := s.store().(logicalSwitchStore)
		delivery, capable := s.store().(logicalDeliveryStore)
		if !ok || !capable || machine == "" {
			apierr.Call(w, logicalRefusal(r.Context(), "unavailable"))
			return
		}
		c, err := st.LogicalConversation(r.Context(), machine, session)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		if c == nil || c.Deleted || c.MachineID != machine || c.SessionID != session || !s.keepsPickOn(r.Context(), session, machine) {
			apierr.Call(w, logicalRefusal(r.Context(), "unavailable"))
			return
		}
		var p db.ConversationInputResolution
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&p); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		actor, _ := json.Marshal(profileActor(r))
		if err := delivery.ResolveConversationInputManually(r.Context(), machine, session, string(actor), p); err != nil {
			if err == db.ErrConversationEpoch || err == db.ErrConversationSwitchBusy {
				apierr.Write(w, 409, err)
			} else {
				apierr.Write(w, 400, err)
			}
			return
		}
		s.logicalChanged()
		s.historyWake()
		apierr.WriteJSON(w, map[string]any{"request_id": p.RequestID, "action": p.Action, "recorded": true})
	})
}
