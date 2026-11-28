package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestRetirementCancelReachesHub(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	var unconditional atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn := bus.NewConn(r.Context(), ws)
		peer := rpc.New("h", conn)
		peer.OnRequest = func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
			switch method {
			case assistantwire.MethodDevice:
				var call DeviceCall
				if json.Unmarshal(raw, &call) == nil && (call.Method == nodeproto.StopIfIdle || call.Method == deviceSessionStop) {
					unconditional.Add(1)
				}
			case assistantwire.MethodDeviceRetire:
				var call DeviceCall
				if err := json.Unmarshal(raw, &call); err != nil || call.Method != nodeproto.StopIfIdle {
					return nil, &rpc.Error{Code: "invalid_params", Message: "wrong retirement method"}
				}
				close(started)
				<-ctx.Done()
				close(cancelled)
				return nil, ctx.Err()
			}
			return map[string]any{}, nil
		}
		_ = peer.Serve(r.Context(), conn)
	}))
	defer server.Close()
	b := NewBusHub(BusOptions{Hub: server.URL, Token: "test-token", Logf: testLogf(t)})
	store := &memStore{}
	s := New(Options{Hub: b, Store: func() Store { return store }, Logf: testLogf(t)})
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- b.Serve(ctx, s) }()
	defer func() { stop(); <-served }()
	waitFor(t, "the bus connection", func() bool { return b.client.Peer() != nil })
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := b.DeviceCall(callCtx, DeviceCall{Device: "box", Method: nodeproto.StopIfIdle,
			Params: json.RawMessage(`{"session_id":"s1","retire":true,"stop_cause":"task_completed"}`)})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("guarded retirement never reached the hub; unconditional calls=%d", unconditional.Load())
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the hub did not receive retirement cancellation")
	}
	if err := <-done; err == nil {
		t.Fatal("a cancelled retirement succeeded")
	}
	if unconditional.Load() != 0 {
		t.Fatal("retirement used the noncancellable device.call")
	}
}
