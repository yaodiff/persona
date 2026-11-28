package db

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nwverse/persona/server/internal/proto"
)

func requireStorageCheckViolation(t *testing.T, err error, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != constraint {
		t.Fatalf("want check violation for %s, got %v", constraint, err)
	}
}

func TestSentMessageStorageChecks(t *testing.T) {
	for _, tc := range []struct {
		name string
		sets Sets
	}{
		{"hub", HubSet},
		{"manager", ManagerSet},
		{"shared", AllSets},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			d, err := Attach(ctx, freshDatabase(t), tc.sets)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Close)
			if err := d.migrateFrom(ctx, numberedMigrations(t)); err != nil {
				t.Fatal(err)
			}
			const insert = `INSERT INTO sent_messages (client_msg_id, machine_id, session_id, state, echo)
				VALUES ($1, 'machine', 'session', $2, $3)`
			if _, err := d.pool.Exec(ctx, insert, "old", "legacy", json.RawMessage(`[]`)); err != nil {
				t.Fatal(err)
			}
			if err := d.migrate(ctx); err != nil {
				t.Fatal(err)
			}
			var kept bool
			if err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT FROM sent_messages
				WHERE client_msg_id = 'old' AND state = 'legacy' AND echo = '[]')`).Scan(&kept); err != nil || !kept {
				t.Fatalf("old row after NOT VALID migrations: %v, %v", kept, err)
			}

			for _, state := range []string{SendStateSending, SendStateDelivered, SendStateUnknown, SendStateLater} {
				if _, err := d.pool.Exec(ctx, insert, state, state, nil); err != nil {
					t.Fatalf("insert %q with no echo: %v", state, err)
				}
			}
			if err := d.RecordEcho(ctx, SendStateSending, "machine", json.RawMessage(`{"line":"user-line"}`)); err != nil {
				t.Fatal(err)
			}
			_, err = d.pool.Exec(ctx, insert, "bad-state", "", nil)
			requireStorageCheckViolation(t, err, "sent_messages_state_check")
			err = d.SettleSend(ctx, SendStateSending, "invalid")
			requireStorageCheckViolation(t, err, "sent_messages_state_check")
			_, err = d.pool.Exec(ctx, insert, "bad-echo", SendStateSending, json.RawMessage(`null`))
			requireStorageCheckViolation(t, err, "sent_messages_echo_shape")
			err = d.RecordEcho(ctx, SendStateDelivered, "machine", json.RawMessage(`[]`))
			requireStorageCheckViolation(t, err, "sent_messages_echo_shape")
		})
	}
}

func TestHubJSONStorageChecks(t *testing.T) {
	ctx := t.Context()
	d, err := Open(ctx, freshDatabase(t), HubSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	for _, tc := range []struct {
		constraint, insert, update, valid, wrongContainer string
		nullable                                          bool
	}{
		{
			"devices_prefs_shape",
			`INSERT INTO devices (device_id, prefs) VALUES ('phone', $1)`,
			`UPDATE devices SET prefs = $1 WHERE device_id = 'phone'`,
			`{"input":true}`, `[]`, true,
		},
		{
			"phone_tools_input_schema_shape",
			`INSERT INTO phone_tools (name, input_schema) VALUES ('phone_device_info', $1)`,
			`UPDATE phone_tools SET input_schema = $1 WHERE name = 'phone_device_info'`,
			`{"type":"object"}`, `[]`, false,
		},
		{
			"peers_endpoints_shape",
			`INSERT INTO peers (hub_id, fp, token, credential_id, endpoints) VALUES ('peer', '', '', '', $1)`,
			`UPDATE peers SET endpoints = $1 WHERE hub_id = 'peer'`,
			`[]`, `{}`, false,
		},
		{
			"machine_audit_detail_shape",
			`INSERT INTO machine_audit (machine_id, action, detail) VALUES ('machine', 'confirm', $1)`,
			`UPDATE machine_audit SET detail = $1 WHERE machine_id = 'machine'`,
			`{}`, `[]`, false,
		},
		{
			"phone_write_queue_result_shape",
			`INSERT INTO phone_write_queue (id, tool, args, result) VALUES ('write', 'phone_reminders_create', '{}', $1)`,
			`UPDATE phone_write_queue SET result = $1 WHERE id = 'write'`,
			`{"content":[],"is_error":false}`, `[]`, true,
		},
		{
			"client_ops_target_shape",
			`INSERT INTO client_ops (id, kind, digest, target) VALUES ('00000000-0000-4000-8000-000000000001', 'message', 'fixture', $1)`,
			`UPDATE client_ops SET target = $1 WHERE id = '00000000-0000-4000-8000-000000000001'`,
			`{"path":"/example","query":""}`, `[]`, false,
		},
		{
			"client_ops_result_shape",
			`INSERT INTO client_ops (id, kind, digest, target, result) VALUES ('00000000-0000-4000-8000-000000000002', 'message', 'fixture', '{}', $1)`,
			`UPDATE client_ops SET result = $1 WHERE id = '00000000-0000-4000-8000-000000000002'`,
			`{"status":200,"body":null}`, `[]`, true,
		},
	} {
		t.Run(tc.constraint, func(t *testing.T) {
			for _, invalid := range []string{tc.wrongContainer, `null`, `false`} {
				_, err := d.pool.Exec(ctx, tc.insert, json.RawMessage(invalid))
				requireStorageCheckViolation(t, err, tc.constraint)
			}
			if _, err := d.pool.Exec(ctx, tc.insert, json.RawMessage(tc.valid)); err != nil {
				t.Fatalf("insert %s: %v", tc.valid, err)
			}
			for _, invalid := range []string{tc.wrongContainer, `null`, `false`} {
				_, err := d.pool.Exec(ctx, tc.update, json.RawMessage(invalid))
				requireStorageCheckViolation(t, err, tc.constraint)
			}
			if tc.nullable {
				if tag, err := d.pool.Exec(ctx, tc.update, nil); err != nil || tag.RowsAffected() != 1 {
					t.Fatalf("update to SQL NULL: %s, %v", tag, err)
				}
			}
			if tag, err := d.pool.Exec(ctx, tc.update, json.RawMessage(tc.valid)); err != nil || tag.RowsAffected() != 1 {
				t.Fatalf("update to %s: %s, %v", tc.valid, tag, err)
			}
		})
	}
}

func TestPhoneToolsStoreNullSchemaAsEmptyObject(t *testing.T) {
	d, ctx := open(t), t.Context()
	if err := d.ReplacePhoneTools(ctx, PhoneToolRegistration{DeviceID: "phone", Tools: []proto.Tool{
		{Name: "phone_device_info", InputSchema: json.RawMessage(`null`)},
	}}); err != nil {
		t.Fatal(err)
	}
	var empty bool
	if err := d.pool.QueryRow(ctx, `SELECT input_schema = '{}' FROM phone_tools WHERE device_id = 'phone'`).Scan(&empty); err != nil || !empty {
		t.Fatalf("missing schema was not stored as an empty object: %v, %v", empty, err)
	}
}
