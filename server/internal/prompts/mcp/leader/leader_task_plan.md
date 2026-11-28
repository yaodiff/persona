Hand part of your own task to workers: each task you give this starts a new coding agent on a computer, beneath the task you lead, at once, with no card for the user to start it. You get this tool only because your task was handed out as a leader task. Workers belong to this task only: you cannot pick another to-do (todo_id is refused, and every task sets role=worker), a worker cannot hand work on, and at most 8 workers of this task run at a time. A worker that changes files takes the default permission mode unless you set one; a task that only reads sets readonly true and gets the read-only constraint in its first message. Claude-family launchers always start in bypassPermissions mode; Codex read-only tasks keep the plan-mode sandbox. Leave machine and cwd out to start the worker where you run. What a worker reports, and when it stops, stalls or waits for review, reaches you as a message that starts with "Persona · supervision"; your own reports still go to whoever handed your task to you. You supervise each worker: give it done_when, and review it with task_review when its supervision message comes. Returns at once, with each worker's to-do; it does not confirm the agent started.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "tasks"
  ],
  "additionalProperties": false,
  "properties": {
    "tasks": {
      "type": "array",
      "minItems": 1,
      "maxItems": 8,
      "items": {
        "type": "object",
        "required": [
          "title",
          "prompt",
          "role",
          "readonly"
        ],
        "additionalProperties": false,
        "properties": {
          "title": {
            "type": "string",
            "description": "the worker's task in a short line"
          },
          "prompt": {
            "type": "string",
            "description": "the worker's first message: everything it needs to do the work without asking you"
          },
          "role": {
            "type": "string",
            "enum": ["worker"],
            "description": "required: worker; a worker cannot hand work on"
          },
          "machine": {
            "type": "string",
            "description": "machine id; empty: the computer you run on"
          },
          "machine_policy": {
            "type": "string",
            "enum": ["fixed", "flexible"],
            "description": "fixed (default): keep this machine; flexible: a failed launcher health check may move it to another computer"
          },
          "cwd": {
            "type": "string",
            "description": "the project directory: absolute path or ~/…; empty: your own directory when the worker runs on your computer"
          },
          "launcher": {
            "type": "string",
            "description": "launcher name, one that machine has; empty: the default"
          },
          "host": {
            "type": "string",
            "description": "agy (companion) or an enabled engine such as herdr. Overrides the preset host."
          },
          "model": {
            "type": "string",
            "description": "model id; empty: the launcher's default"
          },
          "tier": {
            "type": "string",
            "description": "an Agent preset (a tier of the model profile in use); explicit launcher, model, effort and speed override it"
          },
          "speed": {
            "type": "string",
            "enum": ["standard", "fast", "ultrafast"],
            "description": "new-start speed; explicit choice overrides the tier. standard/omitted: launcher default. Codex: fast, ultrafast; Claude Code: fast only"
          },
          "effort": {
            "type": "string",
            "description": "explicit effort override; omit to use the tier's"
          },
          "permission": {
            "type": "string",
            "enum": [
              "bypassPermissions",
              "plan"
            ],
            "description": "empty: the default mode for a writable task. Claude-family launchers always use bypassPermissions; readonly=true adds a first-prompt constraint; Codex keeps plan; TRAE CLI and omp take none"
          },
          "multi_issue_report": {"type":"boolean","description":"Only for a report investigating several issues; requires a findings list before park or close."},
          "readonly": {
            "type": "boolean",
            "description": "true when the worker only reads (looks things up, reads code, changes no project files); false when it changes files. The first message automatically adds the read-only constraint. Claude-family launchers use bypassPermissions; Codex keeps its plan sandbox"
          },
          "done_when": {
            "type": "array",
            "maxItems": 6,
            "items": {
              "type": "string"
            },
            "description": "what done means: results the worker must show, one each; the worker is told them"
          },
          "goal": {
            "type": "string",
            "description": "a read-only worker's goal: what you get from it, in one sentence (a writable worker needs done_when instead); estimates are automatic for both"
          },
          "distinct_from": {
            "type": "array",
            "items": {
              "type": "object",
              "required": ["todo_id", "reason"],
              "additionalProperties": false,
              "properties": {
                "todo_id": {"type": "string"},
                "reason": {"type": "string", "minLength": 1}
              }
            },
            "description": "required for each open todo whose saved repository issue/PR link this worker references: its todo_id and why this is separate work, kept in the brief"
          },
          "expect_minutes": {
            "type": "integer",
            "minimum": 0,
            "description": "optional minutes override; omit/0 for comparable history or the 60-minute system_default. Review at 1×; stuck at 1.5× (min 20m); overdue at 3× (min 24h)"
          },
          "why": {
            "type": "string",
            "description": "optional: one line on why this machine and directory"
          }
        }
      }
    }
  }
}
```
