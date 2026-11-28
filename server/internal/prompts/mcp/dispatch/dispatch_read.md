Read the subtasks you handed to other computers with dispatch_start. With no arguments: the user's other connected computers (id, name, whether they are online, home directory, the Agent presets each can start for dispatch_start's tier) and the tasks you dispatched with their states. With `session_id`: that task's state and its agent's last turn, which ends with its final reply when it has stopped. Give `wait_seconds` to wait for a working task to stop before answering (at most 50; call again while it still works, and do other work in between when you can). Only tasks you dispatched yourself can be read.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "session_id": {
      "type": "string",
      "description": "The A2A task ID, or the session ID dispatch_start returned. You can also use the first 8 characters of a session ID."
    },
    "wait_seconds": {
      "type": "integer",
      "description": "wait up to this long for a working task to stop; default 0, at most 50"
    }
  }
}
```
