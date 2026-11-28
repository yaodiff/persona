package db

import (
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestManagerStorageChecks(t *testing.T) {
	ctx := t.Context()
	d, err := Open(ctx, freshDatabase(t), ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if _, err := d.pool.Exec(ctx, `INSERT INTO todos (id, title) VALUES ('todo', 'Example')`); err != nil {
		t.Fatal(err)
	}

	t.Run("defaults", func(t *testing.T) {
		for _, query := range []string{
			`INSERT INTO task_briefs (id, brief) VALUES ('default', '{}') RETURNING state = '{}'::jsonb`,
			`INSERT INTO todo_pr_followups (followup_id, todo_id, pr_url, supervisor, created_at)
				VALUES ('default', 'todo', 'https://example.com/pull/default', 'owner', now()) RETURNING state = '' AND watch_status = ''`,
			`INSERT INTO file_transfer_audit (id, from_machine, to_machine, state) VALUES ('default', 'source', 'target', 'failed')
				RETURNING caller_kind = '' AND kind = ''`,
		} {
			var ok bool
			if err := d.pool.QueryRow(ctx, query).Scan(&ok); err != nil || !ok {
				t.Fatalf("defaults for %s: accepted=%v err=%v", query, ok, err)
			}
		}
	})

	badJSON := []string{`[]`, `null`, `false`, `42`, `"text"`}
	for _, tc := range []struct {
		constraint, insert, update string
		valid, invalid             []string
	}{
		{
			"task_briefs_brief_shape",
			`INSERT INTO task_briefs (id, brief) VALUES ($1, $2::jsonb)`,
			`UPDATE task_briefs SET brief = $2::jsonb WHERE id = $1`,
			[]string{`{}`, `{"goal":"Example"}`}, badJSON,
		},
		{
			"task_briefs_state_shape",
			`INSERT INTO task_briefs (id, brief, state) VALUES ($1, '{}', $2::jsonb)`,
			`UPDATE task_briefs SET state = $2::jsonb WHERE id = $1`,
			[]string{`{}`, `{"log":[]}`}, badJSON,
		},
		{
			"todo_pr_followups_state_check",
			`INSERT INTO todo_pr_followups (followup_id, todo_id, pr_url, supervisor, created_at, state)
				VALUES ($1, 'todo', 'https://example.com/pull/' || $1, 'owner', now(), $2)`,
			`UPDATE todo_pr_followups SET state = $2 WHERE followup_id = $1`,
			[]string{"", repository.ReviewOpen, repository.ReviewClosed, repository.ReviewMerged}, []string{"unknown"},
		},
		{
			"todo_pr_followups_watch_status_check",
			`INSERT INTO todo_pr_followups (followup_id, todo_id, pr_url, supervisor, created_at, watch_status)
				VALUES ($1, 'todo', 'https://example.com/pull/' || $1, 'owner', now(), $2)`,
			`UPDATE todo_pr_followups SET watch_status = $2 WHERE followup_id = $1`,
			[]string{"", repository.PRFollowupWatchActive, repository.PRFollowupWatchHostNotConnected},
			[]string{repository.PRFollowupReasonUnsupportedHost},
		},
		{
			"file_transfer_audit_caller_kind_check",
			`INSERT INTO file_transfer_audit (id, from_machine, to_machine, state, caller_kind) VALUES ($1, 'source', 'target', 'done', $2)`,
			`UPDATE file_transfer_audit SET caller_kind = $2 WHERE id = $1`,
			[]string{"", "agent", "assistant"}, []string{"unknown"},
		},
		{
			"file_transfer_audit_kind_check",
			`INSERT INTO file_transfer_audit (id, from_machine, to_machine, state, kind) VALUES ($1, 'source', 'target', 'failed', $2)`,
			`UPDATE file_transfer_audit SET kind = $2 WHERE id = $1`,
			[]string{"", proto.FilePackFile, proto.FilePackDir}, []string{"unknown"},
		},
		{
			"file_transfer_audit_state_check",
			`INSERT INTO file_transfer_audit (id, from_machine, to_machine, state) VALUES ($1, 'source', 'target', $2)`,
			`UPDATE file_transfer_audit SET state = $2 WHERE id = $1`,
			[]string{proto.TransferDone, proto.TransferFailed}, []string{"", "refused"},
		},
		{
			"held_operations_kind_check",
			`INSERT INTO held_operations (op_id, machine_id, kind, call) VALUES ($1, 'machine', $2, '{}')`,
			`UPDATE held_operations SET kind = $2 WHERE op_id = $1`,
			[]string{HeldKindMessage, HeldKindStart}, []string{"", "unknown"},
		},
		{
			"held_operations_call_shape",
			`INSERT INTO held_operations (op_id, machine_id, kind, call) VALUES ($1, 'machine', 'message', $2::jsonb)`,
			`UPDATE held_operations SET call = $2::jsonb WHERE op_id = $1`,
			[]string{`{}`, `{"method":"session.send","params":{"text":"Example"}}`, `{"meta":{"title":"Example"}}`}, badJSON,
		},
	} {
		t.Run(tc.constraint, func(t *testing.T) {
			for i, value := range tc.valid {
				id := fmt.Sprintf("%s-%d", tc.constraint, i)
				if _, err := d.pool.Exec(ctx, tc.insert, id, value); err != nil {
					t.Fatalf("insert %q: %v", value, err)
				}
				if tag, err := d.pool.Exec(ctx, tc.update, id, value); err != nil || tag.RowsAffected() != 1 {
					t.Fatalf("update %q: %s, %v", value, tag, err)
				}
			}
			for _, value := range tc.invalid {
				_, err := d.pool.Exec(ctx, tc.insert, tc.constraint+"-invalid", value)
				requireStorageCheckViolation(t, err, tc.constraint)
				_, err = d.pool.Exec(ctx, tc.update, tc.constraint+"-0", value)
				requireStorageCheckViolation(t, err, tc.constraint)
			}
		})
	}
}

func TestManagerStorageChecksUpgrade(t *testing.T) {
	const firstCheck = 20261128050232
	before := buildOf(t, func(version int64) bool { return version < firstCheck }, nil)
	for _, tc := range []struct {
		name string
		sets Sets
	}{{"manager", ManagerSet}, {"shared", AllSets}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			d, err := Attach(ctx, freshDatabase(t), tc.sets)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Close)
			if err := d.migrateFrom(ctx, before); err != nil {
				t.Fatal(err)
			}
			if _, err := d.pool.Exec(ctx, `
				INSERT INTO todos (id, title) VALUES ('old', 'Example');
				INSERT INTO task_briefs (id, brief, state) VALUES ('old', '[]', 'null');
				INSERT INTO todo_pr_followups (followup_id, todo_id, pr_url, supervisor, created_at, state, watch_status)
					VALUES ('old', 'old', 'https://example.com/pull/old', 'owner', now(), 'legacy', 'legacy');
				INSERT INTO file_transfer_audit (id, from_machine, to_machine, caller_kind, kind, state)
					VALUES ('old', 'source', 'target', 'legacy', 'legacy', 'legacy');
				INSERT INTO held_operations (op_id, machine_id, kind, call) VALUES ('old', 'machine', 'legacy', '[]');
			`); err != nil {
				t.Fatalf("seed old rows: %v", err)
			}
			if err := d.migrate(ctx); err != nil {
				t.Fatalf("upgrade with old rows: %v", err)
			}
			if err := d.migrate(ctx); err != nil {
				t.Fatalf("reopen after upgrade: %v", err)
			}
			if err := d.migrateFrom(ctx, before); err != nil {
				t.Fatalf("older runner after upgrade: %v", err)
			}
			var kept bool
			if err := d.pool.QueryRow(ctx, `SELECT
				EXISTS (SELECT FROM task_briefs WHERE id = 'old' AND brief = '[]' AND state = 'null') AND
				EXISTS (SELECT FROM todo_pr_followups WHERE followup_id = 'old' AND state = 'legacy' AND watch_status = 'legacy') AND
				EXISTS (SELECT FROM file_transfer_audit WHERE id = 'old' AND caller_kind = 'legacy' AND kind = 'legacy' AND state = 'legacy') AND
				EXISTS (SELECT FROM held_operations WHERE op_id = 'old' AND kind = 'legacy' AND call = '[]')`).Scan(&kept); err != nil || !kept {
				t.Fatalf("old rows after upgrade: kept=%v err=%v", kept, err)
			}
		})
	}
}

func TestUrgentCallOutcomeRemainsFreeText(t *testing.T) {
	d, ctx := open(t), t.Context()
	c := UrgentCall{ID: "outcome", MachineID: "machine", Conversation: "conversation", Matter: "example", Title: "Example",
		Why: "rule", State: "ringing", Attempt: 1, CreatedAt: time.Now()}
	if err := d.InsertUrgentCall(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, found, err := d.UrgentCall(ctx, c.ID); err != nil || !found || got.Outcome != "" {
		t.Fatalf("initial outcome = %q, found=%v err=%v", got.Outcome, found, err)
	}
	c.State, c.Outcome = "ended", "We agreed to call again tomorrow."
	if ok, err := d.UpdateUrgentCall(ctx, c); err != nil || !ok {
		t.Fatalf("save free-text outcome: updated=%v err=%v", ok, err)
	}
	if got, found, err := d.UrgentCall(ctx, c.ID); err != nil || !found || got.Outcome != c.Outcome {
		t.Fatalf("saved outcome = %q, found=%v err=%v", got.Outcome, found, err)
	}
}
