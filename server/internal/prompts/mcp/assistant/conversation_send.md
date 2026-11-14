Send a message to another Persona conversation (not this one). If it is running, the message waits until its current turn ends; if the user is talking in it, until they have been quiet for 2 minutes. The result says which. The message arrives marked as coming from this conversation, so the other side can answer with conversation_send. Conversations passing messages back and forth are limited (a few requests from the user's message, a few messages per pair in ten minutes): your first message back to the conversation whose message you are working on is its answer and adds no hop. Don't reply just to acknowledge. An organization policy may stop some conversations from forwarding work to the main or a leader conversation; such a send is refused with the policy's reason. Passing on the owner's own words to the main conversation stays allowed: start the message with their verbatim words. Do not retry a refused send; follow what the refusal says instead.

```json
{
  "type": "object",
  "required": ["id", "text"],
  "properties": {
    "id": {
      "type": "string",
      "description": "full id or its first 8 characters"
    },
    "text": {
      "type": "string"
    }
  }
}
```
