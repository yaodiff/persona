Atomically merge an open source to-do into an open target to-do under todos_v2: transfers repository/PR/issue links, active blockers, and decisions, reparents children without cycles, rewrites downstream after edges, appends provenance notes, and closes the source to-do. Refuses when source has an active agent unless force_with_active_agent=true. Explicit topology primitive only; requires todos_v2=true.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "target_id",
    "source_id"
  ],
  "properties": {
    "target_id": {
      "type": "string",
      "description": "Open surviving target to-do id."
    },
    "source_id": {
      "type": "string",
      "description": "Open source to-do id to merge into target and close."
    },
    "force_with_active_agent": {
      "type": "boolean",
      "description": "Allow merging when source has an active agent (default false)."
    }
  },
  "additionalProperties": false
}
```
