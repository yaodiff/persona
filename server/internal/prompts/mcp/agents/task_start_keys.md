Answer a failed startup's shell or Agent prompt in its kept terminal tab while no Agent runs there. Only the main conversation or the task's current supervising conversation may act. Read the tab with task_start_screen before and after. Send up to 32 characters without Enter, then up to 8 named keys (enter, esc, ctrl+c, y, n, up, down). The keys, text and reason go in the task history. Use task_retry to start again.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["todo_id", "reason"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "the task's id, as the 督办 message gives it"
    },
    "text": {
      "type": "string",
      "maxLength": 32,
      "description": "text to type as is, before the keys"
    },
    "keys": {
      "type": "array",
      "maxItems": 8,
      "items": { "type": "string" },
      "description": "keys to press after the text, by name"
    },
    "reason": {
      "type": "string",
      "description": "why these keys, in the user's language, one sentence"
    }
  }
}
```
