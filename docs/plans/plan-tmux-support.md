# Plan: tmux as a terminal engine (#1180)

Status: proposal, waiting for the maintainer to decide ("Points for the maintainer to decide" below). Once decided, implement it in the order of "Phases"; when done, merge the design parts into `docs/integrations/engines.md` and delete this plan.

## Background

Many people open Claude Code / Codex directly in tmux panes. Today these sessions show only as "Not running" in the App: you can see the transcript but not the state,
and sending a message starts another `claude --resume` in herdr. tmux appears in Persona only indirectly: botmux sessions run in its own tmux
and are listed by the botmux host (`server/internal/node/hostbotmux.go`) through `botmux observe`, without reading tmux directly.

The host interface has already been extracted (`server/internal/host/host.go`, design in `docs/integrations/engines.md` ("Host interface")), so tmux is just one more host implementation:
type `tmux`, the host lives in the node package (new file hosttmux.go), plus a thin tmux client package (new package internal/tmux: run commands, parse `-F` output, read/write control mode).
The shared layer above hosts (recognizing sessions, deciding state, pre-send checks, capability reporting) stays as is, with no `if` added for tmux.

## Measurements (tmux 3.7c, macOS, only on a self-started `-L persona-1180-probe`, kill-server afterwards)

All conclusions below were run on a separate socket; anything not run is marked "unverified". No real claude was run inside tmux (the state items need to be tested during implementation).

| What was tested | Result |
| --- | --- |
| `list-panes -a -F` | Gets every session / window / pane in one call: `#{session_id}` `$0`, `#{window_id}` `@1`, `#{pane_id}` `%2`, `pane_pid` (the pane's shell), `pane_tty`, `pane_current_command` (foreground process name), `pane_current_path`, `pane_width`×`pane_height`, `pane_dead`, `pane_in_mode`, `alternate_on`, `pane_title` |
| Environment of processes in a pane | `TMUX=<socket path>,<server pid>,<session index>`, `TMUX_PANE=%0` |
| Foreground process | In `ps -t <pane_tty>`, the one whose stat has `+` is in the foreground (`sleep 30` is `S+`, the shell is `Ss`); `pane_current_command` also gives the foreground process name |
| `capture-pane` | `-p` plain text of the current screen; `-e` with SGR (tmux re-emits it its own way, e.g. `\e[31m…\e[39m`); `-J` rejoins soft-wrapped lines (a 250-character line in a 120-column pane reads back as 250); `-S -` includes scrollback history (default `history-limit` 2000 lines) |
| Bracketed paste | `set-buffer -b <name>` + `paste-buffer -p -d -b <name>`: when the program enabled `?2004h` it receives `ESC[200~…ESC[201~`, multiple lines as one block; `-d` deletes the buffer after use (`list-buffers` is empty afterwards) |
| Keys | `send-keys -l '<text>'` types literally (`; Enter C-c $HOME` are not expanded); `send-keys BTab` and `send-keys -H 1b 5b 5a` both produce `ESC[Z` |
| **Copy mode** | When the pane is in copy mode (`pane_in_mode=1`), text from `send-keys` is swallowed by copy mode and the program never gets it; `paste-buffer` still writes to the program (copy mode does not exit) |
| **Synchronized input** | With `synchronize-panes` on for the window, `send-keys` to one pane reaches every pane in the window; `paste-buffer` writes only to the target pane |
| Panes kept after exit | With `remain-on-exit on` the pane stays after the program exits, `pane_dead=1` |
| Creating | `new-window -d -t <session>: -c <dir> -n <name> -P -F '#{session_id} #{window_id} #{pane_id}'` returns the new pane's ids directly; `-d` does not switch away from the window currently shown on the computer |
| Title | A title set by the program with OSC 2 goes into `pane_title` (`⠸ busy title` verbatim); when nobody sets one, the default is the **hostname** (must not be shown as the session name, nor sent to the hub) |
| Control mode without resizing | `tmux -C attach` without a size keeps the window at 120×40 (client size empty); adding `-f ignore-size` is the same |
| Control mode resizing | `refresh-client -C 50x20` changes the window to 50×20; **after the control client exits, a session with no other client stays at 50×20** and does not restore itself |
| Control mode stream | `%output %<pane> <bytes>`: the program's **raw output** (octal-escaped), not a redrawn grid; `-f no-output` turns it off |
| Control mode notifications | `refresh-client -B 'st:%*:#{pane_title}\|#{pane_current_command}'` subscribes to all panes; on change you get `%subscription-changed st $0 @0 0 %0 : ⠸ working\|sleep`; there are also `%window-add`, `%window-renamed`, `%layout-change`, `%session-changed`. The `#` in the command must be quoted, otherwise it is treated as a comment (`parse error`) |
| `pipe-pane -O` | Writes the program's raw output to a command, the same bytes as `%output` |
| Socket directory | The local `/tmp/tmux-<uid>/` had two socket files but no tmux process at all: **socket file present, server already gone is the normal case** (did not connect to them, only looked at the directory and `ps`) |

The probe scripts are outside the worktree (`~/dev/persona-1180/probe/`) and not in the repo; during implementation they become `hosttmux_live_test.go`.

## Point-by-point answers

### 1. Discovering sessions: how to find multiple tmux servers

**Finding servers**: tmux has no command to "list all servers"; a server is just a socket. Three sources are merged and deduplicated by socket path:

1. Every socket under the default directory `$TMUX_TMPDIR/tmux-<uid>/` (when unset, `/tmp/tmux-<uid>/`, which macOS resolves to `/private/tmp/…`): everything started with `-L <name>` is here.
2. The `TMUX` environment variable of claude / codex processes in the process table (its first field is the socket path): servers started with `-S <any path>` can only be found this way.
   The process table must be scanned anyway (to recognize sessions); the Orca host already has a two-step scan, "first `ps -o pid=,stat=,tty=,args=` to find claude / codex, then `ps -E` only on those". Reuse it, taking only `TMUX`, `TMUX_PANE`, `BOTMUX_SESSION_ID` from the environment.
3. (Optional, see open point 1) listed explicitly in `machines[].tmux_sockets` for that computer in hub.json.

Each socket is queried with `tmux -S <path> list-panes -a -F …`; connection refused / the file is not a socket means a dead socket file (the normal case, as measured above), treated as no server, without logging (same as herdr-core's `ErrNotRunning`).
Never clean up other people's socket files.

**Recognizing Agents**: match by tty, not environment variables. Each pane's `pane_tty` is unique on the whole machine; a claude / codex process in the process table counts as the Agent in a pane only if its tty equals that pane's `pane_tty`,
its stat has `+` (foreground process group) and does not start with `T` (not suspended). Nesting then separates naturally:
when tmux runs inside a herdr pane, claude's tty belongs to the tmux pane; when herdr runs inside a tmux pane, claude's tty belongs to the herdr pane and tmux does not claim it
(`docs/lessons/agents.md` records the tmux server inheriting `HERDR_PANE_ID` and reporting botmux sessions into a herdr pane: judging ownership by environment variables is unreliable).
compatible Codex forks follows the shared layer's approach (owner file); the host only provides "all panes". Session ids follow the shared layer: pid of the foreground claude → `sessions/<pid>.json`.

**The tmux command itself**: a node resident under launchd has only the system PATH, so try PATH first, then `/opt/homebrew/bin/tmux`, `/usr/local/bin/tmux` (same as botmux looking for `~/.botmux/bin/botmux`).
When the client and server versions differ too much, tmux reports `protocol version mismatch`: report that server as incompatible with a `note` in `node.info`; do not guess (the exact error text is unverified; test between two versions during implementation).

### 2. Mapping spaces / slots

| Persona | tmux | Notes |
| --- | --- | --- |
| `space_kind` | `tmux` | |
| space | session | `space_id` = `<server>/$<n>`, name is the session name, ordered by `session_created` |
| slot | window | `slot_id` = `<server>/@<n>`, name is the window name, index is `window_index` |
| pane | pane | see `PaneRef` below |

- **Pane id**: tmux's `%N` is unique only within one server and restarts from `%0` after a server restart. So it is stored as `tmux:<server>/%N`, where `<server>` is the socket (the name for the default directory, e.g. `default`; the full path elsewhere) plus the server pid:
  `tmux:default@4242/%3`. The hook builds the same string from `TMUX` (its second field is the pid) and `TMUX_PANE`. When the server's pid changes, old bindings expire naturally and never land on the new server's `%3` (see open point 3).
  Add `tmux` to `host.Kinds`: herdr ids look like `w1:p2` and never start with `tmux:`, so existing data needs no migration.
- **Session groups** (`new-session -t` sharing windows): the same pane is listed once under each session in the group; deduplicate by pane id and assign it to the group's earliest-created session.
- Spaces of **multiple servers** are independent (the id includes the server); the App's grouping key being "machine + space_kind + space_id" is already a step to be done (`docs/integrations/engines.md` ("Shared layer above hosts")).
- Hostname titles: when `pane_title` equals the hostname (tmux's default), treat it as no title.

### 3. Reading the screen and live streaming

**Reading the screen** (`Read`): `capture-pane -p`. tmux gives more than Orca or herdr-core, so it does not declare `PlainScreen`:

| `ReadOpts` | tmux |
| --- | --- |
| `visible` | `capture-pane -p` |
| `recent` | `capture-pane -p -S -<Lines>` |
| `recent-unwrapped` | plus `-J` |
| `ANSI` | plus `-e` (SGR as re-emitted by tmux; check it against recorded real output for the App's VT subset) |

So Claude's pre-send checks (draft / greyed text, `ensureSubmitted`), colored screen reads and scrolling up via `/screen/before` all take the full path, same as herdr.
Note: when the program is on the alternate screen, `capture-pane` reads the alternate screen while the history belongs to the main screen (Claude does not use the alternate screen by default); when the pane is in copy mode it reads the program's screen, not the screen scrolled to on the computer (unverified; test `-M` during implementation).

**Live streaming** (the terminal tab, `Streamer`): three options compared:

| Option | What you get | Verdict |
| --- | --- | --- |
| Polling `capture-pane` | grid snapshots with color, no push | can only refresh once per second; that is the current "fall back to screen reads" |
| `pipe-pane -O` | the program's raw bytes; each pane can have only one pipe, so the user's own `pipe-pane` (recording, logging) would be displaced | not used |
| Control mode `tmux -C attach` | `%output` pushes the program's raw bytes; without a size it **does not resize panes** (measured); one connection covers all panes of the whole server | use it, but do not forward the bytes directly |

`%output` is the program's raw output, not grid-redrawn frames like herdr observe; the App's own VT subset cannot handle arbitrary raw output (the same issue as in `docs/integrations/herdr-core.md` ("herdr-core host")).
So: **`%output` is only a signal that "this pane changed"; frames are fetched fresh with `capture-pane -e -p`**, combined with `#{cursor_x},#{cursor_y}`, `#{cursor_flag}` into one frame
(full-screen `ESC[2J` + per-line `CUP` + line content + cursor position), with changes within 30 ms merged into one (the node's current `termMinGap`). The node remembers each line of the previous frame and redraws only changed lines, which gives incremental frames.
The frame format is the same as herdr observe, so the hub and App need no changes. Size comes from `pane_width`×`pane_height`; after `%layout-change`, send a full frame at the new size.

**Takeover** (`Controller`, phone size): `refresh-client -C` can resize, but it resizes the whole **window** (other panes in the window change too), and as measured, after the control client leaves, a session with no other client stays at the phone size,
unlike herdr, which restores automatically. Doing it would mean recording the original size and restoring it when the node exits, which cannot happen if the node is `kill -9`ed. Suggest no takeover in the first version; the terminal tab only mirrors at "computer size" (see open point 6).

### 4. Sending messages and keys

Two measured pitfalls decide the approach: `send-keys` is swallowed in copy mode and, in a `synchronize-panes` window, typed into every pane of the window; `paste-buffer` writes only to the target pane's program in both cases.
So **all input goes through a named buffer + `paste-buffer`, never `send-keys`**:

- **`Prompt`** (send a message): first confirm the Agent is in the foreground (the pane's tty has a foreground claude / codex in the process table, and the pane is not `pane_dead`); otherwise `ErrNoAgent` / `not_delivered`, nothing typed.
  Then `load-buffer -b persona-<random> -` (text comes in over stdin, free of command-line length limits) + `paste-buffer -p -d -b persona-<random> -t <pane>`: if the program enabled bracketed paste it arrives as one block, and multiple lines do not submit at the first newline.
  Wait 150 ms (plus 10 ms per line, at most 1 second, same as Orca), confirm once more that the Agent is in the foreground, then paste a lone `\r` to submit. Any failure after the paste returns `delivered:unknown`.
- **`SendText`** (type without Enter): the same buffer, without `-p` (tmux converts `\n` to `\r` by default; add `-r` to keep it as is). Doable; no `TextTyper` limitation declared.
- **`SendKeys`**: key name → xterm bytes, reusing Orca's table directly (`orcaKeyBytes` in `hostorca_keys.go`, moved to a shared place); the bytes also go through `paste-buffer`; one key at a time, 40 ms apart (so `esc` does not combine with the following sequence into alt).
  Keys not in the table are rejected before any key is pressed; keys that type characters get the foreground check first; Enter, esc, arrows and ctrl+c are not checked (a stuck pane must still be interruptible).
- **Copy mode**: `paste-buffer` still delivers, and we do not exit copy mode for the user (that would change the screen on the computer); the screen reads the App sees are the program's screen.
- Buffers are deleted with `-d` after use; names have a random suffix so they do not clash with the user's own buffers. Failure paths run one extra `delete-buffer`.

Answering questions / plans / permissions: claude started by Persona has the hooks and goes through cards just like herdr; claude opened outside has no Persona hooks and is answered by reading the screen (`screen.ParseDialog` + keys),
same as Orca and Herdr App, but because colors are available the draft detection is complete.

### 5. Starting new sessions

`Launcher` can do everything:

- `Spaces`: the sessions of all servers. `CreateSpace`: `new-session -d -s <name> -c <dir> -P -F …` on the **default socket** (this starts a server if there is none; see below).
  `CreateSlot`: `new-window -d -t <server>/<session>: -c <dir> -n <name> -P -F …`. All with `-d`, so they do not switch away from the window being viewed on the computer (just as herdr does not steal focus).
- `Run`: paste the command + `\r`. The pane runs the user's interactive `default-shell`, so launchers in `~/.zshrc` expand as usual and `docs/integrations/launchers.md` needs no change; `personad launch <name>` works too.
- `NameAgent`: tmux has no "Agent name"; write it as a pane user option `set -p -t <pane> @persona-agent j-xxxxxx`, without changing the title (claude changes the title itself).
- New sessions are started by Persona and have the hooks (`--plugin-dir`), so state and cards are as complete as in herdr.
- **Environment of servers started by the node**: a tmux server's global environment comes from the process that started it. A server started by a node resident under launchd has only the system PATH and the node's environment,
  and every pane the user later opens in it inherits that. So the node starts a server only when **there is no server at all**, using the login shell's environment (`$SHELL -lc 'tmux new-session -d …'`) with Persona's own variables removed (see open point 5).

### 6. State

tmux does not know the state of the program in a pane. Sources in priority order:

1. **Persona's hooks** (claude started by Persona): question, permission, plan → Needs you; `UserPromptSubmit` / `Stop` → Working / done, same as on herdr machines.
2. **Title**: while working, Claude Code puts a spinner character in front of the terminal title (`stripSpinner` in `hostherdrcore.go` already strips it); a braille spinner → Working (the compatible fork's current check).
   Control mode subscribes to `pane_title` and pushes changes immediately, no polling (not verified with a real claude in tmux; test during implementation).
3. **Screen reads**: the compatible fork's approach, at most one read every 2 seconds: `esc to interrupt` → Working, a dialog recognized by `screen.ParseDialog` → Needs you, anything else → done.
4. The transcript fills in "last line" and "done, not seen" as usual (the hub derives `unread` / `idle` from whether it was seen, `docs/protocol/agent-states.md`).

Codex follows the shared layer (corrections such as `codexTurnOver`).

**Hooks recognizing panes**: when there is no `HERDR_PANE_ID`, `HC_TERMINAL_ID` or `ORCA_PANE_KEY`, `HookPane` in `personad hook` reads `TMUX` + `TMUX_PANE` and reports `tmux:<server>/%N`.
When tmux runs inside a herdr pane, claude has both (`HERDR_PANE_ID` is inherited), so on machines that chose the tmux engine `TMUX_PANE` takes precedence;
the node records tmux panes only on machines whose engine is tmux (same as Orca), so herdr machines are unaffected.

### 7. No duplicates with botmux

botmux's tmux sessions are listed by the botmux host and skipped by the tmux host: if the environment of a pane's foreground process has `BOTMUX_SESSION_ID` (botmux sets it on every process it starts, `botmuxPaneEnv` in `hostbotmux.go`), it is not an Agent.
The fallback is a session name starting with `bmx-` (that is what botmux observe's `backend.sessionName` looks like in the test fixtures; not verified against a real botmux).
This is the same rule as the botmux host stepping aside from herdr panes when botmux uses the herdr backend, just reversed: a session belongs to exactly one host. For what to do when botmux is turned off (`machines[].botmux: false`), see open point 8.

### 8. Engine selection

- `proto.EngineTmux = "tmux"`, display name "tmux", description "tmux in the terminal"; add one entry each to iOS `MachineEngine` and Android `Engines.kt`; the web shows whatever the hub's `engines` lists.
- `engines` in `node.info`: "detected and available" only if the `tmux` command is found and at least one socket has a server answering; only dead socket files counts as not detected. Version from `tmux -V`.
- `engineHosts("tmux")` registers only the tmux host (+ botmux as before).
- **Side by side**: `herdr-both` already does "refresh multiple hosts in parallel, isolate events and online state per host", so adding `herdr-tmux` is just one more combination; but combinations keep multiplying,
  and a better shape may be "a set of checked hosts" instead of an enum. Not in phase one; see open point 9.

## Aligning with the host interface

| Interface | tmux host | Phase |
| --- | --- | --- |
| `Host` (`Snapshot`, `Agents`, `Agent`, `Read`, `SendText`, `SendKeys`, `Prompt`, `PaneProcesses`, `PaneEnv` = `TMUX_PANE`) | 1–4 above | one |
| `Organizer`: `RenameSpace` = `rename-session`, `RenameSlot` = `rename-window`, `Close` = ctrl+c twice then `kill-pane` (for the last pane, tmux closes the window itself) | doable | one |
| `Launcher` / `SpaceLister` | 5 above | one |
| `Watcher` | one control mode connection per server (`-f ignore-size,no-output`), subscribing to titles, foreground commands and window add/remove; phase one polls `list-panes` every second (a few ms per call) | two |
| `Streamer` | 3 above: triggered by `%output` + frames from `capture-pane -e` | three |
| `Controller` | not done (see open point 6) | — |
| `PlainScreen` / `TextTyper` | not declared (has color, can type without Enter) | — |
| `MetadataReporter` | not done (herdr-specific) | — |

Errors follow `host.Mark`: failed foreground check, dead pane, server gone → `ErrNotDelivered` (`delivered:none`); server gone → `ErrNotRunning`; dialog open → `ErrDialogOpen`.

## Phases

1. **Phase one**: discovery (the first two of the three sources), listing and state (hooks + title + screen reads, polled every second), colored screen reads, sending messages, keys, answering dialogs, closing, renaming, start / resume, hooks recognizing panes, botmux dedup, the engine option (node + both Apps).
   Covers the issue's acceptance criteria; the terminal tab falls back to screen reads.
2. **Phase two**: the control mode `Watcher` (push; polling drops to a 10-second fallback).
3. **Phase three**: live mirroring in the terminal tab (`Streamer`).
4. Later: side by side (herdr + tmux), takeover (if decided).

## Test plan

tmux can run for real in tests, fully isolated (`TMUX_TMPDIR` pointed at `t.TempDir()`, a random `-L` name, `kill-server` at the end), so tests mainly use real tmux rather than a fake:

- **Host tests** (`hosttmux_test.go`, `t.Skip` if the machine has no `tmux`): start panes on an isolated server, run a fake `claude` in them (a script written by the test: enables bracketed paste, writes received input to a file, prints dialog screens on demand),
  and assert the listed sessions, spaces / slots, the three screen-read sources and colors, that `Prompt` delivers one bracketed-paste block plus a `\r`, key bytes, and these edge cases:
  delivery still works in copy mode, `synchronize-panes` writes only the target pane, a `pane_dead` pane is not an Agent, a suspended one (`kill -STOP`) is not either, session groups are listed once,
  after a server restart an old `PaneRef` (pid changed) never hits a new pane, dead socket files do not error, no buffers are left behind.
- **node layer**: drive it from the node's request entry points like `herdr_*_test.go` (list sessions, send, close, start), confirming the shared layer takes no herdr branches on the tmux host; `HookPane` precedence (both herdr-inside-tmux and tmux-inside-herdr environments);
  botmux dedup (fake `claude` with `BOTMUX_SESSION_ID`); switching with `engine.use tmux` and detection in `node.info` (server present / only dead sockets / tmux not installed).
- **CI**: GitHub's macOS and Ubuntu images both ship tmux (not checked against the current images); if not, skip and say so in the log.
- **Live testing** (`-tags tmuxlive`): only read-only calls against the tmux the user is actually using (allowlist: `list-panes`, `list-sessions`, `capture-pane`, `display-message`; any other command is rejected before it is sent),
  printing panes, recognized Agents, pids and session ids, cross-checked against `sessions/<pid>.json`, without printing screen contents. Write operations only on servers the test starts itself.
- **Real claude**: start a claude with a launcher in an isolated server, confirm the title spinner, answering dialogs from screen reads, hook cards, start / close; write the conclusions into `docs/lessons/`.
- **App**: the engine option and "detected / not detected", iOS and Android screenshots in dark and light; the terminal tab is tested separately in phase three (record real frames as fixtures and compare line by line with `capture-pane`).
- **Acceptance** (from the issue): on a computer with only tmux installed, after choosing the tmux engine, Claude / Codex are listed, state is correct, you can view, send messages, answer questions / permissions, start new sessions, the terminal tab works, and botmux sessions are not duplicated.

## Points for the maintainer to decide

Each comes with a recommendation; reversible, low-impact ones (buffer naming, timeouts, intervals) are settled during implementation as recommended and not listed here.

1. **Which servers to find**: recommend the default directory + reverse lookup from the Agent processes' `TMUX` as enough; should hub.json's `machines[].tmux_sockets` be added for the case "started with `-S`, no Agent in it yet, but you want to start one from the phone"? Recommend not adding it for now.
2. **Can the control mode connection stay resident**: a control client shows up in `list-clients`, sets `session_attached` to 1, and triggers the user's configured `client-attached` hook.
   Both push (phase two) and live streaming (phase three) need it. Recommend accepting it, minimizing the impact with `-f ignore-size,no-output`, and documenting it in `docs/integrations/engines.md`; if not accepted, there is only per-second polling and no live terminal.
3. **Should pane ids include the server pid**: recommend yes (`tmux:default@4242/%3`), so after a server restart old bindings never point at a new pane with the same number; the cost is that after a server restart, bound sessions must be recognized again from processes (the shared layer looks at processes first anyway).
4. **Space = session**: recommend mapping session / window / pane to space / slot / pane; the alternative is splitting spaces by working directory like botmux (ignoring tmux's structure), clearer for people who stuff dozens of windows into one session, but it does not match what the user sees on the computer.
5. **Can the node start a tmux server for the user**: starting from the phone with no server means the node starts one on the default socket, and the server's global environment is inherited by every later pane.
   Recommend allowing it, started with the login shell's environment; or not starting one at all, and when there is no server, telling the user "run tmux once on the computer first" (the same requirement herdr has today).
6. **Terminal tab mirrors only, no takeover**: resizing in tmux resizes the whole window and does not restore automatically (measured). Recommend mirroring only in the first version; takeover would mean accepting "the window stays at phone size if the node is killed".
7. **Sending messages in copy mode**: recommend delivering anyway (`paste-buffer` gets through) without exiting copy mode for the user; the alternative is rejecting with "this pane is scrolling through history on the computer".
8. **When botmux is turned off**: on machines with `machines[].botmux: false`, should botmux's tmux sessions be listed by the tmux engine? Recommend still skipping them: typing into them would collide with botmux's own delivery, and replies would not go back to the Lark thread.
9. **Side by side**: support "herdr + tmux" in phase one? Recommend no, single choice first; if done later, consider changing the engine from an enum to "a set of checked hosts" instead of adding combinations like `herdr-tmux`, `tmux-orca`.
10. **Install global hooks for claude opened outside?**: Orca installs its own hooks into `~/.claude/settings.json`; Persona currently does not touch the user's global settings. Recommend continuing not to; sessions opened outside rely on title + screen reads, and dialogs are answered from screen reads.
