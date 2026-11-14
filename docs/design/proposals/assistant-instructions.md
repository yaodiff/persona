# Proposal: one home for each assistant instruction, with behaviour checks before cuts

Status: proposed (#5898). Nothing here is built. Builds on the measurements of #5764 and leaves its tool-discovery and
snapshot-delta workstreams in place; supervision events stay in #5665. Covers the assistant's generated instructions,
its MCP tool descriptions and the messages Persona sends it; no screen changes.

## 1. Goal

**One sentence**: the assistant follows its rules as reliably as today or better, every rule has one home that people
and Agents can find and change, and what the model reads on every turn is only what that turn may need.

The first two halves are the point. A smaller context, a lower cache-write cost and a shorter first reply are welcome
side effects, not the reason: Claude Code caches the unchanged prefix, so a long instruction file costs most when a
conversation starts or its cache has expired, and how much that is on a real install is not measured yet (section 2c).

**Reached when**:

| # | What holds | Checked by |
| --- | --- | --- |
| G1 | Behaviour is kept | Every case of the behaviour set (section 4b) passes at least as often after a step as before it |
| G2 | One home per rule | Every rule in the disposition table (section 4a) has exactly one home; other places reference it |
| G3 | The always-loaded text shrinks and stays shrunk | The size ratchet (section 4a) records each step's reduction and never takes a raised baseline |
| G4 | Nothing gets worse on a live install | The week after a step, against the week before: `reply_wait`, `ask_outcome`, task rework and view-error counts (section 4d) |

## 2. What happens today

### 2a. Where instructions come from

| Layer | Source | Read |
| --- | --- | --- |
| Shared instructions | `server/internal/prompts/assistant/claude_md.md`, rendered to `CLAUDE.md` and `AGENTS.md` in the assistant directory | By every conversation in that directory, on every turn |
| Role block | `server/internal/prompts/assistant/context.md` (main chat, leader, background supervisor) | In the conversation's snapshot |
| Snapshot | `server/internal/prompts/assistant/snapshot.md`, `server/internal/prompts/assistant/turn_context.md` | At the start of each message, as a delta |
| Tool descriptions | `server/internal/prompts/mcp/` | With the tool list, on every turn unless the runtime defers it |
| Event messages | `server/internal/prompts/assistant/supervise.md`, `server/internal/prompts/agents/wake.md` and others | When the event comes |
| Skills | `server/internal/assistantskills/builtin/` (today only `persona-office`), extension skills | Name and description always; body when the model opens it |

The instruction file is written per directory, not per conversation. The main chat, the background supervisor and the
leaders all read the same text; what differs between them is only the role block.

### 2b. Sizes

From the goldens (synthetic input, `server/internal/assistant/testdata/prompts/assistant_claude_md.full.golden`):
33,277 bytes, of which "Working practices" is 18,354, "Interacting with the user" 7,825 and "Data you can access"
4,896. Inside "Working practices", dispatch and supervision take 8,175 bytes and the rich-view card rules 5,395 (only when
the client supports views). Phone procedures and place reminders take about 3,400 bytes in "Interacting with
the user".

The tool goldens (`server/internal/hub/testdata/prompts/mcp_tools.assistant.golden`) hold 78 tools in about 118,000
bytes of compact JSON. The largest are `task_plan` (about 13,100) and `task_review` (about 10,600). On a real install
#5764 counted 116 Persona tools in 143,828 bytes, with phone, mail, history-import and codebase families making up
about 28.7 KB.

### 2c. What is known and what is not

- **Repeated rules.** Authorization before production writes, outward sends, deletions and spending is written three
  times in `claude_md.md` and again in `wake.md`. Dispatch rules are in `claude_md.md` and in
  `server/internal/prompts/mcp/agents/task_plan.md`. Supervision policy is in `claude_md.md`, `supervise.md` and
  `server/internal/prompts/mcp/agents/task_review.md`. A change to one copy can leave the others behind.
- **Growth by addition.** `claude_md.md` was changed 26 times between 2026-11-09, where the history on `main` starts,
  and 2026-11-12, almost always by adding a rule.
- **No behaviour check for the main chat.** The goldens pin text. The live checks in `docs/assistant/prompts.md`
  ("Behaviour checks") cover three single-call judges, not the assistant's conversation.
- **Not known: whether length causes mistakes.** No issue so far reports the assistant skipping a written rule because
  of the prompt's size. That long, repetitive instructions dilute attention is a reasonable expectation, not evidence;
  step P0 measures it before any cut depends on it.
- **Not known: what the cache does.** How often the assistant's conversations start cold, how often the cache expires,
  and whether the runtime on an install defers MCP tool definitions are not measured. #5764's recorded request shapes
  list every Persona tool.

## 3. One home for each kind of instruction

### 3a. Placement

| Kind of instruction | Home | Why there |
| --- | --- | --- |
| Identity, language, how to answer on a phone | Shared instructions | Every turn of every role needs it |
| Red lines: authorization before production writes, outward sends, deletions, spending; no passwords in chat | Shared instructions, once, at the top | A miss cannot be undone, so it never depends on the model opening something |
| When to use which family of tools, and where to look first | Shared instructions | The model must know a capability exists before it can look for its details |
| Rules for one role (supervision, the clerk's duties, a leader's reporting) | That role's block in `context.md` | Only that role reads it |
| How to fill a tool's parameters, its limits, its own preconditions | The tool's description | Read exactly when the tool is used |
| Format details the hub checks (view JSON, `done_when` present) | The validation error the hub returns | The hub already rejects a wrong call; the error says how to fix it |
| Domain procedures whose mistakes are caught (rich-view layout, phone operations that the phone confirms, lark-cli usage) | A skill | Loaded when the work needs it |
| What an event is and the next step | The event message, with references to stable policy (#5665) | The event carries facts; policy has its home above |

**One rule, one home**: a rule is written where the table puts it. Anywhere else names it in a few words or points to
it; it is never restated.

### 3b. What may become a skill

A skill is read only when the model decides to open it, so a procedure moves into one only when skipping the skill
leads to a mistake that something catches before it matters:

- the hub validates the result and returns an error (views, `view_show`);
- the user confirms on the phone before the action happens (phone writes, mail sends);
- the command reports its own misuse (`--help`, a CLI's errors).

Red lines and anything irreversible never move into a skill.

The mechanism exists: `server/internal/assistantskills/` copies built-in skills into the assistant directory's
`.claude/skills`, Codex reads the same directory (`server/internal/node/assistant_codex_thread.go`), and other runtimes
get it under `.agents/skills` (`server/internal/node/assistant_runtime.go`). A skill can be put in only under a
condition, as `assistantskills.Requires` does for connectors, so the view skill can follow the client's support as the
`PersonaView` section does today.

### 3c. Role blocks are re-sent when context is lost

Rules that move from the shared instructions into a role block must still be there after a fresh start, a resume, a
node restart and compaction. What a conversation has seen of its snapshot is kept in memory and forgotten when it is
compacted (`server/internal/node/assistant_context.go`), so the snapshot is sent whole again; step P2 checks that the
role block rides along in every one of those cases before moving anything (section 4c).

## 4. Verification

Prompt changes alter what a model does, which no compiler checks. The method below does not prove a change correct; it
makes a regression visible before merge or within a week after, and makes every step revertible on its own.

### 4a. The text

- **Goldens** stay as they are and show every rendered change in the PR.
- **Disposition table**: a PR that removes or moves text lists each rule it touched and where it went (role block, tool
  description, validation error, skill, event message), or why it can go (the hub enforces it; it duplicated a named
  home).
- **Size ratchet**: a test records the bytes of each section of the shared instructions and of each tool description,
  and fails when one grows past its baseline. A step that shrinks something lowers the baseline in the same PR. Like
  the Apple token ratchet, it never takes a raised baseline; a rule that has to grow is moved or shortened elsewhere
  first.

### 4b. The behaviour set

- **Cases**: synthetic and public, as the judges' cases are. Each holds a snapshot, a message (from the owner, a
  supervision event or a follow-up) and canned tool results. Every rule a step touches has at least one case. A first
  set of about 15 covers what the assistant is for:
  - three independent requests in one message become three `task_plan` tasks, each with `done_when` and
    `expect_minutes`;
  - a question about a running task is answered from the snapshot or `agent_read`, without `agent_send`;
  - a supervision event is reviewed with evidence and closed with `task_review`, not retold to the owner;
  - an event the owner need not see ends with `supervision_finish` and `visibility: "silent"`;
  - an outward send without authorization goes to `ask_user` first;
  - a question that does not block the next step uses `ask_user` with `wait: false`, in the main chat and in a leader
    conversation alike (#7392);
  - work for a direction carries its `supervisor`;
  - a recurring request uses `schedule_routine`, not a chain of follow-ups.
- **Checks are deterministic where possible**: which tools were called, with which arguments, in which order. A model
  grader is used only where no such check exists, and says so.
- **Runs**: each case runs five times against a real model on the old and the new text; a PR attaches both tables.
  This runs outside CI, like the judges' live checks (`PERSONA_LIVE_AI`).
- **Pass bar**: decision 1 in section 6.

### 4c. End to end

The behaviour set calls the model directly, so it cannot see what the runtime adds. For steps that depend on the
runtime, a check runs the real Claude Code (and Codex where the assistant may use it) in a temporary assistant
directory:

- a step that moves a procedure into a skill shows the skill opened and the result valid on its cases;
- a step that moves rules into a role block shows them present after a fresh start, a resume and a compaction.

### 4d. Live

After merge, the owner's install runs a week (decision 2). Compared with the week before:

- `reply_wait` and `ask_outcome` from `GET /api/v1/assistant/availability` (`docs/assistant/assistant.md`,
  "Availability metrics (#4924)");
- tasks accepted, sent back for rework and cancelled;
- view errors returned to the assistant.

The week before is the baseline week #5807 already owes, so P0 waits for it. A step that makes any of these worse is
reverted on its own; each step is one PR for that reason.

## 5. Steps

Each step is its own issue and PR. P0 comes first so the others can be judged.

| Step | What | Depends on | Revert |
| --- | --- | --- | --- |
| P0 | The behaviour set and its runner (4b), the end-to-end check (4c), the size ratchet (4a); a first run on today's text | #5807's baseline week for the live part | Tests only |
| P1 | Red lines in one place at the top of the shared instructions; the copies in `claude_md.md` and `wake.md` point to it | P0 | One PR |
| P2 | Supervision and clerk rules leave the shared instructions for the role blocks of the background supervisor, the leaders and the main chat | P0, coordinated with #5665 | One PR |
| P3 | Rich-view layout rules and examples into a built-in skill put in when the client supports views; the shared instructions keep when to use a view | P0 | One PR |
| P4 | Phone procedures into the phone tools' descriptions or a skill; place-reminder timing stays where the timing is decided | P0 | One PR |
| P5 | `task_plan` and `task_review` descriptions keep how to fill them; when to dispatch stays in the shared instructions, once | P0, P2 | One PR |
| P6 | Optional tool families on demand, through a mechanism the runtimes support: #5764's tool workstream, judged with this method | P0 | Its own issue (#5764) |

Snapshot deltas stay in #5764 and are not a step here.

## 6. Open decisions for the owner

1. **Pass bar for a step.** Recommended: no case passes fewer runs than before (out of five), and the total does not
   fall. Against: five runs leave noise, so a case that drops by one run may be rerun ten times before it blocks.
2. **Live trial first or straight to everyone.** Recommended: merge, then a week on the owner's install before the next
   step builds on it. A setting that switches between old and new text would double the prompts to maintain.
3. **Which model the behaviour set runs on.** Recommended: the model bound to the main chat's purpose
   (`docs/assistant/model-config.md`), plus one tier down when a step claims to help smaller models. Persona names no
   model in code (PRINCIPLES 5).
4. **Is the red-line list complete?** Recommended: production writes, outward sends, deletions, spending, passwords
   in chat, and the per-action phone confirmations for locks, garage doors and security. Adding to it is the owner's
   call; P1 writes down whatever is decided.
5. **Phone procedures: tool descriptions or a skill.** Recommended: tool descriptions, since the tools are only offered
   when the phone is connected; a skill only if the shared parts outgrow them.

## 7. Principles self-check (`docs/design/PRINCIPLES.md`)

| Principle | How this proposal meets it |
| --- | --- |
| 1 Defaults first | No setting; the assistant behaves as before, which section 4 checks |
| 2 Progressive disclosure | Procedures load when the work needs them; nothing new on any screen |
| 3 User control | The owner's About me notes, standing rules and authorizations keep their place and are not shortened |
| 4 Consistency | Reuses the built-in skill path, the role blocks, the judges' live-check pattern and the Apple ratchet's rule; adds no new kind of prompt |
| 5 Model and product separate | No model is named; the behaviour set runs on the owner's bindings, and switching the model changes no feature |
| 6 Rules are checkable | G1 to G4 each have a check in section 4; every step has its revert |
| 7 Fast, local-first and stateful | Not applicable: no client change |

## 8. Related

- #5764: measurements of instructions, tools and snapshots; owns tool discovery and snapshot deltas.
- #5665: supervision events carry facts and references instead of the handling manual.
- #5807: the baseline week of the main-chat availability metrics.
- `docs/design/proposals/main-chat-availability.md`: the background supervisor whose role block P2 fills.
- `docs/assistant/prompts.md`: where prompts live and the rules for changing them.
