# Hub wire schemas

JSON Schemas for the wire shapes of domains the shared fixtures (`spec/fixtures/`) already pin (#7891 stage 5, #9117).
Each `<domain>.schema.json` generates the clients' types with `scripts/gen/extension_schema.py`, the same generator and
subset as `schema/extension/README.md`; the file's `x-out` names the generated TypeScript (`ts`), Swift (`swift`), Go
(`go`) and Kotlin (`kotlin`) files it wants. Swift and Kotlin types carry no prefix. Go and Kotlin take the package of
the directory they are written to (the generator checks that the files already there say the same); a Go file gets no
package doc and names its registry of definitions after the schema (`todoViewDefs`), since it shares its package with
hand-written code. Android and Go are generated only for a schema whose `x-out` names them.

```sh
python3 scripts/gen/extension_schema.py           # after changing a schema
python3 scripts/gen/extension_schema.py --check   # compare only (also part of scripts/ci/guards.sh)
python3 scripts/test/test_wire_schema.py          # --check, fixture samples hold to the schema, no hand-written copy
```

| File | Domain | Generated |
| --- | --- | --- |
| `agent-view.schema.json` | An Agent's five-phase view (#15397): `AgentPhase`, `AgentReason` (a closed union tagged by `kind`) and `AgentView`, which the manager sends as `Session.view` (`spec/fixtures/agent-view`, `docs/protocol/agent-states.md` "Five phases") | `server/internal/agentviewwire/agent_view_gen.go`, `apple/Packages/PersonaWire/Sources/PersonaWire/AgentViewWire.swift`, `android/core/contracts/src/main/kotlin/persona/core/contracts/generated/AgentViewWire.kt`, `web/src/lib/agent-view-wire.ts`, `harmony/entry/src/main/ets/models/AgentViewWire.ets`, `native/app-core/crates/persona-core/src/agent_view_wire.rs` (written, not yet a `mod` of `persona-core`) |
| `counts.schema.json` | `CountsView`, the counts one manager works out (#15512, `docs/protocol/counts.md`): each count id a named field with its number, its counted rows and the rows it leaves out, and the optional `totals` (`spec/fixtures/counts/view_samples.json`) | `web/src/lib/counts-wire.ts`, `apple/Packages/PersonaWire/Sources/PersonaWire/CountsWire.swift`, `server/internal/assistantwire/counts_gen.go`, `android/core/contracts/src/main/kotlin/persona/core/contracts/generated/CountsWire.kt` |
| `integrations-projection.schema.json` | The owning assistant's Integrations catalog and detail (#17762), with typed references, filter counts and section descriptors (`spec/fixtures/integrations-projection/views.json`) | `server/internal/assistantwire/integrations_projection_gen.go` |
| `recents.schema.json` | `RecentSetup`, New Agent's recent setups (`spec/fixtures/recents`) | `web/src/lib/recents-wire.ts`, `apple/Packages/PersonaWire/Sources/PersonaWire/RecentsWire.swift` |
| `todo-view.schema.json` | To-dos (#10226): a row's view (`TodoSummary`) and its asks (`OwnerAsk`), the list's counts (`TodoCounts`) and places (`TodoPlaces`) (`spec/fixtures/todo-view`, `spec/fixtures/todo-counts`); a to-do's conversation in rounds (`TodoTimeline`), its results (`TodoResult`) and what changed since the owner looked (`TodoRecap`) (#10465, `spec/fixtures/todo-rounds`), and what `GET /api/v1/todos/{id}` carries of them beside the row (`TodoItemDetail`); a decision push's `todo` (`TodoDecidePush`, #10465, `spec/fixtures/todo-push`) | `web/src/lib/todo-view-wire.ts`, `apple/Packages/PersonaWire/Sources/PersonaWire/TodoViewWire.swift`, `server/internal/assistantwire/todo_view_gen.go`, `android/core/contracts/src/main/kotlin/persona/core/contracts/generated/TodoViewWire.kt` |
| `todo-wait.schema.json` | To-do waiting reasons (#16168): `TodoWait`, `TodoWaitKind`, `TodoWaitPRBlocker`, and `TodoWaitContact`, computed once on the server and emitted as `wait` on `GET /api/v1/todos` and `GET /api/v1/todos/{id}` (`spec/fixtures/todo-wait`) | `server/internal/assistantwire/todo_wait_gen.go`, `apple/Packages/PersonaWire/Sources/PersonaWire/TodoWaitWire.swift`, `android/core/contracts/src/main/kotlin/persona/core/contracts/generated/TodoWaitWire.kt`, `web/src/lib/todo-wait-wire.ts`, `harmony/entry/src/main/ets/models/TodoWaitWire.ets` |

`x-out` may also name `arkts` (HarmonyOS: the TypeScript output, without what ArkTS lacks) and `rust` (serde types;
the crate that includes the file needs `serde` with `derive` and `serde_json`, and its `mod` line `#[rustfmt::skip]`,
since the generator writes the file without rustfmt). `ids.schema.json` and `agent-view.schema.json` ask for Rust; neither
file is a `mod` of `persona-core` yet. The generator is
`scripts/gen/wiregen/`, one module per language (`model.py` reads the schemas; `go.py`, `ts.py`, `arkts.py`,
`swift.py`, `kotlin.py`, `rust.py`); `scripts/gen/extension_schema.py` stays the command.

## Typed keywords (opt-in)

The keywords below turn the open shapes the generator writes by default into closed types (#15397, the typed core
plan's 2.1–2.5). Each is off unless a schema names it, so a schema without them generates the same bytes as before;
`scripts/test/test_wiregen_typed.py` checks every one in every language against
`scripts/test/fixtures/wiregen/typed.schema.json` and compiles the output where the toolchain is on the machine. A
misused keyword stops the generator.

| Keyword | Where | Generates |
| --- | --- | --- |
| `"x-enum": "closed"` | a string `enum` definition | A real enum with an explicit unknown that keeps the raw value: Go `type Phase string` with its constants, the ordered `Phases []Phase` and `Valid()` (an unknown value keeps its string and is not valid); Swift `enum Phase { case needsYou, …; case unknown(String) }` with `init(rawValue:)`, `rawValue` and hand-written `Codable`; Kotlin `sealed interface Phase` of `data object`s plus `Unknown(raw)`, `Phase.of(raw)` and a string serializer; TypeScript and ArkTS the literal union without `(string & {})`, plus `UnknownPhase` and `parsePhase(raw)`; Rust `enum Phase { …, Unknown(String) }` with `parse`, `as_str` and serde impls. A field of the enum has the enum's type; a required one defaults to the unknown `""` where a language defaults. A value whose name would be the type's own (`unknown`, `all`) takes a `Value` suffix. Every value must make a name (letters and digits, a letter first). |
| `"x-enum": "closed"` on a `oneOf` union | a discriminated union | Go's discriminator field gets a named type, `<Union><Tag>` (`AgentReason.Kind` is `AgentReasonKind`), with typed constants, the ordered `<Union><Tag>s` and `Valid()`; a tag Go does not know keeps its string. Swift, Kotlin, TypeScript, ArkTS and Rust already decode an unknown tag to the union's unknown case, so their output is the same as without it. `x-unknown` does not go on a union. |
| `"x-unknown": "count"` | with `"x-enum": "closed"` | Decoding a value the enum does not know also calls the file's defect hook, named after the schema (`todo-view` → `TodoViewWire`): Go `var TodoViewWireUnknownEnum func(enum, raw string)`, Swift `TodoViewWireDefects.unknownEnum`, Kotlin `TodoViewWireDefects.unknownEnum`, TypeScript and ArkTS `TodoViewWireDefects.unknownEnum` (called from `parseX`), Rust `static TODO_VIEW_WIRE_UNKNOWN_ENUM: OnceLock<fn(&str, &str)>`. Unset, nothing is called; set it once at start. |
| `"x-id": "<Name>"` | a string definition, repeating its name; or a string property naming such a definition, in its own file or in `ids.schema.json` | An ID type: Go `type SessionID string`; Swift `struct SessionID: RawRepresentable, Codable, Hashable, Sendable` encoding as the bare string; Kotlin `@JvmInline value class SessionID(val raw: String)`; TypeScript `string & { readonly __brand: "SessionID" }` with the constructor `sessionID(raw)` (ArkTS: a plain `string` alias, it has no intersection types); Rust `#[serde(transparent)] struct SessionID(pub String)`. Fields referencing it have that type. A property naming one of `ids.schema.json`'s takes that file's type, written once there: TypeScript and ArkTS `import type` it from that file's output, Go imports `wireids`, Swift and Kotlin find it in the same module and package; Rust does not take one yet. |
| `"x-text": "<Name>"` | a string definition, repeating its name; or a string property naming such a definition | A label: free text the sender chooses, not a closed set (an extension's own word for a state). Go `type AssistantEventState string`; TypeScript and ArkTS a plain `type AssistantEventState = string` alias, so callers passing string literals keep compiling; Swift, Kotlin and Rust the shape of an `x-id` (a `RawRepresentable` struct, an inline value class, a transparent newtype) encoding as the bare string. Fields referencing it have that type; a property keeps its own `maxLength`. |
| `"x-tristate": "<Name>"` | a boolean definition, repeating its name; or a boolean property naming such a definition. The field must be optional and take no `x-go-pointer` | An optional boolean whose absence means something of its own, as one closed value in place of `*bool`: Go `type AssistantEventReplace uint8` with `<Name>Default` (absent), `<Name>True` and `<Name>False`, `IsZero`, `Or(absent bool) bool` and `MarshalJSON` / `UnmarshalJSON`, its fields tagged `omitzero` so the default stays absent; `null` reads as absent, anything but a boolean fails to decode. TypeScript (`replace?: boolean`), Swift (`Bool?`), Kotlin (`Boolean?`) and Rust (`Option<bool>`) keep their optional boolean, which already has three states. |
| `propertyNames: {"$ref": <closed enum>}` with every value in `required` | a map property | A struct with one field per key, named `<Owner><Field>` (`TodoCounts.sections` → `TodoCountsSections`), in place of `map[string]int` / `[String: Int]`. Swift reads and writes it by key (`sections[.todo] += 1`, a `subscript` over the enum) and Kotlin reads it (`sections[TodoListSection.Todo]`); an unknown key reads 0 and writing one changes nothing. A map keyed by an open enum, or that does not require every key, stays a map. |
| `"x-decode": "strict"` | an object definition, or the file's root (then every object in it) | Decoding a missing required field or a mistyped one fails: Swift `try c.decode` / `try c.decodeIfPresent` in place of `try?` with a default, Kotlin required fields without defaults, Rust required fields without `#[serde(default)]`. A closed enum's unknown value still decodes, to its unknown. Go and TypeScript decode as before (Go's `encoding/json` already refuses a mistyped field). Not with `additionalProperties`. |
| `"x-typed-except": ["go"]` | the file's root, naming targets of its `x-out` | Those targets generate as if the schema had none of `x-enum`, `x-unknown`, `x-id`, `x-text` and `x-tristate`: open enums, strings, booleans and maps, byte for byte. A stage while one language's callers cannot move yet: `todo-view.schema.json` keeps Go open until the to-do files of #15068 can take the closed types (typed core plan P4b). |
| `"x-opaque": "<reason>"` | a definition of free JSON (no `type`, or an object without `properties`) | JSON that stays open on purpose, with its reason in the doc comment: Go `type ToolInput json.RawMessage` with pass-through `MarshalJSON` / `UnmarshalJSON`; Swift `typealias ToolInput = JSONValue`, Kotlin `= JsonElement`, TypeScript `= unknown`, Rust `= serde_json::Value`. The reason is required; on an inline property the keyword is refused (name a definition). |

Every sample a fixture holds for a definition must name exactly the schema's fields, at any depth, and every definition
must be reached by some sample (`test_wire_schema.py`). The clients' tests write each sample back through the generated
type and compare: Apple (`apple/PersonaTests/RecentSetupsTests.swift`, `apple/PersonaTests/TodoViewWireTests.swift`) the
bytes, Go (`server/internal/assistantwire/todo_view_gen_test.go`) and Kotlin
(`android/core/contracts/src/test/kotlin/persona/core/contracts/TodoViewWireTest.kt`) the JSON values; Go also holds
what it writes to the schema with `server/internal/jsonsubset`, as the tests of the code that builds these values
should, since a nil slice or map writes `null`. A sample writes an optional boolean only when it is true and leaves an
empty list or map out, as Go's `omitempty` does, and a time as Go writes it.

Hand-written copies of a generated type may not return: the test fails when a tracked file declares one of a schema's
names outside its generated files, in every language its `x-out` names and in those its `x-guard` adds (`arkts`,
`rust`: languages a copy could be written in before they are generated, or never are). `x-reserved` lists names a later
stage will define, guarded from now on. The rule is per schema, so `recents.schema.json` guards TypeScript and Swift
only (its Kotlin and Rust copies predate it). A decoder that reads the JSON key by key declares no type (HarmonyOS's
`Wire.text(obj, 'key')`), so `x-guard-keys` lists keys and values distinctive enough that the test also fails on a
quoted one in a Swift, Kotlin or ArkTS source the schema guards, outside its generated files and the tests. A key goes
there only while no client source quotes it for another reason; TypeScript and Go are not scanned for keys, since plain
code there compares the generated types' values.

## IDs and AgentRef (`ids.schema.json`, #15397)

`ids.schema.json` defines the first batch of contract IDs with `x-id` (`MachineID`, `SessionID`, `InteractionID`,
`TodoID`, `ConversationID`, `HubID`, `DeviceID`, `ExtensionID`, `ClientMsgID`, `InboxItemID`, `ConnectorID`,
`SubscriptionID`) and `AgentRef{machine, session}`, the one shape for "points at an Agent" (strict decode). Go's output is
the contract package `server/internal/wireids`. Core's opaque `agentref` types keep their names; the guard above skips
`server/internal/agentref` for those three names only (`OPAQUE_COUNTERPARTS` in `test_wire_schema.py`).

Another wire schema names these IDs with a property's `"x-id"` (`{"type": "string", "x-id": "MachineID"}`), never a
copy of the definition: the generator reads `ids.schema.json` beside each schema (`SHARED_WIRE` in
`scripts/gen/extension_schema.py`) and the outputs refer to its types (see `x-id` above). A `$ref` across files is not in
the subset, so the fixture checks and JSON Schema validators read the property as the string it is on the wire.
`todo-view.schema.json` is the first to do so. Follow-up, not built: wiring the generated Rust file
(`native/app-core/crates/persona-core/src/ids_wire.rs`, written but not yet a `mod`) into `persona-core`, and a Rust
target taking shared IDs, are left for P5.
