package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/agentprompt"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/taskworkspace"
)

const (
	machineFixed    = "fixed"
	machineFlexible = "flexible"
)

// Missing policies on older saved plans are fixed too: inferred intent cannot
// authorize moving work (and its local directory) to a different computer.
func (t *PlanTask) normalizeMachinePolicy() error {
	if t.backendWorkspace() {
		t.MachinePolicy = machineFixed
	}
	if t.MachinePolicy == "" {
		t.MachinePolicy = machineFixed
	}
	if t.MachinePolicy != machineFixed && t.MachinePolicy != machineFlexible {
		return fmt.Errorf("unknown machine_policy %q; use fixed or flexible", t.MachinePolicy)
	}
	return nil
}

// placeTask names t's machine by its id and makes ~ in its directory that machine's home; false: no such machine.
func (s *Service) placeTask(ctx context.Context, t *PlanTask) bool {
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return false
	}
	t.Machine = m.ID
	t.Cwd = proto.ExpandHome(t.Cwd, m.Home)
	return true
}

// healthyPlace moves t off a machine where its launcher failed its health check, to one where it works and the directory exists
// (launcher_health_check.go); its why says so. False: it stays.
func (s *Service) healthyPlace(ctx context.Context, t *PlanTask) bool {
	if t.MachinePolicy != machineFlexible {
		return false
	}
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return false
	}
	alt, note, moved := s.healthDetour(ctx, m, s.startLauncher(ctx, t.Launcher), func(alt Device) bool {
		if t.Role == roleLeader && s.isWorkMachine(alt.ID) { // a task leader never lands on a work-trust computer
			placementOf(ctx).hit(alt.ID, "leader_work_machine", nil)
			return false
		}
		if t.scratchWorkspace() {
			if !alt.ScratchWorkspace {
				placementOf(ctx).hit(alt.ID, "scratch_unsupported", nil)
				return false
			}
			return true
		}
		return s.taskDirectoryExists(ctx, alt.ID, rehome(t.Cwd, m, alt))
	})
	t.noteWhy(note)
	if moved {
		// Keep the machine the task was first placed on across repeated moves; each move refreshes the landing and the
		// health-check reason. Names, not ids, go on the card/receipt (#2271).
		if t.Reroute == nil {
			t.Reroute = &PlanReroute{FromMachine: firstNonEmpty(m.Name, m.ID)}
		}
		reroute := *t.Reroute
		t.Reroute = &reroute
		t.Reroute.ToMachine = firstNonEmpty(alt.Name, alt.ID)
		t.Reroute.Reason = note
		if t.scratchWorkspace() {
			t.Machine, t.Cwd, t.WorkspaceResolved = alt.ID, "", nil
		} else {
			t.Machine, t.Cwd = alt.ID, rehome(t.Cwd, m, alt)
		}
	}
	return moved
}

// leaderOnWorkMachine refuses a task leader placed on a work-trust computer: its agents there get only the reporting
// tools, so the role could not work, and the privacy boundary is not widened for it.
func (s *Service) leaderOnWorkMachine(t *PlanTask, m Device) error {
	if t.Role == roleLeader && s.isWorkMachine(m.ID) {
		return fmt.Errorf("role \"leader\" cannot run on %s: a work-trust computer gives its agents only the reporting tools, so it could "+
			"not supervise workers. Put the task leader on a computer you own", m.Name)
	}
	return nil
}

// noteWhy adds note to t's why (once), in brackets after the assistant's reason: the card shows it.
func (t *PlanTask) noteWhy(note string) {
	if note != "" && !strings.Contains(t.Why, note) {
		t.Why = strings.TrimSpace(t.Why + " (" + note + ")")
	}
}

// fitTaskLauncher holds t's resolved launcher (params, resolveStart's) to what m has (fitLauncher): a default m lacks
// is swapped for one it has, which t then names, without a tier that no longer applies, and its why says so. An
// explicit tier (or a policy preset) whose launcher m lacks is refused there instead, with no swap and no start
// (#12032). It also reports what the resolved launcher runs (startAgent: its agent type, and whether it runs with
// Allow all), using the same launcher list rather than another probe. A read-only task whose launcher has no read-only
// mode is refused here (readonlyLauncherRefusal), when it is planned and again before it starts.
func checkAliasAgent(aliases proto.ModelAliases, agent startAgent) error {
	if len(aliases) > 0 && agent.agent != "" && agent.agent != "claude" {
		return fmt.Errorf("model_aliases require a Claude Code terminal launcher")
	}
	return nil
}

func (s *Service) fitTaskLauncher(ctx context.Context, t *PlanTask, m Device, own startPick, params map[string]any) (startAgent, error) {
	note, agent, err := s.fitNewLauncher(ctx, m, own, params)
	if err != nil {
		return agent, err
	}
	if err := checkAliasAgent(t.ModelAliases, agent); err != nil {
		return agent, err
	}
	if t.Readonly {
		// The Antigravity companion runs read-only agy tasks with bypassPermissions and the first-message
		// constraint, matching Claude Code; terminal agy without the companion stays refused (start_host.go, #13464).
		s.companionStartHost(ctx, m, agent, params)
		if err := s.readonlyLauncherRefusal(ctx, m, params, agent); err != nil {
			return startAgent{}, err
		}
	}
	if note == "" {
		return agent, nil
	}
	t.Launcher, _ = params["launcher"].(string)
	t.Model, _ = params["model"].(string)
	t.Effort, _ = params["effort"].(string)
	t.Speed, _ = params["speed"].(string)
	t.Tier = ""
	t.startNamed.launcher, t.startNamed.noModel = true, t.Model == ""
	t.noteWhy(note)
	return agent, nil
}

// readonlyLauncherRefusal refuses a read-only task on m whose launcher (params', as fitLauncher left it) starts a work
// agent (proto.WorkAgentTypes: pi, the Cursor CLI, the Antigravity CLI) in a terminal, before anything reaches the node:
// readonly_unsupported, naming the launchers of m that take a read-only task (Claude Code's and Codex's), so the plan
// can be made writable or with one of them (#8316). These agents have no flag that holds them read-only, and a line in
// the prompt does not (pi's tools write files), so Persona does not start one for a task the owner marked read-only;
// the node refuses their plan-mode start too (node/control.go). No other launcher is put in its place: the plan named
// that agent. Anything else goes on as before: a launcher whose agent the computer's list does not say goes to the node
// with plan, which refuses what it cannot hold read-only (nothing is refused here on a guess), and a start an engine
// runtime runs is the runtime's to refuse (Paseo holds only its Claude Code providers read-only, caps.readonly). An
// agy start on the Antigravity companion (host agy, fitTaskLauncher) is allowed and runs with bypassPermissions and the
// first-message read-only constraint like Claude Code (#13464).
func (s *Service) readonlyLauncherRefusal(ctx context.Context, m Device, params map[string]any, agent startAgent) error {
	if !proto.IsWorkAgentType(agent.agent) {
		return nil
	}
	host, _ := params["host"].(string)
	if agentStartHost(m, host) != "" || agent.agent == "agy" && host == proto.HostAgy {
		return nil
	}
	launcher, _ := params["launcher"].(string)
	launchers := strings.Join(s.readonlyLaunchersOn(ctx, m), ", ")
	data, _ := json.Marshal(map[string]string{"launcher": launcher, "launchers": launchers, "delivered": "none"})
	return &rpc.Error{Code: "readonly_unsupported", Data: data, Message: i18n.T(s.lang(ctx), "assistant-tasks.readonly_launcher_unsupported",
		i18n.Args{"machine": firstNonEmpty(m.Name, m.ID), "launcher": launcher, "agent": kindName(agent.agent), "launchers": cmp.Or(launchers, "—")})}
}

// readonlyLaunchersOn are the launchers of m a read-only task can start on, as its picker lists them (plain claude
// when m has Claude Code, its own, hub.json's and the managed ones): those that start Claude Code or Codex. Only plain
// claude when the list can't be read and m has Claude Code; none otherwise.
func (s *Service) readonlyLaunchersOn(ctx context.Context, m Device) []string {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := s.pickerLaunchers(cctx, m.ID)
	if err != nil && m.ClaudeVersion != "" {
		return []string{"claude"}
	}
	var names []string
	for _, l := range list {
		if (l.AgentType == "claude" || l.AgentType == "codex") && l.Name != "" && !slices.Contains(names, l.Name) {
			names = append(names, l.Name)
		}
	}
	return names
}

// taskDirectoryExists checks the mapped directory before a flexible task changes machines. Missing, denied or
// unreadable paths (including a node too old to answer fs.stat) keep that machine out of the detour. Concurrent
// identical checks coalesce in-flight, and positive confirmations are reused within the same logical placement
// decision (placementOf(ctx)); separate requests always re-check live so deleted directories are rejected immediately.
func (s *Service) taskDirectoryExists(ctx context.Context, machine, dir string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	flightKey := taskDirCacheKey(machine, dir)
	cacheKey := ""
	if pl := placementOf(ctx); pl != nil && pl.ID != "" {
		cacheKey = pl.ID + "\x00" + flightKey
	}
	exists, err := s.tasks.dirMeta.dirExists.getOrFetch(
		ctx,
		s.taskMetaNow,
		flightKey,
		cacheKey,
		5*time.Second,
		func(ok bool) bool { return ok },
		nil,
		func(callCtx context.Context) (bool, error) {
			var res struct {
				Stats []struct {
					Path string `json:"path"`
					Kind string `json:"kind"`
				} `json:"stats"`
			}
			if err := s.call(callCtx, machine, "fs.stat", map[string]any{"paths": []string{dir}}, &res); err != nil {
				return false, err
			}
			return len(res.Stats) == 1 && res.Stats[0].Path == dir && res.Stats[0].Kind == "dir", nil
		},
	)
	if err != nil {
		placementOf(ctx).hit(machine, "directory_unknown", nil)
		return false
	}
	if !exists {
		placementOf(ctx).hit(machine, "directory_missing", nil)
	}
	return exists
}

// deferredTaskStart is the first input of a Companion session that answered Starting after defer_mcp. It is activated
// through the ordinary idempotent session.send path only after the task role and supervision relationship are linked.
type deferredTaskStart struct {
	text           string
	images         []proto.Image
	clientMsgID    string
	permissionMode string
}

// deferredTaskPrompt folds separate task_instructions and task_attachments into the first prompt sent by deferred
// Companion activation, matching node.startRuntime's immediate fallback order and keeping <attached_images> last.
func deferredTaskPrompt(prompt string, params map[string]any) string {
	instructions, _ := params["task_instructions"].(string)
	attachments, _ := params["task_attachments"].(string)
	return appendDeferredPromptSection(appendDeferredPromptSection(prompt, instructions), attachments)
}

func appendDeferredPromptSection(prompt, section string) string {
	if strings.TrimSpace(section) == "" {
		return prompt
	}
	if strings.TrimSpace(prompt) == "" {
		return section
	}
	if i := strings.LastIndex(prompt, "\n\n<attached_images>"); i >= 0 && strings.HasSuffix(strings.TrimSpace(prompt), "</attached_images>") {
		return strings.TrimSpace(prompt[:i]) + "\n\n" + section + prompt[i:]
	}
	return strings.TrimSpace(prompt) + "\n\n" + section
}

// startTaskAgent starts the agent for t on its machine (another when its launcher does not work there: t says which)
// and answers the machine's id and the session's: the machine it reached is the one the agent is linked to (looking it
// up again after the start could find another, or none). A failed start counts against the launcher there.
func (s *Service) startTaskAgent(ctx context.Context, t *PlanTask, retry bool) (mid, sid string, err error) {
	p := newPlacement(t.Machine, "enforce")
	if prev := t.Placement; prev != nil && (prev.TaskID == "" && t.Reroute != nil || prev.ReasonKey == placementReasonExtension || prev.ReasonKey == placementReasonFallback) {
		// Start on the landing shown on the plan card or chosen by automatic placement, retaining its audit evidence.
		if prev.ID != "" && (prev.ReasonKey == placementReasonExtension || prev.ReasonKey == placementReasonFallback) {
			p.ID = prev.ID
			if !prev.At.IsZero() {
				p.At = prev.At
			}
		}
		p.Requested, p.ReasonKey, p.Reason = prev.Requested, prev.ReasonKey, prev.Reason
		p.Candidates = prev.cloneCandidates()
	}
	p.TaskID = t.TodoID
	ctx = context.WithValue(ctx, placementKey{}, p)
	defer func() { s.finishPlanPlacement(ctx, p, t, sid, err) }()
	t.startedAgent = ""
	t.deferredStart = nil
	if err := s.checkClaimedGatewayStart(ctx, *t); err != nil {
		return "", "", err
	}
	if err := s.checkRequestStart(ctx, *t); err != nil {
		return "", "", err
	}
	if err := s.checkTodoStartReadiness(ctx, *t); err != nil {
		return "", "", err
	}
	if (t.HandoffID != "" || t.HandoffOperationID != "") && (t.Permission != "" || t.Readonly) {
		t.preservePermission = true
	}
	t.normalizeReadonlyPermission(readonlyModeOf(s.settings(ctx)))
	settledPermission := t.Permission
	if err := t.normalizeMachinePolicy(); err != nil {
		return "", "", err
	}
	// What the task itself named, kept from its first try in this process. The task as stored never carries what a
	// tier, Model configuration › Agents or a start-defaults policy filled in (storedLaunch, #12618), so a task read
	// back from the store (task_retry, a remedy, a queued start, a retry from the card, a manager restart) names only
	// its own and is resolved again against the current defaults, except that a tier keeps the candidate the task's
	// previous Agent ran on (its Dispatch, below; candidate_keep.go, #14701).
	firstTry := t.callerPick == nil
	if t.callerPick == nil {
		own := t.launchPick()
		if t.CallerPermission != nil && !t.preservePermission {
			own.permission = *t.CallerPermission // settled Permission is not the caller's choice on a read-back task
		}
		t.callerPick = &own
	}
	caller := *t.callerPick
	// Every later try starts from what the task named: what a policy filled in on an earlier try goes back first,
	// whichever rule fits now (start_defaults.go), so a rule that changed or no longer fits leaves nothing of the earlier one.
	// The first try keeps the raw input for validation.
	if !firstTry {
		if t.startFilled.any() || !t.startNamed.launcher {
			t.Launcher, t.Tier = caller.launcher, caller.tier
		}
		if t.startFilled.any() || !t.startNamed.noModel {
			t.Model = caller.model
		}
		if t.startFilled.any() || !t.startNamed.effort {
			t.Effort = caller.effort
		}
		t.Speed = caller.speed
		t.Permission, t.Ultracode = caller.permission, copyBool(caller.ultracode)
	}
	t.startFilled, t.startNamed = startFill{}, startNamed{}
	// A self-planning constraint applies only while a policy rule asks for it on this try.
	t.planFirst = false
	params := map[string]any{"model_source": planModelSource(*t), "launcher": t.Launcher, "model": t.Model, "effort": t.Effort, "speed": t.Speed, "tier": t.Tier}
	if caller.model != "" {
		params["explicit_model"] = caller.model
	}
	if len(t.ModelAliases) != 0 {
		params["model_aliases"] = t.ModelAliases
	}
	if t.Host != "" {
		params["host"] = t.Host // before the fit: an engine host's runtime runs its own launchers (#8140)
	}
	// The start-defaults policy: its preset goes in before the start is resolved, as if the task had named that tier;
	// its effort and ultracode after the launcher fit, and the goal (a rule's or the Codex default) after that.
	defaults, withDefaults := s.taskStartDefaults(ctx, t)
	presetNote := ""
	if withDefaults {
		presetNote = s.applyStartPreset(ctx, t, caller, defaults, params)
	}
	tierCtx, tierSlot := withTierStart(ctx, t.Machine)
	// Another Agent of the same task (a retry, a continuation, a queued or restarted start read back) keeps the
	// candidate its previous Agent ran on, unless this start names a launcher, a model or another tier of its own
	// (candidate_keep.go, #14701): the tier's pick keeps it, or re-picks it on a clear problem.
	if keep := s.taskKeep(ctx, t); keep != nil {
		prof, _, _ := s.activeModelProfile(ctx)
		if keepsTier(prof, params, keep.Prev) {
			if tier, _ := params["tier"].(string); strings.TrimSpace(tier) == "" {
				params["tier"] = keep.Prev.Tier
			}
			tierSlot.keep = keep
		}
	}
	own, err := s.resolveStart(tierCtx, params, t.LauncherDefaultModel)
	if err != nil {
		return "", "", err
	}
	// The candidate the tier gave this start (pickTierCandidate): on the plan card as the task's dispatch, and one
	// model_routing row once the Agent started.
	t.Dispatch, t.tierChoice = nil, tierSlot.pick
	if tierSlot.pick != nil {
		if t.startFilled.preset {
			// A start-defaults policy chose this tier for this start only: the next Agent rolls again, it keeps nothing.
			if tierSlot.pick.Args == nil {
				tierSlot.pick.Args = map[string]any{}
			}
			tierSlot.pick.Args[dispatchPresetArg] = true
		}
		t.Dispatch = tierSlot.pick.dispatch()
		if k := tierSlot.keep; k != nil && k.Prev.Launcher == t.Dispatch.Launcher && k.Prev.Model == t.Dispatch.Model {
			t.Dispatch.Failed = k.Prev.Failed // the same candidate again: its failed starts in a row go on counting
		}
		defer func() {
			if err != nil && failedStart(err) {
				t.Dispatch.Failed++
			}
			if err == nil && sid != "" {
				row := modelRoutingOf(*tierSlot.pick, mid)
				row.TodoID, row.PlanID, row.TaskIndex, row.SessionID = t.TodoID, t.planID, t.planIndex, sid
				s.recordModelRouting(ctx, row)
			}
		}()
	}
	if t.defaulted || t.Launcher == "" {
		own.launcher, t.defaulted = "", true // filled in from a default, by this try or an earlier one
	}
	t.Launcher, _ = params["launcher"].(string)
	t.Model, _ = params["model"].(string)
	t.Effort, _ = params["effort"].(string)
	t.Speed, _ = params["speed"].(string)
	m, err := s.placePlanStart(ctx, t, own, params, retry)
	defer func() {
		// A failure before the caller takes ownership of the claim releases it here; the success path keeps it on the
		// task for runPlanTaskStart to release after linking.
		if err != nil && t.admissionRelease != nil {
			t.admissionRelease()
			t.admissionRelease = nil
			t.admissionClaimMachine = ""
		}
	}()
	if err != nil {
		return "", "", err
	}
	if t.Parent != "" && params["host"] == nil && s.taskWorktreeKind(ctx, t) == proto.EngineOrca {
		params["host"] = proto.EngineOrca
	}
	// On the computer it landed on (a flexible task may have moved): a default it lacks gives way to one it has, a
	// launcher the plan named that it lacks is refused before any tab opens (#2640). Before the health check below,
	// which is then about the launcher that will run.
	fit, err := s.fitTaskLauncher(ctx, t, m, own, params)
	if err != nil {
		return "", "", err
	}
	if err := checkHostLauncher(params, fit); err != nil {
		return "", "", err
	}
	// An agy task that names no host starts on the computer's Antigravity companion where that runs its launcher
	// (start_host.go, #13447); a retry in a pane an earlier try opened, or a prepared backend workspace, stays as it was.
	companion := t.inPane == "" && !t.backendWorkspace() && s.companionStartHost(ctx, m, fit, params)
	if t.CallerPermission != nil && !t.preservePermission {
		keepSettled := false
		if retry && settledPermission != "" && *t.CallerPermission == "" && t.RetryFromAgent != "" {
			// The failed agent kind is durable evidence; launcher names can change meaning across queued reroutes.
			keepSettled = t.RetryFromAgent == fit.agent && !t.RetryFromAllowAll
		}
		if keepSettled {
			t.Permission = settledPermission
		} else {
			t.Permission = *t.CallerPermission // a different runtime settles from the caller's value, not an earlier try's mode
			t.normalizeReadonlyPermission(readonlyModeOf(s.settings(ctx)))
		}
	}
	t.Model, _ = params["model"].(string)
	if err := checkStartSpeed(params, fit, ""); err != nil {
		return "", "", err
	}
	configuredMode, _ := params["permission_mode"].(string)
	delete(params, "permission_mode")
	if !t.preservePermission {
		switch {
		case t.Readonly:
			// Allow all and the first message's constraint, as for Claude Code: agy's ACP plan mode prompts on every
			// Bash call (including read-only inspection commands), stalling autonomous tasks. Codex keeps plan.
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
	modes := startTakesModes(fit.agent)
	if !t.preservePermission && t.CallerPermission != nil && t.Permission == "" && modes {
		t.Permission = s.taskPermission(ctx, t.Machine, nil)
	}
	if !startTakesEffort(fit.agent) {
		delete(params, "effort")
		t.Effort = ""
	}
	if withDefaults { // only what the task left open, after the fit, which may itself have filled some in
		s.applyStartExtras(ctx, t, caller, defaults, fit.agent, params, presetNote)
	}
	// The goal: a rule's, else the Codex default for a task with criteria (startGoalSource); the default is not sent
	// where the node is known to refuse it, and its refusal elsewhere is only logged (below).
	defaultGoal := s.applyStartGoal(t, defaults, withDefaults, fit.agent, defaultGoalRefusedOn(m, params, fit), params)
	if t.MachinePolicy == machineFixed {
		// Only an actual terminal start can recheck a repaired terminal shell.
		// An explicit retry keeps that existing recovery path on the fixed target;
		// login-shell checks and authentication gates are not bypassed.
		// A failing record is probed once on the spot first, as a detour does: periodic checks may be off, and a
		// record a fix made stale would otherwise refuse this machine until someone rechecked by hand.
		if r, bad := s.launcherUnusableNow(ctx, m.ID, s.startLauncher(ctx, t.Launcher)); bad && !(retry && r.Kind == errKindNotInTerminal) {
			return "", "", i18n.Err("assistant-tasks.fixed_launcher_unhealthy", i18n.Args{
				"machine": m.ID, "launcher": t.Launcher, "reason": r.Error,
			})
		}
	}
	if err := s.checkRequestStart(ctx, *t); err != nil {
		return "", "", err
	}
	if err := s.admitTaskWorkspace(ctx, t); err != nil {
		return "", "", err
	}
	if t.usesPreparedWorkspace() {
		defer func() {
			if err != nil && !startResultUnknown(err) {
				s.releaseUnusedBackendStart(ctx, t)
			}
		}()
	}
	if t.rememberKind { // "start these directly": the machine and repository that run, now that admission fixed them
		t.rememberKind = false
		s.trustKinds(ctx, []PlanTask{*t})
	}
	if t.grantSchedule != "" { // "let this scheduled task start these": the same kind, the schedule's own (#8045)
		id := t.grantSchedule
		t.grantSchedule = ""
		s.grantScheduleKind(ctx, id, *t)
	}
	prompt := t.Prompt
	if t.HandoffID != "" {
		if handoffSummary := strings.TrimSpace(s.todoHandoffSummary(ctx, t.HandoffID)); handoffSummary != "" {
			prompt = agentprompt.WithContext(fit.agent, prompt, handoffSummary)
		}
	}
	if t.usesPreparedWorkspace() && len(t.WorkspaceResolved.Repositories) > 0 {
		rows, err := json.Marshal(t.WorkspaceResolved.Repositories)
		if err != nil {
			return "", "", err
		}
		prompt += "\n\n" + prompts.RenderWorkspaceRepositories(string(rows), answerLanguage(s.hello(ctx).Locale))
	}
	var rules []string
	if current, ok := s.todoNow(t.TodoID); t.MultiIssueReport || ok && current.Supervision != nil && current.Supervision.Investigation != nil {
		rules = append(rules, prompts.RenderTaskInvestigation())
	}
	if strings.TrimSpace(t.Context) != "" {
		prompt += "\n\n" + t.Context
	}
	if t.Continues != "" {
		prompt += "\n\n" + prompts.RenderTaskContinuation(prompts.TaskContinuation{TodoID: t.Continues, Readonly: t.Readonly})
	}
	if t.Readonly {
		prompt += "\n\n" + prompts.RenderReadonlyTaskConstraint(answerLanguage(s.hello(ctx).Locale))
	}
	if t.planFirst {
		prompt += "\n\n" + prompts.RenderPlanFirstTaskConstraint()
	}
	if len(t.DoneWhen) > 0 { // what the supervisor will check (supervise.go)
		prompt += "\n\n" + prompts.RenderTaskDoneWhen(t.DoneWhen)
	}
	var entry prompts.TaskHandoff // none verified: a work machine, no to-do
	if t.TodoID != "" && !s.isWorkMachine(m.ID) {
		entry = s.taskEntry(ctx, m.ID, fit)
		rules = append(rules, prompts.RenderTaskHandoff(entry))
	}
	switch {
	case t.Role == roleLeader: // what it may hand out, and how its workers reach it (task_leader.go)
		// Through the terminal entry the handoff note above names, for an Agent without the Persona MCP (#12136).
		rules = append(rules, prompts.RenderTaskLeader(prompts.TaskLeader{Terminal: entry.Terminal()}))
	case t.Parent != "":
		rules = append(rules, prompts.RenderTaskWorker())
	}
	// Files for the user are named by path, never pasted into the reply (task_files.go, #10511). Always asked for,
	// unlike the closeout form below, which depends on a switch.
	rules = append(rules, prompts.RenderTaskFiles(answerLanguage(s.hello(ctx).Locale)))
	// One structured closeout carries delivery too when both switches are on.
	delivery := !t.Readonly && s.doneNeedsDelivery(ctx)
	brief := s.closeoutBrief(ctx, m.ID, delivery)
	if delivery { // what done means: live and checked (delivery.go)
		rules = append(rules, prompts.RenderTaskDelivery(brief != ""))
	}
	if brief != "" {
		rules = append(rules, brief)
	}
	// The project background, and the decisions of the todo and its continuation ancestors (a task started again may
	// have some), follow the assignment; they remain user context, never framework policy. Direction notes are prompt
	// context, separate from the main supervision ledger (#10623, #5732). SkipProjectMemory leaves out the project and
	// direction context, including extra workspace repositories; the to-do's own decisions still follow.
	direction := s.taskDirectionOf(ctx, t)
	background := ""
	if !t.SkipProjectMemory {
		background = s.projectHandoffContextForTodo(ctx, m.ID, t.Cwd, direction, s.todoForPlanContext(t))
	}
	var repoPaths []string
	if t.usesPreparedWorkspace() {
		for _, repo := range t.WorkspaceResolved.Repositories {
			if repo.AbsolutePath == t.Cwd {
				continue
			}
			repoPaths = append(repoPaths, repo.AbsolutePath)
			if !t.SkipProjectMemory {
				if info := s.projectHandoffContext(ctx, m.ID, repo.AbsolutePath, direction); info != "" {
					background = strings.TrimSpace(background + "\n\n" + repo.AbsolutePath + "\n" + info)
				}
			}
		}
	}
	if decisions := s.todoHandoffDecisionsContext(ctx, t.TodoID); decisions != "" {
		background = strings.TrimSpace(background + "\n\n" + decisions)
	}
	// The memory entries the task needs (memory_handoff.go, scene 3): general ones its words bear on, sensitive ones
	// only after the owner confirmed a plan whose words carry their categories. The brief notes which went only once
	// this start went through: a failure below, or a fallback to another computer, carried nothing from here.
	if memory, carried := s.handOffMemoryContext(ctx, *t, m.ID); memory != "" {
		background = strings.TrimSpace(background + "\n\n" + memory)
		noteCtx, todoID := context.WithoutCancel(ctx), t.TodoID
		defer func() {
			if err == nil && sid != "" {
				s.noteHandOffMemory(noteCtx, todoID, carried)
			}
		}()
	}
	if background != "" {
		prompt += "\n\n" + background
	}
	if len(rules) > 0 {
		framework := strings.Join(rules, "\n\n")
		if m.TaskInstructions {
			params["task_instructions"] = framework
		} else {
			prompt += "\n\n" + framework // old nodes must receive every required rule
		}
	}
	prompt, err = s.withStandingAuthorizations(ctx, m.ID, t.Cwd, prompt, repoPaths...)
	if err != nil {
		return "", "", err
	}
	// The user's attachments end it, copied here when this is not the assistant's computer (#2437): last, so the
	// pictures' <attached_images> block ends the message as it does one sent straight to an agent.
	if len(t.Attachments)+len(t.Mentioned) > 0 {
		handed := s.handAttachments(ctx, t.AttachFrom, m.ID, t.Attachments, t.Mentioned)
		prompt = withAttachments(prompt, handed)
		if params["task_instructions"] != nil && handed.text() != "" {
			params["task_attachments"] = handed.text()
			prompt = strings.TrimSuffix(prompt, "\n\n"+handed.text())
		}
	}
	// A to-do's stored pictures go with every start for it, as they go with the app's manual handoffs (#11084).
	// The node decodes them on this machine; any pictures the owner attached to this turn stay in the prompt too.
	if t.TodoID != "" {
		if current, ok := s.todoNow(t.TodoID); ok && len(current.Images) > 0 {
			rows, err := s.todoImageContents(ctx, current)
			if err != nil {
				return "", "", err
			}
			params["images"] = todoProtoImages(rows)
		}
	}
	params["cwd"], params["prompt"], params["tab_name"] = t.Cwd, prompt, localTabName(t.Title)
	if t.WaitInputID != "" {
		params["client_msg_id"] = t.WaitInputID
	}
	if w := t.WorkspaceResolved; w != nil && w.WorkspaceID != "" {
		params["workspace_id"] = w.WorkspaceID
		if w.Created || w.Reused {
			params["reopen_worktree_workspace"] = true
		}
	}
	ctx, unlockTaskSpace, err := s.withTaskSpaceStart(ctx, *t, m, params)
	if err != nil {
		return "", "", err
	}
	defer unlockTaskSpace()
	if t.Model != "" {
		params["model"] = t.Model
	}
	if modes {
		mode := t.Permission
		if mode == "" { // a plan saved before task_plan settled it, or a task started another way (tasks_permission.go)
			if fit.allowAll() {
				mode = "bypassPermissions"
			} else {
				mode = s.taskPermission(ctx, m.ID, nil)
			}
		}
		params["permission_mode"] = mode
	} // else none, plan included: a read-only task's first message carries the constraint (above)
	if t.Ultracode != nil && *t.Ultracode {
		params["ultracode"] = true
	}
	if t.inPane != "" { // a retry in the tab an earlier try did not start in (start_remedy.go)
		params["in_pane"] = t.inPane
	}
	var res struct {
		Session  proto.Session            `json:"session"`
		Terminal *devicewire.TaskTerminal `json:"task_terminal,omitempty"`
		// What an engine that runs only its own launchers ran in place of what was asked (#8140).
		Instead *proto.StartInstead `json:"instead"`
	}
	picked, _ := params["launcher"].(string)
	t.TerminalTarget = taskTerminalTarget{}
	terminalToken := ""
	if t.CloseTabOnAccept && !retry && t.inPane == "" {
		terminalToken = newStartSessionID()
		params["task_terminal_token"] = terminalToken
	}
	// Remember the sessions already there, so a start whose answer never comes back can adopt only the session it
	// created (the same reconciliation agent_start gets, tools.go): a slow link can leave the node building the tab
	// after the hub's wait ends (#6540). Its own short bound keeps a slow list out of the start's wait.
	lctx, lcancel := context.WithTimeout(ctx, agentStartListTimeout)
	knownIDs := s.knownSessionIDs(lctx, m.ID)
	lcancel()
	// Name the session ourselves for a Claude start, so an unanswered start is reconciled by this exact id: two
	// workspace-none tasks share a directory and would otherwise be able to adopt each other's sessions. Agents that
	// pick their own ids keep the heuristic fallback with its open-todo exclusion (session_adopt.go).
	expectID := ""
	if s.launcherAgent(ctx, m.ID, s.startLauncher(ctx, picked)) == "claude" {
		expectID = newStartSessionID()
		params["session_id"] = expectID
	}
	clearStandardSpeed(params)
	s.setPlanThen(ctx, m.ID, params)
	deferredImages, _ := params["images"].([]proto.Image)
	deferredText := deferredTaskPrompt(prompt, params)
	companionTarget := t.inPane == "" && !t.backendWorkspace() && params["host"] == proto.HostAgy
	// A role-bearing Companion start must not attach MCP before linkPlanAgent can answer its identity-scoped tools/list.
	// The real ACP id comes back first; only a Companion that actually answers Starting is activated after the link.
	if companionTarget && (t.Role == roleLeader || t.Parent != "") && (deferredText != "" || len(deferredImages) > 0) {
		params["defer_mcp"] = true
		if id, _ := params["client_msg_id"].(string); id == "" {
			params["client_msg_id"] = newStartSessionID() // the post-link send has the same once-only identity as an immediate start
		}
	}
	noteDeferredStart := func(session proto.Session) {
		if params["defer_mcp"] != true || !session.Starting {
			return
		}
		clientMsgID, _ := params["client_msg_id"].(string)
		permMode, _ := params["permission_mode"].(string)
		t.deferredStart = &deferredTaskStart{text: deferredText, images: append([]proto.Image(nil), deferredImages...), clientMsgID: clientMsgID, permissionMode: permMode}
	}
	callNode := func() error {
		if err := s.checkContinuationScope(t); err != nil {
			return err
		}
		if err := s.checkRequestStart(ctx, *t); err != nil {
			return err
		}
		if err := s.checkClaimedGatewayStart(ctx, *t); err != nil {
			return err
		}
		if err := s.checkTodoStartReadiness(ctx, *t); err != nil {
			return err
		}
		bindCtx := context.WithValue(ctx, placementBindKey{}, p)
		if !t.usesPreparedWorkspace() {
			return s.call(bindCtx, m.ID, DeviceSessionStart, params, &res)
		}
		return s.startBackendTask(bindCtx, t, params, &res)
	}
	start := func() error { return s.startWithCodexRouteNotice(ctx, t.TodoID, fit.agent, params, callNode) }
	sentAt := time.Now()
	err = start()
	// Extras the agent it landed on cannot have (ultracode #3596, a start goal: start_defaults.go) are each dropped
	// once, in whatever order the node refuses them; both refusals come before anything starts.
	var skipped string
	var refused bool
	for range 3 {
		if reason, no := s.goalRefusal(err, params); no {
			delete(params, "goal")
			s.logf("task %s: start: %s; starting without a goal", t.TodoID, reason)
			if !defaultGoal { // a goal the owner never asked for is no news on the task's card
				t.noteWhy(i18n.T(s.lang(ctx), "assistant-tasks.start_goal_skipped", i18n.Args{"reason": reason}))
			}
			err = start()
			continue
		}
		if reason, no := s.ultracodeRefusal(ctx, err, params); no {
			skipped, refused = reason, true
			delete(params, "ultracode")
			s.logf("task %s: start: %s; starting without ultracode", t.TodoID, skipped)
			err = start()
			continue
		}
		// The companion the hub picked went away before the start reached it: nothing ran, and the start goes to the
		// computer's default host instead (a pane), never for a read-only task, which does not fall back to a pane.
		if companionAway(err, companion) && !t.Readonly {
			companion = false
			delete(params, "host")
			delete(params, "defer_mcp")
			s.logf("task %s: start: the Antigravity companion is not connected on %s; starting in its default host", t.TodoID, m.ID)
			if err := s.checkContinuationScope(t); err != nil {
				return "", "", err
			}
			if err := s.checkRequestStart(ctx, *t); err != nil {
				return "", "", err
			}
			err = start()
			continue
		}
		break
	}
	if err != nil {
		var stateErr *gatewayQueueStateError
		var blocked *todoStartBlocked
		if err == errGatewayTaskHandled || errors.As(err, &stateErr) || errors.As(err, &blocked) {
			return "", "", err
		}
		s.recordStartFailure(ctx, m.ID, s.startLauncher(ctx, picked), err)
		if !t.usesPreparedWorkspace() && startResultUnknown(err) {
			if t.startWithoutReplay() {
				if expectID != "" {
					lookup, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentStartListTimeout)
					session, found := s.adoptStartedSession(lookup, m, s.startLauncher(ctx, picked), t.Cwd, expectID, nil)
					cancel()
					if found {
						t.startedAgent = cmp.Or(session.Agent, fit.snapshot)
						noteDeferredStart(session)
						s.nameAgentTabLater(agentTabNameStart(m.ID, session, params["tab_name"].(string), t.Title))
						return m.ID, session.SessionID, nil
					}
				}
				return "", "", err
			}
			// The node may still have built the tab and launched the agent. Ask the machine itself before this is
			// called a failure: an adopted session returns through the normal success path, so the task is linked
			// once, with no failure receipt, retry or reroute (and no duplicate agent on another computer, #6540).
			if sess, ok := s.adoptTaskStartSession(ctx, m, s.startLauncher(ctx, picked), t.Cwd, expectID, knownIDs); ok {
				s.logf("task %s: start answered late on %s; adopted session %s from the machine's own list", t.TodoID, m.ID, sess.SessionID)
				t.startedAgent = cmp.Or(sess.Agent, fit.snapshot)
				noteDeferredStart(sess)
				s.nameAgentTabLater(agentTabNameStart(m.ID, sess, params["tab_name"].(string), t.Title))
				return m.ID, sess.SessionID, nil
			}
			// Nothing found, and the link took the answer (it closed, or the machine is not reachable now): the
			// node may not have had the start at all. Wait for the link to come back and look once more; still
			// nothing, the caller starts it once again (#7511). A link that stayed up is a busy node: no retry.
			if s.startLinkDown(ctx, m.ID, err) {
				back := s.waitMachineBack(ctx, m.ID)
				if back {
					// A start that may have reached the node is looked for until the node's own start has run out
					// of time, so a retry never opens a second agent beside one still being built (#6540).
					if sess, ok := s.adoptTaskStartSessionUntil(ctx, m, s.startLauncher(ctx, picked), t.Cwd, expectID, knownIDs, startMayHaveArrived(err, sentAt)); ok {
						s.logf("task %s: start's link dropped; adopted session %s on %s after it reconnected", t.TodoID, sess.SessionID, m.ID)
						t.startedAgent = cmp.Or(sess.Agent, fit.snapshot)
						noteDeferredStart(sess)
						s.nameAgentTabLater(agentTabNameStart(m.ID, sess, params["tab_name"].(string), t.Title))
						return m.ID, sess.SessionID, nil
					}
				}
				return "", "", startLinkLost{err: err, machine: m.Name, back: back}
			}
		}
		return "", "", err
	}
	if t.usesPreparedWorkspace() && res.Session.Cwd != t.Cwd {
		return "", "", taskworkspace.Failure("result_mismatch", "start", t.WorkspaceResolved.OperationID, "", t.Cwd)
	}
	if res.Session.SessionID == "" || res.Session.MachineID != "" && res.Session.MachineID != m.ID {
		return "", "", &rpc.Error{Code: "identity_conflict", Message: i18n.T(s.lang(ctx), "assistant-tasks.start_identity_conflict")}
	}
	if terminalToken != "" && res.Terminal != nil && res.Terminal.Token == terminalToken &&
		res.Terminal.SessionID == res.Session.SessionID && res.Terminal.PaneID == res.Session.PaneID && !res.Session.Starting {
		t.TerminalTarget = taskTerminalTarget{Machine: m.ID, TaskTerminal: *res.Terminal}
	}
	ran := ranLauncher(s.startLauncher(ctx, picked), res.Instead, res.Session)
	if res.Session.StartError != "" || res.Session.Starting {
		observed := res.Session
		observed.Launcher = ran
		s.followStarts(ctx, DeviceSessions{MachineID: m.ID, Sessions: []proto.Session{observed}})
		if observed.StartError != "" {
			return "", "", sessionStartErr(observed)
		}
	}
	if !res.Session.Starting {
		s.recordStartSuccess(ctx, m.ID, ran)
	}
	// The engine ran a launcher of the same kind, its own default model or a lower effort in place of what the task
	// asked for (#8140): the task names what runs, and its why says so.
	if note := s.startInsteadNote(ctx, m, res.Instead, res.Session); note != "" {
		if res.Instead.Launcher != "" && ran != picked {
			t.Launcher, t.Tier = ran, ""
			t.startNamed.launcher = true
		}
		if res.Instead.Model != "" {
			t.Model = ""
			t.startNamed.noModel = true
		}
		if res.Instead.Effort != "" {
			t.Effort = res.Session.Effort
			t.startNamed.effort = true
		}
		t.noteWhy(note)
	}
	if refused { // only once it started here: a task moved elsewhere after a failed start asks again
		off := false
		t.Ultracode = &off // a retry does not ask again
		if t.callerPick != nil {
			t.callerPick.ultracode = copyBool(t.Ultracode) // nor a policy's later rule: off is the task's own now
		}
		t.noteWhy(i18n.T(s.lang(ctx), "assistant-tasks.ultracode_skipped", i18n.Args{"reason": skipped}))
	}
	t.startedAgent = cmp.Or(res.Session.Agent, fit.snapshot)
	noteDeferredStart(res.Session)
	s.nameAgentTabLater(agentTabNameStart(m.ID, res.Session, params["tab_name"].(string), t.Title))
	return m.ID, res.Session.SessionID, nil
}

// taskStartAdoptWait bounds how long after an unanswered task start the hub asks the machine for the session its
// node may still be creating: a node's tab.create can land after the hub's wait ended (a tab 23 seconds late in
// #6540's incident). taskStartAdoptPoll is the pause between lists. Vars so tests shorten them instead of sleeping
// through the production bounds.
var (
	taskStartAdoptWait         = 30 * time.Second
	taskStartAdoptPoll         = 2 * time.Second
	taskStartFinalAdoptTimeout = time.Minute
)

// adoptTaskStartSession gives a task start whose answer never came back a second opinion from the target machine's
// own session list, reusing agent_start's adoption rule (adoptStartedSession): the start's own session id is matched
// exactly first, then a session absent from the pre-start snapshot, hub-started, a non-assistant agent of the same
// launcher and directory with a fresh sign of life that no open todo links, newest wins. The start's own context may
// be spent, so the reconciliation runs on a detached one. A pre-start snapshot that failed (nil knownIDs) disables
// only the heuristic: the exact expectID still needs no snapshot. With neither a snapshot nor an exact id nothing can
// be adopted, but a nonzero until still holds the retry while the original request may be running. A machine that
// cannot answer confirms nothing: the caller keeps its failure behavior, and nothing is started elsewhere.
func (s *Service) adoptTaskStartSession(ctx context.Context, m Device, launcher, cwd, expectID string, knownIDs map[string]bool) (proto.Session, bool) {
	return s.adoptTaskStartSessionUntil(ctx, m, launcher, cwd, expectID, knownIDs, time.Time{})
}

// adoptTaskStartSessionUntil is adoptTaskStartSession that keeps looking until until when that is later than
// taskStartAdoptWait from now. For a start that may have reached its node (nonzero until), one final lookup uses a
// fresh context after the polling deadline: the last pre-deadline list can just miss the session (#7772).
func (s *Service) adoptTaskStartSessionUntil(ctx context.Context, m Device, launcher, cwd, expectID string, knownIDs map[string]bool, until time.Time) (proto.Session, bool) {
	if knownIDs == nil && expectID == "" && until.IsZero() {
		return proto.Session{}, false
	}
	wait := max(taskStartAdoptWait, time.Until(until))
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait)
	defer cancel()
	for {
		if sess, ok := s.adoptStartedSession(actx, m, launcher, cwd, expectID, knownIDs); ok {
			return sess, true
		}
		if actx.Err() == nil {
			select {
			case <-time.After(taskStartAdoptPoll):
				continue
			case <-actx.Done():
			}
		}
		if until.IsZero() {
			return proto.Session{}, false
		}
		// Each list and the pause inside this final reconciliation have their own bounds, so the expired polling
		// context must not be reused. Without a snapshot or exact id this returns false after the same safe wait.
		// The final whole-call deadline also covers the todo book load used by heuristic adoption.
		fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), taskStartFinalAdoptTimeout)
		defer fcancel()
		return s.adoptStartedSession(fctx, m, launcher, cwd, expectID, knownIDs)
	}
}

// updatePlanTask keeps the machine, directory and why task i of started plan planID runs with now, its actual launch
// permission, and its ultracode when the start went without it. Of its launcher, model, effort, speed and tier it keeps
// the task's own (storedLaunch), not what a tier, Model configuration › Agents or a start-defaults policy filled in on
// this try: a later start reading the task back must not take those for its own choice (#12618).
func (s *Service) updatePlanTask(ctx context.Context, planID string, i int, t PlanTask) {
	launch := t.storedLaunch()
	if err := s.patchPlanTask(ctx, planID, i, func(pt *PlanTask) {
		pt.Machine, pt.Cwd, pt.Why = t.Machine, t.Cwd, t.Why
		if t.Placement != nil {
			pt.Placement = t.Placement
		}
		pt.Reroute, pt.Ultracode = t.Reroute, launch.ultracode
		pt.Workspace, pt.WorkspaceResolved, pt.MachinePolicy = t.Workspace, t.WorkspaceResolved, t.MachinePolicy
		pt.WorkspaceInferred, pt.ReservedTodoID = t.WorkspaceInferred, t.ReservedTodoID
		pt.Launcher, pt.Model, pt.Effort, pt.Speed, pt.Tier = launch.launcher, launch.model, launch.effort, launch.speed, launch.tier
		pt.LauncherDefaultModel = t.LauncherDefaultModel
		pt.Permission = launch.permission
		pt.StartPermission = t.Permission
	}); err != nil {
		s.logf("plan %s: %v", planID, err)
	}
}

// A typed pre-launch refusal is safe to retry once the task's conversations have
// the fallback notice. Never replay an uncertain start.
func (s *Service) startWithCodexRouteNotice(ctx context.Context, todoID, agent string, params map[string]any, start func() error) error {
	effort, _ := params["effort"].(string)
	if todoID == "" || agent != "codex" || effort == "" || params["codex_effort_fallback"] == proto.CodexFallbackAnnounced {
		return start()
	}
	params["codex_effort_fallback"] = proto.CodexFallbackNotify
	err := start()
	if !refusedWith(err, proto.CodeCodexEffortTerminal) {
		return err
	}
	task, ok := s.todoNow(todoID)
	if !ok || task.DoneAt != nil {
		return err
	}
	notice := noticeBody{Text: i18n.T(s.lang(ctx), codexRouteNoticeKey)}
	if _, err := s.appendFeed(ctx, task.Origin, feedNotice, todoID, notice); err != nil {
		return err
	}
	if v := task.Supervision; v != nil && v.Supervisor != "" && v.Supervisor != task.Origin {
		if _, err := s.appendFeed(ctx, v.Supervisor, feedNotice, todoID, notice); err != nil {
			return err
		}
	}
	params["codex_effort_fallback"] = proto.CodexFallbackAnnounced
	return start()
}

const codexRouteNoticeKey = "assistant-tasks.codex_effort_terminal"

// isCodexRouteNoticeText reports whether text matches the hub's Codex terminal
// fallback notice in any supported language.
func isCodexRouteNoticeText(text string) bool {
	trimmed := strings.TrimSpace(text)
	return slices.ContainsFunc(i18n.Langs, func(lang i18n.Lang) bool {
		return trimmed == i18n.T(lang, codexRouteNoticeKey)
	})
}

// isCodexRouteProgress reports whether p is a legacy Codex terminal fallback
// notice previously stored in Supervision.Progress.
func isCodexRouteProgress(p *TaskProgress) bool {
	return p != nil && p.Kind == upReportProgress && isCodexRouteNoticeText(p.Text)
}

// readTaskNotice preserves the stored event ID and time for clients merging old feed pages.
// Older hubs recorded this notice as an Agent's progress report.
func readTaskNotice(item FeedItem) FeedItem {
	if item.Kind != feedReport || item.Ref == "" {
		return item
	}
	var report reportBody
	if json.Unmarshal(item.Body, &report) == nil && report.Kind == upReportProgress && isCodexRouteNoticeText(report.Text) {
		item.Kind = feedNotice
	}
	return item
}
