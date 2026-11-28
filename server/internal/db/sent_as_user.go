package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// The ledger of what was written as the user (migrations/0420_sent_as_user.sql, docs/extensions/connectors.md "Sent as the user").

// SentAsUser is one write that went out through a connector's gate (connectorwire.SentAsUser, which the knowledge-ui
// module reads too).
type SentAsUser = connectorwire.SentAsUser

// How many rows one read of the ledger gives: by default, and at most.
const (
	SentAsUserDefault = 50
	SentAsUserMost    = 200
)

// RecordSentAsUser adds a row (At: now when zero) and returns its id. An empty JSON field is an empty object.
func (d *DB) RecordSentAsUser(ctx context.Context, s SentAsUser) (int64, error) {
	if s.At.IsZero() {
		s.At = time.Now()
	}
	var id int64
	err := d.pool.QueryRow(ctx, `INSERT INTO sent_as_user (at, connector, action, target, text, summary, origin, let_through, message_ref)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		s.At, s.Connector, s.Action, objectOrEmpty(s.Target), s.Text, s.Summary, objectOrEmpty(s.Origin), objectOrEmpty(s.LetThrough),
		s.MessageRef).Scan(&id)
	return id, err
}

// SetSentAsUserOutcome completes a durable, pre-send audit attempt. A pending
// attempt after a crash deliberately says nothing about whether delivery occurred.
func (d *DB) SetSentAsUserOutcome(ctx context.Context, id int64, outcome string) error {
	_, err := d.pool.Exec(ctx, `UPDATE sent_as_user SET let_through = let_through || jsonb_build_object('outcome', $2::text) WHERE id = $1`, id, outcome)
	return err
}

func objectOrEmpty(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte(`{}`)
	}
	return raw
}

// sentAsUserColumns are a row's columns in the order scanSentAsUser reads them.
const sentAsUserColumns = `id, at, connector, action, target, text, summary, origin, let_through, message_ref, recalled_at`

func scanSentAsUser(r pgx.Row) (SentAsUser, error) {
	var s SentAsUser
	var target, origin, through []byte
	err := r.Scan(&s.ID, &s.At, &s.Connector, &s.Action, &target, &s.Text, &s.Summary, &origin, &through, &s.MessageRef, &s.RecalledAt)
	s.Target, s.Origin, s.LetThrough = target, origin, through
	return s, err
}

// SentAsUsers are the ledger's rows newest first (by id): those before the id before (0: from the newest), at most
// limit (SentAsUserDefault when below 1, at most SentAsUserMost), of connector when it is set. next is the id to ask
// for the page after this one (before = next), 0 when this is the last.
func (d *DB) SentAsUsers(ctx context.Context, before int64, limit int, connector string) (rows []SentAsUser, next int64, err error) {
	if limit < 1 {
		limit = SentAsUserDefault
	}
	limit = min(limit, SentAsUserMost)
	res, err := d.pool.Query(ctx, `SELECT `+sentAsUserColumns+`
		FROM sent_as_user WHERE ($1::bigint = 0 OR id < $1::bigint) AND ($2 = '' OR connector = $2) ORDER BY id DESC LIMIT $3`,
		before, connector, limit+1)
	if err != nil {
		return nil, 0, err
	}
	rows, err = pgx.CollectRows(res, func(r pgx.CollectableRow) (SentAsUser, error) { return scanSentAsUser(r) })
	if err != nil {
		return nil, 0, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].ID
	}
	return rows, next, nil
}

const sentAsUsersForArchiveSQL = `SELECT ` + sentAsUserColumns + ` FROM sent_as_user
	WHERE connector = $1 AND at >= $2 AND at < $3 ORDER BY at, id`

// SentAsUsersForArchive reads one connector's ledger in [since, until), oldest first.
// Archive matching needs every candidate, including recalled messages still present in the archive;
// the settings page's pagination limit does not apply here.
func (d *DB) SentAsUsersForArchive(ctx context.Context, connector string, since, until time.Time) ([]SentAsUser, error) {
	rows, err := d.pool.Query(ctx, sentAsUsersForArchiveSQL, connector, since, until)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SentAsUser, error) { return scanSentAsUser(r) })
}

// PruneSentAsUser deletes every row written before before and says how many there were.
func (d *DB) PruneSentAsUser(ctx context.Context, before time.Time) (int64, error) {
	tag, err := d.pool.Exec(ctx, `DELETE FROM sent_as_user WHERE at < $1`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// SentAsUserByID is the row with the id id; ok is false when there is none.
func (d *DB) SentAsUserByID(ctx context.Context, id int64) (row SentAsUser, ok bool, err error) {
	row, err = scanSentAsUser(d.pool.QueryRow(ctx, `SELECT `+sentAsUserColumns+` FROM sent_as_user WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SentAsUser{}, false, nil
	}
	return row, err == nil, err
}

// MarkSentAsUserRecalled sets recalled_at of the row with the id id to at and says whether it did: not when the row is
// gone, or was marked before (the first time stays, whoever asks again).
func (d *DB) MarkSentAsUserRecalled(ctx context.Context, id int64, at time.Time) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE sent_as_user SET recalled_at = $2 WHERE id = $1 AND recalled_at IS NULL`, id, at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
