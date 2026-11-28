package hub

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/db/dbtest"
	"github.com/nwverse/persona/server/internal/proto"
)

// hubAgentDirs keeps hidden Agent folders for the assistant the tests connect, as the devices do: per machine, the
// assistant told after each change.
type hubAgentDirs struct {
	mu      sync.Mutex
	rules   map[string][]proto.HiddenAgentDir
	changed func(machine string)
}

func (d *hubAgentDirs) HiddenAgentDirs(_ context.Context, id string) ([]proto.HiddenAgentDir, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.rules[id]), "", nil
}

func (d *hubAgentDirs) EditHiddenAgentDirs(_ context.Context, id string, edit func([]proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error)) ([]proto.HiddenAgentDir, error) {
	d.mu.Lock()
	next, err := edit(slices.Clone(d.rules[id]))
	if err == nil {
		d.rules[id] = slices.Clone(next)
	}
	changed := d.changed
	d.mu.Unlock()
	if err == nil && changed != nil {
		changed(id)
	}
	return next, err
}

// testAgentDirs are the hidden folders the tests' assistants read, by hub (dialAssistant passes them on).
var testAgentDirs sync.Map

func testAgentDirsOf(h *Hub) assistant.AgentDirs {
	if d, ok := testAgentDirs.Load(h); ok {
		return d.(*hubAgentDirs)
	}
	return nil
}

// withAgentDirs gives the assistant connected to h from now on hidden Agent folders the test changes.
func withAgentDirs(t *testing.T, h *Hub) *hubAgentDirs {
	t.Helper()
	d := &hubAgentDirs{rules: map[string][]proto.HiddenAgentDir{}}
	testAgentDirs.Store(h, d)
	t.Cleanup(func() { testAgentDirs.Delete(h) })
	return d
}

// Real manager and hub connected over the bus: omission from FleetUp is an
// authoritative resolution, so hiding must never remove the pending card there.
func TestHiddenDirectoryFleetUpRetainsPendingCard(t *testing.T) {
	hiddenDirectoryCardRoundTrip(t, false)
}

func TestHiddenDirectoryUnhideRestoresPreviouslyOmittedCard(t *testing.T) {
	hiddenDirectoryCardRoundTrip(t, true)
}

func hiddenDirectoryCardRoundTrip(t *testing.T, previouslyOmitted bool) {
	t.Helper()
	base := os.Getenv(dbtest.Env)
	if base == "" {
		t.Skip(dbtest.Env + " not set")
	}
	url, cleanup, err := dbtest.Own(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	database, err := db.Open(context.Background(), url, db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "Computer"})
	registerMachine(h, m)
	m.sessions = []proto.Session{{MachineID: "box", SessionID: "blocked", Cwd: "/work/noisy", Status: proto.StatusBlocked}}
	dirs := withAgentDirs(t, h)
	a := connectAssistant(t, h, database)
	const within = 30 * time.Second
	// Tools can arrive before the startup FleetUp. Its empty snapshot must be
	// applied before we create the card or simulate an older hub omitting it.
	eventually(t, within, "initial interaction snapshot", func() bool {
		h.reported.mu.Lock()
		defer h.reported.mu.Unlock()
		return h.reported.interactions != nil
	})
	app := &client{out: make(chan []byte, 256), subs: map[[2]string]bool{}, role: roleFull}
	h.mu.Lock()
	h.clients[app] = struct{}{}
	h.mu.Unlock()
	t.Cleanup(func() { h.mu.Lock(); delete(h.clients, app); h.mu.Unlock() })
	ctx, cancel := context.WithCancel(t.Context())
	ix := proto.Interaction{ID: "ix_hidden_transport", MachineID: "box", SessionID: "blocked", Kind: proto.IxPermission, OutwardWrite: true, Summary: "Send the project update"}
	raw, _ := json.Marshal(map[string]any{"interaction": ix})
	done := make(chan struct{})
	var value any
	var handled bool
	var requestErr error
	go func() {
		defer close(done)
		value, handled, requestErr = a.svc.NodeRequest(ctx, "box", "interaction.create", raw)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(within):
			t.Error("node request did not stop")
		}
	})
	waitEvent := func(event, id string) {
		t.Helper()
		deadline := time.NewTimer(within)
		defer deadline.Stop()
		for {
			select {
			case b := <-app.out:
				var p struct {
					Type, ID    string
					Interaction proto.Interaction
				}
				_ = json.Unmarshal(b, &p)
				if p.Type == event && (p.ID == id || p.Interaction.ID == id) {
					return
				}
				if p.Type == "interaction_resolved" && p.ID == ix.ID {
					t.Fatal("hiding/reconnect resolved the blocked agent's card")
				}
			case <-done:
				t.Fatalf("node request ended waiting for %s for %s: %v", event, id, requestErr)
			case <-deadline.C:
				t.Fatalf("client did not receive %s for %s", event, id)
			}
		}
	}
	waitEvent("interaction", ix.ID)
	// Saved as the computer's settings page saves: the devices store the rules, then tell the assistant.
	save := func(rules ...proto.HiddenAgentDir) {
		t.Helper()
		if _, err := dirs.EditHiddenAgentDirs(ctx, "box", func([]proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error) { return rules, nil }); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	save(proto.HiddenAgentDir{Pattern: "/work/noisy", Enabled: true})
	// The marker travels on the same bus as FleetUp. Seeing it in app.out
	// consumes all earlier replay events before the omission/unhide assertion.
	a.svc.FleetUp(ctx)
	marker := proto.Interaction{ID: "ix_transport_barrier", MachineID: "box", SessionID: "barrier", Kind: proto.IxPermission}
	if err := a.bh.Tell(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: marker}); err != nil {
		t.Fatal(err)
	}
	waitEvent("interaction", marker.ID)
	hasCard := func(c proto.Interaction) bool { return c.ID == ix.ID }
	if !slices.ContainsFunc(h.pendingInteractions(), hasCard) {
		t.Fatal("FleetUp forgot a hidden pending card at the real hub")
	}
	if previouslyOmitted {
		// State left behind by the older visibility-filtered reconnect snapshot.
		h.takeFleetInteractionsFrom(assistantwire.AssistantID, nil)
		if len(h.pendingInteractions()) != 0 {
			t.Fatal("legacy omission fixture did not remove the cards")
		}
		waitEvent("interaction_resolved", ix.ID)
	}
	save()
	waitEvent("interaction", ix.ID)
	if !slices.ContainsFunc(h.pendingInteractions(), hasCard) {
		t.Fatal("unhide lost the actionable card")
	}
	if err := h.resolve(ix.ID, proto.Resolution{Allow: new(true)}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		r, ok := value.(proto.Resolution)
		if requestErr != nil || !handled || !ok || r.Allow == nil || !*r.Allow {
			t.Fatalf("node did not receive approval: %+v (handled %v, error %v)", value, handled, requestErr)
		}
	case <-time.After(within):
		t.Fatal("retained card could not unblock the node")
	}
}
