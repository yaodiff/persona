package extwire

// ScopeBotsFeishuIdentity: reading the paired Lark bot's public identity, never a secret.
const ScopeBotsFeishuIdentity = "bots.feishu.identity"

// ScopeBotsFeishuHandoff: handing a group question to the Lark bot. Granted by name only.
const ScopeBotsFeishuHandoff = "bots.feishu.handoff"

func init() {
	addRouteScopes(map[string]string{
		"GET /api/v1/bots/feishu/identity":  ScopeBotsFeishuIdentity,
		"POST /api/v1/bots/feishu/handoffs": ScopeBotsFeishuHandoff,
	})
}
