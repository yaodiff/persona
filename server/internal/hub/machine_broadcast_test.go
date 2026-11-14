package hub

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// An old broadcast has read m1 and is blocked on m2. A real removal and a real
// manager report then emit newer full lists; the old broadcast must not land last.
func TestMachineBroadcastStaleAfterRealRemovalAndReport(t *testing.T) {
	h := newTestHub(t)
	app := &client{out: make(chan []byte, 8)}
	addTestClient(h, app)
	m1 := newMachine(h, MachineConfig{ID: "m1", Name: "one"})
	m2 := newMachine(h, MachineConfig{ID: "m2", Name: "two"})
	m1.managed, m1.owner = true, devicesOwner
	m2.managed, m2.owner = true, devicesOwner
	registerMachine(h, m1)
	registerMachine(h, m2)

	m2.mu.Lock()
	locked := true
	unlockM2 := func() {
		if locked {
			m2.mu.Unlock()
			locked = false
		}
	}
	defer unlockM2()
	oldDone := make(chan struct{})
	go func() {
		h.broadcastMachines()
		close(oldDone)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		stack := string(buf[:n])
		blocked := false
		for _, gr := range strings.Split(stack, "\n\n") {
			if strings.Contains(gr, "sync.Mutex.Lock") &&
				strings.Contains(gr, "hub.(*Machine).snapshot") &&
				strings.Contains(gr, "hub.(*Hub).broadcastMachines") {
				blocked = true
				break
			}
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			unlockM2()
			t.Fatalf("old broadcast did not reach m2:\n%s", stack)
		}
		time.Sleep(time.Millisecond)
	}

	goneDone := make(chan struct{})
	go func() {
		h.machineGone("m2")
		close(goneDone)
	}()
	for len(h.machinesNow()) != 1 {
		if time.Now().After(deadline) {
			unlockM2()
			t.Fatal("machineGone did not remove m2")
		}
		time.Sleep(time.Millisecond)
	}

	reportDone := make(chan struct{})
	go func() {
		h.applyDeviceStatesFrom(devicesOwner, devicewire.States{Machines: []devicewire.State{{
			Machine: proto.Machine{ID: "m1", Name: "one"}, Connected: true,
		}}})
		close(reportDone)
	}()
	for {
		m1.mu.Lock()
		online := m1.online
		m1.mu.Unlock()
		if online {
			break
		}
		if time.Now().After(deadline) {
			unlockM2()
			t.Fatal("manager report did not bring m1 online")
		}
		time.Sleep(time.Millisecond)
	}
	unlockM2()

	for name, done := range map[string]<-chan struct{}{"old": oldDone, "removal": goneDone, "report": reportDone} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s broadcast did not finish", name)
		}
	}

	var last []struct {
		ID     string `json:"id"`
		Online bool   `json:"online"`
	}
	for seen := 0; seen < 3; {
		select {
		case raw := <-app.out:
			var msg struct {
				Type     string `json:"type"`
				Machines []struct {
					ID     string `json:"id"`
					Online bool   `json:"online"`
				} `json:"machines"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Type == "machines" {
				last = msg.Machines
				seen++
			}
		case <-time.After(3 * time.Second):
			t.Fatal("missing machines event")
		}
	}
	if len(last) != 1 || last[0].ID != "m1" || !last[0].Online {
		t.Fatalf("final machine list is stale: %+v", last)
	}
}
