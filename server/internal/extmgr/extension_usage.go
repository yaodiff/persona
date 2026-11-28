package extmgr

import (
	"context"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// usage.gateway_accounts (#4576, docs/extensions/extensions.md"Quota", docs/server/usage-windows.md"Gateway account inventory"): the account
// lists of the account-pool gateways among the hub's usage sources, for an extension granted usage_gateways (a relay's quota
// extension lists on its sidebar page every account of the gateway, not only those one launcher's
// key reaches). The devices in this process keep the lists from their last read of each source; nothing is asked of a
// gateway here, and no key or address is in the answer.

// gatewayInventory exposes the same snapshot as the bus to a caller in the same process.
type gatewayInventory interface {
	UsageGatewayAccounts() proto.GatewayAccounts
}

func (h *Manager) usageGatewayAccounts(_ context.Context, id string) (any, error) {
	p := h.extensions.reg.Get(id)
	if p == nil || !p.Record.Granted.UsageGateways {
		return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: "usage.gateway_accounts needs the permission usage_gateways"}
	}
	if d, ok := h.opts.Devices.(gatewayInventory); ok {
		return d.UsageGatewayAccounts(), nil
	}
	return proto.GatewayAccounts{Sources: []proto.GatewaySource{}, Accounts: []proto.GatewayAccount{}}, nil
}
