package hub

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/pushcrypto"
	"github.com/nwverse/persona/server/internal/relayproto"
)

// In office network mode no route answers a public source, pairing and health included; the LAN, a trusted range
// (the VPN) and loopback reach the routes as before.
func TestPrivateNetworkOnlyRefusesPublicSources(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, remote string
		tls                        bool
		status                     int
		code                       string
	}{
		{"public api", "GET", "/api/v1/machines", "198.51.100.1:1", false, 403, codePrivateNetworkOnly},
		{"public api over TLS", "GET", "/api/v1/machines", "198.51.100.1:1", true, 403, codePrivateNetworkOnly},
		{"public health", "GET", "/api/v1/health", "198.51.100.1:1", true, 403, codePrivateNetworkOnly},
		{"public ws", "GET", "/api/v1/ws?token=x", "198.51.100.1:1", true, 403, codePrivateNetworkOnly},
		{"public page", "GET", "/", "198.51.100.1:1", true, 403, codePrivateNetworkOnly},
		{"public global IPv6", "GET", "/api/v1/machines", "[2001:db8::5]:1", true, 403, codePrivateNetworkOnly},
		{"LAN api", "GET", "/api/v1/machines", "[fd00::5]:1", false, 200, ""},
		{"VPN range", "GET", "/api/v1/machines", "192.0.2.1:1", true, 200, ""},
		{"loopback api", "GET", "/api/v1/machines", "127.0.0.1:1", false, 200, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			h.cfg.Security = SecurityConfig{TrustedCIDRs: []string{"192.0.2.0/24"}, PrivateNetworkOnly: true}
			requireCode(t, securityRequestHost(h, tc.method, tc.path, h.cfg.Token, tc.remote, "hub.example:18660", tc.tls), tc.status, tc.code)
		})
	}
}

// Off (the default), a public source reaches the same gates as before: TLS with a device token is not refused here.
func TestPrivateNetworkOnlyOff(t *testing.T) {
	h := newTestHub(t)
	r := securityRequestHost(h, "GET", "/api/v1/health", "", "198.51.100.1:1", "hub.example:18660", true)
	if r.Code == 403 && strings.Contains(r.Body.String(), codePrivateNetworkOnly) {
		t.Fatalf("refused with the switch off: %d %s", r.Code, r.Body)
	}
}

// The start names every setting that reaches past the private network, and starts with none of them.
func TestPrivateNetworkOnlyStartCheck(t *testing.T) {
	cfg := defaultConfig()
	cfg.Security.PrivateNetworkOnly = true
	cfg.PublicURL = "https://hub.corp.example:18662"
	cfg.Public.Advertise = []string{"hub.corp.example", "[fd00::2]:18662", "100.80.0.2"}
	cfg.Public.LinkAdvertise = []string{"https://link.corp.example:18662"}
	off := false
	cfg.Public.AdvertiseIPv6 = &off
	if err := cfg.checkPrivateNetworkOnly(); err != nil {
		t.Fatalf("private addresses and names: %v", err)
	}
	on := true
	cfg.Public.AdvertiseIPv6 = &on
	cfg.Public.Advertise = append(cfg.Public.Advertise, "https://203.0.113.9:18662")
	cfg.Public.LinkAdvertise = append(cfg.Public.LinkAdvertise, "https://203.0.113.10:18662")
	cfg.Public.DDNS = &DDNSConfig{}
	cfg.Security.AllowPublicPlaintext = true
	cfg.Sharing = &SharingConfig{PublicOrigin: "https://share.example.com"}
	cfg.Machines = append(cfg.Machines, MachineConfig{ID: "nat", Kind: "tailcat"}, MachineConfig{ID: "old", Kind: "ssh", TailcatAddr: "x"})
	err := cfg.checkPrivateNetworkOnly()
	if !errors.Is(err, errPrivateNetworkConflicts) {
		t.Fatalf("err %v, want errPrivateNetworkConflicts", err)
	}
	for _, want := range []string{"allow_public_plaintext", "public.ddns", "advertise_ipv6", "203.0.113.9", "203.0.113.10", "sharing.public_origin", `"nat"`, `"old"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
	cfg.Security.PrivateNetworkOnly = false
	if err := cfg.checkPrivateNetworkOnly(); err != nil {
		t.Fatalf("switch off: %v", err)
	}
}

func TestPublicAddressIn(t *testing.T) {
	for raw, public := range map[string]bool{
		"https://203.0.113.9:18662": true, "203.0.113.9": true, "[2001:db8::1]:18662": true, "https://[2001:db8::1]": true,
		"[fd00::3]:18660": false, "fd00::1": false, "100.64.0.9": false,
		"127.0.0.1": false, "https://hub.corp.example": false, "": false,
	} {
		if _, got := publicAddressIn(raw); got != public {
			t.Errorf("publicAddressIn(%q) = %v, want %v", raw, got, public)
		}
	}
}

// The build's own update source (the publisher's) is not used in office network mode; hub.json's update.url is.
func TestPrivateNetworkOnlyUpdateSource(t *testing.T) {
	h := newTestHub(t)
	h.build.UpdateURL = "https://dl.example.com/dl"
	h.cfg.Security.PrivateNetworkOnly = true
	if s := h.updateSource(); s.url != "" {
		t.Fatalf("build source used: %+v", s)
	}
	if url, _ := h.nodePackages(); url != "" {
		t.Fatalf("node packages from the build source: %q", url)
	}
	h.cfg.Update.URL = "file:///opt/persona-mirror"
	if s := h.updateSource(); s.url != "file:///opt/persona-mirror" || s.from != updateFromConfig {
		t.Fatalf("hub.json source: %+v", s)
	}
}

// Global IPv6 addresses are not handed out unless asked for; LAN ones are.
func TestPrivateNetworkOnlyAdvertisesNoGlobalIPv6(t *testing.T) {
	cfg := defaultConfig()
	cfg.PublicURL = ""
	cfg.Listen = ":18660"
	cfg.Security.PrivateNetworkOnly = true
	up := net.FlagUp
	addrs := []localAddress{{Addr: netip.MustParseAddr("2a01:4f8::5"), Interface: "en0", Flags: up},
		{Addr: netip.MustParseAddr("fd00::5"), Interface: "en0", Flags: up}}
	lan := false
	for _, e := range collectEndpoints(cfg, 0, addrs, time.Now()) {
		if e.Kind == "ipv6" {
			t.Fatalf("global IPv6 advertised: %+v", e)
		}
		lan = lan || e.Kind == "lan"
	}
	if !lan {
		t.Fatal("the LAN address is not advertised")
	}
	cfg.Security.PrivateNetworkOnly = false
	if got := collectEndpoints(cfg, 0, addrs, time.Now()); len(got) != 2 {
		t.Fatalf("switch off: %+v", got)
	}
}

// A push in office network mode carries nothing APNs can read: the fallback alert and the payload sealed to the phone,
// which opens to what the hub built; a phone without a key gets the fallback only, and no background push.
func TestSealedDirectPush(t *testing.T) {
	p := newPusher(t.TempDir(), nil, t.Logf)
	p.privateOnly = true
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	d := Device{DeviceID: "phone", Name: "phone", APNSToken: "aa", PushKey: base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())}
	payload := map[string]any{"aps": map[string]any{"alert": map[string]any{"title": "Deploy approval", "body": "rm -rf build/"},
		"category": "INTERACTION_PERMISSION", "thread-id": "session-1"}, "session_id": "session-1"}
	body, err := p.sealedDirect(d, "alert", payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"Deploy approval", "rm -rf", "INTERACTION_PERMISSION", "session-1"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("APNs body carries %q: %s", secret, body)
		}
	}
	var out struct {
		APS map[string]any `json:"aps"`
		E   string         `json:"e"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.E == "" || out.APS["mutable-content"] != float64(1) {
		t.Fatalf("envelope %s (%v)", body, err)
	}
	raw, _ := base64.StdEncoding.DecodeString(out.E)
	plain, err := pushcrypto.Open(priv, raw)
	if err != nil || !strings.Contains(string(plain), "Deploy approval") || !strings.Contains(string(plain), `"iat"`) {
		t.Fatalf("opened %s (%v)", plain, err)
	}
	if body, err := p.sealedDirect(d, "background", map[string]any{"persona_wake": "x"}); err != nil || !strings.Contains(string(body), "content-available") {
		t.Fatalf("background %s (%v)", body, err)
	}

	keyless := Device{DeviceID: "old", Name: "old", APNSToken: "bb"}
	body, err = p.sealedDirect(keyless, "alert", payload)
	if err != nil || strings.Contains(string(body), "Deploy approval") || strings.Contains(string(body), `"e"`) {
		t.Fatalf("keyless alert %s (%v)", body, err)
	}
	if _, err := p.sealedDirect(keyless, "background", payload); !errors.Is(err, errNoPushKey) {
		t.Fatalf("keyless background: %v", err)
	}
}

// Office network mode never posts to a relay, whatever the phone registered, and pushes no Live Activity.
func TestPrivateNetworkOnlyNoRelay(t *testing.T) {
	p := newPusher(t.TempDir(), nil, t.Logf)
	p.privateOnly = true
	d := Device{DeviceID: "phone", APNSToken: "aa", RelayURL: "https://relay.example.com/relay", RelayGrant: "g", PushKey: "k", VoIPToken: "cc", VoIPGrant: "v"}
	if p.relayed(d) || p.reachable(d) || p.callable(d) {
		t.Fatalf("relayed %v reachable %v callable %v", p.relayed(d), p.reachable(d), p.callable(d))
	}
	if _, _, err := p.postRelayOnce(d, relayproto.PushRequest{Type: "alert"}); !errors.Is(err, errRelayOff) {
		t.Fatalf("post to relay: %v", err)
	}
	if _, err := p.renewGrant(d); !errors.Is(err, errRelayOff) {
		t.Fatalf("renew grant: %v", err)
	}
	var got pushResult = -1
	<-p.sendLiveActivityThen(d, "dd", map[string]any{"aps": map[string]any{}}, "10", func() {}, func(r pushResult) { got = r })
	if got != pushRefused {
		t.Fatalf("live activity outcome %v", got)
	}
}
