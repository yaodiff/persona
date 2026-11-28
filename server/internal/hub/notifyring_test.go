package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ringHub is a hub whose "phone" takes "needs you" and every "Finished", with no push route (an Android phone).
func ringHub(t *testing.T) *Hub {
	t.Helper()
	h := &Hub{clients: map[*client]struct{}{}, push: newPusher(t.TempDir(), nil, t.Logf)}
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneAlways}); err != nil {
		t.Fatal(err)
	}
	return h
}

// ringConnect opens a connection of device that says client_hello with features, and returns it with the ack read.
func ringConnect(t *testing.T, h *Hub, device string, features ...string) *client {
	t.Helper()
	c := &client{device: device, out: make(chan []byte, 2*notifyRingMax)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	ringHello(t, h, c, features...)
	return c
}

// ringHello has c say client_hello again and reads its ack.
func ringHello(t *testing.T, h *Hub, c *client, features ...string) {
	t.Helper()
	hello, _ := json.Marshal(map[string]any{"type": "client_hello", "features": features})
	h.onClientMessage(context.Background(), c, hello)
	select {
	case raw := <-c.out:
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil || m["type"] != "client_hello_ack" {
			t.Fatalf("first after client_hello: %s", raw)
		}
	default:
		t.Fatal("no client_hello_ack")
	}
}

func ringDisconnect(h *Hub, c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// ringSent is what c was sent since it was last read, in order, raw.
func ringSent(c *client) [][]byte {
	var out [][]byte
	for {
		select {
		case raw := <-c.out:
			out = append(out, raw)
		default:
			return out
		}
	}
}

func ringDecoded(t *testing.T, raws [][]byte) []map[string]any {
	t.Helper()
	out := make([]map[string]any, 0, len(raws))
	for _, raw := range raws {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		out = append(out, m)
	}
	return out
}

func ringAlert(h *Hub, title string) {
	alert := notifyPayload(notifyRequest{Title: title, Level: levelActive}, "", "")
	h.notifyWSFrom("", kindAlert, func(i18n.Lang) map[string]any { return alert }, nil, quietNone)
}

func ringPending(h *Hub, ixs ...proto.Interaction) {
	f := &h.reported
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.interactions == nil {
		f.interactions = map[string]proto.Interaction{}
	}
	for _, ix := range ixs {
		f.interactions[ix.ID] = ix
	}
}

func ringResolve(h *Hub, id string) {
	f := &h.reported
	f.mu.Lock()
	delete(f.interactions, id)
	f.mu.Unlock()
}

// A question asked and a turn finished while the phone's socket was down reach it after its next client_hello, as
// the same events a connected socket got, once.
func TestNotifyRingReplaysWhatAnOfflineDeviceMissed(t *testing.T) {
	h := ringHub(t)
	if err := h.push.setPrefs("tablet", NotifyPrefs{Input: true, Done: DoneAlways}); err != nil {
		t.Fatal(err)
	}
	tablet := ringConnect(t, h, "tablet", "ws_notify") // connected all along: what a live socket gets
	phone := ringConnect(t, h, "phone", "ws_notify")
	ringDisconnect(h, phone)

	ix := proto.Interaction{ID: "ix1", MachineID: "m", SessionID: "s", Kind: proto.IxQuestion, SessionTitle: "Fix login", Summary: "Push to beta?"}
	ringPending(h, ix)
	h.notifyInteraction(assistantwire.AssistantID, ix, "box", false)
	h.notifyDone(assistantwire.AssistantID, "done", "m", "box", "s", "Fix login", "", pushWants{Kind: "done", Took: time.Hour, Long: time.Minute})
	if got := ringSent(phone); len(got) != 0 {
		t.Fatalf("a closed connection was sent %q", got)
	}
	live := ringSent(tablet)
	if len(live) != 2 {
		t.Fatalf("the connected device got %q", live)
	}

	again := ringConnect(t, h, "phone", "ws_notify")
	replayed := ringSent(again)
	if !slices.EqualFunc(replayed, live, func(a, b []byte) bool { return string(a) == string(b) }) {
		t.Fatalf("replayed %q, want what the connected device got, %q", replayed, live)
	}
	if m := ringDecoded(t, replayed); m[0]["kind"] != "interaction" || m[0]["interaction_id"] != "ix1" || m[1]["kind"] != "done" {
		t.Fatalf("replayed %v", m)
	}

	// Once: another client_hello on this connection (a language change) or the next connection gets none of them.
	ringHello(t, h, again, "ws_notify")
	if got := ringSent(again); len(got) != 0 {
		t.Fatalf("a second client_hello replayed %q", got)
	}
	ringDisconnect(h, again)
	if got := ringSent(ringConnect(t, h, "phone", "ws_notify")); len(got) != 0 {
		t.Fatalf("the next connection replayed %q", got)
	}
}

func TestNotifyRingReplaysOnlyTheDisplayedOccurrence(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprint("replacement_", replacement), func(t *testing.T) {
			h := ringHub(t)
			phone := ringConnect(t, h, "phone", "ws_notify")
			ringDisconnect(h, phone)
			ix := proto.Interaction{ID: "ix1", MachineID: "m", SessionID: "s", Kind: proto.IxPermission,
				Summary: "first", CreatedAt: time.Date(2026, 11, 25, 1, 2, 3, 123456789, time.UTC)}
			ringPending(h, ix)
			h.notifyInteraction(assistantwire.AssistantID, ix, "box", false)
			ringResolve(h, ix.ID)
			current := ix
			if replacement {
				current.Summary = "replacement"
				current.CreatedAt = current.CreatedAt.Add(time.Nanosecond)
			}
			ringPending(h, current)
			got := ringDecoded(t, ringSent(ringConnect(t, h, "phone", "ws_notify")))
			if replacement {
				if len(got) != 0 {
					t.Fatalf("replayed the old occurrence over its replacement: %v", got)
				}
			} else if len(got) != 1 || got[0]["interaction_created_at"] != ix.CreatedAt.Format(time.RFC3339Nano) {
				t.Fatalf("same occurrence did not retain its exact identity: %v", got)
			}
		})
	}
}

// What is kept is what a socket would have got at that moment: the device's settings decide when the event is
// selected (needs you, "Finished", a manager's mute, quiet hours, the capability a card needs) and are not applied
// again on replay.
func TestNotifyRingKeepsWhatTheSettingsTookThen(t *testing.T) {
	h := ringHub(t)
	h.push.now = func() time.Time { return time.Date(2026, 11, 6, 23, 30, 0, 0, time.UTC) }
	ringDisconnect(h, ringConnect(t, h, "phone", "ws_notify")) // no elicitation
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneLong, MutedManagers: []string{"other-manager"},
		Quiet: QuietHours{Enabled: true, Start: "23:00", End: "07:00", TZ: "UTC"}}); err != nil {
		t.Fatal(err)
	}
	question := proto.Interaction{ID: "ix1", MachineID: "m", SessionID: "s", Kind: proto.IxQuestion, Summary: "pick"}
	form := proto.Interaction{ID: "ix2", MachineID: "m", SessionID: "s", Kind: proto.IxElicitation, Summary: "fill in"}
	ringPending(h, question, form)

	h.notifyDone(assistantwire.AssistantID, "done", "m", "box", "s", "short", "", pushWants{Kind: "done", Took: time.Second, Long: time.Minute})
	h.notifyDone(assistantwire.AssistantID, "done", "m", "box", "s", "long, inside quiet hours", "", pushWants{Kind: "done", Took: time.Hour, Long: time.Minute})
	h.notifyDone("other-manager", "assistant", "", "Persona", "s", "muted manager", "", pushWants{})
	h.notifyInteraction(assistantwire.AssistantID, form, "box", false)     // it cannot show a form
	h.notifyInteraction(assistantwire.AssistantID, question, "box", false) // quiet
	h.notifyDone(assistantwire.AssistantID, "assistant", "", "Persona", "s", "reminder", "now", pushWants{})

	// The quiet hours are over and every switch is off by the time it comes back: what was selected still comes, as it was.
	h.push.now = func() time.Time { return time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC) }
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: false, Done: DoneOff}); err != nil {
		t.Fatal(err)
	}
	got := ringDecoded(t, ringSent(ringConnect(t, h, "phone", "ws_notify")))
	if len(got) != 2 || got[0]["kind"] != "interaction" || got[0]["interaction_id"] != "ix1" || got[0]["quiet"] != true ||
		got[1]["kind"] != "assistant" || got[1]["title"] != "reminder" || got[1]["quiet"] != nil {
		t.Fatalf("replayed %v", got)
	}
}

// A device with a push route gets its alerts from APNs, which keeps them while it is away: nothing is kept for its
// WebSocket. Once that route is gone (its token rejected), it is kept again.
func TestNotifyRingLeavesADeviceWithPushToIt(t *testing.T) {
	h := ringHub(t)
	ringDisconnect(h, ringConnect(t, h, "phone", "ws_notify"))
	pushed := Device{DeviceID: "push-registration", CredentialID: "phone", APNSToken: "token",
		RelayURL: "https://relay.example", RelayGrant: "grant", PushKey: "key"}
	h.push.mu.Lock()
	h.push.devices[pushed.DeviceID] = pushed
	h.push.mu.Unlock()
	if !h.push.routed("phone") {
		t.Fatal("the relayed registration is no push route")
	}
	ringAlert(h, "while pushed")
	if got := ringSent(ringConnect(t, h, "phone", "ws_notify")); len(got) != 0 {
		t.Fatalf("a device with push was replayed %q", got)
	}

	h.mu.Lock()
	for c := range h.clients {
		delete(h.clients, c)
	}
	h.mu.Unlock()
	h.push.mu.Lock()
	delete(h.push.devices, pushed.DeviceID)
	h.push.mu.Unlock()
	ringAlert(h, "no push")
	got := ringDecoded(t, ringSent(ringConnect(t, h, "phone", "ws_notify")))
	if len(got) != 1 || got[0]["title"] != "no push" {
		t.Fatalf("without push, replayed %v", got)
	}
}

// At most notifyRingMax events, the oldest dropped first, and none older than notifyRingMaxAge, whether it ages while
// kept or before the next one is added.
func TestNotifyRingBounds(t *testing.T) {
	h := ringHub(t)
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	h.rings.now = func() time.Time { return now }
	phone := ringConnect(t, h, "phone", "ws_notify")
	ringDisconnect(h, phone)

	for n := range notifyRingMax + 10 {
		ringAlert(h, fmt.Sprint("alert ", n))
	}
	phone = ringConnect(t, h, "phone", "ws_notify")
	got := ringDecoded(t, ringSent(phone))
	if len(got) != notifyRingMax || got[0]["title"] != "alert 10" || got[notifyRingMax-1]["title"] != fmt.Sprint("alert ", notifyRingMax+9) {
		t.Fatalf("replayed %d, from %v to %v", len(got), got[0]["title"], got[len(got)-1]["title"])
	}

	// Aged while kept: dropped at the replay.
	ringDisconnect(h, phone)
	ringAlert(h, "old")
	now = now.Add(notifyRingMaxAge)
	phone = ringConnect(t, h, "phone", "ws_notify")
	if got := ringSent(phone); len(got) != 0 {
		t.Fatalf("replayed one %v old: %q", notifyRingMaxAge, got)
	}

	// Aged before the next: dropped as that one is added.
	ringDisconnect(h, phone)
	ringAlert(h, "old")
	now = now.Add(notifyRingMaxAge + time.Second)
	ringAlert(h, "recent")
	h.mu.Lock()
	kept := len(h.rings.devices["phone"].events)
	h.mu.Unlock()
	if kept != 1 {
		t.Fatalf("kept %d, want only the recent one", kept)
	}
	now = now.Add(notifyRingMaxAge - time.Second)
	got = ringDecoded(t, ringSent(ringConnect(t, h, "phone", "ws_notify")))
	if len(got) != 1 || got[0]["title"] != "recent" {
		t.Fatalf("replayed %v", got)
	}
}

// A card answered or withdrawn while the phone was away is not replayed; the others are.
func TestNotifyRingSkipsACardResolvedMeanwhile(t *testing.T) {
	h := ringHub(t)
	ringDisconnect(h, ringConnect(t, h, "phone", "ws_notify"))
	answered := proto.Interaction{ID: "ix-answered", MachineID: "m", SessionID: "s1", Kind: proto.IxPermission, Summary: "run tests"}
	open := proto.Interaction{ID: "ix-open", MachineID: "m", SessionID: "s2", Kind: proto.IxQuestion, Summary: "which one?"}
	ringPending(h, answered, open)
	h.notifyInteraction(assistantwire.AssistantID, answered, "box", false)
	h.notifyInteraction(assistantwire.AssistantID, open, "box", false)
	h.notifyDone(assistantwire.AssistantID, "done", "m", "box", "s1", "finished", "", pushWants{Kind: "done", Took: time.Hour, Long: time.Minute})
	ringResolve(h, answered.ID)

	got := ringDecoded(t, ringSent(ringConnect(t, h, "phone", "ws_notify")))
	if len(got) != 2 || got[0]["interaction_id"] != open.ID || got[1]["kind"] != "done" {
		t.Fatalf("replayed %v", got)
	}
}

// An event sent to a connected socket is not kept as well; one selected while the device's connection is open but
// before its client_hello said ws_notify is kept and comes right after that hello, once.
func TestNotifyRingNoDuplicateWhileConnected(t *testing.T) {
	h := ringHub(t)
	phone := ringConnect(t, h, "phone", "ws_notify")
	ringAlert(h, "live")
	if got := ringDecoded(t, ringSent(phone)); len(got) != 1 || got[0]["title"] != "live" {
		t.Fatalf("connected: %v", got)
	}
	ringDisconnect(h, phone)
	if got := ringSent(ringConnect(t, h, "phone", "ws_notify")); len(got) != 0 {
		t.Fatalf("replayed what the connected socket got: %q", got)
	}

	h.mu.Lock()
	for c := range h.clients {
		delete(h.clients, c)
	}
	h.mu.Unlock()
	opening := &client{device: "phone", out: make(chan []byte, 8)}
	h.mu.Lock()
	h.clients[opening] = struct{}{}
	h.mu.Unlock()
	ringAlert(h, "before hello")
	if got := ringSent(opening); len(got) != 0 {
		t.Fatalf("sent before its client_hello: %q", got)
	}
	ringHello(t, h, opening, "ws_notify")
	if got := ringDecoded(t, ringSent(opening)); len(got) != 1 || got[0]["title"] != "before hello" {
		t.Fatalf("after its client_hello: %v", got)
	}
	ringAlert(h, "after hello")
	if got := ringDecoded(t, ringSent(opening)); len(got) != 1 || got[0]["title"] != "after hello" {
		t.Fatalf("once it said ws_notify: %v", got)
	}
}

// Only devices that said ws_notify are kept for, at most notifyRingDevices of them; a hello without it, or revoking
// the credential, forgets the device and what was kept for it.
func TestNotifyRingRemembersOnlyTheDevicesThatTakeIt(t *testing.T) {
	h := newTestHub(t)
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	h.rings.now = func() time.Time { return now }
	remembered := func(device string) bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		_, ok := h.rings.devices[device]
		return ok
	}

	ringDisconnect(h, ringConnect(t, h, "legacy", "app_ping"))
	ringDisconnect(h, ringConnect(t, h, "pair_abc", "ws_notify"))
	ringDisconnect(h, ringConnect(t, h, "", "ws_notify"))
	if remembered("legacy") || remembered("pair_abc") || remembered("") {
		t.Fatal("remembered a device that takes no notify events, or a connection naming no device")
	}

	for n := range notifyRingDevices + 1 {
		now = now.Add(time.Second)
		ringDisconnect(h, ringConnect(t, h, fmt.Sprint("device-", n), "ws_notify"))
	}
	h.mu.Lock()
	count := len(h.rings.devices)
	h.mu.Unlock()
	if count != notifyRingDevices || remembered("device-0") || !remembered("device-1") {
		t.Fatalf("remembered %d devices, device-0 %v", count, remembered("device-0"))
	}

	ringDisconnect(h, ringConnect(t, h, "device-1", "app_ping"))
	if remembered("device-1") {
		t.Fatal("a hello without ws_notify kept the device")
	}
	h.revoked("device-2")
	if remembered("device-2") {
		t.Fatal("revoking the credential kept its ring")
	}
}
