package hub

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
)

func TestTaskViewerReportsRetryAfterAllManagersFail(t *testing.T) {
	for _, forced := range []bool{false, true} {
		name := "ordinary retry"
		if forced {
			name = "forced ownership refresh"
		}
		t.Run(name, func(t *testing.T) {
			h := newTestHub(t)
			owners := []string{"manager-a", "manager-b"}
			h.devices.kept = map[string]string{"box": owners[0]}
			view := []assistantwire.SessionRef{{MachineID: "box", SessionID: "agent"}}
			addTestClient(h, &client{role: roleFull, subs: map[[2]string]bool{{"box", "agent"}: true}})
			failing := true
			attempts := 0
			reports := map[string]assistantwire.Clients{}
			for _, owner := range owners {
				c := rosterConn(h, owner, writerFunc(func(raw []byte) (int, error) {
					var event struct {
						Event  string
						Params assistantwire.Clients
					}
					if err := json.Unmarshal(raw, &event); err != nil {
						return 0, err
					}
					if event.Event == assistantwire.EventClients {
						attempts++
						if failing {
							return 0, errors.New("manager connection unavailable")
						}
						reports[owner] = event.Params
					}
					return len(raw), nil
				}))
				c.presence = busPresence{ID: owner, Role: bus.RoleManager}
			}
			h.fleetClientsChanged()
			if attempts != len(owners) || len(h.reported.clientsSent) != 0 || !reflect.DeepEqual(h.reported.clients.Viewing, view) {
				t.Fatalf("all failed sends were cached as delivered: attempts=%d sent=%v snapshot=%+v",
					attempts, h.reported.clientsSent, h.reported.clients)
			}
			failing = false
			if forced {
				h.devices.kept["box"] = owners[1]
				h.fleetClientsRefresh()
			} else {
				h.fleetClientsChanged()
			}
			current := owners[0]
			if forced {
				current = owners[1]
			}
			if attempts != 2*len(owners) || len(reports) != len(owners) || len(h.reported.clientsSent) != len(owners) {
				t.Fatalf("the unchanged global snapshot was not retried: attempts=%d reports=%+v", attempts, reports)
			}
			for _, owner := range owners {
				got := reports[owner].Viewing
				if owner == current && !reflect.DeepEqual(got, view) || owner != current && len(got) != 0 {
					t.Errorf("retry used stale ownership for %s: %+v", owner, got)
				}
			}
			if forced {
				// Once delivery succeeded, an ownership-only move still needs a forced report of the same global set.
				h.devices.kept["box"] = owners[0]
				h.fleetClientsRefresh()
				if attempts != 3*len(owners) || !reflect.DeepEqual(reports[owners[0]].Viewing, view) ||
					len(reports[owners[1]].Viewing) != 0 {
					t.Fatalf("forced refresh was suppressed by the last successful snapshot: attempts=%d reports=%+v", attempts, reports)
				}
			}
		})
	}
}

func TestTaskViewerReportsRetryOnlyTheFailedManager(t *testing.T) {
	for _, failed := range []string{"manager-a", "manager-b"} {
		for _, leaving := range []bool{false, true} {
			name := failed + "/subscribe"
			if leaving {
				name = failed + "/unsubscribe"
			}
			t.Run(name, func(t *testing.T) {
				h := newTestHub(t)
				h.devices.kept = map[string]string{"box": failed}
				page := &client{role: roleFull, subs: map[[2]string]bool{}}
				if leaving {
					page.subs[[2]string{"box", "agent"}] = true
				}
				addTestClient(h, page)
				attempts := map[string]int{}
				reports := map[string]assistantwire.Clients{}
				failing := false
				for _, owner := range []string{"manager-a", "manager-b"} {
					c := rosterConn(h, owner, writerFunc(func(raw []byte) (int, error) {
						var event struct {
							Event  string
							Params assistantwire.Clients
						}
						if err := json.Unmarshal(raw, &event); err != nil {
							return 0, err
						}
						if event.Event == assistantwire.EventClients {
							attempts[owner]++
							if failing && owner == failed {
								return 0, errors.New("manager connection unavailable")
							}
							reports[owner] = event.Params
						}
						return len(raw), nil
					}))
					c.presence = busPresence{ID: owner, Role: bus.RoleManager}
				}
				h.fleetClientsChanged()
				failing = true
				page.mu.Lock()
				if leaving {
					delete(page.subs, [2]string{"box", "agent"})
				} else {
					page.subs[[2]string{"box", "agent"}] = true
				}
				page.mu.Unlock()
				h.fleetClientsChanged()
				h.fleetClientsChanged()
				for owner, count := range attempts {
					want := 2
					if owner == failed {
						want = 3
					}
					if count != want {
						t.Fatalf("partial delivery was not tracked per manager: %s attempts=%d want=%d", owner, count, want)
					}
				}
				failing = false
				h.fleetClientsChanged()
				if attempts[failed] != 4 {
					t.Fatalf("unchanged report did not retry the failed manager: %+v", attempts)
				}
				view := []assistantwire.SessionRef{{MachineID: "box", SessionID: "agent"}}
				if leaving && len(reports[failed].Viewing) != 0 ||
					!leaving && !reflect.DeepEqual(reports[failed].Viewing, view) {
					t.Fatalf("retry did not repair the manager's viewer set: %+v", reports[failed])
				}
				h.fleetClientsChanged()
				for owner, count := range attempts {
					want := 2
					if owner == failed {
						want = 4
					}
					if count != want {
						t.Fatalf("delivered report was sent again: %s attempts=%d want=%d", owner, count, want)
					}
				}
			})
		}
	}
}

func TestTaskViewerReportReachesReconnectedManager(t *testing.T) {
	h := newTestHub(t)
	h.devices.kept = map[string]string{"box": "manager"}
	addTestClient(h, &client{role: roleFull, subs: map[[2]string]bool{{"box", "agent"}: true}})
	reports := 0
	write := writerFunc(func(raw []byte) (int, error) {
		var event struct{ Event string }
		if err := json.Unmarshal(raw, &event); err != nil {
			return 0, err
		}
		if event.Event == assistantwire.EventClients {
			reports++
		}
		return len(raw), nil
	})
	first := rosterConn(h, "manager", write)
	first.presence = busPresence{ID: "manager", Role: bus.RoleManager}
	h.fleetClientsChanged()
	second := rosterConn(h, "manager", write)
	second.presence = first.presence
	h.fleetClientsChanged()
	if reports != 2 || h.reported.clientsSent["manager"] != second {
		t.Fatalf("a reconnected manager inherited its old connection's delivery: reports=%d", reports)
	}
	h.fleetClientsChanged()
	if reports != 2 {
		t.Fatalf("the new connection's successful report was not retained: reports=%d", reports)
	}
}
