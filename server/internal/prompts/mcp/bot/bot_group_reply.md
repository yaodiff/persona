Post the one reply to a teammate's question in a Lark group, on behalf of the owner's bot. Call it at most once in a turn.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["text"],
  "properties": {
    "text": {
      "type": "string",
      "description": "The reply, in the question's language, within the length the request allows."
    }
  }
}
```
