package extmgr

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/modapi"
	"github.com/nwverse/persona/server/internal/moduleswitch"
)

// Modules from extensions (#16254, docs/architecture/core-and-modules.md "Modules from extensions"): an installed
// extension supplies a module's flag only when the host's own list (modapi.ExtensionModules) names its id and the
// installation is the official one. What the package's manifest says never counts: it cannot name a module, and the
// reserved features are dropped from what it publishes (extension_api.go).

// moduleMarketWait bounds reading the markets for the report: the first report after a start may be the one that
// reads them from the database.
const moduleMarketWait = 10 * time.Second

// reportModules are the approved installed extensions that supply a module, each with whether its flag is on now.
func (h *Manager) reportModules() []extwire.ModuleProvider {
	ps := h.extensions
	if ps == nil {
		return nil
	}
	var out []extwire.ModuleProvider
	for _, m := range modapi.ExtensionModules() {
		p := ps.reg.SnapshotExact(string(m.ID))
		if p == nil {
			continue
		}
		if !approvedModuleProvider(m, p.Record, h.marketSpec) {
			continue
		}
		out = append(out, extwire.ModuleProvider{ID: extwire.ModuleID(m.ID), On: moduleProviderOn(m, p.Record, h.modules())})
	}
	return out
}

// approvedModuleProvider: rec is the official installation of module m. It is the extension of m's id, a package
// installed on this machine (not the built-in, not a remote plugin installed from remote: without a package; an
// installed package whose program is attached, Record.AttachedPackage, still is one), from a market whose spec is the
// official one (specOf: the spec of a market by id, false when there is none), verified with the official signing key
// or a retired one (extension.Official). A local upload, a private market's package and a remote plugin never are.
func approvedModuleProvider(m modapi.ExtensionModule, rec extension.Record, specOf func(id string) (string, bool)) bool {
	id := string(m.ID)
	if rec.ID != id || rec.Source != extension.SourceInstalled || (rec.Remote != nil && !rec.AttachedPackage()) {
		return false
	}
	mid := extension.FromMarket(rec.Origin)
	if mid == "" || rec.Origin != extension.MarketOrigin(mid, id) {
		return false
	}
	spec, ok := specOf(mid)
	if !ok || !extension.IsOfficialMarket(spec) {
		return false
	}
	return officialSigningKey(rec.MarketKey)
}

// officialSigningKey: key is the official market's signing key, current or retired.
func officialSigningKey(key string) bool {
	o := extension.Official
	if key == "" || o.SigningKey == "" {
		return false
	}
	return key == o.SigningKey || slices.Contains(o.PreviousSigningKeys, key)
}

// moduleProviderOn: the approved provider supplies module m's flag now: it is enabled, every contribution m needs is
// on, and the modules setting leaves m on. Whether its program runs, or has a problem, does not count: the flag says
// the owner's choice, and a program that is restarting keeps it.
func moduleProviderOn(m modapi.ExtensionModule, rec extension.Record, sw moduleswitch.Set) bool {
	if !rec.Enabled || !sw.On(string(m.ID)) {
		return false
	}
	for _, c := range m.Contributions {
		if slices.Contains(rec.DisabledContributions, c) {
			return false
		}
	}
	return true
}

// marketSpec is the spec of market id as the manager keeps it, false when there is none or the markets cannot be
// read. It reads the markets once (marketsLoad) and takes only the markets' lock, never the registry's with it.
func (h *Manager) marketSpec(id string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), moduleMarketWait)
	defer cancel()
	if h.marketsLoad(ctx) != nil {
		return "", false
	}
	ms := h.marketsState()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	s := ms.byID[id]
	if s == nil {
		return "", false
	}
	return s.rec.Spec, true
}
