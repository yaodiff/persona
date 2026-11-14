package hub

import (
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/sessionlist"
	"github.com/nwverse/persona/server/internal/sessionsearch"
)

const featureSessionsSearch = "sessions_search"

// runtimeSearchContext hands the pure index the authoritative complete display
// catalogs it already serves. Continuations do not read mutable pin/name context.
func (h *Hub) runtimeSearchContext(r *http.Request, query string) (*sessionlist.SearchContext, error) {
	ctx := &sessionlist.SearchContext{Document: sessionsearch.Document, MachineNames: map[string]string{}, Pinned: map[string]bool{}, Errors: map[string]bool{}, Now: time.Now()}
	machines := h.machineList()
	hubs := map[string]bool{}
	for _, m := range machines {
		hubs[m.HubID] = true
	}
	for _, m := range machines {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		if len(hubs) > 1 {
			label := m.HubName
			if label == "" {
				label = m.HubID
			}
			if label != "" {
				name = label + " · " + name
			}
		}
		ctx.MachineNames[m.ID] = name
	}
	var pins Pins
	if sessionlist.SearchNeedsPins(query) && h.store != nil {
		if _, err := h.store.Setting(r.Context(), "pins", &pins); err != nil {
			return nil, err
		}
	}
	// Session IDs can contain slashes. Match each machine prefix against the
	// complete catalog instead of splitting a composite pin at its last slash.
	for _, key := range pins.Sessions {
		for _, m := range machines {
			if sid, ok := strings.CutPrefix(key, m.ID+"/"); ok {
				ctx.Pinned[runtimeListID(m.ID, sid)] = true
			}
		}
	}
	for _, ix := range h.pendingInteractions() {
		sid := ix.SessionID
		if ix.FoldedUnder != "" {
			sid = ix.FoldedUnder
		}
		ctx.Errors[runtimeListID(ix.MachineID, sid)] = true
	}
	return ctx, nil
}
