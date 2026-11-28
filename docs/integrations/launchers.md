# Launchers and “Settings › Model Providers”

A launcher determines which gateway and model claude uses. Previously, all were functions or aliases invoking claude in each computer's `~/.zshrc` (see “Detecting launchers in ~/.zshrc” below;
node types the name into the interactive shell in a herdr pane, expanding functions and aliases, prefixed with `HERDR_AGENT=claude`; see “How herdr recognizes Agents started by launchers” below). Since 2026-10-31, “Settings › Model Providers” manages them alongside entries added in the app.
Code: `server/internal/assistant/models_launchers.go` (owned by the manager; hub only forwards these routes, `docs/assistant/model-config.md`, “Who owns models”), `server/internal/node/managed.go`; app `Views/Settings/ModelLaunchersView.swift`.

## Sources

| Source | Defined in | Actions |
| --- | --- | --- |
| **Persona-managed** | Added in the app: name, Base URL, API Key, authentication method, model, small model, sub-Agent model, custom model option, extra arguments | Edit, delete |
| **rc file (~/.zshrc / ~/.bashrc) and PATH** | Functions and aliases in each computer's login shell config (zsh reads `~/.zshrc`, bash reads `~/.bashrc`), grouped by file, and wrapper scripts on its PATH (`source: "path"`), shown from the last snapshot and refreshed in the background when opening this page (node caches for 1 minute) | View each computer's function definition or script text (Key / Token redacted), hide / restore in Persona |
| **Built-in** | Each computer's plain `claude` command (“Claude Code default” in Model Configuration, `source: "builtin"`), listed per computer with its version | View, hide / restore in Persona (#4384) |
| **External adapter** | An explicitly registered local program supplies a sanitized inventory (`source: "external"`); connection configuration and credentials stay on that computer | Choose a model explicitly in the main chat; edit the connection in its source tool, not in Persona |

- Scope: each Persona-managed entry has its own **device scope** (“Device scope and on-demand delivery” below): all your computers, chosen ones, or none (the key stays with the manager). ~/.zshrc entries identify their source computers (same-name entries merge into one row). Offline computers use their last synchronized lists (settings `launchers.zshrc`).
- A computer that reported its Claude Code version offers the built-in `claude` in its pickers even when the node's shell-wrapper list omits plain `claude` (#4660). The hub uses that same installation signal as the built-in provider and Agent-start fallback; it does not infer an installation from wrappers alone, and a hidden built-in stays hidden. A new work session takes it the same way: the node's start options and work start accept plain `claude` whenever its login shell runs it, listed or not (#7425).
- Deleting a ~/.zshrc launcher **only hides it in Persona** (settings `launchers.hidden`), leaving the file unchanged; it disappears from new-chat and assistant pickers. Hiding the built-in `claude` works the same way (#4384): it leaves the pickers, while a start that names it, or a default that runs it (Model settings set to “Claude Code default”), still runs it. The same holds for a hidden ~/.zshrc or PATH launcher (#4659): Model configuration (the main chat, Agents) or a conversation that names it keeps running it (`withUnhidden` in `assistant/models_launchers.go`), and only a launcher really gone from the computer gives way to another. A profile tier that names a hidden launcher stays among New Agent's presets (#4661), and the hide confirmation lists what still names it (`GET /api/v1/launchers/{name}/uses`).
- **Importing aliases** (`GET` / `POST /api/v1/launchers/import`, or the assistant's managed `launcher_import` MCP tool with `action: preview | commit`; rules in `docs/assistant/model-config.md`, “Importing providers from shell aliases”): the claude aliases of a computer's `~/.zshrc`, `~/.bashrc` and `~/.bash_aliases` become Persona-managed providers with all their settings: one per gateway and key, or one per alias when the aliases differ and the user keeps them apart (`split`). Variables Persona has no field for go into the provider's sealed environment; credentials, values the shell would expand and names tied to one computer are left out and named in the preview, never with their values. An imported provider is `own_service: use`. With `hide_aliases` (MCP `hide_originals`) the import also hides each imported alias in Persona, as deleting would, but only when nothing uses it (Model configuration, a scheduled task, a model profile or its Agent presets) and every computer that lists it gets the provider in its place with the same account (same address and key); the others stay, with the reason. MCP commit uses one owner-confirmation card and both MCP actions return only explicit non-sensitive DTOs.
- **Starting from what the user already has** (#5193; rules in `docs/assistant/model-config.md`, “Starting a provider from a pasted configuration”): the wizard's first step also offers **Paste a configuration** (`POST /api/v1/launchers/import/parse` reads a shell alias or exports, Claude Code's `settings.json`, a `curl` line or a provider in the cindy v1 format into drafts that prefill the wizard; nothing is saved until the user does) and, when a computer has some, **Found on your computers**: the rc-file claude aliases that point at a gateway and are neither hidden nor imported yet, each opening the import above for that alias only (`alias`). Both need the hub's `launcher_import_parse`.
- **Import links** (#5420, #5428, “Import links” below): a link a gateway's key page or another Persona user hands over (`<web address>/settings?tab=models#import=…`, `persona://provider/import?v=1&data=…` opened on iOS or Android, or cindy's `cindy://provider/import?v=1&data=…` pasted into **Paste a configuration**) opens the same wizard filled in, through the same parse route; a provider's ⋯ menu copies one (no key unless the user pastes one into it).
- Persona-managed entries win name collisions (shadowing the ~/.zshrc entry). Names may contain only letters, digits, `_`, and `-`, and cannot be `claude`.

## Sync state (#2854)

`GET /api/v1/launchers` (handled by the assistant process, `assistant/models_launchers_routes.go`; the hub gives each computer's Claude version as `claude_version` in `devices.list`) answers the last-seen snapshot immediately when one exists and runs one bounded refresh behind it; a later read sees the refreshed list and sync state. The first-ever list, with no in-memory or persisted snapshot, still synchronizes before answering. Each computer has at most 10 seconds (`launcherSyncTimeout`) in either path, so a slow or disconnected computer does not hold up a cached page. The node's list script isn't interrupted when the hub gives up: it finishes into the cache, ready for a later read.
The answer adds `sync`: one item per computer, with `state` `ok` / `offline` / `timeout` / `error` (`error` carries the reason), plus `shell`, `rc_file` and the last successful sync time `synced_at`
(settings `launchers.zshrc.meta`). A computer that didn't answer still shows its last list, but the entries' `stale` names that computer; every client marks them “Not updated” and explains why in the per-computer group.
An entry's `rc_files` are the files it came from (`source: "zshrc"` only; older nodes don't report it, so it is `~/.zshrc`).

The Assistant picker gives shell discovery 10 seconds and reserves one further second for own-service checks. A check
cut short by this budget uses the last answer if one exists. On a shell timeout the picker can show that computer's
last known shell and PATH launchers and the providers in its current scope.
Runtime launchers require a live response. Confirmed unavailable providers still show their reason. If the picker has
neither available nor unavailable entries, it returns a timeout so the client can offer Retry. The node bounds the
shell scan and stops its child processes when it times out. An incomplete scan keeps the previous launcher list for
one more minute when one exists. Retry asks the node to scan again. If the new scan is slow or fails, the picker can
read the node's last completed shell list and live runtime entries within the same 10-second discovery budget.

## External model adapters (#7940)

For advanced users who already manage connections in another local tool, an optional adapter supplies the model
inventory and prepares a native CLI's connection settings. It is not a runtime or a model gateway: Persona still
owns the conversation, permissions, hooks, MCP tools, controls and cancellation. Without the local opt-in described
in `docs/operations/config.md` ("A local external model adapter"), the node does not read adapter configuration,
search for a program or start background work.

### Local authority and supported use

Only the node's startup environment may select the registry through `PERSONA_LAUNCHER_ADAPTER_CONFIG`. The registry
contains a stable adapter ID, a fixed command argument array and a trusted absolute `native_program` path. The hub,
clients and inventory cannot provide commands. Interpreter-based programs must name the interpreter explicitly;
an absolute script with an `env` shebang alone may still depend on the service's PATH.

Version 1 supports an explicit, saved model pick in the user's main conversation on a POSIX computer, with the
Claude Code CLI and a policy that does not require the agent sandbox. It does not support Agent SDK, coding or
terminal starts, automatic fallback, global purpose defaults, profiles, tiers or background tasks. A required or
unknown sandbox policy refuses the start; it never disables the sandbox or substitutes another runtime.

The manager checks the actual main conversation and its persisted pick before supplying an internal execution
binding. The node checks that binding and its local registry again. A stale client, a forged source field or a
missing registry cannot turn the reserved `persona_ext_` launcher namespace into a shell command.

### Inventory contract

The node appends `inventory --protocol 1 --offline` to the locally registered command. Stdin is closed and stdout
must contain one JSON document, without banners. A minimal synthetic response is:

```json
{
  "protocol": 1,
  "targets": [{
    "id": "example-target",
    "name": "Example connection",
    "engine": "claude-code",
    "revision": "example-revision",
    "capabilities": {
      "contexts": ["main-chat"],
      "runtimes": ["cli"],
      "platforms": ["posix"]
    },
    "credential_state": "unknown",
    "models": [{"id": "example-model", "name": "Example model", "default": true}]
  }]
}
```

Targets have stable IDs independent of display names. One target represents a connection and execution method;
its models retain their original IDs and use the same `proto.ValidModelID` grammar as saved Persona picks and native
execution, including a supported `~` prefix. An adapter must not advertise IDs that cannot be saved or executed.
A target may carry an `unavailable_code` from the adapter error vocabulary.
`credential_state` is `configured`, `missing` or `unknown`: configured is not a successful call, unknown can be tried
explicitly, and missing cannot start. No URL, key, secret reference, raw environment, argument recipe or local path
belongs in this response. Unknown fields and invalid or duplicate identities are rejected rather than forwarded.

Inventory reads have a five-second deadline and a one-MiB stdout limit, at most 256 targets and 4096 models.
An enabled node announces `external_launcher_inventory_v1`; the manager's internal device row projects it as
`external_inventory:true`. Nodes without the local opt-in advertise neither, and the manager does not probe them.

The manager reads this source through `launchers.list {source:"external",refresh:true}`. This branch runs before
shell, host or runtime discovery and returns only the registered adapter's sanitized entries. Missing registration,
invalid inventory and refresh failures are explicit errors, not a healthy empty source. A saved external target or
model refresh can load a cold registry directly, without waiting for shell startup files. The loaded target still
passes identity, scope, capability, model and revision checks; refreshing never grants authority.

External and ordinary snapshots are updated independently. A successful external read replaces only that machine's
external entries; a failed read does not erase ordinary last-seen launchers. A late or failed shell refresh cannot
erase the external snapshot or turn a partial result into a complete successful list. `launchers.changed` invalidates
this source in the background, and ordinary directory reads keep their existing nonblocking/cache behavior.

### Native bootstrap and credentials

For a supported start the node appends the following arguments to its fixed local command:

```sh
exec --protocol 1 --context main-chat --runtime cli \
  --target example-target --model example-model --revision example-revision \
  --native-program /absolute/path/to/claude \
  --settings-output /absolute/private/process-directory/settings.json \
  -- -p --input-format stream-json --output-format stream-json
```

The shown native flags are abbreviated: Persona supplies the full settings, session/resume, hooks, tools and MCP
arguments. The adapter parses only its own side of `--`, does not read stdin in advance and never sends the native
`-p` through its interactive command parser. Execution uses argument arrays without a shell. Stdout belongs only to
the native stream, stderr diagnostics contain static codes without configuration values, and exit status, EOF and
termination signals must survive the wrapper. Native initialization is acknowledged before Persona delivers the
first user message. External inventory/bootstrap waits share the current operation's cancellation token, so Stop
can cancel a pending start before it obtains the conversation's typing lock or delivers input. That startup token
is detached after initialization and is not stored as the lifetime context of a resident conversation; completing
one RPC does not terminate a successfully started native process. External startup errors expose static codes,
not executable paths or raw operating-system exceptions.

The adapter resolves credentials locally, never from RPC arguments. It preserves Persona's settings and pins only
the selected connection's approved environment in a single final settings file, because process environment alone
cannot establish precedence over existing settings. The node assigns a random private directory; the final file
is 0600 and its directory is 0700. Credentials can therefore exist in protected temporary local files, but never
in inventory, command arguments, manager state or diagnostics. The adapter cleans up normally and the node removes
the directory after exit or a forced stop. User settings and credential files are not rewritten.

Settings or native program paths are not accepted from a request. User and project settings are checked for native
provider-mode switches that could override the selected route. A managed or remotely cached policy may have effective
rules this bounded integration cannot establish, so its presence is refused rather than read or bypassed. The shared
native policy locations include the selected profile's `remote-settings.json`, Linux `/etc/claude-code` policy files
and directories, and the platform's managed policy roots. The adapter does not disable settings sources or rewrite
the user's proxy/egress policy.

Source-tool session and permission preferences are not authority to change Persona's controls. An implementation
must explicitly exclude those preferences and diagnose them without values; unknown or unsafe recipe fields fail
before native execution. It must not silently import bypass mode, replace hooks/MCP, change the model by an alias
search or run an additional proxy.

### Selection, revision and failure

The manager materializes an external target's default row into an exact model ID when saving the main chat's pick.
A later catalog default change does not change that choice. Resetting the pick returns to the existing Persona
default; it does not make the external target a global default or a fallback for other conversations. When changing
to or away from an external connection while the previous process is busy, the next message is refused until that
work finishes or is stopped; it is never delivered using the previous external authorization. Compaction, accepted
queued input and active background work also count as work that must not be cut off by a model/source change.

Clearing the main conversation retains its explicit choice in the manager's durable handoff. Only that handoff can
reserve a server-generated replacement main-conversation ID and carry the external choice into the new main chat;
a normal new chat, guest or caller-supplied ID cannot obtain this scope. Failed starts keep the reservation and choice
for retry instead of falling back or repeatedly losing the user's selection.

A revision identifies the effective non-secret recipe, not the label, usage order or credential value. The adapter
re-reads configuration and authentication at each new start and refuses a revision mismatch before native creation.
An inventory update does not restart an existing conversation: its actual process binding and the current catalog
revision are separate. A later new process uses a freshly validated binding; Persona does not replay a user message
or silently select another connection after a mismatch.

Unsupported scope/runtime/platform/sandbox, missing credentials, invalid inventory and local adapter failures are
not vendor outages. The separate shell probe, automatic connection test and repair do not run through this source
in version 1. Verify the supported path by explicitly using it in the main chat. Changing the trusted command or
disabling the registry takes effect when the node is restarted; it does not rewrite saved model picks.

## Which shell

The node runs the list script and shell-defined launchers, and asks a launcher's CLI for its model list, in the user's shell started the way their terminal starts it (`loginShellProgram` and `shellFlag`, `node/os_other.go`; #16217):

- **macOS**: the account's login shell when it is bash or zsh, whatever rc files the other shell has, run as a login, interactive shell (`-lic`), as Terminal opens every window. zsh then reads `/etc/zprofile` (`path_helper`, which puts the system directories first), `~/.zprofile` (Homebrew's `shellenv`) and `~/.zshrc`; bash reads `~/.bash_profile` (or `~/.bash_login`, `~/.profile`; not `~/.bashrc` unless one of them sources it). A bash user with none of those three files would get nothing of theirs from a login shell, so that bash runs as `-ic` and reads `~/.bashrc`. A node under launchd has no `$SHELL` and only `PATH=/usr/bin:/bin:/usr/sbin:/sbin`, and macOS keeps accounts in Directory Services, not `/etc/passwd`, so the node asks `dscl . -read /Users/<user> UserShell` once (it builds without cgo, so no `getpwuid`).
- **Linux**: zsh when there is one, but bash when the user's login shell is bash (`$SHELL`, or `/etc/passwd` under systemd, where it is unset) and there is no `~/.zshrc`, run interactively (`-ic`): zsh reads `~/.zshrc`, bash `~/.bashrc`, not `~/.bash_profile` / `~/.profile`, so aliases kept there aren't listed. A bash user with a `~/.zshrc` keeps zsh here: an account that can't change its login shell often starts zsh from `~/.bashrc`.
- Always bash without zsh; Git Bash on Windows (`-ic`, `~/.bashrc`).

Settings › Model providers names the file the launchers came from accordingly (`~/.zshrc`, `~/.bashrc`, or `~/.bash_profile` for a login bash on macOS).
Bash has no zsh `(z)` / `(Q)`, and macOS's bundled bash 3.2 has no `BASH_ALIASES`, so under bash the script prints `alias -p`, and the node splits words and strips quotes by the shell's rules itself (nothing expanded or run, `node/launchers_shell.go`) before handing them to the same parser.

### The terminal's shell is another environment (#1642)

The node **lists** launchers, and runs "Test availability", the launcher health checks, probes and repairs, in its own login shell (`loginShellProgram`, "Which shell" above). A start **types** the launcher into a new herdr tab, whose shell is whatever herdr opens there. When the two read different startup files, a launcher the list shows does not exist where it runs:

- herdr's panes run inside a container (sharing the home directory) whose `~/.bashrc` detects the container and returns early, before the launchers are defined;
- the node lists from zsh (`~/.zshrc`, zsh installed and a `~/.zshrc` there) while the account's login shell, and so the tab, is bash;
- herdr's `[terminal] default_shell`, or a herdr server started outside the node with another `$SHELL`;
- a bash login tab reads `~/.bash_profile` / `~/.profile`, which does not source `~/.bashrc`;
- a Persona-managed launcher's `personad` (an absolute path) or `claude` is not visible inside the terminal's environment.

The tab's shell answers within a second with its own line (`bash: <launcher>: command not found`, `zsh: command not found: <launcher>`, `sh: 1: <launcher>: not found`, `fish: Unknown command: <launcher>`, `…: No such file or directory`, `…: Permission denied`). The node reads that line while it waits for claude (`node/shellmiss.go`; only after the command it typed, never the echoed command itself, older scrollback, or claude's own output) and ends the start right away, closing the tab, with `launcher_missing` (or `claude_missing` when the launcher ran but the tab's PATH has no claude) and `where: "terminal"`.
The hub says it in the user's language: which shell the tab opens, that it has no such command, and which shell and rc file the launcher list came from (`docs/protocol/protocol.md` "Error body"). Lines about other commands (rc noise, a `PROMPT_COMMAND`) do not end the start; a start that then times out names the last such line instead of guessing at a busy machine. The same lines in another language count too (bash translates its messages and glibc its errors, so a Chinese machine prints `bash: 行 1: <launcher>: 未找到命令`): a line that a shell's name or the start file prefixes, whose colon-separated parts (ASCII or full-width) name the start's own command, its `claude` or the start file next to other words. Such a line about another command is not read, so it is never named at the timeout. Shells whose lines fit neither still wait for the timeout.
A few lines need care: dash, ksh and busybox say `…: <path>: not found` of a missing path, which counts as a missing file like bash's `No such file or directory` (a managed launcher's `personad` the terminal cannot see); `personad launch` prints its `claude not found in PATH …` through `log.Fatal`, with the date and time in front; fish follows an error inside a function with a backtrace (`in function 'cc' with arguments '--session-id …'`) that repeats the session id but is no echo of the command.
A launcher the node does not list either (removed from the rc file, a stale name from an old picker or plan) ends the start the same way but as plain `launcher_missing`, without `where`: it is gone from the login shell too, so it is neither worded nor kept as the terminal's problem.

To fix it, make the launcher exist where the terminal's shell reads it:

- define it above the early `return` of the rc file, or in a file both environments source (`~/.config/launchers.sh` sourced from `~/.zshrc` and `~/.bashrc`);
- make the shell herdr opens the one the node lists from (the login shell, or herdr's `default_shell`), or have `~/.bash_profile` source `~/.bashrc`;
- or add it in Persona as a model provider ("Running Persona-managed launchers" below): nothing then depends on rc files, as long as the terminal sees the node's `personad` and `claude`.

Launcher health keeps such a launcher apart (`not_in_terminal`, `docs/server/launcher-health.md`): the login shell's check passes, so only a start with it that works clears it.

**The node's own program, on a home reached through a link (#10365).** A home that is a link (`/home/me -> /data/home/me`) and a terminal in a container that mounts the home at `/home/me` without the real path: the node's resolved file (`/data/home/me/.persona/bin/personad`) does not exist there, so every managed launcher failed with `…/personad: No such file or directory`, and the Agents there lost Persona's hooks and MCP server, which name the same path.
The node keeps two spellings of itself (`node/selfprogram.go`): the resolved file stays the update target (`node.info`'s `executable`) and what macOS attributes Full Disk Access to, while every process it starts is handed the data directory's spelling of the same file (`<PERSONA_HOME>/bin/personad`, where the start files already live), else the program under `$HOME`'s own spelling, else the file as it is. `personad node doctor` asks the same function, so the generated hooks, settings and MCP files it checks are the ones the node wrote and `--fix` does not flip them back.
The checkouts the node makes with git (task and "New worktree…" checkouts on herdr-core or without herdr's worktree command, Agents' own worktrees, review copies) get their two git links rewritten to the home's spelling (`node/worktree_links.go`), and a directory the hub resolves for an Agent to start in (`workspace.resolve` with `terminal`) or a checkout `worktree.prepare` hands back is named the same way, so git and the Agent work inside the container too.
After the update a `not_in_terminal` record left by the old path still holds until a start with that launcher works: one explicit retry of the failed task (which passes the fixed-machine gate for this kind of record) clears it.

The terminal's shell can also stop before the start types anything, on a question its startup files ask (oh-my-zsh's update question, compinit's insecure directories, a passphrase; #2570, #10365). The start then ends with `shell_waiting`, not a launcher problem: the tab is kept for 10 minutes and the App offers the question's answers. Picking one types it into that tab and starts the Agent there; the message that start refused goes out once it is up (`docs/protocol/protocol.md` "Answering a start's question"). The tab a start opens gets oh-my-zsh's, oh-my-bash's and powerlevel10k's own switches for those questions in its shell's environment where the engine takes one, unset again before the launcher runs (`docs/integrations/engines.md` "What a start tab's shell starts with").

## Detecting launchers in ~/.zshrc (`launcherScript` in `node/launchers.go`)

node gathers two things in one login-shell invocation (`zsh -ic`, loading `~/.zshrc` first), cached for 1 minute.
The script frames what it prints with begin/end markers, and node reads only between them: what rc files print while the shell starts (Ubuntu's `/etc/bash.bashrc` sudo hint) or an exit trap prints afterwards is never a launcher (#6310).

- **Output of `_claude_launchers`**: Existing `~/.zshrc` helper functions (one fixed-width line per name, gateway, model) still work and come first.
  When a listed name resolves to a PATH wrapper, its row keeps the helper's metadata and gets the wrapper's config directory for node-local matching.
  A shell alias or function of the same name takes precedence over the file.
- **Aliases** (since 2026-11-01): zsh tokenizes and unquotes each alias (`${(@Q)${(z)…}}`); node recognizes only those **whose final program is `claude`**.
  They may have preceding variables (`alias dmcc="ANTHROPIC_BASE_URL=… ANTHROPIC_AUTH_TOKEN=… claude --dangerously-skip-permissions"`), pass through
  `env` (including `-u` / `-i`) / `command` / `exec` / `noglob`, use absolute paths, or run `export …;` first within the same alias. Gateway comes from `ANTHROPIC_BASE_URL`,
  model from `ANTHROPIC_MODEL`, or `--model` if absent. Sort by name (the shell does not preserve definition order); aliases already listed under the same name by `_claude_launchers` are not duplicated.
- **Wrapper scripts on PATH** (#5954, macOS and Linux): an executable file on the login shell's PATH (`~/.local/bin/claude-work`: a few `export`s for the gateway and key,
  then `exec claude … "$@"`), listed with `source: "path"` after the functions and aliases. node reads each file's first 40 lines (no `#!`, binary, or over 64 KB: skipped)
  the way the shell would run them: assignments and `export`s (`$NAME` / `${NAME}` of a variable set earlier gets its value; any other `${…}` form, `$(…)` or a
  single-quoted `$` leaves the value unknown, shown as nothing), `unset`, then the first command that is `claude` (by name or path, through `exec` / `env` / `command`).
  It is a wrapper only when that command **passes the script's arguments on** (`"$@"`: Persona's starts add theirs after the name) and gives claude nothing but
  flags of its own and their values: `-p`, a prompt or a subcommand (`claude mcp add`), a word after a flag that takes none, or claude in a pipeline, is a
  script that uses claude for something. A top-level `exit` or an `exec` of another program before it ends the script. A here-document's text is skipped,
  and so is a line node cannot follow (functions, groups). A claude inside an `if`, a `case` or a loop, or after `&&` / `||`, counts, but its probe is
  "not seen"; a guard closed before it (`if …; then exit 1; fi`) does not matter. `env -i` / `env -` in front drops what the script exported.
  Only the first file of a name on PATH counts, as for the shell; a name the shell has as a function or alias (both run before a file) is not a script launcher,
  and neither is `claude` / `codex` / `omp` itself. The scan stats only regular files and links, reads two bytes before the rest, and gets 3 seconds (a hung
  network mount): past them the last scan's scripts stand.
  A thin wrapper such as `claude-work` can also exec a shell helper by absolute path or alongside the wrapper,
  with literal arguments and `"$@"` at the end. The node reads one helper within the same bounds. It resolves simple
  positional parameters, assignments and exports, and recognizes `dirname "$0"` and `dirname "$(readlink -f "$0")"`
  as script locations. If the helper execs Claude Code with a known `CLAUDE_CONFIG_DIR` and no configured endpoint,
  the node can attribute a terminal session with an empty endpoint to the one launcher with that directory.
  Configuration directories stay on the node. Sourced files, dynamic paths and further helper hops are outside this scan.
- **Codex** (since 2026-11-02): If `codex` is on PATH, add `codex`, plus aliases running only one `codex` command, after the Claude entries, with `agent: "codex"`.
  On a Mac where the login shell finds no `codex` (and has no alias or function of that name), the ChatGPT app's bundled CLI
  (`/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex`) is plain `codex` instead, run by that path (#10437).
  `codex-app` (the Codex App, `docs/integrations/codex.md`, "New chats in the Codex App") is only ever an explicit pick: a default that
  gives way to a launcher the computer has never picks it (`launcherInstead`), and Model configuration offers it to no purpose. Claude's
  desktop app's `claude-desktop` can be Agents' default (`docs/assistant/model-config.md`, "Claude's desktop app as Agents' launcher (#13117)").
  See `docs/integrations/codex.md`, “Starting from the phone” for creating Codex sessions from the phone.
- Not launchers: an alias named exactly `claude`, aliases running an internal Codex-fork CLI, Codex aliases doing other work (`cd x && codex`),
  or unrelated aliases (`ll`, `gst`…). If none exist, only default `claude` appears.
- The list contains only names, gateways, and models; keys / tokens in aliases never leave node. “View definition” displays the alias text from `whence -f`, with keys redacted as usual.
- Function-based launchers still require `_claude_launchers` to list them: the final program in a function body cannot be determined without running it (see “Parameters on the launcher page” below).
- **Launchers with their own `--settings`** (`alias work="claude --settings ~/.claude/settings.work.json …"`, or a function invoking claude this way; gateway, token, and model live in that file's `env`):
  Claude Code recognizes only the **last** `--settings`, so Persona does not use `--settings` for its own hooks (those routing questions, approvals, and completion to the phone). With local Claude Code ≥ 2.1,
  hooks are a plugin loaded only for this session (`--plugin-dir ~/.persona/claude-hooks`, `assistant-hooks` for the assistant, `node/hooksplugin.go`); the launcher's own `--settings`
  works unchanged for aliases and functions alike. Persona passes `--settings` only to launchers it manages (see “Running Persona-managed launchers” below).
  Older Claude Code (or an unreadable version) can load hooks only through `--settings`. At startup, node merges the alias settings and Persona settings into one file (all alias settings + both sets of hooks,
  Persona's last), writing `~/.persona/launcher-settings/<launcher>-<claude|assistant>-settings.json` (0600, contains tokens) and passing it as the last `--settings`.
  It rewrites the file on every start (`node/launcher_settings.go`). It recognizes `~`, `$HOME`, relative paths (from the session directory), and `--settings '{…}'`; unreadable files fall back to Persona settings alone.
  A function-based launcher's `--settings` is not visible and still gets overridden on these claude versions.
- Model listing (`node/launcher_models.go`) passes `--settings '{"disableAllHooks":true}'` to avoid triggering hooks (alias and managed-launcher settings are still merged). A function's own
  `--settings` is still overridden here, so the listed models omit its effects: disabling hooks can only be specified in `--settings`, and merging requires first reading the settings supplied by the function (#1411).
  On Unix, model discovery for a Persona-managed Claude Code provider invokes `personad launch` directly for native
  CLI binaries and for npm scripts whose Node.js is already available. For npm installations, including Node Version
  Manager (NVM) and Homebrew prefixes, the probe uses the matching Node.js executable when it finds one and keeps the
  child's search path in its original order. Other launcher scripts and CLIs that need shell setup run through the
  user's interactive shell.

## How herdr recognizes Agents started by launchers (`HERDR_AGENT`, #3454)

herdr identifies Agents by the name of a pane's foreground process (basename `claude` / `claude-code`, `codex`); an unrecognized name is not reported as an Agent. When a launcher wraps and execs a differently named program
(for example, a Python script execs a version-named native binary `claude-2.1.318`), Claude's input box is already on screen, but node never receives herdr's “this is claude” report
and fails only after 3 minutes. herdr's wrapper mechanism is `HERDR_AGENT=<agent>`: before checking names, it reads the foreground process's exec-time environment (Linux `/proc/<pid>/environ`, macOS `KERN_PROCARGS2`).
Supported since herdr 0.7.1 (Linux) / 0.7.5 (macOS); ignored by older herdr and on Windows.

- When starting Claude in a herdr pane, node types `HERDR_AGENT=claude <launcher> --session-id … --mcp-config …` (for `claude_start_dir`,
  `HERDR_AGENT=claude CLAUDE_CONFIG_DIR=… claude …`; for Persona-managed launchers, `HERDR_AGENT=claude personad launch <name> …`). This also applies to panes chosen with “Run Agent here.”
  `resume_argv` used after herdr restarts (`eval "… --resume <id> …"`, `node/herdrresume.go`) has the same prefix. Launchers declared by hub as Codex use `HERDR_AGENT=codex <launcher> …`;
  see `docs/integrations/codex.md`, “Starting from the phone” The internal Codex fork and pi get no prefix (herdr has no names for them). Other engines (Orca…) and headless runs (assistant, availability tests, model listing) get none either.
- Prefix only shells supporting `VAR=val cmd`: bash, zsh, fish (since 3.1), sh, dash, ksh, mksh. For a new tab, the shell interpreting this line is the one herdr uses to open the pane
  (herdr uses its own `$SHELL`: node-started herdr gets the user's login shell, `userShell` in `node/herdrstart_unix.go`; it does not read `[terminal] default_shell` from herdr configuration).
  `resume_argv` follows that shell too (herdr opens a new pane and types `eval …` on resume); “Run Agent here” uses the pane's foreground shell. tcsh / csh, pwsh, nu, elvish, xonsh,
  or unknown shells still get unprefixed commands, preserving previous recognition behavior. On Windows, node does not type the prefix (herdr does not read it, and `codex-start` does not set it).
- Apply it only before the Agent command: the shell passes it only to that command's process, without retaining it afterward. **Do not** `export` it into the pane shell or set it in herdr tab / workspace environments:
  `HERDR_AGENT` in the shell's own environment is authoritative to herdr (even a later `unset` cannot remove it), making everything subsequently run in that pane look like Claude. Do not use `env HERDR_AGENT=claude <launcher>` either:
  `env` cannot resolve functions or aliases.
- Cases where claude receives it (tested with bash 5.2 / 5.3, zsh 5.9, fish 4.6, dash): programs on PATH, functions, aliases (including variable-prefixed `alias x='A=1 claude …'`), and `eval` of the same text.
  `node/herdragent_shell_test.go` runs node's commands in real shells (bash; also zsh and fish when installed), asserting that claude receives `HERDR_AGENT=claude` and the arguments, but the shell and next program do not retain it.
- Cases where it does not reach claude (still unrecognized as before, no worse): aliases expanding to another command first (`alias x='export A=1; claude'`, `alias x='cd ~/w && claude'`), where the prefix goes to `export` / `cd`.
  With zsh `POSIX_BUILTINS` (or bash `--posix`), the variable before `export` remains in the shell as an unexported variable, harmlessly.
- With zsh `POSIX_BUILTINS` and a function-based launcher, `HERDR_AGENT=claude` **remains exported in that pane's shell**. After claude exits, herdr treats later programs in the pane as Claude too.
  This option is rarely enabled; if it is, write the launcher as an alias or script.
- Wrappers that exec claude with argv[0] still `claude` were already recognized by herdr; the prefix changes nothing.
- If recognition still fails, see `docs/lessons/agents.md`, “claude did not become ready…,” for node's error.

## Work agents: pi, the Cursor CLI, the Antigravity CLI

pi, the Cursor CLI (`cursor-agent`) and the Antigravity CLI (`agy`) are agents the user works with, started in a herdr pane the way Claude and Codex are (`server/internal/node/workagent.go`). A machine lists each as a launcher when its program is on the login shell's PATH (`pi`, `cursor-agent`, `agy`), so New Agent and `task_plan` / `agent_start` offer it there and nowhere else. `launcher` is the agent's name (`pi`, `cursor`, `agy`); `model` is passed as `--model` (a provider and model, such as `ag-chat/cursor/grok-4.7` for pi). pi and Cursor take no permission mode, and none of them takes ultracode.

**agy's permission mode is its start's.** An agy start runs in Allow all (`--dangerously-skip-permissions`) unless it names another mode: `acceptEdits` passes `--mode accept-edits`, `auto` passes `--mode auto`, and `default` passes no flag (agy's own approval). Like Claude Code, an agy worker from `agent_start` or `task_plan` gets Allow all when nothing names a mode (`startAgent.allowAll`), since nobody at the pane answers agy's approval prompt; a mode the call or plan names stays. The row reports the mode it started in as `permission_mode`, and a herdr restart resumes it with the same flag. The mode, effort and model cannot be changed afterwards: `session.modes` on an agy or Cursor pane is refused with `modes_unsupported` (409) and no key reaches the pane, and their rows say `capabilities.modes: false`, so no client offers the chips. agy cycles its own modes with shift+tab, but its footer is not Claude Code's and Allow all is a start flag outside that cycle, so the node does not drive it. Antigravity's runtime (`agy acp`, the companion conversation) keeps its own mode switch.

A managed Antigravity launcher with a key (`GOOGLE_GEMINI_BASE_URL` / `GEMINI_API_KEY`) also decides how the Antigravity companion (Agents without a terminal, agy as the assistant, the model list) signs in: the node gives the companion that launcher's variables and `PERSONA_AGY_MODEL_PROVIDER=gemini`, and the companion then uses the Gemini API key provider instead of a Google account (`docs/integrations/runtimes.md`, "Gemini API key provider", #13762). With several such launchers the first by name is used (fetched ahead whatever its delivery, #13871), and removing the key (or the launcher) restarts the companion on the sign-in again.

For a one-shot Antigravity model request, an interactive sign-in prompt ends the run with `model_run_logged_out`. A complete answer received before that prompt is returned. If the request reaches its deadline first, the node reports `model_run_timeout` even when a launcher child still has the output pipe open.

**Creating one** (`POST /api/v1/launchers`, a full-device token): `{"kind": "launcher", "agent": "agy", "name": "agy-gw", "base_url": "https://<gateway address>", "api_key": "<key>", "model": "<an agy model id the gateway serves>"}`. `model` is required (it is the launcher's default; pick one from `agy models`, such as `gemini-4-neon-high`), `auth` is not used (agy sends the key in `x-goog-api-key` itself), and `name` may be left out. The key is stored sealed and never sent back. A herdr pane start or resume of that launcher types `personad launch agy-gw …`, which fetches the launcher's `GOOGLE_GEMINI_BASE_URL` / `GEMINI_API_KEY` from the node (launch key, as for a managed Claude launcher) and execs `agy --settings '{"modelProvider":"gemini"}' …`; the key is in no typed line, no argument and no resume command. An `--settings` the pane's own arguments carry is kept as it is.

For herdr starts, the Antigravity model picker reads the local launcher's `models` output and uses the standard catalog cache and refresh. Picking an effort passes `--effort` with one of `low`, `medium`, `high`, `xhigh` or `max`. When a model ID already names an effort variant, an explicit effort selects the corresponding catalog variant. Persona reports an unavailable variant before opening the pane. An effort without a model uses the launcher's default model. pi and Cursor keep their own effort.

The first message is never typed into the pane:

- **pi**: through the Persona extension in that pi process (`session.send`, `docs/integrations/pi.md`). If the extension does not list the session within 30 seconds, Persona uses bounded, best-effort agent cleanup and leaves the tab at the shell; a user-picked pane with ambiguous ownership is left untouched. The message is not sent (`pi_no_extension`), so a start never falls back to typing into pi's input.
- **cursor and agy**: through herdr's `agent.prompt`, once herdr reports the agent. herdr 0.9.5 detects both (its integrations `cursor` and `antigravity-cli`) and reads `HERDR_AGENT` for a wrapper that execs them under another name. The phone shows the terminal and can send messages and interrupt.
  herdr reports the agent, and calls it idle, as soon as its input box shows, and `agent.prompt` only types. agy is still signing in at that point, and it can drop a message typed then (#11345). So the start checks in the background that the agent took the message (`server/internal/node/workagent_first.go`). It counts as taken when herdr calls the agent working, blocked or done, or when herdr's Antigravity integration reports the agy conversation. If the agent stays idle and the screen shows neither the message nor a `[Pasted text …]` chip, the message is typed again, up to three times in all. Otherwise the session is listed as needing the user, with the reason, instead of idle. Messages sent to the session while the check runs wait for it.

Neither keeps token counts the node can read, so their usage is not on the token calendar (`docs/server/machines.md`, "Token usage"). A herdr restart starts Cursor again without a session. Pi's resume names its session (`pi --session`). Pi runs with `--approve` on start and resume, so a directory with project-local files does not stop it at a Trust prompt nobody answers (`docs/integrations/pi.md`, "Project trust"). An agy start answers agy's folder-trust dialog ("Do you trust the contents of this project?") by trusting the folder the start chose, as Claude's and Codex's starts do, before its first message goes in; the watch is bounded, and a start with no dialog types nothing extra (`server/internal/node/agytrust.go`, #11424). Resuming one of these sessions from the phone with `session.start` is refused (`work_agent_resume`): a stopped pi resumes in its own herdr tab when the phone sends it a message (`docs/integrations/pi.md`), and the others are resumed on the computer.

**Antigravity CLI conversation history and restore.** Persona's own agy hooks (installed by the node; see `personad node doctor`, line `agy-hooks`) or `herdr integration install antigravity-cli` report the pane's conversation. Once Persona verifies that conversation ID, it reads that conversation's local history. On a herdr restart, a Persona-started pane uses `agy --conversation <id>` to reopen the verified conversation and preserves the selected model. Until Persona has a verified ID, the pane restores with `agy` and the original `--model` option if set, starting a fresh conversation. The phone can still send messages, interrupt, and use the terminal while it waits for the conversation.

**Where the assistant starts agy.** A New Agent with the computer's default host starts agy in a herdr pane, even while the Antigravity companion is connected (#10237): someone is at that terminal. A `task_plan` task or an `agent_start` that names no `host` starts it on the companion instead, whenever the computer's launcher list says the companion runs that launcher (its own `agy`, or a launcher kept in Persona that starts agy while the companion is listed). Nobody sits at an assistant-dispatched pane, and a companion session gives its supervisor the conversation as it goes, approval cards, Persona's tools and token usage (`server/internal/assistant/start_host.go`, #13447). `host: "agy"` asks for the companion, and `host: "herdr"` (or another engine host the computer enables) keeps the pane. A start naming `agy` while no companion is connected is refused with `agy_companion_offline` before anything opens (`server/internal/node/starthost.go`); one the hub sent there by itself then starts again in the default host, so the task still runs. An Agent another Agent starts (`a2a_send`'s new Agent, `dispatch_start`) has nobody at its pane either and starts on the companion the same way (#14887); when the companion went away, a writable start goes to the default host and a read-only one fails with the node's reason, since no pane holds it read-only.

**The Cursor CLI on a Mac over SSH.** `cursor-agent` keeps its sign-in in the macOS keychain. A pane opened inside an SSH session (or by a launchd job that is not the GUI user's) finds the keychain locked and asks for it, so the agent never becomes ready and the start fails. Run it in the GUI session: herdr itself started there (the node service of a logged-in user), not `ssh mac cursor-agent`. The same applies to anything else the CLI reads from the keychain.

What these agents do not do here, and why:

- No conversation in the app for Cursor. Herdr drives the pane, and Cursor's conversation stays in the terminal.
- No phone approval cards for cursor and agy in a pane. pi's come from its extension; the other two have no Persona hook, so their own dialogs stay on the terminal (agy on its companion asks through Persona's cards).
- Supervision (`report_to_supervisor`, done checks) reaches an agent Persona can send to. pi qualifies once its extension is in; cursor and agy qualify for a message through herdr, and nothing more.
- **No read-only start in a pane.** Claude Code runs a read-only task with `bypassPermissions` and the constraint written into its first message, and Codex with `--sandbox read-only`; these three have neither in a terminal, and a prompt-only constraint would not hold an agent whose own tools write files. (agy on its companion is allowed: a read-only agy task the companion runs starts there with `bypassPermissions` and the first-message constraint like Claude Code, `docs/assistant/todos.md` "Read-only planned tasks".) So a `task_plan` task with `readonly: true` for one of them is refused when it is planned (#8316, `server/internal/assistant/tasks_placement.go` `readonlyLauncherRefusal`): `readonly_unsupported`, naming that computer's Claude Code and Codex launchers, so the task can be planned writable or on one of them. Nothing is saved or started, it is never started writable, and Persona does not substitute another launcher. The same check runs before a start (a card edited to one of them, a plan saved earlier), and the node still refuses a plan-mode start of theirs that reaches it, before any pane opens (`readonly_unsupported`, `server/internal/node/control.go`). An `a2a_send` launch in `plan` (its default) or a `dispatch_start` that Model configuration starts in `plan` is refused the same way when the tool is called, before its A2A task is recorded, and again when the start goes out (#14887, `server/internal/assistant/start_host.go`).

## Speed

A new terminal Agent can take an explicit `speed`: `standard`, `fast` or `ultrafast`. The assistant supplies it on `agent_start` or a `task_plan` task, or stores it on an Agent preset (`ModelTier.speed`, edited through `persona_settings` key `model_profiles.tiers`). An explicit start value wins over the preset. A different launcher or model leaves the preset's speed behind, just as a different model leaves its effort behind. Omitted and `standard` send no override: the launcher's own setting applies, and no machine or global Codex / Claude Code default changes.

- **Codex:** `fast` becomes `-c service_tier=priority`; `ultrafast` becomes `-c service_tier=ultrafast`. Before launch the node checks that the selected model's own catalog advertises that native tier. An invalid or unadvertised choice is refused rather than letting Codex run it silently; the rollout's `turn_context` records the native `service_tier`.
- **Claude Code:** `fast` adds `"fastMode": true` to a merged, per-start `--settings` file, preserving the launcher's effective settings. `ultrafast` is refused. If a shell function supplies `--settings` that cannot be merged, `fast` is refused before launch rather than replacing them. Claude Code itself falls back to standard when its account or model cannot run fast mode (currently fast mode requires Opus on the first-party API with usage credits), so Persona cannot guarantee the faster output speed.
- **Everything else and resumes:** faster speed is refused before approval or launch. This includes work agents, TRAE CLI, omp, external runtimes, Paseo providers, desktop apps and every resume. A node without `session_start_speed` returns `node_outdated` for `fast` or `ultrafast` instead of silently starting standard.

Speed is start-only: it is not kept in the execution fingerprint or herdr's automatic-resume command, and a later resume uses the launcher's own setting. The current New Agent form and plan cards do not show or edit speed; the launcher-list and worker-preset responses expose it for clients and extensions.

## Running Persona-managed launchers

- Stored in settings `launchers.managed`; the manager AES-GCM encrypts API keys with its own `secret.key` (the built-in manager's `~/.persona/managers/assistant/secret.key`, the same key used for email credentials, `server/internal/secretkey`). **The API never returns keys**.
- Which computers get a provider, and when its key travels, is the provider's device scope (next section). A computer in scope gets the provider's name and settings without secrets on connect and after every change (`launchers.catalog`); its key, headers and environment come only when the node asks for them (`launchers.pull`). node keeps them only in memory; hub neither stores nor decrypts them.
- At startup, node types `personad launch <name> …` into a herdr pane (or executes it for the headless assistant). This subcommand gets settings through local node.sock
  (`launcher.env`), removes inherited `ANTHROPIC_*` and related shell variables, sets `ANTHROPIC_BASE_URL`, the key (see “Authentication method” below), and model tiers
  (`ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_*_MODEL`; the small model goes to haiku / small fast; the sub-Agent model to `CLAUDE_CODE_SUBAGENT_MODEL`, following the primary model if empty;
  the session's model with `[1m]` when its `context_window` in `models` is over 200K, a tier, small or sub-Agent model set to a model of its own when that window is 1M or more;
  custom model options go to `ANTHROPIC_CUSTOM_MODEL_OPTION` and its name / description), then execs claude.
  Keys never enter the terminal or command line. You can also type `personad launch <name>` directly in a terminal.
  With `--ticket <ticket>` (`personad launch --ticket <ticket> <name> …`, or `personad launch --ticket <ticket> -- claude …` for plain
  claude) it also fetches the values a hand-off card approved for that one start (node.sock `secret.env`, with the launch key and the
  one-time ticket) and puts them in claude's environment; `docs/extensions/secret-providers.md` "The hand-off to one Agent run".
- **Only `personad launch` gets the settings** (#5588). Every agent's hooks and MCP servers have to reach node.sock, so being able to connect proves nothing.
  `launcher.env` answers only a request that carries the machine's launch key, a random value the node keeps in `launch.key` beside node.sock
  (0600, written when the node starts listening, kept across restarts). `personad launch` reads the file and sends the key, so every start works as before:
  from the node, typed into a herdr pane, herdr's `resume_argv` after a herdr restart, and typed by hand in a terminal.
  Any other request gets one error, whatever launcher it names, with none of the settings in it. That includes a process that cannot read the data directory
  (an agent in a sandbox that hides it, #5357) and a `personad` older than the node, which does not send the key (run the node's own binary instead).
  - The check is exactly as strong as the file boundary. A process of the same user outside such a sandbox can read `launch.key`, but it can also read the
    `launcher-settings` files below, which hold the keys themselves. A sandbox for agents must keep `launch.key` hidden along with `secret.key`,
    and must start where `personad launch` execs the agent, not around `personad launch`, which has to read the key first.
  - Why not a one-time nonce from the node: it cannot reach a launch typed in a terminal, or herdr's stored resume command, which herdr runs on its own after it restarts.
    Checking the peer process (uid, pid ancestry) adds nothing either. The socket is already 0600, `personad launch` runs whatever `claude` comes first in PATH, and a herdr pane's
    start shares its ancestry with the agents' own tool processes.
- Settings `env` overrides process environment. A different gateway in the user's global `~/.claude/settings.json` `env` would override these variables, so Persona also writes the same variables (including the key)
  to `~/.persona/launcher-settings/<name>-env-settings.json` (0600) and passes it through `--settings` (`node/launcher_settings.go`). On older Claude Code, this is merged with
  Persona hooks in `<name>-<claude|assistant>-settings.json`. These files are rewritten on every start; manual edits are lost.
- **A proxy that does not answer** (#5953): a provider with no `proxy` of its own keeps the proxy the shell gives it, since a gateway may be reachable only through it.
  But a proxy app that has quit, still named in the shell or in the Claude config directory's `settings.json` `env`, held every request until it timed out.
  So when the gateway itself takes a direct TCP connection (its Base URL's host and port; none: `api.anthropic.com:443`), `personad launch` drops an inherited
  `HTTP(S)_PROXY` / `ALL_PROXY` (either case) whose address takes no TCP connection within a second, twice, and says so on stderr; the settings file above sets such a
  variable from `settings.json` empty (settings `env` cannot unset), noted in the node log.
  A gateway that does not answer directly either keeps every proxy: nothing would be gained, and the proxy may come back while the session runs.
  Variables the launcher sets itself (`proxy`, `env`) are never touched (`node/deadproxy.go`).
- **Artifacts are always disabled**: claude started by a Persona-managed launcher gets `CLAUDE_CODE_DISABLE_ARTIFACT=1` (both in `personad launch` environment and the settings `env` above).
  The settings file also sets `"enableArtifact": false` (Claude Code honors disabling it through `--settings`). Artifacts publish to claude.ai and cannot work with a gateway + API key anyway.
  Since 2.1.26x, Claude Code enables them by default, adding Artifacts skill instructions to system messages in interactive sessions. Some gateways reject this in capability checks
  (`400 request is not representable on this edge: Claude Code Artifacts are enabled`), failing a new herdr Agent's first turn; `claude -p` is unaffected.
- **No automatic refusal fallback to a model the gateway lacks** (#7319): a provider of #2455's shape with a Base URL whose host is not Anthropic's own API, and whose `models` list is not empty
  but lacks one of the models Claude Code falls back to (`claude-opus-4-8`, `claude-opus-5`, `claude-sonnet-5` as of Claude Code 2.1.322; compared trimmed, in any case, without `[1m]`), gets
  `CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK=1` (both in `personad launch` environment and the settings `env` above), so a refused reply ends on its refusal instead
  of moving to a model the gateway does not serve. A provider with no `models` list, or one that lists all three, keeps Claude Code's fallback. The rule reads the declared list, not what the
  gateway serves: a provider imported from a computer's aliases lists only the model its alias names, so it has the fallback off until its list names all three or its `env` empties the variable. The provider's `env` wins either way:
  `CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK=` keeps the fallback, `=1` turns it off (`docs/integrations/claude-integration.md`, "Refusal fallback behind a gateway").
- **Tool search off behind a gateway** (#10365): a managed provider whose Base URL (its own `env`'s `ANTHROPIC_BASE_URL`, else the stored one) is not Anthropic's own API gets
  `ENABLE_TOOL_SEARCH=false` (both in `personad launch` environment, which then drops an inherited value, and the settings `env` above): every tool is loaded up front, and no request carries
  `defer_loading` tools, `tool_reference` blocks or the `ToolSearch` tool, which a gateway edge may refuse (`400 request is not representable on this edge: ToolSearch is not supported on this endpoint`).
  Claude Code keeps tool search off behind such a Base URL by itself, but any value of the variable lifts that rule, and the assistant's processes carry one. The provider's `env` wins:
  `ENABLE_TOOL_SEARCH=true` opts back in (deferral saves about 23K tokens per request where the gateway carries it); a refusal is then resent once with every tool loaded and remembered for that
  provider and model (`docs/integrations/claude-integration.md`, "Tool search behind a gateway (#10365)").
- **No tier alias behind a gateway** (#10365): Claude Code sends a tier variable's value (`ANTHROPIC_DEFAULT_OPUS_MODEL` …) to the gateway as the model's name, so a provider whose default model or tier
  field held `opus` asked the gateway for "opus" (404). For a provider whose Base URL is not Anthropic's own API, a tier alias (`opus`, `sonnet`, `haiku`, `fable`, `best`, `opusplan`, `default`, `inherit`,
  with or without `[1m]`) in its default model, a tier field or its `env` counts as unset: the tier's own model, else the default model, else nothing (Claude Code's own ID). A `--model <alias>` the node
  passes (an assistant conversation, a terminal Agent, a launcher switch, Test, a typed `/model`) becomes the model the provider's environment gives that tier, `[1m]` kept, or is left out (the provider's default;
  a typed `/model` types `/model default`); `personad launch` does the same to whatever reaches it and says so on stderr when it leaves one out. Plain `claude`, shell launchers and providers on Anthropic's own API keep their aliases.
- **Authentication method** (`auth`, 2026-11-04): which variable receives the key. `api_key` (default, used by existing launchers) sets `ANTHROPIC_API_KEY`; Claude Code sends
  `x-api-key`, the only method accepted by the official API. `bearer` sets `ANTHROPIC_AUTH_TOKEN` and sends `Authorization: Bearer`, for compatible gateways accepting only Bearer
  (common relay gateways accept both, requiring no change). When the manager directly calls the Messages API with a launcher (triage, translation, etc.), it follows this header choice too; Responses already uses Bearer and needs no selection.
  Editing a launcher without `auth` in the request (an app not yet upgraded) preserves the old value.
- **Persona request identity**: Persona's direct API calls, connection tests, model discovery and quota requests send `User-Agent: Persona/<version>` by default. A provider's custom `User-Agent` takes precedence where custom headers are supported. Quota requests use the quota extension's own `User-Agent` when supplied and do not inherit provider headers. A direct API redirect to another origin and subsequent redirects use Persona's default identity to keep the custom value private; Agent processes send their own headers.
- **OpenCode Go session routing** (#10014, `server/internal/modelhttp/opencode.go`): OpenCode Go subscription gateways (`opencode.ai` and `*.opencode.ai` under `/inference/go` or `/zen/go`) reject requests missing `x-opencode-session` with HTTP 400 `MissingSessionID`. Manual custom headers use the general per-provider field described above. When a request targets an OpenCode Go gateway and carries no `x-opencode-session`, Persona attaches a UUIDv4 session identifier automatically across direct API calls (`server/internal/ai`: derived from `Request.SessionID` or `WithSessionID(ctx)` when present, otherwise stable per client instance), connection tests and model discovery (`assistant/models_probe.go`), availability checks (`POST /api/v1/launchers/{name}/check`), and managed launcher environments (`ANTHROPIC_CUSTOM_HEADERS` in `node/launcherenv.go` for Claude Code; `PERSONA_OPENCODE_SESSION` + `env_http_headers` in `node/managed.go`, catalog probes in `node/codex_catalog.go`, and `node/pi_providers.go` for Codex and pi). An explicit `x-opencode-session` header where supported takes precedence, and cross-origin redirects drop the header. OpenCode Go is a coding subscription rather than a pay-as-you-go vendor endpoint, so it is supported natively by URL detection rather than listed in `presets_builtin.json`.
- **Testing sends what a launch sends** (#5329): `POST /api/v1/launchers/test` and `/discover` put the key in one header only, the one the launch's variable makes Claude Code send:
  `auth: "api_key"` → `x-api-key`, `"bearer"` → `Authorization: Bearer`; without `auth`, a stored provider (`name`) uses its stored method (the predicate `personad launch` uses) and a preset its own.
  A test that sent both headers used to pass for a gateway that then refused the launch with 401. Responses and Chat Completions probes send Bearer only, as their real calls do.
  A request with no `auth`, no `name` and no `preset` (an app from before #5329 typing a gateway) still sends both, as before, and so does a stored Codex or Antigravity
  launcher by name, whatever `auth` says: they launch with `OPENAI_API_KEY` / `GEMINI_API_KEY` (`node/managed.go`), which `auth` does not choose, and the web wizard does not ask them.
- **Finding the method** (`auth: "auto"`, feature `launcher_auth_auto`): the probe sends Bearer first and, when the gateway answers 401 or 403 (not Anthropic's region block),
  the same request again with `x-api-key`. A passing test carries `auth` (`"bearer"` or `"api_key"`), the header that worked; a gateway that accepts both answers `bearer`,
  the web wizard's earlier default for a custom gateway and the variable most compatible gateways document. Any other failure is not retried and carries no `auth`;
  both refused answers the `x-api-key` attempt's failure, except a 401 after Bearer's 403: the gateway knew the Bearer key and forbade something, which says more.
  Without a key no header goes, so there is nothing to find (no retry, no `auth`). Discovery settles the header on the first page and keeps it for the rest, but names none: a model list
  may answer without looking at the key, so only a test says what a launch needs. `auth` is never stored as `auto`: the client saves the method the test found.
- **Set the other authentication variable to empty**: Claude Code merges `env` from all settings sources. Variables the launcher **does not set** still come from the user's `~/.claude/settings.json`.
  If that contains another gateway's `ANTHROPIC_AUTH_TOKEN`, Claude sends it as `Authorization: Bearer` alongside the launcher's `x-api-key`. The gateway receives two credentials
  and returns 401 `invalid or missing API key` unless one happens to match. So launchers with a key always set the unused variable (`ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY`)
  to empty, both in the environment and the `--settings` above. An explicitly empty variable is not refilled from user settings (tested with Claude Code 2.1.308 on 2026-11-04). Without a key, neither is set, as before.
- Interactive Claude Code asks “Do you want to use this API key?” the first time it sees a key in `ANTHROPIC_API_KEY`, with “No (recommended)” selected by default.
  Enter (or selecting the default on a phone dialog card) records the key as rejected, leaving later sessions from this launcher without credentials. The user entered the key themselves in Settings › Models,
  so before execing claude, `personad launch` records it as approved, equivalent to selecting Yes in the dialog: the key's last 20 characters (Claude Code's key identifier) are written to
  `customApiKeyResponses.approved` in Claude's global configuration (`$CLAUDE_CONFIG_DIR` or `.claude.json` under home; old `~/.claude/.config.json` when in use),
  and removed from `rejected` (`node/apikeyapproval.go`). Writes use Claude's own configuration lock
  (`<config path>.lock` directory, stale after 10 seconds), avoiding concurrent writes with running claude. Already approved keys are not rewritten; no write occurs before a configuration file exists (first run,
  when Claude handles onboarding). Write failures print one line to stderr and startup continues. Headless `-p` (assistant) does not ask in the first place.
- Before settings arrive after a hub restart or node reconnection, `personad launch` reports “hub 还没发过来” (“hub hasn't sent them yet”). A provider out of this computer's scope is never named to it, so the same message appears.

## Device scope and on-demand delivery (#2333)

A key bought for one computer (an isolated container the assistant runs on) should not reach every other computer, nor show in their pickers. Each Persona-managed provider says where it may run and when its secrets travel (`server/internal/assistant/models_launchers_scope.go`, node `server/internal/node/managed.go`):

| Field | Values | Meaning |
| --- | --- | --- |
| `share` | `all` / `selected` / `none` | `all`: every confirmed personal computer (work computers, `trust: "work"`, only when listed in `scope_machines`). `selected`: only the computers in `scope_machines`. `none`: no computer; the key stays with the manager (direct API calls such as triage and translation still use it) |
| `scope_machines` | machine ids | With `selected` the computers it runs on; with `all` the work computers added to it |
| `delivery` | `on_demand` / `push` | `on_demand`: a computer gets the key the first time it needs it, then keeps it in memory. `push` (“Keep a copy on each computer”): every computer in scope fetches it as soon as it connects, so it still launches after the hub becomes unreachable |
| `own_service` | `skip` / `use` | `skip`: not used on a computer whose harness already has a usable service of its own (below); `use`: used there too |
| `machine_keys` | `{machine id: key}` (write only) | A key for one computer that replaces the provider's key there (`""` removes it). Reads return only `key_machines`, the ids that have one |

- **Invariants**: secrets go only to a computer that is in scope, confirmed (not pending, `docs/protocol/protocol.md` “Computer trust”) and connected to this manager; the API never returns keys. A computer out of scope receives nothing about the provider (not even its name), does not list it in `GET /api/v1/machines/{mid}/launchers` or new-Agent pickers, and every start with it there (main chat, Agents, tasks, scheduled runs, the availability test) is refused by the manager with `launcher_out_of_scope`; Model configuration's status explains it for a purpose bound to that computer.
- **Defaults**: a provider added now is `all` + `on_demand` + `skip`; `use` when it is added with `assign` (it is meant for the main chat and new Agents right away, even where Claude Code is signed in), is a direct API connection (`kind: api`), or is imported from a computer's aliases (the alias in the rc file makes that computer count as having a service of its own, so `skip` would keep the provider off the computer it came from). Records saved before these fields existed are read as `all` + `push` + `use` **including work computers**, exactly the old behavior (`legacy_scope: true` in reads); saving any of the four fields makes the record explicit. A `PUT` without them keeps what is stored.
- **A usable service of its own** (`own_service`), per harness, read by the node with the checks it already has and cached ten minutes (`launchers.own_service`, `node/ownservice.go`):
  - Claude Code: plain `claude` signed in or given a key or gateway by its environment (`claude.auth_status`, `node/claudeauth.go`), or any Claude launcher in the login shell's rc file or PATH (`launchers.list`).
  - Codex: `codex login status` succeeds (ChatGPT or API key login), or a Codex alias in the rc file.
  - Antigravity, Oh My Pi: not detected; they always count as having none.
  Unknown (an old node, a timeout) counts as none: the rule is a convenience, the scope is the boundary.
- **Pull**: the node asks the manager with `launchers.pull {name, own_service?}` before it uses a provider whose catalog entry is `withheld`: a `session.start` / `assistant.send` / `assistant.warm` / `session.send`, the availability test, model listing, sign-in status, and `personad launch` (typed in a terminal or herdr's `resume_argv`). It reports its own service for that harness only when the provider says `skip`. The manager checks the connection's machine (never a field in params) against the scope, picks that computer's key, and answers the full `ManagedLauncher` (a key that is a reference resolved for that computer); otherwise it answers `launcher_out_of_scope`, `launcher_own_service`, or `launcher_key_unresolved` when the key's reference did not resolve, in the user's language. A `push` provider is pulled in the background right after each catalog.
- **pi** (#8194): pi uses the providers in its computer's scope too, without a `models.json`. pi's Persona extension asks the node (node.sock `launcher.pi`, gated by the launch key exactly like `launcher.env` above); the node pulls each provider pi can use (`proto.ManagedLauncher.PiAPI`: a Messages provider for Claude Code or Oh My Pi, a Codex provider; not Antigravity, a Responses or Chat Completions API connection, or a provider with a proxy of its own, which pi would not go through) the same way a start does, own-service rule included, and answers with the keys. The default model comes from the manager (`launchers.agents_default`: Model Settings › Agents when it names such a provider in this computer's scope; names only). Reads mark these providers `pi: true` (`GET /api/v1/launchers`, `GET /api/v1/machines/{mid}/launchers`), shown as "pi too" in the web list and "pi can use it too" in New Agent's model panel. Details: `docs/integrations/pi.md` ("Persona's model providers").
- **Cache**: what a node pulled stays in its memory until the next catalog says the provider changed (`rev`) or left its scope, or the node restarts. Nothing is written to disk beyond the per-start settings files above.
- **What degrades when the manager is unreachable** (the node's link to its manager is down: hub machine restarting, network): a provider this node already pulled since it started, or any `push` provider, still launches (`personad launch` by hand, herdr's `resume_argv` after a herdr restart). An `on_demand` provider not used since the node started fails with “cannot reach the hub to fetch the settings”; a node restarted while the manager is away has nothing, as before. Choose `push` for providers that must survive that.
- **Rolling upgrade**: a node without `launchers.catalog` (it answers `unknown method`) gets `launchers.managed` as before, but only with the providers in its scope, keys included, so on-demand and the own-service rule apply only once it is updated. An old manager keeps pushing `launchers.managed` to new nodes, which still take it (every entry there is full).
- Per-computer keys live in their own setting `launchers.machine_keys` (`[{launcher, machine, key_sealed}]`, sealed and re-sealed with the providers' keys); deleting a provider deletes them.
- A key that is a reference keeps the value its secret provider gave, sealed (`secrets.launcher_kept`), until the key changes, the provider is deleted or the secret provider goes or says it is gone; editing anything else asks nobody ([secret-providers.md](../extensions/secret-providers.md) "Launcher keys").
- Web: Settings › Model providers › a provider › “Devices” (`web/src/views/settings/ProviderScope.tsx`), grouped by harness in the list.
- iOS / Mac: Settings › Model Providers › a provider › Computers (`apple/Persona/Views/Settings/ProviderComputersPage.swift`, the form and what a save sends in `apple/Persona/Views/Settings/ProviderScopeForm.swift`), a row next to Proxy in the wizard's Advanced, on the preset key step and in the older form; the detail page says the scope and how many computers have a key of their own, and the list groups by harness when there is more than one. Demo Mode takes scopes too. `-openProviderComputers all|selected|none|legacy|keys|api` opens a made-up provider's page on the hub's computers (`new` with `-openProviderWizard models`); `via` opens an API connection that sends through the first computer at Connect (Send Through, `docs/assistant/model-config.md` "Sending through a computer").
- Android: Settings › Model providers › a provider › Edit, or its “Computers” row (`android/feature/settings/src/main/kotlin/persona/feature/settings/ProviderEditScreens.kt`, the form in `ProviderScopeState.kt`); a new provider's “Computers” is under “Advanced” (“More options” on the key step). Grouped by harness in the list.

## Parameters on the launcher page (`node/launcherprobe.go`)

Reading only the text of a ~/.zshrc function cannot reveal what helpers such as `_claude_proxy URL TOKEN MODEL WINDOW` or local variables ultimately set. node therefore actually runs the launcher in a
subshell, replacing `claude` / `codex` / the internal Codex-fork CLI / `dsh` / `command` / `env` with stubs that print only the environment and arguments the Agent would receive. Comparing these with the shell's own
environment counts only changes made by the launcher (a launcher running `dsh` is recognized as DSH; DSH is not actually started, and it is no longer launched as Claude).
`command env … claude` is read like `env … claude`: the `command` stub hands `env` to the `env` stub. It used to pass it to the real `/usr/bin/env`, which started the real claude and left the launcher unseen (#5972).
If Claude arguments include `--settings` (`alias work="claude --settings ~/.claude/settings.work.json"`, last one wins, file or inline JSON),
that settings file's `env` overrides the detected environment (settings take priority, as in Claude Code). Gateway and key often live there; without reading it, the page misses the gateway and “Quota” assumes there is none (#1378).
If unreadable, behavior stays as before.

Probes run `( <name> )` in a login shell, so hub-requested probes, quota (`launchers.http`), `--model` checks, and model lists operate only on launchers known to this computer
(listed from ~/.zshrc, managed, runtime, `claude` / the internal Codex-fork CLI) or declared in hub.json (hub vouches for them with `configured`). Every other name returns “unknown launcher”
and is never executed as a command (for example `reboot`, #1185). Deploy hub before node when adding wrapper scripts in hub.json; old hubs do not send `configured`, so new nodes do not recognize them.

The page groups by computer (identical results merge into one group):

- Execution: Agent (Claude Code / Codex / internal Codex fork), gateway, variable holding the key (value not shown)
- Models: primary model, Fable / Opus / Sonnet / Haiku, small model, sub-Agent, with each variable name; Codex checks `-m`
- Context: window (`CLAUDE_CODE_MAX_CONTEXT_TOKENS`), auto-compaction window if different, thinking effort, request timeout
- Startup arguments and other settings (`ANTHROPIC_*`, `CLAUDE_CODE_*`, proxies…; key / token-like values appear as •••)
- Function text is collapsed; keys written directly in arguments (`_claude_proxy … sk-…`, `Bearer …`) are also redacted

**Files are read, not run** (#5954): the stubs are shell functions, which a script (another process) does not see, so running one would start the real claude.
So the probe asks the shell first what the name is (`whence -w` in zsh, `type -t` in bash): a file on PATH is reported with its path and not run, whether or not
the list has it (a `_claude_launchers` helper still printing the name of a function since replaced by a script, a list not read yet). Its probe comes from the
text instead (`scriptProbe`, `node/pathlaunchers.go`): claude, the script's arguments (expanded as above) with what the probe passes in place of `"$@"`, and
what the script sets (a value it does not spell out is left out, never sent to a gateway as written), with its `--settings` env on top as above. A file that is
no wrapper, or a claude in a branch, is "not seen". The page's text is the script's first 40 lines, keys redacted. What the page shows of a probe masks keys by
form and by name: an inline `--settings '{…}'` is shown as `{…}`, a header value such as `x-api-key: …` and a proxy URL's user and password as •••.

**Launchers that cannot be intercepted** (#1489): If a function calls another script that then `exec …/claude` by absolute path, stub functions cannot replace it, and the probe does not observe the Agent starting.
- The page says “This launcher's gateway and model could not be detected… It still works normally,” rather than treating it as broken.
- Sending and creation with a model (`launcherModelCheck`) use the old pre-probe path: pass the model to the launcher through `--model`, no longer returning 502.
- Launchers that can be intercepted and hardcode another model are still rejected.
- Note: the probe actually runs that script (the stub cannot replace it), and the script's claude is killed after 15 seconds.

A run executes whatever the launcher does before claude (such as fetching a token), so each launcher runs at most once per 10 minutes, with a 15-second timeout (killing children too),
stdin from /dev/null. Unit tests: `launcherprobe_test.go` uses `testdata/launchers.zsh` to cover helpers, small models and windows, variables before helpers,
subshell exports, several Codex forms, and `--settings` env (`withSettingsEnv`).

## Testing availability (`node/launchercheck.go`)

“Test availability” on the detail page actually sends a request requiring a one-word answer on every online computer with this launcher (for Persona-managed launchers, on the hub machine, chosen by the manager)
(`claude -p … --output-format json --max-turns 1`, `codex exec` for Codex and its variants only, `pi -p … --no-session` for pi, `-p` alone for the other agents; until #8194 every agent but Claude, Antigravity and Oh My Pi got Codex's flags, so a pi launcher failed with `Unknown option '--skip-git-repo-check'` and the health check called its CLI outdated). It runs in an empty directory, with a 150-second timeout and no hooks (`disableAllHooks` in `--settings`, still merged with managed-launcher env and the alias's own `--settings`: on some computers, Persona hooks stalled it until timeout, #3412). It displays ✓ / ✗, elapsed time, the answering model,
and the redacted failure reason. This costs a few tokens. `POST /api/v1/launchers/{name}/check?machine=`.
hub also uses this to check every launcher on every computer periodically, avoiding broken ones when dispatching work and automatically fixing what it can; see `docs/server/launcher-health.md`.

## Deleting

Tap “Delete launcher / Hide in Persona” on the detail page to show “Confirm deletion / Confirm hiding” at the button (popover, requested 2026-10-31); tap again to execute.

Swipe left to “Hide” ~/.zshrc launchers in the list, or “Restore” in “Hidden” (requested 2026-11-01), without another confirmation: this changes only visibility in Persona, and can be undone by swiping back.
Persona-managed entries swipe to “Delete,” removing the stored key too, with no restoration, so confirmation is shown first. After hiding, restoring, or deleting in details, the list updates too: all changes go through `AppModel.launchersChanged()`, and the list refetches on `launchersRevision` (#1433). `LauncherSwipeTests` against mock hub (`MOCK_MANAGED_LAUNCHER=1`) covers swipes and hiding from details.

## API

| API | Purpose |
| --- | --- |
| `GET /api/v1/launchers` | All entries plus the built-in `claude`; a last-seen snapshot answers at once while one background refresh runs (the first-ever list synchronizes, at most 10 seconds per computer); `sync` is each computer's latest known sync state |
| `GET /api/v1/launchers/{name}` | One entry; ~/.zshrc entries include `definitions` (redacted functions per computer) and `details` (parameters from running once) |
| `POST /api/v1/launchers/{name}/check?machine=` | Test availability |
| `POST /api/v1/launchers` | Add `{name, base_url, model, small_model?, subagent_model?, custom_model?, custom_model_name?, custom_model_description?, args?, api_key, api_key_reference?, auth?, preset?, display_name?, assign?, share?, scope_machines?, delivery?, own_service?, machine_keys?, machine_key_references?}` (scope fields: “Device scope and on-demand delivery”; a key that is a reference to a provider extension's secret, such as `op://…`: `docs/extensions/secret-providers.md` “Launcher keys”); `auth` is `api_key` (default) or `bearer`. With `preset`, missing values are filled from the preset, and `name` may be empty (see “Built-in presets” and “Adding from extensions” below); without `preset`, `name` may be empty too when the hub lists `launcher_auto_name`: the hub makes the ID from `display_name` or the host (“Display names” below); with `assign`, returns `{launcher, assigned, kept}` |
| `GET /api/v1/launchers/presets` | Provider presets: built-ins (`plugin: "builtin"`) plus extensions (`docs/extensions/extensions.md`, “Model providers”), sorted by request language. Old hubs return 404; the app treats this as none |
| `PUT /api/v1/launchers/{name}` | Edit (`api_key` / `api_key_reference` unchanged if omitted; either replaces the other); Persona-managed only. Fields under “Provider model and gateway parameters” below retain their old values if omitted; so do `display_name` (`""` clears it) and the scope fields (“Device scope and on-demand delivery”). The rest is the provider as `POST` takes it and is sent in full: `base_url` and `model` are required on every `PUT` (a body of `{"own_service":"use"}` alone is refused for its missing Base URL), so a client changes one setting by sending the provider as `GET` returned it with that field changed. The ID is the path's: `name` in the body is ignored |
| `POST /api/v1/launchers/test` | Test connection (without node, see below): `{name?, base_url?, api?, auth?, api_key?, headers?, model, preset?}` → `{ok, ms, model, error?, code?, auth?}` (`model_not_found` means the gateway is reachable but the selected model is missing or unavailable to this key, unlike a missing endpoint's `not_found`; `quota_exhausted` means HTTP 402 used up the key's budget / quota; `region_blocked` means Anthropic blocked the region and changing keys will not help, see `docs/protocol/protocol.md`). With `preset`, missing address, protocol and model use preset defaults. `auth` is `api_key`, `bearer` or `auto`, and only that header is sent; the answer's `auth` is the method `auto` found (“Authentication method” above) |
| `POST /api/v1/launchers/discover` | Fetch models: `{name?, base_url?, auth?, api_key?, headers?, agent?}` (`agent`: the agent a provider not saved yet runs, which picks the protocol its gateway's capability report is asked about) → `{models:[{id, name?, context_window?, max_output_tokens?, efforts?, all_efforts?, default_effort?, no_effort?, image_input?, tool_calls?, inherited_from?, price?}], error?, code?}` (the fields under "Connection tests and model discovery"); `auth` as for the test, though the answer never names the method found |
| `POST /api/v1/launchers/catalog` | What the built-in catalog knows of model IDs typed by hand (#4975), matched as discovery matches them: `{ids:[…]}` (at most 100, each up to 200 bytes) → `{models:[{id, name?, context_window?, efforts?, default_effort?, inherited_from?, price?}]}`, IDs it knows nothing of left out (an unlisted ID ending in `[1m]` comes back with the 1M window, as discovery gives it). Reads no gateway, key or stored provider (`assistant/models_catalog_routes.go`), so it never answers what only a gateway reports (`max_output_tokens`, `all_efforts`, `no_effort`, `image_input`, `tool_calls`) |
| `POST /api/v1/launchers/import/parse` | Read a pasted configuration into provider drafts (#5193, feature `launcher_import_parse`): `{text}` (at most 256 KiB) → `{format, drafts:[{label?, aliases?, provider, env_names?, skipped_env?, skipped_headers?, matches?, notes?}], notes:[{code, line?, name?}]}`. Saves nothing and reads no computer; `provider` is a `POST /api/v1/launchers` body carrying the pasted key, never another secret; `matches` are the stored providers on the same Base URL, each with whether its key is the pasted one (`assistant/models_import_parse.go`; rules in `docs/assistant/model-config.md`, “Starting a provider from a pasted configuration”). With `launcher_import_link` it also reads import links (`format: "link"`, “Import links” below) |
| `DELETE /api/v1/launchers/{name}` | Delete Persona-managed entries; hide ~/.zshrc entries |
| `POST /api/v1/launchers/{name}/refresh-key` | Ask a key's secret provider again (a rotated secret; [secret-providers.md](../extensions/secret-providers.md) "Launcher keys") |
| `POST /api/v1/launchers/{name}/restore` | Unhide |
| `GET /api/v1/launchers/{name}/quota?machine=&refresh=1` | Quota (see below): `{machine, checked_at, quota}`, or `{error, unsupported}` if unavailable. The manager caches for one minute; `refresh=1` rereads |
| `GET /api/v1/machines/{mid}/launchers` | Launchers this computer can start (for pickers): its ~/.zshrc entries excluding hidden ones, plus the Persona-managed entries in its scope (not those skipped for its own service, which come in `unavailable` with why, #8109); each includes `source`, and a Persona-managed one its `display_name` (feature `launcher_auto_name`) |

The assistant's model is selected only in “Model Settings › Main Chat” (“Settings › Assistant” no longer has a model entry, #1291); scheduled tasks' model entry opens unified “Model Settings.” Providers own connections and defaults; purposes own specific choices. See `docs/assistant/model-config.md`.

## Provider model and gateway parameters (#2455)

Persona-managed providers add these fields following cindy's model (`proto.ManagedLauncher`, all optional, old data still readable). See `docs/integrations/claude-integration.md` for how node turns them into env.

| Field | Meaning |
| --- | --- |
| `models` | Gateway models `[{id, name?, context_window?, max_output_tokens?, efforts?, all_efforts?, default_effort?, no_effort?, image_input?, tool_calls?, price?}]` (what discovery reads into them, and how saving keeps the levels: "Connection tests and model discovery" above); the session's model runs as `<id>[1m]` when its window is above 200K (its window also goes to the compact window and context limit); a tier, the small model or the sub-Agent model set to a model runs as `<id>[1m]` when that model's window is 1M or more (#5149). An ID already ending in `[1m]`, or one `models` gives no window, is passed as it is (`docs/integrations/claude-integration.md`, "Model variables and the 1M marker"). `max_output_tokens` becomes `CLAUDE_CODE_MAX_OUTPUT_TOKENS`: the least of the models the session runs (its model, which `personad launch` reads from the start's `--model`, the tiers, small and sub-Agent model, the extra `/model` entry), each at most an eighth of its window and none with an unknown window, capped at 128,000, set only when the session's model is not a Claude one, every other one has a limit or is a Claude one, and the result is over 32,000; `no_effort` keeps the default `effort` off a session on that model (`docs/integrations/claude-integration.md`, "What a gateway reports about a model"). A list that lacks one of Claude Code's refusal fallback models turns that fallback off (#7319, "No automatic refusal fallback" above) |
| `opus_model` / `sonnet_model` / `haiku_model` / `fable_model` | Models for each tier (`ANTHROPIC_DEFAULT_*_MODEL`), falling back to `model` when empty |
| `subagent_force` | `CLAUDE_CODE_SUBAGENT_MODEL_FORCE=1` |
| `headers` | Custom request headers, one `Name: value` per line. The web wizard edits them on **Connect** for add and edit, before its automatic or manual checks. Direct Messages, Responses and Chat Completions send them on test, discovery and inference; compatible launcher HTTP fallback sends them too. Claude Code uses `ANTHROPIC_CUSTOM_HEADERS`, Codex maps each field to a collision-free environment variable through `env_http_headers`, and pi registers the same map. omp supports them only on its Anthropic-compatible path with a non-Anthropic base URL; Antigravity has no arbitrary-header channel. Unsupported runtime/model combinations refuse the setting and its check rather than silently dropping it. Reserved protocol and credential fields (`Authorization`, `X-Api-Key`, `Anthropic-Version`, `Content-Type`, `Content-Length`, `Host`, connection and transfer fields) are rejected; a custom `User-Agent` remains supported |
| `proxy` / `no_proxy` | Gateway `HTTP(S)_PROXY`, set in both upper and lower case (Node's fetch reads the lower-case one first, and an empty one falls through to the other) / the hosts that go direct, in both spellings too: Claude Code's own check reads both, but the runtime under it (Bun) takes `no_proxy \|\| NO_PROXY`, so a lower-case list from the shell alone would still send the gateway through the proxy (#6238). A provider with no `proxy` follows the computer's proxy, and its hosts are added to the lists its claude would read without it, spelling by spelling: the user's `settings.json` `env` lists first (Claude Code applies them over the environment), else the shell's at launch or the node's for the main conversation, and a spelling left empty takes the other's. `launcher.env` carries the user's lists (`no_proxy_merge`) for `personad launch` to combine with the shell's, and the provider's settings file pins only the spellings the user's settings set. A provider with a `proxy` of its own brings its whole list, an empty one too: the computer's is for the computer's proxy, and its `*` or the gateway in it would turn the provider's off; one whose `env` sets `HTTP(S)_PROXY` or the list is not merged either. `*` sends everything direct, the agent's web pages and commands too; iOS's Proxy › No Proxy stores the gateway's host instead, so only the gateway goes direct, and a list that is the gateway's host alone follows a new Base URL. proxy must be an http(s) or socks5 address without a user or password |
| `env` | The provider's own environment, one `NAME=value` per line (at most 50): `export` before it is fine, a line starting with `#` is a comment, a value in quotes is what is inside them, and an unquoted one loses a trailing ` # …`. Set after everything else, so it wins over the fields above, the shell and the user's settings `env`; `NAME=` with no value sets the variable empty. A proxy variable (`HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, `NO_PROXY`) goes in both spellings unless both are given. To go around a proxy, put the gateway's host in `no_proxy` rather than emptying proxy variables: an empty `HTTPS_PROXY` still falls through to `HTTP_PROXY`. The main conversation ignores `CLAUDE_CODE_EFFORT_LEVEL` / `CLAUDE_CODE_AUTO_COMPACT_WINDOW` here (it decides them itself). Applied for Codex and Antigravity providers too (#5953) |
| `timeout_ms` / `compact_window` / `max_context` | `API_TIMEOUT_MS` / `CLAUDE_CODE_AUTO_COMPACT_WINDOW` / `CLAUDE_CODE_MAX_CONTEXT_TOKENS`; 0 means default |
| `effort` | Default thinking level when caller gives none. Passed to Claude Code as `--effort <level>` by `personad launch` only when the start's arguments carry no `--effort` and the session's model (the last `--model` of the start's arguments, else of the provider's own `args`, a tier alias such as `opus` read as that tier's model, else `ANTHROPIC_MODEL` as the provider's `env` leaves it) is not marked `no_effort` (a starting point rather than a pin; Claude Code still sends its own default level, and a later `/model` switch keeps the level); the environment variable `CLAUDE_CODE_EFFORT_LEVEL` is not set by default so the session's `/effort` and UI controls work. To pin the level across all sessions, set `CLAUDE_CODE_EFFORT_LEVEL` explicitly in `env` (#2307) |

A Codex provider (`agent` `codex`) reaches its gateway as a model provider of Persona's own: `personad launch` passes `-c model_provider="persona-launcher"` with `base_url` from `OPENAI_BASE_URL` and `env_key = "OPENAI_API_KEY"` (`codexProviderArgs` in `node/managed.go`). Codex 0.16x ignores those two variables for its built-in OpenAI provider and went to `api.openai.com` over a WebSocket, so every turn ended in "Reconnecting... 5/5". Arguments that choose a provider or profile themselves (`-c model_provider=…`, `-p`, `--oss`) are left alone. When the gateway serves Codex's own model catalog (at `<base>/models` with `client_version`, or `<base>/codex/models`, found by the answer's shape) and this computer's codex reads one by default (0.161 and later), the provider table also has its `model_catalog_url` (#8797): codex lists the gateway's models and runs each with the gateway's context window and thinking levels instead of its bundled OpenAI table. The node looks when `personad launch` asks for the provider, waits for that at most 2 seconds (past it the start goes on without a catalog while the node keeps looking), and keeps the answer (fresh 10 minutes, then given at once while it asks again in the background); without a catalog the arguments are what they were. A Codex provider's own `models` are not added to its picker, as they are to Claude Code's ("Adding from extensions"): the picker is its codex's own `model/list` (`personad launch <name> app-server`), so it shows the catalog codex was given. Details: `docs/integrations/codex.md`, "Gateway model catalog".

- **Headers and the environment are secrets, like keys**: The manager encrypts them with the same key into `headers_sealed` / `env_sealed`, decrypting only when sending to node or to the model gateway; GET returns `has_headers` / `has_env` plus `header_names` (names only, never values). Reopened editors show those names with the values hidden.
  PUT without `headers` (`env`) retains the value; an empty string clears it. Other new fields behave the same way: omission leaves them unchanged (old apps editing providers do not clear them). Errors and launcher diagnostics redact every header value. Direct inference retains headers on a same-origin redirect and drops them after any cross-origin hop; probes never follow redirects. Quota requests keep their existing exclusion and do not inherit provider headers.
- **Validate on write**: Valid Base URL; thinking level only low / medium / high / xhigh / max, with each model's default level included in its supported levels; no negative numbers (a model's `max_output_tokens` included); no empty or duplicate model IDs; each header line must be a valid HTTP `Name: value` pair with no reserved protocol or credential field; each environment line must be `NAME=value` with a shell variable's name. Errors never quote a value.
- **A Messages Base URL is stored without a trailing `/v1`, and pasted endpoint paths are stripped** (#4886, #10014): Claude Code and the Anthropic SDKs add `/v1/messages` to `ANTHROPIC_BASE_URL` themselves, so an address pasted as many gateways document it (`https://gw.example.com/v1` or `https://gw.example.com/v1/messages`) would run as `/v1/v1/messages` or `/v1/messages/v1/messages` and answer 404. Likewise, pasting a full Chat Completions or Responses endpoint (`https://opencode.ai/inference/go/openai/v1/chat/completions`, `https://api.openai.com/v1/responses`) into **Base URL** would double the path on probes and direct calls. Saving (POST / PUT, a preset filling in the address) and importing from ~/.zshrc normalize the path with `proto.MessagesBaseURL` and `proto.APIBaseURL` (`server/internal/proto/launcher.go`): Messages providers (`proto.ManagedLauncher.MessagesBase`: `api` empty or `messages`, any agent but Codex and Antigravity) strip a pasted `/v1/messages` suffix first and then drop any trailing `/v1` segments (`a run of /v1 goes whole`), keeping deployment prefixes (`https://gw.example.com/team/v1` → `https://gw.example.com/team`, `https://gw.example.com/messages/v1` and `https://gw.example.com/messages/v1/messages` → `https://gw.example.com/messages`, DeepSeek's `/anthropic/v1` → `/anthropic`); Chat Completions drops a trailing `/chat/completions` and Responses (plus Codex launchers) drops a pasted `/v1/responses` while preserving bare `/responses` deployment prefixes (`https://gw.example.com/responses` stays unchanged) and keeping `/v1` on OpenAI-compatible bases (`https://api.openai.com/v1`).
  A provider stored before this normalization is read the same way (`proto.ManagedLauncher.LaunchBaseURL`, `assistant/models_resolve.go`, `assistant/models_probe.go`, and `ai.apiEndpoint` in `server/internal/ai/ai.go`) without rewriting the record: nodes, connection tests, model discovery and direct API calls all reach `<base>/v1/<endpoint>` without 404s. Saving it again stores the normalized Base URL, which counts as a changed gateway and clears its failure records.
- On read, `source` stays `persona` / `zshrc`; provider origin (`zshrc:<alias>@<machine>`, `preset:<id>`) lives in `origin`.
- Names `test`, `discover`, `import`, `presets`, and `claude` are reserved for APIs and cannot be used.

**Connection tests and model discovery** request the gateway directly from the manager (`assistant/models_probe.go`) (following cindy's `buildProbeRequest` / `provider-model-fetch`), also available before saving a new provider:

- With `name`, use stored Base URL, protocol, key, headers, default model, and proxy; request values override stored values (`headers: ""` means no headers for this request). Direct-API providers reject a test or discovery request when effective stored proxy or no_proxy is nonempty; their custom headers are sent on both operations and on inference. The provider kind comes from the saved entry or preset; an anonymous Responses or Chat Completions request is also direct API. Anonymous Messages requests retain launcher testing semantics because they do not identify the provider kind. A launcher with headers must have a real runtime channel for them: Codex, Claude Code, pi, and omp on its documented Anthropic-compatible custom-gateway path are supported; Antigravity and omp's other model paths refuse before contacting the gateway.
- Connection testing sends a short `ping` to `POST <base>/v1/messages` for Messages or a streaming `POST <base>/v1/responses` for Responses (with a developer message, as the real calls send their system prompt), requiring the Responses completion event, or a streaming `POST <base>/v1/chat/completions` for Chat Completions, requiring nonempty response text and a terminal choice or `[DONE]`. Chat Completions tests send `max_tokens: 256` and retry once with `max_completion_tokens` only when the gateway explicitly rejects the former and requests the latter. Model discovery sends `GET <base>/v1/models`. A deployment path prefix is retained and `/v1` is not duplicated in either probes or direct API calls. A Messages probe reads the base as the node hands it to Claude Code (`proto.MessagesBaseURL`, above), so a passing test means Agents reach the same `<base>/v1/messages`.
  Chat Completions effort uses `reasoning_effort`; `off` / `none` / `disabled` maps to `none`, and levels accepted for Responses, including `max`, pass through for gateway/model validation rather than being clamped locally. Direct-purpose validation rejects `ultra` for Chat just as for Responses. If a 400 explicitly rejects the parameter itself, the probe removes it, logs the downgrade and retries once with that modified request. For an off request only, a 400 that rejects `none` as a value gets the same without-field retry; an explicit non-off level, an effort ceiling or any other 400 is returned unchanged. This modified request is not a same-request retry, so `X-Should-Retry: false` forbids neither the modified request nor learning from it. A learned rejection is remembered for one hour by Base URL, credential, route computer and model (at most 128 connection/model entries process-wide), separately for the whole field and for `none` alone, so later probes and real calls omit it immediately instead of paying another 400 and log line. A successful degraded probe returns an empty `effort`, so it does not mark the requested level verified. Real calls use the same mapping, retry rule and cache.
  Both `x-api-key` and `Authorization: Bearer` are sent (strip leading `Bearer ` from the key), plus `anthropic-version: 2023-06-01` and custom headers.
- DeepSeek's official HTTPS bases (root, `/v1`, `/anthropic`, or `/anthropic/v1` at `api.deepseek.com`, with an optional explicit port 443) discover models at the same origin's `/models`, as specified by its [model-list API](https://api-docs.deepseek.com/api/list-models/). The configured inference base is not changed: its [Messages API](https://api-docs.deepseek.com/api/create-message/) still uses `/anthropic/v1/messages`. Other authorities, nonstandard ports and unknown path prefixes retain the normal discovery rule; there is no endpoint scanning or cross-host fallback.
- Timeout: 30 seconds for the whole operation. Failures identify authentication, missing endpoints, rejected request formats, rate limiting, timeout and connectivity; key and header values are redacted. Both APIs return 200; inspect `ok` / `error` and `code` for the result. A 400 or 404 whose OpenAI- or Anthropic-shaped body identifies the selected model as missing or unavailable to this key returns `code: model_not_found` and says that the gateway is reachable but the model is not; this includes an Anthropic 404 `not_found_error` whose message starts `model:`. An ordinary missing endpoint remains `not_found`. HTTP 402 returns `quota_exhausted` with budget / quota advice. Other codes include `auth`, `wrong_protocol`, `region_blocked`, `timeout` and `unsupported_effort`; clients that do not recognize a code display the supplied `error` text.
- A model-list 404 keeps `code: not_found` but explains that discovery is separate from connection testing, with existing models and manually entered IDs still usable. It does not mark the provider invalid or permanently disable discovery.
- Model lists accept `{data:[…]}`, `{models:[…]}`, or a bare array (objects or strings). IDs use `id` / `slug` / `key` / `model_name` / `model_id`; names use `display_name` / `name`; windows use `max_model_len` / `context_length` / `context_window` / `max_context_length` / `max_input_tokens`. Pagination follows cursors or same-endpoint links, up to 100 pages, deduplicating IDs in order (a repeated entry fills only what the first left unsaid). Invalid, repeated or excessive pagination returns an error with the partial list.
- **What else a list reports** (#8778, `assistant/models_discover_meta.go`), read by the shape of the fields and never by which gateway answered, in the spellings cindy's discovery reads; a list that reports none of it answers exactly as before, with none of these fields:
  - **What says nothing** (#8796): a field that is `null`, and a list with an entry of a shape it can't read (an object naming none of `effort` / `value` / `id` / `name`, a number), say nothing, as if left out; blank strings in a list are skipped, and a list of only blanks says nothing. An empty list says nothing either (where a field has several spellings, the next one is read), except a level list (`reasoning_levels: []`: no level) and the capability report's `protocols: []` (no protocol), where empty is the gateway's answer.
  - **Thinking levels**, from the first of `reasoning_levels`, `supported_reasoning_levels`, `supported_efforts`, `reasoning_efforts`, `efforts` (strings, or objects naming one in `effort` / `value`), then a `reasoning` object's `allowed` / `supported_efforts` / `efforts` / `levels` unless its `status` is `unknown`. Only the shared words count: `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, `ultra` (`proto.EffortWords`, any case); a list naming only other words (a vendor's own) says nothing. The default comes from `default_reasoning_level` / `default_reasoning_effort` / `default_effort` or the `reasoning` object's `default` / `default_effort`; `none` as a default is no default.
  - **No thinking level** (`no_effort: true`, with no `efforts` and no default): a level list that is empty or names `none` alone, a `reasoning` object whose `status` is `unsupported`, or `reasoning` / `supports_reasoning` / `supports_reasoning_effort` false. Any top-level level list wins over these signals. Last, a guess: a whole `supported_parameters` list (one that names a parameter every chat model takes, such as `temperature`, `top_p` or `max_tokens`, as OpenRouter's does) naming none of `reasoning`, `reasoning_effort`, `reasoning_budget`, `include_reasoning`, `thinking` suggests no level, but not when one of those booleans is true. The guess is settled last (`guessNoEffort`), after the list's repeated entries and the capability report: it holds only if none of them said anything of the model's levels and the built-in catalog knows no levels for it (exact or inherited). A list of a few picked-out capabilities (`["tools","tool_choice"]`) guesses nothing. A wrong "no level" costs more than "unknown": Claude Code then gets no `--effort` and pi treats the model as not reasoning. An empty `efforts` alone still means unknown, so older clients keep offering every level.
  - **`efforts`** are the reported levels as Claude Code takes them (`proto.ProjectEfforts` onto `proto.EffortLevels`: `minimal` counts as `low`, `ultra` as `max`, `none` is dropped, as cindy's `clampEffortForClaude`), so every client and today's save validation accept them; `default_effort` is the default projected the same way, and only when it is one of them (or the levels are unknown). **`all_efforts`** keeps the gateway's own words, weakest first, only when one of them falls outside low…max. The projection can put in `efforts` a word the gateway did not list (`low` for its `minimal`): Claude Code can send nothing below low or above max, and a gateway that serves Claude Code's requests to another model fits that word to the model's own nearest level (magpie's `fitEffort`); the gateway's exact words stay in `all_efforts` for agents that can send them.
  - **`max_output_tokens`**: `max_output_tokens`, `max_completion_tokens`, `output_token_limit` / `outputTokenLimit` / `maxOutputTokens`, `limit.output` or `top_provider.max_completion_tokens`. `max_tokens` is not read: some lists use it for the whole context, and a list that pairs it with `max_input_tokens` (Anthropic's shape) may fill both with stand-ins for a model the gateway doesn't know, which the list can't tell apart. Windows keep their existing fields, `max_input_tokens` included.
  - **`image_input`**: `supports_image_input`, `supports_vision`, `vision`, `capabilities.vision` / `capabilities.image_input`, else whether `modalities.input` / `input_modalities` / `architecture.input_modalities` names `image` (an empty list says nothing). **`tool_calls`**: `tool_call`, `tool_calls`, `supports_tool_calls`, `supports_tools`, `supports_function_calling`, `function_calling`, `capabilities.tool_use` / `capabilities.function_calling`, else `true` when `supported_parameters` names `tools`, and `false` only when it is a whole parameter list without it. Both are omitted when unknown.
  - **No chat model**: an entry whose `kind` / `type` / `mode` / `model_type` / `task` is an image, video, audio, speech, transcription, embedding, rerank or moderation word, whose output modalities (`modalities.output`, `output_modalities`, `architecture.output_modalities`) leave out `text`, or whose `supported_endpoint_types` are all non-chat (new-api's `image-generation`, `image-edit`, `embeddings`, `jina-rerank`, `openai-video` and the like) is left out of the answer, so no client offers it as a chat model.
- **Capability report.** Once the list is read and holds a model, discovery asks `GET <base>/v1/model-capabilities?protocol=<p>` with the same credential header, where `p` is the protocol the provider's requests go on: `responses` for a Responses connection or a Codex launcher (the request's `agent` when it names one, else a stored provider's: the web wizard names the form's agent on every probe, `""` for a direct API connection, since it keeps a Codex launcher's api at messages), `chat_completions` for Chat Completions, else `messages`. The answer `{"data":[{"model_id","protocols":[…],"reasoning":{"status":"supported|unsupported|unknown","allowed":[…],"default"}}]}` (as some gateways serve it beside their list) fills only what the list left unsaid for each listed id, read the same way as a list entry (levels it brings come with its default unless the list's is one of them; the first entry for an id is its answer); ids the list lacks are ignored, and a model listed there with no protocol (none it can be reached on with the provider's protocol, an image model for one) is left out. It only adds: a 404, any other failure, a body it can't read or no answer within 5 seconds (and never past the 30-second budget) leaves the list as it was, and the request is skipped for an empty list or a list read outside the base (DeepSeek's `/models`, above).
- **Precedence**: what the gateway reports wins over the catalog: its levels (or that there are none: the catalog fills no levels or default then), its output limit and what it takes. The catalog's default fills a gap only when it is one of the reported levels. Windows keep their rule (an exact catalog entry's window wins; an inherited one only fills a gap).
- **Saving keeps levels in the words the provider's agent or API takes** (`settleSavedEfforts`): a Codex launcher keeps `minimal` and `ultra` in `all_efforts` (`proto.CodexEffortLevels`), a direct API connection keeps `none` (its purposes send "off" / "none" as no thinking), and every other launcher keeps nothing beyond `efforts` (Claude Code's low…max), so `all_efforts` is dropped there; a word none of them takes is dropped everywhere, `minimal` / `ultra` becoming `low` / `max` where the vocabulary lacks them. `all_efforts` stays only while it projects onto the saved `efforts`: levels changed by hand leave `efforts` the truth. `no_effort` stays only while no level is chosen: a level or a default level saved for the model undoes it. A model the request names none of the reported fields for (`max_output_tokens`, `all_efforts`, `no_effort`, `image_input`, `tool_calls`; an app from before them, such as the iOS and Android editors, resending `models`) keeps what is stored for it, under the same rules; a model that names any of them (in any case, as JSON decoding matches keys), even as `false` or `null`, is saved as sent, so what it leaves out is cleared. A change of agent or kind re-projects the stored models. The web wizard carries the fields through its form without showing them and names `no_effort` for every model; fetching the list again replaces a row's with what the gateway reports now, nothing included, so saving after a fetch clears a report the gateway no longer makes. From an app that doesn't know the fields, a stored report is cleared by choosing levels (for `no_effort`) or by removing the model and adding it again. pi reads them for its model metadata (`docs/integrations/pi.md`). A Claude Code launch reads `max_output_tokens` and `no_effort` (the `models` and `effort` rows of the table above); `image_input`, `tool_calls` and `all_efforts` are not read there, and launches still offer `efforts` as before.
- **The direct-API purpose editor** (web `DirectAPIEditor`) uses a Chat model's stored `providers[].models` capabilities, with discovery as the fallback when no entry is stored. For a legacy Chat provider, `providers[].model_aliases` maps tier aliases such as `sonnet` and `opus[1m]` to the stored model IDs; real requests and named probes resolve the same aliases before applying capabilities. Chat prefers nonempty `all_efforts`, else `efforts`, and keeps only levels a direct-purpose Save accepts; `none` / `off` appears as **Off**, and an efforts-only entry keeps **Off** as a separate control. When none of the exact reported levels can be saved, it keeps only the default and explains that those levels are unavailable; it does not substitute projected `efforts`, because Chat's runtime applies the exact `all_efforts` list. Responses and Messages continue to use projected `efforts`, including their existing unknown-metadata options. With no reported levels Chat offers the standard direct-purpose options, and a current unlisted value stays selectable. A direct API connection's saved vocabulary stays `none` with low…max, the levels a purpose can be saved with (`effortLevelsForKind`): `minimal` and `ultra`, which `ai.ApplyReasoningEffort` would pass to Responses, are refused for a purpose.
- The built-in catalog `server/internal/assistant/models_catalog.json` supplies known model metadata (Claude models and common gateway models such as GLM, Kimi, DeepSeek V4, Qwen3, MiniMax, Seed, and MiMo: id, name, context_window, efforts, default_effort and, where stated, price; Apache-2.0, see `THIRD_PARTY_NOTICES.md`). Lookups ignore case, a vendor prefix (anything before the last `/`), `[1m]`, and a trailing `-YYYYMMDD` date. An exact entry wins, including its name, and its context window takes precedence over a server-reported window; other server metadata is kept. `price` is the exact entry's global, standard USD reference price per one million tokens (`input`, `output`, optional `cache_read`, and `cache_write` / `cache_write_1h` for 5-minute and 1-hour cache writes where the registry states them; a missing `cache_write_1h` is `cache_write`'s price), only where the registry states one without an input-size tier. It is display metadata, not a quote from the configured gateway.
  An id the catalog does not list takes the context window, effort levels and default effort of the closest **earlier** generation of the same family, and only where the server did not report that field. The discovered model then carries `inherited_from` (that catalog id) when one of those values was used. The family is the leading name plus the same size or variant suffix: `qwen3.9-27b` can inherit from `qwen3.8-27b`, not from `qwen3.8-235b`, `qwen3.8-max` or `qwen3.9-plus`. A dot and a dash are the same separator, so `glm-5-3` is the catalog entry `glm-5.3`, and `qwen3.8.27b` / `qwen3.9.max` are the same suffixes as `qwen3.8-27b` / `qwen3.9-max`. A trailing `.0` (`glm-5.3.0`) is not that row: a server-reported window and name stay, and gaps still come from `glm-5.3` rather than from `glm-5.2`. `[1m]` and a trailing date are stripped in either order. Generation numbers compare numerically (`5.10` is newer than `5.9`); a later generation is never a source. A parameter size with no generation number (`7b`, `8x22b`) does not inherit. The name, reference price and routing are not inherited. An unlisted id ending in `[1m]` is a 1M window: the marker beats an inherited window, and a server-reported window beats the marker. `[1m]` on an exact catalog id does not replace that entry's window.
  To update the catalog, regenerate from the newer registry, taking only values explicitly stated there. The generation fallback reuses those stated values for a newer id; it does not add a row.
  The same lookup answers `POST /api/v1/launchers/catalog` for an id typed by hand, so the web wizard fills such a model the way discovery fills a listed one (#4975).

## Direct-API providers

Persona-managed connections add `kind` (`launcher` / `api`, omitted to retain legacy launcher behavior) and `api` (`messages` / `responses` / `chat`) fields.
For `kind: api`, the manager sends requests directly; these entries do not appear in the new-Agent launcher list. Small models, sub-Agent models, custom model options, and extra CLI arguments do not apply.
Entries with `api` empty or `messages` are also sent to node alongside launchers (#2455), allowing the main chat to start claude with their Base URL and key; `responses` and `chat` direct API entries are not sent. Direct-API entries support the same sealed custom headers on connection tests, model discovery and real calls; proxy and no_proxy remain rejected because the manager's real calls do not honor those per-provider settings. Saving rejects nonempty proxy values, including stored values retained by an update that omits them. Existing entries with these proxy settings are kept intact, but their direct calls and named connection tests/discovery report the unsupported settings; explicitly clear both fields to repair the entry. Launcher entries retain their transport settings subject to the runtime support matrix above.

Chat Completions cannot be configured as a launcher and uses Bearer authentication for direct model calls. It supports sealed per-provider custom headers on probes and inference, but not proxy, no_proxy, environment, timeout, context limits, tier models, or subagent settings; saving a Chat Completions provider with any of those unsupported fields set is rejected. A default effort and purpose effort overrides are supported. A requested level is omitted when the selected model entry has `no_effort`, or when its direct capability list (`all_efforts` when present, otherwise `efforts`) is nonempty and excludes that level; `off` counts as `none` for `all_efforts`, while an `efforts`-only entry keeps it as a separate control. Empty lists remain unknown rather than meaning "no efforts". Model-list metadata otherwise remains available for display. Existing Chat Completions providers cannot be changed to another protocol; add a separate provider instead. Voice providers use a separate real-time protocol.
Authentication keeps the existing encrypted-key storage, with no need to put API keys in purpose configuration. The original `/launchers` API and provider names remain compatible.

## Built-in presets

hub includes common vendors' public endpoints (`server/internal/assistant/presets_builtin.json`, embedded in the binary): Anthropic-compatible ones as Claude Code launchers, and direct Responses APIs for OpenAI and Grok. New users with “Only an API key” can select a vendor and paste the key to connect (onboarding design #287, sections 4.3 and 5.4).
`GET /api/v1/launchers/presets` lists built-ins first, then extensions; extension presets with the same ID override the built-in entry without changing its position.

- Each entry: `id` (`builtin/<vendor>`), `plugin: "builtin"`, `name` (plain English name), `display_name` and `description` (request language, entries in `assistant-models_presets`), `kind: launcher` with `api: messages`,
  or `kind: api` with `api: responses` (no `small_model`), `base_url`, `auth` (`api_key` / `bearer`, following the vendor's Claude Code instructions; `bearer` for a direct API), `model` (default), `small_model`, `models`, `signup_url` (signup / key page). Only built-ins fill `auth`, `signup_url`, and `display_name`.
- Order: Chinese first lists vendors directly accessible in China (DeepSeek, Kimi, Zhipu GLM, Alibaba Cloud Model Studio, MiniMax), then Anthropic, the international versions and Grok. Other languages list Anthropic, DeepSeek, the international versions and Grok first. Each entry's `region` (`cn` / `global` / `both`) determines the group, with file order within groups.
- Include only public addresses explicitly documented by vendors (pay-as-you-go endpoints, not coding subscriptions, relays, or company gateways). Addresses, authentication methods, and model names were checked against official documentation; no keys or real requests were used. Update this file when vendors change model names; `TestBuiltinPresetsAreComplete` checks complete fields for every entry.
  List few models, and prefer ids the vendor keeps current over dated releases (xAI's `<model>` follows the latest stable release, `<model>-<date>` never changes): the list only feeds the wizard's model choice for new providers, while saved providers keep their model.
- **OpenAI** uses `https://api.openai.com/v1`, Bearer authentication and Responses, with GPT-5.4 and GPT-5.4 Mini ([official API quickstart](https://developers.openai.com/api/docs/quickstart), [GPT-5.4](https://developers.openai.com/api/docs/models/gpt-5.4), [GPT-5.4 Mini](https://developers.openai.com/api/docs/models/gpt-5.4-mini)).
- **Direct-API presets** (OpenAI and Grok, #2204): xAI documents its Anthropic-compatible `/v1/messages` as deprecated, so Grok is `kind: api` on Responses. Such a provider serves only the manager's own calls (Processing, Sorting, answering Agents' questions), never the main chat or Agents: `assign` answers `kept: incompatible` for both.
  The web, iOS and Android wizards derive this from the preset (`presetRunsClaude` / `runsClaude`: `kind: api` with `api` other than empty or `messages`), hide the "main conversation and new Agents use it" switch, send no `assign`, do not wait for readiness and skip the Claude Code warning, and say on the key step and the last step what it can't run. iOS also leaves direct-API presets out of “Where to get a model”.
  Unconfigured API uses run on the first direct-API provider (`fallbackAPIProvider`), so adding one can move them; both steps say so before and after saving. The description in the list (`desc_grok`) says it can't run the main chat or Agents on every client.
  Alibaba Cloud Model Studio uses `dashscope.aliyuncs.com/apps/anthropic` (pay-as-you-go `sk-` keys; Coding Plan and Token Plan keys cannot be used at this address).
- Adding uses `POST /api/v1/launchers {preset, api_key, name: "", display_name: ""}`. Empty `name` is the slug of the preset ID's vendor portion (`builtin/glm` → `glm`; the rule in “Display names” below), with `-2`, `-3` for duplicates. If hub generated `name` and `display_name` is also empty, use the preset display name (“Zhipu GLM”); a name with `-2` gets the same number in its display name (“Zhipu GLM 2”), keeping entries distinguishable in model configuration.
  A manually supplied `name` without `display_name` (including older clients adding from extensions) does not get an inferred display name; it still displays `name`.
  Without `preset`, `display_name` may also be supplied (any language, at most 64 characters), and `name` may be empty when the hub lists `launcher_auto_name` (“Display names” below). The provider stores `display_name`, shown in lists and model-configuration provider names; `name` remains the startup ID.
- `assign: ["assistant", "agent.new"]`: After saving, try each listed purpose, switching to the new provider only while it remains default—no provider or model selected in model configuration, and the main-chat launcher still default `claude` (unchanged in Settings › Assistant or hub.json).
  Results are `assigned: [purposes]`, `kept: [{purpose, reason}]`; `reason` is `customized` (someone selected it), `incompatible` (it cannot be used for this purpose), or `failed` (configuration read/write failed). Only these two purposes are accepted; others return 400 with nothing saved.

- hub advertises `builtin_presets` in `features`; clients send empty `name` with a preset and `assign` only when present. Old hubs return 400 for empty `name` and do not recognize `assign`.
- `POST /api/v1/launchers/discover` also accepts `preset`: use the preset address if `base_url` is omitted; an explicit address wins.
- The new-Agent `/model` list adds preset models only for providers “added from this built-in preset” (matched by `preset`). Extension presets still also match by gateway address; built-ins do not, preventing a ~/.zshrc launcher pointing to `api.anthropic.com` from gaining an extra list of models.
- Compatibility cost: old apps' “Add from plugin” also lists built-in presets, with “Provided by extension ‘builtin’” in the description and the plain English name as title (old clients do not read `display_name`). Adding still works (they already send `name` + key + `preset`); wording is tracked in #3621.

## Display names (#5029)

A Persona-managed provider has two names. `name` is its **startup ID**: ASCII (`^[A-Za-z_][A-Za-z0-9_-]*$`; not `claude`, `presets`, `test`, `discover`, `import` or `health`), the name starts, rc files, Agent presets (`tiers`) and the model configuration (`launcher.<name>`) refer to. It never changes once the provider is added (`PUT` takes it from the path), since all of those would lose it. `display_name` is what people see, in any language (at most 64 characters, no control characters); empty shows the ID.

- With `launcher_auto_name` in `features`, `POST /api/v1/launchers` without `preset` takes an empty `name` and the hub makes the ID: the slug of `display_name`, else of the base URL's host, else `provider`. The slug rule is the presets' (`launcherSlug` in `assistant/models_presets.go`): lower case, every character other than `a-z`, `0-9` and `_` becomes `-`, a run of `-` is one, none at either end, at most 40 bytes, `p-` before a leading digit. A name any launcher has (rc files' included) or a reserved one gets `-2`, `-3`….
  “Team Relay” → `team-relay`, and a second one `team-relay-2`, still shown as “Team Relay”; “我的中转站” on `https://relay.example.com` → `relay-example-com`; “Test” → `test-2`; “1min relay” → `p-1min-relay`.
- With no display name either, the host becomes it, numbered as the ID is: `https://gw.example.com/v1` → `gw-example-com` shown as “gw.example.com”, the next one `gw-example-com-2` as “gw.example.com 2”; a host longer than a display name may be is cut so the name, number included, fits 64 characters (`numberedName`). A display name the request gives is stored trimmed and never numbered. A `name` the request gives keeps its checks: 400 when invalid or reserved, 409 when taken.
- Where it shows: the provider lists, the model configuration's provider names, the descriptions of a provider's own models in the `/model` list, and the pickers. `GET /api/v1/machines/{mid}/launchers` carries `display_name` on Persona-managed entries only (never on rc-file, built-in or PATH launchers); `tiers[].launcher` and `default.provider` stay IDs, which clients look up in `launchers`.
- `PUT /api/v1/launchers/{name}` changes it: left out or `null` keeps it, `""` clears it.
- Without `launcher_auto_name` (an older hub), clients ask for the ID as before.

## Import links (#5420)

A provider can travel as a link: a gateway's key page offers one to its signed-in user, or someone shares a provider
they already have. The payload is cindy's v1 provider import JSON (the format cindy documents for its import links), so one
payload works in every client. Persona reads three carriers:

- **Persona's web link**: `<web address>/settings?tab=models#import=<data>`, optionally with `&v=1` in the fragment.
  The data sits after `#`, so the browser never sends it to the hub and it is in no access log. At load the web takes
  the fragment out of the address bar and the Back list first thing, before the language catalog loads or error
  reports start (`takeImportLink` in `web/src/lib/import-link.ts`, called at the top of `main.tsx`; a link pasted into
  an open page's address bar is taken on `hashchange` in `App.tsx`). It keeps the link in memory only and opens
  Settings › Model providers › Add on the paste view in its link mode, which sends the link to
  `POST /api/v1/launchers/import/parse` as `{text}`. Signing in to Persona first keeps it; a sign-in layer in front of
  the hub that reloads the page gets the link put back in the address bar first, and one that sends the browser
  elsewhere to sign in drops it (open the link again). A browser's own history may still have recorded the address
  when it was opened; that is one reason a copied link carries no key unless the user puts one in.
- **Persona's app link** (#5428): `persona://provider/import?v=1&data=<data>` opens Settings › Model providers on iOS,
  iPadOS and Android. The app sends the whole link to `POST /api/v1/launchers/import/parse`, never decoding or validating
  the payload itself, and opens the existing provider wizard with the returned draft, or shows the refusal in plain words.
  iOS opens the prefilled connection form. Android uses the same review as **Paste a configuration**, in a link mode that
  hides the link text and offers **Paste something else**; **Continue** opens the prefilled form. The link stays in memory
  only while that wizard is open, is never shown or logged, and nothing is saved before Save. A hub too old to have the
  parse route's link support answers 404, and either app says it can't open import links yet (without depending on whether
  its first hello has arrived).
- **cindy's link**: `cindy://provider/import?v=1&data=<data>`, pasted into **Paste a configuration** (a browser does
  not open it). The legacy `xdt-maker://` scheme is not read. Persona's app link pasted there reads the same way, as does
  Persona's web link, so someone with their own hub can use a link made on another one.

`data` is the payload's UTF-8 JSON in base64url without padding. The manager (`assistant/models_import_link.go`)
checks a link as cindy does and refuses the whole link on the first problem, with one note and no draft:

| Rule | Note |
| --- | --- |
| An app scheme's: exactly `provider/import`, no fragment, only `v` and `data`, each once. The web's: only `import` (once) and `v` (at most once) in the fragment. A link with a blank inside (a messenger wrapped it) | `link_invalid` |
| `v` other than `1` (the web's: absent means 1) | `link_version`, named when it is a number |
| more than 32 768 bytes after `cindy://` or `persona://` or in the web fragment; data of more than 24 576 bytes decoded | `link_too_long` |
| data that is empty, not canonical base64url (padding, `+`, `/`, stray bits), not UTF-8 or not a JSON object | `link_invalid` |
| a field v1 does not define (a batch `{providers}` is a file's, not a link's) | `link_field`, with its path when that is a plain word |
| a value v1 refuses: `kind`, `name` (60 at most), `endpoints` (1 to 8), `protocol`, an http(s) `baseUrl` without a user, query or fragment and with a port of 65 535 at most, `modelsUrl` on the same origin, `requestPath`, `targets`, models (256 at most, distinct, thinking levels only with `reasoning`), headers (24 at most, only with an API key), keys (4 KiB at most, 1 KiB for a built-in slot, no line breaks or control characters), no sign-in only on loopback addresses, two endpoints tied for one agent | `link_invalid`, with the field's path |

A note names a field's path, a parameter or a version, never a value: an unknown field whose name is no plain word,
or starts like a service's key (`sk-`, `ghp_`…), goes unnamed. A link that passes is read as a pasted cindy
provider is (`docs/assistant/model-config.md`, “Starting a provider from a pasted configuration”), with
`format: "link"`: `preset`, `builtin` and OAuth links are valid but noted (`cindy_kind`, `cindy_auth`), an endpoint's
own `requestPath` is noted (`cindy_request_path`: Persona calls the usual path), and nothing is
saved until the user presses Save in the wizard. A link without any key does not note `no_key` on its draft: typing
one's own key is what such a link expects. A Messages endpoint's draft names no sign-in method, since v1 does not
say which header a gateway takes: the wizard's own check finds it (“Authentication method”). The shared cases are in
`spec/fixtures/provider-import-link/cases.json`.

**Copy import link.** The dialog of a Persona-managed Claude Code gateway, direct Responses API or direct Chat
Completions API in Settings › Model providers
has **⋯ › Copy import link** (web). The web builds the payload from the provider as `GET /api/v1/launchers` lists it
(`linkPayload` in `web/src/lib/provider-link.ts`): `kind: custom`, the display name (cut to 60), one endpoint on the
stored Base URL (`anthropic-messages` for a gateway, `openai-responses`, `openai-chat`) and the models
with the default first, with their names, context windows and thinking levels, and what discovery read from the gateway as
cindy names it (`maxOutputTokens`, `supportsImageInput`, `supportsToolCalls`, `reasoning: false` for a model that takes
no level; #8778, which the hub reads back into the same fields: cindy writes `reasoning` only when its source states
it, so `false` there is a statement, not a default) (only their IDs, then none, when the list
would pass 256 models, 24 KiB of JSON or 32 KiB of fragment; a model with an empty, overlong or repeated ID, or a
thinking level cindy doesn't know, is left out rather than have the link refused; IDs are trimmed). Headers,
environment, proxy, tiers and the device scope stay behind. A direct Messages API, Codex, Antigravity and omp offer no
link: no v1 endpoint reads back as them (a Messages endpoint comes back as a Claude Code gateway).

The link carries no key by default: whoever opens it types their own. The hub never returns a stored key to any client,
and links add no route that would. The dialog's **Put a key in the link** switch (off) takes a key the user pastes there,
under a warning that anyone who gets the link can use the key and that links pass through chat apps, browsers and their
history; the key lives only in the dialog and the copied text.

Hello feature `launcher_import_link`: the parse route reads links. A web app on an older hub says the hub can't open
import links yet instead of sending one. The phone apps send the opened link to the parse route even if the first hello
has not arrived; its 404 gives the same message.

## Adding from extensions

Model providers contributed by extensions (`docs/extensions/extensions.md`, “Model providers”) add “From Extension” under “Settings › Model Providers › Add.” When hub's `GET /api/v1/launchers/presets`
has presets, the add page starts with a “Enter manually / From Extension” switch (without presets, it remains manual-only as before). From Extension: choose a preset (name, description, connection method, address),
enter only the name and key, and choose the default model from the preset's `models` (no selection uses the preset default). `POST /api/v1/launchers` sends only `name`, `api_key`, and `preset`, adding `model` only if another model was selected.
The manager fills address, connection method, API, and small model from the preset (extension-owned presets are requested from hub through `app.call plugins.presets`). The preset's `key_hint` appears below the key, and `models_error` below the model. Once added, it can be edited like a manual entry. Extension provider declarations accept `messages` and `responses`; configure a `chat` direct API provider manually.

When choosing a model for a new Agent (`GET /api/v1/machines/{mid}/launchers/{name}/models`), launchers using extension gateways list not only Claude Code's own `/model` entries,
but all preset models (`presetModels` in `assistant/models_presets.go`). Persona-managed entries use their stored preset; entries without one (manually added before extension presets existed) and ~/.zshrc entries
match presets by gateway address (ignoring case and trailing `/`). Only `kind: launcher` presets and Claude Code launchers are eligible. Same-name entries already in `/model` are not duplicated;
added entries use the preset name as `description`. One launcher per gateway can therefore select all its models, without a launcher per model; the picker follows updates to the extension's `dynamic_models`.
A Persona-managed provider's own `models` (the field above) are listed the same way, after the `/model` entries and before the preset's,
with the provider's display name as `description`: Claude Code's `/model` shows only its tier aliases, so a gateway's other models could not be
picked otherwise. They also count for the send check (`launcherHasModel`), so a model picked from them is not dropped from a message.

- iOS: `LauncherPresetModel` (`apple/Persona/Views/Settings/LauncherPresetModel.swift`, unit tests `LauncherPresetModelTests`), `LauncherPresetViews.swift`;
  DEBUG `-tab settings -openModelLaunchers YES -openLauncherEditor preset` opens From Extension directly (`-openLauncherEditor YES` opens the add page).
- Web (#3693, onboarding #287 section 4.3, “Three paths,” B): the add dialog's first step directly lists every preset from `GET /api/v1/launchers/presets` (built-ins first, hub order; extensions marked “Extension”).
  “Custom compatible gateway” comes last; address and authentication method appear only there. The client no longer hardcodes “Official Anthropic,” using built-in preset `builtin/anthropic` instead. For a preset, paste only the key: after typing pauses for 0.7 seconds (at least 8 characters),
  it automatically tests once with `POST /api/v1/launchers/test {preset, api_key, auth}` using the preset's protocol. Failure shows hub's plain-language error and “Retest”; `signup_url` is “Where to get a key” (opens a new tab). Name (generated by hub if empty) and default model are under “More options.”
  The custom gateway's “Connect” step (#4881) checks itself the same way: once the URL is valid and a key is typed (at least 8 characters) and both stop changing for 0.7 seconds,
  it calls `POST /api/v1/launchers/discover`, then `POST /api/v1/launchers/test` once with the default model (the first found when none is set, else the model typed to test with);
  whatever the list said (a key may answer without being allowed to list models, so a list's 401 / 403 says nothing about chatting); with no model known, nothing is tested.
  A URL being typed counts only once focus moves from its field to something else on the page (the window losing focus does not count), the step is left, or a paste fills all of it
  (a paste into part of it, a drop, autofill or a typing suggestion counts as typing; a **Next** refused for another field does not count), so a key (a saved one included) never goes to a half-typed host; meanwhile the step says leaving the field starts the check. For a new provider this runs once per connection (URL, key, sign-in method, protocol, type, headers): naming it,
  changing the test model, or going to “Choose models” and back sends nothing. Editing a saved provider sends nothing until its URL, key or sign-in method differs from what is stored.
  A test or fetch pressed by hand counts as the check: a test pressed while the check fetches is not repeated, and a fetch by hand never tests. A test of the same model that is running or passed is not sent again when the list is fetched again.
  Only the URL and key gate “Test connection” and “Fetch models”; the name only gates “Next” and “Save”.
  **Sign-in method** (#5329, hub feature `launcher_auth_auto`): a new gateway starts on “Automatic”. The check sends `auth: "auto"`; when the connection test passes, the method it
  found is kept in the form (`auth_found`, cleared when the connection changes) and the check box says it in one line (“This gateway signs in with Bearer.”); later tests send that method alone.
  Discovery does not name it: a model list may not check the key. Saving stores the method found; with none found, a new gateway saves Bearer (the earlier default) and an edited one keeps its own.
  Editing opens on the stored method, named in the same box (“Signs in with x-api-key.”), and tests with it alone. The choice (Automatic / Bearer / x-api-key) sits under “More options”, reached from the line's “Change”.
  A hub without the feature gets the earlier wizard: the two-way control on the step, Bearer for a new gateway, and never `auto`.
  A failure shows the hub's words and “Retry” (it repeats what failed, never a test that passed); once models are found, “Next” is the step's one primary button (editing: “Save” becomes secondary there). The cost note and both buttons stay.
  `formProblems().reach` and `autoCheckable` in `web/src/lib/providers.ts`; tests in `ProviderWizard.requests.test.tsx`.
  Save includes `assign: ["assistant", "agent.new"]` (toggle on by default, “Use for the main chat and new Agents”); the final screen uses `assigned` / `kept` to explain which purposes switched and which stayed unchanged, with reasons.
  Gap: the design requires the toggle to default off “when usable Claude Code already exists,” pending `assistant_readiness` (the fourth PR). Until then it always defaults on; hub's assign-if-default protects only purposes explicitly customized by users.
  Without `builtin_presets` in hub's `features` (old hub), name is required above the key, with no `assign` or toggle. Importing a computer's aliases first sat in an “Advanced” group below the list; since #5024 that group is gone and an **Import** button sits beside **Add provider** in the providers header (icon only at phone width), its dialog explaining when there is no computer to import from.
  Logic: `web/src/lib/launcher-presets.ts` (same-name `.test.ts`); UI: `web/src/views/settings/ProviderWizard.tsx` (tests `ProviderWizard.test.tsx`).
  Below the providers (#5193, only when the hub lists `launcher_import_parse`): **Paste a configuration**, a text box read by itself 0.7 s after it stops changing and at once on a paste (`POST /api/v1/launchers/import/parse`), which lists what it found, what each draft brings and leaves out, and every part of the text it did not use; **Continue** prefills the gateway's steps (connection, models, tiers, the rest under “Advanced”), where the usual check runs. A draft on the Base URL of a stored provider offers, beside adding it as a new provider, **adding its models** to that provider (same key) or **replacing only its key** (another key); either opens that provider's edit wizard with the change filled in, at “Choose models” or “Connect”. Then **Found on your computers**, when there is any: rc-file claude aliases with a gateway address that are neither hidden nor already imported (no Persona provider whose origin is `zshrc:<alias>@<one of its computers>`), each opening the import dialog for that alias on its first online computer. Logic: `web/src/lib/provider-paste.ts`; UI: `web/src/views/settings/ProviderPaste.tsx`.
  An import link opened in the browser (#5420, “Import links” below) opens the wizard straight on this paste view in its link mode: no text box, a line saying it was opened from a link and that nothing is saved until Save, and **Paste something else**; the link is read at once and its drafts, matches and notes show as a paste's do. A provider's dialog has **⋯ › Copy import link** for Persona-managed Claude Code gateways and direct APIs (`web/src/views/settings/ProviderLinkCopy.tsx`).
  “Choose models” (#4975): a model ID added by hand is looked up with `POST /api/v1/launchers/catalog` and fills only what is still empty (name, window, thinking levels; an older hub without the route is ignored).
  A gateway that lists more models than the step checks by itself (`AUTO_CHECK`, 8; #6224): the Connect line of the models found offers **Choose models**, which goes there as Next does; the step has a filter over model ID and name; and every row offers **Set as default**, which on an unchecked row also checks it (there it shows on hover and keyboard focus, always on touch, like other row actions). Tests in `ProviderWizard.test.tsx` ("with many models"); the mock's `MOCK_DISCOVER_MODELS=<n>` makes "Fetch models" find that many.
  For a Claude Code provider, “Claude Code tiers” under the list says what Opus, Sonnet, Haiku / small model and Subagents run (an empty one runs the session's model, the default unless the session picks another, as `providerEnv` falls back), and **Adjust** opens the fields in place; Fable and forcing the subagent model stay under “Advanced”.
  Haiku / small model is one value, `small_model` (the Haiku tier follows it); a saved provider whose `haiku_model` differs shows the two apart for as long as the step is open (an emptied Haiku field then says it follows the small model). Subagents are never filled in: they do the real work, so they follow the session's model unless the user picks one.
  A new provider's Opus, Sonnet, Haiku / small model and Fable fill themselves in from the checked models (`suggestTiers` in `web/src/lib/provider-tiers.ts`): an ID naming a tier (opus / sonnet / haiku / fable) goes to it, the default model when it is one of them, else the newest generation;
  within the default model's family (its leading name, as the catalog's generations read it), flash / air / mini / nano / lite / turbo / small (or the smallest parameter size) are Haiku / small model, ultra / max / pro (or the largest size, or the plain model beside lighter ones) are Opus, and plus (or the plain model beside a heavier one) is Sonnet; another family's models are not used. A tier nothing fits stays empty, and so does one whose pick is the default model: empty, it runs the session's model, which is the default unless the session picks another; for the same reason the fields offer the default model only as the empty choice, not again by name. An empty tier runs the session's model with its 1M marker; a tier set to a model gets the marker when that model's window is 1M or more.
  A filled-in value is marked Auto and follows the checked models and the default until the user picks one (an empty “Same as default model” included); a picked one never changes again. When the Haiku tier or the small model runs on the Opus tier's model while a lighter checked model exists, one line says which of the two does, and **Apply** sets the small model to it (and the Haiku tier, when that one was on the Opus model; a Haiku tier set to another model stays).
  A saved provider's tiers never change by themselves: the same line offers its empty tiers what the checked models suggest, applied only with **Apply**. Logic and tests: `web/src/lib/provider-tiers.ts` (`.test.ts`), `ProviderWizard.tiers.test.tsx`.
- Android (#3048): Settings › AI › Model providers list (groups, search, details, availability tests, hide / restore / delete, import from ~/.zshrc). See `docs/android/android.md`, “Other settings parity items.”
- Android (#1331): “Add model provider” page (`NewProviderScreen.kt`, `NewProviderState.kt` in `feature/settings`), reached from “+” at the top right of Settings › Model configuration or “Add model provider…” in a purpose page's provider sheet (adding from a purpose page selects it after saving, but the purpose still needs Save). With `launcher_import_parse` (#5220), the same page also offers **Paste a configuration** and **Found on your computers** under the same rules as the web entries above. `ProviderPasteState.kt` / `ProviderPasteScreen.kt` review parsed drafts and all notes, then reuse the existing add / edit form: same-key matches add only new models, another-key matches replace only the key, and nothing is saved by parsing itself. Each found alias opens the existing import page for that alias alone on its first online computer (or its first when none is online), with `alias` on both preview and import. See `docs/android/android.md`, “New model provider.”
- Screenshots: the fake hub includes two sample presets (`example-gateway/team`, `example-gateway/api`); `POST /api/v1/launchers` fills them and adds them to the list in memory.

## Quota

“Quota” on the launcher page reads how much allowance its key has left at the gateway. **Which gateways to read and how is the quota extension's responsibility** (`docs/extensions/extensions.md`, “Quota”): the open-source repository contains no gateway-specific readers.
Without a quota extension, this section explains that querying is unavailable, without an error. Code: shared `server/internal/quota` (report shape, delegated requests), manager-side `assistant/models_quota.go` (routes, cache, gateway selection), hub-side `extmgr/extension_quota.go` (extension quota reading, `app.call plugins.quota`),
and node-side `node/launcherquota.go`.

A launcher whose provider reports a prepaid balance rather than an allowance window is separate from the Quota extension: an official DeepSeek launcher added in Persona has its `GET /user/balance` read by the manager with the same sealed key and shown as an amount and currency in the sidebar's usage panel (`docs/server/usage-windows.md`, “Prepaid balances (#11825)”). The key never leaves the manager, and a launcher found in a shell rc file keeps its key on that computer, so only launchers added in Persona report a balance.

- Extensions **cannot see keys**: They answer `quota.query` and pass requests through `quota.http` to the party holding the key, which replaces `${launcher_key}` in the request with the key.
  Requests may go only to the launcher's own gateway (same origin, including redirects); key occurrences in responses are replaced with the placeholder again.
- Who sends: For Persona-managed launchers, the manager sends with decrypted keys (hub delegates through `models.launcher_http`; quota returns `503 assistant_offline` when the manager is absent). For ~/.zshrc launchers, **the computer with that launcher** sends (node `launchers.http`: run the probe to obtain address and key, then request the gateway locally).
  The key never leaves that computer, and the address is reachable from there. Prefer the hub computer. Old nodes without `launchers.http` report “node is too old.”
- With multiple quota extensions: ask those declaring `hosts` matching the gateway hostname first, then those without `hosts`. Skip `unsupported`; use the first answer. If none recognizes it, querying is unavailable.
- Cache results for one minute (`refresh=1` rereads). The report's `gateway` is the extension-contributed ID, and `gateway_name` is its human-readable name.
- App: `LauncherQuotaSection`, above “Availability” (same on web, `web/src/views/settings/LauncherQuota.tsx`; Android places it on the launcher page reached from Machines, passing that computer as `machine=`, see `docs/android/android.md`, “Extension UI and marketplace”). Title: “Quota · <gateway name>,” using the report's `gateway_name` (fallback to `gateway` for old hubs). The app hardcodes no gateway names.
  Usage ≥ 70% is orange, ≥ 90% or exhausted is red; refresh at the top right. DEBUG `-openLauncherPage <name>` opens it directly.
- The key's own use (`usage`) is shown by each entry's label. An entry's optional `period` (`day`, `week`, `month`) says
  what the label covers, so a client can add entries up: the iPhone's Usage widget shows today's money as the sum of the
  `day` entries' `cost_usd` over the launchers that have a gateway, and no money when none says (`docs/apple/home-widgets.md`).
  Fake hub's `cc_main` and `cc_team` carry quota from two example gateways (`示例网关`, “Example gateway,” and `示例中转`, “Example relay”) for screenshots.


## Model request attribution

Gateway accounting can opt in to source headers with `PERSONA_MODEL_SOURCE_HOSTS`. Its default is empty: Persona
adds no source headers to any destination. Set it in the service environment of the hub/assistant manager and every
node that makes model requests, for example:

```sh
PERSONA_MODEL_SOURCE_HOSTS='gateway.example.com,*.relay.example.com'
```

Entries are comma-separated hostnames, without a scheme, port or path. Exact names match only that host;
`*.relay.example.com` matches subdomains at any depth, but not `relay.example.com` itself. Matching ignores hostname
case and the destination port. A bare `*` is unsupported. Use only your gateway hosts; official vendor addresses
must stay outside the allowlist. Restart the affected services and their model sessions after changing it so existing
processes and restored terminal commands do not retain an earlier environment or generated settings.

Persona checks the effective Base URL/provider address, never the launcher name. On an allowed destination it adds:

| Header | Value |
| --- | --- |
| `X-Persona-Role` | `main` for interactive main/side chats; `leader` for direction conversations; `routine` for scheduled runs and their conversations; `task_agent` for work Agents; `background` for sorting, knowledge helpers, supervision and model health probes |
| `X-Persona-Todo` | Linked to-do ID, when known at process start (an `agent_start` makes its to-do before the start, so its Agent names it) |
| `X-Persona-Routine` | Scheduled-task ID, when known |
| `X-Persona-Session` | Persona session ID, when known at process start/resume |

Persona's own gateway reads for extensions (quota and usage through the hub or a node) and provider tests and model
lists are `background`. Only role enums and validated IDs enter these fields. A routine delivered into an interactive conversation has the
routine's accounting role for that run; the next interactive message restores the conversation's role. Manager-owned
records supply the metadata on starts, sends and autonomous resumes. Source changes participate in process reuse,
and node state preserves attribution through the manager's database boundary.

Claude Code uses newline-separated `Name: Value` pairs in `ANTHROPIC_CUSTOM_HEADERS`. Persona merges its reserved
source fields into the effective existing headers in a generated, private per-start settings file, preserving other
headers and settings. The generated settings pin the verified gateway URL with the headers, including for terminal
restores; user launcher/configuration files are never edited. An unknown endpoint or a launcher whose dynamic
settings cannot be read is skipped. Administrator-enforced settings can supersede these generated settings.

Codex uses a final CLI helper after normal shell launcher expansion, or after a managed provider is resolved. It adds
per-invocation `-c` overrides for the selected custom provider's `env_http_headers`. Existing literal headers are carried
in environment bindings too, so credentials never enter the new arguments. Authentication, provider URL and unrelated
headers stay intact. These local configuration overrides keep attribution on that process rather than a shared daemon.
A terminal's shell has its engine's environment, not the node's, so the node hands its allowlist to that helper in the
start file (`codex-start`) or in its answer to `personad launch`; the shell need not export `PERSONA_MODEL_SOURCE_HOSTS`.
Some Codex traffic cannot carry the fields: the built-in `openai` provider ignores ordinary provider header overrides;
explicit remote/desktop app-servers and external runtimes own their own HTTP transport; project-level provider choices
are skipped because only Codex knows whether it trusts that project configuration. Unknown/unreadable provider
configuration, compact `-c...` / `-p...` options and legacy administrator provider overlays are also skipped.
Use a Persona-managed or ordinary custom Responses provider on a local CLI when attribution is required. A new Codex session's ID is allocated after startup, so its initial process may omit
`X-Persona-Session`; a resume with a known ID can include it.

Persona does not add source headers to Antigravity (`agy`) Agent requests. Managed launchers with a Gemini API key
select `modelProvider: gemini` by default. In agy 1.5.10, both the CLI and ACP ignored `AGY_LLM_GATEWAY_HEADERS`
when sending generation requests to `GOOGLE_GEMINI_BASE_URL`. To check attribution, inspect a
`streamGenerateContent` POST in the gateway log. A model catalog GET can carry headers that generation omits.

Antigravity's Enterprise Gateway can send operator-configured headers on generation requests. Its headers apply
to the whole process. Persona's ACP companion shares one process across ordinary work Agents, so setting a task ID
there would label other Agents' requests with the same ID. For a terminal Agent, an allowlist check would need
the effective gateway destination because administrator settings can override the URL independently of the headers.
See Google's [Enterprise Gateway configuration and precedence](https://docs.cloud.google.com/gemini/enterprise/docs/ai-developer-tools/deploy-enterprise-gateway#settings-precedence).

Direct Messages, Responses and Chat Completions helpers use `background`, including helpers sent through a node.
Cross-origin redirects strip all source fields, even when the next origin is also allowlisted. A gateway should also
strip these fields before forwarding a request to its vendor.

After deployment, inspect gateway ingress logs for one interactive main chat, one direction conversation, one scheduled
run, one work Agent from each supported launcher and one background helper. Check the role and available IDs, without
logging prompts or credentials. Confirm that direct official-vendor requests and an empty allowlist have no source
headers, and that vendor-bound gateway egress has none. This repository change neither deploys the configuration nor
changes gateway accounting/enforcement.
# One-session Claude Code model aliases

`POST /api/v1/sessions` and the `agent_start` tool accept a `model_aliases` object for a new Claude Code terminal session. In `task_plan`, put it on the individual task. For example:

```json
{"model_aliases":{"sonnet_model":"vendor/standard","haiku_model":"vendor/fast","subagent_model":"vendor/helper"}}
```

The other keys are `opus_model` and `fable_model`. For these starts and managed Claude gateway terminal starts,
omitted aliases keep non-Claude launcher values, then use the non-Claude default model (`deepseek-chat` if none
is configured). Claude Code's own refusal fallback is disabled. A concrete Claude ID can still be named explicitly
for this start. Overrides cannot set arbitrary environment variables and do not change the shared launcher or
assistant model. Both the manager and the target node must support this option; a node without
`session_start_model_aliases` is refused with `409 node_outdated` instead of starting on the launcher's models.
To launch a Muster flow directly, pass the same object as `muster run --model-aliases '<json>'` instead.
