package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

const conversationSegmentsKey = "assistant.segments"

type logicalSwitchRequest struct {
	OperationID        string `json:"operation_id"`
	ExpectedEpoch      int64  `json:"expected_epoch"`
	MachineID          string `json:"machine_id"`
	Launcher           string `json:"launcher"`
	Model              string `json:"model,omitempty"`
	Effort             string `json:"effort,omitempty"`
	PermissionMode     string `json:"permission_mode,omitempty"`
	AllowOfflineSource bool   `json:"allow_offline_source,omitempty"`
}
type logicalInput struct {
	Method    string               `json:"method"`
	Params    map[string]any       `json:"params"`
	Transport *devicewire.NodeCall `json:"transport,omitempty"`
}
type segmentTarget struct {
	Registered        bool   `json:"registered"`
	Engine            string `json:"engine"`
	SupervisionPolicy bool   `json:"supervision_policy"`
	Prepare           bool   `json:"prepare"`
}

// The node resolves a registered native/runtime execution path. An empty or
// unrecognized path cannot be made valid by guessing from a launcher name.
func knownSegmentTarget(target segmentTarget) bool {
	return target.Registered && target.Prepare && proto.AssistantEngineKind(target.Engine) != ""
}
func (s *Service) logicalTarget(ctx context.Context, machine, launcher string) (segmentTarget, error) {
	var target segmentTarget
	if err := s.launcherScopeErr(ctx, machine, launcher); err != nil {
		return target, err
	}
	raw, err := s.logicalPhysical(ctx, machine, "assistant.segment.check", map[string]any{"launcher": launcher})
	if err != nil {
		return target, err
	}
	if json.Unmarshal(raw, &target) != nil || !knownSegmentTarget(target) {
		return target, logicalRefusal(ctx, "unknown_engine")
	}
	return target, nil
}

// sessionEngineAgent is the agent a session row runs: an empty agent outside a runtime is Claude Code, as the node
// lists a native Claude Code session without one (proto.Session.Agent). A runtime's empty agent stays unknown.
func sessionEngineAgent(row proto.Session) string {
	if row.Agent == "" && row.SourceKind != proto.SourceRuntime {
		return "claude"
	}
	return row.Agent
}
func (s *Service) segmentsEnabled(ctx context.Context) bool {
	if s.store() == nil {
		return false
	}
	var v struct {
		Enabled *bool `json:"enabled"`
	}
	_, err := s.store().Setting(ctx, conversationSegmentsKey, &v)
	return err == nil && (v.Enabled == nil || *v.Enabled)
}
func (s *Service) logicalSwitchRoutes() {
	s.logicalDeliveryRoutes()
	s.handle("GET /api/v1/assistant/segments", func(w http.ResponseWriter, r *http.Request) {
		apierr.WriteJSON(w, map[string]any{"enabled": s.segmentsEnabled(r.Context()), "feature": "assistant_segments", "routing": true})
	})
	s.handle("PUT /api/v1/assistant/segments", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, 403, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		var p struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&p); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		if s.store() == nil {
			apierr.Call(w, logicalRefusal(r.Context(), "unavailable"))
			return
		}
		if err := s.store().PutSetting(r.Context(), conversationSegmentsKey, p); err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, p)
	})
	s.handle("GET /api/v1/assistant/conversations/{id}/segments", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, 403, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		ctx := r.Context()
		machine, id := r.URL.Query().Get("machine_id"), r.PathValue("id")
		if machine == "" || !s.keepsPickOn(ctx, id, machine) {
			apierr.Call(w, logicalRefusal(ctx, "unavailable"))
			return
		}
		st, ok := s.store().(logicalSwitchStore)
		if !ok {
			apierr.Call(w, logicalRefusal(ctx, "unavailable"))
			return
		}
		c, err := s.physicalLogicalConversation(ctx, st, machine, id)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		epoch := int64(1)
		var operation any
		if c != nil {
			if c.Deleted || c.MachineID != machine || c.SessionID != id {
				apierr.Call(w, logicalRefusal(ctx, "private_segment"))
				return
			}
			epoch = c.Epoch
			if c.Switch != nil {
				operation = map[string]any{"operation_id": c.Switch.OperationID, "phase": c.Switch.Phase, "error": c.Switch.Error}
			}
		}
		role, err := s.logicalRoleRequired(ctx, machine, id)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"enabled": s.segmentsEnabled(ctx), "epoch": epoch, "switch": operation, "supervision_required": role, "blocked_input": s.logicalBlockedInput(ctx, st, c)})
	})
	s.handle("POST /api/v1/assistant/conversations/{id}/switch", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, 403, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		var p struct {
			logicalSwitchRequest
			SourceMachine string `json:"source_machine_id"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		p.AllowOfflineSource = false
		op, err := s.beginLogicalSwitch(r.Context(), p.SourceMachine, r.PathValue("id"), p.logicalSwitchRequest)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, op)
	})
	s.handle("GET /api/v1/assistant/conversations/{id}/switch/{operation}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, 403, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		st, ok := s.store().(logicalSwitchStore)
		if !ok {
			apierr.Call(w, logicalRefusal(r.Context(), "unavailable"))
			return
		}
		op, err := st.ConversationSwitch(r.Context(), r.URL.Query().Get("machine_id"), r.PathValue("id"), r.PathValue("operation"))
		if err != nil {
			apierr.Call(w, err)
			return
		}
		if op == nil {
			http.NotFound(w, r)
			return
		}
		c, err := st.LogicalConversation(r.Context(), r.URL.Query().Get("machine_id"), r.PathValue("id"))
		if err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"operation_id": op.OperationID, "phase": op.Phase, "expected_epoch": op.ExpectedEpoch, "error": op.Error, "blocked_input": s.logicalBlockedInput(r.Context(), st, c)})
	})
}

func (s *Service) logicalRoleRequired(ctx context.Context, machine, id string) (bool, error) {
	policy, err := s.supervisionInstructions(ctx, machine, id)
	if err != nil {
		return false, err
	}
	cfg := s.settings(ctx)
	return policy != "" || slices.Contains(cfg.Leaders, id) || id == cfg.BackgroundSupervisor || id == cfg.Main && cfg.MainSupervisesOwnTasks != nil && *cfg.MainSupervisesOwnTasks, nil
}

func (s *Service) conversationSwitchBusy(machine, id string, row proto.Session) bool {
	if row.Starting || row.Status == proto.StatusWorking || row.Status == proto.StatusBlocked || row.PendingInteractions > 0 ||
		backgroundRunning(row) || len(row.Queued) > 0 || (row.Compaction != nil && row.Compaction.State == proto.CompactionRunning) {
		return true
	}
	for _, ix := range s.pendingInteractions() {
		if ix.SessionID == id && ix.MachineID == machine {
			return true
		}
	}
	return false
}

func (s *Service) beginLogicalSwitch(ctx context.Context, machine, id string, p logicalSwitchRequest) (any, error) {
	st, ok := s.store().(logicalSwitchStore)
	if !ok {
		return nil, logicalRefusal(ctx, "unavailable")
	}
	if !s.segmentsEnabled(ctx) {
		return nil, logicalRefusal(ctx, "disabled")
	}
	if machine == "" || id == "" || p.MachineID == "" || p.Launcher == "" || p.OperationID == "" || len(p.OperationID) > 128 || p.ExpectedEpoch < 1 {
		return nil, logicalRefusal(ctx, "unavailable")
	}
	request, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	// Replay is checked before current status: accepted operations remain idempotent.
	old, err := st.ConversationSwitch(ctx, machine, id, p.OperationID)
	if err != nil {
		return nil, err
	}
	if old != nil {
		if string(old.Request) != string(request) {
			return nil, db.ErrConversationEpoch
		}
		return map[string]any{"operation_id": old.OperationID, "phase": old.Phase}, nil
	}
	c, err := s.physicalLogicalConversation(ctx, st, machine, id)
	if err != nil {
		return nil, err
	}
	row, known := s.session(ctx, machine, id)
	offlineSource := p.AllowOfflineSource && s.machinePhysicallyOffline(ctx, machine)
	if !known {
		if !offlineSource || !s.isFailoverEligibleOffline(ctx, machine, id) || s.isWorkMachine(p.MachineID) {
			return nil, logicalRefusal(ctx, "unavailable")
		}
	} else {
		if !row.Assistant || row.PaneID != "" || s.isWorkMachine(p.MachineID) {
			return nil, logicalRefusal(ctx, "unavailable")
		}
		if !offlineSource && s.conversationSwitchBusy(machine, id, row) {
			return nil, logicalRefusal(ctx, "busy")
		}
	}
	target, err := s.logicalTarget(ctx, p.MachineID, p.Launcher)
	if err != nil {
		return nil, err
	}
	role, err := s.logicalRoleRequired(ctx, machine, id)
	if err != nil {
		return nil, err
	}
	if role && !target.SupervisionPolicy {
		return nil, s.supervisionRuntimeRefusal(ctx, p.MachineID)
	}
	if c == nil {
		if p.ExpectedEpoch != 1 {
			return nil, logicalRefusal(ctx, "unknown_engine")
		}
		var historyGen string
		var sourceEngine string
		if known && assistantKind(sessionEngineAgent(row)) != "" {
			sourceEngine = proto.AssistantExecutionIdentity(sessionEngineAgent(row), row.SourceKind, row.SourceInstanceID, row.Launcher)
		}
		if !offlineSource {
			if assistantKind(sessionEngineAgent(row)) == "" {
				return nil, logicalRefusal(ctx, "unknown_engine")
			}
			var page historyPage
			if err = s.physicalHistoryCall(ctx, machine, DeviceSessionItems, map[string]any{"session_id": id, "limit": 1, "lazy_images": true}, &page); err != nil {
				return nil, err
			}
			historyGen = page.History
		} else {
			// Source machine is offline: read persisted ConversationHistory from Postgres.
			var page historyPage
			if err = s.physicalHistoryCall(ctx, machine, DeviceSessionItems, map[string]any{"session_id": id, "limit": 1, "lazy_images": true}, &page); err == nil && page.History != "" {
				historyGen = page.History
			} else if hs, ok := s.store().(conversationHistoryStore); ok {
				if h, herr := hs.ConversationHistory(ctx, machine, id); herr == nil && h != nil && h.History != "" {
					historyGen = h.History
				}
			}
			if sourceEngine == "" {
				cfg := s.settings(ctx)
				agent := s.launcherAgent(ctx, machine, cfg.Launcher)
				if assistantKind(agent) == "" {
					agent = "claude"
				}
				sourceEngine = proto.AssistantExecutionIdentity(agent, "", "", cfg.Launcher)
			}
		}
		if historyGen == "" {
			return nil, logicalRefusal(ctx, "missing_generation")
		}
		if sourceEngine == "" {
			return nil, logicalRefusal(ctx, "unknown_engine")
		}
		if known && !row.LogicalPlaceholder && s.isAssistantRow(row) {
			s.noteConversationSpaces(ctx, []db.ConversationSpace{{MachineID: machine, SessionID: id, Space: runSpace(row)}})
		}
		if err = st.AdoptConversationAliases(ctx, machine, id, randomHex(16), sourceEngine, historyGen, s.physicalAliases(machine)); err != nil {
			return nil, err
		}
		c, err = st.LogicalConversation(ctx, machine, id)
		if err != nil {
			return nil, err
		}
	}
	if c == nil || c.Deleted || c.MachineID != machine || c.SessionID != id {
		return nil, logicalRefusal(ctx, "private_segment")
	}
	a, ok := c.Active()
	if !ok {
		return nil, logicalRefusal(ctx, "unavailable")
	}
	if err = st.BeginConversationSwitch(ctx, machine, id, db.ConversationSwitch{OperationID: p.OperationID, ExpectedEpoch: p.ExpectedEpoch, SourceSegment: a.ID, Request: request}); err != nil {
		if errors.Is(err, db.ErrConversationSwitchBusy) {
			return nil, logicalRefusal(ctx, "busy")
		}
		return nil, err
	}
	s.logicalChanged()
	s.historyWake()
	return map[string]any{"operation_id": p.OperationID, "phase": "preparing", "epoch": c.Epoch}, nil
}

// Keep room in the eight-connection pool for foreground traffic and node callbacks.
const logicalSweepAdvanceLimit = 4

// The loop is independent of the switch-enable setting: disabling new switches
// must not strand existing routing, queued messages or deferred closures.
func (s *Service) logicalSweep(ctx context.Context) {
	st, ok := s.store().(logicalSwitchStore)
	if !ok {
		return
	}
	rows, err := st.LogicalConversations(ctx)
	if err != nil {
		return
	}
	candidates, err := st.ConversationPreparedCandidates(ctx)
	if err != nil {
		return
	}
	for _, candidate := range candidates {
		c, err := st.LogicalConversation(ctx, candidate.LogicalMachine, candidate.LogicalSession)
		if err != nil {
			continue
		}
		adopted := false
		if c != nil {
			for _, seg := range c.Segments {
				if seg.MachineID == candidate.MachineID && seg.SessionID == candidate.SessionID {
					adopted = true
					break
				}
			}
		}
		if adopted {
			_ = st.ConversationPreparedCandidateDone(ctx, candidate.MachineID, candidate.SessionID)
			continue
		}
		if c != nil && c.Switch != nil && c.Switch.OperationID == candidate.OperationID {
			continue
		}
		closeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err = s.logicalPhysical(closeCtx, candidate.MachineID, "session.close", map[string]any{"session_id": candidate.SessionID})
		cancel()
		if err == nil {
			_ = st.ConversationPreparedCandidateDone(ctx, candidate.MachineID, candidate.SessionID)
		}
	}
	s.logical.mu.Lock()
	after := s.logical.sweepAfter
	s.logical.mu.Unlock()
	// Resume after the last admitted conversation so repeatedly failing switches
	// in the first rows cannot keep later conversations from getting a turn.
	start := slices.IndexFunc(rows, func(c db.LogicalConversation) bool {
		return c.MachineID == after[0] && c.SessionID == after[1]
	}) + 1
	for i := range rows {
		c := rows[(start+i)%len(rows)]
		pending, err := st.ConversationInputPending(ctx, c.MachineID, c.SessionID)
		if err != nil {
			continue
		}
		closing := false
		for _, seg := range c.Segments {
			closing = closing || seg.ClosePending
		}
		if c.Switch == nil && !pending && !closing {
			continue
		}
		key := [2]string{c.MachineID, c.SessionID}
		s.logical.mu.Lock()
		if len(s.logical.running) >= logicalSweepAdvanceLimit {
			s.logical.mu.Unlock()
			break
		}
		if s.logical.running == nil {
			s.logical.running = map[[2]string]bool{}
		}
		busy := s.logical.running[key]
		if !busy {
			s.logical.running[key] = true
			s.logical.sweepAfter = key
		}
		s.logical.mu.Unlock()
		if busy {
			continue
		}
		go func(c db.LogicalConversation) {
			defer func() { s.logical.mu.Lock(); delete(s.logical.running, key); s.logical.mu.Unlock() }()
			run, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			if s.expireLogicalSwitch(run, st, &c, time.Now()) {
				return
			}
			if err := s.advanceLogicalConversation(run, st, &c); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, db.ErrConversationEpoch) {
				s.logf("assistant: segment operation awaits reconciliation: %v", err)
				if op := c.Switch; op != nil {
					_ = st.NoteConversationSwitchError(ctx, c.MachineID, c.SessionID, op.OperationID, logicalSwitchErrorText(err))
				}
			}
		}(c)
	}
	closures, err := st.ConversationClosures(ctx)
	if err != nil {
		return
	}
	for _, key := range closures {
		closeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err = s.logicalPhysical(closeCtx, key[0], "session.close", map[string]any{"session_id": key[1]})
		cancel()
		if err == nil {
			_ = st.ConversationClosureDone(ctx, key[0], key[1])
		}
	}
}

// logicalSwitchPrepareLimit bounds a switch that has not activated its target yet: past it, counted from the
// switch's start and so kept across retries and restarts, the switch is rejected and its conversation stays on
// its source (#15317). A variable so tests can shorten it.
var logicalSwitchPrepareLimit = 30 * time.Minute

// logicalSwitchErrorLimit caps the error kept on a switch, which a batch item may show as its reason.
const logicalSwitchErrorLimit = 512

func logicalSwitchErrorText(err error) string {
	text := err.Error()
	if len(text) > logicalSwitchErrorLimit {
		text = strings.ToValidUTF8(text[:logicalSwitchErrorLimit], "")
	}
	return text
}

// logicalSwitchExpiredReason says what a switch that ran out of time was still waiting for, with its latest error.
func logicalSwitchExpiredReason(op *db.ConversationSwitch) string {
	limit := logicalSwitchPrepareLimit.Round(time.Second).String()
	var reason string
	switch op.Phase {
	case "in_doubt":
		reason = "the new conversation's preparation could not be confirmed within " + limit
	case "prepared":
		reason = "the prepared conversation could not take over within " + limit
	default:
		reason = "the new conversation was not ready within " + limit
	}
	if op.Error != "" {
		reason += "; last error: " + op.Error
	}
	return reason + "; the conversation kept its original settings"
}

// logicalSourceMayBeRetired: c's active segment may carry the node's retirement mark, which a send must lift first.
// An activation retires its source before switching, so any switched conversation may; so may one whose prepared
// switch was cancelled after that retirement (its abandoned target is a second segment at the first epoch).
func logicalSourceMayBeRetired(c *db.LogicalConversation) bool {
	return c.Epoch > 1 || len(c.Segments) > 1
}

// expireLogicalSwitch rejects c's switch when it is still before activation (preparing, prepared, or an unconfirmed
// preparation) past logicalSwitchPrepareLimit, so the switch ends with a reason instead of retrying for good and
// a batch item waiting on it ends refused. An activated switch, its first-turn delivery and an ambiguous input
// delivery are never timed out. It reports whether it handled this pass: a cancel that could not take the lock is
// retried on the next sweep rather than racing a new attempt.
func (s *Service) expireLogicalSwitch(ctx context.Context, st logicalSwitchStore, c *db.LogicalConversation, now time.Time) bool {
	op := c.Switch
	if op == nil || op.ExpectedEpoch != c.Epoch || op.CreatedAt.IsZero() || now.Sub(op.CreatedAt) < logicalSwitchPrepareLimit {
		return false
	}
	if op.Phase != "preparing" && op.Phase != "prepared" && op.Phase != "in_doubt" {
		return false
	}
	reason := logicalSwitchExpiredReason(op)
	cancelled, err := st.CancelConversationSwitch(ctx, c.MachineID, c.SessionID, op.OperationID, op.ExpectedEpoch, reason)
	if err != nil {
		s.logf("assistant: switch %s past its limit: cancel: %v", op.OperationID, err)
		return true
	}
	if !cancelled {
		return true
	}
	s.logf("assistant: switch %s of %s/%s ended: %s", op.OperationID, c.MachineID, short(c.SessionID), reason)
	s.afterLogicalSwitchCancel(ctx, st, c.MachineID, c.SessionID)
	return true
}

// afterLogicalSwitchCancel rebinds a conversation whose switch was cancelled before activation: it stops following
// an abandoned target and lifts a retirement its source may already carry.
func (s *Service) afterLogicalSwitchCancel(ctx context.Context, st logicalSwitchStore, machine, id string) {
	if next, err := st.LogicalConversation(ctx, machine, id); err == nil && next != nil {
		rebind, cancel := context.WithTimeout(ctx, 5*time.Second)
		s.restoreLogicalSource(rebind, next, 3)
		s.logicalRebind(rebind, next)
		cancel()
	}
	s.logicalChanged()
	s.historyWake()
}

// restoreAfterCancelledRetire runs where a prepared switch's own retire of its source has returned but the switch
// did not activate. If the switch was cancelled meanwhile (its source still active), it lifts the retirement again:
// the cancel's own restore can run before that retire lands on the node, or meet the node busy with it, and nothing
// else lifts the mark at the first epoch until the next send.
func (s *Service) restoreAfterCancelledRetire(ctx context.Context, st logicalSwitchStore, machine, id, operation string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	op, err := st.ConversationSwitch(ctx, machine, id, operation)
	if err != nil || op == nil || op.Phase != "rejected" {
		return
	}
	c, err := st.LogicalConversation(ctx, machine, id)
	if err != nil || c == nil {
		return
	}
	if a, ok := c.Active(); !ok || a.ID != op.SourceSegment {
		return
	}
	s.restoreLogicalSource(ctx, c, 5)
	s.logicalRebind(ctx, c)
}

// restoreLogicalSource lifts the node's retirement mark from c's active segment, trying up to attempts times: the
// node refuses it while another call on that conversation holds its lock.
func (s *Service) restoreLogicalSource(ctx context.Context, c *db.LogicalConversation, attempts int) {
	a, ok := c.Active()
	if !ok || !logicalSourceMayBeRetired(c) {
		return
	}
	var err error
	for try := range attempts {
		if try > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
		if _, err = s.logicalPhysical(ctx, a.MachineID, "assistant.segment.restore", map[string]any{"session_id": a.SessionID}); err == nil {
			return
		}
	}
	s.logf("assistant: %s/%s keeps its retirement until its next send: %v", c.MachineID, short(c.SessionID), err)
}

func (s *Service) advanceLogicalConversation(ctx context.Context, st logicalSwitchStore, c *db.LogicalConversation) error {
	unresolved, unresolvedErr := st.ConversationUnresolvedInput(ctx, c.MachineID, c.SessionID)
	if unresolvedErr != nil {
		return unresolvedErr
	}
	// A pre-upgrade switch may already be preparing with a source delivery in
	// doubt. Coordinate that input before preparation/activation can block drain.
	if unresolved != nil {
		if active, ok := c.Active(); ok {
			return s.reconcileLogicalInput(ctx, st, c, active, *unresolved)
		}
		return nil
	}
	for _, seg := range c.Segments {
		if seg.ClosePending {
			closeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_, err := s.logicalPhysical(closeCtx, seg.MachineID, "session.close", map[string]any{"session_id": seg.SessionID})
			cancel()
			if err == nil {
				_ = st.ConversationSegmentClosed(ctx, seg.ID)
			}
		}
	}
	op := c.Switch
	if op != nil && (op.Phase == "preparing" || op.Phase == "in_doubt" && c.Epoch == op.ExpectedEpoch) {
		var p logicalSwitchRequest
		if err := json.Unmarshal(op.Request, &p); err != nil {
			return err
		}
		if p.PermissionMode == "" {
			source, _ := s.session(ctx, c.MachineID, c.SessionID)
			p.PermissionMode = source.PermissionMode
			if p.PermissionMode == "" {
				p.PermissionMode = "default"
			}
		}
		handoff, claimed, err := st.ConversationPreparation(ctx, c.MachineID, c.SessionID, op.OperationID)
		if err != nil {
			return err
		}
		if len(handoff) == 0 {
			if source, ok := c.Active(); ok && s.isZeroTurnPreparedSegment(ctx, c, source) {
				if prev, err := st.ConversationPendingHandoff(ctx, c.MachineID, c.SessionID, source.ID); err == nil && len(prev) > 0 {
					handoff = prev
				}
			}
			if len(handoff) == 0 {
				h, err := s.logicalSwitchHandoff(ctx, st, c)
				if err != nil {
					return err
				}
				handoff, err = json.Marshal(h)
				if err != nil {
					return err
				}
			}
			if err = st.PutConversationHandoff(ctx, c.MachineID, c.SessionID, op.OperationID, handoff); err != nil {
				return err
			}
		}
		method := "assistant.segment.receipt"
		if !claimed {
			var won bool
			won, err = st.ClaimConversationPreparation(ctx, c.MachineID, c.SessionID, op.OperationID)
			if err != nil {
				return err
			}
			if won {
				method = "assistant.segment.prepare"
			}
		}
		params := map[string]any{"operation_id": logicalNodeOperation(c, op.OperationID), "launcher": p.Launcher, "model": p.Model, "effort": p.Effort, "permission_mode": p.PermissionMode, "is_main": c.SessionID == s.settings(ctx).Main}
		if method == "assistant.segment.prepare" {
			row, _ := s.session(ctx, c.MachineID, c.SessionID)
			params["title"] = row.Title
			cwd, err := s.prepareSpace(ctx, p.MachineID, row.Space)
			if err != nil {
				_ = st.ResolveConversationPreparation(ctx, c.MachineID, c.SessionID, op.OperationID, "rejected", "preparation unavailable")
				return err
			}
			params["cwd"] = cwd
			// The role's policy goes with the preparation, as the manager makes it (never from the app's request): the
			// first input carries the same policy, so the prepared process is kept. A Codex thread with no turn yet
			// cannot be resumed, so a restart for a policy arriving only with that input started a new thread under
			// another id and lost the segment (a direction supervisor switched to Codex, live 2026-11-22).
			policy, err := s.supervisionInstructions(ctx, c.MachineID, c.SessionID)
			if err != nil {
				_ = st.ResolveConversationPreparation(ctx, c.MachineID, c.SessionID, op.OperationID, "rejected", "preparation unavailable")
				return err
			}
			if policy != "" {
				params["supervision_instructions"] = policy
			}
		}
		// The durable claim allows one start. A saved positive receipt survives
		// manager and node restarts. The node prepares an empty, private session
		// and may call back into the hub, so release the DB connection before the RPC.
		targetSession, err := st.ConversationPreparedSession(ctx, c.MachineID, c.SessionID, op.OperationID, p.MachineID, c.Epoch)
		if err != nil {
			return err
		}
		if targetSession == "" {
			raw, err := s.logicalPhysical(ctx, p.MachineID, method, params)
			if err != nil {
				// A failed receipt lookup leaves the earlier prepare unconfirmed. Only
				// a definite failure of the initial prepare can reject it here.
				if method == "assistant.segment.prepare" && logicalNotTaken(err) {
					return st.ResolveConversationPreparation(ctx, c.MachineID, c.SessionID, op.OperationID, "rejected", "preparation refused")
				}
				_ = st.ResolveConversationPreparation(ctx, c.MachineID, c.SessionID, op.OperationID, "in_doubt", "preparation receipt pending")
				return err
			}
			var reply struct {
				SessionID string `json:"session_id"`
				Prepared  bool   `json:"prepared"`
			}
			if json.Unmarshal(raw, &reply) != nil || !reply.Prepared || reply.SessionID == "" {
				return nil
			}
			targetSession = reply.SessionID
			// Keep the positive receipt even if cancellation or deletion won during the
			// RPC. Mapping checks authority again, and the sweep closes an orphan.
			save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			err = st.RecordConversationPreparedCandidate(save, db.ConversationPreparedCandidate{
				MachineID: p.MachineID, SessionID: targetSession,
				LogicalMachine: c.MachineID, LogicalSession: c.SessionID, OperationID: op.OperationID,
			})
			cancel()
			if err != nil {
				return err
			}
		}
		source, ok := c.Active()
		if !ok {
			return logicalRefusal(ctx, "unavailable")
		}
		target, err := s.logicalTarget(ctx, p.MachineID, p.Launcher)
		if err != nil {
			return err
		}
		// Pull a proven physical tail after preparation; never infer it from item count.
		// A confirmed 0-turn prepared placeholder has no transcript yet and freezes at 0.
		// For an offline-source failover switch, fall back to persisted Postgres ConversationHistory.
		var tail historyPage
		var tailSeq int
		err = s.physicalHistoryCall(ctx, source.MachineID, DeviceSessionItems, map[string]any{"session_id": source.SessionID, "limit": 1, "lazy_images": true}, &tail)
		if err == nil && tail.ToSeq != nil && !tail.MoreAfter {
			tailSeq = *tail.ToSeq
		} else if s.isZeroTurnEmptyTail(ctx, c, source, tail, err, p.AllowOfflineSource) {
			tailSeq = 0
		} else if p.AllowOfflineSource && (errors.Is(busError(err), apierr.ErrOffline) || errors.Is(busError(err), rpc.ErrClosed) || s.machinePhysicallyOffline(ctx, source.MachineID)) {
			seq, okTail := s.offlineHistoryTailSeq(ctx, source.MachineID, source.SessionID)
			if !okTail {
				if err != nil {
					return err
				}
				return logicalRefusal(ctx, "missing_generation")
			}
			tailSeq = seq
		} else {
			if err != nil {
				return err
			}
			return logicalRefusal(ctx, "missing_generation")
		}
		var prepared conversationlog.Segment
		prepared.ID = randomHex(16)
		prepared.MachineID = p.MachineID
		prepared.SessionID = targetSession
		prepared.Engine = target.Engine
		prepared.Launcher = p.Launcher
		prepared.Model = p.Model
		prepared.Effort = p.Effort
		prepared.PermissionMode = p.PermissionMode
		if err = st.PrepareConversationSegment(ctx, c.MachineID, c.SessionID, op.OperationID, prepared, tailSeq); err != nil {
			return err
		}
		c, err = st.LogicalConversation(ctx, c.MachineID, c.SessionID)
		if err != nil || c == nil {
			return err
		}
		op = c.Switch
	}
	if op != nil && op.Phase == "prepared" {
		// Card creation/approval is fenced by this durable unfinished operation.
		for _, ix := range s.pendingInteractions() {
			if ix.MachineID == c.MachineID && ix.SessionID == c.SessionID {
				return logicalRefusal(ctx, "busy")
			}
		}
		if source, ok := c.Active(); ok {
			_, err := s.logicalPhysical(ctx, source.MachineID, "assistant.segment.retire", map[string]any{"session_id": source.SessionID})
			if err != nil && !errors.Is(err, apierr.ErrOffline) && !errors.Is(busError(err), apierr.ErrOffline) && !errors.Is(busError(err), rpc.ErrClosed) {
				// The retire may still have landed on the node.
				s.restoreAfterCancelledRetire(ctx, st, c.MachineID, c.SessionID, op.OperationID)
				return err
			}
		}
		if err := st.ActivateConversationSegment(ctx, c.MachineID, c.SessionID, op.OperationID, c.Epoch); err != nil {
			if errors.Is(err, db.ErrConversationEpoch) {
				s.restoreAfterCancelledRetire(ctx, st, c.MachineID, c.SessionID, op.OperationID)
			}
			return err
		}
		var err error
		c, err = st.LogicalConversation(ctx, c.MachineID, c.SessionID)
		if err != nil {
			return err
		}
		s.logicalRebind(ctx, c)
	}
	if c == nil {
		return nil
	}
	if c.Switch == nil {
		if active, ok := c.Active(); ok && s.isVanishedZeroTurnSegment(ctx, c, active) {
			if pending, err := st.ConversationInputPending(ctx, c.MachineID, c.SessionID); err == nil && pending {
				if _, err := s.beginZeroTurnRecoverySwitch(ctx, st, c, active); err != nil {
					return err
				}
				s.historyWake()
				return nil
			}
		}
	}
	return s.drainLogicalInput(ctx, st, c)
}

func (s *Service) isZeroTurnEmptyTail(ctx context.Context, c *db.LogicalConversation, source conversationlog.Segment, tail historyPage, err error, allowOfflineSource bool) bool {
	emptyPage := err == nil && (tail.ToSeq == nil || *tail.ToSeq == 0) && len(tail.Items) == 0
	missingTranscript := err != nil && isMissingTranscriptErr(err)
	offlineAllowed := allowOfflineSource && err != nil && (errors.Is(busError(err), apierr.ErrOffline) || errors.Is(busError(err), rpc.ErrClosed) || s.machinePhysicallyOffline(ctx, source.MachineID))
	return (emptyPage || missingTranscript || offlineAllowed) && s.isZeroTurnPreparedSegment(ctx, c, source)
}

func (s *Service) beginZeroTurnRecoverySwitch(ctx context.Context, st logicalConversationStore, c *db.LogicalConversation, active conversationlog.Segment) (*db.LogicalConversation, error) {
	launcher := active.Launcher
	if launcher == "" {
		launcher = proto.AssistantEngineKind(active.Engine)
	}
	if active.MachineID == "" || launcher == "" || s.isWorkMachine(active.MachineID) {
		return nil, logicalRefusal(ctx, "unavailable")
	}
	target, err := s.logicalTarget(ctx, active.MachineID, launcher)
	if err != nil {
		return nil, err
	}
	role, err := s.logicalRoleRequired(ctx, c.MachineID, c.SessionID)
	if err != nil {
		return nil, err
	}
	if role && !target.SupervisionPolicy {
		return nil, s.supervisionRuntimeRefusal(ctx, active.MachineID)
	}
	p := logicalSwitchRequest{
		OperationID:    "zero-turn-recover-" + randomHex(12),
		ExpectedEpoch:  c.Epoch,
		MachineID:      active.MachineID,
		Launcher:       launcher,
		Model:          active.Model,
		Effort:         active.Effort,
		PermissionMode: active.PermissionMode,
	}
	request, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if err := st.BeginConversationSwitch(ctx, c.MachineID, c.SessionID, db.ConversationSwitch{
		OperationID:   p.OperationID,
		ExpectedEpoch: c.Epoch,
		SourceSegment: active.ID,
		Request:       request,
	}); err != nil && !errors.Is(err, db.ErrConversationSwitchBusy) && !errors.Is(err, db.ErrConversationEpoch) {
		return nil, err
	}
	s.logicalChanged()
	next, err := st.LogicalConversation(ctx, c.MachineID, c.SessionID)
	if err != nil {
		return nil, err
	}
	if next == nil || next.Deleted {
		return nil, db.ErrConversationDeleted
	}
	return next, nil
}

func (s *Service) drainLogicalInput(ctx context.Context, st logicalSwitchStore, c *db.LogicalConversation) (retErr error) {
	a, ok := c.Active()
	if !ok {
		return nil
	}
	unresolved, err := st.ConversationUnresolvedInput(ctx, c.MachineID, c.SessionID)
	if err != nil {
		return err
	}
	if unresolved != nil {
		return s.reconcileLogicalInput(ctx, st, c, a, *unresolved)
	}
	if c.Switch != nil && c.Switch.Phase != "active" {
		return nil
	}
	pending, err := st.ConversationInputPending(ctx, c.MachineID, c.SessionID)
	if err != nil || !pending {
		return err
	}
	vanishedActiveTarget := c.Switch != nil && c.Switch.Phase == "active" && s.isVanishedZeroTurnSegment(ctx, c, a)
	delivery, ok := st.(logicalDeliveryStore)
	if !ok {
		return logicalRefusal(ctx, "unavailable")
	}
	input, err := st.ClaimConversationInput(ctx, c.MachineID, c.SessionID, c.Epoch)
	if err != nil || input == nil {
		return err
	}
	defer s.publishSoon() // its row's undelivered bubble moves on: awaiting its receipt, then gone or expired
	// Until the input call is attempted, local validation/restore failures prove
	// no message was sent. Do not strand that input as an ambiguous crash claim.
	dispatched := false
	defer func() {
		if retErr == nil || dispatched {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		operation := ""
		if c.Switch != nil {
			operation = c.Switch.OperationID
		}
		if err := delivery.RequeueClaimedConversationInput(cleanup, c.MachineID, c.SessionID, c.Epoch, *input, operation); err != nil {
			retErr = errors.Join(retErr, err)
			return
		}
		if c.Switch != nil {
			if next, err := st.LogicalConversation(cleanup, c.MachineID, c.SessionID); err == nil {
				s.logicalRebind(cleanup, next)
			}
		}
		s.historyWake()
	}()
	var queued logicalInput
	if err = json.Unmarshal(input.Payload, &queued); err != nil {
		return err
	}
	if queued.Transport != nil {
		ctx = context.WithValue(ctx, logicalTransportKey{}, *queued.Transport)
	}
	if err = s.putSupervisionInstructions(ctx, c.MachineID, c.SessionID, queued.Params); err != nil {
		return err
	}
	if a.Ordinal > 1 {
		// Execution choices were resolved against the source when this input was
		// queued. Only its content/provenance travels; the activated target owns
		// launcher/model/permission and process-specific configuration.
		for _, key := range []string{"launcher", "model", "effort", "permission_mode", "config_dir", "model_fingerprint", "runtime", "external"} {
			delete(queued.Params, key)
		}
		queued.Params["launcher"], queued.Params["model"], queued.Params["effort"], queued.Params["permission_mode"] = a.Launcher, a.Model, a.Effort, a.PermissionMode
		// The active segment's mode is inherited state, not the queued caller's explicit choice. A Claude segment uses
		// the current default; non-Claude and external segments keep their legacy generic mode without a resume default.
		delete(queued.Params, assistantwire.PermissionModeExplicitParam)
		defaultMode, _ := queued.Params[assistantwire.DefaultPermissionModeParam].(string)
		useDefault := defaultMode != "" && !proto.IsExternalLauncher(a.Launcher) && s.startPermissionModeSupported(ctx, a.MachineID, a.Launcher, defaultMode)
		if useDefault && a.Launcher != "" && a.Launcher != "claude" {
			agent := s.launcherAgent(withUnhidden(ctx, a.Launcher), a.MachineID, a.Launcher)
			useDefault = agent == "" || agent == "claude"
		}
		if useDefault {
			delete(queued.Params, "permission_mode")
		} else {
			delete(queued.Params, assistantwire.DefaultPermissionModeParam)
		}
	}
	var handoff json.RawMessage
	if c.Switch != nil {
		if handoff, _, err = st.ConversationPreparation(ctx, c.MachineID, c.SessionID, c.Switch.OperationID); err != nil {
			return err
		}
	} else if c.Epoch > 1 {
		// An idle acceptance kept it for this input (#14488); accepting the input discards it.
		if handoff, err = st.ConversationPendingHandoff(ctx, c.MachineID, c.SessionID, a.ID); err != nil {
			return err
		}
	}
	if c.Switch != nil || len(handoff) > 0 {
		var h mainHandoff
		if err = json.Unmarshal(handoff, &h); err != nil {
			return err
		}
		text, _ := queued.Params["text"].(string)
		queued.Params["text"] = renderLogicalHandoff(string(s.lang(ctx)), h, text)
	}
	var sendErr error
	err = st.WithConversationEpoch(ctx, c.MachineID, c.SessionID, c.Epoch, func(current *db.LogicalConversation) error {
		active, ok := current.Active()
		if !ok || active.ID != a.ID {
			return db.ErrConversationEpoch
		}
		valid, err := delivery.ConversationInputClaimCurrent(ctx, c.MachineID, c.SessionID, c.Epoch, *input)
		if err != nil {
			return err
		}
		if !valid {
			return db.ErrConversationEpoch
		}
		if vanishedActiveTarget && current.Switch != nil && current.Switch.Phase == "active" && s.physicalSegmentMissing(ctx, active) {
			return logicalRefusal(ctx, "unavailable")
		}
		if logicalSourceMayBeRetired(current) {
			if _, err := s.logicalPhysical(ctx, a.MachineID, "assistant.segment.restore", map[string]any{"session_id": a.SessionID}); err != nil {
				return err
			}
		}
		if active.Ordinal > 1 {
			marker, ok := s.store().(logicalSegmentInputStore)
			if !ok {
				return logicalRefusal(ctx, "unavailable")
			}
			if err := marker.MarkConversationSegmentDispatched(ctx, c.MachineID, c.SessionID, active.ID); err != nil {
				return err
			}
		}
		dispatched = true
		_, sendErr = s.logicalPhysical(ctx, a.MachineID, queued.Method, cloneLogicalParams(queued.Params, a))
		return nil
	})
	if err != nil {
		return err
	}
	outcome := "accepted"
	if sendErr != nil {
		outcome = "in_doubt"
		if logicalNotTaken(sendErr) {
			outcome = "rejected"
		}
	}
	operation := ""
	if c.Switch != nil {
		operation = c.Switch.OperationID
	}
	if err = delivery.ResolveClaimedConversationDelivery(ctx, c.MachineID, c.SessionID, c.Epoch, *input, operation, outcome); err != nil {
		return err
	}
	if c.Switch != nil {
		next, err := st.LogicalConversation(ctx, c.MachineID, c.SessionID)
		if err != nil {
			return err
		}
		s.logicalRebind(ctx, next)
	}
	s.historyWake()
	return sendErr
}

func renderLogicalHandoff(language string, h mainHandoff, text string) string {
	turns := make([]prompts.HandoffTurn, 0, len(h.Recent))
	for _, t := range h.Recent {
		turns = append(turns, prompts.HandoffTurn{Role: t.Role, Text: t.Text})
	}
	return prompts.RenderSegmentHandoff(prompts.SegmentHandoff{
		Language:     language,
		FromLauncher: h.FromLauncher,
		FromModel:    h.FromModel,
		Summary:      h.Summary,
		Recent:       turns,
		Text:         text,
		Partial:      h.Partial,
	})
}

func logicalHandoffExecution(row proto.Session, c *db.LogicalConversation, items []proto.Item) (string, string) {
	prev := conversationExecutionNow(c.MachineID, row, c)
	launcher, model := strings.TrimSpace(prev.Launcher), strings.TrimSpace(prev.Model)
	if model == "" {
		model = strings.TrimSpace(row.Model)
	}
	if model == "" {
		for i := len(items) - 1; i >= 0; i-- {
			if items[i].Role == "assistant" {
				if m := strings.TrimSpace(items[i].Model); m != "" {
					model = m
					break
				}
			}
		}
	}
	return launcher, model
}

func logicalHandoffTurn(item proto.Item) (prompts.HandoffTurn, bool, bool) {
	switch item.Kind {
	case proto.KindText:
		if item.Role != "user" && item.Role != "assistant" {
			return prompts.HandoffTurn{}, false, false
		}
		if item.Stored != nil && !item.Stored.Complete {
			return prompts.HandoffTurn{}, false, true
		}
		text := strings.TrimSpace(transcript.StripInjected(item.Text))
		if text == "" {
			return prompts.HandoffTurn{}, false, false
		}
		return prompts.HandoffTurn{Role: item.Role, Text: text}, true, false
	case proto.KindToolUse:
		name := strings.TrimSpace(item.ToolName)
		raw := strings.TrimSpace(string(item.ToolInput))
		if name == "" && raw == "" {
			return prompts.HandoffTurn{}, false, false
		}
		text := name
		if raw != "" {
			text = name + "(" + clip(oneLine(raw), 400) + ")"
		}
		return prompts.HandoffTurn{Role: "tool_use", Text: text}, true, item.Stored != nil && !item.Stored.Complete
	case proto.KindToolResult:
		name := strings.TrimSpace(item.ToolName)
		body := strings.TrimSpace(transcript.StripInjected(item.Text))
		if name == "" && body == "" {
			return prompts.HandoffTurn{}, false, false
		}
		var b strings.Builder
		if item.IsError {
			b.WriteString("[error] ")
		}
		if name != "" {
			b.WriteString(name)
			if body != "" {
				b.WriteString(": ")
			}
		}
		if body != "" {
			b.WriteString(clip(body, 800))
		}
		return prompts.HandoffTurn{Role: "tool_result", Text: b.String()}, true, item.Stored != nil && !item.Stored.Complete
	default:
		return prompts.HandoffTurn{}, false, false
	}
}

func (s *Service) logicalSwitchHandoff(ctx context.Context, st logicalSwitchStore, c *db.LogicalConversation) (mainHandoff, error) {
	var h mainHandoff
	raw, err := s.logicalHistoryCall(ctx, st, c, DeviceAssistantHandoff, json.RawMessage(`{}`))
	if err != nil {
		// When the active source machine is offline and Postgres has no covered page yet,
		// degrade to a partial handoff carrying any persisted summary instead of blocking failover.
		if a, ok := c.Active(); ok && (errors.Is(busError(err), apierr.ErrOffline) || errors.Is(busError(err), rpc.ErrClosed) || s.machinePhysicallyOffline(ctx, a.MachineID)) {
			h.Partial = true
			if history, ok := s.store().(conversationHistoryStore); ok {
				if source, herr := history.ConversationHistory(ctx, a.MachineID, a.SessionID); herr == nil && source != nil && source.SummaryKnown {
					h.Summary = strings.TrimSpace(source.Summary)
				}
			}
			return h, nil
		}
		return mainHandoff{}, err
	}
	if err = json.Unmarshal(raw, &h); err != nil {
		return h, err
	}
	row, _ := s.session(ctx, c.MachineID, c.SessionID)
	h.FromLauncher, h.FromModel = logicalHandoffExecution(row, c, nil)
	previousSummary := h.Summary
	h.Summary = ""
	if a, ok := c.Active(); ok {
		if history, ok := s.store().(conversationHistoryStore); ok {
			source, err := history.ConversationHistory(ctx, a.MachineID, a.SessionID)
			if err != nil {
				return h, err
			}
			if source != nil && source.SummaryKnown && strings.TrimSpace(source.Summary) != "" {
				h.Summary = source.Summary
				return h, nil
			}
		}
	}
	// The source engine's own compaction result is always preferred. The logical
	// handoff already reads persisted summaries; ask its active source once too.
	if a, ok := c.Active(); ok {
		var source mainHandoff
		if err = s.physicalHistoryCall(ctx, a.MachineID, DeviceAssistantHandoff, map[string]any{"session_id": a.SessionID}, &source); err == nil && strings.TrimSpace(source.Summary) != "" {
			h.Summary = source.Summary
			return h, nil
		}
	}
	if len(h.Recent) == 0 {
		return h, nil
	}
	page, err := s.logicalHistoryPage(ctx, st, c, historyParams{Limit: 500, LazyImages: true})
	if err != nil {
		return h, err
	}
	if h.FromLauncher == "" || h.FromModel == "" {
		h.FromLauncher, h.FromModel = logicalHandoffExecution(row, c, page.Items)
	}
	summaryTurns := []prompts.HandoffTurn{}
	size := 0
	for i := len(page.Items) - 1; i >= 0; i-- {
		turn, ok, partial := logicalHandoffTurn(page.Items[i])
		if partial {
			h.Partial = true
		}
		if !ok {
			continue
		}
		if size+len(turn.Text) > 120000 {
			h.Partial = true
			break
		}
		size += len(turn.Text)
		summaryTurns = append(summaryTurns, turn)
	}
	slices.Reverse(summaryTurns)
	h.Partial = h.Partial || page.Partial || (page.HasMore != nil && *page.HasMore)
	payload := map[string]any{"previous_summary": previousSummary, "history": summaryTurns}
	if h.FromLauncher != "" || h.FromModel != "" {
		payload["previous_execution"] = map[string]string{"launcher": h.FromLauncher, "model": h.FromModel}
	}
	input, _ := json.Marshal(payload)
	call, err := checkModelCall(assistantwire.ModelCompleteParams{System: prompts.RenderSegmentSummary(answerLanguage(s.hello(ctx).Locale)), Messages: []assistantwire.ModelMessage{{Role: "user", Content: string(input)}}, MaxTokens: 2048})
	if err != nil {
		return h, err
	}
	h, fellBack, err := summarizeLogicalHandoff(ctx, h, previousSummary, func() (assistantwire.ModelCompleteResult, error) {
		return s.runModelCall(ctx, "kb", "conversation handoff", "conversation handoff", call)
	})
	if err != nil {
		return h, err
	}
	if fellBack {
		s.logf("assistant: conversation handoff for %s/%s continues without a new summary: the knowledge-processing model is unavailable", c.MachineID, c.SessionID)
	}
	return h, nil
}

// summarizeLogicalHandoff asks run for the handoff's summary. A paused model is waited out (waitLogicalSummary).
// A model that is not set up (model_unavailable) does not recover by retrying: the sweep would retry the switch
// every few seconds and leave it preparing until someone changed Settings. The switch goes ahead with the recent
// turns already in h, the earlier summary if there was one, and Partial set so the new session is told some
// history is missing; fellBack reports that. Any other failure is returned for the sweep to retry.
func summarizeLogicalHandoff(ctx context.Context, h mainHandoff, previousSummary string, run func() (assistantwire.ModelCompleteResult, error)) (mainHandoff, bool, error) {
	result, err := waitLogicalSummary(ctx, run)
	if err != nil {
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == assistantwire.CodeModelUnavailable {
			h.Summary = previousSummary
			h.Partial = true
			return h, true, nil
		}
		return h, false, err
	}
	h.Summary = result.Text
	return h, false, nil
}

func waitLogicalSummary(ctx context.Context, run func() (assistantwire.ModelCompleteResult, error)) (assistantwire.ModelCompleteResult, error) {
	for {
		out, err := run()
		if err == nil {
			return out, nil
		}
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != assistantwire.CodeModelPaused {
			return out, err
		}
		var pause assistantwire.ModelPause
		_ = json.Unmarshal(re.Data, &pause)
		until, _ := time.Parse(time.RFC3339, pause.Until)
		wait := time.Until(until)
		if wait < time.Second {
			wait = time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return out, ctx.Err()
		case <-timer.C:
		}
	}
}

// Node receipts are scoped by logical identity as well as the public operation ID.
func logicalNodeOperation(c *db.LogicalConversation, operation string) string {
	data, _ := json.Marshal([3]string{c.MachineID, c.SessionID, operation})
	digest := sha256.Sum256(data)
	return "segment-" + hex.EncodeToString(digest[:])
}
