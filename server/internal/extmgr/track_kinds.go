package extmgr

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/extension"
)

// DomainTrackKinds returns the enabled extensions' declarative track kinds.
// The assistant validates their configurations before applying them. Disabled
// and removed contributions disappear without deleting any track records.
func (h *Manager) DomainTrackKinds(ctx context.Context) ([]extension.TrackKindItem, error) {
	var out []extension.TrackKindItem
	for _, k := range h.domainTrackKinds() {
		out = append(out, k.TrackKindItem)
	}
	return out, nil
}

// domainTrackKind is a contributed kind with the extension that contributes it: the hub's word, never the manifest's
// (TrackKindItem takes no such field), so the assistant can tell the conversations that kind's own extension opens in
// one of its spaces from those any other extension opens (assistant/track_space_md.go spaceStateRefusal).
type domainTrackKind struct {
	extension.TrackKindItem
	Extension string `json:"extension"`
}

func (h *Manager) domainTrackKinds() []domainTrackKind {
	if h.extensions == nil || h.extensions.reg == nil {
		return nil
	}
	var out []domainTrackKind
	for _, p := range h.extensions.reg.List() {
		if !p.Record.Enabled || p.Manifest == nil {
			continue
		}
		for _, item := range p.Manifest.TrackKinds() {
			out = append(out, domainTrackKind{TrackKindItem: item, Extension: p.Record.ID})
		}
	}
	return out
}

// DomainTrackKindsJSON is the small in-process seam used by the assistant: each kind with the extension that
// contributes it.
func (h *Manager) DomainTrackKindsJSON(context.Context) (json.RawMessage, error) {
	return json.Marshal(h.domainTrackKinds())
}
