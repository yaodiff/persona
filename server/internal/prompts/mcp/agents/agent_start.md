Start a new agent on one of the user's computers, in a project directory, with a first prompt. Use for real development work instead of doing it here. Check machines_list before choosing a machine. Returns the new session id. When the launcher failed its last health check on that machine, the agent starts on a machine where it works (a directory under the home moves to that home) and the answer says so. Claude-family launchers and Paseo providers with Auto Accept always start in bypassPermissions. TRAE CLI takes model and effort overrides. Omp takes no effort setting.

The launcher decides the agent: Claude Code's starts Claude Code; pi or cursor (or one listed as that agent) starts pi or the Cursor CLI in a herdr pane with the model you give; agy starts the Antigravity CLI on the computer's companion when connected, else in a pane. Those three take no permission mode and no ultracode; their effort is their own.

For parallel work needing its own checkout, give `worktree.branch`: Persona uses the machine's engine (herdr or Orca) to create and list it under the workspace containing `cwd`, then starts the agent there. Never tell the agent to run `git worktree add`: that hides it from Orca and the app. Existing branches are checked out; new ones start at that workspace's HEAD. A branch checked out elsewhere is refused.

Claude Code only: `secret_env` maps variable names to secret references (e.g. `op://vault/item/field`), never values. The owner approves before launch; values reach only this Agent on a connected computer. The receipt lists the grant status. Tell the Agent the names, never ask it to print values.

Give `ultracode: true` for a large task: one that spans many files or needs checking from several angles (design, tests, review). Claude Code then runs every substantial step as a multi-agent workflow: slower and costlier: off for small work. Do not write the word "ultracode" into the prompt instead. Claude Code agents only (2.1.284 or later): any other agent, an older Claude Code or a node too old for it starts without it, and the answer says so. Once Claude Code confirms it, agents_list shows `ultracode=on`. An organization policy may keep some conversations read-only: a call from such a conversation is refused with the policy's reason. Do not retry; follow the refusal.{{template "shared/mcp_language" .}}

`wait_seconds` observes up to 50 seconds after dispatch, appending `Inline observation` JSON: `completed`(this input's `reply`), `question`(pending `interaction_id`), `timeout`/`cancelled`/`unavailable`/`interrupted`/`failed`, with known input/session ids. Observation never answers questions, accepts tasks, resends or cancels work; normal supervision/`task_review` applies. Older nodes may time out rather than return an unrelated reply.

```json
{
  "type": "object",
  "required": [
    "machine",
    "cwd",
    "prompt"
  ],
  "properties": {
    "wait_seconds": {
      "type": "integer",
      "minimum": 0,
      "description": "0/omitted: normal receipt"
    },
    "machine": {
      "type": "string"
    },
    "cwd": {
      "type": "string",
      "description": "absolute path or ~/…; with worktree, a directory of the repository to branch from"
    },
    "prompt": {
      "type": "string"
    },
    "skip_project_memory": {
      "type": "boolean",
      "description": "default false; true skips project/direction memory; prompt unchanged"
    },
    "attachments": {
      "type": "array",
      "maxItems": 8,
      "items": { "type": "string" },
      "description": "absolute paths of pictures/files the user gave in this conversation (Claude Code path or app attachment); copied to the agent, appended to its message with mentions updated; others refused"
    },
    "launcher": {
      "type": "string",
      "description": "listed launcher (Claude Code, pi, cursor or agy); unknown names refused with choices; empty: default/fallback, claude first, answer says which"
    },
    "secret_env": {
      "type": "object",
      "maxProperties": 8,
      "additionalProperties": { "type": "string" },
      "description": "NAME → secret-provider reference (op://vault/item/field), never a value; card approval first"
    },
    "ultracode": {
      "type": "boolean",
      "description": "multi-agent workflows; default false"
    },
    "worktree": {
      "type": "object",
      "description": "engine-made worktree of cwd's repository",
      "required": [
        "branch"
      ],
      "properties": {
        "branch": {
          "type": "string",
          "description": "the branch to check out (made from the workspace's HEAD when it does not exist)"
        }
      }
    },
    "tier": {
      "type": "string",
      "description": "Agent preset from the 模型档位 line, by kind of work (old names work); picked before approval, shown on the card; explicit launcher/model/effort/speed overrides"
    },
    "host": {
      "type": "string",
      "description": "agy (Antigravity companion) or an enabled engine"
    },
    "speed": {
      "type": "string",
      "enum": ["standard", "fast", "ultrafast"],
      "description": "new-start speed; explicit choice overrides the tier. standard/omitted: launcher default. Codex: fast, ultrafast; Claude Code: fast only"
    },
    "effort": {
      "type": "string",
      "description": "Explicit effort; omit for tier/purpose default."
    },
    "model_aliases": {"type":"object","description":"New Claude Code terminal session only: sonnet_model, haiku_model, opus_model, fable_model, subagent_model → concrete IDs; omitted inherit.","additionalProperties":{"type":"string"}},
    "model": {
      "type": "string",
      "description": "Explicit model id or alias the agent takes; omit for the tier or purpose default."
    }
  }
}
```
