# 扩展 SDK

写 Persona 扩展用的官方 SDK（#1843、#1853）。零依赖、自成一体：扩展仓库按版本拷一份（`scripts/gen/sync-sdk.sh`），不需要拉主仓库。
怎么用见 `docs/extensions/extensions.md`「Writing extensions: SDK, templates and command line」。

| 目录 | 内容 |
| --- | --- |
| `go/` | Go module `github.com/nwverse/persona/sdk/go`，只依赖标准库。`wire/` 是生成的类型，`ext/` 是运行时（进程协议、UI 路由、HTTP、环境变量），`exttest/` 是扩展自己测试里跑的一致性检查。hub 用 `replace` 引用同一份。根包 `sdk` 把 SDK 和 `bundle/sdk.tar.gz` 编进 personad，给 `personad extension new` 用 |
| `ts/` | TypeScript（Bun）：`src/index.ts` 是运行时，`src/wire.ts` 是生成的类型；扩展用 `bun build --compile` 打成单个可执行文件 |
| `templates/` | `personad extension new` 的模板：`go/`、`ts/`，`{{id}}`、`{{name}}` 会被换掉，`*.tmpl` 去掉后缀 |
| `examples/` | 完整的示例扩展。`quicknotes/`：不改 Core、装到只含 Core 的 hub 上的功能示例（`docs/extensions/core-only-features.md`），直接引用旁边的 `go/`；不进 `bundle/sdk.tar.gz` |

生成的文件别手改：`wire` 改 `schema/extension/` 再跑 `python3 scripts/gen/extension_schema.py`（见 `schema/extension/README.md`）；
改了 `ts/` 或 `templates/` 跑 `python3 scripts/gen/sdk_bundle.py` 更新 `go/bundle/sdk.tar.gz`（`scripts/test/test_sdk_bundle.py` 会查）。
