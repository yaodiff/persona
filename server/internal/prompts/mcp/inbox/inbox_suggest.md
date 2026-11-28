Submit the event’s recap and one suggestion (replaces any earlier one). Nothing goes out until the user confirms; the program executes it verbatim. `kind`: `reply` — message in the user's voice ready to send (no 【…】 blanks; omit unknowns and explain in `note`; to really `@` someone in a connector that supports mentions such as Feishu, pass `mentions` as `[{"id":"<verified user id>","name":"<exact display name>"}]` from the source's search/member tool and write `@name` verbatim in `text` — `@name` without `mentions` is ordinary text; Feishu sheet/Base `changes`: ref, field, listed value); `agent` — new Agent instruction in `text` (`machine`, `cwd` optional); `handoff` — hand to an existing eligible conversation on this manager (main, active leader, or linked task) with `machine`, `session`, and `text` as the note (processing chat only; refuses archived, unknown, or other-manager targets); `todo` — to-do title in `text` (action-led, no chat/people names), `due` only for an explicit deadline (`2006-01-02` all day or RFC 3339 with offset; omit if unsure and note why; confirming files the row); `conclusion` — dealt with, `text` conclusion, `outcome` `fyi` or `done`. `handback: true` returns confirmation here as an instruction for user judgment. `note`: 1–2 sentences on findings/evidence. `recap`: background, progress, blockers (≤600 chars) every time. `title` (no chat/person names) and `summary` (1 sentence on state and who waits) every time; source-titled items (mail, docs, PRs/issues) ignore `title`. `related`: ids of related items in other chats/sources. `recap` without `kind` creates no suggestion and may include `title`, `summary`, and `follow`. `follow` (valid only without `kind`) waits on the other side: `who` (empty = other side's reply), `next`, `check_at` (RFC 3339 with zone) or `check_in` (`30m`, `4h`, `2d`; default `24h`, clamped 30m–14d). `reported`: `{text, by}` records one completed external action unobserved by the program (never plans; `by` `agent`|`assistant`, default `assistant`; one line ≤120 chars; refused on filed items; deduped against latest). Call at end of turn. In a handed-off chat (context names event id), pass `item` to write back (also shown as a card); refuses unhanded or filed items.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "processing_turn": {
      "type": "string"
    },
    "item": {
      "type": "string"
    },
    "kind": {
      "type": "string",
      "enum": ["reply", "agent", "handoff", "conclusion", "todo"]
    },
    "text": {
      "type": "string"
    },
    "note": {
      "type": "string"
    },
    "handback": {
      "type": "boolean"
    },
    "outcome": {
      "type": "string",
      "enum": ["fyi", "done"]
    },
    "recap": {
      "type": "string"
    },
    "reported": {
      "type": "object",
      "required": ["text"],
      "properties": {
        "text": {
          "type": "string"
        },
        "by": {
          "type": "string",
          "enum": ["agent", "assistant"],
          "default": "assistant"
        }
      }
    },
    "title": {
      "type": "string"
    },
    "summary": {
      "type": "string"
    },
    "follow": {
      "type": "object",
      "properties": {
        "who": {
          "type": "string"
        },
        "next": {
          "type": "string"
        },
        "check_at": {
          "type": "string",
          "format": "date-time"
        },
        "check_in": {
          "type": "string"
        }
      }
    },
    "machine": {
      "type": "string"
    },
    "session": {
      "type": "string"
    },
    "cwd": {
      "type": "string"
    },
    "due": {
      "type": "string"
    },
    "related": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "mentions": {
      "type": "array",
      "maxItems": 100,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["id", "name"],
        "properties": {
          "id": {
            "type": "string"
          },
          "name": {
            "type": "string"
          }
        }
      }
    },
    "changes": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["ref", "field", "value"],
        "properties": {
          "ref": {
            "type": "string"
          },
          "field": {
            "type": "string"
          },
          "value": {
            "type": "string"
          }
        }
      }
    }
  }
}
```
