package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

func codexServiceTier(raw json.RawMessage) string { return transcript.CodexServiceTier(raw) }

func codexTierID(tier string) string {
	switch tier {
	case "standard":
		return "default"
	case "fast":
		return "priority"
	default:
		return tier
	}
}

func codexSpeedChoices(tiers []proto.ServiceTier) []proto.ServiceTier {
	choices := []proto.ServiceTier{{ID: "default", Name: "Standard"}}
	seen := map[string]bool{"default": true}
	for _, choice := range tiers {
		choice.ID = codexTierID(choice.ID)
		if !validSpeedTier(choice.ID) || seen[choice.ID] {
			continue
		}
		choices = append(choices, choice)
		seen[choice.ID] = true
	}
	if len(choices) < 2 {
		return nil
	}
	return choices
}

func validSpeedTier(tier string) bool {
	return tier != "" && len(tier) <= 64 && strings.IndexFunc(tier, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) == -1 && tier[0] != '-'
}

// Translate a canonical choice to the ID advertised by that app-server. Older native catalogs used "fast".
func codexNativeSpeed(tiers []proto.ServiceTier, want string) string {
	if len(codexSpeedChoices(tiers)) == 0 {
		return ""
	}
	for _, choice := range tiers {
		if codexTierID(choice.ID) == want {
			return choice.ID
		}
	}
	if want == "default" {
		return want
	}
	return ""
}

func (n *Node) codexSpeedRow(s *proto.Session) {
	c, thread, ok := n.codexEffortThread(s.SessionID)
	if !ok {
		return
	}
	c.mu.Lock()
	tiers := c.models[n.codexModelNow(s.SessionID, c, thread, s.Model)].tiers
	c.mu.Unlock()
	if thread.serviceTier != "" {
		s.ServiceTier = thread.serviceTier
	}
	s.ServiceTiers = codexSpeedChoices(tiers)
}

// Speed is a separate RPC so an older node cannot silently ignore it in modes.
// There is no optimistic or next-message fallback for a billable speed choice.
func (n *Node) setSessionSpeed(ctx context.Context, id, tier string) error {
	tier = codexTierID(tier)
	if !validSpeedTier(tier) {
		return speedUnsupported()
	}
	unlock, err := n.inputTurn(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	n.mu.Lock()
	proc := n.procs[id]
	n.mu.Unlock()
	if proc != nil && proc.agent == "codex" && proc.cx != nil {
		return proc.cx.setSpeed(ctx, tier)
	}
	c, thread, ok := n.codexEffortThread(id)
	if !ok {
		return n.setTerminalSpeed(ctx, id, tier)
	}
	model := n.codexModelNow(id, c, thread, n.metaOf(id).model)
	c.mu.Lock()
	tiers := c.models[model].tiers
	c.mu.Unlock()
	native := codexNativeSpeed(tiers, tier)
	if native == "" {
		return refusal("session_speed_unsupported", "This model does not advertise the requested speed.", nil)
	}
	call, cancel := context.WithTimeout(ctx, codexEffortTimeout)
	defer cancel()
	if err := c.wire.call(call, "thread/settings/update", map[string]any{"threadId": thread.id, "serviceTier": native}, nil); err != nil {
		return fmt.Errorf("couldn't change Codex speed: %w", err)
	}
	c.mu.Lock()
	if current, exists := c.threads[thread.id]; exists {
		// A native settings notification received during the RPC is newer.
		if current.serviceTierRevision == thread.serviceTierRevision {
			current.serviceTier = tier
			current.serviceTierRevision++
		}
		current.revision++
		c.threads[thread.id] = current
	}
	c.mu.Unlock()
	n.notifyCodex()
	return nil
}

// setCodexTerminalSpeed uses the same rollout acknowledgment as terminal model changes, with the input lock held by
// setSessionSpeed through confirmation.
func (n *Node) setCodexTerminalSpeed(ctx context.Context, id, tier string) error {
	t, why, live := n.codexOnTerminalCtx(ctx, id, true)
	if !live {
		return speedUnsupported()
	}
	if why != nil {
		var rejected *rpc.Error
		if errors.As(why, &rejected) && rejected.Code == proto.CodeModesCodexNoRollout {
			return why
		}
		return speedUnsupported()
	}
	n.forgetMeta(t.rollout)
	now := n.codexTerminalSettingsOf(id, t, n.metaOf(id))
	model, _ := codexModelEffortsIn(t.home, now.model)
	if !slices.ContainsFunc(codexSpeedChoices(model.tiers), func(choice proto.ServiceTier) bool { return choice.ID == tier }) {
		return speedUnsupported()
	}
	native := tier
	switch tier {
	case "default":
		native = "standard"
	case "priority":
		native = "fast"
	}
	_, err := n.applyCodexTerminalSettings(ctx, id, t, "/model --speed="+native, codexTerminalSettings{tier: tier})
	var outcome *rpc.Error
	if errors.As(err, &outcome) {
		switch outcome.Code {
		case proto.CodeModesCodexRefused:
			return refusal("session_speed_refused", outcome.Message, nil)
		case proto.CodeModesCodexUnconfirmed:
			return terminalSpeedUnconfirmed()
		}
	}
	return err
}
