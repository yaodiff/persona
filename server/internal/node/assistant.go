package node

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/cachewarm"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/promptcontent"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/shellcmd"
	"github.com/nwverse/persona/server/internal/transcript"
)

// The assistant (the app's "Chat" tab) runs Claude Code headless — `<launcher> -p --input-format stream-json
// --output-format stream-json --include-partial-messages` — instead of in a herdr tab: nothing appears in herdr,
// tokens stream as they are generated, and switching the launcher (model) just restarts the process with --resume.
// A launcher that runs Codex gets `codex app-server` instead (assistant_codex.go).

// A long conversation is compacted (docs/assistant/assistant.md "Compaction"): every restart after the prompt cache expired (an hour)
// writes the whole context again, half a million tokens for an assistant conversation that had never been compacted.
// Claude Code compacts on its own once the context nears the auto-compact window, also under -p (tested on 2.1.314), by
// default the model's own window. Every conversation follows what the launcher declares (#3066:
// CLAUDE_CODE_AUTO_COMPACT_WINDOW, CLAUDE_CODE_MAX_CONTEXT_TOKENS, a managed launcher's CompactWindow / MaxContext /
// model window), else the model; only the main conversation and the leader conversations on a managed launcher are held
// to the manager's assistant.main_compact_window when that is smaller (#7314, #15318, assistant_compaction_policy.go).
// Codex alike with model_auto_compact_token_limit. Claude Code's own idle compaction only runs in interactive sessions,
// so a process about to be let go with more than 75% of its window in context (the declared one, else the one Claude
// Code reports; assistantIdleCompact until either is known) is sent /compact first, while its cache is still warm: the
// next start reads a summary instead, and nobody waits for it.
//
// assistantIdleCompact is 75% of standardWindow, the smallest window Claude Code holds a current model to (and the one
// it assumes for a model it does not know). Claude Code reports the real window at the end of every turn, before a
// process can be idle, so it only stands for an agent that reports none: there it never waits past a 200K window.
const assistantIdleCompact = standardWindow / 4 * 3

// assistantAllowedTools run without asking outside Plan mode: Persona's own tools (the hub asks the phone itself before the ones that
// send something on the user's behalf — mail_send, phone writes; Feishu writes through an extension's gated command pass larkgate.go).
// Plan rejects MCP before consulting this list. Without the list every notify_user or schedule_followup waited for a tap on the phone,
// so a follow-up firing at night just stalled.
const assistantAllowedTools = "mcp__persona"

// assistantBuiltinTools keeps the headless assistant's file, shell, web, skill and sub-agent capabilities without
// the software-development, notebook, worktree, LSP, workflow and Claude-specific scheduling surfaces. Persona MCP
// tools are not affected by this built-in allow-list; scheduling and user questions use their Persona tools instead.
// ToolSearch is in the list because --tools filters it too: without it, the tools of every MCP server other than
// Persona's (deferred since #5838: the user's own servers, claude.ai connectors) are listed but can never be loaded
// (#5839).
const assistantBuiltinTools = "Bash,Read,Write,Edit,Monitor,Skill,Agent,ListAgents,SendMessage,TaskStop,WebFetch,WebSearch,ToolSearch"

// SendMessage is a Claude built-in, not a Persona MCP call, so the manager/node origin guard cannot classify its
// cross-Agent resume or message. Inbox handling keeps the investigation tools but must finish through inbox_suggest.
const assistantInboxBuiltinTools = "Bash,Read,Write,Edit,Monitor,Skill,Agent,ListAgents,TaskStop,WebFetch,WebSearch,ToolSearch"

var errAssistantOriginUnknown = errors.New("manager origin attestation unavailable; resume refused")
var errAssistantOriginBusy = errors.New("conversation origin changed while the previous assistant process is busy")

type assistantProc struct {
	cacheProxy *cachewarm.Proxy
	id         string
	agent      string // "claude" (stream-json) or "codex" (app-server, assistant_codex.go)
	cx         *codexConn
	launcher   string
	cwd        string
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	exited     chan struct{}
	born       time.Time
	// personaMCPOnly is fixed at a Claude or Codex process start. A warm spare must still match
	// the hub's current capability before it becomes a new conversation.
	personaMCPOnly bool
	// categories is the (PERSONA_CATEGORIES value, known) state this process started with (categories_env.go).
	// Set at start, never changed: when processCategories differs, an idle process restarts and a stale spare is rejected.
	categories sessionCategoriesState
	// guest: started as a guest conversation (no Bash, no gateEnv / PERSONA_CATEGORIES).
	guest bool
	// origin is set before the process can make its first model tool call, including while Codex still has a proc-* id.
	origin string
	// stderrDone closes when its stderr ends (watchAssistantStderr has read all of it): what a failed start printed
	// is complete only then. nil for Codex.
	stderrDone chan struct{}
	// ready closes at its first JSON line on stdout (claude): it started. nil for Codex.
	ready        chan struct{}
	readyOnce    sync.Once
	shellReadyAt time.Time // guarded by mu; shell init completed, before invoking the launcher
	stopReason   string    // guarded by mu; a node-initiated startup/control failure

	wmu    sync.Mutex   // one writer on stdin at a time (messages and control requests)
	reqSeq atomic.Int64 // control request ids

	settingsMu sync.Mutex                               // serializes settings until their control responses arrive
	controls   map[string]chan assistantControlResponse // guarded by mu

	mu          sync.Mutex
	mode        string // permission mode / effort level acknowledged by Claude (modes.go)
	effort      string
	model       modelExecution
	fingerprint string
	// turnRecovery belongs to the turn currently answering on this process. A newer request may cancel the
	// conversation-map entry, but must not rewrite this older turn's retry budget before its result arrives.
	turnRecovery *assistantTurnRecovery
	// instructions is the digest of the CLAUDE.md / AGENTS.md in cwd when it started (instructionsDigest): the agent
	// reads them only at start, so once the hub rewrites them (new prompts, assistant.prepare) the next idle message
	// resumes the conversation in a fresh process (instructionsStale).
	instructions  string
	canBypass     bool // started with --permission-mode bypassPermissions: only then can it switch there (needsRestartFor)
	busy          bool
	lastActive    time.Time
	partial       strings.Builder // text of the assistant message being generated
	lastDelta     time.Time
	deltaSent     string   // the partial text last sent (partialGrewLocked, flushPartial)
	deltaArmed    bool     // a trailing send of the partial text is on its way (flushPartial)
	tasks         []string // running background tasks and monitors (descriptions), from background_tasks_changed
	tasksObserved bool     // an explicit background list was received; nil alone is not empty evidence
	tasksRevision uint64   // changes to background evidence, including invalidation
	context       int      // tokens in the conversation's context at its last request (claude), or after a compaction
	compactIdleAt int
	compacting    bool // an idle compaction is running (compactIdle)
	compacted     bool // compactIdle ran in this process: it is let go when idle again
	manual        bool // an explicit /compact (manual or idle-aware delivery): its end is reported
	requested     bool // its first request was logged
	// release: the main conversation's release policy as the reaper last read it from its record (assistant_reap.go).
	release           assistantRelease
	releaseCompaction bool // the main-chat compaction before releasing the process reports its end
	// tui: started as interactive Claude Code through `personad assistant-tui` (assistant_tui.go). tuiNote: why it
	// runs claude -p although interactive was asked for (the bridge fell back, or a recent fallback holds new starts
	// off); "" when it runs as asked.
	tui           bool
	tuiNote       string
	stderrTail    []string // its last assistantStderrTail stderr lines (watchAssistantStderr)
	claudeVersion string   // the Claude Code its system/init named (claude_code_version), "" before it

	// declaredWindow: the window the launcher declares (assistantCompaction.declared), 0 for none; replyModel: the
	// model of the conversation's last reply. compactIdleAt follows the window Claude Code reports for it (followWindow).
	declaredWindow int
	replyModel     string
	// toolSearch: what its start did about Claude Code's tool search, and endpoint where its requests go, "" Anthropic's
	// own API (assistant_tool_search.go). Set at the start, never changed.
	toolSearch toolSearchStart
	endpoint   string
	// repliedAt: when replyModel last answered (assistant_recovery.go: a transient "model does not exist" on it).
	repliedAt time.Time

	// usageChecked: how far the transcript was checked for an impossible usage at the end of its turns
	// (assistant_usage.go); 0: from the start.
	usageChecked int64

	turnError     string // the latest turn failed on the model service, as Codex's app-server said it (#2571)
	turnErrorKind string // proto.TurnErrorTransient, User or UsageLimit

	// written: the user's messages written to Claude, in order, until its command_lifecycle says it started, cancelled
	// or finished them; the ones it said it queued are the conversation's queue (assistant_queue.go, #4342).
	written []assistantInput
	// lifecycle: it printed a command_lifecycle line, so it says what became of every message (Claude Code 2.1.322
	// says queued within a millisecond of the write, measured); until it says it, a message may still be on its way in
	// (workingLocked).
	lifecycle bool

	// owner: the Node that started it, or the one that took it over from an earlier connection of a resident node
	// (assistant_keep.go); nil in synthetic tests that never went through spawn.
	owner atomic.Pointer[Node]
	// stopped: stop() has been invoked on this process, so its exit waiter sweeps any straggler descendants from its
	// process group before dropping it from liveAssistants and closing exited.
	stopped atomic.Bool

	// round: it runs a round of the background supervisor, resumed at the end of the conversation's first turn
	// (assistant_round.go). It is never compacted when idle: that would cut the first turn off the chain the next
	// round resumes on. It has started once it prints its init, not any line: a refused anchor prints a result first.
	round bool
	// toolSet: the tool set it was started with, "" every tool of the assistant's, roundToolSet a round's
	// (assistant_round.go). A message that needs another set restarts it once it settled (assistantSend).
	toolSet string
	// retire: a message that needs every tool went into this round's process while it worked; it is let go once it
	// settled (assistant_round.go roundSettled). settleArmed: that look is on its way (settleSoon).
	retire, settleArmed bool
	// awaited: a message that needs every tool waits for this round's process to end its turn (assistant_round.go
	// awaitRoundTurn); a Stop meanwhile ends the process at once and lets that message go in once it exited
	// (assistantInterrupt), and awaitStopped says so to the wait.
	awaited, awaitStopped bool
	// lastOut: when it last printed a line, as nanoseconds since born on the monotonic clock (readAssistant, printed):
	// it has settled once it printed nothing for assistantSettle, not working (assistant_round.go quiet). reading: the
	// line reader is on a line now, so the lines behind it are not read yet (readAssistant).
	lastOut atomic.Int64
	reading atomic.Int32
	// initSeen: it printed its system/init. Claude Code prints one as each turn starts (measured on 2.1.322): the first
	// starts the turn of the message that started the process, already busy, and every later one starts a turn the
	// node may not have asked for (readAssistant).
	initSeen bool
	// staleLooked: when its turn was last checked against Claude Code's own record (assistant_stale_turn.go).
	staleLooked time.Time
}

func (p *assistantProc) write(line []byte) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err := p.stdin.Write(append(line, '\n'))
	return err
}

// started marks the process as having started: its first JSON line came out (a round's: its init, readAssistant).
func (p *assistantProc) started() {
	if p.ready != nil {
		p.readyOnce.Do(func() { close(p.ready) })
	}
}

// hasStarted says started was called (or there is nothing to wait for).
func (p *assistantProc) hasStarted() bool {
	if p.ready == nil {
		return true
	}
	select {
	case <-p.ready:
		return true
	default:
		return false
	}
}

func (p *assistantProc) alive() bool {
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

// isAssistantSession tells assistant conversations (headless) apart from herdr sessions.
func (n *Node) isAssistantSession(id string) bool { return n.isAssistantSessionIn(id, nil) }

// isAssistantSessionIn is isAssistantSession with the assistant's project name and a Codex rollout's stat taken once
// per list pass (codexListPass).
func (n *Node) isAssistantSessionIn(id string, pass *codexListPass) bool {
	n.mu.Lock()
	_, ok := n.procs[id]
	a := n.agents[id]
	n.mu.Unlock()
	if ok || n.externalStarts.has(id) || n.isRoundFork(id) {
		return true
	}
	// A real host pane is stronger evidence than a working directory: users may resume a terminal session there.
	// Keep owned headless processes above this check, even if a stale host snapshot still mentions the session.
	if a.PaneID != "" {
		return false
	}
	if st, ok := n.state.get(id); ok && (identity.IsAssistantDir(st.Cwd) || n.roomState(st)) {
		return true
	}
	if p := n.transcriptPathIn(id, pass); p != "" && (compat.IsAssistantTranscript(filepath.Base(filepath.Dir(p))) ||
		isAssistantProject(filepath.Base(filepath.Dir(p)), pass.assistantProject(n))) {
		return true
	}
	// A conversation an agent runtime runs (assistant_runtime.go), listed before this node kept its state.
	if s, _, ok := n.runtimes.rowOf(id); ok && identity.IsAssistantDir(s.Cwd) {
		return true
	}
	return false
}

// assistantResumable: conversation id can be continued, its process is running or its transcript is still on disk.
// A scheduled task's own conversation is sent with resume_only, so one that is gone (cleaned up after 30 days, or
// never written) starts a new conversation at the hub instead of a blank one under the old id (docs/assistant/assistant.md
// "Scheduled tasks").
func (n *Node) assistantResumable(id string) bool {
	if _, _, known := n.runtimes.belongs(id); known { // runtime ownership is independent of local state or id shape
		return true
	}
	if id == "" || !validID(id) {
		return false
	}
	n.mu.Lock()
	p := n.procs[id]
	n.mu.Unlock()
	return (p != nil && p.alive()) || n.transcriptPath(id) != ""
}

func (n *Node) isManagedRuntimeLauncher(name string) bool {
	if name == "" {
		return false
	}
	if m, ok := n.managedLauncher(name); ok && m.AgentType() == "agy" {
		return true
	}
	if n.autoCompanions && !shellLauncherNames()[name] && (name == agyRuntimeID || name == opencodeRuntimeID) {
		return true
	}
	return false
}

func (n *Node) pendingAutoCompanionRuntime(id string) (string, bool) {
	if !n.autoCompanions || !runtimeSessionID(id) {
		return "", false
	}
	for _, rtID := range []string{agyRuntimeID, opencodeRuntimeID} {
		if strings.HasPrefix(id, rtID+"-") {
			return rtID, true
		}
	}
	return "", false
}

// runtimeResumablePending reports whether id looks like an existing runtime conversation whose runtime has not yet
// connected or finished pushing its first session list after a node restart.
func (n *Node) runtimeResumablePending(id, launcher string) bool {
	if id == "" || n.runtimes == nil {
		return false
	}
	var st sessionState
	var hasState bool
	if n.state != nil {
		st, hasState = n.state.get(id)
	}
	rl := launcherOr(st.Launcher, launcher)
	rt := n.assistantRuntime(rl)
	if rt == nil && launcher != "" && launcher != rl {
		rt = n.assistantRuntime(launcher)
	}
	if rt == nil && runtimeSessionID(id) {
		rt, _ = n.runtimes.assistantRuntimeByPrefix(id)
	}
	if rt != nil {
		return !rt.listedSessions()
	}
	if hasState && (runtimeSessionID(id) || st.EngineRuntime != "" || st.RemoteAgent || n.isManagedRuntimeLauncher(rl)) {
		return true
	}
	if n.isManagedRuntimeLauncher(launcher) {
		return true
	}
	if runtimeSessionID(id) {
		if _, pending := n.runtimes.unlistedAssistantRuntime(); pending {
			return true
		}
		if _, pending := n.pendingAutoCompanionRuntime(id); pending {
			return true
		}
	}
	return false
}

// checkAssistantResumable verifies that a resume_only send may continue conversation id.
// Unlike assistantResumable, it distinguishes a truly missing conversation ("is gone") from a runtime conversation
// whose owning runtime has not connected or pushed its initial session list yet after a restart (errRuntimeAway),
// and probes a ready runtime for older sessions outside its recent session window before concluding gone.
func (n *Node) checkAssistantResumable(ctx context.Context, id, launcher string) error {
	if n.assistantResumable(id) {
		return nil
	}
	if id == "" {
		return fmt.Errorf("conversation %s is gone", id)
	}
	if n.state != nil && n.state.isLoading() {
		if err := n.state.loaded(ctx); err != nil {
			return err
		}
		if n.assistantResumable(id) {
			return nil
		}
	}
	if n.runtimes == nil {
		return fmt.Errorf("conversation %s is gone", id)
	}
	var st sessionState
	var hasState bool
	if n.state != nil {
		st, hasState = n.state.get(id)
	}
	rl := launcherOr(st.Launcher, launcher)
	rt := n.assistantRuntime(rl)
	if rt == nil && launcher != "" && launcher != rl {
		rt = n.assistantRuntime(launcher)
	}
	if rt == nil && runtimeSessionID(id) {
		rt, _ = n.runtimes.assistantRuntimeByPrefix(id)
	}
	if rt != nil {
		if !rt.listedSessions() {
			return errRuntimeAway(cmp.Or(rt.label(), rl, "runtime"))
		}
		if !hasState {
			return fmt.Errorf("conversation %s is gone", id)
		}
		if gone := n.runtimes.goneOf(id); gone != nil {
			return fmt.Errorf("conversation %s is gone", id)
		}
		if rt.peer == nil {
			return errRuntimeAway(cmp.Or(rt.label(), rl, "runtime"))
		}
		cctx, cancel := context.WithTimeout(ctx, runtimeClaimTimeout)
		var page struct {
			Items []json.RawMessage `json:"items"`
		}
		err := rt.peer.Call(cctx, "session.items", map[string]any{"session_id": id, "limit": 1}, &page)
		cancel()
		if hasSession(err) {
			n.runtimes.claim(rt, id)
			return nil
		}
		if rpcCode(err) == codeNotFound {
			n.runtimes.unclaim(id, err)
			return fmt.Errorf("conversation %s is gone", id)
		}
		return errRuntimeAway(cmp.Or(rt.label(), rl, "runtime"))
	}
	if hasState && (runtimeSessionID(id) || st.EngineRuntime != "" || st.RemoteAgent || n.isManagedRuntimeLauncher(rl)) {
		return errRuntimeAway(cmp.Or(rl, st.EngineRuntime, "runtime"))
	}
	if n.isManagedRuntimeLauncher(launcher) {
		return errRuntimeAway(launcher)
	}
	if runtimeSessionID(id) {
		if label, pending := n.runtimes.unlistedAssistantRuntime(); pending {
			return errRuntimeAway(cmp.Or(rl, label, "runtime"))
		}
		if rtID, pending := n.pendingAutoCompanionRuntime(id); pending {
			return errRuntimeAway(cmp.Or(rl, rtID, "runtime"))
		}
	}
	return fmt.Errorf("conversation %s is gone", id)
}

type sendTraceKey struct{}

type assistantSendTiming struct {
	node  *Node
	id    string
	start time.Time
}

func (s assistantSendTiming) mark(phase string, ok bool) {
	s.node.logf("send_timing trace=%s phase=%s elapsed_ms=%d ok=%t", s.id, phase, time.Since(s.start).Milliseconds(), ok)
}

func serviceTierRefusal(ctx context.Context, code string) error {
	return refusal(code, i18n.T(i18n.From(ctx), "rpc-errors."+code), nil)
}

// assistantInputOrigin resolves an existing conversation's origin for both content checks and sends.
func assistantInputOrigin(st sessionState, requested modelExecution, resume bool) (origin string, known bool) {
	origin, known = requested.Origin, requested.OriginKnown || st.OriginKnown || !resume
	if resume && !requested.OriginKnown && (st.OriginKnown || st.Origin != "" || origin == "") {
		// A persisted decision, including a legacy non-empty Inbox origin, wins over an unattested caller value.
		origin = st.Origin
	}
	return origin, known
}

// assistantSend delivers a user message, starting or restarting the headless claude when needed.
// id == "" starts a new conversation and returns its id.
func (n *Node) assistantSend(ctx context.Context, id, cwd, launcher, text string, images []proto.Image, mode, effort string, options ...modelExecution) (_ string, sendErr error) {
	if text != "" || len(images) != 0 {
		n.inputCacheWarm(id)
	}
	clientID, _ := ctx.Value(sendTraceKey{}).(string)
	timing := assistantSendTiming{node: n, id: proto.SendTraceID(clientID), start: time.Now()}
	timing.mark("assistant_received", true)
	if launcher != "" && !shellcmd.NameRe.MatchString(launcher) {
		return "", fmt.Errorf("invalid launcher %q", launcher)
	}
	strictRuntime := n.strictRuntimeSession(id, executionOption(options).Strict)
	if id != "" && !validID(id) && !n.runtimeAssistant(id) && !strictRuntime {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	externalLauncher := launcher
	if externalLauncher == "" && id != "" {
		if saved, ok := n.state.get(id); ok {
			externalLauncher = saved.Launcher
		}
	}
	if proto.IsExternalLauncher(externalLauncher) {
		key := cmp.Or(id, newIDFrom(ctx))
		if key != "" {
			var finish func()
			ctx, finish = n.beginExternalStartup(ctx, key)
			defer finish()
		}
	}
	var previousStopAt int64
	if id != "" {
		defer n.typing.lock(id)()
		state, _ := n.state.get(id)
		previousStopAt = state.StopCauseAt
		if err := n.a2aSendReady(ctx, id); err != nil {
			return "", err
		}
	}
	if state, ok := n.state.get(id); ok && state.SegmentRetired {
		return "", refusal("conversation_retired", i18n.T(i18n.From(ctx), "rpc-errors.conversation_retired"), nil)
	}
	if externalStartupOf(ctx) != nil && ctx.Err() != nil {
		return "", externalAdapterError(ctx, ctx.Err())
	}
	timing.mark("typing_locked", true)
	if err := n.answerAgainCurrent(ctx, id); err != nil {
		return "", err
	}
	if strings.HasPrefix(clientMsgIDFrom(ctx), "model-retry:") {
		st, _ := n.state.get(id)
		if st.StopCause != "" && !st.StopCause.AllowsSupervisorResume() {
			return "", n.stoppedRefusal(id)
		}
		if st.TaskExecution {
			return "", errAssistantRecoveryCancelled // a stale manager wake cannot take over the task's strict retry
		}
	}
	recovery, restart := assistantRecoveryOf(ctx)
	recoverySwitched := false
	if recovery != nil {
		n.mu.Lock()
		valid := n.recoveries[id] == recovery
		recoverySwitched = valid && recovery.switched
		p := n.procs[id]
		n.mu.Unlock()
		if !valid {
			return "", errAssistantRecoveryCancelled
		}
		if restart {
			if p == nil {
				return "", errAssistantRecoveryTasks
			}
			// A successful explicit empty list is the only proof that a restart kills no background work.
			if p.cx != nil {
				p.cx.syncTasks()
			} else if p.agent == "claude" {
				p.syncClaudeTasks(ctx)
			}
			p.mu.Lock()
			safe := p.tasksObserved && len(p.tasks) == 0
			p.mu.Unlock()
			if !safe {
				return "", errAssistantRecoveryTasks
			}
			// The old process stops below, once the new start is checked: one refused here keeps the conversation (#3495).
		}
	} else {
		n.cancelAssistantRecovery(id)
	}
	restartForRecovery := recovery != nil && restart
	resume := id != ""
	st, _ := n.state.get(id)
	if executionOption(options).Strict {
		if err := n.checkTaskDirectory(id, cwd, launcher); err != nil {
			return "", err
		}
		_, _, strictRuntime = n.runtimes.belongs(id)
	}
	// External authority and the raw runtime must be checked before guest/resume normalization can replace options.
	requested := executionOption(options)
	callerModeExplicit := requested.PermissionModeExplicit && mode != ""
	public := requested.Public // audience belongs to this input, even when launcher normalization replaces execution
	requestedLauncher := launcherOr(launcher, st.Launcher)
	if proto.IsExternalLauncher(requestedLauncher) || requested.External != nil {
		bindingID := id
		if bindingID == "" {
			bindingID = newIDFrom(ctx)
		}
		if err := n.checkExternalAssistant(ctx, bindingID, requestedLauncher, requested, false); err != nil {
			return "", err
		}
	}
	// Read and consume the one-shot choice before guest or launcher normalization can replace options. A resumed send
	// must never lose an explicit tier and quietly continue on the thread's current speed.
	startTier := executionOption(options).ServiceTier
	if startTier != "" {
		options = slices.Clone(options)
		for i := range options {
			options[i].ServiceTier = ""
		}
		if resume {
			return "", serviceTierRefusal(ctx, "assistant_service_tier_start_only")
		}
	}
	if err := n.validAssistantModes(ctx, launcherOr(launcher, st.Launcher), mode, effort); err != nil {
		return "", err
	}
	isMain, mainWindow := executionOption(options).IsMain, executionOption(options).MainCompactWindow
	policy := executionOption(options).SupervisionInstructions
	policyResolved := executionOption(options).SupervisionResolved || policy != ""
	if !policyResolved {
		policy = st.SupervisionInstructions
	}
	interject := executionOption(options).Interject // this message, not the conversation (assistant_interject.go)
	if len(options) == 0 {
		isMain, mainWindow = st.IsMain, st.MainCompactWindow
	}
	// A guest conversation (assistant_guest.go): a new one asked for, or one that is.
	guest, guestKind := st.Guest, st.GuestKind
	if !resume {
		guest, guestKind = guestFrom(ctx), guestKindFrom(ctx)
	} else if n.roomState(st) {
		// A meeting's room: never resumed without its tools (its record lost them: refused), nor without its kind
		// (lost by a manager that did not keep it: its directory says, and the record has it again).
		if guest == nil {
			return "", errRoomRecord
		}
		if guestKind != guestMeeting {
			guestKind, st.GuestKind = guestMeeting, guestMeeting
			n.state.put(id, st)
		}
	}
	origin, originKnown := assistantInputOrigin(st, requested, resume)
	if resume && requested.OriginDecisionRequired && !originKnown && guest == nil {
		// A pre-upgrade record has no ordinary-origin decision. Neither an older manager's send nor a non-empty caller
		// origin proves one; wait for the new manager's explicit origin_known instead of starting ordinary built-ins.
		return "", errAssistantOriginUnknown
	}
	if mode == "" && guest == nil {
		running := false
		onRuntime := false
		runtimeAgent := ""
		if resume {
			n.mu.Lock()
			p := n.procs[id]
			running = p != nil && p.alive()
			n.mu.Unlock()
			if row, h, ok := n.runtimes.rowOf(id); ok {
				onRuntime = true
				running = row.Status != proto.StatusOffline
				runtimeAgent = cmp.Or(row.Agent, h.agent())
			} else {
				_, _, runtimeOwned := n.runtimes.belongs(id)
				onRuntime = runtimeOwned || n.runtimeAssistant(id)
			}
		}
		// A deliberate conversation choice wins. For Claude Code, a stopped/new process follows the manager's current
		// default; legacy AppMode/transcript evidence is used only when an older manager supplied no default. Other
		// agents always keep that legacy precedence, and a live process keeps its mode until a later stopped resume.
		if st.AppModeExplicit {
			mode = st.AppMode
		}
		if mode == "" && !running && requested.DefaultPermissionMode != "" {
			defaultAgent := ""
			if resume {
				if onRuntime {
					defaultAgent = cmp.Or(runtimeAgent, assistantOnRuntime)
				} else {
					defaultAgent = n.conversationAgent(id)
				}
			}
			if defaultAgent == "" {
				defaultAgent, _ = n.assistantAgent(ctx, launcherOr(requestedLauncher, "claude"))
			}
			if defaultAgent == "claude" {
				mode = requested.DefaultPermissionMode
			}
		}
		if mode == "" {
			mode = st.AppMode
		}
		if mode == "" {
			mode = n.assistantModeOf(id)
		}
		if mode == "" {
			mode = proto.ModeBypass
		}
	}
	if origin == originInbox && guest == nil {
		// Only the manager's dedicated field may carry the AI twin's own saved mode. The generic caller mode and an older
		// manager's missing field stay in default. A live process keeps its mode; a new or stopped process uses the setting.
		keepCurrent := false
		if resume {
			n.mu.Lock()
			p := n.procs[id]
			keepCurrent = p != nil && p.alive()
			n.mu.Unlock()
		}
		current := ""
		if keepCurrent {
			// The incoming generic mode is caller-controlled, not evidence of what this process runs. Read the live
			// conversation's actual mode so neither an old nor a current manager can elevate it through that field.
			current, _ = n.modesOf(id)
			current = cmp.Or(current, st.AppMode, proto.ModeDefault)
		}
		mode = inboxPermissionMode(requested.InboxPermissionMode, current, keepCurrent)
	}
	if err := checkFileReferenceAudience(ctx, text, origin, guest != nil, public); err != nil {
		return "", err
	}
	if guest != nil {
		if refs, err := assistantskills.References(text); err != nil || len(refs) != 0 {
			return "", skillSelectionError(assistantskills.ErrReference)
		}
		if resume {
			launcher, options = "", nil // it keeps what it started on (the hub sends none either)
		}
	}
	crossEngineResume := false
	if own, switched := n.assistantResumeLauncher(ctx, id, launcher, st); switched && !strictRuntime {
		if executionOption(options).Strict {
			return "", errors.New("task execution context differs from this conversation; start a new conversation")
		}
		n.logger.Printf("assistant %s: resumes on %s, the kind of agent it was started on, not on %s", short(id), launcherOr(own, st.Launcher), launcher)
		launcher, options = own, nil // the model picked for the other launcher is not this one's
		crossEngineResume = true
	}
	execution := executionOption(options)
	execution.Public = public
	execution.Origin = origin
	// A new conversation has no legacy origin to recover. A resumed one gets here only with the manager's explicit
	// decision or a previously persisted one; internal sends retain the decision already in session state.
	execution.OriginKnown = originKnown
	if execution.Source.Role == "" {
		execution.Source = st.ModelSource
	}
	// An agent runtime's launcher, or a conversation one runs (assistant_runtime.go): it keeps its model; the node
	// keeps the conversation's state, snapshot, mode and memory.
	rl := launcherOr(launcher, st.Launcher)
	if proto.IsExternalLauncher(rl) {
		execution.IsMain = isMain && guest == nil
		bindingID := id
		if bindingID == "" {
			bindingID = newIDFrom(ctx)
		}
		if err := n.checkExternalAssistant(ctx, bindingID, rl, execution, false); err != nil {
			return "", err
		}
	}
	if onRuntime, runs := n.assistantRuntimeRoute(id, rl, strictRuntime); guest != nil && (onRuntime != nil || runs) {
		return "", errGuestAgent
	} else if onRuntime != nil || runs {
		if refs, err := promptcontent.Files(text); err != nil || len(refs) > 0 {
			return "", fileRuntimeUnsupported(ctx)
		}
		if origin == originInbox {
			// External runtimes do not yet attest the origin on their first callback. Refuse rather than leave an
			// unguarded startup window; built-in Claude and Codex carry it in their process and session state.
			return "", errors.New("inbox handling requires a built-in Claude or Codex launcher")
		}
		if startTier != "" {
			return "", serviceTierRefusal(ctx, "assistant_service_tier_unsupported")
		}
		if execution.ConfigDir != "" {
			return "", errors.New(assistantwire.ConfigDirClaudeOnly)
		}
		if id != "" && (onRuntime == nil || !runs) && !(runs && launcher == "") { // another kind of agent than its own
			return "", errors.New("换到另一种 Agent 需要新建对话")
		}
		skillCwd := n.runtimeAssistantCwd(id, cwd, st)
		selected, err := n.selectedSkills(ctx, id, skillCwd, rl, text, execution)
		if err != nil {
			return "", err
		}
		eff := effort
		if eff == "" {
			eff = execution.Effort
		}
		superviseRoundFrom(ctx).set(roundKept, roundWhyUnsupported) // a runtime keeps its own context (assistant_round.go)
		return n.assistantSendRuntime(ctx, onRuntime, id, cwd, rl, text, images, mode, execution.Public, execution.Strict, callerModeExplicit, selected, previousStopAt, execution.Model, eff, policy)
	}
	if len(options) == 0 || (execution.Fingerprint == "" && execution.Model == "") {
		if launcher == "" || launcher == st.Launcher {
			// What it ran, with this message's claude_interactive: the manager's switch, sent with every message, not a
			// model pick (assistant_tui.go). Without it a message that picks no model (a supervision message, a relay)
			// started claude -p, and restarted a conversation that runs interactive Claude Code.
			execution = modelExecution{Source: execution.Source, CacheKeepalive: execution.CacheKeepalive, Strict: execution.Strict, Model: st.Model, Effort: st.ModelEffort, UseEffort: st.ModelFingerprint != "", Fingerprint: st.ModelFingerprint, SupervisionInstructions: st.SupervisionInstructions, Origin: origin, OriginKnown: originKnown, Interactive: execution.Interactive, FollowsSetting: execution.FollowsSetting || st.FollowsSetting || (crossEngineResume && requested.FollowsSetting), PersistedModel: st.Model != "" && guest == nil}
		}
	}
	if launcher != "" && st.Launcher != "" && launcher != st.Launcher && execution.Fingerprint == "" {
		execution.UseEffort = true
		execution.Fingerprint = "launcher-default"
	}
	execution.Public = public
	if effort != "" {
		execution.Effort = effort
	}
	if execution.PersistedModel && execution.Model != "" && !proto.ValidModelID(execution.Model) {
		execution.Model, execution.Effort, execution.UseEffort, execution.Fingerprint, execution.PersistedModel = "", effort, true, "launcher-default", false
	}
	if err := execution.checkModel(); err != nil {
		return "", err
	}
	if err := n.validAssistantModes(ctx, rl, "", execution.Effort); err != nil {
		return "", err
	}
	if launcher == "" && id != "" {
		launcher = st.Launcher
	}
	if launcher == "" {
		launcher = "claude"
	}
	if err := n.ensureManaged(ctx, launcher); err != nil { // the conversation's own provider (managed.go)
		return "", err
	}
	if execution.PersistedModel && execution.Model != "" {
		n.sanitizePersistedModel(ctx, id, launcher, st, &execution, crossEngineResume, isMain || st.IsMain || execution.FollowsSetting, effort)
	}
	if crossEngineResume && execution.Model == "" && execution.Fingerprint == "" && guest == nil {
		execution.UseEffort = true
		execution.Fingerprint = "launcher-default"
	}
	execution.IsMain = isMain && guest == nil
	protected := strictProtectedExecution(execution) || n.recoveryProtected(id)
	if recovery == nil && id != "" && protected {
		// Discard a pre-upgrade fallback hold/marker. The manager's current model choice is authoritative,
		// even while that model is still quota-limited; do not probe or substitute another execution.
		n.mu.Lock()
		legacyHold := n.fallbacks[id] != nil
		n.mu.Unlock()
		if legacyHold || n.modelRecovery(id) != nil {
			n.releaseModelRecovery(id, execution.Model)
		}
	}
	if recovery == nil && id != "" && !protected && !execution.Strict && !proto.IsExternalLauncher(launcher) {
		askedModel := execution.Model
		// The fallback that answered (#3788). holdExpired is declared apart: ":=" would shadow execution in this block.
		var holdExpired bool
		execution, holdExpired = n.heldFallbackWithExpiry(id, launcher, execution)
		execution = n.applyModelRecoveryReturn(ctx, id, askedModel, execution, holdExpired)
	}
	execution.IsMain = isMain && guest == nil
	// The window the manager holds the main conversation and each leader conversation to (#7314, #15318), never a
	// guest's. Only a managed launcher's start applies it (capWindow): on another one it would only change the
	// fingerprint and restart the conversation for nothing.
	execution.MainCompactWindow = 0
	if guest == nil {
		execution.MainCompactWindow = n.mainWindowOn(launcher, mainWindow)
	}
	execution.SupervisionInstructions = st.SupervisionInstructions
	if policyResolved {
		execution.SupervisionInstructions = policy
	}
	if guest != nil {
		execution.SupervisionInstructions = ""
	}
	var existing *assistantProc
	if proto.IsExternalLauncher(launcher) {
		n.mu.Lock()
		existing = n.procs[id]
		n.mu.Unlock()
	}
	if execution.Model != "" && !sameExternalProcessModel(existing, launcher, execution.Model) {
		if _, err := n.launcherModelCheck(ctx, launcher, execution.Model); err != nil {
			return "", err
		}
	}
	execution.IsMain = isMain && guest == nil
	agent, err := n.assistantAgent(ctx, launcher)
	if err != nil {
		return "", err
	}
	if agent == traexAgent {
		if origin != originInbox || !originKnown {
			return "", errTraexAssistantOrigin
		}
		mode = inboxPermissionMode(mode, "", false)
	}
	if startTier != "" && agent != "codex" {
		return "", serviceTierRefusal(ctx, "assistant_service_tier_unsupported")
	}
	if guest != nil && agent != "claude" {
		return "", errGuestAgent
	}
	asked := ""
	if agent == "claude" {
		if _, err := n.resolveClaudeModelArg(ctx, launcher, execution.Model); err != nil {
			return "", err
		}
		asked = n.startProfile(executionOption(options).ConfigDir, guestKind, resume)
		execution.ConfigDir, err = n.assistantProfileSelection(ctx, id, launcher, asked, resume)
		if err != nil {
			return "", err
		}
	} else if executionOption(options).ConfigDir != "" {
		return "", errors.New(assistantwire.ConfigDirClaudeOnly)
	}
	if proto.IsExternalLauncher(launcher) {
		execution.StartupContext = ctx
	}
	// Validate the file scope before taking a spare or persisting a conversation.
	fileCwd := cwd
	if fileCwd == "" {
		fileCwd = n.storedCwd(st.Cwd)
	}
	files, err := n.selectedFiles(ctx, text, fileCwd, launcher, execution.Runtime)
	if err != nil {
		return "", err
	}
	if len(files) > 0 {
		// An empty cwd would let a new send adopt a spare in any directory. Require the directory that was checked.
		cwd = fileCwd
	}
	var p *assistantProc
	if id == "" && guest != nil {
		// Never the warm spare: it was started with the assistant's tools. Its process reads Guest from the state.
		// Under the id the hub asked for when it gave one (a meeting's room: made once, also across a restart).
		id = cmp.Or(newIDFrom(ctx), newUUID())
		n.state.put(id, sessionState{Launcher: launcher, Cwd: cwd, Guest: guest, GuestKind: guestKind})
	} else if id == "" && newIDFrom(ctx) != "" && agent == "claude" {
		id = newIDFrom(ctx) // never the spare either: it has an id of its own
	} else if id == "" {
		// The spare was started without --permission-mode, and bypassPermissions can't be switched to later.
		if p = n.adoptSpareFor(cwd, launcher, mode, execution); p != nil {
			id, launcher, cwd = p.id, p.launcher, p.cwd
			p.mu.Lock()
			p.origin = origin // no model turn ran as a spare; every later MCP call now carries this conversation's origin
			p.mu.Unlock()
		} else if agent == "claude" {
			id = newUUID()
		}
	}
	if id != "" && (origin != "" || resume && originKnown) {
		// A Claude process can discover MCP as soon as it starts. Persist the origin before spawning it so even that
		// first request is classified as this conversation, not as an ordinary Agent with the same session id. An
		// explicitly ordinary resumed decision must also clear a persisted Inbox origin before tool selection and spawn.
		st.Origin, st.OriginKnown = origin, originKnown
		if origin != "" {
			st.Launcher = launcher
			// Recovery sends an empty cwd to mean the persisted directory. Do not clear it before storedCwd below reads it.
			st.Cwd = cmp.Or(cwd, st.Cwd)
		}
		n.state.put(id, st)
	}
	// A reused warm process is already running: its profile needs no sign-in check (a spare is only adopted in the
	// start's own profile). A cold start asks before anything is spawned.
	if p == nil && agent == "claude" && !resume {
		if err := n.assistantNewProfileAuth(ctx, launcher, asked, execution.ConfigDir); err != nil {
			return "", err
		}
	}
	timing.mark("preflight_done", true)
	if cwd == "" {
		cwd = fileCwd
	}
	selectionSession := id
	if !resume {
		selectionSession = "" // a newly allocated Claude id has no resumable profile yet
	}
	selected, err := n.selectedSkills(ctx, selectionSession, cwd, launcher, text, execution)
	if err != nil {
		return "", err
	}
	selectedLauncher, selectedExecution := launcher, execution
	// The live snapshot the message starts with, asked for while the process starts (assistant_context.go). A new
	// Codex conversation has no id yet: it can't be the main one.
	hasInput := strings.TrimSpace(text) != "" || len(images) > 0
	snapc := make(chan contextAnswer, 1)
	if guest != nil || !hasInput || execution.Public {
		snapc <- contextAnswer{} // guests and public mentions get no owner state; empty starts leave it for the first real message
	} else {
		go func(id, cwd, query string) { snapc <- n.assistantContextForCwd(id, cwd, query) }(id, cwd, text)
	}
	if id == "" { // a new app-server conversation: the server picks the thread ID
		if p, err = n.spawnAssistantAppServer(agent, "", cwd, launcher, false, false, mode, execution); err != nil {
			return "", err
		}
		id = p.id
	}

	// One at a time with restoreModel and compactions on this conversation's process (model_restore.go): a restore
	// either sees the process this send made busy, or this send starts a new one after the restore closed it.
	defer n.assistantLife.lock(id)()
	timing.mark("lifecycle_locked", true)
	n.mu.Lock()
	p = n.procs[id]
	n.mu.Unlock()
	// A trailing refused turn stays in the transcript and, since Claude Code rebuilds the messages of a resume from the
	// newest chain, would be merged into the next ordinary prompt. Stop the idle process that holds the refused turn in
	// memory (and its sessions/<pid>.json lock file) first, then prune the refused tail so the next spawn resumes cold
	// on clean history. Skip during immediate model-switch recovery (recovery != nil): sendAssistantRecovery sends only
	// prompts.RenderModelRetry(), which relies on the interrupted request still being in the transcript for the
	// fallback model to answer.
	if agent == "claude" && id != "" && recovery == nil && (hasInput || resume) {
		canHeal := p == nil || !p.alive()
		if !canHeal {
			p.mu.Lock()
			canHeal = !p.workingLocked() && len(p.tasks) == 0
			p.mu.Unlock()
		}
		if canHeal && n.transcriptHasTrailingClaudeRefusal(id) {
			if p != nil {
				if held := n.takeAssistant(id); held != nil {
					stopAndWait([]*assistantProc{held})
				}
				n.rememberTasks(id, nil)
				p = nil
			}
			if pruned, clean := n.healClaudeRefusalTranscript(id); pruned {
				n.mu.Lock()
				delete(n.ctxSent, id) // a fresh process takes the whole snapshot
				n.mu.Unlock()
				if !clean { // its file went with its only turn: started again below, an empty chat meanwhile
					drop := n.startsBegun.begin(id, n.startWait())
					defer func() {
						if sendErr != nil {
							drop()
						}
					}()
				}
			}
		}
	}
	// Claude takes bypassPermissions only at start; Codex gets the policy with every turn (codexPolicy).
	if p != nil && p.alive() && p.agent != agent {
		return "", errors.New("换到另一种 Agent 需要新建对话")
	}
	// A round of the background supervisor resumes at the end of its first turn, in a new process (assistant_round.go):
	// the idle one stops once it settled, and the round gets the whole snapshot, the earlier ones being out of its
	// context. One that began to work meanwhile (a turn Claude Code started on its own) takes the round as before.
	var round spawnRound
	joins := false // the round goes in as before because the process works: it joins that process (below)
	if r := superviseRoundFrom(ctx); r != nil {
		round = n.superviseRound(ctx, r, id, p, agent, guest != nil)
		if round.anchor != "" && p != nil && !n.stopSettled(ctx, p) {
			r.set(roundKept, roundWhyBusy)
			n.logf("assistant %s: supervision round goes in as before (%s)", short(id), roundWhyBusy)
			round = spawnRound{}
		}
		joins = r.keptBusy()
		if round.anchor != "" {
			if p != nil {
				n.rememberTasks(id, nil) // idle, with no background task (superviseRound)
				p = nil
			}
			n.mu.Lock()
			delete(n.ctxSent, id)
			n.mu.Unlock()
		}
	}
	// The mode this message runs in: its own, or the one the app set (persisted, it outlives the node).
	want := mode
	if want == "" && p != nil {
		want, _ = n.modesOf(id)
	}
	// The tool set this message needs is round.toolSet: the round's when the round starts from the first turn (p is
	// gone by then), else every tool: the owner's message, a supervision message once
	// assistant.background_supervisor_keeps_context is on, any message of a manager that asks for no round, and a round
	// that goes in as before. A process a round started has fewer tools and is never idle-compacted, so once it settled
	// it is replaced by a plain --resume with every tool, compacted when idle as any other process (assistant_round.go).
	otherTools := p != nil && p.toolSet != round.toolSet
	if otherTools && p.round && superviseRoundFrom(ctx) == nil && recovery == nil {
		// A message that needs every tool, for a round's process at work: it waits for the turn to end (bounded), so it
		// goes into a plain resume with every tool below rather than into the round's process (assistant_round.go).
		n.awaitRoundTurn(ctx, p)
	}
	currentExecution := execution
	// Pin only a process this send can actually retain. A restart for tools, permissions, instructions, categories or recovery must
	// use the manager's current revision, never the old process's binding in a new process.
	categoriesStale := guest == nil && n.categoriesStale(p, cwd)
	if proto.IsExternalLauncher(launcher) && p != nil && p.alive() && !otherTools && !restartForRecovery && !p.needsRestartFor(want) && !p.instructionsStale() && !categoriesStale {
		execution = pinExternalExecution(p, launcher, execution)
	}
	executionChanged := p != nil && (p.launcher != launcher || p.fingerprint != n.executionFingerprint(launcher, execution) || n.tuiEffortStale(p, execution))
	if p != nil && (!p.alive() || executionChanged || p.needsRestartFor(want) || p.instructionsStale() || otherTools || n.toolSearchStale(p) || categoriesStale) {
		p.mu.Lock()
		originChanged := assistantOriginToolPolicy(p.origin) != assistantOriginToolPolicy(origin) ||
			assistantOriginToolPolicy(p.model.Origin) != assistantOriginToolPolicy(origin)
		active := p.busy || len(p.tasks) > 0
		if originChanged {
			// The old built-in policy must also survive compactions and accepted or queued input that has not started a turn.
			active = p.workingLocked()
		}
		if categoriesStale {
			// Do not interrupt active compactions or accepted/queued input when recycling for PERSONA_CATEGORIES.
			active = p.workingLocked()
		}
		if proto.IsExternalLauncher(p.launcher) || proto.IsExternalLauncher(launcher) {
			active = p.workingLocked() // compactions and accepted/unacknowledged queued input also belong to this process
		}
		if otherTools {
			// A round's process is not replaced for its tools while anything in it would be lost (workingLocked): also
			// a compaction, the user's included, which sets no busy flag, and a message Claude Code queued or started.
			// It is what superviseRound calls busy, so a round it keeps as busy joins this process, as its answer says:
			// also one kept because the process did not settle while stopSettled waited (it printed all along), which
			// may have gone quiet by now. A round kept for another reason (no anchor) needs every tool like any message.
			active = p.workingLocked() || joins
		}
		roleChanged := p.model.SupervisionInstructions != execution.SupervisionInstructions
		p.mu.Unlock()
		alive := p.alive()
		if otherTools && !active && alive && !restartForRecovery {
			active = !n.stopSettled(ctx, p) // once it settled: one that began to work meanwhile is not cut
		}
		if active && p.alive() && !restartForRecovery { // a recovery's busy is its own hold on the failed turn
			if originChanged {
				// Unlike a model switch, an origin change cannot queue onto the old process: its built-in tools are the
				// security boundary. Keep the old work intact and let the manager retry once the process is idle.
				return "", errAssistantOriginBusy
			}
			if proto.IsExternalLauncher(p.launcher) || proto.IsExternalLauncher(launcher) {
				return "", externalRefusal(ctx, "adapter_busy")
			}
			if roleChanged {
				// Do not kill active work, or deliver a new role's message on the old system
				// policy. Manager-generated messages remain queued for an idle retry.
				return "", refusal(assistantwire.CodeConversationBusy, i18n.T(i18n.From(ctx), "rpc-errors.assistant_supervision_busy"), nil)
			}
			if categoriesStale && n.categoriesStricterFor(p, cwd) {
				// Do not kill active work, or deliver a turn onto a process whose PERSONA_CATEGORIES
				// lacks newly accumulated categories or stays known while categories are unknown.
				return "", refusal(assistantwire.CodeConversationBusy, i18n.T(i18n.From(ctx), "rpc-errors.assistant_categories_busy"), nil)
			}
			if execution.Strict && executionChanged {
				return "", errors.New("task execution context differs from this conversation; start a new conversation")
			}
			if otherTools && superviseRoundFrom(ctx) == nil && recovery == nil {
				// A message that needs every tool, for a round's process still at work after awaitRoundTurn (past its
				// bound, only background tasks running, or a turn Claude Code began after the wait looked): it goes in as
				// Claude Code queues it and runs once on the round's tools, and the process is let go once it settled
				// (assistant_round.go roundSettled), so the next message starts with every tool. A round kept as busy and a
				// recovery's retry of the round's turn are the round's own.
				p.mu.Lock()
				p.retire = true
				p.mu.Unlock()
				n.logf("assistant %s: a supervision round's process is still working: the message goes in on the round's tools, and the process is let go once it settled", short(id))
			}
			// Answering, or running background tasks a new process would kill: the message goes to this process on
			// what it runs, and the switch waits for the first message after it is idle. Refusing it left the user
			// unable to send while the conversation kept starting tasks (#1554).
			p.mu.Lock()
			launcher, execution = p.launcher, p.model
			if p.needsRestartForLocked(mode) {
				mode = "" // start-only: noted as applied, it would never get its new process
			}
			p.mu.Unlock()
		} else {
			if otherTools && alive {
				n.logf("assistant %s: started with a supervision round's tools: restarted with every tool for this message", short(id)) // settled, or a recovery's restart
			}
			n.stopAssistantProcess(id) // model switch, dead process, bypassPermissions (start-only), new instructions or tools: resume in a fresh one
			n.rememberTasks(id, nil)
			p = nil
			if proto.IsExternalLauncher(launcher) {
				execution = currentExecution
			}
		}
	}
	// Model recovery or adoption of an existing process may have replaced execution above. Carry the manager's origin
	// decision through that replacement before tool selection and persistence, including an explicitly ordinary empty origin.
	execution.Origin = origin
	execution.OriginKnown = originKnown || execution.OriginKnown
	if startTier == "" {
		n.noteModes(id, mode, effort) // the ordinary start keeps its original ordering
	}
	fresh := p == nil
	if p == nil {
		if p, err = n.spawnAssistantWith(id, cwd, launcher, resume && (agent == traexAgent || n.transcriptPath(id) != ""), false, round, execution); err != nil {
			if execution.PersistedModel && execution.Model != "" && assistantModelRouteUnavailable(err.Error()) {
				n.noteModelRouteUnavailable(id, launcher, execution.Model)
				n.clearStalePersistedModel(id, launcher, execution.Model)
			}
			return "", err
		}
	}
	if title, preparing := ctx.Value(segmentTitleKey{}).(string); preparing && p.cx != nil {
		// Codex writes the empty rollout when named. The receipt must remain resumable after idle release.
		if err := p.cx.call(ctx, "thread/name/set", map[string]any{"threadId": id, "name": title}, nil); err != nil {
			n.logf("assistant %s: could not persist prepared Codex thread: %v", short(id), err)
			n.stopAssistantProcess(id)
			return "", refusal("missing_generation", i18n.T(i18n.From(ctx), "assistant-conversation_segments.missing_generation"), nil)
		}
	}
	if startTier != "" {
		// Acknowledge the native setting before persisting or exposing the new conversation. This process is either a
		// cold start or an adopted spare, so a refusal must stop it rather than leave an unused conversation behind.
		if err := p.cx.setSpeed(ctx, startTier); err != nil {
			n.logf("assistant %s: Codex refused service tier %q before its first turn: %v", short(id), startTier, err)
			n.stopAssistantProcess(id)
			n.notifySessions()
			return "", serviceTierRefusal(ctx, "assistant_service_tier_refused")
		}
		n.noteModes(id, mode, effort)
	}
	fromRecovery := execution.FromRecovery || recoverySwitched
	if st, known := n.state.get(id); !known || st.Launcher != launcher || st.Cwd != cwd || (!fromRecovery && (st.Model != execution.Model || st.ModelFingerprint != execution.Fingerprint || st.ModelEffort != execution.Effort || (st.ModelFromRecovery && !execution.PersistedModel))) || st.Origin != origin || st.OriginKnown != execution.OriginKnown || st.ModelSource != execution.Source || st.IsMain != execution.IsMain || st.MainCompactWindow != execution.MainCompactWindow || st.ConfigDir != execution.ConfigDir || st.FollowsSetting != execution.FollowsSetting || st.SupervisionInstructions != execution.SupervisionInstructions || hasInput && st.TaskExecution != execution.Strict || !resume && !hasInput {
		started := st.StartedAt
		if started.IsZero() {
			started = time.Now().UTC()
		}
		launcherChanged := !known || st.Launcher != launcher
		st.Launcher, st.Cwd, st.StartedAt = launcher, cwd, started
		if !fromRecovery || launcherChanged {
			st.Model, st.ModelFingerprint, st.ModelEffort = execution.Model, execution.Fingerprint, execution.Effort
			if fromRecovery {
				st.ModelFromRecovery = true
			} else if !execution.PersistedModel {
				st.ModelFromRecovery = false
			}
		}
		st.Origin = origin
		st.OriginKnown = execution.OriginKnown
		st.ModelSource = execution.Source
		st.IsMain, st.MainCompactWindow = execution.IsMain, execution.MainCompactWindow
		st.SupervisionInstructions = execution.SupervisionInstructions
		st.FollowsSetting = execution.FollowsSetting
		st.ConfigDir = execution.ConfigDir
		if hasInput {
			st.TaskExecution = execution.Strict
		}
		if !resume && !hasInput {
			st.AwaitingFirstMessage = true
		}
		n.state.put(id, st)
	}

	n.persistModes(id, mode, effort, callerModeExplicit && origin != originInbox && guest == nil) // after saving state, including defaults filled on resume
	if err := n.idleCompactBeforeInput(ctx, p); err != nil {
		return "", err
	}
	n.noteAssistantRecoveryProcess(ctx, p)
	if !hasInput && p.cx != nil {
		// Codex has no startup settings controls; do not allocate a turn for an empty start.
		err := n.syncAssistantModes(ctx, p)
		awaitList(n.notifySessionsSoon())
		if err != nil {
			return "", err
		}
		if err := finishExternalStartup(ctx); err != nil {
			return "", err
		}
		return id, nil
	}
	if hasInput && (launcher != selectedLauncher || execution != selectedExecution || len(selected) > 0 && p.agent == "claude") {
		// A new native conversation uses the file scope checked before startup. A resumed one can retain an
		// existing process's launcher and runtime while a switch waits.
		if resume && len(files) > 0 {
			files, err = n.selectedFiles(ctx, text, cwd, launcher, execution.Runtime)
			if err != nil {
				return "", err
			}
		}
		// An active process may retain its old environment while a switch waits. Never send selections to that
		// different environment simply because the display names match. Claude startup can also
		// reveal a managed-policy cache; recheck before handing exact-file instructions to it.
		selected, err = n.selectedSkills(ctx, id, cwd, launcher, text, execution)
		if err != nil {
			return "", err
		}
	}
	if len(files) > 0 {
		text += "\n\n" + prompts.RenderAssistantSelectedFiles(files, agentPromptLanguage(ctx))
	}
	retryMode, retryEffort := n.modesOf(id) // retain the request before an unconfirmed control can roll it back
	// Controls do not depend on the live context. Start their acknowledged sync
	// while the snapshot RPC is pending, but join both before writing any input.
	// Busy state and context receipts still change only at the delivery boundary.
	var modec chan error
	if p.agent == "claude" {
		modec = make(chan error, 1)
		go func() { modec <- n.syncAssistantModes(ctx, p) }()
	}
	var live string
	commit := func() {}
	if hasInput {
		live, commit = n.turnContext(id, <-snapc)
	}
	if p.cx != nil {
		p.mu.Lock()
		wasBusy := p.busy
		p.busy = true
		p.turnError = ""
		p.turnErrorKind = ""
		p.lastActive = time.Now()
		p.model.NewModelChoice = execution.NewModelChoice
		if recovery != nil || !wasBusy {
			p.turnRecovery = recovery
			p.model.PersistedModel = execution.PersistedModel
			p.model.FromRecovery = fromRecovery
		}
		p.mu.Unlock()
		if err := p.cx.deliver(ctx, live, text, images, selected...); err != nil {
			p.mu.Lock()
			p.busy = p.cx.turn() != ""
			p.mu.Unlock()
			if execution.PersistedModel && execution.Model != "" && assistantModelRouteUnavailable(err.Error()) {
				n.noteModelRouteUnavailable(id, launcher, execution.Model)
				n.clearStalePersistedModel(id, launcher, execution.Model)
			}
			return "", fmt.Errorf("send to %s: %w", p.agent, err)
		}
		n.state.clearStopAfterAssistantInput(id, previousStopAt)
		n.commitModelChoice(id, execution)
		commit()
		awaitList(n.notifySessionsSoon())
		return id, nil
	}

	// A model recovery's resend continues the failed turn its busy flag holds: no message of the owner's, so no
	// interjection (#10178).
	interject = interject && recovery == nil && !fresh && p.interjects()
	midTurn := recovery == nil && !fresh && p.midTurn()
	content := []map[string]any{}
	if live != "" {
		content = append(content, map[string]any{"type": "text", "text": live})
	}
	if len(selected) > 0 {
		skills := make([]prompts.AssistantSelectedSkill, len(selected))
		for i, e := range selected {
			skills[i] = prompts.AssistantSelectedSkill{Name: e.Name, Path: e.Path, Ref: e.ID}
		}
		content = append(content, map[string]any{"type": "text", "text": prompts.RenderAssistantSelectedSkills(skills)})
	}
	if midTurn {
		content = append(content, interjectNote(interject))
	}
	for _, img := range images {
		content = append(content, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": img.Mime, "data": img.Data}})
	}
	if strings.TrimSpace(text) != "" || len(images) == 0 {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	in := assistantInput{text: text, images: images, content: content, supervision: ctx.Value(supervisionDeliveryKey{}) == true}
	startAttemptsAt := time.Now()
	if fresh && !p.born.IsZero() {
		startAttemptsAt = p.born
	}
	for attempt := 1; ; attempt++ {
		if proto.IsExternalLauncher(launcher) && ctx.Err() != nil {
			return "", externalAdapterError(ctx, ctx.Err())
		}
		var controlErr error
		if attempt == 1 {
			controlErr = <-modec
		} else {
			controlErr = n.syncAssistantModes(ctx, p) // a retry has its own new process
		}
		if controlErr != nil {
			n.notifySessionsSoon() // the modes it kept, or went back to
		}
		var unconfirmed *assistantControlUnconfirmed
		timing.mark("preparation_joined", controlErr == nil) // context may have fallen back; this marks the join, not snapshot success
		if controlErr != nil && (!fresh || ctx.Err() != nil || !errors.As(controlErr, &unconfirmed)) {
			return "", controlErr
		}
		if controlErr == nil && !hasInput {
			// Allocation and its retries confirm settings without consuming a
			// snapshot, acknowledging context, or starting a context-only turn.
			if err := finishExternalStartup(ctx); err != nil {
				return "", err
			}
			awaitList(n.notifySessionsSoon())
			return id, nil
		}
		p.mu.Lock()
		wasBusy := p.busy
		p.busy = true
		p.turnError = ""
		p.turnErrorKind = ""
		p.compacted = false // a new turn: compact again when it is idle and long
		p.lastActive = time.Now()
		p.partial.Reset()
		p.model.NewModelChoice = execution.NewModelChoice
		if recovery != nil || !wasBusy {
			p.turnRecovery = recovery
			p.model.PersistedModel = execution.PersistedModel
			p.model.FromRecovery = fromRecovery
		}
		p.mu.Unlock()
		var timeout *assistantControlTimeoutError
		retryControl := errors.As(controlErr, &timeout)
		werr := controlErr
		if proto.IsExternalLauncher(launcher) && ctx.Err() != nil {
			return "", externalAdapterError(ctx, ctx.Err())
		}
		if werr == nil {
			sentAt := time.Now()
			werr = p.writeInput(in) // a new id each attempt: a process that died may have written the last one down
			if werr == nil {
				if !wasBusy {
					n.ttft.arm(id, sentAt, "")
				}
				n.commitModelChoice(id, execution)
			}
			timing.mark("input_written", werr == nil)
		}
		switch {
		case werr != nil && !fresh:
			n.stopAssistantProcess(id)
			n.notifySessionsSoon() // the modes it took, and the process gone
			return "", fmt.Errorf("send to claude: %w", werr)
		case werr != nil && !retryControl:
			werr = p.startFailure() // it is gone already: say why, not that the pipe is closed (#2581)
		case werr != nil:
			werr = fmt.Errorf("%s: %w", i18n.T(i18n.Default, "node-assistant-start.not_delivered"), werr)
		case fresh:
			werr = p.awaitStart(assistantStartWait)
			if werr != nil {
				timing.mark("start_failed", false)
			} else {
				select {
				case <-p.ready:
					timing.mark("process_ready", true)
				default:
					timing.mark("start_wait_elapsed", true) // awaitStart's timeout is not observed readiness
				}
			}
		}
		if werr == nil {
			if interject {
				n.backgroundForeground(p)
			}
			break
		}
		timing.mark("start_attempt_failed", false)
		n.stopAssistantProcess(id)
		if round.anchor != "" {
			// The round's start was refused (Claude Code changed its undocumented --resume-session-at, or the anchor
			// went meanwhile): the message goes in as before, once, and the next round tries again.
			n.logf("assistant %s: the supervision round did not start from its first turn (%v); resuming it as before", short(id), werr)
			round = spawnRound{}
			superviseRoundFrom(ctx).set(roundKept, roundWhyStartFailed)
			// An unconfirmed control rolled these back; the plain fallback
			// must confirm the same request, just like an ordinary retry.
			n.noteModes(id, retryMode, retryEffort)
			n.persistModes(id, retryMode, retryEffort)
			if p, err = n.spawnAssistant(id, cwd, launcher, resume && n.transcriptPath(id) != "", false, execution); err != nil {
				return "", err
			}
			n.noteAssistantRecoveryProcess(ctx, p)
			continue
		}
		if (!p.missingCommand() && !retryControl) || attempt > assistantStartRetries {
			timing.mark("start_failed", false)
			if retryControl {
				return "", refusal("assistant_control_failed", i18n.T(i18n.Default, "node-assistant-start.attempts_exhausted", i18n.Args{
					"attempts": attempt, "wait": time.Since(startAttemptsAt).Round(100 * time.Millisecond).String(), "reason": werr.Error(),
				}), nil)
			}
			return "", p.startRefusal(werr)
		}
		// Retry only before user input was written: a missing executable or an unconfirmed startup deadline.
		n.logf("assistant %s: %s (attempt %d), starting it again in %s", id[:8], werr, attempt, assistantStartRetryDelay)
		select {
		case <-ctx.Done():
			return "", p.startRefusal(werr)
		case <-time.After(assistantStartRetryDelay):
		}
		// No user input was delivered on this attempt. Restore the requested settings for the new process.
		// Keep the original choices when retrying that transient startup failure.
		n.noteModes(id, retryMode, retryEffort)
		n.persistModes(id, retryMode, retryEffort)
		if p, err = n.spawnAssistantWith(id, cwd, launcher, resume && n.transcriptPath(id) != "", false, round, execution); err != nil {
			return "", err
		}
		n.noteAssistantRecoveryProcess(ctx, p) // the replacement is the process the recovery follows now (#3495)
	}
	if err := finishExternalStartup(ctx); err != nil {
		return "", err
	}
	n.state.clearStopAfterAssistantInput(id, previousStopAt)
	commit()
	awaitList(n.notifySessionsSoon())
	return id, nil
}

// storedCwd is where a conversation resumed without a folder runs: the folder it ran in before (the node's state, kept
// in the hub's database), else the assistant directory. A stored folder that is gone (the data directory moved,
// docs/operations/persona-cutover.md) is the folder of the same name in the data directory when there is one (the assistant
// directory, the health space, another manager's), else the assistant directory: it is never made again where it was,
// which would start the conversation in an empty folder without its instructions and memory (#6299). On a scoped
// node the assistant directory is its own (assistantFallbackDir), and a stored built-in one goes back to it
// (unlistedAssistantDir): resumed outside the scope, the conversation was never listed.
func (n *Node) storedCwd(stored string) string {
	if n.unlistedAssistantDir(stored) {
		stored = ""
	}
	if stored != "" {
		if dirExists(stored) {
			return stored
		}
		if name := filepath.Base(stored); name != "." && name != string(filepath.Separator) {
			if moved := filepath.Join(identity.DataDir(n.home), name); dirExists(moved) && !n.unlistedAssistantDir(moved) {
				return moved
			}
		}
	}
	return n.assistantFallbackDir()
}

// readAssistant turns the stream-json output into live deltas and turn-finished signals.
// The structured messages themselves come from the transcript, like every other session.
func (n *Node) readAssistant(p *assistantProc, r io.Reader) {
	started := n
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	// While a line is handled (a result's transcript refresh and pushes can take a while), the ones behind it wait in
	// the pipe: the process is not quiet until the reader is back at the pipe (assistant_round.go quiet).
	for ; sc.Scan(); p.lineRead() {
		p.reading.Add(1)
		n := p.ownerOr(started) // a resident node's next connection may have taken it over (assistant_keep.go)
		line := sc.Bytes()
		if string(line) == assistantShellReady {
			p.mu.Lock()
			p.shellReadyAt = time.Now()
			p.mu.Unlock()
			n.logf("assistant %s: shell initialization completed elapsed_ms=%d", short(p.id), time.Since(p.born).Milliseconds())
			continue
		}
		if len(line) == 0 || line[0] != '{' { // interactive zsh may print banners
			continue
		}
		p.printed()
		var ev struct {
			Type      string                   `json:"type"`
			Subtype   string                   `json:"subtype"`
			SessionID string                   `json:"session_id"`
			Tasks     json.RawMessage          `json:"tasks"`
			Tools     []string                 `json:"tools"`
			Version   string                   `json:"claude_code_version"`
			Response  assistantControlResponse `json:"response"`
			Command   string                   `json:"command_uuid"` // command_lifecycle: the user message it is about
			State     string                   `json:"state"`        // and what became of it (assistant_queue.go)
			Event     struct {
				Type  string `json:"type"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					Thinking    string `json:"thinking"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
				Message struct {
					ID    string `json:"id"`
					Model string `json:"model"`
					Usage struct {
						Input       int `json:"input_tokens"`
						CacheRead   int `json:"cache_read_input_tokens"`
						CacheCreate int `json:"cache_creation_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			} `json:"event"`
			Compact struct {
				Post int `json:"post_tokens"`
			} `json:"compact_metadata"`
			ParentToolUseID *string                     `json:"parent_tool_use_id"`
			IsError         bool                        `json:"is_error"`
			Result          string                      `json:"result"`
			Errors          json.RawMessage             `json:"errors"`
			Reason          string                      `json:"reason"` // tuiFallbackSubtype: why interactive did not start
			ModelUsage      map[string]claudeModelUsage `json:"modelUsage"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			p.invalidateUpdateTasks()
			// A field of another type than the node reads (a gateway's token count as a string, say) fails the line
			// but not the fields around it, which are decoded all the same. A turn's end is still its end: dropped,
			// the conversation stayed working until its next turn ended (#14351).
			var mismatch *json.UnmarshalTypeError
			if ev.Type != "result" || !errors.As(err, &mismatch) {
				continue
			}
		}
		if p.round && ev.SessionID != "" && ev.SessionID != p.id && validID(ev.SessionID) {
			n.noteRoundFork(ev.SessionID, p.id)
		}
		switch {
		case p.round:
			n.settleSoon(p) // its row says working until it settled (assistant_round.go); it starts with its init (below)
		case p.tui && tuiFallbackLine(line):
			// the bridge's notice that it runs claude -p instead (assistant_tui_bridge.go fallback) is no start: the
			// print-mode command's own first line is, and one that exits before printing any fails the start as it
			// would without the bridge
		default:
			p.started()
		}
		if p.round && ev.Type == "result" && !p.hasStarted() {
			// A round's process that refuses its --resume-session-at (assistant_round.go) prints only a result
			// (error_during_execution, no turn) and exits 1, saying why on stderr too (measured on Claude Code 2.1.322).
			// That is no start, so the send starts the conversation again as before (awaitStart), and no turn of the
			// conversation's: it neither fails the conversation's turn nor starts a recovery.
			continue
		}
		switch ev.Type {
		case "system":
			if ev.Subtype == "init" {
				// Claude Code prints its init as each turn starts (measured on 2.1.322), before the model is asked: the
				// first one in the turn of the message that started the process, which made it busy already. A later one
				// starts a turn the node may not have asked for: the one it takes a finished background task's
				// notification up in, right after it emptied its task list. Busy from here, not from the model's first
				// token seconds later: in between nothing would show it working, and a restart then (a round, a new tool
				// set, assistant_round.go) would kill the turn. Not for a compaction's init, which comes right before its
				// end (the compaction is work of its own: workingLocked).
				p.mu.Lock()
				if ev.Version != "" {
					p.claudeVersion = ev.Version
				}
				resumed := p.initSeen && !p.busy && !p.compacting
				p.initSeen = true
				if resumed {
					p.busy, p.lastActive = true, time.Now()
				}
				p.mu.Unlock()
				p.started()          // a round's process's start; any other's started with its first line
				if ev.Tools != nil { // an interactive Claude Code's bridge names none (assistant_tui.go)
					n.toolsAtStart(p.id, ev.Tools)
				}
				if resumed {
					n.ttft.arm(p.id, time.Now(), "")
					n.notifySessionsSoon()
				}
				continue
			}
			if ev.Subtype == "compact_boundary" {
				n.forgetCacheWarm(p.id)
				p.mu.Lock()
				p.context = ev.Compact.Post
				report := p.manual || p.releaseCompaction
				p.manual, p.releaseCompaction = false, false
				p.mu.Unlock()
				n.recordIdleCompactionContext(p.id, ev.Compact.Post)
				n.contextCompacted(p.id)
				if report {
					n.compactionEnded(p.id, "")
				}
				continue
			}
			if ev.Subtype == tuiFallbackSubtype {
				n.tuiFellBack(p, ev.Reason)
				continue
			}
			if ev.Subtype != "background_tasks_changed" {
				continue
			}
			tasks, err := p.recordBackgroundTasks(ev.Tasks)
			if err != nil {
				n.logf("assistant background inventory: %v", err)
				continue
			}
			n.rememberTasks(p.id, tasks)
		case "control_response":
			p.controlResponse(ev.Response)
		case "command_lifecycle":
			if ev.State == "started" {
				n.ttft.arm(p.id, time.Now(), ev.Command)
			}
			if p.commandLifecycle(ev.Command, ev.State) {
				n.notifySessionsSoon() // the queue strip shows it, or no longer
			}
		case "rate_limit_event":
			// the account's subscription windows after a reply (#12855, usagewindows_claudestream.go)
			n.claudeStreamUsage(p.launcher, p.model.ConfigDir, line, time.Now())
		case "stream_event":
			if ev.ParentToolUseID != nil && *ev.ParentToolUseID != "" {
				// A subagent's stream (#4495): its words are not the conversation's live text, its start does not clear
				// the conversation's text or turn error, and its stop is not what the conversation just said. Its own
				// transcript has its words; here it only shows the conversation is at work.
				p.mu.Lock()
				p.lastActive = time.Now()
				p.mu.Unlock()
				continue
			}
			switch ev.Event.Type {
			case "message_start":
				if p.cacheProxy != nil {
					p.cacheProxy.Confirm(ev.Event.Message.ID)
				}
				// A turn can start without a message from us: claude takes up a finished background task's
				// notification on its own, and a message sent meanwhile waits behind it. The result of that turn
				// cleared busy while claude went on working, so the phone showed the chat as finished. Its init (above)
				// or the queued message's started (commandLifecycle) set busy already; this is the last word.
				// A new message: the last one's text is in the transcript by now. Clear the app's live text, or a
				// message that went on to call tools stays on screen twice (its item, and the live copy) meanwhile.
				u := ev.Event.Message.Usage
				p.mu.Lock()
				had := p.partial.Len() > 0
				p.partial.Reset()
				p.turnError = ""
				p.turnErrorKind = ""
				resumed := !p.busy
				p.busy = true
				p.lastActive = time.Now()
				first := false
				if tokens := u.Input + u.CacheRead + u.CacheCreate; ev.ParentToolUseID == nil && (tokens == 0 || transcript.PlausibleContext(tokens, 0)) {
					// a subagent's requests are its own context (they do not get here; the check stays for an empty
					// parent id); a gateway's impossible number is none (assistant_usage.go), and the zeros it reports on
					// the other replies of a turn say nothing either: the last real one stands
					if tokens > 0 {
						p.context = tokens
					}
					if m := ev.Event.Message.Model; m != "" {
						p.replyModel, p.repliedAt = m, time.Now()
					}
					first, p.requested = !p.requested, true
				}
				p.mu.Unlock()
				if first { // what a start costs: the history read from the prompt cache, or written again
					n.logger.Printf("assistant %s: first request since start: %d tokens in context, %d read from the cache, %d written",
						p.id[:8], u.Input+u.CacheRead+u.CacheCreate, u.CacheRead, u.CacheCreate)
				}
				if resumed {
					n.notifySessionsSoon()
				}
				if had {
					n.emitDelta(p.id, "")
				}
			case "content_block_delta":
				if ev.Event.Delta.Text != "" || ev.Event.Delta.Thinking != "" || ev.Event.Delta.PartialJSON != "" {
					n.ttft.firstToken(p.id, time.Now(), "")
				}
				if ev.Event.Delta.Type != "text_delta" {
					continue
				}
				p.mu.Lock()
				p.partial.WriteString(ev.Event.Delta.Text)
				text := n.partialGrewLocked(p)
				p.mu.Unlock()
				if text != "" {
					n.emitDelta(p.id, text)
				}
			case "message_stop":
				// The finished block lands in the transcript; keep showing it until the item arrives.
				p.mu.Lock()
				text := p.partial.String()
				p.deltaSent = text
				p.mu.Unlock()
				n.emitDelta(p.id, text)
			}
		case "result":
			n.ttft.clearPending(p.id)
			var kinds []string
			if ev.IsError {
				kinds = n.claudeResultKinds(p.id)
			}
			recovering := n.recoverAssistantResult(p, ev.IsError, ev.Result, kinds...)
			if !recovering {
				n.clearAssistantRecovery(p, !ev.IsError)
				if ev.IsError {
					n.handleModelRouteFailure(p, ev.Result)
				} else {
					// A turn that ended well is newer evidence than an exhausted chain: the failure card's cause and reset
					// time must not survive onto a later error, so drop the marker on any successful result (#7824).
					if r := n.modelRecovery(p.id); r != nil && r.ChainExhausted {
						n.clearModelRecovery(p.id)
					}
				}
			}
			p.mu.Lock()
			reportCompaction := p.manual || p.releaseCompaction
			contextTokens := p.context
			p.manual, p.releaseCompaction = false, false
			window := reportedWindow(ev.ModelUsage, p.replyModel)
			changed, idleAt := p.followWindow(window), p.compactIdleAt
			p.compacting = false
			if !recovering {
				p.busy = false
				// A failed turn (is_error, or an answer that is only an auth / upstream error) is the session's turn
				// error, as Codex's is: the apps show it failed and the push says so (#4669). The transcript may hold no
				// line for it at all (a start refused before the model ran).
				if text, kind := claudeTurnFailure(ev.IsError, ev.Result, kinds); text != "" {
					p.turnError, p.turnErrorKind = text, kind
				}
			}
			p.lastActive = time.Now()
			p.partial.Reset()
			p.mu.Unlock()
			if contextTokens > 0 {
				n.recordIdleCompactionContext(p.id, contextTokens)
			}
			if changed {
				n.logger.Printf("assistant %s: the model's window is %d tokens, idle compaction at %d", p.id[:8], window, idleAt)
			}
			if reportCompaction { // /compact ended without a boundary
				var details []string
				_ = json.Unmarshal(ev.Errors, &details)
				n.compactionEnded(p.id, compactFailure(ev.IsError, ev.Result, details...))
			}
			if f, err := n.file(p.id); err == nil {
				n.refresh(p.id, f)
			}
			n.emitDelta(p.id, "")
			if !recovering {
				n.notifySessionsSoon() // not here on the reader: a long build held up the control answers a send waits on
				go n.checkTurnUsage(p, ev.IsError && promptTooLong(ev.Result))
			}
		}
	}
	stopUnreadAssistant(p.ownerOr(started).logger, p, short(p.id), sc.Err())
}

// stopUnreadAssistant stops a process whose stdout the node can no longer read (a line longer than the scanner's
// buffer: a big image or tool result). Nobody drains its stdout any more, so it would block on its next write and
// never exit, leaving its conversation busy for good; stopped, its exit watcher cleans up. os.ErrClosed is the pipe
// closed by cmd.Wait: the process has already exited.
func stopUnreadAssistant(logger *log.Logger, p *assistantProc, name string, err error) {
	if err == nil || errors.Is(err, os.ErrClosed) {
		return
	}
	logger.Printf("assistant %s: stdout read: %v", name, err)
	p.stop()
}

// deltaEvery is how often the text of a reply being generated goes to the hub (as `session.delta`, the whole text so
// far): the model's text comes in bursts of a few dozen characters, each made of many small deltas.
const deltaEvery = 80 * time.Millisecond

// partialGrewLocked is called with p.mu held once the reply's text grew. It returns the text to send now, at most once
// every deltaEvery ("" for nothing now). When it is too soon, one send of whatever the text is by then is armed for when
// deltaEvery has passed: without it the rest of a burst waited for the next burst, 350 ms and up to 2 s (#6651).
func (n *Node) partialGrewLocked(p *assistantProc) string {
	if wait := deltaEvery - time.Since(p.lastDelta); wait > 0 {
		if !p.deltaArmed {
			p.deltaArmed = true
			time.AfterFunc(wait, func() { p.ownerOr(n).flushPartial(p) })
		}
		return ""
	}
	text := p.partial.String()
	p.lastDelta, p.deltaSent = time.Now(), text
	return text
}

// flushPartial is the armed send: the reply's text as it is now, unless it is empty (a new message or the turn's end
// cleared it meanwhile) or was sent already. Under deltaMu with the read, so a clear sent after the read goes after it.
func (n *Node) flushPartial(p *assistantProc) {
	n.deltaMu.Lock()
	defer n.deltaMu.Unlock()
	p.mu.Lock()
	p.deltaArmed = false
	text := p.partial.String()
	if text == "" || text == p.deltaSent {
		p.mu.Unlock()
		return
	}
	p.lastDelta, p.deltaSent = time.Now(), text
	p.mu.Unlock()
	n.notifyDelta(p.id, text)
}

// emitDelta sends a conversation's live text ("" clears it). Sends are serialized (deltaMu) with flushPartial's.
func (n *Node) emitDelta(id, text string) {
	n.deltaMu.Lock()
	defer n.deltaMu.Unlock()
	n.notifyDelta(id, text)
}

func (n *Node) notifyDelta(id, text string) {
	if n.peer != nil {
		_ = n.peer.Notify("session.delta", map[string]any{"session_id": id, "text": text})
	}
}

// needsRestartFor: the process can't switch to mode, a new one has to start in it. Claude refuses
// set_permission_mode bypassPermissions unless it was started in that mode (Persona doesn't pass
// --dangerously-skip-permissions), so switching there resumes the conversation in a fresh process.
func (p *assistantProc) needsRestartFor(mode string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.needsRestartForLocked(mode)
}

// needsRestartForLocked is needsRestartFor with p.mu held.
func (p *assistantProc) needsRestartForLocked(mode string) bool {
	if p.agent == "claude" && p.tui && p.tuiNote == "" {
		// Interactive Claude Code takes its permission mode as a start flag only (assistant_tui.go).
		return mode != "" && cmpMode(mode) != cmpMode(p.mode)
	}
	return p.agent == "claude" && mode == proto.ModeBypass && !p.canBypass
}

// cmpMode is a permission mode for comparing: no mode is the default one.
func cmpMode(m string) string {
	if m == "" {
		return "default"
	}
	return m
}

// effortNeedsRestartLocked: an interactive process takes its effort as a start flag only, so another one needs a new
// process (assistant_tui.go). p.mu is held.
func (p *assistantProc) effortNeedsRestartLocked(effort string) bool {
	return p.agent == "claude" && p.tui && p.tuiNote == "" && effort != "" && effort != p.effort
}

// rememberTasks keeps a conversation's running background tasks in the session state (the hub's Postgres), so a
// node restart — which kills them with their process — can tell the conversation what it lost (recoverAssistantTasks).
func (n *Node) rememberTasks(id string, tasks []string) {
	st, ok := n.state.get(id)
	if !ok || slices.Equal(st.Tasks, tasks) || (len(st.Tasks) == 0 && len(tasks) == 0) {
		return
	}
	st.Tasks = tasks
	n.state.put(id, st)
}

// recoverAssistantTasks runs once the session state is loaded: conversations whose background tasks or monitors
// were running when the node went down (its connection ended and it stopped its conversations, stopAtConnectionEnd,
// or a service restart killed them with it) get a message saying so, which wakes them up to re-arm what still matters.
// Without it they would wait forever for a notification that cannot come. send delivers the message (assistantSend).
// The record is cleared only once the message went: one that failed (its launcher not up yet after the restart, the
// deadline) keeps it, so the next connection tells the conversation instead of nobody ever doing so. A send that timed
// out after all but was delivered tells it twice, which is harmless; never telling it is not (#6306).
func (n *Node) recoverAssistantTasks(send func(id, text string) error) {
	n.state.mu.Lock()
	lost := map[string][]string{}
	for id, st := range n.state.Sessions {
		if len(st.Tasks) > 0 && identity.IsAssistantDir(st.Cwd) {
			lost[id] = st.Tasks
		}
	}
	n.state.mu.Unlock()
	for id, tasks := range lost {
		n.mu.Lock()
		_, running := n.procs[id]
		n.mu.Unlock()
		if running {
			continue
		}
		if err := send(id, prompts.RenderLostTasks(tasks)); err != nil {
			n.logger.Printf("assistant %s: tell it about lost background tasks (kept for the next connection): %v", id[:8], err)
			continue
		}
		n.forgetLostTasks(id, tasks)
		n.logger.Printf("assistant %s: %d background tasks lost in the restart, told the conversation", id[:8], len(tasks))
	}
}

// forgetLostTasks clears the lost tasks the conversation was just told about (recoverAssistantTasks), only while the
// record still holds them: what the woken conversation re-armed in the meantime is its new list and stays.
func (n *Node) forgetLostTasks(id string, told []string) {
	n.state.update(id, func(v *sessionState) bool {
		if !slices.Equal(v.Tasks, told) {
			return false
		}
		v.Tasks = nil
		return true
	})
}

// assistantSessions lists running headless conversations as live sessions.
func (n *Node) assistantSessions() []proto.Session {
	n.mu.Lock()
	procs := make([]*assistantProc, 0, len(n.procs))
	for _, p := range n.procs {
		procs = append(procs, p)
	}
	recovering := make(map[string]bool, len(n.recoveries))
	for id, r := range n.recoveries {
		recovering[id] = true
		if _, running := n.procs[id]; !running && r.running && r.proc != nil {
			// A model switch has already stopped the old process and not registered its replacement yet. Keep the turn
			// working: an offline row here can fail and rerun a scheduled task while its fallback is still starting.
			procs = append(procs, r.proc)
		}
	}
	n.mu.Unlock()
	var out []proto.Session
	for _, p := range procs {
		n.reconcileStaleTurn(p) // a turn whose end the node missed is over here, for every reader of the row (#14351)
		p.mu.Lock()
		// A registered replacement is still starting until its recovery message is sent. A round's process works until it
		// settled (assistant_round.go): the manager keeps supervision messages for it queued meanwhile.
		busy := p.busy || recovering[p.id] || p.round && !p.settledLocked()
		status := proto.StatusDone // finished turn; the hub turns it into idle once seen
		if busy {
			status = proto.StatusWorking
		}
		updated := p.lastActive
		turnError := p.turnError
		turnErrorKind := p.turnErrorKind
		var queued []string
		if busy {
			queued = queuedTexts(p.queuedLocked()) // typed while it works and not taken yet (assistant_queue.go)
		}
		p.mu.Unlock()
		if !busy {
			if st, ok := n.state.get(p.id); ok && st.AwaitingFirstMessage {
				status = proto.StatusIdle
			}
		}
		s := proto.Session{SessionID: p.id, Cwd: p.cwd, Status: status, Launcher: p.launcher, UpdatedAt: updated.UTC(),
			Queued: queued}
		if p.cx != nil {
			s.Agent = p.agent
		}
		var record turnFailure // the transcript's word on a failed turn
		if path := n.transcriptPath(p.id); path != "" {
			if st, err := os.Stat(path); err == nil {
				m := n.meta(path, st)
				s.Title, s.Model, s.LastText, s.Rev = m.title, m.model, snippet(assistantAnswer(m)), st.Size()
				s.PermissionMode, s.Effort = n.currentModes(p.id, m)
				n.setSessionContext(&s, m)
				// Background shells outlive the turn, as in a pane's session: a channel run (Lark) waits for the turn
				// they start, and a list shows them (channel.Background).
				n.setBackground(&s, m)
				s.ModelFallback = m.modelFallback
				n.setModelRestore(&s, path, m)
				untitled(&s, m)
				if !busy {
					s.UpdatedAt = lastUsed(m, st)
					record = m.turnErr
				}
			}
		}
		if !busy { // a finished turn's failure: what Codex's app-server said, else the transcript's (Claude Code's, a rollout's)
			if turnError != "" {
				record = reported(turnError, turnErrorKind, record)
			}
			record.set(&s)
		}
		if p.agent == "codex" && p.cx != nil {
			p.cx.speedRow(&s)
		}
		n.decorate(&s)
		out = append(out, s)
	}
	return out
}

// notifySessions pushes the session list right away instead of waiting for the next poll. A list the manager holds
// already, byte for byte, does not go again (sessions_sent.go).
func (n *Node) notifySessions() {
	if n.peer == nil {
		return
	}
	n.notifyList(n.sessionList(context.Background(), false))
}

// notifySessionsSoon is notifySessions without holding the caller for the list. A message's send returns once its
// line is written (after awaitList's short wait): building the list reads every row's transcript and the hosts'
// panes, and on a computer with a few hundred sessions that took longer than the manager waits for the send (90 s),
// so a delivered message was reported failed and sent again. Asks that come while a list is being built fold into one
// more build after it, so the last list pushed has them all, and sends in a row build one list at a time. The channel
// closes once a list built after the ask has gone out.
func (n *Node) notifySessionsSoon() <-chan struct{} {
	return n.scheduleSessionsSoon(true)
}

// publishSessionsSoon coalesces runtime lists while keeping publish's startup guard and revision-only pushes.
func (n *Node) publishSessionsSoon() <-chan struct{} {
	return n.scheduleSessionsSoon(false)
}

func (n *Node) scheduleSessionsSoon(notify bool) <-chan struct{} {
	done := make(chan struct{})
	if n.peer == nil {
		close(done)
		return done
	}
	b := &n.listPush
	b.mu.Lock()
	if b.building {
		b.again = true
		b.nextNotify = b.nextNotify || notify
		b.next = append(b.next, done) // the build running began before this ask: the one after it answers
		b.mu.Unlock()
		return done
	}
	b.building = true
	b.current = append(b.current, done)
	b.mu.Unlock()
	go func() {
		for {
			if notify {
				n.notifySessions()
			} else {
				n.publish(n.sessionList(context.Background(), false))
			}
			b.mu.Lock()
			b.pushed++
			for _, c := range b.current {
				close(c)
			}
			b.current = nil
			if !b.again {
				b.building = false
				b.mu.Unlock()
				return
			}
			notify, b.nextNotify = b.nextNotify, false
			b.again, b.current, b.next = false, b.next, nil
			b.mu.Unlock()
		}
	}()
	return done
}

// listPush is notifySessionsSoon's state: a build running, an ask that came during it, who waits for which build,
// and how many builds went out.
type listPush struct {
	mu         sync.Mutex
	building   bool
	again      bool
	nextNotify bool            // an assistant ask requires the next build to send all changed fields
	current    []chan struct{} // closed when the build running now has gone out
	next       []chan struct{} // closed when the build after it has
	pushed     int
}

// listPushWait is how long a send waits for the list that shows its conversation working, so its answer goes after
// that list as it did when the list was built inside the send (the manager drains its queues and counts rounds by
// the lists it is pushed). A list slower than this is not waited for: the answer goes first, and the list after it.
var listPushWait = 3 * time.Second

// awaitList waits for done at most listPushWait.
func awaitList(done <-chan struct{}) {
	t := time.NewTimer(listPushWait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
}

// notifyList is notifySessions with the list built.
func (n *Node) notifyList(list []proto.Session) {
	n.mu.Lock()
	n.lastList = listKey(list)
	n.revsChanged(list)
	n.mu.Unlock()
	n.sendSessions(list)
}

// assistantAnswer is what a conversation's row says it last said: its latest turn's answer from the first words on
// (transcript.Meta.TurnAnswer), the push body when the turn ends, so a report written before a tool call is not left
// out for the line after it. Its last words when those are an error notice (the push marks the turn cut off by
// their start, assistantwire.CutOff) or the tail read held none of the turn's words.
func assistantAnswer(m quickMeta) string {
	if m.answer == "" || assistantwire.CutOff(m.lastText) {
		return m.lastText
	}
	return m.answer
}
