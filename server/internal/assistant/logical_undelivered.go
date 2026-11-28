package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/wireids"
)

// A logical conversation's input queue, as its owner sees it (docs/operations/logical-input-reconciliation.md
// "Delivery deadline", #17348's follow-up).
//
// Every input that has not gone in is on the conversation's row as an undelivered message (proto.Session.Undelivered,
// the same bubbles #10194 draws), with why: queued, waiting behind an input whose receipt is not in, waiting for an
// engine switch, awaiting its own receipt until a deadline, or expired. A send that is queued answers with its bubble at
// once (logicalQueuedReply). A row with any of them is never idle (logicalDeliveryState).
//
// An attempt whose receipt is still unknown at its deadline expires (expireLogicalInput): the payload stays, nothing
// is sent again by itself, and the queue behind it goes on at once. Its owner then picks Retry (sent once more, last in
// the queue) or Delete through the undelivered routes (logicalOwnerAction).

// logicalInputDeliveryDeadline is how long an attempt may wait for its receipt before it expires. A var so tests
// shorten it; PERSONA_INPUT_DELIVERY_DEADLINE overrides it within logicalInputDeadlineMin..Max (main.go).
var logicalInputDeliveryDeadline = 150 * time.Second

const (
	logicalInputDeadlineMin = 30 * time.Second
	logicalInputDeadlineMax = 30 * time.Minute
)

// logicalInputDeadlineFrom parses the override; ok=false keeps the default.
func logicalInputDeadlineFrom(v string) (time.Duration, bool) {
	d, err := time.ParseDuration(v)
	if err != nil || d < logicalInputDeadlineMin || d > logicalInputDeadlineMax {
		return 0, false
	}
	return d, true
}

// logicalUndeliveredLimit bounds the inputs one snapshot reads across all conversations.
const logicalUndeliveredLimit = 2000

type logicalInputDeadlineStore interface {
	ExpireConversationInput(context.Context, string, string, int64, db.ConversationInput, string, time.Time) (bool, error)
	ConversationUndeliveredInputs(context.Context, int) ([]db.ConversationUndeliveredInput, error)
	ConversationInputOwners(context.Context, string, string) ([]string, error)
	ResolveExpiredConversationInput(context.Context, string, string, string, string, bool) (bool, error)
}

func (s *Service) logicalInputNow() time.Time {
	if s.logicalInputClock != nil {
		return s.logicalInputClock()
	}
	return time.Now()
}

// expireLogicalInput settles input, still unconfirmed for reason, once its deadline passed, and lets the queue behind
// it go on now rather than at the next sweep. Before the deadline it does nothing: the reconciler asks again.
func (s *Service) expireLogicalInput(ctx context.Context, st logicalSwitchStore, c *db.LogicalConversation, input db.ConversationInput, reason string) error {
	store, ok := st.(logicalInputDeadlineStore)
	now := s.logicalInputNow()
	if !ok || input.ClaimedAt.IsZero() || now.Sub(input.ClaimedAt) < logicalInputDeliveryDeadline {
		return nil
	}
	expired, err := store.ExpireConversationInput(ctx, c.MachineID, c.SessionID, c.Epoch, input, "delivery deadline passed: "+reason, now.Add(-logicalInputDeliveryDeadline))
	if err != nil || !expired {
		return err
	}
	s.logf("assistant: input %s of %s/%s expired unconfirmed after %s; later input goes on", short(input.ClientMsgID), c.MachineID, short(c.SessionID), logicalInputDeliveryDeadline)
	next, err := st.LogicalConversation(ctx, c.MachineID, c.SessionID)
	if err != nil {
		return err
	}
	s.logicalRebind(ctx, next)
	s.logicalChanged()
	s.publishSoon()
	s.historyWake()
	if next == nil || next.Deleted {
		return nil
	}
	return s.drainLogicalInput(ctx, st, next)
}

// logicalUndeliveredViews is every logical conversation's undelivered input, keyed by its public (machine, session).
// ok=false: the store could not be read, and the snapshot is partial.
func (s *Service) logicalUndeliveredViews(ctx context.Context) (map[[2]string][]proto.UndeliveredMessage, bool) {
	store, capable := s.store().(logicalInputDeadlineStore)
	if !capable {
		return nil, true
	}
	rows, err := store.ConversationUndeliveredInputs(ctx, logicalUndeliveredLimit)
	if err != nil {
		s.logf("assistant: undelivered logical input: %v", err)
		return nil, false
	}
	return logicalUndeliveredMessages(s.lang(ctx), rows, logicalInputDeliveryDeadline), true
}

// logicalUndeliveredMessages turns queue rows (in queue order per conversation) into the row's messages.
func logicalUndeliveredMessages(lang i18n.Lang, rows []db.ConversationUndeliveredInput, deadline time.Duration) map[[2]string][]proto.UndeliveredMessage {
	out := map[[2]string][]proto.UndeliveredMessage{}
	for start := 0; start < len(rows); {
		key := [2]string{rows[start].LogicalMachine, rows[start].LogicalSession}
		end := start
		for end < len(rows) && rows[end].LogicalMachine == key[0] && rows[end].LogicalSession == key[1] {
			end++
		}
		group := rows[start:end]
		start = end
		head, queued := "", 0
		for _, r := range group {
			switch r.Input.State {
			case "sending", "in_doubt":
				if head == "" {
					head = r.Input.ClientMsgID
				}
			case "queued":
				queued++
			}
		}
		ahead := 0
		list := make([]proto.UndeliveredMessage, 0, len(group))
		for _, r := range group {
			m := proto.UndeliveredMessage{ID: r.Input.ClientMsgID, Text: r.Text, Images: r.Images, At: r.CreatedAt.UTC(), State: proto.UndeliveredWaiting}
			switch r.Input.State {
			case db.ConversationInputExpired:
				m.State, m.Reason, m.Since = proto.UndeliveredFailed, proto.UndeliveredReceiptExpired, r.ExpiredAt.UTC()
			case "sending", "in_doubt":
				m.Reason, m.Since, m.Deadline, m.Behind = proto.UndeliveredAwaitingReceipt, r.Input.ClaimedAt.UTC(), r.Input.ClaimedAt.Add(deadline).UTC(), queued
				ahead++
			default:
				m.Since, m.Ahead = r.CreatedAt.UTC(), ahead
				switch {
				case head != "":
					m.Reason, m.BlockedBy = proto.UndeliveredBehindUnconfirmed, wireids.ClientMsgID(head)
				case r.SwitchPending:
					m.Reason = proto.UndeliveredSwitchPending
				default:
					m.Reason = proto.UndeliveredQueued
				}
				ahead++
			}
			m.Error = logicalUndeliveredLine(lang, m.Reason)
			list = append(list, m)
		}
		out[key] = list
	}
	return out
}

// logicalUndeliveredLine is the line under the bubble, in the owner's language.
func logicalUndeliveredLine(lang i18n.Lang, reason proto.UndeliveredReason) string {
	switch reason {
	case proto.UndeliveredAwaitingReceipt:
		return i18n.T(lang, "assistant-undelivered.awaiting_receipt")
	case proto.UndeliveredBehindUnconfirmed:
		return i18n.T(lang, "assistant-undelivered.behind_unconfirmed")
	case proto.UndeliveredSwitchPending:
		return i18n.T(lang, "assistant-undelivered.switch_pending")
	case proto.UndeliveredReceiptExpired:
		return i18n.T(lang, "assistant-undelivered.receipt_expired")
	}
	return i18n.T(lang, "assistant-undelivered.queued")
}

// logicalQueuedMessage is the bubble a send that was just queued shows at once: the same message the row carries
// from the next snapshot on. A failed read leaves it as plainly queued, never as delivered.
func (s *Service) logicalQueuedMessage(ctx context.Context, machine, session, id string) proto.UndeliveredMessage {
	lang := s.lang(ctx)
	fallback := proto.UndeliveredMessage{ID: id, At: time.Now().UTC(), State: proto.UndeliveredWaiting, Reason: proto.UndeliveredQueued}
	fallback.Since, fallback.Error = fallback.At, logicalUndeliveredLine(lang, fallback.Reason)
	views, ok := s.logicalUndeliveredViews(ctx)
	if !ok {
		return fallback
	}
	for _, m := range views[[2]string{machine, session}] {
		if m.ID == id {
			return m
		}
	}
	return fallback
}

// logicalDeliveryState is a row's state while its queue holds undelivered input: an expired one waits for the owner
// (needs_you), one on its way is working. Never idle with a message that has not gone in.
func logicalDeliveryState(row proto.Session) string {
	if row.Undelivered == nil {
		return row.State
	}
	on := false
	for _, m := range *row.Undelivered {
		if m.Reason == proto.UndeliveredReceiptExpired {
			return proto.StateNeedsYou
		}
		on = on || m.Reason.Valid()
	}
	if on {
		switch row.State {
		case proto.StateIdle, proto.StateUnread, proto.StateUnknown:
			return proto.StateWorking
		}
	}
	return row.State
}

// logicalOwnerAction is Retry (retry) or Delete on message msg of conversation session when it is a logical
// conversation's expired input. False when no logical conversation holds it: the route goes on as before.
func (s *Service) logicalOwnerAction(w http.ResponseWriter, r *http.Request, session, msg string, retry bool) bool {
	store, ok := s.store().(logicalInputDeadlineStore)
	if !ok {
		return false
	}
	machines, err := store.ConversationInputOwners(r.Context(), session, msg)
	if err != nil || len(machines) == 0 {
		return false
	}
	machine := r.URL.Query().Get("machine_id")
	if machine == "" && len(machines) == 1 {
		machine = machines[0]
	}
	owned := false
	for _, m := range machines {
		owned = owned || m == machine
	}
	if !owned || !s.keepsPickOn(r.Context(), session, machine) {
		apierr.Write(w, http.StatusNotFound, errors.New("no such message"))
		return true
	}
	action := "delete"
	if retry {
		action = "retry"
	}
	actor, _ := json.Marshal(profileActor(r))
	changed, err := store.ResolveExpiredConversationInput(r.Context(), machine, session, msg, string(actor), retry)
	switch {
	case errors.Is(err, db.ErrConversationEpoch):
		apierr.Write(w, http.StatusConflict, errors.New(i18n.T(s.lang(r.Context()), "assistant-undelivered.not_expired")))
		return true
	case errors.Is(err, db.ErrConversationSwitchBusy):
		apierr.Write(w, http.StatusConflict, errors.New(i18n.T(s.lang(r.Context()), "assistant-undelivered.sending_now")))
		return true
	case err != nil:
		s.logf("assistant: owner %s of input %s in %s: %v", action, short(msg), short(session), err)
		apierr.Write(w, http.StatusInternalServerError, errors.New(i18n.T(s.lang(r.Context()), "assistant-undelivered.not_saved")))
		return true
	}
	if changed {
		s.logf("assistant: owner %s on expired input %s of %s/%s", action, short(msg), machine, short(session))
		s.logicalChanged()
		s.publishSoon()
		s.historyWake() // a retried input drains now
	}
	apierr.WriteJSON(w, logicalOwnerActionReply{OK: true, Repeated: !changed})
	return true
}

// logicalOwnerActionReply answers Retry / Delete on a logical input; Repeated: it had already taken effect, and
// nothing was done again (a double tap sends nothing twice).
type logicalOwnerActionReply struct {
	OK       bool `json:"ok"`
	Repeated bool `json:"repeated,omitempty"`
}

// logicalDeliveredLater is a send's "delivered" when its manager holds the message to send later: the value the hub's
// kept answers use (hub/send.go laterBody), which every client draws as the row's undelivered bubble.
const logicalDeliveredLater = "later"

// logicalQueuedReply answers a send the logical queue took (or had already). Delivered and Undelivered are set while
// it has not gone in: the bubble the sender draws at once.
type logicalQueuedReply struct {
	OK          bool                      `json:"ok"`
	Queued      bool                      `json:"queued"`
	Duplicate   bool                      `json:"duplicate"`
	SessionID   wireids.SessionID         `json:"session_id"`
	ClientMsgID wireids.ClientMsgID       `json:"client_msg_id"`
	Delivered   string                    `json:"delivered,omitempty"`
	Undelivered *proto.UndeliveredMessage `json:"undelivered,omitempty"`
}
