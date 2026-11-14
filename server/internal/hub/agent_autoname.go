package hub

import (
	"context"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// featureAgentAutoname: POST /api/v1/sessions/{mid}/{sid}/autoname names one Agent session with the model when the
// user asks (docs/protocol/protocol.md "Naming an Agent on request", #7432). The name and the model call are the
// manager's (assistant/agent_naming_request.go); the hub checks who asks, gates a rolling upgrade and routes to the
// machine's owner.
const featureAgentAutoname = assistantwire.CapabilityAgentAutoname

// agentAutonameFeatures is the feature while every manager that carries Agents takes the request. A hub-wide feature
// cannot promise it for part of the fleet: an older or offline owner hides the action until it is ready.
func (h *Hub) agentAutonameFeatures() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ids := h.archiveManagers(ctx)
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if h.archiveManagerHas(id, assistantwire.CapabilityAgentAutoname) != http.StatusOK {
			return nil
		}
	}
	return []string{featureAgentAutoname}
}

// forwardAgentAutoname relays a full device's request to the manager that owns the machine. An extension's token is
// not given the route (extwire.RouteScopes): the request stands for the user's own wish over a name they may have
// written by hand.
func (h *Hub) forwardAgentAutoname(w http.ResponseWriter, r *http.Request) {
	if principalOf(r.Context()).Role != roleFull {
		writeErr(w, http.StatusForbidden, i18n.Err("assistant-agent_naming.forbidden"))
		return
	}
	owner, ok := h.owner(w, r, h.byMachine)
	if !ok {
		return
	}
	switch h.archiveManagerHas(owner, assistantwire.CapabilityAgentAutoname) {
	case http.StatusOK:
		h.forwardHTTP(w, r, owner)
	case http.StatusServiceUnavailable:
		writeErrCode(w, http.StatusServiceUnavailable, assistantwire.CodeAssistantOffline, errAssistantOffline)
	default: // an older manager has no such route
		writeErrCode(w, http.StatusNotImplemented, "agent_autoname_unsupported", i18n.Err("assistant-agent_naming.upgrade"))
	}
}
