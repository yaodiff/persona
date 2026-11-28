package db

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/larksyncwire"
)

// The Lark user sync's tables (migrations/manager/20261125053132_lark_sync.sql; internal/modules/lark/larksync): each
// binding's cursors, the dedup index across bindings and the outbox the knowledge base's larksync connector reads.
// A run is committed in one transaction (CommitLarkSyncRun), so content, cursors and index move together or not at all.

// LarkSyncOutbox is one row of the outbox: a chat batch (Kind "chat") or a document (Kind "doc"), its payload the
// JSON the connector hands the knowledge base as it is.
type LarkSyncOutbox = larksyncwire.OutboxRow

// Outbox kinds.
const (
	LarkSyncChat = larksyncwire.OutboxChat
	LarkSyncDoc  = larksyncwire.OutboxDoc
)

// LarkSyncCursors are a binding's cursors by resource.
func (d *DB) LarkSyncCursors(ctx context.Context, binding string) (map[string]string, error) {
	rows, err := d.pool.Query(ctx, `SELECT resource, cursor FROM lark_sync_cursors WHERE binding_key = $1`, binding)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var res, cur string
		if err := rows.Scan(&res, &cur); err != nil {
			return nil, err
		}
		out[res] = cur
	}
	return out, rows.Err()
}

// DeleteLarkSyncCursors drops a binding's cursors (the binding was removed: what it read stays in the knowledge base).
func (d *DB) DeleteLarkSyncCursors(ctx context.Context, binding string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM lark_sync_cursors WHERE binding_key = $1`, binding)
	return err
}

// LarkSyncIndex looks up the index's records of keys; a key without one is absent from the map.
func (d *DB) LarkSyncIndex(ctx context.Context, keys []string) (map[string]larksyncwire.IndexRecord, error) {
	out := map[string]larksyncwire.IndexRecord{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT canonical_key, kind, revision, body_hash, sources, updated_at
		FROM lark_sync_index WHERE canonical_key = ANY($1)`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanLarkSyncIndex(rows)
		if err != nil {
			return nil, err
		}
		out[r.Key] = r
	}
	return out, rows.Err()
}

func scanLarkSyncIndex(r pgx.Row) (larksyncwire.IndexRecord, error) {
	var rec larksyncwire.IndexRecord
	var sources []byte
	if err := r.Scan(&rec.Key, &rec.Kind, &rec.Revision, &rec.BodyHash, &sources, &rec.Updated); err != nil {
		return rec, err
	}
	if len(sources) > 0 {
		if err := json.Unmarshal(sources, &rec.Sources); err != nil {
			return rec, err
		}
	}
	return rec, nil
}

// PutLarkSyncIndex merges recs into the index (mergeLarkSyncIndex) on its own.
func (d *DB) PutLarkSyncIndex(ctx context.Context, recs []larksyncwire.IndexRecord) error {
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error { return mergeLarkSyncIndex(ctx, tx, recs) })
}

// CommitLarkSyncRun commits one binding's run at once: its cursors (upserted; resources not named keep theirs), the
// index records it staged (merged) and its outbox rows. A failure leaves everything as it was.
func (d *DB) CommitLarkSyncRun(ctx context.Context, binding string, cursors map[string]string, recs []larksyncwire.IndexRecord, outbox []LarkSyncOutbox) error {
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		resources := make([]string, 0, len(cursors))
		for res := range cursors {
			resources = append(resources, res)
		}
		sort.Strings(resources) // one lock order whoever commits
		for _, res := range resources {
			if _, err := tx.Exec(ctx, `INSERT INTO lark_sync_cursors (binding_key, resource, cursor, updated_at) VALUES ($1, $2, $3, now())
				ON CONFLICT (binding_key, resource) DO UPDATE SET cursor = EXCLUDED.cursor, updated_at = now()`,
				binding, res, cursors[res]); err != nil {
				return err
			}
		}
		if err := mergeLarkSyncIndex(ctx, tx, recs); err != nil {
			return err
		}
		for _, row := range outbox {
			if row.Kind != LarkSyncChat && row.Kind != LarkSyncDoc {
				return errors.New("lark sync outbox: unknown kind " + string(row.Kind))
			}
			if _, err := tx.Exec(ctx, `INSERT INTO lark_sync_outbox (kind, canonical_key, payload) VALUES ($1, $2, $3)`,
				string(row.Kind), row.CanonicalKey, []byte(row.Payload)); err != nil {
				return err
			}
		}
		return nil
	})
}

// mergeLarkSyncIndex writes recs over the index rows of their keys, locked for the transaction: the sources are
// merged (larksyncwire.MergeSources) and the revision and body hash only ever move forward (larksyncwire.Decide), so
// two bindings committing the same content in either order end with the newest revision and both sources.
func mergeLarkSyncIndex(ctx context.Context, tx pgx.Tx, recs []larksyncwire.IndexRecord) error {
	if len(recs) == 0 {
		return nil
	}
	byKey := map[string]larksyncwire.IndexRecord{}
	for _, r := range recs {
		if r.Key == "" {
			continue
		}
		if have, ok := byKey[r.Key]; ok {
			r = mergeLarkSyncRecord(have, true, r)
		}
		byKey[r.Key] = r
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows, err := tx.Query(ctx, `SELECT canonical_key, kind, revision, body_hash, sources, updated_at
		FROM lark_sync_index WHERE canonical_key = ANY($1) ORDER BY canonical_key FOR UPDATE`, keys)
	if err != nil {
		return err
	}
	old := map[string]larksyncwire.IndexRecord{}
	for rows.Next() {
		r, err := scanLarkSyncIndex(rows)
		if err != nil {
			rows.Close()
			return err
		}
		old[r.Key] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range keys {
		have, ok := old[k]
		r := mergeLarkSyncRecord(have, ok, byKey[k])
		sources, err := json.Marshal(r.Sources)
		if err != nil {
			return err
		}
		if r.Sources == nil {
			sources = []byte(`[]`)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO lark_sync_index (canonical_key, kind, revision, body_hash, sources, updated_at)
			VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (canonical_key) DO UPDATE SET kind = EXCLUDED.kind, revision = EXCLUDED.revision,
				body_hash = EXCLUDED.body_hash, sources = EXCLUDED.sources, updated_at = now()`,
			r.Key, r.Kind, r.Revision, r.BodyHash, sources); err != nil {
			return err
		}
	}
	return nil
}

// mergeLarkSyncRecord is the record to keep when add arrives for a key whose stored record is have (ok false: none).
func mergeLarkSyncRecord(have larksyncwire.IndexRecord, ok bool, add larksyncwire.IndexRecord) larksyncwire.IndexRecord {
	if !ok {
		return add
	}
	out := have
	switch larksyncwire.Decide(have, true, add.Revision, add.BodyHash) {
	case larksyncwire.DecideNewer:
		out.Revision, out.BodyHash = add.Revision, add.BodyHash
	case larksyncwire.DecideSame:
		if out.BodyHash == "" {
			out.BodyHash = add.BodyHash
		}
	}
	if out.Kind == "" {
		out.Kind = add.Kind
	}
	out.Sources = larksyncwire.MergeSources(have.Sources, add.Sources)
	return out
}

// LarkSyncOutboxAfter are the outbox rows of kind after the sequence after, oldest first, at most limit.
func (d *DB) LarkSyncOutboxAfter(ctx context.Context, kind larksyncwire.OutboxKind, after int64, limit int) ([]LarkSyncOutbox, error) {
	if limit < 1 {
		limit = 100
	}
	rows, err := d.pool.Query(ctx, `SELECT seq, kind, canonical_key, payload, created_at FROM lark_sync_outbox
		WHERE kind = $1 AND seq > $2 ORDER BY seq LIMIT $3`, string(kind), after, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanLarkSyncOutbox)
}

// LarkSyncDocsSince are the newest 'doc' row of each canonical key written since since.
func (d *DB) LarkSyncDocsSince(ctx context.Context, since time.Time) ([]LarkSyncOutbox, error) {
	rows, err := d.pool.Query(ctx, `SELECT DISTINCT ON (canonical_key) seq, kind, canonical_key, payload, created_at
		FROM lark_sync_outbox WHERE kind = 'doc' AND created_at >= $1 ORDER BY canonical_key, seq DESC`, since)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, scanLarkSyncOutbox)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func scanLarkSyncOutbox(r pgx.CollectableRow) (LarkSyncOutbox, error) {
	var o LarkSyncOutbox
	var payload []byte
	err := r.Scan(&o.Seq, &o.Kind, &o.CanonicalKey, &payload, &o.CreatedAt)
	o.Payload = payload
	return o, err
}

// PruneLarkSyncOutbox deletes the outbox rows written before before and says how many there were.
func (d *DB) PruneLarkSyncOutbox(ctx context.Context, before time.Time) (int64, error) {
	tag, err := d.pool.Exec(ctx, `DELETE FROM lark_sync_outbox WHERE created_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
