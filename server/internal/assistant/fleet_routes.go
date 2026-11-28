package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// The apps' routes on the interactions and the sessions' states that are the manager's, relayed by the hub (as
// http.request) with the same shapes as when the hub answered them; while the manager is away the hub answers 503
// assistant_offline. The hub keeps checking what a device may do (an answer-only device's answers) before it relays,
// and answers the lists itself from what the manager told it (GET /api/v1/interactions, GET /api/v1/sessions).

// FleetRoutes are those routes.
var FleetRoutes = []string{
	"POST /api/v1/interactions/{id}/resolve",
	"POST /api/v1/interactions/{id}/clarify",
	"POST /api/v1/interactions/{id}/reanswer",
	"POST /api/v1/interactions/{id}/seen",
	"GET /api/v1/interactions/history",
	"POST /api/v1/sessions/{mid}/{sid}/seen",
}

func requestedInteractionOccurrence(r *http.Request) (*time.Time, error) {
	raw := r.Header.Get(assistantwire.InteractionCreatedAtHeader)
	if raw == "" {
		return nil, nil
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, errors.New("invalid interaction occurrence")
	}
	return &at, nil
}

// fleetHandler serves FleetRoutes, the rest going to next.
func (s *Service) fleetHandler(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/interactions/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		var res proto.Resolution
		if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		res.AnsweredBy, res.Reason = "", "" // only the assistant's interaction_answer says it answered
		res.Remembered = false              // only the manager's own "Always allow" answers say so
		res.Late = false                    // only the history's record of an answer after expiry says so
		id := r.PathValue("id")
		expected, err := requestedInteractionOccurrence(r)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		var approver *assistantwire.HandoffApprover // the device the hub vouched may approve a hand-off card
		if a, ok := assistantwire.HandoffApproverOf(r.Context()); ok {
			approver = &a
		}
		// An answer through this route is a device's, the user's own on one of theirs (an extension's when the hub says
		// so): the history keeps it as such (interactions.ended_by), so it is told from a card that expired or was
		// withdrawn, or that someone answered for the user. The exact occurrence header still fences which card it answers.
		by := db.EndedByDevice
		if r.Header.Get(RoleHeader) == roleExtension {
			by = db.EndedByExtension
		}
		if err := s.resolveApprovedAt(db.WithEndedBy(r.Context(), by), id, res, approver, expected); err != nil {
			switch {
			case errors.Is(err, errOutboundSettingsChanged):
				apierr.WriteCode(w, http.StatusConflict, "outbound_settings_changed", err)
			case errors.Is(err, errOutboundOwnerAnswer):
				apierr.Write(w, http.StatusForbidden, err)
			case errors.Is(err, errHandoffElsewhere):
				apierr.WriteCode(w, http.StatusForbidden, proto.CodeSecretHandoffElsewhere, err)
			case errors.Is(err, errSecretRunEnv):
				apierr.WriteCode(w, http.StatusForbidden, proto.CodeSecretRunEnvElsewhere, err)
			case errors.Is(err, errNotPending):
				s.writeEnded(r.Context(), w, id, res, err, r.Header.Get(RoleHeader) == roleExtension)
			case errors.Is(err, errInvalidElicitation), errors.Is(err, errInvalidQuestionCancel), errors.Is(err, errInvalidEditedAnswer),
				errors.Is(err, errMissingAllow), errors.Is(err, errMissingApprove):
				apierr.Write(w, http.StatusBadRequest, err)
			default:
				apierr.Call(w, err) // prompt_changed: 409, nothing pressed
			}
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/v1/interactions/{id}/clarify", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text        string `json:"text"`
			ClientMsgID string `json:"client_msg_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		// This endpoint has no resolution or field-value envelope: clarification
		// cannot allow, deny, dismiss or attribute the card.
		expected, err := requestedInteractionOccurrence(r)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err := s.clarifyAt(r.Context(), r.PathValue("id"), in.Text, strings.TrimSpace(in.ClientMsgID), nil, expected); err != nil {
			switch {
			case errors.Is(err, errClarifyUnavailable):
				apierr.Write(w, http.StatusGone, err)
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				apierr.Call(w, err)
			default:
				apierr.Write(w, http.StatusBadRequest, err)
			}
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true, "pending": true})
	})
	mux.HandleFunc("POST /api/v1/interactions/{id}/reanswer", s.serveReanswer) // "Change answer" (fleet_delegate.go)
	mux.HandleFunc("POST /api/v1/interactions/{id}/seen", func(w http.ResponseWriter, r *http.Request) {
		s.markInteractionSeen(r.Context(), r.PathValue("id"))
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/interactions/history", s.serveInteractionHistory) // interaction_history.go
	mux.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/seen", func(w http.ResponseWriter, r *http.Request) {
		m, ok := s.fleetMachine(r.PathValue("mid"))
		if !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("unknown machine"))
			return
		}
		s.markSeen(r.Context(), m.ID, r.PathValue("sid"))
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
	mux.Handle("/", next)
	return mux
}

// writeEnded answers a resolve of an interaction that is over already. Answered (or handed back, and handed back
// again) it is the same outcome as the first time: 200 with already and the answer as the history keeps it, so the
// app shows the card as answered rather than an error — the first answer's event may not have reached it. Otherwise
// (expired, withdrawn, handed back, or unknown) 410, with how it ended when the history knows. An extension's token
// (ext) is not given the answer: it may be a card of a space hidden from extensions or hold what was typed into a
// card's fields, which the history does not show it either (docs/extensions/extensions.md "Never shown to
// extensions").
func (s *Service) writeEnded(ctx context.Context, w http.ResponseWriter, id string, r proto.Resolution, err error, ext bool) {
	var status string
	var kept *proto.Resolution
	if just := (*answeredJustNow)(nil); errors.As(err, &just) {
		status, kept = just.status, &just.kept // answered a moment ago: the history may not have it yet (#1230)
	} else if st := s.fleetStore(); st != nil {
		ctx, cancel := context.WithTimeout(ctx, historyTimeout)
		status, kept, _ = st.InteractionOutcome(ctx, id)
		cancel()
	}
	if status == "resolved" || (status == "dismissed" && r.Dismiss) ||
		(status == "cancelled" && r.IsQuestionCancel() && kept != nil && kept.Cancel) {
		out := map[string]any{"ok": true, "already": true, "status": status}
		if kept != nil && !ext {
			out["resolution"] = kept
		}
		apierr.WriteJSON(w, out)
		return
	}
	body := apierr.Body(http.StatusGone, "", err)
	if status != "" && status != "pending" {
		body["status"] = status
	}
	apierr.WriteBody(w, http.StatusGone, body)
}

// ---- the queue as the assistant's own code asks for it (it was the hub's: device.call, app.call) ----

// localSessions is sessions.list as localFleetDeviceCall answers it, for this manager's own readers: the decorated rows
// and the muted ones themselves, not encoded to JSON and decoded again, which was most of what a read cost (#3197).
// The rows share their slices and pointers with the devices' list, as every decorated row does: read, never written
// in place (proto.Session). false when the fleet is not this process's (the hub answers then).
func (s *Service) localSessions(machine string) (list []proto.Session, muted map[string]bool, ok bool, err error) {
	if !s.fleetOn() || machine == DeviceAll {
		return nil, nil, false, nil
	}
	m, found := s.fleetMachine(machine)
	if !found {
		return nil, map[string]bool{}, true, ErrNoDevice
	}
	list = s.decorated(m.ID)
	muted = map[string]bool{}
	for i := range list {
		if s.mutedAs(m.ID, list[i].SessionID, list[i].SpaceName) { // mutedSessions, as a set
			muted[list[i].SessionID] = true
		}
	}
	return list, muted, true, nil
}

// localFleetDeviceCall answers the device methods the hub answered from what was its (the pending interactions, the
// session lists as the apps get them, one session); false for the others.
func (s *Service) localFleetDeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, bool, error) {
	if !s.fleetOn() {
		return nil, false, nil
	}
	switch {
	case p.Method == DeviceInteractionsList:
		if p.Device == DeviceAll {
			out, err := json.Marshal(map[string]any{"interactions": s.visiblePendingInteractions()})
			return out, true, err
		}
		m, ok := s.fleetMachine(p.Device)
		if !ok {
			return nil, true, ErrNoDevice
		}
		list := slices.DeleteFunc(s.visiblePendingInteractions(), func(ix proto.Interaction) bool {
			return ix.MachineID != "" && ix.MachineID != m.ID
		})
		out, err := json.Marshal(map[string]any{"interactions": list})
		return out, true, err
	case p.Device == DeviceAll:
		return nil, false, nil
	case p.Method == DeviceSessionFollow || p.Method == DeviceSessionUnfollow:
		m, ok := s.fleetMachine(p.Device)
		if !ok {
			return nil, true, ErrNoDevice
		}
		var a struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(p.Params, &a); err != nil || a.SessionID == "" {
			return nil, true, fmt.Errorf("%s needs session_id", p.Method)
		}
		err := s.follow(ctx, m.ID, a.SessionID, p.Method == DeviceSessionFollow)
		return json.RawMessage(`{}`), true, err
	case p.Method == DeviceSessionsList:
		m, ok := s.fleetMachine(p.Device)
		if !ok {
			return nil, true, ErrNoDevice
		}
		list := s.decorated(m.ID)
		out, err := json.Marshal(map[string]any{"sessions": list, "muted": s.mutedSessions(m.ID, list)})
		return out, true, err
	case p.Method == DeviceSessionGet:
		m, ok := s.fleetMachine(p.Device)
		if !ok {
			return nil, true, ErrNoDevice
		}
		var a struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(p.Params, &a); err != nil {
			return nil, true, err
		}
		sess, ok := s.nodeSession(m.ID, a.SessionID)
		if ok {
			if !sess.LogicalPlaceholder {
				sess.Assistant = m.isAssistant(sess)
			}
			if sess.Assistant && sess.Space == "" {
				if sess.LogicalPlaceholder {
					if sp, known := s.logicalRowSpace(ctx, sess); known {
						sess.Space = sp
					}
				} else {
					sess.Space = identity.SpaceOf(sess.Cwd)
				}
			}
			s.nameAgentRow(m.ID, &sess)
			// The node fills in no unified state: the callers that ask whether it works or waits for the user
			// (sessionBusy, waitTurnEnd, a repair's end) read the manager's, as the lists have it.
			if d, found := s.decoratedSession(m.ID, a.SessionID); found {
				sess.State, sess.PendingInteractions, sess.StateSince = d.State, d.PendingInteractions, d.StateSince
			}
		}
		out, err := json.Marshal(map[string]any{"session": sess, "ok": ok})
		return out, true, err
	}
	return nil, false, nil
}

// localFleetAppCall answers the interaction queue's app methods (interactions.get, interactions.answer,
// interactions.resolve); false for the others.
func (s *Service) localFleetAppCall(ctx context.Context, p AppCall) (json.RawMessage, bool, error) {
	if !s.fleetOn() || p.AppID != AppInteractions || p.Method == assistantwire.InteractionsOwnerAction {
		return nil, false, nil
	}
	var a struct {
		MachineID     string            `json:"machine_id"`
		SessionID     string            `json:"session_id"`
		InteractionID string            `json:"interaction_id"`
		Answers       map[string]string `json:"answers"`
		Reason        string            `json:"reason"` // interactions.answer: why the assistant answered so (interaction_answer)
		Resolution    proto.Resolution  `json:"resolution"`
		Text          string            `json:"text"`
		ClientMsgID   string            `json:"client_msg_id"`
		ChatID        string            `json:"chat_id"`
		MessageID     string            `json:"message_id"`
		Action        string            `json:"action"`
		Channel       string            `json:"channel"`
		CreatedAt     *time.Time        `json:"created_at"`
	}
	if err := json.Unmarshal(p.Params, &a); err != nil {
		return nil, true, err
	}
	var out any
	switch p.Method {
	case InteractionsGet:
		if ix, ok := s.pendingIx(a.InteractionID); ok {
			out = map[string]any{"interaction": ix, "ok": true}
		} else {
			out = map[string]any{"ok": false, "status": s.interactionOutcome(ctx, a.InteractionID)}
		}
	case assistantwire.InteractionsAnswer:
		ok, err := s.answerQuestion(a.MachineID, a.SessionID, a.InteractionID, a.Answers, a.Reason)
		if err != nil {
			return nil, true, err
		}
		if ok {
			out = map[string]any{"ok": true}
		} else {
			out = map[string]any{"ok": false, "status": s.interactionOutcome(ctx, a.InteractionID)}
		}
	case InteractionsResolve:
		r, err := s.channelResolveAt(ctx, a.Channel, a.InteractionID, a.Resolution, a.Action, a.CreatedAt)
		if err != nil {
			return nil, true, err
		}
		out = r
	case assistantwire.InteractionsClarify:
		r, err := s.channelClarifyAt(ctx, a.Channel, a.InteractionID, a.Text, a.ClientMsgID, a.Action,
			&clarificationOrigin{Channel: a.Channel, ChatID: a.ChatID, MessageID: a.MessageID}, a.CreatedAt)
		if err != nil {
			return nil, true, err
		}
		out = r
	default:
		return nil, false, nil
	}
	b, err := json.Marshal(out)
	return b, true, err
}

// channelResolve answers a card as the user answered it in channel ch (interactions.resolve): only with the owner
// action the hub handed over with that press or message in ch (a card's for that card, a message's for a kind of
// card the channel takes typed), once — the hub takes it (interactions.owner_action, with the card's kind from the
// queue) before the card is answered. It is the user's own answer (no AnsweredBy).
func (s *Service) channelResolve(ctx context.Context, ch, id string, r proto.Resolution, action string) (any, error) {
	return s.channelResolveAt(ctx, ch, id, r, action, nil)
}

func (s *Service) channelResolveAt(ctx context.Context, ch, id string, r proto.Resolution, action string, expected *time.Time) (any, error) {
	id = strings.TrimSpace(id)
	ix, ok := s.pendingIx(id)
	if !ok {
		return map[string]any{"ok": false, "status": s.interactionOutcome(ctx, id)}, nil
	}
	if expected != nil && !ix.CreatedAt.Equal(*expected) {
		return map[string]any{"ok": false, "status": "stale"}, nil
	}
	if len(ix.Fields) > 0 {
		// What the card asks for (a password) is typed in the app, never in a channel: there it can only be declined.
		// Refused before the press is used up, so the card stays as it was.
		if r.Allow == nil || *r.Allow {
			return nil, i18n.Err("assistant-channels_plugin.fill_in_app")
		}
		r.Values = nil
	}
	if !editedAnswerAllowed(ix, r) {
		return nil, errInvalidEditedAnswer
	}
	params := map[string]any{"interaction_id": id, "action": action, "channel": ch, "kind": ix.Kind}
	if expected != nil {
		params["created_at"] = expected.Format(time.RFC3339Nano)
	}
	if err := s.appCall(ctx, AppInteractions, assistantwire.InteractionsOwnerAction, params, nil); err != nil {
		return nil, err
	}
	r.AnsweredBy, r.Remembered = "", false
	if ownerActionProposal(ix) {
		expectation := &interactionExpectation{occurrence: &ix}
		if err := s.resolveOwnerProposalAt(ctx, ix, r, ownerActionCardProof(ix, ch), expectation); err != nil {
			if errors.Is(err, errNotPending) {
				return map[string]any{"ok": false, "status": s.endedStatus(ctx, id, err)}, nil
			}
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	}
	var err error
	if expected == nil {
		err = s.resolve(id, r)
	} else {
		err = s.resolveAnswerAt(ctx, id, r, true, nil, nil, &interactionExpectation{occurrence: &ix})
	}
	if err != nil {
		if errors.Is(err, errNotPending) {
			return map[string]any{"ok": false, "status": s.endedStatus(ctx, id, err)}, nil
		}
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// channelClarify verifies the same fresh owner action as channelResolve, then
// takes the separate non-final path. Blank and over-limit input is rejected
// before that action is consumed. Field values never enter this envelope.
func (s *Service) channelClarify(ctx context.Context, ch, id, text, clientMsgID, action string, origin *clarificationOrigin) (any, error) {
	return s.channelClarifyAt(ctx, ch, id, text, clientMsgID, action, origin, nil)
}

func (s *Service) channelClarifyAt(ctx context.Context, ch, id, text, clientMsgID, action string, origin *clarificationOrigin, expected *time.Time) (any, error) {
	if err := validateClarifyQuestion(text); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	if clientMsgID == "" {
		clientMsgID = "approval-question-" + id + "-" + randomHex(6)
	}
	ix, ok := s.pendingIx(id)
	if !ok {
		return map[string]any{"ok": false, "status": s.interactionOutcome(ctx, id)}, nil
	}
	if expected != nil && !ix.CreatedAt.Equal(*expected) {
		return map[string]any{"ok": false, "status": "stale"}, nil
	}
	if ch != channel.Feishu || !ix.CanClarify || ix.Private {
		return nil, errClarifyUnavailable
	}
	var proof struct {
		Token string `json:"clarification_proof"`
	}
	params := map[string]any{"interaction_id": id, "action": action, "channel": ch, "kind": ix.Kind,
		"clarify": true, "text": text, "client_msg_id": clientMsgID, "machine_id": ix.MachineID, "session_id": ix.SessionID}
	if expected != nil {
		params["created_at"] = expected.Format(time.RFC3339Nano)
	}
	if err := s.appCall(ctx, AppInteractions, assistantwire.InteractionsOwnerAction, params, &proof); err != nil {
		return nil, err
	}
	if proof.Token != "" {
		ctx = assistantwire.WithClarificationProof(ctx, proof.Token)
	}
	var err error
	if expected == nil {
		err = s.clarify(ctx, id, text, clientMsgID, origin)
	} else {
		err = s.clarifyAt(ctx, id, text, clientMsgID, origin, expected)
	}
	if err != nil {
		if errors.Is(err, errClarifyUnavailable) {
			return map[string]any{"ok": false, "status": s.interactionOutcome(ctx, id)}, nil
		}
		return nil, err
	}
	return map[string]any{"ok": true, "pending": true}, nil
}
