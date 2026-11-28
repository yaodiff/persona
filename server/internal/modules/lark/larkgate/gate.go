// Package larkgate holds the module's lark-cli node command gate rules, bypass exemptions, and
// lark-cli phone-card wording (#13404). Generic proto.CommandGate HTTP rule matching and shell parsing stay in
// internal/node so non-Lark extension gates keep working in core-only builds.
//
//persona:layer library
package larkgate

// Gate implements the lark module's node-side gate rules (internal/node/larkgateport.Gate).
type Gate struct{}

// New returns a stateless Feishu/Lark gate rule set.
func New() *Gate { return &Gate{} }
