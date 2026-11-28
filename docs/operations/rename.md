# Persona naming cutover

代码与界面使用 **Persona**。2026-11-07 的兼容约定取代了 #277 的一次性硬切换方案。现有安装按[原位升级指引](upgrade.md#in-place-upgrade-for-existing-installs)保留实际服务和状态目录，并按[滚动升级兼容清单](../protocol/rolling-upgrade-compatibility.md)核对各端版本。该兼容清单和升级指引不单独授权生产操作；生产授权边界仍由 [#1102](https://github.com/nwverse/persona/issues/1102) 决定，历史[切换操作](persona-cutover.md)的禁令则继续有效。

## Registered application identities remain unchanged

Keep the actual values in private configuration for `app.bundle_id`, `app.app_group`, `android.application_id`, iCloud containers, keychain access groups, and App Store / Google registration. Only the variables that carry them change. Examples in the repository are placeholders, not registration values. Do not register a new app, regenerate keys, or discard pairing credentials.

Provisioning profile display names and service labels are operational configuration. Make sure the configured signing profiles exist before archiving on a Mac. Check pending notifications, queued workers and saved Shortcuts against the native upgrade and downgrade evidence below before deploying a new client build.

### 可逆的原生端兼容（#6152）

2026-11-07 的约定要求新旧版本兼容和回装，不再做破坏性转换。历史字面量集中在 `apple/Shared/NativeStateMigration.swift`；独立 target 通过 storage-only 条件编译同一物理文件，不复制第二份常量。读取先用有效的新偏好、选择和 Keychain 数据，再回退到旧数据。启动只补齐缺失的新值，显式保存同时写新旧值，不删除旧存储。兼容逻辑不递归重写目录、图片、草稿、发件箱、录音或不透明归档。冲突的推送密钥保留两份身份：新键优先读取，旧键仍可解开经过认证的旧推送。

公开 App Intent 的 Swift 名称和标题可以更新，系统 `persistentIdentifier` 保持稳定；隐藏适配器接纳中间版本保存的新身份。具体类型 extension 中的标识必须直接写成字面量，仍只放在兼容文件。验收分别检查编译后的 metadata、旧版实际保存的 Shortcut，以及保存后回装旧版的结果。不能修改 action blob 来让测试通过。

Control Center 的旧 kind 继续注册，共用同一绘制和操作配方。Live Activity 同时注册旧的真实 attributes 类型和中间版本的新类型；新建和 push-to-start 使用旧 hub 认识的稳定类型。Swift alias 或编译成功不是系统验收，必须检查原位升级后旧活动的实际 ID 和更新结果。Session 快照保留旧 started-by 字段，供旧 App 和手表读取。合并代码不授权部署、线上重启、执行维护 apply 或发布 TestFlight。

<a id="源码位置构建入口与兼容名字"></a>

## Canonical source and build entry points

| Category | Canonical name |
| --- | --- |
| Go module | `github.com/nwverse/persona/server` |
| Hub / node / hook / MCP binary | `personad`, source `server/cmd/personad` |
| Connector program | `persona-link` |
| Hub / manager programs | `persona-hub`, `persona-manager` |
| Linux services | `personad.service`, `personad.socket`, `personad-node.service`, `persona-link.service` |
| macOS LaunchAgents | `com.persona.hub`, `com.persona.node` (custom connector jobs are inventoried separately) |
| Runtime root / build config | `~/.persona`, `~/.config/persona/config.json` |
| Config override / runtime variables | `PERSONA_CONFIG`, `PERSONA_*` |
| Config / simulator command tools | `scripts/persona_config.py`, `scripts/app/personactl.py`, `PERSONACTL_*` |
| Apple project / modules | `apple/Persona.xcodeproj`, `Persona`, `PersonaWidgets`, `PersonaNotify`, `PersonaLink` |
| Android source / build conventions | `persona.*`, `personaApplicationId`, `persona.android.application` |
| MCP server / tool prefix | `persona`, `mcp__persona__*`, `persona_settings`, `ask_persona` |
| URL scheme / assistant route | `persona://`, `/persona`, `/persona/<id>`, `/persona/new` |
| Message context / file tags | `persona-context`, `persona-file` |
| Header / runtime subprotocol | `X-Persona-*`, `persona-runtime.v1` |
| Default database / container names for new installs | `persona`, `persona_test`, `persona-postgres` |

The `/` front door opens the main conversation. Previously sent assistant links are covered by the route aliases in the [compatibility matrix](../protocol/rolling-upgrade-compatibility.md). Mochi's built-in ID is `persona` and the bundled frog pack is `nwverse`. The five retired bundled choices now display Mochi through the look API's effective-ID fallback, and their old IDs remain reserved. See [Avatars](../apple/persona.md).

## Runtime root: one directory, no automatic migration

The runtime uses the explicit `PERSONA_HOME` or its supported former-name equivalent first. It resolves an absolute path, `~/…`, or a path relative to HOME. With no override it reuses an existing default root under the former name and uses `~/.persona` for a fresh install. Initialization can add `~/.persona` as an alias for the former default root. A custom root may use an existing valid symlink. A dangling or cyclic symlink requires restoring its target before initialization.

**Two independent stores** (#744). A machine that has not been through the cutover can still have its older default directory (`~/.<previous-name>`) next to `~/.persona` as a store of its own, not an alias. In that case:

- Set `PERSONA_HOME` or its supported former-name equivalent to the intended store. Initialization then uses only that store.
- Without an effective runtime-root override, initialization stops with the error "independent state directories exist" before writing to either store.
- Set the same root for clients such as `personad mcp`, `hook`, `launch` and `notify` so they reach the socket opened by the running service.

Agents never run with the node's environment, so the node writes its root into everything it hands them: `PERSONA_HOME` in the `env` of `mcp.json`'s server, in front of the hook command, and in front of the commands it types into a pane (`docs/integrations/engines.md`, "What a start types").

`PERSONA_CONFIG` selects the build/deploy config. The build tools use the current default config path when it exists and fall back to the former default path. They also accept the former environment override as described in the [compatibility matrix](../protocol/rolling-upgrade-compatibility.md).

<a id="部署脚本按服务恢复运行目录"></a>

## Recover the runtime directory per service

`scripts/persona_identity.py --service hub|node|link` looks for installed systemd units under the current and former names. For node and Link it also looks for installed LaunchAgents. Use the macOS hub's actual LaunchAgent to identify its root. The selected role's unit takes precedence when present. An explicit runtime-root override already in the environment, including an empty value, takes precedence over the service definition.

The resolver supports systemd unit resets, sorted drop-ins, `XDG_CONFIG_HOME`, `%h` / `%%`, external `EnvironmentFile` overrides and `UnsetEnvironment`. It parses root overrides as data. Missing required environment files fail closed. Auto mode rejects independent roots, so select the service role explicitly. `--prepare` initializes the selected root as described above. `--prefix` resolves the root on the remote machine and exports it for the remote service under both supported names.

## Verification boundaries

Use the [compatibility matrix and verification record](../protocol/rolling-upgrade-compatibility.md) for the isolated protocol and deployment checks. The native-client checks above cover preferences, Keychain, Shortcuts and Live Activities. Rehearse the actual version combination before the approved production update.
