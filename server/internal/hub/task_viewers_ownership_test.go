package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
)

func waitTaskViewerScope(t *testing.T, got <-chan assistantwire.Clients, want []assistantwire.SessionRef) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	var last assistantwire.Clients
	for {
		select {
		case last = <-got:
			if reflect.DeepEqual(last.Viewing, want) {
				return
			}
		case <-deadline:
			t.Fatalf("manager viewer scope = %+v, want %+v", last.Viewing, want)
		}
	}
}

func TestTaskViewersFollowMachineOwnershipChanges(t *testing.T) {
	for _, kind := range []string{"configured move", "kept move", "kept owner update"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			var h *Hub
			var a, b string
			var gotA, gotB chan assistantwire.Clients
			var move func(string) error
			if kind == "configured move" {
				hub, srv, _ := newBusHub(t)
				h = hub
				h.cfg.Machines = []MachineConfig{{ID: "box", Name: "Box", Kind: "local"}}
				registerMachine(h, newMachine(h, h.cfg.Machines[0]))
				a, b = assistantwire.AssistantID, "m-0b0b0b"
				gotA, gotB = make(chan assistantwire.Clients, 64), make(chan assistantwire.Clients, 64)
				connectClientsManager(t, h, srv, a, gotA)
				connectClientsManager(t, h, srv, b, gotB)
				move = func(to string) error {
					_, err := h.moveMachine(ctx, "box", to)
					return err
				}
			} else {
				h = newTestHub(t)
				h.store = newMeshStore()
				h.bus.idents = &keptIdents{ids: &memIdents{}, kept: map[string]json.RawMessage{}}
				if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(`{"machines":[]}`), 0o600); err != nil {
					t.Fatal(err)
				}
				h.startDevices(ctx)
				srv := newHubServer(t, h)
				one := connectKeeper(t, h, srv, "m-0a0a0a", json.RawMessage(`{"id":"box","name":"Box","kind":"ssh","ssh_host":"box.example"}`))
				two := connectKeeper(t, h, srv, "m-0b0b0b")
				a, b, gotA, gotB = one.id, two.id, one.clients, two.clients
				h.devices.moving.Lock()
				err := h.setKept(ctx, "box", a)
				h.devices.moving.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				move = func(to string) error {
					if kind == "kept move" {
						_, err := h.moveMachine(ctx, "box", to)
						return err
					}
					h.devices.moving.Lock()
					defer h.devices.moving.Unlock()
					return h.setKept(ctx, "box", to)
				}
			}

			oldA, oldB := h.busConnOf(a), h.busConnOf(b)
			c := &client{role: roleFull, subs: map[[2]string]bool{{"box", "agent"}: true}}
			h.mu.Lock()
			h.clients[c] = struct{}{}
			h.mu.Unlock()
			h.fleetClientsChanged()
			view := []assistantwire.SessionRef{{MachineID: "box", SessionID: "agent"}}
			waitTaskViewerScope(t, gotA, view)
			waitTaskViewerScope(t, gotB, nil)

			for _, to := range []string{b, a} {
				if err := move(to); err != nil {
					t.Fatalf("move to %s: %v", to, err)
				}
				if h.busConnOf(a) != oldA || h.busConnOf(b) != oldB || oldA == nil || oldB == nil {
					t.Fatal("the test reconnected a manager instead of refreshing its existing report")
				}
				if to == b {
					waitTaskViewerScope(t, gotA, nil)
					waitTaskViewerScope(t, gotB, view)
				} else {
					waitTaskViewerScope(t, gotA, view)
					waitTaskViewerScope(t, gotB, nil)
				}
				if got := h.viewedSessions(); !reflect.DeepEqual(got, view) {
					t.Fatalf("ownership changed the global viewer set: %+v", got)
				}
			}
		})
	}
}

func TestTaskViewerRefreshWaitsForMachineHandover(t *testing.T) {
	a, b := assistantwire.AssistantID, "m-0b0b0b"
	for _, from := range []string{a, b} {
		t.Run("from "+from, func(t *testing.T) {
			var updated atomic.Int32
			// Keep the handover's delayed manager/config notifications inside this fixture's lifetime.
			synctest.Test(t, func(t *testing.T) {
				to := b
				if from == b {
					to = a
				}
				h := newTestHub(t)
				h.bus.idents = &memIdents{}
				h.cfg.Machines = []MachineConfig{{ID: "box", Name: "Box", Kind: "local"}}
				m := newMachine(h, h.cfg.Machines[0])
				m.owner, m.online, m.connected = from, true, true
				registerMachine(h, m)
				view := []assistantwire.SessionRef{{MachineID: "box", SessionID: "agent"}}
				addTestClient(h, &client{role: roleFull, subs: map[[2]string]bool{{"box", "agent"}: true}})
				// The page has not changed, so the handover must force a new scoped report.
				h.reported.clients = assistantwire.Clients{Viewing: view}
				h.reported.clientsSent = map[string]*busConn{}
				type report struct {
					manager, assigned, cached string
					online, connected         bool
					viewing                   []assistantwire.SessionRef
				}
				reports := make(chan report, 8)
				notifications := make(chan struct{}, 2)
				allow := make(chan struct{})
				unblock := sync.OnceFunc(func() { close(allow) })
				for _, id := range []string{a, b} {
					c := rosterConn(h, id, writerFunc(func(raw []byte) (int, error) {
						var event struct {
							Event  string
							Params assistantwire.Clients
						}
						if err := json.Unmarshal(raw, &event); err != nil {
							return 0, err
						}
						if event.Event == assistantwire.EventClients {
							got := report{manager: id, assigned: h.entryOwner("box"), viewing: event.Params.Viewing}
							m.mu.Lock()
							got.cached, got.online, got.connected = m.owner, m.online, m.connected
							m.mu.Unlock()
							reports <- got
							// Stop at the first write: a later notification cannot hide an incomplete handover.
							<-allow
						}
						if event.Event == assistantwire.EventUpdated {
							updated.Add(1)
							notifications <- struct{}{}
						}
						return len(raw), nil
					}))
					c.presence = busPresence{ID: id, Role: bus.RoleManager}
					h.reported.clientsSent[id] = c
				}
				// moveMachineMoving commits the assignment before handing over the cached machine.
				h.devices.assign = map[string]string{"box": to}
				done := make(chan struct{})
				go func() {
					h.machineHandedOver("box", to)
					close(done)
				}()
				defer func() {
					unblock()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("handover did not finish after unblocking its notification")
					}
				}()
				var first report
				select {
				case first = <-reports:
				case <-time.After(3 * time.Second):
					t.Fatal("handover did not refresh the unchanged global viewer set")
				}
				unblock()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("handover did not finish")
				}
				got := []report{first}
				for len(reports) > 0 {
					got = append(got, <-reports)
				}
				if len(got) != 2 {
					t.Fatalf("one handover sent %d viewer reports, want one per manager", len(got))
				}
				seen := map[string]bool{}
				for _, r := range got {
					if seen[r.manager] {
						t.Errorf("duplicate viewer report to %s", r.manager)
					}
					seen[r.manager] = true
					if r.assigned != to || r.cached != to || r.online || r.connected {
						t.Errorf("viewer report preceded the completed handover: %+v, want owner %s and offline", r, to)
					}
					var want []assistantwire.SessionRef
					if r.manager == to {
						want = view
					}
					if !reflect.DeepEqual(r.viewing, want) {
						t.Errorf("%s viewer scope = %+v, want %+v", r.manager, r.viewing, want)
					}
				}
				for range 2 {
					select {
					case <-notifications:
					case <-time.After(3 * time.Second):
						t.Fatal("handover's downstream manager notification did not finish")
					}
				}
				synctest.Wait()
			})
			if got := updated.Load(); got != 2 {
				t.Fatalf("fixture returned before downstream manager notifications finished: got %d, want 2", got)
			}
		})
	}
}
