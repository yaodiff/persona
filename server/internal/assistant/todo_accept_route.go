package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/githubwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

func (s *Service) todoAcceptRoute() {
	// POST /api/v1/todos/{id}/accept {"asked_at"}: the Accept button of a `brief` item. It verifies the task when asked_at is
	// the round that waits now; a button of an earlier round, or a task already verified, is 409. A decision whose Agent
	// cannot take it now (offline, no live Agent, an authentication or transient failure before anything was typed:
	// decisionWaitsForSupervisor) is recorded and handed to the task's supervisor: {"ok":true,"delivered":"none","reason"}
	// (#17431). Any other failed send is 502 accept_not_taken (423 with the node's code when the node refused it, such as
	// dialog_open, #7343), and asks that cannot be read or an acceptance that
	// cannot be saved is 503; either way nothing was taken (conclusion.go, errAcceptStore). A send whose answer may have
	// reached the agent, or did before what followed failed, is 502 send_in_doubt (todo_write_outcome.go). A decision also takes
	// {"option"}: the label of the option chosen (the recommended one is what the plain press takes; no such option is
	// 400), or {"response"}: decide_yourself, not_what_i_asked or explain_first, which takes nothing and goes to the
	// task's supervisor (decision.go; any other value is 400).
	s.handle("POST /api/v1/todos/{id}/accept", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change todos"))
			return
		}
		var in struct {
			AskedAt  string            `json:"asked_at"`
			Option   string            `json:"option"`
			Response string            `json:"response"`
			Answers  map[string]string `json:"answers"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil || in.AskedAt == "" {
			apierr.Write(w, http.StatusBadRequest, errors.New("asked_at is required"))
			return
		}
		var result acceptResult
		var err error
		switch {
		case isAskTodo(r.PathValue("id")):
			if in.Response != "" {
				apierr.Write(w, http.StatusBadRequest, errDecisionOption)
				return
			}
			result, err = s.chooseExpiredQuestion(r.Context(), r.PathValue("id"), in.AskedAt, in.Option, in.Answers)
		case in.Answers != nil:
			apierr.Write(w, http.StatusBadRequest, errDecisionOption)
			return
		case in.Response != "" && in.Option != "":
			apierr.Write(w, http.StatusBadRequest, errors.New("give option or response, not both"))
			return
		case in.Response != "":
			if !slices.Contains(ownerResponses, in.Response) {
				apierr.Write(w, http.StatusBadRequest, errors.New("response must be decide_yourself, not_what_i_asked or explain_first"))
				return
			}
			result.taken, err = s.ownerCannotAnswer(r.Context(), r.PathValue("id"), in.AskedAt, in.Response)
		case in.Option != "":
			result, err = s.chooseOption(r.Context(), r.PathValue("id"), in.AskedAt, in.Option)
		default:
			result, err = s.acceptTryFromApp(r.Context(), r.PathValue("id"), in.AskedAt)
		}
		if errors.Is(err, errCloseNotConfirmed) {
			writeTypedNothing(w, http.StatusUnprocessableEntity, "todo_close_not_confirmed", err)
			return
		}
		if errors.Is(err, errDecisionOption) {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if errors.Is(err, errAcceptStore) { // nothing accepted: the button works again once the store answers
			apierr.Write(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		if isInvestigationGuardError(err) {
			// Unconfirmed or open investigation findings block closing the task, not Agent delivery.
			// Use 422 so older apps keep the button retryable (unlike 409) and do not show a 502 delivery failure.
			writeTypedNothing(w, http.StatusUnprocessableEntity, "investigation_findings_pending", err)
			return
		}
		if delivered := writeDelivered(err); err != nil && delivered != "none" {
			// The words may be with the Agent already, or are (todo_write_outcome.go): a failure every app shows, never
			// "nothing was changed", so the owner looks before taking it again. The button works again (the round still
			// waits), as for any 502.
			writeInDoubt(w, http.StatusBadGateway, err, delivered, answerInDoubtKeys)
			return
		}
		if nodeRefusal(err) {
			// The node refused with a code it words itself (a dialog open in the terminal, a draft in its input box):
			// that code and the node's details, so the app can say what to do first instead of "try again later".
			// Nothing was taken; the button works again (#7343). Not the 409 every other node call answers
			// (apierr.Call): on this route the apps already installed (iOS, Android, older web pages) read any 409 as
			// "this round is over" and lock the button, though nothing reached the agent. 423 Locked (the terminal is
			// held by the dialog) is a failure to every one of them, which shows the hub's sentence and keeps the button.
			writeNodeRefusal(w, err)
			return
		}
		if err != nil { // another delivery failure: nothing typed or taken, the button works again
			// In the request's language, the reason too when the hub words it (an offline computer, no agent).
			writeTypedNothing(w, http.StatusBadGateway, "accept_not_taken", i18n.Err("assistant-reports.accept_not_taken",
				i18n.Args{"error": clip(oneLine(i18n.Localize(err, s.requestCtxLang(r.Context()))), 120)}))
			return
		}
		if !result.taken {
			apierr.Write(w, http.StatusConflict, i18n.Err("assistant-feishu_cards.already_handled"))
			return
		}
		reply := acceptRouteReply{OK: true}
		if result.undelivered != nil {
			reply.Delivered, reply.Reason = "none", decisionNotDeliveredReason(result.undelivered, s.requestCtxLang(r.Context()))
		}
		apierr.WriteJSON(w, reply)
	})
}

// nodeRefusal is err a node's coded refusal (*rpc.Error with a code other than a transport failure's): nothing was
// typed, and its code says why.
func nodeRefusal(err error) bool {
	var re *rpc.Error
	if !errors.As(err, &re) {
		return false
	}
	switch re.Code {
	case "", "internal", "node_unreachable", "timeout":
		return false
	}
	return true
}

// decisionWaitsForSupervisor reports whether a decision whose send failed is recorded and handed to the task's
// supervisor instead of staying with the owner (#17431): nothing can receive it now (noAgentCanTake), its computer is
// away or its agents could not be listed, or the send failed before anything was typed (an authentication failure on
// the node, a transient RPC failure, a request that never went out). Never a coded node refusal the owner acts on (a
// dialog open in the terminal, a draft in its input box: 423), a send that may have reached the Agent (send_in_doubt),
// or the task's own state (completed, not found, the store).
func decisionWaitsForSupervisor(err error) bool {
	if noAgentCanTake(err) {
		return true
	}
	if err == nil || nodeRefusal(err) || writeDelivered(err) != "none" || errors.Is(err, errAcceptStore) {
		return false
	}
	var m *i18n.Message
	if errors.As(err, &m) {
		return m.Key == "assistant-tasks.route_offline"
	}
	var re *rpc.Error
	return errors.Is(err, rpc.ErrNotSent) || errors.Is(err, apierr.ErrOffline) || errors.As(err, &re)
}

// decisionNotDeliveredReason is why a recorded decision did not reach its Agent, as the owner and the supervisor read it: one
// line in l, clipped, with anything token-like blanked.
func decisionNotDeliveredReason(err error, l i18n.Lang) string {
	return clip(oneLine(githubwire.Scrub(i18n.Localize(err, l))), 120)
}

// acceptRouteReply is the accept route's answer to a decision taken. Delivered "none": it was recorded and handed to the
// task's supervisor because no Agent could take it now, Reason saying why.
type acceptRouteReply struct {
	OK        bool   `json:"ok"`
	Delivered string `json:"delivered,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

const timeKeyLayout = time.RFC3339Nano

// acceptReviewRound accepts task id when the round the button was made for
// (askedAt, as the card wrote it) is the round that waits now. The card is not one of the ask's own messages, so
// acceptTry cannot tell an old round's button from a current one by its message id. The errors are acceptTry's: a
// decision whose recommendation did not reach the agent, or errAcceptStore (the asks cannot be read or the acceptance
// saved), which is not "another round".
func (s *Service) acceptReviewRound(ctx context.Context, id, askedAt string) (acceptResult, error) {
	at, err := time.Parse(timeKeyLayout, askedAt)
	if err != nil {
		return acceptResult{}, nil
	}
	s.reviewDismissals.actions.acquire(id)
	defer s.reviewDismissals.actions.release(id)
	return s.acceptTryAsDismissalLocked(ctx, id, "", at, false)
}
