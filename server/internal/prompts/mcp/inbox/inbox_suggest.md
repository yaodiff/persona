Hand in your suggestion for the inbox event this conversation handles (one at a time: a new one replaces the last), and the event's recap. It waits in the user's inbox; nothing goes out until they confirm, and then the program does exactly what it says (you never send it yourself). `kind`: `reply` — one message to send, `text` in the user's voice, ready to go out as it is (no blanks to fill in like 【…】: leave out what you could not find and say so in `note`; for a Feishu sheet / Base item `changes` proposes moving rows' status: ref, field and one of the values the item lists); `agent` — an Agent to start, `text` its instruction (`machine` and `cwd` optional); `todo` — a to-do for the user to add: `text` its title (clear, starting with the action, no chat or people names), `due` only when the message states a deadline: a day `2006-01-02` for all day, or a moment as RFC 3339 with its offset; leave it out when unsure and say so in `note`; the user can edit both and adds it with one tap (a row that came to a to-do counts as handled); `conclusion` — you dealt with it, `text` the conclusion, `outcome` `fyi` (worth knowing) or `done` (nothing to do). `handback: true` for one the user has to judge: their confirmation comes back here as an instruction instead of being carried out. `note`: one or two sentences on what you found and where. `recap`: the event's background, progress and where it is stuck (up to 600 characters), every time. `title`: what the matter is, without chat or person names; `summary`: one sentence on where things stand and who is waited on for what. Write both every time; items whose title comes from the source (mail, documents, PRs / issues, and the like) ignore `title`, and the tool says so. `related`: ids of inbox items in other chats or sources this one is about. `recap` without `kind` creates no suggestion, and may include `title`, `summary`, and `follow`. `follow` records waiting on the other side: `who` (empty means waiting for a reply), `next`, and `check_at` (RFC 3339 with a time zone) or `check_in` (such as `30m`, `4h`, or `2d`). `check_at` takes precedence; with neither, look again in `24h`; the resulting time is clamped to 30 minutes–14 days from now. `follow` is valid only without `kind`. `reported`: `{text, by}` records one externally visible action you or an Agent actually completed but the program did not observe, without verifying it; never use it for plans or actions not taken. `by` is `agent` or `assistant` (default `assistant`), and `text` is one sentence, kept on one line and clipped to 120 characters. It may be given alone or accompany `kind`, `recap`, `title`, `summary`, or `follow`; a filed item refuses it, and text identical to the item's latest `reported` entry is not recorded twice. Call it at the end of each turn. In a conversation the user handed an inbox item to (its context block names the item's id), pass that id as `item` and call it when you have something to write back to the item, and again whenever it changes (it also shows in that conversation as a card); it refuses an item not handed to this conversation or one the user already filed: answer in the chat then.

```json
{
  "type": "object",
  "properties": {
    "item": {
      "type": "string"
    },
    "kind": {
      "type": "string",
      "enum": ["reply", "agent", "conclusion", "todo"]
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
