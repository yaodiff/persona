package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrThreadTaken: the thread is already bound to another todo (a thread belongs to at most one).
var ErrThreadTaken = errors.New("that thread is already bound to another todo")

// TodoThread is the chat thread a todo is bound to (#5279): the channel it lives on, the chat and the thread in it,
// and a title to show. The first three name the thread; none of them is empty in a stored binding.
type TodoThread struct {
	Channel  string `json:"channel"`
	ChatID   string `json:"chat_id"`
	ThreadID string `json:"thread_id"`
	Title    string `json:"title"`
}

// ListTodoThreads returns the bindings of all todos that have one, by todo id.
func (d *DB) ListTodoThreads(ctx context.Context) (map[string]TodoThread, error) {
	rows, err := d.pool.Query(ctx, `SELECT id, thread FROM todos WHERE thread IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]TodoThread{}
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var th TodoThread
		if err := json.Unmarshal(raw, &th); err != nil {
			return nil, err
		}
		out[id] = th
	}
	return out, rows.Err()
}

// SetTodoThread binds todo todoID to th (replacing the thread it had) and moves its updated_at to at. ErrTodoNotFound
// when the todo is gone, ErrThreadTaken when another todo holds the thread.
func (d *DB) SetTodoThread(ctx context.Context, todoID string, th TodoThread, at time.Time) error {
	raw, err := json.Marshal(struct {
		TodoThread
		Followups struct {
			Since time.Time `json:"since"`
		} `json:"followups"`
	}{TodoThread: th, Followups: struct {
		Since time.Time `json:"since"`
	}{at}})
	if err != nil {
		return err
	}
	tag, err := d.pool.Exec(ctx, `UPDATE todos SET thread = CASE
		WHEN thread ->> 'channel' = $2::jsonb ->> 'channel'
		 AND thread ->> 'chat_id' = $2::jsonb ->> 'chat_id'
		 AND thread ->> 'thread_id' = $2::jsonb ->> 'thread_id'
		THEN jsonb_set(thread, '{title}', $2::jsonb -> 'title')
		ELSE $2::jsonb END, updated_at = $3 WHERE id = $1`, todoID, raw, at)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "todos_thread" {
		return ErrThreadTaken
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTodoNotFound
	}
	return nil
}

// TodoThreadState includes internal source checkpoints; ListTodoThreads exposes only the public binding.
func (d *DB) TodoThreadState(ctx context.Context, id string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := d.pool.QueryRow(ctx, `SELECT thread FROM todos WHERE id = $1 AND thread IS NOT NULL`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTodoNotFound
	}
	return raw, err
}

// SaveTodoThreadFollowups cannot overwrite a rebind, another checkpoint, or a closed task.
// Observation does not make the task look recently active by changing updated_at.
func (d *DB) SaveTodoThreadFollowups(ctx context.Context, id string, before, next json.RawMessage) (json.RawMessage, bool, error) {
	var raw json.RawMessage
	err := d.pool.QueryRow(ctx, `UPDATE todos SET thread = jsonb_set(thread, '{followups}', $3::jsonb)
		WHERE id = $1 AND done_at IS NULL AND thread = $2::jsonb RETURNING thread`, id, before, next).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return raw, err == nil, err
}

// ClearTodoThread unbinds todo todoID and moves its updated_at to at; changed is false (and updated_at stays) when it
// had no thread. ErrTodoNotFound when the todo is gone.
func (d *DB) ClearTodoThread(ctx context.Context, todoID string, at time.Time) (changed bool, err error) {
	tag, err := d.pool.Exec(ctx, `UPDATE todos SET thread = NULL, updated_at = $2 WHERE id = $1 AND thread IS NOT NULL`, todoID, at)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	var exists bool
	if err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM todos WHERE id = $1)`, todoID).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, ErrTodoNotFound
	}
	return false, nil
}

// TodoOfThread is the todo bound to the thread (channel, chat, thread), or ErrTodoNotFound when none is.
func (d *DB) TodoOfThread(ctx context.Context, channel, chatID, threadID string) (string, error) {
	var id string
	err := d.pool.QueryRow(ctx, `SELECT id FROM todos WHERE thread IS NOT NULL
		AND thread ->> 'channel' = $1 AND thread ->> 'chat_id' = $2 AND thread ->> 'thread_id' = $3`,
		channel, chatID, threadID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrTodoNotFound
	}
	return id, err
}
