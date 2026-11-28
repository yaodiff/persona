Read the last 30 lines of a failed startup's kept tab while the start is being remedied. A supervision message says how long the tab is kept. Only the main conversation or the task's current supervising conversation may read it. See what stopped the shell or Agent. The screen stays in this conversation.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["todo_id"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "the task's id, as the 督办 message gives it"
    },
    "reason": {
      "type": "string",
      "description": "why you look, in the user's language (optional; kept with the task's history)"
    }
  }
}
```
