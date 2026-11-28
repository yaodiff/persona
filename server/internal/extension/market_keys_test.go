package extension

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func newPrivateKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv, FormatSigningKey(pub)
}

const privatePayload = "{\"market_version\":1,\"name\":\"Private\",\"extensions\":[]}\n"

// Any one trusted key verifies a catalog; SignedBy names it; a wrong signature, another signer or no envelope is
// ErrCatalogUnverified; no keys parses without verifying.
func TestParseCatalogKeys(t *testing.T) {
	k1, pub1 := newPrivateKey(t)
	k2, pub2 := newPrivateKey(t)
	k3, pub3 := newPrivateKey(t)
	payload := []byte(privatePayload)
	one, err := SignCatalog(payload, k1)
	if err != nil {
		t.Fatal(err)
	}
	both, err := SignCatalog(payload, k1, k2)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		raw    []byte
		keys   []string
		signer string // "" when it must fail
	}{
		{"one key", one, []string{pub1}, pub1},
		{"rotation: old and new trusted, signed by old", one, []string{pub2, pub1}, pub1},
		{"rotation: both signatures, first trusted key wins", both, []string{pub2, pub1}, pub2},
		{"rotation: only the new key trusted", both, []string{pub2}, pub2},
		{"another signer", one, []string{pub3}, ""},
		{"plain index where keys are required", payload, []string{pub1}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, err := ParseCatalogKeys(c.raw, c.keys)
			if c.signer == "" {
				if !errors.Is(err, ErrCatalogUnverified) {
					t.Fatalf("verified: %v", err)
				}
				return
			}
			if err != nil || f.SignedBy != c.signer || string(f.Payload) != privatePayload {
				t.Fatalf("%v %+v", err, f)
			}
		})
	}
	// Signature bytes changed under the right key id.
	var env map[string]any
	_ = json.Unmarshal(one, &env)
	sig := env["signatures"].([]any)[0].(map[string]any)
	b, _ := base64.StdEncoding.DecodeString(sig["sig"].(string))
	b[0] ^= 0xff
	sig["sig"] = base64.StdEncoding.EncodeToString(b)
	bad, _ := json.Marshal(env)
	if _, err := ParseCatalogKeys(bad, []string{pub1}); !errors.Is(err, ErrCatalogUnverified) {
		t.Fatalf("a corrupted signature verified: %v", err)
	}
	// Payload changed after signing.
	tampered := strings.Replace(string(one), `Private`, `Privat3`, 1)
	if _, err := ParseCatalogKeys([]byte(tampered), []string{pub1}); !errors.Is(err, ErrCatalogUnverified) {
		t.Fatalf("a tampered payload verified: %v", err)
	}
	// No keys: an envelope parses, unverified.
	if f, err := ParseCatalogKeys(one, nil); err != nil || f.SignedBy != "" {
		t.Fatalf("unsigned read: %v %+v", err, f)
	}
	_ = k3
}

func TestNormalizeMarketKeys(t *testing.T) {
	_, pub1 := newPrivateKey(t)
	_, pub2 := newPrivateKey(t)
	raw := strings.TrimPrefix(pub1, "ed25519:")
	got, err := NormalizeMarketKeys([]string{" ed25519:" + raw + " ", "", pub2, pub1})
	if err != nil || len(got) != 2 || got[0] != pub1 || got[1] != pub2 {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := NormalizeMarketKeys(nil); err != nil || len(got) != 0 {
		t.Fatalf("no keys: %v %v", got, err)
	}
	if _, err := NormalizeMarketKeys([]string{"ed25519:short"}); err == nil {
		t.Fatal("a malformed key was taken")
	}
	if _, err := NormalizeMarketKeys([]string{raw}); err == nil {
		t.Fatal("a key without its ed25519: prefix was taken")
	}
	if _, err := NormalizeMarketKeys([]string{Official.SigningKey}); err == nil {
		t.Fatal("the official key was taken for a private market")
	}
	many := []string{}
	for range MarketKeysMost + 1 {
		_, k := newPrivateKey(t)
		many = append(many, k)
	}
	if _, err := NormalizeMarketKeys(many); err == nil {
		t.Fatal("too many keys were taken")
	}
}

func TestTrustPinAndVerifyKeys(t *testing.T) {
	_, pub1 := newPrivateKey(t)
	_, pub2 := newPrivateKey(t)
	spec := "https://example.com/private/persona-extensions.json"
	if TrustPin(spec, "", []string{pub1, pub2}) != TrustPin(spec, "", []string{pub2, pub1}) {
		t.Fatal("the pin depends on key order")
	}
	if TrustPin(spec, "", []string{pub1}) == TrustPin(spec, "", []string{pub1, pub2}) || TrustPin(spec, "", nil) != "" || TrustPin(spec, "x", nil) == "" {
		t.Fatal("the pin does not follow the key set")
	}
	if TrustPin(Official.Spec, "", []string{pub1}) != Official.SigningKey {
		t.Fatal("the official pin is not the compiled-in key")
	}
	s, _ := ParseMarket(spec)
	if keys := (&Market{Spec: s, TrustedKeys: []string{pub1}}).VerifyKeys(); len(keys) != 1 || keys[0] != pub1 {
		t.Fatalf("private keys: %v", keys)
	}
	o, _ := ParseMarket(Official.Spec)
	if keys := (&Market{Spec: o, TrustedKeys: []string{pub1}}).VerifyKeys(); len(keys) != 1 || keys[0] != Official.SigningKey {
		t.Fatalf("the official market used owner keys: %v", keys)
	}
}

// Re-signing a signed catalog keeps the exact payload bytes.
func TestCatalogPayloadResign(t *testing.T) {
	k1, _ := newPrivateKey(t)
	k2, pub2 := newPrivateKey(t)
	raw, _ := SignCatalog([]byte(privatePayload), k1)
	payload, err := CatalogPayload(raw)
	if err != nil || string(payload) != privatePayload {
		t.Fatalf("%q %v", payload, err)
	}
	again, err := SignCatalog(payload, k2)
	if err != nil {
		t.Fatal(err)
	}
	if f, err := ParseCatalogKeys(again, []string{pub2}); err != nil || string(f.Payload) != privatePayload {
		t.Fatalf("%v", err)
	}
	if _, err := SignCatalog([]byte(`{"market_version":1,"name":"x","extensions":[{"manifest":{"id":"Bad"}}]}`+"x"), k1); err == nil {
		t.Fatal("signed a document that is not an index")
	}
}
