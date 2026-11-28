package assistant

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Auditable context handoff for continuing a to-do on another provider or launcher (#12980, Phase A).
//
// When a coding Agent cannot continue on its current provider or launcher (Claude Code -> Codex,
// a Claude launcher whose config directory lacks the latest transcript, gateway 503s, expired sign-in,
// or exhausted quota), the hub builds and persists a deterministic, size-capped handoff artifact from:
//   - the to-do's brief (goal, owner request, effective criteria, out-of-bounds constraints),
//   - its effective recorded decisions (and continuation ancestors),
//   - the predecessor Agent's bounded transcript (anchored to seq ranges), and
//   - a scoped workspace Git snapshot (`git.review` against HEAD, labeled with its base).
//
// No summarization model call is made. The exact persisted summary text is injected as the first
// context block before the replacement Agent's task prompt, and delivery is tracked explicitly
// (`pending` -> `accepted` | `rejected` | `unknown`).
//
// Hard safety invariants:
//   - Provider safety refusals stay recorded blockers and MUST NEVER trigger a switch or replay.
//   - Unresolved approvals / pending interactions stay visible and are NEVER dismissed, granted, or replayed.
//   - A provider/launcher switch NEVER raises permissions over the predecessor execution.
//   - Retrying the same operation_id or racing concurrent requests cannot start two active workers
//     or inject context twice; unknown delivery stays pending and is never auto-replayed.

const (
	handoffStrategyFreshFull        = "fresh_full_summary"
	handoffGeneratorDeterministicV1 = "deterministic_v1"

	handoffDeliveryPending  = "pending"
	handoffDeliveryAccepted = "accepted"
	handoffDeliveryRejected = "rejected"
	handoffDeliveryUnknown  = "unknown"

	todoHandoffMaxRunes          = 12000
	todoHandoffTranscriptLimit   = 200
	todoHandoffTranscriptTimeout = 10 * time.Second
	todoHandoffDiffTimeout       = 15 * time.Second
)

// HandoffSourceRange anchors a transcript contribution to exact sequence numbers.
type HandoffSourceRange struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent,omitempty"`
	FromSeq   int    `json:"from_seq"`
	ToSeq     int    `json:"to_seq"`
	ItemCount int    `json:"item_count"`
	Truncated bool   `json:"truncated,omitempty"`
}

// HandoffDiffRef records the exact Git workspace snapshot base at handoff time.
type HandoffDiffRef struct {
	Scope     string `json:"scope"`
	BaseHash  string `json:"base_hash,omitempty"`
	BaseShort string `json:"base_short,omitempty"`
	BaseRef   string `json:"base_ref,omitempty"`
	HeadHash  string `json:"head_hash,omitempty"`
	HeadShort string `json:"head_short,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Root      string `json:"root,omitempty"`
	FileCount int    `json:"file_count"`
	Truncated bool   `json:"truncated,omitempty"`
}

// HandoffGap labels a missing, unreadable, or truncated source in a handoff artifact.
type HandoffGap struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// TodoContextHandoff is an auditable handoff record persisted for a to-do continuation.
type TodoContextHandoff struct {
	ID             string               `json:"id"`
	OperationID    string               `json:"operation_id"`
	TodoID         string               `json:"todo_id"`
	SourceMachine  string               `json:"source_machine,omitempty"`
	SourceSession  string               `json:"source_session,omitempty"`
	SourceLauncher string               `json:"source_launcher,omitempty"`
	SourceAgent    string               `json:"source_agent,omitempty"`
	TargetMachine  string               `json:"target_machine,omitempty"`
	TargetSession  string               `json:"target_session,omitempty"`
	TargetLauncher string               `json:"target_launcher,omitempty"`
	TargetAgent    string               `json:"target_agent,omitempty"`
	SourceRanges   []HandoffSourceRange `json:"source_ranges"`
	BriefRevision  string               `json:"brief_revision,omitempty"`
	DiffRef        *HandoffDiffRef      `json:"diff_ref,omitempty"`
	Strategy       string               `json:"strategy"`
	Generator      string               `json:"generator"`
	Summary        string               `json:"summary"`
	MissingSources []HandoffGap         `json:"missing_sources"`
	SupersedesID   string               `json:"supersedes_id,omitempty"`
	DeliveryStatus string               `json:"delivery_status"`
	DeliveryError  string               `json:"delivery_error,omitempty"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
	DeliveredAt    *time.Time           `json:"delivered_at,omitempty"`
}

type todoContextHandoffStore interface {
	CreateTodoContextHandoff(ctx context.Context, r db.TodoContextHandoffRow) (*db.TodoContextHandoffRow, bool, error)
	UpdateTodoContextHandoffDelivery(ctx context.Context, id, targetMachine, targetSession, targetAgent, status, deliveryError string, at time.Time) (*db.TodoContextHandoffRow, error)
	GetTodoContextHandoff(ctx context.Context, id string) (*db.TodoContextHandoffRow, error)
	GetTodoContextHandoffByOperation(ctx context.Context, todoID, operationID string) (*db.TodoContextHandoffRow, error)
	ListTodoContextHandoffs(ctx context.Context, todoID string) ([]db.TodoContextHandoffRow, error)
}

var _ todoContextHandoffStore = (*db.DB)(nil)

func (s *Service) todoHandoffDB() todoContextHandoffStore {
	if s.todoDB() == nil {
		return nil
	}
	st, _ := s.store().(todoContextHandoffStore)
	return st
}

// todoContextHandoffBook is the in-memory store used when no SQL store is attached (unit tests).
type todoContextHandoffBook struct {
	mu     sync.Mutex
	byID   map[string]TodoContextHandoff
	byOp   map[[2]string]string // (todo_id, operation_id) -> id
	byTodo map[string][]string  // todo_id -> ids (oldest first)
}

func (b *todoContextHandoffBook) init() {
	if b.byID == nil {
		b.byID = map[string]TodoContextHandoff{}
		b.byOp = map[[2]string]string{}
		b.byTodo = map[string][]string{}
	}
}

func todoContextHandoffToRow(h TodoContextHandoff) db.TodoContextHandoffRow {
	sr, _ := json.Marshal(nonNilSlice(h.SourceRanges))
	ms, _ := json.Marshal(nonNilSlice(h.MissingSources))
	var dr json.RawMessage
	if h.DiffRef != nil {
		dr, _ = json.Marshal(h.DiffRef)
	}
	return db.TodoContextHandoffRow{
		ID:             h.ID,
		OperationID:    h.OperationID,
		TodoID:         h.TodoID,
		SourceMachine:  h.SourceMachine,
		SourceSession:  h.SourceSession,
		SourceLauncher: h.SourceLauncher,
		SourceAgent:    h.SourceAgent,
		TargetMachine:  h.TargetMachine,
		TargetSession:  h.TargetSession,
		TargetLauncher: h.TargetLauncher,
		TargetAgent:    h.TargetAgent,
		SourceRanges:   sr,
		BriefRevision:  h.BriefRevision,
		DiffRef:        dr,
		Strategy:       h.Strategy,
		Generator:      h.Generator,
		Summary:        h.Summary,
		MissingSources: ms,
		SupersedesID:   h.SupersedesID,
		DeliveryStatus: h.DeliveryStatus,
		DeliveryError:  h.DeliveryError,
		CreatedAt:      h.CreatedAt,
		UpdatedAt:      h.UpdatedAt,
		DeliveredAt:    h.DeliveredAt,
	}
}

func todoContextHandoffFromRow(r db.TodoContextHandoffRow) TodoContextHandoff {
	h := TodoContextHandoff{
		ID:             r.ID,
		OperationID:    r.OperationID,
		TodoID:         r.TodoID,
		SourceMachine:  r.SourceMachine,
		SourceSession:  r.SourceSession,
		SourceLauncher: r.SourceLauncher,
		SourceAgent:    r.SourceAgent,
		TargetMachine:  r.TargetMachine,
		TargetSession:  r.TargetSession,
		TargetLauncher: r.TargetLauncher,
		TargetAgent:    r.TargetAgent,
		SourceRanges:   []HandoffSourceRange{},
		BriefRevision:  r.BriefRevision,
		Strategy:       r.Strategy,
		Generator:      r.Generator,
		Summary:        r.Summary,
		MissingSources: []HandoffGap{},
		SupersedesID:   r.SupersedesID,
		DeliveryStatus: r.DeliveryStatus,
		DeliveryError:  r.DeliveryError,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
		DeliveredAt:    r.DeliveredAt,
	}
	if len(r.SourceRanges) > 0 {
		_ = json.Unmarshal(r.SourceRanges, &h.SourceRanges)
		if h.SourceRanges == nil {
			h.SourceRanges = []HandoffSourceRange{}
		}
	}
	if len(r.DiffRef) > 0 && string(r.DiffRef) != "null" {
		var dr HandoffDiffRef
		if json.Unmarshal(r.DiffRef, &dr) == nil {
			h.DiffRef = &dr
		}
	}
	if len(r.MissingSources) > 0 {
		_ = json.Unmarshal(r.MissingSources, &h.MissingSources)
		if h.MissingSources == nil {
			h.MissingSources = []HandoffGap{}
		}
	}
	return h
}

func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func cloneTodoContextHandoff(h TodoContextHandoff) TodoContextHandoff {
	cp := h
	cp.SourceRanges = append([]HandoffSourceRange{}, h.SourceRanges...)
	cp.MissingSources = append([]HandoffGap{}, h.MissingSources...)
	if h.DiffRef != nil {
		dr := *h.DiffRef
		cp.DiffRef = &dr
	}
	if h.DeliveredAt != nil {
		t := *h.DeliveredAt
		cp.DeliveredAt = &t
	}
	return cp
}

// persistTodoContextHandoff stores h idempotently by (todo_id, operation_id).
// When an entry for that operation already exists, returns it with created=false.
func (s *Service) persistTodoContextHandoff(ctx context.Context, h TodoContextHandoff) (TodoContextHandoff, bool, error) {
	if h.ID == "" {
		h.ID = "handoff_" + randomHex(8)
	}
	if h.OperationID == "" {
		h.OperationID = "op_" + randomHex(12)
	}
	if h.Strategy == "" {
		h.Strategy = handoffStrategyFreshFull
	}
	if h.Generator == "" {
		h.Generator = handoffGeneratorDeterministicV1
	}
	if h.DeliveryStatus == "" {
		h.DeliveryStatus = handoffDeliveryPending
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = time.Now().UTC()
	}
	if h.UpdatedAt.IsZero() {
		h.UpdatedAt = h.CreatedAt
	}
	h.SourceRanges = nonNilSlice(h.SourceRanges)
	h.MissingSources = nonNilSlice(h.MissingSources)

	if st := s.todoHandoffDB(); st != nil {
		saved, created, err := st.CreateTodoContextHandoff(ctx, todoContextHandoffToRow(h))
		if err != nil {
			return TodoContextHandoff{}, false, err
		}
		if saved == nil {
			return TodoContextHandoff{}, false, errors.New("handoff record missing after create")
		}
		return todoContextHandoffFromRow(*saved), created, nil
	}

	b := &s.handoffs
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	key := [2]string{h.TodoID, h.OperationID}
	if existingID, ok := b.byOp[key]; ok {
		return cloneTodoContextHandoff(b.byID[existingID]), false, nil
	}
	cp := cloneTodoContextHandoff(h)
	b.byID[cp.ID] = cp
	b.byOp[key] = cp.ID
	b.byTodo[cp.TodoID] = append(b.byTodo[cp.TodoID], cp.ID)
	return cloneTodoContextHandoff(cp), true, nil
}

// recordTodoHandoffDelivery records the delivery outcome of handoff id.
func (s *Service) recordTodoHandoffDelivery(ctx context.Context, id, targetMachine, targetSession, targetAgent, status, deliveryError string) (TodoContextHandoff, error) {
	if id == "" {
		return TodoContextHandoff{}, nil
	}
	now := time.Now().UTC()
	if st := s.todoHandoffDB(); st != nil {
		row, err := st.UpdateTodoContextHandoffDelivery(ctx, id, targetMachine, targetSession, targetAgent, status, deliveryError, now)
		if err != nil {
			return TodoContextHandoff{}, err
		}
		if row == nil {
			return TodoContextHandoff{}, nil
		}
		return todoContextHandoffFromRow(*row), nil
	}
	b := &s.handoffs
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	h, ok := b.byID[id]
	if !ok {
		return TodoContextHandoff{}, nil
	}
	if targetMachine != "" {
		h.TargetMachine = targetMachine
	}
	if targetSession != "" {
		h.TargetSession = targetSession
	}
	if targetAgent != "" {
		h.TargetAgent = targetAgent
	}
	h.DeliveryStatus = status
	h.DeliveryError = deliveryError
	h.UpdatedAt = now
	if status == handoffDeliveryAccepted && h.DeliveredAt == nil {
		t := now
		h.DeliveredAt = &t
	}
	b.byID[id] = h
	return cloneTodoContextHandoff(h), nil
}

func (s *Service) getTodoContextHandoff(ctx context.Context, id string) (*TodoContextHandoff, error) {
	if id == "" {
		return nil, nil
	}
	if st := s.todoHandoffDB(); st != nil {
		row, err := st.GetTodoContextHandoff(ctx, id)
		if err != nil || row == nil {
			return nil, err
		}
		h := todoContextHandoffFromRow(*row)
		return &h, nil
	}
	b := &s.handoffs
	b.mu.Lock()
	defer b.mu.Unlock()
	if h, ok := b.byID[id]; ok {
		cp := cloneTodoContextHandoff(h)
		return &cp, nil
	}
	return nil, nil
}

func (s *Service) getTodoContextHandoffByOp(ctx context.Context, todoID, operationID string) (*TodoContextHandoff, error) {
	if todoID == "" || operationID == "" {
		return nil, nil
	}
	if st := s.todoHandoffDB(); st != nil {
		row, err := st.GetTodoContextHandoffByOperation(ctx, todoID, operationID)
		if err != nil || row == nil {
			return nil, err
		}
		h := todoContextHandoffFromRow(*row)
		return &h, nil
	}
	b := &s.handoffs
	b.mu.Lock()
	defer b.mu.Unlock()
	if id, ok := b.byOp[[2]string{todoID, operationID}]; ok {
		cp := cloneTodoContextHandoff(b.byID[id])
		return &cp, nil
	}
	return nil, nil
}

func (s *Service) listTodoContextHandoffs(ctx context.Context, todoID string) ([]TodoContextHandoff, error) {
	if todoID == "" {
		return nil, nil
	}
	if st := s.todoHandoffDB(); st != nil {
		rows, err := st.ListTodoContextHandoffs(ctx, todoID)
		if err != nil {
			return nil, err
		}
		out := make([]TodoContextHandoff, 0, len(rows))
		for _, r := range rows {
			out = append(out, todoContextHandoffFromRow(r))
		}
		return out, nil
	}
	b := &s.handoffs
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := b.byTodo[todoID]
	out := make([]TodoContextHandoff, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		if h, ok := b.byID[ids[i]]; ok {
			out = append(out, cloneTodoContextHandoff(h))
		}
	}
	slices.SortFunc(out, func(a, c TodoContextHandoff) int {
		if cmpAt := c.CreatedAt.Compare(a.CreatedAt); cmpAt != 0 {
			return cmpAt
		}
		return strings.Compare(c.ID, a.ID)
	})
	return out, nil
}

func (s *Service) latestTodoContextHandoff(ctx context.Context, todoID string) *TodoContextHandoff {
	list, err := s.listTodoContextHandoffs(ctx, todoID)
	if err != nil || len(list) == 0 {
		return nil
	}
	h := list[0]
	return &h
}

// ---- Safety refusal, unresolved approval, and permission-escalation guards ----

// sessionSafetyRefusal reports whether session c is currently blocked by a provider safety/policy
// refusal. A safety refusal stays a recorded blocker and must never trigger a provider switch or
// replay of the refused action (#12980).
func sessionSafetyRefusal(c proto.Session) (bool, string) {
	if c.ModelFallback != nil {
		cat := strings.ToLower(strings.TrimSpace(c.ModelFallback.Category))
		sub := strings.ToLower(strings.TrimSpace(c.ModelFallback.Subtype))
		switch cat {
		case proto.ModelRecoveryReasonSafeguards:
			return true, cmp.Or(c.ModelFallback.Explanation, c.ModelFallback.Text, "provider safety refusal")
		case proto.ModelRecoveryReasonQuota, proto.ModelRecoveryReasonRateLimit,
			proto.ModelRecoveryReasonServer, proto.ModelRecoveryReasonUnavailable:
			// Explicitly non-safety model recovery categories are eligible for provider switches.
		default:
			if strings.Contains(sub, "refusal") {
				return true, cmp.Or(c.ModelFallback.Explanation, c.ModelFallback.Text, "provider safety refusal")
			}
		}
	}
	if sessionTurnErrorCode(c) == proto.TurnErrorModelFallbackUnavailable {
		return true, cmp.Or(c.TurnError, "provider safety refusal fallback unavailable")
	}
	for _, sample := range []string{c.TurnError, c.LastText} {
		if safetyRefusalText(sample) {
			return true, clip(oneLine(sample), 180)
		}
	}
	return false, ""
}

func safetyRefusalText(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return false
	}
	return strings.Contains(t, "safeguards flagged") ||
		(strings.Contains(t, "can't help with this") && strings.Contains(t, "start a new session to continue")) ||
		strings.Contains(t, "safety classifier refused") ||
		strings.Contains(t, "safety_classifier") ||
		strings.Contains(t, "safety refusal") ||
		strings.Contains(t, "content_filter") ||
		strings.Contains(t, `"stop_reason":"refusal"`) ||
		strings.Contains(t, `"stop_reason": "refusal"`)
}

// sessionUnresolvedApproval reports whether session c has an unresolved approval or owner interaction.
func (s *Service) sessionUnresolvedApproval(c proto.Session) bool {
	if c.PendingInteractions > 0 || c.Status == proto.StatusBlocked {
		return true
	}
	if c.MachineID != "" && c.SessionID != "" {
		for _, ix := range s.pendingInteractions() {
			if ix.MachineID == c.MachineID && ix.SessionID == c.SessionID {
				return true
			}
		}
	}
	return false
}

// permissionRank orders permission modes from most restrictive to most permissive so a provider or
// launcher switch never silently or explicitly raises permissions.
func permissionRank(mode string, readonly bool) int {
	m := strings.TrimSpace(mode)
	if readonly && (m == "" || m == "plan") {
		return 0
	}
	switch m {
	case "plan":
		return 0
	case "", "default":
		return 1
	case "auto":
		return 2
	case "acceptEdits":
		return 2
	case "dontAsk", "bypassPermissions":
		return 3
	default:
		return 1
	}
}

type todoPredecessorInfo struct {
	agent        TodoAgent
	session      proto.Session
	hasSess      bool
	planLauncher string
	planPerm     string
	planReadonly bool
}

// todoPredecessor returns the active (or most recently linked) Agent for todo, with its live/decorated
// session snapshot when available, falling back to the to-do's current plan assignment for persisted
// permission/readonly metadata when the live session's permission mode is empty or unavailable (#12980).
func (s *Service) todoPredecessor(ctx context.Context, todo Todo) (todoPredecessorInfo, bool) {
	var picked TodoAgent
	found := false
	for _, a := range todo.Agents {
		if a.active() {
			if !found || !picked.active() || a.LinkedAt.After(picked.LinkedAt) {
				picked, found = a, true
			}
		} else if !found || (!picked.active() && a.LinkedAt.After(picked.LinkedAt)) {
			picked, found = a, true
		}
	}
	if !found {
		return todoPredecessorInfo{}, false
	}
	info := todoPredecessorInfo{agent: picked}
	if c, ok := s.decoratedSession(picked.MachineID, picked.SessionID); ok {
		info.session, info.hasSess = c, true
	} else if c, ok := s.session(ctx, picked.MachineID, picked.SessionID); ok {
		info.session, info.hasSess = c, true
	}
	if strings.TrimSpace(info.session.PermissionMode) == "" || strings.TrimSpace(info.session.Launcher) == "" {
		if p, idx, err := s.currentPlanAssignment(ctx, todo); err == nil && p != nil && idx >= 0 && idx < len(p.Tasks) {
			pt := p.Tasks[idx]
			info.planLauncher = strings.TrimSpace(pt.Launcher)
			if strings.TrimSpace(info.session.PermissionMode) == "" {
				info.planPerm = strings.TrimSpace(cmp.Or(pt.StartPermission, pt.Permission))
				info.planReadonly = pt.Readonly
				if info.planPerm == "" && info.planReadonly {
					info.planPerm = "plan"
				}
			}
		}
	}
	return info, true
}

func (p todoPredecessorInfo) launcher() string {
	if p.hasSess && p.session.Launcher != "" {
		return p.session.Launcher
	}
	if p.planLauncher != "" {
		return p.planLauncher
	}
	if p.hasSess && p.session.Agent != "" {
		return p.session.Agent
	}
	if p.agent.Agent != "" {
		return p.agent.Agent
	}
	return "claude"
}

func (p todoPredecessorInfo) agentKind() string {
	if p.hasSess && p.session.Agent != "" {
		return p.session.Agent
	}
	if p.agent.Agent != "" {
		return p.agent.Agent
	}
	return "claude"
}

func (p todoPredecessorInfo) cwd() string {
	if p.hasSess && p.session.Cwd != "" {
		return p.session.Cwd
	}
	return p.agent.Cwd
}

func (p todoPredecessorInfo) permission() string {
	if p.hasSess {
		if m := strings.TrimSpace(p.session.PermissionMode); m != "" {
			return m
		}
	}
	if p.planPerm != "" {
		return p.planPerm
	}
	if p.planReadonly {
		return "plan"
	}
	return ""
}

type continuationSwitch struct {
	machineChanged  bool
	launcherChanged bool
}

func (c continuationSwitch) any() bool {
	return c.machineChanged || c.launcherChanged
}

// continuationSwitchKind classifies how continuing todo on (targetMachine, targetLauncher, targetAgent)
// differs from its predecessor. Both machine moves and launcher/provider switches require auditable handoffs
// and fail-closed non-escalation checks; only launcher/provider switches always require an explicit owner
// Start card when an orchestrator (main chat or direction leader) dispatches the continuation.
func (s *Service) continuationSwitchKind(ctx context.Context, todo Todo, targetMachine, targetLauncher, targetAgent string) continuationSwitch {
	pred, ok := s.todoPredecessor(ctx, todo)
	if !ok {
		return continuationSwitch{}
	}
	var sw continuationSwitch
	if targetMachine != "" && pred.agent.MachineID != "" && targetMachine != pred.agent.MachineID {
		sw.machineChanged = true
	}
	srcLauncher := cmp.Or(pred.launcher(), "claude")
	dstLauncher := s.startLauncher(ctx, strings.TrimSpace(targetLauncher))
	if dstLauncher != srcLauncher {
		sw.launcherChanged = true
	} else if targetAgent != "" && pred.agentKind() != "" && targetAgent != pred.agentKind() {
		sw.launcherChanged = true
	}
	return sw
}

// isProviderSwitchContinuation reports whether continuing todo on (targetMachine, targetLauncher)
// moves work to another launcher, provider, or machine relative to its predecessor.
func (s *Service) isProviderSwitchContinuation(ctx context.Context, todo Todo, targetMachine, targetLauncher string) bool {
	return s.continuationSwitchKind(ctx, todo, targetMachine, targetLauncher, "").any()
}

// continuationNeedsOwner keeps implicit health detours and non-supervisory continuations behind
// Start. An authorized supervisor may choose another launcher/provider as well as another owned
// machine: handoffLauncherSwitch records that choice, not a new owner-only decision. This predicate
// never grants execution permission; both planning and dispatch enforce checkContinuationBlockers.
func (task PlanTask) continuationNeedsOwner(orchestrates bool) bool {
	return task.HandoffOperationID != "" && (task.Reroute != nil || !orchestrates)
}

// handoffBlockerError carries a machine-readable refusal code for HTTP callers.
type handoffBlockerError struct {
	code string
	msg  string
}

func (e handoffBlockerError) Error() string { return e.msg }

// checkContinuationBlockers enforces the safety-refusal, unresolved-approval, and non-escalation
// invariants before a continuation or provider switch may proceed.
func (s *Service) checkContinuationBlockers(ctx context.Context, todo Todo, targetPermission string, targetReadonly, isProviderSwitch bool) error {
	for _, a := range todo.Agents {
		if !a.active() {
			continue
		}
		var sess proto.Session
		var has bool
		if c, ok := s.decoratedSession(a.MachineID, a.SessionID); ok {
			sess, has = c, true
		} else if c, ok := s.session(ctx, a.MachineID, a.SessionID); ok {
			sess, has = c, true
		}
		if !has {
			sess = proto.Session{MachineID: a.MachineID, SessionID: a.SessionID}
		}
		if refused, why := sessionSafetyRefusal(sess); refused {
			return handoffBlockerError{
				code: "safety_refusal",
				msg: fmt.Sprintf("to-do %s is blocked by a provider safety refusal on %s/%s (%s): a safety refusal remains a recorded blocker and cannot trigger a provider switch or replay",
					todo.ID, a.MachineID, short(a.SessionID), why),
			}
		}
		if s.sessionUnresolvedApproval(sess) {
			return handoffBlockerError{
				code: "unresolved_approval",
				msg: fmt.Sprintf("to-do %s has an unresolved approval or question on %s/%s: resolve it directly with the owner first; switching never grants or dismisses approvals",
					todo.ID, a.MachineID, short(a.SessionID)),
			}
		}
	}
	if isProviderSwitch {
		if pred, ok := s.todoPredecessor(ctx, todo); ok {
			srcPerm := pred.permission()
			srcReadonly := srcPerm == "plan" || pred.planReadonly
			// Fail closed when predecessor permissions cannot be established from the live session or
			// persisted plan assignment: treat unknown predecessor mode as "default" (rank 1) so
			// switching never silently or explicitly escalates to auto/acceptEdits/dontAsk/bypassPermissions.
			effectiveSrc := cmp.Or(srcPerm, "default")
			srcRank := permissionRank(effectiveSrc, srcReadonly)
			dstPerm := strings.TrimSpace(targetPermission)
			if dstPerm != "" || targetReadonly {
				dstRank := permissionRank(dstPerm, targetReadonly)
				if dstRank > srcRank {
					return handoffBlockerError{
						code: "permission_escalation",
						msg: fmt.Sprintf("provider switch cannot raise permissions on to-do %s from %q to %q",
							todo.ID, effectiveSrc, cmp.Or(dstPerm, "default")),
					}
				}
			}
		}
	}
	return nil
}

// ---- Deterministic handoff generator (no model call) ----

var (
	todoHandoffReminderRe = lazyre.New(`(?s)<system-reminder\b[^>]*>.*?</system-reminder>`)
	todoHandoffDataURIRe  = lazyre.New(`data:image/[\w.+-]+;base64,[A-Za-z0-9+/=]+`)
	todoHandoffPatchRe    = lazyre.New(`(?m)^\*\*\* (?:Add File|Update File|Delete File|Move to): (.+)$`)
	todoHandoffExitRe     = lazyre.New(`(?i)\bexit(?:ed)?(?:\s+code)?[:\s]+(-?\d+)`)
	todoHandoffFailRe     = lazyre.New(`\bFAIL\s+(\S+)`)
)

type todoHandoffRenderLevel struct {
	requestCap  int
	decisionCap int
	turnCount   int
	turnCap     int
	fileCap     int
	cmdCap      int
	diffFileCap int
}

var todoHandoffRenderLevels = []todoHandoffRenderLevel{
	{requestCap: 1200, decisionCap: 20, turnCount: 8, turnCap: 600, fileCap: 25, cmdCap: 12, diffFileCap: 40},
	{requestCap: 800, decisionCap: 15, turnCount: 6, turnCap: 400, fileCap: 18, cmdCap: 8, diffFileCap: 25},
	{requestCap: 400, decisionCap: 10, turnCount: 4, turnCap: 240, fileCap: 12, cmdCap: 6, diffFileCap: 15},
	{requestCap: 240, decisionCap: 6, turnCount: 2, turnCap: 160, fileCap: 8, cmdCap: 4, diffFileCap: 10},
	{requestCap: 160, decisionCap: 4, turnCount: 1, turnCap: 120, fileCap: 5, cmdCap: 3, diffFileCap: 5},
}

type todoHandoffBuildInput struct {
	Todo           Todo
	SourceMachine  string
	SourceSession  string
	SourceLauncher string
	SourceAgent    string
	SourceCwd      string
	KnownFailure   string
	TargetMachine  string
	TargetLauncher string
	TargetAgent    string
	TargetCwd      string
	OperationID    string
	SupersedesID   string
}

// buildTodoContextHandoff deterministically assembles an auditable context handoff artifact from
// the to-do brief, recorded decisions, predecessor transcript, and scoped Git diff. No model is called.
func (s *Service) buildTodoContextHandoff(ctx context.Context, in todoHandoffBuildInput) TodoContextHandoff {
	now := time.Now().UTC()
	h := TodoContextHandoff{
		ID:             "handoff_" + randomHex(8),
		OperationID:    cmp.Or(strings.TrimSpace(in.OperationID), "op_"+randomHex(12)),
		TodoID:         in.Todo.ID,
		SourceMachine:  in.SourceMachine,
		SourceSession:  in.SourceSession,
		SourceLauncher: in.SourceLauncher,
		SourceAgent:    in.SourceAgent,
		TargetMachine:  in.TargetMachine,
		TargetLauncher: in.TargetLauncher,
		TargetAgent:    in.TargetAgent,
		SourceRanges:   []HandoffSourceRange{},
		Strategy:       handoffStrategyFreshFull,
		Generator:      handoffGeneratorDeterministicV1,
		MissingSources: []HandoffGap{},
		SupersedesID:   in.SupersedesID,
		DeliveryStatus: handoffDeliveryPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	// 1. Brief (goal, owner request, effective criteria, constraints, exact revision).
	var goal, reqText string
	var criteria, outOfBounds []string
	if brief, ok := s.briefOfTodo(in.Todo.ID); ok {
		h.BriefRevision = brief.ID + "@" + brief.scopeRevision().UTC().Format(time.RFC3339Nano)
		goal = brief.Goal
		if brief.Request != nil {
			reqText = strings.TrimSpace(brief.Request.Text)
		}
		criteria = append([]string(nil), brief.Criteria...)
		outOfBounds = append([]string(nil), brief.OutOfBounds...)
	} else {
		goal = in.Todo.Title
		h.MissingSources = append(h.MissingSources, HandoffGap{
			Source: "brief",
			Reason: "no task brief is recorded for this to-do",
		})
	}

	// 2. Effective decisions (this to-do and its continuation ancestors, oldest first).
	var decisions []prompts.TodoContextDecision
	if st := s.todoDB(); st != nil {
		ids := s.continuationTodos(in.Todo.ID)
		var rows []db.TodoDecisionRow
		for _, id := range ids {
			part, err := st.ListTodoDecisions(ctx, id, false)
			if err != nil {
				h.MissingSources = append(h.MissingSources, HandoffGap{
					Source: "decisions",
					Reason: fmt.Sprintf("could not read decisions for %s: %s", id, clip(oneLine(err.Error()), 120)),
				})
				continue
			}
			rows = append(rows, part...)
		}
		slices.SortFunc(rows, func(a, b db.TodoDecisionRow) int {
			if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
				return c
			}
			return strings.Compare(a.ID, b.ID)
		})
		for _, r := range rows {
			by := "user"
			if r.Source == db.DecisionAgent || r.Source == db.DecisionOutcome {
				by = "agent"
			}
			decisions = append(decisions, prompts.TodoContextDecision{
				By:       by,
				Question: clip(oneLine(r.Question), 160),
				Choice:   clip(oneLine(r.Choice), 200),
				Reason:   clip(oneLine(r.Reason), 160),
			})
		}
	}

	// 3. Predecessor transcript (bounded read via session.items).
	var touchedFiles, commands, failedCmds []string
	var turns []prompts.TodoContextTurn
	if in.SourceMachine != "" && in.SourceSession != "" {
		tctx, cancel := context.WithTimeout(ctx, todoHandoffTranscriptTimeout)
		var page struct {
			Items   []proto.Item `json:"items"`
			HasMore bool         `json:"has_more"`
		}
		err := s.call(tctx, in.SourceMachine, DeviceSessionItems, map[string]any{
			"session_id":  in.SourceSession,
			"limit":       todoHandoffTranscriptLimit,
			"lazy_images": true,
		}, &page)
		cancel()
		if err != nil {
			h.MissingSources = append(h.MissingSources, HandoffGap{
				Source: "transcript",
				Reason: fmt.Sprintf("could not read predecessor transcript %s/%s: %s", in.SourceMachine, short(in.SourceSession), clip(oneLine(err.Error()), 120)),
			})
		} else if len(page.Items) == 0 {
			h.MissingSources = append(h.MissingSources, HandoffGap{
				Source: "transcript",
				Reason: fmt.Sprintf("predecessor transcript %s/%s had no readable items", in.SourceMachine, short(in.SourceSession)),
			})
		} else {
			fromSeq, toSeq := page.Items[0].Seq, page.Items[len(page.Items)-1].Seq
			for _, it := range page.Items {
				if it.Seq > 0 && (fromSeq == 0 || it.Seq < fromSeq) {
					fromSeq = it.Seq
				}
				if it.Seq > toSeq {
					toSeq = it.Seq
				}
			}
			h.SourceRanges = append(h.SourceRanges, HandoffSourceRange{
				MachineID: in.SourceMachine,
				SessionID: in.SourceSession,
				Agent:     in.SourceAgent,
				FromSeq:   fromSeq,
				ToSeq:     toSeq,
				ItemCount: len(page.Items),
				Truncated: page.HasMore,
			})
			if page.HasMore {
				h.MissingSources = append(h.MissingSources, HandoffGap{
					Source: "transcript",
					Reason: fmt.Sprintf("earlier transcript items before seq %d were omitted (capped at latest %d items)", fromSeq, len(page.Items)),
				})
			}
			touchedFiles, commands, failedCmds, turns = extractTodoHandoffTranscript(page.Items)
		}
	} else {
		h.MissingSources = append(h.MissingSources, HandoffGap{
			Source: "transcript",
			Reason: "no predecessor session was linked to this to-do",
		})
	}

	// 4. Scoped Git workspace snapshot at handoff time (git.review against HEAD).
	diffMachine := cmp.Or(in.TargetMachine, in.SourceMachine)
	diffCwd := cmp.Or(in.TargetCwd, in.SourceCwd)
	var diffFiles []string
	var diffBaseLabel, diffHeadLabel, diffBranch string
	var diffTruncated bool
	if diffMachine != "" && diffCwd != "" {
		dctx, cancel := context.WithTimeout(ctx, todoHandoffDiffTimeout)
		var rev proto.GitReview
		err := s.call(dctx, diffMachine, "git.review", proto.GitReviewRequest{
			Cwd:   diffCwd,
			Scope: proto.ReviewUncommitted,
		}, &rev)
		cancel()
		if err != nil {
			h.MissingSources = append(h.MissingSources, HandoffGap{
				Source: "git_diff",
				Reason: fmt.Sprintf("could not read workspace diff at %s on %s: %s", diffCwd, diffMachine, clip(oneLine(err.Error()), 120)),
			})
		} else {
			dr := &HandoffDiffRef{
				Scope:     cmp.Or(rev.Scope, proto.ReviewUncommitted),
				BaseHash:  rev.Base.Hash,
				BaseShort: rev.Base.Short,
				BaseRef:   rev.Base.Ref,
				Root:      rev.Root,
				FileCount: len(rev.Files),
				Truncated: rev.Truncated,
			}
			if rev.Head != nil {
				dr.HeadHash = rev.Head.Hash
				dr.HeadShort = rev.Head.Short
				dr.Branch = rev.Head.Branch
				diffHeadLabel = cmp.Or(rev.Head.Short, rev.Head.Hash)
				diffBranch = rev.Head.Branch
			}
			h.DiffRef = dr
			diffTruncated = rev.Truncated
			switch {
			case rev.Base.Empty:
				diffBaseLabel = "empty tree"
			case rev.Base.Short != "" && rev.Base.Ref != "":
				diffBaseLabel = rev.Base.Short + " (" + rev.Base.Ref + ")"
			case rev.Base.Short != "":
				diffBaseLabel = rev.Base.Short + " (HEAD)"
			case rev.Base.Hash != "":
				diffBaseLabel = clip(rev.Base.Hash, 12) + " (HEAD)"
			default:
				diffBaseLabel = "HEAD"
			}
			for _, f := range rev.Files {
				line := f.Status + " " + f.Path
				if f.OldPath != "" && f.OldPath != f.Path {
					line = f.Status + " " + f.OldPath + " -> " + f.Path
				}
				switch {
				case f.Refused:
					line += " [refused]"
				case f.Binary:
					line += " [binary]"
				case f.Added > 0 || f.Deleted > 0:
					line += fmt.Sprintf(" (+%d/-%d)", f.Added, f.Deleted)
				}
				diffFiles = append(diffFiles, line)
			}
			if rev.Truncated {
				h.MissingSources = append(h.MissingSources, HandoffGap{
					Source: "git_diff",
					Reason: fmt.Sprintf("workspace diff was truncated at %d files", len(rev.Files)),
				})
			}
		}
	} else {
		h.MissingSources = append(h.MissingSources, HandoffGap{
			Source: "git_diff",
			Reason: "no workspace directory is recorded for this to-do",
		})
	}

	// Render deterministically within todoHandoffMaxRunes.
	promptRanges := make([]prompts.TodoContextSourceRange, 0, len(h.SourceRanges))
	for _, r := range h.SourceRanges {
		promptRanges = append(promptRanges, prompts.TodoContextSourceRange{
			MachineID: r.MachineID, SessionID: r.SessionID, Agent: r.Agent,
			FromSeq: r.FromSeq, ToSeq: r.ToSeq, ItemCount: r.ItemCount, Truncated: r.Truncated,
		})
	}
	promptGaps := make([]prompts.TodoContextGap, 0, len(h.MissingSources))
	for _, g := range h.MissingSources {
		promptGaps = append(promptGaps, prompts.TodoContextGap{Source: g.Source, Reason: g.Reason})
	}

	var rendered string
	for _, lvl := range todoHandoffRenderLevels {
		data := prompts.TodoContextHandoff{
			HandoffID:      h.ID,
			OperationID:    h.OperationID,
			TodoID:         h.TodoID,
			TodoTitle:      clip(oneLine(in.Todo.Title), 140),
			Strategy:       h.Strategy,
			Generator:      h.Generator,
			SupersedesID:   h.SupersedesID,
			SourceMachine:  h.SourceMachine,
			SourceSession:  h.SourceSession,
			SourceLauncher: h.SourceLauncher,
			SourceAgent:    h.SourceAgent,
			TargetMachine:  h.TargetMachine,
			TargetLauncher: h.TargetLauncher,
			TargetAgent:    h.TargetAgent,
			BriefRevision:  h.BriefRevision,
			KnownFailure:   clip(oneLine(in.KnownFailure), 240),
			Goal:           clip(oneLine(goal), 300),
			Request:        clip(strings.TrimSpace(reqText), lvl.requestCap),
			Criteria:       criteria,
			OutOfBounds:    outOfBounds,
			Decisions:      tailSlice(decisions, lvl.decisionCap),
			SourceRanges:   promptRanges,
			Files:          tailSlice(touchedFiles, lvl.fileCap),
			Commands:       tailSlice(commands, lvl.cmdCap),
			Failed:         tailSlice(failedCmds, lvl.cmdCap),
			RecentTurns:    clipTurns(tailSlice(turns, lvl.turnCount), lvl.turnCap),
			DiffScope:      proto.ReviewUncommitted,
			DiffBase:       diffBaseLabel,
			DiffHead:       diffHeadLabel,
			DiffBranch:     diffBranch,
			DiffFiles:      tailSlice(diffFiles, lvl.diffFileCap),
			DiffTruncated:  diffTruncated || len(diffFiles) > lvl.diffFileCap,
			MissingSources: promptGaps,
		}
		rendered = prompts.RenderTodoContextHandoff(data)
		if utf8.RuneCountInString(rendered) <= todoHandoffMaxRunes {
			break
		}
	}
	if utf8.RuneCountInString(rendered) > todoHandoffMaxRunes {
		const marker = "\n[truncated]\n== End of to-do context handoff; the task prompt follows =="
		keep := max(todoHandoffMaxRunes-utf8.RuneCountInString(marker), 100)
		rendered = textfmt.Cut(rendered, keep) + marker
	}
	h.Summary = rendered
	return h
}

func tailSlice[T any](s []T, maxLen int) []T {
	if len(s) <= maxLen {
		return s
	}
	return s[len(s)-maxLen:]
}

func clipTurns(turns []prompts.TodoContextTurn, maxRunes int) []prompts.TodoContextTurn {
	out := make([]prompts.TodoContextTurn, 0, len(turns))
	for _, t := range turns {
		out = append(out, prompts.TodoContextTurn{Speaker: t.Speaker, Text: clip(t.Text, maxRunes)})
	}
	return out
}

func extractTodoHandoffTranscript(items []proto.Item) (files, commands, failed []string, turns []prompts.TodoContextTurn) {
	fileSeen := map[string]bool{}
	cmdIdx := map[string]int{}
	fileTools := map[string]bool{
		"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true,
		"apply_patch": true, "str_replace_editor": true,
	}
	cmdTools := map[string]bool{
		"Bash": true, "shell": true, "exec_command": true, "local_shell": true,
	}
	for _, it := range items {
		switch {
		case it.Role == "user" && it.Kind == proto.KindText:
			txt := strings.TrimSpace(cleanTodoHandoffText(transcript.StripInjected(it.Text)))
			if txt == "" {
				continue
			}
			speaker := "user"
			if it.From != "" {
				speaker = "agent:" + clip(oneLine(it.From), 40)
			} else if prompts.ParseInjected(it.Text) != nil {
				speaker = "persona"
			}
			turns = append(turns, prompts.TodoContextTurn{Speaker: speaker, Text: txt})
		case it.Role == "assistant" && it.Kind == proto.KindText:
			txt := strings.TrimSpace(cleanTodoHandoffText(it.Text))
			if txt == "" {
				continue
			}
			turns = append(turns, prompts.TodoContextTurn{Speaker: "assistant", Text: txt})
		case it.Role == "assistant" && it.Kind == proto.KindToolUse && (fileTools[it.ToolName] || cmdTools[it.ToolName]):
			var input map[string]any
			if json.Unmarshal(it.ToolInput, &input) != nil {
				continue
			}
			if fileTools[it.ToolName] {
				for _, key := range []string{"file_path", "path", "notebook_path"} {
					if p, _ := input[key].(string); p != "" && !fileSeen[p] {
						fileSeen[p] = true
						files = append(files, clip(oneLine(p), 200))
					}
				}
				if it.ToolName == "apply_patch" {
					for _, key := range slices.Sorted(maps.Keys(input)) {
						if s, ok := input[key].(string); ok {
							for _, m := range todoHandoffPatchRe.FindAllStringSubmatch(s, -1) {
								p := strings.TrimSpace(m[1])
								if p != "" && !fileSeen[p] {
									fileSeen[p] = true
									files = append(files, clip(oneLine(p), 200))
								}
							}
						}
					}
				}
			} else if cmdTools[it.ToolName] {
				cmd := extractCommandInput(input)
				if cmd == "" {
					continue
				}
				if it.ToolUseID != "" {
					cmdIdx[it.ToolUseID] = len(commands)
				}
				commands = append(commands, clip(oneLine(cmd), 140))
			}
		case it.Kind == proto.KindToolResult && it.ToolUseID != "":
			idx, ok := cmdIdx[it.ToolUseID]
			if !ok {
				continue
			}
			isErr := it.IsError || (it.Outcome != nil && it.Outcome.Status == proto.OutcomeFailed)
			exitCode := 0
			hasExit := false
			if it.Outcome != nil && it.Outcome.ExitCode != nil {
				exitCode, hasExit = *it.Outcome.ExitCode, true
				if exitCode != 0 {
					isErr = true
				}
			} else if m := todoHandoffExitRe.FindStringSubmatch(it.Text); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					exitCode, hasExit = n, true
					if n != 0 {
						isErr = true
					}
				}
			}
			if hasExit {
				commands[idx] = fmt.Sprintf("%s (exit %d)", commands[idx], exitCode)
			} else if isErr {
				commands[idx] += " (failed)"
			}
			if isErr {
				if m := todoHandoffFailRe.FindStringSubmatch(it.Text); m != nil {
					failed = append(failed, clip(oneLine(commands[idx]+" -> FAIL "+m[1]), 180))
				} else {
					failed = append(failed, commands[idx])
				}
			}
		}
	}
	return files, commands, failed, turns
}

func cleanTodoHandoffText(s string) string {
	if strings.Contains(s, "<system-reminder") {
		s = todoHandoffReminderRe.ReplaceAllString(s, "")
	}
	if strings.Contains(s, ";base64,") {
		s = todoHandoffDataURIRe.ReplaceAllString(s, "[image]")
	}
	var b bytes.Buffer
	for _, line := range strings.Split(s, "\n") {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimRight(line, " \t\r"))
	}
	return strings.TrimSpace(b.String())
}

func extractCommandInput(input map[string]any) string {
	for _, key := range []string{"command", "cmd"} {
		switch v := input[key].(type) {
		case string:
			return v
		case []any:
			var argv []string
			for _, a := range v {
				if s, ok := a.(string); ok {
					argv = append(argv, s)
				}
			}
			if len(argv) == 3 && (argv[1] == "-lc" || argv[1] == "-c") {
				return argv[2]
			}
			return strings.Join(argv, " ")
		}
	}
	return ""
}

// ---- Owner-direct to-do handoff routes ----

type todoHandoffPostRequest struct {
	OperationID           string `json:"operation_id"`
	Machine               string `json:"machine"`
	Launcher              string `json:"launcher"`
	Model                 string `json:"model,omitempty"`
	Effort                string `json:"effort,omitempty"`
	Prompt                string `json:"prompt,omitempty"`
	Permission            string `json:"permission,omitempty"`
	Preview               bool   `json:"preview,omitempty"`
	Regenerate            bool   `json:"regenerate,omitempty"`
	DeferOnMissingSources bool   `json:"defer_on_missing_sources,omitempty"`
}

func (s *Service) todoHandoffRoutes() {
	// GET /api/v1/todos/{id}/handoffs: inspect all persisted context handoff artifacts for a to-do.
	// Full owner credentials only; extensions cannot read predecessor transcript/diff handoffs.
	s.handle("GET /api/v1/todos/{id}/handoffs", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("full credentials required to inspect to-do handoffs"))
			return
		}
		id := r.PathValue("id")
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		if _, ok := s.todoNow(id); !ok {
			apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found"))
			return
		}
		list, err := s.listTodoContextHandoffs(r.Context(), id)
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		if list == nil {
			list = []TodoContextHandoff{}
		}
		apierr.WriteJSON(w, map[string]any{"handoffs": list})
	})

	// POST /api/v1/todos/{id}/handoff: owner-approved continuation of a to-do onto another provider or
	// launcher without requiring a response from the previous model.
	s.handle("POST /api/v1/todos/{id}/handoff", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("full credentials required to hand off a to-do"))
			return
		}
		var req todoHandoffPostRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		res, status, code, err := s.executeOwnerTodoHandoff(ownerStart(r.Context()), r.PathValue("id"), req)
		if err != nil {
			if res != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":   err.Error(),
					"code":    code,
					"handoff": res,
				})
				return
			}
			if code != "" {
				apierr.WriteCode(w, status, code, err)
				return
			}
			apierr.Write(w, status, err)
			return
		}
		if status != 0 && status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			payload := map[string]any{"handoff": res}
			if code != "" {
				payload["code"] = code
			}
			_ = json.NewEncoder(w).Encode(payload)
			return
		}
		apierr.WriteJSON(w, map[string]any{"handoff": res})
	})
}

type ownerTodoHandoffPlan struct {
	todo          Todo
	pred          todoPredecessorInfo
	machine       Device
	targetAgent   string
	targetCwd     string
	effectivePerm string
	opID          string
	buildIn       todoHandoffBuildInput
}

func (s *Service) buildOwnerHandoffInput(ctx context.Context, todo Todo, pred todoPredecessorInfo, m Device, launcher, targetAgent, targetCwd, opID string) todoHandoffBuildInput {
	var supersedesID string
	if prev := s.latestTodoContextHandoff(ctx, todo.ID); prev != nil {
		supersedesID = prev.ID
	}
	return todoHandoffBuildInput{
		Todo:           todo,
		SourceMachine:  pred.agent.MachineID,
		SourceSession:  pred.agent.SessionID,
		SourceLauncher: pred.launcher(),
		SourceAgent:    pred.agentKind(),
		SourceCwd:      pred.cwd(),
		KnownFailure:   pred.session.TurnError,
		TargetMachine:  m.ID,
		TargetLauncher: launcher,
		TargetAgent:    targetAgent,
		TargetCwd:      targetCwd,
		OperationID:    opID,
		SupersedesID:   supersedesID,
	}
}

func (s *Service) prepareOwnerTodoHandoff(ctx context.Context, todo Todo, req todoHandoffPostRequest) (ownerTodoHandoffPlan, int, string, error) {
	pred, hasPred := s.todoPredecessor(ctx, todo)
	targetMachine := req.Machine
	if targetMachine == "" && hasPred {
		targetMachine = pred.agent.MachineID
	}
	m, ok := s.machineByName(ctx, targetMachine)
	if !ok {
		return ownerTodoHandoffPlan{}, http.StatusBadRequest, "unknown_machine", fmt.Errorf("unknown machine %q", targetMachine)
	}
	if !m.Reachable {
		return ownerTodoHandoffPlan{}, http.StatusConflict, "machine_offline", machineOffline(m.Name)
	}
	if err := s.launcherScopeErr(ctx, m.ID, req.Launcher); err != nil {
		return ownerTodoHandoffPlan{}, http.StatusConflict, "launcher_out_of_scope", err
	}

	// Safety refusal, unresolved approval, and permission-escalation checks run before anything touches the predecessor.
	// When the predecessor's permission mode cannot be established, default conservatively to "default" so
	// target launcher defaults (e.g. Auto Accept) never silently escalate permissions.
	srcPerm := cmp.Or(pred.permission(), "default")
	effectivePerm := req.Permission
	if effectivePerm == "" {
		effectivePerm = srcPerm
	}
	if err := s.checkContinuationBlockers(ctx, todo, effectivePerm, effectivePerm == "plan", true); err != nil {
		var be handoffBlockerError
		code := "handoff_blocked"
		if errors.As(err, &be) {
			code = be.code
		}
		return ownerTodoHandoffPlan{}, http.StatusConflict, code, err
	}

	targetCwd := pred.cwd()
	if targetCwd == "" {
		return ownerTodoHandoffPlan{}, http.StatusConflict, "missing_workspace", fmt.Errorf("to-do %s has no workspace directory to continue in", todo.ID)
	}
	if assistantWorkspace(m, targetCwd) {
		return ownerTodoHandoffPlan{}, http.StatusBadRequest, "invalid_workspace", i18n.Err("rpc-errors.agent_assistant_workspace")
	}

	// Verify target launcher existence, readonly compatibility, requested modes/effort, and health before
	// stopping or unlinking the predecessor.
	fitParams := map[string]any{
		"launcher": req.Launcher,
		"model":    req.Model,
		"effort":   req.Effort,
	}
	own, err := s.resolveStart(ctx, fitParams, false)
	if err != nil {
		return ownerTodoHandoffPlan{}, http.StatusBadRequest, "invalid_launcher", err
	}
	_, fit, err := s.fitNewLauncher(ctx, m, own, fitParams)
	if err != nil {
		return ownerTodoHandoffPlan{}, http.StatusConflict, "launcher_unavailable", err
	}
	targetAgent := cmp.Or(fit.agent, s.launcherAgent(ctx, m.ID, req.Launcher))
	if effectivePerm == "plan" {
		if err := s.readonlyLauncherRefusal(ctx, m, fitParams, fit); err != nil {
			return ownerTodoHandoffPlan{}, http.StatusConflict, "readonly_unsupported", err
		}
	}
	if err := startModesRefused(req.Launcher, targetAgent, req.Permission, req.Effort); err != nil {
		return ownerTodoHandoffPlan{}, http.StatusBadRequest, "unsupported_launch_modes", err
	}
	if r, bad := s.launcherUnusableNow(ctx, m.ID, req.Launcher); bad {
		return ownerTodoHandoffPlan{}, http.StatusConflict, "launcher_unhealthy", i18n.Err("assistant-tasks.fixed_launcher_unhealthy", i18n.Args{
			"machine": m.ID, "launcher": req.Launcher, "reason": r.Error,
		})
	}

	opID := req.OperationID
	if opID == "" {
		opID = "op_" + randomHex(12)
	}
	buildIn := s.buildOwnerHandoffInput(ctx, todo, pred, m, req.Launcher, targetAgent, targetCwd, opID)
	return ownerTodoHandoffPlan{
		todo:          todo,
		pred:          pred,
		machine:       m,
		targetAgent:   targetAgent,
		targetCwd:     targetCwd,
		effectivePerm: effectivePerm,
		opID:          opID,
		buildIn:       buildIn,
	}, 0, "", nil
}

func (s *Service) executeOwnerTodoHandoff(ctx context.Context, todoID string, req todoHandoffPostRequest) (*TodoContextHandoff, int, string, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return nil, http.StatusServiceUnavailable, "todos_unavailable", errTodosUnavailable
	}
	todo, ok := s.todoNow(todoID)
	if !ok {
		return nil, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found")
	}
	req.OperationID = strings.TrimSpace(req.OperationID)
	req.Machine = strings.TrimSpace(req.Machine)
	req.Launcher = strings.TrimSpace(req.Launcher)
	req.Model = strings.TrimSpace(req.Model)
	req.Effort = strings.TrimSpace(req.Effort)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Permission = strings.TrimSpace(req.Permission)
	if req.Launcher == "" {
		return nil, http.StatusBadRequest, "invalid_request", errors.New("launcher is required")
	}
	if req.Model != "" && !proto.ValidModelID(req.Model) {
		return nil, http.StatusBadRequest, "invalid_model", fmt.Errorf("invalid model %q", req.Model)
	}
	if len(req.OperationID) > 128 {
		return nil, http.StatusBadRequest, "invalid_operation_id", errors.New("operation_id must be at most 128 characters")
	}

	if req.Preview {
		prep, status, code, err := s.prepareOwnerTodoHandoff(ctx, todo, req)
		if err != nil {
			return nil, status, code, err
		}
		preview := s.buildTodoContextHandoff(ctx, prep.buildIn)
		return &preview, http.StatusOK, "", nil
	}

	// Idempotent replay check before mutating state: retrying an accepted, pending, or unknown
	// operation never starts a second worker or injects context twice.
	if req.OperationID != "" {
		existing, err := s.getTodoContextHandoffByOp(ctx, todoID, req.OperationID)
		if err != nil {
			return nil, http.StatusServiceUnavailable, "handoff_unavailable", err
		}
		if existing != nil {
			return existing, http.StatusOK, "", nil
		}
	}

	// Single-flight claim on this to-do: racing requests cannot start two active workers.
	s.tasks.mu.Lock()
	if s.tasks.reusing[todoID] {
		s.tasks.mu.Unlock()
		return nil, http.StatusConflict, "handoff_in_progress", fmt.Errorf("another continuation or handoff is already in progress on to-do %s", todoID)
	}
	st, claimed := s.claimTaskStartLocked(todoID)
	if !claimed {
		s.tasks.mu.Unlock()
		return nil, http.StatusConflict, "handoff_in_progress", fmt.Errorf("to-do %s already has a start in progress", todoID)
	}
	s.tasks.reusing[todoID], s.tasks.taking[todoID] = true, st
	s.tasks.mu.Unlock()
	defer s.releaseTakenTodo(todoID)

	// Re-check operation idempotency under the single-flight claim.
	if req.OperationID != "" {
		if existing, err := s.getTodoContextHandoffByOp(ctx, todoID, req.OperationID); err != nil {
			return nil, http.StatusServiceUnavailable, "handoff_unavailable", err
		} else if existing != nil {
			return existing, http.StatusOK, "", nil
		}
	}

	// Reload todo and resolve predecessor, blockers, target launcher/modes/health, and build input under the claim.
	todo, ok = s.todoNow(todoID)
	if !ok {
		return nil, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found")
	}
	prep, status, code, err := s.prepareOwnerTodoHandoff(ctx, todo, req)
	if err != nil {
		return nil, status, code, err
	}

	// Optional pre-check for missing sources so the owner can defer before stopping the predecessor.
	if req.DeferOnMissingSources {
		draft := s.buildTodoContextHandoff(ctx, prep.buildIn)
		if len(draft.MissingSources) > 0 {
			return &draft, http.StatusConflict, "missing_sources", fmt.Errorf("handoff deferred: %d source(s) missing or truncated", len(draft.MissingSources))
		}
	}

	// Preserve an existing supervisor. When automatic Agent supervision is enabled, an untracked to-do
	// gets the same owner its other Agent work has; a reporting-only origin keeps its quiet report path.
	if s.agentTasks(ctx) {
		s.ensureSupervisor(ctx, todo.ID)
	}

	// Ensure predecessor safely relinquishes the workspace before starting the replacement.
	stopped, err := s.stopContinuedTaskAgents(ctx, todo)
	if err != nil {
		var be handoffBlockerError
		code := "workspace_not_quiescent"
		if errors.As(err, &be) {
			code = be.code
		}
		return nil, http.StatusConflict, code, err
	}

	// Refresh todo, predecessor session snapshot, effective permission, and supersedes metadata after
	// quiescence so the persisted handoff reflects final predecessor state and failure details.
	if latestTodo, ok := s.todoNow(todoID); ok {
		todo = latestTodo
	}
	pred := prep.pred
	effectivePerm := prep.effectivePerm
	if freshPred, ok := s.todoPredecessor(ctx, todo); ok {
		pred = freshPred
		if req.Permission == "" {
			effectivePerm = cmp.Or(pred.permission(), "default")
		}
	}
	m := prep.machine
	targetAgent := prep.targetAgent
	targetCwd := prep.targetCwd
	buildIn := s.buildOwnerHandoffInput(ctx, todo, pred, m, req.Launcher, targetAgent, targetCwd, prep.opID)

	// Build and persist the canonical auditable handoff after predecessor quiescence.
	handoff := s.buildTodoContextHandoff(ctx, buildIn)
	saved, created, err := s.persistTodoContextHandoff(ctx, handoff)
	if err != nil {
		return nil, http.StatusServiceUnavailable, "handoff_persist_failed", err
	}
	if !created {
		return &saved, http.StatusOK, "", nil
	}

	taskPrompt := req.Prompt
	if taskPrompt == "" {
		taskPrompt = fmt.Sprintf("Continue to-do %s (%s) from the recorded handoff context above.", todo.ID, todo.Title)
	}
	pt := PlanTask{
		Title:              todo.Title,
		Prompt:             taskPrompt,
		Machine:            m.ID,
		MachinePolicy:      machineFixed,
		Cwd:                targetCwd,
		Launcher:           req.Launcher,
		Model:              req.Model,
		Effort:             req.Effort,
		Permission:         effectivePerm,
		Readonly:           effectivePerm == "plan",
		Continues:          todo.ID,
		ReuseTodo:          todo.ID,
		TodoID:             todo.ID,
		OwnerStarted:       true,
		HandoffID:          saved.ID,
		HandoffOperationID: saved.OperationID,
		preservePermission: true,
	}
	if brief, ok := s.briefOfTodo(todo.ID); ok {
		pt.DoneWhen = brief.Criteria
		pt.Brief = &brief
	}

	startCtx, startCancel := context.WithTimeout(ctx, taskStartTimeout)
	mid, sid, startErr := s.startTaskAgent(startCtx, &pt, false)
	startCancel()

	if startErr != nil {
		bookCtx, bookCancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
		defer bookCancel()
		if startResultUnknown(startErr) || nodeMayHaveTaken(startErr) {
			updated, recErr := s.recordTodoHandoffDelivery(bookCtx, saved.ID, m.ID, "", targetAgent, handoffDeliveryUnknown, startErr.Error())
			if recErr != nil {
				s.logf("todo %s: record handoff %s delivery_unknown: %v", todo.ID, saved.ID, recErr)
				return &saved, http.StatusServiceUnavailable, "handoff_delivery_record_failed", fmt.Errorf("target startup outcome unknown (%v) and recording delivery state failed: %w", startErr, recErr)
			}
			return &updated, http.StatusAccepted, "delivery_unknown", nil
		}
		updated, recErr := s.recordTodoHandoffDelivery(bookCtx, saved.ID, m.ID, "", targetAgent, handoffDeliveryRejected, startErr.Error())
		if recErr != nil {
			s.logf("todo %s: record handoff %s delivery_rejected: %v", todo.ID, saved.ID, recErr)
			return &saved, http.StatusServiceUnavailable, "handoff_delivery_record_failed", fmt.Errorf("target startup rejected (%v) and recording delivery state failed: %w", startErr, recErr)
		}
		// Definite target startup rejection happens before releasing predecessor links or opening a new
		// supervision round, so recoverable task state stays intact for choosing another target.
		return &updated, http.StatusConflict, "target_start_rejected", startErr
	}

	// Only release predecessor links, reopen, and open a new supervision round once the replacement Agent
	// has actually started, using a detached bounded context so client disconnects cannot orphan the task.
	linkCtx, linkCancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
	defer linkCancel()
	if err := s.releaseStoppedAgents(linkCtx, todo.ID, stopped); err != nil {
		s.logf("todo %s: release stopped predecessor agents after starting %s/%s: %v", todo.ID, mid, short(sid), err)
		detail := fmt.Sprintf("replacement agent %s/%s started but releasing predecessor links failed: %v", mid, sid, err)
		if updated, recErr := s.recordTodoHandoffDelivery(linkCtx, saved.ID, mid, sid, cmp.Or(pt.startedAgent, targetAgent), handoffDeliveryUnknown, detail); recErr == nil {
			return &updated, http.StatusServiceUnavailable, "release_failed", err
		}
		return &saved, http.StatusServiceUnavailable, "release_failed", err
	}
	if todo.DoneAt != nil {
		if err := s.reopenTodo(linkCtx, todo.ID); err != nil {
			s.logf("todo %s: reopen after starting %s/%s: %v", todo.ID, mid, short(sid), err)
			detail := fmt.Sprintf("replacement agent %s/%s started but reopening to-do failed: %v", mid, sid, err)
			if updated, recErr := s.recordTodoHandoffDelivery(linkCtx, saved.ID, mid, sid, cmp.Or(pt.startedAgent, targetAgent), handoffDeliveryUnknown, detail); recErr == nil {
				return &updated, http.StatusServiceUnavailable, "reopen_failed", err
			}
			return &saved, http.StatusServiceUnavailable, "reopen_failed", err
		}
	}
	s.startSupervisionRound(linkCtx, todo.ID)

	var linkErr error
	for attempt := 0; attempt < 2; attempt++ {
		if linkErr = s.linkPlanAgent(linkCtx, pt, mid, sid, false); linkErr == nil {
			break
		}
	}
	if linkErr != nil {
		s.logf("todo %s: link handoff agent %s/%s: %v", todo.ID, mid, short(sid), linkErr)
		detail := fmt.Sprintf("replacement agent %s/%s started but linking to to-do failed: %v", mid, sid, linkErr)
		if updated, recErr := s.recordTodoHandoffDelivery(linkCtx, saved.ID, mid, sid, cmp.Or(pt.startedAgent, targetAgent), handoffDeliveryUnknown, detail); recErr == nil {
			return &updated, http.StatusServiceUnavailable, "link_failed", linkErr
		}
		return &saved, http.StatusServiceUnavailable, "link_failed", linkErr
	}
	updated, err := s.recordTodoHandoffDelivery(linkCtx, saved.ID, mid, sid, cmp.Or(pt.startedAgent, targetAgent), handoffDeliveryAccepted, "")
	if err != nil {
		s.logf("todo %s: record handoff %s accepted: %v", todo.ID, saved.ID, err)
		return &saved, http.StatusServiceUnavailable, "handoff_delivery_record_failed", err
	}
	return &updated, http.StatusOK, "", nil
}

// prepareContinuationHandoff builds and persists a handoff for a cross-launcher/provider task_plan
// continuation once the owner approves Start. Returns the handoff ID ("" if not applicable).
func (s *Service) prepareContinuationHandoff(ctx context.Context, t *PlanTask, todo Todo, pred todoPredecessorInfo, hasPred bool) error {
	if t.Continues == "" || !hasPred {
		return nil
	}
	if !s.isProviderSwitchContinuation(ctx, todo, t.Machine, t.Launcher) && t.HandoffOperationID == "" {
		return nil
	}
	opID := cmp.Or(t.HandoffOperationID, "op_"+randomHex(12))
	t.HandoffOperationID = opID
	if existing, err := s.getTodoContextHandoffByOp(ctx, todo.ID, opID); err == nil && existing != nil {
		t.HandoffID = existing.ID
		return nil
	}
	var supersedesID string
	if prev := s.latestTodoContextHandoff(ctx, todo.ID); prev != nil {
		supersedesID = prev.ID
	}
	targetLauncher := strings.TrimSpace(t.Launcher)
	if targetLauncher == "" && t.Tier != "" {
		preview := *t
		if s.applyTaskTier(ctx, &preview) == nil && preview.Launcher != "" {
			targetLauncher = preview.Launcher
		}
	}
	targetLauncher = s.startLauncher(ctx, targetLauncher)
	targetAgent := s.launcherAgent(ctx, t.Machine, targetLauncher)
	h := s.buildTodoContextHandoff(ctx, todoHandoffBuildInput{
		Todo:           todo,
		SourceMachine:  pred.agent.MachineID,
		SourceSession:  pred.agent.SessionID,
		SourceLauncher: pred.launcher(),
		SourceAgent:    pred.agentKind(),
		SourceCwd:      pred.cwd(),
		KnownFailure:   pred.session.TurnError,
		TargetMachine:  t.Machine,
		TargetLauncher: targetLauncher,
		TargetAgent:    targetAgent,
		TargetCwd:      taskRunDir(*t),
		OperationID:    opID,
		SupersedesID:   supersedesID,
	})
	saved, _, err := s.persistTodoContextHandoff(ctx, h)
	if err != nil {
		return err
	}
	t.HandoffID = saved.ID
	return nil
}

// todoHandoffSummary returns the persisted summary text for handoffID, or "" if unavailable.
func (s *Service) todoHandoffSummary(ctx context.Context, handoffID string) string {
	if handoffID == "" {
		return ""
	}
	h, err := s.getTodoContextHandoff(ctx, handoffID)
	if err != nil || h == nil {
		return ""
	}
	return h.Summary
}
