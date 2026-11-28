# Execution spec: runtime, channel, model, effort

What an Agent or a conversation runs on has four dimensions:

| Dimension | What it is | Examples of where it comes from |
|---|---|---|
| **runtime** | the agent program that runs the turn | Claude Code (`claude`), Codex (`codex`), Antigravity (`agy`), `omp`, `pi`, a runtime plugin |
| **channel** | how that runtime reaches a model: credentials, base URL, protocol, device scope, usage source | a runtime's own sign-in on a computer, a managed gateway channel, a computer's own shell launcher |
| **model** | the model id the channel serves | whatever the channel lists |
| **effort** | the thinking level | whatever the model and runtime take |

`proto.ExecutionSpec{runtime, channel, model, effort}` carries them. Each dimension may be empty, which means "inherit"
(`ExecutionSpec.Inherit`): a conversation's pick over the model profile's tier over Settings › Model configuration
over the launcher's own defaults. The platform only declares capabilities: no channel name or model list is built in.

Server code: `server/internal/proto/execspec.go` (contract), `server/internal/assistant/execution_spec.go` (the
matrix, pure), `execution_decl.go` (the declarations the hub knows), `execution_channels.go` (managed channels, their
aliases and the ledger), `execution_project.go` (tiers and picks), `execution_lastgood.go`, `execution_routes.go`.

## Channels

A channel has a kind (`proto.ChannelKind`):

- `native`: a runtime's own sign-in on a computer (a subscription login). Named `native:<runtime>`; a computer has it
  when its launcher list shows the runtime's own launcher.
- `managed`: a gateway or API connection Persona keeps: one key, one set of headers, one proxy and one device scope,
  shared by every runtime bound to it. Kept in `execution.channels` (key and headers sealed, re-sealed by the key
  rotation). Every Persona-managed launcher of its own is also read as a managed channel of the same name.
- `external_shell`: a launcher a computer's own rc file defines. Named `shell:<launcher>`.

Native sign-ins and gateway channels coexist on one computer; usage is pooled per (channel, sign-in), so a gateway
channel never borrows a computer's subscription and the other way round.

A managed channel declares the protocols it serves (`endpoints`: `anthropic_messages`, `openai_responses`,
`openai_chat`, `gemini_native`, `acp_native`), its models (`canonical` id, efforts, context window, an optional
`quota_pool` name, and per protocol an edge: `native`, `bridged_stable` or `bridged_experimental`, with an optional
wire model id, effort transport and warnings), its usage source (`quota`: a gateway source and the `claude` or
`codex` provider) and its **bindings**: one per runtime, each naming the launcher alias that runs it.

### Launchers are aliases

Each binding is a launcher alias: `withChannelAliases` makes it a managed launcher on every read of
`launchers.managed`, so the older iOS and Web clients, the model configuration, `personad launch <alias>` and the
delivery to the computers (catalog, pull, scope, per-computer keys) keep working unchanged. Aliases are never stored
as launchers (`persistedLaunchers`), and the launcher routes refuse to edit or delete one on its own
(`err_alias_read_only`): it is changed through its channel. A managed launcher carries `channel` when it is an alias.

A save may fold existing launchers into the channel (`replace_launchers`): each must be a launcher of its own with a
binding of the same name. Its key, headers, scope and proxy seed the channel's when the save gives none.

### Changes never stop a running session

Saving, deleting or rolling back a channel only updates the records and their revision and tells the computers, as a
launcher's save does. No running session is stopped: new sessions, and each running one's next turn, use what was
saved.

### Ledger and rollback

Every change adds a rollback point (at most 10): the catalog as it was and the launchers the change folded in.
`POST /api/v1/execution/channels/rollback` restores one (none given: the newest). Restoring is itself a point, so it
can be undone the same way. Restored channels get a new revision, so a computer drops what it pulled of the version
they replace. The ledger is kept in `execution.channels.ledger` and `execution.channels.ledger_launchers` (sealed
fields re-sealed by the key rotation).

## Runtime declarations

Each runtime declares the protocols it speaks (preferred first), how it takes an effort (`cli_flag`, `env`,
`acp_config`, `model_suffix`, `none`), the efforts it takes, what a running session can switch for its next turn and
whether a managed launcher runs it (only those can be bound to a managed channel). The built-in adapters' declarations
are in `builtinRuntimeCaps`; the `assistant.execution` setting adds or replaces them by runtime name (a runtime
plugin's, or a correction).

## Computer readiness

What a computer has is what the hub last saw there, without asking the node: the runtimes and channels of its
launcher list, and the managed channels whose device scope includes it. A computer that has not synced yet is
`known: false`.

## ValidateCombination

`execDecl.judge(spec, machine)` intersects the runtime's protocols, the channel's supply for the model and the
computer's readiness. A verdict (`proto.CombinationVerdict`) has a level:

- `allowed`: a native or stably bridged combination with nothing to say; runs silently.
- `notice`: runs, with a gentle note: `bridged_experimental` (labelled "compatibility bridge (experimental)" and never
  `recommended`), a bridge warning, an effort clamped to one the model takes or ignored by the runtime, a model the
  channel does not list, a computer offline or not synced yet.
- `blocked`: does not run. `reasons` say why in plain words (in the request's language), `alternatives` list up to
  three combinations that run the same model and effort.

Blocking reasons: `runtime_unknown`, `channel_unknown`, `no_common_protocol`, `model_not_offered`, `machine_unknown`,
`runtime_not_ready`, `channel_not_ready`, `channel_out_of_scope`, `no_launcher`. The verdict also says the protocol,
the wire model and effort the runtime will send (a model whose efforts are listed as `<model>-<effort>` ids beside
`<model>` takes the effort as the id suffix) and the launcher alias that runs it.

`execDecl.resolve` first fills an empty runtime or channel with the best runnable alias (silent before noticed, then
by edge tier).

## Records that name a launcher

A model profile's tier and its candidates, and a conversation's pick, keep `launcher` and may also keep `runtime` and
`channel`:

- Given without a launcher, a save fills the alias that runs them (`projectTiers`, `projectPick`); a combination that
  can't run is refused with the blocking reason.
- Given with a launcher, the launcher wins.
- At dispatch, a candidate that keeps a runtime and channel whose launcher no longer runs them (an alias renamed, a
  channel rebound) runs on the alias that does now (`reprojectCandidates`).
- A record with neither is read and written exactly as before.

## Usage source

A launcher's usage pool (`gatewayPool`) is its admission group's `quota_pool` when it has one (the highest-priority
override), else the `quota` of the channel it is an alias of. Candidate headroom then counts only that source's
accounts, and the model under the channel's `quota_pool` name for it when it declares one.

## Last known good

When a turn ends without an error on a launcher, the conversation's execution is kept as its last known good
(`assistant.last_good_specs`, at most 512, the same one rewritten at most hourly). `POST …/model/restore` makes it the
conversation's pick with every check a pick gets: like any pick, it stops nothing that runs and the next message runs
on it.

## Routes

| Route | Body / answer |
|---|---|
| `GET /api/v1/execution/channels` | `ChannelCatalogView {revision, channels: [ChannelConfig], rollbacks: [ChannelRevision]}`, never a key or headers (`has_key`, `has_headers`) |
| `PUT /api/v1/execution/channels/{name}` | `ChannelConfig` plus optional `revision` (409 when the catalog moved on); `{ok, channel, revision}`. Full role. |
| `DELETE /api/v1/execution/channels/{name}?revision=` | `{ok, revision}`; 404 for an unknown channel. Full role. |
| `POST /api/v1/execution/channels/rollback` | `{revision?}`; `{ok, revision}`; 404 with no such point. Full role. |
| `GET /api/v1/execution/catalog?machine=` | `ExecutionCatalog {runtimes, channels, machines, aliases, combinations?}`; with `machine`, every alias's verdict there |
| `POST /api/v1/execution/validate` | `ExecutionValidateRequest {spec, machine?}` → `CombinationVerdict` |
| `PUT /api/v1/assistant/conversations/{id}/model` | also takes `runtime` and `channel` (with or without `launcher`); `model_pick` carries them back |
| `GET /api/v1/assistant/conversations/{id}/model/last-good` | `{last_good: LastGoodSpec \| null}` |
| `POST /api/v1/assistant/conversations/{id}/model/restore` | `{ok, model_pick, last_good}`; 404 when none is kept. Full role. |

`PUT /api/v1/model-profiles/{id}` takes `runtime` and `channel` on a tier and on each candidate.

## Web

The Web reads these routes as described in `docs/web/web.md` "Four dimensions in the Web (#17524)": the main chat's
panel offers model and effort from the recommended combinations, the route folds out, Settings › Models has the Engines
and Channels cards, and New Agent and presets show each combination's verdict.
