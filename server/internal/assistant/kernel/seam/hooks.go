package seam

import (
	"context"
	"strconv"

	"github.com/nwverse/persona/server/internal/agentref"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/domainkit/defect"
)

// The hooks: what a module does when something happens to the assistant as a whole. Each runs where its module's line
// ran before it had one, and each module has at most one of each kind: a hook has no name of its own, so its module is
// its key. Hooks run in registration order, on the goroutine the Registry method is called on.

// hooks are the modules' hooks of one kind, in registration order.
type hooks[F any] struct {
	kind string // the Kernel method that registers them, for defects
	list []hook[F]
}

// hook is one module's hook.
type hook[F any] struct {
	module ModuleName
	run    F
}

// add keeps run as module's hook; the caller has refused a nil run. A second hook of the kind from one module is a
// defect.
func (h *hooks[F]) add(op string, module ModuleName, run F) {
	for _, e := range h.list {
		if e.module == module {
			defect.Raise(op, "module "+strconv.Quote(module.name)+" has a "+h.kind+" hook already")
		}
	}
	h.list = append(h.list, hook[F]{module: module, run: run})
}

// TurnSettled runs fn with an Agent's session once its turn has ended and stayed still for the settle delay, unless
// the turn was a meeting's side exchange: where package assistant's turnEnded runs its settled-turn work
// (Registry.TurnSettled). A nil fn, a second TurnSettled hook from this module, or a call after Seal is a defect.
func (k Kernel) TurnSettled(fn func(ctx context.Context, agent agentref.Ref)) {
	const op = "seam.Kernel.TurnSettled"
	reg := k.registry(op)
	reg.open(op)
	if fn == nil {
		defect.Raise(op, "module "+strconv.Quote(k.module.name)+"'s TurnSettled hook is nil")
	}
	reg.settled.add(op, k.module, fn)
}

// Connected runs fn after each handshake with the hub, once the connection counts as up, on a goroutine of its own
// (Registry.Connected). A nil fn, a second Connected hook from this module, or a call after Seal is a defect.
func (k Kernel) Connected(fn func(ctx context.Context)) {
	const op = "seam.Kernel.Connected"
	reg := k.registry(op)
	reg.open(op)
	if fn == nil {
		defect.Raise(op, "module "+strconv.Quote(k.module.name)+"'s Connected hook is nil")
	}
	reg.connected.add(op, k.module, fn)
}

// Snapshot has fn fill the module's part of each snapshot the assistant reports, before the Agents' work is read
// (Registry.Snapshot). A nil fn, a second Snapshot hook from this module, or a call after Seal is a defect.
func (k Kernel) Snapshot(fn func(ctx context.Context, snap *assistantwire.Snapshot)) {
	const op = "seam.Kernel.Snapshot"
	reg := k.registry(op)
	reg.open(op)
	if fn == nil {
		defect.Raise(op, "module "+strconv.Quote(k.module.name)+"'s Snapshot hook is nil")
	}
	reg.parts.add(op, k.module, fn)
}

// TurnSettled tells each module's TurnSettled hook, in registration order, that agent's turn settled.
func (r *Registry) TurnSettled(ctx context.Context, agent agentref.Ref) {
	r.made("seam.Registry.TurnSettled")
	for _, h := range r.settled.list {
		h.run(ctx, agent)
	}
}

// Connected tells each module's Connected hook, in registration order, that a handshake with the hub went through
// and the connection is up. The caller runs it on a goroutine of its own.
func (r *Registry) Connected(ctx context.Context) {
	r.made("seam.Registry.Connected")
	for _, h := range r.connected.list {
		h.run(ctx)
	}
}

// Snapshot has each module's Snapshot hook, in registration order, fill its part of snap.
func (r *Registry) Snapshot(ctx context.Context, snap *assistantwire.Snapshot) {
	r.made("seam.Registry.Snapshot")
	if snap == nil {
		defect.Raise("seam.Registry.Snapshot", "the snapshot is nil")
	}
	for _, h := range r.parts.list {
		h.run(ctx, snap)
	}
}
