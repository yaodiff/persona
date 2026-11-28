# Plan: a strongly typed Core boundary, generated for every client (#15397)

**Status: P0 to P3 in progress (2026-11-25); D-A, D-B and D-C decided by the owner on 2026-11-25 (section 2.1); how the
five Agent phases change existing screens is still open (section 5).** Each step is its own PR under #15397. **Who it
serves:** everyone indirectly: Assistant users stop seeing badges and states that disagree between devices, Agents users
get one meaning for each state on every client, and contributors get compile errors instead of silent mismatches. No
step before P5 changes anything people see.

**How the facts were taken:** read on `origin/main` at 51bbeec35 (2026-11-25, #14312); nothing was built or run for the
inventory. Counts in section 1 are grep estimates; P1's go/ast counter is the authority and its baseline replaces them.
Paths are relative to `server/internal/` unless they start with a top-level directory. Line numbers drift; find code by
name. Paths that do not exist yet are plain text.

## 0. Summary

1. **Core's inside already has a strong typed foundation; its outward boundary has almost none.**
   `server/internal/archguard/domainrules` gives `//persona:layer core` packages more than 50 rules (`core-any`,
   `core-primitive-field`, `seam-any`, ...), with existing violations held by a count that may only fall
   (`server/internal/archguard/testdata/domain_layers/baseline.txt`). `agentref` already has opaque `MachineID`,
   `SessionID` and `InteractionID`. But the contract layer (`proto`, the `*wire` packages, `nodeproto`, `rpc`, `modapi`)
   is checked only for imports (`TestContractsArePure` in `server/internal/archguard/archguard_test.go`), not for type
   strength, and that layer is what clients see.
2. **Weak types on the boundary** (16 wire packages, tests excluded): about 233 bare `string` state / kind / type
   fields; about 532 bare `string` `*ID` fields and no named ID types; 11 `map[string]any` and about 122
   `json.RawMessage`; about 100 outbound WebSocket events built as `map[string]any{"type": …}`, whose only list is prose
   in `docs/protocol/protocol.md`; 55 enum-like TEXT columns in storage, 18 of them with a CHECK.
3. **Clients compute what Core should.** "Needs you" is computed by 19 functions in 5 code bases; 26 of the 32 rows in
   `spec/fixtures/counts/surfaces.json` are marked `differs`. Only 39 definitions of the hub protocol are generated from
   a schema (to-dos and recents); sessions, interactions, the Inbox and the WebSocket envelope are handwritten on every
   client.
4. **The existing generator produces weak types itself.** `scripts/gen/extension_schema.py` emits enums as open string
   constants (TypeScript `| (string & {})`; Swift `enum X { static let … }` beside `var section: String`) and enum-keyed
   maps as `[String: Int]` / `map[string]int` (`TodoCounts.sections`). Generated Swift swallows decode errors with
   `try?` in 105 places. **So the generator is upgraded first, then used to push outward.**
5. **Direction: JSON Schema in `schema/wire/` is the single source; it generates Go, TypeScript, Swift, Kotlin and Rust.
   Core (the managers) computes derived values, such as the five-phase Agent view and each surface's "Needs you"
   count; clients only draw.** Constraints are ranked types > archguard / lint > CI gates > docs, and existing
   violations are held by ratchets that only fall.
6. **Alongside Core slimming** (#13430 and #13411, now moving the recording writers #13407 and the Lark board #13404): the first four steps touch only new
   files (`schema/wire/*`, the generator, new guards); `proto.Session` only gains fields; the paths slimming is moving
   get ratchets, not rewrites; ratchets count globally with name lists, so moving a type from `kbwire` to
   `recordingwire` is neutral.

## 1. Inventory: weak types on Core's boundary

Users of each item: S Swift (iOS, Mac, watch, Vision), K Kotlin (Android), W web (and desktop), H HarmonyOS (ArkTS),
R the shared Rust core, M modules and extensions, N nodes.

### 1.1 String states and kinds (closed sets typed as `string`)

| # | Where | Today | Used by | What went wrong |
|---|---|---|---|---|
| A1 | `proto/session.go` `Session.State string`, six untyped constants | no named type | S K W H R | web's `stateOf` trusts `s.state` with no runtime check (`web/src/lib/format.ts`); each client falls back differently (1.4) |
| A2 | `proto/session.go` `Session.Status string`, seven constants | overlaps A1 | S K W H | `agentState` in `assistant/fleet.go` has no `StatusInterrupted` branch, so it lands in `unknown` and clients read `status !== "interrupted"` themselves (10 places in web and Android) |
| A3 | sub-states as flags: `BackgroundWorking/Waiting`, `AwaitsReply/Asks/AsksRead`, `Work.just_finished` | `docs/protocol/agent-states.md` describes about 11 visible states; the wire has 6 states + 7 statuses + 4 flag groups | S K W | three separate sort orders (`AgentListOrder.rank`, `agentRank`, `lib/format.ts`); iOS and Android tab priorities differ |
| A4 | `proto/item.go` `Item.Role/Kind string`; some kind values only in comments | half named | S K W R | #15051: `MapComponent` joined the union but the renderer's property allow-list lacked `map` |
| A5 | `proto/interaction.go` `Interaction.Kind` (question, plan, permission, screen, elicitation) | untyped constants | S K W M | whether `screen` counts as "Needs you" (D11 of #11679) was disputed because kind carries no behaviour |
| A6 | `proto/message_send.go` `MessageSendState`, `TodoSendState` `Type`/`State` | bare string | S K W | — |
| A7 | `proto/computer.go` `ComputerJob.Kind/State`, values only in comments | bare string | S W | — |
| A8 | `relayproto/relayproto.go` `PushRequest.Type` (alert, background, liveactivity, voip) | bare string | push relay | — |
| A9 | generated `assistantwire/todo_view_gen.go` phases, sections, reasons, line keys, entry types | generated, but fields stay `string` plus a `[]string` list | S K W | #11731: Swift assembled line keys itself until they were generated |
| A10 | `sessionlist/search.go` and `sessionlist/params.go` each hard-code the six states; `extension/looks.go` has a different five (with `thinking`) | one set copied three times, one copy different | M | — |
| A11 | client-private narrow enums (`HomeSnapshot.swift`, `WatchPet.swift`, `AssistantActionBar.swift`, `DirectionTree.swift`/`.kt`; Android checks both `needs_user` and `needs_you`) | each client invents a vocabulary | S K | #4035: Android's `@WireEnum` silently decoded the new notify kind `reply` as `unknown` and the reminder never fired; #4399: iOS decoded an unknown `repeat` as `once` and dropped `cron` on write-back (`docs/lessons/android.md`, `docs/lessons/assistant.md`) |

Estimated counts: State/Status/Phase 99, Kind 104, Type 30, **233** in all (proto 107, assistantwire 46, recordingwire
18, the rest scattered). Wire packages have only about ten named string enums (`StartStatusState`, `StopCause`,
`MCPWaitKind`, `SessionEchoState`, `cliwire.ErrorKind`, `connectorwire.Scope/Tier/Capability`,
`repositorywire.Problem/Layout`).

### 1.2 `map[string]any` and `json.RawMessage` pass-through

| # | Where | Passed to | Note |
|---|---|---|---|
| B1 | `proto/item.go` `Item.ToolInput`, the same field in `proto/interaction.go` | S K W R | every tool call; #13858: agy stored arguments as a JSON string and paths kept their quotes |
| B2 | `proto/interaction.go` `RequestedSchema`, `Content map[string]any` (elicitation) | S K W M | MCP forms: legitimately open JSON, to be marked opaque |
| B3 | `extwire/wire.go` `Item / Computer / Browser / IPhone` RawMessage, `Detail map[string]any` | M W | — |
| B4 | `assistantwire/settings.go`, `assistantwire/agents.go` `Params map[string]any` | S K W | settings should be a tagged union |
| B5 | `assistantwire/wire.go`, `outbox.go` `Message`; `inbox.go` `Item`; generated `todo_view_gen.go` `Todo` | S K W | the generator leaves this door open too |
| B6 | `devicewire/wire.go` `Entry`, `Machines []RawMessage`, `Configure map[string]RawMessage` | S K W | — |
| B7 | `recordingwire/recordings.go`, seven RawMessage fields | S K W | **slimming is moving this: ratchet only** |
| B8 | `relayproto/relayproto.go` `APS map[string]any` | relay | APNs is open JSON; mark opaque |
| B9 | handlers: `writeJSON(v any)` in `hub/http.go`, `devices/http.go`, `extmgr/helpers.go` writing `map[string]any` in 289 places (hub 71, assistant 164, devices 17, extmgr 37) | all | these responses have no type at all; clients reverse-engineer them |
| B10 | `modapi/modapi.go` `AddMCPTool(…, run func(ctx, string, string, json.RawMessage) (string, error))`; `Setting.Value/Check func(json.RawMessage) json.RawMessage` | M | two positional bare strings (machine, session) are easy to swap. **This is slimming's module port: agree with its leader.** The tool slot is typed since P8 part 1 (`modapi.MCPTool`, `ToolCall`, `Tool[A]`; `docs/architecture/core-and-modules.md`, "Slots"); `Setting`'s functions remain |
| B11 | `extension/manifest.go` `Contributes map[string]json.RawMessage`, `Configuration` | M | `schema/extension/manifest.schema.json` exists; Go does not use generated types |

Estimated counts: 11 `map[string]any` and about 122 RawMessage in wire packages, no bare `any` fields; about 1,615
`map[string]any{` literals in the handler layer (extmgr, assistant, devices, hub).

### 1.3 Bare string IDs

- **About 532 `*ID string` fields in wire packages**: proto 200, assistantwire 182, devicewire 44, connectorwire 26,
  recordingwire 24, cliwire 20, repositorywire 15, kbwire 12, the rest scattered.
- Named ID types in the wire layer: **none**. Core has `agentref.MachineID/SessionID/InteractionID`, but they are opaque
  structs that never reach the wire.
- Typical: one struct in `proto/session.go` holds `MachineID`, `SessionID`, `ParentSessionID`, `RootSessionID`,
  `SourceThreadID`, `PaneID`, `TabID` and `WorkspaceID` side by side, and assigning one to another compiles. `modapi`'s
  `run(ctx, string, string, …)` likewise.
- What went wrong: #5537, an Inbox item's `source` string was used as a connector ID, so for `sub_…` every client got
  404 and "Retry" could never succeed; #1852 / #1909, "is this to-do a task" was tested as `origin != ""` in about 70
  places (`docs/lessons/assistant.md`).

### 1.4 Values clients derive from lists themselves

**"Needs you"**: 5 code bases, 19 functions (`docs/plans/plan-derived-counts.md` section 2). Representative: iOS
`AppModel.countBadges` → `AgentsNeedsYou.badge`, `AgentsNeedsYouRows`, `AssistantScope`, `AppIconBadge`,
`HomeWidgetWriter`, `WatchPet`, `IntentSupport`; Android `ShellCounts.kt` (which also fabricates a Session with
`state="needs_you"`), `Root.kt`, `AgentsListInfo.kt`, `NeedsYou.kt`, `HubManagers.kt`, `MergedServices.kt`; web
`web/src/lib/sessions.ts`, `current-assistant.ts`, `manager-attention.ts`, `inbox.ts`, `todo-triage.ts`,
`agent-filter.ts`, `views/Machines.tsx`, `desktop-island.ts`, `desktop/src/badge.ts`; Rust `persona-core` `sort::counts`;
HarmonyOS `MachineDirectory.ets`.

The Inbox has three counting units and the To-dos badge four rule sets. The only counts the server sends: FleetState
(default manager only), Persona Link's `needs_you`, `inbox_needs_you` (`hub/managers.go`) and `TodoCounts`; no hello or
push carries a badge.

**State mapping** (one wire value, different fallbacks):

| Client | Where | Unknown value | Old hub without `state` |
|---|---|---|---|
| Swift | `apple/Shared/Models.swift` | `.unknown` | blocked → needsYou, done → unread |
| Kotlin | `core/contracts/.../Models.kt` | `Unknown` | as Swift |
| Rust | `persona-core/src/state.rs` | `Unknown` | as Swift |
| **Web** | `web/src/lib/format.ts` | **passed through**, still typed `AgentState` | **`pending_interactions > 0` → needs_you; done/unknown → idle** (unlike the other three) |
| HarmonyOS | `MachineDirectory.ets`, `AgentTaskGroupRows.ets` (which mixes in `'stuck'`) | falls to the secondary color | — |

Swift also changes state locally (marking read turns unread into idle in `Models.swift`); Inbox status strings are
handwritten in `apple/Shared/InboxNow.swift` and Android's `InboxFollow.kt`, and only Android's has `failed`.

What went wrong: a564bf5c2 (#12977), the "Needs you" badge counted finished tasks carrying an `ask`, disagreeing with
triage; 1aeee8d0e (#14385), iOS's To-dos badge drifted from web's `ownerNeeds`; 401c83306 (#11527), `unknown` was read
as stopped and dispatch took a half-written reply as the final result.

### 1.5 Combinations of optional fields standing for a state

| # | Where | Combination | Should be |
|---|---|---|---|
| C1 | `assistant/todos.go` (HTTP response) | no status field; derived from `DoneAt *time + DoneBy + DoneReason + CancelledAt *time + CancelledReason + StartError + ReopenedAt *time` | a `TodoLifecycle` union: `Open / Done{by, at, reason} / Cancelled{at, reason} / StartFailed{code}` (the generated view has a phase, the raw response does not) |
| C2 | `proto/session.go` | `TurnError + TurnErrorKind + TurnErrorCode + TurnErrorResetsAt *time + TurnErrorResumes bool + TurnErrorAfterWork bool`, plus `StartError + StartErrorCode + StartErrorArgs map[string]string` | an `AgentError` union: `UsageLimit{resetsAt, resumes} / Model{code} / StartRefused{code, args}`, the source of the new `error` phase |
| C3 | `proto/session.go` `PendingInteractions int` decides needs_you; `PendingEffort *string + EffortChangeFailed bool`; `Ultracode *bool + UltracodeLostAt *time` (nil = unknown); `RequestedModel *string` (nil = old node, `""` = launcher default) | nil, empty and value overloaded on one pointer | an explicit `Known<T> = Unknown / Default / Value(T)` |
| C4 | `proto/interaction.go` `Resolution.Dismiss bool + Pending bool` | non-final close | a `ResolutionState` enum |
| C5 | `proto/session.go` `NativeArchive`, `UndeliveredMessage` `State + Error + NextAt *time + DeleteOnly` | Error means something only in one State | each State carries its own payload |
| C6 | `proto/computer.go` `ComputerJob.Code` means different things while running and after failure | — | as C5 |

Estimated counts: `*time.Time` 54, `*bool` 52, `*Error string` 45, Pending/Failed/Done-style booleans 26.

### 1.6 The WebSocket envelope and the internal bus

- **Inbound** (`hub/http.go`): one anonymous struct of about 40 fields (the union of every command's fields), then a
  `switch msg.Type` over 22 literals. **Unknown types and JSON errors are dropped silently.** Scoped tokens keep a
  separate literal allow-list (`hub/scoped_tokens.go`).
- **Outbound**: no envelope type. `Hub.broadcast(v any)` recovers the type with `m["type"].(string)`;
  `assistant/service.go` `event(ctx, ev map[string]any)` likewise. 116 `"type": "…"` sites, 49 distinct types, passed by
  the hand-kept `managerMessageTypes` (41) and `hubMessageTypes` (about 40) in `hub/bus_broadcast.go`. **The full event
  list exists only as a table of about 97 rows in `docs/protocol/protocol.md`.**
- **Bus / RPC**: `rpc.Handler func(ctx, method string, params json.RawMessage) (any, error)`, `Peer.Call(ctx, method
  string, params, out any)`; 43 `handleBus("…")` registrations on the hub. About 324 dotted names are `Method*/Event*`
  constants, but **names are not bound to parameter and result types**. On the node only 37 methods use the typed
  `nodeRPC[P,R]` (`node/node_methods.go`); the rest go through `handleMethod`'s prefix and switch with a shared anonymous
  parameter struct of about 110 fields.
- **Extensions and connectors**: `extmgr/repository_providers.go` `repositoryCall(…, method string, …)` called with five
  literals such as `"repo.parse"`; 21 distinct `.Call(ctx, "literal"` in extmgr, extension, assistant and devices;
  connectors have about 45 method constants and a switch of about 35 branches (`connector/serve/handle.go`).

What went wrong: ed7ce33f3 (#13037), `connectorwire.InboxThing` gained `thread_state` but the plugin schema did not,
main went red and deploys stopped; 0eee339ce (#14786), a new `Session` field slipped into the handwritten encoder's layout
hash and every Session fell back to `json.Marshal`.

### 1.7 Storage

- 55 `*status|kind|state|role|phase|outcome` TEXT columns, 18 with a CHECK, almost all from tables created after
  2026-11-19. Missing, for example: `interactions.kind/status` (manager `0001_init.sql`), `0231_assistant_tasks.sql`,
  `0405_todo_supervision.sql` (`DEFAULT ''`), `0487_todo_kind.sql`, `0531_urgent_calls.sql`, `a2a_tasks`,
  `assistant_captures`, and on the hub `0031_device_tokens.sql`, `0311_bus_identities.sql`.
- About 78 jsonb columns, shapes mostly unchecked; `interactions.payload/resolution` fully opaque.

### 1.8 What already exists to build on

| Facility | Governs today | Missing |
|---|---|---|
| `archguard/domainrules` (`//persona:layer`) | core packages: no `any`, no bare primitive fields, no exported fields; app packages: `app-named-primitive` and others, counts only fall | the contract layer is outside the rules |
| `archguard/testdata/contracts.txt` + `TestContractsArePure` | contracts import only the standard library and other contracts | field types |
| `central_lists_test.go`, `typed_handle_test.go`, `TestNodeHandleMethodFreeze` | freeze central lists; move node methods to the typed path | WebSocket events and bus methods |
| `schema/wire/` + `scripts/gen/extension_schema.py` + `scripts/test/test_wire_schema.py` | four languages generated, fixture replay, handwritten copies blocked (`x-guard`) | two domains only; open enums; no ID types; enum-keyed maps become `map[string]` |
| `persona-core-typegen` | bincode codecs between the Rust core and clients | hub JSON; web does not use it |
| `spec/fixtures/needs-you`, `spec/fixtures/counts/surfaces.json`, `counts_agreement.py` | pin each client's counts to fixtures, record differences | a record, not a gate |
| `scripts/ci/lower_baselines.py`, `scripts/ci/ratchet_base.py`, `scripts/ci/changed_ratchets.py` | the count ratchets that only fall | no type ratchet |
| #15299 `fleetcount`, #15202 `kernel/seam`, #14928 Core change gate (all in review) | "Needs you" rules in one strict core; module registration through a typed seam; owner review of Core paths | this plan stacks on them rather than starting a parallel track |

## 2. Target types

### 2.1 Closed enums with an explicit unknown

Rule: **a set is closed and clients must switch over it exhaustively; a value the client does not know decodes to
`.unknown(raw)`, never to some real value and never to a pass-through string.** This closes both #4035 (silently unknown,
nobody notices) and #4399 (decoded into a wrong real value).

| Language | Shape |
|---|---|
| Go | `type AgentPhase string` + constants + `func (AgentPhase) Valid() bool` + an ordered `AgentPhases` list; `UnmarshalJSON` maps an unknown value to `AgentPhaseUnknown` keeping the raw string, and counts it |
| Swift | `enum AgentPhase: Hashable, Sendable { case needsYou, working, done, error, idle; case unknown(String) }` with handwritten `Codable`; no `@unknown default` escape generated |
| Kotlin | `sealed interface AgentPhase { object NeedsYou … ; data class Unknown(val raw: String) }` with a custom serializer; `when` is checked exhaustive |
| TypeScript | `type AgentPhase = "needs_you" \| … \| {unknown: string}` **without** `(string & {})`; a generated `parseAgentPhase(raw)`; eslint `switch-exhaustiveness-check` |
| Rust | `#[serde(other)]` or a custom `Unknown(String)` |

Schema: `"x-enum": "closed"` (new) and `"x-unknown": "count"` (decoding an unknown value records a defect). Open enums
stay as they are until each domain moves to closed, so 14 or more Swift files are not broken at once.

**Agent states become five** (`AgentPhase`):

| Phase | Absorbs today's | Reason, as `AgentReason` (a tagged union, 2.2) |
|---|---|---|
| `needs_you` | `state=needs_you` (`PendingInteractions>0` or `Status=blocked`) | `Cards{n, held}` / `TerminalDialog`; whether it counts comes from Core's `counted: bool` and `left_out: Muted / AssistantLooking` (D6 / D11 of #11679) |
| `working` | `working`, including background running and background waiting | `Turn` / `Background{waiting: bool}` |
| `done` | `unread`, "just finished" (`work.just_finished`) and "awaiting your reply" `awaits_reply` (D-A) | `Unseen` / `JustFinished` / `Asks{question}` |
| `error` | **new**: `turn_error*`, `start_error*` and `interrupted` | `UsageLimit{resets_at, resumes}` / `Model{code}` / `StartRefused{code, args}` / `RestartInterrupted{resumable}` |
| `idle` | `idle` (seen), `offline`, `unknown` | `Seen` / `NotRunning` / `HostUnknown` |

- The manager computes it in `fleetcount` and sends it as `Session.view: AgentView{phase, reason, counted, rank}`.
  `rank` replaces the three clients' separate sort orders.
- The old `state` / `status` fields are frozen and still sent for old clients; new clients read only `view`.
- **Decided by the owner on 2026-11-25 (all three as recommended):**
  - **D-A**: "awaiting your reply" is `done` with reason `Asks{question}` and `counted=false`; it does not count toward
    "Needs you".
  - **D-B**: `unknown` is `idle` with reason `HostUnknown`; screens may say "status unknown".
  - **D-C**: `interrupted` is `error` with reason `RestartInterrupted{resumable}`, carrying a "Continue" remedy.
- Still open: how keeping only five phases changes existing screens (section 5).

### 2.2 Tagged unions instead of optional-field combinations

The generator already supports `oneOf` + `x-discriminator` with an unknown case. Uses: `AgentReason` (above),
`AgentError` (C2), `TodoLifecycle` (C1), `ResolutionState` (C4), `UndeliveredState = Waiting{next_at} / Failed{error,
delete_only}` (C5), three-state `Known<T>` (C3; Go gets concrete `KnownModel`, `KnownBool`, since the core rule
`core-type-param` forbids generics), and settings `Params` (B4).

**WebSocket events**: `ServerEvent` and `ClientCommand`, each a union tagged by `type`, replace the anonymous inbound
struct, `map[string]any{"type"}`, `managerMessageTypes` and the table in the docs.

### 2.3 ID newtypes

- Schema: `"x-id": "SessionID"`.
- Generated: Go `type SessionID string` (contract layer, serializable); Swift `struct SessionID: RawRepresentable,
  Hashable, Codable, Sendable`; Kotlin `@JvmInline value class SessionID(val raw: String)`; TypeScript `type SessionID =
  string & {readonly __brand: "SessionID"}`; Rust `struct SessionID(String)`.
- Compound reference: `AgentRef{machine: MachineID, session: SessionID}` is the one shape for "points at an Agent",
  replacing `[2]string` and paired string parameters; aligned with the `ref` in #15299.
- Between the contract `SessionID` and core's opaque `agentref.SessionID`, the adapter converts exactly once
  (`agentrefadapt` exists).
- First batch: `MachineID`, `SessionID`, `InteractionID`, `TodoID`, `ConversationID`, `HubID`, `DeviceID`,
  `ExtensionID`, `ClientMsgID`, `InboxItemID`, `ConnectorID`, `SubscriptionID` (#5537 came from mixing the last two).

### 2.4 Derived values Core computes

- **One "Needs you"**: one number per surface, computed by the manager (`fleetcount.AgentsNeedsYou(surface, …)`,
  #15299), sent in a generated `CountsView` (step 6 of #11679):
  `CountsView{agents_needs_you, inbox{actions, unread}, todos{needs_you, ...}, persona_tab, app_badge, widgets...}`,
  **one named field per surface, no `map[string]int`**.
- `TodoCounts.sections` becomes a struct with one field per section (the generator turns a map whose
  `propertyNames` is a closed enum, all keys required, into a struct).
- Also sent by Core: `AgentView.rank`, `AgentView.counted`, the Inbox status (replacing two handwritten mappings), and
  the to-do phase (already there).
- **Client rule**: draw only, sort only by Core's `rank`; local optimistic updates (marking read) go through a
  transition function Core provides (the Rust core or a generated `apply(event)`), never `state = .idle` by hand.
- Old hubs: "no `view`, so infer it" is written in **one** place, the Rust core's `state.rs` or a generated fallback that
  web calls through its TypeScript output; no per-client fallback (the web divergence in 1.4 came from that).

### 2.5 Explicit opaque

Genuinely open JSON (MCP `RequestedSchema`, `ToolInput`, APNs `aps`, extension `Contributes` values) stays RawMessage,
but the schema must say `"x-opaque": "<reason>"`, and Go gets a named type `type ToolInput json.RawMessage`. RawMessage
and `map[string]any` without that mark go into the ratchet and only fall.

### 2.6 One set of types for Swift, Kotlin, TypeScript and Rust

**Single source: `schema/wire/*.schema.json`. Generator: `scripts/gen/extension_schema.py`, kept, not replaced.**

Upgrades, each switched on per schema so existing output stays byte-identical:

1. `x-enum: closed` generates the enums of 2.1;
2. `x-id` generates the newtypes of 2.3;
3. enum-keyed maps with every key required generate structs;
4. `x-decode: strict`: Swift throws instead of `try?` and reports a defect, Kotlin stops coercing defaults (#2589);
5. `x-out: rust`: serde types that `persona-core` uses directly (todo-view today has only `x-guard: rust`, Rust still
   handwritten);
6. `x-event`: Go constructors generated from the `ServerEvent` union, `managerMessageTypes` and the event table in
   `docs/protocol/protocol.md` become generated output (`scripts/test/test_doc_wire.py` compares against it);
7. `x-rpc`: method descriptors `Method[P,R]{Name}`; on Go `rpc.Call(ctx, peer, assistantwire.MethodX, params)` infers
   the result type; the node's `nodeRPC[P,R]` is the model.

**Against `persona-core-typegen`**: it keeps the bincode FFI between Rust and the clients. Hub JSON types are generated
as Rust structs from the schema and reflected by typegen, so both sides agree.

Each domain follows the path todo-view already took: schema → generate → `spec/fixtures/<domain>` samples → replay tests
on every client → `x-guard` blocks handwritten copies → delete the old handwritten types.

## 3. Where each constraint lives (strongest first); existing code held by ratchets

| Strength | Means | Governs | Existing code |
|---|---|---|---|
| **1 types** | generated closed enums, ID newtypes, unions, named count structs; exhaustive switches in Swift, Kotlin and TypeScript; Go `Valid()` plus one adapter conversion | a wrong reading does not compile | new domains are typed from the first line; old ones move per section 4 |
| **2 lint / archguard** | new contract-layer rules in `domainrules`: `contract-bare-enum`, `contract-bare-id`, `contract-untyped` (`map[string]any` / RawMessage without `x-opaque`), `contract-optional-state` (count only), `wire-inline-event` (`map[string]any{"type":` in handlers), `wire-literal-call` (`.Call(ctx, "literal"`). Clients: eslint `switch-exhaustiveness-check` and no `.state ===` comparisons; a Swift source scan forbidding `default:` on generated enums and string comparison of state; a detekt rule forbidding `== "needs_you"` on Android; ArkTS through `x-guard-keys` | new code cannot add more | same format as `domain_layers/baseline.txt`, global counts plus name lists |
| **3 CI gates** | `scripts/test/test_wire_schema.py` requires a fixture for every `ServerEvent` and method; `counts_agreement.py` becomes a gate and `differs` in `surfaces.json` only falls (26 → 0); a migration lint: new enum-like TEXT columns need a CHECK, new jsonb columns a shape or an opaque note; unknown-enum defects on staging must be 0 | clients agree; storage holds no illegal values | old tables get CHECK `NOT VALID` then `VALIDATE`, one table per PR |
| **4 docs** | the event table in `docs/protocol/protocol.md` and the state table in `docs/protocol/agent-states.md` are generated or compared against generated output; `docs/server/architecture.md` gains "Type rules for the contract layer" | for people | — |

**The ratchet** (P1; the script is scripts/test/test_wire_types_ratchet.py or its sibling under `scripts/ci/`, wired
into `scripts/ci/lower_baselines.py`):

- Scope: every package in `server/internal/archguard/testdata/contracts.txt`, plus handler-layer `writeJSON(map…)`,
  `map[string]any{"type"` and literal `.Call(ctx, "…")`.
- **Global counts and name lists, never per package**: a type moving from `kbwire` to `recordingwire` keeps its count and
  its name, so it is not reported. Only a new name is a rise.
- Counted with go/ast, not grep; the measured baseline replaces these estimates: bare state/status/phase 99, kind 104,
  type 30, `*ID string` about 532, `map[string]any` 11, RawMessage about 122, `*time` / `*bool` / `*Error` 54 / 52 / 45,
  outbound `{"type":` 100, handler `writeJSON(map)` 289, literal `.Call` 21. Later: enum columns without CHECK 37, client
  "Needs you" functions 19, `differs` 26, and per client the handwritten wire mirrors (Apple `Shared` about 616, Android
  `core/contracts` about 560, web `lib/types.ts` 53) and string comparisons of state.
- As with `domainrules`, **no ignore marker**: exceptions are written into the baseline with their PR.

## 4. Step-by-step PRs

Each step goes issue → claim → branch → PR and keeps this document current. "Conflict" means against what Core slimming
is moving: `server/internal/{kb,kbwire,recordingwire,modules/*,larkhost,modapi}`, the assistant's
`*lark*|*feishu*|briefing*|meeting_*` files, `web/src/modules/*`, the Apple and Android module packages, and the PRs in
review #14928, #15202 and #15299.

| Step | Scope | Risk | Against slimming |
|---|---|---|---|
| **P0 plan and decisions** | this document, its Docs index line, tracking issue #15397 with D-A / D-B / D-C | low | docs only |
| **P1 count-only ratchet** | the weak-type ratchet with a baseline, in `scripts/ci/lower_baselines.py` and `scripts/ci/changed_ratchets.py`, run by CI | low: a wrong baseline at worst reports falsely, fixed in the baseline file | **before everything else**, so moves made by slimming cannot add weak types; global counts keep moves neutral. The slimming leader reviews the counting rules before merge |
| **P2 generator upgrade** | `scripts/gen/extension_schema.py`: `x-enum: closed`, `x-id`, enum-keyed map → struct, `x-decode: strict`, `x-out: rust`, `x-opaque`; split by language; tests per keyword; **existing output byte-identical** (`--check`) | medium: four languages share it; Swift `Codable` is handwritten | none (`scripts/gen/` is not a slimming surface); likely a Core path under #14928, so `core-change` |
| **P3 ID newtypes (additive)** | new schema/wire/ids.schema.json; generated Go (contract), Swift `PersonaWire`, Kotlin, TypeScript, Rust; new schemas use `x-id`; a `contract-bare-id` rule counted by ratchet | low: only additions | none; `recordingwire`'s 24 IDs wait until slimming finishes |
| **P4 todo-view to closed** | `schema/wire/todo-view.schema.json` turns on `x-enum: closed` and `x-id`; `TodoCounts.sections` becomes a struct; call sites follow (about 14 Swift, 13 web, 25 Android files) | medium: many call sites; unknown sections from old hubs go to `.unknown` | unrelated to slimming; **conflicts with #15068** (to-dos by matter): after it lands, or agreed with its author |
| **P4a (done) todo-view closed on the clients** | the phase, section, reason, row line key and entry type closed with `x-unknown: count`; a run's machine and session, an ask's interaction and a push's to-do take `ids.schema.json`'s IDs (a property's `x-id` naming a shared file's ID); `TodoCounts.sections` a struct read by section; Swift, web and HarmonyOS call sites follow (Android reads no todo-view type outside its contract test). `TodoLine.key` stays a string: a timeline entry's line uses the keys of spec/fixtures/todo-rounds. Go is held open with `x-typed-except: ["go"]`, byte for byte as before | medium: Swift is checked by hosted Apple CI only | none of #15068's files |
| **P4b Go to closed, after #15068** | drop `x-typed-except` from `todo-view.schema.json`; Go's `assistantwire` then has `TodoPhase`, `TodoListSection`, `TodoReason`, `TodoLineKey`, `TodoEntryType` types, `wireids` IDs and a `TodoCountsSections` struct, and the to-do projection and its handlers follow (`server/internal/assistant` to-do files, `assistantwire/todos.go`, `hub/todo_view.go`'s sums field by field) | medium: the files #15068 changes | after #15068 merges |
| **P5 AgentView, five phases** | new schema/wire/agent-view.schema.json (`AgentPhase`, `AgentReason`, `AgentError`, `AgentView`); after #15299, `fleetcount` computes it onto `Session.view` (one new field in `proto/session.go`; `state` / `status` frozen); spec/fixtures/agent-view derived from the needs-you fixtures; `interrupted` no longer lands in unknown; the old-hub fallback only in Rust `state.rs` and its TypeScript output | medium-high: a visible change; screenshots per `docs/design/PRINCIPLES.md`, together with `docs/protocol/agent-states.md` | touches hot files (`assistant/fleet.go`, `proto/session.go`): small PRs. **Not** the Lark cards (`modules/lark` is moving); Lark reads `AgentView` through its port after the move |
| **P5a–d clients read AgentView** | one PR per client: web (`stateOf` reads `view`, its own fallback deleted) → iOS (`AgentState` becomes the generated type; local state change in `Models.swift` deleted) → Android → HarmonyOS and Rust; each deletes its handwritten enum and `x-guard` keeps it gone | medium: screenshots dark and light | client module packages untouched; where `AppModel` is being split, change types only in `Shared/`'s model layer |
| **P6 CountsView** | steps 6–13 of `docs/plans/plan-derived-counts.md`, not a second project; this plan adds only: one named field per surface (no map), and `counts_agreement.py` as a gate with `differs` ratcheted | medium | on #11679's schedule; #15299 is its step 5 |
| **P7 WebSocket event unions** | schema/wire/events.schema.json, **core families first** (sessions, machines, interactions, to-dos, assistant chats): Go constructors and client decoders; `managerMessageTypes` / `hubMessageTypes` generated; an inbound `ClientCommand` union replaces the anonymous struct in `hub/http.go`, unknown commands record a defect instead of vanishing. One family per PR | medium-high: `hub/http.go` and `assistant/service.go` are the hottest files in the repository; `server/internal/hub/architecture_guard_test.go` watches the hub | **recording, Lark, voice and briefing events wait until those modules move**; modules declare their own event schemas through `modapi` (`EventsDeclared` in `hub/bus_broadcast.go` is the start), agreed with the slimming leader: module event types are generated from the module's own schema, not added to Core's union |
| **P8 bus / RPC descriptors** | `Method*` constants in `assistantwire`, `devicewire`, `extwire` become `Method[P,R]`; `handleBus` takes descriptors; `repo.*` and connector methods too; `wire-literal-call` ratcheted | medium: many mechanical changes, one method at a time | `modapi.AddMCPTool` is **done (part 1, the slimming leader's call)**: `AddMCPTool(modapi.MCPTool)` with `ToolCall{Machine wireids.MachineID; Session wireids.SessionID; Args ToolArgs}`, `Tool[A]` decoding arguments strictly and a named `ToolResult`; the old signature was replaced outright, since no module registered a tool |
| **P9 storage CHECKs** | a migration lint (new enum-like TEXT columns need a CHECK); one PR per old table, `ADD CONSTRAINT … NOT VALID` then `VALIDATE`; CHECK values compared with the schema's closed enums | medium: illegal values already stored fail `VALIDATE`; check data first; migrations named by UTC second | recording and Lark tables after their modules move (they may get module-owned tables) |
| **P10 delete handwritten mirrors** | per domain, delete the parts of Apple `Models.swift`, Android `Models.kt`, web `types.ts` that have generated types; where a client ratchet reaches 0, the lint turns from a count into a hard block | low to medium | client module packages move with their modules |

P8 part 2a introduces `buswire` and binds the seven `extwire` calls to their request and reply types. Source transfers
forward the original bundle JSON so newer managers can exchange fields the hub does not know. The existing method names
and empty-object acknowledgments stay on the wire.

**Order**: P0 → P1 → (P2 and P3 in parallel) → P4 → P5 → P5a–d / P6 (merging with #11679) → P7 → P8 → P9 → P10. P0 to P3
touch nothing slimming is moving and can start now; before each step from P5 on, check slimming's progress in #15397.

**Hard rules to stay out of slimming's way:**

1. Paths slimming is moving get ratchets only, no type rewrites; once a module lands, it writes its own schema in the
   **module** with the same generator.
2. `proto/*.go` only gains fields; existing field types do not change and frozen fields keep being sent.
3. New types live in new files (`schema/wire/*` and generated output); central lists register from each domain's own
   file as `central_lists_test.go` requires.
4. Ratchets count globally, so moves are neutral.
5. Every PR touching a Core path carries the `core-change` label and a reason (enforced once #14928 lands).

## 5. Risks and open questions

- **Forward compatibility vs closed sets**: closed enums with `.unknown(raw)` cover a new hub talking to an old client,
  but **adding a phase is itself a breaking change**; it is negotiated through `proto.Features` capability bits (clients
  declare the phases they know, the hub degrades for old ones). Part of P5's design.
- **Five phases against existing screens (open, owner)**: "waiting in the background", "awaiting your reply" and "just
  finished" each have their own look today. As reasons under five phases, whether screens still tell them apart is a
  design decision with screenshots per `docs/design/PRINCIPLES.md`. Under "Never change how an existing screen looks
  directly" it may have to ship as a new style or with the owner's explicit approval. Asked separately before P5.
- **532 Go ID fields** cannot all be replaced without colliding with everyone. Only new code must use ID types (with the
  ratchet); the hot types (`Session`, `Interaction`, `Todo`) change along with P5 and P7.
- The generator was a single 1,262-line stdlib-only Python file; P2 splits it by language so it does not become the
  next conflict hot spot.
- Section 1's counts are grep estimates (RawMessage counts include a few function signatures, `{"type":` includes some
  nested UI maps); P1's go/ast counts are the authority.

## 6. Decisions

| ID | Date | Decision | By |
|---|---|---|---|
| D-A | 2026-11-25 | "Awaiting your reply" is `done` / `Asks{question}`, `counted=false` | owner |
| D-B | 2026-11-25 | `unknown` is `idle` / `HostUnknown` | owner |
| D-C | 2026-11-25 | `interrupted` is `error` / `RestartInterrupted{resumable}` with "Continue" | owner |
