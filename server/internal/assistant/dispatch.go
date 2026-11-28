package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Cross-machine dispatch (docs/plans/plan-agent-dispatch.md): an agent on one computer hands a subtask to a new agent
// on another connected computer (dispatch_start) and reads its state and final reply back (dispatch_read). The
// manager starts the session as it starts any agent, so the hub stays the only path between computers. Off unless
// manager.json agents.dispatch.enabled is true; while off the tools are not listed and their calls are refused.

// dispatchToolNames are the agents' dispatch tools (prompts/mcp/dispatch). Like agentToolNames they act for the
// agent calling them: the node names its session (node.callerTools).
var dispatchToolNames = []string{"dispatch_start", "dispatch_read"}

func isDispatchTool(name string) bool { return slices.Contains(dispatchToolNames, name) }

// dispatchTools are the dispatch tools when the switch is on, none otherwise.
func (s *Service) dispatchTools(ctx context.Context) []proto.Tool {
	if !s.hello(ctx).Agents.Dispatch.On() {
		return nil
	}
	return toolsNamed(dispatchToolNames)
}

const (
	// dispatchSettingKey holds the dispatched tasks ([]dispatchTask): who may read which session survives a restart.
	dispatchSettingKey = "agents.dispatch.tasks"
	dispatchKeep       = 7 * 24 * time.Hour
	dispatchKeepMost   = 200
	// dispatchStartGrace: a task this young counts as working although its session does not say so yet.
	dispatchStartGrace = 2 * time.Minute
	// dispatchWaitMost bounds one dispatch_read wait: a tool call held longer risks its caller's own timeout.
	dispatchWaitMost = 50 * time.Second
)

// dispatchPoll is how often a waiting dispatch_read looks at the task's session.
var dispatchPoll = 2 * time.Second

// dispatchTask is one subtask an agent handed to another computer.
type dispatchTask struct {
	FromMachine string    `json:"from_machine"`
	FromSession string    `json:"from_session"`
	Machine     string    `json:"machine"`
	Session     string    `json:"session"`
	Cwd         string    `json:"cwd,omitempty"`
	Title       string    `json:"title,omitempty"`
	At          time.Time `json:"at"`
	a2a         *a2aTask
}

// dispatchBook is the dispatched tasks, read from the settings on first use and saved on every change.
type dispatchBook struct {
	admit  sync.Mutex
	mu     sync.Mutex
	loaded bool
	tasks  []dispatchTask
}

// dispatched are the tasks kept (a copy). A read that fails is tried again by the next call; meanwhile the tasks of
// this process are still known.
func (s *Service) dispatched(ctx context.Context) []dispatchTask {
	b := &s.dispatch
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadDispatched(ctx)
	return slices.Clone(b.tasks)
}

// loadDispatched reads the saved tasks once, before those this process added; the book's lock is held.
func (s *Service) loadDispatched(ctx context.Context) {
	b := &s.dispatch
	st := s.store()
	if b.loaded || st == nil {
		return
	}
	var saved []dispatchTask
	if _, err := st.Setting(ctx, dispatchSettingKey, &saved); err != nil {
		s.logf("dispatch: the saved tasks could not be read: %v", err)
		return
	}
	b.tasks, b.loaded = append(saved, b.tasks...), true
}

// addDispatched records t, dropping tasks older than dispatchKeep and all but the newest dispatchKeepMost.
func (s *Service) addDispatched(ctx context.Context, t dispatchTask) {
	b := &s.dispatch
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadDispatched(ctx)
	kept := b.tasks[:0]
	for _, old := range b.tasks {
		if t.At.Sub(old.At) < dispatchKeep {
			kept = append(kept, old)
		}
	}
	kept = append(kept, t)
	if n := len(kept) - dispatchKeepMost; n > 0 {
		kept = kept[n:]
	}
	b.tasks = kept
	st := s.store()
	if st == nil || !b.loaded {
		return // no database, or its tasks are not read yet: saving now would drop them
	}
	if err := st.PutSetting(ctx, dispatchSettingKey, b.tasks); err != nil {
		s.logf("dispatch: the task %s on %s was not saved: %v", short(t.Session), t.Machine, err)
	}
}

// canonical is machine's id as the manager keeps it.
func (s *Service) canonical(machine string) string {
	if s.devs != nil {
		return s.devs.Canonical(machine)
	}
	return machine
}

// dispatchTool runs one of the dispatch tools for agent session on machine.
func (s *Service) dispatchTool(ctx context.Context, machine, session, name string, args json.RawMessage) proto.ToolResult {
	if !s.hello(ctx).Agents.Dispatch.On() {
		return proto.TextResult("handing tasks to other computers is switched off (agents.dispatch.enabled in manager.json); do the work here", true)
	}
	if session == "" {
		return proto.TextResult("Persona cannot tell which agent is calling: this computer's node may be older than the hub (update it), "+
			"or this agent does not run in a pane Persona follows", true)
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	machine = s.canonical(machine)
	var res string
	var err error
	if name == "dispatch_start" {
		if err := s.guardRefusal(ctx, machine, session, "agent_start"); err != nil {
			return proto.TextResult(err.Error(), true)
		}
		res, err = s.dispatchStart(ctx, machine, session, args, time.Now())
	} else {
		res, err = s.dispatchRead(ctx, machine, session, args)
	}
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	return proto.TextResult(res, false)
}

// dispatchStart starts a new agent on another computer for agent machine/session and records it as that agent's.
func (s *Service) dispatchStart(ctx context.Context, machine, session string, args json.RawMessage, now time.Time) (result string, resultErr error) {
	var a struct {
		Machine   string `json:"machine"`
		Cwd       string `json:"cwd"`
		Prompt    string `json:"prompt"`
		Launcher  string `json:"launcher"`
		Tier      string `json:"tier"`
		MessageID string `json:"message_id"`
		Worktree  struct {
			Branch string `json:"branch"`
		} `json:"worktree"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	a.Machine, a.Cwd, a.Prompt = strings.TrimSpace(a.Machine), strings.TrimSpace(a.Cwd), strings.TrimSpace(a.Prompt)
	if a.Machine == "" || a.Cwd == "" || a.Prompt == "" {
		return "", errors.New("machine, cwd and prompt are required; nothing was started")
	}
	am, ok := s.machineByName(ctx, a.Machine)
	if !ok {
		return "", fmt.Errorf("unknown computer %q (computers: %s); nothing was started", a.Machine, s.machineIDs(ctx))
	}
	switch {
	case s.canonical(am.ID) == machine:
		return "", errors.New("that is the computer you run on: use your own in-session subagents for work here; nothing was started")
	case s.isWorkMachine(am.ID):
		return "", fmt.Errorf("%s is a work computer: tasks are not handed to it; nothing was started", am.Name)
	}
	// The start is detached from the calling turn, as agent_start's is: a computer waking up can take minutes.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentStartTimeout)
	defer cancel()
	a.Cwd = proto.ExpandHome(a.Cwd, am.Home)
	source := a2aRef{machine, session}
	launch := a2aLaunch{CWD: a.Cwd, Launcher: strings.TrimSpace(a.Launcher), Tier: strings.TrimSpace(a.Tier), Branch: strings.TrimSpace(a.Worktree.Branch), Title: clip(oneLine(a.Prompt), 100)}
	if launch.Tier != "" && !s.dispatchRetry(sctx, source, a.MessageID) {
		// The tier is resolved, or refused, before the task is recorded (agent_presets.go); a retry matches it as named.
		run, err := s.agentStartTier(sctx, am, launch.Tier, launch.Launcher)
		if err != nil {
			return "", fmt.Errorf("%w; nothing was started", err)
		}
		launch.Run = run
	}
	// A start that could not run read-only where it would go is refused before its task is recorded; a retry by
	// message_id is answered as before (admitDispatchA2A).
	refused := s.dispatchStartRefusal(sctx, am, launch)
	tracker, existing, err := s.admitDispatchA2A(sctx, source, s.canonical(am.ID), a.MessageID, a.Prompt, launch, now, refused)
	if err != nil {
		return "", err
	}
	if existing {
		return dispatchA2AResult(tracker.task, fmt.Sprintf("Task %s on %s: %s.", tracker.task.TargetSession, am.Name, dispatchA2AStatus(tracker.task))), nil
	}
	var res struct {
		Session proto.Session `json:"session"`
	}
	attempted := false
	finished := false
	var startErr error
	defer func() {
		if finished {
			return
		}
		if startErr == nil {
			startErr = resultErr
		}
		s.finishDispatchA2A(sctx, tracker, res.Session, attempted, startErr)
	}()
	from := prompts.DispatchTask{Prompt: a.Prompt}
	if fm, ok := s.machineByName(ctx, machine); ok {
		from.FromMachine = fm.Name
	}
	if c, ok := s.session(ctx, machine, session); ok {
		from.From = agentLabel(c)
	}
	title := localTabName(a.Prompt)
	params := map[string]any{"cwd": a.Cwd, "launcher": strings.TrimSpace(a.Launcher), "model": "", "effort": "",
		"tab_name": title, "title": title, "prompt": s.withCloseoutBrief(sctx, am.ID, prompts.RenderDispatchTask(from), false)}
	launch.Run.lay(params) // a tier's launcher, model, effort and speed; never its permission mode (agent_presets.go)
	if err := s.applyModel(sctx, "agent.new", params); err != nil {
		return "", fmt.Errorf("%w; nothing was started", err)
	}
	if tracker != nil {
		task := tracker.task
		first := task.Events[0]
		first.Text = params["prompt"].(string)
		params["prompt"] = a2aPrompt(task, &first, source, string(s.lang(sctx)), s.a2aLaunchExecutable(sctx, am, params))
		params["session_id"] = task.TargetSession
		params["client_msg_id"] = a2aDeliveryID(task.ID, 1)
		params["message_source"] = a2aMessageSource(source)
	}
	note := ""
	if branch := launch.Branch; branch != "" {
		wt, err := s.newWorktree(sctx, am.ID, a.Cwd, branch)
		if err != nil {
			return "", fmt.Errorf("the worktree on branch %s was not made, nothing was started: %w", branch, err)
		}
		a.Cwd = wt.Path
		params["cwd"], params["workspace_id"] = wt.Path, wt.WorkspaceID
		note = fmt.Sprintf(" It runs in a new worktree on branch %s.", wt.Branch)
	}
	if launch.Run.Launcher != "" { // what runs, Model configuration › Agents laid over the tier, as agent_start says it
		selected := pickOf(params)
		note += fmt.Sprintf(" Preset %s: runs on %s.", launch.Tier,
			ModelTier{Launcher: selected.launcher, Model: selected.model, Effort: selected.effort, Speed: selected.speed}.runs())
	}
	dispatchAllowAll(sctx, s, am, a.Launcher, params)
	// Where it starts, decided as it goes out (start_host.go): an agy launcher on the computer's companion where that
	// runs it, as for agent_start; a read-only start a pane cannot hold is refused here, not by the node.
	companion, err := s.agentStartedHost(sctx, am, params)
	if err != nil {
		return "", err
	}
	s.setPlanThen(ctx, am.ID, params)
	decision := newPlacement(am.ID, "report")
	if tracker != nil {
		decision.TaskID = tracker.task.ID
	}
	sctx = context.WithValue(sctx, placementBindKey{}, decision)
	attempted = true
	startErr = s.call(sctx, am.ID, DeviceSessionStart, params, &res)
	if companionAway(startErr, companion) && params["permission_mode"] != "plan" {
		// The companion went away before the start reached it: nothing ran, and a writable start opens in the default
		// host. A read-only one fails with the node's reason: no pane holds it read-only.
		delete(params, "host")
		s.logf("dispatch: the Antigravity companion is not connected on %s; starting in its default host", am.ID)
		startErr = s.call(sctx, am.ID, DeviceSessionStart, params, &res)
	}
	if startErr != nil {
		switch {
		case errors.Is(startErr, apierr.ErrOffline):
			return "", fmt.Errorf("%s is offline; nothing was started", am.Name)
		case startResultUnknown(startErr) || sendUnknown(startErr):
			if tracker != nil {
				return "", fmt.Errorf("%s has not confirmed the start of A2A task %s. Check that task before starting another Agent", am.Name, tracker.task.ID)
			}
			return "", fmt.Errorf("%s did not answer the start in time: an agent may or may not have started there, and it is "+
				"not recorded as yours. Tell the user instead of starting it again", am.Name)
		}
		return "", fmt.Errorf("the start on %s failed: %w", am.Name, startErr)
	}
	res.Session.Cwd = cmp.Or(res.Session.Cwd, a.Cwd)
	s.finishDispatchA2A(sctx, tracker, res.Session, true, nil)
	finished = true
	title = s.nameStartedAgent(sctx, am.ID, res.Session, title)
	s.nameAgentTabLater(agentTabNameStart(am.ID, res.Session, title, a.Prompt))
	if tracker == nil {
		s.addDispatched(ctx, dispatchTask{FromMachine: machine, FromSession: session, Machine: s.canonical(am.ID),
			Session: res.Session.SessionID, Cwd: a.Cwd, Title: title, At: now})
		s.recordSubagentLink(s.canonical(am.ID), res.Session.SessionID, machine, session, proto.SubagentOriginDispatch)
	}
	s.logf("dispatch %s/%s -> %s/%s in %s", machine, short(session), am.ID, short(res.Session.SessionID), a.Cwd)
	child := res.Session
	if id := s.superviseDispatched(ctx, am, child, a.Prompt, cmp.Or(from.From, short(session)), cmp.Or(from.FromMachine, machine)); id != "" {
		decision.TaskID = id
		s.savePlacement(ctx, decision)
	}
	if tracker != nil {
		return fmt.Sprintf("Started on %s in %s: session %s.%s A2A task %s. Questions and results will be delivered to this session. Read details with dispatch_read or a2a_get.",
			am.Name, a.Cwd, res.Session.SessionID, note, tracker.task.ID), nil
	}
	return fmt.Sprintf("Started on %s in %s: session %s.%s It works on its own and the user can watch it in the apps. "+
		"Read its state and final reply with dispatch_read (session_id %s, wait_seconds up to 50).",
		am.Name, a.Cwd, res.Session.SessionID, note, res.Session.SessionID), nil
}

// dispatchState checks the computer before its session. Only a listed done or idle session is known to have stopped.
func (s *Service) dispatchState(ctx context.Context, t dispatchTask, now time.Time) string {
	if t.a2a != nil {
		return dispatchA2AStatus(t.a2a)
	}
	computer := s.computerState(t.Machine)
	switch computer.state {
	case standingComputerRemoved:
		return "computer removed"
	case standingComputerOffline:
		return "computer offline"
	}
	c, found, err := s.sessionNow(ctx, t.Machine, t.Session)
	switch {
	case errors.Is(err, ErrNoDevice):
		return "computer removed"
	case err != nil:
		return "not read"
	case !found && now.Sub(t.At) < dispatchStartGrace:
		return "working"
	case !found && !computer.listed:
		return "not read"
	case !found:
		return "not listed"
	}
	switch c.Status {
	case proto.StatusWorking:
		return "working"
	case proto.StatusBlocked:
		return "waiting for the user"
	case proto.StatusInterrupted, proto.StatusOffline:
		return "interrupted"
	case proto.StatusIdle, proto.StatusDone:
		return "stopped"
	default:
		return "state unknown"
	}
}

func (s *Service) dispatchWorking(ctx context.Context, t dispatchTask, now time.Time) bool {
	return s.dispatchState(ctx, t, now) == "working"
}

// dispatchRead answers agent machine/session about the tasks it dispatched: all of them and the computers there
// are, or one task's state and last turn, after waiting for it to stop when asked.
func (s *Service) dispatchRead(ctx context.Context, machine, session string, args json.RawMessage) (string, error) {
	var a struct {
		SessionID   string `json:"session_id"`
		WaitSeconds int    `json:"wait_seconds"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	tasks, _, err := s.dispatchTasks(ctx, a2aRef{machine, session})
	if err != nil {
		return "", err
	}
	var mine []dispatchTask
	for _, t := range tasks {
		if t.FromMachine == machine && t.FromSession == session {
			mine = append(mine, t)
		}
	}
	id := strings.TrimSpace(a.SessionID)
	if id == "" {
		return s.dispatchOverview(ctx, machine, mine), nil
	}
	var found []dispatchTask
	for _, t := range mine {
		if t.Session == id || len(id) >= 8 && strings.HasPrefix(t.Session, id) || t.a2a != nil && t.a2a.ID == id {
			found = append(found, t)
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf("no task %s among those you dispatched (dispatch_read with no arguments lists them)", id)
	}
	t := found[0]
	wait := min(time.Duration(max(a.WaitSeconds, 0))*time.Second, dispatchWaitMost)
	if t.a2a != nil {
		return s.readDispatchA2A(ctx, a2aRef{machine, session}, t.a2a, wait)
	}
	state := s.dispatchState(ctx, t, time.Now())
	for deadline := time.Now().Add(wait); state == "working" && time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(dispatchPoll):
		}
		state = s.dispatchState(ctx, t, time.Now())
	}
	name := t.Machine
	if m, ok := s.machineByName(ctx, t.Machine); ok {
		name = m.Name
	}
	head := fmt.Sprintf("Task %s on %s (%s): %s.", t.Session, name, t.Cwd, state)
	switch state {
	case "working":
		head += " Its turn is not over: what follows is not its result yet. Call again with wait_seconds."
	case "stopped":
		head += " Its last reply below is its result."
	case "waiting for the user":
		head += " It asked the user something and goes on once they answer in the apps."
	case "computer offline":
		return head + " " + name + " is offline now: its state and last reply cannot be read until it is connected again.", nil
	case "computer removed":
		return head + " " + name + " is no longer one of the computers this Persona manages: nothing can be read there.", nil
	case "not listed":
		return head + " The session is no longer in " + name + "'s list; its last reply was not read this time; state unknown.", nil
	case "not read":
		return head + " Its state could not be read this time, so it is unknown; read it again later.", nil
	case "state unknown":
		head += " Persona cannot tell whether its turn is over: the last reply below may not be its result."
	}
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, t.Machine, DeviceSessionItems, map[string]any{"session_id": t.Session, "turns": 1}, &res); err != nil {
		return "", fmt.Errorf("%s Its transcript could not be read: %w", head, err)
	}
	return transcriptText(head, sessionLink(s.webBase(ctx), t.Machine, t.Session, false), res.Items), nil
}

// dispatchOverview lists the computers a task can go to from machine, each with the Agent presets it can start
// (dispatch_start's tier), and the calling agent's tasks.
func (s *Service) dispatchOverview(ctx context.Context, machine string, mine []dispatchTask) string {
	var b strings.Builder
	b.WriteString("Computers a task can go to:\n")
	n := 0
	_, durableErr := s.a2aStore()
	cfg := s.a2aConfig(ctx)
	var targets, online []Device
	for _, m := range s.devices(ctx) {
		if s.canonical(m.ID) == machine || s.isWorkMachine(m.ID) ||
			durableErr == nil && !s.a2aMachineAllowed(ctx, cfg, s.canonical(m.ID)) {
			continue
		}
		targets = append(targets, m)
		if m.Online || m.Reachable {
			online = append(online, m)
		}
	}
	presets := s.presetsOn(ctx, online)
	for _, m := range targets {
		state := "offline"
		if m.Online || m.Reachable {
			state = "online"
		}
		home := m.Home
		if home == "" {
			home = "home not reported"
		}
		fmt.Fprintf(&b, "- %s (id %s): %s, %s\n", m.Name, m.ID, state, home)
		writePresets(&b, presets, m.ID)
		n++
	}
	if n == 0 {
		b.WriteString("- none: no other computer is connected\n")
	}
	b.WriteString("\nTasks you dispatched:\n")
	if len(mine) == 0 {
		b.WriteString("- none\n")
	}
	now := time.Now()
	for _, t := range mine {
		fmt.Fprintf(&b, "- %s on %s (%s): %s", t.Session, t.Machine, t.Cwd, s.dispatchState(ctx, t, now))
		if t.Title != "" {
			fmt.Fprintf(&b, " · %s", t.Title)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// superviseDispatched makes dispatched session c on am a supervised task of the main conversation, as agent_supervise
// makes one (adoptAgent): a todo whose notes say which agent handed it out, its card in the main conversation's feed,
// and the supervision messages and task_review of any task the main conversation hands out. The calling agent still
// reads the result with dispatch_read. Without a main conversation, or when the todo cannot be made, the task runs
// untracked as before; the start itself has succeeded either way.
func (s *Service) superviseDispatched(ctx context.Context, am Device, c proto.Session, prompt, from, fromMachine string) string {
	main := s.settings(ctx).Main
	if main == "" || c.SessionID == "" {
		return ""
	}
	notes := i18n.T(s.lang(ctx), "assistant-tasks.dispatch_notes", i18n.Args{"from": from,
		"from_machine": fromMachine, "machine": am.Name, "cwd": c.Cwd}) + "\n\n" + prompt
	todo, made, err := s.adoptAgent(context.WithoutCancel(ctx), main, am, c, "", prompt, notes, nil, 0, false)
	if err != nil {
		s.logf("dispatch %s on %s: task: %v", short(c.SessionID), am.ID, err)
		return ""
	}
	if made {
		s.taskCard(ctx, main, todo.ID, "")
	}
	return todo.ID
}

// dispatchStartRefusal is why a new dispatch_start on am cannot go out, worked out on a probe before its task is
// recorded: the start as it will reach the node (launch's launcher with Model configuration › Agents laid over it,
// and dispatch_start's permission rule, dispatchAllowAll) in plan on a computer that cannot hold its launcher
// read-only (agentStartedHost). nil when it can go out, or when Model configuration cannot give its launcher now: the
// start reports that as before.
func (s *Service) dispatchStartRefusal(ctx context.Context, am Device, launch a2aLaunch) error {
	probe := map[string]any{"launcher": launch.Launcher, "model": "", "effort": ""}
	launch.Run.lay(probe) // the tier's launcher and model, as the start lays them on its own params
	if s.applyModel(ctx, "agent.new", probe) != nil {
		return nil
	}
	dispatchAllowAll(ctx, s, am, launch.Launcher, probe)
	_, err := s.agentStartedHost(ctx, am, probe)
	return err
}

// dispatchAllowAll starts a dispatched Agent, a supervised task's (superviseDispatched), in Allow all as agent_start
// and a planned task start theirs (launcher_fit.go allowAll; docs/assistant/unified-entry.md "Permissions": Claude-family
// launchers always start in bypassPermissions): without it a Claude Code child took its computer's default mode and
// stopped at its first tool approval. A mode Model configuration's agent.new binding names stays. The launcher is only
// looked up here: the start keeps the one it was given, and a launcher fitLauncher would refuse or replace, or one it
// cannot tell, keeps the computer's default.
func dispatchAllowAll(ctx context.Context, s *Service, am Device, launcher string, params map[string]any) {
	if mode, _ := params["permission_mode"].(string); mode != "" {
		return
	}
	probe := maps.Clone(params)
	_, fit, err := s.fitLauncher(ctx, am, startPick{launcher: strings.TrimSpace(launcher)}, probe)
	if err != nil || probe["launcher"] != params["launcher"] || !startTakesModes(fit.agent) || !fit.allowAll() {
		return
	}
	params["permission_mode"] = "bypassPermissions"
}
