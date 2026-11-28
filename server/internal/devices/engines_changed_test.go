package devices

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// reported says whether the machines were due to be told to the apps since the last call (Devices.changed).
func reported(d *Devices) bool {
	select {
	case <-d.reportDue:
		return true
	default:
		return false
	}
}

// A node's engines.changed (Orca quit or opened after node.info, node/orcaengine.go, #17037) is the machine's engines
// from then on, and the apps are told; one that says nothing new, an empty one and one from a machine let go change
// nothing.
func TestEnginesChangedIsTheMachines(t *testing.T) {
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "box", Name: "box"})
	herdrRow := proto.Engine{ID: proto.EngineHerdr, Name: "herdr", Available: true}
	running := proto.Engine{ID: proto.EngineOrca, Name: "Orca", Available: true, Version: "1.4.277", State: proto.EngineRunning}
	m.mu.Lock()
	m.online = true
	m.info.Engines = []proto.Engine{herdrRow, running}
	m.mu.Unlock()
	reported(d)

	stopped := proto.Engine{ID: proto.EngineOrca, Name: "Orca", Version: "1.4.277", State: proto.EngineStopped}
	b, _ := json.Marshal(proto.EnginesChanged{Engines: []proto.Engine{herdrRow, stopped}})
	m.onEvent(proto.EventEnginesChanged, b)
	if got := m.snapshot().Engines; len(got) != 2 || got[1] != stopped {
		t.Fatalf("after Orca quit: %+v", got)
	}
	if !reported(d) {
		t.Error("the apps were not told")
	}

	m.onEvent(proto.EventEnginesChanged, b) // the same again
	m.onEvent(proto.EventEnginesChanged, json.RawMessage(`{"engines":[]}`))
	m.onEvent(proto.EventEnginesChanged, json.RawMessage(`not json`))
	if got := m.snapshot().Engines; len(got) != 2 || got[1] != stopped {
		t.Fatalf("after nothing new: %+v", got)
	}
	if reported(d) {
		t.Error("told with nothing new")
	}

	ctx, stop := context.WithCancel(context.Background())
	stop()
	m.ctx = ctx // let go (stopped)
	b, _ = json.Marshal(proto.EnginesChanged{Engines: []proto.Engine{herdrRow, running}})
	m.onEvent(proto.EventEnginesChanged, b)
	if got := m.snapshot().Engines; got[1] != stopped {
		t.Errorf("a machine let go took a late list: %+v", got)
	}
}
