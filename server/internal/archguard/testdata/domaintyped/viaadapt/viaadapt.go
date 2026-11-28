// Package viaadapt is the type-aware domain rules' planted application code that reaches a core only through its
// adapter (TestDomainTypedPlanted): it imports no core, and a line a rule refuses ends with a comment naming the rule.
package viaadapt

import (
	"github.com/nwverse/persona/server/internal/assistant/fleetcount/fleetcountadapt"
	"github.com/nwverse/persona/server/internal/proto"
	"planted/helper"
)

// quietView decides on an agent's visibility in application code, from the value the adapter hands out; its
// methods name no type of the core.
type quietView struct{ quiet bool } // planted: typed-visitor-impl

func (v *quietView) VisitListed()            { v.quiet = false }
func (v *quietView) VisitQuiet()             { v.quiet = true }
func (v *quietView) VisitHiddenByDirectory() { v.quiet = true }

// listedOnly has one of a visitor's methods and not its set: it implements none.
type listedOnly struct{}

func (listedOnly) VisitListed() {}

// Quiet branches on the visibility through the planted visitors, its own and a helper's, next to the twin that asks
// the core, and logs a core value obtained through the adapter to a custom logger.
func Quiet(s *proto.Session, logf func(string, ...any)) (bool, bool, bool) {
	var v quietView
	fleetcountadapt.Visibility(s).Accept(&v)
	var f helper.Flags // planted: typed-visitor-impl
	fleetcountadapt.Visibility(s).Accept(&f)
	logf("vis %v", fleetcountadapt.Visibility(s)) // planted: typed-codec
	logf("left out %v", fleetcountadapt.Visibility(s).LeftOut())
	return v.quiet, f.Quiet(), fleetcountadapt.Visibility(s).LeftOut()
}
