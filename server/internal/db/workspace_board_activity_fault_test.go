package db

import (
	"context"
	"reflect"
	"slices"
	"testing"
)

// Exercise failures before the document write, after it, during retention, and at
// COMMIT. A successful in-memory diff is not evidence that both SQL writes commit.
func TestWorkspaceBoardActivityTransactionFaults(t *testing.T) {
	for _, stage := range []string{"document", "journal", "retention", "commit"} {
		t.Run(stage, func(t *testing.T) {
			d, _ := openBoardActivity(t)
			ctx := context.Background()
			initial, err := d.WorkspaceBoard(ctx)
			if err != nil {
				t.Fatal(err)
			}
			before := saveActivityCard(t, d, initial, "a", "Original name")
			if stage == "retention" {
				if _, err := d.pool.Exec(ctx, `INSERT INTO workspace_board_activity(revision,change_count,card_count,kinds,card_ids,changes,encoded_bytes)
 SELECT n,0,0,'{}','{}','[]',2 FROM generate_series(2,500)n; UPDATE workspace_board SET revision=500 WHERE id=true`); err != nil {
					t.Fatal(err)
				}
				before.Revision = 500
			}
			if _, err := d.pool.Exec(ctx, `CREATE FUNCTION reject_activity_transaction() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected activity fault'; END $$`); err != nil {
				t.Fatal(err)
			}
			trigger := map[string]string{
				"document":  `CREATE TRIGGER reject_activity_transaction BEFORE UPDATE ON workspace_board FOR EACH ROW EXECUTE FUNCTION reject_activity_transaction()`,
				"journal":   `CREATE TRIGGER reject_activity_transaction BEFORE INSERT ON workspace_board_activity FOR EACH ROW EXECUTE FUNCTION reject_activity_transaction()`,
				"retention": `CREATE TRIGGER reject_activity_transaction BEFORE DELETE ON workspace_board_activity FOR EACH ROW EXECUTE FUNCTION reject_activity_transaction()`,
				"commit":    `CREATE CONSTRAINT TRIGGER reject_activity_transaction AFTER INSERT ON workspace_board_activity DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_activity_transaction()`,
			}[stage]
			if _, err := d.pool.Exec(ctx, trigger); err != nil {
				t.Fatal(err)
			}
			next := before
			next.Cards = slices.Clone(before.Cards)
			next.Cards[0].Title = "Uncommitted name"
			if _, err := d.SaveWorkspaceBoard(ctx, before.Revision, next); err == nil {
				t.Fatal("fault did not abort save")
			}
			after, err := d.WorkspaceBoard(ctx)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("document changed: %+v %v", after, err)
			}
			var count, max int64
			if err := d.pool.QueryRow(ctx, `SELECT count(*),max(revision) FROM workspace_board_activity`).Scan(&count, &max); err != nil {
				t.Fatal(err)
			}
			if count != before.Revision || max != before.Revision {
				t.Fatalf("journal changed: count=%d max=%d, expected %d", count, max, before.Revision)
			}
		})
	}
}
