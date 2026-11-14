package devices

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
)

func TestFileCallCancelReachesNode(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{fileStarted: make(chan struct{}), fileCancelled: make(chan struct{})}
	connectedMachine(t, d, "box", n)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := d.Call(ctx, devicewire.NodeCall{MachineID: "box", Method: "file.copy"})
		done <- err
	}()
	select {
	case <-n.fileStarted:
	case <-time.After(time.Second):
		t.Fatal("node did not receive file.copy")
	}
	cancel()
	select {
	case <-n.fileCancelled:
	case <-time.After(time.Second):
		t.Fatal("node's file.copy handler was not cancelled")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled file.copy succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("manager did not stop waiting")
	}
}

func TestFileCallRequestReachesNode(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{}
	connectedMachine(t, d, "box", n)
	params, _ := json.Marshal(devicewire.NodeCall{MachineID: "box", Method: "file.copy", Params: json.RawMessage(`{}`)})
	out, handled, err := d.Request(context.Background(), devicewire.MethodFileCall, params)
	if !handled || err != nil {
		t.Fatalf("file_call: handled=%v err=%v", handled, err)
	}
	var answer map[string]any
	raw, ok := out.(json.RawMessage)
	if !ok || json.Unmarshal(raw, &answer) != nil || answer["method"] != "file.copy" {
		t.Fatalf("file_call answer = %#v", out)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.calls) == 0 || n.calls[len(n.calls)-1] != "file.copy {}" {
		t.Fatalf("node calls = %v", n.calls)
	}
}
