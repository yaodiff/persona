package hub

import (
	"net/http"

	"github.com/nwverse/persona/server/internal/proto"
)

// featureElicitation is what a phone registers with (POST /api/v1/devices `features`) when it shows MCP forms and
// links and opens one from its push (#2433): such a card is pushed to it, and waits for it while no app is connected.
// Only the iPhone app registers it; an older hub drops the name, so nothing waits and nothing is pushed there.
const featureElicitation = "elicitation"

// takesElicitation: d registered for MCP forms and links by push.
func takesElicitation(d Device) bool { return hasFeature(d, featureElicitation) }

// pushesElicitation: a card's push would reach a phone that shows MCP forms and links now (fleet.clients
// `elicitation_push`). That is one registered for them, with a way to push it (its route is not none) and "Needs you"
// on, and not holding pushes back for an open desktop app (Phone Muted), as sendFrom decides for every card. An
// agent's mute is the manager's to add (assistant/fleet_ix.go elicitationReachable).
func (p *Pusher) pushesElicitation(desktopOnline bool) bool {
	if p == nil {
		return false
	}
	wanted := alertWanted(pushWants{Kind: "input"}.fn(), desktopOnline)
	for _, d := range p.list() {
		if !takesElicitation(d) || p.routeOf(d).Route == pushNone || p.desktopOpen != nil && p.desktopOpen(d.DeviceID) {
			continue
		}
		if wanted(devicePrefs(d.Prefs)) {
			return true
		}
	}
	return false
}

func (h *Hub) hasElicitationClient() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.role != roleReadOnly && c.role != roleScoped && c.has("elicitation") {
			return true
		}
	}
	return false
}

// requestHasElicitation binds REST opt-in to an authenticated connection's hello.
// Client headers alone never declare support, and legacy requests have no platform.
func (h *Hub) requestHasElicitation(r *http.Request) bool {
	who, rc := principalOf(r.Context()), requestClientOf(r.Context())
	if rc.Platform == "" || who.Role == roleReadOnly || who.Role == roleScoped {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		c.mu.Lock()
		matches := c.device == who.ID && c.meta.Platform == rc.Platform && c.meta.Version == rc.Version
		c.mu.Unlock()
		if matches && c.has("elicitation") {
			return true
		}
	}
	return false
}

func (h *Hub) requestHasApprovalMirrors(r *http.Request) bool {
	who, rc := principalOf(r.Context()), requestClientOf(r.Context())
	if rc.Platform == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		c.mu.Lock()
		matches := c.device == who.ID && c.meta.Platform == rc.Platform && c.meta.Version == rc.Version
		if rc.Platform == "web" {
			// Every tab says web/1 and shares the browser credential. Only the
			// requesting tab's own hello can opt its REST snapshot into copies.
			matches = matches && rc.Instance != "" && c.meta.Instance == rc.Instance
		}
		c.mu.Unlock()
		if matches && c.wants(featureApprovalMirrors) {
			return true
		}
	}
	return false
}

func visibleInteractions(ixs []proto.Interaction, elicitation bool) []proto.Interaction {
	out := make([]proto.Interaction, 0, len(ixs))
	for _, ix := range ixs {
		if ix.Kind != proto.IxElicitation || elicitation {
			out = append(out, ix)
		}
	}
	return out
}

// withMuted stamps each card with whether its agent or space is muted on this hub (#4360): an answer-only device
// (the watch) cannot read /api/v1/mutes, so it filters by this flag — a muted card is neither shown nor buzzes for.
// The phone keeps its own mutes and filters those itself. The flag is omitempty, so older clients decode the list
// exactly as before.
func (h *Hub) withMuted(ixs []proto.Interaction) []proto.Interaction {
	for i := range ixs {
		ixs[i].Muted = h.muted(ixs[i].MachineID, ixs[i].SessionID)
	}
	return ixs
}

// sendPendingInteractions queues the pending cards for c. The snapshot is taken and queued under h.mu, where
// broadcastTo queues: an interaction_resolved for a card in it is queued after it, never before (#3937).
func (h *Hub) sendPendingInteractions(c *client, elicitationOnly bool) {
	h.sendPendingInteractionSelection(c, elicitationOnly, false)
}

// sendPendingMirrorInteractions completes a pre-hello authoritative snapshot
// once the client declares display-copy support in client_hello.
func (h *Hub) sendPendingMirrorInteractions(c *client) {
	h.sendPendingInteractionSelection(c, false, true)
}

func (h *Hub) sendPendingInteractionSelection(c *client, elicitationOnly, mirrorsOnly bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ix := range h.pendingInteractions() {
		if mirrorsOnly && ix.MirrorOf == "" {
			continue
		}
		if ix.MirrorOf != "" && !c.wants(featureApprovalMirrors) {
			continue
		}
		if c.role == roleReadOnly && h.hiddenFromReadOnly(ix) {
			continue
		}
		if ix.Kind == proto.IxElicitation {
			if c.role == roleReadOnly || !c.has("elicitation") {
				continue
			}
		} else if elicitationOnly {
			continue
		}
		c.sendJSON(map[string]any{"type": "interaction", "interaction": ix})
	}
}
