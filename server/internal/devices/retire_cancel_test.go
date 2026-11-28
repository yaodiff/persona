package devices

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
)

func TestRetirementCancelReachesNode(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{fileStarted: make(chan struct{}), fileCancelled: make(chan struct{})}
	connectedMachine(t, d, "box", n)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	params, _ := json.Marshal(devicewire.NodeCall{MachineID: "box", Method: nodeproto.StopIfIdle,
		Params: json.RawMessage(`{"session_id":"s1","retire":true,"stop_cause":"task_completed"}`)})
	go func() {
		_, handled, err := d.Request(ctx, devicewire.MethodRetireCall, params)
		if !handled {
			done <- errors.New("retirement request was not handled")
			return
		}
		done <- err
	}()
	select {
	case <-n.fileStarted:
	case err := <-done:
		t.Fatalf("retirement ended before reaching the node: %v", err)
	case <-time.After(time.Second):
		t.Fatal("node did not receive guarded retirement")
	}
	cancel()
	select {
	case <-n.fileCancelled:
	case <-time.After(time.Second):
		t.Fatal("node did not receive retirement cancellation")
	}
	if err := <-done; err == nil {
		t.Fatal("cancelled retirement succeeded")
	}
}

func TestRetirementRequestRejectsOtherMethods(t *testing.T) {
	d := newTestDevices(t)
	for _, method := range []string{"session.stop", "session.send", "file.copy"} {
		params, _ := json.Marshal(devicewire.NodeCall{MachineID: "box", Method: method})
		_, handled, err := d.Request(context.Background(), devicewire.MethodRetireCall, params)
		if !handled || err == nil {
			t.Fatalf("%s accepted by the retirement-only route: handled=%v err=%v", method, handled, err)
		}
	}
}
