# Server-first logic and thin clients

Who this serves: contributors and reviewers designing or changing Persona across the Go server and any client
(Web, iOS, Android, Watch, Vision Pro, Desktop, and HarmonyOS).

## Why

Persona ships across many clients—Web, iOS, Android, Watch, Vision Pro, Desktop, and HarmonyOS—all talking to one Go
hub and its managers. Whenever shared domain logic is implemented independently on each client:

- every new capability or rule refinement must be authored, tested, and debugged multiple times across languages;
- platforms drift, so the same underlying data behaves differently or shows conflicting states, actions, or counts on
  different screens;
- older or slower-updating clients silently diverge from newer ones.

This rule applies Persona's **Consistency first** and **Rules are checkable** principles—balanced with **Fast,
local-first and stateful** for immediate UI feedback (`docs/design/PRINCIPLES.md`)—across **every Persona domain**:
Agents, Assistant chat, Inbox, To-dos & Tasks, Directions, Knowledge, Integrations & Channels, Briefings,
Notifications, Settings, and Devices.

## Ownership principle (the litmus test)

Start every design decision from first principles:

- **Server owns (Single Source of Truth):** any logic that defines **shared product truth**, **domain semantics or
  rules**, or **behavior that must agree across clients**.
- **Clients own (Thin Clients):** logic tied strictly to **presentation, local transient UI interaction, device-native
  capabilities, or offline/low-latency UX**.

**Quick test:** if two clients could disagree on the outcome for the same user data—or if a rule would have to be
re-implemented across Web, iOS, Android, and Watch—the Go server owns it.

## Rule 1 — Server-owned domain logic (Go server first)

By default, domain logic lives in the Go server as the single source of truth and is exposed through structured wire
contracts. Following `docs/server/architecture.md`, business logic belongs in the owning manager
(`server/internal/assistant`, `server/internal/devices`, `server/internal/extmgr`, or a feature module under
`server/internal/modules/`), while `server/internal/hub` stays a communication center.

Server-owned logic spans the full read and write lifecycle across seven dimensions:

1. **Domain state and transitions**: lifecycle state machines, domain commands, invariants, completion and closure
   semantics, and rollback or compensation rules.
2. **Validation, canonicalization, and conflict/idempotency control**: input validation, normalization, deduplication,
   bounds checks, revision/compare-and-set fences, and operation idempotency receipts.
3. **Querying, filtering, search, indexing, and windowing**: server-side filters, search, lightweight navigation
   indexes, and bounded keyset cursors or windows—so clients never download whole histories just to filter or search
   locally.
4. **Derived projections and summaries**: panel-facing view states, bucketing and grouping, sorting and prioritization,
   folding and blocker surfacing, count definitions (`docs/protocol/counts.md`), and cross-entity aggregations.
5. **Permissions, capabilities, and action affordances**: who may perform an operation, whether an action is enabled or
   disabled, and the authoritative reason or capability flag—so clients do not duplicate complex conditional matrices
   per platform.
6. **Routing, orchestration, scheduling, and side effects**: event routing and triage, approval and confirmation gates,
   notification and push grading/silencing, timers and schedules, and retries or self-healing loops.
7. **Cross-device user state**: read/seen marks, pins, archives, dismissals, synced preferences, and opt-in feature or
   module switches that must stay consistent across every device.

## Rule 2 — Thin client boundary

Clients present server-owned state, send explicit user intents, and deliver responsive platform-native UX; they do not
become independent engines of business rules.

**Clients own:**

- Presentation and platform-native UI using the design system (`docs/design/DESIGN_SYSTEM.md`).
- Responsive layout, list virtualization, accessibility, typography, and motion.
- Local transient UI interaction state: composer drafts, keyboard focus and row selection, transient expand/collapse
  toggles, local navigation stacks, and immediate optimistic transitions reconciled against authoritative server
  outcomes (`docs/design/PRINCIPLES.md`, "7. Fast, local-first and stateful").
- Device-native capabilities (haptics, widgets, Live Activities, OS share/intent bridges, local device tools) plus
  offline cache presentation, snapshot/cursor synchronization, and reconnect mechanics
  (`docs/architecture/core-panel-contract.md`).

**Clients must not:**

- Duplicate domain rules, state transitions, validation or conflict policies, permission/affordance matrices, full-set
  filtering, or count and status definitions independently across platforms.
- Define client-only states, counts, or eligibility rules that can contradict the server or another client.

**Bounded exceptions (offline or low-latency UX):**

When a piece of logic genuinely must run client-side—for example, offline snapshot assembly, instant local filtering
over an already-bounded cache, or pre-round-trip optimistic feedback—keep it bounded and consolidated rather than
ad-hoc per platform:

- share native client logic in `native/app-core`;
- pin cross-platform behavior with shared golden fixtures under `spec/fixtures/`;
- document in the PR and owning doc why the server cannot own it.

## Rule 3 — Additive evolution, compatibility, and rollback

Server-owned contracts must evolve without breaking older or unmodified clients during rolling upgrades
(`docs/protocol/rolling-upgrade-compatibility.md`):

- **Strictly additive optional fields**: emit new state, projection, or affordance fields as optional JSON fields
  (`omitempty` in Go) so older clients ignore unknown keys safely.
- **Preserve existing wire semantics**: do not remove, rename, or silently alter the meaning of existing fields while
  older clients still read them; maintain deterministic compatibility mappings alongside new fields until legacy
  consumers retire.
- **Shared fixture contract tests**: back shared wire shapes and server-owned rules with fixtures under
  `spec/fixtures/` (such as `spec/fixtures/wire`, `spec/fixtures/counts`, and `spec/fixtures/needs-you`) exercised by
  both server and client test suites.
- **Lossless rollback verification**: for gated or staged rollouts, test the full cycle—enable, write additive state,
  and disable or roll back—confirming older clients and existing user data remain intact.

## Rule 4 — Design and PR self-check

Every design proposal and PR that adds or changes domain behavior, queries, wire shapes, or client logic should pass
three checks:

1. **Server ownership**: Does any newly added logic—state transitions, validation/idempotency, querying/windowing,
   derived projections, permissions/affordances, orchestration, or cross-device state—belong in the Go server rather
   than a client?
2. **No shadow client rules**: Does this change add client-side domain logic, counts, affordances, or filters that can
   diverge across platforms—and if it is a justified offline or low-latency exception, is it consolidated in
   `native/app-core` and/or pinned by `spec/fixtures/` with a written reason?
3. **Additive compatibility and verification**: Are all new server wire fields strictly additive and optional
   (`omitempty`) for older or unmodified clients, backed by shared fixture coverage and lossless rollback verification
   where applicable?
