package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
)

type historicalReceiptStore struct {
	*db.DB
	historical       string
	current          bool
	currentOperation string
	blocked          string
}

func (st *historicalReceiptStore) ResolveHistoricalConversationDelivery(_ context.Context, _, _ string, epoch int64, input db.ConversationInput, outcome string) error {
	if epoch != 4 || input.DispatchEpoch != 3 || input.DispatchSession != "original-session" {
		panic("historical dispatch identity changed")
	}
	st.historical = outcome
	return nil
}

func (st *historicalReceiptStore) ResolveClaimedConversationDelivery(_ context.Context, _, _ string, _ int64, _ db.ConversationInput, operation, _ string) error {
	st.current = true
	st.currentOperation = operation
	return nil
}

func (st *historicalReceiptStore) NoteConversationInputBlocked(_ context.Context, _, _ string, _ int64, _ db.ConversationInput, reason string) error {
	st.blocked = reason
	return nil
}

func TestCrossEpochReconcilerQueriesOriginalDispatchAndDoesNotAcceptNewSwitch(t *testing.T) {
	for _, receipt := range []string{`{"accepted":true}`, `{"accepted":false}`, `{"rejected":true}`, `{"accepted":true,"rejected":true}`} {
		t.Run(receipt, func(t *testing.T) {
			s, f, d, _ := logicalPair(t, "agy")
			ctx := t.Context()
			activatePair(t, d)
			st := &historicalReceiptStore{DB: d}
			calls := 0
			f.answerNode("assistant.segment.input", func(machine string, raw json.RawMessage) (any, error) {
				var p map[string]any
				_ = json.Unmarshal(raw, &p)
				if machine != "server" || p["session_id"] != "original-session" || p["client_msg_id"] != "lost-reply" {
					t.Fatalf("receipt sent to new active instead of original target: %s %s", machine, raw)
				}
				calls++
				return json.RawMessage(receipt), nil
			})
			active := logicalFixtureTarget("agy")
			active.ID, active.SessionID, active.State = "new-segment", "new-session", "active"
			c := &db.LogicalConversation{MachineID: "server", SessionID: "side-chat", Epoch: 4, Segments: []conversationlog.Segment{active}, Switch: &db.ConversationSwitch{OperationID: "new-switch", Phase: "active"}}
			input := db.ConversationInput{ClientMsgID: "lost-reply", Epoch: 3, State: "in_doubt", DispatchEpoch: 3, DispatchSegment: "old-segment", DispatchMachine: "server", DispatchSession: "original-session"}
			if err := s.reconcileLogicalInput(ctx, st, c, active, input); err != nil {
				t.Fatal(err)
			}
			want := ""
			if receipt == `{"accepted":true}` {
				want = "accepted"
			}
			if receipt == `{"rejected":true}` {
				want = "rejected"
			}
			if calls != 1 || st.current || st.historical != want || (want == "" && st.blocked == "") {
				t.Fatalf("wrong coordination boundary: %+v calls=%d", st, calls)
			}
			if c.Switch.Phase != "active" {
				t.Fatal("historical receipt accepted new switch")
			}
		})
	}
}

func TestManualInputResolutionRouteRequiresRoleAndCAS(t *testing.T) {
	s, _, d, _ := logicalPair(t, "agy")
	c := activatePair(t, d)
	ctx := t.Context()
	if err := d.EnqueueConversationInput(ctx, "server", "side-chat", "ambiguous", 2, json.RawMessage(`{"method":"assistant.send","params":{"text":"keep original"}}`)); err != nil {
		t.Fatal(err)
	}
	input, err := d.ClaimConversationInput(ctx, "server", "side-chat", 2)
	if err != nil || input == nil {
		t.Fatalf("claim: %+v %v", input, err)
	}
	request := db.ConversationInputResolution{RequestID: "human-resolution", ExpectedEpoch: c.Epoch, Input: *input, Action: "replay", Reason: "explicit operator replay of uncertain delivery"}
	raw, _ := json.Marshal(request)
	url := "/api/v1/assistant/conversations/side-chat/inputs/resolve?machine_id=server"
	code, _ := conversationSwitchTestCall(t, s, "POST", url, "reader", string(raw))
	if code != 403 {
		t.Fatalf("reader manually resolved input: %d", code)
	}
	stale := request
	stale.Input.Version++
	bad, _ := json.Marshal(stale)
	code, _ = conversationSwitchTestCall(t, s, "POST", url, "", string(bad))
	if code != 409 {
		t.Fatalf("stale CAS status=%d", code)
	}
	for range 2 {
		code, body := conversationSwitchTestCall(t, s, "POST", url, "", string(raw))
		if code != 200 {
			t.Fatalf("explicit replay: %d %s", code, body)
		}
	}
}

func TestSourceReceiptDoesNotAcceptAnAlreadyPreparingUpgradeSwitch(t *testing.T) {
	s, f, d, _ := logicalPair(t, "agy")
	st := &historicalReceiptStore{DB: d}
	f.answerNode("assistant.segment.input", func(_ string, _ json.RawMessage) (any, error) {
		return map[string]bool{"accepted": true}, nil
	})
	active := logicalFixtureTarget("agy")
	active.ID, active.SessionID, active.State = "source", "original-session", "active"
	c := &db.LogicalConversation{MachineID: "server", SessionID: "side-chat", Epoch: 3, Segments: []conversationlog.Segment{active}, Switch: &db.ConversationSwitch{OperationID: "pre-upgrade-prepared", ExpectedEpoch: 3, TargetSegment: "prepared-target", Phase: "prepared"}}
	input := db.ConversationInput{ClientMsgID: "source-reply-lost", Epoch: 3, State: "in_doubt", DispatchEpoch: 3, DispatchSegment: "source", DispatchMachine: "server", DispatchSession: "original-session"}
	if err := s.reconcileLogicalInput(t.Context(), st, c, active, input); err != nil {
		t.Fatal(err)
	}
	if !st.current || st.currentOperation != "" || st.historical != "" || c.Switch.Phase != "prepared" {
		t.Fatalf("source receipt consumed pending activation: %+v", st)
	}
}
