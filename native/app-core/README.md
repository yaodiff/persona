# Persona's shared client core

One Rust library under the iOS, Android and HarmonyOS clients' native UIs. Design, decisions and phases:
[the plan](../../docs/plans/plan-rust-core.md) (#6793). Phase 1 is #6871.

## Layout

| Crate | What it is |
| --- | --- |
| `persona-core` | Pure logic: no IO, no clock, no FFI, no `unsafe`. It holds `continuity`, `sync`, `transport` / `wire_envelope`, `sort` / `state` / `recents`, `turns` / `task_index` / `viewed_images`, `answer`, `stream_chunks` and the bounded `stream_repair` slices. Each implements only the fixtures and source-derived behavior listed below; no domain is a complete client UI model. |
| `persona-core-runtime` | Runs `persona-core` on one worker thread. `dispatch` is lock-free on the caller: it pushes an encoded action onto the worker's channel, with the pending count and stop flag in atomics. The worker applies actions, sends requests to the client and builds each chat's patch. `changed` wakes the client once per batch, and the client pulls the patch with `drain`. `wire` holds every type that crosses the boundary. |
| `persona-core-ffi` | The entry points, built as `libpersona_core`: C functions for Swift (`include/persona_core.h`) and JNI functions for Android's `persona.core.rustcore.NativeBridge`. Each catches panics and returns its failure value. The JNI dispatch moves the action bytes into the worker's channel without a second copy. |
| `persona-core-ohos` | HarmonyOS's single NAPI library. Its default host build tests the close-safe runtime registry and all shared fixtures; the `ohos` feature adds ohos-rs's NAPI entry points and thread-safe callbacks, supplied by the device runtime. |
| `persona-core-typegen` | Generates the clients' bincode codecs for the `wire` types: Kotlin to android/core/rustcore, Swift to apple/Packages/PersonaCore and ArkTS to harmony/entry/src/main/ets/core/rustcore. All are committed and checked for drift. |

The hub's item fields are preserved in `ListOp::Insert` / `ListOp::Update`, including supervision promotion retained by
iOS. An updated item keeps its stable list identity instead of becoming a remove/insert pair. Wire and snapshot parsing
share first-value-wins handling of repeated keys without normalizing those original item bytes; #8227's restoration
defect is fixed by #8922, with round-trip and parser regressions in `cargo test`.

## Current boundary evidence (#9236)

This is an inventory of merged code and reproducible tests, not a migration acceptance table. "Host" means a Linux or
macOS host. "Production" means the client calls the core for its authoritative result, not merely that an entry point,
codec or adapter exists. "Partial" always has its scope named. Empty cells mean **not implemented**; "not measured"
means an available path has not been run here. No new physical-device execution was performed for this audit, and the
required physical-device acceptance cells are **blocked: no physical device** (maintainer decision, 2026-11-14). The
one exception to "no domain-specific physical run" is the continuity benchmark's contributor-reported physical-arm64 /
4 KiB-kernel result below: it was not independently rerun and is not 16 KiB acceptance. Host, simulator and packaged-
emulator results never fill the physical-device column.

| Domain | cargo test | Host JNI | C ABI | SwiftPM | ArkTS host | Real NAPI | Production iOS / Android / HarmonyOS | Device |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `sync` | [10 iOS-derived cases / 120 steps](../../spec/fixtures/sync-driver/README.md): single-group reconnect backoff, event cursor and lifecycle (#9783) | | | | | | Native on every client | **blocked: no physical device** |
| `continuity` | 38 cases / 94 steps, including #8227 duplicate-key snapshot restoration | Full runtime corpus | Generic runtime C ABI is linked and exported; fixture replay is through SwiftPM | Full corpus through the real C XCFramework on macOS (#7160, #8972); native Store replay also exists | Full generated-codec / registry replay | Not measured; OHOS feature compiles, no loaded NAPI module | Native / native + DEBUG shadow (#7448) / native; iOS production shadow is unmerged #8970 | Android x86_64 packaged emulator replay passed; contributor-reported arm64 / 4 KiB benchmark exists, not independently rerun; physical 16 KiB acceptance blocked |
| `transport` / `wire_envelope` | 17 wire cases plus the 64 source-derived Envelope cases and nested-scalar regressions (#8955, #8995, #9082, #9187) | Full wire runtime plus diagnostic Envelope replay | Partial: `persona_core_transport_frame` covers the shared-scalar gate and Other routing, not the complete wire corpus | No raw-transport Swift binding/replay implemented; generic action codecs are not a full wire replay | Full wire decoder plus selected production Envelope refusals | Generic runtime NAPI unexecuted; separate wire/Envelope NAPI projection not implemented | Native everywhere; no production client sends `WirePayload` | No domain-specific physical run; acceptance blocked |
| `sort` / `state` / `recents` | 60 sort, 13 state and 21 recents cases | Sort 60 + state 13 (#9110); recents not implemented | Sort smoke; recents 11 conversation + 10 setup (#9654); state not implemented | Sort 60 on macOS (#9110); state/recents not implemented | Sort 60 + state 13; recents 11 conversation + 10 setup (#9584) | Sort/recents exports committed, actual NAPI not measured; state has no NAPI export | Native / native + DEBUG sort shadow (#8954) / **core Agents-list sort in production** (#8957, #8966, #9066); no state/recents migration | No domain-specific physical run; acceptance blocked |
| `turns` / `task_index` | 57 static turn cases and five task-index cases + receipt; four operation cases have host-JNI coverage | Turns 57 + 10 operation checkpoints; task index 5 + receipt | Turns 57; task index 5 + receipt | Turns 57; task index 5 + receipt on macOS (#8952) | Turns 57 + 10 operation checkpoints (#9235); task index 5 + receipt | Turns and task-index NAPI unexecuted; host turns is not #8808's Linux loader | Native everywhere | No domain-specific physical run; acceptance blocked |
| `answer` | 15 question + 9 recommendation cases (#9233), including newline / NBSP / TAB trimming, and the draft each click leaves (#9238) | Full 15 + 9 | Full 15 + 9 | Full 15 + 9 on macOS (#8952 plus #9233) | Full 15 + 9 through host projection | Not measured | Native everywhere | No domain-specific physical run; acceptance blocked |
| `stream_chunks` / `stream_repair` | Split 21; repair covers fence, table, final-marker, inline, math-end and plain-open-fence source-derived assertions | Split 21; repair 21 fence + 6 end, 22 table + 15 end, 14 final-marker, 51 explicit inline + 13 boundary, 6 explicit math end + 57 boundary expectations and 342 mixed-reply prefixes; plain fence 5 explicit + 31 boundary | Split 21; repair 80 complete objects (#9241); plain fence 5 explicit + 31 boundary (#9563) | Split 21 on macOS; repair not implemented | Split 21; repair 80 complete objects (#9241); plain fence 5 explicit + 31 boundary (#9563) | Split NAPI unexecuted; repair and plain-fence NAPI exports committed, real NAPI not executed | Native everywhere | No domain-specific physical run; acceptance blocked |
| `viewed_images` | Source-derived ordinary/folded leaf and turn ownership | Ordinary/folded leaf plus identity, ordering and error controls | Ordinary + folded complete objects (#9017) | 28 complete objects + 8 no-line boundaries on macOS (#9237 / #9246) | Ordinary post-overlay + folded complete objects (#9577); no-image/malformed/wrong-expectation controls | NAPI export committed, real NAPI not executed | Native everywhere | No domain-specific physical run; acceptance blocked |

`scripts/ci/core.sh` exercises the cargo, C ABI and ArkTS-host columns, including generated-codec and OHOS declaration
checks. `:core:rustcore:test` is the real host-JNI suite; `:core:data` / `:feature:agents` host tests cover their
adapters. SwiftPM's recorded macOS results (#7160, #8972) are reproducible with `native/app-core/scripts/build-apple.sh`
and `xcrun swift test --package-path apple/Packages/PersonaCore -Xswiftc -warnings-as-errors`; this Linux environment
cannot rerun them. Those reports used later Xcode versions, so Xcode 26.0 remains unverified. A C header symbol, a
generated Swift type, a compiled OHOS feature or a host fake is not itself a replay result.

The StreamRepair boundary is deliberately narrower than full Markdown repair: fence recognition/repair, the bounded
table scanner, final unfinished block-marker hold-back, scalar inline repair and math-end classification have cargo
and real host-JNI evidence. Their C ABI and ArkTS-host replays (#9241) cover the named fence/table/marker inputs,
including inherited inline expectations; they do not replay the complete inline or math corpora. The separate
`projectPlainOpenFence` Kotlin/JNI projection (#9515) replays five explicit Apple assertions and 31 source-derived
controls, taking the caller's exact drawn-info-string list as input. The independent Linux C ABI client and real ArkTS
host helper replay that same corpus (#9563) through separate `{text}` projection entry points. There is no SwiftPM,
real-NAPI or production-adapter coverage. Rendering, timing/coalescing and production adapters remain out of scope. #8227 was a duplicate-key snapshot correctness bug and is
closed. It does not close
the separate Android performance follow-up: #9048's lock-free dispatch and owned-JNI-byte hand-off have overlapping
host A/B distributions, and the original emulator / device go/no-go evidence remains unresolved as recorded in the plan.

## Build and test

Needs rustup; `rust-toolchain.toml` pins the compiler.

```sh
cargo test --locked                                # unit tests and the shared fixtures, directly and through the runtime
cargo run --locked -p persona-core-typegen          # regenerate the clients' codecs after changing a wire type
cargo run --locked -p persona-core-typegen -- --check
cargo build --locked --release -p persona-core-ffi # libpersona_core.{a,so,dylib}
bash scripts/test-c-abi.sh                        # independent C11 client + dynamic C exports (Linux)
python3 -I scripts/ohos-napi-defs.py --check        # napi-derive definitions and index.d.ts
python3 -I scripts/test_ohos_napi_defs.py           # declaration parser and drift-check regressions
```

`scripts/ohos-napi-defs.py` runs a host `cargo check` with the `ohos` feature and `NAPI_TYPE_DEF_TMP_FOLDER` set.
`napi-derive-ohos` 1.2.0 writes one JSON record per exported function there; this generator maps those records to
`harmony/entry/src/main/cpp/types/libpersona_core/index.d.ts` in its stable function order. Binary inputs, results and effects use native
`Uint8Array`, and every wake/effect callback must have no leading error argument and return `void`; `changedChat`
keeps separate chat and version arguments. The declaration's `CoreHost` facade and method references stay unchanged.
`--check` compares the complete generated file; without it, the same deterministic output is written for inspection.
No NAPI library is linked or loaded. For testing the converter alone, `--records <JSON-lines file>` replaces Cargo,
and `--print-records` prints the validated raw records. Keep the complete generic `ThreadsafeFunction` types on the
`#[napi]` functions: napi-derive does not expand Rust type aliases into their TypeScript callback signatures.

`scripts/ci/core.sh` runs all of these, plus formatting and clippy. On Linux, `scripts/test-c-abi.sh` compiles
`tests/c_abi_smoke.c` as a separate C11 client against the checked-in header, dynamically links it to the exact release
`libpersona_core.so`, and checks that the header's declared functions have dynamic exports. The client replays the
unchanged fixtures through every pure C projection: sort's `empty_list` and the three iOS-derived held cases (#9033),
`persona_core_project_answer` and
`persona_core_display_answer` for all 15 question + 9 recommendation cases (complete conditional expected objects,
invalid page/option and the U+200B boundaries), `persona_core_project_stream_chunks` for all 21 complete
`{done, tail}` objects, `persona_core_project_stream_repair` (#9241) for 22 fence + 19 marker/boundary + 39
table/boundary complete `{repaired, end}` objects stated independently from the Apple tests/source and existing JNI
replay. The separate `persona_core_project_plain_open_fence` (#9563) replays #9515's five explicit Apple assertions
and 31 source-derived controls through complete `{text}` objects and wrong-expectation controls, including Unicode
whitespace, exact drawn predicates and malformed input.
`persona_core_project_turns` for all 57 static cases with the checked-in current-iOS overlay
applied by the client (73 complete turn objects across the 54 non-empty cases), and
`persona_core_project_task_index` for all five complete expected objects plus the background receipt, and
`persona_core_project_viewed_images` (#9017) for the complete post-overlay ordinary viewed line and a source-derived
folded line, plus `persona_core_transport_frame` for the shared-scalar Envelope gate and Other routing (20 named
refusals). The latter is not the full 17-case wire corpus. `persona_core_project_recents` (#9654) dispatches the
existing fixture-facing operations and compares all 11 conversation + 10 setup complete expected objects directly
from the unchanged JSON. The shown operation receives its existence predicate as an explicit `existing` string list;
there is no storage or clock. Expected objects
are compared completely after a canonical re-serialization (sorted object keys, exact array order and scalars);
exact-name manifests, malformed-input empty buffers and a deliberately wrong expected object are checked per domain.
A missing fixture, client source or release library fails rather than skips. This proves a real C compiler/linker can
consume the C ABI; it is not a Swift importer, Apple ABI, NAPI/HAP, physical-device or performance test. Generated
inputs and the executable stay under the ignored `native/app-core/target/` directory.

Android's `:core:rustcore` tests build a host copy of
the library and replay the same continuity, transport, sort, state and static-turn fixtures through the real JNI
binding. `StateBindingFixtureTest` (#9691) compares the exact two `session_state` and eleven `hub_state` complete
expected objects through `NativeBridge.projectState`. The pure fixture adapter in `persona_core::state` is shared with
the ArkTS host helper; no second state machine, persisted client state or production adapter is introduced. The JNI
entry is debug/test-only and excluded from the Release Android export. The
turn replay compares complete expected `turns` arrays using the checked-in `spec/fixtures/lines/ios/differences.json`.
Its 57 cases include three that compare empty arrays only; they do not verify layout. It does not compare rendered
lines, execute Swift or connect a production Android adapter. The operation replay (#8815) additionally drives
`RustCore` dispatch/drain through JNI, preserves each raw JSON item and calls the turn projection at all 10 checkpoints
in the four existing operation cases. It compares 13 complete turn objects and exact item IDs against the same checked-in
corpus plus overlay, and separately replays the existing replacement through the standalone `replace` branch. These
runtime operations are not incremental ChatStore rebuild, rendered-line/layout, task-index or device-performance evidence.
The stream replay (#8827) calls `projectStreamChunks` through the real Kotlin/JNI boundary for all 21 existing
`spec/fixtures/stream-chunks/split.json` cases, comparing complete `{done, tail}` expected objects from the unchanged
fixture. It checks malformed input and a wrong expected object. It does not execute Swift, visual gaps, rendering,
timing/coalescing, production adapters or physical-device performance. The fence/table/marker StreamRepair slices have
separate real host-JNI coverage; this PR adds their C ABI and ArkTS host replays. Only their SwiftPM projection remains
unimplemented; the generated NAPI export is committed but no NAPI library has been loaded or executed.
The fence StreamRepair replay (#9051) calls `projectStreamRepair` through real Kotlin/JNI for 21 independently
stated repaired strings and six explicit code/text end cases from Apple's tests, including opener/closure indentation,
partial markers and malformed/wrong-expected controls. The final-marker hold-back (#9084) replays all 13 Apple
`markersOnly` cases/controls plus digit boundaries. Table repair (#9164) replays all 22 repair and 15 table/text end
assertions from Apple's table-related tests, including half-written rows, early delimiter rows, list indentation and
lazy-line exclusion. Inline repair (#9286) replays all 51 explicit Apple inline-related assertions and 13 separate
source-derived boundary controls. Its scalar scanner follows emphasis/strikethrough flanking, code spans,
links/images, escapes, bare/autolinks and inline-formula skipping. All 342 Unicode-scalar prefixes of Apple's mixed
reply are replayed through real stream-chunks JNI into stream-repair JNI, preserving each completed-line prefix and
leaving the finished reply unchanged. Math end (#9395) adds Apple's six explicit math assertions and 57 source-derived
controls for open/closed blocks, one-line display formulas, inline formulas in prose, money, code spans, CJK, trailing
indentation, Unicode whitespace and the 400-extended-grapheme plausibility boundary. It follows the distinction between
scalar-based inline repair and `MathMarkdown.display`'s extended-grapheme indexing; it does not implement
`MathMarkdown.rewrite` or rendering. Plain-open-fence projection (#9515) separately replays all five explicit Apple
assertions and 31 source-derived controls through `projectPlainOpenFence`. It preserves the source's distinct Scan and
Foundation whitespace rules and injects the complete drawn-info-string predicate; it does not execute a renderer.
The answer replay (#8838) calls `projectAnswer` and `displayAnswer` through real Kotlin/JNI for all 15 question and
9 recommendation cases. It compares complete existing expected objects with their conditional fields and checks invalid
input, a wrong expectation and the U+200B boundary. #9233 adds a ninth recommendation for newline / NBSP / TAB
trimming and retains the same complete-object comparisons. This does not execute Swift, send a resolution to the hub, measure
UI timing or connect a production Android adapter. The TaskIndex replay (#8842) covers the fixture-facing fields from
`TaskIndex.build`: five existing complete expected objects and a background receipt with empty notices/statuses. It
checks real JNI output, malformed input and a wrong expectation. It does not project rendered ChatLine, full task
consumer state, UI badges or actual background execution. The viewed-image leaf (#8860, #8943) uses the existing turn
projection to keep the image entry's owner, compares complete post-overlay and source-derived viewed lines through
real Kotlin/JNI, and checks duplicate/running/error/Read-extension and wrong-expectation cases. Folded images follow
path/size/`i:` identity and join ordinary tool images in work-group order; no-image folds produce no line. Missing
optional fold fields do not reject queued text+image messages (#9024); actual fold grouping is unchanged. This is not
`full ChatLine, multiple viewed lines, FoldImage.ref synthesis, UI image loading or actual tool execution. SwiftPM
replays the same bounded leaf on macOS (#9237 / #9246): 28 complete post-overlay objects and eight no-line boundaries.
Its full-suite pass after #9252 is not separately claimed by this audit; no real-NAPI, production or device result follows.

```sh
cd android && ./gradlew :core:rustcore:test
```

Android's production packaging lives in `:core:rustcore-android`, leaving the JVM/JNI replay above intact. Its pinned
NDK 29.0.14206865 / direct LLVM build, ABI sizes, uncompressed 16 KB packaging and real device fixture replay are
described in `android/core/rustcore-android/README.md`. `scripts/build-android.sh` produces both ABIs without
`cargo-ndk` (`PERSONA_ANDROID_ABIS` and `PERSONA_ANDROID_CARGO_PROFILE` narrow a debug build; debug packages use the
`android-dev` profile); the Gradle task declares the core inputs so unchanged sources do not recompile Rust.

## Android continuity adapter (#7448)

`:core:data` routes native chat transport through `ContinuityAdapter`. Kotlin wraps the existing reducer and sync
and remains the production default; `-PpersonaContinuity=rust` selects Rust for repository unit tests. Rust owns one
lazy process-wide core, starts it on a worker after a frame, and applies patches and chat-owned fetch effects on
coalesced frame drains. Only the JVM test scheduler waits with `flush`; the app returns after dispatch, not after a
patch acknowledgement. State wakes use their chat identity.

The adapter consumes #7554 without changing Rust, JNI or codecs: `ChatPatch.verified` owns verification,
`Action::HubChanged` owns changes heard during a fill, and adjacent input/`Ack` submissions distinguish applied no-ops
from queued work. Asynchronous fold responses carry `fold_id`, so independent folds open concurrently. Asynchronous
older/fold calls install a single-use input slot before dispatch and wait with `withTimeoutOrNull`; effects from expired inputs cannot satisfy
later calls. The driver tags effects at the worker's ordered Ack wake and delivers completed slots from `patch.acked`.

Asynchronous fill completion belongs to the applied callback chain. Merely queuing `After` or `Reloaded` neither
finishes nor cancels it; until a response is successfully queued, its request owner reports failures or cancellation
with `FillFailed`. Only content inputs newer than an error's input boundary can clear that error; arbitrary or older
Acks cannot. Each reconciliation waits for its own successful fill-response receipt. Synchronous repository paths
retain the original state-lock sections, serialized HTTP operations, downloaded fold results and error-clearing rules.
They do not use receipt timeouts, operation slots or submitted-history generations. Only asynchronous adapters submit
outside that lock and open concurrent folds. Ack-only frames reuse the item list, skip coverage scans and do not
schedule cache writes.
`Rejected` has no chat
or input identity, so the driver records diagnostics and broadcasts a notification without failing another chat or
releasing its gates. Bounded native waits and the core's duplicate-fill gate keep later retries possible.

Enable the debug-only shadow with `-PpersonaContinuity=shadow` in JVM tests, or launch `DebugEntryActivity` with
`--es debug.continuity shadow`, then force-stop and relaunch the app (use `kotlin` to turn it off). Kotlin remains
authoritative: the shadow serially replays every input on its own background queue, waiting only for its own core,
created after the first frame. It compares item ids/order, items, covered, history, hasMore, loadedOnce and requests.
Kotlin's per-step gap/verified and Rust's persistent verification authority are not equivalent fields: cache restores
and live pushes intentionally differ, covered by a separate real-JNI contract test rather than a second state machine.
Rust-only acknowledgements and concurrent-fold capability do not change the Kotlin result returned to the repository.
`PersonaCoreShadow` reports contain only domain, rule and input kind with counts/flags; never items, identifiers, text,
history or sequence values. Driver-level rejection has its own `rust-rejected` rule, including rejection followed by
an exception, and never logs the rejection reason through the shadow. Native failure disables only that chat's shadow.
Neither accept nor close waits for Rust; close preserves queued inputs. After every 10 completed input comparisons,
and after draining on close, each shadow logs only `compared=<count> mismatches=<count>` to `PersonaCoreShadow`.
`compared` excludes queued inputs, failed native work and rejections; `mismatches` counts individual rule reports.
Release source sets contain neither the shadow nor its preferences.

## iOS production DEBUG continuity shadow (#8970)

The iOS `ChatStore` remains the authoritative production implementation behind its continuity adapter. DEBUG mirrors
its immutable inputs into the real C core on a private serial queue, applies each drained patch and compares decoded
items and continuity state. The mirror covers the native cached snapshot, prefetch and reload-retention semantics,
older pages and independently opened folds. Core effects never reach transport. A patch is a delta, not a snapshot;
scheduling-only inputs must not clear its state by draining without applying it.

Diagnostics contain only the domain, rule and input shape (kind, counts and presence flags). They never contain item
JSON, message text, identifiers, history or sequence values. DEBUG uses the mirror by default, with
`-showContinuityShadow NO` to turn it off and `YES` to opt in for performance A/B; Release has neither the mirror nor
the switch. Optimized simulator evidence is labelled as such and is not a physical-device acceptance.

## iOS Agents-list adapter and sort shadow (#9058)

`AgentSortProjectionAdapter` returns the existing Swift `AgentListProjection`; there is no Rust production-selection
path. `-showAgentSortShadow` is opt-in in DEBUG only. The observer waits for the published structural revision, takes
the cache's immutable input and already-computed projection, then submits at most one running and one pending comparison
to a utility queue. Presentation folds, including `TabPanes` tabs, are not part of the production cache key and never
rebuild or reorder its result. JSON conversion, C ABI invocation and typed output comparison happen off the main actor,
not in a view body. Reports contain only the domain, a rule, enum values and counts/flags, including a periodic compared
count; never titles, identifiers, paths, query text or exception messages.

The shared `agents_list` fixture contract remains unchanged. `presentation_rows` is a separate opt-in for the compact By
Project tab rows (#9292), and the adapter passes the production-prepared parent rows, nested-row counting flags and
folded-child counts (#9136). The core applies the production `multiHub` / selected-`hub` setting before counts and quick
filters, retaining hub-qualified paths when two hubs have the same machine/session short ID. One frozen `now` drives both
sides of explicit `since:` and inferred Search as Code windows; timestamps retain fractional seconds.
The adapter preserves the production filtered traversal for `unread_shown`, independently of By Project display order.
Compact presentation rows reuse the computed group order rather than session-ID input order (#9335).
The core also projects iOS's Title, Conversation, User, Assistant and Errors scopes, prose-only reply text, all four error
reasons, dedicated machine/harness/project/model/pin settings and Search as Code heuristics (#9110). The adapter supplies
`goal.objective`, `todo.title`, `work.line` and the prepared flattened CodexTasks descendants as `search_subtasks`; a matching
descendant admits its root without becoming a separate row or count. These 27 independently derived cases extend the shared
corpus to 60 complete `agents_list` projections without changing any older expected object. Unsupported grouping and source
quick filters are still explicitly reported, not silently treated as parity.
With `inventory`, the core returns `filtered` and `unpinned` root ids independently of renderable groups. A nested row
that admits the query retains its prepared parent through `nested_match`; an unregistered-machine session stays in the
inventory without inventing a machine section. The DEBUG comparison consumes these arrays directly, and legacy fixture
outputs omit them unless requested (#9221). This is a DEBUG shadow, not approval to replace Swift with the core.

## Android Agents-list adapter and sort shadow (#8954)

`:feature:agents` supplies its existing scoped list projection to `AgentListAdapter`. Kotlin remains authoritative; the
default factory and the entire Release source set return only `KotlinAgentListAdapter`, with no observer or shadow class.
The optional DEBUG observer uses `projectSort` on its own FIFO worker after the Kotlin result has returned. Enable it in
JVM tests with `-Ppersona.sort=shadow`, or launch `DebugEntryActivity` with `--es debug.sortShadow on`, then force-stop
and relaunch (`off` disables it). Reports use the `PersonaCoreShadow` tag and hold only `agents-list-sort`, a rule code
and counts/flags/enum values: never titles, paths, queries, identifiers or exception messages.

The core's `state`, `machine` and `space` projections compare complete published outputs. `Repo` and `MachineSpace`
use the core's `space` projection for their inner list and separately report the unsupported outer repository/computer
headers; to-do, direction and history extensions are not silently called parity. Input conversion preserves multi-hub
identity and includes the core's `held` field from the shell's visible Agent chat and its entry-time Unread capture
while its held row remains open. The production Kotlin path still owns range preprocessing, `retainOpenedRows`, history
and children. A caller-supplied
instant makes `since:` searches deterministic. The 33 legacy checked-in `agents_list` fixtures pass through both production
Kotlin and the real host JNI projection, including three State, Space and quiet-fold cases for the production
`heldInList` behavior aligned with iOS (#9033). All 60 cases, including the scoped-search contract, pass the direct host-JNI
complete-expectation replay. The extended iOS settings are not a claim of Android UI or native-adapter adoption.
The separate multi-hub pin identity issue is #9027.

Release has no sort-shadow implementation and its native library does not export `Java_..._projectSort`. The app still
packages the other core entry points for the existing packaging work: this is a strict boundary for this shadow, not a
claim that the whole native library is absent. `buildAndroidReleaseCore` builds its own Cargo target and checks both
ABIs with `scripts/check-android-sort-shadow.py`; Release JVM tests additionally prove that no shadow class is on the
classpath and the factory remains Kotlin even when the debug property is set. Conversion, redaction, native failure,
production entry and old-vs-new Kotlin behavior have focused JVM tests. This is host and packaged-artifact evidence,
not a physical-device shadow run, Rust promotion or iOS behavior change. The production held-row alignment changes
which existing row and section the Android list displays; its host Compose captures are not device evidence.
## HarmonyOS Agents-list core (#8957, #8966)

`CoreAgentListAdapter` calls the synchronous pure `coreProjectSort` NAPI function directly. It is the only production
Agents-list sorter: the temporary ArkTS projection, debug shadow, snapshot worker and comparison reports are deleted.
`AgentWorkspace` maps the core's complete groups, `visible_rows`, folds and counts back to the original `ResourceRow`
identities used by native actions. The default is iOS By Status; By Machine and By Project are the other choices.
State/space folds, idle expansion, quick filters including BackgroundWaiting, pin/mute ranks, search operators and
multi-Hub machine identities all use the same settings as the core. When manager is supplied, machine paths are
`group|manager|id` (or `manager|id` for the default Hub); legacy input without it retains `group|id`. This keeps
same-Hub, same-ID managers independent (#9068). Per-Hub pin/mute preference scoping is the separate #9027 contract.
Native failure retains the last usable projection
and exposes the existing failure state instead of presenting an empty successful list. There is no second release
sorter or debug-only native fallback; an older build is the rollback boundary while no HAP/device evidence is available.

`harmony/tests/core-host/agent-sort-adapter.test.mjs` drives the production adapter's real native-import contract and
the real core through all 60 checked-in `agents_list` fixtures, including the three held cases (#9033) and the 27
scoped-search cases (#9110). The complete expected objects match **60/60**. Host workspace tests also exercise frozen-reading identity, explicit folds, filters
and offline-history membership. The portable suite provides a synchronous fake for the same NAPI boundary, not
evidence of an actual HAP NAPI callback.
Physical-device and HAP/NAPI execution remain **blocked: no physical device**; this host environment has no
`devecocli`, so a release HAP was not built.
## Android answer adapter and shadow (#9143)

`AnswerBuilder.build/display` keeps its existing signatures and delegates to a Kotlin-default `AnswerAdapter`, so every
existing Agent request and chat panel keeps the same returned Kotlin objects. The separate hub-provided task
recommendation text (`TodoAsk.recommend`) is not part of this question-option projection. Release compiles only the
Kotlin path; `RustCoreAnswer`, the observer and its preferences have no Release source-set or JNI entry.

The DEBUG launcher accepts `--es debug.answerShadow on` (then force-stop and relaunch; use `off` to disable it for the
next process). JVM `:core:domain:shadowTest` always compiles the observer separately from the production jar and is
also a dependency of the standard root `test`. Kotlin returns before the observer's own bounded FIFO worker converts
the question-only operation, loads the native library or calls JNI. At most 32 comparisons wait in that queue, repeated
labels are compared once per adapter after a successful comparison, and a full queue drops only new diagnostic work.
The DEBUG answer JNI itself uses a bounded Rust channel (one executing job plus at most one waiting job), with a
one-second result wait on the client's background worker. The main thread never calls it; a native thread that itself
hangs cannot be forcibly cancelled, but it cannot make the queue unbounded.
Permission, plan, screen, elicitation and quick-entry operations outside the pure core's contract are reported as
unsupported, not as parity. Exact answer/display comparisons stay in that worker, including picks and custom-answer
state for all three supported operations; a report contains only domain, rule and counts/flags/operation enums, never a
label, question, answer, identifier or exception message. Every 10 completed comparisons logs only
`answer compared=<count> mismatches=<count>` to `PersonaCoreShadow`.

The replay compares all 15 `questions.json` and 9 `recommended.json` complete expected objects through both the
current Kotlin builder and real host JNI, with the exact fixture-name manifest. Known semantic differences are asserted
as differences, not hidden by editing fixtures or the current production result. This is host/JVM and DEBUG observer
evidence, not hub delivery, UI timing or a physical-device run; Release's two answer JNI exports are checked absent on
both packaged ABIs. The U+200B display boundary was independently fixed by #9189 and is included in the ninth case.

The retained `other` draft that the stricter shadow reports as `other-state` (#9238) was decided with the iOS oracle, not
from either implementation. iOS's `QuestionForm.pick` leaves an emptied entry (`other[q.question] = ""`: the key stays),
and Advance, Submit and `send` change neither map. The core does exactly that, so it is unchanged; Android's
`AnswerBuilder`, which removes the key, is the side that differs. Every answer, page and resolution agrees, and iOS reads a
missing entry and an empty one alike, but Android's form draws its Other field while the key exists. An adapter that
stores the core's draft must therefore drop an emptied entry first, or the form must test the text instead of the key.
Until then the shadow keeps reporting `other-state` for each accepted single choice: that is this known difference, not
noise to filter out. `answer_projection.rs` asserts the rule for all 24 cases and for the issue's two-question chain, and
`TestResolutionFixturesIOS` (macOS, `PERSONA_VERIFY_IOS_FIXTURES=1`) asserts it against iOS's own code for every question
case and for that chain. The shared `expected` objects carry answers, not the draft, so no fixture or replay manifest
changed.

## Reproducing phase-1 evidence

These are experiment entry points, not claims that every cell has run. A host or simulator result never substitutes
for a physical-device result. Raw library bytes, stripped bytes, an app's packaged increment and store-download size
are separate measurements; gzip is not a store-download report. `docs/plans/plan-rust-core.md` keeps the measured
values and the open cells for #6871.

| Evidence | Command / entry point | What still has to be supplied or measured |
| --- | --- | --- |
| Core tests, codec drift, HarmonyOS binding lints and notices, release FFI and independent C ABI client | `scripts/ci/core.sh` | Rustup, a C compiler and binutils on Linux; no phone or platform SDK. The lane adds the `aarch64-unknown-linux-ohos` Rust target and lints `persona-core-ohos` with its `ohos` feature, which the host build never compiles. The separate C11 client links to the real release library, but this is not evidence of Swift/Apple ABI or a device library. |
| Real host-JNI boundary | `cd android && ./gradlew :core:rustcore:test` | JDK 21; host JVM tests do not need the Android NDK or a device. |
| Android ABIs and packaged bytes | Build the APK, then run `native/app-core/scripts/android-package-report.py` with the exact arguments in `android/core/rustcore-android/README.md`. | x86_64 Linux, JDK 21, SDK 37 and NDK 29.0.14206865. Reports raw/stripped/gzip/APK bytes and ELF/APK alignment, not store downloads. |
| Android real packaged-JNI replay | `native/app-core/scripts/android-device-test.py --serial <owned serial> --suite replay --apk </absolute/path/to/debug androidTest.apk> --output <report directory>` | An explicitly owned emulator or device; no hub is needed. The existing host and software-emulator passes do not establish physical arm64 or a 16 KiB kernel. |
| Android optimized benchmark | The same runner with `--suite benchmark` and the release androidTest APK | Physical arm64 / 16 KiB-kernel evidence is **blocked: no physical device** (maintainer decision, 2026-11-14); do not wait for it. If an arm64 Android 15+ device with a 16 KiB kernel later becomes available, rerun this existing command and retain its named device/build and raw samples. Keep the original 100 warm-ups, 1,000 samples and 50 µs / 1 ms budgets; an emulator failure stays a failure. |
| Apple XCFramework and real-C XCTest | `native/app-core/scripts/build-apple.sh`, then `xcrun swift test --package-path apple/Packages/PersonaCore -Xswiftc -warnings-as-errors` | macOS and Xcode; no physical phone is needed. On Linux with a Linux Apple toolchain the script builds the device and arm64 simulator slices without the macOS one (#16944), so the XCTest run still needs a Mac. `DEVELOPER_DIR=<Xcode 26.0.1>/Contents/Developer scripts/ci/apple.sh packages` checks the minimum compiler when that Xcode is installed. Other Xcode versions must be labelled "26.0 not verified". |
| Apple launch, stream, scroll and ten-open/close memory | `scripts/app/perf-test.sh` covers only its listed frame/hitch suites. A launch and ten-open/close memory entry is not yet implemented; do not infer those results from it. | Physical-iPhone evidence is **blocked: no physical device** (maintainer decision, 2026-11-14); do not wait for it. An owned simulator and named optimized build may still supply reference data. App Launch / Allocations / Animation Hitches are the procedures for a later device; an XCFramework or archive is not a download-size measurement. |
| HarmonyOS API 26 library | `OHOS_SDK_NATIVE=<API 26 native SDK> native/app-core/scripts/build-ohos.sh` | An installed API 26 native SDK, the Rust OHOS target and populated offline Cargo cache are still required. The script derives `OHOS_NDK_HOME` (which `napi-build-ohos` reads) from the directory that holds the SDK's `native` directory. With a stub SDK layout the build compiles the release closure and stops at the link step; an actual link has not been run. Host registry tests do not link the device NAPI. |
| ArkTS codec and wire corpus against the real core, on the host | `scripts/ci/core.sh` builds the helper and runs `harmony/tests/core-host/replay.test.mjs` and `transport.test.mjs`. Locally: `cd native/app-core && cargo build --locked -p persona-core-ohos --example host_replay`, then `PERSONA_CORE_HOST_REPLAY=$PWD/target/debug/examples/host_replay node --test '../harmony/tests/core-host/*.test.mjs'` (Node.js 22.15+). | ArkTS-encoded actions drive the real runtime through `persona-core-ohos`'s registry, and ArkTS decodes the effects and patches: 38 cases / 94 steps, plus 42 values built independently in Rust whose bytes must match in both directions. It caught `u32` fields being written and read as 8 bytes (#7485). The wire corpus is also replayed through the transport decoder with byte-identical items, and representative `WirePayload` actions are compared with hand-built `WireBatch` actions. Host evidence only: no NAPI library, HAP or device; callbacks arrive as records, not thread-safe functions. |
| HarmonyOS real-NAPI replay | `devecocli build --modules entry@ohosTest`, then install and run `aa test` with `coreReplay 1`; exact arguments are in `harmony/README.md`. | **Blocked: no physical device** (maintainer decision, 2026-11-14); do not wait for it. A future API 26/HDC environment must also have DevEco CLI and the native SDK. HAP linking, real NAPI callbacks, device timing and packaged increment remain unmeasured. |

Physical-device cells are no longer prerequisites for the next milestones. They stay explicitly blocked above rather than
being marked passed or supplied by a simulator. Xcode 26.0.1 minimum-compiler verification remains a separate Mac
toolchain task, not a physical-device requirement. Device selection, signing, SDK installation and license acceptance
belong to the maintainer's environment if it later becomes available; none is silently substituted by an emulator or
a release upload.

The ArkTS host tests live outside the portable `harmony/tests/*.test.mjs` glob, because the replay needs the helper
binary; a missing helper or too-old Node fails the test rather than skipping it. `harmony/tests/core-host/sort.test.mjs`
replays every Agents-list sort projection and all 13 existing `session_state` / `hub_state` cases against the real core.
The state command shares `persona_core::state::project_fixture` with the real Kotlin/JNI consumer (#9691), which calls
`session_state` and `AgentState::from_hub`; expectations come only
from the unchanged checked-in fixtures, with exact-name, invalid-input and wrong-expectation checks.
`harmony/tests/core-host/projections.test.mjs` (#8937) replays the current 15 answer + 9 display_answer,
21 stream_chunks and 5 task_index cases plus the background receipt through a host JSON projection command; #9233 added
the ninth recommendation. The answer comparison preserves the fixture's
conditional next-page, picks and display fields. The receipt retains its description and started call with empty
notices/statuses. Exact manifests, malformed inputs and deliberately wrong expected objects are checked per domain.
`harmony/tests/core-host/stream-repair.test.mjs` (#9241) replays the same 80 complete `{repaired, end}` objects
through the `stream_repair` host projection command, including the digit, leading-blank-line and delimiter boundaries.
Both surfaces reject malformed inputs and deliberately wrong `end`, wrong `repaired` and extra-field expectations.
The separate `plain_open_fence` host projection (#9563) replays #9515's five explicit Apple assertions and 31
source-derived controls through complete `{text}` objects and wrong-expectation checks, including malformed input and
non-drawn preservation.
The `ohos`-feature NAPI projection exports are `coreProjectSort`, `coreProjectAnswer`, `coreDisplayAnswer`,
`coreProjectStreamChunks`, `coreProjectStreamRepair` (#9241), `coreProjectPlainOpenFence` (#9563),
`coreProjectViewedImages` (#9577), `coreProjectRecents` (#9584), `coreProjectTaskIndex` and `coreProjectTurns`; their
generated declarations are checked for drift. `coreDisplayAnswer` takes a bare option label, while the other nine take
JSON documents.
`harmony/tests/core-host/recents.test.mjs` (#9584) replays all 11 conversation and 10 setup fixture cases through the
real `recents` host dispatcher, comparing complete expected objects and exact manifests. The fixture supplies the shown
operation's existing-key list in place of a persistence closure; no storage, clock or production adapter is introduced.
`harmony/tests/core-host/viewed-images.test.mjs` (#9577) compares the ordinary complete post-overlay object and the
source-derived folded object through the real `viewed_images` host projection, including identity, order, malformed,
no-image and deliberately wrong expectations. It does not reconstruct `FoldImage.ref`, load images or project full
`ChatLine`s.
`harmony/tests/core-host/turns.test.mjs` (#9235) adds `coreProjectTurns` and its host JSON projection. It compares
all 57 unchanged static cases after applying the checked-in current-iOS overlay: 54 nonempty cases hold 73 complete
turn objects, and three explicitly named cases compare empty arrays rather than establishing layout coverage. The
same generated ArkTS codec and existing registry dispatch/flush/drain commands replay four operation cases at all 10
checkpoints, with exact item IDs and 13 complete turn objects; an existing burst is also replayed through the
standalone `replace` branch. Exact manifests, malformed/wrong-shaped inputs and deliberately wrong static and
checkpoint expectations are checked. The helper supplies a fold range only after the real runtime requests it. No
turn fixture, overlay or turn-grouping semantics changes.
`harmony/tests/rustcore-wire.test.mjs` round-trips the generated codec against itself, which cannot catch a symmetric
mistake, so it is not a substitute. These host projections do not execute the NAPI functions or exercise a state codec,
a loaded NAPI module, NAPI argument conversion or callbacks, a HAP/HDC run or a physical device. In particular,
turn helper evidence does not replace #8808's real Linux NAPI addon loader. The production HarmonyOS `coreProjectSort`
consumer is a separate merged adapter (#9066), exercised on the host by `agent-sort-adapter.test.mjs`; the other
projections have no production consumer.

## Host-only boundary diagnostics

On an x86_64 Linux host, `cd android && ./gradlew :core:rustcore:boundaryCostTest` builds a separate optimized
`test-support` FFI library and runs the segmented Runtime/JNI/codec measurements. Ordinary `:core:rustcore:test`
excludes these measurement classes and keeps its debug host-JNI replay. The dedicated task writes raw nanoseconds and
metadata to `core/rustcore/build/reports/boundary-cost/boundary-cost.json` and
`core/rustcore/build/reports/codec-comparison/codec-comparison.json` (paths relative to `android/`). A missing native
library or measurement nonce fails the task rather than silently skipping evidence.

Each run uses 100 warm-ups and 1,000 retained samples per segment, with the same bounded item/action and a real
nonempty patch per frame. The frame is exactly JNI drain + Kotlin decode + list apply on the same patch bytes;
OpenChat and flush are excluded. `PERSONA_MEASUREMENT_DIAGNOSTICS=0` disables runtime diagnostics entirely; do not
interpret the absent native counters in that mode as zero cost. With diagnostics enabled, the `/proc` reads and lock
observation can dominate the measured spans. The registry lease, queue send, patch clone/encode and Kotlin decode
are reported separately and overlap rather than forming an additive wall-clock decomposition.

The codec comparison is an allocation microbenchmark with strict UTF-8 and matching validation on its fixed input,
not a general codec-equivalence or frame-performance proof. Host and emulator values are diagnostic references only:
they do not replace the original Android benchmark, the 50 µs / 1 ms budgets, or physical-device evidence.

## Rules

- **A wire type change ships with its regenerated codecs.** Never edit generated files by hand.
- **Fixtures come first.** A rule changes in `spec/fixtures/<domain>`, then here; expectations are never generated from
  this code.
- **Text is out of scope.** No user-facing text: the core returns codes and enums, and each client words them from its
  own localization resources.
- **The main thread stays cheap.** Calls a client makes on its main thread (`dispatch`, `drain`) never do work that grows
  with a chat; that work belongs to the worker.

## Transport decoding

`persona_core::transport` turns the bytes the host received into the values `continuity` takes (the plan's decision 2:
JSON is decoded once, in Rust). `Action::WirePayload` carries the bytes (below); no production client sends them yet.

| Function | Payload | iOS type it follows |
| --- | --- | --- |
| `decode_latest` | the latest page or an older page of `GET …/items` | `APIClient.ItemsResponse` |
| `decode_newer` | a catch-up page (`after_seq`) | `APIClient.NewerResponse` |
| `decode_range` | a page of an opened fold | `Range` in `APIClient.foldedWork` |
| `decode_frame` | a WebSocket frame: `Frame::Items`, or `Frame::Other` for any other type | `ServerEvent.decode` and its `Envelope` |

- Each item's JSON is kept byte for byte; only the keys the core's rules use are read.
- A refusal is a `DecodeError`: a reason code (`invalid_utf8`, `truncated_json`, `malformed_json`, `not_object`,
  `missing_key`, `wrong_type`, `bad_item`) and a detail made of positions and protocol key names. It never repeats
  payload content. One refused item refuses its page, named by the index of the first one.
- A key that appears more than once keeps its first value, as Foundation's keyed container does.
- `cargo test` replays the 17 wire cases these decoders own (`spec/fixtures/wire`: `items.json`, `item_events.json`
  and the `items` events of `compatibility.json`), all of them accepted payloads, and rebuilds every payload step of
  the continuity corpus from bytes, comparing the step and the state with the hand-built batch. Of the 79 expected
  fields of those cases, 52 are values the decoder produced; 25 are item fields the core does not read, compared with
  the item's kept JSON, and 2 are the frame's type. The same 17 cases are replayed through the real JNI binding as
  `Action.WirePayload` by Android's `TransportBindingFixtureTest` (`./gradlew :core:rustcore:test`, a host library),
  which feeds each case's `input` exactly as the file holds it and compares every published item with its raw source
  text; that is host evidence only, not a device or iOS run.
- The refusal rules are tested by the crate's unit tests: every key a decoder reads, with every kind of wrong value.

### Source-derived WebSocket Envelope projection (#8955)

`persona_core::wire_envelope` is a pure diagnostic projection of the narrow current-iOS slice in
`spec/fixtures/wire-envelope`: the shared scalar and raw-JSON envelope keys, `mesh` / `fleet` tolerance, unknown
types, and routing requirements for `hello`, `inbox`, `items`, `fleet`, `screen` and `sessions_list`. It returns
accepted, ignored or dropped with the documented projected fields; known event types outside that set return
`out_of_scope`, never an assumed acceptance. It does not route an event, publish a patch or replace `decode_frame`.
The existing continuity transport behavior and its known differences are unchanged.

All 64 complete expected objects were hand-derived from the Swift sources cited per case, including duplicate keys
whose input reaches the decoder as raw source text. `cargo test` and `WireEnvelopeBindingFixtureTest` replay exactly
the same file and expectations; the latter calls the real host Kotlin/JNI `projectWireEnvelope` function, and checks
malformed input and a wrong expectation. Integer spellings, encodings, unprojected nested models, raw `JSONValue`
extreme floating-point behavior and full Item / Fleet models remain explicitly limited in the domain README.
Neither replay executed Swift, iOS, a simulator, a production adapter or a physical device.

It does not match iOS everywhere. The module's documentation has the full list; in short:

- **Shared scalar / raw-JSON Envelope keys are checked before routing (#8995).** iOS decodes its shared `Envelope`
  before choosing an event. `decode_frame` now rejects a wrong-typed known scalar, list shape or raw `JSONValue`
  number even when another event owns the key (`rows`, `features`, `state`, `backlog`, `args`, …). An unknown event
  type remains `Frame::Other`; it is not treated as a continuity event. The #8955 source-derived fixture replays this
  production gate, and the real host Kotlin/JNI, ArkTS and independently compiled C ABI paths check its selected
  refusals. The C path does not replay the full wire corpus.
  The production Item JSON and exact `bad_item` locations are unchanged; `dispatch` still only queues bytes.
- **Nested structs the core does not read.** iOS refuses a payload when an item's `image`, `task`, `compact`,
  `injected` or `model_refusal`, or a fold's `images` or `tasks`, fails to decode. The core accepts it.
- **Number spellings the hub never writes.** The core takes integer literals only and refuses `8.0` or `8e0`, which
  Foundation reads as 8. Reading an integer through a double exactly needs `serde_json`'s `float_roundtrip`
  reader, which added 33,328 B to the stripped x86_64 host library (741,032 B to 774,360 B) when measured.
- **Encoding and kinds.** The core takes only UTF-8 without a byte order mark, and keeps an unknown item `kind` as
  the hub wrote it.

The same decoders are reached by `Action::WirePayload { chat, payload }` (`persona-core-runtime`'s `wire`): a
`Payload` is one response body or WebSocket frame as received, and its variant says which request it answers
(`Latest`, `Newer`, `Reloaded`, `Older`, `Range`, `Frame`). Decoding happens on the worker, never in `dispatch`; a
`Frame` of another type is ignored and one iOS would drop is dropped without an error. A refused page is an
`Effect::Rejected` whose reason is a code and protocol key names, never payload content.
`crates/persona-core-runtime/tests/transport_through_runtime.rs` replays the 17 wire cases through it with the JSON
exactly as the files hold it: 32 items checked byte for byte, every case except the opened-fold range page also
compared with the equivalent hand-built `ChatEvent`, plus separate tests that drive a range page into an opened fold
and an older page into history, and refusal, other-frame and dispatch-cost tests. The same corpus is replayed through the real JNI
binding (`TransportBindingFixtureTest` in `:core:rustcore`) and through ArkTS and the transport decoder on the host
(`harmony/tests/core-host/transport.test.mjs`); both are host evidence, not device runs.

  - A no-op batch without a span merges rather than clearing existing items; pending patches commit their complete
    state and ops together on drain (#8970, with a new 39th case / 95th step in the shared corpus).
  - The patch also carries `verified` (the chat's items are known complete, the rule Android used to derive per
client), and `acked` (a host's acknowledgement watermark, so an applied no-op is visible). `Action::HubChanged`
settles as if a gap were found, iOS's `settle(.fill)`. Folds are keyed by id and independent: different folds open
and complete in either order, with `fold_id` on the range request, event and payload; a caller that sends no id keeps
the older single-opening behaviour. `RangeFailed { fold_id: None }` cancels **every** opening, so a transport failure
for one of several keyed folds must report its id or the other folds are cancelled too.

Nothing here was executed on iOS. The rules were read from the Swift sources and from swift-foundation's
`JSONDecoder`; the wire cases' expectations are derived from the iOS sources as well (`derived: true`) and are checked
by the hub's golden test and the Android replay, and no iOS test reads those cases yet.
