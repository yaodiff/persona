Record the owner's needs_user choice or user_check Pass ("Pass"); main chat or supervisor only. Quote their words and exact option; never guess. told_agent=true records only (already relayed); false also sends. Both settle the ask.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["todo_id", "option", "said"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "task id"
    },
    "option": {
      "type": "string",
      "description": "exact option label"
    },
    "said": {
      "type": "string",
      "description": "owner's words"
    },
    "told_agent": {
      "type": "boolean",
      "description": "true: already delivered to the Agent, record only; false: relay and record"
    }
  }
}
```
