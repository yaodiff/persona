package hub

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestBusFileCallCancelReachesManager(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newTestHub(t)
		started, cancelled := make(chan struct{}), make(chan struct{})
		playManager(t, h, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if method != devicewire.MethodFileCall {
				return map[string]any{}, nil
			}
			close(started)
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		var res json.RawMessage
		go func() {
			done <- h.callBusAsIs(ctx, assistantwire.AssistantID, devicewire.MethodFileCall,
				devicewire.NodeCall{MachineID: "m1", Method: "file.copy", Params: json.RawMessage(`{}`)}, &res)
		}()
		select {
		case <-started:
		case err := <-done:
			t.Fatalf("file.copy ended before reaching the manager: %v", err)
		case <-time.After(time.Second):
			t.Fatal("manager did not receive file.copy")
		}
		cancel()
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("manager's file.copy handler was not cancelled")
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancelled file.copy succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("caller did not stop waiting")
		}
	})
}

func TestBusOrdinaryCallRunsOnAfterCallerCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newTestHub(t)
		started, ended := make(chan struct{}), make(chan string, 1)
		playManager(t, h, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if method != devicewire.MethodCall {
				return map[string]any{}, nil
			}
			close(started)
			select {
			case <-ctx.Done():
				ended <- "cancelled"
			case <-time.After(300 * time.Millisecond):
				ended <- "ran on"
			}
			return map[string]any{}, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- h.callBusAsIs(ctx, assistantwire.AssistantID, devicewire.MethodCall,
				devicewire.NodeCall{MachineID: "m1", Method: "session.send", Params: json.RawMessage(`{}`)}, nil)
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("manager did not receive session.send")
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("caller: %v, want context.Canceled", err)
		}
		select {
		case got := <-ended:
			if got != "ran on" {
				t.Fatalf("ordinary devices.call was %q; the manager must run on", got)
			}
		case <-time.After(time.Second):
			t.Fatal("manager's ordinary call did not finish")
		}
	})
}

func TestBusFileCallFallsBackOnlyForOldManager(t *testing.T) {
	for _, tc := range []struct {
		name      string
		method    string
		firstErr  *rpc.Error
		wantCalls []string
		wantErr   bool
	}{
		{name: "old manager copy", method: "file.copy", firstErr: &rpc.Error{Code: bus.CodeUnknownMethod, Message: "unknown method devices.file_call"}, wantCalls: []string{devicewire.MethodFileCall, devicewire.MethodCall}},
		{name: "old manager zip", method: "file.zip", firstErr: &rpc.Error{Code: bus.CodeUnknownMethod, Message: "unknown method devices.file_call"}, wantCalls: []string{devicewire.MethodFileCall, devicewire.MethodCall}},
		{name: "other error is not retried", method: "file.copy", firstErr: &rpc.Error{Code: bus.CodeInvalidParams, Message: "bad file call"}, wantCalls: []string{devicewire.MethodFileCall}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newTestHub(t)
				calls := make(chan string, 2)
				playManager(t, h, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
					if method != devicewire.MethodFileCall && method != devicewire.MethodCall {
						return map[string]any{}, nil
					}
					calls <- method
					if method == devicewire.MethodFileCall {
						return nil, tc.firstErr
					}
					return map[string]any{"ok": true}, nil
				})
				h.devices.mu.Lock()
				h.devices.upOpen = true
				h.devices.mu.Unlock()
				var out map[string]any
				err := (&busNode{h: h, id: "m1"}).Call(context.Background(), tc.method, map[string]any{}, &out)
				if (err != nil) != tc.wantErr {
					t.Fatalf("Call error = %v, wantErr %v", err, tc.wantErr)
				}
				if !tc.wantErr && out["ok"] != true {
					t.Fatalf("Call result = %#v", out)
				}
				if len(calls) != len(tc.wantCalls) {
					t.Fatalf("manager received %d calls, want %d", len(calls), len(tc.wantCalls))
				}
				for _, want := range tc.wantCalls {
					if got := <-calls; got != want {
						t.Errorf("manager call = %q, want %q", got, want)
					}
				}
			})
		})
	}
}

func TestDeviceBusMethod(t *testing.T) {
	for method, want := range map[string]string{
		"file.copy":    devicewire.MethodFileCall,
		"file.zip":     devicewire.MethodFileCall,
		"file.move":    devicewire.MethodCall,
		"session.send": devicewire.MethodCall,
	} {
		if got := deviceBusMethod(method); got != want {
			t.Errorf("%s uses %q, want %q", method, got, want)
		}
	}
}
