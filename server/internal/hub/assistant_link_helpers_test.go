package hub

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// assistantLink as an assistant.Hub for the tests that run the assistant in-process (assistant_link_test.go,
// assistant_permission_scope_test.go): each method is the bus handler's call as the built-in assistant makes it. The
// running hub answers these on the bus instead (assistant_link.go init, bus_forward.go).

func (l assistantLink) DeviceCall(ctx context.Context, p assistantwire.DeviceCall) (json.RawMessage, error) {
	return l.deviceCallFrom(ctx, assistantwire.AssistantID, p)
}

func (l assistantLink) AppCall(ctx context.Context, p assistantwire.AppCall) (json.RawMessage, error) {
	return l.appCallFrom(ctx, assistantwire.AssistantID, p)
}

// NotifyRequest delivers a push the manager decided on (fleet_link.go deliverPush): every device whose settings
// take it. The held/reason answer says whether ln mode kept it for the briefing.
func (l assistantLink) NotifyRequest(_ context.Context, p assistantwire.NotifyRequest) (assistantwire.NotifyResult, error) {
	heldReason, err := l.h.deliverPushFrom(assistantwire.AssistantID, p.Notification)
	if err != nil {
		return assistantwire.NotifyResult{}, err
	}
	res := assistantwire.NotifyResult{OK: true}
	if heldReason != "" {
		res.Held, res.Reason = true, heldReason
	}
	return res, nil
}

// Broadcast sends the assistant's live update to the apps as it is (ws.broadcast).
func (l assistantLink) Broadcast(_ context.Context, b assistantwire.Broadcast) {
	if err := l.h.relayBroadcastFrom(assistantwire.AssistantID, b.Message); err != nil {
		l.h.logf("assistant: broadcast: %v", err)
	}
}

// Snapshot keeps what the assistant reported for the hub's own features.
func (l assistantLink) Snapshot(ctx context.Context, s assistantwire.Snapshot) (assistantwire.SnapshotResult, error) {
	return l.snapshotFrom(ctx, assistantwire.AssistantID, s)
}
