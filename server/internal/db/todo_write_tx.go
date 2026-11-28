package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type rowQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// TodoBlockerWrites uses the caller's transaction for blocker changes and locks the rows it reads.
// The caller first locks the owning to-dos, which also fences inserts through the foreign key.
type TodoBlockerWrites struct {
	tx pgx.Tx
}

func TodoBlockersTx(tx pgx.Tx) TodoBlockerWrites {
	return TodoBlockerWrites{tx: tx}
}

func (b TodoBlockerWrites) ListTodoBlockersByTodo(ctx context.Context, todoID string) ([]TodoBlockerRow, error) {
	return listTodoBlockersByTodo(ctx, b.tx, todoID, true)
}

func (b TodoBlockerWrites) UpsertTodoBlocker(ctx context.Context, r TodoBlockerRow) (*TodoBlockerRow, error) {
	return upsertTodoBlocker(ctx, b.tx, r)
}

func (b TodoBlockerWrites) CancelTodoBlocker(ctx context.Context, id string, at time.Time) (*TodoBlockerRow, error) {
	return cancelTodoBlocker(ctx, b.tx, id, at)
}
