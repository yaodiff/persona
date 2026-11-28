package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrAssistantPageVersionNotFound = errors.New("assistant page version is no longer available")

const assistantPageVersionSummary = `page_id, version, title, by_kind, by_machine, by_session, by_device, note, at`
const assistantPageVersionColumns = assistantPageVersionSummary + ", view"

func scanAssistantPageVersion(row pgx.Row) (AssistantPageVersionRow, error) {
	var v AssistantPageVersionRow
	err := row.Scan(&v.PageID, &v.Version, &v.Title, &v.By.Kind, &v.By.Machine, &v.By.Session, &v.By.Device,
		&v.By.Note, &v.At, &v.View)
	return v, err
}

// AssistantPageVersions are a page's retained versions including their complete views, latest first.
func (d *DB) AssistantPageVersions(ctx context.Context, id string) ([]AssistantPageVersionRow, error) {
	return d.assistantPageVersions(ctx, id, assistantPageVersionColumns)
}

// AssistantPageVersionSummaries lists a page's history without loading the views.
func (d *DB) AssistantPageVersionSummaries(ctx context.Context, id string) ([]AssistantPageVersionRow, error) {
	return d.assistantPageVersions(ctx, id, assistantPageVersionSummary+", NULL::jsonb")
}

func (d *DB) assistantPageVersions(ctx context.Context, id, columns string) ([]AssistantPageVersionRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT `+columns+` FROM assistant_page_versions WHERE page_id = $1 ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AssistantPageVersionRow, error) {
		return scanAssistantPageVersion(row)
	})
}

// AssistantPageVersion reads one retained snapshot for preview.
func (d *DB) AssistantPageVersion(ctx context.Context, id string, version int64) (*AssistantPageVersionRow, error) {
	v, err := scanAssistantPageVersion(d.pool.QueryRow(ctx, `SELECT `+assistantPageVersionColumns+`
		FROM assistant_page_versions WHERE page_id = $1 AND version = $2`, id, version))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAssistantPageVersionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// RestoreAssistantPageVersion records the chosen title and view as a new owner-initiated version.
// The title reservation, head check, pause and snapshot all take effect in the same transaction.
func (d *DB) RestoreAssistantPageVersion(ctx context.Context, id string, expected, version int64, device string, at time.Time) (*AssistantPageRow, error) {
	if id == "" || at.IsZero() {
		return nil, errors.New("restoring a page version needs a page id and a time")
	}
	at = at.UTC().Truncate(time.Microsecond)
	var restored *AssistantPageRow
	err := d.pagesTx(ctx, true, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := old.kept(); err != nil {
			return err
		}
		if old.Version != expected || version < 1 || version >= expected {
			return ErrAssistantPageVersion
		}
		var title string
		err = tx.QueryRow(ctx, `SELECT title FROM assistant_page_versions WHERE page_id = $1 AND version = $2`, id, version).Scan(&title)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAssistantPageVersionNotFound
		}
		if err != nil {
			return err
		}
		key := AssistantPageTitleKey(title)
		if err := titleTaken(ctx, tx, key, id); err != nil {
			return err
		}

		// History stores the title and view. Keep the current watch, readings and owner settings.
		row, err := scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET
			title = $3, title_key = $4, view = (SELECT view FROM assistant_page_versions WHERE page_id = $1 AND version = $2),
			version = version + 1, paused = true, paused_by = 'restore', refresh_asked_at = NULL, refresh_device = '',
			updated_at = $5, content_at = $5 WHERE id = $1 RETURNING `+assistantPageColumns, id, version, title, key, at), true)
		if err != nil {
			return err
		}
		if err := writeAssistantPageVersion(ctx, tx, row, AssistantPageWriter{Kind: AssistantPageByRestore, Device: device}, at); err != nil {
			return err
		}
		restored = row
		return nil
	})
	if err != nil {
		return nil, mapTitleConflict(err)
	}
	return restored, nil
}
