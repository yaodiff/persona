package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/repositorywire"
)

func TestPlanStartTimeout(t *testing.T) {
	ordinary := PlanTask{}
	if got := ordinary.planStartTimeout(); got != taskStartTimeout {
		t.Errorf("ordinary task timeout = %v, want %v", got, taskStartTimeout)
	}
	minimum := 2*repoMirrorCallTimeout + 3*5*time.Minute + taskStartTimeout
	workspaces := map[string]PlanTask{
		"resolved": {WorkspaceResolved: &TaskWorkspaceResolved{Repositories: make([]BackendLaunchRepository, 2)}},
		"selected": {Workspace: &TaskWorkspace{
			Mode:      workspaceContainer,
			Container: &ContainerWorkspaceSelection{SelectedRepoIDs: []string{"repo_web", "repo_api"}},
		}},
	}
	for name, task := range workspaces {
		if got := task.planStartTimeout(); got <= minimum {
			t.Errorf("%s workspace timeout = %v, want more than %v for two mirrors, workspace RPCs and the agent start", name, got, minimum)
		}
	}
	saved := PlanTask{Workspace: &TaskWorkspace{
		Mode:    workspaceBackend,
		Backend: &BackendWorkspaceSelection{OperationID: "op_saved"},
	}}
	minimum = repositorywire.MaxTaskRepositories*repoMirrorCallTimeout + 3*5*time.Minute + taskStartTimeout
	if got := saved.planStartTimeout(); got <= minimum {
		t.Errorf("unresolved saved workspace timeout = %v, want more than %v for the full repository limit", got, minimum)
	}
	saved.WorkspaceResolved = &TaskWorkspaceResolved{OperationID: "op_saved"}
	if got := saved.planStartTimeout(); got != taskStartTimeout {
		t.Errorf("resolved empty workspace timeout = %v, want %v", got, taskStartTimeout)
	}
}

// ctxAwareLinkStore is a Store + todoStore whose agent links refuse a spent context, as pgx does: its first pool
// Acquire on a cancelled or deadline-exceeded context fails at once. Only the methods the task-start path calls are
// real; the embedded nil todoStore panics on anything else, so an unexpected call fails the test loudly.
type ctxAwareLinkStore struct {
	*profileTaskStore
	todoStore
	mu          sync.Mutex
	rows        []db.TodoRow
	linked      int
	failSession string
}

func newCtxAwareLinkStore() *ctxAwareLinkStore {
	return &ctxAwareLinkStore{profileTaskStore: &profileTaskStore{
		memStore: &memStore{vals: map[string][]byte{}}, plans: map[string]db.TaskPlanRow{},
	}}
}

func (st *ctxAwareLinkStore) ListTodos(context.Context) ([]db.TodoRow, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]db.TodoRow(nil), st.rows...), nil
}
func (st *ctxAwareLinkStore) ListAllTodoSpaces(context.Context) ([]db.TodoSpaceRow, error) {
	return nil, nil
}
func (st *ctxAwareLinkStore) ListAllTodoAgents(context.Context) ([]db.TodoAgentRow, error) {
	return nil, nil
}
func (st *ctxAwareLinkStore) ListTodoThreads(context.Context) (map[string]db.TodoThread, error) {
	return map[string]db.TodoThread{}, nil
}
func (st *ctxAwareLinkStore) ListTodoSources(context.Context) (map[string]db.TodoSource, error) {
	return map[string]db.TodoSource{}, nil
}
func (st *ctxAwareLinkStore) ListTodoDecisions(context.Context, string, bool) ([]db.TodoDecisionRow, error) {
	return nil, nil
}
func (st *ctxAwareLinkStore) CreateTodo(_ context.Context, t db.TodoRow) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.rows = append(st.rows, t)
	return nil
}
func (st *ctxAwareLinkStore) link(ctx context.Context, sessionID string) ([]string, error) {
	if sessionID == st.failSession {
		st.mu.Lock()
		st.linked++
		st.mu.Unlock()
		return nil, errors.New("injected exact-session link failure")
	}
	if err := ctx.Err(); err != nil {
		return nil, err // what pgx answers when the start's own context is already spent
	}
	st.mu.Lock()
	st.linked++
	st.mu.Unlock()
	return nil, nil
}
func (st *ctxAwareLinkStore) LinkTodoAgentWithSource(ctx context.Context, _, _, sessionID, _, _, _, _, _, _ string, _ time.Time) ([]string, error) {
	return st.link(ctx, sessionID)
}
func (st *ctxAwareLinkStore) LinkTodoAgentOfDoneTask(ctx context.Context, _, _, sessionID, _, _, _, _, _, _ string, _ time.Time) ([]string, error) {
	return st.link(ctx, sessionID)
}
func (st *ctxAwareLinkStore) linkCount() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.linked
}

// shortTaskStartReconcile makes a task start time out in milliseconds and gives the reconciliation that follows a
// short, quick window instead of the production bounds.
func shortTaskStartReconcile(t *testing.T) {
	t.Helper()
	oldStart, oldWait, oldPoll, oldListTimeout := taskStartTimeout, taskStartAdoptWait, taskStartAdoptPoll, agentStartListTimeout
	taskStartTimeout, taskStartAdoptWait, taskStartAdoptPoll, agentStartListTimeout =
		100*time.Millisecond, time.Second, 15*time.Millisecond, 100*time.Millisecond
	shortAgentStartTimeout(t) // adoptStartedSession's own between-list pause as well
	t.Cleanup(func() {
		taskStartTimeout, taskStartAdoptWait, taskStartAdoptPoll, agentStartListTimeout = oldStart, oldWait, oldPoll, oldListTimeout
	})
}

// shortTaskStartAdopt shortens only the search for the session of a start that did not answer (one second, quick polls),
// for a test whose fake fails the start at once. The start keeps its budget: what it reads before it asks the node (the
// plan, the model configuration in Postgres) is not squeezed into milliseconds (#14158).
func shortTaskStartAdopt(t *testing.T) {
	t.Helper()
	oldWait, oldPoll := taskStartAdoptWait, taskStartAdoptPoll
	taskStartAdoptWait, taskStartAdoptPoll = time.Second, 15*time.Millisecond
	t.Cleanup(func() { taskStartAdoptWait, taskStartAdoptPoll = oldWait, oldPoll })
}

// reconcileHub is a fake hub with the given machines (no sessions yet) whose session.start always spends the
// caller's wait, like a node whose link is stuck while it builds the tab.
func reconcileHub(t *testing.T, machines ...string) (*Service, *fakeHub) {
	t.Helper()
	shortTaskStartReconcile(t)
	f := newFakeHub()
	devs := make([]Device, 0, len(machines))
	for _, id := range machines {
		f.sessions[id] = nil
		devs = append(devs, Device{ID: id, Name: id, Home: "/home/me", Online: true, Reachable: true})
	}
	f.devices = devs
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	f.slowStart = true
	return newStorelessService(t, f), f
}

func startReconcileTask(t *testing.T, s *Service, machine, policy string) Todo {
	t.Helper()
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "超时对账", "把活干完", "conv1", "plan_x")
	if err != nil {
		t.Fatal(err)
	}
	s.startPlanTask("plan_x", "conv1", 0, PlanTask{
		Title: todo.Title, Prompt: "把活干完", Machine: machine, Cwd: "/code/app",
		Permission: "bypassPermissions", TodoID: todo.ID, MachinePolicy: policy,
	}, false)
	return todo
}

// lateSessionAfterTheWait has the node on machine show the task's session only after the hub stopped waiting for the
// start and listed the machine once without it: the session appears from the second sessions.list after
// session.start, as a tab the node creates after the hub's wait ran out (#6540). slowStart holds the fake until the
// start's context ends, so every list after the start comes after the wait.
func lateSessionAfterTheWait(f *fakeHub, machine string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	started, lists := false, 0
	f.onStart = func(*fakeHub, map[string]any) { started = true }
	f.onList = func(f *fakeHub, m string) {
		if m != machine || !started {
			return
		}
		if lists++; lists == 2 {
			f.sessions[machine] = []proto.Session{{
				SessionID: "s-late", MachineID: machine, Cwd: "/code/app", Agent: "claude", Launcher: "claude",
				StartedByPersona: true, Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now().UTC(),
			}}
		}
	}
}

// The node spends the hub's whole wait but creates the tab afterwards: the hub reconciles the session from the
// machine's own list and links it to the task — no failure, no receipt, no second start (#6540).
func TestTaskStartAdoptsTheSessionThatStartedLate(t *testing.T) {
	s, f := reconcileHub(t, "box")
	lateSessionAfterTheWait(f, "box")
	todo := startReconcileTask(t, s, "box", machineFixed)
	waitFor(t, "the late session to be linked", func() bool { return len(todoNow(s, todo.ID).Agents) == 1 })

	got := todoNow(s, todo.ID)
	if got.DoneAt != nil || got.StartError != "" {
		t.Fatalf("an adopted start was recorded as a failure: %+v", got)
	}
	if len(got.Agents) != 1 || got.Agents[0].MachineID != "box" || got.Agents[0].SessionID != "s-late" {
		t.Fatalf("the late session is not linked: %+v", got.Agents)
	}
	if starts := taskStartMachines(f); len(starts) != 1 || starts[0] != "box" {
		t.Fatalf("the start was attempted %v, want exactly one on box", starts)
	}
	if strings.Contains(feedKinds(s, "conv1"), "receipt") {
		t.Fatal("an adopted start produced a failure receipt")
	}
}

// An adopted late session is linked on a detached context: by the time the reconciliation succeeds the start's own
// context has spent taskStartTimeout, and a database (pgx) refuses every write on it, which would fail the task
// although its agent is running. The link store below rejects spent contexts like pgx does (#6540).
func TestTaskStartAdoptionLinksOnADetachedContext(t *testing.T) {
	shortTaskStartReconcile(t)
	f := newFakeHub()
	f.sessions["box"] = nil
	f.devices = []Device{{ID: "box", Name: "box", Home: "/home/me", Online: true, Reachable: true}}
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	f.slowStart = true
	st := newCtxAwareLinkStore()
	s := newServiceOn(t, f, st)
	ctx := context.Background()
	lateSessionAfterTheWait(f, "box")
	todo, err := s.createTodo(ctx, "超时对账", "把活干完", "conv1", "plan_x")
	if err != nil {
		t.Fatal(err)
	}
	s.startPlanTask("plan_x", "conv1", 0, PlanTask{
		Title: todo.Title, Prompt: "把活干完", Machine: "box", Cwd: "/code/app",
		Permission: "bypassPermissions", TodoID: todo.ID, MachinePolicy: machineFixed,
	}, false)
	waitFor(t, "the late session to be linked through the store", func() bool {
		return st.linkCount() == 1 && len(todoNow(s, todo.ID).Agents) == 1
	})

	got := todoNow(s, todo.ID)
	if got.DoneAt != nil || got.StartError != "" {
		t.Fatalf("the reconciled start was recorded as a failure on the spent context: %+v", got)
	}
	if got.Agents[0].SessionID != "s-late" {
		t.Fatalf("the late session is not linked: %+v", got.Agents)
	}
	if starts := taskStartMachines(f); len(starts) != 1 || starts[0] != "box" {
		t.Fatalf("the start was attempted %v, want exactly one on box", starts)
	}
	if strings.Contains(feedKinds(s, "conv1"), "receipt") {
		t.Fatal("an adopted start produced a failure receipt")
	}
}

// The wait ends and the machine keeps answering with no matching session: the task still fails, exactly once, with
// no start anywhere else.
func TestTaskStartUnknownAndNothingStartedStillFails(t *testing.T) {
	s, f := reconcileHub(t, "box")
	todo := startReconcileTask(t, s, "box", machineFixed)
	waitFor(t, "the task to end as a failed start", func() bool { return todoNow(s, todo.ID).DoneAt != nil })

	got := todoNow(s, todo.ID)
	if !strings.Contains(got.StartError, "deadline") || len(got.Agents) != 0 {
		t.Fatalf("nothing started but the task did not fail: %+v", got)
	}
	if starts := taskStartMachines(f); len(starts) != 1 || starts[0] != "box" {
		t.Fatalf("starts %v, want exactly one on box", starts)
	}
	if !strings.Contains(feedKinds(s, "conv1"), "receipt") {
		t.Fatal("the failed start has no receipt")
	}
}

// The machine cannot answer the reconciliation lists (the pre-start snapshot included): nothing can be confirmed,
// so the existing failure behavior stands, and nothing is started elsewhere.
func TestTaskStartUnknownWithUnreachableNodeKeepsFailure(t *testing.T) {
	s, f := reconcileHub(t, "box")
	f.mu.Lock()
	f.sessionsFail["box"] = 10000
	f.mu.Unlock()
	todo := startReconcileTask(t, s, "box", machineFixed)
	waitFor(t, "the task to end as a failed start", func() bool { return todoNow(s, todo.ID).DoneAt != nil })

	got := todoNow(s, todo.ID)
	if got.StartError == "" || len(got.Agents) != 0 {
		t.Fatalf("an unreachable machine reconciled a session: %+v", got)
	}
	if starts := taskStartMachines(f); len(starts) != 1 || starts[0] != "box" {
		t.Fatalf("starts %v, want exactly one on box", starts)
	}
}

// An unknown result never moves a flexible task to another computer, whether the late session is reconciled or not:
// the node can still be creating the first session, so a reroute would open a duplicate agent there (#6540).
func TestUnknownTaskStartNeverReroutesAFlexibleTask(t *testing.T) {
	t.Run("late session adopted on the fixed target", func(t *testing.T) {
		s, f := reconcileHub(t, "box", "alt")
		ctx := context.Background()
		// box carries a failing record (what used to justify the detour); alt is healthy and has the directory.
		s.recordHealth(ctx, "box", "claude", healthResult{err: "context deadline exceeded", source: healthSourceStart})
		s.recordHealth(ctx, "alt", "claude", healthResult{ok: true, source: healthSourceCheck})
		lateSessionAfterTheWait(f, "box")
		todo := startReconcileTask(t, s, "box", machineFlexible)
		waitFor(t, "the late session to be linked", func() bool { return len(todoNow(s, todo.ID).Agents) == 1 })

		got := todoNow(s, todo.ID)
		if got.DoneAt != nil || got.StartError != "" || len(got.Agents) != 1 || got.Agents[0].SessionID != "s-late" {
			t.Fatalf("the late flexible start was not adopted on box: %+v", got)
		}
		if starts := taskStartMachines(f); len(starts) != 1 || starts[0] != "box" {
			t.Fatalf("a flexible unknown start left its machine: %v", starts)
		}
	})

	t.Run("nothing confirmed still fails on the same machine", func(t *testing.T) {
		s, f := reconcileHub(t, "box", "alt")
		ctx := context.Background()
		s.recordHealth(ctx, "box", "claude", healthResult{err: "context deadline exceeded", source: healthSourceStart})
		s.recordHealth(ctx, "alt", "claude", healthResult{ok: true, source: healthSourceCheck})
		todo := startReconcileTask(t, s, "box", machineFlexible)
		waitFor(t, "the task to end as a failed start", func() bool { return todoNow(s, todo.ID).DoneAt != nil })

		if got := todoNow(s, todo.ID); got.StartError == "" || len(got.Agents) != 0 {
			t.Fatalf("the unconfirmed flexible start did not fail: %+v", got)
		}
		if starts := taskStartMachines(f); len(starts) != 1 || starts[0] != "box" {
			t.Fatalf("an unconfirmed flexible start was rerouted: %v", starts)
		}
	})
}
