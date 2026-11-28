package badapp

import (
	"github.com/nwverse/persona/server/internal/assistant/fleetcount"
	"github.com/nwverse/persona/server/internal/domainkit/instant"
	"github.com/nwverse/persona/server/internal/domainkit/slot"
)

// view reads a tally itself.
type view struct{ rows int }

func (x *view) VisitAgent(a slot.Slot, w fleetcount.Why, s instant.Instant) { x.rows++ } // out-visitor-impl

// whyView branches on a closed set whose visitor methods take no parameters.
type whyView struct{ held bool }

func (x *whyView) VisitNeedsYou() { x.held = false } // out-visitor-impl
func (x *whyView) VisitHeld()     { x.held = true }  // out-visitor-impl

// unrelated has Visit* methods that match no imported domain visitor interface.
type unrelated struct{}

func (unrelated) VisitOther()     {}
func (unrelated) VisitHeld() bool { return false }
