package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// nextOfType reads c's queued events until one of type typ (or fails when none is queued).
func nextOfType(t *testing.T, c *client, typ string) map[string]any {
	t.Helper()
	for {
		select {
		case b := <-c.out:
			var m map[string]any
			if json.Unmarshal(b, &m) == nil && m["type"] == typ {
				return m
			}
		default:
			t.Fatalf("no %q event queued", typ)
			return nil
		}
	}
}

func desktopNames(t *testing.T, ev map[string]any) []string {
	t.Helper()
	list, _ := ev["desktops"].([]any)
	names := []string{}
	for _, d := range list {
		m, _ := d.(map[string]any)
		names = append(names, m["name"].(string)+"/"+m["device"].(string))
	}
	return names
}

// A Mac registers as a desktop, not as the phone: every client hears about it, and it goes when it disconnects.
func TestRegisterDesktopIsAnnouncedAndGone(t *testing.T) {
	h := newTestHub(t)
	phone := &client{out: make(chan []byte, 16), subs: map[[2]string]bool{}, role: roleFull}
	mac := &client{out: make(chan []byte, 16), subs: map[[2]string]bool{}, device: "dev_mac", role: roleFull}
	h.mu.Lock()
	h.clients[phone], h.clients[mac] = struct{}{}, struct{}{}
	h.mu.Unlock()

	h.onClientMessage(context.Background(), mac, []byte(`{"type":"register_desktop","device_name":"MacBook"}`))
	if got := desktopNames(t, nextOfType(t, phone, "desktops")); len(got) != 1 || got[0] != "MacBook/dev_mac" {
		t.Fatalf("desktops = %v", got)
	}
	if !h.desktopOnline() {
		t.Fatal("a registered desktop is online")
	}
	if h.phoneClient() != nil {
		t.Fatal("a desktop must not take the phone's tool calls")
	}

	h.dropClient(mac)
	if got := desktopNames(t, nextOfType(t, phone, "desktops")); len(got) != 0 {
		t.Fatalf("after the Mac left: %v", got)
	}
	if h.desktopOnline() {
		t.Fatal("no desktop is online after it left")
	}
}

func TestUnregisterDesktopKeepsClientAndCanRegisterAgain(t *testing.T) {
	h := newTestHub(t)
	phone := &client{out: make(chan []byte, 32), subs: map[[2]string]bool{}, role: roleFull}
	desktop := &client{out: make(chan []byte, 16), subs: map[[2]string]bool{}, device: "dev_desktop", role: roleFull}
	h.mu.Lock()
	h.clients[phone], h.clients[desktop] = struct{}{}, struct{}{}
	h.mu.Unlock()

	h.onClientMessage(context.Background(), desktop, []byte(`{"type":"register_desktop","device_name":"Desktop","device_id":"push"}`))
	if got := desktopNames(t, nextOfType(t, phone, "desktops")); len(got) != 1 {
		t.Fatalf("desktops = %v", got)
	}
	h.onClientMessage(context.Background(), desktop, []byte(`{"type":"unregister_desktop"}`))
	if got := desktopNames(t, nextOfType(t, phone, "desktops")); len(got) != 0 {
		t.Fatalf("after unregister: %v", got)
	}
	if h.desktopOnline() || h.desktopOpen("push") {
		t.Fatal("withdrawn desktop still suppresses phone alerts")
	}
	if _, ok := h.clients[desktop]; !ok {
		t.Fatal("unregister must not disconnect the app")
	}
	h.onClientMessage(context.Background(), desktop, []byte(`{"type":"unregister_desktop"}`))
	select {
	case event := <-phone.out:
		t.Fatalf("idempotent unregister emitted %s", event)
	default:
	}
	h.onClientMessage(context.Background(), desktop, []byte(`{"type":"register_desktop","device_name":"Desktop"}`))
	if !h.desktopOnline() {
		t.Fatal("an active desktop can register again")
	}
}

// An answer-only credential (a watch) cannot pose as a desktop: it would mute the phone's pushes.
func TestAnswerOnlyCannotRegisterDesktop(t *testing.T) {
	h := newTestHub(t)
	phone := &client{out: make(chan []byte, 16), subs: map[[2]string]bool{}, role: roleFull}
	watch := &client{out: make(chan []byte, 16), subs: map[[2]string]bool{}, device: "dev_watch", role: "answer"}
	h.mu.Lock()
	h.clients[phone], h.clients[watch] = struct{}{}, struct{}{}
	h.mu.Unlock()
	h.onClientMessage(context.Background(), watch, []byte(`{"type":"register_desktop","device_name":"Watch"}`))
	if h.desktopOnline() {
		t.Fatal("an answer-only device registered as a desktop")
	}
}

// One Mac reconnecting has two sockets for a while: listed once, as the newer connection.
func TestOneDesktopPerDeviceNewestWins(t *testing.T) {
	h := newTestHub(t)
	phone := &client{out: make(chan []byte, 16), subs: map[[2]string]bool{}, role: roleFull}
	old := &client{out: make(chan []byte, 16), subs: map[[2]string]bool{}, device: "dev_mac", role: roleFull}
	fresh := &client{out: make(chan []byte, 16), subs: map[[2]string]bool{}, device: "dev_mac", role: roleFull}
	h.mu.Lock()
	h.clients[phone], h.clients[old], h.clients[fresh] = struct{}{}, struct{}{}, struct{}{}
	h.mu.Unlock()
	h.onClientMessage(context.Background(), old, []byte(`{"type":"register_desktop","device_name":"Old"}`))
	h.onClientMessage(context.Background(), fresh, []byte(`{"type":"register_desktop","device_name":"New"}`))
	for i := 0; i < 5; i++ { // map order is random: ask a few times
		if got := h.desktops(false); len(got) != 1 || got[0].Name != "New" {
			t.Fatalf("desktops = %+v", got)
		}
	}
}

// A client that is not a desktop leaving says nothing about desktops.
func TestDropNonDesktopIsQuiet(t *testing.T) {
	h := newTestHub(t)
	phone := &client{out: make(chan []byte, 16), subs: map[[2]string]bool{}}
	other := &client{out: make(chan []byte, 16), subs: map[[2]string]bool{}}
	h.mu.Lock()
	h.clients[phone], h.clients[other] = struct{}{}, struct{}{}
	h.mu.Unlock()
	h.dropClient(other)
	select {
	case b := <-phone.out:
		t.Fatalf("unexpected event %s", b)
	default:
	}
}

// "Phone Muted" (WeChat's): while a desktop is online a phone that chose it gets none of the pushes its settings pick;
// with no desktop, or without the choice, the settings decide as before; what was asked for explicitly still goes.
func TestMuteWhileDesktop(t *testing.T) {
	input := func(p NotifyPrefs) bool { return p.Input }
	muted := NotifyPrefs{Input: true, MuteWhileDesktop: true}
	plain := NotifyPrefs{Input: true}
	for _, c := range []struct {
		name    string
		prefs   NotifyPrefs
		desktop bool
		wants   func(NotifyPrefs) bool
		want    bool
	}{
		{"muted, Mac online", muted, true, input, false},
		{"muted, no Mac", muted, false, input, true},
		{"not muted, Mac online", plain, true, input, true},
		{"muted but the setting is off anyway", NotifyPrefs{MuteWhileDesktop: true}, false, input, false},
		{"explicit reminder, muted, Mac online", muted, true, nil, true},
	} {
		if got := alertWanted(c.wants, c.desktop)(c.prefs); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// The Live Activity a push starts says nothing while muted; it still starts.
	st := FleetState{NeedsYou: 2}
	if aps := startPayload(st, time.Now(), true)["aps"].(map[string]any); aps["alert"] != nil || aps["event"] != "start" {
		t.Fatalf("muted start %+v", aps)
	}
	if aps := startPayload(st, time.Now(), false)["aps"].(map[string]any); aps["alert"] == nil {
		t.Fatal("an unmuted start keeps its alert")
	}
	if p := devicePrefs(json.RawMessage(`{"mute_while_desktop":true}`)); !p.MuteWhileDesktop {
		t.Fatal("mute_while_desktop is stored")
	}
	if defaultPrefs().MuteWhileDesktop {
		t.Fatal("off until the phone turns it on")
	}
}
