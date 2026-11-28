Record agreed work or reminders now; run work via task_plan. Open title/due duplicates return the existing to-do; edit its bindings via todo_repositories_get/set. Leader chats record work in their direction and complete unassigned work via todo_complete. Bindings are atomic, using repositories_list IDs and revision. No file or progress changes. A result naming an active related Agent means reuse it, not a new dispatch.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "title"
  ],
  "properties": {
    "title": {
      "type": "string",
      "description": "what is to be done, in a line, in the user's language"
    },
    "notes": {
      "type": "string",
      "description": "Details."
    },
    "topic_message_id": {
      "type": "string",
      "description": "Lark message ID supplied in the current prompt."
    },
    "due": {
      "type": "string",
      "description": "YYYY-MM-DD, YYYY-MM-DD HH:MM (local), RFC3339, or 2h/3d."
    },
    "remind": {
      "type": "string",
      "description": "Future reminder (local/RFC3339 or 30m/2h/1d). Only if asked or urgent."
    },
    "priority": {
      "type": "string",
      "enum": [
        "high",
        "normal",
        "low"
      ],
      "description": "normal when left out"
    },
    "kind": {
      "type": "string",
      "enum": [
        "todo",
        "concern"
      ],
      "description": "todo (default); concern (main chat only) closes only via concern_report."
    },
    "audience": {
      "type": "string",
      "enum": [
        "human",
        "autonomous"
      ],
      "description": "Optional: human or autonomous."
    },
    "direction_id": {
      "type": "string",
      "description": "Optional direction id."
    },
    "project": {
      "type": "string",
      "description": "Existing kb_list project slug/name; only when clear, no near-duplicates."
    },
    "part_of": {
      "type": "string",
      "description": "Request to-do id this is a piece of; hand out via task_plan todo_id."
    },
    "after": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "description": "With part_of: sibling piece ids it waits for."
    },
    "repository_bindings": {
      "type": "object",
      "required": [
        "catalog_revision",
        "repositories"
      ],
      "properties": {
        "catalog_revision": {
          "type": "integer",
          "minimum": 0
        },
        "repositories": {
          "type": "array",
          "maxItems": 20,
          "items": {
            "type": "object",
            "required": [
              "repo_id"
            ],
            "properties": {
              "repo_id": {
                "type": "string"
              },
              "directory_alias": {
                "type": "string"
              },
              "requested_ref": {
                "type": "object",
                "properties": {
                  "kind": {
                    "type": "string",
                    "enum": [
                      "default",
                      "branch",
                      "tag",
                      "commit",
                      "ref"
                    ]
                  },
                  "value": {
                    "type": "string",
                    "minLength": 1
                  }
                },
                "additionalProperties": false,
                "required": [
                  "kind"
                ],
                "oneOf": [
                  {
                    "type": "object",
                    "properties": {
                      "kind": {
                        "type": "string",
                        "const": "default"
                      }
                    }
                  },
                  {
                    "required": [
                      "value"
                    ]
                  }
                ]
              },
              "links": {
                "type": "array",
                "items": {
                  "type": "object",
                  "properties": {
                    "url": {
                      "type": "string",
                      "minLength": 1
                    },
                    "kind": {
                      "type": "string",
                      "enum": [
                        "issue",
                        "pull_request"
                      ]
                    },
                    "number": {
                      "type": "string",
                      "pattern": "^[1-9][0-9]{0,19}$"
                    },
                    "title": {
                      "type": "string",
                      "description": "Plain source title; trim; ≤1024 UTF-8 bytes.",
                      "maxLength": 1024
                    }
                  },
                  "additionalProperties": false,
                  "oneOf": [
                    {
                      "required": [
                        "url"
                      ],
                      "not": {
                        "anyOf": [
                          {
                            "required": [
                              "kind"
                            ]
                          },
                          {
                            "required": [
                              "number"
                            ]
                          }
                        ]
                      }
                    },
                    {
                      "required": [
                        "kind",
                        "number"
                      ],
                      "not": {
                        "required": [
                          "url"
                        ]
                      }
                    }
                  ]
                },
                "maxItems": 50
              }
            },
            "additionalProperties": false
          }
        }
      },
      "additionalProperties": false
    }
  }
}
```
