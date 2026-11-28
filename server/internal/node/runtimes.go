package node

// Runtimes (docs/integrations/runtimes.md, wire in docs/protocol/protocol.md "Runtime ↔ node"): agent runtimes without a terminal that keep
// their own sessions — DSH's Web plugin first (docs/integrations/dsh.md) — connect to ~/.persona/runtime.sock and hang their
// sessions, status and interactions into this node. They are not hosts in the internal/host sense (where a terminal
// lives, the machine's engine): a runtime has no panes, it reports its own sessions and content, and it runs beside
// whichever engine the machine uses. Every runtime speaks the same protocol, so a new one needs no code here.
// The wire is the hub ↔ node JSON lines (internal/rpc) on a long-lived connection: the runtime pushes its session list,
// the items of watched sessions and live text; it asks the phone through interaction.create; the node sends it every
// session.* call for a session it lists. The hub and the apps see ordinary sessions and interactions.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// RuntimeProtocol is the runtime wire's version, answered to runtime.hello; it grows only for changes old runtimes misread.
const RuntimeProtocol = 1

// RuntimeSocketPath is where runtimes reach the node.
func RuntimeSocketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(identity.SocketDir(home), "runtime.sock")
}

// runtimeCallTimeout bounds one node → runtime call; a history page of a long session is the slow one.
var runtimeCallTimeout = 30 * time.Second

// runtimeIDRe: runtime ids and the session ids a runtime may use. Wider than validID (DSH names them session-<uuid>), never a path.
var runtimeIDRe = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// runtimeHello is what a runtime says about itself first.
type runtimeHello struct {
	// Runtime is the runtime's id ("dsh"): its sessions' default agent and space kind.
	Runtime string `json:"runtime"`
	// Name is what the apps call it ("DSH"): its sessions' agent_label, the text of errors about them. Default: Runtime.
	Name string `json:"name,omitempty"`
	// Agent is the agent its sessions run, when it is not the runtime itself (a runtime hosting Claude Code).
	Agent   string `json:"agent,omitempty"`
	Version string `json:"version,omitempty"`
	// Instance identifies the process behind the connection (the SDK makes one per runtime it creates): a reconnect
	// says the same, another process configured with the same runtime id does not and is refused (runtime_conflict).
	// Empty from older runtimes.
	Instance string `json:"instance,omitempty"`
	// Launcher, when set, is offered in the new-session form; session.start with it goes to this runtime.
	Launcher string `json:"launcher,omitempty"`
	// LauncherModel is what the form shows next to the launcher (e.g. the runtime's default model).
	LauncherModel string `json:"launcher_model,omitempty"`
	// Launchers are more entries for the form, each with the agent it starts and its models: a runtime running
	// several agents (Paseo: one per provider, runtimecontrol.go). runtime.launchers replaces them later.
	Launchers []runtimeLauncher `json:"launchers,omitempty"`
	// Package is the code the runtime runs when this node can keep it up to date (runtimepkg, docs/integrations/runtimes.md "Auto-update"):
	// its package name and hash. A different hash than the node's gets an update offer in the reply.
	Package *struct {
		Name string `json:"name"`
		Hash string `json:"hash"`
	} `json:"package,omitempty"`
	Caps struct {
		Start        bool `json:"start"`
		Send         bool `json:"send"`
		Interrupt    bool `json:"interrupt"`
		Rename       bool `json:"rename"`
		Close        bool `json:"close"`
		Rewind       bool `json:"rewind"`
		Fork         bool `json:"fork"`
		Input        bool `json:"input"` // session.input types one line into a particular waiting command
		QueueEdit    bool `json:"queue_edit"`
		QueueSendNow bool `json:"queue_send_now"`
		// Assistant: its launcher may run the Persona assistant (assistant_runtime.go; docs/integrations/runtimes.md "Assistant").
		Assistant bool `json:"assistant"`
		// RemoteAgent: the assistant runtime runs remotely (e.g. cloud API / extension backend) without local
		// filesystem tools; node exposes memory_read/memory_write MCP tools and enforces confirm-writes posture.
		RemoteAgent bool `json:"remote_agent"`
		// SupervisionPolicy: the runtime carries manager-owned supervision_instructions on its own authoritative
		// policy channel (separate from user messages) on session.start and session.send, and fails closed
		// (assistant_supervision_runtime) when a nonempty policy cannot be applied (#11173).
		SupervisionPolicy bool `json:"supervision_policy"`
		// AssistantEnvironment: the local runtime applies the generic Assistant process environment (including
		// session-scoped gate proxy addresses, never Auth-held credentials or raw upstream sockets) only to dedicated
		// Assistant workers, including replacements, and never to shared ordinary-Agent workers.
		AssistantEnvironment bool `json:"assistant_environment"`
		// Execution reports the actual per-session execution environment via session.execution.
		Execution bool `json:"execution"`
		// Modes: the phone's permission mode and effort for its sessions go to it (session.modes).
		Modes bool `json:"modes"`
		// Model permits session.modes model changes. Models provides live launchers.models discovery.
		Model  bool `json:"model"`
		Models bool `json:"models"`
		// Check performs the launcher availability probe in the runtime's authenticated context.
		Check bool `json:"check"`
		// Resume: session.start with resume takes a Claude session it does not have over, under that id (the machine's
		// engine continuing a session not running anywhere, runtimecontrol.go).
		Resume bool `json:"resume"`
		// KeepAlways: the runtime keeps no session rule of its own and asks the hub to remember "Always allow for this session" for it
		// (#3974): its permission cards that offer always get the node's key for the tool. A runtime that keeps its own
		// rule (opencode's save rule, Paseo's suggestions) does not say it, so its grant is never widened to the tool.
		KeepAlways bool `json:"keep_always"`
		// Seen: session.seen goes to it when the user saw one of its sessions in Persona (#6299), for a runtime that
		// keeps an unread mark of its own (Paseo's requiresAttention) to clear it there too.
		Seen bool `json:"seen"`
		// Archive: session.archive / session.unarchive go to it, for its own reversible archive (archive.go): a
		// session the user archives in Persona is archived there too, and restored with it.
		Archive bool `json:"archive"`
		// Readonly: a session.start with permission_mode "plan" either starts in a read-only mode before its first
		// prompt runs or is refused with readonly_unsupported; it never falls back to a writable provider (#3238).
		Readonly             bool `json:"readonly"`
		BackendWorkspaceRoot bool `json:"backend_workspace_root"`
		// SameKind: its starts run only its own launchers (#8140). One it does not have runs on one of its launchers
		// of the agent kind session.start names (agent_type), or is refused (launcher_kind_missing), never another
		// kind of agent; what it asked for and does not run with comes back with the session (instead). As the start's
		// engine host its launchers are then all a start there can run (runtimes_samekind.go). Without it the node sends a
		// launcher it does not have as shell_launcher, as before (Cindy).
		SameKind bool `json:"same_kind"`
		// Workspaces: it keeps workspaces of its own, pushes them (runtime.workspaces) and makes worktrees as its
		// workspaces (workspace.prepare, workspace.create): as the machine's engine, they are the catalog's and its
		// worktrees are made by it (runtimeworkspaces.go).
		Workspaces bool `json:"workspaces"`
		// History: sessions.history goes to it (its driver's listHistory), for sessions active in a time window,
		// inactive ones too (sessions_history.go).
		History bool `json:"history"`
	} `json:"caps"`
	// CodexClaims are the Codex app-server instances the runtime serves itself, which the node leaves to it
	// (codex_instance_claims.go): a runtime on the socket's, never a daemon's.
	CodexClaims []runtimeCodexClaim `json:"codex_claims,omitempty"`
}

// runtimeConn is one connected runtime.
type runtimeConn struct {
	serial int
	peer   *rpc.Peer

	mu sync.Mutex
	// drop ends the connection (RuntimeHost.serve's); nil in tests that make connections by hand.
	drop     func()
	hello    runtimeHello
	ready    bool            // runtime.hello arrived
	sessions []proto.Session // its last runtime.sessions
	// sessionsListed is true once this connection has pushed its first runtime.sessions list.
	// Before that arrives (for example while a freshly started runtime scans disk), absence from
	// runtimeRegistry.owner/known cannot prove a runtime session is gone.
	sessionsListed bool
	watched        map[string]bool // sessions this connection was asked to watch, including an attempt still pending
	// watchTokens identifies the current attempt for each watched session. The counter is per connection and is never
	// reset while it can have an RPC outstanding, so a late error cannot clear a newer attempt or a later hub watch.
	watchTokens    map[string]uint64
	nextWatchToken uint64
	assistantPIDs  map[string]int
	// onWatchFailed is nil in production and used by concurrency tests to observe completion after locks are released.
	onWatchFailed func()
	// drivenTold: sessions this connection was told the hub started through another runtime (runtime.driven).
	drivenTold map[string]bool
	// What its list said beyond the rows the apps see: the terminal pane a session runs in (pane_id: the extension of a
	// pi in a herdr pane, pi.go), how the terminal engine resumes one it lists offline (resume_argv), the Claude
	// session whose transcript holds a session's conversation (transcript: a runtime running Claude Code through its
	// SDK, Paseo; runtimetranscript.go), and the Codex thread a session's conversation is (codex_thread: a runtime
	// running Codex, Paseo; runtimecodex.go).
	panes        map[string]string
	resume       map[string][]string
	transcripts  map[string]string
	codexThreads map[string]string
	// agyPaneBackground holds running tasks projected for a live terminal when the ACP row hides them.
	agyPaneBackground map[string][]proto.BackgroundTask
	agyPreviews       map[string]agyPreview
	// inputWaits is bound to this connection, guarded by runtimeRegistry.mu.
	inputWaits map[runtimeInputKey]*runtimeInputWait
	// workspaces are its own workspaces, as its last runtime.workspaces said (caps.workspaces, runtimeworkspaces.go);
	// workspacesSaid: it said them on this connection (a workspace missing from them is gone).
	workspaces     []runtimeWorkspace
	workspacesSaid bool
	// daemon is its id in daemons.json when the node dialed it (daemons.go), "" for a runtime on the socket.
	daemon string
	// builtinAgy is true only when RuntimeHost.registerRuntime verified this connection matches the built-in
	// Antigravity companion instance it spawned (agybridge.go).
	builtinAgy bool
	// pid is the process at the other end of the runtime socket; 0 when not known (a daemon's link, the Orca bridge,
	// a system that does not tell). A runtime running the assistant vouches for its descendants' MCP servers
	// (mcpAssistantOf).
	pid int
	// superseded is true once a newer runtime.hello of the same runtime replaced this connection as stale (guarded by
	// runtimeRegistry.mu).
	superseded bool
	// Per-connection ordered runtime.hub delivery state (guarded by h.mu).
	// hubHolders counts in-flight runtime.hello calls gating notifications between registerRuntime
	// and completion of the runtime.hello reply write (rpc.AfterReply), ensuring runtime.hub events
	// never precede the hello reply on the wire even across duplicate or concurrent hellos.
	hubHolders       int
	hubNotifying     bool   // true while this connection's drainHubNotifications goroutine is active
	hubStateInit     bool   // true once hello reply or a runtime.hub push recorded hubLastConnected/hubLastEpoch
	hubLastConnected bool   // last connected state delivered to this runtime (hello reply or runtime.hub)
	hubLastEpoch     uint64 // RuntimeHost.hubEpoch of that last delivered state
	hubTargetSeq     uint64 // highest RuntimeHost.hubSeq applied to this connection
	hubTargetEpoch   uint64 // RuntimeHost.hubEpoch of hubTargetSeq
	hubTargetConn    bool   // RuntimeHost connected state of hubTargetSeq
	hubClosed        bool   // connection closed/removed; stop delivering runtime.hub
}

// isDaemon: the node dialed it (a daemon in daemons.json), it did not connect to the socket.
func (h *runtimeConn) isDaemon() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.daemon != ""
}

// id is the runtime's id ("" before hello).
func (h *runtimeConn) id() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hello.Runtime
}

// agent is the agent its sessions run.
func (h *runtimeConn) agent() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hello.Agent != "" {
		return h.hello.Agent
	}
	return h.hello.Runtime
}

// listedSessions reports whether this runtime connection has pushed at least one runtime.sessions snapshot.
func (h *runtimeConn) listedSessions() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessionsListed
}

// assistant: the runtime runs the Persona assistant's conversations (caps.assistant).
func (h *runtimeConn) assistant() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hello.Caps.Assistant
}

// remoteAgent: the runtime runs assistant conversations remotely without local filesystem access (caps.assistant && caps.remote_agent).
func (h *runtimeConn) remoteAgent() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hello.Caps.Assistant && h.hello.Caps.RemoteAgent
}

// isRemoteRuntimeSession reports whether session carries the remote-assistant confirm-writes posture, using durable
// session state, gateProxy marks, and registry records (including ambiguous ownership and startup state load) so
// disconnects or ownership changes never clear the boundary.
func (n *Node) isRemoteRuntimeSession(session string) bool {
	if n == nil || session == "" {
		return false
	}
	id := session
	if parent, _, ok := splitRuntimeChild(session); ok {
		id = parent
	}
	if n.gates().remoteSession(id) || (id != session && n.gates().remoteSession(session)) {
		return true
	}
	if n.state != nil {
		for _, candidate := range []string{id, session} {
			if st, ok := n.state.get(candidate); ok {
				if st.RemoteAgent {
					return true
				}
				if st.Launcher != "" {
					if h := n.assistantRuntime(st.Launcher); h != nil && h.remoteAgent() {
						return true
					}
				}
			}
			if candidate == session {
				break
			}
		}
	}
	if n.runtimes != nil && n.runtimes.isRemoteSession(session) {
		return true
	}
	if n.state != nil && n.state.isLoading() && runtimeSessionID(id) {
		return true
	}
	return false
}

// supervisionPolicy: the runtime runs assistant conversations and accepts supervision_instructions on its own policy
// channel (caps.assistant && caps.supervision_policy).
func (h *runtimeConn) supervisionPolicy() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hello.Caps.Assistant && h.hello.Caps.SupervisionPolicy
}

// assistantEnvironment permits local, dedicated-worker runtimes to opt into the generic Assistant environment
// (with session-scoped local gate proxy addresses for Auth-backed / relayed gates). A daemon or remote-agent
// runtime can run off-machine or without local filesystem access, where local command paths and local proxy
// addresses must not be sent.
func (h *runtimeConn) assistantEnvironment() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hello.Caps.Assistant && h.hello.Caps.AssistantEnvironment && !h.hello.Caps.RemoteAgent && h.daemon == ""
}

// label is what the apps call the runtime.
func (h *runtimeConn) label() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hello.Name != "" {
		return h.hello.Name
	}
	return h.hello.Runtime
}

// splitRuntimeChild splits the id of a runtime session's subagent, "<parent>@<child>" (the id Claude's subagents have
// too: docs/protocol/protocol.md "Runtime ↔ node"); false for any other id.
func splitRuntimeChild(id string) (parent, child string, ok bool) {
	parent, child, ok = strings.Cut(id, "@")
	return parent, child, ok && runtimeIDRe.MatchString(parent) && runtimeIDRe.MatchString(child)
}

// Bounds on what one runtime row may carry about the work it left running.
const (
	maxRuntimeSubagents  = 50
	maxRuntimeBackground = 50
	maxRuntimeTaskText   = 500
	// maxRuntimeLastText is as much of a row's last_text as the node's own rows keep (snippet).
	maxRuntimeLastText = 200
)

// bridgePreview shortens engine replies before they go over the local runtime connection.
// Strip marker lines before snippet flattens them. A marker-only update stays a single
// marker on this connection so the receiving node can keep the session's previous words.
func bridgePreview(text string) string {
	words := transcript.WithoutBotmuxSentinel(text)
	if text != "" && words == "" {
		return transcript.BotmuxSentinel
	}
	return snippet(words)
}

// runtimePreviews reads the previous snapshot only if a row or child reports nothing but
// the botmux sentinel. Ordinary snapshots need no index or extra lock.
func (h *runtimeConn) runtimePreviews() func(id, text string) string {
	var previous map[string]string
	return func(id, text string) string {
		clean := transcript.WithoutBotmuxSentinel(text)
		if clean != "" || text == "" {
			return clean
		}
		if previous == nil {
			h.mu.Lock()
			previous = make(map[string]string, len(h.sessions))
			for i := range h.sessions {
				s := &h.sessions[i]
				previous[s.SessionID] = s.LastText
				for _, a := range s.Subagents {
					previous[a.SessionID] = a.LastText
				}
			}
			h.mu.Unlock()
		}
		return previous[id]
	}
}

// runtimeWork keeps a row's subagents and background tasks to what the apps can use: subagents with a valid id, each
// readable as "<session>@<id>" (whatever the runtime wrote there), background tasks with an id; text clipped, the
// lists bounded. A runtime's background task has no output file of this machine's to open.
func runtimeWork(s *proto.Session, agy bool, preview func(id, text string) string) {
	subs := s.Subagents[:0:0]
	seen := map[string]bool{}
	for _, a := range s.Subagents {
		if len(subs) == maxRuntimeSubagents || !runtimeIDRe.MatchString(a.ID) || seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		a.SessionID = s.SessionID + "@" + a.ID
		a.Description = clipRuntimeTaskText(a.Description)
		a.LastText = clipRuntimeTaskText(preview(a.SessionID, a.LastText))
		a.Type, a.Model = clipRuntimeTaskText(a.Type), clipRuntimeTaskText(a.Model)
		subs = append(subs, a)
	}
	s.Subagents = subs
	if len(s.Subagents) == 0 {
		s.Subagents = nil
	}
	s.Background = runtimeBackground(s.Background, maxRuntimeBackground, agy)
	if agy {
		s.SetBackgroundEnded(runtimeBackground(s.BackgroundEnded(), agyBackgroundEndedLimit, agy))
	}
}

func clipRuntimeTaskText(t string) string {
	if r := []rune(t); len(r) > maxRuntimeTaskText {
		return string(r[:maxRuntimeTaskText]) + "…"
	}
	return t
}

// runtimeBackground bounds and sanitizes task metadata from the runtime wire.
func runtimeBackground(source []proto.BackgroundTask, limit int, agy bool) []proto.BackgroundTask {
	var bg []proto.BackgroundTask
	for _, t := range source {
		if len(bg) == limit {
			break
		}
		if strings.TrimSpace(t.ID) == "" {
			continue
		}
		switch {
		case t.Kind == "agent":
		case agy && (t.Kind == "monitor" || t.Kind == "workflow"):
		default:
			t.Kind = "shell"
		}
		t.ID, t.Description, t.Command, t.OutputFile = clipRuntimeTaskText(t.ID), clipRuntimeTaskText(t.Description), clipRuntimeTaskText(t.Command), ""
		bg = append(bg, t)
	}
	return bg
}

// setResume records how the engine resumes the sessions this runtime lists (resume_argv), replacing the last list's.
func (h *runtimeConn) setResume(m map[string][]string) {
	h.mu.Lock()
	h.resume = m
	h.mu.Unlock()
}

// resumeArgv is how the terminal engine resumes session id, as this runtime said; nil when it did not.
func (h *runtimeConn) resumeArgv(id string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.resume[id]...)
}

// row is this runtime's row for session id.
func (h *runtimeConn) row(id string) (proto.Session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rowLocked(id)
}

// rowLocked looks up a session while the caller holds h.mu.
func (h *runtimeConn) rowLocked(id string) (proto.Session, bool) {
	for _, s := range h.sessions {
		if s.SessionID == id {
			return s, true
		}
	}
	return proto.Session{}, false
}

// listLen is how many rows its last runtime.sessions kept.
func (h *runtimeConn) listLen() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sessions)
}

func (h *runtimeConn) sessionsCopy() []proto.Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]proto.Session(nil), h.sessions...)
}
