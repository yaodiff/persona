package teamdocs

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	defaultPageSize = 25
	maxPageSize     = 100
)

// approvedRevision carries the provenance needed to choose and later verify a pinned revision.
type approvedRevision struct {
	Project    string    `json:"project"`
	Document   string    `json:"document"`
	Number     int64     `json:"revision"`
	SHA256     string    `json:"sha256"`
	Author     string    `json:"author"`
	ApprovedBy string    `json:"approved_by"`
	ApprovedAt time.Time `json:"approved_at"`
}

type approvalPage struct {
	Items []approvedRevision `json:"items"`
	Next  string             `json:"next,omitempty"`
}

const approvedColumns = `project, document, revision, sha256, author, approved_by, approved_at`

func (store *Store) approvedDocuments(ctx context.Context, token, project, after string, limit int) (approvalPage, error) {
	if !storeID.MatchString(project) || after != "" && !storeID.MatchString(after) || limit < 1 || limit > maxPageSize {
		return approvalPage{}, ErrInvalid
	}
	// Walk the document index and take the most recent approval from each history.
	// A newer unapproved head still leaves its predecessor visible.
	items, err := store.readApprovals(ctx, token, project, `
		SELECT approved.* FROM teamdocs_heads h
		CROSS JOIN LATERAL (
			SELECT `+approvedColumns+` FROM teamdocs_revisions r
			WHERE r.project = h.project AND r.document = h.document AND approved_by IS NOT NULL
			ORDER BY revision DESC LIMIT 1
		) approved
		WHERE h.project = $1 AND h.document COLLATE "C" > $2
		ORDER BY h.document COLLATE "C" LIMIT $3`, project, after, limit+1)
	if err != nil {
		return approvalPage{}, err
	}
	page := approvalPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.Next = page.Items[limit-1].Document
	}
	return page, nil
}

func (store *Store) approvalHistory(ctx context.Context, token, project, document string, before int64, limit int) (approvalPage, error) {
	if !storeID.MatchString(project) || !storeID.MatchString(document) || before < 0 || limit < 1 || limit > maxPageSize {
		return approvalPage{}, ErrInvalid
	}
	ceiling := int64(math.MaxInt64)
	if before > 0 {
		ceiling = before - 1
	}
	items, err := store.readApprovals(ctx, token, project, `
		SELECT `+approvedColumns+` FROM teamdocs_revisions
		WHERE project = $1 AND document = $2 AND revision <= $3 AND approved_by IS NOT NULL
		ORDER BY revision DESC LIMIT $4`, project, document, ceiling, limit+1)
	if err != nil {
		return approvalPage{}, err
	}
	page := approvalPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.Next = strconv.FormatInt(page.Items[limit-1].Number, 10)
	}
	return page, nil
}

func (store *Store) readApprovals(ctx context.Context, token, project, query string, args ...any) ([]approvedRevision, error) {
	var items []approvedRevision
	err := store.withMember(ctx, token, project, roleReader, func(tx pgx.Tx, _ string) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, pgx.RowToStructByPos[approvedRevision])
		return err
	})
	return items, err
}
