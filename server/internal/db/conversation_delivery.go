package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func requireNoUnresolvedConversationInput(ctx context.Context, tx pgx.Tx, machine, session string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT client_msg_id FROM conversation_inputs WHERE logical_machine=$1 AND logical_session=$2 AND state IN ('sending','in_doubt') ORDER BY position LIMIT 1`, machine, session).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: blocked_input %s requires delivery reconciliation", ErrConversationSwitchBusy, id)
}

// Also repairs rows claimed by an old binary during a rolling upgrade. This uses
// committed historical activation evidence only, never the current active target.
func (d *DB) ConversationUnresolvedInput(ctx context.Context, machine, session string) (*ConversationInput, error) {
	_, err := d.pool.Exec(ctx, `UPDATE conversation_inputs i SET dispatch_segment=s.id,dispatch_machine=s.machine_id,dispatch_session=s.session_id,dispatch_epoch=i.epoch
 FROM conversation_segments s WHERE i.logical_machine=$1 AND i.logical_session=$2 AND i.state IN ('sending','in_doubt') AND i.dispatch_epoch=0
 AND s.logical_machine=i.logical_machine AND s.logical_session=i.logical_session
 AND ((i.epoch=1 AND s.ordinal=1) OR EXISTS(SELECT 1 FROM conversation_switches w WHERE w.logical_machine=i.logical_machine AND w.logical_session=i.logical_session AND w.expected_epoch+1=i.epoch AND w.target_segment=s.id AND w.phase IN ('active','accepted')))`, machine, session)
	if err != nil {
		return nil, err
	}
	var row ConversationInput
	var raw []byte
	err = d.pool.QueryRow(ctx, `SELECT client_msg_id,position,epoch,payload,state,dispatch_segment,dispatch_machine,dispatch_session,dispatch_epoch,delivery_version,blocked_reason,last_reconciled_at,coalesce(claimed_at,created_at) FROM conversation_inputs WHERE logical_machine=$1 AND logical_session=$2 AND state IN ('sending','in_doubt') ORDER BY position LIMIT 1`, machine, session).
		Scan(&row.ClientMsgID, &row.Position, &row.Epoch, &raw, &row.State, &row.DispatchSegment, &row.DispatchMachine, &row.DispatchSession, &row.DispatchEpoch, &row.Version, &row.BlockedReason, &row.LastReconciledAt, &row.ClaimedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	row.Payload = json.RawMessage(raw)
	if row.BlockedReason == "" {
		row.BlockedReason = "awaiting original dispatch receipt; automatic replay disabled"
	}
	return &row, err
}

// Root epoch and attempt identity are separate fences. A stale reconciler cannot
// release a newer queue after activation, nor can a receipt settle a replay attempt.
func lockConversationDelivery(ctx context.Context, tx pgx.Tx, machine, session string, epoch int64, input ConversationInput) error {
	var current int64
	if err := tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 AND deleted_at IS NULL FOR UPDATE NOWAIT`, machine, session).Scan(&current); err != nil {
		return err
	}
	if current != epoch {
		return ErrConversationEpoch
	}
	var matches bool
	if err := tx.QueryRow(ctx, `SELECT position=$4 AND epoch=$5 AND state=$6 AND dispatch_segment=$7 AND dispatch_machine=$8 AND dispatch_session=$9 AND dispatch_epoch=$10 AND delivery_version=$11 FROM conversation_inputs WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3 FOR UPDATE NOWAIT`, machine, session, input.ClientMsgID, input.Position, input.Epoch, input.State, input.DispatchSegment, input.DispatchMachine, input.DispatchSession, input.DispatchEpoch, input.Version).Scan(&matches); err != nil {
		return err
	}
	if !matches || input.State != "sending" && input.State != "in_doubt" {
		return ErrConversationEpoch
	}
	return nil
}

func (d *DB) NoteConversationInputBlocked(ctx context.Context, machine, session string, epoch int64, input ConversationInput, reason string) error {
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		if err := lockConversationDelivery(ctx, tx, machine, session, epoch, input); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE conversation_inputs SET blocked_reason=$4,last_reconciled_at=now() WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3`, machine, session, input.ClientMsgID, reason)
		return err
	})
}

// Historical settlement never resolves the current switch, clears its handoff,
// changes its target, or requeues another input. Positive and definitive negative receipts
// close this attempt; a definite refusal requeues the same message in place.
// Missing receipts remain ambiguous and never authorize automatic replay.
func (d *DB) ResolveHistoricalConversationDelivery(ctx context.Context, machine, session string, epoch int64, input ConversationInput, outcome string) error {
	if outcome != "accepted" && outcome != "rejected" {
		return errors.New("invalid historical delivery outcome")
	}
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		if err := lockConversationDelivery(ctx, tx, machine, session, epoch, input); err != nil {
			return err
		}
		if input.DispatchEpoch <= 0 || input.DispatchEpoch >= epoch || input.DispatchSegment == "" {
			return ErrConversationEpoch
		}
		if outcome == "rejected" {
			// Definite non-delivery can retry safely. Preserve the old attempt and
			// payload in audit before the next claim records its new target.
			key := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", input.ClientMsgID, input.DispatchEpoch, input.Version)))
			p := ConversationInputResolution{RequestID: fmt.Sprintf("receipt-requeue:%x", key), ExpectedEpoch: epoch, Input: input, Action: "replay", Reason: "original dispatch explicitly refused input; retry original FIFO position"}
			raw, err := json.Marshal(p)
			if err != nil {
				return err
			}
			if err := recordConversationInputResolution(ctx, tx, machine, session, "runtime_receipt", p, raw); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE conversation_inputs SET state='queued',delivery_version=delivery_version+1,dispatch_segment='',dispatch_machine='',dispatch_session='',dispatch_epoch=0,blocked_reason='',last_reconciled_at=now() WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3`, machine, session, input.ClientMsgID)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE conversation_inputs SET state=$4,delivery_version=delivery_version+1,blocked_reason='',last_reconciled_at=now() WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3`, machine, session, input.ClientMsgID, outcome); err != nil {
			return err
		}
		if outcome == "accepted" {
			return consumeHistoricalInputHandoff(ctx, tx, machine, session, input)
		}
		return nil
	})
}

// Only the original idle-accepted activation's handoff was carried by this
// historical input. A future rollback must not attach that old handoff again.
func consumeHistoricalInputHandoff(ctx context.Context, tx pgx.Tx, machine, session string, input ConversationInput) error {
	_, err := tx.Exec(ctx, `UPDATE conversation_switches SET handoff=NULL WHERE logical_machine=$1 AND logical_session=$2 AND phase='accepted' AND target_segment=$3 AND expected_epoch+1=$4`, machine, session, input.DispatchSegment, input.DispatchEpoch)
	return err
}

type ConversationInputResolution struct {
	RequestID     string            `json:"request_id"`
	ExpectedEpoch int64             `json:"expected_epoch"`
	Input         ConversationInput `json:"input"`
	Action        string            `json:"action"`
	Reason        string            `json:"reason"`
}

// ResolveConversationInputManually is the only ambiguous replay entry point.
// The caller must authenticate a full-role human, supply the diagnostic CAS
// tuple and a unique request id. The original queue position and payload survive.
func (d *DB) ResolveConversationInputManually(ctx context.Context, machine, session, actor string, p ConversationInputResolution) error {
	if p.RequestID == "" || len(p.RequestID) > 128 || actor == "" || p.Reason == "" || len(p.Reason) > 4096 || p.Input.ClientMsgID == "" || p.Action != "accepted" && p.Action != "rejected" && p.Action != "replay" {
		return errors.New("invalid manual input resolution")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		// Serialize idempotency with this root, even after the input settles.
		var epoch int64
		if err := tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 AND deleted_at IS NULL FOR UPDATE NOWAIT`, machine, session).Scan(&epoch); err != nil {
			return err
		}
		var old []byte
		var by string
		err := tx.QueryRow(ctx, `SELECT request,actor FROM conversation_input_resolutions WHERE logical_machine=$1 AND logical_session=$2 AND request_id=$3`, machine, session, p.RequestID).Scan(&old, &by)
		if err == nil {
			if !bytes.Equal(old, raw) || by != actor {
				return ErrConversationEpoch
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := lockConversationDelivery(ctx, tx, machine, session, p.ExpectedEpoch, p.Input); err != nil {
			return err
		}
		if err := recordConversationInputResolution(ctx, tx, machine, session, actor, p, raw); err != nil {
			return err
		}
		state := p.Action
		if state == "replay" {
			state = "queued"
		}
		if _, err := tx.Exec(ctx, `UPDATE conversation_inputs SET state=$4,delivery_version=delivery_version+1,blocked_reason='',last_reconciled_at=now(),dispatch_segment=CASE WHEN $4='queued' THEN '' ELSE dispatch_segment END,dispatch_machine=CASE WHEN $4='queued' THEN '' ELSE dispatch_machine END,dispatch_session=CASE WHEN $4='queued' THEN '' ELSE dispatch_session END,dispatch_epoch=CASE WHEN $4='queued' THEN 0 ELSE dispatch_epoch END WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3`, machine, session, p.Input.ClientMsgID, state); err != nil {
			return err
		}
		if p.Action == "accepted" && p.Input.DispatchEpoch > 0 && p.Input.DispatchEpoch < epoch {
			return consumeHistoricalInputHandoff(ctx, tx, machine, session, p.Input)
		}
		// A manual acceptance can confirm the current switch only for its own
		// first dispatch. Historical settlement cannot consume current handoff.
		if (p.Action == "accepted" || p.Action == "rejected") && p.Input.DispatchEpoch == epoch {
			var operation string
			err := tx.QueryRow(ctx, `SELECT operation_id FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND phase='active' AND expected_epoch+1=$3 AND target_segment=$4`, machine, session, epoch, p.Input.DispatchSegment).Scan(&operation)
			if err == nil {
				return resolveConversationSwitch(ctx, tx, machine, session, operation, p.Action, "manual input settlement: "+p.Reason, switchSettlement{manual: true})
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if p.Action == "accepted" {
				_, err = tx.Exec(ctx, `UPDATE conversation_switches SET handoff=NULL WHERE logical_machine=$1 AND logical_session=$2 AND phase='accepted' AND target_segment=$3`, machine, session, p.Input.DispatchSegment)
				return err
			}
		}
		return nil
	})
}

func recordConversationInputResolution(ctx context.Context, tx pgx.Tx, machine, session, actor string, p ConversationInputResolution, raw []byte) error {
	_, err := tx.Exec(ctx, `INSERT INTO conversation_input_resolutions(logical_machine,logical_session,request_id,client_msg_id,actor,reason,action,request,original_payload,payload_hash,dispatch_segment,dispatch_machine,dispatch_session,dispatch_epoch)
 SELECT logical_machine,logical_session,$4,client_msg_id,$5,$6,$7,$8,payload,payload_hash,dispatch_segment,dispatch_machine,dispatch_session,dispatch_epoch FROM conversation_inputs WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3`, machine, session, p.Input.ClientMsgID, p.RequestID, actor, p.Reason, p.Action, raw)
	return err
}

// ConversationInputClaimCurrent is checked while WithConversationEpoch holds the
// root SHARE lock through the RPC. Claim, settlement and operator replay all need
// the exclusive root lock, so an attempt cannot change between this check/send.
func (d *DB) ConversationInputClaimCurrent(ctx context.Context, machine, session string, epoch int64, input ConversationInput) (bool, error) {
	var current bool
	err := d.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_inputs i JOIN logical_conversations c ON c.machine_id=i.logical_machine AND c.session_id=i.logical_session WHERE i.logical_machine=$1 AND i.logical_session=$2 AND i.client_msg_id=$3 AND c.deleted_at IS NULL AND c.epoch=$4 AND i.epoch=$5 AND i.position=$6 AND i.dispatch_segment=$7 AND i.dispatch_machine=$8 AND i.dispatch_session=$9 AND i.dispatch_epoch=$10 AND i.delivery_version=$11 AND i.state='sending')`, machine, session, input.ClientMsgID, epoch, input.Epoch, input.Position, input.DispatchSegment, input.DispatchMachine, input.DispatchSession, input.DispatchEpoch, input.Version).Scan(&current)
	return current, err
}
