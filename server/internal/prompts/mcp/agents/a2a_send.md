Send a text request to another Persona session, start a new cross-machine or cross-harness subagent, or supply input on an existing A2A task. Use a new message_id for each message and reuse it on retries. Set machine_id and session_id to message an existing independent peer session. Set machine_id and cwd (with optional launcher and permission_mode, defaulting to plan and capped at your own permission level) to launch a new subagent on the same or another enrolled computer; launched sessions are folded under your session rather than listed separately. Give tier to launch it on one of the user's Agent presets, which sets its launcher, model, effort and speed but not its permission mode; a2a_sessions with presets true lists each computer's presets, what they run and when to use them. For a reply set task_id. With wake enabled (the default), questions and results return to your session after your current turn ends. You can stop your turn while waiting.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "message_id",
    "text"
  ],
  "properties": {
    "machine_id": {
      "type": "string"
    },
    "session_id": {
      "type": "string"
    },
    "task_id": {
      "type": "string"
    },
    "context_id": {
      "type": "string"
    },
    "message_id": {
      "type": "string"
    },
    "text": {
      "type": "string",
      "maxLength": 12000
    },
    "wake": {
      "type": "boolean"
    },
    "cwd": {
      "type": "string"
    },
    "launcher": {
      "type": "string"
    },
    "tier": {
      "type": "string"
    },
    "permission_mode": {
      "type": "string"
    }
  }
}
```
