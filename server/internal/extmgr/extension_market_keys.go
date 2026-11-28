package extmgr

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Private market keys (docs/extensions/extensions.md "Private market keys"): the owner gives a nonofficial market one
// or more Ed25519 public keys. While it has any, its catalog must be signed by one of them (the official envelope
// format), a catalog that is not is quarantined like an official one, and installs and upgrades record the verifying
// key. A market without keys stays unsigned, exactly as before. The keys verify provenance only: the default sandbox
// exemption stays with the compiled-in official key (extension_sandbox.go).

// marketKeysOut is GET …/markets/{id}/keys: the trusted keys by key id (public, so shown whole).
type marketKeysOut struct {
	Keys map[string]string `json:"keys"`
}

// How an installed market extension's package was verified (ExtensionView.MarketSignature, a MarketSignatureView):
//   - official: by the official market's compiled-in key;
//   - signed: by one of its private market's trusted keys, still trusted (KeyID says which);
//   - key_removed: by a private market key the owner no longer trusts, or whose market is gone;
//   - unsigned: not verified (an unsigned market, or installed before the market had keys);
//   - market_invalid: verified when installed, but the market's catalog no longer verifies (no upgrades from it).
const (
	extSigOfficial      = "official"
	extSigSigned        = extension.SigSigned
	extSigKeyRemoved    = "key_removed"
	extSigUnsigned      = extension.SigUnsigned
	extSigMarketInvalid = "market_invalid"
)

// marketKeyViews are keys by key id.
func marketKeyViews(keys []string) map[string]string {
	out := map[string]string{}
	for _, k := range keys {
		out[keyIDOf(k)] = k
	}
	return out
}

// sameMarketTrust: a and b read, verify and authenticate the same way (a snapshot taken before a network read
// against the record after it).
func sameMarketTrust(a, b extension.MarketRecord) bool {
	return a.Spec == b.Spec && a.SigningKey == b.SigningKey && a.TokenSealed == b.TokenSealed && slices.Equal(a.TrustedKeys, b.TrustedKeys)
}

// requiresSignature: the market's catalog must verify before anything is installed from it.
func requiresSignature(rec extension.MarketRecord) bool {
	return extension.IsOfficialMarket(rec.Spec) || len(rec.TrustedKeys) > 0
}

// marketVerified: installs and upgrades may use the market's current index. An unsigned market (no keys) may, as
// before; the official one and a private one with keys only while their last catalog verified with a key they trust
// now.
func marketVerified(rec extension.MarketRecord) bool {
	if rec.Signature.Bad() {
		return false
	}
	switch {
	case extension.IsOfficialMarket(rec.Spec):
		return rec.Signature.State == extension.SigSigned && rec.Signature.KeyID == keyIDOf(extension.Official.SigningKey)
	case len(rec.TrustedKeys) > 0:
		return rec.Signature.State == extension.SigSigned && slices.ContainsFunc(rec.TrustedKeys, func(k string) bool { return keyIDOf(k) == rec.Signature.KeyID })
	}
	return true
}

// marketKeysEdit changes market id's trusted keys to edit(current) and verifies its saved catalog with them at once
// (no network: a removed key stops counting now, and the right key added back verifies the saved catalog again), then
// reads the market again in the background.
func (h *Manager) marketKeysEdit(ctx context.Context, id string, edit func([]string) ([]string, error)) (MarketView, error) {
	if err := h.marketsLoad(ctx); err != nil {
		return MarketView{}, err
	}
	ms := h.marketsState()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	s := ms.byID[id]
	if s == nil {
		return MarketView{}, errNoMarket
	}
	if extension.IsOfficialMarket(s.rec.Spec) {
		return MarketView{}, i18n.Err("extension-market_security.official_key")
	}
	next, err := edit(slices.Clone(s.rec.TrustedKeys))
	if err != nil {
		return MarketView{}, err
	}
	keys, err := extension.NormalizeMarketKeys(next)
	if err != nil {
		return MarketView{}, err
	}
	rec, index := s.rec, s.index
	rec.TrustedKeys = keys
	if len(rec.Index) > 0 {
		verified, ix, verr := signedIndex(rec, &extension.Fetched{Raw: rec.Index}, time.Now(), h.build.Number)
		verified.FetchedAt = rec.FetchedAt
		if verr == nil {
			index = ix
		}
		rec = verified
	} else if len(keys) == 0 {
		rec.Signature, rec.Error = extension.MarketSignature{State: extension.SigUnsigned}, ""
	} else {
		rec.Signature = extension.MarketSignature{}
	}
	if err := h.marketStore().PutExtensionMarket(ctx, rec); err != nil {
		return MarketView{}, err
	}
	s.rec, s.index = rec, index
	ms.readLater(h, id)
	h.logf("extension market %s: %d trusted keys (%s)", id, len(keys), signatureText(rec.Signature))
	return h.marketView(s), nil
}

// marketSignatureOf is how p, installed from a market, was verified; nil for an extension not from a market.
func (h *Manager) marketSignatureOf(ctx context.Context, p *extension.Extension) *MarketSignatureView {
	market := extension.FromMarket(p.Record.Origin)
	if p.Record.Source != extension.SourceInstalled || market == "" {
		return nil
	}
	key := p.Record.MarketKey
	if key == "" {
		return &MarketSignatureView{State: extSigUnsigned}
	}
	v := &MarketSignatureView{State: extSigKeyRemoved, KeyID: keyIDOf(key)}
	if extension.Official.SigningKey != "" && key == extension.Official.SigningKey {
		v.State = extSigOfficial
	}
	if h.marketsLoad(ctx) != nil {
		return v
	}
	ms := h.marketsState()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	s := ms.byID[market]
	if s == nil {
		return v
	}
	if v.State != extSigOfficial && !extension.IsOfficialMarket(s.rec.Spec) && slices.Contains(s.rec.TrustedKeys, key) {
		v.State = extSigSigned
	}
	if v.State != extSigKeyRemoved && s.rec.Signature.Bad() {
		v.State = extSigMarketInvalid
	}
	return v
}

// marketKeyRoutes are the trusted keys' routes under base (marketRoutesAt's gate and fail).
func (h *Manager) marketKeyRoutes(api *http.ServeMux, base string, gate func(http.HandlerFunc) http.HandlerFunc, fail func(http.ResponseWriter, error, string)) {
	api.HandleFunc("GET "+base+"/markets/{mid}/keys", gate(func(w http.ResponseWriter, r *http.Request) {
		ms := h.marketsState()
		ms.mu.Lock()
		s := ms.byID[r.PathValue("mid")]
		var keys []string
		if s != nil {
			keys = slices.Clone(s.rec.TrustedKeys)
		}
		ms.mu.Unlock()
		if s == nil {
			fail(w, errNoMarket, "")
			return
		}
		writeJSON(w, marketKeysOut{Keys: marketKeyViews(keys)})
	}))
	api.HandleFunc("POST "+base+"/markets/{mid}/keys", gate(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Key string `json:"key"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		v, err := h.marketKeysEdit(r.Context(), r.PathValue("mid"), func(keys []string) ([]string, error) {
			if _, err := extension.ParseSigningKey(in.Key); err != nil {
				return nil, err
			}
			return append(keys, in.Key), nil
		})
		if err != nil {
			fail(w, err, "invalid_key")
			return
		}
		writeJSON(w, marketOut(r, v))
	}))
	api.HandleFunc("DELETE "+base+"/markets/{mid}/keys/{kid}", gate(func(w http.ResponseWriter, r *http.Request) {
		kid := r.PathValue("kid")
		v, err := h.marketKeysEdit(r.Context(), r.PathValue("mid"), func(keys []string) ([]string, error) {
			kept := slices.DeleteFunc(keys, func(k string) bool { return keyIDOf(k) == kid || k == kid })
			if len(kept) == len(keys) {
				return nil, errNoMarketKey
			}
			return kept, nil
		})
		if errors.Is(err, errNoMarketKey) {
			writeErrCode(w, http.StatusNotFound, "not_found", err)
			return
		}
		if err != nil {
			fail(w, err, "invalid_key")
			return
		}
		writeJSON(w, marketOut(r, v))
	}))
}

var errNoMarketKey = i18n.Err("hub-extension_market.no_key")
