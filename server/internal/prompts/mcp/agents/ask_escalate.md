Hand the cards a task's agents wait on to the user, when a 督办 message gave them to you first and the user must decide: a tool permission or a plan (no conversation approves those), or a question that is irreversible, seen by other people, changes the goal or goes against a decision already made on the task. Every card of the task the user has not been told of goes to them at once, as one reminder (in the user's low-distraction mode it pushes only when why_owner or p0 says it is theirs alone), with your reason and recommendation on it; their answers go straight to the agents and are kept as the task's decisions. Only the task's supervisor and the main conversation can. A leader's escalation is told to the main conversation too, so don't ask the user the same thing again yourself.

```json
{
  "type": "object",
  "required": ["todo_id", "reason"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "the task's todo id (todo_…), from the 督办 message"
    },
    "reason": {
      "type": "string",
      "description": "in a sentence, in the user's language: what they are to decide and why it is theirs"
    },
    "recommend": {
      "type": "string",
      "description": "the option you recommend, in a line (an option's label when there is one)"
    },
    "why_owner": {
      "type": "string",
      "enum": ["irreversible", "visible_to_others", "priority", "acceptance", "only_owner_knows"],
      "description": "why only the user can decide it. In the user's low-distraction mode only irreversible, visible_to_others and only_owner_knows push to their phone; the rest wait in their Needs you list and briefing"
    },
    "p0": {
      "type": "boolean",
      "description": "true: the user must do or decide this themselves now, so it pushes even in low-distraction mode whatever why_owner says"
    }
  }
}
```
