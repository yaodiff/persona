package extension

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Private market keys (docs/extensions/extensions.md "Private market keys"): the owner may give a nonofficial market
// one or more Ed25519 public keys. Its catalog must then carry the same embedded signature envelope as the official
// one, signed by any of them; installs and upgrades from it record the verifying key (Record.MarketKey). They never
// grant the official sandbox exemption, which only the compiled-in official key id does.

// MarketKeysMost bounds a market's trusted keys: enough to rotate (old and new side by side), small enough to read.
const MarketKeysMost = 8

// NormalizeMarketKeys checks keys, writes each one canonically ("ed25519:<standard base64>") and drops repeats (the
// same key id), keeping the first order. Empty input is no keys. The official key, current or retired
// (Official.PreviousSigningKeys), is refused: it is compiled in, and a private market claiming it would read as
// official provenance (a module's flag trusts either one: internal/extmgr module_providers.go).
func NormalizeMarketKeys(keys []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, k := range keys {
		if strings.TrimSpace(k) == "" {
			continue
		}
		pub, err := ParseSigningKey(k)
		if err != nil {
			return nil, err
		}
		id := KeyID(pub)
		if seen[id] {
			continue
		}
		canonical := FormatSigningKey(pub)
		if officialKeyID(id) {
			return nil, i18n.Err("extension-market_security.official_key")
		}
		seen[id] = true
		out = append(out, canonical)
	}
	if len(out) > MarketKeysMost {
		return nil, i18n.Err("extension-market_security.too_many_keys", i18n.Args{"most": MarketKeysMost})
	}
	return out, nil
}

// officialKeyID: id is the key id of the official market's signing key, current or retired.
func officialKeyID(id string) bool {
	for _, k := range append([]string{Official.SigningKey}, Official.PreviousSigningKeys...) {
		if k != "" && MarketKeyID(k) == id {
			return true
		}
	}
	return false
}

// FormatSigningKey is k as markets, configuration and the command line write it.
func FormatSigningKey(k ed25519.PublicKey) string {
	return "ed25519:" + base64.StdEncoding.EncodeToString(k)
}

// MarketKeyID is the key id of a configured key ("" when it does not parse).
func MarketKeyID(key string) string {
	k, err := ParseSigningKey(key)
	if err != nil {
		return ""
	}
	return KeyID(k)
}

// TrustPin is a market's verification identity at one moment, compared between a preview and its commit: the
// official key for the official market, the sorted configured keys for a private one, "" for an unsigned one. legacy
// is the market's stored single key (MarketRecord.SigningKey, cleared on load for private markets): any change to it
// changes the pin too.
func TrustPin(spec, legacy string, keys []string) string {
	if IsOfficialMarket(spec) {
		return Official.SigningKey
	}
	sorted := slices.Clone(keys)
	slices.Sort(sorted)
	if legacy != "" {
		sorted = append([]string{"legacy " + legacy}, sorted...)
	}
	return strings.Join(sorted, "\n")
}

// SignCatalog wraps payload (the exact index bytes) in the signed envelope ParseCatalog reads, signed by every key in
// keys (several during a rotation). payload must already be a valid index: it is checked the way a hub checks it.
func SignCatalog(payload []byte, keys ...ed25519.PrivateKey) ([]byte, error) {
	if len(keys) == 0 || len(keys) > 16 {
		return nil, i18n.Err("extension-market_security.catalog")
	}
	sigs := make([]catalogSignature, 0, len(keys))
	for _, k := range keys {
		if len(k) != ed25519.PrivateKeySize {
			return nil, i18n.Err("extension-market_security.public_key")
		}
		pub := k.Public().(ed25519.PublicKey)
		sigs = append(sigs, catalogSignature{Alg: "ed25519", KeyID: KeyID(pub), Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(k, payload))})
	}
	raw, err := json.Marshal(struct {
		Payload    string             `json:"payload"`
		Signatures []catalogSignature `json:"signatures"`
	}{string(payload), sigs})
	if err != nil {
		return nil, err
	}
	pubs := make([]string, 0, len(keys))
	for _, k := range keys {
		pubs = append(pubs, FormatSigningKey(k.Public().(ed25519.PublicKey)))
	}
	if _, err := ParseCatalogKeys(raw, pubs); err != nil {
		return nil, err
	}
	return raw, nil
}

// CatalogPayload is the index inside a catalog file: the payload of a signed envelope (re-signing it keeps those exact
// bytes), or the file itself when it is a plain index.
func CatalogPayload(raw []byte) ([]byte, error) {
	f, err := ParseCatalogKeys(raw, nil)
	if err != nil {
		return nil, err
	}
	return f.Payload, nil
}
