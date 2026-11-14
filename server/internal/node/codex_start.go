package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
	"github.com/nwverse/persona/server/internal/shellcmd"
)

// Starting a Codex from the phone (docs/integrations/codex.md "Starting from the phone"). A Codex launcher (plain `codex`, or an alias whose
// command is codex) runs in a new herdr tab with the first message as its prompt argument: Codex makes the thread
// itself — on the shared app-server daemon for a plain 0.163, where #220 joins it (codex_daemon.go); in its own process
// for 0.153 or a launcher with -c, on the terminal route. Neither the message nor the launcher's words (an alias can
// carry a key) go through a shell or onto the screen: they are written to a file only the user can read, the tab runs
// `personad codex-start <file>`, and that execs the words with the message as one argument after "--". The start answers once the new thread is known — the rollout in that directory, begun after the
// launch, whose first message is exactly this one — with that thread's id, so the phone opens the session that is
// there; it fails, with what the tab shows, when none comes in time.

// codexStartWait is how long a start waits for Codex's thread (a trust dialog, a slow ~/.zshrc, the first turn).
var codexStartWait = 60 * time.Second

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

// codexExecutionArgs is a Codex launcher's words argv running model execution e: its model in place of any the words
// name, and its effort, which as the purpose's explicit choice takes precedence over the launcher's defaults.
func codexExecutionArgs(argv []string, e modelExecution) []string {
	if e.Model != "" {
		argv = append(withoutModelArgs(argv), "--model", e.Model)
	}
	if e.Effort != "" {
		argv = append(argv, "-c", "model_reasoning_effort="+e.Effort)
	}
	return argv
}

// startCodex starts a Codex launcher in a new tab with the first message and returns its session once the thread is
// known. With no message it returns the waiting row once Codex's input box is up (codex_waiting.go). In a pane it was
// given (adoptedPane: a switch from Claude Code, engine_switch.go) it starts there, and a start that fails leaves that
// pane open: what it left in front is for the caller to clear.
func (n *Node) startCodex(ctx context.Context, launch host.Launcher, cwd, launcher string, argv []string, prompt, ws, tabName, mode string) (*proto.Session, error) {
	waiting := strings.TrimSpace(prompt) == ""
	if waiting {
		prompt = ""
	}
	if mode == "bypassPermissions" {
		argv = n.codexBypassArgv(launcher, argv)
	}
	if n.codexOnSharedDaemon(argv) {
		if err := codexDaemonSocketGone(n.codexHome()); err != nil {
			return nil, err // a new tab would only show codex failing to connect, after a minute's wait
		}
	}
	key := cwd + "\x00" + launcher
	if err := n.closeAbandonedCodex(ctx, key); err != nil {
		return nil, err
	}
	if codexOnRemote(argv) {
		// A launcher on an explicit app-server (--remote) sends no directory unless told one.
		argv = append(argv, "-C", cwd)
	}
	dir, err := os.MkdirTemp(n.dir, "codex-start-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir) // codex-start removes the file once read; the directory goes when the start ends
	file := filepath.Join(dir, "start.json")
	owner, _ := launch.(interface{ Kind() string })
	isolateHooks := owner != nil && owner.Kind() == host.Orca
	b, _ := json.Marshal(codexStartFile{Argv: argv, Prompt: prompt, Agent: herdrAgentFor(launch, "codex"), IsolateHooks: isolateHooks, Dir: cwd})
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
	shells := paneShells(ctx, created)
	cmd := n.agentCommand(shellQuote(n.bin)+" codex-start "+shellQuote(file), shellQuote, shells...) // agentenv.go
	began := time.Now()
	if err := n.settleShell(ctx, pane); err != nil { // the fresh shell at rest first (shellsettle.go, #2570)
		return nil, n.startTabFailed(ctx, launch, created, ws, "", err)
	}
	done, err := n.runAgentCommand(ctx, launch, created, ws, "", cmd, shells...) // typedstart.go
	if err != nil {
		if adoptedPane(ctx) != nil && prompt != "" && n.codexMessageMayHaveGone(ctx, pane, file) {
			return nil, unconfirmedStart(err) // a switch's message may be in it (engine_switch.go)
		}
		return nil, err
	}
	defer done()
	var id string
	if waiting {
		err = n.waitCodexBox(ctx, pane)
		if errors.Is(err, errCodexBoxLate) {
			// Codex runs but its box has not come up yet: the row all the same, and its tab stays. Nothing is queued in it
			// that could run later; the first message waits for the box itself (codexBoxFor).
			n.logf("codex %s: its input box did not come within %s; offering the row anyway", pane, codexStartWait)
			err = nil
		}
	} else {
		id, err = n.waitCodexThread(ctx, pane, cwd, prompt, began)
	}
	if err != nil {
		shown, _ := n.readPane(ctx, pane, "recent-unwrapped", 30)
		if strings.TrimSpace(shown) == "" { // a screen Codex draws from the top of a tall pane: the bottom rows are blank
			shown, _ = n.readPane(ctx, pane, "visible", 30)
		}
		if adoptedPane(ctx) != nil && prompt != "" && n.codexMessageMayHaveGone(ctx, pane, file) {
			// A switch's message (engine_switch.go) may be in the Codex the line started: no other start may get it too.
			return nil, unconfirmedStart(fmt.Errorf("%v\n%s", err, lastLines(shown, 12)))
		}
		start := codexAbandonedStart{launch: launch, created: created, workspace: ws}
		var cleanupErr error
		// A pane it did not open (a switch from Claude Code, engine_switch.go) is the user's terminal and is never
		// closed: the switch quits what the start left in it (clearPane).
		if adoptedPane(ctx) == nil {
			cleanupErr = closeCodexStart(ctx, start)
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
		if cleanupErr != nil {
			return nil, &rpc.Error{Code: proto.CodeAgentStartStalled, Message: message + "\n" + cleanupErr.Error()}
		}
		var coded *rpc.Error
		if errors.As(err, &coded) {
			return nil, &rpc.Error{Code: coded.Code, Message: message, Data: coded.Data}
		}
		return nil, fmt.Errorf("%w\n%s", err, lastLines(shown, 12))
	}
	if waiting {
		return n.openCodexWait(ctx, pane, cwd, launcher, mode, began), nil
	}
	n.bindPane(pane, id)
	st := sessionState{Pane: pane, Launcher: launcher, Cwd: cwd, StartedAt: began.UTC()}
	if mode == "plan" {
		st.AppMode = mode
	}
	if legs, ok := engineHistoryOf(ctx); ok { // a switch from Claude Code starts it (engine_switch.go)
		st.EngineHistory = legs
	}
	n.state.put(id, st)
	_ = n.refreshHosts(ctx)
	a, ok := n.agent(id)
	if !ok {
		a = host.Agent{Agent: "codex", PaneID: pane, Cwd: cwd, AgentStatus: proto.StatusWorking}
	}
	s := n.sessionFor(id, a)
	s.Agent = "codex"
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

// waitCodexThread waits for the thread a start made: a rollout in the directory (as given, or resolved: Codex records
// the shell's resolved working directory, or -C as given), begun after the launch, whose first user message is the
// prompt. Codex's own dialogs on the way are answered as the user would for a start they asked
// for: the folder is trusted (they chose it), an update is skipped. A launcher that fails ends the wait at once.
func (n *Node) waitCodexThread(ctx context.Context, pane, cwd, prompt string, began time.Time) (string, error) {
	poll := n.readinessPoll()
	deadline := time.Now().Add(codexStartWait)
	resolved := cwd
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		resolved = r
	}
	homes := []string{n.codexHome()}
	for _, h := range n.codexInventory().Homes {
		if h != homes[0] {
			homes = append(homes, h)
		}
	}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(poll):
		}
		for _, home := range homes {
			for _, r := range rollouts(home) {
				if (r.cwd != cwd && r.cwd != resolved) || r.mtime.Before(began.Add(-time.Second)) {
					continue
				}
				// A thread begun by this launch (not one resumed in the same directory) whose first message is ours.
				if at, first := codexThreadStart(r.path); !at.Before(began.Add(-2*time.Second)) && strings.TrimSpace(first) == strings.TrimSpace(prompt) {
					return n.codexPublicID(home, r.id), nil
				}
			}
		}
		// The screen without the blank rows under it: Codex 0.167 draws its trust dialog from the top of the pane, and in
		// a pane 51 rows tall the last 40 "recent" rows were all blank (#2351).
		text, err := n.readPane(ctx, pane, "visible", screen.DialogLines)
		if err != nil {
			continue
		}
		if i := strings.LastIndex(text, codexStartError); i >= 0 {
			return "", errors.New(strings.TrimSpace(strings.SplitN(text[i:], "\n", 2)[0]))
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
		if strings.Contains(text, "Update available") && strings.Contains(text, "Skip") {
			if d := screen.ParseDialog(text); d != nil {
				for _, o := range d.Options {
					if o.Label == "Skip" {
						_ = n.sendKeys(ctx, pane, o.Keys...)
					}
				}
			}
		}
	}
	message := fmt.Sprintf("Codex 已经在电脑上的新标签里打开了，但 %d 秒内没认出它这次的会话（第一条消息还没进它的会话记录）", int(codexStartWait.Seconds()))
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

// codexThreadStart is when a rollout's thread began (its session_meta) and the text of its first user message ("" when
// there is none yet).
func codexThreadStart(path string) (time.Time, string) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
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
	Argv   []string `json:"argv"`
	Prompt string   `json:"prompt"`
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

// CodexStart is `personad codex-start <file>`: it claims a codexStartFile (renames it away, then reads and removes
// it), changes to its directory, then runs its words with the message as one more argument after "--", in place of
// itself. No shell sees either. Claimed by a rename, the file is either this process's or withdrawn by the node (a
// switch whose start gave up on it, codexMessageMayHaveGone): never both. The file's agent replaces a HERDR_AGENT the
// shell had: appended, the inherited one would come first, and that is the one getenv and herdr read.
func CodexStart(args []string) error {
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
	words := f.Argv
	env := os.Environ()
	if f.Dir != "" {
		if err := os.Chdir(f.Dir); err != nil {
			return fmt.Errorf("%s %v", codexStartError, err)
		}
		if _, set := os.LookupEnv("PWD"); set { // what the shell said its directory was: the one it runs in now
			env = append(withoutEnv(env, []string{"PWD"}), "PWD="+f.Dir)
		}
	}
	if f.Agent != "" {
		env = append(withoutEnv(env, []string{herdrAgentEnv}), herdrAgentEnv+"="+f.Agent)
	}
	for len(words) > 0 && shellcmd.AssignRe.MatchString(words[0]) {
		env = append(env, words[0])
		words = words[1:]
	}
	if len(words) == 0 {
		return errors.New(codexStartError + " no command")
	}
	path, err := exec.LookPath(words[0])
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
	if f.Prompt != "" { // a resume without a message (`personad tab-start`, pi.go) runs the words alone
		argv = append(argv, "--", f.Prompt)
	}
	path, argv, env, _ = codexStartExec(path, argv, env)
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

// codexStartExec is what codex-start execs for its words (path, argv, env): in the agent sandbox when it is on
// (agentsandbox.go), for a Codex only when it is sure to run danger-full-access in its own process (codexInner). A
// managed launcher (`personad launch`) is left to Launch, which wraps its own start; other words (pi resumed by
// tab-start) are not in this phase. Not wrapped, they come back as they are.
func codexStartExec(path string, argv, env []string) (string, []string, []string, sandbox.AgentStatus) {
	if len(argv) == 0 || filepath.Base(argv[0]) != "codex" {
		return path, argv, env, sandbox.AgentStatus{State: sandbox.AgentOff}
	}
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	inner := func() string { return codexInner(argv, env, cwd, home) }
	logf := func(format string, a ...any) { codexStartNotef(os.Stderr, format, a...) }
	wrapped, wenv, st := wrapAgent("codex-start", home, identity.DataDir(home), cwd, nil, inner, append([]string{path}, argv[1:]...), env, logf)
	if wrapped[0] == path {
		return path, argv, env, st
	}
	return wrapped[0], wrapped, wenv, st
}
