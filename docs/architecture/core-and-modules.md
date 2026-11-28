# Persona Core and feature modules

Who this serves: people who change Persona's code, on any platform. Nothing here is visible to someone using Persona:
with every module on (the default) the product is what it was.

Persona is split into **Persona Core**, the parts everything else stands on, and **feature modules**, optional features
that reach the core only through one declared contract. The core never imports a module, so a module can be left out of
a build, switched off without one, or tested on its own. Decisions and stages: #13430 (tracking #13411); the server
half: #13408.

## What Persona Core is

"Persona Core" is a product-layer term. It holds (#13430 section 1):

- the Assistant main chat, with its memory (the knowledge-base files and search, the `kb_*` tools) and composer dictation;
- Agents: handing work to coding Agents, supervision with its follow-up scheduler, and `claudeplugin`;
- To-dos and the task board;
- Computers: machines, Link, the node with Claude Code, Codex and herdr;
- Inputs: the connector and channel framework, and the Inbox shell;
- hub plumbing (transport, auth, push, pairing), the extension host, and the shell mechanics (navigation, the settings
  shell, the design system);
- the north-star metrics, the yardstick of the core flow.

The membership test: if the owner can say "I never use this" and the assistant, Agents, to-dos, computers and connectors
still work, it is a module. The first set (voice, recording, lark, mail, health, briefing, ...) is in #13430 section 2.

No code is renamed for the term. The names that already say "core" mean something else:

| Name | What it is |
| --- | --- |
| `apple/Packages/PersonaCore` | The Apple binding of the shared Rust client library; it keeps its name. The Apple layer for Persona Core is `PersonaHostApp` |
| `native/app-core` | The shared Rust client logic every Persona Core client depends on; module-specific domains never go into it |
| `android/core/*` | Android's infrastructure Gradle modules |
| `server/internal/herdrcore` | An engine library |

## The module contract

One manifest concept, with the same field names on every platform: `schema/module.schema.json`. A module's manifest says
who it is (`id`, `api`, `origin`: `builtin` or `extension`, `title`), what it depends on (`needs`: must be loaded; `wants`:
used when loaded), and one list per kind of slot it adds to: `feature` (a wire feature flag), `hub_routes`, `mcp_tools`,
`prompt_sections`, `settings`, `shell_places`, `chat_card_kinds`, `composer_actions`, `chat_accessories`,
`background_jobs`, `schedules`, `db_migrations`, `i18n_namespaces`, `node_programs`, `channel_kinds`, `frame_handlers` and
`push_categories`. A module adds nothing it does not declare, and a host refuses a contribution the manifest does not
name. Where an extension point exists or is filed, the slot uses its name (#13400 to #13403), so built-in modules and
extensions share one registry shape and a module can later become an extension.

**A disabled or excluded module is absent.** That is the contract: its routes answer 404, its tools and feature flags
are not announced, it runs no loop, its settings keys stay stored but are hidden, and its tables stay. The core without
modules is a tested product.

What the server enforces today is only the slots it serves (see "Slots" below): a module's settings are hidden but
stay stored, its routine facts are gone (the routine opens with none), and its tables stay, since no module owns one
yet. A module that declares a slot the host does not serve is not loaded.

## Classification

`schema/module-classification.txt` (#13843) is the one list of what is Persona Core and what belongs to a module. It
classifies every unit as `core` or a module id: each Go package directory under `server/` and `sdk/go/`, each Apple
directory `apple/<name>`, `apple/Persona/<name>` and `apple/Packages/<name>`, each Gradle project
`android/settings.gradle.kts` includes, and each directory under `web/src` one and two levels deep. A unit that is a
module's future home gets the module id even while it still lives in a core path (`server/internal/lark` is `lark`), so
the list shows how far each module is from its own directory.

One line per unit, `<kind> <unit> <core|module id>`, with `module <id>` lines declaring the ids (#13430 section 2 plus
the modules already built). The most specific line wins. `<dir>/... <id>` gives a whole subtree to a module, and the
module roots (`server/internal/modules/*/...`, `web/src/modules/*`, `:module:*`) give each directory below them the
module its name says. Core is never inherited: each core unit has its own line, so nothing becomes core without
someone writing it down. A line may also name a directory deeper than the units (`apple/Persona/Views/Mail mail`).

`scripts/test/test_module_classification.py` (run by `scripts/ci/scripts.sh`) fails on a unit no line classifies and on
a line that matches nothing. To add a package, directory or Gradle project, add its line; to move one into a module
root, delete its old line (the root's line classifies the new place); `--print` lists every unit with its class. The
Apple module ratchet (`scripts/test/test_apple_module_refs.py`) takes the directories of the modules it guards from the
file; the server, web and Android guards still key on their module roots.

## Server

| Piece | Where | What it is |
| --- | --- | --- |
| Module API | `server/internal/modapi` | Pure types: `Info` (the manifest), `Module` (`Info()` + `Register(Host)`), `Host` (everything a module may use of the core) and the contribution types. A contract package (`server/internal/archguard/testdata/contracts.txt`) |
| Modules | `server/internal/modules/<id>` | One package per module, with its `module.json` (embedded: `Info()` reads it). No nested Go modules |
| Composition root | `server/internal/modules/all` | The one list of built-in modules, one file per module with its build constraint. `internal/manager`, `internal/managercli` and the binaries `cmd/personad` and `cmd/persona-manager` link it |
| Provider seams | `server/internal/builtinext` | What the core needs of a module that runs as a built-in extension program, declared by the core and provided by the composition root (see "Provider seams") |
| Host | `server/internal/assistant/kernel` | Loads the modules compiled in and switched on, serves `modapi.Host` to each, and answers the core's questions about what they added |

The core asks the kernel (`s.modules` in package assistant, `server/internal/assistant/modules.go`), never a module's
package. A module may later keep its own prompts in its own tree, under the same rules as `server/internal/prompts/`
(#13430, decision 5); none does yet.

### Loading

The manager loads modules once, in `assistant.New`, before `New` returns. Core routes are registered first so their real
patterns reserve the core namespace. Module registration finishes during construction; background jobs start only with
`Service.Run`. The kernel leaves out a module that is
switched off, is written for another `api`, is not `builtin`, declares a slot the host does not serve yet, has a feature
that is not exactly `module.<its own id>`, has a route or tool that conflicts with the core or an already accepted
module, needs a module that does not load (wherever it stands in the list; a cycle of needs loads none of them), or whose
`Register` fails. A module registers after the modules it needs and after those it wants that load; otherwise list order
holds. `wants` never keeps a module from loading. An id listed again is ignored after its first entry. A module left out
adds nothing, including what it added before failing; why is kept (`Kernel.Skipped`) and logged.

### Slots

`modapi.Info` declares every slot of the contract so manifests can be written against the whole of it. The server
serves only what the pilot needs so far; a module that fills any other slot is not loaded, so nothing it expects goes
missing quietly.

| Slot | Served | How |
| --- | --- | --- |
| `settings` | Yes | `Host.AddSetting(modapi.Setting)`: the module says what a value means (`Value`, `Check`, `Describe`); the core keeps it stored, lists it in `persona_settings` and asks the owner to approve a change |
| `feature` | Yes | The manifest value is `module.<id>`. While the module is loaded it joins the manager hello's `features`; the hub forwards it while that default manager is connected. Every built-in module declares it, and the manager announces the client-only modules' flags itself (below) |
| `hub_routes` | Yes | `Host.AddHubRoute(pattern, handler)`: the assistant serves the declared `METHOD /api/v1/…` route only while the module is loaded |
| `mcp_tools` | Yes | `Host.AddMCPTool(modapi.MCPTool)`: the assistant lists and dispatches the declared tool only while the module is loaded ("Assistant tools" below) |
| `background_jobs` | Yes | `Host.AddBackgroundJob(name, run)`: `Service.Run` starts the declared loop under its context; an absent module starts none |
| `schedules`: `facts` | Yes | `Host.AddScheduleFacts(modapi.ScheduleFacts)`: the wait rules and `ScheduleFactsRunner` for scheduled routines with facts or reports, using narrow `Host` ports (`Tasks`, `Todos`, `Inbox`, `Later`, `Schedule`, `Feed`, `Delivery`, `DayRecap`, `Prompts`) |
| `schedules` (others), `prompt_sections`, `db_migrations`, `i18n_namespaces`, `node_programs`, `channel_kinds`, `frame_handlers`, `push_categories`, `shell_places`, `chat_card_kinds`, `composer_actions`, `chat_accessories` | Not yet | Declared but not served: each gets a `Host` method when its first module moves (#13408 remaining work, #13400 to #13403) |

Route validation uses the real `http.ServeMux` conflict rules, including equivalent wildcard patterns, against the core
and previously accepted modules. The reserved tool names include conditional core tools and compatibility aliases. A
conflict rejects the module's complete contribution before the live mux changes; core tool dispatch remains authoritative.

**Assistant tools.** A module's tool is a `modapi.MCPTool` (`server/internal/modapi/tool.go`): the name its manifest
lists in `mcp_tools`, the description and input schema the model is shown, and a handler. Build it with `modapi.Tool`
and an argument struct, so the contract decodes the model's arguments once and strictly before the handler runs:

```go
type remindArgs struct {
	Text string `json:"text"`
}

err := h.AddMCPTool(modapi.Tool("module_remind", "Remind the owner of something.", modapi.ToolSchema(remindSchema),
	func(ctx context.Context, call modapi.ToolCall, a remindArgs) (modapi.ToolResult, error) {
		return modapi.ToolText("Reminder saved for " + call.Machine.String() + "."), nil
	}))
```

- `ToolCall` says who called: `Machine` (`wireids.MachineID`) and `Session` (`wireids.SessionID`, the calling Agent
  session on that machine: an assistant conversation's session or another Agent's that was offered the tool; empty
  when the core calls the tool with no session behind it), and `Args`, the model's arguments as sent.
- An argument the struct does not declare, a value of the wrong type or anything after the object fails the call with
  `invalid arguments: …` before the handler runs; a call with no arguments decodes an empty object. Keep the struct's
  `json` tags and the schema's properties the same. Never decode into a map.
- `ToolResult.Text` reaches the model as it is; a failure is the handler's error, whose text the model sees as the
  tool's error. `TestModuleToolWireIsPinned` (`server/internal/assistant/modules_tool_wire_test.go`) pins the
  definition, the output and the error as the model receives them.
- Raw JSON crosses this slot in two named types only: `ToolSchema` (the JSON Schema text shown to the model, which the
  core never reads) and `ToolArgs` (the model's arguments on their way to the tool's decoder). A tool that must read
  its arguments itself fills `MCPTool.Run` and reads `call.Args`.
- `AddMCPTool` refuses a tool the manifest does not name, a tool without a handler and a tool added twice.

A manager that publishes module flags also announces the reserved `modules` marker, including when every module is off
or built out. Clients treat a module with a `feature` as off only when the hello has `modules` and lacks that feature;
no hello, or no `modules` marker from an older hub, leaves it on. This keeps the default behavior on older hubs. The
marker and the module flags disappear from the hub hello when their default manager disconnects. Extension reports cannot
publish `modules` or anything in the `module.` namespace.

Because a missing flag means off, every flag a client gates on has to be announced by a publishing manager:

- **Every built-in module declares `feature`** (`module.<id>`), also one that registers nothing with the kernel
  (`voice`, `volc`, `mail`): `TestEveryBuiltinModuleAnnouncesItsFlag` in `server/internal/modules/all`. Built out or
  switched off, its flag is gone and the clients hide their half.
- **Client-only modules** (`modapi.ClientModules`: `glasses`)
  have no server half, so the manager announces their flags itself. The `modules` settings row switches one off the
  same way (`{"disabled": ["health"]}`), which is how the owner turns a client module off from the hub without a new
  build. An id leaves the list in the PR that gives the module a server half (`TestNoBuiltinModuleIsListedAsClientOnly`).
  A client that starts gating a new module adds its id here in the same PR, or the module disappears on current hubs.
  `scripts/test/test_module_flags.py` (run by `scripts/ci/scripts.sh`) reads the ids the web, Android and Apple modules
  gate and fails on one no hub announces.

### The switch

- **At build time**: the build tag `persona_without_<id>` leaves one module out (a `-` in the id is `_` in the tag, which
  Go's build constraints need: `persona_without_screen_share`), `persona_core_only` leaves every module
  out. `go build -tags persona_core_only ./...` must pass. `scripts/ci/server.sh build` builds both and runs, with each
  tag, the module and kernel tests and a `-run` subset of `internal/assistant` (Brief, Module, Setting, Routine, Facts,
  ScheduledConfiguration, NorthStar, Accept, Later, Golden): the assistant's tests of the briefing itself skip
  (`needsBriefingModule`), and `TestBuildWithoutBriefingLoadsNone` checks the core without it. Every server package
  passes with `persona_core_only` too: the `core-node`, `core-managers` and `core-other` lanes run the whole suite
  with it ("How to build and run the core alone" below).
- **At run time**: the settings row `modules` (the `settings` table, as every other setting; no state file) holds
  `{"disabled": ["<id>", …]}`. `persona_settings` lists and reads this row as the `modules` key. A set rejects unknown
  or duplicate ids (known are the compiled-in modules, the client-only ones of `modapi.ClientModules` and the ones
  installed extensions may supply, `modapi.ExtensionModules`, on any build) before showing the existing owner approval card; `null` or an empty disabled list
  restores the default. The manager reads the row once where it starts (`server/internal/assistant/main.go`,
  `ReadModuleSwitch`) and hands it to `assistant.New` as `Options.ModulesOff`, so the saved change takes effect at the
  next manager start, never as a hot switch in the running process. The manager reads it before it makes the extension
  manager, and the kernel and every provider seam get the same list (below). A built-in module switched off is absent
  exactly as one left out of the build; a client-only one loses its hub flag, so the clients hide it. There is no settings screen yet; the settings registry of #13395 remains its intended UI home.
- **One predicate for the seams** (#14405): `server/internal/moduleswitch`. A module that reaches the core through a
  provider seam (a port its file in a composition root fills in `init`, "Provider seams" below) is switched off by that
  port: the port is a `moduleswitch.Port`, which keeps the provider under the module's id and hands it out (`Get`)
  only to a caller whose `moduleswitch.Set` has the module on (`Set.On`). The `Set` is a value each manager carries
  (`extmgr.Options.Modules`), never process state: one process can run several managers (`internal/manager`). A node
  has no settings row; it reads the switch from the hub's features it gets in `node.configure` (`hub_features`), by
  the clients' rule (`moduleswitch.FromFeatures`: with the `modules` marker, a module whose `module.<id>` flag is
  missing is off), and keeps it for the process (a node serves one hub). Before its first `node.configure`, or from a
  hub without the marker, every module is on.
- **Tested for every module**: `TestEverySeamFollowsTheSwitch` in `server/internal/modules/all` and in
  `server/internal/modules/all/node` runs `server/internal/moduleswitch/switchtest` over the root: every seam each
  module of the build recorded must hand out its provider with the module on and nothing with it switched off, and every
  call the root's `init` makes must be such a seam. The only calls allowed otherwise are the root's own
  (`kernel.Provide`) and the binary-only ones below; an entry no longer called fails.
- **Checked on a running hub**: `python3 scripts/ci/core-only-smoke.py --switched-off <ids>` builds by default, starts
  a hub on a disposable Postgres, writes the row, restarts and checks the ids are gone (the manager's loaded modules,
  their flags, programs' extensions, connectors, routes) while every other module stays, then clears the row,
  restarts and checks they return.

What switched off means, by seam (each port follows `Set.On`; a build without the module provides nothing at all):

| Seam | Module | Switched off at the next manager start |
| --- | --- | --- |
| Kernel slots (`settings`, `feature`, `hub_routes`, `mcp_tools`, `background_jobs`, schedule facts) | every module | Not loaded: no setting, route, tool or job, and its `module.<id>` flag is gone from the hello |
| `server/internal/extmgr/githubport` | `github` | No built-in GitHub connector: not registered, listed or polled; `/api/v1/connectors/github/auth` answers 404 |
| `server/internal/extmgr/imessageport` | `imessage` | No Messages connector or built-in `imessage` extension; `/api/v1/imessage` answers that it is not configured |
| `server/internal/extmgr/mailport` | `mail` | Mail is not opened: the mail routes answer 404, no `mail_*` tool or mail connector, no `mail.accounts` setting |
| `server/internal/builtinext` (`ProvideVolc`, `ProvideVoice`) | `volc`, `voice` | The built-in extension is not registered or started; no meeting transcribes with it |
| `server/internal/builtinext` (`AddProgram`) | `volc`, `voice` | The extension host starts no `personad volc` / `personad voice`, a `hub.json` entry's included (`ProgramOff`: it fails as the unknown subcommand would). The subcommand stays in the binary |
| `server/internal/botport` | `lark` | No bot client is made: the bot cannot be bound, registered or started |
| `server/internal/cliport` | `lark` | `lark.cli` is not read (logged at start), the extension host's lark-cli integration is off and the knowledge base's Lark import (`kb.lark_minutes`) logs that and skips its runs |
| `server/internal/larkhost` | `lark` | The outbound channel port is `larkhost.None`, as without the module; the core's pushes to the chat return `nil` without a log or a retry, and `notify_user`'s copy report records the Feishu copy as `skipped` (told nowhere in its answer) |
| `server/internal/knowledgeuihost` | `knowledge-ui` | No layer: no `kb_history_*` tool or import, the selection page's chat list answers as a source without history, days carry no sent marks |
| `server/internal/healthhost` | `health` | No layer: the intake and items routes answer 404, no `health_intake` tool, no `health_intake` or `health_items` capability, no water reminders |
| `server/internal/recordinghost` | `recording` | No layer: the recording routes answer 404, no audio socket, room tools, final passes or meeting capabilities |
| `server/internal/node/imessageport` | `imessage` | The node answers as a computer without Messages; the manager sends it no `connector.*` call and drops `imessage` from the node's built-in programs |
| `server/internal/node/iphoneport` | `phone-extra` | The node lists no `iphone` program and has none of its methods; the manager drops `iphone` from the node's built-in programs, so it offers and calls no phone control |
| `server/internal/node/screenport` | `screen-share` | The node offers no screen session: a live screen is the plain relayed stream; the clients hide screen sharing (flag gone) |
| `server/internal/nodecmd/commandport` (`AddCommand`) | `office`, `phone-extra` | **Binary-only**: `personad office` / `personad iphone` run as their own process with no hub, so they stay until a build without the module. The `persona-office` skill still ships to agents, as it does in a build without `office` |

A node learns the switch at its next `node.configure` (on every connection): its first `node.info` is answered before
that, which is why the manager filters the node's built-in programs itself. Client-only modules (`modapi.ClientModules`)
have no seam; their flag goes and the clients hide them.

### A module that is off keeps what the user has

Switching a module off, at run time or by leaving it out of the build, **must not hide or change data the user already
has** (the owner's rule for every module move, #13430). "Absent" above is about the module's routes, tools, jobs and
entries, never about what it wrote earlier:

- **Reading existing data is Core.** Listing, opening and searching what is on disk or in the database keeps working
  with the module off, and the search index keeps indexing those files. The read path never goes through a module's
  provider.
- **The write side is the module's.** Creating, uploading, transcribing, summarising, the background rounds and the
  entry points go with the module: with it off they do nothing, and no stored file is touched.
- **Off never deletes and never rewrites.** Deleting stays the user's explicit action, and the retention the user
  configured stays the user's setting: neither is a side effect of the switch. State that was in flight when the
  module went is shown as stored and marked in the response only (below), so the module takes it up from the same
  stored fields when it is back.
- **A reviewer checks the off case for "old data still there, unchanged"**, not only "entry gone":
  `scripts/ci/core-only-smoke.py` and `scripts/ci/module-e2e.py` start their hubs on a knowledge base that already
  holds recordings and check exactly that ("Recording" below).

Recordings are the first application ("Recording" below, `docs/plans/plan-kb-recording-port.md`).

### How to build and run the core alone

Persona Core is a product on its own: a hub built without any module starts, serves and passes the core's tests.

- **Build**: `cd server && go build -tags persona_core_only -o ../bin/ ./cmd/personad ./cmd/persona-hub ./cmd/persona-manager`.
  One module left out: `-tags persona_without_<id>`.
- **Run**: the core-only `persona-hub run` (or `personad hub run`) with a `hub.json` as for any hub (docs/operations/config.md);
  keep `persona-manager` beside `persona-hub` and the hub starts it. The manager's log says `modules: loaded none`
  (a default build: `modules: loaded briefing, voice, volc`, …).
- **Install**: a release has the core alone as install packages beside the full ones (the core edition:
  `core-latest.json`, `core-personad-<os>-<arch>.gz`, …), `install.sh --core` installs them, and an install keeps its
  edition through every update. [core-install.md](../operations/core-install.md) is the document;
  `python3 scripts/ci/core-install.py` follows it in a clean container, from the packages alone.
- **Smoke test**: `python3 scripts/ci/core-only-smoke.py` builds the three programs with `persona_core_only`, starts a
  disposable `postgres:18-alpine` container on a private port, writes a `hub.json` in a temporary home and runs
  `persona-hub run` with a clean environment (no `PERSONA_HOME` of the machine's own hub). It checks health, an
  authenticated request, the manager's `modules: loaded none`, that every module's program (`builtinext.AddProgram` in
  `internal/modules/all`) is an unknown subcommand of `personad` and `persona-manager`, that its extension is not in
  `/api/v1/extensions` and `/api/v1/extensions/<id>/-/assistant-data` answers 404, and a clean SIGTERM shutdown, then
  prints one PASS / FAIL / TIMEOUT line. `--full` runs the same on a default build and expects the opposite. `DOCKER`
  names another Docker client.
- **End-to-end gate**: `python3 scripts/ci/module-e2e.py --modules <ids>` switches the modules off and on at run time
  (the `modules` row) on a test hub started the same way, with a default build and the real web app in headless
  Chrome: flags, the manager's list, the module's routes, extensions and connectors, its web entry and chunks, and the
  core's pages in every phase, plus the mock hub as an older hub. Off, a module's entry points are gone, and data the
  user already has is asserted the other way: a stored recording is still listed and opens read-only in the web app,
  with none of the module's controls and none of its chunks. A module move merges only after it passes on its
  branch (docs/operations/testing.md "Module e2e gate", #14417).
- **Tests**: `GOFLAGS="-trimpath -tags=persona_core_only" python3 scripts/ci/server-shards.py <node|managers|other>
  --database-file <file with database_url>` runs a shard's every package without modules; a test of a module's own
  behaviour skips without it (`needsBriefingModule`, `needsVoiceModule`, `needsVolcModule`). Locally the `other` shard
  needs Bun 1.4.2 on `PATH`, as in CI.
- **CI** (`scripts/ci/server.sh`): the `core-other` lane runs 'Core-only install from packages'; the `build` lane runs 'Core-only hub smoke' and 'Builds without each module' (each
  `server/internal/modules/<id>/module.json`: `go build -tags persona_without_<id> ./...` and the module list and guard
  tests, so a module that moves is covered with no edit); the `core-node`, `core-managers` and `core-other` lanes (and
  `all`) run 'Server <shard> tests with Postgres, core only'. Measured on the hub machine (64 cores, shared): node
  about 5.5 minutes, managers about 6, other about 3.5.
  The per-module steps ('Builds without the … module(s)', 'Core-only and module-less builds') run in the `node`,
  `managers`, `core-node` and `core-other` lanes, one or two each, not in `build`: together they outgrow a hosted job's
  20 minutes. Each runs only its own `persona_without_<id>` tests (#14412): the module's `go build ./...` and the module
  list and guard tests are 'Builds without each module', and every package's tests with `persona_core_only` are the
  core lanes' (the node step keeps the one `persona_core_only` build, Windows' node included). Where a module section
  below names `persona_core_only` among a step's tags, the core lanes run those tests.
- **Size**: `python3 scripts/diag/core_size.py [--no-build] [--json] <ref> …` prints the core's non-test Go lines (every
  `*.go` under `server/` that is not `_test.go`, `server/internal/modules/` left out), each module's lines, and the
  stripped sizes of `personad`, `persona-hub` and `persona-manager` built by default and with `persona_core_only`
  (`go build -trimpath -ldflags '-s -w'`, the same for both). Read from `git archive`, so no checkout changes.

| Ref | Core non-test lines | Module lines | personad default / core-only | persona-hub | persona-manager default / core-only |
| --- | --- | --- | --- | --- | --- |
| `9fa299334` (before modules moved) | 842,284 | briefing 173 | 118,911,239 / 118,898,951 B | 40,755,463 B | 100,917,511 / 100,909,319 B |
| `2da914f99` (voice and volc moved) | 825,432 | briefing 173, voice 15,132, volc 3,259 | 119,030,023 / 117,809,415 B | 40,792,327 B | 101,007,623 / 99,791,111 B |

`persona-hub` has no module in it, so both of its builds are the same size.

### Provider seams

Some modules are not code the manager runs but a built-in extension's program: `volc` is `personad volc` and `voice`
is `personad voice`, which the extension host (`internal/extmgr`) starts and which reach the hub only through its API. The core needs two things of
such a module, and it declares them in `server/internal/builtinext` in its own terms, never as the module's types:

- the program a subcommand runs (`builtinext.AddProgram`, `LookupProgram`; `personad`'s usage lists the programs the
  build has);
- what the extension host needs of the extension: for `volc` its ids and its form's choices (`builtinext.Volc`); for
  `voice` its transcription contribution's id and how its settings merge, which the host reads to say what the
  extension lacks (`builtinext.Voice`).

The module's file in `server/internal/modules/all` provides them in its `init`, under the module's build constraint.
With the module left out nothing is provided: the subcommand is unknown and the extension host registers no such
extension, exactly as before the extension existed (`TestNoVolcWithoutItsModule`, `TestNoVoiceWithoutItsModule`). Such a module registers nothing
with the kernel yet; its `module.json` declares no slot. `scripts/ci/server.sh build` builds and tests the binaries,
`builtinext`, the modules, the guards, the extension host's tests of them and the manager's end-to-end meeting
transcription tests with `persona_core_only`, `persona_without_volc` and `persona_without_voice`.

Mail (`server/internal/modules/mail`, #13936) is the other kind: a service the extension manager runs in process
(the `/api/v1/mail/` routes, the `mail_*` tools and the mail connector, the Inbox's mail, the `mail.accounts` setting).
The manager reaches it through the contract `server/internal/mailwire` (the values both sides pass and the `Service`
interface; the module aliases them) and the port `server/internal/extmgr/mailport`: the module's file in
`server/internal/modules/all` provides `mail.Open` there in its `init`, and the manager opens mail with it when it runs.
The port is a package of its own so the composition root need not import `extmgr`. With the module left out
(`persona_without_mail`, `persona_core_only`) nothing is provided and the manager runs as with a mail service that
failed to open: the mail routes answer 404, no `mail_*` tool or mail connector is offered, `persona_settings` has no
`mail.accounts`, and everything else runs (`TestManagerWithoutMail`). The module registers nothing with the kernel and
its `module.json` declares no slot; the run-time switch (the `modules` row) covers it through `mailport` ("The
switch"). Its prompts (`server/internal/prompts/mcp/mail`) and locales (`mail-*`) stay in
the core's trees until `prompt_sections` and `i18n_namespaces` are served. `scripts/ci/server.sh build` builds and
tests the core, the modules, the guards and the manager's mail, inbox, connector and settings tests with
`persona_core_only` and with `persona_without_mail`.

The GitHub connector (`server/internal/modules/github`, #13936) is a connector the extension manager registers. It is
written against the connector SPI, the contract `server/internal/connectorwire` (the `Connector` interface, its
`Manifest` and capability interfaces; `internal/connector`, the framework that runs connectors, aliases them), and the
manager reaches it through the contract `server/internal/githubwire` (manager.json's `github` section, the token
source and its errors, the CI-check snapshot, and the `Connector` interface of the methods the manager calls) and the
port `server/internal/extmgr/githubport`, which the module's file in `server/internal/modules/all` fills with
`github.Provider`. The assistant's CI checks read `githubwire.PRCheckSnapshot` from the manager, so they need no
module. With the module left out (`persona_without_github`, `persona_core_only`) the manager registers no built-in
GitHub connector: it is not listed, `/api/v1/connectors/github/auth` answers 404, no `github_*` tool is offered, and
an extension's GitHub connector can still take the id (`TestManagerWithoutGitHub`). Its prompts
(`server/internal/prompts/mcp/github`, `prompts/inbox/kinds/github.md`) stay in the core's trees as mail's do.
`scripts/ci/server.sh build` runs "Builds without the connector modules" with both tags, and
`scripts/ci/core-only-smoke.py` checks that a core-only hub lists no GitHub connector.
Its `module.json` declares `feature` = `module.github`, so a hub whose manager loaded the module announces it.
Switched off in the `modules` row, `githubport` hands the manager no factory, so the connector is absent as in a build
without it (`TestManagerWithoutGitHub` runs both ways).

The iMessage connector (`server/internal/modules/imessage`, #13936) is the same kind of connector, with a second side:
the reader of a Mac's Messages runs in that Mac's node. The manager reaches the connector through the contract
`server/internal/imessagewire` (the id, manager.json's `imessage` section, the `Caller` and its errors, the `Remote`
interface it drives) and the port `server/internal/extmgr/imessageport`, which `server/internal/modules/all` fills.
The node reaches the module's handler of the hub's `connector.*` RPCs (`imessage.ServeLocal`) through the port
`server/internal/node/imessageport`, which the node's composition root `server/internal/modules/all/node` fills;
`server/internal/nodecmd` links that root (it is listed in `testdata/module_roots.txt`, and a root may import either
list), so `personad node`, persona-node and the manager's local node all have it, and persona-node still links no part
of the manager. With the module left out (`persona_without_imessage`, `persona_core_only`) the manager has no Messages
connector or built-in Messages extension, `/api/v1/imessage` answers that it is not configured
(`TestManagerWithoutIMessage`), and the node answers as a computer without Messages does. Its `module.json` declares
`feature` = `module.imessage`. Switched off in the `modules` row, both ports hand out nothing: the manager registers no
Messages connector or extension (`TestManagerWithoutIMessage` runs both ways), so it sends the node no `connector.*`
call, and a node whose hub announces no `module.imessage` answers as a computer without Messages. "Builds without the connector
modules" in `scripts/ci/server.sh` runs with `persona_without_imessage` too, and `scripts/ci/core-only-smoke.py`
checks that a core-only hub has no `imessage` extension.

The `lark` module (#13404) is of the same kind as mail: code the manager runs in process, which the extension host
drives. The core declares what it needs of the Feishu / Lark bot's client in `server/internal/botport` (the client's REST calls,
the long connection, the device-code registration, as interfaces over the pure types of `server/internal/botcard`, a
leaf library the core's cards are built with), and `server/internal/modules/all/lark.go` provides it from
`server/internal/modules/lark/feishubot`. The lark-cli adapter and its connector (`server/internal/modules/lark/larkcli`,
`server/internal/modules/lark/connector/larkcli`) are reached the same way through `server/internal/cliport`, with their
values in the contract `server/internal/cliwire`; the connector builds on the connector SPI (`connectorwire`). Without the
module `botport.Available()` and `cliport.Available()` are false: the bot cannot be bound, registered or started (the
setting answers that it is not part of this build), `lark.cli` is not read (a warning at start), the knowledge base's
Lark import (`server/internal/assistant/kblark`, through the same runner) skips its runs, and the extension host's and
the import's tests of them skip (`needsLarkModule`, `needsLarkCLI`). `scripts/ci/server.sh build` builds and tests with `persona_core_only` and with
`persona_without_lark`. The assistant's own Feishu layer reaches the module through `server/internal/larkhost` ("Lark" below).

### The node's modules

Some modules run only on the computers: their code is part of the node (`persona-node`, and `personad` or
`persona-manager` running a node or its device commands). The manager's list (`internal/modules/all`) cannot hold
that code, since `persona-node` must not link the manager's modules (its `TestPersonaNodeDoesNotLinkTheHubOrTheManager`
refuses `internal/modules/all`), so it goes into the node's composition root, `server/internal/modules/all/node`, which
`server/internal/nodecmd` links (see the iMessage connector above). Each module adds a file there under its build
constraint (`!persona_without_<id> && !persona_core_only`) that fills the node's ports, each declaring what the node
needs in its own terms:

- a device command: `server/internal/nodecmd/commandport` (`AddCommand`, `LookupCommand`). `nodecmd` runs a command
  it does not know itself if a module provides it, and its usage lists the provided commands (`CommandUsage`).
- a live screen's sessions (#6398): `server/internal/node/screenport` (`Provide`, `New`, the `Links` and `Session`
  interfaces). Without them the node offers no session, and a live screen is the plain relayed stream the phone gets
  when it asks for none.
- the built-in program `iphone` (phone control): `server/internal/node/iphoneport` (`Provide`, `Get`, the `Bridge`
  interface of its methods over `proto` types). Without it `node.info` lists no `iphone` program, so the hub offers no
  phone control on that computer, and the program's methods are unknown.

Such a module keeps its manager side small: the module's root package (`internal/modules/<id>`) holds only its
manifest and a `modapi.Module` that registers nothing, and `internal/modules/all` lists it, so a hub announces its flag
(`module.<id>`) when the manager has it. The node half lives in a sub-package (`internal/modules/<id>/<pkg>`) that only
the node's root imports, so the manager does not link it. With the module left out nothing is provided: the command is
unknown and missing from the usage (`TestOfficeFollowsTheBuildTags` in the node's root), and
`scripts/ci/core-only-smoke.py` builds `persona-node` as well and checks that a core-only `personad` and `persona-node`
lack every command the node's root provides. `scripts/ci/server.sh build` runs "Builds without the node modules" with
each such module's tag and `persona_core_only`, Windows' node included. Switched off in the `modules` row, the node's
ports hand out nothing once the node has the hub's features, and the manager drops the module's built-in programs
from the node's info; a device command stays in the binary ("The switch").

The modules of this kind:

- `office` (`server/internal/modules/office`, #14258): `personad office …`, the Office files generator
  (`docs/assistant/office.md`), in `internal/modules/office/office`.
- `screen-share` (`server/internal/modules/screen-share`, #14258): the node's screen sessions
  (`internal/modules/screen-share/screenlink`, the relays and direct routes of `docs/extensions/computer-use.md`) and
  the measuring tool `vncbench`. Its manifest's flag `module.screen-share` replaces the id's place in
  `modapi.ClientModules`, so a hub whose manager lacks the module turns the clients' screen sharing off. The node's
  `internal/screen` is not part of it: it reads Agents' terminal panes (dialogs, the input box, rewind) and is core.
- `phone-extra` (`server/internal/modules/phone-extra`, #14258): phone control of this computer's paired iPhones
  (`docs/extensions/iphone.md`): the built-in program `iphone` (`phone-extra/nodeiphone`), the device bridge's client
  and pairing store (`phone-extra/iphone`), the look-and-do core (`phone-extra/iphonectl`), and `personad iphone …`.
  It uses two small helpers of the node that are leaf libraries (`internal/clitext`, `internal/node/nodekit`).

### Guards

**Controlling changes to the core (#14916).** Persona Core has no UI: it is the hub API and data model for Agents, sessions,
tasks and to-dos, with the clients' headless code and the foundation interfaces. The clients' interface code is the default
panel built on Core. `schema/core-paths.txt` has two tiers: `core` (strict review, owner review, no module
references; the label is recommended, not required since #17195) and `panel` (only no module references); modules are free. The guards below keep modules and core apart; three
more pieces control changes to tier core itself, in this order of strength. The merge check: `scripts/hooks/core-change-check.py`, run by
`scripts/hooks/merge-pr.sh`, is advisory (#17195): it reports which tier-core paths of `schema/core-paths.txt` a PR changes (read by `scripts/test/core_paths.py`:
the server units this file's classification calls `core`, the clients' headless directories and the foundation interfaces
and guards named one by one; panel files and tests-only changes do not count) and whether a `core-change` label and a `## Why this must change the core` section are present, but no longer refuses the merge — Core changes get strict review instead of a required label. CODEOWNERS:
the core block of `.github/CODEOWNERS` is generated from tier core of the same list (`scripts/hooks/codeowners.py --write`) and requests
the owners' review; whether the review is required is a branch ruleset the administrator sets. The reference guard:
`scripts/test/test_core_references_no_module.py` needs no toolchain and checks on every platform that the four guards'
baselines stay empty and that no core or panel Go, web or Kotlin file imports a module (the Apple ratchet is called as it is). A PR
that edits the list, the check or the guards is itself a core change. The contributor steps are in `CONTRIBUTING.md`,
"Changing Persona Core".

`server/internal/archguard` keeps the boundary. Both module guards read the import lines of every `.go` file under
`server/` (testdata left out), whatever its build constraint or GOOS, so a file only a `persona_core_only` build or one
platform compiles cannot hide a violation (`TestModuleGuardsSeeEveryBuild` proves it on a fixture).

- **`TestCoreImportsNoModule`**: no package outside `internal/modules` imports `internal/modules/…`, except the composition
  roots listed in `testdata/module_roots.txt`, which import only a list: `internal/modules/all` (the manager's) or
  `internal/modules/all/node` (the node's, imported by `internal/nodecmd`). A core package's tests may
  import `internal/modules/all` to run as the manager does (`internal/assistant/modules_link_test.go`), and a module's
  stand-in, `internal/modules/<id>/<id>test`, a fake of the outside service the module's program talks to (the
  extension host's tests run `personad volc` against `volctest`); nothing else of `internal/modules`. Known violations would go into `testdata/module_imports_baseline.txt`, which only shrinks
  (a stale line fails); it is empty.
- **`TestModulesUseOnlyTheSPI`**: every file of a module, tests included, imports only the standard library, third-party
  packages, `modapi` and the other contract packages (pure, so nothing more comes with them), the leaf libraries, its
  own packages, and the modules its `module.json` needs. A module it only wants may be left out of the build (`persona_without_<id>`), so it is reached
  through the kernel, never imported. Each module directory has a `module.json` whose id is the directory's name.
  Known exceptions are debt in `testdata/module_services.txt`, which only shrinks (a stale line fails): `<module id>
  <core package>` for any of the module's files, `<module id> (test) <core package>` for its tests only, no wildcards.
  The lines are the GitHub and iMessage connectors' tests, which run them through the connector registry and gate, the
  conformance suite and the subscription service with the modules' own fixtures, the Health module's water import
  test on the real SQL (`health (test) internal/db`), `recording internal/kb`: the recordings'
  storage (the `kb.Recording` model with its `Engine` and `Store`) stays in the knowledge base until it offers a
  recording port, and `knowledge-ui internal/kb`: the history import writes into the knowledge base, the core's memory. A declared core-service dependency is a known debt, not a precedent: the assistant, the hub and the
  extension host are never listed.
- **`TestLibrariesAreLeaves`**: the leaf libraries (`testdata/libraries.txt`) are the core's packages a module may use
  that hold no state and own no feature (localization, the extension SDK, small helpers). Each depends only on the
  standard library, third-party packages, the contracts and the other libraries, so a module that uses one reaches no
  manager, hub, node, store or module. A library is added only when a module needs it and it passes this test.
- **`TestStandInsHoldNoModuleCode`**: a stand-in imports only what a library may, never its module, so the core tests
  that use it reach none of the module's code.
- **`TestContractsArePure`** covers `modapi` like every contract package, and the hub's
  `TestHubDoesNotImportTheManager` (`server/internal/hub/architecture_guard_test.go`) forbids `internal/modules`, and
  `persona-node`'s `TestPersonaNodeDoesNotLinkTheHubOrTheManager` forbids the manager's list, `internal/modules/all`.
- `scripts/test/test_module_schema.py` checks every `module.json` against `schema/module.schema.json`, refuses invalid
  manifests, and keeps `modapi.Info`'s fields and the schema's in step.

### The briefing pilot

The daily briefing is a built-in module (#13430, decision 3; #13845). The scheduler calls its runner through
`modapi`. The runner prepares the facts, feed item and paginated message cards for delivery.

- **The module owns** (`server/internal/modules/briefing`): the `assistant.briefing_waits` setting (its type, defaults,
  validation and card text), the wait rules of the briefing's per-direction rows, routine fact gathering (`FactsAt`,
  Inbox follow-up filtering, in-progress sorting, per-run retention and schedule lifecycle hooks), the evening day
  recording summary (`Day`, `BoundDay`), the `brief` feed item body and writer, the model-failure fallback lifecycle,
  card rendering and pagination, and the `POST /api/v1/later/{id}/handled` feed action registered through `AddHubRoute`.
- **The core keeps** the stored value (the `briefing_waits` field of the assistant's settings, byte for byte as before),
  the setting's description in `persona_settings`, the ledgers behind the narrow `modapi.Ports` adapters
  (`server/internal/assistant/modules_ports.go`), transport retries and the shared `POST /api/v1/todos/{id}/accept`
  action used by ordinary to-do reviews and decisions.
- **With the module absent** a routine with facts on opens with no facts and keeps no briefing run, so its answer goes
  out as a routine with facts off does (no brief item, no briefing rows on the Lark card); a daily report gets the north
  star's lines alone; the acceptance reminders wait for no briefing; `assistant.briefing_waits` is hidden from
  `persona_settings` but stays stored. The later-item action returns 404. Conversation history still serves stored
  brief items and the shared to-do acceptance action stays available. `server/internal/assistant/modules_test.go` covers each of these.

### Volcengine speech

`volc` (`server/internal/modules/volc`, #13431) is the Volcengine speech provider module, apart from voice (#13430
section 2): the program `personad volc` and its stand-in service `volctest` (docs/extensions/volcengine-speech.md). The
extension host keeps the extension's manifest, its meetings switch and its settings text (`internal/extmgr/volc_extension.go`),
from what the module provides through `builtinext`; its catalog strings (`hub-volc_extension.*`, `volc-*`) stay in the
hub's locales. `volc` is also the first id of `modapi.ExtensionModules` ("Modules from extensions" below): once the
official market's `volc` package is installed (handed off from the built-in, or on a build without the module), that
package decides `module.volc`, on while it is enabled with both `transcription/volc` and `speech/volc` on; until
then the built-in module's flag stands as before.

### Modules from extensions

An installed extension may supply a module's flag (#16254), so a module can move out of the binary into an
extension package without its clients noticing. Only the host decides which: `modapi.ExtensionModules`
(`server/internal/modapi/modapi.go`) lists the module ids an extension may supply and the contributions
(`<point>/<id>`) each needs; today it is `volc` (`transcription/volc`, `speech/volc`). A package never names the
module it claims.

- **Who is a provider** (`server/internal/extmgr/module_providers.go`): the registry's record of the listed id, a
  package installed on the hub's machine (`source: installed`, not a remote plugin installed from `remote:` without a
  package; an installed package whose program is attached, `Record.AttachedPackage`, still counts), from a market whose spec is the
  official one (`extension.IsOfficialMarket`), verified with the official signing key or a retired one
  (`extension.Official.PreviousSigningKeys`). A local upload, a private market's package and another id are not
  providers. A private market cannot hold an official key, current or retired (`extension.NormalizeMarketKeys`).
- **Its flag** is on while the extension is enabled, every contribution the module needs is on and the `modules`
  row leaves the id on. Whether its program is running, or has a problem, does not count.
- **The channel**: the manager reports each provider with its flag in `extensions.report` (`modules: [{id, on}]`,
  `extwire.Report.Modules`, [protocol.md](../protocol/protocol.md) "Extensions, connectors and bots (extensions.*, 2026-11-05)"), again on every
  registry change, when its extensions are loaded and on every reconnection. The hub
  (`server/internal/hub/extensions_link.go`, `reportedModules`) reads it from the default manager only, while that
  manager is connected and its hello carries the `modules` marker, beside the built-in modules' flags of the same
  hello. An installed provider decides its module's flag: one that is on adds `module.<id>`, one that is off removes the
  built-in module's flag of that id. The hub keeps the report while the manager is away (the flags go with the
  connection, as the hello's do) and, for the assistant, across a restart. An older manager reports no providers and
  an older hub ignores them, so both keep today's flags.
- **Reserved features**: the marker `modules` and any `module.<id>` are never an extension's own feature. The manifest
  schema cannot declare `module.<id>`, the manager drops both from the `api.features` it reports (logged once), and the
  hub drops them from every report's features.
- **Rows nothing may serve** (#16263, `server/internal/extension/dormant.go`): before it loads the registry the manager
  names every listed id with whether the `modules` row switched it off (`Registry.HoldModule`). Load then sets aside,
  in a private dormant table, the rows of a listed id that nothing may serve: a built-in's row on a build without that
  built-in (an orphan, such as `volc` on a `persona_without_volc` or core-only build), and any row of a module the
  `modules` row switched off, installed packages included. A dormant row is not listed, nothing runs it, it supplies no
  flag, and nothing in it changes: its settings, switches, sealed secrets and files stay as they were. Load never fails
  because of one. Switching the module back on and restarting reads the row again as it was.
- **Adopting an orphan**: the catalog offers the official package of an orphan's id as an install. The registry's
  commit (`validatePlan`, which every install entry point goes through: the catalog, a bundle, an upload, the command
  line) takes the orphan's row over in place only for a package from the official market verified with its current
  signing key that provides every contribution the built-in's stored manifest names, and only while the module is on.
  The installed row keeps the owner's settings, switch, switched-off contributions and installation time, and is the
  built-in's verified successor (`BuiltinPredecessor`, `PreviousOrigin: builtin:<id>`), so its storage key, sealed
  secrets, data and the transcription pinned to the built-in stay; the write is pinned to the row as Load read it, so a
  row that changed meanwhile is refused. Anything else over an orphan (a local upload, a private market, a missing
  contribution, a module switched off) is refused with the identity error and writes nothing. Once adopted it is an
  ordinary installed package: it can be upgraded, rolled back to the built-in on a build that has it, or uninstalled.
- **Rollback across builds**: a package rolled back to its first version on a build without the built-in has no
  built-in to name as its previous version. On a build with the built-in again, rollback still returns to it
  (`Registry.Rollback`), with the owner's settings, switches and secrets. On a build without it, rolling back to the
  built-in is refused and nothing changes.
- **The stream contract in the SDK**: the transcription point's job wire (`TranscriptionInfo`, `TranscriptionFrame`,
  the frame and code names, `F`) and the `X-Persona-Role` and `X-Persona-Lang` header names are in the Go SDK
  (`sdk/go/wire/transcription.go`, hand-written, not generated from the schemas) so a package outside personad speaks
  them; `server/internal/extwire` keeps its names as aliases of these.

### Voice

`voice` (`server/internal/modules/voice`, #13431) is full-duplex voice: the program `personad voice`, which also serves
the built-in meeting transcription contribution (`gpt-live`), and its stand-in upstream `voicetest`
(docs/assistant/voice.md). The core keeps the generic seam: the extension host's `transcription` point, which any
extension may contribute to, and the meeting transcription the assistant chooses; the built-in default is whatever the
voice module provides (`builtinext.Voice`), and without the module there is none, so a meeting transcribes with a
chosen extension or on the phone. The apps' composer dictation is core; the extension's GPT-Live dictation mode
(docs/assistant/voice.md 4.18) goes with the module, and apps use it only while the extension advertises its
`dictation` feature. Still in the core and named by string, to move as the host serves their slots: the channel kind
(`channel.KindVoice`), `assistantwire.PluginsVoice`, the `voice` and `speech` extension points, the extension host's
glue (`internal/extmgr/voice_extension.go`) and the `hub-voice_extension.*` strings. The developer tool
`server/scripts/transcribelive` measures live transcription and reads the knowledge base.

### Recording

`recording` (`server/internal/modules/recording`, #13407) is meeting recording on the manager: the `/api/v1/recordings`
routes and the phone's audio socket, meetings attended and transcribed on the hub (live jobs, final passes, the
fallback), the meeting asks, photos, live view, rooms, Just me and follow-ups, and the push of minutes and day recaps
with the to-dos made of their action items (docs/assistant/meeting-minutes.md, docs/assistant/day-recap.md). Its
manifest declares `feature: module.recording`, which the kernel announces while it is loaded.

- **The seam**: the core reaches it through `server/internal/recordinghost`, in the core's own terms (`Layer`: what
  the core asks of it; `Host`: what it asks of the core). `server/internal/modules/all/recording.go` provides it under
  `!persona_without_recording && !persona_core_only`; the assistant makes one layer per manager before it registers
  the knowledge base's routes (`internal/assistant/recording.go`, `loadRecording`), unless the `modules` row switches
  it off. The routes, room tools and loops stay on this seam rather than the kernel's `hub_routes`, `mcp_tools` and
  `background_jobs` slots: the routes are registered with the knowledge base's route guard, the room tools are guest
  tools (not assistant tools) and the loops run on the knowledge base's own schedule, so each moves when the kernel
  can carry those.
- **The host is the surface to shrink**: about 75 methods (`recordinghost.Host`: settings rows, conversations and
  their origins, guests, the computers, pushes and channels, to-dos, models, the transcription choice, tracks). Each
  group becomes a narrower port as the core serves one.
- **Shared with the core**: the knowledge base (`recording internal/kb`, above), the transcription job's socket
  (`server/internal/transcribejob`, a leaf library the core's transcription for extensions uses too) and the meeting
  transcription choice, which stays in the core (`meeting_transcription_settings.go`, the extension host's
  `transcription` point).
- **Times as configuration**: the module's times (its `timings.go`, pinned by `TestDefaultTimings`) and the
  attending feeder's clock and timer come through `Host.Timings` and `Host.FeedClock` from
  `assistant.Options.RecordingTimings`, `RecordingTimingLists`, `RecordingFeedNow` and `RecordingFeedAfter`: a test and
  tuning knob of the code that builds the manager, never a user setting, read from no settings, `hub.json` or route.
  `Layer.Wait` ends the live hub runs and waits for the module's background work; the manager calls it when it stops
  (`kbEnd`).
- **Tests**: the module's own tests run on a fake core (`fakecore_test.go`, with the assistant's fake hub ported as
  `fakehub_test.go` and a real knowledge base on the test's directory); the core's tests of how the module meets the
  rest of the manager (day-recap pushes over the channels, the evening facts, `kb_get`, the fleet's attendee facts,
  meeting turns against tool calls, hub transcription end to end) stay in `internal/assistant` and skip without the
  module (`needsRecordingModule`).
- **With the module absent** (`persona_without_recording`, `persona_core_only`, or switched off) the recording routes
  answer 404, the audio socket is unknown (so are the photo upload and delete routes; the list stays readable below), the hello names none of the nine meeting and recording capabilities (so
  the hub announces none of their feature flags), no room tool is offered and no final pass or attending feeder runs
  (`TestRecordingFollowsItsModule`). The recordings already stored stay in the knowledge base, **listed, readable
  and searchable, byte for byte unchanged** ("A module that is off keeps what the user has" above):

  | With the module off | What happens |
  | --- | --- |
  | The knowledge list (`GET /api/v1/kb/recordings`, `kb_list`) | Works: every stored recording, as before |
  | A recording's detail (`GET /api/v1/kb/recordings/{id}`, `kb_get`): transcript, minutes, day recap, photo texts | Works, the same response as with the module |
  | Search (`GET /api/v1/kb-search`, `kb_search`) and the search index | Works: transcripts, minutes, recaps and photo texts stay indexed |
  | Tags, pinned facts and projects of a recording (`PUT /api/v1/kb/recordings/{id}`, `kb_update`) | Works: the knowledge base's generic edit |
  | Delete (`DELETE /api/v1/kb/recordings/{id}`) | Works: the knowledge base's own route, always there |
  | Retention (`recording_days`, `minutes_days`) | Runs as configured (`kb.RetentionWithoutRecordingModule`, the one decision point) |
  | Crash recovery of an interrupted upload | Runs when the store opens, as ever |
  | New recordings, uploads, appends, the audio socket, photo upload and deletion, rename, the minutes and recap routes | Gone: the module's routes answer 404 |
  | The minutes, titles and recap lanes, summaries, the sweep of hub transcripts after a prune | Run and do nothing: no model call, no file written |
  | Stored photos (`GET /api/v1/recordings/{id}/photos` and `GET …/photos/{n}`) | Work: Core's own read-only routes (`assistant/kb_recording_media.go`), the same paths and responses with the module on or off; adding and deleting photos stay the module's |
  | Stored audio | No route has ever served it (the chunks are the final pass's input, deleted after it); the chunk files of a recording in flight stay on disk untouched and the module resumes from them |

- **Reader, kept writes and writers** (`docs/plans/plan-kb-recording-port.md`, PR 2): in `server/internal/kb` every
  recording file is split by what its code is. `<file>_read.go` is the read path (list, get, the transcript of
  record, minutes and recap readers, the search view with the undo-journal overlay) and `<file>_keep.go` the writes
  that exist without the module (delete, retention with the trim, the undo-journal commit and the recovery in
  `NewStore`, the `meeting.json` update primitive, the photo book the trim needs): both are Core. No `<file>.go` of
  writers is left there but one: `recordings_summary.go`, the summary round, which is the knowledge base's own
  summarise, tag and link-to-projects of an entry applied to a recording and still runs only with the module (below).
  Reads, delete, retention and recovery call no writer.
- **Every recording writer but the summary round is in the module** (PRs 3, 4 and 5):
  `server/internal/modules/recording/writers` holds `minutes.go`, `minutes_overview.go`, `minutes_timeout.go`,
  `day_recap.go` and `day_recap_write.go` (PR 3), `recording_title.go`, `recording_photos.go`,
  `recording_audio.go`, `transcription.go` and `hub_segments.go` (PR 4), and `recordings.go` (the upload),
  `recording_day.go` (a day's appends), `live.go` (the live log), `meeting.go` and `meeting_material.go`
  (meeting.json and the material of an answer), `recording_upload_rest.go` and `echo.go` (PR 5), moved from
  `internal/kb` with their bodies unchanged. They reach the store through the **storage port**, `recordingwire.Core`,
  which the knowledge base implements (`kb.RecordingCore`, `server/internal/kb/recording_core.go`) and hands to the
  module in its hook (`recordinghost.Layer.Hook`). The port has two groups, 46 methods. `Reader` (22) is the knowledge
  base's own read path, the functions every other reader calls, with the photo book as stored, the hub's
  transcript, the tombstones, one committed file of a recording, and the three settings the upload reads (the
  retention days, whether the hub can transcribe, whether an answer may hold earlier meetings). `Writer` (24) is the
  write primitives (`CommitRecording`, the upload's commit under the undo journal; `PutMeta`, `PutSummary`,
  `WriteFile`, `WriteDurable`,
  `RecoverRecording`, `BumpPhotosRev`, `PutPhotoBook`, `PutPhotoText`, `UpdateMeetingLocked`), the change feed, the
  index and its purge, the engine's clock, log, models and output language, and `Locks`: the store's locks the
  writers take themselves (recordings, then meeting, then files, then meta, and the files lock's read side for an
  audio append; the order is the store's and did not change). **The live log's lock and the audio writers' state
  are the module's**: `liveMu` and the per-recording audio mutex with the map of them (`storeState` in `shim.go`)
  left `kb.Store` with their writers; nothing in Core took either. The six locks in their order, which did not
  change: `recMu` (Core: delete, retention and a hand edit take it too), `meetMu` (Core: the trim takes it),
  `liveMu` (the module), the audio mutex (the module), `recFilesMu` and `metaMu` (Core: readers, the commit, the
  recovery, every entry's meta.json). The state is
  one per store: a second engine over the same store gets writers that share the first ones' state
  (`Writer.Provider`), so a delete, the trim and retention reach the audio an open socket writes through
  (`AudioDropped`, `AudioClosed`), and with no module there is no state and both are no-ops. An audio append is a
  plain call: no goroutine, and the files lock's read side through one interface call. The echo rule
  (`server/internal/modules/recording/writers/echo.go`) moved with the upload's cleaner: Core's track recordings,
  which run that cleaner on an upload the module's route hands them, take it from the route as a
  `recordingwire.UploadCleaner` (`recordinghost.Host.PutSpaceRecordingRoute`) instead of calling it in `internal/kb`.
  The port is wider than the plan's first
  sketch because the writers read and write the store in many places; the alternative was to leave them in Core. The
  package has its own `engine` and `store` over the port (`shim.go`), so that a writer's body reads as it did. The
  rules of a recording's stored state that both sides apply (`PlanMinutes`, `PlanRecap`, `RecordingEnd`, the hub
  transcription checks) are pure functions in `recordingwire` (`rules.go`). **The port is strongly typed**: a
  recording's id is `recordingwire.ID`, its files `recordingwire.File`, and what was a string with a few meanings is
  a closed set (`Change`, `MeetingPart`, `CalendarKind`, `Purpose`, each with `Known`); derived values (the output
  language, the owner's name for the model, a file's path, whether a recording was deleted, whether a model error
  puts the work off) are computed by Core and handed out. The stored JSON and
  the knowledge base's own call sites keep their text.
- **The write side is a provider** (`recordingwire.Provider`): the four background rounds (`WriteMinutes`,
  `TitleRecordings`, `WriteDayRecaps`, `SummarizeRecordings`), `AfterPrune` (the module's part of the retention round)
  and the audio writers' two touch points with delete and retention (`AudioDropped`, `AudioClosed`). The module's
  provider is `writers.Writers`: all of them are its own code but the summary round, which it carries to the
  knowledge base's own (`recordingwire.WritersInCore`, `kb.RecordingWriters`, `server/internal/kb/recordings_summary.go`):
  that round names the retry book, the tag and project machinery and a project's log, none of them about recordings,
  so it stays in Core and runs only with the module. The audio touch points take a `recordingwire.ID`. The module's
  `Hook` gives it to the knowledge base (`kb.Engine.SetRecordingProvider`); without the module the knowledge base
  holds `recordingwire.NoProvider`, a value whose methods do nothing, so no call site checks for the module. The
  assistant's lanes and its retention tick call the rounds through `kb.Engine.RecordingProvider()`
  (`internal/assistant/kb_lanes.go`, `kb.go`). Before this split the lanes ran whether or not the module was loaded;
  they now stop with it.
- **In flight when the module goes**: an upload not complete, minutes or a day recap still `pending`, a hub
  transcription `live` or `finishing` stay as stored. The knowledge base's responses carry a derived
  `paused: "module_off"` for such a recording (`Detail.Paused`, `Entry.Paused`; `kb.Engine.recordingPaused`), computed
  when read and never written, so the files do not change and the module resumes from the stored state.
- **Checked** by `TestRecordingsStayReadableWithoutTheModule`, `TestRecordingInFlightIsPausedWithoutTheModule` and
  `TestRecordingDeleteAndRetentionWithoutTheModule` (`internal/kb`, black-box over the stored files with no
  provider), `TestMinutesResumeWhenTheModuleIsBack` (`internal/modules/recording/writers`),
  `TestStoredRecordingsOutliveTheirModule` (`internal/assistant`, in every build), and on running hubs by the two gates above, which compare every file under
  `recordings/` before and after the module is off.

### Health

`health` (`server/internal/modules/health`, #14406) is the manager's Health intake (docs/assistant/health.md "Water
and protein"): the `/api/v1/assistant/health/intake…`, `…/items` and `…/water/reminder` routes, the water reminders, the intake
page, the legacy water extension's import and the `health_intake` tool. Its manifest declares `feature: module.health`,
which the kernel announces while it is loaded (it left `modapi.ClientModules` with this move).

- **The seam**: `server/internal/healthhost`, in recording's shape: `Layer` (core -> module: `Routes`, `Tool`,
  `ReminderLoop`, `Items`, `CardAction`, `WorkoutReceived`, `WorkoutReport`) and `Host` (module -> core: the settings rows, the intake store, the water extension's migration, the
  apps' events, the language and time zone, standby and quiet push, the push with its card, and the extension page's
  fitting). The module declares its own `Host` with the same methods and types (`server/internal/modules/health/host.go`),
  so it imports no core service; `server/internal/modules/all/health.go` provides its layer under
  `!persona_without_health && !persona_core_only`; its port is a `moduleswitch.Port`, so the `modules` row switches it
  off as the other seams. The assistant makes the layer where it registered the routes before
  (`server/internal/assistant/health_layer.go`), so routes, the tool's place in the list and the hello stay as they were.
- **Shared with the core**: the entry's type is a contract (`assistantwire.HealthIntakeRow`, which `db.HealthIntakeRow`
  aliases); the `health_intake` table and its SQL stay in `internal/db`; the tool's description stays in
  `server/internal/prompts/mcp/health`. The Health *space* (its conversations' directory, policies and outbound
  confirmation, `server/internal/assistant/health.go`) is core: every core tool's space check reads it.
  Tell Health (#14714, `server/internal/assistant/health_tell.go`) also belongs to the core, alongside workout uploads:
  it stores receipts and controls Health conversations, their delivery and their training-chat pointer. Its route
  and `health_tell` capability remain available when the Health data module is absent or switched off.
- **Tests**: the module's tests run on a fake core (`fakecore_test.go`); the water import's acceptance also runs on the
  real SQL (`health (test) internal/db` in `module_services.txt`). The core's tests of how the module meets the rest of
  the manager (the tool's space guard, the intake page fitted for older apps, the push outbox, the module's absence)
  stay in `server/internal/assistant/health_module_test.go` and skip without the module (`needsHealthModule`).
- **With the module absent** (`persona_without_health`, `persona_core_only`, or switched off in the `modules` row) the
  nine routes (including health items GET/PUT) answer 404, the hello names neither `health_items` nor `health_intake`
  (so the hub announces neither feature), the `health_intake` tool is not listed and refuses, and no
  water reminder runs (`TestHealthFollowsItsModule`). The entries already stored stay in the database. The Web keeps
  the core Health space's stored conversations, reports and memory on its existing `/health…` and
  `/tracks/health…` addresses through a read-only default-panel archive; it does not call the absent intake routes.
  Health chat retains phone-data guidance but omits intake-item instructions. The `Items` seam reads the ordered
  `health.items` setting (or computed defaults); item saves, tool logging and reminder planning share the module lock.
  Strength training (#14713) adds `StrengthStore`, `CardShown`, `CardChanged` and `PageLink` to Host. Its contract types
  live in `assistantwire`; the four strength tables and atomic draft/card changes remain in `internal/db`. The module
  owns `health_strength`, its managed `health-strength-` cards and `strength.*` states under `-health-intake`.
  The core dispatches reserved card actions before its generic outbox, persists strength workout windows through
  `WorkoutReceived` before upload acknowledgment, and asks `WorkoutReport` for current inputs inside `runTask`.
  `WorkoutReport` returns `assistantwire.StrengthReportPrompt` (the prompt and workout start time) so a successful
  strength report can continue the training chat for the workout's local start day. Without the module, Tell Health
  still opens Health conversations, but reports do not select training chats and instructions omit strength guidance.
  No module, no added strength item, an unknown window or assembly errors keep the stored task prompt; other workouts
  remain byte-identical. Module absence also removes `health_strength` from hello and refuses its card buttons, while
  retaining all stored strength data. No additional page route or hub business logic is introduced.

### Knowledge UI

`knowledge-ui` (`server/internal/modules/knowledge-ui`, Go package `knowledgeui`, #14407) is the knowledge pages'
pipelines that are not the core's memory (#13430 section 2): the import of the connectors' older chat history
(`kbhistory`: the `kb_history_plan`, `kb_history_status`, `kb_history_stop` and `kb_history_resume` tools, the driver
its loop runs, the chat lists of the selection page) and the marks of the lines a knowledge base day shows that the
owner sent from Persona (`kbsent`, by the sent-as-user ledger). Its manifest declares `feature: module.knowledge-ui`,
so the flag the clients gate the knowledge pages on is the kernel's to announce, no longer `modapi.ClientModules`'.

- **The seam**: `server/internal/knowledgeuihost`, as recording's (`Layer`: core -> module; `Host`: module -> core,
  in the standard library's, the knowledge base's and the contracts' types). `server/internal/modules/all/knowledgeui.go`
  provides it under `!persona_without_knowledge_ui && !persona_core_only`, through a `moduleswitch.Port`; the assistant makes one layer per manager
  (`internal/assistant/knowledge_ui.go`, `loadKnowledgeUI`) unless the `modules` row switches it off. The layer keeps
  the import's shared state (one call in flight, the source running, the process's instance); the core keeps the
  loop that wakes it on the knowledge base's schedule (`kbhistory_service.go`), the selection itself and its
  candidates route, which asks the layer for the chats and what each would be read as.
- **Shared with the core**: the knowledge base (`knowledge-ui internal/kb`, "Guards" above) and the sent-as-user row,
  `connectorwire.SentAsUser` (`db.SentAsUser` is an alias). Prompts (`mcp/kb_history_*`) and strings stay where
  they were.
- **Tests**: the import's and the marks' own tests moved with them (`kbhistory`, `kbsent`); the assistant's tests of
  the tools, the candidates route and the marked days through the Service stay in `internal/assistant`, read the
  import's files from the knowledge base's directory and skip without the module (`needsKnowledgeUIModule`).
- **With the module absent** (`persona_without_knowledge_ui`, `persona_core_only`, or switched off) the four
  `kb_history_*` tools are not offered and answer as unknown, no import runs, `GET /api/v1/kb/selection/candidates`
  answers that the source has no history (404) without asking it, a knowledge base day comes without sent marks
  (`sent_origin` "unavailable"), and `module.knowledge-ui` is not announced (`TestKnowledgeUIFollowsItsModule`). What
  an import wrote stays in the knowledge base.

### Lark

The assistant's Feishu / Lark layer (`server/internal/assistant/feishu*.go`: the chat's turns, cards, commands,
workbench, topics and pushes, about 10k lines) is moving into the `lark` module (#13404), in the shape of recording's.

- **The seam**: `server/internal/larkhost`. Its `Outbound` is the **outbound channel port**, the one way core code
  (acceptance, briefing, reports, conclusions, decisions, owner actions, to-dos and their sources, concerns) reaches
  the owner's chat: `Status` and `Features` (what the channel supports now), `Post` (a card, with a text fallback and
  an idempotency key), `Note` and `Reply` (a line under a message), `UpdateCard` and `ThreadPost` (a line for a to-do's
  chat thread; the core writes the text). Its values are the contract's (`assistantwire.ChannelTarget`,
  `assistantwire.ThreadEvent`) and `internal/botcard`'s. `Layer` (core -> module) embeds the port and grows with each
  move; `Host` (module -> core) is what the moved code reads of the assistant. The module declares its own `Host`
  with the same methods (`server/internal/modules/lark/layer.go`), so it imports no core service, and
  `server/internal/modules/all/lark.go` provides its layer under `!persona_without_lark && !persona_core_only`.
- **The core asks the port**, `s.chat()` (`server/internal/assistant/lark.go`), never a `feishu*.go` function: a
  core file that posts to the chat calls the port. The transport that posts and edits cards (`feishu_transport.go`) still
  lives in the core, because the cards, pushes, board and workbench pin that call it are still the core's, and it is
  served to the module through the host under the port's names (the reply, reaction and message reads moved into the
  module); each move takes code into the module and drops the host methods it no longer needs.
- **The Feishu turns are the module's** (`server/internal/modules/lark/turn.go`, `cot.go`, `cot_worker.go`,
  `stream.go`): a Feishu run's progress card and CardKit entity, its agent message and the worker that writes it, and
  its answer. The channels' pipeline (the runs, their lock, finishing, folding and dropping them) stays in the core,
  which reaches the turns through `larkhost.Turns` (the core's `feishuSink` forwards to it). The module keeps its own
  state of each live run in the core's `chanLive.sink` (`Host.RunState`), which the core asks to `Abort` when it drops
  the run and to forget a held run's turn (`NextTurn`). What the turns read of the core is the host's second group of
  methods: the channels' runs and their values under their lock, the hub's `feishu.*` transport, and the run's live
  notes (`Host.RunProgress`, `assistantwire.RunProgress`). The progress card, the folded process panel, the pieces note
  and the answer's cards are the module's (`progress.go`, `answer.go`); the core's pushes and mirrors ask the layer for
  the answer's cards and the note (`larkhost.Cards`: none without the module). The module's times (`timings.go`) come through
  `Host.Timings` from `assistant.Options.LarkTimings` and `LarkTimingLists`, a test and tuning knob as recording's;
  `Layer.Wait` waits for the agent messages' workers (the service waits for them when its connection ends).
- **The workbench is the module's** (`server/internal/modules/lark/workbench.go`): `/home`, `/today` and `/inbox`,
  their buttons and the home card, reached through `larkhost.Workbench`. The core gives it what it shows, in the
  contract's terms (`assistantwire.WorkbenchItem`, `WorkbenchCounts`, `WorkbenchAgent`, `WorkbenchBoardRow`,
  `WorkbenchCompanion`, `WorkbenchMeeting`; the Today card's JSON as stored), and does what its buttons ask (file or
  mute an Inbox item, draft and send a reply, switch the chat's target, the other cards). The Inbox item a button names
  is read by the Inbox core (`server/internal/assistant/inbox_workbench.go`); the counts by
  `server/internal/assistant/workbench_view.go`.
- **A to-do's topic lines are the module's** (`server/internal/modules/lark/thread_posts.go`): the queue (a line a
  minute at most, what came meanwhile joined), the wording and the post under the topic's latest message. The module
  implements the port's `ThreadPost`, so the host no longer serves it. It reads the to-do's Feishu thread, the bot's
  status, the topic anchors (kept with the chat's state, `server/internal/modules/lark/chat_state.go`),
  the to-do's link, and the core's clock and timer (`PostNow`, `PostLater`, which a to-do's source report waits on
  too).
- **The chat is the module's** (`server/internal/modules/lark/chat_state.go`, `message.go`, `commands.go`,
  `card_actions.go`, `text_answer.go`, `read.go`, `models.go`, `elsewhere.go`, `thread.go`, `thread_open.go`,
  `nudge.go`): its state (the settings row `bots.feishu.state`, read and written through the host's `Setting` and
  `PutSetting`, with unchanged bytes: the values both sides name are `assistantwire.ChatTarget`, `ChatCard`,
  `ChatCardRef`, `ChatBoardRef` and `CompanionBot`), the owner's messages and presses, the commands, the nudges, the
  topics' messages and the to-dos they open. The core reaches it through `larkhost.Chat`: the hub's events
  (`Message`, `Action`, `Owner`, `Recalled`, `View`, which the Service's `Feishu*` methods pass on), what the core asks
  (`StatusKept`, `Route`, `SetTarget`, `RememberThread`, `Send`, `CardsWaiting`, `CardNudged`, `TopicAnchor`,
  `LegacyRuns`, `NudgeTick`, which the core's nudge sweep runs once a minute) and `Book`, which runs a function on the
  chat's records under its lock for the core's cards (`owner_action_reply.go`, `reports.go`; it goes when they move).
  What the chat asks of the core is the host's chat group: where a target is and the run a message starts
  (`feishu_route.go`, which other channels use too), an interaction answered or closed, an acceptance pressed, the
  main conversation's model, and the to-do, decision and question ledgers of a topic (`TodoOfThread`, `ThreadRoutes`,
  `TopicDecision`, `OpenTodo`), which stay in the core. The chat's times are module timings (`turn_wait`,
  `topic_lookup_wait`, `card_answer_wait`, `start_card_window`, `form_window`), its clock the core's per-instance knob;
  `Layer.Wait` also waits for the chat's messages being handled and the agents and forms a press starts.
- **The agents' cards are the module's** (`server/internal/modules/lark/asks.go`, `ask_cards.go`): the card of a
  question, a permission, a plan or a terminal dialog, where it goes, the card it becomes once over, and the cards
  closed while their node is away. The core tells the chat through the interaction port, `larkhost.Asks`
  (`AskAnnounced`, `AskPost`, `AskWanted`, `AskEnded`, `AskDetached`, `AskBack`, `AsksCatchUp`: nothing, and no card
  wanted, without the module), next to the generic channels' own fan-out (`channelInteraction`). What a card says of
  the core crosses as data, one value per question: `Host.AskView` (`assistantwire.AskView`: who asks, the task and
  its direction, the conversation's page, the core's wording of the interaction; built in
  `server/internal/assistant/ask_view.go`), `Host.AskRoute` (`assistantwire.AskRoute`: the runs of the chat on the
  conversation, from the channels' runs), `Host.AskSensitive` (the exit policy of its words, asked only for a card the
  chat does not drive with "all cards" on, since it may wait for the text to be tagged) and `Host.AskState` (the
  core's live record: ended, still up). The module reads nothing else of the core for a card.
- **The standing board, the pinned workbench and the companion bots are the module's**
  (`server/internal/modules/lark/board.go`, `workbench_pin.go`, `companions.go`): when the "waiting for you" card is
  edited (the settle timer and its lock), how it is sent and replaced, the workbench's pin, its loop and the button
  every card carries, and the notices a notifications companion long-polls. The core tells them through
  `larkhost.Board` (`BoardChanged`, `WorkbenchOn`, `WorkbenchStamp`, `DivertNotices`: nothing, the card unchanged and
  `false` without the module). `DivertNotices` takes `assistantwire.NoticeKind`, with `report`, `push` and `task` as
  its JSON values. An unknown kind raises a defect even when no companion is available.
  The rows (what waits and for how long, `board_rows.go`) and the waiting list as a card stay the core's, since the
  briefing and the acceptance rounds read them too. The module asks for the card as of a time (`Host.BoardCard`) and
  for whether the pending cards are read (`Host.FleetReady`). The four routes the companions use (`PUT
  …/feishu/companions`, `GET …/feishu/notices`, `POST …/feishu/notices/action`, `POST …/feishu/workbench`) are the
  module's `hub_routes`, served through the kernel only while it is on. With the module off or built out they answer
  404 (before, an off bot answered from the core). The request tells the module which manager's layer it is for
  (`larkhost.WithLayer`, put on it where the core registers a module's routes). The stored chat state
  (`bots.feishu.state`: the board's message, the companions) is only ever written by the module's code; switching it
  off neither rewrites nor deletes it, and it is read again when the module comes back. Locks, the same before and after the move: the board's `run` (one send or edit at a time) is the outermost; under it
  the board reads the chat's records (the chat's lock, through `Book`) and, through `Host.WorkbenchBoard` or
  `Host.BoardCard`, the core's pending cards, ended set, acceptance book (`loadAcceptLocked`) and to-dos, each taken and
  released in turn, none held across the next, so the core's locks are never taken inside the board's own. The board's
  `mu` and the companions' `mu` and the workbench's flag are leaves, never held across a call out. A core caller of
  `BoardChanged` may hold its own lock (`channels_handler.go`, `todos.go`): the call takes only the chat's lock and the
  board's `mu`, as `boardChanged` did, and never waits for `run`. The acceptance rounds cross as data
  (`Host.WorkbenchBoard`'s rows).
- **The user sync is the module's** (`server/internal/modules/lark/larksync/service.go` and `service_*.go`):
  settings and sealed-token management, OAuth v3 user authorization and refresh, sync-only bot device-code
  registration, HTTP routes (`/api/v1/bots/feishu/sync*`), scheduler loop, and the `larksync` outbox connector
  (`Changes`, `Items`, `Status`). Core (`server/internal/extmgr/larksync_host.go`) holds only the minimal adapter
  implementing `botport.SyncHost` (`server/internal/botport/botport.go`) and delegates the five external call sites
  through `botport.SyncService`, while `server/internal/larksyncwire/types.go` holds the shared settings keys, sealed
  field lists, outbox row structs, and `larksyncwire.Store` contract implemented by `internal/db`. With the `lark`
  module off or built out, the routes answer 404, the `larksync` connector is absent, the loop is a no-op, primary
  registration falls back to `docs_standard`, and stored `bots.feishu.sync*` settings remain untouched
  (`server/internal/extmgr/larksync_module_test.go`).
- **Tests**: the turns' own tests run in the module on a fake core (`server/internal/modules/lark/service_test.go`:
  the channels' runs, their finishing, folding and dropping, and the hub's transport; `chat_fake_test.go`: the chat's
  host methods as the core answers the plain case); the assistant's tests of the turns through the channels' pipeline
  stay in `internal/assistant` and skip without the module (`needsLarkLayer`), and its tests of the chat hand it the
  owner's messages and presses as the hub does (`server/internal/assistant/lark_chat_test.go`); its tests of the
  agents' cards drive the interaction port with the real core behind it
  (`server/internal/assistant/lark_asks_test.go`).
- **With the module absent** (`persona_without_lark`, `persona_core_only`) the port is `larkhost.None`, a bot switched
  off: its status reads without an error as neither on nor ready (so an acceptance ask is card-less and a briefing
  goes out by push, as with the bot off), it supports nothing, a post or a reply answers `larkhost.ErrNoChannel`, and
  notes and thread lines are dropped; the chat takes nothing, nothing waits in it, and its records are empty.
  Switched off in the `modules` row it is the same: `larkhost`, `botport` and `cliport` all follow the switch ("The
  switch").

### How to add a module

These steps move a feature of this repository into an in-tree module, and step 3 changes Core paths (the
registration in `server/internal/modules/all`, a `module <id>` line in `schema/module-classification.txt`). To add a
feature to a core-only install without changing Core code, write an extension instead:
`docs/extensions/core-only-features.md`, with the sample `sdk/examples/quicknotes`.

1. Pick the id from #13430 section 2 and check its manifest needs only slots the kernel serves; if it needs another,
   add the `Host` method and the kernel's support first, in their own PR, and update "Slots" above.
2. Create `server/internal/modules/<id>/` with `module.json` (validated by `schema/module.schema.json`), a `Module` whose
   `Info()` reads it and whose `Register` adds what it declares. Import only `modapi`, contract packages and the modules
   it needs (never one it only wants).
3. Add `server/internal/modules/all/<id>.go` with `//go:build !persona_without_<id> && !persona_core_only`, and
   `want_<id>_test.go` there under the same constraint, adding the id to `wantModules`. A module that runs as a
   built-in extension program provides what the core needs of it there too ("Provider seams").
4. In the core, replace each direct call with a question to the kernel (`s.modules`), and define what happens when the
   module is absent; test both.
5. Run the archguard tests, the tagged builds and `scripts/test/test_module_schema.py`.

## Clients

Each client documents its side in its own platform doc; these are pointers only. Across both Core and feature
modules, shared domain logic lives in the Go server while clients stay thin (`docs/architecture/server-first-logic.md`).

- **Apple** (#13432): `PersonaHostApp` with `PersonaModuleAPI` and `PersonaModule<Name>` packages. See `docs/apple/` (written with #13432).
  A module the hub may turn off declares `feature` = `module.<id>`; the app follows the rule agreed on #13844 (off only
  when the hello has the marker `modules` and lacks the flag; remembered per hub before the hello). See
  `docs/apple/ios.md`, "Modules" (#13849).
- **Android** (#13433): `:core:moduleapi` and `:module:<id>`. See `docs/android/` (written with #13433).
- **Web** (#13434, #13846): the module host `web/src/core/module-host.ts` and `web/src/modules/<id>/`, serving routes,
  sidebar entries, chat slots, Settings categories and card renderers. Shipped module switches are lasting browser
  preferences in Settings › General › Optional features. The form reads the registry and the browser stores disabled
  module ids in `persona.modules.off`. A core-only build has no module switches. See `docs/web/web.md` ("Modules").

Web Labs admits temporary trials or a choice between stable and new implementations of one capability. A trial keeps
the stable default and a lossless rollback, and links a tracking issue with a plan to graduate or remove it. Put
permanent browser and advanced preferences in their owning Settings category. Keep irreversible changes, internal
debug controls and placeholder demos out of Labs. See `docs/web/web.md` ("Labs").

**Hub flags.** Every client switches modules on the hub's hello the same way (agreed on #13844): a module's `feature`
is `module.<id>`; a hub that publishes module flags also announces the marker `modules`; a module with a `feature` is
off only when the hello has `modules` and lacks the module's flag. No hello yet, or an older hub without `modules`,
leaves it on. An approved installed extension supplies its module's flag the same way ("Modules from extensions"
above): the clients see only `module.<id>`.

Every client reads the same manifest fields from `schema/module.schema.json`.
