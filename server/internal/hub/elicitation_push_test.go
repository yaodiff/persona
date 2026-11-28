package hub

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// relayPhone registers another phone with the relay h's phone uses (relayHub), with its own token and key.
func relayPhone(t *testing.T, h *Hub, id, token string, features ...string) {
	t.Helper()
	phone, _ := h.push.device("phone")
	resp, err := http.Post(phone.RelayURL+"/v1/register", "application/json",
		strings.NewReader(`{"apns_token":"`+token+`","env":"sandbox","topic":"app.example"}`))
	if err != nil {
		t.Fatal(err)
	}
	var reg struct{ Grant string }
	_ = json.NewDecoder(resp.Body).Decode(&reg)
	resp.Body.Close()
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	if err := h.push.register(Device{DeviceID: id, Name: id, Kind: "iphone", APNSToken: token, APNSEnv: "sandbox", Topic: "app.example",
		PushKey: base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), RelayURL: phone.RelayURL + "/", RelayGrant: reg.Grant,
		Features: features}); err != nil {
		t.Fatal(err)
	}
}

// cardWentTo is the devices a card's push went to, as sendCard noted them for taking it back.
func cardWentTo(h *Hub, id string) map[string]bool {
	w := &h.push.withdrawals
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.pushed[id].devices)
}

// An MCP form or link (#2433) is pushed only to the phones that registered `elicitation`: an app from before it gets
// none, as before, while every other card still goes to both. The push names the MCP server and says what it asks,
// without the link it opens or a form's values, has no buttons (a tap opens the conversation), and is taken back
// when the card is over elsewhere.
func TestElicitationPushGoesOnlyToPhonesThatTakeIt(t *testing.T) {
	h, f, priv := relayHub(t)
	h.push.withdrawals.gather = 60 * time.Millisecond
	phone, _ := h.push.device("phone")
	phone.Features = []string{"withdraw", "elicitation", "some_future_feature"}
	if err := h.push.register(phone); err != nil {
		t.Fatal(err)
	}
	if d, _ := h.push.device("phone"); !slices.Equal(d.Features, []string{"withdraw", "elicitation"}) {
		t.Fatalf("kept features %v", d.Features)
	}
	relayPhone(t, h, "older-phone", "b1b2c3d4e5f60718a1b2c3d4e5f60718", "withdraw") // an app from before #2433
	lang := h.push.langOf(phone)
	alertOf := func(r map[string]any) (map[string]any, map[string]any) {
		aps, _ := r["aps"].(map[string]any)
		alert, _ := aps["alert"].(map[string]any)
		return aps, alert
	}

	form := proto.Interaction{ID: "ix_form", Kind: proto.IxElicitation, MachineID: "m1", SessionID: "s1", SessionTitle: "Fix the login",
		MCPServerName: "example-mcp", Summary: "Choose the environment", Elicitation: &proto.Elicitation{Mode: "form",
			Message: "Choose the environment", RequestedSchema: json.RawMessage(`{"type":"object","properties":{"env":{"type":"string","default":"staging-value"}}}`)}}
	h.notifyInteraction("", form, "Laptop", false)
	if went := cardWentTo(h, form.ID); len(went) != 1 || !went["phone"] {
		t.Fatalf("the form was pushed to %v", went)
	}
	r := f.wait(t, 1)[0]
	if r.Token != phone.APNSToken || r.PushType != "alert" {
		t.Fatalf("push %+v", r)
	}
	p := opened(t, priv, r)
	if raw, _ := json.Marshal(p); strings.Contains(string(raw), "staging-value") || strings.Contains(string(raw), "requested_schema") {
		t.Fatalf("the form is in its push: %s", raw)
	}
	aps, alert := alertOf(p)
	if aps["category"] != "INTERACTION_ELICITATION" || p["ask"] != nil || aps["interruption-level"] != "time-sensitive" {
		t.Fatalf("aps %v ask %v", aps, p["ask"])
	}
	if alert["title"] != i18n.T(lang, "push.elicitation_server_title", i18n.Args{"server": "example-mcp"}) ||
		alert["body"] != "Choose the environment"+i18n.T(lang, "push.fill_in_suffix") {
		t.Fatalf("alert %v", alert)
	}
	if p["interaction_id"] != form.ID || p["machine_id"] != "m1" || p["session_id"] != "s1" {
		t.Fatalf("where it opens: %v", p)
	}

	link := proto.Interaction{ID: "ix_link", Kind: proto.IxElicitation, MachineID: "m1", SessionID: "s1", MCPServerName: "example-mcp",
		Summary: "Authorize at https://auth.example.com/authorize?token=secret-value.", Elicitation: &proto.Elicitation{Mode: "url",
			Message: "Authorize at https://auth.example.com/authorize?token=secret-value.", URL: "https://auth.example.com/authorize?token=secret-value",
			ID: "request_example"}}
	h.notifyInteraction("", link, "Laptop", false)
	if went := cardWentTo(h, link.ID); len(went) != 1 || !went["phone"] {
		t.Fatalf("the link was pushed to %v", went)
	}
	p = opened(t, priv, f.wait(t, 2)[1])
	if raw, _ := json.Marshal(p); strings.Contains(string(raw), "secret-value") || strings.Contains(string(raw), "/authorize") {
		t.Fatalf("the link is in its push: %s", raw)
	}
	if _, alert := alertOf(p); alert["body"] != "Authorize at auth.example.com." {
		t.Fatalf("alert %v", alert)
	}

	// Answered elsewhere: taken back from the phone it went to.
	h.push.withdraw(link.ID)
	bg := f.wait(t, 3)[2]
	if ids, _ := opened(t, priv, bg)["resolved_interaction_ids"].([]any); bg.PushType != "background" || len(ids) != 1 || ids[0] != link.ID {
		t.Fatalf("withdrawal %+v %v", bg, ids)
	}

	// Every other card goes to both phones, as ever.
	h.notifyInteraction("", card("ix_permission"), "Laptop", false)
	if went := cardWentTo(h, "ix_permission"); len(went) != 2 {
		t.Fatalf("a permission card was pushed to %v", went)
	}
}

// The alert never carries a link, whatever the server wrote around it.
func TestElicitationAlertBodyCutsLinksToTheirHost(t *testing.T) {
	l := i18n.Lang("en")
	for msg, want := range map[string]string{
		"Open https://auth.example.com/a?code=x to continue":      "Open auth.example.com to continue",
		"授权：https://auth.example.com/a?code=x。然后回来":               "授权：auth.example.com。然后回来",
		"See (http://example.com:8080/path) and [HTTPS://b.test]": "See (example.com) and [b.test]",
		"Go to https://?x=1, then":                                "Go to …, then",
		"":                                                        i18n.T(l, "push.elicitation_body"),
	} {
		ix := proto.Interaction{Kind: proto.IxElicitation, Elicitation: &proto.Elicitation{Mode: "url", Message: msg}}
		if got := elicitationAlertBody(l, ix); got != want {
			t.Errorf("%q: %q, want %q", msg, got, want)
		}
	}
}

// fleet.clients' `elicitation_push`: a phone registered for MCP forms and links whose card push would go now. Not an
// app from before (or the watch), which never registers it; not a device without a way to push it (notifications off,
// Android and browsers, which have no token); not while "Needs you" is off or Phone Muted holds pushes back for an
// open desktop app. An older hub, which knew only the names before it, drops the feature: nothing waits there.
func TestPushesElicitation(t *testing.T) {
	if (*Pusher)(nil).pushesElicitation(false) {
		t.Fatal("no pusher")
	}
	p := newPusher(t.TempDir(), nil, func(string, ...any) {})
	relayed := func(id, token string, features ...string) Device {
		return Device{DeviceID: id, Name: id, Kind: "iphone", APNSToken: token, APNSEnv: "sandbox", Topic: "app.example",
			PushKey: "key-" + id, RelayURL: "https://relay.example.com/relay", RelayGrant: "grant-" + id, Features: features}
	}
	register := func(d Device) {
		t.Helper()
		if err := p.register(d); err != nil {
			t.Fatal(err)
		}
	}
	register(relayed("older", "c1b2c3d4e5f60718a1b2c3d4e5f60718", "withdraw"))
	register(relayed("watch", "d1b2c3d4e5f60718a1b2c3d4e5f60718"))
	register(relayed("no-token", "", "elicitation"))
	if p.pushesElicitation(false) {
		t.Fatal("no phone that takes them can be pushed")
	}
	register(relayed("phone", "e1b2c3d4e5f60718a1b2c3d4e5f60718", "withdraw", "elicitation"))
	if !p.pushesElicitation(false) {
		t.Fatal("the phone that takes them")
	}
	if err := p.setPrefs("phone", NotifyPrefs{Input: true, MuteWhileDesktop: true}); err != nil {
		t.Fatal(err)
	}
	if !p.pushesElicitation(false) || p.pushesElicitation(true) {
		t.Fatal("Phone Muted holds them back only while a desktop app is open")
	}
	if err := p.setPrefs("phone", NotifyPrefs{Input: false}); err != nil {
		t.Fatal(err)
	}
	if p.pushesElicitation(false) {
		t.Fatal(`"Needs you" off`)
	}
	if err := p.setPrefs("phone", NotifyPrefs{Input: true}); err != nil {
		t.Fatal(err)
	}

	known := knownFeatures
	knownFeatures = map[string]bool{featureWithdraw: true, compat.Feature: true} // an older hub's
	t.Cleanup(func() { knownFeatures = known })
	register(relayed("phone", "e1b2c3d4e5f60718a1b2c3d4e5f60718", "withdraw", "elicitation"))
	if d, _ := p.device("phone"); hasFeature(d, featureElicitation) || p.pushesElicitation(false) {
		t.Fatalf("an older hub kept %v", d.Features)
	}
}

// connectClientsManager connects manager id on the bus, handing each fleet.clients it hears to got.
func connectClientsManager(t *testing.T, h *Hub, srv *httptest.Server, id string, got chan<- assistantwire.Clients) *bus.Client {
	t.Helper()
	c := &bus.Client{Hub: srv.URL, Token: busToken(t, h, id, id), HelloMethod: busHello,
		HelloParams: func() any { return map[string]any{"id": id} },
		OnRequest:   echoService,
		OnEvent: func(event string, params json.RawMessage) {
			var v assistantwire.Clients
			if event == assistantwire.EventClients && json.Unmarshal(params, &v) == nil {
				got <- v
			}
		},
		Backoff: 20 * time.Millisecond, MaxBackoff: 300 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	wctx, wcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer wcancel()
	if err := c.Wait(wctx); err != nil {
		t.Fatal(err)
	}
	return c
}

// The managers hear when a phone that takes MCP forms and links by push comes or goes, and when its settings change,
// as `elicitation_push`; a manager that connects later hears what is true then. `elicitation` keeps meaning a
// connected app, so a manager from before the field, which reads only that, still hands such a request back at once.
func TestFleetClientsSaysWhenAPhoneTakesThemByPush(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.push.devicesChanged = func() { go h.fleetClientsChanged() } // as the hub wires it (hub.go)
	got := make(chan assistantwire.Clients, 16)
	connectClientsManager(t, h, srv, assistantwire.AssistantID, got)
	waitFor := func(got <-chan assistantwire.Clients, want assistantwire.Clients, what string) {
		t.Helper()
		for {
			select {
			case c := <-got:
				if c.Elicitation == want.Elicitation && c.ElicitationPush == want.ElicitationPush {
					return
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s: no fleet.clients %+v", what, want)
			}
		}
	}
	next := func(want assistantwire.Clients, what string) { t.Helper(); waitFor(got, want, what) }
	next(assistantwire.Clients{}, "at the handshake")
	phone := Device{DeviceID: "phone", Name: "phone", Kind: "iphone", APNSToken: "f1b2c3d4e5f60718a1b2c3d4e5f60718", APNSEnv: "sandbox",
		Topic: "app.example", PushKey: "key", RelayURL: "https://relay.example.com/relay", RelayGrant: "grant", Credential: "credential-example",
		Features: []string{"elicitation"}}
	if err := h.push.register(phone); err != nil {
		t.Fatal(err)
	}
	next(assistantwire.Clients{ElicitationPush: true}, "a phone registered")
	desk := make(chan assistantwire.Clients, 16)
	d := connectClientsManager(t, h, srv, "desk", desk)
	if err := d.Peer().Notify("test.event", map[string]any{}); err != nil { // its first word after the handshake
		t.Fatal(err)
	}
	waitFor(desk, assistantwire.Clients{ElicitationPush: true}, "another manager connecting after")
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: false}); err != nil {
		t.Fatal(err)
	}
	next(assistantwire.Clients{}, `"Needs you" turned off`)
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true}); err != nil {
		t.Fatal(err)
	}
	next(assistantwire.Clients{ElicitationPush: true}, `"Needs you" back on`)
	h.push.signedOut("credential-example")
	next(assistantwire.Clients{}, "the phone signed out")
}
