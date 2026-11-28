package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/proto"
)

// The persisted states of a send receipt, shared by the hub and manager.
const (
	SendStateSending   = "sending"
	SendStateDelivered = "delivered"
	SendStateUnknown   = "unknown"
	SendStateLater     = "later"
)

// ClaimSend records a message about to be sent. A client_msg_id seen before returns that send's state instead
// (sending, delivered, unknown, or later). The empty string means this one is new and now recorded as sending.
func (d *DB) ClaimSend(ctx context.Context, id, machine, session string) (string, error) {
	// A restored outbox can retry much later. Expiring an unacknowledged operation makes an old retry execute again.
	tag, err := d.pool.Exec(ctx, `INSERT INTO sent_messages (client_msg_id, machine_id, session_id, state)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, id, machine, session, SendStateSending)
	if err != nil || tag.RowsAffected() == 1 {
		return "", err
	}
	var state string
	err = d.pool.QueryRow(ctx, `SELECT state FROM sent_messages WHERE client_msg_id = $1`, id).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) { // forgotten in between (SettleSend "": nothing was sent)
		return d.ClaimSend(ctx, id, machine, session)
	}
	return state, err
}

// SettleSend records how a send ended; "" forgets it (nothing was typed: a retry may send it).
func (d *DB) SettleSend(ctx context.Context, id, state string) error {
	if state == "" {
		_, err := d.pool.Exec(ctx, `DELETE FROM sent_messages WHERE client_msg_id = $1`, id)
		return err
	}
	_, err := d.pool.Exec(ctx, `UPDATE sent_messages SET state = $2 WHERE client_msg_id = $1`, id, state)
	return err
}

// RecordEcho keeps the node's record of where a sent message landed (node/echo.go). A record of a found line is not
// replaced by one that still expects it (two notices can arrive out of order). A2A receipts can create their own
// records because the Manager sends them outside the app ledger.
func (d *DB) RecordEcho(ctx context.Context, id, machine string, echo json.RawMessage) error {
	if strings.HasPrefix(id, proto.A2ADeliveryPrefix) {
		var receipt struct {
			Session string               `json:"session_id"`
			Source  *proto.MessageSource `json:"source"`
		}
		if json.Unmarshal(echo, &receipt) == nil && receipt.Session != "" && receipt.Source != nil && receipt.Source.Kind == proto.SourceConversation {
			// Manager A2A deliveries skip the app send ledger. A trusted node's
			// receipt still has to be available when that node reconnects.
			_, err := d.pool.Exec(ctx, `INSERT INTO sent_messages (client_msg_id, machine_id, session_id, state, echo)
				VALUES ($1, $2, $3, $4, $5) ON CONFLICT (client_msg_id) DO UPDATE SET echo = EXCLUDED.echo
				WHERE sent_messages.machine_id = EXCLUDED.machine_id AND sent_messages.session_id = EXCLUDED.session_id
				AND (coalesce(sent_messages.echo->>'line', '') = '' OR coalesce(EXCLUDED.echo->>'line', '') <> '')`,
				id, machine, receipt.Session, SendStateUnknown, echo)
			return err
		}
	}
	_, err := d.pool.Exec(ctx, `UPDATE sent_messages SET echo = $3
		WHERE client_msg_id = $1 AND machine_id = $2
		AND (echo IS NULL OR echo->>'line' IS NULL OR echo->>'line' = '' OR coalesce($3::jsonb->>'line', '') <> '')`, id, machine, echo)
	return err
}

// Echoes are the machine's records from the last day, oldest first: what a node that started again is handed back.
func (d *DB) Echoes(ctx context.Context, machine string) ([]json.RawMessage, error) {
	rows, err := d.pool.Query(ctx, `SELECT echo FROM sent_messages
		WHERE machine_id = $1 AND echo IS NOT NULL AND created_at > now() - interval '1 day' ORDER BY created_at`, machine)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var e json.RawMessage
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
