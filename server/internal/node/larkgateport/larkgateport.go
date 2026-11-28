// Package larkgateport connects the lark module's node-side gate rules to internal/node without core importing
// internal/modules/lark (#13404). When the lark module is excluded (persona_without_lark, persona_core_only) or
// switched off (moduleswitch.FromFeatures), Get returns nil; generic proto.CommandGate handling stays in core.
//
//persona:layer library
package larkgateport

import "github.com/nwverse/persona/server/internal/moduleswitch"

const (
	BoundaryOrdinary = 0
	BoundaryOutbound = 1
	BoundaryDeletion = 2
)

// Gate supplies Feishu/Lark-specific node gate rules, bypass exemptions, and card wording.
type Gate interface {
	CLIRead(method, risk string) bool
	ClassifyAction(method, path string) (what string, boundary int, matched bool)
	CLIBypassSafe(path, risk string, args []string) bool
	CLIBotActor(args []string) bool
	ActorLabel(args []string) string
	CLISummary(method, path, risk string, args []string, what string) string
	DefaultTitle() string
}

var port = moduleswitch.NewPort[Gate]("node/larkgateport")

// Provide registers the lark module's node gate under its module id; internal/modules/all/node calls it from init.
func Provide(module string, g Gate) { port.Provide(module, g) }

// Get returns the lark module's node gate, or nil when excluded by build tags or switched off in s.
func Get(s moduleswitch.Set) Gate {
	g, _ := port.Get(s)
	return g
}
