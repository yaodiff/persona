Atomically split an open to-do into 1-20 child to-dos under todos_v2, inheriting direction_id and project, wiring intra-batch after_indices and existing after prerequisites, optionally copying repository links as references and moving active blockers from the source to-do. Explicit topology primitive only; requires todos_v2=true.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "id",
    "children"
  ],
  "properties": {
    "id": {
      "type": "string",
      "description": "Open source to-do id to split."
    },
    "children": {
      "type": "array",
      "minItems": 1,
      "maxItems": 20,
      "items": {
        "type": "object",
        "required": [
          "title"
        ],
        "properties": {
          "title": {
            "type": "string",
            "description": "Short title for the child to-do."
          },
          "notes": {
            "type": "string",
            "description": "Optional details."
          },
          "priority": {
            "type": "string",
            "enum": [
              "high",
              "normal",
              "low"
            ],
            "description": "normal when omitted."
          },
          "after_indices": {
            "type": "array",
            "items": {
              "type": "integer",
              "minimum": 0
            },
            "description": "0-based indices of other non-cyclic children in this split batch that must precede this child."
          },
          "after": {
            "type": "array",
            "items": {
              "type": "string"
            },
            "description": "Existing sibling child to-do ids this child waits for."
          },
          "move_blocker_ids": {
            "type": "array",
            "items": {
              "type": "string"
            },
            "description": "Active blocker ids on the source to-do to move onto this child."
          },
          "copy_repo_links": {
            "type": "boolean",
            "description": "Copy saved repository links from source onto this child as reference links."
          }
        },
        "additionalProperties": false
      }
    },
    "append_audit_note": {
      "type": "boolean",
      "description": "Append a provenance note listing created child ids to the source to-do (default true)."
    }
  },
  "additionalProperties": false
}
```
