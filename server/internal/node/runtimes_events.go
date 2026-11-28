package node

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// newRuntimeHost creates a standalone RuntimeHost bound to n.runtimes without listening yet.
func (n *Node) newRuntimeHost() *RuntimeHost {
	rh := &RuntimeHost{reg: n.runtimes, path: n.runtimePath(), version: n.Version, logger: n.logger, autoCompanions: n.autoCompanions}
	if rh.reg == nil {
		rh.reg = newRuntimeRegistry()
	}
	if rh.reg.logf == nil && n.logger != nil {
		rh.reg.logf = n.logger.Printf
	}
	return rh
}

// listenRuntimeSocket gives n a runtime socket of its own, at runtimePath, for the connection it serves itself
// (`--stdio` with no resident node: runtimehost.go). A resident node's Nodes share one instead.
func (n *Node) listenRuntimeSocket(ctx context.Context) (*RuntimeHost, error) {
	rh := n.newRuntimeHost()
	n.runtimes = rh.reg
	if err := rh.Listen(ctx); err != nil {
		return nil, err
	}
	n.runtimeHost = rh
	rh.attach(n)
	return rh, nil
}

func (n *Node) runtimePath() string {
	if n.runtimeSock != "" {
		return n.runtimeSock
	}
	return RuntimeSocketPath()
}

// runtimeEvent takes a runtime's push for the hub (its list is the host's: RuntimeHost.event).
func (n *Node) runtimeEvent(h *runtimeConn, event string, params json.RawMessage) {
	h.mu.Lock()
	ready := h.ready
	h.mu.Unlock()
	if !ready {
		return // hello first
	}
	var about struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(params, &about) == nil && about.SessionID != "" && n.runtimeSessionBlocked(about.SessionID, "") {
		return // a blocked Agent folder (agentdirs.go): nothing of it is sent
	}
	switch event {
	case "usage.report":
		n.runtimeUsageReport(h, params, time.Now())
	case "usage.windows":
		n.runtimeUsageWindows(h, params, time.Now())
	case proto.EventModeSet:
		var e proto.ModeSetEvent
		if json.Unmarshal(params, &e) == nil && n.runtimes.owns(h, e.SessionID) && n.peer != nil {
			_ = n.peer.Notify(proto.EventModeSet, params)
		}
	case "session.items":
		var p struct {
			SessionID string       `json:"session_id"`
			Items     []proto.Item `json:"items"`
			ToSeq     *int         `json:"to_seq"`
			FromSeq   *int         `json:"from_seq"`
			History   string       `json:"history"`
		}
		if json.Unmarshal(params, &p) != nil || !n.runtimes.owns(h, p.SessionID) {
			return
		}
		if n.runtimeTranscript(p.SessionID) != "" {
			return // its items are the transcript's, which the node pushes itself (runtimetranscript.go)
		}
		last := 0
		if len(p.Items) > 0 {
			last = p.Items[len(p.Items)-1].Seq
		}
		if p.ToSeq != nil {
			last = max(last, *p.ToSeq)
		}
		n.runtimes.pushed(p.SessionID, last, p.History, p.FromSeq != nil && *p.FromSeq == 0)
		if n.peer != nil {
			raw := n.echoedRaw(p.SessionID, params, "items") // echo.go
			raw = n.toolViews.attachRaw(p.SessionID, raw, "items", true)
			raw = n.ttft.annotateRaw(p.SessionID, raw, "items", true)
			_ = n.peer.Notify("session.items", raw)
		}
	case "session.delta":
		var p struct {
			SessionID string `json:"session_id"`
			Text      string `json:"text"`
		}
		if json.Unmarshal(params, &p) != nil || !n.runtimes.owns(h, p.SessionID) {
			return
		}
		n.runtimes.agyWorkDelta(h, p.SessionID)
		if n.peer == nil {
			return
		}
		if p.Text != "" {
			n.ttft.firstToken(p.SessionID, time.Now(), "")
		}
		n.deltaMu.Lock()
		_ = n.peer.Notify("session.delta", params)
		n.deltaMu.Unlock()
	case "interaction.cancel":
		if n.peer != nil {
			_ = n.peer.Notify("interaction.cancel", params)
		}
	}
}

// normalizeRuntimeSessions prepares a live row update, using the preceding snapshot from this connection.
func normalizeRuntimeSessions(h *runtimeConn, list []proto.Session) []proto.Session {
	return normalizeRuntimeRows(h, list, h.runtimePreviews())
}

// normalizeRuntimeRows keeps the sessions a runtime may list (valid ids, once each) and fills what the apps expect:
// the agent and its label, the space kind (the runtime's id). It works in place: the rows kept are list's first ones,
// in list's own array, the rest cleared, so list is the caller's to give up. Rows share read-only capabilities when equal.
func normalizeRuntimeRows(h *runtimeConn, list []proto.Session, preview func(id, text string) string) []proto.Session {
	h.mu.Lock()
	rt, agent, label, caps, daemon := h.hello.Runtime, h.hello.Agent, h.hello.Name, h.hello.Caps, h.daemon != ""
	h.mu.Unlock()
	if agent == "" {
		agent = rt
	}
	if label == "" {
		label = rt
	}
	readOnly := ""
	if !caps.Send {
		readOnly = label + " 没有开放从手机发消息"
	}
	// A row names the workspace it is in only for an engine runtime whose workspaces are the catalog's
	// (runtimeworkspaces.go): the apps tie the session to that catalog row by it. Any other runtime's workspace_id is no
	// workspace the apps know, and would only keep them from placing the session by its folder.
	_, engine := engineRuntimes[rt]
	ownWorkspaces := engine && caps.Workspaces && !daemon
	seen := make(map[string]bool, len(list))
	capsByValue := map[proto.SessionCapabilities]*proto.SessionCapabilities{}
	boolTrue, boolFalse := true, false
	capBool := func(v bool) *bool {
		if v {
			return &boolTrue
		}
		return &boolFalse
	}
	panes := map[string]string{}
	defer func() {
		h.mu.Lock()
		if h.panes == nil {
			h.panes = map[string]string{}
		}
		for id, pane := range panes {
			h.panes[id] = pane
		}
		h.mu.Unlock()
	}()
	out := list[:0] // row i is written at or before i, after it was read
	for _, s := range list {
		if !runtimeIDRe.MatchString(s.SessionID) || seen[s.SessionID] {
			continue
		}
		seen[s.SessionID] = true
		s.MachineID, s.State, s.PendingInteractions = "", "", 0 // the hub's to fill
		// Started from Persona is the runtime's word for a session the hub started through it (Paseo keeps it on the
		// agent's label, #8130): it outlives a node restart in the middle of a start, which the manager then adopts
		// rather than starting a second agent (assistant/session_adopt.go). A daemon's sessions are another machine's
		// or another user's: never. The node's own record of a start says so too (decorateState).
		s.StartedByPersona = s.StartedByPersona && !daemon
		s.Assistant = false
		// Of the hierarchy (docs/protocol/protocol.md "Session hierarchy") the runtime says when the session began
		// (started_at); what it folds under and the rollup are the manager's, the parent a blocked child asks for the
		// node's (below), from what a daemon's own node named (daemons.go) when the row names no other.
		asking := s.AskingParentSessionID()
		s.SetFold("", "", "", nil)
		s.SetSubagentProvenance("", "", "")
		s.SetAskingParentSessionID("")
		if s.Agent == "" {
			s.Agent = agent
		}
		agy := agent == agyAgent && s.Agent == agyAgent && !daemon
		if agy && agyStopped(s.Status) {
			s.Background = nil
		}
		if s.AgentLabel == "" && (s.Agent == rt || (agent != "" && s.Agent == agent && agent != "claude") || daemon && s.Agent != "claude") {
			// The runtime runs its own agent (DSH, opencode-<pid>): the apps call it by the runtime's name. A daemon is called by the
			// name its config gives, whatever agent it runs (a platform running DSH), unless that is Claude Code.
			s.AgentLabel = label
		}
		if s.SpaceKind == "" {
			s.SpaceKind = rt
		}
		// The source fields Codex app-server sessions use too (docs/protocol/protocol.md "Codex sources and capabilities"): the apps gate
		// sending, interrupting, answering cards and every terminal control on capabilities, the hub guards sends.
		// What a runtime declared, never more: no terminal, no attach, cards always answerable (they are its own asks).
		s.SourceKind, s.SourceInstanceID, s.SourceThreadID, s.ParentThreadID = proto.SourceRuntime, rt, "", ""
		if rt == paseoRuntimeID || rt == cindyRuntimeID || rt == t3codeRuntimeID || rt == orcaRuntimeID && !daemon {
			// An engine host, though no terminal (#2576). Orca's chats (orcabridge.go) are its engine's too: the apps ask
			// that engine about the chat's worktree, not the computer's default one.
			s.Host = rt
		} else {
			s.Host = ""
		}
		// Rename and rewind (#6294) are for the runtime's own top-level sessions, not a sub-Agent's row: a "<session>@<id>"
		// is an agent inside a session, not one the runtime lists, and a session another one started folds under it.
		sub := s.IsSubagent || strings.Contains(s.SessionID, subagentSep)
		// A row may narrow what the runtime declared for its own session: its rewind, its modes and its model (what the
		// agent behind it can do: Paseo's providers differ, and Paseo changes nothing of an agent it keeps archived,
		// #8133). Only a row may offer to restore the files with a rewind. A row that says nothing keeps what the runtime
		// declared (an older bridge's rows).
		var own proto.SessionCapabilities
		if s.Capabilities != nil {
			own = *s.Capabilities
		}
		keeps := func(c *bool) bool { return c == nil || *c }
		rewind := caps.Rewind && !sub && keeps(own.Rewind)
		rewindCode := rewind && own.RewindCode != nil && *own.RewindCode
		// The efforts a row lists are what session.modes takes for it (Session.Efforts): none where the node refuses
		// session.modes (runtimes_calls.go) or the row takes its modes away.
		modes := caps.Modes && keeps(own.Modes) && !strings.Contains(s.SessionID, subagentSep)
		if !modes {
			s.Efforts = nil
		}
		// The permission modes it lists for an agent that is not Claude Code (#8261) go with its modes too, and only where
		// the apps' Claude Code chips do not stand (claudeModes) — except Antigravity (agy), which keeps capabilities.modes
		// true while advertising a narrower permission_modes subset when a native mode (such as auto) is unavailable.
		claudeModes := modes && runtimeModes(caps.Modes, rt, agent, s)
		permissionModes := own.PermissionModes
		if !modes || (claudeModes && s.Agent != agyAgent && agent != agyAgent) {
			permissionModes = 0
		}
		rowCaps := proto.SessionCapabilities{Read: true, Send: caps.Send, Interrupt: caps.Interrupt, Approve: true,
			A2AWakeOnSend:     caps.Send && own.A2AWakeOnSend && !sub && !s.Archived,
			SupervisionPolicy: caps.Assistant && caps.SupervisionPolicy && !sub,
			Close:             capBool(caps.Close), Rename: capBool(caps.Rename && !sub), Modes: capBool(claudeModes),
			Rewind: capBool(rewind), RewindCode: capBool(rewindCode), Fork: caps.Fork && !sub,
			QueueEdit: capBool(caps.QueueEdit && !sub), QueueSendNow: capBool(caps.QueueSendNow && !sub),
			Model:           capBool(caps.Modes && caps.Model && keeps(own.Model) && !strings.Contains(s.SessionID, subagentSep)),
			PermissionModes: permissionModes}
		if s.Capabilities = capsByValue[rowCaps]; s.Capabilities == nil {
			s.Capabilities = new(proto.SessionCapabilities)
			*s.Capabilities = rowCaps
			capsByValue[rowCaps] = s.Capabilities
		}
		s.ReadOnlyReason, s.Attached, s.CodexRoute, s.CodexRouteNote = readOnly, false, "", ""
		// The pane is the engine's to show (pi.go joins it with the engine's pane), not part of the runtime's row.
		if s.PaneID != "" && validPane(s.PaneID) {
			panes[s.SessionID] = s.PaneID
		}
		s.PaneID, s.TabID = "", ""
		if !ownWorkspaces || !runtimeIDRe.MatchString(s.WorkspaceID) {
			s.WorkspaceID = ""
		}
		switch s.Status {
		case proto.StatusWorking, proto.StatusDone, proto.StatusIdle, proto.StatusOffline:
		case proto.StatusBlocked:
			// A session another one started (is_subagent, parent_session_id) that waits for the user is a row of its
			// own while it does (#4494): the apps that fold every child away (iOS, Android, older web) leave out no
			// session that needs the user, whichever rows they count. Its parent is named again once it was answered.
			// Meanwhile asking_parent_session_id names it (#5120): the apps that show children as cards in their
			// parent's chat keep the card, and its question, there.
			parent := s.ParentSessionID
			if parent == "" && s.RootSessionID != s.SessionID {
				parent = s.RootSessionID
			}
			s.SetAskingParentSessionID(cmp.Or(parent, asking))
			s.IsSubagent, s.ParentSessionID, s.RootSessionID = false, "", ""
		default:
			s.Status = proto.StatusUnknown
		}
		if s.Title == "" && s.Cwd != "" {
			s.Title = filepath.Base(s.Cwd)
		}
		if s.UpdatedAt.IsZero() {
			s.UpdatedAt = time.Now().UTC()
		}
		s.UpdatedAt = s.UpdatedAt.UTC()
		if bd := s.ContextBreakdown().Sanitized(); bd != nil {
			if bd.Free == nil && s.ContextWindow > 0 && s.ContextTokens > 0 {
				bd.Free = proto.Int(max(int(s.ContextWindow)-int(s.ContextTokens), 0))
			}
			s.SetContextBreakdown(bd)
		} else if s.ContextBreakdown() != nil {
			s.SetContextBreakdown(nil)
		}
		s.LastText = preview(s.SessionID, s.LastText)
		runtimeLastText(&s)
		runtimeWork(&s, agy, preview)
		out = append(out, s)
	}
	clear(list[len(out):]) // the rows left out hold no strings
	return out
}

// runtimeWatch asks a runtime to push a session's new items, from since on; for a session whose conversation is a
// transcript here (transcript: true), only its live text. The caller owns the registered attempt and handles an error;
// success leaves its registration untouched.
func (n *Node) runtimeWatch(h *runtimeConn, id string, since int) error {
	ctx, cancel := context.WithTimeout(context.Background(), runtimeCallTimeout)
	defer cancel()
	params := map[string]any{"session_id": id, "since_seq": since}
	n.runtimes.mu.Lock()
	history := n.runtimes.lastHistory[id]
	n.runtimes.mu.Unlock()
	if history != "" {
		params["history"] = history
	}
	if n.runtimeTranscript(id) != "" {
		params["since_seq"], params["transcript"] = 0, true
	}
	if err := h.peer.Call(ctx, "session.watch", params, nil); err != nil {
		n.logger.Printf("runtime %s: watch %s: %v", h.id(), id, err)
		return err
	}
	return nil
}

// runtimeAsk forwards a runtime's question / plan / permission to the phone and returns the answer; {dismiss: true}
// when nobody answered (expired, handed back to the computer, no hub). A hub link that drops under a resumable
// interaction (one with an id the runtime chose or a tool call id) answers with code node_gone: the runtime asks the
// next node with the same id.
func (n *Node) runtimeAsk(ctx context.Context, h *runtimeConn, params json.RawMessage) (any, error) {
	var p struct {
		Interaction proto.Interaction `json:"interaction"`
		// TimeoutSeconds: how long the runtime waits at most (default: the hook timeout).
		TimeoutSeconds int `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad interaction: %w", err)
	}
	ix := p.Interaction
	switch ix.Kind {
	case proto.IxQuestion, proto.IxPlan, proto.IxPermission:
	default:
		return nil, fmt.Errorf("unsupported interaction kind %q", ix.Kind)
	}
	if !n.runtimes.owns(h, ix.SessionID) {
		return nil, fmt.Errorf("session %s is not listed by this runtime", ix.SessionID)
	}
	assistantSid := ix.SessionID
	s, hasRow := h.row(ix.SessionID)
	isAsstSession := false
	if n.state != nil {
		isAsstSession = n.isAssistantSession(assistantSid)
		if !isAsstSession {
			if parent, _, ok := splitRuntimeChild(ix.SessionID); ok && n.isAssistantSession(parent) {
				isAsstSession = true
				assistantSid = parent
			}
		}
	} else {
		if hasRow && identity.IsAssistantDir(s.Cwd) {
			isAsstSession = true
		} else if parent, _, ok := splitRuntimeChild(ix.SessionID); ok {
			if ps, ok := h.row(parent); ok && identity.IsAssistantDir(ps.Cwd) {
				isAsstSession = true
				assistantSid = parent
			}
		}
	}
	if !hasRow && assistantSid != ix.SessionID {
		s, hasRow = h.row(assistantSid)
	}
	var st sessionState
	if n.state != nil {
		st, _ = n.state.get(assistantSid)
	}
	sessionCwd := ""
	if hasRow {
		sessionCwd = s.Cwd
	}
	if st.Cwd != "" {
		sessionCwd = st.Cwd
	}
	rawCwd := strings.TrimSpace(ix.Cwd)
	execCwd := rawCwd
	if execCwd != "" {
		execCwd = proto.ExpandHome(execCwd, n.home)
	}
	if execCwd == "" {
		execCwd = sessionCwd
	} else if !filepath.IsAbs(execCwd) && sessionCwd != "" {
		execCwd = filepath.Join(sessionCwd, execCwd)
	}
	if n.runtimeSessionBlocked(ix.SessionID, "") ||
		(rawCwd != "" && n.runtimeSessionBlocked(ix.SessionID, rawCwd)) ||
		(execCwd != "" && execCwd != rawCwd && n.runtimeSessionBlocked(ix.SessionID, execCwd)) {
		return proto.Resolution{Dismiss: true}, nil // a blocked Agent folder (agentdirs.go): handed back to the computer
	}
	rt := h.id()
	// An id the runtime chose is the same when it asks again: the runtime SDK asks the next node with the same card
	// after a hub link drop or a node restart (its interactionId helper derives it from the tool call).
	ownID := ix.ID != "" && runtimeIDRe.MatchString(ix.ID)
	if !ownID {
		key := ix.ToolUseID
		if key == "" {
			key = newUUID()
		}
		sum := sha256.Sum256([]byte(rt + "\x00" + ix.SessionID + "\x00" + ix.Kind + "\x00" + key))
		ix.ID = "ix_" + hex.EncodeToString(sum[:])[:16]
	}
	// Who asks, as on a Codex card (proto.Interaction.Agent): the apps draw a runtime's card as its own agent's
	// ("Allow" / "Deny", its reason, "Always allow" only with an always_label it gives), not as Claude's, whose "Always allow for this session" a
	// runtime cannot keep. A runtime hosting Claude Code (agent "claude") gets Claude's card.
	h.mu.Lock()
	agent, label := h.hello.Agent, h.hello.Name
	h.mu.Unlock()
	if agent == "" {
		agent = rt
	}
	if label == "" {
		label = rt
	}
	// A runtime running several agents (Paseo: Claude Code and others) says which one each session runs: the card is
	// that agent's, and called by the name its row gives it whenever it gives one. Paseo's other providers (Grok, Kimi,
	// Cursor…) all run under Paseo's own id, each named by its provider's label: the card and its push say "Grok", not
	// the runtime's "Paseo" (#8142).
	if hasRow {
		if s.Agent != "" && s.Agent != agent {
			agent = s.Agent
		}
		if s.AgentLabel != "" {
			label = s.AgentLabel
		}
		if ix.SessionTitle == "" {
			ix.SessionTitle = s.Title
		}
	}
	if agent == "claude" {
		ix.Agent, ix.AgentLabel = "", ""
	} else {
		ix.Agent, ix.AgentLabel = agent, label
	}
	now := time.Now().UTC()
	ix.MachineID, ix.Screen, ix.Dialog, ix.Secret, ix.OutwardWrite, ix.EditableText = "", "", nil, false, false, ""
	if isAsstSession {
		if sessionCwd != "" && execCwd != "" {
			ix.Cwd = identity.CardCwd(sessionCwd, execCwd)
		} else if ix.Cwd == "" {
			ix.Cwd = execCwd
		}
	} else if ix.Cwd == "" {
		ix.Cwd = sessionCwd
	}
	// "Always allow for this session" kept by the hub for a runtime's permission card (#3974): what it grants is the node's word, never
	// the runtime's. Only for a runtime that asked for it in its hello (caps.keep_always), never in a space or on a card with fields.
	// Local assistant_environment runtimes (such as Antigravity) get scoped tool:* keys matching Claude Code hooks (directory-scoped
	// file tools, inspected non-inbox Bash, or tool:<name>); non-assistant cards that already carry a native always_label keep their
	// native rule without widening. Other keep_always runtimes (e.g. DSH) keep always_label-gated runtime:<rt>:tool:<tool> keys.
	inSpace := runtimeInSpace(h, ix) || identity.InSpace(sessionCwd) || identity.InSpace(execCwd) ||
		(hasRow && (s.Space != "" || identity.InSpace(s.Cwd))) || n.isSpaceSession(assistantSid)
	ix.AlwaysKeys = n.runtimeAlwaysKeys(h, rt, &ix, assistantSid, inSpace, isAsstSession)
	ix.CreatedAt = now
	timeout := nodeclient.HookTimeout
	if p.TimeoutSeconds > 0 && time.Duration(p.TimeoutSeconds)*time.Second < timeout {
		timeout = time.Duration(p.TimeoutSeconds) * time.Second
	}
	ix.ExpiresAt = now.Add(timeout)
	// Its id is the same when the runtime asks again through the next node: one the runtime chose, or one made from
	// its tool call. The hub then keeps the card through a hub link drop, and the drop answers node_gone (askOnce), not
	// a dismiss: a dismiss reached the runtime before it heard the hub was gone (detach), and the Paseo bridge took a
	// Codex or ACP card, which has no tool call id, for one the user handed back (#8128).
	ix.Resumable = ownID || ix.ToolUseID != ""
	if h.assistantEnvironment() && ix.Kind == proto.IxPermission && strings.EqualFold(ix.ToolName, "Bash") && isAsstSession {
		if res, handled, err := n.runtimeAssistantCommandGate(ctx, ix, assistantSid, sessionCwd, execCwd, hasRow, s); handled || err != nil {
			return res, err
		}
	}
	res, ok, err := n.ask(ctx, ix)
	if errors.Is(err, errNodeGone) {
		return nil, &rpc.Error{Message: "node going away", Code: "node_gone"}
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		return proto.Resolution{Dismiss: true}, nil
	}
	return res, nil
}

var truncatedCommandNoteRe = lazyre.New(`\n…（已截断，共 [0-9]+ 字节）$`)

// runtimeAssistantCommandGate runs a local assistant_environment runtime's Bash command through Persona's shared
// Lark/command gate (larkCheck) before ordinary permission prompting: classified reads auto-allow, writes confirm on
// the phone and issue one-use gateProxy grants, and configured or exfiltration rules deny. Truncated or empty commands
// fail closed to ordinary confirmation. Non-gated commands auto-allow only for non-guest sessions in bypass mode.
func (n *Node) runtimeAssistantCommandGate(ctx context.Context, ix proto.Interaction, session, sessionCwd, execCwd string, hasRow bool, s proto.Session) (any, bool, error) {
	var bashIn struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(ix.ToolInput, &bashIn) != nil || strings.TrimSpace(bashIn.Command) == "" || truncatedCommandNoteRe.MatchString(bashIn.Command) {
		return nil, false, nil
	}
	if identity.InSpace(sessionCwd) || (hasRow && (s.Space != "" || identity.InSpace(s.Cwd))) {
		n.gates().markSpace(session)
	}
	gateCwd := execCwd
	if gateCwd == "" {
		gateCwd = identity.AssistantDir(n.home)
	}
	gateIx := ix
	gateIx.SessionID = session
	gateIx.Agent, gateIx.AgentLabel, gateIx.AlwaysLabel, gateIx.AlwaysKeys = "", "", "", nil
	gateIx.ToolName = "Bash"
	if gateIx.SessionTitle == "" {
		gateIx.SessionTitle = n.assistantTitle(session, gateCwd)
	}
	cfg := n.assistantLark(ctx)
	call, _, original, editable := n.editableLarkCall(cfg, bashIn.Command, gateCwd)
	if editable {
		gateIx.EditableText = original
	}
	out, err := n.larkCheckOutcome(ctx, session, gateCwd, bashIn.Command, gateIx)
	if errors.Is(err, errNodeGone) {
		return nil, true, &rpc.Error{Message: "node going away", Code: "node_gone"}
	}
	if err != nil {
		return nil, true, err
	}
	switch out.decision {
	case "allow":
		if editable && out.editedText != "" && out.editedText != original {
			card := out.card
			if card.InteractionID == "" {
				card.InteractionID = gateIx.ID
			}
			if err := n.applyLarkProxyEdit(ctx, cfg, session, card, call, out.editedText); err != nil {
				return proto.Resolution{Allow: proto.Bool(false), Message: err.Error(), Reason: "gate_rule"}, true, nil
			}
		}
		return proto.Resolution{Allow: proto.Bool(true), Message: out.reason}, true, nil
	case "deny":
		if out.pending {
			return proto.Resolution{Dismiss: true, Pending: true, ApprovalID: out.approvalID, Message: out.reason}, true, nil
		}
		if out.dismiss {
			return proto.Resolution{Dismiss: true, ApprovalID: out.approvalID, Message: out.reason}, true, nil
		}
		if out.userDenied {
			return proto.Resolution{Allow: proto.Bool(false), Message: out.userNote}, true, nil
		}
		return proto.Resolution{Allow: proto.Bool(false), Message: out.reason, Reason: "gate_rule"}, true, nil
	}
	mode, _ := n.modesOf(session)
	rowMode := ""
	if hasRow {
		rowMode = s.PermissionMode
	}
	nonGuest := n.guestOf(session) == nil
	nodeBypass := mode == proto.ModeBypass && (rowMode == "" || rowMode == proto.ModeBypass)
	rowFallbackBypass := (mode == "" || mode == "auto") && rowMode == proto.ModeBypass
	if nonGuest && (nodeBypass || rowFallbackBypass) {
		return proto.Resolution{Allow: proto.Bool(true)}, true, nil
	}
	return nil, false, nil
}

// runtimeAlwaysKeys computes hub-remembered AlwaysKeys for a runtime permission card.
// Any runtime-supplied always_keys are ignored (caller replaces ix.AlwaysKeys with the return value).
func (n *Node) runtimeAlwaysKeys(h *runtimeConn, rt string, ix *proto.Interaction, assistantSid string, inSpace, isAsstSession bool) []string {
	h.mu.Lock()
	keepAlways := h.hello.Caps.KeepAlways
	remoteCap := h.hello.Caps.RemoteAgent
	h.mu.Unlock()
	if !keepAlways || ix.Kind != proto.IxPermission {
		return nil
	}
	// Remote-agent runtimes and sessions with latched RemoteAgent confirm-writes posture never get hub AlwaysKeys,
	// even if caps.keep_always and always_label are present.
	if remoteCap || h.remoteAgent() || n.isRemoteSession(assistantSid) || (ix.SessionID != assistantSid && n.isRemoteSession(ix.SessionID)) {
		if isAsstSession {
			ix.AlwaysLabel = ""
		}
		return nil
	}
	toolName := strings.TrimSpace(ix.ToolName)
	if h.assistantEnvironment() {
		if isAsstSession {
			// Assistant sessions never create native ACP allow_always grants; Persona's scoped AlwaysKeys govern.
			ix.AlwaysLabel = ""
		} else if strings.TrimSpace(ix.AlwaysLabel) != "" {
			// Non-assistant session with a native allow_always label (#15785): preserve its native scope without widening.
			return nil
		}
		if toolName == "" || len(ix.Fields) > 0 || inSpace {
			return nil
		}
		return n.scopedRuntimeAlwaysKeys(toolName, ix.ToolInput, assistantSid)
	}
	if strings.TrimSpace(ix.AlwaysLabel) == "" || toolName == "" || len(ix.Fields) > 0 || inSpace {
		return nil
	}
	if strings.EqualFold(toolName, "Bash") && n.assistantOrigin(assistantSid) == originInbox {
		return nil
	}
	return []string{"runtime:" + rt + ":tool:" + toolName}
}

// scopedRuntimeAlwaysKeys builds Claude-compatible scoped session AlwaysKeys for a local assistant_environment runtime.
// File tools require an absolute, non-truncated, non-escaping, non-root, non-space path (directory-scoped); Bash requires
// a parseable, non-empty, non-truncated command and never grants whole-tool tool:Bash on origin:inbox (#15292).
// Unrecognized tools fail closed whenever their input contains path-like or command-like fields.
func (n *Node) scopedRuntimeAlwaysKeys(toolName string, toolInput json.RawMessage, assistantSid string) []string {
	if strings.EqualFold(toolName, "Bash") {
		if n.assistantOrigin(assistantSid) == originInbox {
			return nil
		}
		var bashIn struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(toolInput, &bashIn) != nil || strings.TrimSpace(bashIn.Command) == "" || truncatedCommandNoteRe.MatchString(bashIn.Command) {
			return nil
		}
		return []string{"tool:Bash"}
	}
	lower := strings.ToLower(toolName)
	switch lower {
	case "read", "edit", "write", "multiedit", "notebookedit":
		scope := runtimeFileRuleScope(toolInput, n.home)
		if scope == "" {
			return nil
		}
		tool := "Edit"
		if lower == "read" {
			tool = "Read"
		}
		return []string{"tool:" + tool + "(" + scope + ")"}
	case "delete_file", "move_file", "remove_file", "rename_file", "copy_file",
		"multi_replace_file_content", "replace_file_content", "write_to_file", "write_file",
		"edit_file", "view_file", "read_file", "notebook_edit", "run_command", "execute_command", "shell":
		// Unmapped raw file/shell tool names must never fall through to unscoped tool:<name>.
		return nil
	}
	if !isKnownUnscopedAlwaysTool(toolName) && hasPathOrCommandInput(toolInput) {
		return nil
	}
	return []string{"tool:" + toolName}
}

func isKnownUnscopedAlwaysTool(toolName string) bool {
	if strings.HasPrefix(toolName, "mcp__") {
		return true
	}
	switch toolName {
	case "WebSearch", "WebFetch", "Grep", "Glob", "Agent":
		return true
	}
	return false
}

// hasPathOrCommandInput reports whether raw tool input is malformed or carries any path-like or command-like field
// (checked case-insensitively across nested JSON objects), causing unrecognized tools to fail closed.
func hasPathOrCommandInput(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return true
	}
	return valueHasPathOrCommandKey(v, 0)
}

func valueHasPathOrCommandKey(v any, depth int) bool {
	if depth > 8 {
		return true
	}
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			switch strings.ReplaceAll(strings.ToLower(k), "_", "") {
			case "filepath", "path", "targetfile", "absolutepath", "directorypath", "searchpath",
				"notebookpath", "source", "destination", "target",
				"command", "commandline", "cmd", "script":
				return true
			}
			if valueHasPathOrCommandKey(val, depth+1) {
				return true
			}
		}
	case []any:
		for _, item := range x {
			if valueHasPathOrCommandKey(item, depth+1) {
				return true
			}
		}
	}
	return false
}

func isWindowsDriveOrUNC(p string) bool {
	if len(p) >= 3 && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		return true
	}
	return strings.HasPrefix(p, `\\`)
}

// runtimeFileRuleScope extracts the directory rule ("//abs/dir/**") for a runtime file tool call.
// Returns "" for missing, relative, traversal (".."), trailing-separator, bare-home, truncated,
// root, space-targeted, or symlink-escaping paths so callers fail closed.
func runtimeFileRuleScope(input json.RawMessage, home string) string {
	var a struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	if json.Unmarshal(input, &a) != nil {
		return ""
	}
	p := strings.TrimSpace(a.FilePath)
	if p == "" {
		p = strings.TrimSpace(a.NotebookPath)
	}
	if p == "" || truncatedCommandNoteRe.MatchString(p) {
		return ""
	}
	p = proto.ExpandHome(p, home)
	// Reject bare ".", trailing separators, or any ".." path segment before Clean can collapse them.
	if p == "." || strings.HasSuffix(p, "/") || strings.HasSuffix(p, `\`) {
		return ""
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == "." || seg == ".." {
			return ""
		}
	}
	winPath := isWindowsDriveOrUNC(p)
	if !winPath && !filepath.IsAbs(p) {
		return ""
	}
	cleanHome := ""
	if home != "" {
		cleanHome = filepath.Clean(home)
	}
	if winPath {
		slash := strings.ReplaceAll(p, `\`, "/")
		if strings.HasPrefix(slash, "//") {
			parts := strings.Split(strings.TrimPrefix(slash, "//"), "/")
			// //server/share/dir/file requires at least 4 segments; //server/share/file's parent is the share root.
			if len(parts) < 4 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
				return ""
			}
		}
		dir := path.Dir(slash)
		if len(dir) <= 3 && strings.HasSuffix(strings.TrimSuffix(dir, "/"), ":") {
			return ""
		}
		if dir == "." || dir == "/" || path.Dir(dir) == dir {
			return ""
		}
		if cleanHome != "" {
			homeSlash := strings.TrimSuffix(strings.ReplaceAll(cleanHome, `\`, "/"), "/")
			if strings.EqualFold(strings.TrimSuffix(slash, "/"), homeSlash) || strings.EqualFold(strings.TrimSuffix(dir, "/"), homeSlash) {
				return ""
			}
		}
		if identity.InSpace(slash) || identity.InSpace(dir) {
			return ""
		}
		// On non-Windows hosts, return the lexically validated Windows directory key;
		// on Windows, fall through to run native os.Lstat / realPath symlink and dir checks.
		if runtime.GOOS != "windows" {
			if !strings.HasPrefix(dir, "/") {
				dir = "/" + dir
			}
			trimmed := strings.TrimSuffix(dir, "/")
			if trimmed == "" {
				return ""
			}
			return "/" + trimmed + "/**"
		}
	}
	cleanP := filepath.Clean(p)
	dir := filepath.Dir(cleanP)
	if filepath.Dir(dir) == dir {
		return ""
	}
	samePath := func(a, b string) bool {
		if runtime.GOOS == "windows" {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	if cleanHome != "" && (samePath(cleanP, cleanHome) || samePath(dir, cleanHome)) {
		return ""
	}
	// Reject direct file symlinks (both live and dangling), existing directories (whose Dir would widen
	// to the parent), and any symlink-based parent escape, while preserving lexical keys
	// (e.g. /tmp vs /private/tmp) when no escape occurs.
	if fi, err := os.Lstat(cleanP); err == nil && (fi.Mode()&os.ModeSymlink != 0 || fi.IsDir()) {
		return ""
	}
	realP := realPath(cleanP)
	realDir := realPath(dir)
	if !samePath(filepath.Dir(realP), realDir) || samePath(filepath.Dir(realDir), realDir) {
		return ""
	}
	if cleanHome != "" && samePath(realDir, realPath(cleanHome)) {
		return ""
	}
	if identity.InSpace(cleanP) || identity.InSpace(dir) || identity.InSpace(realP) || identity.InSpace(realDir) {
		return ""
	}
	slashDir := filepath.ToSlash(dir)
	if !strings.HasPrefix(slashDir, "/") {
		slashDir = "/" + slashDir
	}
	trimmed := strings.TrimSuffix(slashDir, "/")
	if trimmed == "" {
		return ""
	}
	return "/" + trimmed + "/**"
}

// runtimeInSpace: the card's session is in a space (its cwd or the row's), where every write is confirmed
// (docs/assistant/health.md "Outbound"). A directory inside the space counts: a tool's `cd` may have left either there
// (identity.InSpace, #2368).
func runtimeInSpace(h *runtimeConn, ix proto.Interaction) bool {
	if identity.InSpace(ix.Cwd) {
		return true
	}
	s, ok := h.row(ix.SessionID)
	return ok && (s.Space != "" || identity.InSpace(s.Cwd))
}

// runtimeModes is a runtime row's modes capability, the apps' Claude Code chips: the runtime declared caps.modes (the
// node refuses session.modes otherwise, runtimes_calls.go), the row is not a subagent's, and its agent takes Claude
// Code's modes (claude: Orca, Paseo's Claude providers) or is the runtime's own (agy). Paseo has no agent of its own:
// its bridge calls every agent other than Claude Code and Codex "paseo" (integrations/paseo/lib/map.js appAgent), and
// takes only its own approval and Allow all for those and for Codex (bridge.js modes). So a Paseo row running Codex,
// opencode or any other agent gets none, and older apps show no chips there; its row lists the modes it takes instead
// (capabilities.permission_modes, #8261).
func runtimeModes(declared bool, rt, rtAgent string, s proto.Session) bool {
	own := (s.Agent == rt || (rtAgent != "" && s.Agent == rtAgent)) && rt != paseoRuntimeID
	return declared && !strings.Contains(s.SessionID, subagentSep) && (s.Agent == "claude" || own)
}

// runtimeLastText bounds a runtime row's last_text, its preview (docs/integrations/runtimes.md), as the node's own
// rows' are (#16343): a runtime may report the whole reply there, which every list and frame would then carry. The
// question a long reply ends with is read off the whole of it first, as for a host's answer (sessionListIn): the
// manager reads a question off last_text itself only while it is the whole reply (assistant decorateAwaits).
func runtimeLastText(s *proto.Session) {
	if utf8.RuneCountInString(s.LastText) <= maxRuntimeLastText {
		return
	}
	if !s.AsksRead && s.Asks == "" {
		s.Asks, s.AsksRead = closeout.AskOf(s.LastText), true
	}
	s.LastText = snippet(s.LastText)
}
