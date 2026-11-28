package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// RecordProjectReview commits an observation and its delivery together. Replayed or older
// observations cannot open another episode after recovery. An owner input does not affect it.
func (d *DB) RecordProjectReview(ctx context.Context, id, target, binding string, event json.RawMessage, phase string, at time.Time) (bool, error) {
	queued := false
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		if phase != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO project_review_state VALUES ($1, '-infinity', '')
				ON CONFLICT DO NOTHING`, binding); err != nil {
				return err
			}
			var blocked string
			var older, same bool
			if err := tx.QueryRow(ctx, `SELECT block_reason, $2 < observed_at, $2 = observed_at
				FROM project_review_state WHERE binding=$1 FOR UPDATE`, binding, at).Scan(&blocked, &older, &same); err != nil {
				return err
			}
			switch phase {
			case "blocked":
				if older || same && blocked != "" {
					return nil
				}
				if blocked != "" {
					_, err := tx.Exec(ctx, `UPDATE project_review_state SET observed_at=$2 WHERE binding=$1`, binding, at)
					return err
				}
			case "working", "working_after_reconnect":
				// Postgres compares at as it stores it (microseconds), so a replayed row is the same, not newer.
				// A newer row is work, also when it is the first after a reconnect and the episode is a stall:
				// the stall is keyed by the same progress time.
				if older || same {
					return nil
				}
				phase = "working"
			}
			if phase != "working" {
				tag, err := tx.Exec(ctx, `INSERT INTO project_review_deliveries(id,target,event) VALUES ($1,$2,$3)
					ON CONFLICT (id) DO NOTHING`, id, target, event)
				if err != nil {
					return err
				}
				queued = tag.RowsAffected() > 0
				if !queued || older || same && blocked != "" {
					return nil
				}
			}
			_, err := tx.Exec(ctx, `UPDATE project_review_state SET observed_at=$2,
				block_reason=CASE WHEN $3='blocked' THEN $4::jsonb->>'kind' ELSE '' END WHERE binding=$1`,
				binding, at, phase, event)
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO project_review_deliveries(id,target,event) VALUES ($1,$2,$3)
			ON CONFLICT (id) DO NOTHING`, id, target, event)
		if err == nil {
			queued = tag.RowsAffected() > 0
		}
		return err
	})
	return queued, err
}

type ProjectReviewDelivery struct {
	ID           string
	Event        json.RawMessage
	RetryAt      time.Time
	Attempts     int
	HistoryAfter *int
	ReviewOnly   bool
	// Downgraded: reserved with a scoped prompt, later resent as triage. The scoped text may have landed first.
	Downgraded bool
	// Sends: native sends started, each under its own client message ID. SendConfirmed: the hub took the latest,
	// at SendConfirmedAt.
	Sends           int
	SendConfirmed   bool
	SendConfirmedAt time.Time
}

// NextProjectReview returns the head, even if it is waiting for its next attempt.
func (d *DB) NextProjectReview(ctx context.Context, target string) (ProjectReviewDelivery, bool, error) {
	var e ProjectReviewDelivery
	var confirmedAt *time.Time
	err := d.pool.QueryRow(ctx, `SELECT id,event,retry_at,attempts,history_after,coalesce(review_only,false),scope_downgraded,sends,send_confirmed,
		send_confirmed_at FROM project_review_deliveries WHERE target=$1 AND settled_at IS NULL ORDER BY seq LIMIT 1`, target).Scan(&e.ID,
		&e.Event, &e.RetryAt, &e.Attempts, &e.HistoryAfter, &e.ReviewOnly, &e.Downgraded, &e.Sends, &e.SendConfirmed, &confirmedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, false, nil
	}
	if confirmedAt != nil {
		e.SendConfirmedAt = *confirmedAt
	}
	return e, err == nil, err
}

func (d *DB) RetryProjectReview(ctx context.Context, id string, retryAt time.Time, outcome string) error {
	_, err := d.pool.Exec(ctx, `UPDATE project_review_deliveries SET attempts=attempts+1,retry_at=$2,outcome=$3
		WHERE id=$1 AND settled_at IS NULL`, id, retryAt, outcome)
	return err
}

func (d *DB) SettleProjectReview(ctx context.Context, id, outcome string) error {
	_, err := d.pool.Exec(ctx, `UPDATE project_review_deliveries SET settled_at=now(),outcome=$2
		WHERE id=$1 AND settled_at IS NULL`, id, outcome)
	return err
}

// ReserveProjectReviewHistory saves the transcript position and scope before any native send.
func (d *DB) ReserveProjectReviewHistory(ctx context.Context, id string, after int, reviewOnly bool) error {
	_, err := d.pool.Exec(ctx, `UPDATE project_review_deliveries
		SET history_after=$2,review_only=$3 WHERE id=$1 AND history_after IS NULL AND settled_at IS NULL`, id, after, reviewOnly)
	return err
}

// DowngradeProjectReview records that a reserved scoped delivery is now sent as triage only. It never upgrades.
func (d *DB) DowngradeProjectReview(ctx context.Context, id string) error {
	_, err := d.pool.Exec(ctx, `UPDATE project_review_deliveries SET review_only=true,scope_downgraded=true
		WHERE id=$1 AND history_after IS NOT NULL AND review_only IS NOT TRUE AND settled_at IS NULL`, id)
	return err
}

// BeginProjectReviewSend counts a native send before it starts and returns its number, which names its client
// message ID. The hub has not confirmed it yet.
func (d *DB) BeginProjectReviewSend(ctx context.Context, id string) (int, error) {
	var n int
	err := d.pool.QueryRow(ctx, `UPDATE project_review_deliveries SET sends=sends+1,send_confirmed=false,send_confirmed_at=NULL
		WHERE id=$1 AND settled_at IS NULL RETURNING sends`, id).Scan(&n)
	return n, err
}

// ConfirmProjectReviewSend records that the hub took send n at at, unless another send started since.
func (d *DB) ConfirmProjectReviewSend(ctx context.Context, id string, n int, at time.Time) error {
	_, err := d.pool.Exec(ctx, `UPDATE project_review_deliveries SET send_confirmed=true,send_confirmed_at=$3
		WHERE id=$1 AND sends=$2 AND settled_at IS NULL`, id, n, at)
	return err
}

// AdvanceProjectReviewHistory moves the transcript position past a review turn that closed without a receipt, so
// the next send's echo and receipt are looked for after it.
func (d *DB) AdvanceProjectReviewHistory(ctx context.Context, id string, after int) error {
	_, err := d.pool.Exec(ctx, `UPDATE project_review_deliveries SET history_after=$2,send_confirmed=false,send_confirmed_at=NULL
		WHERE id=$1 AND history_after IS NOT NULL AND history_after < $2 AND settled_at IS NULL`, id, after)
	return err
}
