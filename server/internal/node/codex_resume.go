package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Resuming a Codex thread nothing runs, from the phone (#5220): a message to it, or a resume (session.start with
// resume), brings it back in a new tab of the engine with `codex resume <id>` (resumeCodex, retire.go). One Persona
// started comes back with the launcher, directory, model and effort it ran with (an effort Codex's config already runs
// is left out of the words, as at the start); one started elsewhere (`codex` in a terminal, since closed) lists as a
// Codex history row and comes back in the directory its rollout names, through the launcher the caller picked when
// that one runs Codex, else plain codex. Afterwards it is Persona's like any other: retired and resumed again the same
// way. Orca resumes it as herdr does (#17044): a new terminal in the Orca worktree holding its directory, `cd` there,
// then codex-start; the stock launcher with Orca's own Codex arguments, as a new start of it there.
//
// Only a thread nothing has open: no app-server owns it (codex_remote.go: sends go there), no process holds its
// rollout (a Codex CLI still running it, the Codex App's own app-server: a second writer would fork it), no Codex App
// still owns it (codex_desktop.go), not a subagent or a program's run. And only in the default Codex home, whose
// thread ids are the session ids: a thread of another home is listed under an opaque key (codexSourceID).

const codexResumeRefused = "codex_resume"

// codexResumeHold is how long a resume that was not confirmed (the Codex did not show in its tab in time) keeps
// another from starting: it may still come up, and a second would write the same thread.
const codexResumeHold = 2 * time.Minute

// codexResumes: thread id → when its last unconfirmed resume started.
var codexResumes sync.Map

// codexResumable: thread id is a Codex history row that may be resumed in a terminal of this machine's engine.
func (n *Node) codexResumable(ctx context.Context, id string) bool {
	return n.codexResumableIn(ctx, id, nil)
}

// codexResumableIn is codexResumable within a list pass (codexListPass).
func (n *Node) codexResumableIn(ctx context.Context, id string, pass *codexListPass) bool {
	src, ok := n.codexSource(id)
	if !ok || src.path == "" || src.thread != id || n.codexBlocked(id) || n.isAssistantSessionIn(id, pass) || n.ownedAssistantSource(src.thread) {
		return false
	}
	if _, live := n.agent(id); live {
		return false
	}
	kind := n.startKind(n.withResumeHost(ctx, id))
	if _, runtime := engineRuntimes[kind]; runtime { // herdr, Herdr App, Orca (#17044), Tern open the terminal; a runtime does not
		return false
	}
	if _, err := hostCapFor[host.Launcher](n, kind); err != nil {
		return false
	}
	n.codices.mu.Lock()
	held := n.codices.liveRollouts[id] != ""
	n.codices.mu.Unlock()
	if held {
		return false
	}
	if _, _, err := n.codexOwner(id); !errors.Is(err, errCodexNoOwner) {
		return false
	}
	if n.codexRemoteConnecting(src.home) { // its app-server may own the thread once it is back
		return false
	}
	o := readCodexOrigin(src.path)
	if o.exec || o.sub || o.parent != "" || o.guardian {
		return false
	}
	// A shared app-server stamp corroborated by the sidebar remains App-owned. Only an explicit desktop origin becomes
	// eligible for terminal recovery after the App and every writer have stopped.
	if o.sharedAppStamp() && pass.appState(src.home).listed(src.thread) {
		return false
	}
	desktop := o.desktop
	if desktop {
		// Refuse resuming a Codex App thread whenever the App bus or native queue is live or the App itself is running
		// and could load it.
		if pass.desktopAvailable(src.home) {
			return false
		}
		if pass != nil {
			if pass.desktopRunning(n, src.home) {
				return false
			}
		} else if running, complete := n.codexAppRunningNow(src.home); running || !complete {
			return false
		}
	}
	m, ok := metaOfIn(src.home, src.path, pass)
	if !ok {
		return false
	}
	// The turn state is the session scan's (quickMeta), not the rollout header's.
	var turn quickMeta
	if st, err := os.Stat(src.path); err == nil {
		turn = n.meta(src.path, st)
	}
	if n.codexEncryptedFailed(id, turn) || desktop && n.codexTurnRunning(src, turn) {
		return false
	}
	return !m.sub && m.cwd != "" && !n.codexWriterLockHeld(src)
}

// codexRemoteConnecting: an app-server of home is configured but not connected yet.
func (n *Node) codexRemoteConnecting(home string) bool {
	n.codices.mu.Lock()
	var remotes []*codexRemote
	for _, c := range n.codices.remotes {
		remotes = append(remotes, c)
	}
	n.codices.mu.Unlock()
	for _, c := range remotes {
		c.mu.Lock()
		connecting := c.spec.Home == home && !c.ready
		c.mu.Unlock()
		if connecting {
			return true
		}
	}
	return false
}

// codexHeldNow: a Codex process has the rollout at path open right now, or the processes could not all be read. The
// list's holders come from a scan up to 30 seconds old (discoverCodex): a Codex opened on the computer since, or a
// resume still starting, would be a second writer of the thread.
func (n *Node) codexHeldNow(path string) bool {
	open, complete := n.codexOpenNow(path, false)
	return open || !complete
}

// codexOpenNow says whether a Codex process has the rollout at path open right now (cliOnly: a Codex CLI, not an
// app-server, which keeps a thread it has loaded), and whether every process could be listed and read (complete).
func (n *Node) codexOpenNow(path string, cliOnly bool) (open, complete bool) {
	n.codices.mu.Lock()
	scan, checked := n.codices.scan, n.codices.checked
	n.codices.mu.Unlock()
	var procs []codexProcess
	listed := true
	switch {
	case checked != nil:
		procs, listed = checked()
	case scan != nil:
		procs = scan()
	default:
		return false, true
	}
	if cliOnly {
		procs = slices.DeleteFunc(procs, func(p codexProcess) bool { return p.endpoint != "" })
	}
	rollouts, read := scanOpenRollouts(procs)
	complete = listed && read
	for _, paths := range rollouts {
		for _, p := range paths {
			if filepath.Base(p) == filepath.Base(path) {
				return true, complete
			}
		}
	}
	return false, complete
}

// codexResumeLauncher is the launcher a thread started outside Persona is resumed with: the caller's when it runs
// Codex in this machine's own Codex home, else plain codex. One on another app-server (--remote) would have that one
// load the thread; one that sets CODEX_HOME looks for the thread in another home.
func (n *Node) codexResumeLauncher(ctx context.Context, launcher string) string {
	if launcher == "" || launcher == "claude" {
		return "codex"
	}
	argv, ok := n.codexLauncher(ctx, launcher)
	if !ok || codexOnRemote(argv) {
		return "codex"
	}
	for _, w := range argv {
		if strings.HasPrefix(w, "CODEX_HOME=") {
			return "codex"
		}
	}
	return launcher
}

// paneOf is the pane bound to session id, "" for none.
func (n *Node) paneOf(id string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	for pane, sid := range n.panes {
		if sid == id {
			return pane
		}
	}
	return ""
}

// resumeCodexThread resumes Codex thread id with text as its next message ("" for none): with what Persona started
// it with when it did, otherwise in its rollout's directory through launcher (when it runs Codex) or plain codex.
// What it was resumed with is kept, so the next time it comes back the same way.
func (n *Node) resumeCodexThread(ctx context.Context, id, launcher, text string, images []proto.Image, mode string) error {
	st, mine := n.state.get(id)
	previousMode := st.AppMode
	if mode != "" {
		st.AppMode = "" // An explicit non-plan choice restores the launcher's own permission behavior.
		if mode == "plan" {
			st.AppMode = mode
		}
	}
	complete := mine && st.Launcher != "" && st.Cwd != ""
	busy := refusal(codexResumeRefused, "这个 Codex 会话正在电脑上运行（或还在启动），这次没有发送：等它出现在列表里再发。", nil)
	if at, ok := codexResumes.Load(id); ok && time.Since(at.(time.Time)) < codexResumeHold {
		return busy
	}
	if path := n.codexPath(id); path != "" && n.codices != nil && n.codexHeldNow(path) {
		return busy
	}
	if src, ok := n.codexSource(id); ok && codexAppOrigin(src) {
		running, complete := n.codexAppRunningNow(src.home)
		if running {
			return takeoverRefusal("app_running")
		}
		if !complete {
			return takeoverRefusal("unavailable")
		}
	}
	if !complete {
		src, ok := n.codexSource(id)
		if !ok || src.path == "" {
			return refusal(codexResumeRefused, "Codex 会话请在电脑上恢复（codex resume）：找不到它的会话记录。", nil)
		}
		st.Launcher = n.codexResumeLauncher(ctx, launcher)
		st.Cwd = metaOf(src.home, src.path).cwd
	}
	err := n.resumeOtherWith(ctx, id, st, text, images)
	if err != nil && encryptedCodexFailure(err.Error()) {
		n.takeovers.mu.Lock()
		if n.takeovers.failed == nil {
			n.takeovers.failed = map[string]bool{}
		}
		n.takeovers.failed[id] = true
		n.takeovers.mu.Unlock()
		codexResumes.Delete(id)
		return takeoverRefusal("continue_only")
	}
	if maybeDelivered(err) && n.paneOf(id) != "" { // a tab runs it (one never made holds nothing off)
		codexResumes.Store(id, time.Now())
	} else {
		codexResumes.Delete(id)
	}
	if (!complete || st.AppMode != previousMode) && (err == nil || maybeDelivered(err)) {
		// Kept even when the start was not confirmed: the Codex may be up in its tab by now, and it is Persona's.
		if a, ok := n.agent(id); ok {
			st.Pane = a.PaneID
		} else {
			st.Pane = n.paneOf(id)
		}
		n.state.put(id, st)
	}
	return err
}

// resumeCodexSession is session.start's resume of a Codex thread that is not running: resumed like a message would
// resume it, without one unless the start brings its first.
func (n *Node) resumeCodexSession(ctx context.Context, id, launcher, prompt, mode string) (*proto.Session, error) {
	defer n.typing.lock(id)() // not while a message to it is resuming it too
	if a, ok := n.agent(id); ok {
		if mode == "plan" {
			return nil, readonlyStartUnsupported() // A concurrent start cannot receive new CLI permission flags.
		}
		return n.sessionFor(id, a), nil
	}
	st, mine := n.state.get(id)
	if !n.codexResumable(ctx, id) && (n.externalCodex(id) || !mine || st.Launcher == "" || st.Cwd == "") {
		return nil, refusal(codexResumeRefused, "这个 Codex 会话正被别的程序打开，或不是在终端里开的：请在电脑上恢复（codex resume）。", nil)
	}
	if err := n.resumeCodexThread(ctx, id, launcher, prompt, nil, mode); err != nil {
		return nil, err
	}
	a, ok := n.agent(id)
	if !ok {
		return nil, fmt.Errorf("Codex 没有接上会话 %s", id)
	}
	return n.sessionFor(id, a), nil
}
