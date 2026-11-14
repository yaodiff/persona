Rename one coding agent's session with the model when the user explicitly asks for Auto Rename. This replaces the current title, including a hand-written one, with a short title generated from its conversation; it does not change the terminal tab's title. The user must confirm this write on the phone every time; a declined or unanswered confirmation does nothing and makes no model call. The background automatic-naming switch and its once-only/placeholder restrictions do not block this explicit request. Do not use it on Persona conversations (including the main chat), on an agent the user has not asked to rename, or with a session-id prefix. Use the exact machine and session_id from agents_list. On success the JSON answer has title, title_source: auto and changed; on refusal it has the stable code and retryable fields. The HTTP endpoint used by client menus is separate and does not depend on this tool.

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["machine", "session_id"],
  "properties": {
    "machine": {
      "type": "string",
      "description": "the machine's exact id or name, as agents_list shows it"
    },
    "session_id": {
      "type": "string",
      "description": "the agent's exact full session id, never a prefix"
    }
  }
}
```
