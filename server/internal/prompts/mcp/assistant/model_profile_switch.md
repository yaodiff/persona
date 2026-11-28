Inspect first. Apply a profile to future model calls and Agent starts. Saves a rollback point.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "Saved profile ID"
    }
  },
  "additionalProperties": false,
  "required": [
    "id"
  ]
}
```
