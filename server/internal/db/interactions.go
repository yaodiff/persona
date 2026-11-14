package db

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- interactions ----

func (d *DB) InsertInteraction(ctx context.Context, ix proto.Interaction) error {
	payload, err := json.Marshal(ix)
	if err != nil {
		return err
	}
	_, err = d.pool.Exec(ctx, `INSERT INTO interactions (id, machine_id, session_id, kind, payload, created_at)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (id) DO NOTHING`,
		ix.ID, ix.MachineID, ix.SessionID, ix.Kind, payload, ix.CreatedAt)
	return err
}

// MarkInteractionResumable records that pending interaction id outlives a restart from now on (its payload's
// resumable, which ExpireStaleInteractions reads): an ask_user question that let go of its turn and is now waited on by
// no call (assistant/ask_release.go, #7392). false when id is not pending.
func (d *DB) MarkInteractionResumable(ctx context.Context, id string) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE interactions SET payload = jsonb_set(payload, '{resumable}', 'true'::jsonb)
		WHERE id = $1 AND status = 'pending'`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// FinishInteraction records how an interaction ended; only the first outcome counts.
func (d *DB) FinishInteraction(ctx context.Context, id, status string, res *proto.Resolution) error {
	var raw []byte
	if res != nil {
		raw, _ = json.Marshal(res)
	}
	_, err := d.pool.Exec(ctx, `UPDATE interactions SET status = $2, resolution = $3, resolved_at = now()
		WHERE id = $1 AND status = 'pending'`, id, status, raw)
	return err
}

// LateAnswer records on expired card id an answer that came after it expired (proto.Resolution.Late): answers (by
// question) are added to those recorded late before, and by / reason (proto.Resolution.AnsweredBy, Reason) say who
// answered. The card stays "expired"; resolved_at becomes when the answer came. A card that did not expire is left as
// it is (false).
func (d *DB) LateAnswer(ctx context.Context, id string, answers map[string]string, by, reason string) (bool, error) {
	add, err := json.Marshal(answers)
	if err != nil {
		return false, err
	}
	who := map[string]string{}
	if by != "" {
		who["answered_by"] = by
	}
	if reason != "" {
		who["reason"] = reason
	}
	extra, err := json.Marshal(who)
	if err != nil {
		return false, err
	}
	tag, err := d.pool.Exec(ctx, `UPDATE interactions SET resolved_at = now(), resolution = jsonb_build_object('late', true,
			'answers', COALESCE(CASE WHEN resolution->>'late' = 'true' THEN resolution->'answers' END, '{}'::jsonb) || $2::jsonb) || $3::jsonb
		WHERE id = $1 AND status = 'expired'`, id, add, extra)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// InteractionRecord is a past interaction with its outcome.
type InteractionRecord struct {
	Interaction proto.Interaction `json:"interaction"`
	Status      string            `json:"status"`
	Resolution  *proto.Resolution `json:"resolution,omitempty"`
	ResolvedAt  *time.Time        `json:"resolved_at,omitempty"`
}

// The statements of Interactions, one per combination of filters. The single statement they replace matched an
// empty filter with an OR ("$1 = empty OR machine_id = $1"). Once Postgres switched that cached statement to a generic
// plan (after five runs) it could not fold the empty test away, so it walked interactions_created over every row ever
// kept. TestInteractionsHistoryIndexed checks the plans of the cached ones.
const (
	interactionsSQL        = `SELECT payload, status, resolution, resolved_at FROM interactions ORDER BY created_at DESC LIMIT $1`
	interactionsMachineSQL = `SELECT payload, status, resolution, resolved_at FROM interactions
		WHERE machine_id = $1 ORDER BY created_at DESC LIMIT $2`
	interactionsSessionSQL = `SELECT payload, status, resolution, resolved_at FROM interactions
		WHERE session_id = $1 ORDER BY created_at DESC LIMIT $2`
	interactionsMachineSessionSQL = `SELECT payload, status, resolution, resolved_at FROM interactions
		WHERE machine_id = $1 AND session_id = $2 ORDER BY created_at DESC LIMIT $3`
)

// Interactions lists recent interactions, newest first, optionally for one machine and/or one session.
func (d *DB) Interactions(ctx context.Context, machine, session string, limit int) ([]InteractionRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// A machine alone or a session alone has no index that orders by created_at: the best plan walks
	// interactions_created for a busy one and reads interactions_session then sorts for a quiet one. A generic plan
	// settles on the walk, which reads every row ever kept for a quiet one, so these two go as the unnamed statement
	// of QueryExecModeExec, planned for each call with its values. TestInteractionsNotCached checks that.
	var q string
	var args []any
	switch {
	case machine != "" && session != "":
		q, args = interactionsMachineSessionSQL, []any{machine, session, limit}
	case machine != "":
		q, args = interactionsMachineSQL, []any{pgx.QueryExecModeExec, machine, limit}
	case session != "":
		q, args = interactionsSessionSQL, []any{pgx.QueryExecModeExec, session, limit}
	default:
		q, args = interactionsSQL, []any{limit}
	}
	rows, err := d.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (InteractionRecord, error) {
		var rec InteractionRecord
		var payload, res []byte
		if err := r.Scan(&payload, &rec.Status, &res, &rec.ResolvedAt); err != nil {
			return rec, err
		}
		if err := json.Unmarshal(payload, &rec.Interaction); err != nil {
			return rec, err
		}
		if len(res) > 0 {
			rec.Resolution = &proto.Resolution{}
			if err := json.Unmarshal(res, rec.Resolution); err != nil {
				return rec, err
			}
		}
		return rec, nil
	})
}

// InteractionQuery pages the interaction history by time (GET /api/v1/interactions/history with since, before or
// kind): newest first, created at or after Since, before the cursor (Before, BeforeID) and of Kind, each when set.
type InteractionQuery struct {
	Machine, Session, Kind string
	Since                  time.Time
	// Before is the cursor of the page before: rows created before it, or at it with an id before BeforeID.
	Before   time.Time
	BeforeID string
	Limit    int // 1…500; another value is 100
}

// InteractionPage is a page of the history and, when it was full, the cursor of the next older one.
type InteractionPage struct {
	Records      []InteractionRecord
	NextBefore   *time.Time
	NextBeforeID string
}

// InteractionsPage is Interactions with a time range, a kind and a cursor. Its statement is built for the filters
// given and planned for each call with its values (QueryExecModeExec): the history is read rarely, and a generic plan
// over optional filters walks every row ever kept (see Interactions). The cursor's created_at bound is written twice,
// once alone, so the walk down interactions_created starts at it rather than at the newest row.
func (d *DB) InteractionsPage(ctx context.Context, q InteractionQuery) (InteractionPage, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	var where []string
	args := []any{pgx.QueryExecModeExec}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args)-1)
	}
	if q.Machine != "" {
		where = append(where, "machine_id = "+arg(q.Machine))
	}
	if q.Session != "" {
		where = append(where, "session_id = "+arg(q.Session))
	}
	if q.Kind != "" {
		where = append(where, "kind = "+arg(q.Kind))
	}
	if !q.Since.IsZero() {
		where = append(where, "created_at >= "+arg(q.Since))
	}
	if !q.Before.IsZero() {
		b := arg(q.Before)
		if q.BeforeID == "" {
			where = append(where, "created_at < "+b)
		} else {
			where = append(where, "created_at <= "+b, "(created_at, id) < ("+b+", "+arg(q.BeforeID)+")")
		}
	}
	sql := `SELECT payload, status, resolution, resolved_at, created_at, id FROM interactions`
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += " ORDER BY created_at DESC, id DESC LIMIT " + arg(q.Limit)
	rows, err := d.pool.Query(ctx, sql, args...)
	if err != nil {
		return InteractionPage{}, err
	}
	var page InteractionPage
	var lastAt time.Time
	var lastID string
	page.Records, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (InteractionRecord, error) {
		var rec InteractionRecord
		var payload, res []byte
		if err := r.Scan(&payload, &rec.Status, &res, &rec.ResolvedAt, &lastAt, &lastID); err != nil {
			return rec, err
		}
		if err := json.Unmarshal(payload, &rec.Interaction); err != nil {
			return rec, err
		}
		if len(res) > 0 {
			rec.Resolution = &proto.Resolution{}
			if err := json.Unmarshal(res, rec.Resolution); err != nil {
				return rec, err
			}
		}
		return rec, nil
	})
	if err != nil {
		return InteractionPage{}, err
	}
	if len(page.Records) == q.Limit {
		page.NextBefore, page.NextBeforeID = &lastAt, lastID
	}
	return page, nil
}

// InteractionRecord is one interaction with its outcome; nil when there is no such row.
func (d *DB) InteractionRecord(ctx context.Context, id string) (*InteractionRecord, error) {
	var rec InteractionRecord
	var payload, res []byte
	err := d.pool.QueryRow(ctx, `SELECT payload, status, resolution, resolved_at FROM interactions WHERE id = $1`, id).
		Scan(&payload, &rec.Status, &res, &rec.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload, &rec.Interaction); err != nil {
		return nil, err
	}
	if len(res) > 0 {
		rec.Resolution = &proto.Resolution{}
		if err := json.Unmarshal(res, rec.Resolution); err != nil {
			return nil, err
		}
	}
	return &rec, nil
}

// The startup statements of ExpireStaleInteractions. Both must stay answerable from the interactions_pending
// partial index (migration 0393); TestPendingInteractionsIndexed checks the plans of these exact constants.
const (
	resumablePendingSQL = `SELECT payload FROM interactions WHERE status = 'pending'
		AND (payload->>'resumable')::boolean IS TRUE AND (payload->>'expires_at')::timestamptz > now()`
	cancelPendingSQL = `UPDATE interactions SET status = 'cancelled', resolved_at = now()
		WHERE status = 'pending' AND NOT (id = ANY($1))
		AND id NOT IN (SELECT interaction_id FROM session_work WHERE interaction_id <> '')
		AND id NOT IN (SELECT protected.id FROM settings,
			jsonb_array_elements(CASE WHEN jsonb_typeof(value) = 'array' THEN value ELSE '[]'::jsonb END) AS item,
			LATERAL unnest(ARRAY[item->'card'->>'id', item->>'expired_id']) AS protected(id)
			WHERE key = 'agents.tool_loops' AND protected.id IS NOT NULL)
		RETURNING id`
)

// ExpireStaleInteractions marks interactions left pending by a previous hub process cancelled, and returns the
// resumable ones not yet expired instead: their hooks reconnect and claim them again. The manager's cards about a
// turn (session_work's interaction_id) and loop cards in the manager's settings stay pending too. The manager restores
// them under the same id and ends cards it no longer needs. cancelled are the ids it marked: the apps may still show
// them (#4568).
func (d *DB) ExpireStaleInteractions(ctx context.Context) (kept []proto.Interaction, cancelled []string, err error) {
	rows, err := d.pool.Query(ctx, resumablePendingSQL)
	if err != nil {
		return nil, nil, err
	}
	kept, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (proto.Interaction, error) {
		var payload []byte
		var ix proto.Interaction
		if err := r.Scan(&payload); err != nil {
			return ix, err
		}
		return ix, json.Unmarshal(payload, &ix)
	})
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, len(kept))
	for i, ix := range kept {
		ids[i] = ix.ID
	}
	rows, err = d.pool.Query(ctx, cancelPendingSQL, ids)
	if err != nil {
		return nil, nil, err
	}
	cancelled, err = pgx.CollectRows(rows, pgx.RowTo[string])
	return kept, cancelled, err
}

// InteractionOutcome returns an interaction's status and resolution; an empty status when there is no such row.
func (d *DB) InteractionOutcome(ctx context.Context, id string) (string, *proto.Resolution, error) {
	var status string
	var raw []byte
	err := d.pool.QueryRow(ctx, `SELECT status, resolution FROM interactions WHERE id = $1`, id).Scan(&status, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil || len(raw) == 0 {
		return status, nil, err
	}
	var res proto.Resolution
	return status, &res, json.Unmarshal(raw, &res)
}
