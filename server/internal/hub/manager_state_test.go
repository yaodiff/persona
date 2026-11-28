package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// stateManager is a manager on the bus for the per-manager state: it answers http.request, devices.call,
// assistant.tools and the source moves, and keeps the events the hub sends it.
type stateManager struct {
	id   string
	peer *rpc.Peer
	ws   interface{ CloseNow() error }

	mu      sync.Mutex
	events  []string        // "event params"
	calls   []string        // "method params" of the source moves
	sources map[string]bool // the mail accounts it has
}

func connectStateManager(t *testing.T, h *Hub, srv *httptest.Server, id string, sources ...string) *stateManager {
	t.Helper()
	f := &stateManager{id: id, sources: map[string]bool{}}
	for _, s := range sources {
		f.sources[s] = true
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ws, err := bus.Dial(ctx, srv.URL, busToken(t, h, id, id), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	f.ws = ws
	conn := bus.NewConn(ctx, ws)
	f.peer = rpc.New("s", conn)
	f.peer.OnEvent = func(event string, params json.RawMessage) {
		f.mu.Lock()
		f.events = append(f.events, event+" "+string(params))
		f.mu.Unlock()
	}
	f.peer.OnRequest = f.answer
	go func() { _ = f.peer.Serve(ctx, conn) }()
	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ccancel()
	if err := f.peer.Call(cctx, busHello, map[string]any{"id": id}, nil); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *stateManager) answer(_ context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case assistantwire.MethodHTTP:
		body, _ := json.Marshal(map[string]any{"who": f.id})
		return assistantwire.HTTPResponse{Status: http.StatusOK, Body: body}, nil
	case devicewire.MethodCall:
		return map[string]any{"who": f.id}, nil
	case assistantwire.MethodTools:
		return map[string]any{"tools": []proto.Tool{{Name: "tool_of_" + strings.ReplaceAll(f.id, "-", "_")}}}, nil
	case extwire.MethodSourceExport, extwire.MethodSourceImport, extwire.MethodSourceRelease:
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, method+" "+string(params))
		var ref extwire.SourceBundle
		_ = json.Unmarshal(params, &ref)
		switch method {
		case extwire.MethodSourceExport:
			if !f.sources[ref.Source] {
				return nil, &rpc.Error{Code: extwire.CodeNoSource, Message: "no such source"}
			}
			// A newer manager's bundle can have fields the hub has never seen.
			return struct {
				Data        json.RawMessage `json:"data"`
				FutureField string          `json:"future_field"`
				Kind        string          `json:"kind"`
				Source      string          `json:"source"`
			}{
				Data:        json.RawMessage(`{"password":"pw"}`),
				FutureField: "<&>",
				Kind:        "mail",
				Source:      ref.Source,
			}, nil
		case extwire.MethodSourceImport:
			if f.sources[ref.Source] {
				return nil, &rpc.Error{Code: extwire.CodeSourceExists, Message: "has it"}
			}
			f.sources[ref.Source] = true
			return extwire.SourceRef{Source: ref.Source}, nil
		default:
			delete(f.sources, ref.Source)
			return map[string]any{}, nil
		}
	}
	return nil, &rpc.Error{Code: "no_such_thing", Message: "the fake manager has no " + method}
}

func (f *stateManager) notify(t *testing.T, event string, params any) {
	t.Helper()
	if err := f.peer.Notify(event, params); err != nil {
		t.Fatal(err)
	}
}

func (f *stateManager) call(t *testing.T, method string, params, out any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return f.peer.Call(ctx, method, params, out)
}

// lastConfig is the machines of the last devices.config the hub sent it ("" before one came).
func (f *stateManager) lastConfig() (ids []string, got bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.events) - 1; i >= 0; i-- {
		ev, params, _ := strings.Cut(f.events[i], " ")
		if ev != devicewire.EventConfig {
			continue
		}
		var cfg struct {
			Machines []struct {
				ID string `json:"id"`
			} `json:"machines"`
		}
		_ = json.Unmarshal([]byte(params), &cfg)
		for _, m := range cfg.Machines {
			ids = append(ids, m.ID)
		}
		return ids, true
	}
	return nil, false
}

func (f *stateManager) has(source string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sources[source]
}

// Each manager's snapshot, tools, extensions' report and Live Activity are its own: a second manager's do not
// replace the assistant's; its conversations are its own on the machine moved to it, and only there.
func TestManagerStateIsKeptByManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}, {ID: "mb", Name: "mb", Kind: "local"}}
	a := connectStateManager(t, h, srv, assistantwire.AssistantID)
	b := connectStateManager(t, h, srv, "m-0b0b0b")
	if _, err := h.moveMachine(context.Background(), "mb", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	a.notify(t, devicewire.EventState, devicewire.States{Machines: []devicewire.State{{Machine: proto.Machine{ID: "ma", Name: "ma"}, Connected: true}}})
	b.notify(t, devicewire.EventState, devicewire.States{Machines: []devicewire.State{{Machine: proto.Machine{ID: "mb", Name: "mb"}, Connected: true}}})
	eventually(t, 5*time.Second, "ma and mb reported", func() bool { return h.localMachine("ma") != nil && h.localMachine("mb") != nil })

	var res assistantwire.SnapshotResult
	if err := a.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: "a", Seq: 1,
		Sessions: []assistantwire.SessionView{{MachineID: "ma", SessionID: "conv-a", Title: "A"}},
		Profile:  &assistantwire.Profile{MachineID: "ma", Main: "conv-a", Timezone: "Europe/Paris"}}, &res); err != nil || !res.OK {
		t.Fatalf("the assistant's snapshot: %v %+v", err, res)
	}
	// The second manager's conversation runs on its own machine mb; what it lists on the assistant's (conv-x) takes
	// nothing.
	if err := b.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: "b", Seq: 1,
		Sessions: []assistantwire.SessionView{{MachineID: "mb", SessionID: "conv-b", Title: "B's"}, {MachineID: "ma", SessionID: "conv-x"}},
		Profile:  &assistantwire.Profile{MachineID: "mb", Main: "conv-b", Timezone: "Asia/Tokyo"}}, &res); err != nil || !res.OK {
		t.Fatalf("the second manager's snapshot: %v %+v", err, res)
	}
	if h.conversationOwner("conv-a") != assistantwire.AssistantID || h.conversationOwner("conv-b") != "m-0b0b0b" || h.conversationOwner("conv-x") != "" {
		t.Fatalf("conversations: %q %q %q", h.conversationOwner("conv-a"), h.conversationOwner("conv-b"), h.conversationOwner("conv-x"))
	}
	// The hub's profile stays the assistant's; both main conversations are known.
	if p := h.assistantProfile(); p.Timezone != "Europe/Paris" || p.Main != "conv-a" {
		t.Fatalf("the hub's profile: %+v", p)
	}
	if !h.isMainOn("ma", "conv-a") || !h.isMainOn("mb", "conv-b") || h.isMainOn("ma", "conv-x") {
		t.Fatal("main conversations")
	}
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "ma", Sessions: []proto.Session{
		{MachineID: "ma", SessionID: "conv-a", Title: "A"}, {MachineID: "ma", SessionID: "conv-x"}, {MachineID: "ma", SessionID: "s3"}}})
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "mb", Sessions: []proto.Session{{MachineID: "mb", SessionID: "conv-b", Title: "B's"}}})
	got := map[string]proto.Session{}
	for _, m := range []string{"ma", "mb"} {
		for _, s := range h.decorated(h.localMachine(m)) {
			got[s.SessionID] = s
		}
	}
	if got["conv-a"].Manager != assistantwire.AssistantID || got["s3"].Manager != assistantwire.AssistantID || got["conv-x"].Manager != assistantwire.AssistantID {
		t.Fatalf("the assistant's: %+v", got)
	}
	if s := got["conv-b"]; s.Manager != "m-0b0b0b" {
		t.Fatalf("the second manager's conversation: %+v", s)
	}

	// Tools: each manager's own.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if tools := h.asstOf("m-0b0b0b").Tools(ctx); len(tools) != 1 || tools[0].Name != "tool_of_m_0b0b0b" {
		t.Fatalf("its tools: %+v", tools)
	}

	// The extensions' reports merge; a prefix goes to the manager that serves it.
	a.notify(t, extwire.EventReport, extwire.Report{Features: []string{"fa"}, APIs: []extwire.APIPrefix{{Prefix: "pa"}}})
	b.notify(t, extwire.EventReport, extwire.Report{Features: []string{"fb", "fa"}, APIs: []extwire.APIPrefix{{Prefix: "pb"}}})
	eventually(t, 5*time.Second, "both reports", func() bool { return len(h.extReport().Features) == 2 })
	if r := h.extReport(); r.Features[0] != "fa" || r.Features[1] != "fb" || h.extPrefixOwner("pb", "", "") != "m-0b0b0b" || h.extPrefixOwner("pa", "", "") != assistantwire.AssistantID {
		t.Fatalf("merged: %+v", r)
	}

	// The Live Activity: the default manager's.
	a.notify(t, assistantwire.EventFleetLive, assistantwire.FleetLive{Stats: FleetStats{Requests: 1}})
	b.notify(t, assistantwire.EventFleetLive, assistantwire.FleetLive{Stats: FleetStats{Requests: 2}})
	eventually(t, 5*time.Second, "the assistant's live", func() bool { _, st := h.fleetLive(); return st.Requests == 1 })
	if err := h.managerStore().PutSetting(context.Background(), managerDefaultKey, "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	h.ownersChanged()
	if _, st := h.fleetLive(); st.Requests != 2 {
		t.Fatalf("the default's live: %+v", st)
	}
	if err := h.managerStore().PutSetting(context.Background(), managerDefaultKey, assistantwire.AssistantID); err != nil {
		t.Fatal(err)
	}
	h.ownersChanged()

	// Removed: what it reported goes.
	if code, _ := ownershipGet(t, h, srv, http.MethodDelete, "/api/v1/managers/m-0b0b0b", nil); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if h.conversationOwner("conv-b") != "" || h.extPrefixOwner("pb", "", "") != "" || h.isMainOn("mb", "conv-b") {
		t.Fatal("the removed manager's state stayed")
	}
}

// A machine of hub.json moves to another manager: the hub keeps it, hands each manager its machines again, sends
// what concerns the machine to the new one; its credential goes only to it; removing that manager gives it back.
// A source moves the same way: exported by the old one, imported by the new one, released by the old one.
func TestMoveMachineAndSource(t *testing.T) {
	h, srv, store := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}, {ID: "mc", Name: "mc", Kind: "local"}}
	a := connectStateManager(t, h, srv, assistantwire.AssistantID, "mail:acct-1")
	b := connectStateManager(t, h, srv, "m-0c0c0c")
	report := func(f *stateManager, ids ...string) {
		st := devicewire.States{}
		for _, id := range ids {
			st.Machines = append(st.Machines, devicewire.State{Machine: proto.Machine{ID: id, Name: id, Online: true}, Connected: true,
				ConnectedAt: time.Now().UTC()})
		}
		f.notify(t, devicewire.EventState, st)
	}
	report(a, "ma", "mc")
	eventually(t, 5*time.Second, "reported", func() bool { return h.localMachine("ma") != nil && h.localMachine("mc") != nil })

	code, out := ownershipGet(t, h, srv, http.MethodPost, "/api/v1/managers/m-0c0c0c/machines", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("no machine_id: %d %v", code, out)
	}
	post := func(path, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, out := post("/api/v1/managers/m-0c0c0c/machines", `{"machine_id":"nope"}`); code != http.StatusBadRequest || out["code"] != "not_movable" {
		t.Fatalf("unknown machine: %d %v", code, out)
	}
	if code, out := post("/api/v1/managers/m-ffffff/machines", `{"machine_id":"ma"}`); code != http.StatusNotFound {
		t.Fatalf("unknown manager: %d %v", code, out)
	}
	if code, out := post("/api/v1/managers/m-0c0c0c/machines", `{"machine_id":"ma"}`); code != http.StatusOK || out["from"] != assistantwire.AssistantID || out["manager"] != "m-0c0c0c" {
		t.Fatalf("move: %d %v", code, out)
	}
	if h.machineOwner("ma") != "m-0c0c0c" || h.machineOwner("mc") != assistantwire.AssistantID {
		t.Fatalf("owners: %s %s", h.machineOwner("ma"), h.machineOwner("mc"))
	}
	var kept map[string]string
	if ok, err := store.Setting(context.Background(), machineOwnersKey, &kept); err != nil || !ok || kept["ma"] != "m-0c0c0c" {
		t.Fatalf("kept: %v %v %v", kept, ok, err)
	}
	eventually(t, 5*time.Second, "each manager handed its machines", func() bool {
		ai, aok := a.lastConfig()
		bi, bok := b.lastConfig()
		return aok && bok && strings.Join(ai, ",") == "mc" && strings.Join(bi, ",") == "ma"
	})
	// The old one's report still has it (it is letting go): not taken; the new one's is.
	report(a, "ma", "mc")
	time.Sleep(100 * time.Millisecond)
	if h.machineOwner("ma") != "m-0c0c0c" {
		t.Fatal("the old manager's report took it back")
	}
	report(b, "ma")
	eventually(t, 5*time.Second, "ma online at its new manager", func() bool {
		for _, m := range h.machineList() {
			if m.ID == "ma" {
				return m.Online && m.Manager == "m-0c0c0c"
			}
		}
		return false
	})
	var who map[string]any
	if err := h.localMachine("ma").call(context.Background(), "panes.list", map[string]any{}, &who); err != nil || who["who"] != "m-0c0c0c" {
		t.Fatalf("node call: %v %v", who, err)
	}
	if code, out := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/machines/ma/herdr", nil); code != http.StatusOK || out["who"] != "m-0c0c0c" {
		t.Fatalf("its route: %d %v", code, out)
	}
	// Its credential: only to its manager.
	if err := a.call(t, devicewire.MethodConnectorCred, devicewire.ConnectorCred{MachineID: "ma"}, nil); rpcCode(err) != devicewire.CodeNoDevice {
		t.Fatalf("the old manager asked for its credential: %v", err)
	}

	// A source: from the assistant (the default) to the second manager.
	if code, out := post("/api/v1/managers/m-0c0c0c/sources", `{"source":"mail:acct-9"}`); code != http.StatusNotFound || out["code"] != extwire.CodeNoSource {
		t.Fatalf("unknown source: %d %v", code, out)
	}
	if code, out := post("/api/v1/managers/m-0c0c0c/sources", `{"source":"mail:acct-1"}`); code != http.StatusOK || out["from"] != assistantwire.AssistantID {
		t.Fatalf("move the source: %d %v", code, out)
	}
	if a.has("mail:acct-1") || !b.has("mail:acct-1") {
		t.Fatal("the source did not move")
	}
	const bundleJSON = `{"data":{"password":"pw"},"future_field":"<&>","kind":"mail","source":"mail:acct-1"}`
	b.mu.Lock()
	imported := slices.Contains(b.calls, extwire.MethodSourceImport+" "+bundleJSON)
	b.mu.Unlock()
	if !imported {
		t.Fatal("the new manager did not receive the original bundle JSON")
	}
	if code, out := post("/api/v1/managers/m-0c0c0c/sources", `{"source":"mail:acct-1","from":"m-0c0c0c"}`); code != http.StatusOK {
		t.Fatalf("to where it is: %d %v", code, out)
	}

	// Removing the manager gives its machines back to the carrier.
	if code, _ := ownershipGet(t, h, srv, http.MethodDelete, "/api/v1/managers/m-0c0c0c", nil); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if h.machineOwner("ma") != assistantwire.AssistantID {
		t.Fatalf("after removal: %s", h.machineOwner("ma"))
	}
	eventually(t, 5*time.Second, "the carrier has both again", func() bool {
		ai, _ := a.lastConfig()
		return strings.Join(ai, ",") == "ma,mc"
	})
	// The source with an offline manager: 503.
	if code, out := post("/api/v1/managers/assistant/sources", `{"source":"mail:acct-1","from":"m-0c0c0c"}`); code != http.StatusNotFound {
		t.Fatalf("from a removed manager: %d %v", code, out)
	}
}
