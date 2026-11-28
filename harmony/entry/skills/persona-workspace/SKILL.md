---
name: persona-workspace
description: Workflow status, Agent task delegation, and lock-screen-gated confirmation skills for Persona on HarmonyOS 7.
version: 1.0.0
---

# Persona Workspace Skill

Use this skill when the user checks on active tasks, delegates work to a paired computer's Agent session, or reviews pending confirmation requests:

1. **Check Status (`checkStatus`)**: Report running tasks, pending confirmation count, and the oldest pending request.
2. **Send Agent Task (`sendAgentTask`)**: Dispatch a prompt to an active session on a trusted online computer.
3. **Approve First Pending (`approveFirstPending`)**: Approve the oldest eligible tool permission or `acceptEdits` plan only when the device is unlocked (fails closed on a locked screen; defers questions and `bypassPermissions` plans to Persona).
4. **Push Subscription Item (`pushItem`)**: Save a note or clip into Persona's device push source with offline queue fallback.
