package assistant

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/wireids"
)

func logicalUndeliveredFor(t *testing.T, s *Service, machine, session string) []proto.UndeliveredMessage {
	t.Helper()
	views, ok := s.logicalUndeliveredViews(t.Context())
	if !ok {
		t.Fatal("undelivered logical input could not be read")
	}
	return views[[2]string{machine, session}]
}

func undeliveredByID(list []proto.UndeliveredMessage, id string) (proto.UndeliveredMessage, bool) {
	for _, m := range list {
		if m.ID == id {
			return m, true
		}
	}
	return proto.UndeliveredMessage{}, false
}

// The reported bug: a message sent behind an unconfirmed one was answered "ok" and then nothing showed. Now the
// send answers with its bubble and why, the row shows both inputs, and past the deadline the unconfirmed one expires
// (never sent again) while the queue behind it goes on by itself. Retry sends it exactly once more; Delete drops it.
func TestLogicalUnconfirmedInputExpiresReleasesQueueAndRetriesOnce(t *testing.T) {
	s, f, d, _ := logicalPair(t, "pi")
	ctx := t.Context()
	activatePair(t, d)
	sends := map[string]int{}
	texts := map[string]string{}
	lose := true
	f.answerNode(DeviceAssistantSend, func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		id, _ := p["client_msg_id"].(string)
		sends[id]++
		texts[id], _ = p["text"].(string)
		if lose && id == "lost" {
			return nil, errors.New("fake connection lost after acceptance")
		}
		return map[string]any{"session_id": "target-chat"}, nil
	})
	receipts := 0
	f.answerNode("assistant.segment.input", func(_ string, _ json.RawMessage) (any, error) {
		receipts++
		return map[string]bool{"accepted": false}, nil // absent: uncertain
	})
	now := time.Now()
	s.logicalInputClock = func() time.Time { return now }

	raw, _ := json.Marshal(map[string]any{"session_id": "side-chat", "client_msg_id": "lost", "text": "first words"})
	if _, _, err := s.logicalCall(ctx, "server", DeviceAssistantSend, raw); err != nil {
		t.Fatal(err)
	}
	c, _ := d.LogicalConversation(ctx, "server", "side-chat")
	_ = s.drainLogicalInput(ctx, d, c) // dispatched; the reply is lost: in doubt

	// Sent now, behind the unconfirmed one: the answer says so at once, with the bubble.
	raw, _ = json.Marshal(map[string]any{"session_id": "side-chat", "client_msg_id": "later", "text": "second words"})
	out, _, err := s.logicalCall(ctx, "server", DeviceAssistantSend, raw)
	if err != nil {
		t.Fatal(err)
	}
	var reply logicalQueuedReply
	if err := json.Unmarshal(out, &reply); err != nil || !reply.Queued || reply.Delivered != logicalDeliveredLater || reply.Undelivered == nil {
		t.Fatalf("queued send answered as a plain success: %s %v", out, err)
	}
	if u := reply.Undelivered; u.ID != "later" || u.State != proto.UndeliveredWaiting || u.Reason != proto.UndeliveredBehindUnconfirmed || u.BlockedBy != wireids.ClientMsgID("lost") || u.Ahead != 1 || u.Text != "second words" || u.Error == "" {
		t.Fatalf("queued bubble does not say where it waits: %+v", u)
	}
	list := logicalUndeliveredFor(t, s, "server", "side-chat")
	head, ok := undeliveredByID(list, "lost")
	if !ok || head.Reason != proto.UndeliveredAwaitingReceipt || head.State != proto.UndeliveredWaiting || head.Behind != 1 || head.Deadline.Sub(head.Since) != logicalInputDeliveryDeadline {
		t.Fatalf("unconfirmed head not shown with its deadline: %+v", list)
	}
	row := proto.Session{State: proto.StateIdle, Undelivered: &list}
	if got := logicalDeliveryState(row); got != proto.StateWorking {
		t.Fatalf("row with undelivered input shown as %s", got)
	}

	// Within the deadline: asked again, still waiting, nothing sent again, nothing overtakes.
	for range 2 {
		c, _ = d.LogicalConversation(ctx, "server", "side-chat")
		_ = s.drainLogicalInput(ctx, d, c)
	}
	if sends["lost"] != 1 || sends["later"] != 0 || receipts == 0 {
		t.Fatalf("ambiguous input repeated or overtaken before its deadline: %v receipts=%d", sends, receipts)
	}

	// Past it: expired, the queue goes on by itself in the same pass.
	now = now.Add(logicalInputDeliveryDeadline + time.Second)
	c, _ = d.LogicalConversation(ctx, "server", "side-chat")
	if err := s.drainLogicalInput(ctx, d, c); err != nil {
		t.Fatal(err)
	}
	if sends["lost"] != 1 || sends["later"] != 1 {
		t.Fatalf("expiry did not release the queue exactly once: %v", sends)
	}
	list = logicalUndeliveredFor(t, s, "server", "side-chat")
	expired, ok := undeliveredByID(list, "lost")
	if !ok || len(list) != 1 || expired.State != proto.UndeliveredFailed || expired.Reason != proto.UndeliveredReceiptExpired || expired.Text != "first words" {
		t.Fatalf("expired input not kept for its owner: %+v", list)
	}
	row = proto.Session{State: proto.StateIdle, Undelivered: &list}
	if got := logicalDeliveryState(row); got != proto.StateNeedsYou {
		t.Fatalf("expired input does not ask its owner: %s", got)
	}
	c, _ = d.LogicalConversation(ctx, "server", "side-chat")
	if c.Switch != nil {
		t.Fatalf("switch did not settle after its first input expired: %+v", c.Switch)
	}
	// The switch context the lost first turn carried goes with the next input instead.
	if !strings.Contains(texts["later"], "fake source summary") || !strings.Contains(texts["later"], "second words") {
		t.Fatalf("expired first input dropped the switch context: %q", texts["later"])
	}
	for range 2 {
		c, _ = d.LogicalConversation(ctx, "server", "side-chat")
		if err := s.drainLogicalInput(ctx, d, c); err != nil {
			t.Fatal(err)
		}
	}
	if sends["lost"] != 1 {
		t.Fatalf("expired input sent again by itself: %v", sends)
	}

	// Retry: a reader cannot; the owner's second tap does nothing more.
	lose = false
	path := "/api/v1/assistant/conversations/side-chat/undelivered/lost/retry"
	if code, _ := conversationSwitchTestCall(t, s, "POST", path, "reader", "{}"); code != 403 {
		t.Fatalf("reader retried: %d", code)
	}
	code, body := conversationSwitchTestCall(t, s, "POST", path, "", "{}")
	if code != 200 || strings.Contains(string(body), "repeated") {
		t.Fatalf("retry: %d %s", code, body)
	}
	code, body = conversationSwitchTestCall(t, s, "POST", path, "", "{}")
	if code != 200 || !strings.Contains(string(body), `"repeated":true`) {
		t.Fatalf("second tap: %d %s", code, body)
	}
	for range 2 {
		c, _ = d.LogicalConversation(ctx, "server", "side-chat")
		if err := s.drainLogicalInput(ctx, d, c); err != nil {
			t.Fatal(err)
		}
	}
	if sends["lost"] != 2 {
		t.Fatalf("retry did not send exactly once more: %v", sends)
	}
	if list := logicalUndeliveredFor(t, s, "server", "side-chat"); len(list) != 0 {
		t.Fatalf("delivered input still shown: %+v", list)
	}
	// Delete on a delivered input changes nothing.
	if code, _ := conversationSwitchTestCall(t, s, "DELETE", "/api/v1/assistant/conversations/side-chat/undelivered/lost", "", ""); code != 409 {
		t.Fatalf("delete of a delivered input: %d", code)
	}
}

func TestLogicalExpiredInputDeleteIsAuditedAndIdempotent(t *testing.T) {
	s, f, d, _ := logicalPair(t, "pi")
	ctx := t.Context()
	activatePair(t, d)
	sends := 0
	f.answerNode(DeviceAssistantSend, func(string, json.RawMessage) (any, error) {
		sends++
		return nil, errors.New("fake connection lost after acceptance")
	})
	f.answerNode("assistant.segment.input", func(string, json.RawMessage) (any, error) {
		return nil, errors.New("fake target unreachable") // receipt cannot be read at all
	})
	now := time.Now()
	s.logicalInputClock = func() time.Time { return now }
	raw, _ := json.Marshal(map[string]any{"session_id": "side-chat", "client_msg_id": "gone", "text": "drop me"})
	if _, _, err := s.logicalCall(ctx, "server", DeviceAssistantSend, raw); err != nil {
		t.Fatal(err)
	}
	c, _ := d.LogicalConversation(ctx, "server", "side-chat")
	_ = s.drainLogicalInput(ctx, d, c)
	path := "/api/v1/assistant/conversations/side-chat/undelivered/gone"
	if code, _ := conversationSwitchTestCall(t, s, "DELETE", path, "", ""); code != 409 {
		t.Fatalf("input still awaiting its receipt was deleted: %d", code)
	}
	now = now.Add(logicalInputDeliveryDeadline + time.Second)
	c, _ = d.LogicalConversation(ctx, "server", "side-chat")
	if err := s.drainLogicalInput(ctx, d, c); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if code, body := conversationSwitchTestCall(t, s, "DELETE", path, "", ""); code != 200 {
			t.Fatalf("delete: %d %s", code, body)
		}
	}
	if code, _ := conversationSwitchTestCall(t, s, "POST", path+"/retry", "", "{}"); code != 409 {
		t.Fatalf("retry after delete: %d", code)
	}
	if list := logicalUndeliveredFor(t, s, "server", "side-chat"); len(list) != 0 || sends != 1 {
		t.Fatalf("deleted input still shown or sent again: %+v sends=%d", list, sends)
	}
}

func TestLogicalUndeliveredMessagesSayWhyAndWhere(t *testing.T) {
	at := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	in := func(machine, session, id, state string) db.ConversationUndeliveredInput {
		return db.ConversationUndeliveredInput{LogicalMachine: machine, LogicalSession: session, Input: db.ConversationInput{ClientMsgID: id, State: state, ClaimedAt: at}, Text: id, CreatedAt: at, ExpiredAt: at.Add(time.Minute)}
	}
	switching := in("m", "b", "q2", "queued")
	switching.SwitchPending = true
	got := logicalUndeliveredMessages(i18n.Lang("en"), []db.ConversationUndeliveredInput{
		in("m", "a", "old", db.ConversationInputExpired), in("m", "a", "head", "in_doubt"), in("m", "a", "q1", "queued"), in("m", "a", "q2", "queued"),
		switching,
	}, 2*time.Minute)
	a := got[[2]string{"m", "a"}]
	if len(a) != 4 || a[0].Reason != proto.UndeliveredReceiptExpired || a[0].State != proto.UndeliveredFailed || !a[0].Since.Equal(at.Add(time.Minute)) {
		t.Fatalf("expired: %+v", a)
	}
	if a[1].Reason != proto.UndeliveredAwaitingReceipt || a[1].Behind != 2 || !a[1].Deadline.Equal(at.Add(2*time.Minute)) {
		t.Fatalf("head: %+v", a[1])
	}
	if a[2].Reason != proto.UndeliveredBehindUnconfirmed || a[2].BlockedBy != "head" || a[2].Ahead != 1 || a[3].Ahead != 2 {
		t.Fatalf("queued behind: %+v %+v", a[2], a[3])
	}
	if b := got[[2]string{"m", "b"}]; len(b) != 1 || b[0].Reason != proto.UndeliveredSwitchPending || b[0].Ahead != 0 {
		t.Fatalf("switching: %+v", b)
	}
	for _, m := range append(a, got[[2]string{"m", "b"}]...) {
		if m.Error == "" || strings.HasPrefix(m.Error, "assistant-undelivered.") || !m.Reason.Valid() {
			t.Fatalf("line missing for %+v", m)
		}
	}
	if _, ok := logicalInputDeadlineFrom("10s"); ok {
		t.Fatal("deadline below its floor accepted")
	}
	if d, ok := logicalInputDeadlineFrom("3m"); !ok || d != 3*time.Minute {
		t.Fatal("deadline override refused")
	}
}

func TestLogicalDeliveryStateLeavesOtherRowsAlone(t *testing.T) {
	kept := []proto.UndeliveredMessage{{ID: "k", State: proto.UndeliveredFailed}} // #10194's kept message: no reason
	for _, row := range []proto.Session{{State: proto.StateIdle}, {State: proto.StateIdle, Undelivered: &kept}} {
		if got := logicalDeliveryState(row); got != proto.StateIdle {
			t.Fatalf("row changed: %s", got)
		}
	}
	waiting := []proto.UndeliveredMessage{{ID: "w", State: proto.UndeliveredWaiting, Reason: proto.UndeliveredQueued}}
	if got := logicalDeliveryState(proto.Session{State: proto.StateNeedsYou, Undelivered: &waiting}); got != proto.StateNeedsYou {
		t.Fatalf("needs-you lowered to %s", got)
	}
}
