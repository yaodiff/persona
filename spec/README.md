# 共享行为规格

这里保存 Android 纯 Kotlin 核心和以后 iOS 核心共用的行为规格与夹具，依据是 [重设计总纲 D1、D4](../docs/archive/redesign/README.md)。Android 用 JUnit 读取；以后 iOS 跟进时，XCTest 读取同一批 JSON。

**行为以当前 iOS 实现及其单测为准。** 文档与实现有差异时，保留现有行为，在领域 README 中说明差异及其来源。更新行为应先更新规格和夹具，再调整各端实现。

## 目录

```text
spec/
  README.md
  fixtures/
    continuity/       对话同步、补洞、缓存和折叠工作
    lines/            对话行
    sort/             排序和分组
    answer/           回复与交互
    question-review/  multi-question review step (Swift answer oracle only)
    terminal/         终端
    wire/             协议解码
    wire-envelope/    WebSocket 共享 Envelope 的宽容解码，iOS 源码推导并由 Rust / Kotlin host JNI 回放
    injected/         hub 注入给模型的消息怎么认
    comments/         comment blocks a message leads with (web and iOS)
    sync/             chat sync scenarios: hub state, frames with faults, what each device shows
    sync-driver/      one hub group's reconnect, event cursor and lifecycle decisions
    fold-changes/     the files a run of work changed, counted by the hub and the web alike
    fold-sources/     the web pages a run of work used, read by the hub and the web alike
    dashboards/       Dashboards watch pages (#13020): how the manager draws a watch, when its alert rule trips and pushes
    graph-layout/     an extension graph's layout, reading order and folds, checked by the web, iOS, Android and the hub
    input-clip/       the whole-input bound after the hub and runtime SDK's two string passes
    attention/        Persona Link's "Needs you" summary against the web badge
    counts/           every place a count is drawn, the count it draws, and how the clients' replays agree
    entries/          the Assistant's two composer entries, Needs you and In progress
    needs-you/        the Agents tab's Needs you: one row per waiting session, what its header, filter and badge count
    triage/           the To-dos page's sections (status SOP), checked by the web app and the hub
    todo-view/        a to-do's view (phase, section, reason, line, asks) the manager works out, schema/wire/todo-view
    todo-counts/      the To-dos list's counts and places, schema/wire/todo-view
    todo-rounds/      a to-do's conversation in rounds, its results and what changed since the owner looked, schema/wire/todo-view
    todo-asks/        an answer to a to-do's own ask: what it takes, refuses and changes, schema/wire/todo-view
    todo-writes/      a to-do's message or answer that failed: what it typed, and what the hub's receipt makes of it
    todo-detail/      a to-do's page: which elements it draws, in order, and its one primary action
    todo-layout/      the To-dos window rule: a window and what is wanted → its regions and their widths
    todo-push/        a decision's push: what the manager sends → its category, mutable-content and todo, schema/wire/todo-view
    provider-import-link/ provider import links: what the web builds and what the hub reads
    deep-links/       links to Persona's pages: the hub's paths and what each client opens for a link
    agent-names/      what every client calls each coding agent (names.json)
    permission-modes/ what every client calls each permission mode, in en, zh-Hans and ja (names.json)
    star-prompt/      when the GitHub star prompts show (empty Agents pill, milestone card, onboarding toast)
    stream-chunks/     the completed-block split of a streaming reply, checked against iOS by the pure Rust core
    meeting-echo/     Persona's echo in a meeting: a line is one only when its time and its words match, checked by the hub and iOS
    meeting-transcription-problems/ why the hub isn't transcribing a meeting: the extension's code, the phone's problem and what it says, checked by the hub and iOS
    minutes-overview/ the minutes' summary card: bold places, cleaning, the fallback and its Markdown, checked by the hub, the mock, iOS and web
    minutes-debates/  real disagreements in minutes: cleaning, unknown status, empty fallback and Markdown in both layouts across en/zh-Hans/ja, checked by the hub, the mock, iOS and web
    minutes-wait/     what the minutes line says while the minutes wait for the final pass, with its progress, checked by the hub, iOS and web
    speakers-align/   speaker timeline numbering, overlap pairing, trimming, two-layer alignment, eligibility, stats and quality checks, checked by the hub (web and mock later)
    speakers-names/   speaker display-name four-row table (including also) and roster edit rules (naming, auto-merge, undo, no chains, clear), checked by the hub (web and mock later)
    tool-names/       tool names, subjects and arguments in words, checked by Android (iOS later)
    chat-search/      what search in chat and the assistant's chat index find of each item, checked by web (Go and Android later)
    screen-link/      the live screen's session wire, checked by the node (Go) and the iPhone (PersonaScreenKit)
    file-types/       a .ts file's type from its bytes, TypeScript or an MPEG transport stream, checked by the node and the hub (Go)
    screen-udp/       the live screen's video over UDP: sealing, packets, Reed-Solomon blocks, checked by the node (Go) and the iPhone
    screen-input/     the live screen's pixel scroll record, checked by the node (Go) and PersonaInputKit (helper and iPhone)
    review/           what the node's git.review answers (empty fields left out), written by the node and decoded by iOS
    look-colors/      automatic theme color seed extraction from avatar pixels, checked by the hub (Go), web (TypeScript) and Apple (Swift)
    tokens/           设计 token 的校验夹具（如该领域提供）
  tokens/             设计 token 的来源文件，见 D7
```

各目录由对应车道维护，表中链接可能在相应车道合入后才存在。

| 领域 | 规则与用例索引 |
| --- | --- |
| continuity | [连续性 README](fixtures/continuity/README.md)：连续位置、丢批补洞、历史变化、分页三态、重连、fold 替换与展开. Also replayed by the shared Rust core (`native/app-core`, `cargo test`) and through its Android binding (`:core:rustcore`), #6871 |
| lines | 见各自 [README](fixtures/lines/README.md) |
| sort | See the domain [README](fixtures/sort/README.md). The reference implementation is `persona_core::sort` in `native/app-core`, checked by these iOS-derived fixtures. HarmonyOS production uses the core projection through `coreProjectSort` (#7579, #8966). |
| answer | 见各自 [README](fixtures/answer/README.md) |
| question-review | [README](fixtures/question-review/README.md): the review step of a question card of several questions, replayed by the Swift answer oracle only (#8483) |
| terminal | 见各自 [README](fixtures/terminal/README.md) |
| wire | 见各自 [README](fixtures/wire/README.md). The `items`, `items_after`, `items_range` cases and the `items` events are also replayed by the shared Rust core's `transport` decoders (`native/app-core`, `cargo test`), #7385 |
| wire-envelope | [README](fixtures/wire-envelope/README.md): a narrow, source-derived current-iOS WebSocket Envelope slice (#8955), replayed by Rust and real host Kotlin/JNI; no iOS execution or production transport change |
| injected | 见各自 [README](fixtures/injected/README.md)：hub 注入给模型的消息（任务通知、定时任务……）怎么认 |
| review | [README](fixtures/review/README.md): the node's real `git.review` answers, empty `header` and base `hash` left out, written by the node's test and decoded by iOS (#8733) |
| comments | [README](fixtures/comments/README.md): the `> Comment N` / `Code comment N` / `Element comment N` blocks a message leads with, written and read back by the web app and the iOS app (#151) |
| attention | [README](fixtures/attention/README.md): Persona Link's "Needs you" summary counts what the web app's badge counts, checked by Go and the web app |
| counts | [README](fixtures/counts/README.md): the surface table, every place a count is drawn on each client, the count it draws and whether drawing the manager's view would show today's number, held to the code by `scripts/test/test_counts_surfaces.py`; and the agreement report, which compares what the iOS and Android needs-you replays count (`scripts/test/counts_agreement.py`, #11900) |
| session-search | [README](fixtures/session-search/README.md): ranked runtime-Agent search, explicit query operators and shared local/server matching (#7624) |
| chat-export | [README](fixtures/chat-export/README.md): a conversation exported as Markdown (#7375): who wrote each part, merged replies, work steps on and off, cut text and fences, written by the web app |
| entries | [README](fixtures/entries/README.md): which of the Assistant's two composer entries (Needs you, In progress) each task, Agent, plan and message card goes to, checked by the web app and Android (#3387) |
| needs-you | [README](fixtures/needs-you/README.md): the Agents tab's Needs you section, one row per session that waits for the user, and what its header, filter and badge count, checked by iOS and Android (#3580) |
| triage | [README](fixtures/triage/README.md): the To-dos page's sections (the status SOP), related rows and counts, checked by the web app and the hub (#3483) |
| todo-view | [README](fixtures/todo-view/README.md): the view the manager works out for each to-do (phase, section, reason, line 2, times, marks, asks, steps) by the redesign's decision table, held to `schema/wire/todo-view.schema.json`, answered by the manager's projection (`server/internal/assistant/todo/projection`, `fixtures_test.go`) and read back by the generated Go, Kotlin and Swift types (#10226) |
| todo-counts | [README](fixtures/todo-counts/README.md): a To-dos list's counts (N, open, in progress, rows per section), its places (All, Today, Upcoming, projects) and the order within each section, with the same schema and readers (#10226); its counting rules are replayed by the iOS app's `TodoCountsFallback` (#10465) |
| todo-asks | [README](fixtures/todo-asks/README.md): an answer to one of a to-do's own asks through `POST /api/v1/todos/{id}/asks/{ask}/answer` (what waits, the ask's id and the answer → the status, code, reply and what it changed), held to `schema/wire/todo-view.schema.json`, replayed by the manager against its route (`server/internal/assistant/todo_asks_routes_test.go`) and read back by the generated Go types (#10465) |
| todo-writes | [README](fixtures/todo-writes/README.md): how a to-do's message or answer that carries the owner's words ends (what happens to the words → the manager's status, code and `delivered` → the hub's final `todo_send_state` and receipt), replayed by the manager against its routes (`server/internal/assistant/todo_write_outcome_test.go`) and by the hub's relay (`server/internal/hub/todo_send_state_test.go`), the seam between two packages that never import each other (#10465) |
| todo-detail | [README](fixtures/todo-detail/README.md): a to-do's page (the paper) from what the item route answers and where it is shown: its elements in order, its one primary action and whom its composer addresses, with a guard that everyday pages name no engine, runtime, model, path, repository or session id; checked by `scripts/test/test_todo_fixtures.py`, read by no client yet: iOS will replay it from its paper (#10465), the web and Android from their stages |
| todo-layout | [README](fixtures/todo-layout/README.md): the To-dos window rule, a window, its text size and what the owner asked for → the sidebar, list, paper and conversation regions with their forms and widths, every window of the redesign's table included; checked by `scripts/test/test_todo_fixtures.py`, read by no client yet: each will replay it with its own pure function, iOS first (#10465) |
| todo-push | [README](fixtures/todo-push/README.md): a decision's push, the `todo` a manager sends → the push's category, whether it sets `mutable-content`, and its `todo` (an older manager's unchanged, the lock-screen rule, the options forwarded), answered by the hub (`TestDecisionPushFixtures`), held to the schema by `scripts/test/test_wire_schema.py` and read back by the generated Go, Swift and Kotlin types (#10465) |
| sync | [README](fixtures/sync/README.md): stories of one chat on one or more devices (sends, cards, reconnects, hub restarts, wakes, long chats) with faults, checked at every step by Go, the clients and the end-to-end runs (#1073) |
| sync-driver | [README](fixtures/sync-driver/README.md): single-group socket, retry, cursor and lifecycle decisions derived from iOS and replayed against the pure Rust driver (#9783) |
| stream-chunks | [README](fixtures/stream-chunks/README.md): the pure Rust projection of iOS `StreamChunks.split`, with source-cited Apple cases (#8607) |
| tool-names | [README](fixtures/tool-names/README.md): a tool no table names in words ("GitHub · Create issue"), what its call is about, a shell's command line and its arguments as a key/value list, checked by Android; iOS's own tests hold the same cases until it reads them (#4339) |
| fold-changes | [README](fixtures/fold-changes/README.md): the files a run of work's successful edits changed and their lines, counted by the hub for a fold and by the web for the same calls unfolded (#5157) |
| fold-sources | [README](fixtures/fold-sources/README.md): the web pages a run of work's successful web calls used, read by the hub for a fold and by the web for the same calls unfolded (#9132) |
| input-clip | [README](fixtures/input-clip/README.md): the hub and runtime SDK's third tool-input pass, bounding encoded objects by dropping array tails and then object fields (#2907) |
| provider-import-link | [README](fixtures/provider-import-link/README.md): provider import links (cindy v1), the payload and fragment the web builds for a provider and what the hub reads from a link, checked by Go, the mock hub and the web app (#5420) |
| deep-links | [README](fixtures/deep-links/README.md): the route table of `docs/protocol/deep-links.md` (#9926): the paths the hub's builder writes, and the page each client opens for a web link to a paired hub, an app link (`persona://open<path>`) or an older form, checked by the hub, iOS, the web and the desktop app (Android with #9932) |
| star-prompt | [README](fixtures/star-prompt/README.md): when the GitHub star prompts show, the empty Agents pill, the milestone card after enough Agents started and the onboarding toast, checked by the web app and Android (#5761; iOS with #5910) |
| meeting-echo | [README](fixtures/meeting-echo/README.md): which transcript lines of a meeting are Persona's own answer heard by the microphone (they overlap a stretch it spoke in and their words match what it said; a stretch saved without text keeps the time rule), checked by the hub and the iOS app (#5905) |
| meeting-transcription-problems | [README](fixtures/meeting-transcription-problems/README.md): why the hub isn't transcribing a meeting (#13446): the transcription contribution's error code, the `problem` code the manager passes on (refused, busy, unreachable, failed) and what the phone's fallback notice makes of it, checked by the hub and the iOS app |
| minutes-overview | [README](fixtures/minutes-overview/README.md): the minutes' summary card (#6563), which places of a text are bold (each `strong` entry's first occurrence after the one before), how a client cleans a malformed overview and when it falls back to the minutes as before, and the hub's Markdown of the same minutes, checked by the hub, the mock hub, the iPhone and the web |
| minutes-debates | [README](fixtures/minutes-debates/README.md): real disagreements in meeting minutes (#16728), client cleaning rules, unknown status and extra-key handling, empty/malformed fallback and hub Markdown in both layouts across en, zh-Hans and ja, checked by the hub, the mock hub, the iPhone and the web |
| minutes-wait | [README](fixtures/minutes-wait/README.md): what the minutes line says while a hub-mode meeting's minutes wait for the final pass (#13461): the hub's `transcription.final_done` and `end` and when it sends them, then which line a client shows (checking the whole recording, with minutes done of the total, or writing the minutes) and how it rounds them, checked by the hub, the iPhone and the web |
| speakers-align | [README](fixtures/speakers-align/README.md): speaker timeline rules (#17674), first-time numbering, overlap pairing, recording trimming, two-layer alignment, line eligibility (`spread` / holes / echo), original and merged stats, minor threshold and the four quality checks, checked by the hub (`server/internal/recordingspeakers/recordingspeakersadapt`) |
| speakers-names | [README](fixtures/speakers-names/README.md): speaker display-name four-row table (including `also`) and roster edit rules (#17674), name trimming and length limits, duplicate-name auto-merge and server `undo`, manual merge chain flattening, unmerge returning unnamed, and `clear`, checked by the hub (`server/internal/recordingspeakers/recordingspeakersadapt`) |
| screen-link | [README](fixtures/screen-link/README.md): the live screen's session wire (#6398), every message framed byte for byte alike by the node and the iPhone's client |
| file-types | [README](fixtures/file-types/README.md): a `.ts` / `.mts` / `.cts` file typed by its first bytes (#7974), a real TypeScript module and a real MPEG-TS segment, checked by the node and the hub (Go) |
| graph-layout | [README](fixtures/graph-layout/README.md): an extension view's `graph` (UI version 10, #9727), each node's place in slots, the edges drawn, the reading order with depths, fold counts and group frames, written by the reference `scripts/gen/graph_layout.py` and checked by the web, iOS, Android and the hub |
| organization-snapshot | [README](fixtures/organization-snapshot/README.md): the owner-only runtime tree, three-state availability mapping, open to-dos, concurrency, dated model observations, and complete or incomplete P0 lineage, replayed by the Go server and reserved for later client implementations |
| look-colors | [README](fixtures/look-colors/README.md): automatic theme color seed extraction for uploaded avatars (#15449), pinned on tiny synthetic PNGs so the hub (Go), web (TypeScript) and Apple clients (Swift) produce identical `#RRGGBB` seeds |
| tokens | 见各自 [README](tokens/README.md)；校验夹具如放在 `fixtures/tokens/`，结构由该领域 README 说明 |

## 夹具约定

- 夹具放在 `spec/fixtures/<领域>/*.json`，UTF-8、2 空格缩进、文件末尾换行，按主题拆分。
- 每个领域提供中文 README：规则清单中每条规则一句话，附来源和覆盖用例名；说明 JSON 结构、缺省值、比较方法、验证命令及新增用例的方法。
- 每个用例必须有 `name`、`description`、`source`、`input`、`expected`。`name` 是领域内唯一的英文 snake_case；跨领域引用用 `<领域>/<name>`。`description` 是中文一句话。
- `source` 记录可定位的规则出处，例如 iOS 文件和函数、单测名、文档章节、Go 文件。直接取自 iOS 单测的用例增加 `from_test`；依据实现补充的边界用例增加 `derived: true`，并提供实现对照的验证方法。扩展已有单测场景时，保留测试来源并说明增加了什么。
- `input` 和 `expected` 由各领域定义，不依赖 Swift、Kotlin 或 Go 的运行时类型。保留数组顺序及 JSON 中缺失、`null`、`false`、`0` 的区别；哪些区别不影响行为，必须在领域 README 中明确。
- 推荐用 `{"version": 1, "cases": [...]}` 包装同主题用例；采用其他顶层结构的领域必须在自己的 README 中说明。调整现有格式时同步更新消费者，避免静默跳过未知事件或空用例集。
- 所有数据均为编造。消息、标识符、路径、地址、时间都使用明确的示例值；路径用 `/home/user/project`，域名用 `example.com`，IP 用文档地址段，不从真实会话复制内容。
- 有 Go 对应实现时，写测试读取夹具；只有 iOS 实现时，优先在临时目录编译并引用未修改的源文件核对，程序不入库。做不到的部分应逐项列明已有单测断言和验证限制。
- 编写规格的车道不修改 `ios/` 或 `android/`。提交只包含本车道的文件，执行 `python3 scripts/privacy_check.py --staged`，并扫描提交信息。

## 接入与新增

消费者递归发现自己负责领域的 JSON，按文件顺序之外的独立用例运行；每个用例从自己的初始状态开始。事件序列必须逐步比较状态和动作，不能只看最终结果，也不能跳过失败步骤。

新增用例时，先找到当前 iOS 的函数或单测，编造最小输入，再写明确的期望值。更新领域规则索引，运行已有实现的对照及相关测试；不要用待测实现自动生成期望后直接认定通过。

涉及等待的规格应区分调度常量与实测性能。连续性夹具的 `delay_ms: 1500` 是 `AppModel.settle` 的设定值；实际调度延迟未实测，不作为性能结论。
