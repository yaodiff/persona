package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Helpers for the tests that drive the assistant through the hub, the way the apps and nodes do.

// shown is what the hub shows of an assistant conversation: the assistant's last snapshot.
func shown(h *Hub, id string) assistant.SessionView {
	settleAssistant(h) // what the hub told it so far is in
	a := &h.assistant
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.sessions[id]
}

// assistantSetting reads one of the assistant's settings as its store has it.
func assistantSetting(t *testing.T, h *Hub, key string, out any) {
	t.Helper()
	if _, err := assistantStoreOf(t, h).Setting(context.Background(), key, out); err != nil {
		t.Fatal(err)
	}
}

// titleBy is who named a conversation, as the assistant saved it.
func titleBy(t *testing.T, h *Hub, id string) string {
	t.Helper()
	var m map[string]struct {
		By string `json:"by"`
	}
	assistantSetting(t, h, "assistant.titles", &m)
	return m[id].By
}

// hopOf is how many relayed messages away from the user's own the conversation's work is, as the assistant saved it.
func hopOf(t *testing.T, h *Hub, id string) int {
	t.Helper()
	var st struct {
		Hops map[string]struct {
			Hop int       `json:"hop"`
			At  time.Time `json:"at"`
		} `json:"hops"`
	}
	assistantSetting(t, h, "assistant.relay", &st)
	if hp, ok := st.Hops[id]; ok && time.Since(hp.At) <= time.Hour {
		return hp.Hop
	}
	return 0
}

// archive archives (on) or brings back a conversation the way the app does; the status it got.
func archive(t *testing.T, h *Hub, id string, on bool) int {
	t.Helper()
	op := "unarchive"
	if on {
		op = "archive"
	}
	mux := http.NewServeMux()
	h.assistantRoutesRelayed(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/assistant/conversations/"+id+"/"+op, nil))
	return rec.Code
}

// The hub's facts go to the assistant as events (over the bus); each helper returns once the assistant took it.

// noteStarted is a conversation the assistant started at at (assistant.start), as it keeps it until its first turn
// ends: it reads it when it starts again.
func noteStarted(t *testing.T, h *Hub, machine, id string, at time.Time) {
	t.Helper()
	m := map[string]any{}
	assistantSetting(t, h, "assistant.titles", &m)
	m[id] = map[string]any{"machine_id": machine, "by": "pending", "started": at.UTC()}
	if err := assistantStoreOf(t, h).PutSetting(context.Background(), "assistant.titles", m); err != nil {
		t.Fatal(err)
	}
	restartAssistant(t, h)
}

// turnEnded is a turn's end as the assistant takes it, waited for: the event's handler runs on its own there (it asks
// a model, waits for a task's agents), so the test runs it itself.
func turnEnded(t *testing.T, h *Hub, machine, id string) {
	t.Helper()
	assistantOf(t, h).svc.TurnEnded(context.Background(), assistant.SessionRef{MachineID: machine, SessionID: id})
}

// userRenamed is the user renaming a conversation in the app (assistant.rename, as renameSession asks).
func userRenamed(t *testing.T, h *Hub, machine, id, title string) {
	t.Helper()
	if _, err := h.asst().Rename(context.Background(), machine, id, map[string]any{"title": title}, ""); err != nil {
		t.Fatal(err)
	}
}

func userSpoke(h *Hub, machine, id string) {
	h.asst().UserSpoke(context.Background(), assistant.SessionRef{MachineID: machine, SessionID: id})
	settleAssistant(h)
}

// The hub's end of the bus: a stale snapshot is refused, a newer one replaces the old; nothing is pushed for it (the
// session lists are the manager's to tell).
func TestAssistantSnapshotIsKeptAndShown(t *testing.T) {
	h, n := conversationsHub(t)
	link := assistantLink{h}
	ctx := context.Background()
	c := appClient(h)
	pushes := func() int {
		got := 0
		for {
			select {
			case <-c.out:
				got++
			default:
				return got
			}
		}
	}
	snap := assistant.Snapshot{Epoch: "e", Seq: 10, Sessions: []assistant.SessionView{{MachineID: "server", SessionID: "trip-0002", Title: "出游"}}}
	if _, err := link.Snapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	// The lists it decorates with it are the manager's (assistant/fleet.go): the hub keeps the snapshot alone.
	if shown(h, "trip-0002").Title != "出游" {
		t.Fatalf("kept: %+v", shown(h, "trip-0002"))
	}
	_ = n
	if _, err := link.Snapshot(ctx, assistant.Snapshot{Epoch: "e", Seq: 9}); err == nil || shown(h, "trip-0002").Title != "出游" {
		t.Fatalf("an older one is refused: %v", err)
	}
	snap.Seq = 11
	if _, err := link.Snapshot(ctx, snap); err != nil || pushes() != 0 {
		t.Fatalf("nothing changed, nothing pushed: %v", err)
	}
}

// The hub answers the assistant's device.call from the machine (its node's methods as they are) or from what it
// holds about it, and its hello with hub.json's defaults and the machines.
func TestAssistantLinkDeviceCall(t *testing.T) {
	h, n := conversationsHub(t)
	link := assistantLink{h}
	ctx := context.Background()
	hello, err := link.Hello(ctx)
	if err != nil || hello.Assistant.MachineID != "server" || !slices.Equal(hello.Assistant.Machines, []string{"server"}) || hello.Assistant.Standby {
		t.Fatalf("hello: %+v %v", hello, err)
	}
	raw, err := link.DeviceCall(ctx, assistant.DeviceCall{Device: "server", Method: assistant.DeviceSessionsList})
	var list struct {
		Sessions []proto.Session `json:"sessions"`
	}
	if err != nil || json.Unmarshal(raw, &list) != nil || len(list.Sessions) != len(n.m.sessions) || !list.Sessions[0].Assistant {
		t.Fatalf("sessions.list is the list as the apps get it: %s %v", raw, err)
	}
	if _, err := link.DeviceCall(ctx, assistant.DeviceCall{Device: "nowhere", Method: assistant.DeviceSessionsList}); !errors.Is(err, assistant.ErrNoDevice) {
		t.Fatalf("an unknown machine: %v", err)
	}
	raw, err = link.DeviceCall(ctx, assistant.DeviceCall{Device: "server", Method: assistant.DeviceSessionRename,
		Params: json.RawMessage(`{"session_id":"trip-0002","title":"x"}`)})
	if err != nil || string(raw) != `{"ok":true}` || n.lastSend(t)["rename"] == nil {
		t.Fatalf("a node method goes to the node: %s %v", raw, err)
	}
}

// seedTasks gives the assistant its scheduled tasks as if it had saved them. It reads them on first use, so this is
// for a hub whose assistant has not looked at its schedule yet; a hub without a database gets one in memory.
func seedTasks(t *testing.T, h *Hub, tasks ...assistant.Task) {
	t.Helper()
	if h.store == nil {
		h.store = &settingsStore{vals: map[string][]byte{}}
	}
	if err := assistantStoreOf(t, h).PutSetting(context.Background(), "assistant.tasks", tasks); err != nil {
		t.Fatal(err)
	}
}

// runNow is "Run now" in the app (POST …/tasks/{id}/run): the conversation the run went into.
func runNow(t *testing.T, h *Hub, id string) (proto.Session, error) {
	t.Helper()
	mux := http.NewServeMux()
	h.assistantRoutesRelayed(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/assistant/tasks/"+id+"/run", nil))
	if rec.Code != http.StatusOK {
		return proto.Session{}, fmt.Errorf("%d %s", rec.Code, rec.Body)
	}
	var out struct {
		MachineID string `json:"machine_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		return proto.Session{}, err
	}
	return proto.Session{MachineID: out.MachineID, SessionID: out.SessionID}, nil
}

// testDB is the test Postgres with the assistant's schedule emptied (skipped without one).
func testDB(t *testing.T) *db.DB {
	t.Helper()
	url := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set (scripts/test/test-server.sh runs this on the hub machine)")
	}
	database, err := db.Open(context.Background(), url, db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	for _, k := range []string{"assistant.tasks", "assistant.routines", "assistant.followups", "assistant.runs"} {
		if err := database.PutSetting(context.Background(), k, nil); err != nil {
			t.Fatal(err)
		}
	}
	return database
}

// The hub answers the assistant's schedule calls with its own functions: the machines; a push like an agent's, a live
// update as it is; the profile its snapshot reports.
func TestAssistantLinkScheduleCalls(t *testing.T) {
	h, n := conversationsHub(t)
	link := assistantLink{h}
	ctx := context.Background()
	raw, err := link.DeviceCall(ctx, assistant.DeviceCall{Device: assistant.DeviceAll, Method: assistant.DeviceDevicesList})
	if err != nil || string(raw) != `{"apps":[],"complete":false,"devices":[{"id":"server","name":"server","kind":"local","home":"/home/me","online":false,"reachable":true}],"others":[],"phone_online":false}` {
		t.Fatalf("devices.list: %s %v", raw, err)
	}
	// (What the model configuration asks of the extensions, plugins.*, is the manager's: extmgr answers it in its process.)
	// (The knowledge base's sources, connectors.archives…, are the manager's: extmgr's kb_sources_test.go.)
	// One of the hub's features starts a conversation: the assistant prepares its directory and starts it on the
	// purpose's model.
	s, err := h.asst().Start(ctx, assistant.StartParams{Text: "hi", PermissionMode: "plan", Purpose: "assistant"})
	if err != nil || s.SessionID != "s1" || s.MachineID != "server" || n.lastSend(t)["permission_mode"] != "plan" || n.lastSend(t)["launcher"] != "claude" {
		t.Fatalf("assistant.start prepares and starts the conversation with the purpose's model: %+v %v %v", s, err, n.lastSend(t))
	}

	c := appClient(h)
	link.Broadcast(ctx, assistant.Broadcast{Message: json.RawMessage(`{"type":"assistant_feed","item":{"id":"x","at":"2026-11-04T00:00:00Z"}}`)})
	select {
	case b := <-c.out:
		if !jsonEqual(withoutSeq(b), withoutSeq([]byte(`{"type":"assistant_feed","item":{"id":"x","at":"2026-11-04T00:00:00Z"}}`))) {
			t.Fatalf("an event goes to the apps as it is: %s", b)
		}
	default:
		t.Fatal("no event")
	}

	snap := assistant.Snapshot{Epoch: "e", Seq: 1, Tasks: []assistant.TaskView{{ID: "r", Label: "早报", RunMachineID: "server", RunSessionID: "trip-0002"}},
		Profile: &assistant.Profile{MachineID: "server", Main: "main-0001", Keep: []assistant.SessionRef{{MachineID: "server", SessionID: "main-0001"},
			{MachineID: "server", SessionID: "trip-0002"}}}}
	if _, err := link.Snapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	// The node is told what it keeps by the manager that connects it (internal/devices TestSetKeepTellsTheNodes).
	if keep := h.assistantProfile().Keep; len(keep) != 2 || keep[0].SessionID != "main-0001" || keep[1].SessionID != "trip-0002" {
		t.Fatalf("the profile keeps the main conversation and the tasks' own: %v", keep)
	}
}

func TestAssistantLinkTaskInstructionsCapability(t *testing.T) {
	h, n := conversationsHub(t)
	for _, tc := range []struct {
		protocol int
		cap      bool
		want     bool
	}{{0, true, false}, {1, false, false}, {1, true, true}} {
		n.m.mu.Lock()
		n.m.info.Protocol = tc.protocol
		n.m.info.Capabilities = nil
		if tc.cap {
			n.m.info.Capabilities = []string{"task_instructions_v1"}
		}
		n.m.mu.Unlock()
		raw, err := (assistantLink{h}).DeviceCall(t.Context(), assistant.DeviceCall{Device: assistant.DeviceAll, Method: assistant.DeviceDevicesList})
		var out struct {
			Devices []assistant.Device `json:"devices"`
		}
		if err != nil || json.Unmarshal(raw, &out) != nil || len(out.Devices) != 1 || out.Devices[0].TaskInstructions != tc.want {
			t.Fatalf("capability=%s %v", raw, err)
		}
	}
}

func TestAssistantLinkScratchWorkspaceCapability(t *testing.T) {
	h, n := conversationsHub(t)
	for _, tc := range []struct {
		protocol int
		cap      bool
		want     bool
	}{{0, true, false}, {1, false, false}, {1, true, true}} {
		n.m.mu.Lock()
		n.m.info.Protocol = tc.protocol
		n.m.info.Capabilities = nil
		if tc.cap {
			n.m.info.Capabilities = []string{"scratch_workspace_v1"}
		}
		n.m.mu.Unlock()
		raw, err := (assistantLink{h}).DeviceCall(t.Context(), assistant.DeviceCall{Device: assistant.DeviceAll, Method: assistant.DeviceDevicesList})
		var out struct {
			Devices []assistant.Device `json:"devices"`
		}
		if err != nil || json.Unmarshal(raw, &out) != nil || len(out.Devices) != 1 || out.Devices[0].ScratchWorkspace != tc.want {
			t.Fatalf("scratch capability=%s %v", raw, err)
		}
	}
}
