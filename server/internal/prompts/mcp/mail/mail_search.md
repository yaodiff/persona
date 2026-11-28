Search mail summaries. Use account_id from mail_accounts. Single-account results include uid_validity for mail_move. Gmail accepts its own search syntax; others search full text. On no hits, scan recent headers for all words (case-insensitive) in subject, sender or recipients. Follow before_uid to scan older mail.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "account_id": {
      "type": "string",
      "description": "Omit for all inboxes"
    },
    "mailbox": {
      "type": "string",
      "description": "Default INBOX"
    },
    "query": {
      "type": "string"
    },
    "limit": {
      "type": "integer",
      "description": "Max results, default 20, max 200"
    },
    "before_uid": {
      "type": "integer",
      "description": "Page below this UID"
    }
  },
  "additionalProperties": false
}
```
