package devicewire_test

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/devicewire"
)

func TestForwardingMethodsKeepLegacyWire(t *testing.T) {
	checkMethodWire(t, devicewire.AcceptMethod, "devices.accept",
		devicewire.Accept{ID: "relay", Kind: devicewire.AcceptLink, MachineID: "box"},
		`{"id":"relay","kind":"link","machine_id":"box"}`, buswire.None{}, `{}`)
	for _, tc := range []struct {
		method buswire.Method[devicewire.NodeCall, json.RawMessage]
		name   string
	}{
		{devicewire.CallMethod, "devices.call"},
		{devicewire.FileCallMethod, "devices.file_call"},
		{devicewire.RetireCallMethod, "devices.retire_call"},
		{devicewire.RequestMethod, "devices.request"},
	} {
		checkMethodWire(t, tc.method, tc.name,
			devicewire.NodeCall{MachineID: "box", Method: "example", Params: json.RawMessage(`{"name":"value"}`)},
			`{"machine_id":"box","method":"example","params":{"name":"value"}}`,
			json.RawMessage(`{"ok":true}`), `{"ok":true}`)
	}
	checkMethodWire(t, devicewire.PortDialMethod, "port.dial",
		devicewire.PortDial{ID: "stream", Port: 1234, StreamVersion: devicewire.PortStreamHalfCloseV1},
		`{"id":"stream","port":1234,"stream_version":1}`,
		devicewire.PortDialReply{StreamVersion: devicewire.PortStreamHalfCloseV1}, `{"stream_version":1}`)
}

func checkMethodWire[P, R any](t *testing.T, method buswire.Method[P, R], name string, params P, wantParams string, reply R, wantReply string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if method.Name != name {
			t.Errorf("method name = %q, want %q", method.Name, name)
		}
		if got, err := json.Marshal(params); err != nil || string(got) != wantParams {
			t.Errorf("wire params = %s, %v, want %s", got, err, wantParams)
		}
		if got, err := json.Marshal(reply); err != nil || string(got) != wantReply {
			t.Errorf("wire reply = %s, %v, want %s", got, err, wantReply)
		}
	})
}

func TestConnectorCredRequestWire(t *testing.T) {
	var req devicewire.ConnectorCredRequest
	if err := json.Unmarshal([]byte(`{"machine_id":"m1","future_field":true}`), &req); err != nil {
		t.Fatal(err)
	}
	if raw, err := json.Marshal(req); err != nil || string(raw) != `{"machine_id":"m1"}` {
		t.Fatalf("wire request = %s, %v", raw, err)
	}
}

func TestPairConnectorReplyKeepsLegacyWire(t *testing.T) {
	for _, reply := range []devicewire.PairConnectorReply{
		{},
		{Address: "https://host.example", Version: "v1"},
	} {
		want, _ := json.Marshal(map[string]any{"address": reply.Address, "version": reply.Version})
		if got, err := json.Marshal(reply); err != nil || string(got) != string(want) {
			t.Errorf("wire reply = %s, %v, want %s", got, err, want)
		}
	}
}
