package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

type taskWorkspaceStore interface {
	TaskWorkspace(context.Context, string, string) (*taskworkspace.Record, error)
	BeginTaskWorkspace(context.Context, string, string, string, ...*taskworkspace.Record) (*taskworkspace.Record, error)
	MutateTaskWorkspace(context.Context, string, string, int64, string, string) (*taskworkspace.Record, error)
	SaveTaskWorkspaceResult(context.Context, string, string, int64, taskworkspace.Record) (*taskworkspace.Record, error)
}
type BackendWorkspaceInspection struct {
	Workspace   *taskworkspace.Record  `json:"workspace"`
	Observation *taskworkspace.Record  `json:"observation,omitempty"`
	Error       *taskworkspace.Problem `json:"error,omitempty"`
}

func (s *Service) backendWorkspaceStore() (taskWorkspaceStore, error) {
	st, ok := s.store().(taskWorkspaceStore)
	if !ok {
		return nil, taskworkspace.Failure("store_unavailable", "manager", "", "", "")
	}
	return st, nil
}
func (s *Service) backendWorkspace(ctx context.Context, id, op string) (taskWorkspaceStore, *taskworkspace.Record, error) {
	st, err := s.backendWorkspaceStore()
	if err != nil {
		return nil, nil, err
	}
	r, err := st.TaskWorkspace(ctx, id, op)
	if err != nil {
		return st, nil, err
	}
	if r == nil {
		return st, nil, taskworkspace.Failure("not_found", "manager", op, "", "")
	}
	if r.Plan.Source.Manager != s.managerIdentity() {
		return st, nil, taskworkspace.Failure("identity_mismatch", "manager", op, "", "")
	}
	return st, r, nil
}
func (s *Service) backendWorkspaceCall(ctx context.Context, method string, req taskworkspace.Request) (taskworkspace.Record, error) {
	var result taskworkspace.Record
	timeout := taskworkspace.RPCTimeout
	if method == "prepare" {
		remoteRepos := 0
		for _, source := range req.Plan.Source.Repositories.Repositories {
			if source.Local == nil {
				remoteRepos++
			}
		}
		timeout = taskworkspace.PrepareTimeout(remoteRepos)
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := s.call(callCtx, req.Plan.Source.MachineID, taskworkspace.MethodPrefix+method, req, &result)
	if err != nil {
		return result, taskworkspace.Failure("operation_unknown", method, req.OperationID, "", req.Plan.RootPath)
	}
	if result.Error != nil && result.WorkspaceID == "" {
		return result, result.Error
	}
	return result, nil
}

func (s *Service) backendWorkspaceCapabilities(ctx context.Context, machine, op, cwd string) (taskworkspace.Capabilities, error) {
	var support taskworkspace.Capabilities
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := s.call(ctx, machine, taskworkspace.MethodPrefix+"capabilities", nil, &support)
	if err == nil {
		return support, nil
	}
	code := "node_unavailable"
	if strings.Contains(err.Error(), "unknown method "+taskworkspace.MethodPrefix) {
		code = "unsupported"
	}
	return support, taskworkspace.Failure(code, "capability", op, "", cwd)
}

// PrepareBackendWorkspace consumes an already saved naming plan. It never
// starts an Engine, edits a legacy workspace or changes the task's business stage.
func (s *Service) PrepareBackendWorkspace(ctx context.Context, id, op string) (*taskworkspace.Record, error) {
	return s.prepareBackendWorkspace(ctx, id, op)
}

func (s *Service) prepareBackendWorkspace(ctx context.Context, id, op string, replacing ...*taskworkspace.Record) (*taskworkspace.Record, error) {
	st, err := s.backendWorkspaceStore()
	if err != nil {
		return nil, err
	}
	naming, ok := s.store().(workspaceNamingStore)
	if !ok {
		return nil, taskworkspace.Failure("store_unavailable", "plan", op, "", "")
	}
	plan, err := naming.WorkspaceNamingPlan(ctx, id, op)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, taskworkspace.Failure("not_found", "plan", op, "", "")
	}
	if _, ok := s.device(ctx, plan.Source.MachineID); !ok {
		return nil, taskworkspace.Failure("node_unavailable", "plan", op, "", "")
	}
	support, err := s.backendWorkspaceCapabilities(ctx, plan.Source.MachineID, op, "")
	if err != nil {
		return nil, err
	}
	if support.Capability != taskworkspace.Capability || len(plan.Repositories) == 0 && !support.EmptyRepositories {
		return nil, taskworkspace.Failure("unsupported", "capability", op, "", "")
	}
	if slices.ContainsFunc(plan.Source.Repositories.Repositories, func(repo repository.PreparationRepository) bool { return repo.Local != nil }) && !support.LocalGit {
		return nil, taskworkspace.Failure("unsupported", "capability", op, "", "")
	}
	current, err := st.TaskWorkspace(ctx, id, "")
	if err != nil {
		return nil, err
	}
	if current != nil && current.State == "cancelled" && current.NodeOperationID == "" {
		settled, err := s.CancelBackendWorkspace(ctx, id, current.OperationID)
		if err != nil {
			return settled, err
		}
		if settled.Error != nil && !(settled.Error.Code == "operation_conflict" && settled.NodeOperationID != "") {
			return settled, nil
		}
	}
	r, err := st.BeginTaskWorkspace(ctx, id, op, s.managerIdentity(), replacing...)
	if err != nil {
		return nil, err
	}
	if r.State == "cancelled" || r.State == "removed" {
		return r, nil
	}
	if _, ok := s.device(ctx, r.Plan.Source.MachineID); !ok {
		return r, taskworkspace.Failure("node_unavailable", "prepare", r.OperationID, "", "")
	}
	if r.PreviousOperationID != "" {
		observed, inspectErr := s.backendWorkspaceCall(ctx, "inspect", r.Request())
		// A conflict echoes the requested operation and digest. Only the Node's
		// recorded owner identifies a correction it has already adopted.
		adopted := inspectErr == nil && observed.NodeOperationID == r.OperationID && observed.OperationID == r.OperationID && observed.InputDigest == r.InputDigest
		if !adopted {
			prior, err := st.TaskWorkspace(ctx, id, r.PreviousOperationID)
			if err != nil {
				return r, err
			}
			if prior == nil {
				return r, taskworkspace.Failure("operation_conflict", "prepare", op, "", "")
			}
			old, err := s.backendWorkspaceCall(ctx, "cancel", prior.Request())
			if err != nil {
				return r, err
			}
			if old.Error != nil {
				return r, old.Error
			}
		}
	}
	req := r.Request()
	req.MirrorRepos = s.prepareEnabledMirrorsForWorkspace(ctx, r)
	result, err := s.backendWorkspaceCall(ctx, "prepare", req)
	if err != nil {
		result = *r
		result.Error = err.(*taskworkspace.Problem)
	}
	if result.Error != nil && result.Error.Code == "operation_busy" {
		r.Error = result.Error
		return r, nil
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return st.SaveTaskWorkspaceResult(saveCtx, id, r.OperationID, r.Revision, result)
}

// InspectBackendWorkspace preserves the distinction between persisted business
// state and live Node observations, including an unavailable or unknown outcome.
func (s *Service) InspectBackendWorkspace(ctx context.Context, id, op string) (BackendWorkspaceInspection, error) {
	_, r, err := s.backendWorkspace(ctx, id, op)
	if err != nil {
		return BackendWorkspaceInspection{}, err
	}
	view := BackendWorkspaceInspection{Workspace: r}
	result, err := s.backendWorkspaceCall(ctx, "inspect", r.Request())
	if err != nil {
		view.Error = err.(*taskworkspace.Problem)
		return view, nil
	}
	view.Observation = &result
	return view, nil
}
func (s *Service) CancelBackendWorkspace(ctx context.Context, id, op string) (*taskworkspace.Record, error) {
	st, r, err := s.backendWorkspace(ctx, id, op)
	if err != nil {
		return r, err
	}
	r, err = st.MutateTaskWorkspace(ctx, id, r.OperationID, 0, "cancel", "")
	if err != nil {
		return r, err
	}
	observation, err := s.backendWorkspaceCall(ctx, "cancel", r.Request())
	if err != nil {
		r.Error = err.(*taskworkspace.Problem)
	} else {
		r.Error = observation.Error
		if observation.NodeOperationID != "" {
			r.NodeOperationID = observation.NodeOperationID
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			return st.SaveTaskWorkspaceResult(saveCtx, id, r.OperationID, r.Revision, *r)
		}
	}
	return r, nil
}
func (s *Service) CleanupBackendWorkspace(ctx context.Context, id, op string, force, deleteBranch bool) (*taskworkspace.Record, error) {
	st, r, err := s.backendWorkspace(ctx, id, op)
	if err != nil {
		return r, err
	}
	if r.State == "removed" {
		return r, nil
	}
	if slices.ContainsFunc(r.Uses, func(use string) bool { return strings.HasPrefix(use, "launch_") }) {
		if err := s.releaseStoppedBackendUses(ctx, r, ""); err != nil {
			return r, err
		}
		_, r, err = s.backendWorkspace(ctx, id, op)
		if err != nil {
			return r, err
		}
	}
	if r.NodeOperationID != "" && r.NodeOperationID != r.OperationID {
		owned, err := st.TaskWorkspace(ctx, id, r.NodeOperationID)
		if err != nil {
			return r, err
		}
		if owned == nil || owned.WorkspaceID != r.WorkspaceID || owned.NodeOperationID != "" && owned.NodeOperationID != owned.OperationID {
			return r, taskworkspace.Failure("identity_mismatch", "cleanup", r.OperationID, "", r.Plan.RootPath)
		}
		physical, err := s.CleanupBackendWorkspace(ctx, id, owned.OperationID, force, deleteBranch)
		if err != nil {
			return r, err
		}
		if physical.State != "removed" {
			r.Error = physical.Error
			return r, nil
		}
		r, err = st.MutateTaskWorkspace(ctx, id, r.OperationID, r.Revision, "cleanup", "")
		if err != nil {
			return r, err
		}
		result := *r
		result.State = "removed"
		result.Cleanup = physical.Cleanup
		result.Repositories = physical.Repositories
		result.Error = nil
		return st.SaveTaskWorkspaceResult(ctx, id, r.OperationID, r.Revision, result)
	}
	r, err = st.MutateTaskWorkspace(ctx, id, r.OperationID, r.Revision, "cleanup", "")
	if err != nil {
		return r, err
	}
	req := r.Request()
	req.Force = force
	req.DeleteBranch = deleteBranch
	result, err := s.backendWorkspaceCall(ctx, "cleanup", req)
	if err != nil {
		result = *r
		result.Error = err.(*taskworkspace.Problem)
	}
	if result.Error != nil && result.Error.Code == "operation_busy" {
		result = *r
		result.Error = taskworkspace.Failure("operation_unknown", "cleanup", r.OperationID, "", r.Plan.RootPath)
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return st.SaveTaskWorkspaceResult(saveCtx, id, r.OperationID, r.Revision, result)
}

// AcquireBackendWorkspace and CheckBackendWorkspaceUse are T4's admission
// boundary. Keep the use until the session has stopped; unknown calls retain it.
func (s *Service) AcquireBackendWorkspace(ctx context.Context, id, op, use string) (*taskworkspace.Record, error) {
	st, r, err := s.backendWorkspace(ctx, id, op)
	if err != nil {
		return r, err
	}
	r, err = st.MutateTaskWorkspace(ctx, id, r.OperationID, r.Revision, "acquire", use)
	if err != nil {
		return r, err
	}
	req := r.Request()
	req.UseID = use
	result, err := s.backendWorkspaceCall(ctx, "acquire", req)
	if err != nil {
		return r, err
	}
	if result.Error != nil {
		return r, result.Error
	}
	if result.OperationID != r.OperationID || result.WorkspaceID != r.WorkspaceID || result.InputDigest != r.InputDigest || result.State != "ready" || result.Plan.Digest != r.Plan.Digest || workspacenaming.CheckPlan(result.Plan) != nil || taskworkspace.ValidateReady(result) != nil {
		return r, taskworkspace.Failure("result_mismatch", "acquire", r.OperationID, "", r.Plan.RootPath)
	}
	return s.CheckBackendWorkspaceUse(ctx, id, r.OperationID, use)
}
func (s *Service) CheckBackendWorkspaceUse(ctx context.Context, id, op, use string) (*taskworkspace.Record, error) {
	st, err := s.backendWorkspaceStore()
	if err != nil {
		return nil, err
	}
	return st.MutateTaskWorkspace(ctx, id, op, 0, "check", use)
}
func (s *Service) ReleaseBackendWorkspace(ctx context.Context, id, op, use string) (*taskworkspace.Record, error) {
	st, r, err := s.backendWorkspace(ctx, id, op)
	if err != nil {
		return r, err
	}
	req := r.Request()
	req.UseID = use
	result, err := s.backendWorkspaceCall(ctx, "release", req)
	if err != nil {
		return r, err
	}
	if result.Error != nil {
		return r, result.Error
	}
	return st.MutateTaskWorkspace(ctx, id, r.OperationID, r.Revision, "release", use)
}
func (s *Service) ensureBackendWorkspacePlan(ctx context.Context, id string, in namingRequest) error {
	if !workspacenaming.ValidOperationID(in.OperationID) || len(in.WorkspaceRules) > 0 {
		return taskworkspace.Failure("invalid_input", "plan", in.OperationID, "", "")
	}
	st, ok := s.store().(workspaceNamingStore)
	if !ok {
		return taskworkspace.Failure("store_unavailable", "plan", in.OperationID, "", "")
	}
	prior, err := st.WorkspaceNamingPlanForSave(ctx, id, in.OperationID)
	if err != nil {
		return err
	}
	if prior != nil {
		if in.MachineID != "" && in.MachineID != prior.Source.MachineID || in.Layout != "" && in.Layout != prior.Source.Layout {
			return taskworkspace.Failure("operation_conflict", "plan", in.OperationID, "", "")
		}
		if in.SelectedRepoIDs != nil {
			ids := []string{}
			for _, r := range prior.Source.Repositories.Repositories {
				ids = append(ids, r.RepoID)
			}
			if taskworkspace.Hash(ids) != taskworkspace.Hash(backendSortedIDs(in.SelectedRepoIDs)) {
				return taskworkspace.Failure("operation_conflict", "plan", in.OperationID, "", "")
			}
		}
		return nil
	}
	preview, revision, err := s.namingPreview(ctx, id, in, false)
	if err != nil {
		return err
	}
	if preview.Plan == nil {
		return taskworkspace.Failure("naming_incomplete", "plan", in.OperationID, "", "")
	}
	_, err = st.SaveWorkspaceNamingPlan(ctx, id, in.OperationID, revision, *preview.Plan)
	return err
}
func backendWorkspaceError(w http.ResponseWriter, err error) {
	var p *taskworkspace.Problem
	if errors.As(err, &p) {
		status := 400
		switch p.Code {
		case "not_found":
			status = 404
		case "node_unavailable", "store_unavailable":
			status = 503
		case "operation_conflict", "revision_conflict", "not_ready", "worktree_in_use":
			status = 409
		}
		body := apierr.LocalizedBody(status, "task_workspace_"+p.Code, i18n.Err("task-workspaces.invalid"), i18n.OfWriter(w))
		body["problem"] = p
		apierr.WriteBody(w, status, body)
		return
	}
	if errors.Is(err, db.ErrTodoCompleted) {
		backendWorkspaceError(w, taskworkspace.Failure("not_ready", "manager", "", "", ""))
		return
	}
	namingError(w, err)
}
func (s *Service) taskWorkspaceRoutes() {
	s.handle("POST /api/v1/todos/{id}/backend-workspace/start", s.startOwnerBackendPlan)
	s.handle("GET /api/v1/todos/{id}/backend-workspace", func(w http.ResponseWriter, r *http.Request) {
		st, err := s.backendWorkspaceStore()
		if err != nil {
			backendWorkspaceError(w, err)
			return
		}
		record, err := st.TaskWorkspace(r.Context(), r.PathValue("id"), "")
		if err != nil {
			backendWorkspaceError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"workspace": record})
	})
	s.handle("GET /api/v1/todos/{id}/backend-workspace/operations/{operation_id}", func(w http.ResponseWriter, r *http.Request) {
		view, err := s.InspectBackendWorkspace(r.Context(), r.PathValue("id"), r.PathValue("operation_id"))
		if err != nil {
			backendWorkspaceError(w, err)
			return
		}
		apierr.WriteJSON(w, view)
	})
	s.handle(assistantwire.RouteBackendWorkspacePrepare, func(w http.ResponseWriter, r *http.Request) {
		var in namingRequest
		if !namingBody(w, r, &in) {
			return
		}
		id := r.PathValue("id")
		if err := s.ensureBackendWorkspacePlan(r.Context(), id, in); err != nil {
			backendWorkspaceError(w, err)
			return
		}
		record, err := s.PrepareBackendWorkspace(r.Context(), id, in.OperationID)
		if err != nil {
			backendWorkspaceError(w, err)
			return
		}
		apierr.WriteJSON(w, record)
	})
	for _, action := range []string{"cancel", "cleanup"} {
		s.handle("POST /api/v1/todos/{id}/backend-workspace/"+action, func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				OperationID  string `json:"operation_id"`
				Force        bool   `json:"force,omitempty"`
				DeleteBranch bool   `json:"delete_branch,omitempty"`
			}
			if !namingBody(w, r, &in) {
				return
			}
			if !workspacenaming.ValidOperationID(in.OperationID) {
				backendWorkspaceError(w, taskworkspace.Failure("invalid_input", action, in.OperationID, "", ""))
				return
			}
			var record *taskworkspace.Record
			var err error
			if action == "cancel" {
				record, err = s.CancelBackendWorkspace(r.Context(), r.PathValue("id"), in.OperationID)
			} else {
				record, err = s.CleanupBackendWorkspace(r.Context(), r.PathValue("id"), in.OperationID, in.Force, in.DeleteBranch)
			}
			if err != nil {
				backendWorkspaceError(w, err)
				return
			}
			apierr.WriteJSON(w, record)
		})
	}
}

func backendSortedIDs(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

func (s *Service) toolTodoWorkspace(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	var in struct {
		ID              string            `json:"id"`
		Action          string            `json:"action"`
		OperationID     string            `json:"operation_id,omitempty"`
		MachineID       string            `json:"machine_id,omitempty"`
		Layout          repository.Layout `json:"layout,omitempty"`
		SelectedRepoIDs []string          `json:"selected_repo_ids,omitempty"`
		Force           bool              `json:"force,omitempty"`
		DeleteBranch    bool              `json:"delete_branch,omitempty"`
	}
	if err := decodeTaskRepositoryInput(args, &in); err != nil {
		return "", err
	}
	if in.Action != "inspect" && in.Action != "prepare" && in.Action != "cancel" && in.Action != "cleanup" {
		return "", taskworkspace.Failure("invalid_input", "tool", in.OperationID, "", "")
	}
	if in.Action != "cleanup" && (in.Force || in.DeleteBranch) {
		return "", taskworkspace.Failure("invalid_input", "tool", in.OperationID, "", "")
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", err
	}
	todo, ok := s.todoNow(in.ID)
	if !ok {
		return "", db.ErrTodoNotFound
	}
	if in.Action != "inspect" {
		if ok, outcome, err := s.confirmOutbound(ctx, machine, self, "todo_workspace", "", todo.Title, args); err != nil || !ok {
			return todoNotAllowed(outcome, err)
		}
		if !workspacenaming.ValidOperationID(in.OperationID) {
			return "", taskworkspace.Failure("invalid_input", "tool", in.OperationID, "", "")
		}
	}
	var result any
	var err error
	switch in.Action {
	case "inspect":
		result, err = s.InspectBackendWorkspace(ctx, in.ID, in.OperationID)
	case "prepare":
		err = s.ensureBackendWorkspacePlan(ctx, in.ID, namingRequest{MachineID: in.MachineID, Layout: in.Layout, SelectedRepoIDs: in.SelectedRepoIDs, OperationID: in.OperationID})
		if err == nil {
			result, err = s.PrepareBackendWorkspace(ctx, in.ID, in.OperationID)
		}
	case "cancel":
		result, err = s.CancelBackendWorkspace(ctx, in.ID, in.OperationID)
	case "cleanup":
		result, err = s.CleanupBackendWorkspace(ctx, in.ID, in.OperationID, in.Force, in.DeleteBranch)
	}
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(result)
	return string(raw), err
}
