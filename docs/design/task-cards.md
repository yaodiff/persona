# 任务卡：iOS 与 Web 的对比和统一设计稿

状态：P1–P7 已定（2026-11-12，全部按推荐，见 §5），按本稿实现（#5569）。Web 一侧已实现，逐项状态见 §7；iOS 一侧见 §7 的 iOS 行。
对比基线：`origin/main` @ `d6b446fcea`（2026-11-12）。截图在 [`task-cards/`](task-cards/)，数据来自 `scripts/mock/mock_hub.py`
（`MOCK_TASKS=1 MOCK_TASKS_CUT=1 MOCK_USAGE_LIMIT=1 MOCK_TODO_ASKS=1 MOCK_TASK_DECISION=1`），iOS 是 iPhone 17 Pro 模拟器（iOS 26.0，英文），
Web 是本地 vite 页面，视口 430 px 宽（手机宽度，和 iPhone 可比），深浅色各一张。

## 0. 先更正：两端「任务卡」现在画在哪

上一轮对比把 `TaskCard`、`ReceiptCard` 当成聊天里的主要卡片。这一轮读调用点后发现并不是（下表是 #5569 之前的现状；Web 的结论卡已恢复，死代码已删，见 §7）：

| 要看的东西 | Web 现在画在哪 | iOS 现在画在哪 |
| --- | --- | --- |
| 计划卡（待开始） | **Needs you 托盘**里的 `PlanCard`（`NeedsYouRow`）；聊天里 `simplified` 时 open 的计划不画 | Needs You 托盘（`AssistantActionBar`）和聊天（`FeedLine` → `PlanCard`） |
| 已开始的任务 / 计划 | 聊天里一张「交接卡」（`TaskHandoffs` / `PlanHandoffs`：箭头图标、标题、NEW、时间、状态，点开去 Agent 页） | 聊天里 `DispatchedTaskLinks`（`TaskOpenButton`，「Dispatched: 标题」） |
| 执行中进度 | **In progress 托盘**的行 | In Progress 托盘的行 |
| 结论（做完了 / 停了 / 失败） | 聊天里**不画**，只画「被模型服务打断」的那种（`interruptLook`）；其余靠交接卡的状态字（#5569 起四态都画，见 §3.3） | 聊天里 `ReceiptCard`，四种：done / stopped / failed / interrupted |
| 要你拍板 | Needs you 托盘的 `TaskDecision`，早报卡的 `AcceptRow` | 托盘的 `BriefAcceptRow`（早报卡同一个组件） |

`web/src/views/assistant/Tasks.tsx` 里的 `TaskCard`、`TaskLine`，和 iOS `TaskViews.swift` 里的 `TaskCard`、`TaskLine`
在生产路径上没有调用点（Web 要 `FeedLineView` 不带 `simplified` 才走到，`ChatView` 恒带；iOS 全仓没有 `TaskCard(` 的实例化），
只有测试在用。上一轮清单里「失败卡缺 Retry」「Web 的计划进度行 / 孤儿行」等条目比较的是这些**死代码**，本稿不再把它们当作差异；
P7 已定，Web 一侧已删（§7）。**更正**：`OutcomeReceipt`、`ReceiptCard`、`InterruptedReceipt` 不是死代码：`ReceiptCard` 一直是活的，
只是 `simplified` 时对非打断的回执直接返回空；#5569 起它画四态。

下面逐项对比的是**线上真正可见的面**。

## 1. 已查实的上一轮「需要核对」

| 项 | 结果 |
| --- | --- |
| 目录 chip | 两端一致：都只显示目录最后一段（Web `cwd.split("/").at(-1)`，iOS `task.project`），截图里都是 `weather`（`web-plan-open-*`、`ios-plan-open-*`） |
| 权限 chip 是否常显 | **两端都常显**（截图里都是「Can edit files」）。上一轮「iOS 只在只读时显示」说错了；只读时两端都把它换成「Read-only」。不再是差异 |
| Web 失败文案 | 「Didn't start: <原因>」，红色，在交接卡下面（`web-handoff-failed-*`） |
| Web 绿 / 红 / 橙取值 | 见 §4 token 表。注意 Web 浅色的 `--red-tint` 是 `#f7f8f9`，几乎是中性灰，失败结论卡在浅色下看不出红 |
| iOS 有没有 Later / Don't handle（Web 叫「Dismiss」的那个，实际用词是 Don't handle） | 任务拍板行没有：`apple/Persona/Views/Assistant/Tasks` 和 `TasksStore` 里没有对应的写接口调用；Web 的 `TaskDecision` 有 Later（只在本浏览器会话里推迟）和 Dismiss（落库，可撤销）。「Actions」菜单里的项我没有逐个展开看 |

## 2. 差异、取舍和理由

判断标准（顺序不代表优先级）：**单手**（拇指够得到、点击区够大、不用先展开再点）、**信息密度**、**两端一致**、**可发现性**（看得出能点、看得出下一步）。
不默认哪一端为准。

### D1 进度行：状态字的位置

- iOS：标题左，状态字**右对齐成一列**，灰色；第二行是在做什么。
- Web（#5569 之前）：状态字紧跟标题（同一行、小灰字），第二行是在做什么；行尾的图标不是统一的 `›`：已完成是绿勾，
  needs_you / 打断 / 用量限制 / 失败（`blocked`）是橙色 ⓘ，其余才是 `›`。
- **选 iOS 的右对齐状态列，加上 Web 的行尾 `›`。** 行尾槽位固定宽：能点开的行一律 `›`，已完成是勾，只有用量限制是橙色 ⓘ
  （needs_you / 打断 / 失败改成 `›`，不再是 ⓘ）。状态字颜色见 §3.2。
- 理由：状态是扫一眼要比较的东西，右对齐成一列比跟在不等长的标题后面好扫（密度、单手扫读）；iOS 的行没有任何「可点」提示，Web 的 `›` 让整行可点这件事一眼可见（可发现性）。Web 状态字跟标题走的写法在长标题被截断时会把状态挤没，右列不会。

### D2 状态词

- iOS：Starting / Working / Usage Limit / Unread / Needs You / Interrupted · N agents（首字母大写）。
- Web：Not started / Working / Usage limit / Unread（句首大写）。
- **选 iOS 的 `Starting`；大小写各用平台惯例（iOS Title Case，Web 句首大写），不强求一致。**
- 理由：这个状态是「马上要开始」，说 `Not started` 会让人以为卡住了；任务真的没起来是另一个状态（`Didn't start: <原因>`）。大小写是平台习惯（HIG 对按钮 / 标签用 Title Case），统一反而别扭。

### D3 用量限制的颜色和信息

- iOS：灰点 + 「Usage Limit」，没有第二行原因，没有重置时间。
- Web（#5569 之前）：橙色时钟 + 「Usage limit」；托盘行的第二行就是 agent 最后一句话（碰巧是「You've hit your usage limit.」），
  **没有**重置时间（重置时间只在 To-dos 页、任务板、`TaskDock` 的提示里有），行尾橙色 ⓘ。
- **选 Web。** 用 `caution` 角色色（iOS `Theme.caution`，Web `--orange`）画时钟，第二行写原因和重置时间。
- 理由：用量限制是用户可以做点什么（等、换账号、继续）的状态，灰点读起来像「已结束」；原因和重置时间是用户决定要不要等的唯一依据。

### D4 Needs you 托盘：紧凑行 vs 内联展开

- iOS：进度行是**紧凑行**（标题 + 状态 + 一两行摘要）；计划卡、消息卡、简报行在托盘里是**展开的**，拍板行放不下按钮时收进「Actions」菜单；
  托盘窗口很矮（约 280 pt），一屏看 2–3 行。
- Web：有些行直接展开成可答的卡（计划批准、选项题、Bash 批准都在原地，按钮在卡底），一张卡就超过一屏；另一些（没有问题的 Agent、
  只是在等的任务、失败行）是紧凑的一行。两端都不是统一的紧凑或展开。
- **不选其一，做成「折叠行 + 一键展开原地答」**：默认紧凑行（iOS 的密度），点行或行里的主按钮就在原地展开成 Web 那样的可答卡，答完收回。
- 理由：单手场景下，要答的东西一多，Web 的全展开会把后面的卡挤出屏、要一直滚；iOS 的紧凑行答一道题要多一次点击和一次页面跳转。折叠 + 原地展开两头都保住。这条有产品含义，见 P2。

### D5 计划卡（待开始）

两端结构几乎一样：小标题（图标 + 「Start like this」）→ 任务标题（多个任务带编号）→ chip 行（机器 / 目录 / 模型 / 权限）→ why → 「Start these directly」→ 按钮行。差异：

| 点 | iOS | Web | 选 |
| --- | --- | --- | --- |
| 按钮行位置 | 跟在内容后面，长了会滚出屏（iOS 补） | `sticky bottom-0`，卡比托盘高时按钮仍贴托盘底（#3822） | **Web**：单手最怕按钮被滚走 |
| 按钮宽度 | 已经是 Start 撑满（`maxWidth: .infinity`），另两个按内容 | Start 撑满，另两个按内容 | 一致，不用改。真正的差距是下面的吸底和 brief |
| 「直接开始」 | 圆形勾选 +「Start These Directly」 | 方形 checkbox +「Start these right away from now on」 | **控件用各平台原生的；文案用 Web 的**：「from now on」说清了这是一条持续生效的偏好，不是只对这次 |
| 标题字号 | `bodyStrong`（17 semibold） | 14.5 px semibold | 各用自己的，见 §4（Web 整体字号比 iOS 小一档，是平台差异不是错误） |
| brief / 工作区 | 没有 | 卡里有 `task.brief` 折叠和工作区编辑 | **Web**：开始前能看到任务被什么标准验收，iOS 补 |

### D6 聊天里的已开始任务（交接卡）

- iOS：一个按钮样式的行，「Dispatched: <标题>」，多个任务时先写「Dispatched N」再列标题。
- Web：卡片：箭头图标、标题、NEW 标、时间、**状态**（Working / Unread…），右上 ↗；失败时下面一行红字原因。
- **选 Web 的卡片形态**（带状态和时间）。
- 理由：派出去之后用户最想知道的是「现在怎么样了」，iOS 这行只有标题，要点开才知道；Web 卡片在聊天里就能看到状态变化。（iOS 的截图：`ios-handoff-receipt-*`，每张里都有「Dispatched: 标题」那一行，它是一条灰底的整行按钮，只有标题和「Open ›」，没有状态也没有时间。）

### D7 结论卡：画不画、画成什么样

- iOS（截图 `ios-handoff-receipt-{done,stopped,failed,interrupted}-*`）：`ReceiptCard`：左图标（✓ 绿 / 时钟灰 / ✕ 红）+ 标题（「X is done / stopped, not done yet / didn't start / was interrupted」）+ 摘要最多 4 行（Markdown 链接）+ 「PR」「View」两个按钮；底色是状态色 12% 透明，stopped 用灰。
- Web（`OutcomeReceipt`，活的，只是聊天里 `simplified` 时被短路）：同样结构，另有「Show all」展开、Copy / Quote、验收提示行；
  #5569 之前线上只画被打断的批次卡。
- **选：两端都画结论卡，结构取 iOS 的，加上 Web 的「Show all」和 Copy / Quote。** 对 Web 是恢复，对 iOS 是补两个小功能。
- 理由：结论里有 Agent 最后一段话和 PR，是任务的交付物；只靠交接卡的状态字，用户在聊天里看不到「做完了什么」，要去 Agent 页翻。摘要 4 行截断但无法展开是 iOS 现在的缺口。Web 是否有意去掉结论卡见 P3。

四种结论态的统一规格见 §3.3。

### D8 要你拍板：行内布局

- iOS：`BriefAcceptRow` 用 `ViewThatFits`：按钮放得下就一行，放不下就收成「Actions」菜单；文字占整行宽。
- Web：`AcceptRow` 文字在左、按钮在右同一行；430 px 宽下文字被挤成约 4 个字一行（`web-brief-to-verify-*`，「选缓存方案」那一条）。
- **选 iOS 的 `ViewThatFits`；窄屏下 Web 把按钮换到文字下面（堆叠），而不是收菜单。**
- 理由：文字被挤成一窄列是明显缺陷，可读性第一。收进菜单会让主操作（Accept / Take recommendation）多一次点击，单手场景下不如把按钮堆到下面、主按钮满宽。iOS 也建议同改：放不下时堆叠，而不是菜单。

### D9 要你拍板：Later / Don't handle

- iOS：没有（拍板行的 Later / 不处理都没有；回复草稿有一个「Do not handle」）。
- Web：**Later**（只在这个浏览器会话里推迟）+ **Don't handle**（落库、可撤销，`assistant-tasks.dismiss_draft`；中文「稍后 / 不处理」，日文「あとで / 対応しない」），吸底。
- **统一用词是 Later / Don't handle**（Web 本来就这样，不是「Dismiss」）：**Don't handle** 落库、可撤销（hub `POST /todos/{id}/decision-dismiss`
  和 `decision-undo-dismiss`），**Later** 只在这次 App 会话里推迟，按拍板轮次计；iOS 补，回复草稿的「Do not handle」改成「Don't handle」。
- 理由：拍板项不想现在处理时，不能只有「答」一条路，否则它永远占着 Needs you 的计数（见 #3804）。可撤销的「不处理」比确认对话框轻。

### D10 颜色：失败和中断的底色

- iOS：失败 / 中断卡底色 = `danger` 12% 透明，深浅色都是红底。
- Web：`--red-tint`，浅色 `#f7f8f9`（几乎灰），深色 `#1f2022`（几乎黑）；红只在图标和文字上。`--red-tint` 是**有意**中性的
  （`styles.css` 的注释，`theme-contrast.test.ts` 和 `extension-theme.test.ts` 钉着它等于 `--inset`，约 45 个文件在用）。
- **选 iOS：底色用 `danger` 的 12%。** 落地时**不改 `--red-tint`**，加一个新 token `--danger-wash`（`--red` 的 12% 混色），只给失败 / 中断结论卡和
  Needs you 里的失败行用（见 §4.4 和 §7）。
- 理由：失败是要用户注意的状态，浅色下和普通灰卡片分不出来是缺陷；两端同一个语义色按同一个比例混合，也方便做 token 表。
  对比度（实测，WCAG）：12% 红叠在浅色画布上，`--red` 4.31、`--ink-2` 4.37、`--ink-3` 4.12，都不到 4.5；叠在浅色 surface 上 4.78 / 4.85 / 4.57。
  所以失败 / 中断卡和托盘里失败行上的**字一律用 `--ink`**（画布和 surface 上都在 11:1 以上，`theme-contrast.test.ts` 钉着）；`--red` 和 `--accent` 在浅色画布上都不到 4.5（`--accent` 在 12% 红底上只有 2.7–3.0），只给图标用，Retry / Remove 也写成 `--ink`。

### D11 圆角、间距、字号

- iOS 卡圆角 14、控件 10、气泡 20；Web 卡圆角 10、控件 8。
- **各平台保留自己的圆角**，但在 token 表里记成「同一个角色」，不要求数值相等。iOS 的大圆角是系统风格，Web 用 14 会在桌面密度下显得松。
- 内边距：两端卡片都是 16（iOS `Space.l`，Web `p-4`），gap 8 / 12 对应 `Space.s` / `Space.m`，已经一致。
- 字号：Web 写死的 `text-[14.5px] / [13px] / [12px] / [12.5px]` 要收成命名 token（§4），否则不能检查一致性；`12.5px` 这一档在 iOS 没有对应，并入 12 或 13。

## 3. 融合后的卡片结构

### 3.1 计划卡（待开始）

```
┌──────────────────────────────────────────┐
│ ☰ Start like this            (label,caption strong, secondary)
│ ① 任务标题                    (heading / 14.5 semibold)
│   [机器] [目录] [模型] [权限]  (chip 一行，可换行；只读时权限换成「Read-only」accent)
│   为什么这样安排              (caption, secondary)
│   ▸ 验收标准 brief            (折叠，Web 已有，iOS 补)
│ ② …（多任务：编号 + 分隔线）
│ ☐ Start these directly from now on
├──────────────────────────────────────────┤  ← 吸底
│ [   Start / Start N tasks   ] [Edit] [Don't start]
└──────────────────────────────────────────┘
```

字段顺序以 Web 为准（多出 brief 和工作区），按钮行吸底、主按钮撑满。已开始 / 已放弃后收成一行小字（「Started N tasks」/「Not started · N tasks」），失败的任务另起红色行。

### 3.2 执行中的进度行（托盘）和交接卡

```
进度行：  [状态标记]  标题                              状态字（右列）  ›
                      第二行：在做什么 / 原因 / 重置时间
交接卡：  [↳]  标题  [NEW]                                        ↗
          🕐 11 min · 状态字
          （失败：Didn't start: <原因>，红色）
```

| 状态 | 标记 | 状态字 / 颜色 | 第二行 |
| --- | --- | --- | --- |
| Working | 转圈（`ai`） | Working，secondary | 在做什么，一行；后台任务数 |
| Starting | 空心圈 + `caution` 点 | Starting，secondary | PR 号 / 「Starting the agent」 |
| Needs you | `caution` 实心点 | Needs you，`attention` / `cautionText` | 它在等什么（「Waiting for your answer」或问题本身）；行外圈 1.5 pt `danger` 描边仅在卡片形态下 |
| Usage limit | 时钟，`caution` | Usage limit，`caution` | 原因 + 重置时间；行尾 ⓘ |
| Interrupted | ⓘ，`danger` | Interrupted，`danger` | 打断原因 |
| Failed to start | ⓘ，`danger` | Didn't start，`danger` | 原因 |
| Unread / Done | ✓，`positive` / secondary | Unread / Done | 最后一句话（一行） |

### 3.3 结论卡（四态）

```
┌──────────────────────────────────────────┐
│ [图标]  标题（done / stopped / didn't start / interrupted）
│         摘要（Markdown，4 行；放不下出现「Show all」）
│         [PR #N]  [View]      （失败时：[Retry]；PR 在 App 内链接卡片里打开）
│         （复核中时：灰字一行「Waiting for review · N criteria」）
└──────────────────────────────────────────┘
```

| 态 | 图标 | 底色 | 按钮 |
| --- | --- | --- | --- |
| Done | ✓ 实心，`positive` | `positive` 12% | PR、View |
| Stopped（没做完） | 时钟，secondary | `Fill.well` / `--field` | PR、View |
| Failed to start | ✕ 实心，`danger` | `danger` 12%（Web `--danger-wash`） | Retry |
| Interrupted | ✕ 实心，`danger` | `danger` 12%（Web `--danger-wash`） | Continue、View |

### 3.4 要你拍板

```
标题                                  （secondaryMedium / 14 medium）
结论一句（verdict）                    （caption）
Recommended: …                         （caption secondary）
Waited 2 h · 来自 Agent「…」
窄屏：  [ Open ]  [ Later ]  [ Don't handle ]
        [      Take recommendation (满宽，主按钮)      ]
宽屏：  文字在左，按钮在右（和现在一致）
```

## 4. 双端共用 token 表

同一个角色，两端各用自己平台的值；表的目的是**每个角色只有一个名字**，实现时在 Web 的 `styles.css` 加 `--role-*` / `text-*` 别名，在 iOS 的 `Theme` 里已经有的直接用。

### 4.1 字号

| 角色 | iOS `Theme.Font` | iOS 默认 pt | Web 现状 | Web 建议类 |
| --- | --- | --- | --- | --- |
| 卡片标题 | `bodyStrong`（body semibold） | 17 | `text-[14.5px] font-semibold` | `text-card-title`（14.5） |
| 正文 / 摘要 | `body` / `secondary` | 17 / 15 | `--chat-body` 15 / `--chat-secondary` 14 | 沿用 |
| 行标题 | `secondaryMedium` | 15 medium | `text-[14.5px] font-semibold` | `text-row-title` |
| 元信息（时间、地点） | `caption` | 13 | `text-[12px]` / `[12.5px]` / `[13px]` 混用 | `text-meta`（13）；12 / 12.5 并入 |
| chip / 标签 / 计数 | `label` | 12 | `text-[12px]` | `text-label`（12） |
| Secondary meta (the Inbox event page's state lines, times and section labels) | `label` / `caption` | 12 / 13 | `text-[11.5px]` | `text-submeta` (11.5; font size only, no line height) |
| 强调的小字（按钮式链接） | `captionStrong` | 13 semibold | `font-semibold text-[12.5px]` | `text-meta font-semibold` |
| 代码 / 路径 | `code` | 13 | `font-mono` | 沿用 |

写死的 px 字号（`text-[13px]` 等，约 3400 处）不立刻改，但只许减不许增：`scripts/ci/web_font_ratchet.py`（基线
`scripts/ci/web_font_baseline.json`，接在 `scripts/ci/guards.sh`，说明在 `docs/operations/testing.md`）。上表的 `text-card-title` 等别名
还没加进 `styles.css`，现在落地的只有这条检查；加别名和逐步替换是后续的事。

### 4.2 间距

| 角色 | iOS `Theme.Space` | pt | Web |
| --- | --- | --- | --- |
| 紧凑 | `xs` / `sm` | 4 / 6 | `gap-1` / `gap-1.5` |
| 行内 | `s` | 8 | `gap-2` |
| 卡内分组 | `m` | 12 | `gap-3` |
| 卡内边距、页边 | `l` / `Space.page` | 16 | `p-4` / `px-4` |
| 区块之间 | `xl` | 24 | `gap-6` |

### 4.3 圆角

| 角色 | iOS `Theme.Radius` | Web |
| --- | --- | --- |
| badge / chip（胶囊） | `badge` 6 / capsule | `rounded-full` |
| 控件、行内小块 | `control` 10 | `--radius-control` 8 |
| 卡片 | `card` 14 | `--radius-card` 10 |
| 气泡 | `bubble` 20 | `rounded-[12px]`（用户气泡）、`[18px]`（Web 的路由气泡） |

### 4.4 角色色（浅色 / 深色）

| 角色 | iOS | iOS 值 | Web 变量 | Web 值 |
| --- | --- | --- | --- | --- |
| 强调（可点） | `Theme.accent` | 系统蓝 | `--accent` | `#0285ff` / `#3d9aff` |
| 成功 | `Theme.positive` | `#248A3D` / `#30D158` | `--green` | `#107638` / `#6dd697` |
| 危险 | `Theme.danger` | `#D93A3A` / `#FF5F57` | `--red` | `#bd3038` / `#ff9499` |
| 注意（点、图标） | `Theme.caution` | `#F08A00` / `#FF9F0A` | `--orange` | `#a64c00` / `#eea66d` |
| 注意（文字） | `Theme.cautionText` | `#B35400` / `#FF9F0A` | `--attention` | `#8f5c00` / `#f1b969` |
| AI / 工作中 | `Theme.ai` | `#5B4FD6` / `#9C94FF` | `--ai` | `#5b4fd6` / `#9c94ff`（**两端相同**） |
| 次要文字 | `.secondary` | 系统 | `--ink-2` | `#62656b` / `#a5a8ad` |
| 第三级文字 | `.tertiary` | 系统 | `--ink-3` | `#66696f` / `#979aa0` |
| 凹陷底（行、输入） | `Theme.Fill.well` | 系统 quaternary | `--field` | `#f2f2f3` / `#2b2c2f` |
| 成功底 | `positive` 12% | — | `--green-tint` | `#e8f5ed` / `#3dbb7224` |
| 危险底 | `danger` 12% | — | `--danger-wash` = `color-mix(in srgb, var(--red) 12%, transparent)`（#5569）；`--red-tint` 不动，仍是 `#f7f8f9` / `#1f2022` | 见 D10 |

两端的绿、红、橙数值并不相同（只有 AI 紫相同），这是各自为了对比度调的，不要求逐值一致；一致的是**角色**和**用在哪**。
Web 的 `--orange` 浅色 `#a64c00` 比 iOS 的 `caution` 暗很多，更接近 `cautionText`；Web 没有对应 `Theme.caution`（图标用的亮橙）的变量，建议补一个 `--caution`。

### 4.5 动画

沿用：iOS `Theme.Motion.state`（0.25 s）、`layout`（0.3 s）；Web 的 `duration-150/200` 和 `prefers-reduced-motion` 已经有 `motion-reduce`，不改。

## 5. 拍板点（已定）

2026-11-12 用户拍板：P1–P7 全部按下面的推荐执行。每条后面写了落地时的具体做法。

- **P1 拍板行：窄屏堆叠还是收菜单（D8）。** 推荐：堆叠，主按钮满宽。依据：菜单让主操作多一次点击。
  **已定：堆叠，主按钮满宽；两端都做，iOS 去掉「Actions」菜单收纳。** 窄屏时文字占满宽，次要按钮一行（Open、Finish now、Later、Don't handle，放不下就换行），主按钮在它们下面独占一行满宽（§3.4）；宽屏不变。Web 的 Later / Don't handle 通过 `AcceptRow` 的 `secondary` 插进这一行。
- **P2 Needs you 托盘形态（D4）：紧凑行、内联展开、折叠行 + 原地展开。** 推荐：折叠行 + 原地展开。依据：单手滚动成本和答一道题的点击数两头都不吃亏。代价：要在 iOS 托盘里做可展开的行，工作量最大的一项。
  **已定：折叠行 + 原地展开，两端统一。** 默认全部折叠（只有一行时也不自动展开）；点行或行尾的 chevron 在原地展开成完整可答的卡；答完后折叠 / 消失。展开状态的键带着这一轮的标识（任务的 review / ask 时间、Agent 的问题 id），同一个任务再问一次时回到折叠；展开后卡里不再重复标题（行头就是标题）。
  对话自己的问题 / 权限卡（已经在聊天底部）的行保持原来的「关掉托盘并滚到那张卡」。
- **P3 Web 是否恢复聊天里的结论卡（D7）。** 推荐：恢复，结构取 iOS，加「Show all」。依据：结论是交付物，状态字不替代它。若当时去掉是有意的（避免和交接卡重复），则改成「交接卡在做完后展开成结论」，同样要把摘要带出来。
  **已定：Web 恢复聊天里的结论卡（done / stopped / failed / interrupted 四态），结构取 iOS，加「Show all」和 Copy / Quote；iOS 也补「Show all」和 Copy / Quote。**
- **P4 Later / Don't handle 的语义（D9）。** 推荐：「不处理」落库可撤销，Later 只推迟当前会话；iOS 补。需要确认 iOS 的「Do not handle」（回复草稿用）和这里是不是同一个概念，要不要统一成一个词。
  **已定：「不处理」落库可撤销，Later 只推迟这次 App 会话；iOS 补上。统一的词是 Later / Don't handle（稍后 / 不处理，あとで / 対応しない），
  iOS 回复草稿的「Do not handle」改成「Don't handle」。**（可逆：若要字面的「Dismiss」，要改两端界面、`docs/assistant/todos.md`、`protocol.md`、
  本文件和 `DESIGN_SYSTEM.md`、e2e 脚本。）
- **P5 失败底色（D10）。** 推荐：Web 的 `--red-tint` 重新取值。这是改已有界面的样子，按仓库约定属于「按用户明确要求改」，需要你确认才算。
  **已定：底色用 `danger` 12% 混色，和 iOS 一致；落地为新 token `--danger-wash`，`--red-tint` 不动**（它是有意中性的，被两个测试钉着，
  约 45 个文件在用；12% 红叠在浅色画布上文字对比度不到 4.5，聊天里的失败 / 中断卡因此用 `--ink` 写字，见 D10）。可逆。
- **P6 token 表是否进仓库作为双端约束。** 推荐：先放在文档里，Web 侧加 `scripts` 检查写死的 px 字号只能减少（仿 iOS 的 `TokenRatchetTests`），不立刻改页面。
  **已定：token 表先放文档；Web 加写死 px 字号只减不增的检查（baseline / ratchet）。**
- **P7 清理死代码。** Web 的 `TaskCard` / `OutcomeReceipt` / `TaskLine` 和 iOS 的 `TaskCard` / `TaskLine` 要不要删，还是 P3 恢复时复用。推荐：P3 定了再决定；定之前不删。
  **已定：P3 落地后，删掉两端确实不再使用的旧卡片组件（如 iOS 未实例化的 `TaskCard`）。**

### 5.1 iOS 落地状态（#5569，PR-B）

| 项 | iOS 状态 |
| --- | --- |
| P1 拍板行窄屏堆叠 | 已做：`BriefAcceptRow` 去掉「Actions」菜单，次要按钮一排（放不下就换行），主按钮独占下一行满宽（主按钮在下，跟 §3.4 的图；DESIGN_SYSTEM 3(c) 写的是主按钮在上，这里以设计稿为准） |
| P2 Needs you 折叠行 + 原地展开 | 已做：计划、回复草稿、有未答 ask 的任务三类行默认折叠，点行或箭头原地展开成完整卡；不自动展开单独的一行；会话自己的提问行仍然是「关托盘去看底部卡」。展开的卡留在视图树里（折成 0 高度），计划卡的勾选和草稿的编辑不丢 |
| P3 / D7 结论卡 | 已做：Show All / Show Less、Copy / Quote（⋯ 菜单）、失败 Retry、中断 Continue（用量限制会自己续的不给 Continue） |
| P4 一对词 | 已做：Later / Don't handle（稍后 / 不处理，あとで / 対応しない）；拍板行的 Later 只在本次 App 运行内，Don't handle 落库、可撤销（6 秒 Undo） |
| §3.1 计划卡 | 已做：Start 满宽、放不下时 Edit / Don't Start 换到下一行；brief 折叠显示；托盘里按钮钉在列表下方；「Start these directly from now on」 |
| §3.2 进度行 / D3 | 已做：状态词右列带色、行尾 ›、第二行（用量限制 = 原因 + 重置时间，橙色时钟）、Starting 空心圈 + caution 点 |
| D6 交接卡 | 已做：`DispatchHandoffCard`，**一个任务一张卡**（Web 是每个 Agent 一张，120 秒内合并）；这是可接受的差别 |
| P7 清理 | 已做：`TaskCard`、`TaskLine`、`TaskNowLine`、`TaskSentList`、`TaskProgressLine`、`TaskOpenButton` 已删；`SentToAgents.takenIn` / `shown` 没动，但 App 里本来就没有调用它们（交接消息一直按普通消息画成 exchange 行），所以这次没有消息消失；要让交接卡列出它们是后续的事 |
| 与 Web 的已知差别 | iOS 的 usage limit 状态不加 10 分钟宽限（共用的 `entries` fixture 不认）；宽限只用在第二行的重置时间和结论卡上 |

## 6. 局限

- iOS 聊天里的交接卡和四种结论卡的截图是在 mock hub 上逐个回答掉待处理的批准 / 选项题之后，滚动聊天流截到的。done / stopped 来自同一条回执，做法是在 mock hub 上把对应待办标成完成（`PATCH /api/v1/todos/{id}` `{"done":true}`）后再截；interrupted / failed 用 `MOCK_TASKS_CUT=1` 的数据。
- iOS 的「Dispatched」行截到的都是已有 mock 数据里的任务，没有单独造「运行中 / 需要你」状态的交接行；这两种状态的进度在托盘截图里。
- Web 的结论卡（`OutcomeReceipt`）在 #5569 之前线上不画，所以对比用的截图里没有它；§3.3 的样子是按源码和 iOS 的实际样子合成的设计。
  落地后的截图在 `task-cards/web-*-after-*`（见 §7）。
- 只看了英文；中文和日文的文案长度没有对比。
- D10 的建议色最初是估的；实现时按 WCAG 算过（见 D10 的数字），`theme-contrast.test.ts` 钉住了 `--danger-wash` 的对比度。

## 截图索引

`task-cards/` 下，每项深浅色各一张（`-light` / `-dark`）：

- `ios-plan-open-*`、`web-plan-open-*`：计划卡（待开始）
- `ios-tray-progress-*`、`web-tray-progress-*`：In progress 托盘（working / starting / usage limit / unread）
- `ios-tray-needs-top-*`、`web-tray-needs-*`：Needs you 托盘
- `web-handoff-working-*`、`web-handoff-failed-*`：Web 聊天里的交接卡（运行中 / 没起来）
- `ios-handoff-receipt-{done,stopped,failed,interrupted}-*`：iOS 聊天里的「Dispatched」行和结论卡的四种状态
- `web-brief-to-verify-*`：早报里的拍板行（看 D8 的窄屏挤压）
- `task-cards/impl/web-<状态>-<before|after>-<light|dark>.png`：#5569 Web 一侧的前后对比（430 px，对着 mock hub）：`tray-needs`、`decision-row`（展开的拍板行）、`failed-row`、`handoff-failed`（交接卡「Didn't start」）、`expanded-ask`、`expanded-plan`、`tray-running`、`receipt-interrupted`

## 7. 实现状态（#5569）

Web 一侧（本 PR）：

| 项 | 状态 | 做法 |
| --- | --- | --- |
| P3 结论卡 | 已实现 | `FeedLineView` 的 `simplified` 分支对所有 `receipt` 画 `ReceiptCard`（done / stopped / failed / interrupted）；`review`、`report` 仍不画。失败卡保留 Retry，Copy / Quote 保留；交接卡在同一任务有失败结论卡时只写「没开起来」，原因在结论卡里（一个 Agent 一张交接卡，不重复原因）。`ReceiptHookOrder.test.tsx` 守住 hook 顺序（#5422） |
| P1 拍板行 | 已实现 | `AcceptRow` 套一层 `@container`；窄于 `md`（28 rem）时文字满宽、次要按钮一行、主按钮满宽独占一行；`stacked` 一直如此 |
| §3.2 / D1 进度行 | 已实现 | 状态字右对齐成独立一列（`max-w-[40%]` 截断），行尾固定宽槽位：`›` / 绿勾（已完成）/ 橙色 ⓘ（只有用量限制）；颜色：Needs you `attention`、用量限制 `orange`、打断和失败 `red`。`data-task-slot`、`data-task-mark`、`data-running-agent` 和标题的 `font-semibold` 不变，新增 `data-task-status` |
| D2 Starting | 已实现 | 托盘行里 `not_started` 写 `assistant-tasks.status_starting`（Starting / 启动中 / 起動中）；`lib-state-labels.not_started` 不动（To-dos 页、任务板还用它） |
| D3 用量限制第二行 | 已实现 | 用量限制行的第二行是 `usageLimitLabel`（「Usage limit · resets 14:20」，已知重置时间时）；没有重置时间时是 `lib-usage-limit.short` / `resumes_later`。每 30 秒跟着宽限时间刷新。聊天里的用量限制卡本来就有 |
| P2 折叠行 | 已实现 | `AssistantActionBar` 的 `openRows`（不叫 `expanded`，那是整个托盘铺满，#4656）；`NeedsYouRow` 对计划、消息、有问题的 Agent / 任务、拍板、验收折叠成 `button[aria-expanded]`（标记、标题、在等什么、种类、chevron），展开后外层 `div[data-needs-row][data-needs-open]` 里是标题按钮（再点折叠）和原来的完整卡。`data-needs-row` 在两种状态下都在最外层。没有问题的 Agent、只是在等的任务、失败行本来就是一行，不变；对话自己的卡（`OwnAskRow`）不变。换托盘时全部折叠；答完的卡从 Needs you 消失，展开状态的键是「行键 @ 这一轮」，留在集合里直到换托盘：撤销「不处理」后行回来仍是展开的，同一个任务再问一次（新一轮）回到折叠。展开后卡里的任务标题不再是单独的「打开任务」按钮（标题行改成折叠开关） |
| P4 用词 | Web 无需改 | Web 本来就是 Later / Don't handle；本文 D9 / P4 已改正 |
| P5 `--danger-wash` | 已实现 | 新 token，失败 / 中断结论卡、待处理的中断批次卡、Needs you 里的失败行用；`--red-tint` 不动。对比度见 D10，`theme-contrast.test.ts` 钉住 |
| P6 px 字号棘轮 | 已实现 | `scripts/ci/web_font_ratchet.py` + `web_font_baseline.json`（3423 处，314 个文件）+ `test_web_font_ratchet.py`，接在 `guards.sh`；只能减不能增，`--update` 只降；拆分或搬动文件时，没有记录的新文件可以拿走别的文件在同一次改动里少掉的数（总数不会升） |
| P7 死代码 | Web 已删 | `TaskCard`、`useTaskCard`、`TaskLine`、`TaskSentList`、`useTaskUltracode`、`TaskTitle`、`useTaskMenu`、`taskCardDrawn`、`useTaskCardShown`、`FeedLineView` 的 `task` / `plan` 非简化分支，`receipt_failed_above` 等 6 个只有它们用的 i18n 键，和只测这些的用例。保留：`takenIn`（`lib/feed.ts`，隐藏交接消息的规则，原来是 `TaskCard` 列出它们；现在没有地方列，留待后续决定）、`ReceiptSummary`、`Clamped` |
| 文档 | 已改 | 本文 §0、D1–D5、D7、D9、D10、P1–P7、§4.1、§4.4、§6；`DESIGN_SYSTEM.md` 的 `--danger-wash` 行和 4.8.0；`docs/operations/testing.md` |

iOS 一侧：见 iOS 的 PR。说明：iOS 聊天里的交接卡一个条目一张（Web 是一个 Agent 一张，120 秒内合并），形态一致，这个差别可以接受。

没做的：`WaitingRow`（`Brief.tsx`）有同样的「打开」链接挤压，不在 P1 范围；`text-card-title` 等字号别名；展开后给「打开任务」留入口。
