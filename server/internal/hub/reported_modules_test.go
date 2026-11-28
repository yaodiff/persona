package hub

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The flags of modules approved installed extensions supply (#16254, Report.Modules): the hub takes them from the
// default manager's report only, while that manager is connected and its hello carries the modules marker.

// helloManager makes a connection of manager id current, its hello announcing features.
func helloManager(t *testing.T, h *Hub, id string, features ...string) *busConn {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"id": id, "features": features})
	if err != nil {
		t.Fatal(err)
	}
	var p busPresence
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	c := &busConn{ident: db.BusIdentity{ID: id, Role: bus.RoleManager}, presence: p, peer: rpc.New("h", io.Discard)}
	h.bus.mu.Lock()
	if h.bus.current == nil {
		h.bus.current = map[string]*busConn{}
	}
	h.bus.current[id] = c
	h.bus.mu.Unlock()
	return c
}

// managerGone drops manager id's current connection.
func managerGone(h *Hub, id string) {
	h.bus.mu.Lock()
	delete(h.bus.current, id)
	h.bus.mu.Unlock()
}

// reportFrom hands the hub c's extensions.report, as the bus does.
func reportFrom(t *testing.T, h *Hub, c *busConn, r extwire.Report) {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	busEvents[extwire.EventReport](h, c, raw)
}

func volcReport(on bool) extwire.Report {
	return extwire.Report{Features: []string{"example_feed"}, Modules: []extwire.ModuleProvider{{ID: "volc", On: on}}}
}

// wantFeatures fails unless the hub's features carry each of has and none of hasNot.
func wantFeatures(t *testing.T, step string, got []string, has, hasNot []string) {
	t.Helper()
	for _, f := range has {
		if !slices.Contains(got, f) {
			t.Errorf("%s: %q missing from %v", step, f, got)
		}
	}
	for _, f := range hasNot {
		if slices.Contains(got, f) {
			t.Errorf("%s: %q in %v", step, f, got)
		}
	}
	seen := map[string]bool{}
	for _, f := range got {
		if seen[f] {
			t.Errorf("%s: %q listed twice", step, f)
		}
		seen[f] = true
	}
}

// Without the modules marker in the default manager's hello (an older manager) the report's modules are ignored, as
// are another manager's and an id the host does not list; with it, an approved provider adds its flag, and one that
// is off removes the built-in module's flag of that id (the installed provider decides) but no other module's.
func TestReportedModulesNeedMarker(t *testing.T) {
	h := newTestHub(t)
	t.Cleanup(func() { waitHubWorkers(t, h) })
	a := helloManager(t, h, assistantwire.AssistantID) // an older manager: no marker, no module flags
	reportFrom(t, h, a, volcReport(true))
	wantFeatures(t, "no marker", h.features(), []string{"example_feed"}, []string{"modules", "module.volc"})

	helloManager(t, h, assistantwire.AssistantID, "modules", "module.briefing")
	wantFeatures(t, "marker", h.features(), []string{"modules", "module.briefing", "module.volc"}, nil)

	// The built-in volc announced in the hello gives way to an installed provider that is off.
	a = helloManager(t, h, assistantwire.AssistantID, "modules", "module.briefing", "module.volc")
	wantFeatures(t, "built-in and installed on", h.features(), []string{"module.volc", "module.briefing"}, nil)
	reportFrom(t, h, a, volcReport(false))
	wantFeatures(t, "installed off", h.features(), []string{"modules", "module.briefing"}, []string{"module.volc"})

	// An older manager's report has no modules: the hello's built-in flags stand as they are.
	reportFrom(t, h, a, extwire.Report{Features: []string{"example_feed"}})
	wantFeatures(t, "report without modules", h.features(), []string{"module.volc", "module.briefing"}, nil)

	// Only listed ids count, and only the default manager's report.
	reportFrom(t, h, a, extwire.Report{Modules: []extwire.ModuleProvider{{ID: "volc2", On: true}, {ID: "briefing", On: false}}})
	wantFeatures(t, "unlisted ids", h.features(), []string{"module.volc", "module.briefing"}, []string{"module.volc2"})
	other := helloManager(t, h, "m-other", "modules")
	reportFrom(t, h, other, volcReport(false))
	wantFeatures(t, "another manager's report", h.features(), []string{"module.volc"}, nil)
	a = helloManager(t, h, assistantwire.AssistantID, "modules")
	reportFrom(t, h, a, extwire.Report{})
	reportFrom(t, h, other, volcReport(true))
	wantFeatures(t, "another manager's provider", h.features(), []string{"modules"}, []string{"module.volc"})
}

// The default manager going offline takes its providers' flags with it, as it takes its built-in modules' flags.
func TestReportedModulesGoneWhileManagerOffline(t *testing.T) {
	h := newTestHub(t)
	t.Cleanup(func() { waitHubWorkers(t, h) })
	a := helloManager(t, h, assistantwire.AssistantID, "modules")
	reportFrom(t, h, a, volcReport(true))
	wantFeatures(t, "online", h.features(), []string{"modules", "module.volc"}, nil)
	managerGone(h, assistantwire.AssistantID)
	wantFeatures(t, "offline", h.features(), nil, []string{"modules", "module.volc"})
	if r := h.extReportOf(assistantwire.AssistantID); r == nil || len(r.Modules) != 1 {
		t.Fatalf("the report was not kept while the manager is away: %+v", r)
	}
}

// A manager that reconnects has its providers' flags from the report the hub kept until it reports again (BusUp
// sends a new one): the first features after the hello are not missing them, and the new report then decides.
func TestReportedModulesSurviveManagerReconnect(t *testing.T) {
	h := newTestHub(t)
	t.Cleanup(func() { waitHubWorkers(t, h) })
	a := helloManager(t, h, assistantwire.AssistantID, "modules")
	reportFrom(t, h, a, volcReport(true))
	managerGone(h, assistantwire.AssistantID)
	wantFeatures(t, "away", h.features(), nil, []string{"module.volc"})
	a = helloManager(t, h, assistantwire.AssistantID, "modules")
	wantFeatures(t, "back, before its report", h.features(), []string{"modules", "module.volc"}, nil)
	reportFrom(t, h, a, volcReport(false))
	wantFeatures(t, "back, provider off", h.features(), []string{"modules"}, []string{"module.volc"})
	reportFrom(t, h, a, volcReport(true))
	wantFeatures(t, "back, provider on", h.features(), []string{"modules", "module.volc"}, nil)
}

// A restarted hub reads the assistant's last report back (loadExtReport): the providers' flags are there from the
// manager's first hello, before its next report.
func TestReportedModulesSurviveHubRestart(t *testing.T) {
	ctx := context.Background()
	store := newMeshStore()
	h := newTestHub(t)
	h.store = store
	a := helloManager(t, h, assistantwire.AssistantID, "modules")
	reportFrom(t, h, a, volcReport(true))
	waitFor(t, "the report with its modules to be saved", func() bool {
		var got extwire.Report
		ok, err := store.Setting(ctx, extReportKey, &got)
		return err == nil && ok && len(got.Modules) == 1 && got.Modules[0] == extwire.ModuleProvider{ID: "volc", On: true}
	})
	waitHubWorkers(t, h)

	restarted := newTestHub(t)
	restarted.store = store
	t.Cleanup(func() { waitHubWorkers(t, restarted) })
	wantFeatures(t, "restarted, no manager yet", restarted.features(), nil, []string{"modules", "module.volc"})
	restarted.loadExtReport(ctx)
	wantFeatures(t, "restarted, loaded, no manager yet", restarted.features(), nil, []string{"module.volc"})
	helloManager(t, restarted, assistantwire.AssistantID, "modules")
	wantFeatures(t, "restarted, manager's hello", restarted.features(), []string{"modules", "module.volc"}, nil)
}

// A node's configuration carries the same flags as the apps' hello (hub_features), the providers' included, read when
// the node connects.
func TestNodeHubFeaturesCarryReportedModules(t *testing.T) {
	h := newTestHub(t)
	t.Cleanup(func() { waitHubWorkers(t, h) })
	m := newMachine(h, MachineConfig{ID: "computer"})
	nodeFeatures := func() []string {
		t.Helper()
		reply := h.machineConnected(context.Background(), m, time.Now())
		var features []string
		if err := json.Unmarshal(reply.Configure["hub_features"], &features); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(features, h.features()) {
			t.Fatalf("node features %v differ from the hello's %v", features, h.features())
		}
		return features
	}
	a := helloManager(t, h, assistantwire.AssistantID, "modules", "module.volc")
	reportFrom(t, h, a, volcReport(true))
	wantFeatures(t, "provider on", nodeFeatures(), []string{"modules", "module.volc"}, nil)
	reportFrom(t, h, a, volcReport(false))
	wantFeatures(t, "provider off", nodeFeatures(), []string{"modules"}, []string{"module.volc"})
}
