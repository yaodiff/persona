package orcart

import (
	"context"
	"strings"
)

// Orca's chats as tabs: worktree.ps lists a chat only once it has a turn (an agent row Orca's status feed reports), so
// a chat opened and not yet written to is found among the tabs (session.tabs.listAll), where every chat has one.
// Checked on Orca 1.4.277 (docs/integrations/orca.md "Structured sessions").

// chatTabLeaf ends the pane key ChatTabs gives the agent row of a chat found among the tabs, after the chat's own tab
// ("structured-agent-session-<session id>"), so SessionOf reads the chat out of it as out of a worktree.ps row's.
const chatTabLeaf = ":tab"

// ChatTabs is session.tabs.listAll: the chats of each worktree's tabs, in order, as worktree.ps would list them (a
// Worktree with only WorktreeID and Agents set). A chat's row has its pane key (SessionOf names the chat), its provider
// (AgentType), its title (ConversationName), and its status when Orca has one (State, UpdatedAt; none for a chat with
// no turn yet). A host without the method answers method_not_found (IsCode), which a caller takes as "no tabs to add".
func (c *Client) ChatTabs(ctx context.Context) ([]Worktree, error) {
	var res struct {
		Snapshots []struct {
			Worktree string `json:"worktree"`
			Tabs     []struct {
				Type       string `json:"type"`
				SessionID  string `json:"sessionId"`
				Agent      string `json:"agent"`
				Title      string `json:"title"`
				ChatStatus *struct {
					State     string `json:"state"`
					MainAgent *struct {
						State string `json:"state"`
					} `json:"mainAgent"`
					UpdatedAt int64 `json:"updatedAt"`
				} `json:"chatStatus"`
			} `json:"tabs"`
		} `json:"snapshots"`
	}
	if err := c.Call(ctx, ListTimeout, "session.tabs.listAll", map[string]any{}, &res); err != nil {
		return nil, err
	}
	var out []Worktree
	for _, s := range res.Snapshots {
		w := Worktree{WorktreeID: s.Worktree}
		for _, t := range s.Tabs {
			if t.Type != "agent-session" || strings.TrimSpace(t.SessionID) == "" {
				continue
			}
			row := AgentRow{PaneKey: SessionPanePrefix + t.SessionID + chatTabLeaf, AgentType: t.Agent, ConversationName: t.Title}
			if st := t.ChatStatus; st != nil {
				row.State, row.UpdatedAt = st.State, st.UpdatedAt
				if st.MainAgent != nil && st.MainAgent.State != "" {
					row.MainAgent = &struct {
						State string `json:"state"`
					}{State: st.MainAgent.State}
				}
			}
			w.Agents = append(w.Agents, row)
		}
		if len(w.Agents) > 0 {
			out = append(out, w)
		}
	}
	return out, nil
}

// Reveal is agentSession.reveal: Orca opens the chat's tab again (a chat closed in Orca keeps its record and history),
// so it takes messages again. A chat Orca no longer has is refused (an agent_session_* code).
func (c *Client) Reveal(ctx context.Context, sessionID string) error {
	var res struct {
		OK      *bool `json:"ok"`
		Refusal *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"refusal"`
	}
	if err := c.Call(ctx, WriteTimeout, "agentSession.reveal", map[string]any{"sessionId": sessionID}, &res); err != nil {
		return err
	}
	if res.OK != nil && !*res.OK {
		e := &Error{Method: "agentSession.reveal", Code: "agent_session_refused"}
		if res.Refusal != nil {
			e.Code, e.Message = res.Refusal.Code, res.Refusal.Message
		}
		return e
	}
	return nil
}
