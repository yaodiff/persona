Send email after approval on the phone. Use a clear subject and body. For replies, pass the original Message-ID as in_reply_to and append it to the original References chain.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "account_id": {
      "type": "string"
    },
    "to": {
      "type": "array",
      "items": {
        "$ref": "#/$defs/address"
      }
    },
    "cc": {
      "type": "array",
      "items": {
        "$ref": "#/$defs/address"
      }
    },
    "bcc": {
      "type": "array",
      "items": {
        "$ref": "#/$defs/address"
      }
    },
    "subject": {
      "type": "string"
    },
    "text": {
      "type": "string",
      "description": "Plain-text body"
    },
    "html": {
      "type": "string",
      "description": "Optional HTML body"
    },
    "in_reply_to": {
      "type": "string",
      "description": "Message-ID being replied to"
    },
    "references": {
      "type": "string",
      "description": "Space-separated Message-IDs from the original thread"
    }
  },
  "required": ["account_id", "to", "subject", "text"],
  "$defs": {
    "address": {
      "type": "object",
      "properties": {
        "name": {
          "type": "string"
        },
        "email": {
          "type": "string"
        }
      },
      "required": ["email"]
    }
  },
  "additionalProperties": false
}
```
