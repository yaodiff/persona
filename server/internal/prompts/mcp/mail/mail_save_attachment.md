Save one attachment (up to 25 MB) under attachments/mail on your computer and return its path. Use the attachment_id from mail_read. Existing names get a numbered suffix. Does not change read status.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "account_id": {
      "type": "string"
    },
    "uid": {
      "type": "integer"
    },
    "mailbox": {
      "type": "string",
      "description": "Mailbox name, default INBOX"
    },
    "attachment_id": {
      "type": "string",
      "description": "ID from mail_read"
    },
    "dir": {
      "type": "string",
      "description": "Subfolder to save into, relative (e.g. bills/2026-10); no absolute paths, '..' or hidden folders"
    }
  },
  "required": ["account_id", "uid", "attachment_id"],
  "additionalProperties": false
}
```
