package db

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SupervisionTriageRow records what a background model decided about one signal generation.
type SupervisionTriageRow struct {
	ID, TodoID, Recipient, Signal, SignalKey, Title, Detail, Decision, Reason string
	At                                                                        time.Time
}

// SupervisionTriage reads the verdicts already recorded for the signals being retried.
func (d *DB) SupervisionTriage(ctx context.Context, ids []string) (map[string]string, error) {
	rows, err := d.pool.Query(ctx, `SELECT id, decision FROM supervision_triage WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string, len(ids))
	for rows.Next() {
		var id, decision string
		if err := rows.Scan(&id, &decision); err != nil {
			return nil, err
		}
		out[id] = decision
	}
	return out, rows.Err()
}

// SaveSupervisionTriage returns the winning verdicts after all new rows are durable.
// Another instance saving the same generation cannot overwrite the first verdict.
func (d *DB) SaveSupervisionTriage(ctx context.Context, verdicts []SupervisionTriageRow) (map[string]string, error) {
	verdicts = slices.Clone(verdicts)
	slices.SortFunc(verdicts, func(a, b SupervisionTriageRow) int { return strings.Compare(a.ID, b.ID) })
	out := make(map[string]string, len(verdicts))
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		for _, v := range verdicts {
			var decision string
			if err := tx.QueryRow(ctx, `INSERT INTO supervision_triage AS s
				(id, todo_id, recipient, signal, signal_key, title, detail, decision, reason, at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
				ON CONFLICT (id) DO UPDATE SET id = s.id RETURNING decision`,
				v.ID, v.TodoID, v.Recipient, v.Signal, v.SignalKey, v.Title, v.Detail, v.Decision, v.Reason, v.At).Scan(&decision); err != nil {
				return err
			}
			out[v.ID] = decision
		}
		return nil
	})
	return out, err
}

// DeferredSupervision reads the briefing window and any backlog the briefings have not yet included.
func (d *DB) DeferredSupervision(ctx context.Context, since, through time.Time) ([]SupervisionTriageRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT id, todo_id, recipient, signal, signal_key, title, detail, decision, reason, at
		FROM supervision_triage WHERE decision = 'defer' AND (at > $1 OR briefed_at IS NULL) AND at <= $2 ORDER BY at, id`, since, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SupervisionTriageRow
	for rows.Next() {
		var v SupervisionTriageRow
		if err := rows.Scan(&v.ID, &v.TodoID, &v.Recipient, &v.Signal, &v.SignalKey, &v.Title, &v.Detail, &v.Decision, &v.Reason, &v.At); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SupervisionBriefed records the first durable briefing for these rows. The verdicts
// stay queryable and still appear in other briefings whose window includes them.
func (d *DB) SupervisionBriefed(ctx context.Context, ids []string, at time.Time) error {
	_, err := d.pool.Exec(ctx, `UPDATE supervision_triage SET briefed_at = $2
		WHERE id = ANY($1) AND decision = 'defer' AND briefed_at IS NULL`, ids, at)
	return err
}
