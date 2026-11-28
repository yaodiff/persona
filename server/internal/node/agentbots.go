package node

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/agents"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Registered bot endpoints use the same Orca client as the structured-session bridge. The manager supplies an exact
// session ID, or a bot ID whose open Home Chat this node resolves (bots.chats) on every call; this node never selects a
// first/default bot or another chat, and the manager never dials Orca. agentbot.roster and agentbot.items list the
// computer's bots and a bot's open items for the manager's discovery and the assistant's tools (#16891).
func (n *Node) handleAgentBot(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch method {
	case "agentbot.roster":
		return agentBotRoster(ctx, orcaClient())
	case "agentbot.items":
		var req struct {
			BotID    string                 `json:"bot_id"`
			Statuses []proto.OrcaItemStatus `json:"statuses"`
		}
		if err := json.Unmarshal(raw, &req); err != nil || !validBotRef(req.BotID) {
			return nil, agentBotRPCError("invalid", "invalid bot items request")
		}
		items, err := orcaClient().BotItems(ctx, proto.OrcaRef(req.BotID), req.Statuses...)
		if err != nil {
			return nil, agentBotRosterError(err)
		}
		return items, nil
	case "agentbot.describe", "agentbot.history":
		var req agents.BotHistoryRequest
		if err := json.Unmarshal(raw, &req); err != nil || (req.SessionID == "") == (req.BotID == "") ||
			req.BotID != "" && !validBotRef(req.BotID) {
			return nil, agentBotRPCError("invalid", "invalid bot history request")
		}
		limit := req.Limit
		if method == "agentbot.describe" {
			limit = 1
		}
		c := orcaClient()
		session := req.SessionID
		if req.BotID != "" {
			home, err := agentBotHome(ctx, c, req.BotID)
			if err != nil {
				return nil, err
			}
			session = home
		}
		page, err := agentBotRead(ctx, c, session, req.Cursor, limit)
		if err != nil {
			return nil, agentBotReadError(err)
		}
		return page, nil
	case "agentbot.send":
		var req agents.BotSendRequest
		if err := json.Unmarshal(raw, &req); err != nil || req.SessionID == "" || req.OperationID == "" || req.Text == "" ||
			req.BotID != "" && !validBotRef(req.BotID) {
			return nil, agentBotRPCError("invalid", "invalid bot send request")
		}
		c := orcaClient()
		if req.BotID != "" {
			// The operation was fixed for this chat: a bot that has started another Home Chat since is not followed with
			// it (the user reads the new chat first and sends again), and nothing is written.
			home, err := agentBotHome(ctx, c, req.BotID)
			if err != nil {
				return nil, err
			}
			if home != req.SessionID {
				return nil, agentBotRPCError("conflict", "bot started a new Home Chat")
			}
		}
		page, err := agentBotRead(ctx, c, req.SessionID, "", 1)
		if err != nil {
			return nil, agentBotReadError(err)
		}
		if !page.CanSend {
			return nil, agentBotRPCError("not_found", "bot session is not active")
		}
		// The operation ID and exact payload fingerprint were fixed before this call. No retryUnknown/auto-repost is used.
		err = c.SendSession(ctx, orcart.Write{SessionID: req.SessionID, Fence: page.Fence, OperationID: req.OperationID}, req.Text, nil)
		if err != nil {
			return nil, agentBotSendError(err)
		}
		return map[string]any{"accepted": true}, nil
	default:
		return nil, agentBotRPCError("invalid", "unsupported bot method")
	}
}

// validBotRef: an Orca bot id as a request may carry one.
func validBotRef(id string) bool { return id != "" && len(id) <= 200 && strings.TrimSpace(id) == id }

// agentBotHome is the bot's open Home Chat (bots.chats); not_found when the bot is gone or has none open.
func agentBotHome(ctx context.Context, c *orcart.Client, botID string) (string, error) {
	home, _, _, err := c.BotHome(ctx, proto.OrcaRef(botID))
	switch {
	case err != nil && (orcart.IsCode(err, "bot_not_found") || orcart.IsCode(err, "invalid_argument")):
		return "", agentBotRPCError("not_found", "bot is not on this computer")
	case err != nil:
		return "", agentBotReadError(err)
	case home == "":
		return "", agentBotRPCError("not_found", "bot has no open Home Chat")
	}
	return string(home), nil
}

// agentBotRoster lists the computer's bots (bots.roster) with each one's open Home Chat (bots.chats), home folder (the
// worktree its Home Chat runs in, worktree.ps) and open items (bots.items.list). Orca not running is no bots, not a
// failure; an Orca that has no bots (an older one) lists none. A bot whose chats or items cannot be read is listed
// without them.
func agentBotRoster(ctx context.Context, c *orcart.Client) (*proto.OrcaBotRoster, error) {
	bots, err := c.Bots(ctx)
	switch {
	case errors.Is(err, orcart.ErrNotRunning):
		return &proto.OrcaBotRoster{Running: false, Bots: []proto.OrcaBot{}}, nil
	case orcart.IsCode(err, "method_not_found") || orcart.IsCode(err, "method_not_supported"):
		return &proto.OrcaBotRoster{Running: true, Bots: []proto.OrcaBot{}}, nil
	case err != nil:
		return nil, agentBotRosterError(err)
	}
	folders := map[string]string{} // Orca session → the local worktree its row is in
	if trees, _, _, err := c.Worktrees(ctx, ""); err == nil {
		for _, w := range trees {
			if w.HostID != "" && w.HostID != "local" {
				continue
			}
			for _, a := range w.Agents {
				if id, ok := orcart.SessionOf(a.PaneKey); ok {
					folders[id] = w.Path
				}
			}
		}
	}
	for i := range bots {
		b := &bots[i]
		if home, title, active, err := c.BotHome(ctx, b.BotID); err == nil {
			b.HomeSessionID, b.HomeTitle, b.LastActiveMs = home, title, active
			b.Folder = folders[string(home)]
		}
		if items, err := c.BotItems(ctx, b.BotID); err == nil {
			for _, it := range items.Items {
				if it.Status == proto.OrcaItemDone || it.Status == proto.OrcaItemCanceled {
					continue
				}
				b.Open++
				if it.Status == proto.OrcaItemNeedsYou {
					b.NeedsYou++
				}
			}
		}
	}
	slices.SortStableFunc(bots, func(a, b proto.OrcaBot) int { return strings.Compare(a.Name, b.Name) })
	return &proto.OrcaBotRoster{Running: true, Bots: bots}, nil
}

func agentBotRosterError(err error) error {
	switch {
	case orcart.IsCode(err, "bot_not_found"):
		return agentBotRPCError("not_found", "bot is not on this computer")
	case errors.Is(err, orcart.ErrNotRunning):
		return agentBotRPCError("unavailable", "bot runtime is unavailable")
	}
	return agentBotRPCError("unavailable", "bots are unavailable")
}

// agentBotPageWithFence carries the write fence out of band; it is not exposed in personal-Agent messages.
type agentBotPageWithFence struct {
	agents.BotPage
	Fence int64 `json:"-"`
}

func agentBotRead(ctx context.Context, c *orcart.Client, sessionID, cursor string, limit int) (*agentBotPageWithFence, error) {
	if limit == 0 {
		limit = 40
	}
	trees, _, _, err := c.Worktrees(ctx, "")
	if err != nil {
		return nil, err
	}
	var bound *orcart.AgentRow
	for _, tree := range trees {
		if tree.HostID != "" && tree.HostID != "local" {
			continue
		}
		for i := range tree.Agents {
			row := &tree.Agents[i]
			orcaID, ok := orcart.SessionOf(row.PaneKey)
			if !ok || orcaID != sessionID {
				continue
			}
			if bound != nil {
				return nil, agentBotRPCError("conflict", "bot session binding is ambiguous")
			}
			bound = row
		}
	}
	if bound == nil {
		return nil, agentBotRPCError("not_found", "bot session is not active")
	}
	history, err := c.History(ctx, sessionID, cursor, limit)
	if err != nil {
		return nil, err
	}
	page := &agentBotPageWithFence{BotPage: agents.BotPage{CanSend: true, Working: history.Working,
		Messages: []agents.Message{}, HasMore: history.HasOlder, NextCursor: history.NextCursor, SessionID: sessionID},
		Fence: history.Fence}
	page.RankTimeMs = bound.UpdatedAt
	for _, item := range history.Items {
		if item.Body.Kind != "message" || item.ItemID == "" {
			continue
		}
		m := agents.Message{ExternalID: item.ItemID, Role: item.Body.Role, CreateTimeMs: item.ObservedAt, Position: item.Sequence,
			Blocks: make([]agents.Block, 0, len(item.Body.Blocks))}
		for _, block := range item.Body.Blocks {
			m.Blocks = append(m.Blocks, agents.Block{Type: block.Type, Text: block.Text, Name: block.Name, Status: block.State,
				Summary: block.Summary, Input: block.Input})
			if block.Type == "tool-result" {
				m.Blocks[len(m.Blocks)-1].Text = block.Output
			}
		}
		page.Messages = append(page.Messages, m)
		if item.ObservedAt > page.RankTimeMs {
			page.RankTimeMs = item.ObservedAt
		}
	}
	return page, nil
}

func agentBotRPCError(code, message string) *rpc.Error {
	return &rpc.Error{Code: code, Message: message}
}

func agentBotReadError(err error) error {
	var re *rpc.Error
	if errors.As(err, &re) {
		return err
	}
	if orcart.IsCode(err, "agent_session_identity_required") || orcart.IsCode(err, "agent_session_not_found") {
		return agentBotRPCError("not_found", "bot session is not active")
	}
	if errors.Is(err, orcart.ErrNotRunning) {
		return agentBotRPCError("unavailable", "bot runtime is unavailable")
	}
	return agentBotRPCError("unavailable", "bot history is unavailable")
}

func agentBotSendError(err error) error {
	switch {
	case orcart.IsCode(err, "agent_session_operation_unknown"):
		return agentBotRPCError("unknown", "bot send outcome is unknown")
	case orcart.IsCode(err, "agent_session_payload_mismatch"):
		return agentBotRPCError("conflict", "bot operation payload changed")
	case orcart.IsCode(err, "agent_session_identity_required"), orcart.IsCode(err, "agent_session_not_found"):
		return agentBotRPCError("not_found", "bot session is not active")
	case errors.Is(err, orcart.ErrNotRunning):
		return agentBotRPCError("unavailable", "bot runtime is unavailable")
	case orcart.Refused(err):
		return agentBotRPCError("refused", "bot refused the send")
	default:
		// A transport/decode failure after a write may leave its outcome unknown. Never turn it into a fresh send.
		return agentBotRPCError("unknown", "bot send outcome is unknown")
	}
}
