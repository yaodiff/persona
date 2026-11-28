package assistant

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A task whose agent did not start because of its launcher on that computer (the terminal cannot run it, its key or
// model route is broken, its subscription is used up) starts again on another launcher of the same computer only when
// someone says so (#10365): task_retry's launcher, or the app's "Use <launcher> instead" (POST /api/v1/todos/{id}/retry
// with {launcher, model}, hello feature task_retry_launcher). The hub never swaps a named launcher by itself; it
// suggests (PlanTask.RetryWith) and checks the one asked for before anything is reopened. The task keeps its computer,
// directory and workspace (the checkout it already made is used again), so the retry is the same task on another
// launcher, not a new one.

// retryWith is another launcher (and its model) a retry starts a task's agent with; zero: the one its plan has. by: who
// asked (user), for the start_remedy step that records the change; "" when the caller writes its own (task_retry).
// answer: the owner's answer to the startup question the last start stopped on, typed into its kept tab first, where
// the retry then starts (start_answer.go). failedAt: only the failed start that ended then is started again (the asks
// route's todo:retry@<done_at>); zero: whichever the to-do has.
type retryWith struct {
	launcher, model, by, answer string
	failedAt                    time.Time
}

// retryRefused: a retry on another launcher refused before anything changed (409, the words say why).
type retryRefused struct{ error }

func (e retryRefused) Unwrap() error { return e.error }

// retryWithMax bounds the launchers a failed task suggests.
const retryWithMax = 3

// launcherSpecificFailure: err is a failed start that another launcher on the same computer may not have: the
// terminal could not run the launcher (launcher_missing), the computer does not have the one the plan named, its health
// record kept it off a fixed computer, or its error reads as its key, its model route or its subscription's limit. A
// computer that is offline or a link that dropped is not the launcher's, nor is Claude Code missing (every Claude Code
// launcher needs it), nor the request's own refusal.
func launcherSpecificFailure(err error) bool {
	if err == nil || transientStartErr(err) || errors.Is(err, errMachineOffline) || startRefused(err) || startStalled(err) {
		return false
	}
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == proto.CodeLauncherMissing {
		return true
	}
	var m *i18n.Message
	if errors.As(err, &m) && (m.Key == "assistant-tasks.fixed_launcher_unhealthy" || m.Key == "assistant-tasks.launcher_not_on_machine") {
		return true
	}
	kind := codedLauncherErrorKind(err)
	if kind == "" {
		kind = classifyLauncherError(err.Error())
	}
	switch kind {
	case errKindAuth, errKindModelRoute, errKindUsageLimit:
		return true
	}
	return false
}

// personadMissing: t's start failed because the terminal could not run the file a Persona-managed launcher starts
// through (personad itself: launcher_missing, where terminal, a path it could not run, node/shellmiss.go). Every
// managed launcher starts through the same program, so none of them is a way out until that is fixed.
func (s *Service) personadMissing(ctx context.Context, t PlanTask) bool {
	a := t.ErrorArgs
	if t.ErrorCode != proto.CodeLauncherMissing || a["where"] != "terminal" || a["kind"] != "no_such_file" && a["kind"] != "permission_denied" {
		return false
	}
	return s.managedLauncher(ctx, cmp.Or(a["launcher"], t.Launcher))
}

// ranLauncher is the launcher task t's last start ran: the one its refusal names (runPlanTaskStart adds it when the
// node's does not), else the one its plan names or the default. A tier, a start-defaults policy or the agent.new
// binding may have chosen it, and the plan keeps only what the task named itself (storedLaunch), so the plan's
// launcher alone is not what failed.
func (s *Service) ranLauncher(ctx context.Context, t PlanTask) string {
	return cmp.Or(t.ErrorArgs["launcher"], s.startLauncher(ctx, t.Launcher))
}

// managedLauncher: name is a Persona-managed launcher (Settings › Models), which a node runs through personad launch.
func (s *Service) managedLauncher(ctx context.Context, name string) bool {
	return name != "" && slices.ContainsFunc(s.storedLaunchers(ctx), func(l storedLauncher) bool { return l.Name == name })
}

// retryLaunchers are the launchers task t, whose start on computer m just failed with err, may start again with
// there (launcherAlternatives), suggested only for a failure of its launcher (launcherSpecificFailure); t carries the
// failure's code and args.
func (s *Service) retryLaunchers(ctx context.Context, m Device, t PlanTask, err error) []string {
	if !launcherSpecificFailure(err) {
		return nil
	}
	return s.launcherAlternatives(ctx, m, t)
}

// launcherAlternatives are the launchers other than t's (and except) that t may start with on m: plain claude first
// when m has Claude Code (launcherInstead's order), then m's own and the managed ones in their list's order, each
// running the agent t's launcher runs, never one the health records say cannot run there now, or a managed one while
// the terminal could not run personad itself (personadMissing). A list that can't be read gives plain claude alone, when
// m has it and t's launcher runs Claude Code.
func (s *Service) launcherAlternatives(ctx context.Context, m Device, t PlanTask, except ...string) []string {
	failed := s.ranLauncher(ctx, t)
	list, known := s.launchersHere(withUnhidden(ctx, failed), m.ID)
	var configured []ConfiguredLauncher
	if known {
		for _, l := range list {
			configured = append(configured, ConfiguredLauncher{Launcher: l})
		}
		configured = sessionLaunchers(configured, s.hello(ctx).Models.Launchers[m.ID])
	}
	kind := "claude"
	if i := slices.IndexFunc(configured, func(l ConfiguredLauncher) bool { return l.Name == failed }); i >= 0 {
		kind = cmp.Or(configured[i].AgentType, kind)
	} else {
		for _, l := range s.storedLaunchers(ctx) {
			if l.Name == failed {
				kind = cmp.Or(l.AgentType(), kind)
			}
		}
	}
	noManaged := s.personadMissing(ctx, t)
	var out []string
	add := func(name, agent string, managed bool) {
		if len(out) >= retryWithMax || name == "" || name == failed || slices.Contains(except, name) || slices.Contains(out, name) ||
			cmp.Or(agent, "claude") != kind || managed && noManaged {
			return
		}
		if _, bad := s.launcherUnusable(ctx, m.ID, name); bad {
			return
		}
		out = append(out, name)
	}
	if m.ClaudeVersion != "" || slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == "claude" }) {
		add("claude", "claude", false)
	}
	for _, l := range configured {
		if startableLauncher(l.Launcher) {
			add(l.Name, l.AgentType, compat.Source(l.Source) == "persona")
		}
	}
	return out
}

// retryOn makes task t, whose start failed on computer m, start again with another launcher (with.launcher, and
// with.model or that launcher's default), checked before anything changes: the launcher runs on m (fitLauncher: one
// m's list lacks is refused with the ones it has), is not one a health record says cannot run there now (a terminal
// record excepted: only a real start clears it, as an explicit retry on a fixed computer goes back to it), holds a
// read-only task read-only. A managed one is taken even after the terminal could not run personad itself: it is only
// left out of the suggestions (launcherAlternatives), since the owner may have fixed the terminal (or the node, which
// now hands terminals the home's spelling of its program) since that record. t keeps its
// computer, directory and workspace; its tier goes (it named the launcher before), and so does a start-defaults
// policy's pick (the task names the launcher now). Its permission mode stays when the launcher runs the same agent
// as the one that failed (a Codex task in plan mode stays in plan mode), and is settled again for another agent or
// when it was what Allow all made it; its why says what changed. No launcher is a bare retry and changes nothing here.
// A launcher-only override always selects that launcher's native model default, even when it is the plan's launcher.
func (s *Service) rememberRetryFrom(ctx context.Context, t *PlanTask, m Device, launcher string) {
	a := s.launcherAgentOn(ctx, m, launcher)
	t.RetryFromAgent, t.RetryFromAllowAll = a.agent, a.allowAll()
}

func (s *Service) retryOn(ctx context.Context, t *PlanTask, m Device, with retryWith) error {
	name, model := strings.TrimSpace(with.launcher), strings.TrimSpace(with.model)
	from := s.ranLauncher(ctx, *t)
	s.rememberRetryFrom(ctx, t, m, from)
	if name == "" {
		if model != "" {
			return planInvalid{i18n.Err("assistant-tasks.retry_model_without_launcher")}
		}
		return nil
	}
	if err := checkModelID(model); err != nil {
		return planInvalid{err}
	}
	// Compared with what the failed start ran (ranLauncher), not with the plan's launcher: a task whose tier or
	// start-defaults policy chose the launcher names none itself, and kept so, it would start on that one again.
	if name == t.Launcher && t.Tier == "" && model == "" && t.Model == "" {
		t.LauncherDefaultModel = true
		return nil
	}
	machine := firstNonEmpty(m.Name, m.ID)
	alternatives := func() string {
		return cmp.Or(strings.Join(s.launcherAlternatives(ctx, m, *t, name), ", "), "—")
	}
	params := map[string]any{"launcher": name}
	if t.Host != "" {
		params["host"] = t.Host
	}
	if model != "" {
		params["model"] = model
	}
	_, agent, err := s.fitLauncher(ctx, m, startPick{launcher: name, model: model}, params)
	if err != nil {
		return retryRefused{err}
	}
	if r, bad := s.launcherUnusableNow(ctx, m.ID, name); bad && r.Kind != errKindNotInTerminal && name != from {
		return retryRefused{i18n.Err("assistant-tasks.retry_launcher_unusable", i18n.Args{
			"launcher": name, "machine": machine, "reason": r.Error, "launchers": alternatives()})}
	}
	if t.Readonly {
		s.companionStartHost(ctx, m, agent, params)
		if err := s.readonlyLauncherRefusal(ctx, m, params, agent); err != nil {
			return retryRefused{err}
		}
	}
	t.Launcher, t.Model, t.Tier = name, model, ""
	t.LauncherDefaultModel = model == ""
	modes := startTakesModes(agent.agent)
	if !startTakesEffort(agent.agent) {
		t.Effort = ""
	}
	if !t.preservePermission {
		if agent.allowAll() {
			t.Permission = "bypassPermissions"
		} else {
			// Settled again, as task_plan settles it, only for another agent than the one that failed, or when the
			// mode is what Allow all gave that one (Claude Code's, Auto Accept's): the same agent keeps the task's own.
			if was := s.launcherAgentOn(ctx, m, from); was.agent != agent.agent || was.allowAll() {
				t.Permission = ""
			}
			t.normalizeReadonlyPermission(readonlyModeOf(s.settings(ctx)))
			if t.Permission == "" && modes {
				t.Permission = s.taskPermission(ctx, t.Machine, nil)
			}
		}
	}
	t.RetryWith = nil
	if name != from {
		t.noteWhy(i18n.T(s.lang(ctx), "assistant-tasks.retried_on_launcher", i18n.Args{"to": name, "from": from, "machine": machine}))
		s.logf("task %s: retrying its start on %s with launcher %s instead of %s", t.TodoID, m.ID, name, from)
	}
	return nil
}

// launcherAgentOn is what launcher name runs on computer m, as fitLauncher reads it for a start; nothing when it
// can't be told (the computer does not have it).
func (s *Service) launcherAgentOn(ctx context.Context, m Device, name string) startAgent {
	_, agent, err := s.fitLauncher(ctx, m, startPick{launcher: name}, map[string]any{"launcher": name})
	if err != nil {
		return startAgent{}
	}
	return agent
}
