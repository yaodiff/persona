Send a message, as the user, to an Orca bot's current Home Chat (the bot from orca_bot_list). Only when the user asked to tell the bot something: the bot is an agent on that computer and acts on what it reads. It answers once Orca accepted the message, which is not the bot's reply: read that later with orca_bot_read, and never send again to retry. A bot that started a new Home Chat meanwhile is not followed with the message; read it again and send anew.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["bot", "text"],
  "properties": {
    "machine": {
      "type": "string",
      "description": "machine id or name; needed only when two computers have a bot of that name"
    },
    "bot": {
      "type": "string",
      "description": "the bot's id or name from orca_bot_list"
    },
    "text": {
      "type": "string",
      "description": "the message, as the user would write it to the bot"
    }
  }
}
```
