Answer a card handed to you as supervisor or internal reviewer. Use exact machine/session_id/interaction_id from the wake-up; maintenance session_id may be empty. Internal cards may belong to assistant chats; never answer your own. Questions use offered labels (arrays for multi-select), terminal dialogs use labels/numbers, permissions/plans use allow and reason. The answer is the assistant's and becomes a task decision. Secrets/forms, outward messages, spending, irreversible and trust/Bypass approvals need the owner (ask_escalate for task cards). Other assistant cards stay unavailable.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["machine", "session_id", "interaction_id"],
  "properties": {
    "machine": {
      "type": "string",
      "description": "the machine's id or name, as agents_list shows it"
    },
    "session_id": {
      "type": "string",
      "description": "the agent's session id, or its first 8 characters"
    },
    "interaction_id": {
      "type": "string",
      "description": "the waiting interaction's id, from agents_list"
    },
    "answers": {
      "type": "object",
      "description": "each question's own wording → the option label to pick; a multi-select gives an array of labels",
      "additionalProperties": {
        "oneOf": [
          { "type": "string" },
          { "type": "array", "items": { "type": "string" } }
        ]
      }
    },
    "allow": {
      "type": "boolean",
      "description": "a permission or plan card: true allows (approves), false denies"
    },
    "reason": {
      "type": "string",
      "description": "in a sentence, why this answer (kept with the task's decision)"
    }
  }
}
```
