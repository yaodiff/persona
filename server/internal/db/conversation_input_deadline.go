package db

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Delivery deadline (docs/operations/logical-input-reconciliation.md "Delivery deadline").
//
// A claimed input whose receipt stays unknown is uncertain, and uncertainty never
// authorizes replay. It must not fence the queue forever either: past its deadline
// the manager settles the exact attempt (CAS on the dispatch tuple) as expired.
// The payload stays for the owner, the attempt is audited, and the queue moves on.
// Only the owner can send it again (RetryExpiredConversationInput, once) or drop it.

// Input states outside the original reconciliation set.
const (
	ConversationInputExpired   = "expired"
	ConversationInputDismissed = "dismissed"
)

// Actors recorded in conversation_input_resolutions for deadline transitions.
const (
	conversationInputDeadlineActor = "delivery_deadline"
)

// ExpireConversationInput settles one uncertain attempt claimed at or before cutoff.
// False with no error means the attempt is still within its deadline. A changed
// attempt, epoch or a concurrent dispatch (lock held) fails the CAS and changes nothing.
// The expiring first input of the current switch accepts that switch and keeps its
// handoff for the next input: the target already owns the conversation, and the
// context a lost first turn carried must still reach it.
func (d *DB) ExpireConversationInput(ctx context.Context, machine, session string, epoch int64, input ConversationInput, reason string, cutoff time.Time) (bool, error) {
	if reason == "" || len(reason) > 4096 {
		return false, errors.New("invalid input expiry")
	}
	expired := false
	err := conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		if err := lockConversationDelivery(ctx, tx, machine, session, epoch, input); err != nil {
			return err
		}
		var due bool
		if err := tx.QueryRow(ctx, `SELECT coalesce(claimed_at,created_at) <= $4 FROM conversation_inputs WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3`, machine, session, input.ClientMsgID, cutoff).Scan(&due); err != nil {
			return err
		}
		if !due {
			return nil
		}
		key := sha256.Sum256(fmt.Appendf(nil, "%s:%d:%d", input.ClientMsgID, input.DispatchEpoch, input.Version))
		p := ConversationInputResolution{RequestID: fmt.Sprintf("deadline:%x", key), ExpectedEpoch: epoch, Input: input, Action: "expired", Reason: reason}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if err := recordConversationInputResolution(ctx, tx, machine, session, conversationInputDeadlineActor, p, raw); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE conversation_inputs SET state='expired',expired_at=now(),blocked_reason=$4,last_reconciled_at=now(),delivery_version=delivery_version+1 WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3`, machine, session, input.ClientMsgID, reason); err != nil {
			return err
		}
		expired = true
		if input.DispatchEpoch != epoch {
			return nil // historical: never touches the current switch
		}
		var operation string
		err = tx.QueryRow(ctx, `SELECT operation_id FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND phase='active' AND expected_epoch+1=$3 AND target_segment=$4`, machine, session, epoch, input.DispatchSegment).Scan(&operation)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return resolveConversationSwitch(ctx, tx, machine, session, operation, "accepted", "first input unconfirmed past its delivery deadline", switchSettlement{keepHandoff: true})
	})
	return expired && err == nil, err
}

// ConversationUndeliveredInput is one input the owner has to see on the
// conversation's row: queued, awaiting its receipt, or expired. Text and image
// count come from the stored payload; the payload itself never leaves the store.
type ConversationUndeliveredInput struct {
	LogicalMachine string
	LogicalSession string
	// Input has the row's identity, position, state, claim time and blocked reason; never its payload.
	Input     ConversationInput
	Text      string
	Images    int
	CreatedAt time.Time
	ExpiredAt time.Time
	// SwitchPending: the conversation has a switch that has not activated its target yet.
	SwitchPending bool
}

// conversationUndeliveredTextMax bounds the text a row carries per input.
const conversationUndeliveredTextMax = 4000

// ConversationUndeliveredInputs lists every live conversation's undelivered input
// in queue order, at most limit rows.
func (d *DB) ConversationUndeliveredInputs(ctx context.Context, limit int) ([]ConversationUndeliveredInput, error) {
	rows, err := d.pool.Query(ctx, `WITH i AS (
 SELECT i.*, convert_from(i.payload,'UTF8')::jsonb AS doc FROM conversation_inputs i
 JOIN logical_conversations c ON c.machine_id=i.logical_machine AND c.session_id=i.logical_session AND c.deleted_at IS NULL
 WHERE i.state IN ('queued','sending','in_doubt','expired'))
SELECT i.logical_machine,i.logical_session,i.client_msg_id,i.position,i.state,
 left(coalesce(i.doc#>>'{params,text}',''),$2),
 CASE WHEN jsonb_typeof(i.doc#>'{params,images}')='array' THEN jsonb_array_length(i.doc#>'{params,images}') ELSE 0 END,
 i.created_at,coalesce(i.claimed_at,i.created_at),coalesce(i.expired_at,i.created_at),i.blocked_reason,coalesce(w.phase,'')
FROM i LEFT JOIN conversation_switches w ON w.logical_machine=i.logical_machine AND w.logical_session=i.logical_session AND w.phase IN ('preparing','prepared','active','in_doubt')
ORDER BY i.logical_machine,i.logical_session,i.position LIMIT $1`, limit, conversationUndeliveredTextMax)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ConversationUndeliveredInput, error) {
		var u ConversationUndeliveredInput
		var phase string
		err := row.Scan(&u.LogicalMachine, &u.LogicalSession, &u.Input.ClientMsgID, &u.Input.Position, &u.Input.State, &u.Text, &u.Images, &u.CreatedAt, &u.Input.ClaimedAt, &u.ExpiredAt, &u.Input.BlockedReason, &phase)
		u.SwitchPending = phase != "" && phase != "active"
		return u, err
	})
}

// ConversationInputOwners lists the logical machines whose conversation session
// holds input id. Owner actions name only the conversation and the message.
func (d *DB) ConversationInputOwners(ctx context.Context, session, id string) ([]string, error) {
	rows, err := d.pool.Query(ctx, `SELECT i.logical_machine FROM conversation_inputs i JOIN logical_conversations c ON c.machine_id=i.logical_machine AND c.session_id=i.logical_session AND c.deleted_at IS NULL WHERE i.logical_session=$1 AND i.client_msg_id=$2`, session, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ResolveExpiredConversationInput applies the owner's choice to an expired input:
// retry requeues the original payload at the queue's tail (once); otherwise it is
// dismissed, never sent, its payload kept only in the audit. Only an expired row changes (CAS on its state and attempt
// version under the root lock). Repeating an action that already took effect
// succeeds with changed=false, so a double tap sends nothing twice; any other
// state (still waiting, delivered, the other action taken) is ErrConversationEpoch.
func (d *DB) ResolveExpiredConversationInput(ctx context.Context, machine, session, id, actor string, retry bool) (changed bool, err error) {
	if id == "" || actor == "" {
		return false, errors.New("invalid owner input action")
	}
	action := "dismiss"
	if retry {
		action = "retry"
	}
	err = conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		var epoch int64
		if err := tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 AND deleted_at IS NULL FOR UPDATE NOWAIT`, machine, session).Scan(&epoch); err != nil {
			return err
		}
		var input ConversationInput
		err := tx.QueryRow(ctx, `SELECT client_msg_id,position,epoch,state,dispatch_segment,dispatch_machine,dispatch_session,dispatch_epoch,delivery_version FROM conversation_inputs WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3 FOR UPDATE NOWAIT`, machine, session, id).
			Scan(&input.ClientMsgID, &input.Position, &input.Epoch, &input.State, &input.DispatchSegment, &input.DispatchMachine, &input.DispatchSession, &input.DispatchEpoch, &input.Version)
		if err != nil {
			return err
		}
		if input.State != ConversationInputExpired {
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_input_resolutions WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3 AND action=$4)`, machine, session, id, action).Scan(&taken); err != nil {
				return err
			}
			done := !retry && input.State == ConversationInputDismissed ||
				retry && taken && input.State != ConversationInputDismissed
			if done {
				return nil
			}
			return ErrConversationEpoch
		}
		// One audit row per expired attempt and action: the version names the attempt.
		p := ConversationInputResolution{RequestID: fmt.Sprintf("owner-%s:%s:%d", action, id, input.Version), ExpectedEpoch: epoch, Input: input, Action: action, Reason: "owner chose " + action + " for an input that expired unconfirmed"}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if err := recordConversationInputResolution(ctx, tx, machine, session, actor, p, raw); err != nil {
			return err
		}
		if retry {
			// Tail, not the original position: later input already went in, and the
			// owner sends it again now. The next claim records a new dispatch identity.
			_, err = tx.Exec(ctx, `UPDATE conversation_inputs SET state='queued',position=DEFAULT,epoch=$4,created_at=now(),claimed_at=NULL,expired_at=NULL,blocked_reason='',last_reconciled_at=NULL,dispatch_segment='',dispatch_machine='',dispatch_session='',dispatch_epoch=0,delivery_version=delivery_version+1 WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3`, machine, session, id, epoch)
		} else {
			_, err = tx.Exec(ctx, `UPDATE conversation_inputs SET state='dismissed',payload='{}'::bytea,delivery_version=delivery_version+1 WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3`, machine, session, id)
		}
		changed = err == nil
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrConversationEpoch
	}
	return changed && err == nil, err
}
