// Package helper is the type-aware domain rules' planted helper (TestDomainTypedPlanted): it imports no core and reaches
// none, so its visitors are reported where a package that reaches the core uses them.
package helper

// Flags is told an agent's visibility, with methods that name no type of the core.
type Flags struct{ quiet bool }

func (f *Flags) VisitListed()            { f.quiet = false }
func (f *Flags) VisitQuiet()             { f.quiet = true }
func (f *Flags) VisitHiddenByDirectory() { f.quiet = true }

// Quiet says what Flags was told.
func (f *Flags) Quiet() bool { return f.quiet }

// Peek is told a tally through methods that take its type parameters: it is a visitor only once they are the core's.
type Peek[S, W, I, T any] struct{ total *T }

func (p Peek[S, W, I, T]) VisitAgent(S, W, I)           {}
func (p Peek[S, W, I, T]) VisitCard(S, W)               {}
func (p Peek[S, W, I, T]) VisitAgentByCard(_, _ S, _ W) {}
func (p Peek[S, W, I, T]) VisitCardUnder(S)             {}
func (p Peek[S, W, I, T]) VisitTotal(total T)           { *p.total = total }

// NewPeek is a Peek that writes the total it is told into total.
func NewPeek[S, W, I, T any](total *T) Peek[S, W, I, T] { return Peek[S, W, I, T]{total: total} }

// Logf models a user-declared logger that forwards its variadic any arguments to fmt/log.
func Logf(string, ...any) {}

// Print models a helper taking a single any value that forwards it to reflection-based formatting.
func Print(any) {}
