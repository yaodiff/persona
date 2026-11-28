package extmgr

import (
	"slices"

	"github.com/nwverse/persona/server/internal/extension"
)

// builtinMarketUpgrade is the owner-visible choice to move a registered built-in
// to its package. A catalog cannot opt a built-in in, or switch the stable speech
// and transcription references used by existing recordings and client settings.
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
	// upgraded on a later build that no longer compiles in the speech module.
	for _, point := range []string{transcriptionPoint, speechPoint} {
		required := old.Full.Items(point)
		offered := m.Items(point)
		if len(required) == 0 {
			return false
		}
		for _, id := range required {
			if !slices.Contains(offered, id) {
				return false
			}
		}
	}
	return true
}
