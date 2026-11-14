Read-only: inspect the current assistant's existing agent_autoname confirmation cards and one coding agent's current naming snapshot. This does not prove that a particular earlier model call started, completed or succeeded: the model's tool-call id is not a manager-side correlation. The naming snapshot may predate the call or come from another caller, and background naming failures are not attributed to it. Do not retry a request from this result; unknown or ambiguous means the evidence cannot settle the outcome. Use only the exact machine and full session id from agents_list. The caller is verified from the actual assistant conversation; no other assistant's cards can be read. Optional since/before are an RFC3339 window of at most 24 hours, and must be supplied together. Confirmation history is read with bounded pages; incomplete pages are reported. This tool never calls the naming model, writes settings, approves or resolves a card, renames an agent, or publishes a session list. Not for Persona conversations, ordinary coding Agents, or extensions.

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["machine", "session_id"],
  "properties": {
    "machine": {
      "type": "string",
      "description": "the machine's exact id, as agents_list shows it"
    },
    "session_id": {
      "type": "string",
      "description": "the agent's exact full session id, never a prefix"
    },
    "since": {
      "type": "string",
      "format": "date-time",
      "description": "inclusive RFC3339 start of the original confirmation window; requires before"
    },
    "before": {
      "type": "string",
      "format": "date-time",
      "description": "exclusive RFC3339 end of the original confirmation window [since,before); at most 24 hours after since"
    }
  }
}
```
