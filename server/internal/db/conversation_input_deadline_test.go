package db

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func undeliveredRows(t *testing.T, d *DB) map[string]ConversationUndeliveredInput {
	t.Helper()
	rows, err := d.ConversationUndeliveredInputs(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ConversationUndeliveredInput{}
	for _, r := range rows {
		out[r.Input.ClientMsgID] = r
	}
	return out
}

// A historical uncertain input expires by CAS, keeps its payload and audit, never
// touches the current switch, and the queue behind it drains in order. The
// current switch's own first input expiring then accepts that switch.
func TestExpiredUncertainInputReleasesQueueAndSettlesSwitch(t *testing.T) {
	d, _, input := historicalDeliveryFixture(t)
	ctx := t.Context()
	before := time.Now().Add(-time.Hour)
	if ok, err := d.ExpireConversationInput(ctx, "root", "chat", 4, input, "receipt absent", before); err != nil || ok {
		t.Fatalf("expired inside its deadline: %v %v", ok, err)
	}
	stale := input
	stale.Version++
	if _, err := d.ExpireConversationInput(ctx, "root", "chat", 4, stale, "receipt absent", time.Now().Add(time.Hour)); !errors.Is(err, ErrConversationEpoch) {
		t.Fatalf("stale attempt expired: %v", err)
	}
	rows := undeliveredRows(t, d)
	if rows["old"].Input.State != "in_doubt" || rows["next"].Input.State != "queued" || rows["next"].Text != "" {
		t.Fatalf("queue projection: %+v", rows)
	}
	if ok, err := d.ExpireConversationInput(ctx, "root", "chat", 4, input, "receipt absent", time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("expire: %v %v", ok, err)
	}
	if _, err := d.ExpireConversationInput(ctx, "root", "chat", 4, input, "receipt absent", time.Now().Add(time.Hour)); !errors.Is(err, ErrConversationEpoch) {
		t.Fatalf("expired twice: %v", err)
	}
	var payload, audit []byte
	var state, actor string
	if err := d.pool.QueryRow(ctx, `SELECT state,payload FROM conversation_inputs WHERE client_msg_id='old'`).Scan(&state, &payload); err != nil || state != "expired" || string(payload) != string(input.Payload) {
		t.Fatalf("expired input lost its payload: %s %q %v", state, payload, err)
	}
	if err := d.pool.QueryRow(ctx, `SELECT actor,original_payload FROM conversation_input_resolutions WHERE client_msg_id='old' AND action='expired'`).Scan(&actor, &audit); err != nil || actor != "delivery_deadline" || string(audit) != string(input.Payload) {
		t.Fatalf("expiry not audited: %s %q %v", actor, audit, err)
	}
	rows = undeliveredRows(t, d)
	if r := rows["old"]; r.Input.State != "expired" || r.Text != "original content" || r.Images != 1 || r.SwitchPending {
		t.Fatalf("expired projection: %+v", r)
	}
	c, _ := d.LogicalConversation(ctx, "root", "chat")
	if c.Switch == nil || c.Switch.OperationID != "op4" || c.Switch.Phase != "active" {
		t.Fatalf("historical expiry touched the current switch: %+v", c.Switch)
	}
	if err := conversationTransaction(ctx, d, func(tx pgx.Tx) error { return requireNoUnresolvedConversationInput(ctx, tx, "root", "chat") }); err != nil {
		t.Fatalf("expired input still fences switches: %v", err)
	}
	next, err := d.ClaimConversationInput(ctx, "root", "chat", 4)
	if err != nil || next == nil || next.ClientMsgID != "next" || next.ClaimedAt.IsZero() {
		t.Fatalf("queue not released in order: %+v %v", next, err)
	}
	// The current switch's own first input, unconfirmed past its deadline.
	if ok, err := d.ExpireConversationInput(ctx, "root", "chat", 4, *next, "receipt absent", time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("expire first input: %v %v", ok, err)
	}
	c, _ = d.LogicalConversation(ctx, "root", "chat")
	if a, _ := c.Active(); c.Switch != nil || c.Epoch != 4 || a.ID != "s4" {
		t.Fatalf("switch not accepted on its target: %+v", c)
	}
	if handoff, err := d.ConversationPendingHandoff(ctx, "root", "chat", "s4"); err != nil || len(handoff) == 0 {
		t.Fatalf("switch context not kept for the next input: %s %v", handoff, err)
	}
	if last, err := d.ClaimConversationInput(ctx, "root", "chat", 4); err != nil || last == nil || last.ClientMsgID != "last" {
		t.Fatalf("last input not released: %+v %v", last, err)
	}
}

func TestOwnerRetryAndDismissOfExpiredInputAreCASAndIdempotent(t *testing.T) {
	d, _, input := historicalDeliveryFixture(t)
	ctx := t.Context()
	if _, err := d.ResolveExpiredConversationInput(ctx, "root", "chat", "old", "owner", true); !errors.Is(err, ErrConversationEpoch) {
		t.Fatalf("retry of an input still awaiting its receipt: %v", err)
	}
	if ok, err := d.ExpireConversationInput(ctx, "root", "chat", 4, input, "receipt absent", time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatal(err)
	}
	var lastPosition int64
	if err := d.pool.QueryRow(ctx, `SELECT max(position) FROM conversation_inputs`).Scan(&lastPosition); err != nil {
		t.Fatal(err)
	}
	if changed, err := d.ResolveExpiredConversationInput(ctx, "root", "chat", "old", "owner", true); err != nil || !changed {
		t.Fatalf("retry: %v %v", changed, err)
	}
	if changed, err := d.ResolveExpiredConversationInput(ctx, "root", "chat", "old", "owner", true); err != nil || changed {
		t.Fatalf("second tap retried again: %v %v", changed, err)
	}
	if _, err := d.ResolveExpiredConversationInput(ctx, "root", "chat", "old", "owner", false); !errors.Is(err, ErrConversationEpoch) {
		t.Fatalf("dismiss after retry: %v", err)
	}
	var state string
	var position, retries int64
	var payload []byte
	if err := d.pool.QueryRow(ctx, `SELECT state,position,payload FROM conversation_inputs WHERE client_msg_id='old'`).Scan(&state, &position, &payload); err != nil || state != "queued" || position <= lastPosition || string(payload) != string(input.Payload) {
		t.Fatalf("retry not at the tail with its payload: %s %d<=%d %v", state, position, lastPosition, err)
	}
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM conversation_input_resolutions WHERE client_msg_id='old' AND action='retry'`).Scan(&retries); err != nil || retries != 1 {
		t.Fatalf("retry audit: %d %v", retries, err)
	}
	for i, want := range []string{"next", "last", "old"} {
		row, err := d.ClaimConversationInput(ctx, "root", "chat", 4)
		if err != nil || row == nil || row.ClientMsgID != want {
			t.Fatalf("FIFO after retry: want %s got %+v %v", want, row, err)
		}
		if row.ClientMsgID == "old" && (row.DispatchSegment != "s4" || row.DispatchEpoch != 4) {
			t.Fatalf("retry kept the old dispatch identity: %+v", row)
		}
		operation := ""
		if i == 0 {
			operation = "op4" // the switch's own first input
		}
		if err := d.ResolveClaimedConversationDelivery(ctx, "root", "chat", 4, *row, operation, "accepted"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.ResolveExpiredConversationInput(ctx, "root", "chat", "missing", "owner", true); !errors.Is(err, ErrConversationEpoch) {
		t.Fatalf("unknown input: %v", err)
	}
}

func TestOwnerDismissKeepsPayloadOnlyInAudit(t *testing.T) {
	d, _, input := historicalDeliveryFixture(t)
	ctx := t.Context()
	if ok, err := d.ExpireConversationInput(ctx, "root", "chat", 4, input, "receipt absent", time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatal(err)
	}
	owners, err := d.ConversationInputOwners(ctx, "chat", "old")
	if err != nil || len(owners) != 1 || owners[0] != "root" {
		t.Fatalf("owners: %v %v", owners, err)
	}
	for i := range 2 {
		if changed, err := d.ResolveExpiredConversationInput(ctx, "root", "chat", "old", "owner", false); err != nil || changed != (i == 0) {
			t.Fatalf("dismiss %d: %v %v", i, changed, err)
		}
	}
	if _, err := d.ResolveExpiredConversationInput(ctx, "root", "chat", "old", "owner", true); !errors.Is(err, ErrConversationEpoch) {
		t.Fatalf("retry after dismiss: %v", err)
	}
	var payload, audit []byte
	if err := d.pool.QueryRow(ctx, `SELECT i.payload,r.original_payload FROM conversation_inputs i JOIN conversation_input_resolutions r USING (logical_machine,logical_session,client_msg_id) WHERE i.client_msg_id='old' AND r.action='dismiss'`).Scan(&payload, &audit); err != nil || string(payload) != "{}" || string(audit) != string(input.Payload) {
		t.Fatalf("dismiss payload/audit: %q %q %v", payload, audit, err)
	}
	if _, ok := undeliveredRows(t, d)["old"]; ok {
		t.Fatal("dismissed input still shown")
	}
	if receipt, err := d.ConversationInputReceipt(ctx, "root", "chat", "old", input.Payload); err != nil || receipt != ConversationInputDismissed {
		t.Fatalf("dismissed receipt: %q %v", receipt, err)
	}
}
