Start a new Persona conversation with an opening message and return its id at once (it does not wait for the answer). For work that deserves its own thread, when the user asks for one. The message arrives marked as coming from this conversation; it is named after its first turn like any new conversation. It is told to send its result back with conversation_send; if a turn of it fails, or ends without it ever having sent anything back, Persona leaves one notice here (once per conversation). An organization policy may keep some conversations read-only, including starting another conversation from them: such a call is refused with the policy's reason. Do not retry it; follow what the refusal says instead.

```json
{
  "type": "object",
  "required": ["text"],
  "properties": {
    "text": {
      "type": "string",
      "description": "the opening message: everything the new conversation needs, it does not see this one"
    }
  }
}
```
