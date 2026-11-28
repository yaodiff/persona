# pi and pi-web

Sessions from [pi](https://pi.dev) (`@earendil-works/pi-coding-agent`) and its web client pi-web (`@agegr/pi-web`) appear in “Agents”:
lists and status, chat content (text, thinking, tool calls and results, images), token-by-token output, sending messages from the phone, interrupting, renaming, creating sessions in pi-web,
and answering pi questions (`ask_user`) and approving tools you specify from the phone.

Supported since 2026-11-01 (issue #170). pi is the second integration under the runtime specification (`docs/integrations/runtimes.md`): `integrations/pi/` contains a pi extension and a small companion process,
both built on `integrations/runtime-sdk/`. pi follows the route of whichever host manages it:

| Where pi runs | What it is in Persona | Responsibilities |
| --- | --- | --- |
| **In a herdr pane** (pi TUI) | A herdr session, like Claude in herdr | Panes, spaces, labels, terminal display, keys: herdr; chat content, sending, interrupting, cards, token-by-token output: the extension in the same pi process (`server/internal/node/pi.go`) |
| **In pi-web** | A session of runtime `pi-web` | Extension (in the pi-web server process) + companion process (on-disk sessions, creation, wake-up) |
| In another terminal (not herdr) | A session of runtime `pi-<pid>` | Extension; no terminal display |

## Structure

```
 App ── hub ── node ── ~/.persona/runtime.sock ◄─┬─ persona extension (one connection per pi process)
                                                 │    · pi terminal / pi --mode rpc: runtime `pi-<pid>`, only sessions running in this process
                                                 │    · pi-web server process: runtime `pi-web`, sessions running in pi-web
                                                 └─ pi-web companion process companion.mjs: runtime `pi-files`
                                                      · Lists recent on-disk pi sessions (within 7 days, at most 80), paginates files
                                                      · Creates sessions and sends to non-running sessions through pi-web HTTP APIs
```

- **Why one connection per process**: pi-web runs many sessions in one process (embedded SDK, no child processes); pi loads the extension module only once and calls the factory function once per session (verified in testing).
  The extension keeps a host at module level (`globalThis`); each session registers its own extension API with it.
- **Why a companion process**: pi-web loads extensions only when a session is open. After a restart, before anyone opens a session in the web client, the extension alone would leave Persona unable to see any pi sessions
  or create one from the phone. The companion uses the same host code (`createPiHost({listDisk: true})`) and pi's own `SessionManager`.
- **How pi in herdr becomes one row**: herdr reports the Agent as `pi`; its pi integration (`herdr integration install pi`) also reports the session file (`agent_session {kind: "path"}`,
  filename `<time>_<session id>.jsonl`). Without herdr's integration, the extension reports its pane in the list (`pane_id` = `HERDR_PANE_ID`), so node can still match it.
  This row belongs to herdr (`source_kind` empty, terminal available); its title, last message, model, and revision come from the extension's listing. The extension's row no longer appears separately. Calls are routed by type:
  `session.keys` / `screen` / `terminal.watch` / close / label names go to herdr; everything else (content, sending, interrupting, title changes, cards) goes to the extension.
- **pi panes without the extension**: Still listed, with only the terminal. Chat shows an explanation; sending is rejected (`pi_no_extension`: do not blindly type into pi's input box), and interrupt sends Esc.
- **Both sides list the same session**: The companion lists on-disk sessions (including ones it just asked pi-web to create) as `offline`; the process running the session (the extension in pi-web, or a pi terminal) lists it as running.
  node chooses the running owner (`runtimeRegistry.recomputeOwners`): pi session files have no lock, so messages must never go to a second process writing the same file.
  When pi-web reclaims an idle session, the companion takes over. On the next message, it asks pi-web to reopen the session, and the extension takes ownership again.

## Who manages sessions, and where offline sessions resume

At session start, the extension records where it runs using `pi.appendEntry("persona-host", {host})` (`herdr`, `terminal`, `pi-web`; only when it changes, excluded from model context;
not recorded for sessions launched by other programs, such as `pi --mode rpc` or `pi -p`). After a session stops, the companion lists it from disk and uses the last record to decide:

- Previously run in herdr or a terminal: the row carries `resume_argv: ["pi", "--approve", "--session", <file>]` (`--approve`: "Project trust" below). When the phone sends a message, node opens a new tab in the herdr space for that directory
  (with Orca the computer's engine, a new terminal in the Orca worktree holding that directory, which `cd`s there, #17044),
  runs it through `personad tab-start <0600 file>` (commands and paths do not pass through a shell or appear on screen), waits for the new pi extension to list the session, then sends the message.
  If the engine is unavailable, it rejects the request (`engine_missing`); it does **not** open it in pi-web instead. If node is too old to recognize `resume_argv`, the companion also rejects the request rather than waking pi-web.
- Previously run in pi-web, or no record (sessions from before the extension was installed): pi-web wakes it, as before.

## Installation

On the computer running pi / pi-web (it must be in the hub's machine list, and node must be able to run). The companion requires node support for `prefer_running` (personad after this PR).
It does not connect to old nodes; it connects automatically after node restarts (upgrades). The extension keeps working (but without on-disk sessions or session creation from the phone):

1. Install pi and pi-web: `npm i -g @earendil-works/pi-coding-agent @agegr/pi-web`. Models: nothing to configure when Persona's model providers cover this computer ("Persona's model providers" below); otherwise, or for models of your own, configure them in pi (`~/.pi/agent/models.json`, see pi's documentation).
2. Install with automatic-update support (“Automatic updates” below): `node <Persona repository>/integrations/pi/install.mjs`. This installs the package into
   `~/.persona/runtime-packages/persona-pi-runtime/current/` and prints the exact paths for the next two steps.
   Then run `pi install ~/.persona/runtime-packages/persona-pi-runtime/current/pi` (it uses the adjacent `runtime-sdk` through a relative path).
   pi terminals, `pi --mode rpc`, and pi-web all load it. Restart pi-web after installation.
   Alternatively, run `pi install <Persona repository>/integrations/pi` directly (a local directory is linked, not copied), but this does not auto-update: you must update the checkout and restart pi-web yourself.
3. Keep the companion running: `node ~/.persona/runtime-packages/persona-pi-runtime/current/pi/companion.mjs [--port <pi-web port>]`, supervised by launchd (`KeepAlive`) /
   systemd (`Restart=always`). During an automatic update it exits after installing the new version, letting the supervisor restart it with the new code.
   It lists all on-disk sessions (stopped herdr sessions and sessions reclaimed by pi-web); install it even if you use pi only in herdr.
4. To use pi in herdr: `herdr integration install pi` (herdr's own pi integration, which reports status and session files; optional, since the extension can report the pane).

### Configuration (optional)

`~/.pi/agent/persona.json` (under `PI_CODING_AGENT_DIR`):

| Field | Default | Description |
| --- | --- | --- |
| `approve` | `[]` | Tools that require approval on the phone (or in pi's own dialog), such as `["bash", "write"]`. pi itself has no permission confirmation; nothing is blocked by default. The phone card has “Always approve” (“Don't ask about Bash again in this session”): after tapping it, that tool no longer asks in this session. This is recorded in the session file (custom entry `persona-allow`, excluded from model context) and remains effective when another pi process resumes the session. One-time approvals and denials are not recorded |
| `askUser` | `true` | Gives the model an `ask_user` tool: ask the user a question (optionally with choices). The card goes to the phone whenever a hub is connected, and pi's own dialog (terminal / pi-web page) opens beside it: the first answer wins and the other is taken back. A message sent to the session while it waits is the answer. The session shows as needing you meanwhile, and stopping the turn withdraws the question (#8097) |
| `answerFirst` | `auto` | Who gets approvals first: `auto` = the phone if it has driven this session within the last 6 hours (a session the hub started through the companion counts: the node tells pi-web's extension, `runtime.driven`), or no local UI is open (in pi-web: no browser has the session open, read from pi-web's `GET /api/agent/debug`); otherwise local first (pi terminal / pi-web page); `phone` / `local` fixes one side. If no local dialog is available (`pi -p`, JSON mode), approval cards go to the phone (when connected to the hub); if the phone also does not answer, execution is blocked. A local approval dialog shows the session as needing you and is withdrawn when the turn is stopped |
| `socket` | `~/.persona/runtime.sock` | node runtime socket; the `PERSONA_RUNTIME_SOCKET` environment variable also works |
| `providers` | `true` | Register Persona's model providers in pi ("Persona's model providers"); `false` leaves pi to its own configuration |
| `autoUpdate` | `true` | Extensions installed from the package store accept versions supplied by node (“Automatic updates”); `false` prevents switching. The companion uses `--no-update` |

### Persona's model providers

A computer whose pi has no `models.json` still runs pi and pi-web sessions on the providers in Settings › Model providers (#8194),
as Claude Code does (`docs/integrations/launchers.md`, "Device scope and on-demand delivery"):

- **What pi gets**: each Persona-managed provider this computer is in scope for and that speaks a protocol pi has: a Messages provider for
  Claude Code or Oh My Pi becomes an `anthropic-messages` provider (bearer providers also send `Authorization: Bearer`), a Codex provider an
  `openai-responses` one. Not offered: Antigravity providers, Responses / Chat Completions API connections (the node never gets them), providers with a
  proxy of their own (pi sends straight to the gateway), and providers without a key. They are registered as `persona-<provider id>` with the
  provider's models (its default model first, context windows from what the provider knows, no prices). Output limits, image input and thinking
  support use the provider's reported model metadata when available; otherwise pi receives the existing estimates based on the context window
  and model name. A reported output limit is capped at the context window.
- **How**: when pi loads the extension (`lib/providers.js`; every pi process, `pi -p` included, and every pi-web session), it reads the node's launch
  key beside `node.sock` and asks `launcher.pi`. The node (`server/internal/node/pi_providers.go`) pulls what it does not have yet with `launchers.pull`,
  under the provider's scope and own-service rule, and answers. The extension passes the keys to `pi.registerProvider()`: they live in pi's memory
  and the node's, never in a file (no `models.json`, no `auth.json`). A process that cannot read the launch key (a sandbox hiding the data directory),
  a computer with no node, or one out of every provider's scope registers nothing and pi behaves as before.
- **Your own configuration wins**: a provider id already in your `models.json` is left to it; a `defaultProvider` / `defaultModel` in pi's settings
  (global or the project's `.pi/settings.json`), or `--model` / `--provider` / `--models` on the command line, keeps pi's choice; so does a model of your
  own provider that is already selected and signed in, and a resumed session keeps its model. Otherwise a new session starts on Model Settings ›
  Agents (manager `launchers.agents_default`) with its thinking level, when that is one of these providers.
- **Where you see it**: the web's Settings › Model providers marks these providers "pi too"; New Agent's model panel says "pi can use it too" on their group.
- **Limits**: the manager must be reachable the first time a provider is used since the node started (choose "Keep a copy on each computer" for
  providers that must work while it is away). Needs an updated node, manager and extension; an older node refuses `launcher.pi` and an older manager
  names no default.

### Automatic updates

personad embeds this package at build time (the extension, companion, and `runtime-sdk` in `integrations/pi`; `persona-pi-runtime` in `internal/runtimepkg`).
The package carried by node is the version to run (see `docs/integrations/runtimes.md`, “Auto-update (optional)”, for the general approach). Only installations from the package store (pi loads `<store>/current/pi`) update:

- **Extension** (pi terminals, `pi --mode rpc`, pi-web server process): reports its package hash when connecting to node. If node carries a different package, it fetches, verifies, and installs it as the new `current` (replacing the symlink by rename),
  then waits until **all sessions in this process are between turns and none has a card on the phone**, stops its connection, and has each session run `/persona-reload` on itself. The extension registers this command,
  which calls pi's `ctx.reload()` (pi grants extensions this ability only inside commands), so `sendUserMessage("/persona-reload", {expandPromptTemplates: true})` executes it as a command
  rather than sending it to the model. When pi reloads the extension it reads from disk (jiti, no module cache), reading the new `current`: the same process and session now use the new code. The new host connects to node with the same
  process identity (`instance`), replacing the old connection. pi-web does not permit reloading while replying, so it must wait until idle.
  With no session open in pi-web, there is nothing to reload. The next session still opens with cached old code, then switches immediately after opening.
- **Companion process**: likewise fetches and installs the package, then exits; launchd / systemd restarts it with the new code (without supervision it would stay stopped, so supervision is required).
- If switching fails (old pi cannot register commands, or the reload returns the old version): stay on the old version and do not retry that version in this process; retry after pi restarts.
- To see the running version: node logs `runtime pi-web <version> #N connected`; extension logs require `PERSONA_PI_DEBUG=1`.

The first switch (from a version without this mechanism, or an installation from a checkout) must be manual: run `install.mjs`, `pi install` the new path, run the companion from the new path, and restart pi-web and the companion.
These are all terminal commands on that computer (or ask an Agent to do it); there is no UI to click through.

The companion's `--port` / `--host` must match pi-web (default `127.0.0.1:30141`). If pi-web sets `PI_WEB_PASSWORD`, the companion needs the same environment variable.

### Project trust

pi asks before it loads project-local files (`.pi/`, `.agents/`, `AGENTS.md` and the like) from a directory it has no saved decision for. A pi Persona starts or resumes in a herdr pane has nobody at that prompt: a `task_plan` / `agent_start` start, herdr's resume of one, and a stopped session woken by a phone message would wait there, the extension would never list the session, and the first message would not be sent (#9462).
So those runs pass `--approve` ("trust project-local files for this run"): the directory the task or the person chose is trusted for that process only. Nothing is written to `~/.pi/agent/trust.json`, so a pi the user starts in a terminal still asks, and a saved decision there stays the user's. pi versions without project trust also lack the flag and refuse it; update pi (`pi update self`).
Persona's own health check runs `pi -p … --no-session` in an empty temporary directory, which has nothing to trust.

## Mapping

`integrations/pi/lib/map.js`, entries in a pi session file → Persona items:

| pi | Persona |
| --- | --- |
| user message (text, images) | user `text` / `image` (images are placeholders; data uses `session.image`) |
| assistant message: `thinking` / `text` / `toolCall` | `thinking` / `text` / `tool_use`; tool names and arguments use Claude's presentation: `bash`→`Bash`, `read`→`Read` (`path`→`file_path`), `edit`→`Edit` (one change) / `MultiEdit` (multiple), `write`→`Write`… |
| `stopReason: aborted / error` | notice “已中断” (“Interrupted”) / “出错了：…” (“Error: …”) |
| `stopReason: length` (output reaches the limit) | notice “输出达到上限，这一轮停了” (“Output limit reached; this turn stopped”) |
| `toolResult` | `tool_result` (with `tool_use_id`, `is_error`); images inside become separate `image` items (with the same `tool_use_id`; the app displays them on this call's card) |
| Command the user runs with `!` in pi (`bashExecution`) | A `Bash` `tool_use` + `tool_result` pair |
| Compaction, branch summaries, visible custom messages | notice |
| System prompts, model / thinking-level changes, labels, session names, usage | No item (seq still advances) |

- **seq** is the entry's position in the session file (starting at 1). pi is append-only, so it is stable and increasing.
- **Branches**: pi sessions are trees (“Edit from here” creates a branch). A page contains only entries on the current branch; `history` = session ID + the entry where the branch leaves file order.
  It stays unchanged for appends, changes when switching branches, and makes the app refetch the whole page rather than stitch two branches together.
- Sending: while the session waits in `ask_user`, the message is the answer to the question. Otherwise, while the session is running, `pi.sendUserMessage(…, {deliverAs: "steer"})` inserts it into the current turn, read after the step under way, and the send answers `queued` (the manager says "queued", not "sent"); when idle, it starts a new turn.
- Interrupt: inside pi-web through pi-web's own Stop (`POST /api/agent/<id> {type: "abort"}`: it also cancels the dialogs extensions have open and takes back what was queued for the turn), elsewhere `ctx.abort()`; `ask_user` gives up on the turn's abort signal. The interrupt then waits up to 10 seconds for `agent_settled` and answers `settled`, or fails with `interrupt_timeout` (#8097). Rename uses `pi.setSessionName`.
  A given `client_msg_id` is sent only once.

## Token usage

The node meters pi's token usage from its session files (`$PI_CODING_AGENT_DIR` or `~/.pi/agent`), whether or not Persona started the session: each assistant message's
usage, cache warming's `usage` entries and the compaction and branch summaries' calls, under the agent `pi` on the token calendar (`docs/server/machines.md`, "Token usage").

## Limitations

- Only sessions running in pi processes with this extension installed are visible, plus on-disk sessions from the last 7 days (with the companion). Without pi-web running, non-running sessions can be viewed but not messaged.
- pi terminals outside herdr have no terminal display and cannot be closed from the phone; those inside herdr can.
- The phone cannot yet create a new pi in herdr by itself (the app's create goes through pi-web); `task_plan` and `agent_start` with `launcher: pi` do, in a herdr pane (`docs/integrations/launchers.md`, “Work agents”). Stopped pi sessions in herdr can be resumed from the phone.
- pi itself has no plan approval, so there is no corresponding card.
- pi-web's HTTP API is not a public, stable API (the companion uses only `POST /api/agent/new` and `POST /api/agent/<id>`). Run the end-to-end tests below after upgrading pi-web.

## Validation

- `cd integrations/pi && npm test`: mapping, lists, pagination, sending (steer, images), interrupting, renaming, push continuity and branches, the companion's disk listing and pi-web APIs,
  approval (phone / local dialog / block if nobody answers), `ask_user`, pane reporting in herdr, recording the host, `resume_argv`, and “herdr sessions are not handed to pi-web.”
- node: `server/internal/node/pi_test.go` (fake herdr): one herdr session row, call routing, recognizing sessions from the extension's pane, missing extensions, resuming in herdr, and rejecting when herdr is absent.
- End-to-end (`DSH_CHECKOUT` is used only for its fake model service):
  - All three use the SDK's common acceptance tests (`runtime-sdk/testing/conformance.mjs`: text turns, approval, denial with a reason, questions, token-by-token output, interrupting, continuous pushes).
  - `node integrations/pi/e2e/herdr.mjs`: pi TUI in real herdr (a separate named session, configuration in a temporary directory, herdr pi integration installed), real `personad node`, and the companion:
    one herdr session row, extension serving requests, display from the pane, common acceptance tests, phone messages not typed into the pane, disk listing after `/quit`, then another message resuming and receiving an answer in a new tab in the same herdr space. 2026-11-01: 23/23.
  - `node integrations/pi/e2e/run.mjs`: real `pi --mode rpc` with the extension and real `personad node`: listing, common acceptance tests. 2026-11-01: 16/16.
  - `node integrations/pi/e2e/update.mjs`: automatic updates. pi-web and the companion are installed from the package store (the script supervises the companion like launchd); node's package comes from a directory
    (`PERSONA_RUNTIME_PACKAGES`). No package is offered for the same version; after switching to a node carrying a new version, the extension in pi-web switches in place (same pi-web process, same session),
    the companion installs, exits, and returns on the new version, `current` points to the new version, phone messages still receive replies after the switch, and `/persona-reload` never enters the chat. 2026-11-02: 9/9.
    Unit tests: the last five cases in `integrations/pi/test/pi.test.js` (wait until idle before switching; no switch or message without a command; companion exits after installation; restore and stop retrying when returning to the same version; do not install a mismatched hash).
  - `node integrations/pi/e2e/pi-web.mjs`: real pi-web (isolated configuration directory, this package installed through `packages`, idle reclamation set to 6 seconds) + companion:
    launcher visible before any session exists, creation from the phone, sending, disk listing and pagination after reclamation, waking on another message, extension taking ownership again, and listing sessions created in the web client. 2026-11-01: 11/11.

### Assistant conversations

The pi-web path can start an empty Assistant session and apply session-local tool,
live-context and supervision hooks before the first prompt. The ordinary terminal
pi path does not advertise Assistant starts. Assistant custom state stays outside
user message text; closing the execution session revokes its hooks. Tools verify
the actual session context on every call.

The pi-web launcher advertises one default model row. Choosing it sends no model
or effort override: this adapter keeps the model selected inside pi. It refuses
unsupported explicit model/effort choices and permission modes before releasing
input; it supports default approvals and bypassPermissions. No permission upgrade
is inferred during a logical conversation switch. Unit contracts use fake pi
extension APIs and a fake web backend, without a provider call.

Native shell actions still obey Persona command gates in bypassPermissions. The
adapter reads current tool rules for the actual session, rejects matching shell
commands (including nested shell wrappers), and asks the model to use the scoped
Persona action tool. Failed rule reads block shell execution; ordinary coding
Agent sessions retain their existing behavior. Gate parsing is shared with the
OpenCode companion and covered by its existing contracts plus pi-specific fakes.

A Persona message arriving during an active pi Assistant turn returns retryable
`busy` with `delivered: none`, leaving role, permissions and hidden context intact.
The normal durable delivery path waits for idle, so the next `before_agent_start`
can apply the fresh snapshot and policy together. An answer to the current turn's
own question may resolve that question without changing its role or permissions.
Ordinary coding Agent steering is unchanged.
