package assistantwire

import (
	"encoding/json"

	"github.com/nwverse/persona/server/internal/buswire"
)

// NotifyMethod delivers a notification from the assistant to the hub.
var NotifyMethod = buswire.Method[NotifyRequest, NotifyResult]{Name: MethodNotify}

// MarshalJSON keeps the key order of the original notify.request reply.
func (r NotifyResult) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Devices *int   `json:"devices,omitempty"`
		Held    bool   `json:"held,omitempty"`
		Kept    int    `json:"kept,omitempty"`
		OK      bool   `json:"ok"`
		Reason  string `json:"reason,omitempty"`
	}{
		Devices: r.Devices,
		Held:    r.Held,
		Kept:    r.Kept,
		OK:      r.OK,
		Reason:  r.Reason,
	})
}
