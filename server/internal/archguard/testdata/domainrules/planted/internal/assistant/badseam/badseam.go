// Package badseam is a registration seam with an open field and an untyped port.
//
//persona:layer application seam
package badseam

import "sync"

// Hooks is the modules' hooks.
type Hooks struct {
	mu    sync.Mutex
	Named map[string]func() // seam-exported-field
}

// Register keeps a module's port.
func (h *Hooks) Register(name string, port any) {} // seam-any

type Store interface {
	Load() any           // seam-any
	Save(map[string]any) // seam-any
}

type Events func(map[string]any) // seam-any
type Payload map[string]any      // seam-any
type Value = interface{}         // seam-any

type Embedded interface {
	interface {
		Send(any) // seam-any
	}
}

type LoopName struct{ name string }

func (n LoopName) Text() string { return n.name } // core-accessor

type Registry struct{ loops []LoopName }

func (r *Registry) Loops() []LoopName { return r.loops } // core-accessor

type ModuleName LoopName
type moduleAlias = ModuleName

func (n ModuleName) Text() string   { return n.name }    // core-accessor
func (n moduleAlias) Label() string { return n.name }    // core-accessor
func (n LoopName) Label() string    { return (&n).name } // core-accessor

func (n moduleAlias) Matches(name string) bool { return n.name == name }

type privateName ModuleName

func (n privateName) Text() string { return n.name }
