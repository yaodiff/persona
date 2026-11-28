package serve

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
)

// Where a call comes from (#13246) crosses the wire as track, track_kind and categories, and reaches the connector in
// its context; categories not known stay absent (not an empty list, which is general), and a call that says nothing
// leaves the context as it is.
func TestCallFromCrossesTheWire(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   connector.CallContext
		json string
	}{
		{"general", connector.CallContext{Track: "main", Categories: []string{}}, `{"name":"x_read","track":"main","categories":[]}`},
		{"sensitive", connector.CallContext{Track: "qa-finance", TrackKind: "space", Categories: []string{"finance"}},
			`{"name":"x_read","track":"qa-finance","track_kind":"space","categories":["finance"]}`},
		{"not known", connector.CallContext{Track: "main"}, `{"name":"x_read","track":"main"}`},
		{"with origin device", connector.CallContext{Track: "main", Categories: []string{}, OriginDeviceID: "dev_abc123def456"},
			`{"name":"x_read","track":"main","categories":[],"origin_device_id":"dev_abc123def456"}`},
		{"origin device only", connector.CallContext{OriginDeviceID: "dev_abc123def456"},
			`{"name":"x_read","origin_device_id":"dev_abc123def456"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := CallParams{Name: "x_read", CallFrom: CallFromContext(connector.WithCallContext(context.Background(), tc.in))}
			b, _ := json.Marshal(p)
			if string(b) != tc.json {
				t.Fatalf("wire %s, want %s", b, tc.json)
			}
			var back CallParams
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatal(err)
			}
			got, ok := connector.CallContextOf(back.context(context.Background()))
			if !ok || got.Track != tc.in.Track || got.TrackKind != tc.in.TrackKind || got.OriginDeviceID != tc.in.OriginDeviceID || (got.Categories == nil) != (tc.in.Categories == nil) ||
				!slices.Equal(got.Categories, tc.in.Categories) {
				t.Fatalf("connector got %+v (%v), want %+v", got, ok, tc.in)
			}
		})
	}
	if _, ok := connector.CallContextOf(CallFrom{}.context(context.Background())); ok {
		t.Fatal("a call that says nothing gave a context")
	}
	var old CallParams
	if err := json.Unmarshal([]byte(`{"name":"x_read","args":{}}`), &old); err != nil || old.Categories != nil || old.OriginDeviceID != "" {
		t.Fatalf("an older hub's call: %+v %v", old, err)
	}
	do := DoParams{
		Name:      "x_write",
		RequestID: "req-1",
		CallFrom:  CallFromContext(connector.WithCallContext(context.Background(), connector.CallContext{Track: "main", OriginDeviceID: "dev_abc123def456"})),
	}
	b, err := json.Marshal(do)
	if err != nil {
		t.Fatal(err)
	}
	var doBack DoParams
	if err := json.Unmarshal(b, &doBack); err != nil {
		t.Fatal(err)
	}
	got, ok := connector.CallContextOf(doBack.context(context.Background()))
	if !ok || got.Track != "main" || got.OriginDeviceID != "dev_abc123def456" {
		t.Fatalf("DoParams round-trip got %+v (%v)", got, ok)
	}
}
