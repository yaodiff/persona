package devices

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/streams"
)

// A preview the hub opened on a node (preview.open, a headless browser tab on a stream) ends on the node when a new
// bus connection says hello, like a forwarded port: the node closes the tab only when its stream closes.
func TestANewBusConnectionEndsPreviewStreams(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{}
	connectedMachine(t, d, "box", n)
	if _, err := d.Call(context.Background(), devicewire.NodeCall{MachineID: "box", Method: "preview.open",
		Params: json.RawMessage(`{"id":"v1","port":5173}`)}); err != nil {
		t.Fatal(err)
	}
	d.Hello(json.RawMessage(`{}`))
	eventually(t, 2*time.Second, "the preview stream closed on the node", func() bool {
		return slices.Contains(n.eventsSeen(), `stream.close {"id":"v1"}`)
	})
}

func TestPortDialTrackedBeforeReply(t *testing.T) {
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "box"})
	d.addEntry(m)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	manager, node := rpc.New("m", a), rpc.New("n", b)
	m.peer = manager
	manager.OnEvent = m.noteClosed
	nodeEvents := make(chan string, 4)
	node.OnEvent = func(e string, p json.RawMessage) { nodeEvents <- e + " " + string(p) }
	asked := make(chan devicewire.PortDial, 1)
	reply := make(chan struct{})
	const replyJSON = `{"stream_version":1,"future_option":"<&>"}`
	node.OnRequest = func(ctx context.Context, method string, p json.RawMessage) (any, error) {
		var dial devicewire.PortDial
		if method != devicewire.MethodPortDial || json.Unmarshal(p, &dial) != nil {
			return nil, &rpc.Error{Code: "invalid_params"}
		}
		asked <- dial
		select {
		case <-reply:
			return json.RawMessage(replyJSON), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() { _ = manager.Serve(ctx, a) }()
	go func() { _ = node.Serve(ctx, b) }()
	type result struct {
		out json.RawMessage
		err error
	}
	callDone := make(chan result, 1)
	go func() {
		out, err := d.Call(ctx, devicewire.NodeCall{MachineID: "box", Method: devicewire.MethodPortDial,
			StreamVersion: devicewire.PortStreamHalfCloseV1, Params: json.RawMessage(`{"id":"p1","port":1234}`)})
		callDone <- result{out, err}
	}()
	select {
	case dial := <-asked:
		if dial.StreamVersion != devicewire.PortStreamHalfCloseV1 {
			t.Fatalf("manager dialed with stream version %d", dial.StreamVersion)
		}
	case <-ctx.Done():
		t.Fatal("port.dial did not reach the node")
	}
	d.Hello(json.RawMessage(`{}`))
	select {
	case event := <-nodeEvents:
		if want := streams.EventClose + ` {"id":"p1","abort":true}`; event != want {
			t.Fatalf("cleanup event %q, want %q", event, want)
		}
	case <-ctx.Done():
		t.Fatal("new bus did not clean up the pending dial")
	}
	close(reply)
	if got := <-callDone; got.err != nil || string(got.out) != replyJSON {
		t.Fatalf("forwarded port.dial reply = %s, %v, want %s", got.out, got.err, replyJSON)
	}
	m.mu.Lock()
	remaining := len(m.opened.streams)
	m.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("late reply restored %d tracked streams", remaining)
	}
}
