{{if eq .Lang "en"}}Main or a direction lead can adopt an Agent with no open task. A leader needs done_when and a non-work-trust computer. Use task_scope to revise an existing task and agent_transfer to transfer supervision.{{else if eq .Lang "ja"}}メイン会話か担当分野のリーダーは、進行中のタスクがない Agent を監督できます。leader には done_when と仕事用以外のコンピュータが必要です。変更には task_scope、移管には agent_transfer を使います。{{else}}主对话或方向主管可收编无进行中任务的 Agent。leader 需 done_when 和非工作信任电脑。改范围用 task_scope，移交用 agent_transfer。{{end}}{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "machine",
    "session_id"
  ],
  "properties": {
    "machine": {
      "type": "string",
      "description": "agents_list machine id or name"
    },
    "session_id": {
      "type": "string",
      "description": "Agent session id or first 8 characters"
    },
    "title": {
      "type": "string",
      "description": "{{if eq .Lang "en"}}English title under 20 characters. Defaults to the Agent title.{{else if eq .Lang "ja"}}短いタスク名（日本語、20文字未満）。空の場合は Agent のタイトルを使います{{else}}Chinese title under 20 chars. Defaults to Agent title.{{end}}"
    },
    "role": {
      "type": "string",
      "enum": ["leader"]
    },
    "work_mode": {
      "type": "string",
      "enum": ["execute", "research", "coordinate"],
      "description": "Default execute."
    },
    "completion_mode": {
      "type": "string",
      "enum": ["inherit", "draft", "ready", "landed", "verified"]
    },
    "done_when": {
      "type": "array",
      "maxItems": 6,
      "items": {
        "type": "string"
      },
      "description": "checkable completion criteria"
    },
    "expect_minutes": {
      "type": "integer",
      "description": "estimated minutes from now"
    }
  }
}
```
