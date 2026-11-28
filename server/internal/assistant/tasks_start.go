package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/taskworkspace"
)

// ---- starting and dismissing a plan ----

var errPlanNotOpen = errors.New("this plan was already started or dismissed")

// planInvalid is a start the user's edits make impossible as they are (a model that is no model id): 400.
type planInvalid struct{ error }

// startPlan opens a todo for each task of the open plan id (edits, when given, replace the tasks' fields the user can
// change) and starts their agents in the background; remember lets this kind start without asking next time.
// On the owner's own Start (ownerStart) that also lets the scheduled task whose round planned it start these kinds by
// itself from now on (scheduleGranting, #8045), each task's kind is added to that schedule's grant; never in the
// health space.
func (s *Service) startPlan(ctx context.Context, id string, edits []PlanTask, remember bool) (*TaskPlan, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return nil, err
	}
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.State != planOpen {
		return nil, errPlanNotOpen
	}
	supervisors := make([]string, len(p.Tasks))
	for i := range p.Tasks {
		if i < len(edits) {
			applyPlanEdit(&p.Tasks[i], edits[i])
		}
		task := &p.Tasks[i]
		task.CloseTabOnAccept = ownerStarts(ctx) && i < len(edits) && edits[i].CloseTabOnAccept
		task.TerminalTarget, task.TerminalCleanup = taskTerminalTarget{}, ""
		task.TerminalAuthorized = false
		if task.CloseTabOnAccept && (task.ReuseTodo != "" || task.Continues != "" || task.Parent != "") {
			return nil, planInvalid{errors.New("close_tab_on_accept requires a new to-do from this plan")}
		}
		if task.Continues != "" && task.HandoffOperationID != "" {
			if strings.TrimSpace(task.Permission) == "plan" {
				task.Readonly = true
			}
			if strings.TrimSpace(task.Permission) != "" || task.Readonly {
				task.preservePermission = true
			}
		}
		p.Tasks[i].normalizeReadonlyPermission(readonlyModeOf(s.settings(ctx)))
		p.Tasks[i].Machine = strings.TrimSpace(p.Tasks[i].Machine)
		if err := s.normalizePlacementConstraints(ctx, &p.Tasks[i]); err != nil {
			return nil, planInvalid{err}
		}
		if err := p.Tasks[i].normalizeMachinePolicy(); err != nil {
			return nil, planInvalid{err}
		}
		if p.Tasks[i].Machine == "" && (p.Tasks[i].backendWorkspace() || p.Tasks[i].containerWorkspace()) {
			return nil, planInvalid{errors.New("workspace.backend and workspace.container require an explicit machine")}
		}
		if err := checkModelID(p.Tasks[i].Model); err != nil { // typed on the card (#1644): refused before anything starts
			return nil, planInvalid{err}
		}
		if err := p.Tasks[i].ModelAliases.Validate(); err != nil {
			return nil, planInvalid{err}
		}
		params := map[string]any{"speed": p.Tasks[i].Speed}
		if _, err := validateStartSpeed(params); err != nil {
			return nil, planInvalid{err}
		}
		preview := p.Tasks[i]
		checkSpeed := false
		if err := s.applyTaskTier(ctx, &preview); err == nil {
			checkSpeed = proto.FasterSpeed(preview.Speed)
		} else {
			// An unknown tier has always been recorded as the task's start failure below, not an HTTP refusal here.
			checkSpeed = false
		}
		if checkSpeed && p.Tasks[i].Machine != "" {
			if _, err := s.fitPlanTaskSpeed(ctx, &p.Tasks[i]); err != nil {
				return nil, planInvalid{err}
			}
		}
		p.Tasks[i].Permission = strings.TrimSpace(p.Tasks[i].Permission)
		if p.Tasks[i].Machine != "" {
			if m, ok := s.machineByName(ctx, p.Tasks[i].Machine); ok && p.Tasks[i].Role == roleLeader && s.isWorkMachine(m.ID) {
				return nil, planInvalid{fmt.Errorf("task %d: role \"leader\" cannot run on %s: a work-trust computer gives its agents only "+
					"the reporting tools, so it could not supervise workers. Put the task leader on a computer you own", i+1, m.Name)}
			}
			if !s.placeTask(ctx, &p.Tasks[i]) {
				return nil, i18n.Err("assistant-tasks.task_no_machine", i18n.Args{"task": p.Tasks[i].Title, "machine": strconv.Quote(p.Tasks[i].Machine)})
			}
		}
		// Recheck waiting cards, including ones saved before continuations always reused their original to-do.
		if p.Tasks[i].Parent == "" {
			if err := s.takeOver(ctx, p.Conversation, &p.Tasks[i]); err != nil {
				return nil, planInvalid{fmt.Errorf("task %d: %w", i+1, err)}
			}
		}
		if task.Continues != "" {
			if prevTodo, ok := s.todoNow(task.Continues); ok {
				resolvedLauncher := cmp.Or(task.Launcher, preview.Launcher)
				providerSwitch := s.isProviderSwitchContinuation(ctx, prevTodo, task.Machine, resolvedLauncher) || task.HandoffOperationID != ""
				if providerSwitch && task.Permission == "plan" {
					task.Readonly = true
				}
				if err := s.checkContinuationBlockers(ctx, prevTodo, task.Permission, task.Readonly, providerSwitch); err != nil {
					return nil, planInvalid{fmt.Errorf("task %d: %w", i+1, err)}
				}
				if providerSwitch && task.HandoffOperationID == "" {
					task.HandoffOperationID = "op_" + randomHex(12)
				}
			}
		}
		task.RequestGate = s.planRequestGate(*task)
		if task.RequestGate != "" {
			if !ownerStarts(ctx) {
				return nil, planInvalid{i18n.Err("assistant-tasks.request_owner_start")}
			}
			if task.Continues != "" {
				return nil, planInvalid{i18n.Err("assistant-tasks.request_new_plan")}
			}
			if task.ReuseTodo != "" && !task.pieceEdit().empty() {
				return nil, planInvalid{i18n.Err("assistant-tasks.request_edit_piece")}
			}
			if err := s.planPieces(task); err != nil {
				return nil, planInvalid{err}
			}
		}
		if task.scratchWorkspace() && task.Cwd != "" && (task.WorkspaceResolved == nil || (!task.WorkspaceResolved.Created && !task.WorkspaceResolved.Reused)) {
			if identity.IsAssistantDir(task.Cwd) {
				return nil, planInvalid{i18n.Err("rpc-errors.agent_assistant_workspace")}
			}
			if task.Machine != "" {
				if m, ok := s.machineByName(ctx, task.Machine); ok && assistantWorkspace(m, proto.ExpandHome(task.Cwd, m.Home)) {
					return nil, planInvalid{i18n.Err("rpc-errors.agent_assistant_workspace")}
				}
			}
			return nil, planInvalid{fmt.Errorf("workspace %q cannot be combined with cwd; omit cwd to use a managed scratch directory", workspaceScratch)}
		}
		if strings.TrimSpace(task.Cwd) == "" && !task.backendWorkspace() && !s.canInferContainerWorkspace(ctx, *task) && !task.scratchWorkspace() {
			return nil, planInvalid{errors.New("title, prompt and cwd are required")}
		}
		if p.Tasks[i].Machine != "" {
			if err := s.resolveTaskWorkspace(ctx, &p.Tasks[i]); err != nil {
				return nil, planInvalid{err}
			}
			s.resolveTaskRepo(ctx, &p.Tasks[i]) // where it runs after the card's edits (its workspace, not the plan's cwd)
		} else if p.Tasks[i].ReservedTodoID == "" {
			p.Tasks[i].ReservedTodoID = firstNonEmpty(p.Tasks[i].ReuseTodo, p.Tasks[i].TodoID, "todo_"+randomHex(8))
		}
		if task.Continues != "" && task.Brief == nil {
			// Cards saved before briefs must bind the current scope before they can start.
			if err := s.planBriefs(ctx, "", p.Conversation, p.Tasks[i:i+1]); err != nil {
				return nil, planInvalid{err}
			}
		}
		if err := s.checkContinuationScope(task); err != nil {
			return nil, planInvalid{err}
		}
		if supervisors[i], err = s.planTaskSupervisor(ctx, p.Conversation, *task); err != nil {
			return nil, err
		}
	}
	// Waiting cards may have changed, or gained a conflicting association, since planning.
	if err := s.checkPlanRepositoryDuplicates(ctx, p.Tasks); err != nil {
		return nil, planInvalid{err}
	}
	p.State = planStarted
	for i := range p.Tasks {
		p.Tasks[i].OwnerStarted = ownerStarts(ctx)
		// Remembered by startTaskAgent once admission fixed the folder that runs: its repository is the kind, never
		// the one the plan was made from when the workspace is in another (#8259).
		p.Tasks[i].rememberKind = remember
		if scheduleGrants(ctx) && ownerStarts(ctx) && p.Tasks[i].Schedule != "" && !p.Tasks[i].Readonly && !s.selfPolicy(ctx, p.Conversation).ConfirmOutbound {
			p.Tasks[i].grantSchedule = p.Tasks[i].Schedule // added once admission fixed the folder (startTaskAgent)
		}
	}
	ok, err := s.movePlan(ctx, p, planOpen)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errPlanNotOpen
	}
	planOwners := map[string]Todo{}
	planQueuedKeys := map[string]bool{}
	pendingPlacementBatchSize := 0
	for i := range p.Tasks {
		if p.Tasks[i].Machine == "" && p.Tasks[i].RequestGate == "" {
			pendingPlacementBatchSize++
		}
	}
	pendingPlacementBatchIdx := 0
	for i := range p.Tasks {
		t := &p.Tasks[i]
		s.refreshSoftInbox(ctx, t)
		if t.InboxNotice != "" {
			if _, err := s.appendFeedOnce(ctx, p.ID+fmt.Sprintf("-inbox-%d", i), p.Conversation, feedNotice, p.ID, noticeBody{Text: i18n.T(s.lang(ctx), "assistant-tasks.inbox_link_removed")}); err != nil {
				s.logf("Inbox link notice: %v", err)
			}
		}
		if err := s.checkPlannedInbox(ctx, p.Conversation, *t); err != nil {
			t.Error = err.Error()
			continue
		}
		var todo Todo
		if t.ReuseTodo != "" {
			var pred todoPredecessorInfo
			var hasPred bool
			if curTodo, ok := s.todoNow(t.ReuseTodo); ok {
				pred, hasPred = s.todoPredecessor(ctx, curTodo)
			}
			// The todo is already there with its own title, notes and task card in the conversation's feed (a Lark topic
			// opened it): the task runs on it, and nothing is made or shown twice.
			todo, err = s.takeTodoForTask(ctx, p.ID, p.Conversation, *t)
			if err != nil {
				t.Error = err.Error()
				continue
			}
			s.applyTodosV2DispatchDefaults(ctx, p.Conversation, supervisors[i], t, &todo)
			if t.Continues != "" && (t.HandoffOperationID != "" || s.isProviderSwitchContinuation(ctx, todo, t.Machine, t.Launcher)) {
				if err := s.prepareContinuationHandoff(ctx, t, todo, pred, hasPred); err != nil {
					t.Error = err.Error()
					s.releaseTakenTodo(t.ReuseTodo)
					continue
				}
				if t.HandoffID != "" && (t.Permission != "" || t.Readonly) {
					t.preservePermission = true
				}
			}
			if t.Project != "" && todo.Project != t.Project {
				if moved, err := s.editTodo(ctx, todo.ID, db.TodoEdit{Project: &t.Project}); err != nil {
					s.logf("task %s: project: %v", todo.ID, err)
				} else {
					todo = moved
				}
			}
			if t.Audience != "" && todo.Audience != t.Audience {
				if updated, err := s.editTodo(ctx, todo.ID, db.TodoEdit{Audience: &t.Audience}); err != nil {
					s.logf("task %s: audience: %v", todo.ID, err)
				} else {
					todo = updated
				}
			}
			if t.DirectionID != "" && todo.DirectionID != t.DirectionID {
				if updated, err := s.editTodo(ctx, todo.ID, db.TodoEdit{DirectionID: &t.DirectionID}); err != nil {
					s.logf("task %s: direction_id: %v", todo.ID, err)
				} else {
					todo = updated
				}
			}
		} else {
			s.applyTodosV2DispatchDefaults(ctx, p.Conversation, supervisors[i], t, nil)
			title, retitle := s.taskTitle(ctx, t.Title, t.Prompt) // the assistant's own title stays when it is a real one
			if t.InboxItem != "" {
				s.tasks.mu.Lock()
				s.tasks.reusing[t.ReservedTodoID] = true
				s.tasks.mu.Unlock()
			}
			if t.containerWorkspace() && len(t.Workspace.Container.SelectedRepoIDs) > 0 {
				todo, err = s.createTaskTodoWithRepositoriesAndAudience(ctx, t.ReservedTodoID, title, t.Prompt, p.Conversation, p.ID, t.Project, t.InboxItem, t.Audience, t.DirectionID, t.Workspace.Container.SelectedRepoIDs)
			} else {
				todo, err = s.createTaskTodoAs(ctx, t.ReservedTodoID, title, t.Prompt, p.Conversation, p.ID, t.Project, t.InboxItem, t.Audience, t.DirectionID)
			}
			if err != nil {
				if t.InboxItem != "" {
					s.releaseTakenTodo(t.ReservedTodoID)
				}
				t.Error = err.Error()
				continue
			}
			if retitle {
				s.nameTodoLater(ctx, todo.ID, title, t.Prompt)
			}
		}
		if keep, err := s.linkPlannedInbox(ctx, p.Conversation, *t, todo); err != nil {
			t.Error = err.Error()
			if keep {
				t.TodoID = todo.ID
			}
			if t.ReuseTodo != "" {
				s.releaseTakenTodo(t.ReuseTodo)
			} else {
				if !keep {
					if err := s.deleteTodo(ctx, todo.ID); err != nil {
						s.logf("task %s: undo Inbox admission: %v", todo.ID, err)
					}
				}
				s.releaseTakenTodo(todo.ID)
			}
			continue
		}
		todo, err = s.startPieces(ctx, *t, todo)
		if err != nil {
			t.Error = err.Error()
			s.releaseTakenTodo(t.ReuseTodo)
			continue
		}
		t.TodoID = todo.ID
		s.saveApprovedPRBlockers(ctx, todo.ID, t.BlockedByPR, true)
		if s.settings(ctx).todosV2On() {
			afterIDs := cleanIDs(slices.Concat(t.After, todo.After))
			if len(afterIDs) > 0 {
				s.syncAfterBlockers(ctx, todo.ID, afterIDs, t.UnlockStage, t.UnlockContracts)
			}
		}
		if t.RequestGate != "" {
			// Persist the approved edges, including edges inherited from a reused to-do.
			t.After = cleanIDs(slices.Concat(t.After, todo.After))
			t.Queue = s.prerequisiteQueue(ctx, *t, s.requestBlockers(ctx, *t))
		} else if t.Machine == "" {
			q := s.pendingPlacementQueueEntry(ctx, p.ID, pendingPlacementBatchSize, pendingPlacementBatchIdx, s.placementNow())
			pendingPlacementBatchIdx++
			t.Queue = &q
		}
		if t.Queue == nil && t.Workspace != nil && t.Workspace.Mode == workspaceExisting && (!t.WorkspaceInferred || t.Workspace.Queue) && t.WorkspaceResolved != nil {
			effMachine := s.effectiveBoardMachine(t.Machine)
			wsPath := t.WorkspaceResolved.Path
			wsKey := effMachine + "\x00" + s.taskDirKey(ctx, t.Machine)(wsPath)
			owner := s.taskWorkspaceOwner(ctx, t.Machine, wsPath, t.TodoID, false)
			if owner == nil {
				if prev, ok := planOwners[wsKey]; ok {
					cp := prev
					owner = &cp
				}
			}
			if owner != nil && s.shouldQueueExistingWorkspace(ctx, *t, owner) {
				q := s.workspaceQueueEntry(ctx, effMachine, wsPath, owner.ID, owner.Title, false, nil)
				t.Queue = &q
				planQueuedKeys[wsKey] = true
			} else if owner == nil {
				planOwners[wsKey] = Todo{ID: todo.ID, Title: todo.Title}
			}
		}
		if t.Queue == nil {
			readiness, err := s.taskTodoReadiness(ctx, todo, *t)
			if err != nil {
				s.logf("task %s: approved start: read prerequisites: %v", todo.ID, err)
			}
			if err != nil || !readiness.ready() {
				t.Queue = s.blockerWaitQueue(ctx, readiness.refs())
			}
		}
		if t.Queue != nil {
			t.Placement = queuedPlacement(*t, t.Queue)
		}
		s.briefStarted(ctx, p, t, todo) // what was asked, kept from now on (task_brief_plan.go)
		// Its stage kind from that brief; how it is handed out is kept for every later look (task_stage_kind.go).
		s.restage(ctx, todo.ID, func(st *todoStage) { st.Readonly = t.Readonly })
		// The planning lead owns its task from the first dispatch; main owns its own plans. A task leader's worker
		// keeps its leader Agent through supervisors[i].
		if sup := supervisors[i]; sup != "" {
			if t.Parent == "" && t.ReuseTodo == "" && sup != p.Conversation {
				s.taskCard(ctx, sup, todo.ID, "")
			}
			if err := s.supervisePlanTask(ctx, todo.ID, sup, t); err != nil {
				s.failPlanSupervision(ctx, todo, t, err)
				continue
			}
		}
		if t.Parent != "" { // a task leader's worker: supervised by its leader's agent, with no conversation to show a card in
			continue
		}
		if t.ReuseTodo != "" {
			continue
		}
		if _, err := s.appendFeed(ctx, p.Conversation, feedTask, todo.ID, nil); err != nil {
			s.logf("task %s: feed: %v", todo.ID, err)
		}
	}
	if saved, err := s.movePlan(ctx, p, planStarted); err != nil || !saved { // keep the todo ids
		if err == nil {
			err = errPlanNotOpen
		}
		s.logf("plan %s: %v", p.ID, err)
		if slices.ContainsFunc(p.Tasks, func(task PlanTask) bool { return task.RequestGate != "" }) {
			for _, task := range p.Tasks {
				s.releaseTakenTodo(firstNonEmpty(task.ReuseTodo, task.TodoID))
			}
			return nil, err
		}
	} else {
		s.rememberPlannedWriteScopes(ctx, p)
	}
	s.event(ctx, map[string]any{"type": "task_plan", "plan": s.planWithSpace(ctx, p)})
	// Pre-claim workspaces that have a later queued sibling in this same plan so deterministic FIFO order holds
	// before goroutines reach admitTaskWorkspace.
	for i := range p.Tasks {
		task := p.Tasks[i]
		if task.TodoID == "" || task.Error != "" || task.Queue != nil || task.Workspace == nil ||
			task.Workspace.Mode != workspaceExisting || (task.WorkspaceInferred && !task.Workspace.Queue) || task.WorkspaceResolved == nil {
			continue
		}
		effMachine := s.effectiveBoardMachine(task.Machine)
		wsKey := effMachine + "\x00" + s.taskDirKey(ctx, task.Machine)(task.WorkspaceResolved.Path)
		if planQueuedKeys[wsKey] {
			s.preclaimWorkspace(ctx, effMachine, task.WorkspaceResolved.Path, task.TodoID)
		}
	}
	var queuedRecheck []workspaceTarget
	for i := range p.Tasks {
		if task := p.Tasks[i]; task.TodoID != "" && task.Error == "" {
			if task.Queue != nil && task.Placement != nil {
				s.savePlacement(ctx, task.Placement)
			}
			if task.Queue != nil && task.Queue.Key == pendingPlacementQueueKey {
				held := task.ReuseTodo
				if held == "" && task.InboxItem != "" {
					held = task.TodoID
				}
				s.releaseTakenTodo(held)
				batchSize := pendingPlacementBatchSize
				if task.Queue.Placement != nil && task.Queue.Placement.BatchSize > 0 {
					batchSize = task.Queue.Placement.BatchSize
				}
				s.onPendingPlacementEnqueued(ctx, p.ID, task.TodoID, batchSize)
				continue
			}
			if task.Queue != nil && task.Queue.Key == blockerWaitQueueKey {
				held := task.ReuseTodo
				if held == "" && task.InboxItem != "" {
					held = task.TodoID
				}
				s.releaseTakenTodo(held)
				continue
			}
			if task.Queue != nil && task.Queue.Key == workspaceQueueKey && task.Queue.Workspace != nil {
				held := task.ReuseTodo
				if held == "" && task.InboxItem != "" {
					held = task.TodoID
				}
				s.releaseTakenTodo(held)
				queuedRecheck = append(queuedRecheck, workspaceTarget{machine: task.Queue.Workspace.Machine, path: task.Queue.Workspace.Path})
				continue
			}
			s.startPlanTaskAsync(p.ID, p.Conversation, i, task, false, func() {
				held := task.ReuseTodo
				if held == "" && task.InboxItem != "" {
					held = task.TodoID
				}
				s.releaseTakenTodo(held) // linked or failed: the todo is no longer in the gap
			})
		}
	}
	for _, target := range queuedRecheck {
		s.drainWorkspaceQueue(ctx, target.machine, target.path)
	}
	return p, nil
}

// applyTodosV2DispatchDefaults fills default direction_id and audience for a planned task when todos_v2 is enabled (#15654).
// It never overwrites an explicit caller value or an existing reused to-do's non-empty value, and is a complete no-op when
// todos_v2 is off.
func (s *Service) applyTodosV2DispatchDefaults(ctx context.Context, conversation, supervisor string, t *PlanTask, existing *Todo) {
	if !s.settings(ctx).todosV2On() {
		return
	}
	effSup := supervisor
	if effSup == "" {
		effSup = cmp.Or(t.Supervisor, conversation)
	}
	isLeaderSup := effSup != "" && s.isLeader(ctx, effSup)
	if t.DirectionID == "" && (existing == nil || existing.DirectionID == "") && isLeaderSup {
		t.DirectionID = effSup
	}
	if t.Audience == "" && (existing == nil || existing.Audience == "") {
		partOf := t.PartOf
		if partOf == "" && existing != nil {
			partOf = existing.PartOf
		}
		if isLeaderSup || !t.UserRequested || partOf != "" || t.Parent != "" {
			t.Audience = db.AudienceAutonomous
		}
	}
}

// failPlanSupervision closes a prepared card whose required ledger could not be stored. Keeping TodoID preserves the
// visible failed-start link; Error keeps the async dispatcher from starting an unsupervised Agent.
func (s *Service) failPlanSupervision(ctx context.Context, todo Todo, t *PlanTask, cause error) {
	reason := clip(oneLine(fmt.Sprintf("record supervision: %v", cause)), 200)
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
	defer cancel()
	if err := s.failTodo(fctx, todo.ID, reason); err != nil {
		s.logf("task %s: fail after supervision error: %v", todo.ID, err)
	}
	t.Error = reason
	// No async start body will run, so release every claim that body would have released.
	held := t.ReuseTodo
	if held == "" && t.InboxItem != "" {
		held = todo.ID
	}
	s.releaseTakenTodo(held)
	s.releaseTaskWorkspace(todo.ID)
	if t.admissionRelease != nil {
		t.admissionRelease()
		t.admissionRelease = nil
	}
}

// supervisePlanTask keeps a continuation's ledger in step with its brief even
// if the scope changed between accepting the card and preparing dispatch.
func (s *Service) supervisePlanTask(ctx context.Context, id, supervisor string, t *PlanTask) error {
	_, _, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
		if v.assigned() {
			supervisor = v.Supervisor // a transfer that won before this write stays authoritative
		} else if s.isLeader(ctx, supervisor) || s.liveSideSupervisor(ctx, supervisor) {
			v.SupervisorSource = "plan"
		}
		if t.MultiIssueReport && v.Investigation == nil {
			v.Investigation = &investigationFindings{}
		}
		if t.Continues == "" {
			v.Supervisor, v.DoneWhen, v.ExpectMinutes = supervisor, cleanDoneWhen(t.DoneWhen), t.ExpectMinutes
			return true
		}
		brief, ok := s.briefOfTodo(t.Continues)
		if !ok {
			return false
		}
		v.Supervisor, v.DoneWhen, v.ExpectMinutes = supervisor, cleanDoneWhen(brief.Criteria), brief.ExpectMinutes
		return true
	})
	if err != nil {
		s.logf("task %s: supervision: %v", id, err)
	}
	return err
}

// notReusable is the refusal of a todo_id: it says why, so the assistant can plan again without it.
func notReusable(id, why string) error {
	return fmt.Errorf("todo_id %s cannot be used: %s (leave todo_id out and the task gets a todo of its own)", id, why)
}

func (s *Service) taskTodoForConversation(conversation, id string) (Todo, error) {
	t, ok := s.todoNow(id)
	switch {
	case !ok:
		return Todo{}, notReusable(id, "there is no such todo")
	case conversation == "" || t.Origin != conversation && (t.Supervision == nil || t.Supervision.Supervisor != conversation):
		return Todo{}, notReusable(id, "it was not opened from this conversation, nor is it supervised by it")
	}
	return t, nil
}

// reusableTodo says whether a task of a plan from conversation may run on todo id (task_plan's todo_id): the todo is
// one that conversation opened or supervises (a handover after its agent stopped carries on in the same to-do, #6427),
// is not closed for good (a failed start is taken up again: takeTodoForTask reopens it, #10365), its start is not being
// remedied in a kept tab (task_retry's), has no live agent on it, is not starting again already (a Retry's, task_retry's
// start on its way, tasksState.retrying) and is not taken by a plan that started and has not linked its agent yet. An
// agent linked and never let go whose session stopped (its terminal kept, tasks_close.go), was archived or whose
// computer the hub no longer knows is no agent working on it: those links are answered as stopped, for the taker to
// let go (releaseStoppedAgents). A computer that cannot be reached is not an answer: its agent counts as working.
// taking: the plan hold and the start are what the caller claimed itself, so they are not looked at (takeTodoForTask
// holds them).
func (s *Service) reusableTodo(ctx context.Context, conversation, id string, taking bool) (stopped []TodoAgent, err error) {
	t, err := s.taskTodoForConversation(conversation, id)
	if err != nil && ownerStarts(ctx) && ctx.Value(ownerBackendTodoKey{}) == id {
		// An owner's manual to-do has no originating conversation. The owner
		// route binds this one exact to-do; all ordinary takeover checks follow.
		if manual, ok := s.todoNow(id); ok && manual.Origin == "" && supervisorOf(manual) == "" {
			t, err = manual, nil
		}
	}
	if err != nil {
		return nil, err
	}
	if t.closedForGood() {
		return nil, notReusable(id, "it is already done")
	}
	if s.requestStartPending(ctx, t) {
		return nil, notReusable(id, "an approved start for this request piece is already waiting or its launch is unconfirmed")
	}
	if s.workspaceQueuePending(ctx, t) {
		return nil, notReusable(id, "a start for this to-do is already queued for its workspace")
	}
	if s.pendingPlacementPending(ctx, t) {
		return nil, notReusable(id, "a start for this to-do is already queued for automatic machine placement")
	}
	if s.startRemedying(ctx, t) {
		return nil, notReusable(id, "its start is being remedied in the tab its computer keeps: use task_retry")
	}
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		if !s.todoAgentStopped(ctx, a) {
			return nil, notReusable(id, "an agent is already working on it")
		}
		stopped = append(stopped, a)
	}
	if !taking {
		s.tasks.mu.Lock()
		busy, starting := s.tasks.reusing[id], s.tasks.retrying[id] != nil
		s.tasks.mu.Unlock()
		if busy {
			return nil, notReusable(id, "another plan is starting an agent on it")
		}
		if starting {
			return nil, notReusable(id, errStartingAgain)
		}
	}
	return stopped, nil
}

// errStartingAgain is why a to-do whose agent is starting again (a Retry, task_retry, a new agent after stopped
// ones, another plan taking it over: tasksState.retrying) is not taken by a plan.
const errStartingAgain = "its agent is starting again already (a Retry, or a new agent on it): wait for how that start ends"

// startRemedying: t's start did not come up and is being remedied in the tab its computer keeps (start_remedy.go).
// Only a supervised task's start is ever remedied, so the plans are read for those alone; a plan that can't be read
// says nothing is being remedied. task_retry's mark on a start that had failed for good keeps no tab (no until): its
// start is on its way under the start claim while it runs, and once the to-do is no longer a failed start, a mark left
// behind (its manager stopped meanwhile) remedies nothing.
func (s *Service) startRemedying(ctx context.Context, t Todo) bool {
	if t.Supervision == nil {
		return false
	}
	p, i, err := s.currentPlanAssignment(ctx, t)
	if err != nil {
		return false
	}
	r := p.Tasks[i].Start
	return r != nil && r.State == remedying && (!r.Until.IsZero() || t.failedStart())
}

// todoAgentStopped: nothing runs agent a now, so a new agent may take over its to-do: its session is listed stopped
// (switchStopped) or archived, its computer is gone from the hub, or its computer authoritatively confirms via
// session.info that no such session exists there. An unlisted session whose existence cannot be ruled out (for
// example, an aged-out Antigravity session still on disk beyond the recent list cap, an away runtime, a known
// no_transcript session, or an unreachable computer) stays fail-closed as not stopped.
func (s *Service) todoAgentStopped(ctx context.Context, a TodoAgent) bool {
	_, stopped, listed, listErr := s.todoAgentStopPresenceRow(ctx, a)
	if stopped || listed || listErr != nil {
		return stopped
	}
	return s.todoAgentConfirmedMissing(ctx, a)
}

// todoAgentStopPresence is todoAgentStopped plus listed: listed says a session list answered and some row names
// a.SessionID as its own id or one of its aliases. Aliases matter for Antigravity: while an agy-<conversation> runs
// in a terminal pane, the node lists the pane row and puts agy-<conversation> in its aliases, so matching the row id
// alone would call a live Agent absent. A listed live session therefore stays fail-closed against a contradictory
// stop answer; an id no row or alias names can still be settled by the computer's authoritative "no such session"
// stop answer.
func (s *Service) todoAgentStopPresence(ctx context.Context, a TodoAgent) (stopped, listed bool) {
	_, stopped, listed, _ = s.todoAgentStopPresenceRow(ctx, a)
	return stopped, listed
}

// todoAgentStopPresenceRow checks a's computer session list once, matching by SessionID or Aliases so an Antigravity
// conversation running in a terminal pane under a pane row is still seen as live. Any live matching row wins, and among
// live matches a terminal-backed row takes precedence so continuation never routes a terminal pane to session.close.
func (s *Service) todoAgentStopPresenceRow(ctx context.Context, a TodoAgent) (row proto.Session, stopped, listed bool, listErr error) {
	list, err := s.sessions(ctx, a.MachineID)
	if errors.Is(err, ErrNoDevice) {
		return proto.Session{}, true, false, nil
	}
	if err != nil {
		return proto.Session{}, false, false, err
	}
	// Every row naming the id counts: a stopped alias row can sort before the live exact row (runtime
	// fork aliases), so one stopped match must not hide another live match. Any live match wins.
	live := false
	liveHasTerminal := false
	for _, c := range list {
		if !sessionNamesID(c, a.SessionID) {
			continue
		}
		cLive := !(c.Archived || switchStopped(c))
		cTerm := c.PaneID != "" || (c.Capabilities != nil && c.Capabilities.Terminal)
		if !listed {
			row = c
			listed = true
		}
		if cLive {
			if !live || (cTerm && !liveHasTerminal) {
				row = c
				liveHasTerminal = cTerm
			}
			live = true
		}
	}
	if !listed {
		return proto.Session{}, false, false, nil
	}
	return row, !live, true, nil
}

// sessionNamesID reports whether row is id or names id as another of its machine-known ids (proto.Session.Aliases).
func sessionNamesID(row proto.Session, id string) bool {
	if row.SessionID == id {
		return true
	}
	return slices.Contains(row.Aliases(), id)
}

// otherTodoOwnsNamedSession reports whether an ACTIVE row id/alias the machine lists for a.SessionID is
// already the live Agent of another open to-do. It closes the agy companion-uuid vs pane-row split that
// the exact-id activeBySession guard cannot see. A list error returns false and leaves the existing
// fail-closed stop path in charge.
func (s *Service) otherTodoOwnsNamedSession(ctx context.Context, a TodoAgent, todoID string) bool {
	list, err := s.sessions(ctx, a.MachineID)
	if err != nil {
		return false
	}
	ids := map[string]struct{}{a.SessionID: {}}
	for _, c := range list {
		if !sessionNamesID(c, a.SessionID) {
			continue
		}
		ids[c.SessionID] = struct{}{}
		for _, al := range c.Aliases() {
			ids[al] = struct{}{}
		}
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for id := range ids {
		if owner := s.todos.activeBySession[[2]string{a.MachineID, id}]; owner != "" && owner != todoID {
			return true
		}
	}
	return false
}

// todoAgentConfirmedMissing probes a's computer directly via session.info to confirm that no such session exists
// there. Only an authoritative missing-session refusal (not_found with confirmed_missing=true or reason=unknown_session)
// returns true; existing/aged-out sessions, no_transcript, away runtimes, older nodes, and transport errors stay false.
func (s *Service) todoAgentConfirmedMissing(ctx context.Context, a TodoAgent) bool {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := s.call(cctx, a.MachineID, "session.info", map[string]any{"session_id": a.SessionID}, nil)
	return sessionConfirmedMissing(err)
}

// sessionConfirmedMissing reports whether err is a node's authoritative confirmation that a session does not exist.
func sessionConfirmedMissing(err error) bool {
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != codeSessionNotFound || len(re.Data) == 0 {
		return false
	}
	var data struct {
		Reason           string `json:"reason"`
		ConfirmedMissing bool   `json:"confirmed_missing"`
	}
	if json.Unmarshal(re.Data, &data) != nil {
		return false
	}
	return data.ConfirmedMissing || data.Reason == proto.NotFoundUnknownSession
}

// agyCompanionIDRe is the Antigravity companion row id agy-<conversation-uuid> shape the node's
// agyConversationID recognizes. For this id the node's plain "invalid session id" stop answer is not
// proof the conversation is gone: a live pane normally carries it only as an alias, and when two panes
// report one conversation the node leaves that alias off both rows. Pane/worker-derived agy ids (the
// form a task links, e.g. the short agy-<8hex> worker id) are instead checked by the node's agyPaneRow,
// so the same text is an id-specific "no such live pane" answer for them.
var agyCompanionIDRe = lazyre.New(`^agy-[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// stopErrSaysSessionGone reports whether a session.stop error is the reached computer's authoritative answer that
// the target session does not exist there: the typed not_found code current nodes and runtimes return
// (sessionNotFound) for every id form, or the legacy plain "invalid session id %q" text older Persona nodes send
// for a non-standard row id they no longer know. The legacy branch is tightly bounded (review P2): a plain handler
// error crosses RPC with an EMPTY code (re.Code == "" is required), the text must name this exact session id, and
// the id must be an Antigravity pane/worker row (agy- prefix, but not the companion agy-<uuid> form the node cannot
// certify from that text alone). Other non-hex otherSession ids such as omp-w1-p2 stay fail-closed: their legacy
// text is not an id-specific gone answer this hub may forgive. Transport, timeout, offline and refusal errors are
// none of these, so a computer that cannot be reached is never read as a stopped agent.
func stopErrSaysSessionGone(err error, sessionID string) bool {
	if err == nil {
		return false
	}
	if sessionNotFound(err) {
		return true
	}
	var re *rpc.Error
	if !errors.As(err, &re) {
		return false
	}
	if re.Code != "" || re.Message != fmt.Sprintf("invalid session id %q", sessionID) {
		return false
	}
	return legacyAgyPaneID(sessionID)
}

// legacyAgyPaneID reports whether id is an Antigravity pane/worker-derived row id: agy-prefixed but not
// the companion agy-<conversation-uuid> shape. Only this form may use the node's legacy empty-code
// invalid-id text as an authoritative gone signal.
func legacyAgyPaneID(id string) bool {
	return strings.HasPrefix(id, "agy-") && !agyCompanionIDRe.MatchString(id)
}

// takeTodoForTask is the todo a started plan's task runs on (PlanTask.ReuseTodo), checked again now (it may have been
// closed or taken since the plan was made) and held until the task's agent is linked (releaseTakenTodo). The hold is
// also the to-do's one start on its way (claimTaskStart, which a Retry and task_retry take too), so a Retry of a failed
// start and a plan taking it over never both start an agent on it, whichever comes first (#10365). The stopped agents
// still linked to it are let go first: the new agent is the one working on it, in a new round of the to-do's
// supervision (startSupervisionRound).
func (s *Service) takeTodoForTask(ctx context.Context, planID, conversation string, task PlanTask) (Todo, error) {
	id := task.ReuseTodo
	s.tasks.mu.Lock()
	if s.tasks.reusing[id] {
		s.tasks.mu.Unlock()
		return Todo{}, notReusable(id, "another plan is starting an agent on it")
	}
	st, claimed := s.claimTaskStartLocked(id)
	if !claimed {
		s.tasks.mu.Unlock()
		return Todo{}, notReusable(id, errStartingAgain)
	}
	s.tasks.reusing[id], s.tasks.taking[id] = true, st
	s.tasks.mu.Unlock()
	var stopped []TodoAgent
	var err error
	if task.Continues == id {
		var todo Todo
		todo, err = s.taskTodoForConversation(conversation, id)
		if err == nil && (s.planRequestGate(task) != "" || s.requestStartPending(ctx, todo)) {
			err = i18n.Err("assistant-tasks.request_new_plan")
		}
		if err == nil && task.Brief != nil {
			err = s.checkContinuationScope(&task)
		}
		if err == nil {
			stopped, err = s.stopContinuedTaskAgents(ctx, todo)
		}
	} else {
		stopped, err = s.reusableTodo(ctx, conversation, id, true)
	}
	if err != nil {
		s.releaseTakenTodo(id)
		return Todo{}, err
	}
	// TodoID is the execution binding; ReuseTodo preserves each historical plan's intended target.
	// Retire old bindings while this takeover holds the to-do, before the new agent can ask for approval. Main-chat
	// handoffs move the to-do, not its plans, so the old current binding can belong to any earlier conversation.
	// Creation order is not start order, and a later retry can change an old plan's UpdatedAt.
	plans, err := s.planExecutionHistory(ctx)
	if err != nil {
		s.releaseTakenTodo(id)
		return Todo{}, err
	}
	for _, p := range plans {
		if p.ID == planID {
			continue
		}
		for i, task := range p.Tasks {
			if task.TodoID == id {
				if err := s.patchPlanTask(ctx, p.ID, i, func(t *PlanTask) { t.TodoID = "" }); err != nil {
					s.releaseTakenTodo(id)
					return Todo{}, err
				}
			}
		}
	}
	if err := s.releaseStoppedAgents(ctx, id, stopped); err != nil {
		s.releaseTakenTodo(id)
		return Todo{}, err
	}
	if task.Continues == id {
		if err := s.reopenTodo(ctx, id); err != nil {
			s.releaseTakenTodo(id)
			return Todo{}, err
		}
	} else if cur, ok := s.todoNow(id); ok && cur.failedStart() {
		// A failed start taken up through todo_id is open again, its error cleared, as a Retry reopens it: the new
		// agent works on it, and it is not counted closed meanwhile (#10365). Another assistant sharing the store
		// that reopened it first is starting it again itself: not a second agent.
		_, reopened, err := s.reopenFailedTodo(ctx, id)
		if err == nil && !reopened {
			err = notReusable(id, errStartingAgain)
		}
		if err != nil {
			s.releaseTakenTodo(id)
			return Todo{}, err
		}
	}
	// The new Agent's work is a new round of its supervision: an earlier round's acceptance is history now (#11553).
	s.startSupervisionRound(ctx, id)
	t, _ := s.todoNow(id)
	return formatTodo(t), nil
}

// runtimeCanClose reports whether c is a non-terminal runtime session that explicitly advertises session.close.
// Terminal-backed sessions (including pi/omp inside a terminal pane) and assistant conversations never match.
func runtimeCanClose(c proto.Session) bool {
	return c.SourceKind == proto.SourceRuntime &&
		!c.Assistant &&
		c.PaneID == "" &&
		c.Capabilities != nil &&
		!c.Capabilities.Terminal &&
		c.Capabilities.Close != nil && *c.Capabilities.Close
}

// runtimeCanInterruptOnly reports whether c is a non-terminal runtime session without session.close
// (such as Antigravity Desktop, DSH, Managed Agents, or opencode TUI) whose execution is controlled by session.interrupt.
func runtimeCanInterruptOnly(c proto.Session) bool {
	return c.SourceKind == proto.SourceRuntime &&
		!c.Assistant &&
		c.PaneID == "" &&
		c.Capabilities != nil &&
		!c.Capabilities.Terminal &&
		(c.Capabilities.Close == nil || !*c.Capabilities.Close) &&
		c.Capabilities.Interrupt
}

// runtimeSessionBusy reports whether a runtime session has active or queued work that must be interrupted before
// releasing its to-do link.
func runtimeSessionBusy(c proto.Session) bool {
	return c.Status == proto.StatusWorking ||
		c.Status == proto.StatusBlocked ||
		c.Starting ||
		c.State == proto.StateWorking ||
		c.State == proto.StateNeedsYou ||
		c.BackgroundWorking ||
		len(c.Background) > 0 ||
		len(c.Queued) > 0
}

// stopContinuedAgent stops one active predecessor Agent during a task_plan(continues=...) handoff (#16231).
// Terminal-backed Agents always use session.stop so their terminal pane stays open (#4754).
// Non-terminal runtime sessions use their declared lifecycle control: session.close when Capabilities.Close is true
// (e.g. native Antigravity companion, Paseo, Opencode companion, Grok, Orca, T3Code, Cindy, Pi Web), or
// session.interrupt when Close is unavailable and Interrupt is true while busy (e.g. Antigravity Desktop, DSH,
// Managed Agents, Opencode TUI). When an unlisted non-terminal session answers no_terminal to session.stop,
// session.close is attempted only if the live session list confirmed no terminal pane exists for it.
func (s *Service) stopContinuedAgent(ctx context.Context, a TodoAgent, sess proto.Session, listed bool, listErr error) error {
	targetID := a.SessionID
	if listed && sess.SessionID != "" && (sess.PaneID != "" || runtimeCanClose(sess) || runtimeCanInterruptOnly(sess)) {
		targetID = sess.SessionID
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	switch {
	case listErr == nil && runtimeCanClose(sess):
		return s.call(cctx, a.MachineID, "session.close", map[string]any{"session_id": targetID}, nil)
	case listed && runtimeCanInterruptOnly(sess):
		if !runtimeSessionBusy(sess) {
			return nil
		}
		return s.call(cctx, a.MachineID, "session.interrupt", map[string]any{"session_id": targetID}, nil)
	default:
		err := s.call(cctx, a.MachineID, deviceSessionStop, map[string]any{"session_id": targetID}, nil)
		canFallbackClose := listErr == nil &&
			!sess.Assistant &&
			sess.PaneID == "" &&
			(sess.Capabilities == nil || (!sess.Capabilities.Terminal && (sess.Capabilities.Close == nil || *sess.Capabilities.Close)))
		if canFallbackClose && isNoTerminalStopError(err) {
			return s.call(cctx, a.MachineID, "session.close", map[string]any{"session_id": targetID}, nil)
		}
		return err
	}
}

// stopContinuedTaskAgents is an explicit handoff, not the idle task-closure pass: stop the old process but keep its
// terminal and history. A refusal leaves the handoff failed on this to-do, with no replacement Agent.
func (s *Service) stopContinuedTaskAgents(ctx context.Context, todo Todo) ([]TodoAgent, error) {
	var stopped []TodoAgent
	for _, a := range todo.Agents {
		if !a.active() {
			continue
		}
		s.todos.mu.RLock()
		other := s.todos.activeBySession[[2]string{a.MachineID, a.SessionID}]
		s.todos.mu.RUnlock()
		if other != "" && other != todo.ID {
			// A historical link cannot authorize stopping an Agent that now belongs to another task.
			// Return only this to-do's stale link for release, not the other to-do's live link.
			// releaseStoppedAgents does not stop the process.
			stopped = append(stopped, a)
			continue
		}
		if agyCompanionIDRe.MatchString(a.SessionID) && s.otherTodoOwnsNamedSession(ctx, a, todo.ID) {
			// The exact-id guard misses the agy companion-uuid vs live pane-row alias split: the node
			// would resolve the uuid to that other to-do's pane and quit its Agent. Release only this
			// stale link without the stop, as in the exact-id cross-to-do case.
			stopped = append(stopped, a)
			continue
		}
		if _, ok := s.device(ctx, a.MachineID); !ok {
			return nil, fmt.Errorf("continues: cannot confirm agent %s/%s stopped: its computer is unavailable", a.MachineID, a.SessionID)
		}
		var sess proto.Session
		var hasSess bool
		if c, ok := s.decoratedSession(a.MachineID, a.SessionID); ok {
			sess, hasSess = c, true
		} else if c, ok := s.session(ctx, a.MachineID, a.SessionID); ok {
			sess, hasSess = c, true
		}
		liveRow, isStopped, listed, listErr := s.todoAgentStopPresenceRow(ctx, a)
		if !hasSess {
			if listed {
				sess, hasSess = liveRow, true
			} else {
				sess = proto.Session{MachineID: a.MachineID, SessionID: a.SessionID}
			}
		}
		if refused, why := sessionSafetyRefusal(sess); refused {
			return nil, handoffBlockerError{
				code: "safety_refusal",
				msg:  fmt.Sprintf("continues: agent %s/%s on to-do %s is blocked by a provider safety refusal (%s); safety refusals remain recorded blockers", a.MachineID, short(a.SessionID), todo.ID, why),
			}
		}
		if listed {
			if refused, why := sessionSafetyRefusal(liveRow); refused {
				return nil, handoffBlockerError{
					code: "safety_refusal",
					msg:  fmt.Sprintf("continues: agent %s/%s on to-do %s is blocked by a provider safety refusal (%s); safety refusals remain recorded blockers", a.MachineID, short(a.SessionID), todo.ID, why),
				}
			}
		}
		if s.sessionUnresolvedApproval(sess) || (listed && s.sessionUnresolvedApproval(liveRow)) {
			return nil, handoffBlockerError{
				code: "unresolved_approval",
				msg:  fmt.Sprintf("continues: agent %s/%s on to-do %s has an unresolved approval or question; resolve it first", a.MachineID, short(a.SessionID), todo.ID),
			}
		}
		// A list row that is live (listed and not stopped) contradicting the stop answer stays hard.
		// "listed but stopped" only reaches stop for the companion-uuid verification call and may be
		// reconciled by an authoritative not_found from the node.
		liveListed := listed && !isStopped
		confirmedMissing := false
		if !isStopped && !listed && listErr == nil && s.todoAgentConfirmedMissing(ctx, a) {
			s.logf("continues: agent %s/%s on to-do %s is confirmed missing on its computer; letting its stale link go", a.MachineID, a.SessionID, todo.ID)
			s.endSessionCards(a.MachineID, a.SessionID)
			isStopped = true
			confirmedMissing = true
		}
		// For the companion agy-<uuid> form a stopped exact row can coexist with two live panes whose
		// double attribution made the node omit the alias, so presence alone cannot certify it stopped:
		// always ask the node's raw-pane authority for that form even when the list says stopped, unless
		// session.info already certified confirmed_missing (which vetoes on raw pane attribution and presence lock).
		if (!isStopped || agyCompanionIDRe.MatchString(a.SessionID)) && !confirmedMissing {
			target := sess
			if listed {
				target = liveRow
			}
			err := s.stopContinuedAgent(ctx, a, target, listed, listErr)
			switch {
			case err == nil:
				s.endSessionCards(a.MachineID, a.SessionID)
			case liveListed || !stopErrSaysSessionGone(err, a.SessionID):
				// A session the list shows live, or any uncertain/refusal error, stays fail-closed: the handoff fails
				// and a retry can reconcile it. Only an absent row plus the computer's own "no such session" answer is
				// the idempotent already-gone case (an orphaned agy worker after its companion restarted).
				return nil, fmt.Errorf("continues: cannot stop agent %s/%s on to-do %s: %w", a.MachineID, a.SessionID, todo.ID, err)
			default:
				s.logf("continues: agent %s/%s on to-do %s is no longer on its computer; letting its stale link go: %v",
					a.MachineID, a.SessionID, todo.ID, err)
				s.endSessionCards(a.MachineID, a.SessionID)
			}
		}
		stopped = append(stopped, a)
	}
	return stopped, nil
}

func isNoTerminalStopError(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == "no_terminal"
}

// todoAgentReleaser is the store's letting go of one agent link (released_at only); a store without it keeps that in
// memory only.
type todoAgentReleaser interface {
	ReleaseTodoAgent(ctx context.Context, todoID, machineID, sessionID string, at time.Time) error
}

var _ todoAgentReleaser = (*db.DB)(nil)

// releaseStoppedAgents lets go of the stopped agents still linked to todo id (reusableTodo), so the agent taking it
// over is the one the to-do and its follow-up see. They stay in its history.
func (s *Service) releaseStoppedAgents(ctx context.Context, id string, stopped []TodoAgent) error {
	if len(stopped) == 0 {
		return nil
	}
	now := time.Now().UTC()
	s.todos.writing.Lock()
	if st, ok := s.todoDB().(todoAgentReleaser); ok {
		for _, a := range stopped {
			if err := st.ReleaseTodoAgent(ctx, id, a.MachineID, a.SessionID, now); err != nil {
				s.todos.writing.Unlock()
				return err
			}
		}
	}
	s.todos.mu.Lock()
	if t, ok := s.todos.todos[id]; ok {
		for _, a := range stopped {
			for i := range t.Agents {
				if t.Agents[i].MachineID == a.MachineID && t.Agents[i].SessionID == a.SessionID && t.Agents[i].ReleasedAt == nil {
					t.Agents[i].ReleasedAt = &now
				}
			}
			if key := [2]string{a.MachineID, a.SessionID}; s.todos.activeBySession[key] == id {
				delete(s.todos.activeBySession, key)
			}
		}
		t.UpdatedAt = now
	}
	s.todos.mu.Unlock()
	s.todos.writing.Unlock()
	s.logf("task %s: let go of %d stopped agent(s) for the agent taking it over", id, len(stopped))
	s.linksChanged(ctx)
	s.todosChanged(ctx)
	for _, a := range stopped {
		s.drainWorkspaceQueue(ctx, a.MachineID, a.Cwd)
	}
	return nil
}

// releaseTakenTodo lets go of a todo takeTodoForTask held, and ends the start it claimed with it; id "" is nothing.
func (s *Service) releaseTakenTodo(id string) {
	if id == "" {
		return
	}
	s.tasks.mu.Lock()
	delete(s.tasks.reusing, id)
	st := s.tasks.taking[id]
	delete(s.tasks.taking, id)
	s.tasks.mu.Unlock()
	if st != nil {
		s.endTaskStart(id, st, proto.Session{})
	}
}

// taskStartTimeout bounds one start of a task's agent: a busy machine's shell can take minutes to run claude
// (node's claudeStartWait, #1396).
var taskStartTimeout = 5 * time.Minute

func (t *PlanTask) planStartTimeout() time.Duration {
	repositories := 0
	if w := t.WorkspaceResolved; w != nil {
		repositories = len(w.Repositories)
	}
	if t.containerWorkspace() {
		repositories = max(repositories, len(t.Workspace.Container.SelectedRepoIDs))
	}
	if repositories == 0 && t.WorkspaceResolved == nil && t.backendOperationID() != "" {
		// Admission loads an unresolved operation's frozen repository set after the start deadline is set.
		repositories = repositorywire.MaxTaskRepositories
	}
	if repositories == 0 {
		return taskStartTimeout
	}
	// Leave room for an old workspace's cancellation and the new workspace's acquisition,
	// in addition to mirror preparation, direct checkout, and launching the agent.
	return taskStartTimeout + assistantwire.BackendWorkspacePrepareTimeout(repositories) + 2*assistantwire.BackendWorkspaceRPCTimeout
}

// taskFailTimeout bounds recording a failed start (the todo, the plan, the receipt), after the start's own time.
const taskFailTimeout = 30 * time.Second

// startPlanTask starts one task's agent and links it to the task's todo. A failed start ends the task: the todo is
// done with why (so nothing shows it as going on, #1077), the plan keeps the error, and the conversation gets a
// receipt — one per task: a retry that fails again (retry) updates the error the receipt shows (clients read it off
// the todo). A task the user finished while its agent was still starting (#2954) is not a failed start: the agent
// that did start stays in the task and the close loop retires it (tasks_close.go). Use startPlanTaskAsync to
// schedule it: go startPlanTask would register too late for the manager's idle check.
func (s *Service) startPlanTask(planID, conversation string, i int, t PlanTask, retry bool) {
	s.taskStarting.Add(1)
	defer s.taskStarting.Add(-1)
	s.runPlanTaskStart(planID, conversation, i, t, retry)
}

// startPlanTaskAsync reserves the start before launching its goroutine: the request may return before the
// goroutine is scheduled, and the manager must not see that gap as idle. Keep the reservation through cleanup.
func (s *Service) startPlanTaskAsync(planID, conversation string, i int, t PlanTask, retry bool, after func()) {
	s.taskStarting.Add(1)
	go func() {
		defer s.taskStarting.Add(-1)
		if after != nil {
			defer after()
		}
		s.runPlanTaskStart(planID, conversation, i, t, retry)
	}()
}

// activateDeferredTaskStart attaches the Companion's MCP servers and starts its first turn through the same ordinary
// send path a later message uses. Its client_msg_id is the id session.start carried, so an uncertain answer is never
// retried into a second first turn.
func (s *Service) activateDeferredTaskStart(ctx context.Context, t *PlanTask, mid, sid string) error {
	d := t.deferredStart
	if d == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskStartTimeout)
	defer cancel()
	params := map[string]any{"session_id": sid, "text": d.text, "client_msg_id": d.clientMsgID}
	if d.permissionMode != "" {
		params["permission_mode"] = d.permissionMode
	}
	if len(d.images) > 0 {
		params["images"] = d.images
	}
	return s.call(ctx, mid, DeviceSessionSend, params, nil)
}

// closeDeferredTaskStart discards a newly created ACP session that provably never got its first prompt. It stops only
// the Agent and leaves the user's terminal open. It is not used for an uncertain activation: that session may already
// be running and remains linked for recovery instead.
func (s *Service) closeDeferredTaskStart(ctx context.Context, t *PlanTask, mid, sid string) {
	if t.deferredStart == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
	defer cancel()
	if err := s.call(ctx, mid, deviceSessionStop, map[string]any{"session_id": sid}, nil); err != nil {
		s.logf("task %s: close deferred Companion session %s/%s: %v", t.TodoID, mid, sid, err)
	}
}

// deferBlockedTaskStart returns a known-unsent start to a retryable queue.
func (s *Service) deferBlockedTaskStart(ctx context.Context, t PlanTask, blocked *todoStartBlocked, retry bool) {
	if blocked.err != nil {
		s.logf("task %s: defer start: read prerequisites: %v", t.TodoID, blocked.err)
	}
	q := s.blockerWaitQueue(ctx, blocked.refs)
	q.Retry = retry
	policy := queueCAS
	switch {
	case t.RequestGate != "" && t.Queue != nil:
		policy = queueOwnerStart
	case t.claimedGatewayStart():
		policy = queueQuotaStart
	}
	if t.Queue != nil && (blocked.err != nil || blocked.after && t.Queue.Workspace != nil) {
		*q = *t.Queue
		q.Claimed, q.NextAt = false, time.Now().UTC()
		if ws := t.Queue.Workspace; ws != nil && len(blocked.refs) > 0 {
			*q = s.workspaceAfterWaitEntry(ctx, ws.Machine, ws.Path, blocked.refs, t.Queue)
		}
	}
	s.replaceTaskQueue(ctx, t, q, policy)
}

// runPlanTaskStart is the shared start body; its caller owns the idle reservation through every return path.
func (s *Service) runPlanTaskStart(planID, conversation string, i int, t PlanTask, retry bool) {
	t.planID, t.planIndex = planID, i
	ctx, cancel := context.WithTimeout(context.Background(), t.planStartTimeout())
	defer cancel()
	if t.RequestGate != "" {
		queued := t.Queue
		if !s.claimRequestStart(ctx, &t) {
			return
		}
		if queueExpired(queued, time.Now()) {
			task := &TaskPlan{ID: planID, Conversation: conversation}
			s.failQueuedStart(ctx, task, i, t, queued)
			return
		}
	} else if s.planRequestGate(t) != "" {
		s.logf("task %s: prerequisites changed before dispatch; make a new plan", t.TodoID)
		return
	}
	defer s.releaseTaskWorkspace(t.TodoID)
	// The admission claim (if the gate took one) is released when this start body ends either way: on success the
	// agent is linked into the todo by then so the ledger's direction count carries it, and on failure nothing runs.
	defer func() {
		if t.admissionRelease != nil {
			t.admissionRelease()
			t.admissionRelease = nil
		}
	}()
	placed, placedWhy, placedPermission := t.Machine, t.Why, t.Permission
	if t.Role == roleLeader { // legacy one-shot tool discovery may wait for this link (task_leader.go)
		s.leaderStarts.Add(1)
		defer s.leaderStarts.Add(-1)
	}
	t.Permission = strings.TrimSpace(t.Permission)
	mid, sid, err := s.startTaskAgent(ctx, &t, retry)
	var blocked *todoStartBlocked
	var lost startLinkLost
	var quota *gatewayQuotaWaitError
	var wsWait *workspaceBusyQueueError
	if !t.startWithoutReplay() && errors.As(err, &lost) && lost.back {
		// The link took the start's answer and the machine, back, does not show the agent: start it once more, with
		// a whole start's time of its own (the first may have spent its own on the dead link). Not counted against
		// the launcher (#7511).
		s.logf("task %s: start: %v; the link to %s dropped and came back without the agent; starting once more", t.TodoID, err, t.Machine)
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), t.planStartTimeout())
		defer rcancel()
		ctx = rctx
		mid, sid, err = s.startTaskAgent(ctx, &t, retry)
	} else if !t.startWithoutReplay() && err != nil && ctx.Err() == nil && !startStalled(err) &&
		!errors.As(err, &quota) && !errors.As(err, &wsWait) && !errors.As(err, &blocked) {
		// The machine's link dropped (a node restarting, a tunnel blinking): try once more after a moment. Any other
		// launcher failure may move a flexible task, after verifying its directory on the destination.
		// An unknown result (the wait ran out or the link dropped while the answer was out) already went through
		// adoption inside startTaskAgent: neither a blind retry nor a reroute may run, since the node can still be
		// creating that first session and either one would open a second agent (#6540).
		unknown := startResultUnknown(err)
		shouldRetry := transientStartErr(err) && !unknown
		if !shouldRetry && t.MachinePolicy == machineFlexible && !unknown {
			shouldRetry = s.previewHealthPlacement(ctx, &t)
		}
		if shouldRetry && (errors.Is(err, errMachineOffline) || errors.Is(err, apierr.ErrOffline)) {
			// Not sent: no connection to the machine. The retry waits a moment and then for the link to come back
			// (#7511), not only a fixed moment that a reconnect taking a minute outlasts; still down when the wait
			// ends, it fails as offline at once without reaching the machine.
			s.logf("task %s: start: %v; retrying once %s is back", t.TodoID, err, t.Machine)
			select {
			case <-time.After(s.taskStartRetry()):
			case <-ctx.Done():
			}
			if ctx.Err() == nil {
				s.waitMachineBack(ctx, t.Machine)
			}
			if ctx.Err() == nil {
				mid, sid, err = s.startTaskAgent(ctx, &t, retry)
			}
		} else if shouldRetry {
			s.logf("task %s: start: %v; retrying", t.TodoID, err)
			select {
			case <-time.After(s.taskStartRetry()):
				mid, sid, err = s.startTaskAgent(ctx, &t, retry)
			case <-ctx.Done():
			}
		}
	}
	if errors.As(err, &blocked) {
		s.deferBlockedTaskStart(ctx, t, blocked, retry)
		return
	}
	if errors.As(err, &quota) {
		if saveErr := s.persistGatewayWait(ctx, t, quota.queue); saveErr != nil {
			s.logf("task %s: save gateway quota wait: %v", t.TodoID, saveErr)
			err = i18n.Err("assistant-tasks.gateway_quota_unsaved")
		} else {
			return
		}
	}
	if errors.Is(err, errGatewayTaskHandled) {
		s.replaceTaskQueue(ctx, t, nil, queueCAS)
		return
	}
	var quotaState *gatewayQueueStateError
	if errors.As(err, &quotaState) {
		s.logf("task %s: queued launch eligibility unconfirmed: %v", t.TodoID, quotaState)
		return
	}
	// The admission and workspace queues publish the refusal together with its
	// retry time. A changed launch target still follows the usual plan update.
	planUpdate := t
	if isAdmissionDenied(err) || errors.As(err, &wsWait) {
		planUpdate.Placement = nil
	}
	if t.Machine != placed || t.Why != placedWhy || t.Permission != placedPermission {
		s.updatePlanTask(ctx, planID, i, planUpdate)
	} else if planUpdate.Placement != nil {
		// Recording the decision must not pin launch defaults or overwrite a workspace saved at admission.
		write, stop := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
		if err := s.patchPlanTask(write, planID, i, func(pt *PlanTask) {
			if pt.TodoID == t.TodoID {
				pt.Placement = t.Placement
			}
		}); err != nil {
			s.logf("task %s: save placement on plan: %v", t.TodoID, err)
		}
		stop()
	}
	launched := err == nil
	linkedDone := false
	if !launched && t.Dispatch != nil {
		// A failed start keeps its dispatch, with the candidate's failed starts in a row, for the next try to read
		// (candidate_keep.go: three re-pick).
		write, stop := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
		if saveErr := s.patchPlanTask(write, planID, i, func(pt *PlanTask) {
			if pt.TodoID == t.TodoID {
				pt.Dispatch = t.Dispatch
			}
		}); saveErr != nil {
			s.logf("task %s: record dispatch: %v", t.TodoID, saveErr)
		}
		stop()
	}
	if launched {
		// The start's own context can be spent already: an unanswered start whose session was just reconciled
		// (#6540) reaches here after taskStartTimeout, and a database write on that context fails at once, which
		// would fail the task although its agent is running. The link gets its own time, like the bookkeeping below.
		linkCtx, linkCancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
		if saveErr := s.patchPlanTask(linkCtx, planID, i, func(pt *PlanTask) {
			if pt.TodoID == t.TodoID {
				pt.Dispatch = t.Dispatch // the latest start's: a start on no tier clears an earlier one
			}
		}); saveErr != nil {
			s.logf("task %s: record dispatch: %v", t.TodoID, saveErr)
		}
		if t.TerminalTarget.Machine != "" {
			if saveErr := s.patchPlanTask(linkCtx, planID, i, func(pt *PlanTask) {
				if pt.TodoID == t.TodoID && pt.CloseTabOnAccept && pt.TerminalCleanup == "" && pt.TerminalTarget.Machine == "" {
					pt.TerminalTarget = t.TerminalTarget
				}
			}); saveErr != nil {
				s.logf("task %s: record terminal identity: %v", t.TodoID, saveErr)
			}
		}
		err = s.linkPlanAgent(linkCtx, t, mid, sid, false)
		linkCancel()
		if errors.Is(err, db.ErrTodoCompleted) {
			// The user tapped "Done" while the agent was starting (a busy machine takes minutes, #1396). The task stays
			// done and its agent stays in it: nothing reads failed, and the close loop still finds the agent to retire.
			retryCtx, retryCancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
			err = s.linkPlanAgent(retryCtx, t, mid, sid, true)
			retryCancel()
			linkedDone = err == nil
			if err != nil {
				s.logf("task %s: link to the finished task: %v", t.TodoID, err)
			}
		}
	}
	if err == nil && t.deferredStart != nil {
		if linkedDone {
			// The owner finished the task while its ACP identity was being created. No first prompt is needed now.
			s.closeDeferredTaskStart(ctx, &t, mid, sid)
		} else {
			err = s.activateDeferredTaskStart(ctx, &t, mid, sid)
			if err != nil && sendUnknown(err) {
				// The role link is durable and the send may already be running. Keep it exactly as an adopted unknown start:
				// no close, failure receipt or blind second Agent; a later send uses the now-linked identity.
				s.logf("task %s: deferred Companion activation has an unknown result; keeping the linked session: %v", t.TodoID, err)
				err = nil
			}
		}
	}
	if err != nil && t.deferredStart != nil {
		// Linking failed before any prompt, or activation definitely refused it: this new ACP session is safe to reclaim.
		s.closeDeferredTaskStart(ctx, &t, mid, sid)
	}
	if err == nil {
		if !linkedDone && s.settings(ctx).todosV2On() {
			rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
			if recErr := s.RecordSupervisedTaskActive(rctx, t.TodoID, mid, sid, s.runtimeBootEpoch()); recErr != nil {
				s.logf("task %s: record todos_v2 active checkpoint: %v", t.TodoID, recErr)
			}
			rcancel()
		}
		if t.HandoffID != "" {
			hctx, hcancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
			_, _ = s.recordTodoHandoffDelivery(hctx, t.HandoffID, mid, sid, t.startedAgent, handoffDeliveryAccepted, "")
			hcancel()
		}
		if t.Queue != nil { // a queued retry made it through the gate and linked: its waiting record is done
			s.clearAdmissionQueue(context.WithoutCancel(ctx), planID, i, t.TodoID)
		}
		s.remedyStarted(context.WithoutCancel(ctx), planID, conversation, i, t) // after an assistant's retry (start_remedy.go)
		return
	}
	if t.RequestGate != "" {
		var blocked *requestBlocked
		if errors.As(err, &blocked) {
			s.requeueRequestStart(ctx, t, blocked.ids)
			return
		}
	}
	if t.startWithoutReplay() {
		var remote *rpc.Error
		if launched || startResultUnknown(err) || errors.As(err, &lost) || nodeMayHaveTaken(err) ||
			errors.As(err, &remote) && remote.Code == "identity_conflict" {
			if t.HandoffID != "" {
				hctx, hcancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
				_, _ = s.recordTodoHandoffDelivery(hctx, t.HandoffID, t.Machine, "", "", handoffDeliveryUnknown, err.Error())
				hcancel()
			}
			s.logf("task %s: queued launch unconfirmed, not replaying: %v", t.TodoID, err)
			return
		}
	}
	if errors.As(err, &wsWait) {
		s.queueWorkspaceBusy(planID, i, t, wsWait, retry)
		return
	}
	// An admission-gate refusal means the machine or direction is briefly full, not that the start failed: park the
	// task in the admission queue (no failTodo, no failure receipt) and let the queue loop retry it (task_queue.go).
	if isAdmissionDenied(err) {
		s.queueAdmissionRefusal(planID, i, t, err, retry)
		return
	}
	s.logf("task %s: start: %v", t.TodoID, err)
	// The start may have used up ctx (a busy machine's shell taking all of taskStartTimeout): recording the failure
	// on it failed too, and the task stayed "starting" with no agent for ever (#4510). The bookkeeping gets its own time.
	ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
	defer cancel()
	limit := 200
	if code, _ := codedStartRefusal(err); code != "" {
		limit = proto.CodedStartErrorMax // the hub's sentence, whose remedy comes last (#1642): kept whole
	}
	reason := clip(oneLine(s.startFailureText(ctx, t.Machine, err)), limit)
	if t.Queue != nil {
		// A queued start got past the gate and failed otherwise: its wait is over, and its card reads what happened
		// next rather than "waiting" until the queue's bounds run out (#8092).
		s.clearAdmissionQueue(ctx, planID, i, t.TodoID)
	}
	// The new tab's shell waits on a question, or the agent's screen stopped, and the computer keeps the tab: a
	// supervised task's start is remedied first, not failed yet (start_remedy.go, #2570).
	code, args := codedStartRefusal(err)
	// The launcher this start ran, when the refusal does not name it: a tier, a start-defaults policy or the agent.new
	// binding may have chosen it, which the plan does not keep, and a retry on another launcher starts from it
	// (ranLauncher, #10365).
	if args["launcher"] == "" {
		if args == nil {
			args = map[string]string{}
		}
		args["launcher"] = s.startLauncher(ctx, t.Launcher)
	}
	if !t.usesPreparedWorkspace() && t.RequestGate == "" && s.beginRemedy(ctx, planID, i, t, err, code, args, reason) {
		return
	}
	// A retry the assistant asked for that failed so its tab is gone: the start fails for good, and the supervisor and
	// the user hear of it as of a first failure.
	remedied := t.Start != nil && t.Start.State == remedying
	// Its launcher's failure on that computer: the others there it may start with, for the card and the supervisor
	// (start_retry_launcher.go, #10365). Only suggested: nothing starts on them unless someone asks.
	var retryWith []string
	if m, ok := s.machineByName(ctx, t.Machine); ok {
		failed := t
		failed.ErrorCode, failed.ErrorArgs = code, args
		retryWith = s.retryLaunchers(ctx, m, failed, err)
	}
	if perr := s.patchPlanTask(ctx, planID, i, func(pt *PlanTask) {
		pt.Error = err.Error()
		var problem *taskworkspace.Problem
		if errors.As(err, &problem) {
			pt.WorkspaceError = problem
		} else if t.usesPreparedWorkspace() && t.backendRequest != nil {
			pt.WorkspaceError = taskworkspace.Failure("engine_start_failed", "start", t.backendRequest.OperationID, "", t.Cwd)
		}
		pt.ErrorCode, pt.ErrorArgs, pt.RetryWith = code, args, retryWith
		if remedied && pt.Start != nil {
			pt.Start.State, pt.Start.Pane, pt.Start.At = remedyFailed, "", time.Now().UTC()
		}
	}); perr != nil {
		s.logf("plan %s: task %d's start error: %v", planID, i, perr)
	}
	if ferr := s.failTodo(ctx, t.TodoID, reason); ferr != nil {
		s.logf("task %s: fail: %v", t.TodoID, ferr)
	}
	if t.HandoffID != "" {
		_, _ = s.recordTodoHandoffDelivery(ctx, t.HandoffID, t.Machine, "", "", handoffDeliveryRejected, err.Error())
	}
	s.tasks.mu.Lock()
	s.tasks.receipted[t.TodoID] = time.Now()
	s.tasks.mu.Unlock()
	if remedied {
		s.remedyStep(ctx, conversation, t.TodoID, startRemedyBody{Action: "gave_up", By: "hub", Attempt: t.Start.Attempt, Reason: reason})
	}
	if retry && !remedied {
		return
	}
	_, _ = s.appendFeed(ctx, conversation, feedReceipt, t.TodoID, receiptBody{Title: t.Title, State: "failed",
		Summary: i18n.T(s.lang(ctx), "assistant-tasks.failed_start", i18n.Args{"reason": reason})})
	if todo, ok, err := s.todo(ctx, t.TodoID); err == nil && ok {
		s.taskStartFailed(ctx, todo, t.Machine, t.Start, reason)
		s.taskReport(todo, reportFailed, t.Machine, [2]string{}, reason, nil)
	}
}

var (
	errTaskNotRetryable = i18n.Err("assistant-tasks.not_retryable")
	errTaskNotInPlan    = i18n.Err("assistant-tasks.not_in_plan")
	errMachineOffline   = errors.New("machine offline") // matched with errors.Is; the text is machineOffline's
)

// machineOffline is errMachineOffline for machine name, said in the request's language.
func machineOffline(name string) error {
	return offlineError{i18n.Err("assistant-tasks.machine_offline", i18n.Args{"name": name})}
}

type offlineError struct{ msg error }

func (e offlineError) Error() string        { return e.msg.Error() }
func (e offlineError) Unwrap() error        { return e.msg }
func (e offlineError) Is(target error) bool { return target == errMachineOffline }

var errTaskPlanHistory = errors.New("task plan execution history is unavailable")

type planExecutionHistoryStore interface {
	TaskPlansMadeSince(ctx context.Context, since time.Time) ([]db.TaskPlanRow, error)
}

// planExecutionHistory lists every started or dismissed plan across conversations. Main-chat handoffs move open
// to-dos without moving their plans, so no single Origin/Supervisor/historical conversation is a complete search scope.
func (s *Service) planExecutionHistory(ctx context.Context) ([]*TaskPlan, error) {
	if st := s.store(); st != nil {
		history, ok := st.(planExecutionHistoryStore)
		if !ok {
			return nil, errTaskPlanHistory
		}
		rows, err := history.TaskPlansMadeSince(ctx, time.Time{})
		if err != nil {
			return nil, err
		}
		plans := make([]*TaskPlan, 0, len(rows))
		for _, row := range rows {
			plans = append(plans, planFromRow(row))
		}
		return plans, nil
	}
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	var plans []*TaskPlan
	for _, plan := range s.tasks.plans {
		if plan.State != planStarted && plan.State != planDismissed {
			continue
		}
		cp := *plan
		cp.Tasks = append([]PlanTask(nil), plan.Tasks...)
		plans = append(plans, &cp)
	}
	return plans, nil
}

// planHistoryOnce is planExecutionHistory read at most once, when first asked: one sweep over the open tasks
// (remedyWatch) shares it, as it lists each machine once (machineSessions), instead of reading and decoding every plan
// ever started for each to-do with no plan id of its own (#8770). Its plans are read only. A failed first read is
// shared too, so that pass defers every remedy expiry to the next sweep, which builds a fresh closure and tries again.
func (s *Service) planHistoryOnce() func(context.Context) ([]*TaskPlan, error) {
	var (
		read  bool
		plans []*TaskPlan
		err   error
	)
	return func(ctx context.Context) ([]*TaskPlan, error) {
		if !read {
			read = true
			plans, err = s.planExecutionHistory(ctx)
		}
		return plans, err
	}
}

// currentPlanAssignment resolves the execution binding, which is not the to-do's historical PlanID after a same-to-do
// continuation. An exact binding in the same owning conversation keeps the ordinary direct lookup cheap. A handoff or
// retired binding requires a global started-plan check: exactly one exact TodoID is current; guessing or merging
// candidates could restore the wrong permission, so absence and ambiguity both fail closed with errTaskNotInPlan.
func (s *Service) currentPlanAssignment(ctx context.Context, todo Todo) (*TaskPlan, int, error) {
	return s.currentPlanAssignmentIn(ctx, todo, s.planExecutionHistory)
}

// currentPlanAssignmentIn is currentPlanAssignment with the global check reading the plans' history through history.
func (s *Service) currentPlanAssignmentIn(ctx context.Context, todo Todo, history func(context.Context) ([]*TaskPlan, error)) (*TaskPlan, int, error) {
	p, loadErr := s.loadPlan(ctx, todo.PlanID)
	if loadErr != nil && !errors.Is(loadErr, errPlanNotFound) {
		return nil, 0, loadErr
	}
	if loadErr == nil {
		i := slices.IndexFunc(p.Tasks, func(t PlanTask) bool { return t.TodoID == todo.ID })
		sameOwner := p.Conversation == todo.Origin && (todo.Supervision == nil || todo.Supervision.Supervisor == "" || todo.Supervision.Supervisor == p.Conversation)
		if i >= 0 && sameOwner {
			return p, i, nil
		}
	}
	plans, err := history(ctx)
	if err != nil {
		return nil, 0, err
	}
	var found *TaskPlan
	foundAt := -1
	for _, candidate := range plans {
		if candidate.State != planStarted {
			continue
		}
		for i, task := range candidate.Tasks {
			if task.TodoID != todo.ID {
				continue
			}
			if found != nil {
				return nil, 0, errTaskNotInPlan
			}
			found, foundAt = candidate, i
		}
	}
	if found == nil {
		return nil, 0, errTaskNotInPlan
	}
	return found, foundAt, nil
}

// retryTask starts again the agent of a task that did not start (#1396): the same task as its plan has it (machine,
// directory, prompt, launcher, model, permission, title), in the same todo — reopened, its error and the plan's
// cleared — so the task shows as starting where it was, not as a second one. A task already
// starting again, or started since, is answered as it is: a double tap starts one agent. A machine that is offline is
// refused before anything changes. A task that is open is not started again: one whose agent the model service cut
// off is told to go on (continueTask).
// The todo it answers is as the apps get it.
func (s *Service) retryTask(ctx context.Context, todoID string) (json.RawMessage, error) {
	return s.retryTaskWith(ctx, todoID, retryWith{})
}

// retryTaskWith is retryTask, on another launcher of the same computer when with names one (retryOn, #10365): checked
// before the to-do is reopened, a refusal changes nothing. The same launcher goes back to the tab its last start
// stopped in while the computer keeps it (keptTabOf), so a shell question answered there meanwhile does not stop it
// again in a new tab; another launcher opens a new one. With an answer, that answer is typed into the tab first
// (answerKeptTab), and only a failed start whose question it answers starts again: refused, nothing changes. So with a
// launcher: when another start of the task is on its way (a Retry, task_retry, a plan taking the to-do over) or it
// started since, nothing starts on the launcher asked for, and that is refused rather than answered as if it had.
func (s *Service) retryTaskWith(ctx context.Context, todoID string, with retryWith) (json.RawMessage, error) {
	view, started, err := s.retryTaskStart(ctx, todoID, with)
	if err == nil && !started && with.launcher != "" {
		return nil, retryRefused{i18n.Err("assistant-tasks.retry_launcher_not_started", i18n.Args{"launcher": with.launcher})}
	}
	return view, err
}

// retryTaskStart is retryTaskWith; started: this call started the task's agent again (else the to-do is answered as
// it is: another start is on its way, or it started since, a cut-off agent of it told to go on unless a launcher was
// named).
func (s *Service) retryTaskStart(ctx context.Context, todoID string, with retryWith) (_ json.RawMessage, started bool, _ error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return nil, false, err
	}
	cur, ok, err := s.todo(ctx, todoID)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, db.ErrTodoNotFound
	}
	// A to-do no conversation opened is nobody's task to start again. One with no plan id of its own (a plan took it
	// over through todo_id) is: the plan that binds it is found below (currentPlanAssignment, #8770).
	if cur.Origin == "" {
		return nil, false, errTaskNotRetryable
	}
	// A caller that saw one failure starts only that one again: once the to-do is open or failed anew it is answered as
	// it is, and no cut-off agent of it is told to go on.
	if !with.failedAt.IsZero() && (cur.DoneAt == nil || !cur.DoneAt.Equal(with.failedAt) || !cur.failedStart()) {
		view, err := s.todoView(ctx, todoID)
		return view, false, err
	}
	if cur.DoneAt == nil {
		if with.answer != "" { // open: started again, or remedied by the assistant
			return nil, false, errAnswerNotWaiting
		}
		if with.launcher != "" { // open: started again since; another launcher is for a failed start, not a Continue
			view, err := s.todoView(ctx, todoID)
			return view, false, err
		}
		view, err := s.continueTask(ctx, cur)
		return view, false, err
	}
	failed := cur.failedStart()
	var st *taskStart
	busy := s.taskStartOnWay(todoID)
	if failed {
		var claimed bool
		st, claimed = s.claimTaskStart(todoID)
		busy = !claimed
	}
	if busy || !failed {
		if with.answer != "" { // another start is on its way, or it is done: one answer is typed by one start
			return nil, false, errAnswerNotWaiting
		}
		if !busy && cur.DoneAt != nil {
			return nil, false, errTaskNotRetryable // done by hand or by the hub, not a failed start
		}
		view, err := s.todoView(ctx, todoID)
		return view, false, err
	}
	defer func() {
		if !started {
			s.endTaskStart(todoID, st, proto.Session{})
		}
	}()
	p, i, err := s.currentPlanAssignment(ctx, cur)
	if err != nil {
		return nil, false, err
	}
	p.Tasks[i].normalizeReadonlyPermission(readonlyModeOf(s.settings(ctx)))
	t := p.Tasks[i]
	if t.RequestGate != "" || s.planRequestGate(t) != "" {
		return nil, false, retryRefused{i18n.Err("assistant-tasks.request_new_plan")}
	}
	t.Permission = strings.TrimSpace(t.Permission)
	if t.Machine == "" {
		if with.answer != "" {
			return nil, false, errAnswerNotWaiting
		}
		if with.launcher == "" && with.model != "" {
			return nil, false, planInvalid{i18n.Err("assistant-tasks.retry_model_without_launcher")}
		}
		if err := checkModelID(with.model); err != nil {
			return nil, false, planInvalid{err}
		}
		from := s.ranLauncher(ctx, t)
		if with.launcher != "" {
			t.Launcher, t.Model, t.Tier = with.launcher, with.model, ""
			t.LauncherDefaultModel = with.model == ""
			t.RetryWith = nil
		}
		reopened, ok, err := s.reopenFailedTodo(ctx, todoID)
		if err != nil {
			return nil, false, err
		}
		view, err := json.Marshal(reopened)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return view, false, nil
		}
		q := s.pendingPlacementQueueEntry(ctx, p.ID, 1, 0, s.placementNow())
		q.Retry = true
		var savedPlacement *placement
		if err := s.patchPlanTask(ctx, p.ID, i, func(pt *PlanTask) {
			*pt = t
			pt.Queue = &q
			pt.Placement = nil
			pt.Placement = queuedPlacement(*pt, &q)
			savedPlacement = pt.Placement
			pt.WorkspaceError = nil
			pt.Error, pt.ErrorCode, pt.ErrorArgs, pt.RetryWith = "", "", nil, nil
		}); err != nil {
			s.logf("task %s: retry placement requeue failed (plan %s): %v", todoID, p.ID, err)
			if ferr := s.failTodo(ctx, todoID, err.Error()); ferr != nil {
				s.logf("task %s: retry placement rollback fail: %v", todoID, ferr)
			}
			return nil, false, err
		}
		if savedPlacement != nil {
			s.savePlacement(ctx, savedPlacement)
		}
		if with.launcher != "" && with.launcher != from && with.by != "" {
			s.remedyStep(ctx, p.Conversation, todoID, startRemedyBody{Action: "retry", By: with.by, LauncherFrom: from, LauncherTo: with.launcher})
		}
		s.logf("task %s: retrying its start via placement queue (plan %s)", todoID, p.ID)
		started = true
		s.endTaskStart(todoID, st, proto.Session{})
		s.onPendingPlacementEnqueued(ctx, p.ID, todoID, 1)
		return view, true, nil
	}
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return nil, false, i18n.Err("assistant-tasks.no_machine", i18n.Args{"machine": strconv.Quote(t.Machine)})
	}
	if err := s.leaderOnWorkMachine(&t, m); err != nil { // a retry of a leader plan saved on a computer that is work-trust now
		return nil, false, fmt.Errorf("%w; nothing was started", err)
	}
	if !m.Reachable {
		return nil, false, machineOffline(m.Name)
	}
	launcher, model, from := t.Launcher, t.Model, s.ranLauncher(ctx, t)
	inPane := keptTabOf(t, time.Now())
	if err := s.retryOn(ctx, &t, m, with); err != nil {
		return nil, false, err
	}
	moved := t.Launcher != launcher && t.Launcher != from // on another launcher than the one that failed
	if moved || t.Model != model {                        // another launcher starts in a new tab, as task_retry's does
		inPane = ""
	}
	typed := false
	if with.answer != "" {
		if typed, err = s.answerKeptTab(ctx, m, t, inPane, with.answer); err != nil {
			return nil, false, err
		}
	}
	reopened, ok, err := s.reopenFailedTodo(ctx, todoID)
	if err != nil {
		return nil, false, err
	}
	view, err := json.Marshal(reopened)
	if err != nil {
		return nil, false, err
	}
	if !ok { // another assistant sharing the store reopened it first
		return view, false, nil
	}
	// Task i is kept as this retry starts it (t), its error cleared. The plan is read again under its lock, so what its
	// other tasks recorded since p was read stays.
	if err := s.patchPlanTask(ctx, p.ID, i, func(pt *PlanTask) {
		*pt = t
		pt.WorkspaceError = nil
		pt.Error, pt.ErrorCode, pt.ErrorArgs, pt.RetryWith = "", "", nil, nil
	}); err != nil {
		s.logf("plan %s: %v", p.ID, err)
	}
	if moved && with.by != "" { // the step the task's history shows: who moved it to which launcher
		s.remedyStep(ctx, p.Conversation, todoID, startRemedyBody{Action: "retry", By: with.by, LauncherFrom: from, LauncherTo: t.Launcher})
	}
	switch { // the answer's id, never what a prompt asked for
	case with.answer != "" && typed:
		s.remedyStep(ctx, p.Conversation, todoID, startRemedyBody{Action: "answer", By: with.by, Choice: with.answer})
	case with.answer != "": // the question had left the tab's last line (answered at the computer): not the owner's answer
		s.remedyStep(ctx, p.Conversation, todoID, startRemedyBody{Action: "answer_not_typed", By: with.by, Choice: with.answer})
	}
	s.logf("task %s: retrying its start on %s", todoID, m.ID)
	started = true
	t.inPane = inPane
	s.startPlanTaskAsync(p.ID, p.Conversation, i, t, true, func() { s.endTaskStart(todoID, st, proto.Session{}) })
	return view, true, nil
}

// continueText is what goes on a cut-off turn: what the chat's "Retry" on an API error card sends.
const continueText = "继续"

// continueHold bounds how long a sent "Continue" keeps further taps from sending another while the agent takes it up.
var continueHold = 30 * time.Second

// continueTask tells the agent of a task whose latest turn the model service cut off (#1516) to go on, as the chat's
// "Retry" on the error card does; the task shows as in progress again once the agent works. One "Continue" per cut: taps while
// one is on its way, or before the agent took it up, answer the todo as it is, as does a task with no agent cut off.
// The agent's machine offline is refused.
func (s *Service) continueTask(ctx context.Context, cur Todo) (json.RawMessage, error) {
	if cur.Supervision != nil && cur.Supervision.state(cur) == reviewTimedOut {
		return s.continueTimedOut(ctx, cur)
	}
	cut := cutAgent(s.taskAgents(ctx, cur))
	if cut.SessionID == "" {
		return s.todoView(ctx, cur.ID)
	}
	if proto.UnrecoverableTurnErrorCode(cut.TurnError) != "" || cut.TurnErrorKind == proto.TurnErrorUnrecoverable ||
		cut.TurnErrorCode == proto.TurnErrorCompactionSealed {
		return nil, i18n.Err("assistant-agents_work.conversation_invalid")
	}
	at := s.cutRow(cut) // the turn picked, by the identity it shows by: every check below goes by it
	m, ok := s.device(ctx, cut.MachineID)
	if !ok || !m.Reachable {
		name := cut.MachineID
		if ok {
			name = m.Name
		}
		return nil, machineOffline(name)
	}
	st, claimed := s.claimTaskStart(cur.ID)
	if !claimed {
		return s.todoView(ctx, cur.ID)
	}
	release := func() { s.endTaskStart(cur.ID, st, proto.Session{}) }
	// Still the turn it was picked for, now that this Continue holds the claim: one that went on meanwhile (an
	// earlier Continue taken up, the owner at the computer) is not continued again.
	if now := cutAgent(s.taskAgents(ctx, cur)); now.SessionID != cut.SessionID || now.TurnError != cut.TurnError || continueTakenUp(now, cut, at) {
		release()
		return s.todoView(ctx, cur.ID)
	}
	if stoppedAgent(cut) { // it stopped since (read-only, #4754): a new agent goes on with the task (tasks_successor.go)
		var started proto.Session
		defer func() { s.endTaskStart(cur.ID, st, started) }() // a message that waited for it goes to the new agent
		if live, _ := liveTaskAgents(s.taskAgents(ctx, cur)); len(live) > 0 {
			return s.todoView(ctx, cur.ID) // another agent of the task still runs: it is that one's
		}
		var err error
		if started, err = s.startTaskSuccessor(ctx, cur, cut, continueText); err != nil {
			_, st.uncertain = errors.AsType[*uncertainTaskStart](err)
			return nil, err
		}
		return s.todoView(ctx, cur.ID)
	}
	if err := s.call(ctx, cut.MachineID, DeviceSessionSend, map[string]any{"session_id": cut.SessionID, "text": continueText}, nil); err != nil {
		release()
		return nil, err
	}
	s.logf("task %s: told %s/%s to go on after: %s", cur.ID, cut.MachineID, cut.SessionID, clip(oneLine(cut.TurnError), 120))
	go func() {
		defer release()
		ctx := context.WithoutCancel(ctx)
		n := 0
		for end := time.Now().Add(continueHold); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
			if n++; s.continuePolled != nil {
				s.continuePolled(n)
			}
			// Still cut off by the same error: not taken up yet. The session is as the node lists it: Claude Code's error
			// is its last words, a Codex turn's the node's own TurnError (no words of its answer, #2185).
			c, ok := s.session(ctx, cut.MachineID, cut.SessionID)
			if !ok {
				continue // no answer this time: nothing says it took it up, the claim stays (until continueHold)
			}
			if c.Status == proto.StatusWorking || continueTakenUp(c, cut, at) {
				return // it took it up: a later cut may be continued again
			}
		}
	}()
	return s.todoView(ctx, cur.ID)
}

// cutAgent is the agent among agents whose turn was cut off (a turn error) latest; none when no turn was.
func cutAgent(agents []proto.Session) proto.Session {
	var cut proto.Session
	for _, c := range agents {
		if c.TurnError != "" && (cut.SessionID == "" || c.UpdatedAt.After(cut.UpdatedAt)) {
			cut = c
		}
	}
	return cut
}

// cutRow is the identity of cut's turn that Continue goes by (continueTakenUp): for the hub's own word on a turn with
// no output (silent_turns.go), the row its verdict was made at, which is what the verdict holds by; a row read later
// (a moment late, its time corrected since) is no other turn. Otherwise the row as picked.
func (s *Service) cutRow(cut proto.Session) rowAt {
	if proto.SilentTurnKind(cut.TurnErrorKind) {
		if m, ok := s.quietMarks(cut.MachineID)[cut.SessionID]; ok {
			return m.rowAt
		}
	}
	return rowOf(cut)
}

// continueTakenUp: agent c (the node's row, or the lists') is no longer at cut, the turn "Continue" was sent for, at
// row at (cutRow). The hub's own word on a turn with no output (silent_turns.go) is not on the node's row: that turn
// is over once the verdict no longer holds for c (rowAt.holds, the very check that shows it), so the guard is never let
// go while the verdict still shows. Otherwise the error that cut it off is gone: Claude Code's is its last words, a
// Codex turn's the node's own TurnError (#2185).
func continueTakenUp(c, cut proto.Session, at rowAt) bool {
	if proto.SilentTurnKind(cut.TurnErrorKind) {
		return !at.holds(c)
	}
	return strings.TrimSpace(c.LastText) != cut.TurnError && c.TurnError != cut.TurnError
}

// taskStartRetryDelay is how long a task waits before its one retry after a start the new tab's terminal was not
// ready for, or a flexible task's move (Service.taskRetry shortens it in tests). A start that found no connection to
// its machine waits for the link instead (waitMachineBack, tasks_start_link.go).
const taskStartRetryDelay = 3 * time.Second

// taskStartRetry is the wait before a failed task start's one retry: taskStartRetryDelay unless tests set taskRetry.
func (s *Service) taskStartRetry() time.Duration {
	if s.taskRetry > 0 {
		return s.taskRetry
	}
	return taskStartRetryDelay
}

// Bounds of the bounded retry before a needs recheck treats a node as unreachable: agentReachBackoff before the
// second try and doubled before the third; agentReachMax bounds one whole recheck, machines included (tests shorten).
var (
	agentReachAttempts = 3
	agentReachBackoff  = 300 * time.Millisecond
	agentReachMax      = 2 * time.Second
)

// transientStartErr: the agent did not start because a link on the way was down or dropped (this hub's to the
// machine, or one further on that answered with the same words: a member hub, a connector), or the new tab's
// terminal was not ready for the start command (proto.CodeTerminalNotReady), not because the node refused or the
// launcher failed; worth one retry, and never counted against the launcher (launcher_health.go).
func transientStartErr(err error) bool {
	var re *rpc.Error
	return errors.Is(err, rpc.ErrClosed) || errors.Is(err, apierr.ErrOffline) ||
		errors.As(err, &re) && (re.Message == rpc.ErrClosed.Error() || re.Code == proto.CodeTerminalNotReady)
}

// taskPlanWithdrawnEvent: a conversation withdrew a plan card of its own (task_plan_withdraw). Detail: plan,
// conversation, reason, tasks (their titles).
const taskPlanWithdrawnEvent = "task_plan_withdrawn"

// withoutWithdrawn is plans less those their own conversation withdrew (task_plan_withdrawn in events): dismissed as
// the owner's Dismiss leaves them, but the owner did not answer them, so they say nothing about which kinds of task the
// owner starts or turns down (rulePlanRuns, the setup suggestions).
func withoutWithdrawn(plans []*TaskPlan, events []db.WorkEvent) []*TaskPlan {
	withdrawn := map[string]bool{}
	for _, e := range events {
		if id, _ := e.Detail["plan"].(string); e.Kind == taskPlanWithdrawnEvent && id != "" {
			withdrawn[id] = true
		}
	}
	if len(withdrawn) == 0 {
		return plans
	}
	out := make([]*TaskPlan, 0, len(plans))
	for _, p := range plans {
		if !withdrawn[p.ID] {
			out = append(out, p)
		}
	}
	return out
}

// toolTaskPlanWithdraw is task_plan_withdraw: the conversation that put up plan card plan_id takes it down while nothing
// of it has started, as the owner's Dismiss does (dismissPlan), its reason kept as a task_plan_withdrawn work event
// (#6739). A card whose work was done elsewhere, or that the owner dropped, otherwise waited for "Start" in every list
// of what is the owner's to handle until they dismissed it themselves. Another conversation's plan, and one that
// started or was dismissed, are refused, and nothing changes.
func (s *Service) toolTaskPlanWithdraw(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		PlanID string `json:"plan_id"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	reason, err := remedyReason(a.Reason)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(a.PlanID)
	if id == "" {
		return "", errors.New("plan_id is required: the plan's id, as task_plan's answer or the snapshot's plans waiting to start give it")
	}
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		return "", fmt.Errorf("plan %s: %w", id, err)
	}
	if p.Conversation != self {
		return "", fmt.Errorf("plan %s was put up by another conversation: only that one withdraws it (or the user dismisses it); nothing was changed", id)
	}
	if p.State != planOpen {
		return "", fmt.Errorf("plan %s is %s, no longer waiting for Start: nothing was withdrawn", id, p.State)
	}
	if p, err = s.dismissPlan(ctx, id); err != nil {
		return "", fmt.Errorf("plan %s: %w; nothing was withdrawn", id, err)
	}
	titles := make([]string, 0, len(p.Tasks))
	for _, t := range p.Tasks {
		titles = append(titles, t.Title)
	}
	s.workEvent("", self, taskPlanWithdrawnEvent, map[string]any{"plan": id, "conversation": self, "reason": reason, "tasks": titles})
	s.logf("plan %s: withdrawn by its conversation %s: %s", id, short(self), reason)
	return fmt.Sprintf("Withdrew plan %s (%d task(s): %s): its card no longer waits for Start. Reason kept: %s", id, len(titles),
		strings.Join(titles, "; "), reason), nil
}

func (s *Service) dismissPlan(ctx context.Context, id string) (*TaskPlan, error) {
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.State != planOpen {
		return nil, errPlanNotOpen
	}
	p.State = planDismissed
	ok, err := s.movePlan(ctx, p, planOpen)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errPlanNotOpen
	}
	s.event(ctx, map[string]any{"type": "task_plan", "plan": s.planWithSpace(ctx, p)})
	return p, nil
}
