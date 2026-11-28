package assistant

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

type todoTopologyWrite struct {
	s       *Service
	tx      pgx.Tx
	at      time.Time
	created []db.TodoRow
	edited  []db.TodoRow
	ledgers map[string]*Supervision
}

// writeTodoTopology keeps the graph still from validation through commit and cache publication. It gives
// readers the whole batch under todos.mu and releases the writers before running any notification effects.
func (s *Service) writeTodoTopology(ctx context.Context, prepare func() error, write func(*todoTopologyWrite) error) ([]Todo, error) {
	var updated []Todo
	var retired [][2]string
	var after []func()
	err := func() error {
		s.todos.writing.Lock()
		defer s.todos.writing.Unlock()
		s.supervise.writeMu.Lock()
		defer s.supervise.writeMu.Unlock()
		if prepare != nil {
			if err := prepare(); err != nil {
				return err
			}
		}
		w := &todoTopologyWrite{s: s, at: time.Now().UTC()}
		if s.todoDB() != nil {
			st, ok := s.store().(interface {
				Transaction(context.Context, func(pgx.Tx) error) error
			})
			if !ok {
				return errors.New("to-do store does not support topology transactions")
			}
			if err := st.Transaction(ctx, func(tx pgx.Tx) error {
				w.tx = tx
				return write(w)
			}); err != nil {
				return err
			}
		} else if err := write(w); err != nil {
			return err
		}

		s.todos.mu.Lock()
		defer s.todos.mu.Unlock()
		for _, row := range w.created {
			updated = append(updated, s.addTodoRowLocked(row))
		}
		for _, row := range w.edited {
			if ledger := w.ledgers[row.ID]; ledger != nil {
				t := s.todos.todos[row.ID]
				ledger.Progress = t.Supervision.Progress
				t.Supervision = ledger
			}
			out, keys, effect, ok := s.setTodoRowLocked(row.ID, row, nil)
			if ok {
				updated = append(updated, out)
				retired = append(retired, keys...)
			}
			if effect != nil {
				after = append(after, effect)
			}
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}
	for _, effect := range after {
		effect()
	}
	if len(after) > 0 || len(retired) > 0 {
		s.todoRowSet(ctx, retired)
	} else if len(updated) > 0 {
		s.todosChanged(ctx)
	}
	return updated, nil
}

// lockTodos checks the persisted versions of the fields the graph and close guards read. Other
// writes can change updated_at without changing these fields. Take SQL row locks in ID order
// before touching repositories, blockers, or decisions.
func (w *todoTopologyWrite) lockTodos(ctx context.Context, ids []string) error {
	if w.tx == nil {
		return nil
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		cur, ok := w.s.todoNow(id)
		if !ok {
			return db.ErrTodoNotFound
		}
		row, err := db.LockTodoTx(ctx, w.tx, id)
		if err != nil {
			return err
		}
		if row.PartOf != cur.PartOf || !slices.Equal(row.After, cur.After) || row.DoneBy != cur.DoneBy ||
			!sameStoredTodoTime(row.DoneAt, cur.DoneAt) || !sameStoredTodoTime(row.ReopenedAt, cur.ReopenedAt) ||
			row.Title != cur.Title || row.Notes != cur.Notes || row.DirectionID != cur.DirectionID ||
			row.Project != cur.Project || row.ItemType != cur.ItemType || row.Kind != cur.Kind ||
			row.Origin != cur.Origin || row.StartError != cur.StartError {
			return topologyConflictError{err: i18n.Err("assistant-todos.topology_changed")}
		}
	}
	return nil
}

func sameStoredTodoTime(stored, inMemory *time.Time) bool {
	if stored == nil || inMemory == nil {
		return stored == nil && inMemory == nil
	}
	return stored.Equal(inMemory.Truncate(time.Microsecond))
}

func (w *todoTopologyWrite) edit(ctx context.Context, cur Todo, edit db.TodoEdit) (db.TodoRow, error) {
	row := todoRowOf(cur)
	if w.tx != nil {
		saved, err := db.EditTodoTx(ctx, w.tx, cur.ID, edit, w.at)
		if err != nil {
			return db.TodoRow{}, err
		}
		row = *saved
	} else {
		edit.Apply(&row, w.at)
	}
	w.edited = append(w.edited, row)
	return row, nil
}

func (w *todoTopologyWrite) create(ctx context.Context, row db.TodoRow) error {
	if w.tx != nil {
		if err := db.CreateTodoTx(ctx, w.tx, row); err != nil {
			return err
		}
	}
	w.created = append(w.created, row)
	return nil
}

func (w *todoTopologyWrite) blockers() todoBlockerWriter {
	if w.tx != nil {
		return db.TodoBlockersTx(w.tx)
	}
	return w.s.todoBlockerDB()
}
