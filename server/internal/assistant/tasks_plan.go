package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the assistant's task_plan tool ----

// planTaskIn is a task as task_plan's arguments have it. Its todo_id is the todo to run on (PlanTask.ReuseTodo); the
// plan's own todo_id, the todo a started task became, is the hub's to set, and the shallower field wins in JSON.
type planTaskIn struct {
	PlanTask
	taskContractInput
	TodoID        string   `json:"todo_id"`
	WritePrefixes []string `json:"write_prefixes,omitempty"`
	ForceParallel bool     `json:"force_parallel,omitempty"`
	ChangedFiles  []string `json:"changed_files,omitempty"`
}

// toolTaskPlanFrom is task_plan from the conversation sessionID running on machine (where its attachments are).
func (s *Service) toolTaskPlanFrom(ctx context.Context, machine, sessionID string, args json.RawMessage) (string, error) {
	return s.taskPlanFrom(ctx, machine, sessionID, args, nil)
}

// taskPlanFrom is task_plan from sessionID: a conversation (scope nil), or the agent of the task leader's task in scope,
// which plans workers beneath that task only and starts them at once (task_leader.go).
func (s *Service) taskPlanFrom(ctx context.Context, machine, sessionID string, args json.RawMessage, scope *leaderScope) (string, error) {
	wait, err := agentWaitDuration(args)
	if err != nil {
		return "", err
	}
	var a struct{ Tasks []PlanTask }
	var contracts []taskContract
	lead := "" // optional initial leader assignment; omission keeps the planning lead or main
	if scope != nil {
		tasks, err := scope.workerTasks(args)
		if err != nil {
			return "", err
		}
		a.Tasks = tasks
	} else {
		var in struct {
			Tasks      []planTaskIn `json:"tasks"`
			Supervisor string       `json:"supervisor"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("tasks: %v", err)
		}
		var err error
		if lead, err = s.planSupervisor(ctx, sessionID, in.Supervisor); err != nil {
			return "", err
		}
		todosV2 := s.settings(ctx).todosV2On()
		for i, t := range in.Tasks {
			contract, err := t.validatedContract()
			if err != nil {
				return "", fmt.Errorf("task %d: %w", i+1, err)
			}
			contracts = append(contracts, contract)
			t.PlanTask.ReuseTodo = strings.TrimSpace(t.TodoID)
			t.PlanTask.Parent = "" // the hub's, set only for a task leader's workers
			t.PlanTask.Supervisor = lead
			t.PlanTask.WriteScope = nil
			if todosV2 && (len(t.WritePrefixes) > 0 || len(t.ChangedFiles) > 0) {
				t.PlanTask.WriteScope = &planWriteScope{WritePrefixes: t.WritePrefixes, ChangedFiles: t.ChangedFiles, ForceParallel: t.ForceParallel}
			}
			a.Tasks = append(a.Tasks, t.PlanTask)
		}
	}
	if len(a.Tasks) == 0 {
		return "", fmt.Errorf("tasks is empty")
	}
	if len(a.Tasks) > 8 {
		return "", fmt.Errorf("at most 8 tasks in one plan")
	}
	if scope != nil {
		if err := s.checkLeaderRoom(ctx, scope, len(a.Tasks)); err != nil {
			return "", err
		}
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", fmt.Errorf("todos are unavailable: %v", err)
	}
	// Inspect the original fields before goal clipping or any workspace/brief side effects.
	if err := s.checkPlanRepositoryDuplicates(ctx, a.Tasks); err != nil {
		return "", err
	}
	reused := map[string]bool{}
	modes := map[string]string{} // each computer's Claude Code default mode, read once for this plan
	for i := range a.Tasks {
		t := &a.Tasks[i]
		t.WaitInputID = ""
		t.BackendStartDigest = ""
		t.WorkspaceError = nil
		asked := strings.TrimSpace(t.Permission) // the caller's own, before read-only replaces it
		if t.Readonly {
			asked = ""
		}
		callerPermission := asked
		t.CallerPermission = &callerPermission
		t.LauncherDefaultModel = false // the hub's: a tool call cannot import another start's native-default choice
		t.StartPermission = ""
		t.RetryFromAgent, t.RetryFromAllowAll = "", false // a new plan cannot import another retry's provenance
		t.normalizeReadonlyPermission(readonlyModeOf(s.settings(ctx)))
		t.TodoID, t.Error, t.ErrorCode, t.ErrorArgs, t.RetryWith = "", "", "", nil, nil
		t.HandoffID, t.HandoffOperationID, t.handoffLauncherSwitch = "", "", false
		t.WorkspaceResolved, t.ReservedTodoID, t.WorkspaceInferred = nil, "", false
		t.OwnerStarted = false // the hub's: only the owner's Start on the card sets it (startPlan)
		t.MemoryTopic = nil    // the hub's: the plan's own confirmation decision sets it (below)
		t.RequestGate, t.Queue = "", nil
		t.CloseTabOnAccept, t.TerminalTarget, t.TerminalCleanup = false, taskTerminalTarget{}, ""
		t.TerminalAuthorized = false
		// The hub's too: the scheduled round this plan comes from is the hub's to tell (scheduledRoundOf, #8045).
		t.Schedule, t.ScheduleTitle, t.ScheduleBasis = "", "", ""
		// Reroute is hub-owned: drop any reroute a model passes in task_plan args, so healthyPlace seeds it only from a
		// real health detour and its first-detour guard keeps the true original machine (#2271).
		t.Reroute, t.Placement = nil, nil
		t.Dispatch = nil // the hub's: written by the start that resolves the tier
		t.Title, t.Prompt, t.Cwd = strings.TrimSpace(t.Title), strings.TrimSpace(t.Prompt), strings.TrimSpace(t.Cwd)
		t.Machine = strings.TrimSpace(t.Machine)
		t.Model = strings.TrimSpace(t.Model)
		t.Speed = strings.TrimSpace(t.Speed)
		t.Permission = strings.TrimSpace(t.Permission)
		if err := s.normalizePlacementConstraints(ctx, t); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		if t.Host = strings.TrimSpace(t.Host); checkStartHostName(t.Host) != nil {
			return "", fmt.Errorf("task %d: %w", i+1, checkStartHostName(t.Host))
		}
		if t.OwnerStep = strings.TrimSpace(t.OwnerStep); !validOwnerStep(t.OwnerStep) {
			return "", fmt.Errorf("task %d: owner_step must be empty, %q, %q or %q", i+1, ownerStepMoney, ownerStepOutward, ownerStepIrreversible)
		}
		aud, err := todoAudience(t.Audience)
		if err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		t.Audience = aud
		t.DirectionID = strings.TrimSpace(t.DirectionID)
		if s.settings(ctx).todosV2On() {
			if len(t.BlockedByPR) > 0 {
				normPRs, err := parseBlockedByPRURLs(t.BlockedByPR)
				if err != nil {
					return "", fmt.Errorf("task %d: %w", i+1, err)
				}
				t.BlockedByPR = normPRs
			}
			t.UnlockStage, t.UnlockContracts, err = parseUnlockStageRule(t.UnlockStage, t.UnlockContracts)
			if err != nil {
				return "", fmt.Errorf("task %d: %w", i+1, err)
			}
		} else {
			t.BlockedByPR = nil
			t.UnlockStage, t.UnlockContracts = "", nil
		}
		if err := t.checkRole(scope != nil); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		if t.Machine != "" {
			if m, ok := s.machineByName(ctx, t.Machine); ok && t.Role == roleLeader && s.isWorkMachine(m.ID) {
				return "", fmt.Errorf("task %d: role \"leader\" cannot run on %s: a work-trust computer gives its agents only the "+
					"reporting tools, so it could not supervise workers. Put the task leader on a computer you own", i+1, m.Name)
			}
		}
		if err := t.normalizeMachinePolicy(); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		if t.Machine == "" && (t.backendWorkspace() || t.containerWorkspace()) {
			return "", fmt.Errorf("task %d: workspace.backend and workspace.container require an explicit machine", i+1)
		}
		if t.ExpectMinutes < 0 {
			return "", fmt.Errorf("task %d: expect_minutes must be zero or a positive number of minutes", i+1)
		}
		t.DoneWhen, t.ExpectMinutes = cleanDoneWhen(t.DoneWhen), min(t.ExpectMinutes, 24*60)
		if t.Title == "" || t.Prompt == "" || t.Cwd == "" && !t.backendWorkspace() && !s.canInferContainerWorkspace(ctx, *t) && !t.scratchWorkspace() {
			return "", fmt.Errorf("task %d: title, prompt and cwd are required", i+1)
		}
		if t.scratchWorkspace() && t.Cwd != "" {
			if identity.IsAssistantDir(t.Cwd) {
				return "", fmt.Errorf("task %d: %w", i+1, i18n.Err("rpc-errors.agent_assistant_workspace"))
			}
			if t.Machine != "" {
				if m, ok := s.machineByName(ctx, t.Machine); ok && assistantWorkspace(m, proto.ExpandHome(t.Cwd, m.Home)) {
					return "", fmt.Errorf("task %d: %w", i+1, i18n.Err("rpc-errors.agent_assistant_workspace"))
				}
			}
			return "", fmt.Errorf("task %d: workspace %q cannot be combined with cwd; omit cwd to use a managed scratch directory", i+1, workspaceScratch)
		}
		t.Brief = nil // the hub's (planBriefs below), never the tool call's
		t.Goal, t.OutOfBounds = clip(oneLine(t.Goal), 300), cleanDoneWhen(t.OutOfBounds)
		t.Continues, t.ContinuesWhy = strings.TrimSpace(t.Continues), clip(oneLine(t.ContinuesWhy), 300)
		t.StartedWhy = clip(oneLine(t.StartedWhy), 300)
		t.EstimateSource, t.EstimateEvidence = "", nil
		t.EstimateCalibration = true // hub-owned provenance of this tool's dispatch
		if t.Continues == "" || t.ExpectMinutes > 0 {
			t.ExpectMinutes, t.EstimateSource = automaticEstimate(t.ExpectMinutes, "")
		}
		if err := briefRequired(t); err != nil && !(s.briefsOptional && t.Continues == "") {
			return "", fmt.Errorf("task %d (%s): %w", i+1, t.Title, err)
		}
		if scope == nil {
			if err := s.takeOver(ctx, sessionID, t); err != nil {
				return "", fmt.Errorf("task %d: %v", i+1, err)
			}
		}
		t.InboxOptOut = false
		t.InboxHandoff = ""
		t.InboxMatch, t.InboxMatchText, t.InboxNotice, t.InboxCandidates = "", "", "", nil
		if err := s.planInboxSource(ctx, machine, sessionID, t); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		if t.Model != "" && !proto.ValidModelID(t.Model) { // it goes to the agent as --model (#1644)
			return "", fmt.Errorf("task %d: model %q is not a model id: %s", i+1, t.Model, proto.ModelIDRule)
		}
		if err := t.ModelAliases.Validate(); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		if scope == nil { // a task leader has none to give and keeps its task's project
			atts, err := cleanAttachments(t.Attachments)
			if err == nil {
				atts, err = s.checkAttachments(ctx, machine, sessionID, atts)
			}
			if err != nil {
				return "", fmt.Errorf("task %d: %w", i+1, err)
			}
			t.Attachments, t.Mentioned, t.AttachFrom = atts, s.mentionedAttachments(ctx, machine, sessionID, t.Prompt+"\n\n"+t.Context, atts), ""
			if t.Project, err = s.todoProject(ctx, t.Project); err != nil {
				return "", fmt.Errorf("task %d: %w", i+1, err)
			}
		}
		if len(t.Attachments)+len(t.Mentioned) > 0 {
			t.AttachFrom = machine
		}
		if t.Machine == "" {
			if s.autoTaskUsesAssistantWorkspace(ctx, t) {
				return "", fmt.Errorf("task %d: %w", i+1, i18n.Err("rpc-errors.agent_assistant_workspace"))
			}
			if _, err := validateStartSpeed(map[string]any{"speed": t.Speed}); err != nil {
				return "", fmt.Errorf("task %d: %w", i+1, err)
			}
			preview := *t
			if err := s.applyTaskTier(ctx, &preview); err != nil {
				return "", fmt.Errorf("task %d: %w", i+1, err)
			}
			if t.Continues != "" {
				if prevTodo, ok := s.todoNow(t.Continues); ok {
					if err := s.checkContinuationBlockers(ctx, prevTodo, t.Permission, t.Readonly, false); err != nil {
						return "", fmt.Errorf("task %d: %w", i+1, err)
					}
				}
			}
			if t.ReservedTodoID == "" {
				t.ReservedTodoID = firstNonEmpty(t.ReuseTodo, t.TodoID, "todo_"+randomHex(8))
			}
			continue
		}
		if !s.placeTask(ctx, t) {
			return "", fmt.Errorf("task %d: unknown machine %q (machines: %s)", i+1, t.Machine, s.machineIDs(ctx))
		}
		if m, ok := s.machineByName(ctx, t.Machine); ok && assistantWorkspace(m, t.Cwd) {
			return "", fmt.Errorf("task %d: %w", i+1, i18n.Err("rpc-errors.agent_assistant_workspace"))
		}
		// Validate the tier now without pinning its model: it is resolved again at dispatch.
		preview := *t
		if err := s.applyTaskTier(ctx, &preview); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		if s.previewHealthPlacement(ctx, &preview) {
			t.Placement = preview.Placement
		}
		t.Machine, t.Cwd, t.Why = preview.Machine, preview.Cwd, preview.Why
		t.Reroute = preview.Reroute
		if m, ok := s.machineByName(ctx, t.Machine); ok && assistantWorkspace(m, t.Cwd) {
			return "", fmt.Errorf("task %d: %w", i+1, i18n.Err("rpc-errors.agent_assistant_workspace"))
		}
		// What it runs on the computer it lands on (#2640): a default that computer lacks is swapped now, so the card
		// names the launcher that will run and its why says so; a launcher this plan names that it lacks is refused.
		takesModes := true
		own := t.launchPick()
		resolvedLauncher := cmp.Or(t.Launcher, preview.Launcher)
		var fit startAgent
		var configuredMode string
		var fitParams map[string]any
		var fitMachine Device
		var fitOK bool
		if m, ok := s.machineByName(ctx, t.Machine); ok {
			var err error
			fit, fitParams, fitOK, err = s.resolveFitPlanTask(ctx, t)
			if err != nil {
				return "", fmt.Errorf("task %d: %w", i+1, err)
			}
			configuredMode, _ = fitParams["permission_mode"].(string)
			if fitOK {
				if err := checkStartSpeed(fitParams, fit, ""); err != nil {
					return "", fmt.Errorf("task %d: %w", i+1, err)
				}
				fitMachine = m
			}
			if lp, _ := fitParams["launcher"].(string); lp != "" {
				resolvedLauncher = lp
			}
		}
		// Evaluate continuation blockers and provider-switch handoff guards after healthyPlace and fitPlanTask have
		// resolved the actual landing machine and launcher (#12980).
		if t.Continues != "" {
			if prevTodo, ok := s.todoNow(t.Continues); ok {
				targetAgent := ""
				if fitOK {
					targetAgent = fit.agent
				}
				sw := s.continuationSwitchKind(ctx, prevTodo, t.Machine, resolvedLauncher, targetAgent)
				providerSwitch := sw.any()
				if providerSwitch {
					if pred, ok := s.todoPredecessor(ctx, prevTodo); ok {
						srcPerm := cmp.Or(pred.permission(), "default")
						if asked == "" && (!t.Readonly || srcPerm == "plan") {
							t.Permission = srcPerm
							cp := t.Permission
							t.CallerPermission = &cp
						}
					}
					if t.Permission == "plan" {
						t.Readonly = true
					}
					if t.Permission != "" || t.Readonly {
						t.preservePermission = true
					}
				}
				if err := s.checkContinuationBlockers(ctx, prevTodo, t.Permission, t.Readonly, providerSwitch); err != nil {
					return "", fmt.Errorf("task %d: %w", i+1, err)
				}
				if t.Readonly && fitOK {
					if err := s.readonlyLauncherRefusal(ctx, fitMachine, fitParams, fit); err != nil {
						return "", fmt.Errorf("task %d: %w", i+1, err)
					}
				}
				if providerSwitch {
					t.HandoffOperationID = "op_" + randomHex(12)
					t.handoffLauncherSwitch = sw.launcherChanged
				}
			}
		}
		if fitOK {
			if !t.preservePermission {
				switch {
				case t.Readonly:
					if fit.allowAll() {
						t.Permission = "bypassPermissions"
					}
				case configuredMode != "" && fit.supportsPermissionMode(configuredMode):
					if t.Permission == "" {
						t.Permission = configuredMode
					}
				case fit.allowAllOver(t.Permission):
					t.Permission = "bypassPermissions"
				}
			}
			// Check the caller's choices before saving a plan for a launcher with limited start options.
			if takesModes = startTakesModes(fit.agent); !takesModes {
				if err := startModesRefused(cmp.Or(t.Launcher, resolvedLauncher), fit.agent, asked, own.effort); err != nil {
					return "", fmt.Errorf("task %d: %w", i+1, err)
				}
				if asked != "" { // a work agent's, not refused: the card shows none, as none will run
					t.Permission = ""
				}
			}
			if !startTakesEffort(fit.agent) {
				t.Effort = ""
			}
		}
		// Unset: settled on the computer it lands on, so the card shows what will run (tasks_permission.go). An agent
		// that takes no mode gets none: a read-only task keeps its plan, which only says it is read-only.
		if t.Permission == "" && takesModes {
			t.Permission = s.taskPermission(ctx, t.Machine, modes)
		}
		if err := s.resolveTaskWorkspace(ctx, t); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		s.resolveTaskRepo(ctx, t) // the kind planStartsAtOnce looks up
	}
	for i, t := range a.Tasks {
		if t.ReuseTodo == "" {
			continue
		}
		if reused[t.ReuseTodo] {
			return "", fmt.Errorf("task %d: todo_id %s is already the todo of another task in this plan", i+1, t.ReuseTodo)
		}
		reused[t.ReuseTodo] = true
		if t.Continues == "" {
			if _, err := s.reusableTodo(ctx, sessionID, t.ReuseTodo, false); err != nil {
				return "", fmt.Errorf("task %d: %v", i+1, err)
			}
		}
	}
	if scope == nil {
		for i := range a.Tasks {
			if err := s.planPieces(&a.Tasks[i]); err != nil {
				return "", fmt.Errorf("task %d: %w", i+1, err)
			}
			a.Tasks[i].RequestGate = s.planRequestGate(a.Tasks[i])
		}
	}
	if err := s.planBriefs(ctx, machine, sessionID, a.Tasks, contracts...); err != nil {
		return "", err
	}
	for i := range a.Tasks {
		if err := s.matchPlannedInbox(ctx, machine, sessionID, &a.Tasks[i]); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
	}
	// A round the hub started for a scheduled task (schedule_grant.go, #8045): its plan names the schedule, so the card
	// can offer to let it start these by itself, and that schedule's grant may start them now. Never for a plan whose
	// words are confirmed before they leave (policy below), which always waits for Start.
	if scope == nil && slices.ContainsFunc(a.Tasks, func(t PlanTask) bool { return t.UserRequested }) && s.inScheduledTurn(ctx, machine, sessionID) {
		// A round the hub started has no message of the owner's: "the user asked for this" is only the model's word.
		for i := range a.Tasks {
			a.Tasks[i].UserRequested = false
		}
	}
	// A direction's lead that a hand-over of the owner's quoted words went into (#11699): the words may have reached the
	// wrong lead, so until the owner writes there, "the user asked for this" is no Start for writable work it plans
	// outside the kinds the owner already let this lead start (heldOutsideOwnKinds, #12953). Since decision rights a
	// lead's own plan starts at once whatever it says (orchestrates below), and a scheduled round has dropped user_requested
	// above: kept, it no longer holds a plan back.
	if scope == nil && slices.ContainsFunc(a.Tasks, func(t PlanTask) bool { return t.UserRequested }) && s.handedOver(sessionID) {
		s.heldOutsideOwnKinds(ctx, machine, sessionID, a.Tasks)
	}
	// Supervisors orchestrate on the owner's behalf (decision rights, 2026-11-23): a plan from the main conversation or
	// a direction lead is the supervisor's own release, so it starts at once, read-only or writable, whether or not the
	// owner asked for it in so many words (user_requested only records where the work came from). This holds in
	// scheduled follow-up and check-in rounds in that supervisor's own conversation too (user_requested is still cleared
	// above). It waits for the owner's Start only when a task needs them: a request gate, a track that confirms every
	// hand-off (health), or a task that spends money, speaks outward as the owner or takes an irreversible step they
	// have not agreed to (owner_step). Also in a lead a hand-over of the owner's quoted words went into (#11699): words
	// handed to the wrong lead start work in the wrong place, which is reversible and its supervisor's to set right, none
	// of the six. Ordinary conversations and standalone scheduled tasks are not a supervisor's release: as before.
	orchestrates := scope == nil && (s.isLeader(ctx, sessionID) || (s.isMainOn(ctx, machine, sessionID) && !s.mainPlansWait))
	// A plan from a conversation hands its prompts to agents outside it (scene 3): what it says, titles first (they are
	// what its push shows), decides once whether it waits for "Start" and what its push says (health.go). A leader's
	// workers (scope) need no decision.
	var policy TrackPolicy
	var space string
	if scope == nil {
		var out strings.Builder
		for _, t := range a.Tasks {
			out.WriteString(t.Title + "\n")
		}
		for _, t := range a.Tasks {
			out.WriteString(t.Prompt + "\n" + t.Context + "\n\n")
		}
		space, _ = s.selfSpace(ctx, sessionID)
		u := s.exitUse(ctx, out.String(), sessionID, space)
		policy = policyOf(u)
		if orchestrates && !s.trackPolicy(ctx, space).ConfirmOutbound {
			// A direction lead hands work to Agents on the owner's own computers as the owner's
			// delegate: what the words say is not asked about again (the owner confirmed the work when it reached them).
			// A track whose own rules confirm every hand-off (health) still asks; no sensitive memory rides along untapped.
			policy.ConfirmOutbound = false
		}
		// The categories that made the plan wait for "Start" are the sensitive memory entries its starts may carry once
		// the owner tapped it (memory_handoff.go): the text the owner confirms is the one tagged, never another.
		if policy.ConfirmOutbound && u.Known {
			for i := range a.Tasks {
				a.Tasks[i].MemoryTopic = slices.Clone(u.Categories)
			}
		}
	}
	var round Task
	var inRound bool
	if scope == nil && !policy.ConfirmOutbound {
		if round, inRound = s.scheduledRoundOf(ctx, machine, sessionID); inRound && round.grantEligible() && s.claudeConversation(ctx, machine, sessionID) {
			for i := range a.Tasks { // the card offers to let this schedule start these by itself
				a.Tasks[i].Schedule, a.Tasks[i].ScheduleTitle, a.Tasks[i].ScheduleBasis = round.ID, round.labelIn(s.lang(ctx)), round.runGrantBasis
			}
		}
	}
	if wait > 0 && len(a.Tasks) == 1 && scope == nil {
		a.Tasks[0].WaitInputID = "wait_" + randomHex(16)
	}
	if s.settings(ctx).todosV2On() {
		for i := range a.Tasks {
			if a.Tasks[i].ReuseTodo == "" {
				s.applyTodosV2DispatchDefaults(ctx, sessionID, cmp.Or(a.Tasks[i].Supervisor, sessionID), &a.Tasks[i], nil)
			}
		}
	}
	var reuseSpace *string
	if scope == nil {
		callerSpace := s.agentCallerSpace(ctx, machine, sessionID)
		reuseSpace = &callerSpace
	}
	dups := s.planDuplicates(ctx, a.Tasks, reuseSpace) // before the plan's own todos exist
	// Same-directory active agents the conversation already supervises: surfaced with exact ids even when the titles
	// are worded too differently for planDuplicates (#13192). A hint, never a refusal.
	reuse := reuseHintText("task_plan", s.planReuseHints(ctx, sessionID, a.Tasks))
	conflictHints := s.planWriteScopeConflicts(ctx, a.Tasks)
	now := time.Now().UTC()
	p := &TaskPlan{ID: "plan_" + randomHex(6), Conversation: sessionID, Tasks: a.Tasks, State: planOpen,
		CreatedAt: now, UpdatedAt: now}
	if err := s.savePlan(ctx, p); err != nil {
		return "", err
	}
	// The plan goes in the item's body too: a client that missed the task_plan event (or raced it) still draws the
	// card from the conversation itself.
	if scope == nil {
		if _, err := s.appendFeed(ctx, sessionID, feedPlan, p.ID, map[string]any{"plan": s.planWithSpace(ctx, p)}); err != nil {
			s.logf("task plan %s: feed: %v", p.ID, err)
		}
	}
	// A plan whose words are confirmed before they leave waits for the user's "Start" on its card, whatever would start
	// it at once; one whose words are redacted names no task in its push (policy above).
	if scope != nil { // no card: a leader's workers start at once, as part of the task the user let start
		started, err := s.startPlan(ctx, p.ID, nil, false)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Queued %d worker(s) beneath your task %s for dispatch:\n", len(started.Tasks), scope.task.ID)
		for _, t := range started.Tasks {
			if t.Error != "" {
				fmt.Fprintf(&b, "- %s on %s: dispatch failed: %s\n", t.Title, t.Machine, t.Error)
			} else if t.Queue != nil && t.Queue.Key == workspaceQueueKey {
				fmt.Fprintf(&b, "- %s → todo %s on %s (queued: %s)\n", t.Title, t.TodoID, t.Machine, t.Queue.Reason)
			} else if t.Queue != nil && t.Queue.Key == pendingPlacementQueueKey {
				fmt.Fprintf(&b, "- %s → todo %s (pending placement)\n", t.Title, t.TodoID)
			} else {
				fmt.Fprintf(&b, "- %s → todo %s on %s (dispatching)\n", t.Title, t.TodoID, t.Machine)
			}
		}
		b.WriteString("This is not confirmation that any agent started: you are told when one fails to start. A worker that has " +
			"report_to_supervisor reports to you in a message, and they cannot hand work on.")
		b.WriteString(dups)
		b.WriteString(s.inboxPlanNotes(ctx, started.Tasks))
		b.WriteString(conflictHints)
		b.WriteString(taskPlanEstimateNotes(started.Tasks))
		return b.String(), nil
	}
	var granted func() func(PlanTask) bool
	if inRound {
		granted = func() func(PlanTask) bool { return s.scheduleGrantCover(ctx, round, machine, sessionID) }
	}
	// Launcher/provider choice is part of an authorized supervisor's dispatch authority. Continuations
	// have already passed the safety, approval and non-escalation checks above; startPlan rechecks them.
	// Private hand-off policy, request prerequisites, implicit health detours and real owner steps still
	// hold the plan. Ordinary conversations still need their existing auto-start authorization.
	step := planOwnerStep(p.Tasks)
	needsOwner := policy.ConfirmOutbound || step != "" ||
		slices.ContainsFunc(p.Tasks, func(task PlanTask) bool {
			return task.RequestGate != "" || task.continuationNeedsOwner(orchestrates)
		})
	var autoStartReasons []string
	if !needsOwner && !orchestrates {
		autoStartReasons = s.planAutoStartReasons(ctx, p.Tasks, granted)
	}
	if needsOwner || (!orchestrates && autoStartReasons == nil) {
		s.event(ctx, map[string]any{"type": "task_plan", "plan": s.planWithSpace(ctx, s.planWithSuggestion(ctx, p, s.lang(ctx)))})
		var b strings.Builder
		fmt.Fprintf(&b, "Plan %s needs the user's Start on its card; nothing has started through this call.\n", p.ID)
		for _, t := range p.Tasks {
			fmt.Fprintf(&b, "- %s: waiting for the user to tap Start\n", t.Title)
		}
		b.WriteString("After the user taps Start, check the task cards or context snapshot for the outcome.")
		b.WriteString(reuse)
		b.WriteString(dups)
		b.WriteString(s.inboxPlanNotes(ctx, p.Tasks))
		b.WriteString(conflictHints)
		b.WriteString(taskPlanEstimateNotes(p.Tasks))
		if _, talking := s.ownersPresent(time.Now())[sessionID]; talking {
			// The owner is talking in this very conversation: the card is in front of them, a push would only repeat it.
			return b.String(), nil
		}
		// A blocked plan stays in the conversation/feed. Missing dispatch authority, prerequisites or a
		// private-boundary confirmation is not itself one of the owner's six decision categories. Do not
		// turn that internal wait into a category-zero phone notification or an owner decision report.
		if step == "" {
			return b.String(), nil
		}
		// A real owner step below the answer is easy to miss: push its typed card as well.
		titles := make([]string, 0, len(p.Tasks))
		for _, t := range p.Tasks {
			titles = append(titles, t.Title)
		}
		l := s.lang(ctx)
		title := i18n.T(l, "assistant-tasks.plan_waiting", i18n.Args{"n": len(p.Tasks)})
		if len(titles) == 1 {
			title = i18n.T(l, "assistant-tasks.plan_waiting_single")
		}
		here := s.settings(ctx).MachineID
		machineName := "Persona"
		if m, ok := s.device(ctx, here); ok {
			machineName = m.Name
		}
		body := strings.Join(titles, i18n.T(l, "assistant-tasks.list_sep"))
		if policy.Redact {
			title, body = s.redactedNotice(ctx, l, space)
		}
		s.push(withCard(ctx, planCardSource(step)), Notification{CardID: p.ID, Subtitle: machineName, DeviceID: here, SessionID: sessionID, Title: title, Body: body})
		if lead != "" || s.isLeader(ctx, sessionID) {
			s.lreports.plansDue.Store(true)
			s.directionsSoon() // a leader's plan waiting for Start is a decision for the owner (leader_reports.go)
		}
		return b.String(), nil
	}
	started, err := s.startPlan(ctx, p.ID, nil, false)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	supervisor := ""
	fmt.Fprintf(&b, "Queued %d task(s) for dispatch without asking (plan %s):\n", len(started.Tasks), started.ID)
	for i, t := range started.Tasks {
		reason := "delegated by the direction lead"
		if orchestrates && !s.isLeader(ctx, sessionID) {
			reason = "released by you as their supervisor"
		} else if !orchestrates {
			reason = autoStartReasons[i]
		}
		if t.Error != "" {
			fmt.Fprintf(&b, "- %s: automatic dispatch failed (%s) on %s: %s\n", t.Title, reason, t.Machine, t.Error)
		} else {
			if t.Queue != nil && t.Queue.Key == workspaceQueueKey {
				fmt.Fprintf(&b, "- %s: automatic start queued (%s) → todo %s on %s (queued: %s)\n", t.Title, reason, t.TodoID, t.Machine, t.Queue.Reason)
			} else if t.Queue != nil && t.Queue.Key == pendingPlacementQueueKey {
				fmt.Fprintf(&b, "- %s: automatic start queued (%s) → todo %s (pending placement)\n", t.Title, reason, t.TodoID)
			} else {
				fmt.Fprintf(&b, "- %s: automatic start initiated (%s) → todo %s on %s (dispatching)\n", t.Title, reason, t.TodoID, t.Machine)
			}
			if todo, ok := s.todoNow(t.TodoID); ok && todo.Supervision != nil {
				supervisor = todo.Supervision.Supervisor
			}
		}
	}
	b.WriteString("This is not confirmation that any agent started. Startup can still fail; the task cards and context snapshot show the outcome.")
	switch {
	case supervisor == "":
	case supervisor == sessionID:
		b.WriteString(" You supervise them: a supervision message comes when one needs you (review it with task_review).")
	case supervisor == s.settings(ctx).Main:
		b.WriteString(" The main conversation supervises them (its supervision messages and task_review); this conversation " +
			"gets a one-line notice with the task's link when each one ends.")
	default:
		fmt.Fprintf(&b, " Leader conversation %s supervises them: their supervision messages go there, and it reviews them with task_review.", supervisor)
	}
	b.WriteString(reuse)
	b.WriteString(dups)
	b.WriteString(s.inboxPlanNotes(ctx, started.Tasks))
	b.WriteString(conflictHints)
	b.WriteString(taskPlanEstimateNotes(started.Tasks))
	if wait > 0 && len(started.Tasks) == 1 && scope == nil && started.Tasks[0].Error == "" {
		t := started.Tasks[0]
		return s.waitAgentReply(ctx, b.String(), t.WaitInputID, wait, func(ctx context.Context) (string, string, error) {
			todo, ok, err := s.todo(ctx, t.TodoID)
			if !ok || err != nil {
				return "", "", nil
			}
			if todo.StartError != "" {
				return "", "", errors.New(todo.StartError)
			}
			if len(todo.Agents) > 0 {
				a := todo.Agents[len(todo.Agents)-1]
				return a.MachineID, a.SessionID, nil
			}
			return "", "", nil
		}), nil
	}
	return b.String(), nil
}

// planSupervisor validates an optional initial leader assignment. A lead may name itself; main may choose
// a current lead explicitly. Omission uses the planning lead, or main for an ordinary conversation.
func (s *Service) planSupervisor(ctx context.Context, self, lead string) (string, error) {
	lead = strings.TrimSpace(lead)
	switch {
	case lead == "" || lead == self:
		return "", nil
	case !s.isMain(ctx, self):
		return "", errors.New("supervisor: only the main conversation may name another leader; omit it to keep this direction lead as supervisor")
	}
	if err := s.validateSupervisorLead(ctx, lead); err != nil {
		return "", fmt.Errorf("supervisor: %w", err)
	}
	return lead, nil
}

// applyPlanEdit takes what the user changed on the card; an empty field keeps the assistant's.
func applyPlanEdit(t *PlanTask, e PlanTask) {
	if e.ModelAliases != nil {
		t.ModelAliases = e.ModelAliases
	}
	if e.Workspace != nil {
		t.Workspace, t.WorkspaceResolved, t.WorkspaceInferred = e.Workspace, nil, false
		if e.Workspace.Mode == workspaceScratch && strings.TrimSpace(e.Cwd) == "" {
			t.Cwd = ""
		}
	}
	if s := strings.TrimSpace(e.Title); s != "" {
		t.Title = s
	}
	if s := strings.TrimSpace(e.Prompt); s != "" {
		t.Prompt = s
	}
	if s := strings.TrimSpace(e.Machine); s != "" {
		if s != t.Machine {
			t.MachinePolicy = machineFixed
			// The user's explicit retarget supersedes a plan-time health detour: drop the stale reroute so the receipt
			// names the machine the task will actually run on, not the old landing (#2271).
			t.Reroute, t.Placement = nil, nil
			if t.scratchWorkspace() && (t.WorkspaceResolved == nil || (!t.WorkspaceResolved.Created && !t.WorkspaceResolved.Reused)) {
				t.WorkspaceResolved = nil
			}
		}
		t.Machine = s
	}
	if e.MachinePolicy != "" {
		t.MachinePolicy = e.MachinePolicy
	}
	if s := strings.TrimSpace(e.Cwd); s != "" {
		t.Cwd = s
	}
	if s := strings.TrimSpace(e.Launcher); s != "" {
		t.Launcher = s
	}
	if s := strings.TrimSpace(e.Model); s != "" {
		t.Model = s
	}
	if s := strings.TrimSpace(e.Tier); s != "" {
		t.Tier = s
	}
	if s := strings.TrimSpace(e.Effort); s != "" {
		t.Effort = s
	}
	if s := strings.TrimSpace(e.Speed); s != "" {
		t.Speed = s
	}
	if s := strings.TrimSpace(e.Permission); s != "" && s != strings.TrimSpace(t.Permission) {
		t.Permission = s
		t.CallerPermission = &s
	}
	if e.Ultracode != nil {
		t.Ultracode = e.Ultracode
	}
}

func planErr(w http.ResponseWriter, err error) {
	var invalid planInvalid
	switch {
	case errors.As(err, &invalid):
		apierr.Write(w, http.StatusBadRequest, invalid.error)
	case errors.Is(err, errPlanNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.Is(err, errPlanNotOpen):
		apierr.Write(w, http.StatusConflict, err)
	default:
		apierr.Write(w, http.StatusServiceUnavailable, err)
	}
}

// checkRole requires an explicit role for every newly planned task, before anything is planned. Any conversation may
// hand out a leader: its own reports reach the conversation that planned it. A task leader may plan only workers.
// Stored plans do not pass through here, so their legacy empty role still means an ordinary worker.
func (t *PlanTask) checkRole(byLeader bool) error {
	if t.Role == "" {
		if byLeader {
			return errors.New(`role is required: "worker"`)
		}
		return errors.New(`role is required: "worker" or "leader"`)
	}
	if t.Role != "worker" && t.Role != roleLeader {
		return fmt.Errorf("role %q is not one: \"worker\" or \"leader\"", t.Role)
	}
	if byLeader && t.Role != "worker" {
		return errors.New(`a task leader plans workers only: role must be "worker"`)
	}
	return nil
}

// planPushOver: a plan card is decided anywhere (Start, Don't start, a withdrawal). Its push carried the plan's id as
// its card (Notification.CardID), so the card is over everywhere at once (resolveCard).
func (s *Service) planPushOver(id string) { s.resolveCard(id) }
