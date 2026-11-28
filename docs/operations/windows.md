# node on Windows (terminal engines and desktop apps)

Lets the phone see and drive Claude Code inside herdr or Orca on a Windows computer: that computer runs the resident node (`personad node --attach`) and the connector persona-link (active mode, see [link.md](link.md)),
while the hub lives elsewhere (WSL2 / Docker / another machine). Requirements and steps are in #1090; the overview of Windows support is #1179. Pitfalls we hit are in [lessons/windows.md](../lessons/windows.md).

## Status

Both engines work: **herdr** (native Windows build since 0.9.5, see "herdr" below) can create and resume sessions from the phone, same as macOS / Linux; **Orca** can only view and drive sessions that are already open.
The table below is for Orca; where the herdr engine differs is in the table in the "herdr" section.

| Capability | Windows (Orca) | Notes |
| --- | --- | --- |
| Build | ✅ | `GOOS=windows go build ./...`; process groups, signals, file locks and CPU time are in `internal/osproc` and each package's `_windows.go` |
| Tray icon and panel | ✅ | `install` also starts the tray at sign-in: connection state, reconnect, restart or install the background service, pairing (active and passive), renaming the computer. No notifications. See "Tray" below |
| Resident node, persona-link active mode | ✅ | `persona-link install` installs a scheduled task that starts at logon, restarts on exit and shows no window ("Install and start at logon" below); there is no one-line install script or installer package (#1179) |
| List Orca sessions and state | ✅ | One snapshot per second, as on macOS |
| Read transcripts and session info | ✅ | `%USERPROFILE%\.claude\projects\<encoded directory>\<id>.jsonl`; the directory encoding is the same as Claude Code's (every non-alphanumeric becomes `-`, `C:\Users\…` → `C--Users-…`) |
| Send messages (multi-line as one), keys, interrupt | ✅ | Bracketed paste works under ConPTY |
| Answer dialogs (AskUserQuestion, permissions) | ✅ | Answered by reading the screen, same as Orca on macOS |
| Close a session | ✅ | |
| Create a session from the phone, resume a session that isn't running | ✅ | Since #5548 Orca opens a terminal in the session's worktree and runs `claude --resume` there, as herdr does, and since #17044 a Codex, TRAE CLI or pi session's resume command the same way (not yet checked on Windows); an omp session is refused with why (`orca_resume`). Orca's terminals run Windows PowerShell by default, which reads neither the `cd -- <dir> &&` nor the POSIX start the node types, so the node opens that terminal in Git Bash (`terminal.create`'s `shell`) when Orca advertises `terminal.create-shell-selection.v1` (Orca 1.4.256 does; #9810). An older Orca opens its default shell: set its Windows terminal shell to Git Bash |
| Claude in the desktop app or in a terminal Persona does not drive | ✅ | Listed as running with its own state, transcript streamed; owner input uses Persona's plugin when available, with the session's peer inbox (a named pipe) as the fallback. Stop and AskUserQuestion answers use the inbox; permission approvals and other dialogs stay on the computer. The newer Stop/answer behavior remains unverified on Windows. See "Claude outside Persona's terminals" below |
| Persona plugin for Claude Code (desktop app sessions, other terminals) | ✅ | The resident node installs and updates it at the user scope of `%USERPROFILE%\.claude` with Claude Code's own CLI (the one on `PATH`, or the desktop app's copy under `%LOCALAPPDATA%\Claude-3p\claude-code`); `persona-link plugin status\|install\|uninstall\|on\|off` ([link.md](link.md) "Claude Code plugin") |
| Machine monitoring (CPU, memory, swap, network, disk, system info) | ✅ | Win32 calls, no processes spawned; the metrics follow the Windows column of [machines.md](../server/machines.md) ("Collection definitions") (#1943) |
| Files: delete with Undo, Replace in move and copy | ✅ | Deleted files and folders go to the user's Recycle Bin through the shell (`IFileOperation`), so Explorer lists them and can restore them too; Undo from the phone or the web puts them back by their `trash_id`, after a restart as well. What the shell would delete for good instead (a network folder, a drive without a Recycle Bin, the Recycle Bin turned off or too small, a path over 259 characters) and a file open in another program are refused and stay where they are. `server/internal/node/trash_windows.go` (#9933) |
| Port forwarding | ✅ | Win32 calls, no processes spawned: the process tree from a Toolhelp snapshot, listeners from `GetExtendedTcpTable` (#2114, [ports.md](ports.md)). A server whose tree Git Bash broke (`cd x && npm run dev` from Claude Code's Bash tool) is found by the pane its environment names (#9815) |
| Codex discovery (processes, homes, `--listen` endpoints, which rollouts a process holds open) | ✅ | Win32 calls, no processes spawned: Toolhelp32 and the PEB for `codex.exe`, each one's handle table for its open rollouts ("Codex" below). Compatible Codex forks are still not discovered |
| Codex desktop App chats: list, state, send text and images | ✅ | Through the App's bus, the named pipe `\\.\pipe\codex-ipc` ("Codex" below) |
| Codex CLI running in a plain terminal | ✅ view, state | Seen as running from the rollout it holds open (working / done as on macOS), so never resumed into a second writer; view only, as on macOS. On a `--listen` app-server it joins like anywhere else |
| Resume an exited Codex CLI thread from the phone | ❌ | The Orca engine does not resume Codex on any platform; it works with the herdr engine (herdr table below) |
| Processes the node starts itself: managed launchers, `codex` start | ✅ | `osproc.Exec`: Windows has no exec, so it starts a child in the same console and exits with its exit code ("herdr" below) |
| hub self-update | ❌ | Still `syscall.Exec`; the hub doesn't run natively on Windows (#1179) |
| Updating the resident node through the hub ([node-update.md](node-update.md)) | ✅ | **Redeploy** works as on Linux and macOS once the scheduled task's supervisor runs the node with `--supervised` (persona-link from this change on). With no exec on Windows, the node moves its running file aside, puts the new one in place and exits asking the supervisor to start it again at once ("Updates through the hub" below). A node under an older supervisor says to run `persona-link install` once; the hub needs a `personad-windows-<arch>.exe` build of its version to send: in its `bin/nodes` (`deploy.sh` puts it there, a hub update installs it from releases after v0.9.1), or the release's ("Node packages" in node-update.md) |
| Passive mode (hub connects to this machine) | Not verified | Builds, and `install --listen` takes the same path; you have to open the port in Windows Firewall yourself (`install` prints the `New-NetFirewallRule` command) |

## Desktop projects and new sessions

The node also lists Claude Desktop and Codex App projects independently of terminal engines. The existing web UI runs in the desktop app or browser; the independently installed Persona Link supplies local discovery, history and message delivery. Empty saved projects can appear before any Agent runs there. Claude's existing saved environments supply their native names and identities; sessions without an environment reference fall back to their original project folders, keeping desktop-created worktrees together.

- **Claude discovery:** search `Claude`, `Claude-3p` and `Claude-Data` under the user's local and roaming AppData, including Microsoft Store redirection under `Packages/Claude_*/LocalCache`. Read session-index files and the dedicated saved-environment metadata mirror, never credential-bearing app configuration. The index supplies transcript IDs, archive state, titles, folders and environment references. Native environment IDs are scoped to profile and account; only local absolute roots are included, with WSL, SSH and cloud targets excluded. Reading these names does not provide native create/rename support. Existing transcripts remain in Claude Code's projects directory.
- **Codex projects and history:** merge the legacy `.codex-global-state.json` project catalog with native `projects` / `project_roots` in `state_<n>.sqlite`, preserving project identities and order during migration. Paginated chat content comes from `thread_history_<n>.sqlite`. SQLite reads require `sqlite3.exe`: search the inherited PATH first, then beside `personad.exe`, the current registered user/system PATH and `%LOCALAPPDATA%\Microsoft\WinGet\Links`. These checks do not change PATH, and SQLite runs without a console window.
- **Create or bind a project:** Workflow board's **Add workspace** binds an existing folder or discovered native project to a Persona card. **New project** can create a Persona project, reusing a folder or creating its final missing child under an existing parent. Native Codex App and Claude Desktop project creation is disabled: `workspace.create` with either desktop app returns `desktop_project_sync_unavailable` before creating a directory or native metadata. Create the project in the desktop app, then bind it in Persona. A Persona card's title does not rename the native project. See [workspace-board.md](../web/workspace-board.md).
- **New desktop session:** choose `claude-desktop` or `codex-app` in **New agent**; no terminal engine is needed. Explicit model and effort choices are applied through the owning desktop session; omitted choices keep its defaults. Claude imports the supported permission mode; Codex permissions remain controlled in the App. An existing folder or one new child folder is accepted; a blank folder creates a temporary session folder. Codex requires a first message. Claude imports a CLI-created transcript and receives owner input through Persona's plugin when available, with its peer inbox as the fallback; Codex creates thread metadata and sends the first turn through the owning desktop window. The registered `claude://` and `codex://` handlers open the apps through `ShellExecute`.

A Codex card selects its exact project, even when another project shares its folder. Projects from a different Codex profile, or legacy JSON projects without a native mapping, cannot be started from that selection; open or update the legacy project in Codex first. Claude's existing named environments can be read and bound, but starting with their `desktop_project_id` is refused with `claude_desktop_project_unsupported` before transcript creation, import or delivery because the import cannot guarantee that environment assignment. Clear that selection to start a directory-based Claude desktop session. If session creation succeeds but setup or delivery fails, retry continues the returned session. An empty Claude session retries import only. These recovery paths do not create a replacement conversation.

Windows support extends the upstream desktop launchers from PR #5528 (Claude) and PR #5755 (Codex), both merged on November 13, 2026. This Windows extension has local automated and mock-protocol coverage plus read-only inspection of installed app schemas. Native sidebar project creation and name synchronization are deferred; the project-creation API refuses those requests before side effects. Existing native project discovery, Persona project creation and directory-based desktop sessions remain available. Real Windows message delivery and new desktop sessions remain unverified. The older real-machine checks below do not validate these additions.

## Install and start at logon

Releases publish `persona-link-windows-amd64.exe.gz` and `persona-link-windows-arm64.exe.gz` on [GitHub](https://github.com/nwverse/persona/releases/latest), together with `personad-windows-<arch>.exe.gz` and `persona-node-windows-<arch>.exe.gz` for the resident node. They are single executables compressed with gzip, not ZIP archives. The POSIX `install.sh` is for Linux and macOS, not Windows; use PowerShell on Windows.

The following downloads the matching architecture, verifies the archive against the release's `SHA256SUMS`, and expands it. For a computer that runs Agents, repeat the download and decompression with `personad-windows-$arch.exe.gz` (or `persona-node-windows-$arch.exe.gz` for the device's own program), then pass the resulting `personad.exe` to the install command below. `personad` is the full resident-node program, not a native Windows hub.

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
& "$dir\$name" version -v
& "$dir\$name" install --no-node --pair '<the persona://link?… link from the hub>'
& "$env:USERPROFILE\.persona\bin\persona-link.exe" status
```

To include a locally built resident node instead, omit `--no-node` and pass `--personad .\personad.exe`. No administrator is needed. Active mode needs no SSH or VPN. Only the optional SSH tunnel uses the system `ssh`; install Windows' OpenSSH client if that feature is wanted and `ssh` is not already available. These command-line executables are not Authenticode-signed, so Windows may warn before the first run; the SHA-256 check verifies the download, not a publisher signature.

To build both programs from source instead:

```powershell
cd server
go generate ./internal/runtimepkg   # the runtime packages and the Claude Code plugin the node carries
go build -o personad.exe .\cmd\personad
go build -o persona-link.exe .\cmd\persona-link
.\persona-link.exe install --personad .\personad.exe --pair '<the persona://link?… link from the hub>'   # skip --pair if already paired
```

No administrator needed. On Windows, `install` does, in order:

1. With `--pair`, pair first; `--listen` switches to passive mode (same as Linux, see [link.md](link.md), "Linux (headless)").
2. If the scheduled task is running, end it first (a running program file can't be replaced) and wait for persona-link to release its run lock. If another persona-link is running (say, a foreground `persona-link run`), it asks you to stop it. A running tray is asked to quit, for the same reason.
3. Copy itself to `~/.persona/bin/persona-link.exe`; copy the personad given by `--personad` to `~/.persona/bin/personad.exe`. Unlike Linux, if one is given it is always replaced:
   running `install` again with a new personad is the way to upgrade by hand (the hub can update it too, "Updates through the hub" below). If none is given and there is no `personad.exe`, only persona-link is installed (same as `--no-node`).
4. Put another copy at `~/.persona/bin/persona-link-service.exe`: the same program, with only the subsystem in the PE header changed from console to GUI (`guicopy.go`).
5. Write `~/.persona/persona-link-task.xml` (UTF-16), register the scheduled task "Persona Link" with `schtasks /Create /XML`, start it with `schtasks /Run`, and wait for it to connect to the hub (same as Linux, up to 20 seconds).
6. Install the tray for this user and start it: the sign-in entry, the handlers for `persona://` links and the previous brand's ones and the running tray ("Tray" below). `--no-tray` skips it and removes the sign-in entry.

Persona Link is complete on its own: the scheduled task and the tray need no other Persona program on the computer.

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
| `persona-link status` | An extra line "Background service: scheduled task "Persona Link" · state · starts at logon"; the state is whatever schtasks prints in the system language (Running / 正在运行, the Chinese-locale "Running"…), shown but not parsed. Then a line for the tray: running or not, starts at sign-in or not (`tray` in `--json`) |
| `persona-link uninstall [--purge]` | Ends and deletes the scheduled task (the node stops with it), deletes `persona-link-task.xml` and `persona-link-service.exe`. quits the tray and removes its sign-in entry, its link handlers and WebView2's cache. `--purge` is as on Linux and also deletes the supervisor's logs and lock |
| `persona-link reconnect` | Sets the running connector's named reconnect event, as the tray's **Reconnect now** does |
| `persona-link tray [--open <link>]` | Runs the tray (what the sign-in entry runs). A second one shows the running tray's panel, or hands it the link |
| `persona-link name [<new>\|--reset]` | As on Linux: shows or changes what the hub calls this computer ([link.md](link.md), "The computer's name") |
| `persona-link node install` / `node uninstall` | Not installed separately: the node is in the same scheduled task; use `install` (with node) / `install --no-node` |
| `persona-link node status [--json]` | Whether the scheduled task includes the node, and the task's state |
| `persona-link plugin status\|install\|uninstall\|on\|off [--json]` | The Persona plugin for Claude Code, through `personad.exe claude-plugin`; on by default ([link.md](link.md) "Claude Code plugin") |

## Updates through the hub

The hub updates a Windows node the way it updates any other ([node-update.md](node-update.md)): the build goes over the node RPC, the node checks it, keeps a backup, starts the rollback watchdog and swaps the file. What differs is what Windows lets a running program do (`server/internal/nodeupdate/os_windows.go`):

- **Who restarts it**: the scheduled task's supervisor starts the node as `node --attach --supervised`, which is what tells the node that something starts it again when it exits. Without that (a supervisor from before this, or a node run by hand) the node says to run `persona-link install` once on the computer, and the App shows that.
- **Swapping its own file**: Windows will not replace the file of a running program, but lets it be renamed. The node moves its file aside (`.node-update-retired-…`), puts the new one in its place, and deletes the retired file once nothing runs from it (the next confirmation, startup or sweep).
- **No exec**: the node exits with `nodeproto.RestartExitCode` and the supervisor starts the installed file again at once, as a new process: the hub sees the node reconnect a second or two later. A rollback, or a node that finds the restored old file at startup, does the same.
- **The watchdog** runs detached from a copy of the old program (`personad.exe.node-update-backup-<id>.exe`: Windows runs only files that end in `.exe`) and says it is ready through the transaction marker, as the systemd one does. It stays in the supervisor's job: when the task ends, it ends too, and the next node's startup recovery starts another or rolls back.
- **Stopping the new program on a rollback**: the restarted node is a new process. A build that can take updates here records its pid at startup; one that cannot (a build from before this, which therefore never confirms) does not. The marker also names the supervisor, and after putting the old file back the watchdog stops the supervisor's children that run from the node's file and started before the restore, so the supervisor starts the old program again.
- **Backups in use**: the watchdog runs from the backup until it sees the update confirmed, so a backup that cannot be deleted yet is moved aside and deleted later.

The first update needs a supervisor that passes `--supervised`: install once with a persona-link and personad from this change on. The hub sends the `personad-windows-<arch>.exe` it finds among its node builds (`~/.persona/bin/nodes`, "Node packages" in [node-update.md](node-update.md)), or the same program's `.exe.gz` from the immutable versioned release manifest. A Windows node's downloaded filename keeps `.exe`, including in the manager's cache.

## Tray

The tray icon is the Windows counterpart of the Mac menu bar App ([link.md](link.md), "Tray and menu bar App"): it shows the link's state and does what the App's menu does, item for item.
Persona Link only pairs the computer and keeps its connection: the tray posts no notifications, shows no "Needs you" list and does not open or drive sessions.
It is part of persona-link (`internal/linktray`, `persona-link tray`), not a separate program, so it is always the same version as the connector it manages. Pure Go, no cgo: Win32 for the icon, the menu and the windows, WebView2 (`wailsapp/go-webview2`) for the panel and the pairing window.

- **What runs**: `install` writes "Persona Link" under `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` → `"%USERPROFILE%\.persona\bin\persona-link-service.exe" tray` (the GUI-subsystem copy, so no console window) and starts it right away.
  The tray is not in the scheduled task: the supervisor's Job would end it with the task, and `install` starts it outside any Job (`CREATE_BREAKAWAY_FROM_JOB`) so it outlives the terminal that ran `install`.
  The panel's switch **Show in tray at sign-in** (or Settings › Apps › Startup) only changes the sign-in entry; the background service runs either way.
- **One per data directory**: a named mutex; a second `tray` hands its link (or "show the panel") to the running one over `WM_COPYDATA` and exits. `install` asks the running tray to quit before replacing the program and starts the new one afterwards.
- **State is read, never asked**: `link.json`, `link.status.json` and whether the pid in it is a live persona-link (`OpenProcess` and the image name). The data directory is watched (`FindFirstChangeNotification`), so changes show at once;
  otherwise it reads them every second while the panel or the pairing window is open and every 3 seconds when not. The scheduled task's state (schtasks, about 50 ms) is read every 30 seconds and after each action.
- **Reconnect now and network changes**: Windows has no `SIGUSR1` / `SIGUSR2`. persona-link waits on two named events instead, `Local\PersonaLink.<hash of link.json's path>.recheck` and `.reconnect` (`internal/link/poke*.go`), which the tray and `persona-link reconnect` set;
  pairing sets `recheck` so a running persona-link reads the new credential at once. persona-link watches network changes itself with `NotifyIpInterfaceChange` (`internal/netaddr`), as it does with netlink on Linux.
- **Icon**: the Persona helper, the same art as the app icon (`server/internal/linktray/art`, made by `scripts/assets/make_icon.py link`), drawn at the notification area's size and DPI. It is faded while the link is not up (connecting, offline, not paired), and a colored dot in its corner marks the states that need you (waiting for your OK on the phone, background service not running, offline), in the light or dark taskbar's shade (`icons.go`; `LINKTRAY_ICON_SHEET=<dir> go test ./internal/linktray/ -run Icon` writes them out to look at).
  Left click opens the panel; right click is a native menu with the same actions, which works without WebView2.
- **Panel and pairing window**: HTML pages (`internal/linktray/web`; every size and color comes from `tokens.css`) loaded from memory, with no server and nothing fetched. The panel is a flyout over acrylic next to the icon, on the taskbar's side;
  the pairing window (active and passive, as in the Mac App) is a normal window over Mica. Both follow the system's light or dark mode, accent color, transparency setting and the monitor's DPI (per-monitor v2).
  The backdrops need Windows 11 22H2 (build 22621); earlier builds get a solid background. Slow actions (install, restart, pair) run off the UI thread, and their result shows in the panel in the user's words.
- **Rename this computer…**: in the panel (and the right-click menu) while connected to a hub that renames computers and once the computer is confirmed. It opens a name field in the panel; Save asks the running
  persona-link to have the hub rename it, as `persona-link name` does ([link.md](link.md), "The computer's name"), and the panel says the hub's answer. The status line shows the name the hub gives the computer.
- **Links**: `persona://` links, and the previous brand's ones that older hubs print, open the pairing window with the link filled in (`HKCU\Software\Classes\<scheme>`, running `tray --open "%1"`).
  A scheme that another program already handles is left alone (a desktop App's phone link must keep working), and uninstall removes only the handlers that are ours.
- **Language**: Windows' display language, en / zh-Hans / ja (`internal/i18n/locales/*/link-tray.json`). The failures it recognizes (hub unreachable, link refused, no background service) are explained in that language, with the raw error after the explanation.
- **WebView2**: Windows 11 always has the runtime. On Windows 10 without it, clicking the icon says so in a message box and shows the native menu. WebView2 keeps the tray's profile (a cache, nothing of the user's) in `%LOCALAPPDATA%\Persona\LinkTray`, and `uninstall` deletes it.
  `PERSONA_TRAY_DEVTOOLS=1` turns on the developer tools (F12); a port number (`PERSONA_TRAY_DEVTOOLS=9333`) also opens remote debugging on that port, for Playwright's `connect_over_cdp`.
- **Pages in a browser**: `LINKTRAY_PAGES=<dir> go test ./internal/linktray/ -run TestWritePages` writes every sample state's panel and the pairing window, in each language and theme, as HTML files.

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
go generate ./internal/runtimepkg   # the runtime packages and the Claude Code plugin the node carries
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
- **Shell in the pane**: herdr opens PowerShell by default on Windows, and the start commands the node types are POSIX shell syntax (`'…'` quoting, `A=1 cmd`). There are two ways to run Agents:
  - **Git Bash as the pane shell**, the setup this page has always described. Set it in herdr's `%APPDATA%\herdr\config.toml`; the node then types its command directly:

    ```toml
    [terminal]
    default_shell = "C:/Program Files/Git/bin/bash.exe"
    ```

  - **herdr's default PowerShell** (Windows PowerShell 5.1 or PowerShell 7), through a handoff to Git Bash (#8107); it needs the full Git for Windows. The node writes the Agent's POSIX command, with its `PERSONA_HOME` / `unset` prefix, to the same self-deleting 0600 start file as on macOS / Linux, and types one short PowerShell line that uses `&` to run Git Bash with `-ic` and source that file (`-ic` is how the node already reads launchers, so aliases, functions and `PATH` from `~/.bashrc` work). Only the Git Bash path and the file's path cross PowerShell, single-quoted with every single-quote character PowerShell knows doubled. When Git Bash cannot be found through `CLAUDE_CODE_GIT_BASH_PATH`, `git` or the standard install directories, the start fails with that reason before anything is typed; it never falls back to WSL's `bash`.
    **The handoff has not been confirmed on a real Windows machine yet**: its quoting and the choice of line are covered by tests on other systems and the Windows build compiles, nothing more. If a start fails in a PowerShell pane, set Git Bash as above and report what the pane showed in #8107.

  Which of the two a start uses depends on the shell that will read the typed line. A host that says its new pane runs PowerShell or pwsh gets the handoff; a host that names any other shell is taken at its word. A new pane of classic herdr, which does not say, is asked once for its foreground program; when that is no known shell, `[terminal] default_shell` decides (found the way herdr finds its config: `HERDR_CONFIG_PATH`, `XDG_CONFIG_HOME`, `%APPDATA%\herdr`), and without either it is PowerShell. Git Bash or another POSIX shell gets the command typed directly, with no start file. `cmd.exe` is refused before anything is written or typed, with a message asking for PowerShell or Git Bash.
  A pane a start adopts (Run Agent Here, a stopped session resumed in the terminal it left, a launcher or engine switch) is read from the programs in front of it, innermost last. When the innermost one is PowerShell or pwsh (`powershell.exe`, `pwsh.exe`, any case), the start takes the same handoff (#9854); Git Bash, including Git Bash started from PowerShell, gets the command it got before. This has not been confirmed on a real Windows machine either. The check that the pane is at a shell prompt, which comes first, is unchanged: it knows `pwsh` but not `powershell` or a name ending in `.exe`, so a pane whose front program herdr reports that way is refused as not at a shell prompt, with nothing typed (which names herdr reports on Windows has not been checked).
  Launcher sync's `"shell": "bash"` and `"rc_file": "~/.bashrc"` describe the Git Bash that reads launcher definitions, not the shell in herdr's pane.
  **Not covered by the handoff**: a pane of Orca, Tern or herdr-core that names no shell (Orca opens Git Bash itself when it can, #9810); and classic herdr's own restore after it restarts, whose `resume_argv` is still a POSIX `eval` command that PowerShell cannot run. After a herdr restart, use the app's ordinary Resume, which opens a new pane.
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

**Runtimes on Node.js use a named pipe.** Node.js on Windows treats every `net.connect` path as a named pipe and cannot
reach an AF_UNIX socket (`connect EACCES`), so the built-in Antigravity, opencode and Paseo companions never connected to
`runtime.sock`: agy stayed the plain CLI launcher, which can start Agents but not the main chat. The node therefore also
listens on `\\.\pipe\persona-runtime-<random>` and passes that path to the companions it starts (`--socket` and
`PERSONA_RUNTIME_SOCKET`; `server/internal/node/runtimepipe_windows.go`). The name is new on every start and handed only
to processes the node starts, after the node owns the pipe, so nobody can create it first. Its DACL allows only the
node's account, remote clients are refused, and its handles are not inheritable. As on the socket, the node does not know
the connecting process. If the pipe cannot be created, the node logs `runtime pipe:` and passes `runtime.sock` as before.
A runtime started by hand on Windows still defaults to `runtime.sock` and cannot connect.

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
- **Stop and question answers**: the inbox's priority `now` interrupts the running turn or waiting tool call so Stop and AskUserQuestion answers can be handled immediately (#5961). This upstream route was measured on macOS; Windows end-to-end behavior remains unverified. Permission approvals, plan approvals and other dialogs still require the computer. See [Claude integration](../integrations/claude-integration.md#stop-and-answers-from-the-phone-5961).

## shell

When the node runs shell scripts on Windows, such as launcher probing and `claude --version`, it uses Git for Windows' bash (`loginShellProgram`: `CLAUDE_CODE_GIT_BASH_PATH`,
otherwise found from `git`'s location and common install directories), the same as Claude Code itself on Windows. It doesn't use a bare `bash`: in the PATH the service gets, `System32\bash.exe` comes first, and that's WSL.
If Git Bash can't be found, these commands don't start (the machine info has no Claude version); they never end up running in WSL.

## Real-machine checks

The earlier platform support was checked on a real Windows 10 or 11 machine between 2026-11-03 and 2026-11-07: Orca and Claude through the hub and an Android phone, the herdr engine, install and start at logon, the tray, Claude outside Persona's terminals, and Codex. The tray's return with the Persona helper icon and without notifications (2026-11-25) was checked on a real Windows 11 machine against a live hub: the panel, its state lines and the sign-in switch. The newer desktop project and session changes have the narrower validation scope described above.
What ran, what was found on the way, and what each check could not cover are in `docs/archive/operations/windows-records.md`.
