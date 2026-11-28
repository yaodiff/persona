package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// What an extension may read of the assistant's sensitive information (#13246, migrations/manager/
// 20261122225047_assistant_extension_trust.sql). The manager checks the values; the store keeps what it is given.

// ExtensionTrust is one extension's: whether it is the user's own, and the sensitive categories it was granted.
type ExtensionTrust struct {
	Extension  string
	Trust      string // "owner" or "third_party"; "" when no row says
	Categories []string
}

// ExtensionTrustOf reads one extension's trust level. An absent row has an empty level.
func (d *DB) ExtensionTrustOf(ctx context.Context, extension string) (string, error) {
	var trust string
	err := d.pool.QueryRow(ctx, `SELECT trust FROM assistant_extension_trust WHERE extension_id = $1`, extension).Scan(&trust)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return trust, err
}

// ExtensionTrusts are every extension a trust or a grant was kept for, by extension id.
func (d *DB) ExtensionTrusts(ctx context.Context) (map[string]ExtensionTrust, error) {
	out := map[string]ExtensionTrust{}
	rows, err := d.pool.Query(ctx, `SELECT extension_id, trust FROM assistant_extension_trust`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t ExtensionTrust
		if err := rows.Scan(&t.Extension, &t.Trust); err != nil {
			rows.Close()
			return nil, err
		}
		out[t.Extension] = t
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = d.pool.Query(ctx, `SELECT extension_id, category FROM assistant_extension_grants ORDER BY extension_id, category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ext, cat string
		if err := rows.Scan(&ext, &cat); err != nil {
			return nil, err
		}
		t := out[ext]
		t.Extension = ext
		t.Categories = append(t.Categories, cat)
		out[ext] = t
	}
	return out, rows.Err()
}

// ForgetExtensionTrust drops extension's trust and grants (it was removed).
func (d *DB) ForgetExtensionTrust(ctx context.Context, extension string) error {
	if _, err := d.pool.Exec(ctx, `DELETE FROM assistant_extension_grants WHERE extension_id = $1`, extension); err != nil {
		return err
	}
	_, err := d.pool.Exec(ctx, `DELETE FROM assistant_extension_trust WHERE extension_id = $1`, extension)
	return err
}

// SetExtensionTrust keeps extension's trust and replaces its granted categories with cats, as by says (the user).
func (d *DB) SetExtensionTrust(ctx context.Context, extension, trust string, cats []string, by string) error {
	if extension == "" {
		return fmt.Errorf("extension trust: no extension")
	}
	if cats == nil {
		cats = []string{} // NULL would match no row to delete
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO assistant_extension_trust (extension_id, trust) VALUES ($1, $2)
		ON CONFLICT (extension_id) DO UPDATE SET trust = EXCLUDED.trust, updated_at = now()`, extension, trust); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM assistant_extension_grants WHERE extension_id = $1 AND NOT (category = ANY($2::text[]))`,
		extension, cats); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO assistant_extension_grants (extension_id, category, granted_by)
		SELECT $1, c, $3 FROM unnest($2::text[]) AS c ON CONFLICT (extension_id, category) DO NOTHING`, extension, cats, by); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
