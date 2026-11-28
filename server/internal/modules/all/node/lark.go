//go:build !persona_without_lark && !persona_core_only

package node

import (
	"github.com/nwverse/persona/server/internal/modules/lark/larkgate"
	"github.com/nwverse/persona/server/internal/node/larkgateport"
)

// The Feishu / Lark module on the node: Feishu/Lark-specific command gate action rules, bypass exemptions and card wording (#13404).
func init() { larkgateport.Provide("lark", larkgate.New()) }
