package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

const (
	workspaceBackend   = "backend"
	workspaceContainer = "container"
	workspaceNew       = "new"
	workspaceExisting  = "existing"
	workspaceNone      = "none"
	workspaceScratch   = "scratch"
	preferExisting     = "prefer-existing"
	preferNew          = "prefer-new"
)

// TaskWorkspace is a task-level policy, separate from its resolved checkout. The
// object variants leave room for future repository entries without changing cwd.
type TaskWorkspace struct {
	Mode      string
	Existing  string
	Queue     bool
	Branch    string
	Backend   *BackendWorkspaceSelection
	Container *ContainerWorkspaceSelection
}

// BackendWorkspaceSelection references a saved naming plan for the target to-do.
type BackendWorkspaceSelection struct {
	OperationID string `json:"operation_id"`
	Host        string `json:"host,omitempty"`
}

// ContainerWorkspaceSelection selects a multi-repository container backend workspace:
// saved repository IDs to plan/prepare on the target machine, an existing saved/prepared
// container operation_id on todo_id, or both to pin a stable operation_id.
type ContainerWorkspaceSelection struct {
	SelectedRepoIDs []string `json:"selected_repo_ids,omitempty"`
	OperationID     string   `json:"operation_id,omitempty"`
	Host            string   `json:"host,omitempty"`
}

func (w *TaskWorkspace) UnmarshalJSON(raw []byte) error {
	*w = TaskWorkspace{}
	var mode string
	if json.Unmarshal(raw, &mode) == nil {
		if mode != workspaceNew && mode != workspaceNone && mode != workspaceScratch {
			return fmt.Errorf("workspace must be new, scratch, {existing: cwd or workspace_id}, {new: {branch: name}}, or none")
		}
		w.Mode = mode
		return nil
	}
	var in struct {
		Backend   *BackendWorkspaceSelection   `json:"backend"`
		Container *ContainerWorkspaceSelection `json:"container"`
		Existing  string                       `json:"existing"`
		Queue     bool                         `json:"queue"`
		New       *struct {
			Branch string `json:"branch"`
		} `json:"new"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return fmt.Errorf("invalid workspace: %w", err)
	}
	in.Existing = strings.TrimSpace(in.Existing)
	if in.Backend != nil {
		if in.Container != nil || in.New != nil || in.Existing != "" || in.Queue || !workspacenaming.ValidOperationID(in.Backend.OperationID) {
			return fmt.Errorf("workspace.backend requires a single saved operation_id")
		}
		w.Mode, w.Backend = workspaceBackend, in.Backend
		return nil
	}
	if in.Container != nil {
		if in.New != nil || in.Existing != "" || in.Queue {
			return fmt.Errorf("workspace must select exactly one of new, existing, none, backend, or container")
		}
		sel := &ContainerWorkspaceSelection{
			OperationID: strings.TrimSpace(in.Container.OperationID),
			Host:        strings.TrimSpace(in.Container.Host),
		}
		if sel.OperationID != "" && !workspacenaming.ValidOperationID(sel.OperationID) {
			return fmt.Errorf("workspace.container.operation_id is invalid")
		}
		if len(in.Container.SelectedRepoIDs) > repository.MaxTaskRepositories {
			return fmt.Errorf("workspace.container.selected_repo_ids exceeds maximum of %d repositories", repository.MaxTaskRepositories)
		}
		if len(in.Container.SelectedRepoIDs) > 0 {
			seen := make(map[string]bool, len(in.Container.SelectedRepoIDs))
			sel.SelectedRepoIDs = make([]string, 0, len(in.Container.SelectedRepoIDs))
			for _, id := range in.Container.SelectedRepoIDs {
				id = strings.TrimSpace(id)
				if id == "" || len(id) > 80 {
					return fmt.Errorf("workspace.container.selected_repo_ids contains an invalid repository id")
				}
				if seen[id] {
					return fmt.Errorf("workspace.container.selected_repo_ids contains duplicate repository id %q", id)
				}
				seen[id] = true
				sel.SelectedRepoIDs = append(sel.SelectedRepoIDs, id)
			}
		}
		if len(sel.SelectedRepoIDs) == 0 && sel.OperationID == "" {
			return fmt.Errorf("workspace.container requires selected_repo_ids or operation_id")
		}
		w.Mode, w.Container = workspaceContainer, sel
		return nil
	}
	if in.Existing != "" && in.New == nil {
		w.Mode, w.Existing, w.Queue = workspaceExisting, in.Existing, in.Queue
		return nil
	}
	if in.New != nil && in.Existing == "" && !in.Queue {
		w.Mode, w.Branch = workspaceNew, strings.TrimSpace(in.New.Branch)
		return nil
	}
	return fmt.Errorf("workspace must select exactly one of new, existing, scratch, or none")
}

func (w TaskWorkspace) MarshalJSON() ([]byte, error) {
	switch w.Mode {
	case workspaceBackend:
		return json.Marshal(map[string]any{"backend": w.Backend})
	case workspaceContainer:
		return json.Marshal(map[string]any{"container": w.Container})
	case workspaceExisting:
		if w.Queue {
			return json.Marshal(map[string]any{"existing": w.Existing, "queue": true})
		}
		return json.Marshal(map[string]string{"existing": w.Existing})
	case workspaceNew:
		if w.Branch != "" {
			return json.Marshal(map[string]any{"new": map[string]string{"branch": w.Branch}})
		}
	}
	return json.Marshal(w.Mode)
}

type BackendLaunchRepository struct {
	workspacenaming.RepositoryPlan
	ResolvedRef    string `json:"resolved_ref,omitempty"`
	ResolvedCommit string `json:"resolved_commit,omitempty"`
}

// TaskWorkspaceResolved is the server-computed checkout or directory. WorkspaceID belongs to the engine.
type TaskWorkspaceResolved struct {
	BackendWorkspaceID string                    `json:"backend_workspace_id,omitempty"`
	OperationID        string                    `json:"operation_id,omitempty"`
	Repositories       []BackendLaunchRepository `json:"repositories,omitempty"`
	Machine            string                    `json:"machine"`
	Path               string                    `json:"path"`
	Branch             string                    `json:"branch,omitempty"`
	WorkspaceID        string                    `json:"workspace_id,omitempty"`
	Created            bool                      `json:"created,omitempty"`
	Reused             bool                      `json:"reused,omitempty"`
	// Shared: the engine could not prepare a checkout for an inferred new workspace, so the task runs in a cwd
	// another active task also writes in, as every task did before workspaces existed.
	Shared bool `json:"shared,omitempty"`
}

func (t *PlanTask) scratchWorkspace() bool {
	return t.Workspace != nil && t.Workspace.Mode == workspaceScratch
}

func scratchUnsupportedError(ctx context.Context, lang i18n.Lang, machine string) error {
	return &rpc.Error{
		Code:    "scratch_unsupported",
		Message: i18n.T(lang, "assistant-tasks_workspace.scratch_unsupported", i18n.Args{"machine": machine}),
	}
}

func isUnknownScratchMethodErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unknown method "+nodeproto.MethodWorkspaceScratchPrepare) ||
		strings.Contains(msg, "method not found")
}

func (s *Service) prepareScratchWorkspace(ctx context.Context, t *PlanTask, dryRun bool) (nodeproto.ScratchWorkspaceResult, error) {
	id := firstNonEmpty(t.TodoID, t.ReuseTodo, t.ReservedTodoID)
	if !nodeproto.ValidScratchID(id) {
		return nodeproto.ScratchWorkspaceResult{}, &rpc.Error{
			Code:    "invalid_scratch_id",
			Message: fmt.Sprintf("invalid scratch workspace identifier %q", id),
		}
	}
	m, hasMachine := s.machineByName(ctx, t.Machine)
	label := t.Machine
	if hasMachine {
		label = firstNonEmpty(m.Name, m.ID)
		if !m.ScratchWorkspace {
			return nodeproto.ScratchWorkspaceResult{}, scratchUnsupportedError(ctx, s.lang(ctx), label)
		}
	}
	var res nodeproto.ScratchWorkspaceResult
	callCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	err := s.call(callCtx, t.Machine, nodeproto.MethodWorkspaceScratchPrepare, nodeproto.ScratchWorkspaceRequest{
		TodoID: nodeproto.ScratchID(id),
		DryRun: dryRun,
	}, &res)
	if err != nil {
		if isUnknownScratchMethodErr(err) {
			return nodeproto.ScratchWorkspaceResult{}, scratchUnsupportedError(ctx, s.lang(ctx), label)
		}
		return nodeproto.ScratchWorkspaceResult{}, err
	}
	if !proto.NodeAbs(res.Path) {
		return nodeproto.ScratchWorkspaceResult{}, i18n.ErrEnglish("assistant-tasks_workspace.scratch_path")
	}
	if hasMachine && assistantWorkspace(m, res.Path) {
		return nodeproto.ScratchWorkspaceResult{}, i18n.Err("rpc-errors.agent_assistant_workspace")
	}
	return res, nil
}

func validWorkspacePreference(v string) bool {
	return v == preferExisting || v == preferNew
}

func (s *Service) taskWorkspacePreference(ctx context.Context) string {
	if s.settings(ctx).TaskWorkspacePreference == preferNew {
		return preferNew
	}
	return preferExisting
}

// taskWorkspaceResolve resolves a directory binding on machine with terminal spelling, deduplicating concurrent
// in-flight lookups and caching only valid available absolute answers for taskDirCacheTTL.
func (s *Service) taskWorkspaceResolve(ctx context.Context, machine, path string) (proto.Workspace, error) {
	key := taskDirCacheKey(machine, path)
	return s.tasks.dirMeta.workspace.getOrFetch(
		ctx,
		s.taskMetaNow,
		key,
		key,
		12*time.Second,
		func(ws proto.Workspace) bool {
			return ws.Available && proto.NodeAbs(ws.Cwd)
		},
		func(ws proto.Workspace) []string {
			if ws.Available && proto.NodeAbs(ws.Cwd) {
				return []string{taskDirCacheKey(machine, ws.Cwd)}
			}
			return nil
		},
		func(callCtx context.Context) (proto.Workspace, error) {
			var ws proto.Workspace
			if err := s.call(callCtx, machine, "workspace.resolve", map[string]any{"path": path, "terminal": true}, &ws); err != nil {
				return proto.Workspace{}, err
			}
			return ws, nil
		},
	)
}

// resolveTaskWorkspace is metadata-only: it must never create a checkout on a
// waiting card. Re-resolving after an edit or a machine change asks that machine.
func (s *Service) resolveTaskWorkspace(ctx context.Context, t *PlanTask) error {
	return s.resolveTaskWorkspaceForDispatch(ctx, t, true)
}

func (s *Service) resolveTaskWorkspaceForDispatch(ctx context.Context, t *PlanTask, prepareOnNode bool) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if t.ReservedTodoID == "" {
		t.ReservedTodoID = firstNonEmpty(t.ReuseTodo, t.TodoID, "todo_"+randomHex(8))
	}
	if t.backendWorkspace() {
		return s.resolveBackendTask(ctx, t)
	}
	t.dispatchedWorkspace = nil
	if w := t.WorkspaceResolved; t.Workspace != nil && (t.Workspace.Mode == workspaceNew || t.Workspace.Mode == workspaceScratch) && w != nil &&
		(w.Created || w.Reused || t.nodeDispatchedWorkspace()) {
		if w.Machine != t.Machine {
			return i18n.ErrEnglish("assistant-tasks_workspace.machine_local", i18n.Args{"from": w.Machine, "to": t.Machine})
		}
		return nil
	}
	var sourceCwd, sourceAsked string // the inferred cwd's workspace, and the other task that writes there
	var owner *Todo
	var prepared bool
	var prep proto.WorktreePrepare
	var prepErr error
	t.cwdStatus, t.cwdStatusOf, t.cwdStatusResolved = nil, "", "" // never an answer about a folder an earlier resolve asked of
	if t.Workspace == nil || t.WorkspaceInferred {
		mode := workspaceNone
		queueExisting := false
		if !t.Readonly {
			if repoIDs := s.inferredSharedBranchContainerRepos(ctx, *t); len(repoIDs) >= 2 {
				t.Workspace = &TaskWorkspace{
					Mode:      workspaceContainer,
					Container: &ContainerWorkspaceSelection{SelectedRepoIDs: repoIDs},
				}
				t.WorkspaceInferred = true
				return s.resolveContainerTask(ctx, t)
			}
			st, err := s.taskWorktreeStatus(ctx, t.Machine, t.Cwd, 12*time.Second)
			if err != nil {
				return fmt.Errorf("%s: %w", i18n.T(s.lang(ctx), "assistant-tasks_workspace.infer", i18n.Args{"machine": t.Machine}), err)
			}
			// What the machine says of t.Cwd, for resolveTaskRepo: used only while t runs in that folder (or its checkout).
			t.cwdStatus, t.cwdStatusOf = &st, t.Machine+"\x00"+proto.NodeClean(t.Cwd)
			if st.Path != "" {
				mode = workspaceExisting
				_, hasSharedRepo := s.anySharedBranchRepository(ctx)
				eagerPrepare := !hasSharedRepo && s.taskWorkspacePreference(ctx) == preferNew
				var source proto.Workspace
				var sourceErr error
				if eagerPrepare {
					// With prefer-new and no shared-branch repository overrides, mode is guaranteed workspaceNew;
					// overlap workspace.resolve and worktree.prepare to save a serial round-trip. A node that prepares
					// the workspace itself never gets the engine call (previewDispatchedWorkspace's rule, asked first).
					prepared = true
					var engineSkipped bool
					prepCtx, cancelPrep := context.WithCancel(ctx)
					var wg sync.WaitGroup
					wg.Add(1)
					go func() {
						defer wg.Done()
						if prepareOnNode && !s.enginePathAllowed(prepCtx, t) {
							engineSkipped = true
							return
						}
						prepErr = s.prepareTaskWorktree(prepCtx, t, map[string]any{"cwd": t.Cwd}, &prep)
					}()
					source, sourceErr = s.taskWorkspaceResolve(ctx, t.Machine, t.Cwd)
					if sourceErr != nil || !source.Available || !proto.NodeAbs(source.Cwd) {
						cancelPrep()
					}
					wg.Wait()
					cancelPrep()
					prepared = !engineSkipped // skipped: the engine is asked later only if the node turns out to need it
				} else {
					source, sourceErr = s.taskWorkspaceResolve(ctx, t.Machine, t.Cwd)
				}
				if sourceErr != nil {
					return fmt.Errorf("%s: %w", i18n.T(s.lang(ctx), "assistant-tasks_workspace.resolve", i18n.Args{"path": t.Cwd}), sourceErr)
				}
				if !source.Available || !proto.NodeAbs(source.Cwd) {
					return i18n.ErrEnglish("assistant-tasks_workspace.unavailable", i18n.Args{"path": t.Cwd})
				}
				sourceAsked, sourceCwd = t.Cwd, source.Cwd
				t.cwdStatusResolved = t.Machine + "\x00" + proto.NodeClean(source.Cwd)
				s.aliasTaskWorktreeStatus(t.Machine, t.Cwd, source.Cwd)
				owner = s.taskWorkspaceOwner(ctx, t.Machine, source.Cwd, firstNonEmpty(t.TodoID, t.ReuseTodo), false)
				if hasSharedRepo && s.resolvedTaskWorkflow(ctx, *t) == repository.TaskWorkflowSharedBranch {
					mode = workspaceExisting
					queueExisting = true
				} else if eagerPrepare || s.taskWorkspacePreference(ctx) == preferNew || owner != nil {
					mode = workspaceNew
				}
			}
		}
		t.Workspace = &TaskWorkspace{Mode: mode}
		if mode == workspaceExisting {
			t.Workspace.Existing = t.Cwd
			t.Workspace.Queue = queueExisting
		}
		t.WorkspaceInferred = true
	}
	res := &TaskWorkspaceResolved{Machine: t.Machine, Path: proto.NodeClean(t.Cwd)}
	switch t.Workspace.Mode {
	case workspaceNone:
	case workspaceScratch:
		scratchRes, err := s.prepareScratchWorkspace(ctx, t, true)
		if err != nil {
			return err
		}
		res.Path = proto.NodeClean(scratchRes.Path)
	case workspaceExisting:
		selected := t.Workspace.Existing
		if selected == "" {
			return fmt.Errorf("workspace.existing requires a cwd or workspace_id")
		}
		if m, ok := s.machineByName(ctx, t.Machine); ok {
			selected = proto.ExpandHome(selected, m.Home)
		}
		if sourceCwd != "" && (selected == sourceAsked || proto.NodeClean(selected) == proto.NodeClean(sourceAsked)) {
			res.Path = proto.NodeClean(sourceCwd)
			break
		}
		if !proto.NodeAbs(selected) {
			var list proto.WorkspacesResponse
			if err := s.call(ctx, t.Machine, "workspaces.list", map[string]any{}, &list); err != nil {
				return fmt.Errorf("%s: %w", i18n.T(s.lang(ctx), "assistant-tasks_workspace.list", i18n.Args{"machine": t.Machine}), err)
			}
			var found bool
			for _, w := range list.Workspaces {
				qualified := qualifyWorkspace(t.Machine, w)
				if w.ID == selected || qualified.ID == selected {
					if !w.Available || w.Cwd == "" {
						return i18n.ErrEnglish("assistant-tasks_workspace.catalog_unavailable", i18n.Args{"workspace": selected, "machine": t.Machine})
					}
					selected, res.WorkspaceID, res.Branch = w.Cwd, w.ID, w.Branch
					found = true
					break
				}
			}
			if !found {
				return i18n.ErrEnglish("assistant-tasks_workspace.not_found", i18n.Args{"workspace": selected, "machine": t.Machine})
			}
		}
		// terminal: the directory as the Agent's terminal opens it (a home reached through a link keeps its own
		// spelling, #10365); older nodes answer the physical path as before.
		resolved, err := s.taskWorkspaceResolve(ctx, t.Machine, selected)
		if err != nil {
			return fmt.Errorf("%s: %w", i18n.T(s.lang(ctx), "assistant-tasks_workspace.resolve", i18n.Args{"path": selected}), err)
		}
		if !resolved.Available || !proto.NodeAbs(resolved.Cwd) {
			return i18n.ErrEnglish("assistant-tasks_workspace.unavailable", i18n.Args{"path": selected})
		}
		res.Path = proto.NodeClean(resolved.Cwd)
	case workspaceNew:
		if prepareOnNode {
			resolved, err := s.previewDispatchedWorkspace(ctx, t, res)
			if err != nil {
				return err
			}
			if resolved {
				t.WorkspaceResolved = res
				return nil
			}
		}
		res.Branch = firstNonEmpty(t.Workspace.Branch, "task/"+firstNonEmpty(t.TodoID, t.ReservedTodoID))
		if !prepared {
			prepErr = s.prepareTaskWorktree(ctx, t, map[string]any{"cwd": t.Cwd}, &prep)
		}
		if prepErr == nil {
			if !proto.NodeAbs(prep.Path) {
				return i18n.ErrEnglish("assistant-tasks_workspace.engine_path")
			}
			res.Path = proto.NodeJoin(prep.Path, herdr.WorktreeDirName(res.Branch))
		}
		if prepErr != nil {
			if repositoryWithoutCommits(prepErr) {
				// Nothing is committed, so no checkout can be made; the folder itself is the only workspace (#5548).
				if !t.WorkspaceInferred || sourceCwd == "" {
					return i18n.ErrEnglish("assistant-tasks_workspace.no_commits", i18n.Args{"machine": t.Machine, "path": t.Cwd})
				}
				if owner != nil {
					return i18n.ErrEnglish("assistant-tasks_workspace.busy_no_commits",
						i18n.Args{"id": owner.ID, "title": owner.Title, "machine": t.Machine, "path": sourceCwd})
				}
				res.Branch = ""
				t.Workspace, res.Path = &TaskWorkspace{Mode: workspaceExisting, Existing: t.Cwd}, proto.NodeClean(sourceCwd)
				break
			}
			if t.WorkspaceInferred && sourceCwd != "" && engineCannotPrepare(prepErr) {
				// An engine without checkouts (Paseo, Cindy, or a host without checkout support) runs the task in its cwd,
				// as tasks did before workspaces: a busy cwd is shared rather than claimed, a free one is reused.
				res.Branch = ""
				if owner != nil {
					t.Workspace, res.Shared = &TaskWorkspace{Mode: workspaceNone}, true
				} else {
					t.Workspace, res.Path = &TaskWorkspace{Mode: workspaceExisting, Existing: t.Cwd}, proto.NodeClean(sourceCwd)
				}
				break
			}
			return fmt.Errorf("%s: %w", i18n.T(s.lang(ctx), "assistant-tasks_workspace.prepare", i18n.Args{"machine": t.Machine}), prepErr)
		}
	default:
		return fmt.Errorf("unknown task workspace policy %q", t.Workspace.Mode)
	}
	t.WorkspaceResolved = res
	return nil
}

// engineCannotPrepare: the machine's engine has no checkouts to prepare (a runtime such as Paseo or Cindy, or a host
// without checkout support), as the node's error code says; any other failure, a missing host included, stays an error.
func engineCannotPrepare(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == proto.CodeWorktreeUnsupported
}

// repositoryWithoutCommits: the node refused a checkout because the cwd's repository has no commit yet.
func repositoryWithoutCommits(err error) bool {
	if p, ok := errors.AsType[*taskworkspace.Problem](err); ok {
		return p.Code == "no_commits"
	}
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == proto.CodeWorktreeNoCommits
}

// taskWorkspaceOwner checks durable active task links, and the gap between
// admission and linking. Callers that admit a writer hold workspaceMu as well.
// Directories compare by taskDirKey: on a Windows machine, case aside, and a
// linked home's two spellings as one. Branch-checkout reuse also checks owners
// below the checkout root.
func (s *Service) taskWorkspaceOwner(ctx context.Context, machine, cwd, except string, descendants bool) *Todo {
	key := s.taskDirKey(ctx, machine)
	machine, cwd = s.effectiveBoardMachine(machine), key(cwd)
	matches := func(dir string) bool {
		dir = key(dir)
		return dir == cwd || descendants && strings.HasPrefix(strings.ReplaceAll(dir, `\`, "/"),
			strings.TrimRight(strings.ReplaceAll(cwd, `\`, "/"), "/")+"/")
	}
	s.tasks.mu.Lock()
	owner := s.tasks.workspaceClaims[machine+"\x00"+cwd]
	if (owner == "" || owner == except) && descendants {
		for key, id := range s.tasks.workspaceClaims {
			m, dir, ok := strings.Cut(key, "\x00")
			if ok && m == machine && id != except && matches(dir) {
				owner = id
				break
			}
		}
	}
	s.tasks.mu.Unlock()
	if owner != "" && owner != except {
		if t, ok := s.todoNow(owner); ok {
			return &t
		}
	}
	for _, t := range s.todosOf(ctx, "*") {
		if t.ID == except || t.DoneAt != nil {
			continue
		}
		for _, a := range t.Agents {
			if a.ReleasedAt == nil && a.DismissedAt == nil && s.effectiveBoardMachine(a.MachineID) == machine && matches(a.Cwd) {
				return &t
			}
		}
	}
	return nil
}

// taskDirKey is how task folders on machine compare: proto.NodeKey's form, with a directory under the home as its
// links resolve (the node's HomeReal) written under the home as its node names it. A node names the folder an Agent
// starts in by the home's own spelling (workspace.resolve terminal, #10365), while a claim or an Agent's folder from
// before, from an older node or from a terminal host that reports it physically has the resolved one: either must
// find the same writer.
func (s *Service) taskDirKey(ctx context.Context, machine string) func(string) string {
	m, ok := s.machineByName(ctx, machine)
	if effective := s.effectiveBoardMachine(machine); !ok && effective != machine {
		m, _ = s.machineByName(ctx, effective)
	}
	return func(dir string) string { return homeDirKey(dir, m.Home, m.HomeReal) }
}

// homeDirKey is proto.NodeKey(dir), with dir under real (a home as its links resolve) written under home; the key
// alone when either home is unknown, they are one path, or are of different systems.
func homeDirKey(dir, home, real string) string {
	dir = proto.NodeKey(dir)
	if !proto.NodeAbs(home) || !proto.NodeAbs(real) || proto.WindowsPath(home) != proto.WindowsPath(real) {
		return dir
	}
	home, real = proto.NodeKey(home), proto.NodeKey(real)
	sep := "/"
	if proto.WindowsPath(real) {
		sep = `\`
	}
	under := strings.TrimSuffix(real, sep) + sep
	if home == real || strings.HasPrefix(home, under) { // a home inside its own target is no spelling to fold
		return dir
	}
	if dir == real {
		return home
	}
	if rest, ok := strings.CutPrefix(dir, under); ok {
		return strings.TrimSuffix(home, sep) + sep + rest
	}
	return dir
}

func (s *Service) taskWorkspaceBusy(ctx context.Context, machine, cwd, except string) error {
	if owner := s.taskWorkspaceOwner(ctx, machine, cwd, except, false); owner != nil {
		return i18n.ErrEnglish("assistant-tasks_workspace.busy", i18n.Args{"id": owner.ID, "title": owner.Title, "machine": machine, "path": cwd})
	}
	return nil
}

// admitTaskWorkspace resolves at the final placement and claims the actual cwd
// before any session can start.
func (s *Service) admitTaskWorkspace(ctx context.Context, t *PlanTask) error {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	if t.backendWorkspace() {
		return s.admitBackendTask(ctx, t)
	}
	if err := s.recoverDispatchedWorkspace(ctx, t); err != nil {
		return err
	}
	if t.nodeDispatchedWorkspace() {
		if t.WorkspaceResolved.Machine != t.Machine {
			return i18n.ErrEnglish("assistant-tasks_workspace.machine_local", i18n.Args{"from": t.WorkspaceResolved.Machine, "to": t.Machine})
		}
		return s.admitDispatchedWorkspace(ctx, t)
	}
	s.tasks.workspaceMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.tasks.workspaceMu.Unlock()
		}
	}()
	prepared := t.WorkspaceResolved != nil && (t.WorkspaceResolved.Created || t.WorkspaceResolved.Reused)
	if prepared && t.WorkspaceResolved.Machine != t.Machine {
		return i18n.ErrEnglish("assistant-tasks_workspace.machine_local", i18n.Args{"from": t.WorkspaceResolved.Machine, "to": t.Machine})
	}
	if t.scratchWorkspace() {
		if t.ReservedTodoID == "" {
			t.ReservedTodoID = firstNonEmpty(t.ReuseTodo, t.TodoID, "todo_"+randomHex(8))
		}
		scratchRes, err := s.prepareScratchWorkspace(ctx, t, false)
		if err != nil {
			var re *rpc.Error
			var line *i18n.Message
			if (errors.As(err, &re) && (re.Code == "scratch_unsupported" || re.Code == "agent_assistant_workspace")) ||
				(errors.As(err, &line) && line.Key == "rpc-errors.agent_assistant_workspace") {
				return err
			}
			return fmt.Errorf("%s: %w", i18n.T(s.lang(ctx), "assistant-tasks_workspace.scratch_create"), err)
		}
		t.WorkspaceResolved = &TaskWorkspaceResolved{
			Machine: t.Machine,
			Path:    proto.NodeClean(scratchRes.Path),
			Created: scratchRes.Created,
			Reused:  scratchRes.Reused,
		}
		// Once allocated on disk it is machine-local, including automatic and explicit retries.
		t.MachinePolicy = machineFixed
	} else if !prepared {
		if err := s.resolveTaskWorkspaceForDispatch(ctx, t, t.planID != "" && t.inPane == ""); err != nil {
			return err
		}
		if t.Workspace.Mode == workspaceNew {
			if t.dispatchedWorkspace != nil && t.dispatchedWorkspace.plan != nil {
				s.tasks.workspaceMu.Unlock()
				locked = false
				return s.admitDispatchedWorkspace(ctx, t)
			}
			var wt proto.WorktreeCreateResult
			var reused bool
			var err error
			if t.dispatchedWorkspace != nil && t.dispatchedWorkspace.reuse {
				// An explicit branch already has a checkout. Recheck its
				// usability without letting a missing checkout trigger creation.
				wt, reused, err = s.findTaskWorktree(ctx, t)
				if err == nil && !reused {
					err = taskworkspace.Failure("resource_conflict", "admission", "", "", t.WorkspaceResolved.Path)
				}
			} else {
				wt, reused, err = s.newTaskWorktree(ctx, t)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", i18n.T(s.lang(ctx), "assistant-tasks_workspace.create"), err)
			}
			if !proto.NodeAbs(wt.Path) {
				return i18n.ErrEnglish("assistant-tasks_workspace.engine_path")
			}
			t.WorkspaceResolved = &TaskWorkspaceResolved{Machine: t.Machine, Path: proto.NodeClean(wt.Path),
				Branch: firstNonEmpty(wt.Branch, t.WorkspaceResolved.Branch), WorkspaceID: wt.WorkspaceID, Created: !reused, Reused: reused}
			// Once allocated it is machine-local, including automatic and explicit retries.
			t.MachinePolicy = machineFixed
		}
	}
	t.Cwd = t.WorkspaceResolved.Path
	// A shared fallback has mode none, but its resolved marker must still survive the start.
	keep := t.Workspace.Mode != workspaceNone || t.WorkspaceResolved.Shared
	// The repository is the one this folder is in, not the one the plan was made from: an existing workspace, or the
	// checkout the engine made, can be another repository (#8259). Saved below with the admitted cwd, and what
	// "start these directly" remembers. A task without a workspace runs where it was planned: nothing to ask again
	// unless it is to be remembered.
	repo := t.Repo
	if t.repoOf != t.Machine+"\x00"+proto.NodeClean(t.Cwd) && (keep || t.rememberKind || t.grantSchedule != "") {
		s.resolveTaskRepo(ctx, t)
	}
	if t.Workspace.Mode != workspaceNone {
		if owner := s.taskWorkspaceOwner(ctx, t.Machine, t.Cwd, t.TodoID, false); owner != nil {
			if s.shouldQueueExistingWorkspace(ctx, *t, owner) {
				return &workspaceBusyQueueError{
					machine:    s.effectiveBoardMachine(t.Machine),
					path:       t.Cwd,
					ownerID:    owner.ID,
					ownerTitle: owner.Title,
				}
			}
			return i18n.ErrEnglish("assistant-tasks_workspace.busy", i18n.Args{"id": owner.ID, "title": owner.Title, "machine": t.Machine, "path": t.Cwd})
		}
		if t.TodoID != "" {
			claim := s.effectiveBoardMachine(t.Machine) + "\x00" + s.taskDirKey(ctx, t.Machine)(t.Cwd)
			s.tasks.mu.Lock()
			if s.tasks.workspaceClaims == nil {
				s.tasks.workspaceClaims = map[string]string{}
			}
			s.tasks.workspaceClaims[claim] = t.TodoID
			s.tasks.mu.Unlock()
		}
	}
	if t.planID != "" && keep {
		// Persist the engine's answer before starting, so a retry never creates a second tree.
		return s.patchPlanTask(ctx, t.planID, t.planIndex, func(pt *PlanTask) {
			pt.Cwd, pt.Repo, pt.Workspace, pt.WorkspaceResolved = t.Cwd, t.Repo, t.Workspace, t.WorkspaceResolved
			pt.WorkspaceInferred, pt.ReservedTodoID, pt.MachinePolicy = t.WorkspaceInferred, t.ReservedTodoID, t.MachinePolicy
		})
	}
	if t.planID != "" && t.Repo != repo { // no workspace, same folder: only what its repository is came out otherwise
		return s.patchPlanTask(ctx, t.planID, t.planIndex, func(pt *PlanTask) { pt.Repo = t.Repo })
	}
	return nil
}

func (s *Service) prepareTaskWorktree(ctx context.Context, t *PlanTask, params map[string]any, out *proto.WorktreePrepare) error {
	if kind := s.taskWorktreeKind(ctx, t); kind != "" {
		params["kind"] = kind
	}
	return s.call(ctx, t.Machine, DeviceWorktreePrepare, params, out)
}

// findTaskWorktree reuses the idle checkout of its branch. Explicit branches must be clean
// (#8357). The to-do's generated branch can retain untracked files in the checkouts folder. A node that cannot
// look up the branch still falls back to normal creation.
func (s *Service) findTaskWorktree(ctx context.Context, t *PlanTask) (proto.WorktreeCreateResult, bool, error) {
	branch, own := t.Workspace.Branch, false
	if branch == "" {
		branch, own = t.WorkspaceResolved.Branch, true
	}
	if branch != "" {
		var prep proto.WorktreePrepare
		err := s.prepareTaskWorktree(ctx, t, map[string]any{"cwd": t.Cwd, "branch": branch, "allow_untracked": own}, &prep)
		switch at := prep.Existing; {
		case err != nil && !own:
			return proto.WorktreeCreateResult{}, false, err
		case err != nil:
			if refused := ownCheckoutRefusal(err); refused != nil {
				return proto.WorktreeCreateResult{}, false, refused
			}
		case at != nil:
			if !proto.NodeAbs(at.Path) || at.Branch != branch {
				return proto.WorktreeCreateResult{}, false, i18n.ErrEnglish("assistant-tasks_workspace.engine_checkout")
			}
			// in_checkouts from the node; an older one leaves it out, and its path then says it as written.
			if own && !at.InCheckouts && !nodePathWithin(at.Path, prep.Path) {
				return proto.WorktreeCreateResult{}, false, i18n.ErrEnglish("assistant-tasks_workspace.own_checkout_elsewhere",
					i18n.Args{"branch": branch, "path": at.Path})
			}
			if owner := s.taskWorkspaceOwner(ctx, t.Machine, at.Path, t.TodoID, true); owner != nil {
				return proto.WorktreeCreateResult{}, false, i18n.ErrEnglish("assistant-tasks_workspace.busy",
					i18n.Args{"id": owner.ID, "title": owner.Title, "machine": t.Machine, "path": at.Path})
			}
			return proto.WorktreeCreateResult{Path: at.Path, Branch: at.Branch}, true, nil
		}
	}
	return proto.WorktreeCreateResult{}, false, nil
}

func (s *Service) newTaskWorktree(ctx context.Context, t *PlanTask) (proto.WorktreeCreateResult, bool, error) {
	if wt, reused, err := s.findTaskWorktree(ctx, t); reused || err != nil {
		return wt, reused, err
	}
	kind := s.taskWorktreeKind(ctx, t)
	wt, err := s.newWorktreeOn(ctx, t.Machine, t.Cwd, t.WorkspaceResolved.Branch, kind, s.taskCheckoutOnly(ctx, t, kind))
	if t.Workspace.Branch == "" && err != nil {
		if path, taken := pathTaken(err); taken {
			return wt, false, taskCheckoutRefusal{i18n.ErrEnglish("assistant-tasks_workspace.own_path_taken",
				i18n.Args{"path": path}).(*i18n.Message), err}
		}
	}
	return wt, false, err
}

// ownCheckoutRefusal is the hub's sentence for the node's coded refusal of the to-do's own leftover checkout (dirty,
// in use, changed or unverifiable), naming its path and what to do next; nil for any other failure.
func ownCheckoutRefusal(err error) error {
	var re *rpc.Error
	if !errors.As(err, &re) {
		return nil
	}
	var data struct{ Branch, Path, Owner, Files string }
	_ = json.Unmarshal(re.Data, &data)
	args := i18n.Args{"branch": data.Branch, "path": data.Path, "owner": data.Owner}
	var key string
	switch re.Code {
	case proto.CodeCheckoutDirty:
		key = "own_checkout_dirty"
		if data.Files != "" {
			key = "own_checkout_dirty_files"
			args["files"] = data.Files
		}
		message := i18n.ErrEnglish("assistant-tasks_workspace."+key, args).(*i18n.Message)
		return fmt.Errorf("%w (%s)", taskCheckoutRefusal{message, err}, data.Path)
	case proto.CodeCheckoutInUse:
		key = "own_checkout_in_use"
	case proto.CodeCheckoutChanged, proto.CodeCheckoutUnverified:
		key = "own_checkout_unverified"
	default:
		return nil
	}
	return taskCheckoutRefusal{i18n.ErrEnglish("assistant-tasks_workspace."+key, args).(*i18n.Message), err}
}

// pathTaken: the node refused to make the checkout because its folder already holds something (code
// worktree_path_taken), and where.
func pathTaken(err error) (string, bool) {
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeWorktreePathTaken {
		return "", false
	}
	var data struct{ Path string }
	_ = json.Unmarshal(re.Data, &data)
	return data.Path, data.Path != ""
}

// taskCheckoutRefusal reads as the hub's sentence and keeps the node's coded refusal under it, so the task's error
// still carries that code (errors.As) for the clients that map codes first.
type taskCheckoutRefusal struct {
	*i18n.Message
	node error
}

func (e taskCheckoutRefusal) Unwrap() []error { return []error{e.Message, e.node} }

// nodePathWithin: the node's path p is dir or lies under it, compared as the node's paths are (proto.NodeKey).
func nodePathWithin(p, dir string) bool {
	if dir == "" {
		return false
	}
	p, dir = strings.ReplaceAll(proto.NodeKey(p), `\`, "/"), strings.TrimRight(strings.ReplaceAll(proto.NodeKey(dir), `\`, "/"), "/")
	return p == dir || strings.HasPrefix(p, dir+"/")
}

func (s *Service) releaseWorkspaceClaimsOnly(todoID string) []workspaceTarget {
	if todoID == "" {
		return nil
	}
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	var released []workspaceTarget
	for key, owner := range s.tasks.workspaceClaims {
		if owner == todoID {
			delete(s.tasks.workspaceClaims, key)
			if m, dir, ok := strings.Cut(key, "\x00"); ok && m != "" && dir != "" {
				released = append(released, workspaceTarget{machine: m, path: dir})
			}
		}
	}
	return released
}

func (s *Service) releaseTaskWorkspace(todoID string) {
	released := s.releaseWorkspaceClaimsOnly(todoID)
	if len(released) > 0 {
		s.drainReleasedWorkspaces(released)
	}
}

// Preview uses the saved task as its trusted base, applying only card-editable
// fields. It neither changes the saved plan nor allocates a host workspace.
func (s *Service) previewPlanWorkspaces(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, fmt.Errorf("read-only role cannot edit task workspaces"))
		return
	}
	p, err := s.loadPlan(r.Context(), r.PathValue("id"))
	if err != nil {
		planErr(w, err)
		return
	}
	if p.State != planOpen {
		planErr(w, errPlanNotOpen)
		return
	}
	var in struct {
		Tasks []PlanTask `json:"tasks"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	for i := range p.Tasks {
		if i < len(in.Tasks) {
			applyPlanEdit(&p.Tasks[i], in.Tasks[i])
		}
		if !s.placeTask(r.Context(), &p.Tasks[i]) {
			apierr.Write(w, http.StatusBadRequest, fmt.Errorf("unknown machine %s", p.Tasks[i].Machine))
			return
		}
		if err := s.resolveTaskWorkspace(r.Context(), &p.Tasks[i]); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
	}
	apierr.WriteJSON(w, map[string]any{"plan": p})
}
