package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func orgRegressionEnv(t *testing.T, store *memStore) *todoEventEnv {
	t.Helper()
	e := directionEnv(t, store)
	setSettings(t, e.s, func(v *Settings) { v.Main, v.Timezone = "conv_main", "UTC" })
	e.title("conv_lead_a", "Direction A")
	e.title("conv_lead_b", "Direction B")
	e.leaders("conv_lead_a", "conv_lead_b")
	e.syncLeaders()
	return e
}

func orgPutTask(e *todoEventEnv, id, supervisor string, agents ...TodoAgent) {
	putTodo(e.s, Todo{ID: id, Title: id, Supervision: &Supervision{Supervisor: supervisor},
		Agents: agents, CreatedAt: time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)})
}

func orgWorkingSession(machine, id string) proto.Session {
	return proto.Session{MachineID: machine, SessionID: id, Title: id + " display", Agent: "claude",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)}
}

func orgSnapshotNow(t *testing.T, e *todoEventEnv) orgSnapshot {
	t.Helper()
	snapshot, err := e.s.organizationSnapshot(context.Background(), time.Date(2026, 11, 24, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func orgNodeCount(snapshot orgSnapshot, id string) int {
	count := 0
	for _, node := range snapshot.Nodes {
		if node.ID == id {
			count++
		}
	}
	return count
}

func TestOrganizationSnapshotParentDeduplicationAndConflicts(t *testing.T) {
	e := orgRegressionEnv(t, &memStore{})
	orgPutTask(e, "todo_same", "conv_lead_a",
		TodoAgent{MachineID: "mac", SessionID: "parent", Title: "Parent"},
		TodoAgent{MachineID: "mac", SessionID: "child", Title: "Child", Source: sourceSpawned, Via: "mac/parent"})
	orgPutTask(e, "todo_conflict", "conv_lead_b",
		TodoAgent{MachineID: "mac", SessionID: "parent-a", Title: "Parent A"},
		TodoAgent{MachineID: "mac", SessionID: "parent-b", Title: "Parent B"},
		TodoAgent{MachineID: "mac", SessionID: "child-conflict", Title: "Conflicting child", Source: sourceSpawned, Via: "mac/parent-a"})
	orgPutTask(e, "todo_missing", "conv_main",
		TodoAgent{MachineID: "mac", SessionID: "child-missing", Title: "Missing parent child", Source: sourceSpawned, Via: "mac/absent"})
	sameChild := orgWorkingSession("mac", "child")
	sameChild.ParentSessionID = "parent"
	conflictChild := orgWorkingSession("mac", "child-conflict")
	conflictChild.ParentSessionID = "parent-b"
	missingChild := orgWorkingSession("mac", "child-missing")
	missingChild.ParentSessionID = "absent"
	e.f.setSessions("mac", orgWorkingSession("mac", "parent"), sameChild,
		orgWorkingSession("mac", "parent-a"), orgWorkingSession("mac", "parent-b"), conflictChild, missingChild)
	snapshot := orgSnapshotNow(t, e)
	nodes := orgNodesByID(snapshot)
	if got := orgNodeCount(snapshot, "agent:mac/child"); got != 1 {
		t.Fatalf("child appears %d times, want once", got)
	}
	if parent := nodes["agent:mac/child"].ParentID; parent == nil || *parent != "agent:mac/parent" {
		t.Fatalf("identical task/runtime parents were not deduplicated: %+v", nodes["agent:mac/child"])
	}
	for _, id := range []string{"agent:mac/child-conflict", "agent:mac/child-missing"} {
		if orgNodeCount(snapshot, id) != 1 || nodes[id].ParentID != nil {
			t.Fatalf("%s was cloned or given a guessed parent: %+v", id, nodes[id])
		}
	}
	if reason := nodes["agent:mac/child-conflict"].ParentAvailability.Reason; reason != "conflicting_parent_records" {
		t.Fatalf("parent conflict reason %q", reason)
	}
	if reason := nodes["agent:mac/child-missing"].ParentAvailability.Reason; reason != "parent_node_missing" {
		t.Fatalf("missing parent reason %q", reason)
	}
}

func TestOrganizationSnapshotParentCyclesKeepEveryNode(t *testing.T) {
	e := orgRegressionEnv(t, &memStore{})
	set := func(id, supervisor, session, via string) {
		orgPutTask(e, id, supervisor,
			TodoAgent{MachineID: "mac", SessionID: session, Title: session + " leader", Source: sourceLeader},
			TodoAgent{MachineID: "mac", SessionID: session + "-w", Title: session + " worker", Source: sourceWorker, Via: via})
	}
	set("todo_loop_a", "mac/session-b", "session-a", "todo_loop_b")
	set("todo_loop_b", "mac/session-a", "session-b", "todo_loop_a")
	set("todo_into_loop", "mac/session-a", "session-in", "todo_loop_a")
	snapshot := orgSnapshotNow(t, e)
	cycles := 0
	for _, id := range []string{"session-a", "session-a-w", "session-b", "session-b-w", "session-in", "session-in-w"} {
		nodeID := "agent:mac/" + id
		if got := orgNodeCount(snapshot, nodeID); got != 1 {
			t.Errorf("%s appears %d times, want once", nodeID, got)
		}
	}
	for _, node := range snapshot.Nodes {
		if node.ParentAvailability.Reason == "parent_cycle" {
			cycles++
		}
	}
	if cycles == 0 {
		t.Fatal("the worker cycle was hidden instead of reported")
	}
}

func orgBriefLinks(e *todoEventEnv, links map[string]taskBriefLink) {
	e.s.tbriefs.mu.Lock()
	defer e.s.tbriefs.mu.Unlock()
	e.s.tbriefs.init()
	for id, link := range links {
		e.s.tbriefs.links[id] = link
	}
}

func TestOrganizationSnapshotContinuationIsSeparateFromParentage(t *testing.T) {
	e := orgRegressionEnv(t, &memStore{})
	released := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	orgPutTask(e, "todo_old", "conv_lead_a", TodoAgent{MachineID: "mac", SessionID: "old", Title: "Old", ReleasedAt: &released})
	orgPutTask(e, "todo_current", "conv_lead_a", TodoAgent{MachineID: "mac", SessionID: "current", Title: "Current"})
	orgPutTask(e, "todo_split", "conv_lead_a", TodoAgent{MachineID: "mac", SessionID: "split", Title: "Split"})
	e.f.setSessions("mac", orgWorkingSession("mac", "current"), orgWorkingSession("mac", "split"))
	orgBriefLinks(e, map[string]taskBriefLink{
		"todo_old":     {Brief: "brief", At: released},
		"todo_current": {Brief: "brief", Continues: "todo_old", At: released.Add(time.Hour)},
		"todo_split":   {Brief: "brief", At: released.Add(2 * time.Hour)},
	})
	snapshot := orgSnapshotNow(t, e)
	if orgNodeCount(snapshot, "agent:mac/old") != 0 || orgNodeCount(snapshot, "agent:mac/current") != 1 || orgNodeCount(snapshot, "agent:mac/split") != 1 {
		t.Fatalf("continuation normalized the wrong workers: %+v", snapshot.Nodes)
	}
	todos := map[string]orgOpenTodo{}
	for _, todo := range snapshot.OpenTodos {
		todos[todo.ID] = todo
	}
	if len(todos) != 3 || todos["todo_current"].Continues != "todo_old" || todos["todo_old"].SupersededBy != "todo_current" ||
		todos["todo_current"].ContinuationStatus != "linked" || todos["todo_split"].ContinuationStatus != "none" {
		t.Fatalf("continuation metadata: %+v", snapshot.OpenTodos)
	}

	// An old to-do that is still independently open and active is not deleted
	// merely because a continuation points at a newer one.
	orgPutTask(e, "todo_old", "conv_lead_a", TodoAgent{MachineID: "mac", SessionID: "old", Title: "Old"})
	e.f.setSessions("mac", orgWorkingSession("mac", "old"), orgWorkingSession("mac", "current"), orgWorkingSession("mac", "split"))
	snapshot = orgSnapshotNow(t, e)
	if orgNodeCount(snapshot, "agent:mac/old") != 1 {
		t.Fatal("an independently active continuation branch was dropped")
	}

	orgBriefLinks(e, map[string]taskBriefLink{
		"todo_old":     {Brief: "brief", Continues: "todo_current", At: released},
		"todo_current": {Brief: "brief", Continues: "todo_old", At: released.Add(time.Hour)},
	})
	snapshot = orgSnapshotNow(t, e)
	for _, todo := range snapshot.OpenTodos {
		if todo.ID != "todo_split" && (todo.ContinuationStatus != "unknown" || todo.ContinuationReason != "continuation_cycle") {
			t.Fatalf("continuation cycle was not explicit: %+v", todo)
		}
	}
}

func TestOrganizationSnapshotMachineUnknownAndKnownZero(t *testing.T) {
	t.Run("fresh empty list is a known zero", func(t *testing.T) {
		e := orgRegressionEnv(t, &memStore{})
		e.f.setSessions("mac")
		snapshot := orgSnapshotNow(t, e)
		for _, machine := range snapshot.Machines {
			if machine.MachineID == "mac" && (machine.Running.Value == nil || *machine.Running.Value != 0) {
				t.Fatalf("fresh empty list: %+v", machine)
			}
		}
		main := orgNodesByID(snapshot)[orgMainNodeID(e.s.settings(context.Background()))]
		if main.Concurrency == nil || main.Concurrency.Running.Value == nil || *main.Concurrency.Running.Value != 0 {
			t.Fatalf("direction with no candidates must be a known zero: %+v", main.Concurrency)
		}
	})
	t.Run("offline retained rows are not zero", func(t *testing.T) {
		e := orgRegressionEnv(t, &memStore{})
		e.f.devices = []Device{{ID: "mac", Name: "mac", Online: false, Reachable: false}}
		e.f.setSessions("mac", orgWorkingSession("mac", "working"))
		snapshot := orgSnapshotNow(t, e)
		for _, machine := range snapshot.Machines {
			if machine.MachineID == "mac" && (machine.Running.Value != nil || machine.Freshness.Status != orgOffline) {
				t.Fatalf("offline machine: %+v", machine)
			}
		}
	})
	t.Run("claim-only machine remains visible", func(t *testing.T) {
		e := orgRegressionEnv(t, &memStore{})
		e.s.admission.machineClaims = map[string]int{"ghost": 3}
		snapshot := orgSnapshotNow(t, e)
		found := false
		for _, machine := range snapshot.Machines {
			if machine.MachineID == "ghost" {
				found = true
				if machine.Running.Value != nil || machine.UnlinkedStartClaims.Value == nil || *machine.UnlinkedStartClaims.Value != 3 || machine.ConcurrencyLimit.Value == nil {
					t.Fatalf("claim-only machine: %+v", machine)
				}
			}
		}
		if !found {
			t.Fatal("claim-only machine was dropped")
		}
	})
}

func TestOrganizationSnapshotSupervisorUnknownOwnStateSurvivesStoppedWorker(t *testing.T) {
	cases := []struct {
		name        string
		ownMachine  string
		ownSession  string
		supervisor  string
		nodeID      string
		workerState string
	}{
		{name: "main", ownMachine: "server", ownSession: "conv_main", supervisor: "conv_main", nodeID: "main:conv_main"},
		{name: "direction", ownMachine: "m", ownSession: "conv_lead_a", supervisor: "conv_lead_a", nodeID: "direction:conv_lead_a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := orgRegressionEnv(t, &memStore{})
			e.f.devices = []Device{
				{ID: "mac", Name: "mac", Online: true, Reachable: true},
				{ID: "server", Name: "server", Online: true, Reachable: true},
				{ID: "m", Name: "m", Online: true, Reachable: true},
			}
			e.f.sessionsFail = map[string]int{tc.ownMachine: 1}
			e.f.setSessions(tc.ownMachine, orgWorkingSession(tc.ownMachine, tc.ownSession))
			e.f.setSessions("mac")
			orgPutTask(e, "todo_failed", tc.supervisor, TodoAgent{
				MachineID: "mac", SessionID: "failed-worker", Title: "Failed worker", StartError: "boom",
			})

			snapshot := orgSnapshotNow(t, e)
			nodes := orgNodesByID(snapshot)
			supervisorNode, ok := nodes[tc.nodeID]
			if !ok {
				t.Fatalf("missing supervisor node %s", tc.nodeID)
			}
			if supervisorNode.State != nil {
				t.Fatalf("unknown own state was replaced by %q: %+v", *supervisorNode.State, supervisorNode.StateAvailability)
			}
			if supervisorNode.StateAvailability.Status != orgUnknown ||
				supervisorNode.StateAvailability.Reason != "session_list_unavailable" ||
				supervisorNode.StateAvailability.Source != "agents_list_session_list" {
				t.Fatalf("supervisor did not retain own unknown source: %+v", supervisorNode.StateAvailability)
			}
			worker, workerOK := nodes["agent:mac/failed-worker"]
			if !workerOK || worker.State == nil || *worker.State != "stopped" ||
				worker.StateAvailability.Status != orgAvailable || worker.Activity != "start_failed" {
				t.Fatalf("known worker failure was not retained: %+v", worker)
			}
			if tc.name == "main" {
				owner, ownerOK := nodes["owner"]
				if !ownerOK || owner.State != nil || owner.StateAvailability.Status != orgUnknown ||
					owner.StateAvailability.Reason != "session_list_unavailable" ||
					owner.StateAvailability.Source != "agents_list_session_list" {
					t.Fatalf("owner did not inherit the completed main unknown: %+v", owner)
				}
			}
		})
	}
}

func TestOrganizationSnapshotOwnerUsesFinishedMainState(t *testing.T) {
	cases := []struct {
		name     string
		state    string
		status   string
		activity string
	}{
		{name: "working", state: proto.StateWorking, status: proto.StatusWorking, activity: "working"},
		{name: "needs_you", state: proto.StateNeedsYou, status: proto.StatusBlocked, activity: "owner_decision"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := orgRegressionEnv(t, &memStore{})
			mainSession := orgWorkingSession("server", "conv_main")
			mainSession.State = tc.state
			mainSession.Status = tc.status
			e.f.setSessions("server", mainSession)

			snapshot := orgSnapshotNow(t, e)
			nodes := orgNodesByID(snapshot)
			main, mainOK := nodes["main:conv_main"]
			owner, ownerOK := nodes["owner"]
			if !mainOK || main.State == nil || *main.State != tc.state || main.Activity != tc.activity ||
				main.StateAvailability.Status != orgAvailable || main.StateAvailability.Source != "runtime_session" {
				t.Fatalf("main did not establish its own state: %+v", main)
			}
			if !ownerOK || owner.State == nil || *owner.State != tc.state || owner.Activity != tc.activity ||
				owner.StateAvailability.Status != orgAvailable || owner.StateAvailability.Source != "runtime_session" {
				t.Fatalf("owner did not use the finished main state: %+v", owner)
			}
		})
	}
}

func TestOrganizationSnapshotOwnerOnlyMethodsAndRepeatedReads(t *testing.T) {
	store := &memStore{}
	e := directionEnv(t, store)
	setSettings(t, e.s, func(v *Settings) { v.Main, v.Timezone = "conv_main", "UTC" })
	e.title("conv_lead_a", "Direction A")
	e.title("conv_lead_b", "Direction B")
	e.leaders("conv_lead_a", "conv_lead_b")
	published := make(chan struct{}, 1)
	e.f.mu.Lock()
	e.f.onTell = func() {
		select {
		case published <- struct{}{}:
		default:
		}
	}
	e.f.mu.Unlock()
	e.syncLeaders()
	// syncLeaders publishes its new direction records asynchronously. Let that
	// seed write finish before the read-only measurement window starts.
	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("the seed direction publish did not finish")
	}
	orgPutTask(e, "todo_worker", "conv_lead_a", TodoAgent{MachineID: "mac", SessionID: "worker", Title: "Worker"})
	e.f.setSessions("mac", orgWorkingSession("mac", "worker"))
	e.f.answerNode(DeviceSessionItems, func(string, json.RawMessage) (any, error) { return historyPage{}, nil })
	for _, role := range []string{"answer", "extension", "link", "peer", "readonly"} {
		e.f.calls = nil
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, organizationSnapshotRoute, nil)
		request.Header.Set(RoleHeader, role)
		e.s.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden || len(e.f.calls) != 0 {
			t.Fatalf("role %s: status %d calls %d", role, recorder.Code, len(e.f.calls))
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		e.f.calls = nil
		recorder := httptest.NewRecorder()
		e.s.ServeHTTP(recorder, httptest.NewRequest(method, organizationSnapshotRoute, nil))
		if recorder.Code != http.StatusMethodNotAllowed || len(e.f.calls) != 0 {
			t.Fatalf("%s: status %d calls %d", method, recorder.Code, len(e.f.calls))
		}
	}

	beforeTree, err := json.Marshal(e.s.directionTree(context.Background(), time.Date(2026, 11, 24, 1, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	e.f.mu.Lock()
	modelRequests, events, snaps := e.f.modelRequests, len(e.f.events), len(e.f.snaps)
	starts, sends, agentSent, sentOnce := len(e.f.starts), len(e.f.sends), len(e.f.agentSent), len(e.f.sentOnce)
	pushes, told := len(e.f.pushes), len(e.f.told)
	e.f.mu.Unlock()
	puts := store.putCount()
	e.s.admission.mu.Lock()
	claims := maps.Clone(e.s.admission.machineClaims)
	directionClaims := maps.Clone(e.s.admission.directionClaims)
	e.s.admission.mu.Unlock()
	for range 2 {
		recorder := httptest.NewRecorder()
		e.s.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, organizationSnapshotRoute, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET: %d %s", recorder.Code, recorder.Body)
		}
	}
	if recorder := httptest.NewRecorder(); func() bool {
		e.s.ServeHTTP(recorder, httptest.NewRequest(http.MethodHead, organizationSnapshotRoute, nil))
		return recorder.Code == http.StatusOK
	}() != true {
		t.Fatal("standard HEAD read was rejected")
	}
	afterTree, err := json.Marshal(e.s.directionTree(context.Background(), time.Date(2026, 11, 24, 1, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeTree, afterTree) {
		t.Fatal("the existing direction-tree response changed")
	}
	e.f.mu.Lock()
	unchanged := modelRequests == e.f.modelRequests && events == len(e.f.events) && snaps == len(e.f.snaps) &&
		starts == len(e.f.starts) && sends == len(e.f.sends) && agentSent == len(e.f.agentSent) && sentOnce == len(e.f.sentOnce) &&
		pushes == len(e.f.pushes) && told == len(e.f.told)
	e.f.mu.Unlock()
	e.s.admission.mu.Lock()
	afterClaims := maps.Clone(e.s.admission.machineClaims)
	afterDirectionClaims := maps.Clone(e.s.admission.directionClaims)
	e.s.admission.mu.Unlock()
	if !unchanged || puts != store.putCount() || !maps.Equal(claims, afterClaims) || !maps.Equal(directionClaims, afterDirectionClaims) {
		t.Fatalf("repeated reads caused side effects: puts %d/%d claims %v/%v direction %v/%v", puts, store.putCount(), claims, afterClaims, directionClaims, afterDirectionClaims)
	}
	for _, call := range e.f.calls {
		switch call.Method {
		case DeviceDevicesList, DeviceSessionsList, DeviceInteractionsList, DeviceSessionItems:
		default:
			t.Fatalf("unexpected write or control call during GET: %s", call.Method)
		}
	}
}

func TestOrganizationSnapshotColdReadHasNoSideEffects(t *testing.T) {
	store := &memStore{}
	hub := newFakeHub()
	service := newServiceOn(t, hub, store)
	for range 2 {
		recorder := httptest.NewRecorder()
		service.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, organizationSnapshotRoute, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("cold GET: %d %s", recorder.Code, recorder.Body)
		}
	}
	if store.putCount() != 0 {
		t.Fatalf("cold read seeded or changed settings: %d writes", store.putCount())
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.modelRequests != 0 || len(hub.starts) != 0 || len(hub.sends) != 0 || len(hub.events) != 0 || len(hub.pushes) != 0 {
		t.Fatal("cold read caused model, start, send, event, or push side effects")
	}
}

func TestOrganizationSnapshotMultipleProjectsMakeDirectionLimitUnknown(t *testing.T) {
	e := orgRegressionEnv(t, &memStore{})
	orgPutTask(e, "todo_project_a", "conv_lead_a")
	orgPutTask(e, "todo_project_b", "conv_lead_a")
	e.s.todos.mu.Lock()
	e.s.todos.todos["todo_project_a"].Project = "project-a"
	e.s.todos.todos["todo_project_b"].Project = "project-b"
	e.s.todos.mu.Unlock()
	snapshot := orgSnapshotNow(t, e)
	node := orgNodesByID(snapshot)["direction:conv_lead_a"]
	if node.Concurrency == nil || node.Concurrency.Limit.Value != nil || node.Concurrency.Limit.Status != orgUnknown {
		t.Fatalf("ambiguous direction limit was guessed: %+v", node.Concurrency)
	}
}

func TestOrganizationSnapshotUniqueIDsAcrossEveryRead(t *testing.T) {
	e := orgRegressionEnv(t, &memStore{})
	orgPutTask(e, "todo_unique", "conv_lead_a", TodoAgent{MachineID: "mac", SessionID: "worker", Title: "Worker"})
	e.f.setSessions("mac", orgWorkingSession("mac", "worker"))
	for range 3 {
		snapshot := orgSnapshotNow(t, e)
		seen := map[string]bool{}
		for _, node := range snapshot.Nodes {
			if seen[node.ID] {
				t.Fatalf("duplicate node %s", node.ID)
			}
			seen[node.ID] = true
		}
		ids := map[string]bool{}
		for _, todo := range snapshot.OpenTodos {
			if ids[todo.ID] {
				t.Fatalf("duplicate open to-do %s", todo.ID)
			}
			ids[todo.ID] = true
		}
		if !ids["todo_unique"] {
			t.Fatal("open to-do missing")
		}
	}
}
