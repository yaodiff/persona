package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// agent_modes adjusts a running coding Agent's model, reasoning effort, and/or permission mode through the node's
// existing session.modes RPC (DeviceSessionModes). It stays strictly within the target session's advertised
// capabilities and Persona's safety boundaries:
//   - launcher switches stay on agent_switch (unknown fields such as launcher or ultracode are rejected);
//   - targets are resolved inside the caller's privacy space and re-checked after any confirmation wait;
//   - assistant conversations, stopped/offline sessions, unmanaged external runtime sessions, provider safety refusals,
//     read-only tasks (for permission_mode), and sessions with unresolved approvals (for permission_mode) are refused;
//   - any effective permission_mode change requires an explicit owner-only phone confirmation card (OwnerProposal on
//     cardSecretForm verified with validInternalAnswer); model/effort-only changes still honor confirmOutbound for
//     isolated spaces;
//   - permission_mode changes flow through DeviceSessionModes, which attaches AssistantModeActor and records the change
//     in session_mode_changes (#10297).

const (
	// agentModesWait matches the hub's 90-second /modes deadline (hub/ultracode.go).
	agentModesWait          = 90 * time.Second
	agentModesMaxInputBytes = 16 << 10
	agentModesMaxModelLen   = 200
)

type agentModesArgs struct {
	Machine        string `json:"machine"`
	SessionID      string `json:"session_id"`
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	PermissionMode string `json:"permission_mode"`
}

func decodeAgentModesArgs(args json.RawMessage) (agentModesArgs, error) {
	var a agentModesArgs
	if len(args) > agentModesMaxInputBytes {
		return a, fmt.Errorf("arguments exceed %d bytes", agentModesMaxInputBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return a, fmt.Errorf("arguments: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return a, errors.New("arguments: exactly one JSON object is required")
	}
	a.Machine = strings.TrimSpace(a.Machine)
	a.SessionID = strings.TrimSpace(a.SessionID)
	a.Model = strings.TrimSpace(a.Model)
	a.Effort = strings.TrimSpace(a.Effort)
	a.PermissionMode = strings.TrimSpace(a.PermissionMode)
	if a.Model == "" && a.Effort == "" && a.PermissionMode == "" {
		return a, errors.New("at least one of model, effort, or permission_mode is required")
	}
	if a.Model != "" {
		if len(a.Model) > agentModesMaxModelLen {
			return a, fmt.Errorf("model exceeds %d bytes", agentModesMaxModelLen)
		}
		if strings.ContainsFunc(a.Model, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return a, errors.New("model cannot contain control characters")
		}
	}
	if a.Effort != "" && a.Effort != "auto" && !slices.Contains(proto.CodexEffortLevels, a.Effort) {
		return a, fmt.Errorf("invalid effort %q", a.Effort)
	}
	if a.PermissionMode != "" && !slices.Contains(proto.PermissionModes, a.PermissionMode) {
		return a, fmt.Errorf("invalid permission_mode %q", a.PermissionMode)
	}
	return a, nil
}

func (s *Service) toolAgentModes(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	a, err := decodeAgentModesArgs(args)
	if err != nil {
		return "", err
	}
	space := s.agentCallerSpace(ctx, machine, self)
	am, c, err := s.findAgentInSpace(ctx, a.Machine, a.SessionID, &space)
	if err != nil {
		return "", err
	}
	effPerm, noop, err := s.validateAgentModesTarget(ctx, self, am, c, a)
	if err != nil {
		return "", err
	}
	if noop {
		return fmt.Sprintf("%s on %s is already in permission_mode %s: nothing changed.", agentLabel(c), am.Name, c.PermissionMode), nil
	}

	approvedFrom := c.PermissionMode
	approvalInput, err := marshalAgentModesProposal(am.ID, c.SessionID, approvedFrom, a.Model, a.Effort, effPerm)
	if err != nil {
		return "", err
	}

	changesSummary := formatAgentModesChanges(approvedFrom, a.Model, a.Effort, effPerm)
	if effPerm != "" {
		resText, allowed, err := s.confirmAgentPermissionMode(ctx, machine, self, am, c, approvedFrom, effPerm, changesSummary, approvalInput)
		if err != nil {
			return "", err
		}
		if !allowed {
			return resText, nil
		}
	} else {
		summary := agentLabel(c) + " · " + am.Name + "\n\n" + changesSummary
		if ok, outcome, err := s.confirmOutbound(ctx, machine, self, "agent_modes", "", summary, approvalInput); err != nil {
			return "", err
		} else if !ok {
			return outboundNotDone(outcome, "The user declined on the phone: nothing changed.", "nothing changed"), nil
		}
	}

	// Re-check after confirmation wait: the target may have stopped, moved spaces, become read-only, or gained an
	// unresolved approval while the card was waiting.
	am, c, err = s.findAgentInSpace(ctx, am.ID, c.SessionID, &space)
	if err != nil {
		return "", fmt.Errorf("the Agent is no longer available in this space after confirmation: %w. Nothing changed", err)
	}
	recheckArgs := a
	recheckArgs.PermissionMode = effPerm
	effPerm, noop, err = s.validateAgentModesTarget(ctx, self, am, c, recheckArgs)
	if err != nil {
		return "", err
	}
	if noop {
		return fmt.Sprintf("%s on %s is already in permission_mode %s: nothing changed.", agentLabel(c), am.Name, c.PermissionMode), nil
	}

	params := map[string]any{"session_id": c.SessionID}
	if a.Model != "" {
		params["model"] = a.Model
	}
	if a.Effort != "" {
		params["effort"] = a.Effort
	}
	if effPerm != "" {
		params["permission_mode"] = effPerm
	}

	wctx, cancel := context.WithTimeout(ctx, agentModesWait)
	defer cancel()
	var reply struct {
		Changed       *bool  `json:"changed"`
		Applied       string `json:"applied"`
		PendingEffort string `json:"pending_effort"`
	}
	if err := s.call(wctx, am.ID, DeviceSessionModes, params, &reply); err != nil {
		return "", fmt.Errorf("%s on %s: %w", agentLabel(c), am.Name, err)
	}

	appliedChanges := formatAgentModesChanges(c.PermissionMode, a.Model, a.Effort, effPerm)
	s.logf("agent_modes %s on %s: %s (from %s)", short(c.SessionID), am.ID, appliedChanges, short(self))
	if reply.Changed != nil && !*reply.Changed && reply.Applied != "restart" && reply.PendingEffort == "" {
		return fmt.Sprintf("%s on %s already matched (%s): nothing changed.", agentLabel(c), am.Name, appliedChanges), nil
	}
	var notes []string
	if reply.Applied == "restart" {
		notes = append(notes, "permission_mode restart queued for the end of the current turn")
	}
	if reply.PendingEffort != "" {
		notes = append(notes, fmt.Sprintf("effort %s queued for the end of the current turn", reply.PendingEffort))
	}
	suffix := ""
	if len(notes) > 0 {
		suffix = " (" + strings.Join(notes, "; ") + ")"
	}
	return fmt.Sprintf("Updated %s on %s: %s%s.", agentLabel(c), am.Name, appliedChanges, suffix), nil
}

// validateAgentModesTarget checks every policy and capability guard for changing c's modes. It returns the effective
// permission mode to apply (empty when already equal to c.PermissionMode) and whether the entire request is a no-op.
func (s *Service) validateAgentModesTarget(ctx context.Context, self string, am Device, c proto.Session, a agentModesArgs) (effPerm string, noop bool, err error) {
	label := agentLabel(c)
	if c.Assistant {
		return "", false, errors.New("that is an assistant conversation, not a coding agent: change its model or settings in Model settings or conversations_switch")
	}
	if stoppedAgent(c) || c.Status == proto.StatusOffline {
		return "", false, fmt.Errorf("%s on %s has stopped or is offline: session.modes only adjusts running Agents. Nothing changed", label, am.Name)
	}
	if err := s.externalSessionRefusal(ctx, self, am, c); err != nil {
		return "", false, err
	}
	if refused, why := sessionSafetyRefusal(c); refused {
		return "", false, fmt.Errorf("%s is blocked by a provider safety refusal (%s): a safety refusal remains a recorded blocker and cannot be bypassed with mode changes. Nothing changed", label, why)
	}
	effPerm = a.PermissionMode
	if effPerm != "" && c.PermissionMode != "" && c.PermissionMode == effPerm {
		effPerm = ""
	}
	if a.Model == "" && a.Effort == "" && effPerm == "" {
		return "", true, nil
	}
	if effPerm != "" {
		if s.sessionUnresolvedApproval(c) {
			return "", false, fmt.Errorf("%s has an unresolved approval or question waiting for the owner: resolve it directly first; changing permission_mode never grants or dismisses approvals. Nothing changed", label)
		}
		ro, err := s.isReadonlyTaskSession(ctx, am.ID, c.SessionID)
		if err != nil {
			return "", false, fmt.Errorf("cannot verify whether %s is a read-only task: %w. Nothing changed", label, err)
		}
		if ro {
			return "", false, fmt.Errorf("%s is running a read-only task: its permission_mode cannot be changed through agent_modes. Nothing changed", label)
		}
	}
	if err := modesCapabilityRefusal(c, am.Name, a.Model, a.Effort, effPerm); err != nil {
		return "", false, err
	}
	return effPerm, false, nil
}

// isReadonlyTaskSession reports whether machine/sid is currently executing an open read-only task from a started plan.
// Any store error fails closed so permission_mode changes cannot bypass read-only enforcement during an outage.
func (s *Service) isReadonlyTaskSession(ctx context.Context, machine, sid string) (bool, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return false, err
	}
	todo, ok := s.taskOf(ctx, machine, sid)
	if !ok {
		return false, nil
	}
	plans, err := s.conversationPlans(ctx, todo.Origin)
	if err != nil {
		return false, err
	}
	for _, plan := range plans {
		if plan.State != planStarted {
			continue
		}
		for _, task := range plan.Tasks {
			if task.TodoID == todo.ID && task.Readonly {
				return true, nil
			}
		}
	}
	return false, nil
}

// modesCapabilityRefusal rejects changes that the session's advertised Capabilities explicitly forbid. Nil capability
// pointers (older nodes) fall through to the node's own session.modes validation.
func modesCapabilityRefusal(c proto.Session, machineName, model, effort, perm string) error {
	caps := c.Capabilities
	if caps == nil {
		return nil
	}
	label := agentLabel(c)
	withReason := func(msg string) error {
		if r := strings.TrimSpace(c.ReadOnlyReason); r != "" {
			return fmt.Errorf("%s (%s). Nothing changed", msg, r)
		}
		return fmt.Errorf("%s. Nothing changed", msg)
	}
	if perm != "" {
		if caps.PermissionModes != 0 {
			if !caps.PermissionModes.Has(perm) {
				return fmt.Errorf("%s on %s does not support permission_mode %q (supported: %s). Nothing changed",
					label, machineName, perm, strings.Join(caps.PermissionModes.List(), ", "))
			}
		} else if caps.Modes != nil && !*caps.Modes {
			return withReason(fmt.Sprintf("%s on %s does not support changing permission_mode via session.modes", label, machineName))
		}
	}
	if model != "" && caps.Model != nil && !*caps.Model {
		return withReason(fmt.Sprintf("%s on %s does not support changing model via session.modes", label, machineName))
	}
	if effort != "" {
		if len(c.Efforts) > 0 && effort != "auto" && !slices.Contains(c.Efforts, effort) {
			return fmt.Errorf("%s on %s does not support effort %q (supported: %s). Nothing changed",
				label, machineName, effort, strings.Join(c.Efforts, ", "))
		}
		if caps.Modes != nil && !*caps.Modes && len(c.Efforts) == 0 && (caps.Model == nil || !*caps.Model) {
			return withReason(fmt.Sprintf("%s on %s does not support changing effort via session.modes", label, machineName))
		}
	}
	return nil
}

func (s *Service) confirmAgentPermissionMode(ctx context.Context, machine, self string, am Device, c proto.Session, fromMode, toMode, changes string, input json.RawMessage) (string, bool, error) {
	l := s.lang(ctx)
	key := "assistant-tools.agent_modes_card"
	if toMode == proto.ModeBypass {
		key = "assistant-tools.agent_modes_bypass_card"
	}
	now := time.Now().UTC()
	ix := proto.Interaction{
		ID:            "ix_" + randomHex(6),
		MachineID:     machine,
		SessionID:     self,
		Kind:          proto.IxPermission,
		ToolName:      "agent_modes",
		OwnerProposal: true,
		Reason:        "only_owner_knows: approve changing a coding Agent's permission mode",
		Summary: i18n.T(l, key, i18n.Args{
			"agent":   agentLabel(c),
			"machine": am.Name,
			"changes": changes,
		}),
		ToolInput:    input,
		SessionTitle: "Persona",
		CreatedAt:    now,
		ExpiresAt:    now.Add(15 * time.Minute),
	}
	if conv, ok := s.session(ctx, machine, self); ok {
		ix.Cwd = conv.Cwd
	}
	r, err := s.ask(withCard(ctx, cardSecretForm), ix)
	if errors.Is(err, errUserSpoke) {
		return "The user wrote in the conversation instead of confirming: nothing changed.", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !validInternalAnswer(ix, r) {
		return outboundNotDone(r.Outcome(), "The user declined on the phone: nothing changed.", "nothing changed"), false, nil
	}
	return "", true, nil
}

func marshalAgentModesProposal(machineID, sessionID, fromPerm, model, effort, perm string) (json.RawMessage, error) {
	p := map[string]string{
		"machine":    machineID,
		"session_id": sessionID,
	}
	if model != "" {
		p["model"] = model
	}
	if effort != "" {
		p["effort"] = effort
	}
	if perm != "" {
		if fromPerm != "" {
			p["from_permission_mode"] = fromPerm
		}
		p["permission_mode"] = perm
	}
	return json.Marshal(p)
}

func formatAgentModesChanges(fromPerm, model, effort, perm string) string {
	var parts []string
	if model != "" {
		parts = append(parts, "model="+model)
	}
	if effort != "" {
		parts = append(parts, "effort="+effort)
	}
	if perm != "" {
		if fromPerm != "" {
			parts = append(parts, fmt.Sprintf("permission_mode=%s→%s", fromPerm, perm))
		} else {
			parts = append(parts, "permission_mode="+perm)
		}
	}
	return strings.Join(parts, ", ")
}
