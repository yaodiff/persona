package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/workspaceboard"
)

var ErrWorkspaceBoardConflict = errors.New("workspace board revision conflict")

// seedWorkspaceBoard never overwrites an existing board, including under concurrent first reads.
func (d *DB) seedWorkspaceBoard(ctx context.Context) error {
	data, err := json.Marshal(workspaceboard.Default())
	if err != nil {
		return err
	}
	_, err = d.pool.Exec(ctx, `INSERT INTO workspace_board (id, revision, data) VALUES (true, 0, $1) ON CONFLICT (id) DO NOTHING`, data)
	return err
}

// WorkspaceBoard reads the board and seeds the default only when the row is missing, so reading an existing board is one
// statement (the web board polls it every 10 s). Concurrent first reads stay safe: the seed never overwrites, and every
// reader then returns the stored row.
func (d *DB) WorkspaceBoard(ctx context.Context) (workspaceboard.State, error) {
	s, err := d.readWorkspaceBoard(ctx)
	if !errors.Is(err, pgx.ErrNoRows) {
		return s, err
	}
	if err := d.seedWorkspaceBoard(ctx); err != nil {
		return workspaceboard.State{}, err
	}
	return d.readWorkspaceBoard(ctx)
}

func (d *DB) readWorkspaceBoard(ctx context.Context) (workspaceboard.State, error) {
	var revision int64
	var data []byte
	if err := d.pool.QueryRow(ctx, `SELECT revision, data FROM workspace_board WHERE id = true`).Scan(&revision, &data); err != nil {
		return workspaceboard.State{}, err
	}
	return decodeWorkspaceBoard(revision, data)
}

// WorkspaceBoardInUse is whether the board was ever changed. Opening the page seeds an untouched board (revision 0),
// which does not count: before #2475 the board showed for everyone, so most hubs have that row.
func (d *DB) WorkspaceBoardInUse(ctx context.Context) (bool, error) {
	var revision int64
	err := d.pool.QueryRow(ctx, `SELECT revision FROM workspace_board WHERE id = true`).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return revision > 0, err
}

func decodeWorkspaceBoard(revision int64, data []byte) (workspaceboard.State, error) {
	var s workspaceboard.State
	if err := json.Unmarshal(data, &s); err != nil {
		return workspaceboard.State{}, fmt.Errorf("decode workspace board: %w", err)
	}
	s.Revision = revision
	if s.Cards == nil {
		s.Cards = []workspaceboard.Card{}
	}
	if err := workspaceboard.Validate(s); err != nil {
		return workspaceboard.State{}, fmt.Errorf("stored workspace board: %w", err)
	}
	return s, nil
}

// SaveWorkspaceBoard commits a revision and its metadata journal together. A stale
// writer or a failed journal insert cannot change either document or history.
func (d *DB) SaveWorkspaceBoard(ctx context.Context, expected int64, next workspaceboard.State) (workspaceboard.State, error) {
	if expected < 0 || expected == math.MaxInt64 {
		return workspaceboard.State{}, fmt.Errorf("%w: invalid revision", workspaceboard.ErrInvalid)
	}
	next.Revision = expected + 1
	if next.Cards == nil {
		next.Cards = []workspaceboard.Card{}
	}
	if err := workspaceboard.Validate(next); err != nil {
		return workspaceboard.State{}, err
	}
	data, err := json.Marshal(next)
	if err != nil {
		return workspaceboard.State{}, err
	}
	if err := d.seedWorkspaceBoard(ctx); err != nil {
		return workspaceboard.State{}, err
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return workspaceboard.State{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var revision int64
	var oldData []byte
	if err := tx.QueryRow(ctx, `SELECT revision, data FROM workspace_board WHERE id = true FOR UPDATE`).Scan(&revision, &oldData); err != nil {
		return workspaceboard.State{}, err
	}
	if revision != expected {
		return workspaceboard.State{}, ErrWorkspaceBoardConflict
	}
	previous, err := decodeWorkspaceBoard(revision, oldData)
	if err != nil {
		return workspaceboard.State{}, err
	}
	changes := workspaceboard.ActivityChanges(previous, next)
	activity, err := json.Marshal(changes)
	if err != nil {
		return workspaceboard.State{}, err
	}
	var at time.Time
	var saved []byte
	if err := tx.QueryRow(ctx, `UPDATE workspace_board SET revision = revision + 1, data = $2, updated_at = now()
  WHERE id = true AND revision = $1 RETURNING revision, data, updated_at`, expected, data).Scan(&revision, &saved, &at); err != nil {
		return workspaceboard.State{}, err
	}
	summary := workspaceboard.ActivitySummaryFor(revision, at, changes)
	if _, err := tx.Exec(ctx, `INSERT INTO workspace_board_activity
  (revision, created_at, change_count, card_count, kinds, card_ids, changes, encoded_bytes)
  VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, revision, at, summary.ChangeCount, summary.CardCount, summary.Kinds, workspaceboard.ActivityCardIDs(changes), activity, len(activity)); err != nil {
		return workspaceboard.State{}, err
	}
	// The board row lock serializes pruning with every write. Keep the newest
	// complete entry even when it alone exceeds the byte budget.
	if _, err := tx.Exec(ctx, `WITH retained AS (
    SELECT revision, row_number() OVER (ORDER BY revision DESC) AS position,
      sum(encoded_bytes) OVER (ORDER BY revision DESC) AS bytes
    FROM workspace_board_activity
  ) DELETE FROM workspace_board_activity WHERE revision IN (
    SELECT revision FROM retained WHERE position > 1 AND (position > $1 OR bytes > $2)
  )`, workspaceboard.ActivityRetentionLimit, workspaceboard.ActivityRetentionBytes); err != nil {
		return workspaceboard.State{}, err
	}
	result, err := decodeWorkspaceBoard(revision, saved)
	if err != nil {
		return workspaceboard.State{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return workspaceboard.State{}, err
	}
	return result, nil
}

type WorkspaceSnapshotRow struct {
	MachineID string
	Data      json.RawMessage
	UpdatedAt time.Time
}

func (d *DB) ListWorkspaceSnapshots(ctx context.Context) ([]WorkspaceSnapshotRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT machine_id, data, updated_at FROM workspace_snapshots ORDER BY machine_id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (WorkspaceSnapshotRow, error) {
		var s WorkspaceSnapshotRow
		err := r.Scan(&s.MachineID, &s.Data, &s.UpdatedAt)
		return s, err
	})
}

func (d *DB) SaveWorkspaceSnapshot(ctx context.Context, machineID string, data json.RawMessage) error {
	if strings.TrimSpace(machineID) == "" || strings.TrimSpace(machineID) != machineID || len(machineID) > workspaceboard.MaxIDBytes || !utf8.ValidString(machineID) || strings.ContainsFunc(machineID, unicode.IsControl) || len(data) > 4<<20 || !json.Valid(data) {
		return fmt.Errorf("%w: invalid workspace snapshot", workspaceboard.ErrInvalid)
	}
	_, err := d.pool.Exec(ctx, `INSERT INTO workspace_snapshots (machine_id, data) VALUES ($1, $2)
  ON CONFLICT (machine_id) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`, machineID, []byte(data))
	return err
}
