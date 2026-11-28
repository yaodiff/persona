package extmgr

import (
	"errors"
	"net/http"
	"slices"
	"sort"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// ContributionView is one contribution of an extension and its switch (docs/extensions/extensions.md "Turning contributions on and off").
type ContributionView struct {
	Point   string `json:"point"`
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Enabled bool   `json:"enabled"`
	// CanDisable: the user can switch it here. Not for hub.json's extensions, nor where the switch is elsewhere
	// (Via: "connector" — a connector, or a channel bound to one, goes with the connector's switch; "config" — an agent
	// filter goes with a field of the extension's settings).
	CanDisable bool   `json:"can_disable"`
	Via        string `json:"via,omitempty"`
}

// contributionViews are p's contributions on the points this hub has, in a fixed order (by point, then as written).
func (ps *extensions) contributionViews(p *extension.Extension) []ContributionView {
	m := p.Full
	var out []ContributionView
	points := make([]string, 0, len(m.Contributes))
	for point := range m.Contributes {
		points = append(points, point)
	}
	sort.Strings(points)
	for _, point := range points {
		for _, id := range m.Items(point) {
			v := ContributionView{Point: point, ID: id, Title: extension.ContributionTitle(m, point, id),
				Enabled: !slices.Contains(p.Record.DisabledContributions, extension.ContributionKey(point, id)), CanDisable: !p.ReadOnly()}
			if m.SwitchedElsewhere(point, id) {
				v.Via, v.CanDisable = "connector", false
				v.Enabled = ps.connectorOn(m, point, id)
			}
			if _, ok := m.SwitchedByConfig(point, id); ok { // an agent filter's switch is its when field (agentfilters.go)
				v.Via, v.CanDisable = "config", false
				v.Enabled = p.Record.Enabled && ps.agentFilterHolds(p, id)
			}
			out = append(out, v)
		}
	}
	return out
}

// connectorOn is whether the connector that a contribution goes with is switched on.
func (ps *extensions) connectorOn(m *extension.Manifest, point, id string) bool {
	if ps.h == nil || ps.h.connectors == nil {
		return true
	}
	if point == "channel" {
		for _, c := range m.Channels() {
			if c.ID == id {
				id = c.Connector
			}
		}
	}
	return ps.h.connectors.Enabled(id)
}

// contributionRoutes: PUT /api/v1/extensions/{id}/-/contributions/{point}/{cid} {enabled} switches one contribution and
// answers the extension's detail. A connector's goes to the connector's own switch (the same as Settings › Connectors).
func (h *Manager) contributionRoutes(api *http.ServeMux) {
	api.HandleFunc("PUT /api/v1/extensions/{id}/-/contributions/{point}/{cid}", func(w http.ResponseWriter, r *http.Request) {
		if principalOf(r.Context()).Role != roleFull {
			writeErr(w, http.StatusForbidden, errors.New("this device can only read and answer"))
			return
		}
		ps := h.extensions
		var p *extension.Extension
		if ps != nil {
			p = ps.reg.Get(r.PathValue("id"))
		}
		if p == nil {
			writeErrCode(w, http.StatusNotFound, "not_found", errors.New("no such extension"))
			return
		}
		var in struct {
			Enabled *bool `json:"enabled"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if in.Enabled == nil {
			writeErrCode(w, http.StatusBadRequest, "bad_request", errors.New("enabled: true or false"))
			return
		}
		point, cid := r.PathValue("point"), r.PathValue("cid")
		if !slices.Contains(p.Full.Items(point), cid) {
			writeErrCode(w, http.StatusNotFound, "not_found", errors.New("no such contribution"))
			return
		}
		switch {
		case p.ReadOnly():
			writeErrCode(w, http.StatusConflict, "read_only", i18n.Err("hub-extension_contributions.read_only", i18n.Args{"id": p.Record.ID}))
			return
		case p.Full.SwitchedElsewhere(point, cid):
			connectorID := cid
			if point == "channel" {
				for _, c := range p.Full.Channels() {
					if c.ID == cid {
						connectorID = c.Connector
					}
				}
			}
			if h.connectors == nil || h.connectors.Get(connectorID) == nil {
				writeErrCode(w, http.StatusConflict, "not_running", i18n.Err("hub-extension_contributions.not_running"))
				return
			}
			if err := h.setConnectorEnabled(r.Context(), connectorID, *in.Enabled); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		default:
			if err := ps.reg.SetContribution(r.Context(), p.Record.ID, point, cid, *in.Enabled, actor(r)); err != nil {
				writeErrCode(w, http.StatusConflict, "read_only", err)
				return
			}
		}
		if point == "text_lookup" {
			h.textLookupChanged()
		}
		writeJSON(w, detailOut(r, ps.detail(r.Context(), ps.reg.Get(p.Record.ID))))
	})
}
