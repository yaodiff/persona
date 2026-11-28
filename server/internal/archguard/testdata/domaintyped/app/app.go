// Package app is the type-aware domain rules' planted application code (TestDomainTypedPlanted): a line a rule
// refuses ends with a comment naming the rule, and the twins beside it pass.
package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nwverse/persona/server/internal/agentref"
	"github.com/nwverse/persona/server/internal/agentref/agentrefadapt"
	"github.com/nwverse/persona/server/internal/assistant/fleetcount"
	"github.com/nwverse/persona/server/internal/assistant/fleetcount/fleetcountadapt"
	"github.com/nwverse/persona/server/internal/domainkit/fail"
	"github.com/nwverse/persona/server/internal/domainkit/instant"
	"github.com/nwverse/persona/server/internal/domainkit/outcome"
	"github.com/nwverse/persona/server/internal/domainkit/slot"
	"github.com/nwverse/persona/server/internal/proto"
	"planted/helper"
)

// peek decides on a tally's total in application code, whatever its methods' parameters are spelled.
type peek struct{ total fleetcount.RowCount } // planted: typed-visitor-impl

func (p *peek) VisitAgent(slot.Slot, fleetcount.Why, instant.Instant) {}
func (p *peek) VisitCard(slot.Slot, fleetcount.Why)                   {}
func (p *peek) VisitAgentByCard(_, _ slot.Slot, _ fleetcount.Why)     {}
func (p *peek) VisitCardUnder(slot.Slot)                              {}
func (p *peek) VisitTotal(total fleetcount.RowCount)                  { p.total = total }

// Peeker is a helper's generic type, a tally visitor once instantiated with the core's types.
func Peeker(total *fleetcount.RowCount) fleetcount.TallyVisitor {
	return helper.NewPeek[slot.Slot, fleetcount.Why, instant.Instant](total) // planted: typed-visitor-impl
}

// host has a method of a visitor's name and no visitor's set: it implements none.
type host struct{}

func (host) VisitAgent(s *proto.Session) {}

// holder keeps a core value in a field, which %v prints through reflection.
type holder struct{ ref agentref.Ref }

type logger interface{ Printf(string, ...any) }

type round struct{ logf func(string, ...any) }

type anyBox struct{ v any }

type ctxKey struct{}

func keepRef(agentref.Ref) {}

func refPair(ref agentref.Ref) (string, agentref.Ref) { return "ref %v", ref }

func returnBoxed(ref agentref.Ref) any {
	return ref // planted: typed-codec
}

// Run drops outcomes and failures, prints core values and compares failures, next to the twins that do not.
func Run(s *proto.Session, f1, f2 fail.Fail, ref agentref.Ref, logf func(string, ...any), l logger, ch chan any) string {
	_ = fleetcountadapt.Agent(s, false, 0) // planted: typed-discard
	fleetcountadapt.Agent(s, false, 0)     // planted: typed-discard
	if _, failed := outcome.Unpack(fleetcountadapt.Agent(s, false, 0)); failed != nil {
		return fleetcountadapt.Log(failed)
	}
	if b, err := json.Marshal(ref); err == nil { // planted: typed-codec
		return string(b)
	}
	if f1 == f2 { // planted: typed-fail-compare
		return ""
	}
	logf("ref %v", ref) // planted: typed-codec
	logf(refPair(ref))  // planted: typed-codec
	r := round{logf: logf}
	r.logf("holder %v", holder{ref: ref}) // planted: typed-codec
	l.Printf("ref %v", ref)               // planted: typed-codec
	helper.Logf("ref %v", ref)            // planted: typed-codec
	helper.Print(ref)                     // planted: typed-codec
	_ = any(ref)                          // planted: typed-codec
	var boxed any = ref                   // planted: typed-codec
	boxed = ref                           // planted: typed-codec
	_ = boxed
	_ = []any{ref}                 // planted: typed-codec
	_ = map[string]any{"ref": ref} // planted: typed-codec
	_ = anyBox{v: ref}             // planted: typed-codec
	ch <- ref                      // planted: typed-codec
	// Compliant twins: adapter-formatted strings, plain booleans, context.WithValue, and typed non-any calls pass.
	logf("ref %s", agentrefadapt.Log(ref))
	r.logf("same %v", ref.IsSame(ref))
	l.Printf("same %v", ref.IsSame(ref))
	helper.Logf("count %d", 1)
	helper.Print(agentrefadapt.Log(ref))
	keepRef(ref)
	_ = context.WithValue(context.Background(), ctxKey{}, ref)
	_ = returnBoxed(ref)
	if f1 == nil {
		return fmt.Sprintf("%s", agentrefadapt.Log(ref))
	}
	return fmt.Sprintf("%v", holder{ref: ref}) // planted: typed-codec
}
