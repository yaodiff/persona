package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

// containerWorkspaceMemStore implements the repository, naming, and backend-workspace
// store interfaces in memory using the exact same contract helpers as repositorystore.
type containerWorkspaceMemStore struct {
	*memRepoStore
	wmu        sync.Mutex
	rules      map[string]repositorystore.WorkspaceRulesRecord
	naming     map[string]workspacenaming.Plan
	workspaces map[string]*taskworkspace.Record
	heads      map[string]string
	todos      map[string]db.TodoRow
	feed       map[string][]db.FeedRow
}

func newContainerWorkspaceMemStore() *containerWorkspaceMemStore {
	return &containerWorkspaceMemStore{
		memRepoStore: newMemRepoStore(),
		rules:        map[string]repositorystore.WorkspaceRulesRecord{},
		naming:       map[string]workspacenaming.Plan{},
		workspaces:   map[string]*taskworkspace.Record{},
		heads:        map[string]string{},
		todos:        map[string]db.TodoRow{},
		feed:         map[string][]db.FeedRow{},
	}
}

func (m *containerWorkspaceMemStore) TaskPlan(_ context.Context, id string) (*db.TaskPlanRow, error) {
	m.memRepoStore.mu.Lock()
	defer m.memRepoStore.mu.Unlock()
	row, ok := m.memRepoStore.plans[id]
	if !ok {
		return nil, db.ErrTaskPlanNotFound
	}
	cp := row
	return &cp, nil
}

func (m *containerWorkspaceMemStore) TaskPlansMadeSince(_ context.Context, since time.Time) ([]db.TaskPlanRow, error) {
	m.memRepoStore.mu.Lock()
	defer m.memRepoStore.mu.Unlock()
	var out []db.TaskPlanRow
	for _, row := range m.memRepoStore.plans {
		if since.IsZero() || !row.CreatedAt.Before(since) {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m *containerWorkspaceMemStore) AppendFeed(_ context.Context, row db.FeedRow) error {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	m.feed[row.Conversation] = append(m.feed[row.Conversation], row)
	return nil
}

func (m *containerWorkspaceMemStore) Feed(_ context.Context, conv string, since time.Time, limit int) ([]db.FeedRow, error) {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	rows := m.feed[conv]
	out := make([]db.FeedRow, 0, len(rows))
	for _, row := range rows {
		if since.IsZero() || row.At.After(since) {
			out = append(out, row)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (m *containerWorkspaceMemStore) catalog(ctx context.Context) (repository.Catalog, error) {
	var doc repositoryDocument
	if _, err := m.Setting(ctx, repositoriesKey, &doc); err != nil {
		return repository.Catalog{}, err
	}
	return doc.taskCatalog(), nil
}

func (m *containerWorkspaceMemStore) CreateTodo(_ context.Context, row db.TodoRow) error {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	m.todos[row.ID] = row
	return nil
}

func (m *containerWorkspaceMemStore) CreateTodoWithRepositories(ctx context.Context, row db.TodoRow, in repository.CreateBindings) error {
	if !repository.ValidRevision(in.CatalogRevision) || in.Repositories == nil {
		return repository.Problem("invalid")
	}
	catalog, err := m.catalog(ctx)
	if err != nil {
		return err
	}
	if catalog.Revision != *in.CatalogRevision {
		return repository.Problem("catalog_conflict")
	}
	rows, err := repository.Bind(nil, in.Repositories, catalog)
	if err != nil {
		return err
	}
	m.wmu.Lock()
	m.todos[row.ID] = row
	m.wmu.Unlock()
	m.memRepoStore.mu.Lock()
	m.memRepoStore.sets[row.ID] = repository.TaskSet{SchemaVersion: 1, TodoID: row.ID, Revision: 1, Repositories: rows}
	m.memRepoStore.mu.Unlock()
	return nil
}

func (m *containerWorkspaceMemStore) SetTodoRepositories(ctx context.Context, id string, in repository.SetInput) (repository.TaskSet, error) {
	if !repository.ValidRevision(in.ExpectedRevision) || !repository.ValidRevision(in.ExpectedCatalogRevision) || in.Repositories == nil {
		return repository.TaskSet{}, repository.Problem("invalid")
	}
	catalog, err := m.catalog(ctx)
	if err != nil {
		return repository.TaskSet{}, err
	}
	if catalog.Revision != *in.ExpectedCatalogRevision {
		return repository.TaskSet{}, repository.Problem("catalog_conflict")
	}
	m.memRepoStore.mu.Lock()
	defer m.memRepoStore.mu.Unlock()
	cur := m.memRepoStore.sets[id]
	if cur.Revision != *in.ExpectedRevision {
		return repository.TaskSet{}, repository.Problem("revision_conflict")
	}
	rows, err := repository.Bind(cur.Repositories, in.Repositories, catalog)
	if err != nil {
		return repository.TaskSet{}, err
	}
	cur.SchemaVersion, cur.TodoID, cur.Revision, cur.Repositories = 1, id, cur.Revision+1, rows
	m.memRepoStore.sets[id] = cur
	return cur, nil
}

func (m *containerWorkspaceMemStore) TodoWorkspaceRules(_ context.Context, id string) (repositorystore.WorkspaceRulesRecord, error) {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	if rec, ok := m.rules[id]; ok {
		return rec, nil
	}
	return repositorystore.WorkspaceRulesRecord{TodoID: id}, nil
}

func (m *containerWorkspaceMemStore) SetTodoWorkspaceRules(_ context.Context, id string, expected int64, patch json.RawMessage) (repositorystore.WorkspaceRulesRecord, error) {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	cur := m.rules[id]
	cur.TodoID = id
	if cur.Revision != expected {
		return cur, repository.Problem("revision_conflict")
	}
	rules, err := workspacenaming.Patch(cur.Rules, patch)
	if err != nil {
		return cur, err
	}
	cur.Rules = rules
	cur.Revision++
	m.rules[id] = cur
	return cur, nil
}

func (m *containerWorkspaceMemStore) WorkspaceNamingPlan(_ context.Context, id, operation string) (*workspacenaming.Plan, error) {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	plan, ok := m.naming[operation]
	if !ok || plan.Source.TodoID != id {
		return nil, nil
	}
	cp := plan
	return &cp, nil
}

func (m *containerWorkspaceMemStore) WorkspaceNamingPlanForSave(_ context.Context, id, operation string) (*workspacenaming.Plan, error) {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	plan, ok := m.naming[operation]
	if !ok {
		return nil, nil
	}
	if plan.Source.TodoID != id {
		return nil, repository.Problem("revision_conflict")
	}
	cp := plan
	return &cp, nil
}

func (m *containerWorkspaceMemStore) SaveWorkspaceNamingPlan(ctx context.Context, id, operation string, revision int64, plan workspacenaming.Plan) (*workspacenaming.Plan, error) {
	if !workspacenaming.ValidOperationID(operation) || plan.Source.TodoID != id {
		return nil, repository.Problem("invalid")
	}
	if err := workspacenaming.CheckPlan(plan); err != nil {
		return nil, err
	}
	set, err := m.TodoRepositories(ctx, id)
	if err != nil {
		return nil, err
	}
	m.wmu.Lock()
	defer m.wmu.Unlock()
	if prior, ok := m.naming[operation]; ok {
		if prior.Source.TodoID != plan.Source.TodoID || prior.Source.MachineID != plan.Source.MachineID ||
			prior.Source.Layout != plan.Source.Layout || prior.Source.Rules != plan.Source.Rules ||
			prior.Source.Repositories.Digest != plan.Source.Repositories.Digest {
			return nil, repository.Problem("revision_conflict")
		}
		cp := prior
		return &cp, nil
	}
	if m.rules[id].Revision != revision {
		return nil, repository.Problem("revision_conflict")
	}
	ids := make([]string, 0, len(plan.Source.Repositories.Repositories))
	for _, r := range plan.Source.Repositories.Repositories {
		ids = append(ids, r.RepoID)
	}
	snapshot, err := repository.Snapshot(plan.Source.Manager, id, set.Revision, set.Repositories, ids)
	if err != nil {
		return nil, err
	}
	if snapshot.Digest != plan.Source.Repositories.Digest {
		return nil, repository.Problem("revision_conflict")
	}
	m.naming[operation] = plan
	cp := plan
	return &cp, nil
}

func cloneWorkspaceRecord(r *taskworkspace.Record) *taskworkspace.Record {
	if r == nil {
		return nil
	}
	cp := *r
	cp.Repositories = slices.Clone(r.Repositories)
	cp.Uses = slices.Clone(r.Uses)
	return &cp
}

func (m *containerWorkspaceMemStore) TaskWorkspace(_ context.Context, todo, op string) (*taskworkspace.Record, error) {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	if op == "" {
		op = m.heads[todo]
	}
	if op == "" {
		return nil, nil
	}
	r, ok := m.workspaces[op]
	if !ok || r.Plan.Source.TodoID != todo {
		return nil, nil
	}
	return cloneWorkspaceRecord(r), nil
}

func (m *containerWorkspaceMemStore) BeginTaskWorkspace(ctx context.Context, todo, op, manager string, replacing ...*taskworkspace.Record) (*taskworkspace.Record, error) {
	if !workspacenaming.ValidOperationID(op) {
		return nil, taskworkspace.Failure("invalid_input", "manager", op, "", "")
	}
	set, err := m.TodoRepositories(ctx, todo)
	if err != nil {
		return nil, err
	}
	m.wmu.Lock()
	defer m.wmu.Unlock()
	if old, ok := m.workspaces[op]; ok && old.Plan.Source.TodoID == todo {
		if old.Plan.Source.Manager != manager {
			return nil, taskworkspace.Failure("identity_mismatch", "manager", op, "", "")
		}
		return cloneWorkspaceRecord(old), nil
	}
	plan, ok := m.naming[op]
	if !ok || plan.Source.TodoID != todo {
		return nil, taskworkspace.Failure("not_found", "manager", op, "", "")
	}
	if plan.Source.Manager != manager {
		return nil, taskworkspace.Failure("invalid_input", "manager", op, "", "")
	}
	ids := make([]string, 0, len(plan.Source.Repositories.Repositories))
	for _, r := range plan.Source.Repositories.Repositories {
		ids = append(ids, r.RepoID)
	}
	snapshot, err := repository.Snapshot(manager, todo, set.Revision, set.Repositories, ids)
	if err != nil {
		return nil, err
	}
	if snapshot.Digest != plan.Source.Repositories.Digest {
		return nil, taskworkspace.Failure("snapshot_stale", "manager", op, "", "")
	}
	if head := m.heads[todo]; head != "" {
		if cur := m.workspaces[head]; cur != nil && cur.State != "cancelled" && cur.State != "removed" {
			switch {
			case len(replacing) > 0 && replacing[0].OperationID == head && replacing[0].Revision == cur.Revision &&
				len(cur.Uses) == 0 && cur.Plan.RootPath != plan.RootPath:
				cur.State, cur.Revision = "cancelled", cur.Revision+1
			case cur.InputDigest == taskworkspace.InputKey(plan):
				return cloneWorkspaceRecord(cur), nil
			default:
				return nil, taskworkspace.Failure("operation_conflict", "manager", op, "", "")
			}
		}
	}
	r := &taskworkspace.Record{
		SchemaVersion: 1,
		WorkspaceID:   "ws_" + taskworkspace.Hash([]string{manager, todo, op})[:24],
		OperationID:   op,
		Plan:          plan,
		InputDigest:   taskworkspace.InputKey(plan),
		State:         "preparing",
		Repositories:  []taskworkspace.Instance{},
		Revision:      1,
		Uses:          []string{},
	}
	m.workspaces[op] = r
	m.heads[todo] = op
	return cloneWorkspaceRecord(r), nil
}

func (m *containerWorkspaceMemStore) MutateTaskWorkspace(_ context.Context, todo, op string, version int64, action, use string) (*taskworkspace.Record, error) {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	r, ok := m.workspaces[op]
	if !ok || r.Plan.Source.TodoID != todo {
		return nil, taskworkspace.Failure("not_found", "manager", op, "", "")
	}
	if version > 0 && r.Revision != version {
		return nil, taskworkspace.Failure("revision_conflict", "manager", op, "", "")
	}
	switch action {
	case "cancel":
		if r.State != "removed" {
			r.State = "cancelled"
		}
	case "acquire":
		if r.State != "ready" || r.Cleanup != nil || !workspacenaming.ValidOperationID(use) {
			return nil, taskworkspace.Failure("not_ready", "manager", op, "", "")
		}
		if strings.HasPrefix(use, "launch_") && slices.ContainsFunc(r.Uses, func(other string) bool { return strings.HasPrefix(other, "launch_") && other != use }) {
			return nil, taskworkspace.Failure("worktree_in_use", "manager", op, "", "")
		}
		if !slices.Contains(r.Uses, use) {
			r.Uses = append(r.Uses, use)
		}
	case "release":
		r.Uses = slices.DeleteFunc(r.Uses, func(id string) bool { return id == use })
	case "check":
		if r.State != "ready" || r.Cleanup != nil || !slices.Contains(r.Uses, use) {
			return nil, taskworkspace.Failure("not_ready", "manager", op, "", "")
		}
		return cloneWorkspaceRecord(r), nil
	default:
		return nil, taskworkspace.Failure("invalid_input", "manager", op, "", "")
	}
	r.Revision++
	return cloneWorkspaceRecord(r), nil
}

func (m *containerWorkspaceMemStore) SaveTaskWorkspaceResult(_ context.Context, todo, op string, version int64, result taskworkspace.Record) (*taskworkspace.Record, error) {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	cur, ok := m.workspaces[op]
	if !ok || cur.Plan.Source.TodoID != todo {
		return nil, taskworkspace.Failure("not_found", "manager", op, "", "")
	}
	if cur.Revision != version || cur.State == "cancelled" && result.State != "cancelled" || cur.State == "removed" {
		return cloneWorkspaceRecord(cur), nil
	}
	if result.WorkspaceID != cur.WorkspaceID || result.OperationID != op || result.InputDigest != cur.InputDigest || result.Plan.Digest != cur.Plan.Digest || workspacenaming.CheckPlan(result.Plan) != nil {
		return nil, taskworkspace.Failure("identity_mismatch", "manager", op, "", "")
	}
	if err := taskworkspace.ValidateReady(result); err != nil {
		return nil, err
	}
	result.Uses = slices.Clone(cur.Uses)
	result.Revision = cur.Revision + 1
	cp := result
	m.workspaces[op] = &cp
	return cloneWorkspaceRecord(&cp), nil
}

func seedContainerCatalog(t *testing.T, st Store, repos ...struct{ id, url string }) {
	t.Helper()
	doc := repositoryDocument{Revision: 1}
	for _, r := range repos {
		id, err := repository.Parse(r.url, "")
		if err != nil {
			t.Fatal(err)
		}
		doc.Repositories = append(doc.Repositories, RepositoryBinding{
			Identity:   id,
			ID:         r.id,
			VerifiedAt: time.Now().UTC(),
		})
	}
	if err := st.PutSetting(context.Background(), repositoriesKey, doc); err != nil {
		t.Fatal(err)
	}
}

type containerTestHarness struct {
	s            *Service
	f            *fakeHub
	st           workspaceFixtureStore
	mu           sync.Mutex
	prepareCalls int
	resolveCalls int
	startCalls   int
	lastPrepare  taskworkspace.Request
	lastStart    struct {
		cwd         string
		host        string
		prompt      string
		workspace   taskworkspace.Request
		workspaceID string
	}
	physical map[string]*taskworkspace.Record
	failNext error
}

type workspaceFixtureStore interface {
	Store
	workspaceNamingStore
	todoRepositoryStore
}

func newContainerTestHarness(t *testing.T) *containerTestHarness {
	t.Helper()
	return newWorkspaceTestHarness(t, newContainerWorkspaceMemStore())
}

func newWorkspaceTestHarness(t *testing.T, st workspaceFixtureStore) *containerTestHarness {
	t.Helper()
	seedContainerCatalog(t, st,
		struct{ id, url string }{"repo_web", "https://github.com/example/web"},
		struct{ id, url string }{"repo_api", "https://github.com/example/api"},
		struct{ id, url string }{"repo_dup_a", "https://github.com/org-a/shared"},
		struct{ id, url string }{"repo_dup_b", "https://github.com/org-b/shared"},
	)
	f := newFakeHub()
	f.sessions["box"] = nil
	f.devices = []Device{{ID: "box", Name: "box", Home: "/home/example", Reachable: true, ClaudeVersion: "2.1.0"}}
	s := newServiceOn(t, f, st)
	s.briefsOptional = true
	h := &containerTestHarness{
		s:        s,
		f:        f,
		st:       st,
		physical: map[string]*taskworkspace.Record{},
	}
	f.nodes = map[string]nodeAnswer{
		"worktree.status": func(string, json.RawMessage) (any, error) {
			return proto.WorktreeStatus{}, nil
		},
		"workspace.naming.resolve": func(_ string, raw json.RawMessage) (any, error) {
			h.mu.Lock()
			h.resolveCalls++
			h.mu.Unlock()
			var src workspacenaming.Source
			if err := json.Unmarshal(raw, &src); err != nil {
				return nil, err
			}
			return workspacenaming.Resolve(src, workspacenaming.NodeContext{Home: "/home/example"}), nil
		},
		"task.workspace.capabilities": func(string, json.RawMessage) (any, error) {
			return map[string]any{"capability": taskworkspace.Capability, "start": true}, nil
		},
		"task.workspace.prepare": func(_ string, raw json.RawMessage) (any, error) {
			var req taskworkspace.Request
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, err
			}
			h.mu.Lock()
			h.prepareCalls++
			h.lastPrepare = req
			r := taskworkspace.Record{
				SchemaVersion:   1,
				WorkspaceID:     req.WorkspaceID,
				OperationID:     req.OperationID,
				NodeOperationID: req.OperationID,
				Plan:            req.Plan,
				InputDigest:     taskworkspace.InputKey(req.Plan),
				State:           "ready",
				Uses:            []string{},
			}
			for i, p := range req.Plan.Repositories {
				commit := strings.Repeat(fmt.Sprintf("%x", i+1), 40)
				input := req.Plan.Source.Repositories.Repositories[i]
				common := "/home/example/.persona/task-workspaces/" + p.RepoID + ".git"
				if input.Local != nil {
					commit, common = input.Local.Commit, input.Local.CommonDir
				}
				r.Repositories = append(r.Repositories, taskworkspace.Instance{
					RepoID:         p.RepoID,
					Input:          input,
					RelativePath:   p.RelativePath,
					Path:           p.AbsolutePath,
					CommonDir:      common,
					TaskBranch:     p.TaskBranch,
					ResolvedRef:    "refs/heads/main",
					ResolvedCommit: commit,
					State:          "ready",
				})
			}
			h.physical[req.OperationID] = &r
			h.mu.Unlock()
			return r, nil
		},
		"task.workspace.inspect": func(_ string, raw json.RawMessage) (any, error) {
			var req taskworkspace.Request
			_ = json.Unmarshal(raw, &req)
			h.mu.Lock()
			defer h.mu.Unlock()
			if r, ok := h.physical[req.OperationID]; ok {
				return *r, nil
			}
			return taskworkspace.Record{Error: taskworkspace.Failure("not_found", "inspect", req.OperationID, "", "")}, nil
		},
		"task.workspace.acquire": func(_ string, raw json.RawMessage) (any, error) {
			var req taskworkspace.Request
			_ = json.Unmarshal(raw, &req)
			h.mu.Lock()
			defer h.mu.Unlock()
			if r, ok := h.physical[req.OperationID]; ok {
				return *r, nil
			}
			return taskworkspace.Record{Error: taskworkspace.Failure("not_found", "acquire", req.OperationID, "", "")}, nil
		},
		"task.workspace.release": func(_ string, raw json.RawMessage) (any, error) {
			var req taskworkspace.Request
			_ = json.Unmarshal(raw, &req)
			h.mu.Lock()
			defer h.mu.Unlock()
			if r, ok := h.physical[req.OperationID]; ok {
				r.Launch = nil
				return *r, nil
			}
			return taskworkspace.Record{Error: taskworkspace.Failure("not_found", "release", req.OperationID, "", "")}, nil
		},
		devicewire.MethodStartBackendSession: func(machine string, raw json.RawMessage) (any, error) {
			var in struct {
				Cwd         string                `json:"cwd"`
				Host        string                `json:"host"`
				Prompt      string                `json:"prompt"`
				WorkspaceID string                `json:"workspace_id"`
				Workspace   taskworkspace.Request `json:"backend_workspace"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, err
			}
			h.mu.Lock()
			h.startCalls++
			h.lastStart.cwd = in.Cwd
			h.lastStart.host = in.Host
			h.lastStart.prompt = in.Prompt
			h.lastStart.workspace = in.Workspace
			h.lastStart.workspaceID = in.WorkspaceID
			fail := h.failNext
			h.failNext = nil
			sid := fmt.Sprintf("sess_container_%d", h.startCalls)
			if fail == nil {
				if r, ok := h.physical[in.Workspace.OperationID]; ok {
					r.Launch = &taskworkspace.Launch{UseID: in.Workspace.UseID, SessionID: sid}
				}
			}
			h.mu.Unlock()
			if fail != nil {
				return nil, fail
			}
			row := proto.Session{SessionID: sid, MachineID: machine, Cwd: in.Cwd, Agent: "claude", Status: proto.StatusWorking}
			f.sessions[machine] = append(f.sessions[machine], row)
			return map[string]any{"session": row}, nil
		},
	}
	return h
}

func TestTaskWorkspaceContainerJSONValidation(t *testing.T) {
	validCases := []struct {
		name string
		raw  string
		mode string
	}{
		{"selected repos", `{"container":{"selected_repo_ids":["repo_web","repo_api"]}}`, workspaceContainer},
		{"existing operation", `{"container":{"operation_id":"op_existing","host":"herdr"}}`, workspaceContainer},
		{"repos plus operation", `{"container":{"selected_repo_ids":["repo_web"],"operation_id":"op_pin","host":"orca"}}`, workspaceContainer},
		{"legacy new", `"new"`, workspaceNew},
		{"legacy none", `"none"`, workspaceNone},
		{"legacy existing", `{"existing":"/code/app"}`, workspaceExisting},
		{"legacy new branch", `{"new":{"branch":"task/demo"}}`, workspaceNew},
		{"legacy backend", `{"backend":{"operation_id":"op_saved","host":"herdr"}}`, workspaceBackend},
	}
	for _, tc := range validCases {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			var w TaskWorkspace
			if err := json.Unmarshal([]byte(tc.raw), &w); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.raw, err)
			}
			if w.Mode != tc.mode {
				t.Fatalf("mode = %q, want %q", w.Mode, tc.mode)
			}
			encoded, err := json.Marshal(w)
			if err != nil {
				t.Fatalf("marshal %+v: %v", w, err)
			}
			var roundTrip TaskWorkspace
			if err := json.Unmarshal(encoded, &roundTrip); err != nil {
				t.Fatalf("round-trip %s: %v", encoded, err)
			}
			if roundTrip.Mode != tc.mode {
				t.Fatalf("round-trip mode = %q, want %q", roundTrip.Mode, tc.mode)
			}
		})
	}

	invalidCases := []struct {
		name string
		raw  string
	}{
		{"empty container", `{"container":{}}`},
		{"empty selected list", `{"container":{"selected_repo_ids":[]}}`},
		{"blank repo id", `{"container":{"selected_repo_ids":["repo_web","  "]}}`},
		{"duplicate repo id", `{"container":{"selected_repo_ids":["repo_web","repo_web"]}}`},
		{"invalid operation id", `{"container":{"operation_id":"bad/op"}}`},
		{"mixed with backend", `{"container":{"selected_repo_ids":["repo_web"]},"backend":{"operation_id":"op_1"}}`},
		{"mixed with existing", `{"container":{"selected_repo_ids":["repo_web"]},"existing":"/code/app"}`},
		{"mixed with new", `{"container":{"selected_repo_ids":["repo_web"]},"new":{"branch":"b"}}`},
		{"unknown field inside container", `{"container":{"selected_repo_ids":["repo_web"],"bogus":true}}`},
		{"too many repos", func() string {
			ids := make([]string, repository.MaxTaskRepositories+1)
			for i := range ids {
				ids[i] = fmt.Sprintf("repo_%d", i)
			}
			b, _ := json.Marshal(map[string]any{"container": map[string]any{"selected_repo_ids": ids}})
			return string(b)
		}()},
	}
	for _, tc := range invalidCases {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			var w TaskWorkspace
			if err := json.Unmarshal([]byte(tc.raw), &w); err == nil {
				t.Fatalf("expected error for %s, got %+v", tc.raw, w)
			}
		})
	}
}

func TestTaskPlanContainerMultiRepoPrepareLaunchAndRetry(t *testing.T) {
	h := newContainerTestHarness(t)
	ctx := context.Background()

	args := `{"tasks":[{"title":"Cross-repo auth","prompt":"Update auth across web and api","role":"worker","machine":"box","permission":"bypassPermissions","readonly":false,"user_requested":false,"why":"Needs both repos","done_when":["Both repos pass tests"],"workspace":{"container":{"selected_repo_ids":["repo_web","repo_api"],"host":"herdr"}}}]}`
	out, err := h.s.toolTaskPlan(ctx, "container-chat", json.RawMessage(args))
	if err != nil {
		t.Fatalf("task_plan: %v", err)
	}
	if !strings.Contains(out, "waiting") && !strings.Contains(out, "card") {
		t.Fatalf("unexpected task_plan reply: %s", out)
	}
	// Plan creation must be metadata-only: previewed paths, zero disk prepare, zero launch.
	if h.resolveCalls != 1 || h.prepareCalls != 0 || h.startCalls != 0 {
		t.Fatalf("plan preview mutated backend state: resolve=%d prepare=%d start=%d", h.resolveCalls, h.prepareCalls, h.startCalls)
	}
	p := readonlyFeedPlan(t, h.s, "container-chat")
	task := p.Tasks[0]
	if task.MachinePolicy != machineFixed || task.Workspace == nil || task.Workspace.Mode != workspaceContainer {
		t.Fatalf("unexpected task policy: %+v", task)
	}
	wantRoot := "/home/example/workspace/task-" + task.ReservedTodoID
	if task.Cwd != wantRoot || task.WorkspaceResolved == nil || task.WorkspaceResolved.Path != wantRoot || len(task.WorkspaceResolved.Repositories) != 2 {
		t.Fatalf("bad preview resolved workspace: cwd=%q resolved=%+v", task.Cwd, task.WorkspaceResolved)
	}

	// Start the plan: binds repos atomically, saves container naming plan, prepares and launches.
	started, err := h.s.startPlan(ctx, p.ID, nil, false)
	if err != nil {
		t.Fatalf("startPlan: %v", err)
	}
	waitFor(t, "container agent link", func() bool {
		todo, ok := h.s.todoNow(task.ReservedTodoID)
		return ok && len(todo.Agents) == 1
	})

	saved, err := h.s.loadPlan(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	stTask := saved.Tasks[0]
	if stTask.Error != "" || stTask.TodoID != task.ReservedTodoID {
		t.Fatalf("container launch failed: task=%+v", stTask)
	}
	if h.prepareCalls != 1 || h.startCalls != 1 {
		t.Fatalf("expected 1 prepare and 1 start, got prepare=%d start=%d", h.prepareCalls, h.startCalls)
	}
	if h.lastPrepare.Plan.Source.Layout != repository.LayoutContainer || len(h.lastPrepare.Plan.Repositories) != 2 {
		t.Fatalf("prepare did not use container layout with 2 repos: %+v", h.lastPrepare.Plan)
	}
	if h.lastStart.cwd != wantRoot || h.lastStart.host != "herdr" {
		t.Fatalf("wrong start target: cwd=%q host=%q wantRoot=%q", h.lastStart.cwd, h.lastStart.host, wantRoot)
	}
	if !strings.Contains(h.lastStart.prompt, wantRoot+"/web") || !strings.Contains(h.lastStart.prompt, wantRoot+"/api") || !strings.Contains(h.lastStart.prompt, "resolved_commit") {
		t.Fatalf("start prompt missing container repo details: %s", h.lastStart.prompt)
	}
	if stTask.WorkspaceResolved.BackendWorkspaceID == "" || stTask.WorkspaceResolved.OperationID == "" || len(stTask.WorkspaceResolved.Repositories) != 2 {
		t.Fatalf("resolved metadata incomplete: %+v", stTask.WorkspaceResolved)
	}
	for _, repo := range stTask.WorkspaceResolved.Repositories {
		if repo.ResolvedCommit == "" || repo.AbsolutePath == "" {
			t.Fatalf("missing resolved commit/path on %+v", repo)
		}
	}
	// Verify todo repository bindings were persisted in the existing store.
	repoSet, err := h.st.TodoRepositories(ctx, stTask.TodoID)
	if err != nil || len(repoSet.Repositories) != 2 {
		t.Fatalf("todo repositories not persisted: %+v err=%v", repoSet, err)
	}
	// Verify agent is linked to the todo.
	todo, ok := h.s.todoNow(stTask.TodoID)
	if !ok || len(todo.Agents) != 1 || todo.Agents[0].Cwd != wantRoot {
		t.Fatalf("todo agent not linked at container root: ok=%v todo=%+v", ok, todo)
	}

	// Verify retry after a failed start reuses the exact same operation_id and ready workspace without re-preparing.
	h.mu.Lock()
	h.failNext = errors.New("temporary engine failure")
	h.mu.Unlock()
	args2 := `{"tasks":[{"title":"Retry container","prompt":"Verify retry reuses ready container","role":"worker","machine":"box","permission":"bypassPermissions","readonly":false,"user_requested":false,"why":"Retry test","done_when":["Passes"],"workspace":{"container":{"selected_repo_ids":["repo_web","repo_api"]}}}]}`
	if _, err := h.s.toolTaskPlan(ctx, "retry-chat", json.RawMessage(args2)); err != nil {
		t.Fatalf("task_plan retry: %v", err)
	}
	p2 := readonlyFeedPlan(t, h.s, "retry-chat")
	if _, err := h.s.startPlan(ctx, p2.ID, nil, false); err != nil {
		t.Fatalf("startPlan retry: %v", err)
	}
	waitFor(t, "container start failure recorded", func() bool {
		fp, err := h.s.loadPlan(ctx, p2.ID)
		return err == nil && len(fp.Tasks) == 1 && fp.Tasks[0].Error != ""
	})
	failedPlan, _ := h.s.loadPlan(ctx, p2.ID)
	if failedPlan.Tasks[0].WorkspaceError == nil || failedPlan.Tasks[0].WorkspaceResolved.OperationID == "" {
		t.Fatalf("expected recorded start failure with workspace_error: %+v", failedPlan.Tasks[0])
	}
	firstOp := failedPlan.Tasks[0].WorkspaceResolved.OperationID
	h.mu.Lock()
	prepBeforeRetry := h.prepareCalls
	h.mu.Unlock()
	if _, err := h.s.retryTask(ctx, failedPlan.Tasks[0].TodoID); err != nil {
		t.Fatalf("retryTask: %v", err)
	}
	waitFor(t, "container retry agent link", func() bool {
		todo, ok := h.s.todoNow(failedPlan.Tasks[0].TodoID)
		return ok && len(todo.Agents) == 1
	})
	retriedPlan, _ := h.s.loadPlan(ctx, p2.ID)
	if retriedPlan.Tasks[0].Error != "" || retriedPlan.Tasks[0].WorkspaceResolved.OperationID != firstOp {
		t.Fatalf("retry did not succeed on same op: %+v", retriedPlan.Tasks[0])
	}
	h.mu.Lock()
	prepAfterRetry := h.prepareCalls
	h.mu.Unlock()
	if prepAfterRetry != prepBeforeRetry {
		t.Fatalf("retry re-prepared an already-ready container workspace: before=%d after=%d", prepBeforeRetry, prepAfterRetry)
	}
	_ = started
}

func TestTaskPlanContainerExistingPreparedAndExistingTodoMerge(t *testing.T) {
	t.Run("reuse existing prepared container workspace by operation_id", func(t *testing.T) {
		h := newContainerTestHarness(t)
		ctx := context.Background()
		todo, err := h.s.createTaskTodoWithRepositories(ctx, "todo_prepared1", "Prepared work", "notes", "chat-a", "", "", "", []string{"repo_web", "repo_api"})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.s.ensureBackendWorkspacePlan(ctx, todo.ID, namingRequest{
			OperationID:     "op_prepared_container",
			MachineID:       "box",
			Layout:          repository.LayoutContainer,
			SelectedRepoIDs: []string{"repo_web", "repo_api"},
		}); err != nil {
			t.Fatal(err)
		}
		rec, err := h.s.PrepareBackendWorkspace(ctx, todo.ID, "op_prepared_container")
		if err != nil || rec.State != "ready" {
			t.Fatalf("pre-prepare failed: rec=%+v err=%v", rec, err)
		}
		prepCount := h.prepareCalls

		args := fmt.Sprintf(`{"tasks":[{"todo_id":%q,"title":"Launch prepared","prompt":"Use prepared container","role":"worker","machine":"box","permission":"bypassPermissions","readonly":false,"user_requested":false,"why":"Reuse container","done_when":["Done"],"workspace":{"container":{"operation_id":"op_prepared_container","host":"orca"}}}]}`, todo.ID)
		if _, err := h.s.toolTaskPlan(ctx, "chat-a", json.RawMessage(args)); err != nil {
			t.Fatalf("task_plan: %v", err)
		}
		p := readonlyFeedPlan(t, h.s, "chat-a")
		if _, err := h.s.startPlan(ctx, p.ID, nil, false); err != nil {
			t.Fatalf("startPlan: %v", err)
		}
		waitFor(t, "prepared container agent link", func() bool {
			cur, ok := h.s.todoNow(todo.ID)
			return ok && len(cur.Agents) == 1
		})

		h.mu.Lock()
		gotPrep, gotStart, gotLast := h.prepareCalls, h.startCalls, h.lastStart
		h.mu.Unlock()
		if gotPrep != prepCount {
			t.Fatalf("re-prepared an already-ready container: before=%d after=%d", prepCount, gotPrep)
		}
		if gotStart != 1 || gotLast.host != "orca" || gotLast.workspace.OperationID != "op_prepared_container" {
			t.Fatalf("wrong start for prepared container: calls=%d start=%+v", gotStart, gotLast)
		}
	})

	t.Run("merge catalog repo into existing todo preserving alias and links", func(t *testing.T) {
		h := newContainerTestHarness(t)
		ctx := context.Background()
		todo, err := h.s.createTodo(ctx, "Existing feature", "notes", "chat-b", "")
		if err != nil {
			t.Fatal(err)
		}
		zero, one := int64(0), int64(1)
		if _, err := h.s.setTodoRepositories(ctx, todo.ID, repository.SetInput{
			ExpectedRevision:        &zero,
			ExpectedCatalogRevision: &one,
			Repositories: []repository.TaskInput{{
				RepoID:         "repo_web",
				DirectoryAlias: "frontend",
				RequestedRef:   repository.RequestedRef{Kind: "branch", Value: "main"},
				Links:          []repository.LinkInput{{URL: "https://github.com/example/web/issues/42", Title: "Issue 42"}},
			}},
		}); err != nil {
			t.Fatal(err)
		}

		args := fmt.Sprintf(`{"tasks":[{"todo_id":%q,"title":"Existing feature","prompt":"Edit web and api together","role":"worker","machine":"box","permission":"bypassPermissions","readonly":false,"user_requested":false,"why":"Cross-repo","done_when":["Done"],"workspace":{"container":{"selected_repo_ids":["repo_web","repo_api"]}}}]}`, todo.ID)
		if _, err := h.s.toolTaskPlan(ctx, "chat-b", json.RawMessage(args)); err != nil {
			t.Fatalf("task_plan: %v", err)
		}
		p := readonlyFeedPlan(t, h.s, "chat-b")
		// Preview must keep custom alias "frontend" for repo_web and default "api" for repo_api, without writing DB yet.
		setBeforeStart, _ := h.st.TodoRepositories(ctx, todo.ID)
		if len(setBeforeStart.Repositories) != 1 {
			t.Fatalf("preview mutated todo repositories: %+v", setBeforeStart)
		}
		var relPaths []string
		for _, repo := range p.Tasks[0].WorkspaceResolved.Repositories {
			relPaths = append(relPaths, repo.RelativePath)
		}
		slices.Sort(relPaths)
		if !slices.Equal(relPaths, []string{"api", "frontend"}) {
			t.Fatalf("preview relative paths = %v, want [api frontend]", relPaths)
		}

		if _, err := h.s.startPlan(ctx, p.ID, nil, false); err != nil {
			t.Fatalf("startPlan: %v", err)
		}
		waitFor(t, "merged container agent link", func() bool {
			cur, ok := h.s.todoNow(todo.ID)
			return ok && len(cur.Agents) == 1
		})

		saved, _ := h.s.loadPlan(ctx, p.ID)
		h.mu.Lock()
		starts := h.startCalls
		h.mu.Unlock()
		if saved.Tasks[0].Error != "" || starts != 1 {
			t.Fatalf("merged container launch failed: task=%+v starts=%d", saved.Tasks[0], starts)
		}
		setAfterStart, _ := h.st.TodoRepositories(ctx, todo.ID)
		if len(setAfterStart.Repositories) != 2 {
			t.Fatalf("expected 2 merged repositories, got %+v", setAfterStart)
		}
		var webRow *repository.TaskRepository
		for i := range setAfterStart.Repositories {
			if setAfterStart.Repositories[i].RepoID == "repo_web" {
				webRow = &setAfterStart.Repositories[i]
			}
		}
		if webRow == nil || webRow.DirectoryAlias != "frontend" || len(webRow.Links) != 1 || webRow.Links[0].Number != "42" {
			t.Fatalf("existing repo alias or links were lost during merge: %+v", webRow)
		}
	})
}

func TestTaskPlanContainerValidationAndErrors(t *testing.T) {
	t.Run("unknown repo id rejected", func(t *testing.T) {
		h := newContainerTestHarness(t)
		args := `{"tasks":[{"title":"Bad repo","prompt":"Do work","role":"worker","machine":"box","readonly":false,"user_requested":false,"why":"test","done_when":["done"],"workspace":{"container":{"selected_repo_ids":["repo_web","repo_missing"]}}}]}`
		_, err := h.s.toolTaskPlan(context.Background(), "chat-err", json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "not_found") {
			t.Fatalf("expected not_found error, got %v", err)
		}
		if h.prepareCalls != 0 || h.startCalls != 0 {
			t.Fatalf("unexpected side effects: prepare=%d start=%d", h.prepareCalls, h.startCalls)
		}
	})

	t.Run("duplicate directory alias collision rejected", func(t *testing.T) {
		h := newContainerTestHarness(t)
		args := `{"tasks":[{"title":"Dup alias","prompt":"Do work","role":"worker","machine":"box","readonly":false,"user_requested":false,"why":"test","done_when":["done"],"workspace":{"container":{"selected_repo_ids":["repo_dup_a","repo_dup_b"]}}}]}`
		_, err := h.s.toolTaskPlan(context.Background(), "chat-err", json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "alias_duplicate") {
			t.Fatalf("expected alias_duplicate error, got %v", err)
		}
	})

	t.Run("operation_id only without todo_id rejected", func(t *testing.T) {
		h := newContainerTestHarness(t)
		args := `{"tasks":[{"title":"Missing todo","prompt":"Do work","role":"worker","machine":"box","readonly":false,"user_requested":false,"why":"test","done_when":["done"],"workspace":{"container":{"operation_id":"op_existing"}}}]}`
		_, err := h.s.toolTaskPlan(context.Background(), "chat-err", json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "todo_id") {
			t.Fatalf("expected todo_id error, got %v", err)
		}
	})

	t.Run("non-container root layout plan rejected by workspace.container", func(t *testing.T) {
		h := newContainerTestHarness(t)
		ctx := context.Background()
		todo, err := h.s.createTaskTodoWithRepositories(ctx, "todo_root1", "Root work", "notes", "chat-root", "", "", "", []string{"repo_web"})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.s.ensureBackendWorkspacePlan(ctx, todo.ID, namingRequest{
			OperationID:     "op_root_only",
			MachineID:       "box",
			Layout:          repository.LayoutRoot,
			SelectedRepoIDs: []string{"repo_web"},
		}); err != nil {
			t.Fatal(err)
		}
		args := fmt.Sprintf(`{"tasks":[{"todo_id":%q,"title":"Wrong layout","prompt":"Do work","role":"worker","machine":"box","readonly":false,"user_requested":false,"why":"test","done_when":["done"],"workspace":{"container":{"operation_id":"op_root_only"}}}]}`, todo.ID)
		_, err = h.s.toolTaskPlan(ctx, "chat-root", json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "container layout") {
			t.Fatalf("expected container layout error, got %v", err)
		}
	})

	t.Run("operation_id owned by another todo conflicts", func(t *testing.T) {
		h := newContainerTestHarness(t)
		ctx := context.Background()
		other, err := h.s.createTaskTodoWithRepositories(ctx, "todo_other", "Other", "notes", "chat-other", "", "", "", []string{"repo_web"})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.s.ensureBackendWorkspacePlan(ctx, other.ID, namingRequest{
			OperationID:     "op_taken",
			MachineID:       "box",
			Layout:          repository.LayoutContainer,
			SelectedRepoIDs: []string{"repo_web"},
		}); err != nil {
			t.Fatal(err)
		}
		args := `{"tasks":[{"title":"Conflict","prompt":"Do work","role":"worker","machine":"box","readonly":false,"user_requested":false,"why":"test","done_when":["done"],"workspace":{"container":{"selected_repo_ids":["repo_web"],"operation_id":"op_taken"}}}]}`
		_, err = h.s.toolTaskPlan(ctx, "chat-err", json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "conflict") {
			t.Fatalf("expected conflict error, got %v", err)
		}
	})

	t.Run("machine mismatch on saved container plan rejected", func(t *testing.T) {
		h := newContainerTestHarness(t)
		ctx := context.Background()
		h.f.sessions["other-box"] = nil
		h.f.devices = append(h.f.devices, Device{ID: "other-box", Name: "other-box", Home: "/home/other", Reachable: true, ClaudeVersion: "2.1.0"})
		todo, err := h.s.createTaskTodoWithRepositories(ctx, "todo_mach", "Mach", "notes", "chat-mach", "", "", "", []string{"repo_web"})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.s.ensureBackendWorkspacePlan(ctx, todo.ID, namingRequest{
			OperationID:     "op_on_box",
			MachineID:       "box",
			Layout:          repository.LayoutContainer,
			SelectedRepoIDs: []string{"repo_web"},
		}); err != nil {
			t.Fatal(err)
		}
		args := fmt.Sprintf(`{"tasks":[{"todo_id":%q,"title":"Wrong machine","prompt":"Do work","role":"worker","machine":"other-box","readonly":false,"user_requested":false,"why":"test","done_when":["done"],"workspace":{"container":{"operation_id":"op_on_box"}}}]}`, todo.ID)
		_, err = h.s.toolTaskPlan(ctx, "chat-mach", json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "identity_mismatch") {
			t.Fatalf("expected identity_mismatch error, got %v", err)
		}
	})

	t.Run("unavailable machine rejected cleanly", func(t *testing.T) {
		h := newContainerTestHarness(t)
		h.f.nodes["workspace.naming.resolve"] = func(string, json.RawMessage) (any, error) {
			return nil, errors.New("machine offline")
		}
		args := `{"tasks":[{"title":"Offline box","prompt":"Do work","role":"worker","machine":"box","readonly":false,"user_requested":false,"why":"test","done_when":["done"],"workspace":{"container":{"selected_repo_ids":["repo_web","repo_api"]}}}]}`
		_, err := h.s.toolTaskPlan(context.Background(), "chat-err", json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "node_unavailable") {
			t.Fatalf("expected node_unavailable error, got %v", err)
		}
	})

	t.Run("unsupported node fails admission without saving naming plan", func(t *testing.T) {
		h := newContainerTestHarness(t)
		ctx := context.Background()
		h.f.nodes["task.workspace.capabilities"] = func(string, json.RawMessage) (any, error) {
			return map[string]any{"capability": taskworkspace.Capability, "start": false}, nil
		}
		args := `{"tasks":[{"title":"Old node","prompt":"Do work","role":"worker","machine":"box","permission":"bypassPermissions","readonly":false,"user_requested":false,"why":"test","done_when":["done"],"workspace":{"container":{"selected_repo_ids":["repo_web","repo_api"],"operation_id":"op_old_node"}}}]}`
		if _, err := h.s.toolTaskPlan(ctx, "chat-old", json.RawMessage(args)); err != nil {
			t.Fatalf("task_plan: %v", err)
		}
		p := readonlyFeedPlan(t, h.s, "chat-old")
		if _, err := h.s.startPlan(ctx, p.ID, nil, false); err != nil {
			t.Fatalf("startPlan: %v", err)
		}
		waitFor(t, "unsupported workspace error", func() bool {
			sv, err := h.s.loadPlan(ctx, p.ID)
			return err == nil && len(sv.Tasks) == 1 && sv.Tasks[0].Error != ""
		})
		saved, _ := h.s.loadPlan(ctx, p.ID)
		if saved.Tasks[0].WorkspaceError == nil || saved.Tasks[0].WorkspaceError.Code != "unsupported" {
			t.Fatalf("expected unsupported workspace_error, got %+v", saved.Tasks[0])
		}
		h.mu.Lock()
		prep, starts := h.prepareCalls, h.startCalls
		h.mu.Unlock()
		if prep != 0 || starts != 0 {
			t.Fatalf("unsupported node prepared or started: prepare=%d start=%d", prep, starts)
		}
		if plan, _ := h.st.WorkspaceNamingPlan(ctx, saved.Tasks[0].TodoID, "op_old_node"); plan != nil {
			t.Fatalf("unsupported node left orphaned naming plan: %+v", plan)
		}
	})
}

func TestTaskPlanContainerLegacyModesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name      string
		workspace string
		wantMode  string
	}{
		{"default omitted", "", workspaceExisting},
		{"explicit new", `"new"`, workspaceNew},
		{"explicit existing", `{"existing":"/code/app"}`, workspaceExisting},
		{"explicit none", `"none"`, workspaceNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, f := workspaceTaskService(t)
				backendCalls := 0
				f.nodes["workspace.naming.resolve"] = func(string, json.RawMessage) (any, error) {
					backendCalls++
					return nil, errors.New("legacy mode must not call workspace.naming.resolve")
				}
				f.nodes["task.workspace.prepare"] = func(string, json.RawMessage) (any, error) {
					backendCalls++
					return nil, errors.New("legacy mode must not call task.workspace.prepare")
				}
				p := workspacePlan(t, s, tc.workspace)
				if p.Tasks[0].Workspace.Mode != tc.wantMode {
					t.Fatalf("mode = %q, want %q", p.Tasks[0].Workspace.Mode, tc.wantMode)
				}
				if _, err := s.startPlan(context.Background(), p.ID, nil, false); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if backendCalls != 0 || len(f.starts) != 1 {
					t.Fatalf("legacy path changed: backendCalls=%d starts=%d", backendCalls, len(f.starts))
				}
			})
		})
	}
}

func TestTaskPlanContainerWorkspaceRealPostgres(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	f := newFakeHub()
	f.sessions["box"] = nil
	f.devices = []Device{{ID: "box", Name: "box", Home: "/home/example", Reachable: true, ClaudeVersion: "2.1.0"}}
	var physical *taskworkspace.Record
	f.nodes = map[string]nodeAnswer{
		"worktree.status": func(string, json.RawMessage) (any, error) {
			return proto.WorktreeStatus{}, nil
		},
		"workspace.naming.resolve": func(_ string, raw json.RawMessage) (any, error) {
			var src workspacenaming.Source
			_ = json.Unmarshal(raw, &src)
			return workspacenaming.Resolve(src, workspacenaming.NodeContext{Home: "/home/example"}), nil
		},
		"task.workspace.capabilities": func(string, json.RawMessage) (any, error) {
			return map[string]any{"capability": taskworkspace.Capability, "start": true}, nil
		},
		"task.workspace.prepare": func(_ string, raw json.RawMessage) (any, error) {
			var req taskworkspace.Request
			_ = json.Unmarshal(raw, &req)
			r := taskworkspace.Record{
				SchemaVersion: 1, WorkspaceID: req.WorkspaceID, OperationID: req.OperationID,
				NodeOperationID: req.OperationID, Plan: req.Plan, InputDigest: taskworkspace.InputKey(req.Plan),
				State: "ready", Uses: []string{},
			}
			for i, p := range req.Plan.Repositories {
				r.Repositories = append(r.Repositories, taskworkspace.Instance{
					RepoID: p.RepoID, Input: req.Plan.Source.Repositories.Repositories[i],
					RelativePath: p.RelativePath, Path: p.AbsolutePath, CommonDir: "/home/example/owned/" + p.RepoID + ".git",
					TaskBranch: p.TaskBranch, ResolvedRef: "refs/heads/main", ResolvedCommit: strings.Repeat("b", 40), State: "ready",
				})
			}
			physical = &r
			return r, nil
		},
		"task.workspace.acquire": func(string, json.RawMessage) (any, error) { return *physical, nil },
		"task.workspace.release": func(string, json.RawMessage) (any, error) { return *physical, nil },
		devicewire.MethodStartBackendSession: func(machine string, raw json.RawMessage) (any, error) {
			var in struct {
				Cwd       string                `json:"cwd"`
				Workspace taskworkspace.Request `json:"backend_workspace"`
			}
			_ = json.Unmarshal(raw, &in)
			row := proto.Session{SessionID: "pg_container_sess", MachineID: machine, Cwd: in.Cwd, Agent: "claude", Status: proto.StatusWorking}
			f.sessions[machine] = append(f.sessions[machine], row)
			return map[string]any{"session": row}, nil
		},
	}

	s, _ := repositoryService(t, database)
	s.briefsOptional = true
	setHub(s, f)
	var webRepo, apiRepo struct{ Repository RepositoryBinding }
	for _, item := range []struct {
		url string
		out *struct{ Repository RepositoryBinding }
	}{
		{"https://github.com/example/web", &webRepo},
		{"https://github.com/example/api", &apiRepo},
	} {
		w := placeDo(s, "", "POST", repoBase, fmt.Sprintf(`{"url":%q}`, item.url))
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), item.out) != nil {
			t.Fatal(w.Code, w.Body)
		}
	}

	args := fmt.Sprintf(`{"tasks":[{"title":"PG container","prompt":"Work across web and api","role":"worker","machine":"box","permission":"bypassPermissions","readonly":false,"user_requested":false,"why":"Integration","done_when":["Tests pass"],"workspace":{"container":{"selected_repo_ids":[%q,%q]}}}]}`, webRepo.Repository.ID, apiRepo.Repository.ID)
	if _, err := s.toolTaskPlan(t.Context(), "pg-chat", json.RawMessage(args)); err != nil {
		t.Fatalf("task_plan: %v", err)
	}
	p := readonlyFeedPlan(t, s, "pg-chat")
	if _, err := s.startPlan(t.Context(), p.ID, nil, false); err != nil {
		t.Fatalf("startPlan: %v", err)
	}
	waitFor(t, "pg container agent link", func() bool {
		saved, err := s.loadPlan(t.Context(), p.ID)
		if err != nil || len(saved.Tasks) != 1 || saved.Tasks[0].TodoID == "" {
			return false
		}
		todo, ok := s.todoNow(saved.Tasks[0].TodoID)
		return ok && len(todo.Agents) == 1 && todo.Agents[0].SessionID == "pg_container_sess"
	})
	saved, err := s.loadPlan(t.Context(), p.ID)
	if err != nil || saved.Tasks[0].Error != "" || saved.Tasks[0].WorkspaceResolved.BackendWorkspaceID == "" || len(saved.Tasks[0].WorkspaceResolved.Repositories) != 2 {
		t.Fatalf("bad saved pg container plan: %+v err=%v", saved, err)
	}
}
