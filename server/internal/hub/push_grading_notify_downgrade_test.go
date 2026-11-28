package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// gradingNotifyUserPush is a notify_user-shaped push (assistantwire.NotifyPush, Asked=true) from a session on the hub's own machine.
func gradingNotifyUserPush(session, level string) assistantwire.Notification {
	n := gradingNotification(level, "", "notice from "+session)
	n.DeviceID, n.SessionID = "mac", session
	n.Asked = true
	return n
}

// mainGradingProfile marks main-conv on mac as the assistant's main conversation, the way its snapshot would report it.
func mainGradingProfile(h *Hub) {
	h.assistant.profile = &assistantwire.Profile{MachineID: "mac", Main: "main-conv"}
}

// ln mode on, notify_user from a conversation that is not the main one and without an explicit level: the notice is
// held for the briefing as P1 (no phone push, one intercept), and deliverPushFrom says why.
func TestPushGradingNotifyUserNonMainConversationHeldForBriefing(t *testing.T) {
	h, f := gradingHub(t)
	mainGradingProfile(h)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)
	out, err := h.deliverPushFrom(assistantwire.AssistantID, gradingNotifyUserPush("routine-conv", ""))
	reason := heldOf(out)
	if err != nil {
		t.Fatal(err)
	}
	if reason != interceptReasonP1 {
		t.Fatalf("held reason = %q, want %q", reason, interceptReasonP1)
	}
	if *attempts != 0 {
		t.Fatalf("got %d phone pushes, want 0 (the notice must stay in the briefing)", *attempts)
	}
	if calls := len(f.calls(t, 0)); calls != 0 {
		t.Fatalf("APNs got %d calls, want 0", calls)
	}
	view := h.push.heldPushesView("", 0)
	if len(view) != 1 || view[0].Reason != interceptReasonP1 || view[0].Level != assistantwire.LevelP1 {
		t.Fatalf("intercepts = %+v, want one P1 held entry", view)
	}
}

// ln mode on, notify_user from the main conversation: the owner still gets its notices, pushed as P0.
func TestPushGradingNotifyUserMainConversationPushes(t *testing.T) {
	h, f := gradingHub(t)
	mainGradingProfile(h)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)
	out, err := h.deliverPushFrom(assistantwire.AssistantID, gradingNotifyUserPush("main-conv", ""))
	reason := heldOf(out)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("held reason = %q, want empty (the main conversation's notice must push)", reason)
	}
	if *attempts != 1 {
		t.Fatalf("got %d phone pushes, want 1", *attempts)
	}
	if calls := len(f.calls(t, 1)); calls != 1 {
		t.Fatalf("APNs got %d calls, want 1", calls)
	}
	if view := h.push.heldPushesView("", 0); len(view) != 0 {
		t.Fatalf("got %d intercepts, want none for a pushed main-conversation notice", len(view))
	}
}

// ln mode on, an explicitly urgent notice (P0) from a non-main conversation still interrupts.
func TestPushGradingNotifyUserExplicitP0PushesFromNonMainConversation(t *testing.T) {
	h, f := gradingHub(t)
	mainGradingProfile(h)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)
	out, err := h.deliverPushFrom(assistantwire.AssistantID, gradingNotifyUserPush("supervisor-conv", assistantwire.LevelP0))
	reason := heldOf(out)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("held reason = %q, want empty for an explicit P0", reason)
	}
	if *attempts != 1 {
		t.Fatalf("got %d phone pushes, want 1", *attempts)
	}
	if calls := len(f.calls(t, 1)); calls != 1 {
		t.Fatalf("APNs got %d calls, want 1", calls)
	}
}

// With the switch off every notify_user push goes out as before, from any conversation, at its named level.
func TestPushGradingNotifyUserDowngradeIsOffWithSwitchOff(t *testing.T) {
	h, f := gradingHub(t)
	mainGradingProfile(h)
	attempts := gradingAttempts(h)
	for _, session := range []string{"main-conv", "routine-conv", "supervisor-conv"} {
		out, err := h.deliverPushFrom(assistantwire.AssistantID, gradingNotifyUserPush(session, ""))
		reason := heldOf(out)
		if err != nil {
			t.Fatal(err)
		}
		if reason != "" {
			t.Fatalf("session %s held with %q while ln mode is off", session, reason)
		}
	}
	if *attempts != 3 {
		t.Fatalf("got %d phone pushes with ln mode off, want 3", *attempts)
	}
	if calls := len(f.calls(t, 3)); calls != 3 {
		t.Fatalf("APNs got %d calls, want 3", calls)
	}
	if view := h.push.heldPushesView("", 0); len(view) != 0 {
		t.Fatalf("got %d intercepts with ln mode off, want none", len(view))
	}
}

// The downgrade only touches notify_user pushes: a finished turn (P2) keeps its derived level from a non-main
// conversation and stays silent in ln mode as before.
func TestPushGradingNotifyUserDowngradeLeavesOtherKindsAlone(t *testing.T) {
	h, _ := gradingHub(t)
	mainGradingProfile(h)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	n := gradingNotifyUserPush("routine-conv", "")
	n.Kind = assistantwire.NotifyDone
	out, err := h.deliverPushFrom(assistantwire.AssistantID, n)
	reason := heldOf(out)
	if err != nil {
		t.Fatal(err)
	}
	if reason != interceptReasonP2 {
		t.Fatalf("held reason = %q, want %q (a finished turn stays P2)", reason, interceptReasonP2)
	}
}

// gradingNotifyBusCall runs the real notify.request bus handler (the one an assistant process reaches over the wire) and
// returns its decoded answer, so the {ok, held, reason} JSON contract is covered end to end rather than mocked.
func gradingNotifyBusCall(t *testing.T, h *Hub, n assistantwire.Notification) map[string]any {
	t.Helper()
	raw, err := json.Marshal(assistantwire.NotifyRequest{Notification: n})
	if err != nil {
		t.Fatal(err)
	}
	out, err := busMethods[assistantwire.MethodNotify](h, context.Background(), nil, raw)
	if err != nil {
		t.Fatalf("notify.request: %v", err)
	}
	reply, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var res map[string]any
	if err := json.Unmarshal(reply, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

// The whole wire path: a non-main conversation's default notify_user (no level named, exactly what the tool sends)
// comes back held with the intercept reason, and nothing reaches APNs.
func TestPushGradingNotifyBusReplyHeldForNonMainConversation(t *testing.T) {
	h, f := gradingHub(t)
	mainGradingProfile(h)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)
	n := gradingNotifyUserPush("routine-conv", "") // Level empty: the assistant tool no longer prefills P1
	n.ID = "ntf_bus_held"
	res := gradingNotifyBusCall(t, h, n)
	if res["ok"] != true || res["held"] != true || res["reason"] != interceptReasonP1 {
		t.Fatalf("bus reply = %#v, want ok/held/ln_p1_held", res)
	}
	if *attempts != 0 {
		t.Fatalf("got %d phone pushes, want 0", *attempts)
	}
	if calls := len(f.calls(t, 0)); calls != 0 {
		t.Fatalf("APNs got %d calls, want 0", calls)
	}
	if view := h.push.heldPushesView("", 0); len(view) != 1 || view[0].Reason != interceptReasonP1 {
		t.Fatalf("intercepts = %+v, want one P1 entry", view)
	}
}

// The whole wire path for the main conversation: the same level-less notify_user pushes and the answer carries no
// held fields — pinning the rule the hub and the assistant tool agree on (the tool passes the empty level through).
func TestPushGradingNotifyBusReplyPushedForMainConversation(t *testing.T) {
	h, f := gradingHub(t)
	mainGradingProfile(h)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)
	n := gradingNotifyUserPush("main-conv", "")
	n.ID = "ntf_bus_main"
	res := gradingNotifyBusCall(t, h, n)
	if res["ok"] != true {
		t.Fatalf("bus reply = %#v, want ok", res)
	}
	if _, ok := res["held"]; ok {
		t.Fatalf("bus reply = %#v, must not say held for the main conversation", res)
	}
	if *attempts != 1 {
		t.Fatalf("got %d phone pushes, want 1", *attempts)
	}
	if calls := len(f.calls(t, 1)); calls != 1 {
		t.Fatalf("APNs got %d calls, want 1", calls)
	}
}

// heldOf is the ln-mode reason out says the push was held ("" when it was not, or nothing was reported).
func heldOf(out *pushOutcome) string {
	if out == nil {
		return ""
	}
	return out.held
}

// Non-Asked NotifyPush notices targeting the main conversation without an explicit level (such as plan_waiting,
// schedule_skipped, or minutes_ready) must stay at derived P1 and be held under push grading and attention modes,
// never promoted via notify_user.main. Preview supports optional asked (defaulting to true for backward compatibility).
func TestNotifyPushNonAskedMainConvStaysP1(t *testing.T) {
	h, f := gradingHub(t)
	mainGradingProfile(h)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)

	nonAsked := gradingNotification("", "plan:p1", "Plan waiting")
	nonAsked.DeviceID, nonAsked.SessionID = "mac", "main-conv"
	nonAsked.Asked = false
	out, err := h.deliverPushFrom(assistantwire.AssistantID, nonAsked)
	if err != nil {
		t.Fatal(err)
	}
	if heldOf(out) != interceptReasonP1 {
		t.Fatalf("non-Asked main-conv held = %q, want %q", heldOf(out), interceptReasonP1)
	}
	if *attempts != 0 {
		t.Fatalf("non-Asked main-conv alerted %d times, want 0", *attempts)
	}
	if view := h.push.heldPushesView("", 0); len(view) != 1 || view[0].Level != assistantwire.LevelP1 || view[0].Reason != interceptReasonP1 {
		t.Fatalf("intercepts = %+v, want one P1 held entry", view)
	}

	// Same non-Asked notice through the real notify.request bus handler returns held/reason.
	nonAskedBus := gradingNotification("", "minutes:m1", "Minutes ready")
	nonAskedBus.ID = "ntf_non_asked_main"
	nonAskedBus.DeviceID, nonAskedBus.SessionID = "mac", "main-conv"
	nonAskedBus.Asked = false
	res := gradingNotifyBusCall(t, h, nonAskedBus)
	if res["ok"] != true || res["held"] != true || res["reason"] != interceptReasonP1 {
		t.Fatalf("bus reply = %#v, want ok/held/%s", res, interceptReasonP1)
	}

	// Preview parity: asked=false keeps P1 held; omitted asked defaults to true (backward compatible P0 promotion).
	prevNonAsked := previewGradingHTTP(t, h, nil, `{"notification":{"kind":"assistant","machine_id":"mac","session_id":"main-conv","asked":false}}`)
	if prevNonAsked.Allow || !prevNonAsked.Held || prevNonAsked.EffectiveLevel != assistantwire.LevelP1 || prevNonAsked.LiveReason != interceptReasonP1 {
		t.Fatalf("preview asked=false = %+v, want held P1", prevNonAsked)
	}
	prevDefault := previewGradingHTTP(t, h, nil, `{"notification":{"kind":"assistant","machine_id":"mac","session_id":"main-conv"}}`)
	if !prevDefault.Allow || prevDefault.EffectiveLevel != assistantwire.LevelP0 {
		t.Fatalf("preview omitted asked = %+v, want backward-compatible allow P0", prevDefault)
	}

	// Explicit Asked=true notify_user from main-conv still promotes to P0 and pushes.
	askedMain := gradingNotifyUserPush("main-conv", "")
	outAsked, err := h.deliverPushFrom(assistantwire.AssistantID, askedMain)
	if err != nil {
		t.Fatal(err)
	}
	if heldOf(outAsked) != "" || *attempts != 1 {
		t.Fatalf("Asked main-conv: out=%+v attempts=%d, want delivered once", outAsked, *attempts)
	}
	f.calls(t, 1)

	// With push grading off and fragment attention mode active, non-Asked main-conv still stays P1 (not promoted to P0).
	if err := h.push.setPushGrading(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	setAttention(h, assistantwire.AttentionFragment, time.Hour, nil)
	nonAskedAttn := gradingNotification("", "sched:s1", "Schedule skipped")
	nonAskedAttn.DeviceID, nonAskedAttn.SessionID = "mac", "main-conv"
	nonAskedAttn.Asked = false
	outAttn, err := h.deliverPushFrom(assistantwire.AssistantID, nonAskedAttn)
	if err != nil {
		t.Fatal(err)
	}
	if heldOf(outAttn) != assistantwire.AttentionReasonFragmentBatch || *attempts != 1 {
		t.Fatalf("non-Asked main-conv under fragment mode: out=%+v attempts=%d, want %s", outAttn, *attempts, assistantwire.AttentionReasonFragmentBatch)
	}
}
