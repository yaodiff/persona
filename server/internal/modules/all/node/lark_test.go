//go:build !persona_without_lark && !persona_core_only

package node

func init() { want = append(want, "lark") }
