Close, send back or hand up one of the worker tasks you handed out with task_plan. You get this tool only as a task leader, and it reaches your own direct workers only (any other task is refused). accepted: the work meets its done_when (you checked the evidence): the task closes. With a nonempty current done_when, accepted requires exactly one check per current criterion, each met=true with nonblank evidence. Missing, duplicate, unknown or unmet checks and blank evidence are refused before any review or completion is written. cancelled and workers without done_when keep their existing behavior. rework: say what is missing (note, or checks that are not met): stalled/low-ROI churn (repeated_error, low_roi_edge, or omitted rework_kind) caps at 2 rounds, while external_input/substantive_new cap at 10 total, after which it goes up as a decision; prefer accepted with a brief note over low-ROI edge cases once core criteria are met. needs_user: only for a decision that is not yours: reason says what to decide and recommend what you would pick; it goes to whoever handed you your task, and nothing is shown to the user from here. cancelled: not to be done: the task closes and its agent is closed. A worker's supervision message tells you when one needs this.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "todo_id",
    "verdict"
  ],
  "additionalProperties": false,
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "the worker's task id, as the supervision message and task_plan's answer give it"
    },
    "verdict": {
      "type": "string",
      "enum": ["accepted", "rework", "needs_user", "cancelled", "findings"]
    },
    "findings_review": {
      "type": "object",
      "description": "verdict=findings: omit to read; supply the displayed version to confirm and record dispositions without moving task state.",
      "required": ["version"],
      "properties": {
        "version": {"type":"integer","minimum":1},
        "dispositions": {
          "type":"array",
          "items": {
            "type":"object",
            "required":["id"],
            "properties": {
              "id":{"type":"string"},
              "follow_up_todo_id":{"type":"string","description":"Supervised to-do with the exact next action in done_when."},
              "needs_user_todo_id":{"type":"string","description":"Child to-do with a needs_user card whose reason is the exact next action."},
              "wait_target":{"type":"string","description":"A single finding may match the park evidence's target."},
              "done_evidence":{"type":"string","description":"Evidence that this finding was resolved."},
              "owner_drop_decision_id":{"type":"string","description":"Owner's manual decision, question 'Drop investigation finding <id>?', choice 'Drop', with reason."}
            }
          }
        }
      }
    },
    "rework_kind": {
      "type": "string",
      "enum": ["external_input", "substantive_new", "repeated_error", "low_roi_edge"],
      "description": "rework, optional: external_input (external review/conflict/owner input; skips 2-round stalled cap), substantive_new (prior gap fixed, new defect found; resets stalled cap), repeated_error/low_roi_edge (default when omitted; cap 2 stalled, 10 total)"
    },
    "note": {
      "type": "string",
      "description": "rework: what is missing, written to the worker; accepted and cancelled: one line of why"
    },
    "checks": {
      "type": "array",
      "maxItems": 6,
      "items": {
        "type": "object",
        "required": ["criterion", "met"],
        "additionalProperties": false,
        "properties": {
          "criterion": {"type": "string", "description": "one of the task's done_when"},
          "met": {"type": "boolean"},
          "evidence": {"type": "string", "description": "what shows it, or what is missing"}
        }
      },
      "description": "each done_when with whether it is met, and the evidence"
    },
    "reason": {
      "type": "string",
      "description": "needs_user: one sentence on what is to be decided"
    },
    "recommend": {
      "type": "string",
      "description": "needs_user: the option you recommend"
    }
  }
}
```
