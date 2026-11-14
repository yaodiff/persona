package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/proto"
)

// startupDevices is one reachable computer whose listed configuration changes between startup rounds. Calls stay in
// process: the empty extension registry still makes the production sync ask for identity and then plugin.node.sync.
type startupDevices struct {
	Devices
	mu        sync.Mutex
	view      devices.View
	viewReads atomic.Int64
	synced    chan struct{}
}

func (d *startupDevices) Views() []devices.View {
	d.viewReads.Add(1)
	d.mu.Lock()
	defer d.mu.Unlock()
	return []devices.View{d.view}
}

func (d *startupDevices) Has(id string) bool { return id == "box" }

func (d *startupDevices) CallNode(_ context.Context, id, method string, _ json.RawMessage) (json.RawMessage, error) {
	if id != "box" {
		return nil, fmt.Errorf("unexpected machine %q", id)
	}
	switch method {
	case "plugin.node.identity":
		return json.RawMessage(`{"supported":false}`), nil
	case "plugin.node.sync":
		d.synced <- struct{}{}
		return json.RawMessage(`{"programs":[]}`), nil
	default:
		return nil, nil
	}
}

func (d *startupDevices) change(round int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.view.Name = fmt.Sprintf("Box %d", round)
	d.view.Kind = []string{"ssh", "local"}[round%2]
	d.view.SSHHost = fmt.Sprintf("box-%d", round)
}

func (d *startupDevices) config() MachineConfig {
	d.mu.Lock()
	defer d.mu.Unlock()
	return MachineConfig{ID: d.view.ID, Name: d.view.Name, Kind: d.view.Kind, SSHHost: d.view.SSHHost}
}

// BusUp refreshes the machine mirror while NodeConnected starts extension reconciliation. The registry is initialized:
// without it syncExtensionNodes returns before the machine ID read this regression is meant to cover. Run with -race.
func TestStartupMachineConfigurationRace(t *testing.T) {
	const rounds = 40
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	h.extensions = newExtensions(h.cfg.dir, nil, h.logf)
	d := &startupDevices{
		view: devices.View{
			Summary: devices.Summary{ID: "box", Online: true, Reachable: true},
			Info:    proto.NodeInfo{OS: "linux", Arch: "amd64"},
		},
		synced: make(chan struct{}, rounds),
	}
	h.opts.Devices = d

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for round := 0; round < rounds; round++ {
		d.change(round)
		start := make(chan struct{})
		done := make(chan struct{}, 2)
		go func() {
			<-start
			h.BusUp()
			done <- struct{}{}
		}()
		go func() {
			<-start
			h.NodeConnected("box")
			done <- struct{}{}
		}()
		close(start)
		for returned := 0; returned < 2; returned++ {
			select {
			case <-done:
			case <-timer.C:
				t.Fatalf("round %d did not return from BusUp and NodeConnected", round)
			}
		}
	}

	for completed := 0; completed < rounds; completed++ {
		select {
		case <-d.synced:
		case <-timer.C:
			t.Fatalf("only %d of %d extension reconciliations reached plugin.node.sync", completed, rounds)
		}
	}
	// The last sync signals before returning while still holding this lock. Taking it joins that return and, because
	// syncs are serialized, every earlier one too. The empty registry and Linux view make the built-in callbacks return
	// before node or package IO, and those read-only returns touch no test cleanup.
	l := h.extensions.nodes.lock("box")
	joined := make(chan struct{})
	go func() {
		l.Lock()
		l.Unlock()
		close(joined)
	}()
	select {
	case <-joined:
	case <-timer.C:
		t.Fatal("the last extension reconciliation did not return")
	}
	if got, want := d.viewReads.Load(), int64(2*rounds); got < want {
		t.Fatalf("machine mirror refreshed from %d device views, want at least %d", got, want)
	}
	h.fleetMu.Lock()
	m := h.byID["box"]
	h.fleetMu.Unlock()
	if m == nil {
		t.Fatal("the connected computer is missing")
	}
	if got, want := m.config(), d.config(); got != want {
		t.Fatalf("listed configuration = %+v, want %+v", got, want)
	}
	t.Logf("%d device-view refreshes and %d completed extension reconciliations", d.viewReads.Load(), rounds)
}
