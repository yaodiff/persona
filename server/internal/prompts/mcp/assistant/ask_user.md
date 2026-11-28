Ask multiple-choice questions with explicit decision rights. Use decision_scope=internal for execution choices or recommendations: a designated supervisor is woken and answers as the assistant. Owner decisions require why_owner or owner_action. For a personal unlock, use owner_action=personal_unlock and let the owner act in their own app; never request a password or private key. Owners can also type an answer. Default wait=true holds this turn; a user message withdraws a waiting question, and a queued message can release the turn after two minutes. With wait=false, return at once, end the turn without guessing, and receive one later receipt with the answer, cancellation or expiry, including after restart. Questions wait up to two hours, or until deadline. An unanswered internal question authorizes nothing. Owner deadline defaults require reversible=true and recommend on every question; outward, spending, personal and irreversible actions never take a default.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["questions"],
  "properties": {
    "decision_scope": {
      "type": "string",
      "enum": ["internal", "owner"],
      "description": "Internal execution questions go to a supervisor. Owner questions require why_owner or owner_action. Omitting all decision-right metadata is rejected."
    },
    "why_owner": {
      "type": "string",
      "enum": ["priority", "acceptance", "only_owner_knows", "visible_to_others", "irreversible"],
      "description": "Why this decision requires the owner; omit for internal execution questions"
    },
    "owner_action": {
      "type": "string",
      "enum": ["personal_unlock", "login", "second_factor", "experience_check", "spending"],
      "description": "An action only the owner can perform; never include credentials"
    },
    "wait": {
      "type": "boolean",
      "description": "default true: hold this turn until the user answers. false: put the card up and return at once; the answer arrives later as a new message in this conversation"
    },
    "deadline": {
      "type": "string",
      "description": "optional: until when the questions wait, a duration (\"30m\", \"4h\") or an RFC 3339 time, 1 minute to 30 days ahead; needs reversible and a recommend on every question"
    },
    "reversible": {
      "type": "boolean",
      "description": "with deadline: whether going ahead with the recommended options can be undone later. false for deleting, sending anything to other people, spending money or changing production: those are never taken by default"
    },
    "questions": {
      "type": "array",
      "minItems": 1,
      "maxItems": 4,
      "items": {
        "type": "object",
        "required": ["question", "options"],
        "properties": {
          "question": {
            "type": "string"
          },
          "header": {
            "type": "string",
            "description": "short label, ≤12 chars"
          },
          "multiSelect": {
            "type": "boolean"
          },
          "recommend": {
            "type": "string",
            "description": "the label of the option you recommend; required with deadline"
          },
          "options": {
            "type": "array",
            "minItems": 2,
            "maxItems": 4,
            "items": {
              "type": "object",
              "required": ["label"],
              "properties": {
                "label": {
                  "type": "string"
                },
                "description": {
                  "type": "string"
                }
              }
            }
          }
        }
      }
    }
  }
}
```
