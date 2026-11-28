package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

type placementTaskStore struct {
	*profileTaskStore
}

func (st *placementTaskStore) TaskPlansQueued(_ context.Context, _ time.Time) ([]db.TaskPlanRow, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]db.TaskPlanRow, 0, len(st.plans))
	for _, row := range st.plans {
		out = append(out, row)
	}
	return out, nil
}

type placementTestEnv struct {
	s      *Service
	f      *todoEventHub
	d      *resourceDevs
	store  *placementTaskStore
	mu     sync.Mutex
	now    time.Time
	logs   []string
	events []recordedKBEvent
}

func (e *placementTestEnv) logsText() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.logs, "\n")
}

func (e *placementTestEnv) clearLogs() {
	e.mu.Lock()
	e.logs = nil
	e.mu.Unlock()
}

func (e *placementTestEnv) clockNow() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *placementTestEnv) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

func (e *placementTestEnv) placementEvents() []assistantwire.PlacementPendingEvent {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	var out []assistantwire.PlacementPendingEvent
	for _, ev := range e.f.sent {
		if ev.name != assistantwire.EventPlacementPending {
			continue
		}
		var p assistantwire.PlacementPendingEvent
		if json.Unmarshal(ev.raw, &p) == nil {
			out = append(out, p)
		}
	}
	return out
}

func newPlacementTestEnv(t *testing.T) *placementTestEnv {
	t.Helper()
	base := newFakeHub().withMachine()
	base.devices = []Device{
		{ID: "hub-1", Name: "Hub", Kind: "local", Home: "/home/hub", Online: true, Reachable: true},
		{ID: "linux-heavy", Name: "Linux Heavy", Kind: "remote", Home: "/home/linux", Online: true, Reachable: true},
		{ID: "linux-idle", Name: "Linux Idle", Kind: "remote", Home: "/home/linux", Online: true, Reachable: true},
		{ID: "mac-1", Name: "Mac Mini", Kind: "remote", Home: "/Users/dev", Online: true, Reachable: true},
	}
	base.devicesFull = true
	base.launcherList = []proto.Launcher{
		{Name: "codex"},
		{Name: "claude"},
	}
	base.sessions["hub-1"] = []proto.Session{
		{SessionID: "conv_pub", Title: "Public Chat", Assistant: true},
		{SessionID: "conv_priv", Title: "Private Health Chat", Assistant: true, Space: SpaceHealth},
	}
	base.sessions["linux-heavy"] = nil
	base.sessions["linux-idle"] = nil
	base.sessions["mac-1"] = nil
	th := &todoEventHub{fakeHub: base}

	d := &resourceDevs{
		fleetDevs: newFleetDevs(),
		machines: map[string]*proto.Machine{
			"hub-1":       {Kind: "local", OS: "linux", Arch: "amd64", System: &proto.System{OS: "linux", Arch: "amd64", Cores: 16}},
			"linux-heavy": {Kind: "remote", OS: "linux", Arch: "amd64", System: &proto.System{OS: "linux", Arch: "amd64", Cores: 8}},
			"linux-idle":  {Kind: "remote", OS: "linux", Arch: "amd64", System: &proto.System{OS: "linux", Arch: "amd64", Cores: 8}},
			"mac-1":       {Kind: "remote", OS: "darwin", Arch: "arm64", System: &proto.System{OS: "darwin", Arch: "arm64", Cores: 12}},
		},
		samples: map[string]*proto.MetricSample{
			"hub-1":       admissionSample(0.2, 32<<30, 8<<30, 0),
			"linux-heavy": admissionSample(4.0, 32<<30, 20<<30, 2),
			"linux-idle":  admissionSample(0.5, 32<<30, 6<<30, 0),
			"mac-1":       admissionSample(1.0, 32<<30, 8<<30, 1),
		},
		versions: map[string]*bool{},
	}
	for _, dev := range base.devices {
		d.add(dev.ID, dev.Name)
		d.setSessions(dev.ID, base.sessions[dev.ID])
	}

	store := &placementTaskStore{profileTaskStore: &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	env := &placementTestEnv{
		f:     th,
		d:     d,
		store: store,
		now:   time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC),
	}
	s := New(Options{
		Hub:   th,
		Store: func() Store { return store },
		Logf: func(format string, args ...any) {
			env.mu.Lock()
			env.logs = append(env.logs, fmt.Sprintf(format, args...))
			env.mu.Unlock()
		},
	})
	env.s = s
	t.Cleanup(func() {
		waitFor(t, "task starts settle", func() bool { return s.taskStarting.Load() == 0 })
	})
	s.devs = d
	s.health.loaded = true
	s.health.recheck = map[string]*healthRecheck{}
	s.health.recheckAt = map[string]time.Time{}
	on := true
	s.admission.cached = &AdmissionSettings{Enabled: &on}
	s.admission.at = time.Now()
	s.admission.machineClaims = map[string]int{}
	s.admission.directionClaims = map[string]int{}
	setSettings(t, s, func(v *Settings) {
		v.Main = "conv_pub"
		v.MachineID = "hub-1"
	})

	s.placementSched.now = env.clockNow
	s.placementSched.manualSweep = true
	// Register public/private conversation spaces in the space book so todoVerdicts knows them immediately.
	s.noteConversationSpaces(context.Background(), []db.ConversationSpace{
		{MachineID: "hub-1", SessionID: "conv_pub", Space: ""},
		{MachineID: "hub-1", SessionID: "conv_priv", Space: SpaceHealth},
	})
	return env
}

func (e *placementTestEnv) doReq(method, path, role, extID, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if role != "" {
		r.Header.Set(RoleHeader, role)
	}
	if extID != "" {
		r.Header.Set(ExtensionHeader, extID)
	}
	e.s.ServeHTTP(rec, r)
	return rec
}

func extractPlanID(t *testing.T, res string) string {
	t.Helper()
	id := regexp.MustCompile(`plan_[0-9a-f]+`).FindString(res)
	if id == "" {
		t.Fatalf("no plan id in response: %s", res)
	}
	return id
}

func TestTaskPlanExplicitMachineKeepsBackwardCompatibility(t *testing.T) {
	e := newPlacementTestEnv(t)
	ctx := context.Background()
	res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
		"tasks":[{
			"title":"Run explicit check",
			"prompt":"Inspect repository files",
			"machine":"linux-idle",
			"cwd":"~/repo",
			"role":"worker",
			"readonly":true,
			"goal":"Confirm layout",
			"user_requested":true,
			"why":"Explicit target requested"
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	planID := extractPlanID(t, res)
	p, err := e.s.loadPlan(ctx, planID)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Tasks[0]; got.Machine != "linux-idle" || got.Cwd != "/home/linux/repo" || got.MachinePolicy != machineFixed {
		t.Fatalf("explicit machine plan altered: %+v", got)
	}
	if p.Tasks[0].Queue != nil && p.Tasks[0].Queue.Key == pendingPlacementQueueKey {
		t.Fatalf("explicit machine task must not enter pending_placement: %+v", p.Tasks[0].Queue)
	}
	waitFor(t, "explicit machine agent start", func() bool { return len(taskStartMachines(e.f.fakeHub)) == 1 })
	if got := taskStartMachines(e.f.fakeHub); got[0] != "linux-idle" {
		t.Fatalf("started on %v, want [linux-idle]", got)
	}
}

func TestTaskPlanOptionalMachineConstraintsValidationAndPersistence(t *testing.T) {
	e := newPlacementTestEnv(t)
	ctx := context.Background()

	// Invalid enum values and machine-bound workspaces without explicit machine are rejected cleanly.
	for _, tc := range []struct {
		name string
		args string
		want string
	}{
		{"bad-os", `{"tasks":[{"title":"Bad OS","prompt":"p","cwd":"~/repo","role":"worker","readonly":true,"goal":"g","user_requested":true,"why":"w","placement_constraints":{"os":"freebsd"}}]}`, "placement_constraints.os"},
		{"bad-workload", `{"tasks":[{"title":"Bad Workload","prompt":"p","cwd":"~/repo","role":"worker","readonly":true,"goal":"g","user_requested":true,"why":"w","placement_constraints":{"workload":"extreme"}}]}`, "placement_constraints.workload"},
		{"container-without-machine", `{"tasks":[{"title":"Container","prompt":"p","workspace":{"container":{"selected_repo_ids":["r1"]}},"role":"worker","readonly":true,"goal":"g","user_requested":true,"why":"w"}]}`, "explicit machine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got err=%v, want substring %q", err, tc.want)
			}
		})
	}

	// Valid optional machine with placement_constraints defers ~ expansion and persists pending_placement across reload.
	res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
		"tasks":[{
			"title":"Auto placed task",
			"prompt":"Secret task prompt never shown to scheduler",
			"cwd":"~/repo",
			"launcher":"codex",
			"role":"worker",
			"readonly":true,
			"goal":"Inspect codebase",
			"user_requested":true,
			"why":"Any healthy Linux worker",
			"placement_constraints":{
				"os":"LINUX",
				"capabilities":["GPU","linux"],
				"workload":"HEAVY",
				"candidate_machines":["linux-idle","linux-heavy"]
			}
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	planID := extractPlanID(t, res)
	p, err := e.s.loadPlan(ctx, planID)
	if err != nil {
		t.Fatal(err)
	}
	pt := p.Tasks[0]
	if pt.Machine != "" || pt.Cwd != "~/repo" {
		t.Fatalf("machine/cwd should be deferred: machine=%q cwd=%q", pt.Machine, pt.Cwd)
	}
	if pt.Queue == nil || pt.Queue.Key != pendingPlacementQueueKey || pt.Queue.Placement == nil || pt.Queue.Placement.Version != 1 {
		t.Fatalf("pending_placement queue not persisted: %+v", pt.Queue)
	}
	if pt.PlacementConstraints == nil || pt.PlacementConstraints.OS != "linux" || pt.PlacementConstraints.Workload != "heavy" {
		t.Fatalf("placement_constraints not normalized: %+v", pt.PlacementConstraints)
	}
	// Simulate hub restart by deserializing directly from the persisted TaskPlanRow JSON.
	raw, err := json.Marshal(p.Tasks)
	if err != nil {
		t.Fatal(err)
	}
	restored := planFromRow(db.TaskPlanRow{ID: p.ID, Conversation: p.Conversation, State: p.State, Tasks: raw})
	if rpt := restored.Tasks[0]; rpt.Queue == nil || rpt.Queue.Key != pendingPlacementQueueKey ||
		rpt.Queue.Placement == nil || rpt.Queue.Placement.Version != 1 ||
		rpt.PlacementConstraints == nil || rpt.PlacementConstraints.OS != "linux" {
		t.Fatalf("persisted pending_placement lost after restart reload: %+v", rpt)
	}
	if starts := taskStartMachines(e.f.fakeHub); len(starts) != 0 {
		t.Fatalf("deferred task should not start before placement: %v", starts)
	}
}

func TestPlacementPendingEndpointLeasePrivacyAndEvents(t *testing.T) {
	e := newPlacementTestEnv(t)
	ctx := context.Background()

	// Missing ExtensionHeader is rejected for extension role.
	if rec := e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("missing ExtensionHeader: got %d, want 403", rec.Code)
	}

	// Polling GET /placement/pending activates a 60s lease that expires when polling stops.
	rec := e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "sched-ext", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /pending: %d %s", rec.Code, rec.Body.String())
	}
	if !e.s.hasActivePlacementLease(e.clockNow().Add(59 * time.Second)) {
		t.Fatal("lease should remain active at 59s")
	}
	if e.s.hasActivePlacementLease(e.clockNow().Add(60 * time.Second)) {
		t.Fatal("lease should expire at 60s")
	}
	// Refresh lease back to active at current clock.
	_ = e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "sched-ext", "")

	// Enqueue one visible task and one hidden privacy-space task.
	pubRes, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
		"tasks":[{
			"title":"Public Secret Title",
			"context":"Private context hint",
			"prompt":"Ultra secret prompt body",
			"cwd":"~/repo",
			"launcher":"codex",
			"role":"worker",
			"readonly":true,
			"goal":"Check repo",
			"user_requested":true,
			"why":"Public work",
			"placement_constraints":{"os":"linux","workload":"standard"}
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	pubPlanID := extractPlanID(t, pubRes)
	pubPlan, _ := e.s.loadPlan(ctx, pubPlanID)
	pubTodoID := pubPlan.Tasks[0].TodoID

	privRes, err := e.s.toolTaskPlan(ctx, "conv_priv", json.RawMessage(`{
		"tasks":[{
			"title":"Health private task",
			"prompt":"Sensitive medical prompt",
			"cwd":"~/notes",
			"role":"worker",
			"readonly":true,
			"goal":"Check notes",
			"user_requested":true,
			"why":"Private work"
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	privPlanID := extractPlanID(t, privRes)
	privPlan, _ := e.s.loadPlan(ctx, privPlanID)
	privTodoID := privPlan.Tasks[0].TodoID

	// Only the visible task emits placement.pending, carrying only {plan_id, todo_id, batch_size}.
	evs := e.placementEvents()
	if len(evs) != 1 || string(evs[0].PlanID) != pubPlanID || string(evs[0].TodoID) != pubTodoID || evs[0].BatchSize != 1 {
		t.Fatalf("placement.pending events = %+v, want only visible task %s/%s", evs, pubPlanID, pubTodoID)
	}

	// GET /pending returns only the visible task and never leaks prompts/titles/context.
	rec = e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "sched-ext", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /pending: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, leak := range []string{"Ultra secret prompt body", "Public Secret Title", "Private context hint", "Sensitive medical prompt", privTodoID, privPlanID} {
		if strings.Contains(body, leak) {
			t.Fatalf("GET /pending leaked %q: %s", leak, body)
		}
	}
	var pending struct {
		LeaseTTLSeconds int                                  `json:"lease_ttl_seconds"`
		Tasks           []assistantwire.PlacementPendingTask `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if pending.LeaseTTLSeconds != 60 || len(pending.Tasks) != 1 {
		t.Fatalf("pending response = %+v", pending)
	}
	got := pending.Tasks[0]
	if string(got.TodoID) != pubTodoID || got.Version != 1 || got.CwdHint != "~/repo" || got.Launcher != "codex" ||
		got.PlacementConstraints == nil || got.PlacementConstraints.OS != "linux" || got.PlacementConstraints.Workload != "standard" {
		t.Fatalf("pending task payload = %+v", got)
	}

	// Extension cannot assign or defer a hidden privacy-space task (404).
	if rec := e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+privTodoID+"/assign", roleExtension, "sched-ext", `{"machine":"linux-idle","version":1}`); rec.Code != http.StatusNotFound {
		t.Fatalf("assign hidden todo = %d %s, want 404", rec.Code, rec.Body.String())
	}
	if rec := e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+privTodoID+"/defer", roleExtension, "sched-ext", `{"reason":"busy","version":1}`); rec.Code != http.StatusNotFound {
		t.Fatalf("defer hidden todo = %d %s, want 404", rec.Code, rec.Body.String())
	}
}

func TestPlacementAssignEnforcesHardConstraintsAdmissionAndCAS(t *testing.T) {
	e := newPlacementTestEnv(t)
	ctx := context.Background()
	_ = e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "sched-ext", "")

	res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
		"tasks":[{
			"title":"Linux worker job",
			"prompt":"Run tests",
			"cwd":"~/repo",
			"launcher":"codex",
			"role":"worker",
			"readonly":true,
			"goal":"Verify suite",
			"user_requested":true,
			"why":"Needs Linux worker",
			"placement_constraints":{
				"os":"linux",
				"capabilities":["amd64"],
				"candidate_machines":["linux-idle","linux-heavy","hub-1","mac-1"]
			}
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	planID := extractPlanID(t, res)
	p, _ := e.s.loadPlan(ctx, planID)
	todoID := p.Tasks[0].TodoID

	// 1. Hub machine rejected when require_hub is false (default).
	rec := e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/assign", roleExtension, "sched-ext", `{"machine":"hub-1","reason":"try hub","version":1}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "hub_excluded") {
		t.Fatalf("hub assign = %d %s, want 422 hub_excluded", rec.Code, rec.Body.String())
	}

	// 2. OS mismatch rejected (mac-1 is darwin).
	rec = e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/assign", roleExtension, "sched-ext", `{"machine":"mac-1","reason":"try mac","version":1}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "os_mismatch") {
		t.Fatalf("os mismatch assign = %d %s, want 422 os_mismatch", rec.Code, rec.Body.String())
	}

	// 3. Admission gate refusal (overload linux-heavy) returns 422 admission_refused and leaks no slot.
	e.d.samples["linux-heavy"] = admissionSample(99.0, 32<<30, 4<<30, 0)
	rec = e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/assign", roleExtension, "sched-ext", `{"machine":"linux-heavy","reason":"try overloaded","version":1}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "admission_refused") {
		t.Fatalf("overloaded assign = %d %s, want 422 admission_refused", rec.Code, rec.Body.String())
	}

	// 4. Failing launcher on target machine returns 422 launcher_unavailable.
	e.s.health.recs = map[string]*LauncherHealth{
		"linux-idle/codex": {Machine: "linux-idle", Launcher: "codex", State: healthFailing, Kind: errKindUsageLimit, Error: "usage limit reached", CheckedAt: time.Now()},
	}
	rec = e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/assign", roleExtension, "sched-ext", `{"machine":"linux-idle","reason":"try broken launcher","version":1}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "launcher_unavailable") {
		t.Fatalf("unhealthy launcher assign = %d %s, want 422 launcher_unavailable", rec.Code, rec.Body.String())
	}
	e.s.health.recs = nil

	// 4b. Missing target directory returns 422 directory_missing.
	e.f.mu.Lock()
	if e.f.nodes == nil {
		e.f.nodes = map[string]nodeAnswer{}
	}
	e.f.nodes["fs.stat"] = func(_ string, _ json.RawMessage) (any, error) {
		return map[string]any{"stats": []map[string]string{{"path": "/home/linux/repo", "kind": "missing"}}}, nil
	}
	e.f.mu.Unlock()
	rec = e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/assign", roleExtension, "sched-ext", `{"machine":"linux-idle","reason":"try missing dir","version":1}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "directory_missing") {
		t.Fatalf("missing dir assign = %d %s, want 422 directory_missing", rec.Code, rec.Body.String())
	}
	e.f.mu.Lock()
	delete(e.f.nodes, "fs.stat")
	e.f.mu.Unlock()

	// 5. Stale CAS version returns 409 placement_version_mismatch.
	rec = e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/assign", roleExtension, "sched-ext", `{"machine":"linux-idle","reason":"wrong version","version":9}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "placement_version_mismatch") {
		t.Fatalf("wrong version assign = %d %s, want 409 placement_version_mismatch", rec.Code, rec.Body.String())
	}

	// 6. Valid assign succeeds, expands ~/repo on target, writes placement audit, clears queue, and starts agent once.
	rec = e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/assign", roleExtension, "sched-ext", `{"machine":"linux-idle","reason":"selected by score","version":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid assign = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var assigned map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &assigned); err != nil {
		t.Fatal(err)
	}
	if assigned["assigned"] != true || assigned["machine"] != "linux-idle" || assigned["cwd"] != "/home/linux/repo" || assigned["placement_id"] == "" {
		t.Fatalf("assign response = %v", assigned)
	}
	waitFor(t, "assigned task starts on linux-idle", func() bool {
		return len(taskStartMachines(e.f.fakeHub)) == 1 && e.s.taskStarting.Load() == 0
	})
	if starts := taskStartMachines(e.f.fakeHub); len(starts) != 1 || starts[0] != "linux-idle" {
		t.Fatalf("started machines = %v, want [linux-idle]", starts)
	}
	stored, _ := e.s.loadPlan(ctx, planID)
	if stored.Tasks[0].Queue != nil || stored.Tasks[0].Machine != "linux-idle" || stored.Tasks[0].Cwd != "/home/linux/repo" {
		t.Fatalf("stored plan after assign: %+v", stored.Tasks[0])
	}
	if stored.Tasks[0].Placement == nil || stored.Tasks[0].Placement.ReasonKey != placementReasonExtension || stored.Tasks[0].Placement.Choice != "linux-idle" {
		t.Fatalf("placement audit not recorded on task: %+v", stored.Tasks[0].Placement)
	}
	auditRec := onlyStoredPlacement(t, e.store)
	if auditRec.Choice != "linux-idle" || auditRec.ReasonKey != placementReasonExtension || !strings.Contains(auditRec.Reason, "selected by score") {
		t.Fatalf("stored placement audit record = %+v", auditRec)
	}

	// 7. Duplicate CAS assign returns 409 placement_conflict and never double-starts.
	rec = e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/assign", roleExtension, "sched-ext", `{"machine":"linux-idle","reason":"duplicate","version":1}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "placement_conflict") {
		t.Fatalf("duplicate assign = %d %s, want 409 placement_conflict", rec.Code, rec.Body.String())
	}
	if starts := taskStartMachines(e.f.fakeHub); len(starts) != 1 {
		t.Fatalf("duplicate assign started extra agent: %v", starts)
	}
}

func TestPlacementAssignPreservesTraexModelAndEffort(t *testing.T) {
	e := newPlacementTestEnv(t)
	e.f.launcherList = append(e.f.launcherList, proto.Launcher{Name: "traex", Agent: "traex"})
	ctx := context.Background()
	_ = e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "sched-ext", "")

	res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
		"tasks":[{
			"title":"TRAE CLI terminal task",
			"prompt":"Inspect the repository",
			"cwd":"~/repo",
			"launcher":"traex",
			"model":"example-fast",
			"effort":"xhigh",
			"role":"worker",
			"readonly":true,
			"goal":"Check the repository",
			"user_requested":true,
			"why":"Use the chosen model on a Linux worker",
			"placement_constraints":{"os":"linux"}
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := e.s.loadPlan(ctx, extractPlanID(t, res))
	if err != nil {
		t.Fatal(err)
	}
	rec := e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+plan.Tasks[0].TodoID+"/assign",
		roleExtension, "sched-ext", `{"machine":"linux-idle","reason":"selected by score","version":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("assign = %d %s, want 200", rec.Code, rec.Body.String())
	}
	waitFor(t, "assigned TRAE CLI task starts", func() bool {
		return len(taskStartMachines(e.f.fakeHub)) == 1 && e.s.taskStarting.Load() == 0
	})
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	if len(e.f.starts) != 1 || e.f.starts[0]["launcher"] != "traex" {
		t.Fatalf("starts = %v, want one TRAE CLI start", e.f.starts)
	}
	start := e.f.starts[0]
	if start["model"] != "example-fast" || start["effort"] != "xhigh" {
		t.Fatalf("TRAE CLI start got model %q and effort %q, want example-fast and xhigh", start["model"], start["effort"])
	}
}

func TestPlacementDeferCASAndVisibility(t *testing.T) {
	e := newPlacementTestEnv(t)
	ctx := context.Background()
	_ = e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "sched-ext", "")

	res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
		"tasks":[{
			"title":"Deferrable job",
			"prompt":"Wait for capacity",
			"cwd":"~/repo",
			"role":"worker",
			"readonly":true,
			"goal":"Run later",
			"user_requested":true,
			"why":"Capacity wait"
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	planID := extractPlanID(t, res)
	p, _ := e.s.loadPlan(ctx, planID)
	todoID := p.Tasks[0].TodoID

	nextAt := e.clockNow().Add(45 * time.Second).Format(time.RFC3339)
	rec := e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/defer", roleExtension, "sched-ext",
		fmt.Sprintf(`{"reason":"all workers saturated","next_at":%q,"version":1}`, nextAt))
	if rec.Code != http.StatusOK {
		t.Fatalf("defer = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var out struct {
		Deferred bool `json:"deferred"`
		Version  int  `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Deferred || out.Version != 2 {
		t.Fatalf("defer response = %+v", out)
	}

	// Deferred task is hidden from GET /pending until next_at.
	rec = e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "sched-ext", "")
	if strings.Contains(rec.Body.String(), todoID) {
		t.Fatalf("deferred task should not appear before next_at: %s", rec.Body.String())
	}

	// Stale version defer is rejected with 409 placement_version_mismatch.
	rec = e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/defer", roleExtension, "sched-ext", `{"reason":"stale","version":1}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "placement_version_mismatch") {
		t.Fatalf("stale defer = %d %s, want 409 placement_version_mismatch", rec.Code, rec.Body.String())
	}

	// Advance clock past next_at: task reappears with version=2 and last_reason preserved.
	e.advance(46 * time.Second)
	rec = e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "sched-ext", "")
	var pending struct {
		Tasks []assistantwire.PlacementPendingTask `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if len(pending.Tasks) != 1 || pending.Tasks[0].Version != 2 || pending.Tasks[0].LastReason != "all workers saturated" {
		t.Fatalf("pending after defer expiry = %+v", pending.Tasks)
	}
}

func TestPlacementFallbackZeroSecondFifteenSecondAndTimeout(t *testing.T) {
	t.Run("zero-second-without-lease", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
			"tasks":[{
				"title":"No scheduler active",
				"prompt":"Run immediately via fallback",
				"cwd":"~/repo",
				"role":"worker",
				"readonly":true,
				"goal":"Quick check",
				"user_requested":true,
				"why":"Fallback check",
				"placement_constraints":{"os":"linux"}
			}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		planID := extractPlanID(t, res)
		// No lease exists -> 0s immediate fallback on sweep picks linux-idle (lowest load, excluding hub-1).
		e.s.sweepPendingPlacement(ctx)
		waitFor(t, "0s fallback starts on linux-idle", func() bool { return len(taskStartMachines(e.f.fakeHub)) == 1 })
		if starts := taskStartMachines(e.f.fakeHub); starts[0] != "linux-idle" {
			t.Fatalf("0s fallback picked %v, want [linux-idle]", starts)
		}
		p, _ := e.s.loadPlan(ctx, planID)
		if p.Tasks[0].Placement == nil || p.Tasks[0].Placement.ReasonKey != placementReasonFallback {
			t.Fatalf("fallback placement audit = %+v", p.Tasks[0].Placement)
		}
	})

	t.Run("zero-second-for-hidden-space-even-with-lease", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		_ = e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "sched-ext", "")
		_, err := e.s.toolTaskPlan(ctx, "conv_priv", json.RawMessage(`{
			"tasks":[{
				"title":"Private space job",
				"prompt":"Do not wait 15s for extension",
				"cwd":"~/repo",
				"role":"worker",
				"readonly":true,
				"goal":"Private check",
				"user_requested":true,
				"why":"Private work",
				"placement_constraints":{"os":"linux"}
			}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		e.s.sweepPendingPlacement(ctx)
		waitFor(t, "hidden task falls back at 0s", func() bool { return len(taskStartMachines(e.f.fakeHub)) == 1 })
		if starts := taskStartMachines(e.f.fakeHub); starts[0] != "linux-idle" {
			t.Fatalf("hidden task fallback picked %v, want [linux-idle]", starts)
		}
	})

	t.Run("fifteen-second-fallback-with-active-lease", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		_ = e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "sched-ext", "")
		_, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
			"tasks":[{
				"title":"Waiting for scheduler",
				"prompt":"Run on mac",
				"cwd":"~/repo",
				"role":"worker",
				"readonly":true,
				"goal":"Check mac",
				"user_requested":true,
				"why":"Needs macOS",
				"placement_constraints":{"capabilities":["macos","xcode"]}
			}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		// At 10s, lease is active and 15s fallback window has not elapsed -> must NOT fall back yet.
		e.advance(10 * time.Second)
		e.s.sweepPendingPlacement(ctx)
		if starts := taskStartMachines(e.f.fakeHub); len(starts) != 0 {
			t.Fatalf("fell back too early at 10s: %v", starts)
		}
		// At 15s, fallback triggers and picks mac-1 (the only darwin/xcode-capable machine).
		e.advance(5 * time.Second)
		e.s.sweepPendingPlacement(ctx)
		waitFor(t, "15s fallback starts on mac-1", func() bool { return len(taskStartMachines(e.f.fakeHub)) == 1 })
		if starts := taskStartMachines(e.f.fakeHub); starts[0] != "mac-1" {
			t.Fatalf("15s fallback picked %v, want [mac-1]", starts)
		}
	})

	t.Run("no-matching-machine-stays-queued-until-30m-timeout", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
			"tasks":[{
				"title":"Windows only job",
				"prompt":"Run Windows build",
				"cwd":"~/repo",
				"role":"worker",
				"readonly":true,
				"goal":"Build on Windows",
				"user_requested":true,
				"why":"Needs Windows",
				"placement_constraints":{"os":"windows"}
			}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		planID := extractPlanID(t, res)
		e.s.sweepPendingPlacement(ctx)
		p, _ := e.s.loadPlan(ctx, planID)
		todoID := p.Tasks[0].TodoID
		if p.Tasks[0].Queue == nil || p.Tasks[0].Queue.Key != pendingPlacementQueueKey || p.Tasks[0].Queue.Attempt != 1 {
			t.Fatalf("task with no matching machine must stay queued: %+v", p.Tasks[0].Queue)
		}
		if todo := todoNow(e.s, todoID); todo.DoneAt != nil || todo.StartError != "" {
			t.Fatalf("task should remain open before 30m timeout: %+v", todo)
		}
		// Advance to 30m (admissionQueueMaxWait) -> sweep fails the queued start.
		e.advance(admissionQueueMaxWait)
		e.s.sweepPendingPlacement(ctx)
		p, _ = e.s.loadPlan(ctx, planID)
		if p.Tasks[0].Queue != nil || p.Tasks[0].Error == "" {
			t.Fatalf("task should fail at 30m timeout: queue=%+v err=%q", p.Tasks[0].Queue, p.Tasks[0].Error)
		}
		if todo := todoNow(e.s, todoID); todo.StartError == "" {
			t.Fatalf("todo should record StartError at 30m timeout: %+v", todo)
		}
	})
}

func (e *placementTestEnv) setDevicesOnline(online map[string]bool) {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	for i := range e.f.devices {
		on := online[e.f.devices[i].ID]
		e.f.devices[i].Online = on
		e.f.devices[i].Reachable = on
	}
}

func TestPlacementFallbackUsesHubWhenNoEligibleNonHubOnline(t *testing.T) {
	t.Run("hub-only-online-remote-offline", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		e.setDevicesOnline(map[string]bool{"hub-1": true})

		res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
			"tasks":[{
				"title":"Hub only auto placement",
				"prompt":"Run task on the only online computer",
				"cwd":"~/repo",
				"role":"worker",
				"readonly":true,
				"goal":"Finish check",
				"user_requested":true,
				"why":"Single online hub computer"
			}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		planID := extractPlanID(t, res)
		e.s.sweepPendingPlacement(ctx)
		waitFor(t, "hub fallback starts on hub-1", func() bool {
			return len(taskStartMachines(e.f.fakeHub)) == 1 && e.s.taskStarting.Load() == 0
		})
		if starts := taskStartMachines(e.f.fakeHub); starts[0] != "hub-1" {
			t.Fatalf("started on %v, want [hub-1]", starts)
		}
		p, err := e.s.loadPlan(ctx, planID)
		if err != nil {
			t.Fatal(err)
		}
		if p.Tasks[0].Queue != nil || p.Tasks[0].Machine != "hub-1" {
			t.Fatalf("plan task after hub fallback = %+v", p.Tasks[0])
		}
		if p.Tasks[0].Placement == nil || p.Tasks[0].Placement.Choice != "hub-1" || p.Tasks[0].Placement.ReasonKey != placementReasonFallback {
			t.Fatalf("placement audit = %+v", p.Tasks[0].Placement)
		}
		logs := e.logsText()
		for _, want := range []string{"placement queue: enqueued", "placement queue: assigned to hub-1"} {
			if !strings.Contains(logs, want) {
				t.Errorf("logs missing %q:\n%s", want, logs)
			}
		}
	})

	t.Run("hub-fallback-when-only-non-hub-fails-hard-constraints", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		e.setDevicesOnline(map[string]bool{"hub-1": true, "mac-1": true})

		_, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
			"tasks":[{
				"title":"Linux task on hub with darwin worker online",
				"prompt":"Run linux build",
				"cwd":"~/repo",
				"role":"worker",
				"readonly":true,
				"goal":"Build on linux",
				"user_requested":true,
				"why":"Needs linux",
				"placement_constraints":{"os":"linux"}
			}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		e.s.sweepPendingPlacement(ctx)
		waitFor(t, "linux task falls back to hub-1", func() bool {
			return len(taskStartMachines(e.f.fakeHub)) == 1 && e.s.taskStarting.Load() == 0
		})
		if starts := taskStartMachines(e.f.fakeHub); starts[0] != "hub-1" {
			t.Fatalf("started on %v, want [hub-1]", starts)
		}
	})

	t.Run("hub-fallback-respects-candidate-machines", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		e.setDevicesOnline(map[string]bool{"hub-1": true})

		res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
			"tasks":[{
				"title":"Pinned to offline worker",
				"prompt":"Wait for worker",
				"cwd":"~/repo",
				"role":"worker",
				"readonly":true,
				"goal":"Run on worker",
				"user_requested":true,
				"why":"Must run on linux-idle",
				"placement_constraints":{"candidate_machines":["linux-idle"]}
			}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		planID := extractPlanID(t, res)
		e.s.sweepPendingPlacement(ctx)
		if starts := taskStartMachines(e.f.fakeHub); len(starts) != 0 {
			t.Fatalf("hub not in candidate_machines must not start: %v", starts)
		}
		p, _ := e.s.loadPlan(ctx, planID)
		if p.Tasks[0].Queue == nil || !strings.Contains(p.Tasks[0].Queue.Reason, "candidate_machines") {
			t.Fatalf("queue reason should name candidate_machines constraint: %+v", p.Tasks[0].Queue)
		}
	})

	t.Run("extension-assign-allows-hub-when-no-eligible-non-hub-online", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		e.setDevicesOnline(map[string]bool{"hub-1": true})
		_ = e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "sched-ext", "")

		res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
			"tasks":[{
				"title":"Extension hub fallback",
				"prompt":"Run via scheduler on hub",
				"cwd":"~/repo",
				"role":"worker",
				"readonly":true,
				"goal":"Confirm hub assign",
				"user_requested":true,
				"why":"Single online hub"
			}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		planID := extractPlanID(t, res)
		p, _ := e.s.loadPlan(ctx, planID)
		todoID := p.Tasks[0].TodoID
		rec := e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/assign", roleExtension, "sched-ext", `{"machine":"hub-1","reason":"only online computer","version":1}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("hub assign with no online non-hub = %d %s, want 200", rec.Code, rec.Body.String())
		}
		waitFor(t, "assigned hub task starts", func() bool {
			return len(taskStartMachines(e.f.fakeHub)) == 1 && e.s.taskStarting.Load() == 0
		})
		if starts := taskStartMachines(e.f.fakeHub); starts[0] != "hub-1" {
			t.Fatalf("started on %v, want [hub-1]", starts)
		}
	})
}

func TestPlacementDiagnosticReasonAndGiveUpLogNameGateAndNumbers(t *testing.T) {
	t.Run("overloaded-non-hub-keeps-hub-protected-and-reports-exact-numbers", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		e.f.hello.Locale = "en"
		e.setDevicesOnline(map[string]bool{"hub-1": true, "linux-idle": true, "linux-heavy": true})
		e.d.samples["linux-idle"] = admissionSample(99.0, 32<<30, 8<<30, 0)
		e.d.samples["linux-heavy"] = admissionSample(0.5, 32<<30, 8<<30, 40)

		res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
			"tasks":[{
				"title":"Busy linux fleet",
				"prompt":"Wait for linux capacity",
				"cwd":"~/repo",
				"role":"worker",
				"readonly":true,
				"goal":"Check capacity",
				"user_requested":true,
				"why":"Capacity test",
				"placement_constraints":{"os":"linux"}
			}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		planID := extractPlanID(t, res)
		e.s.sweepPendingPlacement(ctx)

		if starts := taskStartMachines(e.f.fakeHub); len(starts) != 0 {
			t.Fatalf("hub must stay protected when matching non-hub workers are online: %v", starts)
		}
		p, _ := e.s.loadPlan(ctx, planID)
		todoID := p.Tasks[0].TodoID
		if p.Tasks[0].Queue == nil || p.Tasks[0].Queue.Attempt != 1 {
			t.Fatalf("expected queued attempt 1, got %+v", p.Tasks[0].Queue)
		}
		for _, want := range []string{"Linux Idle", "99.0", "8", "Linux Heavy", "40", "Hub", "require_hub=false"} {
			if !strings.Contains(p.Tasks[0].Queue.Reason, want) {
				t.Errorf("queue reason missing %q: %s", want, p.Tasks[0].Queue.Reason)
			}
		}

		// Task card displays the concrete blocking gate and numbers while waiting.
		cards, err := e.s.taskCards(ctx, []FeedItem{{Kind: feedPlan, Ref: planID, At: e.clockNow()}}, map[string]*TaskPlan{planID: p})
		if err != nil {
			t.Fatal(err)
		}
		var cardReason string
		for _, c := range cards {
			if c.TodoID == todoID {
				cardReason = c.Reason
			}
		}
		if !strings.Contains(cardReason, "99.0") {
			t.Fatalf("waiting task card reason missing concrete load reading: %q", cardReason)
		}

		// Unrelated wakes before pendingPlacementRetryInterval must not hammer retries.
		e.advance(10 * time.Second)
		e.s.sweepPendingPlacement(ctx)
		p, _ = e.s.loadPlan(ctx, planID)
		if p.Tasks[0].Queue.Attempt != 1 {
			t.Fatalf("sweep before retry interval ran attempt %d, want 1", p.Tasks[0].Queue.Attempt)
		}

		// Timeout after 30m writes gate+numbers into StartError and the give-up log.
		e.advance(admissionQueueMaxWait)
		e.s.sweepPendingPlacement(ctx)
		todo := todoNow(e.s, todoID)
		if todo.DoneAt == nil || !strings.Contains(todo.StartError, "99.0") || !strings.Contains(todo.StartError, "40") {
			t.Fatalf("timed-out StartError missing concrete readings: %+v", todo)
		}
		logs := e.logsText()
		for _, want := range []string{
			"placement queue (attempt 1):",
			"assistant-tasks.admission_load(cores=8,load=99.0",
			"admission queue: gave up after 1 attempt(s) (queue=pending_placement):",
			"99.0",
		} {
			if !strings.Contains(logs, want) {
				t.Errorf("logs missing %q:\n%s", want, logs)
			}
		}
	})

	t.Run("hub-only-hits-hub-procs-limit-and-reports-count-and-limit", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		e.f.hello.Locale = "en"
		e.setDevicesOnline(map[string]bool{"hub-1": true})
		e.d.samples["hub-1"] = admissionSample(0.2, 32<<30, 8<<30, admissionHubMaxClaudeProcs)

		res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
			"tasks":[{
				"title":"Hub procs full",
				"prompt":"Wait for hub slot",
				"cwd":"~/repo",
				"role":"worker",
				"readonly":true,
				"goal":"Run when free",
				"user_requested":true,
				"why":"Hub admission check"
			}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		planID := extractPlanID(t, res)
		e.s.sweepPendingPlacement(ctx)
		if starts := taskStartMachines(e.f.fakeHub); len(starts) != 0 {
			t.Fatalf("hub over hub_max_claude_procs must not start: %v", starts)
		}
		p, _ := e.s.loadPlan(ctx, planID)
		if p.Tasks[0].Queue == nil {
			t.Fatal("expected task to stay queued")
		}
		if strings.Contains(p.Tasks[0].Queue.Reason, "require_hub=false") {
			t.Fatalf("hub-only failure should report actual hub gate, not hub_excluded: %s", p.Tasks[0].Queue.Reason)
		}
		wantProcs := fmt.Sprintf("%d", admissionHubMaxClaudeProcs)
		if !strings.Contains(p.Tasks[0].Queue.Reason, wantProcs) {
			t.Fatalf("queue reason missing hub procs reading %s: %s", wantProcs, p.Tasks[0].Queue.Reason)
		}
		logs := e.logsText()
		if !strings.Contains(logs, "assistant-tasks.admission_procs(count=20,limit=20)") {
			t.Fatalf("logs missing structured hub procs hit:\n%s", logs)
		}
	})

	t.Run("hub-only-hits-direction-cap-and-reports-running-and-limit", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		e.f.hello.Locale = "en"
		e.setDevicesOnline(map[string]bool{"hub-1": true})
		on := true
		cap1 := 1
		e.s.admission.cached = &AdmissionSettings{Enabled: &on, DirectionCap: &cap1}
		e.s.admission.directionClaims["conv_pub"] = 1

		res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
			"tasks":[{
				"title":"Direction cap blocked",
				"prompt":"Wait for direction slot",
				"cwd":"~/repo",
				"role":"worker",
				"readonly":true,
				"goal":"Run when slot opens",
				"user_requested":true,
				"why":"Direction cap diagnostic check"
			}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		planID := extractPlanID(t, res)
		e.s.sweepPendingPlacement(ctx)
		if starts := taskStartMachines(e.f.fakeHub); len(starts) != 0 {
			t.Fatalf("direction cap exceeded must not start: %v", starts)
		}
		p, _ := e.s.loadPlan(ctx, planID)
		if p.Tasks[0].Queue == nil || !strings.Contains(p.Tasks[0].Queue.Reason, "1 agents running, at its limit of 1") {
			t.Fatalf("queue reason should name direction cap running/limit: %+v", p.Tasks[0].Queue)
		}
		logs := e.logsText()
		if !strings.Contains(logs, "assign hub-1 failed:") || !strings.Contains(logs, "limit of 1") {
			t.Fatalf("logs missing direction cap assignment failure:\n%s", logs)
		}
	})
}

func TestPlacementTaskRetryReenqueuesLogsAndGuardsDuplicateRemedy(t *testing.T) {
	e := newPlacementTestEnv(t)
	ctx := context.Background()
	e.f.hello.Locale = "en"
	e.setDevicesOnline(map[string]bool{"hub-1": true})
	e.d.samples["hub-1"] = admissionSample(0.2, 32<<30, 8<<30, admissionHubMaxClaudeProcs)

	res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
		"tasks":[{
			"title":"Retryable auto-placed task",
			"prompt":"Retry after capacity frees",
			"cwd":"~/repo",
			"launcher":"claude",
			"role":"worker",
			"readonly":true,
			"goal":"Run after retry",
			"user_requested":true,
			"why":"Retry test"
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	planID := extractPlanID(t, res)
	e.s.sweepPendingPlacement(ctx)
	e.advance(admissionQueueMaxWait)
	e.s.sweepPendingPlacement(ctx)

	p, _ := e.s.loadPlan(ctx, planID)
	todoID := p.Tasks[0].TodoID
	if todo := todoNow(e.s, todoID); todo.DoneAt == nil || !todo.failedStart() {
		t.Fatalf("precondition failed: todo must be failedStart after timeout: %+v", todo)
	}

	e.clearLogs()
	retryArgs, _ := json.Marshal(map[string]string{
		"todo_id":  todoID,
		"reason":   "capacity recovered; switch launcher",
		"launcher": "codex",
	})
	out, err := e.s.toolTaskRetry(ctx, "conv_pub", retryArgs)
	if err != nil {
		t.Fatalf("toolTaskRetry on pending_placement failed: %v", err)
	}
	if !strings.Contains(out, "starting again") || !strings.Contains(out, "codex") {
		t.Fatalf("unexpected toolTaskRetry output: %s", out)
	}
	p, _ = e.s.loadPlan(ctx, planID)
	if p.Tasks[0].Queue == nil || p.Tasks[0].Queue.Key != pendingPlacementQueueKey || !p.Tasks[0].Queue.Retry {
		t.Fatalf("task_retry must re-enqueue pending_placement: %+v", p.Tasks[0].Queue)
	}
	if p.Tasks[0].Launcher != "codex" || p.Tasks[0].Machine != "" {
		t.Fatalf("task_retry launcher override not persisted: launcher=%q machine=%q", p.Tasks[0].Launcher, p.Tasks[0].Machine)
	}
	if todo := todoNow(e.s, todoID); todo.DoneAt != nil || todo.StartError != "" {
		t.Fatalf("task_retry must reopen failed todo: %+v", todo)
	}
	logs := e.logsText()
	for _, want := range []string{
		fmt.Sprintf("task %s: retrying its start via placement queue (plan %s)", todoID, planID),
		fmt.Sprintf("task %s: placement queue: enqueued (plan %s", todoID, planID),
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("retry logs missing %q:\n%s", want, logs)
		}
	}

	// A second task_retry while the first retry is still queued in remedying must refuse cleanly and keep Queue intact.
	if _, err := e.s.toolTaskRetry(ctx, "conv_pub", retryArgs); err == nil || !strings.Contains(err.Error(), "already queued") {
		t.Fatalf("duplicate task_retry while queued = %v, want already queued error", err)
	}
	p, _ = e.s.loadPlan(ctx, planID)
	if p.Tasks[0].Queue == nil || p.Tasks[0].Queue.Key != pendingPlacementQueueKey {
		t.Fatalf("duplicate task_retry destroyed pending_placement queue: %+v", p.Tasks[0].Queue)
	}

	// Once capacity frees, sweep launches the retried task on hub-1 with the overridden launcher.
	e.d.samples["hub-1"] = admissionSample(0.2, 32<<30, 8<<30, 0)
	e.s.sweepPendingPlacement(ctx)
	waitFor(t, "retried task starts on hub-1", func() bool {
		return len(taskStartMachines(e.f.fakeHub)) == 1 && e.s.taskStarting.Load() == 0
	})
	if starts := taskStartMachines(e.f.fakeHub); starts[0] != "hub-1" {
		t.Fatalf("retried task started on %v, want [hub-1]", starts)
	}
	p, _ = e.s.loadPlan(ctx, planID)
	if p.Tasks[0].Queue != nil || p.Tasks[0].Machine != "hub-1" || p.Tasks[0].Launcher != "codex" {
		t.Fatalf("retried task final state = %+v", p.Tasks[0])
	}
}

// placementAuditHitKeys returns the filter hit keys recorded on machine in the task's placement audit.
func placementAuditHitKeys(t *testing.T, p *placement, machine string) []string {
	t.Helper()
	if p == nil {
		t.Fatal("no placement audit on task")
	}
	for _, c := range p.Candidates {
		if c.Machine != machine {
			continue
		}
		keys := make([]string, 0, len(c.Hits))
		for _, h := range c.Hits {
			keys = append(keys, h.Key)
		}
		return keys
	}
	t.Fatalf("machine %s not recorded in placement audit %+v", machine, p.Candidates)
	return nil
}

// queuedFallbackTask builds a deferred task (no machine) constrained to linux machines. fields adds caller choices
// such as a tier; the audit and assertions stay in the test itself.
func queuedFallbackTask(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	task := map[string]any{
		"title":                 "Fallback launcher check",
		"prompt":                "Run the job",
		"cwd":                   "~/repo",
		"role":                  "worker",
		"readonly":              true,
		"goal":                  "Placement regression",
		"user_requested":        true,
		"why":                   "15953 regression",
		"placement_constraints": map[string]any{"os": "linux"},
	}
	for k, v := range fields {
		task[k] = v
	}
	raw, err := json.Marshal(map[string]any{"tasks": []map[string]any{task}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func createFallbackPlan(t *testing.T, e *placementTestEnv, args json.RawMessage) string {
	t.Helper()
	res, err := e.s.toolTaskPlan(context.Background(), "conv_pub", args)
	if err != nil {
		t.Fatal(err)
	}
	return extractPlanID(t, res)
}

// failingUsageLimit is a fresh usage-limit health record: unusable without an on-the-spot probe, so the test stays
// deterministic (launcherUnusableNow returns it immediately within healthRecheckThrottle).
func failingUsageLimit(machine, launcher string) *LauncherHealth {
	return &LauncherHealth{
		Machine: machine, Launcher: launcher, State: healthFailing, Kind: errKindUsageLimit,
		Error: "usage limit reached", CheckedAt: time.Now(),
	}
}

// failingOther is a fresh non-usage-limit failure: launcherUnusableNow re-probes it on the spot, so the test can
// exercise the stale-record recovery path deterministically through the fake node's health check answer.
func failingOther(machine, launcher string) *LauncherHealth {
	return &LauncherHealth{
		Machine: machine, Launcher: launcher, State: healthFailing, Kind: errKindOther,
		Error: "something broke", CheckedAt: time.Now(),
	}
}

// TestPlacementFallbackChecksResolvedLauncherHealthAndPresence covers #15953: when a deferred task names no launcher,
// the kernel fallback must resolve the effective launcher (default agent.new / claude, or the per-machine tier
// candidate) on every candidate and enforce launcherOn + launcherUnusable there, instead of recommending a computer
// whose launcher is missing or unhealthy and livelocking on the same failure until the 30m deadline.
func TestPlacementFallbackChecksResolvedLauncherHealthAndPresence(t *testing.T) {
	t.Run("unhealthy default launcher skips lowest load and picks healthy machine", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		// pt.Launcher == "" resolves to claude; the lowest-load linux-idle has it failing.
		e.s.health.recs = map[string]*LauncherHealth{"linux-idle/claude": failingUsageLimit("linux-idle", "claude")}
		planID := createFallbackPlan(t, e, queuedFallbackTask(t, nil))

		e.s.sweepPendingPlacement(ctx)
		waitFor(t, "fallback starts on the healthy machine", func() bool { return len(taskStartMachines(e.f.fakeHub)) == 1 })
		if starts := taskStartMachines(e.f.fakeHub); starts[0] != "linux-heavy" {
			t.Fatalf("fallback picked %v, want [linux-heavy]", starts)
		}
		p, _ := e.s.loadPlan(ctx, planID)
		if p.Tasks[0].Queue != nil {
			t.Fatalf("task must leave the queue, still queued: %+v", p.Tasks[0].Queue)
		}
		if got := placementAuditHitKeys(t, p.Tasks[0].Placement, "linux-idle"); !slices.Contains(got, "launcher_unhealthy") {
			t.Fatalf("linux-idle audit hits = %v, want launcher_unhealthy", got)
		}
	})

	t.Run("missing configured agent.new launcher skips lowest load and picks machine that has it", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		setModel(t, e.s, "agent.new", ModelBinding{Provider: "launcher.codex"})
		// linux-idle no longer lists codex; the other machines still use the fixture's [codex, claude] list.
		e.f.zshrc = map[string][]proto.Launcher{"linux-idle": {{Name: "claude"}}}
		planID := createFallbackPlan(t, e, queuedFallbackTask(t, nil))

		e.s.sweepPendingPlacement(ctx)
		waitFor(t, "fallback starts where the default launcher exists", func() bool { return len(taskStartMachines(e.f.fakeHub)) == 1 })
		if starts := taskStartMachines(e.f.fakeHub); starts[0] != "linux-heavy" {
			t.Fatalf("fallback picked %v, want [linux-heavy]", starts)
		}
		p, _ := e.s.loadPlan(ctx, planID)
		if got := placementAuditHitKeys(t, p.Tasks[0].Placement, "linux-idle"); !slices.Contains(got, "launcher_missing") {
			t.Fatalf("linux-idle audit hits = %v, want launcher_missing", got)
		}
	})

	t.Run("unhealthy tier launcher skips lowest load on the per-machine tier resolution", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		saveTier(t, e.s, "dev", ModelTier{Launcher: "codex", Model: "m1"})
		e.s.health.recs = map[string]*LauncherHealth{"linux-idle/codex": failingUsageLimit("linux-idle", "codex")}
		planID := createFallbackPlan(t, e, queuedFallbackTask(t, map[string]any{"tier": "dev"}))

		e.s.sweepPendingPlacement(ctx)
		waitFor(t, "fallback starts on a machine with a healthy tier launcher", func() bool { return len(taskStartMachines(e.f.fakeHub)) == 1 })
		if starts := taskStartMachines(e.f.fakeHub); starts[0] != "linux-heavy" {
			t.Fatalf("fallback picked %v, want [linux-heavy]", starts)
		}
		p, _ := e.s.loadPlan(ctx, planID)
		if got := placementAuditHitKeys(t, p.Tasks[0].Placement, "linux-idle"); !slices.Contains(got, "launcher_unhealthy") {
			t.Fatalf("linux-idle audit hits = %v, want launcher_unhealthy", got)
		}
	})

	t.Run("healthy tier candidate accepted even when the default launcher is unhealthy there", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		saveTier(t, e.s, "dev", ModelTier{
			Launcher: "codex", Model: "m1",
			Candidates: []TierCandidate{{Launcher: "codex", Model: "m1"}, {Launcher: "claude", Model: "m2"}},
		})
		// The default claude launcher fails on the lowest-load machine, but the tier's first candidate codex is fine:
		// filtering the default launcher instead of the tier-resolved one would wrongly skip this machine.
		e.s.health.recs = map[string]*LauncherHealth{"linux-idle/claude": failingUsageLimit("linux-idle", "claude")}
		planID := createFallbackPlan(t, e, queuedFallbackTask(t, map[string]any{"tier": "dev"}))

		e.s.sweepPendingPlacement(ctx)
		waitFor(t, "fallback starts on the lowest-load machine", func() bool { return len(taskStartMachines(e.f.fakeHub)) == 1 })
		if starts := taskStartMachines(e.f.fakeHub); starts[0] != "linux-idle" {
			t.Fatalf("fallback picked %v, want [linux-idle]", starts)
		}
		p, _ := e.s.loadPlan(ctx, planID)
		got := placementAuditHitKeys(t, p.Tasks[0].Placement, "linux-idle")
		if slices.Contains(got, "launcher_unhealthy") || slices.Contains(got, "launcher_missing") {
			t.Fatalf("linux-idle audit hits = %v, want no launcher failure", got)
		}
	})

	t.Run("missing default launcher on every machine still starts through the assignment swap", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		setModel(t, e.s, "agent.new", ModelBinding{Provider: "launcher.codex"})
		// No candidate machine offers codex: fitLauncher swaps a missing default launcher rather than refusing, so
		// the fallback must not starve the task at the queue deadline (it keeps preferring machines that offer it).
		e.f.zshrc = map[string][]proto.Launcher{
			"linux-idle":  {{Name: "claude"}},
			"linux-heavy": {{Name: "claude"}},
		}
		planID := createFallbackPlan(t, e, queuedFallbackTask(t, nil))

		e.s.sweepPendingPlacement(ctx)
		waitFor(t, "task starts despite the missing default launcher", func() bool { return len(taskStartMachines(e.f.fakeHub)) == 1 })
		if starts := taskStartMachines(e.f.fakeHub); starts[0] != "linux-idle" {
			t.Fatalf("fallback picked %v, want [linux-idle]", starts)
		}
		p, _ := e.s.loadPlan(ctx, planID)
		if p.Tasks[0].Queue != nil {
			t.Fatalf("task must not starve in the queue: %+v", p.Tasks[0].Queue)
		}
	})

	t.Run("stale failing launcher record is probed on the spot and cleared", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		// The fake node answers a passing launchers.check, so launcherUnusableNow clears the stale record instead of
		// requeuing a task this healthy machine can start.
		e.s.health.recs = map[string]*LauncherHealth{"linux-idle/claude": failingOther("linux-idle", "claude")}
		_ = createFallbackPlan(t, e, queuedFallbackTask(t, nil))

		e.s.sweepPendingPlacement(ctx)
		waitFor(t, "fallback starts on the probed machine", func() bool { return len(taskStartMachines(e.f.fakeHub)) == 1 })
		if starts := taskStartMachines(e.f.fakeHub); starts[0] != "linux-idle" {
			t.Fatalf("fallback picked %v, want [linux-idle]", starts)
		}
		if !slices.Contains(e.f.fakeHub.checked, "linux-idle/claude") {
			t.Fatalf("no on-the-spot health probe ran for linux-idle/claude: %v", e.f.fakeHub.checked)
		}
	})

	t.Run("all machines unusable keeps the safe queue-and-retry behavior without starting", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		e.s.health.recs = map[string]*LauncherHealth{
			"linux-idle/claude":  failingUsageLimit("linux-idle", "claude"),
			"linux-heavy/claude": failingUsageLimit("linux-heavy", "claude"),
		}
		planID := createFallbackPlan(t, e, queuedFallbackTask(t, nil))

		e.s.sweepPendingPlacement(ctx)
		if starts := taskStartMachines(e.f.fakeHub); len(starts) != 0 {
			t.Fatalf("no machine is usable, but started on %v", starts)
		}
		p, _ := e.s.loadPlan(ctx, planID)
		if p.Tasks[0].Queue == nil || p.Tasks[0].Queue.Key != pendingPlacementQueueKey || p.Tasks[0].Queue.Attempt != 1 {
			t.Fatalf("task with no usable machine must stay queued for retry: %+v", p.Tasks[0].Queue)
		}
		if p.Tasks[0].Queue.Reason == "" {
			t.Fatal("queued task must record why no machine matched")
		}
		for _, machine := range []string{"linux-idle", "linux-heavy"} {
			if got := placementAuditHitKeys(t, p.Tasks[0].Placement, machine); !slices.Contains(got, "launcher_unhealthy") {
				t.Fatalf("%s audit hits = %v, want launcher_unhealthy", machine, got)
			}
		}
	})
}

// TestPlacementAssignUnhealthyDefaultLauncherNamesResolvedLauncher pins the fixed_launcher_unhealthy wording for a
// task without an explicit launcher: the args carry the resolved launcher name instead of an empty field.
func TestPlacementAssignUnhealthyDefaultLauncherNamesResolvedLauncher(t *testing.T) {
	e := newPlacementTestEnv(t)
	ctx := context.Background()
	_ = e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "sched-ext", "")
	planID := createFallbackPlan(t, e, queuedFallbackTask(t, nil))
	p, _ := e.s.loadPlan(ctx, planID)
	todoID := p.Tasks[0].TodoID
	e.s.health.recs = map[string]*LauncherHealth{"linux-idle/claude": failingUsageLimit("linux-idle", "claude")}

	rec := e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/assign", roleExtension, "sched-ext",
		`{"machine":"linux-idle","reason":"unhealthy default","version":1}`)
	body := rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "launcher_unavailable") {
		t.Fatalf("assign = %d %s, want 422 launcher_unavailable", rec.Code, body)
	}
	if !strings.Contains(body, "claude") || strings.Contains(body, "Launcher  is") {
		t.Fatalf("error must name the resolved claude launcher, got %q", body)
	}
	if starts := taskStartMachines(e.f.fakeHub); len(starts) != 0 {
		t.Fatalf("refused assignment must not start an agent: %v", starts)
	}
}
