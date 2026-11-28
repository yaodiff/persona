package extmgr

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/extension"
)

// builtinMarketUpgrade is the owner-visible choice to move a registered built-in
// to its package. A catalog cannot opt a built-in in, or switch the stable
// references (speech, transcription, voice) existing recordings and client
// settings use.
func (h *Manager) builtinMarketUpgrade(rec extension.MarketRecord, entry *extension.MarketEntry) bool {
	if !extension.IsOfficialMarket(rec.Spec) || !marketVerified(rec) || entry.Parsed == nil || entry.Replaces != "" {
		return false
	}
	m := entry.Parsed
	old := h.extensions.reg.SnapshotExact(m.ID)
	if old == nil || old.Full == nil || m.Runtime == nil || !m.Runtime.HTTP ||
		!(h.extensions.reg.CanHandoffBuiltin(m.ID) || old.Record.VerifiedBuiltinPredecessor()) {
		return false
	}
	// Resolve from the installed manifest so a verified successor can still be
	// upgraded on a later build that no longer compiles in the module. Every point
	// the built-in serves must be served by the package under the same ids; one
	// the package adds (voice's events subscription) is its own and agreed to in
	// the preview like any added permission.
	needed := old.Full.Contributions()
	if len(needed) == 0 {
		return false
	}
	for point, required := range needed {
		offered := m.Items(point)
		for _, id := range required {
			if !slices.Contains(offered, id) {
				return false
			}
		}
	}
	return !h.extensions.voiceStaysBuiltin(context.Background(), old)
}

// voiceStaysBuiltin says whether the built-in voice extension has something its official package cannot take over,
// so no upgrade is offered and the hub keeps the built-in (#16264): a quota gateway's upstream, which only the built-in
// is given (voiceSpec), or a computer to archive calls on, which the package's first version does not write. A
// verified successor is past that choice: upgrading it again is the package's own update.
func (ps *extensions) voiceStaysBuiltin(ctx context.Context, old *extension.Extension) bool {
	if !ps.isVoice(old) {
		return false
	}
	return ps.voiceGatewayState(ctx) != nil || voiceArchiveSet(ps.reg.SavedConfig(old))
}
