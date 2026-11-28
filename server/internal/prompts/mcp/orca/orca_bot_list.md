List the Orca bots on the user's computers (Orca's own assistants that live in a home folder and keep a Home Chat), read now from each computer's local Orca: each bot's computer, id, name, the agent it runs, what it is for, its Home Chat and when it was last active, how many of its items wait on the user (`needs_you`) and how many are open, and `session`, its Agents → Bots conversation in Persona. `machine` limits it to one computer. Changes nothing.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "machine": {
      "type": "string",
      "description": "machine id or name; all connected computers when omitted"
    }
  }
}
```
