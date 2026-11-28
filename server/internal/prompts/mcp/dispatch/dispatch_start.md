Hand a subtask to a new agent on another of the user's connected computers and get its session ID. Call dispatch_read without arguments to see the computers, their home directories and the Agent presets each can start. Give the new agent a self-contained prompt with the goal, paths on that computer, expected result and boundaries. You and the user can follow the session in Persona. Questions and results come back through A2A, so you can finish your turn while it works. Use dispatch_read for a status check, or a2a_get for the task's messages. A dispatched agent cannot dispatch another task. The manager limits how many tasks you can run at once.

Give `worktree.branch` when the work needs a branch and checkout of its own on that computer: Persona makes the worktree through that computer's engine and starts the agent in it. Do not tell the agent to run `git worktree add` itself.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["machine", "cwd", "prompt"],
  "properties": {
    "machine": {
      "type": "string",
      "description": "the other computer's id or name, as dispatch_read lists it"
    },
    "cwd": {
      "type": "string",
      "description": "the directory on that computer: an absolute path or ~/…; with worktree, a directory of the repository to branch from"
    },
    "prompt": {
      "type": "string",
      "description": "the whole subtask, self-contained"
    },
    "launcher": {
      "type": "string",
      "description": "a launcher that computer has; empty: the tier's, else the user's default for new agents"
    },
    "tier": {
      "type": "string",
      "description": "an Agent preset that computer can start, by name as dispatch_read lists it: its launcher, model, effort and speed (not its permission mode); a different launcher given too wins and the preset gives nothing"
    },
    "message_id": {
      "type": "string",
      "description": "A unique ID for this launch. Reuse it if you retry the same launch."
    },
    "worktree": {
      "type": "object",
      "description": "start the agent in a new worktree of cwd's repository, made by that computer's engine",
      "required": ["branch"],
      "properties": {
        "branch": {
          "type": "string",
          "description": "the branch to check out (made from the workspace's HEAD when it does not exist)"
        }
      }
    }
  }
}
```
