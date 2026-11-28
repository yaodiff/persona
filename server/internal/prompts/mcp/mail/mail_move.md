Move one email after owner approval. Use uid_validity from mail_search for its account. On partial or unknown results, check both mailboxes before retrying. Undo using the returned mailbox, uid and uid_validity.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "account_id": {"type": "string"},
    "mailbox": {"type": "string"},
    "uid": {"type": "integer", "minimum": 1},
    "uid_validity": {"type": "integer", "minimum": 1},
    "to": {"type": "string", "enum": ["archive", "junk", "trash", "inbox"]},
    "to_mailbox": {"type": "string"}
  },
  "required": ["account_id", "mailbox", "uid", "uid_validity"],
  "oneOf": [{"required": ["to"]}, {"required": ["to_mailbox"]}],
  "additionalProperties": false
}
```
