package extmgr

import (
	"context"

	"github.com/nwverse/persona/server/internal/extension"
)

func (h *Manager) decorateMarketMetadata(ctx context.Context, v *ExtensionView, p *extension.Extension) {
	market := extension.FromMarket(p.Record.Origin)
	if p.Record.Source != extension.SourceInstalled || market == "" || h.marketsLoad(ctx) != nil {
		return
	}
	ms := h.marketsState()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	s := ms.byID[market]
	if s == nil || s.index == nil || !marketVerified(s.rec) {
		return
	}
	for _, e := range s.index.Extensions {
		if e.Parsed == nil || e.Parsed.ID != p.Record.ID || p.Record.Origin != "market:"+market+"/"+e.Parsed.ID {
			continue
		}
		v.IconURL, v.Downloads, v.DownloadsUpdatedAt = e.IconURL, e.Downloads, s.index.DownloadsUpdatedAt
		if extension.ValidIconPath(e.Icon) {
			v.IconURL = ""
		}
		return
	}
}
