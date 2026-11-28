package node

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/modelhttp"
)

// When herdr's server restarts it restores its panes and, once a client attaches, resumes the agents in them with its
// built-in command: a bare `claude --resume <id>` (session.resume_agents_on_restore, on by default). For a Claude
// Persona started that drops everything Persona started it with — the launcher (a gateway's credentials: the
// session silently moved to the user's own subscription), --settings, --mcp-config, Persona's hooks — and a session
// without a transcript stopped at Claude's login screen (2026-11-04, #1879).
//
// herdr v0.9.5 has no per-pane switch to turn its resume off, but it takes a per-pane resume command: when herdr
// detects the agent process in the pane (or a reporter claims an unrecognized one), pane.report_agent_session may
// attach resume_argv, and herdr runs that instead of its built-in one, only for that pane — without holding lifecycle
// authority on panes herdr detects itself, so herdr's screen and OSC detection still decides agent_status (#4657).
// Every Agent Persona starts in herdr gets `eval "<the command Persona starts it with, --resume <id>>"`, typed into
// the restored pane's shell like herdr's own (so ~/.zshrc launchers work). Agents that the user starts in a terminal
// are never touched: herdr resumes them as before. The user's herdr config is not changed. herdr keeps the command
// across later session reports with another id (checked against v0.9.5), so a Claude that moved to a new session
// (/clear) is reported again from its SessionStart hook. herdr before 0.9.2 answers the report ok and drops
// resume_argv: on such a server nothing is reported, the node says once why, and a new server (an upgrade) is told
// (#11792). An Agent such a restart resumed bare is listed off its launcher (offlauncher.go).

// resumeSource is the reporter Persona sends resume_argv as (a custom source: herdr's own integration keeps its own).
const resumeSource = "custom:persona"

// agentLaunch is what an agent in a pane was started with, to start it again the same way.
type agentLaunch struct {
	Source modelhttp.Source `json:"model_source,omitzero"`

	AdditionalDirs                   []string `json:"additional_dirs,omitempty"`
	TaskInstructionFile              string
	TaskInstructions                 string
	Agent                            string // "claude" (empty means claude), "codex", "traex", "pi"
	Launcher, Settings, Hooks, Model string
	managed                          bool   // a refresh must run through personad launch even if a later catalog omits it
	Effort                           string // a work agent's explicit effort, empty for its default
	Mode                             string // an agy's permission mode (agyModeFlags), empty in a record from before it
	Display                          string // the live replies plugin's directory, "" for none (liveRepliesPlugin)
	Plugins                          bool   // claude loads plugins: Persona's plugin directory and office rule
	ID                               string
	AgyConversation                  string // native ID verified for this pane, separate from the Persona row ID
	// Server is the herdr server instance (herdr.Client.ServerID) that last took the resume command, or that was
	// found too old to keep one (herdrDropsResume); "" before. A pane whose server changed since (a restart, a live
	// handoff, an upgrade from a herdr older than 0.9.2, which ignored resume_argv) is reported again by
	// backfillResume.
	Server string
}

type claudeLaunch = agentLaunch

// claudeCommandLine is the command that starts Claude for spec with args (--session-id / --resume …, mode flags),
// quoting with q.
func (n *Node) claudeCommandLine(spec claudeLaunch, args string, q func(string) string) string {
	launcher := n.launcherCommandQ(spec.Launcher, q)
	if spec.managed {
		launcher = q(n.bin) + " launch " + spec.Launcher
	}
	cmd := launcher + " " + args
	for _, dir := range spec.AdditionalDirs {
		cmd += " --add-dir " + q(dir)
	}
	if spec.Settings != "" {
		cmd += " --settings " + q(spec.Settings)
	}
	if spec.TaskInstructionFile != "" {
		cmd += " --append-system-prompt-file " + q(spec.TaskInstructionFile)
	}
	cmd += " --mcp-config " + q(n.agentMCPConfig()) + " --allowedTools " + q(n.agentAllowedTools(spec.Plugins))
	if spec.Model != "" {
		cmd += " --model " + q(spec.Model)
	}
	if spec.Plugins {
		cmd += " --plugin-dir " + q(n.pluginDir())
	}
	if spec.Hooks != "" {
		cmd += " --plugin-dir " + q(spec.Hooks)
	}
	if spec.Display != "" {
		cmd += " --plugin-dir " + q(spec.Display)
	}
	return cmd
}

// dquote quotes s for a POSIX shell without apostrophes (herdr refuses them in resume_argv): as is when it needs no
// quoting, else in double quotes with \, ", $ and ` escaped.
func dquote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '/' || r == '.' || r == '-' || r == '_' || r == ':' || r == '=' || r == '+' || r == ',' ||
			r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) < 0 {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")
	return `"` + r.Replace(s) + `"`
}

// claudeResumeArgv is the resume_argv of a Claude Persona started: eval of its start command with --resume id, herdr
// told it is Claude as at the start (resumeHerdrAgent), with the node's directory and without the assistant's
// environment (agentenv.go). ok is false when the command cannot be said without an apostrophe (a path with one in
// it): herdr would refuse it.
func (n *Node) claudeResumeArgv(spec claudeLaunch, id string) (argv []string, ok bool) {
	if validModes("", spec.Effort) != nil {
		return nil, false
	}
	cmd := withHerdrAgent(resumeHerdrAgent("claude"), n.claudeCommandLine(spec, "--resume "+id+modeFlags("", spec.Effort), dquote))
	cmd = n.agentCommand(cmd, dquote, herdrPaneShell())
	if !resumeSayable(cmd) || !validID(id) {
		return nil, false
	}
	return []string{"eval", cmd}, true
}

// maxResumeArgv is herdr's limit on a resume_argv's total length (agent_resume.rs, v0.9.5).
const maxResumeArgv = 8192

// resumeArgvMinHerdr is the first herdr that keeps a reported resume_argv. An older server answers the report ok and
// drops the field, so its restart resumes the agent with herdr's bare `claude --resume <id>` (#11792).
var resumeArgvMinHerdr = [3]int{0, 9, 2}

// herdrDropsResume: v, the version a herdr server answers ping with, is older than resumeArgvMinHerdr. A server that
// names no version is not taken to drop it: its command is reported as before.
func herdrDropsResume(v string) bool {
	return versionAtLeast(v, [3]int{}) && !versionAtLeast(v, resumeArgvMinHerdr)
}

// warnDroppedResume says once per herdr server (its ServerID) that it drops resume commands, which only an upgrade of
// herdr on this computer changes.
func (n *Node) warnDroppedResume(server, version string) {
	n.mu.Lock()
	seen := n.droppedResume[server]
	if n.droppedResume == nil {
		n.droppedResume = map[string]bool{}
	}
	n.droppedResume[server] = true
	n.mu.Unlock()
	if !seen {
		n.logf("herdr resume: herdr %s drops resume commands (they need herdr %d.%d.%d or later): after a herdr restart the agents "+
			"Persona started in it resume bare (claude --resume <id>), off their launchers, until herdr is upgraded",
			version, resumeArgvMinHerdr[0], resumeArgvMinHerdr[1], resumeArgvMinHerdr[2])
	}
}

// resumeSayable reports whether herdr takes cmd as eval's argument: no apostrophe, no control character (both make
// herdr answer invalid_resume_argv) and within herdr's length limit, counting "eval".
func resumeSayable(cmd string) bool {
	return !strings.Contains(cmd, "'") && len("eval")+len(cmd) <= maxResumeArgv &&
		strings.IndexFunc(cmd, unicode.IsControl) < 0
}

// Keep alias credentials out of herdr's persisted command. A restore reads the
// launcher's words again and hands them to the same resolver as a file-based start.
func (n *Node) herdrCodexLauncher(name string) string {
	launcherCache.Lock()
	words := launcherCache.codex[name]
	launcherCache.Unlock()
	if len(words) == 0 {
		if _, managed := n.managedLauncher(name); managed {
			return dquote(n.bin) + " launch " + dquote(name)
		}
		if name == "codex" {
			return "codex" // codex-start resolves the stock CLI even before discovery
		}
	}
	if name == "codex" && len(words) == 1 && (words[0] == "codex" || filepath.IsAbs(words[0])) {
		return dquote(words[0])
	}
	// Rediscover custom launcher words in the marked shell on restore.
	return "--launcher " + dquote(name)
}

func herdrCodexRestore(args []string) (codexStartFile, error) {
	if len(args) >= 2 && args[0] == "--launcher" {
		name := args[1]
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		env := append(codexPaneEnv(&herdrHost{}), codexStartMarker)
		out, err := scanLaunchers(ctx, env...)
		if err != nil {
			return codexStartFile{}, err
		}
		_, launchers, _, _ := mergeLaunchers(string(out))
		words := launchers[name]
		if len(words) == 0 {
			if name != "codex" {
				return codexStartFile{}, fmt.Errorf("%s Codex launcher %q not found", codexStartError, name)
			}
			words = []string{"codex"} // a function or PATH entry only the marked shell sees
		}
		args = append(words, args[2:]...)
	}
	return codexStartFile{Argv: args, Agent: herdrAgentName("codex")}, nil
}

// agentResumeArgv is the resume_argv for an agent Persona started (Claude, Codex, a compatible CLI, pi, the Cursor
// CLI, the Antigravity CLI). A Codex, and a work agent herdr detects, gets HERDR_AGENT like Claude. A work agent's
// command keeps its model and effort overrides. Pi names its session, omp requires its session file, and agy adds a
// verified conversation ID when available.
func (n *Node) agentResumeArgv(spec agentLaunch, id string) (argv []string, ok bool) {
	kind := spec.Agent
	if kind == "" || kind == "claude" {
		return n.claudeResumeArgv(spec, id)
	}
	if kind == ompAgent { // omp resumes a session file, never a pane or an id (ompresume.go)
		file := n.ompKnownFile(id)
		if file == "" {
			return nil, false
		}
		cmd := n.launcherCommandQ(spec.Launcher, dquote) + " --resume " + dquote(file)
		cmd = n.agentCommand(cmd, dquote, herdrPaneShell())
		if !resumeSayable(cmd) {
			return nil, false
		}
		return []string{"eval", cmd}, true
	}
	var cmd string
	if kind == "codex" {
		cmd = dquote(n.bin) + " codex-start --herdr-resume " + n.herdrCodexLauncher(spec.Launcher) + codexTaskFlags(spec.TaskInstructions) + " resume " + id
	} else if _, program, ok := workAgent(kind); ok {
		launcher := spec.Launcher
		resumeID := id
		model := spec.Model
		if kind == agyAgent {
			launcher = n.launcherCommandQ(launcher, dquote)
			resumeID = spec.AgyConversation
			model = agyEffortModel(model, spec.Effort)
		}
		cmd = withHerdrAgent(resumeHerdrAgent(kind), workAgentResume(kind, workAgentCommand(launcher, program, model, spec.Effort, spec.Mode), resumeID))
	} else {
		cmd = withHerdrAgent(resumeHerdrAgent(kind), n.launcherCommandQ(spec.Launcher, dquote)+codexTaskFlags(spec.TaskInstructions)+" resume "+id)
	}
	cmd = n.agentCommand(cmd, dquote, herdrPaneShell())
	if !resumeSayable(cmd) || (!validID(id) && !runtimeIDRe.MatchString(id)) {
		return nil, false
	}
	return []string{"eval", cmd}, true
}

// resumeHerdrAgent is the HERDR_AGENT in front of a resume_argv (hostherdr.go): herdr types the argv into the shell it
// opens the restored pane with (herdrPaneShell), so only when that shell takes `VAR=val cmd`.
func resumeHerdrAgent(kind string) string {
	if !takesAssign(herdrPaneShell()) {
		return ""
	}
	return herdrAgentName(kind)
}

// holdResume gives herdr the pane's resume command and remembers how the pane's Claude was started, for a later
// SessionStart with another session id. Hosts other than herdr: nothing to do. Failures are logged: the start
// itself went through.
func (n *Node) holdResume(ctx context.Context, launch host.Launcher, pane, id string, spec claudeLaunch) {
	h, ok := launch.(*herdrHost)
	if !ok || h.c == nil {
		return
	}
	spec.ID = id
	var agyConv string
	if spec.Agent == agyAgent {
		n.ensureAgyHooksForLaunch() // agyhooks.go (#13950): ensure hooks exist for resumed agy panes
		agyConv = n.agyConversation(id, pane)
	}
	n.mu.Lock()
	if spec.Agent == agyAgent {
		spec.AgyConversation = agyConv
	}
	if n.launches == nil {
		n.launches = map[string]claudeLaunch{}
	}
	n.launches[pane] = spec
	n.mu.Unlock()
	n.reportResume(ctx, h, pane, spec)
}

// reportResume gives herdr the resume command of the agent Persona started in pane.
//
// A pane's reports take turns, and each one says what n.launches holds for the pane when its turn comes (spec only
// while the pane has nothing there). Every change to n.launches is followed by a report, so the last one herdr gets is
// the newest command: two in flight at once (the live replies setting turned off and on again quickly, a SessionStart
// meanwhile) would otherwise reach herdr in either order, and the older one, arriving last, would stay until the next
// change.
func (n *Node) reportResume(ctx context.Context, h *herdrHost, pane string, spec claudeLaunch) {
	defer n.resumes.lock(pane)()
	n.mu.Lock()
	if cur, ok := n.launches[pane]; ok {
		spec = cur
	}
	n.mu.Unlock()
	reportID := spec.ID
	if spec.Agent == agyAgent {
		spec.AgyConversation = n.agyConversation(spec.ID, pane)
		if spec.AgyConversation != "" {
			reportID = spec.AgyConversation
		}
		n.mu.Lock()
		if cur, ok := n.launches[pane]; ok && cur.ID == spec.ID {
			// What is reported is what the pane resumes: kept, so the server is recorded below and backfill does not
			// report the same conversation again.
			cur.AgyConversation = spec.AgyConversation
			n.launches[pane] = cur
		}
		n.mu.Unlock()
		if spec.AgyConversation != "" {
			n.noteAgyConversationMode(spec.ID, spec.AgyConversation, spec.Mode)
		}
	}
	argv, ok := n.agentResumeArgv(spec, spec.ID)
	if !ok {
		if spec.Agent == ompAgent && n.ompKnownFile(spec.ID) == "" {
			n.logf("herdr resume: %s (%s) is an omp with no known session file: no resume command reported", pane, spec.ID)
		} else {
			n.logf("herdr resume: %s (%s) not given a resume command (herdr would refuse it: an apostrophe, a control character or too long)", pane, spec.ID)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	agentName := spec.Agent
	if agentName == "" {
		agentName = "claude"
	}
	server := h.serverID(ctx, host.ParsePane(pane)) // before the report: a server that replaces this one meanwhile is newer
	settled := func() {
		n.mu.Lock()
		if cur, ok := n.launches[pane]; ok && cur.ID == spec.ID && cur.AgyConversation == spec.AgyConversation {
			cur.Server = server
			n.launches[pane] = cur
		}
		n.mu.Unlock()
	}
	if v := h.serverVersion(ctx, host.ParsePane(pane)); herdrDropsResume(v) {
		// It would answer ok and keep nothing. Settled against this server all the same, so the server an upgrade
		// brings (a new one) is given the command (backfillResume).
		n.warnDroppedResume(server, v)
		settled()
		return
	}
	if err := h.PaneReportResume(ctx, host.ParsePane(pane), resumeSource, agentName, reportID, argv); err != nil {
		n.logf("herdr resume: %s (%s): %v", pane, spec.ID, err)
		return
	}
	settled()
}

// resumeOnSessionStart follows a Persona-started Claude to its new session (/clear, /resume in it): herdr would
// otherwise resume the old one after a restart. Panes Persona did not start, and the same session again: nothing.
func (n *Node) resumeOnSessionStart(pane, id string) {
	if pane == "" || !validID(id) {
		return
	}
	n.mu.Lock()
	spec, ok := n.launches[pane]
	moved := ok && spec.ID != id
	if moved {
		spec.ID = id
		n.launches[pane] = spec
	}
	n.mu.Unlock()
	if !moved {
		return
	}
	h, isHerdr := n.hosts.Get(host.Herdr).(*herdrHost)
	if !isHerdr || h.c == nil || host.ParsePane(pane).Kind != host.Herdr {
		return
	}
	go n.reportResume(context.Background(), h, pane, spec)
}

// backfillResume gives herdr the resume command of Persona-started agents found running in herdr that have none from
// this node yet: ones started by an older node, or before this node restarted (n.launches is memory only). Once
// per pane and session, and once more each time the pane's herdr server is a new one (herdr.Client.ServerID): on a
// user's machine herdr 0.9.0 ignored every resume_argv, an upgrade to 0.9.5 by live handoff carried nothing over,
// and the node, sure it had reported, let the next restart resume the Agents bare. The work (settings paths, the reports)
// runs in the background, off the refresh.
func (n *Node) backfillResume(agents []host.Agent) {
	h, ok := n.hosts.Get(host.Herdr).(*herdrHost)
	if !ok || h.c == nil {
		return
	}
	type todo struct {
		pane string
		id   string
		st   sessionState
		kind string
		agy  string
	}
	type held struct{ pane, server string }
	var work []todo
	var recheck []held
	var reports []string
	agyByPane := map[string]string{}
	for _, a := range agents {
		if a.Agent == agyAgent && a.PaneID != "" && host.ParsePane(a.PaneID).Kind == host.Herdr {
			agyByPane[a.PaneID] = n.agyConversation(otherSession(a), a.PaneID)
		}
	}
	n.mu.Lock()
	for _, a := range agents {
		if a.PaneID == "" || host.ParsePane(a.PaneID).Kind != host.Herdr {
			continue
		}
		id := n.panes[a.PaneID]
		if id == "" {
			id = a.SessionID()
		}
		var agy string
		if a.Agent == agyAgent {
			id = otherSession(a)
			agy = agyByPane[a.PaneID]
		} else if !validID(id) {
			continue
		}
		if spec, ok := n.launches[a.PaneID]; ok && spec.ID == id {
			if spec.Agent == agyAgent && spec.AgyConversation != agy {
				spec.AgyConversation, spec.Server = agy, ""
				n.launches[a.PaneID] = spec
				reports = append(reports, a.PaneID)
				continue
			}
			if spec.Server != "" {
				recheck = append(recheck, held{a.PaneID, spec.Server})
			}
			continue
		}
		st, ok := n.state.get(id)
		if !ok || st.Launcher == "" {
			continue // not started by Persona (the user's own agent: herdr resumes it as before)
		}
		kind := a.Agent
		if kind == "" {
			kind = launcherAgentType(st.Launcher)
		}
		if kind == "" {
			kind = "claude"
		}
		if n.launches == nil {
			n.launches = map[string]agentLaunch{}
		}
		n.launches[a.PaneID] = agentLaunch{Agent: kind, Launcher: st.Launcher, Model: st.Model, Effort: st.ModelEffort, Mode: agyBackfillMode(kind, st.AppMode), ID: id, AgyConversation: agy, TaskInstructions: codexKeptTaskInstructions(st), TaskInstructionFile: st.TaskInstructionFile} // claimed: once
		work = append(work, todo{a.PaneID, id, st, kind, agy})
	}
	n.mu.Unlock()
	var moved []string
	if len(recheck) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		for _, r := range recheck {
			if s := h.serverID(ctx, host.ParsePane(r.pane)); s != "" && s != r.server {
				moved = append(moved, r.pane)
			}
		}
		cancel()
		n.mu.Lock()
		for _, pane := range moved { // claimed: a refresh while the report is out does not send another
			if cur, ok := n.launches[pane]; ok {
				cur.Server = ""
				n.launches[pane] = cur
			}
		}
		n.mu.Unlock()
	}
	if len(work) == 0 && len(moved) == 0 && len(reports) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for i, pane := range append(moved, reports...) {
			n.mu.Lock()
			spec, ok := n.launches[pane]
			n.mu.Unlock()
			if ok {
				if i < len(moved) {
					n.logf("herdr resume: %s (%s) is on a new herdr server: its resume command again", pane, spec.ID)
				}
				n.reportResume(ctx, h, pane, spec)
			}
		}
		for _, w := range work {
			var spec agentLaunch
			if w.kind == "claude" || w.kind == "" {
				settings, hooks := n.claudeStartSettings(ctx, w.st.Launcher, "claude", w.st.Cwd)
				spec = agentLaunch{Agent: w.kind, Launcher: w.st.Launcher, Settings: settings, Hooks: hooks, Display: n.liveRepliesPlugin(hooks),
					TaskInstructionFile: w.st.TaskInstructionFile, Model: n.claudeModelArg(w.st.Launcher, w.st.Model), Effort: w.st.ModelEffort, Plugins: n.claudeLoadsPlugins(ctx), ID: w.id}
			} else {
				spec = agentLaunch{Agent: w.kind, Launcher: w.st.Launcher, Model: w.st.Model, Effort: w.st.ModelEffort, Mode: agyBackfillMode(w.kind, w.st.AppMode), ID: w.id, AgyConversation: w.agy, TaskInstructions: codexKeptTaskInstructions(w.st)}
			}
			n.mu.Lock()
			if cur, ok := n.launches[w.pane]; !ok || cur.ID != w.id || cur.AgyConversation != w.agy {
				n.mu.Unlock()
				continue // moved on meanwhile (a start, a SessionStart): that one reported
			}
			n.launches[w.pane] = spec
			n.mu.Unlock()
			n.reportResume(ctx, h, w.pane, spec)
		}
	}()
}

// agyBackfillMode returns the persisted permission mode to restore into an agy pane's resume_argv across node
// restarts; non-agy agents keep Mode empty and legacy agy records with empty AppMode stay empty (no escalation).
func agyBackfillMode(kind, mode string) string {
	if kind != agyAgent {
		return ""
	}
	return mode
}

// noteAgyConversationMode mirrors a Persona-started agy pane's permission mode to its verified companion row ID
// (agy-<uuid>) so if the pane later stops or is not restored across a reboot, continuing the conversation through
// the companion keeps the same permission mode instead of falling back to local settings.json.
func (n *Node) noteAgyConversationMode(paneSessionID, conversation, mode string) {
	if n.state == nil || conversation == "" || mode == "" {
		return
	}
	st, ok := n.state.get(paneSessionID)
	if !ok || st.Launcher == "" || st.AppMode == "" {
		return
	}
	convID := agyAgent + "-" + conversation
	if _, exists := n.state.get(convID); exists {
		return
	}
	n.state.put(convID, sessionState{
		Launcher:    st.Launcher,
		Cwd:         st.Cwd,
		Model:       st.Model,
		ModelEffort: st.ModelEffort,
		AppMode:     mode,
		StartedAt:   st.StartedAt,
	})
}

// resumeLocks makes the resume reports of one herdr pane take turns (reportResume).
type resumeLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// lock waits for the pane's report in flight, if any, and returns the unlock.
func (r *resumeLocks) lock(pane string) func() {
	r.mu.Lock()
	if r.locks == nil {
		r.locks = map[string]*sync.Mutex{}
	}
	l := r.locks[pane]
	if l == nil {
		l = &sync.Mutex{}
		r.locks[pane] = l
	}
	r.mu.Unlock()
	l.Lock()
	return l.Unlock
}
