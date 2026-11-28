Retry a failed task in its kept tab after clearing the blocker, or reopen it in a new tab. An optional launcher uses the same computer and workspace. The main conversation may retry any task. Other conversations may retry only tasks they supervise. Up to 3 manual retries. Returns at once. Look for the result in supervision. The task history records the reason.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["todo_id", "reason"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "the task's id, from the 督办 message"
    },
    "reason": {
      "type": "string",
      "description": "why it should start now, in the user's language, one sentence"
    },
    "launcher": {
      "type": "string",
      "description": "optional: another launcher of that computer, as the 督办 message lists"
    },
    "model": {
      "type": "string",
      "description": "with launcher: a model id (default: its own)"
    }
  }
}
```
