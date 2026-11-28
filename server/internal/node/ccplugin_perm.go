package node

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Permission, plan, and question approval bridging for Persona's Claude Code plugin
// (integrations/claude-code-plugin, ccplugin.go).
//
// Outside Claude sessions (Claude Desktop, external terminals without a tmux pane, background sessions)
// do not run Persona's managed persona-hooks. When the session loads Persona's plugin with the
// "permissions" capability and passes ccPluginServesPermissions, the plugin bridges classic.PermissionRequest
// over POST /claude-plugin/v1/permission using renewable long polls (each <= 25 s under Claude Code's 30 s
// fetch limit) keyed by (session_id, pid, req_id). Each distinct request spawns one background goroutine
// reusing hooks.go's card builders, hookdecided.go dedup, and n.ask(ctx, ix), naturally supporting concurrent
// "Also waiting" prompts. Computer-side completions (classic.PostToolUse, classic.PermissionDenied,
// turn.complete, or a slower local hook winning the race) call POST /claude-plugin/v1/permission-done to
// cancel n.ask and withdraw the phone card immediately (recording answered_by: "terminal" for verified
// PostToolUse approvals).

const (
	// ccPluginPermBodyMost allows large ExitPlanMode plans or Write/Edit tool_input payloads on /permission.
	ccPluginPermBodyMost    = 4 << 20
	ccPluginPermRequestMost = ccPluginPermBodyMost + (64 << 10)
	// ccPluginPermDoneTTL retains settled permission results and completion receipts briefly so a racing
	// renewal poll or early turn/tool completion observes the final outcome instead of opening a duplicate card.
	ccPluginPermDoneTTL = 2 * time.Minute
	// ccPluginPersonaHookTTL bounds how long a remembered persona-hooks process stays in memory.
	ccPluginPersonaHookTTL = 24 * time.Hour
)

var (
	// ccPluginPermTTL bounds how long a single plugin permission card may stay open.
	ccPluginPermTTL = nodeclient.HookTimeout
	// ccPluginPermIdleTTL cancels an unanswered plugin permission card when no renewal arrives
	// (for example, Claude aborted the hook without emitting PostToolUse or turn.complete).
	ccPluginPermIdleTTL = 90 * time.Second
)

// ccPluginPermID scopes a plugin permission request to one live Claude process and session so two concurrent
// processes generating the same req_id in the same millisecond never collide.
type ccPluginPermID struct {
	sessionID string
	pid       int
	reqID     string
}

type ccPluginPersonaHook struct {
	pid int
	at  time.Time
}

type ccPluginPerm struct {
	id        ccPluginPermID
	reqID     string
	sessionID string
	pid       int
	toolUseID string
	toolName  string
	agentID   string
	permKey   string
	createdAt time.Time
	decided   *terminalDecided

	mu        sync.Mutex
	waiters   int
	idleTimer *time.Timer
	cancel    context.CancelFunc
	done      bool
	doneAt    time.Time
	action    string         // "decide", "passthrough", or "retry"
	decision  map[string]any // set when action == "decide"
	ready     chan struct{}
}

func (p *ccPluginPerm) attachCancel(cancel context.CancelFunc) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return false
	}
	p.cancel = cancel
	return true
}

func (p *ccPluginPerm) finish(action string, decision map[string]any) {
	p.mu.Lock()
	if p.done {
		p.mu.Unlock()
		return
	}
	p.done = true
	p.doneAt = time.Now()
	p.action = action
	p.decision = decision
	if p.idleTimer != nil {
		p.idleTimer.Stop()
		p.idleTimer = nil
	}
	close(p.ready)
	p.mu.Unlock()
}

func (p *ccPluginPerm) cancelAsPassthrough() {
	p.cancelWithReason(false)
}

func (p *ccPluginPerm) cancelWithReason(terminal bool) {
	p.mu.Lock()
	alreadyDone := p.done
	cancel := p.cancel
	if !alreadyDone {
		if terminal && p.decided != nil {
			p.decided.Store(true)
		}
		p.done = true
		p.doneAt = time.Now()
		p.action = "passthrough"
		p.decision = nil
		if p.idleTimer != nil {
			p.idleTimer.Stop()
			p.idleTimer = nil
		}
		close(p.ready)
	}
	p.mu.Unlock()
	if !alreadyDone && cancel != nil {
		cancel()
	}
}

func (p *ccPluginPerm) outcome() (action string, decision map[string]any, done bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.action, p.decision, p.done
}

func (p *ccPluginPerm) enterWait() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waiters++
	if p.idleTimer != nil {
		p.idleTimer.Stop()
		p.idleTimer = nil
	}
}

func (p *ccPluginPerm) leaveWait() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waiters > 0 {
		p.waiters--
	}
	if p.waiters == 0 && !p.done && p.idleTimer == nil {
		ttl := ccPluginPermIdleTTL
		p.idleTimer = time.AfterFunc(ttl, func() {
			p.cancelAsPassthrough()
		})
	}
}

// notePersonaHook records that session id runs Persona's managed non-report-only hooks (persona-hooks or
// claude-settings.json), immediately revoking plugin permission authorization and cancelling any active
// plugin permission card for that session to guarantee zero double cards.
func (r *ccPluginRegistry) notePersonaHook(sessionID string, pid int) {
	if sessionID == "" {
		return
	}
	now := time.Now()
	var toCancel []*ccPluginPerm
	r.mu.Lock()
	if r.personaHooks == nil {
		r.personaHooks = map[string]ccPluginPersonaHook{}
	}
	for sid, rec := range r.personaHooks {
		if now.Sub(rec.at) > ccPluginPersonaHookTTL {
			delete(r.personaHooks, sid)
		}
	}
	prev := r.personaHooks[sessionID]
	if pid <= 0 {
		pid = prev.pid
	}
	r.personaHooks[sessionID] = ccPluginPersonaHook{pid: pid, at: now}
	if s := r.sessions[sessionID]; s != nil && s.permAuthorized && (pid <= 0 || s.pid == pid) {
		s.permAuthorized = false
		if s.poll != nil {
			select {
			case s.poll <- ccPluginDelivery{Messages: []ccPluginMessage{}}:
			default:
			}
			s.poll, s.done = nil, nil
		}
	}
	for _, p := range r.perms {
		if p.sessionID == sessionID && (pid <= 0 || p.pid == pid) {
			toCancel = append(toCancel, p)
		}
	}
	r.mu.Unlock()
	for _, p := range toCancel {
		p.cancelAsPassthrough()
	}
}

func (r *ccPluginRegistry) hasPersonaHook(sessionID string, pid int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.personaHooks[sessionID]
	if !ok || time.Since(rec.at) > ccPluginPersonaHookTTL {
		return false
	}
	return rec.pid == 0 || pid == 0 || rec.pid == pid
}

// hasActivePermission reports whether session id currently has an unanswered plugin permission card.
func (r *ccPluginRegistry) hasActivePermission(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.perms {
		if p.sessionID != sessionID {
			continue
		}
		_, _, done := p.outcome()
		if !done {
			return true
		}
	}
	return false
}

// coversAsk reports whether an active plugin AskUserQuestion permission request already covers (sessionID, toolUseID).
// This allows outsideAskCard to suppress duplicate clq_ cards only when the plugin genuinely owns that question,
// preserving fallback clq_ cards for questions opened before the plugin's initial /poll authorization.
func (r *ccPluginRegistry) coversAsk(sessionID, toolUseID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.perms {
		if p.sessionID != sessionID || p.toolName != askTool {
			continue
		}
		_, _, done := p.outcome()
		if done {
			continue
		}
		if p.toolUseID == "" || (toolUseID != "" && p.toolUseID == toolUseID) {
			return true
		}
	}
	return false
}

// ccPluginServesPermissions reports whether claude c should bridge interactive permissions through the
// Persona plugin: it must be an outside session served by the plugin, not in an external tmux pane
// (which uses screen cards), not an assistant session, and not managed by persona-hooks.
func (n *Node) ccPluginServesPermissions(c claudeProc) bool {
	if !ccPluginServes(n, c) || outsidePane(c) != "" {
		return false
	}
	if n.isAssistantSession(c.SessionID) || n.hasPersonaHooks(c) {
		return false
	}
	return true
}

func (n *Node) hasPersonaHooks(c claudeProc) bool {
	if n.ccPlugins.hasPersonaHook(c.SessionID, c.pid) {
		return true
	}
	return n.claudeArgvHasPersonaHooks(c.pid)
}

func (n *Node) claudeArgvHasPersonaHooks(pid int) bool {
	if pid <= 0 {
		return false
	}
	args, err := processArgv(pid)
	if err != nil || len(args) == 0 {
		return false
	}
	baseDir := n.dir
	if baseDir == "" && n.home != "" {
		baseDir = filepath.Join(n.home, ".persona")
	}
	if baseDir == "" {
		return false
	}
	claudeHooksDir := filepath.Join(baseDir, "claude-hooks")
	assistantHooksDir := filepath.Join(baseDir, "assistant-hooks")
	claudeSettingsPath := filepath.Join(baseDir, "claude-settings.json")
	assistantSettingsPath := filepath.Join(baseDir, "assistant-settings.json")
	matchPath := func(got, want string) bool {
		if got == "" || want == "" {
			return false
		}
		if sameDir(got, want) {
			return true
		}
		return sameDir(filepath.Dir(filepath.Clean(got)), filepath.Dir(filepath.Clean(want))) &&
			filepath.Base(filepath.Clean(got)) == filepath.Base(filepath.Clean(want))
	}
	matchHooksDir := func(p string) bool {
		return matchPath(p, claudeHooksDir) || matchPath(p, assistantHooksDir)
	}
	matchSettings := func(p string) bool {
		return matchPath(p, claudeSettingsPath) || matchPath(p, assistantSettingsPath)
	}
	for i, a := range args {
		switch {
		case a == "--":
			return false
		case a == "--plugin-dir" && i+1 < len(args):
			if matchHooksDir(args[i+1]) {
				return true
			}
		case strings.HasPrefix(a, "--plugin-dir="):
			if matchHooksDir(strings.TrimPrefix(a, "--plugin-dir=")) {
				return true
			}
		case a == "--settings" && i+1 < len(args):
			if matchSettings(args[i+1]) {
				return true
			}
		case strings.HasPrefix(a, "--settings="):
			if matchSettings(strings.TrimPrefix(a, "--settings=")) {
				return true
			}
		}
	}
	return false
}

type ccPluginPermRequest struct {
	SessionID      string          `json:"session_id"`
	ReqID          string          `json:"req_id"`
	WaitMS         int64           `json:"wait_ms"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id"`
	Cwd            string          `json:"cwd"`
	TranscriptPath string          `json:"transcript_path"`
	PermissionMode string          `json:"permission_mode"`
	PromptID       string          `json:"prompt_id"`
	AgentID        string          `json:"agent_id"`
	AgentType      string          `json:"agent_type"`
	MCPServer      *struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	} `json:"mcp_server"`
}

// validCCPluginReqID matches bridge.ts's ID format: 1..128 ASCII letters, digits, '_' or '-'.
func validCCPluginReqID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// ccPluginPermission handles POST /claude-plugin/v1/permission: idempotently starts one card ask per
// (session_id, pid, req_id) and holds the HTTP request up to wait_ms (<= 25 s), returning decide, passthrough, or renew.
func (n *Node) ccPluginPermission(ctx context.Context, pid int, claude claudeProc, body []byte) (int, any) {
	var req ccPluginPermRequest
	if json.Unmarshal(body, &req) != nil || !validID(req.SessionID) || !validCCPluginReqID(req.ReqID) || req.ToolName == "" {
		return http.StatusBadRequest, map[string]any{"error": "invalid"}
	}
	if !n.ccPluginServesPermissions(claude) || n.peer == nil || n.ccPlugins.classicPassedOver(req.SessionID, pid) {
		return http.StatusOK, map[string]any{"action": "passthrough"}
	}
	if sess := n.claudeProcHerdrSession(claude); n.herdrSessionBlocked(sess) && !n.isAssistantSession(req.SessionID) && !n.keptSessions()[req.SessionID] {
		n.noteHerdrSessionID(req.SessionID, sess)
		return http.StatusOK, map[string]any{"action": "passthrough"}
	}
	cwd := cmp.Or(req.Cwd, claude.Cwd)
	if n.sessionBlocked(req.SessionID, cwd) {
		return http.StatusOK, map[string]any{"action": "passthrough"}
	}
	wait := time.Duration(req.WaitMS) * time.Millisecond
	if wait <= 0 || wait > ccPluginPollMost {
		wait = ccPluginPollMost
	}

	toolInput := req.ToolInput
	if len(toolInput) == 0 {
		toolInput = json.RawMessage("{}")
	}
	in := hookInput{
		HookEventName:  "PermissionRequest",
		SessionID:      req.SessionID,
		TranscriptPath: req.TranscriptPath,
		Cwd:            cwd,
		PermissionMode: req.PermissionMode,
		ToolName:       req.ToolName,
		ToolInput:      toolInput,
		ToolUseID:      req.ToolUseID,
		PromptID:       req.PromptID,
		AgentID:        req.AgentID,
		AgentType:      req.AgentType,
		MCPServer:      req.MCPServer,
	}
	permKey := ccPluginPermKey(in)

	entry, isNew, alreadyDone := n.ccPlugins.getOrCreatePerm(req, pid, permKey)
	if alreadyDone {
		return http.StatusOK, map[string]any{"action": "passthrough"}
	}
	if isNew {
		n.rememberTranscript(in)
		go n.runCCPluginPermission(entry, claude, in)
	}

	entry.enterWait()
	defer entry.leaveWait()

	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-entry.ready:
		action, decision, _ := entry.outcome()
		if action == "retry" {
			return http.StatusServiceUnavailable, map[string]any{"error": "node_restarting"}
		}
		if action == "decide" && decision != nil {
			return http.StatusOK, map[string]any{"action": "decide", "decision": decision}
		}
		return http.StatusOK, map[string]any{"action": "passthrough"}
	case <-t.C:
		return http.StatusOK, map[string]any{"action": "renew"}
	case <-ctx.Done():
		return http.StatusOK, map[string]any{"action": "renew"}
	}
}

func (r *ccPluginRegistry) getOrCreatePerm(req ccPluginPermRequest, pid int, permKey string) (entry *ccPluginPerm, isNew, alreadyDone bool) {
	now := time.Now()
	id := ccPluginPermID{sessionID: req.SessionID, pid: pid, reqID: req.ReqID}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prunePermsLocked(now)
	if existing, ok := r.perms[id]; ok {
		return existing, false, false
	}
	if at, ok := r.completedReqs[id]; ok && now.Sub(at) <= ccPluginPermDoneTTL {
		return nil, false, true
	}
	if req.ToolUseID != "" {
		if at, ok := r.completedTools[req.SessionID+"\x00"+req.ToolUseID]; ok && now.Sub(at) <= ccPluginPermDoneTTL {
			return nil, false, true
		}
	}
	if r.perms == nil {
		r.perms = map[ccPluginPermID]*ccPluginPerm{}
	}
	entry = &ccPluginPerm{
		id:        id,
		reqID:     req.ReqID,
		sessionID: req.SessionID,
		pid:       pid,
		toolUseID: req.ToolUseID,
		toolName:  req.ToolName,
		agentID:   req.AgentID,
		permKey:   permKey,
		createdAt: now,
		decided:   &terminalDecided{},
		ready:     make(chan struct{}),
	}
	r.perms[id] = entry
	return entry, true, false
}

func (r *ccPluginRegistry) prunePermsLocked(now time.Time) {
	for id, p := range r.perms {
		p.mu.Lock()
		expired := p.done && now.Sub(p.doneAt) > ccPluginPermDoneTTL
		p.mu.Unlock()
		if expired {
			delete(r.perms, id)
		}
	}
	for id, at := range r.completedReqs {
		if now.Sub(at) > ccPluginPermDoneTTL {
			delete(r.completedReqs, id)
		}
	}
	for k, at := range r.completedTools {
		if now.Sub(at) > ccPluginPermDoneTTL {
			delete(r.completedTools, k)
		}
	}
}

func ccPluginPermKey(in hookInput) string {
	if in.ToolName == askTool {
		in.ToolInput = questionInputWithoutAnswers(in.ToolInput)
	}
	return permissionKey(in)
}

// ccPluginPermAttempt scopes the interactionID fallback attempt salt to (pid, reqID) so two concurrent
// Claude processes sharing a session never generate the same Hub card ID when tool_use_id is absent,
// while renewals from the same process remain deterministic.
func ccPluginPermAttempt(pid int, reqID string) string {
	return fmt.Sprintf("p%d:%s", pid, reqID)
}

func (n *Node) runCCPluginPermission(entry *ccPluginPerm, claude claudeProc, in hookInput) {
	defer n.hookFlights.enter(in.SessionID)()

	baseCtx := context.WithValue(context.Background(), terminalDecidedKey{}, entry.decided)
	askCtx, cancel := context.WithTimeout(baseCtx, ccPluginPermTTL)
	defer cancel()
	if !entry.attachCancel(cancel) {
		return // already cancelled before goroutine started
	}
	watchCtx, stopWatch := n.watchAgent(askCtx, sockRequest{
		AgentPID:   claude.pid,
		AgentStart: claudeFileStart(claude.ProcStart),
	})
	defer stopWatch()

	ix := proto.Interaction{
		ID:        interactionID(in, ccPluginPermAttempt(entry.pid, entry.reqID)),
		SessionID: in.SessionID,
		Agent:     "claude",
		Cwd:       in.Cwd,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().Add(ccPluginPermTTL).UTC(),
		ToolName:  in.ToolName,
		ToolUseID: in.ToolUseID,
		ToolInput: in.ToolInput,
		Resumable: true,
		AgentID:   in.AgentID,
		AgentType: in.AgentType,
	}
	if in.MCPServer != nil {
		ix.MCPServerName, ix.MCPConfigSource = in.MCPServer.Name, in.MCPServer.Source
	}
	if p := n.transcriptPath(in.SessionID); p != "" {
		if st, err := os.Stat(p); err == nil {
			m := n.meta(p, st)
			ix.SessionTitle = m.title
			if ix.Cwd == "" {
				ix.Cwd = m.cwd
			}
		}
	}
	if ix.SessionTitle == "" && claude.NameSource != "derived" {
		ix.SessionTitle = claude.Name
	}
	if ix.SessionTitle == "" && ix.Cwd != "" {
		ix.SessionTitle = filepath.Base(ix.Cwd)
	}

	decision, retry := n.askCCPluginPermission(watchCtx, in, ix)
	if retry {
		entry.finish("retry", nil)
		return
	}
	if decision != nil {
		entry.finish("decide", decision)
		return
	}
	entry.finish("passthrough", nil)
}

func (n *Node) askCCPluginPermission(ctx context.Context, in hookInput, ix proto.Interaction) (decision map[string]any, retry bool) {
	switch in.ToolName {
	case "ExitPlanMode":
		var p struct {
			Plan string `json:"plan"`
		}
		_ = json.Unmarshal(in.ToolInput, &p)
		ix.Kind, ix.Plan, ix.PlanThen = proto.IxPlan, p.Plan, n.planThen(in.SessionID)
		ctx, answered, stop := n.decided.watch(ctx, permissionKey(in))
		defer stop()
		if n.decided.grace(ctx, answered) {
			return nil, false
		}
		res, ok, err := n.ask(ctx, ix)
		if errors.Is(err, errNodeGone) {
			return nil, true
		}
		if answered() || err != nil || !ok || res.Approve == nil {
			return nil, false
		}
		if !*res.Approve {
			msg := "用户在手机上打回了这个计划。"
			if strings.TrimSpace(res.Feedback) != "" {
				msg += "修改意见：" + res.Feedback
			}
			return map[string]any{"behavior": "deny", "message": msg}, false
		}
		mode := cmp.Or(proto.PlanAfterMode(ix.PlanThen), res.Mode, proto.ModeBypass)
		if n.peer != nil && res.ModeActor != nil {
			_ = n.peer.Notify(proto.EventModeSet, proto.ModeSetEvent{SessionID: in.SessionID, From: in.PermissionMode, To: mode, Actor: res.ModeActor})
		}
		var input map[string]any
		_ = json.Unmarshal(in.ToolInput, &input)
		if input == nil {
			input = map[string]any{}
		}
		return map[string]any{
			"behavior":           "allow",
			"updatedInput":       input,
			"updatedPermissions": []any{map[string]any{"type": "setMode", "mode": mode, "destination": "session"}},
		}, false

	case askTool:
		if !questionInteraction(in, &ix) {
			return nil, false
		}
		if proto.HasDuplicateQuestionText(ix.Questions) {
			return map[string]any{"behavior": "deny", "message": duplicateQuestionReason}, false
		}
		ctx, answered, stop := n.decided.watch(ctx, permissionKey(in))
		defer stop()
		if n.decided.grace(ctx, answered) {
			return nil, false
		}
		// If a fallback clq_ card is already published for this session, withdraw it synchronously before
		// publishing the replacement ix_ card so each pending question has only one home on the hub.
		if n.cx.hasOutsideAsk(in.SessionID) {
			n.syncCodexCards(ctx)
		}
		res, ok, err := n.ask(ctx, ix)
		if errors.Is(err, errNodeGone) {
			return nil, true
		}
		if answered() {
			return nil, false
		}
		if res.Dismiss {
			// Remember the owner's hand-back so subsequent scans do not resurrect a fallback clq_ card
			// while the computer still displays the same question.
			n.dismissOutsideAskFor(in.SessionID, in.ToolUseID)
			return nil, false
		}
		if err == nil && ok && res.Cancel {
			n.holdOutsideAskFor(in.SessionID, in.ToolUseID)
			return map[string]any{"behavior": "deny", "message": prompts.RenderQuestionCancelled()}, false
		}
		if err != nil || !ok || len(res.Answers) == 0 {
			return nil, false
		}
		n.holdOutsideAskFor(in.SessionID, in.ToolUseID)
		var input map[string]any
		_ = json.Unmarshal(in.ToolInput, &input)
		if input == nil {
			input = map[string]any{}
		}
		input["answers"] = res.Answers
		return map[string]any{"behavior": "allow", "updatedInput": input}, false

	default:
		ix.Kind, ix.Summary = proto.IxPermission, toolSummary(in.ToolName, in.ToolInput)
		ctx, answered, stop := n.decided.watch(ctx, permissionKey(in))
		defer stop()
		if n.decided.grace(ctx, answered) {
			return nil, false
		}
		res, ok, err := n.ask(ctx, ix)
		if errors.Is(err, errNodeGone) {
			return nil, true
		}
		if answered() || err != nil || !ok || res.Allow == nil {
			return nil, false
		}
		if !*res.Allow {
			msg := res.Message
			if msg == "" {
				msg = "用户在手机上拒绝了这次调用。"
			}
			return map[string]any{"behavior": "deny", "message": msg}, false
		}
		var input map[string]any
		_ = json.Unmarshal(in.ToolInput, &input)
		if input == nil {
			input = map[string]any{}
		}
		d := map[string]any{"behavior": "allow", "updatedInput": input}
		if res.Always {
			d["updatedPermissions"] = []any{map[string]any{
				"type":        "addRules",
				"rules":       []any{map[string]any{"toolName": in.ToolName}},
				"behavior":    "allow",
				"destination": "session",
			}}
		}
		return d, false
	}
}

type ccPluginPermDoneRequest struct {
	SessionID string          `json:"session_id"`
	ReqID     string          `json:"req_id"`
	ReqIDs    []string        `json:"req_ids"`
	ToolUseID string          `json:"tool_use_id"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	TurnID    string          `json:"turn_id"`
	AgentID   string          `json:"agent_id"`
	Reason    string          `json:"reason"`
}

// ccPluginPermissionDone handles POST /claude-plugin/v1/permission-done: cancels matching pending
// plugin permission asks by (session_id, pid, req_id), tool_use_id, turn, or unambiguous tool input key
// so the phone card withdraws immediately and any waiting renewal poll exits with passthrough.
// Verified computer-side approvals (reason "post_tool_use") withdraw with interaction.dismiss so history
// preserves answered_by: "terminal".
func (n *Node) ccPluginPermissionDone(pid int, claude claudeProc, body []byte) (int, any) {
	var req ccPluginPermDoneRequest
	if json.Unmarshal(body, &req) != nil || !validID(req.SessionID) {
		return http.StatusBadRequest, map[string]any{"error": "invalid"}
	}
	terminal := req.Reason == "post_tool_use"
	cancelled := n.ccPlugins.completePerm(req, pid)
	for _, p := range cancelled {
		p.cancelWithReason(terminal)
	}
	return http.StatusOK, map[string]any{"ok": true}
}

func (r *ccPluginRegistry) completePerm(req ccPluginPermDoneRequest, pid int) []*ccPluginPerm {
	now := time.Now()
	var matched []*ccPluginPerm
	seen := map[ccPluginPermID]bool{}
	add := func(p *ccPluginPerm) {
		if p != nil && !seen[p.id] {
			seen[p.id] = true
			matched = append(matched, p)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.prunePermsLocked(now)
	recordReq := func(rid string) {
		if !validCCPluginReqID(rid) {
			return
		}
		if r.completedReqs == nil {
			r.completedReqs = map[ccPluginPermID]time.Time{}
		}
		r.completedReqs[ccPluginPermID{sessionID: req.SessionID, pid: pid, reqID: rid}] = now
	}
	if req.ReqID != "" {
		recordReq(req.ReqID)
	}
	for _, rid := range req.ReqIDs {
		recordReq(rid)
	}
	if req.ToolUseID != "" {
		if r.completedTools == nil {
			r.completedTools = map[string]time.Time{}
		}
		r.completedTools[req.SessionID+"\x00"+req.ToolUseID] = now
	}
	if req.ReqID != "" {
		if p := r.perms[ccPluginPermID{sessionID: req.SessionID, pid: pid, reqID: req.ReqID}]; p != nil {
			add(p)
		}
	}
	for _, rid := range req.ReqIDs {
		if p := r.perms[ccPluginPermID{sessionID: req.SessionID, pid: pid, reqID: rid}]; p != nil {
			add(p)
		}
	}
	if req.ToolUseID != "" {
		for _, p := range r.perms {
			if p.sessionID == req.SessionID && p.pid == pid && p.toolUseID == req.ToolUseID {
				add(p)
			}
		}
	}
	if req.Reason == "turn_complete" && req.ReqID == "" && len(req.ReqIDs) == 0 {
		for _, p := range r.perms {
			if p.sessionID == req.SessionID && p.pid == pid && p.agentID == req.AgentID {
				add(p)
			}
		}
	}
	if len(matched) == 0 && req.ToolName != "" && req.ReqID == "" && len(req.ReqIDs) == 0 {
		key := ccPluginPermKey(hookInput{
			SessionID: req.SessionID,
			AgentID:   req.AgentID,
			ToolName:  req.ToolName,
			ToolInput: req.ToolInput,
		})
		var unpaired []*ccPluginPerm
		for _, p := range r.perms {
			if p.sessionID == req.SessionID && p.pid == pid && p.toolUseID == "" && p.permKey == key {
				_, _, done := p.outcome()
				if !done {
					unpaired = append(unpaired, p)
				}
			}
		}
		if len(unpaired) == 1 {
			add(unpaired[0])
		}
	}
	slices.SortFunc(matched, func(a, b *ccPluginPerm) int { return a.createdAt.Compare(b.createdAt) })
	return matched
}

func (n *Node) outsideAskIDFor(sessionID, toolUseID string) string {
	if toolUseID == "" {
		if c, ok := n.outsideClaude(sessionID); ok {
			if call := n.pendingOutsideAsk(c); call != nil {
				toolUseID = call.ToolUseID
			}
		}
	}
	if toolUseID == "" {
		return ""
	}
	return outsideAskPrefix + shortHash(sessionID+"\x00"+toolUseID)
}

func (n *Node) dismissOutsideAskFor(sessionID, toolUseID string) {
	if id := n.outsideAskIDFor(sessionID, toolUseID); id != "" {
		n.cx.dismiss(sessionID, id)
	}
}

func (n *Node) holdOutsideAskFor(sessionID, toolUseID string) {
	if id := n.outsideAskIDFor(sessionID, toolUseID); id != "" {
		n.cx.hold(sessionID, id)
	}
}
