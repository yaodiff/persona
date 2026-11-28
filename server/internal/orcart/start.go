package orcart

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Starting a chat (#17047): agent.launch with the chat surface (Orca 1.4.277, CapChatLaunch) opens a chat tab in the
// background with the first message and options, as Orca's own New chat does; agentSession.create (CreateSupport) is
// the structured way, whose first message can carry pictures. Both take the session id the caller names, so a start
// tried again finds the chat it made instead of making another. Checked on Orca 1.4.277
// (docs/integrations/orca.md "Structured sessions").

// CapChatLaunch: agent.launch takes surface "chat".
const CapChatLaunch = "agent.launch.surface.v1"

// InstalledAgents is preflight.installedAgents: the agents Orca found on this computer (its provider ids).
func (c *Client) InstalledAgents(ctx context.Context) ([]string, error) {
	var res struct {
		Agents []string `json:"agents"`
	}
	if err := c.Call(ctx, CallTimeout, "preflight.installedAgents", map[string]any{}, &res); err != nil {
		return nil, err
	}
	return res.Agents, nil
}

// ChatAgents is agentSession.agents: the providers Orca runs as chats on this computer (its provider ids; Orca 1.4.277,
// method_not_found on an older one). It does not say whether Orca can open one now: a provider whose chat runs on a
// runtime Orca installs itself (its "managedRuntime", the same before and after the install) opens only once that
// runtime is there, which Orca tells streaming clients only (agentSession.subscribeAvailability,
// managedRuntime.subscribe); the local socket refuses those (method_not_supported, measured on 1.4.277), so
// ChatRuntimeMissing is how a caller learns it.
func (c *Client) ChatAgents(ctx context.Context) ([]string, error) {
	var res struct {
		Agents []struct {
			Agent string `json:"agent"`
		} `json:"agents"`
	}
	if err := c.Call(ctx, CallTimeout, "agentSession.agents", map[string]any{}, &res); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res.Agents))
	for _, a := range res.Agents {
		if a.Agent != "" {
			out = append(out, a.Agent)
		}
	}
	return out, nil
}

// ChatSurfaceUnavailable: err is agent.launch refusing to open the agent as a chat (agent_launch_surface_unavailable),
// with nothing launched; Orca's words say why and that it can start in a terminal.
func ChatSurfaceUnavailable(err error) bool { return IsCode(err, "agent_launch_surface_unavailable") }

// ChatRuntimeMissing: err is agent.launch refusing a chat because the provider's chat runtime is not installed on this
// computer (Orca 1.4.277's reason chat_runtime_not_installed, which its answer carries only in its words: "Claude can't
// open as a chat here because its chat runtime isn't installed on this host (install it from a new chat tab in
// Orca)…"). Every other reason (a project under WSL, a host that cannot run one there) is ChatSurfaceUnavailable alone.
func ChatRuntimeMissing(err error) bool {
	var e *Error
	return ChatSurfaceUnavailable(err) && errors.As(err, &e) && strings.Contains(strings.ToLower(e.Message), "chat runtime")
}

// CreateSupport is agentSession.createSupport: Orca can create a chat of agent in worktree (a selector, "id:<id>").
func (c *Client) CreateSupport(ctx context.Context, worktree, agent string) (bool, error) {
	var res struct {
		Supported bool `json:"supported"`
	}
	if err := c.Call(ctx, CallTimeout, "agentSession.createSupport", map[string]any{"worktree": worktree, "agent": agent}, &res); err != nil {
		return false, err
	}
	return res.Supported, nil
}

// LaunchChat is agent.launch of a chat of agent named sessionID in worktree (a selector, "id:<id>"), in the background
// (Orca's window keeps what it shows), with prompt as its first message when there is one and options (Orca's option
// keys: permissionMode, model, effort…) set before it runs. operationID is Orca's launch operation (OperationID): the
// same one replays the launch. A session id Orca already has is refused (agent_launch_session_already_exists).
func (c *Client) LaunchChat(ctx context.Context, sessionID, operationID, worktree, agent, prompt string, options map[string]string) error {
	params := map[string]any{"agent": agent, "operationId": operationID, "sessionId": sessionID, "surface": "chat",
		"presentation": "background", "target": map[string]any{"kind": "existing", "worktree": worktree}}
	if t := strings.TrimRight(prompt, " \t\r\n"); t != "" {
		params["prompt"] = map[string]any{"text": t, "delivery": "submit"}
	}
	if len(options) > 0 {
		params["chatOptions"] = options
	}
	return c.Call(ctx, WriteTimeout, "agent.launch", params, nil)
}

// CreateChat is agentSession.create of a chat of agent named sessionID in worktree (a selector), with its first
// message (text and pictures, files on this machine; none when both are empty, its id clientMsgID) and options. Its
// envelope names no fence: the session does not exist yet.
func (c *Client) CreateChat(ctx context.Context, sessionID, clientMsgID, worktree, agent, text string, images []string,
	options map[string]string) error {
	fields := map[string]any{"worktree": worktree, "agent": agent}
	var blocks []any
	if t := strings.TrimRight(text, " \t\r\n"); t != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": t})
	}
	for _, p := range images {
		blocks = append(blocks, map[string]any{"type": "image-ref", "path": p})
	}
	if len(blocks) > 0 {
		if clientMsgID == "" {
			clientMsgID = sessionID + "-first"
		}
		fields["firstMessage"] = map[string]any{"clientMessageId": clientMsgID,
			"body": map[string]any{"kind": "message", "role": "user", "blocks": blocks}}
	}
	if len(options) > 0 {
		fields["options"] = options
	}
	w := Write{SessionID: sessionID, OperationID: OperationID(time.Now(), "create:"+sessionID)}
	return c.write(ctx, "agentSession.create", "agentSession.create", w, nil, fields, nil)
}
