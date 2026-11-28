package connector

import (
	"context"
	"slices"
)

// CallContext is where a connector call comes from (#13246, docs/protocol/connector-protocol.md "Call context"): the
// calling conversation's track, the track's kind, and the information categories the conversation carries. A
// connector may refuse a call by them; whether the phone confirms a write stays the platform's decision.
type CallContext struct {
	// Track is the track's slug, "main" for the main track.
	Track string
	// TrackKind is the track's kind ("" when not known).
	TrackKind string
	// Categories are the conversation's information categories (none is general); nil when they are not known, which
	// a connector takes for sensitive.
	Categories []string
	// OriginDeviceID is the hub-authenticated paired device ID (for example "dev_...") that originated the current
	// user turn, or "" when the turn was not initiated by a paired user device (scheduled task, channel, extension,
	// or legacy master token). Set exclusively by the hub/manager from verified credentials; never client-supplied.
	OriginDeviceID string
}

type callContextKey struct{}

// WithCallContext is ctx for a call from c.
func WithCallContext(ctx context.Context, c CallContext) context.Context {
	c.Categories = slices.Clone(c.Categories)
	return context.WithValue(ctx, callContextKey{}, c)
}

// CallContextOf is where ctx's call comes from (WithCallContext); ok false when nobody said.
func CallContextOf(ctx context.Context) (CallContext, bool) {
	c, ok := ctx.Value(callContextKey{}).(CallContext)
	return c, ok
}
