# Windows pitfalls

Read this before changing the Windows node (#1090), or developing or testing `personad` on a Windows computer. See [windows.md](../operations/windows.md) for the design and current status.

## Release packages and native verification

- **Keep `.exe` through the entire node-package path**: a Windows release is `personad-windows-amd64.exe.gz` or `persona-node-windows-amd64.exe.gz`, not its POSIX spelling. Manifest parsing removes both suffixes to get `windows-amd64`; manager selection accepts only that exact Windows name; download and cache removal strips only `.gz`. A connector's Link report does not verify personad or persona-node. A native report binds its program, version, build, feed, compressed bytes and decompressed PE bytes; node programs must also match the `latest.json` carried in that same receipt.
- **A Windows build missing from the release stays behind on the hub**: releases up to v0.9.1 list no Windows `personad` in `latest.json`, so a hub's own update never replaced `bin/nodes/personad-windows-<arch>.exe`, and Redeploy sent the old build as the hub's version until the node's self-check refused it (#12958). When a Windows Redeploy fails its self-check, look at what `bin/nodes` holds for Windows and whether the release's manifest lists it.
- **Cross-compilation is not native execution**: Go can compile Windows programs on a Mac, and their PE architecture and build stamp can be checked there, but `windows_release.py` must run the real `version`, `version -v` and node self-check on the matching Windows architecture before a receipt is publishable. SHA-256 alone does not establish what the program reports. The CLI's native verification is distinct from the Electron installer's required Authenticode signature.

## Development environment

- **Check out the repository with LF endings**: Git for Windows defaults to `core.autocrlf=true`, so every checked-out `.go` file has CRLF endings. `gofmt -l` reports many files, and scripts and golden files can also fail to match.
  After cloning, run `git config core.autocrlf false && git config core.eol lf` in this repository, then `git rm -q --cached -r . && git reset -q --hard` to check it out again (commit or stash your changes first).
- **Retained third-party bytes must bypass text conversion**: `-whitespace` only controls whitespace checks; it does not keep `core.autocrlf=true` from changing checkout or archive bytes. Use `-text` for checksum-pinned notices and inventories. Preserve their original LF, CRLF or mixed endings instead of normalizing them or updating their pins to match a converted copy. Test staging, checkout and archive behavior with autocrlf enabled, including ordinary neighboring files that should still convert (#9342).
- **Unix socket paths must not exceed 108 bytes**: Go supports AF_UNIX on Windows, but a long path makes `bind` return `invalid argument` (not “unsupported”).
  Paths such as `~/.persona/node-attach.sock` are fine; do not put test sockets deep inside temporary directories (Claude Code's scratchpad exceeded the limit).
- **`/tmp` is not the temporary directory**: On Windows, `os.MkdirTemp("/tmp", …)` means `\tmp` on the current drive, which usually does not exist. Tests that need short paths should use platform-specific files (`listen_*.go` in `orcarttest`).
- **Windows Python has no `fcntl`**: a repository script that locks with `fcntl.flock` fails with `ModuleNotFoundError` under Git Bash. One that must run on a Windows dev machine locks with `msvcrt.locking` there (`LK_NBLCK` on the first byte, trying again while it raises `PermissionError`), as `scripts/hooks/merge-pr.sh` and `scripts/operations/pr_watch_merge.py` do; two programs sharing a lock file must lock the same byte (seek to 0 first: `open("a+")` starts at the end). Its `subprocess.run(["bash", …])` also searches System32 before PATH, where `bash.exe` is WSL's: take `shutil.which("bash")`.

## Processes and shells

- **`bash` may be WSL**: In the system PATH inherited by the service, `C:\Windows\System32\bash.exe` precedes Git. It runs commands inside WSL (another “Linux” machine, with its own claude and `~/.zshrc`).
  On Windows, node must always run shell scripts with Git for Windows bash (`loginShellProgram`, like Claude Code itself, honoring `CLAUDE_CODE_GIT_BASH_PATH`). If it cannot be found, fail to start the command; do not fall back to bare `bash`.
  Use `interactiveShell()` wherever a launcher is started. The assistant, Codex assistant, and launcher-model listing each once hardcoded “bash if zsh is missing.” On Windows, the assistant's claude entered WSL
  and immediately exited with “open terminal failed: not a terminal”; the main chat did not respond, and there was nothing in the logs (#1994). Do not choose a shell independently in new launch sites.
- **A native path in a `bash -ic` script loses its quoting at the Git Bash boundary**: `shellQuote` closes and reopens its quotes around each backslash (`'C:'\\'Users'…`), which a POSIX shell reads back correctly, but Git Bash started as a native Windows process with the script as its argument eats an escape layer and ends with an unmatched quote ("unexpected EOF while looking for matching `''"). The assistant's launch (profile directory, a managed launcher's `personad`) exited about 200 ms after starting (#5487).
  Every script the node hands its shell as an argument with native paths in it (`-ic`, `loginShell`: the assistant's launch, Codex's app-server, the model list, the launcher and sign-in checks) quotes them with `scriptPath` and its launcher with `launcherForScript`: forward slashes on Windows (`C:/Users/…`, which Git Bash and Windows programs both take), and an apostrophe as `'"'"'` (a double quote survives the boundary, as `"$@"` does). Commands typed into a terminal (herdr panes) cross no such boundary and keep `launcherCommand`. Leave `shellQuote` and other arguments alone. `filepath.ToSlash` converts nothing when tests run on macOS, so replace `\` explicitly.
  The assistant's claude then has `CLAUDE_CONFIG_DIR=C:/Users/…` while other starts have `C:\Users\…`: a directory read back from a running process is cleaned to backslashes (`scannedConfigDir`) before it is compared, or one profile is listed twice.
- **The arguments after a Git Bash script lose their quoting too**: Windows hands a program one command line, and `exec.Cmd` writes it for the MSVC runtime (`syscall.EscapeArg`: no quotes around an argument without spaces, `"` as `\"`). Git Bash splits it the MSYS way, where `\"` outside quotes is a backslash that opens a quote, so arguments with double quotes in them run together. Codex's `-c` TOML values arrived as one argument and the main chat started without the persona MCP server (18 arguments sent, 2 received; #6418).
  A command the node starts with Git Bash as the program calls `msysCmdLine` after setting `SysProcAttr`: it quotes every argument whole and sets `SysProcAttr.CmdLine`. Other programs keep Go's quoting.
- **Read other processes' environment variables from the PEB**: There is no `/proc` or `ps -E`. `procs_windows.go` lists processes with Toolhelp32, then uses `NtQueryInformationProcess` + `ReadProcessMemory` on candidates
  to read `RTL_USER_PROCESS_PARAMETERS` (command line, current directory, environment block). Pointers in the PEB are addresses in the other process: read bytes and extract fields using x/sys offsets.
  Do not read directly into a struct such as `windows.PEB` that contains Go pointer fields (the GC would treat the other process's addresses as its own pointers).
- **Environment-variable values can contain spaces**: Whitespace-splitting regexes such as `envVarRe` are for `ps -E`; they truncate `C:\Users\A B\.claude`. On Windows, fetch the complete value by name (`pickEnv`); names are case-insensitive.
- **There is no “foreground process group” or “suspended” state**: On Windows, the Orca host considers claude / codex foreground when its parent-process chain contains no agent process for the same pane. See [windows.md](../operations/windows.md) (“Orca”).
- **PIDs are reused quickly and do not increase monotonically**: Sorting by PID to put wrappers first does not work on Windows. Sort by start time; when walking the parent chain, require ancestors to have started before the current process.
- **Git Bash breaks the process tree**: MSYS2 emulates `fork`, and for `cd x && cmd` the forked bash that runs `cmd` as a new Windows process exits, so `cmd`'s parent pid names a process that is gone. A walk down from the agent (port discovery) or up to it misses everything below that point, and whether it happens depends on the command line: a lone `python -m http.server` keeps its chain, `cd dir && npx …` loses it (#9815).
  Claude Code's Bash tool is Git Bash on Windows, so this is the usual case, not an edge. A process's engine pane variables (`ORCA_PANE_KEY`, `HERDR_PANE_ID`…) survive it: MSYS passes the environment through its fork and exec. Use them where the tree comes up empty.
- **A companion on Node.js guessed Unix names**: the Antigravity companion checked `~/.persona/bin/personad` with `existsSync` (which adds no `.exe`), concluded personad was missing and quietly gave the main chat no Persona tools; and its media guard hook was `/bin/sh -c …`, which agy runs through `cmd.exe` on Windows, so every `read_file` failed with "The system cannot find the path specified" (`integrations/antigravity/lib/compat.js`, `lib/media-guard.js`). Both hid behind the companion never connecting at all (#14480). A file check for a program needs `.exe` on Windows, and a hook written for a tool needs that tool's Windows shell.

## Paths the hub reads

- **A Windows node's paths reach a hub that may not be Windows**: the node reports directories as `C:\Users\…`, and on a Linux or macOS hub `filepath` splits only at `/`, so `filepath.Base` returns the whole path. `identity`'s directory checks (`IsAssistantDir`, `IsAssistantDirOf`, `SpaceOf`, `InHealthSpace`) missed the computer's assistant and health directories, and its main chat conversations never reached the Persona tab (#6419). `filepath.ToSlash` does not help there: it converts nothing off Windows.
  Code on the hub or a manager that takes apart a directory a node sent reads it through `identity.NodePath` first: a path with a drive letter or a `\\server\` prefix gets forward slashes on a hub that is not Windows, and anything else stays as it is. Test such code with Windows paths and run the test on Linux, not only on the computer you develop on.
- **`path` is not a node's syntax either**: it knows only `/` on every system, Windows included. Task workspaces checked the node's answers with `path.IsAbs`, so a Windows computer's `C:\…` was never absolute, and every writable task in a Git directory there failed with "Workspace C:\… is not an available absolute directory" (#7106).
  A path that is checked or joined and then sent back to its node goes through `proto.NodeAbs`, `proto.NodeClean` and `proto.NodeJoin`. They keep the node's own syntax (backslashes, an upper-case drive letter, a root of `C:\` or `\\server\share` that `..` cannot climb out of), so the node reads the result as its own path.
  Two of a node's paths compare by `proto.NodeKey`, which also ignores case for a Windows path: `C:\Code\App` and `c:\code\app` are one directory there. Keys are for comparing only; never send one to the node.

## Paths the node's file tools take

- **A short (8.3) name is a second name for the same entry**: NTFS usually gives a name that is not valid 8.3 a short alias as well (`C:\Users\EXAMPL~1`, `PERSON~1` for `.persona`, `GIT-CR~1` for `.git-credentials`). Windows hands them out itself: `TEMP` and `TMP` are usually `C:\Users\EXAMPL~1\AppData\Local\Temp`, so agents mention such paths. The path guard compares names, so before #9891 an alias got past it: secrets were readable, `.persona` took writes, files in the home reached through the home's alias were refused as read-only, and a save, move or copy through an alias gave the file the alias as its name.
  The file tools resolve every client path through `absPath`, which spells out short names with `GetLongPathName` (`longPath`, `node/longpath_windows.go`) before `realPath` and the guard see them. A new file tool resolves its paths the same way, its last name included (`statDetail` once joined that name as asked). `GetLongPathName` keeps link and junction names, and it needs the `\\?\` prefix past `MAX_PATH` unless long paths are switched on. Test with a home whose own name has an alias (`EvalSymlinks` the temp folder first: it is often written with aliases), and skip where the volume makes none.
- **`filepath.EvalSymlinks` does not see through a junction** (2026-11-20, #11277). A directory junction (`mklink /J`) is how a Windows user points one folder at another, for example Persona's private Claude profile at `~/.claude`. Go does not treat a junction as a symbolic link, and with Go 1.27.1 `EvalSymlinks` returned an error for a path through one. Code that dedupes directories by resolved path then sees a junction and its target as two directories. The assistant memory merge took the memory, reached through the linked profile, for another profile's copy of itself, and backed it up on every run. To tell whether two paths are one directory, compare them with `os.Stat` and `os.SameFile`, which follow a junction, as `recentDirs` and `memoryCopies` do. In tests, `cmd /c mklink /J` makes a junction without administrator rights.

## The Recycle Bin (file.trash)

`node/trash_windows.go` (#9933), checked on a Windows 11 machine on 2026-11-17:

- **With `FOF_NOCONFIRMATION` the shell deletes for good, silently, what it can't recycle**: `FOF_ALLOWUNDO` asks for the Recycle Bin only "if possible". A file on a network share (`\\localhost\C$\…`) or with a path over `MAX_PATH` would have been deleted permanently, and the warning about it is the confirmation `FOF_NOCONFIRMATION` answers. `SHFileOperation` can't tell beforehand. `IFileOperation` with a progress sink can: `PreDeleteItem` gets `TSF_DELETE_RECYCLE_IF_POSSIBLE` (0x80) only for an item that will be recycled; for any other the sink returns `E_ABORT`, and the item and a folder's contents stay.
- **A recycled item's result is a success code that isn't `S_OK`**: `PostDeleteItem`'s `hrDelete` was `COPYENGINE_S_DONT_PROCESS_CHILDREN` (0x00270008) for a file and for a folder. Test failure by the sign (`int32(hr) < 0`), never `!= S_OK`: the first version answered an error for a file that was already in the bin. A file open in another program fails with `COPYENGINE_E_SHARING_VIOLATION_SRC` (0x80270027), which has no Windows message; word it.
- **Where an item went**: `PostDeleteItem`'s `psiNewlyCreated` is the item in the bin, `<volume>\$Recycle.Bin\<user SID>\$R<6 characters><ext>`, and `$I<same>` beside it holds the original path (with long names, even when the delete was given an 8.3 path) and the time. Undo renames the `$R…` back and removes the `$I…`; Explorer then no longer lists it. A junction is recycled as the junction; its target keeps its files.
- **Tests that recycle use the user's own Recycle Bin**: there is no bin per test. Recycle only what is in the test's temp folders and put back what the test leaves, found by the original paths in the `$I` files (`keepNothingRecycled`). Shared tests that look for trashed files in a trash folder skip on Windows (`skipOnRecycleBin`).
- **A COM sink in Go**: `windows.NewCallback` takes pointer-sized parameters, pointer types included. Declare COM objects as `*shellItem`, not `uintptr`, so no `unsafe.Pointer(uintptr)` conversion is left for `go vet` to flag. COM keeps the sink's address after the call that is given it, so the sink is a package variable (never moved), and its callbacks are made once (`NewCallback` has a limit per process).

## Scheduled tasks (automatic startup)

`persona-link install` registers scheduled tasks on Windows ([windows.md](../operations/windows.md), “Install and start at logon”). These findings were all tested on a physical Windows 11 machine on 2026-11-04:

- **Standard users cannot register “Run whether user is logged on or not” tasks**: With S4U `LogonType` (no stored password), `Register-ScheduledTask` returns access denied (even for an unelevated administrator account).
  Only `InteractiveToken` works, running with the login session.
- **“Restart on failure” does not cover program exits**: `RestartOnFailure` restarts only when the task cannot start. A program that exited with code 1 was not restarted even after 4 minutes. For supervision, add a trigger that fires every few minutes,
  with the `IgnoreNew` multiple-instance policy (do nothing if already running).
- **Repetition on a logon trigger starts only at the next login**: Immediately after installation, before another login, it will not start every 5 minutes. Use a separate time trigger for supervision (`StartBoundary` set to the installation time); it still fires on schedule after a restart.
- **Console programs open windows**: A task running a console program in the user session opens a console window for it (a Windows Terminal window if Windows Terminal is the default terminal).
  Have the task start a GUI-subsystem program instead (a copy with Subsystem in the PE header changed from 3 to 2), which starts the console program with `CREATE_NO_WINDOW`: a console, but no window.
- **Change the defaults**: `ExecutionTimeLimit` defaults to 72 hours, after which the task ends (set `PT0S`); `Priority` defaults to 7, “below normal” (set 5); by default it will not start on battery and stops when unplugged.
- **schtasks output uses the system code page** (GBK on a Chinese system), and the `/Query` status column is localized (Running / 正在运行, both meaning “running”): display it, but do not parse it.
  Write XML files as UTF-16LE with a BOM so Chinese characters in usernames and paths are not corrupted.
- **Ending a task kills only the daemon itself**: Its children survive. The daemon puts itself in a Job with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` (subsequent children automatically join it),
  so when it disappears (`/End`, a crash, or a kill), its children disappear too.
- A running `.exe` cannot be overwritten or deleted. Before reinstalling, `/End` the task and wait for persona-link to release the run lock; during uninstall, the daemon file may take a moment to become deletable.

## Updating a running program (node updates)

`server/internal/nodeupdate`; see [windows.md](../operations/windows.md) ("Updates through the hub"). Found on a physical Windows 11 machine on 2026-11-12:

- **A running program's file cannot be replaced, but can be renamed**: `os.Rename(new, running.exe)` fails with access denied, while renaming `running.exe` away works. Move the old file aside, then put the new one in place; delete the aside file once its process is gone (deleting it earlier fails the same way).
- **Go's `os.Open` does not share delete**: a rename over a file this process (or any other) holds open fails with access denied. Close the file before renaming over it.
- **Only files ending in `.exe` start**: `exec.Command` on a staged `.node-update-stage-123` reports "executable file not found". Anything that is run (the staged build's self-check, the watchdog's backup copy) needs the suffix.
- **`FlushFileBuffers` needs a handle that may write**: `f.Sync()` on a file opened with `os.Open`, or on a directory, returns access denied. Open the file read-write to flush it; a directory cannot be flushed: use `MoveFileEx` with `MOVEFILE_WRITE_THROUGH` for a durable rename instead.
- **No inherited descriptors**: `exec.Cmd.ExtraFiles` is not supported, so a child cannot report readiness on fd 3. Report through a file both sides read.
- **A program runs from the file a process has open**: the watchdog runs from the backup it may restore, so the backup can only be moved aside, not deleted, until the watchdog exits.
- **A restart is a new process**: with exec the new program keeps the pid the rollback watchdog recorded; on Windows it does not, so a target build that never records itself (one from before Windows updates) kept running after a rollback put the old file back (found 2026-11-13 updating a node to a release without this support). Record the supervisor and find its children.
- **The image path follows a rename**: `QueryFullProcessImageName` returns where the program's file is now (the moved-aside name), while `Get-Process` / `Win32_Process.ExecutablePath` show the path it was started from. Match both.

## Claude Code's peer inbox (desktop app, other terminals)

Measured 2026-11-07 with Claude Code 2.1.308 / 2.1.291; see [claude-integration.md](../integrations/claude-integration.md) ("Claude outside Persona's terminals").

- **The inbox takes the message but the model may answer the wrong party**: Claude Code prefixes a peer's message with "Another Claude session sent a message" and tells the model to reply with SendMessage to `from=`. With no `from`, the first throwaway session answered "the session named Persona is unreachable" instead of "OK". The text must say the words are the user's own and to answer in the session (`prompts/agents/peer_message`).
- **A message without the `<cross-session-message>` envelope loses its sender and body in the transcript**: Claude Code fills `origin.name` / `origin.body` only from the envelope. Send the envelope, signed with `from-name`.
- **The inbox says nothing on the connection**: no ack, no error. Whether a message was taken shows only in the transcript (a `queue-operation`, or a hidden user line with a peer origin, within a few hundred ms). A bypass-permissions session holds every message without `from-mode` for the person at the computer; nothing reaches the transcript until it is approved.
- **Windows needs the auth line first**: the inbox drops any connection whose first line is not `{"type":"auth","token":…}`. The key file's name hashes the pipe path in lower case (`\\.\pipe\local\cc-msg-…`), not as `sessions/<pid>.json` writes it.
- **`entrypoint` is not always `claude-desktop`**: one desktop build writes `claude-desktop-3p`. Match the prefix.
- **`procStart` on Windows is a FILETIME** (100 ns since 1601) of the process's creation, not a Linux clock tick count; compare it with `GetProcessTimes` to tell a reused pid apart.
- **An interactive claude under a pseudo console needs its std handles to be the console's**: a `CreatePseudoConsole` child that inherits the parent's redirected stdout decides it is not on a terminal and runs as `--print` ("Input must be provided … when using --print"). Set `STARTF_USESTDHANDLES` with null handles.
- **Testing on a dev machine**: start a throwaway `claude` with its own `CLAUDE_CONFIG_DIR` (copy `settings.json`, write a `.claude.json` with `hasCompletedOnboarding` and the test directory trusted) and an empty working directory; never send into a session the user has open. The first failed start renamed the test `.claude.json` into `backups/`: write it again before the next try.

## Desktop projects and session creation

The Windows project/session extension was checked with local tests, mock protocols and read-only inspection of installed app schemas. Real app sends and new desktop sessions remain unverified. Native sidebar project creation and name synchronization are deferred, so native project requests are refused before side effects. Ordinary Persona project creation and existing native project discovery remain available. These findings do not extend the older physical-machine acceptance records.

- **Claude's index is not limited to roaming AppData:** installations can use local or roaming `Claude`, `Claude-3p` or `Claude-Data`, including Store redirection in `Packages/Claude_*/LocalCache`. Search each supported layout. Use the current process's AppData overrides only for the current user's home, so fixture or scoped-home reads cannot escape into personal data. Read session-index files and dedicated environment metadata, not credential-bearing app configuration.
- **A desktop-created worktree is not a new project:** use a matching `startedFromEnvironmentId` to retain the saved environment's name and identity, scoped to profile and account. Without that reference, prefer the index's `originCwd` over the working `cwd`, then resolve the physical directory and Git root. Apply the same grouping to live sessions, offline transcripts and workspace discovery. Normalize Windows case and separators; keep drive and UNC roots valid when browsing or walking to a parent.
- **Saved projects can outlive their sessions and terminal engines:** enumerate the desktop catalog independently. Codex can migrate projects to SQLite while leaving thread assignments in global state; retain the legacy/native mapping and merge both catalogs. Report source errors instead of replacing a previously known catalog with an empty success.
- **A scheduled task can have yesterday's PATH:** installing `sqlite3.exe` does not update an already running node's environment. Search the inherited PATH first, then the executable directory, current registered user/system PATH and WinGet links. Do not modify PATH to repair discovery. Run read-only SQLite queries without showing a console window.
- **Thread metadata creation must leave conversation ownership with the app:** a short-lived Codex app-server creates new thread metadata without a model request. Exit it before routing the first message through the owning desktop window; never resume an existing desktop thread in that metadata client. For directory-based Claude starts, import the locally seeded transcript and send through its peer inbox.
- **Project metadata, the native sidebar and the board are separate state:** Codex's project API writes native database records, but the inspected Windows sidebar reads a different catalog cached by the app. Claude's saved-environment mutations belong to its renderer store; reading the mirror does not expose creation or rename. An API response or a first imported session cannot confirm a same-named sidebar project. Both native `workspace.create` paths return `desktop_project_sync_unavailable` before directory or metadata creation. Disable those UI choices and direct users to create projects in the desktop app, then bind them. Do not promise that refresh/restart fixes it or patch JSON underneath the cache.
- **Directory creation and a board update are separate commits:** serialize the manager's revision check, Persona folder creation and board save; compare existing bindings by physical path on the node before side effects. Retain a created folder if the final board CAS fails so a retry can reuse it; deleting the directory as rollback could destroy user files. Native project requests fail before this flow.
- **Discovery does not prove a new session can join an environment:** Claude's verified transcript import has no verified named-environment selector. Reject `claude-environment:` selections with `claude_desktop_project_unsupported` before seed, import or send, including recovery. Directory-based starts remain available; validate a `claude-project:` selection against the chosen folder rather than silently changing its project.
- **A folder alone cannot identify a Codex project:** multiple projects can share it. Carry the catalog source ID, check its profile tag, resolve only verified legacy/native mappings and recheck the selected project's live roots. An unmigrated JSON-only project must fail explicitly instead of selecting another one by folder.
- **A failed start can already have created a conversation:** preserve the returned session ID and separate creation from first-message delivery. Retry content through that session's send route; retry an empty Claude conversation with its known `resume_session_id` to finish import only. An unknown delivery result must stay uncertain, not trigger another creation.

## Tray (WebView2, notification area)

The tray (`server/internal/linktray`, [windows.md](../operations/windows.md), "Tray") was removed with #6770 and restored, without notifications and with the Persona helper as its icon, on 2026-11-25. Found on a physical Windows 11 machine on 2026-11-07:

- **WebView2 holds back its callbacks while one of them runs**: creating a WebView inside a `WebMessageReceived` handler gets the environment back, but the controller's completion waits until the handler returns.
  go-webview2's `Embed` spins a nested message loop until then, so it never returned: the pairing window stayed hidden, and the tray's thread stayed inside that loop (which also swallowed the quit message). Handle page messages after the event has returned (`app.fromPage` queues them).
- **go-webview2's `PutRasterizationScale` passes a pointer to the double**: the COM method takes the double itself. On windows/amd64 Go's syscall also copies the integer arguments into the XMM registers, so pass `math.Float64bits(scale)`, as its `PutZoomFactor` does.
  With the pointer, pages rendered at scale 0.5: `devicePixelRatio` was 0.5 and everything was half size.
- **go-webview2 clears `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS`** (and the other override variables) before it creates the environment, so Microsoft's documented way to add `--remote-debugging-port` does nothing. Pass flags through `AdditionalBrowserArgs` (`PERSONA_TRAY_DEVTOOLS=<port>`).
- **Windows 11 puts new notification icons in the overflow**: UI Automation on the taskbar doesn't see them. `HKCU\Control Panel\NotifyIconSettings\<id>` (`ExecutablePath`, `InitialTooltip`) shows that the shell took the icon.
  A tray run from a temporary directory leaves an entry there; delete it after testing.
- **Tests must not touch the real registry**: the install tests once wrote the HKCU Run entry and the pairing link handlers, pointing at a test's temporary directory. Everything the tray does to the computer goes through `trayControl`, which tests fake.
- **A page that loses focus hides the flyout**: while scripting the panel over CDP, Playwright's clicks wait for it to be visible forever; click with `element.click()` in the page.
- **Push the new view to a hidden panel after an action**: the tray pushed views only while the panel was visible, so a switch flipped from the right-click menu, or an action that ended after the panel closed, left the page showing the old state, and the next open showed it too. After a user's action the tray pushes the view whether the panel is shown or not (`app.changed`).

## Codex

See [windows.md](../operations/windows.md) ("Codex") and [codex.md](../integrations/codex.md) ("Windows"). Found on a physical Windows 11 machine on 2026-11-07:

- **The Codex App's bus is a named pipe, not `<CODEX_HOME>/ipc/ipc.sock`**: `\\.\pipe\codex-ipc`, one per user session whatever the Codex home. Without it the App's chats stayed "view only" and nothing said why: the node looked for a socket file that never exists on Windows.
- **Node.js cannot connect to an AF_UNIX socket on Windows**: `net.connect(path)` always treats the path as a named pipe, so a runtime on Node.js dialing `runtime.sock` gets `connect EACCES`. The runtime SDK retries quietly, so the node log showed the companion running and never connected, and agy stayed a plain CLI launcher that cannot run the main chat. The node hands those runtimes a named pipe instead (`server/internal/node/runtimepipe_windows.go`).
- **`os.Stat` on a named pipe connects to it**: Go's Stat opens the path with `CreateFile`. On a pipe that takes one of the server's free instances (a test listener saw a client arrive) or fails with "All pipe instances are busy".
  To see whether a pipe exists, list it (`FindFirstFile` on the full name; it does not connect).
- **go-winio pipes have no buffer by default**: `ListenPipe(name, nil)` makes a pipe whose write waits for the other side to read. A fake server that writes two frames in a row (the answer to `initialize` and its own question) deadlocked against a client
  writing its next request. The App (Node/libuv) has buffers, so the fake must too (`PipeConfig.InputBufferSize` / `OutputBufferSize`).
- **The `codex://` handler of an MSIX package has no `shell\open\command`**: `HKEY_CLASSES_ROOT\codex` has `URL Protocol` and nothing to run; the shell activates the package (`AssocQueryString` with `ASSOCF_IS_PROTOCOL` gives its AppUserModelID).
  Checking for a command finds none; check `URL Protocol` and ask `AssocQueryString`. Open it with `ShellExecute` on a thread with COM initialised, not `cmd /c start` (a console window) or `rundll32`.
- **Without `ps` / `lsof`, every Codex scan said "could not tell"**: the resume check took that for "held", so nothing broke, but every running CLI listed as offline history and the Codex App's stdio app-server was never seen.
- **Who holds a file open, fast**: a process's handle table (`NtQueryInformationProcess(ProcessHandleInformation)`), filtered to File objects, then `GetFileType` before `GetFinalPathNameByHandle`. Asking a pipe handle its name can hang behind a read pending on it; asking its type does not.
  Asking the file instead (`FileProcessIdsUsingFileInformation`, `RmGetList`) costs about 40 ms per file: seconds for a Codex home.
- **npm installs `codex` as `codex.cmd`**: `exec.LookPath("codex")` finds the batch file, and running a batch file has `cmd.exe` parse the arguments again, the first message included (`"`, `&`, `%`, line breaks). Run the `codex.exe` the shim's `codex.js` would start.
- **Codex's shared daemon does not start from npm's launcher on Windows**: `codex app-server daemon start` fails with "this Windows launcher prevents background processes from outliving it", so a plain `codex` 0.163 runs in its own process there.
- **A Codex `unix://` endpoint with a drive path is no URL**: `url.Parse("unix://C:\…")` fails on a port after host `C`. Parse the path as a path.
- **`os.Symlink` needs Developer Mode or an administrator**: about ten node tests that make symlinks fail on a standard Windows account ("A required privilege is not held by the client"); they are not Codex failures.

## Machine monitoring (sysmon)

`sysmon/windows.go`; see [machines.md](../server/machines.md) for measurement definitions. Findings tested on a physical machine on 2026-11-04:

- **`NtQuerySystemInformation(SystemProcessorPerformanceInformation)` requires an exact-size buffer**: Given 4096 bytes, it returns `STATUS_INFO_LENGTH_MISMATCH`;
  increasing the buffer to the returned length does not help. It must be exactly “logical CPU count × 48 bytes.” Other classes (page files) allow increasing to the returned length. This was why all per-core data was initially empty.
- **The registry ProductName still says “Windows 10” on Windows 11**: They share the NT 10.0 kernel; identify Windows 11 by CurrentBuild ≥ 22000.
- **Google Drive's G: is a fake fixed disk**: `GetDriveType` reports `DRIVE_FIXED`, the filesystem claims FAT32, capacity copies C:, and it even has a volume GUID.
  Only asking for the volume's disk extents (`IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS`, no administrator privileges needed) reveals it: disk 0, offset 0, length 0. Exclude it using this, not guesses based on the filesystem name.
- **One `net.Interfaces()` call takes about 7 ms** (using `GetAdaptersAddresses` underneath), while reading counters for one adapter (`GetIfEntry2Ex`) takes only tens of microseconds. Cache the adapter list for one minute.
- Determine which adapters count from `MIB_IF_ROW2.InterfaceAndOperStatusFlags`: bit 0 is HardwareInterface, bit 1 is FilterInterface. Tailscale (wintun),
  Wi-Fi Direct, Bluetooth personal-area networking, and loopback all lack HardwareInterface.

## Orca

- **Orca runtime on Windows uses named pipes**: `transports` in `orca-runtime.json` contains `named-pipe` (`\\.\pipe\orca-<pid>-…`) and `websocket`, but no `unix`.
  `orcart` selects the transport by platform: on Windows, `named-pipe` first (`tailscale/go-winio` for dialing), then `unix`; on other platforms, only `unix`. The protocol (one JSON per line) is unchanged.
- Orca terminals have `ORCA_PANE_KEY` / `ORCA_AGENT_PANE`, just like macOS, plus `ORCA_USER_DATA_PATH` (`%APPDATA%\orca`).
- Paths in the `topologyRevisions` keys of `terminal.list` mix separators (`C:/Users/…` and `C:\Users\…` on the same computer). Do not compare them directly to `worktreePath` as strings.
- **Orca's terminals run Windows PowerShell 5.1 unless the user changed Orca's `terminalWindowsShell`**. A POSIX line typed there (`cd -- '…' && …`, `'\''` quoting) leaves no error in any log: PowerShell rejects `&&`, or an odd count of quotes leaves it at its `>>` continuation prompt, and the node waits for an Agent that never starts (#9810).
  Open the terminal in the shell you type for instead of translating the quoting: `terminal.create` takes `shell` (`git-bash`, `powershell.exe`, `pwsh.exe`, `cmd.exe`, `wsl.exe`), and only a runtime that advertises `terminal.create-shell-selection.v1` in `status.get` honours it; an older one ignores the field and starts its default.
  The fake runtime (`orcarttest`) has no shell, so unit tests cannot show this: check a change to what the node types into Orca against the real Orca on Windows.

## herdr

- **herdr's API is a named pipe whose name is the socket path**: herdr uses interprocess's `GenericNamespaced`; `C:\Users\<you>\AppData\Roaming\herdr\herdr.sock` maps to
  `\\.\pipe\C:\Users\<you>\AppData\Roaming\herdr\herdr.sock`. The path itself is not a file; check the pipe namespace without opening the pipe (see the `os.Stat` pitfall above). The configuration directory is `%APPDATA%\herdr`, not `~/.config/herdr`.
- **`herdr server` started over ssh exits with the ssh session** (Windows OpenSSH ends the entire job when closing a session); `Start-Process` behaves the same way. For a persistent server, use a scheduled task triggered at login, running in the user's desktop session.
- **Panes default to PowerShell**: the node's start commands use POSIX shell syntax (`'…'` quotes, `A=1 cmd`), which PowerShell splits incorrectly. Setting `[terminal] default_shell` in `config.toml` to Git Bash is the setup known to work.
  This key must be under `[terminal]`; at the top level, `reload-config` only reports “unknown config key default_shell; ignoring key.”
- **Quote a typed start for the shell that reads the typed line, not the shell that reads the launcher definitions** (#8107): launcher sync's `"shell": "bash"` describes the Git Bash that reads `~/.bashrc`, while the pane that receives the line is PowerShell. `shellQuote` turns a Windows path into the POSIX shape `'C:'\\'Users'…`, which PowerShell splits into separate arguments. For a PowerShell pane the node therefore writes the whole POSIX start, with its `PERSONA_HOME` / `unset` prefix, to a start file and types only `& '<Git Bash>' -ic '<source command>'`; the leading `&` is required to run a quoted program. A PowerShell single-quoted string must double each of the five characters PowerShell takes for a single quote (ASCII `'` and U+2018 through U+201B). The nested Bash source path uses ANSI-C `$'…'` quoting: the usual POSIX `'"'"'` form would put embedded double quotes through Windows PowerShell 5.1's lossy rebuilding of native arguments. `-ic`, not a guessed `-l`, is the existing `loginShell` mode that makes `~/.bashrc` launchers visible.
- **A fix for a platform nobody can test changes only what is known to be broken** (#8107): the handoff was written without a Windows machine, so every start that already worked types exactly the bytes it typed before, checked against literals captured from the code before the change: Git Bash however the node learns of it, a host-named shell other than PowerShell, an unknown shell, an Orca, Tern or herdr-core pane that names no shell, an adopted pane (Run Agent Here) and herdr's own `resume_argv`. Only a new classic-herdr pane in PowerShell, a host-named PowerShell and a classic-herdr pane in `cmd.exe` (refused with the reason instead of failing after the wait) behave differently. An adopted pane whose innermost program is PowerShell later joined them (#9854). Case, slash and `.exe` normalization of Windows shell names lives only in that new decision, not in the shared `takesAssign` / `shellProgram`, which other checks read. PowerShell's `ParserError` / `Unexpected token` words are looked for only after a start that typed the `&` line, so an Agent's ordinary JavaScript output cannot cost a POSIX start an extra full-screen read.
- **An adopted pane's programs are also the shell-prompt check's input** (#9854): Run Agent Here, a stopped session resumed in its own terminal and a launcher or engine switch read the programs in front of the pane twice, once to decide that only shells are there (`shellProgram`, `allShells`) and once for the start. Teaching the shared list PowerShell's Windows names would change which panes those checks accept, so the names are recognised only inside the start: `startPaneShells` turns a stack whose innermost program is PowerShell or pwsh into the new pane's PowerShell marker (`adoptedPaneShells`, Windows only), and the list handed to the checks stays the raw programs.
- **A fresh Claude Code install first shows the theme-selection onboarding**: node's `waitReady` cannot find the input box within a minute and reports “claude did not become ready,” while the screen shows a theme list. Set `hasCompletedOnboarding: true` in `~/.claude.json`, or run it manually once.
- **apiKeyHelper is executed by cmd on Windows**: `cat ~/.config/…` returns “'cat' 不是内部或外部命令” (“'cat' is not recognized as an internal or external command”); use `type "C:\…\key"`.
- **MinGit does not contain bash.exe** (only `usr\bin\sh.exe`). For Git Bash, use PortableGit or the installed distribution.
- **Quotes are parsed again by cmd when running PowerShell over ssh**: For long scripts, use `powershell -EncodedCommand <UTF-16LE base64>`. PowerShell 5.1 also splits arguments such as `-test.run=A|B` incorrectly when passing them to native programs; wrap them in `cmd /c "…"`.
- **`signal.Ignore(os.Interrupt)` cannot block Ctrl+C**: On Windows, only `signal.Notify` disables the default exit behavior for Ctrl+C / Ctrl+Break (see the os/signal documentation). When `osproc.Exec` used Ignore,
  one Ctrl+C in the console ended the launcher with `0xc000013a`; the child (claude) survived, the parent disappeared, and the exit code was lost. `TestExecSurvivesCtrlC` guards this.
- A test program `xxx.test.exe` can keep running briefly after exit (child processes), making scp fail to overwrite it with `dest open … Failure`. Use another filename or end the process first.
- **Stopping a background shell does not stop the mock hub it started, and a second mock still binds the port**: on Windows, ending the Git Bash job that ran `python3 scripts/mock/mock_hub.py` leaves the Python process running, and Python's `HTTPServer` sets `SO_REUSEADDR`, which on Windows lets a restarted mock bind the same port too. Requests then go to either process, so a fix in the mock seems to have no effect (an unknown `/mock/…` route answers 404). Before restarting, look with `netstat -ano | grep :<port>` and end your own old process by its PID (`taskkill //PID <pid> //F`), never every Python.

## Multiline terminal input and failed starts (#11145)

A terminal newline is input, not a safe way to delimit a message. On Windows, herdr prompts use explicit bracketed
paste followed by a separate Enter, checking that the same Agent is still in front and has not opened a dialog.
This applies to Claude Code and other detected runtimes too. Codex starts with a supplied first message instead use
the protected launch file and one native argv value, bypassing the terminal input stream entirely. Resolve npm shims
to the native executable on the final managed-provider hop too. Keep full rollout matching and precise failure
cleanup: argv acceptance is not a delivery receipt. Windows limits the quoted command line to 32,767 UTF-16 units
including NUL; reject overflow without truncation or a paste fallback. Codex App uses structured IPC input.

Codex can display a paste as `[Pasted Content N chars]` followed by literal tail text, or several chips. An anchored
chip-only check rejects such a composer even when the input arrived. Match chip lengths in Unicode characters and
visible fragments against the full expected input; a prefix chip with its tail still in flight must wait. Codex
normalizes line endings before counting. Confirm the complete first message in the rollout after submitting once. A chip is not a
delivery receipt or evidence of ConPTY corruption. A compact footer with directory `~` and warnings is status, not
draft content. Keep any real draft above that footer protected.

An older desktop-bundled CLI can precede a newly upgraded npm CLI on PATH. Check the executable the launcher's
actual environment resolves, not only npm's version. Startup skips Codex's recognized update offer with Escape on
all systems; it must not select the default installer. A late offer can consume or hide a paste: after skipping,
reuse a matching draft, or replay once only into an empty unqueued composer. Unknown dialogs and foreign drafts
still refuse; failure closes only the launch's own new pane. Capture an incident's rollout and version before
attributing an older truncation to an update offer.

Closing a failed start's new pane does not archive a rollout it already published. Identify that rollout only through
the pane's explicit thread ID or its unique rollout writer, and require its start time to belong to the launch.
Close the owned pane before requesting Codex's reversible native archive. Never use a shared directory, title or
matching first line as cleanup authority. Adopted panes remain the user's, and cleanup failures remain visible and
must finish before another start with the same directory and launcher. A deployed incident needs its own version
and transport evidence; a source-level transport regression alone does not prove which installed boundary failed.

A native acceptance later failed even with a mixed-chip matcher: a long visible prefix appeared without its line
separators/non-BMP character, followed by two chips. Screen text cannot distinguish renderer loss from actual input
loss. Codex 0.170.0's external editor exports `composer_text_with_pending`, including expanded paste elements; a
disposable pane with a process-local read-only capture editor can measure that actual draft without submitting it.
Never relax the matcher to silently ignore missing characters. Process-boundary regressions must run the real
private-file helper and receiver; on WSL, execute the Windows PE variant too, not only a fake composer.

Desktop launch instructions are not a first message. When combining automatic platform instructions with a
desktop start, keep a blank user prompt blank: Claude may import an empty session, while Codex must refuse before
creating one. Test this with explicit task instructions too, so the check covers hosts without automatic platform text.
