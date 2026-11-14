// Package mcpwait carries an opted-in MCP call's opaque correlator and enum-only in-process transitions. It holds no
// card content or routing names; the node and manager still relay only proto.MCPWaitEvent over their connections.
package mcpwait

import (
	"context"

	"github.com/nwverse/persona/server/internal/proto"
)

type callContextKey struct{}
type pushContextKey struct{}
type interactionKindContextKey struct{}

type interactionKind struct {
	id   string
	kind proto.MCPWaitKind
}

// WithCall returns ctx with callID attached. An empty ID leaves the context unchanged, preserving old-call behavior.
func WithCall(ctx context.Context, callID string) context.Context {
	if callID == "" {
		return ctx
	}
	return context.WithValue(ctx, callContextKey{}, callID)
}

// CallFrom returns the opaque MCP call correlator, or "" for an old or non-opted-in call.
func CallFrom(ctx context.Context) string {
	callID, _ := ctx.Value(callContextKey{}).(string)
	return callID
}

// WithPush installs the in-process push/restore operation for one live reporter. A push returns the one-time restore.
func WithPush(ctx context.Context, push func(proto.MCPWaitKind) func()) context.Context {
	if push == nil {
		return ctx
	}
	return context.WithValue(ctx, pushContextKey{}, push)
}

// Push reports one enum through the live reporter, or returns a no-op restore for an old or non-opted-in call.
func Push(ctx context.Context, kind proto.MCPWaitKind) func() {
	push, _ := ctx.Value(pushContextKey{}).(func(proto.MCPWaitKind) func())
	if push == nil {
		return func() {}
	}
	return push(kind)
}

// WithInteractionKind preserves what a card creator knows: presentation as a question does not necessarily mean an
// answer wait. The id scopes the semantic override to exactly one interaction, never to nested cards of another id.
func WithInteractionKind(ctx context.Context, id string, kind proto.MCPWaitKind) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, interactionKindContextKey{}, interactionKind{id: id, kind: kind})
}

// InteractionKindFrom returns the explicit semantic kind for id, if this card's creator supplied one.
func InteractionKindFrom(ctx context.Context, id string) (proto.MCPWaitKind, bool) {
	override, _ := ctx.Value(interactionKindContextKey{}).(interactionKind)
	if override.id == "" || override.id != id {
		return proto.MCPWaitUnknown, false
	}
	return override.kind, true
}
