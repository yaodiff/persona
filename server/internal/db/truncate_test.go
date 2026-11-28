package db

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestTruncateForTestDiscoversMigrationTables(t *testing.T) {
	ctx, d := t.Context(), open(t)
	rows, err := d.pool.Query(ctx, `SELECT format('%I.%I', table_schema, table_name)
		FROM information_schema.tables
		WHERE table_type = 'BASE TABLE' AND table_schema NOT IN ('pg_catalog', 'information_schema')
			AND table_name <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	want, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.tablesToTruncateForTest(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("discovered tables %v, want %v", got, want)
	}
	t.Logf("discovered all %d migrated business tables", len(want))
}

func TestTruncateForTestClearsIndependentTables(t *testing.T) {
	ctx := t.Context()
	d, err := Open(ctx, migratedDatabase(t, AllSets), AllSets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)

	if _, err := d.pool.Exec(ctx, `
		INSERT INTO task_plans (id, conversation, tasks) VALUES ('plan', 'conversation', '[]');
		INSERT INTO task_briefs (id, brief) VALUES ('brief', '{}');
		INSERT INTO agent_placements (id, task_id, machine, session_id, started_at, decision)
			VALUES ('placement', 'task', 'machine', 'session', now(), '{}');
		CREATE SCHEMA "Test.Schema";
		CREATE TABLE "Test.Schema"."Work""Log" (id int);
		CREATE TABLE public."Work""Log" (id int);
		INSERT INTO "Test.Schema"."Work""Log" VALUES (1);
		INSERT INTO public."Work""Log" VALUES (2)
	`); err != nil {
		t.Fatal(err)
	}

	migrationRecords := func() string {
		t.Helper()
		var records string
		err := d.pool.QueryRow(ctx, `SELECT coalesce(
			jsonb_agg(to_jsonb(m) ORDER BY version, name, part), '[]')::text FROM schema_migrations m`).Scan(&records)
		if err != nil {
			t.Fatal(err)
		}
		return records
	}
	before := migrationRecords()
	if before == "[]" {
		t.Fatal("expected the migrated fixture to have migration records")
	}
	if err := d.TruncateForTest(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []pgx.Identifier{
		{"public", "task_plans"},
		{"public", "task_briefs"},
		{"public", "agent_placements"},
		{"Test.Schema", `Work"Log`},
		{"public", `Work"Log`},
	} {
		var hasRows bool
		if err := d.pool.QueryRow(ctx, "SELECT EXISTS (SELECT FROM "+table.Sanitize()+")").Scan(&hasRows); err != nil {
			t.Fatal(err)
		}
		if hasRows {
			t.Errorf("%s retained rows after TruncateForTest", table.Sanitize())
		}
	}
	if migrationRecords() != before {
		t.Error("TruncateForTest changed the migration ledger")
	}
}

func TestTruncateForTestSkipsUnrelatedEmptyTables(t *testing.T) {
	ctx, d := t.Context(), open(t)
	if _, err := d.pool.Exec(ctx, `INSERT INTO todos (id, title) VALUES ('t1', 'x');
		INSERT INTO todo_decisions (id, todo_id, question, choice, created_at) VALUES ('d1', 't1', 'q', 'c', now());
		INSERT INTO task_plans (id, conversation, tasks) VALUES ('removed', 'conversation', '[]');
		DELETE FROM task_plans WHERE id = 'removed'`); err != nil {
		t.Fatal(err)
	}
	var hasPages bool
	if err := d.pool.QueryRow(ctx, `SELECT pg_relation_size('task_plans') > 0`).Scan(&hasPages); err != nil || !hasPages {
		t.Fatalf("empty task_plans must have pages so the row probe runs: %v", err)
	}
	lock, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, `LOCK TABLE task_plans IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	resetCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := d.TruncateForTest(resetCtx); err != nil {
		t.Fatalf("reset with a writer lock on empty task_plans: %v", err)
	}
	var rows int
	if err := d.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM todos) + (SELECT count(*) FROM todo_decisions)`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("%d rows left in todos and todo_decisions, err=%v", rows, err)
	}
}
