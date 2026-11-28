Draft a reusable dashboard with view, from_view (own live card at the displayed version), or watch alone. The owner chooses Keep. Bind recurring refresh via schedule_routine.page_id this turn. Update replaces view/watch. On conflict, get first. Kept display is fixed. Changing alert disables alerts. Patch readings/items on kept pages. Ask before resuming paused/restored pages. Get includes readings/taps. Main lists all kept pages. Remove requires reason.{{template "shared/mcp_language" .}}

```json
{
  "type": "object", "required": ["action"], "additionalProperties": false,
  "properties": {
    "action": {"type": "string", "enum": ["create", "update", "patch", "get", "list", "remove"]},
    "id": {"type": "string"},
    "title": {"type": "string", "maxLength": 40, "description": "sidebar name, not built-in"},
    "icon": {"type": "string", "description": "chart, list, calendar, heart…"},
    "why": {"type": "string", "maxLength": 200},
    "view": {"type": "object", "description": "reply/open(https)/push.page/pop. No html/image."},
    "from_view": {
      "type": "object",
      "required": ["id", "version"], "additionalProperties": false,
      "properties": {"id": {"type": "string", "description": "view_show ID"}, "version": {"type": "integer", "minimum": 1}}
    },
    "watch": {"type": ["object", "null"], "description": "{question,display(number|status|table|trend),unit?,columns?,alert?:{when,value}}; null clears draft"},
    "reading": {"type": "object", "description": "{value}|{status,text?}|{rows}"},
    "items": {"type": "array", "items": {"type": "object"}, "description": "<=50 components replacing existing key+type"},
    "alert_text": {"type": "string", "maxLength": 120},
    "version": {"type": "integer"},
    "note": {"type": "string", "maxLength": 120, "description": "history line"},
    "reason": {"type": "string", "maxLength": 200}
  }
}
```
