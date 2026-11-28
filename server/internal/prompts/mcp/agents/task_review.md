accepted closes after delivery_gate (default merged: park verified open PRs; linked issues must close). user_check keeps the task and Agent open until owner Pass; the acceptance guards still apply. If opted in at Start, accepted done cleans the idle tab unless user_check; report closed/skipped/pending, never close tabs yourself. Rework caps stalled/low-ROI churn at 2 rounds (external_input/substantive_new: 10 total). Replace stopped Agents via agent_start. Park external waits with evidence.

For accepted (outcome=done), list each done_when once in checks (met=true, nonblank evidence); put extra work in also_done. Outcome=cannot, cancelled and criteria-free tasks keep prior rules. For uncovered scope or outstanding Closeout Missing/Waiting, use needs_user, or record the outstanding text in deferred_scope with an open linked follow_up_todo_id. For findings, confirm each disposition with verdict=findings before acceptance, including user_check.

Give accepted outcome/reason/link; pass direct PR/MR/doc URL in link for needs_user when available. The hub sends conclusion cards and passes verification unless user_check. needs_user can default to recommend at 24h/deadline; irreversible/visible_to_others only remind. Recheck at 24h. can_wait needs low cost, nobody blocked today and no deadline within 12h. “You decide”, “This isn't what I asked for” and “Tell me the background first” return to supervision. Clarify without repeating or acting irreversibly/externally.

A non-main reviewer of another supervisor's task may only accept/cancel; send gaps via agent_send. A leader's needs_user reaches main too. Cancel duplicates/unwanted tasks. Viewing alone permits untouched cancellation.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["todo_id", "verdict"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "the task's todo id (todo_…), from the 督办 message or the context snapshot"
    },
    "verdict": {
      "type": "string",
      "enum": ["accepted", "rework", "needs_user", "parked", "cancelled", "findings"]
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
    "checks": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["criterion", "met"],
        "properties": {
          "criterion": {"type": "string", "description": "one done_when criterion (or what the task was for, when it had none)"},
          "met": {"type": "boolean"},
          "evidence": {"type": "string", "description": "proof it is met (link, command output), or what is missing"}
        }
      },
      "description": "each criterion with evidence/gap; updates state, evidence, time, and supervisor. Omitted entries keep progress"
    },
    "note": {
      "type": "string",
      "description": "{{if eq .Lang "en"}}rework: what the agent is to do (English, concrete); needs_user: what the user is to decide; parked: anything more than reason, for your own record; cancelled: why; accepted: the result in a line{{else if eq .Lang `ja`}}rework は Agent が行う具体的な作業を日本語で記述します。needs_user はユーザーが決めること、parked は reason 以外に記録する補足、cancelled は理由、accepted は結果を一行で記述します{{else}}rework: what the agent is to do (Chinese, concrete); needs_user: what the user is to decide; parked: anything more than reason, for your own record; cancelled: why; accepted: the result in a line{{end}}"
    },
    "outcome": {
      "type": "string",
      "enum": ["done", "cannot"],
      "description": "accepted: done (work complete or answer yes) or cannot (proved impossible); default done"
    },
    "reason": {
      "type": "string",
      "description": "one sentence in the user's language: accepted (why; default note's first line), needs_user (required: what to decide), or parked (required: what it waits for; include PR link if waiting on a PR)"
    },
    "waiting_on": {
      "type": "string",
      "enum": ["someone_else", "external_event", "date"],
      "description": "parked (required): someone_else (person acts/answers), external_event (outside event), or date. Owner decisions use needs_user; legacy owner_priority parks stay readable"
    },
    "until": {
      "type": "string",
      "description": "parked, optional: duration (\"3d\", \"36h\", \"90m\") or RFC 3339 time, 1m–30d ahead; default 24h. waiting_on=date requires explicit RFC 3339"
    },
    "link": {
      "type": "string",
      "description": "accepted/needs_user: direct PR/MR/doc URL when available (shown on decision card); defaults to task PR/MR, note/checks URL, or chat"
    },
    "recommend": {
      "type": "string",
      "description": "needs_user (required): recommended option label (first in options); owner OK sends it to the agent"
    },
    "why_owner": {
      "type": "string",
      "enum": ["irreversible", "visible_to_others", "priority", "acceptance", "only_owner_knows"],
      "description": "needs_user (required): irreversible (delete/prod write/spend), visible_to_others (external message/comment/review), priority (whether/order), acceptance (visual check), only_owner_knows (private fact); decide anything else yourself"
    },
    "options": {
      "type": "array",
      "minItems": 2,
      "maxItems": 4,
      "items": {
        "type": "object",
        "required": ["label", "leads_to"],
        "properties": {
          "label": {"type": "string", "description": "short button label"},
          "leads_to": {"type": "string", "description": "one sentence: what choosing it leads to"}
        }
      },
      "description": "needs_user (required): 2–4 options, recommended first"
    },
    "cost_of_waiting": {
      "type": "string",
      "description": "needs_user (required): one sentence on the cost of waiting"
    },
    "question_quote": {
      "type": "string",
      "description": "needs_user: the Agent's question verbatim when reason paraphrases it (hub checks for an existing reply)"
    },
    "can_wait": {
      "type": "boolean",
      "description": "needs_user, optional: defer to next daily briefing (low cost, nobody blocked today, no deadline <12h); no push/card, listed in Needs you. Ignored for irreversible/visible_to_others, deadline <12h, reversible=false, or no briefing with facts due within 24h and >=12h before deadline; default false"
    },
    "deadline": {
      "type": "string",
      "description": "needs_user, optional: how long the decision waits, duration (\"4h\") or RFC 3339 time, 1m–30d ahead; needs reversible. Default 24h reversible; none for why_owner irreversible/visible_to_others"
    },
    "also_done": {
      "type": "array",
      "maxItems": 6,
      "items": {"type": "string"},
      "description": "accepted, optional: what was done beyond the task's brief (one line each); the task's page lists it after the criteria"
    },
    "withdraw_needs_user": {
      "type": "boolean",
      "description": "accepted only, task in needs_user: its supervisor or main takes it back and closes it silently (no card/push)"
    },
    "user_check": {
      "type": "boolean",
      "description": "accepted only: user must inspect result (e.g. UI change); shows \"Please verify\" card awaiting \"Pass\" instead of auto-passing; default false"
    },
    "deferred_scope": {
      "type": "string",
      "maxLength": 600,
      "description": "accepted: quote outstanding Closeout text or describe deferred scope in the owner's language"
    },
    "follow_up_todo_id": {
      "type": "string",
      "description": "Open follow-up for deferred_scope, linked through part_of or after"
    },
    "reversible": {
      "type": "boolean",
      "description": "with deadline: whether taking recommend can be undone; false for delete/external send/spend/prod change (never auto-taken; required false for irreversible/visible_to_others)"
    },
    "evidence": {
      "type": "object",
      "required": ["kind", "target", "wake", "next_actor"],
      "properties": {
        "kind": {
          "type": "string",
          "enum": ["person_reply", "merge_review", "external_event", "date"],
          "description": "External wait kind"
        },
        "target": {"type": "string", "maxLength": 200, "description": "The person or external object"},
        "wake": {"type": "string", "maxLength": 600, "description": "Observable change that ends the wait"},
        "next_actor": {"type": "string", "enum": ["external", "owner"], "description": "external: third party, system or time, without a prior owner step. owner: use needs_user"},
        "url": {"type": "string", "maxLength": 2000, "description": "Required for merge_review (ready PR/MR) and external_event"},
        "interaction_id": {"type": "string", "maxLength": 200, "description": "external_event only. This worker's exact pending owner request ID from agents_list. Shows the existing request"},
        "pr_event": {
          "type": "string",
          "enum": ["terminal", "terminal_or_review"],
          "description": "GitHub PR waits only. terminal for merge or close. terminal_or_review adds new submitted reviews"
        },
        "contact": {
          "type": "object",
          "required": ["channel", "url", "sender", "sent_at", "request", "status"],
          "properties": {
            "channel": {"type": "string", "maxLength": 100},
            "url": {"type": "string", "maxLength": 2000, "description": "sent-message/receipt link, not a channel homepage"},
            "sender": {"type": "string", "maxLength": 200},
            "sent_at": {"type": "string", "format": "date-time", "description": "actual sent time; not in the future"},
            "request": {"type": "string", "maxLength": 600},
            "status": {"type": "string", "enum": ["sent"]}
          },
          "description": "Sent request, required for person_reply and merge_review"
        },
        "ready": {"type": "boolean", "enum": [true], "description": "merge_review only. The PR/MR is ready for the reviewer"},
        "reply": {
          "type": "object",
          "required": ["source", "brand", "app_id", "account_open_id", "person_open_id", "chat_id", "thread_id", "request_message_id"],
          "properties": {
            "source": {"type": "string", "enum": ["larkcli"]},
            "brand": {"type": "string", "enum": ["feishu", "lark"]},
            "app_id": {"type": "string"},
            "account_open_id": {"type": "string"},
            "person_open_id": {"type": "string", "description": "expected human OpenID in this app, not self"},
            "chat_id": {"type": "string"},
            "thread_id": {"type": "string", "description": "provider topic ID, not root-message ID"},
            "request_message_id": {"type": "string"}
          },
          "description": "person_reply: exact IDs from a configured lark-cli reader, never names. contact.url must be the sent request's matching topic AppLink. A verified reply asks the current supervisor to review, not direct Agent delivery"
        }
      },
      "description": "Required for parked. Inspect the source. recorded_by is output only. Missing legacy evidence is unverified"
    }
  }
}
```
