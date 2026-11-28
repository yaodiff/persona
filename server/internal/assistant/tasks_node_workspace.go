package assistant

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

type dispatchedWorkspacePlan struct {
	plan     *workspacenaming.Plan
	revision int64
	reuse    bool
}

func (t *PlanTask) nodeDispatchedWorkspace() bool {
	return t.Workspace != nil && t.Workspace.Mode == workspaceNew &&
		t.WorkspaceResolved != nil && t.WorkspaceResolved.OperationID != ""
}

func (t *PlanTask) usesPreparedWorkspace() bool {
	return t.backendWorkspace() || t.nodeDispatchedWorkspace()
}

// capabilityUnsupported: the node answered that it prepares no task workspaces (an older node).
func capabilityUnsupported(err error) bool {
	problem, ok := errors.AsType[*taskworkspace.Problem](err)
	return ok && problem.Code == "unsupported"
}

// nodePreparesTaskWorkspaces: the node prepares a new dispatch's workspace itself.
func nodePreparesTaskWorkspaces(support taskworkspace.Capabilities) bool {
	return support.Capability == taskworkspace.Capability && support.LocalGit && support.Start
}

// enginePathAllowed asks the node what previewDispatchedWorkspace will ask, before it, for a caller that would call the
// engine early: only a negative answer allows the engine path. A positive answer or a failed lookup leaves the
// decision to previewDispatchedWorkspace.
func (s *Service) enginePathAllowed(ctx context.Context, t *PlanTask) bool {
	support, err := s.backendWorkspaceCapabilities(ctx, t.Machine, "", t.Cwd)
	if err != nil {
		return capabilityUnsupported(err)
	}
	return !nodePreparesTaskWorkspaces(support)
}

// Preview a new dispatch without allocating a path. Only a negative capability
// answer permits the engine path. A failed lookup can be retried on the same node.
func (s *Service) previewDispatchedWorkspace(ctx context.Context, t *PlanTask, resolved *TaskWorkspaceResolved) (bool, error) {
	support, err := s.backendWorkspaceCapabilities(ctx, t.Machine, "", t.Cwd)
	if err != nil {
		if capabilityUnsupported(err) {
			return false, nil
		}
		return false, err
	}
	if !nodePreparesTaskWorkspaces(support) {
		return false, nil
	}
	naming, ok := s.store().(workspaceNamingStore)
	if !ok {
		return false, taskworkspace.Failure("store_unavailable", "plan", "", "", "")
	}
	settings, ok := s.savedSettings(ctx)
	if !ok {
		return false, taskworkspace.Failure("store_unavailable", "plan", "", "", "")
	}
	id := firstNonEmpty(t.TodoID, t.ReuseTodo, t.ReservedTodoID)
	source := workspacenaming.Source{
		Manager: s.managerIdentity(), TodoID: id, Title: t.Title, MachineID: t.Machine,
		Timezone: settings.Timezone, PlannedAt: time.Now().UTC(), Layout: repository.LayoutContainer, TaskBranch: t.Workspace.Branch,
	}
	var rules workspacenaming.Rules
	var rulesRevision int64
	var set repository.TaskSet
	if t.TodoID != "" || t.ReuseTodo != "" {
		todo, ok := s.todoNow(id)
		if !ok {
			return false, db.ErrTodoNotFound
		}
		source.Title = firstNonEmpty(todo.Title, t.Title)
		rec, err := naming.TodoWorkspaceRules(ctx, id)
		if err != nil {
			return false, err
		}
		rules, rulesRevision = rec.Rules, rec.Revision
		set, err = s.savedTodoRepositories(ctx, id)
		if err != nil {
			return false, err
		}
	}
	source.Rules, source.RuleSources = workspacenaming.Effective(settings.WorkspaceRules, rules)
	var selected []string
	for _, row := range set.Repositories {
		if row.SourceSnapshot.CloneURL != "" {
			selected = append(selected, row.RepoID)
		}
	}
	if len(selected) == 0 {
		var local taskworkspace.LocalSource
		if err := s.call(ctx, t.Machine, taskworkspace.LocalSourceMethod,
			taskworkspace.LocalSourceRequest{Cwd: t.Cwd, TaskBranch: t.Workspace.Branch}, &local); err != nil {
			return false, err
		}
		if local.Error != "" {
			return false, taskworkspace.Failure(string(local.Error), "source", "", "", t.Cwd)
		}
		if local.ExistingPath != "" {
			if t.Workspace.Branch == "" || !proto.NodeAbs(local.ExistingPath) {
				return false, taskworkspace.Failure("result_mismatch", "plan", "", "", local.ExistingPath)
			}
			resolved.Path, resolved.Branch = local.ExistingPath, t.Workspace.Branch
			t.dispatchedWorkspace = &dispatchedWorkspacePlan{reuse: true}
			return true, nil
		}
		if local.Repository != nil {
			source.Layout = repository.LayoutRoot
			source.Repositories, err = repositorywire.LocalSnapshot(source.Manager, id, *local.Repository)
		} else {
			if t.cwdStatus != nil && t.cwdStatus.Path != "" || t.Workspace.Branch != "" {
				return false, taskworkspace.Failure("source_unavailable", "source", "", "", t.Cwd)
			}
			if !support.EmptyRepositories {
				return false, nil
			}
			source.Repositories, err = repository.Snapshot(source.Manager, id, set.Revision, nil, nil)
		}
	} else {
		if len(selected) == 1 {
			source.Layout = repository.LayoutRoot
		}
		source.Repositories, err = repository.Snapshot(source.Manager, id, set.Revision, set.Repositories, selected)
	}
	if err != nil {
		return false, err
	}
	var preview workspacenaming.Preview
	if err := s.call(ctx, t.Machine, "workspace.naming.resolve", source, &preview); err != nil {
		return false, taskworkspace.Failure("node_unavailable", "plan", "", "", "")
	}
	if preview.Plan == nil {
		var codes []string
		for _, diagnostic := range preview.Diagnostics {
			if diagnostic.Severity == "error" {
				codes = append(codes, diagnostic.Code)
			}
		}
		return false, fmt.Errorf("workspace naming failed (%s): %w", strings.Join(codes, ", "), taskworkspace.Failure("naming_incomplete", "plan", "", "", ""))
	}
	if err := validateWorkspaceNamingPlan(*preview.Plan, source); err != nil {
		return false, err
	}
	resolved.Path = preview.Plan.RootPath
	for _, repo := range preview.Plan.Repositories {
		resolved.Repositories = append(resolved.Repositories, BackendLaunchRepository{RepositoryPlan: repo})
	}
	if len(preview.Plan.Repositories) == 1 {
		resolved.Branch = preview.Plan.Repositories[0].TaskBranch
	}
	t.dispatchedWorkspace = &dispatchedWorkspacePlan{plan: preview.Plan, revision: rulesRevision}
	return true, nil
}

func (t *PlanTask) dispatchedWorkspaceOperationID() string {
	return "op_" + taskworkspace.Hash([]string{t.TodoID, t.planID, strconv.Itoa(t.planIndex), "new"})[:24]
}

// A restart can land between saving the naming plan and updating the task.
// Read the frozen operation before probing the node or choosing a legacy checkout.
func (s *Service) recoverDispatchedWorkspace(ctx context.Context, t *PlanTask) error {
	if t.Workspace == nil || t.Workspace.Mode != workspaceNew || t.TodoID == "" || t.planID == "" || t.inPane != "" {
		return nil
	}
	if w := t.WorkspaceResolved; w != nil && (w.Created || w.Reused || w.OperationID != "") {
		return nil
	}
	if _, ok := s.store().(workspaceNamingStore); !ok {
		return nil
	}
	err := s.resolveSavedBackendPlan(ctx, t, t.TodoID, t.dispatchedWorkspaceOperationID(), false)
	if problem, ok := errors.AsType[*taskworkspace.Problem](err); ok && problem.Code == "not_found" {
		return nil
	}
	return err
}

func (s *Service) admitDispatchedWorkspace(ctx context.Context, t *PlanTask) error {
	if t.planID == "" || t.TodoID == "" {
		return taskworkspace.Failure("invalid_input", "admission", "", "", "")
	}
	op := t.WorkspaceResolved.OperationID
	if op == "" {
		op = t.dispatchedWorkspaceOperationID()
		naming, ok := s.store().(workspaceNamingStore)
		if !ok {
			return taskworkspace.Failure("store_unavailable", "plan", op, "", "")
		}
		draft := t.dispatchedWorkspace
		if _, err := naming.SaveWorkspaceNamingPlan(ctx, t.TodoID, op, draft.revision, *draft.plan); err != nil {
			return err
		}
		// Name the operation before any node side effect. A retry, including one
		// during a node rollback, must keep this choice and its frozen inputs.
		t.WorkspaceResolved.OperationID, t.MachinePolicy = op, machineFixed
		if err := s.patchPlanTask(ctx, t.planID, t.planIndex, func(pt *PlanTask) {
			pt.Workspace, pt.WorkspaceInferred = t.Workspace, t.WorkspaceInferred
			pt.WorkspaceResolved, pt.MachinePolicy = t.WorkspaceResolved, machineFixed
		}); err != nil {
			return err
		}
	}
	if err := s.resolveSavedBackendPlan(ctx, t, t.TodoID, op, false); err != nil {
		return err
	}
	support, err := s.backendWorkspaceCapabilities(ctx, t.Machine, op, t.Cwd)
	if err != nil {
		return err
	}
	if support.Capability != taskworkspace.Capability || !support.LocalGit || !support.Start {
		return taskworkspace.Failure("unsupported", "capability", op, "", t.Cwd)
	}
	st, err := s.backendWorkspaceStore()
	if err != nil {
		return err
	}
	head, err := st.TaskWorkspace(ctx, t.TodoID, "")
	if err != nil {
		return err
	}
	if head != nil && head.State == "failed" && head.Error != nil && head.Error.Code == "workspace_drift" {
		if replaced, err := s.replaceDriftedDispatch(ctx, st, t, op, head); replaced || err != nil {
			return err
		}
	}
	err = s.admitPreparedTaskWorkspace(ctx, t, op)
	p, drifted := errors.AsType[*taskworkspace.Problem](err)
	if !drifted || p.Code != "workspace_drift" && (p.Code != "operation_conflict" || p.Stage != "manager") {
		return err
	}
	head, readErr := st.TaskWorkspace(ctx, t.TodoID, "")
	if readErr != nil {
		return readErr
	}
	if head != nil && (head.OperationID == p.OperationID || p.Code == "operation_conflict") {
		if replaced, replaceErr := s.replaceDriftedDispatch(ctx, st, t, op, head); replaced || replaceErr != nil {
			return replaceErr
		}
	}
	return err
}

// replaceDriftedDispatch leaves the old checkout and branch on disk. The Node
// must attest drift and release every old launch before the manager can replace
// its head. A timeout, lost reply or different Node is not proof of drift.
func (s *Service) replaceDriftedDispatch(ctx context.Context, st taskWorkspaceStore, t *PlanTask, op string, old *taskworkspace.Record) (bool, error) {
	if old.Plan.Source.MachineID != t.Machine || old.Plan.Source.Manager != s.managerIdentity() {
		return false, nil
	}
	view, err := s.InspectBackendWorkspace(ctx, t.TodoID, old.OperationID)
	if err != nil {
		return false, err
	}
	observed := view.Observation
	if observed == nil || observed.OperationID != old.OperationID || observed.NodeOperationID != old.OperationID ||
		observed.WorkspaceID != old.WorkspaceID || observed.InputDigest != old.InputDigest ||
		observed.Error == nil || observed.Error.Code != "workspace_drift" {
		return false, nil
	}
	if err := s.releaseStoppedBackendUses(ctx, old, ""); err != nil {
		return false, err
	}
	old, err = st.TaskWorkspace(ctx, t.TodoID, old.OperationID)
	if err != nil {
		return false, err
	}
	if old == nil {
		return false, taskworkspace.Failure("not_found", "prepare", op, "", "")
	}
	naming := s.store().(workspaceNamingStore)
	suffix := "-retry-" + taskworkspace.Hash([]string{t.dispatchedWorkspaceOperationID(), old.OperationID})[:10]
	replacement := "op_" + taskworkspace.Hash(suffix)[:24]
	plan, err := naming.WorkspaceNamingPlan(ctx, t.TodoID, replacement)
	if err != nil {
		return false, err
	}
	if plan == nil {
		requested, err := naming.WorkspaceNamingPlan(ctx, t.TodoID, op)
		if err != nil {
			return false, err
		}
		if requested == nil {
			return false, taskworkspace.Failure("not_found", "plan", op, "", "")
		}
		source := requested.Source
		source.Rules.TaskDirectoryTemplate += suffix
		if source.TaskBranch == "" && len(source.Repositories.Repositories) > 0 {
			source.Rules.BranchTemplate += suffix
		}
		var preview workspacenaming.Preview
		if err := s.call(ctx, t.Machine, "workspace.naming.resolve", source, &preview); err != nil {
			return false, err
		}
		if preview.Plan == nil {
			return false, taskworkspace.Failure("naming_incomplete", "plan", replacement, "", "")
		}
		if err := validateWorkspaceNamingPlan(*preview.Plan, source); err != nil {
			return false, err
		}
		rules, err := naming.TodoWorkspaceRules(ctx, t.TodoID)
		if err != nil {
			return false, err
		}
		plan, err = naming.SaveWorkspaceNamingPlan(ctx, t.TodoID, replacement, rules.Revision, *preview.Plan)
		if err != nil {
			return false, err
		}
	}
	applySavedBackendPlan(t, plan, replacement)
	if err := s.patchPlanTask(ctx, t.planID, t.planIndex, func(pt *PlanTask) {
		pt.Cwd, pt.MachinePolicy, pt.WorkspaceResolved = t.Cwd, machineFixed, t.WorkspaceResolved
	}); err != nil {
		return false, err
	}
	r, err := s.prepareBackendWorkspace(ctx, t.TodoID, replacement, old)
	if err != nil {
		return false, err
	}
	if r.Error != nil {
		return false, r.Error
	}
	return true, s.admitPreparedTaskWorkspace(ctx, t, replacement)
}
