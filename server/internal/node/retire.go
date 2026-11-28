package node

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Stopping an agent and keeping its terminal (#4754, docs/integrations/engines.md "Agents run in a terminal"). An agent
// runs on top of a terminal: Persona quits the agent process of a finished task, an idle agent (hub tasks_close.go,
// tasks_idle.go) or a cancelled task's (supervise_leader.go), and its pane stays with the shell it was typed into in
// front, as when the agent exits by itself. Only the user closes a terminal (Close, Archive and close, a pane's
// close). The session id, its transcript and, in the node's state, the launcher, directory and model it ran with stay.
//
// session.stop quits any agent running in a pane of the machine's engine; with retire it first checks what
// session.retire always did: stopped (not working, not waiting), not starting, not an assistant conversation, not a
// session another tool owns. session.retire is what a hub before #4754 asks for a finished or idle agent: it now stops
// the agent the same way and keeps the pane too. Only "Archive and close", which the user pressed, still closes the
// terminal: session.retire with close_terminal (a node before #4754 closes it without being asked). A session without
// a terminal (another tool's, an engine that runs agents itself) is refused: no_terminal to session.stop, which the
// hub then closes as before, not_retirable to session.retire as it always was.

const (
	notRetirable = "not_retirable"
	noTerminal   = "no_terminal"
	notStopped   = "not_stopped"
	// The manager's owner-activity grace period, also enforced at the node's stop boundary.
	stopInputIdle = 10 * time.Minute
)

// stopRounds and stopWait bound session.stop: each round presses Ctrl+C twice and waits up to stopWait for the agent
// to leave its pane. A working Claude takes a round to interrupt its turn and another to quit (tests shorten them).
var (
	stopRounds = 3
	stopWait   = 2 * time.Second
)

// retire stops session id's agent when it is still and keeps its pane, or closes the pane when closeTerminal (the
// user's "Archive and close", or the owner's setting to close an Agent's terminal when its session ends, #10355);
// done false (and no error) when it was not running. With afterExit, an Agent that already quit has the marked, idle
// shell it left closed too (closeAfterExit: Herdr only, refused when anything else is in it). cause is what the
// close records: an automatic one (idle, task_completed) keeps the supervisor's resume, anything else is the user's;
// an after-exit close keeps the cause the Agent stopped with.
// retireActivityGuard carries the session activity version and pane observed by the manager before requesting a
// guarded idle close. When Active is true, the node refreshes the live host state under recovery+typing locks and
// refuses with not_retirable if the session became active, opened a dialog, changed pane, or changed transcript version.
type retireActivityGuard struct {
	Active            bool
	ExpectedRev       int64
	HasExpectedRev    bool
	ExpectedUpdatedAt time.Time
	ExpectedPane      string
}

func (n *Node) retire(ctx context.Context, id string, closeTerminal, afterExit bool, cause proto.StopCause, guards ...retireActivityGuard) (done bool, err error) {
	var guard retireActivityGuard
	if len(guards) > 0 {
		guard = guards[0]
	}
	if !closeTerminal {
		if guard.Active {
			return false, refusal(notRetirable, "guarded retire requires close_terminal", nil)
		}
		return n.stopWithCause(ctx, id, true, proto.StopUnknown)
	}
	if guard.Active && !guard.HasExpectedRev && guard.ExpectedUpdatedAt.IsZero() {
		return false, refusal(notRetirable, "guarded retire requires activity version or timestamp", nil)
	}
	release := n.recovery.lock(id)
	defer release()
	if err := n.resolveStopRuntime(ctx, id); err != nil {
		return false, err
	}
	unlock, _, ok, err := n.stoppable(id, true)
	if err != nil {
		return false, err
	}
	if !ok {
		if !afterExit || guard.Active {
			return false, nil
		}
		return n.closeExited(ctx, id)
	}
	defer unlock()
	if guard.Active {
		stillRunning, guardErr := n.verifyRetireActivityGuardLocked(ctx, id, guard)
		if guardErr != nil {
			return false, guardErr
		}
		if !stillRunning {
			return false, nil
		}
	}
	if !cause.AllowsSupervisorResume() || cause == proto.StopRestart || cause == proto.StopTimeout {
		cause = proto.StopUser
	}
	if err := n.closeWithCause(ctx, id, cause); err != nil {
		return false, err
	}
	return true, nil
}

// verifyRetireActivityGuardLocked checks live host state and current transcript activity under n.recovery and n.typing
// locks before a guarded idle terminal close. It returns (false, nil) when the agent is no longer running.
func (n *Node) verifyRetireActivityGuardLocked(ctx context.Context, id string, guard retireActivityGuard) (bool, error) {
	a, ok := n.agent(id)
	if !ok {
		return false, nil
	}
	if guard.ExpectedPane != "" && a.PaneID != guard.ExpectedPane {
		return false, refusal(notRetirable, "session pane changed", nil)
	}
	kind := host.ParsePane(a.PaneID).Kind
	h := n.hosts.Get(kind)
	if h == nil {
		return false, refusal(notRetirable, "this session host is not available", nil)
	}
	if err := n.refreshHostForPaneStrict(ctx, h, a.PaneID); err != nil {
		return false, refusal(notRetirable, "could not verify live session state: "+err.Error(), nil)
	}
	a, ok = n.agent(id)
	if !ok {
		return false, nil
	}
	if a.AgentStatus != proto.StatusIdle && a.AgentStatus != proto.StatusDone {
		why := "the agent status is unknown"
		if a.AgentStatus != "" {
			why = "the agent is " + a.AgentStatus
		}
		return false, refusal(notRetirable, why, nil)
	}
	if guard.ExpectedPane != "" && a.PaneID != guard.ExpectedPane {
		return false, refusal(notRetirable, "session pane changed", nil)
	}
	if head := n.dialogOn(id, a.PaneID); head != "" {
		return false, refusal(notRetirable, "session has an open dialog", nil)
	}
	if head := n.allowDialog(id); head != "" {
		return false, refusal(notRetirable, "session has an open approval dialog", nil)
	}
	if !guard.ExpectedUpdatedAt.IsZero() && !a.LastTextAt.IsZero() && a.LastTextAt.After(guard.ExpectedUpdatedAt) {
		return false, refusal(notRetirable, "session had newer output", nil)
	}
	curRev, curUpdatedAt, hasTranscript := n.currentSessionActivity(id, a)
	if guard.HasExpectedRev {
		if hasTranscript {
			if curRev != guard.ExpectedRev {
				return false, refusal(notRetirable, "session activity version changed", nil)
			}
		} else if guard.ExpectedRev > 0 {
			return false, refusal(notRetirable, "session activity version could not be verified", nil)
		} else {
			n.mu.Lock()
			lastRev := n.lastRevs[id]
			n.mu.Unlock()
			if lastRev != guard.ExpectedRev {
				return false, refusal(notRetirable, "session activity version changed", nil)
			}
		}
	}
	if !guard.ExpectedUpdatedAt.IsZero() && hasTranscript && !curUpdatedAt.IsZero() && !curUpdatedAt.Equal(guard.ExpectedUpdatedAt) {
		return false, refusal(notRetirable, "session activity timestamp changed", nil)
	}
	return true, nil
}

func (n *Node) currentSessionActivity(id string, a host.Agent) (rev int64, updatedAt time.Time, hasTranscript bool) {
	if p := n.transcriptPath(id); p != "" {
		if st, err := os.Stat(p); err == nil {
			m := n.meta(p, st)
			rev = st.Size()
			updatedAt = lastUsed(m, st)
			hasTranscript = true
		}
	}
	if a.Agent == traexAgent {
		if t, ok := n.traexThread(id); ok {
			if u := t.updated(); !u.IsZero() && (updatedAt.IsZero() || u.After(updatedAt)) {
				updatedAt = u
			}
		}
	}
	return rev, updatedAt, hasTranscript
}

// closeExited closes the shell session id's agent left when it quit (closeAfterExit), under the recovery lock its
// caller holds; done false when there is no such shell. Its stop cause stays as it was.
func (n *Node) closeExited(ctx context.Context, id string) (bool, error) {
	return n.closeExitedChecked(ctx, id, nil)
}

// closeIdleTerminal applies the owner's close-on-end setting without bypassing automatic retirement's guard.
// The running Agent's Close and an already exited Agent's shell both recheck input at their irreversible boundaries.
func (n *Node) closeIdleTerminal(ctx context.Context, id string, cause proto.StopCause) (stopped, closed bool, err error) {
	release := n.recovery.lock(id)
	defer release()
	if err := ctx.Err(); err != nil {
		return false, false, err
	}
	if err := n.refreshHosts(ctx); err != nil {
		return false, false, err
	}
	before := func() error { return n.checkAutomaticStop(ctx, id) }
	unlock, _, running, err := n.stoppable(id, true)
	if err != nil {
		return false, false, err
	}
	if running {
		defer unlock()
		err = n.closeWithCauseChecked(ctx, id, cause, before)
		stopped, closed = err == nil, err == nil
	} else {
		closed, err = n.closeExitedChecked(ctx, id, before)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, false, ctxErr
	}
	return stopped, closed, err
}

func (n *Node) checkAutomaticStop(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !n.typing.inputIdle(id, stopInputIdle) {
		return refusal(notRetirable, "input is pending or was received within the last 10 minutes", nil)
	}
	return nil
}

func (n *Node) closeExitedChecked(ctx context.Context, id string, before func() error) (bool, error) {
	if !validID(id) {
		return false, fmt.Errorf("invalid session id %q", id)
	}
	if err := n.switchingRefusal(id); err != nil {
		return false, err
	}
	unlock := n.typing.lock(id)
	defer unlock()
	if before != nil {
		if err := before(); err != nil {
			return false, err
		}
	}
	if _, running := n.agent(id); running {
		return false, refusal(notRetirable, "the agent is running again", nil)
	}
	st, mine := n.state.get(id)
	if !mine {
		return false, nil // not Persona's: never closed
	}
	pane := st.Pane
	if pane == "" {
		p, _ := n.keptPane(id)
		pane = p.PaneID
	}
	if host.ParsePane(pane).Kind == host.Orca {
		h, ref, err := n.paneHost(pane)
		if err != nil {
			return false, err
		}
		o, ok := h.(*orcaHost)
		if !ok {
			return false, unsupported(h)
		}
		return n.closeStoppedOrca(ctx, o, id, ref.String(), st, before)
	}
	if pane == "" || host.ParsePane(pane).Kind != host.Herdr {
		return false, nil // no shell it left that Persona can tell apart safely: kept, as before
	}
	_, kept := n.keptPane(id) // in the hosts' last snapshots; closeAfterExit reads the engine again before closing
	if err := n.closeAfterExit(ctx, id, st, before); err != nil {
		return false, err
	}
	return kept, nil
}

// stop quits session id's agent and keeps its pane (retire: only one that is still); stopped false (and no error)
// when it was not running.
func (n *Node) stop(ctx context.Context, id string, retire bool) (stopped bool, err error) {
	return n.stopWithCause(ctx, id, retire, proto.StopUser)
}

// resolveStopRuntime probes connected Antigravity runtimes for an unlisted companion session ID (agy-<uuid>, which can
// drop off runtime.sessions past maxSessions=80) when this machine has no local terminal pane, presence lock, or active
// start holding it. If the runtime owns it, runtimeFor claims it so resolveAgyCompanionStop/stoppable classify it as
// no_terminal / not_retirable instead of not_found; if the owning-prefix runtime confirms it does not exist, that typed
// not_found error (with confirmed_missing=true) is returned (#16410, #16231).
func (n *Node) resolveStopRuntime(ctx context.Context, id string) error {
	conversation, ok := agyConversationID(id)
	if !ok {
		return nil
	}
	if h, _, known := n.runtimes.belongs(id); h != nil || known {
		return nil
	}
	if n.hasOwnSession(id) || n.agyPaneRow(id) ||
		n.isAssistantSession(id) || n.runtimeAssistant(id) || n.externalCodex(id) || n.restarting(id) {
		return nil
	}
	if r := n.starts.get(id); r != nil && r.ended.IsZero() {
		return nil
	}
	if _, _, held := n.agyOpenIn(id); held || n.agyHasRawPaneAttribution(conversation) {
		return nil
	}
	switch agyPresenceLockProbe(filepath.Join(n.agyDataRoot(), "presence", conversation+".lock")) {
	case agyLockHeld, agyLockUnknown:
		return nil
	}
	_, err := n.runtimeFor(ctx, "session.interrupt", id)
	return err
}

func (n *Node) stopWithCause(ctx context.Context, id string, retire bool, cause proto.StopCause) (stopped bool, err error) {
	return n.stopWithGuard(ctx, id, retire, cause, false)
}

func (n *Node) stopWithGuard(ctx context.Context, id string, retire bool, cause proto.StopCause, guarded bool) (stopped bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := n.resolveStopRuntime(ctx, id); err != nil {
		return false, err
	}
	// An Antigravity companion row id agy-<conversation-uuid> resolves here first: to its one live pane
	// row, to an authoritative not_found for an orphaned conversation, or to a hard refusal. Every other
	// id comes back unchanged. Resolving before the recovery lock also serializes companion-id stops with
	// pane-row stops on the same row key.
	origID := id
	id, err = n.resolveAgyCompanionStop(id, retire)
	if err != nil {
		return false, err
	}
	release := n.recovery.lock(id)
	defer release()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if guarded {
		if err := n.refreshHosts(ctx); err != nil {
			return false, err
		}
	}
	if cause == proto.StopUser {
		if err := n.saveStopCause(ctx, id, proto.StopUser); err != nil {
			return false, err
		}
		defer n.state.stopped(id, cause)
	}
	unlock, a, ok, err := n.stoppable(id, retire)
	if err != nil {
		return false, err
	}
	if !ok {
		// The routed row went away between resolution and stoppable. For a companion uuid re-check raw
		// attribution: a refresh can have moved the conversation to another pane, and returning the
		// idempotent (false,nil) here would otherwise make the hub release that live conversation.
		if lerr := n.agyStopAbsentVerdict(origID, id); lerr != nil {
			return false, lerr
		}
		return false, nil
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if conversation, isCompanion := agyConversationID(origID); isCompanion && id != origID {
		// The routed unique pane could gain a twin or move while saveStopCause/stoppable ran; never
		// Ctrl+C one pane while raw attribution now shows more than one.
		if err := n.agyCompanionStopRecheck(conversation, id); err != nil {
			return false, err
		}
	}
	if cause != proto.StopUser {
		n.state.stopped(id, proto.StopUnknown) // An unconfirmed/failed automatic stop never grants recovery.
	}
	var before func() error
	if guarded {
		before = func() error { return n.checkAutomaticStop(ctx, id) }
	}
	stopped, err = n.quitAgentChecked(ctx, id, a.PaneID, before)
	if stopped && err == nil {
		if conversation, isCompanion := agyConversationID(origID); isCompanion && id != origID {
			// A twin pane can have appeared during the quit verification; quitAgent only proves the
			// routed row left, so confirm no other pane still attributes the conversation before success.
			if perr := n.agyCompanionStopPostQuit(conversation); perr != nil {
				return false, perr
			}
		}
		n.state.stopped(id, cause)
	}
	return stopped, err
}

// stoppable checks that session id's agent may be stopped now (retire: only a still one) and holds its typing lock
// (unlock releases it) when ok; ok false with no error when it is not running.
func (n *Node) stoppable(id string, retire bool) (unlock func(), a host.Agent, ok bool, err error) {
	no := func(code, why string) (func(), host.Agent, bool, error) {
		if retire {
			code = notRetirable // what hubs before #4754 know
		}
		return nil, a, false, refusal(code, why, nil)
	}
	h, _, known := n.runtimes.belongs(id)
	if !validID(id) && !n.agyPaneRow(id) {
		// session.stop and session.retire are dispatched before runtimeFor (node.go), so a live or known external
		// runtime session with a non-hex id (opencode-*, hermes-*, pi-*, paseo-*, etc.) must be refused here rather
		// than falling through to "not running" or "invalid session id".
		if h != nil || known {
			if n.isAssistantSession(id) || n.runtimeAssistant(id) {
				return no(notRetirable, "assistant conversations are not stopped")
			}
			return no(noTerminal, "this session belongs to another tool")
		}
		// A runtime's session (agy-b-<conversation>, opencode-*, hermes-*, etc.) that no runtime lists any more
		// reaches here. Fail closed while a matching runtime is still starting or has connected without pushing
		// its initial runtime.sessions list yet; otherwise nothing runs it on this computer, so it is not running
		// (#16376). A pane's row of another work agent (cursor) is not this: it runs, and is refused as before;
		// so is an id of any other shape.
		if _, running := n.agent(id); !running && runtimeIDRe.MatchString(id) {
			if n.isAssistantSession(id) || n.runtimeAssistant(id) {
				return no(notRetirable, "assistant conversations are not stopped")
			}
			if n.restarting(id) {
				return no(notRetirable, "still starting")
			}
			if r := n.starts.get(id); r != nil && r.ended.IsZero() {
				return no(notRetirable, "still starting")
			}
			if label, pending := n.pendingRuntimeForStop(id); pending {
				return nil, a, false, errRuntimeAway(label)
			}
			return nil, a, false, nil
		}
		return nil, a, false, fmt.Errorf("invalid session id %q", id)
	}
	_, terminalOnly := n.terminalOnly(id)
	switch {
	case n.isAssistantSession(id) || n.runtimeAssistant(id):
		return no(notRetirable, "assistant conversations are not stopped")
	case n.externalCodex(id) || h != nil || known || terminalOnly:
		return no(noTerminal, "this session belongs to another tool")
	}
	if n.restarting(id) {
		// Refused at once rather than after the lock below, which a message carrying out a launcher switch holds for
		// minutes: stopping the agent it starts then would leave the switch for nothing (launcher_switch.go).
		return no(notRetirable, "still starting")
	}
	unlock = n.typing.lock(id) // not while a message is on its way in (a send may be resuming it right now)
	fail := func(code, why string) (func(), host.Agent, bool, error) {
		unlock()
		return no(code, why)
	}
	if r := n.starts.get(id); r != nil && r.ended.IsZero() {
		return fail(notRetirable, "still starting")
	}
	if a, ok = n.agent(id); !ok {
		unlock()
		return nil, a, false, nil
	}
	kind := host.ParsePane(a.PaneID).Kind
	if _, runtime := engineRuntimes[kind]; runtime {
		return fail(noTerminal, "this engine runs its agents without a terminal")
	}
	if n.hosts.Get(kind) == nil {
		return fail(noTerminal, "this session host is not available")
	}
	if retire {
		if a.AgentStatus == proto.StatusWorking || a.AgentStatus == proto.StatusBlocked {
			return fail(notRetirable, "the agent is "+a.AgentStatus)
		}
		switch a.Agent {
		case "", "claude", "codex", traexAgent, agyAgent: // agy leaves its TUI on two Ctrl+C presses too (#11087)
		default:
			return fail(notRetirable, "cannot stop a "+a.Agent)
		}
	}
	return unlock, a, true, nil
}

// quitAgent presses Ctrl+C twice in pane until session id's agent is no longer in it (or in any pane), and never
// closes the pane: the shell the agent ran in stays. An agent that does not quit is refused as not_stopped.
func (n *Node) quitAgent(ctx context.Context, id, pane string) (bool, error) {
	return n.quitAgentChecked(ctx, id, pane, nil)
}

func (n *Node) quitAgentChecked(ctx context.Context, id, pane string, before func() error) (bool, error) {
	for range stopRounds {
		stopped, err := n.quitAgentRound(ctx, id, pane, nil, false, before)
		if err != nil {
			return false, err
		}
		if stopped {
			return true, nil
		}
	}
	return false, refusal(notStopped, "the agent did not quit", nil)
}

// quitAgentRound is one quitAgent attempt: two Ctrl+C presses, then a bounded wait for the session to leave pane.
// With only and requireFresh, an abandoned start uses the same primitive but every verification refresh must succeed
// for that pane's host; the ordinary quitAgent keeps its tolerant multi-round refresh behavior.
func (n *Node) quitAgentRound(ctx context.Context, id, pane string, only host.Host, requireFresh bool, before func() error) (bool, error) {
	if requireFresh {
		// Do not let a successful discovery followed by an outage turn the first Ctrl+C pair into an unverified interrupt,
		// and do not send it to a shell after the agent has already left.
		if err := n.refreshHosts(ctx, only); err != nil {
			return false, err
		}
		if a, ok := n.agent(id); !ok || a.PaneID != pane {
			return true, nil
		}
	}
	// A guarded retirement must recheck activity before each irreversible action.
	check := func() error {
		if before == nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return before()
	}
	if err := check(); err != nil {
		return false, err
	}
	codexAgent := false
	codexExtra := 0
	if a, ok := n.agent(id); ok && a.PaneID == pane && a.Agent == "codex" {
		codexAgent = true
		if !requireFresh {
			// While mid-turn on an app-server, abort the turn at the protocol level first so Ctrl+C reaches an
			// idle composer; otherwise keep Ctrl+C-only keys (codex_waiting_test.go) and follow up within Codex's
			// ~1s "Press Ctrl-C again to exit" window before stopWait expires it (#12389).
			wireInterrupted := n.interruptCodexActiveTurn(ctx, id)
			if err := check(); err != nil {
				return false, err
			}
			codexExtra = 1
			if !wireInterrupted && (a.AgentStatus == proto.StatusWorking || a.AgentStatus == proto.StatusBlocked) {
				codexExtra = 2
			}
		}
	}
	_ = n.sendKeys(ctx, pane, "ctrl+c")
	time.Sleep(200 * time.Millisecond)
	if err := check(); err != nil {
		return false, err
	}
	if err := n.sendKeys(ctx, pane, "ctrl+c"); err != nil {
		return false, err
	}
	extraAt := time.Time{}
	if codexAgent && codexExtra > 0 && stopWait >= 400*time.Millisecond {
		extraAt = time.Now().Add(250 * time.Millisecond)
	}
	for deadline := time.Now().Add(stopWait); ; {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		var err error
		if only != nil {
			err = n.refreshHosts(ctx, only)
		} else {
			err = n.refreshHosts(ctx)
		}
		if err == nil {
			a, ok := n.agent(id)
			if !ok || a.PaneID != pane {
				return true, nil
			}
			// If earlier Ctrl+C presses only aborted a turn or cleared a draft, follow up with Ctrl+C while still
			// inside Codex's short exit-confirmation window (never on non-Codex panes).
			if codexExtra > 0 && !extraAt.IsZero() && !time.Now().Before(extraAt) {
				codexExtra--
				if codexExtra > 0 {
					extraAt = time.Now().Add(250 * time.Millisecond)
				} else {
					extraAt = time.Time{}
				}
				if a.Agent == "codex" {
					if err := check(); err != nil {
						return false, err
					}
					_ = n.sendKeys(ctx, pane, "ctrl+c")
				}
			}
		} else if requireFresh {
			return false, err
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false, nil
}

// resumed is what a Claude session that is not running is resumed with: the directory, launcher and model it was
// started with, the modes it ran in and its title as the tab's name.
type resumed struct {
	cwd, launcher, mode, effort, tab    string
	taskID, workspaceID, workspaceLabel string
	execution                           modelExecution
}

// resumeWith is how a Claude session that is not running comes back, by a message (send) or by a start that resumes
// it and leaves these out (session.start, fillResume).
func (n *Node) resumeWith(id string, opts sendOpts) resumed {
	st, _ := n.state.get(id)
	r := resumed{cwd: st.Cwd, tab: n.resumeTabName(id), taskID: st.TaskID, workspaceID: st.WorkspaceID, workspaceLabel: st.WorkspaceLabel}
	if r.taskID == "" {
		r.taskID, r.workspaceID, r.workspaceLabel = opts.TaskID, opts.WorkspaceID, opts.WorkspaceLabel
	}
	r.launcher, r.execution = n.resumeLauncher(st, id, opts)
	r.execution.Source = st.ModelSource
	if r.cwd == "" {
		if p := n.transcriptPath(id); p != "" {
			if fi, err := os.Stat(p); err == nil {
				r.cwd = n.meta(p, fi).cwd
			}
		}
	}
	r.mode, r.effort = n.modesOf(id)
	return r
}

// fillResume: a start that resumes a Claude session that is not running (the app's Resume Session, #4328) keeps what
// the session ran with for whatever the start leaves out (asked), as a message that resumes it does; before, it ran
// on the launcher's default model and wrote that over the one the session remembered. The model comes along only
// with the launcher it was picked for. A Codex thread resumes its own way (codex_resume.go), and a running session is
// not started again: both keep what was asked. A TRAE CLI session gets only its launcher, directory and task
// (traexResumeWith): without them a bare Resume (the web's sends only the id) would start Claude Code on its id.
func (n *Node) fillResume(id string, asked resumed) resumed {
	if !validID(id) {
		return asked
	}
	if _, live := n.agent(id); live || n.codexPath(id) != "" {
		return asked
	}
	r := n.resumeWith(id, sendOpts{})
	if n.traexPath(id) != "" {
		r = n.traexResumeWith(id)
	}
	out := asked
	if out.execution.Source.Role == "" {
		out.execution.Source = r.execution.Source
	}
	out.launcher = cmp.Or(asked.launcher, r.launcher)
	if out.launcher == r.launcher && asked.execution.Model == "" && asked.execution.Fingerprint == "" {
		out.execution.Model, out.execution.Fingerprint = r.execution.Model, r.execution.Fingerprint
	}
	out.cwd = cmp.Or(asked.cwd, r.cwd)
	out.mode = cmp.Or(asked.mode, r.mode)
	out.effort = cmp.Or(asked.effort, r.effort)
	out.tab = cmp.Or(asked.tab, r.tab)
	out.taskID = cmp.Or(asked.taskID, r.taskID)
	if r.taskID != "" && out.taskID == r.taskID {
		// The node's own task record is authoritative; a conflicting user workspace is never adopted.
		out.workspaceID = cmp.Or(r.workspaceID, asked.workspaceID)
		out.workspaceLabel = cmp.Or(r.workspaceLabel, asked.workspaceLabel)
	} else {
		out.workspaceID = cmp.Or(asked.workspaceID, r.workspaceID)
		out.workspaceLabel = cmp.Or(asked.workspaceLabel, r.workspaceLabel)
	}
	return out
}

// traexResumeWith is what a TRAE CLI session that is not running comes back with when a start leaves it out:
// `<launcher> resume <id>` (startOtherAgent) on the launcher it ran on (the CLI by name for one Persona has no record
// of), in its directory, on its task. Its model, effort and permission mode stay the CLI's own: a TRAE CLI takes them
// only on a new session (control.go).
func (n *Node) traexResumeWith(id string) resumed {
	st, _ := n.state.get(id)
	r := resumed{launcher: cmp.Or(st.Launcher, traexAgent), cwd: st.Cwd, taskID: st.TaskID, workspaceID: st.WorkspaceID,
		workspaceLabel: st.WorkspaceLabel, tab: n.traexTitle(id)}
	r.execution.Source = st.ModelSource
	if r.cwd == "" {
		if t, ok := n.traexThread(id); ok && t.Cwd != "" {
			r.cwd = t.Cwd
		} else if p := rolloutPath(n.traexHome(), id); p != "" {
			r.cwd = metaOf(n.traexHome(), p).cwd
		}
	}
	return r
}

// resumeTabName names the tab a session is resumed in: its title (Claude's AI title, or the one it was given), so
// the tab reads as it did before it was closed rather than agent-<n>.
func (n *Node) resumeTabName(id string) string {
	p := n.transcriptPath(id)
	if p == "" {
		return ""
	}
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return n.meta(p, fi).title
}

// resumeOtherWith resumes a Codex or a compatible CLI that is not running, with text as its first message, the user's
// Resume (#4754): with the launcher, directory and model to resume with (st): the session's own, or
// what a Codex thread started outside Persona is resumed with (codex_resume.go). Herdr and Orca alike (#17044): a new
// terminal in the engine's workspace for its directory, or the shell its agent left.
func (n *Node) resumeOtherWith(ctx context.Context, id string, st sessionState, text string, images []proto.Image) error {
	ctx = n.withResumeHost(ctx, id)
	if st.TaskID != "" {
		ctx, _ = withTaskSpace(ctx, st.TaskID, st.WorkspaceID, st.WorkspaceLabel)
	}
	if r := n.resumeEngineRefusal(ctx, id); r != nil {
		return r
	}
	launch, err := n.launcherFor(n.startKind(ctx))
	if err != nil {
		return err
	}
	if fi, err := os.Stat(st.Cwd); err != nil || !fi.IsDir() {
		return fmt.Errorf("directory %s does not exist", st.Cwd)
	}
	codex := n.codexPath(id) != ""
	var argv []string
	if codex {
		var ok bool
		argv, ok = n.codexLauncher(ctx, st.Launcher)
		if !ok {
			return fmt.Errorf("启动器「%s」不再运行 Codex，没法接着这个会话", st.Launcher)
		}
		if st.AppMode == "" && stockOrcaCodexArgv(st.Launcher, argv) {
			// Orca's own Codex launch profile, as a new start of the stock launcher there runs it: the resumed Codex
			// gets the arguments it started with (codex_orca_launch_defaults.go). Herdr leaves the words as they are.
			if argv, err = codexOrcaLaunchDefaults(ctx, launch, argv); err != nil {
				return err
			}
		}
		if argv, err = n.codexReadonlyArgs(st.Launcher, argv, st.AppMode); err != nil {
			return err // Refuse conflicting permissions before creating a workspace, tab or attachment.
		}
		argv = codexTaskArgs(codexExecutionArgs(argv, n.codexOwnEffort(ctx, argv, codexPaneEnv(launch), st.Cwd, modelExecution{Model: st.Model, Effort: st.ModelEffort})), codexKeptTaskInstructions(st))
		if src, ok := n.codexSource(id); ok {
			argv = codexHomeArgv(argv, src.home, canonicalCodexHome(n.codexHome(), n.home))
		}
		if home, thread, ok := n.codexThread(id); ok && codexArchived(home, n.codexPath(id)) {
			// Restore through Codex itself before its TUI can block the first prompt on an archive confirmation.
			restored, err := n.codexArchive(ctx, id, home, thread, false)
			if err != nil {
				return err
			}
			if restored.Native != nativeArchiveDone {
				return refusal(nativeArchiveFailed, "Codex cannot restore its archived session: "+restored.Reason, nil)
			}
		}
	}
	paths, err := n.saveImages(images)
	if err != nil {
		return err
	}
	if len(paths) > 0 {
		text = strings.TrimSpace(text) + "\n\n<attached_images>\n" + strings.Join(paths, "\n") + "\n</attached_images>"
	}
	ws, err := n.tabSpace(ctx, launch, st.Cwd, "")
	if err != nil {
		return err
	}
	tab := n.resumeTabName(id)
	if !codex { // a compatible CLI: `<launcher> resume <id> <message>`
		_, err = n.startOtherAgent(ctx, launch, st.Cwd, st.Launcher, traexAgent, text, id, ws, tab)
		return uncertain(err)
	}
	return uncertain(n.resumeCodex(ctx, launch, id, st, argv, text, ws, tab))
}

// Resume already knows the thread and only waits for it to reappear in the host. A var for tests.
var codexResumeWait = 60 * time.Second

// resumeCodex runs `<launcher words> resume <id> -- <message>` (through codex-start, like a new Codex, in the thread's
// directory) in a new tab and waits until the engine shows that Codex in it. In a pane it was given (adoptedPane: a
// switch between engines, engine_switch.go) it runs there once its shell is at rest.
func (n *Node) resumeCodex(ctx context.Context, launch host.Launcher, id string, st sessionState, argv []string, prompt, ws, tabName string) error {
	if codexOnRemote(argv) {
		argv = append(argv, "-C", st.Cwd)
	}
	thread := id
	if _, raw, ok := n.codexThread(id); ok && raw != "" {
		thread = raw
	}
	argv = append(argv, "resume", thread)
	dir, err := os.MkdirTemp(n.dir, "codex-start-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "start.json")
	// In an Orca terminal the resumed Codex keeps that terminal's hooks, as a new start there does (codex_start.go).
	owner, _ := launch.(interface{ Kind() string })
	isolateHooks := owner != nil && owner.Kind() == host.Orca
	b, _ := json.Marshal(codexStartFile{Source: sourceSession(st.ModelSource, id), SourceHosts: sourceHostsFor(st.ModelSource), Argv: argv, Prompt: prompt, Agent: herdrAgentFor(launch, "codex"), IsolateHooks: isolateHooks, Dir: st.Cwd})
	if err := os.WriteFile(file, b, 0o600); err != nil {
		return err
	}
	created, err := n.newTab(ctx, launch, st.Cwd, ws, tabName, id)
	if err != nil {
		return err
	}
	pane := created.Pane.String()
	if adoptedPane(ctx) != nil {
		// A shell that just took the agent's exit back: at rest before the line goes in (shellsettle.go).
		if err := n.settleShell(ctx, pane); err != nil {
			return err
		}
	} else {
		time.Sleep(300 * time.Millisecond) // the fresh shell loads ~/.zshrc; typed input is buffered either way
	}
	shells := n.startPaneShells(ctx, launch, created)
	done, err := n.runAgentCommand(ctx, launch, created, ws, "", n.agentStartCommand(shellQuote(n.bin)+" codex-start "+shellQuote(file), shellQuote, shells...), shells...) // typedstart.go
	if err != nil {
		if adoptedPane(ctx) != nil && prompt != "" && n.codexMessageMayHaveGone(ctx, pane, file) {
			return unconfirmedStart(err) // a switch's message may be in it (engine_switch.go)
		}
		return err // the tab is closed: no pane to bind
	}
	defer done()
	n.bindPane(pane, id) // the pane runs this thread (kept in its state): no guessing another rollout for it
	if src, ok := n.codexSource(id); ok && src.home != "" && n.codices != nil {
		n.codices.mu.Lock()
		if n.codices.byPane == nil {
			n.codices.byPane = map[string]string{}
		}
		n.codices.byPane[pane] = src.home
		n.codices.mu.Unlock()
	}
	wait, cancel := context.WithTimeout(ctx, codexResumeWait)
	defer cancel()
	for {
		if err := n.refreshHosts(wait); err == nil {
			if a, ok := n.agent(id); ok && a.PaneID == pane {
				// Codex may come up behind its own update offer, whose default is Update now: the next Enter (a
				// message, a /compact) would install it and quit Codex (#11316). Skipped as a new start skips it.
				n.settleCodexStartup(wait, pane)
				return nil
			}
		}
		select {
		case <-wait.Done():
			screen, _ := n.readPane(context.WithoutCancel(ctx), pane, "recent-unwrapped", 30)
			err := fmt.Errorf("Codex 没有在新标签页里接上会话 %s\n%s", id, lastLines(screen, 12))
			if adoptedPane(ctx) != nil && prompt != "" && n.codexMessageMayHaveGone(ctx, pane, file) {
				return unconfirmedStart(err) // a switch's message may be in it (engine_switch.go)
			}
			return err
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// newTab opens a tab in workspace ws for cwd, named tabName (or agent-<n>); with no workspace, a new one named after
// the directory, whose first tab it is. Its shell starts with startShellEnv where the engine takes one (startenv.go).
// The pane is marked as Persona's (personaTabToken, the session id when it is already known): once its agent exits,
// the tab stays with the shell, linked to the session (personatab.go).
func (n *Node) newTab(ctx context.Context, launch host.Launcher, cwd, ws, tabName, id string) (*host.Created, error) {
	ts := taskSpaceIn(ctx, launch)
	if c := adoptedPane(ctx); c != nil { // "Run Agent Here": the pane the user picked, not a new tab (panes_actions.go)
		if ts != nil {
			ts.adopted = true
		}
		if a, ok := launch.(paneAdopter); ok {
			if err := a.AdoptPane(ctx, c.Pane, cwd); err != nil {
				return nil, err
			}
		}
		return c, nil
	}
	if c, err := keptClaimOf(ctx).tab(ctx); err != nil || c != nil { // a failed start's kept tab (startremedy.go)
		if ts != nil {
			ts.adopted = true
		}
		return c, err
	}
	var created *host.Created
	var err error
	if ws == "" {
		label := filepath.Base(cwd)
		if ts != nil {
			label = ts.label
		}
		if created, err = createStartSpace(ctx, launch, cwd, label); err == nil && created.SlotID != "" {
			var taken map[string]bool
			if _, ok := launch.(*herdrCoreHost); ok {
				taken = n.tabLabels(ctx, launch, created.SpaceID) // CreateSpace can reuse an app space by its folder.
			}
			_ = renameSlot(ctx, launch, created.SlotID, tabLabel(tabName, taken))
		}
		// Orca opens the tab in the worktree that holds cwd: one Orca just made for this Agent (worktree.add) has a first
		// shell that is Persona's, gone now as after a start into the workspace by id (#17050).
		if _, orca := launch.(*orcaHost); orca && err == nil && created.SpaceID != "" {
			n.closeWorktreeShells(ctx, launch, created.SpaceID, created.Pane.String())
		}
	} else {
		created, err = createStartSlot(ctx, launch, ws, cwd, tabLabel(tabName, n.tabLabels(ctx, launch, ws)))
		var missing *herdr.Error
		if h, ok := launch.(*herdrHost); ok && ctx.Value(reopenWorktreeWorkspaceKey{}) == true &&
			errors.As(err, &missing) && missing.Code == "workspace_not_found" {
			created, err = n.reopenWorktreeTab(ctx, h, ws, cwd, tabName)
		}
		if err == nil {
			if ts != nil && created != nil && created.SpaceID == "" {
				created.SpaceID = ws
			}
			n.closeWorktreeShells(ctx, launch, cmp.Or(created.SpaceID, ws), created.Pane.String()) // the Agent's tab is the worktree's tab now
		}
	}
	if err != nil {
		return nil, hostFailure(ctx, "terminal_open_failed", err)
	}
	n.forgetPane(created.Pane.String()) // a new tab: whatever ran in a pane of its id before, nothing runs in it yet
	ts.opened(created.SpaceID)
	n.markPersonaTab(ctx, launch, created.Pane, id)
	markTaskTerminal(ctx, launch, created.Pane)
	if h, ok := launch.(*orcaHost); ok {
		h.markOrcaTerminal(ctx, created.Pane, id)
	}
	return created, nil
}

// paneAdopter is a launcher that types a start only into a terminal it opened itself unless told it may (Orca's
// single-use launch grant, orcaHost.Run): newTab grants it the pane a start was told to use, which the host checks
// again right before it types.
type paneAdopter interface {
	AdoptPane(ctx context.Context, pane host.PaneRef, cwd string) error
}

// agyPaneRow: id is the pane-derived row (otherSession) of an agy herdr is running now. agy rows never carry a
// session id of the hex form validID accepts, so without this an agy Agent could not be stopped or retired (#11087).
func (n *Node) agyPaneRow(id string) bool {
	a, ok := n.agent(id)
	return ok && a.Agent == agyAgent && a.PaneID != "" && id == otherSession(a)
}

// pendingRuntimeForStop reports whether a runtime matching id's "<runtime>-" prefix is still starting up or has
// connected without pushing its initial runtime.sessions list yet, so stop fails closed with runtime_away instead
// of assuming the session is already gone.
func (n *Node) pendingRuntimeForStop(id string) (string, bool) {
	if n.runtimes != nil {
		if label, pending := n.runtimes.unlistedRuntimeByPrefix(id); pending {
			return label, true
		}
		if n.runtimes.hasListedRuntimePrefix(id) {
			return "", false
		}
	}
	if rtID, pending := n.pendingAutoCompanionRuntime(id); pending {
		return rtID, true
	}
	return "", false
}
