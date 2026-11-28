package node

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// runtimeOf is the runtime that runs launcher name in a terminal host's start: the one offering it, unless the login
// shell or a launcher kept in Persona has that name. Those run instead, as that host's pickers list them under the
// name: a managed launcher made from a Paseo provider of the same name is started with personad launch, not through
// Paseo. nil when no runtime runs it. Where every start goes to an engine runtime that runs only its own launchers
// (Paseo with caps.same_kind) its provider of that name runs instead, whatever else has it: runtimeOfIn. Engine
// runtimes this machine does not enable never offer launchers (runtimeRegistry.hostOff), so they cannot take a name
// from an enabled runtime.
func (n *Node) runtimeOf(name string) *runtimeConn {
	if shellLauncherNames()[name] {
		return nil
	}
	if _, managed := n.managedLauncher(name); managed {
		return nil
	}
	return n.runtimes.launcherRuntime(name)
}

// engineHostOff: runtime is an engine host (Paseo, Cindy) this machine does not enable. Its providers start nothing
// here and are not listed (launchers.list), even while its runtime is connected.
func (n *Node) engineHostOff(runtime string) bool {
	_, engine := engineRuntimes[runtime]
	return engine && !n.hostEnabled(runtime)
}

// startRuntime is the runtime a terminal host's start with launcher name goes to, and the model it starts with when
// the form chose none: runtimeOf, or the runtime's own agy launcher for a managed Antigravity launcher, whatever its
// name. Checks about an agent already in a terminal (a launcher switch) ask here; a new start asks startRuntimeIn.
// nil when the start is not a runtime's.
func (n *Node) startRuntime(name string) (h *runtimeConn, model string) {
	if h = n.runtimeOf(name); h != nil {
		return h, ""
	}
	if m, ok := n.managedLauncher(name); ok && m.AgentType() == "agy" && !shellLauncherNames()[name] {
		return n.runtimes.launcherRuntime("agy"), m.Model
	}
	return nil, ""
}

// runtimeStart starts a session in the runtime a start in ctx with the launcher goes to (startRuntimeIn: on a host
// whose engine runtime runs only its own launchers, that runtime for every name it offers); false when none does.
func (n *Node) runtimeStart(ctx context.Context, p runtimeStartParams) (*proto.Session, *proto.StartInstead, error, bool) {
	h, model := n.startRuntimeIn(ctx, p.Launcher)
	if h == nil || n.usesAgyPane(ctx, p.Launcher, p.AgentType) {
		return nil, nil, nil, false
	}
	if p.Model == "" {
		p.Model = model
	}
	s, instead, err := n.runtimeStartOn(ctx, h, p)
	return s, instead, err, true
}

// runtimeStartOn starts a session in runtime h (session.start): the directory, the first message, the launcher picked
// and what the form chose with it (model, permission mode, effort), the kind of agent a launcher it does not have
// starts (agent_type, caps.same_kind), the session to continue. Fields are sent only when set: a runtime that knows
// none of them still starts its own way. Its answer is the session and what it was asked for and does not run with
// (instead: a runtime that runs only its own launchers says it), nil when it runs as asked.
func (n *Node) runtimeStartOn(ctx context.Context, h *runtimeConn, p runtimeStartParams) (*proto.Session, *proto.StartInstead, error) {
	if p.PermissionMode == "plan" {
		// Only a runtime that promises read-only starts (caps.readonly) gets one: without that promise its provider
		// may ignore the mode, or fall back to a writable one.
		h.mu.Lock()
		readonly := h.hello.Caps.Readonly
		h.mu.Unlock()
		if !readonly {
			return nil, nil, readonlyStartUnsupported()
		}
	}
	if p.AgentType != "" && !sameKind(h) {
		p.AgentType = "" // only a runtime that runs a launcher it lacks by its kind is told the kind
	}
	ctx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
	defer cancel()
	params := map[string]any{"cwd": p.Cwd, "prompt": p.Prompt}
	for k, v := range map[string]string{"launcher": p.Launcher, "model": p.Model, "permission_mode": p.PermissionMode,
		"effort": p.Effort, "workspace_id": p.WorkspaceID, "resume": p.Resume, "session_id": p.SessionID, "agent_type": p.AgentType,
		"client_msg_id": p.ClientMsgID} {
		if v != "" {
			params[k] = v
		}
	}
	if len(p.Images) > 0 {
		params["images"] = p.Images
	}
	if p.Shell {
		params["shell_launcher"] = true
	}
	if p.OpenElsewhere {
		params["open_elsewhere"] = true
	}
	if p.DeferMCP {
		params["defer_mcp"] = true
	}
	if h.agent() == agyAgent {
		ch, err := n.agyChannelFor(ctx, p.Launcher)
		if err != nil {
			return nil, nil, err
		}
		if ch != nil {
			params["channel"] = ch
		}
	}
	var res struct {
		Session json.RawMessage     `json:"session"`
		Instead *proto.StartInstead `json:"instead"`
	}
	if b := backendLaunchFrom(ctx); b != nil {
		h.mu.Lock()
		supported := h.hello.Caps.BackendWorkspaceRoot
		h.mu.Unlock()
		if !supported && !b.taskDispatch {
			return nil, nil, refusal("backend_workspace_unsupported", "This runtime cannot start in a prepared task directory.", nil)
		}
		if supported {
			params["backend_workspace_root"] = true
		}
		if err := b.beforeDispatch(p.SessionID); err != nil {
			return nil, nil, err
		}
	}
	// The ordinary runtime inventory is self-reported. Authorize protected
	// input only for a fresh session ID chosen here or supplied by the manager.
	var binding *runtimeInputBinding
	inputSession := p.SessionID
	if p.Resume == "" && h.reportsInput() {
		if inputSession == "" {
			inputSession = newUUID()
		}
		if runtimeIDRe.MatchString(inputSession) && !n.hasOwnSession(inputSession) && !n.isAssistantSession(inputSession) {
			binding = n.runtimes.beginInputStart(h, inputSession)
			if binding != nil {
				params["session_id"], params["input_binding"] = inputSession, binding.proof
				defer n.runtimes.finishInputStart(inputSession, binding)
			}
		}
	}
	sentAt := time.Now()
	if p.SessionID != "" && p.Prompt != "" {
		n.ttft.arm(p.SessionID, sentAt, "")
	}
	if err := h.peer.Call(ctx, "session.start", params, &res); err != nil {
		if p.SessionID != "" && p.Prompt != "" && !maybeDelivered(err) {
			n.ttft.disarm(p.SessionID, sentAt)
		}
		return nil, nil, err
	}
	var s proto.Session
	var row struct {
		Transcript string `json:"transcript"`
	}
	if len(res.Session) > 0 {
		if err := json.Unmarshal(res.Session, &s); err != nil {
			return nil, nil, fmt.Errorf("%s: the started session: %w", h.label(), err)
		}
		_ = json.Unmarshal(res.Session, &row)
	}
	started, err := n.listStarted(h, s, row.Transcript)
	if err != nil {
		return nil, nil, err
	}
	if binding != nil && started.SessionID == inputSession {
		n.runtimes.mu.Lock()
		binding.confirmed = true
		n.runtimes.mu.Unlock()
	}
	if p.Prompt != "" && started.SessionID != "" && p.SessionID == "" {
		n.ttft.armIfUnarmed(started.SessionID, sentAt, "")
	}
	if p.Resume == "" {
		n.recordEngineStart(h, started, p.Cwd)
		n.recordAgyRuntimeStart(h, started, p)
	}
	return started, cleanInstead(res.Instead), nil
}

// recordAgyRuntimeStart persists an explicit permission mode chosen when Persona starts a non-assistant Antigravity
// companion session. The companion keeps per-session modes in memory only, so without a persisted record a companion,
// node or machine restart falls back to local settings.json (losing bypassPermissions or escalating read-only plan).
// Starts that left permission_mode empty stay unrecorded here so they continue to follow local settings.json.
func (n *Node) recordAgyRuntimeStart(h *runtimeConn, s *proto.Session, p runtimeStartParams) {
	if h == nil || h.agent() != agyAgent || s == nil || s.SessionID == "" || n.state == nil {
		return
	}
	managedAgy := p.Launcher != "" && p.Launcher != agyAgent
	if p.PermissionMode == "" && !managedAgy {
		return
	}
	mode := ""
	if p.PermissionMode != "" {
		mode = cmp.Or(s.PermissionMode, p.PermissionMode)
	}
	launcher := cmp.Or(p.Launcher, s.Launcher, agyAgent)
	if n.state.update(s.SessionID, func(st *sessionState) bool {
		changed := false
		if mode != "" && st.AppMode != mode {
			st.AppMode = mode
			changed = true
		}
		if managedAgy && st.Launcher != launcher {
			st.Launcher = launcher
			changed = true
		}
		return changed
	}) {
		return
	}
	n.state.put(s.SessionID, sessionState{
		Launcher:    launcher,
		Cwd:         cmp.Or(s.Cwd, p.Cwd),
		Model:       cmp.Or(s.Model, p.Model),
		ModelEffort: cmp.Or(s.Effort, p.Effort),
		AppMode:     mode,
		StartedAt:   time.Now().UTC(),
	})
}

// recordEngineStart keeps the node's record of a session an engine runtime (Paseo, Cindy) has just started, as a
// terminal start keeps one (putAgentState) and a start with a creation identity already did (creationResult): its
// launcher, its directory, when, and the runtime that started it (EngineRuntime). The engine lists an archived Agent
// only for a while (Paseo: the 80 newest archived in the last 7 days) and the node has no transcript under the id
// Persona named it by, so without the record the computer would have nothing of it once it is not listed, and refuse
// an extension's resume of it (resumeOutsideHealth) that the engine takes back (#10941). A session that has a record
// keeps it.
func (n *Node) recordEngineStart(h *runtimeConn, s *proto.Session, cwd string) {
	if _, engine := engineRuntimes[h.id()]; !engine || s == nil || s.SessionID == "" || n.state == nil {
		return
	}
	if _, ok := n.state.get(s.SessionID); ok {
		return
	}
	n.state.put(s.SessionID, sessionState{Launcher: s.Launcher, Cwd: cmp.Or(s.Cwd, cwd), StartedAt: time.Now().UTC(), EngineRuntime: h.id(), RemoteAgent: h.remoteAgent()})
}

// listStarted lists a session h just started right away, before the runtime's next runtime.sessions, so the chat
// that opens next finds it. Who answers for it is decided as for any list (recomputeOwners): a runtime that starts
// sessions elsewhere (pi's companion starts them in pi-web) reports them offline, and the one running it, if it has
// listed it already, keeps it. transcript: the Claude transcript the started row names as its conversation (a session
// taken over keeps its own, runtimetranscript.go), taken as a list naming it would be: a watch right after the start
// is the transcript's, and the next list naming it moves nothing (transcriptMoved).
func (n *Node) listStarted(h *runtimeConn, started proto.Session, transcript string) (*proto.Session, error) {
	list := normalizeRuntimeSessions(h, []proto.Session{started})
	if len(list) == 0 {
		return nil, fmt.Errorf("%s started a session without a valid id", h.label())
	}
	s := list[0]
	known := !h.isDaemon() && namesTranscript(s.SessionID, transcript) && n.transcriptPath(transcript) != ""
	b := n.runtimes
	b.mu.Lock()
	h.mu.Lock()
	found := false
	for i := range h.sessions {
		if h.sessions[i].SessionID == s.SessionID {
			h.sessions[i], found = s, true
		}
	}
	if !found {
		h.sessions = append(h.sessions, s)
	}
	if known {
		if h.transcripts == nil {
			h.transcripts = map[string]string{}
		}
		h.transcripts[s.SessionID] = transcript
	}
	h.mu.Unlock()
	b.recomputeOwners()
	h.mu.Lock()
	if h.hello.Caps.Assistant && h.hello.Caps.RemoteAgent {
		b.markRemoteLocked(s.SessionID)
	}
	if startedHere(s) { // not while it is only starting: a takeover that fails leaves the session this machine's (#8130)
		b.remember(h, s.SessionID, time.Now())
	}
	h.mu.Unlock()
	b.noteStarted(h, s.SessionID, time.Now())
	var tell *runtimeConn // runs it already, and is to hear that the hub started it (runtime.driven)
	if o := b.owner[s.SessionID]; o != nil && o != h {
		o.mu.Lock()
		for _, r := range o.sessions {
			if r.SessionID == s.SessionID {
				s = r // the running one's row: its status, title, agent
				if r.Status != proto.StatusOffline && !o.drivenTold[r.SessionID] {
					if o.drivenTold == nil {
						o.drivenTold = map[string]bool{}
					}
					o.drivenTold[r.SessionID] = true
					tell = o
				}
			}
		}
		o.mu.Unlock()
	}
	b.mu.Unlock()
	if tell != nil && tell.peer != nil {
		_ = tell.peer.Notify("runtime.driven", map[string]any{"session_id": s.SessionID})
	}
	n.publish(n.sessionList(context.Background(), false))
	return &s, nil
}

// noteRuntimeWorking marks h's cached row for id working as soon as session.send succeeds, before the runtime's
// debounced runtime.sessions push arrives, unless the session is currently waiting on a user interaction.
func (n *Node) noteRuntimeWorking(h *runtimeConn, id string, before *proto.Session) {
	if h == nil || id == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for index := range h.sessions {
		row := &h.sessions[index]
		if row.SessionID != id || row.Status == proto.StatusBlocked {
			continue
		}
		if before != nil && (row.Status != before.Status || row.Rev != before.Rev || row.TurnError != before.TurnError || !row.UpdatedAt.Equal(before.UpdatedAt)) {
			return
		}
		row.Status = proto.StatusWorking
		row.UpdatedAt = time.Now().UTC()
		return
	}
}
