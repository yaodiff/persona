package assistantwire_test

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestNotifyMethodWire(t *testing.T) {
	checkMethodWire[assistantwire.NotifyRequest, assistantwire.NotifyResult](t,
		assistantwire.NotifyMethod, "notify.request",
		`{"notification":{"id":"ntf_report","kind":"assistant","title":"Report ready"}}`,
		`{"ok":true}`)
}

func TestNotifyResultLegacyJSON(t *testing.T) {
	for _, tc := range []struct{ name, wire string }{
		{"unreported", `{"ok":true}`},
		{"zero devices", `{"devices":0,"ok":true,"reason":"no_device"}`},
		{"delivered and kept", `{"devices":2,"kept":1,"ok":true}`},
		{"held", `{"devices":0,"held":true,"ok":true,"reason":"ln_p1_held"}`},
		{"held by a hub without delivery counts", `{"held":true,"ok":true,"reason":"ln_p1_held"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkJSONRoundTrip[assistantwire.NotifyResult](t, tc.wire)
		})
	}

	if got, err := json.Marshal(assistantwire.NotifyResult{OK: true, Queued: true}); err != nil || string(got) != `{"ok":true}` {
		t.Errorf("queued notification wire = %s, %v, want {\"ok\":true}", got, err)
	}
}
