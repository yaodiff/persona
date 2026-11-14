# 对话行折叠夹具

本目录固定当前 iOS 的 `items → ChatRow → ChatBlock/Turn → ChatLine` 行为，供 Android 纯 Kotlin/JUnit 实现和以后的 XCTest 共用。判定依据是现有源码和测试；设计稿尚未实现的状态行、失败文案不作为当前 iOS 的输出。所有消息、标识和时间均为编造数据。

## 文件和执行范围

| 文件 | 覆盖内容 |
| --- | --- |
| `structure.json` | 基本问答、行标识、菜单归属、回顾、中断、压缩通知 |
| `queued.json` | 中途吸收、多消息轮按消息分段、普通图片和被吸收的图片 |
| `wakes.json` | 多次唤醒取答、后续抽屉、回顾后重新归轮、图片归属 |
| `work.json` | 思考叙述、工具配对、失败、缺结果、工具图片、时间边界 |
| `folds.json` | fold 计数、重复页、扩大范围、展开还原、范围外的结果 |
| `tasks.json` | 后台任务关联、主题、结果文案和后台徽标 |
| `frozen.json` | 按 `(seq, part)` 冻结、冻结时的进行中轮、补入更早历史 |
| `rebuild.json` | 增量与全量重建等价、前插历史、后台突发和替换收敛 |
| `layout.json` | 待处理交互在正文之外、输入框上方的位置契约 |

61 个用例中，59 个通过 Swift 调用现有逻辑核对；`layout.json` 的 2 个用例通过 `mainFooter` 的源码顺序核对，未运行 SwiftUI 界面测试。`verification: "view_source_order"` 明示这一区别。没有仅凭推测填写的 `derived` 用例，也没有只能依赖 XCTest 断言、无法对照的内容用例。

## 规则及来源

每行是一条规则，末列给出覆盖用例；用例内的 `source` 保留完整文件和函数名称。

| 规则 | 来源 | 用例 |
| --- | --- | --- |
| 空窗口不生成行，纯问答不补空工作行。 | `ChatView.chatLines` | `empty_session`、`plain_exchange` |
| 进行中轮即使还没步骤也保留工作行，只有最后一段可以进行中。 | `chatLines(live:)`、`liveTurnID` | `live_empty_work`、`live_reply_stays_after_work`、`live_queued_message_keeps_position` |
| 普通用户文本开新轮，`queued: true` 文本加入当前轮，窗口首条 queued 文本可以独立开轮。 | `ChatBlock.build` | `ordinary_user_starts_next_turn`、`queued_without_existing_turn` |
| 完成轮的多条用户文本和进行中一样按消息分段，每条一个消息行，工作和回复挂在各自下面（#2512 起不再合并）。 | `chatLines`、`segments` | `finished_queued_messages_apart`、`open_queued_messages_work`、`three_users_apart_without_steps` |
| 工作展开只追加独立步骤和收起行，已有消息和工作标题的标识不变。 | `ChatLineTests.workExpansionKeepsExistingIdentities`、`unfoldWork` | `closed_work_identity`、`open_work_identity` |
| 多消息轮里每条消息的菜单归属于它自己那一行。 | `ChatLineTests.messageMenuStillBelongsToItsOriginalRow`、`ChatLine.holds` | `menu_owner_per_message` |
| 每段最后一条助手文本是回复，之前的助手文本作为工作叙述。 | `chatLines`、`workItems` | `open_queued_messages_work`、`trailing_tool_after_answer` |
| 非空 thinking 保留为可展开叙述，独立的空白 thinking 不进入工作行。 | `workItems`、`ThinkingRow`；`CLAUDE.md` thinking 经验 | `thinking_narration_preserved`、`standalone_empty_thinking_hidden` |
| thinking 在工具前形成独立行，在连续工具之间或末尾进入工具组，组内空 thinking 也保留。 | `ChatStore.buildRows`、`workItems` | `thinking_before_between_after_tools`、`empty_thinking_between_tools_is_retained` |
| 工具按 `tool_use_id` 在整个窗口配结果，最后一个结果生效，孤立结果不另生成行。 | `buildRows` | `late_result_attaches_to_original_call`、`last_result_for_call_wins`、`orphan_tool_result_is_not_a_row` |
| 工具缺结果时 `ToolCall.isRunning` 为真，但不会据此把完成轮的工作行改成进行中。 | `ToolCall.isRunning`、`chatLines(live:)` | `missing_tool_result_is_running` |
| 工具失败只取结果的 `is_error`，不根据输出文字推断。 | `ToolCall.isError` | `failed_tool_counts_known_failure` |
| 带 `tool_use_id` 的图片归工具卡，普通及被吸收的用户图片在消息上方显示。 | `buildRows`、`photos`、`chatLines`；`CLAUDE.md` 图片两条路 | `tool_images_belong_to_call`、`regular_user_images`、`absorbed_user_images` |
| After background-task wakes the answer is the latest run's last reply, whatever its length (#638; it used to be the longest). These cases were updated from `ChatStoreTests.backgroundWakeEndsOnTheNewestWords` and `wakeAfterARecapStaysInItsExchange`, read against the Swift code rather than run. | `Turn.pickAnswerAcrossWakes` | `multiple_wakes_pick_latest_answer`、`only_last_reply_in_each_wake_is_candidate`、`short_reply_after_wake_is_the_answer`、`wake_after_recap_reopens_turn`、`photos_after_wake_stay_with_message` |
| 选中回复之前的工作留在主列表，之后的助手内容只放抽屉，抽屉从第一次唤醒开始。 | `pickAnswerAcrossWakes`、`wakeLines` | `short_reply_after_wake_is_the_answer`、`long_answer_after_wake`、`live_tail_runs_behind_chip` |
| 唤醒后的用户文本和图片仍留在主轮，`tail` 和 `afterWake` 排除这些用户输入。 | `pickAnswerAcrossWakes`、`ChatRow.isUserInput`；`ChatStoreTests.picturesSentAfterAWakeStayWithTheirMessage` | `photos_after_wake_stay_with_message` |
| 回顾独立于轮，回顾后的唤醒能重新打开原轮且不移动回顾。 | `ChatBlock.build`；`ChatStoreTests.wakeAfterARecapStaysInItsExchange` | `recap_separates_turns`、`wake_after_recap_reopens_turn` |
| 没有唤醒标记的后台通知只算工作过程，没有回复的唤醒轮也保留抽屉入口。 | `ChatBlock.build`、`pickAnswerAcrossWakes` | `notification_without_wake`、`wake_without_any_reply` |
| 窗口以唤醒通知开头时先显示该通知，后续运行可以由唤醒入口承接。 | `chatLines`、`wakeLines` | `orphan_wake_opens_with_notice`、`live_tail_runs_behind_chip`、`live_answer_after_wake` |
| 无任务字段且含“中断”的通知进入轮尾，压缩通知和任务标题里的“中断”仍属工作过程。 | `ChatBlock.build`；`TaskNoteTests.aNoticeAboutAnInterruptIsNotTheInterrupt` | `interrupt_is_trailing_notice`、`compaction_stays_inside_work`、`task_title_containing_interrupt_is_not_interrupt` |
| fold 按其代表的工具和思考计数，结束时间取 `fold.until`，收起步数按工具数加思考数。 | `ToolStep`、`unfoldWork`；`FoldedWorkTests.aFoldCountsWhatItStandsFor` | `fold_counts_tools_and_thoughts` |
| fold 替换范围内的工作，重复 fold 幂等，扩大范围替换旧 fold，展开还原原步骤。 | `ChatStore.dropFolded`、`unfold`；`FoldedWorkTests.aFoldReplacesTheStepsItCoversAndOpeningItPutsThemBack` | `fold_replaces_extends_and_unfolds` |
| fold 范围外的工具仍接收之后到达的结果。 | `buildRows`；`FoldedWorkTests.stepsOutsideAFoldStay` | `outside_fold_tool_keeps_late_result` |
| 后台通知优先按调用标识关联，再按工具结果公布的任务标识关联。 | `TaskIndex.build`；`TaskNoteTests.noticesFindTheirCalls` | `task_notices_find_calls` |
| 后台任务主题优先用调用描述，Monitor 事件优先用详情首个非空行，找不到调用时用通知标题。 | `TaskNoteText.subject` | `task_notices_find_calls`、`monitor_event_uses_first_detail_line`、`task_without_loaded_call_uses_title` |
| 后台结果与徽标按状态和退出码生成，事件只累计事件数，尚无通知时为后台运行中。 | `TaskNoteText.outcome`、`badge` | `task_notices_find_calls`、`background_running_badge`、`task_status_outcomes` |
| 当前工作行从本段消息或首个步骤算到末条内容，进行中时算到输入的固定时钟，缺起点则隐藏耗时。 | `chatLines`、`WorkLine.duration` | `timed_work_ends_at_answer`、`missing_start_time_hides_duration`、`missing_start_time_uses_first_step`、`trailing_tool_after_answer` |
| 冻结按数字块序号截取 item 前缀，保留冻结时的进行中轮，更早历史仍能补入。 | `currentLines`、`prefixCount`、`Frozen`；`ux.md` 3.5 | `frozen_snapshot_excludes_newer_items`、`frozen_uses_numeric_block_order`、`older_page_stays_visible_during_freeze` |
| 增量重建须等于全量重建，迟到结果、前插历史和整段替换都不能遗留旧行。 | `ChatStore.rebuild`、`scheduleRebuild`；`RebuildTests` | `incremental_with_late_result_and_older_page`、`background_bursts_and_replacement` |
| 窗口可从助手过程开始，未知类型不生成消息。 | `buildRows`、`ChatBlock.build` | `orphan_assistant`、`unknown_item_is_ignored` |
| 请求面板按给定顺序排在正文外、输入框上方，不给它伪造 transcript 行。 | `ChatView.mainFooter`、`chatScroll`；`ux.md` 3.2、3.4 | `single_interaction_in_footer`、`multiple_interactions_in_footer` |

## JSON 结构

每个文件是 `{"schema_version": 1, "domain": "lines", "cases": [...]}`。每个用例都有唯一的英文 snake_case `name`、中文 `description`、出处数组 `source`、`input` 和 `expected`。由原测试搬出的场景写 `from_test`，实现边界与缩小后的性质场景写 `derived: true`；两者不混用。

### 输入

`input.items` 使用 [协议的 Item](../../../docs/protocol/protocol.md) 字段名，按 transcript 顺序排列，`id`、`seq`、`role`、`kind` 必填；未给出的可选字段视为 `null`。同一 `seq` 内按 `id` 最后一个 `#` 后的数字排序，不按字符串排序。夹具不负责解析原始 `attachment/queued_command`：它已经被服务端规范化成 `queued: true` 的用户文本及图片。

支持 `text`、`thinking`、`tool_use`、`tool_result`、`notice`、`image`、`recap`、`fold`，未知 `kind` 按现有宽容解码处理。`tool_input`、`image`、`task`、`fold` 的嵌套结构直接采用线上结构；`task` 是 notice 的字段，不是独立 kind。图片只用编造的占位数据，不测试图片解码。

| 可选字段 | 含义与默认值 |
| --- | --- |
| `live_turn_id` | 当前进行中的 Turn ID，缺省为 `null`，对应 iOS 已从会话状态选出的轮，不根据工具结果自行推断 |
| `open_work` | 展开的行 ID 集合，缺省为空，既可以有 `w-…`，也可以有抽屉的 `dw-…` |
| `now` | 固定比较时钟，缺省为 `2026-01-01T00:01:00Z`，只用于工作行时长投影 |
| `menu_queries` | 要传给每行 `holds` 的原消息 ID，允许 `null`，结果按查询顺序排列 |
| `frozen` | `{seq, part, live_turn_id}`，按该位置截取 item 前缀并用冻结时的进行中轮重建 |
| `check_prefixes` | 为真时，对每个 item 前缀验证增量 Rows/Blocks 与全量结果相同，缺省为假 |
| `operations` | 有序的状态操作，存在时 `expected` 改为每次操作后的 `checkpoints` |
| `pending_interaction_ids` | 只用于位置用例的请求标识列表，不参与 items 折叠 |

`operations` 支持 `merge`、`replace`、`unfold` 和 `burst_replace`。前三者的 `items` 分别是新页、完整替换页和展开取回的原步骤；`unfold` 还需 `fold_id`。`burst_replace.batches` 按批合并，每批后让后台任务执行，再立即用 `items` 替换整段记录。每次操作可以覆盖 `open_work`、`live_turn_id`。对照器等待后台 Rows/Blocks 收敛，带明确超时。

### 期望输出

普通用例的 `expected.lines` 按阅读顺序排列，即最早到最新，iOS 倒置列表的渲染顺序不改变这个契约。`expected.turns` 是中间轮结构的核对信息；它有助于区分取答、图片归属与最终渲染规则。操作用例的 `expected.checkpoints` 按操作顺序保存同样结构，并额外列出 `item_ids`。

每行都有 `id`、`type`、`is_input`；提供 `menu_queries` 时还含 `holds` 布尔数组。内容行保留 `text`，其他内容通过 `item_ids` 或步骤里的 `item_id` 回指输入，不复制图片和工具输入等大字段。

| `type` | 内容与结构 |
| --- | --- |
| `user` / `reply` / `thinking` / `notice` / `image` / `recap` | 原条目行；recap 是独立的历史标记，不增加轮间分隔线 |
| `user_images` | `item_ids`，图片组在所属文本上方，ID 为 `img-` 加首图 ID |
| `work` | 过程步骤、计数、时间、展开状态和进行中状态，见下文 |
| `work_step` | `group_id`、`first` 和 `step`，ID 为组 ID 加 `/` 再加步骤 ID |
| `work_end` | `group_id` 和收起步数 `count`，ID 为组 ID 加 `-end` |
| `wakes` | `notice_ids`、`live` 及真正的 `drawer_lines`，ID 为 `wk-` 加 Turn ID |
| `task_note` | 原通知内容及 `task_note` 投影，包含标签、主题、结果、失败标记和回配的调用 ID |

`work` 的 `steps` 即折叠内容，展开时每个步骤另有一条 `work_step`，两者不重复计数。步骤类型包括 `tool`、`thinking`、`notice`、`task_note`、`narration`、`fold`。工具步骤保存实际结果 ID、结果文本、`running`、`failed`、图片数和可选的 `background_badge`；任务步骤沿用 `TaskNoteText` 的标签与结果；fold 保存代表计数和加载范围。

工作计数的含义：

- `tools`、`thoughts` 是步骤所代表的工具数和思考数，包含 fold 的计数。
- `known_failed_tools`、`unresolved_tools` 是当前可见工具卡中，错误标记为真和缺少结果的数量；`contains_fold` 为真时它们仅是下界，fold 没有提供失败数或缺结果数。
- `summary_full`、`summary_short` 是当前 iOS `WorkSummary` 的完整与简略文案；有 `wake: true` 的通知会让文案里的工具数加一，因此文案中的数字可能和实际 `tools` 不同。
- `live` 完全来自进行中轮和段位置，`open` 来自展开集合；`running` 只描述单张工具卡缺结果。

`start`、`end` 是折叠算法选出的时间戳，`elapsed_seconds` 和 `duration_text` 使用固定时钟按现有 `WorkLine.duration` 计算；这些是编造时间戳的规格值，不是性能测量。缺起点时分别为 `null` 和空字符串。完成段缺结束时间时，现有 View 使用当前时钟；对照器以 `now` 固定这条路径。

`separator_before` 只描述展开卡片内部的结构：首步骤没有分隔线，后续步骤及收起行有分隔线；不规定颜色、线宽、位置等像素。原始 iOS 代码分别来自 `WorkStepView` 和 `lineView` 的 `.workEnd` 分支。

`turns` 包含用户、过程、回复、轮尾、唤醒、抽屉和到达顺序的 ID 列表，以及 Turn 本身的计数和起止时间；`process_ids` 中的工具组使用 `tools-<首步骤 ID>`。Turn 统计发生在 View 的空思考过滤之前，不能直接拿 `thought_count` 当工作行的 `thoughts`。有任务通知时还输出 `task_index`，包含 `started`、通知到调用的 `calls`、描述、开始时间和按调用归组的状态序列。

`layout.json` 额外输出 `footer`：请求 ID 顺序、`placement: "above_composer"`、`part_of_transcript: false`；不把请求交互和新的 `StatusLine` 塞进 transcript 行。

## 与设计文档的差异

- [总纲 2.3、D1、D4](../../../docs/archive/redesign/README.md) 和 [架构调研 A4/A5](../../../docs/archive/redesign/research/persona-ios-arch.md) 指定共享行为夹具，本目录固定的是当前 iOS 输出，没有重构生产代码。
- [UX 3.3](../../../docs/archive/redesign/ux.md) 要求折叠时显示失败数、缺结果记录，且耗时只取记录中的轮次时长；当前 `WorkSummary` 未显示这些计数，`WorkLine` 仍用时间戳差及当前时钟，本夹具保留现状，并另列已知失败和缺结果数供后续使用。
- 协议说空思考不显示，实际 `workItems` 只过滤独立 thinking，已并入 `.tools` 的空思考未经再次过滤；两个空思考用例分别固定这两条路径。
- [UX 3.2/5.1](../../../docs/archive/redesign/ux.md) 的统一状态行尚未进入当前 `ChatView`，本目录只覆盖工作行的 `live`，不提前定义新状态机。
- 总纲第 5 节提到权限卡挂记录末尾，而当前 `mainFooter` 把所有请求面板放在输入区；本夹具采用实际位置，与 UX 3.2/3.4 的当前规则一致。
- Swift 的 `String.count` 数扩展字素簇，Kotlin 实现取答时须注意复合表情，不能直接用 UTF-16 长度代替。

## 对照和复跑

对照器没有独立文件入库，下面的复跑步骤在 `/tmp` 生成所需临时文件。Python 读取本 README 的 Swift 适配片段，再从 iOS 源码按唯一标记提取类型和方法；`ChatStore.swift`、`JSONValue.swift`、`TextMemo.swift` 直接作为 `swiftc` 输入，原文件不改动。编译使用 Swift 6 模式，并按 `ios/project.yml` 指定默认 `MainActor`，保留 `ChatStore` 的并发语义。`ChatView` 的私有函数体保持逐字一致，只有外部宿主和输出投影是临时适配代码，未重写折叠算法。

适配器只替代了不比较的图标名称函数，不运行 SwiftUI、滚动、点击、图片解码和真实磁盘缓存恢复；临时缓存通过 `CFFIXED_USER_HOME` 限定在临时目录。源码位置用例校验 `InteractionPanel` 在 `mainFooter` 的 `Composer` 之前；它们只证明声明顺序，不宣称界面测试通过。分隔线的输出结合原 `first` 标记与 View 源码核对。

四份指定测试的覆盖情况：

| 原测试 | 这里如何覆盖 |
| --- | --- |
| `ChatLineTests` 两个测试 | `structure.json` 固定稳定 ID、展开插入行、输入判定和菜单归属，对照时另检查行 ID 唯一及反转可还原 |
| `RebuildTests.incrementalRebuildEqualsAFullOne` | 对照器读取原测试的 LCG 和 session 生成器，验证 40 个种子、全部 6400 个前缀和每个种子前插历史后的等价性 |
| `RebuildTests.incrementalRebuildIsCheap` | 重现长会话的结果等价断言，不将单次耗时当性能基线，本车道未实测性能 |
| `RebuildTests.realSessionsRebuildIncrementally` | 未读取私有语料；前缀与前插历史规则用编造序列验证，不声称覆盖真实会话内容 |
| `RebuildTests.backgroundRebuildConvergesAfterBurstsAndReplacement` | 夹具保留缩小场景，对照器另用原生成器重现突发和替换，带超时等待收敛 |
| `TaskNoteTests` 两个测试 | `tasks.json` 固定调用关联、事件主题、徽标、退出码和时间文案、中断标题的行为 |
| `FoldedWorkTests` 三个测试 | `folds.json` 实际调用 `merge` 和 `unfold`，逐个检查状态快照和最终步骤 |

在仓库根目录执行下面的 Python 块；需要 macOS、Python 3 和支持源码中 `nonisolated` 声明的 Swift 工具链。编译有超时，每次夹具与性质检查也有超时；失败立即退出。`-disable-sandbox` 只避免 Swift 宏插件再启动一层系统 sandbox，外层执行环境的限制仍然有效。

```python
from pathlib import Path
import subprocess
import os
import json
import re
import tempfile

root = Path.cwd()
tmp = Path(tempfile.mkdtemp(prefix='spec-lines-', dir='/tmp'))
readme = (root / 'spec/fixtures/lines/README.md').read_text()
(tmp / 'Runner.swift').write_text(readme.split('```swift\n', 1)[1].split('```', 1)[0])

def between(path, start, end):
    text = (root / path).read_text()
    assert text.count(start) == 1, start
    begin = text.index(start)
    finish = text.index(end, begin)
    return text[begin:finish]

model = 'apple/Shared/Models.swift'
view = 'apple/Persona/Views/Chat/ChatView.swift'
parts = ['import Foundation\n']
parts += [between(model, 'nonisolated enum SessionStatus:', 'nonisolated struct Session:'),
          between(model, 'nonisolated struct SessionKey:', 'nonisolated enum InteractionKind:')]
parts += [between(view, 'struct ChatLine: Identifiable {', '/// One step of an open work line,'),
          between(view, 'struct WorkSummary {', 'struct WorkLine: View {')]
parts += ['struct ChatView {\nvar openWork: Set<String> = []\nstatic let debugOpenAll = false\n']
for start, end in [
    ('    static func prefixCount(', '    private func chatLines('),
    ('    private func chatLines(', '    #if DEBUG'),
    ('    private func unfoldWork(', '    /// Folds or opens a work line'),
    ('    private static func photos(', '    @ViewBuilder private func lineView('),
    ('    private func wakeLines(', '    static let bottomID'),
]:
    parts += [between(view, start, end)]
parts += ['''
    func lines(_ blocks: [ChatBlock], live: String?) -> [ChatLine] {
        unfoldWork(chatLines(blocks, live: live))
    }
    func drawer(_ turn: Turn, live: Bool) -> [ChatLine] {
        unfoldWork(wakeLines(turn, live: live))
    }
}\nenum WorkLine {\n''']
parts += [between(view, '    static func duration(from start:', '\n}\n\nprivate struct EditIndex') + '\n}\n']
parts += [between('apple/Persona/Views/Chat/TaskNoteRow.swift', 'enum TaskNoteText {', '/// A background task reporting back,')]
parts += ['struct RebuildTests {\n' + between('apple/PersonaTests/RebuildTests.swift', '    struct LCG {', '    @Test func incrementalRebuildEqualsAFullOne()') + '\n}\n']
parts += ['enum ToolPresentation { static func symbol(_ name: String) -> String { "" } }\n']
(tmp / 'Extracted.swift').write_text('\n'.join(parts))
subprocess.run(['swiftc', '-parse-as-library', '-swift-version', '6', '-module-cache-path', str(tmp / 'module-cache'), '-default-isolation', 'MainActor', '-enable-bare-slash-regex', '-disable-sandbox',
    'apple/Packages/PersonaWire/Sources/PersonaWire/JSONValue.swift', 'apple/Shared/TextMemo.swift', 'apple/Shared/ChatStore.swift',
    str(tmp / 'Extracted.swift'), str(tmp / 'Runner.swift'), '-o', str(tmp / 'check')],
    check=True, timeout=120)

view_source = (root / view).read_text()
assert '.overlay(alignment: .top) { if !first { WorkCardSlice.separator } }' in view_source
assert '.overlay(alignment: .top) { WorkCardSlice.separator }' in view_source
runtime = tmp / 'runtime'
runtime.mkdir()
env = dict(os.environ, CFFIXED_USER_HOME=str(runtime))
names = set()
all_cases = {}

def check_lines(lines):
    ids = [line['id'] for line in lines]
    assert len(ids) == len(set(ids))
    assert list(reversed(list(reversed(ids)))) == ids
    for line in lines:
        if 'drawer_lines' in line:
            check_lines(line['drawer_lines'])

for path in sorted((root / 'spec/fixtures/lines').glob('*.json')):
    data = json.loads(path.read_text())
    assert data['schema_version'] == 1 and data['domain'] == 'lines'
    assert path.read_text() == json.dumps(data, ensure_ascii=False, indent=2) + '\n'
    for case in data['cases']:
        assert re.fullmatch('[a-z][a-z0-9_]*', case['name'])
        assert case['name'] not in names
        names.add(case['name'])
        assert bool(case.get('from_test')) != bool(case.get('derived'))
        assert case['description'] and case['source']
        all_cases[case['name']] = case
        for state in case['expected'].get('checkpoints', [case['expected']]):
            check_lines(state['lines'])
    subprocess.run([str(tmp / 'check'), str(path)], env=env, check=True, timeout=60)

closed = all_cases['closed_work_identity']['expected']['lines']
opened = all_cases['open_work_identity']['expected']['lines']
assert [line['id'] for line in closed] == [opened[i]['id'] for i in [0, 1, 4]]
assert all_cases['menu_owner_in_merged_messages']['expected']['lines'][0]['holds'] == [True, False, False]
assert all_cases['fold_counts_tools_and_thoughts']['expected']['turns'][0]['tool_count'] == 3
assert all_cases['task_notices_find_calls']['expected']['task_index']['calls'] == {'7#0': 'm', '8#0': 'b', '9#0': 'm'}
subprocess.run([str(tmp / 'check'), '--properties'], env=env, check=True, timeout=60)
print(f'全部通过：{len(names)} 个夹具；临时对照器：{tmp}')
```

临时 Swift 适配片段如下，由上面的入口提取；不单独保存到仓库。

```swift
import Foundation

func stamp(_ d: Date?) -> Any { d.map { ISO8601DateFormatter().string(from: $0) } ?? NSNull() }
func nullable<T>(_ x: T?) -> Any { x.map { $0 as Any } ?? NSNull() }
func itemID(_ row: ChatRow) -> String { row.id }

struct Projection {
    let index: TaskIndex
    let view: ChatView
    let now: Date
    let menuIDs: [String?]

    func note(_ it: Item) -> [String: Any] {
        guard let n = it.task else { return [:] }
        let call = index.calls[it.id]
        return ["label": TaskNoteText.label(n),
                "subject": TaskNoteText.subject(n, description: call.flatMap { index.descriptions[$0] }),
                "outcome": nullable(TaskNoteText.outcome(n, startedAt: call.flatMap { index.startedAt[$0] }, at: it.ts)),
                "failed": n.failed, "call_id": nullable(call)]
    }
    func row(_ r: ChatRow) -> [String: Any] {
        var type: String
        var it: Item?
        switch r {
        case .user(let i): type = "user"; it = i
        case .assistant(let i): type = "reply"; it = i
        case .thinking(let i): type = "thinking"; it = i
        case .notice(let i): type = i.task != nil && i.wake != true ? "task_note" : "notice"; it = i
        case .image(let i): type = "image"; it = i
        case .recap(let i): type = "recap"; it = i
        case .tools: type = "tools"
        }
        var out: [String: Any] = ["type": type, "id": r.id]
        if let it { out["text"] = nullable(it.text) }
        if let it, type == "task_note" { out["task_note"] = note(it) }
        return out
    }
    func step(_ work: WorkItem) -> [String: Any] {
        switch work {
        case .text(let i): return ["type": "narration", "id": work.id, "item_id": i.id, "text": nullable(i.text)]
        case .step(.call(let c)):
            var out: [String: Any] = ["type": "tool", "id": c.id, "call_id": nullable(c.use.toolUseID),
                "name": c.name, "result_id": nullable(c.result?.id), "result_text": nullable(c.result?.text),
                "running": c.isRunning, "failed": c.isError, "image_count": c.images.count]
            if let id = c.use.toolUseID, index.started.contains(id) || index.notes[id] != nil {
                let badge = TaskNoteText.badge(index.notes[id] ?? [])
                out["background_badge"] = ["text": badge.text, "failed": badge.failed]
            }
            return out
        case .step(.folded(let f)):
            return ["type": "fold", "id": f.id, "tools": f.fold?.tools ?? 0, "thoughts": f.fold?.thoughts ?? 0,
                "items": f.fold?.items ?? 0, "through_seq": nullable(f.fold?.throughSeq),
                "through_id": nullable(f.fold?.throughID), "until": stamp(f.fold?.until)]
        case .step(.thought(let i)):
            var out: [String: Any] = ["type": i.kind == .notice ? (i.task != nil && i.wake != true ? "task_note" : "notice") : "thinking",
                "id": i.id, "text": nullable(i.text)]
            if i.task != nil && i.wake != true { out["task_note"] = note(i) }
            return out
        }
    }
    func line(_ l: ChatLine) -> [String: Any] {
        var out: [String: Any] = ["id": l.id, "is_input": l.isInput]
        if !menuIDs.isEmpty { out["holds"] = menuIDs.map { l.holds($0) } }
        switch l.content {
        case .row(let r): out.merge(row(r)) { _, new in new }
        case .userImages(_, let items): out["type"] = "user_images"; out["item_ids"] = items.map(\.id)
        case .mergedUsers(_, let rows): out["type"] = "merged_users"; out["item_ids"] = rows.map(\.id)
        case .splitHeader(_, let count): out["type"] = "split_header"; out["count"] = count
        case .work(let id, let items, let live, let start, let end):
            let summary = WorkSummary(items)
            let at = live ? now : (end ?? now)
            var tools = 0, thoughts = 0, failures = 0, unresolved = 0, folded = false
            for i in items {
                switch i {
                case .step(let s):
                    tools += s.tools
                    if case .thought(let t) = s { if t.kind != .notice { thoughts += 1 } }
                    if case .folded = s { thoughts += s.thoughts; folded = true }
                    if case .call(let c) = s { if c.isError { failures += 1 }; if c.isRunning { unresolved += 1 } }
                default: break
                }
            }
            out.merge(["type": "work", "live": live, "open": view.openWork.contains(id),
                "start": stamp(start), "end": stamp(end),
                "elapsed_seconds": start.map { max(0, Int(at.timeIntervalSince($0))) } as Any? ?? NSNull(),
                "duration_text": WorkLine.duration(from: start, to: at),
                "tools": tools, "thoughts": thoughts, "known_failed_tools": failures,
                "unresolved_tools": unresolved, "contains_fold": folded,
                "summary_full": summary.full, "summary_short": summary.short,
                "steps": items.map(step)]) { _, new in new }
        case .workStep(let group, let item, let first):
            out.merge(["type": "work_step", "group_id": group, "first": first,
                "separator_before": !first, "step": step(item)]) { _, new in new }
        case .workEnd(let group, let count):
            out.merge(["type": "work_end", "group_id": group, "count": count, "separator_before": true]) { _, new in new }
        case .wakes(let t, let live):
            out.merge(["type": "wakes", "notice_ids": t.wakes.map(\.id), "live": live,
                "drawer_lines": view.drawer(t, live: live).map(line)]) { _, new in new }
        }
        return out
    }
    func turn(_ t: Turn) -> [String: Any] {
        ["id": t.id, "user_ids": t.users.map(\.id), "process_ids": t.process.map(\.id),
         "answer_id": nullable(t.answer?.id), "trailing_ids": t.trailing.map(\.id),
         "wake_ids": t.wakes.map(\.id), "after_wake_ids": t.afterWake.map(\.id), "tail_ids": t.tail.map(\.id),
         "ordered_ids": t.ordered.map(\.id), "tool_count": t.toolCount, "thought_count": t.thoughtCount,
         "interim_count": t.interimCount, "start": stamp(t.startedAt), "end": stamp(t.endedAt)]
    }
}

@main struct Runner {
    static func decode<T: Decodable>(_ value: Any, _ type: T.Type) throws -> T {
        try JSON.decoder.decode(type, from: JSONSerialization.data(withJSONObject: value))
    }
    @MainActor static func snapshot(_ items: [Item], _ input: [String: Any]) throws -> [String: Any] {
        var source = items
        var live = input["live_turn_id"] as? String
        if let f = input["frozen"] as? [String: Any], let seq = f["seq"] as? Int, let part = f["part"] as? Int {
            let cut = ChatView.prefixCount(source) { ($0.seq, $0.part) <= (seq, part) }
            source = Array(source[..<cut]); live = f["live_turn_id"] as? String
        }
        let rows = ChatStore.buildRows(source), blocks = ChatBlock.build(rows)
        if input["check_prefixes"] as? Bool == true {
            var settled: ChatStore.SettledRows?
            for n in 0...source.count {
                let prefix = Array(source.prefix(n))
                let built = ChatStore.rebuild(prefix, settled: settled)
                let full = ChatStore.buildRows(prefix)
                precondition(built.rows == full && built.blocks == ChatBlock.build(full), "incremental mismatch at \(n)")
                settled = built.settled
            }
        }
        var view = ChatView()
        view.openWork = Set(input["open_work"] as? [String] ?? [])
        let now = JSON.parseDate(input["now"] as? String ?? "2026-01-01T00:01:00Z")!
        let queries = (input["menu_queries"] as? [Any] ?? []).map { $0 as? String }
        let p = Projection(index: TaskIndex.build(source), view: view, now: now, menuIDs: queries)
        let turns = blocks.compactMap { if case .turn(let t) = $0 { t } else { nil } }
        var out: [String: Any] = ["lines": view.lines(blocks, live: live).map(p.line), "turns": turns.map(p.turn)]
        if source.contains(where: { $0.task != nil }) {
            out["task_index"] = ["started": p.index.started.sorted(), "calls": p.index.calls,
                "descriptions": p.index.descriptions, "started_at": p.index.startedAt.mapValues(stamp),
                "statuses": p.index.notes.mapValues { $0.map { nullable($0.status) } }]
        }
        return out
    }
    @MainActor static func run(_ input: [String: Any]) async throws -> [String: Any] {
        let items = try decode(input["items"] ?? [], [Item].self)
        guard let operations = input["operations"] as? [[String: Any]] else { return try snapshot(items, input) }
        let store = ChatStore(key: SessionKey(machineID: "fixture", sessionID: UUID().uuidString))
        store.merge(items)
        var checkpoints: [[String: Any]] = []
        for op in operations {
            let added = try decode(op["items"] ?? [], [Item].self)
            switch op["op"] as? String {
            case "merge": store.merge(added, backlog: true, hasMore: false)
            case "burst_replace":
                for batch in op["batches"] as! [[Any]] {
                    store.merge(try decode(batch, [Item].self))
                    await Task.yield()
                }
                store.replace(with: added, hasMore: false, span: ItemSpan(history: "replacement", to: added.last?.seq))
            case "replace": store.replace(with: added, hasMore: false, span: ItemSpan(history: "replacement", to: added.last?.seq))
            case "unfold":
                let f = store.items.first { $0.id == op["fold_id"] as? String }!
                store.unfold(f, into: added)
            default: fatalError("unknown operation")
            }
            let deadline = ContinuousClock.now + .seconds(5)
            let expectedRows = ChatStore.buildRows(store.items)
            while (store.rows != expectedRows || store.blocks != ChatBlock.build(expectedRows)) && ContinuousClock.now < deadline {
                try await Task.sleep(for: .milliseconds(5))
            }
            precondition(store.rows == expectedRows && store.blocks == ChatBlock.build(expectedRows), "background rebuild timeout")
            var options = input
            for key in ["open_work", "live_turn_id"] { if let value = op[key] { options[key] = value } }
            var state = try snapshot(store.items, options)
            state["item_ids"] = store.items.map(\.id)
            checkpoints.append(state)
        }
        return ["checkpoints": checkpoints]
    }
    @MainActor static func main() async throws {
        let args = CommandLine.arguments
        if args[1] == "--properties" {
            var prefixes = 0
            for seed in UInt64(1)...40 {
                let all = RebuildTests.session(seed, count: 160)
                var settled: ChatStore.SettledRows?
                for n in 1...all.count {
                    let items = Array(all.prefix(n)), rows = ChatStore.buildRows(items)
                    let built = ChatStore.rebuild(items, settled: settled)
                    precondition(built.rows == rows && built.blocks == ChatBlock.build(rows))
                    settled = built.settled; prefixes += 1
                }
                let older = RebuildTests.session(seed + 1000, count: 30).map { it in
                    var it = it; it.id = "old-" + it.id; it.toolUseID = it.toolUseID.map { "old-" + $0 }; return it
                }
                let built = ChatStore.rebuild(older + all, settled: settled)
                let rows = ChatStore.buildRows(older + all)
                precondition(built.rows == rows && built.blocks == ChatBlock.build(rows))
            }
            var large: [Item] = []
            var seq = 0
            for t in 0..<1000 {
                seq += 1; large.append(RebuildTests.item(seq, "user", .text, text: "第 \(t) 轮"))
                for _ in 0..<6 {
                    seq += 1; large.append(RebuildTests.item(seq, "assistant", .toolUse, tool: "t\(seq)"))
                    seq += 1; large.append(RebuildTests.item(seq, "user", .toolResult, text: "ok", tool: "t\(seq - 1)"))
                }
                seq += 1; large.append(RebuildTests.item(seq, "assistant", .text, text: "答"))
            }
            let settled = ChatStore.rebuild(large, settled: nil).settled
            seq += 1; large.append(RebuildTests.item(seq, "assistant", .thinking, text: "又想"))
            precondition(ChatStore.rebuild(large, settled: settled).rows == ChatStore.buildRows(large))
            let store = ChatStore(key: SessionKey(machineID: "fixture", sessionID: UUID().uuidString))
            let first = RebuildTests.session(8, count: 1800)
            for end in stride(from: 30, through: first.count, by: 30) {
                store.merge(Array(first[(end - 30)..<end])); await Task.yield()
            }
            var replacement = RebuildTests.session(91, count: 400)
            for i in replacement.indices { replacement[i].id = "replacement-" + replacement[i].id }
            store.replace(with: replacement, hasMore: false, span: ItemSpan(history: "new", to: 400))
            let rows = ChatStore.buildRows(store.items)
            let deadline = ContinuousClock.now + .seconds(5)
            while (store.rows != rows || store.blocks != ChatBlock.build(rows)) && ContinuousClock.now < deadline {
                try await Task.sleep(for: .milliseconds(5))
            }
            precondition(store.rows == rows && store.blocks == ChatBlock.build(rows))
            precondition(store.items.allSatisfy { $0.id.hasPrefix("replacement-") })
            print("通过：\(prefixes) 个增量前缀、40 次前插历史、长会话等价和后台替换收敛")
            return
        }
        let path = args[1]
        let fixtures = try JSONSerialization.jsonObject(with: Data(contentsOf: URL(fileURLWithPath: path))) as! [String: Any]
        let cases = fixtures["cases"] as! [[String: Any]]
        for c in cases {
            var actual = try await run(c["input"] as! [String: Any])
            if c["verification"] as? String == "view_source_order" {
                let source = try String(contentsOfFile: "apple/Persona/Views/Chat/ChatView.swift", encoding: .utf8)
                let footer = String(source.components(separatedBy: "private var mainFooter: some View {")[1].components(separatedBy: "private var pendingRecap:")[0])
                let panel = footer.range(of: "ForEach(model.interactions(for: key)) { ix in InteractionPanel(interaction: ix) }")!
                precondition(panel.lowerBound < footer.range(of: "Composer(key:")!.lowerBound)
                let linesBody = source.components(separatedBy: "private func chatLines(")[1].components(separatedBy: "    #if DEBUG")[0]
                precondition(!linesBody.contains("InteractionPanel"))
                let input = c["input"] as! [String: Any]
                actual["footer"] = ["interaction_ids": input["pending_interaction_ids"]!, "placement": "above_composer", "part_of_transcript": false]
            }
            let a = try JSONSerialization.data(withJSONObject: actual, options: [.sortedKeys])
            let e = try JSONSerialization.data(withJSONObject: c["expected"]!, options: [.sortedKeys])
            guard a == e else {
                FileHandle.standardError.write(Data("不匹配：\(c["name"]!)\n".utf8))
                exit(1)
            }
        }
        print("通过：\(cases.count) 个用例")
    }
}
```

## 新增用例

先定位 iOS 函数或现有测试，确定要固定的分支，再往对应主题文件追加用例；新主题可以增加 JSON 文件，保持同一结构。输入只用编造内容，图片、任务和调用标识也如此。

用例名在本领域内唯一；直接移植测试保留 `from_test`，新边界写 `derived: true` 并注明真实函数。先依据原断言或实现填写预期，再运行上面的对照，不要为了让测试通过而自动覆盖整份 `expected`。涉及多次到达的行为用 `operations` 保存每一步快照，展开行为同时检查标题、原行和新增步骤的 ID。

修改后补充规则表，重跑 Swift 对照、JSON 结构与唯一性检查，再执行仓库隐私扫描。需要未来新行为时另定规格版本，保留当前 iOS 差异说明。
