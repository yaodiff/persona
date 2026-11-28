package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// TodoSupervisionRow is a supervised task's ledger (#3124): its supervisor (the conversation that handed it out),
// what done means, how long it should take, and its review: State "" while it goes on, then pending_review, accepted,
// rework (Round counts them), needs_user or cancelled. Reviews is the assistant's reviews, oldest first, as it wrote
// them (JSON the assistant owns).
type TodoSupervisionRow struct {
	TodoID           string          `json:"todo_id"`
	Supervisor       string          `json:"supervisor"`
	SupervisorSource string          `json:"supervisor_source,omitempty"`
	DoneWhen         []string        `json:"done_when"`
	Criteria         []TodoCriterion `json:"criteria"`
	ExpectMinutes    int             `json:"expect_minutes"`
	State            string          `json:"state"`
	Round            int             `json:"round"`
	Reviews          json.RawMessage `json:"reviews"`
	// AutoReview is the latest independent review run (#2173), JSON the assistant owns; nil when none ran.
	AutoReview json.RawMessage `json:"auto_review,omitempty"`
	// InvestigationFindings is the assistant’s durable report and finding dispositions.
	InvestigationFindings json.RawMessage `json:"investigation_findings,omitempty"`
	// Waits is the done judge's finding that the latest stretch of work stopped on the owner's decision (#1277), JSON the
	// assistant owns; nil when it does not hold.
	Waits json.RawMessage `json:"waits,omitempty"`
	// OwnerWork is the latest owner instruction and Persona assignment per linked Agent session (#15406), JSON the
	// assistant owns. It is kept apart from the review state and is never exposed with the to-do.
	OwnerWork json.RawMessage `json:"owner_work,omitempty"`
	// PendingSince is when the task entered pending_review; nil in every other state, and for a task that was already
	// waiting for review with no time recorded.
	PendingSince *time.Time `json:"pending_since,omitempty"`
	FollowedUpAt *time.Time `json:"followed_up_at,omitempty"`
	// CardlessToldAt is when the watch handed the current pending_review round back to its supervisor because it waits
	// on the owner with no card (supervise_cardless.go); nil when this review round was never handed back that way.
	CardlessToldAt *time.Time `json:"cardless_told_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// TodoCriterion is the latest report or check of one done_when entry. At and By
// are absent for an entry nobody has checked; a report never accepts the task.
type TodoCriterion struct {
	Criterion string     `json:"criterion"`
	State     string     `json:"state"` // pending, met, not_met
	Evidence  string     `json:"evidence,omitempty"`
	At        *time.Time `json:"at,omitempty"`
	By        string     `json:"by,omitempty"` // agent:<machine>/<session>, supervisor:<conversation>, judge
}

// AlignTodoCriteria keeps the text and order of done_when authoritative. Reordered
// entries keep their evidence; new or edited entries start pending. The result
// owns its slice and timestamps, as callers use it for snapshots too.
func AlignTodoCriteria(doneWhen []string, criteria []TodoCriterion) []TodoCriterion {
	byText := make(map[string]TodoCriterion, len(criteria))
	for _, c := range criteria {
		byText[c.Criterion] = c
	}
	out := make([]TodoCriterion, 0, len(doneWhen))
	for _, text := range doneWhen {
		c, ok := byText[text]
		if !ok || c.State != "pending" && c.State != "met" && c.State != "not_met" {
			c = TodoCriterion{Criterion: text, State: "pending"}
		}
		if c.At != nil {
			at := *c.At
			c.At = &at
		}
		out = append(out, c)
	}
	return out
}

// ListTodoSupervision returns every supervised task's ledger.
func (d *DB) ListTodoSupervision(ctx context.Context) ([]TodoSupervisionRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT todo_id, supervisor, done_when, expect_minutes, state, round, reviews, auto_review, waits, owner_work, updated_at, criteria, pending_since, supervisor_source, cardless_told_at, investigation_findings, followed_up_at
		FROM todo_supervision`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TodoSupervisionRow
	for rows.Next() {
		var r TodoSupervisionRow
		var when, auto, waits, ownerWork, criteria []byte
		if err := rows.Scan(&r.TodoID, &r.Supervisor, &when, &r.ExpectMinutes, &r.State, &r.Round, &r.Reviews, &auto, &waits, &ownerWork, &r.UpdatedAt, &criteria, &r.PendingSince, &r.SupervisorSource, &r.CardlessToldAt, &r.InvestigationFindings, &r.FollowedUpAt); err != nil {
			return nil, err
		}
		if len(auto) > 0 {
			r.AutoReview = auto
		}
		if len(waits) > 0 {
			r.Waits = waits
		}
		if len(ownerWork) > 0 && string(ownerWork) != "[]" {
			r.OwnerWork = ownerWork
		}
		_ = json.Unmarshal(when, &r.DoneWhen)
		_ = json.Unmarshal(criteria, &r.Criteria)
		r.Criteria = AlignTodoCriteria(r.DoneWhen, r.Criteria)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutTodoSupervision writes a task's ledger whole (the assistant keeps it and writes it as it changes).
// ErrTodoNotFound when the todo is gone.
func (d *DB) PutTodoSupervision(ctx context.Context, r TodoSupervisionRow) error {
	return putTodoSupervision(ctx, d.pool, r)
}

// TransferTodoSupervisionTx changes only assignment columns and checks the ledger read by the caller.
func TransferTodoSupervisionTx(ctx context.Context, tx pgx.Tx, prior TodoSupervisionRow, to string, at time.Time) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE todo_supervision SET supervisor = $2, supervisor_source = 'explicit', updated_at = $3
		WHERE todo_id = $1 AND supervisor = $4 AND updated_at = $5`, prior.TodoID, to, at, prior.Supervisor, prior.UpdatedAt)
	return tag.RowsAffected() != 0, err
}

func putTodoSupervision(ctx context.Context, q execer, r TodoSupervisionRow) error {
	when, _ := json.Marshal(r.DoneWhen)
	if r.DoneWhen == nil {
		when = []byte("[]")
	}
	criteria, err := json.Marshal(AlignTodoCriteria(r.DoneWhen, r.Criteria))
	if err != nil {
		return err
	}
	reviews := r.Reviews
	if len(reviews) == 0 {
		reviews = json.RawMessage("[]")
	}
	var auto, waits []byte // NULL when no review ran / when no wait holds
	ownerWork := r.OwnerWork
	if len(ownerWork) == 0 {
		ownerWork = json.RawMessage("[]")
	}
	if len(r.AutoReview) > 0 {
		auto = r.AutoReview
	}
	if len(r.Waits) > 0 {
		waits = r.Waits
	}
	tag, err := q.Exec(ctx, `INSERT INTO todo_supervision (todo_id, supervisor, done_when, expect_minutes, state, round, reviews, auto_review, waits, owner_work, updated_at, criteria, pending_since, supervisor_source, cardless_told_at, investigation_findings, followed_up_at)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17 WHERE EXISTS (SELECT 1 FROM todos WHERE id = $1)
		ON CONFLICT (todo_id) DO UPDATE SET supervisor = EXCLUDED.supervisor, done_when = EXCLUDED.done_when,
			expect_minutes = EXCLUDED.expect_minutes, state = EXCLUDED.state, round = EXCLUDED.round,
			reviews = EXCLUDED.reviews, auto_review = EXCLUDED.auto_review, waits = EXCLUDED.waits, owner_work = EXCLUDED.owner_work,
			updated_at = EXCLUDED.updated_at, criteria = EXCLUDED.criteria,
			pending_since = EXCLUDED.pending_since, supervisor_source = EXCLUDED.supervisor_source, cardless_told_at = EXCLUDED.cardless_told_at, investigation_findings = EXCLUDED.investigation_findings, followed_up_at = EXCLUDED.followed_up_at`,
		r.TodoID, r.Supervisor, when, r.ExpectMinutes, r.State, r.Round, reviews, auto, waits, ownerWork, r.UpdatedAt, criteria, r.PendingSince, r.SupervisorSource, r.CardlessToldAt, r.InvestigationFindings, r.FollowedUpAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTodoNotFound
	}
	return nil
}

// ReviewDoneTodo closes todo id after an accepted review (DoneByReview) or cancellation (DoneByCancelled), with
// reason while it is open or its start failed, clearing StartError in the same write; false when it was ordinarily
// done already or it is a concern (only its report closes it). ErrTodoNotFound when it is gone.
func (d *DB) ReviewDoneTodo(ctx context.Context, id, reason string, accepted bool, at time.Time) (*TodoRow, bool, error) {
	return reviewDoneTodo(ctx, d.pool, id, reason, accepted, at)
}

// ReviewDoneTodoTx stages the same closure in a caller's transaction.
func ReviewDoneTodoTx(ctx context.Context, tx pgx.Tx, id, reason string, accepted bool, at time.Time) (*TodoRow, bool, error) {
	return reviewDoneTodo(ctx, tx, id, reason, accepted, at)
}

// ReviewTodo commits a review's ledger and closure together. The final guard runs after the tentative writes;
// a refusal rolls them both back, including activity that arrived while the database was busy.
func (d *DB) ReviewTodo(ctx context.Context, id, reason string, accepted bool, at time.Time, ledger *TodoSupervisionRow, guard func() error) (*TodoRow, bool, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	row, closed, err := reviewDoneTodo(ctx, tx, id, reason, accepted, at)
	if err != nil {
		return nil, false, err
	}
	if ledger != nil {
		if err := putTodoSupervision(ctx, tx, *ledger); err != nil {
			return nil, false, err
		}
	}
	if err := guard(); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return row, closed, nil
}

func reviewDoneTodo(ctx context.Context, q queryRower, id, reason string, accepted bool, at time.Time) (*TodoRow, bool, error) {
	doneBy := DoneByCancelled
	if accepted {
		doneBy = DoneByReview
	}
	var r TodoRow
	err := q.QueryRow(ctx, `UPDATE todos SET done_at = $2, done_by = $3, done_reason = $4, start_error = '', updated_at = $2
		WHERE id = $1 AND (done_at IS NULL OR start_error <> '') AND kind <> 'concern' RETURNING `+todoColumns, id, at, doneBy, reason).
		Scan(todoDest(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM todos WHERE id = $1)`, id).Scan(&exists); err != nil {
			return nil, false, err
		}
		if !exists {
			return nil, false, ErrTodoNotFound
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &r, true, nil
}

// LinkTodoReviewer records the independent reviewer of a supervised task's change (#2173) on its todo: source
// "review", via the review run's id, released from the start, so it never counts as one of the task's working agents
// (the judge, rework, closing). Written once; a link already there stays as it is.
func (d *DB) LinkTodoReviewer(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, via string, at time.Time) error {
	tag, err := d.pool.Exec(ctx, `INSERT INTO todo_agents (todo_id, machine_id, session_id, linked_at, released_at, title, cwd, agent, start_error, source, via)
		SELECT $1, $2, $3, $4, $4, $5, $6, $7, '', 'review', $8 WHERE EXISTS (SELECT 1 FROM todos WHERE id = $1)
		ON CONFLICT (todo_id, machine_id, session_id) DO NOTHING`,
		todoID, machineID, sessionID, at, title, cwd, agent, via)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, gerr := d.GetTodo(ctx, todoID); gerr != nil {
			return gerr
		}
	}
	return nil
}
