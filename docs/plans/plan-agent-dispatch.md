# Plan: Agents hand subtasks to other connected computers (#3253)

Status: step 1 is implemented ("Step 1" below). Steps 2 to 4 are proposals; each gets its own issue when someone picks it up. When all are done, merge the design parts into `docs/assistant/unified-entry.md` and delete this plan.

## Background

A coding Agent sometimes finds that part of its task belongs on another computer: the repository or the data is checked out there, the tests need that operating system or a GPU, or its own computer is busy. Until now only the assistant's conversations could place work on a computer (`task_plan`, `agent_start`). An Agent could only stop and say so to its supervisor or the user.

T3 Code (`pingdotgg/t3code`) ships this as a beta called "linked environments": work runs on one connected device and can send subtasks to the others. This plan records how T3 Code does it, what Persona already has, what is missing, and what is worth borrowing under Persona's rule that the default stays simple and advanced features are switched on in configuration.

## How T3 Code does it

Read from its source at commit `cec8d73` (2026-11-10). Paths are in that repository.

**An environment is a server.** One running T3 server, with the machine, credentials, files and state it owns, is an environment (its internals doc `remote.md`). There is no central server. Each client (desktop, web, mobile) saves a list of environments and connects to every one of them directly. A saved environment has an ordered list of routes (LAN address, Tailscale, SSH port forward, the T3 Connect tunnel); the client uses the first route whose descriptor reports the expected environment id. The hosted relay only brokers sign-in and the tunnel; it does not carry application logic.

**Projects and threads belong to one environment.** The composer has an environment picker. Its optional "Auto balance" entry scores environments by CPU count, CPU use and free memory and skips samples older than 15 seconds (`packages/client-runtime/src/load-balancing.ts`). Providers (Claude, Codex and others) and worktrees are per environment.

**Agents delegate through MCP tools.** Every provider session gets an MCP server with `delegate_task`, `task_status`, `task_cancel` and thread tools (its `orchestrator-mcp-server.md` design doc). A child task receives only its prompt, not the parent's history, and can keep or narrow the parent's permission modes but never widen them.

**Cross-device delegation is a link between two servers.** `delegate_task` takes an `environmentId` (`packages/contracts/src/orchestratorMcp.ts`). The mechanism, in `apps/server/src/peer/`:

- *Linking*: the calling environment signs in to the other environment's `/mcp` endpoint as an outside OAuth client, with a pairing code, and keeps the token in its own secret store. The user picks the link's access level on an approval card. Links expire after 30 days. The token is sent only to an address whose descriptor proves it is the linked environment.
- *Routing*: the calling agent's server forwards the call. It finds the project with the same repository on the other machine, launches a thread there marked `delegatedFrom`, and records the task locally with a `remoteChild` reference in place of a child thread.
- *Results*: a follower on the calling server waits on the remote thread, in forwarded waits split into calls of at most 50 seconds so an idle connection is not dropped. When the remote thread settles, the follower reads its last reply and completes the local task, which wakes the parent agent the same way a local child does. Open remote tasks are followed again after a restart. While the target is unreachable the follower retries with backoff up to 5 minutes and fails the task only for a final reason (access denied, thread gone, link forgotten).
- *Permissions*: every forwarded call carries the calling agent's modes in a header, and the receiving side only narrows access with it. Work started over a link below full access is fenced: it can change only threads its own link started, cannot change projects or settings, skips project setup scripts, and cannot use the receiving machine's own links. A full-access link is not fenced.
- *Limits*: a remote task cannot ask for worktree isolation (it runs in the other project's checkout). We found no depth or concurrency cap.
- *Switch*: `linkedEnvironmentsEnabled`, default false, must be on for both machines. Turning it off refuses new links and new calls; tasks already running finish.

The clients show "From <machine>" on threads started over a link.

## What Persona has today

| Need | Persona today |
| --- | --- |
| Connected computers | One hub holds all state; every computer runs a node the hub connects to (SSH, Persona Link, tailcat). Linked hubs still have exactly one primary (`docs/server/mesh.md`). Machine identity is `machine_uid`. |
| Running work on a chosen computer | The assistant's `agent_start` and `task_plan` (`server/internal/prompts/mcp/agents/`) start an Agent on a named computer; `machines_list` reports connection state, resources, working Agents and failing launchers. |
| Worktrees | `agent_start` and `task_plan` take `worktree.branch`; the target computer's engine (herdr, Orca) makes the worktree, so the apps list it. |
| Launcher and model choice | Model Settings › Agents, tiers, per-computer launcher fit and launcher health with a detour to a computer where the launcher works (`docs/server/launcher-health.md`). |
| Placement | Task plans default to `fixed` and may opt into `flexible`. Dispatch keeps its named computer. Manager placement records the applicable filters in report mode, including a busy machine or full launcher group (#13355). |
| Following and results | Supervision in the main conversation: task cards, `agent_read`, `agent_send`, `agent_interrupt`, the Agents' `report_to_supervisor`, independent review, triage and wakeups (`docs/assistant/unified-entry.md`, `docs/assistant/agent-work.md`). |
| Watching from anywhere | Every session is visible and answerable in all clients. |
| Privacy boundaries | Work computers and guest conversations get a reduced tool set (`server/internal/assistant/fleet_tools.go`). |

## Comparison

**Already there, often stronger.** Routing between computers needs no extra pairing: every node already has an authenticated connection to the hub, so there are no per-pair links, tokens or expiry to manage. Placement signals (resources, launcher health, which launchers a computer has) exist. Worktrees on the target computer work, which T3 Code refuses for remote tasks. Supervision, review and triage have no T3 Code counterpart.

**Missing.**

1. *The Agent-side entry.* Only the assistant's conversations can start work on a computer. A coding Agent has no tool for it, and `task_plan` / `agent_start` are refused for a task's Agent (`workerRefusal` in `server/internal/assistant/supervise_leader.go`).
2. *Results back to the calling Agent.* Results flow to the main conversation (task notices, reports), never to another Agent.
3. *The parent-child link in the apps.* A session does not say which Agent started it.
4. *Limits for Agent-started work.* No depth or concurrency rules exist because Agents could not start work.

**Different on purpose.**

- *Hub and spoke, not peer to peer.* T3 Code's servers talk to each other. In Persona the manager places the task and the hub carries the calls, so the two computers never connect to each other and no credential for one computer is stored on another.
- *Who places work.* T3 Code's client picks the environment for a thread. In Persona the assistant plans placement and the user confirms on a card. Agent-side dispatch is an addition for the case where the Agent discovers the need mid-task, not a replacement.
- *One level, not a tree.* T3 Code lets remote work delegate further under a full-access link. Persona keeps Agent-started work one level deep.

## What to borrow

Borrow:

- **An Agent tool that takes a target computer**, with the child getting only its prompt.
- **Off by default, one switch.** T3 Code gates the whole feature behind one setting; so does this plan (`agents.dispatch.enabled`).
- **The parent reads the result as the child's last reply**, with waits cut into pieces of at most 50 seconds so a held tool call never runs into its caller's timeout.
- **Follow open tasks across restarts**: who dispatched what is persisted.
- **Refuse new work when switched off, let running work finish.**
- **"From <computer>" in the clients** (step 3).
- **Waking the parent when the child settles** (step 2).

Do not borrow:

- **Peer links, per-link tokens and access levels.** The hub already authenticates every computer. Links would add pairing, expiry and a second trust model for no gain.
- **Client-side load balancing.** Placement belongs to the manager, which already has the signals.
- **Unbounded depth.** One level covers the need and cannot run away.
- **Repository matching to find the directory.** The caller names the directory; the knowledge base already knows which project lives where, and step 4 can offer it.

## Design

New calls to `dispatch_start` use [A2A](../integrations/a2a.md). The manager
stores task and delivery state before launch, then returns questions and
results to the initiating session. The sections below describe the original rollout.

### Step 1 (implemented): the Agents' `dispatch_start` and `dispatch_read`

Code: `server/internal/assistant/dispatch.go`; tool definitions in `server/internal/prompts/mcp/dispatch/`; the child's first message in `server/internal/prompts/agents/dispatch_task.md`.

```text
Agent on computer A ──MCP──► node A ──► hub ──► manager ──session.start──► hub ──► node B: new Agent
        ▲                                         │ remembers A/session → B/session
        └────────── dispatch_read ◄───────────────┘ state + last turn of B's session
```

- **Switch**: `agents.dispatch` in `manager.json` (`docs/operations/config.md`), `enabled` false by default, `max_active` 3 by default. While off, the tools are not listed for any Agent and a call is refused. Nothing else changes when it is off.
- **`dispatch_start {machine, cwd, prompt, launcher?, tier?, worktree?}`** starts an ordinary Agent session on another connected computer and returns its session id at once. It uses the same start as `agent_start`: Model Settings › Agents picks the launcher and model unless `launcher` names one or `tier` names an Agent preset (resolved and refused as `agent_start` resolves it, without the preset's permission mode, #14672), `worktree.branch` has the target's engine make the worktree, and an agy launcher starts on the target's Antigravity companion where that runs it (#14887). The child's first message says which Agent on which computer handed it the task, that its last reply is the result, that it cannot hand the task on, and that it must ask the user before pushing, deploying, messaging people or merging.
- **`dispatch_read {session_id?, wait_seconds?}`** with no arguments lists the other connected computers (id, name, online, home directory, the Agent presets each can start) and the caller's dispatched tasks with their states. With `session_id` it reads the last turn for a listed session (working, stopped, waiting for the user, interrupted, or state unknown). Only a stopped turn's last reply is its result. Computer offline, computer removed, not listed, and not read explain why the last turn could not be read. A task missing from a connected computer's list stays working during its two-minute start grace. `wait_seconds` (at most 50) waits for a working task to stop.
- **Who calls**: the node names the calling Agent's session for these tools, as it does for `report_to_supervisor` (`callerTools` in `server/internal/node/hooks.go`). A node older than this change does not, and the call is refused with a hint to update the node.
- **Ownership**: the manager records `{from machine, from session, machine, session, cwd, title, time}` in its settings (`agents.dispatch.tasks`; 7 days, newest 200). Only the Agent that dispatched a task can read it. The record survives a manager restart.
- **Limits**:
  - One level: a session that is itself a dispatched task cannot dispatch.
  - At most `max_active` of one Agent's dispatched tasks may be working at a time.
  - Never to the caller's own computer (that is what in-session subagents are for), to a work computer, or to an offline computer.
  - Work computers' Agents and guest conversations do not get the tools.
- **A start with no answer** is not reported as a failure and not recorded: the Agent is told that a session may exist and to tell the user instead of starting again.
- **Visibility**: the child is a normal session. The user sees it in every client, can answer its questions, and can stop it. Triage and notifications treat it as any Agent.
- **Supervision** (#3439): the child also becomes a to-do supervised by the main conversation, through the path `agent_supervise` uses (`adoptAgent` in `server/internal/assistant/agent_control.go`). Its notes say which Agent on which computer handed it out, its task card appears in the main conversation, the main conversation gets its supervision messages (done, stuck, no progress) and closes it with `task_review`, and the live snapshot lists it. The caller still reads the result with `dispatch_read`. With no main conversation no to-do is made and the task runs untracked; a failure to make the to-do never fails the start.

Trade-offs taken in step 1:

- *No per-dispatch approval card.* The switch is the consent, as with `agent_start`, which does not ask either (outside the health space). Step 4 adds an optional card.
- *Polling, not waking.* The caller reads the result with `dispatch_read`. This needs no new kind of message that Persona sends to Agents on its own (`docs/assistant/agent-work.md`, "Safety boundary"). Step 2 adds waking.
- *The child's permission mode is the target's default*, not derived from the caller's. Persona has no cross-launcher mode ceiling yet; the child's first message carries the same "ask before pushing or deploying" rule the wakeups use. Since the child is a supervised task's (its questions and approvals go to its supervisor first), a Claude-family launcher (or an Auto Accept provider) starts it in Allow all, `bypassPermissions`, as `agent_start` and planned tasks do (`dispatchAllowAll`, `docs/assistant/unified-entry.md` "Permissions"); a mode Model configuration names stays, and any other launcher keeps the target's default.
- *Settings, not a table.* The record is small and bounded; a table is worth it once step 2 needs per-task state.

### Step 2: results come back by themselves

When a dispatched session's turn ends, the manager sends the calling Agent one message with the task's state and final reply, if the caller is stopped and still on the turn that dispatched (the same rechecks as wakeups, `docs/assistant/agent-work.md`, "Avoid conflicting with manual user actions"). A working caller picks it up with `dispatch_read`. Add `dispatch_cancel` (interrupts the child; only its caller). This adds a fifth kind of message Persona sends on its own, so the safety boundary section changes with it. Move the records to a table with per-task state (delivered, cancelled).

### Step 3: the apps show the link

`proto.Session` gains `dispatched_from {machine_id, session_id, label}`; the session header shows "From <agent> on <computer>" and the caller's session lists its dispatched tasks. A switch under Settings replaces hand-editing `manager.json` (the file value stays the default).

### Step 4: choosing a computer, and optional approval

`dispatch_read`'s overview adds what `machines_list` knows (resources, failing launchers) and where the knowledge base says a project is checked out. `agents.dispatch.ask` makes each `dispatch_start` wait for the user's yes on a card, with the usual "always allow this kind" choice.

## Points still open

- Whether a supervised task's dispatched children should appear on the task's card in the main conversation (step 3 or later).
- Whether the child should inherit the caller's permission mode once launchers agree on mode names.
