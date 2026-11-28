package node

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
	"github.com/nwverse/persona/server/internal/shellcmd"
)

// Starting a Codex from the phone (docs/integrations/codex.md "Starting from the phone"). A Codex launcher (plain `codex`, or an alias whose
// command is codex) runs in a new herdr tab with an optional first message as its prompt argument: Codex makes the thread
// itself — on the shared app-server daemon for a plain 0.163, where #220 joins it (codex_daemon.go); in its own process
// for 0.153 or a launcher with -c, on the terminal route. Launcher words (an alias can carry a key) stay in a private
// file: the tab runs `personad codex-start <file>`. The helper passes the message as one native argv value after
// "--" on every OS. Windows terminal input is a character stream and cannot carry a long first message reliably;
// no prompt is typed through it, and no shell parses the native argument. A start with a message answers once the new thread is known — the rollout in that directory, begun after the
// launch, whose first message is exactly this one — with that thread's id, so the phone opens the session that is
// there; it fails, with what the tab shows, when none comes in time. Without a message it returns a waiting row (codex_waiting.go).

// PERSONA_CODEX_START_WAIT is a positive Go duration such as "90s" or "4m" on the node.
const codexStartWaitEnv = "PERSONA_CODEX_START_WAIT"
const defaultCodexStartWait = 180 * time.Second

// Fresh terminal starts wait for the composer or for the first message to appear in the thread.
var codexStartWait = codexStartWaitFromEnv()

func codexStartWaitFromEnv() time.Duration {
	value := strings.TrimSpace(os.Getenv(codexStartWaitEnv))
	if value != "" {
		wait, err := time.ParseDuration(value)
		if err == nil && wait > 0 {
			return wait
		}
		log.Printf("%s must be a positive duration; using %s", codexStartWaitEnv, defaultCodexStartWait)
	}
	return defaultCodexStartWait
}

// codexArgv is how a Codex launcher runs: plain codex, or an alias's words; ok false when the launcher is not Codex's.
func (n *Node) codexArgv(launcher string) ([]string, bool) {
	launcherCache.Lock()
	words, ok := launcherCache.codex[launcher]
	launcherCache.Unlock()
	if ok {
		return append([]string(nil), words...), true
	}
	if m, ok := n.managedLauncher(launcher); ok && m.AgentType() == "codex" {
		return []string{n.bin, "launch", launcher}, true
	}
	if launcher == "codex" {
		return []string{"codex"}, true
	}
	return nil, false
}

// codexLauncher is codexArgv for a start: the node reads its launcher list first when it has not since it started
// (the app keeps the list it got across a node restart, so a start can come before any launchers.list).
func (n *Node) codexLauncher(ctx context.Context, launcher string) ([]string, bool) {
	launcherCache.Lock()
	loaded := !launcherCache.at.IsZero()
	launcherCache.Unlock()
	if !loaded && launcher != "claude" {
		_, _ = n.launchers(ctx)
	}
	return n.codexArgv(launcher)
}

// codexFunctionLauncher refuses a launcher that is a shell function running Codex (what the launcher probe found):
// from the phone a Codex starts only as plain codex or an alias running it, whose words codex-start can run without a
// shell; as a Claude it would only fail. nil for any other launcher. probe false: only what an earlier probe found
// (a probe sources ~/.zshrc, and startAsync answers at once; the start after it probes).
func (n *Node) codexFunctionLauncher(ctx context.Context, launcher string, probe bool) error {
	if launcher == "claude" {
		return nil
	}
	if _, ok := n.codexArgv(launcher); ok {
		return nil
	}
	var d proto.LauncherDetail
	if probe {
		d = n.launcherProbeCached(ctx, launcher)
	} else {
		probeCache.Lock()
		if at, ok := probeCache.at[launcher]; ok && time.Since(at) < 10*time.Minute {
			d = probeCache.got[launcher]
		}
		probeCache.Unlock()
	}
	if d.Agent == "codex" {
		return refusal("codex_launcher", "启动器「"+launcher+"」是个跑 Codex 的 shell 函数：从手机新建 Codex 会话只支持 codex 本身和直接跑 codex 的 alias，换一个启动器再试。", nil)
	}
	return nil
}

// codexConfigFlags are the flags that make a 0.163 Codex run its thread in its own process instead of on the shared
// daemon (codex-rs tui daemon_startup.rs).
var codexConfigFlags = []string{"-c", "--config", "-p", "--profile", "--enable", "--disable", "--oss", "--strict-config", "--no-daemon"}

// codexOwnProcess names the launcher flag that keeps a Codex off the shared app-server, "" for none (and for a
// launcher on an app-server of its own, --remote, whatever else it says).
func codexOwnProcess(argv []string) string {
	if codexOnRemote(argv) {
		return ""
	}
	for _, w := range argv {
		flag, _, _ := strings.Cut(w, "=")
		for _, f := range codexConfigFlags {
			if flag == f {
				return f
			}
		}
	}
	return ""
}

// A managed launcher's -- can precede every caller argument. Defer injection until Launch has assembled its argv.
const codexBypassEnv = "PERSONA_CODEX_BYPASS_PERMISSIONS"

func codexBypassArgs(argv []string) []string {
	end := slices.Index(argv, "--")
	if end < 0 {
		end = len(argv)
	}
	for i := 0; i < end; i++ {
		if argv[i] == "--dangerously-bypass-approvals-and-sandbox" || argv[i] == "--yolo" {
			return argv
		}
		if codexValueFlags[argv[i]] {
			i++
		}
	}
	return slices.Insert(slices.Clone(argv), end, "--dangerously-bypass-approvals-and-sandbox")
}

// codexBypassArgv is launcher's words argv with the flag that runs Codex without approvals or sandbox, for a start in
// bypassPermissions: a managed launcher whose own arguments end in "--" gets it through Launch (codexBypassEnv).
func (n *Node) codexBypassArgv(launcher string, argv []string) []string {
	if managed, ok := n.managedLauncher(launcher); ok && len(argv) >= 3 && slices.Equal(argv[:3], []string{n.bin, "launch", launcher}) {
		// Only this wrapper executes managed Args; a same-name shell alias takes precedence.
		args := append(slices.Clone(managed.Args), argv[3:]...)
		if len(codexBypassArgs(args)) == len(args) {
			return argv
		}
		if slices.Contains(managed.Args, "--") {
			return append([]string{codexBypassEnv + "=1"}, argv...)
		}
	}
	return codexBypassArgs(argv)
}

// insertBeforeDashDash puts extra in the option region of args, leaving the separator and prompt suffix in place.
func insertBeforeDashDash(args, extra []string) []string {
	end := slices.Index(args, "--")
	if end < 0 {
		end = len(args)
	}
	out := make([]string, 0, len(args)+len(extra))
	out = append(out, args[:end]...)
	out = append(out, extra...)
	return append(out, args[end:]...)
}

// codexExecutionArgs is a Codex launcher's words argv running model execution e: its model in place of any the words
// name, and its effort, which as the purpose's explicit choice takes precedence over the launcher's defaults.
func codexExecutionArgs(argv []string, e modelExecution) []string {
	var extra []string
	if e.Model != "" {
		argv = withoutModelArgs(argv)
		extra = append(extra, "--model", e.Model)
	}
	if e.Effort != "" {
		extra = append(extra, "-c", "model_reasoning_effort="+e.Effort)
	}
	if e.ServiceTier != "" {
		extra = append(extra, "-c", "service_tier="+e.ServiceTier)
	}
	return insertBeforeDashDash(argv, extra)
}

// startCodex starts a Codex launcher in a new tab with the first message and returns its session once the thread is
// known, its record naming the model and effort asked for (execution), which a resume runs again (retire.go). With no message it returns the waiting row once Codex's input box is up (codex_waiting.go). In a pane it was
// given (adoptedPane: a switch from Claude Code, engine_switch.go) it starts there, and a start that fails leaves that
// pane open: what it left in front is for the caller to clear.
func (n *Node) startCodex(ctx context.Context, launch host.Launcher, cwd, launcher string, argv []string, execution modelExecution, prompt, ws, tabName, mode string) (*proto.Session, error) {
	if execution.Source.Role == "" {
		execution.Source.Role = "task_agent"
	}
	execution.Source.Session = "" // a fresh Codex thread has no Persona session ID yet
	waiting := strings.TrimSpace(prompt) == ""
	if waiting {
		prompt = ""
	}
	if mode == "bypassPermissions" {
		argv = n.codexBypassArgv(launcher, argv)
	}
	if err := n.checkCodexWritableSymlinks(ctx, launch, launcher, argv, cwd); err != nil {
		return nil, err
	}
	key := cwd + "\x00" + launcher
	if err := n.closeAbandonedCodex(ctx, key); err != nil {
		return nil, err
	}
	runs := n.codexOwnEffort(ctx, argv, codexPaneEnv(launch), cwd, execution)
	var prepared *codexPreparedThread
	dedicatedInstructions := execution.TaskInstructions != "" && slices.Contains(argv, codexTaskConfig(execution.TaskInstructions))
	if execution.CodexEffortFallback == proto.CodexFallbackAnnounced {
		runs.Effort = execution.Effort
	} else if runs.Effort != "" {
		var err error
		prepared, err = n.prepareCodexEffort(ctx, launch, argv, cwd, execution)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			if prepared != nil {
				prepared.discard()
			}
			return nil, err
		}
		if prepared == nil && execution.CodexEffortFallback == proto.CodexFallbackNotify && !codexOnRemote(argv) {
			return nil, refusal(proto.CodeCodexEffortTerminal, i18n.T(i18n.From(ctx), "assistant-tasks.codex_effort_terminal"), nil)
		}
	}
	if runtime.GOOS == "windows" {
		prompt = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(inertControls(prompt))
	}
	filePrompt := prompt
	keep := false
	if prepared != nil {
		noAltScreen := slices.Contains(argv, "--no-alt-screen")
		defer func() {
			if !keep {
				prepared.discard()
			}
		}()
		argv = []string{argv[0], "--remote", prepared.server.spec.Endpoint}
		if noAltScreen {
			argv = append(argv, "--no-alt-screen")
		}
		argv = append(argv, "resume", prepared.raw)
		filePrompt = ""
	} else {
		argv = codexExecutionArgs(argv, runs)
		if n.codexOnSharedDaemon(argv) {
			if err := codexDaemonSocketGone(n.codexHome()); err != nil {
				return nil, err // a new tab would only show Codex failing to connect after the startup wait
			}
		}
		if codexOnRemote(argv) {
			// A launcher on an explicit app-server (--remote) sends no directory unless told one.
			argv = append(argv, "-C", cwd)
		}
	}
	dir, err := os.MkdirTemp(n.dir, "codex-start-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir) // codex-start removes the file once read; the directory goes when the start ends
	file := filepath.Join(dir, "start.json")
	owner, _ := launch.(interface{ Kind() string })
	isolateHooks := owner != nil && owner.Kind() == host.Orca
	b, _ := json.Marshal(codexStartFile{Source: execution.Source, SourceHosts: sourceHostsFor(execution.Source), Argv: argv, Prompt: filePrompt, Agent: herdrAgentFor(launch, "codex"), IsolateHooks: isolateHooks, Dir: cwd})
	if err := os.WriteFile(file, b, 0o600); err != nil {
		return nil, err
	}
	created, err := n.newTab(ctx, launch, cwd, ws, tabName, "") // retire.go
	if err != nil {
		return nil, err
	}
	pane := created.Pane.String()
	n.mu.Lock()
	if n.codexStarting == nil {
		n.codexStarting = map[string]bool{}
	}
	n.codexStarting[pane] = true // no guessing a thread for it meanwhile (setAgentsAt)
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.codexStarting, pane)
		n.mu.Unlock()
	}()
	shells := n.startPaneShells(ctx, launch, created)
	cmd := n.agentStartCommand(shellQuote(n.bin)+" codex-start "+shellQuote(file), shellQuote, shells...) // agentenv.go
	began := time.Now()
	if err := n.settleShell(ctx, pane); err != nil { // the fresh shell at rest first (shellsettle.go, #2570)
		return nil, n.startTabFailed(ctx, launch, created, ws, "", err)
	}
	done, err := n.runAgentCommand(ctx, launch, created, ws, "", cmd, shells...) // typedstart.go
	if err != nil {
		if adoptedPane(ctx) != nil && filePrompt != "" && n.codexMessageMayHaveGone(ctx, pane, file) {
			return nil, unconfirmedStart(err) // a switch's message may be in it (engine_switch.go)
		}
		return nil, err
	}
	defer done()
	exited := paneStartExit{file: file}
	var id string
	if prepared != nil {
		id = prepared.raw
		err = n.waitCodexBox(ctx, pane, codexStartWait, &exited)
	} else if waiting {
		err = n.waitCodexBox(ctx, pane, codexStartWait, &exited)
		if errors.Is(err, errCodexBoxLate) {
			// Codex runs but its box has not come up yet: the row all the same, and its tab stays. Nothing is queued in it
			// that could run later; the first message waits for the box itself (codexBoxFor).
			n.logf("codex %s: its input box did not come within %s; offering the row anyway", pane, codexStartWait)
			err = nil
		}
	} else {
		id, err = n.waitCodexThread(ctx, pane, cwd, prompt, began, &exited)
	}
	if err != nil {
		shown, _ := n.readPane(ctx, pane, "recent-unwrapped", 30)
		if strings.TrimSpace(shown) == "" { // a screen Codex draws from the top of a tall pane: the bottom rows are blank
			shown, _ = n.readPane(ctx, pane, "visible", 30)
		}
		if adoptedPane(ctx) != nil && filePrompt != "" && n.codexMessageMayHaveGone(ctx, pane, file) {
			// A switch's message (engine_switch.go) may be in the Codex the line started: no other start may get it too.
			return nil, unconfirmedStart(fmt.Errorf("%v\n%s", err, lastLines(shown, 12)))
		}
		start := codexAbandonedStart{launch: launch, created: created, workspace: ws}
		var cleanupErr error
		// A pane it did not open (a switch from Claude Code, engine_switch.go) is the user's terminal and is never
		// closed: the switch quits what the start left in it (clearPane).
		if adoptedPane(ctx) == nil {
			if prepared == nil {
				identify, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				start.session = n.failedCodexSession(identify, pane, began)
				cancel()
			}
			cleanupErr = n.closeFailedCodexStart(ctx, start)
		}
		if prepared != nil && (cleanupErr != nil || adoptedPane(ctx) != nil) {
			prepared.path = "" // the pane may still be using it
		}
		if cleanupErr != nil {
			n.mu.Lock()
			if n.codexAbandoned == nil {
				n.codexAbandoned = map[string][]codexAbandonedStart{}
			}
			n.codexAbandoned[key] = append(n.codexAbandoned[key], start)
			n.mu.Unlock()
		}
		message := fmt.Sprintf("%v\n%s", err, lastLines(shown, 12))
		var coded *rpc.Error
		if cleanupErr != nil {
			// A start that met Codex's sign-in screen stays that refusal: what stopped it is the launcher's sign-in,
			// whatever became of its tab (the cleanup's words follow).
			if errors.As(err, &coded) && coded.Code == proto.CodeCodexNotLoggedIn {
				return nil, &rpc.Error{Code: coded.Code, Message: message + "\n" + cleanupErr.Error(), Data: coded.Data}
			}
			return nil, &rpc.Error{Code: proto.CodeAgentStartStalled, Message: message + "\n" + cleanupErr.Error()}
		}
		if errors.As(err, &coded) {
			return nil, &rpc.Error{Code: coded.Code, Message: message, Data: coded.Data}
		}
		return nil, fmt.Errorf("%w\n%s", err, lastLines(shown, 12))
	}
	if waiting && prepared == nil {
		return n.openCodexWait(ctx, pane, cwd, launcher, mode, execution, began), nil
	}
	if prepared != nil {
		id = prepared.public
	} else {
		defer n.unclaimCodexThread(id)
	}
	n.bindPane(pane, id)
	n.namePersonaTab(ctx, pane, id) // the tab was marked before the thread was known (personatab.go)
	st := sessionState{ModelSource: execution.Source, Pane: pane, Launcher: launcher, Cwd: cwd, StartedAt: began.UTC(), Model: execution.Model, ModelEffort: execution.Effort, CodexStartTier: execution.ServiceTier, TaskInstructions: execution.TaskInstructions}
	if dedicatedInstructions {
		st.TaskInstructionChannel = "codex"
	}
	if mode == "plan" {
		st.AppMode = mode
	}
	if legs, ok := engineHistoryOf(ctx); ok { // a switch from Claude Code starts it (engine_switch.go)
		st.EngineHistory = legs
	}
	n.putAgentState(ctx, launch, id, st)
	_ = n.refreshHosts(ctx)
	n.joinCodexPanes(ctx)
	if prepared != nil {
		keep = true
		if prompt != "" {
			if err := n.sendCodex(ctx, id, prompt, nil); err != nil {
				return nil, err
			}
		}
	}
	a, ok := n.agent(id)
	if !ok {
		a = host.Agent{Agent: "codex", PaneID: pane, Cwd: cwd, AgentStatus: proto.StatusWorking}
	}
	s := n.sessionFor(id, a)
	s.Agent = "codex"
	if host.ParsePane(pane).Kind != host.Botmux {
		s.CodexRoute, s.CodexRouteNote = n.codexRoute(id)
		n.codexPaneControls(s, n.metaOf(id))
	}
	return s, nil
}

// codexOnRemote: the launcher attaches to an app-server of its own (--remote).
func codexOnRemote(argv []string) bool {
	for _, w := range argv {
		if w == "--remote" || strings.HasPrefix(w, "--remote=") {
			return true
		}
	}
	return false
}

// claimCodexThread reserves a matched public thread ID for pane. Another pane's binding or in-flight claim wins without
// making this waiter stop: it keeps scanning for the next matching rollout instead.
func (n *Node) claimCodexThread(pane, id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.codexThreadAvailableLocked(pane, id, n.panes) {
		return false
	}
	if n.codexClaims == nil {
		n.codexClaims = map[string]string{}
	}
	n.codexClaims[id] = pane
	return true
}

// codexThreadAvailableLocked checks both the caller's bindings and a start that has claimed the thread but has
// yet to bind its pane. The caller holds n.mu through the claim or binding.
func (n *Node) codexThreadAvailableLocked(pane, id string, bindings map[string]string) bool {
	for p, bound := range bindings {
		if bound == id && p != pane {
			return false
		}
	}
	owner, claimed := n.codexClaims[id]
	return !claimed || owner == pane
}

func (n *Node) unclaimCodexThread(id string) {
	n.mu.Lock()
	delete(n.codexClaims, id)
	n.mu.Unlock()
}

// waitCodexThread claims and returns the public ID of the thread a start made: a rollout in the directory (as given, or resolved: Codex records
// the shell's resolved working directory, or -C as given), begun after the launch, whose first user message is the
// prompt. Codex's own dialogs on the way are answered as the user would for a start they asked
// for: the folder is trusted (they chose it), an update is skipped. A launcher that fails ends the wait at once.
func (n *Node) waitCodexThread(ctx context.Context, pane, cwd, prompt string, began time.Time, exit ...*paneStartExit) (string, error) {
	poll := n.readinessPoll()
	maxWait := codexStartWait
	deadline := time.Now().Add(maxWait)
	resolved := cwd
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		resolved = r
	}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(min(time.Until(deadline), poll)):
		}
		scanBy := time.Now().Add(n.codexHomeListBudget())
		for _, home := range n.codexRolloutHomes(pane) {
			for _, r := range n.firstMessageRollouts(home, scanBy) {
				if (r.cwd != cwd && r.cwd != resolved) || r.mtime.Before(began.Add(-time.Second)) {
					continue
				}
				// A thread begun by this launch (not one resumed in the same directory) whose first message is ours.
				if at, first := codexThreadStart(r.path); !at.Before(began.Add(-2*time.Second)) && strings.TrimSpace(first) == strings.TrimSpace(prompt) {
					id := n.codexPublicID(home, r.id)
					if n.claimCodexThread(pane, id) {
						return id, nil
					}
				}
			}
		}
		// The screen without the blank rows under it: Codex 0.167 draws its trust dialog from the top of the pane, and in
		// a pane 51 rows tall the last 40 "recent" rows were all blank (#2351).
		text, err := n.readPane(ctx, pane, "visible", screen.DialogLines)
		if err != nil {
			continue
		}
		if err := n.codexStartupFailure(ctx, pane, text, exit); err != nil {
			return "", err
		}
		if codexSignInScreen(text) {
			return "", codexNotLoggedIn()
		}
		if keys := codexTrustKeys(text); keys != nil {
			_ = n.sendKeys(ctx, pane, keys...)
			select { // the dialog goes before the next look, which would otherwise answer it again
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		if keys := codexSkipUpdateKeys(text); keys != nil {
			_ = n.sendKeys(ctx, pane, keys...)
			_ = sleepCtx(ctx, codexDialogPause)
		}
	}
	message := fmt.Sprintf("Codex 已经在电脑上的新标签里打开了，但 %g 秒内没认出它这次的会话（第一条消息还没进它的会话记录）", maxWait.Seconds())
	if agent, err := n.paneAgent(ctx, pane); err == nil && agent != nil && agent.Agent == "codex" {
		return "", &rpc.Error{Code: proto.CodeAgentStartStalled, Message: message}
	}
	return "", errors.New(message)
}

// codexTrustKeys are the keys that answer Codex's folder trust dialog by trusting the folder, nil when the screen does
// not show that dialog with its cursor. The dialog is known by its question asking about trust (0.163 and 0.167 "Trust
// this folder?", 0.153 "Do you trust the contents of this directory?"), and the option to pick by what it says
// ("Trust and continue", "Yes, continue"), not by its number or the other option's label: 0.167 has "2. Back to Agent
// Command Center" where 0.163 had "2. Quit". The cursor is walked to it and enter picks it. Only the question counts:
// an approval's command above its "Yes, proceed" may say trust too.
func codexTrustKeys(text string) []string {
	d := screen.ParseDialog(text)
	if d == nil || d.Cursor == "" || !strings.Contains(strings.ToLower(d.Question), "trust") {
		return nil
	}
	cur, trust := -1, -1
	for i, o := range d.Options {
		if o.Key == d.Cursor {
			cur = i
		}
		if l := strings.ToLower(o.Label); trust < 0 && (strings.HasPrefix(l, "trust") || strings.HasPrefix(l, "yes")) {
			trust = i
		}
	}
	if cur < 0 || trust < 0 {
		return nil
	}
	var keys []string
	for i := cur; i < trust; i++ {
		keys = append(keys, "down")
	}
	for i := cur; i > trust; i-- {
		keys = append(keys, "up")
	}
	return append(keys, "enter")
}

// codexSignInScreen: the screen is Codex's sign-in screen, which it shows in place of its input box when it has no
// login on this computer. Nothing a start may answer (only a person can sign in) and nothing a wait outlasts, so a
// start ends on it at once (codexNotLoggedIn) instead of waiting out codexStartWait. Known by what it offers, not by
// its numbers, its heading or the other options: every version lists a "Sign in with …" choice ("Sign in with
// ChatGPT", later also "Sign in with Device Code"), next to "Provide your own API key" in older ones and "Sign in
// another way" in newer ones. Only a choice under the cursor counts, and never with the input box drawn: a message
// quoting those words in the conversation is no dialog.
func codexSignInScreen(text string) bool {
	d := screen.ParseDialog(text)
	if d == nil || d.Cursor == "" || len(d.Options) < 2 || screen.ReadCodex(text).View == screen.CodexComposer {
		return false
	}
	for _, o := range d.Options {
		if strings.HasPrefix(strings.ToLower(o.Label), "sign in with ") {
			return true
		}
	}
	return false
}

// codexNotLoggedIn is the refusal of a start that met Codex's sign-in screen (codexSignInScreen). Its code is what the
// hub words in the user's language and counts as the launcher's sign-in (launcher health's auth), never as a stalled
// start.
func codexNotLoggedIn() error {
	return refusal(proto.CodeCodexNotLoggedIn, i18n.T(i18n.Default, "node-codex-start.not_logged_in"), nil)
}

// codexThreadStart is when a rollout's thread began (its session_meta) and the text of its first user message ("" when
// there is none yet).
func codexThreadStart(path string) (time.Time, string) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, ""
	}
	defer f.Close()
	reader, closeReader, err := codexRolloutReader(f)
	if err != nil {
		return time.Time{}, ""
	}
	defer closeReader()
	sc := bufio.NewScanner(reader)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var began time.Time
	for lines := 0; sc.Scan() && lines < 200; lines++ {
		line := sc.Bytes()
		if lines == 0 {
			began = lineTime(line)
		}
		if !strings.Contains(string(line), `"UserMessage"`) {
			continue
		}
		var l struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Item struct {
					Type    string `json:"type"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &l) != nil || l.Payload.Type != "item_completed" || l.Payload.Item.Type != "UserMessage" {
			continue
		}
		var b strings.Builder
		for _, c := range l.Payload.Item.Content {
			b.WriteString(c.Text)
		}
		return began, b.String()
	}
	return began, ""
}

// codexStartError starts what `personad codex-start` prints when it cannot run the launcher. codexStartNote starts
// what it prints and goes on (the agent sandbox not wrapping the start): never read as a failure, since the Codex it
// then runs has the message.
const (
	codexStartError = "personad codex-start:"
	codexStartNote  = "personad codex-start (note):"
)

// codexStartFile is what `personad codex-start` runs: the launcher's words (variables set in front, `A=1 codex …`,
// then the command and its arguments) and the first message.
type codexStartFile struct {
	Source modelhttp.Source `json:"model_source,omitzero"`
	// SourceHosts is the node's PERSONA_MODEL_SOURCE_HOSTS: the pane's shell has its engine's environment, not the
	// node's, so the allowlist that lets Source reach the provider gets to codex-start only this way.
	SourceHosts string   `json:"model_source_hosts,omitempty"`
	Argv        []string `json:"argv"`
	Prompt      string   `json:"prompt"`
	// Agent is the HERDR_AGENT the words run with (hostherdr.go): "codex" for a Codex started in a herdr pane; "" leaves
	// the variable as it is (tab-start, other hosts).
	Agent string `json:"agent,omitempty"`
	// Orca hooks belong to this terminal. A shared Codex daemon can retain another terminal's hook environment.
	IsolateHooks bool `json:"isolate_hooks,omitempty"`
	// Dir is the directory the words run in ("": the shell's): the conversation's. A start in a pane the shell of
	// which is elsewhere (a switch from Claude Code, engine_switch.go) runs Codex there, which records it as the
	// thread's directory, and a resume finds its thread's files from there. No cd is typed: nothing but codex-start
	// and its file goes to the shell.
	Dir string `json:"dir,omitempty"`
}

const codexStartMarker = "PERSONA_CODEX_START=1"

// CodexStart is `personad codex-start <file>`: it claims a codexStartFile (renames it away, then reads and removes
// it), changes to its directory, then runs its words with any message as one more argument after "--", in place of
// itself. Neither is parsed as shell code. Claimed by a rename, the file is either this process's or withdrawn by the node (a
// switch whose start gave up on it, codexMessageMayHaveGone): never both. The file's agent replaces a HERDR_AGENT the
// shell had: appended, the inherited one would come first, and that is the one getenv and herdr read.
func CodexStart(args []string) error {
	if len(args) >= 2 && args[0] == "--herdr-resume" {
		f, err := herdrCodexRestore(args[1:])
		if err != nil {
			return err
		}
		return runStartFile(f, true)
	}
	return startFromFile(args, true)
}

// TabStart resumes a runtime-provided command, such as pi, in a new tab.
func TabStart(args []string) error {
	return startFromFile(args, false)
}

func startFromFile(args []string, codex bool) error {
	if len(args) != 1 {
		return errors.New(codexStartError + " usage: personad codex-start <file>")
	}
	b, err := claimCodexStartFile(args[0])
	var f codexStartFile
	if err == nil {
		err = json.Unmarshal(b, &f)
	}
	if err != nil {
		return fmt.Errorf("%s the first message is gone: %v", codexStartError, err)
	}
	return runStartFile(f, codex)
}

func runStartFile(f codexStartFile, codex bool) error {
	words := f.Argv
	env := os.Environ()
	var explicit []string
	if f.Dir != "" {
		if err := os.Chdir(f.Dir); err != nil {
			return fmt.Errorf("%s %v", codexStartError, err)
		}
		if _, set := os.LookupEnv("PWD"); set { // what the shell said its directory was: the one it runs in now
			env = append(withoutEnv(env, []string{"PWD"}), "PWD="+f.Dir)
			explicit = append(explicit, "PWD="+f.Dir)
		}
	}
	if f.Agent != "" {
		env = append(withoutEnv(env, []string{herdrAgentEnv}), herdrAgentEnv+"="+f.Agent)
		explicit = append(explicit, herdrAgentEnv+"="+f.Agent)
	}
	if f.Source.Role != "" {
		bin, _ := os.Executable()
		sourceEnv := modelSourceEnvironment(f.Source, bin)
		env = append(withoutEnv(env, envNames(sourceEnv)), sourceEnv...)
		explicit = append(explicit, sourceEnv...)
		if hosts := adoptSourceHosts(f.SourceHosts); hosts != "" {
			env = append(withoutEnv(env, []string{modelhttp.SourceHostsEnv}), hosts)
			explicit = append(explicit, hosts)
		}
	}
	for len(words) > 0 && shellcmd.AssignRe.MatchString(words[0]) {
		if name, _, ok := strings.Cut(words[0], "="); ok {
			env = withoutEnv(env, []string{name})
		}
		env = append(env, words[0])
		explicit = append(explicit, words[0])
		words = words[1:]
	}
	if len(words) == 0 {
		return errors.New(codexStartError + " no command")
	}
	if codex {
		// Set it before rc files choose a launcher, even if the pane or alias supplied a stale value.
		env = append(withoutEnv(env, []string{"PERSONA_CODEX_START"}), codexStartMarker)
		explicit = append(explicit, codexStartMarker)
	}
	path, err := exec.LookPath(words[0])
	if err != nil && words[0] == "codex" && runtime.GOOS != "windows" {
		// A native launcher may exist only as an rc function or on the rc-provided PATH.
		if _, shellErr := exec.LookPath(codexStartShellProgram()); shellErr == nil {
			path, err = words[0], nil
		}
	}
	if err != nil {
		return fmt.Errorf("%s %s not found in PATH", codexStartError, words[0])
	}
	// On Windows the launcher may be a batch file (npm's codex.cmd): the program behind it, never cmd.exe with the message.
	path, env, err = codexStartProgram(path, f.Prompt != "", env)
	if err != nil {
		return fmt.Errorf("%s %v", codexStartError, err)
	}
	argv := append([]string(nil), words...)
	if f.IsolateHooks && !codexOnRemote(argv) && codexOwnProcess(argv) == "" {
		// Probe the executable in the new terminal, not the node's PATH. Older CLIs have no shared daemon or flag.
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		probe := exec.CommandContext(ctx, path, "--help")
		probe.Env = env
		help, err := probe.Output()
		cancel()
		if err == nil && strings.Contains(string(help), "--no-daemon") {
			argv = append([]string{argv[0], "--no-daemon"}, argv[1:]...)
		}
	}
	if filepath.Base(words[0]) == "codex" && (filepath.IsAbs(words[0]) || runtime.GOOS == "windows") {
		home, _ := os.UserHomeDir()
		cwd, _ := os.Getwd()
		argv, env = codexModelSource(argv, env, cwd, home)
	}
	if f.Prompt != "" { // a resume without a message (`personad tab-start`, pi.go) runs the words alone
		argv = append(argv, "--", f.Prompt)
	}
	if filepath.Base(argv[0]) == "codex" {
		home, _ := os.UserHomeDir()
		cwd, _ := os.Getwd()
		if err := guardCodexWritableSymlinks(context.Background(), argv, env, cwd, home); err != nil {
			return fmt.Errorf("%s %w", codexStartError, err)
		}
	}
	path, argv, env, _ = codexStartExec(path, argv, env, explicit)
	if err := codexStartCommandLine(argv); err != nil {
		return fmt.Errorf("%s %w", codexStartError, err)
	}
	return osproc.Exec(path, argv, env)
}

// claimCodexStartFile takes codex-start's file at path: renamed away first, then read and removed. A file the node
// withdrew first (codexMessageMayHaveGone) is gone: an error, and nothing of it runs. A rename another program holds
// up for a moment (claimBusy: a scanner opening the new file on Windows) is tried again for claimWait.
func claimCodexStartFile(path string) ([]byte, error) {
	taken := path + ".taken"
	deadline := time.Now().Add(claimWait)
	for {
		err := claimRename(path, taken)
		if err == nil {
			break
		}
		if !claimRetryable(err) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer os.Remove(taken)
	return os.ReadFile(taken)
}

// claimWait bounds how long codex-start tries again a claim another program holds up; claimRename renames and
// claimRetryable tells such a refusal (claimBusy), variables so a test can hold a claim up.
var (
	claimWait      = 2 * time.Second
	claimRename    = os.Rename
	claimRetryable = claimBusy
)

// codexStartNotef prints a note of codex-start's that it goes on after (codexStartNote): never what a start reads as
// codex-start failing (codexStartError).
func codexStartNotef(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, codexStartNote+" "+format+"\n", a...)
}

// The rc file may export defaults. Restore the start's environment without parsing it as code.
// Only the names of the saved environment entries reach argv; values can be secrets. Indirection never evals them.
// A carrier holds the whole NAME=value assignment: even NAME= is nonempty and restores an explicitly empty value.
const codexShellRestoreEnv = `
while [ "$1" != -- ]; do
  if [ -n "${ZSH_VERSION-}" ]; then
    [ -n "${(P)1}" ] || exit 1
    builtin export "${(P)1}" || exit
  else
    [ -n "${!1}" ] || exit 1
    builtin export "${!1}" || exit
  fi
  builtin unset "$1" || exit
  shift
done
shift
`

func codexShellEnvCarriers(assignments []string) (args, env []string) {
	prefix := "__PERSONA_CODEX_START_" + rand.Text() + "_"
	for i, assignment := range assignments {
		name := prefix + strconv.Itoa(i)
		args = append(args, name)
		env = append(env, name+"="+assignment)
	}
	return append(args, "--"), env
}

const codexNativeStartScript = `
builtin cd -- "$1" || exit
shift
` + codexShellRestoreEnv + `
if [[ -n "${PERSONA_MODEL_SOURCE_BIN-}" && -n "${PERSONA_MODEL_SOURCE_HOSTS-}" ]]; then
  function __persona_source_codex { "${PERSONA_MODEL_SOURCE_BIN}" model-source codex "$@"; }
  if [[ -n "${ZSH_VERSION-}" ]] && (( $+functions[codex] )); then
    functions[__persona_source_user_codex]=$functions[codex]
    function codex { __persona_source_user_codex "$@"; }
  elif [[ -n "${BASH_VERSION-}" ]] && builtin declare -F codex >/dev/null; then
    eval "$(builtin declare -f codex | sed '1s/codex/__persona_source_user_codex/')"
    function codex { __persona_source_user_codex "$@"; }
  else
    function codex { __persona_source_codex "$@"; }
  fi
  function command { if [[ "$1" == codex ]]; then shift; __persona_source_codex "$@"; else builtin command "$@"; fi; }
fi
"$@"
`

// codexStartExec is what codex-start execs for its words (path, argv, env): in the agent sandbox when it is on
// (agentsandbox.go), for a Codex only when it is sure to run danger-full-access in its own process (codexInner). A
// managed launcher (`personad launch`) is left to Launch, which wraps its own start; other words (pi resumed by
// tab-start) are not in this phase. Not wrapped, they come back as they are.
func codexStartExec(path string, argv, env, explicit []string) (string, []string, []string, sandbox.AgentStatus) {
	if len(argv) == 0 || filepath.Base(argv[0]) != "codex" {
		return path, argv, env, sandbox.AgentStatus{State: sandbox.AgentOff}
	}
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	inner := func() *sandbox.StatusReason { return codexInnerReason(argv, env, cwd, home) }
	run := append([]string{path}, argv[1:]...)
	if argv[0] == "codex" && runtime.GOOS != "windows" {
		// A different shell's rc file must not override the user's native launcher function.
		if shell, err := exec.LookPath(codexStartShellProgram()); err == nil {
			// Resolve a same-name function after its rc file, without expanding aliases twice or parsing data as code.
			run = []string{shell, shellFlag(shell), codexNativeStartScript, "persona-codex-start", cwd}
			carriers, extraEnv := codexShellEnvCarriers(explicit)
			env = append(env, extraEnv...)
			run = append(run, carriers...)
			run = append(run, argv...)
		}
	}
	logf := func(format string, a ...any) { codexStartNotef(os.Stderr, format, a...) }
	wrapped, wenv, st := wrapAgent("codex-start", home, identity.DataDir(home), cwd, nil, inner, run, env, logf)
	if wrapped[0] == path {
		return path, argv, env, st
	}
	return wrapped[0], wrapped, wenv, st
}
