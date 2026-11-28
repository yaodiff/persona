package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// questionOwnerSource reads decision rights at the ask_user source. The metadata is
// kept in ToolInput, so asynchronous questions retain it across manager restarts.
func questionOwnerSource(ix proto.Interaction) string {
	var rights struct {
		DecisionScope string `json:"decision_scope"`
		WhyOwner      string `json:"why_owner"`
		OwnerAction   string `json:"owner_action"`
	}
	_ = json.Unmarshal(ix.ToolInput, &rights)
	switch rights.OwnerAction {
	case "personal_unlock", "login", "second_factor", "experience_check":
		return cardSecretForm
	case "spending":
		return cardPlanMoney
	}
	if slices.Contains(whyOwners, rights.WhyOwner) {
		return decisionCardSource(rights.WhyOwner)
	}
	if rights.DecisionScope == "internal" {
		return "" // a credential constraint is not itself a request to unlock or disclose it
	}
	// Older callers cannot label personal unlocks. Keep this narrow safety fallback
	// while the updated tool contract asks for a typed action; never request a key.
	text := strings.ToLower(string(ix.ToolInput))
	if strings.Contains(text, "1password") || strings.Contains(text, "private key") || strings.Contains(text, "私钥") { // i18n-ignore: legacy question safety boundary
		return cardSecretForm
	}
	return ""
}

// internalCard admits only decisions within the supervisor's authority. A source
// tag never overrides a secret, an outward write or an owner-only operation.
func (s *Service) internalCard(ix proto.Interaction) bool {
	if secretOrForm(ix) || ix.Private || ix.OutwardWrite || ix.OwnerProposal || ix.ApprovalBatchID != "" {
		return false
	}
	src := s.inputCardSource(ix, false)
	if ix.ToolName == "ask_user" {
		return questionOwnerSource(ix) == ""
	}
	if src == cardExtensionApproval {
		// The source label alone does not prove authority: an unknown extension
		// operation may charge money or change data. Match the execution gate.
		return proto.InternalMaintenanceApproval(ix)
	}
	if src == cardConfirmOutbound {
		// Only the owned-Agent path opts in, after the track boundary was checked.
		return ix.ToolName == "agent_send"
	}
	if ix.Kind == proto.IxScreen && (!supervisedScreen(ix) || userScreen(ix) != "") {
		return false
	}
	if ix.Kind == proto.IxPermission && userApproval(ix) != "" {
		return false
	}
	return src == cardAgentPermission || src == cardAgentQuestion ||
		src == cardToolPermission && proto.InternalMaintenanceApproval(ix)
}

// activeInternalCard needs the manager-owned reviewer entry rather than the
// existing task ladder or a node's ordinary answer lookup.
func (s *Service) activeInternalCard(ix proto.Interaction) bool {
	if !s.internalCard(ix) {
		return false
	}
	src := s.inputCardSource(ix, false)
	return ix.ToolName == "ask_user" || src == cardConfirmOutbound || src == cardExtensionApproval ||
		s.settings(context.Background()).supervisorConversation(ix.SessionID)
}

// internalReviewer never sends an approval back to the conversation that raised
// it. Main's own cards use a separate, auditable background conversation. Failure
// to create it is closed unanswered, never interpreted as approval or owner work.
func (s *Service) internalReviewer(ctx context.Context, ix proto.Interaction) (string, string) {
	cfg := s.settings(ctx)
	home := s.canonical(cfg.MachineID)
	machineOf := func(id string) string {
		if id == cfg.Main {
			return s.canonical(cfg.mainRouteMachine())
		}
		if id == cfg.BackgroundSupervisor {
			return s.canonical(cmp.Or(cfg.BackgroundSupervisorMachineID, cfg.MachineID))
		}
		return s.canonical(s.conversationMachine(ctx, id, ""))
	}
	if cfg.supervisorConversation(ix.SessionID) && ix.SessionID != cfg.Main {
		return cfg.Main, machineOf(cfg.Main) // a source task cannot send a supervisor's own approval downwards
	}
	if ix.SessionID == cfg.Main && cfg.Main != "" {
		s.background.mu.Lock()
		defer s.background.mu.Unlock()
		cfg = s.settings(ctx)
		if cfg.BackgroundSupervisor != "" && cfg.BackgroundSupervisor != ix.SessionID {
			return cfg.BackgroundSupervisor, machineOf(cfg.BackgroundSupervisor)
		}
		if id, err := s.startBackground(ctx); err == nil && id != ix.SessionID {
			cfg = s.settings(ctx)
			return id, machineOf(id)
		}
		return "", home
	}
	to, _ := s.nonOwnerCardTarget(ctx, ix.MachineID, ix.SessionID)
	if to != "" && to != ix.SessionID {
		return to, machineOf(to)
	}
	return "", home
}

func validInternalAnswer(ix proto.Interaction, r proto.Resolution) bool {
	if r.Allow == nil || !*r.Allow || r.Cancel || r.Dismiss || r.Late || r.Remembered || !time.Now().Before(ix.ExpiresAt) {
		return false
	}
	return r.UserAllowed() || r.AnsweredBy == proto.AnsweredByAssistant && ix.ToolName == "agent_send" && !ix.OwnerProposal
}
