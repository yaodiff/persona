package repositorystore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/assistant/repository"
)

const repoMirrorColumns = `machine_id, repo_id, enabled, auto_sync, sync_interval_minutes,
	last_synced_at, last_head_commit, last_default_ref, last_error, disk_bytes,
	consecutive_failures, next_sync_at, updated_at`

func scanRepoMirror(row pgx.Row) (repository.RepoMirrorRecord, error) {
	var r repository.RepoMirrorRecord
	err := row.Scan(
		&r.MachineID, &r.RepoID, &r.Enabled, &r.AutoSync, &r.SyncIntervalMinutes,
		&r.LastSyncedAt, &r.LastHeadCommit, &r.LastDefaultRef, &r.LastError, &r.DiskBytes,
		&r.ConsecutiveFailures, &r.NextSyncAt, &r.UpdatedAt,
	)
	return r, err
}

// RepoMirror reads the mirror configuration and sync status for one (machine, repo_id) pair.
// Returns (nil, nil) when the pair has no saved settings.
func (d *Store) RepoMirror(ctx context.Context, machineID, repoID string) (*repository.RepoMirrorRecord, error) {
	rec, err := scanRepoMirror(d.QueryRow(ctx, `SELECT `+repoMirrorColumns+` FROM repo_mirrors WHERE machine_id=$1 AND repo_id=$2`, machineID, repoID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// ListRepoMirrors returns mirror records filtered by optional machineID and/or repoID.
func (d *Store) ListRepoMirrors(ctx context.Context, machineID, repoID string) ([]repository.RepoMirrorRecord, error) {
	var clauses []string
	var args []any
	if machineID != "" {
		args = append(args, machineID)
		clauses = append(clauses, "machine_id=$"+itoa(len(args)))
	}
	if repoID != "" {
		args = append(args, repoID)
		clauses = append(clauses, "repo_id=$"+itoa(len(args)))
	}
	query := `SELECT ` + repoMirrorColumns + ` FROM repo_mirrors`
	if len(clauses) > 0 {
		query += ` WHERE ` + strings.Join(clauses, " AND ")
	}
	query += ` ORDER BY machine_id ASC, repo_id ASC`
	out := []repository.RepoMirrorRecord{}
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRepoMirror(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// CreateOnDemandRepoMirror enrolls an unconfigured pair and returns its saved settings.
// An explicit configuration or another enrollment can win the insert race.
func (d *Store) CreateOnDemandRepoMirror(ctx context.Context, machineID, repoID string, now time.Time) (*repository.RepoMirrorRecord, error) {
	if !repository.ValidateMirrorMachineID(machineID) || !repository.ValidateMirrorRepoID(repoID) {
		return nil, repository.Problem("invalid")
	}
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO repo_mirrors
			(machine_id, repo_id, enabled, auto_sync, sync_interval_minutes, updated_at)
			VALUES ($1,$2,TRUE,FALSE,$3,$4)
			ON CONFLICT (machine_id, repo_id) DO NOTHING`, machineID, repoID, repository.DefaultMirrorSyncIntervalMinutes, now.UTC())
		return err
	})
	if err != nil {
		return nil, err
	}
	return d.RepoMirror(ctx, machineID, repoID)
}

// ListDueAutoSyncRepoMirrors returns enabled auto-sync mirrors whose next_sync_at is unset or at/before now.
func (d *Store) ListDueAutoSyncRepoMirrors(ctx context.Context, now time.Time) ([]repository.RepoMirrorRecord, error) {
	out := []repository.RepoMirrorRecord{}
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+repoMirrorColumns+`
			FROM repo_mirrors
			WHERE enabled = TRUE AND auto_sync = TRUE AND (next_sync_at IS NULL OR next_sync_at <= $1)
			ORDER BY COALESCE(next_sync_at, updated_at) ASC`, now.UTC())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRepoMirror(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// UpsertRepoMirrorConfig saves per-(machine, repo_id) mirror preferences while preserving prior sync observations.
func (d *Store) UpsertRepoMirrorConfig(ctx context.Context, cfg repository.RepoMirrorRecord) (repository.RepoMirrorRecord, error) {
	if !repository.ValidateMirrorMachineID(cfg.MachineID) || !repository.ValidateMirrorRepoID(cfg.RepoID) {
		return repository.RepoMirrorRecord{}, repository.Problem("invalid")
	}
	if cfg.AutoSync && !cfg.Enabled {
		return repository.RepoMirrorRecord{}, repository.Problem("invalid")
	}
	interval, err := repository.NormalizeMirrorSyncIntervalMinutes(cfg.SyncIntervalMinutes)
	if err != nil {
		return repository.RepoMirrorRecord{}, err
	}
	now := cfg.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var out repository.RepoMirrorRecord
	err = d.Transaction(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `INSERT INTO repo_mirrors
			(machine_id, repo_id, enabled, auto_sync, sync_interval_minutes, next_sync_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (machine_id, repo_id) DO UPDATE SET
				enabled = EXCLUDED.enabled,
				auto_sync = EXCLUDED.auto_sync,
				sync_interval_minutes = EXCLUDED.sync_interval_minutes,
				next_sync_at = EXCLUDED.next_sync_at,
				consecutive_failures = CASE WHEN EXCLUDED.auto_sync = FALSE OR EXCLUDED.enabled = FALSE THEN 0 ELSE repo_mirrors.consecutive_failures END,
				updated_at = EXCLUDED.updated_at
			RETURNING `+repoMirrorColumns,
			cfg.MachineID, cfg.RepoID, cfg.Enabled, cfg.AutoSync, interval, cfg.NextSyncAt, now)
		var scanErr error
		out, scanErr = scanRepoMirror(row)
		return scanErr
	})
	return out, err
}

// SaveRepoMirrorSyncResult persists the latest sync observation and next schedule for an existing mirror row.
func (d *Store) SaveRepoMirrorSyncResult(ctx context.Context, machineID, repoID string, res repository.RepoMirrorSyncUpdate) error {
	now := res.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return d.Transaction(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE repo_mirrors SET
			last_synced_at = COALESCE($3, last_synced_at),
			last_head_commit = CASE WHEN $4 <> '' THEN $4 ELSE last_head_commit END,
			last_default_ref = CASE WHEN $5 <> '' THEN $5 ELSE last_default_ref END,
			last_error = $6,
			disk_bytes = CASE WHEN $7 THEN $8 ELSE disk_bytes END,
			consecutive_failures = $9,
			next_sync_at = $10,
			updated_at = $11
			WHERE machine_id = $1 AND repo_id = $2`,
			machineID, repoID, res.LastSyncedAt, res.LastHeadCommit, res.LastDefaultRef,
			res.LastError, res.HasDiskBytes, res.DiskBytes, res.ConsecutiveFailures, res.NextSyncAt, now)
		return err
	})
}

// DeleteRepoMirror deletes the persisted mirror configuration and status for (machine_id, repo_id).
func (d *Store) DeleteRepoMirror(ctx context.Context, machineID, repoID string) error {
	return d.Transaction(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM repo_mirrors WHERE machine_id=$1 AND repo_id=$2`, machineID, repoID)
		return err
	})
}

func itoa(n int) string {
	switch n {
	case 1:
		return "1"
	case 2:
		return "2"
	default:
		return "3"
	}
}
