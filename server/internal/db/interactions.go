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

// interactionOccurrenceTimeSQL preserves the full RFC3339Nano occurrence time
// retained in the payload. PostgreSQL's created_at column has only microseconds,
// which is not enough to distinguish two occurrences under a reused ID. The
// epoch-second and padded-fraction tuple also avoids nanosecond integer overflow.
func interactionOccurrenceTimeSQL(value string) string {
	return `(EXTRACT(EPOCH FROM DATE_TRUNC('second', (` + value + `)::timestamptz)), COALESCE(RPAD(SUBSTRING(` + value + ` FROM '\.([0-9]+)'), 9, '0'), '000000000'))`
}

// InsertInteraction records a pending occurrence. Replaying the same timestamp
// is idempotent, except that a non-resumable occurrence cancelled by startup
// cleanup (cancelled with no owner resolution) can be listed as pending again.
// A later timestamp is a genuinely reused runtime ID, so its new pending payload
// and occurrence replace the older terminal row. An older replay cannot overwrite
// the latest occurrence, and an owner-resolved occurrence never reopens.
func (d *DB) InsertInteraction(ctx context.Context, ix proto.Interaction) error {
	return d.insertInteraction(ctx, ix, true)
}

// InsertInteractionOccurrence publishes an occurrence without overwriting a
// different pending occurrence that concurrently won the same runtime ID. A
// later occurrence may replace terminal history, and the same cancelled
// non-resumable occurrence may reopen; callers read back to detect the winner.
func (d *DB) InsertInteractionOccurrence(ctx context.Context, ix proto.Interaction) error {
	return d.insertInteraction(ctx, ix, false)
}

func (d *DB) insertInteraction(ctx context.Context, ix proto.Interaction, replacePending bool) error {
	payload, err := json.Marshal(ix)
	if err != nil {
		return err
	}
	differentOccurrence := `(` + interactionOccurrenceTimeSQL("EXCLUDED.payload->>'created_at'") + ` > ` +
		interactionOccurrenceTimeSQL("interactions.payload->>'created_at'") + `)`
	if !replacePending {
		differentOccurrence = `(` + differentOccurrence + ` AND interactions.status <> 'pending')`
	}
	_, err = d.pool.Exec(ctx, `INSERT INTO interactions (id, machine_id, session_id, kind, payload, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET
			machine_id = EXCLUDED.machine_id,
			session_id = EXCLUDED.session_id,
			kind = EXCLUDED.kind,
			payload = EXCLUDED.payload,
			status = 'pending',
			resolution = NULL,
			ended_by = '',
			created_at = EXCLUDED.created_at,
			resolved_at = NULL,
			user_notified_at = NULL,
			auto_authorized_at = NULL,
			authorization_id = '',
			todo_id = ''
		WHERE (`+differentOccurrence+`) OR (
			`+interactionOccurrenceTimeSQL("EXCLUDED.payload->>'created_at'")+` = `+
		interactionOccurrenceTimeSQL("interactions.payload->>'created_at'")+`
			AND interactions.status = 'cancelled' AND interactions.resolution IS NULL
			AND NOT COALESCE((EXCLUDED.payload->>'resumable')::boolean, false)
			AND interactions.machine_id = EXCLUDED.machine_id
			AND interactions.session_id = EXCLUDED.session_id
		)`,
		ix.ID, ix.MachineID, ix.SessionID, ix.Kind, payload, ix.CreatedAt)
	return err
}

// UpdateInteractionDeadline publishes the effective deadline of the same pending card that was inserted at created.
// The creation-time guard prevents a resolved or replacement card from acquiring a deadline after this write was
// prepared. No decision, resolution, or execution state is changed.
func (d *DB) UpdateInteractionDeadline(ctx context.Context, id string, created, expires time.Time) error {
	raw, err := json.Marshal(expires.UTC())
	if err != nil {
		return err
	}
	tag, err := d.pool.Exec(ctx, `UPDATE interactions SET payload = jsonb_set(payload, '{expires_at}', $3::jsonb, false)
		WHERE id = $1 AND created_at = $2 AND status = 'pending'`, id, created, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("interaction deadline was not recorded")
	}
	return nil
}

// InsertResolvedInteraction writes an already-resolved decision in one statement, without ever creating a pending
// card. Unlike InsertInteraction's idempotent card publication, a duplicate id is an error: an audit record must not
// silently disappear. The decision and its resolved timestamp become queryable together.
func (d *DB) InsertResolvedInteraction(ctx context.Context, ix proto.Interaction, res *proto.Resolution) error {
	payload, err := json.Marshal(ix)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	resolvedAt := time.Now().UTC()
	_, err = d.pool.Exec(ctx, `INSERT INTO interactions
		(id, machine_id, session_id, kind, payload, created_at, status, resolution, resolved_at, ended_by)
		VALUES ($1, $2, $3, $4, $5, $6, 'resolved', $7, $8, $9)`,
		ix.ID, ix.MachineID, ix.SessionID, ix.Kind, payload, ix.CreatedAt, raw, resolvedAt, EndedBy(ctx))
	return err
}

// MarkResolvedInteractionOutcome records the result of an operation whose skipped-card decision was inserted before
// its write. A saved outcome also enters the existing automatic-authorization count. If this update fails, the original
// attempted outcome remains, which is deliberately an unknown result rather than a false success or failure.
func (d *DB) MarkResolvedInteractionOutcome(ctx context.Context, id string, created time.Time, outcome string, at time.Time) error {
	if outcome != "saved" && outcome != "not_saved" {
		return errors.New("unknown resolved-interaction outcome")
	}
	raw, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	tag, err := d.pool.Exec(ctx, `UPDATE interactions
		SET payload = jsonb_set(payload, '{tool_input,outcome}', $3::jsonb, false),
		auto_authorized_at = CASE WHEN $4 THEN COALESCE(auto_authorized_at, $5) ELSE auto_authorized_at END
		WHERE id = $1 AND created_at = $2 AND status = 'resolved'`,
		id, created, raw, outcome == "saved", at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("resolved-interaction outcome was not recorded")
	}
	return nil
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

// Who or what ended an interaction (interactions.ended_by, WithEndedBy). Only EndedByDevice is the user on one of their
// devices; the rest are nobody answering (expired, withdrawn, cleared) or someone else answering for them.
const (
	EndedByDevice    = "device"    // the user answered it on a device (the apps' POST /api/v1/interactions/{id}/resolve)
	EndedByExtension = "extension" // an extension answered it through the same route
	EndedByExpired   = "expired"   // its time ran out unanswered
	EndedByWithdrawn = "withdrawn" // the asker went away first (its call ended, its hook disconnected)
	EndedByCleared   = "cleared"   // a newer card replaced it, or it was cleared with the others (a restart)
	// EndedByAssistant, EndedByDefault and EndedByTerminal: answered for the user (proto.Resolution.AnsweredBy).
	EndedByAssistant  = proto.AnsweredByAssistant
	EndedByDefault    = proto.AnsweredByDefault
	EndedByTerminal   = proto.AnsweredByTerminal
	EndedByRemembered = "remembered" // the manager's kept "Always allow" answered it (proto.Resolution.Remembered)
)

type endedByKey struct{}

// WithEndedBy is ctx for a FinishInteraction that records by (one of the EndedBy* above) as who or what ended the card.
func WithEndedBy(ctx context.Context, by string) context.Context {
	return context.WithValue(ctx, endedByKey{}, by)
}

// EndedBy is what WithEndedBy put on ctx; "" when nothing did.
func EndedBy(ctx context.Context) string {
	by, _ := ctx.Value(endedByKey{}).(string)
	return by
}

// FinishInteraction records how an interaction ended, and who or what ended it when ctx says (WithEndedBy); only the
// first outcome counts.
func (d *DB) FinishInteraction(ctx context.Context, id, status string, res *proto.Resolution) error {
	var raw []byte
	if res != nil {
		raw, _ = json.Marshal(res)
	}
	_, err := d.pool.Exec(ctx, `UPDATE interactions SET status = $2, resolution = $3, resolved_at = now(), ended_by = $4
		WHERE id = $1 AND status = 'pending'`, id, status, raw, EndedBy(ctx))
	return err
}

// FinishInteractionOccurrence finalizes only the specified occurrence. It first
// inserts a missing pending row (needed after an earlier best-effort insert
// failed), then conditionally finishes that exact machine/session/timestamp and,
// for a detachable command, its exact call fields. matched is false when the ID
// now belongs to another occurrence; currentTerminal reports that other row's
// state so recovery never cancels it or forgets its own journal while it is
// still pending.
func (d *DB) FinishInteractionOccurrence(ctx context.Context, ix proto.Interaction, status string, res *proto.Resolution) (matched, currentTerminal bool, err error) {
	if err := d.InsertInteraction(ctx, ix); err != nil {
		return false, false, err
	}
	var raw []byte
	if res != nil {
		raw, _ = json.Marshal(res)
	}
	var input []byte
	if ix.Detachable {
		input, err = json.Marshal(ix.ToolInput)
		if err != nil {
			return false, false, err
		}
	}
	tag, err := d.pool.Exec(ctx, `UPDATE interactions SET status = $2, resolution = $3, resolved_at = now(), ended_by = $13
		WHERE id = $1 AND status = 'pending' AND machine_id = $4 AND session_id = $5 AND created_at = $6
		AND COALESCE((payload->>'detachable')::boolean, false) = $7
		AND (NOT $7 OR (kind = $8 AND COALESCE(payload->>'tool_name', '') = $9
			AND COALESCE(payload->>'execution_cwd', '') = $10
			AND COALESCE(payload->'tool_input', 'null'::jsonb) = COALESCE($11::jsonb, 'null'::jsonb)))
		AND `+interactionOccurrenceTimeSQL("interactions.payload->>'created_at'")+` = `+
		interactionOccurrenceTimeSQL("$12::text"),
		ix.ID, status, raw, ix.MachineID, ix.SessionID, ix.CreatedAt, ix.Detachable, ix.Kind, ix.ToolName, ix.ExecutionCwd, input,
		ix.CreatedAt.Format(time.RFC3339Nano), EndedBy(ctx))
	if err != nil {
		return false, false, err
	}
	if tag.RowsAffected() == 1 {
		return true, true, nil
	}
	rec, err := d.InteractionRecord(ctx, ix.ID)
	if err != nil {
		return false, false, err
	}
	if rec == nil {
		return false, false, errors.New("interaction occurrence is missing after its insert")
	}
	if !sameInteractionOccurrence(ix, rec.Interaction) {
		return false, rec.Status != "pending", nil
	}
	if rec.Status == "pending" {
		return false, false, errors.New("interaction occurrence remained pending after its terminal write")
	}
	if rec.Status != status {
		return false, true, errors.New("interaction occurrence already has a different terminal status")
	}
	return true, true, nil
}

func sameInteractionOccurrence(a, b proto.Interaction) bool {
	if a.ID != b.ID || a.MachineID != b.MachineID || a.SessionID != b.SessionID || !a.CreatedAt.Equal(b.CreatedAt) {
		return false
	}
	if a.Detachable || b.Detachable {
		return a.Detachable && b.Detachable && a.Kind == b.Kind && a.ToolName == b.ToolName &&
			a.ExecutionCwd == b.ExecutionCwd && sameJSONDocument(a.ToolInput, b.ToolInput)
	}
	return true
}

// sameJSONDocument compares JSON values using PostgreSQL JSONB's value
// semantics: object order, whitespace and equivalent numeric spellings are not
// identity. Invalid documents retain the exact-byte fallback and fail closed.
func sameJSONDocument(a, b json.RawMessage) bool {
	av, aerr := proto.CanonicalJSON(a)
	bv, berr := proto.CanonicalJSON(b)
	if aerr != nil || berr != nil {
		return string(a) == string(b)
	}
	return string(av) == string(bv)
}

// ApprovedSetting is the exact change a manager-owned permission card authorizes.
// Revision checks a versioned document. Otherwise Before checks a scalar value,
// with Default supplying its effective value before the first write.
type ApprovedSetting struct {
	Key      string
	Revision *int64
	Before   string
	Default  string
	Value    any
}

// FinishSettingInteraction claims a live card and commits its outcome and setting together.
// A competing answer or an expired card changes nothing. A stale setting ends the card as failed.
func (d *DB) FinishSettingInteraction(ctx context.Context, id, status string, answer proto.Resolution, change *ApprovedSetting) (outcome string, err error) {
	raw, err := json.Marshal(answer)
	if err != nil {
		return "", err
	}
	err = pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE interactions SET status = $2, resolution = $3, resolved_at = now()
			WHERE id = $1 AND status = 'pending' AND (payload->>'expires_at')::timestamptz > now()`, id, status, raw)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		outcome = status
		if change == nil {
			return nil
		}
		value, err := json.Marshal(change.Value)
		if err != nil {
			return err
		}
		table := d.settingsTable()
		var updated bool
		if change.Revision != nil {
			updated, err = compareSetting(ctx, tx, table, change.Key, *change.Revision, value)
		} else if change.Before == change.Default {
			tag, err = tx.Exec(ctx, `INSERT INTO `+table+` AS s (key, value, updated_at)
				VALUES ($1, $2::jsonb, now())
				ON CONFLICT (key) DO UPDATE SET value = $2::jsonb, updated_at = now()
				WHERE s.value = to_jsonb($3::text)`, change.Key, value, change.Before)
			updated = tag.RowsAffected() == 1
		} else {
			tag, err = tx.Exec(ctx, `UPDATE `+table+` SET value = $2::jsonb, updated_at = now()
				WHERE key = $1 AND value = to_jsonb($3::text)`, change.Key, value, change.Before)
			updated = tag.RowsAffected() == 1
		}
		if err != nil || updated {
			return err
		}
		outcome = "failed"
		_, err = tx.Exec(ctx, `UPDATE interactions SET status = 'failed', resolution = NULL WHERE id = $1`, id)
		return err
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
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
	resumableAtStartupSQL = `(payload->>'resumable')::boolean IS TRUE
		AND ((payload->>'expires_at')::timestamptz > now()
			OR ((payload->>'owner_proposal')::boolean IS TRUE AND (payload->>'no_expiry')::boolean IS TRUE))`
	resumablePendingSQL = `SELECT payload FROM interactions WHERE status = 'pending' AND ` + resumableAtStartupSQL
	startupPendingSQL   = `SELECT id, CASE WHEN ` + resumableAtStartupSQL + ` THEN payload END
		FROM interactions WHERE status = 'pending'`
	cancelPendingSQL = `UPDATE interactions SET status = 'cancelled', resolved_at = now(), ended_by = 'cleared'
		WHERE status = 'pending' AND id = ANY($1) AND (` + resumableAtStartupSQL + `) IS NOT TRUE
		AND id NOT IN (SELECT interaction_id FROM session_work WHERE interaction_id <> '')
		AND id NOT IN (SELECT protected.id FROM settings,
			jsonb_array_elements(CASE WHEN jsonb_typeof(value) = 'array' THEN value ELSE '[]'::jsonb END) AS item,
			LATERAL unnest(ARRAY[item->'card'->>'id', item->>'expired_id']) AS protected(id)
			WHERE key = 'agents.tool_loops' AND protected.id IS NOT NULL)
		RETURNING payload`
)

// ResumableInteractions returns the pending, unexpired cards that may be
// restored after a manager restart. Unlike ExpireStaleInteractions it never
// writes, so a delayed retry cannot cancel a current-process card.
func (d *DB) ResumableInteractions(ctx context.Context) ([]proto.Interaction, error) {
	rows, err := d.pool.Query(ctx, resumablePendingSQL)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (proto.Interaction, error) {
		var payload []byte
		var ix proto.Interaction
		if err := r.Scan(&payload); err != nil {
			return ix, err
		}
		return ix, json.Unmarshal(payload, &ix)
	})
}

// ExpireStaleInteractions marks interactions left pending by a previous hub process cancelled, and returns the
// resumable ones not yet expired instead: their hooks reconnect and claim them again. The manager's cards about a
// turn (session_work's interaction_id) and loop cards in the manager's settings stay pending too. The manager restores
// them under the same id and ends cards it no longer needs. cancelled carries the
// payloads it marked: the apps may still show them (#4568), and their occurrence
// timestamps prevent a delayed withdrawal from ending a reused interaction ID.
func (d *DB) ExpireStaleInteractions(ctx context.Context) (kept []proto.Interaction, cancelled []proto.Interaction, err error) {
	return d.expireStaleInteractions(ctx, nil)
}

func (d *DB) expireStaleInteractions(ctx context.Context, beforeCancel func()) (kept []proto.Interaction, cancelled []proto.Interaction, err error) {
	rows, err := d.pool.Query(ctx, startupPendingSQL)
	if err != nil {
		return nil, nil, err
	}
	var stale []string
	var id string
	var payload []byte
	_, err = pgx.ForEachRow(rows, []any{&id, &payload}, func() error {
		if payload == nil {
			stale = append(stale, id)
			return nil
		}
		var ix proto.Interaction
		if err := json.Unmarshal(payload, &ix); err != nil {
			return err
		}
		kept = append(kept, ix)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if beforeCancel != nil {
		beforeCancel()
	}
	if len(stale) == 0 {
		return kept, nil, nil
	}
	// Another manager can publish or reclaim a card during recovery. Cancel only
	// rows we saw as stale, provided they are still pending and not resumable. A
	// current-process card inserted after this lookup is not in the stale set.
	rows, err = d.pool.Query(ctx, cancelPendingSQL, stale)
	if err != nil {
		return nil, nil, err
	}
	cancelled, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (proto.Interaction, error) {
		var payload []byte
		var ix proto.Interaction
		if err := r.Scan(&payload); err != nil {
			return ix, err
		}
		return ix, json.Unmarshal(payload, &ix)
	})
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
