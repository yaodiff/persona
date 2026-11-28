// Package kernel is where the assistant's modules meet: each registers its routes and hooks once, through typed
// methods, and the kernel serves them.
//
//persona:layer application seam
package kernel

import (
	"context"
	"net/http"

	"github.com/nwverse/persona/server/internal/agentref"
)

// Registry is the modules' routes and hooks, built once at start.
type Registry struct {
	mux     *http.ServeMux
	settled []func(ctx context.Context, agent agentref.Ref)
}

// NewRegistry is an empty registry.
func NewRegistry() *Registry { return &Registry{mux: http.NewServeMux()} }

// Route serves pattern with serve.
func (r *Registry) Route(pattern string, serve http.HandlerFunc) { r.mux.HandleFunc(pattern, serve) }

// TurnSettled calls fn whenever an agent's turn settles: hooks receive the session as a domain value.
func (r *Registry) TurnSettled(fn func(ctx context.Context, agent agentref.Ref)) {
	r.settled = append(r.settled, fn)
}

// Settle tells every hook that an agent's turn settled.
func (r *Registry) Settle(ctx context.Context, agent agentref.Ref) {
	for _, fn := range r.settled {
		fn(ctx, agent)
	}
}

type Store interface {
	Load() (any string)
	Save(any map[string]string)
}

type Events[T any] func(any T)
type Payload map[string]string

type LoopName struct{ name string }

func (n LoopName) Matches(name string) bool { return n.name == name }

func (r *Registry) HookCount() int { return len(r.settled) }

func (r *Registry) OnSettle() func(context.Context, agentref.Ref) { return r.Settle }
