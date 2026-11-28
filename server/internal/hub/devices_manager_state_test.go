package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"runtime"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

const perManagerOther = "m-0b0b0b"

// newPerManagerHub starts a hub with one machine for the carrier and one for another manager. The in-memory roster
// and settings keep the ownership assignment across startDevices, as a hub restart reads it.
func newPerManagerHub(t *testing.T, warm bool) *Hub {
	t.Helper()
	ctx := context.Background()
	h := newTestHub(t)
	st := newMeshStore()
	h.store = st
	h.bus.idents = &keptIdents{ids: &memIdents{}, kept: map[string]json.RawMessage{
		machineOwnersKey: json.RawMessage(`{"mb":"` + perManagerOther + `"}`),
	}}
	// The roster must already know the owner when startDevices reads the assignment.
	busToken(t, h, perManagerOther, perManagerOther)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "Ma", Kind: "ssh"}, {ID: "mb", Name: "Mb", Kind: "ssh"}}
	if warm {
		saved := devicesWarm{SavedAt: time.Now(), Machines: map[string]devicesWarmMachine{}}
		for _, id := range []string{"ma", "mb"} {
			saved.Machines[id] = devicesWarmMachine{Info: proto.NodeInfo{Hostname: id}}
		}
		if err := st.PutSetting(ctx, devicesWarmKey, saved); err != nil {
			t.Fatal(err)
		}
	}
	h.startDevices(ctx)
	if got := h.machineOwner("mb"); got != perManagerOther {
		t.Fatalf("mb's owner = %q, want %q", got, perManagerOther)
	}
	return h
}

func newPerManagerBusHub(t *testing.T, warm bool) (*Hub, *httptest.Server) {
	t.Helper()
	h := newPerManagerHub(t, warm)
	srv := newHubServer(t, h)
	t.Cleanup(func() {
		for _, c := range h.stopBus() {
			_ = c.ws.CloseNow()
		}
	})
	return h, srv
}

type perManagerList struct {
	Devices  []assistantwire.Device `json:"devices"`
	Complete bool                   `json:"complete"`
}

func perManagerDevicesList(t *testing.T, m *fakeManager) perManagerList {
	t.Helper()
	var out perManagerList
	if err := m.bus.call(assistantwire.MethodDevice, assistantwire.DeviceCall{
		Device: assistantwire.DeviceAll, Method: assistantwire.DeviceDevicesList,
	}, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// perManagerDevicesListFrom uses the same internal request seam for a manager whose bus connection has ended.
func perManagerDevicesListFrom(t *testing.T, h *Hub, from string) perManagerList {
	t.Helper()
	raw, err := (assistantLink{h}).deviceCallFrom(context.Background(), from, assistantwire.DeviceCall{
		Device: assistantwire.DeviceAll, Method: assistantwire.DeviceDevicesList,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out perManagerList
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCarrierDisconnectDoesNotDropOtherManagerDevices(t *testing.T) {
	h, srv := newPerManagerBusHub(t, false)
	a := connectFakeManager(t, h, srv, assistantwire.AssistantID)
	b := connectFakeManager(t, h, srv, perManagerOther)
	a.report(t, "ma")
	b.report(t, "mb")
	eventually(t, 5*time.Second, "both managers' machines reachable", func() bool {
		ma, mb := h.machineByID("ma"), h.machineByID("mb")
		return ma != nil && mb != nil && ma.snapshot().Online && mb.snapshot().Online && ma.reachable() && mb.reachable()
	})

	_ = a.bus.ws.Close(websocket.StatusNormalClosure, "test")
	eventually(t, 5*time.Second, "the carrier's machine offline", func() bool {
		ma := h.machineByID("ma")
		return h.busConnOf(assistantwire.AssistantID) == nil && ma != nil && !ma.snapshot().Online
	})
	if got := perManagerDevicesListFrom(t, h, assistantwire.AssistantID); len(got.Devices) != 1 ||
		got.Devices[0].ID != "ma" || got.Devices[0].Reachable {
		t.Fatalf("the carrier's list after disconnect = %+v, want ma unreachable", got)
	}
	if got := perManagerDevicesList(t, b); !got.Complete || len(got.Devices) != 1 || got.Devices[0].ID != "mb" ||
		!got.Devices[0].Online || !got.Devices[0].Reachable {
		t.Errorf("the connected manager's list after the carrier disconnect = %+v, want mb online, reachable, complete", got)
	}
}

func TestWarmCallWaitsForNonCarrierManager(t *testing.T) {
	h, srv := newPerManagerBusHub(t, true)
	mb := h.machineByID("mb")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var got struct {
		Who string `json:"who"`
	}
	res := make(chan error, 1)
	go func() { res <- mb.call(ctx, "test.ping", map[string]any{}, &got) }()
	select {
	case err := <-res:
		t.Fatalf("mb's call returned before its manager's first report: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	b := connectFakeManager(t, h, srv, perManagerOther)
	b.report(t, "mb")
	select {
	case err := <-res:
		if err != nil || got.Who != perManagerOther {
			t.Fatalf("mb's call after its manager reported: err %v, answer %+v", err, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mb's call stayed blocked after its manager reported")
	}
}

func TestWarmCallDeadlineIsPerManager(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newPerManagerHub(t, true)
		type pendingCall struct {
			id    string
			res   chan error
			early error
		}
		var calls []pendingCall
		for _, id := range []string{"ma", "mb"} {
			res := make(chan error, 1)
			calls = append(calls, pendingCall{id: id, res: res})
			go func() {
				res <- h.machineByID(id).call(context.Background(), "test.ping", map[string]any{}, nil)
			}()
		}
		synctest.Wait()
		for i := range calls {
			select {
			case calls[i].early = <-calls[i].res:
				t.Errorf("%s's call returned before warmUntil: %v", calls[i].id, calls[i].early)
			default:
			}
		}

		synctest.Sleep(devicesWarmGrace)
		synctest.Wait()
		for i := range calls {
			if calls[i].early != nil {
				if !errors.Is(calls[i].early, errManagerOffline) {
					t.Errorf("%s's early error = %v, want errManagerOffline", calls[i].id, calls[i].early)
				}
				continue
			}
			select {
			case err := <-calls[i].res:
				if !errors.Is(err, errManagerOffline) {
					t.Errorf("%s's call after warmUntil = %v, want errManagerOffline", calls[i].id, err)
				}
			default:
				t.Errorf("%s's call did not fail at warmUntil", calls[i].id)
			}
		}
		// Finish the coalesced manager update startDevices scheduled before leaving the fake-time bubble.
		synctest.Sleep(200 * time.Millisecond)
		synctest.Wait()
	})
}

func TestDevicesListCompleteWaitsForManagersOwnReport(t *testing.T) {
	h, srv := newPerManagerBusHub(t, false)
	a := connectFakeManager(t, h, srv, assistantwire.AssistantID)
	b := connectFakeManager(t, h, srv, perManagerOther)
	a.report(t, "ma")
	eventually(t, 5*time.Second, "the carrier's report", func() bool {
		return perManagerDevicesList(t, a).Complete
	})

	if got := perManagerDevicesList(t, b); got.Complete {
		t.Errorf("the other manager's list was complete before its first report: %+v", got)
	}
	b.report(t, "mb")
	eventually(t, 5*time.Second, "the other manager's own report", func() bool {
		return perManagerDevicesList(t, b).Complete
	})
}

// The report's bookkeeping is visible in the same locked transition that releases its waiters. Otherwise a disconnect
// in between can make a post-report call wait for the hub's warm window again.
func TestReportBookkeepingIsAtomicWithManagerUp(t *testing.T) {
	h := newTestHub(t)
	const count = 2000
	entries := make([]devicewire.RecMachine, count)
	h.cfg.Machines = make([]MachineConfig, count)
	for i := range entries {
		id := fmt.Sprintf("m-%04d", i)
		h.cfg.Machines[i] = MachineConfig{ID: id, Name: id, Kind: "ssh"}
		entries[i] = devicewire.RecMachine{ID: id}
	}
	done := make(chan struct{})
	go func() {
		h.applyDeviceStatesFrom(devicesOwner, devicewire.States{Entries: entries})
		close(done)
	}()
	for {
		d := &h.devices
		d.mu.Lock()
		up, reported := d.upOpen[devicesOwner], d.reportedSince[devicesOwner]
		d.mu.Unlock()
		if up {
			if !reported {
				t.Error("the manager is up before its report bookkeeping is visible")
			}
			break
		}
		select {
		case <-done:
			d.mu.Lock()
			defer d.mu.Unlock()
			if !d.upOpen[devicesOwner] || !d.reportedSince[devicesOwner] {
				t.Fatal("the report did not complete its up and bookkeeping state")
			}
			return
		default:
		}
		runtime.Gosched()
	}
	<-done
}

func TestDeviceCallInteractionsListStaysOnHub(t *testing.T) {
	h, srv := newPerManagerBusHub(t, false)
	a := connectFakeManager(t, h, srv, assistantwire.AssistantID)
	b := connectFakeManager(t, h, srv, perManagerOther)
	a.report(t, "ma")
	b.report(t, "mb")
	eventually(t, 5*time.Second, "both managers' machines online", func() bool {
		ma, mb := h.machineByID("ma"), h.machineByID("mb")
		return ma != nil && mb != nil && ma.snapshot().Online && mb.snapshot().Online
	})

	now := time.Now().UTC()
	h.takeFleetInteractionsFrom(assistantwire.AssistantID, []proto.Interaction{
		{ID: "ix-ma", MachineID: "ma", SessionID: "s1", Kind: proto.IxPermission, MirrorMain: true, CreatedAt: now},
		{ID: proto.ApprovalMirrorID("ix-ma", now), MirrorOf: "ix-ma", MachineID: "ma", SessionID: "main", Kind: proto.IxPermission, CreatedAt: now},
		{ID: "ix-a-global", SessionID: "s2", Kind: proto.IxPermission, CreatedAt: now.Add(time.Millisecond)},
	})
	h.takeFleetInteractionsFrom(perManagerOther, []proto.Interaction{
		{ID: "ix-mb", MachineID: "mb", SessionID: "s3", Kind: proto.IxQuestion, CreatedAt: now.Add(2 * time.Millisecond)},
	})

	callIDs := func(from, device string) ([]string, error) {
		raw, err := (assistantLink{h}).deviceCallFrom(context.Background(), from, assistantwire.DeviceCall{
			Device: device, Method: assistantwire.DeviceInteractionsList,
		})
		if err != nil {
			return nil, err
		}
		var out struct {
			Interactions []proto.Interaction `json:"interactions"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(out.Interactions))
		for _, ix := range out.Interactions {
			ids = append(ids, ix.ID)
		}
		return ids, nil
	}

	if ids, err := callIDs(assistantwire.AssistantID, assistantwire.DeviceAll); err != nil || len(ids) != 2 || ids[0] != "ix-ma" || ids[1] != "ix-a-global" {
		t.Fatalf("carrier DeviceAll = %v, %v", ids, err)
	}
	if ids, err := callIDs(assistantwire.AssistantID, "ma"); err != nil || len(ids) != 2 || ids[0] != "ix-ma" || ids[1] != "ix-a-global" {
		t.Fatalf("carrier ma = %v, %v", ids, err)
	}
	if ids, err := callIDs(perManagerOther, "mb"); err != nil || len(ids) != 1 || ids[0] != "ix-mb" {
		t.Fatalf("other mb = %v, %v", ids, err)
	}
	if _, err := callIDs(assistantwire.AssistantID, "mb"); err == nil {
		t.Fatal("carrier read another manager's machine interactions")
	}
	if _, err := callIDs(assistantwire.AssistantID, "missing"); err == nil {
		t.Fatal("unknown machine succeeded")
	}
}
