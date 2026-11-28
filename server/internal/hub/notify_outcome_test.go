package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// notify.request's answer for an assistant's push says where it went (#8138): no device at all, the devices it was
// handed to, an offline device that keeps it, or devices that all turned it off. A card's push reports nothing.
func TestNotifyRequestSaysWhereThePushWent(t *testing.T) {
	h := ringHub(t)
	answer := func(n assistantwire.Notification) string {
		t.Helper()
		out, err := h.deliverPushFrom(assistantwire.AssistantID, n)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(notifyAnswer(out))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	push := func() assistantwire.Notification { return gradingNotification("", "", "Report ready") }

	if got, want := answer(push()), `{"devices":0,"ok":true,"reason":"no_device"}`; got != want {
		t.Fatalf("no device: %s, want %s", got, want)
	}

	phone := ringConnect(t, h, "phone", "ws_notify")
	if got, want := answer(push()), `{"devices":1,"ok":true}`; got != want {
		t.Fatalf("a connected phone: %s, want %s", got, want)
	}
	if sent := ringSent(phone); len(sent) != 1 {
		t.Fatalf("the phone got %q", sent)
	}

	// Offline now: it keeps the notice for its next connection, and nothing took it yet.
	ringDisconnect(h, phone)
	if got, want := answer(push()), `{"devices":0,"kept":1,"ok":true,"reason":"no_device"}`; got != want {
		t.Fatalf("an offline phone: %s, want %s", got, want)
	}

	// Connected but it turned the assistant's pushes off: it could take it and chose not to.
	if err := h.push.setPrefs("tablet", NotifyPrefs{Input: true, MutedManagers: []string{assistantwire.AssistantID}}); err != nil {
		t.Fatal(err)
	}
	ringConnect(t, h, "tablet", "ws_notify")
	if got, want := answer(push()), `{"devices":0,"kept":1,"ok":true,"reason":"filtered"}`; got != want {
		t.Fatalf("a device that muted the assistant: %s, want %s", got, want)
	}

	// What does not report its delivery answers as before.
	if got, want := string(must(json.Marshal(notifyAnswer(nil)))), `{"ok":true}`; got != want {
		t.Fatalf("not reported: %s, want %s", got, want)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// NotifyInput, NotifyDecide, and NotifyReply return &pushOutcome{held} when suppressed so notify.request reports
// held/reason consistently across all notification kinds, while returning nil (no devices key) when not held.
func TestNotifyInputAndDecideReturnHeldOutcome(t *testing.T) {
	h, f := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)

	addPending := func(ix proto.Interaction) {
		ix.Manager = assistantwire.AssistantID
		h.reported.mu.Lock()
		if h.reported.interactions == nil {
			h.reported.interactions = map[string]proto.Interaction{}
		}
		h.reported.interactions[ix.ID] = ix
		h.reported.mu.Unlock()
	}

	// NotifyInput held (P1 card with grading on) -> bus reply carries held=true and reason=ln_p1_held.
	ixHeld := proto.Interaction{ID: "ix_held_p1", MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission, Summary: "git push"}
	addPending(ixHeld)
	resInputHeld := gradingNotifyBusCall(t, h, assistantwire.Notification{
		ID:          "ntf_input_held",
		Kind:        assistantwire.NotifyInput,
		Interaction: &ixHeld,
		Level:       assistantwire.LevelP1,
	})
	if resInputHeld["ok"] != true || resInputHeld["held"] != true || resInputHeld["reason"] != interceptReasonP1 {
		t.Fatalf("held NotifyInput reply = %#v, want ok/held/%s", resInputHeld, interceptReasonP1)
	}
	if *attempts != 0 {
		t.Fatalf("held NotifyInput alerted %d times, want 0", *attempts)
	}

	// NotifyInput allowed (P0 card) -> deliverPushFrom returns nil so notifyAnswer keeps devices unset.
	ixAllowed := proto.Interaction{ID: "ix_allowed_p0", MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission, Summary: "rm"}
	addPending(ixAllowed)
	outInputAllowed, err := h.deliverPushFrom(assistantwire.AssistantID, assistantwire.Notification{
		ID:          "ntf_input_ok",
		Kind:        assistantwire.NotifyInput,
		Interaction: &ixAllowed,
		Level:       assistantwire.LevelP0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if outInputAllowed != nil {
		t.Fatalf("allowed NotifyInput outcome = %+v, want nil (preserve devices==nil)", outInputAllowed)
	}
	if got := string(must(json.Marshal(notifyAnswer(outInputAllowed)))); got != `{"ok":true}` {
		t.Fatalf("allowed NotifyInput answer = %s, want {\"ok\":true}", got)
	}
	if *attempts != 1 {
		t.Fatalf("allowed NotifyInput attempts = %d, want 1", *attempts)
	}

	// NotifyDecide: first push delivers (nil outcome), duplicate within 12h returns held=ln_dedup_12h over the bus.
	askedAt := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	dec1 := todoDecision("td_outcome", askedAt, assistantwire.TodoPushDecide)
	outDec1, err := h.deliverPushFrom(assistantwire.AssistantID, dec1)
	if err != nil {
		t.Fatal(err)
	}
	if outDec1 != nil {
		t.Fatalf("allowed NotifyDecide outcome = %+v, want nil", outDec1)
	}
	if *attempts != 2 {
		t.Fatalf("allowed NotifyDecide attempts = %d, want 2", *attempts)
	}
	dec2 := todoDecision("td_outcome", askedAt.Add(time.Minute), assistantwire.TodoPushDecide)
	resDecDedup := gradingNotifyBusCall(t, h, dec2)
	if resDecDedup["ok"] != true || resDecDedup["held"] != true || resDecDedup["reason"] != interceptReasonDedup {
		t.Fatalf("deduped NotifyDecide reply = %#v, want ok/held/%s", resDecDedup, interceptReasonDedup)
	}
	if *attempts != 2 {
		t.Fatalf("deduped NotifyDecide alerted, attempts = %d, want 2", *attempts)
	}

	// NotifyReply: normal reply delivers (nil outcome); when held by immersive attention mode, returns held/reason.
	outReplyOK, err := h.deliverPushFrom(assistantwire.AssistantID, assistantwire.Notification{
		ID:         "ntf_reply_ok",
		Kind:       assistantwire.NotifyReply,
		Subtitle:   "box",
		Title:      "Agent",
		Body:       "Which branch?",
		RelatedKey: "reply:s1@1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if outReplyOK != nil {
		t.Fatalf("allowed NotifyReply outcome = %+v, want nil", outReplyOK)
	}
	if *attempts != 3 {
		t.Fatalf("allowed NotifyReply attempts = %d, want 3", *attempts)
	}

	setAttention(h, assistantwire.AttentionImmersive, time.Hour, &assistantwire.AttentionFocus{Track: "focus-track"})
	resReplyHeld := gradingNotifyBusCall(t, h, assistantwire.Notification{
		ID:         "ntf_reply_held",
		Kind:       assistantwire.NotifyReply,
		Subtitle:   "box",
		Title:      "Agent",
		Body:       "Another question",
		RelatedKey: "reply:s1@2",
	})
	if resReplyHeld["ok"] != true || resReplyHeld["held"] != true || resReplyHeld["reason"] != assistantwire.AttentionReasonImmersiveHeld {
		t.Fatalf("held NotifyReply reply = %#v, want ok/held/%s", resReplyHeld, assistantwire.AttentionReasonImmersiveHeld)
	}
	resDecHeld := gradingNotifyBusCall(t, h, todoDecision("td_outcome_immersive", askedAt, assistantwire.TodoPushDecide))
	if resDecHeld["ok"] != true || resDecHeld["held"] != true || resDecHeld["reason"] != assistantwire.AttentionReasonImmersiveHeld {
		t.Fatalf("held NotifyDecide reply = %#v, want ok/held/%s", resDecHeld, assistantwire.AttentionReasonImmersiveHeld)
	}
	if *attempts != 3 {
		t.Fatalf("held reply/decide alerted, attempts = %d, want 3", *attempts)
	}
	f.calls(t, 3)
}
