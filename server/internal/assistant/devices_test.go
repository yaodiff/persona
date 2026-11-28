package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/i18n"
	"net/http"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
)

// fakeDevices is the manager's own device layer: it records the node calls it got.
type fakeDevices struct{ calls []string }

func (f *fakeDevices) Hello(json.RawMessage) {}
func (f *fakeDevices) BusUp()                {}
func (f *fakeDevices) Request(context.Context, string, json.RawMessage) (any, bool, error) {
	return nil, false, nil
}
func (f *fakeDevices) Event(string, json.RawMessage) bool     { return false }
func (f *fakeDevices) Handler(next http.Handler) http.Handler { return next }
func (f *fakeDevices) Has(id string) bool                     { return id == "box" }
func (f *fakeDevices) SetKeep(map[string][]string)            {}
func (f *fakeDevices) CallNode(_ context.Context, id, method string, _ json.RawMessage) (json.RawMessage, error) {
	f.calls = append(f.calls, id+" "+method)
	return json.RawMessage(`{"ok":true}`), nil
}

// The assistant's calls to a node go straight to its own device layer; what the hub answers (its lists, following a
// conversation, a send under the apps' once-only guard) and machines it does not have still go to the hub.
func TestDeviceCallsGoToTheDevicesFirst(t *testing.T) {
	d := &fakeDevices{}
	b := NewBusHub(BusOptions{Hub: "http://127.0.0.1:1", Token: "jb_x", Devices: d})
	ctx := context.Background()
	for _, method := range []string{DeviceSessionSend, DeviceSessionStart, DeviceSessionItems, DeviceAssistantSend} {
		if out, err := b.DeviceCall(ctx, DeviceCall{Device: "box", Method: method}); err != nil || string(out) != `{"ok":true}` {
			t.Fatalf("%s: %s %v", method, out, err)
		}
	}
	for _, c := range []DeviceCall{{Device: "box", Method: DeviceSessionsList}, {Device: "box", Method: assistantwire.DeviceSessionFollow},
		{Device: "box", Method: assistantwire.DeviceSessionSendOnce}, {Device: DeviceAll, Method: DeviceDevicesList},
		{Device: "elsewhere", Method: DeviceSessionSend}} {
		if _, err := b.DeviceCall(ctx, c); !errors.Is(err, bus.ErrOffline) { // to the hub, which is not there
			t.Fatalf("%s %s went to %v", c.Device, c.Method, err)
		}
	}
	if len(d.calls) != 4 {
		t.Fatalf("the devices got %v", d.calls)
	}
}

func (f *fakeDevices) MachineResources(string) (*proto.Machine, *proto.MetricSample, *bool) {
	return nil, nil, nil
}
func (f *fakeDevices) List() []devices.Summary                 { return nil }
func (f *fakeDevices) Summary(string) (devices.Summary, bool)  { return devices.Summary{}, false }
func (f *fakeDevices) Canonical(id string) string              { return id }
func (f *fakeDevices) Sessions(string) ([]proto.Session, bool) { return nil, false }
func (f *fakeDevices) AppendSessions(dst []proto.Session, _ string) ([]proto.Session, bool) {
	return dst, false
}
func (f *fakeDevices) Shown(string) (devices.Shown, bool) { return devices.Shown{}, false }
func (f *fakeDevices) SessionsWhere(string, func(*proto.Session) bool) ([]proto.Session, bool) {
	return nil, false
}
func (f *fakeDevices) Session(string, string) (proto.Session, bool) { return proto.Session{}, false }
func (f *fakeDevices) Watched(string, string) bool                  { return false }
func (f *fakeDevices) ConnectedAt(string) time.Time                 { return time.Time{} }
func (f *fakeDevices) Usage() assistantwire.UsageSummary            { return assistantwire.UsageSummary{} }
func (f *fakeDevices) CapacitySnapshot(context.Context) proto.CapacitySnapshot {
	return proto.CapacitySnapshot{}
}

func (f *fakeDevices) UsageAccounts(context.Context) []proto.UsageAccount { return nil }

func (f *fakeDevices) GatewayRetryAt(string, string, time.Time) time.Time { return time.Time{} }

func (f *fakeDevices) UsageGatewayAccounts() proto.GatewayAccounts { return proto.GatewayAccounts{} }

func (f *fakeDevices) Follow(context.Context, string, string, bool) error   { return nil }
func (f *fakeDevices) SetAttention(link.Attention)                          {}
func (f *fakeDevices) LinkRang(context.Context, string, time.Duration) bool { return false }

func (f *fakeDevices) HeldMessages(string, i18n.Lang) map[string][]proto.UndeliveredMessage {
	return nil
}
func (f *fakeDevices) HeldRetry(context.Context, string, string) (bool, error)  { return false, nil }
func (f *fakeDevices) HeldDelete(context.Context, string, string) (bool, error) { return false, nil }
func (f *fakeDevices) AgentTransfer(context.Context, devices.AgentTransfer) (devices.AgentTransferResult, error) {
	return devices.AgentTransferResult{}, errors.New("no transfers here")
}
