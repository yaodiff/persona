package hub

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestBusRetirementCancelReachesManager(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newTestHub(t)
		started, cancelled := make(chan struct{}), make(chan struct{})
		playManager(t, h, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if method != devicewire.MethodRetireCall {
				return map[string]any{}, nil
			}
			close(started)
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- h.callBusAsIs(ctx, assistantwire.AssistantID, devicewire.MethodRetireCall,
				devicewire.NodeCall{MachineID: "m1", Method: nodeproto.StopIfIdle, Params: json.RawMessage(`{}`)}, nil)
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("manager did not receive guarded retirement")
		}
		cancel()
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("manager did not receive retirement cancellation")
		}
		if err := <-done; err == nil {
			t.Fatal("cancelled retirement succeeded")
		}
	})
}

func TestBusRetirementNeverFallsBackForOldManager(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newTestHub(t)
		calls := make(chan string, 3)
		playManager(t, h, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			if method != devicewire.MethodRetireCall && method != devicewire.MethodCall {
				return map[string]any{}, nil
			}
			calls <- method
			return nil, &rpc.Error{Code: bus.CodeUnknownMethod, Message: "old manager"}
		})
		h.devices.mu.Lock()
		h.devices.upOpen = map[string]bool{devicesOwner: true}
		h.devices.mu.Unlock()
		node := &busNode{h: h, id: "m1"}
		if err := node.Call(context.Background(), nodeproto.StopIfIdle, map[string]any{}, nil); err == nil {
			t.Fatal("an old manager accepted guarded retirement")
		}
		if len(calls) != 1 || <-calls != devicewire.MethodRetireCall {
			t.Fatal("guarded retirement fell back to an unconditional call")
		}
	})
}

func TestBusRetirementOnlyAcceptsGuardedStop(t *testing.T) {
	handler := busMethods[assistantwire.MethodDeviceRetire]
	if handler == nil {
		t.Fatal("the guarded retirement route is not registered")
	}
	for _, method := range []string{"session.stop", "session.send", "file.copy"} {
		raw, _ := json.Marshal(assistantwire.DeviceCall{Device: "m1", Method: method})
		if _, err := handler(newTestHub(t), context.Background(), &busConn{}, raw); err == nil {
			t.Fatalf("device.retire accepted %s", method)
		}
	}
}
