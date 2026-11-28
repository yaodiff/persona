Read one Orca bot from orca_bot_list: its open items (what it keeps track of for the user: title, status, and the question and quick replies of one that waits on the user) and the last messages of its Home Chat, oldest first (only words; its tool calls stay in the bot's own chat). Changes nothing: to answer a bot's question, tell the user, or send the bot a message with orca_bot_send when the user asked for it.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["bot"],
  "properties": {
    "machine": {
      "type": "string",
      "description": "machine id or name; needed only when two computers have a bot of that name"
    },
    "bot": {
      "type": "string",
      "description": "the bot's id or name from orca_bot_list"
    },
    "messages": {
      "type": "integer",
      "description": "how many recent messages of its Home Chat to read (default 10, at most 40)"
    }
  }
}
```
