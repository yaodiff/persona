Prepare one exact owner-approved batch for connector writes, managed persona_settings set/add/remove, and model_profile_switch, model_profile_rollback, model_profile_deactivate or model_profile_clear. Use it when the owner asks to review a batch. Put each complete call in items. The cards show redacted arguments and built-in previews. The cards authorize each call. After approval, call each item once in the returned order with its canonical arguments unchanged. Stop at the first error and read its partial result. Built-in calls recheck the approved display and revision. Denial executes nothing. Never retry an uncertain result or treat this as Always permission. Reads, capture, unknown tools, other runtime prompts and oversized items are refused.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["items"],
  "additionalProperties": false,
  "properties": {
    "items": {
      "type": "array",
      "minItems": 1,
      "maxItems": 20,
      "items": {
        "type": "object",
        "required": ["tool", "input"],
        "additionalProperties": false,
        "properties": {
          "tool": {
            "type": "string",
            "description": "Connector write, managed settings write or supported model profile action"
          },
          "input": {
            "type": "object",
            "description": "complete exact arguments, including every connector recipient/body field or built-in action, key, value and profile id"
          }
        }
      }
    }
  }
}
```
