package devices

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestOversizedMCPWaitEventDoesNotReconnectMachine(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{}
	m := connectedMachine(t, d, "box", n)
	m.mu.Lock()
	before := m.peer
	m.mu.Unlock()
	params, err := json.Marshal(proto.MCPWaitEvent{CallID: strings.Repeat("x", 33<<20), Kind: proto.MCPWaitComputer})
	if err != nil {
		t.Fatal(err)
	}
	d.Notify(devicewire.NodeEvent{MachineID: "box", Event: proto.EventMCPWait, Params: params})
	select {
	case <-before.Done():
		t.Fatalf("one %d MiB mcp.wait event closed the machine connection: %v", len(params)>>20, before.Err())
	default:
	}
}
