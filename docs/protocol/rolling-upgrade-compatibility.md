# 滚动升级双向兼容清单（#6223）

> 实现与本地隔离验证清单。基线为 `39a706a338`；改名提交为 `5969be6293`。验收范围为服务端、脚本、网页及 Android 协议／逻辑；原生系统升级边界另由 #6152／#6192 验证。本清单不授权部署、上传或恢复发布循环。

## 协商规则（各端共同遵循）

- 新 hub 在认证后的 `hello.features` 与受保护的 health 能力中报告 `persona_contracts_v1`；app 可在 WebSocket URL 的 `features` 或 `client_hello.features` 中报告同名能力。
- **没有能力就是旧端**，不从 build、时间、产品显示名或未知版本字符串猜测。协议版本本身不因品牌变更提高。
- 新端必须双读改名前和新标识。对没有能力的 hub，发改名前的字段／XML 标签／名称；HTTP 请求头可同时发送两套相同值。
- 新 hub 双读旧／新请求头与数据标识；会话的布尔来源字段同时输出两个键（包括 false），读取时新键存在则优先，不能用布尔 OR 代替 presence。
- 签名、TLS pin、鉴权、权限、manager 所有权、操作去重不因兼容而放宽。副作用请求不以失败重试另一名字。旧／新幂等请求头冲突应拒绝，不能重复执行。
- 推送加密信封没有域版本标记：派生上下文保持改名前的字节，新解密器也接受短暂改名期间已经产生的新上下文；不重建用户密钥。push-to-start 的属性类型另按持久化 `devices.features` 选择，不能只依赖当前 socket 的 hello。
- 兼容字面量集中在各平台的 compatibility 文件；非兼容代码和公开协作文本只使用 Persona。下列旧名是协议数据，不是产品文案。

## 服务端、hub、node、manager

| 标识 | 旧名／字节 | 新名／字节 | 新服务端接旧端 | 新端接旧服务端 |
| --- | --- | --- | --- | --- |
| 能力 | 无 | `persona_contracts_v1` | 缺失时旧默认 | 双读，缺失时发旧默认 |
| admin CSRF／cookie | `X-Jarvis-CSRF`, `jarvis_admin` | `X-Persona-CSRF`, `persona_admin` | 请求头双读；登录 cookie 双发，读新优先，退出清两套 | 新网页双发相同 CSRF，旧 cookie 仍认证；不放宽来源／会话校验 |
| HTTP 客户端与幂等头 | `X-Jarvis-Client`, `X-Jarvis-Op-Id` | `X-Persona-Client`, `X-Persona-Op-Id` | 双读、同值可共存、幂等冲突拒绝 | 双发相同值 |
| hub mesh 转发头 | `X-Jarvis-Peer-{Ttl,Source,Node,From}` | `X-Persona-Peer-{Ttl,Source,Node,From}` | 双读且保留认证／TTL／来源检查 | 双发相同值 |
| mesh node Upgrade | `jarvis-node` | `persona-node` | 接受两者 | 未协商时使用旧 Upgrade |
| 会话创建来源 | `started_by_jarvis` | `started_by_persona` | 双读并双输出 | 双读并双输出 |
| node runtime WS subprotocol | `jarvis-runtime.v1` | `persona-runtime.v1` | 同时接受 | 提供两者，认可任一 |
| MCP namespace / tools | `jarvis`, `plugin_jarvis`, `mcp__jarvis__*`, `jarvis_settings` | `persona`, `plugin_persona`, `mcp__persona__*`, `persona_settings` | Normalize names without changing permission semantics; always read both transcript namespaces | New assistant sessions register only `persona` after the hub announces `persona_contracts_v1`; old or unknown hubs and resumed sessions retain both namespaces (details below) |
| voice delegation | `ask_jarvis` | `ask_persona` | 双读 | 无协商时旧默认 |
| context/file XML | `jarvis-context`, `jarvis-file` | `persona-context`, `persona-file` | 解析两种，不替换任意用户内容 | 无协商时旧默认，双读 |
| launcher 来源 | `jarvis` | `persona` | managed launcher 的 typed DTO 双读来源枚举；不修改无关的 `herdr_focus.source` | managed launcher 输出稳定旧枚举；新 reader 精确归一 |
| 回复去重 seed | `jarvis:reply:<kind>:<clientId>` | 曾改为 `persona:reply:…` | 恢复旧 seed，重试命中同一 receipt | 两端计算同一 UUID，不重复发送 |
| Codex thread UUID seed | `jarvis/codex/thread/v1\x00` | 曾改为 `persona/codex/thread/v1\x00` | 恢复原始字节 | 同一 thread 始终是同一会话，缓存／权限不分裂 |
| builtin look ID | `jarvis`, `jarvis-{circuit,pixel,facet,blob}` | `persona-visor`, `persona-{circuit,pixel,facet,blob}` | 仅内置 ID 精确双读；保留旧持久化值，加载时归一；退役 ID 仍保留，不让 extension 占用 | 随后合入的退役绘制策略保持：不恢复旧 renderer，显示回退到 Mochi；原始选择／时间戳不丢弃 |
| look manifest fallback | `jarvis`, `jarvis-circuit` | `persona-visor`, `persona-circuit` | SDK 在脱离原始对象的校验投影中接受两种旧 enum，退役绘制时投影到当前 Mochi fallback | 不修改包字节、摘要、签名或任意名称；canonical schema 仍只允许当前绘制，不能借兼容重新启用退役 enum |
| 推送唤醒字段 | `jarvis_wake` | `persona_wake` | 同时输出，接收端按一次推送处理 | 双读 |
| 推送 hub group 路由 | `jarvis_group` | `persona_group` | 保留同一 group 归属与认证，输出旧端可读字段 | Apple 双读，不把同一推送路由两次 |
| 推送私钥 Keychain service | `jarvis.push` | `persona.push` | hub 保留已注册公钥，不触发重注册或生成替代密钥 | #6152 复制原私钥／保护属性并读回验证；新 App 必须仍可解开旧 hub 密文 |
| Watch 录音信封 | `jarvisRecording` | `personaRecording` | Watch／iPhone 双读，包括系统已排队 metadata 与 userInfo；hub API 不改 | #6192 发送旧默认或同一包双键，按 UUID／index 去重 |
| Watch 录音回执 | `jarvisRecordingReceipt` | `personaRecordingReceipt` | 双读系统已排队回执，确认既有传输 | #6192 验证 Watch／iPhone 两种版本组合，不让录音永远 pending |
| HPKE info | `jarvis push v1` | `persona push v1` | 发送旧字节 | 解密接受两套字节 |
| relay grant AAD | `jarvis relay grant v1` | `persona relay grant v1` | 旧授权字节继续可解密 | 不改变既有授权密钥 |
| Live Activity attributes | `JarvisActivityAttributes` | `PersonaActivityAttributes` | 按持久化 device feature 选择；缺失用旧类型，不改 push token | Apple 的注册必须与其真实 nominal type 一致；未声明 canonical 注册类型时不持久化该能力 |
| 深链／配对 | `jarvis://{pair,link,peer,connector,session}` | `persona://…` | 接受两种，token／pin 不变 | 无协商的 QR／安装交接保留旧可读链接 |
| 已发送 assistant 网页短链 | `/j/<conversation>`、`tab=jarvis` | `/persona/<conversation>`、`tab=persona` | 网页保留路由 alias，旧路径不需重新生成链接 | 所有客户端双读，Apple #6192 负责原生 URL reader |
| daemon 默认 HTTP 路径 | `/jarvis/runtime` | `/persona/runtime` | 新 daemon 同时接两者；node 不改配置中的 endpoint | 新 node 发两套 WS subprotocol，endpoint 保持原配置 |
| pi 持久宿主／授权记录 | `jarvis-host`, `jarvis-allow` | `persona-host`, `persona-allow` | 精确类型双读，不重写第三方 transcript 正文 | 旧 reader 可读的写入；保持工具授权范围，不将旧记录丢失当成新授权 |
| pi 同进程接管 slot | `__jarvisPi`, `__jarvisPiHost`, `__jarvisPiAskUser` | `__personaPi`, `__personaPiHost`, `__personaPiAskUser` | reload 接管同一旧 host 并 stop 原实例 | 新旧模块使用同一 slot，不启动第二个宿主 |
| pi reload 命令／配置 | `/jarvis-reload`, `jarvis.json`, `JARVIS_PI_HOST` | `/persona-reload`, `persona.json`, `PERSONA_PI_HOST` | 命令 alias、明确配置优先级、显式空变量优先 | 旧配置继续有效，不扩大权限 |
| mDNS service | `_jarvis-hub._tcp` | `_persona-hub._tcp` | 两套广播，同一 hub 身份 | 两套 browse，按 hub id 去重 |
| 证书备用 DNS | `*.jarvis.invalid` | `*.persona.invalid` | 不替换已有证书和 pin | 接受旧证书身份 |
| runtime 包名 | `jarvis-{dsh,pi,paseo}-runtime` | `persona-…` | 同一包内容接受旧 ID | 查询旧 node 时用旧 ID |

基线的 `/api/v1/…` 入口、普通 WebSocket `type` 与 node／manager RPC 方法不因产品名切换而整体改名；新增业务能力仍按各自协商规则处理。协议版本没有因本兼容层提高。注册 Bundle ID、系统 controls／Intent 等原生身份由 #6152／#6192 记录，本清单不以服务端测试代替其系统升级证据。

### Unified integrations and channel settings

| Field | Mixed-version behavior |
| --- | --- |
| `integrations_v2`, `expected_integrations_v2` | A missing boolean in the selected assistant’s profile keeps legacy Settings and a read-only Labs row. Older clients omit both fields on sparse updates. A supporting manager defaults the flag off and rejects a stale expected value with 409. |
| `thread_mode`, `thread_idle_hours` | The web offers editing on installed extension channels reporting a supported mode. Older clients preserve both values on partial writes. Switching off uses legacy message threads and 30 days of inactivity. Switching back on reuses saved preferences. In split modes older plugins can still supply the opaque `thread` key. |
| `target_override` | Optional detail metadata. Clients can display the reported `topics`, `groups_and_topics`, or `threads` precedence hint. An absent hint needs no placeholder. Older clients ignore it and can still edit the default target. |
| `configuration.*.section` | A manifest property can group a field under `connection`, `channel`, `inbox`, or `advanced`. Omitted or empty keeps the existing form valid. Older clients ignore the returned form hint. Extensions declaring the hint need a matching minimum hub version in `engines.persona` for install-time validation. |
| `integrations_projection_v1` | A connected assistant names it in `manager.hello`. A hub that serves the projection publishes `integrations_projection_v1: true` on that assistant's entry in `GET /api/v1/managers`. With the field absent, use the existing settings reads for the same assistant. Older clients keep their existing read and write routes. |

### Agent tab naming (#15387)

| Pair | What happens |
| --- | --- |
| New hub and manager | An unnamed start gets a local placeholder immediately. `agents.tab_name_started` carries the returned session and expected tab identity, and the late model name uses the node's guarded rename. |
| New hub, older manager | `agents.start_params` keeps the older synchronous model naming behavior. The older manager does not understand the completion event, so the new guarded late rename requires a manager upgrade too. |
| Older hub, new manager | Start parameters carry the local placeholder without a model wait. An older hub does not emit the completion event for a synchronous start and retains its legacy asynchronous post-start behavior; upgrade the hub for the guarded completion path. |

### On-demand assistant skills

| Pair | What happens |
| --- | --- |
| New manager, older node | `assistant.prepare.claude_md` retains the complete work and rich-view procedures. The older node ignores `work_guide_md` and `views_guide_md`, so it never refers to a built-in skill its binary does not carry. |
| Older manager, new node | An absent `views_guide_md` keeps the manager's complete rich-view procedure. Existing `work_guide_md` behavior is unchanged. |
| New manager and node | The node syncs both native skill roots before writing instructions. It selects compact rich-view guidance only when both managed `persona-views` copies are ready; if work guidance was requested, its readiness is checked independently. |
| Local same-name skill | A newly introduced built-in does not adopt or replace an existing `persona-views` directory. The node retains the complete instructions because that local directory is not a verified managed copy. The managed directory's marker and a separate ownership file must be hard links to the same inode, so copying and editing the directory revokes ownership. If a later per-send sync finds a user replacement, the node restores its saved complete instructions and stays on them until a new prepare verifies the managed guide. |
| Node downgrade before the next prepare | The shared legacy inventory does not list `persona-views`. An older node therefore does not remove it as an unknown managed built-in, and a process resumed with compact instructions can still read the on-demand skill. A cached same-name extension disables compact selection because an older node may copy it over the managed guide; if that happens, the next runtime sync restores the saved complete instructions. The next prepare from an older manager also restores the complete inline procedure. |

A working supervisor started with the legacy complete policy keeps that exact policy across a manager upgrade until an idle send. Main, background and side conversations without a direction record persist their selected role in `assistant.supervision_roles`; existing `direction.RoleInEffect` records remain authoritative. A new main or background conversation records its compact role only after the node accepts the start and returns the conversation id. Other role changes share the send's outcome settlement: a refused idle send restores the previous baseline, and a policy-only lookup cannot create one. Leader membership transitions inherit only a previously recorded role, before the new membership becomes visible; a pre-upgrade process without that evidence keeps the complete policy. A missing or unreadable baseline always fails closed rather than changing a working process's fingerprint.

A manager downgrade while a role-filtered supervisor is working is a deliberate compatibility limit, not a seamless policy switch. The older manager presents the legacy complete fingerprint, so the node keeps the running process and its policy unchanged and refuses that send as busy; existing supervision delivery retry waits for idle, while a direct follow-up can receive the busy response. Once idle, the older manager's complete policy can be accepted normally. The node's supervision-policy fingerprint and busy guard are not relaxed for this downgrade case.

### MCP namespace / tools

The hub sends its feature list in `devices.connected`, matching the capabilities advertised in `hello.features`.
The devices manager forwards it as `node.configure.hub_features`; the node snapshots that list when starting an
assistant process to choose its MCP namespace. A new Claude, Codex app-server or guest assistant registers the built-in MCP
handler only as `persona` when the hub announces
`persona_contracts_v1`. Its automatic tool permissions use the same namespace selection, so each built-in tool is
offered once and only one built-in MCP process is needed. An old hub, an absent feature list or a connection that has
not received its hub capabilities keeps both `persona` and `jarvis`. On a hub reconnect, `rehook` forwards the new
announcement through `configureNode`; a successful reply without `hub_features` sends an empty list. Until that
`hubConnected` call returns successfully, or if it fails, a retained node connection keeps the previous hub's
capabilities. A new conversation in this window can therefore still register only `persona` after a hub downgrade.
The manager handshake does not wait for capability resets or disconnect nodes over them. A decision for one process
is not changed halfway through its startup. An unused warm process whose namespace policy
no longer matches is replaced before reuse. No user configuration is required.

MCP wait correlation deliberately does not inherit that startup snapshot. The node reads the connection-local
`mcp_wait_v1` flag afresh for every local `mcp.call`, so the next call follows a hub upgrade or rollback while the
assistant keeps running. A call that started without the feature is not registered retroactively, and one already
registered keeps its own lifecycle through its final response.

With a capable hub, resumed assistant sessions also register only `persona`, including idle recycling and scheduled-task
resumes. Claude Code 2.1.322 was driven through a real two-turn check: the first turn called
`mcp__jarvis__todo_decisions`; resuming that transcript with only `persona` configured completed the next request without
a provider or transcript error. Historical recognition remains independent of registration, so readers and read-only
worker supervision accept both `mcp__persona__report_to_supervisor` and its legacy name. Generated assistant instructions
tell the model to translate old `mcp__jarvis__X` references in stored routines to `mcp__persona__X`.

The Antigravity companion registers only `persona` whatever the hub: `agy` names the tools after its own label for the
server and `personad mcp` sends the hub bare tool names, so a second namespace would only count every tool twice against
`agy`'s tool budget (#7333).

Only `persona_settings` is listed in the assistant's tool schemas. `jarvis_settings` remains accepted by the call path
and normalized to `persona_settings`, so an old direct call still runs without publishing a second identical schema.

The shared `mcp.json` generated at node startup and checked or repaired by doctor also retains both namespaces.
It is created before the hub handshake, and ordinary Agent launches and resumes reuse it without a per-session
namespace decision. Doctor cannot safely narrow that file from the connected hub's capabilities alone. This
optimization therefore applies to the per-process Claude, Codex app-server and guest assistant configurations.

Registration and historical recognition are separate: transcript readers, task completion and supervision folding
continue accepting `mcp__persona__*` and `mcp__jarvis__*` forever. Extension namespaces and their permission rules are
unchanged. Guest allow lists remain restricted to their configured tools; namespace selection grants no extra tools.

### Conditional automatic stop

`session.stop_if_idle` is additive; the node protocol version and minimum remain 1. It takes
`{session_id, retire:true, stop_cause}` and returns `{stopped}`. Only an automatic stop cause is accepted.
The node refreshes runtime state and checks cancellation and pending/recent input before each Ctrl+C while holding
the session's typing lock. Incoming input is recorded before waiting for that lock. Input recorded between keys
prevents the second interrupt.

Managers use cancellation-enabled `device.retire` and `devices.retire_call` across the existing bus, only for this
method. Ordinary `device.call`, `devices.call` and explicit user stops keep their completion semantics.
An old hub, manager or node refuses the new method without stopping anything. There is no unconditional
`session.stop` fallback; the Agent stays available until a compatible path can retire it safely.

### MCP progress wait kinds

`mcp_wait_v1` is additive and does not raise the node protocol floor. Only an MCP process that has a progress token
sends `wait_updates:true`; only a node whose authenticated hub currently announces the feature generates a `call_id`
and forwards the opt-in for that call. An old MCP process sends neither field, and an old node or hub returns one final
local-socket line as before. An old manager ignores the optional fields and publishes no updates, so a new MCP process
keeps `Still waiting`. An actually old MCP binary retains its historical lowercase `still waiting`. A new node never
sends an interim line to a client that did not opt in.

Live updates use the existing `devices.notify` event `mcp.wait {call_id, kind}` and local interim lines
`{"wait_kind":kind}`. Managers that accept hub-mediated approval relays declare `mcp_wait_v1` in `manager.hello`; the hub
checks the current connection before each such relay. The optional UUID `call_id` on `computer.call` and `computer.elicit`
correlates a nested LCU interaction without another connection. A target node advertises the same capability in
`node.info` to promise that it preserves the id through that pair. The manager sends the id only to such a target. Its
in-memory registry binds that id to the originating machine and live wait state, so a nested card joins the same reporter
without any machine-routing field. Unknown, malformed, oversized or completed ids get no reporter and never fail the
underlying tool call. Old upstream peers omit or ignore the id and publish no nested update. For an old target on an
otherwise new path, the manager instead publishes `unknown` while that target owns the delegated `computer.call`, then
restores the preceding live state; a silent target must not leave the terminal saying `Waiting for the computer` while
an uncorrelated approval is open. A native permission with both session and always scopes reports `approval` even when
its four choices use question-card presentation; a genuine enum question reports `answer`.

No peer is required to understand a new event: absent updates mean `unknown`, and every receiver normalizes an
unrecognized enum to `unknown`. The four terminal messages are a presentation mapping only;
`unknown | approval | answer | computer` is the entire wire state. Nothing is persisted, and removing the pending call
before its final socket write prevents late updates after the answer. The peer-combination table and terminal messages
are in [protocol.md](protocol.md) ("MCP tools (`personad mcp`)").

### Task workspace sources

The read-only `task.workspace.capabilities` probe adds optional `empty_repositories` and `local_git` fields.
The base `task_workspace_v1` capability and the node protocol floor stay the same.

| Pair or state | Behavior |
| --- | --- |
| New manager and capable node | New isolated task dispatches use node naming and preparation. A local Git source is frozen to its actual commit. A folder-only workspace uses the empty-container capability. |
| New manager and older node | Ordinary dispatch keeps its engine checkout path when the probe omits local support or is unknown. A transport error remains a failure of that attempt. A bound-remote backend plan keeps using the original capability. |
| Older manager and new node | The manager still sends the original preparation and launch fields or calls the engine's checkout methods. A legacy checkout keeps its existing path and ownership rules. |
| Node rolled back after a workspace operation was saved | A retry keeps the saved operation and computer. If the peer cannot read the selected source, the operation can continue after the node is updated. The manager does not replay the start through the engine. |
| Older client and new manager | Optional `system_defaults` and `branch_sources` can be ignored. The naming-plan digest and fields for existing remote plans are unchanged. |

### Assistant conversations on the CLI only (#9799)

Every assistant conversation on Claude Code (the main chat, side chats, scheduled tasks, guests and meeting rooms) runs
on its CLI (`claude -p` with stream-json). A hub or node before #9799 could also run the main chat on the Agent SDK,
chosen by a `runtime` in the main chat's model binding. Each pair keeps working:

| Where | What it gets |
| --- | --- |
| A stored binding | A `model_config` or model-profile binding an older hub saved with `runtime` (`"agentsdk"` or `"cli"`) reads as the CLI, without an error. The key is ignored on read and is gone after the next save. |
| An older client saving model config | `runtime` in a `PUT` / `PATCH` body, with any value (`null` included) and on any purpose, is accepted and ignored: the save succeeds and stores no runtime. |
| A client reading model config | The `assistant` purpose has no `runtimes`, no purpose lists `runtime` in `fields`, and a launcher picker's `default` has no `runtime`. Clients show the runtime picker only when `runtimes` is present, so it is hidden; their picker stays for an older hub. |
| New hub, older node | The manager never sends `runtime` on `assistant.send` / `assistant.warm` and drops one a caller left in the params. The older node gets no runtime and a fingerprint without one, and runs the CLI. A guest conversation it already runs on the Agent SDK stays there until the node is updated: a guest's messages carry no fingerprint, so that node keeps the runtime its state recorded. |
| Older hub, new node | The node ignores the `runtime: "agentsdk"` an older hub sends and starts the CLI. |
| A conversation that ran on the Agent SDK | The node continues it on the CLI with `--resume <id>` in the profile (config directory) its state recorded: the same conversation id, no history lost. One the SDK started on the plain `claude` launcher keeps running the claude executable, as the SDK did, not the launcher's alias or shell function; the first entrypoint on its transcript (`sdk-ts`) tells it apart, as the hub never kept a runtime. |
| Session rows | `runtime` and `runtime_fallback` are no longer produced, and the hub drops them from an older node's report. Clients read their absence as the CLI. |
| The runtime package | The Agent SDK runtime package (`persona-agent-sdk-runtime`, under its older `jarvis-` name too) is no longer carried. An older node that looks for it finds none and runs the CLI, its existing fallback. Copies already unpacked under a node's `runtime-packages/` are unused and not cleaned up. |

### Inbox AI-twin permission mode (#13245)

The AI twin's own saved mode is authorization metadata, not a caller's model choice. The manager sends it as
`inbox_permission_mode` on `assistant.send`, `session.send` and `assistant.model`, separately from the ordinary
`permission_mode`. The dedicated field needs no version negotiation and does not raise the protocol floor:

| Pair | What happens |
| --- | --- |
| New manager, new node | The node applies the AI twin's own supported mode to new chats and stopped-process resumes. Plan, unknown values and an unsupported resolved mode stay in `default`; a live process keeps its current mode. |
| New manager, older node | The older node ignores the dedicated field and retains its previous `default` clamp. The setting takes effect after the node is updated. |
| Older manager, new node | The dedicated field is absent, so the node stays in `default` even if the older manager supplied an ordinary caller `permission_mode`. That caller value never elevates Inbox. |
| Rollback after an explicit bypass | A live process keeps the mode it started with. If an older manager or node drives its next stopped-process resume without the dedicated field, that resume returns to `default`. |

Origin attestation, the Inbox MCP/tool gate and Lark outbound policy are unchanged in every combination.

### Ordinary assistant permission defaults on resume (#13653)

The manager sends its current default as `default_permission_mode` and marks a deliberate generic choice with
`permission_mode_explicit` on `assistant.send`, `session.send` and `assistant.model`. The node persists that explicit
provenance as `app_mode_explicit` in the existing `node_model` JSON. Neither field changes the model fingerprint, the
public client API or the Inbox/guest policy.

| Pair or state | What happens |
| --- | --- |
| New manager, new node | A new or stopped Claude Code process uses the explicit conversation mode first, then the current manager default, then legacy state/transcript evidence. A live process keeps its current mode. |
| New manager, older node | The older node ignores both fields and retains its legacy resume behavior until updated. Initial starts still receive the ordinary `permission_mode`. |
| Older manager, new node | Both fields are absent, so the node retains the legacy `AppMode` / in-memory / transcript precedence and built-in `bypassPermissions` fallback. |
| Non-Claude launcher | The manager omits the resume default and the node keeps legacy `AppMode` / in-memory / transcript precedence, even when the declared mode list contains that default. An explicit generic `permission_mode` follows its existing path. |
| Legacy Claude state without `app_mode_explicit` | Its `AppMode` is treated as inherited when a current manager supplies a default. An explicit choice made after the upgrade persists with the marker and wins thereafter. |

### Node connection lanes (#10660)

A manager and a node built since #10660 frame their connection on four lanes (protocol.md "Lanes"); any older end
keeps JSON lines, byte for byte. The switch is asked for on the connection itself, so what sits between the two ends
does not take part:

| Pair | What happens |
| --- | --- |
| New manager, new node | `node.info` announces `node_lanes_v1`; the manager sends `rpc.lanes`, the node answers and frames its output after the answer, the manager frames its own after one marker line. Logged as `lanes=on` on the "connected" line. |
| New manager, older node | No `node_lanes_v1` (and a node from before the handshake never counts as having it): the manager never asks. Lines, as before. |
| Older manager, new node | The manager never sends `rpc.lanes`; the node announces a capability the manager ignores and writes lines as before. |
| A node that announces lanes and refuses | It answers `rpc.lanes` with an error (`lanes_refused`, or `unknown method`); the manager logs "staying with lines" and goes on with lines. |
| An older relay in between | `node --stdio` relaying to a resident node, Persona Link and its hub relay (`spliceWS`), tailcat, ssh and a member hub copy bytes without reading them; frames pass through all of them, older versions included. A `node --stdio` that serves the connection itself is the node: its own version decides. |
| A manager that stopped waiting | The node's answer switches the manager's input whenever it is read, and its output follows: an answer after the handshake's wait ran out still frames both directions. |
| Lines in flight during the switch | Each side switches its output under its writer's lock right after its announcing line, and its input right after the other side's: nothing written before is read as frames, nothing after as lines (`TestLanesUpgradeWithTrafficInFlight`). |

Nothing is stored: every new connection negotiates again, so a node or manager rolled back mid-session simply connects
with lines next time.

### Resuming node connections (#10663)

A framed connection between a manager and a resident node built since #10663 survives its transport ending for a
while (protocol.md "Resuming"); anything older drops it as before. Resuming is asked for with `rpc.lanes` and happens
on a new transport, through the same relays:

| Pair | What happens |
| --- | --- |
| New manager, new resident node | `node.info` announces `node_resume_v1`; the manager adds `"resume":1` to `rpc.lanes` and the node's answer grants it with a token. Logged as `lanes=on resume=on`. A dropped transport is taken back with `rpc.resume` on a new one. |
| New manager, a node without a resident node (`node --stdio` serving the connection itself) | It ends with its transport, so it does not announce `node_resume_v1`: the manager does not ask, and a drop reconnects as before. |
| New manager, older node | No `node_resume_v1`: lanes as in #10660, a drop reconnects. |
| Older manager, new resident node | The manager's `rpc.lanes` does not ask for resuming, so the node grants none and keeps nothing; a new transport's first line is the manager's handshake, which replaces the connection as before. The node reads that first line before serving, which an older manager writes at once too. |
| A token the node does not have | A node restarted (a new process), or a token already used: `rpc.resume` is answered `resume_refused`, the transport closed, and the manager connects anew. A token asked with again because the answer was lost is still taken until the new transport carries a byte. |
| A drop past the window, or while the node restarts | The manager closes its peer and connects anew after its 30 s; the node's kept connection ends at its own window's end (60 s), or when a new connection replaces it. |
| An older relay in between | Relays copy bytes; the resume goes over a new transport through them like any connection. |

Nothing is stored: tokens live in each process's memory, so a restarted manager or node connects anew.

### Direction session filings (#11463, #11490)

Since #11463 the assistant manager's filing book (settings `assistant.direction_sessions`) keeps three fields per entry
that a manager from before does not know: `parent` (a Codex sub-agent session's parent, for placement rule 1),
`was` (the entry the write replaced, for its Undo) and `removed_at` (an entry taken out, kept seven days for its
Undo). Every manager answers the filing route and keeps its own book: the hub relays a write to the manager the request
names, else to the default one (`server/internal/hub/assistant_link.go`), so a rollback of any manager meets them in
the book that manager keeps:

| Case | What happens |
| --- | --- |
| A new manager reads a book an older one wrote | Its entries have none of the three fields: the Undo of their last write is only `null` (taking the entry out), and a sub-agent session its computer no longer lists is placed by its filing, as before. Nothing is lost. |
| An older manager (rolled back) reads a book a new one wrote | It reads an entry taken out (`removed_at`) as a filing: the session shows filed in that direction again and counts toward its 50. It ignores `was` and `parent`. |
| That older manager writes the book once | It saves every entry without `removed_at`, `was` and `parent`. Rolled forward again, the entries taken out before are filings for good (the owner takes them out again), no write before the rollback can be undone exactly, and a not-listed sub-agent session is placed by its filing until it is listed again. |
| Answers of an older manager | The write's `previous` is the opener of a chat a leader opened, or `""`, never `null`; a `null` direction is taken as `""` (on its own); every write, `null` and `""` included, needs the session listed on its computer (404 `session_not_found` otherwise), and an Undo into an ended or full direction is refused (409 `direction_ended`, `direction_full`); a session whose computer could not be read is `offline`, one whose computer has given no list `not_listed`, and the read has no `sessions_unavailable`. Both versions name `direction_sessions`, so clients reach the same routes and get the older values. |

What an operator can do: rolling forward again before the older manager's first write to the book loses nothing, as
it reads the extra fields without saving. After such a write, a copy of the setting that holds the book
(`assistant.direction_sessions`), taken before the rollback, is the only way to get the entries taken out and the
`was` of each entry back. A manager from before #11515 also leaves the sessions placed under directions out of the
profile's `keep`: a quiet one leaves its computer's list again and its automatic archive goes on, while the book stays
as it was. Rolling forward lists again the ones that only dropped out of a computer's list; one the older manager's
automatic archive took stays archived until the owner restores it, as nothing restores an archive by itself.

### Resumes a computer has nothing of, and an extension's resumes (#10941)

Since #10941 a node refuses a Claude Code resume of an id it has no transcript, start or record of with
`resume_unknown`, before any tab opens, and a node that announces `resume_outside_health` also checks an extension's
resume of a chat its computer does not list (protocol.md `session.start`). The hub, the devices manager, the assistant
manager and the node may each be older:

| Pair | What happens |
| --- | --- |
| New hub, new node | An extension's resume of an unlisted chat goes on marked `resume_outside_health`; the node resumes it only outside the health space, and its `resume_unknown` reaches the extension as the hub's own 404. The owner's resumes are never marked. |
| New hub, older node | The node does not announce `resume_outside_health`, so the hub resumes for an extension only a chat the computer lists outside the space (404 otherwise; 503 `sessions_not_listed`, retryable, before the hub has had a list of that computer since it started, and while it has no word of the node at all, as after a devices manager restarted without its saved state). The owner's resumes behave as that node always did, including a start under an id it has nothing of. |
| New devices manager, node older than the hub last heard | A marked start toward a node that does not announce the check is refused `node_outdated` (409) and nothing starts. |
| Older devices manager, new hub and node | The manager passes a start's params on as they came, so the node gets the mark. |
| Older hub, new node | The hub never marks a resume and lets an extension resume any id its computer is asked for, as before #10941. The node still refuses an id it has nothing of, so no conversation starts under the id of another; a transcript it has is resumed, health space included. |
| New node, older hub: the owner's wording | `resume_unknown` comes with the node's own words, in Chinese: an older hub has no wording of that code in the reader's language. A hub since #10941 words it in en, zh-Hans and ja (`rpc-errors.resume_unknown`). |
| New node, older assistant manager: launcher health | A hub since #10941 does not report a `resume_unknown` refusal to the manager (`StartEnded`). An older hub does, and a manager from before #10941 counts it against the launcher: the launcher shows as failing on that computer until its next check passes (`docs/server/launcher-health.md`). It takes a Resume of an id the computer has nothing of, which the apps offer only for chats a computer listed; updating the hub and its managers before the nodes avoids it. |
| New node, an Agent an older node started through an engine runtime (Paseo, Cindy) | A node since #10941 keeps its record of every Agent it starts through an engine runtime, the folder the check reads, marked with the engine that started it (`engine_runtime`). An older node kept one only for a start with a creation identity (an owner's start from an app), unmarked, so an extension's Agent it started has none the check takes: once its engine no longer lists it, the extension's resume answers 404 and only the owner resumes it. |
| New node, older devices manager: the record's `engine_runtime` | The manager stores a node's records as its own type, which has no `engine_runtime` before #10941, so it drops the mark. Until the node restarts nothing changes (the node keeps its records in memory); after a restart its Agents' records come back unmarked, and an extension's resume of one its engine no longer lists answers 404: only the owner resumes it. The mark is written only when an Agent starts, so updating the manager later does not bring it back for those Agents. Update the devices manager with the hub, before the nodes. |
| Any node: a terminal chat's record | A terminal record alone no longer lets an extension resume a chat its computer does not list (its transcript gone, or a first start that failed): every resume writes the record anew with the folder it names. Nothing is stored for it; the owner's resumes are unchanged. |

The records are the node's own (the hub keeps its copy, as for any start), of the same shape an older node already
writes for a start with a creation identity, plus `engine_runtime` in the model settings the hub keeps with each one
(no table changes), so rolling the node back leaves them to be read as those, the mark ignored. Nothing else is
stored: rolling either side back or forward changes only what the next resume does.

### Outside reporting directions (#8236)

The manager migration `20261120081453_outside_directions.sql` adds only `assistant_outside_directions`; existing
settings, directions, conversations and to-do ownership are not rewritten. A new manager on an older database therefore
starts with no outside bindings until one is created. The table is separate because an older settings writer does not
know the outside marker and would otherwise discard it.

An older manager or hub has neither new write route nor `leaders.write`; those calls keep its previous 404 / 403
behavior. A new CLI talking to an older node receives its legacy `unknown kind` answer and exits unavailable without
retrying. `leaders.read` keeps its existing routes and events. On rollback, an older manager does not read the outside
binding table or emit `external` / Agent-target metadata, and its legacy direction code may treat the stored opaque id
as a conversation id; outside directions are therefore not a supported rollback interface. Existing
conversation-backed directions keep their old behavior. Rolling forward again rejoins bindings that remain in the
table to the surviving direction records; an owner or older manager removing a direction from `assistant.leaders`
still ends its current membership as before.

### Launcher default models (#4663)

A launcher with no explicit model has an empty `model` on the wire; display text is never a model ID. Older nodes and
shell helpers could send the Chinese placeholder `默认` instead. Every client treats that legacy value exactly like an
empty model: the launcher's own default applies, its existing localized Default presentation is used, and no placeholder
is sent as a model pick. Explicit model IDs are unchanged in both directions.

### New omp sessions in herdr and Orca (#11964)

Both old and new nodes can return an empty model list for `omp`. New clients request the launcher models for the
selected host. They offer typed model IDs for native `omp` only when that response includes
`"supports_start_model": true`. An older node can start `omp` with its own default. Older clients ignore the
optional field. A registered runtime called `omp` still supplies its own list of supported models.

### Orca refusals in the reader's language (#17057)

A node since #17057 adds a code and data to the refusals and errors on its Orca paths (the codes and reasons are in
`docs/integrations/engines.md`, "Refusals in the reader's language"), and a hub since then words them in each request's
language. The node's own words are unchanged: what was Chinese is the catalog's zh-Hans text, what was English
(`unsupported: <engine> cannot do this`, `tab <id> is not running`, Orca's Codex launch settings) stays English.

| Pair | What happens |
| --- | --- |
| New hub, new node | The summary is in the request's language; the engine's or Orca's own words are the detail. |
| New hub, older node | Codes the older node already sent (`orca_not_registered`, `runtime_session`, `codex_launch_preferences` without a reason…) are worded by their code; a refusal without the new data reason keeps its words. |
| Older hub, new node | The node's words reach the client as before. Errors that had no code (a tab, pane or terminal the engine would not open, close or split; a tab name; Run Agent Here's checks; `unsupported`) now carry one, so an older hub answers them 409 with the node's words instead of 502 with its generic summary. |
| Older clients | Fields are additive. iOS's rename alert, which looks for an English `unsupported` or `is not running` in the message, shows the hub's sentence after "Couldn't rename:" instead; the web's worktree dialog also reads the code (`unsupported`, reason `engine_unsupported`). |

## Start-defaults field convergence (#11719)

The rule fields of `GET`/`PUT /api/v1/assistant/start-defaults` and of the nested `start_defaults` in `PUT /api/v1/assistant` converge on new names in #11719. This is extension-facing JSON, and the same structure is persisted in the Postgres settings, so dual-read must cover both live requests and already-stored policies.

| Identifier | Old name | New name | New hub / manager behavior |
| --- | --- | --- | --- |
| Rule match | `when` | `applies_when` | Dual-read on decode (stored policies included); dual-write the same value on encode; both keys present with different values is a 400, with equal values the current name is read (presence, never an OR). An explicit `null` is present on the wire, so `null` beside a real value is a disagreement, not an absent field. |
| Candidate list | `then.presets` | `then.candidates` | Same as above; each row gains `priority` (omitted is 0, so a list with no priorities behaves byte-for-byte like the old ordered list). |
| Read-only preview | none | `POST /api/v1/assistant/start-defaults/preview` | Reuses the `assistant.start_defaults` scope, no new scope. An older hub has no such route, so an extension call gets 404/403 and must not be rewritten to another endpoint and retried. |

- **The two write paths fail differently on an old hub/manager, and a writer must know which it is talking to.** On the dedicated `PUT /api/v1/assistant/start-defaults` route, `DisallowUnknownFields` makes an old hub answer 400 to `applies_when` / `candidates`. On the older nested `start_defaults` of `PUT /api/v1/assistant`, the handler decoded with a plain `json.Decoder`, so an old manager **silently ignores** the new fields instead of failing: it can return 200 while storing an empty match and no candidates, which turns a worker-scoped rule into an all-task rule. There is no capability bit to read, so the safe order is to `GET /api/v1/assistant/start-defaults` first and check whether the answer carries `applies_when` / `candidates` (present means a new peer), then write only the field names that peer understands. Never "try both names" within one write as a fallback.
- Reads answer both field names, so an old reader that GETs and then PUTs back does not wipe the policy to empty rules. An old reader does not know `priority`: it drops the field when re-encoding, so a prioritized policy does **not** round-trip through an old reader unchanged — every priority comes back as 0 and equal-weight selection order applies.
- Semantics are not loosened: `assistant.start_defaults` is still granted by name only, an extension's policy still must expire within an hour, unknown fields are still a 400 on the strict route, and the preview writes no state. Aliases are never silently merged; a genuine value conflict must be resolved by the writer.
- Candidate selection reads one `startCandidateSource` interface (`start_candidates.go`); a future dynamic candidates source implements it and changes none of the fields or compatibility rules in this section.

## 配置、目录、服务与升级渠道

| 标识 | 旧名 | 新名 | 新实现读旧安装 | 与旧实现共存／回退 |
| --- | --- | --- | --- | --- |
| 环境变量 | `JARVIS_*` | `PERSONA_*` | 新值优先（显式空值也优先），否则旧值 | 给旧子进程发送同值别名 |
| 运行目录 | `~/.jarvis` | `~/.persona` | 保留已有真实目录和 symlink，不分裂状态 | 无旧目录的新安装建立兼容入口，不搬用户数据 |
| 构建配置 | `~/.config/jarvis/config.json` | `~/.config/persona/config.json` | 明确配置优先，旧文件 fallback | 不重新配对、不新建凭据 |
| 可执行文件 | `jarvisd`, `jarvis-link`, `jarvisd-wechat` | `personad`, `persona-link`, `personad-wechat` | 旧入口别名 | 旧路径仍能启动新二进制 |
| systemd / launchd | `jarvisd{,-node}`, `jarvis-link`, `com.jarvis.{hub,node,manager.*}` | `personad{,-node}`, `persona-link`, `com.persona.…` | 优先已安装服务，避免双启动 | 新装后的旧 installer 再安装不在滚动升级授权范围，另列边界 |
| Postgres 容器／卷／账号／库 | `jarvis-postgres`, `jarvis-pgdata`, `jarvis` | `persona-postgres`, `persona-pgdata`, `persona` | 沿用配置的 URL 和已有容器／卷，不初始化另一套状态 | 已有 URL 不改写，旧角色仍连接同库 |
| 二进制下载产物 | `jarvisd-<os>-<arch>.gz`, `jarvis-link-…`, `jarvisd-wechat-…` | `personad-…`, `persona-link-…`, `personad-wechat-…` | 接受 manifest 的旧产物名，保持摘要校验 | 仅 GET 404 可名称 fallback；摘要失败不能降级 |
| APK | `jarvis.apk` | `persona.apk` | 下载路径／文件名 alias | Android 注册 package 不因显示名改变 |

## 网页与 Android 本地边界

| 标识 | 旧名 | 新名 | 新实现读旧状态 | 写入／安全约束 |
| --- | --- | --- | --- | --- |
| 网页登录 | `jarvis.token`, `jarvis.tokenId` | `persona.token`, `persona.tokenId` | canonical 存在（包括空串）优先；旧 token 的 ID 只与同一旧 token 一起迁移 | 写两套，退出清两套；不把旧 ID 配到新的 token |
| 网页外观与筛选 | `jarvis.theme`, `jarvis.agents.prefs`, `jarvis.agentGrouping`, `jarvis.agentFilter`, `jarvis.files.view` | 相应 `persona.…` 精确键 | 只读登记的键，不按 prefix 扫描任意 storage | 外观同步 bootstrap 在首帧前读旧 theme，不接触凭据；修改双写 |
| 网页新对话草稿 | `draft:jarvis-new` | `draft:persona-new` | Composer 实际读旧键，canonical presence 优先 | 双写，保留其他会话草稿键 |
| Android 分栏偏好 | `jarvis-panes`, `split.jarvis` | `persona-panes`, `split.persona` | canonical preference／值存在时优先，否则读旧；普通 pane key 不改 | 保存当前键，不清空旧 AppData |
| Android 已排队确认 | `jarvis.phone.confirm` | `persona.phone.confirm` | receiver 双读 action，既有 data scheme 不加限制 | 保持 live callback 和锁屏授权限制；不自动批准 |
| Android 更新缓存 | `jarvis-update-*.apk` | `persona-update-*.apk` | 两类都视为更新包 | 清理不当成普通 AppData；不改变 package／签名身份 |

## Side-session fold (`sessions_side_fold`, #7944)

The manager folds a Claude desktop app's side sessions under their parent (`folded_as: "side"`, `folded_side`, the
`children` sides keys; `docs/protocol/protocol.md` "Session hierarchy") and names `sessions_side_fold` in its bus
hello; the hub lists the feature only while every manager that carries Agents names it.

| Pair | What it sees |
| --- | --- |
| New hub and managers, new client | Sides fold into their parent's chat; lists and counts show the parent once. |
| A manager without the capability beside a new one | The hub does not list the feature; new clients keep sides as rows. The new manager's rows still carry the fold, so a `roots=1` page leaves its sides out of the rows, as it does any folded row. |
| Old manager, new hub | No fold and no feature: clients keep today's behaviour. |
| New hub and managers, an older web bundle | Until it reloads it folds by `folded_under` alone: a side is drawn as a child card in its parent's chat and left out of the rows; its parent shows the side's working or needs-you state, not its unseen result (`sides_unread`). |
| New hub and managers, an older Android app | The same: it folds rows by `folded_under`, so a side is one of its parent's child sessions until the app is updated. |
| New hub and managers, an older iOS app | From #8010 on it folds rows by `folded_under` too, so a side is one of its parent's child sessions until the app is updated; an app from before #8010 folds by its own child fields, which a side lacks (no `parent_session_id`), and the side stays a plain row of its own. |

### Screen Link close confirmation (#10006)

Screen Link `close_ack` is message type 14 with the same channel and 64-bit final offset as the `close` it confirms. It is
additive and needs no feature negotiation: a new phone sends it after every `close`, including a repeated close for an
already ended channel, and an old node skips the unknown type. A final-data `ack` is not a close confirmation, because
it can cross with a close the phone has not processed. A new node accepts `close_ack` only for the outstanding close's
channel and exact final offset; duplicate, stale and unknown-channel confirmations are ignored.

| Pair | Behavior |
| --- | --- |
| New node, new phone | An unconfirmed close is offered again on a different surviving route. Confirmation stops another copy; a late valid confirmation also cancels a copy not sent yet. |
| New node, old phone | No `close_ack` arrives. A route change can offer the close again, which the old phone already accepts idempotently. An unconfirmed video close stays recoverable while it has a route; with no route it finishes as before. |
| Old node, new phone | The old node skips type 14. The phone's data, reset and close behavior is otherwise unchanged. |
| Old node, old phone | Unchanged. |

RFB keeps its existing completion rule in every combination: the local close finishes the channel and session at the
next watchdog tick without waiting for `close_ack`.

## Counts view (`counts_view`, #15893)

A manager that names the capability `counts_view` tells the hub its counts view (`counts.view`); the hub relays it to
clients that ask for the feature as `counts` messages, lists the feature always, and says `counts_view: true` on the
roster entry of each online manager that tells one ([counts.md](counts.md) "The hub's relay"). A client draws a
surface from views only while no manager in its scope is absent, and keeps a manager's last view across `view: null`,
going offline and a hub restart (D10 and D13 of `docs/plans/plan-derived-counts.md`).

| Pair | What it sees |
| --- | --- |
| Old app, new hub | It never asks for the feature, so it gets no `counts` message, and it ignores the roster's unknown `counts_view`. Its numbers are its own, as before |
| New app, old hub | The hello lists no `counts_view`: every surface draws the app's fallback, today's rule |
| New app and hub, a manager without the capability | Its roster entry is online without `counts_view`: the surfaces whose scope holds it draw the fallback |
| New manager, old hub | The hub drops `counts.view`, an event it does not know |
| Manager restarts or is redeployed | `view: null`, then a view with a new epoch; meanwhile the app draws its last view as it was, and no rule changes |
| Hub restarts or switches versions | The new hub has no view until each manager connects again and tells its last one; an app keeps the views it has, and one with none draws the fallback until then |
| Manager or hub rolled back | The hello lists no `counts_view`, or the reconnected manager's entry has no `counts_view: true`: the fallback from that message on |
| Several paired hubs | Each hub on its own; the app adds them up |

## Node update draining (`node_update_drain_v1`, #16517)

`node_update_drain_v1` is additive and keeps the node protocol floor unchanged. A resident node that announces it emits `node.update.draining` (`{id, target}`) and includes `update_draining` in `node.info` while draining active work after `node.update.commit`, then includes `{id, target}` in `node.restarting` at handover. Both the manager's two-minute confirmation timer and the local rollback watchdog's deadline start at handover instead of on the commit reply.

| Pair | Behavior |
| --- | --- |
| New manager and capable node | After `node.update.commit`, the manager enters `deploy_phase: "draining"` and waits until `node.restarting` (or a sustained disconnect) before starting the two-minute confirmation timer. A hub restart while draining resumes tracking from `node.info.update_draining` instead of misclassifying the old-version draining node as `rolled_back`. A late reconnect that confirms the target version clears any persisted `failed` deploy record in Postgres. |
| New manager and older node | The node omits `node_update_drain_v1`, `node.update.draining`, and `node.info.update_draining`. The manager keeps the legacy post-commit `confirming` flow and two-minute timeout; late reconnects that confirm the target version still clear persisted `failed` records. |
| Older manager and new node | The older manager ignores `node.update.draining`, `node.info.update_draining`, and the extra fields on `node.restarting`, keeping its existing confirmation flow. Locally, the new node still protects its drain from premature watchdog rollback and arms a fresh post-`exec` deadline at handover. |
| New node with older backup watchdog (disk drift) | Because the watchdog process is launched from the backup binary on disk, `BeginDrain` both marks `draining: true` in the marker and slides `deadline` forward while the draining resident is alive, so even an older backup binary that ignores `draining` never rolls back a live draining resident. Adopted external binaries (#15708) refresh only their adoption deadline and never create a backup or watchdog. |
| Older client and new hub | `deploy_phase: "draining"` is an optional string alongside `deploying: true`; clients that do not know `"draining"` keep treating the machine as actively deploying until completion. |

## Apple 端交接

原生协议兼容跟踪 #6192；本地状态与系统身份跟踪 #6152。Apple 的实现应遵循上述能力和字节约定；本清单不替它们宣称 Keychain、Intent、偏好、Shortcut、旧 controls、活动或 watch 在途数据已经验证。

## 可重复验证

```sh
# 显式提供私有 Postgres 管理连接；脚本只创建／删除随机命名的 fixture 库。
python3 -I scripts/test/test_rolling_compatibility.py --workspace <private-workspace> --real-nodes
# 新网页和 Android 使用真实旧 hub；Gradle 额外参数只通过环境配置。
python3 -I scripts/test/test_rolling_compatibility.py --workspace <private-workspace> --clients --android --case old
```

脚本需要 `PERSONA_COMPAT_DATABASE_URL`；可用 `--old-bin` / `--new-bin` 复用已经构建的二进制。真实 node 使用已缓存的公共 Debian image，不挂载宿主 HOME／socket／PID，不拉取 image。Android 的 `PERSONA_COMPAT_GRADLE_ARGS` 接收本机构建参数；每个 case 先清除测试结果再运行，避免 Gradle 把不同私有 endpoint 的探针当成 up-to-date。诊断和凭据只写到 0700 私有 workspace，超时有明确失败结果。

细项命令、合成截图、初次失败与修正、最终结果和未验证项见 [本地验收记录](../archive/operations/rolling-upgrade-compatibility-verification.md)。

## 验证与边界

本地隔离验收通过：8 个真实 hub／manager 与 fixture／真实 node 组合；新网页和 Android 分别对真实旧 hub＋旧／新 manager 完成实际发送、同 ID 重试仅执行一次和 `done` receipt；服务端 91 包、9,179 个通过测试事件（含子测试）、零失败，53 个可选／平台／helper 跳过；网页 5,025 通过、1 可选 live 跳过，构建通过；Android 10 个模块 2,501 通过、零失败、8 个可选 live 跳过（1 个本兼容探针、7 个新主分支的原生扩展 showcase 场景），APK 重新打包通过。可选 live 探针随后在显式私有环境中另行执行，不把默认跳过当作实测。

网页最终深浅色截图已经查看，只有合成 fixture 数据。SDK、runtime 集成、配置／安装／发布产物及精确标识／隐私／文档路径守卫通过；最初失败和修正细节保留在验收记录。CI 停用，未运行，不能写为通过。没有线上验证或部署；没有 Android 真机／模拟器原地升级与视觉验收，原生系统身份、Watch 在途传输及 Xcode 26.0 验证不由本线程宣称完成。
