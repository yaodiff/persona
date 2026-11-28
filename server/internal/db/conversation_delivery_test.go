package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// Simulate the already-persisted epoch-3 lost-reply / epoch-4 recovery state.
// New code deliberately refuses to create this state through activation.
func historicalDeliveryFixture(t *testing.T) (*DB, string, ConversationInput) {
	t.Helper()
	d, url := historyDB(t)
	ctx := t.Context()
	if err := d.AdoptConversation(ctx, "root", "chat", "s1", "claude", "h1"); err != nil {
		t.Fatal(err)
	}
	for epoch := int64(1); epoch <= 3; epoch++ {
		op := fmt.Sprintf("op%d", epoch+1)
		if err := d.BeginConversationSwitch(ctx, "root", "chat", ConversationSwitch{OperationID: op, ExpectedEpoch: epoch, SourceSegment: fmt.Sprintf("s%d", epoch), Request: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if err := d.PutConversationHandoff(ctx, "root", "chat", op, json.RawMessage(`{"summary":"handoff for new target"}`)); err != nil {
			t.Fatal(err)
		}
		conversation, err := d.LogicalConversation(ctx, "root", "chat")
		if err != nil {
			t.Fatal(err)
		}
		target, ok := conversation.Active()
		if !ok {
			t.Fatal("fixture has no active source")
		}
		target.ID, target.MachineID, target.SessionID, target.Engine = fmt.Sprintf("s%d", epoch+1), fmt.Sprintf("machine%d", epoch+1), fmt.Sprintf("native%d", epoch+1), "agy"
		if err := d.PrepareConversationSegment(ctx, "root", "chat", op, target, 0); err != nil {
			t.Fatal(err)
		}
		if err := d.ActivateConversationSegment(ctx, "root", "chat", op, epoch); err != nil {
			t.Fatal(err)
		}
		if epoch < 3 {
			if ok, err := d.AcceptIdleConversationSwitch(ctx, "root", "chat", op, epoch); err != nil || !ok {
				t.Fatalf("idle accept: %v %v", ok, err)
			}
		}
	}
	// Old binary had no dispatch columns. Backfill must use op3, not active s4.
	payload := []byte(`{"method":"assistant.send","params":{"text":"original content","images":[{"data":"original attachment"}]}}`)
	if err := d.EnqueueConversationInput(ctx, "root", "chat", "old", 4, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE conversation_inputs SET epoch=3,state='in_doubt' WHERE client_msg_id='old'`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"next", "last"} {
		if err := d.EnqueueConversationInput(ctx, "root", "chat", id, 4, json.RawMessage(`{"text":"next"}`)); err != nil {
			t.Fatal(err)
		}
	}
	input, err := d.ConversationUnresolvedInput(ctx, "root", "chat")
	if err != nil || input == nil || input.DispatchSegment != "s3" || input.DispatchEpoch != 3 || input.DispatchMachine != "machine3" || input.DispatchSession != "native3" {
		t.Fatalf("legacy original target: %+v %v", input, err)
	}
	return d, url, *input
}

func TestHistoricalDeliverySettlementPreservesCurrentSwitchAndFIFO(t *testing.T) {
	for _, outcome := range []string{"accepted", "rejected", "absent"} {
		t.Run(outcome, func(t *testing.T) {
			d, url, input := historicalDeliveryFixture(t)
			ctx := t.Context()
			restarted, err := Open(ctx, url, ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if row, err := restarted.ClaimConversationInput(ctx, "root", "chat", 4); err != nil || row != nil {
				t.Fatalf("unresolved was overtaken: %+v %v", row, err)
			}
			if outcome == "absent" {
				if err := restarted.NoteConversationInputBlocked(ctx, "root", "chat", 4, input, "original receipt absent"); err != nil {
					t.Fatal(err)
				}
				pending, _ := d.ConversationUnresolvedInput(ctx, "root", "chat")
				if pending == nil || pending.BlockedReason != "original receipt absent" || pending.LastReconciledAt == nil || string(pending.Payload) != string(input.Payload) {
					t.Fatalf("uncertain payload/diagnostic lost: %+v", pending)
				}
				if row, err := d.ClaimConversationInput(ctx, "root", "chat", 4); err != nil || row != nil {
					t.Fatalf("absent receipt replayed/overtaken: %+v %v", row, err)
				}
				return
			}
			if err := restarted.ResolveHistoricalConversationDelivery(ctx, "root", "chat", 5, input, outcome); !errors.Is(err, ErrConversationEpoch) {
				t.Fatalf("root fence missing: %v", err)
			}
			if err := restarted.ResolveHistoricalConversationDelivery(ctx, "root", "chat", 4, input, outcome); err != nil {
				t.Fatal(err)
			}
			c, _ := d.LogicalConversation(ctx, "root", "chat")
			if c.Switch == nil || c.Switch.OperationID != "op4" || c.Switch.Phase != "active" {
				t.Fatalf("old receipt accepted new target: %+v", c)
			}
			handoff, _, err := d.ConversationPreparation(ctx, "root", "chat", "op4")
			if err != nil || len(handoff) == 0 {
				t.Fatalf("historical receipt consumed current handoff: %s %v", handoff, err)
			}
			originalHandoff, _, err := d.ConversationPreparation(ctx, "root", "chat", "op3")
			if err != nil || (len(originalHandoff) == 0) != (outcome == "accepted") {
				t.Fatalf("original handoff settlement mismatch: %s %v", originalHandoff, err)
			}
			row, err := restarted.ClaimConversationInput(ctx, "root", "chat", 4)
			want := "next"
			if outcome == "rejected" {
				want = "old"
				var original []byte
				if err := d.pool.QueryRow(ctx, `SELECT original_payload FROM conversation_input_resolutions WHERE actor='runtime_receipt' AND client_msg_id='old'`).Scan(&original); err != nil || string(original) != string(input.Payload) {
					t.Fatalf("definite refusal lost original dispatch audit: %q %v", original, err)
				}
				if row != nil && string(row.Payload) != string(input.Payload) {
					t.Fatalf("definite refusal changed the replay payload: %s", row.Payload)
				}
				if err := d.ResolveHistoricalConversationDelivery(ctx, "root", "chat", 4, input, "accepted"); !errors.Is(err, ErrConversationEpoch) {
					t.Fatalf("late old receipt settled the new dispatch: %v", err)
				}
			}
			if err != nil || row == nil || row.ClientMsgID != want || row.DispatchSegment != "s4" || row.DispatchEpoch != 4 {
				t.Fatalf("next input not claimed in order: %+v %v", row, err)
			}
			if again, err := d.ClaimConversationInput(ctx, "root", "chat", 4); err != nil || again != nil {
				t.Fatalf("concurrent/restarted claim duplicated: %+v %v", again, err)
			}
			if err := restarted.ResolveClaimedConversationDelivery(ctx, "root", "chat", 4, *row, "op4", "accepted"); err != nil {
				t.Fatal(err)
			}
			c, _ = d.LogicalConversation(ctx, "root", "chat")
			if c.Switch != nil {
				t.Fatalf("new target receipt failed acceptance: %+v", c.Switch)
			}
			if outcome == "rejected" {
				next, err := d.ClaimConversationInput(ctx, "root", "chat", 4)
				if err != nil || next == nil || next.ClientMsgID != "next" {
					t.Fatalf("later input lost or overtook refused message: %+v %v", next, err)
				}
			}
		})
	}
}

func TestManualConversationInputResolutionCASAuditAndIdempotency(t *testing.T) {
	for _, action := range []string{"accepted", "rejected", "replay"} {
		t.Run(action, func(t *testing.T) {
			d, _, input := historicalDeliveryFixture(t)
			ctx := t.Context()
			p := ConversationInputResolution{RequestID: "manual-1", ExpectedEpoch: 4, Input: input, Action: action, Reason: "operator verified original delivery"}
			stale := p
			stale.Input.Version++
			if err := d.ResolveConversationInputManually(ctx, "root", "chat", "human", stale); !errors.Is(err, ErrConversationEpoch) {
				t.Fatalf("stale CAS accepted: %v", err)
			}
			for range 2 {
				if err := d.ResolveConversationInputManually(ctx, "root", "chat", "human", p); err != nil {
					t.Fatal(err)
				}
			}
			changed := p
			changed.Action = "accepted"
			changed.Reason = "different intent"
			if err := d.ResolveConversationInputManually(ctx, "root", "chat", "human", changed); !errors.Is(err, ErrConversationEpoch) {
				t.Fatalf("idempotency key reused: %v", err)
			}
			var payload []byte
			var count int
			if err := d.pool.QueryRow(ctx, `SELECT original_payload FROM conversation_input_resolutions WHERE request_id='manual-1'`).Scan(&payload); err != nil || string(payload) != string(input.Payload) {
				t.Fatalf("original payload lost: %q %v", payload, err)
			}
			if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM conversation_input_resolutions`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("audit duplicated: %d %v", count, err)
			}
			if err := d.ResolveHistoricalConversationDelivery(ctx, "root", "chat", 4, input, "accepted"); !errors.Is(err, ErrConversationEpoch) {
				t.Fatalf("old receipt crossed manual fence: %v", err)
			}
			row, err := d.ClaimConversationInput(ctx, "root", "chat", 4)
			want := "next"
			if action == "replay" {
				want = "old"
			}
			if err != nil || row == nil || row.ClientMsgID != want || row.DispatchEpoch != 4 {
				t.Fatalf("manual action changed FIFO: %+v %v", row, err)
			}
		})
	}
}

func TestUnresolvedDeliveryFencesNewSwitchAndClaimRace(t *testing.T) {
	d, url := historyDB(t)
	ctx := t.Context()
	if err := d.AdoptConversation(ctx, "root", "chat", "s1", "claude", "h1"); err != nil {
		t.Fatal(err)
	}
	if err := d.EnqueueConversationInput(ctx, "root", "chat", "one", 1, json.RawMessage(`{"text":"one"}`)); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	claims := make(chan *ConversationInput, 2)
	for _, conn := range []*DB{d, other} {
		wg.Add(1)
		go func(conn *DB) {
			defer wg.Done()
			row, err := conn.ClaimConversationInput(ctx, "root", "chat", 1)
			if err != nil && !errors.Is(err, ErrConversationSwitchBusy) {
				t.Error(err)
			}
			claims <- row
		}(conn)
	}
	wg.Wait()
	close(claims)
	count := 0
	for row := range claims {
		if row != nil {
			count++
			if row.DispatchSegment != "s1" || row.DispatchEpoch != 1 {
				t.Fatalf("claim identity missing: %+v", row)
			}
		}
	}
	if count != 1 {
		t.Fatalf("duplicate claim: %d", count)
	}
	if err := d.BeginConversationSwitch(ctx, "root", "chat", ConversationSwitch{OperationID: "blocked", ExpectedEpoch: 1, SourceSegment: "s1", Request: json.RawMessage(`{}`)}); !errors.Is(err, ErrConversationSwitchBusy) {
		t.Fatalf("unresolved input crossed activation: %v", err)
	}
}

func TestManualReplayFencesLateReceiptAtSameEpochAndPreservesOriginalPayload(t *testing.T) {
	d, _ := historyDB(t)
	ctx := t.Context()
	op := prepareLogicalSwitch(t, d)
	if err := d.ActivateConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, 1); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"text":"original input","provenance":{"origin":"operator"}}`)
	if err := d.EnqueueConversationInput(ctx, "root-machine", "root-chat", "uncertain", 2, payload); err != nil {
		t.Fatal(err)
	}
	first, err := d.ClaimConversationInput(ctx, "root-machine", "root-chat", 2)
	if err != nil || first == nil {
		t.Fatalf("first claim: %+v %v", first, err)
	}
	p := ConversationInputResolution{RequestID: "explicit-replay", ExpectedEpoch: 2, Input: *first, Action: "replay", Reason: "operator accepts replay risk"}
	if err := d.ResolveConversationInputManually(ctx, "root-machine", "root-chat", "operator", p); err != nil {
		t.Fatal(err)
	}
	second, err := d.ClaimConversationInput(ctx, "root-machine", "root-chat", 2)
	if err != nil || second == nil || second.Version <= first.Version || string(second.Payload) != string(payload) || second.Position != first.Position {
		t.Fatalf("replay replaced original input: %+v %v", second, err)
	}
	if valid, err := d.ConversationInputClaimCurrent(ctx, "root-machine", "root-chat", 2, *first); err != nil || valid {
		t.Fatalf("old pre-RPC attempt can still dispatch: %v %v", valid, err)
	}
	if err := d.RequeueClaimedConversationInput(ctx, "root-machine", "root-chat", 2, *first, op.OperationID); !errors.Is(err, ErrConversationEpoch) {
		t.Fatalf("old validation cleanup requeued the new attempt: %v", err)
	}
	if valid, err := d.ConversationInputClaimCurrent(ctx, "root-machine", "root-chat", 2, *second); err != nil || !valid {
		t.Fatalf("new attempt was damaged by stale cleanup: %v %v", valid, err)
	}
	if err := d.ResolveClaimedConversationDelivery(ctx, "root-machine", "root-chat", 2, *first, op.OperationID, "accepted"); !errors.Is(err, ErrConversationEpoch) {
		t.Fatalf("late receipt settled a newer attempt: %v", err)
	}
	if err := d.ResolveClaimedConversationDelivery(ctx, "root-machine", "root-chat", 2, *second, op.OperationID, "accepted"); err != nil {
		t.Fatal(err)
	}
	var original []byte
	if err := d.pool.QueryRow(ctx, `SELECT original_payload FROM conversation_input_resolutions WHERE request_id='explicit-replay'`).Scan(&original); err != nil || string(original) != string(payload) {
		t.Fatalf("terminal cleanup erased audit payload: %q %v", original, err)
	}
}

func TestManualCurrentRejectionDoesNotAutomaticallyReplayUncertainInput(t *testing.T) {
	d, _ := historyDB(t)
	op := prepareLogicalSwitch(t, d)
	ctx := t.Context()
	if err := d.ActivateConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, 1); err != nil {
		t.Fatal(err)
	}
	if err := d.EnqueueConversationInput(ctx, "root-machine", "root-chat", "one", 2, json.RawMessage(`{"text":"original"}`)); err != nil {
		t.Fatal(err)
	}
	input, err := d.ClaimConversationInput(ctx, "root-machine", "root-chat", 2)
	if err != nil || input == nil {
		t.Fatalf("claim: %+v %v", input, err)
	}
	p := ConversationInputResolution{RequestID: "manual-reject", ExpectedEpoch: 2, Input: *input, Action: "rejected", Reason: "operator chooses terminal rejection"}
	if err := d.ResolveConversationInputManually(ctx, "root-machine", "root-chat", "operator", p); err != nil {
		t.Fatal(err)
	}
	c, _ := d.LogicalConversation(ctx, "root-machine", "root-chat")
	if c.Epoch != 3 || c.Switch != nil {
		t.Fatalf("current target not rolled back: %+v", c)
	}
	if row, err := d.ClaimConversationInput(ctx, "root-machine", "root-chat", 3); err != nil || row != nil {
		t.Fatalf("manual terminal rejection replayed: %+v %v", row, err)
	}
	if err := d.ResolveConversationInputManually(ctx, "root-machine", "root-chat", "operator", p); err != nil {
		t.Fatalf("idempotent request after rollback: %v", err)
	}
}

func TestActivationRefusesLegacyUnresolvedSourceAndUnknownIdentityStaysBlocked(t *testing.T) {
	d, _ := historyDB(t)
	op := prepareLogicalSwitch(t, d)
	ctx := t.Context()
	if err := d.EnqueueConversationInput(ctx, "root-machine", "root-chat", "old", 1, json.RawMessage(`{"text":"never discard"}`)); err != nil {
		t.Fatal(err)
	}
	// Simulates an older manager's claim during a rolling upgrade.
	if _, err := d.pool.Exec(ctx, `UPDATE conversation_inputs SET state='in_doubt',epoch=9 WHERE client_msg_id='old'`); err != nil {
		t.Fatal(err)
	}
	if err := d.ActivateConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, 1); !errors.Is(err, ErrConversationSwitchBusy) {
		t.Fatalf("activation silently migrated unresolved source: %v", err)
	}
	input, err := d.ConversationUnresolvedInput(ctx, "root-machine", "root-chat")
	if err != nil || input == nil || input.DispatchEpoch != 0 || input.DispatchSegment != "" {
		t.Fatalf("unknown historical identity guessed from active: %+v %v", input, err)
	}
	c, _ := d.LogicalConversation(ctx, "root-machine", "root-chat")
	if c.Epoch != 1 || c.Switch.Phase != "prepared" {
		t.Fatalf("blocked activation changed root: %+v", c)
	}
}
