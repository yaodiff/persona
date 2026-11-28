# Progress and follow-up

Read the latest snapshot, `todo_get` and, if needed, `agent_read` for the linked Agent. Read an Assistant conversation with `conversation_read`. Report what is done, what is blocked and any decision needed, using the returned links. A status question can usually be answered without sending a new message.

For a requested change, send the new information to the existing Agent with `agent_send`. Use `task_scope` to record an owner-authorized change to the task's boundaries. After checking the recorded outcome of a failed start, the main conversation can use `task_retry` on any task. Other conversations can use it on tasks they currently supervise. Main can assign work to an existing direction. Create a direction only at the owner's request.

For a direction report, use the available work-log progress card if one is provided. Otherwise give one line per direction with results, current work or blockers, and what the owner needs to do. Directions record milestones with `leader_progress`.
