package extmgr

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A program on a computer calling the hub (plugin.node.hub_call, docs/extensions/extensions.md "Programs running on
// computers"). It has no hub_api and no token: its node relays the call on its own connection, so the machine is the
// one the request came in on, never something the program says. The hub answers it as it answers the extension's
// program on the hub, with the same scope checks (extensionKBCall), and only when:
//
//   - the method is on the allowlist (proto.PluginNodeHubMethod: knowledge-base calls only);
//   - the machine is one of this manager's computers;
//   - the extension exists and the identity revision is the one the hub started it with (a program left from before
//     an uninstall, a reinstall or a rename is refused);
//   - the extension is on, set up, and switched on for that machine (nodeRunsOn).

// NodeProgramHubCall answers a node's plugin.node.hub_call from machineID's connection.
func (h *Manager) NodeProgramHubCall(ctx context.Context, machineID string, raw json.RawMessage) (any, error) {
	var p proto.PluginNodeHubCall
	if err := json.Unmarshal(raw, &p); err != nil || p.ID == "" || p.Method == "" {
		return nil, &rpc.Error{Code: "invalid_params", Message: "plugin.node.hub_call: id and method"}
	}
	if !proto.PluginNodeHubMethod(p.Method) {
		return nil, &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method " + p.Method + " (not one a program on a computer may call)"}
	}
	denied := func(why string) (any, error) {
		return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: p.Method + ": " + why}
	}
	m := h.machineByID(machineID)
	if m == nil || h.extensions == nil || h.extensions.reg == nil {
		return denied("not from a computer of this hub")
	}
	ps, id := h.extensions, string(p.ID)
	pl := ps.reg.GetExact(id)
	if pl == nil || !ps.nodeIdentityCurrent(id, p.Revision) {
		return denied("no such extension program")
	}
	if !ps.nodeRunsOn(ctx, pl, m) {
		return denied("the extension is not switched on for this computer")
	}
	params := json.RawMessage(p.Params)
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	return ps.extensionKBCall(ctx, id, p.Method, params)
}
