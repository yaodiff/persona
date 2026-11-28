package extmgr

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

// Private market keys (extension_market_keys.go): every key in these tests is generated here.

type privateKey struct {
	priv ed25519.PrivateKey
	text string
	id   string
}

func newPrivateMarketKey(t *testing.T) privateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey{priv, extension.FormatSigningKey(pub), extension.KeyID(pub)}
}

// privateMarket is a website market serving one extension (example-ui) whose catalog the test signs as it likes.
type privateMarket struct {
	t       *testing.T
	h       *Manager
	mux     http.Handler
	phone   string
	srv     *httptest.Server
	dir     string
	mu      sync.Mutex
	catalog []byte
	pkgs    map[string][]byte
	id      string
}

func newPrivateMarket(t *testing.T) *privateMarket {
	h, phone, _ := marketHub(t)
	pm := &privateMarket{t: t, h: h, mux: h.routes(), phone: phone, dir: uiPackage(t), pkgs: map[string][]byte{}}
	pm.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pm.mu.Lock()
		defer pm.mu.Unlock()
		switch {
		case r.URL.Path == "/private/"+extension.IndexFile:
			_, _ = w.Write(pm.catalog)
		case strings.HasPrefix(r.URL.Path, "/private/pkg/") && pm.pkgs[strings.TrimPrefix(r.URL.Path, "/private/pkg/")] != nil:
			_, _ = w.Write(pm.pkgs[strings.TrimPrefix(r.URL.Path, "/private/pkg/")])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(pm.srv.Close)
	h.extensions.markets.client = pm.srv.Client()
	return pm
}

// publish makes version the catalog's example-ui, signed by keys (none: a plain index).
func (pm *privateMarket) publish(version string, keys ...privateKey) {
	pm.t.Helper()
	setVersion(pm.t, pm.dir, version)
	pkg := tarDir(pm.t, pm.dir)
	name := "example-ui-" + version + ".tar.gz"
	payload, _ := json.Marshal(map[string]any{"market_version": 1, "name": "Private extensions",
		"extensions": []any{marketEntry(pm.t, pm.dir, "pkg/"+name, pkg)}})
	raw := payload
	if len(keys) > 0 {
		var privs []ed25519.PrivateKey
		for _, k := range keys {
			privs = append(privs, k.priv)
		}
		var err error
		if raw, err = extension.SignCatalog(payload, privs...); err != nil {
			pm.t.Fatal(err)
		}
	}
	pm.mu.Lock()
	pm.catalog, pm.pkgs[name] = raw, pkg
	pm.mu.Unlock()
}

func (pm *privateMarket) setCatalog(raw []byte) {
	pm.mu.Lock()
	pm.catalog = raw
	pm.mu.Unlock()
}

func (pm *privateMarket) add(keys ...privateKey) (int, map[string]any) {
	pm.t.Helper()
	texts := []string{}
	for _, k := range keys {
		texts = append(texts, k.text)
	}
	body, _ := json.Marshal(map[string]any{"spec": pm.srv.URL + "/private/", "keys": texts})
	code, out := call(pm.t, pm.mux, "POST", "/api/v1/extensions/-/markets", pm.phone, string(body))
	if code == 200 {
		pm.id = out["id"].(string)
	}
	return code, out
}

func (pm *privateMarket) refresh() (int, map[string]any) {
	return call(pm.t, pm.mux, "POST", "/api/v1/extensions/-/markets/"+pm.id+"/refresh", pm.phone, "")
}

func (pm *privateMarket) preview() (int, map[string]any) {
	return call(pm.t, pm.mux, "POST", "/api/v1/extensions/-/catalog/preview", pm.phone, fmt.Sprintf(`{"market":%q,"extension":"example-ui"}`, pm.id))
}

// install installs (or upgrades) example-ui from the market and wants version.
func (pm *privateMarket) install(version string) {
	pm.t.Helper()
	code, pv := pm.preview()
	if code != 200 {
		pm.t.Fatalf("preview: %d %v", code, pv)
	}
	path, body := "/api/v1/extensions/-/install", fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, pv["preview_id"], pv["permissions_digest"])
	if pv["upgrade"] == true {
		path, body = "/api/v1/extensions/example-ui/-/upgrade", fmt.Sprintf(`{"preview_id":%q}`, pv["preview_id"])
	}
	if code, out := call(pm.t, pm.mux, "POST", path, pm.phone, body); code != 200 || out["version"] != version {
		pm.t.Fatalf("install %s: %d %v", version, code, out)
	}
}

func (pm *privateMarket) marketKey() string {
	p := pm.h.extensions.reg.Get("example-ui")
	if p == nil {
		pm.t.Fatal("example-ui is not installed")
	}
	return p.Record.MarketKey
}

// extensionSignature is example-ui's market_signature in GET /api/v1/extensions.
func (pm *privateMarket) extensionSignature() map[string]any {
	pm.t.Helper()
	_, out := call(pm.t, pm.mux, "GET", "/api/v1/extensions", pm.phone, "")
	for _, x := range out["extensions"].([]any) {
		if v := x.(map[string]any); v["id"] == "example-ui" {
			s, _ := v["market_signature"].(map[string]any)
			return s
		}
	}
	pm.t.Fatalf("example-ui not listed: %v", out)
	return nil
}

// listed is the market as GET /api/v1/extensions/-/markets lists it.
func (pm *privateMarket) listed() map[string]any {
	pm.t.Helper()
	for _, m := range marketList(pm.t, pm.h, pm.phone) {
		if m["id"] == pm.id {
			return m
		}
	}
	pm.t.Fatalf("market %s not listed", pm.id)
	return nil
}

func signatureOf(out map[string]any) (string, string) {
	s, _ := out["signature"].(map[string]any)
	return fmt.Sprint(s["state"]), fmt.Sprint(s["key_id"])
}

// A private market with a key: added only when its catalog verifies, installs and upgrades verified (the key recorded
// like the official one's), shown as signed, and still sandboxed by default.
func TestPrivateMarketKeyVerifiesInstallAndUpgrade(t *testing.T) {
	pm := newPrivateMarket(t)
	k := newPrivateMarketKey(t)
	pm.publish("1.0.0", k)
	code, mk := pm.add(k)
	if code != 200 {
		t.Fatalf("add: %d %v", code, mk)
	}
	if state, kid := signatureOf(mk); state != extension.SigSigned || kid != k.id {
		t.Fatalf("signature: %v", mk["signature"])
	}
	if keys, _ := mk["trusted_keys"].(map[string]any); len(keys) != 1 || keys[k.id] != k.text {
		t.Fatalf("trusted_keys: %v", mk["trusted_keys"])
	}
	pm.install("1.0.0")
	if pm.marketKey() != k.text {
		t.Fatalf("recorded key %q", pm.marketKey())
	}
	if s := pm.extensionSignature(); s["state"] != extSigSigned || s["key_id"] != k.id {
		t.Fatalf("market_signature: %v", s)
	}
	if pm.h.extensions.fromTrustedMarket(context.Background(), pm.h.extensions.reg.Get("example-ui")) {
		t.Fatal("a private market key granted the official sandbox exemption")
	}

	pm.publish("1.1.0", k)
	if code, out := pm.refresh(); code != 200 {
		t.Fatalf("refresh: %d %v", code, out)
	}
	pm.install("1.1.0")
	if pm.marketKey() != k.text {
		t.Fatal("upgrade lost the verified key")
	}
}

// A wrong signature (bytes changed under the right key id) and another signer (the key does not match) are refused:
// not added, and an existing market is quarantined, nothing installed or upgraded from its last good catalog.
func TestPrivateMarketKeyRefusesBadSignatures(t *testing.T) {
	k, other := newPrivateMarketKey(t), newPrivateMarketKey(t)
	corrupt := func(raw []byte) []byte {
		var env map[string]any
		_ = json.Unmarshal(raw, &env)
		sig := env["signatures"].([]any)[0].(map[string]any)
		b, _ := base64.StdEncoding.DecodeString(sig["sig"].(string))
		b[5] ^= 0x40
		sig["sig"] = base64.StdEncoding.EncodeToString(b)
		out, _ := json.Marshal(env)
		return out
	}
	for _, c := range []struct {
		name string
		bad  func(pm *privateMarket)
	}{
		{"wrong signature", func(pm *privateMarket) { pm.publish("1.1.0", k); pm.setCatalog(corrupt(pm.catalog)) }},
		{"key mismatch", func(pm *privateMarket) { pm.publish("1.1.0", other) }},
		{"unsigned catalog", func(pm *privateMarket) { pm.publish("1.1.0") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Not added.
			fresh := newPrivateMarket(t)
			c.bad(fresh)
			if code, out := fresh.add(k); code != 400 {
				t.Fatalf("added: %d %v", code, out)
			}
			// An existing market: quarantined, no upgrade, no install.
			pm := newPrivateMarket(t)
			pm.publish("1.0.0", k)
			if code, out := pm.add(k); code != 200 {
				t.Fatalf("add: %d %v", code, out)
			}
			pm.install("1.0.0")
			c.bad(pm)
			code, out := pm.refresh()
			if code != 400 || out["code"] != "market_signature" {
				t.Fatalf("refresh took it: %d %v", code, out)
			}
			if state, _ := signatureOf(pm.listed()); state != extension.SigInvalid {
				t.Fatalf("state %s", state)
			}
			if code, out := pm.preview(); code == 200 {
				t.Fatalf("an upgrade was previewed from a quarantined market: %v", out)
			}
			if s := pm.extensionSignature(); s["state"] != extSigMarketInvalid {
				t.Fatalf("market_signature: %v", s)
			}
			// The manager restarting keeps the quarantine.
			reloadMarkets(pm.h)
			if code, _ := pm.preview(); code == 200 {
				t.Fatal("a restart lifted the quarantine")
			}
		})
	}
}

// No keys: exactly as before. A plain index and even a signed envelope read as unsigned, install, record no key.
func TestPrivateMarketWithoutKeysStaysUnsigned(t *testing.T) {
	k := newPrivateMarketKey(t)
	for _, signed := range []bool{false, true} {
		pm := newPrivateMarket(t)
		if signed {
			pm.publish("1.0.0", k)
		} else {
			pm.publish("1.0.0")
		}
		code, mk := pm.add()
		if code != 200 {
			t.Fatalf("add: %d %v", code, mk)
		}
		if state, _ := signatureOf(mk); state != extension.SigUnsigned || mk["trusted_keys"] != nil {
			t.Fatalf("signed=%v: %v", signed, mk)
		}
		pm.install("1.0.0")
		if pm.marketKey() != "" {
			t.Fatal("an unsigned market recorded a key")
		}
		if s := pm.extensionSignature(); s["state"] != extSigUnsigned {
			t.Fatalf("market_signature: %v", s)
		}
	}
}

// Rotation: add the new key beside the old, sign with both, then only with the new one, and drop the old key. Each
// step keeps installs verified; an installation verified with the dropped key says so until it is upgraded; dropping
// the key the current catalog is signed with quarantines it at once, without a network read.
func TestPrivateMarketKeyRotation(t *testing.T) {
	pm := newPrivateMarket(t)
	oldKey, newKey := newPrivateMarketKey(t), newPrivateMarketKey(t)
	keys := "/api/v1/extensions/-/markets/"
	pm.publish("1.0.0", oldKey)
	if code, out := pm.add(oldKey); code != 200 {
		t.Fatalf("add: %d %v", code, out)
	}
	pm.install("1.0.0")

	code, out := call(t, pm.mux, "POST", keys+pm.id+"/keys", pm.phone, fmt.Sprintf(`{"key":%q}`, newKey.text))
	if code != 200 || len(out["trusted_keys"].(map[string]any)) != 2 {
		t.Fatalf("add key: %d %v", code, out)
	}
	waitMarketsRead(t, pm.h) // the background read a key change starts, before the publisher changes the catalog
	if state, kid := signatureOf(out); state != extension.SigSigned || kid != oldKey.id {
		t.Fatalf("after adding a key: %v", out["signature"])
	}
	if code, _ := call(t, pm.mux, "POST", keys+pm.id+"/keys", pm.phone, `{"key":"ed25519:nope"}`); code != 400 {
		t.Fatal("a malformed key was added")
	}

	pm.publish("1.1.0", oldKey, newKey)
	if code, out := pm.refresh(); code != 200 {
		t.Fatalf("both signatures: %d %v", code, out)
	}
	pm.publish("1.2.0", newKey)
	if code, out = pm.refresh(); code != 200 {
		t.Fatalf("new key only: %d %v", code, out)
	}
	if state, kid := signatureOf(out); state != extension.SigSigned || kid != newKey.id {
		t.Fatalf("signed by %v", out["signature"])
	}

	code, out = call(t, pm.mux, "DELETE", keys+pm.id+"/keys/"+oldKey.id, pm.phone, "")
	if code != 200 || len(out["trusted_keys"].(map[string]any)) != 1 {
		t.Fatalf("remove old key: %d %v", code, out)
	}
	waitMarketsRead(t, pm.h)
	if code, _ := call(t, pm.mux, "DELETE", keys+pm.id+"/keys/"+oldKey.id, pm.phone, ""); code != 404 {
		t.Fatal("removed a key twice")
	}
	if s := pm.extensionSignature(); s["state"] != extSigKeyRemoved || s["key_id"] != oldKey.id {
		t.Fatalf("installed with the removed key: %v", s)
	}
	pm.install("1.2.0")
	if pm.marketKey() != newKey.text || pm.extensionSignature()["state"] != extSigSigned {
		t.Fatalf("the upgrade did not record the new key: %q", pm.marketKey())
	}

	// Dropping the key the catalog is signed with: quarantined now; adding it back verifies the saved catalog again.
	if code, _ := call(t, pm.mux, "POST", keys+pm.id+"/keys", pm.phone, fmt.Sprintf(`{"key":%q}`, oldKey.text)); code != 200 {
		t.Fatal("add the old key back")
	}
	waitMarketsRead(t, pm.h)
	code, out = call(t, pm.mux, "DELETE", keys+pm.id+"/keys/"+newKey.id, pm.phone, "")
	if state, _ := signatureOf(out); code != 200 || state != extension.SigInvalid {
		t.Fatalf("removing the signing key: %d %v", code, out)
	}
	if code, _ := pm.preview(); code == 200 {
		t.Fatal("previewed from a catalog no trusted key signs")
	}
	waitMarketsRead(t, pm.h)
	if code, out := call(t, pm.mux, "POST", keys+pm.id+"/keys", pm.phone, fmt.Sprintf(`{"key":%q}`, newKey.text)); code != 200 {
		t.Fatalf("add the signing key back: %d %v", code, out)
	} else if state, kid := signatureOf(out); state != extension.SigSigned || kid != newKey.id {
		t.Fatalf("saved catalog not verified again: %v", out["signature"])
	}
	waitMarketsRead(t, pm.h)

	// Every key removed: the market is unsigned again, as before keys existed.
	for _, k := range []privateKey{oldKey, newKey} {
		if code, out := call(t, pm.mux, "DELETE", keys+pm.id+"/keys/"+k.id, pm.phone, ""); code != 200 {
			t.Fatalf("remove %s: %d %v", k.id, code, out)
		}
		waitMarketsRead(t, pm.h)
	}
	waitMarketsRead(t, pm.h)
	_, list := call(t, pm.mux, "GET", keys+pm.id+"/keys", pm.phone, "")
	if len(list["keys"].(map[string]any)) != 0 {
		t.Fatalf("keys left: %v", list)
	}
	if code, out := pm.refresh(); code != 200 {
		t.Fatalf("unsigned again: %d %v", code, out)
	} else if state, _ := signatureOf(out); state != extension.SigUnsigned {
		t.Fatalf("state %s", state)
	}
}

// A market row from before the keys existed (no trusted_keys) loads and behaves as an unsigned market, and the
// official market never takes owner keys.
func TestPrivateMarketKeysLegacyAndOfficial(t *testing.T) {
	pm := newPrivateMarket(t)
	pm.publish("1.0.0")
	if code, out := pm.add(); code != 200 {
		t.Fatalf("add: %d %v", code, out)
	}
	pm.h.extensions.markets.mu.Lock()
	rec := pm.h.extensions.markets.byID[pm.id].rec
	pm.h.extensions.markets.mu.Unlock()
	rec.TrustedKeys, rec.Signature = nil, extension.MarketSignature{}
	if err := pm.h.marketStore().PutExtensionMarket(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	reloadMarkets(pm.h)
	if err := pm.h.marketsLoad(context.Background()); err != nil {
		t.Fatal(err)
	}
	pm.install("1.0.0")
	if s := pm.extensionSignature(); s["state"] != extSigUnsigned {
		t.Fatalf("legacy market: %v", s)
	}

	// The official market (its read here goes to the test server and finds nothing) never takes an owner key.
	k := newPrivateMarketKey(t)
	pm.h.extensions.markets.githubAPI = pm.srv.URL
	useOfficialMarket(t, "github:example/plugins", k.text)
	reloadMarkets(pm.h)
	official := 0
	for _, m := range marketList(t, pm.h, pm.phone) {
		if m["official"] == true {
			official++
			if code, _ := call(t, pm.mux, "POST", "/api/v1/extensions/-/markets/"+fmt.Sprint(m["id"])+"/keys", pm.phone, fmt.Sprintf(`{"key":%q}`, newPrivateMarketKey(t).text)); code != 400 {
				t.Fatal("the official market took an owner key")
			}
		}
	}
	waitMarketsRead(t, pm.h)
	if official != 1 {
		t.Fatalf("%d official markets", official)
	}
}
