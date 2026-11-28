package devices

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestUpdateNodesIgnoresUnusedParams(t *testing.T) {
	d := newTestDevices(t)
	for _, params := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`false`), json.RawMessage(`{"future_field":true}`)} {
		out, handled, err := d.Request(t.Context(), devicewire.MethodUpdateNodes, params)
		if err != nil || !handled {
			t.Fatalf("params %s: handled %v, err %v", params, handled, err)
		}
		if raw, err := json.Marshal(out); err != nil || string(raw) != `{}` {
			t.Fatalf("params %s: ack %s, err %v", params, raw, err)
		}
	}
}

func TestReconnectRequestKeepsLegacyDecode(t *testing.T) {
	d := newTestDevices(t)
	for _, tc := range []struct{ params, code, message string }{
		{`{"machine_id":"missing","future_field":true}`, devicewire.CodeNoDevice, `no machine "missing"`},
		{`{"machine_id":3}`, "invalid_params", "devices.reconnect: json: cannot unmarshal number into Go struct field .machine_id of type string"},
	} {
		_, handled, err := d.Request(t.Context(), devicewire.MethodReconnect, json.RawMessage(tc.params))
		var re *rpc.Error
		if !handled || !errors.As(err, &re) || re.Code != tc.code || re.Message != tc.message {
			t.Errorf("params %s: handled %v, err %v", tc.params, handled, err)
		}
	}
}
