package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func (h *Hub) scopedQuestion(ix proto.Interaction) bool {
	return ix.Kind == proto.IxQuestion && ix.ToolName != proto.SecretHandoffTool && !h.hiddenFromExtensions(ix)
}

func (h *Hub) scopedPublicSession(machine, session string) bool {
	m := h.localMachine(machine)
	if m == nil {
		return false
	}
	s, ok := m.session(session)
	return ok && !spaceSession(&s)
}

// scopedDeviceRoute uses the same registered route pattern as extension
// authorization. A scoped device can receive only scopes for Agent integration.
func (h *Hub) scopedDeviceRoute(r *http.Request) string {
	if h.apiMux == nil {
		return ""
	}
	_, pattern := h.apiMux.Handler(r)
	switch pattern {
	case assistantwire.A2ARPC, assistantwire.A2ACard:
		return assistantwire.ScopeA2A
	case "GET /api/v1/interactions/history", "POST /api/v1/interactions/{id}/reanswer":
		// Stored answers have no pending card to prove their visibility or kind here.
		return ""
	case "GET /api/v1/agents/triage":
		// An extension's agents.read reads the triage's switches (extwire/agents_scopes.go, #17851); a scoped device
		// follows chats and has no use for the owner's settings.
		return ""
	case "GET /api/v1/ws",
		"GET /api/v1/machines/{mid}/launchers",
		"GET /api/v1/machines/{mid}/launchers/{name}/models":
		return extwire.ScopeAgentsRead
	}
	return extwire.RouteScopes[pattern]
}

func deviceAgentScope(scope string) bool {
	switch scope {
	case extwire.ScopeAgentsRead, extwire.ScopeAgentsControl, extwire.ScopeAgentsPermissionMode, assistantwire.ScopeA2A:
		return true
	default:
		return false
	}
}

func scopedPermissionDenied(w http.ResponseWriter, scope string) {
	body := errorBody(http.StatusForbidden, "permission_denied", errors.New("this credential cannot access this part of the hub API"))
	if scope != "" {
		body["scope"] = scope
	}
	writeErrorBody(w, http.StatusForbidden, body)
}

func (h *Hub) authorizeScopedDevice(w http.ResponseWriter, r *http.Request, p principal) bool {
	scope := h.scopedDeviceRoute(r)
	if deviceAgentScope(scope) && slices.Contains(p.Scopes, scope) {
		return true
	}
	scopedPermissionDenied(w, scope)
	return false
}

func scopedSendNeedsPermissionMode(s proto.Session, fields map[string]any) bool {
	for key := range fields {
		if strings.EqualFold(key, "permission_mode") {
			return true
		}
	}
	switch s.Status {
	case proto.StatusIdle, proto.StatusWorking, proto.StatusBlocked, proto.StatusDone, proto.StatusInterrupted:
		return false
	}
	return true
}

type scopedSessionSummary struct {
	SessionID string `json:"session_id"`
	MachineID string `json:"machine_id"`
	Title     string `json:"title"`
	Agent     string `json:"agent,omitempty"`
	Status    string `json:"status"`
}

type scopedChatItem struct {
	ID   string    `json:"id"`
	Seq  int       `json:"seq"`
	TS   time.Time `json:"ts"`
	Role string    `json:"role"`
	Kind string    `json:"kind"`
	Text string    `json:"text,omitempty"`
}

// Scoped sockets can follow conversations and check connectivity.
func scopedSocketCommand(command string) bool {
	switch command {
	case "subscribe", "unsubscribe", "ping":
		return true
	default:
		return false
	}
}

// scopedSocketEvent recognizes frames supported by a scoped chat socket.
func scopedSocketEvent(event string) bool {
	switch event {
	case "hello", "pong", "sessions", "items", "delta", "live":
		return true
	default:
		return false
	}
}

// Scoped sockets never replay the owner event log. Project the initial session
// snapshot and the connection's subscribed chat frames into their public shape.
func (c *client) scopedSocketFrame(data []byte) []byte {
	var frame map[string]json.RawMessage
	if json.Unmarshal(data, &frame) != nil {
		return nil
	}
	var kind string
	if json.Unmarshal(frame["type"], &kind) != nil || !scopedSocketEvent(kind) {
		return nil
	}
	out := map[string]any{"type": kind}
	copyFields := func(keys ...string) {
		for _, key := range keys {
			if value, exists := frame[key]; exists {
				out[key] = value
			}
		}
	}
	switch kind {
	case "hello":
		copyFields("server_version", "protocol", "client_id")
	case "pong":
		copyFields("id")
	case "sessions":
		var rows []proto.Session
		if json.Unmarshal(frame["sessions"], &rows) != nil {
			return nil
		}
		summaries := make([]scopedSessionSummary, 0, len(rows))
		for _, row := range rows {
			if !spaceSession(&row) && !row.Archived {
				summaries = append(summaries, scopedSessionSummary{row.SessionID, row.MachineID, row.Title, row.Agent, row.Status})
			}
		}
		out["sessions"] = summaries
		copyFields("machine_id")
	default:
		var mid, sid string
		if json.Unmarshal(frame["machine_id"], &mid) != nil || json.Unmarshal(frame["session_id"], &sid) != nil ||
			!c.hub.scopedPublicSession(mid, sid) {
			return nil
		}
		c.mu.Lock()
		subscribed := c.subs[[2]string{mid, sid}]
		c.mu.Unlock()
		if !subscribed {
			return nil
		}
		copyFields("machine_id", "session_id")
		switch kind {
		case "items":
			var items []proto.Item
			if json.Unmarshal(frame["items"], &items) != nil {
				return nil
			}
			chat := make([]scopedChatItem, 0, len(items))
			for _, item := range items {
				visible := scopedChatItem{ID: item.ID, Seq: item.Seq, TS: item.TS, Role: item.Role, Kind: item.Kind}
				if item.Kind == "text" && (item.Role == "user" || item.Role == "assistant") && item.Injected == nil && item.Source == nil {
					visible.Text = item.Text
				}
				chat = append(chat, visible)
			}
			out["items"] = chat
			copyFields("history", "from_seq", "to_seq", "backlog", "has_more", "more_after")
		case "delta":
			copyFields("text")
		case "live":
			var live *proto.SessionLive
			if json.Unmarshal(frame["live"], &live) != nil {
				return nil
			}
			out["live"] = live
		}
	}
	projected, _ := json.Marshal(out)
	return projected
}

func validDeviceScopes(scopes []string) bool {
	if len(scopes) == 0 {
		return false
	}
	for index, scope := range scopes {
		if !deviceAgentScope(scope) || slices.Contains(scopes[:index], scope) {
			return false
		}
	}
	return true
}
