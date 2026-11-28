package hub

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/relay"
)

const (
	callAlertToken = "a1b2c3d4e5f60718a1b2c3d4e5f60718"
	callVoIPToken  = "f0e1d2c3b4a59687f0e1d2c3b4a59687"
)

// callHub: a hub without apns.json and a phone registered with a relay for its pushes and, when voip, for calls (its
// PushKit token with topic "<bundle>.voip"), as the app does.
func callHub(t *testing.T, voip bool) (*Hub, *relayAPNs, *ecdh.PrivateKey, *httptest.Server) {
	t.Helper()
	h := newTestHub(t)
	if h.push.direct != nil {
		t.Fatal("test hub has an APNs key")
	}
	f := &relayAPNs{}
	rs, err := relay.New(relay.Config{Topics: []string{"app.example"}}, f, bytes.Repeat([]byte{3}, 32), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(rs.Handler())
	t.Cleanup(srv.Close)
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	d := Device{DeviceID: "phone", Name: "phone", Kind: "iphone", APNSToken: callAlertToken, APNSEnv: "sandbox", Topic: "app.example",
		PushKey: base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), RelayURL: srv.URL + "/relay/", RelayGrant: relayGrant(t, srv, callAlertToken, "app.example")}
	if voip {
		d.VoIPToken, d.VoIPGrant = callVoIPToken, relayGrant(t, srv, callVoIPToken, "app.example.voip")
	}
	if err := h.push.register(d); err != nil {
		t.Fatal(err)
	}
	return h, f, priv, srv
}

func relayGrant(t *testing.T, srv *httptest.Server, token, topic string) string {
	t.Helper()
	resp, err := srv.Client().Post(srv.URL+"/relay/v1/register", "application/json",
		strings.NewReader(`{"apns_token":"`+token+`","env":"sandbox","topic":"`+topic+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var reg struct{ Grant string }
	_ = json.NewDecoder(resp.Body).Decode(&reg)
	if reg.Grant == "" {
		t.Fatalf("relay gave no grant for %s: %d", topic, resp.StatusCode)
	}
	return reg.Grant
}

var testRing = assistantwire.CallRing{ID: "call_7f3a", Title: "登录失败率 18%", ConversationID: "conv_1", Attempt: 1}

// TestRingCallThroughRelay: the manager rings over the bus. The push to the phone's PushKit token carries the
// call's id, title, manager and conversation sealed for the phone.
func TestRingCallThroughRelay(t *testing.T) {
	h, f, priv, _ := callHub(t, true)
	if route, _ := h.push.callRouteOfID("phone"); route.Route != pushRelay {
		t.Fatalf("call route %+v", route)
	}
	a := connectAssistant(t, h)
	res, err := a.bh.CallRing(t.Context(), testRing)
	if err != nil || res.Devices != 1 || res.Reason != "" {
		t.Fatalf("ring %+v, %v", res, err)
	}
	r := f.wait(t, 1)[0]
	if r.PushType != "voip" || r.Token != callVoIPToken || r.Topic != "app.example.voip" || r.Priority != "10" {
		t.Fatalf("request %+v", r)
	}
	if r.Expiration < time.Now().Unix() || r.Expiration > time.Now().Add(time.Minute).Unix() {
		t.Fatalf("expiration %d", r.Expiration)
	}
	for _, leak := range []string{"登录", "call_7f3a", "conv_1"} {
		if bytes.Contains(r.Body, []byte(leak)) || strings.Contains(r.CollapseID, leak) {
			t.Fatalf("relay saw %q: %s", leak, r.Body)
		}
	}
	p := opened(t, priv, r)
	call, _ := p["urgent_call"].(map[string]any)
	if call["id"] != "call_7f3a" || call["title"] != "登录失败率 18%" || call["manager"] != "assistant" || call["conversation_id"] != "conv_1" || call["attempt"] != 1.0 {
		t.Fatalf("opened %v", p)
	}
	if p["iat"] == nil || p["hub_id"] != h.hubID() {
		t.Fatalf("opened %v", p)
	}
}

// TestRingCallNoPhone: without a PushKit registration nothing is sent and the manager hears why.
func TestRingCallNoPhone(t *testing.T) {
	h, f, _, _ := callHub(t, false)
	if res := h.push.ringCall(context.Background(), "assistant", testRing); res.Devices != 0 || res.Reason != assistantwire.CallNoPhone {
		t.Fatalf("ring %+v", res)
	}
	if route, _ := h.push.callRouteOfID("phone"); route.Route != pushNone || route.Reason != pushNoToken {
		t.Fatalf("call route %+v", route)
	}
	// ringCall sends nothing when no phone can take calls: it returns before starting any send.
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.got) != 0 {
		t.Fatalf("sent %d", len(f.got))
	}
}

// TestRingCallGoneDropsOnlyTheCallToken: APNs calling the PushKit token dead forgets it, not the phone: its other
// pushes still go.
func TestRingCallGoneDropsOnlyTheCallToken(t *testing.T) {
	h, f, _, _ := callHub(t, true)
	f.gone = true
	if res := h.push.ringCall(context.Background(), "assistant", testRing); res.Devices != 0 || res.Reason != assistantwire.CallRefused {
		t.Fatalf("ring %+v", res)
	}
	d, ok := h.push.device("phone")
	if !ok || d.APNSToken != callAlertToken || d.RelayGrant == "" {
		t.Fatalf("device %+v kept %v", d, ok)
	}
	if d.VoIPToken != "" || d.VoIPGrant != "" || d.VoIPError == "" {
		t.Fatalf("call token kept: %+v", d)
	}
	if route, _ := h.push.callRouteOfID("phone"); route.Route != pushNone {
		t.Fatalf("call route %+v", route)
	}
}

// TestRingCallRenewsItsGrant: a call grant the relay no longer takes (its secret changed) is renewed with the PushKit
// token, and the ring goes.
func TestRingCallRenewsItsGrant(t *testing.T) {
	h, f, _, _ := callHub(t, true)
	other, err := relay.New(relay.Config{Topics: []string{"app.example"}}, f, bytes.Repeat([]byte{9}, 32), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	otherSrv := httptest.NewServer(other.Handler())
	t.Cleanup(otherSrv.Close)
	stale := relayGrant(t, otherSrv, callVoIPToken, "app.example.voip")
	d, _ := h.push.device("phone")
	d.VoIPGrant = stale
	if err := h.push.register(d); err != nil {
		t.Fatal(err)
	}
	if res := h.push.ringCall(context.Background(), "assistant", testRing); res.Devices != 1 {
		t.Fatalf("ring %+v", res)
	}
	if d, _ := h.push.device("phone"); d.VoIPGrant == stale || d.VoIPGrant == "" {
		t.Fatal("grant not renewed")
	}
}

// TestRegisterCleansCallFields: a token that is not one is dropped with its grant, and a grant clears the error.
func TestRegisterCleansCallFields(t *testing.T) {
	for _, c := range []struct {
		in, want Device
	}{
		{Device{VoIPToken: "not hex", VoIPGrant: "g"}, Device{}},
		{Device{VoIPGrant: "g", VoIPError: "x"}, Device{VoIPError: "x"}}, // a grant with no token goes; the reason stays
		{Device{VoIPToken: callVoIPToken, VoIPGrant: "g", VoIPError: "403 topic not served"}, Device{VoIPToken: callVoIPToken, VoIPGrant: "g"}},
		{Device{VoIPToken: callVoIPToken, VoIPError: "403 topic   not served"}, Device{VoIPToken: callVoIPToken, VoIPError: "403 topic not served"}},
	} {
		d := c.in
		cleanVoIP(&d)
		if d.VoIPToken != c.want.VoIPToken || d.VoIPGrant != c.want.VoIPGrant || d.VoIPError != c.want.VoIPError {
			t.Errorf("cleanVoIP(%+v) = %+v", c.in, d)
		}
	}
}

// TestCallRoute: the way a phone is rung, and why not.
func TestCallRoute(t *testing.T) {
	h := newTestHub(t)
	p := h.push
	for _, c := range []struct {
		d            Device
		route, cause string
	}{
		{Device{}, pushNone, pushNoToken},
		{Device{VoIPToken: callVoIPToken, VoIPError: "403 topic not served by this relay", RelayURL: "https://relay.example", RelayGrant: "normal"}, pushNone, pushRelayFailed},
		{Device{VoIPToken: callVoIPToken}, pushNone, pushNoRelay},
		// A URL kept only to name a refused normal registration is not a usable call route.
		{Device{VoIPToken: callVoIPToken, RelayError: "403 topic not served", RelayURL: "https://relay.example", VoIPGrant: "voip"}, pushNone, pushNoRelay},
		{Device{VoIPToken: callVoIPToken, RelayError: "403 topic not served", RelayURL: "https://relay.example", VoIPGrant: "voip", PushKey: "k"}, pushNone, pushNoRelay},
		{Device{VoIPToken: callVoIPToken, RelayURL: "https://relay.example", RelayGrant: "normal", VoIPGrant: "g"}, pushNone, pushRelayFailed}, // no push key
		{Device{VoIPToken: callVoIPToken, RelayURL: "https://relay.example", RelayGrant: "normal", VoIPGrant: "g", PushKey: "k"}, pushRelay, ""},
	} {
		if got := p.callRouteOf(c.d); got.Route != c.route || got.Reason != c.cause {
			t.Errorf("callRouteOf(%+v) = %+v, want %s %s", c.d, got, c.route, c.cause)
		}
	}
}
