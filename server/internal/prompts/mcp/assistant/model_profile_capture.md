With phone approval, save current model fields into a profile.{{template "shared/mcp_language" .}}

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
  "required": ["id"]
}
```
