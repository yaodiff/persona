package hub

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// lnNotifyUserPush is a notify_user-shaped push (assistantwire.NotifyPush) from a session on the hub's own machine.
func lnNotifyUserPush(session, level string) assistantwire.Notification {
	n := lnNotification(level, "", "notice from "+session)
	n.DeviceID, n.SessionID = "mac", session
	return n
}

// lnMainProfile marks main-conv on mac as the assistant's main conversation, the way its snapshot would report it.
func lnMainProfile(h *Hub) {
	h.assistant.profile = &assistantwire.Profile{MachineID: "mac", Main: "main-conv"}
}

// ln mode on, notify_user from a conversation that is not the main one and without an explicit level: the notice is
// held for the briefing as P1 (no phone push, one intercept), and deliverPushFrom says why.
func TestLnNotifyUserNonMainConversationHeldForBriefing(t *testing.T) {
	h, f := lnHub(t)
	lnMainProfile(h)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := lnAttempts(h)
	reason, err := h.deliverPushFrom(assistantwire.AssistantID, lnNotifyUserPush("routine-conv", ""))
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
	view := h.push.lnInterceptsView("", 0)
	if len(view) != 1 || view[0].Reason != interceptReasonP1 || view[0].Level != assistantwire.LevelP1 {
		t.Fatalf("intercepts = %+v, want one P1 held entry", view)
	}
}

// ln mode on, notify_user from the main conversation: the owner still gets its notices, pushed as P0.
func TestLnNotifyUserMainConversationPushes(t *testing.T) {
	h, f := lnHub(t)
	lnMainProfile(h)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := lnAttempts(h)
	reason, err := h.deliverPushFrom(assistantwire.AssistantID, lnNotifyUserPush("main-conv", ""))
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
	if view := h.push.lnInterceptsView("", 0); len(view) != 0 {
		t.Fatalf("got %d intercepts, want none for a pushed main-conversation notice", len(view))
	}
}

// ln mode on, an explicitly urgent notice (P0) from a non-main conversation still interrupts.
func TestLnNotifyUserExplicitP0PushesFromNonMainConversation(t *testing.T) {
	h, f := lnHub(t)
	lnMainProfile(h)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := lnAttempts(h)
	reason, err := h.deliverPushFrom(assistantwire.AssistantID, lnNotifyUserPush("supervisor-conv", assistantwire.LevelP0))
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
func TestLnNotifyUserDowngradeIsOffWithSwitchOff(t *testing.T) {
	h, f := lnHub(t)
	lnMainProfile(h)
	attempts := lnAttempts(h)
	for _, session := range []string{"main-conv", "routine-conv", "supervisor-conv"} {
		reason, err := h.deliverPushFrom(assistantwire.AssistantID, lnNotifyUserPush(session, ""))
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
	if view := h.push.lnInterceptsView("", 0); len(view) != 0 {
		t.Fatalf("got %d intercepts with ln mode off, want none", len(view))
	}
}

// The downgrade only touches notify_user pushes: a finished turn (P2) keeps its derived level from a non-main
// conversation and stays silent in ln mode as before.
func TestLnNotifyUserDowngradeLeavesOtherKindsAlone(t *testing.T) {
	h, _ := lnHub(t)
	lnMainProfile(h)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	n := lnNotifyUserPush("routine-conv", "")
	n.Kind = assistantwire.NotifyDone
	reason, err := h.deliverPushFrom(assistantwire.AssistantID, n)
	if err != nil {
		t.Fatal(err)
	}
	if reason != interceptReasonP2 {
		t.Fatalf("held reason = %q, want %q (a finished turn stays P2)", reason, interceptReasonP2)
	}
}

// lnNotifyBusCall runs the real notify.request bus handler (the one an assistant process reaches over the wire) and
// returns its decoded answer, so the {ok, held, reason} JSON contract is covered end to end rather than mocked.
func lnNotifyBusCall(t *testing.T, h *Hub, n assistantwire.Notification) map[string]any {
	t.Helper()
	raw, err := json.Marshal(assistantwire.NotifyRequest{Notification: n})
	if err != nil {
		t.Fatal(err)
	}
	out, err := busMethods[assistantwire.MethodNotify](h, context.Background(), nil, raw)
	if err != nil {
		t.Fatalf("notify.request: %v", err)
	}
	res, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("notify.request answered %#v, want a JSON object", out)
	}
	return res
}

// The whole wire path: a non-main conversation's default notify_user (no level named, exactly what the tool sends)
// comes back held with the intercept reason, and nothing reaches APNs.
func TestLnNotifyBusReplyHeldForNonMainConversation(t *testing.T) {
	h, f := lnHub(t)
	lnMainProfile(h)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := lnAttempts(h)
	n := lnNotifyUserPush("routine-conv", "") // Level empty: the assistant tool no longer prefills P1
	n.ID = "ntf_bus_held"
	res := lnNotifyBusCall(t, h, n)
	if res["ok"] != true || res["held"] != true || res["reason"] != interceptReasonP1 {
		t.Fatalf("bus reply = %#v, want ok/held/ln_p1_held", res)
	}
	if *attempts != 0 {
		t.Fatalf("got %d phone pushes, want 0", *attempts)
	}
	if calls := len(f.calls(t, 0)); calls != 0 {
		t.Fatalf("APNs got %d calls, want 0", calls)
	}
	if view := h.push.lnInterceptsView("", 0); len(view) != 1 || view[0].Reason != interceptReasonP1 {
		t.Fatalf("intercepts = %+v, want one P1 entry", view)
	}
}

// The whole wire path for the main conversation: the same level-less notify_user pushes and the answer carries no
// held fields — pinning the rule the hub and the assistant tool agree on (the tool passes the empty level through).
func TestLnNotifyBusReplyPushedForMainConversation(t *testing.T) {
	h, f := lnHub(t)
	lnMainProfile(h)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := lnAttempts(h)
	n := lnNotifyUserPush("main-conv", "")
	n.ID = "ntf_bus_main"
	res := lnNotifyBusCall(t, h, n)
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
