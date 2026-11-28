package node

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// Agents other than Claude Code that Persona starts without Claude's flags: a compatible CLI (on the PATH as traex), and a
// launcher the hub says runs Codex or a compatible CLI (hub.json machines[].launchers, a wrapper the login shell's list does not
// show as one). A Codex launcher the login shell lists starts through codex_start.go instead.

// agentLauncher is a launcher as launchers.list answers: with the agent it starts, for hubs that read agent_type.
type agentLauncher struct {
	proto.Launcher
	AgentType string `json:"agent_type,omitempty"`
}

func ompOptionsRefusal(ctx context.Context) error {
	return refusal("omp_options", i18n.T(i18n.From(ctx), "rpc-errors.omp_options"), nil)
}

func ompStartModelHost(kind string) bool {
	return kind == proto.EngineHerdr || kind == proto.EngineOrca
}

// Both the model picker and the start verify that this launcher runs omp and forwards the requested model.
func (n *Node) checkOmpStartModel(ctx context.Context, launcher, model string) error {
	if launcher != ompAgent || !ompStartModelHost(n.startKind(ctx)) {
		return ompOptionsRefusal(ctx)
	}
	if _, managed := n.managedLauncher(launcher); managed || n.runtimeOfIn(ctx, launcher) != nil {
		return ompOptionsRefusal(ctx)
	}
	agent, err := n.launcherModelCheck(ctx, launcher, model)
	if err == nil && agent != ompAgent {
		return ompOptionsRefusal(ctx)
	}
	return err
}

// launcherAgentType is the agent a launcher starts when nothing else says: pi, the Cursor CLI and the Antigravity CLI
// by name (workagent.go), codex, traex and omp by name, else claude.
func launcherAgentType(name string) string {
	if _, _, ok := workAgent(name); ok {
		return name
	}
	if name == "codex" || name == traexAgent || name == ompAgent {
		return name
	}
	return "claude"
}

// Managed wrappers need not be named after the CLI they run. An explicit type wins.
func (n *Node) startAgentType(launcher, declared string) string {
	if declared != "" {
		return declared
	}
	if l, ok := n.managedLauncher(launcher); ok && l.AgentType() == traexAgent {
		return traexAgent
	}
	if kind := n.agentKindOf(launcher, ""); kind == agyAgent || kind == traexAgent {
		return kind
	}
	return launcherAgentType(launcher)
}

func typedLaunchers(list []proto.Launcher) []agentLauncher {
	out := make([]agentLauncher, 0, len(list))
	seen := map[string]bool{}
	for _, l := range list {
		if !seen[l.Name] {
			t := l.Agent
			if t == "" {
				t = launcherAgentType(l.Name)
			}
			out = append(out, agentLauncher{Launcher: l, AgentType: t})
			seen[l.Name] = true
		}
	}
	return out
}

// otherAgentCommand is the launcher's command line, with the first message as its argument (typedArg), for a pane whose
// shell is shells.
func otherAgentCommand(launcher, resume, prompt string, shells ...string) string {
	cmd := launcher
	if resume != "" {
		cmd += " resume " + shellQuote(resume)
	}
	if prompt != "" {
		cmd += " " + shellQuote(typedArg(prompt, shells...))
	}
	return cmd
}

// typedArg is text to quote on a command line that may be typed into the shell (typedstart.go), whose line editor acts
// on keys inside quotes too: its controls inert (^U or ^C would edit the line), a tab as a space (it completes the
// quoted word, closes the quote and leaves the rest to the shell), and outside the sh family (takesAssign) a line
// break as a space (csh ends the command at one, quote open or not).
func typedArg(text string, shells ...string) string {
	text = strings.ReplaceAll(inertControls(text), "\t", " ")
	if !takesAssign(startCommandShells(shells)...) {
		text = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(text)
	}
	return text
}

// startOtherAgent opens a new tab in ws (a new space when ""), runs the launcher there (resuming a session when
// resume is set, with the first message as its argument) and waits until the host reports an agent of that kind in
// the tab with a session id that was not running before (or the one resumed).
func (n *Node) startOtherAgent(ctx context.Context, launch host.Launcher, cwd, launcher, kind, prompt, resume, ws, tabName string, options ...modelExecution) (*proto.Session, error) {
	created, err := n.newTab(ctx, launch, cwd, ws, tabName, resume) // retire.go
	if err != nil {
		return nil, err
	}
	pane := created.Pane.String()
	// Exclude existing sessions when the normal scan has to infer a Codex rollout.
	n.mu.Lock()
	existing := make(map[string]bool, len(n.agents))
	for id := range n.agents {
		existing[id] = true
	}
	n.mu.Unlock()
	if kind == traexAgent && resume == "" {
		// The host may report an old conversation before the new process claims its own.
		for id := range n.traexThreads() {
			existing[id] = true
		}
		for _, r := range rollouts(n.traexHome()) {
			existing[r.id] = true
		}
	}
	if err := n.settleShell(ctx, pane); err != nil { // the fresh shell at rest first (shellsettle.go, #2570)
		return nil, n.startTabFailed(ctx, launch, created, ws, resume, err)
	}
	// A Codex wrapper is told to herdr as Codex (hostherdr.go); a compatible CLI is herdr's to detect. The node's
	// directory in front, the assistant's environment left out (agentenv.go).
	shells := n.startPaneShells(ctx, launch, created)
	// The first message keeps its line breaks only in a start file: typed out, the line may meet csh, which runs each
	// line on its own (typedArg), and Orca does not say which shell it opened.
	argShells := shells
	if !n.typesStartFile(launch, shells...) {
		argShells = nil
	}
	command := n.launcherCommand(launcher)
	var model, effort string
	execution := executionOption(options)
	if kind == traexAgent && resume == "" {
		model, effort = execution.Model, execution.Effort
		if model != "" {
			command += " -m " + shellQuote(model)
		}
		if effort != "" {
			command += " -c " + shellQuote("model_reasoning_effort="+effort)
		}
	} else if kind == ompAgent && resume == "" && execution.Model != "" {
		model = execution.Model
		command += " --model " + shellQuote(model)
	}
	if model != "" || effort != "" {
		if prompt != "" {
			command += " --" // The first message is positional text, even when it begins with a dash.
		}
	}
	cmd := withHerdrAgent(typedHerdrAgent(launch, kind, shells...), otherAgentCommand(command, resume, prompt, argShells...))
	// Not in the agent sandbox (agentsandbox.go).
	n.notePaneStart(launcher)
	done, err := n.runAgentCommand(ctx, launch, created, ws, "", n.agentStartCommand(cmd, shellQuote, shells...), shells...) // typedstart.go
	if err != nil {
		return nil, err
	}
	defer done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var grace time.Time // when a family start gives up on the extension reporting the session (pifamily.go)
	answered := ""      // the last dialog screen answered, not answered again while it stays drawn
	dialogLooks := 0
	for {
		select {
		case <-ctx.Done():
			shown, _ := n.readPane(context.Background(), pane, "recent-unwrapped", 30)
			if strings.TrimSpace(shown) == "" { // A dialog at the top of a tall pane leaves the recent rows blank (#2351).
				shown, _ = n.readPane(context.Background(), pane, "visible", 30)
			}
			return nil, fmt.Errorf("wait for %s session: %w\n%s", kind, ctx.Err(), lastLines(shown, 12))
		case <-ticker.C:
		}
		if err := n.refreshHosts(ctx); err != nil {
			continue
		}
		confirmed := ""
		if kind == traexAgent {
			confirmed = n.traexLiveThread(pane, resume)
		}
		n.mu.Lock()
		var id string
		var agent host.Agent
		for candidate, a := range n.agents {
			if a.PaneID == pane && a.Agent == kind && candidate != "" &&
				(kind != traexAgent || candidate == confirmed) &&
				((resume == "" && !existing[candidate]) || candidate == resume) {
				id, agent = candidate, a
				break
			}
		}
		n.mu.Unlock()
		if id == "" {
			if kind != traexAgent || resume != "" || confirmed != "" {
				continue
			}
			// TraeX waits for folder access before it creates a writer. Only answer that startup question.
			text, err := n.readPane(ctx, pane, "visible", screen.DialogLines)
			d := screen.ParseDialog(text)
			if err != nil || d == nil || len(d.Options) < 2 {
				dialogLooks = 0
				continue
			}
			var keys []string
			if strings.HasPrefix(d.Question, "Do you trust the contents of this directory?") {
				_, keys = matchStartDialog(codexStartDialogs, text, false)
			}
			if keys != nil {
				dialogLooks = 0
				if text != answered {
					if err := n.sendKeys(ctx, pane, keys...); err != nil {
						return nil, n.startTabFailed(ctx, launch, created, ws, resume, err)
					}
					answered = text
				}
				continue
			}
			dialogLooks++
			if dialogLooks >= unknownDialogLooks {
				return nil, n.startTabFailed(ctx, launch, created, ws, resume, unknownDialogError(d))
			}
			continue
		}
		if piFamily(kind) && id == otherSession(agent) {
			// The host knows the pane but not its session yet: the extension may report it in a moment.
			if grace.IsZero() {
				grace = time.Now().Add(piFamilyIDGrace)
			}
			if time.Now().Before(grace) {
				continue
			}
		}
		st := sessionState{Pane: pane, Launcher: launcher, Cwd: cwd, Model: model, ModelEffort: effort, StartedAt: time.Now().UTC()}
		if resume != "" {
			if previous, ok := n.state.get(id); ok {
				if kind == traexAgent {
					model, effort = previous.Model, previous.ModelEffort
					st.Model, st.ModelEffort = model, effort
				}
				st.Cwd = keepSpaceCwd(previous.Cwd, st.Cwd)
			}
		}
		n.putAgentState(ctx, launch, id, st)
		if effort != "" {
			n.noteModes(id, "", effort)
		}
		n.bindPane(pane, id)
		session := n.sessionFor(id, agent)
		session.Agent = kind
		n.holdResume(ctx, launch, pane, id, agentLaunch{Agent: kind, Launcher: launcher, ID: id, Model: model, Effort: effort})
		return session, nil
	}
}
