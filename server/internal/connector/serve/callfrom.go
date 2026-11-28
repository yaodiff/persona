package serve

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/connector"
)

// CallFrom is where tools.call, actions.describe and actions.do come from (#13246, connector.CallContext,
// docs/protocol/connector-protocol.md "Call context"): the calling conversation's track, its kind, and its
// information categories. Categories absent means not known (an older hub, or not told): a connector takes that for
// sensitive; an empty list is general.
type CallFrom struct {
	Track          string    `json:"track,omitempty"`
	TrackKind      string    `json:"track_kind,omitempty"`
	Categories     *[]string `json:"categories,omitempty"`
	OriginDeviceID string    `json:"origin_device_id,omitempty"`
}

// CallFromContext is what ctx says of where its call comes from (connector.WithCallContext), for the hub's side of
// the wire.
func CallFromContext(ctx context.Context) CallFrom {
	c, ok := connector.CallContextOf(ctx)
	if !ok {
		return CallFrom{}
	}
	out := CallFrom{Track: c.Track, TrackKind: c.TrackKind, OriginDeviceID: c.OriginDeviceID}
	if c.Categories != nil {
		cats := slices.Clone(c.Categories)
		out.Categories = &cats
	}
	return out
}

// context is ctx carrying f, for the connector's side (connector.CallContextOf). Nothing said leaves ctx as it is.
func (f CallFrom) context(ctx context.Context) context.Context {
	if f.Track == "" && f.TrackKind == "" && f.Categories == nil && f.OriginDeviceID == "" {
		return ctx
	}
	c := connector.CallContext{Track: f.Track, TrackKind: f.TrackKind, OriginDeviceID: f.OriginDeviceID}
	if f.Categories != nil {
		c.Categories = append([]string{}, *f.Categories...)
	}
	return connector.WithCallContext(ctx, c)
}
