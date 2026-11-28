package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func (t *PlanTask) containerWorkspace() bool {
	return t.Workspace != nil && t.Workspace.Mode == workspaceContainer && t.Workspace.Container != nil
}

func (t *PlanTask) backendWorkspace() bool {
	if t.Workspace == nil {
		return false
	}
	return t.Workspace.Mode == workspaceBackend || t.Workspace.Mode == workspaceContainer
}

func (t *PlanTask) backendHost() string {
	if t.Workspace == nil {
		return ""
	}
	if t.Workspace.Mode == workspaceBackend && t.Workspace.Backend != nil {
		return t.Workspace.Backend.Host
	}
	if t.Workspace.Mode == workspaceContainer && t.Workspace.Container != nil {
		return t.Workspace.Container.Host
	}
	return ""
}

func (t *PlanTask) backendOperationID() string {
	if t.Workspace == nil {
		return ""
	}
	if t.Workspace.Mode == workspaceBackend && t.Workspace.Backend != nil {
		return t.Workspace.Backend.OperationID
	}
	if t.Workspace.Mode == workspaceContainer && t.Workspace.Container != nil {
		if t.Workspace.Container.OperationID != "" {
			return t.Workspace.Container.OperationID
		}
		if t.WorkspaceResolved != nil && t.WorkspaceResolved.OperationID != "" {
			return t.WorkspaceResolved.OperationID
		}
		if t.TodoID != "" && t.planID != "" {
			return "op_" + taskworkspace.Hash([]string{t.TodoID, t.planID, strconv.Itoa(t.planIndex), "container"})[:24]
		}
	}
	return ""
}

func applySavedBackendPlan(t *PlanTask, plan *workspacenaming.Plan, op string) {
	t.MachinePolicy, t.Cwd = machineFixed, plan.RootPath
	resolved := &TaskWorkspaceResolved{Machine: t.Machine, Path: t.Cwd, OperationID: op}
	for _, repo := range plan.Repositories {
		resolved.Repositories = append(resolved.Repositories, BackendLaunchRepository{RepositoryPlan: repo})
	}
	if len(plan.Repositories) == 1 {
		resolved.Branch = plan.Repositories[0].TaskBranch
	}
	t.WorkspaceResolved = resolved
}

func (s *Service) resolveSavedBackendPlan(ctx context.Context, t *PlanTask, id, op string, requireContainer bool) error {
	if id == "" || !workspacenaming.ValidOperationID(op) {
		return taskworkspace.Failure("invalid_input", "plan", op, "", "")
	}
	st, ok := s.store().(workspaceNamingStore)
	if !ok {
		return taskworkspace.Failure("store_unavailable", "plan", op, "", "")
	}
	plan, err := st.WorkspaceNamingPlan(ctx, id, op)
	if err != nil {
		return err
	}
	if plan == nil {
		return taskworkspace.Failure("not_found", "plan", op, "", "")
	}
	if requireContainer && plan.Source.Layout != repository.LayoutContainer {
		return fmt.Errorf("workspace.container requires a container layout plan (got %q): %w", plan.Source.Layout, taskworkspace.Failure("invalid_input", "plan", op, "", ""))
	}
	if plan.Source.MachineID != t.Machine || plan.Source.Manager != s.managerIdentity() || plan.Source.TodoID != id {
		return taskworkspace.Failure("identity_mismatch", "plan", op, "", "")
	}
	applySavedBackendPlan(t, plan, op)
	return nil
}

func (s *Service) resolveBackendTask(ctx context.Context, t *PlanTask) error {
	if t.containerWorkspace() {
		return s.resolveContainerTask(ctx, t)
	}
	id := firstNonEmpty(t.TodoID, t.ReuseTodo)
	selection := t.Workspace.Backend
	if selection == nil || id == "" {
		return taskworkspace.Failure("invalid_input", "plan", "", "", "")
	}
	return s.resolveSavedBackendPlan(ctx, t, id, selection.OperationID, false)
}

func containerMergeInputs(existing []repository.TaskRepository, selected []string) ([]repository.TaskInput, bool) {
	present := make(map[string]bool, len(existing))
	inputs := make([]repository.TaskInput, 0, len(existing)+len(selected))
	for _, row := range existing {
		present[row.RepoID] = true
		inputs = append(inputs, repository.TaskInput{
			RepoID:         row.RepoID,
			DirectoryAlias: row.DirectoryAlias,
			RequestedRef:   row.RequestedRef,
		})
	}
	added := false
	for _, id := range selected {
		if present[id] {
			continue
		}
		present[id] = true
		added = true
		inputs = append(inputs, repository.TaskInput{RepoID: id})
	}
	return inputs, added
}

func (s *Service) resolveContainerTask(ctx context.Context, t *PlanTask) error {
	sel := t.Workspace.Container
	if sel == nil {
		return taskworkspace.Failure("invalid_input", "plan", "", "", "")
	}
	existingID := firstNonEmpty(t.TodoID, t.ReuseTodo)
	if len(sel.SelectedRepoIDs) == 0 {
		if existingID == "" {
			return fmt.Errorf("workspace.container with operation_id only requires todo_id: %w", taskworkspace.Failure("invalid_input", "plan", sel.OperationID, "", ""))
		}
		return s.resolveSavedBackendPlan(ctx, t, existingID, sel.OperationID, true)
	}
	namingStore, ok := s.store().(workspaceNamingStore)
	if !ok {
		return taskworkspace.Failure("store_unavailable", "plan", sel.OperationID, "", "")
	}
	if _, ok := s.store().(taskWorkspaceStore); !ok {
		return taskworkspace.Failure("store_unavailable", "plan", sel.OperationID, "", "")
	}
	if _, ok := s.store().(todoRepositoryStore); !ok {
		return taskworkspace.Failure("store_unavailable", "plan", sel.OperationID, "", "")
	}
	previewTodoID := firstNonEmpty(existingID, t.ReservedTodoID)
	if previewTodoID == "" {
		return taskworkspace.Failure("invalid_input", "plan", sel.OperationID, "", "")
	}
	if sel.OperationID != "" {
		prior, err := namingStore.WorkspaceNamingPlanForSave(ctx, previewTodoID, sel.OperationID)
		if err != nil {
			return err
		}
		if prior != nil {
			if existingID == "" {
				return taskworkspace.Failure("operation_conflict", "plan", sel.OperationID, "", "")
			}
			ids := make([]string, 0, len(prior.Source.Repositories.Repositories))
			for _, repo := range prior.Source.Repositories.Repositories {
				ids = append(ids, repo.RepoID)
			}
			if prior.Source.MachineID != t.Machine || prior.Source.Layout != repository.LayoutContainer ||
				prior.Source.Manager != s.managerIdentity() || prior.Source.TodoID != existingID ||
				taskworkspace.Hash(backendSortedIDs(ids)) != taskworkspace.Hash(backendSortedIDs(sel.SelectedRepoIDs)) {
				return taskworkspace.Failure("operation_conflict", "plan", sel.OperationID, "", "")
			}
			applySavedBackendPlan(t, prior, sel.OperationID)
			return nil
		}
	}
	if _, ok := s.device(ctx, t.Machine); !ok {
		return taskworkspace.Failure("node_unavailable", "plan", sel.OperationID, "", "")
	}
	settings, ok := s.savedSettings(ctx)
	if !ok {
		return taskworkspace.Failure("store_unavailable", "plan", sel.OperationID, "", "")
	}
	var (
		rows  []repository.TaskRepository
		rev   int64
		rules workspacenaming.Rules
		title string
	)
	if existingID != "" {
		if err := s.ensureTodosLoaded(ctx); err != nil {
			return err
		}
		todo, ok := s.todoNow(existingID)
		if !ok {
			return db.ErrTodoNotFound
		}
		title = firstNonEmpty(todo.Title, t.Title, "Task")
		rec, err := namingStore.TodoWorkspaceRules(ctx, existingID)
		if err != nil {
			return err
		}
		rules = rec.Rules
		set, err := s.readTodoRepositories(ctx, existingID)
		if err != nil {
			return err
		}
		rev = set.Revision
		inputs, added := containerMergeInputs(set.Repositories, sel.SelectedRepoIDs)
		if !added {
			rows = set.Repositories
		} else {
			doc, err := s.repositoryDocument(ctx)
			if err != nil {
				return err
			}
			rows, err = repository.Bind(set.Repositories, inputs, doc.taskCatalog())
			if err != nil {
				return err
			}
		}
	} else {
		title = firstNonEmpty(t.Title, "Task")
		doc, err := s.repositoryDocument(ctx)
		if err != nil {
			return err
		}
		inputs, _ := containerMergeInputs(nil, sel.SelectedRepoIDs)
		rows, err = repository.Bind(nil, inputs, doc.taskCatalog())
		if err != nil {
			return err
		}
	}
	snapshot, err := repository.Snapshot(s.managerIdentity(), previewTodoID, rev, rows, sel.SelectedRepoIDs)
	if err != nil {
		return err
	}
	source := workspacenaming.Source{
		Manager:      s.managerIdentity(),
		TodoID:       previewTodoID,
		Title:        title,
		MachineID:    t.Machine,
		Timezone:     settings.Timezone,
		PlannedAt:    time.Now().UTC(),
		Layout:       repository.LayoutContainer,
		Repositories: snapshot,
	}
	source.Rules, source.RuleSources = workspacenaming.Effective(settings.WorkspaceRules, rules)
	var preview workspacenaming.Preview
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := s.call(callCtx, t.Machine, "workspace.naming.resolve", source, &preview); err != nil {
		return fmt.Errorf("resolve container workspace on %s: %w", t.Machine, taskworkspace.Failure("node_unavailable", "plan", sel.OperationID, "", ""))
	}
	if preview.Plan == nil {
		repoID := ""
		codes := make([]string, 0, len(preview.Diagnostics))
		for _, d := range preview.Diagnostics {
			if d.Severity == "error" {
				if repoID == "" {
					repoID = d.RepoID
				}
				codes = append(codes, d.Code)
			}
		}
		problem := taskworkspace.Failure("naming_incomplete", "plan", sel.OperationID, repoID, "")
		if len(codes) > 0 {
			return fmt.Errorf("container workspace naming failed (%s): %w", strings.Join(codes, ", "), problem)
		}
		return problem
	}
	if err := validateWorkspaceNamingPlan(*preview.Plan, source); err != nil {
		return err
	}
	applySavedBackendPlan(t, preview.Plan, sel.OperationID)
	return nil
}

func (s *Service) createTaskTodoWithRepositories(ctx context.Context, id, title, notes, origin, planID, project, inboxItem string, repoIDs []string) (Todo, error) {
	return s.createTaskTodoWithRepositoriesAndAudience(ctx, id, title, notes, origin, planID, project, inboxItem, "", "", repoIDs)
}

func (s *Service) createTaskTodoWithRepositoriesAndAudience(ctx context.Context, id, title, notes, origin, planID, project, inboxItem, audience, directionID string, repoIDs []string) (Todo, error) {
	now := time.Now().UTC()
	row := db.TodoRow{
		ID: id, Title: title, Notes: notes, CreatedAt: now, UpdatedAt: now,
		Origin: origin, PlanID: planID, Quiet: s.quietTask(ctx, origin), Project: project, InboxItem: inboxItem, Audience: audience, DirectionID: strings.TrimSpace(directionID),
	}
	inputs, _ := containerMergeInputs(nil, repoIDs)
	var lastErr error
	for range 3 {
		doc, err := s.repositoryDocument(ctx)
		if err != nil {
			return Todo{}, err
		}
		rev := doc.Revision
		bindings := &repository.CreateBindings{CatalogRevision: &rev, Repositories: inputs}
		todo, err := s.insertTodo(ctx, row, bindings)
		if err == nil {
			return todo, nil
		}
		lastErr = err
		if !errors.Is(err, repository.Problem("catalog_conflict")) {
			return Todo{}, err
		}
	}
	return Todo{}, lastErr
}

func (s *Service) ensureContainerTodoRepositories(ctx context.Context, todoID string, selected []string) error {
	var lastErr error
	for range 3 {
		set, err := s.readTodoRepositories(ctx, todoID)
		if err != nil {
			return err
		}
		inputs, added := containerMergeInputs(set.Repositories, selected)
		if !added {
			return nil
		}
		doc, err := s.repositoryDocument(ctx)
		if err != nil {
			return err
		}
		rev, catRev := set.Revision, doc.Revision
		_, err = s.setTodoRepositories(ctx, todoID, repository.SetInput{
			ExpectedRevision:        &rev,
			ExpectedCatalogRevision: &catRev,
			Repositories:            inputs,
		})
		if err == nil {
			return nil
		}
		lastErr = err
		if !errors.Is(err, repository.Problem("revision_conflict")) && !errors.Is(err, repository.Problem("catalog_conflict")) {
			return err
		}
	}
	return lastErr
}

func (s *Service) probeBackendWorkspaceStart(ctx context.Context, machine, op, cwd string) error {
	capabilities, err := s.backendWorkspaceCapabilities(ctx, machine, op, cwd)
	if err != nil {
		return err
	}
	if !capabilities.Start {
		return taskworkspace.Failure("unsupported", "capability", op, "", cwd)
	}
	return nil
}

func (s *Service) admitBackendTask(ctx context.Context, t *PlanTask) error {
	if t.planID == "" || t.TodoID == "" {
		return taskworkspace.Failure("invalid_input", "admission", "", "", "")
	}
	op := t.backendOperationID()
	if !workspacenaming.ValidOperationID(op) {
		return taskworkspace.Failure("invalid_input", "admission", op, "", "")
	}
	if t.containerWorkspace() && len(t.Workspace.Container.SelectedRepoIDs) > 0 {
		if err := s.probeBackendWorkspaceStart(ctx, t.Machine, op, t.Cwd); err != nil {
			return err
		}
		if err := s.ensureContainerTodoRepositories(ctx, t.TodoID, t.Workspace.Container.SelectedRepoIDs); err != nil {
			return err
		}
		if err := s.ensureBackendWorkspacePlan(ctx, t.TodoID, namingRequest{
			MachineID:       t.Machine,
			Layout:          repository.LayoutContainer,
			SelectedRepoIDs: t.Workspace.Container.SelectedRepoIDs,
			OperationID:     op,
		}); err != nil {
			return err
		}
		if err := s.resolveSavedBackendPlan(ctx, t, t.TodoID, op, true); err != nil {
			return err
		}
	} else {
		if err := s.resolveBackendTask(ctx, t); err != nil {
			return err
		}
		if err := s.probeBackendWorkspaceStart(ctx, t.Machine, op, t.Cwd); err != nil {
			return err
		}
	}
	return s.admitPreparedTaskWorkspace(ctx, t, op)
}

func (s *Service) admitPreparedTaskWorkspace(ctx context.Context, t *PlanTask, op string) error {
	st, err := s.backendWorkspaceStore()
	if err != nil {
		return err
	}
	r, err := st.TaskWorkspace(ctx, t.TodoID, op)
	if err != nil {
		return err
	}
	if r == nil || r.State != "ready" {
		r, err = s.PrepareBackendWorkspace(ctx, t.TodoID, op)
		if err != nil {
			return err
		}
	}
	if r.Error != nil {
		return r.Error
	}
	if r.State != "ready" || r.Plan.RootPath != t.Cwd || r.Plan.Source.MachineID != t.Machine {
		return taskworkspace.Failure("not_ready", "admission", op, "", t.Cwd)
	}
	if t.containerWorkspace() && r.Plan.Source.Layout != repository.LayoutContainer {
		return taskworkspace.Failure("invalid_input", "admission", op, "", t.Cwd)
	}
	if err := taskworkspace.ValidateReady(*r); err != nil {
		return err
	}
	for i := range t.WorkspaceResolved.Repositories {
		row := &t.WorkspaceResolved.Repositories[i]
		for _, repo := range r.Repositories {
			if repo.RepoID == row.RepoID {
				row.AbsolutePath, row.ResolvedRef, row.ResolvedCommit = repo.Path, repo.ResolvedRef, repo.ResolvedCommit
				break
			}
		}
	}
	// Every retry of this plan slot presents the same use. A different plan must first retire the old session.
	use := "launch_" + taskworkspace.Hash([]string{t.planID, strconv.Itoa(t.planIndex), r.OperationID})[:32]
	if t.nodeDispatchedWorkspace() {
		if !s.preclaimWorkspace(ctx, t.Machine, t.Cwd, t.TodoID) {
			return taskworkspace.Failure("worktree_in_use", "admission", op, "", t.Cwd)
		}
		t.WorkspaceResolved.Created = true
		s.resolveTaskRepo(ctx, t)
	}
	if err = s.releaseStoppedBackendUses(ctx, r, use); err != nil {
		return err
	}
	req := r.Request()
	req.UseID = use
	t.WorkspaceResolved.BackendWorkspaceID, t.WorkspaceResolved.OperationID = r.WorkspaceID, r.OperationID
	if err = s.patchPlanTask(ctx, t.planID, t.planIndex, func(pt *PlanTask) {
		pt.Cwd, pt.MachinePolicy, pt.WorkspaceResolved = t.Cwd, machineFixed, t.WorkspaceResolved
		pt.Repo = t.Repo
	}); err != nil {
		return err
	}
	t.backendRequest = &req
	acquired, err := s.AcquireBackendWorkspace(ctx, t.TodoID, r.OperationID, use)
	if err != nil {
		var p *taskworkspace.Problem
		if acquired != nil && slices.Contains(acquired.Uses, use) && errors.As(err, &p) && p.Code != "operation_unknown" && p.Code != "operation_busy" {
			s.releaseUnusedBackendStart(ctx, t)
		}
	}
	return err
}

func (s *Service) startBackendTask(ctx context.Context, t *PlanTask, params map[string]any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, taskStartTimeout)
	defer cancel()
	req := t.backendRequest
	if req == nil {
		return errors.New("backend workspace admission required")
	}
	if _, err := s.CheckBackendWorkspaceUse(ctx, t.TodoID, req.OperationID, req.UseID); err != nil {
		return err
	}
	envelope := *req
	envelope.TaskDispatch = t.nodeDispatchedWorkspace()
	params["backend_workspace"] = envelope
	if host := t.backendHost(); host != "" {
		params["host"] = host
	}
	var result json.RawMessage
	err := s.call(ctx, t.Machine, DeviceSessionStart, params, &result)
	if err != nil {
		// A lost reply may hide a committed Node receipt. Reconcile the exact use.
		if startResultUnknown(err) {
			recoverCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentStartListTimeout)
			defer cancel()
			view, inspectErr := s.InspectBackendWorkspace(recoverCtx, t.TodoID, req.OperationID)
			if inspectErr == nil && view.Observation != nil && view.Observation.OperationID == req.OperationID && view.Observation.WorkspaceID == req.WorkspaceID && view.Observation.NodeOperationID == req.OperationID && view.Observation.Launch != nil && view.Observation.Launch.UseID == req.UseID && view.Observation.Launch.SessionID != "" {
				session, ok := s.adoptStartedSession(recoverCtx, Device{ID: t.Machine}, "", t.Cwd, view.Observation.Launch.SessionID, nil)
				if ok && session.Cwd == t.Cwd && !session.Archived && !session.Starting && session.StartError == "" && session.Status != proto.StatusOffline && session.Status != proto.StatusInterrupted {
					result, _ = json.Marshal(map[string]any{"session": session})
					return json.Unmarshal(result, out)
				}
			}
			return taskworkspace.Failure("operation_unknown", "start", req.OperationID, "", t.Cwd)
		}
		return err
	}
	var answer struct {
		Error *taskworkspace.Problem `json:"error"`
	}
	if err = json.Unmarshal(result, &answer); err != nil {
		return err
	}
	if answer.Error != nil {
		return answer.Error
	}
	return json.Unmarshal(result, out)
}

// nodeMethod is the method a device call goes out as: a session.start that carries an admitted backend workspace
// (startBackendTask) goes to the node's backend start, after call has handled it as the session.start it is. Every
// other call keeps its method. A classifier only, like tracksNodeWrite: call stays the transport its callers start
// through, and they are the start paths (start_paths_test.go).
func nodeMethod(method string, params any) string {
	fields, ok := params.(map[string]any)
	if !ok || method != DeviceSessionStart {
		return method
	}
	if _, managed := fields["backend_workspace"].(taskworkspace.Request); managed {
		return devicewire.MethodStartBackendSession
	}
	return method
}

func (s *Service) releaseUnusedBackendStart(ctx context.Context, t *PlanTask) {
	req := t.backendRequest
	if req == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	// Node holds its launch lock through dispatch. A live or ambiguous receipt refuses release.
	_, err := s.ReleaseBackendWorkspace(ctx, t.TodoID, req.OperationID, req.UseID)
	if err != nil {
		s.logf("task %s: workspace use retained: %v", t.TodoID, err)
	}
}

func (s *Service) releaseStoppedBackendUses(ctx context.Context, r *taskworkspace.Record, keep string) error {
	for _, use := range r.Uses {
		if use == keep || !strings.HasPrefix(use, "launch_") {
			continue
		}
		// Only release a positively identified stopped session. A preparing start can still dispatch.
		view, err := s.InspectBackendWorkspace(ctx, r.Plan.Source.TodoID, r.OperationID)
		if err != nil {
			return err
		}
		obs := view.Observation
		// A checkout's Git drift says nothing about its launch. Release still
		// requires the Node to prove that the exact launch is stopped.
		if obs == nil || obs.Error != nil && obs.Error.Code != "workspace_drift" || obs.OperationID != r.OperationID ||
			obs.NodeOperationID != r.OperationID || obs.WorkspaceID != r.WorkspaceID || obs.InputDigest != r.InputDigest {
			return taskworkspace.Failure("operation_unknown", "release", r.OperationID, "", r.Plan.RootPath)
		}
		// A confirmed cancellation also rules out a late start that had not dispatched.
		unstarted := r.State == "cancelled" && obs.State == "cancelled" && obs.Launch == nil
		if !unstarted && (obs.Launch == nil || obs.Launch.UseID != use || obs.Launch.SessionID == "") {
			return taskworkspace.Failure("worktree_in_use", "release", r.OperationID, "", r.Plan.RootPath)
		}
		if _, err = s.ReleaseBackendWorkspace(ctx, r.Plan.Source.TodoID, r.OperationID, use); err != nil {
			return err
		}
	}
	return nil
}
