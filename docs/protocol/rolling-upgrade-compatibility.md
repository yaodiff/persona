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
| runtime 包名 | `jarvis-{dsh,pi,paseo,agent-sdk}-runtime` | `persona-…` | 同一包内容接受旧 ID | 查询旧 node 时用旧 ID |

基线的 `/api/v1/…` 入口、普通 WebSocket `type` 与 node／manager RPC 方法不因产品名切换而整体改名；新增业务能力仍按各自协商规则处理。协议版本没有因本兼容层提高。注册 Bundle ID、系统 controls／Intent 等原生身份由 #6152／#6192 记录，本清单不以服务端测试代替其系统升级证据。

### MCP namespace / tools

The hub sends its feature list in `devices.connected`, matching the capabilities advertised in `hello.features`.
The devices manager forwards it as `node.configure.hub_features`; the node snapshots that list when starting an
assistant process to choose its MCP namespace. A new Claude, Codex app-server, Agent SDK or guest assistant registers the built-in MCP
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
optimization therefore applies to the per-process Claude, Codex app-server, Agent SDK and guest assistant configurations.

Registration and historical recognition are separate: transcript readers, task completion and supervision folding
continue accepting `mcp__persona__*` and `mcp__jarvis__*` forever. Extension namespaces and their permission rules are
unchanged. Guest allow lists remain restricted to their configured tools; namespace selection grants no extra tools.

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
