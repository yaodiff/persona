# node on Windows (herdr and Orca engines)

Lets the phone see and drive Claude Code inside herdr or Orca on a Windows computer: that computer runs the resident node (`personad node --attach`) and the connector persona-link (active mode, see [link.md](link.md)),
while the hub lives elsewhere (WSL2 / Docker / another machine). Requirements and steps are in #1090; the overview of Windows support is #1179. Pitfalls we hit are in [lessons/windows.md](../lessons/windows.md).

## Status

Both engines work: **herdr** (native Windows build since 0.9.5, see "herdr" below) can create and resume sessions from the phone, same as macOS / Linux; **Orca** can only view and drive sessions that are already open.
The table below is for Orca; where the herdr engine differs is in the table in the "herdr" section.

| Capability | Windows (Orca) | Notes |
| --- | --- | --- |
| Build | ✅ | `GOOS=windows go build ./...`; process groups, signals, file locks and CPU time are in `internal/osproc` and each package's `_windows.go` |
| Desktop notifications and Needs-you overlay | Electron only | The independent Link scheduled task has no tray UI. The Electron desktop's NSIS installation, system notifications and overlay still require Windows acceptance; they are not claimed here |
| Resident node, persona-link active mode | ✅ | `persona-link install` installs a scheduled task that starts at logon, restarts on exit and shows no window ("Install and start at logon" below); there is no one-line install script or installer package (#1179) |
| List Orca sessions and state | ✅ | One snapshot per second, as on macOS |
| Read transcripts and session info | ✅ | `%USERPROFILE%\.claude\projects\<encoded directory>\<id>.jsonl`; the directory encoding is the same as Claude Code's (every non-alphanumeric becomes `-`, `C:\Users\…` → `C--Users-…`) |
| Send messages (multi-line as one), keys, interrupt | ✅ | Bracketed paste works under ConPTY |
| Answer dialogs (AskUserQuestion, permissions) | ✅ | Answered by reading the screen, same as Orca on macOS |
| Close a session | ✅ | |
| Create a session from the phone, resume a Claude session that isn't running | ✅ | Since #5548 Orca opens a terminal in the session's worktree and runs `claude --resume` there, as herdr does; other agents' sessions are refused with why (`orca_resume`) |
| Claude in the desktop app or in a terminal Persona does not drive | ✅ | Listed as running with its own state, transcript streamed; messages go through the session's peer inbox (a named pipe). Questions and permission prompts there stay on the computer. See "Claude outside Persona's terminals" below |
| Machine monitoring (CPU, memory, swap, network, disk, system info) | ✅ | Win32 calls, no processes spawned; the metrics follow the Windows column of [machines.md](../server/machines.md) ("Collection definitions") (#1943) |
| Port forwarding | ❌ | Depends on `ps` / `lsof` / `/proc`; silently unavailable on Windows (#1179) |
| Codex discovery (processes, homes, `--listen` endpoints, which rollouts a process holds open) | ✅ | Win32 calls, no processes spawned: Toolhelp32 and the PEB for `codex.exe`, each one's handle table for its open rollouts ("Codex" below). Compatible Codex forks are still not discovered |
| Codex desktop App chats: list, state, send text and images | ✅ | Through the App's bus, the named pipe `\\.\pipe\codex-ipc` ("Codex" below) |
| Codex CLI running in a plain terminal | ✅ view, state | Seen as running from the rollout it holds open (working / done as on macOS), so never resumed into a second writer; view only, as on macOS. On a `--listen` app-server it joins like anywhere else |
| Resume an exited Codex CLI thread from the phone | ❌ | The Orca engine does not resume Codex on any platform; it works with the herdr engine (herdr table below) |
| Processes the node starts itself: managed launchers, `codex` start | ✅ | `osproc.Exec`: Windows has no exec, so it starts a child in the same console and exits with its exit code ("herdr" below) |
| hub self-update | ❌ | Still `syscall.Exec`; the hub doesn't run natively on Windows (#1179) |
| Updating the resident node through the hub ([node-update.md](node-update.md)) | ✅ | **Redeploy** works as on Linux and macOS once the scheduled task's supervisor runs the node with `--supervised` (persona-link from this change on). With no exec on Windows, the node moves its running file aside, puts the new one in place and exits asking the supervisor to start it again at once ("Updates through the hub" below). A node under an older supervisor says to run `persona-link install` once; the hub needs a `personad-windows-<arch>` build to send ("Node packages" in node-update.md) |
| Passive mode (hub connects to this machine) | Not verified | Builds, and `install --listen` takes the same path; you have to open the port in Windows Firewall yourself (`install` prints the `New-NetFirewallRule` command) |

## Install and start at logon

Releases publish `persona-link-windows-amd64.exe.gz` and `persona-link-windows-arm64.exe.gz` on [GitHub](https://github.com/nwverse/persona/releases/latest). They are single executables compressed with gzip, not ZIP archives. The POSIX `install.sh` is for Linux and macOS, not Windows; use PowerShell on Windows.

The following downloads the matching architecture, verifies the archive against the release's `SHA256SUMS`, and expands it. It installs only the connector: a computer that should also run Agents needs a resident node, which can be built from source as shown below. Windows `personad` / `persona-node` release assets and their release-feed packages are outside the connector release.

```powershell
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$base = 'https://github.com/nwverse/persona/releases/latest/download'
$name = "persona-link-windows-$arch.exe"
$dir = Join-Path $env:USERPROFILE '.persona\downloads'
New-Item -ItemType Directory -Force $dir | Out-Null
curl.exe -fL "$base/$name.gz" -o "$dir\$name.gz"
curl.exe -fL "$base/SHA256SUMS" -o "$dir\SHA256SUMS"
$match = Select-String -LiteralPath "$dir\SHA256SUMS" -Pattern " $([regex]::Escape($name)).gz$"
$expected = $match.Line.Split()[0]
$actual = (Get-FileHash -LiteralPath "$dir\$name.gz" -Algorithm SHA256).Hash.ToLowerInvariant()
if (-not $expected -or $actual -ne $expected) { throw 'SHA-256 mismatch; do not run this download' }

$input = [IO.File]::OpenRead("$dir\$name.gz")
try {
  $gzip = [IO.Compression.GzipStream]::new($input, [IO.Compression.CompressionMode]::Decompress)
  try {
    $output = [IO.File]::Create("$dir\$name")
    try { $gzip.CopyTo($output) } finally { $output.Dispose() }
  } finally { $gzip.Dispose() }
} finally { $input.Dispose() }

& "$dir\$name" version
& "$dir\$name" install --no-node --pair '<the persona://link?… link from the hub>'
& "$env:USERPROFILE\.persona\bin\persona-link.exe" status
```

To include a locally built resident node instead, omit `--no-node` and pass `--personad .\personad.exe`. No administrator is needed. Active mode needs no SSH or VPN. Only the optional SSH tunnel uses the system `ssh`; install Windows' OpenSSH client if that feature is wanted and `ssh` is not already available. These command-line executables are not Authenticode-signed, so Windows may warn before the first run; the SHA-256 check verifies the download, not a publisher signature.

To build both programs from source instead:

```powershell
cd server
go build -o personad.exe .\cmd\personad
go build -o persona-link.exe .\cmd\persona-link
.\persona-link.exe install --personad .\personad.exe --pair '<the persona://link?… link from the hub>'   # skip --pair if already paired
```

No administrator needed. On Windows, `install` does, in order:

1. With `--pair`, pair first; `--listen` switches to passive mode (same as Linux, see [link.md](link.md), "Linux (headless)").
2. If the scheduled task is running, end it first (a running program file can't be replaced) and wait for persona-link to release its run lock. If another persona-link is running (say, a foreground `persona-link run`), it asks you to stop it. Stop the retired tray UI yourself before upgrading; it is no longer managed by this CLI.
3. Copy itself to `~/.persona/bin/persona-link.exe`; copy the personad given by `--personad` to `~/.persona/bin/personad.exe`. Unlike Linux, if one is given it is always replaced:
   running `install` again with a new personad is the way to upgrade by hand (the hub can update it too, "Updates through the hub" below). If none is given and there is no `personad.exe`, only persona-link is installed (same as `--no-node`).
4. Put another copy at `~/.persona/bin/persona-link-service.exe`: the same program, with only the subsystem in the PE header changed from console to GUI (`guicopy.go`).
5. Write `~/.persona/persona-link-task.xml` (UTF-16), register the scheduled task "Persona Link" with `schtasks /Create /XML`, start it with `schtasks /Run`, and wait for it to connect to the hub (same as Linux, up to 20 seconds).

The scheduled task and the separately installed Electron desktop are independent. No tray process, sign-in entry or `persona-link tray` command is installed. An obsolete tray from an older installation may be removed through Windows' own Startup settings; its historical registry and WebView2 details are in [windows-records.md](../archive/operations/windows-records.md).

The "Persona Link" scheduled task:

- **Current user, runs with the logon session** (`InteractiveToken`). A standard user can't register a "run whether logged on or not" task (S4U gets access denied on a real machine; the stored-password kind needs an administrator),
  and the node has to be in the user session to see Orca and its agents anyway. The cost: it isn't there when nobody is logged in to Windows, stops at logoff, and starts again at the next logon.
- **Triggers**: at logon, plus every 5 minutes starting from install. The multiple-instances policy is "ignore new": while the supervisor is running it does nothing; if it's gone it is brought back within 5 minutes.
  The task's own "restart on failure" isn't used: it only covers the task failing to start, not the program exiting non-zero (tested on a real machine). "Repeat" on a logon trigger only starts at the next logon, so a separate time trigger is added.
- No run-time limit (by default the task is ended after 72 hours), runs on battery, isn't stopped when idle ends, normal priority (the default 7 is "below normal", lowering both CPU and disk).
- **What runs is the supervisor** `persona-link-service.exe supervise` (`supervise_windows.go`): it starts persona-link (`run --log ~/.persona/link.log`) with `CREATE_NO_WINDOW`
  and the resident node (`personad.exe node --attach --supervised`, log `~/.persona/node-attach.log`, moved to `.1` at 10 MB), and restarts whichever exits after 5 and 10 seconds respectively (same as `RestartSec` on the two Linux units),
  or at once when it exits with the node's restart code (`nodeproto.RestartExitCode`; a program that keeps asking right after it starts waits the delay).
  What the supervisor itself does goes to `~/.persona/link-service.log` (persona-link's output when it crashes is there too). Only one supervisor runs per computer (file lock on `~/.persona/link-service.run`).
  It starts the node first and waits for it to recreate `node-attach.sock` (it only checks the file's modification time and doesn't connect: each connection is an attach and would kick off the hub's connection; waits up to 10 seconds) before starting persona-link.
  The other way round, the hub connects the link first and immediately opens a stream to a node that isn't listening yet, briefly reporting this machine offline, and `install` concludes "the hub can't reach this machine's node" (#1984).
- The supervisor puts itself in a Job that "kills every process inside when the last handle closes": when the supervisor goes away (task ended, another install / uninstall, logoff, crash),
  persona-link and the node go with it, so when the scheduled task brings it back it doesn't fight leftover old processes.

**Why a GUI-subsystem copy**: when a scheduled task starts a console program in the user session, Windows opens a console window for it (a Windows Terminal window if Windows Terminal is the default terminal),
which stays on the desktop. A GUI-subsystem program has no window, and the console programs it starts with `CREATE_NO_WINDOW` get a console but no window. It's a copy rather than a separate build: whether `persona-link.exe`
is a release or your own `go build`, the supervisor is the same program. Approaches not used: `conhost --headless` (undocumented), a VBScript wrapper (VBScript is being removed from Windows),
`powershell -WindowStyle Hidden` (still flashes a window).

| Command | On Windows |
| --- | --- |
| `persona-link status` | An extra line "Background service: scheduled task "Persona Link" · state · starts at logon"; the state is whatever schtasks prints in the system language (Running / 正在运行, the Chinese-locale "Running"…), shown but not parsed. There is no retired `tray` status field |
| `persona-link uninstall [--purge]` | Ends and deletes the scheduled task (the node stops with it), deletes `persona-link-task.xml` and `persona-link-service.exe`. `--purge` is as on Linux and also deletes the supervisor's logs and lock. It does not change the separately installed desktop app |
| `persona-link reconnect` | Sets the running connector's named reconnect event; also available through the fixed desktop setup action |
| `persona-link name [<new>\|--reset]` | As on Linux: shows or changes what the hub calls this computer ([link.md](link.md), "The computer's name") |
| `persona-link node install` / `node uninstall` | Not installed separately: the node is in the same scheduled task; use `install` (with node) / `install --no-node` |
| `persona-link node status [--json]` | Whether the scheduled task includes the node, and the task's state |

## Updates through the hub

The hub updates a Windows node the way it updates any other ([node-update.md](node-update.md)): the build goes over the node RPC, the node checks it, keeps a backup, starts the rollback watchdog and swaps the file. What differs is what Windows lets a running program do (`server/internal/nodeupdate/os_windows.go`):

- **Who restarts it**: the scheduled task's supervisor starts the node as `node --attach --supervised`, which is what tells the node that something starts it again when it exits. Without that (a supervisor from before this, or a node run by hand) the node says to run `persona-link install` once on the computer, and the App shows that.
- **Swapping its own file**: Windows will not replace the file of a running program, but lets it be renamed. The node moves its file aside (`.node-update-retired-…`), puts the new one in its place, and deletes the retired file once nothing runs from it (the next confirmation, startup or sweep).
- **No exec**: the node exits with `nodeproto.RestartExitCode` and the supervisor starts the installed file again at once, as a new process: the hub sees the node reconnect a second or two later. A rollback, or a node that finds the restored old file at startup, does the same.
- **The watchdog** runs detached from a copy of the old program (`personad.exe.node-update-backup-<id>.exe`: Windows runs only files that end in `.exe`) and says it is ready through the transaction marker, as the systemd one does. It stays in the supervisor's job: when the task ends, it ends too, and the next node's startup recovery starts another or rolls back.
- **Stopping the new program on a rollback**: the restarted node is a new process. A build that can take updates here records its pid at startup; one that cannot (a build from before this, which therefore never confirms) does not. The marker also names the supervisor, and after putting the old file back the watchdog stops the supervisor's children that run from the node's file and started before the restore, so the supervisor starts the old program again.
- **Backups in use**: the watchdog runs from the backup until it sees the update confirmed, so a backup that cannot be deleted yet is moved aside and deleted later.

The first update needs a supervisor that passes `--supervised`: install once with a persona-link and personad from this change on. The hub sends the `personad-windows-<arch>` it finds among its node builds (`~/.persona/bin/nodes`, "Node packages" in [node-update.md](node-update.md)); release feeds do not carry Windows builds yet.

## Reconnect without a tray

Windows has no `SIGUSR1` / `SIGUSR2`. persona-link waits on named events, `Local\PersonaLink.<hash of link.json's path>.recheck` and `.reconnect` (`internal/link/poke*.go`). `persona-link reconnect` sets the latter, and pairing sets `recheck` so a running connector reads the new credential immediately. The Electron setup window calls only these fixed CLI actions.

The connector watches network changes itself with `NotifyIpInterfaceChange` (`internal/netaddr`), as it does with netlink on Linux. The scheduled task, supervisor and resident node do not depend on Electron, WebView2 or the retired tray. The former implementation is preserved as historical evidence in [windows-records.md](../archive/operations/windows-records.md).

## hub in Docker Desktop on the same computer

During acceptance, the hub and node were on the same Windows machine: the hub ran in Docker Desktop (WSL 2), node and persona-link on the Windows side.

- With WSL mirrored networking (`networkingMode=mirrored` in `.wslconfig`), ports published by Docker are only reachable on Windows' `127.0.0.1`; LAN and Tailscale addresses can't connect
  (opening them requires changing the Hyper-V firewall, which needs an administrator).
- The local persona-link connects to `127.0.0.1`: if the hub's advertised candidate addresses include no loopback address, add it manually with `persona-link address add https://127.0.0.1:<hub TLS port>`.
- The phone connects over Tailscale: this machine forwards the tailnet port to localhost with `tailscale serve --bg --tcp <port> tcp://127.0.0.1:<port>` (plain TCP forwarding; TLS is still done by the hub itself,
  and the pinned certificate is still the hub's), and `public.advertise` in hub.json lists `https://<this machine's Tailscale address>:<port>`.
- To pair the phone with a new hub, use **Settings › hub › Add Hub** in the App, not the root pairing link: that would replace the phone's existing pairing ([lessons/android.md](../lessons/android.md)).

## Running it manually (for development and acceptance)

On a machine with the scheduled task installed, first run `schtasks /End /TN "Persona Link"` (or `persona-link uninstall`); otherwise the foreground and scheduled-task copies, two persona-links and two nodes, will fight.

```powershell
cd server
go build -o personad.exe .\cmd\personad
go build -o persona-link.exe .\cmd\persona-link
.\personad.exe node --attach            # resident node; keep it open in another window
.\persona-link.exe pair '<the persona://link?… link from the hub>'
.\persona-link.exe run                    # keeps the link up
```

On the hub side: this machine is `kind: link` in `machines` in hub.json, with `public.tls_listen` on; get the pairing link with `personad hub pair --machine <id>` (for a Docker deployment,
`docker compose exec hub personad hub pair --machine <id>`). Once connected, set this machine's engine to Orca in the App (**Settings › Machines › Engine**).

## herdr

herdr has a native Windows build since 0.9.5 (ConPTY panes, [official notes](https://herdr.dev/docs/windows-beta/)). The node drives it only through herdr's socket API, so the herdr engine on Windows behaves as everywhere else:

| Capability | Windows (herdr) | Notes |
| --- | --- | --- |
| List sessions and state, read transcripts | ✅ | Same as macOS / Linux |
| Create a session from the phone, resume a session that isn't running | ✅ | The node types the start command in a new tab, as elsewhere |
| Send messages (multi-line as one), keys, interrupt | ✅ | |
| Answer question / permission cards | ✅ | Via Claude Code hooks, no screen reading |
| Live terminal (`terminal session observe`) | ❌ | herdr has no direct terminal attach on Windows; falls back to reading the screen |
| Codex in a herdr tab: list, send, answer cards | ✅ (tests) | The terminal route as elsewhere ([codex.md](../integrations/codex.md)); not run against a real herdr on Windows |
| Resume an exited Codex thread from the phone | ✅ (tests) | `codex resume <id>` in a new tab through `personad codex-start`, which runs the `codex.exe` behind npm's `codex.cmd` ("Codex" below); not run against a real herdr on Windows |
| Claude in the desktop app or in a terminal Persona does not drive | ✅ | As in the Orca table above |

- **socket**: herdr's API on Windows is a named pipe, named `\\.\pipe\` plus the socket path (`\\.\pipe\C:\Users\<you>\AppData\Roaming\herdr\herdr.sock`);
  the config directory is `%APPDATA%\herdr` (or `herdr` under `$XDG_CONFIG_HOME` if that's set). `socket_windows.go` in `internal/herdr` dials by this rule, matching herdr's own
  `config_dir` / interprocess `GenericNamespaced`; `$HERDR_SOCKET_PATH` still takes precedence. The path itself isn't a file, so `SocketUp` checks whether the pipe exists.
- **Shell in the pane**: herdr opens PowerShell by default on Windows, but the start commands the node types are POSIX shell syntax (`'…'` quoting, `A=1 cmd`). Set the pane shell to Git Bash in herdr's
  `%APPDATA%\herdr\config.toml`:

  ```toml
  [terminal]
  default_shell = "C:/Program Files/Git/bin/bash.exe"
  ```

- **herdr has to stay resident in the user's desktop session**: a `herdr server` started from an ssh session exits with that session. Run `herdr.exe server` from a scheduled task that starts at logon (current user,
  `Interactive`, no time limit); it's the same kind as the `persona-link install` task.
- **`syscall.Exec`**: `personad launch` (managed launchers) and `codex-start` / `tab-start` replace themselves with claude / codex. Windows has no exec, so `osproc.Exec` starts a child in the same
  console (stdin/stdout inherited as-is, so the pane shows that program), catches Ctrl+C itself without exiting (the child in the same console still receives it; only `signal.Notify` works, `Ignore` doesn't block it), and exits with the child's exit code when it ends.
- **Claude Code's first run**: a freshly installed Claude Code first shows the theme-picking onboarding, so the node never sees the input box ("claude did not become ready"). Run
  `claude` manually once on that computer to finish onboarding, or set `"hasCompletedOnboarding": true` in `%USERPROFILE%\.claude.json`.
## Local sockets

The node's `node.sock`, `runtime.sock` and `node-attach.sock` are still AF_UNIX socket files on Windows (available since Windows 10 1803), under `%USERPROFILE%\.persona\`.
They weren't switched to named pipes: Go's `net` supports them directly, so the hooks, persona-link and `--stdio` forwarding code didn't change a line. Listening, connecting, half-close (`CloseWrite`) and replacing a leftover socket file were verified on a real Windows 11 machine.
Limits: the path can't exceed 108 bytes (a very deep `%USERPROFILE%` gives `bind: invalid argument`); permission bits like `0600` mean nothing on Windows, so it relies on the user directory's own ACL.

## Orca

- **Transport**: the Windows build of Orca writes `named-pipe` (`\\.\pipe\orca-<pid>-…`) and `websocket` in `orca-runtime.json` (`%APPDATA%\orca\`), with no `unix`.
  `orcart` picks by platform: on Windows `named-pipe` first, then `unix`; elsewhere only `unix`. Named pipes are dialed with the existing dependency `tailscale/go-winio`. The protocol, token and metadata re-read rules are unchanged.
  The fake runtime used in tests (`orcarttest`) also uses named pipes on Windows, so the `orcart` and `hostorca` tests on Windows exercise the real transport.
- **Identifying panes and sessions**: as on macOS, by `ORCA_PANE_KEY` in the claude / codex process environment (falling back to `ORCA_AGENT_PANE`), then reading `sessions/<pid>.json`.
  Windows has no `ps -E` or `/proc`: `procs_windows.go` lists processes with Toolhelp32 and reads the command line, current directory and environment block from the PEB only for candidates (`claude`, `codex`, `node`, `bun`);
  only the few environment variables needed are kept (Orca's hook token and the like are never kept).
- **In the foreground**: Windows has no terminal foreground process group and no "suspended". Instead, a process counts only if "no ancestor in its parent chain is an agent process in the same pane" (a claude started by claude isn't the one the terminal shows);
  pids get reused, so an ancestor must have started before the process itself. When a pane has several, claude comes first, then by start time (Windows pids don't increase, so they can't be sorted by pid).
- **Screen-reading card**: Orca reports the session as blocked as soon as the AskUserQuestion PreToolUse hook fires, but starting a hook process on Windows takes a few seconds, so the dialog appears late.
  The card the manager built at that moment couldn't read the dialog ("can't read this dialog yet"); after that the manager looks at the screen again every 500 ms (up to 30 seconds) and swaps in a card with options as soon as the dialog appears (`assistant.upgradeScreenCard` in `assistant/fleet_ix_screen.go`, on all platforms).

## Codex

What the node needs to know about Codex on macOS / Linux comes from `ps`, `lsof` and `/proc`; on Windows from Win32 calls, without starting a process ([codex.md](../integrations/codex.md) has the rest).

- **Processes** (`codex_procs_windows.go`): Toolhelp32 lists the processes; only `codex.exe` ones (the CLI's npm shim starts the native `codex.exe`, the Codex App bundles one as its app-server) have their command line and
  `CODEX_HOME` / `HERDR_PANE_ID` read from the PEB, as for Orca. A `codex.exe` of this logon session that cannot be read (an elevated one) makes the scan "could not tell", so a resume refuses rather than guess.
- **Which rollouts a process holds open**: its handle table (`NtQueryInformationProcess(ProcessHandleInformation)`); each File handle is duplicated for a moment, and only disk files are asked their path
  (`GetFinalPathNameByHandle`). A Codex App app-server has about 700 handles; one pass takes 3–10 ms. Asking each rollout who holds it instead (`FileProcessIdsUsingFileInformation`, the Restart Manager's `RmGetList`)
  took about 40 ms per file, seconds for a home with a few hundred rollouts. The scan runs every 30 seconds and again right before a resume.
- **The Codex App** (`codex_desktop_windows.go`): its bus is not `<CODEX_HOME>/ipc/ipc.sock` but one named pipe per user session, `\\.\pipe\codex-ipc` (the App's main process is `ChatGPT.exe`, package `OpenAI.Codex`).
  Frames and methods are the macOS ones. The pipe is looked for in the pipe namespace (`FindFirstFile`), never opened just to see it: `os.Stat` opens it, which takes one of the App's free pipe instances or connects.
  The pipe does not say which Codex home the App uses, so an App chat is one in a home with the App's `.codex-global-state.json`.
  A thread the App has not loaded is opened with `codex://threads/<id>` through `ShellExecute` (no console window), only once `HKEY_CLASSES_ROOT\codex` is a URL protocol with a handler: the package registers it as a packaged app's protocol, with no `shell\open\command`.
- **`personad codex-start`**: npm installs `codex` as `codex.cmd`, and a batch file has `cmd.exe` read its arguments again, the first message among them (quotes, `&`, `%`, line breaks). codex-start runs the `codex.exe` the shim would start
  (with the `CODEX_MANAGED_*` variables its `codex.js` sets); another batch file launcher is refused when there is a message.
- **app-server on `--listen unix://`**: Codex on Windows serves its control socket as an AF_UNIX file too. Windows has no mode bits, so the socket and its directory must be this user's, with an access list that lets nobody else in
  (SYSTEM and Administrators aside); a `unix://C:\…` endpoint is a path, not a URL with a host. Codex's background daemon (`codex app-server daemon start`) does not start from npm's launcher on Windows, so a plain `codex` there runs in its own process.
  The real-machine check of all this (2026-11-07) is in `docs/archive/operations/windows-records.md`.

## Claude outside Persona's terminals

The desktop app's Code page and a `claude` in an ordinary terminal (Windows Terminal, PowerShell) run where neither herdr nor Orca has a pane. The node finds them by their `sessions/<pid>.json` and lists them live; a message from the phone goes through the session's peer inbox, never by starting a second `claude --resume` on the same transcript. Design and the protocol: [claude-integration.md](../integrations/claude-integration.md), "Claude outside Persona's terminals".

- **The inbox is a named pipe**, `\\.\pipe\LOCAL\cc-msg-<32 hex>`, dialed with `tailscale/go-winio`. On Windows it requires an auth line first: the token is in the key file next to `sessions/<pid>.json` (`<pid>.<sha256 of the pipe path, lower-cased>.key`), readable only by the user, as Persona's node is. Without it the session lists read-only, saying why.
- **The desktop app's entrypoint** is `claude-desktop`, or in some builds `claude-desktop-3p`: both are the desktop app (also in the "open elsewhere" refusal's words).
- **A terminal Persona drives is told apart by the claude process's environment**: a pane variable (`HERDR_PANE_ID`, `ORCA_PANE_KEY`…) read from its PEB, naming a pane an engine in use lists with a claude in it, means the engine lists it, so it is not listed twice.
- **Pid reuse**: Claude Code writes `procStart` as the process's creation time (a FILETIME); the node compares it with the running process's (`procinfo.StartMark`), so a reused pid is not taken for the claude.
- **Held messages**: the desktop app often runs sessions in bypass permissions mode, which holds a message from another sender until the person at the computer approves it in the session; the phone says the message is waiting there. `"crossSessionInbound": "accept"` in that Claude Code's settings delivers without asking.

## shell

When the node runs shell scripts on Windows, such as launcher probing and `claude --version`, it uses Git for Windows' bash (`loginShellProgram`: `CLAUDE_CODE_GIT_BASH_PATH`,
otherwise found from `git`'s location and common install directories), the same as Claude Code itself on Windows. It doesn't use a bare `bash`: in the PATH the service gets, `System32\bash.exe` comes first, and that's WSL.
If Git Bash can't be found, these commands don't start (the machine info has no Claude version); they never end up running in WSL.

## Real-machine checks

Each part above was checked on a real Windows 10 or 11 machine between 2026-11-03 and 2026-11-07: Orca and Claude through the hub and an Android phone, the herdr engine, install and start at logon, the former tray, Claude outside Persona's terminals, and Codex. Those historical checks do not constitute acceptance of the new Electron desktop or the removal of that tray on Windows.
What ran, what was found on the way, and what each check could not cover are in `docs/archive/operations/windows-records.md`.
