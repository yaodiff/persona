Withdraw a pending `ask_user` card of this conversation. Other chats' cards and user-owned approvals/plans/dialogs are rejected; already-ended own cards are idempotent.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["interaction_id"],
  "properties": {
    "interaction_id": {"type": "string"},
    "reason": {"type": "string"}
  }
}
```
