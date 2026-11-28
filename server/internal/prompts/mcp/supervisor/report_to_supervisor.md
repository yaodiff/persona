Report to the Persona assistant that handed you this task and reviews it (your supervisor), not to the user: a decision you need from it (question), that you are stuck (blocked), that you finished with the evidence for each acceptance criterion (done), or where you are in a long task (progress). It reads it within minutes, and answers or redirects you by message. Only for a task Persona supervises; elsewhere nobody receives it and it says so. Don't use it for every small step, and keep asking the user what only the user can decide the usual way. A question that does not hold you up (you can go on with other work, and a late answer costs nothing: which name to prefer, a style choice) can be sent with `urgent: false`: nobody is woken for it, and the owner sees it in their next morning or evening briefing, under "Can wait", and answers you in your chat. If no briefing is switched on it goes as an urgent question, and the answer says so. Ask what blocks you without it. `urgent` is for kind `question` only.

Report individual done_when entries as they change with `criteria`: `number` is the one-based position in the task's ordered done_when, `state` is pending / met / not_met, and evidence is required for met and not_met (a command plus output, a link or the gap). Omitted entries keep their previous state; pending resets an entry. This persists immediately and updates the task's count, but is an agent report, not the supervisor's acceptance. The supervisor receives the entries and evidence in its supervision message and still reviews the task itself.{{template "shared/mcp_language" .}}

For a to-do handoff, copy handoff_receipt from the request and report the head you checked. Use done with the result and evidence, or blocked for a failed check. The report goes to the originating to-do’s supervisor.

Use phase when the task changes state. For a wait, supply wait_state with its specific target and reason. A later phase other than waiting clears the wait. Phase done reports your work for review. Use delivery_pr for the task's actual delivery PR. GitHub and running-build checks establish merge and deployment.

```json
{
  "type": "object",
  "required": ["kind", "text"],
  "properties": {
    "kind": {
      "type": "string",
      "enum": ["progress", "question", "blocked", "done"]
    },
    "text": {
      "type": "string",
      "description": "what you report, in a few lines: the question with its options, what blocks you, what you did"
    },
    "evidence": {
      "type": "array",
      "maxItems": 8,
      "items": { "type": "string" },
      "description": "links, command output, file paths that show it (a PR, CI, a screenshot)"
    },
    "phase": {
      "type": "string",
      "enum": ["preparing", "working", "waiting", "reviewing", "done"]
    },
    "wait_state": {
      "type": "object",
      "required": ["waiting_on", "wait_target", "reason"],
      "properties": {
        "waiting_on": { "type": "string", "enum": ["user", "external", "dependency", "date"] },
        "wait_target": {
          "type": "object",
          "required": ["ref"],
          "properties": {
            "kind": { "type": "string", "enum": ["todo", "pr", "issue", "chat", "url", "time"] },
            "ref": { "type": "string", "description": "exact reference, or a name if kind is omitted" },
            "summary": { "type": "string" }
          }
        },
        "reason": { "type": "string" },
        "until": { "type": "string", "description": "RFC 3339 time, when known" }
      }
    },
    "delivery_pr": { "type": "string", "description": "full URL of the bound or owned delivery PR" },
    "handoff_receipt": {
      "type": "object",
      "required": ["todo_id", "id", "head", "check"],
      "additionalProperties": false,
      "properties": {
        "todo_id": { "type": "string" },
        "id": { "type": "string" },
        "head": { "type": "string" },
        "check": { "type": "string" }
      }
    },
    "findings": {
      "type":"array",
      "description":"For a multi-issue investigation done/blocked report, declare actionable findings; [] declares there are none. Keep IDs stable across reports.",
      "maxItems":30,
      "items":{
        "type":"object",
        "required":["id","summary","next","source"],
        "properties":{
          "id":{"type":"string","maxLength":48},
          "summary":{"type":"string","maxLength":200},
          "next":{"type":"string","maxLength":200},
          "source":{"type":"string","maxLength":600}
        }
      }
    },
    "criteria": {
      "type": "array",
      "maxItems": 6,
      "description": "only the done_when entries you are updating; does not accept or close the task",
      "items": {
        "type": "object",
        "required": ["number", "state"],
        "properties": {
          "number": { "type": "integer", "minimum": 1, "description": "one-based position in the task's done_when" },
          "state": { "type": "string", "enum": ["pending", "met", "not_met"] },
          "evidence": { "type": "string", "maxLength": 600, "description": "required for met/not_met; command + output, link, or what is missing" }
        }
      }
    },
    "step": {
      "type": "integer",
      "description": "the step you are on (progress)"
    },
    "steps": {
      "type": "integer",
      "description": "how many steps there are in all"
    },
    "urgent": {
      "type": "boolean",
      "description": "question only; default true. false: it can wait for the owner's next morning or evening briefing and you go on meanwhile; nobody is woken"
    }
  }
}
```
