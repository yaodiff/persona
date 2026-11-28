package repositorystore

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func workspaceProblem(code, op string) error {
	return taskworkspace.Failure(code, "manager", op, "", "")
}
func readTaskWorkspace(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, todo, op string) (*taskworkspace.Record, error) {
	query := `SELECT o.revision,o.record FROM task_workspace_operations o WHERE o.todo_id=$1 AND o.operation_id=$2`
	args := []any{todo, op}
	if op == "" {
		query = `SELECT o.revision,o.record FROM task_workspace_heads h JOIN task_workspace_operations o ON o.operation_id=h.operation_id WHERE h.todo_id=$1`
		args = args[:1]
	}
	var raw []byte
	var version int64
	err := q.QueryRow(ctx, query, args...).Scan(&version, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r taskworkspace.Record
	if err = json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	r.Revision = version
	return &r, nil
}
func (d *Store) TaskWorkspace(ctx context.Context, todo, op string) (*taskworkspace.Record, error) {
	return readTaskWorkspace(ctx, d, todo, op)
}
func writeTaskWorkspace(ctx context.Context, tx pgx.Tx, r *taskworkspace.Record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO task_workspace_operations(operation_id,todo_id,workspace_id,revision,record) VALUES($1,$2,$3,$4,$5) ON CONFLICT(operation_id) DO UPDATE SET revision=$4,record=$5`, r.OperationID, r.Plan.Source.TodoID, r.WorkspaceID, r.Revision, raw)
	return err
}
func lockWorkspaceTodo(ctx context.Context, tx pgx.Tx, todo string) (bool, error) {
	var done bool
	err := tx.QueryRow(ctx, `SELECT done_at IS NOT NULL FROM todos WHERE id=$1 FOR UPDATE`, todo).Scan(&done)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, db.ErrTodoNotFound
	}
	return done, err
}

// BeginTaskWorkspace atomically adopts the saved T2 plan and selected T1 inputs.
// Business associations remain editable after this operation is registered.
func (d *Store) BeginTaskWorkspace(ctx context.Context, todo, op, manager string, replacing ...*taskworkspace.Record) (*taskworkspace.Record, error) {
	if !workspacenaming.ValidOperationID(op) {
		return nil, workspaceProblem("invalid_input", op)
	}
	var result *taskworkspace.Record
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		done, err := lockWorkspaceTodo(ctx, tx, todo)
		if err != nil {
			return err
		}
		old, err := readTaskWorkspace(ctx, tx, todo, op)
		if err != nil {
			return err
		}
		if old != nil {
			if old.Plan.Source.Manager != manager {
				return workspaceProblem("identity_mismatch", op)
			}
			result = old
			return nil
		}
		if done {
			return db.ErrTodoCompleted
		}
		plan, err := ReadWorkspaceNamingPlanTx(ctx, tx, todo, op)
		if err != nil {
			return err
		}
		if plan.Source.Manager != manager {
			return workspaceProblem("invalid_input", op)
		}
		set, err := ReadTodoRepositoriesTx(ctx, tx, todo, nil)
		if err != nil {
			return err
		}
		matches, err := matchesWorkspaceSnapshot(plan, set)
		if err != nil {
			return err
		}
		if !matches {
			return workspaceProblem("snapshot_stale", op)
		}
		current, err := readTaskWorkspace(ctx, tx, todo, "")
		if err != nil {
			return err
		}
		if len(replacing) > 0 {
			prior := replacing[0]
			if current == nil || current.OperationID != prior.OperationID || current.Revision != prior.Revision ||
				(current.State != "failed" && current.State != "ready") || current.Plan.Source.Manager != manager ||
				len(current.Uses) != 0 || current.Plan.RootPath == plan.RootPath {
				return workspaceProblem("operation_conflict", op)
			}
			// Retire admissions to the old operation in the same transaction that
			// installs the new head. Its receipt remains available for inspection and cleanup.
			current.State, current.Revision = "cancelled", current.Revision+1
			if err := writeTaskWorkspace(ctx, tx, current); err != nil {
				return err
			}
			current = nil
		}
		if current != nil && current.State != "cancelled" && current.State != "removed" {
			if current.InputDigest == taskworkspace.InputKey(plan) {
				result = current
				return nil
			}
			return workspaceProblem("operation_conflict", op)
		}
		r := &taskworkspace.Record{SchemaVersion: 1, WorkspaceID: "ws_" + taskworkspace.Hash([]string{manager, todo, op})[:24], OperationID: op, Plan: plan, InputDigest: taskworkspace.InputKey(plan), State: "preparing", Repositories: []taskworkspace.Instance{}, Revision: 1, Uses: []string{}}
		if current != nil && current.State == "cancelled" {
			if current.Plan.Source.MachineID != plan.Source.MachineID || current.Cleanup != nil {
				return workspaceProblem("resource_conflict", op)
			}
			if len(current.Uses) > 0 {
				return workspaceProblem("worktree_in_use", op)
			}
			r.WorkspaceID = current.WorkspaceID
			r.PreviousOperationID = current.OperationID
			if current.NodeOperationID != "" {
				r.PreviousOperationID = current.NodeOperationID
			}
		}
		if err = writeTaskWorkspace(ctx, tx, r); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO task_workspace_heads(todo_id,operation_id) VALUES($1,$2) ON CONFLICT(todo_id) DO UPDATE SET operation_id=$2`, todo, op)
		result = r
		return err
	})
	return result, err
}

// MutateTaskWorkspace shares the Todo lock with cancellation/completion and use
// admission. No transaction is held while a Node is contacted.
func (d *Store) MutateTaskWorkspace(ctx context.Context, todo, op string, version int64, action, use string) (*taskworkspace.Record, error) {
	var out *taskworkspace.Record
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		done, err := lockWorkspaceTodo(ctx, tx, todo)
		if err != nil {
			return err
		}
		r, err := readTaskWorkspace(ctx, tx, todo, op)
		if err != nil {
			return err
		}
		if r == nil {
			return workspaceProblem("not_found", op)
		}
		if version > 0 && r.Revision != version {
			return workspaceProblem("revision_conflict", op)
		}
		switch action {
		case "cancel":
			if r.State != "removed" {
				r.State = "cancelled"
			}
		case "cleanup":
			if len(r.Uses) > 0 {
				return workspaceProblem("worktree_in_use", op)
			}
			if r.State != "removed" {
				r.State = "cleaning"
			}
		case "acquire":
			if done || r.State != "ready" || r.Cleanup != nil || !workspacenaming.ValidOperationID(use) {
				return workspaceProblem("not_ready", op)
			}
			if strings.HasPrefix(use, "launch_") && slices.ContainsFunc(r.Uses, func(other string) bool { return strings.HasPrefix(other, "launch_") && other != use }) {
				return workspaceProblem("worktree_in_use", op)
			}
			if !slices.Contains(r.Uses, use) {
				r.Uses = append(r.Uses, use)
			}
		case "release":
			r.Uses = slices.DeleteFunc(r.Uses, func(id string) bool { return id == use })
		case "check":
			if done || r.State != "ready" || r.Cleanup != nil || !slices.Contains(r.Uses, use) {
				return workspaceProblem("not_ready", op)
			}
			out = r
			return nil
		default:
			return workspaceProblem("invalid_input", op)
		}
		r.Revision++
		if err = writeTaskWorkspace(ctx, tx, r); err != nil {
			return err
		}
		out = r
		return nil
	})
	return out, err
}
func (d *Store) SaveTaskWorkspaceResult(ctx context.Context, todo, op string, version int64, result taskworkspace.Record) (*taskworkspace.Record, error) {
	var out *taskworkspace.Record
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		done, err := lockWorkspaceTodo(ctx, tx, todo)
		if err != nil {
			return err
		}
		cur, err := readTaskWorkspace(ctx, tx, todo, op)
		if err != nil {
			return err
		}
		if cur == nil {
			return workspaceProblem("not_found", op)
		}
		// A late response never undoes cancellation, a newer use claim or cleanup.
		if cur.Revision != version || cur.State == "cancelled" && result.State != "cancelled" || cur.State == "removed" {
			out = cur
			return nil
		}
		if result.WorkspaceID != cur.WorkspaceID || result.OperationID != op || result.InputDigest != cur.InputDigest || result.Plan.Digest != cur.Plan.Digest || workspacenaming.CheckPlan(result.Plan) != nil {
			return workspaceProblem("identity_mismatch", op)
		}
		if err = taskworkspace.ValidateReady(result); err != nil {
			return err
		}
		if done && result.State != "removed" {
			result.State = "cancelled"
		}
		result.Uses = slices.Clone(cur.Uses)
		result.Revision = cur.Revision + 1
		if err = writeTaskWorkspace(ctx, tx, &result); err != nil {
			return err
		}
		out = &result
		return nil
	})
	return out, err
}
