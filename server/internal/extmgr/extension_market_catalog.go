package extmgr

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// MarketView is a market as the apps list it.
type MarketView struct {
	ID                 string     `json:"id"`
	Spec               string     `json:"spec"`
	Name               string     `json:"name"`
	Description        string     `json:"description,omitempty"`
	Homepage           string     `json:"homepage,omitempty"`
	DownloadsUpdatedAt string     `json:"downloads_updated_at,omitempty"`
	HasToken           bool       `json:"has_token"`
	Extensions         int        `json:"extensions"`
	Warnings           []string   `json:"warnings"`
	Error              string     `json:"error,omitempty"`
	FetchedAt          *time.Time `json:"fetched_at,omitempty"`
	AddedAt            time.Time  `json:"added_at"`
	// Official: the market every hub has without adding it (extension.Official), listed first.
	Official bool `json:"official,omitempty"`
	// Signature is the index's signature (docs/extensions/extensions.md "Signing"): signed / unsigned / invalid / key_changed,
	// and the key it is signed by.
	Signature MarketSignatureView `json:"signature"`
	// TrustedKeys are a private market's owner-configured public keys (extension_market_keys.go); while there are
	// any, its catalog must be signed by one of them. Never set for the official market.
	TrustedKeys map[string]string `json:"trusted_keys,omitempty"` // key id → "ed25519:<base64>"
}

// MarketSignatureView is a market's signature as the apps show it.
type MarketSignatureView struct {
	State string `json:"state"`
	KeyID string `json:"key_id,omitempty"`
}

// CatalogItem is a market's plugin as the catalog shows it, with what this hub has of it.
type CatalogItem struct {
	Replaces           string                     `json:"replaces,omitempty"`
	RenameFrom         string                     `json:"rename_from,omitempty"`
	Market             string                     `json:"market"`
	MarketName         string                     `json:"market_name"`
	ID                 string                     `json:"id"`
	Name               string                     `json:"name"`
	Version            string                     `json:"version"`
	Description        string                     `json:"description,omitempty"`
	Publisher          string                     `json:"publisher,omitempty"`
	Homepage           string                     `json:"homepage,omitempty"`
	IconURL            string                     `json:"icon_url,omitempty"`
	Downloads          *int64                     `json:"downloads,omitempty"`
	DownloadsUpdatedAt string                     `json:"downloads_updated_at,omitempty"`
	Tags               []string                   `json:"tags"`
	Contributes        map[string][]string        `json:"contributes"`
	Permissions        []extension.PermissionLine `json:"permissions"`
	// Problem: why this hub cannot install it (no package for its platform, a newer hub needed); "" when it can.
	Problem string `json:"problem,omitempty"`
	// A newer release that needs an updated hub, independent of the active version's installability.
	LatestIncompatibleVersion string `json:"latest_incompatible_version,omitempty"`
	LatestIncompatibleReason  string `json:"latest_incompatible_reason,omitempty"`
	// Installed is the version this hub has ("" none); InstalledSource where that one is from (builtin, hub.json,
	// installed); FromThisMarket whether it was installed from this market; Upgradable whether this one is newer.
	Installed       string `json:"installed,omitempty"`
	InstalledSource string `json:"installed_source,omitempty"`
	FromThisMarket  bool   `json:"from_this_market,omitempty"`
	Upgradable      bool   `json:"upgradable,omitempty"`
}

// marketBefore orders markets: the official one first, then by when they were added.
func marketBefore(a, b extension.MarketRecord) int {
	if oa, ob := extension.IsOfficialMarket(a.Spec), extension.IsOfficialMarket(b.Spec); oa != ob {
		if oa {
			return -1
		}
		return 1
	}
	if c := a.AddedAt.Compare(b.AddedAt); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// marketName is what the apps show for a market: the official one is "Official extensions" in l whatever its index calls itself
// (#5017), the others the name their index gives.
func marketName(l i18n.Lang, rec extension.MarketRecord) string {
	if extension.IsOfficialMarket(rec.Spec) {
		return i18n.T(l, "hub-extension_market.official_name")
	}
	return rec.Name
}

func (h *Manager) marketView(s *marketState) MarketView {
	l := h.userLang()
	// The error and warnings are kept as the last read wrote them; they are shown in the owner's language, like the name.
	v := MarketView{ID: s.rec.ID, Spec: s.rec.Spec, Name: marketName(l, s.rec), HasToken: s.rec.TokenSealed != "", Warnings: []string{},
		Error: i18n.MarketText(l, s.rec.Error), FetchedAt: s.rec.FetchedAt, AddedAt: s.rec.AddedAt, Official: extension.IsOfficialMarket(s.rec.Spec),
		Signature: MarketSignatureView{State: s.rec.Signature.State, KeyID: s.rec.Signature.KeyID}}
	if len(s.rec.TrustedKeys) > 0 {
		v.TrustedKeys = marketKeyViews(s.rec.TrustedKeys)
	}
	if v.Signature.State == "" { // a market read before #1848
		v.Signature.State = extension.SigUnsigned
	}
	for _, w := range s.rec.Warnings {
		v.Warnings = append(v.Warnings, i18n.MarketText(l, w))
	}
	if s.index != nil {
		v.Description, v.Homepage, v.Extensions = s.index.Description, s.index.Homepage, len(s.index.Extensions)
		v.DownloadsUpdatedAt = s.index.DownloadsUpdatedAt
	}
	if v.Name == "" {
		v.Name = s.rec.Spec
	}
	return v
}

func (h *Manager) marketViews() []MarketView {
	ms := h.marketsState()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	states := make([]*marketState, 0, len(ms.byID))
	for _, s := range ms.byID {
		states = append(states, &marketState{rec: s.rec, index: s.index})
	}
	slices.SortFunc(states, func(a, b *marketState) int { return marketBefore(a.rec, b.rec) })
	out := []MarketView{}
	for _, s := range states {
		out = append(out, h.marketView(s))
	}
	return out
}

// catalog is every market's plugins, filtered by q (in the id, name, description, publisher, tags) and market.
func (h *Manager) catalog(q, market string) []CatalogItem {
	return h.catalogIn(h.userLang(), q, market)
}

// catalogIn is catalog with its problems in l.
func (h *Manager) catalogIn(l i18n.Lang, q, market string) []CatalogItem {
	q = strings.ToLower(strings.TrimSpace(q))
	out := []CatalogItem{}
	for _, s := range h.marketStates() {
		if s.index == nil || (market != "" && market != s.rec.ID) {
			continue
		}
		start := len(out)
		for i := range s.index.Extensions {
			e := &s.index.Extensions[i]
			m := e.Parsed
			ts := e.Translations()
			if q != "" && !strings.Contains(strings.ToLower(strings.Join(append(append([]string{m.ID, m.Name, m.Description, m.Publisher}, translatedWords(ts)...), e.Tags...), "\n")), q) {
				continue
			}
			out = append(out, h.catalogItem(l, s, e, ts))
		}
		if extension.IsOfficialMarket(s.rec.Spec) {
			slices.SortFunc(out[start:], officialCatalogOrder)
		}
	}
	return out
}

// officialCatalogOrder orders the official market's entries: most downloaded first, then by name, then by id. No two
// entries of a market compare equal, so the entries a filter keeps are in the order the whole catalog has them
// (marketChannels sorts only the ones it builds).
func officialCatalogOrder(a, b CatalogItem) int {
	count := func(it CatalogItem) int64 {
		if it.Downloads != nil {
			return *it.Downloads
		}
		return 0
	}
	if c := cmp.Compare(count(b), count(a)); c != 0 {
		return c
	}
	if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// marketStates are the markets with the indexes they have now, in the catalog's order (marketBefore).
func (h *Manager) marketStates() []*marketState {
	ms := h.marketsState()
	ms.mu.Lock()
	states := make([]*marketState, 0, len(ms.byID))
	for _, s := range ms.byID {
		states = append(states, &marketState{rec: s.rec, index: s.index})
	}
	ms.mu.Unlock()
	slices.SortFunc(states, func(a, b *marketState) int { return marketBefore(a.rec, b.rec) })
	return states
}

// catalogItem is entry e of market s as the catalog shows it in l (ts are e's translations), with what this hub has
// of it.
func (h *Manager) catalogItem(l i18n.Lang, s *marketState, e *extension.MarketEntry, ts extension.Translations) CatalogItem {
	m := e.Parsed
	it := CatalogItem{Replaces: e.Replaces, Market: s.rec.ID, MarketName: marketName(l, s.rec), ID: m.ID, Name: ts.Name(l, m.Name), Version: m.Version, Description: ts.Description(l, m.Description),
		Publisher: m.Publisher, Homepage: m.Homepage, IconURL: catalogIconURL(s.rec.ID, e), Downloads: e.Downloads, DownloadsUpdatedAt: s.index.DownloadsUpdatedAt,
		Tags: append([]string{}, e.Tags...), Contributes: m.Contributions(), Permissions: extension.ExplainIn(l, m)}
	verified := marketVerified(s.rec)
	if verified {
		if version, reason := e.LatestIncompatible(); reason != nil {
			it.LatestIncompatibleVersion, it.LatestIncompatibleReason = version, i18n.Localize(reason, l)
		}
	}
	if !verified {
		it.Problem = i18n.T(l, "hub-extension_market.bad_signature", i18n.Args{"err": i18n.MarketText(l, s.rec.Error)})
	} else if err := e.EngineProblem(h.build.Number); err != nil {
		it.Problem = i18n.Localize(err, l)
	} else if e.PackageFor(extension.Platform()) == nil {
		it.Problem = i18n.T(l, "hub-extension_market.no_package", i18n.Args{"platform": extension.Platform()})
	} else if err := h.cantRunHere(m); err != nil {
		it.Problem = i18n.Localize(err, l)
	}
	cur := h.extensions.reg.GetExact(m.ID)
	if cur == nil && e.Replaces != "" {
		if old := h.extensions.reg.GetExact(e.Replaces); old != nil && old.Record.Source == extension.SourceInstalled && old.Record.Origin == "market:"+s.rec.ID+"/"+e.Replaces {
			cur, it.RenameFrom = old, old.Record.ID
		}
	}
	if cur != nil {
		it.Installed, it.InstalledSource = cur.Record.Version, cur.Record.Source
		it.FromThisMarket = extension.FromMarket(cur.Record.Origin) == s.rec.ID
		it.Upgradable = cur.Record.Source == extension.SourceInstalled && extension.NewerVersion(m.Version, cur.Record.Version)
		switch {
		case cur.Record.Source == extension.SourceBuiltin:
			it.Upgradable = h.extensions.reg.CanHandoffBuiltin(m.ID) && h.builtinMarketUpgrade(s.rec, e)
		case cur.Record.BuiltinPredecessor:
			it.Upgradable = it.Upgradable && cur.Record.VerifiedBuiltinPredecessor() && h.builtinMarketUpgrade(s.rec, e)
		}
		if cur.Record.Source == extension.SourceInstalled || it.Upgradable { // an upgrade keeps its settings: the address config: lines name (#8570)
			it.Permissions = extension.ExplainWith(l, m, h.extensions.reg.SavedConfig(cur))
		}
	}
	return it
}

// translatedWords are the names and descriptions of every translation an entry carries: the catalog's search finds
// an extension by them whatever the language shown.
func translatedWords(ts extension.Translations) []string {
	var out []string
	for _, l := range i18n.Langs {
		if t := ts[l]; t != nil {
			out = append(out, t.Name, t.Description)
		}
	}
	return out
}

// marketEntry is a market's entry for plugin id.
func (h *Manager) marketEntry(market, id string) (extension.MarketRecord, *extension.MarketEntry, error) {
	if h.extensions != nil && h.extensions.reg != nil {
		id = h.extensions.reg.CanonicalID(id)
	}
	ms := h.marketsState()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return marketEntryLocked(ms, market, id)
}

func marketEntryLocked(ms *markets, market, id string) (extension.MarketRecord, *extension.MarketEntry, error) {
	s := ms.byID[market]
	if s == nil {
		return extension.MarketRecord{}, nil, errNoMarket
	}
	if !marketVerified(s.rec) {
		// Nothing is installed or upgraded from a market whose signature fails, not even from its last good index.
		return extension.MarketRecord{}, nil, i18n.Err("hub-extension_market.bad_signature_install", i18n.Args{"err": s.rec.Error})
	}
	if s.index != nil {
		for i := range s.index.Extensions {
			if e := &s.index.Extensions[i]; e.Parsed.ID == id {
				return s.rec, e, nil
			}
		}
		// Only an absent normal entry may resolve to its single declared successor in this source.
		var successor *extension.MarketEntry
		for i := range s.index.Extensions {
			e := &s.index.Extensions[i]
			if e.Replaces != id {
				continue
			}
			if successor != nil {
				return extension.MarketRecord{}, nil, i18n.Err("extension-identity.invalid")
			}
			successor = e
		}
		if successor != nil {
			return s.rec, successor, nil
		}
	}
	return extension.MarketRecord{}, nil, i18n.Err("hub-extension_market.not_in_market", i18n.Args{"id": id})
}
