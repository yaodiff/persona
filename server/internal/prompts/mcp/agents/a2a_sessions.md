List independent Persona peer sessions enrolled for A2A communication, optionally on one machine (subagents are excluded from discovery). Use the exact machine_id and session_id when messaging an existing session, or call a2a_send with machine_id and cwd (and optional launcher or tier) to start a new cross-machine or cross-harness subagent. With presets true the answer is {sessions, computers}: the computers a2a_send can start a subagent on, yours included, each with the Agent presets (tier) it can run, what each runs and when to use it.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "machine_id": {
      "type": "string"
    },
    "presets": {
      "type": "boolean"
    }
  }
}
```
