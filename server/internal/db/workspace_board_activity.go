package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/workspaceboard"
)

// WorkspaceBoardActivity returns compact summaries. Exclusive revision cursors
// never repeat entries when newer revisions arrive between pages. The metadata
// and page share one repeatable-read snapshot, including concurrent retention.
func (d *DB) WorkspaceBoardActivity(ctx context.Context, before int64, limit int, cardID string) (workspaceboard.ActivityPage, error) {
	if before < 0 || limit < 1 || limit > 50 || len(cardID) > workspaceboard.MaxIDBytes || !utf8.ValidString(cardID) || strings.TrimSpace(cardID) != cardID || strings.ContainsFunc(cardID, unicode.IsControl) {
		return workspaceboard.ActivityPage{}, fmt.Errorf("%w: invalid activity query", workspaceboard.ErrInvalid)
	}
	page := workspaceboard.ActivityPage{Entries: []workspaceboard.ActivitySummary{}, RetentionLimit: workspaceboard.ActivityRetentionLimit}
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var latest int64
	var oldest *int64
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT revision FROM workspace_board WHERE id = true), 0),
    (SELECT min(revision) FROM workspace_board_activity)`).Scan(&latest, &oldest); err != nil {
		return page, err
	}
	page.LatestRevision = strconv.FormatInt(latest, 10)
	if oldest != nil {
		page.OldestRevision = strconv.FormatInt(*oldest, 10)
	}
	rows, err := tx.Query(ctx, `SELECT revision, created_at, change_count, card_count, kinds
  FROM workspace_board_activity
  WHERE ($1::bigint = 0 OR revision < $1) AND ($2::text = '' OR $2 = ANY(card_ids))
  ORDER BY revision DESC LIMIT $3`, before, cardID, limit+1)
	if err != nil {
		return page, err
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (workspaceboard.ActivitySummary, error) {
		var entry workspaceboard.ActivitySummary
		var revision int64
		err := row.Scan(&revision, &entry.At, &entry.ChangeCount, &entry.CardCount, &entry.Kinds)
		entry.Revision = strconv.FormatInt(revision, 10)
		return entry, err
	})
	if err != nil {
		return page, err
	}
	if len(entries) > limit {
		entries = entries[:limit]
		page.NextCursor = entries[len(entries)-1].Revision
	}
	page.Entries = entries
	if page.Entries == nil {
		page.Entries = []workspaceboard.ActivitySummary{}
	}
	return page, tx.Commit(ctx)
}

// WorkspaceBoardActivityDetail reads only the requested part of an entry's JSON
// array; a full-board change never expands every card into one HTTP response.
func (d *DB) WorkspaceBoardActivityDetail(ctx context.Context, revision int64, offset, limit int) (workspaceboard.ActivityDetail, error) {
	if revision < 1 || offset < 0 || limit < 1 || limit > 100 || offset > 1_000_000 {
		return workspaceboard.ActivityDetail{}, fmt.Errorf("%w: invalid activity detail query", workspaceboard.ErrInvalid)
	}
	result := workspaceboard.ActivityDetail{Revision: strconv.FormatInt(revision, 10), Changes: []workspaceboard.ActivityChange{}}
	var data []byte
	err := d.pool.QueryRow(ctx, `SELECT created_at, jsonb_array_length(changes),
    coalesce((SELECT jsonb_agg(value ORDER BY ordinality)
      FROM jsonb_array_elements(changes) WITH ORDINALITY
      WHERE ordinality > $2 AND ordinality <= $2 + $3), '[]'::jsonb)
  FROM workspace_board_activity WHERE revision = $1`, revision, offset, limit).Scan(&result.At, &result.Total, &data)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result.Changes); err != nil {
		return result, err
	}
	if next := offset + len(result.Changes); next < result.Total {
		result.NextOffset = &next
	}
	return result, nil
}
